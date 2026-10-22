package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
)

// TestBuildCursorMove is the table for moveBuildCursor. The load-bearing rows
// are the two clamps: "down past the last row" and "up past the first" must
// stop on the end and not wrap, so a held-down arrow (or a page key on a short
// list) never jumps to the other end of the plan. "empty list" pins that a
// list with no rows answers 0 rather than -1, which would index out of range
// in the caller.
func TestBuildCursorMove(t *testing.T) {
	cases := []struct {
		name            string
		index, delta, n int
		want            int
	}{
		{name: "down one", index: 1, delta: 1, n: 5, want: 2},
		{name: "up one", index: 1, delta: -1, n: 5, want: 0},
		{name: "down past the last row", index: 4, delta: 1, n: 5, want: 4},
		{name: "up past the first", index: 0, delta: -1, n: 5, want: 0},
		{name: "a page down on a short list", index: 1, delta: 20, n: 5, want: 4},
		{name: "a page up on a short list", index: 3, delta: -20, n: 5, want: 0},
		{name: "one row", index: 0, delta: 1, n: 1, want: 0},
		{name: "empty list", index: 0, delta: 1, n: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := moveBuildCursor(tc.index, tc.delta, tc.n); got != tc.want {
				t.Fatalf("moveBuildCursor(%d, %d, %d) = %d, want %d", tc.index, tc.delta, tc.n, got, tc.want)
			}
		})
	}
}

// TestBuildCursorLanding is the table for landBuildCursor, where the cursor
// goes when a new build read arrives. The load-bearing rows:
//
//   - "same id kept after reorder" is the reason the cursor is kept by id: a
//     later action acts on the cursor's task, and a cursor that stayed on the
//     same row number would now point at a different task.
//   - "id gone" and "id gone at the end" are the landing when the task left
//     the plan: the same row number, clamped to the last row, never -1 and
//     never a panic.
//   - "empty list" must return no id, so nothing can later act on a task that
//     is not on screen.
//   - "no previous id" and "current not in rows" are the first read's start.
func TestBuildCursorLanding(t *testing.T) {
	rows := func(ids ...string) []buildview.Row {
		out := make([]buildview.Row, len(ids))
		for i, id := range ids {
			out[i] = buildview.Row{ID: id, Title: "task " + id, Status: "pending"}
		}
		return out
	}
	cases := []struct {
		name      string
		prevID    string
		prevIndex int
		current   string
		rows      []buildview.Row
		wantID    string
		wantIndex int
	}{
		{name: "same id kept", prevID: "T2", prevIndex: 1, rows: rows("T1", "T2", "T3"), wantID: "T2", wantIndex: 1},
		{name: "same id kept after reorder", prevID: "T2", prevIndex: 1, rows: rows("T3", "T1", "T4", "T2"), wantID: "T2", wantIndex: 3},
		{name: "id gone", prevID: "T2", prevIndex: 1, rows: rows("T1", "T3", "T4"), wantID: "T3", wantIndex: 1},
		{name: "id gone at the end", prevID: "T4", prevIndex: 3, rows: rows("T1", "T2"), wantID: "T2", wantIndex: 1},
		{name: "empty list", prevID: "T2", prevIndex: 1, rows: nil, wantID: "", wantIndex: 0},
		{name: "empty list with no previous id", rows: nil, current: "T1", wantID: "", wantIndex: 0},
		{name: "no previous id", current: "T3", rows: rows("T1", "T2", "T3"), wantID: "T3", wantIndex: 2},
		{name: "current not in rows", current: "T9", rows: rows("T1", "T2"), wantID: "T1", wantIndex: 0},
		{name: "no previous id and no current", rows: rows("T1", "T2"), wantID: "T1", wantIndex: 0},
		{name: "previous id wins over current", prevID: "T1", current: "T2", rows: rows("T1", "T2"), wantID: "T1", wantIndex: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, index := landBuildCursor(tc.prevID, tc.prevIndex, tc.current, tc.rows)
			t.Logf("landed on id=%q index=%d", id, index)
			if id != tc.wantID || index != tc.wantIndex {
				t.Fatalf("landBuildCursor = (%q, %d), want (%q, %d)", id, index, tc.wantID, tc.wantIndex)
			}
		})
	}
}

// buildCursorModel is a dashboard of the given size with the build read in
// and `2` pressed, so the project view's Build section has the keyboard.
func buildCursorModel(t *testing.T, w, h int, snap buildview.Snapshot) Model {
	t.Helper()
	m := testModel(sess("alpha"))
	m.width, m.height = w, h
	m.sizeSessionList()
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m = withBuild(t, m, snap)
	m = openBuildSection(t, m)
	if !m.buildCursorOnTask() {
		t.Fatalf("setup: the Build section's cursor is not on a task (section=%v focus=%v)", m.focusedSection(), m.focus)
	}
	m.clearStatus()
	return m
}

// longBuild is a plan with more tasks than a short window shows.
func longBuild(n int) buildview.Snapshot {
	snap := buildview.Snapshot{Root: "/repo", HasBuild: true, Total: n}
	for i := range n {
		snap.Rows = append(snap.Rows, buildview.Row{ID: fmt.Sprintf("T%d.1", i+1), Title: "task", Status: "pending"})
	}
	return snap
}

// cursorLine is the plain text of the frame from the Build cursor's marker to
// the end of its line, or "" when no line has the marker.
func cursorLine(m Model) string {
	for _, ln := range strings.Split(render(m), "\n") {
		if i := strings.Index(ln, buildCursorMarker+" "); i >= 0 {
			return ln[i:]
		}
	}
	return ""
}

// TestBuildCursorKeys drives the move keys in the Build section. The
// load-bearing rows are "starts on the current task" (the first read lands
// there, not on the first row) and the Sessions row: the same keys with
// Sessions focused must move the session list and leave the task cursor
// where it is.
func TestBuildCursorKeys(t *testing.T) {
	cases := []struct {
		name   string
		keys   []tea.Msg
		wantID string
	}{
		{name: "starts on the current task", wantID: "T1.2"},
		{name: "down", keys: []tea.Msg{keyDown}, wantID: "T2.1"},
		{name: "j", keys: []tea.Msg{keyRune('j')}, wantID: "T2.1"},
		{name: "up", keys: []tea.Msg{keyUp}, wantID: "T1.1"},
		{name: "k past the first row stays", keys: []tea.Msg{keyRune('k'), keyRune('k')}, wantID: "T1.1"},
		{name: "end", keys: []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnd}}, wantID: "T5.1"},
		{name: "end then home", keys: []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnd}, tea.KeyPressMsg{Code: tea.KeyHome}}, wantID: "T1.1"},
		{name: "page down clamps to the last", keys: []tea.Msg{tea.KeyPressMsg{Code: tea.KeyPgDown}}, wantID: "T5.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildCursorModel(t, 120, 40, buildTabSnap())
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			if m.buildCursorID != tc.wantID || m.build.Rows[m.buildCursorIndex].ID != tc.wantID {
				t.Fatalf("cursor on %q (row %d), want %q", m.buildCursorID, m.buildCursorIndex, tc.wantID)
			}
			if got := cursorLine(m); !strings.HasPrefix(got, buildCursorMarker+" "+tc.wantID+" ") {
				t.Errorf("the marker is on %q, want the %s row", got, tc.wantID)
			}
		})
	}

	t.Run("with Sessions focused the keys move the list", func(t *testing.T) {
		m := withBuild(t, testModel(sess("alpha"), sess("beta")), buildTabSnap())
		before := m.buildCursorID
		m, _ = update(t, m, keyDown)
		if s, _ := m.selected(); s.Name != "beta" {
			t.Errorf("down in Sessions selected %q, want beta", s.Name)
		}
		if m.buildCursorID != before {
			t.Errorf("down in Sessions moved the Build cursor to %q", m.buildCursorID)
		}
	})
}

// TestBuildCursorFollowsItsTask pins that the cursor stays on its task across
// a build read that reorders the rows, and lands on the same row number when
// its task leaves the plan. A cursor that stayed on the row number would
// silently point at another task.
func TestBuildCursorFollowsItsTask(t *testing.T) {
	m := buildCursorModel(t, 120, 40, buildTabSnap())
	m, _ = update(t, m, keyDown) // T2.1, row 2
	if m.buildCursorID != "T2.1" {
		t.Fatalf("setup: cursor on %q, want T2.1", m.buildCursorID)
	}
	next := buildTabSnap()
	next.Rows[0], next.Rows[2] = next.Rows[2], next.Rows[0]
	m = withBuild(t, m, next)
	if m.buildCursorID != "T2.1" || m.buildCursorIndex != 0 {
		t.Fatalf("after a reorder the cursor is on %q at row %d, want T2.1 at row 0", m.buildCursorID, m.buildCursorIndex)
	}
	if got := cursorLine(m); !strings.HasPrefix(got, buildCursorMarker+" T2.1 ") {
		t.Errorf("the marker is on %q, want T2.1", got)
	}

	gone := buildTabSnap()
	gone.Rows = append(gone.Rows[:2:2], gone.Rows[3:]...) // T2.1 removed
	m = withBuild(t, m, gone)
	if m.buildCursorID != gone.Rows[0].ID || m.buildCursorIndex != 0 {
		t.Errorf("with its task gone the cursor is on %q at row %d, want %q at row 0", m.buildCursorID, m.buildCursorIndex, gone.Rows[0].ID)
	}
}

// TestBuildCursorScrolledIntoView pins that in a section too short for the
// plan the cursor's row is always on screen: after end, after page keys, and
// after home, which must also bring the summary back. The section scrolls
// round the cursor's line, counted from buildRowsTop; the check here reads
// the frame, so a miscount there shows up as a missing marker.
func TestBuildCursorScrolledIntoView(t *testing.T) {
	snap := longBuild(40)
	m := buildCursorModel(t, 100, 20, snap)
	steps := []struct {
		name   string
		key    tea.Msg
		wantID string
	}{
		{name: "end", key: tea.KeyPressMsg{Code: tea.KeyEnd}, wantID: "T40.1"},
		{name: "page up", key: tea.KeyPressMsg{Code: tea.KeyPgUp}},
		{name: "up", key: keyUp},
		{name: "home", key: tea.KeyPressMsg{Code: tea.KeyHome}, wantID: "T1.1"},
		{name: "page down", key: tea.KeyPressMsg{Code: tea.KeyPgDown}},
		{name: "down", key: keyDown},
	}
	for _, st := range steps {
		m, _ = update(t, m, st.key)
		m.clearStatus()
		id := m.buildCursorID
		if st.wantID != "" && id != st.wantID {
			t.Fatalf("%s: cursor on %q, want %q", st.name, id, st.wantID)
		}
		if frame := render(m); !strings.Contains(frame, buildCursorMarker+" "+id+" ") {
			t.Fatalf("%s: the cursor's row %s is not on screen:\n%s", st.name, id, frame)
		}
	}
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	m.clearStatus()
	if !strings.Contains(render(m), "0/40 done") {
		t.Errorf("home did not bring the summary back on screen:\n%s", render(m))
	}
}

// TestBuildCursorHiddenOutsideBuild pins that the cursor is drawn only while
// the Build section has the keyboard: with Sessions focused no row carries the
// marker or the selection, the rows keep their gutter so the columns do not
// move, and the move keys there are the session list's.
func TestBuildCursorHiddenOutsideBuild(t *testing.T) {
	m := buildCursorModel(t, 120, 40, buildTabSnap())
	m, _ = update(t, m, keyRune('1'))
	if m.focusedSection() != sectionSessions || m.buildCursorOnTask() {
		t.Fatalf("setup: section=%v cursor on a task=%v, want Sessions and no cursor", m.focusedSection(), m.buildCursorOnTask())
	}
	frame := render(m)
	if strings.Contains(frame, buildCursorMarker+" ") {
		t.Errorf("the marker is drawn with Sessions focused:\n%s", frame)
	}
	if !strings.Contains(frame, "  T1.1  done") {
		t.Errorf("the first row lost its gutter:\n%s", frame)
	}
}

// TestBuildCursorRowsKeepTheirColumns pins the gutter's layout at every width
// the id and status must survive: every line fits, every row is shifted by the
// same two cells, exactly one row carries the marker, and the id and status
// show on every row.
func TestBuildCursorRowsKeepTheirColumns(t *testing.T) {
	snap := buildTabSnap()
	for _, width := range []int{120, 80, 50, 30, 20} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			lines := buildSectionLines(defaultLeaderKey, snap, PlanStatus{}, width, buildMarks{cursor: 3, linked: -1})
			top := buildRowsTop(defaultLeaderKey, snap, PlanStatus{}, width)
			text := plain(lines[top:])
			if len(text) != len(snap.Rows) {
				t.Fatalf("%d row lines, want %d", len(text), len(snap.Rows))
			}
			marked := 0
			for i, ln := range text {
				if w := xansi.StringWidth(lines[top+i]); w > width {
					t.Errorf("row %d is %d cells, over %d", i, w, width)
				}
				r := snap.Rows[i]
				gutter := "  "
				if i == 3 {
					gutter = buildCursorMarker + " "
					marked++
				}
				if !strings.HasPrefix(ln, gutter+r.ID+" ") {
					t.Errorf("row %d is %q, want it to start with %q", i, ln, gutter+r.ID)
				}
				if !strings.Contains(ln, r.Status) {
					t.Errorf("row %d lost its status: %q", i, ln)
				}
			}
			if marked != 1 {
				t.Errorf("%d rows marked, want 1", marked)
			}
		})
	}
}
