package ui

import (
	"github.com/dphaener/afk/internal/buildview"
)

// This file is the Build tab's cursor: which task row the owner is on while
// the right side has the keyboard. The tab was a read-only list that the
// scroll keys moved as a whole; a row the owner can point at is what lets a
// later action (setting a task's status) say which task it means.
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

// buildCursorShown reports whether the Build tab draws its cursor: the Build
// tab is the active tab, the detail box is what the main region shows, the
// right side has the keyboard, and there is at least one task row. It is the
// one gate for the gutter, for the scroll keys moving the cursor instead of
// the view, and for keeping the cursor's row on screen, so the three cannot
// disagree: a cursor the keys move but the frame does not draw, or the other
// way round, would be worse than none.
//
// It is false while the session list has the keyboard, which is what keeps
// the tab exactly as it was drawn before the cursor existed in that state.
func (m Model) buildCursorShown() bool {
	return m.focus == focusRight && m.detailDrawn() && m.activeDetailTab() == tabBuild &&
		m.build.Problem == "" && len(m.build.Rows) > 0
}

// moveBuildCursorFor is the detail side's scroll keys on the Build tab: the
// same actions that scroll every other tab move the cursor here, by one row,
// half a page, a page, or to the first or last task. A page is the rows the
// detail box shows, which syncDetailViewport has just set as the viewport's
// height. The viewport is then brought round the cursor's row by
// syncDetailViewport (revealBuildCursor), not scrolled here, so there is one
// place that decides the offset while the cursor is shown.
func (m *Model) moveBuildCursorFor(a modeAction) {
	n := len(m.build.Rows)
	page := max(m.detailViewport.Height(), 1)
	half := max(page/2, 1)
	i := m.buildCursorIndex
	switch a {
	case actionDetailScrollUp:
		i = moveBuildCursor(i, -1, n)
	case actionDetailScrollDown:
		i = moveBuildCursor(i, 1, n)
	case actionDetailHalfPageUp:
		i = moveBuildCursor(i, -half, n)
	case actionDetailHalfPageDown:
		i = moveBuildCursor(i, half, n)
	case actionDetailPageUp:
		i = moveBuildCursor(i, -page, n)
	case actionDetailPageDown:
		i = moveBuildCursor(i, page, n)
	case actionDetailGotoTop:
		i = 0
		// The first task is not the top of the tab: the summary sits above
		// it, and home should show it as it does on every other tab.
		m.detailViewport.GotoTop()
	case actionDetailGotoBottom:
		i = n - 1
	}
	m.buildCursorIndex = i
	m.buildCursorID = m.build.Rows[i].ID
	m.syncDetailViewport()
}

// revealBuildCursor scrolls the detail viewport the least it has to so the
// cursor's row is on screen. The row's line is counted from the same pieces
// composeDetailBody and buildTabLines put above it (detailStripLines, then
// buildRowsTop), never guessed, so a summary that grows a line (the build
// paused, an inbox item) cannot leave the cursor one row off screen.
//
// It runs from syncDetailViewport, on Update's goroutine, after the content
// and height are set.
func (m *Model) revealBuildCursor(contentW int) {
	if !m.buildCursorShown() {
		return
	}
	line := detailStripLines + buildRowsTop(m.build, m.planStatus, contentW) + m.buildCursorIndex
	top := m.detailViewport.YOffset()
	h := max(m.detailViewport.Height(), 1)
	switch {
	case line < top:
		m.detailViewport.SetYOffset(line)
	case line >= top+h:
		m.detailViewport.SetYOffset(line - h + 1)
	}
}
