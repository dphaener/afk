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
	"math"
	"slices"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/nexttask"
	"github.com/dphaener/afk/internal/state"
)

// The title prefixes of the reports the loop raises about a session it
// cannot move by itself. The executor starts each item's title with one of
// them, and Decide finds an earlier stall or host-gone report by the same
// prefix, so they must stay distinct: none may begin with another.
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
	// RecoveryTitlePrefix starts the title of the item raised when a session
	// stopped or hung and the loop has used up its restarts (PLAN §5.6).
	// Decide never searches for it: the task parks on that item, and a
	// parked task is not looked at again until the item is answered.
	RecoveryTitlePrefix = "Session could not be recovered: "
	// PauseFailedTitlePrefix starts the title of the item raised when a
	// session was to be stopped for usage and its host is still running three
	// tries later (PLAN §6.4). Like RecoveryTitlePrefix it is never searched
	// for: the item is blocking and names the task, so the task parks on it
	// on the next tick, before the usage rule is reached again.
	PauseFailedTitlePrefix = "Session could not be stopped for usage: "
	// WakeTitlePrefix starts the title of the item raised when an agent's
	// turn kept failing and the loop has used up its wakes (PLAN T2.5). Like
	// RecoveryTitlePrefix it is never searched for: the item is blocking and
	// names the task, so the task parks on it.
	WakeTitlePrefix = "Session kept failing: "
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
	// WedgedSince is the host's own verdict from host.json: the moment it
	// decided its agent had stopped answering. The zero time means not
	// wedged. Like HostRunning it is advisory: the host sets it after a
	// spell of silence and an unanswered repaint request, and clears it on
	// the first byte of output (internal/host/liveness.go), so a working
	// agent can carry it for a moment. That is why the loop acts on it only
	// once it has stood for WedgeConfirm.
	WedgedSince time.Time
	// RateLimitedAt is the time in the session's rate-limit marker, written
	// by the agent's own hook when a turn ends on a usage limit; the zero
	// time when there is no marker. Nothing removes the marker when the
	// session goes on, so its time is what matters, not its being there: it
	// counts only when it is later than the attempt's start and its last
	// restart (see decideUsage).
	RateLimitedAt time.Time
	// TurnFailedAt is the time in the session's failed-turn marker, written
	// by the agent's own hook when a turn ended on an error worth trying
	// again (a lost connection, a server fault); the zero time when there is
	// no marker. The session's next prompt removes the file, but a tick can
	// still see one that a wake has already answered, so, as with the
	// rate-limit marker, its time is what counts (see decideWake).
	// TurnFailedReason is the error word the hook recorded, and may be empty.
	// It is shown to the owner and decides nothing.
	TurnFailedAt     time.Time
	TurnFailedReason string
}

// UsageView is the usage governor's verdict as the loop sees it: plain values
// the executor worked out from the latest usage reading. Decide never reads
// usage itself and never recomputes the verdict.
type UsageView struct {
	// Consulted false means the executor has no usage source, or did not ask
	// on this tick. Every usage rule is off then, and the other fields mean
	// nothing. It is a field of its own, and not "Action is empty", so that a
	// daemon wired without a usage source behaves exactly as it did before
	// the governor existed.
	Consulted bool
	// Action is the governor's word (internal/governor): run, no_launch,
	// pause, hold or refuse.
	Action governor.Action
	// Reason is the governor's own sentence. Decide does not put it in any
	// Reason of its own: it carries percentages and ages that change with
	// every reading, and the loop writes its state file and its log whenever
	// a reason changes.
	Reason string
	// At is when the reading behind the verdict was taken. A paused session
	// is resumed only on a reading taken after its pause began.
	At time.Time
	// WakeAt is the governor's own wake time for a pause; the zero time when
	// it does not know one.
	WakeAt time.Time
	// NextReset is the earliest reset still ahead among the usage windows,
	// plus one minute; the zero time when none is known. It is the wake time
	// for a session stopped by its rate-limit marker, where the governor
	// itself is not pausing and so gives no WakeAt. The earliest, because the
	// marker does not say which limit was hit: waking early costs one more
	// reading (a resume still needs one that allows work), waking late can
	// cost days.
	NextReset time.Time
}

// LoginView is the last verdict on whether the agent's CLI is logged in on
// this machine. Known false means there is no verdict (never checked, or the
// tool cannot say), and no login rule applies: unknown is not logged out.
type LoginView struct {
	Known, LoggedIn bool
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
	// MaxRestarts is how many times one attempt may be recovered (its agent
	// restarted, or its host started again) before the task is given up on.
	// Zero is a real setting: never recover, give up on the first trouble.
	MaxRestarts int
	// RestartBackoff is the wait before the second recovery; it doubles for
	// each one after that (see backoffFor). The first has no wait.
	RestartBackoff time.Duration
	// ContinueAfter is how long after a recovery the session is told to
	// carry on. The resumed agent needs that long to reach a prompt; a line
	// typed sooner is lost.
	ContinueAfter time.Duration
	// WedgeConfirm is how long the host's wedge verdict must have stood
	// before the loop acts on it. A restart stops the agent, so a verdict
	// that the next byte of output would have cleared must not cause one.
	WedgeConfirm time.Duration
	// MaxWakes is how many times one attempt's session may be woken (its
	// resume command typed because a turn failed, or because it sat waiting)
	// before the owner is told. Zero is a real setting: never wake.
	MaxWakes int
	// WakeAfter is how long after a failed turn the session is woken. The
	// wait is for the fault to pass: a connection that dropped a second ago
	// is likely still down, and a line typed into it fails the same way.
	WakeAfter time.Duration
	// CanRestart, CanRelaunch and CanPrompt say whether the executor has
	// the means to restart a session's agent, to start a session's host
	// again, and to type a line into a session. Each is optional there, so
	// Decide must not choose an action nothing can carry out: with no
	// relaunch a gone host is reported to the owner as before, with no
	// restart a wedge is left alone, with no prompt nothing is typed.
	CanRestart, CanRelaunch, CanPrompt bool
	// Usage is the usage governor's verdict on this tick, and Login the last
	// login verdict. Both are optional: the zero value of each switches its
	// rules off (see UsageView and LoginView).
	Usage UsageView
	Login LoginView
	// CanPause says whether the executor has the means to stop a session's
	// agent. Like the three above it guards against choosing an action
	// nothing can carry out. A pause also needs CanRelaunch: a session that
	// was stopped and can never be started again is worse off than one left
	// to the agent's own wait at the limit.
	CanPause bool
	// PauseRetry is how long after a pause a host that is still running is
	// made to stop; after three times as long the owner is told. The executor
	// always sets it above zero. At zero or below both are due at once, which
	// reports the session on the first tick that still sees its host.
	PauseRetry time.Duration
	// RateLimitFallback is the wake delay for a session stopped by its
	// rate-limit marker when no usage window gives a reset time.
	RateLimitFallback time.Duration
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
	// Questions is every row of the plan's Open questions table, open and
	// answered, as masterplan.Questions read it on this tick. QuestionsErr is
	// set when the plan could not be read for them, and Questions means
	// nothing then. Both are optional as the usage fields are: an executor
	// with no way to read the plan leaves them empty, no task is ever held
	// for a question, and Decide answers as it did before the hold existed.
	Questions    []masterplan.Question
	QuestionsErr error
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
	// ActRestart: the session's agent has stopped answering while its host
	// is still running. Ask the host to restart the agent, resuming the same
	// conversation.
	ActRestart ActionKind = "restart"
	// ActRelaunch: the session's host is gone (a reboot, a crash). Start a
	// new host for the same session.
	ActRelaunch ActionKind = "relaunch"
	// ActContinue: the session was recovered and its agent has had time to
	// start. Type the session's resume command so the work goes on; a
	// resumed agent otherwise sits at an empty prompt.
	ActContinue ActionKind = "continue"
	// ActGiveUp: the session stopped or hung again with every restart used.
	// Tell the owner and park the task.
	ActGiveUp ActionKind = "give_up"
	// ActUsageWait: a task would have been started, created again or
	// un-parked, and nothing new starts because of usage. Nothing is done.
	ActUsageWait ActionKind = "usage_wait"
	// ActLoginWait: the same, because the agent is logged out on this
	// machine. Nothing is done.
	ActLoginWait ActionKind = "login_wait"
	// ActUsagePause: stop the running session's agent because of usage. With
	// Action.Force false this is the start of a pause: save it in the ledger
	// with Action.WakeAt, then ask the host to end. With Force true the pause
	// is already saved and the host did not go: make it stop, and leave the
	// ledger alone so the time the pause began stands.
	ActUsagePause ActionKind = "usage_pause"
	// ActUsagePaused: the session is stopped for usage, or has been asked to
	// stop. Wait.
	ActUsagePaused ActionKind = "usage_paused"
	// ActUsageStuck: the session was to be stopped for usage and its host is
	// still running after every try, so it may still be spending. Tell the
	// owner; the task then parks on that item.
	ActUsageStuck ActionKind = "usage_stuck"
	// ActUsageResume: a session stopped for usage may go on. Start a new
	// host for it, resuming the same conversation.
	ActUsageResume ActionKind = "usage_resume"
	// ActQuestionHold: the next task is named by open rows of the plan's Open
	// questions table. Start nothing for it: raise one inbox item for each
	// row that has none and park the task on those items. Items holds the ids
	// of the items that already exist. No session.
	ActQuestionHold ActionKind = "question_hold"
	// ActQuestionAnswered: every item a held task waited on is answered.
	// Write the answers into the plan's rows and set the task back to
	// pending, or tell the owner when an answer cannot be written.
	ActQuestionAnswered ActionKind = "question_answered"
	// ActWake: an agent's turn ended on an error, or the session has sat
	// waiting at its prompt for too long, and its host is alive and
	// answering. Type the session's resume command. Nothing is restarted.
	ActWake ActionKind = "wake"
	// ActWakeGiveUp: an agent's turn failed again with every wake used. Tell
	// the owner and park the task.
	ActWakeGiveUp ActionKind = "wake_give_up"
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
//
// WakeAt is set on an ActUsagePause that starts a pause, and nowhere else: the
// earliest time the session may be started again, or the zero time when only
// a new usage reading decides. Force is set on an ActUsagePause that repeats
// one: the session was asked to stop and its host is still running.
type Action struct {
	Kind          ActionKind
	Task, Session string
	Reason        string
	Items         []string
	LineStopped   bool
	Resume        ledger.Status
	WakeAt        time.Time
	Force         bool
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
//  3. Running: see decideRunning. Besides watching the session, that is
//     where a session is recovered (its host gone or its agent hung), paused
//     for usage, and woken: its resume command typed when the agent's turn
//     failed or when it has sat waiting at its prompt (PLAN T2.5). A wake
//     never restarts anything, and is never chosen for a parked, paused or
//     shipping task: the first two return earlier inside decideRunning, and
//     a shipping task was answered by rule 2.
//  4. Nothing active and the line is stopped: wait, whatever would be next.
//  5. Otherwise ask nexttask.Next (see decideNext). An owner task is raised
//     as an inbox item, or marked done once that item is answered; a parked
//     task whose items are all answered is un-parked, back to shipping when
//     it was shipping or its session has finished and to running otherwise;
//     a pending task is launched; no task is idle.
//     A task named by an open row of the plan's Open questions table is
//     held before any of that, owner tasks included (see decideNext).
//  6. Nothing active and the project is paused: rule 5's answer stands only
//     when it is about an owner task or an open question; anything else
//     waits.
//  7. Rule 5 chose to launch a task or to un-park one, and the agent is
//     logged out or usage does not allow it: wait instead (see heldBack).
//
// Rule 7 comes last because it holds back only what would start an agent or
// set one going again. An owner task, an idle project and a project with no
// ledger start nothing, so they are answered as before whatever usage says;
// the owner's own task is the thing they can still do while the agent's
// usage is spent. It comes after rule 6 so that a project the owner paused
// keeps saying "paused": `afk resume` is what they must run, and a sentence
// about usage would send them to the wrong place.
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
//
// The two open-question kinds follow the owner kinds in all of this, for the
// same reasons. Asking the owner a question and writing their answer into the
// plan start no agent, so pause lets both through and neither is held back for
// usage or a logged-out agent; a paused project is in fact the best time to
// get its questions answered. A stopped line holds them back with everything
// else.
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
	if o.Paused && a.Kind != ActOwner && a.Kind != ActOwnerDone &&
		a.Kind != ActQuestionHold && a.Kind != ActQuestionAnswered {
		return Action{Kind: ActWait, Reason: "paused; no new task will start until `afk resume`"}
	}
	if h, ok := heldBack(o, a); ok {
		return h
	}
	return a
}

// held is the usage governor's hold on new work: the action that stands in
// for a start, and whether there is one. It is the answer for a launch and
// for creating a session again; heldBack applies it, with two exceptions, to
// an un-park.
//
// The login is checked first: with the agent logged out a launch would fail
// at once, whatever usage says, and logging in is the thing the owner has to
// do. An unknown login holds nothing, and so does usage that was not
// consulted.
//
// The reasons are fixed sentences, one for each governor action, with no
// percentage and no time in them. The loop writes its state file and its log
// when the reason changes, and the governor's own sentence changes with every
// reading; `afk usage` is where the numbers are.
func held(o Observation) (Action, bool) {
	if o.Login.Known && !o.Login.LoggedIn {
		return Action{Kind: ActLoginWait, Reason: "the agent is logged out on this machine; " +
			"nothing new starts until you log it in here"}, true
	}
	if !o.Usage.Consulted || o.Usage.Action == governor.ActionRun {
		return Action{}, false
	}
	var reason string
	switch o.Usage.Action {
	case governor.ActionNoLaunch:
		reason = "usage is close to the limit; nothing new starts until it resets. `afk usage` shows the numbers"
	case governor.ActionPause:
		reason = "usage has reached the pause line; nothing new starts until it resets. `afk usage` shows the numbers"
	case governor.ActionHold:
		reason = "afk has no recent usage reading; nothing new starts. `afk usage` shows why"
	case governor.ActionRefuse:
		reason = "extra usage is switched on for this account; nothing new starts until it is switched off. `afk usage` shows it"
	default:
		// A word this package does not know is not "run", and only "run"
		// may start anything.
		reason = "usage does not allow new work right now; nothing new starts. `afk usage` shows why"
	}
	return Action{Kind: ActUsageWait, Reason: reason}, true
}

// heldBack is rule 7: the held action that replaces a, when a would start an
// agent or set one going again and held applies.
//
// Only a launch and an un-park are ever held. An un-park differs from a
// launch in two ways:
//
//   - Back to shipping, it is never held. It starts no agent, and ship steps
//     are not gated by usage: a task that is merged but not marked done costs
//     nothing to finish and blocks everything after it until it is.
//   - Back to running, it is not held under no_launch. A parked session is a
//     running agent sitting at a prompt, and PLAN §6.3 lets a running session
//     carry on at that level. pause, hold, refuse and a logged-out agent do
//     hold it: the un-park types into the session and sets it spending again.
func heldBack(o Observation, a Action) (Action, bool) {
	switch a.Kind {
	case ActLaunch:
	case ActUnpark:
		if a.Resume == ledger.Shipping {
			return Action{}, false
		}
		loggedOut := o.Login.Known && !o.Login.LoggedIn
		if !loggedOut && o.Usage.Action == governor.ActionNoLaunch {
			return Action{}, false
		}
	default:
		return Action{}, false
	}
	h, ok := held(o)
	if !ok {
		return Action{}, false
	}
	h.Task, h.Session = a.Task, a.Session
	return h, true
}

// Notices says which of the two standing troubles the owner must be told
// about on this tick: extra usage is switched on (the governor says refuse),
// and the agent is logged out. Each is a fact about the machine and not about
// a task, so it is asked beside Decide and not folded into its one Action:
// the executor raises an item for each whatever the tick otherwise does, and
// answers the item itself once the trouble is gone.
//
// There is no "login expires soon" notice. PLAN §6.5 asks for one "if
// detectable", and it is not: the login check gives a yes or a no and no
// date.
func Notices(o Observation) (overage, loggedOut bool) {
	overage = o.Usage.Consulted && o.Usage.Action == governor.ActionRefuse
	loggedOut = o.Login.Known && !o.Login.LoggedIn
	return overage, loggedOut
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
//	a. Its session is not on disk: create it again, or wait when paused,
//	   when the line is stopped, or when the agent is logged out or usage
//	   does not allow new work (see held).
//	b. status.json cannot be read: rules e, u, f and g, then wait. There is
//	   no phase to read, so c and d are skipped, and nothing can be said to
//	   stall.
//	c. Phase done: ship.
//	d. Phase failed: fail.
//	e. An open blocking item for the task or session: park on it.
//	u. Usage (see decideUsage; it is lettered out of order because it was
//	   added between e and f and the rest of this comment names those by
//	   letter). The session is paused for usage: start it again when a new
//	   reading allows, stop it harder when it has not stopped, otherwise
//	   wait. It is not paused and usage has reached the pause line, extra
//	   usage is on, or the session hit a usage limit: pause it.
//	f. The host is not running, past HostGrace, or the agent has stopped
//	   answering for WedgeConfirm: recover the session (see decideRecovery).
//	   That is a relaunch or a restart, a wait for the backoff, or giving up
//	   once MaxRestarts is used. Where the executor cannot relaunch, a gone
//	   host is reported to the owner; where it cannot restart, a wedge is
//	   left alone.
//	g. The session was recovered ContinueAfter ago and is up and answering:
//	   tell it to carry on. A wake whose line could not be typed is owed the
//	   same line and is served here too.
//	w. The agent's last turn failed (see decideWake; lettered out of order
//	   for the reason u is): type the resume command after WakeAfter, wait
//	   out the backoff before a later one, and give up once MaxWakes is used.
//	h. The session is asking something, past StallAfter: type the resume
//	   command once (see stallWake), and report a stall when that has been
//	   done for this wait or may not be done.
//	i. Otherwise wait.
//
// The order carries meaning. A finished session (c, d) is acted on before
// anything about its host, so that a host which is gone by the time its
// session says done or failed is not reported as a dead session, and is not
// started again: the work is over either way, and a relaunch would resume a
// conversation that has nothing left to do. Nothing here assumes the host
// does exit. The ship that follows relies on the opposite for one step only,
// the hand-back of a red CI, which types into the session and so needs its
// host alive after done; when the host is not there that prompt fails and
// the task parks with an item saying how to reach the session.
// Parking (e) comes before recovery and the stall report because a parked
// session sits at a prompt by design, and the owner has already been sent the
// question. That holds for an unreadable status too: the owner being asked is
// a stronger signal than a silent agent, and restarting an agent that is
// waiting for an answer would throw the question away.
// Usage (u) comes after c, d and e and before f, g and h, and each side has
// its reason. A finished session still ships or fails first: its work is over
// and a pause has nothing left to hold back. A blocking item still parks
// first: the owner being asked is the stronger signal, and the pause is kept
// in the ledger for when they answer. It comes before recovery because a
// paused session's host is gone on purpose: recovery would take it for a
// crash and start it again inside the limit it was stopped for, then count
// that against the restart cap and give the task up. And it comes before the
// continue prompt and the stall report because a paused session is not at a
// prompt at all: there is nothing to type into and nobody to report.
// Recovery (f) comes before the continue prompt (g) because a line typed
// into an agent that is not answering goes nowhere, and would be recorded as
// delivered.
// The wake (w) comes after all of those, and its place is what keeps it from
// typing where it must not. A parked session (e) is waiting for the owner; a
// session paused for usage or stopped by its rate-limit marker (u) has
// nothing to gain from a line and would only spend; a gone or wedged host (f)
// needs a relaunch or a restart, not a line; and a line already owed (g) is
// typed there, so that one failed send is never counted as two wakes. A
// shipping task never reaches this function at all. It comes before the stall
// report (h) because a failed turn leaves the session waiting at its prompt,
// which is exactly what a stall looks like, and the owner should hear of it
// only when waking has not worked. It is not in the unreadable-status branch
// (b): with no status there is no telling a session that failed from one that
// is working, and a line typed into a working agent interrupts its turn.
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
		// It is a new launch as far as usage is concerned too: an agent
		// starts, so no_launch holds it as it holds any other start.
		if h, ok := held(o); ok {
			h.Task, h.Session = e.ID, e.Session
			return h
		}
		return act(ActCreate, fmt.Sprintf(
			"%s is running in the ledger but its session %s is not on disk; creating it again", e.ID, e.Session))
	}

	// An unreadable start time is the zero time, which reads as "started
	// long ago": such an entry gets no grace and counts every report as
	// its own. Both lean towards telling the owner once over staying
	// silent or telling them again and again. An unreadable restart time is
	// the zero time as well, which reads as "no recovery to wait for".
	// An unreadable wake time is the zero time too: "never woken", so its
	// backoff reads as served and MaxWakes is what still bounds the entry.
	startedAt, _ := parseTime(e.StartedAt)
	restartAt, _ := parseTime(e.RestartAt)
	wakeAt, _ := parseTime(e.WakeAt)

	continueDue := func() (Action, bool) {
		// At ContinueAfter, not over it: the wait is a margin for the agent
		// to start, and nothing is gained by one more tick.
		if !e.ContinuePending || !o.CanPrompt || !s.HostRunning || !s.WedgedSince.IsZero() ||
			o.Now.Sub(restartAt) < o.ContinueAfter {
			return Action{}, false
		}
		// The line is owed by a wake when the wake is the later of the two:
		// a wake sets the same flag a recovery does, and its line is typed
		// here when the first try at typing it failed.
		if wakeAt.After(restartAt) {
			return act(ActContinue, fmt.Sprintf(
				"the session for %s (%s) is still owed its resume command; typing it", e.ID, e.Session)), true
		}
		return act(ActContinue, fmt.Sprintf(
			"the session for %s (%s) was started again; telling it to carry on", e.ID, e.Session)), true
	}

	if s.Stale {
		// An unreadable status.json is never a failure by itself: the
		// writers replace it all the time and a reader can catch it
		// mid-change. But host liveness comes from host.json, so a dead
		// or wedged host is still seen here, and it must be: a session
		// with no host never repairs its status file, and waiting on it
		// would be waiting for ever. The grace is measured from the
		// entry's start, or from the last recovery when that is later,
		// because the status file's own time is what is missing.
		if a, ok := parkOn(o, e); ok {
			return a
		}
		if a, ok := decideUsage(o, e, s); ok {
			return a
		}
		if a, ok := decideRecovery(o, e, s, latest(startedAt, restartAt)); ok {
			return a
		}
		// A session relaunched with an unreadable status still resumed a
		// conversation and still sits at an empty prompt.
		if a, ok := continueDue(); ok {
			return a
		}
		return act(ActWait, fmt.Sprintf("running %s; its status file cannot be read right now", e.ID))
	}

	switch s.Phase {
	case state.PhaseDone:
		return act(ActShip, fmt.Sprintf("the session for %s finished; handing it to shipping", e.ID))
	case state.PhaseFailed:
		return act(ActFail, fmt.Sprintf("the session for %s ended as failed", e.ID))
	}

	if a, ok := parkOn(o, e); ok {
		return a
	}
	if a, ok := decideUsage(o, e, s); ok {
		return a
	}

	ref := lastActivity(o, e, s, startedAt, restartAt, wakeAt)
	if a, ok := decideRecovery(o, e, s, ref); ok {
		return a
	}
	if a, ok := continueDue(); ok {
		return a
	}
	if a, ok := decideWake(o, e, s); ok {
		return a
	}

	// "Over" the limit, not "at" it: a prompt exactly at the stall limit
	// waits one more tick.
	if s.AwaitingInput && o.Now.Sub(ref) > o.StallAfter {
		if reported(o.Items, e.ID, StallTitlePrefix, startedAt) {
			return act(ActWait, alreadyReported)
		}
		// A failed turn that left no marker looks exactly like this, so the
		// session is sent its resume command once before anyone is paged.
		if woken, whyNot := stallWake(o, e, s); !woken && whyNot == "" {
			return act(ActWake, fmt.Sprintf(
				"the session for %s (%s) has been waiting at the terminal; typing its resume command before telling you",
				e.ID, e.Session))
		}
		return act(ActStall, fmt.Sprintf("the session for %s (%s) is asking something at the terminal", e.ID, e.Session))
	}

	return act(ActWait, fmt.Sprintf("running %s, phase %s", e.ID, s.Phase))
}

// parkOn is rule e: the open blocking items about the entry's task or
// session, as the action that parks on them. It is shared by the readable
// and the unreadable status paths so that the two cannot drift apart.
func parkOn(o Observation, e ledger.Entry) (Action, bool) {
	var blocking []string
	for _, it := range o.Items {
		if it.Open() && it.Blocking && concerns(it, e) {
			blocking = append(blocking, it.ID)
		}
	}
	if len(blocking) == 0 {
		return Action{}, false
	}
	return Action{
		Kind: ActPark, Task: e.ID, Session: e.Session, Items: blocking,
		Reason: fmt.Sprintf("%s is waiting on the owner: inbox %s", e.ID, strings.Join(blocking, ", ")),
	}, true
}

// decideUsage is rule u: what usage means for the one session in flight. The
// second result is false when usage has nothing to say about it, and the
// caller carries on with recovery, the continue prompt and the stall rule.
//
// A pause is the two usage fields of the ledger entry and nothing else (PLAN
// §6.4): the task stays running, its session's agent is stopped through its
// host, and the host ends with it. So "paused" is read from the entry, never
// from the session, and a paused entry never leaves this rule with false:
// every later rule would misread a host that is gone on purpose.
//
// The entry is paused. In this order:
//
//   - Before a readable wake time, with the host gone: wait, whether or not
//     usage was consulted. The executor deliberately does not consult during
//     a known wait (a week-long pause would otherwise probe thousands of
//     times to learn nothing), so this must not depend on a reading.
//   - Resume, when every one of these holds: usage was consulted; the reading
//     was taken after the pause began; the governor says run or no_launch (a
//     session that was running may carry on at no_launch, PLAN §6.3); the
//     wake time has passed, where an empty or unreadable one reads as passed;
//     the login is not known to be out; the executor can relaunch; and the
//     host is not running. The reading's age is the one that looks wrong
//     without its reason: a session stopped by its rate-limit marker is
//     stopped while the last reading still says run, and that same reading
//     would start it again on the next tick, to hit the limit and be stopped
//     again every half minute. A reading taken in the very second of the
//     pause does not count either: the pause is stored to the second, so such
//     a reading cannot be shown to be the later of the two.
//   - The host is still running and the executor can stop it: nothing until
//     PauseRetry after the pause (the polite stop needs a moment), then stop
//     it by force, and from three times PauseRetry tell the owner, because a
//     session that will not stop is still spending. No earlier report is
//     looked up and no count is kept: the item the executor raises is
//     blocking and names the task, so rule e parks the task on the next tick
//     before this rule runs again, exactly as giving up on recovery works. If
//     the owner answers without ending the session, the un-parked task comes
//     back here and is reported again, which is right: it is still spending.
//     Both limits are "at or over", like the recovery backoff: they are waits
//     the loop set itself, served in full when reached.
//   - Otherwise wait, saying what is being waited for.
//
// The entry is not paused. Nothing happens unless usage was consulted: with
// no usage source nothing could ever resume the session, so stopping it
// would be stopping it for good. That holds for the rate-limit marker too.
// Then a pause is due when the governor says pause or refuse (extra usage on
// means a running agent may be spending real money), or the marker is fresh:
// later than both the attempt's start and its last restart. The marker's time
// and the restart time are whole seconds, and a marker in the same second as
// the restart is not after it and is ignored; that leans towards not stopping
// a session that has just been started again, and a session that really is
// at its limit writes another marker. With a pause due but without the means
// to stop and to start again, nothing is done and the caller carries on: the
// agent's own wait at the limit is the backstop.
//
// The wake time of a new pause: the governor's own for its pause, which may
// be the zero time; the zero time for a refuse, which no clock ends; and for
// a marker the earliest reset still ahead, or now plus RateLimitFallback
// when that is unknown or already past. A reset in the past cannot be the
// reset that ends this limit, and using it would resume at once.
//
// Every reason here is fixed words plus, at most, the saved wake time, for
// the reason given at held.
func decideUsage(o Observation, e ledger.Entry, s *SessionView) (Action, bool) {
	act := func(kind ActionKind, reason string) (Action, bool) {
		return Action{Kind: kind, Task: e.ID, Session: e.Session, Reason: reason}, true
	}
	who := fmt.Sprintf("the session for %s (%s)", e.ID, e.Session)

	if e.UsagePauseAt != "" {
		// An unreadable pause time is the zero time, which reads as "paused
		// long ago": any reading is newer than it and every retry is due.
		// An unreadable wake time reads as passed, so a new reading decides.
		pausedAt, _ := parseTime(e.UsagePauseAt)
		wakeAt, wakeKnown := parseTime(e.UsageWakeAt)
		beforeWake := wakeKnown && o.Now.Before(wakeAt)
		until := ""
		if beforeWake {
			until = " until " + wakeText(wakeAt)
		}

		if beforeWake && !s.HostRunning {
			return act(ActUsagePaused, fmt.Sprintf("%s is paused for usage%s", who, until))
		}

		loggedOut := o.Login.Known && !o.Login.LoggedIn
		allowed := o.Usage.Action == governor.ActionRun || o.Usage.Action == governor.ActionNoLaunch
		fresh := o.Usage.At.Truncate(time.Second).After(pausedAt)
		if o.Usage.Consulted && fresh && allowed && !beforeWake && !loggedOut && o.CanRelaunch && !s.HostRunning {
			return act(ActUsageResume, fmt.Sprintf("usage allows work again; starting %s again", who))
		}

		if s.HostRunning {
			if o.CanPause {
				waited := o.Now.Sub(pausedAt)
				if waited >= 3*o.PauseRetry {
					return act(ActUsageStuck, fmt.Sprintf(
						"%s was to be stopped for usage and is still running; it may still be spending", who))
				}
				if waited >= o.PauseRetry {
					a, _ := act(ActUsagePause, fmt.Sprintf(
						"%s was asked to stop for usage and is still running; stopping it", who))
					a.Force = true
					return a, true
				}
			}
			return act(ActUsagePaused, fmt.Sprintf(
				"%s is paused for usage%s; waiting for it to stop", who, until))
		}

		switch {
		case !o.Usage.Consulted:
			return act(ActUsagePaused, fmt.Sprintf(
				"%s is paused for usage, and no usage reading is available to say it may go on. "+
					"`afk usage` shows why; `afk ledger set %s pending` moves the task by hand", who, e.ID))
		case loggedOut:
			return act(ActUsagePaused, fmt.Sprintf(
				"%s is paused for usage, and the agent is logged out on this machine; "+
					"it starts again once you log the agent in here", who))
		case !o.CanRelaunch:
			return act(ActUsagePaused, fmt.Sprintf(
				"%s is paused for usage, and this afk cannot start a session again. "+
					"`afk ledger set %s pending` starts the task over", who, e.ID))
		}
		return act(ActUsagePaused, fmt.Sprintf(
			"%s is paused for usage; it starts again when a new usage reading allows it. `afk usage` shows the numbers", who))
	}

	if !o.Usage.Consulted {
		return Action{}, false
	}
	startedAt, _ := parseTime(e.StartedAt)
	restartAt, _ := parseTime(e.RestartAt)
	marker := !s.RateLimitedAt.IsZero() && s.RateLimitedAt.After(startedAt) && s.RateLimitedAt.After(restartAt)

	var wake time.Time
	var why string
	switch {
	case o.Usage.Action == governor.ActionPause:
		wake, why = o.Usage.WakeAt, "usage reached the pause line"
	case o.Usage.Action == governor.ActionRefuse:
		why = "extra usage is switched on for this account"
	case marker:
		wake, why = o.Now.Add(o.RateLimitFallback), "the session hit a usage limit"
		if o.Usage.NextReset.After(o.Now) {
			wake = o.Usage.NextReset
		}
	default:
		return Action{}, false
	}
	if !o.CanPause || !o.CanRelaunch {
		return Action{}, false
	}
	a, _ := act(ActUsagePause, fmt.Sprintf("%s; pausing %s", why, who))
	a.WakeAt = wake
	return a, true
}

// wakeText is a saved wake time as the owner reads it. It is always UTC and
// says so: the local zone is part of the environment, which Decide does not
// read, and a time with no zone beside it would be read as local.
func wakeText(t time.Time) string {
	return t.UTC().Format("Mon 2 Jan 15:04 UTC")
}

// lastActivity is the last moment anything is known to have happened to this
// attempt: the session wrote its status, the attempt began, the loop last
// recovered or woke the session, or the owner answered one of its items. An answer
// counts because it is the owner acting on the session; without it a task
// would stall again on the very tick its answer un-parked it. A recovery
// counts for the same reason on the other side: a host that was only just
// started again must get its grace again, or a tick that came before the new
// host was seen running would relaunch it a second time. A wake counts so
// that the session gets a whole StallAfter to act on the line it was typed:
// the stall report then comes StallAfter after the wake, and not on the tick
// that follows it.
func lastActivity(o Observation, e ledger.Entry, s *SessionView, startedAt, restartAt, wakeAt time.Time) time.Time {
	ref := latest(latest(latest(s.UpdatedAt, startedAt), restartAt), wakeAt)
	return latest(ref, lastAnswer(o, e))
}

// lastAnswer is when the owner last answered an item about the entry's task
// or session; the zero time when they never have.
func lastAnswer(o Observation, e ledger.Entry) time.Time {
	var at time.Time
	for _, it := range o.Items {
		if !concerns(it, e) {
			continue
		}
		if t, ok := parseTime(it.AnsweredAt); ok {
			at = latest(at, t)
		}
	}
	return at
}

// rateLimitFresh reports whether the session's rate-limit marker is about
// its current run: later than the attempt's start and its last restart. It
// is the same test decideUsage applies to the marker, written out a second
// time here for the two wake rules. The wake rules must read the marker as
// the rule that owns it does: read more loosely, a session would be typed to
// at its limit; more strictly, an old marker would switch the wake off for
// the rest of the attempt.
func rateLimitFresh(e ledger.Entry, s *SessionView) bool {
	startedAt, _ := parseTime(e.StartedAt)
	restartAt, _ := parseTime(e.RestartAt)
	return !s.RateLimitedAt.IsZero() && s.RateLimitedAt.After(startedAt) && s.RateLimitedAt.After(restartAt)
}

// decideWake is rule w: what to do about a session whose agent's last turn
// ended on an error worth trying again (PLAN T2.5). The second result is
// false when the rule does not apply, and the caller carries on with the
// stall rule.
//
// The remedy is to type the session's resume command. Nothing is restarted:
// the host is alive and its agent is answering, it has only stopped working,
// and the conductor acts from the phase it recorded, so the line is safe to
// type again.
//
// The rule applies only when all of these hold, and each is there for a
// reason:
//
//   - The executor can type (CanPrompt). Decide never chooses an action
//     nothing can carry out; the stall rule still tells the owner.
//   - The host is running and has no wedge verdict. A wedge that is not yet
//     confirmed, or that this loop cannot restart, falls through recovery to
//     here, and a line typed into an agent that is not answering goes nowhere
//     and would use up a wake.
//   - No continue prompt is pending. The same line is already owed, and the
//     continue step types it; counting a wake as well would spend two wakes
//     on one failure.
//   - The marker is later than the attempt's start, its last restart and its
//     last wake. Each of those already answered whatever came before it: a
//     new session, a restarted agent and a woken one have all been given the
//     resume command since. Times are whole seconds and "later" is strict, so
//     a marker in the same second as a wake is not woken again; a session
//     that fails again writes a new one.
//   - The marker is later than the owner's last answer to an item about the
//     task. An un-park types the resume command itself, and a marker left
//     over from before the park would otherwise spend a wake of the fresh set
//     on a failure the owner has just dealt with.
//   - No rate-limit marker of this run is as late as it. A rate limit is not
//     a failed turn: typing cannot lift it, and decideUsage is the rule that
//     owns it. Where that rule could not act (no usage source, no means to
//     stop), nothing is typed and the agent's own wait at the limit stands.
//
// Then, in this order: the cap, the first wait, the backoff, the wake. The
// cap comes first, as in decideRecovery, so that a session out of wakes is
// handed to the owner at once and not after one more wait. WakeAfter is
// measured from the marker and is "at or over": it is a wait the loop chose,
// served in full when reached. The backoff is measured from the last wake
// and reuses the recovery schedule (backoffFor over RestartBackoff): none
// before the first wake, the base before the second, doubling after. A later
// wake must have served both waits.
func decideWake(o Observation, e ledger.Entry, s *SessionView) (Action, bool) {
	act := func(kind ActionKind, reason string) (Action, bool) {
		return Action{Kind: kind, Task: e.ID, Session: e.Session, Reason: reason}, true
	}
	if !o.CanPrompt || !s.HostRunning || !s.WedgedSince.IsZero() || e.ContinuePending {
		return Action{}, false
	}
	startedAt, _ := parseTime(e.StartedAt)
	restartAt, _ := parseTime(e.RestartAt)
	wakeAt, _ := parseTime(e.WakeAt)
	failedAt := s.TurnFailedAt
	if failedAt.IsZero() || !failedAt.After(startedAt) || !failedAt.After(restartAt) ||
		!failedAt.After(wakeAt) || !failedAt.After(lastAnswer(o, e)) {
		return Action{}, false
	}
	if rateLimitFresh(e, s) && !failedAt.After(s.RateLimitedAt) {
		return Action{}, false
	}

	who := fmt.Sprintf("the session for %s (%s)", e.ID, e.Session)
	if e.Wakes >= o.MaxWakes {
		if o.MaxWakes <= 0 {
			return act(ActWakeGiveUp, fmt.Sprintf(
				"the agent's turn failed in %s, and afk is set not to wake sessions", who))
		}
		return act(ActWakeGiveUp, fmt.Sprintf(
			"the agent's turn failed again in %s after %s; giving up on it", who, wakeCount(e.Wakes)))
	}
	if o.Now.Sub(failedAt) < o.WakeAfter {
		return act(ActWait, fmt.Sprintf(
			"the agent's turn failed in %s; waiting a moment before typing its resume command", who))
	}
	if e.Wakes >= 1 && o.Now.Sub(wakeAt) < backoffFor(e.Wakes, o.RestartBackoff) {
		return act(ActWait, fmt.Sprintf(
			"the agent's turn failed again in %s; waiting before waking it again (%s so far)", who, wakeCount(e.Wakes)))
	}
	return act(ActWake, fmt.Sprintf("the agent's turn failed in %s; typing its resume command", who))
}

// stallWake answers the two questions the stall rule asks about a session
// that has waited past StallAfter: has it already been sent its resume
// command for this wait, and if not, why may it not be sent now. The session
// is to be woken exactly when both answers are empty: woken false and whyNot
// "". The executor asks the same function when it writes the stall item, so
// what the owner is told about the wake cannot differ from what was decided.
//
// A pending continue prompt is checked first, and it means not woken: the
// line of a wake (or a recovery) is still owed, so nothing has reached the
// session, whatever wake_at says. Nothing more is typed either: the continue
// step owns that line.
//
// Woken is "wake_at is later than the session's last status write". No field
// records a stall wake as such, and none is needed: a wake of either kind,
// for a marker or for a stall, typed the resume command, and a session that
// has written nothing since is the session that line did not move. A session
// that did write its status after a wake and then sat waiting again is on a
// new wait, and is woken for it, while it has wakes left.
//
// The reasons for not waking, in order: nothing can type; MaxWakes is zero,
// the setting for never waking; the wakes are used up; the session hit a
// usage limit in this run, which a typed line cannot lift (a rate limit is
// never treated as a failed turn). Each is a clause that follows "afk did
// not wake it by typing its resume command:" in the stall item.
func stallWake(o Observation, e ledger.Entry, s *SessionView) (woken bool, whyNot string) {
	if e.ContinuePending {
		return false, "an earlier try at typing it has not got through"
	}
	if wakeAt, ok := parseTime(e.WakeAt); ok && wakeAt.After(s.UpdatedAt) {
		return true, ""
	}
	switch {
	case !o.CanPrompt:
		return false, "this afk has no means to type into a session"
	case o.MaxWakes <= 0:
		return false, "afk is set not to wake sessions (`max_wakes = 0` in afk's config.toml)"
	case e.Wakes >= o.MaxWakes:
		return false, fmt.Sprintf("afk has already woken this session %s, which is the limit", wakeCount(e.Wakes))
	case rateLimitFresh(e, s):
		return false, "the session hit a usage limit, which typing cannot lift"
	}
	return false, ""
}

// wakeCount is "1 time" or "N times", for a sentence shown to the owner.
func wakeCount(n int) string {
	if n == 1 {
		return "1 time"
	}
	return fmt.Sprintf("%d times", n)
}

// decideRecovery is rule f: what to do about a session that has stopped. The
// second result is false when there is nothing to recover, or nothing this
// rule may do about it, and the caller carries on with its later rules.
//
// Two things count as stopped. Gone: the host is not running and has not
// been for longer than HostGrace, measured from graceFrom (the caller's last
// sign of life; "over" the grace, not "at" it, as before). Wedged: the host
// is running and has published a wedge verdict.
//
// A gone host with no means to relaunch is reported to the owner exactly as
// it was before recovery existed, once per attempt. A wedge with no means to
// restart is left alone: nobody was told about wedges before, and an inbox
// item the owner can do nothing with from their phone is worse than none.
//
// A wedge must have stood for WedgeConfirm before anything is done. The
// host's verdict is advisory and clears on any byte of output, and a restart
// stops the agent in the middle of whatever it was doing, so a verdict that
// is about to clear must not cost a working agent its turn. A gone host needs
// no such wait: HostGrace has already been one.
//
// Then, in this order: the cap, the backoff, the action. The cap comes first
// so that a session out of restarts is given up on at once and not after one
// more wait. The backoff is "at or over", unlike the grace and the stall
// limit, which are "over": those two guard against acting too soon on a
// reading that may still change, so the boundary leans towards waiting, while
// the backoff is a wait the loop chose for itself and has fully served when
// it is reached. An unreadable restart time is the zero time, so its backoff
// reads as served; the cap is what still bounds such an entry.
func decideRecovery(o Observation, e ledger.Entry, s *SessionView, graceFrom time.Time) (Action, bool) {
	act := func(kind ActionKind, reason string) (Action, bool) {
		return Action{Kind: kind, Task: e.ID, Session: e.Session, Reason: reason}, true
	}

	gone := !s.HostRunning && o.Now.Sub(graceFrom) > o.HostGrace
	wedged := s.HostRunning && !s.WedgedSince.IsZero()
	switch {
	case !gone && !wedged:
		return Action{}, false
	case gone && !o.CanRelaunch:
		startedAt, _ := parseTime(e.StartedAt)
		if reported(o.Items, e.ID, HostGoneTitlePrefix, startedAt) {
			return act(ActWait, alreadyReported)
		}
		return act(ActHostGone, fmt.Sprintf("the session for %s (%s) has stopped running", e.ID, e.Session))
	case wedged && !o.CanRestart:
		return Action{}, false
	case wedged && o.Now.Sub(s.WedgedSince) < o.WedgeConfirm:
		return act(ActWait, fmt.Sprintf(
			"the session for %s (%s) has stopped answering; waiting to be sure before restarting it", e.ID, e.Session))
	}

	what := "has stopped answering"
	if gone {
		what = "has stopped running"
	}
	if e.Restarts >= o.MaxRestarts {
		if e.Restarts == 0 {
			return act(ActGiveUp, fmt.Sprintf(
				"the session for %s (%s) %s, and afk is set not to restart sessions", e.ID, e.Session, what))
		}
		return act(ActGiveUp, fmt.Sprintf(
			"the session for %s (%s) %s again after %s; giving up on it", e.ID, e.Session, what, restartCount(e.Restarts)))
	}
	restartAt, _ := parseTime(e.RestartAt)
	if o.Now.Sub(restartAt) < backoffFor(e.Restarts, o.RestartBackoff) {
		return act(ActWait, fmt.Sprintf(
			"the session for %s (%s) %s again; waiting before trying to recover it again (%s so far)",
			e.ID, e.Session, what, restartCount(e.Restarts)))
	}
	if gone {
		return act(ActRelaunch, fmt.Sprintf("the session for %s (%s) has stopped running; starting it again", e.ID, e.Session))
	}
	return act(ActRestart, fmt.Sprintf("the session for %s (%s) has stopped answering; restarting it", e.ID, e.Session))
}

// restartCount is "1 restart" or "N restarts", for a reason shown to the
// owner.
func restartCount(n int) string {
	if n == 1 {
		return "1 restart"
	}
	return fmt.Sprintf("%d restarts", n)
}

// backoffShiftCap is the most doublings backoffFor applies. Sixteen turns a
// one-minute base into about forty-five days, far past any wait that would
// ever be served, and keeps the shift itself from growing without bound for
// a count read from a hand-edited ledger.
const backoffShiftCap = 16

// backoffFor is the wait before the next recovery of an attempt that has
// already been recovered restarts times: none before the first, base before
// the second, and twice the wait before for each one after that. With the
// defaults (PLAN §5.6) that is at once, after one minute, after two minutes.
//
// It cannot overflow. The doublings are capped, and a base large enough to
// overflow inside the cap gives the longest wait there is, where a wrapped
// shift would give a negative one, which reads as no wait at all.
func backoffFor(restarts int, base time.Duration) time.Duration {
	if restarts <= 0 || base <= 0 {
		return 0
	}
	shift := min(restarts-1, backoffShiftCap)
	if base > math.MaxInt64>>shift {
		return math.MaxInt64
	}
	return base << shift
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
//
// Open questions (PLAN T5.6) are checked first, for whichever task nexttask
// named, in three steps:
//
//   - The task is parked by the hold itself (isQuestionHold) and nexttask
//     chose it, so every item it waited on is answered: ActQuestionAnswered.
//     This comes before the owner branch and before the un-park, and it has
//     to. A held task has no session and was never started. Read as an
//     owner's parked task it would be marked done with nothing done; read as
//     an agent's it would be sent to running, and the next pass would create
//     a session for a task whose row may still be open.
//   - The task is not parked and the plan's questions could not be read:
//     wait. Starting it would be starting it blind, and the hold is the one
//     rule here that exists to stop exactly that.
//   - The task is not parked and open rows name it: ActQuestionHold. An
//     owner's task is held the same way, ahead of ActOwner, because the
//     owner should get the question before they are handed the task that
//     needs its answer.
//
// A row holds its task for as long as it is open in the plan, whatever the
// inbox says. An answered item only stops a second item being raised; the
// agent reads the answer from the plan, so the task must not start until the
// answer is there. A task that is parked for any other reason is not looked
// at for questions: its session is already running, and stopping work that is
// under way is not what the hold is for.
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
		switch {
		case isParked && isQuestionHold(e, o.Items):
			return Action{Kind: ActQuestionAnswered, Task: d.Task, Reason: fmt.Sprintf(
				"every question %s was waiting on is answered; putting the answers in the plan", d.Task)}
		case !isParked && o.QuestionsErr != nil:
			return Action{Kind: ActWait, Task: d.Task, Reason: fmt.Sprintf(
				"the plan's open questions could not be read (%v); %s is not started until they can be", o.QuestionsErr, d.Task)}
		case !isParked:
			if rows := holdingRows(o.Questions, d.Task); len(rows) > 0 {
				// Items names the items that are already there, each once,
				// so the state shows what the owner has been asked so far.
				var have []string
				for _, q := range rows {
					if it, ok := questionItem(o.Items, q); ok && !slices.Contains(have, it.ID) {
						have = append(have, it.ID)
					}
				}
				return Action{Kind: ActQuestionHold, Task: d.Task, Items: have, Reason: holdReason(d.Task, len(rows))}
			}
		}
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
