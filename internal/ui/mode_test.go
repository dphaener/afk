package ui

import (
	"testing"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// TestActiveInputModePrecedenceMatrix pins the declared precedence table
// (mode.go's modePrecedence) against every state combination that matters.
//
// Load-bearing rows:
//
//   - "form open and pane focused" is THE corner this mechanism exists to
//     close (model.go:1613-1620 documents it as uncovered): the defined answer
//     is that the form wins, because it is what is on screen. If this row
//     flips, W4.T2 will wire keystrokes into an invisible child session.
//   - "spawn error over open switcher" pins modal > switcher, which is
//     reachable (spawnFinishedMsg is async) and documented at model.go:872-895.
//     If it flips, a spawn failure's only readable record loses the keyboard.
//   - "switcher over focused pane" pins the documented focusOnArrival case:
//     keys must go to the overlay the user is looking at, not the pane behind
//     it.
//   - "switcher over form" is unreachable in practice but declared so the
//     frame (views.go renders switcher first) and the keyboard can never
//     disagree; enshrining the opposite order would split them.
//   - The four (focus, view, pane) rows under "the two axes" are the whole
//     focus model as data. "list focus with a pane in agent view" is the one
//     that encodes the user's rule — with the list focused nothing on the
//     right owns the keyboard, however live the pane is — and "right focus in
//     agent view without a pane" pins that the keyboard follows the DRAWN
//     surface (the overview, since there is nothing else to draw) rather
//     than falling to normal mode, where typed keys would hit list bindings.
//   - "right focus, overview view, pane present" pins that the view axis
//     outranks the pane's existence: an installed pane behind the overview
//     does not claim the keyboard. This is the row that replaces the old
//     "focused flag hidden behind the summary" corner.
//   - The inputDetail rows pin the layer's POSITION, directly above normal
//     mode and below everything else. modePrecedence is hand-ordered and a
//     row slipped above a modal is shadowed silently, with no error — move
//     the entry up and these rows are what turns it red. The "right focus in
//     overview" row is also what decides esc: filterClear binds esc in
//     inputNormal and the release binds it here, so an applied filter
//     survives the release because this mode outranks normal, not because of
//     any rule ordering.
//   - The inbox rows pin the overlay's place: below every modal, the palette
//     and the switcher, above everything else. "inbox over focused pane" is
//     the one that matters in use: an attach can land focused under the open
//     overlay, and the keys being typed are an answer, not input for a
//     session the box is covering.
//
// The single-state rows exist so a precedence-table edit that drops a layer
// entirely fails loudly rather than shadowing.
func TestActiveInputModePrecedenceMatrix(t *testing.T) {
	cases := []struct {
		name  string
		state modeState
		want  inputMode
	}{
		{
			name:  "zero state is normal",
			state: modeState{},
			want:  inputNormal,
		},

		// The two axes, crossed with whether a pane is drawable for the
		// selected row. Focus decides whether the right side owns the
		// keyboard at all; view and pane decide which right surface does.
		{
			name:  "list focus, agent view, no pane",
			state: modeState{focus: focusList, view: viewAgent},
			want:  inputNormal,
		},
		{
			name:  "list focus with a pane in agent view: the pane is display only",
			state: modeState{focus: focusList, view: viewAgent, pane: true},
			want:  inputNormal,
		},
		{
			name:  "list focus, overview view, no pane",
			state: modeState{focus: focusList, view: viewOverview},
			want:  inputNormal,
		},
		{
			name:  "list focus, overview view, pane behind it",
			state: modeState{focus: focusList, view: viewOverview, pane: true},
			want:  inputNormal,
		},
		{
			name:  "right focus, agent view, pane present: the pane owns the keyboard",
			state: modeState{focus: focusRight, view: viewAgent, pane: true},
			want:  inputPane,
		},
		{
			name:  "right focus in agent view without a pane: the drawn overview owns it",
			state: modeState{focus: focusRight, view: viewAgent},
			want:  inputDetail,
		},
		{
			name:  "right focus, overview view, no pane",
			state: modeState{focus: focusRight, view: viewOverview},
			want:  inputDetail,
		},
		{
			name:  "right focus, overview view, pane present: the view outranks the pane",
			state: modeState{focus: focusRight, view: viewOverview, pane: true},
			want:  inputDetail,
		},

		{
			name:  "filter prompt alone",
			state: modeState{listFiltering: true},
			want:  inputFilter,
		},
		{
			// Reachable: applyPaneAttached moves the focus asynchronously (a
			// honored click arrives already focused), so a focus can land while
			// the prompt is open. The prompt is what is on screen and what the
			// user is typing into, so it keeps the keyboard — the same rule the
			// form/pane row below states.
			name:  "filter prompt over focused pane",
			state: modeState{listFiltering: true, focus: focusRight, pane: true},
			want:  inputFilter,
		},
		{
			// Unreachable in practice (every key while filtering is query text,
			// so `n` cannot open the form), declared so the order is a decision
			// rather than an accident of table position.
			name:  "form over filter prompt",
			state: modeState{formOpen: true, listFiltering: true},
			want:  inputForm,
		},
		{
			name:  "switcher over filter prompt",
			state: modeState{switcherOpen: true, listFiltering: true},
			want:  inputSwitcher,
		},
		{
			name:  "confirm clean alone",
			state: modeState{modal: inputConfirmClean},
			want:  inputConfirmClean,
		},
		{
			name:  "spawn error alone",
			state: modeState{modal: inputSpawnError},
			want:  inputSpawnError,
		},
		{
			name:  "confirm restart alone",
			state: modeState{modal: inputConfirmRestart},
			want:  inputConfirmRestart,
		},
		{
			name:  "edit label alone",
			state: modeState{modal: inputEditLabel},
			want:  inputEditLabel,
		},
		{
			name:  "provider picker alone",
			state: modeState{modal: inputPickProvider},
			want:  inputPickProvider,
		},
		{
			name:  "switcher alone",
			state: modeState{switcherOpen: true},
			want:  inputSwitcher,
		},
		{
			name:  "form alone",
			state: modeState{formOpen: true},
			want:  inputForm,
		},
		{
			name:  "inbox alone",
			state: modeState{inboxOpen: true},
			want:  inputInbox,
		},
		{
			name:  "inbox over focused pane: the overlay is what is on screen",
			state: modeState{inboxOpen: true, focus: focusRight, view: viewAgent, pane: true},
			want:  inputInbox,
		},
		{
			// Reachable: `i` is a rule in detail mode, and the focus stays on
			// the right while the overlay is up.
			name:  "inbox over focused detail",
			state: modeState{inboxOpen: true, focus: focusRight, view: viewOverview},
			want:  inputInbox,
		},
		{
			name:  "inbox over form",
			state: modeState{inboxOpen: true, formOpen: true},
			want:  inputInbox,
		},
		{
			name:  "inbox over filter prompt",
			state: modeState{inboxOpen: true, listFiltering: true},
			want:  inputInbox,
		},
		{
			name:  "inbox over resizing",
			state: modeState{inboxOpen: true, resizing: true},
			want:  inputInbox,
		},
		{
			name:  "switcher over inbox",
			state: modeState{switcherOpen: true, inboxOpen: true},
			want:  inputSwitcher,
		},
		{
			name:  "palette over inbox",
			state: modeState{paletteOpen: true, inboxOpen: true},
			want:  inputPalette,
		},
		{
			// Reachable: a background create can fail while the overlay is up,
			// and its report is the only place that output can be read.
			name:  "spawn error over inbox: modal wins",
			state: modeState{modal: inputSpawnError, inboxOpen: true},
			want:  inputSpawnError,
		},
		{
			name:  "edit label over inbox: modal wins",
			state: modeState{modal: inputEditLabel, inboxOpen: true},
			want:  inputEditLabel,
		},
		{
			name:  "filter prompt over focused detail",
			state: modeState{listFiltering: true, focus: focusRight, view: viewOverview},
			want:  inputFilter,
		},
		{
			name:  "form over focused detail",
			state: modeState{formOpen: true, focus: focusRight, view: viewOverview},
			want:  inputForm,
		},
		{
			name:  "switcher over focused detail",
			state: modeState{switcherOpen: true, focus: focusRight, view: viewOverview},
			want:  inputSwitcher,
		},
		{
			name:  "palette over focused detail",
			state: modeState{paletteOpen: true, focus: focusRight, view: viewOverview},
			want:  inputPalette,
		},
		{
			name:  "confirm clean over focused detail: modal wins",
			state: modeState{modal: inputConfirmClean, focus: focusRight, view: viewOverview},
			want:  inputConfirmClean,
		},
		{
			name:  "resizing alone",
			state: modeState{resizing: true},
			want:  inputResize,
		},
		{
			// Reachable: resize mode is entered from a focused detail pane and
			// leaves the focus on the right, so the resize must outrank it.
			name:  "resizing over focused detail",
			state: modeState{resizing: true, focus: focusRight, view: viewOverview},
			want:  inputResize,
		},
		{
			// Reachable only through a focus-on-arrival attach landing
			// mid-resize: the explicit gesture keeps the keyboard.
			name:  "resizing over focused pane",
			state: modeState{resizing: true, focus: focusRight, pane: true},
			want:  inputResize,
		},
		{
			name:  "filter prompt over resizing",
			state: modeState{listFiltering: true, resizing: true},
			want:  inputFilter,
		},
		{
			name:  "form over resizing",
			state: modeState{formOpen: true, resizing: true},
			want:  inputForm,
		},
		{
			name:  "switcher over resizing",
			state: modeState{switcherOpen: true, resizing: true},
			want:  inputSwitcher,
		},
		{
			name:  "palette over resizing",
			state: modeState{paletteOpen: true, resizing: true},
			want:  inputPalette,
		},
		{
			name:  "spawn error over resizing: modal wins",
			state: modeState{modal: inputSpawnError, resizing: true},
			want:  inputSpawnError,
		},
		{
			name:  "form open and pane focused: form wins (the closed corner)",
			state: modeState{formOpen: true, focus: focusRight, pane: true},
			want:  inputForm,
		},
		{
			name:  "spawn error over open switcher: modal wins",
			state: modeState{modal: inputSpawnError, switcherOpen: true},
			want:  inputSpawnError,
		},
		{
			name:  "spawn error over open form: modal wins",
			state: modeState{modal: inputSpawnError, formOpen: true},
			want:  inputSpawnError,
		},
		{
			name:  "confirm clean over focused pane: modal wins",
			state: modeState{modal: inputConfirmClean, focus: focusRight, pane: true},
			want:  inputConfirmClean,
		},
		{
			name:  "switcher over focused pane: switcher wins",
			state: modeState{switcherOpen: true, focus: focusRight, pane: true},
			want:  inputSwitcher,
		},
		{
			name:  "switcher over form: switcher wins",
			state: modeState{switcherOpen: true, formOpen: true},
			want:  inputSwitcher,
		},
		{
			name: "everything at once: modal wins",
			state: modeState{
				modal:        inputEditLabel,
				switcherOpen: true,
				formOpen:     true,
				focus:        focusRight,
				view:         viewOverview,
				pane:         true,
				resizing:     true,
			},
			want: inputEditLabel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := activeInputMode(tc.state); got != tc.want {
				t.Fatalf("activeInputMode(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestActiveInputModeModalAlwaysWins covers the 640 combinations the named
// matrix above samples: every modal, crossed with every setting of the five
// composing flags and the two focus axes (5 × 2^7 = 640), must derive the
// modal. This is the exhaustive form of the "modal outranks everything"
// invariant — a precedence-table edit that slips any layer above the modals
// turns some cell here red. The modal set is derived from isModal rather than
// listed, so a sixth modal added later is covered the moment its constant
// exists; the count check below makes adding one a deliberate edit rather
// than a silent widening.
func TestActiveInputModeModalAlwaysWins(t *testing.T) {
	var modals []inputMode
	for mode := inputMode(0); mode < inputModeCount; mode++ {
		if mode.isModal() {
			modals = append(modals, mode)
		}
	}
	if len(modals) != 5 {
		t.Fatalf("isModal admits %d modes, want the 5 footer modals", len(modals))
	}
	bools := []bool{false, true}
	focuses := []paneFocus{focusList, focusRight}
	views := []rightView{viewAgent, viewOverview}
	for _, modal := range modals {
		for _, sw := range bools {
			for _, form := range bools {
				for _, filtering := range bools {
					for _, focus := range focuses {
						for _, view := range views {
							for _, pane := range bools {
								for _, resizing := range bools {
									s := modeState{
										modal:         modal,
										switcherOpen:  sw,
										formOpen:      form,
										listFiltering: filtering,
										focus:         focus,
										view:          view,
										pane:          pane,
										resizing:      resizing,
									}
									if got := activeInputMode(s); got != modal {
										t.Errorf("activeInputMode(%+v) = %v, want %v", s, got, modal)
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

// TestModeSnapshot pins the Model→modeState bridge: each overlay-activity
// field maps to its snapshot field, focus and view copy across verbatim, and
// pane is "a live pane belongs to the selected row". The inbox rows are what
// make the overlay own the keyboard at all: a flag the snapshot did not copy
// would leave an open overlay on screen with every key going underneath it.
// The load-bearing rows are the pane ones: a pane installed for a row that is NOT selected does not
// count, and a pane with no selection at all does (the paned row left
// discovery and the list emptied — the pane must stay reachable). Neither
// focus nor view is masked here; the derivation in modePrecedence combines
// them, which is what lets the matrix test above enumerate the raw axes.
func TestModeSnapshot(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Model)
		want modeState
	}{
		{
			name: "zero model",
			mut:  func(*Model) {},
			want: modeState{},
		},
		{
			name: "confirming clean",
			mut:  func(m *Model) { m.modal = inputConfirmClean },
			want: modeState{modal: inputConfirmClean},
		},
		{
			name: "spawn error",
			mut:  func(m *Model) { m.modal = inputSpawnError },
			want: modeState{modal: inputSpawnError},
		},
		{
			name: "confirming restart",
			mut:  func(m *Model) { m.modal = inputConfirmRestart },
			want: modeState{modal: inputConfirmRestart},
		},
		{
			name: "editing label",
			mut:  func(m *Model) { m.modal = inputEditLabel },
			want: modeState{modal: inputEditLabel},
		},
		{
			name: "picking a provider",
			mut:  func(m *Model) { m.modal = inputPickProvider },
			want: modeState{modal: inputPickProvider},
		},
		{
			name: "switcher active",
			mut:  func(m *Model) { m.switcherOpen = true },
			want: modeState{switcherOpen: true},
		},
		{
			name: "form active",
			mut:  func(m *Model) { m.formOpen = true },
			want: modeState{formOpen: true},
		},
		{
			name: "inbox open",
			mut:  func(m *Model) { m.inboxOpen = true },
			want: modeState{inboxOpen: true},
		},
		{
			name: "inbox open over a focused overview",
			mut: func(m *Model) {
				m.inboxOpen = true
				m.focus = focusRight
				m.view = viewOverview
			},
			want: modeState{inboxOpen: true, focus: focusRight, view: viewOverview},
		},
		{
			name: "right focus copies across",
			mut:  func(m *Model) { m.focus = focusRight },
			want: modeState{focus: focusRight},
		},
		{
			name: "overview view copies across",
			mut:  func(m *Model) { m.view = viewOverview },
			want: modeState{view: viewOverview},
		},
		{
			name: "right focus in overview view, no pane",
			mut: func(m *Model) {
				m.focus = focusRight
				m.view = viewOverview
			},
			want: modeState{focus: focusRight, view: viewOverview},
		},
		{
			name: "a pane with no selection counts",
			mut:  func(m *Model) { m.pane = newFakePane("") },
			want: modeState{pane: true},
		},
		{
			name: "a pane for the selected row counts",
			mut: func(m *Model) {
				*m = testModel(runningSess("sess"))
				m.pane = newFakePane("")
				m.paneName = "sess"
			},
			want: modeState{pane: true},
		},
		{
			name: "a pane for another row does not count",
			mut: func(m *Model) {
				*m = testModel(runningSess("sess"))
				m.pane = newFakePane("")
				m.paneName = "other"
			},
			want: modeState{},
		},
		{
			name: "a pane behind the overview still counts: the derivation decides",
			mut: func(m *Model) {
				m.pane = newFakePane("")
				m.view = viewOverview
				m.focus = focusRight
			},
			want: modeState{focus: focusRight, view: viewOverview, pane: true},
		},
		{
			name: "resizing",
			mut:  func(m *Model) { m.resizing = true },
			want: modeState{resizing: true},
		},
		{
			name: "resizing over a drawn pane still counts",
			mut: func(m *Model) {
				m.resizing = true
				m.pane = newFakePane("")
				m.paneName = "sess"
			},
			want: modeState{resizing: true, pane: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m Model
			tc.mut(&m)
			if got := m.modeSnapshot(); got != tc.want {
				t.Fatalf("modeSnapshot() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// key constructors for the routing matrix. Chords carry no Text (the terminal
// sends a C0 byte, decoded to code+ModCtrl); printables carry their Text,
// which is what KeyPressMsg.String() — and therefore key.Matches — reads.
func chord(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl}
}

func press(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: text}
}

// TestRouteKeyMatrix pins every mode's declared routing for the keys whose
// placement was once the order of if-statements in handleKey and the
// per-handler special cases. Load-bearing rows, and why:
//
//   - ctrl+g rows: the mouse chord must be claimable from EVERY mode except
//     the switcher and the inbox overlay (each old handler intercepts it
//     first; the switcher's omission is documented intent at
//     model.go:2161-2165, and the inbox overlay closes on it). A row flipping
//     here either strands the user with a captured mouse or steals a key the
//     switcher never claimed.
//   - ctrl+c rows: quits from modals and switcher, component fall-through in
//     the form (TestFormSwallowsQuitKeysEscIsOnlyExit pins the behavior this
//     data must reproduce), child in a focused pane (the session owns SIGINT).
//   - ctrl+t rows: pane-toggle in pane, normal and detail modes, component in the form
//     (bubbles textarea's TransposeCharacterBackward — model.go:1673-1680),
//     never claimed by a modal. This is the old "intercept position" as data.
//   - ctrl+\ rows: unfocus when focused, close when normal WITH a pane, and
//     ignored when normal without one — the escalating chord plus its one
//     guard (routeConds.paneOpen).
//   - "q" and "y" rows: q must quit ONLY in the two dashboard-keyboard modes,
//     normal and detail (it is filter text in the switcher and prompt text in
//     the label editor, and the child's own byte in a pane); y must confirm ONLY
//     in the two y/n modals. These are the collisions the retired uiMode enum
//     was originally built to prevent (now inputMode's own doc).
//   - esc in the switcher is COMPONENT fall-through, not a rule: the two-stage
//     Esc lives in switcher.Update (switcher.go:130-143), and a parent rule
//     claiming esc would dismiss the overlay over a live filter.
//   - inputDetail rows: the focused detail pane's cross-mode differences, the
//     ones this per-key layout shows and a single-mode table cannot. esc
//     releases here but is ignored in normal mode; ctrl+\ releases UNGATED
//     where normal mode needs routeConds.paneOpen; enter is ignored where
//     normal mode attaches.
//   - the overview keys in normal mode (shift+arrows, ctrl+u/ctrl+d, tab,
//     shift+tab, 1-5) are ignored even with the overview drawn: with the list
//     focused nothing on the right is under control (plan objective 2), so a
//     rule that grew back here would let the list scroll or re-tab the
//     overview.
//     TestDetailFocusRetainedKeySet (detailfocus_test.go) enumerates the whole
//     focused surface; these rows carry only the normal/detail contrasts.
//   - "b" rows: the Build tab's key is a rule in normal and detail and in no
//     other mode. "b in pane forwards to the child" is the load-bearing one:
//     the agent pane keeps every key but its three reserved chords.
//   - "i" rows: the same shape for the inbox key, and "i in pane forwards to
//     the child" is load-bearing for the same reason.
//   - "P" and "U" rows: pause and resume the build, the same shape again. The
//     pane rows are load-bearing twice over: a capital letter is ordinary text
//     to an agent, and a rule here would pause a project's build because the
//     owner typed a sentence. The inbox rows are the other half: in the
//     overlay each is a letter of the answer. The confirm rows show a pending
//     y/n question swallows them like any other letter.
//   - inputInbox rows: the overlay has no parent rule, so EVERY key is the
//     component's. "q in inbox" and "ctrl+c in inbox" are the load-bearing
//     pair: a quit rule here would lose a half-typed answer. "ctrl+g in
//     inbox" is the one chord that is not the mouse toggle in this mode; the
//     overlay closes on it.
func TestRouteKeyMatrix(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())

	ctrlG := chord('g')
	ctrlC := chord('c')
	ctrlT := chord('t')
	ctrlBackslash := chord('\\')
	esc := tea.KeyPressMsg{Code: tea.KeyEsc}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	shiftTab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	shiftUp := tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
	shiftDown := tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	ctrlW := chord('w')
	left := tea.KeyPressMsg{Code: tea.KeyLeft}
	right := tea.KeyPressMsg{Code: tea.KeyRight}
	shiftLeft := tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift}
	shiftRight := tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
	yLower := press('y', "y")
	yUpper := press('Y', "Y")
	nLower := press('n', "n")
	q := press('q', "q")
	plainX := press('x', "x")

	cases := []struct {
		name  string
		mode  inputMode
		conds routeConds
		key   tea.KeyPressMsg
		want  keyDecision
	}{
		// ctrl+g: everywhere but the switcher.
		{"ctrl+g in normal", inputNormal, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in pane", inputPane, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in form", inputForm, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in confirm clean", inputConfirmClean, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in confirm restart", inputConfirmRestart, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in spawn error", inputSpawnError, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in edit label", inputEditLabel, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in detail", inputDetail, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in switcher goes to the list", inputSwitcher, routeConds{}, ctrlG, keyDecision{kind: routeComponent}},

		// ctrl+c: the universal escape hatch, except where documented.
		{"ctrl+c in confirm clean quits", inputConfirmClean, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in spawn error quits", inputSpawnError, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in confirm restart quits", inputConfirmRestart, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in edit label quits", inputEditLabel, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in switcher quits", inputSwitcher, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in normal quits", inputNormal, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in detail quits", inputDetail, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in form is swallowed by the component", inputForm, routeConds{}, ctrlC, keyDecision{kind: routeComponent}},
		{"ctrl+c in pane forwards to the child", inputPane, routeConds{}, ctrlC, keyDecision{kind: routeChild}},

		// ctrl+t: the old intercept position, as per-mode data.
		{"ctrl+t in pane toggles", inputPane, routeConds{}, ctrlT, keyDecision{routeAction, actionPaneToggle}},
		{"ctrl+t in normal is ignored", inputNormal, routeConds{}, ctrlT, keyDecision{kind: routeIgnored}},
		{"ctrl+t in detail toggles", inputDetail, routeConds{}, ctrlT, keyDecision{routeAction, actionPaneToggle}},
		{"ctrl+t in form goes to the component", inputForm, routeConds{}, ctrlT, keyDecision{kind: routeComponent}},
		{"ctrl+t in switcher goes to the list", inputSwitcher, routeConds{}, ctrlT, keyDecision{kind: routeComponent}},
		{"ctrl+t in confirm clean is swallowed", inputConfirmClean, routeConds{}, ctrlT, keyDecision{kind: routeIgnored}},

		// ctrl+\: the escalating chord and its pane guard.
		{"ctrl+backslash in pane unfocuses", inputPane, routeConds{}, ctrlBackslash, keyDecision{routeAction, actionPaneUnfocus}},
		{"ctrl+backslash in normal with a pane closes it", inputNormal, routeConds{paneOpen: true}, ctrlBackslash, keyDecision{routeAction, actionPaneClose}},
		{"ctrl+backslash in normal without a pane is ignored", inputNormal, routeConds{}, ctrlBackslash, keyDecision{kind: routeIgnored}},
		{"ctrl+backslash in detail releases the focus, unguarded", inputDetail, routeConds{}, ctrlBackslash, keyDecision{routeAction, actionDetailUnfocus}},
		{"ctrl+backslash in form goes to the component", inputForm, routeConds{}, ctrlBackslash, keyDecision{kind: routeComponent}},
		{"ctrl+backslash in confirm clean is swallowed", inputConfirmClean, routeConds{}, ctrlBackslash, keyDecision{kind: routeIgnored}},

		// esc, per mode.
		{"esc in confirm clean cancels", inputConfirmClean, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"esc in confirm restart cancels", inputConfirmRestart, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"esc in edit label cancels", inputEditLabel, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"esc in spawn error dismisses", inputSpawnError, routeConds{}, esc, keyDecision{routeAction, actionDismiss}},
		{"esc in form is the form-cancel gesture", inputForm, routeConds{}, esc, keyDecision{routeAction, actionFormCancel}},
		{"esc in switcher goes to the component (two-stage)", inputSwitcher, routeConds{}, esc, keyDecision{kind: routeComponent}},
		{"esc in normal is ignored", inputNormal, routeConds{}, esc, keyDecision{kind: routeIgnored}},
		{"esc in detail releases the focus", inputDetail, routeConds{}, esc, keyDecision{routeAction, actionDetailUnfocus}},
		{"esc in pane forwards to the child", inputPane, routeConds{}, esc, keyDecision{kind: routeChild}},

		// enter, per mode. In normal mode Enter and f are two physical keys for
		// the same current-view focus action; applyKeyAction chooses the rendered
		// pane or detail surface from detailDrawn().
		{"enter in normal focuses the current view", inputNormal, routeConds{}, enter, keyDecision{routeAction, actionFocusPane}},
		{"enter in detail is ignored", inputDetail, routeConds{}, enter, keyDecision{kind: routeIgnored}},
		{"enter in spawn error dismisses", inputSpawnError, routeConds{}, enter, keyDecision{routeAction, actionDismiss}},
		{"enter in edit label saves", inputEditLabel, routeConds{}, enter, keyDecision{routeAction, actionSubmit}},
		{"enter in form is the submit gesture", inputForm, routeConds{}, enter, keyDecision{routeAction, actionFormSubmit}},
		{"enter in switcher goes to the component", inputSwitcher, routeConds{}, enter, keyDecision{kind: routeComponent}},
		{"enter in confirm clean is swallowed", inputConfirmClean, routeConds{}, enter, keyDecision{kind: routeIgnored}},
		{"enter in confirm restart is swallowed", inputConfirmRestart, routeConds{}, enter, keyDecision{kind: routeIgnored}},

		// tab and shift+tab in the form, and the same two in detail mode.
		{"tab in form cycles forward", inputForm, routeConds{}, tab, keyDecision{routeAction, actionFormNextField}},
		{"shift+tab in form cycles back", inputForm, routeConds{}, shiftTab, keyDecision{routeAction, actionFormPrevField}},
		{"tab in detail switches tab forward", inputDetail, routeConds{}, tab, keyDecision{routeAction, actionDetailTabNext}},
		{"shift+tab in detail switches tab back", inputDetail, routeConds{}, shiftTab, keyDecision{routeAction, actionDetailTabPrev}},

		// The n/New collision the mode machine exists to prevent, plus y.
		{"y in confirm clean confirms", inputConfirmClean, routeConds{}, yLower, keyDecision{routeAction, actionConfirm}},
		{"Y in confirm clean confirms", inputConfirmClean, routeConds{}, yUpper, keyDecision{routeAction, actionConfirm}},
		{"y in confirm restart confirms", inputConfirmRestart, routeConds{}, yLower, keyDecision{routeAction, actionConfirm}},
		{"n in confirm clean denies, never opens the form", inputConfirmClean, routeConds{}, nLower, keyDecision{routeAction, actionDeny}},
		{"d in confirm clean is the delete-branch offer", inputConfirmClean, routeConds{}, press('d', "d"), keyDecision{routeAction, actionDeleteBranch}},
		{"D in confirm clean is the delete-branch offer", inputConfirmClean, routeConds{}, press('d', "D"), keyDecision{routeAction, actionDeleteBranch}},
		{"d in confirm restart is swallowed", inputConfirmRestart, routeConds{}, press('d', "d"), keyDecision{kind: routeIgnored}},
		{"n in normal opens the form", inputNormal, routeConds{}, press('n', "n"), keyDecision{routeAction, actionNewSession}},
		{"y in normal is ignored", inputNormal, routeConds{}, yLower, keyDecision{kind: routeIgnored}},
		{"y in edit label goes to the textinput", inputEditLabel, routeConds{}, yLower, keyDecision{kind: routeComponent}},

		// q: quit only in normal and detail.
		{"q in normal quits", inputNormal, routeConds{}, q, keyDecision{routeAction, actionQuit}},
		{"q in detail quits", inputDetail, routeConds{}, q, keyDecision{routeAction, actionQuit}},
		{"q in switcher filters (component)", inputSwitcher, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"q in form types (component)", inputForm, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"q in pane forwards to the child", inputPane, routeConds{}, q, keyDecision{kind: routeChild}},
		{"q in confirm clean is swallowed", inputConfirmClean, routeConds{}, q, keyDecision{kind: routeIgnored}},

		// Navigation and stray text.
		{"up in normal moves the cursor", inputNormal, routeConds{}, up, keyDecision{routeAction, actionCursorUp}},
		{"up in detail scrolls the viewport", inputDetail, routeConds{}, up, keyDecision{routeAction, actionDetailScrollUp}},
		{"up in switcher goes to the list", inputSwitcher, routeConds{}, up, keyDecision{kind: routeComponent}},
		{"shift+up in normal is ignored", inputNormal, routeConds{}, shiftUp, keyDecision{kind: routeIgnored}},
		{"shift+down in normal is ignored", inputNormal, routeConds{}, shiftDown, keyDecision{kind: routeIgnored}},
		{"ctrl+u in normal is ignored", inputNormal, routeConds{}, chord('u'), keyDecision{kind: routeIgnored}},
		{"ctrl+d in normal is ignored", inputNormal, routeConds{}, chord('d'), keyDecision{kind: routeIgnored}},
		{"tab in normal is ignored", inputNormal, routeConds{}, tab, keyDecision{kind: routeIgnored}},
		{"shift+tab in normal is ignored", inputNormal, routeConds{}, shiftTab, keyDecision{kind: routeIgnored}},
		{"1 in normal is ignored", inputNormal, routeConds{}, press('1', "1"), keyDecision{kind: routeIgnored}},
		{"shift+up in detail is ignored", inputDetail, routeConds{}, shiftUp, keyDecision{kind: routeIgnored}},
		{"shift+down in detail is ignored", inputDetail, routeConds{}, shiftDown, keyDecision{kind: routeIgnored}},
		{"plain text in edit label goes to the textinput", inputEditLabel, routeConds{}, plainX, keyDecision{kind: routeComponent}},
		{"plain text in confirm clean is swallowed", inputConfirmClean, routeConds{}, plainX, keyDecision{kind: routeIgnored}},
		{"plain text in pane forwards to the child", inputPane, routeConds{}, plainX, keyDecision{kind: routeChild}},
		{"plain text in normal is ignored", inputNormal, routeConds{}, plainX, keyDecision{kind: routeIgnored}},
		{"plain text in detail is ignored", inputDetail, routeConds{}, plainX, keyDecision{kind: routeIgnored}},

		// The provider picker. enter and esc reuse the confirm/cancel actions
		// (applyKeyAction resolves them by mode); up/k and down/j move the
		// highlight. Every other key is swallowed: q must not quit and a stray
		// letter must not start or dismiss anything — the y/n modals' rule.
		{"ctrl+g in provider picker", inputPickProvider, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+c in provider picker quits", inputPickProvider, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"esc in provider picker cancels", inputPickProvider, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"enter in provider picker confirms", inputPickProvider, routeConds{}, enter, keyDecision{routeAction, actionConfirm}},
		{"up in provider picker moves up", inputPickProvider, routeConds{}, up, keyDecision{routeAction, actionPickerUp}},
		{"k in provider picker moves up", inputPickProvider, routeConds{}, press('k', "k"), keyDecision{routeAction, actionPickerUp}},
		{"down in provider picker moves down", inputPickProvider, routeConds{}, tea.KeyPressMsg{Code: tea.KeyDown}, keyDecision{routeAction, actionPickerDown}},
		{"j in provider picker moves down", inputPickProvider, routeConds{}, press('j', "j"), keyDecision{routeAction, actionPickerDown}},
		{"q in provider picker is swallowed", inputPickProvider, routeConds{}, q, keyDecision{kind: routeIgnored}},
		{"o in provider picker is swallowed", inputPickProvider, routeConds{}, press('o', "o"), keyDecision{kind: routeIgnored}},
		{"y in provider picker is swallowed", inputPickProvider, routeConds{}, yLower, keyDecision{kind: routeIgnored}},
		{"tab in provider picker is swallowed", inputPickProvider, routeConds{}, tab, keyDecision{kind: routeIgnored}},

		// b: the Build tab, from the list and from a focused overview only. The
		// pane row is the one that matters: with the agent focused `b` is a
		// letter the agent is being sent, and a rule here would take it away
		// from every session the dashboard hosts. Everywhere else it is text
		// for a component or is swallowed, like any other letter.
		{"b in normal shows the build tasks", inputNormal, routeConds{}, press('b', "b"), keyDecision{routeAction, actionBuild}},
		{"b in detail shows the build tasks", inputDetail, routeConds{}, press('b', "b"), keyDecision{routeAction, actionBuild}},
		{"b in pane forwards to the child", inputPane, routeConds{}, press('b', "b"), keyDecision{kind: routeChild}},
		{"b in pane forwards to the child even with a pane open and a filter applied", inputPane, routeConds{paneOpen: true, listFiltered: true}, press('b', "b"), keyDecision{kind: routeChild}},
		{"b in form types (component)", inputForm, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"b in filter types (component)", inputFilter, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"b in switcher filters (component)", inputSwitcher, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"b in palette types (component)", inputPalette, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"b in edit label goes to the textinput", inputEditLabel, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"b in confirm clean is swallowed", inputConfirmClean, routeConds{}, press('b', "b"), keyDecision{kind: routeIgnored}},
		{"b in resize is swallowed", inputResize, routeConds{}, press('b', "b"), keyDecision{kind: routeIgnored}},
		// i: the inbox overlay, on the same terms as b.
		{"i in normal opens the inbox", inputNormal, routeConds{}, press('i', "i"), keyDecision{routeAction, actionInbox}},
		{"i in detail opens the inbox", inputDetail, routeConds{}, press('i', "i"), keyDecision{routeAction, actionInbox}},
		{"i in pane forwards to the child", inputPane, routeConds{}, press('i', "i"), keyDecision{kind: routeChild}},
		{"i in pane forwards to the child even with a pane open and a filter applied", inputPane, routeConds{paneOpen: true, listFiltered: true}, press('i', "i"), keyDecision{kind: routeChild}},
		{"i in form types (component)", inputForm, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"i in filter types (component)", inputFilter, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"i in switcher filters (component)", inputSwitcher, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"i in palette types (component)", inputPalette, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"i in edit label goes to the textinput", inputEditLabel, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"i in confirm clean is swallowed", inputConfirmClean, routeConds{}, press('i', "i"), keyDecision{kind: routeIgnored}},
		{"i in resize is swallowed", inputResize, routeConds{}, press('i', "i"), keyDecision{kind: routeIgnored}},
		// P and U: pause and resume the build, on the same terms as b and i. The
		// lower-case letters keep their own meanings, which the last four rows
		// pin: a rule matching without regard to case would turn the project
		// switcher's key into a pause.
		{"P in normal pauses the build", inputNormal, routeConds{}, press('P', "P"), keyDecision{routeAction, actionPauseBuild}},
		{"P in detail pauses the build", inputDetail, routeConds{}, press('P', "P"), keyDecision{routeAction, actionPauseBuild}},
		{"P in pane forwards to the child", inputPane, routeConds{}, press('P', "P"), keyDecision{kind: routeChild}},
		{"P in pane forwards to the child even with a pane open and a filter applied", inputPane, routeConds{paneOpen: true, listFiltered: true}, press('P', "P"), keyDecision{kind: routeChild}},
		{"P in inbox is text (component)", inputInbox, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in form types (component)", inputForm, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in filter types (component)", inputFilter, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in switcher goes to the component", inputSwitcher, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in palette types (component)", inputPalette, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in edit label goes to the textinput", inputEditLabel, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in confirm clean is swallowed", inputConfirmClean, routeConds{}, press('P', "P"), keyDecision{kind: routeIgnored}},
		{"P in resize is swallowed", inputResize, routeConds{}, press('P', "P"), keyDecision{kind: routeIgnored}},
		{"U in normal resumes the build", inputNormal, routeConds{}, press('U', "U"), keyDecision{routeAction, actionResumeBuild}},
		{"U in detail resumes the build", inputDetail, routeConds{}, press('U', "U"), keyDecision{routeAction, actionResumeBuild}},
		{"U in pane forwards to the child", inputPane, routeConds{}, press('U', "U"), keyDecision{kind: routeChild}},
		{"U in pane forwards to the child even with a pane open and a filter applied", inputPane, routeConds{paneOpen: true, listFiltered: true}, press('U', "U"), keyDecision{kind: routeChild}},
		{"U in inbox is text (component)", inputInbox, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in form types (component)", inputForm, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in filter types (component)", inputFilter, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in switcher goes to the component", inputSwitcher, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in palette types (component)", inputPalette, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in edit label goes to the textinput", inputEditLabel, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in confirm clean is swallowed", inputConfirmClean, routeConds{}, press('U', "U"), keyDecision{kind: routeIgnored}},
		{"U in resize is swallowed", inputResize, routeConds{}, press('U', "U"), keyDecision{kind: routeIgnored}},
		{"p in normal still opens the projects", inputNormal, routeConds{}, press('p', "p"), keyDecision{routeAction, actionProjects}},
		{"u in normal is still ignored", inputNormal, routeConds{}, press('u', "u"), keyDecision{kind: routeIgnored}},
		{"p in detail is still swallowed", inputDetail, routeConds{}, press('p', "p"), keyDecision{kind: routeIgnored}},
		{"u in pane forwards to the child", inputPane, routeConds{}, press('u', "u"), keyDecision{kind: routeChild}},

		// The inbox overlay: no parent rule, every key to the component.
		{"q in inbox is text (component)", inputInbox, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"ctrl+c in inbox does not quit (component)", inputInbox, routeConds{}, ctrlC, keyDecision{kind: routeComponent}},
		{"ctrl+g in inbox closes the overlay (component)", inputInbox, routeConds{}, ctrlG, keyDecision{kind: routeComponent}},
		{"esc in inbox goes to the component (two-stage)", inputInbox, routeConds{}, esc, keyDecision{kind: routeComponent}},
		{"enter in inbox goes to the component", inputInbox, routeConds{}, enter, keyDecision{kind: routeComponent}},
		{"i in inbox is text (component)", inputInbox, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"b in inbox is text (component)", inputInbox, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"ctrl+t in inbox goes to the component", inputInbox, routeConds{}, ctrlT, keyDecision{kind: routeComponent}},
		{"ctrl+backslash in inbox goes to the component", inputInbox, routeConds{paneOpen: true}, ctrlBackslash, keyDecision{kind: routeComponent}},
		{"pgup in inbox goes to the component", inputInbox, routeConds{}, tea.KeyPressMsg{Code: tea.KeyPgUp}, keyDecision{kind: routeComponent}},
		// The sixth digit exists for the Build tab in a git-spice repo.
		{"6 in detail jumps to a tab", inputDetail, routeConds{}, press('6', "6"), keyDecision{routeAction, actionDetailTabJump}},
		{"6 in normal is ignored", inputNormal, routeConds{}, press('6', "6"), keyDecision{kind: routeIgnored}},

		// ctrl+w: enters resize mode from normal and detail only; a focused
		// pane forwards it to the child (delete-word in a shell), and inside
		// resize mode the same chord commits.
		{"ctrl+w in normal enters resize", inputNormal, routeConds{}, ctrlW, keyDecision{routeAction, actionEnterResize}},
		{"ctrl+w in detail enters resize", inputDetail, routeConds{}, ctrlW, keyDecision{routeAction, actionEnterResize}},
		{"ctrl+w in pane forwards to the child", inputPane, routeConds{}, ctrlW, keyDecision{kind: routeChild}},
		{"ctrl+w in form goes to the component", inputForm, routeConds{}, ctrlW, keyDecision{kind: routeComponent}},
		{"ctrl+w in switcher goes to the list", inputSwitcher, routeConds{}, ctrlW, keyDecision{kind: routeComponent}},
		{"ctrl+w in filter goes to the prompt", inputFilter, routeConds{}, ctrlW, keyDecision{kind: routeComponent}},
		{"ctrl+w in confirm clean is swallowed", inputConfirmClean, routeConds{}, ctrlW, keyDecision{kind: routeIgnored}},
		{"ctrl+w in resize commits", inputResize, routeConds{}, ctrlW, keyDecision{routeAction, actionExitResize}},

		// Resize mode: its enumerated keys, and everything else swallowed.
		{"esc in resize cancels", inputResize, routeConds{}, esc, keyDecision{routeAction, actionCancelResize}},
		{"h in resize narrows", inputResize, routeConds{}, press('h', "h"), keyDecision{routeAction, actionResizeLeft}},
		{"left in resize narrows", inputResize, routeConds{}, left, keyDecision{routeAction, actionResizeLeft}},
		{"l in resize widens", inputResize, routeConds{}, press('l', "l"), keyDecision{routeAction, actionResizeRight}},
		{"right in resize widens", inputResize, routeConds{}, right, keyDecision{routeAction, actionResizeRight}},
		{"H in resize narrows big", inputResize, routeConds{}, press('H', "H"), keyDecision{routeAction, actionResizeLeftBig}},
		{"shift+left in resize narrows big", inputResize, routeConds{}, shiftLeft, keyDecision{routeAction, actionResizeLeftBig}},
		{"L in resize widens big", inputResize, routeConds{}, press('L', "L"), keyDecision{routeAction, actionResizeRightBig}},
		{"shift+right in resize widens big", inputResize, routeConds{}, shiftRight, keyDecision{routeAction, actionResizeRightBig}},
		{"q in resize quits", inputResize, routeConds{}, q, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in resize quits", inputResize, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+g in resize", inputResize, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+t in resize is ignored", inputResize, routeConds{}, ctrlT, keyDecision{kind: routeIgnored}},
		{"enter in resize is ignored", inputResize, routeConds{}, enter, keyDecision{kind: routeIgnored}},
		{"n in resize is ignored", inputResize, routeConds{}, nLower, keyDecision{kind: routeIgnored}},
		{"j in resize is ignored", inputResize, routeConds{}, press('j', "j"), keyDecision{kind: routeIgnored}},
		{"up in resize is ignored", inputResize, routeConds{}, up, keyDecision{kind: routeIgnored}},
		{"plain text in resize is ignored", inputResize, routeConds{}, plainX, keyDecision{kind: routeIgnored}},
		{"h in normal is ignored", inputNormal, routeConds{}, press('h', "h"), keyDecision{kind: routeIgnored}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reg.route(tc.mode, tc.conds, tc.key)
			if got != tc.want {
				t.Fatalf("route(%v, %+v, %q) = {%v %v}, want {%v %v}",
					tc.mode, tc.conds, tc.key.String(),
					got.kind, got.action, tc.want.kind, tc.want.action)
			}
		})
	}
}

// TestDetailScrollKeysRouteToActions pins W4.T1's four detail-pane scroll
// bindings at the dispatch-table level, and the pgup/pgdown rows alongside
// them for the reason those two mode.go comments give: the whole point of
// adding dedicated detail keys rather than repointing pgup/pgdn is that the
// two key sets stay disjoint. A dispatch-table pass here is NOT evidence the
// feature works — these bindings were fully wired and still completely dead
// until W4.T1 seeded the Model's viewport with content (syncDetailViewport;
// see the doc on detailViewport in model.go), and wired-and-seeded still
// stopped three rows short until the viewport was sized from the frame's own
// row budget (detailRowBudget); ui_test.go's behavior-through-Update tests
// carry the "does it actually scroll, all the way down" assertion this table
// cannot make.
//
// Every row here is load-bearing for the same reason: each is a distinct key
// string (shift+up, shift+down, ctrl+u, ctrl+d, pgup, pgdown) that a typo in
// mode.go's rule table — the wrong binding var, or a copy-pasted action
// constant — would misroute silently, since all six live in the same
// r[inputNormal].rules slice with no compiler check tying binding to action.
// TestOverviewKeysAreAdvertisedOnlyWhileTheOverviewIsFocused pins where the
// overview's scroll and tab keys are discoverable. Both halves are
// load-bearing: each key must carry help in inputDetail's keymap (the focused
// overview's footer renders it), and must be ABSENT from inputNormal's, which
// `?` and the `:` palette build from — advertising them there would offer the
// list a way to drive the right pane (plan objective 2). Naming each key
// string is the point: the generic completeness tests walk the same slices
// and cannot notice a key moving between them.
func TestOverviewKeysAreAdvertisedOnlyWhileTheOverviewIsFocused(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	find := func(bindings []key.Binding, want string) *key.Binding {
		for i := range bindings {
			for _, k := range bindings[i].Keys() {
				if k == want {
					return &bindings[i]
				}
			}
		}
		return nil
	}

	for _, want := range []string{"ctrl+u", "ctrl+d", "tab", "shift+tab", "1"} {
		t.Run(want, func(t *testing.T) {
			found := find(reg.spec(inputDetail).keys.bindings, want)
			if found == nil {
				t.Fatalf("%q is not in inputDetail's keymap, so the focused overview cannot advertise it", want)
			}
			if h := found.Help(); h.Key == "" || h.Desc == "" {
				t.Errorf("%q carries help %+v; both halves must be set or it renders blank", want, h)
			}
			if find(reg.spec(inputNormal).keys.bindings, want) != nil {
				t.Errorf("%q is in inputNormal's keymap; the list must not advertise a right-pane key", want)
			}
		})
	}
	for _, gone := range []string{"shift+up", "shift+down"} {
		if find(reg.spec(inputNormal).keys.bindings, gone) != nil {
			t.Errorf("%q is in inputNormal's keymap; it scrolled the overview from the list", gone)
		}
	}
}

// TestNormalModeKeyMapPrioritizesSurfaceControls pins the palette/help order,
// which is deliberately separate from routing-rule order. At 120x40 the
// palette's first viewport holds 14 rows, so Focus — the one way to the right
// side — must follow movement immediately rather than merely be present
// somewhere in the full registry. ctrl+t must be ABSENT: with the list focused
// it controls nothing (plan objective 2/3), so it is neither a rule nor a
// help/palette row in normal mode. The general completeness and
// palette-runnability tests below guard that the mechanically displaced
// unrelated bindings remain represented.
func TestNormalModeKeyMapPrioritizesSurfaceControls(t *testing.T) {
	keys := defaultKeyMap()
	registry := newModeRegistry(keys)
	bindings := registry.spec(inputNormal).keys.bindings
	want := []key.Binding{keys.Up, keys.Down, keys.Focus}

	sameKeys := func(a, b []string) bool {
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

	if len(bindings) < len(want) {
		t.Fatalf("normal keymap has %d bindings, want at least %d", len(bindings), len(want))
	}
	for i, expected := range want {
		if !sameKeys(bindings[i].Keys(), expected.Keys()) {
			t.Errorf("normal keymap row %d has keys %v, want %v", i+1, bindings[i].Keys(), expected.Keys())
		}
		count := 0
		for _, binding := range bindings {
			if sameKeys(binding.Keys(), expected.Keys()) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("normal keymap contains keys %v %d times, want exactly once", expected.Keys(), count)
		}
	}
	for _, binding := range bindings {
		if sameKeys(binding.Keys(), keys.Toggle.Keys()) {
			t.Errorf("normal keymap carries ctrl+t (%v); the list must not advertise a right-side key", binding.Keys())
		}
	}
}

// TestDetailScrollKeysRouteToActions pins the overview's scroll keys to the
// FOCUSED overview only. The normal-mode rows are load-bearing: ctrl+u/ctrl+d
// and the shift+arrows used to scroll an unfocused overview from the list,
// which is exactly the "list controls the right pane" leak objective 2 rules
// out, so each must fall through to routeIgnored. pgup/pgdown stay list paging.
// The inputDetail rows are the cursor-vs-viewport split: the plain list keys
// scroll the viewport once the overview owns the keyboard.
func TestDetailScrollKeysRouteToActions(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())

	cases := []struct {
		name string
		mode inputMode
		key  tea.KeyPressMsg
		want modeAction // actionNone: the key must be ignored
	}{
		{"shift+up from the list is ignored", inputNormal, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}, actionNone},
		{"shift+down from the list is ignored", inputNormal, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}, actionNone},
		{"ctrl+u from the list is ignored", inputNormal, chord('u'), actionNone},
		{"ctrl+d from the list is ignored", inputNormal, chord('d'), actionNone},
		{"pgup still pages the session list", inputNormal, tea.KeyPressMsg{Code: tea.KeyPgUp}, actionPageUp},
		{"pgdown still pages the session list", inputNormal, tea.KeyPressMsg{Code: tea.KeyPgDown}, actionPageDown},

		// Focused: the cursor-vs-viewport split. up/down/j/k, pgup/pgdn and
		// home/end all claim their PLAIN session-list key here instead, which
		// is the inverted half of the rows above.
		{"focused up scrolls the detail pane up", inputDetail, tea.KeyPressMsg{Code: tea.KeyUp}, actionDetailScrollUp},
		{"focused k scrolls the detail pane up", inputDetail, press('k', "k"), actionDetailScrollUp},
		{"focused down scrolls the detail pane down", inputDetail, tea.KeyPressMsg{Code: tea.KeyDown}, actionDetailScrollDown},
		{"focused j scrolls the detail pane down", inputDetail, press('j', "j"), actionDetailScrollDown},
		{"focused pgup pages the detail pane up", inputDetail, tea.KeyPressMsg{Code: tea.KeyPgUp}, actionDetailPageUp},
		{"focused pgdown pages the detail pane down", inputDetail, tea.KeyPressMsg{Code: tea.KeyPgDown}, actionDetailPageDown},
		{"focused home goes to the top of the detail pane", inputDetail, tea.KeyPressMsg{Code: tea.KeyHome}, actionDetailGotoTop},
		{"focused end goes to the bottom of the detail pane", inputDetail, tea.KeyPressMsg{Code: tea.KeyEnd}, actionDetailGotoBottom},

		{"focused ctrl+u half-pages the detail pane up", inputDetail, chord('u'), actionDetailHalfPageUp},
		{"focused ctrl+d half-pages the detail pane down", inputDetail, chord('d'), actionDetailHalfPageDown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := keyDecision{routeAction, tc.want}
			if tc.want == actionNone {
				want = keyDecision{kind: routeIgnored}
			}
			got := reg.route(tc.mode, routeConds{}, tc.key)
			if got != want {
				t.Fatalf("route(%v, %q) = {%v %v}, want {%v %v}",
					tc.mode, tc.key.String(), got.kind, got.action, want.kind, want.action)
			}
		})
	}
}

// TestModeRegistryCoversEveryMode guards the mechanism's completeness rather
// than any one row: every inputMode must appear exactly once in the precedence
// table (with normal as the unconditional last layer), own a spec whose mode
// field matches its index, and expose a non-empty help keymap. Wave 6 reads
// availability from this data — a mode added without its row here would
// otherwise surface as a help/palette blind spot, not a compile error.
func TestModeRegistryCoversEveryMode(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())

	seen := make(map[inputMode]int)
	for _, layer := range modePrecedence {
		seen[layer.mode]++
	}
	for mode := inputMode(0); mode < inputModeCount; mode++ {
		if seen[mode] != 1 {
			t.Errorf("mode %v appears %d times in modePrecedence, want exactly 1", mode, seen[mode])
		}
	}
	last := modePrecedence[len(modePrecedence)-1]
	if last.mode != inputNormal {
		t.Errorf("last precedence layer is %v, want %v", last.mode, inputNormal)
	}
	if !last.active(modeState{}) {
		t.Error("the final precedence layer must be unconditionally active")
	}

	for mode := inputMode(0); mode < inputModeCount; mode++ {
		spec := reg.spec(mode)
		if spec.mode != mode {
			t.Errorf("registry slot %v holds spec for %v", mode, spec.mode)
		}
		if len(spec.keys.ShortHelp()) == 0 {
			t.Errorf("mode %v exposes an empty keymap", mode)
		}
		if len(spec.keys.FullHelp()) == 0 {
			t.Errorf("mode %v exposes empty full help", mode)
		}
	}
}

// TestModeKeyMapsAdvertiseTheModalAnswers pins the help surface Wave 6 renders
// from: the answers each modal accepts, the switcher's component-owned keys,
// and the form's gestures must be readable off the mode's keymap. The
// switcher's "R" row is load-bearing — the global keyMap deliberately omits R
// (defaultKeyMap's comment on Project), so this keymap is the ONLY structured
// record that the key exists, and losing it makes rescan undiscoverable to
// the palette.
func TestModeKeyMapsAdvertiseTheModalAnswers(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())

	wantKeys := []struct {
		name string
		mode inputMode
		key  string
	}{
		{"confirm clean advertises y", inputConfirmClean, "y"},
		{"confirm clean advertises d", inputConfirmClean, "d"},
		{"confirm clean advertises n", inputConfirmClean, "n"},
		{"confirm clean advertises esc", inputConfirmClean, "esc"},
		{"confirm restart advertises y", inputConfirmRestart, "y"},
		{"spawn error advertises esc", inputSpawnError, "esc"},
		{"spawn error advertises enter", inputSpawnError, "enter"},
		{"edit label advertises enter", inputEditLabel, "enter"},
		{"edit label advertises esc", inputEditLabel, "esc"},
		{"provider picker advertises up", inputPickProvider, "up"},
		{"provider picker advertises k", inputPickProvider, "k"},
		{"provider picker advertises down", inputPickProvider, "down"},
		{"provider picker advertises j", inputPickProvider, "j"},
		{"provider picker advertises enter", inputPickProvider, "enter"},
		{"provider picker advertises esc", inputPickProvider, "esc"},
		{"switcher advertises R", inputSwitcher, "R"},
		{"switcher advertises enter", inputSwitcher, "enter"},
		{"switcher advertises esc", inputSwitcher, "esc"},
		{"form advertises tab", inputForm, "tab"},
		{"form advertises shift+tab", inputForm, "shift+tab"},
		{"form advertises esc", inputForm, "esc"},
		{"pane advertises ctrl+backslash", inputPane, "ctrl+\\"},
		{"normal advertises n", inputNormal, "n"},
		// The keys bubbles/list brought with it. Wave 6 reads availability off
		// this data, so a gesture the component owns but the keymap omits is a
		// help and palette blind spot rather than a compile error.
		{"normal advertises the filter key", inputNormal, "/"},
		{"normal advertises esc for clearing a filter", inputNormal, "esc"},
		{"normal advertises pgup", inputNormal, "pgup"},
		{"normal advertises pgdown", inputNormal, "pgdown"},
		{"normal advertises home", inputNormal, "home"},
		{"normal advertises end", inputNormal, "end"},
		{"filter advertises enter", inputFilter, "enter"},
		{"filter advertises esc", inputFilter, "esc"},
		{"filter advertises ctrl+c", inputFilter, "ctrl+c"},
		{"normal advertises ctrl+w", inputNormal, "ctrl+w"},
		{"detail advertises ctrl+w", inputDetail, "ctrl+w"},
		{"resize advertises ctrl+w", inputResize, "ctrl+w"},
		{"resize advertises esc", inputResize, "esc"},
		{"resize advertises h", inputResize, "h"},
		{"resize advertises l", inputResize, "l"},
		{"resize advertises H", inputResize, "H"},
		{"resize advertises L", inputResize, "L"},
		{"resize advertises shift+left", inputResize, "shift+left"},
		{"resize advertises shift+right", inputResize, "shift+right"},
		// The inbox overlay's keys are all the component's, so this keymap is
		// the only structured record of them, for both of its screens.
		{"normal advertises i", inputNormal, "i"},
		{"detail advertises i", inputDetail, "i"},
		{"normal advertises P", inputNormal, "P"},
		{"detail advertises P", inputDetail, "P"},
		{"normal advertises U", inputNormal, "U"},
		{"detail advertises U", inputDetail, "U"},
		{"inbox advertises enter", inputInbox, "enter"},
		{"inbox advertises esc", inputInbox, "esc"},
		{"inbox advertises up", inputInbox, "up"},
		{"inbox advertises down", inputInbox, "down"},
		{"inbox advertises pgup", inputInbox, "pgup"},
		{"inbox advertises pgdown", inputInbox, "pgdown"},
		{"inbox advertises ctrl+g", inputInbox, "ctrl+g"},
	}

	for _, tc := range wantKeys {
		t.Run(tc.name, func(t *testing.T) {
			for _, b := range reg.spec(tc.mode).keys.ShortHelp() {
				for _, k := range b.Keys() {
					if k == tc.key {
						return
					}
				}
			}
			t.Fatalf("mode %v keymap does not advertise %q", tc.mode, tc.key)
		})
	}
}

// TestEveryModeBindingHasHelpText is W6.T1's structural guarantee: a
// key.Binding with an empty Help() is invisible to help.Model and the `:`
// palette exactly like a bare key comparison would be, so completeness means
// every binding in every mode's keymap carries both halves of WithHelp, not
// just that the binding exists. It enumerates every mode rather than a fixed
// list so a mode added later is covered automatically.
func TestEveryModeBindingHasHelpText(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())

	for mode := inputMode(0); mode < inputModeCount; mode++ {
		for _, b := range reg.spec(mode).keys.ShortHelp() {
			h := b.Help()
			if h.Key == "" || h.Desc == "" {
				t.Errorf("mode %v has a binding with keys %v and help %+v, want non-empty Key and Desc", mode, b.Keys(), h)
			}
		}
	}
}

// TestListOwnedKeysAreRepresentedInTheKeymap diffs list.Model's actual owned
// keys (list.DefaultKeyMap(), read from the library rather than from memory,
// same discipline decisions.md's table used) against the union of
// inputNormal's and inputFilter's keymaps — the only two modes the session
// list ever receives a key in (mode.go's fall-through rules). A key present
// in the component but silent in both keymaps is exactly the blind spot this
// wave exists to close: it fires (or would, if forwarded) with nothing in any
// help surface saying so.
//
// excludedListKeys are the keys the table explicitly narrows away or
// disables, not omissions: h/b/u/left (PrevPage) and right/d (NextPage) lose
// to pgup/pgdown because l, f and o already mean label, focus-pane and
// one-off; g (GoToStart) and G (GoToEnd) lose to home/end for the same
// collision-avoidance reason; "?" (ShowFullHelp/CloseFullHelp) is cleared for
// Wave 6 to claim itself; "v" (Quit) is killed by DisableQuitKeybindings so
// nothing to answer for exists. Each is a decision, not a gap, so the test
// documents rather than silently allow-lists them.
func TestListOwnedKeysAreRepresentedInTheKeymap(t *testing.T) {
	excludedListKeys := map[string]string{
		"left":  "PrevPage narrowed to pgup-only; l/f/o already mean label/focus/one-off",
		"h":     "PrevPage narrowed to pgup-only",
		"b":     "PrevPage narrowed to pgup-only",
		"u":     "PrevPage narrowed to pgup-only",
		"right": "NextPage narrowed to pgdown-only",
		"d":     "NextPage narrowed to pgdown-only",
		"g":     "GoToStart narrowed to home-only",
		"G":     "GoToEnd narrowed to end-only",
		"?":     "ShowFullHelp/CloseFullHelp key set cleared; Wave 6 claims ?",
		"v":     "Quit disabled via DisableQuitKeybindings",
	}

	reg := newModeRegistry(defaultKeyMap())
	represented := map[string]bool{}
	for _, mode := range []inputMode{inputNormal, inputFilter} {
		for _, b := range reg.spec(mode).keys.ShortHelp() {
			for _, k := range b.Keys() {
				represented[k] = true
			}
		}
	}

	lm := list.DefaultKeyMap()
	owned := []key.Binding{
		lm.CursorUp, lm.CursorDown, lm.PrevPage, lm.NextPage,
		lm.GoToStart, lm.GoToEnd, lm.Filter, lm.ClearFilter,
		lm.CancelWhileFiltering, lm.AcceptWhileFiltering,
		lm.ShowFullHelp, lm.CloseFullHelp, lm.Quit, lm.ForceQuit,
	}
	for _, b := range owned {
		for _, k := range b.Keys() {
			if excludedListKeys[k] != "" {
				continue
			}
			if !represented[k] {
				t.Errorf("list.Model owns key %q but neither inputNormal nor inputFilter's keymap represents it", k)
			}
		}
	}
}
