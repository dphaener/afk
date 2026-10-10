package termpane

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

const testTimeout = 5 * time.Second

// fakeChild is an in-memory Child: the test writes "child output" into outW,
// and everything the pane sends to the child accumulates in input.
type fakeChild struct {
	outR *io.PipeReader
	outW *io.PipeWriter

	mu      sync.Mutex
	input   bytes.Buffer
	resizes [][2]int
	killed  bool

	exitOnce sync.Once
	waitCh   chan error
}

func newFakeChild() *fakeChild {
	r, w := io.Pipe()
	return &fakeChild{outR: r, outW: w, waitCh: make(chan error, 1)}
}

func (f *fakeChild) Read(p []byte) (int, error) { return f.outR.Read(p) }

func (f *fakeChild) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.input.Write(p)
}

func (f *fakeChild) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakeChild) Kill() error {
	f.mu.Lock()
	f.killed = true
	f.mu.Unlock()
	f.exit(nil)
	return nil
}

func (f *fakeChild) Wait() error { return <-f.waitCh }

func (f *fakeChild) Close() error { return f.outR.Close() }

// exit simulates child termination: output EOF plus a Wait result.
func (f *fakeChild) exit(err error) {
	f.exitOnce.Do(func() {
		_ = f.outW.Close()
		f.waitCh <- err
	})
}

// emit blocks until the pane's output pump has consumed the bytes.
func (f *fakeChild) emit(t *testing.T, s string) {
	t.Helper()
	if _, err := io.WriteString(f.outW, s); err != nil {
		t.Fatalf("emit %q: %v", s, err)
	}
}

func (f *fakeChild) inputString() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.input.String()
}

func (f *fakeChild) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

func (f *fakeChild) recordedResizes() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resizes)
}

// panicChild is a Child whose output read panics, simulating an emulator-side
// crash inside the output pump goroutine.
type panicChild struct {
	*fakeChild
}

func (p *panicChild) Read([]byte) (int, error) { panic("synthetic emulator crash") }

// startPane starts a Pane over the given Child and returns it plus the Config
// the injected starter received (TERM already layered in by Start).
func startPane(t *testing.T, c Child, cfg Config) (*Pane, Config) {
	t.Helper()
	if len(cfg.Argv) == 0 {
		cfg.Argv = []string{"fake-child"}
	}
	if cfg.Cols == 0 {
		cfg.Cols = 80
	}
	if cfg.Rows == 0 {
		cfg.Rows = 24
	}
	var received Config
	p, err := Start(cfg, Deps{StartChild: func(got Config) (Child, error) {
		received = got
		return c, nil
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, received
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// recvChanged asserts a redraw notification arrives.
func recvChanged(t *testing.T, p *Pane) {
	t.Helper()
	select {
	case <-p.Changed():
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for change notification")
	}
}

// drainChanged clears any pending redraw token so the next recvChanged
// observes a fresh notification.
func drainChanged(p *Pane) {
	select {
	case <-p.changed:
	default:
	}
}

// requireDone asserts the pane's exit channel closes.
func requireDone(t *testing.T, p *Pane) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for pane Done")
	}
}

func plainRender(p *Pane) string { return ansi.Strip(p.Render()) }

func TestRenderShowsChildOutputAndExitSignals(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	f.emit(t, "hello, pane")
	waitFor(t, "output to appear in Render", func() bool {
		return strings.Contains(plainRender(p), "hello, pane")
	})

	f.exit(nil)
	requireDone(t, p)
	if err := p.Err(); err != nil {
		t.Fatalf("Err after clean exit = %v, want nil", err)
	}
	// The final screen must survive exit so the UI can paint it once more.
	if got := plainRender(p); !strings.Contains(got, "hello, pane") {
		t.Fatalf("Render after exit = %q, want it to retain %q", got, "hello, pane")
	}
}

func TestChildExitErrorIsReported(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	wantErr := errors.New("exit status 3")
	f.exit(wantErr)
	requireDone(t, p)
	if err := p.Err(); !errors.Is(err, wantErr) {
		t.Fatalf("Err = %v, want %v", err, wantErr)
	}
}

func TestQueryResponseRoundTrip(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})
	_ = p

	// The child asks for the cursor position (DSR/CPR). The emulator's answer
	// must be pumped back into the PTY or a real child hangs at startup.
	f.emit(t, "\x1b[6n")
	waitFor(t, "cursor position report to reach the child", func() bool {
		return strings.Contains(f.inputString(), "\x1b[1;1R")
	})
}

func TestPumpPanicBecomesExitError(t *testing.T) {
	pc := &panicChild{fakeChild: newFakeChild()}
	p, _ := startPane(t, pc, Config{})

	requireDone(t, p)
	err := p.Err()
	if err == nil {
		t.Fatal("Err = nil, want a contained-panic error")
	}
	if !strings.Contains(err.Error(), "panic in output pump") {
		t.Fatalf("Err = %v, want it to mention the output-pump panic", err)
	}
	if !pc.wasKilled() {
		t.Fatal("child was not killed after the pump panic")
	}
}

// writePanicChild panics on Write, simulating a crash in the response pump
// (as opposed to panicChild, which crashes the output pump on Read).
type writePanicChild struct {
	*fakeChild
}

func (w *writePanicChild) Write([]byte) (int, error) { panic("synthetic response-pump crash") }

func TestResponsePumpPanicBecomesExitError(t *testing.T) {
	wc := &writePanicChild{fakeChild: newFakeChild()}
	p, _ := startPane(t, wc, Config{})

	// Trigger a reply-stream write (the DSR/CPR response to a cursor-position
	// query): the response pump reads the emulator's reply and calls the
	// (panicking) child.Write.
	wc.emit(t, "\x1b[6n")

	requireDone(t, p)
	err := p.Err()
	if err == nil {
		t.Fatal("Err = nil, want a contained-panic error")
	}
	if !strings.Contains(err.Error(), "panic in response pump") {
		t.Fatalf("Err = %v, want it to mention the response-pump panic", err)
	}
}

// TestOutputSurvivesExitBeforeRenderObserved exercises the "child exits
// while output pending" edge case: awaitExit's bounded drain window must let
// the output pump finish processing in-flight output before Done closes, so
// the final Render still shows what the child wrote right before exiting,
// even when nothing has confirmed it landed yet.
func TestOutputSurvivesExitBeforeRenderObserved(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	f.emit(t, "final output before exit")
	f.exit(nil)

	requireDone(t, p)
	if got := plainRender(p); !strings.Contains(got, "final output before exit") {
		t.Fatalf("Render after exit = %q, want it to retain in-flight output", got)
	}
}

func TestSendTextReachesChild(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	p.SendText("hi there")
	waitFor(t, "typed text to reach the child", func() bool {
		return strings.Contains(f.inputString(), "hi there")
	})
}

func TestSendKeyHonorsApplicationCursorMode(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	// Default mode: up arrow encodes as CSI A.
	p.SendKey(KeyEvent{Code: vt.KeyUp})
	waitFor(t, "normal-mode arrow encoding", func() bool {
		return strings.Contains(f.inputString(), "\x1b[A")
	})

	// Child enables DECCKM (application cursor keys); wait for the emulator
	// to process the mode change before pressing again.
	drainChanged(p)
	f.emit(t, "\x1b[?1h")
	recvChanged(t, p)

	p.SendKey(KeyEvent{Code: vt.KeyUp})
	waitFor(t, "application-mode arrow encoding", func() bool {
		return strings.Contains(f.inputString(), "\x1bOA")
	})
}

func TestPasteUsesBracketedPasteWhenEnabled(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	drainChanged(p)
	f.emit(t, "\x1b[?2004h") // child enables bracketed paste
	recvChanged(t, p)

	p.Paste("line one\nline two")
	waitFor(t, "bracketed paste to reach the child", func() bool {
		return strings.Contains(f.inputString(), "\x1b[200~line one\nline two\x1b[201~")
	})
}

func TestOutputTriggersChangeNotification(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	drainChanged(p)
	f.emit(t, "ping")
	recvChanged(t, p)
}

func TestResizePropagatesToChildAndEmulator(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{Cols: 80, Rows: 24})

	if err := p.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if cols, rows := p.Size(); cols != 120 || rows != 40 {
		t.Fatalf("Size = %dx%d, want 120x40", cols, rows)
	}
	got := f.recordedResizes()
	if len(got) != 1 || got[0] != [2]int{120, 40} {
		t.Fatalf("child resizes = %v, want [[120 40]]", got)
	}
}

func TestResizeClampsNonPositiveDimensions(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	if err := p.Resize(0, -3); err != nil {
		t.Fatalf("Resize(0,-3): %v", err)
	}
	if cols, rows := p.Size(); cols != 1 || rows != 1 {
		t.Fatalf("Size = %dx%d, want 1x1", cols, rows)
	}
	got := f.recordedResizes()
	if len(got) != 1 || got[0] != [2]int{1, 1} {
		t.Fatalf("child resizes = %v, want [[1 1]]", got)
	}
}

func TestResizeToZeroZeroClampsAndDoesNotPanic(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{Cols: 80, Rows: 24})

	if err := p.Resize(0, 0); err != nil {
		t.Fatalf("Resize(0,0): %v", err)
	}
	if cols, rows := p.Size(); cols != 1 || rows != 1 {
		t.Fatalf("Size = %dx%d, want 1x1", cols, rows)
	}
	got := f.recordedResizes()
	if len(got) != 1 || got[0] != [2]int{1, 1} {
		t.Fatalf("child resizes = %v, want [[1 1]]", got)
	}
}

func TestResizeToOneByOneDoesNotPanic(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{Cols: 80, Rows: 24})

	if err := p.Resize(1, 1); err != nil {
		t.Fatalf("Resize(1,1): %v", err)
	}
	if cols, rows := p.Size(); cols != 1 || rows != 1 {
		t.Fatalf("Size = %dx%d, want 1x1", cols, rows)
	}
}

func TestCloseKillsChildAndReclaims(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !f.wasKilled() {
		t.Fatal("Close did not kill the child")
	}
	requireDone(t, p)
	// Idempotent: a second Close must not hang or panic.
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestStartForcesTermEnv(t *testing.T) {
	f := newFakeChild()
	_, received := startPane(t, f, Config{Env: []string{"TERM=dumb", "FOO=bar"}})

	if !slices.Contains(received.Env, "TERM=xterm-256color") {
		t.Fatalf("child env %v missing forced TERM=xterm-256color", received.Env)
	}
	if slices.Contains(received.Env, "TERM=dumb") {
		t.Fatalf("child env %v still contains the caller's TERM", received.Env)
	}
	if !slices.Contains(received.Env, "FOO=bar") {
		t.Fatalf("child env %v lost the caller's FOO", received.Env)
	}
}

func TestStartValidation(t *testing.T) {
	if _, err := Start(Config{Cols: 80, Rows: 24}, Deps{StartChild: func(Config) (Child, error) {
		t.Fatal("StartChild called despite empty argv")
		return nil, nil
	}}); err == nil {
		t.Fatal("Start with empty argv did not error")
	}

	if _, err := Start(Config{Argv: []string{"x"}, Cols: 0, Rows: 24}, Deps{StartChild: func(Config) (Child, error) {
		t.Fatal("StartChild called despite zero size")
		return nil, nil
	}}); err == nil {
		t.Fatal("Start with zero cols did not error")
	}

	wantErr := errors.New("no pty for you")
	_, err := Start(Config{Argv: []string{"x"}, Cols: 80, Rows: 24},
		Deps{StartChild: func(Config) (Child, error) { return nil, wantErr }})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start error = %v, want wrapped %v", err, wantErr)
	}
}

func TestMergeEnv(t *testing.T) {
	base := []string{"TERM=dumb", "HOME=/home/x", "PATH=/bin"}
	got := mergeEnv(base, []string{"TERM=xterm-256color", "EXTRA=1"})
	want := []string{"HOME=/home/x", "PATH=/bin", "TERM=xterm-256color", "EXTRA=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("mergeEnv = %v, want %v", got, want)
	}
}

// waitForCursor polls p.Cursor() until it equals want. Polling (rather than a
// straight assert) is required because Cursor() is asynchronous with the
// output pump: f.emit only hands bytes to the pipe, and the emulator has not
// necessarily consumed them when emit returns. On timeout it logs the last
// value it saw, which is what distinguishes "nothing happened" from "wrong
// field".
func waitForCursor(t *testing.T, p *Pane, what string, want CursorState) {
	t.Helper()
	var last CursorState
	defer func() {
		if t.Failed() {
			t.Logf("%s: last observed Cursor() = %+v, want %+v", what, last, want)
		}
	}()
	waitFor(t, what, func() bool {
		last = p.Cursor()
		return last == want
	})
}

// TestCursorTracksChildEscapes pins Cursor() against the escape sequences a
// child actually writes. Load-bearing rows:
//
//   - "hidden then shown" is the DECTCEM path, which exists only through the
//     vt.Callbacks.CursorVisibility hook — x/vt publishes no getter for the
//     cursor's Hidden flag, so a regression that drops the callback
//     registration shows up here and nowhere else.
//   - "fresh pane defaults" pins the values Start seeds before the child has
//     written anything; the renderer draws a cursor from them on the first
//     frame, so a wrong default is visible to the user immediately.
//   - "cursor style sequence" pins the callback-argument inversion: vt's
//     setCursorStyle fires cb.CursorStyle(style, !blink) (screen.go:251), so
//     the second argument is Steady, not blink. If a future x/vt bump fixes
//     that, this row goes red and points straight at the inverting line in
//     Start. Its steps share one pane on purpose — vt fires CursorStyle only
//     on change, so the trailing "\x1b[0 q" (which restores the default) is
//     only observable after a preceding style change.
func TestCursorTracksChildEscapes(t *testing.T) {
	type step struct {
		emit string // child output; "" means assert without writing anything
		want CursorState
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "fresh pane defaults",
			steps: []step{
				{want: CursorState{X: 0, Y: 0, Visible: true, Shape: CursorBlock, Blink: true}},
			},
		},
		{
			name: "absolute positioning",
			steps: []step{
				// CUP is 1-based, CursorPosition() is 0-based.
				{emit: "\x1b[5;10H", want: CursorState{X: 9, Y: 4, Visible: true, Shape: CursorBlock, Blink: true}},
			},
		},
		{
			name: "hidden then shown",
			steps: []step{
				{emit: "\x1b[?25l", want: CursorState{Visible: false, Shape: CursorBlock, Blink: true}},
				{emit: "\x1b[?25h", want: CursorState{Visible: true, Shape: CursorBlock, Blink: true}},
			},
		},
		{
			name: "cursor style sequence",
			steps: []step{
				{emit: "\x1b[4 q", want: CursorState{Visible: true, Shape: CursorUnderline, Blink: false}},
				{emit: "\x1b[5 q", want: CursorState{Visible: true, Shape: CursorBar, Blink: true}},
				{emit: "\x1b[0 q", want: CursorState{Visible: true, Shape: CursorBlock, Blink: true}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeChild()
			p, _ := startPane(t, f, Config{})

			for _, st := range tc.steps {
				if st.emit != "" {
					f.emit(t, st.emit)
				}
				waitForCursor(t, p, fmt.Sprintf("cursor state after %q", st.emit), st.want)
			}
		})
	}
}

func TestSendMouseWithNoModeEnabledSendsNothing(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	p.SendMouse(MouseWheel{X: 0, Y: 0, Button: MouseWheelUp})

	p.SendText("sentinel")
	waitFor(t, "sentinel text to reach the child", func() bool {
		return strings.Contains(f.inputString(), "sentinel")
	})

	got := f.inputString()
	if got != "sentinel" {
		t.Fatalf("child input = %q, want only %q (no mouse encoding)", got, "sentinel")
	}
}

func TestSendMouseWithX10EncodingOnly(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	drainChanged(p)
	f.emit(t, "\x1b[?1000h")
	recvChanged(t, p)

	p.SendMouse(MouseWheel{X: 0, Y: 0, Button: MouseWheelUp})
	waitFor(t, "X10 wheel-up encoding at (0,0)", func() bool {
		return strings.Contains(f.inputString(), "\x1b[M`!!")
	})

	got := f.inputString()
	if !strings.Contains(got, "\x1b[M`!!") {
		t.Fatalf("child input = %q, want X10 form %q at (0,0)", got, "\x1b[M`!!")
	}
}

func TestSendMouseWithSGREncoding(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	drainChanged(p)
	f.emit(t, "\x1b[?1000h\x1b[?1006h")
	recvChanged(t, p)

	p.SendMouse(MouseWheel{X: 4, Y: 2, Button: MouseWheelUp})
	waitFor(t, "SGR wheel-up encoding", func() bool {
		return strings.Contains(f.inputString(), "\x1b[<64;5;3M")
	})

	p.SendMouse(MouseRelease{X: 4, Y: 2, Button: MouseNone})
	waitFor(t, "SGR release encoding with lowercase terminator", func() bool {
		return strings.Contains(f.inputString(), "\x1b[<3;5;3m")
	})

	got := f.inputString()
	if !strings.Contains(got, "\x1b[<64;5;3M") {
		t.Fatalf("child input missing SGR wheel-up %q, got %q", "\x1b[<64;5;3M", got)
	}
	if !strings.Contains(got, "\x1b[<3;5;3m") {
		t.Fatalf("child input missing SGR release %q, got %q", "\x1b[<3;5;3m", got)
	}
}

func TestSendMouseMotionWithSGREncoding(t *testing.T) {
	f := newFakeChild()
	p, _ := startPane(t, f, Config{})

	drainChanged(p)
	f.emit(t, "\x1b[?1002h\x1b[?1006h")
	recvChanged(t, p)

	p.SendMouse(MouseMotion{X: 4, Y: 2, Button: MouseLeft})
	waitFor(t, "SGR motion encoding with motion bit set", func() bool {
		return strings.Contains(f.inputString(), "\x1b[<32;5;3M")
	})

	got := f.inputString()
	if !strings.Contains(got, "\x1b[<32;5;3M") {
		t.Fatalf("child input = %q, want SGR motion form %q (motion bit set, proving drag path)", got, "\x1b[<32;5;3M")
	}
}
