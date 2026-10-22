package ui

import (
	"github.com/dphaener/afk/internal/buildview"
)

// This file is the Build section's cursor: which task row the owner is on
// while the project view's Build section has the keyboard (T16.4; it was the
// overview's Build tab before). A row the owner can point at is what lets an
// action (setting a task's status, its details) say which task it means.
//
// The cursor is remembered by task id and not only by row number. The build
// snapshot is read again every two seconds and its rows can change order or
// lose a task between two reads, and a cursor kept as a bare index would then
// sit on a different task than the one the owner chose, with nothing on
// screen to say so. The index is kept beside the id only to land somewhere
// sensible when the id is gone.

// moveBuildCursor moves the cursor by delta rows in a list of n, clamped at
// both ends rather than wrapping, like moveAllProjects: a held-down arrow stops
// on the last task instead of jumping back to the first. An empty list has no
// row to be on, so the answer is 0.
func moveBuildCursor(index, delta, n int) int {
	if n <= 0 {
		return 0
	}
	return clampInt(index+delta, 0, n-1)
}

// landBuildCursor is where the cursor goes when a new snapshot arrives.
//
// With a previous id still in the rows it stays on that task, wherever the
// task has moved to. With the id gone (a task removed from the plan) it lands
// on the row now at the previous index, clamped to the last row, so the
// cursor stays where the owner's eye already is rather than jumping to the
// top. With no previous id (the first read, or after a project switch) it
// starts on the build's current task, the one most likely to be looked at,
// and on the first row when there is no current task or it is not listed.
// An empty list returns no id and index 0.
//
// current is buildview.Snapshot.Current, passed in so this stays a pure
// function of its arguments.
func landBuildCursor(prevID string, prevIndex int, current string, rows []buildview.Row) (id string, index int) {
	if len(rows) == 0 {
		return "", 0
	}
	if prevID == "" {
		if i := buildRowIndex(rows, current); current != "" && i >= 0 {
			return rows[i].ID, i
		}
		return rows[0].ID, 0
	}
	if i := buildRowIndex(rows, prevID); i >= 0 {
		return prevID, i
	}
	i := clampInt(prevIndex, 0, len(rows)-1)
	return rows[i].ID, i
}

// buildRowIndex is the index of the row with the given id, or -1.
func buildRowIndex(rows []buildview.Row, id string) int {
	for i, r := range rows {
		if r.ID == id {
			return i
		}
	}
	return -1
}

// buildSectionFocused reports whether the project view's Build section has
// the keyboard: a project is open, the dashboard is at the Project level with
// the keyboard on the project view (not on the overview `d` opened, which
// is the right side's), and the focused section is Build. It is the one
// answer `s` and `t` are refused by (leaderConditions' buildTabFocused) and
// the one a pending tokens read opens its overlay under, so the two cannot
// disagree about where the owner is.
func (m Model) buildSectionFocused() bool {
	return m.hasActiveProject() && m.level() == levelProject && m.focus == focusList &&
		m.focusedSection() == sectionBuild
}

// buildCursorOnTask reports whether the Build section's cursor is on a task
// the owner can act on: the section is focused and the snapshot has rows to
// be on. It replaced the Build tab's "cursor shown" gate; the section draws
// the cursor's row selected only while this holds (projectviewrender.go), so
// a cursor the keys act on is always the one on screen.
func (m Model) buildCursorOnTask() bool {
	return m.buildSectionFocused() && m.build.Problem == "" && len(m.build.Rows) > 0
}

// moveBuildCursorFor is the move keys in the Build section: one row, a page
// (the rows the section shows, buildSectionPage) or to the first or last
// task. The section scrolls itself round the cursor when it is drawn
// (buildSectionWindow), so nothing here keeps an offset.
func (m *Model) moveBuildCursorFor(a modeAction) {
	n := len(m.build.Rows)
	if n == 0 {
		return
	}
	page := max(m.buildSectionPage(), 1)
	i := m.buildCursorIndex
	switch a {
	case actionCursorUp:
		i = moveBuildCursor(i, -1, n)
	case actionCursorDown:
		i = moveBuildCursor(i, 1, n)
	case actionPageUp:
		i = moveBuildCursor(i, -page, n)
	case actionPageDown:
		i = moveBuildCursor(i, page, n)
	case actionListTop:
		i = 0
	case actionListBottom:
		i = n - 1
	}
	m.buildCursorIndex = i
	m.buildCursorID = m.build.Rows[i].ID
}
