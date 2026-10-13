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
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ship"
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
//
// The fake ship is shipFn: a script that is handed each ShipRequest and
// answers as internal/ship would. It touches nothing but the request's own
// Save and Stop, so what a test then finds in the ledger and the inbox is the
// loop's doing. With shipFn nil the loop has no shipper at all, which is how
// every test written before the loop could ship still runs.
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
	urgent   []inbox.Item
	idle     []string
	logs     []string

	shipFn   func(ShipRequest) ship.Outcome
	shipReqs []ShipRequest
	// noUrgent leaves Deps.NotifyUrgent unset.
	noUrgent bool

	cleans   []string
	cleanErr error

	// promptOn sets Deps.SendPrompt; prompts is every line it was asked to
	// type, and promptErr what it answers.
	promptOn  bool
	prompts   []typed
	promptErr error

	// restartOn and relaunchOn set Deps.Restart and Deps.Relaunch. Both are
	// off unless a test turns them on, so every test written before the loop
	// could recover a session runs as it did. restarts and relaunches are the
	// calls made; restartRefused, restartErr and relaunchErr are what the
	// fakes answer.
	restartOn, relaunchOn   bool
	restarts                []restartCall
	relaunches              []relaunchCall
	restartRefused          string
	restartErr, relaunchErr error
	// noRestarts is `max_restarts = 0`. backoff and wedgeConfirm replace the
	// rig's restart backoff and the loop's own wedge-confirm default when
	// they are set.
	noRestarts            bool
	backoff, wedgeConfirm time.Duration

	// The usage governor's fakes (governor_test.go). Like restartOn, each
	// switch is off unless a test turns it on, so every test written before
	// the loop had a governor runs as it did.
	//
	// usageOn sets Deps.Usage to fakeUsage, which works its verdict out with
	// the real governor.Decide from the fake account below and the rig's
	// clock, and counts its calls in usageCalls. usedPct and resetsAt are the
	// one usage window the account reports (a zero resetsAt is a window with
	// no known reset), and overage its extra-usage switch. readAt is when the
	// reading was taken; the zero time means "just now", a new reading on
	// every call. noReading is an account never read, unsupported a tool that
	// cannot report usage, and onUnknown the machine's on_unknown_usage.
	usageOn                bool
	usageCalls             int
	usedPct                float64
	resetsAt, readAt       time.Time
	overage                bool
	noReading, unsupported bool
	onUnknown              string
	// loginOn sets Deps.Login, which answers login and counts in loginCalls.
	loginOn    bool
	login      LoginView
	loginCalls int
	// stopOn sets Deps.StopSession. stops is every call made. A stop that
	// works takes the host away; stopErr is what it answers instead, and
	// stopIgnored and forceIgnored make the polite stop and the forced one
	// answer nil and leave the host running.
	stopOn                    bool
	stops                     []stopCall
	stopErr                   error
	stopIgnored, forceIgnored bool
	// markerFn, when set, is Deps.RateLimited. Unset, the loop reads the
	// real marker file from the session's folder, which rateLimit writes.
	markerFn func(worktree, sess string) (time.Time, bool)
}

// recoveryTakes is how far a fake restart or relaunch that works moves the
// clock, as the host's signal ladder would. It is what lets a test tell the
// restart time saved before the call from the one saved after. The limits
// every rig runs with unless a test says otherwise (maxRestarts and
// restartBackoff, the shipped defaults) are beside TestDecide.
const recoveryTakes = 9 * time.Second

// restartCall is one restart the loop asked for. counted is the entry's
// `restarts` as the ledger file held it while the call was being made.
type restartCall struct {
	worktree, session string
	counted           int
}

// relaunchCall is the same for a relaunch. paused is the entry's
// usage_pause_at as the ledger file held it while the call was being made:
// empty for every relaunch but one made with the pause still saved.
type relaunchCall struct {
	req     RelaunchRequest
	counted int
	paused  string
}

// stopCall is one stop the loop asked for. paused is the entry's
// usage_pause_at as the ledger file held it while the call was being made.
type stopCall struct {
	worktree, session string
	force             bool
	paused            string
}

// onDisk reads the entry that names sess straight from the ledger file, the
// way a daemon started a moment later would.
func (r *rig) onDisk(sess string) ledger.Entry {
	r.t.Helper()
	l, err := ledger.Load(r.root)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, e := range l.Tasks {
		if e.Session == sess {
			return e
		}
	}
	r.t.Fatalf("no ledger entry names the session %s", sess)
	return ledger.Entry{}
}

// fakeStop is Deps.StopSession: it records the call and the pause on disk,
// then answers as the test set it to. A stop that works takes the host away,
// as the real host removes its own files when it ends.
func (r *rig) fakeStop(worktree, sess string, force bool) error {
	r.stops = append(r.stops, stopCall{worktree, sess, force, r.onDisk(sess).UsagePauseAt})
	if r.stopErr != nil {
		return r.stopErr
	}
	if (force && r.forceIgnored) || (!force && r.stopIgnored) {
		return nil
	}
	if err := os.Remove(filepath.Join(r.sessionDir(sess), "hostup")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// fakeUsage is Deps.Usage: the real governor's verdict on the rig's fake
// account at the rig's clock, as the view the loop is handed. NextReset is
// worked out the way the real source does, from the one window there is.
func (r *rig) fakeUsage() UsageView {
	r.usageCalls++
	th := governor.DefaultThresholds()
	if r.onUnknown != "" {
		th.OnUnknownUsage = r.onUnknown
	}
	in := governor.Input{Unsupported: r.unsupported, SessionRunning: true, Now: *r.clock, Thresholds: th}
	v := UsageView{Consulted: true, At: *r.clock}
	if !r.unsupported && !r.noReading {
		at := r.readAt
		if at.IsZero() {
			at = *r.clock
		}
		in.Snapshot = &provider.UsageSnapshot{At: at, OverageEnabled: r.overage, Windows: []provider.Window{
			{Kind: provider.WindowSession, UsedPct: r.usedPct, ResetsAt: r.resetsAt}}}
		v.At = at
		if !r.resetsAt.IsZero() {
			v.NextReset = r.resetsAt.Add(time.Minute)
		}
	}
	d := governor.Decide(in)
	v.Action, v.Reason, v.WakeAt = d.Action, d.Reason, d.WakeAt
	return v
}

// countedOnDisk reads the restart count of the entry that names sess straight
// from the ledger file, the way a daemon started a moment later would.
func (r *rig) countedOnDisk(sess string) int {
	r.t.Helper()
	l, err := ledger.Load(r.root)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, e := range l.Tasks {
		if e.Session == sess {
			return e.Restarts
		}
	}
	r.t.Fatalf("no ledger entry names the session %s", sess)
	return 0
}

// wedge has the session's host say its agent stopped answering now, the way
// a host writes wedged_since into host.json. It is a file, like hostup, so
// it is still there for a rig made by restart.
func (r *rig) wedge(name string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.sessionDir(name), "wedged"), []byte(stamp(*r.clock)), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// unwedge clears the verdict, as the host does on the first byte of output.
func (r *rig) unwedge(name string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.sessionDir(name), "wedged")); err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
}

// fakeRestart is Deps.Restart: it records the call and the count on disk,
// then answers as the test set it to. A restart that works takes a while and
// leaves an agent that is answering again.
func (r *rig) fakeRestart(worktree, sess string) (string, error) {
	r.restarts = append(r.restarts, restartCall{worktree, sess, r.countedOnDisk(sess)})
	if r.restartErr != nil {
		return "", r.restartErr
	}
	if r.restartRefused != "" {
		return r.restartRefused, nil
	}
	r.advance(recoveryTakes)
	r.unwedge(sess)
	return "", nil
}

// fakeRelaunch is Deps.Relaunch. A relaunch that works takes a while and
// leaves a running host that has no verdict yet.
func (r *rig) fakeRelaunch(req RelaunchRequest) error {
	r.relaunches = append(r.relaunches, relaunchCall{req, r.countedOnDisk(req.Session), r.onDisk(req.Session).UsagePauseAt})
	if r.relaunchErr != nil {
		return r.relaunchErr
	}
	r.advance(recoveryTakes)
	r.unwedge(req.Session)
	return os.WriteFile(filepath.Join(r.sessionDir(req.Session), "hostup"), nil, 0o644)
}

// typed is one line the loop asked to have typed into a session.
type typed struct{ worktree, session, text string }

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
// entry) holding docs/PLAN.md and an empty ledger.
//
// The empty ledger is what `afk ledger init` writes for a new project, and it
// has to be there: a project with no ledger file at all launches nothing
// (TestTickNoLedger). The tests that mean "no ledger" say so with noLedger.
func newRig(t *testing.T, tasks ...masterplan.Task) *rig {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	r := &rig{t: t, root: root, home: t.TempDir(), clock: &clock, preflightOK: true}
	r.writePlan(planOf(tasks...))
	if err := ledger.Init(root, nil); err != nil {
		t.Fatal(err)
	}
	return r
}

// noLedger takes the ledger file away, leaving the project as one that has
// never had `afk ledger init` run in it.
func (r *rig) noLedger() *rig {
	r.t.Helper()
	if err := os.Remove(ledger.Path(r.root)); err != nil {
		r.t.Fatal(err)
	}
	return r
}

// ledgerText is the ledger file as it is on disk, for the tests that say the
// loop left it alone.
func (r *rig) ledgerText() string {
	r.t.Helper()
	data, err := os.ReadFile(ledger.Path(r.root))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(data)
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
	d := Deps{
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
		Clean:      r.clean,
	}
	if !r.noUrgent {
		d.NotifyUrgent = func(_ Project, it inbox.Item) { r.urgent = append(r.urgent, it) }
	}
	if r.shipFn != nil {
		d.Ship = func(req ShipRequest) ship.Outcome {
			r.shipReqs = append(r.shipReqs, req)
			return r.shipFn(req)
		}
	}
	if r.promptOn {
		d.SendPrompt = func(worktree, sess, text string) error {
			r.prompts = append(r.prompts, typed{worktree, sess, text})
			return r.promptErr
		}
	}
	if r.restartOn {
		d.Restart = r.fakeRestart
	}
	if r.relaunchOn {
		d.Relaunch = r.fakeRelaunch
	}
	if r.usageOn {
		d.Usage = r.fakeUsage
	}
	if r.loginOn {
		d.Login = func() LoginView { r.loginCalls++; return r.login }
	}
	if r.stopOn {
		d.StopSession = r.fakeStop
	}
	d.RateLimited = r.markerFn
	return d
}

// clean is the fake session clean-up. Like the real one it removes the
// worktree and is content when there is none.
func (r *rig) clean(_ Project, sess string) error {
	r.cleans = append(r.cleans, sess)
	if r.cleanErr != nil {
		return r.cleanErr
	}
	return os.RemoveAll(filepath.Join(r.root, "wt", sess))
}

// greenPath is the order of saved states of a ship where nothing goes wrong.
var greenPath = []ship.State{ship.NotStarted, ship.Committed, ship.Pushed, ship.PROpen, ship.CIGreen, ship.Merged, ship.DeployHealthy}

// greenShip is a ship script where nothing goes wrong: each call saves the
// next state and waits, as ship.Advance does after a step, and the call that
// finds deploy_healthy saved says done.
func (r *rig) greenShip(req ShipRequest) ship.Outcome {
	p := req.Progress
	if p.State == ship.DeployHealthy {
		return ship.Outcome{Kind: ship.OutcomeDone, Reason: "the deploy is healthy", Progress: p}
	}
	i := slices.Index(greenPath, p.State)
	if i < 0 {
		r.t.Fatalf("greenShip was handed the state %q", p.State)
	}
	p.State, p.At = greenPath[i+1], *r.clock
	switch p.State {
	case ship.PROpen:
		p.PR = 7
	case ship.Merged:
		p.MergedSHA = "abc1234"
	}
	if err := req.Save(p); err != nil {
		return ship.Outcome{Kind: ship.OutcomeWait, Reason: "the progress could not be saved: " + err.Error(), Progress: req.Progress}
	}
	return ship.Outcome{Kind: ship.OutcomeWait, Reason: "now " + string(p.State), Progress: p}
}

// toShipping runs T1.1 from nothing to `shipping`: launched, finished, handed
// over. The next tick is the first ship step.
func (r *rig) toShipping() {
	r.t.Helper()
	r.tick()
	r.advance(time.Minute)
	r.setPhase("t1-1", state.PhaseDone)
	r.tick()
	r.wantStatus("T1.1", ledger.Shipping)
}

// count says how many times kind is in the decision log.
func (r *rig) count(kind string) int {
	n := 0
	for _, k := range r.decisions() {
		if k == kind {
			n++
		}
	}
	return n
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
		// Like HostRunning, the verdict comes from host.json and so is set
		// for a session whose status cannot be read too.
		if data, err := os.ReadFile(filepath.Join(r.sessionDir(name), "wedged")); err == nil {
			ss.WedgedSince = string(data)
		}
		res := state.ReadStatus(statusPath)
		if res.Stale {
			ss.StatusStale, ss.StaleReason, ss.Kind = true, res.Reason, session.KindStaleStatus
			out = append(out, ss)
			continue
		}
		ss.SessionID, ss.Phase, ss.Branch = res.Status.SessionID, res.Status.Phase, res.Status.Branch
		ss.BaseBranch = res.Status.BaseBranch
		ss.AwaitingInput, ss.UpdatedAt = res.Status.AwaitingInput, res.Status.UpdatedAt
		if v, err := state.ParseVerdictFile(filepath.Join(r.sessionDir(name), "review-verdict.md")); err == nil {
			ss.Verdict = v
		}
		out = append(out, ss)
	}
	return out, nil
}

func (r *rig) settings() Settings {
	s := Settings{StallAfter: stallAfter, HostGrace: hostGrace,
		MaxRestarts: maxRestarts, RestartBackoff: restartBackoff, WedgeConfirm: r.wedgeConfirm}
	if r.noRestarts {
		s.MaxRestarts = 0
	}
	if r.backoff > 0 {
		s.RestartBackoff = r.backoff
	}
	return s
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
//   - TestTickShipNilKeepsShipping's twenty quiet ticks (and their twins in
//     the plan-error and preflight tests): the decision log counts things
//     that happened, not ticks.
//   - TestTickHappyPathToShipping: the whole line, from nothing to `done`
//     and on to the next task. Its count of clean-ups (one) and its check
//     that `done` leaves no ship field behind are what a later reader of the
//     ledger relies on.
//   - TestTickShipParkAndAnswer: one item however many ticks pass, and the
//     answer buys a fresh clock and fresh counters. Without the second half
//     a park that came from a limit parks again on the next step, for ever.
//     Its "before the ship saved anything" row is the only test of the rule
//     that a finished session goes back to shipping and not to running.
//   - TestTickShipStopsTheLine: nothing starts while the marker is there,
//     across a restart, and `afk resume` (ClearStop) is the only way on. Its
//     "killed before the item" row is what keeps a stopped line from being
//     silent.
//   - TestTickShipRequestCarriesTheSession: the ship gets the discovered
//     worktree and base. Every other ship test uses a fake that needs
//     neither, so this is the one that fails if the loop stops looking at a
//     shipping task's session.
//   - TestTickShipWithNoSessionOnDisk: cleaned up, then killed before the
//     ledger said done. The task must still end `done`.
//   - TestTickRelaunchClearsTheShip: a second attempt must not inherit the
//     first one's pull request.
//   - TestTickUnparkTypesTheResumeCommand: exactly one line, the resume
//     command and nothing else, and a send that fails still un-parks.
//   - TestTickNoLedger: a project with no ledger file must start nothing.
//     Its restart half is what fails if "already told" is ever remembered
//     instead of read from the inbox, and its last step (the item answers
//     itself and the first task starts) is what keeps the owner from having
//     to answer an item about a file they have just created.
//   - TestTickOwnerTask's "killed between the item and the ledger" row: the
//     item is written before the ledger, so a daemon killed there must find
//     its own item and not raise a second. Its 250-character row is the only
//     thing standing between a long plan heading and an item the inbox
//     refuses, which would leave the owner's task unannounced for ever.
//   - TestTickOwnerTask as a whole: no launch and no preflight for an owner
//     task, on any tick. That is the bug the rule exists for.
//   - TestTickStoppedLine: the stop holds back new work across a restart and
//     lets a running session finish. `afk resume` is the only way out.
func TestTickHappyPathToShipping(t *testing.T) {
	r := newRig(t, task("T1.1"), task("T1.2", "T1.1"))
	r.shipFn = r.greenShip

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
	if len(r.shipReqs) != 0 {
		t.Errorf("the ship was asked %d times while the session was still working", len(r.shipReqs))
	}

	r.advance(time.Minute)
	r.setPhase("t1-1", state.PhaseDone)
	st = r.tick()
	if st.Kind != string(ActShip) {
		t.Errorf("state on done = %+v", st)
	}
	if e := r.wantStatus("T1.1", ledger.Shipping); e.FinishedAt != "" || e.Session != "t1-1" || e.Ship != "" {
		t.Errorf("shipping entry = %+v", e)
	}
	r.wantDecisions("launch", "tokens", "ship_wait")

	// One ship step a tick: each leaves its state in the ledger, and the
	// task that depends on this one does not start meanwhile.
	for _, next := range greenPath[1:] {
		r.advance(5 * time.Second)
		st = r.tick()
		if st.Kind != string(ActShipStep) || st.Task != "T1.1" || !strings.Contains(st.Reason, string(next)) {
			t.Errorf("state after the step to %s = %+v", next, st)
		}
		if e := r.wantStatus("T1.1", ledger.Shipping); e.Ship != string(next) || e.ShipAt != stamp(*r.clock) {
			t.Errorf("entry after the step to %s = %+v", next, e)
		}
	}
	if len(r.cleans) != 0 || len(r.launches) != 1 {
		t.Errorf("cleans %v, launches %d before the ship was done; want none and 1", r.cleans, len(r.launches))
	}

	r.advance(5 * time.Second)
	st = r.tick()
	if st.Kind != string(ActShipStep) || !strings.Contains(st.Reason, "done") {
		t.Errorf("state on the last step = %+v", st)
	}
	e = r.wantStatus("T1.1", ledger.Done)
	if e.MergedSHA != "abc1234" || e.PR != 7 || e.FinishedAt != stamp(*r.clock) {
		t.Errorf("done entry = %+v, want the merge commit, the pull request and a finish time", e)
	}
	if e.Ship != "" || e.ShipAt != "" || e.CIReruns != 0 || e.CIFixCycles != 0 || len(e.ParkedOn) != 0 {
		t.Errorf("done entry still holds ship fields: %+v", e)
	}
	if !slices.Equal(r.cleans, []string{"t1-1"}) {
		t.Errorf("cleans = %v, want the session once", r.cleans)
	}
	r.wantDecisions("launch", "tokens", "ship_wait", "ship", "ship", "ship", "ship", "ship", "ship", "shipped")

	// And the line moves on.
	r.advance(5 * time.Second)
	st = r.tick()
	if st.Kind != string(ActLaunch) || st.Task != "T1.2" || len(r.launches) != 2 {
		t.Errorf("state after the ship = %+v, launches %d; want T1.2 launched", st, len(r.launches))
	}
	r.wantStatus("T1.2", ledger.Running)
	if !slices.Equal(r.cleans, []string{"t1-1"}) || len(r.items()) != 0 || len(r.notified) != 0 || len(r.urgent) != 0 {
		t.Errorf("cleans %v, items %d, notified %d, urgent %d; want one clean and nothing said",
			r.cleans, len(r.items()), len(r.notified), len(r.urgent))
	}
}

// TestTickShipNilKeepsShipping: with no shipper the loop is what it was
// before it could ship. A finished task waits in `shipping`; twenty more
// ticks change nothing, log nothing, clean nothing and do not start the task
// that depends on it.
func TestTickShipNilKeepsShipping(t *testing.T) {
	r := newRig(t, task("T1.1"), task("T1.2", "T1.1"))
	r.toShipping()
	r.wantDecisions("launch", "tokens", "ship_wait")
	before := r.ledgerText()

	st := r.ticks(20, 5*time.Second)
	if st.Kind != string(ActWait) || !strings.Contains(st.Reason, "waiting to ship") {
		t.Errorf("state while shipping = %+v", st)
	}
	r.wantDecisions("launch", "tokens", "ship_wait")
	r.wantStatus("T1.1", ledger.Shipping)
	r.wantStatus("T1.2", ledger.Pending)
	if after := r.ledgerText(); after != before {
		t.Errorf("the ledger changed with no shipper:\n%s", after)
	}
	if len(r.launches) != 1 || len(r.items()) != 0 || len(r.notified) != 0 || len(r.idle) != 0 || len(r.cleans) != 0 {
		t.Errorf("launches %d, items %d, notified %d, idle %d, cleans %d; want 1, 0, 0, 0, 0",
			len(r.launches), len(r.items()), len(r.notified), len(r.idle), len(r.cleans))
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
	before := r.ledgerText()
	st := r.ticks(5, 5*time.Second)
	if st.Kind != string(ActWait) || !strings.Contains(st.Reason, "paused") {
		t.Errorf("state while paused = %+v", st)
	}
	if len(r.launches) != 0 || r.preflights != 0 {
		t.Fatalf("launches %d, preflights %d while paused; want 0 and 0", len(r.launches), r.preflights)
	}
	if after := r.ledgerText(); after != before {
		t.Errorf("the ledger was changed while paused:\n%s", after)
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

	before := r.ledgerText()
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
	if after := r.ledgerText(); after != before {
		t.Errorf("the ledger was changed, want it left alone:\n%s", after)
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

// TestRunNewBinary: Run stops for a new program file only at the top of a
// pass. The load-bearing rows are "changed on the second pass" (the first
// pass ticks every project, the stopping pass ticks none, and each project's
// log gets exactly one daemon_restart line) and the four carry-on rows, since
// stopping on a doubt would have the service manager restart the daemon for
// nothing. A row ends its run by cancelling the context after the third
// pass's first read, so "carries on" is shown by Run still going until then.
func TestRunNewBinary(t *testing.T) {
	old := Binary{Path: "/bin/afk", Size: 10, ModTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	newer := Binary{Path: "/bin/afk", Size: 11, ModTime: old.ModTime}
	cases := []struct {
		name     string
		start    Binary
		nilDep   bool
		reads    func(call int) (Binary, error)
		wantStop bool
	}{
		{"unchanged", old, false, func(int) (Binary, error) { return old, nil }, false},
		{"changed on the second pass", old, false, func(call int) (Binary, error) {
			if call >= 2 {
				return newer, nil
			}
			return old, nil
		}, true},
		{"unreadable", old, false, func(int) (Binary, error) { return Binary{}, errors.New("gone") }, false},
		{"no dependency", old, true, nil, false},
		{"start unknown", Binary{}, false, func(int) (Binary, error) { return newer, nil }, false},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			a := newRig(t, task("T1.1"))
			b := newRig(t, task("T1.1"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := a.deps()
			ticked := map[string]int{}
			load := d.LoadBuild
			d.LoadBuild = func(root string) ([]masterplan.Task, string, ledger.Ledger, error) {
				ticked[root]++
				return load(root)
			}
			reads := 0
			if !tt.nilDep {
				d.Binary = func() (Binary, error) {
					reads++
					if reads >= 3 {
						cancel()
					}
					return tt.reads(reads)
				}
			}
			if tt.nilDep {
				// No dependency to count passes by: end after enough ticks.
				n := 0
				now := d.Now
				d.Now = func() time.Time {
					if n++; n >= 6 {
						cancel()
					}
					return now()
				}
			}
			s := a.settings()
			s.Binary = tt.start
			projects := []Project{a.project(), {Name: "other", Root: b.root}}
			err := Run(ctx, projects, s, time.Millisecond, d)
			if tt.wantStop {
				if !errors.Is(err, ErrNewBinary) {
					t.Fatalf("Run = %v, want ErrNewBinary", err)
				}
				for _, r := range []*rig{a, b} {
					if got := ticked[r.root]; got != 1 {
						t.Errorf("%s ticked %d times, want 1 (the first pass only)", r.root, got)
					}
					if got := r.count(KindDaemonRestart); got != 1 {
						t.Errorf("%s has %d daemon_restart lines, want 1", r.root, got)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
			if ticked[a.root] < 2 || ticked[b.root] < 2 {
				t.Errorf("ticks = %v, want Run to keep going", ticked)
			}
			if a.count(KindDaemonRestart) != 0 || b.count(KindDaemonRestart) != 0 {
				t.Error("a daemon_restart line was written without a change")
			}
		})
	}
}

// TestTickNoLedger: a project with no ledger file launches nothing and tells
// the owner once, over many ticks and a restart; once `afk ledger init` has
// run, the loop answers its own item and starts the first task that is ready.
//
// Both rows are load-bearing. "a new project" is the ordinary case. "a
// part-built project" is the one the rule was written for: the tasks already
// built must not be started again, so the first launch is the first task
// after them.
func TestTickNoLedger(t *testing.T) {
	cases := []struct {
		name       string
		done       []string
		wantLaunch string
	}{
		{name: "a new project", wantLaunch: "T1.1"},
		{name: "a part-built project", done: []string{"T1.1", "T1.2"}, wantLaunch: "T1.3"},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			first := newRig(t, task("T1.1"), task("T1.2", "T1.1"), task("T1.3", "T1.2")).noLedger()

			st := first.ticks(20, 5*time.Second)
			second := first.restart()
			st2 := second.ticks(20, 5*time.Second)

			items := second.items()
			if len(items) != 1 {
				t.Fatalf("items after 20 ticks, a restart and 20 more = %d, want exactly 1 (logs: %v)", len(items), first.logs)
			}
			it := items[0]
			if it.Title != NoLedgerTitle || it.Kind != "external_action" || !it.Blocking || it.Task != "" || it.Session != "" || !it.Open() {
				t.Errorf("no-ledger item = %+v", it)
			}
			wantContains(t, "the no-ledger body", it.Body,
				"afk ledger init --project "+first.root+"\n",
				"afk ledger init --project "+first.root+" --done-through T",
				"--done T", first.root)
			t.Logf("no-ledger item %s: %s", it.ID, it.Body)

			if n := len(first.launches) + len(second.launches); n != 0 {
				t.Errorf("launches with no ledger = %d, want 0", n)
			}
			if n := first.preflights + second.preflights; n != 0 {
				t.Errorf("preflights with no ledger = %d, want 0: nothing was about to launch", n)
			}
			if len(first.notified) != 1 || len(second.notified) != 0 {
				t.Errorf("notified %d then %d, want 1 then 0", len(first.notified), len(second.notified))
			}
			if ledger.Exists(first.root) {
				t.Error("the loop created a ledger file; only `afk ledger init` may")
			}
			for _, s := range []daemonstate.State{st, st2} {
				if s.Kind != string(ActNoLedger) || s.Task != "" || !strings.Contains(s.Reason, "waiting on the owner") || !strings.Contains(s.Reason, it.ID) {
					t.Errorf("state with no ledger = %+v, want no_ledger, waiting on the owner, naming item %s", s, it.ID)
				}
			}
			second.wantDecisions("no_ledger")

			// The owner runs `afk ledger init`.
			if err := ledger.Init(second.root, tt.done); err != nil {
				t.Fatal(err)
			}
			second.advance(5 * time.Second)
			st = second.tick()

			items = second.items()
			if len(items) != 1 || items[0].Open() || items[0].Answer != "created" || items[0].AnsweredVia != "cli" {
				t.Errorf("items after the ledger was created = %+v, want the one item answered by afk", items)
			}
			if len(second.launches) != 1 || second.launches[0].Task != tt.wantLaunch {
				t.Fatalf("launches after the ledger was created = %+v, want %s", second.launches, tt.wantLaunch)
			}
			if st.Kind != string(ActLaunch) || st.Task != tt.wantLaunch {
				t.Errorf("state after the ledger was created = %+v", st)
			}
			second.wantStatus(tt.wantLaunch, ledger.Running)
			second.wantDecisions("no_ledger", "ledger_found", "launch")
		})
	}
}

// TestTickNoLedgerAnsweredWithoutALedger: answering the item does not make a
// ledger, so the project still cannot be built and the owner is told again.
// It is the same reading the preflight item gets: an answer means "look
// again", and what the loop finds is what it says.
func TestTickNoLedgerAnsweredWithoutALedger(t *testing.T) {
	r := newRig(t, task("T1.1")).noLedger()
	r.ticks(5, 5*time.Second)
	r.answer(r.items()[0].ID)
	r.ticks(20, 5*time.Second)

	open := 0
	for _, it := range r.items() {
		if it.Open() && it.Title == NoLedgerTitle {
			open++
		}
	}
	if open != 1 || len(r.items()) != 2 || len(r.launches) != 0 {
		t.Errorf("open no-ledger items %d of %d, launches %d; want 1 of 2 and 0", open, len(r.items()), len(r.launches))
	}
}

// TestTickOwnerBody: the design task's item sends the owner to the reconcile
// before the answer; every other owner task keeps its old body byte for byte.
//
// Load-bearing rows: "an ordinary owner task" compares with the old text
// verbatim, so a design hand-off cannot leak into other owner tasks; "the
// design task" checks the reconcile text comes before the answer command,
// because answering is what releases the interface tasks. Both stay one
// blocking external_action item and park the task.
func TestTickOwnerBody(t *testing.T) {
	const oldBody = "T0.0 — %s\n\n" +
		"The plan marks this task as yours to do, not an agent's, so afk has not started anything for it. " +
		"Tasks that depend on it wait until it is done.\n\n" +
		"What to do next:\n" +
		"1. Do the task. Its steps are under T0.0 in %s.\n" +
		"2. When it is finished, answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). " +
		"Answering it is what marks T0.0 as done, so answer only when the work really is finished."

	cases := []struct {
		name   string
		title  string
		design bool
	}{
		{name: "the design task", title: "Design system (owner; not an agent task)", design: true},
		{name: "an ordinary owner task", title: "Create the hosting account (owner)"},
		{name: "a look-alike owner task", title: "Design system for the admin pages (owner)"},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			r := newRig(t, masterplan.Task{ID: "T0.0", Title: tt.title}, task("T1.1", "T0.0"))
			r.tick()
			items := r.items()
			if len(items) != 1 {
				t.Fatalf("items = %d, want 1", len(items))
			}
			it := items[0]
			if it.Kind != "external_action" || !it.Blocking || !it.Open() {
				t.Errorf("owner item = %+v", it)
			}
			r.wantStatus("T0.0", ledger.Parked)
			t.Logf("owner item %s:\n%s", it.ID, it.Body)
			if !tt.design {
				if strings.Contains(it.Body, "reconcile") {
					t.Errorf("an ordinary owner task is sent to a reconcile:\n%s", it.Body)
				}
				if want := fmt.Sprintf(oldBody, tt.title, "docs/PLAN.md"); it.Body != want {
					t.Errorf("body changed:\n%q\nwant\n%q", it.Body, want)
				}
				return
			}
			wantContains(t, "the design item's body", it.Body,
				"docs/design-brief.md", "docs/design-system.md", "commit it", "only sees committed files",
				"afk plan reconcile docs/design-system.md", "afk inbox answer", "after the reconcile")
			rec := strings.Index(it.Body, "afk plan reconcile docs/design-system.md")
			ans := strings.Index(it.Body, "afk inbox answer")
			if rec < 0 || ans < 0 || rec > ans {
				t.Errorf("reconcile at %d, answer at %d; want the reconcile first", rec, ans)
			}
		})
	}
}

// TestTickOwnerTask: a task that is the owner's raises one blocking item and
// starts no agent, however many ticks pass; the owner's answer marks it done,
// and only then does the task that depends on it launch.
//
// Load-bearing rows are named in the comment above TestTickHappyPathToShipping.
func TestTickOwnerTask(t *testing.T) {
	const marker = " (owner; not an agent task)"
	long := strings.Repeat("Stand up the build machine and ", 8)
	long = long[:250-len(marker)] + marker
	if n := len(long); n != 250 {
		t.Fatalf("the long title is %d characters, want 250", n)
	}

	cases := []struct {
		name  string
		title string
		// killed runs the first tick as a daemon that died after writing
		// the item and before writing the ledger.
		killed    bool
		wantTitle string
	}{
		{name: "an owner task", title: "Stand up the VM" + marker,
			wantTitle: "Your task: T0.0 — Stand up the VM (owner; not an agent task)"},
		{name: "killed between the item and the ledger", title: "Stand up the VM" + marker, killed: true,
			wantTitle: "Your task: T0.0 — Stand up the VM (owner; not an agent task)"},
		{name: "a 250-character title is cut to fit the inbox", title: long},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			r := newRig(t, masterplan.Task{ID: "T0.0", Title: tt.title}, task("T1.1", "T0.0"))

			if tt.killed {
				// What the dead daemon left: its item, and a ledger that
				// still says pending.
				if _, err := r.store().Add(inbox.NewItem{Task: "T0.0", Kind: "external_action", Blocking: true,
					Title: ownerTitle("T0.0", tt.title), Body: "left by a daemon that was killed"}); err != nil {
					t.Fatal(err)
				}
				r.wantStatus("T0.0", ledger.Pending)
			}

			st := r.tick()
			items := r.items()
			if len(items) != 1 {
				t.Fatalf("items after the first tick = %d, want 1 (logs: %v)", len(items), r.logs)
			}
			it := items[0]
			if it.Kind != "external_action" || !it.Blocking || it.Task != "T0.0" || it.Session != "" || !it.Open() {
				t.Errorf("owner item = %+v", it)
			}
			if tt.wantTitle != "" && it.Title != tt.wantTitle {
				t.Errorf("owner item title = %q, want %q", it.Title, tt.wantTitle)
			}
			if n := len([]rune(it.Title)); n > maxItemTitle || !strings.HasPrefix(it.Title, OwnerTitlePrefix+"T0.0 — ") {
				t.Errorf("owner item title is %d characters: %q", n, it.Title)
			}
			if !tt.killed {
				wantContains(t, "the owner item's body", it.Body,
					"T0.0 — "+tt.title, "yours to do", "has not started anything", "afk inbox answer", "marks T0.0 as done", "docs/PLAN.md")
				t.Logf("owner item %s: %s\n%s", it.ID, it.Title, it.Body)
				if len(r.notified) != 1 || r.notified[0].ID != it.ID {
					t.Errorf("notified = %+v, want the stored item once", r.notified)
				}
			} else if len(r.notified) != 0 {
				t.Errorf("notified = %+v, want nothing: the item was already there", r.notified)
			}
			e := r.wantStatus("T0.0", ledger.Parked)
			if !slices.Equal(e.ParkedOn, []string{it.ID}) || e.Session != "" || e.Branch != "" || e.Attempts != 0 || e.StartedAt != "" {
				t.Errorf("owner entry = %+v, want parked on %s with no session, branch, attempt or start", e, it.ID)
			}
			if st.Kind != string(ActOwner) || st.Task != "T0.0" || st.Session != "" ||
				!strings.Contains(st.Reason, "waiting on the owner") || !strings.Contains(st.Reason, it.ID) {
				t.Errorf("state = %+v, want owner_task, waiting on the owner, naming item %s", st, it.ID)
			}
			r.wantDecisions("owner_task")

			// Twenty ticks and a restart: still one item, and no agent.
			r.ticks(10, time.Minute)
			again := r.restart()
			st = again.ticks(10, time.Minute)
			if n := len(again.items()); n != 1 {
				t.Errorf("items after twenty more ticks = %d, want 1", n)
			}
			if n := len(r.launches) + len(again.launches) + r.preflights + again.preflights; n != 0 {
				t.Errorf("launches and preflights for an owner task = %d, want 0", n)
			}
			if st.Kind != string(ActIdle) || !strings.Contains(st.Reason, "waiting on owner") || !strings.Contains(st.Reason, it.ID) {
				t.Errorf("state while the owner works = %+v", st)
			}
			again.wantStatus("T0.0", ledger.Parked)
			again.wantStatus("T1.1", ledger.Pending)
			again.wantDecisions("owner_task")
			if _, err := os.Stat(filepath.Join(again.root, "wt")); !os.IsNotExist(err) {
				t.Errorf("a session folder exists for an owner task (stat err %v)", err)
			}

			// The owner does the task and answers.
			again.answer(it.ID)
			again.advance(5 * time.Second)
			st = again.tick()
			if st.Kind != string(ActOwnerDone) || st.Task != "T0.0" {
				t.Errorf("state on the answer = %+v", st)
			}
			e = again.wantStatus("T0.0", ledger.Done)
			if e.FinishedAt != stamp(*again.clock) || len(e.ParkedOn) != 0 || e.Session != "" || e.Attempts != 0 {
				t.Errorf("done owner entry = %+v, want finished now with nothing parked and no session", e)
			}
			if len(again.launches) != 0 {
				t.Errorf("launches on the tick the owner task finished = %+v, want none", again.launches)
			}

			again.advance(5 * time.Second)
			again.tick()
			if len(again.launches) != 1 || again.launches[0].Task != "T1.1" {
				t.Fatalf("launches after the owner task = %+v, want T1.1", again.launches)
			}
			again.wantStatus("T1.1", ledger.Running)
			again.wantDecisions("owner_task", "owner_done", "launch")
			if n := len(again.items()); n != 1 {
				t.Errorf("items at the end = %d, want 1", n)
			}
		})
	}
}

// TestTickOwnerAndAgentTaskLaunches: "(owner + agent)" in a title is an
// ordinary task. Without this the marker would swallow agent work the owner
// only helps with.
func TestTickOwnerAndAgentTaskLaunches(t *testing.T) {
	r := newRig(t, masterplan.Task{ID: "T0.1", Title: "Wire up the deploy key (owner + agent)"})
	st := r.tick()
	if len(r.launches) != 1 || r.launches[0].Task != "T0.1" || st.Kind != string(ActLaunch) {
		t.Fatalf("launches = %+v, state = %+v; want T0.1 launched", r.launches, st)
	}
	r.wantStatus("T0.1", ledger.Running)
	if n := len(r.items()); n != 0 {
		t.Errorf("items = %d, want 0", n)
	}
}

// TestTickOwnerTaskWhilePaused: `afk pause` promises that no new task starts,
// and nothing about an owner task starts anything, so a paused project still
// raises the owner's item and still marks the task done on the answer. The
// agent task behind it stays held back, which is the half that is
// load-bearing: letting owner tasks through must not let a launch through.
func TestTickOwnerTaskWhilePaused(t *testing.T) {
	r := newRig(t, ownerTask("T0.0"), task("T1.1", "T0.0"))
	if err := daemonstate.Pause(r.root, *r.clock); err != nil {
		t.Fatal(err)
	}
	r.ticks(5, 5*time.Second)
	items := r.items()
	if len(items) != 1 || !strings.HasPrefix(items[0].Title, OwnerTitlePrefix) {
		t.Fatalf("items while paused = %+v, want the owner's item", items)
	}
	r.wantStatus("T0.0", ledger.Parked)

	r.answer(items[0].ID)
	st := r.ticks(5, 5*time.Second)
	r.wantStatus("T0.0", ledger.Done)
	r.wantStatus("T1.1", ledger.Pending)
	if len(r.launches) != 0 || r.preflights != 0 {
		t.Errorf("launches %d, preflights %d while paused; want 0 and 0", len(r.launches), r.preflights)
	}
	if st.Kind != string(ActWait) || !strings.Contains(st.Reason, "paused") {
		t.Errorf("state after the owner task, still paused = %+v", st)
	}
	r.wantDecisions("owner_task", "owner_done")
}

// TestTickStoppedLine: with the stop marker in place nothing new starts, not
// even an owner's item, across a restart; a session already running is still
// watched to the end; and clearing the marker (what `afk resume` does) lets
// the build go on.
func TestTickStoppedLine(t *testing.T) {
	t.Run("nothing_new_starts", func(t *testing.T) {
		r := newRig(t, ownerTask("T0.0"), task("T1.1"))
		if err := daemonstate.Stop(r.root, "T0.9", "the deploy did not become healthy in 15m", *r.clock); err != nil {
			t.Fatal(err)
		}
		before := r.ledgerText()
		r.ticks(10, 5*time.Second)
		again := r.restart()
		st := again.ticks(10, 5*time.Second)

		if st.Kind != KindLineStopped || st.Task != "" {
			t.Errorf("state = %+v, want line_stopped with no task", st)
		}
		wantContains(t, "the stopped reason", st.Reason, "the line is stopped", "the deploy did not become healthy in 15m", "afk resume")
		if n := len(r.launches) + len(again.launches) + r.preflights + again.preflights; n != 0 {
			t.Errorf("launches and preflights on a stopped line = %d, want 0", n)
		}
		if n := len(again.items()); n != 0 {
			t.Errorf("items on a stopped line = %d, want 0: the stop holds the owner's task back too", n)
		}
		if after := again.ledgerText(); after != before {
			t.Errorf("the ledger was changed on a stopped line:\n%s", after)
		}
		again.wantDecisions()

		if _, err := daemonstate.ClearStop(again.root); err != nil {
			t.Fatal(err)
		}
		again.advance(5 * time.Second)
		again.tick()
		if n := len(again.items()); n != 1 {
			t.Errorf("items after the stop was cleared = %d, want the owner's item", n)
		}
		again.wantStatus("T0.0", ledger.Parked)
	})

	t.Run("a_running_session_is_still_watched", func(t *testing.T) {
		r := newRig(t, task("T1.1"), task("T1.2"))
		r.tick()
		if err := daemonstate.Stop(r.root, "T0.9", "", *r.clock); err != nil {
			t.Fatal(err)
		}
		r.advance(time.Minute)
		r.setPhase("t1-1", state.PhaseExecuting)
		if st := r.tick(); st.Kind != string(ActWait) || st.Task != "T1.1" {
			t.Errorf("state for a working session on a stopped line = %+v, want an ordinary wait", st)
		}
		r.advance(time.Minute)
		r.setPhase("t1-1", state.PhaseFailed)
		r.tick()
		r.wantStatus("T1.1", ledger.Failed)

		// T1.2 does not depend on the failed task, and would start at once
		// on a line that was not stopped.
		st := r.ticks(10, 5*time.Second)
		if st.Kind != KindLineStopped || len(r.launches) != 1 {
			t.Errorf("state = %+v, launches = %d; want line_stopped and only the first launch", st, len(r.launches))
		}
		r.wantStatus("T1.2", ledger.Pending)
	})

	t.Run("an_injected_reader_is_used", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		d := r.deps()
		d.Stopped = func(root string) (string, bool) { return "said by the test for " + filepath.Base(root), true }
		st := Tick(r.project(), r.settings(), d)
		if st.Kind != KindLineStopped || !strings.Contains(st.Reason, "said by the test for "+filepath.Base(r.root)) || len(r.launches) != 0 {
			t.Errorf("state = %+v, launches = %d", st, len(r.launches))
		}
		d.LedgerExists = func(string) bool { return false }
		if st := Tick(r.project(), r.settings(), d); st.Kind != string(ActNoLedger) {
			t.Errorf("state with an injected LedgerExists saying no = %+v, want no_ledger", st)
		}
	})
}

// TestKinds: Kinds is the list the status report is tested against, so a kind
// missing from it is a kind that can reach the owner as a bare code word. The
// list is checked against every constant by name, which is the part that goes
// red when a new kind is added and the list is forgotten.
func TestKinds(t *testing.T) {
	want := []string{
		string(ActStop), string(ActWait), string(ActCreate), string(ActShip), string(ActFail),
		string(ActPark), string(ActHostGone), string(ActStall), string(ActUnpark), string(ActLaunch),
		string(ActIdle), string(ActNoLedger), string(ActOwner), string(ActOwnerDone),
		string(ActShipStep),
		string(ActRestart), string(ActRelaunch), string(ActContinue), string(ActGiveUp),
		string(ActUsageWait), string(ActLoginWait), string(ActUsagePause),
		string(ActUsagePaused), string(ActUsageStuck), string(ActUsageResume),
		KindStopped, KindBlocked, KindLineStopped,
	}
	got := Kinds()
	seen := map[string]bool{}
	for _, k := range got {
		if k == "" || seen[k] {
			t.Errorf("Kinds holds %q empty or twice: %v", k, got)
		}
		seen[k] = true
	}
	for _, k := range want {
		if !seen[k] {
			t.Errorf("Kinds lacks %q", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("Kinds has %d kinds, want %d: %v", len(got), len(want), got)
	}
	if KindLineStopped == KindStopped {
		t.Error("the stopped line shares a kind with the missing project folder")
	}
}

// setEntry writes one ledger entry as it stands, for the tests that start in
// the middle of a ship without running the steps that lead there.
func (r *rig) setEntry(e ledger.Entry) {
	r.t.Helper()
	err := ledger.Update(r.root, func(l *ledger.Ledger) error {
		for i := range l.Tasks {
			if l.Tasks[i].ID == e.ID {
				l.Tasks[i] = e
				return nil
			}
		}
		l.Tasks = append(l.Tasks, e)
		return nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

// saveAndWait is what a ship script does after a step: save, and wait.
func saveAndWait(t *testing.T, req ShipRequest, p ship.Progress) ship.Outcome {
	t.Helper()
	if err := req.Save(p); err != nil {
		t.Errorf("Save(%+v) = %v", p, err)
		return ship.Outcome{Kind: ship.OutcomeWait, Reason: err.Error(), Progress: req.Progress}
	}
	return ship.Outcome{Kind: ship.OutcomeWait, Reason: "now " + string(p.State), Progress: p}
}

// TestTickShipRequestCarriesTheSession: the ship is handed the worktree and
// the base branch of the session as discovered on this tick, with the entry
// and its saved progress. The base is set to something other than the rig's
// default so the test can only pass by reading it from the session.
func TestTickShipRequestCarriesTheSession(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.shipFn = r.greenShip
	r.tick()
	r.advance(time.Minute)
	r.writeStatus("t1-1", func(st *state.Status) { st.Phase = state.PhaseDone; st.BaseBranch = "develop" })
	r.tick()
	r.wantStatus("T1.1", ledger.Shipping)

	r.advance(5 * time.Second)
	r.tick()
	if len(r.shipReqs) != 1 {
		t.Fatalf("ship calls = %d, want 1 (logs: %v)", len(r.shipReqs), r.logs)
	}
	req := r.shipReqs[0]
	if want := filepath.Join(r.root, "wt", "t1-1"); req.Worktree != want {
		t.Errorf("Worktree = %q, want the discovered path %q", req.Worktree, want)
	}
	if req.Base != "develop" {
		t.Errorf("Base = %q, want the session's base_branch develop", req.Base)
	}
	if req.Project != r.project() || req.Entry.ID != "T1.1" || req.Entry.Session != "t1-1" || req.Entry.Branch != "t1-1" {
		t.Errorf("request = project %+v, entry %+v", req.Project, req.Entry)
	}
	if req.Progress != (ship.Progress{}) {
		t.Errorf("first Progress = %+v, want none", req.Progress)
	}
	if req.Save == nil || req.Stop == nil {
		t.Error("the request has no Save or no Stop")
	}

	r.advance(5 * time.Second)
	r.tick()
	if got := r.shipReqs[1]; got.Progress.State != ship.Committed || got.Entry.Ship != "committed" || got.Base != "develop" {
		t.Errorf("second request = progress %+v, entry %+v, base %q", got.Progress, got.Entry, got.Base)
	}
}

// TestTickShipRestartPassesSavedProgress: a daemon killed in the middle of a
// ship and started again hands the ship exactly what the ledger held, and the
// ship goes on from there. Nothing but the disk is carried over.
func TestTickShipRestartPassesSavedProgress(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.shipFn = r.greenShip
	r.toShipping()
	r.ticks(3, 5*time.Second) // committed, pushed, pr_open
	savedAt := *r.clock
	if e := r.entry("T1.1"); e.Ship != "pr_open" || e.PR != 7 {
		t.Fatalf("entry before the restart = %+v", e)
	}

	again := r.restart()
	again.shipFn = again.greenShip
	again.advance(time.Minute)
	st := again.tick()
	if len(again.shipReqs) != 1 {
		t.Fatalf("ship calls after the restart = %d, want 1", len(again.shipReqs))
	}
	p := again.shipReqs[0].Progress
	if p.State != ship.PROpen || p.PR != 7 || !p.At.Equal(savedAt) || p.Reruns != 0 || p.FixCycles != 0 || p.MergedSHA != "" {
		t.Errorf("Progress after the restart = %+v, want pr_open, pull request 7, entered at %s", p, stamp(savedAt))
	}
	if st.Kind != string(ActShipStep) || again.entry("T1.1").Ship != "ci_green" {
		t.Errorf("state = %+v, entry = %+v; want the ship to go on to ci_green", st, again.entry("T1.1"))
	}
	if n := again.count("ship"); n != 4 {
		t.Errorf("ship lines in the decision log = %d, want 4 (one per state, none for the restart)", n)
	}
	if len(again.launches) != 0 || len(again.tokenIDs) != 0 {
		t.Errorf("the restart launched %d sessions and recorded tokens %d times; want neither", len(again.launches), len(again.tokenIDs))
	}
}

// TestTickShipSaveIsOnDiskBeforeShipReturns: Save is the ledger write itself,
// not a note to write later. The script reads the ledger file back before it
// returns, the way a daemon killed right after an action would find it.
//
// The rows inside: a change of state is one decision-log line; a save that
// only moves a counter is none; and a Save after the owner has moved the task
// by hand is refused and changes nothing, so a ship cannot write over them.
func TestTickShipSaveIsOnDiskBeforeShipReturns(t *testing.T) {
	r := newRig(t, task("T1.1"))
	var seen []ledger.Entry
	var movedErr error
	r.shipFn = func(req ShipRequest) ship.Outcome {
		p := req.Progress
		switch len(r.shipReqs) {
		case 1:
			p = ship.Progress{State: ship.PROpen, At: *r.clock, PR: 12}
		case 2:
			p.Reruns, p.At = 1, *r.clock
		case 3:
			p.State, p.Reruns, p.FixCycles, p.At = ship.HandedBack, 0, 1, *r.clock
		default:
			err := ledger.Update(r.root, func(l *ledger.Ledger) error {
				_, err := l.Set("T1.1", ledger.Failed, nil)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			p.State = ship.PROpen
			movedErr = req.Save(p)
			return ship.Outcome{Kind: ship.OutcomeWait, Reason: "could not save", Progress: req.Progress}
		}
		out := saveAndWait(t, req, p)
		seen = append(seen, r.entry("T1.1"))
		return out
	}
	r.toShipping()
	var at []string
	for range 3 {
		r.advance(5 * time.Second)
		at = append(at, stamp(*r.clock))
		r.tick()
	}
	if len(seen) != 3 {
		t.Fatalf("saves seen = %d, want 3 (logs: %v)", len(seen), r.logs)
	}
	if e := seen[0]; e.Status != ledger.Shipping || e.Ship != "pr_open" || e.PR != 12 || e.ShipAt != at[0] {
		t.Errorf("ledger inside the first call = %+v", e)
	}
	if e := seen[1]; e.Ship != "pr_open" || e.CIReruns != 1 || e.ShipAt != at[1] {
		t.Errorf("ledger inside the second call = %+v", e)
	}
	if e := seen[2]; e.Ship != "handed_back" || e.CIReruns != 0 || e.CIFixCycles != 1 || e.PR != 12 {
		t.Errorf("ledger inside the third call = %+v", e)
	}
	if n := r.count("ship"); n != 2 {
		t.Errorf("ship lines in the decision log = %d, want 2: pr_open and handed_back, none for the re-run", n)
	}

	r.advance(5 * time.Second)
	r.tick()
	if movedErr == nil {
		t.Error("Save succeeded on an entry the owner had set to failed")
	}
	if e := r.wantStatus("T1.1", ledger.Failed); e.Ship != "handed_back" {
		t.Errorf("entry after the refused save = %+v, want it left as the owner set it", e)
	}
}

const capTitle = "CI for T1.1 is still failing after afk's fixes"

// capScript is a ship that reaches an open pull request with its hand-backs
// used up, and parks for as long as they stay used up. With fresh counters
// it goes on down the green path.
func capScript(r *rig) func(ShipRequest) ship.Outcome {
	return func(req ShipRequest) ship.Outcome {
		p := req.Progress
		switch {
		case p.State == ship.NotStarted:
			return saveAndWait(r.t, req, ship.Progress{State: ship.PROpen, At: *r.clock, PR: 7, Reruns: 1, FixCycles: 2})
		case p.State == ship.PROpen && p.FixCycles >= 2:
			return ship.Outcome{Kind: ship.OutcomePark, Reason: "CI is still failing after 2 fixes", Progress: p,
				Item: &ship.Item{Kind: ship.ItemFailure, Title: capTitle, Body: "Checks still fail. Fix the branch, then answer this item."}}
		}
		return r.greenShip(req)
	}
}

// TestTickShipParkAndAnswer: a ship that cannot go on parks the task on one
// blocking item, however many ticks pass, and the owner's answer sends it
// back to shipping with a fresh clock and fresh counters, so the retry goes
// on instead of parking again.
func TestTickShipParkAndAnswer(t *testing.T) {
	t.Run("one_item_and_the_answer_buys_a_fresh_try", func(t *testing.T) {
		r := newRig(t, task("T1.1"), task("T1.2", "T1.1"))
		r.promptOn = true
		r.shipFn = capScript(r)
		r.toShipping()
		r.ticks(1, 5*time.Second)
		r.advance(5 * time.Second)
		st := r.tick()

		items := r.items()
		if len(items) != 1 {
			t.Fatalf("items = %d, want 1 (logs: %v)", len(items), r.logs)
		}
		it := items[0]
		if !it.Blocking || it.Task != "T1.1" || it.Session != "t1-1" || it.Kind != "failure" || it.Title != capTitle {
			t.Errorf("item = %+v", it)
		}
		if st.Kind != string(ActPark) || st.Task != "T1.1" || !strings.Contains(st.Reason, it.ID) {
			t.Errorf("state on park = %+v", st)
		}
		e := r.wantStatus("T1.1", ledger.Parked)
		if !slices.Equal(e.ParkedOn, []string{it.ID}) || e.Ship != "pr_open" || e.PR != 7 || e.CIFixCycles != 2 || e.CIReruns != 1 {
			t.Errorf("parked entry = %+v, want it parked on the item with its ship kept", e)
		}

		calls := len(r.shipReqs)
		st = r.ticks(20, 5*time.Second)
		if st.Kind != string(ActIdle) || !strings.Contains(st.Reason, it.ID) {
			t.Errorf("state while parked = %+v", st)
		}
		if len(r.items()) != 1 || len(r.notified) != 1 || len(r.shipReqs) != calls || r.count("park") != 1 {
			t.Errorf("items %d, notified %d, new ship calls %d, park lines %d; want 1, 1, 0, 1",
				len(r.items()), len(r.notified), len(r.shipReqs)-calls, r.count("park"))
		}

		r.answer(it.ID)
		r.advance(5 * time.Second)
		st = r.tick()
		unparkedAt := *r.clock
		if st.Kind != string(ActUnpark) {
			t.Errorf("state on answer = %+v", st)
		}
		e = r.wantStatus("T1.1", ledger.Shipping)
		if len(e.ParkedOn) != 0 || e.Ship != "pr_open" || e.PR != 7 {
			t.Errorf("un-parked entry = %+v, want the same ship", e)
		}
		if e.ShipAt != stamp(unparkedAt) || e.CIReruns != 0 || e.CIFixCycles != 0 {
			t.Errorf("un-parked entry = %+v, want ship_at now and both counters zero", e)
		}
		if len(r.prompts) != 0 {
			t.Errorf("prompts = %v, want none: a finished session is not told to resume", r.prompts)
		}

		// The retry proceeds: the ship sees the fresh counters and goes on.
		r.advance(5 * time.Second)
		r.tick()
		p := r.shipReqs[len(r.shipReqs)-1].Progress
		if p.State != ship.PROpen || p.FixCycles != 0 || p.Reruns != 0 || !p.At.Equal(unparkedAt) {
			t.Errorf("Progress after the answer = %+v, want pr_open with fresh counters and clock", p)
		}
		if e := r.wantStatus("T1.1", ledger.Shipping); e.Ship != "ci_green" {
			t.Errorf("entry after the retry = %+v, want ci_green", e)
		}
		r.ticks(3, 5*time.Second)
		r.wantStatus("T1.1", ledger.Done)
		if len(r.items()) != 1 || r.count("park") != 1 || r.count("tokens") != 1 {
			t.Errorf("items %d, park lines %d, tokens lines %d; want 1 each", len(r.items()), r.count("park"), r.count("tokens"))
		}
	})

	t.Run("killed_between_the_item_and_the_ledger", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		r.shipFn = capScript(r)
		r.toShipping()
		r.ticks(1, 5*time.Second)
		// The dead daemon got as far as the item.
		earlier, err := r.store().Add(inbox.NewItem{Task: "T1.1", Session: "t1-1", Kind: "failure", Blocking: true, Title: capTitle, Body: "said before"})
		if err != nil {
			t.Fatal(err)
		}
		r.ticks(1, 5*time.Second)
		if n := len(r.items()); n != 1 {
			t.Errorf("items = %d, want only the one already raised", n)
		}
		if e := r.wantStatus("T1.1", ledger.Parked); !slices.Equal(e.ParkedOn, []string{earlier.ID}) {
			t.Errorf("parked on %v, want [%s]", e.ParkedOn, earlier.ID)
		}
		if len(r.notified) != 0 {
			t.Errorf("notified = %d, want 0: the owner was told by the daemon that died", len(r.notified))
		}
	})

	t.Run("the_same_title_for_another_task_is_not_this_task's_item", func(t *testing.T) {
		r := newRig(t, task("T1.1"), task("T1.2"))
		r.shipFn = capScript(r)
		r.toShipping()
		r.ticks(1, 5*time.Second)
		if _, err := r.store().Add(inbox.NewItem{Task: "T1.2", Kind: "failure", Title: capTitle}); err != nil {
			t.Fatal(err)
		}
		r.ticks(1, 5*time.Second)
		if n := len(r.items()); n != 2 {
			t.Errorf("items = %d, want 2: T1.1 needs its own", n)
		}
		r.wantStatus("T1.1", ledger.Parked)
	})

	t.Run("parked_before_the_ship_saved_anything", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		r.promptOn = true
		ready := false
		r.shipFn = func(req ShipRequest) ship.Outcome {
			if !ready {
				return ship.Outcome{Kind: ship.OutcomePark, Reason: "gh is not logged in", Progress: req.Progress,
					Item: &ship.Item{Kind: ship.ItemEnvVar, Title: "T1.1 cannot ship: GitHub's command-line tool is not ready", Body: "Run `gh auth login`."}}
			}
			return r.greenShip(req)
		}
		r.toShipping()
		r.ticks(1, 5*time.Second)
		e := r.wantStatus("T1.1", ledger.Parked)
		if e.Ship != "" {
			t.Fatalf("parked entry = %+v, want no ship state for this row to mean anything", e)
		}
		items := r.items()
		if len(items) != 1 || items[0].Kind != "env_var" || !items[0].Blocking {
			t.Fatalf("items = %+v, want one blocking item of the ship's own kind", items)
		}

		ready = true
		r.answer(items[0].ID)
		r.advance(5 * time.Second)
		st := r.tick()
		if st.Kind != string(ActUnpark) {
			t.Errorf("state on answer = %+v", st)
		}
		if e := r.wantStatus("T1.1", ledger.Shipping); e.ShipAt != stamp(*r.clock) {
			t.Errorf("un-parked entry = %+v, want shipping with ship_at now", e)
		}
		r.ticks(7, 5*time.Second)
		r.wantStatus("T1.1", ledger.Done)
		// Back through `running` it would have been handed over twice.
		if r.count("tokens") != 1 || r.count("ship_wait") != 1 || len(r.tokenIDs) != 1 || len(r.prompts) != 0 {
			t.Errorf("tokens lines %d, hand-overs %d, token reads %d, prompts %d; want 1, 1, 1, 0",
				r.count("tokens"), r.count("ship_wait"), len(r.tokenIDs), len(r.prompts))
		}
	})

	t.Run("a_park_with_no_item_still_tells_the_owner", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		r.shipFn = func(req ShipRequest) ship.Outcome {
			return ship.Outcome{Kind: ship.OutcomePark, Reason: "the push was refused", Progress: req.Progress}
		}
		r.toShipping()
		r.ticks(1, 5*time.Second)
		items := r.items()
		if len(items) != 1 || !items[0].Blocking {
			t.Fatalf("items = %+v, want one blocking item", items)
		}
		wantContains(t, "the item", items[0].Title+"\n"+items[0].Body, "T1.1", "The push was refused", "Answer this item")
		r.wantStatus("T1.1", ledger.Parked)
	})
}

const stopReasonText = "the deploy did not answer healthy within 15m"

// stopScript is a ship whose deploy never comes up: it merges, stops the line
// (marker first, then the saved state, as ship.Advance does), waits for as
// long as the marker is there, and says done once it is gone.
func stopScript(r *rig) func(ShipRequest) ship.Outcome {
	return func(req ShipRequest) ship.Outcome {
		p := req.Progress
		switch p.State {
		case ship.NotStarted:
			return saveAndWait(r.t, req, ship.Progress{State: ship.Merged, At: *r.clock, PR: 7, MergedSHA: "abc1234"})
		case ship.Merged:
			if err := req.Stop(req.Entry.ID, stopReasonText); err != nil {
				r.t.Errorf("Stop = %v", err)
			}
			p.State, p.At = ship.Stopped, *r.clock
			if err := req.Save(p); err != nil {
				r.t.Errorf("Save = %v", err)
			}
			return ship.Outcome{Kind: ship.OutcomeStop, Reason: stopReasonText, Progress: p,
				Item: &ship.Item{Kind: ship.ItemFailure, Title: "The deploy after T1.1 did not become healthy",
					Body: "T1.1 was merged, but the deploy is not healthy. Run `afk resume` once it is dealt with."}}
		case ship.Stopped:
			if daemonstate.Stopped(r.root) {
				return ship.Outcome{Kind: ship.OutcomeWait, Reason: "the line is stopped", Progress: p}
			}
			return ship.Outcome{Kind: ship.OutcomeDone, Reason: "the stop was cleared", Progress: p}
		}
		r.t.Fatalf("stopScript was handed the state %q", p.State)
		return ship.Outcome{}
	}
}

// TestTickShipStopsTheLine: a deploy that does not come up stops the line.
// The marker is written, the owner is told once and at the highest priority,
// the task stays `shipping`, nothing else starts across twenty ticks and a
// restart, and clearing the marker (what `afk resume` does) is what finishes
// the task and lets the next one launch.
func TestTickShipStopsTheLine(t *testing.T) {
	t.Run("stop_then_resume", func(t *testing.T) {
		// T1.2 does not depend on T1.1, so only the stop holds it back.
		r := newRig(t, task("T1.1"), task("T1.2"))
		r.shipFn = stopScript(r)
		r.toShipping()
		r.ticks(1, 5*time.Second)
		r.advance(5 * time.Second)
		st := r.tick()

		if task, reason, ok := daemonstate.ReadStop(r.root); !ok || task != "T1.1" || reason != stopReasonText {
			t.Errorf("stop marker = (%q, %q, %v), want T1.1 and the reason", task, reason, ok)
		}
		if _, err := os.Stat(filepath.Join(r.root, ".afk", "build", "stopped")); err != nil {
			t.Errorf("no .afk/build/stopped: %v", err)
		}
		if st.Kind != KindLineStopped || st.Task != "T1.1" {
			t.Errorf("state on stop = %+v, want line_stopped for T1.1", st)
		}
		wantContains(t, "the stopped reason", st.Reason, "the line is stopped", stopReasonText, "afk resume", "T1.1")
		items := r.items()
		if len(items) != 1 {
			t.Fatalf("items = %d, want 1 (logs: %v)", len(items), r.logs)
		}
		if it := items[0]; it.Kind != "failure" || it.Task != "T1.1" || it.Session != "t1-1" || it.Blocking {
			t.Errorf("item = %+v, want a failure for T1.1 that does not block (the marker holds the line)", it)
		}
		if len(r.urgent) != 1 || r.urgent[0].ID != items[0].ID || len(r.notified) != 0 {
			t.Errorf("urgent %d, ordinary %d; want the item sent once, at the highest priority", len(r.urgent), len(r.notified))
		}
		if e := r.wantStatus("T1.1", ledger.Shipping); e.Ship != "stopped" || e.MergedSHA != "abc1234" {
			t.Errorf("entry on stop = %+v", e)
		}

		r.ticks(20, 5*time.Second)
		again := r.restart()
		again.shipFn = stopScript(again)
		st = again.ticks(20, 5*time.Second)
		if st.Kind != KindLineStopped || st.Task != "T1.1" {
			t.Errorf("state while stopped = %+v", st)
		}
		if n := len(again.items()); n != 1 {
			t.Errorf("items while stopped = %d, want 1", n)
		}
		if len(r.urgent)+len(again.urgent) != 1 || len(r.notified)+len(again.notified) != 0 {
			t.Errorf("urgent %d, ordinary %d; want 1 and 0", len(r.urgent)+len(again.urgent), len(r.notified)+len(again.notified))
		}
		if len(r.launches)+len(again.launches) != 1 || len(r.cleans)+len(again.cleans) != 0 {
			t.Errorf("launches %d, cleans %d; want only T1.1's launch and no clean-up", len(r.launches)+len(again.launches), len(r.cleans)+len(again.cleans))
		}
		again.wantStatus("T1.1", ledger.Shipping)
		again.wantStatus("T1.2", ledger.Pending)
		if n := again.count("line_stopped"); n != 1 {
			t.Errorf("line_stopped lines = %d, want 1", n)
		}

		if _, err := daemonstate.ClearStop(again.root); err != nil {
			t.Fatal(err)
		}
		again.advance(5 * time.Second)
		again.tick()
		if e := again.wantStatus("T1.1", ledger.Done); e.Ship != "" || e.MergedSHA != "abc1234" {
			t.Errorf("entry after the stop was cleared = %+v", e)
		}
		if !slices.Equal(again.cleans, []string{"t1-1"}) {
			t.Errorf("cleans = %v, want the session once", again.cleans)
		}
		again.advance(5 * time.Second)
		if st := again.tick(); st.Kind != string(ActLaunch) || st.Task != "T1.2" {
			t.Errorf("state after the stop was cleared = %+v, want T1.2 launched", st)
		}
	})

	t.Run("killed_before_the_item", func(t *testing.T) {
		// The ship saved `stopped` and wrote the marker; the daemon died
		// before the item. The ship now only waits.
		r := newRig(t, task("T1.1"), task("T1.2"))
		r.shipFn = stopScript(r)
		r.setEntry(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1", Attempts: 1,
			StartedAt: stamp(*r.clock), Ship: "stopped", ShipAt: stamp(*r.clock), PR: 7, MergedSHA: "abc1234"})
		if err := daemonstate.Stop(r.root, "T1.1", stopReasonText, *r.clock); err != nil {
			t.Fatal(err)
		}
		st := r.ticks(5, 5*time.Second)
		items := r.items()
		if len(items) != 1 || len(r.urgent) != 1 {
			t.Fatalf("items %d, urgent %d; want the owner told once (logs: %v)", len(items), len(r.urgent), r.logs)
		}
		if items[0].Task != "T1.1" || items[0].Kind != "failure" || items[0].Blocking {
			t.Errorf("item = %+v", items[0])
		}
		wantContains(t, "the item", items[0].Title+"\n"+items[0].Body, "The line is stopped after T1.1", stopReasonText, "afk resume")
		if st.Kind != KindLineStopped || len(r.launches) != 0 {
			t.Errorf("state = %+v, launches %d", st, len(r.launches))
		}
		// Answering it is not a second way out, and does not bring it back.
		r.answer(items[0].ID)
		r.ticks(5, 5*time.Second)
		if len(r.items()) != 1 || len(r.urgent) != 1 {
			t.Errorf("items %d, urgent %d after the answer; want 1 and 1", len(r.items()), len(r.urgent))
		}
		r.wantStatus("T1.1", ledger.Shipping)
	})

	t.Run("no_urgent_sender_uses_the_ordinary_one", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		r.noUrgent = true
		r.shipFn = stopScript(r)
		r.toShipping()
		r.ticks(2, 5*time.Second)
		if len(r.notified) != 1 || len(r.urgent) != 0 {
			t.Errorf("ordinary %d, urgent %d; want the stop sent through Notify", len(r.notified), len(r.urgent))
		}
	})
}

const afterMergeStopReason = "the after-merge command ended with exit code 2"

// afterMergeStopScript is a ship whose after-merge command fails: it merges,
// moves to after_merge, then stops the line (marker first, then the saved
// state), and waits while the marker is there. Its item is the one
// internal/ship writes for a failed command: no word about a deploy.
func afterMergeStopScript(r *rig) func(ShipRequest) ship.Outcome {
	return func(req ShipRequest) ship.Outcome {
		p := req.Progress
		switch p.State {
		case ship.NotStarted:
			return saveAndWait(r.t, req, ship.Progress{State: ship.Merged, At: *r.clock, PR: 7, MergedSHA: "abc1234"})
		case ship.Merged:
			p.State, p.At = ship.AfterMerge, *r.clock
			return saveAndWait(r.t, req, p)
		case ship.AfterMerge:
			if err := req.Stop(req.Entry.ID, afterMergeStopReason); err != nil {
				r.t.Errorf("Stop = %v", err)
			}
			p.State, p.At = ship.Stopped, *r.clock
			if err := req.Save(p); err != nil {
				r.t.Errorf("Save = %v", err)
			}
			return ship.Outcome{Kind: ship.OutcomeStop, Reason: afterMergeStopReason, Progress: p,
				Item: &ship.Item{Kind: ship.ItemFailure, Title: "The after-merge command failed after T1.1",
					Body: "T1.1 was merged, but the after-merge command failed. Run `afk resume` once it is put right."}}
		case ship.Stopped:
			if daemonstate.Stopped(r.root) {
				return ship.Outcome{Kind: ship.OutcomeWait, Reason: "the line is stopped", Progress: p}
			}
			return ship.Outcome{Kind: ship.OutcomeDone, Reason: "the stop was cleared", Progress: p}
		}
		r.t.Fatalf("afterMergeStopScript was handed the state %q", p.State)
		return ship.Outcome{}
	}
}

// loggedDecisions reads the decision log with its questions and choices.
func (r *rig) loggedDecisions() []decisionlog.Entry {
	r.t.Helper()
	entries, err := decisionlog.Read(decisionlog.Path(r.root))
	if err != nil {
		r.t.Fatal(err)
	}
	return entries
}

// TestTickShipAfterMergeStop: a failed after-merge command stops the line the
// same way a deploy does, in words that are true of it. Load-bearing rows:
//   - after_merge_stop: one item, not blocking, sent once at the highest
//     priority, whose text and the line_stopped question carry the command's
//     reason and never claim a deploy failed.
//   - killed_before_the_item: the fallback item built from the marker alone
//     (the ship only waits) is neutral too, and only raised once.
//   - the ship log line: saving after_merge writes exactly one `ship` line
//     choosing after_merge, with no code beyond the existing Save.
func TestTickShipAfterMergeStop(t *testing.T) {
	t.Run("after_merge_stop", func(t *testing.T) {
		r := newRig(t, task("T1.1"), task("T1.2"))
		r.shipFn = afterMergeStopScript(r)
		r.toShipping()
		r.ticks(2, 5*time.Second)
		r.advance(5 * time.Second)
		st := r.tick()

		if st.Kind != KindLineStopped || st.Task != "T1.1" {
			t.Errorf("state on stop = %+v, want line_stopped for T1.1", st)
		}
		wantContains(t, "the stopped reason", st.Reason, afterMergeStopReason, "afk resume")
		items := r.items()
		if len(items) != 1 {
			t.Fatalf("items = %d, want 1 (logs: %v)", len(items), r.logs)
		}
		if it := items[0]; it.Kind != "failure" || it.Task != "T1.1" || it.Blocking {
			t.Errorf("item = %+v, want a failure for T1.1 that does not block", it)
		}
		if len(r.urgent) != 1 || r.urgent[0].ID != items[0].ID || len(r.notified) != 0 {
			t.Errorf("urgent %d, ordinary %d; want the item sent once, at the highest priority", len(r.urgent), len(r.notified))
		}
		r.ticks(10, 5*time.Second)
		if len(r.items()) != 1 || len(r.urgent) != 1 {
			t.Errorf("items %d, urgent %d after more ticks; want 1 and 1", len(r.items()), len(r.urgent))
		}
		r.wantStatus("T1.1", ledger.Shipping)

		var stops []decisionlog.Entry
		for _, e := range r.loggedDecisions() {
			if e.Kind == "line_stopped" {
				stops = append(stops, e)
			}
		}
		if len(stops) != 1 {
			t.Fatalf("line_stopped lines = %d, want 1", len(stops))
		}
		wantContains(t, "the line_stopped line", stops[0].Question+"\n"+stops[0].Chosen,
			"The line was stopped after T1.1: "+afterMergeStopReason+". What now?", afterMergeStopReason, "afk resume")
		if strings.Contains(strings.ToLower(stops[0].Question+stops[0].Chosen), "deploy") {
			t.Errorf("line_stopped line %+v speaks of a deploy", stops[0])
		}
	})

	t.Run("killed_before_the_item", func(t *testing.T) {
		r := newRig(t, task("T1.1"), task("T1.2"))
		r.shipFn = afterMergeStopScript(r)
		r.setEntry(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1", Attempts: 1,
			StartedAt: stamp(*r.clock), Ship: "stopped", ShipAt: stamp(*r.clock), PR: 7, MergedSHA: "abc1234"})
		if err := daemonstate.Stop(r.root, "T1.1", afterMergeStopReason, *r.clock); err != nil {
			t.Fatal(err)
		}
		r.ticks(5, 5*time.Second)
		items := r.items()
		if len(items) != 1 || len(r.urgent) != 1 {
			t.Fatalf("items %d, urgent %d; want the owner told once (logs: %v)", len(items), len(r.urgent), r.logs)
		}
		if items[0].Blocking || items[0].Kind != "failure" || items[0].Task != "T1.1" {
			t.Errorf("item = %+v", items[0])
		}
		text := items[0].Title + "\n" + items[0].Body
		wantContains(t, "the item", text, "The line is stopped after T1.1", afterMergeStopReason, "afk resume")
		if strings.Contains(strings.ToLower(text), "deploy") {
			t.Errorf("item claims a deploy: %q", text)
		}
		for _, e := range r.loggedDecisions() {
			if e.Kind == "line_stopped" && strings.Contains(strings.ToLower(e.Question+e.Chosen), "deploy") {
				t.Errorf("line_stopped line %+v speaks of a deploy", e)
			}
		}
	})

	t.Run("no_reason_is_neutral", func(t *testing.T) {
		body := stopLostBody("T1.1", "  ")
		wantContains(t, "the body", body, "the ship gave no reason", "Look at what went wrong", "afk resume")
		if strings.Contains(strings.ToLower(body), "deploy") {
			t.Errorf("body claims a deploy: %q", body)
		}
	})

	t.Run("ship_log_line", func(t *testing.T) {
		r := newRig(t, task("T1.1"))
		r.shipFn = afterMergeStopScript(r)
		r.toShipping()
		r.ticks(2, 5*time.Second)
		var got []decisionlog.Entry
		for _, e := range r.loggedDecisions() {
			if e.Kind == "ship" && e.Chosen == "after_merge" {
				got = append(got, e)
			}
		}
		if len(got) != 1 {
			t.Errorf("ship lines choosing after_merge = %d, want 1 (kinds: %v)", len(got), r.decisions())
		}
	})
}

// TestTickShipWithNoSessionOnDisk: the session was cleaned up and the daemon
// was killed before the ledger said done. The task is `shipping` at
// deploy_healthy with nothing on disk. The ship is still asked, with an empty
// worktree and base, and the task ends `done`.
func TestTickShipWithNoSessionOnDisk(t *testing.T) {
	r := newRig(t, task("T1.1"), task("T1.2", "T1.1"))
	r.shipFn = r.greenShip
	r.setEntry(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1", Attempts: 1,
		StartedAt: stamp(*r.clock), Ship: "deploy_healthy", ShipAt: stamp(*r.clock), PR: 7, MergedSHA: "abc1234"})

	r.advance(5 * time.Second)
	r.tick()
	if len(r.shipReqs) != 1 {
		t.Fatalf("ship calls = %d, want 1 (logs: %v)", len(r.shipReqs), r.logs)
	}
	if req := r.shipReqs[0]; req.Worktree != "" || req.Base != "" || req.Progress.State != ship.DeployHealthy || req.Progress.MergedSHA != "abc1234" {
		t.Errorf("request = worktree %q, base %q, progress %+v", req.Worktree, req.Base, req.Progress)
	}
	if e := r.wantStatus("T1.1", ledger.Done); e.MergedSHA != "abc1234" || e.Ship != "" || e.FinishedAt == "" {
		t.Errorf("done entry = %+v", e)
	}
	if !slices.Equal(r.cleans, []string{"t1-1"}) || len(r.launches) != 0 || len(r.items()) != 0 {
		t.Errorf("cleans %v, launches %d, items %d; want the clean asked once, no session created, nothing said",
			r.cleans, len(r.launches), len(r.items()))
	}
	r.advance(5 * time.Second)
	if st := r.tick(); st.Kind != string(ActLaunch) || st.Task != "T1.2" {
		t.Errorf("next state = %+v, want T1.2 launched", st)
	}
}

// TestTickShipCleanFailureParks: a session that cannot be cleaned up keeps
// the task from saying done. The owner gets one item saying what to remove
// and that answering retries; the answer runs the clean-up again.
func TestTickShipCleanFailureParks(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.shipFn = r.greenShip
	r.cleanErr = errors.New("the worktree has files git will not remove")
	r.toShipping()
	r.ticks(7, 5*time.Second)

	e := r.wantStatus("T1.1", ledger.Parked)
	if e.Ship != "deploy_healthy" || e.MergedSHA != "abc1234" || e.FinishedAt != "" {
		t.Errorf("parked entry = %+v, want the ship kept at deploy_healthy", e)
	}
	items := r.items()
	if len(items) != 1 || !items[0].Blocking || items[0].Kind != "failure" {
		t.Fatalf("items = %+v", items)
	}
	wantContains(t, "the item", items[0].Title+"\n"+items[0].Body,
		"T1.1", "t1-1", "the worktree has files git will not remove", "afk clean t1-1", "answer this item")
	r.ticks(5, 5*time.Second)
	if len(r.cleans) != 1 || len(r.items()) != 1 {
		t.Errorf("cleans %d, items %d while parked; want 1 and 1", len(r.cleans), len(r.items()))
	}

	r.cleanErr = nil
	r.answer(items[0].ID)
	r.ticks(2, 5*time.Second)
	if e := r.wantStatus("T1.1", ledger.Done); e.Ship != "" || e.MergedSHA != "abc1234" {
		t.Errorf("done entry = %+v", e)
	}
	if len(r.cleans) != 2 || r.count("shipped") != 1 {
		t.Errorf("cleans %d, shipped lines %d; want 2 and 1", len(r.cleans), r.count("shipped"))
	}
}

// TestTickRelaunchClearsTheShip: a task parked in the middle of its ship,
// set back to pending by hand and launched again starts its second attempt
// with no ship at all. Otherwise it would reach `shipping` holding the first
// attempt's pull request and merge commit.
func TestTickRelaunchClearsTheShip(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.shipFn = func(req ShipRequest) ship.Outcome {
		switch {
		case req.Entry.Attempts > 1:
			return ship.Outcome{Kind: ship.OutcomeWait, Reason: "looking", Progress: req.Progress}
		case req.Progress.State == ship.NotStarted:
			return saveAndWait(t, req, ship.Progress{State: ship.Merged, At: *r.clock, PR: 7, Reruns: 1, FixCycles: 1, MergedSHA: "0ld5ha"})
		}
		return ship.Outcome{Kind: ship.OutcomePark, Reason: "the base could not be updated", Progress: req.Progress,
			Item: &ship.Item{Kind: ship.ItemFailure, Title: "Your local main could not be updated after T1.1 merged", Body: "Update it by hand."}}
	}
	r.toShipping()
	r.ticks(2, 5*time.Second)
	if e := r.wantStatus("T1.1", ledger.Parked); e.PR != 7 || e.MergedSHA != "0ld5ha" || e.Ship != "merged" {
		t.Fatalf("parked entry = %+v", e)
	}

	// The owner gives the attempt up: `afk ledger set T1.1 pending`.
	err := ledger.Update(r.root, func(l *ledger.Ledger) error {
		_, err := l.Set("T1.1", ledger.Pending, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.advance(5 * time.Second)
	r.tick()
	e := r.wantStatus("T1.1", ledger.Running)
	if e.Session != "t1-1-2" || e.Attempts != 2 {
		t.Fatalf("relaunched entry = %+v, want attempt 2 in t1-1-2", e)
	}
	if e.Ship != "" || e.ShipAt != "" || e.CIReruns != 0 || e.CIFixCycles != 0 || e.PR != 0 || e.MergedSHA != "" || len(e.ParkedOn) != 0 {
		t.Errorf("relaunched entry still holds the first attempt's ship: %+v", e)
	}

	r.advance(time.Minute)
	r.setPhase("t1-1-2", state.PhaseDone)
	r.tick()
	r.wantStatus("T1.1", ledger.Shipping)
	r.advance(5 * time.Second)
	r.tick()
	last := r.shipReqs[len(r.shipReqs)-1]
	if last.Entry.Session != "t1-1-2" || last.Progress != (ship.Progress{}) {
		t.Errorf("second attempt's request = session %q, progress %+v; want t1-1-2 and an empty progress", last.Entry.Session, last.Progress)
	}
}

// TestTickUnparkTypesTheResumeCommand: when a parked task goes back to
// running, the loop types one line into its session, the session's own
// resume command and nothing else on the line.
//
// Load-bearing rows: the first pins the exact line (anything else leaves
// awaiting_input set, and the session stalls again) and that it is typed
// once however many ticks follow. "a send that fails" is the promise that a
// dead host never keeps a task parked. The last two are where typing would
// do harm: there is no session to type into, or the session has ended.
func TestTickUnparkTypesTheResumeCommand(t *testing.T) {
	cases := []struct {
		name      string
		noSender  bool
		promptErr error
		// before runs after the answer and before the un-parking tick.
		before      func(r *rig)
		wantPrompts int
		wantLog     string
	}{
		{name: "the resume command is typed once, alone on the line", wantPrompts: 1},
		{
			name:        "a send that fails is logged and the task is still un-parked",
			promptErr:   errors.New("could not reach session t1-1: no host is running"),
			wantPrompts: 1,
			wantLog:     "could not reach session t1-1: no host is running",
		},
		{name: "with no sender nothing is typed", noSender: true},
		{
			name: "a session that is not on disk gets no line",
			before: func(r *rig) {
				if err := os.RemoveAll(filepath.Join(r.root, "wt", "t1-1")); err != nil {
					r.t.Fatal(err)
				}
			},
		},
		{
			name:   "a session that ended as failed gets no line",
			before: func(r *rig) { r.setPhase("t1-1", state.PhaseFailed) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, task("T1.1"))
			r.promptOn, r.promptErr = !tc.noSender, tc.promptErr
			r.tick()
			r.advance(time.Minute)
			q, err := r.store().Add(inbox.NewItem{Task: "T1.1", Session: "t1-1", Kind: "question", Blocking: true, Title: "Which database?"})
			if err != nil {
				t.Fatal(err)
			}
			r.setAwaiting("t1-1", true)
			r.tick()
			r.wantStatus("T1.1", ledger.Parked)
			r.ticks(3, 5*time.Second)
			if len(r.prompts) != 0 {
				t.Fatalf("prompts while parked = %v, want none", r.prompts)
			}

			r.answer(q.ID)
			if tc.before != nil {
				tc.before(r)
			}
			r.advance(5 * time.Second)
			st := r.tick()
			if st.Kind != string(ActUnpark) {
				t.Errorf("state on answer = %+v", st)
			}
			if e := r.wantStatus("T1.1", ledger.Running); len(e.ParkedOn) != 0 || e.Ship != "" || e.ShipAt != "" {
				t.Errorf("un-parked entry = %+v", e)
			}
			if len(r.prompts) != tc.wantPrompts {
				t.Fatalf("prompts = %v, want %d", r.prompts, tc.wantPrompts)
			}
			if tc.wantPrompts == 1 {
				want := typed{worktree: filepath.Join(r.root, "wt", "t1-1"), session: "t1-1", text: "/afk:go --session t1-1"}
				if r.prompts[0] != want {
					t.Errorf("prompt = %+v, want %+v", r.prompts[0], want)
				}
			}
			if tc.wantLog != "" {
				wantContains(t, "the log", strings.Join(r.logs, "\n"), tc.wantLog, "T1.1")
			}
			r.ticks(3, 5*time.Second)
			if len(r.prompts) != tc.wantPrompts {
				t.Errorf("prompts after three more ticks = %v, want still %d", r.prompts, tc.wantPrompts)
			}
		})
	}
}

// The recovery tests (PLAN §5.6) run the loop with a fake Restart and a fake
// Relaunch. A session is wedged or its host stopped by a file under the
// session's folder, the way the real verdict and the real host are read from
// host.json, so every one of them also holds across rig.restart.

// running takes T1.1 from nothing to a session that is working: launched, and
// a minute later in phase executing with a fresh status.
func (r *rig) running() {
	r.t.Helper()
	r.tick()
	r.advance(time.Minute)
	r.setPhase("t1-1", state.PhaseExecuting)
	r.wantStatus("T1.1", ledger.Running)
}

// wantRecovery checks the three recovery fields of T1.1's entry.
func (r *rig) wantRecovery(restarts int, pending bool) ledger.Entry {
	r.t.Helper()
	e := r.entry("T1.1")
	if e.Restarts != restarts || e.ContinuePending != pending {
		r.t.Fatalf("restarts %d, continue_pending %v; want %d and %v (entry %+v, logs %v)",
			e.Restarts, e.ContinuePending, restarts, pending, e, r.logs)
	}
	return e
}

// TestTickWedgedSessionIsRestartedAndToldToContinue is the whole of a
// recovery that works: one restart request, then the session's resume command
// typed once.
//
// Load-bearing: the restart time is the one read after the restart finished
// and not the tick's own, because the continue prompt is timed from it and a
// line typed before the new agent is up is lost; the prompt goes out at
// ContinueAfter and not a second sooner; and the ticks after it type nothing,
// which is what fails if continue_pending is not cleared.
func TestTickWedgedSessionIsRestartedAndToldToContinue(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.restartOn, r.promptOn = true, true
	r.running()
	wt := filepath.Join(r.root, "wt", "t1-1")

	r.wedge("t1-1")
	r.advance(DefaultWedgeConfirm)
	asked := stamp(*r.clock)
	st := r.tick()
	if st.Kind != string(ActRestart) || st.Task != "T1.1" || st.Session != "t1-1" {
		t.Errorf("state = %+v, want a restart of T1.1", st)
	}
	if want := []restartCall{{worktree: wt, session: "t1-1", counted: 1}}; !slices.Equal(r.restarts, want) {
		t.Fatalf("restarts = %+v, want %+v", r.restarts, want)
	}
	e := r.wantRecovery(1, true)
	if e.RestartAt == asked || e.RestartAt != stamp(*r.clock) {
		t.Errorf("restart_at = %s; want %s, when the restart finished, not %s, when it was asked for", e.RestartAt, stamp(*r.clock), asked)
	}

	r.advance(DefaultContinueAfter - time.Second)
	r.tick()
	if len(r.prompts) != 0 {
		t.Fatalf("prompts a second before ContinueAfter = %+v, want none", r.prompts)
	}
	r.wantRecovery(1, true)
	r.advance(time.Second)
	st = r.tick()
	if st.Kind != string(ActContinue) {
		t.Errorf("state = %+v, want a continue", st)
	}
	if want := []typed{{worktree: wt, session: "t1-1", text: "/afk:go --session t1-1"}}; !slices.Equal(r.prompts, want) {
		t.Fatalf("prompts = %+v, want %+v", r.prompts, want)
	}
	r.wantRecovery(1, false)

	r.ticks(20, 5*time.Second)
	if len(r.prompts) != 1 || len(r.restarts) != 1 {
		t.Errorf("after twenty more ticks: prompts %d, restarts %d; want 1 and 1", len(r.prompts), len(r.restarts))
	}
	r.wantStatus("T1.1", ledger.Running)
	if n := len(r.items()); n != 0 {
		t.Errorf("items = %d, want 0: a recovery that works tells the owner nothing", n)
	}
	r.wantDecisions("launch", "restart", "continue")
}

// TestTickWedgedWaitsToBeSure: a verdict younger than WedgeConfirm asks for
// nothing and changes nothing; the same verdict, once it has stood that
// long, is acted on. The first half is the load-bearing one: the host clears
// its verdict on any output, and a restart on a young one stops a working
// agent. The paused row is here because a pause holds back new work only,
// and a wedged session is work already paid for.
func TestTickWedgedWaitsToBeSure(t *testing.T) {
	for _, paused := range []bool{false, true} {
		name := "not paused"
		if paused {
			name = "paused"
		}
		t.Run(name, func(t *testing.T) {
			r := newRig(t, task("T1.1"))
			r.restartOn = true
			r.running()
			if paused {
				if err := daemonstate.Pause(r.root, *r.clock); err != nil {
					t.Fatal(err)
				}
			}
			r.wedge("t1-1")
			before := r.ledgerText()

			st := r.tick()
			r.advance(DefaultWedgeConfirm - time.Second)
			st2 := r.tick()
			for _, s := range []daemonstate.State{st, st2} {
				if s.Kind != string(ActWait) || !strings.Contains(s.Reason, "waiting to be sure") {
					t.Errorf("state before the verdict has stood = %+v", s)
				}
			}
			if len(r.restarts) != 0 || r.ledgerText() != before {
				t.Fatalf("restarts %d before WedgeConfirm, or the ledger changed:\n%s", len(r.restarts), r.ledgerText())
			}

			r.advance(time.Second)
			r.tick()
			if len(r.restarts) != 1 {
				t.Fatalf("restarts at WedgeConfirm = %d, want 1 (logs: %v)", len(r.restarts), r.logs)
			}
			r.wantRecovery(1, true)
		})
	}
}

// TestTickRelaunchesAGoneHost: a host that is gone past the grace is started
// again once, resuming the conversation the session recorded.
//
// Load-bearing rows: the first pins that the id handed over is status.json's
// session_id and that the continue prompt follows. The other two are where a
// prompt would do harm: with no id the new host runs the ordinary launch
// command, which carries its own first prompt. "the status cannot be read" is
// the one that fails if the id of a stale view is ever trusted.
func TestTickRelaunchesAGoneHost(t *testing.T) {
	cases := []struct {
		name string
		// status rewrites the session's status.json before its host stops.
		status      func(st *state.Status)
		wantResume  string
		wantPrompts int
	}{
		{name: "the session recorded its conversation", status: func(*state.Status) {}, wantResume: "sid-t1-1", wantPrompts: 1},
		{name: "no conversation yet in phase planning", status: func(st *state.Status) {
			st.Phase, st.SessionID = state.PhasePlanning, ""
		}},
		{name: "the status cannot be read", status: func(st *state.Status) { st.SessionID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, task("T1.1"))
			r.relaunchOn, r.promptOn = true, true
			r.running()
			r.writeStatus("t1-1", tc.status)
			r.stopHost("t1-1")

			// Inside the grace nothing is started: a host that is only slow to
			// appear must not get a second one beside it.
			r.tick()
			if len(r.relaunches) != 0 {
				t.Fatalf("relaunches inside the grace = %+v, want none", r.relaunches)
			}
			r.advance(hostGrace + time.Second)
			st := r.tick()
			if st.Kind != string(ActRelaunch) {
				t.Errorf("state = %+v, want a relaunch", st)
			}
			want := []relaunchCall{{req: RelaunchRequest{
				Worktree: filepath.Join(r.root, "wt", "t1-1"), Session: "t1-1", ResumeID: tc.wantResume}, counted: 1}}
			if !slices.Equal(r.relaunches, want) {
				t.Fatalf("relaunches = %+v, want %+v", r.relaunches, want)
			}
			if e := r.wantRecovery(1, tc.wantResume != ""); e.RestartAt != stamp(*r.clock) {
				t.Errorf("restart_at = %s, want %s, when the new host was up", e.RestartAt, stamp(*r.clock))
			}

			r.ticks(12, 5*time.Second)
			if len(r.prompts) != tc.wantPrompts {
				t.Fatalf("prompts a minute later = %+v, want %d", r.prompts, tc.wantPrompts)
			}
			if tc.wantPrompts == 1 && r.prompts[0].text != "/afk:go --session t1-1" {
				t.Errorf("prompt = %+v, want the resume command", r.prompts[0])
			}
			r.wantRecovery(1, false)
			r.wantStatus("T1.1", ledger.Running)
			if len(r.relaunches) != 1 || len(r.items()) != 0 {
				t.Errorf("relaunches %d, items %d; want 1 and 0", len(r.relaunches), len(r.items()))
			}
			if r.count("relaunch") != 1 || r.count("continue") != tc.wantPrompts {
				t.Errorf("decision log = %v", r.decisions())
			}
		})
	}
}

// TestTickRecoveryGivesUpAtTheCap: a session that wedges again after every
// restart is restarted at once, then after the backoff, then after twice the
// backoff, and at the cap the task parks on one item.
//
// Load-bearing: the tick one second short of each backoff asks for nothing
// (the wedge confirm is cut to a second here so that the backoff is the only
// thing that can be holding it back); twenty ticks after the park add no
// item and no restart; and the answer un-parks with a count of zero, shown by
// the fourth restart going out at once. With the old count left in place the
// tick after the answer would give up again, for ever.
func TestTickRecoveryGivesUpAtTheCap(t *testing.T) {
	r := newRig(t, task("T1.1"))
	r.restartOn = true
	r.wedgeConfirm = time.Second
	r.running()

	// Each restart "works", and the new agent hangs like the one before it.
	rewedge := func(want int) {
		t.Helper()
		if len(r.restarts) != want {
			t.Fatalf("restarts = %d, want %d (logs: %v)", len(r.restarts), want, r.logs)
		}
		r.wantRecovery(want, true)
		r.wedge("t1-1")
		r.advance(r.wedgeConfirm)
	}
	r.wedge("t1-1")
	r.advance(r.wedgeConfirm)
	r.tick()
	rewedge(1)

	for i, wait := range []time.Duration{restartBackoff, 2 * restartBackoff} {
		// The clock already stands wedgeConfirm past the last restart.
		r.advance(wait - r.wedgeConfirm - time.Second)
		st := r.tick()
		if len(r.restarts) != i+1 || st.Kind != string(ActWait) || !strings.Contains(st.Reason, "waiting before trying to recover it again") {
			t.Fatalf("a second short of the backoff %v: restarts %d, state %+v", wait, len(r.restarts), st)
		}
		r.advance(time.Second)
		r.tick()
		rewedge(i + 2)
	}

	st := r.tick()
	items := r.items()
	if len(items) != 1 {
		t.Fatalf("items at the cap = %d, want 1 (state %+v, logs %v)", len(items), st, r.logs)
	}
	it := items[0]
	if it.Title != RecoveryTitlePrefix+"T1.1 (t1-1)" || it.Kind != "failure" || !it.Blocking || it.Task != "T1.1" || it.Session != "t1-1" {
		t.Errorf("give-up item = %+v", it)
	}
	wantContains(t, "the give-up body", it.Body,
		"restarted the session for T1.1 3 times", "afk dash", "afk attach t1-1", "/afk:go --session t1-1",
		"afk inbox answer", "fresh set of restarts", "afk clean t1-1", "afk ledger set T1.1 pending")
	if e := r.wantStatus("T1.1", ledger.Parked); !slices.Equal(e.ParkedOn, []string{it.ID}) {
		t.Errorf("parked on %v, want [%s]", e.ParkedOn, it.ID)
	}
	if st.Kind != string(ActGiveUp) || !strings.Contains(st.Reason, it.ID) {
		t.Errorf("state = %+v", st)
	}
	if len(r.notified) != 1 || r.notified[0].ID != it.ID {
		t.Errorf("notified = %+v, want the one item", r.notified)
	}

	r.ticks(20, time.Minute)
	if len(r.items()) != 1 || len(r.restarts) != 3 {
		t.Fatalf("twenty ticks after the cap: items %d, restarts %d; want 1 and 3", len(r.items()), len(r.restarts))
	}
	r.wantStatus("T1.1", ledger.Parked)

	r.answer(it.ID)
	r.advance(5 * time.Second)
	if st := r.tick(); st.Kind != string(ActUnpark) {
		t.Errorf("state on answer = %+v", st)
	}
	r.wantStatus("T1.1", ledger.Running)
	if e := r.wantRecovery(0, false); e.RestartAt != "" {
		t.Errorf("un-parked entry still holds restart_at %s", e.RestartAt)
	}
	// Still wedged, and the fresh set starts with no wait.
	r.advance(5 * time.Second)
	r.tick()
	if len(r.restarts) != 4 || r.restarts[3].counted != 1 {
		t.Errorf("restarts after the answer = %+v, want a fourth that counted 1", r.restarts)
	}
	if n := len(r.items()); n != 1 {
		t.Errorf("items = %d, want still 1", n)
	}
	r.wantDecisions("launch", "restart", "restart", "restart", "give_up", "unpark", "restart")
}

// TestTickRecoveryOffParksAtOnce: with max_restarts = 0 nothing is ever
// restarted or relaunched, and the first trouble parks the task. The body is
// the load-bearing part: "restarted it 0 times" would read as afk failing and
// not as the setting it is.
func TestTickRecoveryOffParksAtOnce(t *testing.T) {
	cases := []struct {
		name    string
		trouble func(r *rig)
	}{
		{name: "a wedge", trouble: func(r *rig) {
			r.wedge("t1-1")
			r.advance(DefaultWedgeConfirm)
		}},
		{name: "a gone host", trouble: func(r *rig) {
			r.stopHost("t1-1")
			r.advance(hostGrace + time.Second)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, task("T1.1"))
			r.restartOn, r.relaunchOn, r.promptOn, r.noRestarts = true, true, true, true
			r.running()
			tc.trouble(r)
			st := r.tick()
			if st.Kind != string(ActGiveUp) {
				t.Errorf("state = %+v, want a give-up", st)
			}
			items := r.items()
			if len(items) != 1 || !strings.HasPrefix(items[0].Title, RecoveryTitlePrefix) || !items[0].Blocking {
				t.Fatalf("items = %+v, want the one give-up item", items)
			}
			wantContains(t, "the body", items[0].Body, "afk is set not to restart sessions", "max_restarts = 0", "afk dash")
			if strings.Contains(items[0].Body, "0 times") || strings.Contains(items[0].Body, "restarted the session") {
				t.Errorf("the body speaks of restarts that were never made:\n%s", items[0].Body)
			}
			r.wantStatus("T1.1", ledger.Parked)
			r.wantRecovery(0, false)
			r.ticks(5, time.Minute)
			if len(r.restarts)+len(r.relaunches)+len(r.prompts) != 0 || len(r.items()) != 1 {
				t.Errorf("restarts %d, relaunches %d, prompts %d, items %d; want 0, 0, 0 and 1",
					len(r.restarts), len(r.relaunches), len(r.prompts), len(r.items()))
			}
			r.wantDecisions("launch", "give_up")
		})
	}
}

// TestTickRecoveryRestartOutcomes: what the loop keeps of each of the host's
// three answers to a restart request.
//
// Load-bearing rows: "refused" drops the pending prompt (nothing was
// restarted, so there is nobody to tell) and keeps the count, which is what
// lets a session that can never be restarted reach the cap. "not known"
// keeps the prompt pending, because the agent may have been restarted and
// would otherwise sit at an empty prompt for good.
func TestTickRecoveryRestartOutcomes(t *testing.T) {
	cases := []struct {
		name        string
		refused     string
		err         error
		wantPending bool
		// wantLater: restart_at was saved a second time, after the restart.
		wantLater  bool
		wantChosen string
		wantLog    string
	}{
		{name: "restarted", wantPending: true, wantLater: true, wantChosen: "restarted its agent"},
		{name: "refused", refused: "no conversation to resume", wantChosen: "the host refused: no conversation to resume",
			wantLog: "no conversation to resume"},
		{name: "not known", err: errors.New("the session was taken by another client"), wantPending: true,
			wantChosen: "not known whether it did: the session was taken by another client",
			wantLog:    "the session was taken by another client"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, task("T1.1"))
			r.restartOn, r.promptOn = true, true
			r.restartRefused, r.restartErr = tc.refused, tc.err
			r.running()
			r.wedge("t1-1")
			r.advance(DefaultWedgeConfirm)
			asked := stamp(*r.clock)
			st := r.tick()

			if st.Kind != string(ActRestart) || len(r.restarts) != 1 {
				t.Fatalf("state %+v, restarts %+v; want one restart", st, r.restarts)
			}
			e := r.wantRecovery(1, tc.wantPending)
			if later := e.RestartAt != asked; later != tc.wantLater {
				t.Errorf("restart_at = %s, asked at %s; saved again afterwards: %v, want %v", e.RestartAt, asked, later, tc.wantLater)
			}
			var lines []decisionlog.Entry
			for _, d := range r.loggedDecisions() {
				if d.Kind == "restart" {
					lines = append(lines, d)
				}
			}
			if len(lines) != 1 || !strings.Contains(lines[0].Chosen, tc.wantChosen) || lines[0].Rule != "PLAN §5.6" {
				t.Errorf("restart lines in the decision log = %+v, want one choosing %q", lines, tc.wantChosen)
			}
			if tc.wantLog != "" {
				wantContains(t, "the log", strings.Join(r.logs, "\n"), tc.wantLog, "T1.1", "t1-1")
			}

			// Inside the backoff, and short of ContinueAfter, nothing more.
			r.ticks(5, 5*time.Second)
			if len(r.restarts) != 1 || len(r.prompts) != 0 || len(r.items()) != 0 {
				t.Errorf("restarts %d, prompts %d, items %d; want 1, 0 and 0", len(r.restarts), len(r.prompts), len(r.items()))
			}
			r.wantStatus("T1.1", ledger.Running)
			r.wantRecovery(1, tc.wantPending)
		})
	}
}

// TestTickRecoverySavesBeforeTheCall is the crash-loop guard. The fake reads
// the ledger file while it is being called and then fails, standing in for a
// daemon that dies in the middle of a recovery.
//
// Load-bearing: the count the fake saw is 1, so it was on disk before the
// request went out. Moved after the call, a daemon that dies on every
// recovery would find a count of zero every time and recover the same session
// without limit. The second half is the same promise seen from the next
// daemon: it starts from the saved count and the saved time, and so waits out
// the backoff (five minutes here, to stand clear of the host's grace) before
// asking again.
func TestTickRecoverySavesBeforeTheCall(t *testing.T) {
	const backoff = 5 * time.Minute
	died := errors.New("the daemon was killed here")
	cases := []struct {
		name    string
		trouble func(r *rig)
		on      func(r *rig)
		// counted is what each call saw on disk.
		counted func(r *rig) []int
	}{
		{
			name: "restart",
			trouble: func(r *rig) {
				r.wedge("t1-1")
				r.advance(DefaultWedgeConfirm)
			},
			on: func(r *rig) { r.restartOn = true },
			counted: func(r *rig) []int {
				var out []int
				for _, c := range r.restarts {
					out = append(out, c.counted)
				}
				return out
			},
		},
		{
			name: "relaunch",
			trouble: func(r *rig) {
				r.stopHost("t1-1")
				r.advance(hostGrace + time.Second)
			},
			on: func(r *rig) { r.relaunchOn = true },
			counted: func(r *rig) []int {
				var out []int
				for _, c := range r.relaunches {
					out = append(out, c.counted)
				}
				return out
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := newRig(t, task("T1.1"))
			first.backoff = backoff
			tc.on(first)
			first.restartErr, first.relaunchErr = died, died
			first.running()
			tc.trouble(first)
			first.tick()
			if got := tc.counted(first); !slices.Equal(got, []int{1}) {
				t.Fatalf("the count on disk during the call = %v, want [1]: the ledger is saved before the request", got)
			}

			second := first.restart()
			second.backoff = backoff
			tc.on(second)
			second.advance(backoff - time.Second)
			second.tick()
			if got := tc.counted(second); len(got) != 0 {
				t.Fatalf("the next daemon asked again inside the backoff (saw %v)", got)
			}
			second.advance(time.Second)
			second.tick()
			if got := tc.counted(second); !slices.Equal(got, []int{2}) {
				t.Fatalf("the count on disk at the backoff = %v, want [2] (logs: %v)", got, second.logs)
			}
			second.wantStatus("T1.1", ledger.Running)
		})
	}
}

// TestTickRecoveryFieldsAreCleared: restarts, restart_at and continue_pending
// go back to zero wherever the entry gets a new session or leaves `running`.
// Each row starts from an entry that holds all three.
//
// Load-bearing rows: "created again" is the one where a leftover
// continue_pending would do harm at once, typed into a new session on top of
// the prompt it was just started with; its prompt count is the check. "done
// with its host gone" is the order of Decide's rules seen from the executor:
// a finished session is shipped and its host is never started again.
func TestTickRecoveryFieldsAreCleared(t *testing.T) {
	cases := []struct {
		name string
		// arrange changes the entry and the session before the one tick.
		arrange      func(r *rig, e *ledger.Entry)
		wantStatus   ledger.Status
		wantSession  string
		wantLaunches int
	}{
		{name: "a new attempt", arrange: func(r *rig, e *ledger.Entry) { e.Status = ledger.Pending },
			wantStatus: ledger.Running, wantSession: "t1-1-2", wantLaunches: 2},
		{name: "created again", arrange: func(r *rig, e *ledger.Entry) {
			if err := os.RemoveAll(filepath.Join(r.root, "wt", "t1-1")); err != nil {
				r.t.Fatal(err)
			}
		}, wantStatus: ledger.Running, wantSession: "t1-1", wantLaunches: 2},
		{name: "done with its host gone", arrange: func(r *rig, e *ledger.Entry) {
			r.setPhase("t1-1", state.PhaseDone)
			r.stopHost("t1-1")
		}, wantStatus: ledger.Shipping, wantSession: "t1-1", wantLaunches: 1},
		{name: "failed with its host gone", arrange: func(r *rig, e *ledger.Entry) {
			r.setPhase("t1-1", state.PhaseFailed)
			r.stopHost("t1-1")
		}, wantStatus: ledger.Failed, wantSession: "t1-1", wantLaunches: 1},
		{name: "un-parked back to shipping", arrange: func(r *rig, e *ledger.Entry) {
			it, err := r.store().Add(inbox.NewItem{Task: "T1.1", Kind: "failure", Blocking: true, Title: "CI is red"})
			if err != nil {
				r.t.Fatal(err)
			}
			r.answer(it.ID)
			e.Status, e.Ship, e.ParkedOn = ledger.Parked, string(ship.Pushed), []string{it.ID}
		}, wantStatus: ledger.Shipping, wantSession: "t1-1", wantLaunches: 1},
		{name: "shipped", arrange: func(r *rig, e *ledger.Entry) {
			r.shipFn = r.greenShip
			e.Status, e.Ship = ledger.Shipping, string(ship.DeployHealthy)
		}, wantStatus: ledger.Done, wantSession: "t1-1", wantLaunches: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, task("T1.1"))
			r.restartOn, r.relaunchOn, r.promptOn = true, true, true
			r.running()
			e := r.entry("T1.1")
			e.Restarts, e.RestartAt, e.ContinuePending = 2, stamp(r.clock.Add(-time.Hour)), true
			tc.arrange(r, &e)
			r.setEntry(e)

			r.advance(hostGrace + time.Minute)
			r.tick()
			got := r.wantStatus("T1.1", tc.wantStatus)
			if got.Restarts != 0 || got.RestartAt != "" || got.ContinuePending {
				t.Errorf("entry still holds recovery fields: %+v", got)
			}
			if got.Session != tc.wantSession || len(r.launches) != tc.wantLaunches {
				t.Errorf("session %s, launches %d; want %s and %d", got.Session, len(r.launches), tc.wantSession, tc.wantLaunches)
			}
			r.ticks(10, 5*time.Second)
			if len(r.prompts)+len(r.restarts)+len(r.relaunches) != 0 {
				t.Errorf("prompts %+v, restarts %+v, relaunches %+v; want nothing typed and nothing recovered",
					r.prompts, r.restarts, r.relaunches)
			}
		})
	}
}

// TestTickRecoveryWhileAPersonAttaches: a person attaching to a supervised
// session changes no file the loop reads, so the test changes none either.
// What an attach can do is take the session's one connection for a moment, so
// that a restart request or a typed line comes back as "not known" or fails.
// Here each does so once and works on the next try.
//
// Load-bearing: the task is never parked and no item is raised (a taken
// connection is not a broken session), and the recovery still ends with the
// resume command delivered and continue_pending cleared. The decision log
// holds one `continue`, for the send that worked: a failed send is not a
// thing that happened.
func TestTickRecoveryWhileAPersonAttaches(t *testing.T) {
	taken := errors.New("the session was taken by another client")
	r := newRig(t, task("T1.1"))
	r.restartOn, r.promptOn = true, true
	r.running()
	r.wedge("t1-1")
	r.advance(DefaultWedgeConfirm)

	r.restartErr = taken
	r.tick()
	r.wantRecovery(1, true)
	r.wantStatus("T1.1", ledger.Running)

	// Still wedged, so the first request did nothing. The next one is asked
	// for at the backoff, and nothing is typed into a wedged session meanwhile.
	r.restartErr = nil
	r.ticks(11, 5*time.Second)
	if len(r.restarts) != 1 || len(r.prompts) != 0 {
		t.Fatalf("inside the backoff: restarts %d, prompts %d; want 1 and 0", len(r.restarts), len(r.prompts))
	}
	r.advance(5 * time.Second)
	r.tick()
	if len(r.restarts) != 2 {
		t.Fatalf("restarts at the backoff = %d, want 2 (logs: %v)", len(r.restarts), r.logs)
	}
	r.wantRecovery(2, true)

	r.promptErr = taken
	r.advance(DefaultContinueAfter)
	st := r.tick()
	if len(r.prompts) != 1 || st.Kind != string(ActWait) || r.count("continue") != 0 {
		t.Fatalf("a send that failed: prompts %d, state %+v, continue lines %d", len(r.prompts), st, r.count("continue"))
	}
	r.wantRecovery(2, true)

	r.promptErr = nil
	r.advance(5 * time.Second)
	r.tick()
	r.wantRecovery(2, false)
	r.ticks(10, 5*time.Second)
	if len(r.prompts) != 2 || r.count("continue") != 1 || len(r.restarts) != 2 {
		t.Errorf("prompts %d, continue lines %d, restarts %d; want 2, 1 and 2", len(r.prompts), r.count("continue"), len(r.restarts))
	}
	r.wantStatus("T1.1", ledger.Running)
	if n := len(r.items()); n != 0 {
		t.Errorf("items = %d, want 0", n)
	}
	r.wantDecisions("launch", "restart", "restart", "continue")
}

// TestViewOfWedgedSince pins how the host's verdict reaches Decide. The
// load-bearing row is the unreadable one: it must read as "not wedged",
// because a verdict of unknown age cannot be shown to have stood for
// WedgeConfirm and must not restart an agent.
func TestViewOfWedgedSince(t *testing.T) {
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		row    discover.SessionState
		wedged time.Time
	}{
		{name: "no verdict", row: discover.SessionState{Name: "t1-1", HostRunning: true}},
		{name: "a verdict", row: discover.SessionState{Name: "t1-1", HostRunning: true, WedgedSince: stamp(at)}, wedged: at},
		{name: "a verdict with an offset is the same instant",
			row: discover.SessionState{Name: "t1-1", HostRunning: true, WedgedSince: "2026-10-11T11:00:00+02:00"}, wedged: at},
		{name: "an unreadable verdict is no verdict", row: discover.SessionState{Name: "t1-1", HostRunning: true, WedgedSince: "a while ago"}},
		{name: "a stale status still carries the verdict",
			row: discover.SessionState{Name: "t1-1", StatusStale: true, Kind: session.KindStaleStatus, HostRunning: true, WedgedSince: stamp(at)}, wedged: at},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := viewOf([]discover.SessionState{tc.row}, "t1-1")
			if v == nil {
				t.Fatal("no view")
			}
			if !v.WedgedSince.Equal(tc.wedged) || v.WedgedSince.IsZero() != tc.wedged.IsZero() {
				t.Errorf("WedgedSince = %v, want %v", v.WedgedSince, tc.wedged)
			}
		})
	}
}
