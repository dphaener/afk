package daemon

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/governor"
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

	// The recovery limits of the rows that switch recovery on (see
	// recovering). They are the shipped defaults, so a row reads as the
	// schedule the owner gets.
	maxRestarts    = 3
	restartBackoff = time.Minute
	continueAfter  = 30 * time.Second
	wedgeConfirm   = 60 * time.Second

	// The usage limits of the rows that switch the governor on (see
	// governed). They are the shipped defaults as well.
	pauseRetry        = time.Minute
	rateLimitFallback = time.Hour
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

// ownerTask is a task whose title carries the owner marker, in the form the
// master plan uses for T0.0. An agent must never be started for one.
func ownerTask(id string, deps ...string) masterplan.Task {
	return masterplan.Task{ID: id, Title: "Stand up the VM (owner; not an agent task)", DependsOn: deps}
}

// ownerParked is an owner task as the loop leaves it: parked on its item,
// with no session, no branch and no attempt, because none was ever started.
func ownerParked(id string, items ...string) ledger.Entry {
	return ledger.Entry{ID: id, Status: ledger.Parked, ParkedOn: items}
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

// shipParked is a task parked in the middle of its ship: it holds the ship
// state it had reached.
func shipParked(id, shipState string, items ...string) ledger.Entry {
	e := parked(id, items...)
	e.Ship, e.ShipAt, e.PR = shipState, ago(10*time.Minute), 7
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

// recovering is an Observation change for a daemon with every means of
// recovery wired and the limits above. Rows change the fields they test. A
// row without it runs as the loop did before recovery existed: no means, so
// a gone host is reported and a wedge is left alone.
func recovering(change func(*Observation)) func(*Observation) {
	return func(o *Observation) {
		o.MaxRestarts, o.RestartBackoff = maxRestarts, restartBackoff
		o.ContinueAfter, o.WedgeConfirm = continueAfter, wedgeConfirm
		o.CanRestart, o.CanRelaunch, o.CanPrompt = true, true, true
		if change != nil {
			change(o)
		}
	}
}

// recovered is a running entry that has been recovered restarts times, the
// last of them before ago, and may still be waiting to be told to carry on.
func recovered(id string, restarts int, before time.Duration, continuePending bool) ledger.Entry {
	e := running(id)
	e.Restarts, e.RestartAt, e.ContinuePending = restarts, ago(before), continuePending
	return e
}

// wedged is a view change: the host is running and has said for d that its
// agent is not answering.
func wedged(d time.Duration) func(*SessionView) {
	return func(v *SessionView) { v.WedgedSince = now.Add(-d) }
}

// gone is a view change: the host is not running and the session's last sign
// of life was an hour ago, far past the grace.
func gone(v *SessionView) {
	v.HostRunning = false
	v.UpdatedAt = now.Add(-time.Hour)
}

// stale is a view change: status.json cannot be read.
func stale(v *SessionView) {
	v.Stale = true
	v.Phase = ""
	v.UpdatedAt = time.Time{}
}

// both applies two view changes in order.
func both(a, b func(*SessionView)) func(*SessionView) {
	return func(v *SessionView) { a(v); b(v) }
}

// governed is an Observation change for a daemon with a usage source that
// said action on a reading taken this tick, the means to stop a session, and
// every means of recovery (a pause needs the relaunch, and the rows must show
// that recovery, though wired, is not what acts). Rows change the fields they
// test. A row without it runs as the loop did before the governor existed.
func governed(action governor.Action, change func(*Observation)) func(*Observation) {
	return recovering(func(o *Observation) {
		o.Usage = UsageView{Consulted: true, Action: action, Reason: "a sentence with 91% in it", At: now}
		o.CanPause, o.PauseRetry, o.RateLimitFallback = true, pauseRetry, rateLimitFallback
		if change != nil {
			change(o)
		}
	})
}

// unconsulted is a daemon with every means wired whose usage source was not
// asked on this tick. The view still carries action, so a row shows that the
// word is ignored and not merely absent.
func unconsulted(action governor.Action) func(*Observation) {
	return governed(action, func(o *Observation) { o.Usage.Consulted = false })
}

// loggedOut and loggedIn are Observation changes for a known login verdict.
func loggedOut(o *Observation) { o.Login = LoginView{Known: true} }
func loggedIn(o *Observation)  { o.Login = LoginView{Known: true, LoggedIn: true} }

// usagePaused is a running entry whose session was stopped for usage before
// ago. wake is the saved wake time as the ledger holds it; "" is none.
func usagePaused(id string, before time.Duration, wake string) ledger.Entry {
	e := running(id)
	e.UsagePauseAt, e.UsageWakeAt = ago(before), wake
	return e
}

// rateLimited is a view change: the session's rate-limit marker carries a
// time d before now.
func rateLimited(d time.Duration) func(*SessionView) {
	return func(v *SessionView) { v.RateLimitedAt = now.Add(-d) }
}

// stopped is a view change: the host is not running, as after a pause. The
// status time is left recent, so a row that reaches recovery by mistake is
// caught by the gone rows and not by this one.
func stopped(v *SessionView) { v.HostRunning = false }

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
//     the resume command typed on an un-park is one attempt that may not
//     land, so an answered item can leave the session exactly as idle as
//     before, and without the once-per-attempt limit the owner would be
//     paged every StallAfter for ever.
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
//   - "no ledger file starts nothing": with no file every task reads as
//     pending, and the first run of the daemon on a part-built project
//     started its first task again. The row beside it, "an empty ledger
//     launches the first task", is the other half: an empty ledger that
//     exists is the owner saying the project is new.
//   - "a pending owner task is handed to the owner" against "a task marked
//     owner + agent launches": the first is the bug this rule fixes (an agent
//     was started for the owner's VM checklist), the second is the rule not
//     reaching too far and leaving agent work undone.
//   - "pause lets an owner task be raised" and "pause lets an answered owner
//     task finish" against "a stopped line holds back an owner task" and "a
//     stopped line holds back an answered owner task": the two flags differ
//     on exactly these, and swapping the order of the checks in Decide turns
//     one pair or the other red.
//   - "a stopped line still watches a running session" and "a shipping task
//     still ships on a stopped line": the stop holds back new work only. If
//     it came first, the task whose deploy stopped the line could never be
//     finished after `afk resume` (its own ship is what sees the marker go),
//     and a session already paid for would sit unwatched.
//   - "a task parked before its ship saved anything goes back to shipping"
//     against "a task parked on a question goes back to running": the entry
//     alone cannot tell these apart (neither has a ship state), only the
//     parked session's phase can. Sent to running, the first would be handed
//     to shipping a second time, with its tokens recorded and its finish
//     logged twice. "a stale view of a parked session is not read as done"
//     is the guard on the other side.
//   - "a task parked in the middle of its ship goes back to shipping with
//     no session on disk": the ship state alone decides, so a session that
//     was cleaned up cannot send a merged task back to running, which would
//     create its session again and build the task a second time.
//   - "an answered task is not un-parked while the sessions cannot be read":
//     with no view a finished session reads as a working one.
//   - "a stopped line with nothing running waits": the only row whose state
//     is stored as line_stopped with no task, which is what `afk daemon
//     status` shows while the owner looks at the deploy.
//   - "wedged for less than the confirm time waits" against "wedged for the
//     confirm time is restarted": the host's verdict clears on any output,
//     and a restart stops a working agent. Without the first row a moment's
//     false verdict costs a task its turn.
//   - "restarts at the cap and wedged gives up", "restarts at the cap and
//     host gone gives up" and the two "max restarts 0" rows: the cap is the
//     only thing between a session that always hangs and a restart loop that
//     never tells the owner. Zero is a real setting and must give up at
//     once, not restart once.
//   - "wedged inside the backoff waits" against "wedged exactly at the
//     backoff is restarted": the backoff is "at or over" on purpose, unlike
//     the grace and the stall limit, which are "over". Those guard against
//     acting on a reading that may still change; the backoff is a wait the
//     loop set itself and has served in full when it is reached.
//   - "host gone inside the grace measured from the relaunch waits": without
//     restart_at in the grace, the tick after a relaunch would find the old
//     status time, call the new host gone and relaunch it again, burning the
//     whole cap in three ticks.
//   - "phase done with a gone host ships and is not relaunched" and its
//     wedged and failed twins: recovery moved above the done/failed check
//     would resume a conversation whose work is finished. This is the row
//     the mutation check turns red.
//   - "an open blocking item with a wedged host parks" and "stale with an
//     open blocking item and a wedged host parks": a parked session sits at
//     a prompt by design, and restarting it throws the question away.
//   - "continue pending but wedged takes the restart path": a line typed
//     into an agent that is not answering goes nowhere and would be recorded
//     as delivered.
//   - "host gone without the means to relaunch is reported as before" and
//     "wedged without the means to restart waits": a daemon wired without
//     recovery must behave exactly as it did, and must never choose an
//     action nothing can carry out.
//   - "paused project with a wedged running session is restarted": pause
//     stops new work, and a running task carries on under it.
//   - "usage paused with the host gone past the grace is not relaunched" and
//     its at-the-cap twin: a paused session's host is gone on purpose. If the
//     usage rule sat below recovery the session would be started again inside
//     the limit it was stopped for, and then given up on. This is the row the
//     task's mutation check turns red.
//   - Every "unconsulted" row: a daemon with no usage source, or a tick that
//     did not ask, must launch, create, un-park and watch exactly as before.
//     "a fresh rate-limit marker with usage unconsulted does nothing" is the
//     sharpest of them: with no usage source nothing could ever resume the
//     session, so stopping it would be stopping it for good.
//   - "usage paused does not resume on a run reading taken before the pause":
//     a session stopped by its marker is stopped while the last reading still
//     says run. Without the row that reading resumes it on the next tick, it
//     hits the limit again, and the two go round every half minute.
//   - "usage paused never resumes while its host is running": a resume
//     starts a new host, and two hosts for one session is two agents.
//   - "no_launch does not hold an un-park" against "hold holds an un-park":
//     a parked session is a running agent at a prompt, and PLAN §6.3 lets a
//     running session carry on at no_launch and at no other level.
//   - "refuse does not hold an un-park back to shipping": a ship starts no
//     agent, and a merged task that cannot be marked done blocks the line.
//   - "an owner pause keeps its own wording under a usage hold": the owner
//     must be sent to `afk resume`, not to `afk usage`.
//   - "usage paused before its wake waits with usage unconsulted": the
//     executor does not consult during a known wait, so the "paused until"
//     answer must not need a reading.
func TestDecide(t *testing.T) {
	// T1.2 builds on T1.1; T1.3 stands alone, so it is the task that can run
	// while T1.1 is parked or failed.
	plan := []masterplan.Task{task("T1.1"), task("T1.2", "T1.1"), task("T1.3")}
	chain := []masterplan.Task{task("T1.1"), task("T1.2", "T1.1")}

	cases := []struct {
		name     string
		paused   bool
		noLedger bool
		// stopped is the stop marker; stopReason is the text it carries.
		stopped    bool
		stopReason string
		buildErr   error
		inboxErr   error
		tasks      []masterplan.Task // nil means plan
		ledger     ledger.Ledger
		items      []inbox.Item
		session    *SessionView
		// parked is the session of each parked task, by task id; parkedErr
		// stands for a listing that failed.
		parked    map[string]*SessionView
		parkedErr error
		// obs changes the Observation after it is built; nil leaves recovery
		// switched off, as it is for every row written before recovery.
		obs  func(*Observation)
		want Action // Reason is checked through reasonHas, not here
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
			name:      "a shipping task takes a ship step",
			ledger:    led(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1"}),
			session:   view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:      Action{Kind: ActShipStep, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"shipping T1.1"},
		},
		{
			// Cleaned up, then killed before the ledger said done: the ship
			// still has to be asked, so it can say done.
			name:   "a shipping task with no session on disk still takes a ship step",
			ledger: led(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Ship: "deploy_healthy"}),
			want:   Action{Kind: ActShipStep, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "a paused shipping task still ships",
			paused: true,
			ledger: led(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Ship: "pr_open"}),
			want:   Action{Kind: ActShipStep, Task: "T1.1", Session: "t1-1"},
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
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Running},
		},
		{
			name:      "a task parked on a question goes back to running",
			ledger:    led(parked("T1.1", "q1")),
			items:     []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked:    map[string]*SessionView{"T1.1": view(func(v *SessionView) { v.AwaitingInput = true })},
			want:      Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Running},
			reasonHas: []string{"running again"},
		},
		{
			name:      "a task parked in the middle of its ship goes back to shipping",
			ledger:    led(shipParked("T1.1", "pr_open", "q1")),
			items:     []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked:    map[string]*SessionView{"T1.1": view(func(v *SessionView) { v.Phase = state.PhaseDone })},
			want:      Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Shipping},
			reasonHas: []string{"shipping again"},
		},
		{
			name:   "a task parked in the middle of its ship goes back to shipping with no session on disk",
			ledger: led(shipParked("T1.1", "deploy_healthy", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Shipping},
		},
		{
			// The park came from a limit (two hand-backs used up). The
			// answer sends it back to shipping; the executor is what gives
			// it a fresh clock and fresh counters.
			name: "an answered ship park at its limit goes back to shipping for a fresh try",
			ledger: led(func() ledger.Entry {
				e := shipParked("T1.1", "pr_open", "q1")
				e.CIReruns, e.CIFixCycles, e.ShipAt = 1, 2, ago(2*time.Hour)
				return e
			}()),
			items: []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			want:  Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Shipping},
		},
		{
			// GitHub's tool was not logged in: the ship parked before it
			// saved a state, so only the session's phase says it had finished.
			name:   "a task parked before its ship saved anything goes back to shipping",
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(func(v *SessionView) { v.Phase = state.PhaseDone })},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Shipping},
		},
		{
			name:   "a parked task with no ship state and no session on disk goes back to running",
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Running},
		},
		{
			name:   "a stale view of a parked session is not read as done",
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(func(v *SessionView) { v.Stale = true; v.Phase = state.PhaseDone })},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Running},
		},
		{
			name:   "another task's finished session does not send this one to shipping",
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.3": view(func(v *SessionView) { v.Name = "t1-3"; v.Phase = state.PhaseDone })},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Running},
		},
		{
			name:      "an answered task is not un-parked while the sessions cannot be read",
			ledger:    led(parked("T1.1", "q1")),
			items:     []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parkedErr: errors.New("git worktree list: exit status 128"),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"sessions could not be read", "exit status 128"},
		},
		{
			// The listing is needed for an un-park only.
			name:      "unreadable sessions do not hold back a launch",
			ledger:    led(parked("T1.1", "q1")),
			items:     []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			parkedErr: errors.New("git worktree list: exit status 128"),
			want:      Action{Kind: ActLaunch, Task: "T1.3"},
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

		// No ledger file.
		{
			name:      "no ledger file starts nothing",
			noLedger:  true,
			want:      Action{Kind: ActNoLedger},
			reasonHas: []string{"waiting on the owner", "afk ledger init", "--done-through"},
		},
		{
			name:     "no ledger file starts nothing for an owner task either",
			noLedger: true,
			tasks:    []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			want:     Action{Kind: ActNoLedger},
		},
		{
			name:      "a build error wins over no ledger file",
			noLedger:  true,
			buildErr:  errors.New("the master plan has a problem"),
			want:      Action{Kind: ActStop},
			reasonHas: []string{"the master plan has a problem"},
		},
		{
			// Without the items the executor cannot tell whether it already
			// said so, and would say it again on every tick.
			name:      "an inbox error wins over no ledger file",
			noLedger:  true,
			inboxErr:  errors.New("permission denied"),
			want:      Action{Kind: ActWait},
			reasonHas: []string{"inbox"},
		},
		{
			// Pause and a stopped line both mean "start nothing", which a
			// missing ledger already does; the owner still has to be told
			// the project cannot be built.
			name:     "no ledger file is said even when paused and stopped",
			noLedger: true,
			paused:   true,
			stopped:  true,
			want:     Action{Kind: ActNoLedger},
		},

		// Owner tasks.
		{
			name:      "a pending owner task is handed to the owner",
			tasks:     []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			want:      Action{Kind: ActOwner, Task: "T0.0"},
			reasonHas: []string{"waiting on the owner", "T0.0", "your task"},
		},
		{
			name: "a task marked owner + agent launches",
			tasks: []masterplan.Task{
				{ID: "T0.1", Title: "Wire up the deploy key (owner + agent)"},
				task("T1.1", "T0.1"),
			},
			want: Action{Kind: ActLaunch, Task: "T0.1"},
		},
		{
			name:   "an owner task is handed over in its place in the plan, not before",
			tasks:  []masterplan.Task{task("T1.1"), ownerTask("T1.2", "T1.1"), task("T1.3")},
			ledger: led(entry("T1.1", ledger.Done)),
			want:   Action{Kind: ActOwner, Task: "T1.2"},
		},
		{
			name:  "an owner task whose dependency is not done waits its turn",
			tasks: []masterplan.Task{task("T1.1"), ownerTask("T1.2", "T1.1")},
			want:  Action{Kind: ActLaunch, Task: "T1.1"},
		},
		{
			name:   "an answered owner task is done",
			tasks:  []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			ledger: led(ownerParked("T0.0", "o1")),
			items:  []inbox.Item{answered(blocking(item("o1", "T0.0", "")), time.Minute)},
			want:   Action{Kind: ActOwnerDone, Task: "T0.0"},
		},
		{
			name:      "an owner task whose item is open keeps what depends on it waiting",
			tasks:     []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			ledger:    led(ownerParked("T0.0", "o1")),
			items:     []inbox.Item{blocking(item("o1", "T0.0", ""))},
			want:      Action{Kind: ActIdle, Items: []string{"o1"}},
			reasonHas: []string{"waiting on owner", "T0.0", "o1"},
		},
		{
			name:   "an owner task whose item is open lets an independent task launch",
			tasks:  []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0"), task("T1.3")},
			ledger: led(ownerParked("T0.0", "o1")),
			items:  []inbox.Item{blocking(item("o1", "T0.0", ""))},
			want:   Action{Kind: ActLaunch, Task: "T1.3"},
		},
		{
			// An item that cannot be found cannot be shown to be answered.
			name:      "an owner task parked on a missing item is not done",
			tasks:     []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			ledger:    led(ownerParked("T0.0", "gone")),
			want:      Action{Kind: ActIdle, Items: []string{"gone"}},
			reasonHas: []string{"waiting on owner"},
		},
		{
			name:   "pause lets an owner task be raised",
			paused: true,
			tasks:  []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			want:   Action{Kind: ActOwner, Task: "T0.0"},
		},
		{
			name:   "pause lets an answered owner task finish",
			paused: true,
			tasks:  []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			ledger: led(ownerParked("T0.0", "o1")),
			items:  []inbox.Item{answered(blocking(item("o1", "T0.0", "")), time.Minute)},
			want:   Action{Kind: ActOwnerDone, Task: "T0.0"},
		},
		{
			// The owner task is done, so what is next is an ordinary launch,
			// and pause holds that back as it always did.
			name:      "pause still holds back the task after an owner task",
			paused:    true,
			tasks:     []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			ledger:    led(entry("T0.0", ledger.Done)),
			want:      Action{Kind: ActWait},
			reasonHas: []string{"paused"},
		},

		// The stopped line.
		{
			name:       "a stopped line with nothing running waits",
			stopped:    true,
			stopReason: "the deploy of T1.1 did not become healthy",
			ledger:     led(entry("T1.1", ledger.Done)),
			want:       Action{Kind: ActWait, LineStopped: true},
			reasonHas:  []string{"the line is stopped", "the deploy of T1.1 did not become healthy", "afk resume"},
		},
		{
			// A damaged marker has no reason to give and still stops.
			name:      "a stopped line with no reason still says how to clear it",
			stopped:   true,
			want:      Action{Kind: ActWait, LineStopped: true},
			reasonHas: []string{"the line is stopped", "afk resume"},
		},
		{
			name:    "a stopped line still watches a running session",
			stopped: true,
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:    Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "a stopped line leaves a working session to work",
			stopped:   true,
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:    "a stopped line still parks a running session on its question",
			stopped: true,
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(nil),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			name:    "a shipping task still ships on a stopped line",
			stopped: true,
			ledger:  led(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Ship: "stopped"}),
			want:    Action{Kind: ActShipStep, Task: "T1.1", Session: "t1-1"},
		},
		{
			// There is no session to watch, so creating one is a start.
			name:      "a stopped line does not create a missing session",
			stopped:   true,
			ledger:    led(running("T1.1")),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1", LineStopped: true},
			reasonHas: []string{"the line is stopped", "T1.1 has no session"},
		},
		{
			name:    "a stopped line holds back a launch",
			stopped: true,
			want:    Action{Kind: ActWait, LineStopped: true},
		},
		{
			name:    "a stopped line holds back an un-park",
			stopped: true,
			ledger:  led(parked("T1.1", "q1")),
			items:   []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			want:    Action{Kind: ActWait, LineStopped: true},
		},
		{
			name:    "a stopped line holds back an owner task",
			stopped: true,
			tasks:   []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			want:    Action{Kind: ActWait, LineStopped: true},
		},
		{
			name:    "a stopped line holds back an answered owner task",
			stopped: true,
			tasks:   []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			ledger:  led(ownerParked("T0.0", "o1")),
			items:   []inbox.Item{answered(blocking(item("o1", "T0.0", "")), time.Minute)},
			want:    Action{Kind: ActWait, LineStopped: true},
		},
		{
			// `afk resume` clears both, so the reason names the stop: it is
			// the one the owner has to look at before resuming.
			name:      "stopped and paused reads as stopped",
			stopped:   true,
			paused:    true,
			want:      Action{Kind: ActWait, LineStopped: true},
			reasonHas: []string{"the line is stopped"},
		},

		// Rule 3f: recovery. Every row below has a session that is wedged
		// (host running, verdict published) or gone (host not running, past
		// the grace) unless its name says otherwise.
		{
			name:      "wedged for the confirm time is restarted",
			obs:       recovering(nil),
			ledger:    led(running("T1.1")),
			session:   view(wedged(wedgeConfirm)),
			want:      Action{Kind: ActRestart, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"T1.1", "t1-1", "stopped answering"},
		},
		{
			name:      "wedged for less than the confirm time waits",
			obs:       recovering(nil),
			ledger:    led(running("T1.1")),
			session:   view(wedged(wedgeConfirm - time.Second)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"stopped answering", "waiting to be sure"},
		},
		{
			// The confirm wait comes before the cap: a verdict that may clear
			// by itself must not park a task either.
			name:      "wedged for less than the confirm time waits even at the cap",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", maxRestarts, time.Hour, false)),
			session:   view(wedged(wedgeConfirm - time.Second)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"waiting to be sure"},
		},
		{
			name:      "wedged without the means to restart waits",
			obs:       recovering(func(o *Observation) { o.CanRestart = false }),
			ledger:    led(running("T1.1")),
			session:   view(wedged(time.Hour)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			// A wedged agent that had asked something is still told about as
			// a stall when nothing can restart it: the wedge is ignored, the
			// rules after it are not.
			name:   "wedged without the means to restart still stalls",
			obs:    recovering(func(o *Observation) { o.CanRestart = false }),
			ledger: led(running("T1.1")),
			session: view(both(wedged(time.Hour), func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-stallAfter - time.Second)
			})),
			want: Action{Kind: ActStall, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "host gone past the grace is relaunched",
			obs:       recovering(nil),
			ledger:    led(running("T1.1")),
			session:   view(gone),
			want:      Action{Kind: ActRelaunch, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"T1.1", "t1-1", "stopped running"},
		},
		{
			name:    "host gone without the means to relaunch is reported as before",
			obs:     recovering(func(o *Observation) { o.CanRelaunch = false }),
			ledger:  led(running("T1.1")),
			session: view(gone),
			want:    Action{Kind: ActHostGone, Task: "T1.1", Session: "t1-1"},
		},
		{
			// The old once-per-attempt limit still holds on that path.
			name:      "host gone without the means to relaunch is reported once",
			obs:       recovering(func(o *Observation) { o.CanRelaunch = false }),
			ledger:    led(running("T1.1")),
			items:     []inbox.Item{answered(titled(item("hg1", "T1.1", "t1-1"), HostGoneTitlePrefix), 20*time.Minute)},
			session:   view(gone),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"already reported"},
		},
		{
			// A host exactly at its grace is not gone yet, with or without
			// the means to relaunch it.
			name:      "host gone exactly at the grace is not relaunched",
			obs:       recovering(nil),
			ledger:    led(running("T1.1")),
			session:   view(func(v *SessionView) { v.HostRunning = false; v.UpdatedAt = now.Add(-hostGrace) }),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			// Relaunched a grace ago, and the new host is not seen yet. The
			// status time is an hour old, so only restart_at holds this back.
			name:      "host gone inside the grace measured from the relaunch waits",
			obs:       recovering(func(o *Observation) { o.RestartBackoff = time.Second }),
			ledger:    led(recovered("T1.1", 1, hostGrace, false)),
			session:   view(gone),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:    "host gone past the grace measured from the relaunch is relaunched again",
			obs:     recovering(func(o *Observation) { o.RestartBackoff = time.Second }),
			ledger:  led(recovered("T1.1", 1, hostGrace+time.Second, false)),
			session: view(gone),
			want:    Action{Kind: ActRelaunch, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "wedged inside the backoff waits",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 1, restartBackoff-time.Second, false)),
			session:   view(wedged(time.Hour)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"waiting before trying to recover it again", "1 restart"},
		},
		{
			name:    "wedged exactly at the backoff is restarted",
			obs:     recovering(nil),
			ledger:  led(recovered("T1.1", 1, restartBackoff, false)),
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActRestart, Task: "T1.1", Session: "t1-1"},
		},
		{
			// The second wait is twice the first: one backoff is not enough
			// after two recoveries.
			name:      "wedged inside the doubled backoff waits",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 2, 2*restartBackoff-time.Second, false)),
			session:   view(wedged(time.Hour)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"2 restarts"},
		},
		{
			name:    "wedged exactly at the doubled backoff is restarted",
			obs:     recovering(nil),
			ledger:  led(recovered("T1.1", 2, 2*restartBackoff, false)),
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActRestart, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "host gone inside the backoff waits for the relaunch",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 2, 2*restartBackoff-time.Second, false)),
			session:   view(gone),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"stopped running", "waiting before trying to recover"},
		},
		{
			// A restart time that cannot be read is the zero time: its
			// backoff reads as served, and the cap still bounds the entry.
			name: "wedged with an unreadable backoff time is restarted",
			obs:  recovering(nil),
			ledger: led(func() ledger.Entry {
				e := recovered("T1.1", 2, 0, false)
				e.RestartAt = "yesterday"
				return e
			}()),
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActRestart, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "restarts at the cap and wedged gives up",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", maxRestarts, time.Hour, false)),
			session:   view(wedged(time.Hour)),
			want:      Action{Kind: ActGiveUp, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"T1.1", "t1-1", "3 restarts"},
		},
		{
			name:      "restarts at the cap and host gone gives up",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", maxRestarts, time.Hour, false)),
			session:   view(gone),
			want:      Action{Kind: ActGiveUp, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"stopped running", "3 restarts"},
		},
		{
			// Out of restarts is said at once, not after one more backoff.
			name:    "restarts at the cap gives up inside the backoff too",
			obs:     recovering(nil),
			ledger:  led(recovered("T1.1", maxRestarts, time.Second, false)),
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActGiveUp, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "one restart below the cap and wedged is restarted",
			obs:     recovering(nil),
			ledger:  led(recovered("T1.1", maxRestarts-1, time.Hour, false)),
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActRestart, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "max restarts 0 gives up on the first wedged session",
			obs:       recovering(func(o *Observation) { o.MaxRestarts = 0 }),
			ledger:    led(running("T1.1")),
			session:   view(wedged(wedgeConfirm)),
			want:      Action{Kind: ActGiveUp, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"T1.1", "t1-1", "set not to restart"},
		},
		{
			name:      "max restarts 0 and host gone gives up with no relaunch",
			obs:       recovering(func(o *Observation) { o.MaxRestarts = 0 }),
			ledger:    led(running("T1.1")),
			session:   view(gone),
			want:      Action{Kind: ActGiveUp, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"set not to restart"},
		},
		{
			name:    "phase done with a gone host ships and is not relaunched",
			obs:     recovering(nil),
			ledger:  led(running("T1.1")),
			session: view(both(gone, func(v *SessionView) { v.Phase = state.PhaseDone })),
			want:    Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "phase done with a wedged host ships and is not restarted",
			obs:     recovering(nil),
			ledger:  led(running("T1.1")),
			session: view(both(wedged(time.Hour), func(v *SessionView) { v.Phase = state.PhaseDone })),
			want:    Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "phase failed with a wedged or gone host fails and gives up nothing",
			obs:     recovering(func(o *Observation) { o.MaxRestarts = 0 }),
			ledger:  led(running("T1.1")),
			session: view(both(gone, func(v *SessionView) { v.Phase = state.PhaseFailed })),
			want:    Action{Kind: ActFail, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "phase failed with a wedged host fails",
			obs:     recovering(nil),
			ledger:  led(running("T1.1")),
			session: view(both(wedged(time.Hour), func(v *SessionView) { v.Phase = state.PhaseFailed })),
			want:    Action{Kind: ActFail, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "an open blocking item with a wedged host parks",
			obs:     recovering(nil),
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			name:    "an open blocking item with a gone host parks before any relaunch",
			obs:     recovering(nil),
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(gone),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			name:      "paused project with a wedged running session is restarted",
			obs:       recovering(nil),
			paused:    true,
			ledger:    led(running("T1.1")),
			session:   view(wedged(time.Hour)),
			want:      Action{Kind: ActRestart, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"restarting"},
		},
		{
			name:    "a stopped line with a gone running session is still relaunched",
			obs:     recovering(nil),
			stopped: true,
			ledger:  led(running("T1.1")),
			session: view(gone),
			want:    Action{Kind: ActRelaunch, Task: "T1.1", Session: "t1-1"},
		},

		// Rule 3g: the continue prompt after a recovery.
		{
			name:      "continue pending before the continue time waits",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 1, continueAfter-time.Second, true)),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "continue pending exactly at the continue time is told to continue",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 1, continueAfter, true)),
			session:   view(nil),
			want:      Action{Kind: ActContinue, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"T1.1", "t1-1", "carry on"},
		},
		{
			name:    "continue pending but wedged takes the restart path",
			obs:     recovering(nil),
			ledger:  led(recovered("T1.1", 1, time.Hour, true)),
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActRestart, Task: "T1.1", Session: "t1-1"},
		},
		{
			// Wedged again, but too recently to act on: nothing is typed
			// into an agent that is not answering.
			name:      "continue pending and wedged for less than the confirm time waits",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 1, time.Hour, true)),
			session:   view(wedged(time.Second)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"waiting to be sure"},
		},
		{
			name:      "continue pending and wedged without the means to restart waits",
			obs:       recovering(func(o *Observation) { o.CanRestart = false }),
			ledger:    led(recovered("T1.1", 1, time.Hour, true)),
			session:   view(wedged(time.Hour)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "continue pending without the means to prompt waits",
			obs:       recovering(func(o *Observation) { o.CanPrompt = false }),
			ledger:    led(recovered("T1.1", 1, time.Hour, true)),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			// The relaunched host is not up yet and is inside its grace:
			// there is nothing to type into.
			name:      "continue pending with the host not running yet waits",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 1, continueAfter, true)),
			session:   view(func(v *SessionView) { v.HostRunning = false }),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "a recovered session with nothing pending is not told to continue",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 1, time.Hour, false)),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:    "an open blocking item parks before the continue prompt",
			obs:     recovering(nil),
			ledger:  led(recovered("T1.1", 1, time.Hour, true)),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(nil),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			// A recovery is a sign of life: the stall clock starts again
			// from it, as it does from an answered item.
			name:   "a recovery restarts the stall clock",
			obs:    recovering(nil),
			ledger: led(recovered("T1.1", 1, stallAfter, false)),
			session: view(func(v *SessionView) {
				v.AwaitingInput = true
				v.UpdatedAt = now.Add(-time.Hour)
			}),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},

		// Rule 3b with recovery: the status cannot be read.
		{
			name:    "stale status and host gone is relaunched",
			obs:     recovering(nil),
			ledger:  led(running("T1.1")),
			session: view(both(stale, func(v *SessionView) { v.HostRunning = false })),
			want:    Action{Kind: ActRelaunch, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "stale status and host gone without the means to relaunch is reported as before",
			obs:     recovering(func(o *Observation) { o.CanRelaunch = false }),
			ledger:  led(running("T1.1")),
			session: view(both(stale, func(v *SessionView) { v.HostRunning = false })),
			want:    Action{Kind: ActHostGone, Task: "T1.1", Session: "t1-1"},
		},
		{
			// The stale grace runs from the later of the start and the last
			// recovery; the status time is what cannot be read.
			name:      "stale status and host gone inside the grace from the relaunch waits",
			obs:       recovering(func(o *Observation) { o.RestartBackoff = time.Second }),
			ledger:    led(recovered("T1.1", 1, hostGrace, false)),
			session:   view(both(stale, func(v *SessionView) { v.HostRunning = false })),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"status file cannot be read"},
		},
		{
			name:    "stale status and wedged is restarted",
			obs:     recovering(nil),
			ledger:  led(running("T1.1")),
			session: view(both(stale, wedged(wedgeConfirm))),
			want:    Action{Kind: ActRestart, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "stale status and wedged without the means to restart waits",
			obs:       recovering(func(o *Observation) { o.CanRestart = false }),
			ledger:    led(running("T1.1")),
			session:   view(both(stale, wedged(time.Hour))),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"status file cannot be read"},
		},
		{
			name:    "stale status and wedged at the cap gives up",
			obs:     recovering(nil),
			ledger:  led(recovered("T1.1", maxRestarts, time.Hour, false)),
			session: view(both(stale, wedged(time.Hour))),
			want:    Action{Kind: ActGiveUp, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "stale with an open blocking item and a wedged host parks",
			obs:     recovering(nil),
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(both(stale, wedged(time.Hour))),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			// New with recovery, and true with it switched off too: the
			// owner being asked outranks an unreadable status.
			name:    "stale with an open blocking item parks with no recovery wired and nothing wedged",
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(stale),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			name:      "stale status with the host running and continue pending is told to continue",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 1, continueAfter, true)),
			session:   view(stale),
			want:      Action{Kind: ActContinue, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"carry on"},
		},
		{
			name:      "stale status with continue pending before the continue time waits",
			obs:       recovering(nil),
			ledger:    led(recovered("T1.1", 1, continueAfter-time.Second, true)),
			session:   view(stale),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"status file cannot be read"},
		},
		// Rule 7 and rule a: the governor's hold on new work. Every row is
		// wired with every means, so what changes is only the verdict.
		{
			name: "unconsulted usage launches",
			obs:  unconsulted(governor.ActionPause),
			want: Action{Kind: ActLaunch, Task: "T1.1"},
		},
		{
			name: "run launches",
			obs:  governed(governor.ActionRun, nil),
			want: Action{Kind: ActLaunch, Task: "T1.1"},
		},
		{
			name:      "no_launch holds a launch",
			obs:       governed(governor.ActionNoLaunch, nil),
			want:      Action{Kind: ActUsageWait, Task: "T1.1"},
			reasonHas: []string{"close to the limit", "afk usage"},
		},
		{
			name:      "hold holds a launch",
			obs:       governed(governor.ActionHold, nil),
			want:      Action{Kind: ActUsageWait, Task: "T1.1"},
			reasonHas: []string{"no recent usage reading", "afk usage"},
		},
		{
			name:      "pause holds a launch",
			obs:       governed(governor.ActionPause, nil),
			want:      Action{Kind: ActUsageWait, Task: "T1.1"},
			reasonHas: []string{"pause line"},
		},
		{
			name:      "refuse holds a launch",
			obs:       governed(governor.ActionRefuse, nil),
			want:      Action{Kind: ActUsageWait, Task: "T1.1"},
			reasonHas: []string{"extra usage"},
		},
		{
			// Only "run" may start anything; a word from a newer governor
			// is not it.
			name:      "an unknown governor word holds a launch",
			obs:       governed(governor.Action("later"), nil),
			want:      Action{Kind: ActUsageWait, Task: "T1.1"},
			reasonHas: []string{"afk usage"},
		},
		{
			name:      "logged out holds a launch",
			obs:       governed(governor.ActionRun, loggedOut),
			want:      Action{Kind: ActLoginWait, Task: "T1.1"},
			reasonHas: []string{"logged out", "log it in"},
		},
		{
			name:      "logged out wins over usage",
			obs:       governed(governor.ActionPause, loggedOut),
			want:      Action{Kind: ActLoginWait, Task: "T1.1"},
			reasonHas: []string{"logged out"},
		},
		{
			name:      "logged out holds a launch with usage unconsulted",
			obs:       func(o *Observation) { loggedOut(o) },
			want:      Action{Kind: ActLoginWait, Task: "T1.1"},
			reasonHas: []string{"logged out"},
		},
		{
			name: "login unknown launches",
			obs:  governed(governor.ActionRun, func(o *Observation) { o.Login = LoginView{} }),
			want: Action{Kind: ActLaunch, Task: "T1.1"},
		},
		{
			// LoggedIn means nothing without Known.
			name: "login unknown launches whatever its other field says",
			obs:  governed(governor.ActionRun, func(o *Observation) { o.Login = LoginView{LoggedIn: false} }),
			want: Action{Kind: ActLaunch, Task: "T1.1"},
		},
		{
			name: "logged in launches",
			obs:  governed(governor.ActionRun, loggedIn),
			want: Action{Kind: ActLaunch, Task: "T1.1"},
		},
		{
			name:      "an owner task is still raised under a usage hold",
			obs:       governed(governor.ActionHold, loggedOut),
			tasks:     []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			want:      Action{Kind: ActOwner, Task: "T0.0"},
			reasonHas: []string{"your task"},
		},
		{
			name:   "an answered owner task is still done under refuse",
			obs:    governed(governor.ActionRefuse, nil),
			tasks:  []masterplan.Task{ownerTask("T0.0"), task("T1.1", "T0.0")},
			ledger: led(ownerParked("T0.0", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T0.0", "")), time.Minute)},
			want:   Action{Kind: ActOwnerDone, Task: "T0.0"},
		},
		{
			name:      "an owner pause keeps its own wording under a usage hold",
			obs:       governed(governor.ActionHold, loggedOut),
			paused:    true,
			want:      Action{Kind: ActWait},
			reasonHas: []string{"paused", "afk resume"},
		},
		{
			name:       "a stopped line keeps its own wording under a usage hold",
			obs:        governed(governor.ActionHold, nil),
			stopped:    true,
			stopReason: "deploy check failed",
			want:       Action{Kind: ActWait, LineStopped: true},
			reasonHas:  []string{"the line is stopped"},
		},
		{
			name:      "an idle project stays idle under refuse",
			obs:       governed(governor.ActionRefuse, loggedOut),
			ledger:    led(entry("T1.1", ledger.Done), entry("T1.2", ledger.Done), entry("T1.3", ledger.Done)),
			want:      Action{Kind: ActIdle},
			reasonHas: []string{"all done"},
		},
		{
			name:     "no ledger file is still reported under a usage hold",
			obs:      governed(governor.ActionHold, nil),
			noLedger: true,
			want:     Action{Kind: ActNoLedger},
		},

		// Rule a: creating a missing session again is a start.
		{
			name:   "unconsulted usage creates a missing session",
			obs:    unconsulted(governor.ActionRefuse),
			ledger: led(running("T1.1")),
			want:   Action{Kind: ActCreate, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "run creates a missing session",
			obs:    governed(governor.ActionRun, loggedIn),
			ledger: led(running("T1.1")),
			want:   Action{Kind: ActCreate, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "no_launch holds a create",
			obs:    governed(governor.ActionNoLaunch, nil),
			ledger: led(running("T1.1")),
			want:   Action{Kind: ActUsageWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "hold holds a create",
			obs:    governed(governor.ActionHold, nil),
			ledger: led(running("T1.1")),
			want:   Action{Kind: ActUsageWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "pause holds a create",
			obs:    governed(governor.ActionPause, nil),
			ledger: led(running("T1.1")),
			want:   Action{Kind: ActUsageWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "refuse holds a create",
			obs:    governed(governor.ActionRefuse, nil),
			ledger: led(running("T1.1")),
			want:   Action{Kind: ActUsageWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "logged out holds a create",
			obs:    governed(governor.ActionRun, loggedOut),
			ledger: led(running("T1.1")),
			want:   Action{Kind: ActLoginWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "an owner pause keeps its own wording for a missing session under a usage hold",
			obs:       governed(governor.ActionHold, nil),
			paused:    true,
			ledger:    led(running("T1.1")),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"paused"},
		},
		{
			// The pause fields do not survive a session that is gone from
			// disk: there is nothing to resume, so it is a create like any
			// other, and the executor clears them when it creates.
			name:   "usage paused with no session on disk is created again on a run reading",
			obs:    governed(governor.ActionRun, nil),
			ledger: led(usagePaused("T1.1", time.Hour, "")),
			want:   Action{Kind: ActCreate, Task: "T1.1", Session: "t1-1"},
		},

		// Rule 7: an un-park sets an agent going again.
		{
			name:   "unconsulted usage un-parks",
			obs:    unconsulted(governor.ActionRefuse),
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(nil)},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Running},
		},
		{
			name:   "no_launch does not hold an un-park",
			obs:    governed(governor.ActionNoLaunch, nil),
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(nil)},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Running},
		},
		{
			name:   "hold holds an un-park",
			obs:    governed(governor.ActionHold, nil),
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(nil)},
			want:   Action{Kind: ActUsageWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "pause holds an un-park",
			obs:    governed(governor.ActionPause, nil),
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(nil)},
			want:   Action{Kind: ActUsageWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "refuse holds an un-park",
			obs:    governed(governor.ActionRefuse, nil),
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(nil)},
			want:   Action{Kind: ActUsageWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "logged out holds an un-park",
			obs:    governed(governor.ActionRun, loggedOut),
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(nil)},
			want:   Action{Kind: ActLoginWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			// no_launch lets a running session carry on; it does not make a
			// logged-out agent able to.
			name:   "logged out holds an un-park under no_launch",
			obs:    governed(governor.ActionNoLaunch, loggedOut),
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(nil)},
			want:   Action{Kind: ActLoginWait, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:   "hold does not hold an un-park back to shipping",
			obs:    governed(governor.ActionHold, nil),
			ledger: led(shipParked("T1.1", "pr_open", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Shipping},
		},
		{
			name:   "pause does not hold an un-park back to shipping",
			obs:    governed(governor.ActionPause, nil),
			ledger: led(shipParked("T1.1", "pr_open", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Shipping},
		},
		{
			name:   "refuse does not hold an un-park back to shipping",
			obs:    governed(governor.ActionRefuse, nil),
			ledger: led(shipParked("T1.1", "pr_open", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Shipping},
		},
		{
			name:   "logged out does not hold an un-park back to shipping",
			obs:    governed(governor.ActionNoLaunch, loggedOut),
			ledger: led(parked("T1.1", "q1")),
			items:  []inbox.Item{answered(blocking(item("q1", "T1.1", "t1-1")), time.Minute)},
			parked: map[string]*SessionView{"T1.1": view(func(v *SessionView) { v.Phase = state.PhaseDone })},
			want:   Action{Kind: ActUnpark, Task: "T1.1", Session: "t1-1", Resume: ledger.Shipping},
		},
		{
			name: "a shipping task still ships under refuse and logged out",
			obs:  governed(governor.ActionRefuse, loggedOut),
			ledger: led(func() ledger.Entry {
				e := running("T1.1")
				e.Status = ledger.Shipping
				return e
			}()),
			want: Action{Kind: ActShipStep, Task: "T1.1", Session: "t1-1"},
		},

		// Rule u, entry not paused: when a running session is stopped.
		{
			name:      "unconsulted usage leaves a running session alone",
			obs:       unconsulted(governor.ActionPause),
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name: "running and the governor says pause is paused with the governor's wake",
			obs: governed(governor.ActionPause, func(o *Observation) {
				o.Usage.WakeAt, o.Usage.NextReset = now.Add(2*time.Hour), now.Add(5*time.Hour)
			}),
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(2 * time.Hour)},
			reasonHas: []string{"pause line"},
		},
		{
			// The governor gives no wake when a window at the limit has no
			// reset time. Zero stays zero: a new reading decides.
			name:    "running and the governor says pause with no wake keeps the wake empty",
			obs:     governed(governor.ActionPause, func(o *Observation) { o.Usage.NextReset = now.Add(5 * time.Hour) }),
			ledger:  led(running("T1.1")),
			session: view(nil),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1"},
		},
		{
			name: "running and the governor says refuse is paused with no wake",
			obs: governed(governor.ActionRefuse, func(o *Observation) {
				o.Usage.WakeAt, o.Usage.NextReset = now.Add(2*time.Hour), now.Add(5*time.Hour)
			}),
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"extra usage"},
		},
		{
			name:      "running under no_launch just waits",
			obs:       governed(governor.ActionNoLaunch, nil),
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "running under hold just waits",
			obs:       governed(governor.ActionHold, nil),
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			// A logged-out agent with a session running is left to it:
			// recovery already bounds a session that keeps dying.
			name:      "running and logged out just waits",
			obs:       governed(governor.ActionRun, loggedOut),
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "a pause is not chosen without the means to stop",
			obs:       governed(governor.ActionPause, func(o *Observation) { o.CanPause = false }),
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "a pause is not chosen without the means to start again",
			obs:       governed(governor.ActionRefuse, func(o *Observation) { o.CanRelaunch = false }),
			ledger:    led(running("T1.1")),
			session:   view(nil),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			// Without the means the later rules run unchanged, the stall
			// report among them.
			name:    "a pause without the means still reports a stall",
			obs:     governed(governor.ActionPause, func(o *Observation) { o.CanPause = false }),
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.AwaitingInput, v.UpdatedAt = true, now.Add(-time.Hour) }),
			want:    Action{Kind: ActStall, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "a fresh rate-limit marker pauses until the next reset",
			obs:       governed(governor.ActionRun, func(o *Observation) { o.Usage.NextReset = now.Add(3 * time.Hour) }),
			ledger:    led(running("T1.1")),
			session:   view(rateLimited(time.Minute)),
			want:      Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(3 * time.Hour)},
			reasonHas: []string{"hit a usage limit"},
		},
		{
			name:    "a fresh rate-limit marker with no reset known pauses for the fallback",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(running("T1.1")),
			session: view(rateLimited(time.Minute)),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(rateLimitFallback)},
		},
		{
			// A reset already past cannot be the one that ends this limit,
			// and waking at it would resume at once.
			name:    "a fresh rate-limit marker with the next reset already past pauses for the fallback",
			obs:     governed(governor.ActionRun, func(o *Observation) { o.Usage.NextReset = now.Add(-time.Minute) }),
			ledger:  led(running("T1.1")),
			session: view(rateLimited(time.Minute)),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(rateLimitFallback)},
		},
		{
			name:    "a next reset exactly now is not ahead, so the fallback is used",
			obs:     governed(governor.ActionRun, func(o *Observation) { o.Usage.NextReset = now }),
			ledger:  led(running("T1.1")),
			session: view(rateLimited(time.Minute)),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(rateLimitFallback)},
		},
		{
			name:    "a fresh rate-limit marker pauses under no_launch too",
			obs:     governed(governor.ActionNoLaunch, func(o *Observation) { o.Usage.NextReset = now.Add(3 * time.Hour) }),
			ledger:  led(running("T1.1")),
			session: view(rateLimited(time.Minute)),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(3 * time.Hour)},
		},
		{
			// The governor's own pause decides the wake, marker or not.
			name: "a fresh rate-limit marker under a governor pause takes the governor's wake",
			obs: governed(governor.ActionPause, func(o *Observation) {
				o.Usage.WakeAt, o.Usage.NextReset = now.Add(2*time.Hour), now.Add(5*time.Hour)
			}),
			ledger:    led(running("T1.1")),
			session:   view(rateLimited(time.Minute)),
			want:      Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(2 * time.Hour)},
			reasonHas: []string{"pause line"},
		},
		{
			name:    "a fresh rate-limit marker under refuse has no wake",
			obs:     governed(governor.ActionRefuse, func(o *Observation) { o.Usage.NextReset = now.Add(5 * time.Hour) }),
			ledger:  led(running("T1.1")),
			session: view(rateLimited(time.Minute)),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1"},
		},
		{
			// Nothing removes the marker, so one from an earlier attempt in
			// the same worktree is still there.
			name:      "a rate-limit marker older than the attempt does nothing",
			obs:       governed(governor.ActionRun, nil),
			ledger:    led(running("T1.1")),
			session:   view(rateLimited(2 * time.Hour)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "a rate-limit marker in the second the attempt began does nothing",
			obs:       governed(governor.ActionRun, nil),
			ledger:    led(running("T1.1")),
			session:   view(rateLimited(time.Hour)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			// The marker that caused a pause is still there after the
			// resume, which stamps restart_at.
			name:      "a rate-limit marker older than the last restart does nothing",
			obs:       governed(governor.ActionRun, nil),
			ledger:    led(recovered("T1.1", 0, 10*time.Minute, false)),
			session:   view(rateLimited(20 * time.Minute)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "a rate-limit marker in the second of the last restart does nothing",
			obs:       governed(governor.ActionRun, nil),
			ledger:    led(recovered("T1.1", 0, 10*time.Minute, false)),
			session:   view(rateLimited(10 * time.Minute)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:    "a rate-limit marker one second after the last restart pauses",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(recovered("T1.1", 0, 10*time.Minute, false)),
			session: view(rateLimited(10*time.Minute - time.Second)),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(rateLimitFallback)},
		},
		{
			name:      "a fresh rate-limit marker with usage unconsulted does nothing",
			obs:       unconsulted(governor.ActionRun),
			ledger:    led(running("T1.1")),
			session:   view(rateLimited(time.Minute)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:      "a fresh rate-limit marker without the means to stop does nothing",
			obs:       governed(governor.ActionRun, func(o *Observation) { o.CanPause = false }),
			ledger:    led(running("T1.1")),
			session:   view(rateLimited(time.Minute)),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"running T1.1"},
		},
		{
			name:    "a pause is due but the session finished, so it ships",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(running("T1.1")),
			session: view(func(v *SessionView) { v.Phase = state.PhaseDone }),
			want:    Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "a pause is due but a blocking item is open, so it parks",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(running("T1.1")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(nil),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			// The pause outranks recovery: a wedged agent under a pause is
			// stopped, not restarted into the limit.
			name:    "a pause is due and the session is wedged, so it is paused and not restarted",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(running("T1.1")),
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1"},
		},
		{
			// With the host already gone the pause is still recorded, so
			// that recovery does not start it again under the limit.
			name:    "a pause is due and the host is gone, so it is paused and not relaunched",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(running("T1.1")),
			session: view(gone),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "stale status and the governor says pause is paused",
			obs:     governed(governor.ActionPause, func(o *Observation) { o.Usage.WakeAt = now.Add(2 * time.Hour) }),
			ledger:  led(running("T1.1")),
			session: view(stale),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(2 * time.Hour)},
		},
		{
			name:    "stale status and a fresh rate-limit marker is paused",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(running("T1.1")),
			session: view(both(stale, rateLimited(time.Minute))),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", WakeAt: now.Add(rateLimitFallback)},
		},
		{
			name:      "stale status with usage unconsulted waits as before",
			obs:       unconsulted(governor.ActionPause),
			ledger:    led(running("T1.1")),
			session:   view(stale),
			want:      Action{Kind: ActWait, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"status file cannot be read"},
		},

		// Rule u, entry paused: nothing below it may see the session.
		{
			name:      "usage paused with the host gone past the grace is not relaunched",
			obs:       governed(governor.ActionPause, nil),
			ledger:    led(usagePaused("T1.1", 30*time.Minute, "")),
			session:   view(gone),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"paused for usage"},
		},
		{
			name: "usage paused with the host gone and every restart used is not given up on",
			obs:  governed(governor.ActionPause, nil),
			ledger: led(func() ledger.Entry {
				e := usagePaused("T1.1", 30*time.Minute, "")
				e.Restarts, e.RestartAt = maxRestarts, ago(40*time.Minute)
				return e
			}()),
			session: view(gone),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused with the host gone and no means to relaunch is not reported gone",
			obs:     governed(governor.ActionRun, func(o *Observation) { o.CanRelaunch = false }),
			ledger:  led(usagePaused("T1.1", 30*time.Minute, "")),
			session: view(gone),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			// With no relaunch the session can only be started over by hand.
			reasonHas: []string{"cannot start a session again", "afk ledger set T1.1 pending"},
		},
		{
			name:    "usage paused and wedged is not restarted",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", pauseRetry-time.Second, "")),
			session: view(wedged(time.Hour)),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name: "usage paused with continue pending types nothing",
			obs:  governed(governor.ActionPause, nil),
			ledger: led(func() ledger.Entry {
				e := recovered("T1.1", 1, time.Hour, true)
				e.UsagePauseAt = ago(pauseRetry - time.Second)
				return e
			}()),
			session: view(nil),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused and awaiting input past the stall limit reports no stall",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", 30*time.Minute, "")),
			session: view(both(gone, func(v *SessionView) { v.AwaitingInput = true })),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused with the host up and awaiting input past the stall limit reports no stall",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", pauseRetry-time.Second, "")),
			session: view(func(v *SessionView) { v.AwaitingInput, v.UpdatedAt = true, now.Add(-time.Hour) }),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "usage paused with the host still up before the retry time waits",
			obs:       governed(governor.ActionPause, nil),
			ledger:    led(usagePaused("T1.1", pauseRetry-time.Second, "")),
			session:   view(nil),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"waiting for it to stop"},
		},
		{
			name:      "usage paused with the host still up at the retry time is stopped by force",
			obs:       governed(governor.ActionPause, nil),
			ledger:    led(usagePaused("T1.1", pauseRetry, "")),
			session:   view(nil),
			want:      Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", Force: true},
			reasonHas: []string{"still running"},
		},
		{
			name:    "usage paused with the host still up just short of three retry times is still forced",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", 3*pauseRetry-time.Second, "")),
			session: view(nil),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", Force: true},
		},
		{
			name:      "usage paused with the host still up at three retry times is reported stuck",
			obs:       governed(governor.ActionPause, nil),
			ledger:    led(usagePaused("T1.1", 3*pauseRetry, "")),
			session:   view(nil),
			want:      Action{Kind: ActUsageStuck, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"still running", "spending"},
		},
		{
			// The same observation with the stuck item open: rule e parks
			// first, which is the whole of the once-only limit.
			name:    "usage stuck with its item open parks",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", 3*pauseRetry, "")),
			items:   []inbox.Item{titled(item("u1", "T1.1", "t1-1"), PauseFailedTitlePrefix)},
			session: view(nil),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"u1"}},
		},
		{
			name:    "usage paused with the host still up and no means to stop it just waits",
			obs:     governed(governor.ActionPause, func(o *Observation) { o.CanPause = false }),
			ledger:  led(usagePaused("T1.1", time.Hour, "")),
			session: view(nil),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused never resumes while its host is running",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", pauseRetry-time.Second, "")),
			session: view(nil),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused with its host running on a run reading is forced, not resumed",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", pauseRetry, ago(time.Second))),
			session: view(nil),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", Force: true},
		},
		{
			// A forced stop is about the host, not the clock: a wake still
			// ahead does not excuse a session that has not stopped.
			name:      "usage paused before its wake with the host still up is forced",
			obs:       governed(governor.ActionPause, nil),
			ledger:    led(usagePaused("T1.1", pauseRetry, ago(-time.Hour))),
			session:   view(nil),
			want:      Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", Force: true},
			reasonHas: []string{"still running"},
		},
		{
			name:      "usage paused resumes on run after the wake",
			obs:       governed(governor.ActionRun, nil),
			ledger:    led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session:   view(gone),
			want:      Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"starting"},
		},
		{
			name:    "usage paused resumes on no_launch after the wake",
			obs:     governed(governor.ActionNoLaunch, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session: view(gone),
			want:    Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused resumes exactly at the wake",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, ago(0))),
			session: view(gone),
			want:    Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused with no wake resumes on a run reading",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			session: view(gone),
			want:    Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused with an unreadable wake resumes on a run reading",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "next tuesday")),
			session: view(gone),
			want:    Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
		},
		{
			// The host is only just gone: a resume needs no grace, because
			// the loop is the one that stopped it.
			name:    "usage paused resumes with the host gone inside the grace",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			session: view(stopped),
			want:    Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused resumes with a login known to be in",
			obs:     governed(governor.ActionRun, loggedIn),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			session: view(gone),
			want:    Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "usage paused does not resume before the wake",
			obs:       governed(governor.ActionRun, nil),
			ledger:    led(usagePaused("T1.1", 2*time.Hour, ago(-time.Hour))),
			session:   view(gone),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"paused for usage until", "13:00 UTC"},
		},
		{
			name:      "usage paused before its wake waits with usage unconsulted",
			obs:       unconsulted(governor.ActionRun),
			ledger:    led(usagePaused("T1.1", 2*time.Hour, ago(-time.Hour))),
			session:   view(gone),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"paused for usage until", "13:00 UTC"},
		},
		{
			name:      "usage paused does not resume on a run reading taken before the pause",
			obs:       governed(governor.ActionRun, func(o *Observation) { o.Usage.At = now.Add(-3 * time.Hour) }),
			ledger:    led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session:   view(gone),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"new usage reading"},
		},
		{
			// The pause is stored to the second, so a reading inside that
			// second cannot be shown to be the later of the two.
			name: "usage paused does not resume on a reading taken in the second of the pause",
			obs: governed(governor.ActionRun, func(o *Observation) {
				o.Usage.At = now.Add(-2*time.Hour + 900*time.Millisecond)
			}),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			session: view(gone),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name: "usage paused resumes on a reading one second after the pause",
			obs: governed(governor.ActionRun, func(o *Observation) {
				o.Usage.At = now.Add(-2*time.Hour + time.Second)
			}),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			session: view(gone),
			want:    Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused does not resume on pause",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session: view(gone),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused does not resume on hold",
			obs:     governed(governor.ActionHold, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session: view(gone),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused does not resume on refuse",
			obs:     governed(governor.ActionRefuse, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			session: view(gone),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:      "usage paused past its wake with usage unconsulted says no reading is available",
			obs:       unconsulted(governor.ActionRun),
			ledger:    led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session:   view(gone),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"no usage reading is available", "afk ledger set T1.1 pending"},
		},
		{
			name:      "usage paused with no wake and usage unconsulted says no reading is available",
			obs:       unconsulted(governor.ActionRun),
			ledger:    led(usagePaused("T1.1", 2*time.Hour, "")),
			session:   view(gone),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"no usage reading is available", "afk ledger set T1.1 pending"},
		},
		{
			// A daemon with nothing wired at all meets a ledger a governed
			// daemon left behind: it still must not recover the session.
			name:      "usage paused is left alone by a daemon with nothing wired",
			ledger:    led(usagePaused("T1.1", 2*time.Hour, "")),
			session:   view(gone),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"no usage reading is available"},
		},
		{
			name:      "usage paused does not resume while logged out",
			obs:       governed(governor.ActionRun, loggedOut),
			ledger:    led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session:   view(gone),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"logged out"},
		},
		{
			name:    "usage paused does not resume without the means to relaunch",
			obs:     governed(governor.ActionRun, func(o *Observation) { o.CanRelaunch = false }),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session: view(gone),
			want:    Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused and phase done ships",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			session: view(both(gone, func(v *SessionView) { v.Phase = state.PhaseDone })),
			want:    Action{Kind: ActShip, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused and phase failed fails",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			session: view(both(gone, func(v *SessionView) { v.Phase = state.PhaseFailed })),
			want:    Action{Kind: ActFail, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "usage paused with an open blocking item parks",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(gone),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
		{
			name:      "stale status and usage paused with the host gone is not relaunched",
			obs:       governed(governor.ActionPause, nil),
			ledger:    led(usagePaused("T1.1", 30*time.Minute, "")),
			session:   view(both(stale, stopped)),
			want:      Action{Kind: ActUsagePaused, Task: "T1.1", Session: "t1-1"},
			reasonHas: []string{"paused for usage"},
		},
		{
			name:    "stale status and usage paused resumes on run after the wake",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, ago(time.Minute))),
			session: view(both(stale, stopped)),
			want:    Action{Kind: ActUsageResume, Task: "T1.1", Session: "t1-1"},
		},
		{
			name:    "stale status and usage paused with the host still up is forced",
			obs:     governed(governor.ActionPause, nil),
			ledger:  led(usagePaused("T1.1", pauseRetry, "")),
			session: view(stale),
			want:    Action{Kind: ActUsagePause, Task: "T1.1", Session: "t1-1", Force: true},
		},
		{
			name:    "stale status and usage paused with an open blocking item parks",
			obs:     governed(governor.ActionRun, nil),
			ledger:  led(usagePaused("T1.1", 2*time.Hour, "")),
			items:   []inbox.Item{blocking(item("q1", "T1.1", "t1-1"))},
			session: view(both(stale, stopped)),
			want:    Action{Kind: ActPark, Task: "T1.1", Session: "t1-1", Items: []string{"q1"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := tc.tasks
			if tasks == nil {
				tasks = plan
			}
			o := Observation{
				Now: now, InboxErr: tc.inboxErr,
				StallAfter: stallAfter, HostGrace: hostGrace,
				Paused: tc.paused, BuildErr: tc.buildErr,
				NoLedger: tc.noLedger, Stopped: tc.stopped, StopReason: tc.stopReason,
				Tasks: tasks, Ledger: tc.ledger, Items: tc.items, Session: tc.session,
				Parked: tc.parked, ParkedErr: tc.parkedErr,
			}
			if tc.obs != nil {
				tc.obs(&o)
			}
			got := Decide(o)
			if got.Kind != tc.want.Kind || got.Task != tc.want.Task || got.Session != tc.want.Session {
				t.Errorf("Decide = {%s task=%q session=%q}, want {%s task=%q session=%q}\nreason: %s",
					got.Kind, got.Task, got.Session, tc.want.Kind, tc.want.Task, tc.want.Session, got.Reason)
			}
			if got.Resume != tc.want.Resume {
				t.Errorf("Resume = %q, want %q\nreason: %s", got.Resume, tc.want.Resume, got.Reason)
			}
			if got.LineStopped != tc.want.LineStopped {
				t.Errorf("LineStopped = %v, want %v\nreason: %s", got.LineStopped, tc.want.LineStopped, got.Reason)
			}
			if !slices.Equal(got.Items, tc.want.Items) {
				t.Errorf("Items = %v, want %v", got.Items, tc.want.Items)
			}
			// Checked on every row: a wake or a forced stop on an action
			// that is not a usage pause would be carried out by nothing and
			// would still change the stored state.
			if !got.WakeAt.Equal(tc.want.WakeAt) {
				t.Errorf("WakeAt = %s, want %s\nreason: %s", got.WakeAt, tc.want.WakeAt, got.Reason)
			}
			if got.Force != tc.want.Force {
				t.Errorf("Force = %v, want %v\nreason: %s", got.Force, tc.want.Force, got.Reason)
			}
			// The loop writes its state and its log when a reason changes,
			// so no reason may carry the governor's own sentence, whose
			// numbers change with every reading.
			if o.Usage.Reason != "" && strings.Contains(got.Reason, o.Usage.Reason) {
				t.Errorf("Reason = %q holds the governor's sentence", got.Reason)
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
//
// The usage-stuck prefix is held to the same rule against every other one.
// Decide does not search for it, but it shares an inbox with the two that
// are searched for: beginning with either, a "could not be stopped" item
// would be read as a stall or host-gone report and silence one the owner is
// owed in the same attempt.
func TestTitlePrefixesAreDistinct(t *testing.T) {
	if StallTitlePrefix == "" || HostGoneTitlePrefix == "" {
		t.Fatal("a title prefix is empty; every item would match it")
	}
	if strings.HasPrefix(StallTitlePrefix, HostGoneTitlePrefix) || strings.HasPrefix(HostGoneTitlePrefix, StallTitlePrefix) {
		t.Errorf("prefixes overlap: %q and %q", StallTitlePrefix, HostGoneTitlePrefix)
	}
	if PauseFailedTitlePrefix == "" {
		t.Fatal("the pause-failed title prefix is empty; every item would match it")
	}
	for _, other := range []string{StallTitlePrefix, HostGoneTitlePrefix, RecoveryTitlePrefix} {
		if strings.HasPrefix(PauseFailedTitlePrefix, other) || strings.HasPrefix(other, PauseFailedTitlePrefix) {
			t.Errorf("prefixes overlap: %q and %q", PauseFailedTitlePrefix, other)
		}
	}
}

// TestNotices pins the two standing troubles the executor raises an item for.
//
// Load-bearing rows: "refuse without being consulted is no overage" (the view
// of a tick that did not ask means nothing, and an item raised from it would
// be answered and raised again on alternate ticks), "login unknown is not
// logged out" (a tool that cannot say must not page the owner to log in), and
// "pause is not overage" (only extra usage being on is the owner's to fix;
// a pause ends by itself).
func TestNotices(t *testing.T) {
	cases := []struct {
		name          string
		usage         UsageView
		login         LoginView
		wantOverage   bool
		wantLoggedOut bool
	}{
		{name: "nothing known is no notice"},
		{name: "run and logged in is no notice",
			usage: UsageView{Consulted: true, Action: governor.ActionRun}, login: LoginView{Known: true, LoggedIn: true}},
		{name: "refuse is overage",
			usage: UsageView{Consulted: true, Action: governor.ActionRefuse}, wantOverage: true},
		{name: "refuse without being consulted is no overage",
			usage: UsageView{Action: governor.ActionRefuse}},
		{name: "pause is not overage",
			usage: UsageView{Consulted: true, Action: governor.ActionPause}},
		{name: "hold is not overage",
			usage: UsageView{Consulted: true, Action: governor.ActionHold}},
		{name: "no_launch is not overage",
			usage: UsageView{Consulted: true, Action: governor.ActionNoLaunch}},
		{name: "known and out is logged out",
			login: LoginView{Known: true}, wantLoggedOut: true},
		{name: "login unknown is not logged out",
			login: LoginView{}},
		{name: "login unknown is not logged out whatever its other field says",
			login: LoginView{LoggedIn: true}},
		{name: "both at once",
			usage: UsageView{Consulted: true, Action: governor.ActionRefuse}, login: LoginView{Known: true},
			wantOverage: true, wantLoggedOut: true},
		{name: "logged out with usage unconsulted is still logged out",
			usage: UsageView{Action: governor.ActionRefuse}, login: LoginView{Known: true}, wantLoggedOut: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overage, loggedOut := Notices(Observation{Now: now, Usage: tc.usage, Login: tc.login})
			if overage != tc.wantOverage || loggedOut != tc.wantLoggedOut {
				t.Errorf("Notices = (overage %v, logged out %v), want (%v, %v)",
					overage, loggedOut, tc.wantOverage, tc.wantLoggedOut)
			}
		})
	}
}

// TestBackoffFor pins the wait before each recovery.
//
// Load-bearing rows: "none before the first" (the first recovery must not
// wait: the session is already down), "the base before the second" and
// "doubled before the third" (the schedule the README promises: at once,
// after one minute, after two), and the two overflow rows. A count from a
// hand-edited ledger, or a very long base, must give a long wait and never a
// wrapped, negative one, which Decide would read as no wait at all.
func TestBackoffFor(t *testing.T) {
	const longest = time.Duration(1<<63 - 1)
	cases := []struct {
		name     string
		restarts int
		base     time.Duration
		want     time.Duration
	}{
		{"none before the first", 0, time.Minute, 0},
		{"a negative count is no wait", -1, time.Minute, 0},
		{"the base before the second", 1, time.Minute, time.Minute},
		{"doubled before the third", 2, time.Minute, 2 * time.Minute},
		{"doubled again before the fourth", 3, time.Minute, 4 * time.Minute},
		{"the doubling stops at sixteen", 17, time.Minute, time.Minute << 16},
		{"a huge count is the same as the cap", 1 << 30, time.Minute, time.Minute << 16},
		{"a base too long to double is the longest wait", 17, 10000 * time.Hour, longest},
		{"a zero base is no wait", 3, 0, 0},
		{"a negative base is no wait", 3, -time.Minute, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := backoffFor(tc.restarts, tc.base)
			if got != tc.want {
				t.Errorf("backoffFor(%d, %s) = %s, want %s", tc.restarts, tc.base, got, tc.want)
			}
			if got < 0 {
				t.Errorf("backoffFor(%d, %s) = %s is negative", tc.restarts, tc.base, got)
			}
		})
	}
}

// TestRecoveryTitlePrefixIsDistinct holds the give-up item's title to the
// rule the other two prefixes are held to. Decide looks for stall and
// host-gone reports by prefix, so a give-up item that began with either would
// be read as one of them and silence a report the owner is owed in the same
// attempt.
func TestRecoveryTitlePrefixIsDistinct(t *testing.T) {
	if RecoveryTitlePrefix == "" {
		t.Fatal("the recovery title prefix is empty")
	}
	for _, other := range []string{StallTitlePrefix, HostGoneTitlePrefix} {
		if strings.HasPrefix(RecoveryTitlePrefix, other) || strings.HasPrefix(other, RecoveryTitlePrefix) {
			t.Errorf("prefixes overlap: %q and %q", RecoveryTitlePrefix, other)
		}
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
