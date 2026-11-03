package discover

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

// --- real-git-repo test fixtures -------------------------------------------
//
// Discovery's entire value proposition is faithfully reflecting real `git
// worktree list --porcelain` output joined against real on-disk files, so
// (mirroring internal/session's clean_test.go) these tests script real,
// disposable git repos under t.TempDir() rather than faking `git`.

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
// branch "main" and a local git identity set (sandboxes/CI may have none
// configured globally). The path is symlink-resolved so it matches what
// `git worktree list --porcelain` reports (macOS resolves /var to
// /private/var).
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
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".afk/\n"), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "chore: Initial commit")
	return dir
}

// addWorktree creates a new worktree at <repo's parent>/wt-<branch> checked
// out on a fresh branch named branch, and returns its path.
func addWorktree(t *testing.T, repo, branch string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(repo), "wt-"+branch)
	runGit(t, repo, "worktree", "add", "-b", branch, path, "main")
	return path
}

// statusOpts customizes writeStatus's seeded fields; zero values fall back
// to schema-valid defaults.
type statusOpts struct {
	phase         state.Phase
	awaitingInput bool
	loopCount     int
	sessionID     string
	branch        string
	baseBranch    string
}

// writeStatus seeds a schema-valid status.json for name inside worktree per
// status-schema.md §2, and returns its path.
func writeStatus(t *testing.T, worktree, name string, opts statusOpts) string {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	phase := opts.phase
	if phase == "" {
		phase = state.PhaseExecuting
	}
	sessionID := opts.sessionID
	if sessionID == "" && phase != state.PhasePlanning {
		sessionID = "11111111-1111-1111-1111-111111111111"
	}
	branch := opts.branch
	if branch == "" {
		branch = name
	}
	baseBranch := opts.baseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}
	st := state.Status{
		SessionID:     sessionID,
		PlanName:      name,
		Phase:         phase,
		Branch:        branch,
		BaseBranch:    baseBranch,
		LoopCount:     opts.loopCount,
		AwaitingInput: opts.awaitingInput,
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

// writeRawStatus writes literal contents to name's status.json, for
// exercising corrupt/unparseable input.
func writeRawStatus(t *testing.T, worktree, name, contents string) string {
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

// onePlanTaskComplete, oneTaskIncomplete: a minimal plan-format.md-valid plan
// file with one wave and two tasks (one complete, one incomplete).
const twoTaskPlan = `# Sample plan

## Waves

### Wave 1: Only wave

- [x] (W1.T1) Do the first thing

  **What to do**: Thing one.

  **Must NOT do**: Nothing in particular.

  **Files**: a.go

  **Acceptance Criteria**: It works.

  **Model**: light

- [ ] (W1.T2) Do the second thing

  **What to do**: Thing two.

  **Must NOT do**: Nothing in particular.

  **Files**: b.go

  **Acceptance Criteria**: It works.

  **Model**: balanced
`

// writePlan seeds `.afk/plans/{name}.md` inside worktree with contents.
func writePlan(t *testing.T, worktree, name, contents string) {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
}

const approveVerdict = `# Review Verdict

**Plan**: sample-plan
**Date**: 2026-07-09
**Verdict**: APPROVE

## Checks
- Compliance: PASS
- Quality: PASS
- QA: PASS

## Findings
- none
`

// writeVerdict seeds `.afk/{name}/review-verdict.md` inside worktree.
func writeVerdict(t *testing.T, worktree, name, contents string) {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "review-verdict.md")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write verdict file: %v", err)
	}
}

// findByName is a small test-only lookup helper.
func findByName(sessions []SessionState, name string) (SessionState, bool) {
	for _, s := range sessions {
		if s.Name == name {
			return s, true
		}
	}
	return SessionState{}, false
}

// --- Discover ---------------------------------------------------------------

func TestDiscover_MainCheckoutAndTwoWorktrees(t *testing.T) {
	repo := newTestRepo(t)

	// Main checkout hosts a session directly — a first-class session host,
	// not just linked worktrees.
	writeStatus(t, repo, "main-session", statusOpts{phase: state.PhaseExecuting})
	writePlan(t, repo, "main-session", twoTaskPlan)

	wtA := addWorktree(t, repo, "session-a")
	writeStatus(t, wtA, "session-a", statusOpts{phase: state.PhaseReviewing})
	writePlan(t, wtA, "session-a", twoTaskPlan)
	writeVerdict(t, wtA, "session-a", approveVerdict)

	// A worktree with no .afk/ session state at all — a fault row of its
	// own, interleaved by name with the two real sessions rather than
	// grouped into a section (see TestDiscover_WorktreeSideFaultsAreRows).
	addWorktree(t, repo, "no-afk-here")

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("len(sessions) = %d, want 3: %+v", len(sessions), sessions)
	}

	// Sorted by name.
	if sessions[0].Name != "main-session" || sessions[1].Name != "no-afk-here" || sessions[2].Name != "session-a" {
		t.Fatalf("sessions not sorted by name: %+v", sessions)
	}
	if sessions[1].Kind != session.KindWorktreeNoSession {
		t.Errorf("no-afk-here.Kind = %q, want %q", sessions[1].Kind, session.KindWorktreeNoSession)
	}

	main := sessions[0]
	if main.WorktreePath != repo {
		t.Errorf("main-session.WorktreePath = %q, want %q", main.WorktreePath, repo)
	}
	if main.Phase != state.PhaseExecuting {
		t.Errorf("main-session.Phase = %q, want executing", main.Phase)
	}
	if main.Progress != (Progress{Total: 2, Complete: 1, Incomplete: 1}) {
		t.Errorf("main-session.Progress = %+v, want {2 1 0 1}", main.Progress)
	}
	if main.Verdict != nil {
		t.Errorf("main-session.Verdict = %+v, want nil (no verdict file written)", main.Verdict)
	}

	a := sessions[2]
	if a.WorktreePath != wtA {
		t.Errorf("session-a.WorktreePath = %q, want %q", a.WorktreePath, wtA)
	}
	if a.Phase != state.PhaseReviewing {
		t.Errorf("session-a.Phase = %q, want reviewing", a.Phase)
	}
	if a.Verdict == nil {
		t.Fatal("session-a.Verdict = nil, want the parsed approve verdict")
	}
	if a.Verdict.Verdict != state.VerdictApprove {
		t.Errorf("session-a.Verdict.Verdict = %q, want APPROVE", a.Verdict.Verdict)
	}
}

func TestDiscover_PlanningPhaseWithNoPlanFileIsFirstClass(t *testing.T) {
	repo := newTestRepo(t)
	// afk new seeds status.json before the interview runs — no plan file,
	// no waves directory, nothing. This must render as a valid planning
	// session with zero tasks, not an error or a dropped session.
	writeStatus(t, repo, "fresh-session", statusOpts{phase: state.PhasePlanning})

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1: %+v", len(sessions), sessions)
	}

	got := sessions[0]
	if got.Name != "fresh-session" {
		t.Errorf("Name = %q, want fresh-session", got.Name)
	}
	if got.Phase != state.PhasePlanning {
		t.Errorf("Phase = %q, want planning", got.Phase)
	}
	if got.StatusStale {
		t.Errorf("StatusStale = true, want false")
	}
	if got.Progress != (Progress{}) {
		t.Errorf("Progress = %+v, want the zero value", got.Progress)
	}
	if got.PlanError != "" {
		t.Errorf("PlanError = %q, want empty (missing plan file is not an error)", got.PlanError)
	}
	if got.Verdict != nil {
		t.Errorf("Verdict = %+v, want nil", got.Verdict)
	}
}

func TestDiscover_AwaitingInputSurfaced(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "blocked-session", statusOpts{phase: state.PhaseReviewing, awaitingInput: true, loopCount: 2})
	writeStatus(t, repo, "working-session", statusOpts{phase: state.PhaseExecuting, awaitingInput: false})

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	blocked, ok := findByName(sessions, "blocked-session")
	if !ok {
		t.Fatal("blocked-session not found")
	}
	if !blocked.AwaitingInput {
		t.Errorf("blocked-session.AwaitingInput = false, want true")
	}
	if blocked.LoopCount != 2 {
		t.Errorf("blocked-session.LoopCount = %d, want 2", blocked.LoopCount)
	}

	working, ok := findByName(sessions, "working-session")
	if !ok {
		t.Fatal("working-session not found")
	}
	if working.AwaitingInput {
		t.Errorf("working-session.AwaitingInput = true, want false")
	}
}

// knownDeadPID spawns and reaps a short-lived process, returning its PID —
// mirrors internal/host's own meta_test.go helper, since a made-up large int
// is not a meaningful "known-dead PID" assertion.
func knownDeadPID(t *testing.T) int {
	t.Helper()
	cmd := "/bin/sh"
	if _, err := os.Stat(cmd); err != nil {
		t.Skip("/bin/sh not available to spawn a short-lived process")
	}
	proc, err := os.StartProcess(cmd, []string{cmd, "-c", "true"}, &os.ProcAttr{})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	state, err := proc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !state.Exited() {
		t.Fatal("expected process to have exited")
	}
	return proc.Pid
}

func TestDiscover_HostRunningTrueForOwnPID(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "live-session", statusOpts{phase: state.PhaseExecuting})
	if err := host.WriteMeta(repo, "live-session", host.Meta{PID: os.Getpid()}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "live-session")
	if !ok {
		t.Fatal("live-session not found")
	}
	if !got.HostRunning {
		t.Errorf("HostRunning = false, want true (host.json names this test's own PID)")
	}
	if got.HostPID != os.Getpid() {
		t.Errorf("HostPID = %d, want %d", got.HostPID, os.Getpid())
	}
}

func TestDiscover_HostRunningFalseForDeadPID(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "stale-session", statusOpts{phase: state.PhaseExecuting})
	dead := knownDeadPID(t)
	if err := host.WriteMeta(repo, "stale-session", host.Meta{PID: dead}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "stale-session")
	if !ok {
		t.Fatal("stale-session not found")
	}
	if got.HostRunning {
		t.Errorf("HostRunning = true, want false (host.json names a reaped PID)")
	}
	if got.HostPID != dead {
		t.Errorf("HostPID = %d, want %d (still recorded even though not running)", got.HostPID, dead)
	}
}

func TestDiscover_HostRunningFalseWhenHostJSONAbsent(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "no-host-session", statusOpts{phase: state.PhaseExecuting})
	// Deliberately no host.json written — this must not surface as an
	// error (StatusStale, PlanError, etc.), just HostRunning: false.

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "no-host-session")
	if !ok {
		t.Fatal("no-host-session not found")
	}
	if got.HostRunning {
		t.Errorf("HostRunning = true, want false (no host.json written)")
	}
	if got.HostPID != 0 {
		t.Errorf("HostPID = %d, want 0", got.HostPID)
	}
	if got.StatusStale {
		t.Errorf("StatusStale = true, want false (missing host.json is not a status error)")
	}
}

// TestDiscover_SessionIDPassthrough exercises the strictly-passthrough wiring
// for SessionState.SessionID: it must carry status.json's `session_id` field
// through untouched, including the planning-phase case where it is
// schema-valid-empty.
func TestDiscover_SessionIDPassthrough(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "resumable-session", statusOpts{
		phase:     state.PhaseExecuting,
		sessionID: "22222222-2222-2222-2222-222222222222",
	})
	writeStatus(t, repo, "planning-session", statusOpts{phase: state.PhasePlanning})

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	resumable, ok := findByName(sessions, "resumable-session")
	if !ok {
		t.Fatal("resumable-session not found")
	}
	if resumable.SessionID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("resumable-session.SessionID = %q, want the seeded session_id", resumable.SessionID)
	}

	planning, ok := findByName(sessions, "planning-session")
	if !ok {
		t.Fatal("planning-session not found")
	}
	if planning.SessionID != "" {
		t.Errorf("planning-session.SessionID = %q, want empty (schema-valid during planning)", planning.SessionID)
	}
}

// TestDiscover_WorktreeWithoutAfkStateIsAFaultRow is the retargeted form of
// what used to assert such a worktree was ignored entirely. It is not ignored
// any more, and the change is the point: an entry that is only ever printed
// and never surfaced cannot be cleaned, and a worktree with no session of its
// own is one of the two shapes `afk clean` now tears down. The row it
// becomes carries no phase and no progress, which is what its Kind and Reason
// are for.
func TestDiscover_WorktreeWithoutAfkStateIsAFaultRow(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "plain-branch") // no .afk/ at all

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.Name != "plain-branch" || got.Kind != session.KindWorktreeNoSession {
		t.Fatalf("row = {Name:%q Kind:%q}, want {plain-branch worktree-no-session}", got.Name, got.Kind)
	}
	if got.WorktreePath != wt {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, wt)
	}
}

func TestDiscover_MultipleSessionsInSameWorktree(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "session-one", statusOpts{phase: state.PhasePlanning})
	writeStatus(t, repo, "session-two", statusOpts{phase: state.PhaseDone})

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("len(sessions) = %d, want 2: %+v", len(sessions), sessions)
	}
	if sessions[0].Name != "session-one" || sessions[1].Name != "session-two" {
		t.Fatalf("unexpected names: %+v", sessions)
	}
}

func TestDiscover_StaleStatusIsSurfacedNotDropped(t *testing.T) {
	repo := newTestRepo(t)
	// An unknown key is corruption per status-schema.md §2 ("Parsers MUST
	// reject unknown keys"). This must not crash discovery, and the session
	// must still appear (its directory is real) rather than vanish.
	writeRawStatus(t, repo, "corrupt-session", `{
  "session_id": "11111111-1111-1111-1111-111111111111",
  "plan_name": "corrupt-session",
  "phase": "executing",
  "branch": "corrupt-session",
  "base_branch": "main",
  "loop_count": 0,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": "2026-07-09T21:14:03Z",
  "unexpected_field": "boom"
}`)

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.Name != "corrupt-session" {
		t.Errorf("Name = %q, want corrupt-session", got.Name)
	}
	if !got.StatusStale {
		t.Errorf("StatusStale = false, want true")
	}
	if got.StaleReason != state.StaleUnknownField {
		t.Errorf("StaleReason = %q, want %q", got.StaleReason, state.StaleUnknownField)
	}
	if got.Phase != "" {
		t.Errorf("Phase = %q, want empty for a stale status", got.Phase)
	}
	// The raw status above has a well-formed session_id despite the unknown
	// field elsewhere; a stale read must not leak it through — SessionID is
	// schema-valid-empty only in the `planning` phase, and a stale status has
	// no validated phase at all, so passthrough must zero it like every
	// other field.
	if got.SessionID != "" {
		t.Errorf("SessionID = %q, want empty for a stale status", got.SessionID)
	}
}

// TestDiscover_StaleStatusWithLiveHostStillReportsHostRunning pairs
// StatusStale=true with a live host.json — the reviewer-flagged gap where
// buildSessionState's stale early-return used to skip the HostRunning
// check entirely, making W4.T1's "STALE but HostRunning true: attach
// anyway" case unreachable. HostRunning/HostPID come from host.json, which
// is written and read independently of status.json's own validity.
func TestDiscover_StaleStatusWithLiveHostStillReportsHostRunning(t *testing.T) {
	repo := newTestRepo(t)
	writeRawStatus(t, repo, "corrupt-with-live-host", `{
  "session_id": "11111111-1111-1111-1111-111111111111",
  "plan_name": "corrupt-with-live-host",
  "phase": "executing",
  "branch": "corrupt-with-live-host",
  "base_branch": "main",
  "loop_count": 0,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": "2026-07-09T21:14:03Z",
  "unexpected_field": "boom"
}`)
	if err := host.WriteMeta(repo, "corrupt-with-live-host", host.Meta{PID: os.Getpid()}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "corrupt-with-live-host")
	if !ok {
		t.Fatal("corrupt-with-live-host not found")
	}
	if !got.StatusStale {
		t.Errorf("StatusStale = false, want true")
	}
	if !got.HostRunning {
		t.Errorf("HostRunning = false, want true (host.json names this test's own PID, independent of status.json validity)")
	}
	if got.HostPID != os.Getpid() {
		t.Errorf("HostPID = %d, want %d", got.HostPID, os.Getpid())
	}
}

// TestDiscover_StaleStatusWithDeadHostReportsHostRunningFalse mirrors the
// above with a dead PID, so the stale-hoist fix is proven both ways rather
// than only in the direction that happens to look right by coincidence.
func TestDiscover_StaleStatusWithDeadHostReportsHostRunningFalse(t *testing.T) {
	repo := newTestRepo(t)
	writeRawStatus(t, repo, "corrupt-with-dead-host", `{
  "session_id": "11111111-1111-1111-1111-111111111111",
  "plan_name": "corrupt-with-dead-host",
  "phase": "executing",
  "branch": "corrupt-with-dead-host",
  "base_branch": "main",
  "loop_count": 0,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": "2026-07-09T21:14:03Z",
  "unexpected_field": "boom"
}`)
	dead := knownDeadPID(t)
	if err := host.WriteMeta(repo, "corrupt-with-dead-host", host.Meta{PID: dead}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "corrupt-with-dead-host")
	if !ok {
		t.Fatal("corrupt-with-dead-host not found")
	}
	if !got.StatusStale {
		t.Errorf("StatusStale = false, want true")
	}
	if got.HostRunning {
		t.Errorf("HostRunning = true, want false (host.json names a reaped PID)")
	}
}

// TestDiscover_LivenessFieldsPassthrough exercises the strictly-passthrough
// wiring for SessionState.LastOutputAt/WedgedSince: host.json's liveness
// fields (W1.T2) must carry through untouched, exactly like HostPID/
// HostRunning above.
func TestDiscover_LivenessFieldsPassthrough(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "wedged-session", statusOpts{phase: state.PhaseExecuting})
	meta := host.Meta{
		PID:          os.Getpid(),
		LastOutputAt: "2026-07-09T21:10:00Z",
		WedgedSince:  "2026-07-09T21:14:00Z",
	}
	if err := host.WriteMeta(repo, "wedged-session", meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "wedged-session")
	if !ok {
		t.Fatal("wedged-session not found")
	}
	if got.LastOutputAt != meta.LastOutputAt {
		t.Errorf("LastOutputAt = %q, want %q", got.LastOutputAt, meta.LastOutputAt)
	}
	if got.WedgedSince != meta.WedgedSince {
		t.Errorf("WedgedSince = %q, want %q", got.WedgedSince, meta.WedgedSince)
	}
}

// TestDiscover_LivenessFieldsAbsentAreUnknownNotError covers a host.json
// written before last_output_at/wedged_since existed (or by a host that
// never populated them) — both must read as "unknown" (empty string), never
// an error and never a blocking retry, exactly like a stale status.json.
func TestDiscover_LivenessFieldsAbsentAreUnknownNotError(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "legacy-host-session", statusOpts{phase: state.PhaseExecuting})
	// A legacy host.json: only the fields that predate last_output_at/
	// wedged_since. Mirrors internal/host/meta_test.go's own legacy-file
	// fixture rather than relying on host.Meta's omitempty (belt and
	// suspenders: proves discover doesn't require the keys to be present).
	dir := filepath.Join(repo, ".afk", "legacy-host-session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{
  "pid": ` + strconv.Itoa(os.Getpid()) + `,
  "socket": "",
  "started_at": "",
  "argv": []
}`
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy host.json: %v", err)
	}

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "legacy-host-session")
	if !ok {
		t.Fatal("legacy-host-session not found")
	}
	if !got.HostRunning {
		t.Errorf("HostRunning = false, want true (legacy host.json still names a live pid)")
	}
	if got.LastOutputAt != "" {
		t.Errorf("LastOutputAt = %q, want empty (legacy host.json predates the field)", got.LastOutputAt)
	}
	if got.WedgedSince != "" {
		t.Errorf("WedgedSince = %q, want empty (legacy host.json predates the field)", got.WedgedSince)
	}
}

// TestDiscover_UnparseableHostJSONLeavesLivenessFieldsUnknown covers a
// host.json that is present but corrupt (e.g. mid-write, or truncated by a
// crash) — Discover must treat it as "unknown" across the board, exactly
// like it already does for HostPID/HostRunning, never as an error and never
// as a reason to block or retry.
func TestDiscover_UnparseableHostJSONLeavesLivenessFieldsUnknown(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "corrupt-host-session", statusOpts{phase: state.PhaseExecuting})
	dir := filepath.Join(repo, ".afk", "corrupt-host-session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt host.json: %v", err)
	}

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "corrupt-host-session")
	if !ok {
		t.Fatal("corrupt-host-session not found")
	}
	if got.HostRunning {
		t.Errorf("HostRunning = true, want false (host.json is unparseable)")
	}
	if got.HostPID != 0 {
		t.Errorf("HostPID = %d, want 0 (host.json is unparseable)", got.HostPID)
	}
	if got.LastOutputAt != "" {
		t.Errorf("LastOutputAt = %q, want empty (host.json is unparseable)", got.LastOutputAt)
	}
	if got.WedgedSince != "" {
		t.Errorf("WedgedSince = %q, want empty (host.json is unparseable)", got.WedgedSince)
	}
	if got.StatusStale {
		t.Errorf("StatusStale = true, want false (an unparseable host.json says nothing about status.json)")
	}
}

func TestDiscover_MalformedPlanFileSurfacesPlanErrorNotPanic(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "bad-plan-session", statusOpts{phase: state.PhaseExecuting})
	writePlan(t, repo, "bad-plan-session", "not a valid plan file at all")

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "bad-plan-session")
	if !ok {
		t.Fatal("bad-plan-session not found")
	}
	if got.PlanError == "" {
		t.Errorf("PlanError = empty, want a non-empty parse error")
	}
	if got.Progress != (Progress{}) {
		t.Errorf("Progress = %+v, want the zero value on a parse error", got.Progress)
	}
}

func TestDiscover_NoSessionsAnywhereReturnsEmptyNotError(t *testing.T) {
	repo := newTestRepo(t)

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("len(sessions) = %d, want 0: %+v", len(sessions), sessions)
	}
}

// --- scratch sessions (host.json with no status.json) -----------------------

// writeScratchHost seeds a `.afk/{name}/host.json` inside worktree naming
// pid and nothing else — the exact on-disk shape a scratch session leaves
// behind, since `afk new --scratch` (W1.T1) writes no status.json, no plan
// file, and creates no branch or worktree.
func writeScratchHost(t *testing.T, worktree, name string, pid int) {
	t.Helper()
	m := host.Meta{
		PID:       pid,
		Socket:    filepath.Join(worktree, ".afk", name, "host.sock"),
		StartedAt: "2026-07-09T21:14:03Z",
		Argv:      []string{"claude"},
	}
	if err := host.WriteMeta(worktree, name, m); err != nil {
		t.Fatalf("WriteMeta(%s): %v", name, err)
	}
}

// mkAfkDir creates an empty `.afk/{name}/` inside worktree, for the
// "directory with neither file" row.
func mkAfkDir(t *testing.T, worktree, name string) string {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

// kindsByName reduces a discovery result to name -> Scratch. A map collapses
// duplicates, so every caller asserts len(sessions) against len(want) FIRST:
// that pair is what makes a double-counted directory (both a normal and a
// scratch entry for one `.afk/{name}/`) fail loudly instead of silently
// deduplicating into a passing map.
func kindsByName(sessions []SessionState) map[string]bool {
	m := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		m[s.Name] = s.Scratch
	}
	return m
}

// TestDiscover_ScratchSessions covers discover's second source: a
// `.afk/{name}/` directory holding a host.json and no status.json.
//
// Load-bearing rows, in the order they appear:
//   - "host.json with no status.json" — the feature itself: exactly one
//     entry, flagged Scratch.
//   - "both files present" — the anti-double-count row. The two globs run
//     over the same `.afk/*` namespace, so a directory holding both must
//     yield ONE non-scratch session, not two. This is also the row that
//     protects afk's own per-plan notepad directories (this repo's
//     `.afk/no-plan-sessions/` has both files) from being reported as
//     scratch sessions.
//   - "directory with neither file" — a bare `.afk/{name}/` is not a
//     session under either source.
//   - "the plans directory is not a session" — `.afk/plans/` sits in the
//     same glob namespace and must never match.
//   - "unparseable host.json" — the directory's existence, not the file's
//     validity, is what makes a scratch session; an unreadable host.json
//     degrades liveness to unknown, exactly as it already does for a
//     status-backed session, and never drops the entry.
func TestDiscover_ScratchSessions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, repo string)
		want  map[string]bool // session name -> Scratch
	}{
		{
			name: "host.json with no status.json is one scratch session",
			setup: func(t *testing.T, repo string) {
				writeScratchHost(t, repo, "scratch-1", os.Getpid())
			},
			want: map[string]bool{"scratch-1": true},
		},
		{
			name: "both files present is one normal session, never double counted",
			setup: func(t *testing.T, repo string) {
				writeStatus(t, repo, "real", statusOpts{phase: state.PhaseExecuting})
				writeScratchHost(t, repo, "real", os.Getpid())
			},
			want: map[string]bool{"real": false},
		},
		{
			name: "directory with neither file yields nothing",
			setup: func(t *testing.T, repo string) {
				mkAfkDir(t, repo, "leftovers")
			},
			want: map[string]bool{},
		},
		{
			name: "status.json alone is still a normal session",
			setup: func(t *testing.T, repo string) {
				writeStatus(t, repo, "real", statusOpts{phase: state.PhaseExecuting})
			},
			want: map[string]bool{"real": false},
		},
		{
			name: "scratch and normal sessions coexist in one worktree",
			setup: func(t *testing.T, repo string) {
				writeStatus(t, repo, "real", statusOpts{phase: state.PhaseExecuting})
				writeScratchHost(t, repo, "real", os.Getpid())
				writeScratchHost(t, repo, "scratch-1", os.Getpid())
				writeScratchHost(t, repo, "scratch-2", knownDeadPID(t))
			},
			want: map[string]bool{"real": false, "scratch-1": true, "scratch-2": true},
		},
		{
			name: "the plans directory is not a session",
			setup: func(t *testing.T, repo string) {
				writePlan(t, repo, "orphan-plan", twoTaskPlan)
			},
			want: map[string]bool{},
		},
		{
			name: "unparseable host.json still yields a scratch session",
			setup: func(t *testing.T, repo string) {
				dir := mkAfkDir(t, repo, "corrupt-scratch")
				if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte("{not valid json"), 0o644); err != nil {
					t.Fatalf("write corrupt host.json: %v", err)
				}
			},
			want: map[string]bool{"corrupt-scratch": true},
		},
		{
			name: "scratch session in a linked worktree is found",
			setup: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "feature-x")
				writeScratchHost(t, wt, "scratch-1", os.Getpid())
			},
			// The carrier worktree itself has no session of its own, so it is
			// also a fault row now. A scratch directory deliberately does not
			// count as the worktree's own session (MatchedOwnBranch is
			// status-backed only), which is exactly what this fixture builds.
			want: map[string]bool{"scratch-1": true, "feature-x": false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepo(t)
			tt.setup(t, repo)

			sessions, err := Discover(repo, DefaultDeps())
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(sessions) != len(tt.want) {
				t.Fatalf("len(sessions) = %d, want %d: %+v", len(sessions), len(tt.want), kindsByName(sessions))
			}
			got := kindsByName(sessions)
			for name, wantScratch := range tt.want {
				gotScratch, ok := got[name]
				if !ok {
					t.Errorf("session %q missing from %+v", name, got)
					continue
				}
				if gotScratch != wantScratch {
					t.Errorf("session %q Scratch = %v, want %v", name, gotScratch, wantScratch)
				}
			}
		})
	}
}

// TestDiscover_ScratchSessionFieldsAreEmptyNotStale pins the whole
// status-derived surface of a scratch entry. The load-bearing assertion is
// StatusStale: a scratch session has no status.json BY DESIGN, and reporting
// it as stale would put it in the same bucket as a session whose status file
// is genuinely broken — a state the dashboard renders as a problem and W4.T1
// treats as recoverable. Everything else must read as the zero value so no
// consumer can act on a phase, branch, or loop count the session never had.
func TestDiscover_ScratchSessionFieldsAreEmptyNotStale(t *testing.T) {
	repo := newTestRepo(t)
	writeScratchHost(t, repo, "scratch-1", os.Getpid())

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "scratch-1")
	if !ok {
		t.Fatalf("scratch-1 not found in %+v", sessions)
	}

	if !got.Scratch {
		t.Error("Scratch = false, want true")
	}
	if got.StatusStale {
		t.Errorf("StatusStale = true, want false (a scratch session has no status.json by design, and is not a broken one)")
	}
	if got.StaleReason != "" {
		t.Errorf("StaleReason = %q, want empty", got.StaleReason)
	}
	if got.WorktreePath != repo {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, repo)
	}
	if got.Phase != "" {
		t.Errorf("Phase = %q, want empty (a scratch session has no lifecycle)", got.Phase)
	}
	if got.Branch != "" {
		t.Errorf("Branch = %q, want empty (a scratch session creates no branch)", got.Branch)
	}
	if got.BaseBranch != "" {
		t.Errorf("BaseBranch = %q, want empty (an empty BaseBranch is what makes diffPanel skip the vs-base diff)", got.BaseBranch)
	}
	if got.SessionID != "" {
		t.Errorf("SessionID = %q, want empty", got.SessionID)
	}
	if got.LoopCount != 0 {
		t.Errorf("LoopCount = %d, want 0", got.LoopCount)
	}
	if got.AwaitingInput {
		t.Error("AwaitingInput = true, want false")
	}
	if got.UpdatedAt != "" {
		t.Errorf("UpdatedAt = %q, want empty", got.UpdatedAt)
	}
	if got.Plan != nil {
		t.Errorf("Plan = %+v, want nil", got.Plan)
	}
	if got.PlanError != "" {
		t.Errorf("PlanError = %q, want empty (no plan file is not an error)", got.PlanError)
	}
	if got.Progress != (Progress{}) {
		t.Errorf("Progress = %+v, want the zero value", got.Progress)
	}
	if got.Verdict != nil {
		t.Errorf("Verdict = %+v, want nil", got.Verdict)
	}
	if got.VerdictError != "" {
		t.Errorf("VerdictError = %q, want empty", got.VerdictError)
	}
}

// TestDiscover_ScratchSessionHostLiveness proves a scratch entry's liveness
// comes from the same host.json + signal-0 path as a status-backed one —
// the only channel a scratch session has, since it has no status.json to
// report a phase from. Both directions are covered: a coincidentally-true
// live case would otherwise pass for the wrong reason.
func TestDiscover_ScratchSessionHostLiveness(t *testing.T) {
	t.Run("live pid", func(t *testing.T) {
		repo := newTestRepo(t)
		writeScratchHost(t, repo, "scratch-1", os.Getpid())

		sessions, err := Discover(repo, DefaultDeps())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		got, ok := findByName(sessions, "scratch-1")
		if !ok {
			t.Fatal("scratch-1 not found")
		}
		if !got.HostRunning {
			t.Error("HostRunning = false, want true (host.json names this test's own PID)")
		}
		if got.HostPID != os.Getpid() {
			t.Errorf("HostPID = %d, want %d", got.HostPID, os.Getpid())
		}
	})

	t.Run("reaped pid", func(t *testing.T) {
		repo := newTestRepo(t)
		dead := knownDeadPID(t)
		writeScratchHost(t, repo, "scratch-1", dead)

		sessions, err := Discover(repo, DefaultDeps())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		got, ok := findByName(sessions, "scratch-1")
		if !ok {
			t.Fatal("scratch-1 not found")
		}
		if got.HostRunning {
			t.Error("HostRunning = true, want false (host.json names a reaped PID)")
		}
		if got.HostPID != dead {
			t.Errorf("HostPID = %d, want %d (still recorded even though not running)", got.HostPID, dead)
		}
		if got.StatusStale {
			t.Error("StatusStale = true, want false (a dead host does not make a scratch session stale)")
		}
	})
}

// TestDiscover_ScratchSessionIgnoresCoincidentalPlanFile pins the deliberate
// asymmetry with buildSessionState: a scratch entry never reads
// `.afk/plans/{name}.md`. A scratch name is allocated from a counter
// (`scratch-N`, W1.T2) with no reference to the plans directory, so a plan
// file of the same name belongs to some unrelated plan — joining it in would
// attribute another plan's progress to a session that has none.
func TestDiscover_ScratchSessionIgnoresCoincidentalPlanFile(t *testing.T) {
	repo := newTestRepo(t)
	writeScratchHost(t, repo, "scratch-1", os.Getpid())
	writePlan(t, repo, "scratch-1", twoTaskPlan)

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, ok := findByName(sessions, "scratch-1")
	if !ok {
		t.Fatal("scratch-1 not found")
	}
	if got.Plan != nil {
		t.Errorf("Plan = %+v, want nil (a same-named plan file belongs to a different session)", got.Plan)
	}
	if got.Progress != (Progress{}) {
		t.Errorf("Progress = %+v, want the zero value", got.Progress)
	}
}

// TestDiscover_ScratchSessionsSortWithNormalOnes guards the single ordering
// contract Discover has ever made (sorted by Name) against the second
// append happening after the first loop's: appending scratch entries in a
// second pass must not leave the result grouped by source.
func TestDiscover_ScratchSessionsSortWithNormalOnes(t *testing.T) {
	repo := newTestRepo(t)
	writeScratchHost(t, repo, "aaa-scratch", os.Getpid())
	writeStatus(t, repo, "mmm-normal", statusOpts{phase: state.PhaseExecuting})
	writeScratchHost(t, repo, "zzz-scratch", os.Getpid())

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var names []string
	for _, s := range sessions {
		names = append(names, s.Name)
	}
	want := []string{"aaa-scratch", "mmm-normal", "zzz-scratch"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

// --- label passthrough --------------------------------------------------

// TestDiscover_LabelPassthrough closes the one committed-coverage gap W4.T1's
// own QA flagged: applyLabel (discover.go) wires internal/session.ReadLabel
// into SessionState.Label, and prior to this test go test stayed green even
// with applyLabel mutated into a no-op — nothing in this package exercised
// Discover against a real label file on disk. Every row here is load-bearing
// for that reason:
//
//   - "status-backed session with a label": buildSessionState's call to
//     applyLabel. Reverting that call to a no-op fails this row's Label
//     assertion.
//   - "scratch session with a label": buildScratchSessionState has its OWN
//     separate applyLabel call site; a fix that patches only
//     buildSessionState would still pass a test that only covers that row,
//     so the scratch path needs its own assertion.
//   - "no label file is empty, not an error, not stale": the negative this
//     package's standing rule requires — a missing sidecar must never
//     surface as StatusStale or as an error. ReadLabel's own absent-case is
//     already unit-tested in internal/session/label_test.go; this confirms
//     Discover doesn't turn that absence into something else on the way
//     through.
func TestDiscover_LabelPassthrough(t *testing.T) {
	t.Run("status-backed session with a label", func(t *testing.T) {
		repo := newTestRepo(t)
		writeStatus(t, repo, "labeled-session", statusOpts{phase: state.PhaseExecuting})
		if err := session.WriteLabel(repo, "labeled-session", "spike: pty timing"); err != nil {
			t.Fatalf("WriteLabel: %v", err)
		}

		sessions, err := Discover(repo, DefaultDeps())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		got, ok := findByName(sessions, "labeled-session")
		if !ok {
			t.Fatal("labeled-session not found")
		}
		if got.Label != "spike: pty timing" {
			t.Errorf("Label = %q, want %q", got.Label, "spike: pty timing")
		}
	})

	t.Run("scratch session with a label", func(t *testing.T) {
		repo := newTestRepo(t)
		writeScratchHost(t, repo, "scratch-1", os.Getpid())
		if err := session.WriteLabel(repo, "scratch-1", "long-running spike"); err != nil {
			t.Fatalf("WriteLabel: %v", err)
		}

		sessions, err := Discover(repo, DefaultDeps())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		got, ok := findByName(sessions, "scratch-1")
		if !ok {
			t.Fatal("scratch-1 not found")
		}
		if !got.Scratch {
			t.Error("Scratch = false, want true")
		}
		if got.Label != "long-running spike" {
			t.Errorf("Label = %q, want %q", got.Label, "long-running spike")
		}
	})

	t.Run("no label file is empty, not an error, not stale", func(t *testing.T) {
		repo := newTestRepo(t)
		writeStatus(t, repo, "unlabeled-session", statusOpts{phase: state.PhaseExecuting})

		sessions, err := Discover(repo, DefaultDeps())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		got, ok := findByName(sessions, "unlabeled-session")
		if !ok {
			t.Fatal("unlabeled-session not found")
		}
		if got.Label != "" {
			t.Errorf("Label = %q, want empty", got.Label)
		}
		if got.StatusStale {
			t.Error("StatusStale = true, want false (a missing label is not a staleness signal)")
		}
	})
}

// --- provider passthrough -----------------------------------------------

// TestDiscover_ProviderPassthrough pins SessionState.Provider, wired from
// internal/session.ReadProvider by applyProvider. Load-bearing rows:
//
//   - "status-backed codex": buildSessionState's applyProvider call.
//   - "scratch codex": buildScratchSessionState has its own call site.
//   - "stopped host": the sidecar, not host.json, is the source, so a dead
//     host pid must still report the provider.
//   - "claude sidecar canonicalizes to empty": consumers rely on "" meaning
//     default; an explicit "claude" must not leak through as a tag.
//   - "absent": no sidecar is "", never stale.
func TestDiscover_ProviderPassthrough(t *testing.T) {
	setProvider := func(t *testing.T, repo, name, p string) {
		t.Helper()
		dir := filepath.Join(repo, ".afk", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := session.WriteProviderMarker(dir, p); err != nil {
			t.Fatalf("WriteProviderMarker: %v", err)
		}
	}
	discoverOne := func(t *testing.T, repo, name string) SessionState {
		t.Helper()
		sessions, err := Discover(repo, DefaultDeps())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		got, ok := findByName(sessions, name)
		if !ok {
			t.Fatalf("%s not found", name)
		}
		return got
	}

	t.Run("status-backed codex", func(t *testing.T) {
		repo := newTestRepo(t)
		writeStatus(t, repo, "cx", statusOpts{phase: state.PhaseExecuting})
		setProvider(t, repo, "cx", "codex")
		if got := discoverOne(t, repo, "cx"); got.Provider != "codex" {
			t.Errorf("Provider = %q, want codex", got.Provider)
		}
	})

	t.Run("scratch codex", func(t *testing.T) {
		repo := newTestRepo(t)
		writeScratchHost(t, repo, "cx-scratch", os.Getpid())
		setProvider(t, repo, "cx-scratch", "codex")
		got := discoverOne(t, repo, "cx-scratch")
		if !got.Scratch {
			t.Error("Scratch = false, want true")
		}
		if got.Provider != "codex" {
			t.Errorf("Provider = %q, want codex", got.Provider)
		}
	})

	t.Run("stopped host still reports provider", func(t *testing.T) {
		repo := newTestRepo(t)
		writeScratchHost(t, repo, "cx-dead", 0)
		setProvider(t, repo, "cx-dead", "codex")
		got := discoverOne(t, repo, "cx-dead")
		if got.HostRunning {
			t.Fatal("HostRunning = true, want false for this row to mean anything")
		}
		if got.Provider != "codex" {
			t.Errorf("Provider = %q, want codex", got.Provider)
		}
	})

	t.Run("claude sidecar canonicalizes to empty", func(t *testing.T) {
		repo := newTestRepo(t)
		writeStatus(t, repo, "cl", statusOpts{phase: state.PhaseExecuting})
		setProvider(t, repo, "cl", "claude")
		if got := discoverOne(t, repo, "cl"); got.Provider != "" {
			t.Errorf("Provider = %q, want empty", got.Provider)
		}
	})

	t.Run("absent sidecar is empty and not stale", func(t *testing.T) {
		repo := newTestRepo(t)
		writeStatus(t, repo, "none", statusOpts{phase: state.PhaseExecuting})
		got := discoverOne(t, repo, "none")
		if got.Provider != "" {
			t.Errorf("Provider = %q, want empty", got.Provider)
		}
		if got.StatusStale {
			t.Error("StatusStale = true, want false")
		}
	})
}

// --- autonomous passthrough ---------------------------------------------

// TestDiscover_AutonomousPassthrough pins SessionState.Autonomous, wired from
// internal/session.IsAutonomous in buildSessionState. Load-bearing rows:
//
//   - "marker present": the call site itself. Dropping it fails this row.
//   - "stale status.json with marker": the call must sit BEFORE the stale
//     early-return, like applyLabel. Moving it below that return leaves a
//     stale session reporting false while its conductor is still autonomous.
//   - "scratch session with marker": buildScratchSessionState must NOT call
//     it; a scratch session has no conductor, so a stray marker is not a mode.
//   - "no marker": absence is false, never an error or a staleness signal.
func TestDiscover_AutonomousPassthrough(t *testing.T) {
	marker := func(t *testing.T, repo, name string) {
		t.Helper()
		dir := filepath.Join(repo, ".afk", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := session.WriteAutonomousMarker(dir); err != nil {
			t.Fatalf("WriteAutonomousMarker: %v", err)
		}
	}
	discoverOne := func(t *testing.T, repo, name string) SessionState {
		t.Helper()
		sessions, err := Discover(repo, DefaultDeps())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		got, ok := findByName(sessions, name)
		if !ok {
			t.Fatalf("%s not found", name)
		}
		return got
	}

	t.Run("marker present", func(t *testing.T) {
		repo := newTestRepo(t)
		writeStatus(t, repo, "auto-session", statusOpts{phase: state.PhaseExecuting})
		marker(t, repo, "auto-session")
		if got := discoverOne(t, repo, "auto-session"); !got.Autonomous {
			t.Error("Autonomous = false, want true")
		}
	})

	t.Run("no marker", func(t *testing.T) {
		repo := newTestRepo(t)
		writeStatus(t, repo, "manual-session", statusOpts{phase: state.PhaseExecuting})
		got := discoverOne(t, repo, "manual-session")
		if got.Autonomous {
			t.Error("Autonomous = true, want false")
		}
		if got.StatusStale {
			t.Error("StatusStale = true, want false (a missing marker is not a staleness signal)")
		}
	})

	t.Run("scratch session with marker", func(t *testing.T) {
		repo := newTestRepo(t)
		writeScratchHost(t, repo, "scratch-auto", os.Getpid())
		marker(t, repo, "scratch-auto")
		got := discoverOne(t, repo, "scratch-auto")
		if !got.Scratch {
			t.Fatal("Scratch = false, want true")
		}
		if got.Autonomous {
			t.Error("Autonomous = true, want false for a scratch session")
		}
	})

	t.Run("stale status.json with marker", func(t *testing.T) {
		repo := newTestRepo(t)
		writeRawStatus(t, repo, "stale-auto", "{ not json")
		marker(t, repo, "stale-auto")
		got := discoverOne(t, repo, "stale-auto")
		if !got.StatusStale {
			t.Fatal("StatusStale = false, want true (fixture must be unparseable)")
		}
		if !got.Autonomous {
			t.Error("Autonomous = false, want true on a stale session")
		}
	})
}

// --- progressFromPlan (pure, no filesystem needed) --------------------------

func TestProgressFromPlan(t *testing.T) {
	plan := &state.Plan{
		Title: "t",
		Waves: []state.Wave{
			{
				Number: 1,
				Name:   "w1",
				Tasks: []state.Task{
					{ID: "W1.T1", State: state.CheckComplete},
					{ID: "W1.T2", State: state.CheckFailed, FailureReason: "boom"},
					{ID: "W1.T3", State: state.CheckIncomplete},
				},
			},
			{
				Number: 2,
				Name:   "w2",
				Tasks: []state.Task{
					{ID: "W2.T1", State: state.CheckIncomplete},
				},
			},
		},
	}

	got := progressFromPlan(plan)
	want := Progress{Total: 4, Complete: 1, Failed: 1, Incomplete: 2}
	if got != want {
		t.Errorf("progressFromPlan() = %+v, want %+v", got, want)
	}
}

func TestProgressFromPlan_NoWaves(t *testing.T) {
	got := progressFromPlan(&state.Plan{Title: "empty"})
	if got != (Progress{}) {
		t.Errorf("progressFromPlan(empty plan) = %+v, want the zero value", got)
	}
}

// TestBuildSessionStateReadsHostMetaTwice pins buildSessionState's original
// host.json read timing, which a since-reverted refactor collapsed into a
// single early call. host.json's content can't be made to change *during*
// buildSessionState from a black-box test — there is no seam to hook
// mid-function — so this asserts the invariant the timing depends on
// directly from the source: applyHostMeta (this package's sole host.json
// reader) must be called twice in buildSessionState, once before
// state.ReadStatus (to cover the STALE early-return path) and once after
// the plan/verdict parsing (what the non-stale return actually reports).
// Collapsing to one call — regardless of which one survives — silently
// moves the non-stale path's liveness snapshot earlier in the tick, which
// is exactly the regression this guards: a single call still passes every
// other assertion in this file (host.json's content never changes within a
// single synchronous test), so only a check on the call site count catches
// it.
func TestBuildSessionStateReadsHostMetaTwice(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	srcPath := filepath.Join(filepath.Dir(thisFile), "discover.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", srcPath, err)
	}

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "buildSessionState" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatalf("%s: buildSessionState not found", srcPath)
	}

	calls := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "applyHostMeta" {
			calls++
		}
		return true
	})

	if calls != 2 {
		t.Errorf("buildSessionState calls applyHostMeta %d time(s), want exactly 2 (once before the status result is consumed, once after plan/verdict parsing)", calls)
	}
}

// --- ordering: the total order internal/session also uses -------------------

// orderKey reduces a discovery result to the (Name, Scratch) pair the
// ordering contract is expressed in. Both packages' Discover return different
// struct types, so this is the shared shape their orderings are compared in.
type orderKey struct {
	name    string
	scratch bool
}

func stateOrder(sessions []SessionState) []orderKey {
	keys := make([]orderKey, 0, len(sessions))
	for _, s := range sessions {
		keys = append(keys, orderKey{name: s.Name, scratch: s.Scratch})
	}
	return keys
}

func sessionOrder(sessions []session.Session) []orderKey {
	keys := make([]orderKey, 0, len(sessions))
	for _, s := range sessions {
		keys = append(keys, orderKey{name: s.Name, scratch: s.Scratch})
	}
	return keys
}

func sameOrder(a, b []orderKey) bool {
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

// TestSortSessionStatesIsTotalOrder pins sortSessionStates to a key total
// enough that no two distinct entries ever compare equal — the property
// internal/session's sortSessions already has and this package lacked.
//
// Every row feeds the SAME set of entries in two different input orders and
// requires one output. That shape is the whole point: sort.Slice is documented
// as not stable, so a comparator that leaves a pair equal returns whichever
// order the sort happened to land on, and the callers that resolve a session
// by taking the FIRST name match (attach.go's findSessionWorktree,
// internal/ui's resolveFocusTarget/sessionByName) then aim at a different
// session depending on nothing the user can see.
//
// Load-bearing rows:
//   - "scratch and status-backed share a name" — the collision the fix exists
//     for, and the row a Name-only comparator fails: with two elements
//     sort.Slice runs an insertion sort, which leaves an equal-comparing pair
//     in input order, so the two input orders produce two different outputs.
//   - "same name, same kind, different worktrees" — the WorktreePath
//     tie-break; without it the pair is equal-comparing too.
//   - "distinct names" — the pre-existing name ordering, unchanged.
func TestSortSessionStatesIsTotalOrder(t *testing.T) {
	tests := []struct {
		name  string
		input []SessionState
		want  []orderKey
	}{
		{
			name: "scratch and status-backed share a name",
			input: []SessionState{
				{Name: "dup", WorktreePath: "/repo", Scratch: true},
				{Name: "dup", WorktreePath: "/repo/wt-dup"},
			},
			want: []orderKey{{"dup", false}, {"dup", true}},
		},
		{
			name: "same name, same kind, different worktrees",
			input: []SessionState{
				{Name: "dup", WorktreePath: "/repo/b"},
				{Name: "dup", WorktreePath: "/repo/a"},
			},
			want: []orderKey{{"dup", false}, {"dup", false}},
		},
		{
			name: "distinct names",
			input: []SessionState{
				{Name: "zzz", WorktreePath: "/repo", Scratch: true},
				{Name: "aaa", WorktreePath: "/repo"},
			},
			want: []orderKey{{"aaa", false}, {"zzz", true}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forward := append([]SessionState(nil), tc.input...)
			sortSessionStates(forward)

			reversed := make([]SessionState, 0, len(tc.input))
			for i := len(tc.input) - 1; i >= 0; i-- {
				reversed = append(reversed, tc.input[i])
			}
			sortSessionStates(reversed)

			if !sameOrder(stateOrder(forward), tc.want) {
				t.Errorf("forward order = %v, want %v", stateOrder(forward), tc.want)
			}
			if !sameOrder(stateOrder(reversed), tc.want) {
				t.Errorf("reversed order = %v, want %v", stateOrder(reversed), tc.want)
			}
			// The WorktreePath tie-break is invisible in orderKey (both
			// entries reduce to the same pair), so assert it directly.
			for i := range forward {
				if forward[i].WorktreePath != reversed[i].WorktreePath {
					t.Errorf("entry %d worktree differs by input order: %q vs %q",
						i, forward[i].WorktreePath, reversed[i].WorktreePath)
				}
			}
		})
	}
}

// TestSortSessionStatesBreaksKindTies covers the one tie orderKey cannot see:
// two entries agreeing on name, scratch-ness and worktree path and differing
// only in Kind. It is asserted on the Kind sequence rather than through
// stateOrder for exactly that reason — a name-only comparator satisfies every
// row of TestSortSessionStatesIsTotalOrder for this pair.
//
// Discover does not emit this pair today (a faulted worktree is by definition
// one with no status-backed session of its own). The comparator still has to
// be total without depending on that, which is a property of a different
// function and one a later change to it would silently take away.
func TestSortSessionStatesBreaksKindTies(t *testing.T) {
	pair := []SessionState{
		{Name: "dup", WorktreePath: "/repo/wt-dup", Kind: session.KindWorktreeNoSession},
		{Name: "dup", WorktreePath: "/repo/wt-dup"},
	}

	kinds := func(in []SessionState) []session.Kind {
		sorted := append([]SessionState(nil), in...)
		sortSessionStates(sorted)
		out := make([]session.Kind, 0, len(sorted))
		for _, s := range sorted {
			out = append(out, s.Kind)
		}
		return out
	}

	forward := kinds(pair)
	reversed := kinds([]SessionState{pair[1], pair[0]})
	if forward[0] == forward[1] {
		t.Fatalf("fixture kinds are identical (%v); the tie-break cannot be observed", forward)
	}
	if forward[0] != reversed[0] || forward[1] != reversed[1] {
		t.Fatalf("input order changed the output: forward = %v, reversed = %v — "+
			"sort.Slice is not stable, so an equal-comparing pair lands wherever the sort left it",
			forward, reversed)
	}
}

// TestDiscoverOrdersNameCollisionsLikeSessionDiscover is the cross-package pin
// the two comparators need and cannot share.
//
// internal/session.sortSessions and this package's sortSessionStates order the
// same real-world entities, and every caller that resolves a name to a session
// takes the first match — `afk clean dup` through internal/session,
// `afk attach dup` and the dashboard's click-to-focus through this package.
// If the two orderings disagree, one name resolves to two different sessions
// with two different removal paths depending only on which package asked.
//
// They cannot share one implementation: the comparator's inputs are
// session.Session and discover.SessionState, unrelated structs in packages
// that cannot both depend on a common one without internal/session growing an
// import it has no other use for (internal/discover already imports
// internal/session, never the reverse). So the invariant is pinned by test
// instead — and pinned to a LITERAL expected sequence rather than to "the two
// agree", because two comparators that regress together would still agree.
//
// The fixture is the collision itself: a scratch `.afk/dup/` in the main
// checkout and a linked worktree checked out on branch `dup` carrying its own
// `.afk/dup/status.json`. Both are legitimate on-disk states, and both
// packages report both entries under the name `dup`.
func TestDiscoverOrdersNameCollisionsLikeSessionDiscover(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "dup")
	writeStatus(t, wt, "dup", statusOpts{phase: state.PhaseExecuting})
	writeScratchHost(t, repo, "dup", os.Getpid())

	// The status-backed entry first: its name IS a branch name, it is the only
	// one of the pair whose clean has a worktree to remove, and it is what
	// internal/session's sortSessions already picks.
	want := []orderKey{{"dup", false}, {"dup", true}}

	states, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("discover.Discover: %v", err)
	}
	if got := stateOrder(states); !sameOrder(got, want) {
		t.Errorf("discover.Discover order = %v, want %v", got, want)
	}

	sessions, err := session.Discover(repo, session.CleanDeps{})
	if err != nil {
		t.Fatalf("session.Discover: %v", err)
	}
	if got := sessionOrder(sessions); !sameOrder(got, want) {
		t.Errorf("session.Discover order = %v, want %v", got, want)
	}
}

// --- package surface guarantees ---------------------------------------------

// parseDiscoverSource parses discover.go (comments included) from the same
// directory as this test file.
func parseDiscoverSource(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	srcPath := filepath.Join(filepath.Dir(thisFile), "discover.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing %s: %v", srcPath, err)
	}
	return fset, f
}

func findFuncDecl(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == name {
			return d
		}
	}
	t.Fatalf("discover.go: %s not found", name)
	return nil
}

// TestDiscoverNeverBlocks mechanically enforces the guarantee the package doc
// states: Discover runs on the dashboard's 2s refresh ticker, so nothing in
// this package may dial, connect, or perform any IPC/network operation.
//
// It is an allowlist, not a denylist, and deliberately so: a denylist of known
// blocking APIs is only ever as current as the last person who thought to
// extend it, while an allowlist makes any new dependency fail here until
// someone re-confirms it cannot stall the ticker. Extending either list is the
// intended way to add a dependency — the failure is a prompt, not a veto.
//
// Load-bearing assertions:
//   - imports: `net`, `net/http`, or anything else that could open a
//     connection is absent by construction, since it is not in the allowlist.
//   - internal/session selectors: this package imports the one package that
//     also owns `git worktree remove` and every other git-mutating teardown
//     step, for one plain-file-read function and the shared fault-kind
//     vocabulary. Pinning the selector set is what keeps that import from
//     quietly widening into the mutating surface.
//   - internal/host selectors: ReadMeta (os.ReadFile) and Alive (signal-0)
//     are the sanctioned non-file check. host.Dial is the exact call this
//     assertion exists to keep out.
func TestDiscoverNeverBlocks(t *testing.T) {
	_, f := parseDiscoverSource(t)

	allowedImports := map[string]bool{
		"bytes":                                 true,
		"errors":                                true,
		"fmt":                                   true,
		"os":                                    true,
		"os/exec":                               true, // `git worktree list --porcelain` only; see the package doc.
		"path/filepath":                         true,
		"sort":                                  true,
		"strings":                               true,
		"github.com/dphaener/afk/internal/host": true,
		// internal/provider does no I/O of its own (its Preflight and Usage take
		// injected runners); discover uses only its Default constant to canonicalize claude to "".
		"github.com/dphaener/afk/internal/provider": true,
		"github.com/dphaener/afk/internal/session":  true,
		"github.com/dphaener/afk/internal/state":    true,
		// internal/worktree is the shared observation both scanners read
		// from. It carries the same never-block guarantee, enforced in its
		// own package by TestObserveNeverBlocks: file reads, globs and stats
		// only, with the one `git worktree list` subprocess left on this
		// side of its injected RunFunc.
		"github.com/dphaener/afk/internal/worktree": true,
	}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("unquoting import %s: %v", imp.Path.Value, err)
		}
		if !allowedImports[path] {
			t.Errorf("discover.go imports %q, which is not on the never-block allowlist; "+
				"confirm it cannot dial, connect, or otherwise stall the dashboard's 2s ticker, then add it", path)
		}
	}

	selectors := func(pkg string) map[string]bool {
		used := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == pkg {
				used[sel.Sel.Name] = true
			}
			return true
		})
		return used
	}

	// ReadLabel is the one FUNCTION; Kind and its constants are a type and
	// compile-time values, carrying no runtime surface at all. They are on
	// the list because the fault vocabulary has exactly one definition and it
	// lives in the package that acts on it — duplicating the enum here would
	// let the two copies drift, and the dashboard's clean-target match reads
	// both.
	allowedSession := map[string]bool{
		"ReadLabel": true,
		// ReadProvider is a single os.ReadFile of the provider sidecar, the
		// same plain-file-read shape as ReadLabel; no dial, no git.
		"ReadProvider": true,
		// IsAutonomous is a single os.Stat of the marker file, no read, no
		// dial.
		"IsAutonomous": true,
		"Kind":         true, "KindStaleStatus": true,
		"KindWorktreeMissing": true, "KindWorktreeNoSession": true,
		"KindStatusWithoutWorktree": true,
	}
	for name := range selectors("session") {
		if !allowedSession[name] {
			t.Errorf("discover.go references session.%s; internal/session also owns git-mutating teardown, "+
				"so only plain-file-read helpers belong here", name)
		}
	}

	allowedHost := map[string]bool{"ReadMeta": true, "Alive": true}
	for name := range selectors("host") {
		if !allowedHost[name] {
			t.Errorf("discover.go references host.%s; only ReadMeta (a file read) and Alive (signal-0) "+
				"are sanctioned here — host.Dial and friends can stall the ticker", name)
		}
	}

	// The same mechanical enforcement, extended to the shared observation.
	// internal/worktree exists to be the one place two packages look at
	// disk, which makes it the natural place for a future fact that needs a
	// subprocess or a dial. Pinning the selector set means this package finds
	// out here rather than on the dashboard's ticker.
	allowedWorktree := map[string]bool{
		"List": true, "Observe": true, "Deps": true,
		"Worktree": true, "Observation": true, "AfkDir": true,
	}
	for name := range selectors("worktree") {
		if !allowedWorktree[name] {
			t.Errorf("discover.go references worktree.%s; confirm it performs no dial, IPC or subprocess "+
				"of its own, then add it", name)
		}
	}
}

// TestApplyHostMetaDocCommentMatchesCallSites checks applyHostMeta's doc
// comment against what buildSessionState actually does.
//
// The comment is the only thing that explains why applyHostMeta is called
// twice, and it claimed both call sites run "BEFORE any status parsing" — true
// of the first, false of the second, which deliberately runs after
// status/plan/verdict parsing so the reported liveness snapshot is as of the
// end of the tick. A comment asserting a mechanism the code does not have is
// worse than no comment, and nothing else in the suite reads comments, so this
// is the only place that regression can be caught.
//
// It first re-derives the fact from the AST (one call before state.ReadStatus,
// one after) so the comment is checked against the code rather than against a
// hardcoded belief about it, then rejects any sentence that quantifies over
// all call sites and claims "before" without acknowledging "after". That
// heuristic is lexical and therefore gameable in principle; it is not gameable
// by accident, which is what a comment-drift guard needs to be.
func TestApplyHostMetaDocCommentMatchesCallSites(t *testing.T) {
	_, f := parseDiscoverSource(t)

	build := findFuncDecl(t, f, "buildSessionState")
	var statusPos token.Pos
	var metaCalls []token.Pos
	ast.Inspect(build.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if fn, ok := node.Fun.(*ast.Ident); ok && fn.Name == "applyHostMeta" {
				metaCalls = append(metaCalls, node.Pos())
			}
		case *ast.SelectorExpr:
			// The status read moved into internal/worktree.Observe, so the
			// anchor is now where buildSessionState CONSUMES that parse
			// result. The ordering property it marks is unchanged: one
			// liveness read brackets the status handling so the stale
			// early-return still carries it, and one runs after plan/verdict
			// parsing so the non-stale return reports liveness as of the end
			// of the tick.
			if x, ok := node.X.(*ast.Ident); ok && x.Name == "fd" && node.Sel.Name == "StatusSchema" {
				statusPos = node.Pos()
			}
		}
		return true
	})
	if statusPos == token.NoPos {
		t.Fatal("buildSessionState: fd.StatusSchema (the observed status parse result) not consumed")
	}
	var before, after int
	for _, pos := range metaCalls {
		if pos < statusPos {
			before++
		} else {
			after++
		}
	}
	if before != 1 || after != 1 {
		t.Fatalf("buildSessionState calls applyHostMeta %d time(s) before consuming fd.StatusSchema and %d after, want 1 and 1", before, after)
	}

	doc := findFuncDecl(t, f, "applyHostMeta").Doc
	if doc == nil {
		t.Fatal("applyHostMeta has no doc comment")
	}
	text := strings.ToLower(strings.Join(strings.Fields(doc.Text()), " "))
	quantifiers := []string{"both callers", "both call sites", "each caller", "every caller", "all callers", "both calls"}
	for _, sentence := range strings.Split(text, ". ") {
		hasQuantifier := false
		for _, q := range quantifiers {
			if strings.Contains(sentence, q) {
				hasQuantifier = true
				break
			}
		}
		if !hasQuantifier {
			continue
		}
		if strings.Contains(sentence, "before") && !strings.Contains(sentence, "after") {
			t.Errorf("applyHostMeta's doc comment claims every call site runs before status parsing "+
				"(%q), but buildSessionState's second call deliberately runs after status/plan/verdict parsing",
				sentence)
		}
	}
}

// --- fault kinds ------------------------------------------------------------

// TestDiscover_Kind pins this package's half of the shared fault vocabulary.
//
// The vocabulary is shared with internal/session; the two packages now AGREE
// on identity (both label a carrier mismatch KindStatusWithoutWorktree) and
// differ only in how the row RENDERS. Load-bearing rows:
//   - "standalone session in the main checkout" and "corrupt status.json in a
//     worktree checked out on another branch" both carry
//     KindStatusWithoutWorktree — a live `/afk:go` session with a real
//     phase in one case, a corrupt file in the other — because the kind is
//     keyed on the carrier/name mismatch, not on the status file's validity.
//     It still renders as an ordinary, unfaulted row (see
//     TestDiscover_WorktreeSideFaultsAreRows); only the CLEAN identity
//     changes.
//   - "unknown field", "trailing data", and "malformed json" are seeded inside
//     a worktree checked out on branch "sess" itself, so the carrier matches
//     the name and these pin plain KindStaleStatus, not the mismatch
//     override — the parser-disagreement contract internal/ui's
//     dashboardPhase depends on (both decode cleanly for internal/session;
//     trailing data is the sharper case, since internal/session accepts it
//     outright).
//   - "scratch" — KindHealthy with StatusStale FALSE. A one-off's missing
//     status.json is its normal shape, not a fault, and the kind must not
//     quietly reintroduce the staleness the Scratch field exists to deny.
func TestDiscover_Kind(t *testing.T) {
	const validBody = `"session_id": "11111111-1111-1111-1111-111111111111",
  "plan_name": "sess",
  "phase": "executing",
  "branch": "sess",
  "base_branch": "main",
  "loop_count": 0,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": "2026-07-09T21:14:03Z"`

	cases := []struct {
		name        string
		seed        func(t *testing.T, repo string)
		wantKind    session.Kind
		wantStale   bool
		wantScratch bool
		wantPhase   state.Phase
	}{
		{
			name: "healthy session in its own worktree",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "sess")
				writeStatus(t, wt, "sess", statusOpts{phase: state.PhaseExecuting})
			},
			wantKind:  session.KindHealthy,
			wantPhase: state.PhaseExecuting,
		},
		{
			name: "standalone session in the main checkout",
			seed: func(t *testing.T, repo string) {
				writeStatus(t, repo, "sess", statusOpts{phase: state.PhaseExecuting})
			},
			wantKind:  session.KindStatusWithoutWorktree,
			wantPhase: state.PhaseExecuting,
		},
		{
			// Seeded in the primary checkout (branch "main"), like the
			// standalone-session row above, rather than a second linked
			// worktree: a linked worktree checked out on a branch that does
			// not itself match "sess" would ALSO produce that worktree's own
			// KindWorktreeNoSession fault row, which would break this table's
			// single-row assertion for a fact unrelated to what this case
			// tests (the mismatch override applying to a corrupt file too).
			name: "corrupt status.json in a worktree checked out on another branch",
			seed: func(t *testing.T, repo string) {
				writeRawStatus(t, repo, "sess", "{not json")
			},
			wantKind:  session.KindStatusWithoutWorktree,
			wantStale: true,
		},
		{
			// Re-seeded inside a worktree checked out on branch "sess" itself
			// (not the primary checkout) so the carrier branch matches the
			// name and this pins KindStaleStatus, not the mismatch override —
			// the parser-disagreement contract internal/ui's dashboardPhase
			// depends on.
			name: "unknown field",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "sess")
				writeRawStatus(t, wt, "sess", "{\n  "+validBody+",\n  \"boom\": 1\n}")
			},
			wantKind:  session.KindStaleStatus,
			wantStale: true,
		},
		{
			name: "trailing data",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "sess")
				writeRawStatus(t, wt, "sess", "{\n  "+validBody+"\n}\n{}")
			},
			wantKind:  session.KindStaleStatus,
			wantStale: true,
		},
		{
			name: "malformed json",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "sess")
				writeRawStatus(t, wt, "sess", "{not json")
			},
			wantKind:  session.KindStaleStatus,
			wantStale: true,
		},
		{
			name: "scratch one-off",
			seed: func(t *testing.T, repo string) {
				writeScratchHost(t, repo, "sess", os.Getpid())
			},
			wantKind:    session.KindHealthy,
			wantScratch: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			tc.seed(t, repo)

			sessions, err := Discover(repo, DefaultDeps())
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(sessions) != 1 {
				t.Fatalf("len(sessions) = %d, want 1: %+v", len(sessions), sessions)
			}
			got := sessions[0]
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.StatusStale != tc.wantStale {
				t.Errorf("StatusStale = %v, want %v", got.StatusStale, tc.wantStale)
			}
			if got.Scratch != tc.wantScratch {
				t.Errorf("Scratch = %v, want %v", got.Scratch, tc.wantScratch)
			}
			if got.Phase != tc.wantPhase {
				t.Errorf("Phase = %q, want %q", got.Phase, tc.wantPhase)
			}
		})
	}
}

// TestDiscover_StandaloneSessionAgreesWithSessionDiscover states the
// cross-package agreement on IDENTITY as an executable fact rather than a
// comment, from ONE fixture read by both scanners.
//
// A status.json in the main checkout with no worktree of its own is a live
// standalone `/afk:go` session. Both packages now label it
// KindStatusWithoutWorktree — that is what lets the dashboard hand a row to
// `afk clean` by (name, kind) and reach the right teardown — but they still
// differ in how the row RENDERS: internal/discover keeps it an ordinary,
// unfaulted row with a real phase (a live session must not blank out on the
// dashboard), while internal/session's Session carries an empty Branch and
// the carrier as WorktreePath, which is what routes its clean to the zero-git
// teardown instead of `cleanNormalSession`.
func TestDiscover_StandaloneSessionAgreesWithSessionDiscover(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "solo", statusOpts{phase: state.PhaseExecuting})

	rows, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("discover.Discover: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "solo" || rows[0].Kind != session.KindStatusWithoutWorktree {
		t.Fatalf("discover.Discover = %+v, want one row named solo with kind %q", rows, session.KindStatusWithoutWorktree)
	}
	if rows[0].Phase != state.PhaseExecuting {
		t.Errorf("Phase = %q, want %q — a standalone session's detail pane depends on it",
			rows[0].Phase, state.PhaseExecuting)
	}

	sessions, err := session.Discover(repo, session.CleanDeps{})
	if err != nil {
		t.Fatalf("session.Discover: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Name != "solo" || sessions[0].Kind != session.KindStatusWithoutWorktree {
		t.Fatalf("session.Discover = %+v, want one Session named solo with kind %q", sessions, session.KindStatusWithoutWorktree)
	}
	if sessions[0].Branch != "" {
		t.Errorf("Branch = %q, want \"\" — a standalone session's Branch must never be its carrier's own branch", sessions[0].Branch)
	}
}

// --- worktree-side faults ----------------------------------------------------

// faultRow is the shape TestDiscover_WorktreeSideFaultsAreRows compares in:
// the identity a dashboard row is resolved by, and nothing else.
type faultRow struct {
	name    string
	kind    session.Kind
	scratch bool
}

func faultRows(sessions []SessionState) []faultRow {
	rows := make([]faultRow, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, faultRow{name: s.Name, kind: s.Kind, scratch: s.Scratch})
	}
	return rows
}

// TestDiscover_WorktreeSideFaultsAreRows pins this package's ROW POLICY: the
// dashboard surfaces the two WORKTREE-SIDE faults as rows of their own, and
// nothing else.
//
// Load-bearing rows, each for a different reason:
//   - "worktree directory deleted" and "worktree with no session" are the two
//     faults that exist at all. They are the whole feature: without them a
//     session whose directory was deleted is invisible to the dashboard and
//     therefore uncleanable from it.
//   - "standalone session in the main checkout" is the row this package must
//     NOT fault. internal/session labels that same shape
//     KindStatusWithoutWorktree for CLEAN identity, but faulting the row here
//     too would blank the dashboard for every standalone session, which is
//     why the fault policy stays worktree-side ONLY — the kind can be shared
//     without the rendering being shared.
//   - "healthy session in its own worktree" is the MatchedOwnBranch
//     regression: drop that clause and every healthy session grows a
//     duplicate fault row of itself.
//   - "primary checkout with no session" and "detached worktree" are the two
//     shapes that have no branch to name and must never be flagged — the
//     primary checkout is the user's own repo.
//   - "scratch directory named after the carrier branch" is the collision
//     internal/session's Discover already reports two entries for
//     (MatchedOwnBranch is status-backed only, deliberately), and the reason
//     the dashboard's clean target cannot be matched on name alone.
func TestDiscover_WorktreeSideFaultsAreRows(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, repo string)
		want []faultRow
	}{
		{
			name: "worktree directory deleted",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "gone")
				writeStatus(t, wt, "gone", statusOpts{phase: state.PhaseExecuting})
				if err := os.RemoveAll(wt); err != nil {
					t.Fatalf("removing worktree dir: %v", err)
				}
			},
			want: []faultRow{{name: "gone", kind: session.KindWorktreeMissing}},
		},
		{
			name: "worktree with no session",
			seed: func(t *testing.T, repo string) {
				addWorktree(t, repo, "stranger")
			},
			want: []faultRow{{name: "stranger", kind: session.KindWorktreeNoSession}},
		},
		{
			name: "standalone session in the main checkout",
			seed: func(t *testing.T, repo string) {
				writeStatus(t, repo, "solo", statusOpts{phase: state.PhaseExecuting})
			},
			want: []faultRow{{name: "solo", kind: session.KindStatusWithoutWorktree}},
		},
		{
			name: "healthy session in its own worktree",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "sess")
				writeStatus(t, wt, "sess", statusOpts{phase: state.PhaseExecuting})
			},
			want: []faultRow{{name: "sess", kind: session.KindHealthy}},
		},
		{
			name: "primary checkout with no session",
			seed: func(t *testing.T, repo string) {},
			want: nil,
		},
		{
			name: "detached worktree",
			seed: func(t *testing.T, repo string) {
				path := filepath.Join(filepath.Dir(repo), "wt-detached")
				runGit(t, repo, "worktree", "add", "--detach", path, "main")
			},
			want: nil,
		},
		{
			name: "scratch directory named after the carrier branch",
			seed: func(t *testing.T, repo string) {
				wt := addWorktree(t, repo, "feat")
				writeScratchHost(t, wt, "feat", os.Getpid())
			},
			want: []faultRow{
				{name: "feat", kind: session.KindWorktreeNoSession},
				{name: "feat", scratch: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepo(t)
			tt.seed(t, repo)

			sessions, err := Discover(repo, DefaultDeps())
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			got := faultRows(sessions)
			if len(got) != len(tt.want) {
				t.Fatalf("rows = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("rows = %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

// TestDiscover_FaultRowCarriesPathBranchAndReason pins the three fields a
// fault row is useless without.
//
// WorktreePath and Branch are what W3.T3's branch deletion and every teardown
// route steer by, and both survive the directory's deletion in the porcelain
// output (verified against git 2.51.0 by the fixture below, not recalled).
// Reason is the row's second line: a fault entry has no phase and no progress,
// so without it the dashboard would name a session and give no account of why
// it is listed as broken.
//
// StatusStale must stay FALSE. A worktree-side fault is not a broken status
// file — there is no status file at all — and reporting it as stale would put
// it in the bucket the row's own Kind exists to keep it out of.
func TestDiscover_FaultRowCarriesPathBranchAndReason(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "gone")
	writeStatus(t, wt, "gone", statusOpts{phase: state.PhaseExecuting})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("removing worktree dir: %v", err)
	}

	sessions, err := Discover(repo, DefaultDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.WorktreePath != wt {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, wt)
	}
	if got.Branch != "gone" {
		t.Errorf("Branch = %q, want %q", got.Branch, "gone")
	}
	if strings.TrimSpace(got.Reason) == "" {
		t.Error("Reason is empty; a fault row's second line has nothing else to show")
	}
	if got.StatusStale {
		t.Error("StatusStale = true, want false — a missing worktree is not a broken status file")
	}
	if got.HostRunning || got.HostPID != 0 {
		t.Errorf("HostRunning = %v, HostPID = %d, want false/0 — a fault row has no host.json of its own",
			got.HostRunning, got.HostPID)
	}
}
