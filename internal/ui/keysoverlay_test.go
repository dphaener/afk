package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// keyEsc is a plain esc press, shared by the package's tests.
var keyEsc = tea.KeyPressMsg{Code: tea.KeyEsc}

// drainCmd runs cmd and any batch it expands to, returning every message
// produced. It is how tests prove what a key did NOT write or read: a seam is
// only ever reached from inside a Cmd.
func drainCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drainCmd(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// TestKeysKeyDecision is keysKeyDecision row by row. The load-bearing rows:
//
//   - esc while typing or with a query clears it, and only esc with neither
//     closes (A7): one esc too many must never throw the overlay away while
//     the reader is still searching.
//   - j, k, `/` and `?` type while typing: a query has to be able to hold
//     any letter, so the overlay's own keys cannot shadow the filter.
//   - the leader closes whatever the state, as it closes the `:` search.
func TestKeysKeyDecision(t *testing.T) {
	const leader = defaultLeaderKey
	cases := []struct {
		name     string
		key      string
		text     string
		typing   bool
		hasQuery bool
		want     keysOutcome
	}{
		{name: "esc with nothing typed closes", key: "esc", want: keysOutcome{kind: keysClose}},
		{name: "esc while typing clears", key: "esc", typing: true, want: keysOutcome{kind: keysClear}},
		{name: "esc with a kept query clears", key: "esc", hasQuery: true, want: keysOutcome{kind: keysClear}},
		{name: "the leader closes", key: leader, want: keysOutcome{kind: keysClose}},
		{name: "the leader closes while typing", key: leader, typing: true, want: keysOutcome{kind: keysClose}},
		{name: "? closes", key: "?", text: "?", want: keysOutcome{kind: keysClose}},
		{name: "? types while typing", key: "?", text: "?", typing: true, want: keysOutcome{kind: keysType, text: "?"}},
		{name: "/ starts typing", key: "/", text: "/", want: keysOutcome{kind: keysStartTyping}},
		{name: "/ types while typing", key: "/", text: "/", typing: true, want: keysOutcome{kind: keysType, text: "/"}},
		{name: "j scrolls down", key: "j", text: "j", want: keysOutcome{kind: keysScroll, delta: 1}},
		{name: "k scrolls up", key: "k", text: "k", want: keysOutcome{kind: keysScroll, delta: -1}},
		{name: "j types while typing", key: "j", text: "j", typing: true, want: keysOutcome{kind: keysType, text: "j"}},
		{name: "down scrolls while typing", key: "down", typing: true, want: keysOutcome{kind: keysScroll, delta: 1}},
		{name: "up scrolls", key: "up", want: keysOutcome{kind: keysScroll, delta: -1}},
		{name: "pgdown pages", key: "pgdown", want: keysOutcome{kind: keysScroll, delta: 10}},
		{name: "pgup pages", key: "pgup", want: keysOutcome{kind: keysScroll, delta: -10}},
		{name: "home goes to the top", key: "home", want: keysOutcome{kind: keysTop}},
		{name: "end goes to the bottom", key: "end", want: keysOutcome{kind: keysBottom}},
		{name: "enter stops typing", key: "enter", typing: true, want: keysOutcome{kind: keysStopTyping}},
		{name: "enter does nothing otherwise", key: "enter", want: keysOutcome{}},
		{name: "backspace edits while typing", key: "backspace", typing: true, want: keysOutcome{kind: keysBackspace}},
		{name: "q does nothing", key: "q", text: "q", want: keysOutcome{}},
		{name: "q types while typing", key: "q", text: "q", typing: true, want: keysOutcome{kind: keysType, text: "q"}},
		{name: "a control key does not type", key: "ctrl+x", typing: true, want: keysOutcome{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := keysKeyDecision(tc.key, tc.text, leader, tc.typing, tc.hasQuery, 10)
			if got != tc.want {
				t.Fatalf("keysKeyDecision(%q, typing=%v, query=%v) = %v %+v, want %v %+v",
					tc.key, tc.typing, tc.hasQuery, got.kind, got, tc.want.kind, tc.want)
			}
		})
	}
}

// TestKeysOverlayListsEveryAction holds the overlay to the key table: every
// row with a key is listed by its key with its help line, and every row
// reached by name only is listed by its name. With the `:` test
// (TestActionRows), this is "every action has a help
// line and appears in `?` and `:`". It checks the drawn overlay too, at the
// size a person would read it, so a row the layout cut off fails here.
func TestKeysOverlayListsEveryAction(t *testing.T) {
	type listed struct{ key, help string }
	var all []listed
	for _, s := range keysSections(defaultLeaderKey) {
		for _, e := range s.entries {
			all = append(all, listed{e.key, e.help})
		}
	}
	has := func(key, help string) bool {
		for _, l := range all {
			if l.key == key && l.help == help {
				return true
			}
		}
		return false
	}
	m := testModel(sess("a"))
	m.width, m.height = 120, 200
	m, _ = update(t, m, keyRune('?'))
	m.sizeKeysOverlay()
	drawn := xansi.Strip(strings.Join(m.keysView.body(), "\n"))
	for _, r := range keyTable {
		want := r.keyLabel()
		if r.key == "" {
			want = r.name
		}
		if r.help == "" {
			t.Errorf("%s has no help line", r.name)
		}
		if !has(want, r.help) && !has(rowKeyAt(r, levelAgent, defaultLeaderKey), r.help) {
			t.Errorf("%s (%q) is not in the keys overlay", r.name, want)
		}
		if !strings.Contains(drawn, want) {
			t.Errorf("%s (%q) is not drawn in the keys overlay", r.name, want)
		}
	}
	if !has(defaultLeaderKey+" "+defaultLeaderKey, "Send "+defaultLeaderKey+" itself to the agent") {
		t.Error("the overlay does not say how to send the leader to the agent")
	}
}

// TestKeysOverlayDrawsThePathAndTheAgentRule pins §4.9's head: the path
// through the levels and the agent rule, naming the configured leader. The
// configured-leader row is load-bearing: a rule that named ctrl+] while the
// owner had set another leader would tell them to press a key that does
// nothing.
func TestKeysOverlayDrawsThePathAndTheAgentRule(t *testing.T) {
	for _, leader := range []string{defaultLeaderKey, "ctrl+o"} {
		t.Run(leader, func(t *testing.T) {
			m := testModel(sess("a"))
			m.leaderKey = checkLeader(leader)
			m, _ = update(t, m, keyRune('?'))
			frame := xansi.Strip(m.View().Content)
			for _, want := range []string{keysPath, keysAgentRule(leader), "Everywhere", "All projects", "Project", "Agent, after " + leader} {
				if !strings.Contains(frame, want) {
					t.Errorf("the keys overlay lacks %q:\n%s", want, frame)
				}
			}
		})
	}
}

// TestKeysOverlayFilterThenEscTwice is QA's failure case as a test: `/` and a
// query nothing matches say so, the first esc clears the query and keeps the
// overlay, the second closes it, and nothing else about the dashboard moved.
func TestKeysOverlayFilterThenEscTwice(t *testing.T) {
	m := testModel(sess("a"), sess("b"))
	m.width, m.height = 120, 40
	before := m.listSelection()
	m, _ = update(t, m, keyRune('?'))
	for _, r := range "/zzzz" {
		m, _ = update(t, m, keyRune(r))
	}
	if m.keysView.query != "zzzz" {
		t.Fatalf("query = %q, want zzzz", m.keysView.query)
	}
	if frame := xansi.Strip(m.View().Content); !strings.Contains(frame, `No key matches "zzzz"`) {
		t.Fatalf("an empty search does not say so:\n%s", frame)
	}
	m, _ = update(t, m, keyEsc)
	if !m.keysOpen || m.keysView.query != "" || m.keysView.typing {
		t.Fatalf("first esc: open=%v query=%q typing=%v, want the overlay open with the search cleared",
			m.keysOpen, m.keysView.query, m.keysView.typing)
	}
	m, _ = update(t, m, keyEsc)
	if m.keysOpen {
		t.Fatal("second esc did not close the overlay")
	}
	if m.allProjectsOpen || m.focus != focusList || m.listSelection() != before || m.quitting {
		t.Fatal("closing the overlay changed the dashboard under it")
	}
}

// TestKeysOverlayFilterNarrowsByKeyOrHelp: a query keeps the rows whose key
// or help contains it, so "tokens" finds `t` by its help and "R" finds the
// usage key by its key.
func TestKeysOverlayFilterNarrowsByKeyOrHelp(t *testing.T) {
	got := filterKeysSections(keysSections(defaultLeaderKey), "tokens")
	if len(got) != 1 || got[0].title != "Project" || len(got[0].entries) != 1 || got[0].entries[0].key != "t" {
		t.Fatalf("filter tokens = %+v, want only Project's t", got)
	}
	got = filterKeysSections(keysSections(defaultLeaderKey), "usage reading")
	if len(got) != 1 || got[0].entries[0].key != "R" {
		t.Fatalf("filter usage = %+v, want R", got)
	}
}

// TestKeysOverlayScrollsWhenTallerThanTheScreen: at 80×24 the body does not
// fit, so j moves it, k brings it back, and the offset stops at both ends.
func TestKeysOverlayScrollsWhenTallerThanTheScreen(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(t, m, keyRune('?'))
	if len(m.keysView.body()) <= m.keysView.bodyRows() {
		t.Fatalf("setup: the body (%d lines) fits in %d rows", len(m.keysView.body()), m.keysView.bodyRows())
	}
	m, _ = update(t, m, keyRune('k'))
	if m.keysView.offset != 0 {
		t.Fatalf("k at the top moved the offset to %d", m.keysView.offset)
	}
	m, _ = update(t, m, keyRune('j'))
	if m.keysView.offset != 1 {
		t.Fatalf("j moved the offset to %d, want 1", m.keysView.offset)
	}
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	last := len(m.keysView.body()) - m.keysView.bodyRows()
	if m.keysView.offset != last {
		t.Fatalf("end moved the offset to %d, want %d", m.keysView.offset, last)
	}
	m, _ = update(t, m, keyRune('j'))
	if m.keysView.offset != last {
		t.Fatalf("j past the end moved the offset to %d", m.keysView.offset)
	}
	if h := lipgloss.Height(m.renderKeysModal()); h > 24 {
		t.Fatalf("the overlay is %d rows on a 24-row terminal", h)
	}
}

// TestKeysFromTheAgentKeepsTheAgent: the leader then `?` in the Agent opens
// the overlay over the agent with the keyboard left there, every key while it
// is up is the overlay's (nothing reaches the agent), and closing it puts the
// keys back into the agent. The "nothing sent" check is load-bearing: the
// overlay ranks above the pane for exactly this.
func TestKeysFromTheAgentKeepsTheAgent(t *testing.T) {
	fp := newFakePane("X")
	m := focusedModel(t, fp)
	m, _ = update(t, m, leaderKeyPress)
	m, _ = update(t, m, keyRune('?'))
	if !m.keysOpen || m.level() != levelAgent {
		t.Fatalf("leader then ?: keys %v level %v, want the overlay over the Agent", m.keysOpen, m.level())
	}
	for _, k := range []tea.Msg{keyRune('q'), keyRune('/'), keyRune('x'), keyEsc, keyEsc} {
		m, _ = update(t, m, k)
	}
	if m.keysOpen {
		t.Fatal("esc twice did not close the overlay")
	}
	if n := sentToPane(fp); n != 0 {
		t.Fatalf("%d keys reached the agent while the overlay was up", n)
	}
	m, _ = update(t, m, keyRune('z'))
	if n := sentToPane(fp); n != 1 {
		t.Fatalf("after closing, a key reached the agent %d times, want 1", n)
	}
}

// TestAgentOverlaySizeFollowsTheTerminal pins §3.8's agent size and the PTY
// that follows it: at 120 columns the box spans columns 1–118 (the screen
// less one column each side), and below 100 columns the full width; the PTY
// is resized to the box's inside on every terminal resize. Both rows are
// load-bearing, one per side of agentFullWidthBelow.
func TestAgentOverlaySizeFollowsTheTerminal(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		wantX, wantW  int
	}{
		{"120x40", 120, 40, 1, 118},
		{"80x24", 80, 24, 0, 80},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePane("X")
			m := focusedModel(t, fp)
			fp.resizes = nil
			m, _ = update(t, m, tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			_, _, bodyHeight, headerRows, _ := m.mouseFrameLayout()
			x, y, w, h := agentBoxRect(m.width, headerRows, bodyHeight)
			if x != tc.wantX || w != tc.wantW || y != headerRows || h != bodyHeight {
				t.Fatalf("agent box = (%d,%d) %dx%d, want x=%d w=%d y=%d h=%d", x, y, w, h, tc.wantX, tc.wantW, headerRows, bodyHeight)
			}
			wantCols, wantRows := agentContentDims(w, h)
			if len(fp.resizes) == 0 || fp.resizes[len(fp.resizes)-1] != [2]int{wantCols, wantRows} {
				t.Fatalf("pane resized to %v, want %dx%d", fp.resizes, wantCols, wantRows)
			}
			lines := strings.Split(xansi.Strip(m.View().Content), "\n")
			top := []rune(lines[headerRows])
			if len(top) < tc.wantX+tc.wantW || top[tc.wantX] != '╭' || top[tc.wantX+tc.wantW-1] != '╮' {
				t.Fatalf("the agent box's top edge is not at columns %d–%d:\n%s", tc.wantX, tc.wantX+tc.wantW-1, string(top))
			}
			if !strings.Contains(lines[headerRows+1], "Agent · ") {
				t.Fatalf("the agent box has no Agent strip:\n%s", lines[headerRows+1])
			}
		})
	}
}
