package ui

import (
	"errors"
	"strings"
	"testing"

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
			Paused: true, Open: 2, Blocking: 1},
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

// TestAllProjectsExtras pins the words beside a row's state. Load-bearing:
// the singular "holds" (the README and the end-to-end check quote "1 in
// inbox, 1 holds a task up"), and a row with nothing in its inbox saying
// nothing, so a quiet project's line stays short.
func TestAllProjectsExtras(t *testing.T) {
	cases := []struct {
		name string
		row  projectrows.Row
		want string
	}{
		{"nothing", projectrows.Row{}, ""},
		{"open only", projectrows.Row{Open: 3}, "3 in inbox"},
		{"one holds a task up", projectrows.Row{Open: 1, Blocking: 1}, "1 in inbox, 1 holds a task up"},
		{"several hold a task up", projectrows.Row{Open: 3, Blocking: 2}, "3 in inbox, 2 hold a task up"},
		{"plan only", projectrows.Row{PlanWaiting: true}, "plan waiting for approval"},
		{"both", projectrows.Row{Open: 1, PlanWaiting: true}, "1 in inbox · plan waiting for approval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allProjectsExtras(tc.row); got != tc.want {
				t.Fatalf("allProjectsExtras = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAllProjectsSummaryAndProgress pins the screen's top line and the
// done/total column. The "nothing" row is load-bearing: a screen with no
// project needing the owner says so rather than "0 need you".
func TestAllProjectsSummaryAndProgress(t *testing.T) {
	t.Run("summary", func(t *testing.T) {
		cases := []struct {
			name string
			rows []projectrows.Row
			want string
		}{
			{"none", nil, "Nothing needs you"},
			{"quiet rows", []projectrows.Row{{}, {}}, "Nothing needs you"},
			{"two", []projectrows.Row{{NeedsOwner: true}, {}, {NeedsOwner: true}}, "2 need you"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := allProjectsSummary(tc.rows); got != tc.want {
					t.Fatalf("summary = %q, want %q", got, tc.want)
				}
			})
		}
	})
	t.Run("progress", func(t *testing.T) {
		if got := allProjectsProgress(projectrows.Row{Done: 2, Total: 5}); got != "2/5" {
			t.Fatalf("progress = %q, want 2/5", got)
		}
		if got := allProjectsProgress(projectrows.Row{}); got != "-" {
			t.Fatalf("progress with no tasks = %q, want -", got)
		}
	})
}

// TestAllProjectsWindow pins the scroll: the list moves only when the cursor
// would leave it, and never past the end. The "cursor past the page" row is
// load-bearing: without it the cursor walks off the bottom of the screen.
func TestAllProjectsWindow(t *testing.T) {
	cases := []struct {
		name               string
		cursor, page, n    int
		wantStart, wantEnd int
	}{
		{"all fit", 2, 10, 5, 0, 5},
		{"cursor on the first page", 3, 4, 10, 0, 4},
		{"cursor past the page", 6, 4, 10, 3, 7},
		{"cursor on the last row", 9, 4, 10, 6, 10},
		{"no rows", 0, 4, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e := allProjectsWindow(tc.cursor, tc.page, tc.n)
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
	m, cmd := pressAction(t, m, keyRune('A'))
	if !m.allProjectsOpen {
		t.Fatal("ctrl+] A did not open the all-projects screen")
	}
	for _, msg := range collectMsgs(t, cmd) {
		m, _ = update(t, m, msg)
	}
	if !m.allProjectsKnown {
		t.Fatal("the first read did not land")
	}
	return m
}

// TestGoldenAllProjects pins the screen at both widths with a row in every
// state: at 120 columns one line per project with columns, at 80 two lines
// per project and the list scrolled to its first rows. The frames show the
// rows needing the owner first, the gone and can't-read rows, the inbox
// counts and the plan waiting for approval.
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
				m, _ = pressAction(t, m, keyRune('A'))
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
