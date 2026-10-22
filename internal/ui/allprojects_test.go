package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/projectrows"
)

// allProjectsFixture is one row in every state the screen draws differently,
// built through projectrows.Build so the words are the real ones. The roots
// are under /p except the last, the dashboard's own /repo, which is not
// listed (it has the largest order). Order is config.toml's, and it is
// deliberately not the order the screen shows: Sort puts the rows that need
// the owner first.
func allProjectsFixture() []projectrows.Row {
	ins := []projectrows.Input{
		{Name: "alpha", Root: "/p/alpha", Listed: true, Order: 0, HasPlan: true, Done: 3, Total: 8,
			Task: "W1.T2", Title: "Add the parser", Phase: "executing", StateKnown: true, Kind: "wait"},
		{Name: "beta", Root: "/p/beta", Listed: true, Order: 1, HasPlan: true, Done: 2, Total: 6,
			Paused: true, Open: 2, Blocking: 1, Wait: projectrows.Wait{
				ItemID: "bbbbb3", Kind: "question", CreatedAt: "2026-10-20T09:00:00Z",
				Title: "Which database should the cache use?",
				Body:  "The plan says a cache but not where it lives. SQLite keeps it in one file; Redis needs a server on the VM.",
				Task:  "W1.T3", TaskTitle: "Add the cache",
			}},
		{Name: "gamma", Root: "/p/gamma", Listed: true, Order: 2, Gone: true},
		{Name: "delta", Root: "/p/delta", Listed: true, Order: 3,
			ReadErr: "reading /p/delta/.afk/build/ledger.json: invalid character 'x' looking for beginning of value"},
		{Name: "epsilon", Root: "/p/epsilon", Listed: true, Order: 4, PlanWaiting: true},
		{Name: "zeta", Root: "/p/zeta", Listed: true, Order: 5, HasPlan: true, Done: 5, Total: 5},
		{Name: "eta", Root: "/p/eta", Listed: true, Order: 6, HasPlan: true, Done: 1, Total: 4,
			StateKnown: true, Kind: "park", Reason: "W2.T1 asked which database to use", Open: 1, Blocking: 1},
		{Name: "repo", Root: "/repo", Order: 7, HasPlan: true, Done: 0, Total: 3,
			StateKnown: true, Kind: "usage_wait"},
	}
	rows := make([]projectrows.Row, len(ins))
	for i, in := range ins {
		rows[i] = projectrows.Build(in)
	}
	return rows
}

// allProjectsSeam is a fake LoadAllProjects: it counts its calls and returns
// whatever res holds at the time of the call.
type allProjectsSeam struct {
	calls int
	res   AllProjects
}

func (s *allProjectsSeam) load() AllProjects {
	s.calls++
	return s.res
}

// TestAllProjectsInboxAndBar pins a row's INBOX and TASKS cells.
// Load-bearing: a row with nothing in its inbox says nothing (a quiet
// project's cell stays blank), the blocking part uses the band's word, a plan
// waiting says "plan to approve", and a project with no tasks shows "-"
// rather than an empty 0/0 bar.
func TestAllProjectsInboxAndBar(t *testing.T) {
	cases := []struct {
		name string
		row  projectrows.Row
		want string
	}{
		{"nothing", projectrows.Row{}, ""},
		{"open only", projectrows.Row{Open: 3}, "3"},
		{"one blocks", projectrows.Row{Open: 1, Blocking: 1}, "1 · 1 blocking"},
		{"several block", projectrows.Row{Open: 3, Blocking: 2}, "3 · 2 blocking"},
		{"plan only", projectrows.Row{PlanWaiting: true}, "plan to approve"},
		{"both", projectrows.Row{Open: 1, PlanWaiting: true}, "1 · plan to approve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := xansi.Strip(allProjectsInbox(tc.row)); got != tc.want {
				t.Fatalf("allProjectsInbox = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("bar with tasks", func(t *testing.T) {
		got := xansi.Strip(allProjectsBar(projectrows.Row{Done: 2, Total: 5}, allProjectsProgW))
		if !strings.HasSuffix(got, " 2/5") || lipgloss.Width(got) != allProjectsProgW {
			t.Fatalf("bar = %q, want %d cells ending in 2/5", got, allProjectsProgW)
		}
	})
	t.Run("bar with no tasks", func(t *testing.T) {
		if got := allProjectsBar(projectrows.Row{}, allProjectsProgW); strings.TrimSpace(got) != "-" || lipgloss.Width(got) != allProjectsProgW {
			t.Fatalf("bar = %q, want - padded to %d cells", got, allProjectsProgW)
		}
	})
}

// TestAllProjectsBandText pins the band (§4.1 item 1). Load-bearing rows:
// "a mix of groups" (each group counted under its glyph, in group order, and
// the inbox summed over every project), "empty groups are left out" (no "0
// running"), and "no usage reading" (usage left out, never a 0% that was
// not measured).
func TestAllProjectsBandText(t *testing.T) {
	mix := []projectrows.Row{
		{State: projectrows.StateFinished},
		{State: projectrows.StateRunning, Open: 1},
		{State: projectrows.StatePaused, NeedsOwner: true, Open: 2, Blocking: 1},
		{State: projectrows.StateGone, NeedsOwner: true},
		{State: projectrows.StatePaused},
	}
	cases := []struct {
		name     string
		rows     []projectrows.Row
		pct      int
		hasUsage bool
		want     string
	}{
		{"a mix of groups", mix, 0, false,
			"● 2 need you  ◐ 1 running  ○ 1 quiet  ✓ 1 finished   inbox 3 · 1 blocking"},
		{"empty groups are left out", []projectrows.Row{{State: projectrows.StateRunning}}, 0, false,
			"◐ 1 running   inbox 0"},
		{"usage reading", []projectrows.Row{{State: projectrows.StateFinished}}, 62, true,
			"✓ 1 finished   inbox 0   usage "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := xansi.Strip(allProjectsBandText(tc.rows, tc.pct, tc.hasUsage))
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("band = %q, want it to start %q", got, tc.want)
			}
			if tc.hasUsage != strings.Contains(got, "usage") {
				t.Fatalf("band = %q; usage shown should be %v", got, tc.hasUsage)
			}
			if tc.hasUsage && !strings.HasSuffix(got, fmt.Sprintf(" %d%%", tc.pct)) {
				t.Fatalf("band = %q, want it to end with the usage percent", got)
			}
		})
	}
}

// TestHomePanelLines pins the selected project's panel (§4.1 item 4).
// Load-bearing rows: "parked task with a blocking item" (what it waits on,
// the question in full and the task it unblocks, with the title), "plan to
// approve", "gone" (the row's sentence is what it waits on), "nothing
// waiting" (it says so) and "long body is cut at the cap", where only the
// question is cut, its last line ends in "…", and what it waits on and what
// it unblocks both survive.
func TestHomePanelLines(t *testing.T) {
	item := projectrows.Wait{
		ItemID: "aaaaa2", Kind: "question", Title: "Which database?",
		Body: "SQLite or Postgres.\nSay which.", Task: "W2.T1", TaskTitle: "Add the store",
	}
	gone := projectrows.Build(projectrows.Input{Name: "g", Gone: true})
	long := item
	long.Body = strings.Repeat("word ", 80)
	cases := []struct {
		name    string
		row     projectrows.Row
		width   int
		rows    int
		want    []string
		notWant []string
		cut     bool
	}{
		{
			name:  "parked task with a blocking item",
			row:   projectrows.Row{State: projectrows.StateWaiting, NeedsOwner: true, Wait: item},
			width: 60, rows: 8,
			want: []string{"Waits on question aaaaa2", "Which database?", "SQLite or Postgres.", "Say which.", "Unblocks W2.T1 Add the store"},
		},
		{
			name:  "task id alone when the plan gave no title",
			row:   projectrows.Row{NeedsOwner: true, Wait: projectrows.Wait{ItemID: "aaaaa2", Kind: "question", Title: "Q", Task: "W2.T1"}},
			width: 60, rows: 8,
			want: []string{"Unblocks W2.T1"},
		},
		{
			name:  "no unblocks line when the item names no task",
			row:   projectrows.Row{NeedsOwner: true, Wait: projectrows.Wait{ItemID: "aaaaa2", Kind: "decision", Title: "Q"}},
			width: 60, rows: 8,
			want:    []string{"Waits on decision aaaaa2"},
			notWant: []string{"Unblocks"},
		},
		{
			name:  "an item outranks a plan waiting",
			row:   projectrows.Row{NeedsOwner: true, PlanWaiting: true, Wait: item},
			width: 60, rows: 8,
			want:    []string{"Waits on question aaaaa2"},
			notWant: []string{homePanelPlanWaits},
		},
		{
			name:  "plan to approve",
			row:   projectrows.Row{State: projectrows.StateNoPlan, NeedsOwner: true, PlanWaiting: true},
			width: 60, rows: 8,
			want: []string{homePanelPlanWaits, homePanelPlanStarts},
		},
		{
			name:  "gone",
			row:   gone,
			width: 60, rows: 8,
			want:    []string{homePanelWaitsOnYou + " The project folder does not exist."},
			notWant: []string{homePanelNothingHere},
		},
		{
			name:  "nothing waiting",
			row:   projectrows.Build(projectrows.Input{Name: "z", HasPlan: true, Done: 2, Total: 2}),
			width: 60, rows: 8,
			want: []string{homePanelNothingHere, "Every task is done."},
		},
		{
			name:  "long body is cut at the cap",
			row:   projectrows.Row{NeedsOwner: true, Wait: long},
			width: 40, rows: 5,
			want: []string{"Waits on question aaaaa2", "Which database?", "Unblocks W2.T1 Add the store"},
			cut:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := homePanelLines(tc.row, tc.width, tc.rows)
			if len(lines) > tc.rows {
				t.Fatalf("%d lines, want at most %d:\n%s", len(lines), tc.rows, strings.Join(lines, "\n"))
			}
			plain := make([]string, len(lines))
			for i, ln := range lines {
				if w := lipgloss.Width(ln); w > tc.width {
					t.Fatalf("line %d is %d cells, wider than %d: %q", i, w, tc.width, ln)
				}
				plain[i] = xansi.Strip(ln)
			}
			text := strings.Join(plain, "\n")
			// Logged so a -v run shows the panel as drawn, which is the
			// evidence the task's QA reads.
			t.Logf("panel (%d of %d rows):\n%s", len(lines), tc.rows, text)
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Errorf("the panel does not say %q:\n%s", w, text)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(text, w) {
					t.Errorf("the panel says %q and must not:\n%s", w, text)
				}
			}
			if tc.cut {
				if len(lines) != tc.rows {
					t.Fatalf("a cut panel has %d lines, want the cap %d", len(lines), tc.rows)
				}
				if !strings.HasSuffix(plain[len(plain)-2], "…") {
					t.Errorf("the question's last kept line %q does not end in …", plain[len(plain)-2])
				}
			} else if strings.Contains(text, "…") {
				t.Errorf("a panel that fits is cut:\n%s", text)
			}
		})
	}
}

// TestAllProjectsOrder pins the rows' order on the screen (§4.1 item 3):
// NEEDS YOU, RUNNING, QUIET, FINISHED, config order inside each group,
// whatever order the reader returned them in. Load-bearing: the finished
// project listed first in config.toml still comes last, and two quiet
// projects keep their config order.
func TestAllProjectsOrder(t *testing.T) {
	in := []projectrows.Row{
		{Name: "fin", State: projectrows.StateFinished, Order: 0},
		{Name: "quiet2", State: projectrows.StatePaused, Order: 5},
		{Name: "run", State: projectrows.StateRunning, Order: 2},
		{Name: "quiet1", State: projectrows.StateNothingReady, Order: 3},
		{Name: "need", State: projectrows.StateGone, NeedsOwner: true, Order: 4},
	}
	var got []string
	for _, r := range allProjectsOrder(in) {
		got = append(got, r.Name)
	}
	want := []string{"need", "run", "quiet1", "quiet2", "fin"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestAllProjectsListLines pins the grouped list. Load-bearing rows: "wide"
// (one heading per group, in group order with its count, one line per
// project) and "narrow" (two lines per project below 100 columns), plus the
// first row's span taking its heading in, so scrolling to it shows the
// heading.
func TestAllProjectsListLines(t *testing.T) {
	rows := allProjectsOrder(allProjectsFixture())
	cases := []struct {
		name    string
		wide    bool
		perRow  int
		heading []string
	}{
		{"wide", true, 1, []string{"▾ NEEDS YOU", "▾ RUNNING", "▾ QUIET", "▾ FINISHED"}},
		{"narrow", false, 2, []string{"▾ NEEDS YOU", "▾ RUNNING", "▾ QUIET", "▾ FINISHED"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, spans := allProjectsListLines(rows, 0, 76, tc.wide, activityCol{})
			if len(lines) != len(rows)*tc.perRow+len(tc.heading) {
				t.Fatalf("%d lines, want %d", len(lines), len(rows)*tc.perRow+len(tc.heading))
			}
			var heads []string
			for _, ln := range lines {
				if p := strings.TrimSpace(xansi.Strip(ln)); strings.HasPrefix(p, "▾") {
					heads = append(heads, strings.Join(strings.Fields(p)[:len(strings.Fields(p))-1], " "))
				}
			}
			if strings.Join(heads, "|") != strings.Join(tc.heading, "|") {
				t.Fatalf("headings = %v, want %v", heads, tc.heading)
			}
			if !strings.HasSuffix(strings.TrimSpace(xansi.Strip(lines[0])), "5") {
				t.Errorf("NEEDS YOU heading %q does not count its 5 projects", xansi.Strip(lines[0]))
			}
			if spans[0].from != 0 || spans[0].to != 1+tc.perRow {
				t.Errorf("first span = %+v, want its heading and its %d lines", spans[0], tc.perRow)
			}
			for i, sp := range spans[1:] {
				if sp.to-sp.from < tc.perRow {
					t.Errorf("row %d spans %d lines, want at least %d", i+1, sp.to-sp.from, tc.perRow)
				}
			}
		})
	}
}

// TestAllProjectsLayoutFor pins how the pane's rows are shared out.
// Load-bearing: the panel takes a third of the pane and at least 3 rows, and
// a pane too short for that still keeps one project's lines in the list.
// The chart rows (T16.8) are load-bearing too: "chart fits at 120x40" takes
// its two rows from the list and leaves the panel alone, and "chart left
// out" is the pane where taking them would leave the list without one
// project's two lines, so the chart, not the list, gives way.
func TestAllProjectsLayoutFor(t *testing.T) {
	cases := []struct {
		name                          string
		h, perRow, head               int
		chart                         bool
		wantList, wantPane, wantChart int
	}{
		{"120x40", 35, 1, 0, false, 20, 11, 0},
		{"80x24", 19, 2, 0, false, 9, 6, 0},
		{"short pane keeps the panel at three", 9, 1, 0, false, 2, 3, 0},
		{"tiny pane keeps a project", 7, 2, 0, false, 2, 1, 0},
		{"head lines come out of the list", 35, 1, 2, false, 18, 11, 0},
		{"chart fits at 120x40", 35, 1, 0, true, 18, 11, 2},
		{"chart fits at 80x24", 19, 2, 0, true, 7, 6, 2},
		{"chart left out when the list would lose its last project at 80 columns", 10, 2, 0, true, 3, 3, 0},
		{"chart left out on a tiny pane", 7, 2, 0, true, 2, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lay := allProjectsLayoutFor(tc.h, tc.perRow, make([]string, tc.head), true, tc.chart)
			if lay.listRows != tc.wantList || lay.panelRows != tc.wantPane || lay.chartRows != tc.wantChart {
				t.Fatalf("list %d panel %d chart %d, want list %d panel %d chart %d",
					lay.listRows, lay.panelRows, lay.chartRows, tc.wantList, tc.wantPane, tc.wantChart)
			}
		})
	}
}

// TestAllProjectsChips pins the chips (§4.1 item 6). Load-bearing: every
// chip at 120 columns in the design's order, and a narrow pane dropping a
// whole chip rather than cutting one into a key with no word.
func TestAllProjectsChips(t *testing.T) {
	all := "enter open project  a answer  p pause / resume  n new plan  + add  - remove"
	if got := strings.Join(strings.Fields(xansi.Strip(allProjectsChips("ctrl+]", 116))), " "); got != strings.Join(strings.Fields(all), " ") {
		t.Fatalf("chips = %q, want %q", got, all)
	}
	got := xansi.Strip(allProjectsChips("ctrl+]", 76))
	if lipgloss.Width(got) > 76 || strings.Contains(got, "remove") || !strings.HasSuffix(got, "add") {
		t.Fatalf("narrow chips = %q, want every chip but the last, whole", got)
	}
}

// TestAllProjectsWindow pins the scroll: the list moves only when the
// cursor's lines would leave it, and never past the end. The "cursor past the
// page" row is load-bearing: without it the cursor walks off the bottom of
// the screen; "taller than the window" keeps the cursor's first line.
func TestAllProjectsWindow(t *testing.T) {
	cases := []struct {
		name                  string
		from, to, area, total int
		wantStart, wantEnd    int
	}{
		{"all fit", 2, 3, 10, 5, 0, 5},
		{"cursor on the first page", 3, 4, 4, 10, 0, 4},
		{"cursor past the page", 6, 7, 4, 10, 3, 7},
		{"cursor on the last line", 9, 10, 4, 10, 6, 10},
		{"two-line row past the page", 5, 7, 4, 10, 3, 7},
		{"taller than the window", 5, 9, 2, 10, 5, 7},
		{"no lines", 0, 0, 4, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e := allProjectsWindow(tc.from, tc.to, tc.area, tc.total)
			if s != tc.wantStart || e != tc.wantEnd {
				t.Fatalf("window = [%d,%d), want [%d,%d)", s, e, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// TestRowPauseRefusal pins which rows pause and resume refuse on the screen.
// Load-bearing: the no-plan rows use the pause keys' own sentences (so `b`,
// the keys and the screen say one thing), the gone row names the commands
// that fix it, and the unreadable row is let through, because the seam reads
// the project itself and says what is wrong better than the row can.
func TestRowPauseRefusal(t *testing.T) {
	cases := []struct {
		name  string
		row   projectrows.Row
		pause bool
		want  string
	}{
		{"gone pause", projectrows.Row{Name: "gamma", State: projectrows.StateGone}, true, allProjectsGoneNotice("gamma")},
		{"gone resume", projectrows.Row{Name: "gamma", State: projectrows.StateGone}, false, allProjectsGoneNotice("gamma")},
		{"no plan pause", projectrows.Row{Name: "eps", State: projectrows.StateNoPlan}, true, "eps: " + noBuildToPauseNotice},
		{"no plan resume", projectrows.Row{Name: "eps", State: projectrows.StateNoPlan}, false, "eps: " + noBuildToResumeNotice},
		{"unreadable runs", projectrows.Row{State: projectrows.StateUnreadable}, true, ""},
		{"running runs", projectrows.Row{State: projectrows.StateRunning}, true, ""},
		{"paused resumes", projectrows.Row{State: projectrows.StatePaused}, false, ""},
		{"stopped resumes", projectrows.Row{State: projectrows.StateStopped}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rowPauseRefusal(tc.row, tc.pause); got != tc.want {
				t.Fatalf("refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

// openAllProjectsGolden opens the screen on a golden model through the
// leader, as a person does, and lands the first read.
func openAllProjectsGolden(t *testing.T, w, h int, res AllProjects) Model {
	t.Helper()
	m := goldenModel(t, w, h)
	seam := &allProjectsSeam{res: res}
	m.deps.LoadAllProjects = seam.load
	m, cmd := pressAction(t, m, keyEscMsg())
	if !m.allProjectsOpen {
		t.Fatal("esc did not open the all-projects screen")
	}
	for _, msg := range collectMsgs(t, cmd) {
		var next tea.Cmd
		m, next = update(t, m, msg)
		// The rows landing start the activity read (T16.8); land it too, so
		// the frame has its LAST 24H cells and THIS WEEK chart.
		for _, msg := range collectMsgs(t, next) {
			if am, ok := msg.(activityLoadedMsg); ok {
				m, _ = update(t, m, am)
			}
		}
	}
	if !m.allProjectsKnown {
		t.Fatal("the first read did not land")
	}
	return m
}

// TestGoldenAllProjects pins the home screen (§4.1) at both widths with a row
// in every state: the band, the column heads, the four group headings in
// order, the rows (one line each with columns at 120, two lines each at 80
// with the list scrolled to its first rows), and under the rule the selected
// project's panel: beta's blocking question in full and the task it
// unblocks. The chips sit on the pane's last row; at 80 columns the last one
// gives way. Since T16.8 every row read has its LAST 24H line (gone and
// unreadable rows blank) and the THIS WEEK chart sits under the panel with
// the usage line goldenUsage gives, from goldenActivity's fixed numbers.
func TestGoldenAllProjects(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{
		{"dash-allprojects-80x24", 80, 24},
		{"dash-allprojects-120x40", 120, 40},
	} {
		t.Run(size.name, func(t *testing.T) {
			m := openAllProjectsGolden(t, size.w, size.h, AllProjects{
				Rows:       allProjectsFixture(),
				ConfigPath: "/home/owner/.config/afk/config.toml",
			})
			m.build, m.buildKnown = goldenUsage(), true
			assertGolden(t, size.name, render(m))
		})
	}
}

// TestAllProjectsViewLines pins the screen's lines that the goldens do not
// carry. Load-bearing rows: "no listed project" (the only way a new owner
// learns how to fill the screen), "unreadable config keeps the rows" (a
// half-saved config.toml must not blank the screen, and the line must name
// the file), and "before the first read", which must not say "Nothing needs
// you" about projects it has not read.
func TestAllProjectsViewLines(t *testing.T) {
	const cfg = "/home/owner/.config/afk/config.toml"
	active := projectrows.Build(projectrows.Input{Name: "repo", Root: "/repo", HasPlan: true, Total: 2})
	cases := []struct {
		name    string
		model   func(t *testing.T) Model
		want    []string
		notWant []string
	}{
		{
			name: "no listed project",
			model: func(t *testing.T) Model {
				return openAllProjectsGolden(t, 120, 40, AllProjects{Rows: []projectrows.Row{active}, ConfigPath: cfg})
			},
			want: []string{"No projects are listed in " + cfg + ". Add one with afk project add <folder>.", "repo"},
		},
		{
			name: "a listed project hides the empty line",
			model: func(t *testing.T) Model {
				m := openAllProjectsGolden(t, 120, 40, AllProjects{Rows: []projectrows.Row{active}, ConfigPath: cfg})
				m.listed = []ListedProject{{Path: "/repo", Name: "repo"}}
				return m
			},
			notWant: []string{"No projects are listed"},
		},
		{
			name: "two rows hide the empty line before the list is read",
			model: func(t *testing.T) Model {
				return openAllProjectsGolden(t, 120, 40, AllProjects{Rows: allProjectsFixture()[:2], ConfigPath: cfg})
			},
			notWant: []string{"No projects are listed"},
		},
		{
			name: "unreadable config keeps the rows",
			model: func(t *testing.T) Model {
				m := openAllProjectsGolden(t, 120, 40, AllProjects{Rows: allProjectsFixture(), ConfigPath: cfg})
				m, _ = update(t, m, allProjectsLoadedMsg{gen: m.allProjectsGen, res: AllProjects{ConfigPath: cfg, Err: errors.New("line 3: expected '='")}})
				return m
			},
			want: []string{"Could not read " + cfg + ": line 3: expected '='. The rows are from the last good read.", "alpha", "gamma"},
		},
		{
			name: "before the first read",
			model: func(t *testing.T) Model {
				m := goldenModel(t, 120, 40)
				m.deps.LoadAllProjects = func() AllProjects { return AllProjects{} }
				m, _ = pressAction(t, m, keyEscMsg())
				return m
			},
			want:    []string{allProjectsReading},
			notWant: []string{"Nothing needs you"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := render(tc.model(t))
			flat := strings.Join(strings.Fields(frame), " ")
			for _, w := range tc.want {
				if !strings.Contains(flat, strings.Join(strings.Fields(w), " ")) {
					t.Errorf("the screen does not say %q:\n%s", w, frame)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(flat, w) {
					t.Errorf("the screen says %q and must not:\n%s", w, frame)
				}
			}
		})
	}
}
