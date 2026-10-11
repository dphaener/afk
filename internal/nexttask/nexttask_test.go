package nexttask

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
)

// task builds a plan task with the given dependencies. Line is left at zero:
// nothing in this package reads it.
func task(id string, deps ...string) masterplan.Task {
	return masterplan.Task{ID: id, Title: "Task " + id, DependsOn: deps}
}

// entry is a ledger entry with only a status.
func entry(id string, st ledger.Status) ledger.Entry {
	return ledger.Entry{ID: id, Status: st}
}

// parked is a ledger entry parked on the given inbox items.
func parked(id string, items ...string) ledger.Entry {
	return ledger.Entry{ID: id, Status: ledger.Parked, ParkedOn: items}
}

// superseded is a ledger entry replaced by the given tasks.
func superseded(id string, by ...string) ledger.Entry {
	return ledger.Entry{ID: id, Status: ledger.Superseded, ReplacedBy: by}
}

// openOnly reports the listed inbox items as open and every other as answered.
func openOnly(items ...string) func(string) bool {
	return func(id string) bool { return slices.Contains(items, id) }
}

// nebariSplit is the shape of nebari's split task: T4.2 is kept in the plan
// as history, T4.2a and T4.2b replaced it, and T6.1 still names T4.2. T6.1 is
// listed ahead of the replacements here (it is not in nebari) so that plan
// order alone would pick it: the rows using this plan pass only if T6.1 is
// really held back by its dependency.
func nebariSplit() []masterplan.Task {
	return []masterplan.Task{
		task("T4.2"),
		task("T6.1", "T4.2"),
		task("T4.2a"),
		task("T4.2b"),
	}
}

// TestNext is the whole of PLAN §5.2 as one table. A zero openItems (nil)
// means every inbox item is open.
//
// The load-bearing rows:
//
//   - "running blocks ready tasks" and "shipping blocks ready tasks": one task
//     runs at a time. Without rule 1 the daemon would start a second session
//     beside the one in flight.
//   - "superseded dependency waits while one replacement is pending": T6.1 is
//     first in plan order and must still not be chosen. A dependency read as
//     met because its task is "finished with" would start T6.1 before T4.2b
//     has landed.
//   - "superseded with no replacements never satisfies" and "replacements
//     naming each other never satisfy": bad ledger data has to stop the build
//     (ReasonStuck), not release a task, and the loop row would not return at
//     all without the on-path check.
//   - "parked with every item answered goes before an earlier ready task" and
//     "parked with one item still open is skipped": together they pin rule 2
//     to "all answered", and its place ahead of rule 3.
//   - "parked with no items stays parked" and "nil openItems leaves a parked
//     task parked": a parked task must never be released by default, which is
//     what the commands rely on until there is an inbox to ask.
//   - "ledger entry for an id not in the plan is ignored": a leftover
//     "running" entry for a task deleted from the plan would otherwise hold
//     the build at "in progress" for ever.
//   - "failed dependency blocks its dependant, unrelated task still chosen":
//     a failure stops only what depends on it.
//   - "task added between two reads": the plan is read fresh each time, so a
//     task added to it must be picked up with no change to the ledger.
func TestNext(t *testing.T) {
	chain := []masterplan.Task{
		task("T1.1"),
		task("T1.2", "T1.1"),
		task("T1.3", "T1.2"),
	}
	independent := []masterplan.Task{task("T1.1"), task("T1.2"), task("T1.3")}

	cases := []struct {
		name      string
		tasks     []masterplan.Task
		ledger    ledger.Ledger
		openItems func(string) bool
		want      Decision
		// thenTasks, when set, is a second read of the plan: Next is called
		// again with the same ledger and must return thenWant.
		thenTasks []masterplan.Task
		thenWant  Decision
	}{
		{
			name:  "empty ledger picks the first task",
			tasks: chain,
			want:  Decision{Task: "T1.1"},
		},
		{
			name:   "plan order wins among several ready tasks",
			tasks:  []masterplan.Task{task("T1.1"), task("T2.2"), task("T2.1"), task("T1.9")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{entry("T1.1", ledger.Done)}},
			// T2.2 is listed before T2.1 and T1.9: the plan's order decides,
			// not the ids' order.
			want: Decision{Task: "T2.2"},
		},
		{
			name:  "a done chain picks the next",
			tasks: chain,
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.1", ledger.Done),
				entry("T1.2", ledger.Done),
			}},
			want: Decision{Task: "T1.3"},
		},
		{
			name:   "an explicit pending entry is the same as no entry",
			tasks:  chain,
			ledger: ledger.Ledger{Tasks: []ledger.Entry{entry("T1.1", ledger.Pending)}},
			want:   Decision{Task: "T1.1"},
		},
		{
			name:  "a pending dependency is not satisfied",
			tasks: []masterplan.Task{task("T1.2", "T1.1"), task("T1.1")},
			// T1.2 is first in plan order but waits for T1.1.
			want: Decision{Task: "T1.1"},
		},
		{
			name:      "parked with every item answered goes before an earlier ready task",
			tasks:     independent,
			ledger:    ledger.Ledger{Tasks: []ledger.Entry{parked("T1.2", "q1", "q2")}},
			openItems: openOnly(),
			want:      Decision{Task: "T1.2"},
		},
		{
			name:  "the first answered parked task in plan order goes first",
			tasks: independent,
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				parked("T1.3", "q3"),
				parked("T1.2", "q2"),
			}},
			openItems: openOnly(),
			want:      Decision{Task: "T1.2"},
		},
		{
			name:      "parked with one item still open is skipped and a later ready task is chosen",
			tasks:     independent,
			ledger:    ledger.Ledger{Tasks: []ledger.Entry{parked("T1.1", "q1", "q2")}},
			openItems: openOnly("q2"),
			want:      Decision{Task: "T1.2"},
		},
		{
			name:      "parked with no items stays parked",
			tasks:     []masterplan.Task{task("T1.1"), task("T1.2", "T1.1")},
			ledger:    ledger.Ledger{Tasks: []ledger.Entry{parked("T1.1")}},
			openItems: openOnly(),
			want:      Decision{Reason: ReasonWaitingOnOwner, Parked: []string{"T1.1"}},
		},
		{
			name:   "nil openItems leaves a parked task parked",
			tasks:  []masterplan.Task{task("T1.1"), task("T1.2", "T1.1")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{parked("T1.1", "q1")}},
			want: Decision{
				Reason:    ReasonWaitingOnOwner,
				Parked:    []string{"T1.1"},
				WaitingOn: []string{"q1"},
			},
		},
		{
			name:   "a parked dependency is not satisfied, unrelated task still chosen",
			tasks:  []masterplan.Task{task("T1.1"), task("T1.2", "T1.1"), task("T1.3")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{parked("T1.1", "q1")}},
			want:   Decision{Task: "T1.3"},
		},
		{
			name:   "failed dependency blocks its dependant, unrelated task still chosen",
			tasks:  []masterplan.Task{task("T1.1"), task("T1.2", "T1.1"), task("T1.3")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{entry("T1.1", ledger.Failed)}},
			want:   Decision{Task: "T1.3"},
		},
		{
			name: "only blocked tasks left gives blocked by failed",
			tasks: []masterplan.Task{
				task("T1.1"),
				task("T1.2", "T1.1"),
				task("T1.3"),
				task("T1.4", "T1.3"),
				task("T1.5"),
			},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.3", ledger.Failed),
				entry("T1.1", ledger.Failed),
				entry("T1.5", ledger.Done),
			}},
			// Plan order, not the ledger's order.
			want: Decision{Reason: ReasonBlockedByFailed, Failed: []string{"T1.1", "T1.3"}},
		},
		{
			name:   "a failed task with nothing depending on it still blocks all done",
			tasks:  []masterplan.Task{task("T1.1"), task("T1.2")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{entry("T1.1", ledger.Done), entry("T1.2", ledger.Failed)}},
			want:   Decision{Reason: ReasonBlockedByFailed, Failed: []string{"T1.2"}},
		},
		{
			name: "only parked left gives waiting on owner with item ids",
			tasks: []masterplan.Task{
				task("T1.1"),
				task("T1.2"),
				task("T1.3", "T1.1", "T1.2"),
			},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				parked("T1.2", "q9", "q3"),
				parked("T1.1", "q3", "q1", "q5"),
			}},
			// q5 is answered, so it is not listed; q3 is waited on by both
			// tasks and listed once; the ids are sorted.
			openItems: openOnly("q1", "q3", "q9"),
			want: Decision{
				Reason:    ReasonWaitingOnOwner,
				Parked:    []string{"T1.1", "T1.2"},
				WaitingOn: []string{"q1", "q3", "q9"},
			},
		},
		{
			name: "parked and failed together report both",
			tasks: []masterplan.Task{
				task("T1.1"),
				task("T1.2"),
				task("T1.3", "T1.1", "T1.2"),
			},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.1", ledger.Failed),
				parked("T1.2", "q1"),
			}},
			want: Decision{
				Reason:    ReasonWaitingOnOwner,
				Parked:    []string{"T1.2"},
				WaitingOn: []string{"q1"},
				Failed:    []string{"T1.1"},
			},
		},
		{
			name:  "superseded dependency waits while one replacement is pending",
			tasks: nebariSplit(),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a", "T4.2b"),
				entry("T4.2a", ledger.Done),
			}},
			want: Decision{Task: "T4.2b"},
		},
		{
			name:  "superseded dependency is satisfied when both replacements are done",
			tasks: nebariSplit(),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a", "T4.2b"),
				entry("T4.2a", ledger.Done),
				entry("T4.2b", ledger.Done),
			}},
			want: Decision{Task: "T6.1"},
		},
		{
			name:   "superseded with no replacements never satisfies",
			tasks:  []masterplan.Task{task("T4.2"), task("T6.1", "T4.2")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{superseded("T4.2")}},
			want:   Decision{Reason: ReasonStuck, Stuck: []string{"T6.1"}},
		},
		{
			name: "a replacement that is itself superseded is followed",
			tasks: []masterplan.Task{
				task("T4.2"),
				task("T6.1", "T4.2"),
				task("T4.2a"),
				task("T4.3"),
			},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a"),
				superseded("T4.2a", "T4.3"),
				entry("T4.3", ledger.Done),
			}},
			want: Decision{Task: "T6.1"},
		},
		{
			name: "a replacement that is itself superseded waits for its own replacement",
			tasks: []masterplan.Task{
				task("T4.2"),
				task("T6.1", "T4.2"),
				task("T4.2a"),
				task("T4.3"),
			},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a"),
				superseded("T4.2a", "T4.3"),
			}},
			want: Decision{Task: "T4.3"},
		},
		{
			name: "two replacements sharing a further replacement are not a loop",
			tasks: []masterplan.Task{
				task("T4.2"),
				task("T6.1", "T4.2"),
				task("T4.2a"),
				task("T4.2b"),
				task("T4.3"),
			},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a", "T4.2b"),
				superseded("T4.2a", "T4.3"),
				superseded("T4.2b", "T4.3"),
				entry("T4.3", ledger.Done),
			}},
			want: Decision{Task: "T6.1"},
		},
		{
			name: "replacements naming each other never satisfy",
			tasks: []masterplan.Task{
				task("T4.2"),
				task("T6.1", "T4.2"),
				task("T4.2a"),
			},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a"),
				superseded("T4.2a", "T4.2"),
			}},
			want: Decision{Reason: ReasonStuck, Stuck: []string{"T6.1"}},
		},
		{
			name:  "a replacement that is not in the plan never satisfies",
			tasks: []masterplan.Task{task("T4.2"), task("T6.1", "T4.2")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a"),
				entry("T4.2a", ledger.Done),
			}},
			want: Decision{Reason: ReasonStuck, Stuck: []string{"T6.1"}},
		},
		{
			name:  "a superseded task is never chosen",
			tasks: []masterplan.Task{task("T4.2"), task("T4.2a")},
			// T4.2 is first, has no dependencies and is not done, and its
			// replacement is not done either. It is still not a task to run.
			ledger: ledger.Ledger{Tasks: []ledger.Entry{superseded("T4.2", "T4.2a")}},
			want:   Decision{Task: "T4.2a"},
		},
		{
			name:  "running blocks ready tasks",
			tasks: independent,
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.2", ledger.Running),
			}},
			want: Decision{Reason: ReasonInProgress, InProgress: []string{"T1.2"}},
		},
		{
			name:  "shipping blocks ready tasks",
			tasks: independent,
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.1", ledger.Shipping),
			}},
			want: Decision{Reason: ReasonInProgress, InProgress: []string{"T1.1"}},
		},
		{
			name:  "in progress goes before an answered parked task and lists plan order",
			tasks: independent,
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.3", ledger.Shipping),
				parked("T1.2", "q1"),
				entry("T1.1", ledger.Running),
			}},
			openItems: openOnly(),
			want:      Decision{Reason: ReasonInProgress, InProgress: []string{"T1.1", "T1.3"}},
		},
		{
			name:  "all done",
			tasks: chain,
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.1", ledger.Done),
				entry("T1.2", ledger.Done),
				entry("T1.3", ledger.Done),
			}},
			want: Decision{Reason: ReasonAllDone},
		},
		{
			name:  "all done counts superseded tasks",
			tasks: nebariSplit(),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a", "T4.2b"),
				entry("T4.2a", ledger.Done),
				entry("T4.2b", ledger.Done),
				entry("T6.1", ledger.Done),
			}},
			want: Decision{Reason: ReasonAllDone},
		},
		{
			name:  "ledger entry for an id not in the plan is ignored",
			tasks: []masterplan.Task{task("T1.1"), task("T1.2", "T1.1")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T9.9", ledger.Running),
				entry("T9.8", ledger.Failed),
				parked("T9.7", "q1"),
			}},
			openItems: openOnly(),
			want:      Decision{Task: "T1.1"},
		},
		{
			name:  "entries not in the plan do not stop all done",
			tasks: []masterplan.Task{task("T1.1")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.1", ledger.Done),
				entry("T9.8", ledger.Failed),
				parked("T9.7", "q1"),
			}},
			want: Decision{Reason: ReasonAllDone},
		},
		{
			name: "stuck lists every pending task in plan order",
			tasks: []masterplan.Task{
				task("T4.2"),
				task("T6.2", "T6.1"),
				task("T6.1", "T4.2"),
				task("T1.1"),
			},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2"),
				entry("T1.1", ledger.Done),
			}},
			want: Decision{Reason: ReasonStuck, Stuck: []string{"T6.2", "T6.1"}},
		},
		{
			name:      "task added between two reads",
			tasks:     []masterplan.Task{task("T1.1"), task("T1.2", "T1.1")},
			ledger:    ledger.Ledger{Tasks: []ledger.Entry{entry("T1.1", ledger.Done), entry("T1.2", ledger.Done)}},
			want:      Decision{Reason: ReasonAllDone},
			thenTasks: []masterplan.Task{task("T1.1"), task("T1.2", "T1.1"), task("T1.3", "T1.2")},
			thenWant:  Decision{Task: "T1.3"},
		},
		{
			name:  "task added between two reads ahead of a ready one",
			tasks: []masterplan.Task{task("T1.1"), task("T2.1", "T1.1")},
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.1", ledger.Done),
			}},
			want: Decision{Task: "T2.1"},
			// The new task is inserted earlier in the plan, so plan order
			// now puts it first.
			thenTasks: []masterplan.Task{task("T1.1"), task("T1.2", "T1.1"), task("T2.1", "T1.1")},
			thenWant:  Decision{Task: "T1.2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Next(tc.tasks, tc.ledger, tc.openItems)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Next() = %+v, want %+v", got, tc.want)
			}
			// Every decision is either a task or a reason, never both and
			// never neither: a caller branches on Task being empty.
			if (got.Task == "") == (got.Reason == ReasonNone) {
				t.Errorf("Next() = %+v: want exactly one of Task and Reason set", got)
			}
			if tc.thenTasks == nil {
				return
			}
			then := Next(tc.thenTasks, tc.ledger, tc.openItems)
			if !reflect.DeepEqual(then, tc.thenWant) {
				t.Errorf("second Next() = %+v, want %+v", then, tc.thenWant)
			}
		})
	}
}

// TestNextEmptyPlan pins the one input the table cannot hold as a useful row:
// no tasks at all. masterplan.Parse refuses such a plan, so this only guards
// against Next choosing an empty id or panicking if a caller ever passes one.
func TestNextEmptyPlan(t *testing.T) {
	got := Next(nil, ledger.Ledger{Tasks: []ledger.Entry{entry("T1.1", ledger.Running)}}, nil)
	if want := (Decision{Reason: ReasonAllDone}); !reflect.DeepEqual(got, want) {
		t.Errorf("Next(nil) = %+v, want %+v", got, want)
	}
}

// TestInvalidPlanIsRejectedBeforeNext holds the two T1.1 acceptance rows that
// are not Next's to decide, "unknown dependency" and "cycle", beside the
// next-task table. Next is never called on such a plan: its callers run
// masterplan.Validate first and stop on an error, and Next assumes every
// dependency names a task and that following dependencies ends. So the rows
// assert the sentinel error and that no task comes out of the step a caller
// actually takes.
//
// Both rows are load-bearing for the same reason: either plan, passed on to
// Next, would leave tasks that can never start, and the owner would be told
// the build is stuck rather than that the plan has a mistake on a given line.
func TestInvalidPlanIsRejectedBeforeNext(t *testing.T) {
	// choose is what every caller does: validate, then ask for the next task.
	choose := func(tasks []masterplan.Task) (string, error) {
		if err := masterplan.Validate(tasks); err != nil {
			return "", err
		}
		return Next(tasks, ledger.Ledger{}, nil).Task, nil
	}

	cases := []struct {
		name    string
		tasks   []masterplan.Task
		wantErr error
	}{
		{
			name:    "unknown dependency",
			tasks:   []masterplan.Task{task("T1.1"), task("T1.2", "T1.1", "T7.7")},
			wantErr: masterplan.ErrUnknownDependency,
		},
		{
			name: "cycle",
			tasks: []masterplan.Task{
				task("T1.1", "T1.3"),
				task("T1.2", "T1.1"),
				task("T1.3", "T1.2"),
			},
			wantErr: masterplan.ErrCycle,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := choose(tc.tasks)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tc.wantErr)
			}
			if got != "" {
				t.Errorf("task = %q, want none for an invalid plan", got)
			}
		})
	}
}

// TestUnmet covers the dependency test on its own, as `afk tasks` uses it.
//
// The load-bearing rows are "keeps the plan's order", because the listing
// shows the ids as the plan wrote them, and "superseded with one replacement
// pending", because showing T6.1 as free to start while T4.2b is unfinished
// would contradict what Next does.
func TestUnmet(t *testing.T) {
	cases := []struct {
		name   string
		task   masterplan.Task
		ledger ledger.Ledger
		want   []string
	}{
		{
			name: "no dependencies",
			task: task("T1.1"),
			want: nil,
		},
		{
			name:   "every dependency done",
			task:   task("T1.3", "T1.1", "T1.2"),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{entry("T1.1", ledger.Done), entry("T1.2", ledger.Done)}},
			want:   nil,
		},
		{
			name: "keeps the plan's order",
			task: task("T2.6", "T2.5", "T0.3", "T2.4"),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T0.3", ledger.Done),
			}},
			want: []string{"T2.5", "T2.4"},
		},
		{
			name: "every status but done is unmet",
			task: task("T3.1", "T1.1", "T1.2", "T1.3", "T1.4", "T1.5", "T1.6", "T1.7"),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				entry("T1.1", ledger.Pending),
				entry("T1.2", ledger.Running),
				entry("T1.3", ledger.Shipping),
				parked("T1.4", "q1"),
				entry("T1.5", ledger.Done),
				entry("T1.6", ledger.Failed),
				// T1.7 has no entry, which is pending.
			}},
			want: []string{"T1.1", "T1.2", "T1.3", "T1.4", "T1.6", "T1.7"},
		},
		{
			name: "superseded with one replacement pending",
			task: task("T6.1", "T4.2", "T4.3"),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a", "T4.2b"),
				entry("T4.2a", ledger.Done),
				entry("T4.3", ledger.Done),
			}},
			// The old id is reported, as the plan names it, not T4.2b.
			want: []string{"T4.2"},
		},
		{
			name: "superseded with every replacement done",
			task: task("T6.1", "T4.2", "T4.3"),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a", "T4.2b"),
				entry("T4.2a", ledger.Done),
				entry("T4.2b", ledger.Done),
				entry("T4.3", ledger.Done),
			}},
			want: nil,
		},
		{
			name:   "superseded with no replacements",
			task:   task("T6.1", "T4.2"),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{superseded("T4.2")}},
			want:   []string{"T4.2"},
		},
		{
			name: "replacements naming each other",
			task: task("T6.1", "T4.2"),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2a"),
				superseded("T4.2a", "T4.2"),
			}},
			want: []string{"T4.2"},
		},
		{
			name: "a task superseded by itself",
			task: task("T6.1", "T4.2"),
			ledger: ledger.Ledger{Tasks: []ledger.Entry{
				superseded("T4.2", "T4.2"),
			}},
			want: []string{"T4.2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Unmet(tc.task, tc.ledger)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Unmet() = %v, want %v", got, tc.want)
			}
		})
	}
}
