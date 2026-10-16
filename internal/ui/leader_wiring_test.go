package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/termpane"
)

// leaderKeyPress is ctrl+] as the program receives it: ultraviolet decodes
// the byte 0x1D (and a kitty-protocol CSI 93;5u) to {Code: ']', Mod: ModCtrl},
// with no Text (docs/decisions.md "## T9.1").
var leaderKeyPress = tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}

// leaderPassedOn is what the pane must receive when ctrl+] is passed on to
// the agent: the emulator's ctrl+] key event, which x/vt encodes as 0x1D.
var leaderPassedOn = termpane.KeyEvent{Code: ']', Mod: vt.ModCtrl}

// ctrlSpacePress is Ctrl+Space as the program receives it: ultraviolet decodes
// the NUL byte to {Code: KeySpace, Mod: ModCtrl}. It was the leader before
// T9.1 and is an ordinary key now.
var ctrlSpacePress = tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl}

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

// pressLeader is ctrl+] then k, the way every dashboard action is run
// since T7.2. It fails the test if the dialog did not open, so a caller's
// later assertion is about k and never about a leader that went nowhere.
func pressLeader(t *testing.T, m Model, k tea.Msg) Model {
	t.Helper()
	m, _ = update(t, m, leaderKeyPress)
	if !m.leaderOpen {
		t.Fatal("setup: ctrl+] did not open the actions dialog")
	}
	m, _ = update(t, m, k)
	return m
}

// leaderFocusedPane is a pane hosting "a", focused with enter from the list
// (the one key left that moves the keyboard right without the leader).
func leaderFocusedPane(t *testing.T, fp *fakePane) Model {
	t.Helper()
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	m, _ = update(t, m, keyEnter)
	if !m.paneOwnsKeyboard() {
		t.Fatal("setup: enter did not focus the pane")
	}
	return m
}

// TestLeaderOpensTheDialog drives ctrl+] through Update from each place
// it is a rule. Load-bearing rows:
//   - "from a focused agent pane": the pane's mode forwards every unclaimed
//     key to the agent, so this row is what proves the leader is claimed
//     there at all, and that the key was not ALSO typed to the agent.
//   - "from the agent placeholder": dispatchKey hands the placeholder's keys
//     to placeholderKey before the detail rules, so the leader has to be let
//     through there separately; without it this row stays closed.
//   - openedFromPane: the dialog's own mode outranks the pane's, so it is
//     recorded at open, and only the pane row may set it — it is what makes a
//     second ctrl+] reach the agent.
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
				return leaderFocusedPane(t, fp), fp
			},
			from:     inputPane,
			fromPane: true,
		},
		{
			// T7.9: the all-projects screen keeps the leader, unlike the
			// overlays (TestLeaderIgnoredUnderEveryOverlay), because pause and
			// resume act on its rows through the dialog.
			name: "from the all-projects screen",
			model: func(t *testing.T) (Model, *fakePane) {
				m, _, _ := allProjectsModel(t)
				return openAllProjects(t, m), nil
			},
			from: inputAllProjects,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t)
			got, cmd := update(t, m, leaderKeyPress)
			if !got.leaderOpen {
				t.Fatal("ctrl+] did not open the actions dialog")
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

// TestLeaderIgnoredUnderEveryOverlay pins that ctrl+] opens nothing while
// an overlay, a form, a prompt or a confirmation has the keyboard, and that
// the surface it lands on is left as it was. Every row is load-bearing in the
// same way: the leader is a rule only in normal, detail and pane mode, and a
// global intercept added later would turn exactly one of these red. The text
// rows (form, label, filter) also check the key typed nothing: ctrl+] has
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
			name: "switcher",
			open: func(t *testing.T, m Model) Model {
				m.projects = []project.Project{{Path: "/repo", Name: "api"}, {Path: "/repos/afk", Name: "afk"}}
				m.activeProject = 0
				return pressLeader(t, m, keyRune('p'))
			},
			mode: inputSwitcher,
		},
		{
			name: "inbox",
			open: func(t *testing.T, m Model) Model {
				m.buildKnown = true
				return pressLeader(t, m, keyRune('i'))
			},
			mode: inputInbox,
		},
		{
			name: "plan check",
			open: func(t *testing.T, m Model) Model {
				m, _ = runPlanAction(t, withPlanStatus(t, m, planCheckStatusErrors()), "errors")
				return m
			},
			mode: inputPlanCheck,
		},
		{
			name: "new-session form",
			open: func(t *testing.T, m Model) Model { return pressLeader(t, m, keyRune('n')) },
			mode: inputForm,
			text: func(m Model) string { return m.form.nameInput.Value() },
		},
		{
			name: "label edit",
			open: func(t *testing.T, m Model) Model { return pressLeader(t, m, keyRune('l')) },
			mode: inputEditLabel,
			text: func(m Model) string { return m.labelInput.Value() },
		},
		{
			name: "clean confirm",
			open: func(t *testing.T, m Model) Model { return pressLeader(t, m, keyRune('c')) },
			mode: inputConfirmClean,
		},
		{
			// Reaching it through restart needs a wedged row; the modal field is
			// the whole of the state its mode is derived from.
			name: "restart confirm",
			open: func(t *testing.T, m Model) Model { m.modal = inputConfirmRestart; return m },
			mode: inputConfirmRestart,
		},
		{
			name: "filter prompt",
			open: func(t *testing.T, m Model) Model { return pressLeader(t, m, keyRune('/')) },
			mode: inputFilter,
			text: func(m Model) string { return m.list.FilterInput.Value() },
		},
		{
			name: "resize",
			open: func(t *testing.T, m Model) Model { return pressLeader(t, m, keyRune('w')) },
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
				t.Fatalf("ctrl+] opened the actions dialog over the %s", tc.name)
			}
			if mode := activeInputMode(got.modeSnapshot()); mode != tc.mode {
				t.Fatalf("ctrl+] moved the keyboard from %v to %v; the %s must stay open", tc.mode, mode, tc.name)
			}
			if tc.text != nil {
				if after := tc.text(got); after != before {
					t.Fatalf("ctrl+] changed the %s's text from %q to %q", tc.name, before, after)
				}
			}
		})
	}
}

// TestLeaderRunsAnActionOnce pins that a key in the dialog runs its action
// exactly once and closes the dialog. Load-bearing rows:
//   - "g toggles the mouse once" is the "once" proof: run twice, the
//     toggle would land back where it started and the test could not tell
//     it from not running at all, so it checks the flip itself.
//   - "b shows the Build tab": a list key that moves the keyboard right on
//     its own (showBuildTab); run from the dialog it must do the same.
//   - "n from the agent pane opens the form": a list action picked while
//     the agent had the keyboard runs as from the list — the keyboard goes
//     to the list — and nothing at all is typed to the agent.
//   - "t from the agent pane": a right-side action, so it runs where the
//     leader was pressed and the keyboard stays on the right.
//   - "b from the agent pane": an overview action, so from the agent it runs
//     by way of the list, as its old letter could not be pressed there.
func TestLeaderRunsAnActionOnce(t *testing.T) {
	cases := []struct {
		name  string
		model func(t *testing.T) (Model, *fakePane)
		key   tea.KeyPressMsg
		check func(t *testing.T, before, after Model)
	}{
		{
			name:  "g toggles the mouse once",
			model: func(t *testing.T) (Model, *fakePane) { return testModel(sess("a")), nil },
			key:   keyRune('g').(tea.KeyPressMsg),
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
				return leaderFocusedPane(t, fp), fp
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
			name: "t from the agent pane switches to the overview and keeps the right side",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				return leaderFocusedPane(t, fp), fp
			},
			key: keyRune('t').(tea.KeyPressMsg),
			check: func(t *testing.T, _, after Model) {
				if after.view != viewOverview || after.focus != focusRight {
					t.Fatalf("view %v, focus %v; want the overview with the right side focused", after.view, after.focus)
				}
			},
		},
		{
			name: "b from the agent pane shows the Build tab by way of the list",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				m := withBuild(t, leaderFocusedPane(t, fp), buildTabSnap())
				return m, fp
			},
			key: keyB.(tea.KeyPressMsg),
			check: func(t *testing.T, _, after Model) {
				if after.detailTab != tabBuild || after.view != viewOverview || after.focus != focusRight {
					t.Fatalf("tab %v, view %v, focus %v; want the Build tab focused", after.detailTab, after.view, after.focus)
				}
				if after.pane == nil {
					t.Fatal("the pane was closed; it belongs behind the overview")
				}
			},
		},
		{
			name: "\\ from the agent pane gives the keyboard back and keeps the pane",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				return leaderFocusedPane(t, fp), fp
			},
			key: keyRune('\\').(tea.KeyPressMsg),
			check: func(t *testing.T, _, after Model) {
				if after.focus != focusList || after.pane == nil {
					t.Fatalf("focus %v, pane %v; want the list focused and the pane still open", after.focus, after.pane != nil)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t)
			m, _ = update(t, m, leaderKeyPress)
			if !m.leaderOpen {
				t.Fatal("setup: ctrl+] did not open the dialog")
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
//   - "ctrl+] twice in the pane" is how the agent keeps the key the
//     leader takes: exactly one ctrl+] key event (0x1D), no text.
//   - "esc from the pane" closes and sends nothing: esc is an agent key too
//     (it interrupts Claude), and the dialog must not forward its own close.
//   - "ctrl+] twice from the list" sends nothing even with a pane
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
			name:     "ctrl+] twice in the pane",
			focused:  true,
			keys:     []tea.KeyPressMsg{leaderKeyPress, leaderKeyPress},
			wantKeys: []termpane.KeyEvent{leaderPassedOn},
			wantPane: true,
		},
		{
			name:     "esc from the pane",
			focused:  true,
			keys:     []tea.KeyPressMsg{leaderKeyPress, {Code: tea.KeyEsc}},
			wantPane: true,
		},
		{
			name: "ctrl+] twice from the list",
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
				m = leaderFocusedPane(t, fp)
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

// agentBytes is what the agent would read for everything the fake pane was
// given: its keys, run through a real x/vt emulator, the same encoder the
// real pane (termpane.Pane.SendKey) uses. So a row can say "one 0x1D" in bytes
// instead of trusting that a key event means that byte. Text and pastes fail
// the test, because the rows that use this send keys only.
//
// The emulator hands encoded input out through an io.Pipe, so every SendKey
// blocks until it is read. The reader goroutine stops at a sentinel written
// after the keys rather than at Close, because Emulator.Read checks its
// closed flag without a lock and Close from this goroutine would race it.
func agentBytes(t *testing.T, fp *fakePane) string {
	t.Helper()
	if len(fp.texts) != 0 || len(fp.pastes) != 0 {
		t.Fatalf("the pane was sent text %q / pastes %q, want keys only", fp.texts, fp.pastes)
	}
	const sentinel = "<end of keys>"
	emu := vt.NewEmulator(80, 24)
	got := make(chan string, 1)
	go func() {
		var b []byte
		buf := make([]byte, 64)
		for !strings.HasSuffix(string(b), sentinel) {
			n, err := emu.Read(buf)
			b = append(b, buf[:n]...)
			if err != nil {
				break
			}
		}
		got <- strings.TrimSuffix(string(b), sentinel)
	}()
	for _, k := range fp.keys {
		emu.SendKey(k)
	}
	emu.SendText(sentinel)
	return <-got
}

// TestLeaderIsCtrlBracket pins T9.1: ctrl+] is the leader, and Ctrl+Space,
// the leader before it, is an ordinary key. Each row presses keys through
// Update and checks whether the dialog is open and the exact bytes the agent
// would read. Load-bearing rows:
//   - the three ctrl+] rows: the leader has to open from each place a person
//     presses it (the list, the focused overview, the focused agent pane), and
//     from the pane it must not ALSO type 0x1D to the agent.
//   - the three ctrl+space rows: any of them opening the dialog means the old
//     leader is still bound somewhere. The pane row is the one that proves
//     the key is now ordinary rather than swallowed: the agent reads exactly
//     one NUL, which is what Ctrl+Space, ctrl+@ and ctrl+2 all send.
//   - "ctrl+] twice in the pane": the agent still gets the key the dashboard
//     took, as exactly one 0x1D, not NUL (the old pass-through) and not two.
func TestLeaderIsCtrlBracket(t *testing.T) {
	fromList := func(t *testing.T) (Model, *fakePane) {
		fp := newFakePane("X")
		m := testModel(sess("a"))
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
		return m, fp
	}
	fromDetail := func(t *testing.T) (Model, *fakePane) {
		m := focusOverview(t, detailFocusFixture(t, "alpha", "alpha"))
		return m, m.pane.(*fakePane)
	}
	fromPane := func(t *testing.T) (Model, *fakePane) {
		fp := newFakePane("X")
		return leaderFocusedPane(t, fp), fp
	}
	cases := []struct {
		name      string
		model     func(t *testing.T) (Model, *fakePane)
		keys      []tea.KeyPressMsg
		wantOpen  bool
		wantBytes string
	}{
		{name: "ctrl+] opens the dialog from the list", model: fromList, keys: []tea.KeyPressMsg{leaderKeyPress}, wantOpen: true},
		{name: "ctrl+] opens the dialog from the detail side", model: fromDetail, keys: []tea.KeyPressMsg{leaderKeyPress}, wantOpen: true},
		{name: "ctrl+] opens the dialog from the agent pane", model: fromPane, keys: []tea.KeyPressMsg{leaderKeyPress}, wantOpen: true},
		{name: "ctrl+space opens nothing from the list", model: fromList, keys: []tea.KeyPressMsg{ctrlSpacePress}},
		{name: "ctrl+space opens nothing from the detail side", model: fromDetail, keys: []tea.KeyPressMsg{ctrlSpacePress}},
		{name: "ctrl+space in the agent pane types one NUL", model: fromPane, keys: []tea.KeyPressMsg{ctrlSpacePress}, wantBytes: "\x00"},
		{name: "ctrl+] twice in the pane types one 0x1D", model: fromPane, keys: []tea.KeyPressMsg{leaderKeyPress, leaderKeyPress}, wantBytes: "\x1d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t)
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			if m.leaderOpen != tc.wantOpen {
				t.Fatalf("leaderOpen = %v, want %v", m.leaderOpen, tc.wantOpen)
			}
			if onScreen := strings.Contains(render(m), "type to filter, or press an action's key"); onScreen != tc.wantOpen {
				t.Fatalf("dialog on screen = %v, want %v", onScreen, tc.wantOpen)
			}
			if got := agentBytes(t, fp); got != tc.wantBytes {
				t.Fatalf("the agent read %q, want %q", got, tc.wantBytes)
			}
		})
	}
}

// leaderActionCase is one row of TestLeaderRunsEveryAction: a model in the
// mode the action lives in, the key that used to run it there, and how to
// tell that it ran.
type leaderActionCase struct {
	setup func(t *testing.T) Model
	// mode is where the action lives: the list (inputNormal) or the
	// overview (inputDetail) for the right-side actions.
	mode inputMode
	// old is the key that ran the action before T7.2.
	old tea.KeyPressMsg
	// ran reports whether the action happened, from the model before and
	// after and the command the key returned.
	ran func(before, after Model, cmd tea.Cmd) bool
	// query is what a by-name row (no key after the leader) is reached by:
	// typed into the dialog's filter, then enter. Empty for keyed rows.
	query string
}

// leaderApplyFilter applies a session-list filter the way a user does now:
// ctrl+] /, the query, enter.
func leaderApplyFilter(t *testing.T, m Model, q string) Model {
	t.Helper()
	m, _ = update(t, m, leaderKeyPress)
	m, cmd := update(t, m, keySlash())
	m = deliverFilterMatches(t, m, cmd)
	for _, r := range q {
		m, cmd = update(t, m, keyRune(r))
		m = deliverFilterMatches(t, m, cmd)
	}
	m, _ = update(t, m, keyEnter)
	if m.list.FilterState() != list.FilterApplied {
		t.Fatalf("setup: filter state = %v, want applied", m.list.FilterState())
	}
	return m
}

// leaderActionCases has one case per leader-table action, keyed by action;
// TestLeaderRunsEveryAction fails if the table gains an action this map
// lacks. Each setup puts the model where the action lives and where it can
// run, so "did not run" can only mean the key did not reach it.
func leaderActionCases() map[modeAction]leaderActionCase {
	press := func(r rune) tea.KeyPressMsg { return keyRune(r).(tea.KeyPressMsg) }
	list1 := func(t *testing.T) Model { return testModel(sess("a")) }
	withPane := func(t *testing.T) Model {
		m := testModel(sess("a"))
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "a"})
		return m
	}
	overview := func(t *testing.T) Model { return focusOverview(t, detailFocusFixture(t, "alpha", "alpha")) }
	pauseList := func(t *testing.T) Model {
		m, rec := buildPauseModel(t, sess("alpha"))
		rec.resumed = ResumeResult{WasPaused: true}
		return withBuild(t, m, buildPauseSnap())
	}
	cmdIs := func(cmd tea.Cmd, want func(tea.Msg) bool) bool { return cmd != nil && want(cmd()) }
	planList := func(t *testing.T) Model {
		m := testModel(sess("a"))
		m.deps.StartPlan = func(root, mode, text string) PlanStartResult {
			return PlanStartResult{Outcome: PlanStarted, Root: root}
		}
		return m
	}
	allProjects := func(t *testing.T) Model {
		m := testModel(sess("a"))
		m.deps.LoadAllProjects = func() AllProjects { return AllProjects{} }
		return m
	}
	// screen is the all-projects screen open on the fixture's rows, the
	// cursor on alpha (listed), with the add and remove seams wired.
	screen := func(t *testing.T) Model {
		m := testModel(sess("a"))
		m.deps.LoadAllProjects = func() AllProjects { return AllProjects{Rows: allProjectsFixture()} }
		m.deps.AddProject = func(string, string) ProjectEditResult { return ProjectEditResult{} }
		m.deps.RemoveProject = func(string) ProjectEditResult { return ProjectEditResult{} }
		m.allProjectsOpen = true
		m.allProjectsKnown = true
		m.allProjectsRows = allProjectsFixture()
		return m
	}
	// buildTab is the Build tab with the keyboard on the right and the
	// cursor on the current task, with the status seam wired (T7.11).
	buildTab := func(t *testing.T) Model {
		m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
		m.deps.SetTaskStatus = func(string, string, string, []string) TaskStatusResult { return TaskStatusResult{} }
		m, _ = pressAction(t, m, keyB)
		return m
	}
	// tokensTab is the Build tab as buildTab sets it up, with the tokens
	// seam wired and the cursor's task's record held (T7.12).
	tokensTab := func(t *testing.T) Model {
		m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
		m.deps.LoadTaskTokens = func(string, string) TaskTokens { return TaskTokens{} }
		m, _ = pressAction(t, m, keyB)
		m, _ = update(t, m, taskTokensLoadedMsg{gen: m.projectGen, root: m.build.Root, task: m.buildCursorID, rec: TaskTokens{Task: m.buildCursorID}})
		return m
	}
	// usageList is the session list with the usage reading seam wired
	// (T7.13); the seam is never reached, since ran reads only the flag the
	// action sets before its command runs.
	usageList := func(t *testing.T) Model {
		m := testModel(sess("a"))
		m.deps.RefreshUsage = func() UsageRefreshResult { return UsageRefreshResult{} }
		return m
	}
	return map[modeAction]leaderActionCase{
		// "refresh usage" (T7.13) never had a key; R on the bare list is no
		// action and must not become one.
		actionRefreshUsage: {setup: usageList, mode: inputNormal, old: press('R'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.usageRefreshing }},
		// "show this task's tokens" (T7.12) never had a key; T on the Build
		// tab is no action and must not become one.
		actionTaskTokens: {setup: tokensTab, mode: inputDetail, old: press('T'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.taskTokensOpen }},
		// "set this task's status" (T7.11) never had a key; S on the Build
		// tab is no action and must not become one.
		actionTaskStatus: {setup: buildTab, mode: inputDetail, old: press('S'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPickTaskStatus }},
		// Add and remove a project (T7.10) never had a key; + and - on the
		// screen are no action and must not become one.
		actionProjectAdd: {setup: screen, mode: inputAllProjects, old: press('+'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputProjectAddFolder }},
		actionProjectRemove: {setup: screen, mode: inputAllProjects, old: press('-'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputConfirmProjectRemove }},
		// "all projects" (T7.9) never had a key; A on the bare list is no
		// action and must not become one.
		actionAllProjects: {setup: allProjects, mode: inputNormal, old: press('A'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.allProjectsOpen }},
		actionNewSession: {setup: list1, mode: inputNormal, old: press('n'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.formOpen }},
		actionScratch: {setup: list1, mode: inputNormal, old: press('o'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPickProvider }},
		actionProjects: {setup: func(t *testing.T) Model {
			m := testModel(sess("a"))
			m.projects = []project.Project{{Path: "/repo", Name: "api"}, {Path: "/repos/afk", Name: "afk"}}
			m.activeProject = 0
			return m
		}, mode: inputNormal, old: press('p'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.switcherOpen }},
		actionClean: {setup: list1, mode: inputNormal, old: press('c'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputConfirmClean }},
		actionRestart: {setup: func(t *testing.T) Model { return testModel(stoppedSess("alpha")) }, mode: inputNormal, old: press('r'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.restartInFlight.name == "alpha" }},
		actionEditLabel: {setup: list1, mode: inputNormal, old: press('l'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputEditLabel }},
		actionFilterStart: {setup: list1, mode: inputNormal, old: press('/'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.list.SettingFilter() }},
		// Clear filter's old key was esc, which still clears (it is kept
		// without the leader); the old-key row is the bare x, which was
		// never an action and must not become one.
		actionFilterClear: {setup: func(t *testing.T) Model {
			return leaderApplyFilter(t, testModel(sess("alpha"), sess("beta")), "b")
		}, mode: inputNormal, old: press('x'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.list.FilterState() == list.Unfiltered }},
		actionBuild: {setup: func(t *testing.T) Model { return withBuild(t, testModel(sess("alpha")), buildTabSnap()) }, mode: inputNormal, old: press('b'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.detailTab == tabBuild && a.focus == focusRight }},
		actionInbox: {setup: func(t *testing.T) Model { m := testModel(sess("a")); m.buildKnown = true; return m }, mode: inputNormal, old: press('i'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.inboxOpen }},
		actionPauseBuild: {setup: pauseList, mode: inputNormal, old: press('P'),
			ran: func(_, _ Model, cmd tea.Cmd) bool {
				return cmdIs(cmd, func(msg tea.Msg) bool { _, ok := msg.(buildPausedMsg); return ok })
			}},
		actionResumeBuild: {setup: pauseList, mode: inputNormal, old: press('U'),
			ran: func(_, _ Model, cmd tea.Cmd) bool {
				return cmdIs(cmd, func(msg tea.Msg) bool { _, ok := msg.(buildResumedMsg); return ok })
			}},
		actionFocusPane: {setup: func(t *testing.T) Model { m := testModel(runningSess("alpha")); m.view = viewOverview; return m }, mode: inputNormal, old: press('f'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.focus == focusRight }},
		actionPaneToggle: {setup: overview, mode: inputDetail, old: chord('t'),
			ran: func(b, a Model, _ tea.Cmd) bool { return a.view != b.view }},
		actionDetailUnfocus: {setup: overview, mode: inputDetail, old: chord('\\'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.focus == focusList }},
		actionPaneClose: {setup: withPane, mode: inputNormal, old: chord('\\'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.pane == nil }},
		actionMouseToggle: {setup: list1, mode: inputNormal, old: chord('g'),
			ran: func(b, a Model, _ tea.Cmd) bool { return a.mouseCapture != b.mouseCapture }},
		actionEnterResize: {setup: list1, mode: inputNormal, old: chord('w'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.resizing }},
		actionHelp: {setup: list1, mode: inputNormal, old: press('?'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.helpShowAll }},
		actionQuit: {setup: list1, mode: inputNormal, old: press('q'),
			ran: func(_, a Model, _ tea.Cmd) bool { return a.quitting }},
		// The plan actions (T7.3) never had a key; the "old" key is the
		// first letter of the query, which on the bare list is nothing. The
		// reconcile row is reached by "document": its name starts with r,
		// which is restart's key and would run restart when it can.
		actionPlanExtend: {setup: planList, mode: inputNormal, old: press('e'), query: "extend",
			ran: func(_, _ Model, cmd tea.Cmd) bool {
				return cmdIs(cmd, func(msg tea.Msg) bool { _, ok := msg.(planStartedMsg); return ok })
			}},
		actionPlanAmend: {setup: planList, mode: inputNormal, old: press('a'), query: "amend",
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPlanText && a.planTextMode == PlanModeAmend }},
		actionPlanReconcile: {setup: planList, mode: inputNormal, old: press('d'), query: "document",
			ran: func(_, a Model, _ tea.Cmd) bool {
				return a.modal == inputPlanText && a.planTextMode == PlanModeReconcile
			}},
		// "start a new plan" (T7.4) is reached by its full name: its first
		// letter, s, is no leader key, so the whole name filters down to it.
		actionPlanNew: {setup: planList, mode: inputNormal, old: press('s'), query: "start a new plan",
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPlanText && a.planTextMode == PlanModeNew }},
		// "check the plan" (T7.7) is reached by "errors", a word only its
		// help line has: its name starts with c, clean's key, and "warnings"
		// with w, resize's (TestLeaderPlanCheckWordsAreUnique).
		actionPlanCheck: {setup: func(t *testing.T) Model {
			return withPlanStatus(t, testModel(sess("a")), planCheckStatusErrors())
		}, mode: inputNormal, old: press('e'), query: "errors",
			ran: func(_, a Model, _ tea.Cmd) bool { return a.planCheckOpen }},
		// "approve the plan" (T7.8) is reached by "approve": a, its first
		// letter, is no leader key, and only this row says the word
		// (TestLeaderPlanApproveWordIsUnique). Running it raises the confirm;
		// nothing is approved until its y.
		actionPlanApprove: {setup: func(t *testing.T) Model {
			m := withPlanStatus(t, testModel(sess("a")), planApproveStatus(0))
			m.deps.ApprovePlan = func(string) PlanApproveResult { return PlanApproveResult{} }
			return m
		}, mode: inputNormal, old: press('a'), query: "approve",
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputConfirmPlanApprove }},
	}
}

// TestLeaderRunsEveryAction is the per-action table T7.2 is built on: one
// subtest per leader-table action, each asserting that ctrl+] then its
// key runs it and that the key it used to answer to (a bare letter or a
// chord) no longer does, in the mode the action lives in. Every row is
// load-bearing — the old key half is what proves the bare rule, the chord or
// the palette is really gone, and the leader half that nothing was lost on
// the way. The map is checked against leaderTable, so an action added to the
// table without a row here fails.
func TestLeaderRunsEveryAction(t *testing.T) {
	cases := leaderActionCases()
	for _, e := range leaderTable {
		tc, ok := cases[e.action]
		if !ok {
			t.Errorf("leader action %v (%q) has no row in leaderActionCases", e.action, e.key)
			continue
		}
		t.Run(leaderNames[e.action], func(t *testing.T) {
			t.Run("the old key does not run it", func(t *testing.T) {
				m := tc.setup(t)
				if got := activeInputMode(m.modeSnapshot()); got != tc.mode {
					t.Fatalf("setup: mode = %v, want %v", got, tc.mode)
				}
				after, cmd := update(t, m, tc.old)
				if tc.ran(m, after, cmd) {
					t.Fatalf("%q still runs %v without the leader", tc.old.String(), e.action)
				}
				if after.leaderOpen {
					t.Fatalf("%q opened the actions dialog", tc.old.String())
				}
			})
			t.Run("ctrl+] then its key runs it", func(t *testing.T) {
				m := tc.setup(t)
				if got := activeInputMode(m.modeSnapshot()); got != tc.mode {
					t.Fatalf("setup: mode = %v, want %v", got, tc.mode)
				}
				m, _ = update(t, m, leaderKeyPress)
				if !m.leaderOpen {
					t.Fatal("ctrl+] did not open the dialog")
				}
				row, ok := leaderRowFor(m.leader.rows, e.action)
				if !ok || row.blocked != "" {
					t.Fatalf("setup: the %v row is missing or dimmed (%q)", e.action, row.blocked)
				}
				var after Model
				var cmd tea.Cmd
				if e.key == "" {
					// By name only: type the query, then enter on the
					// one row it leaves.
					for _, r := range tc.query {
						m, _ = update(t, m, keyRune(r))
					}
					if v := m.leader.visible(); len(v) != 1 || v[0].action != e.action {
						t.Fatalf("query %q leaves %d rows, want only %v", tc.query, len(v), e.action)
					}
					after, cmd = update(t, m, keyEnter)
				} else {
					k, ok := leaderPressFor(e.key)
					if !ok {
						t.Fatalf("key %q does not round-trip", e.key)
					}
					after, cmd = update(t, m, k)
				}
				if after.leaderOpen {
					t.Fatal("the dialog stayed open")
				}
				if !tc.ran(m, after, cmd) {
					t.Fatalf("ctrl+] %s did not run %v", e.key, e.action)
				}
			})
		})
	}
}

// TestRemovedListKeyOnlyRaisesItsNotice presses every removed key on the
// session list and checks it did nothing but put up its notice. The rows are
// load-bearing as a set: each is a key that used to act here, and the notice
// is the only trace a press may leave. `:` is the palette's old key and
// names the dialog's filter instead of a key.
func TestRemovedListKeyOnlyRaisesItsNotice(t *testing.T) {
	for _, k := range removedListKeys {
		t.Run(k, func(t *testing.T) {
			m := testModel(sess("a"))
			m.buildKnown = true
			press, ok := leaderPressFor(k)
			if !ok {
				t.Fatalf("key %q does not round-trip", k)
			}
			got, cmd := update(t, m, press)
			want, _ := removedKeyNotice(defaultLeaderKey, k)
			if got.status != want || got.statusKind != notifyInfo {
				t.Fatalf("status = %q (%v), want the info notice %q", got.status, got.statusKind, want)
			}
			if cmd != nil {
				t.Errorf("%q returned a command; it must only raise the notice", k)
			}
			t.Logf("%q on the list: notice %q, nothing else", k, got.status)
			if got.formOpen || got.switcherOpen || got.inboxOpen || got.leaderOpen || got.modal != inputNormal ||
				got.quitting || got.helpShowAll || got.list.SettingFilter() || got.focus != focusList || got.resizing {
				t.Fatalf("%q acted on the dashboard: form=%v switcher=%v inbox=%v leader=%v modal=%v quit=%v help=%v filter=%v focus=%v resize=%v",
					k, got.formOpen, got.switcherOpen, got.inboxOpen, got.leaderOpen, got.modal, got.quitting,
					got.helpShowAll, got.list.SettingFilter(), got.focus, got.resizing)
			}
		})
	}
	t.Run("a kept key raises no notice", func(t *testing.T) {
		m := testModel(sess("a"), sess("b"))
		for _, k := range []tea.KeyPressMsg{keyRune('j').(tea.KeyPressMsg), keyRune('x').(tea.KeyPressMsg), chord('t')} {
			got, _ := update(t, m, k)
			if got.status != "" {
				t.Errorf("%q raised %q, want no notice", k.String(), got.status)
			}
		}
	})
	t.Run("the overview raises no notice", func(t *testing.T) {
		m := focusOverview(t, detailFocusFixture(t, "alpha", "alpha"))
		m.clearStatus()
		got, _ := update(t, m, keyRune('n'))
		if got.status != "" || got.formOpen {
			t.Errorf("n on the overview: status %q, form %v; want it ignored", got.status, got.formOpen)
		}
	})
}

// TestRemovedChordsReachTheAgent pins that the chords the agent pane used to keep
// are the agent's since T7.2: each arrives at the pane as its ctrl key event
// and the dashboard's focus, view and mouse are untouched. Every row is
// load-bearing — any one of them still claimed would take a key from every
// agent the dashboard hosts.
func TestRemovedChordsReachTheAgent(t *testing.T) {
	for _, code := range []rune{'t', 'g', '\\', 'w', 'c'} {
		k := chord(code)
		t.Run(k.String(), func(t *testing.T) {
			fp := newFakePane("X")
			m := leaderFocusedPane(t, fp)
			got, _ := update(t, m, k)
			want := termpane.KeyEvent{Code: code, Mod: vt.ModCtrl}
			if len(fp.keys) != 1 || fp.keys[0] != want {
				t.Fatalf("the pane got %+v, want exactly %+v", fp.keys, want)
			}
			if !got.paneOwnsKeyboard() || got.view != viewAgent || got.mouseCapture != m.mouseCapture || got.quitting {
				t.Fatalf("%s acted on the dashboard: pane focused %v, view %v, mouse %v, quit %v",
					k.String(), got.paneOwnsKeyboard(), got.view, got.mouseCapture, got.quitting)
			}
		})
	}
}

// TestCtrlCStillQuits pins A3: ctrl+c leaves the dashboard from the list and
// from the overview without the leader, so a stuck dashboard can always be
// left. Both rows are load-bearing; the agent pane's ctrl+c is the agent's
// (TestRemovedChordsReachTheAgent).
func TestCtrlCStillQuits(t *testing.T) {
	cases := []struct {
		name  string
		model func(t *testing.T) Model
	}{
		{"from the list", func(t *testing.T) Model { return testModel(sess("a")) }},
		{"from the overview", func(t *testing.T) Model { return focusOverview(t, detailFocusFixture(t, "alpha", "alpha")) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, cmd := update(t, tc.model(t), chord('c'))
			if !got.quitting || cmd == nil {
				t.Fatalf("ctrl+c: quitting %v, cmd %v; want the dashboard to quit", got.quitting, cmd != nil)
			}
		})
	}
}

// TestHelpOpensFromTheLeaderAndEscClosesIt pins A5: ctrl+] ? shows the
// full help, and esc on the list closes it before it would clear a filter.
// The filtered row is the load-bearing one: with both up, one esc must take
// the help away and leave the filter, or the owner loses the filter they
// were reading the help to use.
func TestHelpOpensFromTheLeaderAndEscClosesIt(t *testing.T) {
	m := testModel(sess("alpha"), sess("beta"))
	m = leaderApplyFilter(t, m, "b")
	m = pressLeader(t, m, keyRune('?'))
	if !m.helpShowAll {
		t.Fatal("ctrl+] ? did not show the full help")
	}
	m, _ = update(t, m, keyEscMsg())
	if m.helpShowAll {
		t.Fatal("esc did not close the full help")
	}
	if m.list.FilterState() != list.FilterApplied {
		t.Fatalf("esc closing the help also changed the filter to %v", m.list.FilterState())
	}
	m, _ = update(t, m, keyEscMsg())
	if m.list.FilterState() != list.Unfiltered {
		t.Fatalf("the second esc left the filter %v, want it cleared", m.list.FilterState())
	}
}
