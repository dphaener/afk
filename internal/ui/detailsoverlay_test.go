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
	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/session"
)

// TestDetailsBoxRect pins the overlay's one geometry function (§3.8, §2).
// Load-bearing rows: 100 and 99 straddle detailsFullWidthBelow, so a
// breakpoint moved by one column turns one of them red; 120 is the
// 54-column box flush to the right edge; 80 is the full width. Every row
// covers the body exactly, between the top bar and the status bar.
func TestDetailsBoxRect(t *testing.T) {
	for _, tc := range []struct {
		width            int
		wantX, wantW     int
		headerRows, body int
	}{
		{width: 120, wantX: 66, wantW: 54, headerRows: 1, body: 38},
		{width: 100, wantX: 46, wantW: 54, headerRows: 1, body: 38},
		{width: 99, wantX: 0, wantW: 99, headerRows: 1, body: 38},
		{width: 80, wantX: 0, wantW: 80, headerRows: 2, body: 21},
	} {
		t.Run(fmt.Sprintf("%d_columns", tc.width), func(t *testing.T) {
			x, y, w, h := detailsBoxRect(tc.width, tc.headerRows, tc.body)
			if x != tc.wantX || w != tc.wantW {
				t.Errorf("x, w = %d, %d, want %d, %d", x, w, tc.wantX, tc.wantW)
			}
			if x+w != tc.width {
				t.Errorf("box ends at column %d, want flush to the right edge %d", x+w, tc.width)
			}
			if y != tc.headerRows || h != tc.body {
				t.Errorf("y, h = %d, %d, want the body: %d, %d", y, h, tc.headerRows, tc.body)
			}
		})
	}
}

// TestDetailsViewportBudgetMatchesTheDrawnBox pins that the viewport the
// scroll keys move is sized from the same box the frame draws, at a wide and
// a narrow terminal: its width is the box's content width, its height
// detailsBodyBudget's rows for the box, the drawn box is exactly the rect,
// and scrolling to the bottom brings the body's last line on screen. A
// viewport even one row taller than the drawn body would leave that line
// unreachable (detailRowBudget's doc), which is the regression this guards.
func TestDetailsViewportBudgetMatchesTheDrawnBox(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 40}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			m := testModel(bigPlanSession("alpha"))
			m.detailTab = tabWaves
			m, _ = update(t, m, tea.WindowSizeMsg{Width: size.w, Height: size.h})
			m, _ = update(t, m, keyRune('d'))
			if !m.detailsOpen {
				t.Fatal("setup: d did not open the details")
			}
			_, _, w, h := m.detailsBox()
			if got := m.detailViewport.Width(); got != detailContentWidth(w) {
				t.Errorf("viewport width = %d, want the box's content width %d", got, detailContentWidth(w))
			}
			rows, readout := detailsBodyBudget(h, m.detailViewport.TotalLineCount())
			if !readout {
				t.Fatal("fixture broken: the Waves body must overflow the box")
			}
			if got := m.detailViewport.Height(); got != rows {
				t.Errorf("viewport height = %d, want the drawn body's %d rows", got, rows)
			}
			box := m.renderDetailsOverlay(w, h)
			if lipgloss.Width(box) != w || lipgloss.Height(box) != h {
				t.Errorf("drawn box is %dx%d, want the rect's %dx%d", lipgloss.Width(box), lipgloss.Height(box), w, h)
			}
			assertDetailScrollReachesLastLine(t, m)
		})
	}
}

// detailsFromAgent is a dashboard in the Agent with the details opened over
// it by the leader then d: the pane installed, focused and drawn behind.
func detailsFromAgent(t *testing.T) (Model, *fakePane) {
	t.Helper()
	fp := newFakePane("agent output")
	m := testModel(runningSess("alpha"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "alpha"})
	m, _ = update(t, m, keyEnter)
	if !m.paneOwnsKeyboard() {
		t.Fatal("setup: enter did not open the agent")
	}
	gen := m.previewGen
	m, _ = pressAction(t, m, keyRune('d'))
	if !m.detailsOpen || m.detailsFrom != levelAgent {
		t.Fatalf("setup: ctrl+] d: details=%v from=%v, want open from the Agent", m.detailsOpen, m.detailsFrom)
	}
	if m.previewGen != gen {
		t.Fatalf("setup: opening the details bumped previewGen %d -> %d", gen, m.previewGen)
	}
	return m, fp
}

// detailsFromProject is a dashboard at Project with the details opened by d.
func detailsFromProject(t *testing.T) (Model, *fakePane) {
	t.Helper()
	m := testModel(runningSess("alpha"))
	m.stack = stackState{Detected: true}
	m, _ = update(t, m, keyRune('d'))
	if !m.detailsOpen || m.detailsFrom != levelProject {
		t.Fatalf("setup: d: details=%v from=%v, want open from the Project", m.detailsOpen, m.detailsFrom)
	}
	return m, nil
}

// TestDetailsOverlayKeys is the overlay's key handling driven through Update
// (§4.4, step 6 of the task). Load-bearing rows:
//
//   - "esc from the Agent": back in the Agent with the pane still installed
//     and drawn and the keyboard in the agent (inputPane), nothing closed and
//     no key sent on the way. This is the row the task's acceptance names.
//   - "esc from Project": back on the Project with the focused section kept.
//   - the digit rows: 2 is Waves and 3 is Stack in a git-spice repo, 9 is
//     past the last tab and does nothing, and from the Agent a digit is the
//     overlay's, never the agent's.
//   - "j scrolls": a move key moves the viewport, not the session list.
//   - "q quits" and "? opens the keys": the keys that still work over it.
//   - "d with no session selected": nothing opens and the notice says why.
func TestDetailsOverlayKeys(t *testing.T) {
	type check func(t *testing.T, before, after Model, fp *fakePane, cmd tea.Cmd)
	noAgentKeys := func(t *testing.T, fp *fakePane) {
		t.Helper()
		if fp != nil && len(fp.keys)+len(fp.texts) != 0 {
			t.Errorf("a key reached the agent behind the details: keys=%v texts=%v", fp.keys, fp.texts)
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) (Model, *fakePane)
		keys  []tea.Msg
		check check
	}{
		{
			name: "esc from the Agent gives the agent the keyboard back", setup: detailsFromAgent,
			keys: []tea.Msg{keyEsc},
			check: func(t *testing.T, before, after Model, fp *fakePane, cmd tea.Cmd) {
				if after.detailsOpen || cmd != nil {
					t.Fatalf("details=%v cmd=%v, want closed with nothing started", after.detailsOpen, cmd != nil)
				}
				if after.pane != fp || fp.closed != 0 || !after.shouldRenderPane() {
					t.Fatalf("pane same=%v closed=%d drawn=%v, want the agent's pane installed and drawn", after.pane == fp, fp.closed, after.shouldRenderPane())
				}
				if got := activeInputMode(after.modeSnapshot()); got != inputPane || after.level() != levelAgent {
					t.Fatalf("mode = %v level = %v, want the keyboard in the agent", got, after.level())
				}
				noAgentKeys(t, fp)
				// The next key typed is the agent's.
				next, _ := update(t, after, keyRune('x'))
				if len(fp.texts)+len(fp.keys) != 1 {
					t.Fatalf("x after esc did not reach the agent: keys=%v texts=%v", fp.keys, fp.texts)
				}
				_ = next
			},
		},
		{
			name: "esc from Project keeps the focused section", setup: func(t *testing.T) (Model, *fakePane) {
				m := testModel(runningSess("alpha"))
				m.section = sectionBuild
				m, _ = update(t, m, keyRune('d'))
				return m, nil
			},
			keys: []tea.Msg{keyEsc},
			check: func(t *testing.T, before, after Model, _ *fakePane, _ tea.Cmd) {
				if after.detailsOpen || after.level() != levelProject || after.focusedSection() != sectionBuild || after.focus != focusList {
					t.Fatalf("details=%v level=%v section=%v focus=%v, want the Project on Build", after.detailsOpen, after.level(), after.focusedSection(), after.focus)
				}
			},
		},
		{
			name: "2 is Waves", setup: detailsFromProject, keys: []tea.Msg{keyRune('2')},
			check: func(t *testing.T, _, after Model, _ *fakePane, _ tea.Cmd) {
				if after.activeDetailTab() != tabWaves || after.focusedSection() != sectionSessions {
					t.Fatalf("tab=%v section=%v, want Waves and the section unmoved", after.activeDetailTab(), after.focusedSection())
				}
			},
		},
		{
			name: "3 is Stack in a git-spice repo", setup: detailsFromProject, keys: []tea.Msg{keyRune('3')},
			check: func(t *testing.T, _, after Model, _ *fakePane, _ tea.Cmd) {
				if after.activeDetailTab() != tabStack {
					t.Fatalf("tab = %v, want Stack", after.activeDetailTab())
				}
			},
		},
		{
			name: "9 is past the last tab and does nothing", setup: detailsFromProject, keys: []tea.Msg{keyRune('9')},
			check: func(t *testing.T, _, after Model, _ *fakePane, _ tea.Cmd) {
				if after.activeDetailTab() != tabOverview || !after.detailsOpen {
					t.Fatalf("tab=%v details=%v, want the Overview still up", after.activeDetailTab(), after.detailsOpen)
				}
			},
		},
		{
			name: "tab and shift+tab step and wrap", setup: detailsFromProject,
			keys: []tea.Msg{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}},
			check: func(t *testing.T, _, after Model, _ *fakePane, _ tea.Cmd) {
				if after.activeDetailTab() != tabDiff {
					t.Fatalf("shift+tab from the Overview = %v, want Diff (wrapped)", after.activeDetailTab())
				}
				after, _ = update(t, after, tea.KeyPressMsg{Code: tea.KeyTab})
				if after.activeDetailTab() != tabOverview {
					t.Fatalf("tab from Diff = %v, want the Overview (wrapped)", after.activeDetailTab())
				}
			},
		},
		{
			name: "a digit over the Agent is the overlay's", setup: detailsFromAgent, keys: []tea.Msg{keyRune('3')},
			check: func(t *testing.T, _, after Model, fp *fakePane, _ tea.Cmd) {
				if after.activeDetailTab() != tabNotes {
					t.Fatalf("tab = %v, want Notes (no git-spice: 3 is Notes)", after.activeDetailTab())
				}
				noAgentKeys(t, fp)
			},
		},
		{
			name: "j scrolls and moves no row", setup: func(t *testing.T) (Model, *fakePane) {
				m := testModel(bigPlanSession("alpha"), bigPlanSession("bravo"))
				m.detailTab = tabWaves
				m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
				m, _ = update(t, m, keyRune('d'))
				return m, nil
			},
			keys: []tea.Msg{keyRune('j'), keyRune('j')},
			check: func(t *testing.T, before, after Model, _ *fakePane, _ tea.Cmd) {
				if after.detailViewport.YOffset() != 2 {
					t.Fatalf("YOffset = %d, want 2", after.detailViewport.YOffset())
				}
				b, _ := before.selected()
				a, _ := after.selected()
				if a.Name != b.Name {
					t.Fatalf("selection moved %q -> %q", b.Name, a.Name)
				}
			},
		},
		{
			name: "q quits", setup: detailsFromAgent, keys: []tea.Msg{keyRune('q')},
			check: func(t *testing.T, _, after Model, fp *fakePane, _ tea.Cmd) {
				if !after.quitting {
					t.Fatal("q in the details did not quit")
				}
				noAgentKeys(t, fp)
			},
		},
		{
			name: "? opens the keys over it, and closing it returns to the details", setup: detailsFromAgent, keys: []tea.Msg{keyRune('?')},
			check: func(t *testing.T, _, after Model, fp *fakePane, _ tea.Cmd) {
				if !after.keysOpen || !after.detailsOpen {
					t.Fatalf("keys=%v details=%v, want the keys over the details", after.keysOpen, after.detailsOpen)
				}
				after, _ = update(t, after, keyEsc)
				if after.keysOpen || activeInputMode(after.modeSnapshot()) != inputDetail {
					t.Fatalf("after esc: keys=%v mode=%v, want back in the details", after.keysOpen, activeInputMode(after.modeSnapshot()))
				}
				noAgentKeys(t, fp)
			},
		},
		{
			name: ": opens the actions search over it", setup: detailsFromProject, keys: []tea.Msg{keyRune(':')},
			check: func(t *testing.T, _, after Model, _ *fakePane, _ tea.Cmd) {
				if !after.leaderOpen || !after.detailsOpen || after.leaderFrom != inputDetail {
					t.Fatalf("search=%v details=%v from=%v, want the search over the details", after.leaderOpen, after.detailsOpen, after.leaderFrom)
				}
			},
		},
		{
			name: "d with no session selected opens nothing and says why", setup: func(t *testing.T) (Model, *fakePane) {
				m := testModel()
				if !m.hasActiveProject() {
					t.Fatal("fixture broken: the empty project must be the Project level")
				}
				return m, nil
			},
			keys: []tea.Msg{keyRune('d')},
			check: func(t *testing.T, _, after Model, _ *fakePane, cmd tea.Cmd) {
				if after.detailsOpen || cmd != nil {
					t.Fatalf("details=%v cmd=%v, want nothing opened", after.detailsOpen, cmd != nil)
				}
				if after.status != "No session selected." {
					t.Fatalf("notice = %q, want it to say no session is selected", after.status)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.setup(t)
			before := m
			var cmd tea.Cmd
			for _, k := range tc.keys {
				m, cmd = update(t, m, k)
			}
			tc.check(t, before, m, fp, cmd)
		})
	}
}

// TestDetailsOverTheAgent pins what the details overlay leaves alone in the
// Agent: the pane stays installed, drawn and sized, a resize still reaches
// it (load-bearing: a pane behind an overlay that stopped resizing would show
// stale wrap points the moment esc returns to it), and the leader opens no
// strip over the overlay.
func TestDetailsOverTheAgent(t *testing.T) {
	m, fp := detailsFromAgent(t)
	if !strings.Contains(render(m), "agent output") || !strings.Contains(render(m), "Details · alpha") {
		t.Fatalf("the frame must show the agent under the details:\n%s", render(m))
	}
	resizes := len(fp.resizes)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 44})
	if len(fp.resizes) != resizes+1 {
		t.Fatalf("a resize with the details open reached the pane %d times, want once", len(fp.resizes)-resizes)
	}
	cols, rows := m.paneRegionSize()
	if got := fp.resizes[len(fp.resizes)-1]; got != [2]int{cols, rows} {
		t.Fatalf("pane resized to %v, want the agent box's %dx%d", got, cols, rows)
	}
	m, _ = update(t, m, leaderKeyPress)
	if m.keyStripOpen || !m.detailsOpen {
		t.Fatalf("leader in the details: strip=%v details=%v, want it swallowed", m.keyStripOpen, m.detailsOpen)
	}
	if len(fp.keys)+len(fp.texts) != 0 {
		t.Fatalf("the leader reached the agent: %v %v", fp.keys, fp.texts)
	}
}

// TestDetailsOverlayDimsWhatIsUnderIt pins §3.9 for the overlay: a cell of
// the Project under it is dimmed, the status bar is dimmed with it as for
// every overlay, the overlay's own border keeps the accent, and over the
// Agent the agent's border, accent while it had the keyboard, is dimmed too.
// The goldens are stripped of colour, so this is where the dimming is pinned.
func TestDetailsOverlayDimsWhatIsUnderIt(t *testing.T) {
	content := func(m Model) string { return m.View().Content }
	m := testModel(runningSess("alpha"))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	plain := content(m)
	open, _ := update(t, m, keyRune('d'))
	frame := content(open)
	x, y, _, _ := open.detailsBox()

	// The project view's top-left border cell and the status bar's first
	// cell, both outside the box.
	for _, at := range []struct {
		name string
		x, y int
	}{{"project border", 2, y}, {"status bar", 0, open.height - 1}} {
		if before, after := cellOf(t, plain, at.x, at.y), cellOf(t, frame, at.x, at.y); hexOf(before.Style.Fg) == hexOf(after.Style.Fg) && hexOf(before.Style.Bg) == hexOf(after.Style.Bg) {
			t.Errorf("%s at (%d,%d) is not dimmed under the details (fg %s bg %s)", at.name, at.x, at.y, hexOf(after.Style.Fg), hexOf(after.Style.Bg))
		}
	}
	if c := cellOf(t, frame, x, y+2); hexOf(c.Style.Fg) != palette.Tide.Accent {
		t.Errorf("the overlay's left border fg = %s, want the accent %s", hexOf(c.Style.Fg), palette.Tide.Accent)
	}

	agent, _ := detailsFromAgent(t)
	agent, _ = update(t, agent, tea.WindowSizeMsg{Width: 120, Height: 40})
	ax, ay, _, _ := agentBoxRect(agent.width, y, 0)
	if c := cellOf(t, content(agent), ax, ay+2); hexOf(c.Style.Fg) == palette.Tide.Accent {
		t.Errorf("the agent's border under the details is still the accent %s; it must be dimmed", palette.Tide.Accent)
	}
}

// TestDetailsMouse pins the overlay's mouse: a wheel notch over the box
// scrolls it, one outside does nothing, and a click reaches neither the
// agent behind it nor a section of the Project.
func TestDetailsMouse(t *testing.T) {
	m := testModel(bigPlanSession("alpha"))
	m.detailTab = tabWaves
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = update(t, m, keyRune('d'))
	x, y, w, h := m.detailsBox()
	in := tea.MouseWheelMsg{X: x + w/2, Y: y + h/2, Button: tea.MouseWheelDown}
	out := tea.MouseWheelMsg{X: 3, Y: y + h/2, Button: tea.MouseWheelDown}

	got, _ := update(t, m, in)
	if got.detailViewport.YOffset() != 1 {
		t.Errorf("wheel over the box: YOffset = %d, want 1", got.detailViewport.YOffset())
	}
	got, _ = update(t, m, out)
	if got.detailViewport.YOffset() != 0 || got.focusedSection() != m.focusedSection() {
		t.Errorf("wheel outside the box: YOffset=%d section=%v, want nothing moved", got.detailViewport.YOffset(), got.focusedSection())
	}
	got, _ = update(t, m, mouseAt(3, y+3))
	if !got.detailsOpen || got.focusedSection() != m.focusedSection() {
		t.Errorf("a click outside the box: details=%v section=%v, want nothing changed", got.detailsOpen, got.focusedSection())
	}

	agent, fp := detailsFromAgent(t)
	ox, oy, _, _ := agent.paneScreenGeometry()
	agent, _ = update(t, agent, mouseAt(ox, oy))
	if len(fp.mice) != 0 {
		t.Errorf("a click on the agent under the details was forwarded: %v", fp.mice)
	}
}

// TestDetailsCloseWhenTheirSessionLeaves pins step 1's last rule: the
// overlay is about one session, so a tick that drops that session from the
// list closes it (load-bearing: left open it would show another session's
// details under the first one's name), while a tick that only reshuffles the
// rows keeps it open on the same session.
func TestDetailsCloseWhenTheirSessionLeaves(t *testing.T) {
	m := testModel(runningSess("alpha"), runningSess("bravo"))
	m.selectSessionIndex(namedIndex(t, m, "bravo"))
	m, _ = update(t, m, keyRune('d'))
	if m.detailsFor != "bravo" {
		t.Fatalf("setup: details for %q, want bravo", m.detailsFor)
	}

	m.setSessions([]Session{runningSess("bravo"), runningSess("alpha"), runningSess("aardvark")})
	if s, _ := m.selected(); !m.detailsOpen || s.Name != "bravo" {
		t.Fatalf("after a reshuffle: details=%v selected=%q, want bravo's details still open", m.detailsOpen, s.Name)
	}

	m.setSessions([]Session{runningSess("alpha")})
	if m.detailsOpen {
		t.Fatal("the details stayed open after their session left the list")
	}
	if got := activeInputMode(m.modeSnapshot()); got != inputNormal {
		t.Fatalf("mode = %v after the close, want the Project's keys", got)
	}
}

// TestDetailsActionFromTheSearchClosesThem pins detailsActionStays: a row
// run from the `:` search opened over the details closes them first when it
// opens something they would cover (the inbox), and leaves them up when it
// does not (the keys overlay). Both rows are load-bearing: the first keeps
// two overlays from fighting for the keyboard, the second keeps `?` usable.
func TestDetailsActionFromTheSearchClosesThem(t *testing.T) {
	m, _ := detailsFromProject(t)
	m.buildKnown = true
	got, _ := m.runLevelAction(levelProject, inputDetail, actionInbox, false)
	if gm := got.(Model); gm.detailsOpen || !gm.inboxOpen {
		t.Fatalf("inbox from the search: details=%v inbox=%v, want the details closed and the inbox open", gm.detailsOpen, gm.inboxOpen)
	}
	got, _ = m.runLevelAction(levelProject, inputDetail, actionKeys, false)
	if gm := got.(Model); !gm.detailsOpen || !gm.keysOpen {
		t.Fatalf("keys from the search: details=%v keys=%v, want both up", gm.detailsOpen, gm.keysOpen)
	}
}

// TestDetailsTabRow pins the tab row: the visible tabs numbered by position
// (Stack only with git-spice, so 3 is Notes without it), cut to the width,
// and the active tab filled with strip.
func TestDetailsTabRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   stackState
		want string
	}{
		{"git-spice", stackState{Detected: true}, " 1 Overview  2 Waves  3 Stack  4 Notes  5 Diff "},
		{"plain git", stackState{}, " 1 Overview  2 Waves  3 Notes  4 Diff "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := renderDetailsTabRow(visibleDetailTabs(tc.st), tabWaves, 50)
			if got := strings.TrimRight(xansi.Strip(row), " "); got != strings.TrimRight(tc.want, " ") {
				t.Errorf("row = %q, want %q", got, tc.want)
			}
			if lipgloss.Width(row) != 50 {
				t.Errorf("row is %d cells, want 50", lipgloss.Width(row))
			}
			at := strings.Index(xansi.Strip(row), "2 Waves")
			if c := cellOf(t, row, at, 0); hexOf(c.Style.Bg) != palette.Tide.Strip {
				t.Errorf("active tab bg = %s, want the strip %s", hexOf(c.Style.Bg), palette.Tide.Strip)
			}
		})
	}
	if row := xansi.Strip(renderDetailsTabRow(visibleDetailTabs(stackState{Detected: true}), tabOverview, 20)); lipgloss.Width(row) != 20 {
		t.Errorf("a narrow row is %d cells, want cut to 20: %q", lipgloss.Width(row), row)
	}
}

// restartFakes wires the existing restart seams (SpawnHost, the restart's
// host start, and OpenHostPane, the dial its arrival chains into) to
// counters, so a test reads how many restarts and dials a key sent without
// starting a process.
type restartFakes struct {
	spawns, opens int
	pane          *fakePane
}

func withRestartFakes(m Model) (Model, *restartFakes) {
	f := &restartFakes{}
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		f.spawns++
		return session.HostProcess{PID: 4242}, nil
	}
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		f.opens++
		f.pane = newFakePane("A")
		return f.pane, nil
	}
	return m, f
}

// detailsRestartChipDrawn is whether the drawn overlay carries the
// `r restart` chip (keyChip's key, padded, then the action).
func detailsRestartChipDrawn(m Model) bool {
	_, _, w, h := m.detailsBox()
	return strings.Contains(xansi.Strip(m.renderDetailsOverlay(w, h)), " r  restart")
}

// TestEnterOnASessionOpensItsAgentOrItsDetails drives enter through Update
// for each kind of row sessionEnterTarget splits (§4.3). Load-bearing rows:
// "stopped" opens the details and starts nothing (enter never restarts a
// host); "running" connects and goes to the Agent; "pending" and "cleaning"
// open the details, where there is nothing to restart either.
func TestEnterOnASessionOpensItsAgentOrItsDetails(t *testing.T) {
	cleaning := func(m Model) Model {
		m.cleaningSessions = map[cleanID]bool{cleanIDByName("a"): true}
		return m
	}
	for _, tc := range []struct {
		name        string
		row         Session
		setup       func(Model) Model
		wantDetails bool
		wantDial    bool
	}{
		{name: "running", row: runningSess("a"), wantDial: true},
		{name: "stopped", row: stoppedSess("a"), wantDetails: true},
		{name: "pending", row: Session{SessionState: discover.SessionState{Name: "a"}, Pending: true}, wantDetails: true},
		{name: "cleaning", row: stoppedSess("a"), setup: cleaning, wantDetails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := withRestartFakes(testModel(tc.row))
			if tc.setup != nil {
				m = tc.setup(m)
			}
			m, cmd := update(t, m, keyEnter)
			m = settleConnect(t, m, cmd)
			if f.spawns != 0 {
				t.Errorf("enter started %d host(s), want none", f.spawns)
			}
			if m.detailsOpen != tc.wantDetails {
				t.Errorf("details open = %v, want %v", m.detailsOpen, tc.wantDetails)
			}
			if tc.wantDetails && (m.detailsFrom != levelProject || m.level() != levelProject) {
				t.Errorf("details from %v at %v, want over the Project", m.detailsFrom, m.level())
			}
			if (f.opens == 1) != tc.wantDial {
				t.Errorf("dials = %d, want a dial: %v", f.opens, tc.wantDial)
			}
			if tc.wantDial && !m.paneOwnsKeyboard() {
				t.Error("enter on a running row did not land in its agent")
			}
		})
	}
}

// TestDetailsRestartKey is `r` inside the details overlay (T16.5, A4).
// Load-bearing rows:
//
//   - "stopped" sends exactly one restart through the fake seam, and the
//     arriving pane closes the overlay and opens the Agent with the keyboard
//     on it: the restart the owner waited on in the details lands them in it.
//   - "stopped, closed before the pane lands" is the moved-away rule: the
//     pane installs quietly behind the Project and the keyboard stays there.
//   - "pending" and "cleaning" send nothing and draw no chip: restarting
//     either is not allowed, so neither the key nor the chip may offer it.
//   - "wedged" keeps the existing confirm: the restart would kill a live
//     process.
//   - "healthy running" has nothing to restart: no chip, r does nothing.
func TestDetailsRestartKey(t *testing.T) {
	pending := Session{SessionState: discover.SessionState{Name: "a"}, Pending: true}
	for _, tc := range []struct {
		name string
		row  Session
		// open is the key that opens the details: enter on a row with no
		// live agent, d on one with an agent.
		open        tea.KeyMsg
		cleaning    bool
		closeFirst  bool
		wantChip    bool
		wantSpawns  int
		wantModal   inputMode
		wantInAgent bool
		wantDetails bool
	}{
		{name: "stopped", row: stoppedSess("a"), open: keyEnter, wantChip: true, wantSpawns: 1, wantInAgent: true},
		{name: "stopped, closed before the pane lands", row: stoppedSess("a"), open: keyEnter, closeFirst: true, wantChip: true, wantSpawns: 1},
		{name: "pending", row: pending, open: keyEnter, wantDetails: true},
		{name: "cleaning", row: stoppedSess("a"), open: keyEnter, cleaning: true, wantDetails: true},
		{name: "wedged", row: wedgedSess("a", 12*time.Minute), open: keyRune('d'), wantChip: true, wantModal: inputConfirmRestart, wantDetails: true},
		{name: "healthy running", row: runningSess("a"), open: keyRune('d'), wantDetails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := withRestartFakes(testModel(tc.row))
			if tc.cleaning {
				m.cleaningSessions = map[cleanID]bool{cleanIDByName("a"): true}
			}
			m, _ = update(t, m, tc.open)
			if !m.detailsOpen {
				t.Fatal("setup: the details did not open")
			}
			if got := detailsRestartChipDrawn(m); got != tc.wantChip {
				t.Errorf("r restart chip drawn = %v, want %v", got, tc.wantChip)
			}
			if got := strings.Contains(xansi.Strip(m.renderHelp()), "r restart"); got != tc.wantChip {
				t.Errorf("help line names r restart = %v, want %v", got, tc.wantChip)
			}
			m, cmd := update(t, m, keyRune('r'))
			if tc.closeFirst {
				m, _ = update(t, m, keyEsc)
			}
			m = settleConnect(t, m, cmd)
			if f.spawns != tc.wantSpawns {
				t.Errorf("restart requests = %d, want %d", f.spawns, tc.wantSpawns)
			}
			if m.modal != tc.wantModal {
				t.Errorf("modal = %v, want %v", m.modal, tc.wantModal)
			}
			if got := m.paneOwnsKeyboard(); got != tc.wantInAgent {
				t.Errorf("keyboard in the agent = %v, want %v", got, tc.wantInAgent)
			}
			if tc.wantInAgent && m.level() != levelAgent {
				t.Errorf("level = %v, want the Agent", m.level())
			}
			if m.detailsOpen != tc.wantDetails {
				t.Errorf("details open = %v, want %v", m.detailsOpen, tc.wantDetails)
			}
			if tc.closeFirst && (m.pane == nil || m.focus != focusList) {
				t.Errorf("pane=%v focus=%v, want the pane installed behind the Project", m.pane != nil, m.focus)
			}
		})
	}
}
