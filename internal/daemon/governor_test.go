package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/ship"
	"github.com/dphaener/afk/internal/state"
)

// The tests in this file run the whole loop with the usage governor wired in:
// a fake usage source that asks the real governor.Decide about a fake account
// at the rig's clock, a fake login, and a fake stop that takes the session's
// host away. Decide's own usage branches are rows of TestDecide; these are
// about what the executor does with each answer, in what order it writes, and
// what survives a daemon that is killed and started again.
//
// PLAN T2.4's six acceptance behaviours each have a test named for it:
//
//   - launch refused at 75% session: TestTickGovernorLaunchRefusedAt75
//   - a running session paused at 90% and resumed after its reset:
//     TestTickGovernorPausesAt90AndResumesAfterTheReset
//   - extra usage on refuses to run: TestTickGovernorExtraUsageRefusesToRun
//   - a stale reading holds: TestTickGovernorStaleReadingHoldsAnIdleProject
//     and TestTickGovernorStaleReadingLeavesARunningSessionAlone
//   - an unsupported provider follows on_unknown_usage:
//     TestTickGovernorUnsupportedFollowsOnUnknownUsage
//   - logged out blocks launches: TestTickGovernorLoggedOutBlocksLaunches
//
// Load-bearing tests, and what breaks without them:
//
//   - TestTickGovernorPausesAt90AndResumesAfterTheReset: the stop is made
//     with the pause already in the ledger, and the relaunch with it already
//     cleared (stopCall.paused, relaunchCall.paused). Either order reversed
//     and a daemon killed in between restarts a session inside its limit, or
//     stops the one it has just resumed. Its two hours of ticks that never
//     call the usage source are the only check that a long pause does not
//     probe on every tick, and `restarts` staying at zero is what keeps a
//     pause from using up the recovery limit.
//   - TestTickGovernorPausedSessionIsLeftAlone: a paused session's host is
//     gone on purpose. Three hundred ticks with recovery switched on, across
//     a new daemon, must relaunch nothing, give nothing up and report no
//     stall. This is the test that fails if the pause is ever kept anywhere
//     but the ledger.
//   - TestTickGovernorNeverAsksAboutAnIdleProject: behind the usage source is
//     the agent CLI's probe. A project with nothing to do must not run it.
//   - TestTickGovernorSameVerdictLogsOnce: the governor's own sentence changes
//     with every reading. One line and one write of the state file for one
//     verdict is what keeps the log and the file from growing by a line a
//     tick.
//   - TestTickGovernorFailedStopIsForcedThenReported: the forced stop leaves
//     the ledger alone. If it rewrote the pause time, the report to the owner
//     would be pushed back on every try and a session that will not stop
//     would spend in silence.
//   - TestTickGovernorFailedRelaunchLeavesThePauseCleared: with the pause put
//     back, a session that cannot be started would be tried on every tick
//     with nothing to bound it.
//   - TestTickGovernorRecreatedSessionHasNoUsageFields: left in place, the
//     fields would have the next tick stop the session that was just created.
//   - TestTickGovernorRateLimitMarker's last rows: nothing removes the marker,
//     so one that is older than the attempt, or older than the resume, must
//     pause nothing; and with no usage source it must pause nothing at all,
//     because nothing could ever start the session again.
//
// Every tick here is made with a brand-new Deps (rig.tick builds one each
// time), and the tests that say "across a restart" hand the same folders to a
// brand-new rig (reborn). So nothing these tests show can be coming from a
// value the loop kept between ticks.

// governedRig is a rig with the whole governor wired in: a usage source reading
// 10% used, a logged-in agent, and the means to stop a session, start it
// again and type into it.
func governedRig(t *testing.T, tasks ...masterplan.Task) *rig {
	t.Helper()
	return newRig(t, tasks...).govern()
}

func (r *rig) govern() *rig {
	r.usageOn, r.loginOn, r.stopOn, r.relaunchOn, r.promptOn = true, true, true, true, true
	r.login = LoginView{Known: true, LoggedIn: true}
	r.usedPct = 10
	return r
}

// reborn is a new daemon with the governor wired in, pointed at the same
// folders. The fake account and the fake login are carried over, because they
// are the outside world and not the daemon; no record of a call is.
func (r *rig) reborn() *rig {
	n := r.restart().govern()
	n.usedPct, n.resetsAt, n.readAt, n.overage = r.usedPct, r.resetsAt, r.readAt, r.overage
	n.noReading, n.unsupported, n.onUnknown, n.login = r.noReading, r.unsupported, r.onUnknown, r.login
	return n
}

// rateLimit writes the session's rate-limit marker the way the agent's own
// hook does: one line of JSON with a time to the second.
func (r *rig) rateLimit(name string, at time.Time) {
	r.t.Helper()
	path := filepath.Join(r.sessionDir(name), ".rate-limited")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("{\"at\":%q}\n", stamp(at))), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// wantPaused checks that T1.1 is running and paused for usage, with the given
// wake time (empty for none), and returns the entry.
func (r *rig) wantPaused(pausedAt, wakeAt string) ledger.Entry {
	r.t.Helper()
	e := r.wantStatus("T1.1", ledger.Running)
	if e.UsagePauseAt != pausedAt || e.UsageWakeAt != wakeAt {
		r.t.Fatalf("usage_pause_at %q, usage_wake_at %q; want %q and %q (logs %v)",
			e.UsagePauseAt, e.UsageWakeAt, pausedAt, wakeAt, r.logs)
	}
	return e
}

// wantNotPaused checks that T1.1's entry holds neither usage field.
func (r *rig) wantNotPaused() ledger.Entry {
	r.t.Helper()
	e := r.entry("T1.1")
	if e.UsagePauseAt != "" || e.UsageWakeAt != "" {
		r.t.Fatalf("usage_pause_at %q, usage_wake_at %q; want neither (logs %v)", e.UsagePauseAt, e.UsageWakeAt, r.logs)
	}
	return e
}

func wantKind(t *testing.T, st daemonstate.State, kind ActionKind) {
	t.Helper()
	if st.Kind != string(kind) {
		t.Fatalf("state = %+v, want kind %s", st, kind)
	}
}

// titled returns the items with exactly this title, open or answered.
func (r *rig) titled(title string) []inbox.Item {
	r.t.Helper()
	var out []inbox.Item
	for _, it := range r.items() {
		if it.Title == title {
			out = append(out, it)
		}
	}
	return out
}

// pauseAt90 takes T1.1 to a session that was just paused: running, then one
// tick at 90% used with the reset resetIn away. It returns the pause time and
// the wake time as the ledger holds them.
func (r *rig) pauseAt90(resetIn time.Duration) (pausedAt, wakeAt string) {
	r.t.Helper()
	r.running()
	r.usedPct, r.resetsAt = 90, r.clock.Add(resetIn)
	pausedAt, wakeAt = stamp(*r.clock), stamp(r.resetsAt.Add(time.Minute))
	wantKind(r.t, r.tick(), ActUsagePause)
	r.wantPaused(pausedAt, wakeAt)
	return pausedAt, wakeAt
}

// TestTickGovernorLaunchRefusedAt75: at the launch line nothing starts, and
// nobody is told: it ends by itself at the reset. Below it the task starts.
func TestTickGovernorLaunchRefusedAt75(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.usedPct, r.resetsAt = 75, r.clock.Add(2*time.Hour)

	st := r.tick()
	wantKind(t, st, ActUsageWait)
	if st.Task != "T1.1" || !strings.Contains(st.Reason, "close to the limit") {
		t.Errorf("state = %+v, want a wait for T1.1 that says usage is close to the limit", st)
	}
	r.ticks(20, time.Minute)
	r.wantStatus("T1.1", ledger.Pending)
	if len(r.launches) != 0 || r.preflights != 0 {
		t.Fatalf("launches %d, preflights %d at 75%%; want none: a held launch must not even run the preflight",
			len(r.launches), r.preflights)
	}
	if len(r.items()) != 0 || len(r.notified) != 0 || len(r.urgent) != 0 {
		t.Errorf("items %d, notified %d, urgent %d; want nothing said about no_launch",
			len(r.items()), len(r.notified), len(r.urgent))
	}
	r.wantDecisions("governor")

	r.usedPct = 74.9
	wantKind(t, r.tick(), ActLaunch)
	r.wantStatus("T1.1", ledger.Running)
	if len(r.launches) != 1 {
		t.Fatalf("launches below the line = %d, want 1", len(r.launches))
	}
	// The second governor line is the hold ending.
	r.wantDecisions("governor", "launch", "governor")
}

// TestTickGovernorSameVerdictLogsOnce: two readings with different numbers
// under one unchanged verdict give one decision-log line and one write of the
// state file.
func TestTickGovernorSameVerdictLogsOnce(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.usedPct, r.resetsAt = 76, r.clock.Add(2*time.Hour)
	first := r.tick()
	wantKind(t, first, ActUsageWait)
	text, err := os.ReadFile(daemonstate.StatePath(r.root))
	if err != nil {
		t.Fatal(err)
	}

	r.advance(5 * time.Minute)
	r.usedPct = 88
	second := r.tick()
	if second != first {
		t.Errorf("state after a second reading = %+v, want the stored one unchanged, time included: %+v", second, first)
	}
	if now, _ := os.ReadFile(daemonstate.StatePath(r.root)); string(now) != string(text) {
		t.Errorf("the state file was written again for the same verdict:\n%s\n%s", text, now)
	}
	if r.usageCalls != 2 {
		t.Errorf("usage source calls = %d, want 2: both ticks would have launched", r.usageCalls)
	}
	r.wantDecisions("governor")

	// The verdict itself changes, with the kind of state staying the same:
	// that is a new line, and for a hold the one notification.
	r.readAt = r.clock.Add(-time.Hour)
	third := r.tick()
	wantKind(t, third, ActUsageWait)
	wantContains(t, "the state", third.Reason, "no recent usage reading")
	r.ticks(5, time.Minute)
	r.wantDecisions("governor", "governor")
}

// TestTickGovernorPausedReasonsLogOnce: a paused session's state is reworded
// as the pause goes on (asked to stop, stopped until a time, waiting for a
// reading), and none of that is a new decision. The state file follows the
// words; the decision log gets the one line for becoming paused.
func TestTickGovernorPausedReasonsLogOnce(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.stopIgnored = true
	r.pauseAt90(time.Hour)

	r.advance(5 * time.Second)
	asked := r.tick()
	wantKind(t, asked, ActUsagePaused)
	wantContains(t, "the state", asked.Reason, "waiting for it to stop")

	r.stopHost("t1-1")
	r.advance(5 * time.Second)
	until := r.tick()
	wantKind(t, until, ActUsagePaused)
	if until.Reason == asked.Reason || !strings.Contains(until.Reason, "paused for usage until") {
		t.Errorf("state once the host is gone = %+v, want it to say until when", until)
	}

	// Past the wake time with usage still at the pause line.
	*r.clock = r.resetsAt.Add(2 * time.Minute)
	r.resetsAt = r.clock.Add(5 * time.Hour)
	waiting := r.tick()
	wantKind(t, waiting, ActUsagePaused)
	if waiting.Reason == until.Reason || !strings.Contains(waiting.Reason, "new usage reading") {
		t.Errorf("state past the wake time = %+v, want it to say a new reading decides", waiting)
	}
	if len(r.relaunches) != 0 {
		t.Fatalf("relaunches at the pause line = %d, want 0", len(r.relaunches))
	}
	r.wantDecisions("launch", "usage_pause", "governor")
}

// TestTickGovernorPausesAt90AndResumesAfterTheReset is the whole of a pause:
// stopped once at the pause line, left alone until the reset, started again
// once in the same conversation, and told to carry on once.
func TestTickGovernorPausesAt90AndResumesAfterTheReset(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	wt := filepath.Join(r.root, "wt", "t1-1")
	pausedAt, wakeAt := r.pauseAt90(2 * time.Hour)

	// The stop was asked for with the pause already on disk.
	if want := []stopCall{{worktree: wt, session: "t1-1", force: false, paused: pausedAt}}; !slices.Equal(r.stops, want) {
		t.Fatalf("stops = %+v, want %+v", r.stops, want)
	}

	// Until the wake time: nothing is done, and nothing is asked.
	asked, checked := r.usageCalls, r.loginCalls
	var st daemonstate.State
	for r.clock.Add(5 * time.Minute).Before(r.resetsAt.Add(time.Minute)) {
		r.advance(5 * time.Minute)
		st = r.tick()
	}
	wantKind(t, st, ActUsagePaused)
	wantContains(t, "the paused state", st.Reason, "paused for usage until")
	if r.usageCalls != asked || r.loginCalls != checked {
		t.Errorf("usage source calls %d, login checks %d during a pause with a known wake time; want %d and %d, none new",
			r.usageCalls, r.loginCalls, asked, checked)
	}
	if len(r.stops) != 1 || len(r.relaunches) != 0 || len(r.prompts) != 0 || len(r.items()) != 0 {
		t.Fatalf("while paused: stops %d, relaunches %d, prompts %d, items %d; want 1, 0, 0, 0",
			len(r.stops), len(r.relaunches), len(r.prompts), len(r.items()))
	}
	r.wantPaused(pausedAt, wakeAt)

	// At the wake time, with a reading that allows work.
	*r.clock = r.resetsAt.Add(time.Minute)
	r.usedPct, r.resetsAt = 10, r.clock.Add(5*time.Hour)
	wantKind(t, r.tick(), ActUsageResume)
	want := []relaunchCall{{req: RelaunchRequest{Worktree: wt, Session: "t1-1", ResumeID: "sid-t1-1"}, counted: 0, paused: ""}}
	if !slices.Equal(r.relaunches, want) {
		t.Fatalf("relaunches = %+v, want %+v", r.relaunches, want)
	}
	e := r.wantNotPaused()
	if e.Restarts != 0 || !e.ContinuePending || e.RestartAt != stamp(*r.clock) {
		t.Fatalf("after the resume: restarts %d, continue_pending %v, restart_at %s; want 0, true and %s",
			e.Restarts, e.ContinuePending, e.RestartAt, stamp(*r.clock))
	}

	// Thirty seconds later, and not a second sooner, the resume command.
	r.advance(DefaultContinueAfter - time.Second)
	r.tick()
	if len(r.prompts) != 0 {
		t.Fatalf("prompts a second before ContinueAfter = %+v, want none", r.prompts)
	}
	r.advance(time.Second)
	wantKind(t, r.tick(), ActContinue)
	if want := []typed{{worktree: wt, session: "t1-1", text: "/afk:go --session t1-1"}}; !slices.Equal(r.prompts, want) {
		t.Fatalf("prompts = %+v, want %+v", r.prompts, want)
	}

	r.ticks(20, 5*time.Second)
	if len(r.prompts) != 1 || len(r.relaunches) != 1 || len(r.stops) != 1 {
		t.Errorf("twenty ticks on: prompts %d, relaunches %d, stops %d; want 1 each", len(r.prompts), len(r.relaunches), len(r.stops))
	}
	if e := r.wantNotPaused(); e.Restarts != 0 || e.ContinuePending || e.Status != ledger.Running {
		t.Errorf("entry at the end = %+v, want running with no restart counted and nothing pending", e)
	}
	if n := len(r.items()); n != 0 {
		t.Errorf("items = %d, want 0: a pause that works asks the owner nothing", n)
	}
	// One line for the pause and one for the resume. The two governor lines
	// are the state file entering and leaving "paused".
	r.wantDecisions("launch", "usage_pause", "governor", "usage_resume", "governor", "continue")
}

// TestTickGovernorResumeAcrossARestart: the pause, the resume and the prompt
// are each made by a different daemon, with nothing but the folders between
// them. The daemon that comes up just after the resume must not stop the
// session again.
func TestTickGovernorResumeAcrossARestart(t *testing.T) {
	a := governedRig(t, task("T1.1"))
	a.pauseAt90(time.Hour)

	b := a.reborn()
	b.ticks(5, time.Minute)
	if len(b.stops) != 0 || len(b.relaunches) != 0 {
		t.Fatalf("a new daemon during the pause: stops %d, relaunches %d; want none", len(b.stops), len(b.relaunches))
	}
	*b.clock = b.resetsAt.Add(time.Minute)
	b.usedPct, b.resetsAt = 10, b.clock.Add(5*time.Hour)
	wantKind(t, b.tick(), ActUsageResume)
	if len(b.relaunches) != 1 || b.relaunches[0].paused != "" {
		t.Fatalf("relaunches = %+v, want one, made with the pause already cleared from the ledger", b.relaunches)
	}

	c := b.reborn()
	c.ticks(12, 5*time.Second)
	if len(c.stops) != 0 || len(c.relaunches) != 0 {
		t.Fatalf("a new daemon after the resume: stops %d, relaunches %d; want none", len(c.stops), len(c.relaunches))
	}
	if len(c.prompts) != 1 {
		t.Errorf("prompts = %+v, want the resume command typed once", c.prompts)
	}
	if c.count("usage_pause") != 1 || c.count("usage_resume") != 1 {
		t.Errorf("decision log = %v, want one usage_pause and one usage_resume", c.decisions())
	}
}

// TestTickGovernorPausedSessionIsLeftAlone: a session paused for a week, with
// its host gone, every kind of recovery switched on and a question showing at
// its terminal, is not relaunched, restarted, given up on, typed into or
// reported, however many ticks pass and whichever daemon makes them.
func TestTickGovernorPausedSessionIsLeftAlone(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.restartOn = true
	r.running()
	r.setAwaiting("t1-1", true)
	r.usedPct, r.resetsAt = 95, r.clock.Add(7*24*time.Hour)
	pausedAt, wakeAt := stamp(*r.clock), stamp(r.resetsAt.Add(time.Minute))
	wantKind(t, r.tick(), ActUsagePause)
	before := r.ledgerText()

	r.ticks(150, time.Minute)
	n := r.reborn()
	n.restartOn = true
	st := n.ticks(150, time.Minute)
	wantKind(t, st, ActUsagePaused)

	for _, d := range []*rig{r, n} {
		if len(d.relaunches) != 0 || len(d.restarts) != 0 || len(d.prompts) != 0 {
			t.Errorf("relaunches %d, restarts %d, prompts %d on a paused session; want none",
				len(d.relaunches), len(d.restarts), len(d.prompts))
		}
	}
	if len(r.stops) != 1 || len(n.stops) != 0 {
		t.Errorf("stops = %d then %d, want 1 then 0", len(r.stops), len(n.stops))
	}
	if n.usageCalls != 0 {
		t.Errorf("usage source calls by the new daemon = %d, want 0 before the wake time", n.usageCalls)
	}
	if items := n.items(); len(items) != 0 {
		t.Errorf("items = %+v, want none: no stall, no stopped host, no giving up", items)
	}
	if n.ledgerText() != before {
		t.Errorf("the ledger changed during the pause:\n%s\nwas\n%s", n.ledgerText(), before)
	}
	n.wantPaused(pausedAt, wakeAt)
	n.wantDecisions("launch", "usage_pause", "governor")
}

// TestTickGovernorExtraUsageRefusesToRun: with extra usage on nothing
// launches, the owner gets one item at the highest priority, and the loop
// answers it by itself once extra usage is off.
func TestTickGovernorExtraUsageRefusesToRun(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.overage = true

	st := r.tick()
	wantKind(t, st, ActUsageWait)
	wantContains(t, "the state", st.Reason, "extra usage is switched on")
	r.ticks(10, time.Minute)
	n := r.reborn()
	n.ticks(10, time.Minute)

	items := n.titled(OverageTitle)
	if len(items) != 1 || len(n.items()) != 1 {
		t.Fatalf("items = %+v, want exactly one, titled %q", n.items(), OverageTitle)
	}
	it := items[0]
	if !it.Open() || !it.Blocking || it.Kind != "failure" || it.Task != "" || it.Session != "" {
		t.Errorf("item = %+v, want open, blocking, a failure, and about no task or session", it)
	}
	wantContains(t, "the item", it.Body, "Switch extra usage off", "You do not need to answer")
	if len(r.urgent) != 1 || len(r.notified) != 0 || len(n.urgent) != 0 || len(n.notified) != 0 {
		t.Errorf("urgent %d and %d, ordinary %d and %d; want one urgent notification from the first daemon only",
			len(r.urgent), len(n.urgent), len(r.notified), len(n.notified))
	}
	if len(r.launches)+len(n.launches) != 0 || r.preflights+n.preflights != 0 {
		t.Fatalf("launches or preflights with extra usage on; want none")
	}
	n.wantStatus("T1.1", ledger.Pending)

	n.overage = false
	wantKind(t, n.tick(), ActLaunch)
	if got := n.titled(OverageTitle); len(got) != 1 || got[0].Open() || got[0].Answer != "fixed" {
		t.Errorf("item after extra usage went off = %+v, want it answered as fixed", got)
	}
	n.wantStatus("T1.1", ledger.Running)
	n.wantDecisions("overage", "governor", "overage_off", "launch", "governor")
}

// TestTickGovernorExtraUsageStopsARunningSession: a running agent could be
// spending real money, so it is stopped like a pause, with no wake time, and
// started again when a reading shows extra usage off.
func TestTickGovernorExtraUsageStopsARunningSession(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.running()
	r.overage = true
	pausedAt := stamp(*r.clock)
	wantKind(t, r.tick(), ActUsagePause)
	r.wantPaused(pausedAt, "")
	if len(r.stops) != 1 || len(r.titled(OverageTitle)) != 1 || len(r.urgent) != 1 {
		t.Fatalf("stops %d, items %d, urgent %d; want 1 each", len(r.stops), len(r.titled(OverageTitle)), len(r.urgent))
	}

	st := r.ticks(30, time.Minute)
	wantKind(t, st, ActUsagePaused)
	if len(r.relaunches) != 0 || len(r.titled(OverageTitle)) != 1 || len(r.items()) != 1 {
		t.Fatalf("with extra usage still on: relaunches %d, items %d; want 0 and 1", len(r.relaunches), len(r.items()))
	}
	r.wantStatus("T1.1", ledger.Running)

	r.overage = false
	r.advance(time.Minute)
	wantKind(t, r.tick(), ActUsageResume)
	if len(r.relaunches) != 1 {
		t.Fatalf("relaunches once extra usage is off = %d, want 1", len(r.relaunches))
	}
	r.wantNotPaused()
	if got := r.titled(OverageTitle); got[0].Open() {
		t.Errorf("the item is still open after extra usage went off: %+v", got[0])
	}
}

// TestTickGovernorStaleReadingHoldsAnIdleProject: a reading older than
// stale_after holds a launch, and the owner is told once.
func TestTickGovernorStaleReadingHoldsAnIdleProject(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.readAt = r.clock.Add(-governor.DefaultThresholds().StaleAfter)

	st := r.tick()
	wantKind(t, st, ActUsageWait)
	wantContains(t, "the state", st.Reason, "no recent usage reading")
	r.ticks(20, time.Minute)
	if len(r.launches) != 0 || r.preflights != 0 {
		t.Fatalf("launches %d, preflights %d on a stale reading; want none", len(r.launches), r.preflights)
	}
	if len(r.items()) != 0 {
		t.Errorf("items = %+v, want none for a hold", r.items())
	}
	r.wantDecisions("governor")

	// A new reading ends it.
	r.readAt = time.Time{}
	wantKind(t, r.tick(), ActLaunch)
}

// TestTickGovernorStaleReadingLeavesARunningSessionAlone: a hold stops new
// work only. The session goes on, and because the tick's state stays the
// plain wait it was, nothing is sent and nothing is logged. That silence is
// by design: the hold shows in the usage file, not in the loop's.
func TestTickGovernorStaleReadingLeavesARunningSessionAlone(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.running()
	r.readAt = r.clock.Add(-time.Hour)
	before := r.ledgerText()
	asked := r.usageCalls

	st := r.ticks(20, 5*time.Second)
	wantKind(t, st, ActWait)
	if r.usageCalls != asked+20 {
		t.Errorf("usage source calls = %d, want %d: a running task is asked about on every tick", r.usageCalls, asked+20)
	}
	if len(r.stops) != 0 || len(r.relaunches) != 0 || r.ledgerText() != before {
		t.Fatalf("stops %d, relaunches %d, or the ledger changed under a hold", len(r.stops), len(r.relaunches))
	}
	if len(r.items()) != 0 {
		t.Errorf("items %d; want none", len(r.items()))
	}
	r.wantDecisions("launch")
}

// TestTickGovernorUnsupportedFollowsOnUnknownUsage: a tool that cannot report
// usage runs or holds as the machine's on_unknown_usage says.
func TestTickGovernorUnsupportedFollowsOnUnknownUsage(t *testing.T) {
	cases := []struct {
		name, onUnknown string
		wantKind        ActionKind
		wantLaunches    int
	}{
		{name: "run", onUnknown: governor.UnknownRun, wantKind: ActLaunch, wantLaunches: 1},
		{name: "hold", onUnknown: governor.UnknownHold, wantKind: ActUsageWait},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := governedRig(t, task("T1.1"))
			r.unsupported, r.onUnknown = true, tc.onUnknown
			wantKind(t, r.tick(), tc.wantKind)
			r.ticks(10, time.Minute)
			if len(r.launches) != tc.wantLaunches {
				t.Errorf("launches %d; want %d", len(r.launches), tc.wantLaunches)
			}
			if len(r.stops) != 0 || len(r.items()) != 0 {
				t.Errorf("stops %d, items %d; want none", len(r.stops), len(r.items()))
			}
		})
	}
}

// TestTickGovernorLoggedOutBlocksLaunches: with the agent logged out nothing
// launches, whatever usage says, and the owner gets one item at the highest
// priority, answered by the loop once the agent is logged in. A session that
// is already running is left alone.
func TestTickGovernorLoggedOutBlocksLaunches(t *testing.T) {
	t.Run("nothing launches", func(t *testing.T) {
		r := governedRig(t, task("T1.1"))
		r.login = LoginView{Known: true, LoggedIn: false}

		st := r.tick()
		wantKind(t, st, ActLoginWait)
		wantContains(t, "the state", st.Reason, "logged out")
		r.ticks(10, time.Minute)
		n := r.reborn()
		n.ticks(10, time.Minute)
		if len(r.launches)+len(n.launches) != 0 || r.preflights+n.preflights != 0 {
			t.Fatalf("launches or preflights while logged out; want none")
		}
		items := n.titled(LoginTitle)
		if len(items) != 1 || len(n.items()) != 1 {
			t.Fatalf("items = %+v, want exactly one, titled %q", n.items(), LoginTitle)
		}
		if it := items[0]; !it.Open() || !it.Blocking || it.Kind != "failure" || it.Task != "" || it.Session != "" {
			t.Errorf("item = %+v, want open, blocking, a failure, and about no task or session", it)
		}
		wantContains(t, "the item", items[0].Body, "Log the agent in again", "afk daemon status")
		if len(r.urgent) != 1 || len(n.urgent) != 0 || len(r.notified)+len(n.notified) != 0 {
			t.Errorf("urgent %d and %d, ordinary %d; want one urgent notification", len(r.urgent), len(n.urgent), len(r.notified)+len(n.notified))
		}

		n.login.LoggedIn = true
		wantKind(t, n.tick(), ActLaunch)
		if got := n.titled(LoginTitle); got[0].Open() || got[0].Answer != "fixed" {
			t.Errorf("item after the login = %+v, want it answered as fixed", got[0])
		}
		n.wantDecisions("logged_out", "governor", "logged_in", "launch", "governor")
	})

	t.Run("an unknown login holds nothing", func(t *testing.T) {
		r := governedRig(t, task("T1.1"))
		r.login = LoginView{}
		wantKind(t, r.tick(), ActLaunch)
		if len(r.items()) != 0 {
			t.Errorf("items = %+v, want none: unknown is not logged out", r.items())
		}
	})

	t.Run("a running session is left alone", func(t *testing.T) {
		r := governedRig(t, task("T1.1"), task("T1.2"))
		r.running()
		r.login = LoginView{Known: true, LoggedIn: false}
		before := r.ledgerText()
		st := r.ticks(20, 5*time.Second)
		wantKind(t, st, ActWait)
		if len(r.stops) != 0 || r.ledgerText() != before {
			t.Errorf("stops %d, or the ledger changed, for a running session while logged out", len(r.stops))
		}
		if len(r.titled(LoginTitle)) != 1 || len(r.urgent) != 1 {
			t.Errorf("items %d, urgent %d; want the one item all the same", len(r.titled(LoginTitle)), len(r.urgent))
		}
	})
}

// TestTickGovernorNeverAsksAboutAnIdleProject: the usage source and the login
// check are called only on a tick where the answer can change something. Each
// row is a project where it cannot.
func TestTickGovernorNeverAsksAboutAnIdleProject(t *testing.T) {
	cases := []struct {
		name  string
		setup func(r *rig)
		want  ActionKind
	}{
		{name: "every task is done", want: ActIdle, setup: func(r *rig) {
			r.setEntry(ledger.Entry{ID: "T1.1", Status: ledger.Done})
		}},
		{name: "a failed task blocks the rest", want: ActIdle, setup: func(r *rig) {
			r.setEntry(ledger.Entry{ID: "T1.1", Status: ledger.Failed})
		}},
		{name: "paused by the owner", want: ActWait, setup: func(r *rig) {
			if err := daemonstate.Pause(r.root, *r.clock); err != nil {
				r.t.Fatal(err)
			}
		}},
		{name: "no ledger", want: ActNoLedger, setup: func(r *rig) { r.noLedger() }},
		{name: "a task that is shipping", want: ActShipStep, setup: func(r *rig) {
			r.shipFn = func(req ShipRequest) ship.Outcome {
				return ship.Outcome{Kind: ship.OutcomeWait, Reason: "waiting on checks", Progress: req.Progress}
			}
			r.setEntry(ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1", Attempts: 1,
				StartedAt: stamp(*r.clock)})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := governedRig(t, task("T1.1"))
			// A verdict that would hold everything, were it asked for.
			r.overage = true
			r.login = LoginView{Known: true, LoggedIn: false}
			tc.setup(r)
			st := r.ticks(10, time.Minute)
			wantKind(t, st, tc.want)
			if r.usageCalls != 0 || r.loginCalls != 0 {
				t.Errorf("usage source calls %d, login checks %d; want 0 and 0", r.usageCalls, r.loginCalls)
			}
			if len(r.titled(OverageTitle))+len(r.titled(LoginTitle)) != 0 {
				t.Errorf("items = %+v, want neither standing item", r.items())
			}
		})
	}
}

// TestTickGovernorIdleProjectGetsItsItemAnswered: an idle project is not
// asked about, with one exception. While it has one of the standing items
// open it is, or the item raised when it last had work would stay open for
// ever. Once the item is answered the asking stops again.
func TestTickGovernorIdleProjectGetsItsItemAnswered(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.overage = true
	r.tick()
	if len(r.titled(OverageTitle)) != 1 {
		t.Fatalf("items = %+v, want the extra-usage item", r.items())
	}
	// The owner finishes the task by hand: nothing is left to start.
	r.setEntry(ledger.Entry{ID: "T1.1", Status: ledger.Done})

	wantKind(t, r.ticks(5, time.Minute), ActIdle)
	if got := r.titled(OverageTitle); len(got) != 1 || !got[0].Open() {
		t.Fatalf("items with extra usage still on = %+v, want the one item, still open", got)
	}
	asked := r.usageCalls
	if asked != 6 {
		t.Errorf("usage source calls = %d, want 6: the open item keeps the project asked about", asked)
	}

	r.overage = false
	wantKind(t, r.ticks(1, time.Minute), ActIdle)
	if got := r.titled(OverageTitle); len(got) != 1 || got[0].Open() || got[0].Answer != "fixed" {
		t.Errorf("item after extra usage went off = %+v, want it answered as fixed", got)
	}
	r.ticks(10, time.Minute)
	if r.usageCalls != asked+1 {
		t.Errorf("usage source calls = %d, want %d: with the item answered the idle project is left alone again", r.usageCalls, asked+1)
	}
	if len(r.urgent) != 1 {
		t.Errorf("urgent notifications = %d, want 1", len(r.urgent))
	}
}

// TestTickGovernorRateLimitMarker: a marker the agent's own hook wrote when a
// turn ended on a usage limit pauses the session, though the reading itself
// still says run. The wake time is the window's reset when one is known and
// an hour when none is.
func TestTickGovernorRateLimitMarker(t *testing.T) {
	cases := []struct {
		name string
		// setup runs with T1.1 running, a minute after its launch.
		setup func(r *rig)
		// wantWake is how long after the pause the wake time is; zero means
		// the session is not paused at all.
		wantWake time.Duration
	}{
		{name: "the reset is known", wantWake: 3*time.Hour + time.Minute, setup: func(r *rig) {
			r.resetsAt = r.clock.Add(3 * time.Hour)
			r.rateLimit("t1-1", *r.clock)
		}},
		{name: "no reset is known", wantWake: DefaultRateLimitFallback, setup: func(r *rig) {
			r.rateLimit("t1-1", *r.clock)
		}},
		{name: "the reset is already past", wantWake: DefaultRateLimitFallback, setup: func(r *rig) {
			r.resetsAt = r.clock.Add(-10 * time.Minute)
			r.rateLimit("t1-1", *r.clock)
		}},
		{name: "through Deps.RateLimited", wantWake: DefaultRateLimitFallback, setup: func(r *rig) {
			at := *r.clock
			r.markerFn = func(worktree, sess string) (time.Time, bool) {
				if worktree != filepath.Join(r.root, "wt", "t1-1") || sess != "t1-1" {
					r.t.Errorf("marker asked for %s %s", worktree, sess)
				}
				return at, true
			}
		}},
		{name: "a marker from before this attempt", setup: func(r *rig) {
			r.rateLimit("t1-1", r.clock.Add(-time.Hour))
		}},
		{name: "a marker in the second the attempt began", setup: func(r *rig) {
			r.rateLimit("t1-1", r.clock.Add(-time.Minute))
		}},
		{name: "a damaged marker", setup: func(r *rig) {
			if err := os.WriteFile(filepath.Join(r.sessionDir("t1-1"), ".rate-limited"), []byte("{not json"), 0o644); err != nil {
				r.t.Fatal(err)
			}
		}},
		{name: "no usage source", setup: func(r *rig) {
			r.usageOn = false
			r.rateLimit("t1-1", *r.clock)
		}},
		{name: "no means to stop a session", setup: func(r *rig) {
			r.stopOn = false
			r.rateLimit("t1-1", *r.clock)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := governedRig(t, task("T1.1"))
			r.running()
			tc.setup(r)
			before := r.ledgerText()
			pausedAt := *r.clock
			st := r.tick()

			if tc.wantWake == 0 {
				wantKind(t, st, ActWait)
				r.ticks(10, 5*time.Second)
				if len(r.stops) != 0 || r.ledgerText() != before {
					t.Fatalf("stops %d, or the ledger changed; want the session left alone:\n%s", len(r.stops), r.ledgerText())
				}
				return
			}
			wantKind(t, st, ActUsagePause)
			wantContains(t, "the state", st.Reason, "hit a usage limit")
			r.wantPaused(stamp(pausedAt), stamp(pausedAt.Add(tc.wantWake)))
			if len(r.stops) != 1 || r.stops[0].force {
				t.Fatalf("stops = %+v, want one polite stop", r.stops)
			}
		})
	}
}

// TestTickGovernorRateLimitMarkerEpisode: the reading says run all the way
// through, so the wake time is the only thing holding the session back, and
// the marker, which nothing removes, is the only thing that could stop it a
// second time. Neither happens.
func TestTickGovernorRateLimitMarkerEpisode(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.running()
	r.rateLimit("t1-1", *r.clock)
	wantKind(t, r.tick(), ActUsagePause)
	wake := r.clock.Add(DefaultRateLimitFallback)

	st := r.ticks(20, time.Minute)
	wantKind(t, st, ActUsagePaused)
	if len(r.relaunches) != 0 {
		t.Fatalf("relaunches before the wake time = %d, want 0: the reading that says run is not enough", len(r.relaunches))
	}

	*r.clock = wake
	wantKind(t, r.tick(), ActUsageResume)
	if len(r.relaunches) != 1 {
		t.Fatalf("relaunches at the wake time = %d, want 1", len(r.relaunches))
	}
	r.ticks(20, 5*time.Second)
	if len(r.stops) != 1 || len(r.relaunches) != 1 {
		t.Errorf("after the resume: stops %d, relaunches %d; want 1 and 1, the old marker pauses nothing", len(r.stops), len(r.relaunches))
	}
	r.wantNotPaused()
	if r.count("usage_pause") != 1 || r.count("usage_resume") != 1 {
		t.Errorf("decision log = %v, want one usage_pause and one usage_resume", r.decisions())
	}
}

// TestTickGovernorFailedStopIsForcedThenReported: a host that does not end
// when asked is made to after PauseRetry, and one that is still there after
// three times as long is reported to the owner, once, and its task parked.
func TestTickGovernorFailedStopIsForcedThenReported(t *testing.T) {
	t.Run("the forced stop works", func(t *testing.T) {
		r := governedRig(t, task("T1.1"))
		r.stopIgnored = true
		pausedAt, wakeAt := r.pauseAt90(time.Hour)
		before := r.ledgerText()

		st := r.ticks(11, 5*time.Second)
		wantKind(t, st, ActUsagePaused)
		wantContains(t, "the state", st.Reason, "waiting for it to stop")
		if len(r.stops) != 1 {
			t.Fatalf("stops before PauseRetry = %+v, want only the first", r.stops)
		}

		r.advance(5 * time.Second)
		wantKind(t, r.tick(), ActUsagePause)
		if len(r.stops) != 2 || !r.stops[1].force || r.stops[1].paused != pausedAt {
			t.Fatalf("stops at PauseRetry = %+v, want a second one, forced", r.stops)
		}
		if r.ledgerText() != before {
			t.Errorf("the forced stop changed the ledger:\n%s\nwas\n%s", r.ledgerText(), before)
		}

		st = r.ticks(60, 5*time.Second)
		wantKind(t, st, ActUsagePaused)
		if len(r.stops) != 2 || len(r.items()) != 0 || len(r.relaunches) != 0 {
			t.Errorf("after the forced stop: stops %d, items %d, relaunches %d; want 2, 0, 0", len(r.stops), len(r.items()), len(r.relaunches))
		}
		r.wantPaused(pausedAt, wakeAt)
		if r.count("usage_pause") != 1 {
			t.Errorf("decision log = %v, want one usage_pause: the forced stop is the same decision", r.decisions())
		}
	})

	t.Run("nothing stops it", func(t *testing.T) {
		r := governedRig(t, task("T1.1"))
		r.stopIgnored, r.forceIgnored = true, true
		pausedAt, wakeAt := r.pauseAt90(time.Hour)
		before := r.ledgerText()

		// A stop that errors is logged and tried again, like one ignored.
		r.stopErr = errors.New("no socket")
		r.advance(DefaultPauseRetry)
		wantKind(t, r.tick(), ActUsagePause)
		if len(r.stops) != 2 || !r.stops[1].force || r.ledgerText() != before {
			t.Fatalf("stops %+v; want a forced stop and the ledger untouched", r.stops)
		}
		wantContains(t, "the log", strings.Join(r.logs, "\n"), "could not be made to stop", "no socket")
		r.stopErr = nil

		r.advance(2 * DefaultPauseRetry)
		st := r.tick()
		wantKind(t, st, ActUsageStuck)
		title := PauseFailedTitlePrefix + "T1.1 (t1-1)"
		items := r.titled(title)
		if len(items) != 1 || len(r.items()) != 1 {
			t.Fatalf("items = %+v, want one titled %q", r.items(), title)
		}
		it := items[0]
		if !it.Blocking || it.Kind != "failure" || it.Task != "T1.1" || it.Session != "t1-1" {
			t.Errorf("item = %+v, want a blocking failure about T1.1 and t1-1", it)
		}
		wantContains(t, "the item", it.Body, "still running", "afk attach t1-1", "afk clean t1-1")
		wantContains(t, "the state", st.Reason, it.ID)
		if len(r.urgent) != 1 || len(r.notified) != 0 {
			t.Errorf("urgent %d, ordinary %d; want one urgent notification", len(r.urgent), len(r.notified))
		}

		// The next tick parks on it, and that is the end of the reports.
		wantKind(t, r.tick(), ActPark)
		stops := len(r.stops)
		r.ticks(20, time.Minute)
		e := r.wantStatus("T1.1", ledger.Parked)
		if !slices.Equal(e.ParkedOn, []string{it.ID}) || e.UsagePauseAt != pausedAt || e.UsageWakeAt != wakeAt {
			t.Errorf("parked entry = %+v, want parked on %s with its pause kept", e, it.ID)
		}
		if len(r.items()) != 1 || len(r.urgent) != 1 || len(r.stops) != stops {
			t.Errorf("twenty ticks on: items %d, urgent %d, stops %d; want 1, 1 and %d", len(r.items()), len(r.urgent), len(r.stops), stops)
		}

		// The owner stops it by hand and answers. Un-parked, it is not typed
		// into (it has no host): it is started again in its conversation.
		r.stopHost("t1-1")
		r.answer(it.ID)
		*r.clock = r.resetsAt.Add(2 * time.Minute)
		r.usedPct, r.resetsAt = 10, r.clock.Add(5*time.Hour)
		wantKind(t, r.tick(), ActUnpark)
		if len(r.prompts) != 0 || len(r.relaunches) != 0 {
			t.Fatalf("the un-park typed %+v or relaunched %d; want neither on that tick", r.prompts, len(r.relaunches))
		}
		r.wantPaused(pausedAt, wakeAt)
		wantKind(t, r.tick(), ActUsageResume)
		if len(r.relaunches) != 1 || r.relaunches[0].req.ResumeID != "sid-t1-1" {
			t.Fatalf("relaunches = %+v, want one, resuming the conversation", r.relaunches)
		}
		r.wantNotPaused()
		r.ticks(12, 5*time.Second)
		if len(r.prompts) != 1 {
			t.Errorf("prompts = %+v, want the resume command once, after the relaunch", r.prompts)
		}
		if r.count("usage_stuck") != 1 || r.count("park") != 1 {
			t.Errorf("decision log = %v, want one usage_stuck and one park", r.decisions())
		}
	})
}

// TestTickGovernorFailedRelaunchLeavesThePauseCleared: a resume whose
// relaunch fails does not put the pause back. The entry is then an ordinary
// running task with no host, and recovery starts it after the grace and
// counts it.
func TestTickGovernorFailedRelaunchLeavesThePauseCleared(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.pauseAt90(time.Hour)
	*r.clock = r.resetsAt.Add(time.Minute)
	r.usedPct, r.resetsAt = 10, r.clock.Add(5*time.Hour)
	r.relaunchErr = errors.New("no pty")

	st := r.tick()
	wantKind(t, st, ActUsageResume)
	wantContains(t, "the state", st.Reason, "could not be started again", "no pty")
	if len(r.relaunches) != 1 || r.relaunches[0].paused != "" {
		t.Fatalf("relaunches = %+v, want one, made with the pause already cleared", r.relaunches)
	}
	e := r.wantNotPaused()
	if e.Restarts != 0 || e.ContinuePending || e.RestartAt != stamp(*r.clock) || e.Status != ledger.Running {
		t.Fatalf("entry after the failed resume = %+v; want running, no restart counted, nothing pending, restart_at %s", e, stamp(*r.clock))
	}

	// Inside the host's grace nothing is tried again.
	r.relaunchErr = nil
	r.ticks(12, 5*time.Second)
	if len(r.relaunches) != 1 || len(r.stops) != 1 {
		t.Fatalf("inside the grace: relaunches %d, stops %d; want 1 and 1", len(r.relaunches), len(r.stops))
	}
	r.advance(time.Second)
	wantKind(t, r.tick(), ActRelaunch)
	if len(r.relaunches) != 2 || r.relaunches[1].counted != 1 {
		t.Fatalf("relaunches past the grace = %+v, want a second one, counted as a restart", r.relaunches)
	}
	r.wantRecovery(1, true)
	r.wantNotPaused()
	if r.count("usage_resume") != 1 || r.count("relaunch") != 1 || len(r.items()) != 0 {
		t.Errorf("decision log = %v, items %d; want one usage_resume, one relaunch and no item", r.decisions(), len(r.items()))
	}
}

// TestTickGovernorRecreatedSessionHasNoUsageFields: a paused task whose
// session is no longer on disk is created again as a new session, and the
// pause, which was about the old one, goes with it.
func TestTickGovernorRecreatedSessionHasNoUsageFields(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	pausedAt, wakeAt := r.pauseAt90(time.Hour)
	if err := os.RemoveAll(filepath.Join(r.root, "wt", "t1-1")); err != nil {
		t.Fatal(err)
	}

	// Usage still at the pause line: creating a session is a start, and held.
	r.advance(5 * time.Second)
	wantKind(t, r.tick(), ActUsageWait)
	r.wantPaused(pausedAt, wakeAt)
	if len(r.launches) != 1 {
		t.Fatalf("launches at the pause line = %d, want only the first", len(r.launches))
	}

	r.usedPct = 10
	r.advance(5 * time.Second)
	wantKind(t, r.tick(), ActCreate)
	if len(r.launches) != 2 || r.launches[1].Session != "t1-1" {
		t.Fatalf("launches = %+v, want the session t1-1 created again", r.launches)
	}
	if e := r.wantNotPaused(); e.Status != ledger.Running || e.Attempts != 1 {
		t.Errorf("entry = %+v, want running, the same attempt", e)
	}

	r.ticks(20, 5*time.Second)
	if len(r.stops) != 1 || len(r.relaunches) != 0 || len(r.prompts) != 0 {
		t.Errorf("after the session was created again: stops %d, relaunches %d, prompts %+v; want 1, 0 and none",
			len(r.stops), len(r.relaunches), r.prompts)
	}
}

// TestTickGovernorPauseIsClearedWhenTheTaskMovesOn: the pause is about one
// session's run. A paused task that finishes, fails or is started over holds
// neither field afterwards.
func TestTickGovernorPauseIsClearedWhenTheTaskMovesOn(t *testing.T) {
	cases := []struct {
		name string
		move func(r *rig)
		want ledger.Status
	}{
		{name: "its session finished", want: ledger.Shipping, move: func(r *rig) {
			r.setPhase("t1-1", state.PhaseDone)
		}},
		{name: "its session failed", want: ledger.Failed, move: func(r *rig) {
			r.setPhase("t1-1", state.PhaseFailed)
		}},
		{name: "set back to pending and launched again", want: ledger.Running, move: func(r *rig) {
			e := r.entry("T1.1")
			e.Status = ledger.Pending
			r.setEntry(e)
			r.usedPct = 10
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := governedRig(t, task("T1.1"))
			r.pauseAt90(time.Hour)
			tc.move(r)
			r.advance(5 * time.Second)
			r.tick()
			e := r.wantNotPaused()
			if e.Status != tc.want {
				t.Errorf("status = %s, want %s", e.Status, tc.want)
			}
			if len(r.relaunches) != 0 {
				t.Errorf("relaunches = %d, want none", len(r.relaunches))
			}
		})
	}
}

// TestTickGovernorNoMeans: with a usage source but no way to stop a session,
// or none to start one again, a session at the pause line is left running.
// Stopping what cannot be started again would be worse than the agent's own
// wait at the limit.
func TestTickGovernorNoMeans(t *testing.T) {
	for _, name := range []string{"no stop", "no relaunch"} {
		t.Run(name, func(t *testing.T) {
			r := governedRig(t, task("T1.1"))
			r.stopOn, r.relaunchOn = name != "no stop", name != "no relaunch"
			r.running()
			r.usedPct, r.resetsAt = 95, r.clock.Add(time.Hour)
			before := r.ledgerText()
			st := r.ticks(10, 5*time.Second)
			wantKind(t, st, ActWait)
			if len(r.stops) != 0 || r.ledgerText() != before {
				t.Errorf("stops %d, or the ledger changed; want the session left running", len(r.stops))
			}
		})
	}
}

// TestSettingsUsageDefaults: a Settings with neither usage limit set runs
// with the documented ones. The marker row above shows the fallback; this
// shows the retry, at a second short of it and at it.
func TestSettingsUsageDefaults(t *testing.T) {
	r := governedRig(t, task("T1.1"))
	r.stopIgnored = true
	if s := r.settings(); s.PauseRetry != 0 || s.RateLimitFallback != 0 {
		t.Fatalf("the rig sets the usage limits (%v, %v); this test needs them left at zero", s.PauseRetry, s.RateLimitFallback)
	}
	r.pauseAt90(time.Hour)
	r.advance(DefaultPauseRetry - time.Second)
	r.tick()
	if len(r.stops) != 1 {
		t.Fatalf("stops a second before DefaultPauseRetry = %d, want 1", len(r.stops))
	}
	r.advance(time.Second)
	r.tick()
	if len(r.stops) != 2 || !r.stops[1].force {
		t.Fatalf("stops at DefaultPauseRetry = %+v, want a forced second one", r.stops)
	}
}
