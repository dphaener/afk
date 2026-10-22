package ui

import (
	"testing"

	"charm.land/bubbles/v2/key"
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
//   - "spawn error over open jump" pins modal > jump, which is
//     reachable (spawnFinishedMsg is async) and documented at model.go:872-895.
//     If it flips, a spawn failure's only readable record loses the keyboard.
//   - "jump over focused pane" pins the documented focusOnArrival case:
//     keys must go to the overlay the user is looking at, not the pane behind
//     it.
//   - "jump over form" is unreachable in practice but declared so the
//     frame (views.go renders jump first) and the keyboard can never
//     disagree; enshrining the opposite order would split them.
//   - The (focus, view, pane, placeholder) rows under "the two axes" are the
//     whole focus model as data. "list focus with a pane in agent view" is
//     the one that encodes the user's rule — with the list focused nothing on
//     the right owns the keyboard, however live the pane is — and "right
//     focus in agent view with nothing drawn" pins that the keyboard follows
//     the DRAWN surface: with nothing in the agent box the project view is
//     what is on screen, so its keys answer (T16.5 removed the overview that
//     used to stand there).
//   - "right focus, overview view, pane present" pins that the view axis
//     outranks the pane's existence: an installed pane in the overview view
//     does not claim the keyboard.
//   - The details rows pin the overlay's layer (T16.5): above the pane, which
//     it is laid over in the Agent, and below the overlays, the form and the
//     modals. modePrecedence is hand-ordered and a row slipped to the wrong
//     place is shadowed silently, with no error — move the entry and these
//     rows are what turns it red.
//   - The inbox rows pin the overlay's place: below every modal, the actions
//     dialog and the jump search, above everything else. "inbox over focused pane" is
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
			name:  "right focus in agent view with the placeholder drawn: the placeholder owns it",
			state: modeState{focus: focusRight, view: viewAgent, placeholder: true},
			want:  inputPlaceholder,
		},
		{
			name:  "right focus in agent view with nothing drawn: the Project's keys (load-bearing)",
			state: modeState{focus: focusRight, view: viewAgent},
			want:  inputNormal,
		},
		{
			name:  "right focus, overview view, no pane",
			state: modeState{focus: focusRight, view: viewOverview},
			want:  inputNormal,
		},
		{
			name:  "right focus, overview view, pane present: the view outranks the pane",
			state: modeState{focus: focusRight, view: viewOverview, pane: true},
			want:  inputNormal,
		},

		// The details overlay (T16.5). "details over a focused pane" is the
		// row it cannot work without: opened from the Agent the pane stays
		// focused behind it, and if the pane outranked it every scroll key
		// would reach the agent. The form row pins it under the overlays,
		// and the keys and leader rows that `?` and `:` open over it.
		{
			name:  "details alone",
			state: modeState{detailsOpen: true},
			want:  inputDetail,
		},
		{
			name:  "details over a focused pane (load-bearing)",
			state: modeState{detailsOpen: true, focus: focusRight, view: viewAgent, pane: true},
			want:  inputDetail,
		},
		{
			name:  "details over the placeholder",
			state: modeState{detailsOpen: true, focus: focusRight, view: viewAgent, placeholder: true},
			want:  inputDetail,
		},
		{
			name:  "the form outranks details",
			state: modeState{detailsOpen: true, formOpen: true},
			want:  inputForm,
		},
		{
			name:  "keys over details",
			state: modeState{detailsOpen: true, keysOpen: true},
			want:  inputKeys,
		},
		{
			name:  "leader over details",
			state: modeState{detailsOpen: true, leaderOpen: true},
			want:  inputLeader,
		},
		{
			name:  "a modal outranks details",
			state: modeState{detailsOpen: true, modal: inputConfirmRestart},
			want:  inputConfirmRestart,
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
			name:  "plan form alone",
			state: modeState{modal: inputPlanText},
			want:  inputPlanText,
		},
		{
			name:  "plan open offer alone",
			state: modeState{modal: inputConfirmPlanOpen},
			want:  inputConfirmPlanOpen,
		},
		{
			// The plan form is opened from the agent pane too (the leader
			// moves the keyboard to the list first), and a connect landing
			// focused under it must not take the typed sentence.
			name:  "plan form outranks a focused pane",
			state: modeState{modal: inputPlanText, focus: focusRight, view: viewAgent, pane: true},
			want:  inputPlanText,
		},
		{
			name:  "jump alone",
			state: modeState{jumpOpen: true},
			want:  inputJump,
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
			name:  "jump over inbox",
			state: modeState{jumpOpen: true, inboxOpen: true},
			want:  inputJump,
		},
		{
			name:  "plan check alone",
			state: modeState{planCheckOpen: true},
			want:  inputPlanCheck,
		},
		{
			// Reachable: "check the plan" is a list action, but a pane that
			// was focused on arrival can land under the overlay.
			name:  "plan check over focused pane: the overlay is what is on screen",
			state: modeState{planCheckOpen: true, focus: focusRight, view: viewAgent, pane: true},
			want:  inputPlanCheck,
		},
		{
			name:  "plan check over form",
			state: modeState{planCheckOpen: true, formOpen: true},
			want:  inputPlanCheck,
		},
		// The keys overlay (`?`, W3.T1 of T16.2). "keys over a focused pane"
		// is load-bearing: `?` is opened from the Agent with the keyboard left
		// there, so a pane ranked above it would take the letters typed into
		// its filter.
		{
			name:  "keys over focused pane: the overlay is what is on screen",
			state: modeState{keysOpen: true, focus: focusRight, view: viewAgent, pane: true},
			want:  inputKeys,
		},
		{
			name:  "keys over the key strip",
			state: modeState{keysOpen: true, keyStrip: true, focus: focusRight, view: viewAgent, pane: true},
			want:  inputKeys,
		},
		{
			name:  "keys over jump",
			state: modeState{keysOpen: true, jumpOpen: true},
			want:  inputKeys,
		},
		{
			name:  "keys over all projects",
			state: modeState{keysOpen: true, allProjectsOpen: true},
			want:  inputKeys,
		},
		{
			name:  "leader over keys",
			state: modeState{leaderOpen: true, keysOpen: true},
			want:  inputLeader,
		},
		{
			name:  "confirm clean over keys: modal wins",
			state: modeState{modal: inputConfirmClean, keysOpen: true},
			want:  inputConfirmClean,
		},
		{
			name:  "inbox over plan check",
			state: modeState{inboxOpen: true, planCheckOpen: true},
			want:  inputInbox,
		},
		{
			name:  "leader over plan check",
			state: modeState{leaderOpen: true, planCheckOpen: true},
			want:  inputLeader,
		},
		{
			// Reachable for the inbox's reason: a background create can fail
			// while the overlay is up.
			name:  "spawn error over plan check: modal wins",
			state: modeState{modal: inputSpawnError, planCheckOpen: true},
			want:  inputSpawnError,
		},
		{
			name:  "task tokens alone",
			state: modeState{taskTokensOpen: true},
			want:  inputTaskTokens,
		},
		{
			// Reachable in the way the overlay is opened: from the Build tab
			// with the right side focused, so the detail focus is under it.
			name:  "task tokens over focused detail",
			state: modeState{taskTokensOpen: true, focus: focusRight, view: viewOverview},
			want:  inputTaskTokens,
		},
		{
			name:  "plan check over task tokens",
			state: modeState{planCheckOpen: true, taskTokensOpen: true},
			want:  inputPlanCheck,
		},
		{
			name:  "leader over task tokens",
			state: modeState{leaderOpen: true, taskTokensOpen: true},
			want:  inputLeader,
		},
		{
			// Reachable: a background create can fail while the overlay is up.
			name:  "spawn error over task tokens: modal wins",
			state: modeState{modal: inputSpawnError, taskTokensOpen: true},
			want:  inputSpawnError,
		},
		{
			name:  "task tokens over all projects",
			state: modeState{taskTokensOpen: true, allProjectsOpen: true},
			want:  inputTaskTokens,
		},
		{
			name:  "all projects alone",
			state: modeState{allProjectsOpen: true},
			want:  inputAllProjects,
		},
		{
			// Reachable: a click on a notification can attach a pane focused
			// for this project while the screen is open. The screen is what
			// is on screen, so the rows keep the keyboard.
			name:  "all projects over focused pane",
			state: modeState{allProjectsOpen: true, focus: focusRight, view: viewAgent, pane: true},
			want:  inputAllProjects,
		},
		{
			name:  "all projects over focused detail",
			state: modeState{allProjectsOpen: true, focus: focusRight, view: viewOverview},
			want:  inputAllProjects,
		},
		{
			// Load-bearing: the dialog over the screen is how pause and
			// resume reach its rows.
			name:  "leader over all projects",
			state: modeState{leaderOpen: true, allProjectsOpen: true},
			want:  inputLeader,
		},
		{
			name:  "plan check over all projects",
			state: modeState{planCheckOpen: true, allProjectsOpen: true},
			want:  inputPlanCheck,
		},
		{
			// Reachable: a background create can fail while the screen is up.
			name:  "spawn error over all projects: modal wins",
			state: modeState{modal: inputSpawnError, allProjectsOpen: true},
			want:  inputSpawnError,
		},
		{
			name:  "leader over inbox",
			state: modeState{leaderOpen: true, inboxOpen: true},
			want:  inputLeader,
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
			name:  "form over focused detail",
			state: modeState{formOpen: true, focus: focusRight, view: viewOverview},
			want:  inputForm,
		},
		{
			name:  "jump over focused detail",
			state: modeState{jumpOpen: true, focus: focusRight, view: viewOverview},
			want:  inputJump,
		},
		{
			name:  "confirm clean over focused detail: modal wins",
			state: modeState{modal: inputConfirmClean, focus: focusRight, view: viewOverview},
			want:  inputConfirmClean,
		},
		{
			name:  "form open and pane focused: form wins (the closed corner)",
			state: modeState{formOpen: true, focus: focusRight, pane: true},
			want:  inputForm,
		},
		{
			name:  "spawn error over open jump: modal wins",
			state: modeState{modal: inputSpawnError, jumpOpen: true},
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
			name:  "jump over focused pane: jump wins",
			state: modeState{jumpOpen: true, focus: focusRight, pane: true},
			want:  inputJump,
		},
		{
			name:  "jump over form: jump wins",
			state: modeState{jumpOpen: true, formOpen: true},
			want:  inputJump,
		},
		{
			name: "everything at once: modal wins",
			state: modeState{
				modal:    inputEditLabel,
				jumpOpen: true,
				formOpen: true,
				focus:    focusRight,
				view:     viewOverview,
				pane:     true,
				keysOpen: true,
			},
			want: inputEditLabel,
		},

		// The actions dialog (T7.1). "leader over a focused pane" is the row
		// the dialog cannot work without: it is opened FROM the pane, so the
		// pane's layer is active the whole time it is up, and if the pane
		// outranked it every key typed into the dialog would reach the agent.
		{
			name:  "leader alone",
			state: modeState{leaderOpen: true},
			want:  inputLeader,
		},
		{
			name:  "leader over a focused pane",
			state: modeState{leaderOpen: true, focus: focusRight, view: viewAgent, pane: true},
			want:  inputLeader,
		},
		{
			name:  "leader over a focused overview",
			state: modeState{leaderOpen: true, focus: focusRight, view: viewOverview},
			want:  inputLeader,
		},
		{
			name:  "leader over the jump search",
			state: modeState{leaderOpen: true, jumpOpen: true},
			want:  inputLeader,
		},
		{
			name:  "a modal outranks the leader",
			state: modeState{modal: inputSpawnError, leaderOpen: true},
			want:  inputSpawnError,
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

// TestActiveInputModeModalAlwaysWins covers the 1280 combinations the named
// matrix above samples: every modal, crossed with every setting of the five
// composing flags and the two focus axes (10 × 2^7 = 1280), must derive the
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
	// Seven since T7.3 added the plan form and the open-it offer; eight since
	// T7.5 added the new-plan folder confirm; nine since T7.6 added the GitHub
	// repository question; ten since T7.8 added the approve-the-plan confirm;
	// thirteen since T7.10 added the add form's two steps and the remove
	// confirm; sixteen since T7.11 added the task-status picker, its
	// replacing-ids step and its confirm.
	if len(modals) != 16 {
		t.Fatalf("isModal admits %d modes, want the 16 footer modals", len(modals))
	}
	bools := []bool{false, true}
	focuses := []paneFocus{focusList, focusRight}
	views := []rightView{viewAgent, viewOverview}
	for _, modal := range modals {
		for _, sw := range bools {
			for _, form := range bools {
				for _, strip := range bools {
					for _, focus := range focuses {
						for _, view := range views {
							for _, pane := range bools {
								for _, keys := range bools {
									s := modeState{
										modal:    modal,
										jumpOpen: sw,
										formOpen: form,
										keyStrip: strip,
										focus:    focus,
										view:     view,
										pane:     pane,
										keysOpen: keys,
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
			name: "typing a plan change",
			mut:  func(m *Model) { m.modal = inputPlanText },
			want: modeState{modal: inputPlanText},
		},
		{
			name: "offered to open the planning session",
			mut:  func(m *Model) { m.modal = inputConfirmPlanOpen },
			want: modeState{modal: inputConfirmPlanOpen},
		},
		{
			name: "jump active",
			mut:  func(m *Model) { m.jumpOpen = true },
			want: modeState{jumpOpen: true},
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
			name: "plan check open",
			mut:  func(m *Model) { m.planCheckOpen = true },
			want: modeState{planCheckOpen: true},
		},
		{
			name: "task tokens open",
			mut:  func(m *Model) { m.taskTokensOpen = true },
			want: modeState{taskTokensOpen: true},
		},
		{
			name: "all projects open",
			mut:  func(m *Model) { m.allProjectsOpen = true },
			want: modeState{allProjectsOpen: true},
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
			name: "keys overlay open",
			mut:  func(m *Model) { m.keysOpen = true },
			want: modeState{keysOpen: true},
		},
		{
			name: "details open",
			mut:  func(m *Model) { m.detailsOpen = true },
			want: modeState{detailsOpen: true},
		},
		{
			name: "the agent placeholder for a running row with no pane",
			mut: func(m *Model) {
				*m = testModel(runningSess("sess"))
				m.focus = focusRight
			},
			want: modeState{focus: focusRight, placeholder: true},
		},
		{
			name: "a stopped row draws no placeholder (load-bearing: nothing drawn takes no keys)",
			mut: func(m *Model) {
				*m = testModel(sess("sess"))
				m.focus = focusRight
			},
			want: modeState{focus: focusRight},
		},
		{
			name: "leader open",
			mut:  func(m *Model) { m.leaderOpen = true },
			want: modeState{leaderOpen: true},
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
// per-handler special cases. Since T7.2 the list, the overview and the agent
// pane run their actions from the leader (leader.go), so most rows here pin
// a key that is NOT a rule any more. Load-bearing rows, and why:
//
//   - ctrl+g rows: the mouse chord is still claimable from every overlay,
//     form, prompt, confirm and resize mode (T7.2 left their keys alone, A4),
//     the jump search and the inbox overlay excepted as before. On the list and
//     the overview it is ignored (ctrl+] g), and in the pane it is the
//     agent's.
//   - ctrl+c rows: quits from modals, the jump search, the list and the
//     overview (A3: a stuck dashboard can always be left), component
//     fall-through in the form (TestFormSwallowsQuitKeysEscIsOnlyExit), child
//     in a focused pane (the session owns SIGINT).
//   - ctrl+t and ctrl+\ rows: forwarded to the child in the pane, ignored on
//     the list and the overview. A rule growing back in the pane would take
//     the key from every agent the dashboard hosts.
//   - "q" and "y" rows: q quits nowhere but resize mode now (it is filter
//     text in the jump search, prompt text in the label editor, the child's own
//     byte in a pane, and ctrl+] q on the list); y must confirm ONLY in
//     the two y/n modals.
//   - esc rows: in normal mode esc closes the full help first, then clears a
//     filter, and with neither does nothing; on the overview it releases.
//   - the letter rows (n, p, b, i, P, U and the rest): ignored on the list and
//     the overview, the child's in a pane, and text or swallowed in every
//     overlay, exactly as any other letter. The lower-case p and u rows keep
//     the capitals P and U from being matched without regard to case.
//   - the overview keys in normal mode (shift+arrows, ctrl+u/ctrl+d, tab,
//     shift+tab, 1-6) are ignored: with the list focused nothing on the right
//     is under control.
//   - ctrl+] rows: the leader is a rule in normal, detail and pane mode and
//     in no other, so under every overlay, form and confirm it is that
//     surface's key (or swallowed) and the dialog cannot open over it.
//   - inputInbox rows: the overlay has no parent rule, so EVERY key is the
//     component's. "q in inbox" and "ctrl+c in inbox" are the load-bearing
//     pair: a quit rule here would lose a half-typed answer.
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
	yLower := press('y', "y")
	yUpper := press('Y', "Y")
	nLower := press('n', "n")
	q := press('q', "q")
	plainX := press('x', "x")
	// ctrlBracket is the leader, ctrl+], as ultraviolet decodes 0x1D.
	// ctrlSpace was the leader before T9.1 and is an ordinary key now: the
	// rows that press it pin that it opens nothing and, in the pane, goes to
	// the agent.
	ctrlBracket := tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}
	ctrlSpace := tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl}

	cases := []struct {
		name  string
		mode  inputMode
		conds routeConds
		key   tea.KeyPressMsg
		want  keyDecision
	}{
		// ctrl+g: everywhere but the jump search.
		{"ctrl+g in normal is ignored (ctrl+] g)", inputNormal, routeConds{}, ctrlG, keyDecision{kind: routeIgnored}},
		{"ctrl+g in pane forwards to the child", inputPane, routeConds{}, ctrlG, keyDecision{kind: routeChild}},
		{"ctrl+g in form", inputForm, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in confirm clean", inputConfirmClean, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in confirm restart", inputConfirmRestart, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in spawn error", inputSpawnError, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in edit label", inputEditLabel, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in plan form", inputPlanText, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in plan open offer", inputConfirmPlanOpen, routeConds{}, ctrlG, keyDecision{routeAction, actionMouseToggle}},
		{"ctrl+g in detail is ignored (ctrl+] g)", inputDetail, routeConds{}, ctrlG, keyDecision{kind: routeIgnored}},
		{"ctrl+g in jump goes to the box", inputJump, routeConds{}, ctrlG, keyDecision{kind: routeComponent}},

		// ctrl+c: the universal escape hatch, except where documented.
		{"ctrl+c in confirm clean quits", inputConfirmClean, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in spawn error quits", inputSpawnError, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in confirm restart quits", inputConfirmRestart, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in edit label quits", inputEditLabel, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in plan form quits", inputPlanText, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in plan open offer quits", inputConfirmPlanOpen, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		// The plan form is a text prompt: esc cancels, enter starts, and
		// every letter (y and q included) is part of the sentence.
		{"esc in plan form cancels", inputPlanText, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"enter in plan form submits", inputPlanText, routeConds{}, enter, keyDecision{routeAction, actionSubmit}},
		{"y in plan form goes to the textinput", inputPlanText, routeConds{}, yLower, keyDecision{kind: routeComponent}},
		{"q in plan form goes to the textinput", inputPlanText, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"ctrl+] in plan form goes to the textinput", inputPlanText, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},
		// The open-it offer is a y/n confirmation: a stray key neither opens
		// the session nor dismisses the question.
		{"y in plan open offer confirms", inputConfirmPlanOpen, routeConds{}, yLower, keyDecision{routeAction, actionConfirm}},
		{"Y in plan open offer confirms", inputConfirmPlanOpen, routeConds{}, yUpper, keyDecision{routeAction, actionConfirm}},
		{"n in plan open offer denies", inputConfirmPlanOpen, routeConds{}, nLower, keyDecision{routeAction, actionDeny}},
		{"esc in plan open offer cancels", inputConfirmPlanOpen, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"q in plan open offer is swallowed", inputConfirmPlanOpen, routeConds{}, q, keyDecision{kind: routeIgnored}},
		{"enter in plan open offer is swallowed", inputConfirmPlanOpen, routeConds{}, enter, keyDecision{kind: routeIgnored}},
		{"ctrl+c in jump quits", inputJump, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in normal is the key table's (quit)", inputNormal, routeConds{}, ctrlC, keyDecision{kind: routeIgnored}},
		{"ctrl+c in detail quits (the overlay's own rule)", inputDetail, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"ctrl+c in form is swallowed by the component", inputForm, routeConds{}, ctrlC, keyDecision{kind: routeComponent}},
		{"ctrl+c in pane forwards to the child", inputPane, routeConds{}, ctrlC, keyDecision{kind: routeChild}},

		// ctrl+t: the old intercept position, as per-mode data.
		{"ctrl+t in pane forwards to the child", inputPane, routeConds{}, ctrlT, keyDecision{kind: routeChild}},
		{"ctrl+t in normal is ignored", inputNormal, routeConds{}, ctrlT, keyDecision{kind: routeIgnored}},
		{"ctrl+t in detail is ignored (ctrl+] t)", inputDetail, routeConds{}, ctrlT, keyDecision{kind: routeIgnored}},
		{"ctrl+t in form goes to the component", inputForm, routeConds{}, ctrlT, keyDecision{kind: routeComponent}},
		{"ctrl+t in jump goes to the box", inputJump, routeConds{}, ctrlT, keyDecision{kind: routeComponent}},
		{"ctrl+t in confirm clean is swallowed", inputConfirmClean, routeConds{}, ctrlT, keyDecision{kind: routeIgnored}},

		// ctrl+\: the escalating chord and its pane guard.
		{"ctrl+backslash in pane forwards to the child", inputPane, routeConds{}, ctrlBackslash, keyDecision{kind: routeChild}},
		{"ctrl+backslash in normal with a pane is ignored (ctrl+] \\)", inputNormal, routeConds{paneOpen: true}, ctrlBackslash, keyDecision{kind: routeIgnored}},
		{"ctrl+backslash in normal without a pane is ignored", inputNormal, routeConds{}, ctrlBackslash, keyDecision{kind: routeIgnored}},
		{"ctrl+backslash in detail is ignored (ctrl+] \\)", inputDetail, routeConds{}, ctrlBackslash, keyDecision{kind: routeIgnored}},
		{"ctrl+backslash in form goes to the component", inputForm, routeConds{}, ctrlBackslash, keyDecision{kind: routeComponent}},
		{"ctrl+backslash in confirm clean is swallowed", inputConfirmClean, routeConds{}, ctrlBackslash, keyDecision{kind: routeIgnored}},

		// esc, per mode.
		{"esc in confirm clean cancels", inputConfirmClean, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"esc in confirm restart cancels", inputConfirmRestart, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"esc in edit label cancels", inputEditLabel, routeConds{}, esc, keyDecision{routeAction, actionCancel}},
		{"esc in spawn error dismisses", inputSpawnError, routeConds{}, esc, keyDecision{routeAction, actionDismiss}},
		{"esc in form is the form-cancel gesture", inputForm, routeConds{}, esc, keyDecision{routeAction, actionFormCancel}},
		{"esc in jump goes to the component (two-stage)", inputJump, routeConds{}, esc, keyDecision{kind: routeComponent}},
		{"esc in normal is the key table's (go back)", inputNormal, routeConds{}, esc, keyDecision{kind: routeIgnored}},
		{"esc in detail closes the overlay", inputDetail, routeConds{}, esc, keyDecision{routeAction, actionDetailsClose}},
		{"esc in pane forwards to the child", inputPane, routeConds{}, esc, keyDecision{kind: routeChild}},

		// enter, per mode. In normal mode Enter and f are two physical keys for
		// the same current-view focus action; applyKeyAction chooses the rendered
		// pane or detail surface from detailDrawn().
		{"enter in normal is the key table's (go in)", inputNormal, routeConds{}, enter, keyDecision{kind: routeIgnored}},
		{"enter in detail is ignored", inputDetail, routeConds{}, enter, keyDecision{kind: routeIgnored}},
		{"enter in spawn error dismisses", inputSpawnError, routeConds{}, enter, keyDecision{routeAction, actionDismiss}},
		{"enter in edit label saves", inputEditLabel, routeConds{}, enter, keyDecision{routeAction, actionSubmit}},
		{"enter in form is the submit gesture", inputForm, routeConds{}, enter, keyDecision{routeAction, actionFormSubmit}},
		{"enter in jump goes to the component", inputJump, routeConds{}, enter, keyDecision{kind: routeComponent}},
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
		{"n in normal is ignored (ctrl+] n)", inputNormal, routeConds{}, press('n', "n"), keyDecision{kind: routeIgnored}},
		{"y in normal is ignored", inputNormal, routeConds{}, yLower, keyDecision{kind: routeIgnored}},
		{"y in edit label goes to the textinput", inputEditLabel, routeConds{}, yLower, keyDecision{kind: routeComponent}},

		// q: quit only in normal and detail.
		{"q in normal is ignored (ctrl+] q)", inputNormal, routeConds{}, q, keyDecision{kind: routeIgnored}},
		{"q in detail quits (the overlay's own rule)", inputDetail, routeConds{}, q, keyDecision{routeAction, actionQuit}},
		{"q in jump types (component)", inputJump, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"q in form types (component)", inputForm, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"q in pane forwards to the child", inputPane, routeConds{}, q, keyDecision{kind: routeChild}},
		{"q in confirm clean is swallowed", inputConfirmClean, routeConds{}, q, keyDecision{kind: routeIgnored}},

		// Navigation and stray text.
		{"up in normal moves the cursor", inputNormal, routeConds{}, up, keyDecision{routeAction, actionCursorUp}},
		{"up in detail scrolls the viewport", inputDetail, routeConds{}, up, keyDecision{routeAction, actionDetailScrollUp}},
		{"up in jump goes to the box", inputJump, routeConds{}, up, keyDecision{kind: routeComponent}},
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
		{"b in normal is ignored (ctrl+] b)", inputNormal, routeConds{}, press('b', "b"), keyDecision{kind: routeIgnored}},
		{"b in detail is ignored", inputDetail, routeConds{}, press('b', "b"), keyDecision{kind: routeIgnored}},
		{"b in pane forwards to the child", inputPane, routeConds{}, press('b', "b"), keyDecision{kind: routeChild}},
		{"b in pane forwards to the child even with a pane open", inputPane, routeConds{paneOpen: true}, press('b', "b"), keyDecision{kind: routeChild}},
		{"b in form types (component)", inputForm, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"b in jump types (component)", inputJump, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"b in edit label goes to the textinput", inputEditLabel, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"b in confirm clean is swallowed", inputConfirmClean, routeConds{}, press('b', "b"), keyDecision{kind: routeIgnored}},
		// i: the inbox overlay, on the same terms as b.
		{"i in normal is ignored (ctrl+] i)", inputNormal, routeConds{}, press('i', "i"), keyDecision{kind: routeIgnored}},
		{"i in detail is ignored", inputDetail, routeConds{}, press('i', "i"), keyDecision{kind: routeIgnored}},
		{"i in pane forwards to the child", inputPane, routeConds{}, press('i', "i"), keyDecision{kind: routeChild}},
		{"i in pane forwards to the child even with a pane open", inputPane, routeConds{paneOpen: true}, press('i', "i"), keyDecision{kind: routeChild}},
		{"i in form types (component)", inputForm, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"i in jump types (component)", inputJump, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"i in edit label goes to the textinput", inputEditLabel, routeConds{}, press('i', "i"), keyDecision{kind: routeComponent}},
		{"i in confirm clean is swallowed", inputConfirmClean, routeConds{}, press('i', "i"), keyDecision{kind: routeIgnored}},
		// P and U: pause and resume the build, on the same terms as b and i. The
		// lower-case letters keep their own meanings, which the last four rows
		// pin: a rule matching without regard to case would turn the project
		// jump's key into a pause.
		{"P in normal is ignored (ctrl+] P)", inputNormal, routeConds{}, press('P', "P"), keyDecision{kind: routeIgnored}},
		{"P in detail is ignored", inputDetail, routeConds{}, press('P', "P"), keyDecision{kind: routeIgnored}},
		{"P in pane forwards to the child", inputPane, routeConds{}, press('P', "P"), keyDecision{kind: routeChild}},
		{"P in pane forwards to the child even with a pane open", inputPane, routeConds{paneOpen: true}, press('P', "P"), keyDecision{kind: routeChild}},
		{"P in inbox is text (component)", inputInbox, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in form types (component)", inputForm, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in jump goes to the component", inputJump, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in edit label goes to the textinput", inputEditLabel, routeConds{}, press('P', "P"), keyDecision{kind: routeComponent}},
		{"P in confirm clean is swallowed", inputConfirmClean, routeConds{}, press('P', "P"), keyDecision{kind: routeIgnored}},
		{"U in normal is ignored (ctrl+] U)", inputNormal, routeConds{}, press('U', "U"), keyDecision{kind: routeIgnored}},
		{"U in detail is ignored", inputDetail, routeConds{}, press('U', "U"), keyDecision{kind: routeIgnored}},
		{"U in pane forwards to the child", inputPane, routeConds{}, press('U', "U"), keyDecision{kind: routeChild}},
		{"U in pane forwards to the child even with a pane open", inputPane, routeConds{paneOpen: true}, press('U', "U"), keyDecision{kind: routeChild}},
		{"U in inbox is text (component)", inputInbox, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in form types (component)", inputForm, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in jump goes to the component", inputJump, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in edit label goes to the textinput", inputEditLabel, routeConds{}, press('U', "U"), keyDecision{kind: routeComponent}},
		{"U in confirm clean is swallowed", inputConfirmClean, routeConds{}, press('U', "U"), keyDecision{kind: routeIgnored}},
		{"p in normal is ignored (ctrl+] p)", inputNormal, routeConds{}, press('p', "p"), keyDecision{kind: routeIgnored}},
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
		// The plan check holds nothing typed, so ctrl+c quits over it as over
		// the jump search; every other key, the leader included, is the
		// overlay's, which is what keeps the dialog shut while it is up.
		{"ctrl+c in plan check quits", inputPlanCheck, routeConds{}, ctrlC, keyDecision{kind: routeAction, action: actionQuit}},
		{"esc in plan check goes to the component", inputPlanCheck, routeConds{}, esc, keyDecision{kind: routeComponent}},
		{"q in plan check goes to the component", inputPlanCheck, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"ctrl+] in plan check goes to the component", inputPlanCheck, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},
		{"pgdown in plan check goes to the component", inputPlanCheck, routeConds{}, tea.KeyPressMsg{Code: tea.KeyPgDown}, keyDecision{kind: routeComponent}},
		// The tokens overlay is the plan check's shape (T7.12).
		{"ctrl+c in task tokens quits", inputTaskTokens, routeConds{}, ctrlC, keyDecision{kind: routeAction, action: actionQuit}},
		{"esc in task tokens goes to the component", inputTaskTokens, routeConds{}, esc, keyDecision{kind: routeComponent}},
		{"q in task tokens goes to the component", inputTaskTokens, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"ctrl+] in task tokens goes to the component", inputTaskTokens, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},
		{"pgdown in task tokens goes to the component", inputTaskTokens, routeConds{}, tea.KeyPressMsg{Code: tea.KeyPgDown}, keyDecision{kind: routeComponent}},
		// The all-projects screen enumerates its keys and ignores the rest,
		// as detail mode does; the leader is a rule there (pause and resume
		// act on its rows through the dialog), unlike in the overlays.
		{"ctrl+] in all projects is no rule", inputAllProjects, routeConds{}, ctrlBracket, keyDecision{kind: routeIgnored}},
		{"ctrl+c in all projects is the key table's (quit)", inputAllProjects, routeConds{}, ctrlC, keyDecision{kind: routeIgnored}},
		{"esc in all projects is the key table's", inputAllProjects, routeConds{}, esc, keyDecision{kind: routeIgnored}},
		{"enter in all projects is the key table's (go in)", inputAllProjects, routeConds{}, enter, keyDecision{kind: routeIgnored}},
		{"k in all projects moves up", inputAllProjects, routeConds{}, press('k', "k"), keyDecision{routeAction, actionAllProjectsUp}},
		{"j in all projects moves down", inputAllProjects, routeConds{}, press('j', "j"), keyDecision{routeAction, actionAllProjectsDown}},
		{"pgdown in all projects pages", inputAllProjects, routeConds{}, tea.KeyPressMsg{Code: tea.KeyPgDown}, keyDecision{routeAction, actionAllProjectsPageDown}},
		{"home in all projects goes to the top", inputAllProjects, routeConds{}, tea.KeyPressMsg{Code: tea.KeyHome}, keyDecision{routeAction, actionAllProjectsTop}},
		{"q in all projects is ignored", inputAllProjects, routeConds{}, q, keyDecision{kind: routeIgnored}},
		{"P in all projects is ignored (pause is under the leader)", inputAllProjects, routeConds{}, press('P', "P"), keyDecision{kind: routeIgnored}},
		{"b in inbox is text (component)", inputInbox, routeConds{}, press('b', "b"), keyDecision{kind: routeComponent}},
		{"ctrl+t in inbox goes to the component", inputInbox, routeConds{}, ctrlT, keyDecision{kind: routeComponent}},
		{"ctrl+backslash in inbox goes to the component", inputInbox, routeConds{paneOpen: true}, ctrlBackslash, keyDecision{kind: routeComponent}},
		{"pgup in inbox goes to the component", inputInbox, routeConds{}, tea.KeyPressMsg{Code: tea.KeyPgUp}, keyDecision{kind: routeComponent}},
		// Five digits: the five session tabs in a git-spice repo. The sixth
		// was the Build tab's until T16.4 made Build a section, so it is no
		// key in the overview now.
		{"5 in detail jumps to a tab", inputDetail, routeConds{}, press('5', "5"), keyDecision{routeAction, actionDetailTabJump}},
		{"6 in detail is ignored", inputDetail, routeConds{}, press('6', "6"), keyDecision{kind: routeIgnored}},
		{"6 in normal is ignored", inputNormal, routeConds{}, press('6', "6"), keyDecision{kind: routeIgnored}},

		// ctrl+w: bound nowhere since resize mode went (W3.T1 of T16.2); a
		// focused pane forwards it to the child (delete-word in a shell).
		{"ctrl+w in normal is ignored (ctrl+] w)", inputNormal, routeConds{}, ctrlW, keyDecision{kind: routeIgnored}},
		{"ctrl+w in detail is ignored", inputDetail, routeConds{}, ctrlW, keyDecision{kind: routeIgnored}},
		{"ctrl+w in pane forwards to the child", inputPane, routeConds{}, ctrlW, keyDecision{kind: routeChild}},
		{"ctrl+w in form goes to the component", inputForm, routeConds{}, ctrlW, keyDecision{kind: routeComponent}},
		{"ctrl+w in jump goes to the box", inputJump, routeConds{}, ctrlW, keyDecision{kind: routeComponent}},
		{"ctrl+w in confirm clean is swallowed", inputConfirmClean, routeConds{}, ctrlW, keyDecision{kind: routeIgnored}},

		{"h in normal is ignored", inputNormal, routeConds{}, press('h', "h"), keyDecision{kind: routeIgnored}},

		// The keys overlay (`?`): ctrl+c is its one parent rule; q, esc and
		// the leader are the overlay's (its filter types q, esc steps back).
		{"ctrl+c in keys quits", inputKeys, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"q in keys goes to the overlay", inputKeys, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"esc in keys goes to the overlay", inputKeys, routeConds{}, esc, keyDecision{kind: routeComponent}},
		{"ctrl+] in keys goes to the overlay", inputKeys, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},

		// ctrl+], the leader: a rule in normal, detail and pane mode only.
		{"ctrl+] in normal is no rule (decideKey: nothing at Project)", inputNormal, routeConds{}, ctrlBracket, keyDecision{kind: routeIgnored}},
		{"ctrl+] in detail is no rule (decideKey: nothing at Project)", inputDetail, routeConds{}, ctrlBracket, keyDecision{kind: routeIgnored}},
		{"ctrl+] in pane is no rule (decideKey opens the strip first)", inputPane, routeConds{}, ctrlBracket, keyDecision{kind: routeChild}},
		{"ctrl+] in leader goes to the dialog", inputLeader, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},
		{"ctrl+] in jump goes to the component", inputJump, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},
		{"ctrl+] in inbox goes to the component", inputInbox, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},
		{"ctrl+] in form goes to the component", inputForm, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},
		{"ctrl+] in edit label goes to the textinput", inputEditLabel, routeConds{}, ctrlBracket, keyDecision{kind: routeComponent}},
		{"ctrl+] in confirm clean is swallowed", inputConfirmClean, routeConds{}, ctrlBracket, keyDecision{kind: routeIgnored}},
		{"ctrl+] in confirm restart is swallowed", inputConfirmRestart, routeConds{}, ctrlBracket, keyDecision{kind: routeIgnored}},
		{"ctrl+] in spawn error is swallowed", inputSpawnError, routeConds{}, ctrlBracket, keyDecision{kind: routeIgnored}},
		{"ctrl+] in provider picker is swallowed", inputPickProvider, routeConds{}, ctrlBracket, keyDecision{kind: routeIgnored}},
		// ctrl+space, the leader before T9.1, is an ordinary key: it opens
		// the leader nowhere, and the pane hands it to the agent.
		{"ctrl+space in normal opens nothing", inputNormal, routeConds{}, ctrlSpace, keyDecision{kind: routeIgnored}},
		{"ctrl+space in detail opens nothing", inputDetail, routeConds{}, ctrlSpace, keyDecision{kind: routeIgnored}},
		{"ctrl+space in pane forwards to the child", inputPane, routeConds{}, ctrlSpace, keyDecision{kind: routeChild}},
		{"ctrl+space in all projects opens nothing", inputAllProjects, routeConds{}, ctrlSpace, keyDecision{kind: routeIgnored}},
		// The actions search's one parent rule is ctrl+c: q is a letter of
		// the query (§4.7), esc closes it there.
		{"q in the search types (component)", inputLeader, routeConds{}, q, keyDecision{kind: routeComponent}},
		{"ctrl+c in the search quits", inputLeader, routeConds{}, ctrlC, keyDecision{routeAction, actionQuit}},
		{"esc in leader goes to the dialog", inputLeader, routeConds{}, esc, keyDecision{kind: routeComponent}},
		{"ctrl+g in leader goes to the dialog", inputLeader, routeConds{}, ctrlG, keyDecision{kind: routeComponent}},

		// The rest of the removed list keys: every one ignored by the rule
		// table (dispatchKey turns them into a notice afterwards), and the
		// one-letter keys the leader table gained are nothing on the list.
		{"? in normal is ignored (ctrl+] ?)", inputNormal, routeConds{}, press('?', "?"), keyDecision{kind: routeIgnored}},
		{"/ in normal is ignored (ctrl+] /)", inputNormal, routeConds{}, press('/', "/"), keyDecision{kind: routeIgnored}},
		{": in normal is ignored (the palette is gone)", inputNormal, routeConds{}, press(':', ":"), keyDecision{kind: routeIgnored}},
		{"f in normal is ignored (ctrl+] f)", inputNormal, routeConds{}, press('f', "f"), keyDecision{kind: routeIgnored}},
		{"c in normal is ignored (ctrl+] c)", inputNormal, routeConds{}, press('c', "c"), keyDecision{kind: routeIgnored}},
		{"r in normal is ignored (ctrl+] r)", inputNormal, routeConds{}, press('r', "r"), keyDecision{kind: routeIgnored}},
		{"r in detail is ignored (ctrl+] r)", inputDetail, routeConds{}, press('r', "r"), keyDecision{kind: routeIgnored}},
		{"l in normal is ignored (ctrl+] l)", inputNormal, routeConds{}, press('l', "l"), keyDecision{kind: routeIgnored}},
		{"o in normal is ignored (ctrl+] o)", inputNormal, routeConds{}, press('o', "o"), keyDecision{kind: routeIgnored}},
		{"t in normal is ignored", inputNormal, routeConds{}, press('t', "t"), keyDecision{kind: routeIgnored}},
		{"j in normal still moves the cursor", inputNormal, routeConds{}, press('j', "j"), keyDecision{routeAction, actionCursorDown}},
		{"k in normal still moves the cursor", inputNormal, routeConds{}, press('k', "k"), keyDecision{routeAction, actionCursorUp}},
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
// from: the answers each modal accepts, the jump search's component-owned keys,
// and the form's gestures must be readable off the mode's keymap. The
// jump's enter and esc rows are load-bearing: the box stages them in
// jumpKeyDecision, not as rules, so this keymap is the only record the
// footer can name them from.
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
		{"plan form advertises enter", inputPlanText, "enter"},
		{"plan form advertises esc", inputPlanText, "esc"},
		{"plan open offer advertises y", inputConfirmPlanOpen, "y"},
		{"plan open offer advertises n", inputConfirmPlanOpen, "n"},
		{"plan open offer advertises esc", inputConfirmPlanOpen, "esc"},
		{"jump advertises enter", inputJump, "enter"},
		{"jump advertises esc", inputJump, "esc"},
		{"jump advertises up", inputJump, "up"},
		{"form advertises tab", inputForm, "tab"},
		{"form advertises shift+tab", inputForm, "shift+tab"},
		{"form advertises esc", inputForm, "esc"},
		// The keys bubbles/list brought with it. Wave 6 reads availability off
		// this data, so a gesture the component owns but the keymap omits is a
		// help and palette blind spot rather than a compile error.
		{"normal advertises pgup", inputNormal, "pgup"},
		{"normal advertises pgdown", inputNormal, "pgdown"},
		{"normal advertises home", inputNormal, "home"},
		{"normal advertises end", inputNormal, "end"},
		// The inbox overlay's keys are all the component's, so this keymap is
		// the only structured record of them, for both of its screens.
		{"inbox advertises enter", inputInbox, "enter"},
		{"inbox advertises esc", inputInbox, "esc"},
		{"inbox advertises up", inputInbox, "up"},
		{"inbox advertises down", inputInbox, "down"},
		{"inbox advertises pgup", inputInbox, "pgup"},
		{"inbox advertises pgdown", inputInbox, "pgdown"},
		{"inbox advertises ctrl+g", inputInbox, "ctrl+g"},
		// The plan-check overlay's scroll keys and esc are the component's,
		// so this keymap is their only structured record.
		{"plan check advertises esc", inputPlanCheck, "esc"},
		{"plan check advertises up", inputPlanCheck, "up"},
		{"plan check advertises down", inputPlanCheck, "down"},
		{"plan check advertises pgup", inputPlanCheck, "pgup"},
		{"plan check advertises pgdown", inputPlanCheck, "pgdown"},
		{"plan check advertises home", inputPlanCheck, "home"},
		{"plan check advertises end", inputPlanCheck, "end"},
		{"plan check advertises ctrl+c", inputPlanCheck, "ctrl+c"},
		// The tokens overlay's keys are the plan check's (T7.12).
		{"task tokens advertises esc", inputTaskTokens, "esc"},
		{"task tokens advertises up", inputTaskTokens, "up"},
		{"task tokens advertises down", inputTaskTokens, "down"},
		{"task tokens advertises pgup", inputTaskTokens, "pgup"},
		{"task tokens advertises pgdown", inputTaskTokens, "pgdown"},
		{"task tokens advertises ctrl+c", inputTaskTokens, "ctrl+c"},
		// The all-projects screen's own keys are its moves; enter, esc and
		// the action keys are the key table's, which its footer is built
		// from (renderAllProjectsHelpKeyMap, TestHintsNameTheKeys).
		{"all projects advertises up", inputAllProjects, "up"},
		{"all projects advertises down", inputAllProjects, "down"},
		// The leader is the one dashboard key the agent pane and the key
		// strip advertise; the list's and the overview's footers come from
		// the key table since T16.2.
		{"pane advertises ctrl+]", inputPane, "ctrl+]"},
		{"key strip advertises ctrl+]", inputKeyStrip, "ctrl+]"},
		// The `:` search's own gestures are the component's, so this keymap
		// is their only structured record; ctrl+c is its one rule.
		{"leader advertises enter", inputLeader, "enter"},
		{"leader advertises esc", inputLeader, "esc"},
		{"leader advertises up", inputLeader, "up"},
		{"leader advertises ctrl+c", inputLeader, "ctrl+c"},
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
