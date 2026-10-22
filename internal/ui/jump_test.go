package ui

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/dashmeta"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

// This file is the jump search's tests (jump.go, T16.6): the pure rows,
// matching and key decision as tables, then the wiring through Update — the
// box over a running pane, its key precedence, each landing, and the project
// switches it makes (the preview and remembered-selection arms and the open
// counts a switch records), which the project switcher's suite pinned before
// the jump search replaced it.

// --- helpers ---

var (
	// keyJump is the jump search's key: `/` at every level.
	keyJump  = keyRune('/')
	keyCtrlT = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	keyCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

// keyEscMsg is a bare esc press, shared by every test file in the package.
func keyEscMsg() tea.KeyMsg { return tea.KeyPressMsg{Code: tea.KeyEsc} }

// openJumpOverlay presses `/` and asserts it opened, so every test below
// starts from a box opened through the real key path rather than from a
// hand-set flag.
func openJumpOverlay(t *testing.T, m Model) Model {
	t.Helper()
	m = pressKey(t, m, keyJump)
	if !m.jumpOpen {
		t.Fatalf("`/` did not open the jump search (status %q)", m.status)
	}
	return m
}

// jumpSelect moves an open box's highlight onto the first visible row match
// accepts, by pressing down, so a test lands on the row it means whatever
// the needs-you order puts above it.
func jumpSelect(t *testing.T, m Model, what string, match func(jumpRow) bool) Model {
	t.Helper()
	hits := m.jump.visible()
	for range hits {
		if h := m.jump.highlight; h < len(hits) && match(hits[h].row) {
			return m
		}
		m, _ = update(t, m, keyDown)
	}
	t.Fatalf("no jump row for %s among %d rows", what, len(hits))
	return m
}

// jumpSelectProject selects the project row whose path is target.
func jumpSelectProject(t *testing.T, m Model, target string) Model {
	t.Helper()
	return jumpSelect(t, m, "project "+target, func(r jumpRow) bool {
		return r.kind == jumpProject && r.projectPath == target
	})
}

// jumpProjectPaths is the project rows the open box lists, by path, in its
// order.
func jumpProjectPaths(m Model) []string {
	var paths []string
	for _, r := range m.jump.rows {
		if r.kind == jumpProject {
			paths = append(paths, r.projectPath)
		}
	}
	return paths
}

// typeQuery types q into the open box one key at a time.
func typeQuery(t *testing.T, m Model, q string) Model {
	t.Helper()
	for _, r := range q {
		m, _ = update(t, m, keyRune(r))
	}
	return m
}

// jumpKeys is a row's identity as the tables below compare it.
func jumpKeys(rows []jumpRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.kind.String() + ":" + r.projectName + ":" + r.name
	}
	return out
}

// jp is a project in the facts, its row built by needsRow when edit is not
// nil-and-absent: hasRow false leaves it without a home-screen row.
func jp(path, name string, row *projectrows.Row, sessions ...Session) needsYouProject {
	p := needsYouProject{Path: path, Name: name, Sessions: sessions}
	if row != nil {
		p.Row, p.HasRow = *row, true
	}
	return p
}

func rowPtr(r projectrows.Row) *projectrows.Row { return &r }

// --- the pure parts ---

// TestJumpRows pins the order of the rows and which rows are put first
// because they need the owner (§4.6, A6). Load-bearing rows:
//
//   - "kinds then names" is the order with nothing waiting: projects,
//     sessions, tasks, each by name ignoring case, path breaking a tie of
//     names (the two "same" projects), so the order is total.
//   - "needs-you first, in ]'s order" puts a waiting session, a plan to
//     approve and a gone project ahead of everything, in the order `]`
//     walks them: beta's waiting session first (beta is in NEEDS YOU on the
//     home screen), then alpha's plan; inside a project waiting sessions
//     come before the plan.
//   - "a blocking item ranks its task" puts the task the oldest blocking
//     item holds up first, and "...falls back to its project" ranks the
//     project when that task is not listed, so the item is never lost.
//   - "pending and stale awaiting" leave out a creating… placeholder and do
//     not rank a done session whose awaiting flag is stale.
//   - "a label" shows the label and still matches the on-disk name (see
//     TestJumpFilter).
func TestJumpRows(t *testing.T) {
	waiting := Session{SessionState: waitingState("ask")}
	done := sess("old")
	done.Phase = state.PhaseDone
	done.AwaitingInput = true
	pending := sess("new")
	pending.Pending = true
	labelled := sess("feat-x")
	labelled.Label = "Feature X"
	plan := needsRow("alpha", "/p/alpha", 0, func(in *projectrows.Input) { in.PlanWaiting = true })
	gone := needsRow("gamma", "/p/gamma", 2, func(in *projectrows.Input) { in.Gone = true })
	quiet := needsRow("beta", "/p/beta", 1, nil)
	blocking := needsRow("beta", "/p/beta", 1, func(in *projectrows.Input) {
		in.Open, in.Blocking = 1, 1
		in.Wait = projectrows.Wait{ItemID: "q1", CreatedAt: "2026-10-20T09:00:00Z", Task: "W2.T1"}
	})

	tests := []struct {
		name  string
		facts jumpFacts
		want  []string
		// ranked is how many rows at the top need the owner.
		ranked int
	}{
		{
			name: "kinds then names",
			facts: jumpFacts{
				Projects: []needsYouProject{
					jp("/z/same", "same", nil, sess("zed"), sess("Alpha-s")),
					jp("/a/same", "same", nil),
					jp("/p/b", "Beta", nil),
				},
				Tasks: map[string][]JumpTask{"/p/b": {{ID: "W2.T1", Title: "Two"}, {ID: "W1.T1", Title: "One"}}},
			},
			want: []string{
				"project:Beta:Beta", "project:same:same", "project:same:same",
				"session:same:Alpha-s", "session:same:zed",
				"task:Beta:W1.T1 One", "task:Beta:W2.T1 Two",
			},
		},
		{
			name: "needs-you first, in ]'s order",
			facts: jumpFacts{Projects: []needsYouProject{
				jp("/p/alpha", "alpha", rowPtr(plan), sess("calm"), waiting),
				jp("/p/beta", "beta", rowPtr(quiet)),
				jp("/p/gamma", "gamma", rowPtr(gone)),
			}},
			want: []string{
				"session:alpha:ask", "project:alpha:alpha", "project:gamma:gamma",
				"project:beta:beta", "session:alpha:calm",
			},
			ranked: 3,
		},
		{
			name: "a blocking item ranks its task",
			facts: jumpFacts{
				Projects: []needsYouProject{jp("/p/beta", "beta", rowPtr(blocking))},
				Tasks:    map[string][]JumpTask{"/p/beta": {{ID: "W1.T1", Title: "One"}, {ID: "W2.T1", Title: "Two"}}},
			},
			want:   []string{"task:beta:W2.T1 Two", "project:beta:beta", "task:beta:W1.T1 One"},
			ranked: 1,
		},
		{
			name:   "a blocking item whose task is not listed falls back to its project",
			facts:  jumpFacts{Projects: []needsYouProject{jp("/p/beta", "beta", rowPtr(blocking))}},
			want:   []string{"project:beta:beta"},
			ranked: 1,
		},
		{
			name:  "pending and stale awaiting",
			facts: jumpFacts{Projects: []needsYouProject{jp("/p/a", "a", nil, done, pending)}},
			want:  []string{"project:a:a", "session:a:old"},
		},
		{
			name:  "a label",
			facts: jumpFacts{Projects: []needsYouProject{jp("/p/a", "a", nil, labelled)}},
			want:  []string{"project:a:a", "session:a:Feature X"},
		},
		{
			name:  "a project given twice counts once",
			facts: jumpFacts{Projects: []needsYouProject{jp("/p/a", "a", nil), jp("/p/a", "a", nil)}},
			want:  []string{"project:a:a"},
		},
		{
			name: "nothing at all",
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := jumpRows(tt.facts)
			if got := jumpKeys(rows); !slices.Equal(got, tt.want) {
				t.Fatalf("rows = %v, want %v", got, tt.want)
			}
			ranked := 0
			for _, r := range rows {
				if r.needRank >= 0 {
					ranked++
				}
			}
			if ranked != tt.ranked {
				t.Errorf("%d rows need the owner, want %d", ranked, tt.ranked)
			}
			for i := range ranked {
				if rows[i].needRank < 0 {
					t.Errorf("row %d (%s) is not ranked but sits among the ranked rows", i, jumpKeys(rows)[i])
				}
			}
		})
	}
}

// TestJumpMatch pins the subsequence match. Load-bearing rows: "letters
// spread out" (a subsequence, not a substring), "case" (ignored both ways),
// "earliest letters" (the positions underlined are the first that fit), and
// "out of order" (order matters, or every name with the letters would
// match).
func TestJumpMatch(t *testing.T) {
	tests := []struct {
		name, query, text string
		want              []int
		ok                bool
	}{
		{"empty query matches with nothing marked", "", "afk", nil, true},
		{"letters spread out", "afd", "afk-dashboard", []int{0, 1, 4}, true},
		{"case", "AfK", "aFk", []int{0, 1, 2}, true},
		{"earliest letters", "a", "banana", []int{1}, true},
		{"out of order", "ka", "afk", nil, false},
		{"longer than the text", "afkk", "afk", nil, false},
		{"not there", "z", "afk", nil, false},
		{"runes, not bytes", "é", "café", []int{3}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := jumpMatch(tt.query, tt.text)
			if ok != tt.ok || !slices.Equal(got, tt.want) {
				t.Errorf("jumpMatch(%q, %q) = %v, %v; want %v, %v", tt.query, tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestJumpFilter pins which rows a query keeps and the letters it marks.
// Load-bearing rows: "a task's title" marks the title's letters at their
// place after the id, "not across id and title" keeps a task only when one
// of the two holds every letter, "the hidden name" matches a labelled
// session on its on-disk name and marks nothing (that name is not on
// screen), and "spaces" are dropped from the query.
func TestJumpFilter(t *testing.T) {
	labelled := sess("feat-x")
	labelled.Label = "Feature X"
	rows := jumpRows(jumpFacts{
		Projects: []needsYouProject{jp("/p/afk", "afk", nil, labelled)},
		Tasks:    map[string][]JumpTask{"/p/afk": {{ID: "W1.T1", Title: "Jump box"}}},
	})
	tests := []struct {
		name  string
		query string
		want  map[string][]int // row name → marks
	}{
		{"empty keeps every row", "", map[string][]int{"afk": nil, "Feature X": nil, "W1.T1 Jump box": nil}},
		{"a task's id", "w1t", map[string][]int{"W1.T1 Jump box": {0, 1, 3}}},
		{"a task's title", "jbx", map[string][]int{"W1.T1 Jump box": {6, 11, 13}}},
		{"not across id and title", "t1j", map[string][]int{}},
		{"the shown name", "fx", map[string][]int{"Feature X": {0, 8}}},
		{"the hidden name", "t-x", map[string][]int{"Feature X": nil}},
		{"spaces", "w1 t1", map[string][]int{"W1.T1 Jump box": {0, 1, 3, 4}}},
		{"no match", "qqq", map[string][]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string][]int{}
			for _, h := range jumpFilter(rows, tt.query) {
				got[h.row.name] = h.marks
			}
			if len(got) != len(tt.want) {
				t.Fatalf("kept %v, want %v", got, tt.want)
			}
			for name, marks := range tt.want {
				g, ok := got[name]
				if !ok || !slices.Equal(g, marks) {
					t.Errorf("row %q marks = %v (kept %v), want %v", name, g, ok, marks)
				}
			}
		})
	}
}

// TestJumpKeyDecision is the box's key table. Load-bearing rows: "j types"
// and "q types" (letters always type, §4.6; j is not a move here and q not
// quit), "esc clears a query" before "esc closes" (two-stage esc), "enter
// with nothing matching" (the box stays open so the query can be fixed),
// "the leader closes", and the page rows (a page, no wrap).
func TestJumpKeyDecision(t *testing.T) {
	hits := []jumpHit{{row: jumpRow{kind: jumpProject, name: "a"}}, {row: jumpRow{kind: jumpSession, name: "b"}}}
	tests := []struct {
		name      string
		key       tea.KeyPressMsg
		query     string
		hits      []jumpHit
		highlight int
		want      jumpOutcome
	}{
		{"enter goes to the highlighted row", tea.KeyPressMsg{Code: tea.KeyEnter}, "", hits, 1, jumpOutcome{kind: jumpGo, row: hits[1].row}},
		{"enter with nothing matching", tea.KeyPressMsg{Code: tea.KeyEnter}, "zz", nil, 0, jumpOutcome{}},
		{"esc clears a query", tea.KeyPressMsg{Code: tea.KeyEsc}, "ab", hits, 0, jumpOutcome{kind: jumpClear}},
		{"esc closes", tea.KeyPressMsg{Code: tea.KeyEsc}, "", hits, 0, jumpOutcome{kind: jumpClose}},
		{"the leader closes", leaderKeyPress, "ab", hits, 0, jumpOutcome{kind: jumpClose}},
		{"up wraps", tea.KeyPressMsg{Code: tea.KeyUp}, "", hits, 0, jumpOutcome{kind: jumpMove, delta: -1, wrap: true}},
		{"down wraps", tea.KeyPressMsg{Code: tea.KeyDown}, "", hits, 0, jumpOutcome{kind: jumpMove, delta: 1, wrap: true}},
		{"pgup moves a page", tea.KeyPressMsg{Code: tea.KeyPgUp}, "", hits, 0, jumpOutcome{kind: jumpMove, delta: -5}},
		{"pgdown moves a page", tea.KeyPressMsg{Code: tea.KeyPgDown}, "", hits, 0, jumpOutcome{kind: jumpMove, delta: 5}},
		{"backspace edits", tea.KeyPressMsg{Code: tea.KeyBackspace}, "ab", hits, 0, jumpOutcome{kind: jumpType, backspace: true}},
		{"j types", tea.KeyPressMsg{Code: 'j', Text: "j"}, "", hits, 0, jumpOutcome{kind: jumpType, text: "j"}},
		{"q types", tea.KeyPressMsg{Code: 'q', Text: "q"}, "", hits, 0, jumpOutcome{kind: jumpType, text: "q"}},
		{"/ types", tea.KeyPressMsg{Code: '/', Text: "/"}, "", hits, 0, jumpOutcome{kind: jumpType, text: "/"}},
		{"a chord with no text is ignored", keyCtrlT, "", hits, 0, jumpOutcome{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jumpKeyDecision(tt.key, defaultLeaderKey, tt.query, tt.hits, tt.highlight, 5)
			if got.kind != tt.want.kind || got.row.name != tt.want.row.name || got.text != tt.want.text ||
				got.backspace != tt.want.backspace || got.delta != tt.want.delta || got.wrap != tt.want.wrap {
				t.Errorf("decision = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestJumpDialogApply pins what the box does with its own outcomes.
// Load-bearing rows: "a query edit goes back to the top" (the highlight can
// never index past a list the query shortened), "down wraps" and "a page
// stops at the end".
func TestJumpDialogApply(t *testing.T) {
	rows := []jumpRow{{name: "aa"}, {name: "ab"}, {name: "b"}}
	for i := range rows {
		rows[i].fields = []jumpField{{text: rows[i].name}}
	}
	tests := []struct {
		name          string
		query         string
		highlight     int
		o             jumpOutcome
		wantQuery     string
		wantHighlight int
	}{
		{"a query edit goes back to the top", "", 2, jumpOutcome{kind: jumpType, text: "a"}, "a", 0},
		{"backspace drops the last rune", "aé", 1, jumpOutcome{kind: jumpType, backspace: true}, "a", 0},
		{"backspace on nothing", "", 0, jumpOutcome{kind: jumpType, backspace: true}, "", 0},
		{"clear", "ab", 0, jumpOutcome{kind: jumpClear}, "", 0},
		{"down wraps", "", 2, jumpOutcome{kind: jumpMove, delta: 1, wrap: true}, "", 0},
		{"up wraps", "", 0, jumpOutcome{kind: jumpMove, delta: -1, wrap: true}, "", 2},
		{"a page stops at the end", "", 1, jumpOutcome{kind: jumpMove, delta: 10}, "", 2},
		{"a page stops at the top", "", 1, jumpOutcome{kind: jumpMove, delta: -10}, "", 0},
		{"a move with nothing matching", "zz", 0, jumpOutcome{kind: jumpMove, delta: 1, wrap: true}, "zz", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := jumpDialog{rows: rows, query: tt.query, highlight: tt.highlight}
			d.apply(tt.o)
			if d.query != tt.wantQuery || d.highlight != tt.wantHighlight {
				t.Errorf("query, highlight = %q, %d; want %q, %d", d.query, d.highlight, tt.wantQuery, tt.wantHighlight)
			}
		})
	}
}

// TestJumpNameCell pins the underline and the cut. Load-bearing: a mark past
// the cut is dropped (its letter is not on screen) and the cell is always
// exactly width cells, so the columns after it line up.
func TestJumpNameCell(t *testing.T) {
	underlined := jumpMatchStyle.Render("b")
	tests := []struct {
		name  string
		text  string
		marks []int
		width int
		want  string
	}{
		{"padded", "abc", nil, 5, "abc  "},
		{"a mark", "abc", []int{1}, 4, "a" + underlined + "c "},
		{"cut", "abcdef", nil, 4, "abc…"},
		{"a mark past the cut", "abcdef", []int{4}, 4, "abc…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jumpNameCell(tt.text, tt.marks, tt.width)
			if got != tt.want {
				t.Errorf("jumpNameCell = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- the box through Update ---

// jumpWorld is newNeedsWorld with something to find of every kind: project
// a is named "jumpy", b holds the session "jump-fix" and a stopped one, and
// a has two tasks, one titled "Make the jump box". Every project is quiet,
// so nothing is put first.
func jumpWorld(t *testing.T) *needsWorld {
	t.Helper()
	running := sampleState("jump-fix")
	running.HostRunning = true
	running.HostPID = 4242
	stopped := sampleState("asleep")
	sessions := map[string][]discover.SessionState{
		"/repo": {sampleState("home")},
		"/p/b":  {running, stopped},
	}
	rows := []projectrows.Row{needsRow("jumpy", "/p/a", 0, nil), needsRow("b", "/p/b", 1, nil)}
	w := newNeedsWorld(t, sessions, rows)
	w.m.projects[1].Name = "jumpy"
	w.m.everyProject.Tasks = map[string][]JumpTask{
		"/p/a": {{ID: "W1.T1", Title: "Write the docs", Status: "done"}, {ID: "W2.T1", Title: "Make the jump box", Status: "pending"}},
	}
	return w
}

// TestJumpFindsAProjectASessionAndATask is the acceptance jump test: one
// query finds a project, a session and a task across two projects, and
// leaves out what does not match; enter on the task lands on its project at
// Project with the Build section focused and the task selected once the
// switched project's build is read.
func TestJumpFindsAProjectASessionAndATask(t *testing.T) {
	w := jumpWorld(t)
	w.m = openJumpOverlay(t, w.m)
	w.m = typeQuery(t, w.m, "jmp")

	var got []string
	for _, h := range w.m.jump.visible() {
		got = append(got, h.row.kind.String()+":"+h.row.name)
	}
	want := []string{"project:jumpy", "session:jump-fix", "task:W2.T1 Make the jump box"}
	if !slices.Equal(got, want) {
		t.Fatalf("rows for \"jmp\" = %v, want %v", got, want)
	}
	frame := render(w.m)
	for _, s := range []string{"jumpy", "jump-fix", "W2.T1 Make the jump box", "> jmp"} {
		if !strings.Contains(frame, s) {
			t.Errorf("the box does not show %q:\n%s", s, frame)
		}
	}

	w.m = jumpSelect(t, w.m, "the task", func(r jumpRow) bool { return r.kind == jumpTask })
	var cmd tea.Cmd
	w.m, cmd = update(t, w.m, keyEnter)
	w.settle(t, cmd)
	if w.m.jumpOpen || w.m.repo != "/p/a" {
		t.Fatalf("open = %v, repo = %q; want the box closed and project a open", w.m.jumpOpen, w.m.repo)
	}
	w.m, _ = update(t, w.m, buildLoadedMsg{gen: w.m.projectGen, snap: buildview.Snapshot{Rows: []buildview.Row{
		{ID: "W1.T1", Title: "Write the docs"}, {ID: "W2.T1", Title: "Make the jump box"},
	}}})
	if w.m.level() != levelProject || !w.m.buildSectionFocused() || w.m.buildCursorID != "W2.T1" || w.m.buildCursorIndex != 1 {
		t.Errorf("level = %v, build focused = %v, cursor = %q at %d; want Project, Build focused, on W2.T1 at 1",
			w.m.level(), w.m.buildSectionFocused(), w.m.buildCursorID, w.m.buildCursorIndex)
	}
}

// TestJumpLandings pins where enter on each kind of row lands (§4.6).
// Load-bearing rows: "another project" (a switch with the preview armed and
// the open counted, as enter on its home-screen row does), "the active
// project from All projects" (closes the screen, no switch), "a gone
// project" (says why and stays), "a session elsewhere" (the agent landing:
// a switch, then a dial for that session's worktree, no preview), "a stopped
// session" (its details, no dial: enter's rule), "a worktree fault" (its
// project view with it selected, since it has no agent) and "a task in the
// active project" (Build focused with the cursor on it at once).
func TestJumpLandings(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *needsWorld)
		pick  func(r jumpRow) bool
		check func(t *testing.T, w *needsWorld)
	}{
		{
			name: "another project",
			pick: func(r jumpRow) bool { return r.kind == jumpProject && r.projectPath == "/p/b" },
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/p/b" || w.m.level() != levelProject || len(w.opened) != 0 {
					t.Fatalf("repo = %q, level = %v, dials = %d; want /p/b at Project and no dial", w.m.repo, w.m.level(), len(w.opened))
				}
				if w.m.projectOpens["/p/b"] != 1 {
					t.Errorf("opens of /p/b = %d, want the switch counted once", w.m.projectOpens["/p/b"])
				}
			},
		},
		{
			name:  "the active project from All projects",
			setup: func(w *needsWorld) { w.openHome(t, "/p/b") },
			pick:  func(r jumpRow) bool { return r.kind == jumpProject && r.projectPath == "/repo" },
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/repo" || w.m.allProjectsOpen || w.m.level() != levelProject {
					t.Fatalf("repo = %q, home open = %v; want /repo at Project", w.m.repo, w.m.allProjectsOpen)
				}
			},
		},
		{
			name: "a gone project",
			setup: func(w *needsWorld) {
				w.m.everyProject.Rows[1] = needsRow("b", "/p/b", 1, func(in *projectrows.Input) { in.Gone = true })
			},
			pick: func(r jumpRow) bool { return r.kind == jumpProject && r.projectPath == "/p/b" },
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/repo" || !strings.Contains(w.m.status, "is gone") {
					t.Fatalf("repo = %q, status = %q; want no switch and the gone notice", w.m.repo, w.m.status)
				}
			},
		},
		{
			name: "a session elsewhere",
			pick: func(r jumpRow) bool { return r.kind == jumpSession && r.session == "jump-fix" },
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/p/b" || len(w.opened) != 1 || w.opened[0].Name != "jump-fix" || w.opened[0].Worktree != "/wt/jump-fix" {
					t.Fatalf("repo = %q, dials = %+v; want /p/b and one dial for jump-fix at /wt/jump-fix", w.m.repo, w.opened)
				}
				if w.m.level() != levelAgent || w.m.paneName != "jump-fix" {
					t.Errorf("level = %v, pane = %q; want the Agent on jump-fix", w.m.level(), w.m.paneName)
				}
				if w.m.previewOnNextLoad {
					t.Error("an agent landing armed a preview, which would race a second dial")
				}
			},
		},
		{
			name: "a stopped session",
			pick: func(r jumpRow) bool { return r.kind == jumpSession && r.session == "asleep" },
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/p/b" || len(w.opened) != 0 || !w.m.detailsOpen || w.m.detailsFor != "asleep" {
					t.Fatalf("repo = %q, dials = %d, details = %v for %q; want /p/b with asleep's details and no dial",
						w.m.repo, len(w.opened), w.m.detailsOpen, w.m.detailsFor)
				}
			},
		},
		{
			name: "a worktree fault",
			setup: func(w *needsWorld) {
				w.m.setSessions([]Session{sess("home"), faultSess("lost", session.KindWorktreeMissing, "worktree directory no longer exists")})
			},
			pick: func(r jumpRow) bool { return r.kind == jumpSession && r.session == "lost" },
			check: func(t *testing.T, w *needsWorld) {
				s, ok := w.m.selected()
				if w.m.level() != levelProject || !ok || s.Name != "lost" || len(w.opened) != 0 {
					t.Fatalf("level = %v, selected = %q (%v), dials = %d; want Project on the fault row and no dial",
						w.m.level(), s.Name, ok, len(w.opened))
				}
			},
		},
		{
			name: "a task in the active project",
			setup: func(w *needsWorld) {
				w.m.everyProject.Tasks["/repo"] = []JumpTask{{ID: "R1", Title: "Home task"}, {ID: "R2", Title: "Other"}}
				w.m.build = buildview.Snapshot{Rows: []buildview.Row{{ID: "R1"}, {ID: "R2"}}}
				w.m.buildKnown = true
			},
			pick: func(r jumpRow) bool { return r.kind == jumpTask && r.taskID == "R2" },
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/repo" || !w.m.buildSectionFocused() || w.m.buildCursorID != "R2" || w.m.buildCursorIndex != 1 {
					t.Fatalf("repo = %q, build focused = %v, cursor = %q at %d; want /repo, Build focused on R2 at 1",
						w.m.repo, w.m.buildSectionFocused(), w.m.buildCursorID, w.m.buildCursorIndex)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := jumpWorld(t)
			if tt.setup != nil {
				tt.setup(w)
			}
			w.m = openJumpOverlay(t, w.m)
			w.m = jumpSelect(t, w.m, tt.name, tt.pick)
			var cmd tea.Cmd
			w.m, cmd = update(t, w.m, keyEnter)
			w.settle(t, cmd)
			if w.m.jumpOpen {
				t.Fatal("enter on a row must close the box")
			}
			tt.check(t, w)
		})
	}
}

// TestJumpNoMatch is the failure path: a query nothing matches shows the
// empty-result line, enter does nothing and keeps the box open with the
// query, the first esc clears the query and the second closes the box.
func TestJumpNoMatch(t *testing.T) {
	w := jumpWorld(t)
	w.m = openJumpOverlay(t, w.m)
	w.m = typeQuery(t, w.m, "zzzq")
	if frame := render(w.m); !strings.Contains(frame, jumpNoMatch) {
		t.Fatalf("no empty-result line:\n%s", frame)
	}
	var cmd tea.Cmd
	w.m, cmd = update(t, w.m, keyEnter)
	if cmd != nil || !w.m.jumpOpen || w.m.jump.query != "zzzq" || w.m.repo != "/repo" {
		t.Fatalf("enter with no match: cmd %v, open %v, query %q, repo %q; want nothing done", cmd != nil, w.m.jumpOpen, w.m.jump.query, w.m.repo)
	}
	w.m, _ = update(t, w.m, keyEscMsg())
	if !w.m.jumpOpen || w.m.jump.query != "" {
		t.Fatalf("first esc: open %v, query %q; want the query cleared and the box open", w.m.jumpOpen, w.m.jump.query)
	}
	w.m, _ = update(t, w.m, keyEscMsg())
	if w.m.jumpOpen {
		t.Fatal("second esc did not close the box")
	}
}

// TestJumpOpensAtAllProjectsWithNoActiveProject pins A7: a dashboard
// started outside any project, on All projects, opens the jump search over
// its rows and lands on a project from it. Before T16.6 `/` there only said
// there were no projects to switch between.
func TestJumpOpensAtAllProjectsWithNoActiveProject(t *testing.T) {
	m := testModel()
	m.repo = ""
	m.projects = []project.Project{{Path: "/p/a", Name: "a"}, {Path: "/p/b", Name: "b"}}
	m.activeProject = -1
	rows := []projectrows.Row{needsRow("a", "/p/a", 0, nil), needsRow("b", "/p/b", 1, nil)}
	m.deps.LoadAllProjects = (&allProjectsSeam{res: AllProjects{Rows: rows}}).load
	m.everyProject = AllProjects{Rows: rows}
	m.allProjectsOpen = true
	if m.hasActiveProject() || m.level() != levelAllProjects {
		t.Fatal("setup: want All projects with no project open")
	}
	m = openJumpOverlay(t, m)
	if got := jumpProjectPaths(m); !slices.Equal(got, []string{"/p/a", "/p/b"}) {
		t.Fatalf("project rows = %v, want both listed projects", got)
	}
	m = jumpSelectProject(t, m, "/p/b")
	m, _ = update(t, m, keyEnter)
	if m.activeProjectPath() != "/p/b" || m.allProjectsOpen {
		t.Errorf("active = %q, home open = %v; want b open at Project", m.activeProjectPath(), m.allProjectsOpen)
	}
}

// TestJumpFromTheAgent pins `ctrl+] /`: the leader then `/` opens the box
// from the Agent, and every key typed into it stays in the box — none
// reaches the agent behind it, since only the leader may reach afk while the
// agent has the keyboard and the box, once open, is what is on screen.
func TestJumpFromTheAgent(t *testing.T) {
	m, fp, _ := jumpWiringModel(t)
	m, _ = update(t, m, keyEnter)
	if m.level() != levelAgent || !m.paneOwnsKeyboard() {
		t.Fatal("setup: enter did not open the Agent")
	}
	m, _ = pressAction(t, m, keyJump)
	if !m.jumpOpen {
		t.Fatal("ctrl+] / did not open the jump search")
	}
	m = typeQuery(t, m, "jq")
	if m.jump.query != "jq" {
		t.Errorf("query = %q, want the letters typed into the box", m.jump.query)
	}
	if len(fp.keys) != 0 || len(fp.texts) != 0 {
		t.Errorf("the agent got %v/%v, want nothing", fp.keys, fp.texts)
	}
	m, _ = update(t, m, keyEscMsg())
	m, _ = update(t, m, keyEscMsg())
	if m.jumpOpen || m.pane != fp {
		t.Errorf("open = %v, pane kept = %v; want the box closed and the pane still connected", m.jumpOpen, m.pane == fp)
	}
}

// TestJumpKeepsItsSnapshotUnderAScan pins the snapshot: a scan that lands
// under an open box with a new project leaves the rows and the highlight
// where they are, and the new project is there the next time it opens.
// Rebuilding under the owner's hands would move the row they are aiming at.
func TestJumpKeepsItsSnapshotUnderAScan(t *testing.T) {
	m, _, _ := jumpWiringModel(t)
	m = openJumpOverlay(t, m)
	m, _ = update(t, m, keyDown)
	before := jumpProjectPaths(m)

	m, _ = update(t, m, projectScanMsg{projects: scannedProjects("/roots/alpha", "/roots/beta", "/roots/gamma", "/roots/delta")})

	if !m.jumpOpen || m.jump.highlight != 1 || !slices.Equal(jumpProjectPaths(m), before) {
		t.Fatalf("open = %v, highlight = %d, projects = %v; want the snapshot %v kept at 1",
			m.jumpOpen, m.jump.highlight, jumpProjectPaths(m), before)
	}
	m, _ = update(t, m, keyEscMsg())
	m = openJumpOverlay(t, m)
	if got := jumpProjectPaths(m); !slices.Contains(got, "/roots/delta") {
		t.Errorf("reopened rows = %v, want the scanned delta", got)
	}
}

// TestJumpOpensOverTheRunningPane is the overlay contract in one frame: the
// box floats over the dashboard as a modal (modal.go) with its rows on
// screen, and the pane behind it is left running and connected.
func TestJumpOpensOverTheRunningPane(t *testing.T) {
	m, fp, _ := jumpWiringModel(t)
	m = openJumpOverlay(t, m)

	view := render(m)
	for _, name := range []string{"alpha", "beta", "gamma", "jump", jumpPlaceholder} {
		if !strings.Contains(view, name) {
			t.Errorf("View is missing %q:\n%s", name, view)
		}
	}

	// Opening the box is not choosing anything: nothing detaches.
	if m.pane != fp || fp.closed != 0 {
		t.Errorf("pane = %v closed %d times, want the same pane still attached", m.pane, fp.closed)
	}
	if m.paneName != "one" || m.view == viewOverview {
		t.Errorf("paneName = %q viewOverview = %t, want %q and false", m.paneName, m.view == viewOverview, "one")
	}
	if len(fp.keys) != 0 || len(fp.texts) != 0 {
		t.Errorf("`/` was forwarded to the session as %v/%v, want nothing", fp.keys, fp.texts)
	}

	// Esc closes the box and enter puts the pane on screen, proving it
	// stayed alive underneath.
	m, _ = update(t, m, keyEscMsg())
	if m.jumpOpen {
		t.Error("esc did not close the box")
	}
	m, _ = update(t, m, keyEnter)
	if after := render(m); !strings.Contains(after, paneSentinel) {
		t.Errorf("the pane should be rendered in the Agent after esc and enter:\n%s", after)
	}
}

// TestJumpKeyPrecedence is the routing table for an OPEN box. Every row is a
// way the box could leak a key to something behind it:
//
//   - ctrl+t pins that the box keeps a chord with no text from the pane.
//   - down moves the box and never the session list; the cursor invariant
//     below is checked for every row for that reason.
//   - j, q, n and c type into the query (§4.6): j does not move, q does not
//     quit, n does not open the creation form, c does not start a clean.
//   - ctrl+c is the sole global exception, as in the `:` search.
func TestJumpKeyPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		key            tea.KeyMsg
		wantOpen       bool
		wantIndex      int
		wantQuery      string
		wantQuitting   bool
		wantPaneClosed int
	}{
		{name: "ctrl+t does nothing", key: keyCtrlT, wantOpen: true},
		{name: "down moves the box", key: keyDown, wantOpen: true, wantIndex: 1},
		{name: "j types", key: keyRune('j'), wantOpen: true, wantQuery: "j"},
		{name: "q types, not quit", key: keyRune('q'), wantOpen: true, wantQuery: "q"},
		{name: "n types, no creation form", key: keyRune('n'), wantOpen: true, wantQuery: "n"},
		{name: "c types, no clean", key: keyRune('c'), wantOpen: true, wantQuery: "c"},
		{name: "esc closes the box", key: keyEscMsg()},
		{
			name: "ctrl+c quits", key: keyCtrlC, wantOpen: true,
			wantQuitting: true, wantPaneClosed: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fp, _ := jumpWiringModel(t)
			m = openJumpOverlay(t, m)

			m, _ = update(t, m, tt.key)

			if m.jumpOpen != tt.wantOpen {
				t.Errorf("jumpOpen = %t, want %t", m.jumpOpen, tt.wantOpen)
			}
			if m.jump.highlight != tt.wantIndex || m.jump.query != tt.wantQuery {
				t.Errorf("highlight, query = %d, %q; want %d, %q", m.jump.highlight, m.jump.query, tt.wantIndex, tt.wantQuery)
			}
			if m.quitting != tt.wantQuitting {
				t.Errorf("quitting = %t, want %t", m.quitting, tt.wantQuitting)
			}
			if fp.closed != tt.wantPaneClosed {
				t.Errorf("pane closed %d times, want %d", fp.closed, tt.wantPaneClosed)
			}
			if m.listSelection() != 0 {
				t.Errorf("cursor = %d, want 0 — the session list must not move", m.listSelection())
			}
			if m.formOpen {
				t.Error("the creation form must not open from under the box")
			}
			if m.modal != inputNormal {
				t.Errorf("mode = %v, want inputNormal", m.modal)
			}
			if m.view == viewOverview {
				t.Error("the pane behind the box must not be hidden")
			}
			// Both channels: forwardKeyToPane routes a printable rune through
			// SendText and only a special key through SendKey.
			if len(fp.keys) != 0 || len(fp.texts) != 0 {
				t.Errorf("forwarded %v/%v to the session, want nothing", fp.keys, fp.texts)
			}
		})
	}
}

// jumpWiringProjects is the list every test here opens the overlay over:
// three basenames that do not collide, so project.DisplayNames leaves them bare
// and an assertion can look for the name it wrote.
func jumpWiringProjects() []project.Project {
	return scannedProjects("/roots/alpha", "/roots/beta", "/roots/gamma")
}

// jumpWiringModel builds the state these assertions need: three projects
// with the first active, two sessions, a VISIBLE pane on the selected one, and a
// populated scan cache.
//
// The visible pane is the load-bearing part of the fixture — most of what this
// file pins is what the overlay must NOT do to a running session behind it — so
// the helper asserts the pane really is on screen before handing the Model back.
func jumpWiringModel(t *testing.T) (Model, *fakePane, *perProjectSpy) {
	t.Helper()
	spy := &perProjectSpy{}
	d := spy.deps()
	// Layer a per-project discovery fixture over the spy's always-nil stub,
	// preserving the spy's own call recording (spy.snapshot() is what
	// TestJumpEnterSwitchesTheActiveProject asserts against): beta's real
	// load needs to return an actual HostRunning row, the shape switchProjectCmds'
	// loadCmd sees in production, for
	// TestJumpSwitchPreviewsTheFirstSessionOnceItsLoadLands's positive row.
	spyDiscover := d.Discover
	d.Discover = func(repo string) ([]discover.SessionState, error) {
		_, _ = spyDiscover(repo)
		return jumpWiringDiscoverFixture(repo), nil
	}
	d.Projects = jumpWiringProjects()
	d.ActiveProjectIndex = 0
	m := New("/roots/alpha", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40
	m.setSessions([]Session{sess("one"), sess("two")})
	m.lastLoad = time.Unix(1_000_000, 0)
	m.projectSessions = map[string][]discover.SessionState{
		"/roots/alpha": {{}},
		"/roots/beta": {
			{}, {AwaitingInput: true},
			// A findable, HostRunning row: resolveFocusTarget looks up a
			// non-active project's session by name in THIS cache (never in
			// Discover's live result), which is what the focus-request row of
			// TestJumpSwitchPreviewsTheFirstSessionOnceItsLoadLands needs to
			// resolve a click on beta's "beta-target" before the switch. No
			// existing test asserts this slice's length or contents (grepped),
			// so extending it here is safe.
			{Name: "beta-target", HostRunning: true, WorktreePath: "/roots/beta/wt/beta-target"},
		},
	}
	fp := newFakePane(paneSentinel)
	m.pane = fp
	m.paneName = "one"
	// The pane is connected for the selected row and waits behind the
	// Project, which draws the overview (W3.T1 of T16.2): enter shows it.
	if !m.paneForSelection() || m.shouldRenderPane() {
		t.Fatal("precondition: the pane must be connected behind the Project before the overlay opens")
	}
	return m, fp, spy
}

// jumpWiringDiscoverFixture is what the live Discover seam returns per
// project in jumpWiringModel. Only beta carries a real row (a single
// HostRunning session, so "row 0" is unambiguous regardless of any sort the
// list applies) — every other project answers empty, matching the spy's
// original always-nil stub.
func jumpWiringDiscoverFixture(repo string) []discover.SessionState {
	if repo == "/roots/beta" {
		return []discover.SessionState{runningSess("beta-one").SessionState}
	}
	return nil
}

// scanSeams wires the two scan seams onto Deps so scanCmd is live: the pipeline
// is inert without both (see scanEnabled), and the listed-project rows need a
// real Cmd to collect.
func scanSeams(d Deps, found ...string) Deps {
	d.LoadConfig = func() (SearchSettings, string, error) {
		return SearchSettings{
			Roots:    []string{"/roots"},
			Interval: time.Minute,
			MaxDepth: 3,
		}, "/cfg/config.toml", nil
	}
	d.Scan = func([]string, int) []project.Project { return scannedProjects(found...) }
	return d
}

// --- tests ---

// TestJumpEnterSwitchesTheActiveProject pins the other half of the pane
// rule: merely OPENING the overlay leaves the pane attached
// (TestJumpOpensOverTheRunningPane), but SELECTING a different project
// detaches it, which is W3.T1's specified teardown. The spy assertion is what
// makes "dispatches load/stack/branches" mean "against the NEW project" rather
// than merely "dispatched".
func TestJumpEnterSwitchesTheActiveProject(t *testing.T) {
	m, fp, spy := jumpWiringModel(t)
	m = openJumpOverlay(t, m)

	m = jumpSelectProject(t, m, "/roots/beta")
	m, cmd := update(t, m, keyEnter)

	if m.jumpOpen {
		t.Error("choosing a project must close the overlay")
	}
	if m.repo != "/roots/beta" || m.activeProject != 1 {
		t.Errorf("repo = %q activeProject = %d, want %q and 1", m.repo, m.activeProject, "/roots/beta")
	}
	if m.pane != nil || fp.closed != 1 {
		t.Errorf("pane = %v closed %d times, want detached exactly once", m.pane, fp.closed)
	}
	if m.sessions != nil {
		t.Errorf("sessions = %v, want the outgoing project's list cleared", m.sessions)
	}

	msgs := collectMsgs(t, cmd)
	if !hasMsg[sessionsLoadedMsg](msgs) || !hasMsg[stackLoadedMsg](msgs) || !hasMsg[plainBranchesLoadedMsg](msgs) {
		t.Error("a switch must dispatch load, stack, and plain branches")
	}
	discovered, detected, branched := spy.snapshot()
	for _, calls := range [][]string{discovered, detected, branched} {
		if len(calls) != 1 || calls[0] != "/roots/beta" {
			t.Errorf("per-project seam called with %v, want [/roots/beta]", calls)
		}
	}
}

// TestJumpEnterOnTheActiveProjectIsANoOp pins that Enter routes through
// switchProject rather than doing its own repointing: the method no-ops on the
// already-active path, which is what keeps the pane holding the session the user
// is watching from being torn down by a stray Enter.
func TestJumpEnterOnTheActiveProjectIsANoOp(t *testing.T) {
	m, fp, _ := jumpWiringModel(t)
	m = openJumpOverlay(t, m)
	m = jumpSelectProject(t, m, "/roots/alpha")

	m, cmd := update(t, m, keyEnter)

	if m.jumpOpen {
		t.Error("Enter must close the overlay even when it selects the active project")
	}
	if cmd != nil {
		t.Error("re-selecting the active project must dispatch nothing")
	}
	if m.pane != fp || fp.closed != 0 || len(m.sessions) != 2 {
		t.Error("re-selecting the active project must leave pane and session state alone")
	}
}

// TestJumpMouseIsDroppedWhileTheOverlayIsUp is the mouse half of the
// precedence rule. Both rows aim at the pane's own content origin — the one
// coordinate that is guaranteed to be forwarded when nothing owns input — so a
// missing guard fails here rather than passing by accident on a dropped
// out-of-region event.
func TestJumpMouseIsDroppedWhileTheOverlayIsUp(t *testing.T) {
	tests := []struct {
		name  string
		event func(x, y int) tea.MouseMsg
	}{
		{name: "click", event: mouseAt},
		{
			name: "wheel",
			event: func(x, y int) tea.MouseMsg {
				return tea.MouseWheelMsg{
					X: x, Y: y,
					Button: tea.MouseWheelDown,
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fp, originX, originY, _, _ := mouseModel(t)
			m.projects = jumpWiringProjects()
			m = openJumpOverlay(t, m)

			m, cmd := update(t, m, tt.event(originX, originY))

			if cmd != nil {
				t.Error("the mouse path must return no Cmd")
			}
			if len(fp.mice) != 0 {
				t.Errorf("forwarded %v to the session, want nothing", fp.mice)
			}
			if m.listSelection() != 0 {
				t.Errorf("cursor = %d, want 0 — the session list has no mouse behaviour", m.listSelection())
			}
		})
	}
}

// TestSlashWhileThePaneIsFocusedReachesTheSession pins the precondition that makes
// "overlay open AND pane focused" all but unreachable: a focused pane owns every
// key, `/` included, so the overlay cannot be opened from there at all. Without
// this row, the precedence chosen for that combination would look arbitrary.
func TestSlashWhileThePaneIsFocusedReachesTheSession(t *testing.T) {
	m, fp, _ := jumpWiringModel(t)
	m.focus = focusRight

	m, _ = update(t, m, keyJump)

	if m.jumpOpen {
		t.Error("`/` must not open the overlay while the pane owns the keyboard")
	}
	if len(fp.texts) != 1 || fp.texts[0] != "/" {
		t.Fatalf("forwarded %v to the session, want [/]", fp.texts)
	}
}

// TestSpawnErrorModalOutranksTheOverlay pins the third interaction the plan
// asks for explicitly. The modal rows of modePrecedence outrank the jump search, so
// a background create that failed while the overlay was up takes the keyboard:
// esc dismisses the MODAL and the overlay is still there underneath. Pinning it
// is the point — the alternative is discovering it as a bug report.
func TestSpawnErrorModalOutranksTheOverlay(t *testing.T) {
	m, _, _ := jumpWiringModel(t)
	m = openJumpOverlay(t, m)

	m, _ = update(t, m, spawnFinishedMsg{name: "three", err: errors.New("boom")})
	if m.modal != inputSpawnError {
		t.Fatalf("mode = %v, want inputSpawnError", m.modal)
	}
	if !m.jumpOpen {
		t.Error("the failed create must not close the overlay")
	}

	m, _ = update(t, m, keyEscMsg())
	if m.modal != inputNormal {
		t.Errorf("mode = %v, want the modal dismissed first", m.modal)
	}
	if !m.jumpOpen {
		t.Error("the first esc belongs to the modal, not to the overlay")
	}

	m, _ = update(t, m, keyEscMsg())
	if m.jumpOpen {
		t.Error("the second esc should close the overlay")
	}
}

// --- switch-then-load preview wiring ---

// firstSessionsLoaded finds the first sessionsLoadedMsg in a collected batch,
// the way hasMsg answers a yes/no but this test needs the payload (the session
// list the real switchProjectCmds trio actually loaded) to name row 0.
func firstSessionsLoaded(msgs []tea.Msg) (sessionsLoadedMsg, bool) {
	for _, msg := range msgs {
		if sl, ok := msg.(sessionsLoadedMsg); ok {
			return sl, true
		}
	}
	return sessionsLoadedMsg{}, false
}

// firstPreviewTimer finds the first previewTimerMsg in a collected batch.
func firstPreviewTimer(msgs []tea.Msg) (previewTimerMsg, bool) {
	for _, msg := range msgs {
		if pt, ok := msg.(previewTimerMsg); ok {
			return pt, true
		}
	}
	return previewTimerMsg{}, false
}

// firstPaneAttached finds the first paneAttachedMsg in a collected batch — the
// same-project focus-request race row needs it to drive an explicit open's own
// arrival through Update (clearing attachInFlight) before asserting on what a
// LATER load does, rather than stopping at "a Cmd was dispatched".
func firstPaneAttached(msgs []tea.Msg) (paneAttachedMsg, bool) {
	for _, msg := range msgs {
		if pa, ok := msg.(paneAttachedMsg); ok {
			return pa, true
		}
	}
	return paneAttachedMsg{}, false
}

// previewCollectDeadline is collectMsgs' own cmdDeadline (focusrequest_test.go)
// widened for this file alone. previewTimerCmd arms a REAL tea.Tick of
// previewDelay (messages.go), and previewDelay and cmdDeadline are BOTH exactly
// 300ms — racing this suite's genuinely-armed timer against that deadline would
// be a coin flip. Nothing else in this package executes a real preview timer
// through a collected batch, so widening cmdDeadline itself (shared by every
// other test in the file) is not warranted; a local deadline passed into
// collectMsgsWithDeadline is.
const previewCollectDeadline = previewDelay * 2

// collectMsgsPreview is collectMsgsWithDeadline (focusrequest_test.go) at
// previewCollectDeadline instead of the package's shared cmdDeadline — the two
// rows below that let a previewTimerCmd actually fire need the wider window;
// every other test in this file goes through the ordinary short deadline via
// this same helper.
func collectMsgsPreview(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	return collectMsgsWithDeadline(t, cmd, previewCollectDeadline)
}

// TestJumpSwitchPreviewsTheFirstSessionOnceItsLoadLands is the regression
// test for the bug this wave exists to fix: switching projects through the
// jump search clears m.sessions and dispatches the load trio (switchProjectCmds),
// but nothing arms a preview when that load lands — unlike a cursor move, which
// calls schedulePreview() itself. A user who switches projects sees an empty
// pane area until they press a key, even though a session is right there,
// running, at the top of the list.
//
// Row 1 (the positive path) and row 3's second half (the arm surviving a load
// error) are load-bearing in the TDD sense: they exercise the not-yet-built
// one-shot arm flag and MUST fail against today's code, which never calls
// schedulePreview from the sessionsLoadedMsg handler at all. The rest pin
// behavior that must NOT regress once that flag exists, each against a
// specific way of getting there wrong:
//
//   - Row 2 proves a no-op Enter (the already-active project) arms nothing on
//     its next ordinary tick — there was no switch, so there must be no flag.
//     Its reload lands on a HostRunning row that is NOT the one already on
//     screen, so a mutated no-op guard (`if true`) has nothing else to hide
//     behind and this row actually fails under it.
//   - Row 4 proves the flag is one-shot: a SECOND load after the switch (the
//     routine 2s tick) must not re-arm, or every tick after a switch would
//     re-preview row 0 out from under a user who has since moved the cursor.
//   - Row 5 proves a focus-request switch (a click from another project) arms
//     nothing, and drives the click's own explicit open all the way through
//     paneAttachedMsg before asserting: while that open is still outstanding,
//     schedulePreview's OWN "explicit open blocks arming" refusal would hide a
//     wrongly-armed flag (e.g. one moved into switchProjectCmds) just as well
//     as a correctly-unset one, so stopping short of that arrival tests
//     nothing about previewOnNextLoad specifically.
//   - Row 6 is the stale-load race a rapid A→B→C switch (or an ordinary 2s
//     tick for the OUTGOING project still in flight when a switch lands) can
//     produce: a load stamped with the generation the switch just moved past
//     must be dropped whole — neither installed nor allowed to consume the
//     arm — and the correctly-stamped load that follows must still work.
//   - Row 7 is the narrower same-project click race: a focus request for a
//     session in the project the jump search just switched TO never calls
//     switchProjectCmds at all (resolveFocusTarget answers from m.sessions
//     for the active project, so target.project stays empty), so nothing
//     resets previewOnNextLoad on that path — only the explicit open itself,
//     via openPaneFor, can supersede the pending arm.
func TestJumpSwitchPreviewsTheFirstSessionOnceItsLoadLands(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "switching projects arms a preview once the new project's first load lands",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, switchCmd := update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}

				loaded, ok := firstSessionsLoaded(collectMsgsPreview(t, switchCmd))
				if !ok {
					t.Fatal("precondition: switching projects must dispatch a sessionsLoadedMsg")
				}
				if len(loaded.sessions) == 0 || !loaded.sessions[0].HostRunning {
					t.Fatalf("precondition: beta's fixture must load a HostRunning row 0, got %+v", loaded.sessions)
				}
				wantName := loaded.sessions[0].Name

				m, armCmd := update(t, m, loaded)
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatalf("switching to a project must arm a preview for its first-loaded row 0 (%q); it never did", wantName)
				}
				if armed.name != wantName {
					t.Errorf("armed preview name = %q, want row 0's %q", armed.name, wantName)
				}
				if armed.gen != m.previewGen {
					t.Errorf("armed preview gen = %d, want the model's current previewGen %d", armed.gen, m.previewGen)
				}

				// Feeding the timer back through Update — rather than stopping at
				// "a previewTimerMsg was produced" — is what proves the dial was
				// actually dispatched as a PREVIEW: attachInFlight.preview is set
				// by openPaneFor from the request applyPreviewTimer builds, and a
				// timer that merely exists but gets dropped by a stale-gen or
				// wrong-selection check would look identical from the outside.
				m, _ = update(t, m, armed)
				if m.attachInFlight.name != wantName || !m.attachInFlight.preview {
					t.Errorf("attachInFlight = %+v, want {name: %q, preview: true}", m.attachInFlight, wantName)
				}
			},
		},
		{
			name: "re-selecting the already-active project arms nothing on its next load",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				// Moved off the paned row ("one") BEFORE opening the overlay. The
				// no-op re-select must not arm anything REGARDLESS of which row the
				// next load selects, but if that row were the one already on
				// screen, schedulePreview's own "already on screen" refusal would
				// silence a previewTimerMsg whether or not the no-op guard held —
				// hiding a mutated guard (`if true`) exactly as well as a correct
				// one. Landing the reload's selection on a DIFFERENT, HostRunning
				// row removes that cover.
				m, _ = update(t, m, keyDown)
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/alpha")
				m, switchCmd := update(t, m, keyEnter)
				if switchCmd != nil {
					t.Fatalf("precondition: re-selecting the active project must dispatch nothing, got a Cmd")
				}

				m, tickCmd := update(t, m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("one"), runningSess("two")},
					at:       time.Unix(2, 0),
				})
				if s, ok := m.selected(); !ok || s.Name != "two" || !s.HostRunning {
					t.Fatalf("precondition: the reloaded selection must be a real, HostRunning, un-paned row — got %+v (ok=%t)", s, ok)
				}
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("an ordinary tick after a no-op switch must not arm a preview: got %v", msgs)
				}
			},
		},
		{
			name: "a failed first load after the switch does not arm, but the next success does",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}

				m, errCmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, err: errors.New("boom"), at: time.Unix(2, 0)})
				if msgs := collectMsgsPreview(t, errCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a failed first load must not arm a preview: got %v", msgs)
				}

				m, okCmd := update(t, m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("beta-one")},
					at:       time.Unix(3, 0),
				})
				if msgs := collectMsgsPreview(t, okCmd); !hasMsg[previewTimerMsg](msgs) {
					t.Fatalf("the arm-on-switch flag must survive a load error and be consumed by the next success: got %v", msgs)
				}
			},
		},
		{
			name: "a second load after the switch does not re-arm — the flag is one-shot",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, switchCmd := update(t, m, keyEnter)

				loaded, ok := firstSessionsLoaded(collectMsgsPreview(t, switchCmd))
				if !ok {
					t.Fatal("precondition: switching projects must dispatch a sessionsLoadedMsg")
				}
				m, armCmd := update(t, m, loaded)
				if _, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd)); !ok {
					t.Fatal("precondition: the first load after a switch must arm a preview")
				}

				m, tickCmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: loaded.sessions, at: time.Unix(9, 0)})
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a routine tick after the arm was already consumed must not re-arm: got %v", msgs)
				}
			},
		},
		{
			name: "a focus-request switch arms nothing — its own explicit open owns the attach",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}
				// A hand-built rendezvous rather than the full fsnotify fixture
				// (focusrequest_test.go's rendezvousFixture): applyFocusRequest only
				// needs a dir that maps back to a project via m.rendezvous, and
				// fromWatch stays false below so there is no watcher to arm or
				// re-arm.
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				m, focusCmd := update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the focus request must switch to beta, got %q", m.repo)
				}

				msgs := collectMsgsPreview(t, focusCmd)
				loaded, ok := firstSessionsLoaded(msgs)
				if !ok {
					t.Fatal("precondition: the focus-request switch must dispatch a sessionsLoadedMsg")
				}
				// Drive the click's own explicit open to completion — see the
				// function doc for why stopping before this arrival would leave
				// the row unable to tell a correct fix from a broken one.
				attached, ok := firstPaneAttached(msgs)
				if !ok {
					t.Fatal("precondition: the focus request's own explicit open must dispatch a paneAttachedMsg")
				}
				m, _ = update(t, m, attached)
				if m.attachInFlight.name != "" {
					t.Fatalf("precondition: the click's own arrival must clear attachInFlight, got %+v", m.attachInFlight)
				}

				m, tickCmd := update(t, m, loaded)
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a focus-request switch must not arm a preview — its own explicit open already owns the attach: got %v", msgs)
				}
			},
		},
		{
			name: "a stale-generation load from before the switch is dropped whole",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				staleGen := m.projectGen
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}
				if m.projectGen == staleGen {
					t.Fatalf("precondition: a real switch must bump projectGen past %d", staleGen)
				}

				// The shape a rapid A→B→C switch (or an ordinary 2s tick for the
				// OUTGOING project still in flight when the switch lands) produces:
				// a load stamped with the generation the switch just moved past,
				// carrying a DIFFERENT project's sessions.
				m, staleCmd := update(t, m, sessionsLoadedMsg{
					gen:      staleGen,
					sessions: []Session{runningSess("alpha-stale")},
					at:       time.Unix(2, 0),
				})
				if len(m.sessions) != 0 {
					t.Errorf("a stale-generation load must not be applied: sessions = %+v, want the switch's cleared list untouched", m.sessions)
				}
				if msgs := collectMsgsPreview(t, staleCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a stale-generation load must not arm a preview: got %v", msgs)
				}

				m, okCmd := update(t, m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("beta-one")},
					at:       time.Unix(3, 0),
				})
				if msgs := collectMsgsPreview(t, okCmd); !hasMsg[previewTimerMsg](msgs) {
					t.Fatalf("the correctly-stamped load must still arm the preview once it lands: got %v", msgs)
				}
			},
		},
		{
			name: "a same-project focus-request click racing the switch's pending arm clears it",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}
				if !m.previewOnNextLoad {
					t.Fatal("precondition: the switch must arm previewOnNextLoad")
				}

				// A click for a session in beta — the project the switch just
				// landed on — while beta's own first load is still outstanding.
				// resolveFocusTarget answers a request for the ACTIVE project from
				// m.sessions, so it is set here directly rather than waited for:
				// the point of this row is the click racing AHEAD of that load,
				// not behind it.
				m.setSessions([]Session{runningSess("beta-target")})
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				m, focusCmd := update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: a same-project click must not switch away from beta, got %q", m.repo)
				}
				// The supersede has to be visible IMMEDIATELY, at dispatch time —
				// openPaneFor clears the flag synchronously — not merely once the
				// click's own arrival lands, or the window the finding describes
				// stays open for anything racing in between.
				if m.previewOnNextLoad {
					t.Error("a same-project explicit open must supersede the switch's pending arm the moment it dispatches")
				}

				attached, ok := firstPaneAttached(collectMsgsPreview(t, focusCmd))
				if !ok {
					t.Fatal("precondition: the same-project click must dispatch its own explicit open")
				}
				m, _ = update(t, m, attached) // drive the open to completion

				m, tickCmd := update(t, m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("beta-target"), runningSess("beta-one")},
					at:       time.Unix(4, 0),
				})
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a same-project click racing the switch's pending arm must not let a later load arm a preview: got %v", msgs)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// TestApplyScanResultPromotionArmsNoPreview pins previewOnNextLoad's own doc
// comment (model.go): applyScanResult's fallback-to-first-project promotion
// (D7 — an active index of -1 resolved to the first discovered project) is
// automatic reconciliation, not a user selection, and must arm no preview the
// way a jump choice does. The fixture gives the promoted project a real,
// HostRunning row so a wrongly-armed flag would have something to preview —
// an empty list would let this pass for the wrong reason.
func TestApplyScanResultPromotionArmsNoPreview(t *testing.T) {
	d := testDeps()
	d.ActiveProjectIndex = -1
	d.Discover = func(repo string) ([]discover.SessionState, error) {
		if repo == "/roots/a" {
			return []discover.SessionState{runningSess("a-one").SessionState}, nil
		}
		return nil, nil
	}
	m := New("/not-a-repo", time.Second, d)

	got, cmd := m.Update(projectScanMsg{projects: scannedProjects("/roots/a", "/roots/b")})
	m, ok := got.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", got)
	}
	if m.activeProjectPath() != "/roots/a" {
		t.Fatalf("precondition: the promotion must land on the first project, got %q", m.activeProjectPath())
	}
	if m.previewOnNextLoad {
		t.Fatal("precondition: applyScanResult's promotion must not arm previewOnNextLoad itself")
	}

	loaded, ok := firstSessionsLoaded(collectMsgsPreview(t, cmd))
	if !ok {
		t.Fatal("precondition: the promotion must dispatch a sessionsLoadedMsg")
	}
	m, tickCmd := update(t, m, loaded)
	if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
		t.Errorf("applyScanResult's promotion must not arm a preview once its load lands: got %v", msgs)
	}
}

// --- restoring the remembered selection ---------------------------------------

// rememberedLoad is the load every row below hand-builds instead of taking the
// one switchProjectCmds actually dispatched. jumpWiringDiscoverFixture
// answers beta with a SINGLE row, so a load taken from the real trio can only
// ever leave the cursor on row 0 — "the remembered row was restored" and "the
// restore did nothing" would be the same observation, and every row here would
// pass against a no-op.
func rememberedLoad(gen int, sessions ...Session) sessionsLoadedMsg {
	return sessionsLoadedMsg{gen: gen, sessions: sessions, at: time.Unix(2, 0)}
}

// betaRows is the three-row list rows (a), (b), (d) and (e) load: the
// remembered target is the MIDDLE row, so a restore that lands anywhere else
// (row 0, or a clamp to the last row) is visible.
func betaRows() []Session {
	return []Session{runningSess("b-1"), runningSess("b-2"), runningSess("b-3")}
}

// startupSelectionModel is the startup counterpart of jumpWiringModel: a
// New-built Model whose LoadSelections seam remembers one row for the launch
// repo, left deliberately NOT ready. Rows (f) and (g) become ready by sending a
// real tea.WindowSizeMsg through Update — the arrival the consumption guard
// waits on — and row (h) exists precisely to exercise the state before it.
func startupSelectionModel(remembered, initialSession string) Model {
	d := testDeps()
	d.LoadSelections = func() map[string]string { return map[string]string{"/repo": remembered} }
	d.InitialSessionName = initialSession
	d.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane(paneSentinel), nil }
	return New("/repo", time.Second, d)
}

// startupRows is rows (f), (g) and (h)'s session list, shaped like betaRows for
// the same reason: the remembered row is the middle one.
func startupRows() []Session {
	return []Session{runningSess("s-1"), runningSess("s-2"), runningSess("s-3")}
}

// TestJumpRestoresTheRememberedSelectionOnLoad is the restore half of the
// selection-memory feature: the row the user was last on comes back, by NAME,
// on the first load after a jump switch and on the first load at startup.
//
// Rows (a) and (f) are the load-bearing TDD rows — they exercise the restore
// itself and must fail against code that only ever previews row 0. Row (h) is
// the second one: it pins the `m.ready` half of the consumption guard, which a
// restore built for the jump search path alone would not have (ready is always
// true by the time a switch can happen). The rest pin what must NOT change:
//
//   - Row (b) is W3.T1's phantom name (a pending placeholder's guessed name
//     that applyScratchDirs later corrected, or a session since cleaned): an
//     absent remembered row is a plain fall back to row 0, never an error.
//   - Row (c) is the same fall back for a name that IS in the list but only as
//     a worktree-side fault row, which sessionIndex skips by design — a fault
//     row can never satisfy a lookup whose caller goes on to open a pane.
//   - Row (d) is applyFocusRequest's explicit open: it selects the clicked
//     session by name through its OWN arm (never previewOnNextLoad), and
//     still arms no preview — the pane it just opened already owns the
//     attach. Row (e) is applyScanResult's promotion, which restores nothing
//     at all: a restore living inside the previewOnNextLoad block would
//     wrongly inherit that exclusion too, so row (e)'s model is made `ready`
//     first, the same way row (d)'s is, so the guard added for row (h)
//     cannot be what makes it pass.
//   - Row (g) is `--open <name>`: an explicit startup pane request already
//     names the session to show, so New arms nothing and the load must leave
//     both the cursor and that open alone.
func TestJumpRestoresTheRememberedSelectionOnLoad(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "(a) a jump switch restores the remembered row by name and previews it",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}

				m, armCmd := update(t, m, rememberedLoad(m.projectGen, betaRows()...))

				if s, ok := m.selected(); !ok || s.Name != "b-2" {
					t.Errorf("selected = %q (ok=%t), want the remembered %q", s.Name, ok, "b-2")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("the restoring load must still arm a preview for the row it restored")
				}
				if armed.name != "b-2" {
					t.Errorf("armed preview name = %q, want the remembered %q", armed.name, "b-2")
				}
			},
		},
		{
			name: "(b) a remembered name that is not in the list falls back to row 0",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-9"}
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)

				m, armCmd := update(t, m, rememberedLoad(m.projectGen, betaRows()...))

				if s, ok := m.selected(); !ok || s.Name != "b-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q", s.Name, ok, "b-1")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("an absent remembered row must still preview row 0")
				}
				if armed.name != "b-1" {
					t.Errorf("armed preview name = %q, want row 0's %q", armed.name, "b-1")
				}
			},
		},
		{
			name: "(c) a remembered name present only as a worktree-fault row falls back to row 0",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)

				// b-1 asks the owner something, so it shares Needs you with
				// the fault row and stays row 0 in the list's screen order
				// (T16.4), where a fault row otherwise comes first.
				first := runningSess("b-1")
				first.AwaitingInput = true
				m, armCmd := update(t, m, rememberedLoad(m.projectGen,
					first,
					faultSess("b-2", session.KindWorktreeMissing, "worktree directory no longer exists"),
					runningSess("b-3"),
				))

				if s, ok := m.selected(); !ok || s.Name != "b-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q", s.Name, ok, "b-1")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("a fault-row remembered name must still preview row 0")
				}
				if armed.name != "b-1" {
					t.Errorf("armed preview name = %q, want row 0's %q", armed.name, "b-1")
				}
			},
		},
		{
			name: "(d) a focus-request switch selects and remembers the focused session on its first load, arming no preview",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				m, focusCmd := update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the focus request must switch to beta, got %q", m.repo)
				}
				// The click's own explicit open is driven to completion for
				// TestJumpSwitchPreviewsTheFirstSessionOnceItsLoadLands's row-5
				// reason: while it is outstanding, schedulePreview's own refusal
				// would silence a wrongly-restored row just as well as a
				// correctly-unrestored one.
				attached, ok := firstPaneAttached(collectMsgsPreview(t, focusCmd))
				if !ok {
					t.Fatal("precondition: the focus request's own explicit open must dispatch a paneAttachedMsg")
				}
				m, _ = update(t, m, attached)

				m, tickCmd := update(t, m, rememberedLoad(m.projectGen,
					runningSess("b-1"), runningSess("beta-target"), runningSess("b-3"),
				))

				if s, ok := m.selected(); !ok || s.Name != "beta-target" {
					t.Errorf("selected = %q (ok=%t), want the clicked %q — a focus-request switch selects by name through its own arm, not the stale remembered %q or row 0", s.Name, ok, "beta-target", "b-2")
				}
				if got := m.rememberedSelection["/roots/beta"]; got != "beta-target" {
					t.Errorf("rememberedSelection[/roots/beta] = %q, want the clicked %q", got, "beta-target")
				}
				if got := m.dirtySelections["/roots/beta"]; got != "beta-target" {
					t.Errorf("dirtySelections[/roots/beta] = %q, want the clicked %q", got, "beta-target")
				}
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a focus-request switch must not arm a preview — its own explicit open already owns the attach: got %v", msgs)
				}
			},
		},
		{
			name: "(d2) a focus-request switch whose target is absent from the first load falls back to row 0 and remembers nothing new",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				m, focusCmd := update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the focus request must switch to beta, got %q", m.repo)
				}
				attached, ok := firstPaneAttached(collectMsgsPreview(t, focusCmd))
				if !ok {
					t.Fatal("precondition: the focus request's own explicit open must dispatch a paneAttachedMsg")
				}
				m, _ = update(t, m, attached)

				m, tickCmd := update(t, m, rememberedLoad(m.projectGen, betaRows()...))

				if s, ok := m.selected(); !ok || s.Name != "b-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q — the clicked target is absent from this load", s.Name, ok, "b-1")
				}
				if got := m.rememberedSelection["/roots/beta"]; got != "b-2" {
					t.Errorf("rememberedSelection[/roots/beta] = %q, want the untouched %q", got, "b-2")
				}
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a focus-request switch must not arm a preview: got %v", msgs)
				}
			},
		},
		{
			name: "(d3) a jump switch after a focus-request switch does not inherit the focus arm",
			run: func(t *testing.T) {
				m, _, _ := jumpWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2", "/roots/alpha": "a-2"}
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				// Beta's own load is deliberately never delivered: the point of this
				// row is what a jump switch does with no help from it, not what
				// happens once it lands.
				m, _ = update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the focus request must switch to beta, got %q", m.repo)
				}
				// A click moves the focus right at request time (plan W3.T1 e),
				// and `/` is a list key: hand the keyboard back to the list first.
				m, _ = update(t, m, chord('\\'))

				m = openJumpOverlay(t, m)
				// alpha's project row is selected by identity, so this row's
				// intent, a real switch away from beta, holds wherever the
				// jump's order (needs-you first) puts either project.
				m = jumpSelectProject(t, m, "/roots/alpha")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/alpha" {
					t.Fatalf("precondition: the jump switch must land back on alpha, got %q", m.repo)
				}

				m, armCmd := update(t, m, rememberedLoad(m.projectGen,
					runningSess("a-1"), runningSess("a-2"), runningSess("a-3"),
				))

				if s, ok := m.selected(); !ok || s.Name != "a-2" {
					t.Errorf("selected = %q (ok=%t), want the jump search's own remembered %q, never the stale focus-request target %q", s.Name, ok, "a-2", "beta-target")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("the jump search's own restore must still arm a preview for the row it restored")
				}
				if armed.name != "a-2" {
					t.Errorf("armed preview name = %q, want the jump search's own remembered %q", armed.name, "a-2")
				}
			},
		},
		{
			name: "(e) applyScanResult's promotion restores nothing and previews nothing",
			run: func(t *testing.T) {
				d := testDeps()
				d.ActiveProjectIndex = -1
				m := New("/not-a-repo", time.Second, d)
				// Ready BEFORE the load: without it the guard row (h) pins would
				// be what suppresses the restore here, and this row would pass
				// against a promotion that does arm one.
				m.ready = true
				m.width, m.height = 120, 40
				m.sizeSessionList()
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}

				m, _ = update(t, m, projectScanMsg{projects: scannedProjects("/roots/beta", "/roots/gamma")})
				if m.activeProjectPath() != "/roots/beta" {
					t.Fatalf("precondition: the promotion must land on the first project, got %q", m.activeProjectPath())
				}
				if m.previewOnNextLoad {
					t.Fatal("precondition: applyScanResult's promotion must not arm previewOnNextLoad itself")
				}

				m, tickCmd := update(t, m, rememberedLoad(m.projectGen, betaRows()...))

				if s, ok := m.selected(); !ok || s.Name != "b-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q — a promotion restores nothing", s.Name, ok, "b-1")
				}
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("applyScanResult's promotion must not arm a preview: got %v", msgs)
				}
			},
		},
		{
			name: "(f) startup restores the remembered row on its first load and previews it",
			run: func(t *testing.T) {
				m := startupSelectionModel("s-2", "")
				if !m.previewOnNextLoad {
					t.Fatal("precondition: a remembered entry for the launch repo must arm previewOnNextLoad in New")
				}
				m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

				m, armCmd := update(t, m, rememberedLoad(m.projectGen, startupRows()...))

				if s, ok := m.selected(); !ok || s.Name != "s-2" {
					t.Errorf("selected = %q (ok=%t), want the remembered %q", s.Name, ok, "s-2")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("startup with a remembered row must preview it once the first load lands")
				}
				if armed.name != "s-2" {
					t.Errorf("armed preview name = %q, want the remembered %q", armed.name, "s-2")
				}
			},
		},
		{
			name: "(g) startup with InitialSessionName restores nothing — the explicit open owns the pane",
			run: func(t *testing.T) {
				m := startupSelectionModel("s-2", "s-1")
				if m.previewOnNextLoad {
					t.Fatal("precondition: InitialSessionName must leave previewOnNextLoad false in New")
				}
				m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

				m, cmd := update(t, m, rememberedLoad(m.projectGen, startupRows()...))

				if s, ok := m.selected(); !ok || s.Name != "s-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q — an `--open` startup restores nothing", s.Name, ok, "s-1")
				}
				if m.attachInFlight.name != "s-1" || m.attachInFlight.preview {
					t.Errorf("attachInFlight = %+v, want the startup pane's own explicit open for %q", m.attachInFlight, "s-1")
				}
				if msgs := collectMsgsPreview(t, cmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("an `--open` startup must not arm a preview: got %v", msgs)
				}
			},
		},
		{
			name: "(h) a first load before the first WindowSizeMsg leaves the arm set for the next one",
			run: func(t *testing.T) {
				m := startupSelectionModel("s-2", "")
				if !m.previewOnNextLoad || m.ready {
					t.Fatalf("precondition: want an armed, not-yet-ready Model, got armed=%t ready=%t",
						m.previewOnNextLoad, m.ready)
				}

				m, earlyCmd := update(t, m, rememberedLoad(m.projectGen, startupRows()...))

				if !m.previewOnNextLoad {
					t.Error("a load before the first WindowSizeMsg must leave the arm set for the next load")
				}
				if s, ok := m.selected(); !ok || s.Name != "s-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q — nothing is restored at width 0", s.Name, ok, "s-1")
				}
				if msgs := collectMsgsPreview(t, earlyCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a load at width 0 must not arm a preview: got %v", msgs)
				}

				m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
				m, armCmd := update(t, m, rememberedLoad(m.projectGen, startupRows()...))

				if s, ok := m.selected(); !ok || s.Name != "s-2" {
					t.Errorf("selected = %q (ok=%t), want the remembered %q once the size arrived", s.Name, ok, "s-2")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("the load after the first WindowSizeMsg must consume the arm and preview the restored row")
				}
				if armed.name != "s-2" {
					t.Errorf("armed preview name = %q, want the remembered %q", armed.name, "s-2")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// --- per-project open counts ---------------------------------------------------

// openSpy is the Deps.RecordProjectOpen fake for every row below.
//
// The mutex is not decoration: a tick flush runs inside a tea.Cmd and
// collectMsgs runs batch members on their own goroutines, so the write here and
// the test's read of it are genuinely concurrent under -race. And it never calls
// t.Fatal — a Goexit on a Cmd goroutine parks the collector forever instead of
// failing the test, so every row counts calls and asserts afterwards.
type openSpy struct {
	mu    sync.Mutex
	calls []string
}

func (s *openSpy) record(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, path)
	return nil
}

func (s *openSpy) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// countOpenFlushes counts the projectOpensFlushedMsg values in a collected
// batch, which is how "exactly ONE flush Cmd per tick" is asserted: one flush
// Cmd produces exactly one such message, so the message count IS the Cmd count.
// Counting the spy's calls alone cannot tell one Cmd writing three increments
// from three Cmds writing one each — and only the second shape can lose one.
func countOpenFlushes(msgs []tea.Msg) int {
	n := 0
	for _, msg := range msgs {
		if _, ok := msg.(projectOpensFlushedMsg); ok {
			n++
		}
	}
	return n
}

// projectOpensModel is the fixture the open-count rows use: three projects
// with nothing awaiting and a recording RecordProjectOpen seam.
//
// opens is what the load seam answers, copied on the way in so a row can assert
// against its own literal afterwards.
func projectOpensModel(t *testing.T, opens map[string]int) (Model, *openSpy) {
	t.Helper()
	spy := &openSpy{}
	d := testDeps()
	d.Projects = jumpWiringProjects()
	d.ActiveProjectIndex = 0
	d.LoadProjectOpens = func() map[string]int {
		seeded := make(map[string]int, len(opens))
		for path, n := range opens {
			seeded[path] = n
		}
		return seeded
	}
	d.RecordProjectOpen = spy.record
	m := New("/roots/alpha", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40
	m.sizeSessionList()
	return m, spy
}

// TestProjectOpensLaunchProjectPath is the pure core of New's launch recording:
// the resolved project path when the active index is in range, and "" — record
// nothing — otherwise.
//
// The out-of-range rows are the load-bearing ones. They are where this function
// differs from selectionKeyFor, which falls back to the launch repo: a count
// keyed on a repo spelling no project entry carries would rank a project that
// is not in the jump search's list at all, and leave the real entry at zero.
func TestProjectOpensLaunchProjectPath(t *testing.T) {
	tests := []struct {
		name     string
		projects []project.Project
		active   int
		want     string
	}{
		{
			name: "no projects records nothing",
			want: "",
		},
		{
			name:     "a negative index records nothing",
			projects: []project.Project{{Path: "/repo"}},
			active:   -1,
			want:     "",
		},
		{
			name:     "an out-of-range positive index records nothing",
			projects: []project.Project{{Path: "/repo"}},
			active:   1,
			want:     "",
		},
		{
			name:     "an in-range index returns that project's resolved path",
			projects: []project.Project{{Path: "/repo/sub"}, {Path: "/other"}},
			active:   0,
			want:     "/repo/sub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchProjectPath(tt.projects, tt.active); got != tt.want {
				t.Errorf("launchProjectPath(%+v, %d) = %q, want %q", tt.projects, tt.active, got, tt.want)
			}
		})
	}
}

// TestProjectOpensNewSeedsAndRecordsTheLaunch is the startup half of the
// feature: New loads the counts ONCE through the seam and records the launch
// target on top of what it loaded.
//
// Row 3 is load-bearing twice over. It is the only row where the seam answers a
// count for the launch project, so it is what proves the launch open is an
// INCREMENT on the loaded value rather than a reset to 1 — and it pins that
// only the launch project is left pending, not every project the seam returned,
// which would rewrite the whole file on the first tick of every run.
//
// Row 4 is the launched-outside-any-repo case (index -1): there is no entry to
// count, and the loaded counts must survive untouched so the overlay still
// ranks the projects the first scan finds.
func TestProjectOpensNewSeedsAndRecordsTheLaunch(t *testing.T) {
	tests := []struct {
		name      string
		projects  []project.Project
		active    int
		load      func() map[string]int
		wantOpens map[string]int
		wantDirty map[string]int
	}{
		{
			name:      "no seam and no launch project leaves the tally empty",
			wantOpens: nil,
			wantDirty: nil,
		},
		{
			name:      "no seam still records the launch project",
			projects:  jumpWiringProjects(),
			active:    0,
			wantOpens: map[string]int{"/roots/alpha": 1},
			wantDirty: map[string]int{"/roots/alpha": 1},
		},
		{
			name:     "the launch project increments the count the seam loaded",
			projects: jumpWiringProjects(),
			active:   0,
			load: func() map[string]int {
				return map[string]int{"/roots/alpha": 3, "/roots/beta": 7}
			},
			wantOpens: map[string]int{"/roots/alpha": 4, "/roots/beta": 7},
			wantDirty: map[string]int{"/roots/alpha": 1},
		},
		{
			name:     "launched outside any repo records nothing and keeps the loaded counts",
			projects: jumpWiringProjects(),
			active:   -1,
			load: func() map[string]int {
				return map[string]int{"/roots/beta": 2}
			},
			wantOpens: map[string]int{"/roots/beta": 2},
			wantDirty: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loads := 0
			d := testDeps()
			d.Projects = tt.projects
			d.ActiveProjectIndex = tt.active
			if tt.load != nil {
				d.LoadProjectOpens = func() map[string]int {
					loads++
					return tt.load()
				}
			}

			m := New("/roots/alpha", time.Second, d)

			if tt.load != nil && loads != 1 {
				t.Errorf("LoadProjectOpens called %d times, want exactly 1", loads)
			}
			if !maps.Equal(m.projectOpens, tt.wantOpens) {
				t.Errorf("projectOpens = %#v, want %#v", m.projectOpens, tt.wantOpens)
			}
			if !maps.Equal(m.dirtyOpens, tt.wantDirty) {
				t.Errorf("dirtyOpens = %#v, want %#v", m.dirtyOpens, tt.wantDirty)
			}
		})
	}
}

// TestProjectOpensRecordsDeliberateActivationsOnly is the rule the counts mean
// anything at all under: an open is the user NAMING a project, and every other
// way the active project can change is not one.
//
// All three rows are load-bearing, and the last two are the ones a naive "bump
// it wherever the active project changes" implementation fails. Enter on the
// already-active project is documented as a no-op, so counting it would let a
// held-down Enter inflate the row the cursor already sits on; and
// applyScanResult's promotion of the first project is automatic reconciliation
// for an active project that vanished, so counting it would rank a project the
// user never chose.
func TestProjectOpensRecordsDeliberateActivationsOnly(t *testing.T) {
	tests := []struct {
		name      string
		act       func(t *testing.T) Model
		wantOpens map[string]int
		wantDirty map[string]int
	}{
		{
			name: "a jump choice of another project is recorded",
			act: func(t *testing.T) Model {
				m, _ := projectOpensModel(t, nil)
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("setup: the switch must land on beta, got %q", m.repo)
				}
				return m
			},
			wantOpens: map[string]int{"/roots/alpha": 1, "/roots/beta": 1},
			wantDirty: map[string]int{"/roots/alpha": 1, "/roots/beta": 1},
		},
		{
			name: "re-selecting the active project records nothing",
			act: func(t *testing.T) Model {
				m, _ := projectOpensModel(t, nil)
				m = openJumpOverlay(t, m)
				m = jumpSelectProject(t, m, "/roots/alpha")
				m, cmd := update(t, m, keyEnter)
				if cmd != nil {
					t.Fatal("setup: re-selecting the active project must dispatch nothing")
				}
				return m
			},
			wantOpens: map[string]int{"/roots/alpha": 1},
			wantDirty: map[string]int{"/roots/alpha": 1},
		},
		{
			name: "the first-project fallback promotion records nothing",
			act: func(t *testing.T) Model {
				d := testDeps()
				d.ActiveProjectIndex = -1
				d.LoadProjectOpens = func() map[string]int { return map[string]int{"/roots/b": 4} }
				m := New("/not-a-repo", time.Second, d)
				if len(m.dirtyOpens) != 0 {
					t.Fatalf("setup: a launch outside any repo must record nothing, got %#v", m.dirtyOpens)
				}
				m, _ = update(t, m, projectScanMsg{projects: scannedProjects("/roots/a", "/roots/b")})
				if m.activeProjectPath() != "/roots/a" {
					t.Fatalf("setup: the promotion must land on the first project, got %q", m.activeProjectPath())
				}
				return m
			},
			wantOpens: map[string]int{"/roots/b": 4},
			wantDirty: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.act(t)

			if !maps.Equal(m.projectOpens, tt.wantOpens) {
				t.Errorf("projectOpens = %#v, want %#v", m.projectOpens, tt.wantOpens)
			}
			if !maps.Equal(m.dirtyOpens, tt.wantDirty) {
				t.Errorf("dirtyOpens = %#v, want %#v", m.dirtyOpens, tt.wantDirty)
			}
		})
	}
}

// TestProjectOpensFlushOnTick is the debounce: the 2s discovery tick is the only
// thing that writes, it writes through exactly one Cmd, and it empties the
// pending set so the next tick writes nothing.
//
// Two opens of ALPHA inside the one tick is the load-bearing part. The seam adds
// one per call, so the pending tally has to produce TWO calls for it — a
// per-key set that wrote each dirty project once would leave the file one behind
// the in-memory count, permanently, with nothing to ever re-dirty it.
func TestProjectOpensFlushOnTick(t *testing.T) {
	m, spy := projectOpensModel(t, nil)

	m = openJumpOverlay(t, m)
	m = jumpSelectProject(t, m, "/roots/beta")
	m, _ = update(t, m, keyEnter)
	m = openJumpOverlay(t, m)
	m = jumpSelectProject(t, m, "/roots/alpha")
	m, _ = update(t, m, keyEnter)
	if got, want := m.dirtyOpens, map[string]int{"/roots/alpha": 2, "/roots/beta": 1}; !maps.Equal(got, want) {
		t.Fatalf("setup: dirtyOpens = %#v, want %#v", got, want)
	}

	m, tick := update(t, m, tickMsg(time.Unix(1, 0)))
	if len(m.dirtyOpens) != 0 {
		t.Errorf("dirtyOpens = %#v, want the set emptied by the flush", m.dirtyOpens)
	}
	msgs := collectMsgsPreview(t, tick)
	if n := countOpenFlushes(msgs); n != 1 {
		t.Errorf("tick produced %d flush Cmds, want exactly 1", n)
	}
	calls := spy.snapshot()
	counted := map[string]int{}
	for _, path := range calls {
		counted[path]++
	}
	if want := map[string]int{"/roots/alpha": 2, "/roots/beta": 1}; !maps.Equal(counted, want) {
		t.Errorf("RecordProjectOpen calls = %v (tallied %#v), want %#v", calls, counted, want)
	}

	m, second := update(t, m, tickMsg(time.Unix(2, 0)))
	if n := countOpenFlushes(collectMsgsPreview(t, second)); n != 0 {
		t.Errorf("a second tick with nothing pending produced %d flush Cmds, want 0", n)
	}
	if got := spy.snapshot(); len(got) != len(calls) {
		t.Errorf("RecordProjectOpen calls = %v, want the first tick's %d and no more", got, len(calls))
	}
}

// TestProjectOpensTickWithNilSeamClearsTheSet covers the un-wired dashboard
// (every Model in this package that does not opt in, and any caller that builds
// Deps without the seam): nothing is written, and the pending set is cleared
// anyway rather than growing an entry per open that nothing will ever flush.
func TestProjectOpensTickWithNilSeamClearsTheSet(t *testing.T) {
	d := testDeps()
	d.Projects = jumpWiringProjects()
	d.ActiveProjectIndex = 0
	m := New("/roots/alpha", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40
	if m.deps.RecordProjectOpen != nil {
		t.Fatal("setup: testDeps must leave RecordProjectOpen nil")
	}
	if len(m.dirtyOpens) != 1 {
		t.Fatalf("setup: dirtyOpens = %#v, want the launch project pending", m.dirtyOpens)
	}

	m, tick := update(t, m, tickMsg(time.Unix(1, 0)))
	if len(m.dirtyOpens) != 0 {
		t.Errorf("dirtyOpens = %#v, want the set cleared even with no seam", m.dirtyOpens)
	}
	if n := countOpenFlushes(collectMsgsPreview(t, tick)); n != 0 {
		t.Errorf("a nil seam produced %d flush Cmds, want 0", n)
	}
}

// TestProjectOpensQuitFlushesInline pins that the quit path writes INLINE, not
// through a Cmd: tea.Quit is returned beside it, so a Cmd may never run, and
// quitting right after switching to a project is the shape that makes the
// just-opened project's count the one most worth saving. The assertion is
// deliberately made BEFORE the returned Cmd is touched — running it first would
// let a Cmd-based implementation pass.
//
// The multi-increment row is the same rule the tick flush has: a pending count
// of two is two calls, because the seam adds one per call.
func TestProjectOpensQuitFlushesInline(t *testing.T) {
	tests := []struct {
		name  string
		dirty map[string]int
		want  map[string]int
	}{
		{
			name:  "every pending increment is written before the quit",
			dirty: map[string]int{"/roots/alpha": 2, "/roots/beta": 1},
			want:  map[string]int{"/roots/alpha": 2, "/roots/beta": 1},
		},
		{
			name:  "an empty set writes nothing",
			dirty: nil,
			want:  map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(runningSess("a"))
			spy := &openSpy{}
			m.deps.RecordProjectOpen = spy.record
			m.dirtyOpens = tt.dirty

			nm, cmd := m.requestQuit()

			counted := map[string]int{}
			for _, path := range spy.snapshot() {
				counted[path]++
			}
			if !maps.Equal(counted, tt.want) {
				t.Errorf("RecordProjectOpen calls = %#v, want %#v synchronous calls", counted, tt.want)
			}
			if got, ok := nm.(Model); !ok || !got.quitting {
				t.Errorf("requestQuit must still quit (quitting=%v)", ok && got.quitting)
			}
			if cmd == nil {
				t.Error("requestQuit returned no Cmd, want tea.Quit")
			}
		})
	}
}

// TestProjectOpensQuitWithNilSeamIsInert is the un-wired dashboard on the quit
// path: no seam, no write, no panic.
func TestProjectOpensQuitWithNilSeamIsInert(t *testing.T) {
	m := testModel(runningSess("a"))
	m.dirtyOpens = map[string]int{"/repo": 1}

	nm, cmd := m.requestQuit()

	if got, ok := nm.(Model); !ok || !got.quitting {
		t.Errorf("requestQuit must still quit (quitting=%v)", ok && got.quitting)
	}
	if cmd == nil {
		t.Error("requestQuit returned no Cmd, want tea.Quit")
	}
}
