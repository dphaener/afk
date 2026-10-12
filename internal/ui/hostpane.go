package ui

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/termpane"
)

type hostPaneConfig struct {
	Name     string
	Worktree string
	Cols     int
	Rows     int

	// Wait is the retry-dial window; host.DefaultAttachWait when non-positive.
	// The dashboard leaves it zero and takes the default — the window it needs
	// is the one that absorbs a just-spawned host's startup.
	Wait time.Duration
}

// openHostPane dials the session's host and returns a pane rendering it.
//
// The host owns the PTY and outlives every dashboard, so the pane drives it
// over the frame protocol instead of spawning a child of its own: a hostChild
// satisfies termpane.Child, which keeps the emulator upstream unaware it is
// reading a socket rather than a PTY master.
//
// The Resize after Start is load-bearing, not redundant with Config's geometry:
// it reaches the session as SIGWINCH, which is what makes a full-screen child
// repaint. Without it a pane opened onto an idle session stays blank until that
// session next writes on its own.
func openHostPane(cfg hostPaneConfig) (paneHandle, error) {
	pane, _, err := dialHostPane(cfg, wedgeHintDashboard)
	if err != nil {
		return nil, err
	}
	return pane, nil
}

// dialHostPane is openHostPane plus the hostChild behind the pane, for the
// caller that needs the detach reason directly (attach.go's single-pane
// program, which owes its process an exit code). The dashboard reaches the
// same reason through the returned pane instead: paneHandle does not carry
// it, but the wedgeBannerPane every dial returns does, as detachReasoner.
//
// wedgeHint is the recovery guidance the wedge banner shows on THIS surface.
// A required parameter rather than a shared default, because the two callers
// handle keys differently — the dashboard binds wedgeRecoveryKey, attach
// forwards every non-chord key to the child — and a banner naming a key the
// surface does not handle instructs the user to type that key into the
// wedged child.
func dialHostPane(cfg hostPaneConfig, wedgeHint string) (paneHandle, *hostChild, error) {
	socket, err := hostSocketPath(cfg.Worktree, cfg.Name)
	if err != nil {
		return nil, nil, err
	}
	wait := cfg.Wait
	if wait <= 0 {
		wait = host.DefaultAttachWait
	}
	conn, err := host.Dial(socket, wait)
	if err != nil {
		return nil, nil, fmt.Errorf("attaching to the %s session: %w", cfg.Name, err)
	}
	child := &hostChild{conn: conn, exited: make(chan struct{})}
	pane, err := termpane.Start(
		termpane.Config{
			// Argv is unused — StartChild ignores it — but Start rejects an
			// empty one, and naming the equivalent command keeps the value
			// honest for anyone reading a panic or a log line.
			Argv: []string{"afk", "attach", cfg.Name},
			Cols: cfg.Cols,
			Rows: cfg.Rows,
		},
		termpane.Deps{StartChild: func(termpane.Config) (termpane.Child, error) { return child, nil }},
	)
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	if err := pane.Resize(cfg.Cols, cfg.Rows); err != nil {
		_ = pane.Close()
		return nil, nil, err
	}
	wrapped := newWedgeBannerPane(pane, cfg.Worktree, cfg.Name, wedgeHint)
	// Set after construction rather than through the constructor so the
	// tests that build a wedgeBannerPane over a fake keep their shape; the
	// poll goroutine never reads it, and detachReason is only called once
	// the pane has travelled through a message, so no race is opened.
	wrapped.child = child
	return wrapped, child, nil
}

// hostSocketPath prefers the path the host recorded over the derived one, so a
// host listening somewhere unexpected is still reachable.
func hostSocketPath(worktree, name string) (string, error) {
	meta, err := host.ReadMeta(worktree, name)
	if err != nil {
		return "", fmt.Errorf("reading the host record for %s: %w", name, err)
	}
	if meta.Socket != "" {
		return meta.Socket, nil
	}
	return host.SocketPath(worktree, name)
}

// wedgeRecoveryKey names the dashboard's one restart key (Model.restartSelected):
// it restarts a wedged session's child after a confirmation, and respawns a
// stopped session's host straight away. Named once, here, so the banner text
// this file draws and the key the dashboard binds can never name two
// different keys.
const wedgeRecoveryKey = "r"

// wedgeHintDashboard and wedgeHintAttach are the per-surface halves of the
// wedge banner: the same "session unresponsive" fact, but recovery guidance
// matched to what each surface actually does with keys. The dashboard binds
// wedgeRecoveryKey on the session list and the focused overview, and forwards
// it to the child while a pane is focused — so its hint names the key AND the
// chord that leaves the focused pane first. `afk attach` forwards every
// non-chord key to the child and handles no restart at all, so its hint must
// not invite any in-place keypress; the restart lives in the dashboard.
var wedgeHintDashboard = fmt.Sprintf(`press %q to restart (from a focused pane: ctrl+\ first)`, wedgeRecoveryKey)

const wedgeHintAttach = `detach (ctrl+\) and restart it from the dashboard`

// hostFrameWriteTimeout bounds one frame write from a pane to its session's
// host — the live client→host write path, the counterpart of the host's
// clientWriteTimeout and internal/host's attachWriteTimeout on this same
// socket. A host draining its socket normally completes a write in
// microseconds, so 5s only ever fires when the host's read side has stopped
// — exactly the condition under which an undeadlined write here would turn
// one wedged pane into a frozen dashboard (Resize and Close both write this
// socket from Update-adjacent paths). A package-level var, not a const, so
// tests can compress it — the same seam shape as internal/host's
// resizeNudgeDelay and livenessTiming.
var hostFrameWriteTimeout = 5 * time.Second

// wedgeBannerPollInterval is how often wedgeBannerPane re-checks host.json
// for the liveness monitor's wedge verdict. It runs on its own goroutine,
// off Render entirely: Render is called once per Bubble Tea View — many
// times a second while the user is interacting — and a file read on every
// call would be exactly the render-path I/O the plan forbids.
const wedgeBannerPollInterval = time.Second

// wedgeBannerPane wraps a paneHandle so the one call site that reaches the
// emulator's screen — paneHandle.Render(), called from views.go's
// renderPane, which this task's Files list does not permit touching — draws
// a banner over the frozen screen once the host's liveness monitor
// (internal/host/liveness.go) has marked the session wedged.
//
// The verdict is polled from host.json rather than pushed in, because
// paneHandle (model.go, out of scope) has no method for the dashboard to
// hand this wrapper a fresh verdict each tick, and the host does not send a
// frame when it marks a session wedged (only a restart failure produces a
// Data frame — see restart.go's notifyClient). host.Meta.WedgedSince is the
// same value the row indicator (W6.T1, via internal/discover) already
// reads.
type wedgeBannerPane struct {
	paneHandle

	worktree, name string
	hint           string // per-surface recovery guidance; see wedgeHintDashboard/wedgeHintAttach
	now            func() time.Time
	readMeta       func(worktree, name string) (host.Meta, error)
	hostAlive      func(pid int) bool

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	mu          sync.Mutex
	wedgedSince string // RFC3339 from host.Meta; "" means not wedged, or unknown.
	pid         int    // the host pid host.json last named; 0 until a read succeeds.

	// child is the connection behind the wrapped pane, kept only so
	// detachReason can be answered — the emulator upstream collapses a
	// steal and a real exit into the same io.EOF, so the wrapped pane's
	// Done/Err cannot tell them apart and the fact has to be fetched from
	// where it was preserved. nil when the pane is built over a fake.
	child *hostChild
}

// detachReasoner is the optional surface a pane exposes when it can say WHY
// its connection ended. paneHandle deliberately does not carry this (it
// mirrors termpane.Pane, whose PTY children have no such notion), so
// paneEventCmd type-asserts for it instead: a handle without it reports the
// zero reason, which the dashboard treats as a real exit — the safe default,
// because the only reason that changes behaviour is DetachStolen, and
// guessing "stolen" would leave a genuinely dead host reading as alive.
type detachReasoner interface {
	detachReason() host.DetachReason
}

// detachReason reports why the wrapped connection ended, settled once the
// pane's Done has fired (hostChild.markExited records the reason before it
// unblocks Wait, and termpane closes Done only after Wait returns). Zero when
// there is no connection behind this pane to ask.
func (p *wedgeBannerPane) detachReason() host.DetachReason {
	if p.child == nil {
		return 0
	}
	return p.child.detachReason()
}

// newWedgeBannerPane wraps inner and starts the background poll against the
// session's host.json (worktree/name locate it exactly as hostSocketPath
// does). The first poll runs synchronously so an already-wedged session's
// very first Render() shows the banner rather than waiting a full interval.
func newWedgeBannerPane(inner paneHandle, worktree, name, hint string) *wedgeBannerPane {
	p := &wedgeBannerPane{
		paneHandle: inner,
		worktree:   worktree,
		name:       name,
		hint:       hint,
		now:        time.Now,
		readMeta:   host.ReadMeta,
		hostAlive:  host.Alive,
		stop:       make(chan struct{}),
	}
	p.pollOnce()
	p.wg.Add(1)
	go p.pollLoop()
	return p
}

func (p *wedgeBannerPane) pollLoop() {
	defer p.wg.Done()
	t := time.NewTicker(wedgeBannerPollInterval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.pollOnce()
		}
	}
}

// pollOnce is the only place this type touches disk. Two staleness rules
// govern what it records, both about the same fact: a wedge verdict is only
// as live as the host that wrote it.
//
// On a successful read, a wedged_since naming a host whose pid no longer
// answers (host.Alive's signal-0, the same advisory check discovery uses) is
// treated as not-wedged: a SIGKILLed host never removes its host.json, so
// without this check a leftover file would show WEDGED with a growing age
// forever.
//
// On a read error, the last-known verdict is kept ONLY while the host that
// produced it is not known to be dead — internal/host's contract treats a
// missing/unparseable file as stale, never as a fact to act on, and clearing
// the banner on a transient read failure would make it flap. But once the
// last-known pid stops answering, keeping the verdict is the same
// grows-forever defect as the leftover-file case, so it clears. With no pid
// ever learned there is no liveness question to ask, and the anti-flap latch
// stands.
func (p *wedgeBannerPane) pollOnce() {
	meta, err := p.readMeta(p.worktree, p.name)
	if err != nil {
		p.mu.Lock()
		if p.pid > 0 && !p.hostAlive(p.pid) {
			p.wedgedSince = ""
		}
		p.mu.Unlock()
		return
	}
	wedged := meta.WedgedSince
	if wedged != "" && meta.PID > 0 && !p.hostAlive(meta.PID) {
		wedged = ""
	}
	p.mu.Lock()
	p.wedgedSince = wedged
	p.pid = meta.PID
	p.mu.Unlock()
}

// wedgeAge reports how long the session has been wedged, and whether it is
// wedged at all. An empty or unparseable wedgedSince (the latter should
// never happen from a real host.json, but Render must not panic on it) both
// report ok=false, the same "absent is not an error" treatment
// discover.SessionState.WedgedSince already gets in views.go's showsWedged.
func (p *wedgeBannerPane) wedgeAge() (time.Duration, bool) {
	p.mu.Lock()
	since := p.wedgedSince
	p.mu.Unlock()
	if since == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return 0, false
	}
	age := p.now().Sub(t)
	if age < 0 {
		age = 0
	}
	return age, true
}

// Render draws the wedge banner above the emulator's current screen when
// wedged, and passes the screen through untouched otherwise. The embedded
// screen is read first and always included in full — the banner is
// additional content, never a replacement, so the frozen screen the plan
// calls diagnostic survives in the output whether or not the box that draws
// it (renderPane) has room to show all of it.
func (p *wedgeBannerPane) Render() string {
	screen := p.paneHandle.Render()
	age, ok := p.wedgeAge()
	if !ok {
		return screen
	}
	return lipgloss.JoinVertical(lipgloss.Left, renderWedgeBanner(age, p.hint), screen)
}

// renderWedgeBanner is the pure formatting half of the banner: the message
// missing during the incident this wave fixes — that the child has stopped
// responding, for how long, and how to recover it FROM THIS SURFACE (hint is
// per-surface; see wedgeHintDashboard/wedgeHintAttach for why one shared
// text was wrong).
func renderWedgeBanner(age time.Duration, hint string) string {
	return theme.errorText.Render(fmt.Sprintf(
		"⚠ session unresponsive for %s — %s",
		durationShort(age), hint,
	))
}

// Cursor reports the embedded pane's cursor unchanged when not wedged, and
// hides it while wedged. Render() prepends the banner ABOVE the emulator
// screen only while wedged, shifting every screen row down by the banner's
// height — but the embedded Cursor() still reports the unshifted emulator
// row, using the same wedgeAge() check Render() uses. Reporting that stale
// row rather than none would draw the terminal cursor a row too high; the
// child is unresponsive either way, so no cursor is the safer degradation.
func (p *wedgeBannerPane) Cursor() termpane.CursorState {
	if _, ok := p.wedgeAge(); ok {
		return termpane.CursorState{Visible: false}
	}
	return p.paneHandle.Cursor()
}

// Close stops the poll goroutine before delegating to the wrapped pane's own
// Close (hostChild.Close, which detaches without ending the session) — a
// wedgeBannerPane that outlived its pane would leak a goroutine forever.
func (p *wedgeBannerPane) Close() error {
	p.stopOnce.Do(func() { close(p.stop) })
	p.wg.Wait()
	return p.paneHandle.Close()
}

// hostChild presents a host connection as a termpane.Child.
//
// Writes are serialized by writeMu because the emulator's response pump and
// the UI's input path both send frames, and WriteFrame makes two Write calls:
// without the lock a header from one goroutine could land ahead of another's
// payload and desynchronize the stream permanently.
//
// writeMu is deliberately NOT mu. mu guards state (closed, reason) and is
// never held across socket I/O: the incident review found that holding one
// mutex across an undeadlined socket write meant a host that stopped reading
// pinned not just the writer but Close and detachReason too — the exact
// mechanism by which one wedged pane escalates into a frozen dashboard.
// writeMu is held across the write on purpose (that IS the single-writer
// discipline), but every hold is bounded by hostFrameWriteTimeout.
type hostChild struct {
	conn net.Conn

	writeMu sync.Mutex

	mu sync.Mutex
	// closed means the user asked to leave: only Close sets it, and it sets
	// it before writing the Detach frame. Read relies on both halves of that
	// to name the reason for a connection that then closes (see Close). A
	// failed live write closes the connection WITHOUT setting it, so that
	// case still reads as a drop.
	closed bool

	// exited closes when the connection ends, from whichever side notices
	// first — Read seeing EOF or a control frame, or Close dropping the
	// connection. It is what unblocks Wait.
	exited   chan struct{}
	exitOnce sync.Once

	// reason records WHY the connection ended, guarded by mu. Read collapses
	// Stolen and Exit into io.EOF because that is what a terminal child looks
	// like when it goes away, so the distinction has to be preserved here or it
	// is lost — and `afk attach` owes its process the exit code that
	// distinction selects. The zero value means nothing has ended the
	// connection yet, which detachReason reads as a user detach.
	reason host.DetachReason

	// pending holds the unread tail of the current Data frame. A frame can
	// exceed the caller's buffer, and io.Reader is free to return short reads,
	// so the remainder has to survive between Read calls.
	pending []byte
}

// Read returns session output, unwrapping Data frames. Stolen and Exit are
// acted on rather than passed through: both mean this pane is finished, which
// surfaces as io.EOF — the same signal a PTY gives once its child is gone.
func (c *hostChild) Read(p []byte) (int, error) {
	for {
		if len(c.pending) > 0 {
			n := copy(p, c.pending)
			c.pending = c.pending[n:]
			return n, nil
		}
		typ, payload, err := host.ReadFrame(c.conn)
		if err != nil {
			// No Exit frame came first, so the session never said it ended.
			// Which of two things happened depends on whether the user has
			// asked to leave. If Close has started, this is the host's answer
			// to our own Detach frame: it closes the connection with nothing
			// in front of the close, and that can be read here before Close
			// gets back from its write. Calling that a drop told a user who
			// pressed ctrl+\ that their connection dropped. Otherwise the
			// host dropped this client, or died too abruptly to say so, which
			// is not proof of death either (see host.DetachDropped).
			reason := host.DetachDropped
			if c.isClosed() {
				reason = host.DetachUser
			}
			c.markExited(reason)
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
				return 0, io.EOF
			}
			return 0, err
		}
		switch typ {
		case host.Data:
			c.pending = payload
		case host.Stolen:
			c.markExited(host.DetachStolen)
			return 0, io.EOF
		case host.Exit:
			c.markExited(host.DetachExited)
			return 0, io.EOF
		}
	}
}

func (c *hostChild) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := c.writeFrame(host.Data, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *hostChild) Resize(cols, rows int) error {
	return c.writeFrame(host.Resize, host.EncodeResize(clampPaneDim(rows), clampPaneDim(cols)))
}

// Kill detaches; it deliberately does NOT end the session.
//
// termpane calls Kill from Pane.Close and from its panic-containment path, so
// for a PTY child "kill" and "tear the pane down" are the same act. For a host
// child they must not be: the host is a separate process the user expects to
// find still running afterwards. Sending host.Exit here — the frame that tells
// the host to terminate its child — would destroy a session every time a pane
// closed. Ending a session is the session's own business (its child exiting) or
// `afk clean`'s; no pane teardown path reaches host.Exit.
func (c *hostChild) Kill() error {
	return c.Close()
}

// Wait blocks until the connection ends, and never reports that as an error:
// from this side a detach and a session exit are indistinguishable, and neither
// is a failure worth surfacing to the dashboard.
func (c *hostChild) Wait() error {
	<-c.exited
	return nil
}

// Close DETACHES, leaving the session running — a host is a separate process
// that outlives both pane and dashboard. Detach goes out first, best-effort
// and deadline-bounded like every other write on this connection, so the host
// frees this client slot immediately rather than discovering a dead
// connection on its next write — and so a host that has stopped reading
// cannot pin pane teardown (and with it, quitting the dashboard).
//
// closed is set BEFORE the Detach frame is written, and that order is what
// makes the reported reason right. The host answers Detach by closing the
// connection, so Read can see that close while this function is still inside
// its write. Read consults closed to tell "the user left" from "the host
// dropped us"; were the flag set after the write, or the markExited below the
// only record of the user's intent, Read would win the race and report a
// user detach as a dropped connection (`afk attach: connection dropped`,
// exit 13). TestHostChildDetachReason forces that ordering.
//
// markExited is deliberately NOT moved ahead of the write to get the same
// effect: it also unblocks Wait, which would then report the connection as
// ended before the Detach frame had been given its chance to go out.
func (c *hostChild) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.writeMu.Lock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(hostFrameWriteTimeout))
	_ = host.WriteFrame(c.conn, host.Detach, nil)
	c.writeMu.Unlock()

	c.markExited(host.DetachUser)
	return c.conn.Close()
}

// detachReason reports why the connection ended, once Wait has returned.
func (c *hostChild) detachReason() host.DetachReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reason == 0 {
		return host.DetachUser
	}
	return c.reason
}

// writeFrame is the live client→host write path — every keystroke, resize
// and mouse report the dashboard or `afk attach` sends travels through
// here. The deadline is what keeps a host that has stopped draining its
// socket from parking this goroutine forever (5s bounds the worst case; see
// hostFrameWriteTimeout). On any write error the connection is closed here,
// mirroring internal/host's frameWriter: a torn frame stream is beyond
// salvage, and closing is what surfaces the failure — it unblocks the read
// pump on this same connection, the one path that already turns a dead
// connection into a dead pane (Read's error → markExited → Wait returns).
func (c *hostChild) writeFrame(typ host.MessageType, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isClosed() {
		return net.ErrClosed
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(hostFrameWriteTimeout))
	if err := host.WriteFrame(c.conn, typ, payload); err != nil {
		_ = c.conn.Close()
		return err
	}
	return nil
}

func (c *hostChild) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// markExited records the first-noticed end of the connection and unblocks Wait.
// First-wins: Close runs after Read has already seen a Stolen or Exit frame in
// the ordinary teardown, and overwriting that with DetachUser would report a
// stolen or finished session as a plain detach.
//
// First-wins is only safe because every caller passes a reason that is right
// whenever it happens to run first. Read and Close race after a Detach frame
// is written, and nothing orders them, so Read's error path works out the
// reason from the closed flag and never assumes a bare close is a drop (see
// Close).
func (c *hostChild) markExited(reason host.DetachReason) {
	c.exitOnce.Do(func() {
		c.mu.Lock()
		c.reason = reason
		c.mu.Unlock()
		close(c.exited)
	})
}

func clampPaneDim(v int) uint16 {
	switch {
	case v < 1:
		return 1
	case v > 65535:
		return 65535
	}
	return uint16(v)
}
