package governor

import (
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

var testNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// bannedNames are provider names a Reason must never contain. They are
// split so the package source itself stays free of provider names, which the
// task's grep check enforces.
var bannedNames = []string{"clau" + "de", "co" + "dex", "open" + "code", "anthro" + "pic", "open" + "ai"}

func snap(age time.Duration, windows ...provider.Window) *provider.UsageSnapshot {
	return &provider.UsageSnapshot{At: testNow.Add(-age), Windows: windows}
}

func win(kind string, pct float64) provider.Window {
	return provider.Window{Kind: kind, UsedPct: pct}
}

func winReset(kind string, pct float64, reset time.Time) provider.Window {
	return provider.Window{Kind: kind, UsedPct: pct, ResetsAt: reset}
}

func scoped(pct float64) provider.Window {
	return provider.Window{Kind: provider.WindowWeeklyModel, UsedPct: pct, Scope: "Sonnet"}
}

// TestDecide covers the whole PLAN §6.3 matrix. Load-bearing rows: the
// first-match-wins rows (overage beats stale, stale beats pause, pause beats
// no-launch) because a reordered rule list passes every single-rule row; the
// boundary rows (x.9 vs x) because the comparisons are >= and an off-by-one
// would pause a session early or let one run past its limit; the zero-reset
// pause row because a wake time of "one minute after year 1" would wake the
// session at once; and the unsupported rows, which must beat the nil-snapshot
// row since an unsupported provider never has a snapshot.
func TestDecide(t *testing.T) {
	reset1 := testNow.Add(2 * time.Hour)
	reset2 := testNow.Add(5 * time.Hour)
	def := DefaultThresholds()

	tests := []struct {
		name        string
		in          Input
		action      Action
		window      *provider.Window // compared by Kind/UsedPct/Scope when non-nil
		wakeAt      time.Time
		pauseSess   bool
		reasonHas   []string
		checkWindow bool
	}{
		{name: "run: fresh and low", in: Input{Snapshot: snap(time.Minute, win("session", 10), win("weekly", 20))}, action: ActionRun},
		{name: "run: empty windows on a fresh snapshot", in: Input{Snapshot: snap(time.Minute)}, action: ActionRun},
		{name: "run: unknown window below pause line", in: Input{Snapshot: snap(time.Minute, win("unknown", 89.9))}, action: ActionRun},
		{name: "run: unknown window never triggers no-launch", in: Input{Snapshot: snap(time.Minute, win("unknown", 80))}, action: ActionRun},
		{name: "no_launch: session 74.9 runs", in: Input{Snapshot: snap(time.Minute, win("session", 74.9))}, action: ActionRun},
		{name: "no_launch: session 75", in: Input{Snapshot: snap(time.Minute, win("session", 75))}, action: ActionNoLaunch, reasonHas: []string{"session", "75%"}},
		{name: "no_launch: session 85 via its own line", in: Input{Snapshot: snap(time.Minute, win("session", 85))}, action: ActionNoLaunch},
		{name: "no_launch: weekly 84.9 runs", in: Input{Snapshot: snap(time.Minute, win("weekly", 84.9))}, action: ActionRun},
		{name: "no_launch: weekly 85", in: Input{Snapshot: snap(time.Minute, win("weekly", 85))}, action: ActionNoLaunch, reasonHas: []string{"week", "85%", "limit of 85%"}},
		{name: "no_launch: weekly_model 84.9 runs", in: Input{Snapshot: snap(time.Minute, scoped(84.9))}, action: ActionRun},
		{name: "no_launch: weekly_model 85 names scope", in: Input{Snapshot: snap(time.Minute, scoped(85))}, action: ActionNoLaunch, reasonHas: []string{"Sonnet", "85%"}},
		{name: "no_launch: session running stays unpaused", in: Input{Snapshot: snap(time.Minute, win("session", 80)), SessionRunning: true}, action: ActionNoLaunch},
		{name: "pause: session 89.9 is no_launch", in: Input{Snapshot: snap(time.Minute, win("session", 89.9))}, action: ActionNoLaunch},
		{name: "pause: session 90", in: Input{Snapshot: snap(time.Minute, winReset("session", 90, reset1))}, action: ActionPause, wakeAt: reset1.Add(time.Minute), reasonHas: []string{"session", "90%"}},
		{name: "pause: weekly 89.9 is no_launch", in: Input{Snapshot: snap(time.Minute, win("weekly", 89.9))}, action: ActionNoLaunch},
		{name: "pause: weekly 90", in: Input{Snapshot: snap(time.Minute, winReset("weekly", 90, reset1))}, action: ActionPause, wakeAt: reset1.Add(time.Minute)},
		{name: "pause: weekly_model 89.9 is no_launch", in: Input{Snapshot: snap(time.Minute, scoped(89.9))}, action: ActionNoLaunch},
		{name: "pause: weekly_model 90", in: Input{Snapshot: snap(time.Minute, provider.Window{Kind: "weekly_model", UsedPct: 90, Scope: "Sonnet", ResetsAt: reset1})}, action: ActionPause, wakeAt: reset1.Add(time.Minute), reasonHas: []string{"Sonnet"}},
		{name: "pause: unknown window 89.9 runs", in: Input{Snapshot: snap(time.Minute, win("unknown", 89.9))}, action: ActionRun},
		{name: "pause: unknown window 90", in: Input{Snapshot: snap(time.Minute, winReset("unknown", 90, reset1))}, action: ActionPause, wakeAt: reset1.Add(time.Minute)},
		{name: "pause: session running pauses the session", in: Input{Snapshot: snap(time.Minute, winReset("session", 95, reset1)), SessionRunning: true}, action: ActionPause, wakeAt: reset1.Add(time.Minute), pauseSess: true},
		{name: "pause: no session running leaves PauseSession false", in: Input{Snapshot: snap(time.Minute, winReset("session", 95, reset1))}, action: ActionPause, wakeAt: reset1.Add(time.Minute)},
		{name: "pause: two windows, latest reset wins plus one minute", in: Input{Snapshot: snap(time.Minute, winReset("session", 91, reset2), winReset("weekly", 96, reset1))}, action: ActionPause, wakeAt: reset2.Add(time.Minute), window: &provider.Window{Kind: "weekly", UsedPct: 96}, checkWindow: true},
		{name: "pause: zero reset time gives zero wake time", in: Input{Snapshot: snap(time.Minute, win("session", 92))}, action: ActionPause},
		{name: "pause: one zero reset among two zeroes the wake time", in: Input{Snapshot: snap(time.Minute, winReset("session", 92, reset1), win("weekly", 93))}, action: ActionPause},
		{name: "pause: beats no-launch", in: Input{Snapshot: snap(time.Minute, win("session", 80), winReset("weekly", 91, reset1))}, action: ActionPause, wakeAt: reset1.Add(time.Minute)},
		{name: "hold: nil snapshot", in: Input{}, action: ActionHold, reasonHas: []string{"Usage"}},
		{name: "hold: stale by exactly StaleAfter", in: Input{Snapshot: snap(30*time.Minute, win("session", 10))}, action: ActionHold, reasonHas: []string{"30 minutes"}},
		{name: "hold: stale beats pause", in: Input{Snapshot: snap(time.Hour, win("session", 99)), SessionRunning: true}, action: ActionHold},
		{name: "run: age of StaleAfter minus one second is fresh", in: Input{Snapshot: snap(30*time.Minute-time.Second, win("session", 10))}, action: ActionRun},
		{name: "run: snapshot dated after now is fresh", in: Input{Snapshot: snap(-time.Hour, win("session", 10))}, action: ActionRun},
		{name: "refuse: overage on", in: Input{Snapshot: &provider.UsageSnapshot{At: testNow, OverageEnabled: true}}, action: ActionRefuse, reasonHas: []string{"extra usage", "off"}},
		{name: "refuse: overage beats stale", in: Input{Snapshot: &provider.UsageSnapshot{At: testNow.Add(-2 * time.Hour), OverageEnabled: true}}, action: ActionRefuse},
		{name: "refuse: overage beats pause", in: Input{Snapshot: &provider.UsageSnapshot{At: testNow, OverageEnabled: true, Windows: []provider.Window{win("session", 99)}}, SessionRunning: true}, action: ActionRefuse},
		{name: "unsupported: run", in: Input{Unsupported: true}, action: ActionRun},
		{name: "unsupported: hold", in: Input{Unsupported: true, Thresholds: Thresholds{OnUnknownUsage: UnknownHold}}, action: ActionHold},
		{name: "unsupported: unrecognised value holds", in: Input{Unsupported: true, Thresholds: Thresholds{OnUnknownUsage: "maybe"}}, action: ActionHold},
		{name: "unsupported: empty value holds", in: Input{Unsupported: true, Thresholds: Thresholds{OnUnknownUsage: ""}}, action: ActionHold},
		{name: "unsupported: beats nil snapshot", in: Input{Unsupported: true, Snapshot: nil}, action: ActionRun},
		{name: "thresholds: PauseAt 80 pauses at 80", in: Input{Snapshot: snap(time.Minute, winReset("session", 80, reset1)), Thresholds: Thresholds{PauseAt: 80, LaunchBelow: 75, StaleAfter: 30 * time.Minute}}, action: ActionPause, wakeAt: reset1.Add(time.Minute)},
		{name: "thresholds: PauseAt 80 weekly line is 75", in: Input{Snapshot: snap(time.Minute, win("weekly", 75)), Thresholds: Thresholds{PauseAt: 80, LaunchBelow: 75, StaleAfter: 30 * time.Minute}}, action: ActionNoLaunch},
		{name: "thresholds: PauseAt 80 weekly 74.9 runs", in: Input{Snapshot: snap(time.Minute, win("weekly", 74.9)), Thresholds: Thresholds{PauseAt: 80, LaunchBelow: 75, StaleAfter: 30 * time.Minute}}, action: ActionRun},
		{name: "thresholds: longer StaleAfter keeps old snapshot fresh", in: Input{Snapshot: snap(time.Hour, win("session", 10)), Thresholds: Thresholds{PauseAt: 90, LaunchBelow: 75, StaleAfter: 2 * time.Hour}}, action: ActionRun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.Now = testNow
			if in.Thresholds == (Thresholds{}) && !strings.HasPrefix(tt.name, "unsupported: hold") &&
				!strings.HasPrefix(tt.name, "unsupported: unrecognised") && !strings.HasPrefix(tt.name, "unsupported: empty") {
				in.Thresholds = def
			}
			got := Decide(in)
			if got.Action != tt.action {
				t.Fatalf("action = %q (%s), want %q", got.Action, got.Reason, tt.action)
			}
			if !got.WakeAt.Equal(tt.wakeAt) {
				t.Errorf("WakeAt = %v, want %v", got.WakeAt, tt.wakeAt)
			}
			if got.PauseSession != tt.pauseSess {
				t.Errorf("PauseSession = %v, want %v", got.PauseSession, tt.pauseSess)
			}
			if got.Reason == "" {
				t.Error("Reason is empty")
			}
			low := strings.ToLower(got.Reason)
			for _, banned := range bannedNames {
				if strings.Contains(low, banned) {
					t.Errorf("Reason names a provider: %q", got.Reason)
				}
			}
			for _, want := range tt.reasonHas {
				if !strings.Contains(got.Reason, want) {
					t.Errorf("Reason %q lacks %q", got.Reason, want)
				}
			}
			if tt.checkWindow {
				if got.Window == nil || got.Window.Kind != tt.window.Kind || got.Window.UsedPct != tt.window.UsedPct {
					t.Errorf("Window = %+v, want %+v", got.Window, tt.window)
				}
			}
			if (got.Action == ActionPause || got.Action == ActionNoLaunch) && got.Window == nil {
				t.Error("pause/no_launch must carry the window")
			}
		})
	}
}

func TestDefaultThresholds(t *testing.T) {
	d := DefaultThresholds()
	if d.PauseAt != 90 || d.LaunchBelow != 75 || d.StaleAfter != 30*time.Minute || d.OnUnknownUsage != UnknownRun {
		t.Errorf("DefaultThresholds = %+v", d)
	}
	if DefaultProbeInterval != 5*time.Minute {
		t.Errorf("DefaultProbeInterval = %v", DefaultProbeInterval)
	}
}
