package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/activity"
	"github.com/dphaener/afk/internal/projectrows"
)

// TestActivityCells pins one row's LAST 24H line. Load-bearing rows: "all
// zero" (twelve faint ▁ and no work colour: read and quiet must not look
// active), "busiest from another project" (the height is against the
// busiest cell of any project, not the row's own), and "nil entry", which is
// twelve blank cells, never faint ▁: a project not read must not claim to be
// quiet.
func TestActivityCells(t *testing.T) {
	busy := [activity.Buckets]int{}
	busy[11] = 5
	small := [activity.Buckets]int{}
	small[3] = 2
	cases := []struct {
		name      string
		b         [activity.Buckets]int
		known     bool
		busiest   int
		want      string
		wantWork  bool
		wantFaint bool
	}{
		{"all zero", [activity.Buckets]int{}, true, 0, strings.Repeat("▁", 12), false, true},
		{"one busy bucket", busy, true, 5, strings.Repeat("▁", 11) + "▇", true, true},
		{"busiest from another project", small, true, 8, "▁▁▁▂" + strings.Repeat("▁", 8), true, true},
		{"nil entry", [activity.Buckets]int{}, false, 8, strings.Repeat(" ", 12), false, false},
	}
	work, faint := sgrFg(theme.tokens.Work), sgrFg(theme.tokens.Faint)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := activityCells(tc.b, tc.known, tc.busiest)
			if plain := xansi.Strip(got); plain != tc.want {
				t.Fatalf("cells = %q, want %q", plain, tc.want)
			}
			if lipgloss.Width(got) != activityCellsW {
				t.Fatalf("width %d, want %d", lipgloss.Width(got), activityCellsW)
			}
			if strings.Contains(got, work) != tc.wantWork {
				t.Errorf("work colour present = %v, want %v", !tc.wantWork, tc.wantWork)
			}
			if strings.Contains(got, faint) != tc.wantFaint {
				t.Errorf("faint colour present = %v, want %v", !tc.wantFaint, tc.wantFaint)
			}
		})
	}
}

// weekFixture is a week ending Monday 12 January 1970 in UTC (goldenNow's
// day), with sums in day order.
func weekFixture(sums [7]float64) activity.Week {
	w := activity.Week{Sums: sums}
	for i := range w.Days {
		w.Days[i] = time.Date(1970, 1, 12, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i-6)
	}
	return w
}

// TestWeekChartLines pins the THIS WEEK chart. Load-bearing rows: "one big
// day" (the busiest day is the full block and an empty one a faint ▁),
// "narrow drops the oldest" (today must survive, so the days go oldest
// first), and "no usage line" (no reading, no made-up percentages).
func TestWeekChartLines(t *testing.T) {
	const usage = "week 62% · 5-hour 28% · resets Thu 15:57"
	cases := []struct {
		name     string
		week     activity.Week
		usage    string
		width    int
		wantHead string
		wantDays string
	}{
		{"empty week", weekFixture([7]float64{}), usage, 116,
			"THIS WEEK  0 tokens", "Tue ▁ 0  Wed ▁ 0  Thu ▁ 0  Fri ▁ 0  Sat ▁ 0  Sun ▁ 0  Mon ▁ 0"},
		{"one big day", weekFixture([7]float64{0, 0, 0, 1_200_000, 0, 0, 0}), usage, 116,
			"THIS WEEK  1.2M tokens", "Tue ▁ 0  Wed ▁ 0  Thu ▁ 0  Fri █ 1.2M  Sat ▁ 0  Sun ▁ 0  Mon ▁ 0"},
		{"scaled days", weekFixture([7]float64{0, 0, 0, 0, 0, 400, 800}), "", 116,
			"THIS WEEK  1.2k tokens", "Tue ▁ 0  Wed ▁ 0  Thu ▁ 0  Fri ▁ 0  Sat ▁ 0  Sun ▄ 400  Mon █ 800"},
		{"narrow drops the oldest", weekFixture([7]float64{1, 2, 3, 4, 5, 6, 7}), usage, 30,
			"THIS WEEK  28 tokens", "Sat ▆ 5  Sun ▇ 6  Mon █ 7"},
		{"no usage line", weekFixture([7]float64{0, 0, 0, 0, 0, 0, 10}), "", 116,
			"THIS WEEK  10 tokens", "Tue ▁ 0  Wed ▁ 0  Thu ▁ 0  Fri ▁ 0  Sat ▁ 0  Sun ▁ 0  Mon █ 10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := weekChartLines(tc.week, tc.usage, tc.width)
			if len(lines) != weekChartRows {
				t.Fatalf("%d lines, want %d", len(lines), weekChartRows)
			}
			head, days := xansi.Strip(lines[0]), xansi.Strip(lines[1])
			if !strings.HasPrefix(head, tc.wantHead) {
				t.Errorf("head = %q, want it to start %q", head, tc.wantHead)
			}
			fits := len(tc.wantHead)+1+lipgloss.Width(tc.usage) <= tc.width
			if tc.usage != "" && fits && !strings.HasSuffix(head, tc.usage) {
				t.Errorf("head = %q, want the usage line at its end", head)
			}
			if tc.usage == "" && strings.Contains(head, "week ") {
				t.Errorf("head = %q has a usage line with none given", head)
			}
			if days != tc.wantDays {
				t.Errorf("days = %q, want %q", days, tc.wantDays)
			}
			for _, ln := range lines {
				if lipgloss.Width(ln) > tc.width {
					t.Errorf("line %q is wider than %d", xansi.Strip(ln), tc.width)
				}
			}
		})
	}
	t.Run("an empty day is faint and a full one done", func(t *testing.T) {
		days := weekChartLines(weekFixture([7]float64{0, 0, 0, 0, 0, 0, 9}), "", 116)[1]
		if !strings.Contains(days, sgrFg(theme.tokens.Faint)) || !strings.Contains(days, sgrFg(theme.tokens.Done)) {
			t.Fatalf("days %q lack the faint or done colour", days)
		}
	})
}

// TestActivityHeadsAlign pins that the LAST 24H and INBOX heads sit over the
// cells and the inbox of the rows at 120 columns (wide) and at 80 (narrow,
// where the cells sit on the first line). The wide row is load-bearing: the
// NOW column gives up the cells' width, and a head row that did not would
// put LAST 24H over the end of the sentence.
func TestActivityHeadsAlign(t *testing.T) {
	rows := allProjectsOrder(allProjectsFixture())
	b := [activity.Buckets]int{}
	b[0] = 3
	act := activityColumn(true, map[string][activity.Buckets]int{"/p/beta": b}, rows)
	nameW, inboxW := allProjectsColumns(rows)
	for _, tc := range []struct {
		name  string
		width int
		wide  bool
	}{
		{"wide at 120", 116, true},
		{"narrow at 80", 76, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := xansi.Strip(allProjectsHeads(nameW, inboxW, tc.width, tc.wide, true))
			lines, spans := allProjectsListLines(rows, 1, tc.width, tc.wide, act)
			// beta's line with the cells: a wide row's only line, a narrow
			// row's first.
			perRow := 2
			if tc.wide {
				perRow = 1
			}
			var beta string
			for i, r := range rows {
				if r.Root == "/p/beta" {
					beta = xansi.Strip(lines[spans[i].to-perRow])
				}
			}
			at := strings.Index(head, activityHead)
			if at < 0 {
				t.Fatalf("heads %q have no %s", head, activityHead)
			}
			cell := strings.Index(beta, "▇")
			if lipgloss.Width(head[:at]) != lipgloss.Width(beta[:cell]) {
				t.Fatalf("LAST 24H head at %d, beta's first cell at %d\n%s\n%s",
					lipgloss.Width(head[:at]), lipgloss.Width(beta[:cell]), head, beta)
			}
			inbox := strings.Index(head, "INBOX")
			x := strings.Index(beta, "2 · 1 blocking")
			if lipgloss.Width(head[:inbox]) != lipgloss.Width(beta[:x]) {
				t.Fatalf("INBOX head at %d, beta's inbox at %d", lipgloss.Width(head[:inbox]), lipgloss.Width(beta[:x]))
			}
		})
	}
	t.Run("no reader no column", func(t *testing.T) {
		head := xansi.Strip(allProjectsHeads(nameW, inboxW, 116, true, false))
		lines, _ := allProjectsListLines(rows, 0, 116, true, activityCol{})
		if strings.Contains(head, activityHead) {
			t.Fatalf("heads %q name a column with no reader", head)
		}
		for _, ln := range lines {
			if strings.ContainsAny(xansi.Strip(ln), "▁▂▃▄▅▆▇") {
				t.Fatalf("a row has activity cells with no reader: %q", xansi.Strip(ln))
			}
		}
	})
	t.Run("narrow without room leaves the cells out", func(t *testing.T) {
		long := []projectrows.Row{projectrows.Build(projectrows.Input{Name: strings.Repeat("n", 30), Root: "/p/beta", HasPlan: true, Total: 2, Open: 2, Blocking: 1, PlanWaiting: true})}
		nw, iw := allProjectsColumns(long)
		head := xansi.Strip(allProjectsHeads(nw, iw, 76, false, true))
		lines, _ := allProjectsListLines(long, 0, 76, false, activityColumn(true, map[string][activity.Buckets]int{"/p/beta": b}, long))
		if strings.Contains(head, activityHead) || strings.Contains(xansi.Strip(lines[1]), "▇") {
			t.Fatalf("narrow rows with no room still drew the cells:\n%s\n%s", head, xansi.Strip(lines[1]))
		}
	})
}
