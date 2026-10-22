package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/session"
)

// derivedMode is the one question every row in this file asks of a Model built
// through Update: who owns the keyboard right now. It goes through
// modeSnapshot rather than reading m.detailOwnsKeyboard() directly, because the
// snapshot's conjunction (mode.go) is half of the contract — a focused flag
// whose surface is not drawn must not claim the keyboard.
func derivedMode(m Model) inputMode { return activeInputMode(m.modeSnapshot()) }

// detailFocusFixture builds a dashboard whose pane is attached to `paned` and
// whose cursor sits on the row named by `cursor`. Both the ctrl+t case and the
// cursor-moved-away case below need a live-but-undrawn pane, and hand-rolling
// that twice is how the two states drift apart in a test file.
func detailFocusFixture(t *testing.T, paned, cursor string) Model {
	t.Helper()
	m := testModel(runningSess("alpha"), runningSess("beta"))
	m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: paned})
	for i, s := range m.sessions {
		if s.Name == cursor {
			m.list.Select(i)
		}
	}
	return m
}

// focusOverview puts the keyboard on the overview the way a user gets there
// from the list since T16.2: `d`, the selected session's details, which until
// T16.5 is the overview with the keyboard (A2). enter on the list opens the
// agent instead.
func focusOverview(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(t, m, keyRune('d'))
	if cmd != nil {
		t.Fatalf("setup: d dispatched %T, want no lifecycle work", cmd())
	}
	if !m.detailOwnsKeyboard() {
		t.Fatalf("setup: d should have focused the overview")
	}
	return m
}

// settleConnect delivers a connect's whole chain back through Update, the way
// the runtime would: a restart's hostRestartedMsg first, then the dial's
// paneAttachedMsg. It stops at the first paneAttachedMsg, because the Cmd that
// follows an install is the pane's redraw subscription, which blocks by design.
// A chain that never reaches a pane (a refusal, a claimed dial whose arrival
// the test delivers itself) returns the Model unchanged past its last message.
func settleConnect(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for i := 0; cmd != nil && i < 3; i++ {
		var next tea.Cmd
		for _, msg := range collectMsgs(t, cmd) {
			switch msg := msg.(type) {
			case paneAttachedMsg:
				m, _ = update(t, m, msg)
				return m
			case hostRestartedMsg:
				m, next = update(t, m, msg)
			}
		}
		cmd = next
	}
	return m
}

// TestDetailFocusModeDerivation is the mode matrix for the detail pane's focus
// state, driven end to end through Update rather than at the modeState level
// (TestActiveInputModePrecedenceMatrix covers the pure table).
//
// Load-bearing rows, and why:
//
//   - "overview view with a pane installed" and "agent view, cursor off the
//     paned row" are the two states where a session pane and the detail pane
//     are both in play. The VIEW decides between them, never the pane: in
//     overview view `f` focuses the overview even with the selected row's pane
//     alive behind it, and in agent view `f` connects the selected row even
//     though a pane for some other row is installed. The old model guessed
//     from "what is drawn", which is how enter came to focus the overview by
//     accident.
//   - The four overlay rows are the guard against silent precedence shadowing:
//     modePrecedence is hand-ordered and a misplaced entry is shadowed with no
//     error, so every surface that outranks normal mode must still outrank
//     detail focus.
//   - "empty state" proves the entry predicate is the real "detail is drawn"
//     question (!shouldRenderPane && hasActiveProject) and not something that
//     reduces to true: with no active project View paints renderEmptyState and
//     there is no detail box to focus.
func TestDetailFocusModeDerivation(t *testing.T) {
	cases := []struct {
		name            string
		setup           func(t *testing.T) Model
		wantMode        inputMode
		wantDetail      bool
		wantPaneFocused bool
	}{
		{
			name:       "detail pane drawn, enter not pressed",
			setup:      func(t *testing.T) Model { return testModel(runningSess("alpha")) },
			wantMode:   inputNormal,
			wantDetail: false,
		},
		{
			name: "d opens the details",
			setup: func(t *testing.T) Model {
				return focusOverview(t, testModel(runningSess("alpha")))
			},
			wantMode:   inputDetail,
			wantDetail: true,
		},
		{
			name: "session pane rendered and unfocused is unchanged",
			setup: func(t *testing.T) Model {
				m := testModel(runningSess("alpha"))
				m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
				return m
			},
			wantMode:   inputNormal,
			wantDetail: false,
		},
		{
			name: "session pane rendered, enter focuses the session pane as before",
			setup: func(t *testing.T) Model {
				m := testModel(runningSess("alpha"))
				m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
				m, _ = update(t, m, keyEnter)
				return m
			},
			wantMode:        inputPane,
			wantDetail:      false,
			wantPaneFocused: true,
		},
		{
			name: "a pane installed behind the Project: d opens the details, the pane stays undrawn",
			setup: func(t *testing.T) Model {
				m := detailFocusFixture(t, "alpha", "alpha")
				m = focusOverview(t, m)
				if m.shouldRenderPane() {
					t.Fatalf("d must not reveal the pane behind the Project")
				}
				return m
			},
			wantMode:   inputDetail,
			wantDetail: true,
		},
		{
			name: "agent view, cursor off the paned row: enter connects the selected row",
			setup: func(t *testing.T) Model {
				m := detailFocusFixture(t, "alpha", "beta")
				if m.shouldRenderPane() {
					t.Fatalf("setup: the detail pane should be the drawn surface")
				}
				m, cmd := update(t, m, keyEnter)
				if cmd == nil || m.attachInFlight.name != "beta" {
					t.Fatalf("enter should have dialed beta: cmd=%v inFlight=%+v", cmd != nil, m.attachInFlight)
				}
				return m
			},
			// Until beta's pane arrives, the agent placeholder is what is
			// drawn and it holds the keyboard: never the list.
			wantMode:   inputPlaceholder,
			wantDetail: false,
		},
		{
			name: "empty state: enter focuses nothing",
			setup: func(t *testing.T) Model {
				m := testModel()
				m.activeProject = -1
				if m.hasActiveProject() {
					t.Fatalf("setup: the empty state must have no active project")
				}
				m, _ = update(t, m, keyEnter)
				if m.status != "" {
					t.Fatalf("status = %q, want silent no-op", m.status)
				}
				return m
			},
			wantMode:   inputNormal,
			wantDetail: false,
		},
		{
			name: "a modal over a focused detail pane still owns the keyboard",
			setup: func(t *testing.T) Model {
				m := focusOverview(t, testModel(runningSess("alpha")))
				// spawnFinishedMsg raises this modal asynchronously, so it can
				// land over any surface — the same reachability modePrecedence's
				// own comment records for the switcher.
				m.modal = inputSpawnError
				return m
			},
			wantMode:   inputSpawnError,
			wantDetail: true,
		},
		{
			name: "the creation form over a focused detail pane still owns the keyboard",
			setup: func(t *testing.T) Model {
				m := focusOverview(t, testModel(runningSess("alpha")))
				m.formOpen = true
				return m
			},
			wantMode:   inputForm,
			wantDetail: true,
		},
		{
			name: "the project switcher over a focused detail pane still owns the keyboard",
			setup: func(t *testing.T) Model {
				m := focusOverview(t, testModel(runningSess("alpha")))
				m.switcherOpen = true
				return m
			},
			wantMode:   inputSwitcher,
			wantDetail: true,
		},
		{
			name: "the actions dialog over a focused detail pane owns the keyboard",
			setup: func(t *testing.T) Model {
				m := focusOverview(t, testModel(runningSess("alpha")))
				m.leaderOpen = true
				return m
			},
			wantMode:   inputLeader,
			wantDetail: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			if got := derivedMode(m); got != tc.wantMode {
				t.Errorf("mode = %v, want %v", got, tc.wantMode)
			}
			if m.detailOwnsKeyboard() != tc.wantDetail {
				t.Errorf("detailOwnsKeyboard = %v, want %v", m.detailOwnsKeyboard(), tc.wantDetail)
			}
			if m.paneOwnsKeyboard() != tc.wantPaneFocused {
				t.Errorf("paneOwnsKeyboard = %v, want %v", m.paneOwnsKeyboard(), tc.wantPaneFocused)
			}
		})
	}
}

// TestDetailFocusReleaseKeys pins the overview's way back: esc gives the
// keyboard to the session list (levelGoBack, A2).
func TestDetailFocusReleaseKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		press func(t *testing.T, m Model) Model
	}{
		{"esc releases", func(t *testing.T, m Model) Model {
			m, _ = update(t, m, keyEscMsg())
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := focusOverview(t, testModel(runningSess("alpha")))
			m = tc.press(t, m)
			if m.detailOwnsKeyboard() {
				t.Fatalf("detailOwnsKeyboard = true, want the release to have cleared it")
			}
			if got := derivedMode(m); got != inputNormal {
				t.Fatalf("mode = %v, want %v after the release", got, inputNormal)
			}
		})
	}
}

// TestDetailFocusRetainedKeySet enumerates the details overlay's rules at the
// routing layer (T16.5): esc closes it, the tab and scroll keys, q and ctrl+c
// quit, and `:` and `?` open over it. The overlay is an overlay, so the key
// table is not asked while it is up: every other key — the Project letters,
// enter, the leader — must be ignored, or a letter would act on the level
// under the box, or over the Agent reach the agent behind it. Every row is
// load-bearing in that way.
//
// The ten W2.T1 scroll keys are included below alongside the rest of the
// surface; TestDetailFocusedScrollKeyMatrix carries the "does it actually
// scroll" behavioral half this routing-only table cannot make.
func TestDetailFocusRetainedKeySet(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	conds := routeConds{paneOpen: true}

	cases := []struct {
		name string
		key  tea.KeyPressMsg
		want keyDecision
	}{
		{"esc closes the overlay", tea.KeyPressMsg{Code: tea.KeyEsc}, keyDecision{routeAction, actionDetailsClose}},
		{"ctrl+backslash is ignored", chord('\\'), keyDecision{kind: routeIgnored}},
		{"q quits", press('q', "q"), keyDecision{routeAction, actionQuit}},
		{"ctrl+c quits", chord('c'), keyDecision{routeAction, actionQuit}},
		{"the leader is ignored", tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}, keyDecision{kind: routeIgnored}},
		{"ctrl+g is ignored", chord('g'), keyDecision{kind: routeIgnored}},
		{"ctrl+t is ignored", chord('t'), keyDecision{kind: routeIgnored}},
		{"tab moves to the next tab", tea.KeyPressMsg{Code: tea.KeyTab}, keyDecision{routeAction, actionDetailTabNext}},
		{"shift+tab moves to the previous tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, keyDecision{routeAction, actionDetailTabPrev}},
		{"1 jumps to a tab", press('1', "1"), keyDecision{routeAction, actionDetailTabJump}},
		{"5 jumps to a tab", press('5', "5"), keyDecision{routeAction, actionDetailTabJump}},

		{"up scrolls up", tea.KeyPressMsg{Code: tea.KeyUp}, keyDecision{routeAction, actionDetailScrollUp}},
		{"k scrolls up", press('k', "k"), keyDecision{routeAction, actionDetailScrollUp}},
		{"down scrolls down", tea.KeyPressMsg{Code: tea.KeyDown}, keyDecision{routeAction, actionDetailScrollDown}},
		{"j scrolls down", press('j', "j"), keyDecision{routeAction, actionDetailScrollDown}},
		{"ctrl+u half-pages up", chord('u'), keyDecision{routeAction, actionDetailHalfPageUp}},
		{"ctrl+d half-pages down", chord('d'), keyDecision{routeAction, actionDetailHalfPageDown}},
		{"pgup pages up", tea.KeyPressMsg{Code: tea.KeyPgUp}, keyDecision{routeAction, actionDetailPageUp}},
		{"pgdown pages down", tea.KeyPressMsg{Code: tea.KeyPgDown}, keyDecision{routeAction, actionDetailPageDown}},
		{"home goes to the top", tea.KeyPressMsg{Code: tea.KeyHome}, keyDecision{routeAction, actionDetailGotoTop}},
		{"end goes to the bottom", tea.KeyPressMsg{Code: tea.KeyEnd}, keyDecision{routeAction, actionDetailGotoBottom}},

		{"enter is ignored", tea.KeyPressMsg{Code: tea.KeyEnter}, keyDecision{kind: routeIgnored}},
		{"n is ignored", press('n', "n"), keyDecision{kind: routeIgnored}},
		{"c is ignored", press('c', "c"), keyDecision{kind: routeIgnored}},
		{"l is ignored", press('l', "l"), keyDecision{kind: routeIgnored}},
		{"r is ignored", press('r', "r"), keyDecision{kind: routeIgnored}},
		{"p is ignored", press('p', "p"), keyDecision{kind: routeIgnored}},
		{"slash is ignored", press('/', "/"), keyDecision{kind: routeIgnored}},
		{"o is ignored", press('o', "o"), keyDecision{kind: routeIgnored}},
		{"colon opens the actions search", press(':', ":"), keyDecision{routeAction, actionActionsSearch}},
		{"question mark opens the keys", press('?', "?"), keyDecision{routeAction, actionKeys}},
		{"d is ignored", press('d', "d"), keyDecision{kind: routeIgnored}},
		{"f is ignored", press('f', "f"), keyDecision{kind: routeIgnored}},
		{"plain text is ignored", press('x', "x"), keyDecision{kind: routeIgnored}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reg.route(inputDetail, conds, tc.key); got != tc.want {
				t.Fatalf("route(inputDetail, %q) = {%v %v}, want {%v %v}",
					tc.key.String(), got.kind, got.action, tc.want.kind, tc.want.action)
			}
		})
	}
}

// TestDetailsKeepTheProjectKeysOut is the behavioural half of the table
// above: with the details overlay open the Project's keys do nothing, so a
// key meant for the box cannot act on the level under it. Every row is
// load-bearing, and each reverses the T16.2 rule that the overview `d` used
// to give the keyboard to answered them: enter opened the agent, n the
// new-session form, c the clean confirmation.
func TestDetailsKeepTheProjectKeysOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"enter opens nothing", keyEnter},
		{"n opens no form", keyRune('n')},
		{"c asks nothing", keyRune('c')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := focusOverview(t, testModel(runningSess("alpha")))
			m, cmd := update(t, m, tc.key)
			if cmd != nil || !m.detailsOpen || m.formOpen || m.modal != inputNormal || m.attachInFlight.name != "" {
				t.Errorf("%s: cmd=%v details=%v form=%v modal=%v dial=%q, want the details open and nothing else",
					tc.name, cmd != nil, m.detailsOpen, m.formOpen, m.modal, m.attachInFlight.name)
			}
		})
	}
}

// TestRightFocusSurvivesViewFlipsAndPaneLoss pins that nothing about the
// right side changing hands the keyboard to the list, where typed keys would
// hit list bindings (`q` quits, `c` cleans): the details overlay over the
// Agent moves no focus, so esc lands back in the agent, and a tick that takes
// the paned row away leaves the keyboard on the right.
//
// Both rows reach the change through a different channel on purpose. The
// ctrl+] d row is key-driven; the tick row is not, and a pre-dispatch check
// would pass the first and fail the second.
func TestRightFocusSurvivesViewFlipsAndPaneLoss(t *testing.T) {
	t.Run("ctrl+] d and enter round trip from the focused agent", func(t *testing.T) {
		m := detailFocusFixture(t, "alpha", "alpha")
		m, _ = update(t, m, keyEnter)
		if got := derivedMode(m); got != inputPane {
			t.Fatalf("setup: mode = %v, want enter to focus the installed pane", got)
		}

		m, cmd := leaderThen(t, m, 'd')
		if cmd != nil {
			t.Fatalf("ctrl+] d to the details dispatched %T, want nothing started", cmd())
		}
		if m.focus != focusRight || m.view != viewAgent || derivedMode(m) != inputDetail || !m.shouldRenderPane() {
			t.Fatalf("after ctrl+] d: focus=%v view=%v mode=%v drawn=%v, want right/agent/detail over the drawn pane",
				m.focus, m.view, derivedMode(m), m.shouldRenderPane())
		}
		m, cmd = update(t, m, keyEscMsg())
		if cmd != nil {
			t.Fatalf("esc back to the agent dispatched %T, want nothing started", cmd())
		}
		if m.focus != focusRight || m.view != viewAgent || derivedMode(m) != inputPane {
			t.Fatalf("after esc: focus=%v view=%v mode=%v, want right/agent/pane",
				m.focus, m.view, derivedMode(m))
		}
	})

	t.Run("a tick that removes the paned row", func(t *testing.T) {
		m := detailFocusFixture(t, "alpha", "alpha")
		m, _ = update(t, m, keyEnter)
		if !m.paneOwnsKeyboard() {
			t.Fatalf("setup: the focused pane should own the keyboard")
		}

		m, _ = update(t, m, sessionsLoadedMsg{
			gen:      m.projectGen,
			sessions: []Session{runningSess("beta")},
			at:       m.deps.Now(),
		})
		if m.shouldRenderPane() {
			t.Fatalf("setup: dropping the paned row should take the pane off the screen")
		}
		// beta is running with no pane: the Agent draws its placeholder, and
		// the placeholder keeps the keyboard.
		if got := derivedMode(m); got != inputPlaceholder {
			t.Fatalf("mode = %v after the paned row left discovery, want %v — the right side keeps the keyboard",
				got, inputPlaceholder)
		}
	})
}

// TestEnterOpensTheAgentBehindTheOverview is the regression for the Enter
// path through Update: focus the agent, ctrl+] d to the details, esc back to
// the agent, ctrl+] esc back to the Project, then enter. Enter goes in a
// level, so it opens the agent installed behind the Project, with no
// lifecycle work — the pane was connected all along (A5).
func TestEnterOpensTheAgentBehindTheOverview(t *testing.T) {
	m := detailFocusFixture(t, "alpha", "alpha")
	m, _ = update(t, m, keyEnter)
	m = pressKey(t, m, keyRune('d'))
	m = pressKey(t, m, keyEscMsg())
	m = pressKey(t, m, keyEscMsg())
	if m.shouldRenderPane() || m.focus != focusList || m.pane == nil {
		t.Fatalf("setup: drawn=%v focus=%v pane=%v, want the Project, list, pane kept",
			m.shouldRenderPane(), m.focus, m.pane != nil)
	}

	m, cmd := update(t, m, keyEnter)
	if cmd != nil {
		t.Fatalf("enter dispatched %T, want no lifecycle work for a connected pane", cmd())
	}
	if m.view != viewAgent || !m.paneOwnsKeyboard() {
		t.Fatalf("view = %v, pane focused %v; want enter to open the agent", m.view, m.paneOwnsKeyboard())
	}
}

type focusSurface string

const (
	focusSurfacePane   focusSurface = "session pane"
	focusSurfaceDetail focusSurface = "detail"
	focusSurfaceNone   focusSurface = "none"
)

// TestEnterAndFocusKeyCurrentViewMatrix is the list-focus enter contract.
// Since T16.2 enter on a session at Project opens its Agent (openAgent),
// whichever view the right side showed: the selected row's pane is focused,
// or a running selected session is connected with the right side focused at
// the keypress so the pane lands focused, the agent placeholder holding the
// keyboard meanwhile. A row with no live agent has no agent to open, and
// enter opens its details overlay over the Project (T16.5) — it never
// restarts (plan W3.T1 d).
//
// Every row is driven through Update, and every connect is SETTLED — its
// restart and dial results delivered back through Update — because the old
// failure was exactly an arrival undoing what the keypress chose.
//
// Load-bearing rows:
//
//   - "running row, no pane" is objective 4: one open via openPaneFor, focus
//     right at the keypress, and the pane owning the keyboard once it lands.
//   - "stopped row" is the user's reversal of W2's enter-restarts: zero
//     spawns, zero opens, the details open over the list.
//   - "preview dial already in flight" is the retired 300ms race. Enter must
//     CLAIM the preview's dial (zero further opens) and the preview's own
//     connection must install focused — the old code dropped it as stale
//     because right focus with no pane yet read as detail ownership.
//   - "pending preview timer" is the other half of that race: the timer armed
//     before the keypress must not dial a second client after it.
//   - "overview view with a hidden pane opens the agent" is T16.2's change:
//     enter goes in a level, to the agent, even from the overview view —
//     `d` is the way to the overview now.
//   - The pending, worktree-fault and cleaning rows have no live agent either,
//     so they open their details with no Cmd, exactly like the stopped row.
func TestEnterAndFocusKeyCurrentViewMatrix(t *testing.T) {
	const unchangedStatus = "status before the no-project no-op"

	type lifecycleCounts struct{ opens, spawns int }
	type focusCase struct {
		name  string
		setup func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg)

		// Immediately after the key.
		wantCommand bool
		wantStatus  string
		wantFocus   paneFocus
		wantView    rightView
		wantMode    inputMode
		wantTab     detailTab

		// After every Cmd the key returned, and any late message the setup
		// handed back, has been delivered through Update.
		wantSettledMode    inputMode
		wantSettledSurface focusSurface
		wantSettledPane    string
		wantOpens          int
		wantSpawns         int
	}

	// running pane fixture: alpha's pane installed and drawn, list focused.
	paneOn := func(cursor string) func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
		return func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
			return detailFocusFixture(t, "alpha", cursor), nil, nil
		}
	}
	cases := []focusCase{
		{
			name:       "agent view, the selected row's pane installed",
			setup:      paneOn("alpha"),
			wantStatus: "input goes to alpha · ctrl+] esc to return",
			wantFocus:  focusRight, wantView: viewAgent, wantMode: inputPane, wantTab: tabOverview,
			wantSettledMode: inputPane, wantSettledSurface: focusSurfacePane, wantSettledPane: "alpha",
		},
		{
			name: "agent view, running row, no pane",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				return testModel(runningSess("alpha")), nil, nil
			},
			wantCommand: true, wantStatus: "opening alpha…",
			wantFocus: focusRight, wantView: viewAgent, wantMode: inputPlaceholder, wantTab: tabOverview,
			wantSettledMode: inputPane, wantSettledSurface: focusSurfacePane, wantSettledPane: "alpha",
			wantOpens: 1,
		},
		{
			name: "agent view, running row on a non-Overview tab",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				m := testModel(runningSess("alpha"))
				m.detailTab = tabWaves
				return m, nil, nil
			},
			wantCommand: true, wantStatus: "opening alpha…",
			wantFocus: focusRight, wantView: viewAgent, wantMode: inputPlaceholder, wantTab: tabWaves,
			wantSettledMode: inputPane, wantSettledSurface: focusSurfacePane, wantSettledPane: "alpha",
			wantOpens: 1,
		},
		{
			name:        "agent view, cursor off the paned row",
			setup:       paneOn("beta"),
			wantCommand: true, wantStatus: "switching to beta…",
			wantFocus: focusRight, wantView: viewAgent, wantMode: inputPlaceholder, wantTab: tabOverview,
			wantSettledMode: inputPane, wantSettledSurface: focusSurfacePane, wantSettledPane: "beta",
			wantOpens: 1,
		},
		{
			name: "agent view, stopped row opens its details without restarting",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				return testModel(stoppedSess("alpha")), nil, nil
			},
			wantFocus: focusList, wantView: viewAgent, wantMode: inputDetail, wantTab: tabOverview,
			wantSettledMode: inputDetail, wantSettledSurface: focusSurfaceDetail,
		},
		{
			name: "overview view with a hidden pane opens the agent",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				m := detailFocusFixture(t, "alpha", "alpha")
				m.view = viewOverview
				return m, nil, nil
			},
			wantStatus: "input goes to alpha · ctrl+] esc to return",
			wantFocus:  focusRight, wantView: viewAgent, wantMode: inputPane, wantTab: tabOverview,
			wantSettledMode: inputPane, wantSettledSurface: focusSurfacePane, wantSettledPane: "alpha",
		},
		{
			name: "overview view, no pane, connects the agent",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				m := testModel(runningSess("alpha"))
				m.view = viewOverview
				return m, nil, nil
			},
			wantCommand: true, wantStatus: "opening alpha…",
			wantFocus: focusRight, wantView: viewAgent, wantMode: inputPlaceholder, wantTab: tabOverview,
			wantSettledMode: inputPane, wantSettledSurface: focusSurfacePane, wantSettledPane: "alpha",
			wantOpens: 1,
		},
		{
			name: "pending row opens its details",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				return testModel(Session{SessionState: sampleState("alpha"), Pending: true}), nil, nil
			},
			wantFocus: focusList, wantView: viewAgent, wantMode: inputDetail, wantTab: tabOverview,
			wantSettledMode: inputDetail, wantSettledSurface: focusSurfaceDetail,
		},
		{
			name: "worktree-fault row opens its details",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				return testModel(faultSess("alpha", session.KindWorktreeMissing, "worktree is gone")), nil, nil
			},
			wantFocus: focusList, wantView: viewAgent, wantMode: inputDetail, wantTab: tabOverview,
			wantSettledMode: inputDetail, wantSettledSurface: focusSurfaceDetail,
		},
		{
			name: "cleaning row opens its details",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				m := testModel(runningSess("alpha"))
				s, _ := m.selected()
				m.markCleaning(cleanIDFor(s))
				return m, nil, nil
			},
			wantFocus: focusList, wantView: viewAgent, wantMode: inputDetail, wantTab: tabOverview,
			wantSettledMode: inputDetail, wantSettledSurface: focusSurfaceDetail,
		},
		{
			name: "pending preview timer not yet dispatched",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				m := testModel(runningSess("alpha"), runningSess("beta"))
				m, _ = update(t, m, keyDown)
				timer := previewTimerMsg{gen: m.previewGen, name: "beta"}
				return m, &timer, nil
			},
			wantCommand: true, wantStatus: "opening beta…",
			wantFocus: focusRight, wantView: viewAgent, wantMode: inputPlaceholder, wantTab: tabOverview,
			wantSettledMode: inputPane, wantSettledSurface: focusSurfacePane, wantSettledPane: "beta",
			wantOpens: 1,
		},
		{
			name: "preview dial already in flight",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				m := testModel(runningSess("alpha"), runningSess("beta"))
				m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
					return newFakePane("preview"), nil
				}
				m, _ = update(t, m, keyDown)
				m, dial := update(t, m, previewTimerMsg{gen: m.previewGen, name: "beta"})
				if dial == nil {
					t.Fatal("setup: preview timer must dispatch its dial")
				}
				msg, ok := dial().(paneAttachedMsg)
				if !ok {
					t.Fatalf("setup: preview dial returned %T, want paneAttachedMsg", dial())
				}
				return m, nil, &msg
			},
			wantStatus: "opening beta…",
			wantFocus:  focusRight, wantView: viewAgent, wantMode: inputPlaceholder, wantTab: tabOverview,
			wantSettledMode: inputPane, wantSettledSurface: focusSurfacePane, wantSettledPane: "beta",
		},
		{
			name: "no active project with empty status",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				m := testModel()
				m.activeProject = -1
				return m, nil, nil
			},
			wantFocus: focusList, wantView: viewAgent, wantMode: inputNormal, wantTab: tabOverview,
			wantSettledMode: inputNormal, wantSettledSurface: focusSurfaceNone,
		},
		{
			name: "no active project with preexisting status",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *paneAttachedMsg) {
				m := testModel()
				m.activeProject = -1
				m.status = unchangedStatus
				return m, nil, nil
			},
			wantStatus: unchangedStatus,
			wantFocus:  focusList, wantView: viewAgent, wantMode: inputNormal, wantTab: tabOverview,
			wantSettledMode: inputNormal, wantSettledSurface: focusSurfaceNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []struct {
				name string
				msg  tea.KeyMsg
			}{
				{name: "enter", msg: keyEnter},
			} {
				t.Run(key.name, func(t *testing.T) {
					m, timer, arrival := tc.setup(t)
					var counts lifecycleCounts
					m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
						counts.opens++
						return newFakePane("connected"), nil
					}
					m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
						counts.spawns++
						return session.HostProcess{PID: 4243}, nil
					}

					got, cmd := update(t, m, key.msg)
					if (cmd != nil) != tc.wantCommand {
						t.Errorf("command returned = %v, want %v", cmd != nil, tc.wantCommand)
					}
					if got.status != tc.wantStatus {
						t.Errorf("status = %q, want %q", got.status, tc.wantStatus)
					}
					if got.focus != tc.wantFocus || got.view != tc.wantView {
						t.Errorf("focus/view = %v/%v, want %v/%v", got.focus, got.view, tc.wantFocus, tc.wantView)
					}
					if mode := derivedMode(got); mode != tc.wantMode {
						t.Errorf("mode = %v, want %v", mode, tc.wantMode)
					}
					if tab := got.activeDetailTab(); tab != tc.wantTab {
						t.Errorf("active detail tab = %v, want %v", tab, tc.wantTab)
					}

					// Settle: the key's own chain, then the timer armed before
					// the key, then the preview arrival dispatched before it —
					// the order the runtime delivers them in the race.
					got = settleConnect(t, got, cmd)
					if timer != nil {
						var late tea.Cmd
						got, late = update(t, got, *timer)
						if late != nil {
							t.Error("a preview timer armed before the key dialed after it")
						}
					}
					if arrival != nil {
						got, _ = update(t, got, *arrival)
					}

					if mode := derivedMode(got); mode != tc.wantSettledMode {
						t.Errorf("settled mode = %v, want %v", mode, tc.wantSettledMode)
					}
					if got.paneName != tc.wantSettledPane {
						t.Errorf("settled paneName = %q, want %q", got.paneName, tc.wantSettledPane)
					}
					switch tc.wantSettledSurface {
					case focusSurfacePane:
						if !got.shouldRenderPane() {
							t.Error("settled surface is not the expected session pane")
						}
					case focusSurfaceDetail:
						if !got.detailsOpen {
							t.Error("settled surface is not the expected details overlay")
						}
					case focusSurfaceNone:
						if got.shouldRenderPane() || got.detailsOpen {
							t.Errorf("settled surface is pane=%v details=%v, want no surface over the level",
								got.shouldRenderPane(), got.detailsOpen)
						}
					}
					if counts.opens != tc.wantOpens || counts.spawns != tc.wantSpawns {
						t.Errorf("lifecycle calls = open:%d spawn:%d, want open:%d spawn:%d",
							counts.opens, counts.spawns, tc.wantOpens, tc.wantSpawns)
					}
				})
			}
		})
	}
}

// TestDetailsOverlayCarriesTheAccent drives the Overview through `d`: the
// details overlay has the keyboard, so its box wears the accent border and
// the accent strip (§3.3), and opening it runs nothing.
func TestDetailsOverlayCarriesTheAccent(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m, cmd := update(t, m, keyRune('d'))
	if cmd != nil {
		t.Fatalf("d dispatched %T, want no lifecycle work while opening the details", cmd())
	}
	if !m.detailOwnsKeyboard() || m.activeDetailTab() != tabOverview {
		t.Fatalf("d did not open the Overview: detailOwnsKeyboard=%v tab=%v", m.detailOwnsKeyboard(), m.activeDetailTab())
	}
	w, h := m.detailsSize()
	box := m.renderDetailsOverlay(w, h)
	if c := cellOf(t, box, 0, 2); hexOf(c.Style.Fg) != palette.Tide.Accent {
		t.Errorf("left edge fg = %s, want the accent %s", hexOf(c.Style.Fg), palette.Tide.Accent)
	}
	if c := cellOf(t, box, 1, 1); hexOf(c.Style.Bg) != palette.Tide.Accent {
		t.Errorf("title strip bg = %s, want the accent %s", hexOf(c.Style.Bg), palette.Tide.Accent)
	}
}

// TestFocusKeyIsAdvertisedWheneverFocusingIsPossible pins the discoverability
// gate normalBindingEnabled owns for enter/f: advertised whenever there is a
// right side to focus, before and after a pane exists, and not in the empty
// state, where there is neither a detail box nor a selected agent.
//
// The ctrl+t half of each row is the load-bearing W2.T1 addition: list focus
// controls nothing on the right, so ctrl+t is not a list binding at all — not
// a rule, and not in the keymap `?` and the palette render — however much
// there is on the right to switch between.
func TestFocusKeyIsAdvertisedWheneverFocusingIsPossible(t *testing.T) {
	toggleListed := func(m Model) bool {
		reg := m.modes()
		spec := reg.spec(inputNormal)
		for _, b := range spec.keys.bindings {
			if key.Matches(chord('t'), b) {
				return true
			}
		}
		for _, r := range spec.rules {
			if key.Matches(chord('t'), r.binding) {
				return true
			}
		}
		return false
	}

	t.Run("no pane, detail drawn", func(t *testing.T) {
		m := testModel(runningSess("alpha"))
		if !m.normalBindingEnabled(m.keys.Focus) {
			t.Fatalf("%s must be advertised with the detail pane drawn", m.keys.Focus.Help().Key)
		}
		if toggleListed(m) {
			t.Fatalf("ctrl+t is a list binding — it must not be, list focus controls nothing on the right")
		}
	})

	t.Run("pane open", func(t *testing.T) {
		m := testModel(runningSess("alpha"))
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
		if !m.normalBindingEnabled(m.keys.Focus) {
			t.Fatalf("%s must be advertised with a pane open", m.keys.Focus.Help().Key)
		}
		if toggleListed(m) {
			t.Fatalf("ctrl+t is a list binding with a pane open — it must not be")
		}
	})

	t.Run("empty state advertises neither", func(t *testing.T) {
		m := testModel()
		m.activeProject = -1
		if m.normalBindingEnabled(m.keys.Focus) {
			t.Fatalf("%s must not be advertised in the empty state — there is no detail box to focus", m.keys.Focus.Help().Key)
		}
		if toggleListed(m) {
			t.Fatalf("ctrl+t must not be a list binding in the empty state")
		}
	})
}

// TestFocusHelpTextFitsTheEightyColumnBudget pins step 5a's measured budget.
// `enter/f focus view` rides the oversubscribed 80-column list footer, and its
// compact wording is what keeps it legible before the ellipsis costs a cell.
func TestFocusHelpTextFitsTheEightyColumnBudget(t *testing.T) {
	desc := defaultKeyMap().Focus.Help().Desc
	if len(desc) > 11 {
		t.Fatalf("enter/f's help description %q is %d characters, want no more than 11", desc, len(desc))
	}
	if desc == "focus pane" {
		t.Fatalf("enter/f no longer focuses only the pane; its help text must say so")
	}
}

// TestFocusedConnectArrivalYieldsToALaterReleaseToTheList pins that a
// connect's arrival honours the user's LAST focus choice. enter moves the
// focus right at the keypress; ctrl+\ while the dial is out moves it back.
// The arrival must then install the pane drawn but unfocused: moving the
// focus right with no keypress would deliver the next typed keys to the
// session the user just walked away from. The "no release" row is the
// control — without it a pane that never focuses would pass the first row.
func TestFocusedConnectArrivalYieldsToALaterReleaseToTheList(t *testing.T) {
	for _, tc := range []struct {
		name        string
		release     bool
		wantFocused bool
	}{
		{name: "released with ctrl+\\ while dialing", release: true, wantFocused: false},
		{name: "no release", release: false, wantFocused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("a"))
			pane := newFakePane("A")
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return pane, nil }

			m, cmd := update(t, m, keyEnter)
			if cmd == nil || m.focus != focusRight {
				t.Fatalf("setup: enter must dispatch a connect and focus right (cmd=%v focus=%v)", cmd != nil, m.focus)
			}
			if tc.release {
				m = pressKey(t, m, keyEscMsg())
				if m.focus != focusList {
					t.Fatalf("setup: ctrl+\\ must return focus to the list, got %v", m.focus)
				}
			}

			m, _ = update(t, m, drainForPaneAttached(t, cmd))
			// Installed either way; drawn only in the Agent. Released to the
			// list, the pane stays connected behind the Project (A5).
			if m.pane != pane || m.shouldRenderPane() != tc.wantFocused {
				t.Fatalf("the arrival must install a's pane, drawn only in the Agent (installed=%v drawn=%v)",
					m.pane == pane, m.shouldRenderPane())
			}
			if got := m.paneOwnsKeyboard(); got != tc.wantFocused {
				t.Fatalf("paneOwnsKeyboard = %v, want %v (focus=%v)", got, tc.wantFocused, m.focus)
			}
			if tc.release {
				m, _ = update(t, m, keyRune('x'))
				if len(pane.keys)+len(pane.texts) != 0 {
					t.Fatalf("a key typed at the list reached the session: keys=%v texts=%v", pane.keys, pane.texts)
				}
			}
		})
	}
}

// TestConnectSelectedRefusesARowWithNoLiveAgent pins "enter never restarts"
// at the entrance itself, not only in its callers' guards: connectSelected on
// a stopped row spawns nothing, dials nothing, and leaves the focus alone.
func TestConnectSelectedRefusesARowWithNoLiveAgent(t *testing.T) {
	m := testModel(stoppedSess("a"))
	spawns, opens := 0, 0
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		spawns++
		return session.HostProcess{PID: 4242}, nil
	}
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		opens++
		return newFakePane("A"), nil
	}

	got, cmd := m.connectSelected()
	m = got.(Model)
	if cmd != nil {
		_ = collectMsgs(t, cmd)
	}
	if cmd != nil || spawns != 0 || opens != 0 {
		t.Fatalf("connectSelected on a stopped row: cmd=%v spawns=%d opens=%d, want none", cmd != nil, spawns, opens)
	}
	if m.focus != focusList || m.restartInFlight.name != "" {
		t.Fatalf("focus=%v restartInFlight=%q, want list and no restart", m.focus, m.restartInFlight.name)
	}
}

// TestDetailFocusedScrollKeyMatrix is W2.T1's focused/unfocused key matrix:
// every key a user reaches for — up/down/j/k, pgup/pgdn, home/end and
// ctrl+u/ctrl+d — scrolls the detail viewport while focused and leaves the
// session cursor untouched, and never touches the viewport while unfocused.
//
// The UNFOCUSED rows are the regression guard, and they are the load-bearing
// half of this table: a routing mistake that leaks a detail-focused rule
// into inputNormal (or shadows an inputNormal rule with a same-named
// detail one) is obvious while focused — the diff visibly fails to scroll —
// but the identical mistake leaking the OTHER way would silently break
// session-list navigation for every user, focused or not, and nothing else
// in this file would catch it.
//
// ctrl+u/ctrl+d get their own group: they have no list meaning, so from the
// list they must do nothing at all — the list never drives the right pane
// (plan objective 2) — rather than fall back to some other action.
//
// Every upward assertion scrolls down first: at YOffset 0, pgup/home/k/up
// leave the offset unchanged whether or not the routing works, so a fixture
// starting there would pass the assertion vacuously.
func TestDetailFocusedScrollKeyMatrix(t *testing.T) {
	type step struct {
		name string
		key  tea.KeyMsg
	}
	downKeys := []step{
		{"down", keyDown},
		{"j", keyRune('j')},
		{"pgdown", tea.KeyPressMsg{Code: tea.KeyPgDown}},
		{"end", tea.KeyPressMsg{Code: tea.KeyEnd}},
	}
	upKeys := []step{
		{"up", keyUp},
		{"k", keyRune('k')},
		{"pgup", tea.KeyPressMsg{Code: tea.KeyPgUp}},
		{"home", tea.KeyPressMsg{Code: tea.KeyHome}},
	}
	// scrollFixture builds a two-session dashboard on the Waves tab, which is
	// the one tab bigPlanSession's fixture overflows (the Overview shows only
	// one wave) — mirroring TestPageUpDownStillPageTheSessionListNotTheDetailPane's
	// own fixture, so this table exercises the same known-overflowing content.
	scrollFixture := func(t *testing.T) Model {
		t.Helper()
		m := testModel(bigPlanSession("alpha"), bigPlanSession("beta"))
		m.detailTab = tabWaves
		m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
		return m
	}

	t.Run("unfocused: cursor/page/jump keys move the session list, never the viewport", func(t *testing.T) {
		all := append(append([]step{}, downKeys...), upKeys...)
		for _, tc := range all {
			t.Run(tc.name, func(t *testing.T) {
				m := scrollFixture(t)
				before := m.detailViewport.YOffset()

				m, _ = update(t, m, tc.key)

				if got := m.detailViewport.YOffset(); got != before {
					t.Errorf("%s moved the detail viewport (YOffset %d -> %d) while unfocused", tc.name, before, got)
				}
			})
		}
	})

	t.Run("unfocused: ctrl+u/ctrl+d leave the detail viewport alone", func(t *testing.T) {
		for _, k := range []tea.KeyPressMsg{chord('d'), chord('u')} {
			t.Run(k.String(), func(t *testing.T) {
				// Scroll partway while focused, then release: at offset 0 an
				// inert ctrl+u would pass vacuously.
				m := focusOverview(t, scrollFixture(t))
				m, _ = update(t, m, chord('d'))
				m, _ = update(t, m, keyEsc)
				before := m.detailViewport.YOffset()
				if before == 0 || m.focus != focusList {
					t.Fatalf("fixture broken: offset=%d focus=%v", before, m.focus)
				}

				m, _ = update(t, m, k)

				if got := m.detailViewport.YOffset(); got != before {
					t.Errorf("%s moved the detail viewport from the list (YOffset %d -> %d)", k.String(), before, got)
				}
			})
		}
	})

	t.Run("focused: down keys scroll the viewport down and freeze the cursor", func(t *testing.T) {
		for _, tc := range downKeys {
			t.Run(tc.name, func(t *testing.T) {
				m := focusOverview(t, scrollFixture(t))
				before, ok := m.selected()
				if !ok {
					t.Fatalf("fixture broken: nothing selected")
				}
				beforeOffset := m.detailViewport.YOffset()

				m, cmd := update(t, m, tc.key)

				if got := m.detailViewport.YOffset(); got <= beforeOffset {
					t.Errorf("%s did not scroll the detail viewport down while focused (before=%d after=%d)", tc.name, beforeOffset, got)
				}
				if after, _ := m.selected(); after.Name != before.Name {
					t.Errorf("%s moved the session cursor from %q to %q while focused", tc.name, before.Name, after.Name)
				}
				if cmd != nil {
					t.Errorf("%s dispatched a Cmd while focused — a diff scroll must never schedule a preview", tc.name)
				}
			})
		}
	})

	t.Run("focused: up keys scroll the viewport up from a scrolled position", func(t *testing.T) {
		for _, tc := range upKeys {
			t.Run(tc.name, func(t *testing.T) {
				m := focusOverview(t, scrollFixture(t))

				// Scroll down first — see the function doc's vacuous-pass trap.
				m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
				scrolled := m.detailViewport.YOffset()
				if scrolled == 0 {
					t.Fatalf("fixture broken: end did not scroll the focused viewport down")
				}
				before, ok := m.selected()
				if !ok {
					t.Fatalf("fixture broken: nothing selected")
				}

				m, cmd := update(t, m, tc.key)

				if got := m.detailViewport.YOffset(); got >= scrolled {
					t.Errorf("%s did not scroll the detail viewport up while focused (before=%d after=%d)", tc.name, scrolled, got)
				}
				if after, _ := m.selected(); after.Name != before.Name {
					t.Errorf("%s moved the session cursor from %q to %q while focused", tc.name, before.Name, after.Name)
				}
				if cmd != nil {
					t.Errorf("%s dispatched a Cmd while focused — a diff scroll must never schedule a preview", tc.name)
				}
			})
		}
	})

	t.Run("focused: ctrl+u/ctrl+d scroll identically to unfocused", func(t *testing.T) {
		t.Run("ctrl+d", func(t *testing.T) {
			m := focusOverview(t, scrollFixture(t))
			before := m.detailViewport.YOffset()

			m, _ = update(t, m, chord('d'))

			if got := m.detailViewport.YOffset(); got <= before {
				t.Errorf("ctrl+d did not scroll the detail viewport down while focused (before=%d after=%d)", before, got)
			}
		})

		t.Run("ctrl+u", func(t *testing.T) {
			m := focusOverview(t, scrollFixture(t))
			m, _ = update(t, m, chord('d')) // scroll down first — see the function doc
			scrolled := m.detailViewport.YOffset()
			if scrolled == 0 {
				t.Fatalf("fixture broken: ctrl+d did not scroll the focused viewport down")
			}

			m, _ = update(t, m, chord('u'))

			if got := m.detailViewport.YOffset(); got >= scrolled {
				t.Errorf("ctrl+u did not scroll the detail viewport up while focused (before=%d after=%d)", scrolled, got)
			}
		})
	})

	t.Run("focused: home/end reach the top and bottom of the content", func(t *testing.T) {
		m := focusOverview(t, scrollFixture(t))

		m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
		if !m.detailViewport.AtBottom() {
			t.Errorf("end did not reach the bottom of the content while focused")
		}

		m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
		if !m.detailViewport.AtTop() {
			t.Errorf("home did not reach the top of the content while focused")
		}
	})
}

// TestDetailFocusClosesThePendingPreviewRace pins step 8's fix: a preview
// timer armed less than previewDelay before `d` opens the details must not
// dial once it fires — applyPreviewTimer refuses while the overlay is up —
// or the dial would take the host's one client slot for a pane nobody can
// see under the overlay.
func TestDetailFocusClosesThePendingPreviewRace(t *testing.T) {
	m := testModel(runningSess("alpha"), runningSess("beta"))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	// The cursor move onto a running row genuinely arms a preview — the live
	// timer `f` has to invalidate.
	m, armed := update(t, m, keyDown)
	if armed == nil {
		t.Fatalf("setup: the move onto a running row must arm a preview")
	}
	gen := m.previewGen
	name := m.sessions[m.listSelection()].Name

	m = focusOverview(t, m)

	m, cmd := update(t, m, previewTimerMsg{gen: gen, name: name})
	if cmd != nil {
		t.Errorf("the pending preview timer dispatched a dial while the detail pane is focused")
	}
	if m.pane != nil {
		t.Errorf("pane = %v, want nil — the timer must not have opened one over the focused detail view", m.pane)
	}
	if !m.detailOwnsKeyboard() {
		t.Errorf("detailOwnsKeyboard = false, want the dropped timer to have left focus alone")
	}
}

// TestEnterClaimedPreviewSurvivesALaterGenerationBump pins the claim half of
// the retired 300ms race. Enter on a row whose preview dial is already out
// claims that dial rather than opening a second client. A claim is the user's
// own connect from then on, so nothing that merely bumps previewGen may revoke
// it — and the old overview focus bumped it, which is how the old model lost
// the pane enter had asked for. The user wanders off before the arrival
// (ctrl+] d to the details, esc, ctrl+] esc to the list, d to the details
// again, esc):
// the claimed connection must still install, and must install BEHIND those
// later choices rather than yanking focus or view back to the agent.
func TestEnterClaimedPreviewSurvivesALaterGenerationBump(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "enter", key: keyEnter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("alpha"))
			late := newFakePane("claimed preview")
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				return late, nil
			}

			m, dial := update(t, m, previewTimerMsg{gen: m.previewGen, name: "alpha"})
			if dial == nil {
				t.Fatal("setup: preview must dispatch a dial")
			}
			arrival, ok := dial().(paneAttachedMsg)
			if !ok {
				t.Fatalf("preview returned %T, want paneAttachedMsg", dial())
			}

			m, cmd := update(t, m, tc.key)
			if cmd != nil || !m.attachInFlight.claimedPreview || !m.attachInFlight.focusOnArrival {
				t.Fatalf("%s did not claim the preview: cmd=%v inFlight=%+v", tc.name, cmd != nil, m.attachInFlight)
			}
			if m.focus != focusRight {
				t.Fatalf("focus = %v, want the claim to move focus right at the keypress", m.focus)
			}

			m = pressKey(t, m, keyRune('d'))
			m = pressKey(t, m, keyEscMsg())
			m = pressKey(t, m, keyEscMsg())
			m, _ = update(t, m, keyRune('d'))
			if !m.detailOwnsKeyboard() || !m.attachInFlight.claimedPreview {
				t.Fatalf("setup: opening the details: details=%v inFlight=%+v", m.detailOwnsKeyboard(), m.attachInFlight)
			}
			m, _ = update(t, m, keyEsc)

			m, _ = update(t, m, arrival)
			if late.closed != 0 || m.pane != late || m.paneName != "alpha" {
				t.Fatalf("claimed preview was dropped: closed=%d pane=%v name=%q", late.closed, m.pane == late, m.paneName)
			}
			if m.focus != focusList || m.shouldRenderPane() {
				t.Fatalf("arrival moved focus to %v (drawn=%v), want the user's later list kept",
					m.focus, m.shouldRenderPane())
			}
		})
	}
}

// fullHelpFocusRows counts how many times the footer names the Focus binding
// by BOTH its key and its live description ("enter open"). Since W3.T1 of
// T16.2 `?` opens the keys overlay rather than a full help, so the footer's
// one line is the surface that has to offer the key.
func fullHelpFocusRows(m Model) int {
	h := m.keys.Focus.Help()
	return strings.Count(xansi.Strip(m.renderHelp()), h.Key+" "+h.Desc)
}

// TestFocusKeyDiscoverableWheneverFocusingIsPossible is W3.T2 step 1's
// discoverability half: the footer carries the enter entry — exactly once —
// in every state normalBindingEnabled says it would do something, and not in
// the empty state.
//
// "no pane, detail drawn" is the load-bearing row, per the plan's own
// framing: it is what proves normalBindingEnabled (and therefore this
// discoverability) rests on the real `!shouldRenderPane() && hasActiveProject()`
// predicate rather than one that reduces to true — a reduced-to-true
// predicate would pass the empty-state row below too.
func TestFocusKeyDiscoverableWheneverFocusingIsPossible(t *testing.T) {
	t.Run("no pane, detail drawn (m.pane == nil)", func(t *testing.T) {
		m := testModel(runningSess("alpha"))
		if got := fullHelpFocusRows(m); got != 1 {
			t.Errorf("footer has %d enter entries, want exactly 1", got)
		}
	})

	t.Run("pane open", func(t *testing.T) {
		m := testModel(runningSess("alpha"))
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
		if got := fullHelpFocusRows(m); got != 1 {
			t.Errorf("footer has %d enter entries, want exactly 1", got)
		}
	})

	t.Run("cursor off the paned row", func(t *testing.T) {
		m := detailFocusFixture(t, "alpha", "beta")
		if m.shouldRenderPane() {
			t.Fatalf("setup: the detail pane should be the drawn surface")
		}
		if got := fullHelpFocusRows(m); got != 1 {
			t.Errorf("footer has %d enter entries, want exactly 1", got)
		}
	})

	t.Run("empty state: absent", func(t *testing.T) {
		m := testModel()
		m.activeProject = -1
		if got := fullHelpFocusRows(m); got != 0 {
			t.Errorf("footer has %d enter entries in the empty state, want 0 — nothing is focusable", got)
		}
	})
}

// TestDetailFocusFooterAdvertisesTheReleaseKeys is discoverability's other
// half: the footer carries the way out of the details overlay the entire
// time it is up, not just at the moment `d` lands.
func TestDetailFocusFooterAdvertisesTheReleaseKeys(t *testing.T) {
	m := focusOverview(t, testModel(runningSess("alpha")))

	help := xansi.Strip(m.renderHelp())
	for _, want := range []string{"esc back"} {
		if !strings.Contains(help, want) {
			t.Fatalf("focused footer = %q, want it to contain %q", help, want)
		}
	}

	// Scroll first (matrix tests upstream already prove this works) — the
	// release keys must not be a one-frame flash tied to the keypress that
	// entered focus.
	m, _ = update(t, m, keyRune('j'))
	help = xansi.Strip(m.renderHelp())
	for _, want := range []string{"esc back"} {
		if !strings.Contains(help, want) {
			t.Fatalf("focused footer after scrolling = %q, want it to still contain %q", help, want)
		}
	}
}
