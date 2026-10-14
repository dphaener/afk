package ui

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/session"
)

var (
	keyPgUp   = tea.KeyPressMsg{Code: tea.KeyPgUp}
	keyPgDown = tea.KeyPressMsg{Code: tea.KeyPgDown}
	keyHome   = tea.KeyPressMsg{Code: tea.KeyHome}
	keyEnd    = tea.KeyPressMsg{Code: tea.KeyEnd}
)

// manySessions builds n sessions named so their sort order (which
// internal/discover guarantees and rebuildListItems preserves) is their
// creation order. Hosts are STOPPED on purpose: schedulePreview refuses to arm for a stopped
// session, so a test that drains a Cmd batch never waits out previewDelay.
func manySessions(n int) []Session {
	out := make([]Session, n)
	for i := range out {
		out[i] = sess(string(rune('a'+i/26)) + string(rune('a'+i%26)))
	}
	return out
}

// deliverFilterMatches re-delivers whatever list.FilterMatchesMsg a Cmd
// produced, the way the real event loop does. bubbles computes a filter in a
// Cmd, so without this step a typed query narrows nothing and every filter
// assertion below would pass against an unfiltered list.
func deliverFilterMatches(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range collectMsgs(t, cmd) {
		if fm, ok := msg.(list.FilterMatchesMsg); ok {
			m, _ = update(t, m, fm)
		}
	}
	return m
}

// typeFilter presses `/` and then each rune of query, delivering the filter
// results in between. Queries here are kept to one character because
// collectMsgs waits out its whole deadline per keystroke: the textinput
// cursor-blink Cmd shares the batch with the filter Cmd and is a 500ms Tick.
func typeFilter(t *testing.T, m Model, query string) Model {
	t.Helper()
	m, cmd := leaderThen(t, m, '/')
	m = deliverFilterMatches(t, m, cmd)
	for _, r := range query {
		m, cmd = update(t, m, keyRune(r))
		m = deliverFilterMatches(t, m, cmd)
	}
	return m
}

// TestSessionListWrapsSelection is the old cursor-clamp table turned around:
// the list used to clamp at either end, and now it must wrap instead. The
// load-bearing rows are the four that assert the wrap itself — "down
// wraps from the last", "up wraps from zero", "k wraps from zero to the
// last", and "j wraps from the last to zero" — plus the empty-list and
// single-item rows, which are the two cases where a bad wrap reaches
// selected() as an out-of-range index (or a wrong row) rather than failing
// loudly: for an empty list or a list of one, GoToEnd/GoToStart land on
// index 0 because maxCursorIndex() is max(0, ItemsOnPage-1), so these rows
// pin that no special case is needed and none was added. The two 40-session
// rows exercise the same wrap through bubbles' paginator: crossing a page
// boundary on the way to the far end is the one place GoToStart/GoToEnd's
// paging awareness (over VisibleItems, not raw index math) can silently
// disagree with a naive modulo. The pgup/pgdn/home/end rows are unchanged
// from the clamp version — they are the regression guard that wrapping the
// ends doesn't disturb paging in the middle.
func TestSessionListWrapsSelection(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		start   int
		keys    []tea.KeyMsg
		wantSel int
	}{
		{"down within bounds", 3, 0, []tea.KeyMsg{keyDown}, 1},
		{"down wraps from the last", 2, 1, []tea.KeyMsg{keyDown}, 0},
		{"up wraps from zero", 3, 0, []tea.KeyMsg{keyUp}, 2},
		{"j moves down", 3, 0, []tea.KeyMsg{keyRune('j')}, 1},
		{"k moves up", 3, 2, []tea.KeyMsg{keyRune('k')}, 1},
		{"k wraps from zero to the last", 3, 0, []tea.KeyMsg{keyRune('k')}, 2},
		{"j wraps from the last to zero", 3, 2, []tea.KeyMsg{keyRune('j')}, 0},
		{"empty list stays at zero on down and up", 0, 0, []tea.KeyMsg{keyDown, keyUp}, 0},
		{"single-item list stays at zero on down and up", 1, 0, []tea.KeyMsg{keyDown, keyUp}, 0},
		{"end jumps to the last session", 40, 0, []tea.KeyMsg{keyEnd}, 39},
		{"home comes back to the first", 40, 39, []tea.KeyMsg{keyHome}, 0},
		{"up from zero wraps across the page boundary to the last", 40, 0, []tea.KeyMsg{keyUp}, 39},
		{"down from the last wraps across the page boundary to zero", 40, 39, []tea.KeyMsg{keyDown}, 0},
		{"page down then up returns", 40, 0, []tea.KeyMsg{keyPgDown, keyPgUp}, 0},
		{"page down moves off the first page", 40, 0, []tea.KeyMsg{keyPgDown}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(manySessions(tt.count)...)
			m.selectSessionIndex(tt.start)
			for _, k := range tt.keys {
				m, _ = update(t, m, k)
			}
			if tt.name == "page down moves off the first page" {
				if m.listSelection() == 0 {
					t.Fatalf("pgdown did not advance the page: selection = 0")
				}
				return
			}
			if m.listSelection() != tt.wantSel {
				t.Fatalf("selection = %d, want %d", m.listSelection(), tt.wantSel)
			}
		})
	}
}

// TestSessionListWrapRespectsFilter pins wrap to the FILTERED subset, not
// m.sessions. It doesn't fit the (count, start, keys, wantSel) table above
// because listSelection() answers a GlobalIndex — an index into the
// unfiltered list — so once a filter is applied it can no longer name the
// row under test; identity has to come from the selected item's name
// instead.
//
// manySessions names its first 26 sessions "a"+letter and the rest
// "b"+letter, so filtering on "a" keeps every "a*" session plus "ba" (the
// only later session with an 'a' anywhere) and drops "bb", "bc", "bd" — a
// strict subset whose last VISIBLE row, "ba", is not the last row in the
// full 30-session list, "bd". A wrap that recomputed the far end from
// m.sessions instead of m.list.VisibleItems() would land on "bd", and only
// this filtered case would catch it.
func TestSessionListWrapRespectsFilter(t *testing.T) {
	m := testModel(manySessions(30)...)
	m = typeFilter(t, m, "a")
	m, _ = update(t, m, keyEnter)

	if got, total := len(m.list.VisibleItems()), len(m.sessions); got == 0 || got == total {
		t.Fatalf("test setup: filter must narrow to a strict, non-empty subset; visible = %d of %d", got, total)
	}

	m.selectSessionIndex(0) // the first visible row, "aa"
	m, _ = update(t, m, keyUp)

	if s, ok := m.selected(); !ok || s.Name != "ba" {
		t.Fatalf("selected() = %+v (ok=%v), want %q — the last VISIBLE session, not the last unfiltered one", s, ok, "ba")
	}
}

// TestSidebarListClipsToItsHeightBudget is the acceptance criterion the old
// hand-rolled loop could not meet: 60 sessions at a 24-row terminal used to
// print 180 lines into a box sized for 20. The box's own Height would pad but
// never crop, so the frame overflowed and the terminal scrolled the header off.
func TestSidebarListClipsToItsHeightBudget(t *testing.T) {
	for _, count := range []int{0, 1, 3, 60} {
		m := testModel(manySessions(count)...)
		m.width, m.height = 100, 24
		m.ready = true
		m.sizeSessionList()

		listOuter, _, bodyHeight := m.layoutDims()
		got := lipgloss.Height(m.renderList(listOuter, bodyHeight))
		if got != bodyHeight {
			t.Errorf("%d sessions: sidebar height = %d, want exactly the %d-row budget", count, got, bodyHeight)
		}
		if frame := lipgloss.Height(render(m)); frame > m.height {
			t.Errorf("%d sessions: frame height = %d, want at most %d", count, frame, m.height)
		}
	}
}

// TestSessionListRowMatchesRenderRow pins the migration's central promise: the
// delegate draws byte-for-byte what the old renderRowsBody loop drew. The
// fixture carries every optional element at once — label, awaiting-input
// marker, wedged badge, progress, and a branch that differs from the name — so
// a delegate that dropped any one of them fails here rather than in whichever
// end-to-end test happened to cover that element.
func TestSessionListRowMatchesRenderRow(t *testing.T) {
	s := runningSess("alpha")
	s.Label = "the label"
	s.Branch = "feature/alpha-branch"
	s.AwaitingInput = true
	s.WedgedSince = "2026-07-09T23:59:00Z"

	m := testModel(s)
	const width = 60

	// The delegate marks a row selected by index == list.Index(), so the
	// unselected variant has to be drawn at an index the list is NOT on.
	for _, tc := range []struct {
		selected bool
		at       int
	}{{false, 1}, {true, 0}} {
		var buf bytes.Buffer
		l := m.list
		l.SetSize(width, 20)
		l.Select(0)
		d := sessionDelegate{now: m.deps.Now}
		d.Render(&buf, l, tc.at, sessionItem{session: s})

		want := m.renderRow(s, tc.selected, width)
		if buf.String() != want {
			t.Fatalf("selected=%v delegate row differs from renderRow\n got: %q\nwant: %q", tc.selected, buf.String(), want)
		}
	}

	// And the elements really are in the frame, not merely equal to each other:
	// the label, the PHASE and TASKS cells, and the wedged flag (which outranks
	// the awaiting dot in the one flag cell). The on-disk name and the branch
	// are the detail pane's now, not the row's.
	body := xansi.Strip(m.renderRowsBody(width, 20))
	for _, want := range []string{"the label", "EXECUTING", "2/4", wedgedGlyph} {
		if !strings.Contains(body, want) {
			t.Errorf("sidebar body is missing %q:\n%s", want, body)
		}
	}
}

// TestSessionListDoesNotStealQuitOrHelp is the keybinding-ownership criterion.
// bubbles/list binds keys of its own — v2.2.1 gives Quit to "v" and the full
// help toggle to "?" — and either one answering first would take a gesture the
// dashboard owns. `q` must quit the dashboard (running its teardown), `?` must
// be inert here so Wave 6 can bind it, and "v" must do nothing at all.
func TestSessionListDoesNotStealQuitOrHelp(t *testing.T) {
	t.Run("q quits the dashboard", func(t *testing.T) {
		m := testModel(runningSess("a"), runningSess("b"))
		got, cmd := pressAction(t, m, keyRune('q'))
		if !got.quitting {
			t.Error("q must reach the dashboard's own quit handler")
		}
		if cmd == nil {
			t.Error("q must return the dashboard's quit Cmd")
		}
	})

	t.Run("? is inert and does not toggle the list's help", func(t *testing.T) {
		m := testModel(runningSess("a"), runningSess("b"))
		before := m.listSelection()
		got, cmd := update(t, m, keyRune('?'))
		if got.quitting {
			t.Error("? must not quit")
		}
		if cmd != nil {
			t.Errorf("? must be inert in normal mode, got Cmd %T", cmd())
		}
		if got.list.Help.ShowAll {
			t.Error("? reached list.Model's help toggle; Wave 6 needs that key")
		}
		if got.listSelection() != before {
			t.Error("? moved the selection")
		}
	})

	t.Run("v does not quit", func(t *testing.T) {
		m := testModel(runningSess("a"))
		got, cmd := update(t, m, keyRune('v'))
		if got.quitting || cmd != nil {
			t.Error("list's own Quit binding (v under v2.2.1) must not reach tea.Quit")
		}
	})

	t.Run("? types into the filter prompt", func(t *testing.T) {
		m := testModel(runningSess("a"), runningSess("b"))
		m = pressLeader(t, m, keySlash())
		got, _ := update(t, m, keyRune('?'))
		if got.list.FilterInput.Value() != "?" {
			t.Errorf("filter value = %q, want %q — ? must be query text while filtering",
				got.list.FilterInput.Value(), "?")
		}
		if got.list.Help.ShowAll {
			t.Error("? toggled the list's help from inside the filter prompt")
		}
	})
}

// TestSessionListFiltering covers the second half of what the migration buys.
// The load-bearing rows are the two that change what the REST of the dashboard
// sees: selected() must answer from the filtered subset (every per-session key
// reads it), and clearing the filter must restore the full list.
func TestSessionListFiltering(t *testing.T) {
	sessions := []Session{sess("alpha"), sess("beta"), sess("gamma")}

	t.Run("typing narrows the list and owns the keyboard", func(t *testing.T) {
		m := testModel(sessions...)
		m = typeFilter(t, m, "b")

		if mode := activeInputMode(m.modeSnapshot()); mode != inputFilter {
			t.Fatalf("mode = %v, want %v while the prompt is open", mode, inputFilter)
		}
		if got := len(m.list.VisibleItems()); got != 1 {
			t.Fatalf("visible items = %d, want 1", got)
		}
		s, ok := m.selected()
		if !ok || s.Name != "beta" {
			t.Fatalf("selected() = %+v (ok=%v), want beta", s, ok)
		}
	})

	t.Run("enter applies the filter and hands the keyboard back", func(t *testing.T) {
		m := testModel(sessions...)
		m = typeFilter(t, m, "b")
		m, _ = update(t, m, keyEnter)

		if m.list.FilterState() != list.FilterApplied {
			t.Fatalf("filter state = %v, want applied", m.list.FilterState())
		}
		if mode := activeInputMode(m.modeSnapshot()); mode != inputNormal {
			t.Fatalf("mode = %v, want %v after enter", mode, inputNormal)
		}
		if s, _ := m.selected(); s.Name != "beta" {
			t.Fatalf("selected() = %q, want beta", s.Name)
		}
	})

	t.Run("esc clears an applied filter", func(t *testing.T) {
		m := testModel(sessions...)
		m = typeFilter(t, m, "b")
		m, _ = update(t, m, keyEnter)
		m, _ = update(t, m, keyEscMsg())

		if m.list.FilterState() != list.Unfiltered {
			t.Fatalf("filter state = %v, want unfiltered", m.list.FilterState())
		}
		if got := len(m.list.VisibleItems()); got != len(sessions) {
			t.Fatalf("visible items = %d, want all %d back", got, len(sessions))
		}
	})

	t.Run("a discovery tick keeps a filtered list narrowed", func(t *testing.T) {
		m := testModel(sessions...)
		m = typeFilter(t, m, "b")
		m, _ = update(t, m, keyEnter)

		m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: sessions, at: m.deps.Now()})
		if got := len(m.list.VisibleItems()); got != 1 {
			t.Fatalf("visible items after a tick = %d, want the filter still applied", got)
		}
		if s, _ := m.selected(); s.Name != "beta" {
			t.Fatalf("selected() = %q after a tick, want beta", s.Name)
		}
	})

	t.Run("a label is filterable by either name", func(t *testing.T) {
		labelled := sess("gamma")
		labelled.Label = "release prep"
		for _, query := range []string{"r", "g"} {
			m := testModel(sess("alpha"), labelled)
			m = typeFilter(t, m, query)
			if got := len(m.list.VisibleItems()); got != 1 {
				t.Fatalf("query %q: visible items = %d, want 1", query, got)
			}
			if s, _ := m.selected(); s.Name != "gamma" {
				t.Fatalf("query %q: selected %q, want gamma", query, s.Name)
			}
		}
	})

	t.Run("a project switch drops the filter", func(t *testing.T) {
		m := testModel(sessions...)
		m.projects = scannedProjects("/p/a", "/p/b")
		m.activeProject = 0
		m.repo = "/p/a"
		m = typeFilter(t, m, "b")
		m, _ = update(t, m, keyEnter)

		m, _ = m.switchProject("/p/b")
		if m.list.FilterState() != list.Unfiltered {
			t.Fatalf("filter state = %v after a switch, want unfiltered", m.list.FilterState())
		}
	})
}

// TestEverySelectionMoveInvalidatesThePreviewGen is the audit this task most
// risks failing. previewGen is what makes an already-dispatched preview timer
// inert, so EVERY way the selected session can change has to advance it —
// including the four the list component introduced (filter open, filter typing,
// filter results landing, filter cleared) and the four navigation gestures.
// A gesture missing from this table opens a pane for the wrong session, and
// only under a race.
func TestEverySelectionMoveInvalidatesThePreviewGen(t *testing.T) {
	sessions := []Session{sess("alpha"), sess("beta"), sess("gamma")}

	// The filter Cmd the "matches delivered" row arranges, carried across the
	// generation read so that row measures the ASYNC arrival alone rather than
	// the keystroke that requested it.
	var pendingFilterCmd tea.Cmd

	tests := []struct {
		name    string
		arrange func(*testing.T, Model) Model
		move    func(*testing.T, Model) Model
	}{
		{name: "cursor down", move: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyDown); return m }},
		{
			name:    "cursor up",
			arrange: func(t *testing.T, m Model) Model { m.selectSessionIndex(2); return m },
			move:    func(t *testing.T, m Model) Model { m, _ = update(t, m, keyUp); return m },
		},
		{name: "page down", move: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyPgDown); return m }},
		{name: "page up", move: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyPgUp); return m }},
		{name: "home", move: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyHome); return m }},
		{name: "end", move: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyEnd); return m }},
		{
			name: "filter opened",
			move: func(t *testing.T, m Model) Model { m, _ = pressAction(t, m, keySlash()); return m },
		},
		{
			name:    "filter query typed",
			arrange: func(t *testing.T, m Model) Model { m, _ = pressAction(t, m, keySlash()); return m },
			move:    func(t *testing.T, m Model) Model { m, _ = update(t, m, keyRune('b')); return m },
		},
		{
			name: "filter matches delivered",
			arrange: func(t *testing.T, m Model) Model {
				m, _ = pressAction(t, m, keySlash())
				m, pendingFilterCmd = update(t, m, keyRune('b'))
				return m
			},
			move: func(t *testing.T, m Model) Model {
				return deliverFilterMatches(t, m, pendingFilterCmd)
			},
		},
		{
			name: "filter cleared with esc",
			arrange: func(t *testing.T, m Model) Model {
				m = typeFilter(t, m, "b")
				m, _ = update(t, m, keyEnter)
				return m
			},
			move: func(t *testing.T, m Model) Model { m, _ = update(t, m, keyEscMsg()); return m },
		},
		{
			name:    "filter cancelled from the prompt",
			arrange: func(t *testing.T, m Model) Model { return typeFilter(t, m, "b") },
			move:    func(t *testing.T, m Model) Model { m, _ = update(t, m, keyEscMsg()); return m },
		},
		{
			name:    "enter on an empty query",
			arrange: func(t *testing.T, m Model) Model { m, _ = pressAction(t, m, keySlash()); return m },
			move:    func(t *testing.T, m Model) Model { m, _ = update(t, m, keyEnter); return m },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sessions...)
			if tt.arrange != nil {
				m = tt.arrange(t, m)
			}
			// Read IMMEDIATELY before the gesture. Taken any earlier, an
			// arrange step's own bump satisfies the assertion and the row
			// passes with the gesture's invalidation deleted.
			before := m.previewGen
			got := tt.move(t, m)
			if got.previewGen <= before {
				t.Fatalf("previewGen = %d, want it advanced past %d — a stale preview timer stays live",
					got.previewGen, before)
			}
		})
	}
}

// TestFilterPromptArmsNoPreview pins the one refusal this task adds to
// schedulePreview. Arming while the prompt is open would dial whichever session
// the query currently matches — and a dial STEALS the session from any other
// client, including a afk attach in another terminal — so typing a search
// would have side effects on running work. The refusal is arm-side, and it still
// bumps: the row the selection left must not keep a live timer.
func TestFilterPromptArmsNoPreview(t *testing.T) {
	live := []Session{runningSess("alpha"), runningSess("beta")}

	// schedulePreview is asked directly rather than through Update's Cmd batch:
	// the timer it returns is a 300ms tea.Tick and cmdDeadline is also 300ms, so
	// draining the batch to look for one is a coin flip.
	t.Run("no timer while the prompt is open", func(t *testing.T) {
		m := testModel(live...)
		before := m.previewGen
		m, _ = pressAction(t, m, keySlash())
		if m.previewGen <= before {
			t.Fatalf("previewGen = %d, want it advanced past %d even on this refusal", m.previewGen, before)
		}
		if !m.list.SettingFilter() {
			t.Fatal("precondition: / must open the prompt")
		}
		if cmd := m.schedulePreview(); cmd != nil {
			t.Fatal("the prompt armed a preview; typing a search must not dial a session")
		}
	})

	t.Run("accepting the filter arms again", func(t *testing.T) {
		m := testModel(live...)
		m, _ = pressAction(t, m, keySlash())
		m, _ = update(t, m, keyRune('b'))
		m, _ = update(t, m, keyEnter)
		if m.list.SettingFilter() {
			t.Fatal("precondition: enter must close the prompt")
		}
		if cmd := m.schedulePreview(); cmd == nil {
			t.Fatal("accepting the filter must arm a preview for the row it settled on")
		}
	})

	t.Run("cancelling the filter arms again", func(t *testing.T) {
		m := testModel(live...)
		m, _ = pressAction(t, m, keySlash())
		m, _ = update(t, m, keyEscMsg())
		if m.list.SettingFilter() {
			t.Fatal("precondition: esc must close the prompt")
		}
		if cmd := m.schedulePreview(); cmd == nil {
			t.Fatal("cancelling the filter must arm a preview for the row it settled on")
		}
	})
}

// TestDiscoveryTickStillDoesNotBumpThePreviewGen is the other half of the
// audit: applyLoaded deliberately does NOT invalidate, because applyPreviewTimer
// re-checks the armed session by NAME. Bumping here would make that check dead
// code and hide the next bug in it.
func TestDiscoveryTickStillDoesNotBumpThePreviewGen(t *testing.T) {
	m := testModel(sess("alpha"), sess("beta"))
	before := m.previewGen
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen,
		sessions: []Session{sess("alpha"), sess("beta")},
		at:       m.deps.Now(),
	})
	if m.previewGen != before {
		t.Fatalf("previewGen = %d, want it unchanged at %d", m.previewGen, before)
	}
}

// TestFilterMatchesMsgIsNotStolenFromTheOverlayLists pins the message-ownership
// rule. Three lists in this package can ask for a filter result and the message
// names no sender, so applying the switcher's to the sidebar would put project
// rows into the session list — every row would fail its type assertion, the
// sidebar would render blank, and selected() would answer false with the user's
// cursor apparently on a session.
func TestFilterMatchesMsgIsNotStolenFromTheOverlayLists(t *testing.T) {
	m := testModel(sess("alpha"), sess("beta"))
	m.projects = scannedProjects("/p/a", "/p/b")
	m.activeProject = 0
	m.switcherOpen = true

	// A result shaped like the switcher's: items that are NOT sessionItems.
	foreign := list.FilterMatchesMsg(nil)
	got, _ := update(t, m, foreign)

	if len(got.list.Items()) != 2 {
		t.Fatalf("session list items = %d, want the 2 it started with", len(got.list.Items()))
	}
	if s, ok := got.selected(); !ok || s.Name != "alpha" {
		t.Fatalf("selected() = %+v (ok=%v), want alpha still selected", s, ok)
	}
}

// TestSessionRowsRenderAtUniformWidth pins the delegate-level half of the
// W5.T1 fix directly at renderSessionRow (the string sessionDelegate.Render
// prints verbatim): the highlighted row and an unselected row render at the
// same width, and that width is the row width itself. Before the fix only the
// selected branch padded (theme.rowSelected.Width), so an unselected row's
// block stayed at its natural length — the switcher's W7.T1 defect (2) on the
// dashboard's primary surface.
//
// It measures renderSessionRow's output per row rather than widths read back
// out of list.Model.View(): View joins rows with lipgloss.JoinVertical, which
// pads every line shorter than the widest one regardless of what the delegate
// did, so a view-level comparison passes even with the unselected padding
// removed entirely (W7.T1's recorded vacuous-test trap).
//
// The rows are a PENDING row and a STALE row on purpose: an executing row's
// progress bar expands to fill line2 to the full width naturally, which would
// make the comparison pass without any padding at all. The TrimRight check
// below is what proves the chosen rows' equality comes from padding, not from
// content that happens to fill the width.
func TestSessionRowsRenderAtUniformWidth(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	const width = 40

	pending := sess("pending-row")
	pending.Pending = true
	stale := sess("stale-row")
	stale.StatusStale = true

	for _, c := range []struct {
		label string
		s     Session
	}{
		{"pending", pending},
		{"stale", stale},
	} {
		t.Run(c.label, func(t *testing.T) {
			selected := renderSessionRow(c.s, true, width, now)
			unselected := renderSessionRow(c.s, false, width, now)

			if got, want := lipgloss.Width(unselected), lipgloss.Width(selected); got != want {
				t.Errorf("unselected row width = %d, want %d (same as the selected row)", got, want)
			}
			if got := lipgloss.Width(unselected); got != width {
				t.Errorf("unselected row width = %d, want the full row width %d", got, width)
			}
			for _, ln := range strings.Split(xansi.Strip(unselected), "\n") {
				if lipgloss.Width(strings.TrimRight(ln, " ")) >= width {
					t.Fatalf("row line %q fills the width naturally — this case proves nothing about padding; pick sparser content", ln)
				}
			}
		})
	}
}

// TestSessionListFullWidthRowDoesNotWrap is the container half of the W5.T1
// fix, in the style of TestSwitcherListWidthMatchesRenderedBoxUsableWidth
// (switcher_test.go): every row composeRow emits is EXACTLY sidebarContentWidth
// wide, and each must survive renderList's real box — production styles and
// helpers, no synthetic approximation — as one contiguous line, at 80 and at
// 120 columns. Before the fix, renderList handed sizedBox the bare contentW
// where content-plus-padding was wanted, so the box's wrap point sat inside
// contentW and every row wrapped. The symptom is a box taller than the height
// it was given (a wrapped row costs a line) and a row split across two lines.
func TestSessionListFullWidthRowDoesNotWrap(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := testModel(sess("alpha"))
			m.width = width
			listOuter, _, bodyHeight := m.layoutDims()
			contentW := sidebarContentWidth(listOuter)

			p := sess(strings.Repeat("x", contentW))
			p.Pending = true
			m.setSessions([]Session{sess("alpha"), p})

			row := xansi.Strip(m.renderRow(p, false, contentW))
			if got := lipgloss.Width(row); got != contentW {
				t.Fatalf("test premise: the row is %d cells, want exactly contentW %d", got, contentW)
			}

			out := xansi.Strip(m.renderList(listOuter, bodyHeight))
			if got := lipgloss.Height(out); got != bodyHeight {
				t.Fatalf("box is %d rows, want %d — a full-width row wrapped (contentW=%d, listOuter=%d):\n%s",
					got, bodyHeight, contentW, listOuter, out)
			}
			if !strings.Contains(out, row) {
				t.Fatalf("full-width row is not in the box as one line (contentW=%d, listOuter=%d):\n%s",
					contentW, listOuter, out)
			}
		})
	}
}

// TestSelectedRowHighlightSpansTheWholeRow pins the fix spanStyle exists for
// (styles.go). lipgloss closes each styled cell with an SGR reset, and a reset
// clears the ENCLOSING selection background too, so before the fix the
// highlight bar ended at the row's first styled cell — the host-liveness dot,
// two cells in — no matter how wide the sidebar was. That is a bar the eye
// cannot follow, and it is why the cursor row was hard to find.
//
// The rows are chosen for the styled cells they carry, since each one is a
// reset the bar has to survive: an executing row awaiting input (coloured dot,
// bold name, phase hue, muted count, warning flag — five), a fault row (whose
// PHASE cell is a badge with its own background, the case most likely to
// swallow the selection colour), and a pending row (the sparsest branch, which
// must still paint edge to edge rather than stopping where its content does).
func TestSelectedRowHighlightSpansTheWholeRow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	const width = 44
	awaiting := sess("feat-auth")
	awaiting.HostRunning = true
	awaiting.AwaitingInput = true
	awaiting.Progress = discover.Progress{Total: 7, Complete: 3}

	fault := sess("old-migration")
	fault.Kind = session.KindWorktreeMissing

	pending := sess("brand-new")
	pending.Pending = true

	for _, c := range []struct {
		label string
		s     Session
	}{
		{"awaiting", awaiting},
		{"fault", fault},
		{"pending", pending},
	} {
		t.Run(c.label, func(t *testing.T) {
			assertHighlightSpansRow(t, renderSessionRow(c.s, true, width, now), width)
		})
	}
}

// TestUnselectedRowCarriesNoHighlight is the other half: spanStyle is applied
// only on the selected branch, so an unselected row must stay unpainted. Without
// this, a change that pushed the re-assertion into composeRow would highlight
// every row and pass the test above.
func TestUnselectedRowCarriesNoHighlight(t *testing.T) {
	s := sess("feat-auth")
	s.HostRunning = true
	row := renderSessionRow(s, false, 44, time.Unix(1_000_000, 0))
	if strings.Contains(row, "48;2;") {
		t.Errorf("unselected row carries a background: %q", row)
	}
}

// TestRowMarkerColumnsAreUniform pins cursorRail at one cell. The rail replaced
// a one-cell "›", and rowColumns budgets rowMarkerCells for it: a two-cell glyph
// would push the name, PHASE and TASKS columns one cell right on the selected
// row only, so the table's columns would jog as the cursor moved down it.
func TestRowMarkerColumnsAreUniform(t *testing.T) {
	if got := lipgloss.Width(cursorRail); got != 1 {
		t.Errorf("lipgloss.Width(cursorRail) = %d, want 1", got)
	}
	selected, blank := lipgloss.Width(rowMarker(true)), lipgloss.Width(rowMarker(false))
	if selected != blank || selected != rowMarkerCells {
		t.Errorf("marker widths = %d selected / %d blank, want both %d", selected, blank, rowMarkerCells)
	}
}
