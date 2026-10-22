package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// tokensFixtureRows are the rows of a task's token table as `afk tokens`
// prints them: one per phase and role, a subtotal per phase and the total.
// The model names are neutral on purpose; this package names no provider.
var tokensFixtureRows = [][]string{
	{"plan", "planner", "model-a", "4", "1210", "8812", "40211", "120933", "71840", "2.5%"},
	{"execute", "executor", "model-b", "88", "118230", "601230", "9102342", "1802311", "2190334", "76.3%"},
	{"execute", "verifier", "model-b", "31", "6120", "201330", "1201112", "260130", "461225", "16.1%"},
	{"review", "reviewer", "model-a", "20", "2171", "94131", "660405", "121840", "147840", "5.1%"},
	{"plan", "(subtotal)", "", "4", "1210", "8812", "40211", "120933", "71840", "2.5%"},
	{"execute", "(subtotal)", "", "119", "124350", "802560", "10303454", "2062441", "2651559", "92.4%"},
	{"review", "(subtotal)", "", "20", "2171", "94131", "660405", "121840", "147840", "5.1%"},
	{"total", "", "", "143", "127731", "905503", "11004070", "2305214", "2871239", "100.0%"},
}

// tokensTable lays rows out the way the command's table is laid out: a
// right-aligned tabwriter with a two-cell gap, under a header, with the
// columns at the given indexes.
func tokensTable(header []string, cols []int) []string {
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, strings.Join(header, "\t")+"\t")
	for _, r := range tokensFixtureRows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = r[c]
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t")+"\t")
	}
	tw.Flush()
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

// tokensFixture is a recorded task's record, filled the way the root
// package's adapter fills it: "Task <id>" over the full table, and over the
// compact one.
func tokensFixture(task string) TaskTokens {
	full := tokensTable(
		[]string{"phase", "role", "model", "calls", "input", "output", "cache write", "cache read", "weighted", "share"},
		[]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	compact := tokensTable(
		[]string{"phase", "role", "model", "calls", "weighted", "share"},
		[]int{0, 1, 2, 3, 8, 9})
	return TaskTokens{
		State:   TaskTokensRecorded,
		Task:    task,
		Path:    "/repo/.afk/log/tokens.jsonl",
		Short:   "2.9M weighted",
		Lines:   append([]string{"Task " + task}, full...),
		Compact: append([]string{"Task " + task}, compact...),
	}
}

// tokensSeam records each call to Deps.LoadTaskTokens and answers with rec.
type tokensSeam struct {
	calls []string
	rec   func(task string) TaskTokens
}

func (s *tokensSeam) load(root, task string) TaskTokens {
	s.calls = append(s.calls, root+" "+task)
	return s.rec(task)
}

// tokensBuildTab is the Build tab with the keyboard on the right and the
// cursor on the current task (T1.2), with the tokens seam wired to s. The
// read the first build load issues is NOT run, so no record is held yet.
func tokensBuildTab(t *testing.T, s *tokensSeam) Model {
	t.Helper()
	m := testModel(sess("alpha"))
	if s != nil {
		m.deps.LoadTaskTokens = s.load
	}
	m = withBuild(t, m, buildTabSnap())
	m = openBuildTab(t, m)
	if !m.buildCursorShown() || m.buildCursorID != "T1.2" {
		t.Fatalf("setup: cursor shown=%v on %q, want T1.2", m.buildCursorShown(), m.buildCursorID)
	}
	return m
}

// runTokensCmd runs a command that must be a token read and returns its
// message.
func runTokensCmd(t *testing.T, cmd tea.Cmd) taskTokensLoadedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command was returned; want a token read")
	}
	msg, ok := cmd().(taskTokensLoadedMsg)
	if !ok {
		t.Fatalf("the command returned %T, want taskTokensLoadedMsg", cmd())
	}
	return msg
}

// holdTokens delivers the cursor's task's record, so it is held.
func holdTokens(t *testing.T, m Model, rec TaskTokens) Model {
	t.Helper()
	m, _ = update(t, m, taskTokensLoadedMsg{gen: m.projectGen, root: m.build.Root, task: m.buildCursorID, rec: rec})
	if m.taskTokensID != m.buildCursorID {
		t.Fatalf("setup: the record for %q is not held", m.buildCursorID)
	}
	return m
}

// TestTaskTokensRowTotal pins where the cursor row's total goes. The fixture
// is buildTabSnap with the cursor on T1.2, whose row is "T1.2  running  Build
// snapshot types and task rows  t1-2 · executing": 70 cells unaligned, and
// 85 with the total. Load-bearing rows:
//   - "wide": the total follows the detail when there is room.
//   - "detail fits, total does not" is the order the plan asks for: the total
//     goes first, and the detail stays.
//   - "detail dropped though the total would fit": at 70 columns the row has
//     lost its detail, yet its title and the total would fit in the 68 cells
//     left. It must still show no total, so the total cannot outlive the
//     detail; this is the row that fails if the whole-row check goes.
//   - "no total" draws exactly the tab without one, so a dashboard with no
//     record looks as it did before T7.12.
//
// Every row also checks that the tab is one line per task, no line wider than
// the width, and that no other row gains the total.
func TestTaskTokensRowTotal(t *testing.T) {
	const total = "2.9M weighted"
	cases := []struct {
		name       string
		width      int
		total      string
		wantTotal  bool
		wantDetail bool
	}{
		{name: "wide", width: 120, total: total, wantTotal: true, wantDetail: true},
		{name: "unaligned with room", width: 90, total: total, wantTotal: true, wantDetail: true},
		{name: "detail fits, total does not", width: 80, total: total, wantTotal: false, wantDetail: true},
		{name: "detail dropped though the total would fit", width: 70, total: total, wantTotal: false, wantDetail: false},
		{name: "detail dropped", width: 60, total: total, wantTotal: false, wantDetail: false},
		{name: "very narrow", width: 20, total: total, wantTotal: false, wantDetail: false},
		{name: "no total", width: 120, total: "", wantTotal: false, wantDetail: true},
	}
	snap := buildTabSnap()
	const cursor = 1
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := buildTabLinesWith(defaultLeaderKey, snap, PlanStatus{}, tc.width, cursor, tc.total)
			without := buildTabLinesAt(defaultLeaderKey, snap, PlanStatus{}, tc.width, cursor)
			if len(lines) != len(without) {
				t.Fatalf("%d lines with the total, %d without; a row grew a line", len(lines), len(without))
			}
			top := buildRowsTop(defaultLeaderKey, snap, PlanStatus{}, tc.width)
			for i, ln := range lines {
				if w := lipgloss.Width(ln); w > tc.width {
					t.Errorf("line %d is %d wide, max %d", i, w, tc.width)
				}
				if i != top+cursor && strings.Contains(xansi.Strip(ln), total) {
					t.Errorf("line %d, not the cursor's, carries the total: %q", i, xansi.Strip(ln))
				}
			}
			row := xansi.Strip(lines[top+cursor])
			if !strings.Contains(row, "T1.2") {
				t.Fatalf("line %d is not the cursor's row: %q", top+cursor, row)
			}
			if got := strings.Contains(row, total); got != tc.wantTotal {
				t.Errorf("total shown = %v, want %v: %q", got, tc.wantTotal, row)
			}
			if got := strings.Contains(row, "t1-2 · executing"); got != tc.wantDetail {
				t.Errorf("detail shown = %v, want %v: %q", got, tc.wantDetail, row)
			}
			if !tc.wantTotal {
				// Without the total the row is exactly the row without one.
				if row != xansi.Strip(without[top+cursor]) {
					t.Errorf("row without the total differs:\n got %q\nwant %q", row, xansi.Strip(without[top+cursor]))
				}
			}
		})
	}
}

// TestTaskTokensRowTotalFromModel pins which record gives the cursor's row a
// total. Load-bearing rows: only a recorded ledger gives one ("none" and
// "unreadable" must not, or the row would claim a cost nobody measured), a
// record held for another task gives none (the row must not show a
// neighbour's cost while its own is read), and the list having the keyboard
// draws no cursor and so no total. Each frame is drawn 240 columns wide so a
// missing total can only mean there was none to draw.
func TestTaskTokensRowTotalFromModel(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, m Model) Model
		want  string
	}{
		{"recorded", func(t *testing.T, m Model) Model { return holdTokens(t, m, tokensFixture("T1.2")) }, "2.9M weighted"},
		{"none", func(t *testing.T, m Model) Model {
			return holdTokens(t, m, TaskTokens{State: TaskTokensNone, Task: "T1.2", Short: "0 weighted"})
		}, ""},
		{"unreadable", func(t *testing.T, m Model) Model {
			return holdTokens(t, m, TaskTokens{State: TaskTokensUnreadable, Task: "T1.2", Err: "reading ledger: denied", Short: "0 weighted"})
		}, ""},
		{"no record yet", func(t *testing.T, m Model) Model { return m }, ""},
		{"record for another task", func(t *testing.T, m Model) Model {
			m = holdTokens(t, m, tokensFixture("T1.2"))
			m, _ = update(t, m, keyDown)
			return m
		}, ""},
		{"list has the keyboard", func(t *testing.T, m Model) Model {
			m = holdTokens(t, m, tokensFixture("T1.2"))
			m, _ = pressAction(t, m, keyEscMsg())
			if m.focus != focusList {
				t.Fatal("setup: the list does not have the keyboard")
			}
			return m
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &tokensSeam{rec: func(string) TaskTokens { return TaskTokens{} }}
			m := tc.setup(t, tokensBuildTab(t, s))
			// Wide enough that the detail pane has room for the total beside
			// T1.2's whole row; the narrowing is TestTaskTokensRowTotal's.
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 240, Height: 40})
			if got := m.buildCursorTotal(); got != tc.want {
				t.Fatalf("buildCursorTotal() = %q, want %q", got, tc.want)
			}
			frame := render(m)
			if tc.want != "" && !strings.Contains(frame, tc.want) {
				t.Errorf("the frame does not show %q", tc.want)
			}
			if tc.want == "" && strings.Contains(frame, "weighted") {
				t.Errorf("the frame shows a total it should not")
			}
		})
	}
}

// TestTaskTokensContent is the overlay's body for each record. Load-bearing
// rows: the full table when it fits and the compact one when it does not
// (cut columns would lose weighted and share, the ones worth reading); a
// record with no compact form keeps the full one rather than showing nothing;
// none says the plan's sentence under "Task <id>"; unreadable shows the
// reader's own sentence, adding the path only when the sentence lacks it.
func TestTaskTokensContent(t *testing.T) {
	rec := tokensFixture("T1.2")
	fullW := widestLine(rec.Lines)
	noCompact := rec
	noCompact.Compact = nil
	cases := []struct {
		name  string
		rec   TaskTokens
		width int
		want  []string
	}{
		{"full when it fits", rec, fullW, rec.Lines},
		{"compact when one cell short", rec, fullW - 1, rec.Compact},
		{"compact at 72", rec, 72, rec.Compact},
		{"full without a compact form", noCompact, 40, rec.Lines},
		{"none", TaskTokens{State: TaskTokensNone, Task: "T1.2"}, 80, []string{"Task T1.2", taskTokensNoneText}},
		{"zero record uses the cursor's id", TaskTokens{}, 80, []string{"Task T9.9", taskTokensNoneText}},
		{"recorded with no lines reads as none", TaskTokens{State: TaskTokensRecorded, Task: "T1.2"}, 80, []string{"Task T1.2", taskTokensNoneText}},
		{"unreadable names its path once", TaskTokens{State: TaskTokensUnreadable, Task: "T1.2", Path: "/r/tokens.jsonl", Err: "reading ledger /r/tokens.jsonl: permission denied"}, 80,
			[]string{"Task T1.2", "", "reading ledger /r/tokens.jsonl: permission denied"}},
		{"unreadable adds a missing path", TaskTokens{State: TaskTokensUnreadable, Task: "T1.2", Path: "/r/tokens.jsonl", Err: "permission denied"}, 80,
			[]string{"Task T1.2", "", "permission denied", "ledger: /r/tokens.jsonl"}},
		{"unreadable with no reason", TaskTokens{State: TaskTokensUnreadable, Task: "T1.2"}, 80,
			[]string{"Task T1.2", "", "The token ledger could not be read."}},
		{"unreadable wraps the reason", TaskTokens{State: TaskTokensUnreadable, Task: "T1.2", Err: "reading ledger /a/long/path: is a directory"}, 20,
			[]string{"Task T1.2", "", "reading ledger", "/a/long/path: is a", "directory"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := taskTokensContent(tc.rec, "T9.9", tc.width)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("content:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestTaskTokensOverlay opens the overlay through the leader on a held
// record and checks what it draws. Load-bearing rows: 120 columns show the
// full table with its cache columns, 80 the compact one (and a resize moves
// between them, chosen again in relayout and never in View), none and
// unreadable say so, and esc closes it and leaves the keyboard on the Build
// tab where T was pressed.
func TestTaskTokensOverlay(t *testing.T) {
	cases := []struct {
		name     string
		rec      TaskTokens
		w, h     int
		want     []string
		wantNot  []string
		resizeTo int
	}{
		{name: "full at 120", rec: tokensFixture("T1.2"), w: 120, h: 40,
			want: []string{"Task T1.2", "cache write", "cache read", "(subtotal)", "total", "2871239", "100.0%"}},
		{name: "compact at 80", rec: tokensFixture("T1.2"), w: 80, h: 24,
			want: []string{"Task T1.2", "weighted", "2871239", "100.0%"}, wantNot: []string{"cache write"}},
		{name: "compact after a resize to 80", rec: tokensFixture("T1.2"), w: 120, h: 40, resizeTo: 80,
			want: []string{"weighted", "2871239"}, wantNot: []string{"cache write"}},
		{name: "none", rec: TaskTokens{State: TaskTokensNone, Task: "T1.2"}, w: 120, h: 40,
			want: []string{"Task T1.2", "no tokens recorded for this task yet"}},
		{name: "unreadable", rec: TaskTokens{State: TaskTokensUnreadable, Task: "T1.2", Path: "/repo/.afk/log/tokens.jsonl",
			Err: "reading ledger /repo/.afk/log/tokens.jsonl: is a directory"}, w: 120, h: 40,
			want: []string{"Task T1.2", "reading ledger /repo/.afk/log/tokens.jsonl: is a directory"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &tokensSeam{rec: func(string) TaskTokens { return tc.rec }}
			m := tokensBuildTab(t, s)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: tc.w, Height: tc.h})
			m = holdTokens(t, m, tc.rec)
			before := len(s.calls)
			m, cmd := pressAction(t, m, keyRune('t'))
			if !m.taskTokensOpen {
				t.Fatal("the overlay did not open")
			}
			if cmd != nil || len(s.calls) != before {
				t.Fatal("opening on a held record read the ledger")
			}
			if got := activeInputMode(m.modeSnapshot()); got != inputTaskTokens {
				t.Fatalf("mode = %v, want %v", got, inputTaskTokens)
			}
			if tc.resizeTo != 0 {
				m, _ = update(t, m, tea.WindowSizeMsg{Width: tc.resizeTo, Height: 24})
			}
			frame := render(m)
			for _, w := range tc.want {
				if !strings.Contains(frame, w) {
					t.Errorf("the frame does not show %q:\n%s", w, frame)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(frame, w) {
					t.Errorf("the frame shows %q:\n%s", w, frame)
				}
			}
			for i, ln := range strings.Split(frame, "\n") {
				if lipgloss.Width(ln) > m.width {
					t.Errorf("line %d is %d wide, terminal %d", i, lipgloss.Width(ln), m.width)
				}
			}
			m, _ = update(t, m, keyEsc)
			if m.taskTokensOpen {
				t.Fatal("esc did not close the overlay")
			}
			if got := activeInputMode(m.modeSnapshot()); got != inputDetail || !m.buildCursorShown() {
				t.Fatalf("after esc mode = %v cursor shown = %v, want the Build tab with the keyboard", got, m.buildCursorShown())
			}
		})
	}
}

// TestTaskTokensStale pins which reads are dropped when they land. Every row
// is load-bearing: a read from another project generation or build root
// would show the old project's cost on the new project's row, and a read for
// a task the cursor has left would put one task's cost on another's row. A
// dropped task read asks again for the cursor's task, so a moved cursor does
// not wait a tick; a dropped project read asks nothing.
func TestTaskTokensStale(t *testing.T) {
	cases := []struct {
		name      string
		msg       func(m Model) taskTokensLoadedMsg
		wantHeld  bool
		wantAgain bool
	}{
		{"fresh is stored", func(m Model) taskTokensLoadedMsg {
			return taskTokensLoadedMsg{gen: m.projectGen, root: m.build.Root, task: m.buildCursorID, rec: tokensFixture(m.buildCursorID)}
		}, true, false},
		{"stale gen is dropped", func(m Model) taskTokensLoadedMsg {
			return taskTokensLoadedMsg{gen: m.projectGen - 1, root: m.build.Root, task: m.buildCursorID, rec: tokensFixture(m.buildCursorID)}
		}, false, false},
		{"stale root is dropped", func(m Model) taskTokensLoadedMsg {
			return taskTokensLoadedMsg{gen: m.projectGen, root: "/elsewhere", task: m.buildCursorID, rec: tokensFixture(m.buildCursorID)}
		}, false, false},
		{"stale task is dropped and read again", func(m Model) taskTokensLoadedMsg {
			return taskTokensLoadedMsg{gen: m.projectGen, root: m.build.Root, task: "T1.1", rec: tokensFixture("T1.1")}
		}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &tokensSeam{rec: tokensFixture}
			m := tokensBuildTab(t, s)
			m, cmd := update(t, m, tc.msg(m))
			if held := m.taskTokensID != ""; held != tc.wantHeld {
				t.Fatalf("held = %v (%q), want %v", held, m.taskTokensID, tc.wantHeld)
			}
			if tc.wantHeld && m.buildCursorTotal() != "2.9M weighted" {
				t.Errorf("total = %q after a fresh read", m.buildCursorTotal())
			}
			if !tc.wantAgain {
				if cmd != nil {
					t.Fatalf("a %s returned a command", tc.name)
				}
				return
			}
			again := runTokensCmd(t, cmd)
			if again.task != m.buildCursorID {
				t.Fatalf("read again for %q, want the cursor's %q", again.task, m.buildCursorID)
			}
		})
	}
	t.Run("a project switch forgets the record", func(t *testing.T) {
		m := holdTokens(t, tokensBuildTab(t, &tokensSeam{rec: tokensFixture}), tokensFixture("T1.2"))
		m.taskTokensPending = "T1.2"
		guard := m.taskTokensLoading
		m.forgetTaskTokens()
		if m.taskTokensID != "" || m.taskTokens.State != TaskTokensNone || m.taskTokensPending != "" {
			t.Fatalf("after forget: id=%q state=%v pending=%q", m.taskTokensID, m.taskTokens.State, m.taskTokensPending)
		}
		if m.taskTokensLoading == guard {
			t.Fatal("the guard was not replaced")
		}
	})
}

// TestTaskTokensBlocked is the row's reasons in S's order, and the action
// asked directly while blocked. Load-bearing: the seam comes first (sending
// the owner to the Build tab would not help without it), and a blocked
// action calls no seam and opens nothing.
func TestTaskTokensBlocked(t *testing.T) {
	ready := leaderConds{leader: defaultLeaderKey, canLoadTaskTokens: true, buildTabFocused: true, buildTaskSelected: true}
	with := func(f func(*leaderConds)) leaderConds { c := ready; f(&c); return c }
	cases := []struct {
		name  string
		conds leaderConds
		want  string
	}{
		{"runs", ready, ""},
		{"no seam outranks everything", leaderConds{}, taskTokensNoSeam},
		{"not on the Build tab", with(func(c *leaderConds) { c.buildTabFocused = false; c.buildTaskSelected = false }), taskStatusOpenBuildFirst(defaultLeaderKey)},
		{"no task under the cursor", with(func(c *leaderConds) { c.buildTaskSelected = false }), taskTokensNoTask},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskTokensBlockedReason(tc.conds); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
			if got := leaderBlockedReason(actionTaskTokens, tc.conds); got != tc.want {
				t.Fatalf("leader reason = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("blocked from the list calls no seam", func(t *testing.T) {
		s := &tokensSeam{rec: tokensFixture}
		m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
		m.deps.LoadTaskTokens = s.load
		got, cmd := m.applyKeyAction(inputNormal, actionTaskTokens, tea.KeyPressMsg{})
		gm := got.(Model)
		if cmd != nil || len(s.calls) != 0 || gm.taskTokensOpen {
			t.Fatalf("blocked action: cmd=%v calls=%v open=%v", cmd != nil, s.calls, gm.taskTokensOpen)
		}
		if !strings.Contains(gm.status, "Build tab") {
			t.Errorf("notice = %q, want it to send the owner to the Build tab", gm.status)
		}
	})
	t.Run("blocked with no seam calls nothing", func(t *testing.T) {
		m := tokensBuildTab(t, nil)
		got, cmd := m.applyKeyAction(inputDetail, actionTaskTokens, tea.KeyPressMsg{})
		gm := got.(Model)
		if cmd != nil || gm.taskTokensOpen {
			t.Fatal("the action ran with no seam")
		}
		if !strings.Contains(gm.status, "token ledger") {
			t.Errorf("notice = %q, want the no-seam reason", gm.status)
		}
	})
}

// TestTaskTokensActionWithoutRecord runs the action before the record is
// held. Load-bearing rows: the seam is called once, inside the command, and
// the overlay opens when that task's record lands; a pending open is dropped
// when the cursor or the keyboard moved on, so an overlay never appears over
// what the owner went to instead.
func TestTaskTokensActionWithoutRecord(t *testing.T) {
	cases := []struct {
		name     string
		between  func(t *testing.T, m Model) Model
		wantOpen bool
	}{
		{"opens on arrival", func(t *testing.T, m Model) Model { return m }, true},
		{"cursor moved meanwhile", func(t *testing.T, m Model) Model { m, _ = update(t, m, keyDown); return m }, false},
		{"keyboard back on the list", func(t *testing.T, m Model) Model {
			m, _ = pressAction(t, m, keyEscMsg())
			return m
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &tokensSeam{rec: tokensFixture}
			m := tokensBuildTab(t, s)
			m, cmd := pressAction(t, m, keyRune('t'))
			if m.taskTokensOpen {
				t.Fatal("opened before the record was read")
			}
			if len(s.calls) != 0 {
				t.Fatal("the seam was called outside the command")
			}
			msg := runTokensCmd(t, cmd)
			if len(s.calls) != 1 || s.calls[0] != "/repo T1.2" {
				t.Fatalf("seam calls = %q, want one for /repo T1.2", s.calls)
			}
			m = tc.between(t, m)
			m, _ = update(t, m, msg)
			if m.taskTokensOpen != tc.wantOpen {
				t.Fatalf("open = %v, want %v", m.taskTokensOpen, tc.wantOpen)
			}
			if m.taskTokensPending != "" {
				t.Errorf("pending open %q left behind", m.taskTokensPending)
			}
		})
	}
}

// TestTaskTokensReads pins when the ledger is read. Load-bearing rows: no
// seam, no build root or no cursor task issue nothing (an unwired dashboard
// stays as it was); the refresh tick and a cursor move onto another task each
// read the cursor's task; and a read already in flight makes the next one a
// no-op rather than a second read of the same file.
func TestTaskTokensReads(t *testing.T) {
	t.Run("nothing without a seam", func(t *testing.T) {
		if cmd := tokensBuildTab(t, nil).taskTokensCmd(); cmd != nil {
			t.Fatal("a command with no seam")
		}
	})
	t.Run("nothing without a build root", func(t *testing.T) {
		m := testModel(sess("alpha"))
		m.deps.LoadTaskTokens = (&tokensSeam{rec: tokensFixture}).load
		m.buildCursorID = "T1.2"
		if cmd := m.taskTokensCmd(); cmd != nil {
			t.Fatal("a command with no build root")
		}
	})
	t.Run("nothing without a cursor task", func(t *testing.T) {
		m := tokensBuildTab(t, &tokensSeam{rec: tokensFixture})
		m.buildCursorID = ""
		if cmd := m.taskTokensCmd(); cmd != nil {
			t.Fatal("a command with no cursor task")
		}
	})
	t.Run("the first build read lands the cursor and reads", func(t *testing.T) {
		s := &tokensSeam{rec: tokensFixture}
		m := testModel(sess("alpha"))
		m.deps.LoadTaskTokens = s.load
		m, cmd := update(t, m, buildLoadedMsg{snap: buildTabSnap(), gen: m.projectGen})
		if msg := runTokensCmd(t, cmd); msg.task != "T1.2" {
			t.Fatalf("read %q, want the landed cursor's T1.2", msg.task)
		}
	})
	t.Run("the refresh tick reads", func(t *testing.T) {
		s := &tokensSeam{rec: tokensFixture}
		m := tokensBuildTab(t, s)
		for _, c := range m.projectCmds() {
			if _, ok := c().(taskTokensLoadedMsg); ok {
				if len(s.calls) != 1 {
					t.Fatalf("seam calls = %q", s.calls)
				}
				return
			}
		}
		t.Fatal("projectCmds has no token read")
	})
	t.Run("a cursor move reads the new task", func(t *testing.T) {
		m := tokensBuildTab(t, &tokensSeam{rec: tokensFixture})
		m, cmd := update(t, m, keyDown)
		if msg := runTokensCmd(t, cmd); msg.task != m.buildCursorID || msg.task == "T1.2" {
			t.Fatalf("read %q, cursor on %q", msg.task, m.buildCursorID)
		}
	})
	t.Run("a move that stays put reads nothing", func(t *testing.T) {
		m := tokensBuildTab(t, &tokensSeam{rec: tokensFixture})
		m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
		m, cmd := update(t, m, keyUp)
		if cmd != nil {
			t.Fatal("up on the first row issued a read")
		}
	})
	t.Run("a read in flight makes the next a no-op", func(t *testing.T) {
		s := &tokensSeam{rec: tokensFixture}
		m := tokensBuildTab(t, s)
		m.taskTokensLoading.Store(true)
		if msg := m.taskTokensCmd()(); msg != nil {
			t.Fatalf("a second read ran: %T", msg)
		}
		if len(s.calls) != 0 {
			t.Fatal("the seam was called under the guard")
		}
	})
}
