package spice

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// --- Detection matrix ---------------------------------------------------

func TestDetector_Matrix(t *testing.T) {
	errNotFound := errors.New("not found")

	present := func(string) (string, error) { return "/usr/local/bin/gs", nil }
	absent := func(string) (string, error) { return "", errNotFound }

	tests := []struct {
		name          string
		binaryPresent bool
		repoSignal    bool
		want          bool
	}{
		{"binary present, repo initialized", true, true, true},
		{"binary present, repo not initialized", true, false, false},
		{"binary absent, repo initialized", false, true, false},
		{"binary absent, repo not initialized", false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookPath := absent
			if tt.binaryPresent {
				lookPath = present
			}
			d := Detector{
				LookPath: lookPath,
				RepoInit: func(dir string) bool { return tt.repoSignal },
			}
			got := d.Detect("/irrelevant")
			if got != tt.want {
				t.Errorf("Detect() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBinaryPath_PrefersCanonicalOverAlias(t *testing.T) {
	calls := []string{}
	lookPath := func(name string) (string, error) {
		calls = append(calls, name)
		if name == "git-spice" {
			return "/opt/homebrew/bin/git-spice", nil
		}
		return "", errors.New("not found")
	}

	path, ok := binaryPath(lookPath)
	if !ok {
		t.Fatalf("expected binary to resolve")
	}
	if path != "/opt/homebrew/bin/git-spice" {
		t.Errorf("path = %q, want canonical git-spice path", path)
	}
	if calls[0] != "git-spice" {
		t.Errorf("first probe = %q, want git-spice tried first", calls[0])
	}
}

func TestBinaryPath_FallsBackToAlias(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "gs" {
			return "/opt/homebrew/bin/gs", nil
		}
		return "", errors.New("not found")
	}

	path, ok := binaryPath(lookPath)
	if !ok {
		t.Fatalf("expected fallback binary to resolve")
	}
	if path != "/opt/homebrew/bin/gs" {
		t.Errorf("path = %q, want gs alias path", path)
	}
}

func TestBinaryPath_NeitherResolves(t *testing.T) {
	lookPath := func(name string) (string, error) { return "", errors.New("not found") }

	_, ok := binaryPath(lookPath)
	if ok {
		t.Errorf("expected no binary to resolve")
	}
}

// --- RepoInitialized against a real temp git repo -----------------------
//
// This exercises the empirically-validated detection signal directly: a
// repo is git-spice-initialized iff `git rev-parse --verify refs/spice/data`
// succeeds. It deliberately does NOT set `spice.trunk` via `git config`,
// mirroring the real-world claude-marche repo where that config key is
// empty even though git-spice is actively used.

func TestRepoInitialized(t *testing.T) {
	t.Run("not initialized", func(t *testing.T) {
		dir := newTempGitRepo(t)
		if RepoInitialized(dir) {
			t.Errorf("expected RepoInitialized = false for a plain git repo")
		}
	})

	t.Run("initialized via refs/spice/data, spice.trunk absent", func(t *testing.T) {
		dir := newTempGitRepo(t)

		// Confirm the landmine: spice.trunk is NOT set by this setup, exactly
		// like the real claude-marche repo.
		cmd := exec.Command("git", "-C", dir, "config", "--get", "spice.trunk")
		if err := cmd.Run(); err == nil {
			t.Fatalf("expected `git config --get spice.trunk` to fail (empty), it succeeded")
		}

		// Create the refs/spice/data ref the way git-spice's own repo-state
		// storage does: a ref pointing at a real commit.
		head := runGit(t, dir, "rev-parse", "HEAD")
		runGit(t, dir, "update-ref", "refs/spice/data", head)

		if !RepoInitialized(dir) {
			t.Errorf("expected RepoInitialized = true once refs/spice/data exists")
		}
	})
}

func newTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "commit", "--allow-empty", "-q", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		stderr := ""
		if errors.As(err, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		t.Fatalf("git %v failed: %v\n%s", args, err, stderr)
	}
	return trimNewline(string(out))
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// --- ParseLogShort --------------------------------------------------------

func TestParseLogShort_RealFixture(t *testing.T) {
	f := openFixture(t, "log_short.json")
	defer f.Close()

	branches, err := ParseLogShort(f)
	if err != nil {
		t.Fatalf("ParseLogShort: %v", err)
	}

	assertSortedStack(t, branches)
}

func TestParseLogShort_UnsortedFixture_SameResultRegardlessOfInputOrder(t *testing.T) {
	f := openFixture(t, "log_short_unsorted.json")
	defer f.Close()

	branches, err := ParseLogShort(f)
	if err != nil {
		t.Fatalf("ParseLogShort: %v", err)
	}

	assertSortedStack(t, branches)
}

func assertSortedStack(t *testing.T, branches []Branch) {
	t.Helper()

	if len(branches) != 3 {
		t.Fatalf("len(branches) = %d, want 3", len(branches))
	}

	wantOrder := []string{
		"feat/afk-adaptive-dual-mode",
		"feat/afk-go-orchestration",
		"main",
	}
	for i, want := range wantOrder {
		if branches[i].Name != want {
			t.Errorf("branches[%d].Name = %q, want %q", i, branches[i].Name, want)
		}
	}

	// Field-level spot checks on the real captured data.
	adaptive := branches[0]
	if adaptive.Down == nil || adaptive.Down.Name != "main" {
		t.Errorf("adaptive.Down = %+v, want down.name=main", adaptive.Down)
	}
	if len(adaptive.Ups) != 1 || adaptive.Ups[0].Name != "feat/afk-go-orchestration" {
		t.Errorf("adaptive.Ups = %+v, want one up feat/afk-go-orchestration", adaptive.Ups)
	}
	if adaptive.Change == nil || adaptive.Change.ID != "#293" {
		t.Errorf("adaptive.Change = %+v, want id #293", adaptive.Change)
	}
	if adaptive.Change != nil && adaptive.Change.URL != "https://github.com/Ontra-ai/claude-marche/pull/293" {
		t.Errorf("adaptive.Change.URL = %q, unexpected", adaptive.Change.URL)
	}
	if adaptive.Push == nil || adaptive.Push.Ahead != 0 || adaptive.Push.Behind != 0 {
		t.Errorf("adaptive.Push = %+v, want ahead=0 behind=0", adaptive.Push)
	}

	orchestration := branches[1]
	if !orchestration.Current {
		t.Errorf("orchestration.Current = false, want true")
	}
	if orchestration.Down == nil || orchestration.Down.Name != "feat/afk-adaptive-dual-mode" {
		t.Errorf("orchestration.Down = %+v, want down.name=feat/afk-adaptive-dual-mode", orchestration.Down)
	}

	main := branches[2]
	if len(main.Ups) != 1 || main.Ups[0].Name != "feat/afk-adaptive-dual-mode" {
		t.Errorf("main.Ups = %+v, want one up feat/afk-adaptive-dual-mode", main.Ups)
	}
	if main.Current {
		t.Errorf("main.Current = true, want false")
	}
}

func TestParseLogShort_EmptyStream(t *testing.T) {
	f := openFixture(t, "log_short_empty.json")
	defer f.Close()

	branches, err := ParseLogShort(f)
	if err != nil {
		t.Fatalf("ParseLogShort: %v", err)
	}
	if len(branches) != 0 {
		t.Errorf("len(branches) = %d, want 0", len(branches))
	}
}

func TestParseLogShort_InvalidJSON(t *testing.T) {
	r := strings.NewReader(`{"name":"main"} not-json`)
	if _, err := ParseLogShort(r); err == nil {
		t.Errorf("expected an error parsing malformed JSON stream")
	}
}

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	return f
}

// --- BuildBranchCreateArgs / BuildBranchCreateCmd ------------------------

func TestBuildBranchCreateArgs(t *testing.T) {
	tests := []struct {
		name string
		opts BranchCreateOptions
		want []string
	}{
		{
			name: "name and base",
			opts: BranchCreateOptions{Name: "feat/my-branch", Base: "main"},
			want: []string{"branch", "create", "feat/my-branch", "--target", "main", "--no-commit"},
		},
		{
			name: "name only, base defaults to current branch",
			opts: BranchCreateOptions{Name: "feat/my-branch"},
			want: []string{"branch", "create", "feat/my-branch", "--no-commit"},
		},
		{
			name: "no name, generated from commit message",
			opts: BranchCreateOptions{Base: "main"},
			want: []string{"branch", "create", "--target", "main", "--no-commit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildBranchCreateArgs(tt.opts)
			if !stringSlicesEqual(got, tt.want) {
				t.Errorf("BuildBranchCreateArgs() = %v, want %v", got, tt.want)
			}

			// No path in this package may construct `commit fixup` or reach
			// for `branch submit`/`stack submit`/`repo sync` — this is the
			// full universe of arguments this package ever builds.
			for _, arg := range got {
				if arg == "fixup" || arg == "submit" || arg == "sync" {
					t.Errorf("BuildBranchCreateArgs() produced forbidden arg %q", arg)
				}
			}
		})
	}
}

func TestBuildBranchCreateCmd(t *testing.T) {
	opts := BranchCreateOptions{
		Dir:  "/some/repo",
		Name: "feat/my-branch",
		Base: "main",
	}
	cmd := BuildBranchCreateCmd("/opt/homebrew/bin/gs", opts)

	if cmd.Dir != "/some/repo" {
		t.Errorf("cmd.Dir = %q, want /some/repo", cmd.Dir)
	}
	if cmd.Path != "/opt/homebrew/bin/gs" {
		t.Errorf("cmd.Path = %q, want /opt/homebrew/bin/gs", cmd.Path)
	}
	wantArgs := []string{"/opt/homebrew/bin/gs", "branch", "create", "feat/my-branch", "--target", "main", "--no-commit"}
	if !stringSlicesEqual(cmd.Args, wantArgs) {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
}

// --- TrackedBranch / IsTracked --------------------------------------------

// TestTrackedBranch is pure-logic, table-driven: every case is load-bearing
// because BranchDeleteOptions callers (W2.T3) decide the entire spice-vs-
// plain-git branch on this lookup, and a wrong answer in either direction
// either skips git-spice's stack bookkeeping or runs `gs branch delete`
// against a branch git-spice has never heard of.
func TestTrackedBranch(t *testing.T) {
	branches := []Branch{
		{Name: "main"},
		{Name: "feat/a", Down: &BranchRef{Name: "main"}, Ups: []BranchRef{{Name: "feat/b"}}},
		{Name: "feat/b", Down: &BranchRef{Name: "feat/a"}},
	}

	tests := []struct {
		name      string
		branches  []Branch
		lookup    string
		wantFound bool
		wantUps   int
	}{
		{"tracked, no upstack", branches, "feat/b", true, 0},
		{"tracked, with upstack", branches, "feat/a", true, 1},
		{"untracked name", branches, "feat/nonexistent", false, 0},
		{"empty topology", nil, "feat/a", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := TrackedBranch(tt.branches, tt.lookup)
			if found != tt.wantFound {
				t.Fatalf("TrackedBranch() found = %v, want %v", found, tt.wantFound)
			}
			if found && len(got.Ups) != tt.wantUps {
				t.Errorf("TrackedBranch() Ups = %v, want len %d", got.Ups, tt.wantUps)
			}
		})
	}
}

// TestIsTracked_DegradesWhenAbsent exercises the required-absent-safety
// case from the acceptance criteria directly against a plain (non-spice)
// temp repo, the same fixture RepoInitialized already relies on. It does
// not attempt the true-path (needs a real `gs`-tracked branch), which
// TrackedBranch's pure table test already covers without a subprocess.
func TestIsTracked_DegradesWhenAbsent(t *testing.T) {
	dir := newTempGitRepo(t)
	if IsTracked(dir, "main") {
		t.Errorf("IsTracked() = true for a non-spice repo, want false")
	}
}

// --- BuildBranchDeleteArgs / BuildBranchDeleteCmd -------------------------

// TestBuildBranchDeleteArgs is the argv table test the plan requires before
// any implementation: force and non-force both carry --restack=none (never
// the config-dependent default) and --no-prompt (never a hang waiting on a
// prompt). Both rows are load-bearing per the plan's acceptance criteria.
func TestBuildBranchDeleteArgs(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		force  bool
		want   []string
	}{
		{
			name:   "non-force",
			branch: "feat/my-branch",
			force:  false,
			want:   []string{"branch", "delete", "feat/my-branch", "--restack=none", "--no-prompt"},
		},
		{
			name:   "force",
			branch: "feat/my-branch",
			force:  true,
			want:   []string{"branch", "delete", "feat/my-branch", "--force", "--restack=none", "--no-prompt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildBranchDeleteArgs(tt.branch, tt.force)
			if !stringSlicesEqual(got, tt.want) {
				t.Errorf("BuildBranchDeleteArgs() = %v, want %v", got, tt.want)
			}

			hasRestackNone := false
			hasNoPrompt := false
			for _, arg := range got {
				if arg == "--restack=none" {
					hasRestackNone = true
				}
				if arg == "--no-prompt" {
					hasNoPrompt = true
				}
				// Same forbidden-argument universe as BuildBranchCreateArgs:
				// this package never constructs `commit fixup` or reaches for
				// `branch submit`/`stack submit`/`repo sync`.
				if arg == "fixup" || arg == "submit" || arg == "sync" {
					t.Errorf("BuildBranchDeleteArgs() produced forbidden arg %q", arg)
				}
			}
			if !hasRestackNone {
				t.Errorf("BuildBranchDeleteArgs() = %v, missing --restack=none", got)
			}
			if !hasNoPrompt {
				t.Errorf("BuildBranchDeleteArgs() = %v, missing --no-prompt", got)
			}
		})
	}
}

func TestBuildBranchDeleteCmd(t *testing.T) {
	opts := BranchDeleteOptions{
		Dir:   "/some/repo",
		Name:  "feat/my-branch",
		Force: true,
	}
	cmd := BuildBranchDeleteCmd("/opt/homebrew/bin/gs", opts)

	if cmd.Dir != "/some/repo" {
		t.Errorf("cmd.Dir = %q, want /some/repo", cmd.Dir)
	}
	if cmd.Path != "/opt/homebrew/bin/gs" {
		t.Errorf("cmd.Path = %q, want /opt/homebrew/bin/gs", cmd.Path)
	}
	wantArgs := []string{"/opt/homebrew/bin/gs", "branch", "delete", "feat/my-branch", "--force", "--restack=none", "--no-prompt"}
	if !stringSlicesEqual(cmd.Args, wantArgs) {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- LogShort invocation bounding ---------------------------------------

// TestBuildLogShortArgs pins --no-prompt onto the read path. The flag is the
// load-bearing row: a caller that redirects stdin to /dev/null still hands the
// child its controlling terminal, so a prompt has a tty to block on forever.
func TestBuildLogShortArgs(t *testing.T) {
	want := []string{"log", "short", "--json", "--no-prompt"}
	if got := BuildLogShortArgs(); !stringSlicesEqual(got, want) {
		t.Errorf("BuildLogShortArgs() = %v, want %v", got, want)
	}
}

// stubBinary writes an executable shell script to a temp dir and returns its
// path, so the invocation-bounding tests can drive logShort against a process
// whose behavior they control instead of a real git-spice.
func stubBinary(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub-gs")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("writing stub binary: %v", err)
	}
	return path
}

func TestLogShort_TimesOutInsteadOfBlockingForever(t *testing.T) {
	bin := stubBinary(t, "sleep 60\n")

	start := time.Now()
	_, err := logShort(bin, t.TempDir(), 200*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("logShort() returned nil error for a binary that never exits")
	}
	if !strings.Contains(err.Error(), "timed out after") {
		t.Errorf("error = %q, want it to name the timeout", err)
	}
	// The ceiling is the timeout plus the one-second WaitDelay, doubled for a
	// loaded CI box. Without the bound this call never returns at all.
	if elapsed > 4*time.Second {
		t.Errorf("logShort() took %s, want it bounded by the timeout", elapsed)
	}
}

// TestLogShort_TimeoutKillsGrandchildren is the row that actually protects the
// machine. git-spice forks a dozen concurrent `git` children per invocation,
// so a teardown that signals only the direct child leaves the expensive half
// of the work running — which is how abandoned refreshes accumulated
// thousands of live `git` processes.
func TestLogShort_TimeoutKillsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	bin := stubBinary(t, "sleep 60 &\necho $! > "+pidFile+"\nwait\n")

	// Three seconds, not a few hundred milliseconds: the timeout has to be
	// long enough for the stub to get scheduled and record its grandchild at
	// all. A tighter one passes alone and fails inside the full suite, where
	// the box is loaded — the interesting assertion is what the teardown
	// reaches, never how fast it fires.
	if _, err := logShort(bin, dir, 3*time.Second); err == nil {
		t.Fatal("logShort() returned nil error for a binary that never exits")
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("stub never recorded its grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("grandchild pid %q unparseable: %v", raw, err)
	}

	// Signal 0 is the same advisory liveness probe internal/discover uses: it
	// reports whether the pid is still addressable without touching it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("grandchild %d survived the timeout; the kill did not reach the process group", pid)
}

// TestLogShort_ParsesStubOutput keeps the happy path honest about the whole
// pipeline — a bounded, process-grouped invocation still has to hand its
// stdout to ParseLogShort.
func TestLogShort_ParsesStubOutput(t *testing.T) {
	bin := stubBinary(t, `printf '{"name":"feat-b"}{"name":"main","current":true}'`+"\n")

	branches, err := logShort(bin, t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatalf("logShort() error = %v", err)
	}
	if len(branches) != 2 || branches[0].Name != "feat-b" || branches[1].Name != "main" {
		t.Errorf("branches = %+v, want feat-b then main", branches)
	}
}

// --- BuildBranchTrackArgs / BuildBranchTrackCmd / TrackBranch --------

// TestBuildBranchTrackArgs is the argv table test for gs branch track.
// --no-prompt is non-negotiable: gs branch track with no --no-prompt can
// block on a prompt waiting for a terminal that a non-interactive caller
// never has. The load-bearing rows are: (a) with base — --base is included;
// (b) without base — --base is omitted.
func TestBuildBranchTrackArgs(t *testing.T) {
	tests := []struct {
		name string
		opts BranchTrackOptions
		want []string
	}{
		{
			name: "track with explicit base",
			opts: BranchTrackOptions{Name: "feat/my-branch", Base: "main"},
			want: []string{"--no-prompt", "branch", "track", "feat/my-branch", "--base", "main"},
		},
		{
			name: "track without base (empty string)",
			opts: BranchTrackOptions{Name: "feat/my-branch", Base: ""},
			want: []string{"--no-prompt", "branch", "track", "feat/my-branch"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildBranchTrackArgs(tt.opts)
			if !stringSlicesEqual(got, tt.want) {
				t.Errorf("BuildBranchTrackArgs() = %v, want %v", got, tt.want)
			}

			// Verify --no-prompt is always present
			hasNoPrompt := false
			for _, arg := range got {
				if arg == "--no-prompt" {
					hasNoPrompt = true
					break
				}
			}
			if !hasNoPrompt {
				t.Errorf("BuildBranchTrackArgs() = %v, missing --no-prompt", got)
			}
		})
	}
}

func TestBuildBranchTrackCmd(t *testing.T) {
	opts := BranchTrackOptions{
		Dir:  "/some/repo",
		Name: "feat/my-branch",
		Base: "main",
	}
	cmd := BuildBranchTrackCmd("/opt/homebrew/bin/git-spice", opts)

	if cmd.Dir != "/some/repo" {
		t.Errorf("cmd.Dir = %q, want /some/repo", cmd.Dir)
	}
	if cmd.Path != "/opt/homebrew/bin/git-spice" {
		t.Errorf("cmd.Path = %q, want /opt/homebrew/bin/git-spice", cmd.Path)
	}
	wantArgs := []string{"/opt/homebrew/bin/git-spice", "--no-prompt", "branch", "track", "feat/my-branch", "--base", "main"}
	if !stringSlicesEqual(cmd.Args, wantArgs) {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
}

// TestTrackBranchRefusesEmptyBase is the load-bearing safety test: TrackBranch
// MUST refuse to invoke anything when opts.Base is empty, returning an error
// without resolving the binary or running a command. This prevents the
// mis-parenting trap where gs would guess a parent — verified against
// git-spice 0.31.2: `gs --no-prompt branch track <name>` with no --base
// guesses and exits 0.
func TestTrackBranchRefusesEmptyBase(t *testing.T) {
	opts := BranchTrackOptions{
		Dir:  t.TempDir(),
		Name: "test-branch",
		Base: "", // Empty base — must be refused
	}

	err := TrackBranch(opts)
	if err == nil {
		t.Fatalf("TrackBranch() with empty base returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "refusing to track") {
		t.Errorf("TrackBranch() error = %q, want text containing 'refusing to track'", err.Error())
	}
}

// --- BuildBranchUntrackArgs / BuildBranchUntrackCmd / UntrackBranch --------

func TestBuildBranchUntrackArgs(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want []string
	}{
		{
			name: "untrack a branch",
			arg:  "feat/my-branch",
			want: []string{"--no-prompt", "branch", "untrack", "feat/my-branch"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildBranchUntrackArgs(tt.arg)
			if !stringSlicesEqual(got, tt.want) {
				t.Errorf("BuildBranchUntrackArgs() = %v, want %v", got, tt.want)
			}

			// Verify --no-prompt is present
			hasNoPrompt := false
			for _, arg := range got {
				if arg == "--no-prompt" {
					hasNoPrompt = true
					break
				}
			}
			if !hasNoPrompt {
				t.Errorf("BuildBranchUntrackArgs() = %v, missing --no-prompt", got)
			}
		})
	}
}

func TestBuildBranchUntrackCmd(t *testing.T) {
	cmd := BuildBranchUntrackCmd("/opt/homebrew/bin/git-spice", "/some/repo", "feat/my-branch")

	if cmd.Dir != "/some/repo" {
		t.Errorf("cmd.Dir = %q, want /some/repo", cmd.Dir)
	}
	if cmd.Path != "/opt/homebrew/bin/git-spice" {
		t.Errorf("cmd.Path = %q, want /opt/homebrew/bin/git-spice", cmd.Path)
	}
	wantArgs := []string{"/opt/homebrew/bin/git-spice", "--no-prompt", "branch", "untrack", "feat/my-branch"}
	if !stringSlicesEqual(cmd.Args, wantArgs) {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
}
