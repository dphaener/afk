package host

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	xterm "github.com/charmbracelet/x/term"
)

// ErrHostNotReachable is returned by Attach when the retry-dial window
// expires without anything answering on the socket. It is a sentinel because
// callers must tell it apart from a genuine failure: `afk attach` maps it
// to ExitNotRunning, which is how the dashboard recovers from a stale
// host.json whose PID has been recycled (W4.T1).
var ErrHostNotReachable = errors.New("host not reachable")

// DetachReason explains why Attach returned. The three values map 1:1 onto
// the exit-code contract in exit.go via ExitCode.
type DetachReason int

const (
	// DetachUser: the user pressed ctrl-\ (or the terminal's input side
	// ended). The session keeps running in the background.
	DetachUser DetachReason = iota + 1
	// DetachStolen: another client attached and took the session over; this
	// client was told Stolen and disconnected.
	DetachStolen
	// DetachExited: the host said the session ended — it sent an Exit frame
	// because the child process ended or the host was shut down.
	DetachExited
	// DetachDropped: the connection closed WITHOUT an Exit frame. The host
	// sends Exit on every shutdown it gets to run, so a bare close is either
	// the host dropping a client that fell behind its output (the session is
	// still running) or a host that died too abruptly to say so. The two are
	// indistinguishable from the wire, so a caller that needs to know whether
	// the host is alive has to ask discovery rather than assume either.
	DetachDropped
)

// String renders r for logs and human-facing status lines.
func (r DetachReason) String() string {
	switch r {
	case DetachUser:
		return "detached"
	case DetachStolen:
		return "stolen"
	case DetachExited:
		return "session ended"
	case DetachDropped:
		return "connection dropped"
	default:
		return fmt.Sprintf("DetachReason(%d)", int(r))
	}
}

// ExitCode maps r onto the shared `afk attach` exit-code contract
// (exit.go), so the CLI (W3.T4) and the dashboard (W4.T1) never re-derive
// the mapping. An unrecognized reason is a generic failure (1).
func (r DetachReason) ExitCode() int {
	switch r {
	case DetachUser:
		return ExitDetached
	case DetachStolen:
		return ExitStolen
	case DetachExited:
		return ExitSessionEnded
	case DetachDropped:
		return ExitDropped
	default:
		return 1
	}
}

const (
	// detachKey is ctrl-\ (0x1C), the key that detaches from a session.
	detachKey = 0x1C
	// defaultAttachWait is the retry-dial window used when Attach is given
	// a non-positive wait.
	defaultAttachWait = 5 * time.Second
	// attachDialTimeout bounds a single dial attempt. A unix-socket connect
	// either succeeds or is refused immediately, so this only matters for a
	// pathological socket (e.g. a listener with a full accept backlog).
	attachDialTimeout = 500 * time.Millisecond
	// attachDialFirstBackoff/attachDialMaxBackoff bound the poll interval
	// between dial attempts: tight at first, so a host that is nearly ready
	// is picked up in milliseconds, then capped so a long window is cheap.
	attachDialFirstBackoff = 10 * time.Millisecond
	attachDialMaxBackoff   = 100 * time.Millisecond
	// attachInputBufSize is the stdin read chunk. Terminal input arrives in
	// keystrokes and pastes, not megabytes.
	attachInputBufSize = 4096
	// attachDrainGrace bounds the wait for the output loop to stop before
	// the terminal is restored, so host bytes cannot land on a cooked
	// terminal.
	attachDrainGrace = 250 * time.Millisecond
	// attachWriteTimeout bounds a single frame write to the host, the same
	// role clientWriteTimeout plays on the host's side of this same socket
	// (host.go). A live host draining its socket normally completes a write
	// in microseconds, so 5s is far above any legitimate delay; it exists so
	// a host whose read side has stopped draining (nothing detects that
	// today — see the frameWriter doc comment) is caught inside one
	// user-visible stall instead of blocking pumpInput/forwardResizes
	// forever.
	attachWriteTimeout = 5 * time.Second
	// attachOutcomeDepth is one slot per reporting goroutine, so no loop can
	// block trying to report an outcome nobody will read.
	attachOutcomeDepth = 3
)

// Attach connects to a running host and hands it the terminal, blocking
// until the session is detached, stolen, or ends.
//
// The dial RETRIES for up to wait (defaultAttachWait when wait <= 0). This
// window is the readiness gate for every spawn-then-attach chain in afk:
// the host is spawned detached and never waited on, so its socket may not be
// bound yet when attach begins (`afk new`, the dashboard's restart flow,
// auto-attach after a background spawn, and `dash --focus` all rely on it).
// When the window expires with nobody listening, the error wraps
// ErrHostNotReachable so the caller can exit ExitNotRunning rather than
// reporting a generic failure.
//
// Once connected, Attach owns the terminal: stdin goes into raw mode and is
// ALWAYS restored before returning, on every path including errors. The
// current terminal size is sent up front, which makes the host nudge the PTY
// and the child repaint the screen; later SIGWINCHs are forwarded the same
// way.
//
// Attach is a plain blocking call for a foreground process that owns the TTY
// and exits shortly afterwards — it has no dashboard/bubbletea coupling. The
// stdin copy loop's in-flight blocking Read cannot be cancelled, so stdin
// must not be reused after Attach returns; the dashboard therefore shells out
// to `afk attach` as a child process instead of calling this directly.
//
// NO AFK COMMAND CALLS ATTACH TODAY. `afk attach` renders through
// internal/ui.AttachPane, which dials with Dial and drives the frame
// protocol itself; the dashboard's panes do the same. That makes
// internal/ui/hostpane.go's hostChild.writeFrame — not this file's
// frameWriter — the LIVE client→host write path, and it is where any change
// to client-side write behavior (deadlines, error surfacing) must land
// first. Attach and the machinery under it (attachSession, frameWriter) are
// retained as the raw-terminal client and the reference implementation of
// this protocol's client side; treating them as the live path is how a write
// deadline once landed on the wrong file while the incident's path kept
// none.
func Attach(socketPath string, wait time.Duration) (DetachReason, error) {
	return attach(defaultAttachDeps(), socketPath, wait)
}

// Dial connects to a running host's socket with the same retry window Attach
// uses, returning the raw connection for a caller that drives the frame
// protocol itself rather than handing over a terminal. The dashboard's embedded
// pane is that caller: it wraps the connection as a terminal child instead of
// giving the session the real TTY (internal/ui/hostpane.go).
//
// The error wraps ErrHostNotReachable when the window expires with nothing
// listening, so callers can distinguish a stale host record from a real
// failure exactly as Attach's callers do.
func Dial(socketPath string, wait time.Duration) (net.Conn, error) {
	return dialWithRetry(defaultAttachDeps(), socketPath, wait)
}

// DefaultAttachWait is the retry-dial window Attach and Dial use when given a
// non-positive wait. It is exported so callers that need the same readiness
// gate do not re-invent the constant.
const DefaultAttachWait = defaultAttachWait

// attachDeps is the injectable seam around everything Attach needs that a
// test process does not have: the socket, the real terminal's raw mode and
// size, the process's stdin/stdout, and SIGWINCH delivery. It is unexported
// because the only injector is this package's own tests — every other caller
// goes through Attach, which supplies the real implementations.
type attachDeps struct {
	// dial opens one connection to the host's socket.
	dial func(socket string, timeout time.Duration) (net.Conn, error)
	// in is the terminal's input side; out is where host output is written
	// verbatim.
	in  io.Reader
	out io.Writer
	// makeRaw puts the terminal into raw mode and returns the function that
	// undoes it. Whenever err is nil the restore func is non-nil and safe to
	// call exactly once.
	makeRaw func() (restore func() error, err error)
	// size reports the terminal's current size. It never fails: an unknown
	// size falls back to the host's own PTY default, because resizing a PTY
	// to 0x0 breaks full-screen children.
	size func() (rows, cols uint16)
	// notifySignals/stopSignals wrap signal.Notify/signal.Stop for SIGWINCH.
	notifySignals func(c chan<- os.Signal)
	stopSignals   func(c chan<- os.Signal)
}

func defaultAttachDeps() attachDeps {
	return attachDeps{
		dial: func(socket string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", socket, timeout)
		},
		in:      os.Stdin,
		out:     os.Stdout,
		makeRaw: func() (func() error, error) { return makeTerminalRaw(os.Stdin) },
		size:    func() (uint16, uint16) { return terminalSize(os.Stdin) },
		notifySignals: func(c chan<- os.Signal) {
			signal.Notify(c, syscall.SIGWINCH)
		},
		stopSignals: func(c chan<- os.Signal) { signal.Stop(c) },
	}
}

// makeTerminalRaw puts f into raw mode, returning the restore function.
//
// When f is not a terminal (piped stdin, or a `go test` process) raw mode is
// a NO-OP instead of an error: there is no terminal state to change, and
// attach over a pipe is still useful — the Wave 5 integration test drives a
// real host exactly this way. Refusing to attach would buy nothing.
func makeTerminalRaw(f *os.File) (func() error, error) {
	fd := f.Fd()
	if !xterm.IsTerminal(fd) {
		return func() error { return nil }, nil
	}
	state, err := xterm.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("putting the terminal into raw mode: %w", err)
	}
	return func() error {
		if err := xterm.Restore(fd, state); err != nil {
			return fmt.Errorf("restoring the terminal: %w", err)
		}
		return nil
	}, nil
}

// terminalSize reports f's size as rows/cols, falling back to the host's own
// PTY default when f has no size (a pipe, or a terminal that will not answer).
// Returning 0x0 would make the host shrink the PTY to nothing.
func terminalSize(f *os.File) (rows, cols uint16) {
	width, height, err := xterm.GetSize(f.Fd())
	if err != nil || width <= 0 || height <= 0 {
		return defaultRows, defaultCols
	}
	return uint16(height), uint16(width)
}

// attach is Attach with its dependencies injected.
func attach(d attachDeps, socketPath string, wait time.Duration) (reason DetachReason, err error) {
	conn, dialErr := dialWithRetry(d, socketPath, wait)
	if dialErr != nil {
		return 0, dialErr
	}

	restore, rawErr := d.makeRaw()
	if rawErr != nil {
		_ = conn.Close()
		return 0, rawErr
	}
	// Registered FIRST so it runs LAST: by the time the terminal goes back
	// to cooked mode the connection is closed and the output loop is joined,
	// so no host bytes can land on a restored terminal. A restore failure is
	// surfaced (the terminal is probably mangled) but never overwrites an
	// error we already have, and reason stays valid either way.
	var restoreOnce sync.Once
	defer func() {
		restoreOnce.Do(func() {
			if rerr := restore(); rerr != nil && err == nil {
				err = rerr
			}
		})
	}()

	s := &attachSession{
		deps:     d,
		conn:     conn,
		out:      &frameWriter{w: conn},
		done:     make(chan struct{}),
		outcomes: make(chan attachOutcome, attachOutcomeDepth),
	}

	// The host treats any size report as a reason to nudge the PTY, so this
	// is what repaints the screen on (re)attach.
	rows, cols := d.size()
	if werr := s.out.write(Resize, EncodeResize(rows, cols)); werr != nil {
		_ = conn.Close()
		return 0, fmt.Errorf("sending the initial terminal size to %s: %w", socketPath, werr)
	}

	winch := make(chan os.Signal, 1)
	d.notifySignals(winch)
	defer d.stopSignals(winch)

	outputStopped := make(chan struct{})
	go func() {
		defer close(outputStopped)
		s.pumpOutput()
	}()
	go s.pumpInput()
	go s.forwardResizes(winch)

	// Runs before the terminal is restored (defers are LIFO): stop the loops
	// and join the one that writes to the terminal.
	defer func() {
		close(s.done)
		_ = conn.Close()
		select {
		case <-outputStopped:
		case <-time.After(attachDrainGrace):
		}
	}()

	outcome := <-s.outcomes
	return outcome.reason, outcome.err
}

// dialWithRetry polls socketPath until something answers or the window
// expires. Backoff starts tight and is capped, and the deadline is only
// consulted after a failed attempt, so a zero window still gets one try.
func dialWithRetry(d attachDeps, socketPath string, wait time.Duration) (net.Conn, error) {
	wait = attachWaitOrDefault(wait)
	deadline := time.Now().Add(wait)
	backoff := attachDialFirstBackoff

	for attempts := 1; ; attempts++ {
		conn, err := d.dial(socketPath, attachDialTimeout)
		if err == nil {
			return conn, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: nothing answered on %s within %s (%d attempts): %w",
				ErrHostNotReachable, socketPath, wait, attempts, err)
		}
		if backoff > remaining {
			backoff = remaining
		}
		time.Sleep(backoff)
		if next := backoff * 2; next < attachDialMaxBackoff {
			backoff = next
		} else {
			backoff = attachDialMaxBackoff
		}
	}
}

// attachWaitOrDefault normalizes the retry-dial window.
func attachWaitOrDefault(wait time.Duration) time.Duration {
	if wait <= 0 {
		return defaultAttachWait
	}
	return wait
}

// attachOutcome is one loop's verdict on why the attachment is over. err and
// reason are mutually exclusive: a non-nil err means an unexpected failure
// (exit 1), otherwise reason maps to an exit code.
type attachOutcome struct {
	reason DetachReason
	err    error
}

// attachSession is the live state of one attach: the connection, the
// serialized frame writer, and the channels the three loops use to stop and
// to report why they stopped.
type attachSession struct {
	deps     attachDeps
	conn     net.Conn
	out      *frameWriter
	done     chan struct{}
	outcomes chan attachOutcome
	// detaching is set by pumpInput just BEFORE it sends a Detach frame, and
	// read by pumpOutput when the connection ends. The host closes the
	// connection the moment it reads Detach, with no frame of its own, so
	// that close can reach pumpOutput before pumpInput has even returned
	// from the write. Without this flag pumpOutput cannot tell that close
	// from a host that died, and a user who pressed ctrl-\ was told
	// "connection dropped" (see markDetaching).
	detaching atomic.Bool
}

// pumpInput copies terminal input to the host as Data frames, watching for
// the detach key.
//
// LIMITATION (deliberate): ANY 0x1C byte in the stream detaches, including
// one that arrives inside a bracketed paste or in the middle of an escape
// sequence. Recognizing only a "real" ctrl-\ keypress would mean modelling
// the terminal's input grammar here; ctrl-\ is not a byte ordinary typing or
// pasted text produces, so the trade is worth it — and a stray detach is
// harmless, since the session keeps running and can be reattached.
func (s *attachSession) pumpInput() {
	buf := make([]byte, attachInputBufSize)
	for {
		n, err := s.deps.in.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// No copy needed: write is synchronous, so buf is free to be
			// reused as soon as it returns.
			if i := bytes.IndexByte(chunk, detachKey); i >= 0 {
				// Forward whatever was typed before ctrl-\, then detach.
				// Anything after it is dropped — this attachment is over.
				if i > 0 && !s.send(Data, chunk[:i]) {
					return
				}
				s.markDetaching()
				if !s.send(Detach, nil) {
					return
				}
				s.report(attachOutcome{reason: DetachUser})
				return
			}
			if !s.send(Data, chunk) {
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// The terminal's input side is gone. Treat it as an
				// explicit detach: leave the session running rather than
				// ending it out from under the user.
				s.markDetaching()
				_ = s.send(Detach, nil)
				s.report(attachOutcome{reason: DetachUser})
				return
			}
			if s.stopping() {
				return
			}
			s.report(attachOutcome{err: fmt.Errorf("reading terminal input: %w", err)})
			return
		}
	}
}

// markDetaching records that the user asked to leave. It must be called
// BEFORE the Detach frame is sent, not after, and the order looks backwards
// until you see the race it closes: the host answers Detach by closing the
// connection, so pumpOutput can read that close while this goroutine is still
// inside the write. Whichever loop reports first names the outcome, and
// pumpOutput reporting first used to turn a clean detach into DetachDropped
// (exit 13) on roughly one run in seven of the lifecycle test. Recording the
// intent first means that whichever loop wins, it says the same thing.
//
// A flag and not simply an early report(DetachUser): attach returns on the
// first outcome and its teardown closes the connection, which could cut off
// the Detach frame that is about to be written. The flag is a memory write,
// so it adds nothing that can block the pump.
func (s *attachSession) markDetaching() {
	s.detaching.Store(true)
}

// pumpOutput writes the host's Data payloads to the terminal verbatim and
// turns the control frames into outcomes. It is the SINGLE authority on the
// connection being gone, which is why the other loops can ignore their own
// write errors instead of racing to report a reason. Being the authority on
// THAT it is gone does not tell it WHY, so it asks: if pumpInput had already
// recorded a detach (markDetaching), the close is the host's answer to it and
// the outcome is DetachUser; otherwise nobody here asked for it and it is a
// drop. A Stolen or Exit frame read before the close is reported from the
// switch below and still wins, because this loop returns on it and never
// reaches the read that fails.
func (s *attachSession) pumpOutput() {
	for {
		typ, payload, err := ReadFrame(s.conn)
		if err != nil {
			if s.stopping() {
				// attach is already returning for some other reason; this
				// read failed because it closed the connection.
				return
			}
			if s.detaching.Load() {
				// The user asked to detach and the host closed on reading
				// the frame, as it is designed to. This also covers a
				// Detach write that failed or timed out (frameWriter closes
				// the connection then): the user still asked to leave.
				s.report(attachOutcome{reason: DetachUser})
				return
			}
			// EOF, a reset, or a torn frame with no Exit before it: the host
			// dropped us or died without saying so (see DetachDropped).
			s.report(attachOutcome{reason: DetachDropped})
			return
		}
		switch typ {
		case Data:
			if _, werr := s.deps.out.Write(payload); werr != nil {
				s.report(attachOutcome{err: fmt.Errorf("writing session output to the terminal: %w", werr)})
				return
			}
		case Stolen:
			s.report(attachOutcome{reason: DetachStolen})
			return
		case Exit:
			s.report(attachOutcome{reason: DetachExited})
			return
		default:
			// Resize/Detach are client→host only; a well-behaved host never
			// sends them. Ignore rather than tear the session down over a
			// frame that costs us nothing.
		}
	}
}

// forwardResizes turns SIGWINCH into Resize frames. Coalescing is fine and
// intended: the channel holds one signal and every notification re-reads the
// CURRENT size, so a burst of resizes collapses to the final geometry.
func (s *attachSession) forwardResizes(winch <-chan os.Signal) {
	for {
		select {
		case <-s.done:
			return
		case _, ok := <-winch:
			if !ok {
				return
			}
			rows, cols := s.deps.size()
			if !s.send(Resize, EncodeResize(rows, cols)) {
				return
			}
		}
	}
}

// send writes one frame. false means the write failed — the connection is
// gone, so the caller stops without reporting an outcome and lets pumpOutput
// name the reason. Reporting here instead would race pumpOutput and make the
// process's exit code nondeterministic. The one sender whose reason pumpOutput
// could not work out for itself is a detach, which is why pumpInput calls
// markDetaching before it sends Detach: the reason is on record before the
// write that can fail, or that the host can answer by closing.
func (s *attachSession) send(typ MessageType, payload []byte) bool {
	return s.out.write(typ, payload) == nil
}

// report records an outcome without ever blocking. The channel has one slot
// per loop, so the first outcome always lands; later ones are ignored,
// because the first one to arrive is the reason attach is returning.
func (s *attachSession) report(o attachOutcome) {
	select {
	case s.outcomes <- o:
	default:
	}
}

// stopping reports whether attach is already tearing the session down.
func (s *attachSession) stopping() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// frameWriter serializes the three senders — the input loop, the SIGWINCH
// loop, and attach's initial Resize — onto one connection. WriteFrame makes
// two Write calls, so without the lock a header and a payload from different
// goroutines could interleave and corrupt the stream.
//
// A mutex, not the host side's queue-plus-writer-goroutine: there is no slow
// peer to isolate here (the host drains its socket unconditionally, per the
// design constraint in host.go) — except that this assumption has an escape
// hatch. If it's ever wrong, attachWriteTimeout is what stands in for the
// queue-plus-drop the host side uses, and w is net.Conn (not the bare
// io.Writer this only needed before) so write can set it.
type frameWriter struct {
	mu sync.Mutex
	w  net.Conn
}

func (fw *frameWriter) write(typ MessageType, payload []byte) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	_ = fw.w.SetWriteDeadline(time.Now().Add(attachWriteTimeout))
	err := WriteFrame(fw.w, typ, payload)
	if err != nil {
		// A timed-out write leaves the frame stream torn beyond repair
		// either way, so there is nothing a retry could salvage. Closing
		// here — rather than just returning the error — is what makes the
		// timeout actually surface: it unblocks pumpOutput's Read on the
		// same connection, which is the one path that already turns a dead
		// connection into a reported outcome: DetachDropped, or DetachUser
		// when the frame that timed out was the user's own Detach (see
		// pumpOutput). Never DetachExited, which needs an Exit frame from
		// the host. Without this, send()'s callers (pumpInput,
		// forwardResizes) drop the error on the floor by design, trusting
		// pumpOutput to notice — but pumpOutput has nothing to notice from a
		// write timeout alone.
		_ = fw.w.Close()
	}
	return err
}
