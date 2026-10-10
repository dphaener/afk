package host

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Integration coverage for one whole session lifecycle against a REAL host
// process: Serve running in a separate, setsid'd process with /dev/null stdio
// (the shape session.SpawnHost gives `afk _host`), a real unix socket, and
// the real Attach client on the other end. Everything else in this package
// tests Serve and Attach in isolation — in-process, or against a net.Pipe
// fake — so this is the only test that would catch a break in how the two
// halves meet over a real kernel socket.
//
// The host process is this same test binary re-exec'd through TestMain
// (lifecycleRoleEnv), rather than a `go build`-ed afk binary: it needs no
// toolchain at test time and keeps the whole lifecycle hermetic. The layer
// that is therefore NOT covered here is `_host`'s flag parsing in package
// main, which has its own unit test (TestBuildServeConfig in attach_test.go);
// childArgvFromCommandLine below mirrors the one behavior of it this test
// depends on.

const (
	// lifecycleRoleEnv, when set to lifecycleRoleServe, makes this test binary
	// behave as the detached host instead of running the test suite.
	lifecycleRoleEnv   = "AFK_TEST_HOST_ROLE"
	lifecycleRoleServe = "serve"

	lcNameEnv     = "AFK_TEST_HOST_NAME"
	lcWorktreeEnv = "AFK_TEST_HOST_WORKTREE"
	lcSocketEnv   = "AFK_TEST_HOST_SOCKET"
	lcMetaEnv     = "AFK_TEST_HOST_META"
	lcLogEnv      = "AFK_TEST_HOST_LOG"
)

func TestMain(m *testing.M) {
	if os.Getenv(lifecycleRoleEnv) == lifecycleRoleServe {
		os.Exit(lifecycleServeMain())
	}
	os.Exit(m.Run())
}

// lifecycleServeMain is the body of the host process: the same call
// `afk _host` makes, with the config taken from the environment and the
// child argv from everything after the `--` separator.
func lifecycleServeMain() int {
	cfg := ServeConfig{
		Name:     os.Getenv(lcNameEnv),
		Worktree: os.Getenv(lcWorktreeEnv),
		Socket:   os.Getenv(lcSocketEnv),
		MetaPath: os.Getenv(lcMetaEnv),
		LogPath:  os.Getenv(lcLogEnv),
		Argv:     childArgvFromCommandLine(os.Args[1:]),
	}
	if err := Serve(cfg); err != nil {
		// stdio is /dev/null in this process (as in production), so the log
		// file is the only place a failure can be reported. Serve writes
		// there itself whenever it gets far enough to open it.
		return 1
	}
	return 0
}

// childArgvFromCommandLine returns everything after the first bare `--`,
// mirroring what flag.FlagSet.Parse leaves in fs.Args() for `afk _host`.
func childArgvFromCommandLine(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}

// lifecycleChildScript is the stub child the host runs on its PTY. It has to
// do three things at once, which is why it is not simply `exec cat`:
//
//   - publish its own pid, so the test can assert the child is really gone
//     once the host shuts down (exec-less: $$ stays this shell's pid);
//   - echo input back through a TRANSFORM (CHILD[...]), so a round-trip
//     assertion cannot be satisfied by the PTY's own input echo;
//   - print a marker on SIGWINCH, which is the only client-observable
//     evidence of the host's attach-time resize nudge.
//
// Which half runs in the background is load-bearing, and was found the hard
// way: POSIX requires a shell with job control DISABLED to redirect an
// asynchronous list's stdin from /dev/null, so a backgrounded reader gets
// instant EOF and echoes nothing. The reader therefore stays in the
// foreground, where stdin is the PTY, with SIGWINCH set to ignore so its
// blocking read is never interrupted; the WINCH marker moves to a background
// subshell, which still receives SIGWINCH because job control is off and it
// shares the terminal's foreground process group. Its loop sleeps in short
// slices because a trap only runs between commands.
func lifecycleChildScript(pidFile string) string {
	return fmt.Sprintf(`echo $$ > %s
( trap 'printf "GOTWINCH\n"' WINCH; while true; do sleep 0.05; done ) &
trap '' WINCH
while IFS= read -r line; do printf 'CHILD[%%s]\n' "$line"; done`, pidFile)
}

// liveHost is one real host process under test.
type liveHost struct {
	cfg     ServeConfig
	cmd     *exec.Cmd
	pid     int
	pidFile string

	done chan struct{}
	err  error
}

// startLiveHost re-execs this test binary as a detached host and returns once
// it is accepting connections and has published host.json.
func startLiveHost(t *testing.T) *liveHost {
	t.Helper()
	requireSh(t)
	requireUnixBind(t)

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	worktree := t.TempDir()
	name := "lifecycle"
	cfg := ServeConfig{
		Name:     name,
		Worktree: worktree,
		Socket:   filepath.Join(shortSocketDir(t), "lc.sock"),
		MetaPath: filepath.Join(worktree, ".afk", name, "host.json"),
		LogPath:  filepath.Join(worktree, ".afk", name, "host.log"),
	}
	pidFile := filepath.Join(worktree, "child.pid")
	cfg.Argv = []string{"/bin/sh", "-c", lifecycleChildScript(pidFile)}

	// `-test.run=^$` is a safety net: if the role dispatch in TestMain ever
	// stopped working, the re-exec would run zero tests instead of the whole
	// suite recursively.
	cmd := exec.Command(self, append([]string{"-test.run=^$", "--"}, cfg.Argv...)...)
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(),
		lifecycleRoleEnv+"="+lifecycleRoleServe,
		lcNameEnv+"="+cfg.Name,
		lcWorktreeEnv+"="+cfg.Worktree,
		lcSocketEnv+"="+cfg.Socket,
		lcMetaEnv+"="+cfg.MetaPath,
		lcLogEnv+"="+cfg.LogPath,
	)

	// The same stdio discipline session.SpawnHost applies: explicit /dev/null
	// *os.File on all three streams (so exec.Cmd builds no pipes and the host
	// holds none of this process's descriptors) plus Setsid, so the host leads
	// its own session exactly as it does in production. The property itself is
	// regression-tested in internal/session/create_fd_test.go.
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer func() { _ = null.Close() }()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the host process: %v", err)
	}

	h := &liveHost{cfg: cfg, cmd: cmd, pid: cmd.Process.Pid, pidFile: pidFile, done: make(chan struct{})}
	go func() {
		h.err = cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() { h.stopQuietly(t) })

	h.waitReady(t)
	return h
}

// waitReady blocks until the host accepts connections and host.json names it.
func (h *liveHost) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for {
		if h.exited() {
			t.Fatalf("the host process exited during startup (%v); log:\n%s", h.err, h.logTail(t))
		}
		conn, err := net.Dial("unix", h.cfg.Socket)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the host never accepted a connection on %s; log:\n%s", h.cfg.Socket, h.logTail(t))
		}
		time.Sleep(testPollStep)
	}

	// host.json is published AFTER the socket is bound (Serve binds first so a
	// refused startup never spawns a second child), so accepting connections
	// does not imply the file exists yet — poll for it separately.
	var meta Meta
	deadline = time.Now().Add(testBudget)
	for {
		var err error
		if meta, err = ReadMeta(h.cfg.Worktree, h.cfg.Name); err == nil {
			break
		}
		if h.exited() {
			t.Fatalf("the host process exited during startup (%v); log:\n%s", h.err, h.logTail(t))
		}
		if time.Now().After(deadline) {
			t.Fatalf("the host never published %s: %v; log:\n%s", h.cfg.MetaPath, err, h.logTail(t))
		}
		time.Sleep(testPollStep)
	}
	if meta.PID != h.pid {
		t.Errorf("host.json pid = %d, want the host process %d", meta.PID, h.pid)
	}
	if meta.Socket != h.cfg.Socket {
		t.Errorf("host.json socket = %q, want %q", meta.Socket, h.cfg.Socket)
	}
}

func (h *liveHost) exited() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// awaitExit waits for the host process to exit and returns cmd.Wait's error.
func (h *liveHost) awaitExit(t *testing.T) error {
	t.Helper()
	select {
	case <-h.done:
		return h.err
	case <-time.After(testBudget):
		t.Fatalf("the host process (pid %d) did not exit within %s; log:\n%s", h.pid, testBudget, h.logTail(t))
		return nil
	}
}

// stopQuietly is the cleanup path: SIGKILL whatever is left, including the
// child's process group, and reap. Nothing is asserted — a test that got this
// far without an orderly shutdown has already reported why.
func (h *liveHost) stopQuietly(t *testing.T) {
	t.Helper()
	if !h.exited() {
		_ = syscall.Kill(h.pid, syscall.SIGKILL)
		select {
		case <-h.done:
		case <-time.After(testBudget):
		}
	}
	if pid := h.childPID(); pid > 0 {
		// The host puts the child in its own process group (pty.StartWithSize
		// sets Setsid), so one signal reaches the whole stub subtree.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// childPID reads the stub child's pid, or 0 if it never got that far.
func (h *liveHost) childPID() int {
	data, err := os.ReadFile(h.pidFile)
	if err != nil {
		return 0
	}
	pid := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		return 0
	}
	return pid
}

// logTail returns the host's own log, which is the only diagnostic the host
// process can leave behind (its stderr is /dev/null).
func (h *liveHost) logTail(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.cfg.LogPath)
	if err != nil {
		return fmt.Sprintf("<no host log at %s: %v>", h.cfg.LogPath, err)
	}
	return string(data)
}

// requireUnixBind skips when the environment forbids bind() on unix sockets.
// Sandboxed local runs do (EPERM); CI does not, so this never skips there —
// and it deliberately fails, rather than skips, on any other bind error, so a
// real regression cannot hide behind it.
func requireUnixBind(t *testing.T) {
	t.Helper()
	probe := filepath.Join(shortSocketDir(t), "bind-probe.sock")
	ln, err := net.Listen("unix", probe)
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("this environment forbids bind() on unix sockets: %v", err)
		}
		t.Fatalf("probing whether unix sockets can be bound: %v", err)
	}
	_ = ln.Close()
}

// TestHostLifecycleEndToEnd drives one session from spawn to teardown through
// the real socket, asserting every transition `afk attach` and the dashboard
// depend on.
func TestHostLifecycleEndToEnd(t *testing.T) {
	h := startLiveHost(t)
	childPID := readPIDFile(t, h.pidFile)

	// --- attach: data round-trip, and the attach-time resize nudge ---------
	first := newAttachStub(t, nil)
	firstResult := runAttach(first, h.cfg.Socket, testBudget)

	first.awaitOutput(t, "GOTWINCH")
	first.typeKeys(t, "hello-one\n")
	first.awaitOutput(t, "CHILD[hello-one]")

	// --- detach: the client goes away, the session keeps running -----------
	first.typeKeys(t, "\x1c")
	if r := awaitAttach(t, firstResult); r.err != nil || r.reason != DetachUser {
		t.Fatalf("first attach = (%v, %v), want (DetachUser, nil)", r.reason, r.err)
	}
	if h.exited() {
		t.Fatalf("the host process exited on detach (%v); ctrl-\\ must leave the session running", h.err)
	}
	if !Alive(childPID) {
		t.Fatalf("the stub child (pid %d) died on detach", childPID)
	}
	if _, err := os.Stat(h.cfg.MetaPath); err != nil {
		t.Errorf("host.json should survive a detach: %v", err)
	}

	// --- re-attach: works, and nudges the PTY again ------------------------
	// second's output buffer is fresh, and the child only prints GOTWINCH on
	// SIGWINCH, so seeing it here can only come from THIS attach's nudge.
	second := newAttachStub(t, nil)
	secondResult := runAttach(second, h.cfg.Socket, testBudget)
	second.awaitOutput(t, "GOTWINCH")
	second.typeKeys(t, "hello-two\n")
	second.awaitOutput(t, "CHILD[hello-two]")

	// --- a second concurrent attach steals the session --------------------
	third := newAttachStub(t, nil)
	thirdResult := runAttach(third, h.cfg.Socket, testBudget)

	if r := awaitAttach(t, secondResult); r.err != nil || r.reason != DetachStolen {
		t.Fatalf("the stolen attach = (%v, %v), want (DetachStolen, nil)", r.reason, r.err)
	}
	third.awaitOutput(t, "GOTWINCH")
	third.typeKeys(t, "hello-three\n")
	third.awaitOutput(t, "CHILD[hello-three]")

	third.typeKeys(t, "\x1c")
	if r := awaitAttach(t, thirdResult); r.err != nil || r.reason != DetachUser {
		t.Fatalf("third attach = (%v, %v), want (DetachUser, nil)", r.reason, r.err)
	}

	// --- Exit: the child dies, the host exits 0, both files are gone -------
	// Sent on a raw connection because Attach never sends Exit; this is the
	// frame `afk clean` uses to stop a session (internal/session/clean.go).
	exiter, err := net.Dial("unix", h.cfg.Socket)
	if err != nil {
		t.Fatalf("dialing the host to send Exit: %v", err)
	}
	if err := WriteFrame(exiter, Exit, nil); err != nil {
		t.Fatalf("sending Exit: %v", err)
	}
	_ = exiter.Close()

	if err := h.awaitExit(t); err != nil {
		t.Errorf("the host process exited with %v, want a clean exit 0; log:\n%s", err, h.logTail(t))
	}
	if Alive(childPID) {
		t.Errorf("the stub child (pid %d) outlived the host", childPID)
	}
	assertGone(t, h.cfg.Socket)
	assertGone(t, h.cfg.MetaPath)

	if log := h.logTail(t); !strings.Contains(log, "stopped") {
		t.Errorf("the host log does not record the shutdown:\n%s", log)
	}
}

// TestHostLifecycleChildExitEndsTheSessionForItsClient covers the other way a
// session ends — the child finishing on its own — through the same real
// socket, because that is what the dashboard decodes as ExitSessionEnded.
func TestHostLifecycleChildExitEndsTheSessionForItsClient(t *testing.T) {
	h := startLiveHost(t)
	childPID := readPIDFile(t, h.pidFile)

	client := newAttachStub(t, nil)
	result := runAttach(client, h.cfg.Socket, testBudget)
	client.awaitOutput(t, "GOTWINCH")

	// Kill the stub subtree from the outside: the child ending is the event
	// under test, and the host must notice it without any client action.
	if err := syscall.Kill(-childPID, syscall.SIGKILL); err != nil {
		t.Fatalf("killing the stub child's process group: %v", err)
	}

	if r := awaitAttach(t, result); r.err != nil || r.reason != DetachExited {
		t.Fatalf("attach after the child died = (%v, %v), want (DetachExited, nil)", r.reason, r.err)
	}
	if err := h.awaitExit(t); err != nil {
		t.Errorf("the host process exited with %v, want a clean exit 0; log:\n%s", err, h.logTail(t))
	}
	assertGone(t, h.cfg.Socket)
	assertGone(t, h.cfg.MetaPath)
}

// TestChildArgvFromCommandLine pins the `--` split the host role relies on.
func TestChildArgvFromCommandLine(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "argv after the separator", args: []string{"-test.run=^$", "--", "/bin/sh", "-c", "exec cat"}, want: []string{"/bin/sh", "-c", "exec cat"}},
		{name: "no separator means no child", args: []string{"-test.run=^$"}, want: nil},
		{name: "an empty tail means no child", args: []string{"--"}, want: []string{}},
		{name: "only the first separator splits", args: []string{"--", "sh", "--", "-c"}, want: []string{"sh", "--", "-c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := childArgvFromCommandLine(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("childArgvFromCommandLine(%q) = %q, want %q", tt.args, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("childArgvFromCommandLine(%q) = %q, want %q", tt.args, got, tt.want)
				}
			}
		})
	}
}
