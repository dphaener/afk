package ui

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/termpane"
)

// --- hostChild write deadlines (fix cycle 1, blocker on W2.T2's Success
// Criterion: "both client→host write paths have write deadlines") ---
//
// hostChild is the LIVE client→host write path — every dashboard pane and
// every `afk attach` pane writes the socket through hostChild.writeFrame —
// so it, not internal/host's unused frameWriter, is where the incident's
// escalation lived: a host whose read side stops draining left writeFrame
// blocked forever while holding the mutex Close and detachReason also need.
// net.Pipe is the same peer-that-never-reads double client_test.go uses for
// frameWriter: a synchronous conn whose Write blocks until the peer reads,
// with full SetWriteDeadline support.

// shortHostFrameTimeout compresses hostFrameWriteTimeout for one test and
// restores the shipped value on cleanup, the same seam-compression shape as
// internal/host's resizeNudgeDelay and livenessTiming.
func shortHostFrameTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	restore := hostFrameWriteTimeout
	hostFrameWriteTimeout = d
	t.Cleanup(func() { hostFrameWriteTimeout = restore })
}

// stuckPeerChild returns a hostChild whose peer never reads, so every frame
// write blocks until a deadline fires. Both conns are closed on cleanup so a
// still-parked write cannot leak past the test.
func stuckPeerChild(t *testing.T) *hostChild {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() {
		_ = local.Close()
		_ = remote.Close()
	})
	return &hostChild{conn: local, exited: make(chan struct{})}
}

// TestHostChildWriteFrameTimesOutInsteadOfBlockingForever is the blocker's
// acceptance test: a Data write to a host that has stopped reading its socket
// returns a timeout error within the deadline instead of blocking forever,
// and the failure surfaces through the existing teardown path — the failed
// write closes the connection, which unblocks the read pump (termpane's
// reader in production) and settles Wait. Without the SetWriteDeadline this
// test's write never returns and the 2s guard fails it.
func TestHostChildWriteFrameTimesOutInsteadOfBlockingForever(t *testing.T) {
	shortHostFrameTimeout(t, 50*time.Millisecond)
	c := stuckPeerChild(t)

	// The read pump: in production termpane reads hostChild until error.
	// It is what turns a torn connection into a dead pane, so the timeout
	// only "surfaces" if this unblocks.
	readErr := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 64))
		readErr <- err
	}()

	writeErr := make(chan error, 1)
	go func() {
		_, err := c.Write([]byte("keystrokes for a host that stopped reading"))
		writeErr <- err
	}()

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatalf("a frame write to a peer that never reads must fail, got nil")
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("want a deadline error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("hostChild.Write blocked past its deadline: the socket write has no SetWriteDeadline")
	}

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatalf("the read pump should report the torn connection, got a nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("a timed-out write must close the connection so the read pump notices; it is still blocked")
	}

	select {
	case <-c.exited:
	case <-time.After(2 * time.Second):
		t.Fatalf("Wait never unblocked after the timed-out write tore the connection down")
	}

	// Nobody asked to leave here, so this is a drop. It guards the rule
	// Read's reason depends on: a failed live write closes the connection
	// without marking the pane closed by the user.
	if got := c.detachReason(); got != host.DetachDropped {
		t.Fatalf("detachReason after a timed-out write = %v, want %v", got, host.DetachDropped)
	}
}

// TestHostChildCloseDoesNotBlockOnAPeerThatStopsReading covers the second
// site the incident review named: Close sends a best-effort Detach frame on
// the same socket, so without a deadline a wedged host pins pane teardown —
// and with teardown pinned, quitting the dashboard hangs too.
func TestHostChildCloseDoesNotBlockOnAPeerThatStopsReading(t *testing.T) {
	shortHostFrameTimeout(t, 50*time.Millisecond)
	c := stuckPeerChild(t)

	done := make(chan error, 1)
	go func() { done <- c.Close() }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("hostChild.Close blocked forever on its best-effort Detach frame")
	}
}

// TestHostChildStateIsNotPinnedByABlockedSocketWrite pins the mutex split:
// the lock that serializes socket writes must not be the lock that guards
// closed/reason state, or one blocked write pins detachReason (and with it
// `afk attach`'s exit-code path) for the write's whole lifetime. The
// deadline here is deliberately long relative to the assertion window, so
// the state read is proven to complete WHILE the write is still parked.
func TestHostChildStateIsNotPinnedByABlockedSocketWrite(t *testing.T) {
	shortHostFrameTimeout(t, 1500*time.Millisecond)
	c := stuckPeerChild(t)

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		_, _ = c.Write([]byte("x"))
	}()
	// Give the write a moment to actually park inside the conn.
	time.Sleep(50 * time.Millisecond)

	got := make(chan host.DetachReason, 1)
	go func() { got <- c.detachReason() }()

	select {
	case r := <-got:
		if r != host.DetachUser {
			t.Fatalf("detachReason = %v, want the zero-reason default DetachUser", r)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("detachReason blocked behind an in-flight socket write — state must not share the write lock")
	}

	// Join the parked writer before returning: the cleanup that restores
	// hostFrameWriteTimeout must not race a goroutine that still reads it
	// (the same hazard livenessTiming's copy-per-host exists to avoid).
	_ = c.conn.Close()
	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("the blocked write never returned after its connection was closed")
	}
}

// heldWriteConn is a net.Conn whose Write does not return until hold closes.
// The bytes go out first, so the peer can act on them while the writer is
// still inside Write. That is the one thing a plain net.Pipe cannot be made
// to do on demand, and it is the whole of the detach race: the host's close
// arriving at the reader before the writer gets to its next statement.
type heldWriteConn struct {
	net.Conn
	hold <-chan struct{}
	// gaveUp records that hold never closed. Write still returns then, so a
	// broken test fails with a message instead of hanging.
	gaveUp atomic.Bool
}

func (c *heldWriteConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	select {
	case <-c.hold:
	case <-time.After(2 * time.Second):
		c.gaveUp.Store(true)
	}
	return n, err
}

// TestHostChildDetachReason pins which reason a pane reports for each way its
// connection can end. `afk attach` prints that reason and picks its exit code
// from it, so a wrong one tells a user who pressed ctrl+\ that their
// connection dropped (exit 13 where 0 was owed).
//
// Load-bearing rows:
//
//   - "user detach, host closes before the Detach write returns" is the bug
//     this test exists for. The host answers Detach by closing the connection
//     with no frame in front of the close, so the read pump can see that close
//     while Close is still inside its write. Left to chance over a net.Pipe
//     the ordering turns up about once in 2000 detaches, which is why
//     heldWriteConn forces it: Close's write is held until Read has returned.
//     On code that records DetachUser only after the write, this row reports
//     DetachDropped every time.
//   - "host closes with no detach asked for" keeps the fix honest. A fix that
//     reported DetachUser for every bare close would pass the row above and
//     hide every real drop, and the late Close that follows (the dashboard
//     closes a pane after it has ended) must not rewrite the reason either.
//   - The Stolen and Exit rows hold first-wins: a frame Read has already acted
//     on is not overwritten by the Close that tears the pane down afterwards.
//     Without them the session-ended and taken-over messages could both
//     degrade to "detached".
func TestHostChildDetachReason(t *testing.T) {
	tests := []struct {
		name string
		// peerSends are the frames the host side writes before it does
		// anything else.
		peerSends []host.MessageType
		// closeWhileReading has the user close the pane while the read pump is
		// still live, with the peer closing as soon as it has read Detach and
		// Close's write held until Read has seen that. When false, the peer
		// closes on its own and Close runs only after Read has finished, which
		// is the ordinary teardown of a pane that already ended.
		closeWhileReading bool
		want              host.DetachReason
	}{
		{
			name:              "user detach, host closes before the Detach write returns",
			closeWhileReading: true,
			want:              host.DetachUser,
		},
		{
			name: "host closes with no detach asked for",
			want: host.DetachDropped,
		},
		{
			name:      "Stolen read before Close",
			peerSends: []host.MessageType{host.Stolen},
			want:      host.DetachStolen,
		},
		{
			name:      "Exit read before Close",
			peerSends: []host.MessageType{host.Exit},
			want:      host.DetachExited,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shortHostFrameTimeout(t, time.Second)
			local, remote := net.Pipe()
			t.Cleanup(func() {
				_ = local.Close()
				_ = remote.Close()
			})

			readDone := make(chan struct{})
			conn := &heldWriteConn{Conn: local, hold: readDone}
			c := &hostChild{conn: conn, exited: make(chan struct{})}

			// The host side. It closes with no frame in front of the close,
			// which is what a real host does on Detach and on a drop alike.
			peerDone := make(chan struct{})
			go func() {
				defer close(peerDone)
				defer remote.Close()
				for _, typ := range tc.peerSends {
					if err := host.WriteFrame(remote, typ, nil); err != nil {
						return
					}
				}
				if !tc.closeWhileReading {
					return
				}
				for {
					typ, _, err := host.ReadFrame(remote)
					if err != nil || typ == host.Detach {
						return
					}
				}
			}()

			// The read pump: termpane reads a child until it errors.
			go func() {
				defer close(readDone)
				buf := make([]byte, 64)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()

			if !tc.closeWhileReading {
				select {
				case <-readDone:
				case <-time.After(2 * time.Second):
					t.Fatalf("the read pump never saw the connection end")
				}
			}

			closed := make(chan error, 1)
			go func() { closed <- c.Close() }()
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatalf("Close never returned")
			}
			if conn.gaveUp.Load() {
				t.Fatalf("the read pump never saw the host's close, so the ordering under test was not forced")
			}
			// Join both goroutines before the cleanup restores the write
			// timeout and before reading the reason they may still be writing.
			for name, done := range map[string]chan struct{}{"read pump": readDone, "peer": peerDone} {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatalf("the %s never finished", name)
				}
			}

			select {
			case <-c.exited:
			default:
				t.Fatalf("Wait is still blocked after the connection ended")
			}
			if got := c.detachReason(); got != tc.want {
				t.Fatalf("detachReason = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- per-surface wedge banner guidance (fix cycle 1, blocker on W6.T2) ---
//
// dialHostPane is shared by the dashboard and `afk attach`, but the two
// surfaces handle keys differently: the dashboard binds wedgeRecoveryKey on
// the session list (and forwards it to the child while a pane is focused),
// while attach forwards EVERY key except the two chords. A banner that says
// "press r" on the attach surface is an instruction to type a literal r into
// the wedged child, so each surface passes its own guidance.

// TestWedgeBannerHintsMatchEachSurfacesKeyHandling pins the two hints' texts
// against the key handling of the surface each is shown on: the dashboard's
// names the recovery key AND how to reach it from a focused pane (where the
// key otherwise forwards to the child — the banner is shown exactly there),
// and attach's advertises no in-place keypress at all, pointing at the
// dashboard instead.
func TestWedgeBannerHintsMatchEachSurfacesKeyHandling(t *testing.T) {
	dash := xansi.Strip(renderWedgeBanner(12*time.Minute, wedgeHintDashboard))
	if !strings.Contains(dash, "12m") {
		t.Fatalf("dashboard banner missing the wedge age: %q", dash)
	}
	// The banner is shown over a focused pane, where a bare r goes to the
	// child; the leader is the one key the dashboard keeps there, so naming
	// the leader chord is what makes the hint true in the place it is read.
	if !strings.Contains(dash, "ctrl+] "+wedgeRecoveryKey) {
		t.Fatalf("dashboard banner does not name the leader chord for the recovery key %q: %q", wedgeRecoveryKey, dash)
	}
	if strings.Contains(dash, `ctrl+\`) {
		t.Fatalf("dashboard banner names ctrl+\\, which the agent pane forwards to the child: %q", dash)
	}

	att := xansi.Strip(renderWedgeBanner(12*time.Minute, wedgeHintAttach))
	if !strings.Contains(att, "12m") {
		t.Fatalf("attach banner missing the wedge age: %q", att)
	}
	if strings.Contains(att, "press") {
		t.Fatalf("attach banner invites a keypress, but attach forwards every non-chord key to the wedged child: %q", att)
	}
	if !strings.Contains(att, "dashboard") {
		t.Fatalf("attach banner must point at the surface that CAN restart (the dashboard): %q", att)
	}
}

// TestAttachSurfaceWedgeBannerDoesNotAdvertiseAnInertKey is the attach-surface
// test the original W6.T2 change shipped without: the banner `afk attach`
// renders for a wedged session carries the attach guidance (via
// attachPaneConfig, the exact config AttachPane dials with), and the reason
// that matters is proven in the same test — pressing the dashboard's recovery
// key on this surface reaches the child as input, not any handler.
func TestAttachSurfaceWedgeBannerDoesNotAdvertiseAnInertKey(t *testing.T) {
	cfg, hint := attachPaneConfig("alpha", "/wt/alpha", 0)
	if cfg.Name != "alpha" || cfg.Worktree != "/wt/alpha" {
		t.Fatalf("attachPaneConfig built (%q, %q), want (alpha, /wt/alpha)", cfg.Name, cfg.Worktree)
	}
	if hint != wedgeHintAttach {
		t.Fatalf("afk attach must dial with the attach wedge hint, got %q", hint)
	}

	inner := newFakePane("frozen attach screen")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	wb := &wedgeBannerPane{
		paneHandle:  inner,
		hint:        hint,
		now:         func() time.Time { return now },
		wedgedSince: now.Add(-12 * time.Minute).Format(time.RFC3339),
	}
	m := attachModel{pane: wb, name: "alpha", mouseCapture: true, width: 120, height: 30, ready: true}

	frame := xansi.Strip(render(m))
	if !strings.Contains(frame, "session unresponsive") {
		t.Fatalf("attach must render the wedge banner for a wedged session:\n%s", frame)
	}
	banner := xansi.Strip(wb.Render())
	if strings.Contains(banner, fmt.Sprintf("press %q", wedgeRecoveryKey)) {
		t.Fatalf("the attach surface advertises pressing %q in place, but attach has no handler for it: %q",
			wedgeRecoveryKey, banner)
	}

	// The proof the old text was a lie on this surface: r forwards to the
	// wedged child like any other unreserved key.
	attachUpdate(t, m, keyRune([]rune(wedgeRecoveryKey)[0]))
	if len(inner.keys)+len(inner.texts) == 0 {
		t.Fatalf("%q on the attach surface should have been forwarded to the child — that is exactly why the banner must not advertise it",
			wedgeRecoveryKey)
	}
}

// --- wedge verdict staleness against a dead host (fix cycle 1, minor on
// W6.T1) ---

// TestWedgeBannerClearsWhenTheHostIsGone covers the leftover-host.json case:
// a SIGKILLed host never removes its host.json, so a wedged_since written
// before it died reads back forever. The verdict is only as live as the host
// that wrote it, so the banner clears once that host's pid stops answering —
// on the successful-read path AND on the read-error path once a pid is
// known. The anti-flap latch survives only where it is still sound: a read
// error with no host known to be dead.
func TestWedgeBannerClearsWhenTheHostIsGone(t *testing.T) {
	wedged := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)

	t.Run("leftover host.json from a dead host clears", func(t *testing.T) {
		p := &wedgeBannerPane{
			paneHandle: newFakePane("x"),
			now:        time.Now,
			readMeta: func(string, string) (host.Meta, error) {
				return host.Meta{PID: 4242, WedgedSince: wedged}, nil
			},
			hostAlive: func(int) bool { return false },
		}
		p.pollOnce()
		if _, ok := p.wedgeAge(); ok {
			t.Fatalf("a wedge verdict from a host that is no longer running is stale evidence and must not be shown")
		}
	})

	t.Run("live host keeps the verdict", func(t *testing.T) {
		p := &wedgeBannerPane{
			paneHandle: newFakePane("x"),
			now:        time.Now,
			readMeta: func(string, string) (host.Meta, error) {
				return host.Meta{PID: 4242, WedgedSince: wedged}, nil
			},
			hostAlive: func(int) bool { return true },
		}
		p.pollOnce()
		if _, ok := p.wedgeAge(); !ok {
			t.Fatalf("a wedge verdict from a live host must be shown")
		}
	})

	t.Run("read error clears once the last-known host is dead", func(t *testing.T) {
		p := &wedgeBannerPane{
			paneHandle:  newFakePane("x"),
			now:         time.Now,
			pid:         4242,
			wedgedSince: wedged,
			readMeta: func(string, string) (host.Meta, error) {
				return host.Meta{}, errors.New("boom")
			},
			hostAlive: func(int) bool { return false },
		}
		p.pollOnce()
		if _, ok := p.wedgeAge(); ok {
			t.Fatalf("host.json unreadable AND the host that wrote the verdict dead: the banner must not grow its age forever")
		}
	})

	t.Run("read error with no known host keeps the anti-flap latch", func(t *testing.T) {
		// The refinement of W6.T2's documented latch, not its removal: with
		// no pid ever learned there is no liveness question to ask, so a
		// transient read hiccup still must not flap the banner
		// (TestWedgeBannerPanePollOnceKeepsLastVerdictOnReadError pins the
		// same row through the original fixture).
		p := &wedgeBannerPane{
			paneHandle:  newFakePane("x"),
			now:         time.Now,
			wedgedSince: wedged,
			readMeta: func(string, string) (host.Meta, error) {
				return host.Meta{}, errors.New("boom")
			},
			hostAlive: func(int) bool { return false },
		}
		p.pollOnce()
		if _, ok := p.wedgeAge(); !ok {
			t.Fatalf("a transient read error with no host known to be dead must keep the last verdict")
		}
	})
}

// TestShippedHostFrameWriteTimeoutIsTheDocumentedOne pins the production
// value the way internal/host pins restartLadder and its liveness schedule:
// every test above compresses hostFrameWriteTimeout through
// shortHostFrameTimeout first, so a shipped value short enough to drop a
// healthy-but-busy host (or long enough to stop bounding anything) would
// never turn this suite red on its own. 5s matches internal/host's
// clientWriteTimeout and attachWriteTimeout: a draining peer completes a
// frame write in microseconds, so the deadline only ever fires when the
// host's read side has genuinely stopped.
func TestShippedHostFrameWriteTimeoutIsTheDocumentedOne(t *testing.T) {
	if hostFrameWriteTimeout != 5*time.Second {
		t.Errorf("hostFrameWriteTimeout = %v, want 5s, the same bound as the host side's clientWriteTimeout", hostFrameWriteTimeout)
	}
}

// TestWedgeBannerPaneCursor covers wedgeBannerPane.Cursor(): the wedged row
// is load-bearing. Render() prepends a banner above the emulator screen only
// while wedged (see Render() above), which shifts every emulator row down by
// the banner's height on screen — but the embedded pane's Cursor() still
// reports the UNSHIFTED emulator row. Passing that through while wedged would
// place the terminal cursor a row too high, and the child is unresponsive
// anyway (that is what "wedged" means), so a stale position is worse than no
// cursor at all. Not-wedged has no such shift, so it passes through.
func TestWedgeBannerPaneCursor(t *testing.T) {
	want := termpane.CursorState{X: 3, Y: 2, Visible: true, Shape: termpane.CursorBar, Blink: true}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		wedgedSince string // "" means not wedged
		want        termpane.CursorState
	}{
		{
			name:        "not wedged passes the embedded cursor through",
			wedgedSince: "",
			want:        want,
		},
		{
			// Load-bearing: the embedded pane's cursor is still visible and
			// positioned in the emulator's frame, but the banner above it on
			// screen makes that position wrong, so Cursor() must hide it
			// rather than report a stale row.
			name:        "wedged hides the cursor",
			wedgedSince: now.Add(-time.Minute).Format(time.RFC3339),
			want:        termpane.CursorState{Visible: false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := newFakePane("x")
			inner.cursor = want
			p := &wedgeBannerPane{
				paneHandle:  inner,
				now:         func() time.Time { return now },
				wedgedSince: tt.wedgedSince,
			}
			if got := p.Cursor(); got != tt.want {
				t.Fatalf("Cursor() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
