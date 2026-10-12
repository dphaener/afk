package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// ErrHostAlreadyRunning is returned by Serve when a live host is already
// answering on the configured socket. It is a sentinel so callers can tell
// "someone beat me to it" apart from a genuine startup failure.
var ErrHostAlreadyRunning = errors.New("host already running")

const (
	// childTermGrace is how long the child gets to exit after SIGTERM
	// before it is SIGKILLed.
	childTermGrace = 5 * time.Second
	// childKillGrace bounds the wait after SIGKILL; a process wedged in an
	// uninterruptible syscall must not hang the host forever.
	childKillGrace = 2 * time.Second
	// dialProbeTimeout bounds the "is the existing socket alive?" dial.
	dialProbeTimeout = 500 * time.Millisecond
	// clientWriteTimeout bounds a single frame write to the attached
	// client, so a wedged client cannot pin the writer goroutine.
	clientWriteTimeout = 5 * time.Second
	// clientPendingLimit bounds the per-client outbound backlog in bytes.
	// A client that falls this far behind is dropped rather than allowed
	// to stall the PTY pump or grow the host without bound (it can
	// reattach; attach forces a redraw). Bytes, not frames: see
	// appendFrame for why a frame count was the wrong unit.
	clientPendingLimit = 4 << 20
	// clientFlushGrace bounds how long shutdown waits for the attached
	// client's queued frames (the child's final output) to flush.
	clientFlushGrace = 250 * time.Millisecond
	// childInputQueueDepth bounds the client→child input queue drained by
	// writeChild. Input arrives from the client in chunks of at most
	// attachInputBufSize (4096 bytes), so this caps the host at ~256KB of
	// buffered keystrokes: two orders of magnitude more than the PTY's own
	// input buffer (a little over 1KB here), which a child that is reading
	// empties in microseconds. A backlog this deep therefore means the
	// child has stopped reading rather than that it is momentarily busy,
	// and the excess is dropped instead of allowed to stall the serve loop.
	childInputQueueDepth = 64
	// childInputStaleAfter is the write deadline for one queued input
	// chunk: bytes that have waited this long are dropped rather than
	// delivered. It cannot be an fd deadline — a PTY master does not
	// support one (ptmx.SetWriteDeadline returns "file type does not
	// support deadline" on darwin, because the fd is not registered with
	// the runtime poller), so the bound is enforced on the queue instead
	// and an already-blocked write simply runs to completion. 5s matches
	// clientWriteTimeout on the output side: far longer than any healthy
	// terminal reader takes to drain its input, yet short enough that a
	// child which wakes up is not handed a burst of keystrokes aimed at a
	// screen it has since redrawn.
	childInputStaleAfter = 5 * time.Second
	// inputDropLogInterval rate-limits the dropped-input log line. A wedged
	// child produces a drop per keystroke, and host.log is the only record
	// of the incident — one line per second carrying a running total keeps
	// it readable and keeps the log from being what fills the disk. The
	// first drop of a session always logs, so the fact that input is being
	// lost is never delayed.
	inputDropLogInterval = time.Second
	// ctrlQueueDepth buffers control frames (Resize/Detach/Exit and a
	// client's read error) on their way to the serve loop. Data frames
	// deliberately do NOT travel this channel — see readClient — so every
	// producer left is low-rate, and every serve-loop branch is now bounded
	// (the unbounded ptmx.Write that used to sit in the select is gone), so
	// a client's reader goroutine has nothing to park behind here in
	// practice. Dropping frames on overflow instead, the way an overflowed
	// Data frame is dropped, is the wrong trade for this channel: it would
	// strand a client that asked to detach or a session that was asked to
	// exit.
	ctrlQueueDepth = 16
	// ptyReadBufSize is the PTY read chunk size.
	ptyReadBufSize = 32 * 1024
	// ptyDrainGrace bounds the wait for the PTY pump to hit EOF on
	// shutdown, so the client sees the child's last bytes.
	ptyDrainGrace = 250 * time.Millisecond
	// defaultRows/defaultCols are the PTY's size until the first client
	// reports its real terminal size. A 0x0 PTY confuses full-screen
	// programs, so the child never sees one.
	defaultRows = 24
	defaultCols = 80
	// fallbackTERM is used only when the host's own environment has no
	// TERM at all (it is spawned detached, from a possibly bare env).
	fallbackTERM = "xterm-256color"
)

// afterAccept runs in acceptLoop each time it has accepted a connection,
// before that connection is offered to the serve loop, and is handed the
// channel that closes when shutdown begins. It does nothing in the shipped
// host. It exists because the window it sits in — a connection the kernel has
// already told the client is open, which the host has not yet attached — is a
// few instructions wide, so a test that needs a shutdown to land inside it has
// to hold it open rather than hope for it. Package-level for the same reason
// livenessTiming is; newHostRuntime copies it once, so a host that outlives a
// test never reads the variable again.
var afterAccept = func(done <-chan struct{}) {}

// resizeNudgeDelay is the gap between the two Setsize calls of a redraw
// nudge. Long enough that a child which coalesces SIGWINCH still observes
// two distinct sizes, short enough to be invisible. Package-level so tests
// can shorten it.
var resizeNudgeDelay = 25 * time.Millisecond

// ServeConfig is the full input to Serve — everything the detached
// `afk _host` process needs, all passed explicitly as flags by the
// spawner (no derivation from the environment, so the host is testable and
// the spawn command is self-documenting).
type ServeConfig struct {
	// Name is the session name, used only for logging.
	Name string
	// Worktree is the child's working directory.
	Worktree string
	// Socket is the unix socket path to listen on (see SocketPath).
	Socket string
	// MetaPath is where host.json is written (.afk/<name>/host.json).
	MetaPath string
	// LogPath is the host's own log file (.afk/<name>/host.log).
	LogPath string
	// Argv is the child command and its arguments, e.g.
	// {"claude", "/afk:go --session <name>"}.
	Argv []string
	// Provider names the provider whose binary Argv runs ("claude",
	// "codex", "opencode"); empty means claude. Argv alone cannot carry it: a wedged
	// restart builds a different command line (restart.go's resumeArgv), and
	// only the provider says which binary that must be. Optional, so it is not
	// checked by validate; an unknown name refuses a restart, not startup.
	Provider string
	// ProbeUnattached makes the host probe a silent child for a wedge even
	// while no client is attached (see liveness.go). It exists for sessions
	// the daemon runs: the daemon supervises through files and never attaches
	// a client, so under the ordinary rule, which only probes while somebody
	// is attached, a wedged unattended session would never get a verdict and
	// nothing would ever restart it.
	//
	// Off by default, and it must stay that way for a session a person runs:
	// such a session with nobody attached is allowed to sit silent for as
	// long as it likes. Nobody is waiting on it, there is no screen to
	// repaint, and a verdict nobody asked for would only be a wrong cell in
	// the dashboard. Optional, so it is not checked by validate.
	ProbeUnattached bool
}

func (c ServeConfig) validate() error {
	switch {
	case c.Name == "":
		return errors.New("serve: session name is required")
	case c.Worktree == "":
		return errors.New("serve: worktree is required")
	case c.Socket == "":
		return errors.New("serve: socket path is required")
	case c.MetaPath == "":
		return errors.New("serve: host.json path is required")
	case c.LogPath == "":
		return errors.New("serve: log path is required")
	case len(c.Argv) == 0:
		return errors.New("serve: child argv is required")
	}
	return nil
}

// Serve is the body of the detached `afk _host` process: it owns a PTY
// running cfg.Argv and multiplexes it onto a unix socket that at most one
// client is attached to at a time.
//
// Lifecycle: bind the socket (refusing to start only if a live host answers
// on it), start the child under a PTY, publish host.json, then run the
// serve loop until the child exits, a client sends Exit, or the host is
// signalled. Every return path removes the socket file and host.json.
//
// A nil error means an orderly shutdown (child gone, files cleaned up) —
// the caller should exit 0.
func Serve(cfg ServeConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}

	logger, closeLog, err := openLog(cfg.LogPath)
	if err != nil {
		return err
	}
	defer closeLog()

	logger.Printf("starting host: session=%q worktree=%q socket=%q argv=%q", cfg.Name, cfg.Worktree, cfg.Socket, cfg.Argv)

	// Registered before anything is spawned: a SIGTERM arriving mid-startup
	// is buffered here and handled by the serve loop as an orderly shutdown,
	// instead of killing the host by default disposition and stranding the
	// socket file and host.json.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)

	ln, err := listenExclusive(cfg.Socket, logger)
	if err != nil {
		logger.Printf("not starting: %v", err)
		return err
	}
	// Past this point the socket file and host.json are ours, so every
	// return path must release both. (Registered before the child starts:
	// even a failed child start must not leave a socket behind.)
	defer func() {
		// ln.Close() unlinks the socket path BEFORE closing the listening
		// fd, so there is no instant where the path is gone but dials are
		// still refused — which is exactly what makes it safe: a successor
		// host cannot have bound this path yet, so we can never unlink
		// someone else's socket. Do not "helpfully" os.Remove it here.
		if err := ln.Close(); err != nil {
			logger.Printf("closing listener: %v", err)
		}
		if _, err := os.Stat(cfg.Socket); err == nil {
			logger.Printf("warning: %s still exists after closing the listener", cfg.Socket)
		}
		removeOurMeta(cfg.MetaPath, logger)
		logger.Printf("host for session %q stopped", cfg.Name)
	}()

	h := newHostRuntime(cfg, logger, ln)
	if err := h.spawn(cfg.Argv); err != nil {
		logger.Printf("%v", err)
		return err
	}
	// Closing the live generation's PTY master unblocks its pump goroutine.
	// The current generation, not the one started above: a restart retires
	// that one and closes its master itself (see restart.go).
	defer h.closeCurrentPTY()
	defer h.shutdown()

	// ChildPID from the generation spawned just above, not from a later
	// lookup: host.json is how a restart is observed from outside this
	// process (restart.go's setResumedChild replaces this same field), and a
	// host.json that only grew a child_pid after the first restart would make
	// "absent" mean both "an old host wrote this" and "this child is the
	// original", which is exactly the ambiguity the field exists to remove.
	meta := hostMeta{
		Meta: Meta{
			PID:       os.Getpid(),
			Socket:    cfg.Socket,
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			Argv:      cfg.Argv,
			Provider:  cfg.Provider,
		},
		ChildPID: h.cur.pid(),
	}
	h.baseMeta = meta
	if err := writeMetaFile(cfg.MetaPath, meta); err != nil {
		// Without host.json nothing can find this host to stop it, so
		// take the child down rather than leaking a headless child.
		logger.Printf("%v", err)
		h.terminateChild()
		return err
	}

	return h.run(signals)
}

// childProc is one generation of the hosted child: the process, the PTY it
// runs on, and the goroutines serving it. A restart retires one generation
// and installs the next (restart.go), and everything here is per-generation
// for one reason — a retired child's pumps must never touch the live child's
// PTY. The pumps are handed their generation as an argument rather than
// reading hostRuntime.cur, which is what keeps that field free of locks.
type childProc struct {
	cmd  *exec.Cmd
	ptmx *os.File

	// exited closes once cmd.Wait has reaped this child.
	exited chan struct{}
	// ptyDone closes once this generation's pumpPTY has drained its master.
	ptyDone chan struct{}
	// retired closes when this generation has been replaced, stopping its
	// input writer so it cannot feed a PTY whose child is gone.
	retired chan struct{}
}

func (g *childProc) pid() int {
	if g.cmd.Process == nil {
		return 0
	}
	return g.cmd.Process.Pid
}

// gone reports whether this child has already been reaped.
func (g *childProc) gone() bool {
	select {
	case <-g.exited:
		return true
	default:
		return false
	}
}

// hostRuntime is the live state of one Serve call. Ownership rules: rows,
// cols and cur are touched only by the serve loop (run, and Serve before it
// on the same goroutine); client is guarded by mu because the PTY pump reads
// it concurrently.
type hostRuntime struct {
	cfg ServeConfig
	log *log.Logger
	ln  net.Listener

	// cur is the child generation the host is currently serving.
	cur *childProc

	mu     sync.Mutex
	client *clientConn // the single attached client; nil when detached

	rows uint16
	cols uint16

	clients chan *clientConn // accepted connections, from acceptLoop
	ctrl    chan clientFrame // control frames (and read errors) from the attached client
	childIn chan inputFrame  // client→child input, drained by writeChild
	probes  chan struct{}    // liveness monitor → serve loop: nudge the child

	// restarting is true while a restart's signal ladder runs on its own
	// goroutine (see beginRestart). Serve-loop-owned, like cur: it gates a
	// second Restart frame and disables the child-exited branch, whose firing
	// mid-ladder is the restart working rather than the child "exiting on its
	// own". restartDone (buffered 1) is how the ladder goroutine hands the
	// reaped child back to the loop; the buffer means that goroutine can
	// never leak if the loop returns first.
	restarting  bool
	restartDone chan restartHandoff

	// live is the wedge detector's state; see liveness.go. The PTY pump
	// reports output into it and monitorLiveness evaluates it.
	live *liveness

	// baseMeta is host.json's non-liveness content, as Serve published it at
	// startup. Written before monitorLiveness is started and read there —
	// the goroutine start is the happens-before edge; the one later writer,
	// restart.go's setResumedChild, holds publishMu like the reader does.
	baseMeta hostMeta

	// publishMu guards host.json republishes against shutdown; see
	// publishLiveness for why that window has to be closed.
	publishMu  sync.Mutex
	publishOff bool

	// childWriteSince is the unix-nano start time of the ptmx write
	// currently in flight, or 0 while the writer is idle. Atomic because
	// the drop path reads it from other goroutines purely to report how
	// long the child has been refusing input; nothing branches on it.
	childWriteSince atomic.Int64

	// inputDropMu guards the dropped-input bookkeeping. Its own mutex
	// rather than mu: drops are recorded from every client's reader
	// goroutine and from writeChild, and must not contend with the PTY
	// pump's client lookup on the output path.
	inputDropMu   sync.Mutex
	inputDropped  uint64
	inputDropLast time.Time

	// accepting is true from the moment run starts acceptLoop until shutdown
	// has collected what it left behind. Serve-loop-owned: shutdown runs on
	// the same goroutine as run, as Serve's deferred call. acceptDone closes
	// when acceptLoop returns, and unattached (guarded by mu) is the
	// connection it was holding when shutdown began, if any. See
	// endUnattached.
	accepting  bool
	acceptDone chan struct{}
	unattached []*clientConn

	// afterAccept is the package-level seam of the same name, copied at
	// construction; see there.
	afterAccept func(done <-chan struct{})

	done         chan struct{} // closed when the host is shutting down
	shutdownOnce sync.Once
}

// clientFrame is one decoded control frame from a client, or that client's
// read error. conn is carried so the serve loop can ignore frames from a
// client that has since been stolen or dropped.
type clientFrame struct {
	conn    *clientConn
	typ     MessageType
	payload []byte
	err     error
}

// inputFrame is one chunk of client→child input on its way to the PTY. Both
// fields exist to be re-checked at dequeue time rather than at enqueue time:
// conn so bytes typed into a client that has since been stolen from never
// reach the child, and queuedAt so bytes that sat behind a blocked write for
// longer than childInputStaleAfter are discarded instead of delivered late.
type inputFrame struct {
	conn     *clientConn
	payload  []byte
	queuedAt time.Time
}

// inputVerdict is classifyInput's decision about one dequeued input chunk.
type inputVerdict int

const (
	inputWrite inputVerdict = iota
	inputDropStale
	inputDropStolen
)

func (v inputVerdict) String() string {
	switch v {
	case inputWrite:
		return "write"
	case inputDropStale:
		return "stale"
	case inputDropStolen:
		return "stolen"
	default:
		return fmt.Sprintf("inputVerdict(%d)", int(v))
	}
}

// classifyInput decides what writeChild does with a chunk it has just taken
// off the queue: deliver it, drop it as stale, or drop it because the client
// that typed it no longer owns the session. Pure, so the whole matrix is
// table-testable without a PTY or a wedged child. A chunk from a stolen
// client is dropped however fresh it is — the reason it is discarded is
// ownership, not age, and reporting it as stale would misdescribe the event
// in the log.
func classifyInput(current bool, age, maxAge time.Duration) inputVerdict {
	switch {
	case !current:
		return inputDropStolen
	case age > maxAge:
		return inputDropStale
	default:
		return inputWrite
	}
}

func newHostRuntime(cfg ServeConfig, logger *log.Logger, ln net.Listener) *hostRuntime {
	h := &hostRuntime{
		cfg:     cfg,
		log:     logger,
		ln:      ln,
		rows:    defaultRows,
		cols:    defaultCols,
		clients: make(chan *clientConn),
		ctrl:    make(chan clientFrame, ctrlQueueDepth),
		childIn: make(chan inputFrame, childInputQueueDepth),
		// Single slot: a second outstanding nudge request would ask the
		// child the same question twice, and the monitor re-asks on its next
		// tick anyway.
		probes:      make(chan struct{}, 1),
		restartDone: make(chan restartHandoff, 1),
		live:        newLiveness(livenessTiming, time.Now()),
		acceptDone:  make(chan struct{}),
		afterAccept: afterAccept,
		done:        make(chan struct{}),
	}
	return h
}

// spawn starts argv on a fresh PTY at the host's current size and installs it
// as the live generation, together with the three goroutines that serve it.
// Serve-loop only: Serve calls it before run, on the same goroutine, and
// restartChild calls it from inside the loop.
//
// All three goroutines start before host.json is written and before anything
// else touches the child: the child must never block on a full PTY buffer,
// not even during startup, terminateChild needs the reaper running on every
// failure path, and a client that attaches and types before host.json exists
// must not find nobody draining its input.
//
// cur is left untouched when the spawn fails, so a failed restart cannot
// install a generation that does not exist.
func (h *hostRuntime) spawn(argv []string) error {
	cmd, ptmx, err := startChild(h.cfg, argv, h.rows, h.cols, h.log)
	if err != nil {
		return err
	}
	g := &childProc{
		cmd:     cmd,
		ptmx:    ptmx,
		exited:  make(chan struct{}),
		ptyDone: make(chan struct{}),
		retired: make(chan struct{}),
	}
	h.cur = g
	go h.waitChild(g)
	go h.pumpPTY(g)
	go h.writeChild(g)
	return nil
}

// closeCurrentPTY closes the live generation's master, which is what unblocks
// its pump. Retired generations are closed by the restart that retired them,
// so an "already closed" error here is that generation still being h.cur —
// impossible — rather than a double close; it is reported either way.
func (h *hostRuntime) closeCurrentPTY() {
	if h.cur == nil {
		return
	}
	if err := h.cur.ptmx.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		h.log.Printf("closing the pty master: %v", err)
	}
}

// run is the serve loop. It returns nil for an orderly shutdown.
//
// Every branch below must complete in bounded time, because this one
// goroutine is also what services resize, detach, exit, signals and newly
// accepted clients. That is why Data frames are not handled here: writing
// them to the PTY blocks for as long as the child declines to read its
// stdin, which is forever for a wedged child, and a host that cannot answer
// a detach is worse than the wedge it is reporting. Input goes to writeChild
// instead; see readClient.
func (h *hostRuntime) run(signals <-chan os.Signal) error {
	h.accepting = true
	go h.acceptLoop()
	// Started here rather than in newHostRuntime: it republishes host.json,
	// which Serve has only just written, and it must not race that first
	// write (nor run at all on a startup path that failed before it).
	go h.monitorLiveness()

	for {
		// A closed exited channel is permanently ready, and mid-restart that
		// is the ladder doing its job, not the child "exiting on its own" —
		// so the branch is disabled (nil channel) until finishRestart installs
		// the next generation.
		var exited <-chan struct{}
		if !h.restarting {
			exited = h.cur.exited
		}

		select {
		case c := <-h.clients:
			h.attach(c)

		case <-h.probes:
			// The liveness probe, on the serve loop because rows and cols
			// belong to it. Bounded by the same two Setsize calls and one
			// resizeNudgeDelay sleep an attaching client already costs, and
			// rate-limited to once per wedgeProbeInterval; waiting for the
			// answer happens in monitorLiveness, not here.
			h.nudgeRepaint()

		case hs := <-h.restartDone:
			// The ladder goroutine is done; the swap itself (retire, drain,
			// respawn, republish) happens here, on the loop, so cur, rows and
			// cols keep their single-goroutine ownership. Every step of it is
			// bounded; the unbounded part — waiting out the graces — already
			// happened off-loop.
			if err := h.finishRestart(hs); err != nil {
				return err
			}

		case f := <-h.ctrl:
			if !h.isCurrent(f.conn) {
				// A stolen or already-dropped client. Its reader
				// goroutine may still be draining; ignore it.
				continue
			}
			if f.err != nil {
				h.log.Printf("client read: %v; dropping client", f.err)
				h.dropClient(f.conn)
				continue
			}
			switch f.typ {
			case Resize:
				rows, cols, err := DecodeResize(f.payload)
				if err != nil {
					h.log.Printf("bad resize frame: %v", err)
					continue
				}
				h.resize(rows, cols)
			case Detach:
				h.log.Printf("client requested detach")
				h.dropClient(f.conn)
			case Exit:
				h.log.Printf("client requested exit; terminating child")
				h.terminateChild()
				return nil
			case Restart:
				// Bounded: beginRestart only validates the request and hands
				// the ladder to its own goroutine, so a slow restart can no
				// longer park this loop (and with it readClient, whose ctrl
				// sends block). The swap lands back here via restartDone.
				h.beginRestart()
			default:
				h.log.Printf("ignoring unexpected %v frame from client", f.typ)
			}

		case <-exited:
			h.log.Printf("child exited on its own; shutting down")
			h.drainPTY(h.cur)
			return nil

		case sig := <-signals:
			h.log.Printf("received %v; terminating child", sig)
			h.terminateChild()
			return nil
		}
	}
}

// acceptLoop feeds accepted connections to the serve loop. Accept is not
// gated on whether a client is already attached — a new connection steals.
//
// It stops when shutdown begins. A connection it is holding at that moment
// was never attached, but its client has no way to know that: a dial returns
// as soon as the kernel has queued the connection, long before this loop sees
// it. So the connection is ended the way an attached client's is, with an Exit
// frame, and left in h.unattached for shutdown to wait on. Closing it bare, as
// this loop once did, told that client the connection had dropped when the
// session had ended.
func (h *hostRuntime) acceptLoop() {
	defer close(h.acceptDone)
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			select {
			case <-h.done:
				// Also how shutdown stops this loop: it expires the
				// listener's deadline, which fails the Accept above. See
				// endUnattached.
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Transient (e.g. EMFILE): back off so this cannot spin.
			h.log.Printf("accept: %v", err)
			select {
			case <-time.After(50 * time.Millisecond):
			case <-h.done:
				return
			}
			continue
		}
		c := newClientConn(conn, h.log)
		h.afterAccept(h.done)
		select {
		case h.clients <- c:
		case <-h.done:
			c.end()
			h.mu.Lock()
			h.unattached = append(h.unattached, c)
			h.mu.Unlock()
			return
		}
	}
}

// attach makes c the active client, stealing the terminal from whoever held
// it: the previous client is told Stolen and closed. Steal, not multiplex —
// two clients rendering the same PTY cannot agree on a size.
func (h *hostRuntime) attach(c *clientConn) {
	h.mu.Lock()
	old := h.client
	h.client = c
	h.mu.Unlock()

	if old != nil {
		h.log.Printf("new client stole the session from the current client")
		old.sendStolen()
		// close() flushes what is already queued before closing the
		// connection; sendStolen's dedicated channel is independent of
		// that queue, so Stolen still reaches the client even under
		// send-queue backpressure.
		old.close()
	}
	h.log.Printf("client attached")
	go h.readClient(c)
}

// readClient decodes frames from c until it errors, relaying control frames
// (and the terminal error) to the serve loop and input straight to the child
// writer.
//
// Data is split off deliberately. This goroutine is the only reader of the
// socket, so parking it is the moment the client stops being heard at all,
// and h.ctrl is the only place it can park: input is the one high-rate frame
// type, so leaving it in that queue means a burst of keystrokes can fill the
// buffer and hold up the Detach or Exit sitting behind it. queueInput never
// blocks, so a flood is now dropped instead of backing up into this loop.
//
// The cost is that a Resize can overtake input queued just before it. That is
// harmless: a resize is an ioctl and a signal, not part of the child's input
// byte stream, and terminal size changes are asynchronous with typing anyway.
// Input keeps its own order, because one reader feeds one FIFO queue.
func (h *hostRuntime) readClient(c *clientConn) {
	for {
		t, payload, err := ReadFrame(c.conn)
		if err == nil && t == Data {
			h.queueInput(c, payload)
			continue
		}
		select {
		case h.ctrl <- clientFrame{conn: c, typ: t, payload: payload, err: err}:
		case <-h.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// queueInput hands one chunk of client input to writeChild. It never blocks:
// a full queue means the child is not reading its stdin, and the newest bytes
// are dropped rather than allowed to back up into readClient (which would
// stop the socket being read) or into the serve loop (which would stop the
// session being controllable). Dropping is the only option that keeps both —
// the child cannot be forced to read, and it must not be killed for it.
func (h *hostRuntime) queueInput(c *clientConn, payload []byte) {
	select {
	case h.childIn <- inputFrame{conn: c, payload: payload, queuedAt: time.Now()}:
	default:
		h.noteInputDrop(len(payload), h.queueFullReason())
	}
}

// queueFullReason describes a full input queue for host.log, naming how long
// the child has been refusing the in-flight write when that is known — the
// number an operator reading the log after the fact actually needs. An idle
// writer means the queue drained between the failed send and this call, so
// there is no stall to report.
func (h *hostRuntime) queueFullReason() string {
	if stalled := h.childWriteStalledFor(); stalled > 0 {
		return fmt.Sprintf("input queue of %d full; the child has not read its input for %s",
			childInputQueueDepth, stalled.Round(time.Millisecond))
	}
	return fmt.Sprintf("input queue of %d full", childInputQueueDepth)
}

// writeChild is the only writer of the child's PTY: the input direction's
// counterpart to clientConn.writeLoop on the output direction, and here for
// the same reason — the write can block for an unbounded time, so it gets a
// goroutine of its own with a bounded queue in front of it.
//
// Unlike writeLoop it cannot bound the write itself with a deadline: a PTY
// master is not a pollable fd, so ptmx.SetWriteDeadline fails with "file type
// does not support deadline" (verified on darwin), and there is no way to
// interrupt a write already in the kernel. So the bound lives on the queue
// instead — chunks are dropped once they are older than childInputStaleAfter
// — and a wedged write is simply left to finish if the child ever reads
// again. Nothing waits on it, so nothing else is delayed by it.
// A retired generation's writer stops as soon as it notices: retired is
// checked ahead of the queue on every iteration, so input typed around a
// restart follows the new child rather than the dead one. The residual window
// is one chunk — a chunk taken from the queue just as the generation is
// retired goes to the PTY that is about to close — and that is the right
// trade: those bytes were aimed at a screen the restart has already
// destroyed, exactly like the ones childInputStaleAfter drops.
//
// A write still BLOCKED in g.ptmx.Write when the restart closes this
// generation's master is also safe, for a reason stated where the close
// happens (restartChild, above old.ptmx.Close): os.File.Close on a
// blocking-mode fd defers the close(2) until the in-flight write releases
// its reference, so the write never lands on a recycled descriptor and the
// close never waits for it.
func (h *hostRuntime) writeChild(g *childProc) {
	for {
		select {
		case <-g.retired:
			return
		default:
		}
		select {
		case f := <-h.childIn:
			h.writeInput(g, f)
		case <-g.retired:
			return
		case <-h.done:
			// Whatever is still queued dies with the host; the child is
			// being torn down and cannot act on it anyway.
			return
		}
	}
}

func (h *hostRuntime) writeInput(g *childProc, f inputFrame) {
	switch v := classifyInput(h.isCurrent(f.conn), time.Since(f.queuedAt), childInputStaleAfter); v {
	case inputDropStale:
		h.noteInputDrop(len(f.payload), fmt.Sprintf("queued for more than %s while the child did not read", childInputStaleAfter))
		return
	case inputDropStolen:
		// Not a drop worth counting: the bytes were typed into a client
		// that no longer owns the session, so nobody is missing input the
		// child should have had.
		h.log.Printf("discarding %d bytes of input from a client that no longer holds the session", len(f.payload))
		return
	case inputWrite:
	default:
		h.log.Printf("unhandled input verdict %v; discarding %d bytes", v, len(f.payload))
		return
	}

	h.childWriteSince.Store(time.Now().UnixNano())
	_, err := g.ptmx.Write(f.payload)
	h.childWriteSince.Store(0)
	if err != nil {
		h.noteInputDrop(len(f.payload), fmt.Sprintf("writing to the pty: %v", err))
	}
}

// childWriteStalledFor reports how long the in-flight write to the child has
// been outstanding, or 0 when the writer is idle. Advisory: it exists to make
// the log line say how bad the wedge is, and races with the write completing.
func (h *hostRuntime) childWriteStalledFor() time.Duration {
	since := h.childWriteSince.Load()
	if since == 0 {
		return 0
	}
	return time.Since(time.Unix(0, since))
}

// noteInputDrop records that input was lost and says so in host.log, which is
// the only place the operator can find out. Rate-limited to one line per
// inputDropLogInterval, carrying a running total, because a wedged child
// produces a drop per keystroke and an unbounded log is its own outage; the
// first drop always logs, so the loss is never reported late.
func (h *hostRuntime) noteInputDrop(n int, reason string) {
	h.inputDropMu.Lock()
	h.inputDropped++
	total := h.inputDropped
	now := time.Now()
	quiet := !h.inputDropLast.IsZero() && now.Sub(h.inputDropLast) < inputDropLogInterval
	if !quiet {
		h.inputDropLast = now
	}
	h.inputDropMu.Unlock()

	if quiet {
		return
	}
	h.log.Printf("dropped %d bytes of input for session %q (%s); %d chunks dropped so far", n, h.cfg.Name, reason, total)
}

// attached reports whether any client currently holds the session, which is
// what gates the liveness probe: an unattached session has no screen to
// repaint. See decideProbe.
func (h *hostRuntime) attached() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.client != nil
}

// isCurrent reports whether c is the attached client.
func (h *hostRuntime) isCurrent(c *clientConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return c != nil && h.client == c
}

// dropClient detaches c and closes it; the host keeps serving.
func (h *hostRuntime) dropClient(c *clientConn) {
	h.mu.Lock()
	if h.client == c {
		h.client = nil
	}
	h.mu.Unlock()
	c.close()
	h.log.Printf("client detached; host still serving")
}

// pumpPTY copies child output to the attached client. It reads the PTY
// unconditionally — while no client is attached the output is DISCARDED
// rather than buffered (no unbounded memory) and reading never stops (a
// full PTY buffer would block the child). Reattaching redraws via the
// resize nudge, so there is nothing to replay.
func (h *hostRuntime) pumpPTY(g *childProc) {
	defer close(g.ptyDone)

	buf := make([]byte, ptyReadBufSize)
	for {
		n, err := g.ptmx.Read(buf)
		if n > 0 {
			// Before the client lookup, and whether or not anyone is
			// attached: this is the host's only evidence that the child is
			// still alive, and it is worth just as much while detached.
			h.live.noteOutput(time.Now())
			h.mu.Lock()
			c := h.client
			h.mu.Unlock()
			if c != nil {
				// Copy: buf is reused, and the frame is written
				// later by the client's writer goroutine.
				chunk := append([]byte(nil), buf[:n]...)
				if !c.send(Data, chunk) && h.isCurrent(c) {
					// dropClient rather than a bare close: it also clears
					// h.client, so the rest of this burst is discarded like
					// unattached output instead of logging one line per
					// chunk against a client that is already gone. The close
					// carries no Exit frame, which is how the client tells
					// this apart from the session ending (DetachDropped).
					h.log.Printf("client is not consuming output (more than %d bytes behind); dropping it", clientPendingLimit)
					h.dropClient(c)
				}
			}
		}
		if err != nil {
			// EIO (Linux) or EOF once the child's side of the PTY is
			// gone. Either way the child is finished writing.
			return
		}
	}
}

// resize applies a client's terminal size to the PTY, which delivers
// SIGWINCH to the child. When the size is unchanged the kernel still
// delivers SIGWINCH, but a child that re-queries and sees the same size
// often skips repainting — so nudge the COLUMN count (a column change
// reflows less visibly than a row change) and put it straight back. This is
// how attaching redraws the screen.
func (h *hostRuntime) resize(rows, cols uint16) {
	if rows == 0 || cols == 0 {
		h.log.Printf("ignoring resize to %dx%d", rows, cols)
		return
	}
	if rows == h.rows && cols == h.cols {
		h.nudgeRepaint()
		return
	}
	h.setSize(rows, cols)
	h.log.Printf("resized pty to %dx%d", rows, cols)
}

// nudgeRepaint asks the child to repaint at its current size, by changing the
// column count and putting it straight back. Extracted from resize because
// the liveness probe asks exactly the same question of the child (see
// liveness.go) and asking it a second way would be a second thing to keep
// working. Serve-loop only: it reads and writes h.rows/h.cols.
func (h *hostRuntime) nudgeRepaint() {
	rows, cols := h.rows, h.cols
	nudge := cols - 1
	if cols <= 1 {
		nudge = cols + 1
	}
	h.setSize(rows, nudge)
	time.Sleep(resizeNudgeDelay)
	h.setSize(rows, cols)
	h.log.Printf("nudged pty to force a repaint at %dx%d", rows, cols)
}

func (h *hostRuntime) setSize(rows, cols uint16) {
	if err := pty.Setsize(h.cur.ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		h.log.Printf("pty.Setsize(rows=%d, cols=%d): %v", rows, cols, err)
		return
	}
	h.rows, h.cols = rows, cols
}

// waitChild reaps one generation's child and announces it by closing that
// generation's exited channel.
func (h *hostRuntime) waitChild(g *childProc) {
	err := g.cmd.Wait()
	if err != nil {
		h.log.Printf("child (pid %d) exited: %v", g.pid(), err)
	} else {
		h.log.Printf("child (pid %d) exited cleanly", g.pid())
	}
	close(g.exited)
}

// terminateChild takes the child down: SIGTERM, up to childTermGrace to go
// quietly, then SIGKILL. It returns once the child is reaped (or is proven
// unkillable).
func (h *hostRuntime) terminateChild() {
	g := h.cur
	if g == nil || g.gone() {
		return
	}

	h.signal(g, syscall.SIGTERM)
	select {
	case <-g.exited:
	case <-time.After(childTermGrace):
		h.log.Printf("child still alive %s after SIGTERM; sending SIGKILL", childTermGrace)
		h.signal(g, syscall.SIGKILL)
		select {
		case <-g.exited:
		case <-time.After(childKillGrace):
			h.log.Printf("child (pid %d) did not exit after SIGKILL; abandoning it", g.pid())
		}
	}
	h.drainPTY(g)
}

// signalChild signals the child's whole process group, falling back to just
// the child. pty.StartWithSize puts the child in a new session, so it leads
// its own process group (pgid == pid) and anything it spawned goes down
// with it.
func (h *hostRuntime) signal(g *childProc, sig syscall.Signal) {
	pid := g.pid()
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, sig); err == nil {
		return
	}
	if err := g.cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		h.log.Printf("signalling child (pid %d) with %v: %v", pid, sig, err)
	}
}

// drainPTY gives the pump a bounded moment to forward the child's final
// output before the connection is torn down.
func (h *hostRuntime) drainPTY(g *childProc) {
	select {
	case <-g.ptyDone:
	case <-time.After(ptyDrainGrace):
	}
}

// shutdown stops the goroutines and closes every client with a trailing Exit
// frame, which is what tells it the session ended rather than that it was
// dropped (a bare EOF, see DetachDropped). Idempotent.
//
// "Every client" is the attached one and any whose connection reached the
// host without being attached; see endUnattached for the second kind. They
// share one clientFlushGrace for the flush rather than getting one each, so
// the time shutdown takes does not grow with the number of clients.
func (h *hostRuntime) shutdown() {
	h.shutdownOnce.Do(func() { close(h.done) })
	// Before anything else, and before Serve's later deferred removal of
	// host.json: a liveness publish landing after that removal would
	// recreate the file for a session that no longer exists.
	h.stopPublishing()

	h.mu.Lock()
	c := h.client
	h.client = nil
	h.mu.Unlock()
	var ending []*clientConn
	if c != nil {
		// Ended before the unattached connections are collected, so the
		// child's final output is already flushing while that happens.
		c.end()
		ending = append(ending, c)
	}
	ending = append(ending, h.endUnattached()...)
	waitFlushed(ending, clientFlushGrace)
}

// endUnattached ends, with an Exit frame, every connection that reached the
// host before shutdown without being attached, and returns them so shutdown
// can wait for the frames to leave. It does not wait for that itself.
//
// Such a connection is in one of two places. acceptLoop may be holding it,
// having accepted it and not yet handed it to the serve loop, which has
// stopped receiving. Or it may still be in the listen queue, never accepted:
// connecting to a unix socket succeeds once the kernel has queued the
// connection, with no part played by this process. Either way the client
// thinks it is connected, and both used to end badly for it. The first got a
// bare close and the second a reset when Serve closed the listener, and attach
// reports each as "connection dropped" for a session that in fact ended. It
// was seen as TestServeSIGTERMBehavesLikeExit failing about one run in
// fifteen, and a user hits it by opening a session at the moment it ends.
//
// The order matters. acceptLoop is stopped first and its connection
// collected, and only then is the queue emptied, so nothing is accepting
// behind the drain's back when it finds the queue empty.
//
// Stopping acceptLoop means expiring the listener's deadline, because the
// loop is normally parked inside Accept and closing h.done does not wake it.
// An expired deadline fails that Accept, and every later one before it
// reaches the kernel (internal/poll's FD.Accept checks the deadline first),
// which is also why the queue cannot be emptied by calling Accept and has to
// go through acceptQueued.
//
// Every step is bounded by clientFlushGrace: the wait for acceptLoop, and the
// drain, which a client dialing in a loop could otherwise keep fed for ever.
// A connection missed because a bound ran out is reset as before, which
// attach reports as a drop and discovery then corrects; the same honest
// direction endAndWait fails in.
func (h *hostRuntime) endUnattached() []*clientConn {
	if !h.accepting {
		// acceptLoop never started (Serve failed before run), or a second
		// shutdown call. Nothing was accepted, and nobody will close
		// acceptDone, so do not wait on it.
		return nil
	}
	h.accepting = false

	ln, ok := h.ln.(*net.UnixListener)
	if !ok {
		// bindLocked only ever returns a unix listener. Without one there
		// is neither a deadline to stop acceptLoop with nor a queue this
		// code knows how to read, so the connections are left to the
		// listener's close, as they were before.
		return nil
	}
	limit := time.Now().Add(clientFlushGrace)
	if err := ln.SetDeadline(time.Now()); err != nil {
		h.log.Printf("stopping the accept loop: %v", err)
	}
	select {
	case <-h.acceptDone:
	case <-time.After(clientFlushGrace):
		// Draining anyway is safe: the kernel gives each queued connection
		// to exactly one accepter. The connection acceptLoop holds is then
		// ended by acceptLoop itself, just not waited for.
		h.log.Printf("the accept loop did not stop within %s; draining the listen queue without it", clientFlushGrace)
	}

	h.mu.Lock()
	ended := h.unattached
	h.unattached = nil
	h.mu.Unlock()

	queued, err := acceptQueued(ln, limit)
	if err != nil {
		h.log.Printf("draining the listen queue: %v", err)
	}
	for _, conn := range queued {
		c := newClientConn(conn, h.log)
		c.end()
		ended = append(ended, c)
	}
	if len(ended) > 0 {
		h.log.Printf("told %d connection(s) that had not attached yet that the session ended", len(ended))
	}
	return ended
}

// acceptQueued takes every connection waiting in ln's listen queue without
// ever waiting for one to arrive, and gives up at limit.
//
// It calls accept(2) on the listener's descriptor directly because that is the
// only way to ask "is anything queued right now?". ln.Accept parks until a
// connection arrives, and a listener deadline does not turn it into a poll: a
// deadline in the future makes it wait that long on an empty queue, which
// would add the wait to every shutdown, and an expired one fails it before it
// looks at the queue at all. The descriptor is already non-blocking (the net
// package opens every socket that way so it can use the poller), so accept
// returns EAGAIN the moment the queue is empty.
//
// Control is used rather than RawConn.Read because Read takes the descriptor's
// read lock, which an acceptLoop still parked in Accept would hold, and fails
// on the expired deadline for the same reason Accept does.
//
// ForkLock and CloseOnExec are the syscall package's documented way to create
// a descriptor that a concurrent fork and exec cannot leak into a child; the
// accepted socket must not be inherited by anything the process starts.
func acceptQueued(ln *net.UnixListener, limit time.Time) ([]net.Conn, error) {
	rc, err := ln.SyscallConn()
	if err != nil {
		return nil, err
	}
	var conns []net.Conn
	var acceptErr error
	err = rc.Control(func(fd uintptr) {
		for time.Now().Before(limit) {
			syscall.ForkLock.RLock()
			nfd, _, err := syscall.Accept(int(fd))
			if err == nil {
				syscall.CloseOnExec(nfd)
			}
			syscall.ForkLock.RUnlock()
			switch {
			case err == nil:
			case errors.Is(err, syscall.EINTR), errors.Is(err, syscall.ECONNABORTED):
				// Interrupted, or the client gave up while queued: neither
				// says the queue is empty.
				continue
			case errors.Is(err, syscall.EAGAIN):
				return
			default:
				acceptErr = err
				return
			}
			// net.FileConn works on a duplicate of the descriptor, so the
			// original is closed either way.
			f := os.NewFile(uintptr(nfd), "afk-host-queued-client")
			conn, err := net.FileConn(f)
			_ = f.Close()
			if err != nil {
				acceptErr = err
				continue
			}
			conns = append(conns, conn)
		}
	})
	if err != nil {
		return conns, err
	}
	return conns, acceptErr
}

// waitFlushed waits for every connection in conns to finish its flush, for at
// most d in total. One timer for all of them, not one each: the wait is for
// the slowest client, and must not add up across clients.
func waitFlushed(conns []*clientConn, d time.Duration) {
	if len(conns) == 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	for _, c := range conns {
		select {
		case <-c.flushed:
		case <-timer.C:
			return
		}
	}
}

// clientConn is the attached client's connection with a single writer
// goroutine. Every host→client frame goes through the pending list, so frames
// can never interleave mid-frame, and no caller of send can be blocked by a
// slow client. Stolen has its own single-slot channel (stolen) instead of going
// through pending: pending is bounded (clientPendingLimit) and can fill under
// backpressure from a client that has stopped reading, and a stolen client
// must still reliably learn it was stolen rather than having that one frame
// silently dropped like an overflowed Data frame.
type clientConn struct {
	conn net.Conn
	log  *log.Logger
	// wake is a single-slot "pending or closed changed" signal for writeLoop.
	wake chan struct{}
	// stolen is a dedicated, single-slot signal for the Stolen frame,
	// checked by writeLoop ahead of pending on every iteration so a full
	// pending list cannot delay or drop it.
	stolen chan struct{}
	// flushed closes once the writer goroutine has drained pending and
	// closed the connection.
	flushed chan struct{}

	// mu guards everything below. It is only ever held across slice
	// bookkeeping and non-blocking channel operations, never a write.
	mu           sync.Mutex
	pending      []outFrame
	pendingBytes int
	closed       bool
	// ended marks a close that is the session ending (endAndWait), so
	// writeLoop follows the drained output with an Exit frame.
	ended bool
}

type outFrame struct {
	typ     MessageType
	payload []byte
}

func newClientConn(conn net.Conn, logger *log.Logger) *clientConn {
	c := &clientConn{
		conn:    conn,
		log:     logger,
		wake:    make(chan struct{}, 1),
		stolen:  make(chan struct{}, 1),
		flushed: make(chan struct{}),
	}
	go c.writeLoop()
	return c
}

func (c *clientConn) writeLoop() {
	defer close(c.flushed)
	defer func() { _ = c.conn.Close() }()

	for {
		// Checked first, and again in the select below: a Stolen signalled
		// while output is also pending must win immediately rather than
		// waiting behind it — that output is stale PTY bytes the client no
		// longer needs once it has been stolen from.
		if c.takeStolen() {
			return
		}
		select {
		case <-c.stolen:
			c.writeStolen()
			return
		case <-c.wake:
		}

		// Take everything queued in one go. While the previous batch was
		// being written, consecutive Data frames were merging in pending
		// (appendFrame), so a client that fell behind gets its backlog as a
		// few large frames rather than thousands of tiny ones.
		c.mu.Lock()
		batch := c.pending
		c.pending = nil
		c.pendingBytes = 0
		closed, ended := c.closed, c.ended
		c.mu.Unlock()

		for _, f := range batch {
			if c.takeStolen() {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
			if err := WriteFrame(c.conn, f.typ, f.payload); err != nil {
				c.log.Printf("writing %v frame to client: %v", f.typ, err)
				return
			}
		}
		if closed {
			// Only a session ending gets Exit. A plain close — a detach or a
			// dropped client — must stay a bare EOF, since that absence is
			// the client's only signal the session is still running.
			if ended {
				_ = c.conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
				if err := WriteFrame(c.conn, Exit, nil); err != nil {
					c.log.Printf("writing Exit frame to client: %v", err)
				}
			}
			return
		}
	}
}

// takeStolen writes Stolen and reports true if a steal has been signalled.
func (c *clientConn) takeStolen() bool {
	select {
	case <-c.stolen:
		c.writeStolen()
		return true
	default:
		return false
	}
}

// maxMergedFrame caps one merged Data frame, far under MaxFramePayload.
const maxMergedFrame = 256 << 10

// appendFrame queues f behind pending, merging it into the last frame when
// both are Data and the result stays within maxMergedFrame.
//
// The merge is the point. Bounding the queue by frame count (as a 256-slot
// channel once did) makes the bound count PTY reads, not bytes: a child that
// repaints in thousands of tiny writes (observed: codex, answering the attach
// nudge) produces thousands of few-byte frames, each paying a header, two
// syscalls and a client-side emulator write, and a healthy client got dropped
// as "slow". Merging at enqueue time means a backlog costs the client per
// byte, not per write the child happened to make.
//
// Order is preserved: only adjacent Data frames merge, so a non-Data frame
// stays exactly where it was queued relative to the output around it.
func appendFrame(pending []outFrame, f outFrame) []outFrame {
	if n := len(pending); n > 0 && f.typ == Data && pending[n-1].typ == Data &&
		len(pending[n-1].payload)+len(f.payload) <= maxMergedFrame {
		pending[n-1].payload = append(pending[n-1].payload, f.payload...)
		return pending
	}
	return append(pending, f)
}

func (c *clientConn) writeStolen() {
	_ = c.conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
	if err := WriteFrame(c.conn, Stolen, nil); err != nil {
		c.log.Printf("writing Stolen frame to client: %v", err)
	}
}

// send queues one frame. It never blocks: false means the frame was not
// queued — the client is either already closed or more than
// clientPendingLimit bytes behind — and the caller should drop it. Stolen
// notifications do not use send; see sendStolen.
//
// payload must not be reused by the caller: a Data payload may be appended to
// in place when the next one merges into it.
func (c *clientConn) send(typ MessageType, payload []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if c.pendingBytes+len(payload) > clientPendingLimit {
		return false
	}
	c.pending = appendFrame(c.pending, outFrame{typ: typ, payload: payload})
	c.pendingBytes += len(payload)
	c.signal()
	return true
}

// signal wakes writeLoop. Single-slot and non-blocking: one pending token
// covers any number of changes, since writeLoop takes everything at once.
// The caller must hold mu.
func (c *clientConn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// sendStolen signals writeLoop to deliver a Stolen frame ahead of anything
// pending, so a client that has fallen behind on Data still reliably learns it
// was stolen instead of that notification silently dropping like an
// overflowed Data frame would. The stolen channel is buffered 1 and only ever
// signalled once per clientConn (attach() calls this at most once, right
// before closing the client), so the send below cannot block.
func (c *clientConn) sendStolen() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.stolen <- struct{}{}:
	default:
	}
}

// close flushes already-queued frames, then closes the connection. It does
// not wait, so a wedged client cannot stall the serve loop. Idempotent.
func (c *clientConn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.signal()
}

// end is close for a session that is ending: the connection is closed after
// its queued frames and a trailing Exit frame. Like close it does not wait.
// A connection that was already closed stays a bare close.
func (c *clientConn) end() {
	c.mu.Lock()
	if !c.closed {
		c.ended = true
	}
	c.mu.Unlock()
	c.close()
}

// endAndWait is end plus a bounded wait for the flush, so the child's final
// output — and the Exit frame after it — reaches the client before the socket
// closes. A client that is too far behind to receive Exit within d sees a bare
// EOF and reads it as DetachDropped, which discovery then corrects; that is
// the honest direction to fail in.
func (c *clientConn) endAndWait(d time.Duration) {
	c.end()
	waitFlushed([]*clientConn{c}, d)
}

// listenExclusive binds the host's unix socket. A leftover socket file is
// only removed once a dial has PROVEN nobody is answering on it: the dial
// is authoritative, and host.json's PID is not consulted at all. Alive() is
// advisory (a recycled PID false-positives), and a stale PID must never
// block startup — otherwise the dashboard's restart-a-stopped-session flow
// can never recover. Conversely, a host that does answer keeps us out: two
// concurrent restarts of one session must not end up as two hosts running
// two claudes.
// The ENTIRE bind runs under the lock, including the first attempt. An
// unlocked fast path is what made this racy: a contender arriving between a
// lock-holder's os.Remove and its rebind would bind the just-freed path without
// ever taking the lock, so two hosts could come up on one session — the precise
// outcome the lock exists to prevent.
func listenExclusive(socket string, logger *log.Logger) (net.Listener, error) {
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating socket dir %s: %w", dir, err)
	}

	lockPath := socket + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening socket lock %s: %w", lockPath, err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("locking %s: %w", lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	return bindLocked(socket, logger)
}

// isBindConflict reports whether a unix-socket bind failed because the path
// is already taken. EADDRINUSE is the usual errno, but two binds racing to
// CREATE the same path can lose with EEXIST instead (the kernel's file-create
// race, observed on darwin) — both mean the same thing here: somebody else
// has the path, go ask the dial probe who.
func isBindConflict(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.EEXIST)
}

// bindLocked binds the socket, recovering a stale socket file if one is in the
// way. The caller MUST hold the <socket>.lock flock: everything here — the
// bind, the liveness probe, the unlink, the rebind — is one critical section,
// and the probe→remove→rebind window is unsafe outside it (a concurrent
// restart's unlink would drop the socket this one just bound, leaving two hosts
// running two claudes on one session).
//
// The lock file itself is never removed: unlinking it would let whoever is
// blocked on it proceed against a different inode, reopening the same race. It
// is a zero-byte file beside the socket in $TMPDIR, and the lock releases
// implicitly when the fd closes.
func bindLocked(socket string, logger *log.Logger) (net.Listener, error) {
	ln, err := net.Listen("unix", socket)
	if err == nil {
		return ln, nil
	}
	if !isBindConflict(err) {
		return nil, fmt.Errorf("listening on %s: %w", socket, err)
	}

	// The dial is authoritative for liveness: a host that answers keeps us out,
	// and host.json's PID is never consulted (see listenExclusive's doc). Holding
	// the lock is what makes this trustworthy — no competitor can be mid-bind
	// here, so "nobody answers" really does mean the file is abandoned.
	probe, dialErr := net.DialTimeout("unix", socket, dialProbeTimeout)
	if dialErr == nil {
		_ = probe.Close()
		return nil, fmt.Errorf("%w: a host is already listening on %s", ErrHostAlreadyRunning, socket)
	}
	logger.Printf("socket %s exists but does not answer (%v); treating it as stale", socket, dialErr)

	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("removing stale socket %s: %w", socket, err)
	}
	ln, err = net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("listening on %s after clearing a stale socket: %w", socket, err)
	}
	return ln, nil
}

// startChild launches argv under a fresh PTY of the given size, in
// cfg.Worktree. argv is a parameter rather than cfg.Argv because a restart
// runs a different command line (the provider's resume command, see
// restart.go's resumeArgv) in the same session, and rows/cols are parameters
// so a resumed child inherits the attached client's real terminal size
// instead of reverting to 24x80.
func startChild(cfg ServeConfig, argv []string, rows, cols uint16, logger *log.Logger) (*exec.Cmd, *os.File, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cfg.Worktree
	cmd.Env = childEnv(os.Environ())

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, nil, fmt.Errorf("starting child %q under a pty: %w", argv, err)
	}
	logger.Printf("child started: pid=%d cwd=%s argv=%q", cmd.Process.Pid, cfg.Worktree, argv)
	return cmd, ptmx, nil
}

// inheritedSessionVars are the variables an agent uses to tell its own tools
// which session they run in. Each agent sets its own on startup, so a value
// arriving from the host's environment can only be a STRANGER's: a host
// spawned by a afk process that itself ran inside a Claude session carries
// that session's CLAUDE_CODE_SESSION_ID. A codex child sets only the CODEX_*
// pair and passes the stray Claude id through to its tool calls, where the
// conductor reads it as its own and records it in status.json — so restart
// resumes the wrong conversation and hooks, which target by session_id
// (status-schema.md §7), notify the wrong session. Verified on codex-cli
// 0.162.0: a nested `codex exec` saw both its own CODEX_SESSION_ID and the
// parent Claude session's CLAUDE_CODE_SESSION_ID.
var inheritedSessionVars = []string{
	"CLAUDE_CODE_SESSION_ID",
	"CODEX_SESSION_ID",
	"CODEX_THREAD_ID",
}

// childEnv returns env without inheritedSessionVars and with a TERM
// guaranteed. The host is spawned detached and may have inherited a bare
// environment; a child TUI with no TERM degrades badly. An existing TERM (even
// empty) is respected — duplicating the key would be worse than honoring it.
// It never mutates env.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	hasTERM := false
	for _, kv := range env {
		if isInheritedSessionVar(kv) {
			continue
		}
		if strings.HasPrefix(kv, "TERM=") {
			hasTERM = true
		}
		out = append(out, kv)
	}
	if !hasTERM {
		out = append(out, "TERM="+fallbackTERM)
	}
	return out
}

func isInheritedSessionVar(kv string) bool {
	for _, name := range inheritedSessionVars {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

// hostMeta is the exact shape this host writes to host.json: the shared Meta
// contract plus child_pid, the hosted process currently on the PTY.
//
// PID and ChildPID are two different processes and must never be conflated.
// Meta.PID is the HOST's, the process that owns the socket and outlives every
// attached UI; internal/discover's HostRunning signal-0s it to decide whether
// a session is reachable at all, and attach/stop use it to find the host, so
// it is stable for the host's whole life — a restart replaces the child and
// leaves PID alone. ChildPID is what restart.go replaces in place, and it
// exists so that a restart is observable from outside the host: same pid,
// same socket, different child.
//
// It lives here rather than in Meta (meta.go) because child_pid is the
// host's own writing concern, not part of the contract readers depend on:
// ReadMeta's plain json.Unmarshal drops the key it does not know, so every
// existing reader parses this host's files unchanged, and a host.json
// written before the field existed parses here with ChildPID zero
// ("unknown"), the same additive rule Meta's liveness fields follow.
//
// Consequence, stated so nobody rediscovers it as a surprise: no afk code
// READS child_pid back today (ReadMeta returns Meta, which does not carry
// it). It is written for observers outside this process — an operator
// inspecting host.json, the restart tests' before/after assertions — and
// for the dashboard, the day it wants to show the child. Promoting it into
// Meta is the tracked follow-up in the plan notepad, not a change to smuggle
// in here.
type hostMeta struct {
	Meta
	ChildPID int `json:"child_pid,omitempty"`
}

// writeMetaFile persists m at an explicit path, atomically (same-directory
// temp file + rename), so a concurrent reader never sees a torn host.json.
// Serve is handed host.json's location as configuration (`afk _host
// --meta`) rather than deriving it from repoRoot+name the way WriteMeta
// does; honoring that flag means writing to the given path.
//
// Generic over the two shapes host.json is legitimately written in — the
// host's own hostMeta and the bare Meta some test scaffolding seeds — rather
// than taking `any`, so a refactor cannot silently persist something that is
// not host.json at all.
func writeMetaFile[M interface{ Meta | hostMeta }](path string, m M) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling host meta: %w", err)
	}
	data = append(data, '\n')

	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d", filepath.Base(path), os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp host meta: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming host meta into place: %w", err)
	}
	return nil
}

// openLog opens the host's own log file in append mode (the host outlives
// individual clients and may be restarted; history is worth keeping).
func openLog(path string) (*log.Logger, func(), error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating log dir %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("opening host log %s: %w", path, err)
	}
	logger := log.New(f, fmt.Sprintf("[host %d] ", os.Getpid()), log.LstdFlags|log.Lmicroseconds)
	return logger, func() { _ = f.Close() }, nil
}

func removeQuietly(path string, logger *log.Logger) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Printf("removing %s: %v", path, err)
	}
}

// removeOurMeta deletes host.json, but only while it still names this
// process. A successor host that hijacked our stale socket may already have
// republished it, and removing that would make a live session look stopped.
// Unparseable content is ours to clean up.
func removeOurMeta(path string, logger *log.Logger) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Printf("reading %s before removing it: %v", path, err)
		}
		return
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err == nil && m.PID != os.Getpid() {
		logger.Printf("leaving %s in place: it names pid %d, not us", path, m.PID)
		return
	}
	removeQuietly(path, logger)
}
