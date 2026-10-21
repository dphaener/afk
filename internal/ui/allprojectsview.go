// allprojectsview.go draws the all-projects screen (allprojects.go). It reads
// only what the Model already holds — the rows the last read stored, the
// cursor, the last error and the project list — so drawing it reads no file
// and calls no seam.
//
// The screen takes the session list's and the detail side's place in the
// body, as one box the full width of the terminal; the header and the footer
// stay. Every line is cut with an ellipsis rather than wrapped, so a long
// task title or error never moves the rows below it, and the list scrolls to
// keep the cursor on screen.
package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/projectrows"
)

// allProjectsWideWidth is the terminal width from which each project takes
// one line with columns. Below it the columns would cut the detail to a few
// words, so each project takes two lines instead: what it is and its counts,
// then its state and why.
const allProjectsWideWidth = 100

// allProjectsTitle is the name in the box's title strip.
const allProjectsTitle = "All projects"

// Column widths of the wide layout. The label column fits the longest state
// word ("waiting on you", "held for usage") after its §3.6 glyph and a space;
// the name column is as wide as the longest name, up to allProjectsNameMax,
// so a long name is cut rather than pushing every detail off the line.
const (
	allProjectsLabelW   = 16
	allProjectsNameMax  = 24
	allProjectsGap      = "  "
	allProjectsExtrasMx = 48
)

// allProjectsSummary is the screen's first line: how many rows need the
// owner, the count the sort put first.
func allProjectsSummary(rows []projectrows.Row) string {
	n := 0
	for _, r := range rows {
		if r.NeedsOwner {
			n++
		}
	}
	if n == 0 {
		return "Nothing needs you"
	}
	return fmt.Sprintf("%d need you", n)
}

// allProjectsProgress is a row's done/total, or "-" for a project with no
// task list to count (gone, unreadable, no plan).
func allProjectsProgress(r projectrows.Row) string {
	if r.Total == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", r.Done, r.Total)
}

// allProjectsExtras is what is shown beside a row's state rather than
// instead of it: the inbox counts and a plan waiting for approval. Empty when
// there is neither.
func allProjectsExtras(r projectrows.Row) string {
	var parts []string
	if r.Open > 0 {
		s := fmt.Sprintf("%d in inbox", r.Open)
		switch {
		case r.Blocking == 1:
			s += ", 1 holds a task up"
		case r.Blocking > 1:
			s += fmt.Sprintf(", %d hold a task up", r.Blocking)
		}
		parts = append(parts, s)
	}
	if r.PlanWaiting {
		parts = append(parts, "plan waiting for approval")
	}
	return strings.Join(parts, " · ")
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

// allProjectsHead is the lines above the rows: the summary, the reason the
// last read failed, and the empty-list line, then a blank line. Before the
// first read it is the reading line (and the error, if the first read
// failed) and nothing else.
func (m Model) allProjectsHead() []string {
	var head []string
	if !m.allProjectsKnown {
		head = append(head, allProjectsReading)
	} else {
		head = append(head, allProjectsSummary(m.allProjectsRows))
	}
	if m.allProjectsErr != "" {
		path := m.allProjectsConfigPath
		if path == "" {
			path = "config.toml"
		}
		line := "Could not read " + path + ": " + m.allProjectsErr + "."
		if m.allProjectsKnown {
			line += " The rows are from the last good read."
		}
		head = append(head, theme.errorText.Render(line))
	}
	if m.allProjectsEmptyList() {
		path := m.allProjectsConfigPath
		if path == "" {
			path = "config.toml"
		}
		head = append(head, "No projects are listed in "+path+". Add one with afk project add <folder>.")
	}
	if m.allProjectsKnown {
		head = append(head, "")
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

// allProjectsPage is how many projects fit on screen at once, which is both
// how far pgup and pgdown move and how far the list scrolls.
func (m Model) allProjectsPage() int {
	_, contentH := m.allProjectsBox()
	area := contentH - len(m.allProjectsHead())
	return max(area/allProjectsLinesPerRow(m.width), 1)
}

// allProjectsWindow is the first and one-past-last row on screen: the
// smallest scroll that keeps the cursor visible, so the list only moves when
// the cursor would leave it.
func allProjectsWindow(cursor, page, n int) (start, end int) {
	if cursor >= page {
		start = cursor - page + 1
	}
	return start, min(start+page, n)
}

// renderAllProjects draws the screen in the body's place, sized from
// layoutDims as the list and the detail side it stands in for are.
func (m Model) renderAllProjects() string {
	contentW, contentH := m.allProjectsBox()
	lines := m.allProjectsHead()
	for i := range lines {
		lines[i] = truncate(lines[i], contentW)
	}
	hint := ""
	if m.allProjectsKnown && len(m.allProjectsRows) > 0 {
		page := m.allProjectsPage()
		start, end := allProjectsWindow(m.allProjectsCursor, page, len(m.allProjectsRows))
		wide := allProjectsLinesPerRow(m.width) == 1
		nameW, extrasW := allProjectsColumns(m.allProjectsRows, contentW)
		for i := start; i < end; i++ {
			r := m.allProjectsRows[i]
			sel := i == m.allProjectsCursor
			if wide {
				lines = append(lines, allProjectsWideLine(r, sel, nameW, extrasW, contentW))
			} else {
				lines = append(lines, allProjectsNarrowLines(r, sel, contentW)...)
			}
		}
		if end-start < len(m.allProjectsRows) {
			hint = fmt.Sprintf("%d–%d of %d", start+1, end, len(m.allProjectsRows))
		}
	}
	// The pane part cuts the lines to contentH and draws them inside one
	// cell of padding and the border, with the interior painted panel and the
	// selected row filled to the border (§3.5). The title and the scroll
	// position are its title strip, the first inner row. The screen has the
	// keyboard whenever it is up, so the pane is always the focused one.
	return theme.pane(allProjectsTitle, hint, strings.Join(lines, "\n"),
		paneOuterWidth(contentW), contentH+paneStripRows+boxBorderCells, true)
}

// allProjectsState is a row's state as §3.6 draws it: the glyph and the
// state's word in its colour, from stateLook, the one mapping every screen
// uses. The word is the row's label, as before.
func allProjectsState(r projectrows.Row) string {
	return theme.renderState(stateProject, r.Label)
}

// allProjectsColumns sizes the wide layout's name and extras columns from
// every row, not just the ones on screen, so the columns do not jump as the
// list scrolls. The extras column gives way first when the line is short of
// room, because the detail is what says why a project is stuck.
func allProjectsColumns(rows []projectrows.Row, width int) (nameW, extrasW int) {
	for _, r := range rows {
		nameW = max(nameW, lipgloss.Width(r.Name))
		extrasW = max(extrasW, lipgloss.Width(allProjectsExtras(r)))
	}
	nameW = min(max(nameW, 4), allProjectsNameMax)
	extrasW = min(extrasW, allProjectsExtrasMx, width/3)
	return nameW, extrasW
}

// allProjectsWideLine is one project on one line: marker, name, done/total,
// state, detail, then the inbox counts and the plan flag.
//
// The state's glyph (allProjectsState) costs two cells, which come out of the
// progress column (six cells still hold "99/120") and the gap after it (one
// space, then the glyph), so the reason keeps the width it had before the
// glyph was drawn and is not cut any sooner.
func allProjectsWideLine(r projectrows.Row, selected bool, nameW, extrasW, width int) string {
	const progW = 6
	fixed := rowMarkerCells + nameW + len(allProjectsGap) + progW + 1 + allProjectsLabelW + len(allProjectsGap)
	detailW := width - fixed
	if extrasW > 0 {
		detailW -= extrasW + len(allProjectsGap)
	}
	detailW = max(detailW, 1)
	plain := padRight(truncate(r.Name, nameW), nameW) + allProjectsGap +
		padLeft(allProjectsProgress(r), progW) + " " +
		padRight(allProjectsState(r), allProjectsLabelW) + allProjectsGap +
		padRight(truncate(r.Detail, detailW), detailW)
	if extrasW > 0 {
		plain += allProjectsGap + truncate(allProjectsExtras(r), extrasW)
	}
	return allProjectsStyleLine(rowMarker(selected)+truncate(plain, width-rowMarkerCells), r, selected, width)
}

// allProjectsNarrowLines is one project on two lines: the name, done/total,
// counts and plan flag; then, under the name, the state's glyph and word, and
// why.
func allProjectsNarrowLines(r projectrows.Row, selected bool, width int) []string {
	first := r.Name + allProjectsGap + allProjectsProgress(r)
	if x := allProjectsExtras(r); x != "" {
		first += allProjectsGap + x
	}
	// The state's glyph takes the indent's first cell, so the state's word
	// and the reason keep the columns they had before the glyph was drawn.
	second := allProjectsState(r) + " · " + r.Detail
	return []string{
		allProjectsStyleLine(rowMarker(selected)+truncate(first, width-rowMarkerCells), r, selected, width),
		allProjectsStyleLine(rowMarker(selected)+truncate(second, width-rowMarkerCells), r, selected, width),
	}
}

// allProjectsStyleLine paints a row line: the cursor's row is the row part's
// selection (§3.5: sel edge to edge, every word on it sel-fg, the state's
// colour included), a row that needs the owner wears the attention colour,
// and the rest are left as they are. The text was cut before it was styled,
// so the style cannot change where the ellipsis falls.
func allProjectsStyleLine(line string, r projectrows.Row, selected bool, width int) string {
	switch {
	case selected:
		return theme.row(line, width, true, "")
	case r.NeedsOwner:
		// The foreground-only form: the padded badge style would add cells
		// and wrap a line already cut to the box. spanStyle carries it past
		// the state's own colour (allProjectsState), so the words after the
		// state are still in the attention colour.
		return theme.awaitingFlag.Render(spanStyle(line, theme.awaitingFlag))
	}
	return line
}

// padLeft right-aligns s in width cells.
func padLeft(s string, width int) string {
	if gap := width - lipgloss.Width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}
