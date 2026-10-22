package projectrows

import (
	"reflect"
	"testing"
)

// TestBuild is the state table. Load-bearing rows: the precedence rows (gone
// beats everything, stopped beats paused, unreadable beats no plan) because
// "first match wins" is the whole contract; parked_on_inbox_item and
// plan_waiting because they are what puts a healthy project at the top of
// the screen (NeedsOwner); and running_without_phase because the screen must
// say "unknown" and not print a bare colon.
func TestBuild(t *testing.T) {
	plan := Input{Name: "app", Root: "/p/app", HasPlan: true, Total: 5, Done: 2}
	with := func(f func(*Input)) Input { in := plan; f(&in); return in }

	tests := []struct {
		name       string
		in         Input
		state      State
		label      string
		detail     string
		needsOwner bool
	}{
		{"running", with(func(i *Input) { i.Task, i.Title, i.Phase = "T1.2", "Add it", "implementing" }),
			StateRunning, "running", "T1.2 Add it: implementing", false},
		{"running_without_phase", with(func(i *Input) { i.Task, i.Title = "T1.2", "Add it" }),
			StateRunning, "running", "T1.2 Add it: unknown", false},
		{"paused", with(func(i *Input) { i.Paused = true }),
			StatePaused, "paused", "Paused: no new task will start until you resume it. A task that is running carries on.", false},
		{"stopped_with_task_and_reason", with(func(i *Input) { i.Stopped, i.StopTask, i.StopReason = true, "T1.2", "the deploy failed." }),
			StateStopped, "stopped", `Stopped: the line is stopped after T1.2: the deploy failed. No new task will start until you run "afk resume app", which clears the stop.`, true},
		{"stopped_bare", with(func(i *Input) { i.Stopped = true }),
			StateStopped, "stopped", `Stopped: the line is stopped. No new task will start until you run "afk resume app", which clears the stop.`, true},
		{"parked_on_inbox_item", with(func(i *Input) { i.Kind, i.StateKnown, i.Open, i.Blocking = "park", true, 3, 2 }),
			StateWaiting, "waiting on you", "a task is parked until you answer its inbox items", true},
		{"waiting_with_reason", with(func(i *Input) { i.Kind, i.Reason = "give_up", "it kept failing" }),
			StateWaiting, "waiting on you", "a session could not be recovered; waiting for you: it kept failing", true},
		{"held_for_usage", with(func(i *Input) { i.Kind = "usage_wait" }),
			StateHeld, "held for usage", "nothing new is started, because of the usage limits", false},
		{"held_login", with(func(i *Input) { i.Kind = "login_wait" }),
			StateHeld, "held for usage", "nothing new is started, because the agent is logged out", false},
		{"finished", with(func(i *Input) { i.Done = 5 }),
			StateFinished, "finished", "Every task is done.", false},
		{"no_plan", Input{Name: "app", Root: "/p/app"},
			StateNoPlan, "no plan", "This project has no master plan to build.", false},
		{"plan_waiting", with(func(i *Input) { i.PlanWaiting = true }),
			StateNothingReady, "nothing ready", "Nothing is ready to start.", true},
		{"unreadable", with(func(i *Input) { i.ReadErr = "ledger.json: bad\nsecond line" }),
			StateUnreadable, "can't read", "ledger.json: bad", true},
		{"gone", with(func(i *Input) { i.Gone = true }),
			StateGone, "gone", "The project folder does not exist. Check its path in config.toml.", true},
		{"nothing_ready_with_kind", with(func(i *Input) { i.Kind, i.StateKnown = "idle", true }),
			StateNothingReady, "nothing ready", "nothing to start", false},
		{"nothing_ready_with_kind_and_reason", with(func(i *Input) { i.Kind, i.Reason, i.StateKnown = "wait", "ci", true }),
			StateNothingReady, "nothing ready", "waiting: ci", false},
		{"blocking_inbox_on_running", with(func(i *Input) { i.Task, i.Title, i.Phase, i.Blocking = "T1", "x", "p", 1 }),
			StateRunning, "running", "T1 x: p", true},
		{"open_non_blocking_does_not_need_owner", with(func(i *Input) { i.Open = 4 }),
			StateNothingReady, "nothing ready", "Nothing is ready to start.", false},
		{"stopped_beats_paused", with(func(i *Input) { i.Stopped, i.Paused = true, true }),
			StateStopped, "stopped", `Stopped: the line is stopped. No new task will start until you run "afk resume app", which clears the stop.`, true},
		{"paused_beats_waiting", with(func(i *Input) { i.Paused, i.Kind = true, "park" }),
			StatePaused, "paused", "Paused: no new task will start until you resume it. A task that is running carries on.", false},
		{"unreadable_beats_no_plan", Input{Name: "app", ReadErr: "boom"},
			StateUnreadable, "can't read", "boom", true},
		{"gone_beats_everything", Input{Name: "app", Gone: true, ReadErr: "x", Stopped: true, Paused: true, Kind: "park", HasPlan: true},
			StateGone, "gone", "The project folder does not exist. Check its path in config.toml.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Build(tt.in)
			if r.State != tt.state || r.Label != tt.label {
				t.Errorf("state = %q/%q, want %q/%q", r.State, r.Label, tt.state, tt.label)
			}
			if r.Detail != tt.detail {
				t.Errorf("detail = %q, want %q", r.Detail, tt.detail)
			}
			if r.NeedsOwner != tt.needsOwner {
				t.Errorf("NeedsOwner = %v, want %v", r.NeedsOwner, tt.needsOwner)
			}
		})
	}
}

// TestBuildCopiesFields: the screen reads progress, counts, the plan flag and
// whether the project is listed from the row alone, so Build must carry them
// through whatever the state. Load-bearing rows: listed_running (every field
// at once) and unlisted_gone, because the remove action (T7.10) is dimmed on
// an unlisted row whatever state hides the rest, and offered on a listed gone
// folder, which is the row most worth removing.
func TestBuildCopiesFields(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want Row
	}{
		{"listed_running",
			Input{Name: "n", Root: "/r", Listed: true, Order: 7, HasPlan: true, Done: 1, Total: 4,
				Task: "T1", Title: "t", Phase: "ph", Open: 3, Blocking: 2, PlanWaiting: true},
			Row{Name: "n", Root: "/r", Listed: true, State: StateRunning, Label: "running", Detail: "T1 t: ph",
				Task: "T1", Title: "t", Phase: "ph", Done: 1, Total: 4, Open: 3, Blocking: 2,
				PlanWaiting: true, NeedsOwner: true, Order: 7}},
		{"unlisted_gone",
			Input{Name: "n", Root: "/r", Gone: true, Order: 9},
			Row{Name: "n", Root: "/r", State: StateGone, Label: "gone",
				Detail: "The project folder does not exist. Check its path in config.toml.", NeedsOwner: true, Order: 9}},
		{"listed_gone",
			Input{Name: "n", Root: "/r", Listed: true, Gone: true},
			Row{Name: "n", Root: "/r", Listed: true, State: StateGone, Label: "gone",
				Detail: "The project folder does not exist. Check its path in config.toml.", NeedsOwner: true}},
		{"unlisted_no_plan",
			Input{Name: "n", Root: "/r"},
			Row{Name: "n", Root: "/r", State: StateNoPlan, Label: "no plan",
				Detail: "This project has no master plan to build."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r := Build(tt.in); !reflect.DeepEqual(r, tt.want) {
				t.Errorf("row = %+v, want %+v", r, tt.want)
			}
		})
	}
}

// TestSort: needs_owner_first_then_config_order is load-bearing (the screen's
// whole point), and unlisted_active_last pins the largest-Order convention.
// stable_ties pins that equal orders keep their input order.
func TestSort(t *testing.T) {
	r := func(name string, order int, needs bool) Row { return Row{Name: name, Order: order, NeedsOwner: needs} }
	tests := []struct {
		name string
		in   []Row
		want []string
	}{
		{"needs_owner_first_then_config_order",
			[]Row{r("a", 0, false), r("b", 1, true), r("c", 2, false), r("d", 3, true)},
			[]string{"b", "d", "a", "c"}},
		{"config_order_within_group",
			[]Row{r("c", 2, true), r("a", 0, true), r("b", 1, true)},
			[]string{"a", "b", "c"}},
		{"unlisted_active_last",
			[]Row{r("active", 99, false), r("a", 0, false), r("b", 1, false)},
			[]string{"a", "b", "active"}},
		{"unlisted_active_last_among_owner_rows",
			[]Row{r("active", 99, true), r("z", 5, true), r("q", 0, false)},
			[]string{"z", "active", "q"}},
		{"stable_ties",
			[]Row{r("x", 1, false), r("y", 1, false), r("z", 1, false)},
			[]string{"x", "y", "z"}},
		{"empty", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Sort(tt.in)
			var got []string
			for _, x := range tt.in {
				got = append(got, x.Name)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestKindText holds every kind `afk daemon status` words, byte for byte,
// plus an unknown kind that must come back as stored. Load-bearing: W3.T1
// makes daemon_cmd.go delegate here, so a changed word changes that report.
func TestKindText(t *testing.T) {
	tests := []struct{ kind, want string }{
		{"launch", "started a task"},
		{"create", "started the session for a running task again"},
		{"wait", "waiting"},
		{"idle", "nothing to start"},
		{"ship", "a task is finished and waiting to be shipped"},
		{"fail", "a task failed"},
		{"park", "a task is parked until you answer its inbox items"},
		{"unpark", "a parked task is running again"},
		{"stall", "a session is asking something at its terminal"},
		{"host_gone", "a session has stopped running"},
		{"stop", "stopped, because the project cannot be run"},
		{"stopped", "stopped, because the project folder was not found"},
		{"blocked", "a task is ready, but this machine cannot start agents"},
		{"shipping", "shipping a finished task"},
		{"owner_task", "the next task is yours to do, and is waiting in your inbox"},
		{"owner_done", "you confirmed your task, so it is marked done"},
		{"question_hold", "a task is waiting for you to answer an open question from the plan"},
		{"question_answered", "an open question was answered, so the task it held can start"},
		{"no_ledger", "nothing is started, because this project has no task ledger yet"},
		{"line_stopped", "the line is stopped, so no new task will start"},
		{"restart", "restarting a session that stopped answering"},
		{"relaunch", "starting a stopped session again"},
		{"continue", "telling a restarted session to carry on"},
		{"give_up", "a session could not be recovered; waiting for you"},
		{"usage_wait", "nothing new is started, because of the usage limits"},
		{"login_wait", "nothing new is started, because the agent is logged out"},
		{"usage_pause", "stopping a session because of the usage limits"},
		{"usage_paused", "a session is paused because of the usage limits"},
		{"usage_stuck", "a session that had to pause for usage would not stop; waiting for you"},
		{"usage_resume", "starting a session that was paused for usage again"},
		{"wake", "waking a session whose agent stopped working"},
		{"wake_give_up", "a session kept failing after being woken; waiting for you"},
		{"something_new", "something_new"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			if got := KindText(tt.kind); got != tt.want {
				t.Errorf("KindText(%q) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

// TestStoppedText pins the stop line `afk daemon status` prints, including the
// trailing period trimmed from the reason so it is not doubled.
func TestStoppedText(t *testing.T) {
	tests := []struct{ name, task, reason, proj, want string }{
		{"both", "T1", "deploy failed.", "app", `Stopped: the line is stopped after T1: deploy failed. No new task will start until you run "afk resume app", which clears the stop.`},
		{"task_only", "T1", "", "app", `Stopped: the line is stopped after T1. No new task will start until you run "afk resume app", which clears the stop.`},
		{"reason_only", "", "  why  ", "app", `Stopped: the line is stopped: why. No new task will start until you run "afk resume app", which clears the stop.`},
		{"neither", "", "", "app", `Stopped: the line is stopped. No new task will start until you run "afk resume app", which clears the stop.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StoppedText(tt.task, tt.reason, tt.proj); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGroupOf: needs_you_beats_state is load-bearing, since a paused project
// with a blocking item or a plan waiting must sit under NEEDS YOU and not
// QUIET. Every State has a row so a new state cannot silently fall through.
func TestGroupOf(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		want Group
	}{
		{"gone", Row{State: StateGone, NeedsOwner: true}, GroupNeedsYou},
		{"unreadable", Row{State: StateUnreadable, NeedsOwner: true}, GroupNeedsYou},
		{"stopped", Row{State: StateStopped, NeedsOwner: true}, GroupNeedsYou},
		{"waiting", Row{State: StateWaiting, NeedsOwner: true}, GroupNeedsYou},
		{"no_plan", Row{State: StateNoPlan}, GroupQuiet},
		{"paused", Row{State: StatePaused}, GroupQuiet},
		{"held", Row{State: StateHeld}, GroupQuiet},
		{"nothing_ready", Row{State: StateNothingReady}, GroupQuiet},
		{"running", Row{State: StateRunning}, GroupRunning},
		{"finished", Row{State: StateFinished}, GroupFinished},
		{"paused_with_blocking_item_needs_you", Row{State: StatePaused, Blocking: 1, NeedsOwner: true}, GroupNeedsYou},
		{"nothing_ready_with_plan_waiting_needs_you", Row{State: StateNothingReady, PlanWaiting: true, NeedsOwner: true}, GroupNeedsYou},
		{"running_with_blocking_item_needs_you", Row{State: StateRunning, Blocking: 1, NeedsOwner: true}, GroupNeedsYou},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GroupOf(tt.row); got != tt.want {
				t.Errorf("GroupOf = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGroupString pins the four headings' words and their screen order.
func TestGroupString(t *testing.T) {
	want := []string{"NEEDS YOU", "RUNNING", "QUIET", "FINISHED"}
	for i, w := range want {
		if got := Group(i).String(); got != w {
			t.Errorf("Group(%d) = %q, want %q", i, got, w)
		}
	}
}

// TestGrouped: config_order_inside_groups is load-bearing, since the screen
// relies on Sort's order surviving the split; empty_groups_stay_empty lets the
// caller skip a heading by length.
func TestGrouped(t *testing.T) {
	r := func(name string, s State, needs bool) Row { return Row{Name: name, State: s, NeedsOwner: needs} }
	tests := []struct {
		name string
		in   []Row
		want [][]string
	}{
		{"config_order_inside_groups",
			[]Row{r("a", StateStopped, true), r("b", StateGone, true), r("c", StateRunning, false),
				r("d", StateRunning, false), r("e", StateFinished, false), r("f", StatePaused, false), r("g", StateNoPlan, false)},
			[][]string{{"a", "b"}, {"c", "d"}, {"f", "g"}, {"e"}}},
		{"empty_groups_stay_empty",
			[]Row{r("a", StateFinished, false)},
			[][]string{nil, nil, nil, {"a"}}},
		{"no_rows", nil, [][]string{nil, nil, nil, nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Grouped(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("groups = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				var names []string
				for _, x := range got[i] {
					names = append(names, x.Name)
				}
				if len(names) != len(tt.want[i]) || (len(names) > 0 && !reflect.DeepEqual(names, tt.want[i])) {
					t.Errorf("group %v = %v, want %v", Group(i), names, tt.want[i])
				}
			}
		})
	}
}

// TestBuildCopiesWait: Build must carry Wait through unchanged in any state,
// since the panel reads it from the row alone.
func TestBuildCopiesWait(t *testing.T) {
	w := Wait{ItemID: "aaaaa2", Kind: "question", Title: "Which?", Body: "b", CreatedAt: "2026-10-12T10:00:00Z", Task: "T1.3", TaskTitle: "Third"}
	for _, in := range []Input{
		{Name: "n", Root: "/r", HasPlan: true, Wait: w},
		{Name: "n", Root: "/r", Gone: true, Wait: w},
	} {
		if got := Build(in).Wait; got != w {
			t.Errorf("Wait = %+v, want %+v", got, w)
		}
	}
	if got := Build(Input{Name: "n", HasPlan: true}).Wait; got != (Wait{}) {
		t.Errorf("Wait = %+v, want empty", got)
	}
}
