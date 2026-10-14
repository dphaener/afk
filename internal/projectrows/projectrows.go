// Package projectrows turns what is known about each project into the rows of
// the all-projects screen.
//
// It is the one pure place both `afk dash` (T7.9) and the web page (T8.5)
// build those rows from, so it imports neither internal/web nor internal/ui,
// and reads no file, dials nothing and uses no network: the caller reads the
// files and fills an Input. The words (KindText, StoppedText) are the ones
// `afk daemon status` prints, moved here so the shell report and the screens
// cannot drift apart.
package projectrows

import (
	"fmt"
	"sort"
	"strings"
)

// State is the one state a project row is in. First match wins in Build.
type State string

const (
	StateGone         State = "gone"
	StateUnreadable   State = "can't read"
	StateNoPlan       State = "no plan"
	StateStopped      State = "stopped"
	StatePaused       State = "paused"
	StateWaiting      State = "waiting on you"
	StateHeld         State = "held for usage"
	StateRunning      State = "running"
	StateFinished     State = "finished"
	StateNothingReady State = "nothing ready"
)

// Input is everything Build needs about one project, read by the caller.
type Input struct {
	Name, Root string
	// Listed is true for a [[project]] in config.toml; Order is its position
	// there. The unlisted active project gets the largest Order so it sorts
	// last within its group.
	Listed bool
	Order  int
	// Gone is a listed folder that is missing. ReadErr is non-empty when the
	// project's files could not be read.
	Gone    bool
	ReadErr string
	HasPlan bool
	Done    int
	Total   int
	// Task, Title and Phase are the current task, empty when none.
	Task, Title, Phase string
	Paused             bool
	Stopped            bool
	StopTask           string
	StopReason         string
	// Kind and Reason come from .afk/build/daemon.json; StateKnown is false
	// when the daemon has not written it.
	Kind, Reason string
	StateKnown   bool
	// Open and Blocking are the inbox counts.
	Open, Blocking int
	// PlanWaiting is true when the project is on plan-reviews.json.
	PlanWaiting bool
}

// Row is one line of the all-projects screen.
type Row struct {
	Name, Root string
	// Listed is Input.Listed: true for a [[project]] in config.toml, false
	// for the unlisted start-up project. The dashboard's remove action reads
	// it, since only a listed project has an entry to remove.
	Listed bool
	State  State
	// Label is the short state word; Detail is one plain sentence.
	Label  string
	Detail string
	Task   string
	Title  string
	Phase  string
	Done   int
	Total  int
	Open   int
	// Blocking is how many open inbox items hold a task up.
	Blocking    int
	PlanWaiting bool
	NeedsOwner  bool
	Order       int
}

// Build decides a project's row. Exactly one state applies, and the order of
// the checks is the precedence: a missing folder hides everything else, and a
// stopped line outranks a pause because the stop needs a deliberate resume.
func Build(in Input) Row {
	r := Row{
		Name: in.Name, Root: in.Root, Listed: in.Listed,
		Task: in.Task, Title: in.Title, Phase: in.Phase,
		Done: in.Done, Total: in.Total,
		Open: in.Open, Blocking: in.Blocking,
		PlanWaiting: in.PlanWaiting, Order: in.Order,
	}
	set := func(s State, detail string) {
		r.State, r.Label, r.Detail = s, string(s), detail
	}
	switch {
	case in.Gone:
		set(StateGone, "The project folder does not exist. Check its path in config.toml.")
	case in.ReadErr != "":
		first, _, _ := strings.Cut(in.ReadErr, "\n")
		set(StateUnreadable, first)
	case !in.HasPlan:
		set(StateNoPlan, "This project has no master plan to build.")
	case in.Stopped:
		set(StateStopped, StoppedText(in.StopTask, in.StopReason, in.Name))
	case in.Paused:
		set(StatePaused, "Paused: no new task will start until you resume it. A task that is running carries on.")
	case waitingKind(in.Kind):
		d := KindText(in.Kind)
		if in.Reason != "" {
			d += ": " + in.Reason
		}
		set(StateWaiting, d)
	case heldKind(in.Kind):
		set(StateHeld, KindText(in.Kind))
	case in.Task != "":
		phase := in.Phase
		if phase == "" {
			phase = "unknown"
		}
		set(StateRunning, fmt.Sprintf("%s %s: %s", in.Task, in.Title, phase))
	case in.Total > 0 && in.Done == in.Total:
		set(StateFinished, "Every task is done.")
	default:
		d := "Nothing is ready to start."
		if in.StateKnown && in.Kind != "" {
			d = KindText(in.Kind)
			if in.Reason != "" {
				d += ": " + in.Reason
			}
		}
		set(StateNothingReady, d)
	}
	switch r.State {
	case StateGone, StateUnreadable, StateStopped, StateWaiting:
		r.NeedsOwner = true
	}
	if in.Blocking > 0 || in.PlanWaiting {
		r.NeedsOwner = true
	}
	return r
}

func waitingKind(k string) bool {
	switch k {
	case "park", "owner_task", "question_hold", "give_up", "usage_stuck", "wake_give_up":
		return true
	}
	return false
}

func heldKind(k string) bool {
	switch k {
	case "usage_wait", "login_wait", "usage_pause", "usage_paused":
		return true
	}
	return false
}

// Sort puts the rows that need the owner first, then the rest; inside each
// group the order is config.toml's, with the unlisted active project last.
// It is stable so equal orders keep the order they came in.
func Sort(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].NeedsOwner != rows[j].NeedsOwner {
			return rows[i].NeedsOwner
		}
		return rows[i].Order < rows[j].Order
	})
}

// StoppedText is the line of the report for a project whose line is
// stopped: after which task, why, and the command that clears it. It reads
// the stop marker's own words and not the daemon's state file, so it is right
// even when the daemon has not looked since the stop, or is not running.
//
// A marker that cannot be read still stops the line (existence is the
// signal), so it gets the same line without the two things it could not say.
func StoppedText(task, reason, name string) string {
	s := "Stopped: the line is stopped"
	if task != "" {
		s += " after " + task
	}
	if why := strings.TrimRight(strings.TrimSpace(reason), "."); why != "" {
		s += ": " + why
	}
	return s + fmt.Sprintf(". No new task will start until you run \"afk resume %s\", which clears the stop.", name)
}

// KindText says in plain words what a state kind means. The kinds are
// the loop's own vocabulary (internal/daemon), written here as the strings
// they hold so this package stays free of that package's dependencies; an
// unknown one is shown as it is, so a newer daemon's state still reads under
// an older binary.
func KindText(kind string) string {
	switch kind {
	case "launch":
		return "started a task"
	case "create":
		return "started the session for a running task again"
	case "wait":
		return "waiting"
	case "idle":
		return "nothing to start"
	case "ship":
		return "a task is finished and waiting to be shipped"
	case "fail":
		return "a task failed"
	case "park":
		return "a task is parked until you answer its inbox items"
	case "unpark":
		return "a parked task is running again"
	case "stall":
		return "a session is asking something at its terminal"
	case "host_gone":
		return "a session has stopped running"
	case "stop":
		return "stopped, because the project cannot be run"
	case "stopped":
		return "stopped, because the project folder was not found"
	case "blocked":
		return "a task is ready, but this machine cannot start agents"
	case "shipping":
		return "shipping a finished task"
	case "owner_task":
		return "the next task is yours to do, and is waiting in your inbox"
	case "owner_done":
		return "you confirmed your task, so it is marked done"
	case "question_hold":
		return "a task is waiting for you to answer an open question from the plan"
	case "question_answered":
		return "an open question was answered, so the task it held can start"
	case "no_ledger":
		return "nothing is started, because this project has no task ledger yet"
	case "line_stopped":
		return "the line is stopped, so no new task will start"
	case "restart":
		return "restarting a session that stopped answering"
	case "relaunch":
		return "starting a stopped session again"
	case "continue":
		return "telling a restarted session to carry on"
	case "give_up":
		return "a session could not be recovered; waiting for you"
	case "usage_wait":
		return "nothing new is started, because of the usage limits"
	case "login_wait":
		return "nothing new is started, because the agent is logged out"
	case "usage_pause":
		return "stopping a session because of the usage limits"
	case "usage_paused":
		return "a session is paused because of the usage limits"
	case "usage_stuck":
		return "a session that had to pause for usage would not stop; waiting for you"
	case "usage_resume":
		return "starting a session that was paused for usage again"
	case "wake":
		return "waking a session whose agent stopped working"
	case "wake_give_up":
		return "a session kept failing after being woken; waiting for you"
	}
	return kind
}
