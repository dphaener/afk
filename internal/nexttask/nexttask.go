// Package nexttask decides which master-plan task runs next (docs/PLAN.md
// §5.2). It is one pure function: parsed tasks, the ledger and "is this inbox
// item still open" go in, and one task id or the reason there is none comes
// out.
//
// It does no I/O and reads no clock on purpose. The daemon asks this question
// on every tick, and a wrong answer starts a task before the work it builds
// on has landed, or runs a second task beside one already in flight. Keeping
// the decision free of the filesystem is what lets every rule be a row in one
// table test, with no project on disk.
//
// A ready fix task (docs/PLAN.md §2 "Bug fixes") is chosen ahead of other
// ready tasks, but after started work and an answered parked task.
//
// The rules lean towards not starting. Anything this package cannot show to
// be finished (a superseded task with no replacements, a replacement the plan
// does not know, replacements that name each other in a loop) counts as
// unfinished, so the worst outcome of bad data is a build that stops and says
// why, never one that runs ahead.
package nexttask

import (
	"slices"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
)

// Reason says why Next chose no task. ReasonNone means it chose one.
type Reason string

const (
	// ReasonNone is the empty reason: Decision.Task names a task to run.
	ReasonNone Reason = ""
	// ReasonInProgress means a task is already running or shipping. One task
	// runs at a time, so nothing else starts until it ends.
	ReasonInProgress Reason = "in progress"
	// ReasonAllDone means every task in the plan is done or superseded.
	ReasonAllDone Reason = "all done"
	// ReasonWaitingOnOwner means the build cannot move until the owner
	// answers the inbox items a parked task waits on.
	ReasonWaitingOnOwner Reason = "waiting on owner"
	// ReasonBlockedByFailed means what is left depends on a failed task.
	ReasonBlockedByFailed Reason = "blocked by failed"
	// ReasonStuck means tasks are still pending, none can start, and nothing
	// is running, parked or failed to explain it. It takes ledger data that
	// can never be satisfied, such as a superseded task with no replacements.
	ReasonStuck Reason = "stuck"
)

// Decision is Next's answer. Either Task is set and Reason is ReasonNone, or
// Task is empty and Reason says why. The lists carry what a caller needs to
// explain the reason to the owner; a list that does not apply is nil.
type Decision struct {
	// Task is the id of the task to run, or empty.
	Task   string
	Reason Reason
	// Owner is true when Task names a task whose title marks it as the
	// owner's (masterplan.Task.Owner). Next still chooses it exactly where
	// it would choose an ordinary task; the daemon decides what to do with
	// one, because only the daemon knows an agent must not be launched.
	Owner bool
	// InProgress holds the running and shipping tasks, in plan order. Set
	// only with ReasonInProgress.
	InProgress []string
	// Parked holds the parked tasks, in plan order. Set with
	// ReasonWaitingOnOwner.
	Parked []string
	// WaitingOn holds the open inbox item ids the parked tasks wait on,
	// sorted, each once. It is empty when the only parked tasks name no item.
	WaitingOn []string
	// Failed holds the failed tasks, in plan order. Set with
	// ReasonBlockedByFailed, and with ReasonWaitingOnOwner when there are
	// failed tasks as well, so the caller can show both.
	Failed []string
	// Stuck holds the pending tasks, in plan order. Set only with
	// ReasonStuck.
	Stuck []string
}

// entries is the ledger keyed by task id. A task with no key is pending.
type entries map[string]ledger.Entry

// index keys the ledger's entries by id, keeping only the ids keep allows.
// The first entry for an id wins, as it does in ledger.Ledger.Get; a loaded
// ledger never repeats an id, but a ledger built in memory could.
func index(l ledger.Ledger, keep func(id string) bool) entries {
	m := make(entries, len(l.Tasks))
	for _, e := range l.Tasks {
		if _, dup := m[e.ID]; dup || !keep(e.ID) {
			continue
		}
		m[e.ID] = e
	}
	return m
}

// status is the task's status, pending when it has no entry (PLAN §4.2).
func (m entries) status(id string) ledger.Status {
	if e, ok := m[id]; ok {
		return e.Status
	}
	return ledger.Pending
}

// satisfied reports whether a task depending on id may start.
//
// A done task satisfies. A superseded task stands for the tasks that replaced
// it, so it satisfies only when it names at least one replacement and every
// one of them satisfies in turn; a replacement may itself be superseded.
// Every other status does not satisfy.
//
// onPath holds the superseded ids currently being followed. Meeting one again
// means the replacements name each other in a loop, which nothing can ever
// finish, so the answer is no. Without the check a hand-edited ledger with
// such a loop would recurse until the stack ran out. Ids are removed on the
// way back up so that two replacements sharing a further replacement are each
// judged on their own, not mistaken for a loop.
func (m entries) satisfied(id string, onPath map[string]bool) bool {
	e, ok := m[id]
	if !ok {
		return false
	}
	switch e.Status {
	case ledger.Done:
		return true
	case ledger.Superseded:
		if len(e.ReplacedBy) == 0 || onPath[id] {
			return false
		}
		onPath[id] = true
		defer delete(onPath, id)
		for _, r := range e.ReplacedBy {
			if !m.satisfied(r, onPath) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// unmet returns the task's dependencies that are not satisfied, in the order
// the plan lists them, or nil when there are none.
func (m entries) unmet(t masterplan.Task) []string {
	var out []string
	for _, dep := range t.DependsOn {
		if !m.satisfied(dep, map[string]bool{}) {
			out = append(out, dep)
		}
	}
	return out
}

// Unmet returns the task's dependencies that are not yet satisfied, in the
// order the plan lists them, or nil when the task has none left. It applies
// the same test Next does, so a listing built on it agrees with what Next
// would start.
//
// Unmet is given no task list, so unlike Next it cannot leave out ledger
// entries for ids the plan no longer has. The two differ only when a
// superseded task names a replacement that is recorded done but is not in the
// plan: Next counts that dependency as unmet and Unmet does not.
func Unmet(task masterplan.Task, l ledger.Ledger) []string {
	return index(l, func(string) bool { return true }).unmet(task)
}

// Next picks the task to run, or says why there is none. tasks must already
// have passed masterplan.Validate: Next does not look for unknown
// dependencies or cycles, and plan order (the order of tasks) is what breaks
// every tie.
//
// openItems reports whether an inbox item is still waiting for the owner. A
// nil openItems means every item is open, which is what a caller with no
// inbox passes: no parked task is released.
//
// Ledger entries for ids that are not in tasks are ignored, including as
// replacements: a task the plan no longer has cannot hold the build up as
// "running", and cannot finish a dependency either.
//
// The rules, first match wins:
//
//  1. A task is running or shipping: no task, ReasonInProgress.
//  2. The first parked task that names inbox items, none of them still open,
//     is chosen. It goes ahead of pending tasks because it is work already
//     started that only the owner was holding up. Its dependencies are not
//     checked again; they were met when it first ran. A parked task naming no
//     items is never chosen here, because there is nothing whose answering
//     could release it; only a change to the ledger can.
//  3. The first pending fix task (masterplan.Task.Fix) with every dependency
//     satisfied is chosen, ahead of every other ready task. Open questions
//     hold a chosen fix task later, in the daemon, as for any task.
//  4. The first pending task with every dependency satisfied is chosen.
//  5. Every task is done or superseded: ReasonAllDone.
//  6. A task is parked: ReasonWaitingOnOwner.
//  7. A task is failed: ReasonBlockedByFailed.
//  8. Otherwise ReasonStuck.
//
// A plan with no tasks is ReasonAllDone.
func Next(tasks []masterplan.Task, l ledger.Ledger, openItems func(id string) bool) Decision {
	d := choose(tasks, l, openItems)
	if d.Task != "" {
		for _, t := range tasks {
			if t.ID == d.Task {
				d.Owner = t.Owner()
				break
			}
		}
	}
	return d
}

// choose is the ordering rules of Next, before Owner is filled in.
func choose(tasks []masterplan.Task, l ledger.Ledger, openItems func(id string) bool) Decision {
	inPlan := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		inPlan[t.ID] = true
	}
	m := index(l, func(id string) bool { return inPlan[id] })
	open := func(id string) bool { return openItems == nil || openItems(id) }

	var inProgress, parked, failed, pending []string
	var answered, ready, readyFix string
	var waitingOn []string
	finished := 0
	for _, t := range tasks {
		switch m.status(t.ID) {
		case ledger.Running, ledger.Shipping:
			inProgress = append(inProgress, t.ID)
		case ledger.Parked:
			parked = append(parked, t.ID)
			items := m[t.ID].ParkedOn
			stillOpen := 0
			for _, item := range items {
				if open(item) {
					stillOpen++
					waitingOn = append(waitingOn, item)
				}
			}
			if answered == "" && len(items) > 0 && stillOpen == 0 {
				answered = t.ID
			}
		case ledger.Failed:
			failed = append(failed, t.ID)
		case ledger.Pending:
			pending = append(pending, t.ID)
			if ready == "" && len(m.unmet(t)) == 0 {
				ready = t.ID
			}
			if readyFix == "" && t.Fix() && len(m.unmet(t)) == 0 {
				readyFix = t.ID
			}
		case ledger.Done, ledger.Superseded:
			finished++
		}
	}

	switch {
	case len(inProgress) > 0:
		return Decision{Reason: ReasonInProgress, InProgress: inProgress}
	case answered != "":
		return Decision{Task: answered}
	// A ready fix task goes ahead of plan order but behind started work:
	// a bug the owner reported should not wait behind the queue, yet it must
	// not cut in front of a task already begun or one the owner just answered.
	case readyFix != "":
		return Decision{Task: readyFix}
	case ready != "":
		return Decision{Task: ready}
	// Counted, not inferred from the other lists being empty: a status this
	// package does not know (possible only in a ledger built in memory, since
	// ledger.Load rejects one) must not read as finished.
	case finished == len(tasks):
		return Decision{Reason: ReasonAllDone}
	case len(parked) > 0:
		slices.Sort(waitingOn)
		return Decision{
			Reason:    ReasonWaitingOnOwner,
			Parked:    parked,
			WaitingOn: slices.Compact(waitingOn),
			Failed:    failed,
		}
	case len(failed) > 0:
		return Decision{Reason: ReasonBlockedByFailed, Failed: failed}
	default:
		return Decision{Reason: ReasonStuck, Stuck: pending}
	}
}
