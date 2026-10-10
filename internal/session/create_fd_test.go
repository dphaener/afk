package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/host"
)

// This file is the CI tripwire for the stdio-redirect discipline in
// SpawnHost/buildHostCmd (W3.T1).
//
// The landmine it guards: the dashboard shells out with a non-*os.File
// Stdout/Stderr (io.MultiWriter — see internal/ui/messages.go's execAfk and
// spawnNewCmd). exec.Cmd then builds an os.Pipe per stream plus a copier
// goroutine, and Wait() does not return until every process holding the write
// end has closed it. The session host is deliberately long-lived, so if it
// inherited the spawner's fd 1/2 the dashboard's Wait() would block for the
// entire life of the session — a frozen TUI. buildHostCmd prevents that by
// pointing all three of the host's streams at an explicit /dev/null *os.File.
//
// TestBuildHostCmdStdioAndSetsid (create_test.go) asserts that statically, on
// the unstarted exec.Cmd. This file asserts it BEHAVIORALLY, across two real
// process boundaries, which is the only way to catch a regression that keeps
// the field assignments but loses the property (e.g. a "helpful" switch to
// cmd.Stderr = os.Stderr, which is itself the pipe in the spawning process).

const (
	// fdRoleEnv selects a re-exec role for this test binary. TestMain
	// dispatches on it BEFORE m.Run, so a role never runs the test suite.
	fdRoleEnv = "AFK_TEST_FD_ROLE"
	// fdRoleSpawner drives the real SpawnHost launch step, with this test
	// binary injected through Deps.Executable as the "afk" binary.
	fdRoleSpawner = "spawner"
	// fdRoleHost stands in for `afk _host`: it records the descriptors it
	// was handed, then stays alive.
	fdRoleHost = "host"

	fdNameEnv     = "AFK_TEST_FD_NAME"
	fdWorktreeEnv = "AFK_TEST_FD_WORKTREE"
	fdReportEnv   = "AFK_TEST_FD_REPORT"

	// fdSpawnerStderrMark is written to the spawner's stderr, so the test can
	// prove the pipe it is draining is genuinely wired to the child.
	fdSpawnerStderrMark = "spawner-stderr-mark"

	// fdHostLifetime is how long the stand-in host stays alive after
	// reporting. It must comfortably exceed fdWaitBudget: the whole
	// regression is "Wait returns while the host is still running", and the
	// self-exit means a failing test cannot strand the process.
	fdHostLifetime = 30 * time.Second
	// fdWaitBudget bounds the parent's Wait(). A correct spawn returns in
	// milliseconds; an inherited pipe would hold it for fdHostLifetime.
	fdWaitBudget = 10 * time.Second
)

// TestMain implements the re-exec dispatch both roles need. A plain
// os.Executable() inside a test resolves to the TEST binary, which is exactly
// why Deps.Executable exists as a seam — and it is also what makes this
// pattern work: the test binary can be both the spawner and the host.
func TestMain(m *testing.M) {
	switch os.Getenv(fdRoleEnv) {
	case fdRoleSpawner:
		os.Exit(fdSpawnerMain())
	case fdRoleHost:
		os.Exit(fdHostMain())
	}
	os.Exit(m.Run())
}

// fdSpawnerMain is the middle process: it runs the REAL SpawnHost launch step
// and exits immediately afterwards, exactly as `afk new` does.
func fdSpawnerMain() int {
	// The host inherits this process's environment (buildHostCmd sets no
	// cmd.Env), so the role has to be switched over BEFORE spawning —
	// otherwise the host would re-enter this branch and fork forever.
	if err := os.Setenv(fdRoleEnv, fdRoleHost); err != nil {
		fmt.Fprintf(os.Stderr, "spawner: switching the child's role: %v\n", err)
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawner: locating this binary: %v\n", err)
		return 1
	}
	spec := HostSpawn{Name: os.Getenv(fdNameEnv), Worktree: os.Getenv(fdWorktreeEnv)}
	if spec.Name == "" || spec.Worktree == "" {
		fmt.Fprintf(os.Stderr, "spawner: %s and %s must both be set\n", fdNameEnv, fdWorktreeEnv)
		return 1
	}

	h, err := SpawnHost(spec, Deps{Executable: func() (string, error) { return self, nil }})
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawner: SpawnHost: %v\n", err)
		return 1
	}

	// Both streams are pipes in this process. Writing to them proves the
	// parent's copier goroutines are live and draining, so a Wait() that
	// returns promptly cannot be explained by an unwired pipe.
	fmt.Fprintf(os.Stdout, "host-pid %d\n", h.PID)
	fmt.Fprintf(os.Stderr, "%s (host pid %d)\n", fdSpawnerStderrMark, h.PID)
	return 0
}

// fdReport is what the stand-in host records about the descriptors and argv it
// was launched with.
type fdReport struct {
	PID  int      `json:"pid"`
	Argv []string `json:"argv"`
	// <Stream>DevNull is true when the descriptor is the same file as
	// /dev/null; <Stream>Pipe is true when it is a pipe — i.e. inherited
	// from the spawner, which is the regression.
	StdinDevNull  bool `json:"stdin_dev_null"`
	StdoutDevNull bool `json:"stdout_dev_null"`
	StderrDevNull bool `json:"stderr_dev_null"`
	StdinPipe     bool `json:"stdin_pipe"`
	StdoutPipe    bool `json:"stdout_pipe"`
	StderrPipe    bool `json:"stderr_pipe"`
}

// fdHostMain is the stand-in for `afk _host`. It never binds a socket and
// never starts a child — this test is about descriptors, so the real Serve
// would only add a bind() (which some sandboxes deny) and a claude-shaped
// child argv it would then fail to exec.
func fdHostMain() int {
	rec := fdReport{
		PID:           os.Getpid(),
		Argv:          os.Args[1:],
		StdinDevNull:  fdIsDevNull(os.Stdin),
		StdoutDevNull: fdIsDevNull(os.Stdout),
		StderrDevNull: fdIsDevNull(os.Stderr),
		StdinPipe:     fdIsPipe(os.Stdin),
		StdoutPipe:    fdIsPipe(os.Stdout),
		StderrPipe:    fdIsPipe(os.Stderr),
	}
	if err := fdWriteReport(os.Getenv(fdReportEnv), rec); err != nil {
		// stderr is /dev/null here when the code under test is correct, so
		// there is nowhere useful to complain: the missing report file is
		// the signal the test reads.
		return 1
	}
	// Outlive the spawner by a wide margin. If this process were holding the
	// spawner's stderr pipe, the parent's Wait() would block for this long.
	time.Sleep(fdHostLifetime)
	return 0
}

// fdIsDevNull reports whether f refers to the same file as /dev/null.
func fdIsDevNull(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	null, err := os.Stat(os.DevNull)
	if err != nil {
		return false
	}
	return os.SameFile(fi, null)
}

// fdIsPipe reports whether f is a pipe — the shape exec.Cmd creates for a
// non-*os.File Stdout/Stderr, and therefore the shape an inherited descriptor
// would have in the host.
func fdIsPipe(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeNamedPipe != 0
}

// fdWriteReport writes rec to path atomically (temp file + same-directory
// rename), so the test can never read a half-written report.
func fdWriteReport(path string, rec fdReport) error {
	if path == "" {
		return fmt.Errorf("no report path")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fdReadReport polls until the stand-in host has published its report.
func fdReadReport(t *testing.T, path string) fdReport {
	t.Helper()
	deadline := time.Now().Add(fdWaitBudget)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			var rec fdReport
			if jsonErr := json.Unmarshal(data, &rec); jsonErr != nil {
				t.Fatalf("parsing %s (%q): %v", path, data, jsonErr)
			}
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("the spawned host never wrote %s (last error: %v)", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSpawnedHostDoesNotHoldTheSpawnersStdioPipes is the regression test for
// the documented TUI-freeze landmine. It reproduces the dashboard's exact
// situation — a parent whose child's Stdout/Stderr are non-*os.File writers,
// so exec.Cmd owns the pipes and Wait() waits for their EOF — and asserts
// Wait() returns promptly even though the host that child spawned is still
// running.
//
// It fails if anyone reverts the stdio discipline in buildHostCmd to an
// INHERITED descriptor — cmd.Stderr = os.Stderr, which in the spawning process
// IS the pipe: the host then holds the write end for fdHostLifetime and Wait()
// blocks past fdWaitBudget. (Dropping the assignment entirely is the other
// plausible regression, and is caught by TestBuildHostCmdStdioAndSetsid
// instead: exec.Cmd maps a nil Stderr to os.DevNull, so the descriptor stays
// harmless and only the explicitness is lost.)
func TestSpawnedHostDoesNotHoldTheSpawnersStdioPipes(t *testing.T) {
	shortTmpDir(t)
	worktree := t.TempDir()
	report := filepath.Join(worktree, "fd-report.json")

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Registered before the spawn so a failure anywhere below still reaps the
	// detached host instead of leaving it asleep for fdHostLifetime.
	t.Cleanup(func() {
		data, readErr := os.ReadFile(report)
		if readErr != nil {
			return
		}
		var rec fdReport
		if json.Unmarshal(data, &rec) == nil && rec.PID > 0 {
			_ = syscall.Kill(rec.PID, syscall.SIGKILL)
		}
	})

	cmd := exec.Command(self, "-test.run=^$")
	cmd.Env = append(os.Environ(),
		fdRoleEnv+"="+fdRoleSpawner,
		fdNameEnv+"=fdcheck",
		fdWorktreeEnv+"="+worktree,
		fdReportEnv+"="+report,
	)
	// THE POINT OF THE TEST: non-*os.File sinks, exactly like the
	// dashboard's io.MultiWriter. exec.Cmd creates an os.Pipe and a copier
	// goroutine for each, and Wait() blocks until both reach EOF.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout)
	cmd.Stderr = io.MultiWriter(&stderr)

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the spawner: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	waited := make(chan error, 1)
	start := time.Now()
	go func() { waited <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waited:
	case <-time.After(fdWaitBudget):
		t.Fatalf("Wait() on the spawner did not return within %s: the host it spawned is holding an inherited stdio pipe open "+
			"(the stdio redirect in buildHostCmd is the thing that prevents this). stderr so far: %q", fdWaitBudget, stderr.String())
	}
	elapsed := time.Since(start)
	if waitErr != nil {
		t.Fatalf("the spawner exited with %v\nstdout: %q\nstderr: %q", waitErr, stdout.String(), stderr.String())
	}
	t.Logf("Wait() returned in %s (budget %s, host lifetime %s)", elapsed, fdWaitBudget, fdHostLifetime)

	if !strings.Contains(stderr.String(), fdSpawnerStderrMark) {
		t.Errorf("the drained stderr pipe never carried %q (got %q) — the pipe under test was not actually wired to the spawner",
			fdSpawnerStderrMark, stderr.String())
	}

	rec := fdReadReport(t, report)

	// Non-vacuity: Wait() must have returned while the host was STILL
	// RUNNING. If the host had already exited, the pipes would have closed
	// for the wrong reason and this test would prove nothing.
	if !host.Alive(rec.PID) {
		t.Fatalf("the spawned host (pid %d) is already gone, so Wait() returning proves nothing about descriptor inheritance", rec.PID)
	}

	for _, s := range []struct {
		stream  string
		devNull bool
		pipe    bool
	}{
		{"stdin", rec.StdinDevNull, rec.StdinPipe},
		{"stdout", rec.StdoutDevNull, rec.StdoutPipe},
		{"stderr", rec.StderrDevNull, rec.StderrPipe},
	} {
		if s.pipe {
			t.Errorf("the host's %s is a PIPE: it inherited the spawner's descriptor, which is what wedges the dashboard", s.stream)
		}
		if !s.devNull {
			t.Errorf("the host's %s is not %s; SpawnHost must hand the host explicit null descriptors", s.stream, os.DevNull)
		}
	}

	// The host really was launched down the production path: SpawnHost's own
	// argv builder, `_host` flags first, child argv after the separator.
	wantArgv := hostArgs(HostSpawn{Name: "fdcheck", Worktree: worktree}, mustSocketPath(t, worktree, "fdcheck"))
	if !reflect.DeepEqual(rec.Argv, wantArgv) {
		t.Errorf("the host was launched with argv\n  %#v\nwant\n  %#v", rec.Argv, wantArgv)
	}
}

// mustSocketPath resolves the socket path SpawnHost derives for a session.
func mustSocketPath(t *testing.T, worktree, name string) string {
	t.Helper()
	socket, err := host.SocketPath(worktree, name)
	if err != nil {
		t.Fatalf("host.SocketPath: %v", err)
	}
	return socket
}

// TestFDIsDevNullAndFDIsPipeDiscriminate guards the two helpers the
// regression test's assertions rest on. A helper that always returned false
// would make the "not /dev/null" checks fail loudly, but one that always
// returned true would make them silently vacuous.
func TestFDIsDevNullAndFDIsPipeDiscriminate(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = null.Close() })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	if !fdIsDevNull(null) {
		t.Errorf("fdIsDevNull(%s) = false, want true", os.DevNull)
	}
	if fdIsDevNull(r) {
		t.Error("fdIsDevNull(pipe) = true, want false")
	}
	if !fdIsPipe(r) {
		t.Error("fdIsPipe(pipe) = false, want true")
	}
	if fdIsPipe(null) {
		t.Errorf("fdIsPipe(%s) = true, want false", os.DevNull)
	}
}
