package host

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeHost is the host end of an in-memory connection. net.Pipe is
// synchronous — a write blocks until the peer reads — so the decode loop must
// run continuously for the client under test to make any progress.
type fakeHost struct {
	conn   net.Conn
	frames chan sentFrame
}

// sentFrame is one frame the client sent to the fake host.
type sentFrame struct {
	typ     MessageType
	payload []byte
}

// newFakeHost returns the fake host and the connection the client should use.
func newFakeHost(t *testing.T) (*fakeHost, net.Conn) {
	t.Helper()
	hostSide, clientSide := net.Pipe()
	h := &fakeHost{conn: hostSide, frames: make(chan sentFrame, 64)}
	go h.decodeLoop()
	t.Cleanup(func() {
		_ = hostSide.Close()
		_ = clientSide.Close()
	})
	return h, clientSide
}

func (h *fakeHost) decodeLoop() {
	defer close(h.frames)
	for {
		typ, payload, err := ReadFrame(h.conn)
		if err != nil {
			return
		}
		h.frames <- sentFrame{typ: typ, payload: payload}
	}
}

// nextSent returns the next frame the client sent.
func (h *fakeHost) nextSent(t *testing.T) sentFrame {
	t.Helper()
	select {
	case f, ok := <-h.frames:
		if !ok {
			t.Fatal("the client closed the connection while a frame was expected")
		}
		return f
	case <-time.After(testBudget):
		t.Fatalf("timed out after %s waiting for a frame from the client", testBudget)
		return sentFrame{}
	}
}

// awaitSent skips frames until one of type want arrives.
func (h *fakeHost) awaitSent(t *testing.T, want MessageType) sentFrame {
	t.Helper()
	for {
		f := h.nextSent(t)
		if f.typ == want {
			return f
		}
	}
}

// awaitClientGone asserts the client sent nothing more and closed its side.
func (h *fakeHost) awaitClientGone(t *testing.T) {
	t.Helper()
	for {
		select {
		case f, ok := <-h.frames:
			if !ok {
				return
			}
			t.Fatalf("client sent an unexpected %v frame after it should have stopped", f.typ)
		case <-time.After(testBudget):
			t.Fatalf("client never closed its side of the connection")
		}
	}
}

// push sends a frame from the fake host to the client.
func (h *fakeHost) push(t *testing.T, typ MessageType, payload []byte) {
	t.Helper()
	if err := WriteFrame(h.conn, typ, payload); err != nil {
		t.Fatalf("pushing %v frame to the client: %v", typ, err)
	}
}

// syncBuffer is a terminal stand-in: attach's output loop writes to it from a
// goroutine while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// attachStub is the whole attachDeps seam, recorded so tests can assert the
// terminal was raw-moded exactly once and restored exactly once.
type attachStub struct {
	deps attachDeps

	stdinW *io.PipeWriter
	stdout *syncBuffer

	rawErr   error
	made     atomic.Int32
	restored atomic.Int32
	stopped  atomic.Int32
	dials    atomic.Int32

	// notified receives the channel attach registered for SIGWINCH, so a
	// test can deliver a fake signal to attach's own channel.
	notified chan chan<- os.Signal

	sizeMu sync.Mutex
	rows   uint16
	cols   uint16
}

// newAttachStub wires attach to conn (nil = dial the real socketPath), a pipe
// for stdin, and a buffer for stdout.
func newAttachStub(t *testing.T, conn net.Conn) *attachStub {
	t.Helper()

	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() {
		_ = stdinW.Close()
		_ = stdinR.Close()
	})

	s := &attachStub{
		stdinW:   stdinW,
		stdout:   &syncBuffer{},
		notified: make(chan chan<- os.Signal, 1),
		rows:     24,
		cols:     80,
	}
	s.deps = attachDeps{
		dial: func(socket string, timeout time.Duration) (net.Conn, error) {
			s.dials.Add(1)
			if conn == nil {
				return net.DialTimeout("unix", socket, timeout)
			}
			return conn, nil
		},
		in:  stdinR,
		out: s.stdout,
		makeRaw: func() (func() error, error) {
			if s.rawErr != nil {
				return nil, s.rawErr
			}
			s.made.Add(1)
			return func() error {
				s.restored.Add(1)
				return nil
			}, nil
		},
		size: s.currentSize,
		notifySignals: func(c chan<- os.Signal) {
			select {
			case s.notified <- c:
			default:
			}
		},
		stopSignals: func(c chan<- os.Signal) { s.stopped.Add(1) },
	}
	return s
}

func (s *attachStub) currentSize() (uint16, uint16) {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	return s.rows, s.cols
}

func (s *attachStub) setSize(rows, cols uint16) {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	s.rows, s.cols = rows, cols
}

// typeKeys feeds bytes to attach's stdin.
func (s *attachStub) typeKeys(t *testing.T, keys string) {
	t.Helper()
	if _, err := s.stdinW.Write([]byte(keys)); err != nil {
		t.Fatalf("writing %q to the stub stdin: %v", keys, err)
	}
}

// winchChan returns the channel attach registered for SIGWINCH.
func (s *attachStub) winchChan(t *testing.T) chan<- os.Signal {
	t.Helper()
	select {
	case c := <-s.notified:
		return c
	case <-time.After(testBudget):
		t.Fatal("attach never registered a SIGWINCH handler")
		return nil
	}
}

// awaitOutput polls the stub terminal until want shows up.
func (s *attachStub) awaitOutput(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		if got := s.stdout.String(); bytes.Contains([]byte(got), []byte(want)) {
			return
		}
		time.Sleep(testPollStep)
	}
	t.Fatalf("stub terminal never received %q (got %q)", want, s.stdout.String())
}

// attachResult is one attach call's return values.
type attachResult struct {
	reason DetachReason
	err    error
}

// runAttach starts attach in a goroutine, since it blocks until the session
// is over.
func runAttach(s *attachStub, socketPath string, wait time.Duration) <-chan attachResult {
	out := make(chan attachResult, 1)
	go func() {
		reason, err := attach(s.deps, socketPath, wait)
		out <- attachResult{reason: reason, err: err}
	}()
	return out
}

func awaitAttach(t *testing.T, results <-chan attachResult) attachResult {
	t.Helper()
	select {
	case r := <-results:
		return r
	case <-time.After(testBudget):
		t.Fatalf("attach did not return within %s", testBudget)
		return attachResult{}
	}
}

// assertResize fails unless f is a Resize frame for the given geometry.
func assertResize(t *testing.T, f sentFrame, wantRows, wantCols uint16) {
	t.Helper()
	if f.typ != Resize {
		t.Fatalf("frame type = %v, want Resize", f.typ)
	}
	rows, cols, err := DecodeResize(f.payload)
	if err != nil {
		t.Fatalf("DecodeResize: %v", err)
	}
	if rows != wantRows || cols != wantCols {
		t.Errorf("resize = %dx%d, want %dx%d", rows, cols, wantRows, wantCols)
	}
}

func TestAttachSendsInitialResizeAndRoundTripsData(t *testing.T) {
	h, conn := newFakeHost(t)
	stub := newAttachStub(t, conn)
	stub.setSize(41, 137)

	results := runAttach(stub, "/unused.sock", time.Second)

	assertResize(t, h.nextSent(t), 41, 137)

	stub.typeKeys(t, "hello host")
	data := h.awaitSent(t, Data)
	if string(data.payload) != "hello host" {
		t.Errorf("host received %q, want %q", data.payload, "hello host")
	}

	h.push(t, Data, []byte("\x1b[2Jscreen paint"))
	stub.awaitOutput(t, "\x1b[2Jscreen paint")

	h.push(t, Stolen, nil)
	if r := awaitAttach(t, results); r.err != nil || r.reason != DetachStolen {
		t.Fatalf("attach = (%v, %v), want (DetachStolen, nil)", r.reason, r.err)
	}
}

func TestAttachOutcomes(t *testing.T) {
	tests := []struct {
		name string
		// end drives the session to its conclusion.
		end        func(t *testing.T, h *fakeHost, stub *attachStub)
		wantReason DetachReason
		// wantSent are the frames the host must see after the initial
		// Resize, in order.
		wantSent []MessageType
	}{
		{
			name: "ctrl-backslash detaches",
			end: func(t *testing.T, h *fakeHost, stub *attachStub) {
				stub.typeKeys(t, "ab\x1ccd")
			},
			wantReason: DetachUser,
			wantSent:   []MessageType{Data, Detach},
		},
		{
			name: "a lone detach key sends no data",
			end: func(t *testing.T, h *fakeHost, stub *attachStub) {
				stub.typeKeys(t, "\x1c")
			},
			wantReason: DetachUser,
			wantSent:   []MessageType{Detach},
		},
		{
			name: "terminal input ending detaches",
			end: func(t *testing.T, h *fakeHost, stub *attachStub) {
				if err := stub.stdinW.Close(); err != nil {
					t.Fatalf("closing the stub stdin: %v", err)
				}
			},
			wantReason: DetachUser,
			wantSent:   []MessageType{Detach},
		},
		{
			name: "a Stolen frame ends the attachment",
			end: func(t *testing.T, h *fakeHost, stub *attachStub) {
				h.push(t, Stolen, nil)
			},
			wantReason: DetachStolen,
		},
		{
			name: "an Exit frame is the session ending",
			end: func(t *testing.T, h *fakeHost, stub *attachStub) {
				h.push(t, Exit, nil)
			},
			wantReason: DetachExited,
		},
		{
			name: "a bare close with no Exit frame is a drop, not an exit",
			end: func(t *testing.T, h *fakeHost, stub *attachStub) {
				if err := h.conn.Close(); err != nil {
					t.Fatalf("closing the fake host's side: %v", err)
				}
			},
			wantReason: DetachDropped,
		},
		{
			name: "unexpected control frames are ignored, not fatal",
			end: func(t *testing.T, h *fakeHost, stub *attachStub) {
				h.push(t, Resize, EncodeResize(10, 20))
				h.push(t, Detach, nil)
				h.push(t, Stolen, nil)
			},
			wantReason: DetachStolen,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, conn := newFakeHost(t)
			stub := newAttachStub(t, conn)
			results := runAttach(stub, "/unused.sock", time.Second)

			assertResize(t, h.nextSent(t), 24, 80)
			tt.end(t, h, stub)

			r := awaitAttach(t, results)
			if r.err != nil {
				t.Fatalf("attach error = %v, want nil", r.err)
			}
			if r.reason != tt.wantReason {
				t.Errorf("attach reason = %v, want %v", r.reason, tt.wantReason)
			}
			if got, want := stub.made.Load(), int32(1); got != want {
				t.Errorf("raw mode entered %d times, want %d", got, want)
			}
			if got, want := stub.restored.Load(), int32(1); got != want {
				t.Errorf("terminal restored %d times, want %d", got, want)
			}
			if got := stub.stopped.Load(); got != 1 {
				t.Errorf("SIGWINCH handler stopped %d times, want 1", got)
			}

			for i, want := range tt.wantSent {
				if got := h.nextSent(t).typ; got != want {
					t.Fatalf("frame %d after the initial Resize = %v, want %v", i, got, want)
				}
			}
			if len(tt.wantSent) > 0 {
				// Bytes after the detach key are deliberately dropped.
				h.awaitClientGone(t)
			}
		})
	}
}

func TestAttachForwardsSIGWINCHAsResize(t *testing.T) {
	h, conn := newFakeHost(t)
	stub := newAttachStub(t, conn)
	results := runAttach(stub, "/unused.sock", time.Second)

	assertResize(t, h.nextSent(t), 24, 80)

	winch := stub.winchChan(t)
	stub.setSize(60, 200)
	winch <- syscall.SIGWINCH

	assertResize(t, h.awaitSent(t, Resize), 60, 200)

	h.push(t, Stolen, nil)
	if r := awaitAttach(t, results); r.reason != DetachStolen || r.err != nil {
		t.Fatalf("attach = (%v, %v), want (DetachStolen, nil)", r.reason, r.err)
	}
}

func TestAttachRestoresTheTerminalWhenTheInitialResizeFails(t *testing.T) {
	h, conn := newFakeHost(t)
	if err := h.conn.Close(); err != nil {
		t.Fatalf("closing the fake host's side: %v", err)
	}
	stub := newAttachStub(t, conn)

	r := awaitAttach(t, runAttach(stub, "/unused.sock", time.Second))
	if r.err == nil {
		t.Fatal("attach against a dead connection = nil error, want a failure")
	}
	if r.reason != 0 {
		t.Errorf("attach reason = %v, want the zero reason on an error", r.reason)
	}
	if got := stub.restored.Load(); got != 1 {
		t.Errorf("terminal restored %d times, want 1 — raw mode must be undone on every path", got)
	}
}

func TestAttachFailsWhenRawModeCannotBeEntered(t *testing.T) {
	h, conn := newFakeHost(t)
	stub := newAttachStub(t, conn)
	stub.rawErr = errors.New("no tty here")

	r := awaitAttach(t, runAttach(stub, "/unused.sock", time.Second))
	if !errors.Is(r.err, stub.rawErr) {
		t.Fatalf("attach error = %v, want it to wrap %v", r.err, stub.rawErr)
	}
	if got := stub.restored.Load(); got != 0 {
		t.Errorf("restore called %d times after a failed MakeRaw, want 0", got)
	}
	// The connection must not be leaked when raw mode fails.
	h.awaitClientGone(t)
}

func TestAttachReportsHostNotReachableAfterTheWindowExpires(t *testing.T) {
	socket := filepath.Join(shortSocketDir(t), "absent.sock")
	stub := newAttachStub(t, nil)

	const wait = 200 * time.Millisecond
	start := time.Now()
	r := awaitAttach(t, runAttach(stub, socket, wait))
	elapsed := time.Since(start)

	if !errors.Is(r.err, ErrHostNotReachable) {
		t.Fatalf("attach error = %v, want it to wrap ErrHostNotReachable", r.err)
	}
	if r.reason != 0 {
		t.Errorf("attach reason = %v, want the zero reason on an error", r.reason)
	}
	if elapsed < wait {
		t.Errorf("attach gave up after %s, want it to keep retrying for at least %s", elapsed, wait)
	}
	if got := stub.dials.Load(); got < 2 {
		t.Errorf("dial attempts = %d, want more than one (the dial must RETRY)", got)
	}
	if got := stub.made.Load(); got != 0 {
		t.Errorf("raw mode entered %d times without a connection, want 0", got)
	}
	if !bytes.Contains([]byte(r.err.Error()), []byte(socket)) {
		t.Errorf("error %q does not name the socket %q", r.err, socket)
	}
}

func TestAttachRetriesUntilTheHostStartsListening(t *testing.T) {
	socket := filepath.Join(shortSocketDir(t), "late.sock")
	const appearAfter = 250 * time.Millisecond

	conns := make(chan net.Conn, 1)
	listening := make(chan struct{})
	go func() {
		time.Sleep(appearAfter)
		ln, err := net.Listen("unix", socket)
		if err != nil {
			close(listening)
			close(conns)
			return
		}
		defer func() { _ = ln.Close() }()
		close(listening)
		conn, err := ln.Accept()
		if err != nil {
			close(conns)
			return
		}
		conns <- conn
	}()

	stub := newAttachStub(t, nil)
	results := runAttach(stub, socket, testBudget)

	<-listening
	conn, ok := <-conns
	if !ok {
		t.Skip("cannot bind a unix socket in this environment (the sandbox blocks bind)")
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Connected mid-window: the client's first frame is its terminal size.
	typ, payload, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("reading the client's first frame: %v", err)
	}
	assertResize(t, sentFrame{typ: typ, payload: payload}, 24, 80)

	if err := WriteFrame(conn, Stolen, nil); err != nil {
		t.Fatalf("sending Stolen: %v", err)
	}
	if r := awaitAttach(t, results); r.err != nil || r.reason != DetachStolen {
		t.Fatalf("attach = (%v, %v), want (DetachStolen, nil)", r.reason, r.err)
	}
	if got := stub.dials.Load(); got < 2 {
		t.Errorf("dial attempts = %d, want more than one (the listener appeared late)", got)
	}
}

func TestAttachWaitOrDefault(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "zero uses the default window", in: 0, want: defaultAttachWait},
		{name: "negative uses the default window", in: -time.Second, want: defaultAttachWait},
		{name: "an explicit window is honored", in: 250 * time.Millisecond, want: 250 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attachWaitOrDefault(tt.in); got != tt.want {
				t.Errorf("attachWaitOrDefault(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestDialWithRetryBackoffIsBounded(t *testing.T) {
	var attempts int
	d := attachDeps{
		dial: func(socket string, timeout time.Duration) (net.Conn, error) {
			attempts++
			return nil, fmt.Errorf("refused")
		},
	}
	start := time.Now()
	conn, err := dialWithRetry(d, "/nope.sock", 300*time.Millisecond)
	elapsed := time.Since(start)

	if conn != nil {
		t.Error("dialWithRetry returned a connection despite every dial failing")
	}
	if !errors.Is(err, ErrHostNotReachable) {
		t.Fatalf("error = %v, want it to wrap ErrHostNotReachable", err)
	}
	if attempts < 4 {
		t.Errorf("attempts = %d, want the poll to be tight enough for several tries in 300ms", attempts)
	}
	// A capped backoff must not overshoot the window by more than one step.
	if max := 300*time.Millisecond + attachDialMaxBackoff + testBudget/10; elapsed > max {
		t.Errorf("dialWithRetry took %s, want it to stop by about %s", elapsed, max)
	}
}

func TestDetachReasonExitCodes(t *testing.T) {
	tests := []struct {
		reason   DetachReason
		wantCode int
		wantText string
	}{
		{reason: DetachUser, wantCode: ExitDetached, wantText: "detached"},
		{reason: DetachStolen, wantCode: ExitStolen, wantText: "stolen"},
		{reason: DetachExited, wantCode: ExitSessionEnded, wantText: "session ended"},
		{reason: DetachDropped, wantCode: ExitDropped, wantText: "connection dropped"},
		{reason: DetachReason(0), wantCode: 1, wantText: "DetachReason(0)"},
	}
	for _, tt := range tests {
		t.Run(tt.wantText, func(t *testing.T) {
			if got := tt.reason.ExitCode(); got != tt.wantCode {
				t.Errorf("%v.ExitCode() = %d, want %d", tt.reason, got, tt.wantCode)
			}
			if got := tt.reason.String(); got != tt.wantText {
				t.Errorf("String() = %q, want %q", got, tt.wantText)
			}
		})
	}

	// Every reason must be distinct, or the dashboard cannot tell the
	// outcomes apart from the exit code alone.
	seen := map[int]DetachReason{}
	for _, r := range []DetachReason{DetachUser, DetachStolen, DetachExited, DetachDropped} {
		if other, dup := seen[r.ExitCode()]; dup {
			t.Errorf("%v and %v share exit code %d", r, other, r.ExitCode())
		}
		seen[r.ExitCode()] = r
	}
}

func TestMakeTerminalRawIsANoOpForANonTerminal(t *testing.T) {
	// A pipe is not a terminal, which is exactly the `go test` situation:
	// attach must still work, with raw mode as a no-op.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	restore, err := makeTerminalRaw(r)
	if err != nil {
		t.Fatalf("makeTerminalRaw on a pipe = %v, want nil", err)
	}
	if restore == nil {
		t.Fatal("makeTerminalRaw returned a nil restore func with a nil error")
	}
	if err := restore(); err != nil {
		t.Errorf("restore() = %v, want nil", err)
	}
}

func TestTerminalSizeFallsBackToThePTYDefault(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	rows, cols := terminalSize(r)
	if rows != defaultRows || cols != defaultCols {
		t.Errorf("terminalSize(pipe) = %dx%d, want the host's default %dx%d", rows, cols, defaultRows, defaultCols)
	}
}

func TestDefaultAttachDepsAreComplete(t *testing.T) {
	d := defaultAttachDeps()
	switch {
	case d.dial == nil:
		t.Error("dial is nil")
	case d.in == nil:
		t.Error("in is nil")
	case d.out == nil:
		t.Error("out is nil")
	case d.makeRaw == nil:
		t.Error("makeRaw is nil")
	case d.size == nil:
		t.Error("size is nil")
	case d.notifySignals == nil:
		t.Error("notifySignals is nil")
	case d.stopSignals == nil:
		t.Error("stopSignals is nil")
	}

	rows, cols := d.size()
	if rows == 0 || cols == 0 {
		t.Errorf("size() = %dx%d, want a usable size even without a terminal", rows, cols)
	}
}

func TestFrameWriterSerializesConcurrentSenders(t *testing.T) {
	// Two senders racing on a shared writer must never interleave a header
	// with another frame's payload. WriteFrame makes two Write calls, so the
	// bytes are only well-formed if the lock holds.
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	fw := &frameWriter{w: client}
	const perSender = 50
	payload := bytes.Repeat([]byte("x"), 128)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < perSender; i++ {
			if err := fw.write(Data, payload); err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < perSender; i++ {
			if err := fw.write(Resize, EncodeResize(24, 80)); err != nil {
				return
			}
		}
	}()

	read := make(chan error, 1)
	go func() {
		for i := 0; i < perSender*2; i++ {
			typ, got, err := ReadFrame(server)
			if err != nil {
				read <- err
				return
			}
			switch typ {
			case Data:
				if !bytes.Equal(got, payload) {
					read <- fmt.Errorf("frame %d: Data payload was corrupted (%d bytes)", i, len(got))
					return
				}
			case Resize:
				if _, _, err := DecodeResize(got); err != nil {
					read <- fmt.Errorf("frame %d: %w", i, err)
					return
				}
			default:
				read <- fmt.Errorf("frame %d: unexpected type %v — the stream is torn", i, typ)
				return
			}
		}
		read <- nil
	}()

	wg.Wait()
	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("reading back interleaved frames: %v", err)
		}
	case <-time.After(testBudget):
		t.Fatal("timed out reading back interleaved frames")
	}
}

// TestFrameWriterWriteTimesOutWhenThePeerNeverReads is the load-bearing case
// for W2.T2: without a write deadline, this blocks forever (net.Pipe's
// Write is synchronous and nobody ever reads the server side), so the test
// fails by hanging past testBudget rather than by a wrong value — proof the
// deadline, not some incidental behavior, is what makes it pass.
func TestFrameWriterWriteTimesOutWhenThePeerNeverReads(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })

	fw := &frameWriter{w: client}

	done := make(chan error, 1)
	go func() {
		done <- fw.write(Data, []byte("hello"))
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("write to a peer that never reads returned nil, want a timeout error")
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("write error = %v, want a net.Error with Timeout() true", err)
		}
	case <-time.After(testBudget):
		t.Fatal("frameWriter.write blocked past the test budget instead of returning a timeout error")
	}

	// The caller of write must be able to observe the failure too, not just
	// this test's direct call to it: a wedged pumpInput/forwardResizes rely
	// on send() (which just forwards write's error as a bool) and on
	// pumpOutput's Read on the same conn to notice the connection is gone.
	// That only happens if the timed-out write closed it.
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("frameWriter.write did not close the connection after a timeout, so a caller relying on the read side noticing has nothing to notice")
	}
}
