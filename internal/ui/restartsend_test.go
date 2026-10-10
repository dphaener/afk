package ui

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/host"
)

// restartsend_test.go covers the dashboard's actual restart trigger —
// defaultSendRestart and the sendRestartAndAwaitResult core under it — from
// both directions, because fix cycle 2's blocker lived exactly here: the
// trigger used to write one Restart frame and hang up without reading a
// reply, which made the host's refusal (restart.go's sendRestartResult)
// structurally unreachable and reported every refused restart as requested.
//
// The protocol matrix runs over net.Pipe (synchronous, deadline-capable — the
// same double client_test.go uses), and the two integration tests run the
// trigger against a real host.Serve on a real socket, refusing and then
// succeeding, so reverting the trigger to fire-and-ignore turns this file red
// in four different ways.

// shortRestartReplyTimeout compresses the reply window for one test,
// restoring it afterwards. Callers must not leave a goroutine reading the
// var beyond the test body (the same -race hazard hostpane_test.go's
// shortHostFrameTimeout documents).
func shortRestartReplyTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	restore := restartReplyTimeout
	restartReplyTimeout = d
	t.Cleanup(func() { restartReplyTimeout = restore })
}

// TestShippedRestartWireTimeoutsAreTheDocumentedOnes pins the two timing
// vars at their production values, the way internal/host pins restartLadder:
// every behavioural test in this file compresses them first, so without this
// pin a regression (say, a reply window shorter than the ladder it must
// outwait, which would misreport every slow-but-successful restart as merely
// requested) would leave the whole suite green.
func TestShippedRestartWireTimeoutsAreTheDocumentedOnes(t *testing.T) {
	if restartReplyTimeout != 15*time.Second {
		t.Errorf("restartReplyTimeout = %v, want 15s: it must comfortably outlast the host's full shipped signal ladder (~9s) plus the drain and respawn", restartReplyTimeout)
	}
	if restartReplyTimeout <= 9*time.Second {
		t.Errorf("restartReplyTimeout = %v does not cover the shipped ladder's ~9s worst case, so a successful slow restart would be reported as outcome-unknown", restartReplyTimeout)
	}
}

// pipePeer runs f over the far end of a net.Pipe and reports its completion,
// so tests can both script the host side and join it before returning.
func pipePeer(t *testing.T, f func(conn net.Conn)) net.Conn {
	t.Helper()
	local, remote := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(remote)
	}()
	t.Cleanup(func() {
		_ = local.Close()
		_ = remote.Close()
		<-done
	})
	return local
}

func TestSendRestartAwaitResultReadsTheHostsAnswer(t *testing.T) {
	t.Run("a refusal reaches the caller as its reason", func(t *testing.T) {
		conn := pipePeer(t, func(peer net.Conn) {
			if typ, _, err := host.ReadFrame(peer); err != nil || typ != host.Restart {
				t.Errorf("host side read (%v, err %v), want a Restart frame", typ, err)
				return
			}
			// The human-readable Data line panes render arrives first, as it
			// does from the real host; the trigger must skip it rather than
			// mistake it for an answer.
			_ = host.WriteFrame(peer, host.Data, []byte("\r\n[afk] restart failed: status.json is not usable (missing)\r\n"))
			_ = host.WriteFrame(peer, host.RestartResult, []byte("status.json is not usable (missing)"))
		})

		restarted, err := sendRestartAndAwaitResult(conn)
		if restarted {
			t.Fatalf("a refused restart reported restarted=true")
		}
		if err == nil || !strings.Contains(err.Error(), "not usable") {
			t.Fatalf("err = %v, want the host's refusal reason", err)
		}
	})

	t.Run("an empty result confirms the respawn", func(t *testing.T) {
		conn := pipePeer(t, func(peer net.Conn) {
			if typ, _, err := host.ReadFrame(peer); err != nil || typ != host.Restart {
				t.Errorf("host side read (%v, err %v), want a Restart frame", typ, err)
				return
			}
			_ = host.WriteFrame(peer, host.Data, []byte("resumed child output"))
			_ = host.WriteFrame(peer, host.RestartResult, nil)
		})

		restarted, err := sendRestartAndAwaitResult(conn)
		if err != nil {
			t.Fatalf("err = %v, want nil for a confirmed restart", err)
		}
		if !restarted {
			t.Fatalf("restarted = false for an empty (success) RestartResult")
		}
	})

	t.Run("a host that never answers degrades to requested", func(t *testing.T) {
		shortRestartReplyTimeout(t, 100*time.Millisecond)
		conn := pipePeer(t, func(peer net.Conn) {
			if typ, _, err := host.ReadFrame(peer); err != nil || typ != host.Restart {
				t.Errorf("host side read (%v, err %v), want a Restart frame", typ, err)
				return
			}
			// Idle without answering until pipePeer's cleanup closes the
			// pipe under this read; a bare channel wait here would deadlock
			// against that same cleanup's join.
			_, _, _ = host.ReadFrame(peer)
		})

		start := time.Now()
		restarted, err := sendRestartAndAwaitResult(conn)
		if err != nil || restarted {
			t.Fatalf("(restarted=%v, err=%v), want (false, nil): an old host that sends no RestartResult still delivered the frame", restarted, err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("waiting on a silent host took %v, want the compressed reply window", elapsed)
		}
	})

	t.Run("a host that hangs up without answering degrades to requested", func(t *testing.T) {
		conn := pipePeer(t, func(peer net.Conn) {
			if typ, _, err := host.ReadFrame(peer); err != nil || typ != host.Restart {
				t.Errorf("host side read (%v, err %v), want a Restart frame", typ, err)
				return
			}
			_ = peer.Close()
		})

		restarted, err := sendRestartAndAwaitResult(conn)
		if err != nil || restarted {
			t.Fatalf("(restarted=%v, err=%v), want (false, nil): the frame was delivered before the host went away", restarted, err)
		}
	})
}

// TestSendRestartWriteDeadlineFires closes the third client→host write path
// (fix cycle 2's second major): the trigger's freshly dialed connection had
// no SetWriteDeadline anywhere, so a host that stopped reading its socket
// parked the restart Cmd's goroutine forever. Same net.Pipe never-reading
// peer, same os.ErrDeadlineExceeded sentinel as hostpane_test.go's
// writeFrame deadline test.
func TestSendRestartWriteDeadlineFires(t *testing.T) {
	shortHostFrameTimeout(t, 50*time.Millisecond)
	local, remote := net.Pipe()
	t.Cleanup(func() {
		_ = local.Close()
		_ = remote.Close()
	})

	start := time.Now()
	restarted, err := sendRestartAndAwaitResult(local)
	if restarted {
		t.Fatalf("restarted = true against a host that never read the request")
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v, want os.ErrDeadlineExceeded from the write deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the blocked write returned after %v, want the compressed 50ms deadline", elapsed)
	}
}

// --- integration: defaultSendRestart against a real host -------------------

// restartHostFixture is a real host.Serve on a real socket, hosting a silent
// /bin/sh child that dies on SIGINT — the shape defaultSendRestart meets in
// production, minus the wedge (the trigger does not care why it was asked).
type restartHostFixture struct {
	worktree string
	name     string
	cfg      host.ServeConfig
	stopped  chan struct{}
	serveErr error
}

func startRestartHost(t *testing.T) *restartHostFixture {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available to run a stub child")
	}

	worktree := t.TempDir()
	name := "sess"
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	f := &restartHostFixture{
		worktree: worktree,
		name:     name,
		stopped:  make(chan struct{}),
	}
	f.cfg = host.ServeConfig{
		Name:     name,
		Worktree: worktree,
		Socket:   filepath.Join(e2eSocketDir(t), "r.sock"),
		MetaPath: filepath.Join(dir, "host.json"),
		LogPath:  filepath.Join(dir, "host.log"),
		Argv:     []string{"/bin/sh", "-c", "while :; do sleep 0.05; done"},
	}
	go func() {
		f.serveErr = host.Serve(f.cfg)
		close(f.stopped)
	}()
	t.Cleanup(f.stopQuietly)

	deadline := time.Now().Add(e2eBudget)
	for {
		select {
		case <-f.stopped:
			t.Fatalf("host.Serve failed to start: %v (under the sandbox this is the blocked unix bind(); run unsandboxed)", f.serveErr)
		default:
		}
		if m, err := host.ReadMeta(worktree, name); err == nil && m.PID == os.Getpid() {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("host.json never appeared within %s", e2eBudget)
		}
		time.Sleep(e2ePollStep)
	}
}

func (f *restartHostFixture) stopQuietly() {
	select {
	case <-f.stopped:
		return
	default:
	}
	if conn, err := net.Dial("unix", f.cfg.Socket); err == nil {
		_ = host.WriteFrame(conn, host.Exit, nil)
		_ = conn.Close()
	}
	select {
	case <-f.stopped:
	case <-time.After(e2eBudget):
	}
}

// seedStatus writes the nine-key status.json the host's restart reads its
// session id from (status-schema.md §2: plan_name must equal the directory
// name, and a non-planning phase demands a non-empty session_id).
func (f *restartHostFixture) seedStatus(t *testing.T, sessionID string) {
	t.Helper()
	body := fmt.Sprintf(`{
  "session_id": %q,
  "plan_name": %q,
  "phase": "executing",
  "branch": "main",
  "base_branch": "main",
  "loop_count": 1,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": %q
}
`, sessionID, f.name, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(f.worktree, ".afk", f.name, "status.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing status.json: %v", err)
	}
}

// installRecordingClaude puts a `claude` first on PATH that records its argv
// and then idles silently, so the respawn's success is attributable to the
// restart rather than to child output.
func (f *restartHostFixture) installRecordingClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "claude.args")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %s\nwhile :; do sleep 0.05; done\n", argsPath)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("installing the fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

// TestDefaultSendRestartSurfacesARefusalInTheDashboard is the blocker's
// missing test, end to end: a real host with no usable status.json refuses
// the restart, and that refusal must arrive in the dashboard's status line —
// not vanish into a closed socket while the row stays WEDGED unexplained.
// Everything between the keypress's Cmd and the status text is production
// code: defaultSendRestart, the real socket, restart.go's refusal, and the
// restartChildRequestedMsg handler.
func TestDefaultSendRestartSurfacesARefusalInTheDashboard(t *testing.T) {
	f := startRestartHost(t)
	// No status.json is seeded: the exact refusal W4.T1 defines.

	m := testModel(wedgedSess(f.name, 12*time.Minute))
	m.deps.SendRestart = defaultSendRestart

	msg, ok := m.restartChildCmd(f.name, f.worktree)().(restartChildRequestedMsg)
	if !ok {
		t.Fatalf("restartChildCmd produced the wrong message type")
	}
	if msg.err == nil {
		t.Fatalf("a refused restart came back with no error: the trigger never read the host's answer")
	}
	if !strings.Contains(msg.err.Error(), "not usable") {
		t.Errorf("err = %v, want the host's refusal reason (status.json not usable)", msg.err)
	}
	if msg.restarted {
		t.Errorf("a refused restart reported restarted=true")
	}

	got, _ := update(t, m, msg)
	if !strings.Contains(got.status, "restart failed") || !strings.Contains(got.status, f.name) {
		t.Errorf("status = %q, want a visible failure naming the session", got.status)
	}
	if !strings.Contains(got.status, "not usable") {
		t.Errorf("status = %q, want the host's reason so the user knows WHY the row stays wedged", got.status)
	}
}

// TestDefaultSendRestartConfirmsASuccessfulRestart is the success half of the
// same trigger, against the same real host: the empty RestartResult must come
// back as restarted=true, and the child the host respawned must be running
// production's exact resumed argv.
func TestDefaultSendRestartConfirmsASuccessfulRestart(t *testing.T) {
	f := startRestartHost(t)
	const sessionID = "11111111-2222-3333-4444-555555555555"
	f.seedStatus(t, sessionID)
	argsPath := f.installRecordingClaude(t)

	restarted, err := defaultSendRestart(f.worktree, f.name)
	if err != nil {
		t.Fatalf("defaultSendRestart: %v", err)
	}
	if !restarted {
		t.Fatalf("the host respawned the child but the trigger reported the outcome as unknown")
	}

	deadline := time.Now().Add(e2eBudget)
	for {
		if data, rerr := os.ReadFile(argsPath); rerr == nil && strings.TrimSpace(string(data)) == "--resume "+sessionID {
			break
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(argsPath)
			t.Fatalf("the resumed child was run with %q, want %q", strings.TrimSpace(string(data)), "--resume "+sessionID)
		}
		time.Sleep(e2ePollStep)
	}

	m := testModel(wedgedSess(f.name, 12*time.Minute))
	got, _ := update(t, m, restartChildRequestedMsg{name: f.name, restarted: restarted})
	if !strings.Contains(got.status, "restarted") || !strings.Contains(got.status, f.name) {
		t.Errorf("status = %q, want a confirmed-restart report naming the session", got.status)
	}
}
