package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/termpane"
)

// leaderKeyPress is ctrl+space as the program receives it: ultraviolet decodes
// the NUL byte to {Code: KeySpace, Mod: ModCtrl}, with no Text.
var leaderKeyPress = tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl}

// leaderNUL is what the pane must receive when ctrl+space is passed on to the
// agent: the emulator's ctrl+space key event, which x/vt encodes as NUL.
var leaderNUL = termpane.KeyEvent{Code: vt.KeySpace, Mod: vt.ModCtrl}

// leaderPlaceholderModel is the right side focused on the agent placeholder:
// a row with a live host, the agent view, and no pane installed for it.
func leaderPlaceholderModel(t *testing.T) Model {
	t.Helper()
	m := testModel(runningSess("a"))
	m.view = viewAgent
	m.focus = focusRight
	if !m.agentPlaceholderDrawn() {
		t.Fatal("setup: the agent placeholder is not what the right side draws")
	}
	return m
}

// sentToPane is everything a fake pane has been given: keys, text and pastes.
func sentToPane(fp *fakePane) int { return len(fp.keys) + len(fp.texts) + len(fp.pastes) }

// TestLeaderOpensTheDialog drives ctrl+space through Update from each place
// it is a rule. Load-bearing rows:
//   - "from a focused agent pane": the pane's mode forwards every unclaimed
//     key to the agent, so this row is what proves the leader is claimed
//     there at all, and that the key was not ALSO typed to the agent.
//   - "from the agent placeholder": dispatchKey hands the placeholder's keys
//     to placeholderKey before the detail rules, so the leader has to be let
//     through there separately; without it this row stays closed.
//   - openedFromPane: the dialog's own mode outranks the pane's, so it is
//     recorded at open, and only the pane row may set it — it is what makes a
//     second ctrl+space reach the agent.
func TestLeaderOpensTheDialog(t *testing.T) {
	cases := []struct {
		name     string
		model    func(t *testing.T) (Model, *fakePane)
		from     inputMode
		fromPane bool
	}{
		{
			name: "from the session list",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				m := testModel(sess("a"))
				m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
				return m, fp
			},
			from: inputNormal,
		},
		{
			name: "from the focused overview",
			model: func(t *testing.T) (Model, *fakePane) {
				m := focusOverview(t, detailFocusFixture(t, "alpha", "alpha"))
				return m, m.pane.(*fakePane)
			},
			from: inputDetail,
		},
		{
			name: "from the agent placeholder",
			model: func(t *testing.T) (Model, *fakePane) {
				return leaderPlaceholderModel(t), nil
			},
			from: inputDetail,
		},
		{
			name: "from a focused agent pane",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				return focusedModel(t, fp), fp
			},
			from:     inputPane,
			fromPane: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t)
			got, cmd := update(t, m, leaderKeyPress)
			if !got.leaderOpen {
				t.Fatal("ctrl+space did not open the actions dialog")
			}
			if mode := activeInputMode(got.modeSnapshot()); mode != inputLeader {
				t.Fatalf("the keyboard is with %v, want the dialog", mode)
			}
			if cmd != nil {
				t.Fatalf("opening the dialog returned a command (%T); it must be pure state", cmd())
			}
			if got.leaderFrom != tc.from {
				t.Errorf("leaderFrom = %v, want %v", got.leaderFrom, tc.from)
			}
			if got.leader.openedFromPane != tc.fromPane {
				t.Errorf("openedFromPane = %v, want %v", got.leader.openedFromPane, tc.fromPane)
			}
			if len(got.leader.rows) == 0 {
				t.Error("the dialog opened with no rows")
			}
			if fp != nil && sentToPane(fp) != 0 {
				t.Errorf("opening the dialog sent %d inputs to the pane, want none", sentToPane(fp))
			}
			if !strings.Contains(render(got), "type to filter, or press an action's key") {
				t.Error("the dialog is not on screen")
			}
		})
	}
}

// TestLeaderIgnoredUnderEveryOverlay pins that ctrl+space opens nothing while
// an overlay, a form, a prompt or a confirmation has the keyboard, and that
// the surface it lands on is left as it was. Every row is load-bearing in the
// same way: the leader is a rule only in normal, detail and pane mode, and a
// global intercept added later would turn exactly one of these red. The text
// rows (form, label, filter) also check the key typed nothing: ctrl+space has
// no text, so a field that grew a character would mean the key was mistaken
// for a space.
func TestLeaderIgnoredUnderEveryOverlay(t *testing.T) {
	cases := []struct {
		name string
		open func(t *testing.T, m Model) Model
		mode inputMode
		text func(m Model) string
	}{
		{
			name: "palette",
			open: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyRune(':')); return m },
			mode: inputPalette,
			text: func(m Model) string { return m.palette.input.Value() },
		},
		{
			name: "switcher",
			open: func(t *testing.T, m Model) Model {
				m.projects = []project.Project{{Path: "/repo", Name: "api"}, {Path: "/repos/afk", Name: "afk"}}
				m.activeProject = 0
				return openSwitcherOverlay(t, m)
			},
			mode: inputSwitcher,
		},
		{
			name: "inbox",
			open: func(t *testing.T, m Model) Model {
				m.buildKnown = true
				m, _ = update(t, m, keyRune('i'))
				return m
			},
			mode: inputInbox,
		},
		{
			name: "new-session form",
			open: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyRune('n')); return m },
			mode: inputForm,
			text: func(m Model) string { return m.form.nameInput.Value() },
		},
		{
			name: "label edit",
			open: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyRune('l')); return m },
			mode: inputEditLabel,
			text: func(m Model) string { return m.labelInput.Value() },
		},
		{
			name: "clean confirm",
			open: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyRune('c')); return m },
			mode: inputConfirmClean,
		},
		{
			// Reaching it through `r` needs a wedged row; the modal field is
			// the whole of the state its mode is derived from.
			name: "restart confirm",
			open: func(t *testing.T, m Model) Model { m.modal = inputConfirmRestart; return m },
			mode: inputConfirmRestart,
		},
		{
			name: "filter prompt",
			open: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyRune('/')); return m },
			mode: inputFilter,
			text: func(m Model) string { return m.list.FilterInput.Value() },
		},
		{
			name: "resize",
			open: func(t *testing.T, m Model) Model { m, _ = update(t, m, chord('w')); return m },
			mode: inputResize,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.open(t, testModel(sess("a"), sess("b")))
			if mode := activeInputMode(m.modeSnapshot()); mode != tc.mode {
				t.Fatalf("setup: the keyboard is with %v, want %v", mode, tc.mode)
			}
			var before string
			if tc.text != nil {
				before = tc.text(m)
			}
			got, _ := update(t, m, leaderKeyPress)
			if got.leaderOpen {
				t.Fatalf("ctrl+space opened the actions dialog over the %s", tc.name)
			}
			if mode := activeInputMode(got.modeSnapshot()); mode != tc.mode {
				t.Fatalf("ctrl+space moved the keyboard from %v to %v; the %s must stay open", tc.mode, mode, tc.name)
			}
			if tc.text != nil {
				if after := tc.text(got); after != before {
					t.Fatalf("ctrl+space changed the %s's text from %q to %q", tc.name, before, after)
				}
			}
		})
	}
}

// TestLeaderRunsAnActionOnce pins that a key in the dialog runs its action
// exactly once and closes the dialog. Load-bearing rows:
//   - "ctrl+g toggles the mouse once" is the "once" proof: run twice, the
//     toggle would land back where it started and the test could not tell
//     it from not running at all, so it checks the flip itself.
//   - "b shows the Build tab": a list key that moves the keyboard right on
//     its own (showBuildTab); run from the dialog it must do the same.
//   - "n from the agent pane opens the form": a list action picked while
//     the agent had the keyboard runs as from the list — the keyboard goes
//     to the list — and nothing at all is typed to the agent.
//   - "ctrl+t from the agent pane": the pane has its own rule for it, so it
//     runs as the pane's key does and the keyboard stays on the right.
func TestLeaderRunsAnActionOnce(t *testing.T) {
	cases := []struct {
		name  string
		model func(t *testing.T) (Model, *fakePane)
		key   tea.KeyPressMsg
		check func(t *testing.T, before, after Model)
	}{
		{
			name:  "ctrl+g toggles the mouse once",
			model: func(t *testing.T) (Model, *fakePane) { return testModel(sess("a")), nil },
			key:   chord('g'),
			check: func(t *testing.T, before, after Model) {
				if after.mouseCapture == before.mouseCapture {
					t.Fatalf("mouseCapture = %v, want it flipped once", after.mouseCapture)
				}
			},
		},
		{
			name: "b shows the Build tab",
			model: func(t *testing.T) (Model, *fakePane) {
				return withBuild(t, testModel(sess("alpha")), buildTabSnap()), nil
			},
			key: keyB.(tea.KeyPressMsg),
			check: func(t *testing.T, _, after Model) {
				if after.detailTab != tabBuild || after.view != viewOverview || after.focus != focusRight {
					t.Fatalf("tab %v, view %v, focus %v; want the Build tab focused", after.detailTab, after.view, after.focus)
				}
			},
		},
		{
			name:  "n opens the new-session form",
			model: func(t *testing.T) (Model, *fakePane) { return testModel(sess("a")), nil },
			key:   keyRune('n').(tea.KeyPressMsg),
			check: func(t *testing.T, _, after Model) {
				if !after.formOpen {
					t.Fatal("n did not open the form")
				}
			},
		},
		{
			name: "n from the agent pane opens the form with the list focused",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				return focusedModel(t, fp), fp
			},
			key: keyRune('n').(tea.KeyPressMsg),
			check: func(t *testing.T, _, after Model) {
				if !after.formOpen {
					t.Fatal("n did not open the form")
				}
				if after.focus != focusList {
					t.Fatalf("focus = %v, want the list: a list action runs as from the list", after.focus)
				}
				if after.pane == nil {
					t.Fatal("the pane was closed; only the close-pane action may close it")
				}
			},
		},
		{
			name: "ctrl+t from the agent pane switches to the overview and keeps the right side",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				return focusedModel(t, fp), fp
			},
			key: chord('t'),
			check: func(t *testing.T, _, after Model) {
				if after.view != viewOverview || after.focus != focusRight {
					t.Fatalf("view %v, focus %v; want the overview with the right side focused", after.view, after.focus)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t)
			m, _ = update(t, m, leaderKeyPress)
			if !m.leaderOpen {
				t.Fatal("setup: ctrl+space did not open the dialog")
			}
			got, _ := update(t, m, tc.key)
			if got.leaderOpen {
				t.Fatal("the dialog stayed open after running an action")
			}
			tc.check(t, m, got)
			if fp != nil && sentToPane(fp) != 0 {
				t.Errorf("the pane was sent %d inputs; an action run from the dialog must reach the agent never", sentToPane(fp))
			}
		})
	}
}

// TestLeaderEnterRunsTheHighlightedRow moves the highlight and presses enter.
// The row under the highlight is looked up rather than assumed, so a change
// in the dialog's order moves the test with it instead of breaking it.
func TestLeaderEnterRunsTheHighlightedRow(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, leaderKeyPress)
	target := -1
	for i, r := range m.leader.visible() {
		if r.action == actionNewSession {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("setup: the dialog has no new-session row")
	}
	for range target {
		m, _ = update(t, m, keyDown)
	}
	if m.leader.highlight != target {
		t.Fatalf("setup: highlight = %d, want %d", m.leader.highlight, target)
	}
	got, _ := update(t, m, keyEnter)
	if got.leaderOpen {
		t.Fatal("enter left the dialog open")
	}
	if !got.formOpen {
		t.Fatal("enter on the new-session row did not open the form")
	}
}

// TestLeaderDimmedKeyDoesNothing presses the key of a row that cannot run:
// `c` with no session selected. The dialog must stay open and nothing run —
// a dimmed row's key is not a way round its own reason.
func TestLeaderDimmedKeyDoesNothing(t *testing.T) {
	m := testModel()
	m, _ = update(t, m, leaderKeyPress)
	var clean leaderRow
	for _, r := range m.leader.rows {
		if r.action == actionClean {
			clean = r
		}
	}
	if clean.blocked == "" {
		t.Fatal("setup: the clean row is not dimmed with no session selected")
	}
	got, cmd := update(t, m, keyRune('c'))
	if !got.leaderOpen {
		t.Fatal("a dimmed row's key closed the dialog")
	}
	if got.modal != inputNormal || cmd != nil {
		t.Fatalf("a dimmed row's key ran something: modal %v, cmd %v", got.modal, cmd != nil)
	}
}

// TestLeaderPassThrough pins what reaches the agent when the dialog closes.
// Load-bearing rows:
//   - "ctrl+space twice in the pane" is how the agent keeps the key the
//     leader takes: exactly one NUL key event, no text.
//   - "esc from the pane" closes and sends nothing: esc is an agent key too
//     (it interrupts Claude), and the dialog must not forward its own close.
//   - "ctrl+space twice from the list" sends nothing even with a pane
//     installed: the list was not talking to the agent.
func TestLeaderPassThrough(t *testing.T) {
	cases := []struct {
		name     string
		focused  bool
		keys     []tea.KeyPressMsg
		wantKeys []termpane.KeyEvent
		wantPane bool // the pane still has the keyboard afterwards
	}{
		{
			name:     "ctrl+space twice in the pane",
			focused:  true,
			keys:     []tea.KeyPressMsg{leaderKeyPress, leaderKeyPress},
			wantKeys: []termpane.KeyEvent{leaderNUL},
			wantPane: true,
		},
		{
			name:     "esc from the pane",
			focused:  true,
			keys:     []tea.KeyPressMsg{leaderKeyPress, {Code: tea.KeyEsc}},
			wantPane: true,
		},
		{
			name: "ctrl+space twice from the list",
			keys: []tea.KeyPressMsg{leaderKeyPress, leaderKeyPress},
		},
		{
			name: "esc from the list",
			keys: []tea.KeyPressMsg{leaderKeyPress, {Code: tea.KeyEsc}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePane("X")
			var m Model
			if tc.focused {
				m = focusedModel(t, fp)
			} else {
				m = testModel(sess("a"))
				m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
			}
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			if m.leaderOpen {
				t.Fatal("the dialog is still open")
			}
			if len(fp.texts) != 0 || len(fp.pastes) != 0 {
				t.Fatalf("the pane was sent text %q / pastes %q, want none", fp.texts, fp.pastes)
			}
			if len(fp.keys) != len(tc.wantKeys) {
				t.Fatalf("the pane was sent keys %+v, want %+v", fp.keys, tc.wantKeys)
			}
			for i := range tc.wantKeys {
				if fp.keys[i] != tc.wantKeys[i] {
					t.Fatalf("key %d = %+v, want %+v", i, fp.keys[i], tc.wantKeys[i])
				}
			}
			if m.paneOwnsKeyboard() != tc.wantPane {
				t.Fatalf("paneOwnsKeyboard = %v, want %v", m.paneOwnsKeyboard(), tc.wantPane)
			}
		})
	}
}
