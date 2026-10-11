package daemon

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

// The table's fixed clock and limits. Every time in a row is written as a
// distance from now, so a boundary row reads as the boundary it tests.
var (
	now     = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	started = now.Add(-time.Hour)
)

const (
	stallAfter = 10 * time.Minute
	hostGrace  = 60 * time.Second
)

// ago is the RFC 3339 text of a time d before now, the form the ledger and
// the inbox store their times in.
func ago(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

func task(id string, deps ...string) masterplan.Task {
	return masterplan.Task{ID: id, Title: "Task " + id, DependsOn: deps}
}

// running is the ledger entry of a task in flight, started an hour ago in
// the session SessionName gives its first attempt.
func running(id string) ledger.Entry {
	return ledger.Entry{
		ID: id, Status: ledger.Running, Session: SessionName(id, 1),
		Branch: SessionName(id, 1), Attempts: 1, StartedAt: ago(time.Hour),
	}
}

func entry(id string, st ledger.Status) ledger.Entry {
	return ledger.Entry{ID: id, Status: st}
}

func parked(id string, items ...string) ledger.Entry {
	e := running(id)
	e.Status = ledger.Parked
	e.ParkedOn = items
	return e
}

func led(entries ...ledger.Entry) ledger.Ledger { return ledger.Ledger{Tasks: entries} }

// view is a healthy, working session for T1.1: readable status, live host,
// status last written a second ago. Rows change the fields they test.
func view(change func(*SessionView)) *SessionView {
	v := &SessionView{
		Name: "t1-1", Phase: state.PhaseExecuting,
		UpdatedAt: now.Add(-time.Second), HostRunning: true,
	}
	if change != nil {
		change(v)
	}
	return v
}

// item is an open, non-blocking notice. The helpers below change one thing
// each, so a row says exactly what makes its item matter.
func item(id, task, sess string) inbox.Item {
	return inbox.Item{
		ID: id, CreatedAt: ago(30 * time.Minute), Task: task, Session: sess,
		Kind: "notice", Title: "Something to know", Options: []string{},
	}
}

func blocking(it inbox.Item) inbox.Item {
	it.Blocking = true
	it.Kind = "question"
	return it
}

func answered(it inbox.Item, before time.Duration) inbox.Item {
	it.Answer = "ok"
	it.AnsweredAt = ago(before)
	it.AnsweredVia = "cli"
	return it
}

func titled(it inbox.Item, prefix string) inbox.Item {
	it.Title = prefix + "T1.1 (t1-1)"
	it.Kind = "failure"
	it.Blocking = true
	return it
}

func createdAgo(it inbox.Item, before time.Duration) inbox.Item {
	it.CreatedAt = ago(before)
	return it
}

// TestDecide is the whole loop as a table: one row for each branch of Decide
// and one for each side of every limit.
//
// Load-bearing rows, and what breaks without them:
//
//   - "stall exactly at the limit waits" and "one second over stalls": the
//     comparison is "over", not "at or over". These are the rows the task's
//     mutation check turns red.
//   - "host gone exactly at the grace waits" and its one-second-over twin:
//     a host that has only just been started must not be reported dead.
//   - "awaiting input with an open blocking item parks": a parked session
//     always sits at a prompt, so a stall raised there would page the owner
//     about a question they have already been sent.
//   - "an answered item restarts the stall clock": without it the task would
//     stall on the same tick the owner's answer un-parked it.
//   - "an answered stall item means no second stall" and the host-gone twin:
//     nothing can type into a session before T2.2, so an answered item leaves
//     the session exactly as idle as before, and without the once-per-attempt
//     limit the owner would be paged every StallAfter for ever.
//   - "a stall item from an earlier attempt does not suppress a new one":
//     the limit is per attempt. A new attempt that stalls must still page.
//   - "stale status with a dead host past the grace reports host gone": an
//     unreadable status.json is never a failure, but a daemon that waits
//     silently for ever on a dead session is its worst failure.
//   - "an inbox error waits": with no items every park and every duplicate
//     check is wrong, so even a finished session is left for the next tick.
//   - "active entry missing from the plan stops" and "running entry with no
//     session name stops": both come from a hand-edited ledger, and the
//     alternative is a session created for a task that no longer exists or
//     under an empty name.
//   - "paused with a running task still watches" against "paused with a
//     running entry and no session waits" and "paused with nothing running
//     waits": pause stops every new session and nothing else.
//   - "parked then answered un-parks before a pending task launches" and "an
//     item id missing from the inbox keeps the task parked": work already
//     started goes first, and an item that cannot be read is never taken as
//     answered.
//   - "two active entries watch the first in ledger order": only a
//     hand-edited ledger has two, and the loop must still watch one session,
//     the same one every tick.
func TestDecide(t *testing.T) {
	// T1.2 builds on T1.1; T1.3 stands alone, so it is the task that can run
	// while T1.1 is parked or failed.
	plan := []masterplan.Task{task("T1.1"), task("T1.2", "T1.1"), task("T1.3")}
	chain := []masterplan.Task{task("T1.1"), task("T1.2", "T1.1")}

	cases := []struct {
		name     string
		paused   bool
		buildErr error
		inboxErr error
		tasks    []masterplan.Task // nil means plan
		ledger   ledger.Ledger
		items    []inbox.Item
		session  *SessionView
		want     Action // Reason is checked through reasonHas, not here
		// reasonHas lists text the reason must hold. Whole sentences are not
		// pinned: the rows fix what the owner must be told, not the wording.
		reasonHas []string
	}{
		// Rule 1: the project cannot be decided at all.
		{
			name:      "a build error stops",
			buildErr:  errors.New("docs/PLAN.md: line 12: T1.2 depends on T9.9, which is not a task"),
			ledger:    led(running("T1.1")),
			session:   view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:      Action{Kind: ActStop},
			reasonHas: []string{"T1.2 depends on T9.9"},
		},
		{
			name:      "a build error wins over an inbox error",
			buildErr:  errors.New("ledger file is damaged"),
			inboxErr:  errors.New("permission denied"),
			want:      Action{Kind: ActStop},
			reasonHas: []string{"ledger file is damaged"},
		},
		{
			name:      "an inbox error waits",
			inboxErr:  errors.New("open .afk/inbox: permission denied"),
			ledger:    led(running("T1.1")),
			session:   view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:      Action{Kind: ActWait},
			reasonHas: []string{"inbox", "permission denied"},
		},
		{
			name:      "an inbox error waits with nothing running too",
			inboxErr:  errors.New("permission denied"),
			want:      Action{Kind: ActWait},
			reasonHas: []string{"inbox"},
		},
		{
			name:      "active entry missing from the plan stops",
			ledger:    led(running("T9.9")),
			want:      Action{Kind: ActStop, Task: "T9.9"},
			reasonHas: []string{"T9.9", "afk ledger set"},
		},
		{
			name:      "shipping entry missing from the plan stops",
			ledger:    led(ledger.Entry{ID: "T9.9", Status: ledger.Shipping, Session: "t9-9"}),
			want:      Action{Kind: ActStop, Task: "T9.9"},
			reasonHas: []string{"T9.9", "afk ledger set"},
		},
		{
			name:      "an entry missing from the plan stops even behind a healthy one",
			ledger:    led(running("T1.1"), running("T9.9")),
			session:   view(nil),
			want:      Action{Kind: ActStop, Task: "T9.9"},
			reasonHas: []string{"T9.9"},
		},
		{
			name:      "running entry with no session name stops",
			ledger:    led(entry("T1.1", ledger.Running)),
			want:      Action{Kind: ActStop, Task: "T1.1"},
			reasonHas: []string{"afk ledger set T1.1 pending"},
		},
		{
			name:      "shipping entry with no session name stops",
			ledger:    led(entry("T1.1", ledger.Shipping)),
			want:      Action{Kind: ActStop, Task: "T1.1"},
			reasonHas: []string{"afk ledger set T1.1 pending"},
		},

		// Rule 2: which entry is watched, and shipping.
		{
			name:      "a shipping task waits to ship",
			ledger:    led(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1"}),
			session:   view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"T1.1 is waiting to ship"},
		},
		{
			name:    "two active entries watch the first in ledger order",
			ledger:  led(running("T1.1"), ledger.Entry{ID: "T1.2", Status: ledger.Shipping, Session: "t1-2"}),
			session: view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:    Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "a done entry ahead of the running one is not the active one",
			ledger:    led(entry("T1.1", ledger.Done), running("T1.3")),
			session:   view(func(v *SessionView) { v.Name = "t1-3" }),
			want:      Action{Kind: ActWait, Task: "T1.3", Session: "t1-3"},
			reasonHas: []string{"running T1.3"},
		},

		// Rule 3a: the session is not on disk.
		{
			name:   "a running task with no session on disk creates it again",
			ledger: led(running("T1.1")),
			want:   Action{Kind: ActCreate, Task: "T1.1", Session: "t1-1"},
		},
		{
			name: "create uses the recorded session, not a new attempt",
			ledger: led(ledger.Entry{
				ID: "T1.1", Status: ledger.Running, Session: "t1-1-3", Attempts: 3, StartedAt: ago(time.Hour),
			}),
			want: Action{Kind: ActCreate, Task: "T1.1", Session: "t1-1-3"},
		},
		{
			name:      "paused with a running entry and no session waits",
			paused:    true,
			ledger:    led(running("T1.1")),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"paused", "T1.1 has no session"},
		},

		// Rule 3b: status.json cannot be read.
		{
			name:      "stale status with a live host waits",
			ledger:    led(running("T1.1")),
			session:   view(func(v *SessionView) { v.Stale = true; v.Phase = ""; v.UpdatedAt = time.Time{} }),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"T1.1"},
		},
		{
			// The phase of a stale view is whatever was last parsed, or
			// nothing; it must never be acted on.
			name:    "stale status is not read for its phase",
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.Stale = true; v.Phase = state.PhaseFailed }),
			want:    Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name: "stale status with a dead host inside the grace waits",
			ledger: led(ledger.Entry{
				ID: "T1.1", Status: ledger.Running, Session: "t1-1", StartedAt: ago(hostGrace),
			}),
			session: view(func(v *SessionView) { v.Stale = true; v.HostRunning = false }),
			want:    Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name: "stale status with a dead host past the grace reports host gone",
			ledger: led(ledger.Entry{
				ID: "T1.1", Status: ledger.Running, Session: "t1-1", StartedAt: ago(hostGrace + time.Second),
			}),
			session: view(func(v *SessionView) { v.Stale = true; v.HostRunning = false }),
			want:    Action{Kind: ActHostGone, Task: "T1.1", Session: "t1-1"},
		},
		{
			// The stale branch measures from the entry's start, not from the
			// status file's time: that file is the thing that cannot be read.
			name:    "stale status with a dead host ignores the status time",
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.Stale = true; v.HostRunning = false; v.UpdatedAt = now }),
			want:    Action{Kind: ActHostGone, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "stale status with a dead host already reported waits",
			ledger:    led(running("T1.1")),
			items:     []inbox.Item{answered(titled(item("hg1", "T1.1", "t1-1"), HostGoneTitlePrefix), 20*time.Minute)},
			session:   view(func(v *SessionView) { v.Stale = true; v.HostRunning = false }),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"already reported"},
		},
		{
			// A hand-set entry can lack a start time. Nothing can then show
			// the host is new, so it is reported once instead of never.
			name:    "a dead host with no start time on the entry is reported",
			ledger:  led(ledger.Entry{ID: "T1.1", Status: ledger.Running, Session: "t1-1"}),
			session: view(func(v *SessionView) { v.Stale = true; v.HostRunning = false }),
			want:    Action{Kind: ActHostGone, Task: "T1.1", Session: "t1-1"},
		},

		// Rules 3c and 3d: the session ended.
		{
			name:    "phase done ships",
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:    Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "phase failed fails",
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.Phase = state.PhaseFailed }),
			want:    Action{Kind: ActFail, Task: "T1.1", Session: "t1-1"},
		},
		{
			// A finished session's host exits and its last question may
			// still be open; neither holds a finished task back.
			name:   "phase done wins over a blocking item and a dead host",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(func(v *SessionView) {
				v.Phase = state.PhaseDone
				v.HostRunning = false
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "phase failed wins over a dead host",
			ledger: led(running("T1.1")),
			session: view(func(v *SessionView) {
				v.Phase = state.PhaseFailed
				v.HostRunning = false
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActFail, Task: "T1.1", Session: "t1-1"},
		},

		// Rule 3e: park.
		{
			name:    "an open blocking item for the task parks",
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", ""))},
			session: view(nil),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			name:    "an open blocking item for the session parks",
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "", "t1-1"))},
			session: view(nil),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			name:   "park names every matching item and no other",
			ledger: led(running("T1.1")),
			items: []inbox.Item{
				blocking(item("q1", "T1.1", "t1-1")),
				blocking(item("q2", "T1.3", "t1-3")),
				item("q3", "T1.1", "t1-1"),
				answered(blocking(item("q4", "T1.1", "t1-1")), 20*time.Minute),
				blocking(item("q5", "", "t1-1")),
				blocking(item("q6", "", "")),
			},
			session: view(nil),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1", "q5"}},
		},
		{
			name:      "an open item that is not blocking does not park",
			ledger:    led(running("T1.1")),
			items:     []inbox.Item{item("q1", "T1.1", "t1-1")},
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:    "a blocking item for another task does not park",
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "T1.3", "t1-3")), blocking(item("q2", "", ""))},
			session: view(nil),
			want:    Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "an answered blocking item does not park",
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			session: view(nil),
			want:    Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "awaiting input with an open blocking item parks instead of stalling",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			name:   "a dead host with an open blocking item parks instead of reporting",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(func(v *SessionView) {
				v.HostRunning = false
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},

		// Rule 3f: the host is gone.
		{
			name:   "host gone exactly at the grace waits",
			ledger: led(running("T1.1")),
			session: view(func(v *SessionView) {
				v.HostRunning = false
				v.UpdatedAt = now.Add(-hostGrace)
			}),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:   "host gone one second over the grace is reported",
			ledger: led(running("T1.1")),
			session: view(func(v *SessionView) {
				v.HostRunning = false
				v.UpdatedAt = now.Add(-hostGrace - time.Second)
			}),
			want: Action{Kind: ActHostGone, Task: "T1.1", Session: "t1-1"},
		},
		{
			// The status time cannot be read (the zero time), so the entry's
			// start is what the grace is measured from.
			name: "host gone is measured from the start when the status has no time",
			ledger: led(ledger.Entry{
				ID: "T1.1", Status: ledger.Running, Session: "t1-1", StartedAt: ago(hostGrace),
			}),
			session: view(func(v *SessionView) { v.HostRunning = false; v.UpdatedAt = time.Time{} }),
			want:    Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "an answered host-gone item means no second one",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{answered(titled(item("hg1", "T1.1", "t1-1"), HostGoneTitlePrefix), 20*time.Minute)},
			session: view(func(v *SessionView) {
				v.HostRunning = false
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"already reported", "resume the session"},
		},
		{
			// A dead host does not ask anything, whatever the last status
			// said: the host-gone report already covers it.
			name:   "a reported dead host that was awaiting input does not also stall",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{answered(titled(item("hg1", "T1.1", "t1-1"), HostGoneTitlePrefix), 20*time.Minute)},
			session: view(func(v *SessionView) {
				v.HostRunning = false
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"already reported"},
		},
		{
			name:   "an answered stall item does not hide a dead host",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{answered(titled(item("st1", "T1.1", "t1-1"), StallTitlePrefix), 20*time.Minute)},
			session: view(func(v *SessionView) {
				v.HostRunning = false
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActHostGone, Task: "T1.1", Session: "t1-1"},
		},

		// Rule 3g: the session is asking something.
		{
			name:   "stall exactly at the limit waits",
			ledger: led(running("T1.1")),
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-stallAfter)
			}),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:   "one second over the limit stalls",
			ledger: led(running("T1.1")),
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-stallAfter - time.Second)
			}),
			want: Action{Kind: ActStall, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "a quiet session that is not awaiting input does not stall",
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.UpdatedAt = now.Add(-time.Hour) }),
			want:    Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			// A session that asked something the moment it started: the
			// status time is unreadable, so the clock starts at the entry.
			name: "stall is measured from the start when the status has no time",
			ledger: led(ledger.Entry{
				ID: "T1.1", Status: ledger.Running, Session: "t1-1", StartedAt: ago(stallAfter),
			}),
			session: view(func(v *SessionView) { v.AwaitingInput = true; v.UpdatedAt = time.Time{} }),
			want:    Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "an answered item restarts the stall clock",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "")), stallAfter)},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:   "an item answered for the session restarts the stall clock too",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{answered(blocking(item("q1", "", "t1-1")), stallAfter)},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "the restarted stall clock runs out again",
			ledger: led(running("T1.1")),
			items: []inbox.Item{
				answered(blocking(item("q0", "T1.1", "")), 40*time.Minute),
				answered(blocking(item("q1", "T1.1", "")), stallAfter+time.Second),
				// Another task's answer is not this session's business.
				answered(blocking(item("q2", "T1.3", "t1-3")), time.Minute),
			},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActStall, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "an answered stall item means no second stall",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{answered(titled(item("st1", "T1.1", "t1-1"), StallTitlePrefix), 20*time.Minute)},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"already reported", "resume the session"},
		},
		{
			name:   "a stall item from an earlier attempt does not suppress a new one",
			ledger: led(running("T1.1")),
			items: []inbox.Item{answered(
				createdAgo(titled(item("st1", "T1.1", "t1-1"), StallTitlePrefix), time.Hour+time.Second),
				time.Hour+time.Second,
			)},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActStall, Task: "T1.1", Session: "t1-1"},
		},
		{
			// Times are stored to the second, so an item raised in the same
			// second the attempt began belongs to this attempt.
			name:   "a stall item created exactly at the start suppresses",
			ledger: led(running("T1.1")),
			items: []inbox.Item{answered(
				createdAgo(titled(item("st1", "T1.1", "t1-1"), StallTitlePrefix), time.Hour),
				50*time.Minute,
			)},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"already reported"},
		},
		{
			name:   "a stall item for another task does not suppress",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{answered(titled(item("st1", "T1.3", "t1-3"), StallTitlePrefix), 20*time.Minute)},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActStall, Task: "T1.1", Session: "t1-1"},
		},
		{
			// The host was started again after its report was answered and
			// the agent now sits at a prompt: that is a new thing to say.
			name:   "an answered host-gone item does not suppress a stall",
			ledger: led(running("T1.1")),
			items:  []inbox.Item{answered(titled(item("hg1", "T1.1", "t1-1"), HostGoneTitlePrefix), 20*time.Minute)},
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want: Action{Kind: ActStall, Task: "T1.1", Session: "t1-1"},
		},

		// Rule 3h.
		{
			name:      "a working session waits and names its phase",
			ledger:    led(running("T1.1")),
			session:   view(func(v *SessionView) { v.Phase = state.PhaseReviewing }),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1", "reviewing"},
		},

		// Pause.
		{
			name:    "paused with a running task still watches",
			paused:  true,
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:    Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "paused with nothing running waits",
			paused:    true,
			want:      Action{Kind: ActWait},
			reasonHas: []string{"paused"},
		},
		{
			name:      "paused does not un-park an answered task",
			paused:    true,
			ledger:    led(parked("T1.1", "q1")),
			items:     []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			want:      Action{Kind: ActWait},
			reasonHas: []string{"paused"},
		},

		// Rule 5: nothing is in flight.
		{
			name: "an empty ledger launches the first task",
			want: Action{Kind: ActLaunch, Task: "T1.1"},
		},
		{
			name:   "launch picks the first task whose dependencies are done",
			ledger: led(entry("T1.1", ledger.Done)),
			want:   Action{Kind: ActLaunch, Task: "T1.2"},
		},
		{
			// A failed task is launched again only by the owner setting it
			// back to pending; the loop moves on to what does not need it.
			name:   "a failed task is passed over for an independent one",
			ledger: led(ledger.Entry{ID: "T1.1", Status: ledger.Failed, Session: "t1-1", Attempts: 1}),
			want:   Action{Kind: ActLaunch, Task: "T1.3"},
		},
		{
			name:   "parked then answered un-parks before a pending task launches",
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "a task parked on an open item lets an independent task launch",
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			want:   Action{Kind: ActLaunch, Task: "T1.3"},
		},
		{
			name:      "a task parked on an open item with nothing else ready is idle",
			tasks:     chain,
			ledger:    led(parked("T1.1", "q1", "q2")),
			items:     []inbox.Item{blocking(item("q1", "T1.1", "t1-1")), answered(blocking(item("q2", "T1.1", "t1-1")), time.Minute)},
			want:      Action{Kind: ActIdle, Items: []string{"q1"}},
			reasonHas: []string{"waiting on owner", "T1.1", "q1"},
		},
		{
			name:      "an item id missing from the inbox keeps the task parked",
			tasks:     chain,
			ledger:    led(parked("T1.1", "gone")),
			want:      Action{Kind: ActIdle, Items: []string{"gone"}},
			reasonHas: []string{"waiting on owner", "T1.1", "gone"},
		},
		{
			name:      "every task done is idle",
			ledger:    led(entry("T1.1", ledger.Done), entry("T1.2", ledger.Done), entry("T1.3", ledger.Done)),
			want:      Action{Kind: ActIdle},
			reasonHas: []string{"all done"},
		},
		{
			name:      "what is left depending on a failed task is idle and names it",
			tasks:     chain,
			ledger:    led(entry("T1.1", ledger.Failed)),
			want:      Action{Kind: ActIdle, Items: []string{"T1.1"}},
			reasonHas: []string{"blocked by failed", "T1.1", "afk ledger set T1.1 pending"},
		},
		{
			// Superseded with no replacement can never be satisfied, and
			// nothing running, parked or failed explains the standstill.
			name:      "pending tasks that can never start are idle and named",
			tasks:     chain,
			ledger:    led(entry("T1.1", ledger.Superseded)),
			want:      Action{Kind: ActIdle},
			reasonHas: []string{"stuck", "T1.2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := tc.tasks
			if tasks == nil {
				tasks = plan
			}
			got := Decide(Observation{
				Now: now, InboxErr: tc.inboxErr,
				StallAfter: stallAfter, HostGrace: hostGrace,
				Paused: tc.paused, BuildErr: tc.buildErr,
				Tasks: tasks, Ledger: tc.ledger, Items: tc.items, Session: tc.session,
			})
			if got.Kind != tc.want.Kind || got.Task != tc.want.Task || got.Session != tc.want.Session {
				t.Errorf("Decide = {%s task=%q session=%q}, want {%s task=%q session=%q}\nreason: %s",
					got.Kind, got.Task, got.Session, tc.want.Kind, tc.want.Task, tc.want.Session, got.Reason)
			}
			if !slices.Equal(got.Items, tc.want.Items) {
				t.Errorf("Items = %v, want %v", got.Items, tc.want.Items)
			}
			// Every action is shown by `afk daemon status`, so none may come
			// back without a sentence.
			if strings.TrimSpace(got.Reason) == "" {
				t.Errorf("Reason is empty for %s", got.Kind)
			}
			for _, want := range tc.reasonHas {
				if !strings.Contains(got.Reason, want) {
					t.Errorf("Reason = %q, want it to hold %q", got.Reason, want)
				}
			}
		})
	}
}

// TestDecideDoesNotChangeItsInput guards the promise that Decide is pure in
// the other direction too: the executor hands it the ledger and items it is
// about to act on, and a decision that reordered or trimmed them would
// change what the same tick then writes.
func TestDecideDoesNotChangeItsInput(t *testing.T) {
	l := led(parked("T1.1", "q2", "q1"), entry("T1.3", ledger.Done))
	items := []inbox.Item{blocking(item("q2", "T1.1", "t1-1")), blocking(item("q1", "T1.1", "t1-1"))}
	Decide(Observation{
		Now: now, StallAfter: stallAfter, HostGrace: hostGrace,
		Tasks: []masterplan.Task{task("T1.1"), task("T1.2", "T1.1")}, Ledger: l, Items: items,
	})
	if got := l.Tasks[0].ParkedOn; !slices.Equal(got, []string{"q2", "q1"}) {
		t.Errorf("ledger ParkedOn was reordered to %v", got)
	}
	if items[0].ID != "q2" || items[1].ID != "q1" {
		t.Errorf("items were reordered: %s, %s", items[0].ID, items[1].ID)
	}
}

// TestTitlePrefixesAreDistinct pins the one property the once-per-attempt
// limit rests on: a stall item must never be read as a host-gone report or
// the other way round, which is what would happen if one prefix began with
// the other.
func TestTitlePrefixesAreDistinct(t *testing.T) {
	if StallTitlePrefix == "" || HostGoneTitlePrefix == "" {
		t.Fatal("a title prefix is empty; every item would match it")
	}
	if strings.HasPrefix(StallTitlePrefix, HostGoneTitlePrefix) || strings.HasPrefix(HostGoneTitlePrefix, StallTitlePrefix) {
		t.Errorf("prefixes overlap: %q and %q", StallTitlePrefix, HostGoneTitlePrefix)
	}
}

// TestSessionName pins the session a task attempt runs in.
//
// Load-bearing rows: the lettered id (T4.2a is a real id form in the master
// plan, and its letter must survive), attempt 1 having no suffix (PLAN §5.3
// names the branch afk/t4-3), and attempts 2 and 10 (a later attempt must
// not reuse the branch and worktree the earlier one left behind, and the
// suffix is a number, not a single digit). Every row is also run through
// session.ValidateName, because a name that fails there fails only at launch,
// after the ledger already says running.
func TestSessionName(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		attempt int
		want    string
	}{
		{"first attempt has no suffix", "T4.3", 1, "t4-3"},
		{"lettered id keeps its letter", "T4.2a", 1, "t4-2a"},
		{"second attempt", "T4.3", 2, "t4-3-2"},
		{"tenth attempt", "T4.3", 10, "t4-3-10"},
		{"lettered id on a later attempt", "T4.2a", 2, "t4-2a-2"},
		{"two-digit milestone and task", "T12.34", 1, "t12-34"},
		// An attempt below 1 is no attempt at all; it names the first.
		{"attempt zero is the first attempt", "T4.3", 0, "t4-3"},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SessionName(tc.id, tc.attempt)
			if got != tc.want {
				t.Errorf("SessionName(%q, %d) = %q, want %q", tc.id, tc.attempt, got, tc.want)
			}
			if err := session.ValidateName(got); err != nil {
				t.Errorf("SessionName(%q, %d) = %q is not a valid session name: %v", tc.id, tc.attempt, got, err)
			}
			if tc.attempt >= 1 {
				key := fmt.Sprintf("%s attempt %d", tc.id, tc.attempt)
				if other, dup := seen[got]; dup && other != key {
					t.Errorf("%q names both %s and %s", got, other, key)
				}
				seen[got] = key
			}
		})
	}
}
