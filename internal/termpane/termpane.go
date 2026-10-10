// Package termpane bridges a PTY-attached child process to a renderable,
// in-memory terminal pane. It is the core of the embedded-terminal spike:
// a child command (e.g. the claude conductor) runs on a real PTY, its output
// is decoded by a charmbracelet/x/vt emulator, and the host UI renders the
// emulator's screen as a string inside its own layout.
//
// The package is deliberately UI-framework-agnostic: it exposes channels and
// plain types only (no bubbletea imports), so it stays unit-testable and the
// tea.Cmd adapters live with the UI. Communication surfaces:
//
//   - Changed(): a coalescing notification channel — one pending token no
//     matter how many PTY writes arrived, so the UI redraws once per cycle
//     instead of once per write.
//   - Done()/Err(): child-exit notification. Done is closed after the child
//     has exited AND all pump goroutines have been reclaimed; Err then reports
//     the exit error (nil for a clean exit, an internal error for a contained
//     emulator panic).
//
// Concurrency model: two pump goroutines plus a waiter. The output pump reads
// the PTY and feeds the emulator; the response pump reads the emulator's
// reply stream (answers to child queries such as DA1, DSR/CPR, and OSC 10/11
// color queries — claude issues these at startup) and writes it back into the
// PTY. The response pump is load-bearing, not optional: the emulator's reply
// stream is an unbuffered io.Pipe, so query replies (and SendKey/SendText
// input, which shares the pipe) would block forever without a dedicated
// reader, hanging the output pump mid-Write. Both pumps recover() panics from
// the experimental emulator and convert them into an exit-with-error signal —
// crash containment, not a crashed dashboard.
//
// Everything environment-touching goes through the injectable Deps seam
// (Deps struct + DefaultDeps + withDefaults, per repo convention), so tests
// fake the PTY with in-memory pipes.
package termpane

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// KeyEvent is the mode-aware key input type accepted by SendKey. It aliases
// the emulator's key event so encoding (DECCKM application-cursor mode, ctrl
// chords, alt prefixes) is delegated entirely to the emulator — callers never
// hand-roll escape bytes. Key codes and modifier constants (vt.KeyUp,
// vt.ModCtrl, ...) live in the charmbracelet/x/vt package.
type KeyEvent = vt.KeyPressEvent

// MouseEvent is the mode-aware mouse input interface accepted by SendMouse. It
// aliases the emulator's mouse event interface so encoding (SGR vs X10, motion
// bit, release terminator) and the child's-mode gate (whether the child enabled
// ?1000/?1002/?1003 and ?1006) belong to the emulator — callers never hand-roll
// bytes. Callers construct one of the four concrete types (MouseClick,
// MouseRelease, MouseWheel, MouseMotion) directly; the concrete type is what
// tells the emulator press vs release vs wheel vs motion. Button and modifier
// constants (MouseLeft, MouseWheelUp, ModCtrl, ...) live in the
// charmbracelet/x/vt package.
type MouseEvent = vt.Mouse

// MouseClick represents a mouse button press event.
type MouseClick = vt.MouseClick

// MouseRelease represents a mouse button release event.
type MouseRelease = vt.MouseRelease

// MouseWheel represents a mouse wheel scroll event.
type MouseWheel = vt.MouseWheel

// MouseMotion represents a mouse drag (motion-with-button) event.
type MouseMotion = vt.MouseMotion

// MouseButton is the button identifier type.
type MouseButton = vt.MouseButton

// Mouse button constants, based on X11 mouse button codes. Twelve buttons are
// recognized: the three primary buttons, four scroll-wheel directions, two
// browser navigation buttons, and two additional buttons.
const (
	MouseNone       = vt.MouseNone
	MouseLeft       = vt.MouseLeft
	MouseMiddle     = vt.MouseMiddle
	MouseRight      = vt.MouseRight
	MouseWheelUp    = vt.MouseWheelUp
	MouseWheelDown  = vt.MouseWheelDown
	MouseWheelLeft  = vt.MouseWheelLeft
	MouseWheelRight = vt.MouseWheelRight
	MouseBackward   = vt.MouseBackward
	MouseForward    = vt.MouseForward
	MouseButton10   = vt.MouseButton10
	MouseButton11   = vt.MouseButton11
)

// CursorShape is the shape the child asked its cursor to be drawn in. It
// aliases the emulator's style type so DECSCUSR decoding stays entirely in the
// emulator and callers compare against the re-exported constants below.
type CursorShape = vt.CursorStyle

// Cursor shapes, as selected by DECSCUSR (CSI Ps SP q).
const (
	CursorBlock     = vt.CursorBlock
	CursorUnderline = vt.CursorUnderline
	CursorBar       = vt.CursorBar
)

// CursorState is a snapshot of the child's cursor: where it sits in the grid
// (0-based, unlike the 1-based CUP sequence that moves it) and how the child
// asked for it to be drawn. A renderer needs all five fields, because a
// terminal cursor is not just a position — a child that hides it (DECTCEM) or
// switches it to a steady bar expects that honored.
type CursorState struct {
	X, Y    int
	Visible bool
	Shape   CursorShape
	Blink   bool
}

// childTermEnv is forced into every child's environment. The emulator speaks
// xterm-class escape sequences; inheriting the host's TERM (which need not
// match) produces rendering breakage that would be misblamed on the emulator.
const childTermEnv = "TERM=xterm-256color"

// exitDrainWindow bounds how long the waiter lets the output pump drain
// buffered PTY output after the child exits before force-closing the master.
// A grandchild holding the PTY slave open could otherwise block teardown
// forever; a bounded window trades at most this much latency for liveness.
const exitDrainWindow = 500 * time.Millisecond

// Config describes the child process and the initial pane geometry.
type Config struct {
	// Argv is the child command line (argv[0] is the binary). Must be
	// non-empty.
	Argv []string
	// Dir is the child's working directory ("" means inherit).
	Dir string
	// Env holds extra KEY=VALUE variables layered over the inherited
	// environment; a variable named here replaces the inherited one. Start
	// always layers TERM=xterm-256color on top, replacing any TERM given.
	Env []string
	// Cols and Rows are the initial pane size in character cells. Both must
	// be positive.
	Cols, Rows int
}

// Child abstracts the PTY-attached process: byte I/O against the PTY master,
// resize, termination, and exit reaping. The real implementation wraps
// creack/pty + exec.Cmd; tests substitute in-memory pipes.
type Child interface {
	// Read returns the child's terminal output (PTY master side). It must
	// unblock with an error once the child is gone and output is drained.
	io.Reader
	// Write delivers terminal input to the child (PTY master side).
	io.Writer
	// Resize updates the child's window size (TIOCSWINSZ on a real PTY, which
	// also delivers SIGWINCH).
	Resize(cols, rows int) error
	// Kill forcibly terminates the child. It must be safe to call after the
	// child has already exited.
	Kill() error
	// Wait blocks until the child exits and returns its exit error. It is
	// called exactly once, by the pane's waiter goroutine.
	Wait() error
	// Close releases the master side, unblocking any pending Read/Write.
	Close() error
}

// Deps is the injectable seam for every environment-touching operation. Real
// wiring comes from DefaultDeps; tests substitute fakes to exercise the pane
// hermetically.
type Deps struct {
	// StartChild starts cfg.Argv attached to a new PTY at cfg.Cols x cfg.Rows
	// and returns the master-side handle. The cfg it receives already has
	// TERM=xterm-256color layered into Env.
	StartChild func(cfg Config) (Child, error)
}

// DefaultDeps returns Deps wired to the real environment (creack/pty).
func DefaultDeps() Deps {
	return Deps{StartChild: startPTYChild}
}

// withDefaults fills in any nil field on d that has a real-environment default.
func (d Deps) withDefaults() Deps {
	def := DefaultDeps()
	if d.StartChild == nil {
		d.StartChild = def.StartChild
	}
	return d
}

// Pane is a running PTY-attached child bridged to a terminal emulator. Create
// one with Start; it is not usable zero-valued.
type Pane struct {
	child Child
	emu   *vt.SafeEmulator

	// changed carries at most one pending redraw token (see Changed).
	changed chan struct{}
	// done closes once the child has exited and the pumps are reclaimed.
	done chan struct{}
	// outDone / respDone close when the respective pump goroutine returns.
	outDone  chan struct{}
	respDone chan struct{}
	// shutdown tells the response pump to exit on its next wakeup. It exists
	// because vt's Emulator.Close writes the emulator's closed flag without
	// synchronization against a concurrent Read: the pump must be provably
	// gone before awaitExit may call emu.Close.
	shutdown chan struct{}

	killOnce sync.Once

	mu         sync.Mutex
	cols, rows int
	exitErr    error
	exitSet    bool
	// curVisible/curShape/curBlink mirror the emulator's cursor attributes.
	// They are a mirror rather than a read-through because x/vt exposes no
	// getter for them (see the SetCallbacks block in Start); the emulator
	// pushes every change here instead.
	curVisible bool
	curShape   CursorShape
	curBlink   bool
}

// Start launches cfg.Argv on a PTY at cfg.Cols x cfg.Rows, wires it to a
// fresh emulator, and starts the pump goroutines. The child's environment is
// the inherited one plus cfg.Env, with TERM forced to xterm-256color.
func Start(cfg Config, d Deps) (*Pane, error) {
	d = d.withDefaults()
	if len(cfg.Argv) == 0 {
		return nil, errors.New("termpane: argv must not be empty")
	}
	if cfg.Cols <= 0 || cfg.Rows <= 0 {
		return nil, fmt.Errorf("termpane: pane size must be positive, got %dx%d", cfg.Cols, cfg.Rows)
	}
	// Layer the forced TERM over any caller-supplied Env so the injected
	// starter (real or fake) always receives the corrected environment.
	cfg.Env = mergeEnv(cfg.Env, []string{childTermEnv})

	child, err := d.StartChild(cfg)
	if err != nil {
		return nil, fmt.Errorf("termpane: start child: %w", err)
	}

	emu := vt.NewSafeEmulator(cfg.Cols, cfg.Rows)
	// The spike renders the live screen only; don't pay for scrollback.
	emu.SetScrollbackSize(0)

	p := &Pane{
		child:    child,
		emu:      emu,
		changed:  make(chan struct{}, 1),
		done:     make(chan struct{}),
		outDone:  make(chan struct{}),
		respDone: make(chan struct{}),
		shutdown: make(chan struct{}),
		cols:     cfg.Cols,
		rows:     cfg.Rows,
		// vt's zero Cursor is a visible, blinking block (cursor.go:15-25:
		// Hidden and Steady both start false, Style starts CursorBlock), and
		// the callbacks below only fire on change — so these seeds are the
		// only source of truth until the child writes its first DECTCEM or
		// DECSCUSR.
		curVisible: true,
		curShape:   CursorBlock,
		curBlink:   true,
	}
	// x/vt publishes no getter for the cursor's Hidden flag or Style — only
	// CursorPosition (safe_emulator.go:171) — so the callback set
	// (callbacks.go:28-42) is the sole way to observe DECTCEM and DECSCUSR.
	// This must run before the pumps start: SetCallbacks reaches the embedded
	// *Emulator (SafeEmulator embeds it, safe_emulator.go:11-14) and is not
	// mutex-wrapped, so it is only race-free while nothing can call Write yet.
	//
	// Neither body may call a SafeEmulator (or Emulator) method: they run
	// inside Emulator.Write, which SafeEmulator.Write holds its mutex across
	// (safe_emulator.go:26-30), so re-entering the emulator here self-deadlocks
	// on a non-reentrant sync.Mutex. Take p.mu, store, release — nothing else.
	emu.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			p.mu.Lock()
			p.curVisible = visible
			p.mu.Unlock()
		},
		CursorStyle: func(style vt.CursorStyle, blink bool) {
			// The second argument is NOT blink, despite the field signature:
			// setCursorStyle stores Steady = !blink and then fires
			// cb.CursorStyle(style, !blink) (screen.go:246-252), so what
			// arrives here is Steady. Invert it back.
			p.mu.Lock()
			p.curShape, p.curBlink = style, !blink
			p.mu.Unlock()
		},
	})
	go p.pumpOutput()
	go p.pumpResponses()
	go p.awaitExit()
	return p, nil
}

// Render returns the emulator's current screen, styles encoded as ANSI escape
// sequences, sized to the pane. A contained emulator panic yields a
// human-readable failure line instead of crashing the caller.
func (p *Pane) Render() (out string) {
	defer func() {
		if r := recover(); r != nil {
			p.fail(fmt.Errorf("termpane: panic in render: %v", r))
			out = fmt.Sprintf("terminal emulator failure: %v", r)
		}
	}()
	return p.emu.Render()
}

// Cursor reports the child's current cursor position and appearance, so a host
// UI can draw a cursor of its own at the right cell (Render encodes the screen
// but no cursor). A contained emulator panic yields a hidden cursor rather than
// crashing the caller.
//
// The position is read before p.mu is taken, and that ordering is load-bearing.
// The emulator's callbacks take p.mu while the emulator's own lock is held, so
// holding p.mu across CursorPosition (which takes that same lock,
// safe_emulator.go:171) would invert the order and deadlock against a
// concurrent Write from the output pump.
//
// The consequence is that position and visibility/style are read under two
// different locks, so a Write landing between the two reads yields a frame
// whose position and style disagree. That is accepted, not a bug: the same
// Write fires the pane's Changed notification, and the redraw it triggers
// reads a consistent pair.
func (p *Pane) Cursor() (cs CursorState) {
	defer func() {
		if r := recover(); r != nil {
			p.fail(fmt.Errorf("termpane: panic in cursor: %v", r))
			cs = CursorState{Visible: false}
		}
	}()
	pos := p.emu.CursorPosition()
	p.mu.Lock()
	defer p.mu.Unlock()
	return CursorState{
		X:       pos.X,
		Y:       pos.Y,
		Visible: p.curVisible,
		Shape:   p.curShape,
		Blink:   p.curBlink,
	}
}

// SendKey delivers a key press to the child. Encoding is mode-aware: the
// emulator applies DECCKM application-cursor sequences, ctrl chords, and alt
// prefixes as the child has configured them.
func (p *Pane) SendKey(k KeyEvent) {
	defer p.containPanic("send key")
	p.emu.SendKey(k)
}

// SendText delivers literal text to the child (typed characters, no key
// encoding).
func (p *Pane) SendText(s string) {
	defer p.containPanic("send text")
	p.emu.SendText(s)
}

// Paste delivers text as a paste: when the child has enabled bracketed paste
// mode the emulator wraps it in the bracketing sequences, so multi-line
// pastes arrive as one unit.
func (p *Pane) Paste(s string) {
	defer p.containPanic("paste")
	p.emu.Paste(s)
}

// SendMouse delivers a mouse event to the child. Encoding and the child's-mode
// gate are mode-aware: the emulator no-ops unless the child enabled a mouse
// mode (?9/?1000/?1001/?1002/?1003), picks SGR encoding only when the child
// set ?1006, and selects the motion bit and release terminator from the
// concrete type (MouseClick/MouseRelease/MouseWheel/MouseMotion). A claude
// session sets ?1000, ?1002, ?1003 and ?1006, and re-emits them on every
// dimension-changing SIGWINCH — so a reattached pane's fresh emulator has them
// set even though it never saw the child's startup output, because the
// reattach resize nudge (host.go:434-450) delivers that SIGWINCH. Encoded
// bytes reach the child over the existing pumpResponses path, exactly like
// SendKey.
func (p *Pane) SendMouse(m MouseEvent) {
	defer p.containPanic("send mouse")
	p.emu.SendMouse(m)
}

// Resize updates the pane geometry: the emulator first (so redraw output
// triggered by the child's SIGWINCH lands in a correctly sized grid), then
// the PTY. Dimensions below 1 are clamped to 1.
func (p *Pane) Resize(cols, rows int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			e := fmt.Errorf("termpane: panic in resize: %v", r)
			p.fail(e)
			err = e
		}
	}()
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	p.mu.Lock()
	p.cols, p.rows = cols, rows
	p.mu.Unlock()
	p.emu.Resize(cols, rows)
	if rerr := p.child.Resize(cols, rows); rerr != nil {
		return fmt.Errorf("termpane: resize pty: %w", rerr)
	}
	p.notify()
	return nil
}

// Size returns the current pane geometry in character cells.
func (p *Pane) Size() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows
}

// Changed returns the redraw-notification channel. It holds at most one
// pending token regardless of how much output arrived, so a consumer that
// receives, redraws, and re-arms coalesces bursts into single repaints.
func (p *Pane) Changed() <-chan struct{} {
	return p.changed
}

// Done returns a channel that closes once the child has exited and the pump
// goroutines have been reclaimed. Consumers (e.g. a tea.Cmd) block on it and
// then read Err for the outcome.
func (p *Pane) Done() <-chan struct{} {
	return p.done
}

// Err reports why the pane ended: nil for a clean child exit, the child's
// exit error otherwise, or an internal error when a contained emulator panic
// tore the pane down. Meaningful once Done is closed.
func (p *Pane) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}

// Close terminates the child if it is still running and blocks until every
// pane goroutine has been reclaimed. Idempotent, and safe after a natural
// exit.
func (p *Pane) Close() error {
	p.kill()
	<-p.done
	return nil
}

// pumpOutput reads child output and feeds it to the emulator, notifying the
// UI after each processed chunk. It runs until the PTY read errors (child
// exit or master close).
func (p *Pane) pumpOutput() {
	defer close(p.outDone)
	defer p.containPanic("output pump")
	buf := make([]byte, 32*1024)
	for {
		n, err := p.child.Read(buf)
		if n > 0 {
			_, _ = p.emu.Write(buf[:n])
			p.notify()
		}
		if err != nil {
			return
		}
	}
}

// pumpResponses reads the emulator's reply stream (query responses plus
// SendKey/SendText/Paste input, which share the same pipe) and writes it into
// the PTY. It runs until the emulator is closed (io.EOF) or the PTY write
// fails during teardown.
func (p *Pane) pumpResponses() {
	defer close(p.respDone)
	defer p.containPanic("response pump")
	buf := make([]byte, 4096)
	for {
		n, err := p.emu.Read(buf)
		select {
		case <-p.shutdown:
			return // teardown wake-up; drop the wake byte, don't touch the child
		default:
		}
		if n > 0 {
			if _, werr := p.child.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// awaitExit reaps the child, lets the output pump drain (bounded), tears down
// the PTY master and the emulator to unblock both pumps, and finally closes
// done. It is the only goroutine that closes done, and the only caller of
// child.Wait.
//
// TEARDOWN-ORDERING INVARIANT (load-bearing — do not reorder):
//
// The entire Read/Close race safety of the pane rests on this function's
// single-threaded teardown sequence. At the pinned x/vt version, SafeEmulator
// leaves exactly three methods outside its RWMutex: Read (a lockless
// pass-through) and Close/String (not wrapped at all — they fall through to
// the embedded *Emulator). Emulator.Close writes the emulator's unexported
// `closed` flag with NO synchronization against a concurrent Emulator.Read,
// so calling emu.Close while pumpResponses is still parked in emu.Read is a
// real data race (found by -race during W2.T1). This function is the ONLY
// thing preventing it: it (1) signals shutdown, (2) wakes a pump parked in
// Read via the wake byte, (3) waits for respDone — the pump is provably gone
// — and only then (4) calls emu.Close, which in turn releases the wake
// writer and any pending input writers via the pipe's CloseWithError.
//
// This invariant is invisible to internal/ui's tests: they exercise the pane
// through the paneHandle fake, never a real emulator, so no ui-level test
// can catch a reordering here. Any change to this sequence must re-run
// `go test -race -count=3 ./internal/termpane/`, and anyone extending the
// Pane API must check x/vt's safe_emulator.go for whether the new method is
// actually wrapped before exposing it.
func (p *Pane) awaitExit() {
	err := p.child.Wait()
	// A pump failure recorded earlier (first-wins) takes precedence over the
	// kill-induced exit error it caused.
	p.setExitErr(err)

	select {
	case <-p.outDone:
	case <-time.After(exitDrainWindow):
	}
	_ = p.child.Close() // unblock a still-reading output pump
	<-p.outDone

	// Retire the response pump BEFORE emu.Close: vt's Close sets its closed
	// flag without synchronization against a concurrent Read, so the pump
	// must be gone first. The wake write unblocks a pump parked in Read; if
	// the pump already exited, the wake writer parks on the pipe instead and
	// emu.Close releases it.
	close(p.shutdown)
	wakeDone := make(chan struct{})
	go func() {
		defer close(wakeDone)
		defer func() { _ = recover() }()
		p.emu.SendText("\x00")
	}()
	<-p.respDone
	_ = p.emu.Close() // unblock the wake writer and any pending input writers
	<-wakeDone

	p.notify() // let the UI paint the final screen state
	close(p.done)
}

// containPanic recovers a panic in the surrounding goroutine or call and
// converts it into pane teardown with a descriptive exit error.
func (p *Pane) containPanic(where string) {
	if r := recover(); r != nil {
		p.fail(fmt.Errorf("termpane: panic in %s: %v", where, r))
	}
}

// fail records err as the pane outcome (first error wins) and kills the
// child, which drives awaitExit's normal teardown path.
func (p *Pane) fail(err error) {
	p.setExitErr(err)
	p.kill()
}

// setExitErr records the pane outcome; the first recorded value wins so a
// pump-panic error is not overwritten by the exit status of the kill it
// triggered.
func (p *Pane) setExitErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exitSet {
		p.exitErr = err
		p.exitSet = true
	}
}

// kill terminates the child at most once.
func (p *Pane) kill() {
	p.killOnce.Do(func() { _ = p.child.Kill() })
}

// notify coalesces redraw notifications: at most one token is ever pending.
func (p *Pane) notify() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

// ptyChild is the real Child: an exec.Cmd on a creack/pty master.
type ptyChild struct {
	cmd    *exec.Cmd
	master *os.File
}

// startPTYChild is the DefaultDeps StartChild: it launches cfg.Argv on a new
// PTY sized cfg.Cols x cfg.Rows with the inherited environment plus cfg.Env.
func startPTYChild(cfg Config) (Child, error) {
	cmd := exec.Command(cfg.Argv[0], cfg.Argv[1:]...) // #nosec G204 -- argv is caller-supplied by design
	cmd.Dir = cfg.Dir
	cmd.Env = mergeEnv(os.Environ(), cfg.Env)
	master, err := pty.StartWithSize(cmd, winsize(cfg.Cols, cfg.Rows))
	if err != nil {
		return nil, err
	}
	return &ptyChild{cmd: cmd, master: master}, nil
}

func (c *ptyChild) Read(p []byte) (int, error)  { return c.master.Read(p) }
func (c *ptyChild) Write(p []byte) (int, error) { return c.master.Write(p) }

func (c *ptyChild) Resize(cols, rows int) error {
	return pty.Setsize(c.master, winsize(cols, rows))
}

func (c *ptyChild) Kill() error {
	if c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Kill()
}

func (c *ptyChild) Wait() error { return c.cmd.Wait() }

func (c *ptyChild) Close() error { return c.master.Close() }

// winsize converts pane cells to a pty.Winsize, clamped to uint16 range with
// a floor of 1.
func winsize(cols, rows int) *pty.Winsize {
	return &pty.Winsize{Cols: clampU16(cols), Rows: clampU16(rows)}
}

func clampU16(v int) uint16 {
	if v < 1 {
		v = 1
	}
	if v > math.MaxUint16 {
		v = math.MaxUint16
	}
	return uint16(v)
}

// mergeEnv returns base with every variable redefined in overrides removed,
// then overrides appended. Removing shadowed entries matters: with duplicate
// names in an environment block, which copy getenv returns is
// implementation-defined, so "append and hope" does not reliably override.
func mergeEnv(base, overrides []string) []string {
	names := make(map[string]bool, len(overrides))
	for _, kv := range overrides {
		if i := strings.IndexByte(kv, '='); i > 0 {
			names[kv[:i]] = true
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 && names[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, overrides...)
}
