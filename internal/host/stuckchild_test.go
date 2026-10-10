package host

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

// This file is the fixture the rest of the suite has no equivalent of: a
// controllable fake child that can be told, mid-run, to stop reading its
// stdin, stop writing its stdout, or both — which is what a hung `claude`
// looks like from the host's side of the PTY. Everything else in this
// package that plays a child (lifecycleChildScript, the many `/bin/sh -c`
// scripts in host_test.go) is scripted once at spawn time; none of them can
// be flipped from consuming to non-consuming after the fact, which is
// exactly the transition a wedge investigation needs to provoke on demand.
//
// Follows the re-exec pattern already established by lifecycle_test.go
// (TestMain dispatching AFK_TEST_HOST_ROLE=serve into lifecycleServeMain):
// the fixture's body also runs inside this same test binary, re-exec'd. It
// cannot reuse that TestMain (only one is allowed per package, and it
// already belongs to lifecycle_test.go's own dispatch), so it intercepts
// earlier, in init(): init functions all run before go test's generated
// main() ever calls TestMain, so exiting there reaches the fixture's body
// without touching lifecycle_test.go at all.

const (
	// stuckChildRoleEnv, set to stuckChildRoleValue, makes this test binary
	// behave as the fixture's child process instead of running the test
	// suite. Named distinctly from lifecycle_test.go's AFK_TEST_HOST_ROLE
	// so the two re-exec dispatches can never collide.
	stuckChildRoleEnv   = "AFK_TEST_STUCKCHILD_ROLE"
	stuckChildRoleValue = "stuckchild"

	// stuckChildPIDFileEnv names the file the fixture writes its own pid
	// to, mirroring the "echo $$ > pidfile" convention the /bin/sh fixtures
	// in this package use, so tests can pin down a real, distinct process.
	stuckChildPIDFileEnv = "AFK_TEST_STUCKCHILD_PIDFILE"
	// stuckChildCtrlDirEnv names a directory the fixture polls
	// continuously for two marker files (stuckChildNoReadFlag,
	// stuckChildNoWriteFlag). Polling the filesystem, rather than e.g. a
	// signal, is what lets a test flip either switch from its own goroutine
	// with no coordination with the fixture process beyond a path it
	// already knows.
	stuckChildCtrlDirEnv = "AFK_TEST_STUCKCHILD_CTRLDIR"

	stuckChildNoReadFlag  = "no-read"
	stuckChildNoWriteFlag = "no-write"

	// stuckChildBanner is emitted once at startup, unconditionally, so the
	// attach path always has something to render before any test-driven
	// switch takes effect. Tests match on stuckChildBannerMarker rather
	// than this whole string because the PTY's ONLCR translation turns the
	// trailing \n into \r\n on the read side.
	stuckChildBanner       = "STUCKCHILD-READY\n"
	stuckChildBannerMarker = "STUCKCHILD-READY"
	// stuckChildHeartbeat is written on every tick that output is enabled,
	// so a test can watch it stop the instant stuckChildNoWriteFlag is set.
	// Same ONLCR reasoning as the banner applies to its marker.
	stuckChildHeartbeat       = "STUCKCHILD-TICK\n"
	stuckChildHeartbeatMarker = "STUCKCHILD-TICK"

	// stuckChildPollInterval is the cadence both the read and write loops
	// use to re-check their flag, when that loop is not otherwise busy (the
	// write loop sleeps this long between heartbeats; the read loop sleeps
	// this long between flag checks whenever reading is disabled).
	stuckChildPollInterval = 10 * time.Millisecond
)

// init intercepts a re-exec into the fixture role before go test's
// generated main() ever calls TestMain (declared in lifecycle_test.go for
// its own, unrelated role). See the package doc comment above for why this
// has to be init rather than another TestMain.
func init() {
	if os.Getenv(stuckChildRoleEnv) == stuckChildRoleValue {
		os.Exit(stuckChildMain())
	}
}

// stuckChildMain is the whole body of the fixture process. Reading and
// writing run as two independent loops rather than one alternating
// sequence: this fd type does not support read/write deadlines in every
// environment this runs in (observed on darwin), so the only way to keep
// the write side responsive to its own flag while the read side is
// legitimately blocked waiting for input (the ordinary state of an idle
// terminal reader) is to give each side its own goroutine.
func stuckChildMain() int {
	if pidFile := os.Getenv(stuckChildPIDFileEnv); pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "stuckchild: writing pid file:", err)
			return 1
		}
	}
	ctrlDir := os.Getenv(stuckChildCtrlDirEnv)

	if _, err := os.Stdout.WriteString(stuckChildBanner); err != nil {
		fmt.Fprintln(os.Stderr, "stuckchild: writing banner:", err)
		return 1
	}

	go stuckChildWriteLoop(ctrlDir)
	stuckChildReadLoop(ctrlDir)
	return 0
}

// stuckChildWriteLoop emits a heartbeat on every tick that
// stuckChildNoWriteFlag is not set. It never touches stdin, so it keeps
// running at its own cadence regardless of what the read loop is doing.
func stuckChildWriteLoop(ctrlDir string) {
	for {
		if !stuckChildFlagSet(ctrlDir, stuckChildNoWriteFlag) {
			if _, err := os.Stdout.WriteString(stuckChildHeartbeat); err != nil {
				return
			}
		}
		time.Sleep(stuckChildPollInterval)
	}
}

// stuckChildReadLoop is the switch this fixture exists for: while
// stuckChildNoReadFlag is set, it issues no Read syscall on stdin at all —
// not even a non-blocking one — so the PTY's kernel input buffer is left to
// fill with whatever the other end writes, exactly as an unresponsive real
// child would leave it. It returns (ending the process) once stdin reports
// EOF, mirroring how a real child's terminal read loop ends when its
// controlling PTY goes away.
func stuckChildReadLoop(ctrlDir string) {
	buf := make([]byte, 4096)
	for {
		if stuckChildFlagSet(ctrlDir, stuckChildNoReadFlag) {
			time.Sleep(stuckChildPollInterval)
			continue
		}
		if _, err := os.Stdin.Read(buf); err != nil {
			// EOF once the PTY master is closed; anything else is treated
			// the same way, since there is nothing more useful to do with
			// this fd once it has errored.
			return
		}
	}
}

func stuckChildFlagSet(ctrlDir, name string) bool {
	if ctrlDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(ctrlDir, name))
	return err == nil
}

// stuckChild is one fixture process running under its own real PTY, started
// the same way startChild in host.go starts any child (pty.StartWithSize on
// this same binary+argv) — so a real host.Serve could drive this exact
// process; tests here just talk to it directly for tighter control.
type stuckChild struct {
	t    *testing.T
	cmd  *exec.Cmd
	ptmx *os.File

	ctrlDir string
	pidFile string

	out       syncBuffer // accumulated bytes the fixture has written, drained continuously
	drainDone chan struct{}
}

// startStuckChild launches the fixture and waits for its startup banner.
func startStuckChild(t *testing.T) *stuckChild {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	dir := t.TempDir()
	ctrlDir := filepath.Join(dir, "ctrl")
	if err := os.Mkdir(ctrlDir, 0o755); err != nil {
		t.Fatalf("creating control dir: %v", err)
	}
	pidFile := filepath.Join(dir, "pid")

	// -test.run=^$ is the same safety net lifecycle_test.go uses: if the
	// init-based role dispatch above ever broke, this re-exec would run no
	// tests rather than the whole suite recursively.
	cmd := exec.Command(self, "-test.run=^$")
	cmd.Env = append(os.Environ(),
		stuckChildRoleEnv+"="+stuckChildRoleValue,
		stuckChildPIDFileEnv+"="+pidFile,
		stuckChildCtrlDirEnv+"="+ctrlDir,
	)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: defaultRows, Cols: defaultCols})
	if err != nil {
		t.Fatalf("starting the stuckchild fixture under a pty: %v", err)
	}

	sc := &stuckChild{
		t:         t,
		cmd:       cmd,
		ptmx:      ptmx,
		ctrlDir:   ctrlDir,
		pidFile:   pidFile,
		drainDone: make(chan struct{}),
	}
	go sc.drain()
	t.Cleanup(sc.stop)

	sc.awaitOutput(t, stuckChildBannerMarker)
	return sc
}

// drain continuously reads the PTY master into sc.out, exactly like
// hostRuntime.pumpPTY does for a real session: without a reader running at
// all times, the fixture's own writes would back up and block it
// regardless of what a given test is trying to exercise.
func (sc *stuckChild) drain() {
	defer close(sc.drainDone)
	buf := make([]byte, 4096)
	for {
		n, err := sc.ptmx.Read(buf)
		if n > 0 {
			_, _ = sc.out.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// stopReading / resumeReading / stopWriting / resumeWriting flip the
// fixture's switches by creating or removing a marker file it polls.
func (sc *stuckChild) stopReading()   { sc.setFlag(stuckChildNoReadFlag, true) }
func (sc *stuckChild) resumeReading() { sc.setFlag(stuckChildNoReadFlag, false) }
func (sc *stuckChild) stopWriting()   { sc.setFlag(stuckChildNoWriteFlag, true) }
func (sc *stuckChild) resumeWriting() { sc.setFlag(stuckChildNoWriteFlag, false) }

func (sc *stuckChild) setFlag(name string, on bool) {
	sc.t.Helper()
	path := filepath.Join(sc.ctrlDir, name)
	if on {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			sc.t.Fatalf("setting flag %s: %v", name, err)
		}
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		sc.t.Fatalf("clearing flag %s: %v", name, err)
	}
}

// awaitOutput polls the accumulated PTY output until want appears.
func (sc *stuckChild) awaitOutput(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		if got := sc.out.String(); bytes.Contains([]byte(got), []byte(want)) {
			return
		}
		time.Sleep(testPollStep)
	}
	t.Fatalf("the stuckchild fixture never produced %q (got %q)", want, sc.out.String())
}

// stop kills the fixture process and waits for the drain loop to notice.
func (sc *stuckChild) stop() {
	if sc.cmd.Process != nil {
		_ = sc.cmd.Process.Kill()
	}
	_ = sc.ptmx.Close()
	_ = sc.cmd.Wait()
	<-sc.drainDone
}

func TestStuckChildEmitsAStartupBanner(t *testing.T) {
	sc := startStuckChild(t)
	pid := readPIDFile(t, sc.pidFile)
	if pid == os.Getpid() {
		t.Fatalf("the fixture's pid file names this test process, not a distinct child")
	}
}

// TestStuckChildStopsWritingOutputWhenToldTo proves switch (b): once
// stopWriting is set, no further heartbeat bytes arrive, and clearing it
// resumes them.
func TestStuckChildStopsWritingOutputWhenToldTo(t *testing.T) {
	sc := startStuckChild(t)
	sc.awaitOutput(t, stuckChildHeartbeatMarker)

	sc.stopWriting()
	before := len(sc.out.String())
	time.Sleep(20 * stuckChildPollInterval)
	after := len(sc.out.String())

	if after != before {
		t.Fatalf("output grew by %d bytes after stopWriting, want 0 growth", after-before)
	}

	sc.resumeWriting()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) && len(sc.out.String()) == after {
		time.Sleep(testPollStep)
	}
	if len(sc.out.String()) == after {
		t.Fatalf("output never resumed after resumeWriting")
	}
}

// TestStuckChildStopsConsumingInputWhenToldTo is the fixture's core
// contract and this task's required proof: with reading stopped, writes
// into the PTY master eventually stop being consumed.
//
// The lines written are newline-terminated on purpose: a PTY's canonical
// line discipline only queues a line for the reader once it sees the
// newline, and that queue has a small, real capacity (measured against this
// pty implementation at a little over 1KB) — once it is full with nobody
// draining it, master.Write blocks. An unterminated stream of raw bytes
// does NOT exhibit this (verified separately): the driver just discards
// whatever a single in-progress, never-terminated line overflows, which
// looks like sustained non-blocking acceptance and would make this test
// pass whether or not the switch worked at all.
//
// If stopReading were a no-op (the switch disabled), the fixture would keep
// draining its stdin exactly as pumpPTY drains a real child's stdout, the
// writer goroutine below would finish well inside the timeout, and the
// first fatal would fire.
func TestStuckChildStopsConsumingInputWhenToldTo(t *testing.T) {
	sc := startStuckChild(t)
	sc.stopReading()

	line := []byte("0123456789\n")
	const lines = 20000 // ~220KB: far more than the queue that blocks a writer holds
	finished := make(chan error, 1)
	go func() {
		for i := 0; i < lines; i++ {
			if _, err := sc.ptmx.Write(line); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()

	select {
	case err := <-finished:
		t.Fatalf("writing %d lines completed (err=%v) even with reading stopped; want the pty's input queue to fill and block the writer", lines, err)
	case <-time.After(2 * time.Second):
		// Wedged, as expected: the write has not been consumed.
	}

	// Clearing the switch must unwedge it: the fixture drains the backlog
	// and the writer goroutine, still blocked on the same Write call, gets
	// to finish.
	sc.resumeReading()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("writing the remaining lines failed once reading resumed: %v", err)
		}
	case <-time.After(testBudget):
		t.Fatalf("the writer never finished after resumeReading")
	}
}

// TestStuckChildStopsBothWhenToldTo proves switch (c): both directions can
// be disabled together, and the fixture is still alive underneath (merely
// unresponsive, not dead).
func TestStuckChildStopsBothWhenToldTo(t *testing.T) {
	sc := startStuckChild(t)
	sc.awaitOutput(t, stuckChildHeartbeatMarker)
	pid := readPIDFile(t, sc.pidFile)

	sc.stopReading()
	sc.stopWriting()

	before := len(sc.out.String())
	time.Sleep(20 * stuckChildPollInterval)
	if len(sc.out.String()) != before {
		t.Fatalf("output grew while both switches were off")
	}
	if !Alive(pid) {
		t.Fatalf("the fixture process died while both switches were off, want it merely unresponsive")
	}
}
