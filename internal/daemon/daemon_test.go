package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/tokens"
)

// rig is one daemon process as the tests see it: the injected dependencies
// and a record of what was asked of them. It holds nothing the loop reads.
// Everything the loop decides from is in the files under root, which is why
// restart can hand the same root to a brand-new rig and stand in for a
// daemon that was killed and started again.
//
// The fake agent is three small pieces: launch writes a session's
// status.json under root/wt, sessions reads those files back the way
// discovery would report them, and setPhase / setAwaiting are the script
// that moves a session along between ticks.
type rig struct {
	t     *testing.T
	root  string
	home  string
	clock *time.Time

	launches            []LaunchRequest
	launchErr           error
	launchWritesNothing bool

	preflightOK    bool
	preflightSteps string
	preflights     int

	sessionsErr error

	records  []provider.TokenRecord
	tokenErr error
	tokenIDs []string

	notified []inbox.Item
	idle     []string
	logs     []string
}

// fakeTokens embeds the interface and defines only SessionTokens, so a
// method added to provider.Provider later does not break this file.
type fakeTokens struct {
	provider.Provider
	records []provider.TokenRecord
	err     error
}

func (f *fakeTokens) SessionTokens(provider.TokenQuery) ([]provider.TokenRecord, error) {
	return f.records, f.err
}

// planOf writes a master plan with the given tasks.
func planOf(tasks ...masterplan.Task) string {
	var b strings.Builder
	b.WriteString("# Plan\n\n")
	for _, tk := range tasks {
		deps := "none"
		if len(tk.DependsOn) > 0 {
			deps = strings.Join(tk.DependsOn, ", ")
		}
		fmt.Fprintf(&b, "#### %s — %s\n\n- **Depends on:** %s\n\n", tk.ID, tk.Title, deps)
	}
	return b.String()
}

// newRig makes a temp folder that looks like a main checkout (it has a .git
// entry) holding docs/PLAN.md.
func newRig(t *testing.T, tasks ...masterplan.Task) *rig {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	r := &rig{t: t, root: root, home: t.TempDir(), clock: &clock, preflightOK: true}
	r.writePlan(planOf(tasks...))
	return r
}

// restart is a new daemon pointed at the same folders: nothing is carried
// over but the disk and the clock.
func (r *rig) restart() *rig {
	return &rig{t: r.t, root: r.root, home: r.home, clock: r.clock, preflightOK: true, records: r.records}
}

func (r *rig) writePlan(text string) {
	r.t.Helper()
	path := filepath.Join(r.root, "docs", "PLAN.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) project() Project { return Project{Name: "demo", Root: r.root} }

func (r *rig) deps() Deps {
	return Deps{
		Now: func() time.Time { return *r.clock },
		LoadBuild: func(root string) ([]masterplan.Task, string, ledger.Ledger, error) {
			path := filepath.Join(root, "docs", "PLAN.md")
			tasks, err := masterplan.ParseFile(path)
			if err == nil {
				err = masterplan.Validate(tasks)
			}
			if err != nil {
				return nil, "", ledger.Ledger{}, fmt.Errorf("the master plan %s has a problem: %w", path, err)
			}
			l, err := ledger.Load(root)
			return tasks, path, l, err
		},
		Sessions:  r.sessions,
		Preflight: func(Project) (bool, string) { r.preflights++; return r.preflightOK, r.preflightSteps },
		Launch:    r.launch,
		RecordTokens: func(p Project, task, sess, id string) (int, error) {
			r.tokenIDs = append(r.tokenIDs, id)
			res, err := tokens.Record(tokens.RecordInput{
				Provider: &fakeTokens{records: r.records, err: r.tokenErr},
				Home:     r.home, MainCheckout: p.Root,
				Task: task, Session: sess, SessionID: id, Now: *r.clock,
			})
			return len(res.Lines), err
		},
		Notify:     func(_ Project, it inbox.Item) { r.notified = append(r.notified, it) },
		NotifyIdle: func(_ Project, title, body string) { r.idle = append(r.idle, title+"\n"+body) },
		Log:        func(format string, args ...any) { r.logs = append(r.logs, fmt.Sprintf(format, args...)) },
	}
}

func (r *rig) sessionDir(name string) string {
	return filepath.Join(r.root, "wt", name, ".afk", name)
}

// launch is the fake session creation. Like the real one it refuses a
// session whose worktree folder is already there.
func (r *rig) launch(req LaunchRequest) error {
	r.launches = append(r.launches, req)
	if r.launchErr != nil {
		return r.launchErr
	}
	if r.launchWritesNothing {
		return nil
	}
	if _, err := os.Stat(filepath.Join(r.root, "wt", req.Session)); err == nil {
		return fmt.Errorf("a branch named %q already exists", req.Session)
	}
	if err := os.MkdirAll(r.sessionDir(req.Session), 0o755); err != nil {
		return err
	}
	r.writeStatus(req.Session, func(st *state.Status) {})
	return os.WriteFile(filepath.Join(r.sessionDir(req.Session), "hostup"), nil, 0o644)
}

// writeStatus rewrites a session's status.json with the current clock as its
// updated_at, the way every real writer does.
func (r *rig) writeStatus(name string, change func(*state.Status)) {
	r.t.Helper()
	path := filepath.Join(r.sessionDir(name), "status.json")
	st := state.Status{SessionID: "sid-" + name, PlanName: name, Phase: state.PhasePlanning, Branch: name, BaseBranch: "main"}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &st); err != nil {
			r.t.Fatal(err)
		}
	}
	change(&st)
	st.UpdatedAt = stamp(*r.clock)
	data, err := json.Marshal(st)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) setPhase(name string, p state.Phase) {
	r.t.Helper()
	r.writeStatus(name, func(st *state.Status) { st.Phase = p })
}

func (r *rig) setAwaiting(name string, on bool) {
	r.t.Helper()
	r.writeStatus(name, func(st *state.Status) { st.AwaitingInput = on })
}

func (r *rig) stopHost(name string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.sessionDir(name), "hostup")); err != nil {
		r.t.Fatal(err)
	}
}

// sessions is the fake discovery: one row per folder under root/wt, a real
// session row when it has a status.json and the "worktree has no afk
// session" fault row when it does not, as internal/discover reports them.
func (r *rig) sessions(root string) ([]discover.SessionState, error) {
	if r.sessionsErr != nil {
		return nil, r.sessionsErr
	}
	entries, err := os.ReadDir(filepath.Join(root, "wt"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []discover.SessionState
	for _, e := range entries {
		name := e.Name()
		wt := filepath.Join(root, "wt", name)
		statusPath := filepath.Join(r.sessionDir(name), "status.json")
		if _, err := os.Stat(statusPath); err != nil {
			out = append(out, discover.SessionState{Name: name, WorktreePath: wt, Branch: name,
				Kind: session.KindWorktreeNoSession, Reason: "worktree has no afk session"})
			continue
		}
		ss := discover.SessionState{Name: name, WorktreePath: wt}
		_, hostErr := os.Stat(filepath.Join(r.sessionDir(name), "hostup"))
		ss.HostRunning = hostErr == nil
		res := state.ReadStatus(statusPath)
		if res.Stale {
			ss.StatusStale, ss.StaleReason, ss.Kind = true, res.Reason, session.KindStaleStatus
			out = append(out, ss)
			continue
		}
		ss.SessionID, ss.Phase, ss.Branch = res.Status.SessionID, res.Status.Phase, res.Status.Branch
		ss.AwaitingInput, ss.UpdatedAt = res.Status.AwaitingInput, res.Status.UpdatedAt
		if v, err := state.ParseVerdictFile(filepath.Join(r.sessionDir(name), "review-verdict.md")); err == nil {
			ss.Verdict = v
		}
		out = append(out, ss)
	}
	return out, nil
}

func (r *rig) settings() Settings {
	return Settings{StallAfter: stallAfter, HostGrace: hostGrace}
}

func (r *rig) advance(d time.Duration) { *r.clock = r.clock.Add(d) }

func (r *rig) tick() daemonstate.State { return Tick(r.project(), r.settings(), r.deps()) }

// ticks runs n ticks, moving the clock by step before each, and returns the
// last state.
func (r *rig) ticks(n int, step time.Duration) daemonstate.State {
	var st daemonstate.State
	for range n {
		r.advance(step)
		st = r.tick()
	}
	return st
}

func (r *rig) entry(id string) ledger.Entry {
	r.t.Helper()
	l, err := ledger.Load(r.root)
	if err != nil {
		r.t.Fatal(err)
	}
	e, _ := l.Get(id)
	if e.ID == "" {
		e = ledger.Entry{ID: id, Status: ledger.Pending}
	}
	return e
}

func (r *rig) store() inbox.Store {
	return inbox.Store{Dir: inbox.Dir(r.root), Now: func() time.Time { return *r.clock }}
}

func (r *rig) items() []inbox.Item {
	r.t.Helper()
	items, unreadable, err := r.store().List()
	if err != nil || len(unreadable) > 0 {
		r.t.Fatalf("inbox: err %v, unreadable %v", err, unreadable)
	}
	return items
}

func (r *rig) answer(id string) {
	r.t.Helper()
	if _, err := r.store().Answer(id, "done", "cli"); err != nil {
		r.t.Fatal(err)
	}
}

// decisions returns the kinds in the decision log, in order.
func (r *rig) decisions() []string {
	r.t.Helper()
	entries, err := decisionlog.Read(decisionlog.Path(r.root))
	if err != nil {
		r.t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range entries {
		if e.ChosenBy != decisionlog.ChosenByPolicy || !strings.HasPrefix(e.Rule, "PLAN §") || e.Chosen == "" {
			r.t.Errorf("decision %+v: want chosen_by policy, a PLAN rule and a choice", e)
		}
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func (r *rig) wantDecisions(want ...string) {
	r.t.Helper()
	if got := r.decisions(); !slices.Equal(got, want) {
		r.t.Errorf("decision log kinds = %v, want %v", got, want)
	}
}

func (r *rig) wantStatus(id string, want ledger.Status) ledger.Entry {
	r.t.Helper()
	e := r.entry(id)
	if e.Status != want {
		r.t.Fatalf("%s is %s in the ledger, want %s (logs: %v)", id, e.Status, want, r.logs)
	}
	return e
}

func wantContains(t *testing.T, what, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Errorf("%s does not hold %q:\n%s", what, p, text)
		}
	}
}

// The integration tests below run the whole loop (observe, Decide, act)
// against a temp folder, a scripted fake agent and a clock the test moves.
// Decide's own branches are rows of TestDecide; these tests are about what
// the executor does with each answer and about what survives a restart.
//
// Load-bearing tests, and what breaks without them:
//
//   - TestTickKillBetweenLedgerAndLaunch and TestTickRestartAdoptsRunningSession:
//     the loop keeps nothing in memory, so a daemon killed at the worst moment
//     must neither start a second session nor burn an attempt. These are the
//     two that fail if anyone adds a remembered flag or reorders the ledger
//     write and the launch.
//   - TestTickStallRaisesOneItem and TestTickHostGoneRaisesOneItem: "once" is
//     worked out from the inbox, tick after tick and across an answer. Without
//     them the owner is paged every tick, or every StallAfter for ever.
//   - TestTickPreflightFailure: the preflight starts the agent CLI, so it
//     must run once and then wait for the owner's answer, not once per tick.
//   - TestTickPlanError's "ledger fault" row: a stop that does not come from
//     the plan must not be answered as fixed and raised again every tick.
//   - TestTickHalfMadeSession: a worktree with no session files is "no
//     session", so the loop tries to create it and reports the real problem;
//     read as a session it would wait and then report a dead host.
//   - TestTickMissingRoot: a mistyped path in machine config must not create
//     a single file.
//   - TestTickHappyPathToShipping's twenty quiet ticks (and their twins in
//     the plan-error and preflight tests): the decision log counts things
//     that happened, not ticks.
func TestTickHappyPathToShipping(t *testing.T) {
	r := newRig(t, task("T1.1"), task("T1.2", "T1.1"))

	st := r.tick()
	if len(r.launches) != 1 {
		t.Fatalf("launches = %d, want 1 (logs: %v)", len(r.launches), r.logs)
	}
	want := LaunchRequest{Project: r.project(), Task: "T1.1", Session: "t1-1", Description: "Implement T1.1 from docs/PLAN.md"}
	if r.launches[0] != want {
		t.Errorf("launch request = %+v, want %+v", r.launches[0], want)
	}
	e := r.wantStatus("T1.1", ledger.Running)
	if e.Session != "t1-1" || e.Branch != "t1-1" || e.Attempts != 1 || e.StartedAt != stamp(*r.clock) || e.FinishedAt != "" {
		t.Errorf("running entry = %+v", e)
	}
	if st.Kind != string(ActLaunch) || st.Task != "T1.1" || st.Session != "t1-1" {
		t.Errorf("state after launch = %+v", st)
	}
	if stored, ok := daemonstate.ReadState(r.root); !ok || stored != st {
		t.Errorf("stored state = %+v (%v), want what Tick returned %+v", stored, ok, st)
	}
	r.wantDecisions("launch")

	// The scripted agent works through its phases; the loop only watches.
	for _, ph := range []state.Phase{state.PhaseExecuting, state.PhaseReviewing} {
		r.advance(time.Minute)
		r.setPhase("t1-1", ph)
		st = r.tick()
		if st.Kind != string(ActWait) || !strings.Contains(st.Reason, string(ph)) {
			t.Errorf("state in phase %s = %+v", ph, st)
		}
	}
	r.wantDecisions("launch")

	r.advance(time.Minute)
	r.setPhase("t1-1", state.PhaseDone)
	st = r.tick()
	if st.Kind != string(ActShip) {
		t.Errorf("state on done = %+v", st)
	}
	if e := r.wantStatus("T1.1", ledger.Shipping); e.FinishedAt != "" || e.Session != "t1-1" {
		t.Errorf("shipping entry = %+v", e)
	}
	r.wantDecisions("launch", "tokens", "ship_wait")

	// Shipping is the end of the line: twenty more ticks change nothing,
	// log nothing, and do not start the task that depends on this one.
	st = r.ticks(20, 5*time.Second)
	if st.Kind != string(ActWait) || !strings.Contains(st.Reason, "waiting to ship") {
		t.Errorf("state while shipping = %+v", st)
	}
	r.wantDecisions("launch", "tokens", "ship_wait")
	r.wantStatus("T1.1", ledger.Shipping)
	r.wantStatus("T1.2", ledger.Pending)
	if len(r.launches) != 1 || len(r.items()) != 0 || len(r.notified) != 0 || len(r.idle) != 0 {
		t.Errorf("launches %d, items %d, notified %d, idle %d; want 1, 0, 0, 0", len(r.launches), len(r.items()), len(r.notified), len(r.idle))
	}
}

// TestTickTokenLedger checks that a finished task leaves token-ledger lines
// for each phase, read with the agent's own session id from the same
// discovered row the loop watched. The "unreadable" row is load-bearing: a
// cost that cannot be counted must never keep finished work from shipping.
func TestTickTokenLedger(t *testing.T) {
	recs := []provider.TokenRecord{
		{Phase: provider.PhasePlan, Role: provider.RolePlanner, Model: "m-big", Input: 10, Output: 5},
		{Phase: provider.PhaseExecute, Role: provider.RoleTask, Model: "m-big", Input: 20, Output: 7},
		{Phase: provider.PhaseReview, Role: provider.RoleCritic, Model: "m-small", Input: 30, Output: 9},
	}
	cases := []struct {
		name      string
		phase     state.Phase
		tokenErr  error
		want      ledger.Status
		wantLines int
		wantKinds []string
	}{
		{name: "done records every phase", phase: state.PhaseDone, want: ledger.Shipping, wantLines: 3,
			wantKinds: []string{"launch", "tokens", "ship_wait"}},
		{name: "failed records every phase", phase: state.PhaseFailed, want: ledger.Failed, wantLines: 3,
			wantKinds: []string{"launch", "tokens", "task_failed"}},
		{name: "unreadable tokens still ship", phase: state.PhaseDone, tokenErr: provider.ErrNoTranscript, want: ledger.Shipping,
			wantKinds: []string{"launch", "tokens", "ship_wait"}},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			r := newRig(t, task("T1.1"))
			r.records, r.tokenErr = recs, tt.tokenErr
			r.tick()
			r.advance(time.Minute)
			r.setPhase("t1-1", tt.phase)
			r.tick()

			r.wantStatus("T1.1", tt.want)
			r.wantDecisions(tt.wantKinds...)
			if !slices.Equal(r.tokenIDs, []string{"sid-t1-1"}) {
				t.Errorf("session ids asked for = %v, want the one in status.json", r.tokenIDs)
			}
			lines, err := tokens.Read(tokens.LedgerPath(r.root))
			if err != nil || len(lines) != tt.wantLines {
				t.Fatalf("token ledger has %d lines (err %v), want %d", len(lines), err, tt.wantLines)
			}
			seen := map[string]bool{}
			for _, l := range lines {
				seen[l.Phase] = true
				if l.Task != "T1.1" || l.Session != "t1-1" || l.At != stamp(*r.clock) {
					t.Errorf("token line = %+v", l)
				}
			}
			if tt.wantLines > 0 && !(seen[provider.PhasePlan] && seen[provider.PhaseExecute] && seen[provider.PhaseReview]) {
				t.Errorf("phases in the token ledger = %v, want plan, execute and review", seen)
			}

			// A second daemon ticking afterwards records nothing more.
			r.restart().ticks(5, time.Minute)
			again, _ := tokens.Read(tokens.LedgerPath(r.root))
			if len(again) != tt.wantLines {
				t.Errorf("token ledger grew to %d lines", len(again))
			}
		})
	}
}

const verdictText = `# Review Verdict

**Plan**: t1-1
**Date**: 2026-10-11
**Verdict**: REJECT

## Checks
- Compliance: PASS
- Quality: FAIL
- QA: PASS

## Findings
- [blocker] W1.T1: the build is broken
- [major] general: no tests for the new code
- [minor] general: a typo in a comment
`

// TestTickFailedPhase: a session that ends as failed marks the task failed,
// tells the owner what the review said, and the loop moves on to a task that
// does not depend on it. The "moves on" half is load-bearing: one failed
// task must not stop independent work.
func TestTickFailedPhase(t *testing.T) {
	cases := []struct {
		name    string
		verdict string
		want    []string
		wantNot []string
	}{
		{name: "with a verdict", verdict: verdictText,
			want:    []string{"Compliance: PASS", "Quality: FAIL", "QA: PASS", "[blocker] W1.T1: the build is broken", "[major] general: no tests", "afk ledger set T1.1 pending"},
			wantNot: []string{"[minor]"}},
		{name: "without a verdict",
			want: []string{"no review verdict", filepath.Join("wt", "t1-1", ".afk", "t1-1"), "afk ledger set T1.1 pending"}},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			r := newRig(t, task("T1.1"), task("T1.2", "T1.1"), task("T1.3"))
			r.tick()
			r.advance(time.Minute)
			if tt.verdict != "" {
				if err := os.WriteFile(filepath.Join(r.sessionDir("t1-1"), "review-verdict.md"), []byte(tt.verdict), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			r.setPhase("t1-1", state.PhaseFailed)
			st := r.tick()

			if st.Kind != string(ActFail) {
				t.Errorf("state = %+v", st)
			}
			if e := r.wantStatus("T1.1", ledger.Failed); e.FinishedAt != stamp(*r.clock) {
				t.Errorf("failed entry = %+v, want finished now", e)
			}
			items := r.items()
			if len(items) != 1 {
				t.Fatalf("items = %d, want 1", len(items))
			}
			it := items[0]
			if it.Kind != "failure" || it.Blocking || it.Task != "T1.1" || it.Session != "t1-1" {
				t.Errorf("failure item = %+v", it)
			}
			wantContains(t, "the failure item's body", it.Body, tt.want...)
			for _, bad := range tt.wantNot {
				if strings.Contains(it.Body, bad) {
					t.Errorf("the body holds %q:\n%s", bad, it.Body)
				}
			}
			if len(r.notified) != 1 || r.notified[0].ID != it.ID {
				t.Errorf("notified = %+v, want the stored item", r.notified)
			}

			// The next tick starts the independent task, not the dependent one.
			r.advance(5 * time.Second)
			r.tick()
			if len(r.launches) != 2 || r.launches[1].Task != "T1.3" || r.launches[1].Session != "t1-3" {
				t.Fatalf("launches = %+v, want T1.3 second", r.launches)
			}
			r.wantStatus("T1.3", ledger.Running)
			r.wantStatus("T1.2", ledger.Pending)
			r.wantDecisions("launch", "tokens", "task_failed", "launch")
			if n := len(r.items()); n != 1 {
				t.Errorf("items after moving on = %d, want still 1", n)
			}
		})
	}
}

// TestTickParkAndUnpark: an open blocking item parks the task, the owner is
// told once that the build is waiting, and the answer puts the task back to
// running without touching its session.
func TestTickParkAndUnpark(t *testing.T) {
	r := newRig(t, task("T1.1"), task("T1.2", "T1.1"))
	r.tick()
	started := r.entry("T1.1").StartedAt

	r.advance(time.Minute)
	q, err := r.store().Add(inbox.NewItem{Task: "T1.1", Session: "t1-1", Kind: "question", Blocking: true, Title: "Which database?"})
	if err != nil {
		t.Fatal(err)
	}
	r.setAwaiting("t1-1", true)
	st := r.tick()
	if st.Kind != string(ActPark) {
		t.Errorf("state = %+v", st)
	}
	if e := r.wantStatus("T1.1", ledger.Parked); !slices.Equal(e.ParkedOn, []string{q.ID}) {
		t.Errorf("parked on %v, want [%s]", e.ParkedOn, q.ID)
	}

	// Parked for an hour: no stall is raised about a question the owner has
	// already been sent, and the idle notice goes out once.
	st = r.ticks(12, 5*time.Minute)
	if st.Kind != string(ActIdle) || !strings.Contains(st.Reason, q.ID) {
		t.Errorf("state while parked = %+v", st)
	}
	if len(r.idle) != 1 || !strings.Contains(r.idle[0], "waiting on you") {
		t.Errorf("idle notices = %v, want exactly one", r.idle)
	}
	if n := len(r.items()); n != 1 {
		t.Errorf("items = %d, want only the question", n)
	}

	r.answer(q.ID)
	r.advance(5 * time.Second)
	st = r.tick()
	if st.Kind != string(ActUnpark) {
		t.Errorf("state on answer = %+v", st)
	}
	e := r.wantStatus("T1.1", ledger.Running)
	if len(e.ParkedOn) != 0 || e.StartedAt != started || e.Attempts != 1 || e.Session != "t1-1" {
		t.Errorf("un-parked entry = %+v, want the same attempt with nothing parked", e)
	}
	if len(r.launches) != 1 {
		t.Errorf("launches = %d, want 1: un-parking starts no session", len(r.launches))
	}
	r.wantDecisions("launch", "park", "unpark")
}

// TestTickStallRaisesOneItem: a session asking at the terminal for longer
// than the limit raises exactly one item and parks, however many ticks pass,
// and an answered item is not followed by a second one.
func TestTickStallRaisesOneItem(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.tick()
	r.advance(time.Minute)
	r.setAwaiting("t1-1", true)

	// Exactly at the limit is not yet over it.
	r.advance(stallAfter)
	r.tick()
	if n := len(r.items()); n != 0 {
		t.Fatalf("items at the limit = %d, want 0", n)
	}
	r.advance(time.Second)
	st := r.tick()

	items := r.items()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (logs: %v)", len(items), r.logs)
	}
	it := items[0]
	if it.Title != StallTitlePrefix+"T1.1 (t1-1)" || it.Kind != "failure" || !it.Blocking || it.Task != "T1.1" || it.Session != "t1-1" {
		t.Errorf("stall item = %+v", it)
	}
	wantContains(t, "the stall body", it.Body,
		"afk attach t1-1", "/afk:go --session t1-1", "afk inbox answer", "cannot yet wake a session by itself")
	if e := r.wantStatus("T1.1", ledger.Parked); !slices.Equal(e.ParkedOn, []string{it.ID}) {
		t.Errorf("parked on %v, want [%s]", e.ParkedOn, it.ID)
	}
	if st.Kind != string(ActStall) || !strings.Contains(st.Reason, it.ID) {
		t.Errorf("state = %+v", st)
	}

	r.ticks(20, time.Minute)
	if n := len(r.items()); n != 1 {
		t.Fatalf("items after twenty more ticks = %d, want 1", n)
	}

	// The owner answers but the session is still idle: nothing can type
	// into it yet. No second item, for as long as the attempt lasts.
	r.answer(it.ID)
	r.advance(5 * time.Second)
	r.tick()
	r.wantStatus("T1.1", ledger.Running)
	st = r.ticks(20, 5*time.Minute)
	if n := len(r.items()); n != 1 {
		t.Errorf("items after the answer = %d, want still 1", n)
	}
	if st.Kind != string(ActWait) || !strings.Contains(st.Reason, "already reported") {
		t.Errorf("state after the answer = %+v", st)
	}
	r.wantStatus("T1.1", ledger.Running)
	if len(r.notified) != 1 || len(r.launches) != 1 {
		t.Errorf("notified %d, launches %d; want 1 and 1", len(r.notified), len(r.launches))
	}
	r.wantDecisions("launch", "stall", "unpark")
}

// TestTickAwaitingInputWithBlockingItemDoesNotStall: a parked session sits at
// a prompt by design, so a long wait there is not a stall.
func TestTickAwaitingInputWithBlockingItemDoesNotStall(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.tick()
	r.advance(time.Minute)
	q, err := r.store().Add(inbox.NewItem{Task: "T1.1", Kind: "question", Blocking: true, Title: "Which database?"})
	if err != nil {
		t.Fatal(err)
	}
	r.setAwaiting("t1-1", true)
	r.advance(time.Hour)
	r.ticks(20, time.Minute)

	if e := r.wantStatus("T1.1", ledger.Parked); !slices.Equal(e.ParkedOn, []string{q.ID}) {
		t.Errorf("parked on %v, want the question", e.ParkedOn)
	}
	for _, it := range r.items() {
		if strings.HasPrefix(it.Title, StallTitlePrefix) || strings.HasPrefix(it.Title, HostGoneTitlePrefix) {
			t.Errorf("unexpected report %q", it.Title)
		}
	}
	r.wantDecisions("launch", "park")
}

// TestTickHostGoneRaisesOneItem: a session whose host has stopped is reported
// once, with the commands that fit a dead host. The attach command only
// connects to a live host, so the body must not send the owner there.
func TestTickHostGoneRaisesOneItem(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.tick()
	r.stopHost("t1-1")

	r.advance(hostGrace)
	r.tick()
	if n := len(r.items()); n != 0 {
		t.Fatalf("items at the grace = %d, want 0", n)
	}
	r.advance(time.Second)
	r.tick()

	items := r.items()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (logs: %v)", len(items), r.logs)
	}
	it := items[0]
	if it.Title != HostGoneTitlePrefix+"T1.1 (t1-1)" || it.Kind != "failure" || !it.Blocking || it.Task != "T1.1" || it.Session != "t1-1" {
		t.Errorf("host-gone item = %+v", it)
	}
	wantContains(t, "the host-gone body", it.Body,
		"afk dash", "/afk:go --session t1-1", "afk inbox answer", "afk clean t1-1", "afk ledger set T1.1 pending",
		"cannot yet wake a session by itself")
	if strings.Contains(it.Body, "afk attach") {
		t.Errorf("the host-gone body sends the owner to `afk attach`, which cannot start a stopped host:\n%s", it.Body)
	}
	if strings.Contains(stallBody("T1.1", "t1-1"), "afk dash") {
		t.Error("the stall body names `afk dash`; the two bodies must name their own commands")
	}
	if e := r.wantStatus("T1.1", ledger.Parked); !slices.Equal(e.ParkedOn, []string{it.ID}) {
		t.Errorf("parked on %v", e.ParkedOn)
	}

	r.ticks(20, time.Minute)
	r.answer(it.ID)
	r.ticks(20, time.Minute)
	if n := len(r.items()); n != 1 {
		t.Errorf("items after the answer = %d, want still 1", n)
	}
	r.wantDecisions("launch", "host_gone", "unpark")
}

// TestTickRestartAdoptsRunningSession: a daemon killed mid-task and started
// again watches the session that is there and starts no other.
func TestTickRestartAdoptsRunningSession(t *testing.T) {
	first := newRig(t, task("T1.1"), task("T1.2", "T1.1"))
	first.tick()
	first.advance(time.Minute)
	first.setPhase("t1-1", state.PhaseExecuting)

	second := first.restart()
	st := second.ticks(5, 5*time.Second)
	if st.Kind != string(ActWait) || st.Task != "T1.1" || st.Session != "t1-1" {
		t.Errorf("state after restart = %+v", st)
	}
	if len(second.launches) != 0 {
		t.Fatalf("the restarted daemon launched %+v, want nothing", second.launches)
	}
	if e := second.wantStatus("T1.1", ledger.Running); e.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", e.Attempts)
	}

	second.advance(time.Minute)
	second.setPhase("t1-1", state.PhaseDone)
	second.tick()
	second.wantStatus("T1.1", ledger.Shipping)
	if len(second.launches) != 0 {
		t.Errorf("launches after shipping = %+v, want none", second.launches)
	}
	second.wantDecisions("launch", "tokens", "ship_wait")
}

// TestTickKillBetweenLedgerAndLaunch: the first daemon writes the ledger and
// dies before the session exists (its Launch returns without creating
// anything). The second creates that same session once, as the same attempt.
func TestTickKillBetweenLedgerAndLaunch(t *testing.T) {
	first := newRig(t, task("T1.1"))
	first.launchWritesNothing = true
	first.tick()
	if e := first.wantStatus("T1.1", ledger.Running); e.Attempts != 1 || e.Session != "t1-1" {
		t.Fatalf("entry = %+v", e)
	}
	started := first.entry("T1.1").StartedAt

	second := first.restart()
	second.advance(time.Hour)
	st := second.tick()
	if st.Kind != string(ActCreate) {
		t.Errorf("state = %+v, want a create", st)
	}
	want := LaunchRequest{Project: second.project(), Task: "T1.1", Session: "t1-1", Description: "Implement T1.1 from docs/PLAN.md"}
	if len(second.launches) != 1 || second.launches[0] != want {
		t.Fatalf("launches = %+v, want one for %+v", second.launches, want)
	}
	second.ticks(5, 5*time.Second)
	if len(second.launches) != 1 {
		t.Errorf("launches = %d, want the session created once", len(second.launches))
	}
	e := second.wantStatus("T1.1", ledger.Running)
	if e.Attempts != 1 || e.Session != "t1-1" || e.StartedAt != started {
		t.Errorf("entry = %+v, want the same attempt", e)
	}
	if n := len(second.items()); n != 0 {
		t.Errorf("items = %d, want 0", n)
	}
	second.wantDecisions("launch")
}

// TestTickPause: while paused nothing launches, but a task already running
// is still watched to the end.
func TestTickPause(t *testing.T) {
	r := newRig(t, task("T1.1"), task("T1.2", "T1.1"))
	if err := daemonstate.Pause(r.root, *r.clock); err != nil {
		t.Fatal(err)
	}
	st := r.ticks(5, 5*time.Second)
	if st.Kind != string(ActWait) || !strings.Contains(st.Reason, "paused") {
		t.Errorf("state while paused = %+v", st)
	}
	if len(r.launches) != 0 || r.preflights != 0 {
		t.Fatalf("launches %d, preflights %d while paused; want 0 and 0", len(r.launches), r.preflights)
	}
	if _, err := os.Stat(ledger.Path(r.root)); !os.IsNotExist(err) {
		t.Errorf("a ledger was written while paused (stat err %v)", err)
	}

	if _, err := daemonstate.Resume(r.root); err != nil {
		t.Fatal(err)
	}
	r.tick()
	r.wantStatus("T1.1", ledger.Running)

	// Paused again with a task in flight: it still reaches shipping.
	if err := daemonstate.Pause(r.root, *r.clock); err != nil {
		t.Fatal(err)
	}
	r.advance(time.Minute)
	r.setPhase("t1-1", state.PhaseDone)
	r.tick()
	r.wantStatus("T1.1", ledger.Shipping)

	// The owner merges it by hand; the next task still does not start.
	if err := ledger.Update(r.root, func(l *ledger.Ledger) error {
		_, err := l.Set("T1.1", ledger.Done, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.ticks(5, 5*time.Second)
	if len(r.launches) != 1 {
		t.Errorf("launches = %d, want 1: pause holds back T1.2", len(r.launches))
	}
	r.wantStatus("T1.2", ledger.Pending)
}

// TestTickPreflightFailure: a machine that cannot start agents unattended
// gets one blocking item, nothing launches, and the preflight (which starts
// the agent CLI) is not run again until the owner answers.
func TestTickPreflightFailure(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.preflightOK = false
	r.preflightSteps = "Run the agent once by hand in this mode and accept the warning."

	st := r.ticks(20, 5*time.Second)

	items := r.items()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	it := items[0]
	if it.Title != PreflightTitle || it.Kind != "failure" || !it.Blocking {
		t.Errorf("preflight item = %+v", it)
	}
	wantContains(t, "the preflight body", it.Body, r.preflightSteps)
	t.Logf("preflight item %s: %s", it.ID, it.Body)
	if r.preflights != 1 {
		t.Errorf("preflight ran %d times over twenty ticks, want 1", r.preflights)
	}
	if len(r.launches) != 0 {
		t.Errorf("launched %+v, want nothing", r.launches)
	}
	if _, err := os.Stat(ledger.Path(r.root)); !os.IsNotExist(err) {
		t.Errorf("the ledger was written (stat err %v), want it left alone", err)
	}
	if st.Kind != KindBlocked || st.Reason != PreflightTitle+"; see inbox item "+it.ID {
		t.Errorf("state = %+v", st)
	}
	if len(r.notified) != 1 {
		t.Errorf("notified %d times, want 1", len(r.notified))
	}
	r.wantDecisions("preflight_failed")

	// The owner fixes the machine and answers: the check runs again and
	// the task launches.
	r.preflightOK = true
	r.answer(it.ID)
	r.advance(5 * time.Second)
	r.tick()
	if r.preflights != 2 || len(r.launches) != 1 {
		t.Errorf("preflights %d, launches %d after the answer; want 2 and 1", r.preflights, len(r.launches))
	}
	r.wantStatus("T1.1", ledger.Running)
	r.wantDecisions("preflight_failed", "launch")
}

// TestTickLaunchError: a session that cannot be created marks the task
// failed and tells the owner the commands that put it right.
func TestTickLaunchError(t *testing.T) {
	r := newRig(t, task("T1.1"), task("T1.2", "T1.1"))
	r.launchErr = errors.New("git worktree add: exit status 128")

	st := r.tick()
	if st.Kind != string(ActFail) || !strings.Contains(st.Reason, "exit status 128") {
		t.Errorf("state = %+v", st)
	}
	e := r.wantStatus("T1.1", ledger.Failed)
	if e.FinishedAt != stamp(*r.clock) || e.Attempts != 1 || e.Session != "t1-1" {
		t.Errorf("failed entry = %+v", e)
	}
	items := r.items()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	it := items[0]
	if it.Kind != "failure" || it.Blocking || it.Task != "T1.1" || it.Session != "t1-1" {
		t.Errorf("launch-failure item = %+v", it)
	}
	wantContains(t, "the launch-failure body", it.Body,
		"git worktree add: exit status 128", "afk clean t1-1", "afk ledger set T1.1 pending")
	t.Logf("launch-failure item %s: %s", it.ID, it.Body)
	r.wantDecisions("launch", "task_failed")

	// What is left depends on the failed task: the loop goes idle, says so
	// once, and launches nothing more.
	st = r.ticks(20, 5*time.Second)
	if st.Kind != string(ActIdle) || !strings.Contains(st.Reason, "blocked by failed") {
		t.Errorf("state afterwards = %+v", st)
	}
	if len(r.launches) != 1 || len(r.items()) != 1 || len(r.notified) != 1 {
		t.Errorf("launches %d, items %d, notified %d; want 1 each", len(r.launches), len(r.items()), len(r.notified))
	}
	if len(r.idle) != 1 || !strings.Contains(r.idle[0], "blocked by a failed task") {
		t.Errorf("idle notices = %v, want exactly one", r.idle)
	}
	r.wantDecisions("launch", "task_failed")
}

// TestTickPlanError: a project that cannot be read raises one item per
// distinct problem and launches nothing; once it can be read again the items
// are answered and work starts.
//
// The "ledger fault" row is load-bearing. That stop comes from the ledger
// while the plan reads fine, so "the project can be read again" is true on
// the same tick the stop is raised. If the executor answered the item there,
// every tick would close it and raise a new one.
func TestTickPlanError(t *testing.T) {
	t.Run("unknown_dependency", func(t *testing.T) {
		good := planOf(task("T1.1"), task("T1.2", "T1.1"))
		r := newRig(t)
		r.writePlan(planOf(task("T1.1"), task("T1.2", "T9.9")))

		st := r.ticks(20, 5*time.Second)
		items := r.items()
		if len(items) != 1 {
			t.Fatalf("items = %d, want 1", len(items))
		}
		it := items[0]
		if it.Title != StopTitle || it.Kind != "failure" || it.Blocking || it.Task != "" {
			t.Errorf("plan-error item = %+v", it)
		}
		wantContains(t, "the plan-error body", it.Body, "T9.9", "unknown dependency")
		t.Logf("plan-error item %s: %s", it.ID, it.Body)
		if st.Kind != string(ActStop) || !strings.Contains(st.Reason, "T9.9") {
			t.Errorf("state = %+v", st)
		}
		if len(r.launches) != 0 || r.preflights != 0 {
			t.Errorf("launches %d, preflights %d; want 0 and 0", len(r.launches), r.preflights)
		}
		r.wantDecisions("plan_error")

		// A different problem gets its own item, also once.
		r.writePlan("# Plan\n\nNothing here yet.\n")
		r.ticks(20, 5*time.Second)
		if n := len(r.items()); n != 2 {
			t.Fatalf("items after a second problem = %d, want 2", n)
		}
		r.wantDecisions("plan_error", "plan_error")

		// Fixed: both items are answered and the first task launches.
		r.writePlan(good)
		r.advance(5 * time.Second)
		r.tick()
		for _, it := range r.items() {
			if it.Open() || it.Answer != "fixed" || it.AnsweredVia != "cli" {
				t.Errorf("item %s after the fix = %+v, want answered as fixed", it.ID, it)
			}
		}
		if len(r.launches) != 1 || r.launches[0].Task != "T1.1" {
			t.Errorf("launches = %+v, want T1.1", r.launches)
		}
		r.wantDecisions("plan_error", "plan_error", "plan_fixed", "launch")
	})

	t.Run("ledger_fault", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		// A hand-edited ledger: a task the plan does not have is running.
		if err := ledger.Update(r.root, func(l *ledger.Ledger) error {
			l.Tasks = append(l.Tasks, ledger.Entry{ID: "T7.7", Status: ledger.Running, Session: "t7-7"})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		st := r.ticks(20, 5*time.Second)
		items := r.items()
		if len(items) != 1 || !items[0].Open() {
			t.Fatalf("items = %+v, want one open item", items)
		}
		wantContains(t, "the ledger-fault body", items[0].Body, "T7.7", "afk ledger set")
		if st.Kind != string(ActStop) || len(r.launches) != 0 {
			t.Errorf("state = %+v, launches = %d", st, len(r.launches))
		}
		r.wantDecisions("plan_error")
	})
}

// TestTickHalfMadeSession: the ledger says running and a worktree folder is
// there, but it has no session files. That is not a session, so the loop
// tries to create it; the real creation fails on what is already there and
// the owner is told how to clear it. It must not be read as a session whose
// host has died.
func TestTickHalfMadeSession(t *testing.T) {
	first := newRig(t, task("T1.1"))
	first.launchWritesNothing = true
	first.tick()
	if err := os.MkdirAll(filepath.Join(first.root, "wt", "t1-1"), 0o755); err != nil {
		t.Fatal(err)
	}

	second := first.restart()
	second.advance(time.Hour)
	second.tick()

	if len(second.launches) != 1 || second.launches[0].Session != "t1-1" {
		t.Fatalf("launches = %+v, want the recorded session tried again", second.launches)
	}
	second.wantStatus("T1.1", ledger.Failed)
	items := second.items()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if strings.HasPrefix(items[0].Title, HostGoneTitlePrefix) || strings.HasPrefix(items[0].Title, StallTitlePrefix) {
		t.Errorf("raised %q, want the launch failure", items[0].Title)
	}
	wantContains(t, "the body", items[0].Body, "already exists", "afk clean t1-1", "afk ledger set T1.1 pending")
}

// TestTickSessionsError: an error listing sessions is never read as "the
// session is missing". The tick waits and says why where `afk daemon status`
// reads it.
func TestTickSessionsError(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.tick()
	r.sessionsErr = errors.New("git worktree list: exit status 128")
	r.advance(time.Hour)
	st := r.tick()

	if st.Kind != string(ActWait) || st.Task != "T1.1" || !strings.Contains(st.Reason, "git worktree list: exit status 128") {
		t.Errorf("state = %+v", st)
	}
	if stored, ok := daemonstate.ReadState(r.root); !ok || stored != st {
		t.Errorf("stored state = %+v (%v), want %+v", stored, ok, st)
	}
	if len(r.launches) != 1 || len(r.items()) != 0 {
		t.Errorf("launches %d, items %d; want 1 and 0", len(r.launches), len(r.items()))
	}
	r.wantStatus("T1.1", ledger.Running)
	if len(r.logs) == 0 {
		t.Error("the error was not logged")
	}
}

// TestTickMissingRoot: a project path that is not a checkout is reported and
// nothing at all is written there, not even a folder.
func TestTickMissingRoot(t *testing.T) {
	base := t.TempDir()
	notGit := filepath.Join(base, "not-git")
	if err := os.Mkdir(notGit, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, root string }{
		{"no folder", filepath.Join(base, "nope")},
		{"a folder with no .git", notGit},
		{"an empty path", ""},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			clock := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
			var logs []string
			d := Deps{
				Now: func() time.Time { return clock },
				LoadBuild: func(string) ([]masterplan.Task, string, ledger.Ledger, error) {
					t.Error("the build was loaded for a folder that is not a project")
					return nil, "", ledger.Ledger{}, nil
				},
				Log: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
			}
			st := Tick(Project{Name: "demo", Root: tt.root}, Settings{}, d)
			want := daemonstate.State{At: stamp(clock), Kind: KindStopped, Reason: "project folder not found"}
			if st != want {
				t.Errorf("state = %+v, want %+v", st, want)
			}
			if len(logs) != 1 {
				t.Errorf("logs = %v, want one line", logs)
			}
			left, err := os.ReadDir(base)
			if err != nil || len(left) != 1 || left[0].Name() != "not-git" {
				t.Errorf("the temp folder holds %v (err %v), want only not-git", left, err)
			}
			if inside, _ := os.ReadDir(notGit); len(inside) != 0 {
				t.Errorf("files were written into the folder: %v", inside)
			}
		})
	}
}

// TestViewOf pins which discovered rows count as the session to watch. The
// load-bearing rows are the two worktree faults and the scratch row: each
// shares a name with the task's session but has no status.json, and reading
// any of them as a session would turn a half-made session into a silent wait.
func TestViewOf(t *testing.T) {
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	healthy := discover.SessionState{Name: "t1-1", Phase: state.PhaseExecuting, AwaitingInput: true,
		UpdatedAt: stamp(at), HostRunning: true, SessionID: "sid"}
	cases := []struct {
		name string
		rows []discover.SessionState
		want *SessionView
	}{
		{name: "no rows"},
		{name: "another session only", rows: []discover.SessionState{{Name: "t1-2", Phase: state.PhaseDone}}},
		{name: "healthy", rows: []discover.SessionState{healthy},
			want: &SessionView{Name: "t1-1", Phase: state.PhaseExecuting, AwaitingInput: true, UpdatedAt: at, HostRunning: true}},
		{name: "stale status is still a session", rows: []discover.SessionState{{Name: "t1-1", StatusStale: true, Kind: session.KindStaleStatus, HostRunning: true}},
			want: &SessionView{Name: "t1-1", Stale: true, HostRunning: true}},
		{name: "status in a worktree on another branch is still a session",
			rows: []discover.SessionState{{Name: "t1-1", Phase: state.PhaseDone, Kind: session.KindStatusWithoutWorktree}},
			want: &SessionView{Name: "t1-1", Phase: state.PhaseDone}},
		{name: "worktree with no session files", rows: []discover.SessionState{{Name: "t1-1", Kind: session.KindWorktreeNoSession}}},
		{name: "worktree whose folder is gone", rows: []discover.SessionState{{Name: "t1-1", Kind: session.KindWorktreeMissing}}},
		{name: "scratch session of the same name", rows: []discover.SessionState{{Name: "t1-1", Scratch: true, HostRunning: true}}},
		{name: "a fault row before the real one", rows: []discover.SessionState{{Name: "t1-1", Kind: session.KindWorktreeNoSession}, healthy},
			want: &SessionView{Name: "t1-1", Phase: state.PhaseExecuting, AwaitingInput: true, UpdatedAt: at, HostRunning: true}},
		{name: "an unreadable time is the zero time", rows: []discover.SessionState{{Name: "t1-1", Phase: state.PhaseFixing, UpdatedAt: "yesterday"}},
			want: &SessionView{Name: "t1-1", Phase: state.PhaseFixing}},
		{name: "an offset time is the same instant", rows: []discover.SessionState{{Name: "t1-1", Phase: state.PhaseFixing, UpdatedAt: "2026-10-11T11:00:00+02:00"}},
			want: &SessionView{Name: "t1-1", Phase: state.PhaseFixing, UpdatedAt: at}},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			got, row := viewOf(tt.rows, "t1-1")
			if (got == nil) != (tt.want == nil) || (got == nil) != (row == nil) {
				t.Fatalf("view = %+v, row = %+v, want view %+v", got, row, tt.want)
			}
			if got == nil {
				return
			}
			if !got.UpdatedAt.Equal(tt.want.UpdatedAt) {
				t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, tt.want.UpdatedAt)
			}
			got.UpdatedAt, tt.want.UpdatedAt = time.Time{}, time.Time{}
			if *got != *tt.want {
				t.Errorf("view = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}

// TestOnceAndRun: one project that cannot be run does not stop the next, and
// Run returns nil when its context ends. The clock is what ends the context,
// so the test waits on nothing but the loop itself.
func TestOnceAndRun(t *testing.T) {
	missing := Project{Name: "gone", Root: filepath.Join(t.TempDir(), "nope")}

	t.Run("once", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		states := Once([]Project{missing, r.project()}, r.settings(), r.deps())
		if len(states) != 2 || states[0].Kind != KindStopped || states[1].Kind != string(ActLaunch) {
			t.Errorf("states = %+v, want stopped then launch", states)
		}
	})

	t.Run("run", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d := r.deps()
		calls := 0
		d.Now = func() time.Time {
			// Each tick reads the clock at its start; six of those is
			// three passes over the two projects.
			if calls++; calls >= 6 {
				cancel()
			}
			return *r.clock
		}
		if err := Run(ctx, []Project{missing, r.project()}, r.settings(), time.Millisecond, d); err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if len(r.launches) != 1 {
			t.Errorf("launches = %d, want 1: the second project runs despite the first", len(r.launches))
		}
		r.wantStatus("T1.1", ledger.Running)
	})

	t.Run("run_with_a_finished_context", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Run(ctx, []Project{r.project()}, r.settings(), 0, r.deps()); err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if len(r.launches) != 0 {
			t.Errorf("launches = %d, want 0", len(r.launches))
		}
	})
}
