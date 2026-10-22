// allprojectsview.go draws the All projects level as the home screen of
// docs/dash-design.md §4.1. It reads only what the Model already holds — the
// rows the last read stored, the cursor, the last error, the project list and
// the usage reading the build tick keeps — so drawing it reads no file and
// calls no seam.
//
// One pane, "All projects", holds top to bottom: the band (counts by group,
// the inbox summed over every project, usage), the column heads, the rows
// under their group headings, a rule, the selected project's panel and the
// key chips on the pane's last row. Since T16.8 each row also has its LAST
// 24H activity line and the THIS WEEK chart sits under the panel, both drawn
// from the numbers Deps.LoadActivity last read (activityview.go) and left out
// when no reader is wired.
//
// Every line is cut with "…" rather than wrapped, except the panel's question,
// which is wrapped because reading it in full is the panel's purpose; the list
// scrolls to keep the cursor on screen.
package ui

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/usagetext"
)

// allProjectsWideWidth is the terminal width from which each project takes
// one line with columns. Below it the columns would cut the sentence to a few
// words, so each project takes two lines instead (§2): its name and numbers,
// then its state and why.
const allProjectsWideWidth = 100

// allProjectsTitle is the name in the pane's title strip.
const allProjectsTitle = "All projects"

// The columns of a row. The state column fits the longest state word
// projectrows.Build gives ("waiting on you", "held for usage"); the name
// column is as wide as the longest name, never narrower than its head
// (PROJECT) and never wider than allProjectsNameMax, so a long name is cut
// rather than pushing every sentence off the line. The progress column holds
// a bar and "99/120". The inbox column is as wide as the widest inbox cell,
// up to allProjectsInboxMax.
const (
	allProjectsLabelW   = 14
	allProjectsNameMin  = 7
	allProjectsNameMax  = 24
	allProjectsProgW    = 14
	allProjectsInboxMin = 5
	allProjectsInboxMax = 24
	allProjectsGap      = "  "
	// allProjectsGlyphCells is a wide row's state glyph and the space after
	// it, ahead of the name.
	allProjectsGlyphCells = 2
	// allProjectsPanelMin is the fewest rows the selected project's panel
	// takes: enough for what it waits on, one line of the question and what
	// it unblocks.
	allProjectsPanelMin = 3
	// allProjectsUsageBarW is the band's usage bar with its "62%".
	allProjectsUsageBarW = 14
)

// allProjectsGroupTone is the colour of each group's heading and glyph in
// the band: §3.6's tone for what the group means.
var allProjectsGroupTone = map[projectrows.Group]stateTone{
	projectrows.GroupNeedsYou: toneNeed,
	projectrows.GroupRunning:  toneWork,
	projectrows.GroupQuiet:    toneQuiet,
	projectrows.GroupFinished: toneDone,
}

// allProjectsGroupWord is the band's word after each group's count.
var allProjectsGroupWord = map[projectrows.Group]string{
	projectrows.GroupNeedsYou: "need you",
	projectrows.GroupRunning:  "running",
	projectrows.GroupQuiet:    "quiet",
	projectrows.GroupFinished: "finished",
}

// mutedStyle is text in the theme's muted token: the column heads, the
// panel's labels and a chip's action.
func mutedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(tok(theme.tokens.Muted))
}

// needStyle is text in the theme's need token: the blocking part of an inbox
// count, which is the part the owner has to act on.
func needStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(tok(theme.tokens.Need))
}

// allProjectsUsage is the band's usage reading: the tightest window's used
// percent, from the snapshot the build tick already holds (m.build), never
// from usage.json, because View must not touch the disk. False with no
// reading, and the band then leaves usage out rather than show a 0% that was
// never measured.
func (m Model) allProjectsUsage() (int, bool) {
	if !m.buildKnown || !m.build.UsageKnown {
		return 0, false
	}
	w, ok := usagetext.Tightest(m.build.Usage.Windows)
	if !ok {
		return 0, false
	}
	return int(math.Round(w.UsedPct)), true
}

// allProjectsBandText is the band's text (§4.1 item 1): the count of each
// group with its glyph, groups with no project left out; then the inbox
// summed over every row, its blocking part in need; then usage as a bar when
// there is a reading.
func allProjectsBandText(rows []projectrows.Row, usagePct int, hasUsage bool) string {
	var counts []string
	for g, rs := range projectrows.Grouped(rows) {
		if len(rs) == 0 {
			continue
		}
		grp := projectrows.Group(g)
		tone := allProjectsGroupTone[grp]
		glyph := lipgloss.NewStyle().Foreground(theme.toneColor(tone)).Render(toneGlyph[tone])
		counts = append(counts, fmt.Sprintf("%s %d %s", glyph, len(rs), allProjectsGroupWord[grp]))
	}
	open, blocking := 0, 0
	for _, r := range rows {
		open += r.Open
		blocking += r.Blocking
	}
	parts := []string{strings.Join(counts, "  ")}
	inbox := fmt.Sprintf("inbox %d", open)
	if blocking > 0 {
		inbox += " · " + needStyle().Render(fmt.Sprintf("%d blocking", blocking))
	}
	parts = append(parts, inbox)
	if hasUsage {
		parts = append(parts, "usage "+theme.progressPercent(usagePct, allProjectsUsageBarW))
	}
	return strings.Join(parts, "   ")
}

// allProjectsInbox is a row's INBOX cell: the open count, how many of them
// block a task (in need, worded as the band words it), and "plan to approve"
// when a plan waits. Empty when there is nothing, so a quiet project's cell
// stays blank.
func allProjectsInbox(r projectrows.Row) string {
	var parts []string
	if r.Open > 0 {
		s := fmt.Sprint(r.Open)
		if r.Blocking > 0 {
			s += " · " + needStyle().Render(fmt.Sprintf("%d blocking", r.Blocking))
		}
		parts = append(parts, s)
	}
	if r.PlanWaiting {
		parts = append(parts, "plan to approve")
	}
	return strings.Join(parts, " · ")
}

// allProjectsBar is a row's TASKS cell, exactly width cells: a progress bar
// with done/total, or "-" for a project with no task list to count (gone,
// unreadable, no plan), where an empty "0/0" bar would read as a plan with
// nothing in it.
func allProjectsBar(r projectrows.Row, width int) string {
	if r.Total == 0 {
		return padRight("-", width)
	}
	return theme.progressCount(r.Done, r.Total, width)
}

// allProjectsEmptyList reports whether the screen should say that nothing is
// listed. The rows cannot say it on their own — one row is either the one
// listed project or the unlisted active project — so the project list the
// switcher read (Model.listed) is asked too. Both must agree: that list is
// read on a slower tick, and a project just added with `afk project add`
// shows here as a second row before it does there, so a screen with more
// than one row never claims the list is empty.
func (m Model) allProjectsEmptyList() bool {
	return m.allProjectsKnown && len(m.listed) == 0 && len(m.allProjectsRows) <= 1
}

// allProjectsHead is the lines under the band: the reading line before the
// first read, the reason the last read failed, and the empty-list line.
func (m Model) allProjectsHead() []string {
	var head []string
	if !m.allProjectsKnown {
		head = append(head, allProjectsReading)
	}
	path := m.allProjectsConfigPath
	if path == "" {
		path = "config.toml"
	}
	if m.allProjectsErr != "" {
		line := "Could not read " + path + ": " + m.allProjectsErr + "."
		if m.allProjectsKnown {
			line += " The rows are from the last good read."
		}
		head = append(head, theme.errorText.Render(line))
	}
	if m.allProjectsEmptyList() {
		head = append(head, "No projects are listed in "+path+". Add one with afk project add <folder>.")
	}
	return head
}

// allProjectsLinesPerRow is how many lines one project takes at a terminal
// width.
func allProjectsLinesPerRow(termWidth int) int {
	if termWidth >= allProjectsWideWidth {
		return 1
	}
	return 2
}

// allProjectsBox is the content size of the screen's pane: the full body less
// the border and the padding (sidebarContentWidth's arithmetic), and less the
// pane's title strip row (paneStripRows), which renderAllProjects' pane draws
// above the rows.
func (m Model) allProjectsBox() (contentW, contentH int) {
	listOuter, mainOuter, bodyHeight := m.layoutDims()
	return sidebarContentWidth(listOuter + mainOuter), max(bodyHeight-boxBorderCells-paneStripRows, 1)
}

// allProjectsLayout is how the pane's rows are shared out. It is worked out
// in one place because both the drawing and the paging keys (allProjectsPage)
// need the list's height, and two copies of the arithmetic would let pgdown
// move a different distance from the one the screen shows.
type allProjectsLayout struct {
	width, height int
	head          []string
	// list is false before the first read and with no rows: there is no
	// band, list or panel to draw, only the head lines and the chips.
	list bool
	// listRows is the lines the grouped rows get; panelRows the selected
	// project's panel's, under its rule.
	listRows, panelRows int
	// chartRows is the THIS WEEK chart's, under the panel: weekChartRows,
	// or 0 when it is not drawn.
	chartRows int
}

// allProjectsLayoutFor shares contentH rows out: one each for the band, the
// column heads, the rule and the chips, the head lines, then the panel (a
// third of the pane, at least allProjectsPanelMin) and the list the rest. A
// pane too short for that keeps at least one project's lines in the list and
// gives the panel what is left, down to one row, because the list is how the
// owner moves at all.
//
// chart asks for the THIS WEEK chart's rows under the panel. They are taken
// from the list, and only when the list still holds one project's lines
// after them (decisions A5): the chart is a summary, the list is how the
// owner gets anywhere, so on a short pane the chart is what gives way.
func allProjectsLayoutFor(contentH, linesPerRow int, head []string, list, chart bool) allProjectsLayout {
	lay := allProjectsLayout{height: contentH, head: head, list: list}
	if !list {
		return lay
	}
	fixed := 4 + len(head)
	panel := max(contentH/3, allProjectsPanelMin)
	rows := contentH - fixed - panel
	if chart && rows-weekChartRows >= linesPerRow {
		lay.chartRows = weekChartRows
		rows -= weekChartRows
	}
	if rows < linesPerRow {
		panel = max(contentH-fixed-linesPerRow, 1)
		rows = contentH - fixed - panel
	}
	lay.listRows, lay.panelRows = max(rows, 1), panel
	return lay
}

// allProjectsLayout is allProjectsLayoutFor for this Model.
func (m Model) allProjectsLayout() allProjectsLayout {
	w, h := m.allProjectsBox()
	list := m.allProjectsKnown && len(m.allProjectsRows) > 0
	lay := allProjectsLayoutFor(h, allProjectsLinesPerRow(m.width), m.allProjectsHead(), list, m.weekChartShown())
	lay.width = w
	return lay
}

// weekChartShown is whether there is a THIS WEEK chart to draw: a reader is
// wired and a read with a week in it has landed.
func (m Model) weekChartShown() bool {
	return m.deps.LoadActivity != nil && m.homeActivityKnown && m.homeActivity.WeekKnown
}

// allProjectsActivity is the LAST 24H column for the screen's rows, off when
// no reader is wired.
func (m Model) allProjectsActivity() activityCol {
	return activityColumn(m.deps.LoadActivity != nil, m.homeActivity.Lines, m.allProjectsRows)
}

// allProjectsPage is how many projects fit on screen at once, which is how
// far pgup and pgdown move. The group headings share the list's lines, so a
// page can show a project or two fewer than this; moving a little less than
// a screenful keeps the row the owner was on in sight.
func (m Model) allProjectsPage() int {
	return max(m.allProjectsLayout().listRows/allProjectsLinesPerRow(m.width), 1)
}

// allProjectsWindow is the first and one-past-last line of the list on
// screen: the smallest scroll that keeps the cursor's lines [from, to)
// visible, so the list only moves when the cursor would leave it. from wins
// over to when the cursor's lines are taller than the window.
func allProjectsWindow(from, to, area, total int) (start, end int) {
	if to > area {
		start = to - area
	}
	start = min(start, from)
	return start, min(start+area, total)
}

// lineSpan is the lines [from, to) a project takes in the list.
type lineSpan struct{ from, to int }

// allProjectsColumns sizes the name and inbox columns from every row, not
// just the ones on screen, so the columns do not jump as the list scrolls.
func allProjectsColumns(rows []projectrows.Row) (nameW, inboxW int) {
	for _, r := range rows {
		nameW = max(nameW, lipgloss.Width(r.Name))
		inboxW = max(inboxW, lipgloss.Width(allProjectsInbox(r)))
	}
	nameW = min(max(nameW, allProjectsNameMin), allProjectsNameMax)
	inboxW = min(max(inboxW, allProjectsInboxMin), allProjectsInboxMax)
	return nameW, inboxW
}

// allProjectsSentenceW is the NOW column's width on a wide line: whatever the
// other columns leave, the LAST 24H column and its gap included when act.
func allProjectsSentenceW(nameW, inboxW, width int, act bool) int {
	fixed := rowMarkerCells + allProjectsGlyphCells + nameW + allProjectsLabelW + allProjectsProgW + inboxW + 4*len(allProjectsGap)
	if act {
		fixed += activityCellsW + len(allProjectsGap)
	}
	return max(width-fixed, 1)
}

// allProjectsNarrowActivity is whether a narrow row's first line holds the
// LAST 24H cells between the tasks bar and the widest inbox cell (decisions
// A7). It is decided once for every row, from the columns, so the cells and
// the inbox line up down the list; when they do not fit the cells give way,
// because the inbox is what the owner acts on.
func allProjectsNarrowActivity(nameW, inboxW, width int) bool {
	need := rowMarkerCells + nameW + allProjectsProgW + activityCellsW + inboxW + 3*len(allProjectsGap)
	return need <= width
}

// allProjectsHeads is the column heads in muted (§4.1 item 2), set over the
// columns the rows use. Below allProjectsWideWidth they head the first of a
// project's two lines (name, tasks, inbox), since the state and the sentence
// sit under the name there.
//
// act is whether the LAST 24H column is drawn at all; below
// allProjectsWideWidth it is headed only when the rows have room for it
// (allProjectsNarrowActivity), so the head never names a column the rows
// left out.
func allProjectsHeads(nameW, inboxW, width int, wide, act bool) string {
	var s string
	if wide {
		s = strings.Repeat(" ", rowMarkerCells+allProjectsGlyphCells) +
			padRight("PROJECT", nameW) + allProjectsGap +
			padRight("STATE", allProjectsLabelW) + allProjectsGap +
			padRight("TASKS", allProjectsProgW) + allProjectsGap +
			padRight("NOW", allProjectsSentenceW(nameW, inboxW, width, act)) + allProjectsGap
		if act {
			s += padRight(activityHead, activityCellsW) + allProjectsGap
		}
		s += "INBOX"
	} else {
		s = strings.Repeat(" ", rowMarkerCells) +
			padRight("PROJECT", nameW) + allProjectsGap +
			padRight("TASKS", allProjectsProgW) + allProjectsGap
		if act && allProjectsNarrowActivity(nameW, inboxW, width) {
			s += padRight(activityHead, activityCellsW) + allProjectsGap
		}
		s += "INBOX"
	}
	return mutedStyle().Render(truncate(s, width))
}

// allProjectsListLines is the grouped list (§4.1 item 3): each non-empty
// group under its heading (▾, the group's colour, its count) and its rows in
// the order they come, which applyAllProjectsLoaded made group order with
// config order inside. spans[i] is where rows[i] sits, its heading included
// when it is the first of its group, so scrolling to it brings the heading
// along. Rows must be in group order; a run of one group is one heading.
// act is the LAST 24H column; its zero value draws none.
func allProjectsListLines(rows []projectrows.Row, cursor, width int, wide bool, act activityCol) (lines []string, spans []lineSpan) {
	nameW, inboxW := allProjectsColumns(rows)
	spans = make([]lineSpan, len(rows))
	for i := 0; i < len(rows); i++ {
		g := projectrows.GroupOf(rows[i])
		from := len(lines)
		if i == 0 || projectrows.GroupOf(rows[i-1]) != g {
			n := 1
			for i+n < len(rows) && projectrows.GroupOf(rows[i+n]) == g {
				n++
			}
			lines = append(lines, theme.groupHeading(g.String(), n, allProjectsGroupTone[g], false, width))
		}
		sel := i == cursor
		if wide {
			lines = append(lines, allProjectsWideLine(rows[i], sel, nameW, inboxW, width, act))
		} else {
			lines = append(lines, allProjectsNarrowLines(rows[i], sel, nameW, inboxW, width, act)...)
		}
		spans[i] = lineSpan{from: from, to: len(lines)}
	}
	return lines, spans
}

// allProjectsState is a row's glyph and state word in its §3.6 colour, from
// stateLook, the one mapping every screen uses.
func allProjectsState(r projectrows.Row) (glyph, word string) {
	a := stateLook(stateProject, r.Label)
	st := theme.stateStyle(stateProject, r.Label)
	return st.Render(a.glyph), st.Render(a.word)
}

// allProjectsWideLine is one project on one line: the cursor's rail, the
// state's glyph, the name, the state word, the progress bar, the sentence cut
// with "…", the LAST 24H cells when act is on, and the inbox cell.
func allProjectsWideLine(r projectrows.Row, selected bool, nameW, inboxW, width int, act activityCol) string {
	glyph, word := allProjectsState(r)
	sentW := allProjectsSentenceW(nameW, inboxW, width, act.on)
	line := rowMarker(selected) + glyph + " " +
		padRight(truncate(r.Name, nameW), nameW) + allProjectsGap +
		padRight(truncate(word, allProjectsLabelW), allProjectsLabelW) + allProjectsGap +
		allProjectsBar(r, allProjectsProgW) + allProjectsGap +
		padRight(truncate(r.Detail, sentW), sentW) + allProjectsGap
	if act.on {
		line += act.cells(r.Root) + allProjectsGap
	}
	line += truncate(allProjectsInbox(r), inboxW)
	return theme.row(truncate(line, width), width, selected, "")
}

// allProjectsNarrowLines is one project on two lines (§2): the name, the
// progress bar, the LAST 24H cells when they fit (allProjectsNarrowActivity)
// and the inbox cell; then, under the name, the state's glyph and word and
// the sentence.
func allProjectsNarrowLines(r projectrows.Row, selected bool, nameW, inboxW, width int, act activityCol) []string {
	glyph, word := allProjectsState(r)
	first := rowMarker(selected) + padRight(truncate(r.Name, nameW), nameW) + allProjectsGap +
		allProjectsBar(r, allProjectsProgW)
	if act.on && allProjectsNarrowActivity(nameW, inboxW, width) {
		first += allProjectsGap + act.cells(r.Root)
	}
	if x := allProjectsInbox(r); x != "" {
		first += allProjectsGap + x
	}
	second := rowMarker(selected) + glyph + " " + word + " · " + r.Detail
	return []string{
		theme.row(truncate(first, width), width, selected, ""),
		theme.row(truncate(second, width), width, selected, ""),
	}
}

// allProjectsRule is the rule above the panel (§4.1 item 4), in line, with
// the selected project's name set into it so the panel says whose it is
// without taking one of its own rows.
func allProjectsRule(name string, width int) string {
	line := lipgloss.NewStyle().Foreground(tok(theme.tokens.Line))
	name = truncate(name, max(width-4, 0))
	if name == "" {
		return line.Render(strings.Repeat("─", max(width, 0)))
	}
	rest := max(width-lipgloss.Width(name)-3, 0)
	return line.Render("─ ") + lipgloss.NewStyle().Bold(true).Render(name) + " " + line.Render(strings.Repeat("─", rest))
}

// Words of the selected project's panel. Constants because the tests and
// docs/dashboard.md quote them.
const (
	homePanelWaitsOn     = "Waits on"
	homePanelUnblocks    = "Unblocks"
	homePanelPlanWaits   = "Waits on your approval of a new plan."
	homePanelPlanStarts  = "The build starts once it is approved."
	homePanelWaitsOnYou  = "Waits on you:"
	homePanelNothingHere = "Nothing here waits on you."
)

// homePanelLines is the selected project's panel (§4.1 item 4): what it
// waits on, the question in full and what answering it unblocks, at most
// maxRows lines of at most width cells. It is pure, so every case is a table
// row (allprojects_test.go).
//
//   - The oldest open blocking inbox item (Row.Wait): "Waits on" its kind and
//     id, the title in bold and the body, wrapped, then "Unblocks" the task
//     and its title (the id alone when the plan gave no title, nothing when
//     the item names no task). The item outranks a plan waiting, because the
//     item holds a task up and the plan only holds up a build not started.
//   - A plan waiting for approval: it waits on the owner's approval and the
//     build starts once it is given.
//   - Any other row that needs the owner (gone, can't read, stopped, a task
//     parked with no item to show): the row's sentence is what it waits on,
//     since the sentence is already the plain account of what is wrong.
//   - Nothing waiting: it says so, then the row's sentence.
//
// Only the question is cut when the lines overflow, and its last line kept
// ends in "…": what it waits on and what it unblocks are the two lines the
// owner cannot do without, and the full question is one keypress away in the
// inbox.
func homePanelLines(r projectrows.Row, width, maxRows int) []string {
	if width <= 0 || maxRows <= 0 {
		return nil
	}
	muted, bold := mutedStyle(), lipgloss.NewStyle().Bold(true)
	var head, mid, tail []string
	switch {
	case r.Wait.ItemID != "":
		w := r.Wait
		what := strings.TrimSpace(inboxClean(w.Kind, true) + " " + inboxClean(w.ItemID, true))
		head = []string{muted.Render(homePanelWaitsOn) + " " + what}
		for _, ln := range wrapLines(inboxClean(w.Title, true), width) {
			if ln != "" {
				mid = append(mid, bold.Render(ln))
			}
		}
		if body := inboxClean(w.Body, false); body != "" {
			for _, para := range strings.Split(body, "\n") {
				mid = append(mid, wrapLines(strings.TrimRight(para, " "), width)...)
			}
		}
		if task := inboxClean(w.Task, true); task != "" {
			t := muted.Render(homePanelUnblocks) + " " + task
			if title := inboxClean(w.TaskTitle, true); title != "" {
				t += " " + title
			}
			tail = []string{t}
		}
	case r.PlanWaiting:
		head = []string{homePanelPlanWaits}
		mid = wrapLines(homePanelPlanStarts, width)
	case r.NeedsOwner:
		mid = wrapLines(homePanelWaitsOnYou+" "+r.Detail, width)
	default:
		head = []string{homePanelNothingHere}
		mid = wrapLines(r.Detail, width)
	}
	return fitPanel(head, mid, tail, width, maxRows)
}

// fitPanel joins the panel's parts into at most maxRows lines of width
// cells. When they overflow the middle is cut and its last kept line ends in
// "…"; when even one middle line does not fit beside the head and the tail,
// the tail goes first and then the head is cut, so a panel of one row still
// says something.
func fitPanel(head, mid, tail []string, width, maxRows int) []string {
	room := maxRows - len(head) - len(tail)
	if room < 1 && len(mid) > 0 {
		tail = nil
		room = maxRows - len(head)
	}
	if room < 1 && len(mid) > 0 {
		head = head[:max(maxRows-1, 0)]
		room = maxRows - len(head)
	}
	if len(mid) > room {
		mid = append([]string(nil), mid[:max(room, 0)]...)
		if n := len(mid); n > 0 {
			last := mid[n-1]
			if lipgloss.Width(last) >= width {
				last = xansi.Truncate(last, width-1, "")
			}
			mid[n-1] = last + "…"
		}
	}
	out := make([]string, 0, maxRows)
	for _, part := range [][]string{head, mid, tail} {
		for _, ln := range part {
			out = append(out, truncate(ln, width))
		}
	}
	if len(out) > maxRows {
		out = out[:maxRows]
	}
	return out
}

// allProjectsChip is one chip on the pane's last row: the action whose key it
// names, the key to show when the key table has no row for the action yet,
// and the word for what it does.
type allProjectsChip struct {
	action   modeAction
	fallback string
	word     string
}

// allProjectsChipList is §4.1 item 6's chips, in the design's order, in one
// place so the task that gives `a` and `p` their All projects rows (W3.T1 of
// T16.3) changes only the action named here. Every chip names an action with a row at this
// level; the fallback shows only if a row were ever removed.
func allProjectsChipList() []allProjectsChip {
	return []allProjectsChip{
		{actionGoIn, "enter", "open project"},
		{actionAnswerOldest, "a", "answer"},
		{actionPauseToggle, "p", "pause / resume"},
		{actionPlanNew, "n", "new plan"},
		{actionProjectAdd, "+", "add"},
		{actionProjectRemove, "-", "remove"},
	}
}

// allProjectsChips draws the chips in width cells, each key named through
// the key table (keyName) so a chip cannot name a key the dashboard does not
// answer to. A chip that does not fit whole is left out with the ones after
// it, rather than cut into a key with no word: at 80 columns the last one
// gives way.
func allProjectsChips(leader string, width int) string {
	var out string
	for _, c := range allProjectsChipList() {
		k := ""
		if c.action != actionNone {
			k = keyName(leader, levelAllProjects, c.action)
		}
		if k == "" {
			k = c.fallback
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

// renderAllProjects draws the screen in the body's place, sized from
// layoutDims as the list and the detail side it stands in for are.
func (m Model) renderAllProjects() string {
	lay := m.allProjectsLayout()
	w := lay.width
	var lines []string
	hint := ""
	if lay.list {
		usage, hasUsage := m.allProjectsUsage()
		lines = append(lines, theme.band(allProjectsBandText(m.allProjectsRows, usage, hasUsage), w))
	}
	for _, h := range lay.head {
		lines = append(lines, truncate(h, w))
	}
	if lay.list {
		wide := allProjectsLinesPerRow(m.width) == 1
		nameW, inboxW := allProjectsColumns(m.allProjectsRows)
		act := m.allProjectsActivity()
		lines = append(lines, allProjectsHeads(nameW, inboxW, w, wide, act.on))
		list, spans := allProjectsListLines(m.allProjectsRows, m.allProjectsCursor, w, wide, act)
		cur := spans[clampInt(m.allProjectsCursor, 0, len(spans)-1)]
		start, end := allProjectsWindow(cur.from, cur.to, lay.listRows, len(list))
		// A list shorter than its share is not padded: the rule, the panel
		// and the THIS WEEK chart follow it at once, and the rows left over
		// fall above the chips. The panel and the chart still keep their
		// place while the owner moves, because the number of rows does not
		// change with the cursor.
		lines = append(lines, list[start:end]...)
		first, last := -1, -1
		for i, s := range spans {
			if s.from >= start && s.to <= end {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first > 0 || last < len(spans)-1 {
			hint = fmt.Sprintf("%d–%d of %d", first+1, last+1, len(spans))
		}
		if r, ok := m.selectedProjectRow(); ok {
			lines = append(lines, allProjectsRule(r.Name, w))
			panel := homePanelLines(r, w, lay.panelRows)
			for len(panel) < lay.panelRows {
				panel = append(panel, "")
			}
			lines = append(lines, panel...)
		}
		if lay.chartRows > 0 {
			// The usage reset is told in the zone the reader built the days
			// in, so the chart and its usage line speak one clock; the
			// location is data the read carried, not a clock read here.
			usage := ""
			if m.buildKnown && m.build.UsageKnown {
				usage = usagetext.WeekLine(m.build.Usage.Windows, m.homeActivity.Week.Days[6].Location())
			}
			lines = append(lines, weekChartLines(m.homeActivity.Week, usage, w)...)
		}
	}
	for len(lines) < lay.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(lay.height-1, 0))], allProjectsChips(m.leaderKey.name, w))
	// The pane part cuts the lines to the height and draws them inside one
	// cell of padding and the border, with the interior painted panel and
	// the selected row filled to the border (§3.5). The title and the scroll
	// position are its title strip. The screen has the keyboard whenever it
	// is up, so the pane is always the focused one.
	return theme.pane(allProjectsTitle, hint, strings.Join(lines, "\n"),
		paneOuterWidth(w), lay.height+paneStripRows+boxBorderCells, true)
}
