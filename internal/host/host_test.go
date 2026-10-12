package host

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	testBudget   = 10 * time.Second
	testPollStep = 10 * time.Millisecond
)

// serveHarness runs one Serve call in-process against a stub child. The
// outcome is memoized behind stopped rather than left on a channel, so both
// a test and t.Cleanup can observe it.
type serveHarness struct {
	cfg     ServeConfig
	stopped chan struct{}
	err     error
}

// startServe launches Serve with a `/bin/sh -c <script>` child and waits for
// the socket to accept connections.
func startServe(t *testing.T, script string) *serveHarness {
	t.Helper()
	return startServeWithConfig(t, serveConfigFor(t, script))
}

func startServeWithConfig(t *testing.T, cfg ServeConfig) *serveHarness {
	t.Helper()

	h := &serveHarness{cfg: cfg, stopped: make(chan struct{})}
	go func() {
		h.err = Serve(cfg)
		close(h.stopped)
	}()

	t.Cleanup(h.stopQuietly)
	h.waitListening(t)
	return h
}

// serveConfigFor builds a config whose socket lives in a short-enough
// directory for sockaddr_un and whose .afk files live in a temp repo root.
func serveConfigFor(t *testing.T, script string) ServeConfig {
	t.Helper()
	requireSh(t)

	repoRoot := t.TempDir()
	name := "sess"
	return ServeConfig{
		Name:     name,
		Worktree: repoRoot,
		Socket:   filepath.Join(shortSocketDir(t), "h.sock"),
		MetaPath: filepath.Join(repoRoot, ".afk", name, "host.json"),
		LogPath:  filepath.Join(repoRoot, ".afk", name, "host.log"),
		Argv:     []string{"/bin/sh", "-c", script},
	}
}

func (h *serveHarness) waitListening(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		if h.returned() {
			t.Fatalf("Serve returned before listening: %v", h.err)
		}
		conn, err := net.Dial("unix", h.cfg.Socket)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(testPollStep)
	}
	t.Fatalf("socket %s never started accepting connections", h.cfg.Socket)
}

// dial attaches a raw client, stealing the session from any current client.
func (h *serveHarness) dial(t *testing.T) net.Conn {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for {
		conn, err := net.Dial("unix", h.cfg.Socket)
		if err == nil {
			t.Cleanup(func() { _ = conn.Close() })
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dialing %s: %v", h.cfg.Socket, err)
		}
		time.Sleep(testPollStep)
	}
}

// returned reports whether Serve has already returned.
func (h *serveHarness) returned() bool {
	select {
	case <-h.stopped:
		return true
	default:
		return false
	}
}

// waitDone waits for Serve to return and reports its error.
func (h *serveHarness) waitDone(t *testing.T) error {
	t.Helper()
	select {
	case <-h.stopped:
		return h.err
	case <-time.After(testBudget):
		t.Fatalf("Serve did not return within %s", testBudget)
		return nil
	}
}

// stopQuietly is the cleanup path: ask the host to exit, ignore the outcome.
func (h *serveHarness) stopQuietly() {
	if h.returned() {
		return
	}
	if conn, err := net.Dial("unix", h.cfg.Socket); err == nil {
		_ = WriteFrame(conn, Exit, nil)
		_ = conn.Close()
	}
	select {
	case <-h.stopped:
	case <-time.After(testBudget):
	}
}

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available to run a stub child")
	}
}

// shortSocketDir returns a directory short enough that a socket inside it
// fits sockaddr_un's ~104-byte limit. t.TempDir() on macOS is far too long.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "afk-host-test")
	if err != nil {
		dir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	if len(dir) > 80 {
		t.Skipf("no short temp dir available for a unix socket (got %d bytes: %s)", len(dir), dir)
	}
	return dir
}

// readUntil accumulates Data payloads until want appears, failing on any
// control frame, EOF, or timeout.
func readUntil(t *testing.T, conn net.Conn, want string) string {
	t.Helper()
	var got strings.Builder
	deadline := time.Now().Add(testBudget)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		typ, payload, err := ReadFrame(conn)
		if err != nil {
			t.Fatalf("reading frames while waiting for %q (got %q): %v", want, got.String(), err)
		}
		if typ != Data {
			t.Fatalf("got %v frame while waiting for %q (got %q)", typ, want, got.String())
		}
		got.Write(payload)
		if strings.Contains(got.String(), want) {
			return got.String()
		}
	}
}

// expectFrame reads frames, discarding Data, until it sees want.
func expectFrame(t *testing.T, conn net.Conn, want MessageType) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		typ, _, err := ReadFrame(conn)
		if err != nil {
			t.Fatalf("reading frames while waiting for a %v frame: %v", want, err)
		}
		if typ == want {
			return
		}
		if typ != Data {
			t.Fatalf("got %v frame while waiting for %v", typ, want)
		}
	}
}

// expectEOF asserts the host closed the connection.
func expectEOF(t *testing.T, conn net.Conn) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		typ, _, err := ReadFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return
			}
			t.Fatalf("waiting for EOF: %v", err)
		}
		if typ != Data {
			t.Fatalf("got %v frame while waiting for the host to close the connection", typ)
		}
	}
}

func sendFrame(t *testing.T, conn net.Conn, typ MessageType, payload []byte) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(testBudget)); err != nil {
		t.Fatalf("SetWriteDeadline: %v", err)
	}
	if err := WriteFrame(conn, typ, payload); err != nil {
		t.Fatalf("writing %v frame: %v", typ, err)
	}
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(testPollStep)
	}
	t.Fatalf("%s never appeared", path)
	return ""
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	raw := waitForFile(t, path)
	pid := 0
	if _, err := fmt.Sscanf(raw, "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("unusable child pid %q in %s (err %v)", raw, path, err)
	}
	if !Alive(pid) {
		t.Fatalf("child %d should be alive at this point", pid)
	}
	return pid
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected %s to be removed, stat err = %v", path, err)
	}
}

func TestServeDataRoundTrip(t *testing.T) {
	h := startServe(t, "exec cat")
	conn := h.dial(t)

	sendFrame(t, conn, Resize, EncodeResize(24, 80))
	sendFrame(t, conn, Data, []byte("ping-1234\n"))

	readUntil(t, conn, "ping-1234")
}

func TestServeStealsCurrentClient(t *testing.T) {
	h := startServe(t, "exec cat")

	first := h.dial(t)
	sendFrame(t, first, Resize, EncodeResize(24, 80))
	sendFrame(t, first, Data, []byte("first-client\n"))
	readUntil(t, first, "first-client")

	second := h.dial(t)
	expectFrame(t, first, Stolen)
	expectEOF(t, first)

	sendFrame(t, second, Resize, EncodeResize(24, 80))
	sendFrame(t, second, Data, []byte("second-client\n"))
	readUntil(t, second, "second-client")
}

// TestClientConnStolenBypassesFullOutQueue is the regression test for the
// reviewer finding that hostRuntime.attach's old Stolen path went through
// send(), which silently drops the frame when the client's out queue is
// full — leaving a backpressured client to misreport "session ended"
// instead of "stolen". net.Pipe is unbuffered, so leaving the client side
// unread wedges writeLoop's in-flight Write and lets Data frames pile up
// behind it in pending; sendStolen must still get through once that Write is
// released.
func TestClientConnStolenBypassesFullOutQueue(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	c := newClientConn(server, log.New(io.Discard, "", 0))
	t.Cleanup(c.close)

	if !c.send(Data, []byte("x")) {
		t.Fatal("first send should have been queued")
	}
	// Give writeLoop time to dequeue that frame and block inside conn.Write
	// (nobody is reading the client side).
	time.Sleep(50 * time.Millisecond)

	chunk := 64 << 10
	filled := 0
	for i := 0; i < clientPendingLimit/chunk+10; i++ {
		if c.send(Data, make([]byte, chunk)) {
			filled++
		}
	}
	if filled == 0 {
		t.Fatalf("expected at least one frame to fill the pending backlog")
	}
	if c.send(Data, []byte("overflow")) {
		t.Fatalf("expected the pending backlog to be full after %d fills", filled)
	}

	c.sendStolen()

	// Release the wedged Write so writeLoop can loop back around and
	// deliver Stolen. Discard whatever Data frame was already in flight.
	if err := client.SetReadDeadline(time.Now().Add(testBudget)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	typ, _, err := ReadFrame(client)
	if err != nil {
		t.Fatalf("reading the in-flight frame: %v", err)
	}
	if typ == Stolen {
		return // writeLoop already prioritized Stolen over the wedged write.
	}
	expectFrame(t, client, Stolen)
}

func TestServeExitTearsDownEverything(t *testing.T) {
	cfg := serveConfigFor(t, "")
	pidFile := filepath.Join(cfg.Worktree, "child.pid")
	cfg.Argv = []string{"/bin/sh", "-c", fmt.Sprintf("echo $$ > %s; exec cat", pidFile)}

	h := startServeWithConfig(t, cfg)
	conn := h.dial(t)

	pid := readPIDFile(t, pidFile)

	sendFrame(t, conn, Exit, nil)

	if err := h.waitDone(t); err != nil {
		t.Errorf("Serve after Exit = %v, want nil", err)
	}
	if Alive(pid) {
		t.Errorf("child %d is still alive after Exit", pid)
	}
	assertGone(t, cfg.Socket)
	assertGone(t, cfg.MetaPath)

	logData, err := os.ReadFile(cfg.LogPath)
	if err != nil {
		t.Fatalf("reading host log: %v", err)
	}
	if len(logData) == 0 {
		t.Error("host log is empty; the host logged nothing about its own lifecycle")
	}
}

func TestServeDetachKeepsHostServing(t *testing.T) {
	h := startServe(t, "exec cat")

	first := h.dial(t)
	sendFrame(t, first, Resize, EncodeResize(24, 80))
	sendFrame(t, first, Data, []byte("before-detach\n"))
	readUntil(t, first, "before-detach")

	sendFrame(t, first, Detach, nil)
	expectEOF(t, first)

	time.Sleep(200 * time.Millisecond)
	if h.returned() {
		t.Fatalf("Serve returned on Detach (%v); the host must keep serving", h.err)
	}

	second := h.dial(t)
	sendFrame(t, second, Resize, EncodeResize(24, 80))
	sendFrame(t, second, Data, []byte("after-reattach\n"))
	readUntil(t, second, "after-reattach")

	// A client that dies without sending Detach (the attach process being
	// killed) must also leave the host serving.
	if err := second.Close(); err != nil {
		t.Fatalf("closing second client: %v", err)
	}

	third := h.dial(t)
	sendFrame(t, third, Resize, EncodeResize(24, 80))
	sendFrame(t, third, Data, []byte("after-abrupt-close\n"))
	readUntil(t, third, "after-abrupt-close")

	if _, err := os.Stat(h.cfg.MetaPath); err != nil {
		t.Errorf("host.json should still exist while the host serves: %v", err)
	}
}

func TestServeSIGTERMBehavesLikeExit(t *testing.T) {
	// Keep a registration of our own for the whole test so the test binary
	// can never be killed by the SIGTERM below, even if Serve's handler is
	// already gone.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGTERM)
	defer signal.Stop(guard)

	cfg := serveConfigFor(t, "")
	pidFile := filepath.Join(cfg.Worktree, "child.pid")
	cfg.Argv = []string{"/bin/sh", "-c", fmt.Sprintf("echo $$ > %s; exec cat", pidFile)}

	h := startServeWithConfig(t, cfg)
	conn := h.dial(t)
	pid := readPIDFile(t, pidFile)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the host: %v", err)
	}

	expectFrame(t, conn, Exit)
	expectEOF(t, conn)
	if err := h.waitDone(t); err != nil {
		t.Errorf("Serve after SIGTERM = %v, want nil", err)
	}
	if Alive(pid) {
		t.Errorf("child %d is still alive after the host was signalled", pid)
	}
	assertGone(t, cfg.Socket)
	assertGone(t, cfg.MetaPath)
}

// acceptHold is the test side of the afterAccept seam: once armed, it parks
// the host's accept loop on the next connection it accepts and keeps it parked
// until the host begins shutting down.
type acceptHold struct {
	armed atomic.Bool
	// parked receives once per connection the accept loop was parked on.
	parked chan struct{}
	// forced counts the connections that were held across the start of
	// shutdown, which is the ordering the test exists to produce.
	forced atomic.Int32
}

// holdAcceptedUntilShutdown installs an acceptHold. Like
// compressRestartLadder it must be called BEFORE the host starts:
// newHostRuntime copies the seam once, and registering the restore first is
// what makes t.Cleanup's LIFO order run it after the host has stopped.
func holdAcceptedUntilShutdown(t *testing.T) *acceptHold {
	t.Helper()
	a := &acceptHold{parked: make(chan struct{}, 1)}
	prev := afterAccept
	t.Cleanup(func() { afterAccept = prev })
	afterAccept = func(done <-chan struct{}) {
		if !a.armed.Load() {
			return
		}
		a.parked <- struct{}{}
		<-done
		a.forced.Add(1)
	}
	return a
}

// TestServeTellsUnattachedClientsTheSessionEnded forces the ordering behind
// TestServeSIGTERMBehavesLikeExit's intermittent failure (about one run in
// fifteen): net.Dial returns as soon as the kernel has queued the connection,
// which is before the host has accepted it, let alone attached it, so a
// shutdown can begin while a client that believes it is connected is still on
// its way in. Such a client used to get a bare close or a reset, which attach
// reports as "connection dropped" for a session that had in fact ended.
//
// Left to the scheduler that window is a few instructions wide, so the test
// holds it open with the afterAccept seam instead of hoping for it: the accept
// loop is parked with the first pending client in hand, every later pending
// client is therefore still in the listen backlog, and only then is the host
// told to stop. A version that merely dialed and signalled would pass on the
// broken host most of the time, which is the flaky test this replaces.
//
// The pending count is what separates the two places a client can be. The
// "one pending" row has only the client the accept loop is holding, and fails
// if that hand-back loses its Exit. The other rows add clients that were never
// accepted at all, and fail if the backlog is not drained; with the hand-back
// fixed and the backlog not, client 0 passes and the rest are reset. Both are
// load-bearing, since they are separate code in shutdown. The three ways of
// ending the session are separate rows because each leaves the serve loop
// through its own branch, and all of them must reach the same shutdown.
func TestServeTellsUnattachedClientsTheSessionEnded(t *testing.T) {
	tests := []struct {
		name    string
		pending int
		// end makes the host shut down, through the attached client or not.
		end func(t *testing.T, attached net.Conn)
	}{
		{
			name:    "client asks for exit, one pending client in the accept loop's hand",
			pending: 1,
			end: func(t *testing.T, attached net.Conn) {
				sendFrame(t, attached, Exit, nil)
			},
		},
		{
			name:    "client asks for exit, more pending clients still in the listen backlog",
			pending: 8,
			end: func(t *testing.T, attached net.Conn) {
				sendFrame(t, attached, Exit, nil)
			},
		},
		{
			name:    "host is signalled, pending clients in hand and in the backlog",
			pending: 8,
			end: func(t *testing.T, attached net.Conn) {
				if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
					t.Fatalf("signalling the host: %v", err)
				}
			},
		},
		{
			name:    "child exits on its own, pending clients in hand and in the backlog",
			pending: 8,
			end: func(t *testing.T, attached net.Conn) {
				// End-of-file at the start of a line: cat reads zero bytes
				// and exits.
				sendFrame(t, attached, Data, []byte{0x04})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// As in TestServeSIGTERMBehavesLikeExit: the test binary must
			// never be killed by its own SIGTERM.
			guard := make(chan os.Signal, 1)
			signal.Notify(guard, syscall.SIGTERM)
			defer signal.Stop(guard)

			hold := holdAcceptedUntilShutdown(t)
			h := startServe(t, "exec cat")

			// Prove the attached client is past the accept loop before
			// arming the hold: its input only comes back once the serve
			// loop has attached it, and the accept loop hands a connection
			// over only after the seam has run for it. Arming any earlier
			// could park this client instead of a pending one.
			attached := h.dial(t)
			sendFrame(t, attached, Resize, EncodeResize(24, 80))
			sendFrame(t, attached, Data, []byte("attached\n"))
			readUntil(t, attached, "attached")
			hold.armed.Store(true)

			pending := make([]net.Conn, 0, tt.pending)
			for i := 0; i < tt.pending; i++ {
				pending = append(pending, h.dial(t))
				if i > 0 {
					continue
				}
				// Wait until the accept loop is parked on the first one, so
				// the rest cannot be accepted before shutdown begins.
				select {
				case <-hold.parked:
				case <-time.After(testBudget):
					t.Fatalf("the accept loop never accepted the first pending client")
				}
			}

			tt.end(t, attached)

			expectFrame(t, attached, Exit)
			expectEOF(t, attached)
			for i, conn := range pending {
				where := "still in the listen backlog"
				if i == 0 {
					where = "held by the accept loop"
				}
				if err := expectSessionEnded(conn); err != nil {
					t.Errorf("pending client %d (%s) was not told the session ended: %v", i, where, err)
				}
			}
			if err := h.waitDone(t); err != nil {
				t.Errorf("Serve = %v, want nil", err)
			}

			// Without this the assertions above could pass because the host
			// happened to attach every client before it stopped.
			if got := hold.forced.Load(); got != 1 {
				t.Errorf("the accept loop was held across the start of shutdown %d times, want 1: the ordering under test was not forced", got)
			}
			assertGone(t, h.cfg.Socket)
			assertGone(t, h.cfg.MetaPath)
		})
	}
}

// expectSessionEnded reads conn to its end and reports an error unless it
// carried exactly one Exit frame followed by EOF, which is what attach needs to
// report that the session ended. An error rather than a t.Fatal so one test can
// report every pending client, since which ones fail says which path is broken.
func expectSessionEnded(conn net.Conn) error {
	if err := conn.SetReadDeadline(time.Now().Add(testBudget)); err != nil {
		return fmt.Errorf("SetReadDeadline: %w", err)
	}
	typ, _, err := ReadFrame(conn)
	if err != nil {
		return fmt.Errorf("want an Exit frame, got %w", err)
	}
	if typ != Exit {
		return fmt.Errorf("want an Exit frame, got a %v frame", typ)
	}
	typ, _, err = ReadFrame(conn)
	if err == nil {
		return fmt.Errorf("want EOF after the Exit frame, got a %v frame", typ)
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("want EOF after the Exit frame, got %w", err)
	}
	return nil
}

func TestServeChildExitEndsHost(t *testing.T) {
	h := startServe(t, "sleep 1; exit 3")
	conn := h.dial(t)

	// Exit before the close is what tells a client the session ended
	// rather than that it was dropped (DetachDropped).
	expectFrame(t, conn, Exit)
	expectEOF(t, conn)

	if err := h.waitDone(t); err != nil {
		t.Errorf("Serve after the child exited = %v, want nil", err)
	}
	assertGone(t, h.cfg.Socket)
	assertGone(t, h.cfg.MetaPath)
}

func TestServeKeepsDrainingPTYWithNoClientAttached(t *testing.T) {
	cfg := serveConfigFor(t, "")
	doneFile := filepath.Join(cfg.Worktree, "flooded")
	// ~2000 lines is far more than any PTY buffer holds, so this child
	// only reaches the touch if the host drains the PTY with no client
	// attached.
	script := fmt.Sprintf(`i=0
while [ $i -lt 2000 ]; do
  printf 'padding line %%s of unattached output\n' $i
  i=$((i+1))
done
echo done > %s
exec cat`, doneFile)
	cfg.Argv = []string{"/bin/sh", "-c", script}

	h := startServeWithConfig(t, cfg)
	waitForFile(t, doneFile)

	conn := h.dial(t)
	sendFrame(t, conn, Resize, EncodeResize(24, 80))
	sendFrame(t, conn, Data, []byte("still-alive\n"))
	readUntil(t, conn, "still-alive")
}

// The refused host must leave the first host's socket and host.json alone.
// Serve returns from listenExclusive's ErrHostAlreadyRunning before it
// registers the deferred release that calls removeOurMeta, so nothing in the
// refusal path can remove either file.
//
// The test has to wait for host.json before starting the second host:
// startServe reports ready as soon as the socket accepts a connection, but
// Serve writes host.json only after it has spawned the child. Without the
// wait, the os.Stat below could run before the first host wrote the file and
// report "removed" a file that never existed yet.
func TestServeRefusesWhenALiveHostIsListening(t *testing.T) {
	first := startServe(t, "exec cat")
	awaitMeta(t, first.cfg, "was written by the first host", func(Meta) bool { return true })

	second := serveConfigFor(t, "")
	marker := filepath.Join(second.Worktree, "second-child-ran")
	second.Socket = first.cfg.Socket
	second.Argv = []string{"/bin/sh", "-c", fmt.Sprintf("echo ran > %s; exec cat", marker)}

	err := Serve(second)
	if !errors.Is(err, ErrHostAlreadyRunning) {
		t.Fatalf("Serve against a live socket = %v, want ErrHostAlreadyRunning", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the refused host started a second child (marker %s exists)", marker)
	}
	if _, statErr := os.Stat(second.MetaPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the refused host wrote %s", second.MetaPath)
	}

	if _, statErr := os.Stat(first.cfg.Socket); statErr != nil {
		t.Errorf("the refused host removed the live socket: %v", statErr)
	}
	if _, statErr := os.Stat(first.cfg.MetaPath); statErr != nil {
		t.Errorf("the refused host removed the live host.json: %v", statErr)
	}

	conn := first.dial(t)
	sendFrame(t, conn, Resize, EncodeResize(24, 80))
	sendFrame(t, conn, Data, []byte("still-serving\n"))
	readUntil(t, conn, "still-serving")
}

func TestServeHijacksStaleSocketEvenWhenMetaPIDIsAlive(t *testing.T) {
	cfg := serveConfigFor(t, "exec cat")

	// A socket file left behind by a host that died: bound, then closed
	// without unlinking, so a dial gets connection-refused.
	addr, err := net.ResolveUnixAddr("unix", cfg.Socket)
	if err != nil {
		t.Fatalf("ResolveUnixAddr: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("closing stale listener: %v", err)
	}
	if _, err := os.Stat(cfg.Socket); err != nil {
		t.Fatalf("stale socket file should still exist: %v", err)
	}

	// host.json naming a definitely-live PID: Alive() says true, but the
	// dial is authoritative, so startup must proceed anyway.
	if err := writeMetaFile(cfg.MetaPath, Meta{PID: os.Getpid(), Socket: cfg.Socket}); err != nil {
		t.Fatalf("seeding host.json: %v", err)
	}
	if !Alive(os.Getpid()) {
		t.Fatal("precondition: this process should be Alive")
	}

	h := startServeWithConfig(t, cfg)
	conn := h.dial(t)
	sendFrame(t, conn, Resize, EncodeResize(24, 80))
	sendFrame(t, conn, Data, []byte("hijacked\n"))
	readUntil(t, conn, "hijacked")

	meta, err := ReadMeta(cfg.Worktree, cfg.Name)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if meta.Socket != cfg.Socket || len(meta.Argv) == 0 || meta.StartedAt == "" {
		t.Errorf("host.json was not republished by the new host: %+v", meta)
	}
}

// TestListenExclusiveConcurrentStaleRecovery races several restarts at one
// genuinely stale socket file. The flock in recoverStaleSocket must serialize
// the probe→remove→rebind window so that exactly one contender binds, every
// other gets ErrHostAlreadyRunning, and — the actual TOCTOU hazard — no
// loser's os.Remove unlinks the socket the winner just bound (which would
// leave the winner serving a path nobody can dial while a second host binds a
// fresh file: two claudes on one session).
func TestListenExclusiveConcurrentStaleRecovery(t *testing.T) {
	requireUnixBind(t)
	socket := filepath.Join(shortSocketDir(t), "stale-race.sock")

	addr, err := net.ResolveUnixAddr("unix", socket)
	if err != nil {
		t.Fatalf("ResolveUnixAddr: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("binding the to-be-stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("closing the stale listener: %v", err)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("the stale socket file should survive Close: %v", err)
	}

	const contenders = 8
	logger := log.New(io.Discard, "", 0)
	start := make(chan struct{})
	results := make(chan error, contenders)
	listeners := make(chan net.Listener, contenders)
	for i := 0; i < contenders; i++ {
		go func() {
			<-start
			ln, err := listenExclusive(socket, logger)
			if err == nil {
				listeners <- ln
			}
			results <- err
		}()
	}
	close(start)

	var wins, refused int
	for i := 0; i < contenders; i++ {
		switch err := <-results; {
		case err == nil:
			wins++
		case errors.Is(err, ErrHostAlreadyRunning):
			refused++
		default:
			t.Errorf("contender error = %v, want ErrHostAlreadyRunning", err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1", wins)
	}
	if refused != contenders-1 {
		t.Fatalf("refused = %d, want %d", refused, contenders-1)
	}

	winner := <-listeners
	defer winner.Close()
	probe, err := net.DialTimeout("unix", socket, dialProbeTimeout)
	if err != nil {
		t.Fatalf("the winner's socket must remain dialable (a loser unlinked it): %v", err)
	}
	_ = probe.Close()
}

func TestServeResizeDeliversSIGWINCH(t *testing.T) {
	script := `trap 'printf "GOTWINCH\n"' WINCH
while true; do sleep 0.05; done`
	h := startServe(t, script)
	conn := h.dial(t)

	// Same size as the PTY already has: only the column nudge makes the
	// child repaint.
	sendFrame(t, conn, Resize, EncodeResize(defaultRows, defaultCols))
	readUntil(t, conn, "GOTWINCH")

	sendFrame(t, conn, Resize, EncodeResize(40, 120))
	readUntil(t, conn, "GOTWINCH")
}

func TestServeValidatesConfig(t *testing.T) {
	full := ServeConfig{
		Name:     "sess",
		Worktree: "/tmp",
		Socket:   "/tmp/x.sock",
		MetaPath: "/tmp/host.json",
		LogPath:  "/tmp/host.log",
		Argv:     []string{"/bin/sh", "-c", "true"},
	}
	tests := []struct {
		name    string
		mutate  func(*ServeConfig)
		wantErr string
	}{
		{name: "missing name", mutate: func(c *ServeConfig) { c.Name = "" }, wantErr: "session name"},
		{name: "missing worktree", mutate: func(c *ServeConfig) { c.Worktree = "" }, wantErr: "worktree"},
		{name: "missing socket", mutate: func(c *ServeConfig) { c.Socket = "" }, wantErr: "socket"},
		{name: "missing meta path", mutate: func(c *ServeConfig) { c.MetaPath = "" }, wantErr: "host.json"},
		{name: "missing log path", mutate: func(c *ServeConfig) { c.LogPath = "" }, wantErr: "log path"},
		{name: "missing argv", mutate: func(c *ServeConfig) { c.Argv = nil }, wantErr: "argv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := full
			tt.mutate(&cfg)
			err := Serve(cfg)
			if err == nil {
				t.Fatal("Serve: expected a validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Serve error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}

	if err := full.validate(); err != nil {
		t.Errorf("validate on a complete config = %v, want nil", err)
	}
}

func TestChildEnv(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{name: "no TERM gets the fallback", env: []string{"PATH=/bin"}, want: "TERM=" + fallbackTERM},
		{name: "existing TERM is respected", env: []string{"TERM=screen-256color"}, want: "TERM=screen-256color"},
		{name: "empty TERM is respected", env: []string{"TERM="}, want: "TERM="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := append([]string(nil), tt.env...)
			got := childEnv(in)

			var terms []string
			for _, kv := range got {
				if strings.HasPrefix(kv, "TERM=") {
					terms = append(terms, kv)
				}
			}
			if len(terms) != 1 {
				t.Fatalf("TERM entries = %v, want exactly one", terms)
			}
			if terms[0] != tt.want {
				t.Errorf("TERM = %q, want %q", terms[0], tt.want)
			}
			for i, kv := range tt.env {
				if in[i] != kv {
					t.Errorf("childEnv mutated its input: in[%d] = %q, want %q", i, in[i], kv)
				}
			}
		})
	}
}

// TestChildEnvStripsInheritedSessionIDs pins that no agent session id from
// the host's own environment reaches the child. The `claude id` row is the
// load-bearing one: a codex child keeps a stray CLAUDE_CODE_SESSION_ID, and
// the conductor reads it as its own session id. The `lookalike` row keeps the
// match exact, so a variable that merely shares a prefix survives.
func TestChildEnvStripsInheritedSessionIDs(t *testing.T) {
	tests := []struct {
		name     string
		env      []string
		stripped string
	}{
		{name: "claude id", env: []string{"PATH=/bin", "CLAUDE_CODE_SESSION_ID=a9313053"}, stripped: "CLAUDE_CODE_SESSION_ID="},
		{name: "codex session id", env: []string{"CODEX_SESSION_ID=01a11db5"}, stripped: "CODEX_SESSION_ID="},
		{name: "codex thread id", env: []string{"CODEX_THREAD_ID=01a11db5"}, stripped: "CODEX_THREAD_ID="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, kv := range childEnv(tt.env) {
				if strings.HasPrefix(kv, tt.stripped) {
					t.Errorf("childEnv kept %q", kv)
				}
			}
		})
	}
	t.Run("lookalike", func(t *testing.T) {
		got := childEnv([]string{"CLAUDE_CODE_SESSION_ID_EXTRA=x", "PATH=/bin"})
		if len(got) != 3 || got[0] != "CLAUDE_CODE_SESSION_ID_EXTRA=x" || got[1] != "PATH=/bin" {
			t.Errorf("childEnv = %q, want the lookalike and PATH kept in order plus TERM", got)
		}
	})
}

// stuckChildServeConfig builds a ServeConfig whose child is the fixture from
// stuckchild_test.go — the only child in this package that can be told,
// mid-run, to stop reading its stdin, which is what a hung `claude` looks
// like from the host's side of the PTY. Its role and control-directory
// switches travel in the environment because startChild hands the child
// childEnv(os.Environ()), so t.Setenv is how a test reaches them; the argv is
// this test binary re-exec'd, exactly as startStuckChild does it.
func stuckChildServeConfig(t *testing.T) (ServeConfig, string) {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	repoRoot := t.TempDir()
	ctrlDir := filepath.Join(repoRoot, "ctrl")
	if err := os.Mkdir(ctrlDir, 0o755); err != nil {
		t.Fatalf("creating the fixture's control dir: %v", err)
	}
	t.Setenv(stuckChildRoleEnv, stuckChildRoleValue)
	t.Setenv(stuckChildCtrlDirEnv, ctrlDir)

	name := "stuck"
	return ServeConfig{
		Name:     name,
		Worktree: repoRoot,
		Socket:   filepath.Join(shortSocketDir(t), "h.sock"),
		MetaPath: filepath.Join(repoRoot, ".afk", name, "host.json"),
		LogPath:  filepath.Join(repoRoot, ".afk", name, "host.log"),
		// -test.run=^$ is stuckchild_test.go's own safety net: if its
		// init-based role dispatch ever broke, this re-exec runs no tests
		// rather than the whole suite recursively.
		Argv: []string{self, "-test.run=^$"},
	}, ctrlDir
}

// setStuckChildFlag flips one of the fixture's switches. The fixture polls
// its control directory for these marker files, so creating or removing one
// is how the switch is flipped.
//
// For the write switch it then waits for the fixture's acknowledgement, so the
// call returns only once the fixture has acted on the new value: after "stop
// writing" no further heartbeat can be written. The fixture notices a change
// only once per stuckChildPollInterval, and a caller that assumes it did
// sooner sees one stray heartbeat. The read switch is not acknowledged
// because a fixture blocked in Read cannot look at it (see
// stuckChildWriteLoop).
func setStuckChildFlag(t *testing.T, ctrlDir, name string, on bool) {
	t.Helper()
	path := filepath.Join(ctrlDir, name)
	if on {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("setting fixture flag %s: %v", name, err)
		}
	} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clearing fixture flag %s: %v", name, err)
	}
	if name == stuckChildNoWriteFlag {
		awaitStuckChildWriteAck(t, ctrlDir, on)
	}
}

// awaitLogLine polls the host's own log until it contains want. host.log is
// the host's operator-facing record and the only place a dropped-input event
// is visible, so asserting on it is asserting on the contract, not on an
// implementation detail.
func awaitLogLine(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), want) {
			return
		}
		time.Sleep(testPollStep)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("host log %s never contained %q; log so far:\n%s", path, want, data)
}

// floodUntilInputDropped sends chunk as client input until host.log reports
// dropped input, and fails if that has not happened within testBudget. It
// exists because the number of chunks needed depends on how much the PTY will
// absorb before a write blocks, which this test cannot know; the log line is
// the only signal that the queue overflowed. There is no sleep: each send is
// the pacing, and the host reads its socket without ever blocking on the
// child, so the sends cannot stall.
func floodUntilInputDropped(t *testing.T, conn net.Conn, chunk []byte, logPath string) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	sent := 0
	for time.Now().Before(deadline) {
		sendFrame(t, conn, Data, chunk)
		sent++
		if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "bytes of input") {
			return
		}
	}
	data, _ := os.ReadFile(logPath)
	t.Fatalf("host log %s never contained %q after %d chunks of %d bytes; log so far:\n%s", logPath, "bytes of input", sent, len(chunk), data)
}

// TestServeStaysLiveWhenTheChildStopsReadingItsInput is the regression test
// for the inline h.ptmx.Write that used to sit in the serve loop's select. A
// child that stops reading its stdin fills the PTY's input buffer, and that
// write then blocks forever — taking down the one goroutine that also
// services resize, detach, exit, signals and newly accepted clients.
//
// The load-bearing assertions are the ones AFTER the flood: a Resize the
// host acts on, a Detach it answers, and a fresh client it still accepts.
// Those are exactly the frames the preserved incident log showed the host
// still serving while its child was gone, so a host that cannot serve them
// while a child ignores input is strictly worse than the observed reality.
// The dropped-input assertion is equally load-bearing in the other
// direction: staying live by silently discarding keystrokes with no record
// would be an invisible failure mode.
func TestServeStaysLiveWhenTheChildStopsReadingItsInput(t *testing.T) {
	cfg, ctrlDir := stuckChildServeConfig(t)
	h := startServeWithConfig(t, cfg)

	conn := h.dial(t)
	sendFrame(t, conn, Resize, EncodeResize(defaultRows, defaultCols))
	// The heartbeat, not the one-shot banner: waitListening's probe dial
	// attaches and detaches before this client exists, so the banner may
	// already have been discarded by the time anyone is watching.
	readUntil(t, conn, stuckChildHeartbeatMarker)

	// Silence the fixture's heartbeat for the duration of the flood. This
	// client deliberately stops reading while it writes, and a host that is
	// still producing output would fill the socket, trip clientWriteTimeout
	// and drop the client — a real and correct behaviour, but a different
	// one from the wedge under test.
	setStuckChildFlag(t, ctrlDir, stuckChildNoWriteFlag, true)
	// From here the fixture issues no read on its stdin at all, so the
	// PTY's input queue fills and stays full.
	setStuckChildFlag(t, ctrlDir, stuckChildNoReadFlag, true)

	// Newline-terminated on purpose: a PTY's canonical line discipline only
	// queues a completed line for the reader, and only a full queue blocks
	// the writer. An unterminated byte stream is discarded by the driver
	// instead of blocking, which would let this test pass without ever
	// wedging anything.
	//
	// The flood has no fixed size, because no size is safe: the host logs
	// "bytes of input" only when queueInput finds the 64-chunk queue full,
	// which needs the PTY to be full first (the writer blocks in ptmx.Write
	// and the queue then backs up behind it). A fixed 4*childInputQueueDepth
	// chunks (about 22KB) did not always get there: in 15 of 40 saved runs
	// (.afk/t0-5/evidence/w1-t3-before.txt) the log held no drop line at all,
	// so the PTY plus the queue absorbed the whole flood. How much the PTY
	// accepts is the kernel's and the fixture's to decide, so send until the
	// host reports the drop, checking the log between sends, bounded by
	// testBudget.
	chunk := []byte(strings.Repeat("0123456789\n", 8))
	floodUntilInputDropped(t, conn, chunk, cfg.LogPath)

	// A real size change, so "resized" in the log is unambiguous.
	sendFrame(t, conn, Resize, EncodeResize(40, 120))
	awaitLogLine(t, cfg.LogPath, "resized pty to 40x120")

	sendFrame(t, conn, Detach, nil)
	expectEOF(t, conn)

	// The child is still wedged on input, but its output must still reach a
	// freshly attached client.
	setStuckChildFlag(t, ctrlDir, stuckChildNoWriteFlag, false)
	second := h.dial(t)
	sendFrame(t, second, Resize, EncodeResize(40, 120))
	readUntil(t, second, stuckChildHeartbeatMarker)

	if h.returned() {
		t.Fatalf("Serve returned while the child was merely not reading its input: %v", h.err)
	}
}

// TestClassifyInput pins the child writer's per-chunk decision. The
// load-bearing rows are the two "stolen" ones (a chunk typed into a client
// that has since lost the session must never reach the child, however fresh
// it is) and the boundary row (exactly maxAge is still deliverable, so the
// deadline is not off by one poll).
func TestClassifyInput(t *testing.T) {
	const maxAge = 5 * time.Second
	tests := []struct {
		name    string
		current bool
		age     time.Duration
		want    inputVerdict
	}{
		{name: "current and fresh", current: true, age: time.Millisecond, want: inputWrite},
		{name: "current at the boundary", current: true, age: maxAge, want: inputWrite},
		{name: "current but stale", current: true, age: maxAge + time.Millisecond, want: inputDropStale},
		{name: "stolen and fresh", current: false, age: time.Millisecond, want: inputDropStolen},
		{name: "stolen and stale", current: false, age: 2 * maxAge, want: inputDropStolen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyInput(tt.current, tt.age, maxAge); got != tt.want {
				t.Errorf("classifyInput(%v, %v, %v) = %v, want %v", tt.current, tt.age, maxAge, got, tt.want)
			}
		})
	}
}
