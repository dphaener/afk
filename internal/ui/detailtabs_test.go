package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/spice"
	"github.com/dphaener/afk/internal/state"
)

// TestDetailTabKeysRouteToActions pins the dispatch half of the tabs: tab,
// shift+tab and the digits route to the three tab actions when the detail
// pane is on screen, and fall through to ignored — not to some other action —
// when a session pane is. The digit row uses "3" rather than "1" so a binding
// that only carried the first digit would fail here.
func TestDetailTabKeysRouteToActions(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	cases := []struct {
		name string
		key  tea.KeyPressMsg
		want modeAction
	}{
		{"tab is next tab", tea.KeyPressMsg{Code: tea.KeyTab}, actionDetailTabNext},
		{"shift+tab is previous tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, actionDetailTabPrev},
		{"a digit jumps", tea.KeyPressMsg{Code: '3', Text: "3"}, actionDetailTabJump},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reg.route(inputNormal, routeConds{detailShown: true}, tc.key)
			if want := (keyDecision{routeAction, tc.want}); got != want {
				t.Fatalf("route(detail shown, %q) = {%v %v}, want {%v %v}",
					tc.key.String(), got.kind, got.action, want.kind, want.action)
			}
			if got := reg.route(inputNormal, routeConds{}, tc.key); got.kind != routeIgnored {
				t.Fatalf("route(pane on screen, %q) = {%v %v}, want ignored",
					tc.key.String(), got.kind, got.action)
			}
		})
	}
}

// TestVisibleDetailTabs pins the one tab whose presence depends on the repo:
// Stack appears only with git-spice detected, and the digits index the visible
// strip, so Notes is `3` in a plain repo and `4` in a git-spice one.
func TestVisibleDetailTabs(t *testing.T) {
	plain := visibleDetailTabs(stackState{})
	if want := []detailTab{tabOverview, tabWaves, tabNotes, tabDiff}; !equalTabs(plain, want) {
		t.Fatalf("plain repo tabs = %v, want %v", plain, want)
	}
	gs := visibleDetailTabs(stackState{Detected: true})
	if want := []detailTab{tabOverview, tabWaves, tabStack, tabNotes, tabDiff}; !equalTabs(gs, want) {
		t.Fatalf("git-spice repo tabs = %v, want %v", gs, want)
	}
}

func equalTabs(a, b []detailTab) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDetailTabSwitching drives the three actions through Update. The
// load-bearing rows: wrap in both directions, a digit past the last tab is a
// no-op, a chosen Stack tab falls back to the Overview when git-spice is not
// detected (activeDetailTab), and switching snaps the scroll offset to the
// top — a Waves offset applied to the Overview would hide its head.
func TestDetailTabSwitching(t *testing.T) {
	newModel := func(gs bool) Model {
		m := testModel(bigPlanSession("alpha"))
		m.stack = stackState{Detected: gs}
		m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
		return m
	}
	press := func(m Model, keys ...tea.Msg) Model {
		for _, k := range keys {
			m, _ = update(t, m, k)
		}
		return m
	}
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	backtab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}

	t.Run("tab cycles forward and wraps", func(t *testing.T) {
		m := newModel(true)
		want := []detailTab{tabWaves, tabStack, tabNotes, tabDiff, tabOverview}
		for i, w := range want {
			m = press(m, tab)
			if got := m.activeDetailTab(); got != w {
				t.Fatalf("after %d tab presses active = %v, want %v", i+1, got, w)
			}
		}
	})

	t.Run("shift+tab from the Overview wraps to the last tab", func(t *testing.T) {
		m := press(newModel(true), backtab)
		if got := m.activeDetailTab(); got != tabDiff {
			t.Fatalf("active = %v, want Diff", got)
		}
	})

	t.Run("digits index the visible strip", func(t *testing.T) {
		if got := press(newModel(true), keyRune('3')).activeDetailTab(); got != tabStack {
			t.Fatalf("git-spice repo: 3 = %v, want Stack", got)
		}
		if got := press(newModel(false), keyRune('3')).activeDetailTab(); got != tabNotes {
			t.Fatalf("plain repo: 3 = %v, want Notes", got)
		}
		if got := press(newModel(false), keyRune('5')).activeDetailTab(); got != tabOverview {
			t.Fatalf("plain repo: 5 is past the last tab and must be a no-op, got %v", got)
		}
	})

	t.Run("a Stack tab outlives git-spice only as the Overview", func(t *testing.T) {
		m := press(newModel(true), keyRune('3'))
		m.stack = stackState{Detected: false}
		if got := m.activeDetailTab(); got != tabOverview {
			t.Fatalf("active = %v with git-spice gone, want the Overview fallback", got)
		}
		if m.detailTab != tabStack {
			t.Fatalf("the stored tab must survive (%v) so a rescan that finds git-spice again restores it", m.detailTab)
		}
	})

	t.Run("switching resets the scroll offset", func(t *testing.T) {
		m := press(newModel(true), keyRune('2'), chord('d'), chord('d'))
		if m.detailViewport.YOffset() == 0 {
			t.Fatalf("fixture broken: the Waves tab did not scroll")
		}
		m = press(m, keyRune('1'))
		if got := m.detailViewport.YOffset(); got != 0 {
			t.Fatalf("YOffset after switching tabs = %d, want 0", got)
		}
	})

	t.Run("keys are inert while a session pane is on screen", func(t *testing.T) {
		m := newModel(true)
		m.pane = newFakePane("x")
		m.paneName = "alpha"
		m = press(m, tab, keyRune('4'))
		if got := m.activeDetailTab(); got != tabOverview {
			t.Fatalf("tab keys moved the tab to %v while the pane was on screen", got)
		}
		if m.normalBindingEnabled(detailTabNextKey) {
			t.Fatalf("the tab key must be hidden from help while the pane is on screen")
		}
	})
}

// TestOverviewSummarises pins what the Overview shows for a live session and
// what it withholds: the awaiting line only when the flag is set, the
// scheduler's task block (the ready W3.T1 — a legacy plan's failed wave does
// not block the next — and the failed W2.T1 with its reason under
// `Failed (1)`, never a "Wave N" heading or the finished W1.T1), the stack
// path, and the last three issues rather than the whole tail.
func TestOverviewSummarises(t *testing.T) {
	s := sess("feat-a")
	s.AwaitingInput = true
	s.HostRunning = true
	s.HostPID = 1
	s.IssuesTail = []string{"one", "two", "three", "four"}
	s.Plan = &state.Plan{Waves: []state.Wave{
		{Number: 1, Name: "Done", Tasks: []state.Task{{ID: "W1.T1", Title: "a", State: state.CheckComplete}}},
		{Number: 2, Name: "Here", Tasks: []state.Task{{ID: "W2.T1", Title: "b", State: state.CheckFailed, FailureReason: "boom"}}},
		{Number: 3, Name: "Later", Tasks: []state.Task{{ID: "W3.T1", Title: "c"}}},
	}}
	ctx := panelContext{Session: s, Stack: stackState{Detected: true, Branches: []spice.Branch{
		{Name: "main"}, {Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
	}}}
	out := xansi.Strip(strings.Join(overviewPanel{}.Render(ctx, 80), "\n"))

	for _, want := range []string{"Awaiting input", "host running", "Ready (1)", "\n  ○ W3.T1 c\n",
		"Failed (1)", "\n  ✗ W2.T1 b — boom\n", "main › feat-a", "Issues (last 3)", "two", "three", "four"} {
		if !strings.Contains(out, want) {
			t.Errorf("Overview missing %q:\n%s", want, out)
		}
	}
	if r, f := strings.Index(out, "Ready (1)"), strings.Index(out, "Failed (1)"); r < 0 || f < r {
		t.Errorf("Ready block must precede the Failed section:\n%s", out)
	}
	for _, banned := range []string{"Wave 1", "Wave 2", "Wave 3", "W1.T1", "Ready (0)", "\n  one\n"} {
		if strings.Contains(out, banned) {
			t.Errorf("Overview should not show %q:\n%s", banned, out)
		}
	}

	s.AwaitingInput = false
	quiet := xansi.Strip(strings.Join(overviewPanel{}.Render(panelContext{Session: s}, 80), "\n"))
	if strings.Contains(quiet, "Awaiting input") {
		t.Errorf("Overview shows the awaiting line with the flag clear:\n%s", quiet)
	}
}

// TestStackPath pins the path helper's three answers: a chain to the trunk,
// a bare trunk, and "" for a branch the stack does not track. The cycle row
// proves a malformed stream terminates.
func TestStackPath(t *testing.T) {
	ref := func(n string) *spice.BranchRef { return &spice.BranchRef{Name: n} }
	branches := []spice.Branch{
		{Name: "main"},
		{Name: "a", Down: ref("main")},
		{Name: "b", Down: ref("a")},
		{Name: "loop1", Down: ref("loop2")},
		{Name: "loop2", Down: ref("loop1")},
	}
	cases := map[string]string{
		"b":         "main › a › b",
		"a":         "main › a",
		"main":      "main",
		"untracked": "",
		"loop1":     "loop2 › loop1",
	}
	for name, want := range cases {
		if got := stackPath(branches, name); got != want {
			t.Errorf("stackPath(%q) = %q, want %q", name, got, want)
		}
	}
}
