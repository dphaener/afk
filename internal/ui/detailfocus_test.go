package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/dphaener/afk/internal/session"
)

// derivedMode is the one question every row in this file asks of a Model built
// through Update: who owns the keyboard right now. It goes through
// modeSnapshot rather than reading m.detailFocused directly, because the
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

// TestDetailFocusModeDerivation is the mode matrix for the detail pane's focus
// state, driven end to end through Update rather than at the modeState level
// (TestActiveInputModePrecedenceMatrix covers the pure table).
//
// Load-bearing rows, and why:
//
//   - "pane hidden by ctrl+t" and "cursor off the paned row" are the two states
//     where a session pane and the detail pane are both in play. `f` used to
//     reveal/focus the SESSION pane in both; it must now focus the DETAIL pane,
//     because ctrl+t is the only route to the Diff tab for a running session
//     (tab keys do nothing while a pane is on screen) and the feature would
//     otherwise be unreachable in its main path.
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
			name:       "detail pane drawn, f not pressed",
			setup:      func(t *testing.T) Model { return testModel(runningSess("alpha")) },
			wantMode:   inputNormal,
			wantDetail: false,
		},
		{
			name: "detail pane drawn, f pressed",
			setup: func(t *testing.T) Model {
				m := testModel(runningSess("alpha"))
				m, _ = update(t, m, keyRune('f'))
				return m
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
			name: "session pane rendered, f focuses the session pane as before",
			setup: func(t *testing.T) Model {
				m := testModel(runningSess("alpha"))
				m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
				m, _ = update(t, m, keyRune('f'))
				return m
			},
			wantMode:        inputPane,
			wantDetail:      false,
			wantPaneFocused: true,
		},
		{
			name: "pane hidden by ctrl+t: f focuses the DETAIL pane",
			setup: func(t *testing.T) Model {
				m := detailFocusFixture(t, "alpha", "alpha")
				m, _ = update(t, m, chord('t'))
				if !m.paneHidden {
					t.Fatalf("setup: pane should be hidden")
				}
				m, _ = update(t, m, keyRune('f'))
				if !m.paneHidden {
					t.Fatalf("f must not reveal the pane any more — the detail view is what is drawn")
				}
				return m
			},
			wantMode:   inputDetail,
			wantDetail: true,
		},
		{
			name: "cursor off the paned row: f focuses the DETAIL pane",
			setup: func(t *testing.T) Model {
				m := detailFocusFixture(t, "alpha", "beta")
				if m.shouldRenderPane() {
					t.Fatalf("setup: the detail pane should be the drawn surface")
				}
				m, _ = update(t, m, keyRune('f'))
				return m
			},
			wantMode:   inputDetail,
			wantDetail: true,
		},
		{
			name: "empty state: f focuses nothing",
			setup: func(t *testing.T) Model {
				m := testModel()
				m.activeProject = -1
				if m.hasActiveProject() {
					t.Fatalf("setup: the empty state must have no active project")
				}
				m, _ = update(t, m, keyRune('f'))
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
				m := testModel(runningSess("alpha"))
				m, _ = update(t, m, keyRune('f'))
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
				m := testModel(runningSess("alpha"))
				m, _ = update(t, m, keyRune('f'))
				m.formOpen = true
				return m
			},
			wantMode:   inputForm,
			wantDetail: true,
		},
		{
			name: "the project switcher over a focused detail pane still owns the keyboard",
			setup: func(t *testing.T) Model {
				m := testModel(runningSess("alpha"))
				m, _ = update(t, m, keyRune('f'))
				m.switcherOpen = true
				return m
			},
			wantMode:   inputSwitcher,
			wantDetail: true,
		},
		{
			name: "the palette over a focused detail pane still owns the keyboard",
			setup: func(t *testing.T) Model {
				m := testModel(runningSess("alpha"))
				m, _ = update(t, m, keyRune('f'))
				m.paletteOpen = true
				return m
			},
			wantMode:   inputPalette,
			wantDetail: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			if got := derivedMode(m); got != tc.wantMode {
				t.Errorf("mode = %v, want %v", got, tc.wantMode)
			}
			if m.detailFocused != tc.wantDetail {
				t.Errorf("detailFocused = %v, want %v", m.detailFocused, tc.wantDetail)
			}
			if m.paneFocused != tc.wantPaneFocused {
				t.Errorf("paneFocused = %v, want %v", m.paneFocused, tc.wantPaneFocused)
			}
		})
	}
}

// TestDetailFocusReleaseKeys pins both release chords. esc is the interesting
// one: filterClear also binds esc, but it lives in inputNormal and this rule
// lives in the new mode, so MODE PRECEDENCE — not rule ordering — is what
// decides, and an applied session filter must survive the release untouched.
func TestDetailFocusReleaseKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"esc releases", keyEscMsg()},
		{"ctrl+backslash releases", chord('\\')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("alpha"))
			m, _ = update(t, m, keyRune('f'))
			if !m.detailFocused {
				t.Fatalf("setup: f should have focused the detail pane")
			}
			m, _ = update(t, m, tc.key)
			if m.detailFocused {
				t.Fatalf("detailFocused = true, want the release to have cleared it")
			}
			if got := derivedMode(m); got != inputNormal {
				t.Fatalf("mode = %v, want %v after the release", got, inputNormal)
			}
		})
	}

	t.Run("esc releases focus without clearing an applied filter", func(t *testing.T) {
		m := testModel(runningSess("alpha"), runningSess("beta"))
		m = typeFilter(t, m, "b")
		m, _ = update(t, m, keyEnter)
		if m.list.FilterState() != list.FilterApplied {
			t.Fatalf("setup: filter state = %v, want applied", m.list.FilterState())
		}
		m, _ = update(t, m, keyRune('f'))
		if !m.detailFocused {
			t.Fatalf("setup: f should have focused the detail pane")
		}

		m, _ = update(t, m, keyEscMsg())
		if m.detailFocused {
			t.Fatalf("detailFocused = true, want esc to have released focus")
		}
		if m.list.FilterState() != list.FilterApplied {
			t.Fatalf("filter state = %v, want the applied filter kept — a second esc clears it",
				m.list.FilterState())
		}

		m, _ = update(t, m, keyEscMsg())
		if m.list.FilterState() != list.Unfiltered {
			t.Fatalf("filter state = %v, want the second esc to clear it", m.list.FilterState())
		}
	})
}

// TestDetailFocusRetainedKeySet enumerates the focused mode's whole key
// surface at the routing layer. Every row is load-bearing in the same way: the
// mode falls through to routeIgnored, so a key omitted from the rule table
// silently stops working and a key left in silently acts — and the acting half
// is the dangerous one. `enter`, `c`, `n`, `l`, `r`, `p`, `/`, `o` and `:` must
// be ignored so a scroll gesture can never open, clean or steal a session.
//
// The ten W2.T1 scroll keys are included below alongside the rest of the
// surface; TestDetailFocusedScrollKeyMatrix carries the "does it actually
// scroll" behavioral half this routing-only table cannot make.
func TestDetailFocusRetainedKeySet(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	conds := routeConds{paneOpen: true, listFiltered: true, detailShown: true}

	cases := []struct {
		name string
		key  tea.KeyPressMsg
		want keyDecision
	}{
		{"esc releases", tea.KeyPressMsg{Code: tea.KeyEsc}, keyDecision{routeAction, actionDetailUnfocus}},
		{"ctrl+backslash releases", chord('\\'), keyDecision{routeAction, actionDetailUnfocus}},
		{"q quits", press('q', "q"), keyDecision{routeAction, actionQuit}},
		{"ctrl+c quits", chord('c'), keyDecision{routeAction, actionQuit}},
		{"ctrl+g toggles the mouse", chord('g'), keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+t toggles agent/view", chord('t'), keyDecision{routeAction, actionPaneToggle}},
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
		{"colon is ignored", press(':', ":"), keyDecision{kind: routeIgnored}},
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

// TestDetailFocusSwallowsTheDestructiveKeys is the behavioural half of the
// table above: routeIgnored is only worth anything if Update really does
// nothing with it. These three are the keys whose accidental firing would cost
// the user something — a stolen session, a modal, a clean confirmation.
func TestDetailFocusSwallowsTheDestructiveKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"enter", keyEnter},
		{"n", keyRune('n')},
		{"c", keyRune('c')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("alpha"))
			m, _ = update(t, m, keyRune('f'))
			m, cmd := update(t, m, tc.key)

			if cmd != nil {
				t.Errorf("%s dispatched a Cmd while the detail pane is focused", tc.name)
			}
			if m.formOpen || m.modal != inputNormal || m.pane != nil {
				t.Errorf("%s acted: formOpen=%v modal=%v pane=%v", tc.name, m.formOpen, m.modal, m.pane != nil)
			}
			if !m.detailFocused {
				t.Errorf("%s dropped detail focus", tc.name)
			}
		})
	}
}

// TestDetailFocusDoesNotSurviveThePaneBecomingTheDrawnSurface is the
// reconciliation invariant, and it is enforced structurally because the
// failure is SILENT: the snapshot conjunction merely masks a stale
// detailFocused while the pane renders, so the flag resurrects on the next
// ctrl+t and starts scrolling the diff with the session cursor frozen and no
// `f` ever pressed.
//
// Both rows reach the unmasking through a different channel on purpose. The
// ctrl+t row is key-driven; the tick row is not, and a pre-dispatch check
// would pass the first and fail the second.
func TestDetailFocusDoesNotSurviveThePaneBecomingTheDrawnSurface(t *testing.T) {
	t.Run("ctrl+t round trip", func(t *testing.T) {
		m := detailFocusFixture(t, "alpha", "alpha")
		m, _ = update(t, m, chord('t'))
		m, _ = update(t, m, keyRune('f'))
		if !m.detailFocused {
			t.Fatalf("setup: f should have focused the detail pane behind the hidden pane")
		}

		m, _ = update(t, m, chord('t')) // reveal: the pane is the drawn surface again
		if m.detailFocused {
			t.Fatalf("detailFocused survived the reveal — it is masked, not cleared")
		}
		m, _ = update(t, m, chord('t')) // hide again: nothing stale may resurface
		if got := derivedMode(m); got != inputNormal {
			t.Fatalf("mode = %v after a ctrl+t round trip, want %v", got, inputNormal)
		}
	})

	t.Run("a tick that removes the paned row", func(t *testing.T) {
		m := detailFocusFixture(t, "alpha", "alpha")
		m, _ = update(t, m, chord('t'))
		m, _ = update(t, m, keyRune('f'))
		m, _ = update(t, m, chord('t')) // reveal
		if !m.shouldRenderPane() {
			t.Fatalf("setup: the pane should be the drawn surface after the reveal")
		}

		m, _ = update(t, m, sessionsLoadedMsg{
			gen:      m.projectGen,
			sessions: []Session{runningSess("beta")},
			at:       m.deps.Now(),
		})
		if m.shouldRenderPane() {
			t.Fatalf("setup: dropping the paned row should hand the region back to the detail pane")
		}
		if got := derivedMode(m); got != inputNormal {
			t.Fatalf("mode = %v after the paned row left discovery, want %v", got, inputNormal)
		}
	})
}

// TestEnterFocusesTheDrawnDetailWithAHiddenPane is the direct regression for
// the formerly contradictory Enter path. ctrl+t makes the detail pane the
// rendered surface, so Enter must share f's detail-focus result rather than
// revealing and focusing the hidden session pane.
func TestEnterFocusesTheDrawnDetailWithAHiddenPane(t *testing.T) {
	m := detailFocusFixture(t, "alpha", "alpha")
	m, _ = update(t, m, chord('t'))
	if !m.paneHidden {
		t.Fatalf("setup: the pane should be hidden")
	}

	m, cmd := update(t, m, keyEnter)
	if cmd != nil {
		t.Fatalf("enter dispatched %T, want no lifecycle work while focusing the drawn detail", cmd())
	}
	if !m.paneHidden {
		t.Fatalf("paneHidden = false, want Enter to leave the hidden pane hidden")
	}
	if m.paneFocused {
		t.Fatalf("paneFocused = true, want Enter to leave input with the detail surface")
	}
	if !m.detailFocused {
		t.Fatalf("detailFocused = false, want Enter to focus the drawn detail")
	}
	if got := derivedMode(m); got != inputDetail {
		t.Fatalf("mode = %v, want %v", got, inputDetail)
	}
}

type focusSurface string

const (
	focusSurfacePane   focusSurface = "session pane"
	focusSurfaceDetail focusSurface = "detail"
	focusSurfaceNone   focusSurface = "none"
)

// TestEnterAndFocusKeyCurrentViewMatrix is the normal-mode routing contract:
// Enter and f are aliases for focusing the CURRENTLY RENDERED main surface.
// It intentionally drives every row through Update, because the routing table
// alone cannot prove that an attach, restart, or preview arrival did not steal
// focus after the key was accepted.
//
// The table's lifecycle counters are deliberately run only after recording
// whether a command was returned. A focus key must schedule no command at all;
// running an unexpected command makes its attempted host connection/restart
// observable too, while leaving the model result from the original keypress
// intact for the focus assertions.
func TestEnterAndFocusKeyCurrentViewMatrix(t *testing.T) {
	const detailFocusStatus = "scrolling the detail view — the session list is frozen · esc/ctrl+\\ to return"
	const paneFocusStatus = "input goes to alpha · ctrl+\\ to return"
	const unchangedStatus = "status before the no-project no-op"

	type lifecycleCounts struct{ opens, spawns int }
	type previewArrival struct {
		msg  paneAttachedMsg
		pane *fakePane
	}
	type focusCase struct {
		name  string
		setup func(t *testing.T) (Model, *previewTimerMsg, *previewArrival)

		wantCommand                 bool
		wantStatus                  string
		wantPaneFocused, wantDetail bool
		wantPaneHidden              bool
		wantMode                    inputMode
		wantSurface                 focusSurface
		wantTab                     detailTab
		wantOpens, wantSpawns       int
	}

	noPane := func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
		return testModel(runningSess("alpha")), nil, nil
	}
	cases := []focusCase{
		{
			name: "agent pane drawn",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				m := detailFocusFixture(t, "alpha", "alpha")
				return m, nil, nil
			},
			wantStatus: paneFocusStatus, wantPaneFocused: true, wantMode: inputPane,
			wantSurface: focusSurfacePane, wantTab: tabOverview,
		},
		{
			name:       "Overview drawn with no pane",
			setup:      noPane,
			wantStatus: detailFocusStatus, wantDetail: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabOverview,
		},
		{
			name: "non-Overview detail tab drawn",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				m := testModel(runningSess("alpha"))
				m.detailTab = tabWaves
				return m, nil, nil
			},
			wantStatus: detailFocusStatus, wantDetail: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabWaves,
		},
		{
			name: "pane hidden by ctrl+t",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				m := detailFocusFixture(t, "alpha", "alpha")
				m, _ = update(t, m, chord('t'))
				return m, nil, nil
			},
			wantStatus: detailFocusStatus, wantDetail: true, wantPaneHidden: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabOverview,
		},
		{
			name: "cursor off the paned row",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				return detailFocusFixture(t, "alpha", "beta"), nil, nil
			},
			wantStatus: detailFocusStatus, wantDetail: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabOverview,
		},
		{
			name: "stopped session",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				return testModel(stoppedSess("alpha")), nil, nil
			},
			wantStatus: detailFocusStatus, wantDetail: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabOverview,
		},
		{
			name: "pending placeholder",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				return testModel(Session{SessionState: sampleState("alpha"), Pending: true}), nil, nil
			},
			wantStatus: detailFocusStatus, wantDetail: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabOverview,
		},
		{
			name: "worktree-fault row",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				return testModel(faultSess("alpha", session.KindWorktreeMissing, "worktree is gone")), nil, nil
			},
			wantStatus: detailFocusStatus, wantDetail: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabOverview,
		},
		{
			name: "pending preview timer not yet dispatched",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				m := testModel(runningSess("alpha"), runningSess("beta"))
				m, _ = update(t, m, keyDown)
				timer := previewTimerMsg{gen: m.previewGen, name: "beta"}
				return m, &timer, nil
			},
			wantStatus: detailFocusStatus, wantDetail: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabOverview,
		},
		{
			name: "preview dial already in flight",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				m := testModel(runningSess("alpha"), runningSess("beta"))
				previewPane := newFakePane("late preview")
				m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
					return previewPane, nil
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
				pane, ok := msg.pane.(*fakePane)
				if !ok {
					t.Fatalf("setup: preview pane = %T, want *fakePane", msg.pane)
				}
				return m, nil, &previewArrival{msg: msg, pane: pane}
			},
			wantStatus: detailFocusStatus, wantDetail: true, wantMode: inputDetail,
			wantSurface: focusSurfaceDetail, wantTab: tabOverview,
		},
		{
			name: "no active project with empty status",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				m := testModel()
				m.activeProject = -1
				return m, nil, nil
			},
			wantMode: inputNormal, wantSurface: focusSurfaceNone,
			wantTab: tabOverview,
		},
		{
			name: "no active project with preexisting status",
			setup: func(t *testing.T) (Model, *previewTimerMsg, *previewArrival) {
				m := testModel()
				m.activeProject = -1
				m.status = unchangedStatus
				return m, nil, nil
			},
			wantStatus: unchangedStatus, wantMode: inputNormal, wantSurface: focusSurfaceNone,
			wantTab: tabOverview,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []struct {
				name string
				msg  tea.KeyMsg
			}{
				{name: "enter", msg: keyEnter},
				{name: "f", msg: keyRune('f')},
			} {
				t.Run(key.name, func(t *testing.T) {
					m, timer, arrival := tc.setup(t)
					var counts lifecycleCounts
					m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
						counts.opens++
						return newFakePane("unexpected lifecycle open"), nil
					}
					m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
						counts.spawns++
						return session.HostProcess{}, nil
					}

					got, cmd := update(t, m, key.msg)
					if (cmd != nil) != tc.wantCommand {
						t.Errorf("command returned = %v, want %v", cmd != nil, tc.wantCommand)
					}
					if cmd != nil {
						_ = cmd()
					}
					if got.status != tc.wantStatus {
						t.Errorf("status = %q, want %q", got.status, tc.wantStatus)
					}
					if got.paneFocused != tc.wantPaneFocused {
						t.Errorf("paneFocused = %v, want %v", got.paneFocused, tc.wantPaneFocused)
					}
					if got.detailFocused != tc.wantDetail {
						t.Errorf("detailFocused = %v, want %v", got.detailFocused, tc.wantDetail)
					}
					if got.paneHidden != tc.wantPaneHidden {
						t.Errorf("paneHidden = %v, want %v", got.paneHidden, tc.wantPaneHidden)
					}
					if mode := derivedMode(got); mode != tc.wantMode {
						t.Errorf("mode = %v, want %v", mode, tc.wantMode)
					}
					if tab := got.activeDetailTab(); tab != tc.wantTab {
						t.Errorf("active detail tab = %v, want %v", tab, tc.wantTab)
					}
					switch tc.wantSurface {
					case focusSurfacePane:
						if !got.shouldRenderPane() {
							t.Error("visible surface is not the expected session pane")
						}
					case focusSurfaceDetail:
						if !got.detailDrawn() {
							t.Error("visible surface is not the expected detail pane")
						}
					case focusSurfaceNone:
						if got.shouldRenderPane() || got.detailDrawn() {
							t.Errorf("visible surface is pane=%v detail=%v, want no right-hand surface", got.shouldRenderPane(), got.detailDrawn())
						}
					}
					if counts.opens != tc.wantOpens || counts.spawns != tc.wantSpawns {
						t.Errorf("lifecycle calls = open:%d spawn:%d, want open:%d spawn:%d", counts.opens, counts.spawns, tc.wantOpens, tc.wantSpawns)
					}

					if timer != nil {
						got, cmd = update(t, got, *timer)
						if cmd != nil {
							t.Error("a timer pending before detail focus dispatched lifecycle work")
						}
						if !got.detailFocused || !got.detailDrawn() {
							t.Error("a timer pending before detail focus revoked the focused detail surface")
						}
					}
					if arrival != nil {
						got, cmd = update(t, got, arrival.msg)
						if cmd != nil {
							t.Error("a late preview arrival dispatched work")
						}
						if arrival.pane.closed != 1 {
							t.Errorf("late preview connection closed %d times, want 1", arrival.pane.closed)
						}
						if !got.detailFocused || !got.detailDrawn() || got.shouldRenderPane() {
							t.Error("late preview replaced the focused detail surface instead of being dropped")
						}
					}
				})
			}
		})
	}
}

// TestEnterFocusesOverviewWithExactlyOneAccentOwner drives the default
// Overview through Enter (not f) and reuses the established style contract:
// detail focus owns the accent and the sidebar does not. This keeps the visual
// input-owner signal tied to the same alias contract as the routing matrix.
func TestEnterFocusesOverviewWithExactlyOneAccentOwner(t *testing.T) {
	accent := theme.paneBoxFocused.GetBorderTopForeground()
	m := testModel(runningSess("alpha"))
	m, cmd := update(t, m, keyEnter)
	if cmd != nil {
		t.Fatalf("Enter dispatched %T, want no lifecycle work while focusing Overview", cmd())
	}
	if !m.detailFocused || m.activeDetailTab() != tabOverview {
		t.Fatalf("Enter did not focus Overview: detailFocused=%v tab=%v", m.detailFocused, m.activeDetailTab())
	}
	if m.detailBoxStyle().GetBorderTopForeground() != accent {
		t.Error("Enter-focused Overview must carry the accent border")
	}
	if m.listBoxStyle().GetBorderTopForeground() == accent {
		t.Error("Enter-focused Overview must release the sidebar accent")
	}
}

// TestFocusKeyIsAdvertisedWheneverFocusingIsPossible pins the discoverability
// gate normalBindingEnabled owns. A runnable selected session makes both the
// current view focusable and an agent surface available, even before its pane
// exists. The empty-state row is the load-bearing one: it proves neither
// binding is enabled over renderEmptyState, where there is neither a detail box
// nor a selected agent to show.
func TestFocusKeyIsAdvertisedWheneverFocusingIsPossible(t *testing.T) {
	t.Run("no pane, detail drawn", func(t *testing.T) {
		m := testModel(runningSess("alpha"))
		if !m.normalBindingEnabled(m.keys.Focus) {
			t.Fatalf("%s must be advertised with the detail pane drawn", m.keys.Focus.Help().Key)
		}
		if !m.normalBindingEnabled(m.keys.Toggle) {
			t.Fatalf("%s %s must be advertised for a selected runnable session before its pane exists",
				m.keys.Toggle.Help().Key, m.keys.Toggle.Help().Desc)
		}
	})

	t.Run("pane open", func(t *testing.T) {
		m := testModel(runningSess("alpha"))
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
		if !m.normalBindingEnabled(m.keys.Focus) {
			t.Fatalf("%s must be advertised with a pane open", m.keys.Focus.Help().Key)
		}
		if !m.normalBindingEnabled(m.keys.Toggle) {
			t.Fatalf("%s must be advertised with a pane open", m.keys.Toggle.Help().Key)
		}
	})

	t.Run("empty state advertises neither", func(t *testing.T) {
		m := testModel()
		m.activeProject = -1
		if m.normalBindingEnabled(m.keys.Focus) {
			t.Fatalf("%s must not be advertised in the empty state — there is no detail box to focus", m.keys.Focus.Help().Key)
		}
		if m.normalBindingEnabled(m.keys.Toggle) {
			t.Fatalf("%s must not be advertised in the empty state", m.keys.Toggle.Help().Key)
		}
	})
}

// TestFocusHelpTextFitsTheEightyColumnBudget pins step 5a's measured budget.
// `enter/f focus view` rides the same oversubscribed footer line as
// `ctrl+t agent/view`. Their compact wording is what keeps both current-view
// focus and agent-surface discovery legible before the ellipsis costs a cell.
func TestFocusHelpTextFitsTheEightyColumnBudget(t *testing.T) {
	desc := defaultKeyMap().Focus.Help().Desc
	if len(desc) > 11 {
		t.Fatalf("enter/f's help description %q is %d characters, want no more than 11", desc, len(desc))
	}
	if desc == "focus pane" {
		t.Fatalf("enter/f no longer focuses only the pane; its help text must say so")
	}
}

// TestDetailFocusedScrollKeyMatrix is W2.T1's focused/unfocused key matrix:
// every key a user reaches for — up/down/j/k, pgup/pgdn, home/end and
// ctrl+u/ctrl+d — scrolls the detail viewport while focused and leaves the
// session cursor untouched, and does exactly what it did before this task
// while unfocused.
//
// The UNFOCUSED rows are the regression guard, and they are the load-bearing
// half of this table: a routing mistake that leaks a detail-focused rule
// into inputNormal (or shadows an inputNormal rule with a same-named
// detail one) is obvious while focused — the diff visibly fails to scroll —
// but the identical mistake leaking the OTHER way would silently break
// session-list navigation for every user, focused or not, and nothing else
// in this file would catch it.
//
// ctrl+u/ctrl+d get their own group rather than folding into the up/down
// split: they are the PRE-EXISTING detail-scroll chords (mode.go's
// detailHalfPageUp/detailHalfPageDown) and already move the viewport whether
// or not the pane is focused, so both of their rows assert the SAME
// direction rather than an inverted one.
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

	t.Run("unfocused: ctrl+u/ctrl+d still scroll the detail viewport", func(t *testing.T) {
		t.Run("ctrl+d", func(t *testing.T) {
			m := scrollFixture(t)
			before := m.detailViewport.YOffset()

			m, _ = update(t, m, chord('d'))

			if got := m.detailViewport.YOffset(); got <= before {
				t.Fatalf("ctrl+d did not scroll the unfocused detail viewport down (before=%d after=%d)", before, got)
			}
		})

		t.Run("ctrl+u", func(t *testing.T) {
			m := scrollFixture(t)
			// ctrl+u is an UP key: at offset 0 it is a no-op whether or not
			// routing works, so scroll down first (see the function doc).
			m, _ = update(t, m, chord('d'))
			scrolled := m.detailViewport.YOffset()
			if scrolled == 0 {
				t.Fatalf("fixture broken: ctrl+d did not scroll the unfocused viewport down")
			}

			m, _ = update(t, m, chord('u'))

			if got := m.detailViewport.YOffset(); got >= scrolled {
				t.Errorf("ctrl+u did not scroll the unfocused detail viewport up (before=%d after=%d)", scrolled, got)
			}
		})
	})

	t.Run("focused: down keys scroll the viewport down and freeze the cursor", func(t *testing.T) {
		for _, tc := range downKeys {
			t.Run(tc.name, func(t *testing.T) {
				m := scrollFixture(t)
				m, _ = update(t, m, keyRune('f'))
				if !m.detailFocused {
					t.Fatalf("setup: f should have focused the detail pane")
				}
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
				m := scrollFixture(t)
				m, _ = update(t, m, keyRune('f'))

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
			m := scrollFixture(t)
			m, _ = update(t, m, keyRune('f'))
			before := m.detailViewport.YOffset()

			m, _ = update(t, m, chord('d'))

			if got := m.detailViewport.YOffset(); got <= before {
				t.Errorf("ctrl+d did not scroll the detail viewport down while focused (before=%d after=%d)", before, got)
			}
		})

		t.Run("ctrl+u", func(t *testing.T) {
			m := scrollFixture(t)
			m, _ = update(t, m, keyRune('f'))
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
		m := scrollFixture(t)
		m, _ = update(t, m, keyRune('f'))

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
// timer armed less than previewDelay before `f` focuses the detail pane is
// still live when it arrives — schedulePreview only bumps previewGen on a
// cursor move, and focusing the detail pane is not one — so
// applyPreviewTimer's own m.detailFocused check is the ONLY thing standing
// between the timer and applyPaneAttached replacing the diff with a pane
// nobody asked to open.
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

	m, _ = update(t, m, keyRune('f'))
	if !m.detailFocused {
		t.Fatalf("setup: f should have focused the detail pane")
	}

	m, cmd := update(t, m, previewTimerMsg{gen: gen, name: name})
	if cmd != nil {
		t.Errorf("the pending preview timer dispatched a dial while the detail pane is focused")
	}
	if m.pane != nil {
		t.Errorf("pane = %v, want nil — the timer must not have opened one over the focused detail view", m.pane)
	}
	if !m.detailFocused {
		t.Errorf("detailFocused = false, want the dropped timer to have left focus alone")
	}
}

// TestDetailFocusRevokesAClaimedPreview pins the later ordering in the same
// race: Ctrl-T may claim an already-dispatched preview, but a subsequent
// explicit detail focus wins. The generation bump must keep that preview stale
// even if detail focus is released before the connection arrives.
func TestDetailFocusRevokesAClaimedPreview(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "enter", key: keyEnter},
		{name: "f", key: keyRune('f')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("alpha"))
			late := newFakePane("late claimed preview")
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

			m, cmd := update(t, m, chord('t'))
			if cmd != nil || !m.attachInFlight.claimedPreview {
				t.Fatalf("Ctrl-T did not claim the preview: cmd=%v inFlight=%+v", cmd != nil, m.attachInFlight)
			}
			m, cmd = update(t, m, tc.key)
			if cmd != nil || !m.detailFocused || m.attachInFlight.claimedPreview {
				t.Fatalf("focus did not revoke the claim: cmd=%v detail=%v inFlight=%+v", cmd != nil, m.detailFocused, m.attachInFlight)
			}

			// Releasing focus before delivery must not resurrect the old claim.
			m, _ = update(t, m, keyEsc)
			m, cmd = update(t, m, arrival)
			if cmd != nil {
				t.Fatal("late claimed preview dispatched work")
			}
			if late.closed != 1 || m.pane != nil || m.shouldRenderPane() {
				t.Fatalf("late preview survived explicit detail focus: closed=%d pane=%v drawn=%v", late.closed, m.pane, m.shouldRenderPane())
			}
		})
	}
}

// fullHelpFocusRows counts how many rows of `?`'s full help name the Focus
// binding by BOTH its key and its live description — the same two-field
// match TestPaletteIsAdvertisedAndRendered uses for the palette key, because
// help.Model's full view is a padded column layout (key and description
// separated by run-length padding, not one space).
func fullHelpFocusRows(m Model) int {
	m.helpShowAll = true
	full := xansi.Strip(m.renderHelp())
	var n int
	for _, line := range strings.Split(full, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == m.keys.Focus.Help().Key && strings.Contains(line, m.keys.Focus.Help().Desc) {
			n++
		}
	}
	return n
}

// paletteFocusRows counts how many `:` palette rows the Focus binding
// contributes, through the same derivation the live palette renders from
// (paletteEntries, model.go's paletteListSize/openPalette call sites) rather
// than re-deriving the gate here.
func paletteFocusRows(m Model) int {
	reg := m.modes()
	entries := paletteEntries(reg.spec(inputNormal), m.routeConditions(), m.normalBindingEnabled)
	var n int
	for _, e := range entries {
		if e.keyLabel == m.keys.Focus.Help().Key && e.desc == m.keys.Focus.Help().Desc {
			n++
		}
	}
	return n
}

// TestFocusKeyDiscoverableWheneverFocusingIsPossible is W3.T2 step 1's
// discoverability half: `?`'s full help and the `:` palette both carry the
// unfocused `f` entry — exactly one row apiece — in every state
// normalBindingEnabled says focusing would do something, and neither carries
// it in the empty state.
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
			t.Errorf("full help has %d `f` rows, want exactly 1", got)
		}
		if got := paletteFocusRows(m); got != 1 {
			t.Errorf("palette has %d `f` rows, want exactly 1", got)
		}
	})

	t.Run("pane open", func(t *testing.T) {
		m := testModel(runningSess("alpha"))
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
		if got := fullHelpFocusRows(m); got != 1 {
			t.Errorf("full help has %d `f` rows, want exactly 1", got)
		}
		if got := paletteFocusRows(m); got != 1 {
			t.Errorf("palette has %d `f` rows, want exactly 1", got)
		}
	})

	t.Run("cursor off the paned row", func(t *testing.T) {
		m := detailFocusFixture(t, "alpha", "beta")
		if m.shouldRenderPane() {
			t.Fatalf("setup: the detail pane should be the drawn surface")
		}
		if got := fullHelpFocusRows(m); got != 1 {
			t.Errorf("full help has %d `f` rows, want exactly 1", got)
		}
		if got := paletteFocusRows(m); got != 1 {
			t.Errorf("palette has %d `f` rows, want exactly 1", got)
		}
	})

	t.Run("empty state: absent from both", func(t *testing.T) {
		m := testModel()
		m.activeProject = -1
		if got := fullHelpFocusRows(m); got != 0 {
			t.Errorf("full help has %d `f` rows in the empty state, want 0 — nothing is focusable", got)
		}
		if got := paletteFocusRows(m); got != 0 {
			t.Errorf("palette has %d `f` rows in the empty state, want 0 — nothing is focusable", got)
		}
	})
}

// TestDetailFocusFooterAdvertisesTheReleaseKeys is discoverability's other
// half: `?` and `:` are both gated to inputNormal (dispatchKey, mode.go's
// routeIgnored fall-through for inputDetail), so nothing but the footer's
// default renderHelp branch can carry the release gesture while the detail
// pane owns the keyboard. It has to be live the entire time focus holds, not
// just at the moment `f` lands.
func TestDetailFocusFooterAdvertisesTheReleaseKeys(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m, _ = update(t, m, keyRune('f'))
	if !m.detailFocused {
		t.Fatalf("setup: f should have focused the detail pane")
	}

	help := xansi.Strip(m.renderHelp())
	for _, want := range []string{"esc/ctrl+\\", "back to list"} {
		if !strings.Contains(help, want) {
			t.Fatalf("focused footer = %q, want it to contain %q", help, want)
		}
	}

	// Scroll first (matrix tests upstream already prove this works) — the
	// release keys must not be a one-frame flash tied to the keypress that
	// entered focus.
	m, _ = update(t, m, keyRune('j'))
	help = xansi.Strip(m.renderHelp())
	for _, want := range []string{"esc/ctrl+\\", "back to list"} {
		if !strings.Contains(help, want) {
			t.Fatalf("focused footer after scrolling = %q, want it to still contain %q", help, want)
		}
	}
}

// TestFocusDetailToastNamesTheSameReleaseKeysAsTheHint pins the drift the
// review caught: focusDetail carried its own "esc or ctrl+\" literal while
// detailFocusedHint read mode.go's binding, so renaming the chord would have
// left the toast naming a key that no longer released. Both now resolve the
// same inputDetail binding through findBinding.
//
// The empty-expectation guard is the load-bearing half. A findBinding lookup
// that stops matching returns the zero Binding, whose Help().Key is "" — the
// toast then degrades to a stray " to return" and the equality assertion below
// would still pass against it, since "" is contained in every string.
func TestFocusDetailToastNamesTheSameReleaseKeysAsTheHint(t *testing.T) {
	m := testModel(runningSess("alpha"))
	reg := m.modes()
	want := findBinding(reg.spec(inputDetail).keys.bindings, []string{"esc", "ctrl+\\"}).Help().Key
	if want == "" {
		t.Fatalf("inputDetail declares no esc/ctrl+\\ release binding, but the toast and the hint both read one")
	}

	m, _ = update(t, m, keyRune('f'))
	if !m.detailFocused {
		t.Fatalf("setup: f should have focused the detail pane")
	}
	if !strings.Contains(m.status, want) {
		t.Fatalf("focus toast = %q, want it to name the release keys %q", m.status, want)
	}
	if hint := m.detailFocusedHint(); !strings.Contains(hint, want) {
		t.Fatalf("detailFocusedHint() = %q, want it to name the release keys %q", hint, want)
	}
}

// TestReleaseDetailFocusToastUsesCombinedFocusBinding keeps the return trip
// sourced from the same normal-mode binding as every other focus hint. The
// synthetic key label makes a regression to a hard-coded physical `f` visible.
func TestReleaseDetailFocusToastUsesCombinedFocusBinding(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m.detailFocused = true
	m.keys.Focus = key.NewBinding(
		key.WithKeys("enter", "x"),
		key.WithHelp("enter/x", "focus view"),
	)

	result, cmd := m.releaseDetailFocus()
	if cmd != nil {
		t.Fatal("releasing detail focus must not dispatch work")
	}
	got := result.(Model)
	if got.detailFocused {
		t.Fatal("detail focus remained set after release")
	}
	want := "input goes to the session list · enter/x to scroll the detail view again"
	if got.status != want {
		t.Fatalf("release status = %q, want binding-derived %q", got.status, want)
	}
}
