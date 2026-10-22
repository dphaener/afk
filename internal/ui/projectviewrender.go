// projectviewrender.go draws the Project level as the project view of
// docs/dash-design.md §4.2 (T16.4), and holds the Model glue its keys run
// through: which section has the keyboard, what the move keys move there,
// and what enter opens. The decisions themselves are projectview.go's pure
// functions; this file only reads the Model and applies them.
//
// One pane, the project's, holds top to bottom: the band (projectBandText),
// two columns and the key chips on its last row. The left column, 50 cells,
// is the `1 Sessions` section with the read-only pane for the selected
// session under it; the right is `2 Build` over `3 Inbox`. The section with
// the keyboard has the focused strip (§3.4), and its selection is drawn
// filled; the selected session's task is drawn in Build as a linked row
// ending "◂ name" (§3.5).
//
// Drawing reads only what the Model already holds — the sessions, the build
// snapshot and the plan status the refresh tick keeps — so it reads no file
// and calls no seam. Every list scrolls itself round its cursor at draw time,
// so no offset is stored to fall out of step with a refresh.
//
// From projectWideWidth columns it is §3.8's overlay: a box the screen less
// projectOverlayMargin columns each side, over the dimmed home screen (or the
// plain ground when the home screen has no rows to show). Below that one
// section fills the box, the whole body wide, and its strip names all three
// (sectionTabs); the read-only pane is left out there, since `d` shows the
// overview, which carries the same facts. The overview `d` opens takes the
// body as it did before T16.4 at every width, until T16.5 makes it an overlay.
//
// The mouse reads the same geometry (projectBoxRect, projectLayout,
// projectHitAt), and each section's lines and scroll come from one function
// per section (sessionsSectionView, buildSectionView, inboxSectionView) that
// both the drawing and a click read, so a click lands on the row drawn under
// the pointer.
package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/inbox"
)

// projectWideWidth is the terminal width from which the project view is drawn
// with its sections side by side (§4.2). It is the home screen's breakpoint
// (allProjectsWideWidth), §2's one line between wide and narrow.
const projectWideWidth = allProjectsWideWidth

// projectOverlayMargin is the columns of the screen under it the project
// view's box leaves visible on each side from projectWideWidth columns (§3.8:
// "the project view is the screen less 2 columns each side").
const projectOverlayMargin = 2

// projectSectionNames are the sections' names in number order, the narrow
// strip's three names (sectionTabs).
var projectSectionNames = []string{"Sessions", "Build", "Inbox"}

// projectSessionsWidth is the left column's width, the Sessions section and
// the read-only pane (§4.2: "Left, 50 columns").
const projectSessionsWidth = 50

// projectRightMin is the fewest cells the right column keeps. A body too
// narrow for both at full width splits them evenly instead.
const projectRightMin = 30

// projectInboxMinH is the Inbox section's least height: its border, its strip
// and one row, so "Nothing open." still has a line.
const projectInboxMinH = 4

// projectSectionKeysLabel is how the chips and the status bar name the
// section keys: tab, and the three digits.
const projectSectionKeysLabel = "tab/1-3"

// sectionStripChrome is the rows a section spends outside its body: the
// border's two and the strip's one.
const sectionStripChrome = boxBorderCells + paneStripRows

// readOnlyChrome is the rows the read-only pane spends outside its body: the
// border's two, the strip and the rule under it.
const readOnlyChrome = boxBorderCells + 2

// focusedSection is the section with the keyboard at Project; the zero value
// a bare Model holds reads as Sessions.
func (m Model) focusedSection() projectSection {
	if m.section < sectionSessions || m.section > sectionInbox {
		return sectionSessions
	}
	return m.section
}

// projectViewDrawn reports whether the body is the project view: the
// keyboard is not on the overview `d` opened, which still draws the session
// list beside it until T16.5 makes `d` an overlay. The Agent is drawn over
// the project view, so it counts as drawn there. Every width draws it: below
// projectWideWidth it is the narrow layout (projectNarrow).
func (m Model) projectViewDrawn() bool {
	return !(m.focus == focusRight && m.level() == levelProject)
}

// projectNarrow reports whether the project view is the narrow layout, one
// section at a time (§4.2 "Below 100 columns"). It is the terminal's width,
// not the box's, that decides: §2's one line between wide and narrow.
func (m Model) projectNarrow() bool { return m.width < projectWideWidth }

// projectBoxRect is the project view's box on screen: x and y its top-left
// cell, w and h its outer size. From projectWideWidth columns it is §3.8's
// overlay, the screen less projectOverlayMargin columns each side; below it
// the box takes the full width, as the agent's does (§2: "Below 100 columns
// the agent and the details overlay take the full width"), because there the
// two columns each side are worth more to the one section than the glimpse
// of the home screen they would show. Vertically it covers the body exactly,
// so the top bar and the status bar stay readable. headerRows and bodyHeight
// are layoutDims' rows. It is the ONE place the box's geometry is decided:
// View draws the box there and the mouse measures clicks from it.
func projectBoxRect(width, headerRows, bodyHeight int) (x, y, w, h int) {
	if width < projectWideWidth {
		return 0, headerRows, max(width, 1), bodyHeight
	}
	return projectOverlayMargin, headerRows, max(width-2*projectOverlayMargin, 1), bodyHeight
}

// changeSection is tab, shift+tab and 1–3 at Project: the focus moves by
// nextSection. Nothing else changes, so the cursors stay where they were in
// every section and going back to one finds its row.
func (m Model) changeSection(a modeAction) (tea.Model, tea.Cmd) {
	m.section = nextSection(m.focusedSection(), sectionKeyFor(a))
	return m, nil
}

// isListMove reports whether a is one of the session list's move actions, the
// ones a move key at Project resolves to (inputNormal's rules).
func isListMove(a modeAction) bool {
	switch a {
	case actionCursorUp, actionCursorDown, actionPageUp, actionPageDown, actionListTop, actionListBottom:
		return true
	}
	return false
}

// moveInSection is a move key in the Build or the Inbox section. The Build
// cursor landing on another task reads that task's tokens at once, as a move
// on the Build tab did, so `t` and the row's total have them.
func (m Model) moveInSection(a modeAction) (tea.Model, tea.Cmd) {
	switch m.focusedSection() {
	case sectionBuild:
		prev := m.buildCursorID
		m.moveBuildCursorFor(a)
		if m.buildCursorID != prev {
			return m, m.taskTokensCmd()
		}
	case sectionInbox:
		items := m.inboxSectionItems()
		n := len(items)
		if n == 0 {
			return m, nil
		}
		page := max(m.projectLayout().inboxRows, 1)
		i := m.inboxCursorIndex
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
		m.inboxCursorIndex, m.inboxCursorID = i, items[i].ID
	}
	return m, nil
}

// inboxSectionItems is the Inbox section's rows: the open items of the build
// snapshot, none before the first read.
func (m Model) inboxSectionItems() []inbox.Item {
	if !m.buildKnown {
		return nil
	}
	return openInboxItems(m.build.Items)
}

// selectedInboxItem is the item under the Inbox section's cursor, checked by
// id as the Build cursor is, so enter never opens an item the cursor is not
// on.
func (m Model) selectedInboxItem() (inbox.Item, bool) {
	items := m.inboxSectionItems()
	i := m.inboxCursorIndex
	if i < 0 || i >= len(items) || items[i].ID != m.inboxCursorID {
		return inbox.Item{}, false
	}
	return items[i], true
}

// buildTaskSelected reports whether the Build section's cursor names a task:
// the section has the keyboard and the cursor's id and index agree.
func (m Model) buildTaskSelected() bool {
	i := m.buildCursorIndex
	return m.buildCursorOnTask() && m.buildCursorID != "" && i < len(m.build.Rows) &&
		m.build.Rows[i].ID == m.buildCursorID
}

// projectEnterTarget is what enter opens at Project now (projectEnterTarget's
// pure answer over the Model's selections). The status bar asks it too, so
// it never offers enter where enter would do nothing.
func (m Model) projectEnterTarget() enterTarget {
	_, haveSession := m.selected()
	_, haveItem := m.selectedInboxItem()
	return projectEnterTarget(m.focusedSection(), haveSession, m.buildTaskSelected(), haveItem)
}

// projectGoIn is enter at Project: a session opens its agent (openAgent), a
// task its details (openTaskDetails), an inbox item the inbox on that item
// (openInboxOn). An empty section opens nothing and says nothing: the
// section's own line ("Nothing open.") already says why.
func (m Model) projectGoIn() (tea.Model, tea.Cmd) {
	switch m.projectEnterTarget() {
	case enterAgent:
		return m.openAgent()
	case enterTask:
		return m.openTaskDetails()
	case enterItem:
		it, _ := m.selectedInboxItem()
		return m.openInboxOn(it.ID), nil
	}
	return m, nil
}

// openTaskDetails is enter on a Build row: the task's details, which until
// T16.5 are the tokens overlay with the task's status, title and detail at
// its head (tasktokensoverlay.go). With the token reader wired it is `t`
// exactly, read and all. Without it the details still open, with the head
// and "no tokens recorded": the status, title and detail are already held,
// and a missing token reader is no reason to hide them.
func (m Model) openTaskDetails() (tea.Model, tea.Cmd) {
	if m.deps.LoadTaskTokens != nil {
		return m.openTaskTokens()
	}
	row := m.build.Rows[m.buildCursorIndex]
	m.taskTokensOpen = true
	m.taskTokensView = newTaskTokensOverlay(TaskTokens{}, row.ID, row)
	m.sizeTaskTokens()
	m.clearStatus()
	return m, nil
}

// projectLayout is the project view's geometry for the current terminal: the
// pane, the two columns and each section's size. Drawing and the page keys
// both read it, so a page moves by the rows the section shows.
type projectLayout struct {
	// narrow is one section at a time (projectNarrow): every section then
	// takes the whole column area and the read-only pane is left out.
	narrow bool
	// paneW and paneH are the pane's outer size; contentW the width its body
	// is laid out at.
	paneW, paneH, contentW int
	// colH is the columns' height, between the band and the chips.
	colH int
	// leftW and rightW are the two columns' widths.
	leftW, rightW int
	// sessionsH and summaryH are the left column's two heights; summaryH is
	// 0 when no session is selected.
	sessionsH, summaryH int
	// buildH and inboxH are the right column's two heights.
	buildH, inboxH int
	// buildRows and inboxRows are the rows the two right sections show, the
	// page their move keys move by.
	buildRows, inboxRows int
}

// projectLayoutFor is projectLayout as a pure function of the box's size,
// the read-only pane's rows once wrapped (0 with no session selected), the
// Sessions section's rows (its headings and sessions), the number of open
// items and whether the layout is the narrow one.
//
// The read-only pane is given every row it asks for when the Sessions
// section's rows still fit beside it, because the question in it is to be
// read in full (§4.2). When they do not both fit, the sessions keep at least
// half the column, since the list is what the keys move through, and the pane
// is cut at the bottom.
//
// Narrow, the one section shown fills the column area, whichever it is, so
// every section's height (and so the page its move keys move by) is that
// area's, and there is no read-only pane to size.
func projectLayoutFor(width, height, summaryLines, sessionLines, openItems int, narrow bool) projectLayout {
	l := projectLayout{narrow: narrow, paneW: max(width, 1), paneH: max(height, 1)}
	l.contentW = max(l.paneW-paneOuterWidth(0), 1)
	// The band above the columns and the chips below them.
	l.colH = max(l.paneH-boxBorderCells-paneStripRows-2, 0)
	if narrow {
		l.leftW, l.rightW = l.contentW, l.contentW
		l.sessionsH, l.buildH, l.inboxH = l.colH, l.colH, l.colH
		l.buildRows = max(l.colH-sectionStripChrome, 0)
		l.inboxRows = l.buildRows
		return l
	}
	l.leftW = projectSessionsWidth
	if l.contentW < projectSessionsWidth+1+projectRightMin {
		l.leftW = l.contentW / 2
	}
	l.rightW = max(l.contentW-l.leftW-1, 1)
	if summaryLines > 0 {
		want := summaryLines + readOnlyChrome
		sessions := sessionLines + sectionStripChrome
		l.summaryH = want
		if want+sessions > l.colH {
			l.summaryH = min(want, max(l.colH-sessions, l.colH/2))
		}
		if l.summaryH <= readOnlyChrome {
			l.summaryH = 0
		}
	}
	l.sessionsH = l.colH - l.summaryH
	l.inboxH = min(max(openItems, 1)+sectionStripChrome, l.colH/3)
	l.inboxH = max(l.inboxH, min(projectInboxMinH, l.colH/2))
	l.buildH = l.colH - l.inboxH
	l.buildRows = max(l.buildH-sectionStripChrome, 0)
	l.inboxRows = max(l.inboxH-sectionStripChrome, 0)
	return l
}

// projectLayout is projectLayoutFor over the Model's frame: the box
// projectBoxRect places.
func (m Model) projectLayout() projectLayout {
	_, _, bodyHeight := m.layoutDims()
	_, _, w, h := projectBoxRect(m.width, 0, bodyHeight)
	return m.projectLayoutWith(w, h, m.projectSummary(), len(m.inboxSectionItems()))
}

// projectSummary is the read-only pane's lines for the selected session, nil
// with none selected or in the narrow layout, which has no read-only pane.
func (m Model) projectSummary() []summaryLine {
	if m.projectNarrow() {
		return nil
	}
	if s, ok := m.selected(); ok {
		return m.sessionSummary(s)
	}
	return nil
}

// projectLayoutWith is projectLayout for a body width×height with the
// selected session's summary lines already worked out, so the frame and the
// page keys size the read-only pane from the same wrapped rows.
func (m Model) projectLayoutWith(width, height int, summary []summaryLine, openItems int) projectLayout {
	summaryRows := 0
	if len(summary) > 0 {
		// The pane's body is laid out inside its border and padding, the
		// left column less four cells; the column's width does not depend on
		// the rows, so it is worked out first.
		leftW := projectLayoutFor(width, height, 0, 0, 0, false).leftW
		summaryRows = lipgloss.Height(summaryBody(summary, max(leftW-4, 1)))
	}
	sessionRows := len(m.sessions) + len(groupSessions(m.sessions))
	return projectLayoutFor(width, height, summaryRows, sessionRows, openItems, m.projectNarrow())
}

// buildSectionPage is how many task rows a page of the Build section is: the
// rows it shows less its summary. The section scrolls by whole lines, so a
// page a little short of the screen keeps the row the owner was on in sight.
func (m Model) buildSectionPage() int {
	l := m.projectLayout()
	top := buildRowsTop(m.leaderKey.name, m.build, m.planStatus, max(l.rightW-4, 1))
	return max(l.buildRows-top, 1)
}

// linkedTask is the build row that belongs to session s, from the snapshot
// held (linkedTaskFor), or ok false.
func (m Model) linkedTask(s Session) (int, bool) {
	if !m.buildKnown {
		return 0, false
	}
	return linkedTaskFor(s.Name, m.build.Rows)
}

// sessionSummary is the read-only pane's lines for s.
func (m Model) sessionSummary(s Session) []summaryLine {
	var linked *buildview.Row
	if i, ok := m.linkedTask(s); ok {
		linked = &m.build.Rows[i]
	}
	var items []inbox.Item
	if m.buildKnown {
		items = m.build.Items
	}
	return sessionSummaryLines(s, linked, items)
}

// projectStateWord is the band's first part: the project's state as the home
// screen words it, from the last All projects read, "" when that read has no
// row for this project.
func (m Model) projectStateWord() string {
	root := m.activeProjectPath()
	for _, r := range m.allProjectsRows {
		if r.Root == root && root != "" {
			return r.Label
		}
	}
	return ""
}

// sectionHasKeyboard reports whether sec is drawn as the section with the
// keyboard: it is the focused section, and the project view has the keyboard.
// Selections are drawn filled only there (§3.3: one pane has the keyboard at
// a time). Under the Agent the frame is the Project behind the agent box, and
// no row on it is being acted on, so no section has it.
func (m Model) sectionHasKeyboard(sec projectSection) bool {
	return m.level() == levelProject && m.focus == focusList && m.focusedSection() == sec
}

// renderProjectView draws the project view at width×height, the box's size
// (projectBoxRect). Wide, it is the two columns; narrow, the focused section
// alone, the whole column area, with no read-only pane.
func (m Model) renderProjectView(width, height int) string {
	s, haveSession := m.selected()
	summary := m.projectSummary()
	items := m.inboxSectionItems()
	l := m.projectLayoutWith(width, height, summary, len(items))

	var cols string
	if l.narrow {
		switch sec := m.focusedSection(); sec {
		case sectionBuild:
			cols = m.renderBuildSection(l.rightW, l.buildH, m.sectionHasKeyboard(sec), s, haveSession)
		case sectionInbox:
			cols = m.renderInboxSection(items, l.rightW, l.inboxH, m.sectionHasKeyboard(sec))
		default:
			cols = m.renderSessionsSection(l.leftW, l.sessionsH, m.sectionHasKeyboard(sec))
		}
	} else {
		left := m.renderSessionsSection(l.leftW, l.sessionsH, m.sectionHasKeyboard(sectionSessions))
		if l.summaryH > 0 {
			left = lipgloss.JoinVertical(lipgloss.Left, left,
				theme.readOnlyPane(displayName(s), "", summaryBody(summary, l.leftW-4), l.leftW, l.summaryH))
		}
		right := lipgloss.JoinVertical(lipgloss.Left,
			m.renderBuildSection(l.rightW, l.buildH, m.sectionHasKeyboard(sectionBuild), s, haveSession),
			m.renderInboxSection(items, l.rightW, l.inboxH, m.sectionHasKeyboard(sectionInbox)))
		cols = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	}

	lines := []string{theme.band(projectBandText(projectBandInput{State: m.projectStateWord(), Build: m.build}), l.contentW)}
	if l.colH > 0 {
		lines = append(lines, strings.Split(cols, "\n")...)
	}
	lines = append(lines[:min(len(lines), l.colH+1)], projectChips(m.leaderKey.name, l.contentW))
	name := m.sessionListLabel()
	if dir := m.activeProjectPath(); dir != "" {
		name += "  " + dir
	}
	return theme.pane(name, keyName(m.leaderKey.name, levelProject, actionGoBack)+" · all projects",
		strings.Join(lines, "\n"), l.paneW, l.paneH, false)
}

// sectionBox draws a section's box: §3.4's numbered section in the wide
// layout, and in the narrow one the section with every section named in its
// strip (sectionTabs), so the digits that bring the other two back are on
// screen while they are not. focused is sectionHasKeyboard's answer, passed
// in so a test can draw a section either way.
func (m Model) sectionBox(sec projectSection, context, body string, w, h int, focused bool) string {
	name := projectSectionNames[int(sec)-1]
	if m.projectNarrow() {
		return theme.sectionTabs(projectSectionNames, int(sec)-1, context, body, w, h, focused)
	}
	return theme.section(int(sec), name, context, body, w, h, focused)
}

// sectionView is one section's content as drawn: every line it has, the
// window [start, end) of them on screen, and for each line the index of what
// it shows (a session in m.sessions, a task in the build rows, an item in the
// open items), -1 for a heading, a summary line or a blank. Drawing shows
// lines[start:end]; a click reads owner[start+row], so the two cannot
// disagree about which row is where.
type sectionView struct {
	lines      []string
	owner      []int
	start, end int
	context    string
}

// ownerAt is what the section's visible row row shows, or -1.
func (v sectionView) ownerAt(row int) int {
	i := v.start + row
	if row < 0 || i >= v.end || i >= len(v.owner) {
		return -1
	}
	return v.owner[i]
}

// sessionsSectionView is `1 Sessions` at w×h: the sessions under their group
// headings, the selected one marked with the list's rail and filled while the
// section has the keyboard, scrolled to keep it on screen.
func (m Model) sessionsSectionView(w, h int, focused bool) sectionView {
	bw := max(w-4, 1)
	rows := max(h-sectionStripChrome, 0)
	sel, haveSel := m.selected()
	var v sectionView
	from, to := 0, 0
	if len(m.sessions) == 0 {
		v.lines = []string{
			theme.dim.Render("No afk sessions found."),
			theme.dim.Render("Press " + keyName(m.leaderKey.name, levelProject, actionNewSession) + " to start one."),
		}
		v.owner = []int{-1, -1}
	} else {
		nameW := sessionNameWidth(m.sessions, bw)
		for _, g := range groupSessions(m.sessions) {
			v.lines = append(v.lines, theme.groupHeading(g.Group.String(), len(g.Sessions), sessionGroupTone[g.Group], false, bw))
			v.owner = append(v.owner, -1)
			for _, s := range g.Sessions {
				isSel := haveSel && s.Name == sel.Name && s.Kind == sel.Kind
				if isSel {
					from, to = len(v.lines), len(v.lines)+1
				}
				v.lines = append(v.lines, sessionSectionRow(s, isSel, focused, nameW, bw))
				v.owner = append(v.owner, m.sessionRowIndex(s))
			}
		}
		v.context = fmt.Sprint(len(m.sessions))
	}
	v.start, v.end = allProjectsWindow(from, to, rows, len(v.lines))
	return v
}

// sessionRowIndex is s's index in m.sessions, by name and kind, or -1. It
// matches on the kind too, unlike sessionIndex, because a click must land on
// the row it was on, fault rows included (they share a branch's name).
func (m Model) sessionRowIndex(s Session) int {
	for i, x := range m.sessions {
		if x.Name == s.Name && x.Kind == s.Kind {
			return i
		}
	}
	return -1
}

// renderSessionsSection draws `1 Sessions` (sessionsSectionView).
func (m Model) renderSessionsSection(w, h int, focused bool) string {
	v := m.sessionsSectionView(w, h, focused)
	return m.sectionBox(sectionSessions, v.context, strings.Join(v.lines[v.start:v.end], "\n"), w, h, focused)
}

// summaryBody is the read-only pane's body: each line's label in muted, its
// value in the line's tone, the question's value wrapped under its label so
// it is read in full (§4.2).
func summaryBody(lines []summaryLine, width int) string {
	const labelW = 8
	var out []string
	for _, ln := range lines {
		label := mutedStyle().Render(padRight(ln.Label, labelW))
		if ln.Tone == toneNeed {
			label = needStyle().Render(padRight(ln.Label, labelW))
		}
		val := lipgloss.NewStyle().Foreground(theme.toneColor(ln.Tone))
		if ln.Tone == toneQuiet {
			val = lipgloss.NewStyle()
		}
		wrapped := wrapLines(ln.Value, max(width-labelW, 1))
		for i, w := range wrapped {
			if i == 0 {
				out = append(out, label+val.Render(w))
				continue
			}
			out = append(out, strings.Repeat(" ", labelW)+val.Render(w))
		}
	}
	return strings.Join(out, "\n")
}

// sessionGroupTone is each session group's heading colour (§3.5).
var sessionGroupTone = map[sessionGroup]stateTone{
	groupNeedsYou: toneNeed,
	groupWorking:  toneWork,
	groupDone:     toneDone,
}

// sessionPhaseW is a session row's phase column: the longest phase word
// ("executing", "reviewing", "creating…") and one cell.
const sessionPhaseW = 10

// sessionRowFixed is a session row's cells besides the name: the rail, the
// glyph and its space, the gaps, the phase, the tasks count, and room for the
// widest right word ("asks you") with the cell spread keeps before it.
const sessionRowFixed = rowMarkerCells + 2 + 2 + sessionPhaseW + 2 + rowTasksWidth + 1 + len("asks you")

// sessionNameWidth is the name column's width at a row width cells wide: the
// longest display name, at least 4 cells and at most what the row has left
// after its other columns, so the phase, the count and the right word line up
// down the section and a long name is cut rather than pushing them off.
func sessionNameWidth(ss []Session, width int) int {
	w := 4
	for _, s := range ss {
		w = max(w, lipgloss.Width(displayName(s)))
	}
	return max(min(w, width-sessionRowFixed), 4)
}

// sessionPhaseWord is a session row's phase as a word: what the old list's
// PHASE column said, in lower case, with the cases that have no phase worded
// for what they are.
func sessionPhaseWord(s Session) string {
	switch {
	case s.Pending:
		return pendingBadge
	case isWorktreeFault(s.Kind):
		return strings.ToLower(faultBadgeLabel(s.Kind))
	case s.Scratch:
		return "one-off"
	case s.StatusStale:
		return "stale"
	case usagePaused(s):
		return "paused"
	case s.Phase == "":
		return "unknown"
	}
	return string(s.Phase)
}

// sessionSectionRow is one session's row (§4.2): the rail when selected, the
// glyph and phase in the state's colour (sessionGlyphState, stateLook), the
// name, the tasks count, and the right word (`asks you` in need, `open it`
// muted) at the right edge. The row is filled only when selected in the
// section with the keyboard (theme.row).
func sessionSectionRow(s Session, selected, focused bool, nameW, width int) string {
	kind, value := sessionGlyphState(s)
	style := theme.stateStyle(kind, value)
	left := rowMarker(selected) + theme.renderGlyph(kind, value) + " " +
		padRight(truncate(displayName(s), nameW), nameW) + "  " +
		style.Render(padRight(truncate(sessionPhaseWord(s), sessionPhaseW), sessionPhaseW)) + "  " +
		cell(taskCount(s), rowTasksWidth, lipgloss.Right)
	right := ""
	switch w := sessionRightWord(s); w {
	case "asks you":
		right = needStyle().Render(w)
	case "":
	default:
		right = mutedStyle().Render(w)
	}
	return theme.row(spread(left, right, width), width, selected && focused, "")
}

// buildSectionView is `2 Build` at w×h: the build's summary and one row per
// task, the cursor's row selected while the section has the keyboard and the
// selected session's task as a linked row, scrolled to keep the cursor's row
// (or, without the keyboard, the linked row) on screen.
func (m Model) buildSectionView(w, h int, focused bool, s Session, haveSession bool) sectionView {
	bw := max(w-4, 1)
	rows := max(h-sectionStripChrome, 0)
	var v sectionView
	from := 0
	switch {
	case !m.buildKnown:
		v.lines = []string{theme.dim.Render("Reading the build…")}
	case !m.build.HasBuild && m.build.Problem == "":
		v.lines = []string{theme.dim.Render(truncate("This project has no master plan.", bw))}
	default:
		marks := noBuildMarks
		anchor := m.buildCursorIndex
		if haveSession {
			if i, ok := m.linkedTask(s); ok {
				marks.linked, marks.linkedTo = i, s.Name
				if !focused {
					anchor = i
				}
			}
		}
		if focused && m.buildCursorOnTask() {
			marks.cursor = m.buildCursorIndex
			marks.total = m.buildCursorTotal()
		}
		v.lines = buildSectionLines(m.leaderKey.name, m.build, m.planStatus, bw, marks)
		top := buildRowsTop(m.leaderKey.name, m.build, m.planStatus, bw)
		from = top + anchor
		if m.build.Total > 0 {
			v.context = fmt.Sprintf("%d/%d", m.build.Done, m.build.Total)
		}
		// The task rows start after the summary and its blank line, one line
		// a task (buildSectionLines); a problem leaves no rows to own.
		v.owner = make([]int, len(v.lines))
		for i := range v.owner {
			v.owner[i] = -1
			if t := i - top; m.build.Problem == "" && t >= 0 && t < len(m.build.Rows) {
				v.owner[i] = t
			}
		}
	}
	if v.owner == nil {
		v.owner = make([]int, len(v.lines))
		for i := range v.owner {
			v.owner[i] = -1
		}
	}
	v.start, v.end = allProjectsWindow(min(from, len(v.lines)), min(from+1, len(v.lines)), rows, len(v.lines))
	return v
}

// renderBuildSection draws `2 Build` (buildSectionView).
func (m Model) renderBuildSection(w, h int, focused bool, s Session, haveSession bool) string {
	v := m.buildSectionView(w, h, focused, s, haveSession)
	return m.sectionBox(sectionBuild, v.context, strings.Join(v.lines[v.start:v.end], "\n"), w, h, focused)
}

// inboxSectionView is `3 Inbox` at w×h: one row per open item in the inbox
// overlay's columns (`!` in need when it blocks a task, the id, kind, task
// and title), the cursor's row selected while the section has the keyboard.
func (m Model) inboxSectionView(items []inbox.Item, w, h int, focused bool) sectionView {
	bw := max(w-4, 1)
	rows := max(h-sectionStripChrome, 0)
	var v sectionView
	switch {
	case !m.buildKnown:
		v.lines, v.owner = []string{theme.dim.Render("Reading the inbox…")}, []int{-1}
	case len(items) == 0:
		v.lines, v.owner = []string{theme.dim.Render("Nothing open.")}, []int{-1}
	default:
		v.context = fmt.Sprintf("%d open", len(items))
		cols := inboxColumnsFor(items, bw)
		for i, it := range items {
			sel := focused && i == m.inboxCursorIndex
			v.lines = append(v.lines, theme.row(inboxRowLine(it, cols, bw, false), bw, sel, ""))
			v.owner = append(v.owner, i)
		}
	}
	cur := clampInt(m.inboxCursorIndex, 0, max(len(v.lines)-1, 0))
	v.start, v.end = allProjectsWindow(cur, min(cur+1, len(v.lines)), rows, len(v.lines))
	return v
}

// renderInboxSection draws `3 Inbox` (inboxSectionView).
func (m Model) renderInboxSection(items []inbox.Item, w, h int, focused bool) string {
	v := m.inboxSectionView(items, w, h, focused)
	return m.sectionBox(sectionInbox, v.context, strings.Join(v.lines[v.start:v.end], "\n"), w, h, focused)
}

// projectChip is one chip on the project view's last row.
type projectChip struct {
	action modeAction
	key    string
	word   string
}

// projectChipList is §4.2's chips, less `a` (answer), which T16.7 brings to
// the project view. A chip with an action names its key through the key
// table (keyName), so it cannot name a key the dashboard does not answer to;
// the two that stand for several keys (the sections, the moves) are named
// by their labels.
func projectChipList() []projectChip {
	return []projectChip{
		{actionNone, projectSectionKeysLabel, "section"},
		{actionNone, "j/k", "move"},
		{actionGoIn, "", "open"},
		{actionDetails, "", "details"},
		{actionNewSession, "", "new session"},
		{actionClean, "", "clean…"},
		{actionPauseToggle, "", "pause"},
	}
}

// projectChips draws the chips in width cells, in allProjectsChips' way: a
// chip that does not fit whole is left out with the ones after it.
func projectChips(leader string, width int) string {
	var out string
	for _, c := range projectChipList() {
		k := c.key
		if c.action != actionNone {
			k = keyName(leader, levelProject, c.action)
		}
		chip := theme.keyChip(k, c.word)
		next := chip
		if out != "" {
			next = out + allProjectsGap + chip
		}
		if lipgloss.Width(next) > width {
			break
		}
		out = next
	}
	return out
}

// projectColsTop and projectColsLeft are where the project view's columns
// start inside its box: under the border, the title strip and the band, and
// past the border and the cell of padding every pane body has (paneBody).
const (
	projectColsTop  = 1 + paneStripRows + 1
	projectColsLeft = 2
)

// projectHit is where a cell of the project view's box falls: the section it
// is in, and the section's body row under it (0 the first row drawn), -1 when
// the cell is on the section's border or strip.
type projectHit struct {
	sec projectSection
	row int
}

// projectHitAt is projectHit for the cell (x, y) of a box laid out as l, with
// shown the section a narrow layout shows. ok is false off every section: the
// band, the chips, the read-only pane (which never takes the keyboard, §3.4)
// and the box's own border. It is pure, so the click rules are table-tested
// against the layout rather than against a frame.
func projectHitAt(l projectLayout, shown projectSection, x, y int) (projectHit, bool) {
	type rect struct {
		sec        projectSection
		x, y, w, h int
	}
	var rects []rect
	if l.narrow {
		rects = []rect{{shown, projectColsLeft, projectColsTop, l.contentW, l.colH}}
	} else {
		rx := projectColsLeft + l.leftW + 1
		rects = []rect{
			{sectionSessions, projectColsLeft, projectColsTop, l.leftW, l.sessionsH},
			{sectionBuild, rx, projectColsTop, l.rightW, l.buildH},
			{sectionInbox, rx, projectColsTop + l.buildH, l.rightW, l.inboxH},
		}
	}
	for _, r := range rects {
		// A section is drawn only as far as the column area reaches.
		h := min(r.h, projectColsTop+l.colH-r.y)
		if x < r.x || x >= r.x+r.w || y < r.y || y >= r.y+h {
			continue
		}
		row := y - (r.y + 1 + paneStripRows)
		if row < 0 || row >= r.h-sectionStripChrome || x == r.x || x == r.x+r.w-1 {
			row = -1
		}
		return projectHit{sec: r.sec, row: row}, true
	}
	return projectHit{}, false
}

// projectClick is a left click on the project view at the screen cell
// (x, y) (§4.2, A12): a click on a section focuses it, and a click on one of
// its rows selects that row too — a session (as a move key would, preview
// and all), a task (reading its tokens, as a move in Build does) or an inbox
// item. It opens nothing: enter does that, so a click can never start an
// agent the owner only meant to point at. Off every section (the band, the
// chips, the read-only pane, the home screen round the box) it does nothing.
//
// The row is found on the section as it was drawn before the click
// (sectionView's window and owners), since that is what was under the
// pointer; a click that then moves the focus redraws the section round the
// same row.
func (m Model) projectClick(x, y int) (tea.Model, tea.Cmd) {
	_, _, bodyHeight, headerRows, _ := m.mouseFrameLayout()
	bx, by, w, h := projectBoxRect(m.width, headerRows, bodyHeight)
	if x < bx || x >= bx+w || y < by || y >= by+h {
		return m, nil
	}
	s, haveSession := m.selected()
	items := m.inboxSectionItems()
	l := m.projectLayoutWith(w, h, m.projectSummary(), len(items))
	hit, ok := projectHitAt(l, m.focusedSection(), x-bx, y-by)
	if !ok {
		return m, nil
	}
	owner := -1
	if hit.row >= 0 {
		switch hit.sec {
		case sectionSessions:
			owner = m.sessionsSectionView(l.leftW, l.sessionsH, m.sectionHasKeyboard(hit.sec)).ownerAt(hit.row)
		case sectionBuild:
			owner = m.buildSectionView(l.rightW, l.buildH, m.sectionHasKeyboard(hit.sec), s, haveSession).ownerAt(hit.row)
		case sectionInbox:
			owner = m.inboxSectionView(items, l.rightW, l.inboxH, m.sectionHasKeyboard(hit.sec)).ownerAt(hit.row)
		}
	}
	m.section = hit.sec
	if owner < 0 {
		return m, nil
	}
	switch hit.sec {
	case sectionSessions:
		m.selectSessionIndex(owner)
		return m, m.schedulePreview()
	case sectionBuild:
		prev := m.buildCursorID
		m.buildCursorIndex, m.buildCursorID = owner, m.build.Rows[owner].ID
		if m.buildCursorID != prev {
			return m, m.taskTokensCmd()
		}
	case sectionInbox:
		m.inboxCursorIndex, m.inboxCursorID = owner, items[owner].ID
	}
	return m, nil
}

// projectWheel is a wheel notch at Project: it moves the focused section's
// selection one row, as k and j do (A12), wherever the pointer is. Taking
// the focused section rather than the one under the pointer keeps the wheel
// from moving a selection the keyboard is not on, and it needs no layout, so
// a burst of notches costs no frame measurement.
func (m Model) projectWheel(b tea.MouseButton) (tea.Model, tea.Cmd) {
	switch b {
	case tea.MouseWheelUp:
		return m.applyKeyAction(inputNormal, actionCursorUp, tea.KeyPressMsg{})
	case tea.MouseWheelDown:
		return m.applyKeyAction(inputNormal, actionCursorDown, tea.KeyPressMsg{})
	}
	return m, nil
}
