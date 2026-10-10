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
//   - "focused flag without a pane" pins that modeState.paneFocused is the
//     conjunction the old handleKey's guard used — callers build it as
//     paneFocused && pane != nil, and a bare flag must not claim the keyboard.
//   - The detailFocused rows pin the new layer's POSITION, directly above
//     normal mode and below everything else. modePrecedence is hand-ordered
//     and a row slipped above a modal is shadowed silently, with no error —
//     move the entry up and these rows are what turns it red. The
//     "detail focused alone" row is also what decides esc: filterClear binds
//     esc in inputNormal and the release binds it here, so an applied filter
//     survives the release because this mode outranks normal, not because of
//     any rule ordering.
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
		{
			name:  "filter prompt alone",
			state: modeState{listFiltering: true},
			want:  inputFilter,
		},
		{
			// Reachable: applyPaneAttached sets paneFocused asynchronously (a
			// honored click arrives already focused), so a focus can land while
			// the prompt is open. The prompt is what is on screen and what the
			// user is typing into, so it keeps the keyboard — the same rule the
			// form/pane row below states.
			name:  "filter prompt over focused pane",
			state: modeState{listFiltering: true, paneFocused: true},
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
			name:  "focused pane alone",
			state: modeState{paneFocused: true},
			want:  inputPane,
		},
		{
			name:  "detail focused alone",
			state: modeState{detailFocused: true},
			want:  inputDetail,
		},
		{
			// Unreachable through the snapshot conjunctions (paneFocused needs
			// a pane, detailFocused needs the pane NOT to be the drawn
			// surface), declared so the order is a decision rather than an
			// accident of table position — the same reason "switcher over
			// form" is declared above.
			name:  "focused pane over focused detail: the pane wins",
			state: modeState{paneFocused: true, detailFocused: true},
			want:  inputPane,
		},
		{
			name:  "filter prompt over focused detail",
			state: modeState{listFiltering: true, detailFocused: true},
			want:  inputFilter,
		},
		{
			name:  "form over focused detail",
			state: modeState{formOpen: true, detailFocused: true},
			want:  inputForm,
		},
		{
			name:  "switcher over focused detail",
			state: modeState{switcherOpen: true, detailFocused: true},
			want:  inputSwitcher,
		},
		{
			name:  "palette over focused detail",
			state: modeState{paletteOpen: true, detailFocused: true},
			want:  inputPalette,
		},
		{
			name:  "confirm clean over focused detail: modal wins",
			state: modeState{modal: inputConfirmClean, detailFocused: true},
			want:  inputConfirmClean,
		},
		{
			name:  "resizing alone",
			state: modeState{resizing: true},
			want:  inputResize,
		},
		{
			// Reachable: resize mode is entered from a focused detail pane and
			// leaves detailFocused set, so the resize must outrank it.
			name:  "resizing over focused detail",
			state: modeState{resizing: true, detailFocused: true},
			want:  inputResize,
		},
		{
			// Reachable only through a focus-on-arrival attach landing
			// mid-resize: the explicit gesture keeps the keyboard.
			name:  "resizing over focused pane",
			state: modeState{resizing: true, paneFocused: true},
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
			state: modeState{formOpen: true, paneFocused: true},
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
			state: modeState{modal: inputConfirmClean, paneFocused: true},
			want:  inputConfirmClean,
		},
		{
			name:  "switcher over focused pane: switcher wins",
			state: modeState{switcherOpen: true, paneFocused: true},
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
				modal:         inputEditLabel,
				switcherOpen:  true,
				formOpen:      true,
				paneFocused:   true,
				detailFocused: true,
				resizing:      true,
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

// TestActiveInputModeModalAlwaysWins covers the 320 combinations the named
// matrix above samples: every modal, crossed with every setting of the six
// composing flags (5 × 2^6 = 320), must derive the modal. This is the exhaustive form of the
// "modal outranks everything" invariant — a precedence-table edit that slips
// any layer above the modals turns some cell here red. The modal set is
// derived from isModal rather than listed, so a sixth modal added later is
// covered the moment its constant exists; the count check below makes adding
// one a deliberate edit rather than a silent widening.
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
	for _, modal := range modals {
		for _, sw := range bools {
			for _, form := range bools {
				for _, filtering := range bools {
					for _, pane := range bools {
						for _, detail := range bools {
							for _, resizing := range bools {
								s := modeState{
									modal:         modal,
									switcherOpen:  sw,
									formOpen:      form,
									listFiltering: filtering,
									paneFocused:   pane,
									detailFocused: detail,
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

// TestModeSnapshot pins the Model→modeState bridge: each overlay-activity field
// maps to its snapshot field, and paneFocused is the conjunction with pane !=
// nil. The load-bearing rows are the two pane ones — a focused flag with a nil
// pane must NOT claim the keyboard, which is the guard the old handleKey spelled
// `m.paneFocused && m.pane != nil` and the dispatch now inherits from here —
// and the detail pair, which is the same guard mirrored: detailFocused counts
// only while the pane is NOT the drawn surface, so a focus-on-arrival attach
// landing under a focused detail view cannot leave the keyboard on a view that
// is no longer there.
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
			name: "focused flag with a pane",
			mut: func(m *Model) {
				m.pane = newFakePane("")
				m.paneFocused = true
			},
			want: modeState{paneFocused: true},
		},
		{
			name: "focused flag without a pane does not count",
			mut:  func(m *Model) { m.paneFocused = true },
			want: modeState{},
		},
		{
			name: "unfocused pane does not count",
			mut:  func(m *Model) { m.pane = newFakePane("") },
			want: modeState{},
		},
		{
			name: "detail focused with no pane on screen",
			mut:  func(m *Model) { m.detailFocused = true },
			want: modeState{detailFocused: true},
		},
		{
			name: "detail focused while the pane is the drawn surface does not count",
			mut: func(m *Model) {
				m.detailFocused = true
				m.pane = newFakePane("")
				m.paneName = "sess"
			},
			want: modeState{},
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
			want: modeState{resizing: true},
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
//     the switcher (each old handler intercepts it first; the switcher's
//     omission is documented intent at model.go:2161-2165). A row flipping
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
//     normal mode attaches. The shift+arrow pair is the load-bearing one:
//     those chords scroll the detail viewport from normal mode only, and are
//     ignored once focused — README's `shift+↑`/`shift+↓` bullet says exactly
//     that, so a rule table that grew them here would make the README wrong.
//     TestDetailFocusRetainedKeySet (detailfocus_test.go) enumerates the whole
//     focused surface; these rows carry only the normal/detail contrasts.
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
		{"ctrl+t in normal toggles", inputNormal, routeConds{}, ctrlT, keyDecision{routeAction, actionPaneToggle}},
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
		{"shift+up in normal scrolls the detail viewport", inputNormal, routeConds{}, shiftUp, keyDecision{routeAction, actionDetailScrollUp}},
		{"shift+down in normal scrolls the detail viewport", inputNormal, routeConds{}, shiftDown, keyDecision{routeAction, actionDetailScrollDown}},
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
// TestDetailScrollKeysAreAdvertisedInFullHelpAndThePalette pins the four
// detail-scroll keys onto the two surfaces that make them discoverable, since
// nothing else does. The dashboard's curated short help line deliberately omits
// them (it is width-bound and already at its cap — see renderNormalHelpKeyMap),
// so `?` and `:` are the ONLY places a user can find these keys, and both build
// themselves from r[inputNormal]'s modeKeyMap.bindings.
//
// The package's existing completeness tests cannot cover this. Both
// TestFullHelpOmitsNoEnabledBinding and TestEveryNormalRuleIsRunnableFromThePalette
// walk that same bindings slice and assert what is in it renders — so dropping
// these four FROM the slice removes them from the test's input as well, and
// both stay green while the keys silently become undiscoverable. Naming each
// key string explicitly here is the point: this test has to fail in exactly the
// case those two cannot see.
func TestDetailScrollKeysAreAdvertisedInFullHelpAndThePalette(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	bindings := reg.spec(inputNormal).keys.bindings

	for _, want := range []string{"shift+up", "shift+down", "ctrl+u", "ctrl+d"} {
		t.Run(want, func(t *testing.T) {
			var found *key.Binding
			for i := range bindings {
				for _, k := range bindings[i].Keys() {
					if k == want {
						found = &bindings[i]
					}
				}
			}
			if found == nil {
				t.Fatalf("%q is not in inputNormal's help keymap, so neither `?` nor `:` can advertise it", want)
			}
			if h := found.Help(); h.Key == "" || h.Desc == "" {
				t.Errorf("%q carries help %+v; both halves must be set or it renders blank in `?`", want, h)
			}
		})
	}
}

// TestNormalModeKeyMapPrioritizesSurfaceControls pins the palette/help order,
// which is deliberately separate from routing-rule order. At 120x40 the
// palette's first viewport holds 14 rows, so Focus and Toggle must be adjacent
// immediately after movement rather than merely present somewhere in the full
// registry. The general completeness and palette-runnability tests below guard
// that the mechanically displaced unrelated bindings remain represented.
func TestNormalModeKeyMapPrioritizesSurfaceControls(t *testing.T) {
	keys := defaultKeyMap()
	registry := newModeRegistry(keys)
	bindings := registry.spec(inputNormal).keys.bindings
	want := []key.Binding{keys.Up, keys.Down, keys.Focus, keys.Toggle}

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
}

// The mode field defaults to inputNormal (its zero value) for every
// pre-existing row, so those rows are byte-for-byte what this test asserted
// before W2.T1: the unfocused case is the regression guard, and it must keep
// passing unchanged. The inputDetail rows below are the inverted assertion
// W2.T1 adds — the same physical keys, claimed by the FOCUSED mode's own
// rule table instead, with ctrl+u/ctrl+d asserted to route to the identical
// action they already do unfocused (see mode.go's detailHalfPageUp/Down
// comment: they are the pre-existing detail-scroll chords, not list keys,
// in either mode).
func TestDetailScrollKeysRouteToActions(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())

	cases := []struct {
		name string
		mode inputMode
		key  tea.KeyPressMsg
		want modeAction
	}{
		{"shift+up scrolls the detail pane up", inputNormal, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}, actionDetailScrollUp},
		{"shift+down scrolls the detail pane down", inputNormal, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}, actionDetailScrollDown},
		{"ctrl+u half-pages the detail pane up", inputNormal, chord('u'), actionDetailHalfPageUp},
		{"ctrl+d half-pages the detail pane down", inputNormal, chord('d'), actionDetailHalfPageDown},
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

		// ctrl+u/ctrl+d: NOT part of the split above — the same chord, the
		// same action, focused or not.
		{"focused ctrl+u half-pages the detail pane up", inputDetail, chord('u'), actionDetailHalfPageUp},
		{"focused ctrl+d half-pages the detail pane down", inputDetail, chord('d'), actionDetailHalfPageDown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := keyDecision{routeAction, tc.want}
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
