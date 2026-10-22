package ui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// advancingClock returns a Deps.Now stub and the knob that moves it. Model's
// deps is a value field, so a test can swap Now on its own copy mid-test;
// testDeps' stub is constant, and every assertion here turns on two writes
// landing at different times.
func advancingClock(start time.Time) (func() time.Time, func(time.Duration)) {
	now := start
	return func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }
}

// TestNotifyRecordsTextKindAndStamp walks every kind through the helper. The
// load-bearing row is notifyInfo: it is the zero value, so a kind that failed
// to be recorded at all would still read as info, and the case asserts the
// text and stamp alongside it rather than the kind on its own.
func TestNotifyRecordsTextKindAndStamp(t *testing.T) {
	cases := []struct {
		name string
		kind notificationKind
		text string
	}{
		{"info", notifyInfo, "opening a…"},
		{"success", notifySuccess, "cleaned a"},
		{"warn", notifyWarn, "no session selected"},
		{"error", notifyError, "restart failed for a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("a"))
			m.notify(tc.kind, tc.text)
			if m.status != tc.text {
				t.Fatalf("status = %q, want %q", m.status, tc.text)
			}
			if m.statusKind != tc.kind {
				t.Fatalf("statusKind = %d, want %d", m.statusKind, tc.kind)
			}
			if m.statusChangedAt != m.deps.Now() {
				t.Fatalf("statusChangedAt = %v, want the clock's %v", m.statusChangedAt, m.deps.Now())
			}
			if !m.toastVisible() {
				t.Fatalf("a just-recorded notification must toast")
			}
		})
	}
}

// TestNotificationKindZeroValueIsInfo pins the zero value. A dozen-odd tests
// in this package assign m.status directly and never set a kind; with the
// toast styled per kind, they must land on the informational style rather
// than on whatever sits at 0 by accident.
func TestNotificationKindZeroValueIsInfo(t *testing.T) {
	var k notificationKind
	if k != notifyInfo {
		t.Fatalf("zero notificationKind = %d, want notifyInfo (%d)", k, notifyInfo)
	}
	m := testModel(sess("a"))
	if m.statusKind != notifyInfo {
		t.Fatalf("a fresh model's statusKind = %d, want notifyInfo", m.statusKind)
	}
	m.status = "written straight to the field, as the older tests do"
	if m.statusKind != notifyInfo {
		t.Fatalf("a direct status write left statusKind = %d, want notifyInfo", m.statusKind)
	}
}

// TestNotifyStampsUnlessTheToastStillShowsTheText pins notify's one skip:
// identical text while its toast is visible. Every step is load-bearing:
//
//   - repeat-while-visible: the same text arriving inside toastTTL must not
//     re-stamp, or a message rewritten identically by a 2s tick would
//     re-float its toast forever. It also proves the first write is visible
//     to the second call — a stamp deferred until after the handler returned
//     would leave the second comparing against the pre-handler text.
//   - repeat-new-kind: the kind is recorded even on a skipped repeat, so a
//     toast still on screen restyles instead of contradicting itself.
//   - repeat-after-expiry: the same text arriving once the toast has expired
//     MUST re-stamp. This is the row that distinguishes the visible-toast
//     rule from a bare text comparison; with the footer gone, a skip here is
//     a repeat the user never sees.
//   - changed: a genuinely new message stamps at the current clock.
//   - empty: notify("") has no freshness to record; only clearStatus should
//     be emptying the field, and neither may stamp.
func TestNotifyStampsUnlessTheToastStillShowsTheText(t *testing.T) {
	m := testModel(sess("a"))
	now, advance := advancingClock(time.Unix(1_000_000, 0))
	m.deps.Now = now

	m.notify(notifyInfo, "creating a…")
	first := m.statusChangedAt
	if first != now() {
		t.Fatalf("statusChangedAt = %v, want the clock's %v", first, now())
	}

	advance(time.Second)
	m.notify(notifyInfo, "creating a…")
	if m.statusChangedAt != first {
		t.Fatalf("an identical repeat under a visible toast re-stamped: %v, want %v", m.statusChangedAt, first)
	}

	advance(time.Second)
	m.notify(notifyWarn, "creating a…")
	if m.statusChangedAt != first {
		t.Fatalf("a repeat with a new kind re-stamped: %v, want %v", m.statusChangedAt, first)
	}
	if m.statusKind != notifyWarn {
		t.Fatalf("statusKind = %d, want the repeat's notifyWarn", m.statusKind)
	}

	advance(toastTTL)
	if m.toastVisible() {
		t.Fatalf("setup: the toast should have expired")
	}
	m.notify(notifyWarn, "creating a…")
	if m.statusChangedAt != now() {
		t.Fatalf("an identical repeat after the toast expired kept the stale stamp %v, want %v", m.statusChangedAt, now())
	}
	if !m.toastVisible() {
		t.Fatalf("a repeat after expiry must float a fresh toast")
	}

	advance(time.Second)
	m.notify(notifySuccess, "created a")
	if m.statusChangedAt != now() {
		t.Fatalf("a changed message stamped %v, want the current clock %v", m.statusChangedAt, now())
	}

	advance(time.Second)
	changed := m.statusChangedAt
	m.notify(notifyInfo, "")
	if m.statusChangedAt != changed {
		t.Fatalf("an empty notification stamped: %v, want %v", m.statusChangedAt, changed)
	}
	if m.toastVisible() {
		t.Fatalf("an empty status must never toast")
	}
}

// TestRepeatedIdenticalBackgroundEventReachesTheScreen: a session waits on
// the user, is answered, and waits again ten seconds later. Both events carry
// the byte-identical text and the first toast has expired by the time the
// second lands, so a stamp rule that compared text alone would clear the
// batch as delivered with nothing on screen. Driven through Update so the
// production wiring (applyLoaded diff → flushBackground → notify) is what is
// verified, not the helper in isolation.
func TestRepeatedIdenticalBackgroundEventReachesTheScreen(t *testing.T) {
	m := testModel(sess("a"))
	now, adv := advancingClock(time.Unix(1_000_000, 0))
	m.deps.Now = now
	tick := func(s Session) {
		m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{s}, at: now()})
	}

	tick(awaiting(sess("a")))
	if m.status != "a is awaiting input" {
		t.Fatalf("status = %q after the first awaiting-input transition, want the event", m.status)
	}
	first := m.statusChangedAt

	adv(10 * time.Second)
	tick(sess("a"))
	if m.toastVisible() {
		t.Fatalf("setup: the first toast should have expired before the session is answered")
	}
	if m.status != "a is awaiting input" {
		t.Fatalf("status = %q after the answer, want the expired text left in the slot (nothing else notified)", m.status)
	}

	adv(10 * time.Second)
	tick(awaiting(sess("a")))
	if m.status != "a is awaiting input" || m.statusKind != notifyWarn {
		t.Fatalf("status = %q kind = %d after the second transition, want the awaiting-input event again", m.status, m.statusKind)
	}
	if m.statusChangedAt == first || m.statusChangedAt != now() {
		t.Fatalf("second identical event stamped %v (first %v), want the current clock %v — a stale stamp is a dropped toast", m.statusChangedAt, first, now())
	}
	if !m.toastVisible() {
		t.Fatalf("the second awaiting-input event must be on screen")
	}
	if len(m.pendingBackground) != 0 {
		t.Fatalf("pending = %d after the flush, want 0", len(m.pendingBackground))
	}
}

// TestRepeatedRefusalToastsAgain is the user-initiated half of the same rule:
// `r` on a healthy running row is refused with a fixed message (its blocked
// reason, runLevelAction), and a user who presses it again ten seconds later
// is owed the refusal again. Under a bare text comparison the second press
// would leave the expired toast expired and the key would appear to do
// nothing.
func TestRepeatedRefusalToastsAgain(t *testing.T) {
	m := testModel(runningSess("a"))
	now, adv := advancingClock(time.Unix(1_000_000, 0))
	m.deps.Now = now

	m, _ = update(t, m, keyRune('r'))
	if !strings.Contains(m.status, "Only a stopped or stuck agent can be restarted") {
		t.Fatalf("status = %q, want the restart refusal", m.status)
	}
	first := m.statusChangedAt

	adv(10 * time.Second)
	if m.toastVisible() {
		t.Fatalf("setup: the first refusal's toast should have expired")
	}
	m, _ = update(t, m, keyRune('r'))
	if m.statusChangedAt == first || !m.toastVisible() {
		t.Fatalf("second `r` left the stamp at %v with toastVisible=%v, want a fresh toast at %v", m.statusChangedAt, m.toastVisible(), now())
	}
}

// TestNotifyStampsBeforeHandleReturns is the reason stamping moved out of
// Update's wrapper. The wrapper stamped only after handle had returned, so a
// handler could not read the timestamp of a notification it had just issued —
// which is exactly what the background-versus-user precedence rule has to do.
// Here the stamp is already in place on the model handle hands back, before
// Update has done anything with it.
func TestNotifyStampsBeforeHandleReturns(t *testing.T) {
	m := testModel(sess("a"))
	next, _ := m.handle(keyRune('R')) // R with no usage reader says why it cannot read
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("handle returned %T, want ui.Model", next)
	}
	if got.status == "" {
		t.Fatalf("R should have notified")
	}
	if got.statusChangedAt != got.deps.Now() {
		t.Fatalf("statusChangedAt = %v, want the clock's %v before Update returns", got.statusChangedAt, got.deps.Now())
	}

	// A second notification in the same Update sees the first one's write:
	// with the first toast still visible (the clock is inside toastTTL), the
	// identical text is recognised as already on screen and not re-stamped.
	seen := got.statusChangedAt
	got.deps.Now = func() time.Time { return seen.Add(time.Second) }
	got.notify(notifyError, got.status)
	if got.statusChangedAt != seen {
		t.Fatalf("the second notification re-stamped %v, want the first's %v", got.statusChangedAt, seen)
	}
}

// TestClearStatusEmptiesWithoutRewindingTheStamp covers the four sites that
// used to write the empty string directly. The stamp is deliberately left
// where it was: toastVisible checks the empty status first, so there is
// nothing to suppress, and the assertion pins that so a future clear does not
// quietly acquire time semantics.
func TestClearStatusEmptiesWithoutRewindingTheStamp(t *testing.T) {
	m := testModel(sess("a"))
	m.notify(notifyError, "one-off session: exit status 1")
	stamped := m.statusChangedAt

	m.clearStatus()
	if m.status != "" {
		t.Fatalf("status = %q, want empty", m.status)
	}
	if m.statusKind != notifyInfo {
		t.Fatalf("statusKind = %d, want notifyInfo after a clear", m.statusKind)
	}
	if m.statusChangedAt != stamped {
		t.Fatalf("clearStatus moved the stamp to %v, want %v", m.statusChangedAt, stamped)
	}
	if m.toastVisible() {
		t.Fatalf("a cleared status must not toast")
	}
}

// --- background arbitration ---

// bgStep is one action in a background-arbitration scenario. Exactly one
// field is set per step. flush stands in for the Update boundary the
// production wrapper provides, so a scenario can group events into "one
// Update" without a message that emits them (W4 adds those).
type bgStep struct {
	bg      *backgroundEvent // notifyBackground, collected until the next flush
	user    *backgroundEvent // notify: a user-initiated write (category unused)
	advance time.Duration    // move the clock
	flush   bool             // flushBackground: the end of one Update
}

func bg(kind notificationKind, cat backgroundCategory, text string) bgStep {
	return bgStep{bg: &backgroundEvent{kind: kind, category: cat, text: text}}
}
func user(kind notificationKind, text string) bgStep {
	return bgStep{user: &backgroundEvent{kind: kind, text: text}}
}
func advance(d time.Duration) bgStep { return bgStep{advance: d} }
func flush() bgStep                  { return bgStep{flush: true} }

// TestBackgroundArbitration is the slot rule, one scenario per row. The
// load-bearing rows:
//
//   - one-event-keeps-text: a lone event must land verbatim, or every W4
//     notification would arrive as a count of one.
//   - three-events-one-summary: three events collected in one Update produce
//     ONE write, with counts by category — the whole reason the batch exists.
//   - mixed-kind-worst-wins: a batch spanning success and warn is styled as
//     the warn, so a failure is never dressed in a success border.
//   - deferred-behind-user-toast: a background event arriving while a
//     user-initiated toast is live must leave the status untouched AND stay
//     pending — dropping it would satisfy "not replaced" and lose the event.
//   - user-displaces-background: a user write never waits, whatever is in the
//     slot.
//   - second-arrival-coalesces-with-deferred: a background event arriving
//     while another is deferred joins it, so what eventually lands is one
//     summary, not the newest event alone (which would lose the first).
//   - background-yields-to-live-background: the second of two background
//     messages milliseconds apart defers rather than clobbering the first —
//     the policy chosen for projectCmds' three separate replies.
//   - expired-toast-frees-the-slot: once the live toast is past toastTTL the
//     next flush lands the deferred batch; this is the promotion the wrapper
//     performs, tested here at the function level and again through Update
//     in TestDeferredBackgroundPromotesOnTick.
func TestBackgroundArbitration(t *testing.T) {
	cases := []struct {
		name        string
		steps       []bgStep
		wantStatus  string
		wantKind    notificationKind
		wantPending int
	}{
		{
			name:       "one-event-keeps-text",
			steps:      []bgStep{bg(notifySuccess, bgFinished, "turt-99 finished"), flush()},
			wantStatus: "turt-99 finished",
			wantKind:   notifySuccess,
		},
		{
			name: "three-events-one-summary",
			steps: []bgStep{
				bg(notifySuccess, bgFinished, "turt-98 finished"),
				bg(notifyWarn, bgHostDied, "turt-97 host died"),
				bg(notifySuccess, bgFinished, "turt-99 finished"),
				flush(),
			},
			wantStatus: "2 finished, 1 host died",
			wantKind:   notifyWarn,
		},
		{
			name: "mixed-kind-worst-wins",
			steps: []bgStep{
				bg(notifySuccess, bgFinished, "turt-98 finished"),
				bg(notifyError, bgFailed, "turt-97 failed"),
				bg(notifyInfo, bgAwaitingInput, "turt-96 is awaiting input"),
				flush(),
			},
			wantStatus: "1 finished, 1 failed, 1 awaiting input",
			wantKind:   notifyError,
		},
		{
			name: "deferred-behind-user-toast",
			steps: []bgStep{
				user(notifyInfo, "opening turt-1…"),
				advance(time.Second),
				bg(notifySuccess, bgFinished, "turt-99 finished"),
				flush(),
			},
			wantStatus:  "opening turt-1…",
			wantKind:    notifyInfo,
			wantPending: 1,
		},
		{
			name: "user-displaces-background",
			steps: []bgStep{
				bg(notifySuccess, bgFinished, "turt-99 finished"),
				flush(),
				advance(time.Second),
				user(notifyWarn, "no session selected"),
			},
			wantStatus: "no session selected",
			wantKind:   notifyWarn,
		},
		{
			name: "second-arrival-coalesces-with-deferred",
			steps: []bgStep{
				user(notifyInfo, "opening turt-1…"),
				bg(notifySuccess, bgFinished, "turt-99 finished"),
				flush(),
				advance(2 * time.Second),
				bg(notifyWarn, bgHostDied, "turt-98 host died"),
				flush(),
				advance(toastTTL),
				flush(),
			},
			wantStatus: "1 finished, 1 host died",
			wantKind:   notifyWarn,
		},
		{
			name: "background-yields-to-live-background",
			steps: []bgStep{
				bg(notifyError, bgStackError, "stack error: gs log short failed"),
				flush(),
				bg(notifySuccess, bgFinished, "turt-99 finished"),
				flush(),
			},
			wantStatus:  "stack error: gs log short failed",
			wantKind:    notifyError,
			wantPending: 1,
		},
		{
			name: "expired-toast-frees-the-slot",
			steps: []bgStep{
				user(notifyInfo, "opening turt-1…"),
				bg(notifySuccess, bgFinished, "turt-99 finished"),
				flush(),
				advance(toastTTL),
				flush(),
			},
			wantStatus: "turt-99 finished",
			wantKind:   notifySuccess,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("a"))
			now, adv := advancingClock(time.Unix(1_000_000, 0))
			m.deps.Now = now
			for _, st := range tc.steps {
				switch {
				case st.bg != nil:
					m.notifyBackground(st.bg.kind, st.bg.category, st.bg.text)
				case st.user != nil:
					m.notify(st.user.kind, st.user.text)
				case st.advance != 0:
					adv(st.advance)
				case st.flush:
					m.flushBackground()
				}
			}
			if m.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", m.status, tc.wantStatus)
			}
			if m.statusKind != tc.wantKind {
				t.Fatalf("statusKind = %d, want %d", m.statusKind, tc.wantKind)
			}
			if len(m.pendingBackground) != tc.wantPending {
				t.Fatalf("pending = %d events, want %d", len(m.pendingBackground), tc.wantPending)
			}
		})
	}
}

// TestCoalesceBackgroundSummary pins the summary's shape: counts in category
// order (not arrival order, so the same batch always reads the same way),
// pluralised per category, no key hint. The load-bearing rows are
// plural-host-died — "2 host died" is the obvious bug — and
// category-order-not-arrival, which a naive map iteration would randomise.
func TestCoalesceBackgroundSummary(t *testing.T) {
	cases := []struct {
		name   string
		events []backgroundEvent
		want   string
	}{
		{
			name: "plural-host-died",
			events: []backgroundEvent{
				{notifyWarn, bgHostDied, "a host died"},
				{notifyWarn, bgHostDied, "b host died"},
			},
			want: "2 hosts died",
		},
		{
			name: "category-order-not-arrival",
			events: []backgroundEvent{
				{notifyError, bgBranchesError, "branch listing failed"},
				{notifyWarn, bgHostDied, "a host died"},
				{notifySuccess, bgFinished, "b finished"},
			},
			want: "1 finished, 1 host died, 1 branch listing error",
		},
		{
			name: "every-category-named",
			events: []backgroundEvent{
				{notifySuccess, bgFinished, ""},
				{notifyError, bgFailed, ""},
				{notifyInfo, bgAwaitingInput, ""},
				{notifyWarn, bgHostDied, ""},
				{notifyError, bgDiscoveryError, ""},
				{notifyError, bgStackError, ""},
				{notifyError, bgBranchesError, ""},
			},
			want: "1 finished, 1 failed, 1 awaiting input, 1 host died, 1 discovery error, 1 stack error, 1 branch listing error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := coalesceBackground(tc.events)
			if got != tc.want {
				t.Fatalf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWorstKindOrdering pins the severity order the coalescer's worst-wins
// rule reads straight off the constants: error > warn > success > info. If a
// kind is ever inserted out of order, this fails before a mixed batch renders
// a failure in a success border.
func TestWorstKindOrdering(t *testing.T) {
	if !(notifyError > notifyWarn && notifyWarn > notifySuccess && notifySuccess > notifyInfo) {
		t.Fatalf("kind constants are not ordered by severity: info=%d success=%d warn=%d error=%d",
			notifyInfo, notifySuccess, notifyWarn, notifyError)
	}
	kind, _ := coalesceBackground([]backgroundEvent{
		{notifySuccess, bgFinished, "a"},
		{notifyWarn, bgHostDied, "b"},
		{notifySuccess, bgFinished, "c"},
	})
	if kind != notifyWarn {
		t.Fatalf("coalesced kind = %d, want notifyWarn", kind)
	}
}

// TestDeferredBackgroundPromotesOnTick is the production wiring: a deferred
// background notification reaches m.status through Update, not through a
// direct flushBackground call. It also pins the latency the design accepts —
// a tick landing while the user toast is still live leaves the deferral in
// place; the next tick after toastTTL promotes it. Nothing here calls
// flushBackground; if the wrapper ever stops calling it, this test is what
// turns red.
func TestDeferredBackgroundPromotesOnTick(t *testing.T) {
	m := testModel(sess("a"))
	now, adv := advancingClock(time.Unix(1_000_000, 0))
	m.deps.Now = now

	m.notify(notifyInfo, "opening a…")
	m.notifyBackground(notifySuccess, bgFinished, "turt-99 finished")

	adv(time.Second)
	m, _ = update(t, m, tickMsg(now()))
	if m.status != "opening a…" {
		t.Fatalf("status = %q after a tick inside the TTL, want the user toast left alone", m.status)
	}
	if len(m.pendingBackground) != 1 {
		t.Fatalf("pending = %d after a tick inside the TTL, want the event still deferred", len(m.pendingBackground))
	}

	adv(toastTTL)
	m, _ = update(t, m, tickMsg(now()))
	if m.status != "turt-99 finished" || m.statusKind != notifySuccess {
		t.Fatalf("status = %q kind = %d after a tick past the TTL, want the deferred event promoted", m.status, m.statusKind)
	}
	if m.statusChangedAt != now() {
		t.Fatalf("promoted notification stamped %v, want the promoting tick's %v", m.statusChangedAt, now())
	}
	if len(m.pendingBackground) != 0 {
		t.Fatalf("pending = %d after promotion, want 0", len(m.pendingBackground))
	}
}

// TestDiscoveryErrorIsBackgroundOrigin re-points W2.T1's discovery-error
// notification under the arbitration rules: it is tick-driven, so a failing
// pass landing while the user's own toast is live must wait rather than
// overwrite it, and land once the toast has expired. The per-failure
// suppression (TestDiscoveryErrorNotifiesOncePerFailure) is unchanged and
// still holds when nothing is live, which is why that test needed no edit.
func TestDiscoveryErrorIsBackgroundOrigin(t *testing.T) {
	m := testModel(sess("a"))
	now, adv := advancingClock(time.Unix(1_000_000, 0))
	m.deps.Now = now

	m.notify(notifyInfo, "opening a…")
	adv(time.Second)
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, err: errors.New("git worktree list: exit status 128"), at: now()})
	if m.status != "opening a…" {
		t.Fatalf("status = %q, want the user toast untouched by a background discovery error", m.status)
	}

	adv(toastTTL)
	m, _ = update(t, m, tickMsg(now()))
	if !strings.Contains(m.status, "exit status 128") || m.statusKind != notifyError {
		t.Fatalf("status = %q kind = %d, want the deferred discovery error promoted", m.status, m.statusKind)
	}
}

// TestSwitchProjectDropsDeferredBackground: a deferred event describes the
// outgoing project's sessions, and landing it under the incoming project's
// rows would name a session that is not on screen. switchProject already
// resets the discovery-error suppression for the same reason.
func TestSwitchProjectDropsDeferredBackground(t *testing.T) {
	d := testDeps()
	d.Projects = scannedProjects("/roots/a", "/roots/b")
	d.ActiveProjectIndex = 0
	m := New("/roots/a", time.Second, d)
	m.notify(notifyInfo, "opening a…")
	m.notifyBackground(notifySuccess, bgFinished, "a finished")
	m.flushBackground()
	if len(m.pendingBackground) != 1 {
		t.Fatalf("pending = %d, want the event deferred behind the live toast", len(m.pendingBackground))
	}
	m, _ = m.switchProject("/roots/b")
	if len(m.pendingBackground) != 0 {
		t.Fatalf("pending = %d after a project switch, want 0", len(m.pendingBackground))
	}
}
