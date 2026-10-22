package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/dphaener/afk/internal/discover"
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

// pressKey presses k as a dashboard key (pressAction): alone at All projects
// and Project, after the leader in the Agent.
func pressKey(t *testing.T, m Model, k tea.Msg) Model {
	t.Helper()
	m, _ = pressAction(t, m, k)
	return m
}

// leaderFocusedPane is a pane hosting "a", focused with enter from the list:
// enter on a session at Project opens its Agent.
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

// TestLeaderShowsTheKeyStrip drives ctrl+] through Update from every level.
// Load-bearing rows:
//   - "from a focused agent pane": the pane forwards every other key to the
//     agent, so this row proves the leader is claimed there at all, that it
//     shows the strip (§4.8) and not the old actions dialog, and that the key
//     was not ALSO typed to the agent.
//   - "from the agent placeholder": it is the Agent too (A6), and its own
//     keys are answered before decideKey, so the leader has to be let
//     through there separately.
//   - the list, overview and all-projects rows: keys work alone there, so the
//     leader opens nothing — no strip, no dialog — and a person who presses
//     it out of habit loses nothing.
func TestLeaderShowsTheKeyStrip(t *testing.T) {
	cases := []struct {
		name      string
		model     func(t *testing.T) (Model, *fakePane)
		wantStrip bool
	}{
		{
			name: "from the session list",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				m := testModel(sess("a"))
				m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
				return m, fp
			},
		},
		{
			name: "from the focused overview",
			model: func(t *testing.T) (Model, *fakePane) {
				m := focusOverview(t, detailFocusFixture(t, "alpha", "alpha"))
				return m, m.pane.(*fakePane)
			},
		},
		{
			name: "from the all-projects screen",
			model: func(t *testing.T) (Model, *fakePane) {
				m, _, _ := allProjectsModel(t)
				return openAllProjects(t, m), nil
			},
		},
		{
			name: "from the agent placeholder",
			model: func(t *testing.T) (Model, *fakePane) {
				return leaderPlaceholderModel(t), nil
			},
			wantStrip: true,
		},
		{
			name: "from a focused agent pane",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				return leaderFocusedPane(t, fp), fp
			},
			wantStrip: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t)
			before := activeInputMode(m.modeSnapshot())
			got, cmd := update(t, m, leaderKeyPress)
			if got.leaderOpen {
				t.Fatal("ctrl+] opened the actions search; since T16.2 that is `:`")
			}
			if cmd != nil {
				t.Fatalf("ctrl+] returned a command (%T); it must be pure state", cmd())
			}
			if fp != nil && sentToPane(fp) != 0 {
				t.Errorf("ctrl+] sent %d inputs to the pane, want none", sentToPane(fp))
			}
			mode := activeInputMode(got.modeSnapshot())
			if !tc.wantStrip {
				if got.keyStripOpen || mode != before {
					t.Fatalf("ctrl+] changed the keyboard from %v to %v (strip %v); want nothing", before, mode, got.keyStripOpen)
				}
				return
			}
			if mode != inputKeyStrip {
				t.Fatalf("the keyboard is with %v, want the key strip", mode)
			}
			out := render(got)
			if !strings.Contains(out, "afk keys") || !strings.Contains(out, keyStripWaiting) {
				t.Errorf("the strip or its status line is not on screen:\n%s", out)
			}
		})
	}
}

// TestKeyStripPassesAndCloses pins what reaches the agent once the strip is
// open (§4.8). Load-bearing rows:
//   - "leader twice": how the agent keeps the key the leader takes — exactly
//     one ctrl+] key event (0x1D), no text, and the pane keeps the keyboard.
//   - "leader then esc": back to Project, nothing sent. esc is an agent key
//     too (it interrupts Claude), and the strip must not forward it.
//   - "leader then an unlisted key": the strip closes and NOTHING is sent,
//     so a mistyped key after the leader never reaches the agent; the pane
//     keeps the keyboard.
//   - "leader then a Project key": n is a key at Project, not in the Agent,
//     so it is an unlisted key here, not a new-session form over the agent.
func TestKeyStripPassesAndCloses(t *testing.T) {
	cases := []struct {
		name     string
		keys     []tea.KeyPressMsg
		wantKeys []termpane.KeyEvent
		wantPane bool // the pane still has the keyboard afterwards
	}{
		{
			name:     "leader twice",
			keys:     []tea.KeyPressMsg{leaderKeyPress, leaderKeyPress},
			wantKeys: []termpane.KeyEvent{leaderPassedOn},
			wantPane: true,
		},
		{
			name: "leader then esc",
			keys: []tea.KeyPressMsg{leaderKeyPress, {Code: tea.KeyEsc}},
		},
		{
			name:     "leader then an unlisted key",
			keys:     []tea.KeyPressMsg{leaderKeyPress, leaderPress("z")},
			wantPane: true,
		},
		{
			name:     "leader then a Project key",
			keys:     []tea.KeyPressMsg{leaderKeyPress, leaderPress("n")},
			wantPane: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePane("X")
			m := leaderFocusedPane(t, fp)
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			if m.keyStripOpen || m.leaderOpen || m.formOpen {
				t.Fatalf("strip %v, search %v, form %v; want all closed", m.keyStripOpen, m.leaderOpen, m.formOpen)
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
			if !tc.wantPane && (m.level() != levelProject || m.pane == nil) {
				t.Fatalf("level %v, pane %v; want Project with the pane still connected (A5)", m.level(), m.pane != nil)
			}
		})
	}
}

// TestAgentKeysReachTheAgent pins that in the Agent every key but the leader
// goes to the agent (§5). Every row is load-bearing: q and ctrl+c would quit
// the dashboard anywhere else, esc would go back a level, and a letter would
// run an action — each one taken from the agent here would take a key from
// every session the dashboard hosts.
func TestAgentKeysReachTheAgent(t *testing.T) {
	cases := []struct {
		name string
		k    tea.KeyPressMsg
		want termpane.KeyEvent
		text string
	}{
		{name: "q", k: leaderPress("q"), text: "q"},
		{name: "n", k: leaderPress("n"), text: "n"},
		{name: "esc", k: leaderPress("esc"), want: termpane.KeyEvent{Code: vt.KeyEscape}},
		{name: "ctrl+c", k: leaderPress("ctrl+c"), want: termpane.KeyEvent{Code: 'c', Mod: vt.ModCtrl}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePane("X")
			m := leaderFocusedPane(t, fp)
			got, _ := update(t, m, tc.k)
			if got.quitting || got.formOpen || got.keyStripOpen || !got.paneOwnsKeyboard() {
				t.Fatalf("%s acted on the dashboard: quit %v form %v strip %v pane focused %v",
					tc.name, got.quitting, got.formOpen, got.keyStripOpen, got.paneOwnsKeyboard())
			}
			if sentToPane(fp) != 1 {
				t.Fatalf("the pane was given %d inputs (keys %+v, text %q), want exactly the one key", sentToPane(fp), fp.keys, fp.texts)
			}
			if tc.text != "" {
				if len(fp.texts) != 1 || fp.texts[0] != tc.text {
					t.Fatalf("the pane got text %q, want %q", fp.texts, tc.text)
				}
				return
			}
			if len(fp.keys) != 1 || fp.keys[0] != tc.want {
				t.Fatalf("the pane got %+v, want %+v", fp.keys, tc.want)
			}
		})
	}
}

// TestLeaderIgnoredUnderEveryOverlay pins that ctrl+] opens nothing while
// an overlay, a form, a prompt or a confirmation has the keyboard, and that
// the surface it lands on is left as it was. Every row is load-bearing in the
// same way: an open overlay gets every key first (§5), and a global intercept
// added later would turn exactly one of these red. The text rows (form,
// label) also check the key typed nothing: ctrl+] has no text, so a field
// that grew a character would mean the key was mistaken for a space.
func TestLeaderIgnoredUnderEveryOverlay(t *testing.T) {
	cases := []struct {
		name string
		open func(t *testing.T, m Model) Model
		mode inputMode
		text func(m Model) string
	}{
		{
			name: "jump",
			open: func(t *testing.T, m Model) Model {
				m.projects = []project.Project{{Path: "/repo", Name: "api"}, {Path: "/repos/afk", Name: "afk"}}
				m.activeProject = 0
				return pressKey(t, m, keyRune('/'))
			},
			mode: inputJump,
		},
		{
			name: "inbox",
			open: func(t *testing.T, m Model) Model {
				m.buildKnown = true
				return pressKey(t, m, keyRune('i'))
			},
			mode: inputInbox,
		},
		{
			name: "actions search",
			open: func(t *testing.T, m Model) Model { return pressKey(t, m, keyRune(':')) },
			mode: inputLeader,
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
			open: func(t *testing.T, m Model) Model { return pressKey(t, m, keyRune('n')) },
			mode: inputForm,
			text: func(m Model) string { return m.form.nameInput.Value() },
		},
		{
			name: "label edit",
			open: func(t *testing.T, m Model) Model { return pressKey(t, m, keyRune('l')) },
			mode: inputEditLabel,
			text: func(m Model) string { return m.labelInput.Value() },
		},
		{
			name: "clean confirm",
			open: func(t *testing.T, m Model) Model { return pressKey(t, m, keyRune('c')) },
			mode: inputConfirmClean,
		},
		{
			// Reaching it through restart needs a wedged row; the modal field is
			// the whole of the state its mode is derived from.
			name: "restart confirm",
			open: func(t *testing.T, m Model) Model { m.modal = inputConfirmRestart; return m },
			mode: inputConfirmRestart,
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
			if got.keyStripOpen {
				t.Fatalf("ctrl+] opened the key strip over the %s", tc.name)
			}
			wantMode := tc.mode
			if tc.mode == inputLeader || tc.mode == inputJump {
				// The leader closes the `:` search and the jump search: it is
				// how each was reached from the Agent, and pressing it again
				// is "never mind".
				wantMode = inputNormal
			}
			if mode := activeInputMode(got.modeSnapshot()); mode != wantMode {
				t.Fatalf("ctrl+] moved the keyboard from %v to %v; want %v", tc.mode, mode, wantMode)
			}
			if tc.text != nil {
				if after := tc.text(got); after != before {
					t.Fatalf("ctrl+] changed the %s's text from %q to %q", tc.name, before, after)
				}
			}
		})
	}
}

// TestKeysRunAnActionOnce pins that a key runs its action exactly once.
// Load-bearing rows:
//   - "ctrl+] m toggles the mouse once" is the "once" proof: run twice, the
//     toggle would land back where it started and the test could not tell
//     it from not running at all, so it checks the flip itself.
//   - "n opens the new-session form": a Project key alone, with no leader
//     (§5), where T7.2 needed ctrl+] n.
//   - "ctrl+] i from the agent pane": a Project-side overlay picked while the
//     agent had the keyboard opens over the Project — the keyboard goes to
//     the list — and nothing at all is typed to the agent.
//   - "ctrl+] d from the agent pane": the overview takes the keyboard (A2)
//     and the pane stays connected behind it.
//   - "d from the list": the same key, the same thing, from Project.
func TestKeysRunAnActionOnce(t *testing.T) {
	cases := []struct {
		name  string
		model func(t *testing.T) (Model, *fakePane)
		key   tea.KeyPressMsg
		check func(t *testing.T, before, after Model)
	}{
		{
			name: "ctrl+] m toggles the mouse once",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				return leaderFocusedPane(t, fp), fp
			},
			key: leaderPress("m"),
			check: func(t *testing.T, before, after Model) {
				if after.mouseCapture == before.mouseCapture {
					t.Fatalf("mouseCapture = %v, want it flipped once", after.mouseCapture)
				}
				if !after.paneOwnsKeyboard() {
					t.Fatal("the mouse key moved the keyboard out of the agent")
				}
			},
		},
		{
			name:  "n opens the new-session form",
			model: func(t *testing.T) (Model, *fakePane) { return testModel(sess("a")), nil },
			key:   leaderPress("n"),
			check: func(t *testing.T, _, after Model) {
				if !after.formOpen {
					t.Fatal("n did not open the form")
				}
			},
		},
		{
			name: "ctrl+] i from the agent pane opens the inbox with the list focused",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				m := leaderFocusedPane(t, fp)
				m.buildKnown = true
				return m, fp
			},
			key: leaderPress("i"),
			check: func(t *testing.T, _, after Model) {
				if !after.inboxOpen {
					t.Fatal("ctrl+] i did not open the inbox")
				}
				if after.focus != focusList {
					t.Fatalf("focus = %v, want the list: an overlay opens over the Project", after.focus)
				}
				if after.pane == nil {
					t.Fatal("the pane was closed; only cleaning the session closes it")
				}
			},
		},
		{
			name: "ctrl+] d from the agent pane opens the details over it",
			model: func(t *testing.T) (Model, *fakePane) {
				fp := newFakePane("X")
				return leaderFocusedPane(t, fp), fp
			},
			key: leaderPress("d"),
			check: func(t *testing.T, _, after Model) {
				if !after.detailOwnsKeyboard() || !after.shouldRenderPane() || after.pane == nil {
					t.Fatalf("details %v, pane drawn %v, pane %v; want the details over the drawn agent",
						after.detailOwnsKeyboard(), after.shouldRenderPane(), after.pane != nil)
				}
				if after.level() != levelAgent || after.detailsFrom != levelAgent {
					t.Fatalf("level = %v, opened from %v, want the Agent for both", after.level(), after.detailsFrom)
				}
			},
		},
		{
			name:  "d from the list opens the details",
			model: func(t *testing.T) (Model, *fakePane) { return testModel(sess("a")), nil },
			key:   leaderPress("d"),
			check: func(t *testing.T, _, after Model) {
				if !after.detailOwnsKeyboard() || after.detailsFrom != levelProject {
					t.Fatalf("details %v opened from %v, want the details open from the Project", after.detailOwnsKeyboard(), after.detailsFrom)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t)
			got, _ := pressAction(t, m, tc.key)
			if got.keyStripOpen {
				t.Fatal("the strip stayed open after running an action")
			}
			tc.check(t, m, got)
			if fp != nil && sentToPane(fp) != 0 {
				t.Errorf("the pane was sent %d inputs; an afk key must never reach the agent", sentToPane(fp))
			}
		})
	}
}

// TestActionsSearchEnterRunsTheHighlightedRow moves the highlight and presses
// enter. The row under the highlight is looked up rather than assumed, so a
// change in the table's order moves the test with it instead of breaking it.
func TestActionsSearchEnterRunsTheHighlightedRow(t *testing.T) {
	m := testModel(sess("a"))
	m = pressKey(t, m, keyRune(':'))
	if !m.leaderOpen {
		t.Fatal("setup: : did not open the actions search")
	}
	target := -1
	for i, r := range m.leader.visible() {
		if r.action == actionNewSession {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("setup: the search has no new-session row")
	}
	for range target {
		m, _ = update(t, m, keyDown)
	}
	if m.leader.highlight != target {
		t.Fatalf("setup: highlight = %d, want %d", m.leader.highlight, target)
	}
	got, _ := update(t, m, keyEnter)
	if got.leaderOpen {
		t.Fatal("enter left the search open")
	}
	if !got.formOpen {
		t.Fatal("enter on the new-session row did not open the form")
	}
}

// TestActionsSearchLettersType pins §4.7's rule through Update: with the
// search open, a letter that is an action's key types into the query and
// runs nothing. Load-bearing: q would have quit the dashboard and n opened
// the form in T7.2's dialog, which is why every by-name row there needed a
// word no other row's key started.
func TestActionsSearchLettersType(t *testing.T) {
	m := pressKey(t, testModel(sess("a")), keyRune(':'))
	for _, r := range "qn" {
		m, _ = update(t, m, keyRune(r))
	}
	if !m.leaderOpen || m.quitting || m.formOpen {
		t.Fatalf("search %v, quit %v, form %v; want the letters typed", m.leaderOpen, m.quitting, m.formOpen)
	}
	if m.leader.query != "qn" {
		t.Fatalf("query = %q, want %q", m.leader.query, "qn")
	}
}

// TestActionsSearchDimmedRowDoesNothing finds the clean row with no session
// selected and presses enter on it. The search must stay open and nothing
// run — a dimmed row is not a way round its own reason.
func TestActionsSearchDimmedRowDoesNothing(t *testing.T) {
	m := pressKey(t, testModel(), keyRune(':'))
	m = typeText(t, m, "clean session")
	v := m.leader.visible()
	if len(v) != 1 || v[0].action != actionClean || v[0].blocked == "" {
		t.Fatalf("setup: visible rows %+v, want only the dimmed clean row", v)
	}
	got, cmd := update(t, m, keyEnter)
	if !got.leaderOpen {
		t.Fatal("enter on a dimmed row closed the search")
	}
	if got.modal != inputNormal || cmd != nil {
		t.Fatalf("a dimmed row ran something: modal %v, cmd %v", got.modal, cmd != nil)
	}
}

// TestActionsSearchFromTheAgent opens `:` with the leader in the Agent.
// Load-bearing: the rows are the Agent's, with their keys named after the
// leader, esc returns to the agent with nothing sent, and a row run from it
// that opens an overlay opens it over the Project with the pane connected.
func TestActionsSearchFromTheAgent(t *testing.T) {
	fp := newFakePane("X")
	m := leaderFocusedPane(t, fp)
	m = pressKey(t, m, keyRune(':'))
	if !m.leaderOpen || m.leaderLevel != levelAgent {
		t.Fatalf("search open %v at %v, want open at the Agent", m.leaderOpen, m.leaderLevel)
	}
	inbox, ok := leaderRowFor(m.leader.rows, actionInbox)
	if !ok || inbox.keyLabel != defaultLeaderKey+" i" {
		t.Fatalf("inbox row key = %q, want %q", inbox.keyLabel, defaultLeaderKey+" i")
	}
	back, _ := update(t, m, keyEscMsg())
	if back.leaderOpen || !back.paneOwnsKeyboard() || sentToPane(fp) != 0 {
		t.Fatalf("esc: search %v, pane focused %v, sent %d; want the agent back with nothing sent",
			back.leaderOpen, back.paneOwnsKeyboard(), sentToPane(fp))
	}
	m.buildKnown = true
	m.leader = newLeaderDialog(actionRows(levelAgent, defaultLeaderKey, m.leaderConditions()), defaultLeaderKey)
	m = typeText(t, m, "inbox")
	got, _ := update(t, m, keyEnter)
	if !got.inboxOpen || got.focus != focusList || got.pane == nil || sentToPane(fp) != 0 {
		t.Fatalf("inbox %v, focus %v, pane %v, sent %d; want the inbox over the Project, the pane kept",
			got.inboxOpen, got.focus, got.pane != nil, sentToPane(fp))
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

// TestLeaderIsCtrlBracket pins T9.1 under T16.2's levels: ctrl+] is the
// leader, and Ctrl+Space, the leader before it, is an ordinary key. Each row
// presses keys through Update and checks whether the strip is open and the
// exact bytes the agent would read. Load-bearing rows:
//   - "ctrl+] in the agent pane": the strip opens, and ctrl+] is not ALSO
//     typed to the agent.
//   - "ctrl+space in the agent pane types one NUL": the old leader is an
//     ordinary key, not swallowed — exactly one NUL, which is what
//     Ctrl+Space, ctrl+@ and ctrl+2 all send.
//   - "ctrl+] twice in the pane": the agent still gets the key the dashboard
//     took, as exactly one 0x1D, not NUL (the old pass-through) and not two.
func TestLeaderIsCtrlBracket(t *testing.T) {
	fromList := func(t *testing.T) (Model, *fakePane) {
		fp := newFakePane("X")
		m := testModel(sess("a"))
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
		return m, fp
	}
	fromPane := func(t *testing.T) (Model, *fakePane) {
		fp := newFakePane("X")
		return leaderFocusedPane(t, fp), fp
	}
	cases := []struct {
		name      string
		model     func(t *testing.T) (Model, *fakePane)
		keys      []tea.KeyPressMsg
		wantStrip bool
		wantBytes string
	}{
		{name: "ctrl+] in the agent pane", model: fromPane, keys: []tea.KeyPressMsg{leaderKeyPress}, wantStrip: true},
		{name: "ctrl+] on the list opens nothing", model: fromList, keys: []tea.KeyPressMsg{leaderKeyPress}},
		{name: "ctrl+space on the list opens nothing", model: fromList, keys: []tea.KeyPressMsg{ctrlSpacePress}},
		{name: "ctrl+space in the agent pane types one NUL", model: fromPane, keys: []tea.KeyPressMsg{ctrlSpacePress}, wantBytes: "\x00"},
		{name: "ctrl+] twice in the pane types one 0x1D", model: fromPane, keys: []tea.KeyPressMsg{leaderKeyPress, leaderKeyPress}, wantBytes: "\x1d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t)
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			if m.keyStripShown() != tc.wantStrip {
				t.Fatalf("strip shown = %v, want %v", m.keyStripShown(), tc.wantStrip)
			}
			if m.leaderOpen {
				t.Fatal("the actions search opened")
			}
			if got := agentBytes(t, fp); got != tc.wantBytes {
				t.Fatalf("the agent read %q, want %q", got, tc.wantBytes)
			}
		})
	}
}

// levelActionCase is one row of TestEveryActionRuns: a model at a level the
// action works at, and how to tell that it ran.
type levelActionCase struct {
	setup func(t *testing.T) Model
	// ran reports whether the action happened, from the model before and
	// after and the command the key returned.
	ran func(before, after Model, cmd tea.Cmd) bool
	// query is what a by-name row is found by in the `:` search. Empty for
	// a keyed row, which is pressed instead.
	query string
}

// levelActionCases has one case per key-table action, keyed by action;
// TestEveryActionRuns fails if the table gains an action this map lacks. Each
// setup puts the model at a level where the action works and can run, so
// "did not run" can only mean the key did not reach it.
func levelActionCases() map[modeAction]levelActionCase {
	list1 := func(t *testing.T) Model { return testModel(sess("a")) }
	cmdIs := func(cmd tea.Cmd, want func(tea.Msg) bool) bool { return cmd != nil && want(cmd()) }
	planList := func(t *testing.T) Model {
		m := testModel(sess("a"))
		m.deps.StartPlan = func(root, mode, text string) PlanStartResult {
			return PlanStartResult{Outcome: PlanStarted, Root: root}
		}
		return m
	}
	// screen is the all-projects screen open on the fixture's rows, the
	// cursor on alpha (listed), with the add and remove seams wired.
	screen := func(t *testing.T) Model {
		m := testModel(sess("a"))
		m.deps.LoadAllProjects = func() AllProjects { return AllProjects{Rows: allProjectsFixture()} }
		m.deps.AddProject = func(string, string) ProjectEditResult { return ProjectEditResult{} }
		m.deps.RemoveProject = func(string) ProjectEditResult { return ProjectEditResult{} }
		m.deps.StartPlan = func(root, mode, text string) PlanStartResult {
			return PlanStartResult{Outcome: PlanStarted, Root: root}
		}
		m.allProjectsOpen = true
		m.allProjectsKnown = true
		m.allProjectsRows = allProjectsFixture()
		return m
	}
	// buildTab is the Build section with the keyboard and the cursor on the
	// current task, with the status and tokens seams wired.
	buildTab := func(t *testing.T) Model {
		m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
		m.deps.SetTaskStatus = func(string, string, string, []string) TaskStatusResult { return TaskStatusResult{} }
		m.deps.LoadTaskTokens = func(string, string) TaskTokens { return TaskTokens{} }
		return openBuildSection(t, m)
	}
	tokensTab := func(t *testing.T) Model {
		m := buildTab(t)
		m, _ = update(t, m, taskTokensLoadedMsg{gen: m.projectGen, root: m.build.Root, task: m.buildCursorID, rec: TaskTokens{Task: m.buildCursorID}})
		return m
	}
	pane := func(t *testing.T) Model { return leaderFocusedPane(t, newFakePane("X")) }
	// needsElsewhere has one thing needing the owner, a waiting session in
	// another project, so `]` and `[` both switch to it.
	needsElsewhere := func(t *testing.T) Model {
		m := testModel(sess("a"))
		m.projects = []project.Project{{Path: "/repo", Name: "repo"}, {Path: "/p/b", Name: "b"}}
		m.activeProject = 0
		m.projectSessions = map[string][]discover.SessionState{"/p/b": {waitingState("sb")}}
		return m
	}
	return map[modeAction]levelActionCase{
		actionMove: {setup: func(t *testing.T) Model { return testModel(sess("a"), sess("b")) },
			ran: func(b, a Model, _ tea.Cmd) bool { return a.list.Index() != b.list.Index() }},
		actionGoIn: {setup: func(t *testing.T) Model {
			m := testModel(sess("a"))
			m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "a"})
			return m
		}, ran: func(_, a Model, _ tea.Cmd) bool { return a.paneOwnsKeyboard() }},
		actionGoBack: {setup: func(t *testing.T) Model {
			m := testModel(sess("a"))
			m.deps.LoadAllProjects = func() AllProjects { return AllProjects{} }
			return m
		}, ran: func(_, a Model, _ tea.Cmd) bool { return a.allProjectsOpen }},
		actionProjects: {setup: func(t *testing.T) Model {
			m := testModel(sess("a"))
			m.projects = []project.Project{{Path: "/repo", Name: "api"}, {Path: "/repos/afk", Name: "afk"}}
			m.activeProject = 0
			return m
		}, ran: func(_, a Model, _ tea.Cmd) bool { return a.jumpOpen }},
		actionActionsSearch: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.leaderOpen }},
		actionInbox: {setup: func(t *testing.T) Model { m := testModel(sess("a")); m.buildKnown = true; return m },
			ran: func(_, a Model, _ tea.Cmd) bool { return a.inboxOpen }},
		actionDetails: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.detailOwnsKeyboard() }},
		actionRefreshUsage: {setup: func(t *testing.T) Model {
			m := testModel(sess("a"))
			m.deps.RefreshUsage = func() UsageRefreshResult { return UsageRefreshResult{} }
			return m
		}, ran: func(_, a Model, _ tea.Cmd) bool { return a.usageRefreshing }},
		actionKeys: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.keysOpen }},
		actionQuit: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.quitting }},
		actionPlanNew: {setup: screen,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPlanText && a.planTextMode == PlanModeNew }},
		actionProjectAdd: {setup: screen,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputProjectAddFolder }},
		actionProjectRemove: {setup: screen,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputConfirmProjectRemove }},
		// beta holds the fixture's oldest (only) blocking item, so `a` switches
		// to it and waits for its snapshot to open the inbox on bbbbb3.
		actionAnswerOldest: {setup: func(t *testing.T) Model {
			m := screen(t)
			m.projects = []project.Project{{Path: "/repo", Name: "repo", Pinned: true}, {Path: "/p/beta", Name: "beta"}}
			return m
		}, ran: func(_, a Model, _ tea.Cmd) bool { return !a.allProjectsOpen && a.inboxFollow == "bbbbb3" }},
		actionNeedNext: {setup: needsElsewhere,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.repo == "/p/b" }},
		actionNeedPrev: {setup: needsElsewhere,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.repo == "/p/b" }},
		actionPauseToggle: {setup: func(t *testing.T) Model {
			m, _ := buildPauseModel(t, sess("alpha"))
			return withBuild(t, m, buildPauseSnap())
		}, ran: func(_, _ Model, cmd tea.Cmd) bool {
			return cmdIs(cmd, func(msg tea.Msg) bool { _, ok := msg.(buildPausedMsg); return ok })
		}},
		actionNewSession: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.formOpen }},
		actionScratch: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPickProvider }},
		actionClean: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputConfirmClean }},
		actionRestart: {setup: func(t *testing.T) Model { return testModel(stoppedSess("alpha")) },
			ran: func(_, a Model, _ tea.Cmd) bool { return a.restartInFlight.name == "alpha" }},
		actionEditLabel: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputEditLabel }},
		actionTaskStatus: {setup: buildTab,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPickTaskStatus }},
		actionTaskTokens: {setup: tokensTab,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.taskTokensOpen }},
		// The section keys start from Inbox, so 1 (Sessions) has somewhere
		// to move from; tab and shift+tab start from Sessions, the default.
		actionSectionNext: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.focusedSection() == sectionBuild }},
		actionSectionPrev: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.focusedSection() == sectionInbox }},
		actionSectionSessions: {setup: func(t *testing.T) Model { m := list1(t); m.section = sectionInbox; return m },
			ran: func(_, a Model, _ tea.Cmd) bool { return a.focusedSection() == sectionSessions }},
		actionSectionBuild: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.focusedSection() == sectionBuild }},
		actionSectionInbox: {setup: list1,
			ran: func(_, a Model, _ tea.Cmd) bool { return a.focusedSection() == sectionInbox }},
		actionMouseToggle: {setup: pane,
			ran: func(b, a Model, _ tea.Cmd) bool { return a.mouseCapture != b.mouseCapture }},
		// The plan actions are found by any word of their name now that
		// letters always type: no word has to dodge another row's key.
		actionPlanExtend: {setup: planList, query: "extend",
			ran: func(_, _ Model, cmd tea.Cmd) bool {
				return cmdIs(cmd, func(msg tea.Msg) bool { _, ok := msg.(planStartedMsg); return ok })
			}},
		actionPlanAmend: {setup: planList, query: "amend",
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPlanText && a.planTextMode == PlanModeAmend }},
		actionPlanReconcile: {setup: planList, query: "reconcile",
			ran: func(_, a Model, _ tea.Cmd) bool {
				return a.modal == inputPlanText && a.planTextMode == PlanModeReconcile
			}},
		actionPlanFix: {setup: planList, query: "fix a bug",
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputPlanText && a.planTextMode == PlanModeFix }},
		actionPlanCheck: {setup: func(t *testing.T) Model {
			return withPlanStatus(t, testModel(sess("a")), planCheckStatusErrors())
		}, query: "check the plan",
			ran: func(_, a Model, _ tea.Cmd) bool { return a.planCheckOpen }},
		actionPlanApprove: {setup: func(t *testing.T) Model {
			m := withPlanStatus(t, testModel(sess("a")), planApproveStatus(0))
			m.deps.ApprovePlan = func(string) PlanApproveResult { return PlanApproveResult{} }
			return m
		}, query: "approve",
			ran: func(_, a Model, _ tea.Cmd) bool { return a.modal == inputConfirmPlanApprove }},
	}
}

// TestEveryActionRuns is the per-action table T16.2 is built on: one subtest
// per key-table row, each asserting that the row's key — alone at All
// projects and Project, after the leader in the Agent — runs its action, and
// that every action has a help line and is in the `:` search (the `?` half
// lands with W3.T1's keys overlay). A by-name row is run from `:` by its
// name. Every row is load-bearing: the map is checked against keyTable, so a
// row added to the table without a case here fails, and a key that does not
// reach its action fails by name.
func TestEveryActionRuns(t *testing.T) {
	cases := levelActionCases()
	for _, r := range keyTable {
		tc, ok := cases[r.action]
		if !ok {
			t.Errorf("key-table action %v (%q) has no row in levelActionCases", r.action, r.name)
			continue
		}
		t.Run(r.name+" "+r.key, func(t *testing.T) {
			m := tc.setup(t)
			lv := m.level()
			query := tc.query
			if !r.levels.has(lv) {
				// "start a new plan" has two rows, `n` at All projects (the
				// case's setup) and by name inside a project: the by-name
				// row is run from a project, by its name.
				if r.key != "" {
					t.Fatalf("setup is at %v, the row works at %b", lv, r.levels)
				}
				m = testModel(sess("a"))
				m.deps.StartPlan = func(root, mode, text string) PlanStartResult {
					return PlanStartResult{Outcome: PlanStarted, Root: root}
				}
				lv, query = m.level(), r.name
			}
			if strings.TrimSpace(r.help) == "" {
				t.Fatalf("row %q has no help line", r.name)
			}
			rows := actionRows(lv, defaultLeaderKey, m.leaderConditions())
			if !searchSkips(r.action) {
				if _, ok := leaderRowFor(rows, r.action); !ok {
					t.Fatalf("%q is not in the `:` search at %v", r.name, lv)
				}
			}
			var after Model
			var cmd tea.Cmd
			if r.key == "" {
				m, _ = pressAction(t, m, keyRune(':'))
				m = typeText(t, m, query)
				if v := m.leader.visible(); len(v) != 1 || v[0].action != r.action {
					t.Fatalf("query %q leaves %+v, want only %v", query, v, r.action)
				}
				after, cmd = update(t, m, keyEnter)
			} else {
				after, cmd = pressAction(t, m, leaderPress(r.key))
			}
			if !tc.ran(m, after, cmd) {
				t.Fatalf("%q at %v did not run %v", r.key, lv, r.action)
			}
		})
	}
}

// TestRetiredKeysDoNothing presses, on the session list, every key §5 lists
// as replaced that is not a key of its own at Project, and checks it did
// nothing and said nothing: no "moved" notice (T7.2's notice for a retired key is
// gone with the leader table). x and A are the QA scenario's keys. Every row
// is load-bearing as a set: each is a key a hand may still remember.
func TestRetiredKeysDoNothing(t *testing.T) {
	for _, k := range []string{"x", "A", "P", "U", "f", "\\", "g", "w", "b"} {
		t.Run(k, func(t *testing.T) {
			m := testModel(sess("a"))
			m.buildKnown = true
			got, cmd := update(t, m, leaderPress(k))
			if got.status != "" {
				t.Fatalf("%q raised %q, want no notice", k, got.status)
			}
			if cmd != nil {
				t.Errorf("%q returned a command", k)
			}
			if got.formOpen || got.jumpOpen || got.inboxOpen || got.leaderOpen || got.modal != inputNormal ||
				got.quitting || got.keysOpen || got.focus != focusList || got.allProjectsOpen {
				t.Fatalf("%q acted on the dashboard", k)
			}
		})
	}
}

// TestRemovedChordsReachTheAgent pins that the chords the agent pane used to
// keep are the agent's: each arrives at the pane as its ctrl key event and
// the dashboard's focus, view and mouse are untouched. Every row is
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

// TestQuitKeys pins that q and ctrl+c leave the dashboard from every level
// but the Agent, so a stuck dashboard can always be left (A3); the Agent's
// are the agent's (TestAgentKeysReachTheAgent). Every row is load-bearing.
func TestQuitKeys(t *testing.T) {
	cases := []struct {
		name  string
		model func(t *testing.T) Model
		key   tea.KeyPressMsg
	}{
		{"ctrl+c from the list", func(t *testing.T) Model { return testModel(sess("a")) }, chord('c')},
		{"q from the list", func(t *testing.T) Model { return testModel(sess("a")) }, leaderPress("q")},
		{"ctrl+c from the overview", func(t *testing.T) Model { return focusOverview(t, detailFocusFixture(t, "alpha", "alpha")) }, chord('c')},
		{"q from the overview", func(t *testing.T) Model { return focusOverview(t, detailFocusFixture(t, "alpha", "alpha")) }, leaderPress("q")},
		{"q from all projects", func(t *testing.T) Model { m, _, _ := allProjectsModel(t); return openAllProjects(t, m) }, leaderPress("q")},
		{"q with no project", func(t *testing.T) Model { return testModel() }, leaderPress("q")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, cmd := update(t, tc.model(t), tc.key)
			if !got.quitting || cmd == nil {
				t.Fatalf("quitting %v, cmd %v; want the dashboard to quit", got.quitting, cmd != nil)
			}
		})
	}
}

// TestEnterAndEscMoveBetweenLevels walks All projects → Project → Agent and
// back (§5). Load-bearing: each step is one of the level stack's edges, and
// the levels are derived (currentLevel), so each check is on the derived
// level, not a stored one.
func TestEnterAndEscMoveBetweenLevels(t *testing.T) {
	m, _, _ := allProjectsModel(t)
	fp := newFakePane("X")
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	if m.level() != levelProject {
		t.Fatalf("setup: level %v, want Project", m.level())
	}
	m = openAllProjects(t, m)
	if m.level() != levelAllProjects {
		t.Fatalf("esc from the list: level %v, want All projects", m.level())
	}
	// The cursor starts on the first row; the active project's row is the
	// start-up one, so enter on it closes the screen.
	for i, r := range m.allProjectsRows {
		if m.isActiveProjectRoot(r.Root) {
			m.allProjectsCursor = i
		}
	}
	m, _ = update(t, m, keyEnter)
	if m.level() != levelProject {
		t.Fatalf("enter on the active project: level %v, want Project", m.level())
	}
	m, _ = update(t, m, keyEnter)
	if m.level() != levelAgent || !m.paneOwnsKeyboard() {
		t.Fatalf("enter on the session: level %v, pane focused %v; want the Agent", m.level(), m.paneOwnsKeyboard())
	}
	m, _ = update(t, m, leaderKeyPress)
	m, _ = update(t, m, keyEscMsg())
	if m.level() != levelProject || m.pane == nil {
		t.Fatalf("ctrl+] esc: level %v, pane %v; want Project with the pane kept", m.level(), m.pane != nil)
	}
	if sentToPane(fp) != 0 {
		t.Fatalf("the walk sent %d inputs to the agent", sentToPane(fp))
	}
}

// TestHelpKeyShowsAndEscClosesIt pins `?` at Project: it opens the keys
// overlay, and esc closes it before it would go up a level. The esc row is
// load-bearing: one esc must take the overlay away and leave the dashboard on
// Project, or the owner loses their place reading the keys.
func TestHelpKeyShowsAndEscClosesIt(t *testing.T) {
	m := testModel(sess("alpha"), sess("beta"))
	m.deps.LoadAllProjects = func() AllProjects { return AllProjects{} }
	m = pressKey(t, m, keyRune('?'))
	if !m.keysOpen {
		t.Fatal("? did not open the keys overlay")
	}
	m, _ = update(t, m, keyEscMsg())
	if m.keysOpen || m.allProjectsOpen {
		t.Fatalf("esc: keys %v, all projects %v; want the overlay closed and Project kept", m.keysOpen, m.allProjectsOpen)
	}
	m, _ = update(t, m, keyEscMsg())
	if !m.allProjectsOpen {
		t.Fatal("the second esc did not go to All projects")
	}
}
