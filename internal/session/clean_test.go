package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/spice"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/worktree"
)

// --- real-git-repo test fixtures -----------------------------------------
//
// Unlike create_test.go (which hermetically fakes `Run`), afk clean's
// acceptance criteria call for temp git repos: the behaviors under test
// (dirty-worktree detection, snapshot commit, worktree removal, branch
// survival, and .afk/ archiving) are exactly the boundary this package
// exists to get right, and a fake Run could not catch a wrong git invocation
// or a real filesystem race in the copy-then-verify step. `git` itself must
// be on PATH; every other CLI test in this module (create_test.go) already
// assumes that.

// runGit runs git with args in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// newTestRepo creates a bare-bones git repo at a temp dir with one commit on
// branch "main" and local user.name/user.email set (CI/sandbox environments
// may have no global git identity configured). The returned path is
// symlink-resolved: on macOS, t.TempDir() lives under /var, which is itself
// a symlink to /private/var, and `git worktree list --porcelain` reports the
// resolved /private/var form — resolving up front keeps every path
// comparison in these tests consistent with what Discover sees.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "afk Test")
	runGit(t, dir, "config", "user.email", "afk-test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	// Real afk-enabled repos gitignore `.afk/` (see status-schema.md /
	// this repo's own .gitignore) — a project-level .gitignore, not a
	// developer's global excludes file, is what tests should depend on so
	// behavior is identical regardless of the machine running them.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".afk/\n"), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "chore: Initial commit")
	return dir
}

// addWorktree creates a new worktree at <repo's parent>/<branch> checked out
// on a fresh branch named branch, and returns its path.
func addWorktree(t *testing.T, repo, branch string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(repo), "wt-"+branch)
	runGit(t, repo, "worktree", "add", "-b", branch, path, "main")
	return path
}

// writeStatus seeds a minimal, schema-valid status.json for name inside
// worktree, with the given phase.
func writeStatus(t *testing.T, worktree, name, phase string) string {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	st := Status{
		SessionID:     "11111111-1111-1111-1111-111111111111",
		PlanName:      name,
		Phase:         phase,
		Branch:        name,
		BaseBranch:    "main",
		LoopCount:     0,
		AwaitingInput: false,
		TmuxWindow:    "",
		UpdatedAt:     "2026-07-09T21:14:03Z",
	}
	data, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	path := filepath.Join(dir, "status.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write status.json: %v", err)
	}
	return path
}

// testCleanDeps wires CleanDeps to the real `git` binary (defaultRun) and a
// fixed clock/homedir, so the archive path is deterministic and confined to
// a temp dir rather than the real ~/.local/share.
func testCleanDeps(t *testing.T, home string) CleanDeps {
	t.Helper()
	return CleanDeps{
		Run:     defaultRun,
		Now:     func() time.Time { return time.Date(2026, 7, 9, 21, 14, 3, 0, time.UTC) },
		HomeDir: func() (string, error) { return home, nil },
		Stdout:  &strings.Builder{},
		Stderr:  &strings.Builder{},
	}
}

// --- Discover -------------------------------------------------------------

func TestDiscover_MatchedSession(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "my-session")
	writeStatus(t, wt, "my-session", "executing")

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.Name != "my-session" {
		t.Errorf("Name = %q, want %q", got.Name, "my-session")
	}
	if got.Branch != "my-session" {
		t.Errorf("Branch = %q, want %q", got.Branch, "my-session")
	}
	if got.WorktreePath != wt {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, wt)
	}
	if got.Status.Phase != "executing" {
		t.Errorf("Status.Phase = %q, want %q", got.Status.Phase, "executing")
	}
}

// TestDiscover_StatusWithoutWorktreeIsACleanableSession is the headline of
// (W1.T1): a status-backed `.afk/{name}/` whose carrier worktree is checked
// out on some other branch is a Session, not an unactionable report. Every
// by-name clean resolution searches Sessions only, so this shape being
// anything else is exactly what made `afk clean {name}` answer `no session
// named %q found` for a row the user could see.
//
// The asserted fields are the ones the teardown steers by:
//
//   - Kind is KindStatusWithoutWorktree, which is what routes the clean to
//     the zero-git path (see cleanStatusWithoutWorktree).
//   - Branch is EMPTY. The carrier's branch is not this session's, and
//     inheriting it would hand `git branch -D` a branch the user is standing
//     on.
//   - WorktreePath is the CARRIER, because that is where `.afk/{name}/`
//     actually lives and the archive step has to find it.
//   - Reason is non-empty and describes the carrier mismatch rather than
//     claiming no worktree exists for the name — a healthy session of the
//     same name may exist elsewhere, which would make that claim false.
//
// The corrupt subtest is load-bearing: KindStatusWithoutWorktree OVERRIDES
// KindStaleStatus. Routing on the mismatch is the safety property — a stale
// label sends the teardown back into the carrier with git — so the lesser
// fault must not win.
func TestDiscover_StatusWithoutWorktreeIsACleanableSession(t *testing.T) {
	cases := []struct {
		name      string
		seed      func(t *testing.T, repo string) string
		wantPhase string
	}{
		{
			name: "parsed status.json",
			seed: func(t *testing.T, repo string) string {
				// Seeded in the primary worktree (checked out on "main"),
				// which is exempt from the "worktree without status" check
				// per the package-level scope decision — so this fixture
				// produces exactly one entry, the status-side one.
				return writeStatus(t, repo, "stranded-session", "done")
			},
			wantPhase: "done",
		},
		{
			name: "corrupt status.json",
			seed: func(t *testing.T, repo string) string {
				return writeRawStatusFile(t, repo, "stranded-session", "{not json")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			statusPath := tc.seed(t, repo)

			sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(sessions) != 1 {
				t.Fatalf("expected 1 session, got %d: %+v", len(sessions), sessions)
			}
			got := sessions[0]
			if got.Name != "stranded-session" {
				t.Errorf("Name = %q, want %q", got.Name, "stranded-session")
			}
			if got.Kind != KindStatusWithoutWorktree {
				t.Errorf("Kind = %q, want %q", got.Kind, KindStatusWithoutWorktree)
			}
			if got.Branch != "" {
				t.Errorf("Branch = %q, want empty: the carrier's branch is not this session's", got.Branch)
			}
			if got.WorktreePath != repo {
				t.Errorf("WorktreePath = %q, want the carrier %q", got.WorktreePath, repo)
			}
			if got.StatusPath != statusPath {
				t.Errorf("StatusPath = %q, want %q", got.StatusPath, statusPath)
			}
			if got.Status.Phase != tc.wantPhase {
				t.Errorf("Status.Phase = %q, want %q", got.Status.Phase, tc.wantPhase)
			}
			if got.Reason == "" {
				t.Fatal("Reason is empty; this row has nothing to explain its shape with")
			}
			if strings.Contains(got.Reason, "no worktree is checked out") {
				t.Errorf("Reason = %q; it must describe the carrier mismatch, not claim no worktree exists for the name", got.Reason)
			}
			if !strings.Contains(got.Reason, repo) {
				t.Errorf("Reason = %q, want it to name the carrier worktree %q", got.Reason, repo)
			}
		})
	}
}

// TestDiscover_WorktreeWithoutStatusIsAFaultedSession pins the worktree side
// of the reconciliation: a non-primary worktree with no session directory of
// its own is a Session carrying a fault Kind, because it owns a worktree a
// teardown has to remove and only a Session has a teardown.
func TestDiscover_WorktreeWithoutStatusIsAFaultedSession(t *testing.T) {
	repo := newTestRepo(t)
	// A non-primary worktree with no .afk/ session directory at all.
	wt := addWorktree(t, repo, "stray-branch")

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d: %+v", len(sessions), sessions)
	}
	if sessions[0].Kind != KindWorktreeNoSession {
		t.Errorf("Kind = %q, want %q", sessions[0].Kind, KindWorktreeNoSession)
	}
	if sessions[0].Name != "stray-branch" {
		t.Errorf("Name = %q, want %q", sessions[0].Name, "stray-branch")
	}
	if sessions[0].WorktreePath != wt {
		t.Errorf("WorktreePath = %q, want %q", sessions[0].WorktreePath, wt)
	}
	if sessions[0].Branch != "stray-branch" {
		t.Errorf("Branch = %q, want %q", sessions[0].Branch, "stray-branch")
	}
}

func TestDiscover_PrimaryWorktreeWithNoSessionIsNotDiscovered(t *testing.T) {
	// The repo's own primary worktree (checked out on "main", no .afk/
	// session) must never be flagged as "worktree without status" — that
	// would fire on every repo that hasn't run a afk session yet.
	repo := newTestRepo(t)

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no sessions for a bare primary worktree, got %+v", sessions)
	}
}

// TestDiscover_BothReconciliationDirectionsTogether pins that both directions
// of the status/worktree mismatch are detected together and arrive in ONE
// slice, each carrying the Kind its teardown is routed on: the status-side
// entry is KindStatusWithoutWorktree, the worktree-side one
// KindWorktreeNoSession, and the healthy session is unaffected by either.
func TestDiscover_BothReconciliationDirectionsTogether(t *testing.T) {
	repo := newTestRepo(t)

	matched := addWorktree(t, repo, "good-session")
	writeStatus(t, matched, "good-session", "reviewing")

	// Seeded in the primary worktree so it contributes exactly one entry (the
	// status-side one) rather than also tripping the worktree-without-status
	// check against its own carrier branch.
	writeStatus(t, repo, "status-only-session", "failed")

	addWorktree(t, repo, "worktree-only-branch") // no status.json

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	type key struct {
		name string
		kind Kind
	}
	want := []key{
		{"good-session", KindHealthy},
		{"status-only-session", KindStatusWithoutWorktree},
		{"worktree-only-branch", KindWorktreeNoSession},
	}
	if len(sessions) != len(want) {
		t.Fatalf("got %d sessions, want %d: %+v", len(sessions), len(want), sessions)
	}
	for i, w := range want {
		if got := (key{sessions[i].Name, sessions[i].Kind}); got != w {
			t.Errorf("session[%d] = %+v, want %+v", i, got, w)
		}
	}
}

// --- CleanSession -----------------------------------------------------------

func TestCleanSession_DirtyWorktreeSnapshotCommittedThenRemoved(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "dirty-session")
	statusPath := writeStatus(t, wt, "dirty-session", "done")

	// Dirty the worktree with a tracked, uncommitted change.
	if err := os.WriteFile(filepath.Join(wt, "work.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatalf("write work.txt: %v", err)
	}

	sess := Session{
		Name:         "dirty-session",
		WorktreePath: wt,
		Branch:       "dirty-session",
		StatusPath:   statusPath,
		Status:       Status{Phase: "done"},
	}
	home := t.TempDir()
	deps := testCleanDeps(t, home)

	result, err := CleanSession(repo, sess, deps)
	if err != nil {
		t.Fatalf("CleanSession: %v", err)
	}

	if !result.SnapshotCommitted {
		t.Error("SnapshotCommitted = false, want true for a dirty worktree")
	}
	if result.Branch != "dirty-session" {
		t.Errorf("Branch = %q, want %q", result.Branch, "dirty-session")
	}

	// The worktree directory itself must be gone.
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists after CleanSession (err=%v)", wt, err)
	}

	// The branch must survive (never deleted/force-deleted).
	branches := runGit(t, repo, "branch", "--list", "dirty-session")
	if !strings.Contains(branches, "dirty-session") {
		t.Fatalf("branch dirty-session was deleted; git branch --list output: %q", branches)
	}

	// The snapshot commit must exist on that branch with the mandated
	// Conventional-Commits message, and must include work.txt.
	log := runGit(t, repo, "log", "dirty-session", "-1", "--pretty=%s")
	wantMsg := "chore(afk): snapshot session 'dirty-session'"
	if strings.TrimSpace(log) != wantMsg {
		t.Errorf("snapshot commit subject = %q, want %q", strings.TrimSpace(log), wantMsg)
	}
	show := runGit(t, repo, "show", "--stat", "--pretty=", "dirty-session")
	if !strings.Contains(show, "work.txt") {
		t.Errorf("snapshot commit does not include work.txt; git show --stat: %q", show)
	}

	// The archive must exist and contain the archived status.json.
	if result.ArchivePath == "" {
		t.Fatal("ArchivePath is empty")
	}
	archivedStatus := filepath.Join(result.ArchivePath, "status.json")
	if _, err := os.Stat(archivedStatus); err != nil {
		t.Errorf("archived status.json missing at %s: %v", archivedStatus, err)
	}
	if !strings.HasPrefix(result.ArchivePath, filepath.Join(home, ".local", "share", "afk", "archive")) {
		t.Errorf("ArchivePath %q not under expected archive root", result.ArchivePath)
	}
}

func TestCleanSession_CleanWorktreeSkipsSnapshotCommit(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "tidy-session")
	statusPath := writeStatus(t, wt, "tidy-session", "done")

	sess := Session{
		Name:         "tidy-session",
		WorktreePath: wt,
		Branch:       "tidy-session",
		StatusPath:   statusPath,
		Status:       Status{Phase: "done"},
	}
	deps := testCleanDeps(t, t.TempDir())

	result, err := CleanSession(repo, sess, deps)
	if err != nil {
		t.Fatalf("CleanSession: %v", err)
	}
	if result.SnapshotCommitted {
		t.Error("SnapshotCommitted = true, want false for a clean worktree")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists after CleanSession", wt)
	}
	branches := runGit(t, repo, "branch", "--list", "tidy-session")
	if !strings.Contains(branches, "tidy-session") {
		t.Fatal("branch tidy-session was deleted")
	}
}

// TestCleanSession_TrackedAfkDirGetsCommittedBeforeRemoval covers the
// defense-in-depth path: `.afk/` is documented to be gitignored (so
// deleting the archived directory is normally invisible to git), but if a
// target repo violates that convention, CleanSession must still fold the
// resulting tracked deletion into a commit rather than let `git worktree
// remove` fail — or worse, reach for --force.
func TestCleanSession_TrackedAfkDirGetsCommittedBeforeRemoval(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "untracked-afk-session")
	statusPath := writeStatus(t, wt, "untracked-afk-session", "done")

	// Unlike the other tests, force-add and commit .afk/ into the branch
	// itself here — simulating a repo that forgot to gitignore it (-f
	// overrides the project .gitignore seeded by newTestRepo).
	runGit(t, wt, "add", "-f", "-A")
	runGit(t, wt, "commit", "-m", "chore: Track .afk for this test")

	sess := Session{
		Name:         "untracked-afk-session",
		WorktreePath: wt,
		Branch:       "untracked-afk-session",
		StatusPath:   statusPath,
		Status:       Status{Phase: "done"},
	}
	deps := testCleanDeps(t, t.TempDir())

	result, err := CleanSession(repo, sess, deps)
	if err != nil {
		t.Fatalf("CleanSession: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists after CleanSession", wt)
	}
	branches := runGit(t, repo, "branch", "--list", "untracked-afk-session")
	if !strings.Contains(branches, "untracked-afk-session") {
		t.Fatal("branch untracked-afk-session was deleted")
	}
	// Two commits are expected on top of main: the initial `.afk` tracking
	// commit, and the removal commit CleanSession made when re-dirtied.
	log := runGit(t, repo, "log", "untracked-afk-session", "--pretty=%s")
	if strings.Count(log, "chore(afk): snapshot session 'untracked-afk-session'") != 1 {
		t.Errorf("expected exactly one snapshot commit for the post-archive removal, log:\n%s", log)
	}
	if !result.SnapshotCommitted {
		t.Error("SnapshotCommitted = false, want true (the post-archive removal needed committing)")
	}
}

func TestCleanSession_ArchiveContentMatchesSource(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "verify-session")
	statusPath := writeStatus(t, wt, "verify-session", "done")

	// Add the other notepad artifacts alongside status.json.
	dir := filepath.Join(wt, ".afk", "verify-session")
	extras := map[string]string{
		"learnings.md":      "# learnings\nsomething learned\n",
		"decisions.md":      "# decisions\nsomething decided\n",
		"issues.md":         "# issues\nsomething flagged\n",
		"review-verdict.md": "Verdict: APPROVE\n",
	}
	for name, content := range extras {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	sess := Session{
		Name:         "verify-session",
		WorktreePath: wt,
		Branch:       "verify-session",
		StatusPath:   statusPath,
		Status:       Status{Phase: "done"},
	}
	deps := testCleanDeps(t, t.TempDir())

	result, err := CleanSession(repo, sess, deps)
	if err != nil {
		t.Fatalf("CleanSession: %v", err)
	}

	// The source .afk/{name}/ directory must be gone from the worktree
	// (it was archived and the worktree itself was also removed), and every
	// archived file must byte-match what was written.
	for name, content := range extras {
		got, err := os.ReadFile(filepath.Join(result.ArchivePath, name))
		if err != nil {
			t.Fatalf("reading archived %s: %v", name, err)
		}
		if string(got) != content {
			t.Errorf("archived %s = %q, want %q", name, got, content)
		}
	}
}

func TestCleanSession_NeverDeletesBranch(t *testing.T) {
	// Explicit regression guard for the hard constraint: no code path in
	// CleanSession may invoke `git branch -d/-D` or `git push --delete`.
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "guarded-session")
	statusPath := writeStatus(t, wt, "guarded-session", "failed")
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write dirty.txt: %v", err)
	}

	var sawBranchDelete bool
	deps := testCleanDeps(t, t.TempDir())
	realRun := deps.Run
	deps.Run = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "branch" {
			for _, a := range args {
				if a == "-d" || a == "-D" || a == "--delete" {
					sawBranchDelete = true
				}
			}
		}
		return realRun(dir, name, args...)
	}

	sess := Session{
		Name:         "guarded-session",
		WorktreePath: wt,
		Branch:       "guarded-session",
		StatusPath:   statusPath,
		Status:       Status{Phase: "failed"},
	}
	if _, err := CleanSession(repo, sess, deps); err != nil {
		t.Fatalf("CleanSession: %v", err)
	}
	if sawBranchDelete {
		t.Fatal("CleanSession invoked a branch-delete git command")
	}
	branches := runGit(t, repo, "branch", "--list", "guarded-session")
	if !strings.Contains(branches, "guarded-session") {
		t.Fatal("branch guarded-session no longer exists")
	}
}

// --- CleanSession host termination -----------------------------------------

// shrinkHostTimings replaces clean.go's host-termination poll/grace vars
// with millisecond values for the duration of the test, restoring the
// originals via t.Cleanup. Without this, the fallback-to-signals table
// tests would each take several real seconds (hostExitPollTimeout +
// hostSigtermGrace + hostSigkillGrace).
func shrinkHostTimings(t *testing.T) {
	t.Helper()
	origDial, origExit, origTerm, origKill, origPoll :=
		hostDialExitTimeout, hostExitPollTimeout, hostSigtermGrace, hostSigkillGrace, hostPollInterval
	hostDialExitTimeout = time.Millisecond
	hostExitPollTimeout = 10 * time.Millisecond
	hostSigtermGrace = 10 * time.Millisecond
	hostSigkillGrace = 10 * time.Millisecond
	hostPollInterval = time.Millisecond
	t.Cleanup(func() {
		hostDialExitTimeout, hostExitPollTimeout, hostSigtermGrace, hostSigkillGrace, hostPollInterval =
			origDial, origExit, origTerm, origKill, origPoll
	})
}

// writeHostFixture seeds host.json (via host.WriteMeta) plus a placeholder
// socket file and host.log alongside it, and returns their paths so tests
// can assert on removal. The socket file is a plain regular file, not a
// bound listener — terminateHost never dials it directly except through the
// injected CleanDeps.DialExit, so a real socket is unnecessary here.
func writeHostFixture(t *testing.T, worktree, name string, pid int) (socket, metaPath, logPath string) {
	t.Helper()
	socket = filepath.Join(t.TempDir(), name+".sock")
	if err := os.WriteFile(socket, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("seed placeholder socket file: %v", err)
	}
	if err := host.WriteMeta(worktree, name, host.Meta{
		PID:       pid,
		Socket:    socket,
		StartedAt: "2026-07-09T21:14:03Z",
		Argv:      []string{"claude"},
	}); err != nil {
		t.Fatalf("host.WriteMeta: %v", err)
	}
	dir := filepath.Join(worktree, ".afk", name)
	metaPath = filepath.Join(dir, "host.json")
	logPath = filepath.Join(dir, "host.log")
	if err := os.WriteFile(logPath, []byte("host log\n"), 0o644); err != nil {
		t.Fatalf("seed host.log: %v", err)
	}
	return socket, metaPath, logPath
}

func TestCleanSession_LiveHostSocketExitTerminatesBeforeRemoval(t *testing.T) {
	shrinkHostTimings(t)
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "hosted-session")
	statusPath := writeStatus(t, wt, "hosted-session", "done")
	socket, _, _ := writeHostFixture(t, wt, "hosted-session", 4242)

	var dialCalls int
	var signalsSent []syscall.Signal
	alive := true
	deps := testCleanDeps(t, t.TempDir())
	deps.Alive = func(pid int) bool {
		if pid != 4242 {
			t.Errorf("Alive called with pid %d, want 4242", pid)
		}
		return alive
	}
	deps.DialExit = func(gotSocket string, _ time.Duration) error {
		dialCalls++
		if gotSocket != socket {
			t.Errorf("DialExit socket = %q, want %q", gotSocket, socket)
		}
		alive = false // the host answers the Exit frame and exits promptly
		return nil
	}
	deps.SignalKill = func(pid int, sig syscall.Signal) error {
		signalsSent = append(signalsSent, sig)
		return nil
	}

	sess := Session{
		Name: "hosted-session", WorktreePath: wt, Branch: "hosted-session",
		StatusPath: statusPath, Status: Status{Phase: "done"},
	}
	if _, err := CleanSession(repo, sess, deps); err != nil {
		t.Fatalf("CleanSession: %v", err)
	}

	if dialCalls != 1 {
		t.Errorf("DialExit calls = %d, want 1", dialCalls)
	}
	if len(signalsSent) != 0 {
		t.Errorf("signals sent = %v, want none (socket Exit alone should have sufficed)", signalsSent)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Errorf("socket file %s still exists after CleanSession", socket)
	}
}

func TestCleanSession_LiveHostFallsBackToSignalsWhenSocketExitTimesOut(t *testing.T) {
	shrinkHostTimings(t)
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "wedged-session")
	statusPath := writeStatus(t, wt, "wedged-session", "done")
	socket, _, _ := writeHostFixture(t, wt, "wedged-session", 5151)

	var dialCalls int
	var signalsSent []syscall.Signal
	alive := true
	deps := testCleanDeps(t, t.TempDir())
	deps.Alive = func(int) bool { return alive }
	deps.DialExit = func(string, time.Duration) error {
		dialCalls++
		return nil // the host never responds to the Exit frame
	}
	deps.SignalKill = func(_ int, sig syscall.Signal) error {
		signalsSent = append(signalsSent, sig)
		if sig == syscall.SIGKILL {
			alive = false // only SIGKILL actually stops this wedged host
		}
		return nil
	}

	sess := Session{
		Name: "wedged-session", WorktreePath: wt, Branch: "wedged-session",
		StatusPath: statusPath, Status: Status{Phase: "done"},
	}
	if _, err := CleanSession(repo, sess, deps); err != nil {
		t.Fatalf("CleanSession: %v", err)
	}

	if dialCalls != 1 {
		t.Errorf("DialExit calls = %d, want 1", dialCalls)
	}
	if len(signalsSent) != 2 || signalsSent[0] != syscall.SIGTERM || signalsSent[1] != syscall.SIGKILL {
		t.Fatalf("signals sent = %v, want [SIGTERM SIGKILL] in that order", signalsSent)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Errorf("socket file %s still exists after CleanSession", socket)
	}
}

func TestCleanSession_StaleHostJsonWithDeadPidSkipsSignalsAndRemovesFiles(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "stale-host-session")
	statusPath := writeStatus(t, wt, "stale-host-session", "done")
	socket, metaPath, logPath := writeHostFixture(t, wt, "stale-host-session", 9999)

	deps := testCleanDeps(t, t.TempDir())
	deps.Alive = func(int) bool { return false }
	deps.DialExit = func(string, time.Duration) error {
		t.Fatal("DialExit must not be called for a stale host.json (dead pid)")
		return nil
	}
	deps.SignalKill = func(int, syscall.Signal) error {
		t.Fatal("SignalKill must not be called for a stale host.json (dead pid)")
		return nil
	}

	sess := Session{
		Name: "stale-host-session", WorktreePath: wt, Branch: "stale-host-session",
		StatusPath: statusPath, Status: Status{Phase: "done"},
	}
	if _, err := CleanSession(repo, sess, deps); err != nil {
		t.Fatalf("CleanSession: %v", err)
	}

	for _, p := range []string{socket, metaPath, logPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after CleanSession", p)
		}
	}
}

func TestCleanSession_NoHostJsonSkipsTerminationEntirely(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "no-host-session")
	statusPath := writeStatus(t, wt, "no-host-session", "done")

	deps := testCleanDeps(t, t.TempDir())
	deps.Alive = func(int) bool {
		t.Fatal("Alive must not be called when no host.json exists")
		return false
	}
	deps.DialExit = func(string, time.Duration) error {
		t.Fatal("DialExit must not be called when no host.json exists")
		return nil
	}
	deps.SignalKill = func(int, syscall.Signal) error {
		t.Fatal("SignalKill must not be called when no host.json exists")
		return nil
	}

	sess := Session{
		Name: "no-host-session", WorktreePath: wt, Branch: "no-host-session",
		StatusPath: statusPath, Status: Status{Phase: "done"},
	}
	result, err := CleanSession(repo, sess, deps)
	if err != nil {
		t.Fatalf("CleanSession: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists after CleanSession", wt)
	}
	if result.Branch != "no-host-session" {
		t.Errorf("Branch = %q, want %q", result.Branch, "no-host-session")
	}
}

func TestCleanSession_HostThatNeverDiesAbortsBeforeWorktreeRemoval(t *testing.T) {
	shrinkHostTimings(t)
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "immortal-session")
	statusPath := writeStatus(t, wt, "immortal-session", "done")
	writeHostFixture(t, wt, "immortal-session", 6161)

	deps := testCleanDeps(t, t.TempDir())
	deps.Alive = func(int) bool { return true } // never dies, not even after SIGKILL
	deps.DialExit = func(string, time.Duration) error { return nil }
	deps.SignalKill = func(int, syscall.Signal) error { return nil }

	sess := Session{
		Name: "immortal-session", WorktreePath: wt, Branch: "immortal-session",
		StatusPath: statusPath, Status: Status{Phase: "done"},
	}
	if _, err := CleanSession(repo, sess, deps); err == nil {
		t.Fatal("CleanSession: expected an error when the host never terminates, got nil")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree %s should still exist after an aborted CleanSession, but Stat failed: %v", wt, err)
	}
}

func TestDiscover_UnknownKeyStatusIsSurfacedAsZeroValueNotFatal(t *testing.T) {
	// status-schema.md §1: "Parsers MUST reject unknown keys as corruption
	// rather than ignore them silently." A status.json with a stray key must
	// not be silently accepted with that key dropped — but it also must not
	// abort discovery of every other session in the tree; it is surfaced as
	// a corrupt (zero-value Status) session, same as unparseable JSON.
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "corrupt-session")
	dir := filepath.Join(wt, ".afk", "corrupt-session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	corrupt := `{
  "session_id": "",
  "plan_name": "corrupt-session",
  "phase": "executing",
  "branch": "corrupt-session",
  "base_branch": "main",
  "loop_count": 0,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": "2026-07-09T21:14:03Z",
  "unexpected_field": "this violates the schema contract"
}`
	if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(corrupt), 0o644); err != nil {
		t.Fatalf("write status.json: %v", err)
	}

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.Name != "corrupt-session" {
		t.Errorf("Name = %q, want %q", got.Name, "corrupt-session")
	}
	// The unknown key must cause the Status to be treated as corrupt (zero
	// value), NOT silently parsed with the stray field dropped.
	if got.Status.Phase != "" {
		t.Errorf("Status.Phase = %q, want zero-value \"\" (unknown key must reject, not silently ignore)", got.Status.Phase)
	}
}

// --- scratch sessions -------------------------------------------------------
//
// A scratch session (`afk new --scratch`) is a `.afk/{name}/` directory
// holding a host.json with NO status.json beside it: no branch, no worktree of
// its own, no plan. Every test below exists because that shape has to survive
// two code paths that were written when a session was always status-backed —
// discovery (which keyed identity on `name == branch`) and CleanSession (whose
// last two steps remove a worktree that a scratch session does not own).

// TestDiscoverScratchClassification pins which on-disk shapes become scratch
// sessions and which do not.
//
// Load-bearing rows:
//   - "scratch dir alone": the headline case — host.json with no status.json is
//     a scratch session, KindHealthy, even though its name matches no branch;
//     it must never be filed as KindStatusWithoutWorktree, whose teardown
//     archives a directory a one-off has nothing to put in.
//   - "status and host in the same dir": afk's own execution-notepad
//     directories (e.g. `.afk/{plan}/` in this very repo) carry both files.
//     Presence of host.json must never be enough on its own, or every one of
//     them would be reclassified as a scratch session and offered for removal.
//     It is seeded in the primary checkout, so it is status-backed AND
//     carrier-mismatched: KindStatusWithoutWorktree with an empty Branch,
//     never scratch.
//   - "matched session with a live host": the same guard from the other
//     direction — a normal session whose host is running keeps its existing
//     classification exactly.
func TestDiscoverScratchClassification(t *testing.T) {
	type wantSession struct {
		name    string
		branch  string
		scratch bool
		kind    Kind
	}
	cases := []struct {
		name         string
		seed         func(t *testing.T, repo string)
		wantSessions []wantSession
	}{
		{
			name: "scratch dir alone",
			seed: func(t *testing.T, repo string) {
				writeHostFixture(t, repo, "scratch-1", 4242)
			},
			wantSessions: []wantSession{{name: "scratch-1", branch: "", scratch: true}},
		},
		{
			name: "scratch alongside a normal session",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "good-session")
				writeStatus(t, wt, "good-session", "done")
				writeHostFixture(t, repo, "scratch-1", 4242)
			},
			wantSessions: []wantSession{
				{name: "good-session", branch: "good-session", scratch: false},
				{name: "scratch-1", branch: "", scratch: true},
			},
		},
		{
			name: "two scratch dirs",
			seed: func(t *testing.T, repo string) {
				writeHostFixture(t, repo, "scratch-1", 4242)
				writeHostFixture(t, repo, "scratch-2", 4243)
			},
			wantSessions: []wantSession{
				{name: "scratch-1", branch: "", scratch: true},
				{name: "scratch-2", branch: "", scratch: true},
			},
		},
		{
			name: "status and host in the same dir",
			seed: func(t *testing.T, repo string) {
				writeStatus(t, repo, "notepad-dir", "done")
				writeHostFixture(t, repo, "notepad-dir", 4242)
			},
			wantSessions: []wantSession{
				{name: "notepad-dir", branch: "", scratch: false, kind: KindStatusWithoutWorktree},
			},
		},
		{
			name: "matched session with a live host",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "hosted-session")
				writeStatus(t, wt, "hosted-session", "executing")
				writeHostFixture(t, wt, "hosted-session", 4242)
			},
			wantSessions: []wantSession{{name: "hosted-session", branch: "hosted-session", scratch: false}},
		},
		{
			name: "directory with neither file",
			seed: func(t *testing.T, repo string) {
				if err := os.MkdirAll(filepath.Join(repo, ".afk", "plans"), 0o755); err != nil {
					t.Fatalf("mkdir .afk/plans: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			tc.seed(t, repo)

			sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(sessions) != len(tc.wantSessions) {
				t.Fatalf("got %d sessions, want %d: %+v", len(sessions), len(tc.wantSessions), sessions)
			}
			for i, want := range tc.wantSessions {
				got := sessions[i]
				if got.Name != want.name || got.Branch != want.branch || got.Scratch != want.scratch || got.Kind != want.kind {
					t.Errorf("session[%d] = {Name:%q Branch:%q Scratch:%v Kind:%q}, want {Name:%q Branch:%q Scratch:%v Kind:%q}",
						i, got.Name, got.Branch, got.Scratch, got.Kind, want.name, want.branch, want.scratch, want.kind)
				}
			}
		})
	}
}

// TestDiscover_ScratchSessionShape pins the fields CleanSession then depends
// on: WorktreePath is the worktree the directory was found in (host.ReadMeta
// and host.SocketPath both key on it), while Branch and StatusPath stay empty
// because there is nothing on disk for them to point at.
func TestDiscover_ScratchSessionShape(t *testing.T) {
	repo := newTestRepo(t)
	writeHostFixture(t, repo, "scratch-1", 4242)

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.WorktreePath != repo {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, repo)
	}
	if got.StatusPath != "" {
		t.Errorf("StatusPath = %q, want \"\"", got.StatusPath)
	}
	if got.Status.Phase != "" {
		t.Errorf("Status.Phase = %q, want \"\" (a scratch session has no status.json)", got.Status.Phase)
	}
}

// TestCleanSession_ScratchNeverRunsAnyGitCommand is the single most important
// row in this file: the scratch path must never reach `git worktree remove`.
// A scratch session's "worktree" IS the user's repo root, so removing it would
// delete the checkout. The assertion is on the injected Run dep rather than on
// the filesystem, because a filesystem check could only observe the damage
// after the fact — and only in a fixture where the command happened to
// succeed.
func TestCleanSession_ScratchNeverRunsAnyGitCommand(t *testing.T) {
	repo := newTestRepo(t)
	writeHostFixture(t, repo, "scratch-1", 9999)
	dir := filepath.Join(repo, ".afk", "scratch-1")

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 || !sessions[0].Scratch {
		t.Fatalf("expected one scratch session, got %+v", sessions)
	}

	var runCalls [][]string
	deps := testCleanDeps(t, t.TempDir())
	deps.Run = func(_, name string, args ...string) (string, error) {
		runCalls = append(runCalls, append([]string{name}, args...))
		return "", nil
	}
	deps.Alive = func(int) bool { return false }

	result, err := CleanSession(repo, sessions[0], deps)
	if err != nil {
		t.Fatalf("CleanSession: %v", err)
	}

	// Named-command scan first, count second, and the order matters: the scan
	// exists to give the two catastrophic calls their own failure message, and
	// a scan placed after the zero-count assertion below only ever runs over a
	// slice that assertion has already condemned. Scanning first means a
	// regression that reaches `git worktree remove` reports THAT, instead of a
	// generic "ran 1 command".
	for _, call := range runCalls {
		if len(call) >= 3 && call[0] == "git" && call[1] == "worktree" && call[2] == "remove" {
			t.Fatal("CleanSession invoked `git worktree remove` for a scratch session")
		}
		if len(call) >= 2 && call[0] == "git" && call[1] == "branch" {
			t.Fatal("CleanSession invoked a `git branch` command for a scratch session")
		}
	}
	// Zero, not "no worktree remove": the scratch path shells out for nothing
	// at all, and pinning the count keeps a future git call from arriving
	// unnoticed next to the one that must never happen.
	if len(runCalls) != 0 {
		t.Errorf("CleanSession ran %d commands for a scratch session, want 0: %v", len(runCalls), runCalls)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s still exists after CleanSession (err=%v)", dir, err)
	}
	// The repo root — the carrier "worktree" — and its contents must be intact.
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Fatalf("repo root was damaged by cleaning a scratch session: %v", err)
	}
	if result.WorktreePath != "" {
		t.Errorf("CleanResult.WorktreePath = %q, want \"\" (nothing was removed)", result.WorktreePath)
	}
	if result.Branch != "" {
		t.Errorf("CleanResult.Branch = %q, want \"\" (a scratch session owns no branch)", result.Branch)
	}
	if result.Name != "scratch-1" {
		t.Errorf("CleanResult.Name = %q, want %q", result.Name, "scratch-1")
	}
}

// TestCleanSession_NormalSessionStillRemovesWorktree is the other half of the
// guard above: the status-backed path must be untouched by the scratch branch,
// so it still issues exactly one `git worktree remove` for the session's own
// worktree.
func TestCleanSession_NormalSessionStillRemovesWorktree(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "normal-session")
	statusPath := writeStatus(t, wt, "normal-session", "done")

	var removeCalls []string
	deps := testCleanDeps(t, t.TempDir())
	realRun := deps.Run
	deps.Run = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 3 && args[0] == "worktree" && args[1] == "remove" {
			removeCalls = append(removeCalls, args[2])
		}
		return realRun(dir, name, args...)
	}

	sess := Session{
		Name: "normal-session", WorktreePath: wt, Branch: "normal-session",
		StatusPath: statusPath, Status: Status{Phase: "done"},
	}
	if _, err := CleanSession(repo, sess, deps); err != nil {
		t.Fatalf("CleanSession: %v", err)
	}
	if len(removeCalls) != 1 || removeCalls[0] != wt {
		t.Fatalf("`git worktree remove` calls = %v, want exactly [%s]", removeCalls, wt)
	}
}

// TestCleanSaveError pins CleanDeps.BeforeClean (PLAN T17.13): it is called
// once, with the session, while the session's files are all still in place,
// and whatever it returns the clean still runs to the end.
//
// The "hook fails" row is the load-bearing one: a save of a failed session's
// screen that errors must never stop the session being cleaned, only add a
// note. The "no hook" row is the promise that a caller who sets nothing gets
// the clean it always got, with no note. "hook succeeds" keeps a passing
// hook from adding a note, which would read as a problem to the owner.
func TestCleanSaveError(t *testing.T) {
	tests := []struct {
		name     string
		hook     bool
		hookErr  error
		wantNote string
	}{
		{name: "no hook"},
		{name: "hook succeeds", hook: true},
		{name: "hook fails", hook: true, hookErr: errors.New("the Rex block is gone"), wantNote: "before cleaning failed-session: the Rex block is gone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepo(t)
			wt := addWorktree(t, repo, "failed-session")
			statusPath := writeStatus(t, wt, "failed-session", "failed")
			deps := testCleanDeps(t, t.TempDir())

			var calls []Session
			var filesThere bool
			if tt.hook {
				deps.BeforeClean = func(s Session) error {
					calls = append(calls, s)
					_, err := os.Stat(statusPath)
					filesThere = err == nil
					return tt.hookErr
				}
			}
			sess := Session{
				Name: "failed-session", WorktreePath: wt, Branch: "failed-session",
				StatusPath: statusPath, Status: Status{Phase: "failed"},
			}
			res, err := CleanSessionWithOptions(repo, sess, deps, CleanOptions{})
			if err != nil {
				t.Fatalf("CleanSessionWithOptions: %v (the clean must not fail on the hook)", err)
			}
			if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
				t.Fatalf("worktree %s still there after the clean (stat err %v)", wt, statErr)
			}
			if tt.hook {
				if len(calls) != 1 || calls[0].Name != "failed-session" || calls[0].WorktreePath != wt {
					t.Fatalf("BeforeClean calls = %+v, want one with the session", calls)
				}
				if !filesThere {
					t.Fatalf("BeforeClean ran after the session's files were removed; it must run first")
				}
			}
			if res.Notes != tt.wantNote {
				t.Fatalf("Notes = %q, want %q", res.Notes, tt.wantNote)
			}
		})
	}
}

// TestCleanSession_ScratchLiveHostStopLadderRunsBeforeRemoval pins the
// ordering: the host owns host.json, host.log and the socket inside the very
// directory being removed, so removing the directory first would race a live
// writer (the same ordering the create rollback depends on — see learnings.md).
func TestCleanSession_ScratchLiveHostStopLadderRunsBeforeRemoval(t *testing.T) {
	shrinkHostTimings(t)
	repo := newTestRepo(t)
	socket, _, _ := writeHostFixture(t, repo, "scratch-1", 4242)
	dir := filepath.Join(repo, ".afk", "scratch-1")

	var dialCalls int
	var signalsSent []syscall.Signal
	alive := true
	deps := testCleanDeps(t, t.TempDir())
	deps.Run = func(_, name string, args ...string) (string, error) {
		t.Errorf("Run must not be called for a scratch session, got %s %v", name, args)
		return "", nil
	}
	deps.Alive = func(pid int) bool {
		if pid != 4242 {
			t.Errorf("Alive called with pid %d, want 4242", pid)
		}
		return alive
	}
	deps.DialExit = func(gotSocket string, _ time.Duration) error {
		dialCalls++
		if gotSocket != socket {
			t.Errorf("DialExit socket = %q, want %q", gotSocket, socket)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s was already removed when the host was asked to exit: %v", dir, err)
		}
		alive = false // the host answers the Exit frame and exits promptly
		return nil
	}
	deps.SignalKill = func(_ int, sig syscall.Signal) error {
		signalsSent = append(signalsSent, sig)
		return nil
	}

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, err := CleanSession(repo, sessions[0], deps); err != nil {
		t.Fatalf("CleanSession: %v", err)
	}

	if dialCalls != 1 {
		t.Errorf("DialExit calls = %d, want 1", dialCalls)
	}
	if len(signalsSent) != 0 {
		t.Errorf("signals sent = %v, want none (socket Exit alone should have sufficed)", signalsSent)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Errorf("socket file %s still exists after CleanSession", socket)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s still exists after CleanSession", dir)
	}
}

// TestCleanSession_ScratchHostThatNeverDiesAbortsBeforeRemoval mirrors
// TestCleanSession_HostThatNeverDiesAbortsBeforeWorktreeRemoval for scratch:
// the directory is what the removal step destroys here, so a host that
// survives SIGKILL must leave it on disk rather than have its files deleted
// out from under it.
func TestCleanSession_ScratchHostThatNeverDiesAbortsBeforeRemoval(t *testing.T) {
	shrinkHostTimings(t)
	repo := newTestRepo(t)
	writeHostFixture(t, repo, "scratch-1", 6161)
	dir := filepath.Join(repo, ".afk", "scratch-1")

	var signalsSent []syscall.Signal
	deps := testCleanDeps(t, t.TempDir())
	deps.Alive = func(int) bool { return true } // never dies, not even after SIGKILL
	deps.DialExit = func(string, time.Duration) error { return nil }
	deps.SignalKill = func(_ int, sig syscall.Signal) error {
		signalsSent = append(signalsSent, sig)
		return nil
	}

	sess := Session{Name: "scratch-1", WorktreePath: repo, Scratch: true}
	if _, err := CleanSession(repo, sess, deps); err == nil {
		t.Fatal("CleanSession: expected an error when the host never terminates, got nil")
	}
	if len(signalsSent) != 2 || signalsSent[0] != syscall.SIGTERM || signalsSent[1] != syscall.SIGKILL {
		t.Errorf("signals sent = %v, want [SIGTERM SIGKILL] in that order", signalsSent)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("%s should still exist after an aborted CleanSession: %v", dir, err)
	}
}

// TestCleanSession_ScratchRefusesAnUnsafeName guards the one line in this file
// that can destroy data outside the session: `.afk/{name}/` is removed with
// os.RemoveAll, and an empty name would resolve to `.afk` itself — every
// session directory in the repo, including status-backed ones. Discover can
// only ever produce a filepath.Base'd name, so this is defense against a
// hand-constructed Session reaching CleanSession from a future caller.
func TestCleanSession_ScratchRefusesAnUnsafeName(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../elsewhere", "nested/name"} {
		t.Run("name="+name, func(t *testing.T) {
			repo := newTestRepo(t)
			writeStatus(t, repo, "bystander", "done")
			deps := testCleanDeps(t, t.TempDir())
			deps.Run = func(_, cmd string, args ...string) (string, error) {
				t.Errorf("Run must not be called, got %s %v", cmd, args)
				return "", nil
			}

			sess := Session{Name: name, WorktreePath: repo, Scratch: true}
			if _, err := CleanSession(repo, sess, deps); err == nil {
				t.Fatalf("CleanSession(name=%q): expected an error, got nil", name)
			}
			if _, err := os.Stat(filepath.Join(repo, ".afk", "bystander")); err != nil {
				t.Fatalf("an unrelated session directory was removed: %v", err)
			}
		})
	}
}

// TestCleanableWithoutForce pins the phase-gate decision in one place. Load-
// bearing rows: every scratch row is true regardless of phase (a scratch
// session has no phase to be terminal, and refusing it would make --force the
// only way to ever remove one), and the four status-backed rows reproduce the
// CLI's existing done/failed gate unchanged.
func TestCleanableWithoutForce(t *testing.T) {
	cases := []struct {
		name string
		sess Session
		want bool
	}{
		{"done", Session{Status: Status{Phase: "done"}}, true},
		{"failed", Session{Status: Status{Phase: "failed"}}, true},
		{"executing", Session{Status: Status{Phase: "executing"}}, false},
		{"stale (empty phase)", Session{}, false},
		{"scratch", Session{Scratch: true}, true},
		{"scratch with a stray phase", Session{Scratch: true, Status: Status{Phase: "executing"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanableWithoutForce(tc.sess); got != tc.want {
				t.Errorf("CleanableWithoutForce(%+v) = %v, want %v", tc.sess, got, tc.want)
			}
		})
	}
}

// --- TOCTOU + ordering regressions ---------------------------------------

// TestSessionsFromDirs_KindComesFromTheObservationNotALiveStat is the
// regression for the review's TOCTOU finding, re-anchored onto the shared
// observation. The scratch/status-backed decision must be made from the one
// glob pair internal/worktree already took — HasStatusFile — and never from a
// second, later look at the filesystem.
//
// Every path in this table is synthetic: it points inside a temp dir where
// NOTHING was created. That is the whole point. A `.afk/{name}/` that the
// status glob already reported must stay status-backed even when status.json
// is no longer on disk by the time the host-only directories are classified —
// the exact interleaving a conductor writing (or a `afk clean` removing)
// status.json between the two calls produces. An implementation that re-stats
// the sibling status.json instead answers "no status file here" and emits the
// SAME directory a second time as a scratch session; two Sessions with one
// name is what makes a first-match-by-name lookup pick a kind at random, and
// the wrong pick routes a clean into `git worktree remove`.
//
// Load-bearing rows: "status and host, nothing on disk" (the race itself),
// "host only" (the feature — scratch is still discovered, and it is
// KindHealthy: a one-off is not a fault), "corrupt status file" (still
// status-backed, now named KindStaleStatus), and "unreadable status file" (an
// unreadable file is still a hard error, not a silent downgrade to scratch,
// which would offer a real session's directory for removal by the path that
// skips archiving).
func TestSessionsFromDirs_KindComesFromTheObservationNotALiveStat(t *testing.T) {
	root := t.TempDir()
	dir := func(name string) string { return filepath.Join(root, ".afk", name) }
	statusPath := func(name string) string { return filepath.Join(dir(name), "status.json") }

	validStatus := Status{
		SessionID:  "11111111-1111-1111-1111-111111111111",
		PlanName:   "sess",
		Phase:      "done",
		Branch:     "sess",
		BaseBranch: "main",
		UpdatedAt:  "2026-07-09T21:14:03Z",
	}
	// state.Status is what internal/worktree decodes into; the nine fields are
	// identical and only Phase's named type differs.
	validObserved := state.Status{
		SessionID:  validStatus.SessionID,
		PlanName:   validStatus.PlanName,
		Phase:      state.Phase(validStatus.Phase),
		Branch:     validStatus.Branch,
		BaseBranch: validStatus.BaseBranch,
		UpdatedAt:  validStatus.UpdatedAt,
	}

	statusBacked := func(name string, st state.Status, serr error) worktree.AfkDir {
		return worktree.AfkDir{
			Name:            name,
			Path:            dir(name),
			StatusPath:      statusPath(name),
			HasStatusFile:   true,
			StatusStrict:    st,
			StatusStrictErr: serr,
		}
	}
	hostOnly := func(name string) worktree.AfkDir {
		return worktree.AfkDir{
			Name:            name,
			Path:            dir(name),
			HostPath:        filepath.Join(dir(name), "host.json"),
			HasHostFile:     true,
			StatusStrictErr: fmt.Errorf("%s: %w", dir(name), worktree.ErrStatusAbsent),
		}
	}
	withHost := func(fd worktree.AfkDir) worktree.AfkDir {
		fd.HasHostFile = true
		fd.HostPath = filepath.Join(fd.Path, "host.json")
		return fd
	}

	type wantSession struct {
		name    string
		scratch bool
		kind    Kind
	}
	cases := []struct {
		name    string
		dirs    []worktree.AfkDir
		want    []wantSession
		wantErr bool
	}{
		{
			name: "status and host, nothing on disk",
			dirs: []worktree.AfkDir{withHost(statusBacked("sess", validObserved, nil))},
			want: []wantSession{{"sess", false, KindHealthy}},
		},
		{
			name: "host only",
			dirs: []worktree.AfkDir{hostOnly("scratch-1")},
			want: []wantSession{{"scratch-1", true, KindHealthy}},
		},
		{
			name: "status only",
			dirs: []worktree.AfkDir{statusBacked("sess", validObserved, nil)},
			want: []wantSession{{"sess", false, KindHealthy}},
		},
		{
			name: "corrupt status file is still status-backed",
			dirs: []worktree.AfkDir{
				withHost(statusBacked("sess", state.Status{}, errors.New("decoding: invalid character 'n'"))),
			},
			want: []wantSession{{"sess", false, KindStaleStatus}},
		},
		{
			name: "unreadable status file",
			dirs: []worktree.AfkDir{
				withHost(statusBacked("sess", state.Status{},
					fmt.Errorf("%w: reading %s: %w", worktree.ErrStatusUnreadable, statusPath("sess"), os.ErrPermission))),
			},
			wantErr: true,
		},
		{
			name: "one scratch dir beside one status-backed dir",
			dirs: []worktree.AfkDir{
				withHost(statusBacked("sess", validObserved, nil)),
				hostOnly("scratch-1"),
			},
			want: []wantSession{{"sess", false, KindHealthy}, {"scratch-1", true, KindHealthy}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sessionsFromDirs(tc.dirs)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("sessionsFromDirs = %+v, nil — want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("sessionsFromDirs: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d sessions, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].Name != want.name || got[i].Scratch != want.scratch || got[i].Kind != want.kind {
					t.Errorf("session[%d] = {Name:%q Scratch:%v Kind:%q}, want {Name:%q Scratch:%v Kind:%q}",
						i, got[i].Name, got[i].Scratch, got[i].Kind, want.name, want.scratch, want.kind)
				}
			}
		})
	}
}

// permuteSessions returns every ordering of in, each a fresh slice.
func permuteSessions(in []Session) [][]Session {
	if len(in) <= 1 {
		return [][]Session{append([]Session(nil), in...)}
	}
	var out [][]Session
	for i := range in {
		rest := make([]Session, 0, len(in)-1)
		rest = append(rest, in[:i]...)
		rest = append(rest, in[i+1:]...)
		for _, tail := range permuteSessions(rest) {
			out = append(out, append([]Session{in[i]}, tail...))
		}
	}
	return out
}

// TestSortSessions_TotalOrderIsIndependentOfInputOrder pins Discover's output
// order as a total order rather than "sorted by name".
//
// Two Sessions can legitimately share a name — a `.afk/dup/` scratch
// directory in the main checkout and a worktree checked out on branch `dup`
// with its own `.afk/dup/status.json` are both real, and Discover reports
// both. Callers then take the FIRST match by name (cmdClean's findCleanTarget,
// internal/ui's resolveCleanTarget), so with only Name in the comparator,
// sort.Slice — which is explicitly not stable — leaves which KIND of session
// answers `afk clean dup` decided by the order the worktrees happened to be
// scanned in. This asserts one fixed answer for every input permutation.
//
// The load-bearing rows are the three-way `dup` collision: a scratch `dup`, a
// healthy `dup` in its own worktree, and a STRANDED `dup` — a `.afk/dup/`
// carried by the main checkout, which is a KindStatusWithoutWorktree Session
// and therefore a third distinct identity under one name. All three are
// legitimate simultaneously, the comparator must leave them in one fixed
// order for every input permutation, and no two of them may ever compare
// equal: their teardowns are `git worktree remove`, "remove a directory", and
// "remove a directory in the user's checkout" respectively.
func TestSortSessions_TotalOrderIsIndependentOfInputOrder(t *testing.T) {
	fixture := []Session{
		{Name: "dup", Scratch: true, WorktreePath: "/repo"},
		{Name: "dup", WorktreePath: "/repo/wt-dup", Branch: "dup", StatusPath: "/repo/wt-dup/.afk/dup/status.json"},
		{Name: "dup", WorktreePath: "/repo", Kind: KindStatusWithoutWorktree, StatusPath: "/repo/.afk/dup/status.json"},
		{Name: "alpha", WorktreePath: "/repo/wt-alpha", Branch: "alpha"},
		{Name: "zeta", Scratch: true, WorktreePath: "/repo"},
	}
	type key struct {
		name    string
		scratch bool
		wt      string
		kind    Kind
	}
	want := []key{
		{"alpha", false, "/repo/wt-alpha", KindHealthy},
		{"dup", false, "/repo", KindStatusWithoutWorktree},
		{"dup", false, "/repo/wt-dup", KindHealthy},
		{"dup", true, "/repo", KindHealthy},
		{"zeta", true, "/repo", KindHealthy},
	}

	for i, perm := range permuteSessions(fixture) {
		sortSessions(perm)
		for j, w := range want {
			got := key{perm[j].Name, perm[j].Scratch, perm[j].WorktreePath, perm[j].Kind}
			if got != w {
				t.Fatalf("permutation %d: sorted[%d] = %+v, want %+v (full: %+v)", i, j, got, w, perm)
			}
		}
	}
}

// TestDiscover_NameCollisionResolvesToTheStatusBackedSession is the
// end-to-end half of the ordering guarantee, on a real repo: a scratch
// `.afk/dup/` in the main checkout and a worktree on branch `dup` carrying
// `.afk/dup/status.json`. Both are discovered, and the first match by name
// must always be the status-backed one — the main checkout is scanned first,
// so a name-only sort would let discovery order decide.
func TestDiscover_NameCollisionResolvesToTheStatusBackedSession(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "dup")
	writeStatus(t, wt, "dup", "done")
	writeHostFixture(t, repo, "dup", 4242)

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(sessions), sessions)
	}
	if sessions[0].Scratch || sessions[0].WorktreePath != wt {
		t.Errorf("sessions[0] = {Scratch:%v WorktreePath:%q}, want the status-backed session at %q",
			sessions[0].Scratch, sessions[0].WorktreePath, wt)
	}
	if !sessions[1].Scratch || sessions[1].WorktreePath != repo {
		t.Errorf("sessions[1] = {Scratch:%v WorktreePath:%q}, want the scratch session at %q",
			sessions[1].Scratch, sessions[1].WorktreePath, repo)
	}
}

// TestCleanSession_ScratchRecordsZeroGitCallsUnderEveryOrdering is the
// invariant the ordering and TOCTOU fixes exist to protect, asserted where it
// cannot be argued around: on the injected CleanDeps.Run.
//
// The fixture is deliberately the ambiguous one — a scratch `.afk/dup/` in
// the main checkout whose name also belongs to a real worktree-backed session
// — and the whole discovered slice is re-shuffled into every possible order
// before the by-name lookup runs. However the shuffle falls out, cleaning a
// session that came back Scratch must shell out zero times: `git worktree
// remove` pointed at a scratch session's WorktreePath is the user's own
// checkout.
func TestCleanSession_ScratchRecordsZeroGitCallsUnderEveryOrdering(t *testing.T) {
	firstByName := func(sessions []Session, name string) *Session {
		for i := range sessions {
			if sessions[i].Name == name {
				return &sessions[i]
			}
		}
		return nil
	}

	// Three discovered sessions -> six orderings. Each runs as a subtest so it
	// gets its own t.TempDir namespace: addWorktree derives the worktree path
	// from the repo's PARENT, so two fixtures sharing a temp root would
	// collide on `git worktree add`.
	for perm := 0; perm < 6; perm++ {
		t.Run(fmt.Sprintf("ordering %d", perm), func(t *testing.T) {
			repo := newTestRepo(t)
			wt := addWorktree(t, repo, "dup")
			writeStatus(t, wt, "dup", "done")
			writeHostFixture(t, repo, "dup", 9999)
			writeHostFixture(t, repo, "scratch-1", 9998)

			sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			orderings := permuteSessions(sessions)
			shuffled := orderings[perm%len(orderings)]
			sortSessions(shuffled)

			target := firstByName(shuffled, "scratch-1")
			if target == nil {
				t.Fatalf("no session named scratch-1 in %+v", shuffled)
			}
			if !target.Scratch {
				t.Fatalf("scratch-1 resolved to a non-scratch session: %+v", *target)
			}

			var runCalls [][]string
			deps := testCleanDeps(t, t.TempDir())
			deps.Run = func(_, name string, args ...string) (string, error) {
				runCalls = append(runCalls, append([]string{name}, args...))
				return "", nil
			}
			deps.Alive = func(int) bool { return false }

			if _, err := CleanSession(repo, *target, deps); err != nil {
				t.Fatalf("CleanSession: %v", err)
			}
			if len(runCalls) != 0 {
				t.Fatalf("CleanSession ran %d commands for a scratch session, want 0: %v",
					len(runCalls), runCalls)
			}
			if _, err := os.Stat(filepath.Join(repo, ".afk", "scratch-1")); !os.IsNotExist(err) {
				t.Errorf(".afk/scratch-1 still exists (err=%v)", err)
			}
			if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
				t.Fatalf("repo root was damaged: %v", err)
			}
		})
	}
}

// TestCleanScratchSessionDocDoesNotClaimSocketInAfkDir is the sweep this
// fix cycle's W4.T2 instruction asked for: cleanScratchSession's doc comment
// claimed "the directory holds host.json, host.log and a socket", the same
// false shape README.md:208/:347 made about the socket living inside
// `.afk/{name}/`. It actually lives under os.TempDir()/afk-{uid}/ (see
// host.SocketPath), entirely outside .afk/{name}/. This parses the comment
// straight off the source so a reintroduction of the false claim fails it.
func TestCleanScratchSessionDocDoesNotClaimSocketInAfkDir(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	srcPath := filepath.Join(filepath.Dir(thisFile), "clean.go")
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("reading %s: %v", srcPath, err)
	}
	src := string(data)

	if strings.Contains(src, "the directory holds host.json, host.log and\n// a socket") {
		t.Errorf("%s still claims the socket lives inside .afk/{name}/ alongside host.json/host.log", srcPath)
	}
	if !strings.Contains(src, "host.SocketPath") {
		t.Errorf("%s does not point at host.SocketPath for where the socket actually lives", srcPath)
	}
}

// --- fault kinds ------------------------------------------------------------

// writeRawStatusFile writes literal contents to name's status.json inside
// worktree, for the corrupt-file rows the schema-valid writeStatus cannot
// produce.
func writeRawStatusFile(t *testing.T, worktree, name, contents string) string {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "status.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write status.json: %v", err)
	}
	return path
}

// writeRawHostFile writes literal contents to name's host.json inside
// worktree, for the corrupt-host.json row.
func writeRawHostFile(t *testing.T, worktree, name, contents string) {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write host.json: %v", err)
	}
}

// TestWorktreeFaultKind pins the pure half of the worktree-side split. It is
// the whole of W2.T2's routing decision reduced to one boolean, because taking
// the wrong branch there runs `git status` and `git add -A` inside a directory
// that no longer exists — which resolves to the caller's own cwd.
func TestWorktreeFaultKind(t *testing.T) {
	cases := []struct {
		name      string
		dirOnDisk bool
		want      Kind
	}{
		{"directory still there", true, KindWorktreeNoSession},
		{"directory deleted out from under git", false, KindWorktreeMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := worktreeFaultKind(tc.dirOnDisk); got != tc.want {
				t.Errorf("worktreeFaultKind(%v) = %q, want %q", tc.dirOnDisk, got, tc.want)
			}
		})
	}
}

// TestKindIdentityRelevant pins the ONE discriminator every by-name clean
// resolution shares (internal/ui's cleanTargetKind, the CLI's
// findCleanTarget) against the narrower WorktreeFault, which keeps meaning
// "this row is inert: no pane, no preview, no attach".
//
// Load-bearing rows:
//   - KindStatusWithoutWorktree is identity-relevant but NOT a worktree
//     fault. Collapse the two and either the dashboard makes a stranded row
//     inert (no pane on a live standalone session) or a stranded `X` and a
//     healthy `X` resolve to each other's teardown — one of which runs git in
//     the user's checkout.
//   - KindStaleStatus is neither. Make it identity-relevant and every
//     malformed-JSON row starts failing to clean with `no session named %q
//     found`, the exact case internal/ui's dashboardPhase exists to keep
//     reachable.
func TestKindIdentityRelevant(t *testing.T) {
	cases := []struct {
		kind             Kind
		wantIdentity     bool
		wantWorktreeFlaw bool
	}{
		{KindHealthy, false, false},
		{KindStaleStatus, false, false},
		{KindStatusWithoutWorktree, true, false},
		{KindWorktreeMissing, true, true},
		{KindWorktreeNoSession, true, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := tc.kind.IdentityRelevant(); got != tc.wantIdentity {
				t.Errorf("Kind(%q).IdentityRelevant() = %v, want %v", tc.kind, got, tc.wantIdentity)
			}
			if got := tc.kind.WorktreeFault(); got != tc.wantWorktreeFlaw {
				t.Errorf("Kind(%q).WorktreeFault() = %v, want %v", tc.kind, got, tc.wantWorktreeFlaw)
			}
		})
	}
}

// TestDiscoverFaultKinds pins the four-kind vocabulary against the on-disk
// shapes that produce it, and pins the exemptions that keep it quiet.
//
// Load-bearing rows:
//   - "healthy session in its own worktree" — the matchedOwnBranch
//     regression. Drop that fourth clause from the worktree-without-status
//     condition and EVERY healthy non-primary session emits a spurious fault
//     entry of itself alongside its own Session.
//   - "corrupt status.json in its own worktree" — KindStaleStatus, and still
//     an ordinary Session under an ordinary name. staleStatus is deliberately
//     NOT identity-relevant; make it so and every malformed-JSON row starts
//     failing to clean with `no session named %q found`.
//   - "scratch dir with a corrupt host.json" — scratch is evaluated first and
//     wins over every fault, an unparseable host.json included.
//   - "scratch dir named after the carrier branch" — a scratch directory must
//     NOT satisfy matchedOwnBranch (clean.go's own comment says why), or it
//     silently suppresses a real worktree-without-status fault entry.
//   - "standalone session in the primary checkout" and "corrupt standalone
//     session" — both are KindStatusWithoutWorktree, the second one proving
//     the kind OVERRIDES KindStaleStatus: routing on the carrier mismatch is
//     what keeps git away from the user's checkout, so it must survive a
//     second fault on the same directory. internal/discover labels these rows
//     with the same kind and renders them as ordinary rows; the two packages
//     agree on identity and differ only in policy.
//   - "worktree on another branch carrying a foreign session dir" — one
//     directory, two entries with two teardowns: the stranded session and the
//     carrier's own worktree fault. Pinned because cleaning the fault first
//     takes the stranded directory with it (see the plan's Context).
//   - "worktree directory deleted" vs "worktree with no session" — split only
//     on DirOnDisk, because each needs a teardown and they need opposite ones.
func TestDiscoverFaultKinds(t *testing.T) {
	type wantSession struct {
		name    string
		kind    Kind
		scratch bool
	}
	cases := []struct {
		name         string
		seed         func(t *testing.T, repo string)
		wantSessions []wantSession
	}{
		{
			name: "healthy session in its own worktree",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "feat")
				writeStatus(t, wt, "feat", "done")
			},
			wantSessions: []wantSession{{"feat", KindHealthy, false}},
		},
		{
			name: "corrupt status.json in its own worktree",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "feat")
				writeRawStatusFile(t, wt, "feat", "{not json")
			},
			wantSessions: []wantSession{{"feat", KindStaleStatus, false}},
		},
		{
			name: "primary worktree with no session",
			seed: func(t *testing.T, repo string) {},
		},
		{
			name: "detached worktree",
			seed: func(t *testing.T, repo string) {
				runGit(t, repo, "worktree", "add", "--detach",
					filepath.Join(filepath.Dir(repo), "wt-detached"), "main")
			},
		},
		{
			name: "standalone session in the primary checkout",
			seed: func(t *testing.T, repo string) {
				writeStatus(t, repo, "solo", "done")
			},
			wantSessions: []wantSession{{"solo", KindStatusWithoutWorktree, false}},
		},
		{
			name: "corrupt standalone session in the primary checkout",
			seed: func(t *testing.T, repo string) {
				writeRawStatusFile(t, repo, "solo", "{not json")
			},
			wantSessions: []wantSession{{"solo", KindStatusWithoutWorktree, false}},
		},
		{
			name: "worktree on another branch carrying a foreign session dir",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "feat")
				writeStatus(t, wt, "other", "done")
			},
			wantSessions: []wantSession{
				{"feat", KindWorktreeNoSession, false},
				{"other", KindStatusWithoutWorktree, false},
			},
		},
		{
			name: "worktree with no session",
			seed: func(t *testing.T, repo string) {
				addWorktree(t, repo, "stray")
			},
			wantSessions: []wantSession{{"stray", KindWorktreeNoSession, false}},
		},
		{
			name: "worktree directory deleted out from under git",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "gone")
				writeStatus(t, wt, "gone", "done")
				if err := os.RemoveAll(wt); err != nil {
					t.Fatalf("removing worktree directory: %v", err)
				}
			},
			wantSessions: []wantSession{{"gone", KindWorktreeMissing, false}},
		},
		{
			name: "scratch dir with a corrupt host.json",
			seed: func(t *testing.T, repo string) {
				writeRawHostFile(t, repo, "scratch-1", "{")
			},
			wantSessions: []wantSession{{"scratch-1", KindHealthy, true}},
		},
		{
			name: "scratch dir named after the carrier branch",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "feat")
				writeHostFixture(t, wt, "feat", 4242)
			},
			wantSessions: []wantSession{
				{"feat", KindWorktreeNoSession, false},
				{"feat", KindHealthy, true},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			tc.seed(t, repo)

			sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(sessions) != len(tc.wantSessions) {
				t.Fatalf("got %d sessions, want %d: %+v", len(sessions), len(tc.wantSessions), sessions)
			}
			for i, want := range tc.wantSessions {
				got := sessions[i]
				if got.Name != want.name || got.Kind != want.kind || got.Scratch != want.scratch {
					t.Errorf("session[%d] = {Name:%q Kind:%q Scratch:%v}, want {Name:%q Kind:%q Scratch:%v}",
						i, got.Name, got.Kind, got.Scratch, want.name, want.kind, want.scratch)
				}
			}
		})
	}
}

// --- (W2.T2) fault routing ------------------------------------------------

// gitCall is one recorded CleanDeps.Run invocation. The dir is recorded
// alongside the argv because it is half of what these tests exist to pin: a
// git command with an empty dir runs in the CALLER's working directory (see
// defaultRun, create.go:187-189), which for `git add -A && git commit` means
// committing the user's own checkout.
type gitCall struct {
	dir  string
	argv []string
}

// recordingCleanDeps is testCleanDeps with Run wrapped so every invocation is
// recorded before being forwarded to the real git binary.
func recordingCleanDeps(t *testing.T, home string, calls *[]gitCall) CleanDeps {
	t.Helper()
	deps := testCleanDeps(t, home)
	real := deps.Run
	deps.Run = func(dir, name string, args ...string) (string, error) {
		*calls = append(*calls, gitCall{dir: dir, argv: append([]string{name}, args...)})
		return real(dir, name, args...)
	}
	return deps
}

// ranSubcommand reports whether any recorded call is `git <sub> ...`.
func ranSubcommand(calls []gitCall, sub string) bool {
	for _, c := range calls {
		if len(c.argv) >= 2 && c.argv[0] == "git" && c.argv[1] == sub {
			return true
		}
	}
	return false
}

// worktreeRegistered reports whether `git worktree list --porcelain` still
// carries a record for path.
func worktreeRegistered(t *testing.T, repo, path string) bool {
	t.Helper()
	for _, line := range strings.Split(runGit(t, repo, "worktree", "list", "--porcelain"), "\n") {
		if strings.TrimSpace(line) == "worktree "+path {
			return true
		}
	}
	return false
}

// TestDiscover_DiscoveredEntriesCarryTheFieldsTheirTeardownSteersBy pins, for
// every non-healthy shape, the two fields a route cannot be trusted without.
//
// Load-bearing rows:
//   - "missing directory" — the synthesized Session MUST carry the registered
//     WorktreePath and Branch from the porcelain. A zero WorktreePath sends
//     `git status`, `git add -A` and `git commit` into the caller's own cwd.
//   - "detached worktree with a missing directory" — no branch to name, so it
//     is not discovered at all; the `!Detached` clause is what keeps it out.
//   - "standalone session in the primary checkout" — carries the CARRIER as
//     its WorktreePath and an EMPTY Branch. Both directions matter: the
//     archive step needs the carrier to find `.afk/{name}/`, and a
//     populated Branch would offer the user's own checked-out branch to
//     `--delete-branch`.
func TestDiscover_DiscoveredEntriesCarryTheFieldsTheirTeardownSteersBy(t *testing.T) {
	type wantSession struct {
		name         string
		kind         Kind
		worktreePath func(repo string) string
		branch       string
	}
	cases := []struct {
		name         string
		seed         func(t *testing.T, repo string) string
		wantSessions []wantSession
	}{
		{
			name: "worktree directory deleted out from under git",
			seed: func(t *testing.T, repo string) string {
				wt := addWorktree(t, repo, "gone")
				writeStatus(t, wt, "gone", "done")
				if err := os.RemoveAll(wt); err != nil {
					t.Fatalf("removing worktree directory: %v", err)
				}
				return wt
			},
			wantSessions: []wantSession{{"gone", KindWorktreeMissing, nil, "gone"}},
		},
		{
			name: "foreign worktree with no session",
			seed: func(t *testing.T, repo string) string {
				return addWorktree(t, repo, "stray")
			},
			wantSessions: []wantSession{{"stray", KindWorktreeNoSession, nil, "stray"}},
		},
		{
			name: "detached worktree with a missing directory",
			seed: func(t *testing.T, repo string) string {
				wt := filepath.Join(filepath.Dir(repo), "wt-detached")
				runGit(t, repo, "worktree", "add", "--detach", wt, "main")
				if err := os.RemoveAll(wt); err != nil {
					t.Fatalf("removing worktree directory: %v", err)
				}
				return wt
			},
		},
		{
			name: "standalone session in the primary checkout",
			seed: func(t *testing.T, repo string) string {
				writeStatus(t, repo, "solo", "done")
				return repo
			},
			wantSessions: []wantSession{{"solo", KindStatusWithoutWorktree, nil, ""}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			seeded := tc.seed(t, repo)

			sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(sessions) != len(tc.wantSessions) {
				t.Fatalf("got %d sessions, want %d: %+v", len(sessions), len(tc.wantSessions), sessions)
			}
			for i, want := range tc.wantSessions {
				got := sessions[i]
				if got.Name != want.name || got.Kind != want.kind || got.Branch != want.branch {
					t.Errorf("session[%d] = {Name:%q Kind:%q Branch:%q}, want {Name:%q Kind:%q Branch:%q}",
						i, got.Name, got.Kind, got.Branch, want.name, want.kind, want.branch)
				}
				if got.WorktreePath != seeded {
					t.Errorf("session[%d].WorktreePath = %q, want the registered path %q",
						i, got.WorktreePath, seeded)
				}
				if got.WorktreePath == "" {
					t.Errorf("session[%d].WorktreePath is empty; git would run in the caller's cwd", i)
				}
				if got.Reason == "" {
					t.Errorf("session[%d].Reason is empty; a non-healthy row has nothing to explain itself with", i)
				}
			}
		})
	}
}

// TestCleanSession_MissingWorktreeDirectory is the whole of route 1: the
// directory git still has registered is gone, so there is nothing to inspect,
// nothing to snapshot and nothing to archive — and every one of those steps
// would run git INSIDE the dead path. The archive skip is reported rather than
// silent, because `.afk/{name}` died with the worktree and a caller told
// "archived to ..." would be told a falsehood.
func TestCleanSession_MissingWorktreeDirectory(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "gone")
	writeStatus(t, wt, "gone", "done")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("removing worktree directory: %v", err)
	}

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Kind != KindWorktreeMissing {
		t.Fatalf("expected one KindWorktreeMissing session, got %+v", sessions)
	}

	var calls []gitCall
	deps := recordingCleanDeps(t, t.TempDir(), &calls)
	deps.Alive = func(int) bool {
		t.Fatal("Alive must not be called: the dead path has no readable host.json")
		return false
	}

	res, err := CleanSession(repo, sessions[0], deps)
	if err != nil {
		t.Fatalf("CleanSession: %v", err)
	}

	for _, sub := range []string{"status", "add", "commit"} {
		if ranSubcommand(calls, sub) {
			t.Errorf("CleanSession ran `git %s` for a worktree whose directory is gone: %v", sub, calls)
		}
	}
	for _, c := range calls {
		if c.dir != repo {
			t.Errorf("git ran with dir=%q, want the repo root %q (argv %v)", c.dir, repo, c.argv)
		}
	}
	if !res.ArchiveSkipped {
		t.Error("CleanResult.ArchiveSkipped = false, want true: .afk/gone died with the worktree")
	}
	if res.ArchivePath != "" {
		t.Errorf("CleanResult.ArchivePath = %q, want \"\": nothing was archived", res.ArchivePath)
	}
	if !strings.Contains(res.Notes, "not archiving") {
		t.Errorf("CleanResult.Notes = %q, want the skipped archive reported rather than silent", res.Notes)
	}
	if res.SnapshotCommitted {
		t.Error("CleanResult.SnapshotCommitted = true, want false: there was nothing to commit")
	}
	if worktreeRegistered(t, repo, wt) {
		t.Errorf("worktree %s is still registered after CleanSession", wt)
	}
	if !strings.Contains(runGit(t, repo, "branch", "--list"), "gone") {
		t.Error("branch `gone` was deleted; CleanSession must never delete a branch")
	}
}

// TestCleanSession_ForeignWorktreeRoutes covers route 2, whose population
// explicitly includes worktrees afk never created (see Kind's docs).
//
// Load-bearing rows: both dirty ones. `isDirty` counts untracked files and
// `snapshotCommit` runs `git add -A`, so snapshotting here would commit a
// stranger's stray files onto a branch afk does not own — and a foreign
// repo's pre-commit hooks could fail the commit outright. Refusing preserves
// the work, which is the point. The clean row pins the other half: removal
// without `--force`, because --force on a worktree afk does not own is how
// uncommitted work disappears.
func TestCleanSession_ForeignWorktreeRoutes(t *testing.T) {
	cases := []struct {
		name       string
		dirty      func(t *testing.T, wt string)
		wantRemove bool
	}{
		{name: "clean foreign worktree is removed", wantRemove: true},
		{
			name: "untracked file refuses the removal",
			dirty: func(t *testing.T, wt string) {
				if err := os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
					t.Fatalf("seed untracked file: %v", err)
				}
			},
		},
		{
			name: "modified tracked file refuses the removal",
			dirty: func(t *testing.T, wt string) {
				if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("edited\n"), 0o644); err != nil {
					t.Fatalf("modify tracked file: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			wt := addWorktree(t, repo, "stray")
			if tc.dirty != nil {
				tc.dirty(t, wt)
			}
			commitsBefore := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "stray"))

			sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(sessions) != 1 || sessions[0].Kind != KindWorktreeNoSession {
				t.Fatalf("expected one KindWorktreeNoSession session, got %+v", sessions)
			}

			var calls []gitCall
			deps := recordingCleanDeps(t, t.TempDir(), &calls)
			_, err = CleanSession(repo, sessions[0], deps)

			for _, c := range calls {
				for _, a := range c.argv {
					if a == "--force" || a == "-f" {
						t.Errorf("CleanSession passed --force to git for a foreign worktree: %v", c.argv)
					}
				}
			}

			if tc.wantRemove {
				if err != nil {
					t.Fatalf("CleanSession: %v", err)
				}
				if worktreeRegistered(t, repo, wt) {
					t.Errorf("worktree %s is still registered after cleaning a clean foreign worktree", wt)
				}
				return
			}

			if err == nil {
				t.Fatal("CleanSession: expected a refusal for a dirty foreign worktree, got nil")
			}
			if !errors.Is(err, ErrDirtyForeignWorktree) {
				t.Errorf("CleanSession error = %v, want it to wrap ErrDirtyForeignWorktree", err)
			}
			if ranSubcommand(calls, "add") || ranSubcommand(calls, "commit") {
				t.Errorf("CleanSession staged or committed inside a foreign worktree: %v", calls)
			}
			if !worktreeRegistered(t, repo, wt) {
				t.Errorf("worktree %s was removed despite being dirty", wt)
			}
			if _, statErr := os.Stat(wt); statErr != nil {
				t.Errorf("worktree directory %s was destroyed despite being dirty: %v", wt, statErr)
			}
			if got := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "stray")); got != commitsBefore {
				t.Errorf("branch `stray` gained a commit (%s -> %s); the dirty refusal must not snapshot", commitsBefore, got)
			}
		})
	}
}

// TestCleanSession_FaultRouteRefusesAnEmptyWorktreePath is the last line of
// defense behind the "always carry the registered path" rule Discover already
// enforces: a Session assembled anywhere else must not be able to aim a
// teardown at the caller's own working directory. cleanScratchSession has the
// same guard (clean.go's scratch branch) for the same reason.
func TestCleanSession_FaultRouteRefusesAnEmptyWorktreePath(t *testing.T) {
	for _, kind := range []Kind{KindWorktreeMissing, KindWorktreeNoSession} {
		t.Run(string(kind), func(t *testing.T) {
			repo := newTestRepo(t)
			var calls []gitCall
			deps := recordingCleanDeps(t, t.TempDir(), &calls)

			_, err := CleanSession(repo, Session{Name: "x", Branch: "x", Kind: kind}, deps)
			if err == nil {
				t.Fatal("CleanSession: expected a refusal for an empty WorktreePath, got nil")
			}
			if len(calls) != 0 {
				t.Errorf("CleanSession ran %d git commands before refusing: %v", len(calls), calls)
			}
		})
	}
}

// TestTerminateCleanHost_CorruptHostJson pins route 3, a deliberate behavior
// change: an unparseable host.json used to be indistinguishable from an absent
// one and both were a no-op, which left the socket, host.json and host.log
// behind forever. No PID is recoverable from a file that will not parse, so no
// signal is sent — but the socket path does not come from the file at all
// (host.SocketPath derives it from repoRoot+name), so all three still go.
func TestTerminateCleanHost_CorruptHostJson(t *testing.T) {
	repo := newTestRepo(t)
	writeRawHostFile(t, repo, "corrupt-host", "{")
	dir := filepath.Join(repo, ".afk", "corrupt-host")
	metaPath := filepath.Join(dir, "host.json")
	logPath := filepath.Join(dir, "host.log")
	if err := os.WriteFile(logPath, []byte("host log\n"), 0o644); err != nil {
		t.Fatalf("seed host.log: %v", err)
	}
	socket, err := host.SocketPath(repo, "corrupt-host")
	if err != nil {
		t.Fatalf("host.SocketPath: %v", err)
	}
	if err := os.WriteFile(socket, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("seed placeholder socket: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(socket) })

	deps := testCleanDeps(t, t.TempDir())
	deps.Alive = func(int) bool {
		t.Fatal("Alive must not be called: an unparseable host.json yields no PID")
		return false
	}
	deps.SignalKill = func(int, syscall.Signal) error {
		t.Fatal("SignalKill must not be called: an unparseable host.json yields no PID")
		return nil
	}
	deps.DialExit = func(string, time.Duration) error {
		t.Fatal("DialExit must not be called: an unparseable host.json yields no PID")
		return nil
	}

	if err := terminateCleanHost(deps.withDefaults(), Session{Name: "corrupt-host", WorktreePath: repo}); err != nil {
		t.Fatalf("terminateCleanHost: %v", err)
	}
	for _, path := range []string{socket, metaPath, logPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived a corrupt-host.json teardown (err=%v)", path, err)
		}
	}
}

// TestCleanSession_ArchiveFailureAbortsBeforeSourceRemoval pins the data-loss
// guarantee the best-effort staging must never erode: archiving is
// copy-then-verify-then-remove, so a failed archive has to suppress the source
// removal AND the worktree removal. Degrading this step to "report and carry
// on" would delete the only copy of a session's notepads.
func TestCleanSession_ArchiveFailureAbortsBeforeSourceRemoval(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "archived")
	statusPath := writeStatus(t, wt, "archived", "done")

	var calls []gitCall
	deps := recordingCleanDeps(t, t.TempDir(), &calls)
	deps.HomeDir = func() (string, error) { return "", errors.New("no home directory") }

	sess := Session{
		Name: "archived", WorktreePath: wt, Branch: "archived",
		StatusPath: statusPath, Status: Status{Phase: "done"},
	}
	if _, err := CleanSession(repo, sess, deps); err == nil {
		t.Fatal("CleanSession: expected an error when archiving fails, got nil")
	}
	if _, err := os.Stat(statusPath); err != nil {
		t.Errorf("%s was removed after a FAILED archive: %v", statusPath, err)
	}
	if ranSubcommand(calls, "worktree") {
		t.Errorf("CleanSession touched `git worktree` after a failed archive: %v", calls)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree %s was removed after a failed archive: %v", wt, err)
	}
}

// TestCleanableWithoutForceKindGate is the pure half of the phase gate, with
// the one environment fact it branches on passed in rather than probed.
//
// Load-bearing rows: the two `stale status` rows. A corrupt status.json does
// NOT prove nothing is in flight — the conductor may be mid-run behind an
// unparseable file — so it passes the gate only while no host is running.
// Without that guard a CLI clean kills a live conductor. The two worktree-side
// rows are the opposite case: their phase is empty because the file is gone,
// not because work is in flight, so a live host cannot change the answer.
//
// The `status without worktree` rows are a third case, and the two
// empty-phase rows are the point of it. That kind behaves like a healthy
// session while its phase is readable (terminal or --force), but an EMPTY
// phase on it covers BOTH a corrupt status.json — the kind overrides
// KindStaleStatus, so nothing else carries the stale fact — and a parsed file
// whose phase is genuinely empty. Neither proves the conductor stopped, so
// both clean without --force only while no host is running. That is
// deliberate: the kind inherits KindStaleStatus's liveness rule along with
// the fact it displaced.
func TestCleanableWithoutForceKindGate(t *testing.T) {
	cases := []struct {
		name        string
		sess        Session
		hostRunning bool
		want        bool
	}{
		{"done", Session{Status: Status{Phase: "done"}}, false, true},
		{"failed", Session{Status: Status{Phase: "failed"}}, false, true},
		{"executing", Session{Status: Status{Phase: "executing"}}, false, false},
		{"executing with a live host", Session{Status: Status{Phase: "executing"}}, true, false},
		{"empty phase, no kind", Session{}, false, false},
		{"scratch", Session{Scratch: true}, false, true},
		{"stale status, host dead", Session{Kind: KindStaleStatus}, false, true},
		{"stale status, host running", Session{Kind: KindStaleStatus}, true, false},
		{"worktree missing, host dead", Session{Kind: KindWorktreeMissing}, false, true},
		{"worktree missing, host running", Session{Kind: KindWorktreeMissing}, true, true},
		{"worktree no session, host dead", Session{Kind: KindWorktreeNoSession}, false, true},
		{"worktree no session, host running", Session{Kind: KindWorktreeNoSession}, true, true},
		{"status without worktree, done", Session{Kind: KindStatusWithoutWorktree, Status: Status{Phase: "done"}}, false, true},
		{"status without worktree, failed", Session{Kind: KindStatusWithoutWorktree, Status: Status{Phase: "failed"}}, false, true},
		{"status without worktree, executing", Session{Kind: KindStatusWithoutWorktree, Status: Status{Phase: "executing"}}, false, false},
		{"status without worktree, executing with a live host", Session{Kind: KindStatusWithoutWorktree, Status: Status{Phase: "executing"}}, true, false},
		{"status without worktree, empty phase, host dead", Session{Kind: KindStatusWithoutWorktree}, false, true},
		{"status without worktree, empty phase, host running", Session{Kind: KindStatusWithoutWorktree}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanableWithoutForce(tc.sess, tc.hostRunning); got != tc.want {
				t.Errorf("cleanableWithoutForce(%+v, %v) = %v, want %v", tc.sess, tc.hostRunning, got, tc.want)
			}
		})
	}
}

// TestCleanableWithoutForceProbesTheRealHostForStaleStatus pins the impure
// wrapper to the pure decision above: the liveness fact is read from the same
// host.json `afk attach` uses, and it is only paid for by the one kind whose
// answer depends on it.
func TestCleanableWithoutForceProbesTheRealHostForStaleStatus(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "stale")
	statusPath := writeRawStatusFile(t, wt, "stale", "{not json")
	sess := Session{Name: "stale", WorktreePath: wt, Branch: "stale", StatusPath: statusPath, Kind: KindStaleStatus}

	if !CleanableWithoutForce(sess) {
		t.Error("CleanableWithoutForce = false with no host.json at all, want true")
	}

	writeHostFixture(t, wt, "stale", 999999)
	if !CleanableWithoutForce(sess) {
		t.Error("CleanableWithoutForce = false with a dead host PID, want true")
	}

	writeHostFixture(t, wt, "stale", os.Getpid())
	if CleanableWithoutForce(sess) {
		t.Error("CleanableWithoutForce = true with a LIVE host, want false: --force is still required")
	}
}

// TestCleanableWithoutForceCorruptHostJSONRequiresForce covers the fourth
// host.json state the three rows above do not: present but unparseable.
//
// It is the one that matters most and was the easiest to get wrong, because
// the obvious implementation — "any ReadMeta error means no host" — collapses
// it into the absent case and answers "cleanable". That is backwards. A torn
// host.json is what a crashing or mid-write host LEAVES BEHIND, so it is
// evidence a host existed and no evidence at all that it stopped. Combined
// with a corrupt status.json (the only kind that consults liveness), answering
// cleanable let `afk clean` tear down a session whose conductor was alive,
// with no --force and no prompt. Unprovable is not disproved.
func TestCleanableWithoutForceCorruptHostJSONRequiresForce(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "double")
	statusPath := writeRawStatusFile(t, wt, "double", "{not json")
	sess := Session{Name: "double", WorktreePath: wt, Branch: "double", StatusPath: statusPath, Kind: KindStaleStatus}

	// Sanity: with no host.json this shape IS cleanable, so a false below is
	// attributable to the corrupt file and not to the fixture.
	if !CleanableWithoutForce(sess) {
		t.Fatal("fixture broken: CleanableWithoutForce = false with no host.json, want true")
	}

	metaPath := filepath.Join(wt, ".afk", "double", "host.json")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(metaPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt host.json: %v", err)
	}

	if CleanableWithoutForce(sess) {
		t.Error("CleanableWithoutForce = true with BOTH status.json and host.json corrupt, " +
			"want false: an unreadable host.json is not evidence the host stopped, so --force is required")
	}
}

// TestCleanSession_StaleStatusTakesTheNormalPath is the other side of the
// routing switch: KindStaleStatus is a fault, but it is not a WORKTREE-side
// fault. Its directory and its `.afk/{name}/` are both intact — only the
// JSON inside is unreadable — so its teardown is the ordinary one, archive
// included. Routing it alongside the two worktree kinds would skip the archive
// and throw away the notepads of a session whose only problem was one bad byte.
func TestCleanSession_StaleStatusTakesTheNormalPath(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "stale")
	writeRawStatusFile(t, wt, "stale", "{not json")
	notepad := filepath.Join(wt, ".afk", "stale", "learnings.md")
	if err := os.WriteFile(notepad, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("seed notepad: %v", err)
	}

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Kind != KindStaleStatus {
		t.Fatalf("expected one KindStaleStatus session, got %+v", sessions)
	}
	if !CleanableWithoutForce(sessions[0]) {
		t.Error("CleanableWithoutForce = false for a corrupt status.json with no host, want true")
	}

	home := t.TempDir()
	res, err := CleanSession(repo, sessions[0], testCleanDeps(t, home))
	if err != nil {
		t.Fatalf("CleanSession: %v", err)
	}
	if res.ArchiveSkipped {
		t.Error("CleanResult.ArchiveSkipped = true, want false: the session directory was intact")
	}
	if res.ArchivePath == "" {
		t.Fatal("CleanResult.ArchivePath is empty; a stale-status session still gets archived")
	}
	if got, err := os.ReadFile(filepath.Join(res.ArchivePath, "learnings.md")); err != nil || string(got) != "keep me\n" {
		t.Errorf("archived learnings.md = %q (err=%v), want %q", got, err, "keep me\n")
	}
	if worktreeRegistered(t, repo, wt) {
		t.Errorf("worktree %s is still registered after CleanSession", wt)
	}
}

// --- (W1.T1) the status-without-worktree route ----------------------------

// failingRunCleanDeps is testCleanDeps with a Run that fails the test on ANY
// invocation. It is the only honest way to assert "this route runs no git":
// a filesystem check can only observe the damage after the fact, and only in
// a fixture where the command happened to succeed. `git add -A && git commit`
// inside the carrier would succeed quietly on most fixtures — it is the user's
// own checkout, and committing their work is the failure.
func failingRunCleanDeps(t *testing.T, home string) CleanDeps {
	t.Helper()
	deps := testCleanDeps(t, home)
	deps.Run = func(dir, name string, args ...string) (string, error) {
		t.Fatalf("Run must not be called on the status-without-worktree route, got %s %v (dir=%s)", name, args, dir)
		return "", nil
	}
	return deps
}

// TestCleanSession_StatusWithoutWorktreeRunsNoGitCommand is the most
// important test in this file after the scratch one, and for the same reason:
// this route's WorktreePath is the CARRIER worktree, which for a standalone
// `/afk:go` session is the user's own main checkout. `git add -A && git
// commit` there commits work the user never staged; `git worktree remove`
// there aims at their checkout. The assertion is therefore on the injected
// Run, not on the filesystem.
//
// The surviving-bystanders half is not decoration: the removal is an
// os.RemoveAll inside a directory full of the user's files, so the test pins
// what must still be there afterward — an untracked file (which a snapshot
// commit would have swept up), a second session's directory, and the plan
// file, which is deliberately left in place because it lives in the user's
// checkout and is not session-owned state.
func TestCleanSession_StatusWithoutWorktreeRunsNoGitCommand(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(t *testing.T, repo string)
		wantOut string
	}{
		{
			name: "parsed status.json",
			seed: func(t *testing.T, repo string) {
				writeStatus(t, repo, "standalone", "done")
			},
			wantOut: "learnings\n",
		},
		{
			name: "corrupt status.json",
			seed: func(t *testing.T, repo string) {
				writeRawStatusFile(t, repo, "standalone", "{not json")
			},
			wantOut: "learnings\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			tc.seed(t, repo)
			notepad := filepath.Join(repo, ".afk", "standalone", "learnings.md")
			if err := os.WriteFile(notepad, []byte(tc.wantOut), 0o644); err != nil {
				t.Fatalf("seed notepad: %v", err)
			}
			// Bystanders in the same carrier, all of which must survive.
			untracked := filepath.Join(repo, "scratch.txt")
			if err := os.WriteFile(untracked, []byte("mine\n"), 0o644); err != nil {
				t.Fatalf("seed untracked file: %v", err)
			}
			writeStatus(t, repo, "other-session", "executing")
			if err := os.MkdirAll(filepath.Join(repo, ".afk", "plans"), 0o755); err != nil {
				t.Fatalf("mkdir .afk/plans: %v", err)
			}
			plan := filepath.Join(repo, ".afk", "plans", "standalone.md")
			if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
				t.Fatalf("seed plan file: %v", err)
			}
			commitsBefore := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "HEAD"))

			sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			var target *Session
			for i := range sessions {
				if sessions[i].Name == "standalone" {
					target = &sessions[i]
				}
			}
			if target == nil {
				t.Fatalf("no session named standalone in %+v", sessions)
			}
			if target.Kind != KindStatusWithoutWorktree {
				t.Fatalf("standalone resolved to Kind %q, want %q", target.Kind, KindStatusWithoutWorktree)
			}

			home := t.TempDir()
			res, err := CleanSession(repo, *target, failingRunCleanDeps(t, home))
			if err != nil {
				t.Fatalf("CleanSession: %v", err)
			}

			if res.Kind != KindStatusWithoutWorktree {
				t.Errorf("CleanResult.Kind = %q, want %q", res.Kind, KindStatusWithoutWorktree)
			}
			if res.Branch != "" {
				t.Errorf("CleanResult.Branch = %q, want empty: this session owns no branch", res.Branch)
			}
			if res.WorktreePath != repo {
				t.Errorf("CleanResult.WorktreePath = %q, want the carrier %q", res.WorktreePath, repo)
			}
			if res.SnapshotCommitted {
				t.Error("CleanResult.SnapshotCommitted = true; this route must never commit in the carrier")
			}
			if res.ArchiveSkipped {
				t.Error("CleanResult.ArchiveSkipped = true, want false: the session directory was intact")
			}
			if res.ArchivePath == "" {
				t.Fatal("CleanResult.ArchivePath is empty; the notepads must be archived before removal")
			}
			if got, err := os.ReadFile(filepath.Join(res.ArchivePath, "learnings.md")); err != nil || string(got) != tc.wantOut {
				t.Errorf("archived learnings.md = %q (err=%v), want %q", got, err, tc.wantOut)
			}
			if _, err := os.Stat(filepath.Join(res.ArchivePath, "status.json")); err != nil {
				t.Errorf("archived status.json missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(repo, ".afk", "standalone")); !os.IsNotExist(err) {
				t.Errorf(".afk/standalone still exists after the clean (err=%v)", err)
			}
			for _, survivor := range []string{
				untracked,
				plan,
				filepath.Join(repo, "README.md"),
				filepath.Join(repo, ".afk", "other-session", "status.json"),
			} {
				if _, err := os.Stat(survivor); err != nil {
					t.Errorf("%s was destroyed by the clean: %v", survivor, err)
				}
			}
			if got := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "HEAD")); got != commitsBefore {
				t.Errorf("carrier gained a commit (%s -> %s); this route must run no git at all", commitsBefore, got)
			}
			if got := runGit(t, repo, "status", "--porcelain"); !strings.Contains(got, "?? scratch.txt") {
				t.Errorf("git status --porcelain = %q, want the untracked scratch.txt still untracked", got)
			}
		})
	}
}

// TestCleanSession_StatusWithoutWorktreeRefusesUnsafeInput guards the one line
// on this route that can destroy data outside the session: `.afk/{name}/` is
// removed with os.RemoveAll and its root is the user's own checkout, so an
// empty name resolves to `.afk` itself (every session directory in the
// carrier, status-backed ones included) and a traversing name resolves outside
// it entirely. An empty WorktreePath is refused for the reason
// ErrNoWorktreePath exists. Discover can only produce a filepath.Base'd name
// and always fills the carrier path; this is defense against a Session
// assembled by a future caller.
func TestCleanSession_StatusWithoutWorktreeRefusesUnsafeInput(t *testing.T) {
	for _, tc := range []struct {
		label    string
		name     string
		emptyDir bool
	}{
		{label: "empty name", name: ""},
		{label: "dot", name: "."},
		{label: "dotdot", name: ".."},
		{label: "traversal", name: "../elsewhere"},
		{label: "nested", name: "nested/name"},
		{label: "no worktree path", name: "standalone", emptyDir: true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			repo := newTestRepo(t)
			writeStatus(t, repo, "bystander", "done")
			sess := Session{Name: tc.name, WorktreePath: repo, Kind: KindStatusWithoutWorktree}
			if tc.emptyDir {
				sess.WorktreePath = ""
			}

			if _, err := CleanSession(repo, sess, failingRunCleanDeps(t, t.TempDir())); err == nil {
				t.Fatalf("CleanSession(%+v): expected a refusal, got nil", sess)
			}
			if _, err := os.Stat(filepath.Join(repo, ".afk", "bystander")); err != nil {
				t.Fatalf("an unrelated session directory was removed: %v", err)
			}
		})
	}
}

// TestCleanSessionWithOptions_StatusWithoutWorktreeHasNoBranchToDelete pins
// the plan's `--delete-branch` guarantee at this layer: the kind carries no
// branch, so the option reaches deleteSessionBranch's empty-Branch case — a
// REPORTED no-op, not a `git branch -D` against whatever the carrier happens
// to be checked out on. The zero-git assertion rides along, because that is
// the outcome a populated Branch would break.
func TestCleanSessionWithOptions_StatusWithoutWorktreeHasNoBranchToDelete(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "standalone", "done")

	sessions, err := Discover(repo, testCleanDeps(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Kind != KindStatusWithoutWorktree {
		t.Fatalf("expected one KindStatusWithoutWorktree session, got %+v", sessions)
	}

	res, err := CleanSessionWithOptions(repo, sessions[0], failingRunCleanDeps(t, t.TempDir()),
		CleanOptions{DeleteBranch: true, ConfirmedUnmerged: true})
	if err != nil {
		t.Fatalf("CleanSessionWithOptions: %v", err)
	}
	if res.BranchDeleted {
		t.Error("CleanResult.BranchDeleted = true, want false: this session owns no branch")
	}
	if res.DeletedBranchSHA != "" {
		t.Errorf("CleanResult.DeletedBranchSHA = %q, want empty", res.DeletedBranchSHA)
	}
	if !strings.Contains(res.Notes, "no branch") {
		t.Errorf("CleanResult.Notes = %q, want it to report the branch-delete no-op", res.Notes)
	}
	if got := strings.TrimSpace(runGit(t, repo, "branch", "--show-current")); got != "main" {
		t.Errorf("carrier's checked-out branch = %q after the clean, want %q", got, "main")
	}
}

// TestCleanableWithoutForceProbesTheRealHostForStatusWithoutWorktree is the
// impure half of the gate for the new kind: an EMPTY phase on it is the stale
// sub-case (the kind overrides KindStaleStatus, and a parsed file can also
// carry an empty phase), so the answer has to consult the same host.json
// KindStaleStatus consults. A terminal phase never asks — a `done` standalone
// session is cleanable whatever a leftover host.json says.
func TestCleanableWithoutForceProbesTheRealHostForStatusWithoutWorktree(t *testing.T) {
	repo := newTestRepo(t)
	writeRawStatusFile(t, repo, "standalone", "{not json")
	sess := Session{Name: "standalone", WorktreePath: repo, Kind: KindStatusWithoutWorktree}

	if !CleanableWithoutForce(sess) {
		t.Error("CleanableWithoutForce = false with no host.json at all, want true")
	}

	writeHostFixture(t, repo, "standalone", os.Getpid())
	if CleanableWithoutForce(sess) {
		t.Error("CleanableWithoutForce = true with a LIVE host, want false: --force is still required")
	}

	done := Session{Name: "standalone", WorktreePath: repo, Kind: KindStatusWithoutWorktree,
		Status: Status{Phase: "done"}}
	if !CleanableWithoutForce(done) {
		t.Error("CleanableWithoutForce = false for a done phase with a live host, want true: the phase is terminal")
	}
}

// --- CleanSessionWithOptions / CleanOptions.DeleteBranch --------------------
//
// (W2.T3) These tests exercise the opt-in branch-deletion path added on top
// of CleanSession's existing teardown. TestCleanSession_NeverDeletesBranch
// above stays untouched: it is the guard that CleanSession's zero-options
// wrapper never takes this path at all.

// commitOnBranch adds one commit inside wt so the branch it is checked out on
// diverges from main, making Merged report it as NOT an ancestor (unmerged).
// addWorktree alone leaves a branch pointing at the same commit as main,
// which is trivially "merged" by ancestry — this is what the unmerged test
// rows need in order to differ from the merged ones at all.
func commitOnBranch(t *testing.T, wt, filename string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, filename), []byte("unmerged work\n"), 0o644); err != nil {
		t.Fatalf("seed %s: %v", filename, err)
	}
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-m", "feat: add unmerged work")
}

// localBranchExists reports whether repo still has a local branch named name.
func localBranchExists(t *testing.T, repo, name string) bool {
	t.Helper()
	out := runGit(t, repo, "branch", "--list", name)
	return strings.Contains(out, name)
}

// TestCleanSessionWithOptions_DeleteBranch is the load-bearing table test for
// the DeleteBranch/ConfirmedUnmerged opt-in. It never injects IsSpiceTracked:
// none of these repos are git-spice-initialized (newTestRepo never runs `gs
// repo init`), so spice.Detect(repo) is false regardless of whether a real
// git-spice binary happens to be on $PATH, and CleanDeps.withDefaults's real
// spice.IsTracked degrades to false — every row here exercises the plain-git
// branch-delete route on purpose. The spice-tracked route is covered
// separately (TestCleanSessionWithOptions_SpiceTrackedBranchUsesGsBranchDelete)
// because it requires mocking the seam, not a real repo shape.
//
// Load-bearing rows: "unmerged without confirmation is kept" is the row that
// proves merge state INFORMS rather than gates — the plan's explicit
// requirement that a caller can still refuse the deletion of unmerged work.
// "opt-out never deletes" pins that CleanOptions{} (DeleteBranch: false) is
// inert even when the branch is trivially mergeable, i.e. the option is what
// gates deletion, not the merge state alone.
func TestCleanSessionWithOptions_DeleteBranch(t *testing.T) {
	cases := []struct {
		name              string
		unmerged          bool
		opts              CleanOptions
		wantDeleted       bool
		wantNoteSubstring string
	}{
		{
			name:        "merged branch is deleted",
			opts:        CleanOptions{DeleteBranch: true},
			wantDeleted: true,
		},
		{
			name:              "unmerged without confirmation is kept",
			unmerged:          true,
			opts:              CleanOptions{DeleteBranch: true},
			wantDeleted:       false,
			wantNoteSubstring: "not deleting branch",
		},
		{
			name:        "unmerged with ConfirmedUnmerged is deleted",
			unmerged:    true,
			opts:        CleanOptions{DeleteBranch: true, ConfirmedUnmerged: true},
			wantDeleted: true,
		},
		{
			name:        "opt-out never deletes",
			opts:        CleanOptions{},
			wantDeleted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			branch := "opt-" + strings.ReplaceAll(tc.name, " ", "-")
			wt := addWorktree(t, repo, branch)
			writeStatus(t, wt, branch, "done")
			if tc.unmerged {
				commitOnBranch(t, wt, "extra.txt")
			}

			sess := Session{
				Name:         branch,
				WorktreePath: wt,
				Branch:       branch,
				Status:       Status{Phase: "done", BaseBranch: "main"},
			}

			deps := testCleanDeps(t, t.TempDir())
			res, err := CleanSessionWithOptions(repo, sess, deps, tc.opts)
			if err != nil {
				t.Fatalf("CleanSessionWithOptions: %v", err)
			}

			// The worktree teardown itself must never depend on the branch
			// decision: it runs first, unconditionally.
			if worktreeRegistered(t, repo, wt) {
				t.Errorf("worktree %s is still registered after CleanSessionWithOptions", wt)
			}

			gotDeleted := !localBranchExists(t, repo, branch)
			if gotDeleted != tc.wantDeleted {
				t.Errorf("branch %q deleted = %v, want %v", branch, gotDeleted, tc.wantDeleted)
			}
			if res.BranchDeleted != tc.wantDeleted {
				t.Errorf("CleanResult.BranchDeleted = %v, want %v", res.BranchDeleted, tc.wantDeleted)
			}
			if tc.wantDeleted {
				if res.DeletedBranchSHA == "" {
					t.Error("CleanResult.DeletedBranchSHA is empty for a deleted branch; no recovery handle recorded")
				}
			} else if res.DeletedBranchSHA != "" {
				t.Errorf("CleanResult.DeletedBranchSHA = %q, want empty: nothing was deleted", res.DeletedBranchSHA)
			}
			if tc.wantNoteSubstring != "" && !strings.Contains(res.Notes, tc.wantNoteSubstring) {
				t.Errorf("CleanResult.Notes = %q, want it to contain %q", res.Notes, tc.wantNoteSubstring)
			}
		})
	}
}

// TestCleanSessionWithOptions_DeleteBranchMergedWhileHeadElsewhere is the case
// deleteSessionBranch's own doc comment cites to justify `git branch -D` over
// `-d`: `-d` checks merged-ness against the CURRENT branch (or upstream) of
// the repo `git branch` runs in, not against sess.Status.BaseBranch — the
// base Merged actually consulted. Every other case in this file runs `repo`
// (the main checkout from newTestRepo) with HEAD left on "main" for the whole
// test, which is also always the base, so `-d` and `-D` are indistinguishable
// there: both succeed. This test breaks that coincidence on purpose by
// switching repo's checked-out branch to something that does NOT contain the
// session branch's tip, while "main" (the recorded base) does — the ONLY
// case where -d and -D disagree - to prove the substitution the doc comment
// promises is load-bearing rather than cosmetic:
//
//  1. "unrelated" is branched off the repo's initial commit, before the
//     session branch gets its extra commit — so unrelated's history does NOT
//     include that commit.
//  2. The session branch is fast-forward-merged into main, so main (the
//     base) DOES contain it — Merged reports true by ancestry.
//  3. repo is checked out on "unrelated", not "main", when
//     CleanSessionWithOptions runs.
//
// `git branch -d` would refuse here (the branch is not merged into HEAD),
// even though Merged has already proven it safe by ancestry into the actual
// base. `git branch -D` deletes it regardless, which is the behavior this
// test pins.
func TestCleanSessionWithOptions_DeleteBranchMergedWhileHeadElsewhere(t *testing.T) {
	repo := newTestRepo(t)

	// Branch "unrelated" off the initial commit BEFORE the session branch
	// diverges, so it never picks up the session branch's tip.
	runGit(t, repo, "branch", "unrelated")

	branch := "merged-head-elsewhere"
	wt := addWorktree(t, repo, branch)
	writeStatus(t, wt, branch, "done")
	commitOnBranch(t, wt, "extra.txt")

	// Fast-forward main to the session branch's tip while repo is still
	// checked out on main, so Merged(..., "main") is true by ancestry.
	runGit(t, repo, "merge", branch)

	// Now move repo's HEAD off main and onto a branch that does NOT contain
	// the session branch's tip. This is the "HEAD is elsewhere" of the doc
	// comment: -d's own merged-ness check (against HEAD, not against
	// BaseBranch) would be evaluated against "unrelated" here, not "main".
	runGit(t, repo, "checkout", "unrelated")

	sess := Session{
		Name:         branch,
		WorktreePath: wt,
		Branch:       branch,
		Status:       Status{Phase: "done", BaseBranch: "main"},
	}

	deps := testCleanDeps(t, t.TempDir())
	res, err := CleanSessionWithOptions(repo, sess, deps, CleanOptions{DeleteBranch: true})
	if err != nil {
		t.Fatalf("CleanSessionWithOptions: %v", err)
	}

	if worktreeRegistered(t, repo, wt) {
		t.Errorf("worktree %s is still registered after CleanSessionWithOptions", wt)
	}

	if localBranchExists(t, repo, branch) {
		t.Errorf("branch %q still exists: -d would spuriously refuse it because repo's checked-out branch (%q) lacks its commits, even though it is merged into base %q; deletion must use -D", branch, "unrelated", "main")
	}
	if !res.BranchDeleted {
		t.Error("CleanResult.BranchDeleted = false, want true: the branch is merged into its base and should not be refused merely because repo's HEAD sits on an unrelated branch")
	}
	if res.DeletedBranchSHA == "" {
		t.Error("CleanResult.DeletedBranchSHA is empty for a deleted branch; no recovery handle recorded")
	}

	// repo's own checkout is untouched by the deletion: still "unrelated".
	currentOut := runGit(t, repo, "branch", "--show-current")
	if got := strings.TrimSpace(currentOut); got != "unrelated" {
		t.Errorf("repo's checked-out branch = %q after CleanSessionWithOptions, want %q unchanged", got, "unrelated")
	}
}

// TestCleanSessionWithOptions_ScratchHasNoBranchToDelete pins the plan's
// explicit scratch case: DeleteBranch is a no-op for a scratch session, and
// that no-op is reported rather than silent, exactly like every other skipped
// step in this package.
func TestCleanSessionWithOptions_ScratchHasNoBranchToDelete(t *testing.T) {
	repo := newTestRepo(t)
	dir := filepath.Join(repo, ".afk", "scratch-one")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := host.WriteMeta(repo, "scratch-one", host.Meta{PID: 999999, Socket: filepath.Join(t.TempDir(), "s.sock"), StartedAt: "2026-07-09T21:14:03Z", Argv: []string{"claude"}}); err != nil {
		t.Fatalf("host.WriteMeta: %v", err)
	}

	sess := Session{Name: "scratch-one", WorktreePath: repo, Scratch: true}
	deps := testCleanDeps(t, t.TempDir())
	res, err := CleanSessionWithOptions(repo, sess, deps, CleanOptions{DeleteBranch: true})
	if err != nil {
		t.Fatalf("CleanSessionWithOptions: %v", err)
	}
	if res.BranchDeleted {
		t.Error("CleanResult.BranchDeleted = true for a scratch session, want false: it never had a branch")
	}
	if !strings.Contains(res.Notes, "no branch") {
		t.Errorf("CleanResult.Notes = %q, want it to report the branch-delete no-op", res.Notes)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf(".afk/scratch-one still exists after CleanSessionWithOptions: %v", err)
	}
}

// TestCleanSessionWithOptions_SpiceTrackedBranchUsesGsBranchDelete asserts the
// git-spice coordination: when IsSpiceTracked reports true, deletion goes
// through DeleteSpiceBranch (W1.T3's primitive) instead of plain `git
// branch`, and Force is passed unconditionally once CleanSessionWithOptions
// has already decided to proceed. That "already decided" is exactly what
// lets the argv include --restack=none and --no-prompt unconditionally too:
// spice.BuildBranchDeleteArgs (verified independently in spice_test.go)
// appends both regardless of Force, so asserting the DeleteSpiceBranch opts
// here and reconstructing the argv from them proves the pipeline this task
// owns feeds that builder correctly, without this package re-deriving or
// re-testing the builder's own contract.
func TestCleanSessionWithOptions_SpiceTrackedBranchUsesGsBranchDelete(t *testing.T) {
	repo := newTestRepo(t)
	branch := "spice-tracked"
	wt := addWorktree(t, repo, branch)
	writeStatus(t, wt, branch, "done")

	sess := Session{
		Name:         branch,
		WorktreePath: wt,
		Branch:       branch,
		Status:       Status{Phase: "done", BaseBranch: "main"},
	}

	var (
		plainBranchDeleteCalled bool
		gotOpts                 spice.BranchDeleteOptions
		spiceCalled             bool
	)
	deps := testCleanDeps(t, t.TempDir())
	realRun := deps.Run
	deps.Run = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "branch" {
			plainBranchDeleteCalled = true
		}
		return realRun(dir, name, args...)
	}
	deps.IsSpiceTracked = func(dir, name string) bool {
		return dir == repo && name == branch
	}
	deps.DeleteSpiceBranch = func(opts spice.BranchDeleteOptions) (spice.BranchDeleteResult, error) {
		spiceCalled = true
		gotOpts = opts
		return spice.BranchDeleteResult{}, nil
	}

	res, err := CleanSessionWithOptions(repo, sess, deps, CleanOptions{DeleteBranch: true})
	if err != nil {
		t.Fatalf("CleanSessionWithOptions: %v", err)
	}
	if !spiceCalled {
		t.Fatal("spice-tracked branch deletion did not call DeleteSpiceBranch")
	}
	if plainBranchDeleteCalled {
		t.Error("CleanSessionWithOptions ran plain `git branch` for a git-spice-tracked branch")
	}
	if gotOpts.Dir != repo || gotOpts.Name != branch {
		t.Errorf("DeleteSpiceBranch opts = %+v, want Dir=%s Name=%s", gotOpts, repo, branch)
	}
	if !gotOpts.Force {
		t.Error("DeleteSpiceBranch opts.Force = false, want true: gs's own unmerged check runs against the wrong base, exactly like plain -d")
	}
	argv := spice.BuildBranchDeleteArgs(gotOpts.Name, gotOpts.Force)
	found := map[string]bool{}
	for _, a := range argv {
		found[a] = true
	}
	if !found["--restack=none"] {
		t.Errorf("argv %v missing --restack=none", argv)
	}
	if !found["--no-prompt"] {
		t.Errorf("argv %v missing --no-prompt", argv)
	}
	if !res.BranchDeleted {
		t.Error("CleanResult.BranchDeleted = false after a successful git-spice deletion")
	}
}

// TestCleanSessionWithOptions_SpiceDeleteRetargetReachesResult pins the other
// half of W1.T3's contract: spice.BranchDeleteResult.Retargeted names the
// upstack branches `gs branch delete` retargeted onto the deleted branch's
// parent, and the plan wants that surfaced "so the user can restack
// deliberately" — which can only happen if deleteSessionBranch carries it
// onto CleanResult instead of discarding the primitive's return value. The
// plain-git row is load-bearing too: plain `git branch -D` retargets
// nothing, so the field must stay empty on that route rather than echo a
// stale value.
func TestCleanSessionWithOptions_SpiceDeleteRetargetReachesResult(t *testing.T) {
	tests := []struct {
		name       string
		tracked    bool
		retargeted []string
		want       string
	}{
		{"spice delete with upstack branches", true, []string{"up-a", "up-b"}, "up-a, up-b"},
		{"spice delete with no upstack", true, nil, ""},
		{"plain git delete never retargets", false, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepo(t)
			branch := "retarget-" + strings.ReplaceAll(tt.name, " ", "-")
			wt := addWorktree(t, repo, branch)
			writeStatus(t, wt, branch, "done")

			sess := Session{
				Name:         branch,
				WorktreePath: wt,
				Branch:       branch,
				Status:       Status{Phase: "done", BaseBranch: "main"},
			}

			deps := testCleanDeps(t, t.TempDir())
			deps.IsSpiceTracked = func(dir, name string) bool { return tt.tracked }
			deps.DeleteSpiceBranch = func(opts spice.BranchDeleteOptions) (spice.BranchDeleteResult, error) {
				return spice.BranchDeleteResult{Retargeted: tt.retargeted}, nil
			}

			res, err := CleanSessionWithOptions(repo, sess, deps, CleanOptions{DeleteBranch: true})
			if err != nil {
				t.Fatalf("CleanSessionWithOptions: %v", err)
			}
			if !res.BranchDeleted {
				t.Fatal("CleanResult.BranchDeleted = false, want a successful deletion")
			}
			if res.RetargetedBranches != tt.want {
				t.Errorf("CleanResult.RetargetedBranches = %q, want %q", res.RetargetedBranches, tt.want)
			}
		})
	}
}

// TestCleanSessionWithOptions_SpiceDeleteFailureIsReportedNotFatal mirrors the
// rest of this package's best-effort-step convention: a branch delete that
// fails after the worktree is already gone must not turn a successful
// teardown into an error return, or a caller re-running the clean would find
// nothing left to clean and no way to see what went wrong with the branch.
func TestCleanSessionWithOptions_SpiceDeleteFailureIsReportedNotFatal(t *testing.T) {
	repo := newTestRepo(t)
	branch := "spice-delete-fails"
	wt := addWorktree(t, repo, branch)
	writeStatus(t, wt, branch, "done")

	sess := Session{
		Name:         branch,
		WorktreePath: wt,
		Branch:       branch,
		Status:       Status{Phase: "done", BaseBranch: "main"},
	}

	deps := testCleanDeps(t, t.TempDir())
	deps.IsSpiceTracked = func(string, string) bool { return true }
	deps.DeleteSpiceBranch = func(spice.BranchDeleteOptions) (spice.BranchDeleteResult, error) {
		return spice.BranchDeleteResult{}, fmt.Errorf("gs branch delete: boom")
	}

	res, err := CleanSessionWithOptions(repo, sess, deps, CleanOptions{DeleteBranch: true})
	if err != nil {
		t.Fatalf("CleanSessionWithOptions: %v, want the teardown to succeed despite the branch-delete failure", err)
	}
	if res.BranchDeleted {
		t.Error("CleanResult.BranchDeleted = true despite DeleteSpiceBranch returning an error")
	}
	if !strings.Contains(res.Notes, "boom") {
		t.Errorf("CleanResult.Notes = %q, want the DeleteSpiceBranch error reported", res.Notes)
	}
	if !localBranchExists(t, repo, branch) {
		t.Error("branch was deleted despite DeleteSpiceBranch reporting failure")
	}
}

// TestCleanSessionWithOptions_DeleteBranchTagCollisionRecordsBranchTip is the
// deleteSessionBranch half of the tag-shadowing class TestMergedIgnoresTags
// ShadowingBranchNames pins for the ancestry check: the tip-SHA capture that
// feeds CleanResult.DeletedBranchSHA runs `git rev-parse` on the branch name,
// and gitrevisions(7) resolves refs/tags/<name> before refs/heads/<name>, so
// under a colliding tag a bare rev-parse reports the TAG's commit.
// DeletedBranchSHA is the reflog-recovery handle the summary prints — a
// deleted branch is only recoverable by SHA — so recording the tag's commit
// hands the user a recovery handle that names the wrong work, silently.
func TestCleanSessionWithOptions_DeleteBranchTagCollisionRecordsBranchTip(t *testing.T) {
	repo := newTestRepo(t)
	branch := "tag-shadowed"
	wt := addWorktree(t, repo, branch)
	writeStatus(t, wt, branch, "done")
	commitOnBranch(t, wt, "extra.txt")

	// The colliding tag points at a DIFFERENT commit (main's tip), so the two
	// resolutions are distinguishable — asserted below, or the test proves
	// nothing.
	runGit(t, repo, "tag", branch, "refs/heads/main")
	branchTip := strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/heads/"+branch))
	tagTip := strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/tags/"+branch))
	if branchTip == tagTip {
		t.Fatal("fixture broken: the tag and the branch point at the same commit, so the collision is invisible")
	}

	sess := Session{
		Name:         branch,
		WorktreePath: wt,
		Branch:       branch,
		Status:       Status{Phase: "done", BaseBranch: "main"},
	}
	// ConfirmedUnmerged keeps this test about the SHA capture alone: the
	// branch is unmerged, and whether Merged sees through the tag is
	// TestMergedIgnoresTagsShadowingBranchNames' property, not this one's.
	res, err := CleanSessionWithOptions(repo, sess, testCleanDeps(t, t.TempDir()),
		CleanOptions{DeleteBranch: true, ConfirmedUnmerged: true})
	if err != nil {
		t.Fatalf("CleanSessionWithOptions: %v", err)
	}

	if !res.BranchDeleted {
		t.Fatalf("CleanResult.BranchDeleted = false, want true (notes: %q)", res.Notes)
	}
	if res.DeletedBranchSHA != branchTip {
		t.Errorf("CleanResult.DeletedBranchSHA = %q (the tag's commit is %q), want the branch tip %q: the recovery handle must name the deleted branch's work, never the tag that shadows its name", res.DeletedBranchSHA, tagTip, branchTip)
	}
	if localBranchExists(t, repo, branch) {
		t.Errorf("branch %q still exists after CleanSessionWithOptions", branch)
	}
	// The tag is not afk's to delete: `git branch -D` addresses the branch
	// namespace and must leave the identically-named tag alone.
	if got := strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/tags/"+branch)); got != tagTip {
		t.Errorf("tag %q moved or vanished: rev-parse = %q, want %q", branch, got, tagTip)
	}
}

// TestCleanSessionWithOptions_TagCollisionNeverConfirmsAnUnmergedDelete pins
// the tag-shadowing class at the seam where it turns destructive: an unmerged
// branch, unconfirmed, under a tag that shares its name and points inside the
// base's history. Bare refs would let that tag answer deleteSessionBranch's
// Merged check as "merged" and wave the delete through with no confirmation —
// the exact false "merged" W1.T2's Must-NOT forbids. The branch must survive,
// and the refusal must be the recorded outcome.
func TestCleanSessionWithOptions_TagCollisionNeverConfirmsAnUnmergedDelete(t *testing.T) {
	repo := newTestRepo(t)
	branch := "tag-shadowed-unmerged"
	wt := addWorktree(t, repo, branch)
	writeStatus(t, wt, branch, "done")
	commitOnBranch(t, wt, "extra.txt")
	// The tag points at main's tip, so it IS an ancestor of main — resolving
	// it instead of the branch is what would flip the answer to "merged".
	runGit(t, repo, "tag", branch, "refs/heads/main")

	sess := Session{
		Name:         branch,
		WorktreePath: wt,
		Branch:       branch,
		Status:       Status{Phase: "done", BaseBranch: "main"},
	}
	res, err := CleanSessionWithOptions(repo, sess, testCleanDeps(t, t.TempDir()),
		CleanOptions{DeleteBranch: true})
	if err != nil {
		t.Fatalf("CleanSessionWithOptions: %v", err)
	}

	if res.BranchDeleted {
		t.Error("CleanResult.BranchDeleted = true: an unmerged, unconfirmed branch was deleted because a colliding tag read as merged")
	}
	if !localBranchExists(t, repo, branch) {
		t.Errorf("branch %q is gone: the unmerged refusal must keep it", branch)
	}
	if !strings.Contains(res.Notes, "not been merged") {
		t.Errorf("CleanResult.Notes = %q, want the unmerged refusal recorded", res.Notes)
	}
}
