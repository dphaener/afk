package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

var keyRefreshUsage = keyRune('R')

// usageRefreshSeam records every call of the RefreshUsage seam and gives the
// reply the test wants. It never runs a CLI and never writes anything.
type usageRefreshSeam struct {
	calls int
	res   UsageRefreshResult
}

func (s *usageRefreshSeam) refresh() UsageRefreshResult {
	s.calls++
	return s.res
}

// usageReading is a usage.json as LoadBuild hands it over: a session window
// and a weekly one at the given percentages, taken at now.
func usageReading(now time.Time, session, weekly float64) usagestate.File {
	reset := now.Add(3 * time.Hour)
	return usagestate.File{
		At: now,
		Windows: []usagestate.Window{
			{Kind: provider.WindowSession, UsedPct: session, ResetsAt: &reset},
			{Kind: provider.WindowWeekly, UsedPct: weekly, ResetsAt: &reset},
		},
	}
}

// usageRefreshModel is the session list with a build read that holds an old
// usage reading (40% and 91%) and the seam wired to a recorder.
func usageRefreshModel(t *testing.T) (Model, *usageRefreshSeam) {
	t.Helper()
	seam := &usageRefreshSeam{res: UsageRefreshResult{Outcome: UsageRefreshed, Text: "A new usage reading was saved."}}
	m := testModel(sess("alpha"))
	m.width = 160
	m.deps.RefreshUsage = seam.refresh
	m = withBuild(t, m, buildview.Snapshot{Root: "/repo", Usage: usageReading(m.deps.Now(), 40, 91), UsageKnown: true})
	m.clearStatus()
	return m, seam
}

// runUsageRefresh delivers every message cmd produces back to the model, the
// way the runtime does, and returns the model after them.
func runUsageRefresh(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range collectMsgs(t, cmd) {
		m, _ = update(t, m, msg)
	}
	return m
}

// TestUsageRefreshBlockedReason is the row's reason, which the action asks
// again when it runs. Load-bearing rows: "no seam" (a dashboard not wired to
// the reading cannot run the agent's CLI) and "runs with no project" (the
// reading is the machine's, so the row must not be dimmed for want of a
// project or a build). "over the all-projects screen" pins that the
// screen's own rule still decides there.
func TestUsageRefreshBlockedReason(t *testing.T) {
	tests := []struct {
		name  string
		conds leaderConds
		want  string
	}{
		{"runs with the seam", leaderConds{canRefreshUsage: true, hasActiveProject: true, hasBuild: true}, ""},
		{"runs with no project", leaderConds{canRefreshUsage: true}, ""},
		{"no seam", leaderConds{hasActiveProject: true, hasBuild: true}, usageRefreshNoSeam},
		{"over the all-projects screen", leaderConds{canRefreshUsage: true, allProjectsOpen: true}, allProjectsCloseFirst},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := leaderBlockedReason(actionRefreshUsage, tt.conds); got != tt.want {
				t.Fatalf("reason %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUsageRefreshNilSeam is the zero-Deps guarantee: a dashboard not wired
// to the reading dims the row, and neither ctrl+space R nor the action run
// directly calls anything or reads usage. Both rows are load-bearing: the
// first is what the owner can press, the second the action's own refusal.
func TestUsageRefreshNilSeam(t *testing.T) {
	t.Run("the row is dimmed and R calls nothing", func(t *testing.T) {
		m, seam := usageRefreshModel(t)
		m.deps.RefreshUsage = nil
		m, _ = update(t, m, leaderKeyPress)
		r, ok := leaderRowFor(m.leader.rows, actionRefreshUsage)
		if !ok || r.blocked != usageRefreshNoSeam {
			t.Fatalf("row blocked %q, want %q", r.blocked, usageRefreshNoSeam)
		}
		m, cmd := update(t, m, keyRefreshUsage)
		m = runUsageRefresh(t, m, cmd)
		if seam.calls != 0 || m.usageRefreshing {
			t.Fatalf("calls %d, in flight %v; want nothing", seam.calls, m.usageRefreshing)
		}
	})
	t.Run("the action refuses on its own", func(t *testing.T) {
		m, _ := usageRefreshModel(t)
		m.deps.RefreshUsage = nil
		m, cmd := runAction(m, actionRefreshUsage)
		if cmd != nil || m.usageRefreshing {
			t.Fatalf("cmd %v, in flight %v; want nothing but a notice", cmd != nil, m.usageRefreshing)
		}
		if m.status != "This dashboard cannot read usage." || m.statusKind != notifyInfo {
			t.Errorf("notice %v %q", m.statusKind, m.status)
		}
	})
}

// TestUsageRefreshRunsOnce drives ctrl+space R through Update. Load-bearing
// rows: "the seam runs in the command, not in Update" (the agent's CLI can
// take a minute, and the screen must not wait), "a second press calls
// nothing" (one reading at a time) and "the flag is cleared after" (a flag
// left set would refuse every later press).
func TestUsageRefreshRunsOnce(t *testing.T) {
	t.Run("the seam runs in the command, not in Update", func(t *testing.T) {
		m, seam := usageRefreshModel(t)
		m, cmd := pressAction(t, m, keyRefreshUsage)
		if seam.calls != 0 {
			t.Fatal("the seam ran inside Update")
		}
		if cmd == nil || !m.usageRefreshing || !m.hasInFlightWork() {
			t.Fatalf("cmd %v, in flight %v, hasInFlightWork %v", cmd != nil, m.usageRefreshing, m.hasInFlightWork())
		}
		if m.status != usageReadingNotice || m.statusKind != notifyInfo {
			t.Errorf("notice %v %q, want %q", m.statusKind, m.status, usageReadingNotice)
		}
		m = runUsageRefresh(t, m, cmd)
		if seam.calls != 1 {
			t.Fatalf("seam calls %d, want 1", seam.calls)
		}
	})
	t.Run("a second press calls nothing", func(t *testing.T) {
		m, seam := usageRefreshModel(t)
		m, first := pressAction(t, m, keyRefreshUsage)
		m, second := pressAction(t, m, keyRefreshUsage)
		if second != nil {
			t.Fatal("a second press returned a command")
		}
		if m.status != usageRefreshBusy || m.statusKind != notifyInfo {
			t.Errorf("notice %v %q, want %q", m.statusKind, m.status, usageRefreshBusy)
		}
		if !m.usageRefreshing || !m.hasInFlightWork() {
			t.Error("the first reading is no longer in flight")
		}
		m = runUsageRefresh(t, m, first)
		if seam.calls != 1 {
			t.Fatalf("seam calls %d, want 1", seam.calls)
		}
	})
	t.Run("the flag is cleared after", func(t *testing.T) {
		m, seam := usageRefreshModel(t)
		m, cmd := pressAction(t, m, keyRefreshUsage)
		m = runUsageRefresh(t, m, cmd)
		if m.usageRefreshing || m.hasInFlightWork() {
			t.Fatalf("in flight %v, hasInFlightWork %v after the result", m.usageRefreshing, m.hasInFlightWork())
		}
		m, cmd = pressAction(t, m, keyRefreshUsage)
		m = runUsageRefresh(t, m, cmd)
		if seam.calls != 2 {
			t.Fatalf("seam calls %d, want 2 after two presses one after the other", seam.calls)
		}
	})
	t.Run("from the agent pane it still runs", func(t *testing.T) {
		m, seam := usageRefreshModel(t)
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
		m, cmd := pressAction(t, m, keyRefreshUsage)
		m = runUsageRefresh(t, m, cmd)
		if seam.calls != 1 {
			t.Fatalf("seam calls %d, want 1", seam.calls)
		}
	})
}

// TestUsageRefreshNotices is each outcome's notice. Every row is
// load-bearing: a saved reading, an agent that cannot report usage and a
// failed reading must not read alike, "empty text" must still say
// something, and "unknown outcome" must not claim success.
func TestUsageRefreshNotices(t *testing.T) {
	tests := []struct {
		name     string
		res      UsageRefreshResult
		wantKind notificationKind
		wantText string
	}{
		{"refreshed", UsageRefreshResult{UsageRefreshed, "A new usage reading was saved."}, notifySuccess, "A new usage reading was saved."},
		{"unsupported", UsageRefreshResult{UsageRefreshUnsupported, "This agent cannot report how much of its usage is spent."},
			notifyWarn, "This agent cannot report how much of its usage is spent."},
		{"failed", UsageRefreshResult{UsageRefreshFailed, "The agent's command-line tool was not found. Install it, then try again."},
			notifyError, "The agent's command-line tool was not found. Install it, then try again."},
		{"empty text", UsageRefreshResult{UsageRefreshed, "  "}, notifyWarn, usageRefreshNoAnswer},
		{"unknown outcome", UsageRefreshResult{"odd", "Something."}, notifyInfo, "Something."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := usageRefreshModel(t)
			m.usageRefreshing = true
			m, _ = update(t, m, usageRefreshedMsg{result: tt.res})
			if m.statusKind != tt.wantKind || m.status != tt.wantText {
				t.Errorf("notice %v %q, want %v %q", m.statusKind, m.status, tt.wantKind, tt.wantText)
			}
			if m.usageRefreshing {
				t.Error("the in-flight flag is still set")
			}
		})
	}
}

// TestUsageRefreshHeader checks what the header shows after a reading.
// Load-bearing rows: "a reading shows its numbers" (the header must show the
// new reading without waiting for the tick, through a build read and not
// from the result), "a failed reading keeps the old one" (usage.json is as
// it was, so the re-read shows the old numbers) and "a result after a
// project switch is kept" (the reading is the machine's; dropping it would
// lose the notice for nothing).
func TestUsageRefreshHeader(t *testing.T) {
	tests := []struct {
		name string
		res  UsageRefreshResult
		// newReading is what usage.json holds once the reading is over, as
		// the fake LoadBuild reads it: the new reading, or the old one again.
		newReading  bool
		switchFirst bool
		wantKind    notificationKind
		wantHeader  string
		notHeader   string
	}{
		{"a reading shows its numbers", UsageRefreshResult{UsageRefreshed, "A new usage reading was saved."}, true, false,
			notifySuccess, "Week (all models) 34%", "91%"},
		{"a failed reading keeps the old one",
			UsageRefreshResult{UsageRefreshFailed, "Usage could not be read. Check that the agent's command-line tool is installed and logged in with an interactive login."},
			false, false, notifyError, "Week (all models) 91%", "34%"},
		{"a result after a project switch is kept", UsageRefreshResult{UsageRefreshed, "A new usage reading was saved."}, true, true,
			notifySuccess, "Week (all models) 34%", "91%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, seam := usageRefreshModel(t)
			seam.res = tt.res
			reads := 0
			m.deps.LoadBuild = func(string, []BuildSession) buildview.Snapshot {
				reads++
				f := usageReading(m.deps.Now(), 40, 91)
				if tt.newReading {
					f = usageReading(m.deps.Now(), 12, 34)
				}
				return buildview.Snapshot{Root: "/repo", Usage: f, UsageKnown: true}
			}
			if got := xansi.Strip(m.renderHeader()); !strings.Contains(got, "Week (all models) 91%") {
				t.Fatalf("setup: header %q does not show the old reading", got)
			}
			m, cmd := pressAction(t, m, keyRefreshUsage)
			msgs := collectMsgs(t, cmd)
			if len(msgs) != 1 {
				t.Fatalf("the action gave %d messages, want the one result", len(msgs))
			}
			if tt.switchFirst {
				m.projectGen++
			}
			m, cmd = update(t, m, msgs[0])
			if m.status != strings.TrimSpace(tt.res.Text) || m.statusKind != tt.wantKind {
				t.Errorf("notice %v %q, want %v %q", m.statusKind, m.status, tt.wantKind, tt.res.Text)
			}
			if m.usageRefreshing {
				t.Error("the in-flight flag is still set")
			}
			m = runUsageRefresh(t, m, cmd)
			if reads != 1 {
				t.Fatalf("build reads %d, want one after the result", reads)
			}
			got := xansi.Strip(m.renderHeader())
			if !strings.Contains(got, tt.wantHeader) || strings.Contains(got, tt.notHeader) {
				t.Errorf("header %q, want %q and not %q", got, tt.wantHeader, tt.notHeader)
			}
		})
	}
}
