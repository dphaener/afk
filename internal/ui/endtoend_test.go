package ui

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
)

// This file is the one test in the suite with no fakes in the middle of it.
// Every other pane test in this package (ui_test.go, mouse_test.go) drives a
// paneHandle stub, which is the right tool for asserting what the dashboard
// does with a pane but says nothing about whether the pane, the socket, the
// frame protocol, the host's serve loop, the wedge detector and the restart
// ladder actually compose. The incident this whole wave exists for lived in
// exactly that gap: every layer was individually correct and the session was
// still unrecoverable.
//
// So: a real host.Serve on a real unix socket, a real child on a real PTY, a
// real termpane.Pane over a real hostChild, and a real git repo for
// internal/discover to enumerate. The only things the test supplies are the
// child's behaviour (a shell script that goes silent and ignores SIGWINCH,
// the two properties of the wedged claude from the incident) and a fake
// `claude` on PATH for the resumed child — the argv that reaches it is
// production's, resumeArgv's, not one the test handed in.
//
// It cannot reuse internal/host's own W1.T1 fixture (stuckchild_test.go): that
// fixture is a re-exec of the host package's test binary and is invisible from
// here. What it reproduces instead are the fixture's two load-bearing
// properties, in a shell script this package can spawn — silence on demand,
// and a child that never reads its stdin — plus one the fixture gets for free
// from being a Go program: `stty -echo`, so that flooded input cannot echo
// back out of the PTY master and pass for the child's own output.
//
// Timing: the wedge verdict costs 30s of silence plus a 5s probe deadline.
// internal/host's own tests compress livenessTiming, which is package-private
// there and deliberately so; from this package the shipped schedule is the
// only schedule, hence the -short skip and the wide budget below.

const (
	// e2eOldBanner / e2eResumedBanner are what the two child generations
	// print. Short enough not to wrap at e2eCols, distinct enough that
	// finding one in the emulator's screen cannot be an accident.
	e2eOldBanner     = "AFK-E2E-OLD"
	e2eResumedBanner = "AFK-E2E-RESUMED"

	// e2eSessionName is the session directory name, which status.json's
	// plan_name must equal (status-schema.md §2 invariant 1) or the restart
	// refuses to read a session id out of it.
	e2eSessionName = "wedged"
	// e2eSessionID is the conversation the restart must resume.
	e2eSessionID = "11111111-2222-3333-4444-555555555555"

	e2eCols = 80
	e2eRows = 24

	// e2eFloodLines is how many CR-terminated lines are typed into the
	// wedged child. It has to exceed the PTY's own canonical input queue
	// (measured against this pty implementation at ~92 such lines, a little
	// over 1KB — see the notepad) so that a write genuinely blocks in the
	// kernel with nobody draining it, which is the state that used to stall
	// the host's serve loop.
	e2eFloodLines = 300

	// e2eNudgeMarker is the host log line every repaint nudge writes
	// (host.go's nudgeRepaint). Counting it is the only way to tell a verdict
	// the CHILD earned from one the host reached without ever asking: the
	// monitor arms the probe deadline as soon as the serve loop accepts the
	// request into its single-slot channel, so a serve loop that is stuck and
	// never consumes that request still produces a wedged_since — for the
	// wrong reason.
	e2eNudgeMarker = "nudged pty to force a repaint"

	// e2eWedgeBudget covers the shipped detection schedule (30s of silence,
	// a nudge, a 5s deadline, a 1s monitor tick) with room for a loaded
	// machine. e2eBudget is for everything else, all of which is
	// milliseconds of work.
	e2eWedgeBudget = 75 * time.Second
	e2eBudget      = 20 * time.Second
	e2ePollStep    = 25 * time.Millisecond
)

// TestWedgedSessionIsDetectedSurfacedAndRecoveredEndToEnd walks the whole
// chain in one session, in order, because each stage is the setup for the
// next: nothing after stage 3 can even be attempted unless the host really
// marked the child wedged.
//
// The load-bearing assertions, and what reverting one piece of production code
// would do to them:
//
//   - stage 3 (wedged_since lands in host.json) fails if the liveness monitor
//     stops probing, stops timing the probe out, or stops publishing — and
//     also if the serve loop goes back to writing input inline, because the
//     flood in stage 2 leaves a ptmx.Write blocked forever and a serve loop
//     stuck in it never delivers the nudge.
//   - stage 4 fails if internal/discover stops passing WedgedSince through,
//     or if renderRow stops drawing the badge.
//   - stage 5 fails if the pane's banner stops naming the recovery key, or
//     starts replacing the frozen screen instead of sitting above it.
//   - stage 6 fails if the Restart frame is not served, if the signal ladder
//     does not actually reap the child, or if the resumed argv is anything
//     other than `claude --resume <recorded id>`. The resumed child is silent
//     until stage 7 releases it, so a cleared wedged_since here can only have
//     come from the restart itself and not from output.
//   - stage 7 fails if the restart does not keep the socket, the host and the
//     attached client alive: the pane it asserts on is the same object, on the
//     same connection, from stage 1.
func TestWedgedSessionIsDetectedSurfacedAndRecoveredEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end wedge recovery takes ~40s: the host's shipped liveness schedule (30s silence + 5s probe deadline) is not tunable from this package")
	}

	f := startWedgeE2E(t)

	// Stage 1: the real chain carries the child's output to a real pane.
	pane, child, err := dialHostPane(hostPaneConfig{
		Name:     f.name,
		Worktree: f.repo,
		Cols:     e2eCols,
		Rows:     e2eRows,
	}, wedgeHintDashboard(defaultLeaderKey))
	if err != nil {
		t.Fatalf("opening a pane onto the session: %v", err)
	}
	t.Cleanup(func() { _ = pane.Close() })
	awaitScreen(t, pane, e2eOldBanner, e2eBudget)

	// Stage 2: wedge it — silent, ignoring SIGWINCH, and not reading a byte
	// of the input now piling up in its PTY.
	f.hush(t)
	nudges := f.countLog(t, e2eNudgeMarker)
	floodInput(t, pane)

	// Stage 3: the host works out on its own that the child is gone.
	wedged := awaitMeta(t, f, "wedged_since set", e2eWedgeBudget, func(m host.Meta) bool {
		return m.WedgedSince != ""
	})
	if got := f.countLog(t, e2eNudgeMarker); got <= nudges {
		t.Errorf("the host published a wedge verdict without ever nudging the child (%d nudges logged, %d before the silence started), so the verdict says nothing about the child", got, nudges)
	}
	if _, err := time.Parse(time.RFC3339, wedged.WedgedSince); err != nil {
		t.Fatalf("host.json wedged_since = %q, want an RFC3339 timestamp: %v", wedged.WedgedSince, err)
	}
	if wedged.LastOutputAt == "" {
		t.Errorf("host.json recorded a wedge but no last_output_at, so the dashboard cannot say since when")
	}
	oldChildPID := readPIDFile(t, f.oldPIDPath)

	// Stage 4: discovery reports it, and the row the dashboard draws shows it.
	st := awaitDiscover(t, f, "wedged_since", e2eBudget, func(s discover.SessionState) bool {
		return s.WedgedSince != ""
	})
	if st.WedgedSince != wedged.WedgedSince {
		t.Errorf("discovery reported wedged_since %q, want host.json's %q", st.WedgedSince, wedged.WedgedSince)
	}
	if !st.HostRunning {
		// Serve runs on this test's own goroutine, so the pid host.json
		// records is this process: signal-0 must find it.
		t.Errorf("discovery reported the wedged session's host as not running (pid %d)", st.HostPID)
	}
	if row := wedgeRow(st); !strings.Contains(row, wedgedGlyph) {
		t.Errorf("the session row for a wedged session does not carry the wedged flag %q:\n%s", wedgedGlyph, row)
	}

	// Stage 5: the pane explains the wedge without erasing the evidence.
	screen := awaitScreen(t, pane, "session unresponsive", e2eBudget)
	// The banner names the leader chord (ctrl+] r), since a bare r is the
	// agent's key now.
	if want := leaderChord(defaultLeaderKey, actionRestart); !strings.Contains(screen, want) {
		t.Errorf("the wedge banner never named the recovery chord %q:\n%s", want, screen)
	}
	if !strings.Contains(screen, e2eOldBanner) {
		t.Errorf("the wedge banner replaced the frozen screen instead of sitting above it:\n%s", screen)
	}

	// Stage 6: one Restart frame, over the pane's OWN connection, drives the
	// ladder and the respawn.
	if err := child.writeFrame(host.Restart, nil); err != nil {
		t.Fatalf("sending a Restart frame over the pane's connection: %v", err)
	}
	wantArgv := "claude --resume " + e2eSessionID
	resumed := awaitMeta(t, f, "the resumed argv with no wedge", e2eBudget, func(m host.Meta) bool {
		return strings.Join(m.Argv, " ") == wantArgv && m.WedgedSince == ""
	})
	if resumed.PID != wedged.PID {
		t.Errorf("host.json pid = %d after the restart, want the surviving host's %d", resumed.PID, wedged.PID)
	}
	awaitProcessGone(t, oldChildPID, e2eBudget)
	if got := strings.TrimSpace(readFileEventually(t, f.resumedArgsPath, e2eBudget)); got != "--resume "+e2eSessionID {
		t.Errorf("the resumed child was run with %q, want %q", got, "--resume "+e2eSessionID)
	}
	if newPID := readPIDFile(t, f.resumedPIDPath); newPID == oldChildPID {
		t.Errorf("the resumed child has the retired child's pid %d, so nothing was respawned", newPID)
	}

	// Stage 7: the same pane, on the same connection, paints the new child.
	f.release(t)
	awaitScreen(t, pane, e2eResumedBanner, e2eBudget)
	awaitScreenWithout(t, pane, "session unresponsive", e2eBudget)
	clear := awaitDiscover(t, f, "wedged_since cleared", e2eBudget, func(s discover.SessionState) bool {
		return s.WedgedSince == ""
	})
	if row := wedgeRow(clear); strings.Contains(row, wedgedGlyph) {
		t.Errorf("the session row still carries the wedged flag after the child came back:\n%s", row)
	}

	// Stage 8: the host shuts down cleanly and takes its own files with it.
	f.stop(t)
	if f.serveErr != nil {
		t.Errorf("Serve returned %v, want a clean shutdown", f.serveErr)
	}
	if _, err := os.Stat(f.cfg.Socket); !os.IsNotExist(err) {
		t.Errorf("the socket %s outlived the host (stat err = %v)", f.cfg.Socket, err)
	}
	if _, err := os.Stat(f.cfg.MetaPath); !os.IsNotExist(err) {
		t.Errorf("host.json outlived the host (stat err = %v)", err)
	}
}

// --- fixture ---------------------------------------------------------------

// wedgeE2EFixture is one end-to-end session: a real host.Serve goroutine, the
// real files it owns, and the marker paths the two child generations poll.
type wedgeE2EFixture struct {
	repo string
	name string
	dir  string // <repo>/.afk/<name>
	cfg  host.ServeConfig

	// oldPIDPath / hushPath control the first child: the pid it reports, and
	// the file whose appearance makes it fall silent forever.
	oldPIDPath string
	hushPath   string
	// resumedPIDPath / resumedArgsPath / releasePath are the resumed child's:
	// what it is, what it was run with, and when it may start talking.
	resumedPIDPath  string
	resumedArgsPath string
	releasePath     string

	stopped  chan struct{}
	serveErr error
}

func startWedgeE2E(t *testing.T) *wedgeE2EFixture {
	t.Helper()
	requireE2ETools(t)

	repo := e2eRepo(t)
	dir := filepath.Join(repo, ".afk", e2eSessionName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}

	f := &wedgeE2EFixture{
		repo:            repo,
		name:            e2eSessionName,
		dir:             dir,
		oldPIDPath:      filepath.Join(repo, "old.pid"),
		hushPath:        filepath.Join(repo, "old.hush"),
		resumedPIDPath:  filepath.Join(repo, "resumed.pid"),
		resumedArgsPath: filepath.Join(repo, "resumed.args"),
		releasePath:     filepath.Join(repo, "resumed.release"),
		stopped:         make(chan struct{}),
	}
	f.cfg = host.ServeConfig{
		Name:     f.name,
		Worktree: repo,
		Socket:   filepath.Join(e2eSocketDir(t), "e2e.sock"),
		MetaPath: filepath.Join(dir, "host.json"),
		LogPath:  filepath.Join(dir, "host.log"),
		Argv:     []string{"/bin/sh", "-c", f.oldChildScript()},
	}
	f.writeStatus(t)
	f.installFakeClaude(t)

	// Registered before the host starts, so it runs after the host has been
	// asked to stop: whatever a failed run leaves behind, no child of this
	// test survives it.
	t.Cleanup(f.killSurvivors)

	go func() {
		f.serveErr = host.Serve(f.cfg)
		close(f.stopped)
	}()
	t.Cleanup(f.stopQuietly)

	// host.json is written after the socket is bound and the child spawned,
	// so waiting for it costs no spurious connection — a probe dial would
	// attach as a client and immediately be dropped again. A Serve that has
	// already returned will never write it, so that is checked on every poll
	// and reported with Serve's own error: without this, a sandboxed run —
	// where unix bind() is blocked — fails minutes later with a baffling
	// "host.json never showed" instead of the actual refusal.
	deadline := time.Now().Add(e2eBudget)
	for {
		select {
		case <-f.stopped:
			t.Fatalf("host.Serve exited before publishing host.json: %v (under the sandbox this is the blocked unix bind(); run this package's tests unsandboxed)", f.serveErr)
		default:
		}
		if m, err := host.ReadMeta(f.repo, f.name); err == nil && m.Socket == f.cfg.Socket && m.PID == os.Getpid() {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("host.json never named this process and socket within %s", e2eBudget)
		}
		time.Sleep(e2ePollStep)
	}
}

// oldChildScript is the child from the incident, reduced to its two
// observable properties plus one convenience:
//
//   - it can be made silent forever (the hush marker), which is what starts
//     the silence the detector measures;
//   - it ignores SIGWINCH, so the probe's nudge goes unanswered and the
//     silence becomes a verdict;
//   - it never reads its stdin, so typed input piles up in the PTY exactly as
//     it did during the incident.
//
// `stty -echo` is what keeps that last property from muddying the first: with
// echo on, the line discipline reflects flooded input straight back out of the
// master, pumpPTY reads it as output and the silence clock resets. A real
// claude runs its terminal in raw mode with echo off, so this matches it — and
// it also keeps the flood out of the pane's screen, which stage 5 asserts
// still holds the child's last real output.
func (f *wedgeE2EFixture) oldChildScript() string {
	return fmt.Sprintf(`stty -echo
trap '' WINCH
echo $$ > %s
while [ ! -f %s ]; do echo %s; sleep 0.05; done
while true; do sleep 0.05; done
`, f.oldPIDPath, f.hushPath, e2eOldBanner)
}

// installFakeClaude puts an executable named `claude` first on PATH, so the
// host's own resumeArgv resolves to it: the argv under test is production's,
// argv[0] included. exec.LookPath consults this process's environment rather
// than the child's cmd.Env, which is why t.Setenv is the whole mechanism.
//
// The resumed child records what it was run with immediately and then stays
// SILENT until releasePath appears. That gap is load-bearing: "host.json shows
// no wedge after the restart" only says something about the restart while the
// resumed child has produced no output that could have cleared the verdict on
// its own.
func (f *wedgeE2EFixture) installFakeClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
stty -echo
echo "$@" > %s
echo $$ > %s
while [ ! -f %s ]; do sleep 0.02; done
while true; do echo %s; sleep 0.05; done
`, f.resumedArgsPath, f.resumedPIDPath, f.releasePath, e2eResumedBanner)

	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("installing the fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeStatus seeds the status.json the restart reads its session id out of.
// plan_name equals the directory name and phase is not `planning` with a
// non-empty session_id, so internal/state calls it valid rather than stale
// (status-schema.md §2 invariants 1 and 2).
func (f *wedgeE2EFixture) writeStatus(t *testing.T) {
	t.Helper()
	body := fmt.Sprintf(`{
  "session_id": %q,
  "plan_name": %q,
  "phase": "executing",
  "branch": "main",
  "base_branch": "main",
  "loop_count": 1,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": %q
}
`, e2eSessionID, f.name, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(f.dir, "status.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing status.json: %v", err)
	}
}

// countLog counts occurrences of want in the host's own log, which is the
// only record of what the serve loop actually did as opposed to what it was
// asked to do.
func (f *wedgeE2EFixture) countLog(t *testing.T, want string) int {
	t.Helper()
	data, err := os.ReadFile(f.cfg.LogPath)
	if err != nil {
		t.Fatalf("reading the host log %s: %v", f.cfg.LogPath, err)
	}
	return strings.Count(string(data), want)
}

func (f *wedgeE2EFixture) hush(t *testing.T) {
	t.Helper()
	f.touch(t, f.hushPath)
}

func (f *wedgeE2EFixture) release(t *testing.T) {
	t.Helper()
	f.touch(t, f.releasePath)
}

func (f *wedgeE2EFixture) touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("writing the marker %s: %v", path, err)
	}
}

// stop asks the host to exit the way `afk stop` does and waits for Serve to
// return, so the test can assert on its error and on the files it removed. The
// Exit frame needs a fresh connection because the serve loop only acts on
// frames from the client that currently holds the session, and this dial
// steals it from the pane.
func (f *wedgeE2EFixture) stop(t *testing.T) {
	t.Helper()
	conn, err := net.Dial("unix", f.cfg.Socket)
	if err != nil {
		t.Fatalf("dialing %s to stop the host: %v", f.cfg.Socket, err)
	}
	if err := host.WriteFrame(conn, host.Exit, nil); err != nil {
		t.Fatalf("sending Exit: %v", err)
	}
	_ = conn.Close()
	select {
	case <-f.stopped:
	case <-time.After(e2eBudget):
		t.Fatalf("Serve did not return within %s of an Exit frame", e2eBudget)
	}
}

// stopQuietly is the cleanup path: the same Exit, best effort, ignoring every
// outcome, so a test that failed halfway does not leave a host serving.
func (f *wedgeE2EFixture) stopQuietly() {
	select {
	case <-f.stopped:
		return
	default:
	}
	if conn, err := net.Dial("unix", f.cfg.Socket); err == nil {
		_ = host.WriteFrame(conn, host.Exit, nil)
		_ = conn.Close()
	}
	select {
	case <-f.stopped:
	case <-time.After(e2eBudget):
	}
}

// killSurvivors is the last line of defence: if the host never got the chance
// to reap a child (a panic, a timeout, an Exit that went nowhere), SIGKILL
// whatever the fixture's pid files still name. Both children run in their own
// process group under their own PTY, so the group kill reaches their `sleep`
// too.
func (f *wedgeE2EFixture) killSurvivors() {
	for _, path := range []string{f.oldPIDPath, f.resumedPIDPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			continue
		}
		if !host.Alive(pid) {
			continue
		}
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// --- environment -----------------------------------------------------------

// requireE2ETools skips rather than fails where the fixture's tools are
// missing, following this repo's existing precedent for /bin/sh (host_test.go's
// requireSh) and git (discover_test.go's real scripted repos).
func requireE2ETools(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available to run the fixture children")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available for internal/discover to enumerate worktrees")
	}
}

// e2eSocketDir returns a directory short enough for a unix socket path to fit
// sockaddr_un's ~104 bytes; t.TempDir() on macOS is far too long. Mirrors
// internal/host's shortSocketDir, which this package cannot reach.
func e2eSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "afk-ui-e2e")
	if err != nil {
		t.Skipf("no short temp dir available for a unix socket: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if len(dir) > 80 {
		t.Skipf("no short temp dir available for a unix socket (got %d bytes: %s)", len(dir), dir)
	}
	return dir
}

// e2eRepo scripts a real one-commit git repo, because internal/discover's
// entire job is reflecting real `git worktree list --porcelain` output. The
// path is symlink-resolved so it matches what git reports (macOS resolves
// /var to /private/var), which is what makes the discovered WorktreePath
// comparable to the one the host was given.
func e2eRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp repo path: %v", err)
	}
	e2eGit(t, dir, "init", "-b", "main")
	e2eGit(t, dir, "config", "user.name", "afk Test")
	e2eGit(t, dir, "config", "user.email", "afk-test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("seeding the repo: %v", err)
	}
	e2eGit(t, dir, "add", "-A")
	e2eGit(t, dir, "commit", "-m", "chore: Initial commit")
	return dir
}

func e2eGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s (dir=%s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// --- assertions ------------------------------------------------------------

// floodInput types more CR-terminated lines at the child than its PTY's input
// queue can hold, through the same emulator path a keystroke takes. CR rather
// than LF because that is what Enter sends and what the line discipline turns
// into a complete line; an unterminated byte stream is discarded by the driver
// instead of queued, and would never block a writer no matter how much of it
// arrived (see the notepad).
//
// It runs on its own goroutine and is bounded by a select rather than a write
// deadline, the same way internal/host's fixture bounds its writes: nothing in
// this path (an os.Pipe into the emulator, then a socket) supports one, and a
// host that has stopped reading its socket would otherwise turn this into a
// ten-minute panic instead of a failure that names itself.
func floodInput(t *testing.T, pane paneHandle) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < e2eFloodLines; i++ {
			pane.SendText("0123456789\r")
		}
	}()
	select {
	case <-done:
	case <-time.After(e2eBudget):
		t.Fatalf("typing %d lines at the session blocked for %s: the host has stopped reading its socket", e2eFloodLines, e2eBudget)
	}
}

// awaitScreen polls the pane's rendered screen — the same call views.go's
// renderPane makes — until want appears, and returns the screen it found it
// in.
func awaitScreen(t *testing.T, pane paneHandle, want string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last string
	for {
		last = pane.Render()
		if strings.Contains(last, want) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pane never rendered %q within %s; last screen was:\n%s", want, budget, last)
		}
		time.Sleep(e2ePollStep)
	}
}

// awaitScreenWithout is awaitScreen's complement, for content that must go
// away on its own schedule (the banner is redrawn from a background poll of
// host.json, so it outlives the recovery by up to one poll interval).
func awaitScreenWithout(t *testing.T, pane paneHandle, unwanted string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		screen := pane.Render()
		if !strings.Contains(screen, unwanted) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pane still rendered %q %s after the child recovered:\n%s", unwanted, budget, screen)
		}
		time.Sleep(e2ePollStep)
	}
}

// awaitMeta polls host.json through the same reader the dashboard uses until
// want holds. A read error is a retry, never a failure: the host rewrites the
// file by atomic rename and a reader is expected to treat anything it cannot
// parse as stale.
func awaitMeta(t *testing.T, f *wedgeE2EFixture, what string, budget time.Duration, want func(host.Meta) bool) host.Meta {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last host.Meta
	var lastErr error
	for {
		m, err := host.ReadMeta(f.repo, f.name)
		if err == nil {
			last, lastErr = m, nil
			if want(m) {
				return m
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("host.json never showed %s within %s (last read: %+v, err %v)", what, budget, last, lastErr)
		}
		time.Sleep(e2ePollStep)
	}
}

// awaitDiscover runs the real discovery pass — real git, real file reads — and
// polls until the session it returns satisfies want.
func awaitDiscover(t *testing.T, f *wedgeE2EFixture, what string, budget time.Duration, want func(discover.SessionState) bool) discover.SessionState {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last discover.SessionState
	for {
		states, err := discover.Discover(f.repo, discover.DefaultDeps())
		if err != nil {
			t.Fatalf("discovery over %s: %v", f.repo, err)
		}
		found := false
		for _, s := range states {
			if s.Name != f.name {
				continue
			}
			found = true
			last = s
			if want(s) {
				return s
			}
		}
		if !found && time.Now().After(deadline) {
			t.Fatalf("discovery never reported a session named %q in %s (got %d sessions)", f.name, f.repo, len(states))
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery never reported %s within %s (last: %+v)", what, budget, last)
		}
		time.Sleep(e2ePollStep)
	}
}

// wedgeRow renders one discovered session exactly as the dashboard's list
// does. The clock is the real one, not testDeps' fixed instant, because the
// wedge age under test was written by the host seconds ago.
func wedgeRow(st discover.SessionState) string {
	m := testModel(Session{SessionState: st})
	m.deps.Now = time.Now
	return m.renderRow(m.sessions[0], false, 100)
}

// awaitProcessGone waits for a pid to stop existing, via the same advisory
// signal-0 check internal/discover uses.
func awaitProcessGone(t *testing.T, pid int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if !host.Alive(pid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the retired child (pid %d) was still alive %s after the restart, so the signal ladder never reaped it", pid, budget)
		}
		time.Sleep(e2ePollStep)
	}
}

// readPIDFile reads a pid a fixture child wrote about itself, waiting for the
// file to appear.
func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(readFileEventually(t, path, e2eBudget)))
	if err != nil {
		t.Fatalf("parsing the pid in %s: %v", path, err)
	}
	return pid
}

// readFileEventually returns path's contents once it is non-empty. A child
// writing a marker file is not atomic about it, so an empty read is a retry.
func readFileEventually(t *testing.T, path string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		data, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(data)) != "" {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared with content within %s (err %v)", path, budget, err)
		}
		time.Sleep(e2ePollStep)
	}
}
