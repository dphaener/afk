package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/nexttask"
	"github.com/dphaener/afk/internal/session"
)

// This file is the impure half of the loop. Tick reads one Observation from
// disk, asks Decide what to do, and carries out the one Action it gets back.
//
// Nothing here outlives a tick. There is no Daemon value, no map and no
// counter: every tick starts from the files under the project's root, so a
// process started a second after another was killed reaches the same
// decision the dead one would have. Anything that looks like it wants a
// remembered flag ("was this already reported?", "did preflight fail last
// time?") is answered from an inbox item or the state file instead.
//
// Everything that reaches outside the project's own plain files is a field
// of Deps, so the tests drive the whole loop with a fake agent that only
// writes status.json. The inbox, the ledger, the decision log and the state
// files are used directly: they are plain files under Root with one writer
// package each, and faking them would only test the fake.

// Project is one project the loop runs. Root is its main checkout, never a
// session's worktree: the ledger, the inbox and the logs are shared by every
// session and live in the main checkout only.
type Project struct {
	Name, Root string
}

// Settings are the loop's two limits, from machine config.
type Settings struct {
	// StallAfter is how long a session may ask something at the terminal
	// before the owner is told.
	StallAfter time.Duration
	// HostGrace is how long a session may be without a running host before
	// it is reported gone. Zero means DefaultHostGrace.
	HostGrace time.Duration
}

const (
	// DefaultHostGrace covers the gap between the ledger saying a task is
	// running and its host process appearing: creating the worktree and
	// starting the host takes seconds, and a session seen in that gap must
	// not be reported dead.
	DefaultHostGrace = 60 * time.Second

	// defaultStallAfter is used when Settings carries no limit. A zero
	// limit would report a stall the moment a session asked anything, so a
	// caller that forgot to set it gets the documented ten minutes and not
	// a page per prompt.
	defaultStallAfter = 10 * time.Minute

	// defaultTick is Run's pace when it is given none.
	defaultTick = 5 * time.Second
)

// The titles of the two inbox items that are about the machine or the
// project and not about one task. They are exported because the loop finds
// its own earlier item by title (the inbox schema has no field for what
// raised an item), so the title is part of how the loop avoids raising the
// same thing twice.
const (
	// PreflightTitle is the blocking item raised when this machine cannot
	// start an agent in the unattended mode without a dialog.
	PreflightTitle = "afk cannot start agents on this machine"
	// StopTitle is the item raised when the plan, the ledger or the project
	// config cannot be used.
	StopTitle = "afk cannot run this project"
)

// State kinds the executor writes that are not an ActionKind. Every other
// state's kind is the kind of the action taken.
const (
	// KindStopped: the project's folder is not there. Nothing is written.
	KindStopped = "stopped"
	// KindBlocked: a task is ready but the machine failed the preflight.
	KindBlocked = "blocked"

	// ReasonNoProject is the reason of a KindStopped state.
	ReasonNoProject = "project folder not found"
)

// The decision log's rule column: which part of docs/PLAN.md took the
// decision.
const (
	ruleCreate    = "PLAN §5.3 step 2"
	rulePreflight = "PLAN §5.3 step 3"
	ruleWatch     = "PLAN §5.3 step 4"
	rulePlan      = "PLAN §5.1"
	ruleTokens    = "PLAN §4.7"
)

// LaunchRequest asks for one session to be created and its agent started.
type LaunchRequest struct {
	Project Project
	// Task is the master-plan task id; Session is the session, branch and
	// worktree name; Description is the text the agent is started with.
	Task, Session, Description string
}

// Deps is every call the loop makes outside the project's plain files. Now,
// LoadBuild, Sessions, Preflight, Launch and RecordTokens must be set; Notify,
// NotifyIdle and Log may be nil.
type Deps struct {
	// Now is the clock. The package never reads the real one, so a test
	// moves time by hand.
	Now func() time.Time
	// LoadBuild reads the project config, the master plan it names and the
	// ledger. An error means the project cannot be run until it is fixed.
	LoadBuild func(root string) (tasks []masterplan.Task, planPath string, l ledger.Ledger, err error)
	// Sessions lists the project's sessions from local git and files.
	Sessions func(root string) ([]discover.SessionState, error)
	// Preflight says whether this machine can start an agent in the
	// unattended mode, and what to do when it cannot. It runs the agent
	// CLI's plugin listing, so the loop calls it only when a task is about
	// to launch.
	Preflight func(p Project) (ok bool, steps string)
	// Launch creates the session and starts its agent.
	Launch func(LaunchRequest) error
	// RecordTokens appends the session's token use to the token ledger and
	// returns how many lines the session has there.
	RecordTokens func(p Project, task, session, sessionID string) (lines int, err error)
	// Notify tells the owner about an inbox item the loop just added. It is
	// handed the stored item, id included, so the sender can build the same
	// text `afk inbox add` sends. Best effort: it returns nothing.
	Notify func(p Project, it inbox.Item)
	// NotifyIdle is the one notification with no item behind it: the build
	// has stopped moving and only the owner can restart it.
	NotifyIdle func(p Project, title, body string)
	// Log takes one line for the daemon's own log.
	Log func(format string, args ...any)
}

func (d Deps) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

// errLedgerMoved means the ledger on disk no longer holds what this tick
// decided from: the owner ran `afk ledger set`, or another writer got there
// first. Nothing is written, and the next tick decides again from disk.
var errLedgerMoved = errors.New("the ledger changed while afk was deciding")

// pass holds one tick's working values. One is made at the start of a tick
// and dropped at its end; it is a way to pass six arguments, not a place to
// keep anything.
type pass struct {
	p     Project
	d     Deps
	now   time.Time
	store inbox.Store
	obs   Observation
	// planPath is the master plan's path as LoadBuild gave it.
	planPath string
	// row is the discovered session the SessionView was built from, kept
	// because shipping and failing need two things the view does not carry:
	// the agent's own session id (for the token ledger) and the verdict.
	row *discover.SessionState
}

// Tick runs the loop once for one project and returns the project's state as
// it now stands on disk.
//
// A root that is not a git checkout is reported and left completely alone:
// the path comes from a config file typed by hand, and a mistyped one must
// never grow an `.afk` folder somewhere the owner did not mean.
func Tick(p Project, s Settings, d Deps) daemonstate.State {
	now := d.Now()
	if !isCheckout(p.Root) {
		d.logf("%s: %s at %s: no folder there, or no .git in it. Check the path in config.toml.", p.Name, ReasonNoProject, p.Root)
		return daemonstate.State{At: stamp(now), Kind: KindStopped, Reason: ReasonNoProject}
	}
	t := &pass{p: p, d: d, now: now, store: inbox.Store{Dir: inbox.Dir(p.Root), Now: d.Now}}
	st := t.run(s)
	st.At = stamp(now)
	return t.save(st)
}

// Once runs one tick for every project, in order.
func Once(projects []Project, s Settings, d Deps) []daemonstate.State {
	out := make([]daemonstate.State, 0, len(projects))
	for _, p := range projects {
		out = append(out, Tick(p, s, d))
	}
	return out
}

// Run ticks every project, in order, every tick until ctx ends, then returns
// nil. Ending is not an error: the sessions go on running under their own
// hosts, and the next daemon picks them up from the ledger.
//
// A project that cannot be run does not stop the others: Tick turns every
// failure into a state, an inbox item or a log line and returns.
func Run(ctx context.Context, projects []Project, s Settings, tick time.Duration, d Deps) error {
	if tick <= 0 {
		tick = defaultTick
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		for _, p := range projects {
			if ctx.Err() != nil {
				return nil
			}
			Tick(p, s, d)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// isCheckout reports whether root is a folder holding a .git entry. Lstat,
// because .git is a file in a linked worktree and a folder in a main
// checkout, and either is enough to say "git knows this place".
func isCheckout(root string) bool {
	if root == "" {
		return false
	}
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return false
	}
	_, err = os.Lstat(filepath.Join(root, ".git"))
	return err == nil
}

// stamp writes a time the way the ledger, the inbox and the logs store one.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// run observes, decides and acts, and returns the state to store (without
// its time).
func (t *pass) run(s Settings) daemonstate.State {
	root := t.p.Root
	o := Observation{
		Now:        t.now,
		StallAfter: s.StallAfter,
		HostGrace:  s.HostGrace,
		Paused:     daemonstate.Paused(root),
	}
	if o.StallAfter <= 0 {
		o.StallAfter = defaultStallAfter
	}
	if o.HostGrace <= 0 {
		o.HostGrace = DefaultHostGrace
	}

	tasks, planPath, l, err := t.d.LoadBuild(root)
	if err != nil {
		o.BuildErr = err
	} else {
		o.Tasks, o.Ledger, t.planPath = tasks, l, planPath
	}

	// Files that look like items but cannot be read are left out of Items,
	// which is the safe reading everywhere Decide uses them: an id that is
	// not found counts as open, so a task parked on a damaged item stays
	// parked.
	items, _, err := t.store.List()
	if err != nil {
		o.InboxErr = err
	} else {
		o.Items = items
	}

	if o.BuildErr == nil {
		if e, ok := watched(o.Ledger); ok {
			rows, err := t.d.Sessions(root)
			if err != nil {
				// Never read as "the session is missing": that would
				// create it a second time over a session that is there.
				t.d.logf("%s: could not look at the sessions for %s: %v", t.p.Name, e.ID, err)
				t.obs = o
				return daemonstate.State{Kind: string(ActWait), Task: e.ID, Session: e.Session,
					Reason: fmt.Sprintf("the project's sessions could not be read (%v); trying again on the next pass", err)}
			}
			o.Session, t.row = viewOf(rows, e.Session)
		}
	}
	t.obs = o

	a := Decide(o)

	// The project can be read again: close what was said about it, so a
	// later error is not hidden behind an old one. Only when Decide did not
	// stop, because a stop also comes from a ledger changed by hand while
	// the plan reads fine, and answering that item here would have the next
	// tick raise it again, for ever.
	if a.Kind != ActStop && o.InboxErr == nil {
		t.clearStops()
	}

	switch a.Kind {
	case ActStop:
		return t.stop(a)
	case ActLaunch:
		return t.launch(a)
	case ActCreate:
		return t.start(a.Kind, a.Task, a.Session, a.Reason)
	case ActShip:
		return t.ship(a)
	case ActFail:
		return t.fail(a)
	case ActPark:
		return t.park(a)
	case ActStall, ActHostGone:
		return t.report(a)
	case ActUnpark:
		return t.unpark(a)
	default:
		// ActWait and ActIdle change nothing.
		return stateOf(a)
	}
}

// watched returns the entry whose session this tick must look at: the first
// running or shipping entry in ledger order, when it is running and names a
// session. It follows Decide's rule 2 so that sessions, which cost a git
// call to list, are read only on the ticks that use them.
func watched(l ledger.Ledger) (ledger.Entry, bool) {
	for _, e := range l.Tasks {
		if e.Status != ledger.Running && e.Status != ledger.Shipping {
			continue
		}
		return e, e.Status == ledger.Running && e.Session != ""
	}
	return ledger.Entry{}, false
}

// viewOf finds the session called name among the discovered rows and turns
// it into what Decide looks at. It returns nil for a name with no real
// session behind it.
//
// Only a row that came from a status.json counts. Discovery also returns a
// row for a worktree that has a branch but no `.afk/{name}/` folder, and for
// a one-off scratch session; neither has a status file, so neither has a
// phase to watch. Treating the first as a session would leave a half-made
// one (the daemon was killed after the worktree was created and before its
// files were seeded) waiting for a status that will never be written, and
// then reported as a dead host. As "no session" it reaches ActCreate, whose
// Launch fails on the branch that is already there and tells the owner how
// to clear it.
func viewOf(rows []discover.SessionState, name string) (*SessionView, *discover.SessionState) {
	for i := range rows {
		r := &rows[i]
		if r.Name != name || r.Scratch {
			continue
		}
		if r.Kind == session.KindWorktreeNoSession || r.Kind == session.KindWorktreeMissing {
			continue
		}
		v := &SessionView{
			Name:          r.Name,
			Stale:         r.StatusStale,
			Phase:         r.Phase,
			AwaitingInput: r.AwaitingInput,
			HostRunning:   r.HostRunning,
		}
		// The zero time when it cannot be read: Decide then measures from
		// the attempt's start instead.
		v.UpdatedAt, _ = parseTime(r.UpdatedAt)
		return v, r
	}
	return nil, nil
}

// stateOf is the state of a tick that did exactly what Decide said.
func stateOf(a Action) daemonstate.State {
	return daemonstate.State{Kind: string(a.Kind), Task: a.Task, Session: a.Session, Reason: a.Reason}
}

// save stores the tick's state and returns what is on disk afterwards.
//
// The file is written only when something other than the time changed, so
// its time says when the project entered this state, which is what `afk
// daemon status` shows as "since". The same comparison is what makes the
// idle notification fire once: there is no remembered "already told" flag,
// only the fact that the stored state already says so. For that reason the
// notification follows a successful write and is skipped after a failed
// one, since a state that cannot be stored would otherwise notify on every
// tick.
func (t *pass) save(st daemonstate.State) daemonstate.State {
	prev, had := daemonstate.ReadState(t.p.Root)
	if had && prev.Same(st) {
		return prev
	}
	if err := daemonstate.WriteState(t.p.Root, st); err != nil {
		t.d.logf("%s: %v", t.p.Name, err)
		return st
	}
	if st.Kind == string(ActIdle) && t.d.NotifyIdle != nil {
		switch {
		case strings.HasPrefix(st.Reason, string(nexttask.ReasonWaitingOnOwner)):
			t.d.NotifyIdle(t.p, fmt.Sprintf("%s is waiting on you", t.p.Name), st.Reason)
		case strings.HasPrefix(st.Reason, string(nexttask.ReasonBlockedByFailed)):
			t.d.NotifyIdle(t.p, fmt.Sprintf("%s is blocked by a failed task", t.p.Name), st.Reason)
		}
	}
	return st
}

// record appends one line to the decision log. It is called only for a
// transition (the ledger changed, an item was added or answered, tokens were
// recorded), never for a state that merely persists, so the log's length is
// the number of things that happened and not the number of ticks.
func (t *pass) record(kind, task, sess, question, chosen, rule string) {
	err := decisionlog.Append(decisionlog.Path(t.p.Root), decisionlog.Entry{
		At:       stamp(t.now),
		Task:     task,
		Session:  sess,
		Kind:     kind,
		Question: question,
		Chosen:   chosen,
		ChosenBy: decisionlog.ChosenByPolicy,
		Rule:     rule,
	})
	if err != nil {
		t.d.logf("%s: the decision log could not be written: %v", t.p.Name, err)
	}
}

// add stores an inbox item and tells the owner about it.
func (t *pass) add(n inbox.NewItem) (inbox.Item, error) {
	it, err := t.store.Add(n)
	if err != nil {
		t.d.logf("%s: the inbox item %q could not be written: %v", t.p.Name, n.Title, err)
		return inbox.Item{}, err
	}
	if t.d.Notify != nil {
		t.d.Notify(t.p, it)
	}
	return it, nil
}

// openTitled finds an open item with exactly this title.
func openTitled(items []inbox.Item, title string) (inbox.Item, bool) {
	for _, it := range items {
		if it.Open() && it.Title == title {
			return it, true
		}
	}
	return inbox.Item{}, false
}

// change makes one transition of one ledger entry, and refuses when the
// entry on disk is not in the status this tick decided from.
//
// The check is what keeps a tick honest about its own observation. The
// ledger was read some time before this write, and `afk ledger set` can run
// in between; ledger.Update re-reads under its lock, so the callback sees
// the entry as it is now. Writing over a status the owner has just set by
// hand would undo their change with nothing to show for it.
func (t *pass) change(id string, from ledger.Status, fn func(e *ledger.Entry)) error {
	return ledger.Update(t.p.Root, func(l *ledger.Ledger) error {
		for i := range l.Tasks {
			if l.Tasks[i].ID != id {
				continue
			}
			if l.Tasks[i].Status != from {
				return errLedgerMoved
			}
			fn(&l.Tasks[i])
			return nil
		}
		// A task with no entry is pending (PLAN §4.2).
		if from != ledger.Pending {
			return errLedgerMoved
		}
		e := ledger.Entry{ID: id, Status: ledger.Pending}
		fn(&e)
		l.Tasks = append(l.Tasks, e)
		return nil
	})
}

// ledgerTrouble is the state of a tick whose ledger write failed. The tick
// ends there; the next one decides again from disk.
func (t *pass) ledgerTrouble(a Action, err error) daemonstate.State {
	t.d.logf("%s: the ledger could not be changed for %s (%s): %v", t.p.Name, a.Task, a.Kind, err)
	return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
		Reason: fmt.Sprintf("the ledger could not be changed for %s (%v); trying again on the next pass", a.Task, err)}
}

// launch starts the next task: preflight, then the ledger, then the session.
func (t *pass) launch(a Action) daemonstate.State {
	blocked := func(id string) daemonstate.State {
		return daemonstate.State{Kind: KindBlocked, Task: a.Task,
			Reason: fmt.Sprintf("%s; see inbox item %s", PreflightTitle, id)}
	}

	// An open item means preflight already failed and the owner has not
	// said the machine is fixed. Preflight is not run again until they do:
	// it starts the agent CLI's plugin listing, which must not run on every
	// tick, and the answer is the owner's way of saying "try again".
	if it, ok := openTitled(t.obs.Items, PreflightTitle); ok {
		return blocked(it.ID)
	}

	if ok, steps := t.d.Preflight(t.p); !ok {
		body := strings.TrimSpace(steps)
		if body == "" {
			body = "The check that this machine can start an agent without a dialog failed, and gave no steps."
		}
		body += "\n\nNothing is started until this item is answered. When the machine is fixed, answer it and afk checks again."
		it, err := t.add(inbox.NewItem{Kind: "failure", Blocking: true, Title: PreflightTitle, Body: body})
		if err != nil {
			return daemonstate.State{Kind: KindBlocked, Task: a.Task,
				Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v)", PreflightTitle, err)}
		}
		t.record("preflight_failed", a.Task, "",
			"Can this machine start an agent in the unattended mode without a dialog?",
			fmt.Sprintf("no; nothing is launched until inbox item %s is answered", it.ID), rulePreflight)
		return blocked(it.ID)
	}

	// The ledger is written before the session is created. Killed between
	// the two, the next tick finds a running entry with no session and
	// creates that same session (ActCreate), as the same attempt. The other
	// order would leave a session nobody is watching and a ledger that
	// starts the task a second time beside it.
	var name string
	err := t.change(a.Task, ledger.Pending, func(e *ledger.Entry) {
		attempt := e.Attempts + 1
		name = SessionName(e.ID, attempt)
		e.Status = ledger.Running
		e.Session, e.Branch = name, name
		e.Attempts = attempt
		e.StartedAt = stamp(t.now)
		e.ParkedOn = nil
		e.FinishedAt = ""
	})
	if err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("launch", a.Task, name,
		fmt.Sprintf("Which task runs next in %s?", t.p.Name),
		fmt.Sprintf("%s, in session %s", a.Task, name), ruleCreate)
	return t.start(ActLaunch, a.Task, name, fmt.Sprintf("started %s in session %s", a.Task, name))
}

// start creates the session for a running entry. It is the second half of a
// launch, and all of an ActCreate: the entry's recorded session is created
// again with no ledger change and no new attempt.
func (t *pass) start(kind ActionKind, task, name, reason string) daemonstate.State {
	req := LaunchRequest{
		Project:     t.p,
		Task:        task,
		Session:     name,
		Description: fmt.Sprintf("Implement %s from %s", task, t.relPlan()),
	}
	if err := t.d.Launch(req); err != nil {
		return t.launchFailed(task, name, err)
	}
	return daemonstate.State{Kind: string(kind), Task: task, Session: name, Reason: reason}
}

// relPlan is the master plan's path as the agent should be told it: relative
// to the main checkout, because the agent works in a worktree where that
// same relative path names the same file.
func (t *pass) relPlan() string {
	if t.planPath == "" {
		return "the master plan"
	}
	rel, err := filepath.Rel(t.p.Root, t.planPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(t.planPath)
	}
	return filepath.ToSlash(rel)
}

// launchFailed marks a task failed because its session could not be created,
// and tells the owner how to put it right.
//
// The item is added before the ledger is written and looked for first. If
// the tick dies between the two, the next one tries the session again, fails
// again, finds the item it already raised and only finishes the ledger; the
// owner is told once either way.
func (t *pass) launchFailed(task, name string, cause error) daemonstate.State {
	a := Action{Kind: ActFail, Task: task, Session: name}
	title := fmt.Sprintf("%s could not be started (session %s)", task, name)
	it, have := openTitled(t.obs.Items, title)
	if !have {
		body := fmt.Sprintf("afk could not create the session %s for %s:\n\n%v\n\n"+
			"What to do next:\n"+
			"1. Run `afk clean %s` to remove a half-made session, if one was left behind.\n"+
			"2. Run `afk ledger set %s pending` so afk tries the task again.",
			name, task, cause, name, task)
		// A failed add is logged by add; the ledger still has to say failed,
		// or the loop would try the same broken launch on every tick.
		it, _ = t.add(inbox.NewItem{Task: task, Session: name, Kind: "failure", Title: title, Body: body})
	}
	if err := t.change(task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Failed
		e.FinishedAt = stamp(t.now)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("task_failed", task, name,
		fmt.Sprintf("The session for %s could not be created. What now?", task),
		fmt.Sprintf("mark %s failed and tell the owner: %v", task, cause), ruleCreate)
	reason := fmt.Sprintf("%s could not be started: %v", task, cause)
	if it.ID != "" {
		reason += fmt.Sprintf("; see inbox item %s", it.ID)
	}
	return daemonstate.State{Kind: string(ActFail), Task: task, Session: name, Reason: reason}
}

// tokens records the finished session's token use. It never fails the task:
// the work is done whether or not its cost could be counted, so an error is
// logged, written to the decision log, and left behind.
func (t *pass) tokens(a Action) {
	id := ""
	if t.row != nil {
		id = t.row.SessionID
	}
	question := fmt.Sprintf("What did the session for %s use?", a.Task)
	n, err := t.d.RecordTokens(t.p, a.Task, a.Session, id)
	if err != nil {
		t.d.logf("%s: token use for %s (%s) was not recorded: %v", t.p.Name, a.Task, a.Session, err)
		t.record("tokens", a.Task, a.Session, question, fmt.Sprintf("not recorded: %v", err), ruleTokens)
		return
	}
	t.record("tokens", a.Task, a.Session, question, fmt.Sprintf("recorded %d token ledger lines", n), ruleTokens)
}

// ship hands a finished task to shipping, which is the end of the line here:
// nothing in this package merges, pushes or cleans. The task waits in
// `shipping` until the ship step exists or the owner moves it by hand.
func (t *pass) ship(a Action) daemonstate.State {
	t.tokens(a)
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Shipping
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("ship_wait", a.Task, a.Session,
		fmt.Sprintf("The session for %s finished. What now?", a.Task),
		"wait in `shipping`; shipping is a later task's work", ruleWatch)
	return stateOf(a)
}

// fail marks a task failed after its session ended as failed, with the
// review's verdict in an inbox item. The item comes before the ledger write
// and is looked for first, for the reason given on launchFailed.
func (t *pass) fail(a Action) daemonstate.State {
	t.tokens(a)
	title := fmt.Sprintf("%s failed (session %s)", a.Task, a.Session)
	if _, have := openTitled(t.obs.Items, title); !have {
		_, _ = t.add(inbox.NewItem{Task: a.Task, Session: a.Session, Kind: "failure", Title: title, Body: t.failBody(a)})
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Failed
		e.FinishedAt = stamp(t.now)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("task_failed", a.Task, a.Session,
		fmt.Sprintf("The session for %s ended as failed. What now?", a.Task),
		fmt.Sprintf("mark %s failed, tell the owner and move on", a.Task), ruleWatch)
	return stateOf(a)
}

// failBody is the body of a failed task's item: what the review said when
// there is a verdict, and where to look when there is not.
func (t *pass) failBody(a Action) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The session %s for %s ended as failed.\n\n", a.Session, a.Task)
	where := ""
	if t.row != nil && t.row.WorktreePath != "" {
		where = filepath.Join(t.row.WorktreePath, ".afk", a.Session)
	}
	if t.row != nil && t.row.Verdict != nil {
		v := t.row.Verdict
		fmt.Fprintf(&b, "Checks:\n- Compliance: %s\n- Quality: %s\n- QA: %s\n", v.Compliance, v.Quality, v.QA)
		var lines []string
		for _, f := range v.Findings {
			if f.Severity == "blocker" || f.Severity == "major" {
				lines = append(lines, fmt.Sprintf("- [%s] %s: %s", f.Severity, f.TaskID, f.Description))
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\nFindings:\n%s\n", strings.Join(lines, "\n"))
		}
		if where != "" {
			fmt.Fprintf(&b, "\nThe full verdict and the session's notes are in %s.\n", where)
		}
	} else if where != "" {
		fmt.Fprintf(&b, "It left no review verdict afk could read. The session's files are in %s.\n", where)
	} else {
		fmt.Fprintf(&b, "It left no review verdict afk could read. The session's files are in its worktree, under .afk/%s.\n", a.Session)
	}
	fmt.Fprintf(&b, "\nWhat to do next: afk has moved on to any task that does not depend on this one. "+
		"To try %s again in a new session, run `afk ledger set %s pending`.", a.Task, a.Task)
	return b.String()
}

// park records that a task waits on the owner. Its session is left alone.
func (t *pass) park(a Action) daemonstate.State {
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = slices.Clone(a.Items)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("park", a.Task, a.Session,
		fmt.Sprintf("%s has a question only the owner can answer. What now?", a.Task),
		fmt.Sprintf("park it on inbox %s", strings.Join(a.Items, ", ")), ruleWatch)
	return stateOf(a)
}

// unpark marks a parked task running again. No session is started: the one
// it had is still there, and its original start time is kept, because that
// time is what Decide's once-per-attempt limit measures from.
func (t *pass) unpark(a Action) daemonstate.State {
	if err := t.change(a.Task, ledger.Parked, func(e *ledger.Entry) {
		e.Status = ledger.Running
		e.ParkedOn = nil
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("unpark", a.Task, a.Session,
		fmt.Sprintf("Every item %s was parked on is answered. What now?", a.Task),
		"mark it running again; its session is still there", ruleWatch)
	return stateOf(a)
}

// report raises the item for a stalled session or a dead host and parks the
// task on it.
//
// The item is added first because the ledger entry needs its id. If the tick
// dies before the ledger is written, the next one sees an open blocking item
// for the task and parks on it (Decide's rule e), so no second item is
// raised.
func (t *pass) report(a Action) daemonstate.State {
	n := inbox.NewItem{Task: a.Task, Session: a.Session, Kind: "failure", Blocking: true}
	kind := "stall"
	if a.Kind == ActStall {
		n.Title = StallTitlePrefix + fmt.Sprintf("%s (%s)", a.Task, a.Session)
		n.Body = stallBody(a.Task, a.Session)
	} else {
		kind = "host_gone"
		n.Title = HostGoneTitlePrefix + fmt.Sprintf("%s (%s)", a.Task, a.Session)
		n.Body = hostGoneBody(a.Task, a.Session)
	}
	it, err := t.add(n)
	if err != nil {
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
			Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v); trying again on the next pass", a.Reason, err)}
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = []string{it.ID}
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record(kind, a.Task, a.Session,
		fmt.Sprintf("%s. What now?", upperFirst(a.Reason)),
		fmt.Sprintf("tell the owner and park %s on inbox %s", a.Task, it.ID), ruleWatch)
	st := stateOf(a)
	st.Reason = fmt.Sprintf("%s; see inbox item %s", a.Reason, it.ID)
	return st
}

// stallBody tells the owner what to do about a session that is asking
// something. `afk attach` is the right tool here because the host is alive:
// attach connects to a running host and never starts one.
//
// The body cannot name its own item's id, which the store picks when the
// item is written, so it says where the id is shown.
func stallBody(task, sess string) string {
	return fmt.Sprintf("The session for %s is asking something at the terminal, and has been for longer than afk waits. "+
		"afk cannot yet wake a session by itself, so someone has to look.\n\n"+
		"What to do next:\n"+
		"1. Run `afk attach %s` and answer what it is asking. If the agent is idle instead, type `/afk:go --session %s` there.\n"+
		"2. Then answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
		"afk goes back to watching %s once it is answered.",
		task, sess, sess, task)
}

// hostGoneBody tells the owner what to do about a session whose host has
// stopped. It deliberately does not send them to the attach command, which
// only connects to a live host and would report that there is none; the
// dashboard is what starts a stopped host again, and it does so in the mode
// the session was created in.
func hostGoneBody(task, sess string) string {
	return fmt.Sprintf("The session for %s has stopped running: its host process is gone. "+
		"afk cannot yet wake a session by itself, so someone has to look.\n\n"+
		"What to do next, either:\n"+
		"1. Run `afk dash` and open the session %s. That starts its stopped host again, in the same mode. "+
		"If the agent is idle there, type `/afk:go --session %s`.\n"+
		"2. Then answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
		"afk goes back to watching %s once it is answered.\n\n"+
		"Or, if the session is beyond saving: run `afk clean %s`, then `afk ledger set %s pending`, "+
		"and afk starts the task again in a new session.",
		task, sess, sess, task, sess, task)
}

// upperFirst capitalises a reason so it can open a sentence. Reasons from
// Decide are ASCII at their first byte.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// stop tells the owner the project cannot be run, once per distinct error.
//
// An open item with the same title and the same text means this error is
// already in front of the owner, and the tick adds nothing. A different
// error gets its own item, so fixing one problem and hitting another is not
// hidden behind the first report.
func (t *pass) stop(a Action) daemonstate.State {
	st := stateOf(a)
	if t.obs.InboxErr != nil {
		// Without the items there is no telling whether this was already
		// said, and saying it again every tick is worse than the log line.
		t.d.logf("%s: %s: %s (and the inbox could not be read: %v)", t.p.Name, StopTitle, a.Reason, t.obs.InboxErr)
		return st
	}
	body := strings.TrimSpace(a.Reason)
	for _, it := range t.obs.Items {
		if it.Open() && it.Title == StopTitle && strings.TrimSpace(it.Body) == body {
			return st
		}
	}
	it, err := t.add(inbox.NewItem{Kind: "failure", Title: StopTitle, Body: body})
	if err != nil {
		return st
	}
	t.record("plan_error", a.Task, "",
		fmt.Sprintf("Can %s be run?", t.p.Name),
		fmt.Sprintf("no; nothing starts until it is fixed (inbox %s): %s", it.ID, body), rulePlan)
	return st
}

// clearStops answers every open "cannot run this project" item once the
// project can be run again.
func (t *pass) clearStops() {
	var closed []string
	for _, it := range t.obs.Items {
		if !it.Open() || it.Title != StopTitle {
			continue
		}
		if _, err := t.store.Answer(it.ID, "fixed", "cli"); err != nil {
			// Already answered by the owner in the meantime is the same
			// outcome; anything else is tried again on the next tick.
			if !errors.Is(err, inbox.ErrAlreadyAnswered) {
				t.d.logf("%s: inbox item %s could not be answered: %v", t.p.Name, it.ID, err)
			}
			continue
		}
		closed = append(closed, it.ID)
	}
	if len(closed) > 0 {
		t.record("plan_fixed", "", "",
			fmt.Sprintf("Can %s be run?", t.p.Name),
			fmt.Sprintf("yes; answered inbox %s as fixed", strings.Join(closed, ", ")), rulePlan)
	}
}
