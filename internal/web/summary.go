package web

import (
	"fmt"
	"net/url"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/projectrows"
)

// The words shown as a task's phase when they do not come from a file.
const (
	// PhaseUnknown is a running task whose session cannot be read: no
	// session of that name, or a status.json that is stale.
	PhaseUnknown = "unknown"
	// PhaseUsagePaused is a running task the daemon stopped to save usage.
	PhaseUsagePaused = "paused for usage"
	// PhaseWaiting is a parked task.
	PhaseWaiting = "waiting on you"
	// PhaseShipping is a shipping task whose ledger entry has no ship word
	// yet (the ship has been decided but its first step is not recorded).
	PhaseShipping = "shipping"
)

// SummaryInput is everything Summarize reads. The caller does all the file
// reading and passes the results in, so the whole matrix can be tested
// without a project on disk.
type SummaryInput struct {
	Name string
	// Tasks and Ledger are the master plan and the ledger; LoadErr is why
	// they could not be read. With an error set the other two are ignored.
	Tasks   []masterplan.Task
	Ledger  ledger.Ledger
	LoadErr error
	// State is daemon.json and StateKnown whether it could be read.
	State      daemonstate.State
	StateKnown bool
	Paused     bool
	// Stopped is whether the stop marker exists; StopTask and StopReason are
	// what it says, and may be empty when the marker is damaged.
	Stopped    bool
	StopTask   string
	StopReason string
	Sessions   []SessionView
	Items      []inbox.Item
	// Now is used only to say how long ago the last task shipped.
	Now time.Time
}

// ProjectSummary is one project as the overview shows it. Every key is
// always present in the JSON; `stopped`, `current` and `state` are null when
// there is nothing to say.
type ProjectSummary struct {
	Name string `json:"name"`
	// Problem is why the plan or ledger could not be read, in the error's
	// own words, and empty otherwise. With a problem there is no current
	// task and no count: guessing them from half the data would mislead.
	Problem      string       `json:"problem"`
	Paused       bool         `json:"paused"`
	Stopped      *StopView    `json:"stopped"`
	Current      *CurrentTask `json:"current"`
	Done         int          `json:"done"`
	Total        int          `json:"total"`
	LastShip     LastShip     `json:"last_ship"`
	State        *StateView   `json:"state"`
	OpenItems    int          `json:"open_items"`
	OpenBlocking int          `json:"open_blocking"`
}

// StopView is the stop marker: the build line is stopped until the owner
// resumes it.
type StopView struct {
	Task   string `json:"task"`
	Reason string `json:"reason"`
}

// CurrentTask is the task the project is on.
type CurrentTask struct {
	Task    string `json:"task"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Session string `json:"session"`
	Phase   string `json:"phase"`
	// WaitingOn is the inbox items a parked task waits for; an empty array
	// for any other task.
	WaitingOn []string `json:"waiting_on"`
}

// LastShip is how the last attempt to ship a task ended. Text is always
// set, so a page can show it without looking at the other fields.
type LastShip struct {
	Task       string `json:"task"`
	PR         int    `json:"pr"`
	MergedSHA  string `json:"merged_sha"`
	FinishedAt string `json:"finished_at"`
	Stopped    bool   `json:"stopped"`
	Text       string `json:"text"`
}

// StateView is what the daemon last did for the project, from daemon.json.
type StateView struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// Summarize turns what was read from a project's files into what the page
// shows. It is pure.
func Summarize(in SummaryInput) ProjectSummary {
	out := ProjectSummary{Name: in.Name, Paused: in.Paused}
	if in.Stopped {
		out.Stopped = &StopView{Task: in.StopTask, Reason: in.StopReason}
	}
	if in.StateKnown {
		out.State = &StateView{Kind: in.State.Kind, Reason: in.State.Reason, At: in.State.At}
	}
	for _, it := range in.Items {
		if !it.Open() {
			continue
		}
		out.OpenItems++
		if it.Blocking {
			out.OpenBlocking++
		}
	}

	if in.LoadErr != nil {
		out.Problem = in.LoadErr.Error()
		out.LastShip = lastShip(in, ledger.Ledger{})
		return out
	}

	out.Current = currentTask(in.Tasks, in.Ledger, in.Sessions)
	for _, t := range in.Tasks {
		switch in.Ledger.Status(t.ID) {
		case ledger.Superseded:
			// Its work moved to the tasks that replace it, which are in
			// the plan themselves; counting it too would leave the total
			// one that can never be reached.
		case ledger.Done:
			out.Done++
			out.Total++
		default:
			out.Total++
		}
	}
	out.LastShip = lastShip(in, in.Ledger)
	return out
}

// currentTask picks the task to show: the first one in plan order that is
// running or shipping, or failing that the first parked one. Work in progress
// comes first because that is what "what is afk doing" asks; a parked task is
// shown only when nothing is moving, which is when it is the reason.
func currentTask(tasks []masterplan.Task, l ledger.Ledger, sessions []SessionView) *CurrentTask {
	var parked *CurrentTask
	for _, t := range tasks {
		e, ok := l.Get(t.ID)
		if !ok {
			continue
		}
		cur := &CurrentTask{Task: t.ID, Title: t.Title, Status: string(e.Status), Session: e.Session, WaitingOn: []string{}}
		switch e.Status {
		case ledger.Running:
			cur.Phase = runningPhase(e, sessions)
			return cur
		case ledger.Shipping:
			cur.Phase = e.Ship
			if cur.Phase == "" {
				cur.Phase = PhaseShipping
			}
			return cur
		case ledger.Parked:
			if parked == nil {
				cur.Phase = PhaseWaiting
				cur.WaitingOn = append(cur.WaitingOn, e.ParkedOn...)
				parked = cur
			}
		}
	}
	return parked
}

// runningPhase is the phase of a running task.
//
// The usage pause is checked before the session is looked at. A session the
// daemon stopped to save usage has no host, and its status.json still holds
// whatever phase it was in when it was stopped; read on their own, those say
// "executing" for a task that is not running, or look like a session that
// died. The ledger's usage_pause_at is the only record that the stop was
// deliberate (PLAN T2.4), so it wins.
func runningPhase(e ledger.Entry, sessions []SessionView) string {
	if e.UsagePauseAt != "" {
		return PhaseUsagePaused
	}
	for _, sv := range sessions {
		if sv.Name != e.Session {
			continue
		}
		if sv.Stale || sv.Phase == "" {
			return PhaseUnknown
		}
		return sv.Phase
	}
	return PhaseUnknown
}

// lastShip says how the last ship ended.
//
// The stop marker comes first, even when a task finished after it was
// written: the marker means the line is stopped now and stays stopped until
// the owner resumes, and a newer "shipped" line above a stopped project
// would hide the one thing that needs doing.
func lastShip(in SummaryInput, l ledger.Ledger) LastShip {
	if in.Stopped {
		ls := LastShip{Task: in.StopTask, Stopped: true}
		switch {
		case in.StopTask != "" && in.StopReason != "":
			ls.Text = fmt.Sprintf("%s stopped the build: %s", in.StopTask, in.StopReason)
		case in.StopReason != "":
			ls.Text = "The build is stopped: " + in.StopReason
		case in.StopTask != "":
			ls.Text = in.StopTask + " stopped the build. The reason was not recorded."
		default:
			ls.Text = "The build is stopped. The reason could not be read."
		}
		return ls
	}
	if in.LoadErr != nil {
		return LastShip{Text: "Not known, because the plan or the ledger could not be read"}
	}

	var (
		best     ledger.Entry
		bestTime time.Time
		found    bool
	)
	for _, e := range l.Tasks {
		if e.Status != ledger.Done {
			continue
		}
		// A time that does not parse is the zero time, as elsewhere in
		// the ledger. "Not before" and not "after": entries with the same
		// time, or with none (a ledger started with tasks already done),
		// resolve to the later one in the ledger's order. The ledger is
		// sorted by task id every time it is written, so that is the
		// later task.
		at, _ := time.Parse(time.RFC3339, e.FinishedAt)
		if !found || !at.Before(bestTime) {
			best, bestTime, found = e, at, true
		}
	}
	if !found {
		return LastShip{Text: "Nothing shipped yet"}
	}
	ls := LastShip{Task: best.ID, PR: best.PR, MergedSHA: best.MergedSHA, FinishedAt: best.FinishedAt}
	ls.Text = best.ID + " shipped"
	if ago := since(bestTime, in.Now); ago != "" {
		ls.Text += " " + ago
	}
	return ls
}

// since says how long before now a time was, in the largest whole unit, or
// "" when that cannot be said: no time, no clock, or a time in the future
// (a clock that was wrong on the machine that wrote it).
func since(at, now time.Time) string {
	if at.IsZero() || now.IsZero() || at.After(now) {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/(24*time.Hour)), "day")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// RowView is one row of the all-projects screen as the JSON API gives it:
// projectrows.Row with snake_case keys. The folder, the config order and
// whether the project is listed are left out, as ProjectSummary leaves them
// out: the API names projects, it does not say where they are.
type RowView struct {
	Name string `json:"name"`
	// State is the row's projectrows.State word, Label its short word and
	// Detail the one sentence projectrows wrote for it (T7.9's words).
	State  string `json:"state"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
	// Task, Title and Phase are the current task, empty when none.
	Task  string `json:"task"`
	Title string `json:"title"`
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	// Open and Blocking are the inbox counts.
	Open        int  `json:"open"`
	Blocking    int  `json:"blocking"`
	PlanWaiting bool `json:"plan_waiting"`
	NeedsOwner  bool `json:"needs_owner"`
}

func rowView(r projectrows.Row) RowView {
	return RowView{
		Name: r.Name, State: string(r.State), Label: r.Label, Detail: r.Detail,
		Task: r.Task, Title: r.Title, Phase: r.Phase,
		Done: r.Done, Total: r.Total, Open: r.Open, Blocking: r.Blocking,
		PlanWaiting: r.PlanWaiting, NeedsOwner: r.NeedsOwner,
	}
}

// badgeView is a state badge: the design's variant class and its word. The
// word is always drawn, so the state never rests on colour alone.
type badgeView struct {
	Class, Word string
}

// planWaitingBadge marks a plan on the review list, for a project that has
// one besides its build and for a plan that has no project yet.
var planWaitingBadge = badgeView{Class: "badge--needs-you", Word: "Plan waiting for approval"}

// stateBadge maps a row's state to the design system's project-state badge
// (docs/design-system.md, badge). The design names seven states; gone, can't
// read and no plan are projectrows' own and have no variant there, so they
// borrow the nearest one's colour (the danger colour for the two the owner
// must fix, the idle one for a project with nothing to build) and say their
// own word. A state this binary does not know is shown as it is, plain, so
// a newer projectrows still reads.
func stateBadge(s projectrows.State) badgeView {
	switch s {
	case projectrows.StateRunning:
		return badgeView{"badge--running", "Building"}
	case projectrows.StateWaiting:
		return badgeView{"badge--needs-you", "Waiting on you"}
	case projectrows.StatePaused:
		return badgeView{"badge--paused", "Paused"}
	case projectrows.StateStopped:
		return badgeView{"badge--stopped", "Line stopped"}
	case projectrows.StateHeld:
		return badgeView{"badge--held", "Held for usage"}
	case projectrows.StateNothingReady:
		return badgeView{"badge--idle", "Nothing ready"}
	case projectrows.StateFinished:
		return badgeView{"badge--finished", "Finished"}
	case projectrows.StateGone:
		return badgeView{"badge--error", "Gone"}
	case projectrows.StateUnreadable:
		return badgeView{"badge--error", "Can't read"}
	case projectrows.StateNoPlan:
		return badgeView{"badge--idle", "No plan"}
	}
	return badgeView{"badge--plain", string(s)}
}

// inboxLine is a row's inbox counts in words, or "" when the inbox has
// nothing open: a row that says "0 in inbox" is noise on a screen whose job
// is to show what needs the owner.
func inboxLine(open, blocking int) string {
	if open <= 0 {
		return ""
	}
	s := fmt.Sprintf("%d in inbox", open)
	switch {
	case blocking == 1:
		s += ", 1 holds a task up"
	case blocking > 1:
		s += fmt.Sprintf(", %d hold a task up", blocking)
	}
	return s
}

// overviewRow is one row as the home page draws it.
type overviewRow struct {
	Name string
	// Href is where the row goes: the project page, or the plan for a plan
	// waiting for approval that has no project yet.
	Href string
	// Live is true for a project's row, which carries the live markup; a
	// plan waiting alone has no part the change stream names (there is no
	// live part for plan-reviews.json), so it has none.
	Live     bool
	NeedsYou bool
	Selected bool
	// Working marks a running build, for the live indicator's Working.
	Working bool
	Badge   badgeView
	// Extra is a second badge, drawn when its Word is set: a configured
	// project whose amended plan waits for approval.
	Extra badgeView
	// Task, Title and Phase are set for a running build, whose sub-line is
	// its task id and phase; Detail is the sub-line otherwise.
	Task, Title, Phase string
	Detail             string
	Done, Total        int
	Inbox              string
}

// overviewPage is what overview.html draws besides the usage reading. It is
// built by the pure overviewPageFor so every branch of the home page is a
// table row.
type overviewPage struct {
	Heading  string
	NeedsYou []overviewRow
	Others   []overviewRow
	// Selected is the row shown on the right of the desktop split: the
	// first row, needs-you first. Nil when there are no rows.
	Selected *overviewRow
	// Empty is true when there is nothing at all to list.
	Empty bool
}

// reviewOnlyDetail is the sub-line of a plan waiting for approval that has
// no project yet: what the owner does about it.
const reviewOnlyDetail = "Read it, then run afk plan approve in the project folder."

// overviewPageFor lays out the home page from the rows (already in
// projectrows.Sort's order) and the plans waiting for approval that are not
// configured projects.
//
// A review that a row already names is left out: the row is that project,
// and two rows for one name would link the plan twice. The others are
// needs-you rows placed after the project rows that need the owner, since
// projectrows.Sort orders by config.toml and a plan waiting alone has no
// place there.
func overviewPageFor(rows []RowView, reviews []ReviewView) overviewPage {
	var p overviewPage
	named := make(map[string]bool, len(rows))
	for _, r := range rows {
		named[r.Name] = true
		or := overviewRow{
			Name: r.Name, Href: "/p/" + url.PathEscape(r.Name), Live: true,
			NeedsYou: r.NeedsOwner, Badge: stateBadge(projectrows.State(r.State)),
			Detail: r.Detail, Done: r.Done, Total: r.Total, Inbox: inboxLine(r.Open, r.Blocking),
		}
		if r.PlanWaiting {
			or.Extra = planWaitingBadge
		}
		if projectrows.State(r.State) == projectrows.StateRunning {
			or.Working = true
			or.Task, or.Title, or.Phase = r.Task, r.Title, r.Phase
			if or.Phase == "" {
				or.Phase = PhaseUnknown
			}
		}
		if or.NeedsYou {
			p.NeedsYou = append(p.NeedsYou, or)
		} else {
			p.Others = append(p.Others, or)
		}
	}
	for _, rv := range reviews {
		if named[rv.Name] {
			continue
		}
		named[rv.Name] = true
		p.NeedsYou = append(p.NeedsYou, overviewRow{
			Name: rv.Name, Href: "/p/" + url.PathEscape(rv.Name) + "/plan",
			NeedsYou: true, Badge: planWaitingBadge, Detail: reviewOnlyDetail,
		})
	}

	switch n := len(p.NeedsYou); n {
	case 0:
		p.Heading = "Nothing needs you"
	case 1:
		p.Heading = "1 project needs you"
	default:
		p.Heading = fmt.Sprintf("%d projects need you", n)
	}
	switch {
	case len(p.NeedsYou) > 0:
		p.NeedsYou[0].Selected = true
		p.Selected = &p.NeedsYou[0]
	case len(p.Others) > 0:
		p.Others[0].Selected = true
		p.Selected = &p.Others[0]
	default:
		p.Empty = true
	}
	return p
}
