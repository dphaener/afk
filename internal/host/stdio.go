package host

// The stdio host. With ServeConfig.Stdio the host runs as a Rex block's
// command (`afk _host --stdio`): its one client is its own terminal, which is
// the Rex block, and that client is never stolen or dropped. The socket still
// takes the daemon's, the web page's and the dashboard's requests (typing,
// restart, exit); a socket client that asks to attach, which is the Resize an
// interactive attach opens with, is refused with a plain sentence.
//
// The host also writes afk's own Program status record (id=afk) into the
// terminal, in the state status.json says. It must never land inside one of
// the agent's escape sequences or UTF-8 characters: the terminal would show
// garbage or drop the agent's sequence. So one progstatus.Scanner follows the
// OUTER stream, the bytes as the terminal receives them, and it is kept across
// child restarts, because the terminal's parser does not reset when the child
// does: a sequence a dying child left open is still open in the terminal. That
// scanner is apart from pumpPTY's per-generation one, which feeds agent_status
// and must start fresh for each child (docs/PLAN.md, T17.1 amendment (3)).
//
// The design was measured in the T17.1 prototype (docs/decisions.md, T17.1);
// this is a rewrite of it, with the record taken from the files instead of a
// counter and sent on change rather than on a fixed timer (StdioHeartbeat).

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	xterm "github.com/charmbracelet/x/term"

	"github.com/dphaener/afk/internal/progstatus"
	"github.com/dphaener/afk/internal/state"
)

// RefuseAttachLine is what a socket client that asks to attach to a stdio
// host is told. The session's screen is its Rex block, and a second screen
// could not agree with it on a size.
const RefuseAttachLine = "This session runs in Rex; open it there."

// Timings of the stdio host. Package-level, like livenessTiming and
// restartLadder, so a test can shorten them; startStdio copies them once, so
// a host that outlives a test never reads the variables again.
var (
	// stdioStatusEvery is how often status.json and the park flag are read.
	// Two seconds matches the dashboard's own discovery tick: the writers
	// change these files at human speed, and a record a moment late is
	// harmless where a busy loop of file reads is not.
	stdioStatusEvery = 2 * time.Second
	// stdioTick is how often the terminal writer looks at the latest view
	// while the child is quiet. Output wakes it too; the tick is for a quiet
	// agent whose phase changed.
	stdioTick = 500 * time.Millisecond
	// stdioSafeWait is StdioSafeWait, as a variable for tests.
	stdioSafeWait = StdioSafeWait
	// stdioFlushGrace bounds how long shutdown waits for the terminal
	// writer to drain and write its final clear. A terminal that stopped
	// reading would otherwise hold the host open for ever.
	stdioFlushGrace = time.Second
	// windowCallDeadline bounds each WindowControl call the host waits for.
	// About two seconds: a relabel is cosmetic, and the close at exit must
	// not keep a finished host alive for long when Rex does not answer.
	windowCallDeadline = 2 * time.Second
)

// WindowControl is the host's handle on the Rex window it runs in. It is an
// interface so this package never imports internal/rex: the host must run,
// and be tested, with no Rex at all, and host_cmd.go is where the real calls
// are wired in. Both calls may block (they run a command), so the host only
// ever makes them on goroutines of their own, under windowCallDeadline.
type WindowControl interface {
	// SetLabel renames the window.
	SetLabel(label string) error
	// CloseSelf closes the host's own block, and with it the window.
	CloseSelf() error
}

// stdioQueueDepth bounds the chunks waiting for the terminal. A full queue
// blocks the PTY pump, as a real terminal that stops reading blocks its
// program: dropping terminal output would corrupt the screen for good
// (docs/PLAN.md, T17.1 amendment (5)).
const stdioQueueDepth = 256

// Terminal is the terminal a stdio host serves. ServeConfig.Terminal nil
// means the process's own: os.Stdin, os.Stdout, the size of os.Stdout and
// SIGWINCH. A test passes a pipe pair, a fixed size and a channel of its own.
type Terminal struct {
	// In is what is typed at the terminal; nil means os.Stdin.
	In *os.File
	// Out is the terminal's screen; nil means os.Stdout.
	Out *os.File
	// Size reports the terminal's size; ok is false when it cannot be read.
	// nil reads Out's size.
	Size func() (rows, cols uint16, ok bool)
	// Winch receives a value each time the terminal's size may have changed.
	// nil means no resize notifications for a Terminal given in config (the
	// process's SIGWINCH is used only when Terminal itself is nil).
	Winch <-chan struct{}
}

// termChunk is one write for the terminal: the child's output, or a host
// line (notifyClient's "[afk] …"), which waits for a safe offset like afk's
// record does.
type termChunk struct {
	b    []byte
	host bool
}

// stdioHost is the stdio-only state of a hostRuntime. hostRuntime.stdio is
// nil for every host that is not stdio, and each stdio branch tests it.
type stdioHost struct {
	in       *os.File
	out      *os.File
	size     func() (rows, cols uint16, ok bool)
	winch    <-chan struct{}
	sigwinch bool // listen for the process's SIGWINCH (the default terminal)

	// conn is the sentinel identity for input typed at the terminal, so the
	// input queue's ownership check (isCurrent) lets it through. It is never
	// written to and has no writer goroutine.
	conn *clientConn

	// mu guards requesters: socket connections the host reads requests
	// from but never sends the screen to. Its own mutex rather than h.mu,
	// which the pump takes on every chunk of a non-stdio host.
	mu         sync.Mutex
	requesters map[*clientConn]struct{}

	// resized carries the terminal's latest size to the serve loop, which
	// owns rows and cols. One slot, replaced rather than queued: only the
	// latest size matters.
	resized chan [2]uint16

	// restartFor is the requester whose Restart is in flight, which is who
	// gets the RestartResult. Serve-loop owned, like h.restarting.
	restartFor *clientConn

	// view is the status watcher's latest reading, which the writer reads.
	view atomic.Pointer[PhaseView]
	// lastReadable is the watcher's latest readable reading, nil until
	// status.json has been read once. The close decision at exit falls back
	// to it when the file cannot be read at that moment.
	lastReadable atomic.Pointer[PhaseView]

	// window keeps the Rex window's label up to date; nil when
	// ServeConfig.Window is.
	window *windowCtl

	ch       chan termChunk
	stop     chan struct{}
	stopOnce sync.Once
	ended    chan struct{}
	started  bool

	statusEvery time.Duration
	tick        time.Duration
	safeWait    time.Duration
}

// openStdio resolves the terminal, sizes the first child from it and puts a
// terminal that is one into raw mode, all before the child is spawned. The
// returned restore puts the terminal back and must run after shutdown, so the
// final clear of afk's record is written to the terminal as it was used.
func (h *hostRuntime) openStdio() (func(), error) {
	s := &stdioHost{
		conn:        &clientConn{},
		requesters:  map[*clientConn]struct{}{},
		resized:     make(chan [2]uint16, 1),
		ch:          make(chan termChunk, stdioQueueDepth),
		stop:        make(chan struct{}),
		ended:       make(chan struct{}),
		statusEvery: stdioStatusEvery,
		tick:        stdioTick,
		safeWait:    stdioSafeWait,
	}
	t := h.cfg.Terminal
	if t == nil {
		t = &Terminal{}
		s.sigwinch = true
	}
	s.in, s.out, s.size, s.winch = t.In, t.Out, t.Size, t.Winch
	if s.in == nil {
		s.in = os.Stdin
	}
	if s.out == nil {
		s.out = os.Stdout
	}
	if s.size == nil {
		out := s.out
		s.size = func() (uint16, uint16, bool) { return stdioTerminalSize(out) }
	}

	if rows, cols, ok := s.size(); ok {
		h.rows, h.cols = rows, cols
	} else {
		h.log.Printf("stdio host: could not read the terminal's size; starting at %dx%d", h.cols, h.rows)
	}

	restore := func() {}
	if fd := s.in.Fd(); xterm.IsTerminal(fd) {
		st, err := xterm.MakeRaw(fd)
		if err != nil {
			return nil, fmt.Errorf("putting the terminal into raw mode: %w", err)
		}
		restore = func() {
			if err := xterm.Restore(fd, st); err != nil {
				h.log.Printf("stdio host: restoring the terminal: %v", err)
			}
		}
	}
	if h.cfg.Window != nil {
		s.window = &windowCtl{
			wc:       h.cfg.Window,
			log:      h.log,
			deadline: windowCallDeadline,
			labels:   make(chan string, 1),
		}
	}
	h.stdio = s
	h.log.Printf("stdio host: serving its own terminal at %dx%d", h.cols, h.rows)
	return restore, nil
}

// stdioTerminalSize reads a terminal's size; ok is false when f is not one,
// unlike client.go's terminalSize, which falls back to the default size.
func stdioTerminalSize(f *os.File) (rows, cols uint16, ok bool) {
	w, ht, err := xterm.GetSize(f.Fd())
	if err != nil || w <= 0 || ht <= 0 || w > 0xffff || ht > 0xffff {
		return 0, 0, false
	}
	return uint16(ht), uint16(w), true
}

// startStdio starts the terminal writer, the status watcher, the input
// reader and the size watcher. Called once the first child runs, so nothing
// here can outlive a host that failed to start.
func (h *hostRuntime) startStdio() {
	s := h.stdio
	s.started = true
	w := &termWriter{
		out:      s.out,
		log:      h.log,
		name:     h.cfg.Name,
		now:      h.now,
		safeWait: s.safeWait,
		sc:       progstatus.NewScanner(),
		view: func() PhaseView {
			if p := s.view.Load(); p != nil {
				return *p
			}
			return PhaseView{}
		},
	}
	go s.runWriter(w)
	if s.window != nil {
		go s.window.run(h.done)
	}
	go h.watchStdioStatus()
	go h.readTerminalInput()
	go h.watchWinch()
}

// write queues one chunk of child output for the terminal. It blocks while
// the queue is full, like a terminal that has stopped reading blocks its
// program, and gives up only once the host is shutting down. The first try is
// on its own so a chunk that fits is never lost to select's random choice
// against a closed done.
func (s *stdioHost) write(b []byte, done <-chan struct{}) {
	c := termChunk{b: b}
	select {
	case s.ch <- c:
		return
	default:
	}
	select {
	case s.ch <- c:
	case <-done:
	case <-s.stop:
	}
}

// hostLine queues a host message for the terminal. It never blocks, because
// the serve loop calls it: a terminal that has stopped reading must not stop
// the host answering its socket. A dropped line is logged, and host.log is
// the durable record of the event it reported anyway.
func (s *stdioHost) hostLine(b []byte, logger *log.Logger) {
	select {
	case s.ch <- termChunk{b: b, host: true}:
	default:
		logger.Printf("stdio host: the terminal is not reading; could not show %q", b)
	}
}

// holds reports whether c is the terminal's sentinel or a requester.
func (s *stdioHost) holds(c *clientConn) bool {
	if c == s.conn {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.requesters[c]
	return ok
}

func (s *stdioHost) addRequester(c *clientConn) {
	s.mu.Lock()
	s.requesters[c] = struct{}{}
	s.mu.Unlock()
}

func (s *stdioHost) removeRequester(c *clientConn) {
	s.mu.Lock()
	delete(s.requesters, c)
	s.mu.Unlock()
}

// endRequesters ends every requester with an Exit frame, as shutdown ends an
// attached client, and returns them so shutdown can wait for the flush.
func (s *stdioHost) endRequesters() []*clientConn {
	s.mu.Lock()
	reqs := make([]*clientConn, 0, len(s.requesters))
	for c := range s.requesters {
		reqs = append(reqs, c)
	}
	s.requesters = map[*clientConn]struct{}{}
	s.mu.Unlock()
	for _, c := range reqs {
		c.end()
	}
	return reqs
}

// stopWriter ends the terminal writer: it drains what the pump already
// queued, writes the final clear of afk's record if the stream is at a safe
// offset, and returns. The wait is bounded by grace, because a terminal that
// stopped reading would otherwise hold shutdown for ever. Idempotent.
func (s *stdioHost) stopWriter(grace time.Duration) {
	if !s.started {
		return
	}
	s.stopOnce.Do(func() { close(s.stop) })
	select {
	case <-s.ended:
	case <-time.After(grace):
	}
}

// runWriter is the only writer of the terminal.
func (s *stdioHost) runWriter(w *termWriter) {
	defer close(s.ended)
	tick := time.NewTicker(s.tick)
	defer tick.Stop()
	for {
		select {
		case c := <-s.ch:
			w.take(c)
		case <-tick.C:
			w.tick()
		case <-s.stop:
			// What the pump queued before the stop is the child's last
			// output; it goes out before the final clear.
		drain:
			for {
				select {
				case c := <-s.ch:
					w.take(c)
				default:
					break drain
				}
			}
			w.final()
			return
		}
	}
}

// addRequester serves a socket connection on a stdio host: its frames are
// read as for any client, but it never receives the screen and never steals
// it.
func (h *hostRuntime) addRequester(c *clientConn) {
	h.stdio.addRequester(c)
	h.log.Printf("stdio host: request connection accepted")
	go h.readClient(c)
}

// refuseAttach answers an interactive attach on a stdio host. The protocol
// has no refusal frame and adding one is not this change's to make, so the
// reason travels as Data, which an attaching terminal prints, and the
// connection is closed without Exit: the session has not ended. The rex
// runtime refuses before dialling (internal/session); this line is the
// backstop for anything that dials anyway.
func (h *hostRuntime) refuseAttach(c *clientConn) {
	h.log.Printf("stdio host: refused a socket attach; this session's screen is its Rex terminal")
	if !c.send(Data, []byte(RefuseAttachLine+"\r\n")) {
		h.log.Printf("stdio host: could not tell the refused client why")
	}
	h.dropClient(c)
}

// readTerminalInput forwards what is typed at the terminal to the child
// through the same bounded queue socket input uses, under the sentinel
// connection. End of input is logged and the host keeps running: a closed
// input alone is not a closed terminal, and the socket still takes typed
// requests. SIGHUP is what stops a stdio host (docs/PLAN.md, T17.1
// amendment (6)).
func (h *hostRuntime) readTerminalInput() {
	s := h.stdio
	buf := make([]byte, attachInputBufSize)
	for {
		n, err := s.in.Read(buf)
		if n > 0 {
			h.queueInput(s.conn, append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			select {
			case <-h.done:
				return
			default:
			}
			if errors.Is(err, io.EOF) {
				h.log.Printf("stdio host: the terminal's input closed; the host keeps running and still takes requests on its socket")
			} else {
				h.log.Printf("stdio host: reading the terminal's input: %v; the host keeps running and still takes requests on its socket", err)
			}
			return
		}
	}
}

// watchWinch hands every new terminal size to the serve loop. A size the loop
// has not taken yet is replaced, not queued.
func (h *hostRuntime) watchWinch() {
	s := h.stdio
	winch := s.winch
	if s.sigwinch {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGWINCH)
		defer signal.Stop(sig)
		fwd := make(chan struct{}, 1)
		go func() {
			for {
				select {
				case <-h.done:
					return
				case <-sig:
					select {
					case fwd <- struct{}{}:
					default:
					}
				}
			}
		}()
		winch = fwd
	}
	for {
		select {
		case <-h.done:
			return
		case _, ok := <-winch:
			if !ok {
				return
			}
		}
		rows, cols, ok := s.size()
		if !ok {
			h.log.Printf("stdio host: the terminal changed size but its size could not be read")
			continue
		}
		offerLatest(s.resized, [2]uint16{rows, cols})
	}
}

// offerLatest puts v in the one-slot channel ch, replacing a value the
// reader has not taken yet. This goroutine is ch's only sender, so after the
// stale value is taken the second send cannot find the slot full.
func offerLatest(ch chan [2]uint16, v [2]uint16) {
	select {
	case ch <- v:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- v:
	default:
	}
}

// watchStdioStatus reads status.json and the park flag every statusEvery and
// stores the view for the writer. On its own goroutine because Parked is
// injected (T17.8 wires the ledger) and a file read must never sit on the
// writer's path to the terminal.
//
// It is also where the window's label is decided, because a phase change is
// only ever seen here; the label itself is sent by windowCtl.run, so a Rex
// call never delays the next reading either.
func (h *hostRuntime) watchStdioStatus() {
	s := h.stdio
	t := time.NewTicker(s.statusEvery)
	defer t.Stop()
	var label string
	var labelled bool
	for {
		v := readPhaseView(h.statusPath(), h.cfg.Parked)
		s.view.Store(&v)
		if v.Readable {
			r := v
			s.lastReadable.Store(&r)
		}
		if s.window != nil {
			if next, send := nextWindowLabel(h.cfg.Name, h.cfg.Planning, v, label, labelled); send {
				label, labelled = next, true
				s.window.offer(next)
			}
		}
		select {
		case <-h.done:
			return
		case <-t.C:
		}
	}
}

// readPhaseView is the host's reading of status.json through internal/state,
// which implements status-schema.md's reader rules. Anything it calls stale is
// not readable: RecordFor then keeps the last record rather than inventing one.
func readPhaseView(path string, parked func() bool) PhaseView {
	var v PhaseView
	if res := state.ReadStatus(path); !res.Stale {
		v.Readable = true
		v.Phase = string(res.Status.Phase)
		v.AwaitingInput = res.Status.AwaitingInput
	}
	if parked != nil {
		v.Parked = parked()
	}
	return v
}

// termWriter is the terminal writer's state, owned by runWriter's goroutine.
// It is separate from the goroutine so its decisions can be driven by a test
// with a fake clock and a buffer for a terminal.
type termWriter struct {
	out      io.Writer
	log      *log.Logger
	name     string
	now      func() time.Time
	view     func() PhaseView
	safeWait time.Duration

	// sc follows the outer stream: everything written to the terminal,
	// across every child.
	sc *progstatus.Scanner

	last   progstatus.Record
	sent   bool
	sentAt time.Time
	// cleared is set when the agent's own root clear went past since afk's
	// record was last written; that clear removed afk's record with it.
	cleared bool
	// held is host lines waiting for a safe offset.
	held []byte

	// dueSince is when a due record or held line first had to wait for an
	// unended sequence; waitLogged says the wait has been logged once.
	dueSince   time.Time
	waitLogged bool
	errLogged  bool
}

// take handles one queued chunk.
func (w *termWriter) take(c termChunk) {
	if c.host {
		w.held = append(w.held, c.b...)
		w.tick()
		return
	}
	w.chunk(c.b)
}

// due is the record to write and whether it is due now. An unreadable view
// keeps the last record: it is re-sent after a clear or on the heartbeat, but
// nothing new is invented.
func (w *termWriter) due(now time.Time) (progstatus.Record, bool) {
	next, ok := RecordFor(w.name, w.view())
	if !ok {
		if !w.sent {
			return progstatus.Record{}, false
		}
		next = w.last
	}
	return next, ShouldSend(w.last, next, w.sent, now.Sub(w.sentAt), w.cleared)
}

// chunk writes one chunk of child output, with afk's record (and any held
// host lines) inserted at the chunk's last safe offset when one is due. A
// root clear in the chunk ends before that offset (a report's End is always
// safe), so a record re-sent for it lands after the clear and survives it.
func (w *termWriter) chunk(b []byte) {
	res := w.sc.Scan(b)
	for _, r := range res.Reports {
		if r.State == progstatus.StateClear && r.ID == "" {
			w.cleared = true
		}
	}
	now := w.now()
	rec, due := w.due(now)
	if !due && len(w.held) == 0 {
		w.write(b)
		return
	}
	if res.LastSafe < 0 {
		// Inside an agent's sequence from start to end: the record waits.
		w.write(b)
		w.noteWait(now)
		return
	}
	buf := make([]byte, 0, len(b)+len(w.held)+256)
	buf = append(buf, b[:res.LastSafe]...)
	buf = w.insert(buf, rec, due, now)
	buf = append(buf, b[res.LastSafe:]...)
	w.write(buf)
}

// tick writes a due record (or held host lines) on its own, when the stream
// is at a safe offset; otherwise the wait goes on.
func (w *termWriter) tick() {
	now := w.now()
	rec, due := w.due(now)
	if !due && len(w.held) == 0 {
		return
	}
	if !w.sc.Safe() {
		w.noteWait(now)
		return
	}
	w.write(w.insert(nil, rec, due, now))
}

// insert appends the held host lines and, when due, afk's record to buf.
// Both start and end in ground state, so the scanner's state is unchanged by
// them; host lines are scanned anyway, since an error's text is not
// guaranteed to be free of escape bytes.
func (w *termWriter) insert(buf []byte, rec progstatus.Record, due bool, now time.Time) []byte {
	if len(w.held) > 0 {
		w.sc.Scan(w.held)
		buf = append(buf, w.held...)
		w.held = nil
	}
	if due {
		enc, err := progstatus.Encode(rec)
		if err != nil {
			w.log.Printf("stdio host: encoding afk's record: %v", err)
		} else {
			buf = append(buf, enc...)
		}
		// Recorded as sent even when it could not be encoded, so a record
		// that cannot be written is logged once per change rather than on
		// every chunk.
		w.last, w.sent, w.sentAt, w.cleared = rec, true, now, false
	}
	w.dueSince, w.waitLogged = time.Time{}, false
	return buf
}

// noteWait records that something due is waiting for an unended sequence and
// logs it once when the wait reaches safeWait. The host never closes the
// agent's sequence to make room (docs/PLAN.md, T17.1 assumption A7): writing
// a terminator into it would split it.
func (w *termWriter) noteWait(now time.Time) {
	if w.dueSince.IsZero() {
		w.dueSince = now
		return
	}
	if !w.waitLogged && now.Sub(w.dueSince) >= w.safeWait {
		w.waitLogged = true
		w.log.Printf("stdio host: afk's status record has waited %s for the agent to finish an escape sequence; it keeps waiting rather than cut the agent's sequence", w.safeWait)
	}
}

// final writes the clear of afk's record at shutdown, only at a safe offset:
// a clear written into an unended sequence would tear it like any record.
func (w *termWriter) final() {
	if !w.sc.Safe() {
		if w.sent {
			w.log.Printf("stdio host: not clearing afk's record at shutdown: the agent left an escape sequence open")
		}
		return
	}
	var buf []byte
	if len(w.held) > 0 {
		buf = append(buf, w.held...)
		w.held = nil
	}
	if w.sent {
		clr, err := progstatus.Encode(progstatus.Record{State: progstatus.StateClear, ID: "afk"})
		if err != nil {
			w.log.Printf("stdio host: encoding the clear of afk's record: %v", err)
		} else {
			buf = append(buf, clr...)
		}
	}
	w.write(buf)
}

// write puts b on the terminal. A failed write is logged once: a terminal
// that has gone away fails every write after it, and SIGHUP is what ends the
// host in that case.
func (w *termWriter) write(b []byte) {
	if len(b) == 0 {
		return
	}
	if _, err := w.out.Write(b); err != nil && !w.errLogged {
		w.errLogged = true
		w.log.Printf("stdio host: writing to the terminal: %v", err)
	}
}

// windowCtl sends the Rex window's labels and makes the close at exit. Every
// call runs on a goroutine of its own and is waited for at most deadline, so
// neither the PTY pump, the terminal writer nor the status watcher ever waits
// on Rex. Best effort: a failure is logged once for the host's whole life
// (Rex that is not reachable fails every call, and one line says so) and the
// session carries on with the window as it was.
type windowCtl struct {
	wc       WindowControl
	log      *log.Logger
	deadline time.Duration
	// labels holds the label to send next, one slot, replaced rather than
	// queued: only the newest label is worth sending. The watcher is its
	// only sender.
	labels chan string

	failOnce sync.Once
}

// offer hands run the newest label, replacing one it has not taken yet. The
// watcher is the only sender, so after the stale label is taken the second
// send cannot find the slot full.
func (w *windowCtl) offer(label string) {
	select {
	case w.labels <- label:
		return
	default:
	}
	select {
	case <-w.labels:
	default:
	}
	select {
	case w.labels <- label:
	default:
	}
}

// run sends each offered label until the host shuts down. One call is in
// flight at a time: when a call outlives the deadline, run logs it and then
// waits for that call to come back before it starts another, so a Rex that
// hangs costs one goroutine, not one per phase change.
func (w *windowCtl) run(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case label := <-w.labels:
			res := w.start(func() error { return w.wc.SetLabel(label) })
			if !w.await("relabel this session's window", res) {
				select {
				case <-res:
				case <-done:
					return
				}
			}
		}
	}
}

// start runs fn on its own goroutine and returns where its error will land.
// The channel has room for the answer, so the goroutine never blocks on a
// caller that stopped waiting.
func (w *windowCtl) start(fn func() error) chan error {
	res := make(chan error, 1)
	go func() { res <- fn() }()
	return res
}

// await waits for res at most deadline. It reports whether the call came
// back in time, and logs a failure (an error or no answer) once.
func (w *windowCtl) await(what string, res <-chan error) bool {
	t := time.NewTimer(w.deadline)
	defer t.Stop()
	select {
	case err := <-res:
		if err != nil {
			w.fail(what, err)
		}
		return true
	case <-t.C:
		w.fail(what, fmt.Errorf("Rex did not answer within %s", w.deadline))
		return false
	}
}

func (w *windowCtl) fail(what string, err error) {
	w.failOnce.Do(func() {
		w.log.Printf("stdio host: Rex is not reachable, so afk could not %s: %v. The session itself is not affected; further Rex failures are not logged.", what, err)
	})
}

// closeWindowAtExit closes the host's own block unless the session failed,
// so a failed session's window stays open with its last screen for the owner
// to read. Serve runs it as its very last act, after host.json and the socket
// are gone. The phase is read fresh, because the agent's last write may have
// landed after the watcher's last tick, and falls back to the watcher's last
// readable reading when the file cannot be read now.
func (h *hostRuntime) closeWindowAtExit() {
	s := h.stdio
	now := readPhaseView(h.statusPath(), nil)
	if !closeWindowOnExit(now, s.lastReadable.Load()) {
		h.log.Printf("stdio host: the session failed; leaving its Rex window open so its last screen can be read")
		return
	}
	h.log.Printf("stdio host: closing this session's Rex window")
	s.window.await("close this session's window", s.window.start(s.window.wc.CloseSelf))
}
