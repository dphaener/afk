package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

// projectViewSnap is buildTabSnap with the running task (T1.2) linked to the
// session alpha in the ledger, and two open items and an answered one in the
// inbox, so every section has rows and a selection.
func projectViewSnap() buildview.Snapshot {
	snap := buildTabSnap()
	snap.Rows[1].Session = "alpha"
	snap.Items = []inbox.Item{
		{ID: "q-1", Kind: "question", Blocking: true, Task: "T3.1", Title: "Drop the table?"},
		{ID: "n-2", Kind: "notice", Task: "T4.1", Title: "Used the staging key"},
		{ID: "a-3", Kind: "question", Title: "Which provider?", Answer: "Postal"},
	}
	return snap
}

// TestProjectViewSectionFocus drives the section keys through the model. The
// load-bearing rows are the two wraps (tab past Inbox, shift+tab before
// Sessions), which are where a counter that does not wrap would land on a
// section that does not exist, and "2 then 1", which pins that a digit goes
// straight to its section from anywhere rather than stepping.
func TestProjectViewSectionFocus(t *testing.T) {
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	backtab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	cases := []struct {
		name string
		keys []tea.Msg
		want projectSection
	}{
		{"starts on Sessions", nil, sectionSessions},
		{"tab", []tea.Msg{tab}, sectionBuild},
		{"tab twice", []tea.Msg{tab, tab}, sectionInbox},
		{"tab wraps past Inbox", []tea.Msg{tab, tab, tab}, sectionSessions},
		{"shift+tab wraps before Sessions", []tea.Msg{backtab}, sectionInbox},
		{"shift+tab twice", []tea.Msg{backtab, backtab}, sectionBuild},
		{"1", []tea.Msg{tab, keyRune('1')}, sectionSessions},
		{"2", []tea.Msg{keyRune('2')}, sectionBuild},
		{"3", []tea.Msg{keyRune('3')}, sectionInbox},
		{"2 then 1", []tea.Msg{keyRune('2'), keyRune('1')}, sectionSessions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := withBuild(t, testModel(sess("alpha")), projectViewSnap())
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			if got := m.focusedSection(); got != tc.want {
				t.Fatalf("section = %v, want %v", got, tc.want)
			}
			if m.focus != focusList || m.level() != levelProject {
				t.Fatalf("focus=%v level=%v, want the project view to keep the keyboard", m.focus, m.level())
			}
			// The focused section's strip is the one drawn focused; the
			// frame is checked for all three names so a section that drops
			// off the screen fails here.
			frame := render(m)
			for _, name := range []string{"1 Sessions", "2 Build", "3 Inbox"} {
				if !strings.Contains(frame, name) {
					t.Errorf("the frame has no %q:\n%s", name, frame)
				}
			}
		})
	}
}

// TestProjectViewSAndTNeedBuild pins `s` and `t` on the project view: away
// from the Build section they refuse with a notice naming the keys that get
// there (2 and tab), and open nothing; in it they open the status picker and
// the task's details. Every row is load-bearing: each is a section the owner
// can press the key in.
func TestProjectViewSAndTNeedBuild(t *testing.T) {
	refusal := reasonSentence(taskStatusOpenBuildFirst(defaultLeaderKey))
	cases := []struct {
		name    string
		section rune
		key     rune
		opened  func(Model) bool
		refused bool
	}{
		{"s with Sessions focused", '1', 's', func(m Model) bool { return m.modal == inputPickTaskStatus }, true},
		{"t with Sessions focused", '1', 't', func(m Model) bool { return m.taskTokensOpen }, true},
		{"s with Inbox focused", '3', 's', func(m Model) bool { return m.modal == inputPickTaskStatus }, true},
		{"t with Inbox focused", '3', 't', func(m Model) bool { return m.taskTokensOpen }, true},
		{"s in Build opens the picker", '2', 's', func(m Model) bool { return m.modal == inputPickTaskStatus }, false},
		{"t in Build opens the details", '2', 't', func(m Model) bool { return m.taskTokensOpen }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := withBuild(t, testModel(sess("alpha")), projectViewSnap())
			m.deps.SetTaskStatus = func(string, string, string, []string) TaskStatusResult { return TaskStatusResult{} }
			m.deps.LoadTaskTokens = func(_, task string) TaskTokens { return TaskTokens{Task: task} }
			m, _ = update(t, m, keyRune(tc.section))
			m, cmd := update(t, m, keyRune(tc.key))
			for _, msg := range collectMsgs(t, cmd) {
				m, _ = update(t, m, msg)
			}
			t.Logf("after %c then %c: opened=%v notice=%q", tc.section, tc.key, tc.opened(m), m.status)
			if got := tc.opened(m); got == tc.refused {
				t.Fatalf("opened = %v, want %v (status %q)", got, !tc.refused, m.status)
			}
			if tc.refused {
				if m.status != refusal {
					t.Errorf("notice %q, want %q", m.status, refusal)
				}
				if !strings.Contains(m.status, "2") || !strings.Contains(m.status, "tab") {
					t.Errorf("the notice %q does not name 2 and tab", m.status)
				}
			}
		})
	}
}

// TestProjectViewEnter pins what enter opens for each section's selection.
// The load-bearing rows are one per target (the agent, the task's details
// with its id, the inbox on the item's id) and the two empty sections, where
// enter must open nothing at all rather than fall back to the agent.
func TestProjectViewEnter(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) Model
		check func(t *testing.T, m Model)
	}{
		{"a session opens its agent", func(t *testing.T) Model {
			m := withBuild(t, testModel(sess("alpha")), projectViewSnap())
			m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
			return m
		}, func(t *testing.T, m Model) {
			if !m.paneOwnsKeyboard() {
				t.Fatalf("enter on a session did not open its agent (level %v)", m.level())
			}
		}},
		{"a task opens its details with its id", func(t *testing.T) Model {
			m := withBuild(t, testModel(sess("alpha")), projectViewSnap())
			m, _ = update(t, m, keyRune('2'))
			m, _ = update(t, m, keyDown)
			return m
		}, func(t *testing.T, m Model) {
			if !m.taskTokensOpen || m.taskTokensView.task != "T2.1" || m.taskTokensView.row.ID != "T2.1" {
				t.Fatalf("open=%v task=%q row=%q, want the details of T2.1", m.taskTokensOpen, m.taskTokensView.task, m.taskTokensView.row.ID)
			}
			if box := xansi.Strip(m.renderTaskTokensModal()); !strings.Contains(box, "T2.1") || !strings.Contains(box, "shipping") {
				t.Errorf("the details box does not name the task and its status:\n%s", box)
			}
		}},
		{"a task opens its details through the token read", func(t *testing.T) Model {
			m := withBuild(t, testModel(sess("alpha")), projectViewSnap())
			m.deps.LoadTaskTokens = func(_, task string) TaskTokens { return TaskTokens{Task: task} }
			m, _ = update(t, m, keyRune('2'))
			return m
		}, func(t *testing.T, m Model) {
			if !m.taskTokensOpen || m.taskTokensView.row.ID != "T1.2" {
				t.Fatalf("open=%v row=%q, want the details of T1.2", m.taskTokensOpen, m.taskTokensView.row.ID)
			}
		}},
		{"an inbox item opens the inbox on it", func(t *testing.T) Model {
			m := withBuild(t, testModel(sess("alpha")), projectViewSnap())
			m, _ = update(t, m, keyRune('3'))
			m, _ = update(t, m, keyDown)
			return m
		}, func(t *testing.T, m Model) {
			if !m.inboxOpen || m.inbox.screen != inboxScreenItem {
				t.Fatalf("open=%v screen=%v, want the inbox on an item", m.inboxOpen, m.inbox.screen)
			}
			if got := m.inbox.items[m.inbox.cursor].ID; got != "n-2" {
				t.Errorf("the inbox opened on %q, want n-2", got)
			}
		}},
		{"an empty Build section opens nothing", func(t *testing.T) Model {
			m := withBuild(t, testModel(sess("alpha")), buildview.Snapshot{Root: "/repo", HasBuild: true})
			m, _ = update(t, m, keyRune('2'))
			return m
		}, assertNothingOpened},
		{"an empty Inbox section opens nothing", func(t *testing.T) Model {
			m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
			m, _ = update(t, m, keyRune('3'))
			return m
		}, assertNothingOpened},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			m, cmd := update(t, m, keyEnter)
			for _, msg := range collectMsgs(t, cmd) {
				m, _ = update(t, m, msg)
			}
			tc.check(t, m)
		})
	}
}

// assertNothingOpened fails unless the dashboard is still on the project
// view with nothing over it.
func assertNothingOpened(t *testing.T, m Model) {
	t.Helper()
	if m.taskTokensOpen || m.inboxOpen || m.modal != inputNormal || m.focus != focusList || m.level() != levelProject {
		t.Fatalf("enter on an empty section opened something (tokens=%v inbox=%v modal=%v focus=%v level=%v)",
			m.taskTokensOpen, m.inboxOpen, m.modal, m.focus, m.level())
	}
}

// TestProjectViewLinkedRow pins the linked row following the selected
// session. Load-bearing rows: a session the ledger names on a task links that
// task's row ("◂ alpha" on T1.2's line), a session with no task links
// nothing, and a task whose session is not on the list is never drawn linked
// (the link belongs to the selection, not to the ledger alone).
func TestProjectViewLinkedRow(t *testing.T) {
	cases := []struct {
		name     string
		sessions []Session
		mutate   func(*buildview.Snapshot)
		down     int
		wantLink string
		wantRow  string
	}{
		{"a session with a task links it", []Session{sess("alpha"), sess("beta")}, nil, 0, "◂ alpha", "T1.2"},
		{"a session with no task links nothing", []Session{sess("alpha"), sess("beta")}, nil, 1, "", ""},
		{"a task with no listed session is not linked", []Session{sess("beta")},
			func(s *buildview.Snapshot) { s.Rows[1].Session = "ghost" }, 0, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := projectViewSnap()
			if tc.mutate != nil {
				tc.mutate(&snap)
			}
			m := withBuild(t, testModel(tc.sessions...), snap)
			for range tc.down {
				m, _ = update(t, m, keyDown)
			}
			m.clearStatus()
			frame := render(m)
			if tc.wantLink == "" {
				if strings.Contains(frame, "◂") {
					t.Fatalf("a row is linked:\n%s", frame)
				}
				return
			}
			found := false
			for _, ln := range strings.Split(frame, "\n") {
				if strings.Contains(ln, tc.wantRow+"  ") && strings.Contains(ln, tc.wantLink) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no %s row ends %q:\n%s", tc.wantRow, tc.wantLink, frame)
			}
		})
	}
}

// TestProjectViewSessionOrder pins that the list the cursor walks is the
// order the Sessions section draws: Needs you, Working, Done. The fixture's
// names are chosen against that order (the session that needs you sorts last
// by name), so "down crosses a group boundary" fails if the list is still in
// name order. "a refresh that moves a focused session" pins the
// selection pinned to its session (holdSelection) when the overview has the
// keyboard and the session changes group, and the placeholder row pins
// pending spawns landing in Working, by name, without moving the selection.
func TestProjectViewSessionOrder(t *testing.T) {
	asks := sess("zed")
	asks.AwaitingInput = true
	done := sess("able")
	done.Phase = state.PhaseDone
	work := sess("mid")
	names := func(m Model) string {
		var out []string
		for _, s := range m.sessions {
			out = append(out, s.Name)
		}
		return strings.Join(out, ",")
	}

	t.Run("stored in screen order", func(t *testing.T) {
		m := testModel(done, work, asks)
		if got := names(m); got != "zed,mid,able" {
			t.Fatalf("order %s, want zed,mid,able", got)
		}
	})

	t.Run("down crosses a group boundary onto the right session", func(t *testing.T) {
		m := testModel(done, work, asks)
		for _, want := range []string{"zed", "mid", "able"} {
			if s, _ := m.selected(); s.Name != want {
				t.Fatalf("selected %q, want %q", s.Name, want)
			}
			m, _ = update(t, m, keyDown)
		}
	})

	t.Run("a refresh that moves a focused session keeps it selected", func(t *testing.T) {
		// With the overview focused the selection is pinned by name
		// (holdSelection), so the reorder must not hand the overview to the
		// session that took mid's place on screen.
		m := testModel(done, work, asks)
		m, _ = update(t, m, keyDown) // mid
		m = focusOverview(t, m)
		moved := work
		moved.AwaitingInput = true
		m = m.applyLoaded(sessionsLoadedMsg{sessions: []Session{done, moved, asks}})
		if got := names(m); got != "mid,zed,able" {
			t.Fatalf("order %s, want mid,zed,able", got)
		}
		if s, _ := m.selected(); s.Name != "mid" {
			t.Errorf("selected %q after the reorder, want mid", s.Name)
		}
	})

	t.Run("a placeholder lands in Working by name", func(t *testing.T) {
		m := testModel(done, work, asks)
		m.addPendingSpawn("new")
		m.mergePendingSpawnsNow()
		if got := names(m); got != "zed,mid,new,able" {
			t.Fatalf("order %s, want zed,mid,new,able", got)
		}
		if s, _ := m.selected(); s.Name != "zed" {
			t.Errorf("the placeholder moved the selection to %q", s.Name)
		}
	})
}

// TestScreenOrderHelpers is the table for the pure order helpers. The
// load-bearing row is "equal names keep their order": a worktree fault and a
// session can share a name, and discovery puts the fault first, which the
// stable sort must keep.
func TestScreenOrderHelpers(t *testing.T) {
	asks := sess("b")
	asks.AwaitingInput = true
	done := sess("a")
	done.Phase = state.PhaseDone
	fault := sess("c")
	fault.Kind = session.KindWorktreeNoSession
	twin := sess("c")
	twin.AwaitingInput = true
	cases := []struct {
		name string
		in   []Session
		want []int
	}{
		{"groups before names", []Session{done, sess("x"), asks}, []int{2, 1, 0}},
		{"already in order", []Session{asks, sess("x"), done}, []int{0, 1, 2}},
		{"empty", nil, []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := screenOrderIndex(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("order %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("order %v, want %v", got, tc.want)
				}
			}
		})
	}
	t.Run("equal names keep their order", func(t *testing.T) {
		if sessionGroupOf(fault) != sessionGroupOf(twin) {
			t.Fatal("fixture broken: the fault and its twin must share a group")
		}
		if got := screenOrderIndex([]Session{fault, twin}); got[0] != 0 || got[1] != 1 {
			t.Fatalf("order %v, want the fault first", got)
		}
	})
	t.Run("insert at", func(t *testing.T) {
		list := []Session{asks, sess("m"), done}
		row := Session{Pending: true}
		row.Name = "z"
		if got := screenInsertAt(list, row); got != 2 {
			t.Errorf("insert at %d, want 2 (end of Working)", got)
		}
		row.Name = "a"
		if got := screenInsertAt(list, row); got != 1 {
			t.Errorf("insert at %d, want 1 (start of Working)", got)
		}
	})
}

// TestLandInboxCursor is the table for the Inbox section's cursor across a
// read. Load-bearing rows: the item kept wherever it moved (an item answered
// above it must not move the cursor to a neighbour), and the item gone, which
// lands on the row now at its old place.
func TestLandInboxCursor(t *testing.T) {
	items := func(ids ...string) []inbox.Item {
		var out []inbox.Item
		for _, id := range ids {
			out = append(out, inbox.Item{ID: id})
		}
		return out
	}
	cases := []struct {
		name      string
		prevID    string
		prevIndex int
		items     []inbox.Item
		wantID    string
		wantIndex int
	}{
		{"first read starts at the top", "", 0, items("a", "b"), "a", 0},
		{"the item is kept where it moved", "b", 1, items("b", "c"), "b", 0},
		{"the item gone lands at its place", "b", 1, items("a", "c"), "c", 1},
		{"the last item gone clamps", "c", 2, items("a", "b"), "b", 1},
		{"no items", "a", 0, nil, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, i := landInboxCursor(tc.prevID, tc.prevIndex, tc.items)
			if id != tc.wantID || i != tc.wantIndex {
				t.Fatalf("landed on (%q, %d), want (%q, %d)", id, i, tc.wantID, tc.wantIndex)
			}
		})
	}
}

// TestProjectHitAt pins where a cell of the project view's box falls, the
// geometry every click reads. The layout is a 116×37 box (120×40 less the
// overlay's margins, the top bar and the status bar) with a 10-row read-only
// pane. Load-bearing rows: each section's first body row (row 0, the first
// row drawn under its strip), each strip (row -1: focus without a row), the
// gap between the columns and the read-only pane (no section: a click there
// must not move a selection), and both narrow rows, which pin that the one
// section shown takes the whole column area whichever it is.
func TestProjectHitAt(t *testing.T) {
	wide := projectLayoutFor(116, 37, 10, 9, 2, false)
	narrow := projectLayoutFor(80, 22, 0, 9, 2, true)
	rightX := projectColsLeft + wide.leftW + 1
	cases := []struct {
		name   string
		l      projectLayout
		shown  projectSection
		x, y   int
		want   projectHit
		wantOK bool
	}{
		{"sessions first row", wide, sectionSessions, projectColsLeft + 3, projectColsTop + 2, projectHit{sectionSessions, 0}, true},
		{"sessions strip", wide, sectionSessions, projectColsLeft + 3, projectColsTop + 1, projectHit{sectionSessions, -1}, true},
		{"sessions border", wide, sectionSessions, projectColsLeft, projectColsTop + 4, projectHit{sectionSessions, -1}, true},
		{"build first row", wide, sectionSessions, rightX + 3, projectColsTop + 2, projectHit{sectionBuild, 0}, true},
		{"build third row", wide, sectionInbox, rightX + 3, projectColsTop + 4, projectHit{sectionBuild, 2}, true},
		{"inbox strip", wide, sectionSessions, rightX + 3, projectColsTop + wide.buildH + 1, projectHit{sectionInbox, -1}, true},
		{"inbox first row", wide, sectionSessions, rightX + 3, projectColsTop + wide.buildH + 2, projectHit{sectionInbox, 0}, true},
		{"gap between the columns", wide, sectionSessions, projectColsLeft + wide.leftW, projectColsTop + 4, projectHit{}, false},
		{"read-only pane", wide, sectionSessions, projectColsLeft + 3, projectColsTop + wide.sessionsH + 3, projectHit{}, false},
		{"band", wide, sectionSessions, projectColsLeft + 3, projectColsTop - 1, projectHit{}, false},
		{"chips", wide, sectionSessions, projectColsLeft + 3, projectColsTop + wide.colH, projectHit{}, false},
		{"box border", wide, sectionSessions, 0, projectColsTop + 4, projectHit{}, false},
		{"narrow build", narrow, sectionBuild, projectColsLeft + 60, projectColsTop + 2, projectHit{sectionBuild, 0}, true},
		{"narrow inbox lower row", narrow, sectionInbox, projectColsLeft + 3, projectColsTop + 6, projectHit{sectionInbox, 4}, true},
	}
	if wide.summaryH == 0 || wide.narrow || !narrow.narrow {
		t.Fatalf("fixture broken: wide %+v narrow %+v", wide, narrow)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := projectHitAt(tc.l, tc.shown, tc.x, tc.y)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("projectHitAt(%d, %d) = %+v, %v; want %+v, %v", tc.x, tc.y, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestProjectViewNarrow pins §4.2's narrow layout through the model at 80×24:
// one section fills the body, its strip names all three, there is no
// read-only pane, and `2`/`3`/tab change which section is shown. The "no
// read-only pane" check is load-bearing: the pane's labels are what a wide
// layout leaking into the narrow one would show first.
func TestProjectViewNarrow(t *testing.T) {
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	cases := []struct {
		name  string
		keys  []tea.Msg
		shows []string
		hides []string
	}{
		{"opens on Sessions", nil, []string{"alpha", "beta"}, []string{"T2.1", "q-1"}},
		{"2 shows Build", []tea.Msg{keyRune('2')}, []string{"T2.1", "T4.1"}, []string{"beta", "q-1"}},
		{"3 shows Inbox", []tea.Msg{keyRune('3')}, []string{"q-1", "n-2"}, []string{"beta", "T2.1"}},
		{"tab shows Build", []tea.Msg{tab}, []string{"T2.1"}, []string{"beta", "q-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("alpha"), sess("beta"))
			m.width, m.height = 80, 24
			m = withBuild(t, m, projectViewSnap())
			m.clearStatus()
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			frame := xansi.Strip(render(m))
			if !strings.Contains(frame, "1 Sessions · 2 Build · 3 Inbox") {
				t.Errorf("the strip does not name all three:\n%s", frame)
			}
			for _, w := range tc.shows {
				if !strings.Contains(frame, w) {
					t.Errorf("the frame has no %q:\n%s", w, frame)
				}
			}
			for _, w := range tc.hides {
				if strings.Contains(frame, w) {
					t.Errorf("the frame shows %q from a section not shown:\n%s", w, frame)
				}
			}
			for _, label := range []string{"branch  ", "phase   "} {
				if strings.Contains(frame, label) {
					t.Errorf("the narrow layout draws the read-only pane (%q):\n%s", label, frame)
				}
			}
		})
	}
}

// TestProjectViewOverlay pins where the project view's box sits from 100
// columns (§3.8, A10): two columns in from each side, over the home screen
// when All projects has rows and on the plain ground when it has none. The
// "home" row is load-bearing: the margins show the home screen's own border,
// which is what tells the owner the project is laid over it.
func TestProjectViewOverlay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		home   bool
		margin string
	}{
		{"plain ground", false, "  "},
		{"home", true, "│ "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := withBuild(t, testModel(sess("alpha"), sess("beta")), projectViewSnap())
			if tc.home {
				m.allProjectsKnown = true
				m.allProjectsRows = allProjectsFixture()
			}
			m.clearStatus()
			lines := strings.Split(xansi.Strip(render(m)), "\n")
			_, y := findSentinel(render(m), "1 Sessions")
			if y < 0 {
				t.Fatalf("no Sessions section:\n%s", strings.Join(lines, "\n"))
			}
			ln := []rune(lines[y])
			if got := string(ln[:2]); got != tc.margin {
				t.Errorf("left margin = %q, want %q:\n%s", got, tc.margin, lines[y])
			}
			if string(ln[2:4]) != "│ " {
				t.Errorf("the box does not start at column 2: %q", lines[y])
			}
		})
	}
}
