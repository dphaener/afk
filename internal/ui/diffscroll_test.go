package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/discover"
)

// syntheticDiffText is the DiffFull fixture diffScrollModel feeds through
// the real message flow. It is built, not copy-pasted from a real repo, so
// its shape is pinned: comfortably more lines than any of
// TestDetailScrollReachesTheLastLineOfContent's three fixture sizes can show
// at once (the tallest, 120x40, has a body budget well under 60 rows), one
// tab-indented added line (the `+\t` shape every Go source line in a real
// `git diff` carries — see the plan's "Tabs measure zero cells" hazard), and
// one line far longer than any tested content width, so a rendered row that
// overran would show up as a failed width assertion rather than by accident.
func syntheticDiffText() string {
	var b strings.Builder
	b.WriteString("diff --git a/pkg/example.go b/pkg/example.go\n")
	b.WriteString("index 1111111..2222222 100644\n")
	b.WriteString("--- a/pkg/example.go\n")
	b.WriteString("+++ b/pkg/example.go\n")
	b.WriteString("@@ -1,3 +1,60 @@\n")
	b.WriteString("+\tfunc example() {\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "+\tline number %d of the added function body\n", i)
	}
	b.WriteString("-" + strings.Repeat("x", 300) + "\n")
	b.WriteString("+\t}\n")
	b.WriteString(" context line unchanged\n")
	return b.String()
}

// diffScrollModel builds a Model on the Diff tab whose DiffText came from
// syntheticDiffText through the ACTUAL plumbing (loadCmd -> Deps.DiffFull ->
// computeDiffStats -> retainDiffText -> a delivered sessionsLoadedMsg), never
// by setting Session.DiffText directly. That distinction matters: W6.T1's
// task note is explicit that stuffing DiffText in via a direct setter would
// prove the render works but not that the plumbing does, and the fixture
// path is exactly what it is warning about — retainText is gated on the
// active detail tab at Cmd-creation time (messages.go's loadCmd), so the
// switch to the Diff tab has to happen BEFORE the load fires or the retained
// text silently comes back empty.
func diffScrollModel(t *testing.T, width, height int) Model {
	t.Helper()
	diffText := syntheticDiffText()

	deps := testDeps()
	deps.Discover = func(string) ([]discover.SessionState, error) {
		return []discover.SessionState{sampleState("alpha")}, nil
	}
	deps.ResolveBaseRef = resolveQualified
	deps.DiffFull = func(string, string) (string, error) { return diffText, nil }
	deps.DiffNumstat = func(string, string) (string, error) { return "", nil }

	m := New("/repo", time.Second, deps)
	m.ready = true
	m.width = width
	m.height = height
	m.sizeSessionList()
	m.setSessions([]Session{sess("alpha")})

	m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: height})

	// shift+tab from the Overview wraps to the last visible tab, which is
	// always Diff (visibleDetailTabs) whether or not git-spice is detected —
	// same landing TestDetailTabSwitching and TestSwitchDetailTab_ToDiff_
	// DispatchesLoad rely on. This is the real key-driven path a user takes,
	// not a direct field assignment, and it returns the load Cmd this
	// fixture depends on for the next step.
	got, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m = got
	if m.activeDetailTab() != tabDiff {
		t.Fatalf("fixture broken: expected to land on the Diff tab, got %v", m.activeDetailTab())
	}
	if cmd == nil {
		t.Fatalf("fixture broken: switching onto the Diff tab must dispatch a load Cmd")
	}

	msg := cmd()
	loaded, ok := msg.(sessionsLoadedMsg)
	if !ok {
		t.Fatalf("fixture broken: the Diff tab's dispatched Cmd produced %T, want sessionsLoadedMsg", msg)
	}
	m, _ = update(t, m, loaded)

	s, ok := m.selected()
	if !ok {
		t.Fatalf("fixture broken: no session selected after sessionsLoadedMsg")
	}
	if len(s.DiffText) == 0 {
		t.Fatalf("fixture broken: DiffText is empty after the real load — the tab switch must precede the load")
	}
	return m
}

// TestDiffTabScrollWidthSafeAndResetsOnTabSwitch covers the two Diff-specific
// properties TestDetailScrollReachesTheLastLineOfContent's shared assertion
// doesn't: that a diff body containing a tab-indented line and a very long
// line never produces a rendered row wider than the content width (the
// plan's "Tabs measure zero cells" hazard, exercised end-to-end rather than
// only at the renderDiffLine unit level), and that switching away from the
// Diff tab and back puts the scroll back at the top, per README:140's
// documented "Switching tabs puts the scroll back at the top."
func TestDiffTabScrollWidthSafeAndResetsOnTabSwitch(t *testing.T) {
	for _, tc := range []struct{ width, height int }{
		{100, 24},
		{80, 10},
		{120, 40},
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			m := diffScrollModel(t, tc.width, tc.height)
			_, mainOuter, _ := m.layoutDims()
			contentW := detailContentWidth(mainOuter)

			for _, ln := range strings.Split(xansi.Strip(m.detailBody(contentW)), "\n") {
				if w := lipgloss.Width(ln); w > contentW {
					t.Errorf("diff body line exceeds content width %d (got %d): %q", contentW, w, ln)
				}
			}

			for i := 0; i < 1000; i++ {
				before := m.detailViewport.YOffset()
				m, _ = update(t, m, chord('d'))
				if m.detailViewport.YOffset() == before {
					break
				}
			}
			if got := m.detailViewport.YOffset(); got == 0 {
				t.Fatalf("fixture broken: scrolling the Diff tab left YOffset at 0")
			}

			// tab from the Diff tab (the last in visibleDetailTabs) wraps
			// forward to the Overview; shift+tab from there wraps back to
			// Diff — the same round trip TestDetailTabSwitching's "wraps to
			// the last tab" row exercises, driven here instead of set via a
			// direct field assignment so it goes through switchDetailTab's
			// actual GotoTop()/syncDetailViewport calls.
			m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			if got := m.activeDetailTab(); got != tabOverview {
				t.Fatalf("fixture broken: tab from Diff landed on %v, want Overview", got)
			}
			m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
			if got := m.activeDetailTab(); got != tabDiff {
				t.Fatalf("fixture broken: shift+tab from Overview landed on %v, want Diff", got)
			}
			if got := m.detailViewport.YOffset(); got != 0 {
				t.Errorf("switching away from Diff and back left YOffset = %d, want 0 (reset)", got)
			}
		})
	}
}
