package usagetext

import (
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

func tp(t time.Time) *time.Time { return &t }

var base = time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

// TestLead: every governor action is a row, because these words are printed
// by `afk daemon status` and must stay byte for byte. The unknown-action row
// is load-bearing: a newer writer's word must show as stored, not be guessed.
func TestLead(t *testing.T) {
	wake := time.Date(2026, 1, 5, 15, 4, 0, 0, time.UTC)
	cases := []struct {
		name string
		g    *usagestate.Governor
		want string
	}{
		{"nil governor", nil, ""},
		{"empty action", &usagestate.Governor{}, ""},
		{"run", &usagestate.Governor{Action: "run"}, "within the limits"},
		{"no_launch", &usagestate.Governor{Action: "no_launch"}, "nothing new is started"},
		{"pause with wake", &usagestate.Governor{Action: "pause", WakeAt: &wake}, "paused until Mon 5 Jan 15:04"},
		{"pause without wake", &usagestate.Governor{Action: "pause"}, "paused, with no known time to start again"},
		{"hold", &usagestate.Governor{Action: "hold"}, "not known, so nothing new is started"},
		{"refuse", &usagestate.Governor{Action: "refuse"}, "afk will not run"},
		{"unknown action", &usagestate.Governor{Action: "sideways"}, "sideways"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Lead(c.g, nil); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestWindowLabel: one row per window kind; the fallthrough row keeps the
// "Other (...)" wording for kinds this build does not know.
func TestWindowLabel(t *testing.T) {
	cases := []struct{ name, kind, scope, want string }{
		{"session", provider.WindowSession, "", "Session"},
		{"weekly", provider.WindowWeekly, "", "Week (all models)"},
		{"weekly model", provider.WindowWeeklyModel, "Opus", "Week (Opus)"},
		{"unknown kind", provider.WindowUnknown, "x", "Other (x)"},
		{"unrecognised kind", "novel", "y", "Other (y)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WindowLabel(c.kind, c.scope); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestTightest: the tie rows are load-bearing, since a window that resets
// later holds the build back longer and an unknown reset counts as latest.
func TestTightest(t *testing.T) {
	early, late := tp(base.Add(time.Hour)), tp(base.Add(5*time.Hour))
	cases := []struct {
		name string
		ws   []usagestate.Window
		ok   bool
		kind string
	}{
		{"empty", nil, false, ""},
		{"single", []usagestate.Window{{Kind: "a", UsedPct: 5}}, true, "a"},
		{"highest wins", []usagestate.Window{{Kind: "a", UsedPct: 5}, {Kind: "b", UsedPct: 70}, {Kind: "c", UsedPct: 20}}, true, "b"},
		{"tie later reset wins", []usagestate.Window{{Kind: "a", UsedPct: 50, ResetsAt: early}, {Kind: "b", UsedPct: 50, ResetsAt: late}}, true, "b"},
		{"tie earlier listed later reset", []usagestate.Window{{Kind: "a", UsedPct: 50, ResetsAt: late}, {Kind: "b", UsedPct: 50, ResetsAt: early}}, true, "a"},
		{"tie unknown reset beats known", []usagestate.Window{{Kind: "a", UsedPct: 50, ResetsAt: late}, {Kind: "b", UsedPct: 50}}, true, "b"},
		{"tie known after unknown", []usagestate.Window{{Kind: "a", UsedPct: 50}, {Kind: "b", UsedPct: 50, ResetsAt: late}}, true, "a"},
		{"tie both unknown keeps first", []usagestate.Window{{Kind: "a", UsedPct: 50}, {Kind: "b", UsedPct: 50}}, true, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, ok := Tightest(c.ws)
			if ok != c.ok || w.Kind != c.kind {
				t.Errorf("got (%q, %v), want (%q, %v)", w.Kind, ok, c.kind, c.ok)
			}
		})
	}
}

// TestSegment: the no-reading, no-governor, no-windows and unknown-action rows
// are load-bearing (they must never panic or invent text); the 30 minute rows
// pin the stale boundary: exactly 30 minutes is still current.
func TestSegment(t *testing.T) {
	win := []usagestate.Window{{Kind: provider.WindowSession, UsedPct: 41.6}}
	cases := []struct {
		name         string
		f            usagestate.File
		known        bool
		wantL, wantS string
	}{
		{"no reading", usagestate.File{At: base, Windows: win}, false, "", ""},
		{"neither", usagestate.File{At: base}, true, "", ""},
		{"neither with empty action", usagestate.File{At: base, Governor: &usagestate.Governor{}}, true, "", ""},
		{"run", usagestate.File{At: base, Windows: win, Governor: &usagestate.Governor{Action: "run"}}, true,
			"usage: within the limits · Session 42%", "ok · 42%"},
		{"no_launch", usagestate.File{At: base, Windows: win, Governor: &usagestate.Governor{Action: "no_launch"}}, true,
			"usage: nothing new is started · Session 42%", "no new tasks · 42%"},
		{"pause with wake", usagestate.File{At: base, Windows: win, Governor: &usagestate.Governor{Action: "pause", WakeAt: tp(time.Date(2026, 1, 5, 15, 4, 0, 0, time.UTC))}}, true,
			"usage: paused until Mon 5 Jan 15:04 · Session 42%", "paused · 42%"},
		{"pause without wake", usagestate.File{At: base, Windows: win, Governor: &usagestate.Governor{Action: "pause"}}, true,
			"usage: paused, with no known time to start again · Session 42%", "paused · 42%"},
		{"hold", usagestate.File{At: base, Windows: win, Governor: &usagestate.Governor{Action: "hold"}}, true,
			"usage: not known, so nothing new is started · Session 42%", "unknown · 42%"},
		{"refuse", usagestate.File{At: base, Windows: win, Governor: &usagestate.Governor{Action: "refuse"}}, true,
			"usage: afk will not run · Session 42%", "refused · 42%"},
		{"unknown action", usagestate.File{At: base, Windows: win, Governor: &usagestate.Governor{Action: "sideways"}}, true,
			"usage: sideways · Session 42%", "sideways · 42%"},
		{"no governor", usagestate.File{At: base, Windows: win}, true, "usage: Session 42%", "42%"},
		{"no windows", usagestate.File{At: base, Governor: &usagestate.Governor{Action: "run"}}, true,
			"usage: within the limits", "ok"},
		{"tightest of several", usagestate.File{At: base, Windows: []usagestate.Window{
			{Kind: provider.WindowSession, UsedPct: 10}, {Kind: provider.WindowWeeklyModel, Scope: "Opus", UsedPct: 88.4}}}, true,
			"usage: Week (Opus) 88%", "88%"},
		{"rounds half up", usagestate.File{At: base, Windows: []usagestate.Window{{Kind: provider.WindowWeekly, UsedPct: 99.5}}}, true,
			"usage: Week (all models) 100%", "100%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, s := Segment(c.f, c.known, base, nil)
			if l != c.wantL || s != c.wantS {
				t.Errorf("got (%q, %q), want (%q, %q)", l, s, c.wantL, c.wantS)
			}
		})
	}
}

func TestSegmentStale(t *testing.T) {
	win := []usagestate.Window{{Kind: provider.WindowSession, UsedPct: 50}}
	g := &usagestate.Governor{Action: "run"}
	cases := []struct {
		name         string
		age          time.Duration
		wantL, wantS string
	}{
		{"just under", 29*time.Minute + 59*time.Second, "usage: within the limits · Session 50%", "ok · 50%"},
		{"exactly 30 minutes", 30 * time.Minute, "usage: within the limits · Session 50%", "ok · 50%"},
		{"just over", 30*time.Minute + time.Second, "usage: within the limits · Session 50% (read 30 minutes ago)", "ok · 50% (old)"},
		{"hours", 3 * time.Hour, "usage: within the limits · Session 50% (read 3 hours ago)", "ok · 50% (old)"},
		{"days", 72 * time.Hour, "usage: within the limits · Session 50% (read 3 days ago)", "ok · 50% (old)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := usagestate.File{At: base.Add(-c.age), Windows: win, Governor: g}
			l, s := Segment(f, true, base, nil)
			if l != c.wantL || s != c.wantS {
				t.Errorf("got (%q, %q), want (%q, %q)", l, s, c.wantL, c.wantS)
			}
		})
	}
}

// TestWeekLine: the tighter-window row is load-bearing, since the reset shown
// must belong to the window that binds, and the tighter window without a reset
// must drop the reset rather than borrow the other window's. The per-model row
// is load-bearing because a per-model weekly window must never stand in for
// the all-models week. The non-UTC row pins that the reset is shown in the
// owner's zone, not in the zone it was stored in.
func TestWeekLine(t *testing.T) {
	thu := time.Date(2026, 10, 22, 15, 57, 0, 0, time.UTC)
	fri := time.Date(2026, 10, 23, 9, 30, 0, 0, time.UTC)
	est := time.FixedZone("UTC-5", -5*60*60)
	cases := []struct {
		name string
		ws   []usagestate.Window
		loc  *time.Location
		want string
	}{
		{"both windows with reset", []usagestate.Window{
			{Kind: provider.WindowWeekly, UsedPct: 62, ResetsAt: tp(thu)},
			{Kind: provider.WindowSession, UsedPct: 28, ResetsAt: tp(fri)}}, nil,
			"week 62% · 5-hour 28% · resets Thu 15:57"},
		{"session is tighter", []usagestate.Window{
			{Kind: provider.WindowWeekly, UsedPct: 20, ResetsAt: tp(thu)},
			{Kind: provider.WindowSession, UsedPct: 90, ResetsAt: tp(fri)}}, nil,
			"week 20% · 5-hour 90% · resets Fri 09:30"},
		{"only weekly", []usagestate.Window{
			{Kind: provider.WindowWeekly, UsedPct: 62, ResetsAt: tp(thu)}}, nil,
			"week 62% · resets Thu 15:57"},
		{"only session", []usagestate.Window{
			{Kind: provider.WindowSession, UsedPct: 28, ResetsAt: tp(thu)}}, nil,
			"5-hour 28% · resets Thu 15:57"},
		{"none", nil, nil, ""},
		{"per model only", []usagestate.Window{
			{Kind: provider.WindowWeeklyModel, Scope: "Opus", UsedPct: 88, ResetsAt: tp(thu)}}, nil, ""},
		{"tighter window without reset", []usagestate.Window{
			{Kind: provider.WindowWeekly, UsedPct: 62},
			{Kind: provider.WindowSession, UsedPct: 28, ResetsAt: tp(thu)}}, nil,
			"week 62% · 5-hour 28%"},
		{"non-UTC zone", []usagestate.Window{
			{Kind: provider.WindowWeekly, UsedPct: 62, ResetsAt: tp(thu)},
			{Kind: provider.WindowSession, UsedPct: 28, ResetsAt: tp(fri)}}, est,
			"week 62% · 5-hour 28% · resets Thu 10:57"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WeekLine(c.ws, c.loc); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
