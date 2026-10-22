// activityview.go draws T16.8's two parts of the home screen
// (docs/dash-design.md §4.1): each project row's 12-cell LAST 24H line and
// the THIS WEEK chart under the selected project's panel. The numbers come
// from Deps.LoadActivity, read off the draw path on the project scan
// (allprojects.go); everything here is pure, so View reads no file and no
// clock to draw them.
package ui

import (
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/activity"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/tokens"
)

// HomeActivity is one read of every listed project's logs for the home
// screen. It is declared here and filled in by the root package, which can
// read the files this package must not.
type HomeActivity struct {
	// Lines are each project's decision-log lines counted into two-hour
	// cells (activity.Count), keyed by the row's Root as AllProjects.Tasks
	// is, so the rows the web page shares (projectrows.Row) gain no field. A
	// project with no entry draws blank cells.
	Lines map[string][activity.Buckets]int
	// Week is the token weight per day of the last seven, summed over every
	// project read (activity.Week.Add).
	Week activity.Week
	// WeekKnown is false when no project's token ledger could be read at
	// all, so the chart is left out rather than drawn as a week of zeros
	// that was never measured.
	WeekKnown bool
}

// activityCellsW is the LAST 24H column: one cell per two-hour step.
const activityCellsW = activity.Buckets

// activityHead is the LAST 24H column's head.
const activityHead = "LAST 24H"

// activityGlyphs are the seven heights of a non-empty cell, activity.Level 1
// to 7.
var activityGlyphs = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇"}

// weekGlyphs are the eight heights of a day's bar: the busiest day is the
// full block, so the tallest bar always reads as "the most this week".
var weekGlyphs = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// activityCol is what the list needs to draw the LAST 24H column: whether to
// draw it at all (a nil LoadActivity draws no column, not an empty one), the
// cells by row Root, and the busiest cell every row is scaled against.
type activityCol struct {
	on      bool
	lines   map[string][activity.Buckets]int
	busiest int
}

// activityColumn is the column for rows, scaled to the busiest cell of any
// project on the screen (§4.1, decisions A3), so a quiet project's line reads
// quiet next to a busy one rather than each line filling its own height.
func activityColumn(on bool, lines map[string][activity.Buckets]int, rows []projectrows.Row) activityCol {
	if !on {
		return activityCol{}
	}
	var all [][activity.Buckets]int
	for _, r := range rows {
		if b, ok := lines[r.Root]; ok {
			all = append(all, b)
		}
	}
	return activityCol{on: true, lines: lines, busiest: activity.Busiest(all...)}
}

// cells is the column's cells for one row.
func (c activityCol) cells(root string) string {
	b, ok := c.lines[root]
	return activityCells(b, ok, c.busiest)
}

// activityCells is one row's LAST 24H line, exactly activityCellsW cells,
// oldest step first. An empty step is ▁ in faint and a step with any activity
// is ▁–▇ in work, so "nothing" reads apart from "a little" although both are
// the lowest block. known false (no read yet, a gone or unreadable project)
// is blank cells: a row of faint ▁ would claim the project was read and
// found quiet. A selected row loses these colours to sel-fg in theme.row, as
// §3.5 asks of every word on the selection.
func activityCells(b [activity.Buckets]int, known bool, busiest int) string {
	if !known {
		return strings.Repeat(" ", activityCellsW)
	}
	faint := lipgloss.NewStyle().Foreground(tok(theme.tokens.Faint))
	work := lipgloss.NewStyle().Foreground(tok(theme.tokens.Work))
	var sb strings.Builder
	for _, n := range b {
		if l := activity.Level(n, busiest); l > 0 {
			sb.WriteString(work.Render(activityGlyphs[l-1]))
		} else {
			sb.WriteString(faint.Render(activityGlyphs[0]))
		}
	}
	return sb.String()
}

// weekChartRows is how many rows the THIS WEEK chart takes under the panel.
const weekChartRows = 2

// weekChartTitle is the chart's heading. A constant because the tests and
// docs/dashboard.md quote it.
const weekChartTitle = "THIS WEEK"

// weekBar is a day's bar against the busiest day: the full block for the
// busiest, ceil(8*sum/busiest) in eighths for the rest, and a faint ▁ for a
// day with nothing, which reads apart from a small day's ▁ in done.
func weekBar(sum, busiest float64) string {
	if sum <= 0 || busiest <= 0 {
		return lipgloss.NewStyle().Foreground(tok(theme.tokens.Faint)).Render(weekGlyphs[0])
	}
	l := int(math.Ceil(8 * sum / busiest))
	l = min(max(l, 1), len(weekGlyphs))
	return lipgloss.NewStyle().Foreground(tok(theme.tokens.Done)).Render(weekGlyphs[l-1])
}

// weekChartLines is the THIS WEEK chart (§4.1 item 5): a heading row with the
// week's total and, at the right end, the usage line; then one entry per
// day, oldest first, each its weekday, a bar and the day's total. The usage
// line is left out when it does not fit beside the heading, because the
// heading says what the row is. When the days do not all fit, the oldest go
// first (decisions A5): today is the day the owner is most likely to be
// asking about. The weekday is taken from Week.Days, which the reader built
// in the dashboard's own time zone, so nothing here reads a clock.
func weekChartLines(w activity.Week, usageLine string, width int) []string {
	if width <= 0 {
		return nil
	}
	muted := mutedStyle()
	total := 0.0
	busiest := 0.0
	for _, s := range w.Sums {
		total += s
		busiest = max(busiest, s)
	}
	head := muted.Bold(true).Render(weekChartTitle) + "  " + tokens.Short(total) + " tokens"
	if usageLine != "" && lipgloss.Width(head)+1+lipgloss.Width(usageLine) <= width {
		head = spread(head, usageLine, width)
	} else {
		head = truncate(head, width)
	}
	entries := make([]string, len(w.Sums))
	for i, s := range w.Sums {
		entries[i] = w.Days[i].Format("Mon") + " " + weekBar(s, busiest) + " " + muted.Render(tokens.Short(s))
	}
	days := strings.Join(entries, allProjectsGap)
	for len(entries) > 1 && lipgloss.Width(days) > width {
		entries = entries[1:]
		days = strings.Join(entries, allProjectsGap)
	}
	return []string{head, truncate(days, width)}
}
