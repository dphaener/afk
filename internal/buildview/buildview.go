// Package buildview defines what the dashboard shows about a build and works
// out the task rows from parts already read. It is pure on purpose: every
// input is a parameter, so it reads no file, calls no clock and runs no
// command. The loader that fills a Snapshot lives with the code that owns the
// files; this package only says what a row means, so the Build tab, and any
// other surface, cannot drift apart on the words.
//
// Status is always the ledger's own word, as `afk tasks` prints it. Inventing
// a second vocabulary here would make the same task read differently on two
// screens.
package buildview

import (
	"strconv"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/nexttask"
	"github.com/dphaener/afk/internal/usagestate"
)

// Snapshot is everything the dashboard knows about a project's build at one
// moment.
type Snapshot struct {
	// Root is the main checkout the data came from.
	Root string
	// HasBuild is true when the plan file or the ledger file exists.
	HasBuild bool
	// Problem is the load error in plain words, or "".
	Problem string
	Rows    []Row
	// Done and Total count the master plan's tasks.
	Done, Total int
	// Current and CurrentPhase name the task being worked on and its phase.
	Current, CurrentPhase string
	Paused, Stopped       bool
	StopTask, StopReason  string
	LastShip              string
	// Items are in the store's order: open and blocking, open, answered.
	Items []inbox.Item
	// Unreadable counts inbox files that could not be read.
	Unreadable int
	Usage      usagestate.File
	UsageKnown bool
	// PausedSessions maps a session name to its wake time text, for sessions
	// the daemon paused for usage. The text is "" when no wake time is known.
	PausedSessions map[string]string
}

// Row is one master-plan task as the Build tab shows it.
type Row struct {
	ID, Title, Status, Detail string
	// Owner is true for a task that is the owner's to do.
	Owner bool
}

// RowsInput carries what Rows needs.
type RowsInput struct {
	Tasks  []masterplan.Task
	Ledger ledger.Ledger
	// Sessions maps a session name to its phase, "" when unknown.
	Sessions map[string]string
	Items    []inbox.Item
	// Loc is the zone wake times are shown in. Nil means UTC.
	Loc *time.Location
}

// timeLayout is the wake time form the CLI uses ("Mon 2 Jan 15:04").
const timeLayout = "Mon 2 Jan 15:04"

// Rows returns one row per master-plan task in plan order. A ledger entry for
// a task the plan no longer has is not shown: the plan is the list of what the
// build is about, and the ledger only records what happened to it.
func Rows(in RowsInput) []Row {
	if len(in.Tasks) == 0 {
		return nil
	}
	rows := make([]Row, 0, len(in.Tasks))
	for _, t := range in.Tasks {
		e, ok := in.Ledger.Get(t.ID)
		if !ok {
			e = ledger.Entry{ID: t.ID, Status: ledger.Pending}
		}
		rows = append(rows, Row{
			ID:     t.ID,
			Title:  t.Title,
			Status: string(e.Status),
			Detail: detail(t, e, in),
			Owner:  t.Owner(),
		})
	}
	return rows
}

func detail(t masterplan.Task, e ledger.Entry, in RowsInput) string {
	switch e.Status {
	case ledger.Pending:
		var d string
		if unmet := nexttask.Unmet(t, in.Ledger); len(unmet) > 0 {
			d = "waiting for " + strings.Join(unmet, ", ")
		} else {
			d = "ready"
		}
		if t.Owner() {
			d += " · yours to do"
		}
		return d
	case ledger.Running:
		if e.UsagePauseAt != "" {
			d := "paused for usage"
			if w := wakeText(e.UsageWakeAt, in.Loc); w != "" {
				d += " until " + w
			}
			return join(e.Session, d)
		}
		return join(e.Session, in.Sessions[e.Session])
	case ledger.Shipping:
		return join(strings.ReplaceAll(e.Ship, "_", " "), pr(e.PR))
	case ledger.Parked:
		var open []string
		for _, id := range e.ParkedOn {
			if itemOpen(in.Items, id) {
				open = append(open, id)
			}
		}
		if len(open) == 0 {
			return "answered, starting again soon"
		}
		return "waiting on you: " + strings.Join(open, ", ")
	case ledger.Done:
		sha := e.MergedSHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		return join(pr(e.PR), sha)
	case ledger.Failed:
		if e.Attempts > 0 {
			return "attempt " + strconv.Itoa(e.Attempts)
		}
	case ledger.Superseded:
		if len(e.ReplacedBy) > 0 {
			return "replaced by " + strings.Join(e.ReplacedBy, ", ")
		}
	}
	return ""
}

// itemOpen reports whether a parked task still waits on the item. An item with
// no file counts as open: the task is parked on it, and calling it answered
// without seeing the answer would hide what the owner has to look at.
func itemOpen(items []inbox.Item, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return it.Open()
		}
	}
	return true
}

func pr(n int) string {
	if n <= 0 {
		return ""
	}
	return "PR #" + strconv.Itoa(n)
}

// join joins the non-empty parts with the dashboard's separator.
func join(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " · ")
}

// wakeText formats an RFC 3339 wake time, or "" when it is empty or does not
// parse (the ledger does not check it on load).
func wakeText(s string, loc *time.Location) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format(timeLayout)
}

// PausedSessions maps the session of each running entry that carries a usage
// pause to its wake time text ("" when none is known). Entries with no session
// are left out, since there is no row to label.
func PausedSessions(l ledger.Ledger, loc *time.Location) map[string]string {
	out := map[string]string{}
	for _, e := range l.Tasks {
		if e.Status == ledger.Running && e.UsagePauseAt != "" && e.Session != "" {
			out[e.Session] = wakeText(e.UsageWakeAt, loc)
		}
	}
	return out
}

// OpenCounts returns how many items are open and how many of those block.
func OpenCounts(items []inbox.Item) (open, blocking int) {
	for _, it := range items {
		if it.Open() {
			open++
			if it.Blocking {
				blocking++
			}
		}
	}
	return open, blocking
}
