// Package daemon is afk's build loop (docs/PLAN.md §5.3): for one project,
// one task at a time, it picks the next ready master-plan task, starts a
// session for it, and watches that session's files until the task is done,
// failed, parked or stalled.
//
// The loop is split in two. Decide, in this file, is the whole choice of what
// to do on a tick, as one pure function over what is on disk. The executor
// reads the disk into an Observation, calls Decide, and carries out the one
// Action it gets back. The split is what lets every rule be a row in a table
// test with no project, no session and no clock.
//
// The rule the whole package is built on: nothing is held in memory that is
// not on disk. A tick starts from the ledger, the inbox, the plan and the
// session's files, and from nothing else. That is why the daemon can be
// killed at any point and started again safely: there is no state to lose,
// and the next tick reaches the same decision the dead process would have.
// It is also why Decide's limits that must fire once (a stall report, a
// host-gone report) are worked out from inbox items and not from a counter.
package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/nexttask"
	"github.com/dphaener/afk/internal/state"
)

// The title prefixes of the two reports the loop raises about a session it
// cannot move by itself. The executor starts each item's title with one of
// them, and Decide finds an earlier report by the same prefix, so the two
// must stay distinct: neither may begin with the other.
//
// They are titles and not a field of the item because the inbox schema
// (PLAN §4.3) has no field for "what raised this", and a title is the one
// free-text key every reader already shows.
const (
	// StallTitlePrefix starts the title of the item raised when a session
	// has been asking something at the terminal for longer than StallAfter.
	StallTitlePrefix = "Session waiting at the terminal: "
	// HostGoneTitlePrefix starts the title of the item raised when a
	// session's host process is no longer running.
	HostGoneTitlePrefix = "Session stopped running: "
)

// SessionName is the session (and branch, and worktree folder) a task attempt
// runs in: the id in lower case with its dot turned into a hyphen, so T4.3
// is t4-3 and T4.2a is t4-2a. The first attempt has no suffix; attempt N from
// 2 up appends -N, because the branch and worktree of an earlier attempt may
// still exist and a session cannot be created over them. An attempt below 1
// names the first attempt.
//
// Two tasks can never share a name: a task id has exactly one dot, so a
// first-attempt name has exactly one hyphen and a later one exactly two.
func SessionName(taskID string, attempt int) string {
	name := strings.ReplaceAll(strings.ToLower(taskID), ".", "-")
	if attempt < 2 {
		return name
	}
	return fmt.Sprintf("%s-%d", name, attempt)
}

// SessionView is what the loop needs to know about the session of the task
// in flight, read from its status.json and host.json.
type SessionView struct {
	Name string
	// Stale means status.json is missing or cannot be read. The other
	// status fields (Phase, AwaitingInput, UpdatedAt) mean nothing then.
	Stale         bool
	Phase         state.Phase
	AwaitingInput bool
	// UpdatedAt is when status.json was last written; the zero time when
	// that is not known.
	UpdatedAt time.Time
	// HostRunning comes from host.json, not from status.json, so it holds
	// for a stale session too. It is advisory: process ids are reused, so
	// it can say yes for a host that is gone. It does not say no for one
	// that is alive.
	HostRunning bool
}

// Observation is everything Decide looks at: plain values the executor read
// from disk and the clock on this tick.
type Observation struct {
	Now time.Time
	// InboxErr is set when the inbox could not be listed. Items is not
	// trusted then.
	InboxErr error
	// StallAfter is how long a session may ask something at the terminal
	// before the owner is told. HostGrace is how long a session may go
	// without a running host before it is reported gone.
	StallAfter, HostGrace time.Duration
	// Paused is the project's pause flag.
	Paused bool
	// NoLedger means the project has no ledger file at all. That is not the
	// same as an empty ledger: with no file there is no telling a new
	// project from a part-built one, and reading "no file" as "everything
	// is pending" is how a project with eighteen finished tasks once had
	// its first task started again. Ledger is empty then, and means nothing.
	NoLedger bool
	// Stopped is the project's stop marker: a deploy did not come up
	// healthy, and nothing new may start until `afk resume` clears it.
	// StopReason is the marker's own words, for the reason shown to the
	// owner; it may be empty, because a damaged marker still stops the line.
	Stopped    bool
	StopReason string
	// BuildErr is set when the plan, the ledger or the project config
	// could not be used. Tasks and Ledger are not trusted then.
	BuildErr error
	Tasks    []masterplan.Task
	Ledger   ledger.Ledger
	// Items is every readable inbox item, open and answered.
	Items []inbox.Item
	// Session is the session named by the active ledger entry, or nil when
	// that session is not on disk.
	Session *SessionView
	// Parked holds the session of each parked entry that names one, by task
	// id; a task whose session is not on disk has no key. It is filled only
	// on a tick with no active entry, which is the only tick that can
	// un-park. Decide needs it for one question: has the parked session
	// already finished? Session cannot answer that, because it is the active
	// entry's and a parked entry is never the active one.
	Parked map[string]*SessionView
	// ParkedErr is set when the sessions could not be listed on a tick that
	// has no active entry. Parked means nothing then, and no task is
	// un-parked: with no view, a finished session would be read as one that
	// is still working.
	ParkedErr error
}

// ActionKind names the one thing the executor does on a tick.
type ActionKind string

const (
	// ActStop: the project cannot be run until the owner fixes something.
	ActStop ActionKind = "stop"
	// ActWait: nothing to do on this tick; look again on the next.
	ActWait ActionKind = "wait"
	// ActCreate: the ledger says a task is running but its session is not
	// on disk. Create the recorded session again, as the same attempt.
	ActCreate ActionKind = "create"
	// ActShip: the session finished. Hand the task to shipping.
	ActShip ActionKind = "ship"
	// ActFail: the session ended as failed.
	ActFail ActionKind = "fail"
	// ActPark: the owner has to answer Items before the task can go on.
	ActPark ActionKind = "park"
	// ActHostGone: the session's host is not running. Tell the owner.
	ActHostGone ActionKind = "host_gone"
	// ActStall: the session has been asking something at the terminal for
	// too long. Tell the owner.
	ActStall ActionKind = "stall"
	// ActUnpark: every item a parked task waited on is answered. Mark it
	// running again; its session is still there.
	ActUnpark ActionKind = "unpark"
	// ActLaunch: start Task in a new session. Session is empty: the
	// executor works out the attempt and the name.
	ActLaunch ActionKind = "launch"
	// ActIdle: nothing is in flight and nothing can start.
	ActIdle ActionKind = "idle"
	// ActNoLedger: the project has no ledger file. Start nothing and tell
	// the owner how to make one.
	ActNoLedger ActionKind = "no_ledger"
	// ActOwner: the next task is the owner's to do, not an agent's. Raise
	// one inbox item for it and park the task on that item. No session.
	ActOwner ActionKind = "owner_task"
	// ActOwnerDone: the owner answered the item their task was parked on,
	// which is how they say it is done. Mark it done.
	ActOwnerDone ActionKind = "owner_done"
	// ActShipStep: a task is shipping. Take its ship one step forward (push,
	// pull request, checks, merge, deploy check), or wait for whatever that
	// step waits on. ActShip is the hand-over into shipping; this is every
	// tick after it.
	ActShipStep ActionKind = "shipping"
)

// KindLineStopped is the state kind of a tick held back by the stop marker.
// It is not an ActionKind: Decide answers such a tick with ActWait (nothing
// is done), and sets Action.LineStopped so the stored state can say which
// kind of waiting this is. It is also not KindStopped, which was taken first
// and means the project's folder is not there.
const KindLineStopped = "line_stopped"

// Action is Decide's answer. Reason is a plain-language sentence for
// `afk daemon status`; it is always set.
//
// Items holds inbox item ids for ActPark (the items to park on) and for an
// ActIdle that is waiting on the owner (the open items the parked tasks wait
// on). For an ActIdle that is blocked by failed tasks it holds those task
// ids instead. An ActIdle's Reason begins with the nexttask.Reason word it
// came from ("waiting on owner", "blocked by failed", "all done", "stuck"),
// which is how the executor tells them apart.
//
// LineStopped is set on an ActWait that waits because of the stop marker and
// for no other reason. It is a field, not a word at the start of Reason,
// because the reason carries the marker's own text, which this package does
// not write and cannot promise the shape of.
//
// Resume is set on an ActUnpark and nowhere else: the status the task goes
// back to, ledger.Running or ledger.Shipping. It is decided here and not in
// the executor because it is a choice with a wrong answer (see decideNext),
// and so it belongs in the table.
type Action struct {
	Kind          ActionKind
	Task, Session string
	Reason        string
	Items         []string
	LineStopped   bool
	Resume        ledger.Status
}

// Decide chooses what the loop does on this tick. It reads no file, no clock
// and no environment: the same Observation always gives the same Action.
//
// The rules, first match wins:
//
//  1. The build cannot be read (BuildErr): stop. The inbox cannot be listed
//     (InboxErr): wait, because without the items every park and every
//     duplicate check below would be wrong. A running or shipping entry that
//     is not in the plan, or that names no session: stop. Both come from a
//     ledger changed by hand, and creating a session for a task that is gone,
//     or under an empty name, is worse than standing still.
//     The project has no ledger file (NoLedger), checked after the two
//     errors and before the entries: start nothing and say how to make one.
//     After the errors, because a damaged project is the more useful thing
//     to report and because without the inbox the executor could not tell
//     whether it had already said so. Before the entries, because there is
//     no ledger to check.
//  2. The active entry is the first in ledger order that is running or
//     shipping. Shipping: take the ship one step (ActShipStep), whatever the
//     pause flag and the stop marker say.
//  3. Running: see decideRunning.
//  4. Nothing active and the line is stopped: wait, whatever would be next.
//  5. Otherwise ask nexttask.Next (see decideNext). An owner task is raised
//     as an inbox item, or marked done once that item is answered; a parked
//     task whose items are all answered is un-parked, back to shipping when
//     it was shipping or its session has finished and to running otherwise;
//     a pending task is launched; no task is idle.
//  6. Nothing active and the project is paused: rule 5's answer stands only
//     when it is about an owner task; anything else waits.
//
// A running entry is watched, and a shipping one shipped, even when the
// project is paused or the line is stopped. Both stop new work, not the work
// already paid for. For the stop marker this is more than a courtesy: the
// task whose deploy stopped the line is itself still shipping, and its ship
// is what notices that `afk resume` removed the marker and finishes the task.
// Holding it back would leave the line stopped for ever.
//
// Pause and the stopped line differ on owner tasks, on purpose. `afk pause`
// promises only that no new task starts. Raising the owner's item and marking
// their task done start nothing, so pause lets both through. A stopped line
// means the last deploy is broken and the owner has to look before the build
// moves at all, so it holds back everything, those two included. That is why
// rule 4 is checked before nexttask is asked and rule 6 after.
func Decide(o Observation) Action {
	if o.BuildErr != nil {
		return Action{Kind: ActStop, Reason: o.BuildErr.Error()}
	}
	if o.InboxErr != nil {
		return Action{Kind: ActWait, Reason: fmt.Sprintf(
			"the inbox could not be read (%v); nothing is decided until it can be", o.InboxErr)}
	}

	if o.NoLedger {
		return Action{Kind: ActNoLedger, Reason: "waiting on the owner: this project has no task list yet. " +
			"Run `afk ledger init` for a new project, or `afk ledger init --done-through <task>` for one that is part-built."}
	}

	inPlan := make(map[string]bool, len(o.Tasks))
	for _, t := range o.Tasks {
		inPlan[t.ID] = true
	}

	// Every running or shipping entry is checked, not only the first: an
	// entry the plan does not have is invisible to nexttask.Next, which
	// would otherwise start a second task beside it once the first ended.
	var active *ledger.Entry
	for i := range o.Ledger.Tasks {
		e := o.Ledger.Tasks[i]
		if e.Status != ledger.Running && e.Status != ledger.Shipping {
			continue
		}
		if !inPlan[e.ID] {
			return Action{Kind: ActStop, Task: e.ID, Reason: fmt.Sprintf(
				"%s is %s in the ledger but is not a task in the plan. Put it back in the plan, or correct the ledger with `afk ledger set`.",
				e.ID, e.Status)}
		}
		if e.Session == "" {
			return Action{Kind: ActStop, Task: e.ID, Reason: fmt.Sprintf(
				"%s is %s in the ledger but has no session. Set it back with `afk ledger set %s pending`.",
				e.ID, e.Status, e.ID)}
		}
		if active == nil {
			active = &e
		}
	}

	if active != nil {
		if active.Status == ledger.Shipping {
			return Action{Kind: ActShipStep, Task: active.ID, Session: active.Session,
				Reason: fmt.Sprintf("shipping %s", active.ID)}
		}
		return decideRunning(o, *active)
	}

	if o.Stopped {
		return Action{Kind: ActWait, LineStopped: true, Reason: stoppedReason(o)}
	}
	a := decideNext(o)
	if o.Paused && a.Kind != ActOwner && a.Kind != ActOwnerDone {
		return Action{Kind: ActWait, Reason: "paused; no new task will start until `afk resume`"}
	}
	return a
}

// stoppedReason is the sentence for a tick held back by the stop marker: that
// the line is stopped, why when the marker says, and the one command that
// clears it.
func stoppedReason(o Observation) string {
	why := strings.TrimSpace(o.StopReason)
	if why == "" {
		return "the line is stopped; nothing new starts until `afk resume` clears it"
	}
	return fmt.Sprintf("the line is stopped (%s); nothing new starts until `afk resume` clears it", why)
}

// decideRunning is rule 3: what to do about the one task in flight.
//
//	a. Its session is not on disk: create it again, or wait when paused or
//	   when the line is stopped.
//	b. status.json cannot be read: wait, unless the host is gone.
//	c. Phase done: ship.
//	d. Phase failed: fail.
//	e. An open blocking item for the task or session: park on it.
//	f. The host is not running, past HostGrace: report it gone.
//	g. The session is asking something, past StallAfter: report a stall.
//	h. Otherwise wait.
//
// The order carries meaning. A finished session (c, d) is acted on before
// anything about its host, so that a host which is gone by the time its
// session says done or failed is not reported as a dead session: the work is
// over either way. Nothing here assumes the host does exit. The ship that
// follows relies on the opposite for one step only, the hand-back of a red
// CI, which types into the session and so needs its host alive after done;
// when the host is not there that prompt fails and the task parks with an
// item saying how to reach the session.
// Parking (e) comes before the two reports because a parked session sits at
// a prompt by design, and the owner has already been sent the question.
func decideRunning(o Observation, e ledger.Entry) Action {
	act := func(kind ActionKind, reason string) Action {
		return Action{Kind: kind, Task: e.ID, Session: e.Session, Reason: reason}
	}

	s := o.Session
	if s == nil {
		if o.Stopped {
			// The same reading as pause, below: there is no session to
			// watch, so this is a start, and a stopped line starts nothing.
			a := act(ActWait, fmt.Sprintf("%s; %s has no session", stoppedReason(o), e.ID))
			a.LineStopped = true
			return a
		}
		if o.Paused {
			// Creating a session is a new launch as far as the owner is
			// concerned, and pause stops every one of those.
			return act(ActWait, fmt.Sprintf("paused; %s has no session", e.ID))
		}
		return act(ActCreate, fmt.Sprintf(
			"%s is running in the ledger but its session %s is not on disk; creating it again", e.ID, e.Session))
	}

	// An unreadable start time is the zero time, which reads as "started
	// long ago": such an entry gets no grace and counts every report as
	// its own. Both lean towards telling the owner once over staying
	// silent or telling them again and again.
	startedAt, _ := parseTime(e.StartedAt)

	hostGone := func() Action {
		if reported(o.Items, e.ID, HostGoneTitlePrefix, startedAt) {
			return act(ActWait, alreadyReported)
		}
		return act(ActHostGone, fmt.Sprintf("the session for %s (%s) has stopped running", e.ID, e.Session))
	}

	if s.Stale {
		// An unreadable status.json is never a failure by itself: the
		// writers replace it all the time and a reader can catch it
		// mid-change. But host liveness comes from host.json, so a dead
		// host is still seen here, and it must be: a session with no
		// host never repairs its status file, and waiting on it would
		// be waiting for ever. The grace is measured from the entry's
		// start because the status file's own time is what is missing.
		if !s.HostRunning && o.Now.Sub(startedAt) > o.HostGrace {
			return hostGone()
		}
		return act(ActWait, fmt.Sprintf("running %s; its status file cannot be read right now", e.ID))
	}

	switch s.Phase {
	case state.PhaseDone:
		return act(ActShip, fmt.Sprintf("the session for %s finished; handing it to shipping", e.ID))
	case state.PhaseFailed:
		return act(ActFail, fmt.Sprintf("the session for %s ended as failed", e.ID))
	}

	var blocking []string
	for _, it := range o.Items {
		if it.Open() && it.Blocking && concerns(it, e) {
			blocking = append(blocking, it.ID)
		}
	}
	if len(blocking) > 0 {
		a := act(ActPark, fmt.Sprintf("%s is waiting on the owner: inbox %s", e.ID, strings.Join(blocking, ", ")))
		a.Items = blocking
		return a
	}

	// ref is the last moment anything is known to have happened to this
	// attempt: the session wrote its status, the attempt began, or the
	// owner answered one of its items. An answer counts because it is the
	// owner acting on the session; without it a task would stall again on
	// the very tick its answer un-parked it.
	ref := latest(s.UpdatedAt, startedAt)
	for _, it := range o.Items {
		if !concerns(it, e) {
			continue
		}
		if at, ok := parseTime(it.AnsweredAt); ok {
			ref = latest(ref, at)
		}
	}
	idle := o.Now.Sub(ref)

	// "Over" the limit, not "at" it, for both: a host exactly at its grace
	// or a prompt exactly at the stall limit waits one more tick.
	if !s.HostRunning && idle > o.HostGrace {
		return hostGone()
	}
	if s.AwaitingInput && idle > o.StallAfter {
		if reported(o.Items, e.ID, StallTitlePrefix, startedAt) {
			return act(ActWait, alreadyReported)
		}
		return act(ActStall, fmt.Sprintf("the session for %s (%s) is asking something at the terminal", e.ID, e.Session))
	}

	return act(ActWait, fmt.Sprintf("running %s, phase %s", e.ID, s.Phase))
}

// alreadyReported is the reason given when a stall or a dead host has been
// reported for this attempt before.
const alreadyReported = "already reported; waiting for the owner to resume the session"

// decideNext is rule 5: nothing is in flight, so ask which task is next.
//
// An owner task (nexttask's Decision.Owner, from the "(owner)" marker in its
// title) is never launched: no agent can stand up a machine or sign up for an
// account. nexttask still picks it in its ordinary place, so everything that
// depends on it waits for it like any other task. Pending, it becomes an
// inbox item and is parked on that item (ActOwner). Parked with every item
// answered, it is done (ActOwnerDone): the owner's answer is the only signal
// there is that work done away from this machine has finished. It never
// passes through running, so it never gets a session.
//
// A task marked "(owner + agent)" is not an owner task. Its Owner flag is
// false and it launches like any other.
//
// A parked agent task whose items are all answered goes back to one of two
// statuses, and the choice matters:
//
//   - shipping, when the entry holds a ship state (it was parked in the
//     middle of its ship) or its session's phase is done. The second half
//     covers a ship that parked before it saved anything (GitHub's tool not
//     logged in, uncommitted changes), which leaves no ship state behind.
//     Sent back to running, such a task would be seen as "phase done" on the
//     next tick and handed to shipping a second time, recording its tokens
//     and logging its finish twice.
//   - running, otherwise: the session parked on a question and has work left.
//     A session that is not on disk is this case too; the next tick then
//     creates it again.
//
// An unreadable status file is not "done": the phase of a stale view is
// whatever was parsed last, or nothing.
func decideNext(o Observation) Action {
	open := make(map[string]bool, len(o.Items))
	for _, it := range o.Items {
		open[it.ID] = it.Open()
	}
	d := nexttask.Next(o.Tasks, o.Ledger, func(id string) bool {
		// An item that cannot be found cannot be shown to be answered,
		// so it counts as open: a parked task is released only by an
		// answer that is on disk.
		isOpen, found := open[id]
		return !found || isOpen
	})

	if d.Task != "" {
		e, ok := o.Ledger.Get(d.Task)
		isParked := ok && e.Status == ledger.Parked
		if d.Owner {
			if isParked {
				return Action{Kind: ActOwnerDone, Task: d.Task,
					Reason: fmt.Sprintf("you answered the item for your task %s; marking it done", d.Task)}
			}
			return Action{Kind: ActOwner, Task: d.Task,
				Reason: fmt.Sprintf("waiting on the owner: %s is your task, not an agent's", d.Task)}
		}
		if isParked {
			if o.ParkedErr != nil {
				return Action{Kind: ActWait, Task: d.Task, Session: e.Session, Reason: fmt.Sprintf(
					"the items %s was parked on are answered, but the project's sessions could not be read (%v); trying again on the next pass",
					d.Task, o.ParkedErr)}
			}
			v := o.Parked[d.Task]
			if e.Ship != "" || (v != nil && !v.Stale && v.Phase == state.PhaseDone) {
				return Action{Kind: ActUnpark, Task: d.Task, Session: e.Session, Resume: ledger.Shipping,
					Reason: fmt.Sprintf("the items %s was parked on are answered; it is shipping again", d.Task)}
			}
			return Action{Kind: ActUnpark, Task: d.Task, Session: e.Session, Resume: ledger.Running,
				Reason: fmt.Sprintf("the items %s was parked on are answered; it is running again", d.Task)}
		}
		return Action{Kind: ActLaunch, Task: d.Task, Reason: fmt.Sprintf("starting %s", d.Task)}
	}

	word := string(d.Reason)
	switch d.Reason {
	case nexttask.ReasonAllDone:
		return Action{Kind: ActIdle, Reason: word + ": every task in the plan is finished"}
	case nexttask.ReasonWaitingOnOwner:
		reason := fmt.Sprintf("%s: %s parked", word, strings.Join(d.Parked, ", "))
		if len(d.WaitingOn) > 0 {
			reason += fmt.Sprintf("; answer inbox %s", strings.Join(d.WaitingOn, ", "))
		} else {
			// A parked task that names no item can only be released by
			// hand; say how.
			reason += "; no inbox item is named, so set the task with `afk ledger set`"
		}
		if len(d.Failed) > 0 {
			reason += fmt.Sprintf(" (%s failed)", strings.Join(d.Failed, ", "))
		}
		return Action{Kind: ActIdle, Reason: reason, Items: d.WaitingOn}
	case nexttask.ReasonBlockedByFailed:
		return Action{Kind: ActIdle, Items: d.Failed, Reason: fmt.Sprintf(
			"%s: %s failed and what is left depends on it. Fix it, then run `afk ledger set %s pending`.",
			word, strings.Join(d.Failed, ", "), d.Failed[0])}
	case nexttask.ReasonStuck:
		return Action{Kind: ActIdle, Reason: fmt.Sprintf(
			"%s: %s cannot start and nothing is running, parked or failed. Check the ledger for a task marked superseded with no replacement.",
			word, strings.Join(d.Stuck, ", "))}
	default:
		// Not reachable from Decide: "in progress" needs a running or
		// shipping entry in the plan, and that entry would have been
		// the active one. Idle is the answer that starts nothing.
		return Action{Kind: ActIdle, Reason: word}
	}
}

// concerns reports whether an inbox item is about the entry's task or its
// session. An item with neither set is about the project, not this task.
func concerns(it inbox.Item, e ledger.Entry) bool {
	return (it.Task != "" && it.Task == e.ID) || (it.Session != "" && it.Session == e.Session)
}

// reported says whether a stall or host-gone report with the given title
// prefix was already raised for this task during the current attempt, open
// or answered. It is what limits each report to once per attempt.
//
// Answered items count on purpose. Answering the item un-parks the task, and
// the un-park types the session's resume command into it (see unpark in
// daemon.go), but that is one attempt with no promise behind it: the host
// may be gone, the dial may fail, or the agent may be sitting in a dialog
// that swallows the line. A session that the line did not move is exactly as
// idle afterwards as it was before, and counting only open items would raise
// the same report again every StallAfter for as long as it stayed that way.
//
// "This attempt" is an item created at or after the entry's start. At, not
// only after: times are stored to the second, and an item raised in the
// second the attempt began belongs to it. An item whose creation time cannot
// be read counts as this attempt's, for the same lean towards not repeating.
func reported(items []inbox.Item, taskID, prefix string, startedAt time.Time) bool {
	for _, it := range items {
		if it.Task != taskID || !strings.HasPrefix(it.Title, prefix) {
			continue
		}
		created, ok := parseTime(it.CreatedAt)
		if !ok || !created.Before(startedAt) {
			return true
		}
	}
	return false
}

// parseTime reads a time as the ledger and the inbox store it, RFC 3339. The
// executor uses it too, so that every comparison in this package is between
// times and never between their text (two spellings of one instant, such as
// a Z and a +00:00 offset, sort differently as text).
func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// latest returns the later of two times.
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
