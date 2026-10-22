package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/inbox"
)

// buildTabSnap is a build with one task in every ledger status, long titles
// and a detail on every row but one, so a width table has something to drop
// and something to cut.
func buildTabSnap() buildview.Snapshot {
	return buildview.Snapshot{
		Root:     "/repo",
		HasBuild: true,
		Rows: []buildview.Row{
			{ID: "T1.1", Title: "Shared usage wording package", Status: "done", Detail: "PR #12 · 4c30e4a"},
			{ID: "T1.2", Title: "Build snapshot types and task rows", Status: "running", Detail: "t1-2 · executing"},
			{ID: "T2.1", Title: "Build data seam in the dashboard and its real loader", Status: "shipping", Detail: "ci running · PR #14"},
			{ID: "T3.1", Title: "Header segment", Status: "parked", Detail: "waiting on you: q-3"},
			{ID: "T3.2", Title: "Build tab, the b key and paused-for-usage rows", Status: "failed", Detail: "attempt 2"},
			{ID: "T4.1", Title: "Inbox overlay", Status: "pending", Detail: "ready"},
			{ID: "T5.1", Title: "Old walk-through", Status: "superseded"},
		},
		Done:         1,
		Total:        7,
		Current:      "T1.2",
		CurrentPhase: "executing",
	}
}

// plain strips the styling from a set of lines.
func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = xansi.Strip(ln)
	}
	return out
}

// buildRowLine returns the line for a task id, or "" when there is none. A
// task row starts with the cursor's two-cell gutter (buildRowGutter), which
// is taken off here so the rows compare as their columns.
func buildRowLine(lines []string, id string) string {
	for _, ln := range lines {
		ln = strings.TrimRight(strings.TrimPrefix(ln, "  "), " ")
		if strings.HasPrefix(ln, id+" ") || ln == id {
			return ln
		}
	}
	return ""
}

// TestBuildTabLinesWidths is the layout table for the task rows. Every row of
// it checks the same three things at one width, and each is load-bearing:
//
//   - every line fits the width. The detail pane soft-wraps what does not, so
//     an over-wide row would fold into two and the tab would stop being one
//     line per task.
//   - the id and the status are on every task's line. They are the two facts
//     the tab exists for, and the only ones that may never be dropped.
//   - no line holds both a cut title and its detail. The detail goes first;
//     a title is cut only once the detail is gone.
//
// The widths are the detail pane's at a wide terminal, a default one, a narrow
// one, and the floor. wantDetails says how many rows still show a detail, so
// "detail gone" is a number the table states and not something inferred.
func TestBuildTabLinesWidths(t *testing.T) {
	snap := buildTabSnap()
	cases := []struct {
		width       int
		wantDetails int
		// aligned is true when every row fits whole, so the details form a
		// column.
		aligned bool
	}{
		{width: 120, wantDetails: 6, aligned: true},
		{width: 80, wantDetails: 5},
		{width: 50, wantDetails: 1},
		{width: 30, wantDetails: 0},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("width %d", tc.width), func(t *testing.T) {
			lines := buildTabLines(defaultLeaderKey, snap, PlanStatus{}, tc.width)
			for _, ln := range lines {
				if w := lipgloss.Width(ln); w > tc.width {
					t.Errorf("line is %d cells, over the width %d: %q", w, tc.width, xansi.Strip(ln))
				}
			}
			text := plain(lines)
			details, starts := 0, map[int]bool{}
			for _, r := range snap.Rows {
				ln := buildRowLine(text, r.ID)
				if ln == "" {
					t.Fatalf("no line for %s in:\n%s", r.ID, strings.Join(text, "\n"))
				}
				if !strings.Contains(ln, r.Status) {
					t.Errorf("%s: the status %q is missing from %q", r.ID, r.Status, ln)
				}
				cut := strings.Contains(ln, "…")
				hasDetail := r.Detail != "" && strings.Contains(ln, r.Detail)
				if hasDetail {
					details++
					starts[lipgloss.Width(ln[:strings.Index(ln, r.Detail)])] = true
				}
				if cut && hasDetail {
					t.Errorf("%s: the title was cut while the detail still shows: %q", r.ID, ln)
				}
				if !cut && !hasDetail && !strings.Contains(ln, r.Title) {
					t.Errorf("%s: the title is neither whole nor cut in %q", r.ID, ln)
				}
			}
			if details != tc.wantDetails {
				t.Errorf("%d rows show a detail, want %d:\n%s", details, tc.wantDetails, strings.Join(text, "\n"))
			}
			if tc.aligned && len(starts) != 1 {
				t.Errorf("the details start at %d different columns, want one column", len(starts))
			}
		})
	}
}

// TestBuildTabSummary is the table for the block above the task rows: one row
// per fact the block can state. The load-bearing rows:
//
//   - "a plan that cannot be read" must show the reason and NO task rows and
//     no count. The loader has neither to give, and "0/0 done" under a load
//     error would read as an empty plan.
//   - "blocking items" against "open items only": the first says the build is
//     waiting on the owner, the second does not, and the wording is the only
//     thing that tells them apart.
//   - "one item" and "one unreadable file" pin the singular, which a plain
//     Sprintf of the count gets wrong.
func TestBuildTabSummary(t *testing.T) {
	item := func(id string, blocking, answered bool) inbox.Item {
		it := inbox.Item{ID: id, Blocking: blocking}
		if answered {
			it.Answer = "yes"
		}
		return it
	}
	base := func(edit func(*buildview.Snapshot)) buildview.Snapshot {
		s := buildTabSnap()
		if edit != nil {
			edit(&s)
		}
		return s
	}
	cases := []struct {
		name    string
		snap    buildview.Snapshot
		want    []string
		wantNot []string
	}{
		{
			name:    "count and the current task with its phase",
			snap:    base(nil),
			want:    []string{"1/7 done · T1.2: executing", "T4.1"},
			wantNot: []string{"paused:", "stopped:", "inbox", "could not be read"},
		},
		{
			name:    "nothing running",
			snap:    base(func(s *buildview.Snapshot) { s.Current, s.CurrentPhase = "", "" }),
			want:    []string{"1/7 done"},
			wantNot: []string{"1/7 done ·"},
		},
		{
			name: "a current task with no phase known",
			snap: base(func(s *buildview.Snapshot) { s.CurrentPhase = "" }),
			want: []string{"1/7 done · T1.2"},
			wantNot: []string{
				"T1.2:",
			},
		},
		{
			name: "paused",
			snap: base(func(s *buildview.Snapshot) { s.Paused = true }),
			want: []string{"paused: p or afk resume starts it again"},
		},
		{
			name: "stopped with the task and the reason",
			snap: base(func(s *buildview.Snapshot) {
				s.Stopped, s.StopTask, s.StopReason = true, "T3.2", "the checks failed twice"
			}),
			want: []string{"stopped: T3.2 — the checks failed twice"},
		},
		{
			name: "a plan that cannot be read",
			snap: buildview.Snapshot{
				Root: "/repo", HasBuild: true,
				Problem: "The master plan at /repo/docs/PLAN.md could not be read.",
				Items:   []inbox.Item{item("q-1", true, false)},
			},
			want:    []string{"The master plan at /repo/docs/PLAN.md could not be read.", "1 item needs you · i: inbox"},
			wantNot: []string{"done", "no tasks", "T1.1"},
		},
		{
			name: "blocking items",
			snap: base(func(s *buildview.Snapshot) {
				s.Items = []inbox.Item{item("q-1", true, false), item("q-2", true, false), item("n-1", false, false)}
			}),
			want:    []string{"2 items need you · i: inbox"},
			wantNot: []string{"open items"},
		},
		{
			name: "open items only",
			snap: base(func(s *buildview.Snapshot) {
				s.Items = []inbox.Item{item("n-1", false, false), item("n-2", false, false), item("q-1", true, true)}
			}),
			want:    []string{"2 open items · i: inbox"},
			wantNot: []string{"need you"},
		},
		{
			name: "one open item",
			snap: base(func(s *buildview.Snapshot) { s.Items = []inbox.Item{item("n-1", false, false)} }),
			want: []string{"1 open item · i: inbox"},
		},
		{
			name:    "every item answered",
			snap:    base(func(s *buildview.Snapshot) { s.Items = []inbox.Item{item("q-1", true, true)} }),
			wantNot: []string{"inbox"},
		},
		{
			name: "one unreadable inbox file",
			snap: base(func(s *buildview.Snapshot) { s.Unreadable = 1 }),
			want: []string{"1 inbox file could not be read"},
		},
		{
			name: "several unreadable inbox files",
			snap: base(func(s *buildview.Snapshot) { s.Unreadable = 3 }),
			want: []string{"3 inbox files could not be read"},
		},
		{
			name: "a plan with no tasks",
			snap: buildview.Snapshot{Root: "/repo", HasBuild: true},
			want: []string{"0/0 done", "The master plan has no tasks."},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(plain(buildTabLines(defaultLeaderKey, tc.snap, PlanStatus{}, 100)), "\n")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}

// TestBuildTabProblemWraps pins that a long problem sentence is wrapped and
// not cut. It holds the absolute path of the file to fix, which sits at the
// end of the sentence, where a cut would take it.
func TestBuildTabProblemWraps(t *testing.T) {
	snap := buildview.Snapshot{
		HasBuild: true,
		Problem:  "The ledger at /home/someone/code/project/.afk/build/ledger.json is damaged and could not be read.",
	}
	lines := plain(buildTabLines(defaultLeaderKey, snap, PlanStatus{}, 40))
	if len(lines) < 2 {
		t.Fatalf("the problem was not wrapped: %q", lines)
	}
	for _, ln := range lines {
		if lipgloss.Width(ln) > 40 {
			t.Errorf("line over 40 cells: %q", ln)
		}
	}
	if joined := strings.ReplaceAll(strings.Join(lines, ""), " ", ""); !strings.Contains(joined, "ledger.json") || !strings.Contains(joined, "couldnotberead.") {
		t.Errorf("the sentence lost its end: %q", lines)
	}
}

// withBuild hands the model a build snapshot the way the runtime does.
func withBuild(t *testing.T, m Model, snap buildview.Snapshot) Model {
	t.Helper()
	m, _ = update(t, m, buildLoadedMsg{snap: snap, gen: m.projectGen})
	return m
}

// openBuildSection reaches the project view's Build section the way a person
// does since T16.4: `2` gives it the keyboard. It fails the test if the
// section is not focused, so a later assertion is about the section.
func openBuildSection(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = update(t, m, keyRune('2'))
	if !m.buildSectionFocused() {
		t.Fatalf("setup: section %v, focus %v, level %v; want the Build section with the keyboard", m.focusedSection(), m.focus, m.level())
	}
	return m
}

// TestBuildSectionByKey drives the way to the Build section since T16.4 made
// the overview's Build tab a section of the project view. The load-bearing
// rows:
//
//   - "2 reaches Build": the section is reachable, with the project view
//     keeping the keyboard (no overview, the normal mode) so esc still goes to
//     All projects and the move keys move the task cursor.
//   - "reads the same on every row": the section is about the project, so a
//     different selected session must not change it (with no ledger
//     session to link, there is no linked row to differ).
//   - the "no build" rows: the section says why it has no rows instead of
//     drawing an empty box, and the overview has no Build tab any more.
//   - "a plan that cannot be read" shows the reason in the section.
//   - "a live agent pane" is hidden by d, never closed. Closing it would
//     cost the owner the session they were watching, for a look at a list.
func TestBuildSectionByKey(t *testing.T) {
	t.Run("2 reaches Build with the project view keeping the keyboard", func(t *testing.T) {
		m := withBuild(t, testModel(sess("alpha"), sess("beta")), buildTabSnap())
		m.deps.LoadAllProjects = func() AllProjects { return AllProjects{} }
		m = openBuildSection(t, m)
		if m.focus != focusList {
			t.Fatalf("focus=%v, want the project view's", m.focus)
		}
		if got := activeInputMode(m.modeSnapshot()); got != inputNormal {
			t.Fatalf("mode = %v, want the project view's normal mode", got)
		}
		frame := render(m)
		for _, want := range []string{"2 Build", "1/7 done", "T4.1"} {
			if !strings.Contains(frame, want) {
				t.Errorf("the frame is missing %q:\n%s", want, frame)
			}
		}
		m, _ = update(t, m, keyEsc)
		if m.level() != levelAllProjects {
			t.Errorf("esc from the Build section went to %v, want All projects", m.level())
		}
	})

	t.Run("reads the same on every row", func(t *testing.T) {
		m := withBuild(t, testModel(sess("alpha"), sess("beta")), buildTabSnap())
		a, _ := m.selected()
		first := m.renderBuildSection(60, 20, false, a, true)
		m, _ = update(t, m, keyRune('j'))
		b, _ := m.selected()
		if b.Name != "beta" {
			t.Fatalf("fixture broken: selected %q, want beta", b.Name)
		}
		if got := m.renderBuildSection(60, 20, false, b, true); got != first {
			t.Errorf("the Build section changed with the selection:\n%s\n--- was\n%s", xansi.Strip(got), xansi.Strip(first))
		}
	})

	t.Run("a placeholder row still shows the build", func(t *testing.T) {
		pending := Session{Pending: true}
		pending.Name = "being-made"
		m := withBuild(t, testModel(pending), buildTabSnap())
		if frame := render(m); !strings.Contains(frame, "1/7 done") || !strings.Contains(frame, "T4.1") {
			t.Errorf("a placeholder row hid the build:\n%s", frame)
		}
	})

	for _, tc := range []struct {
		name string
		prep func(t *testing.T, m Model) Model
		want string
	}{
		{"no build data yet says it is reading", func(_ *testing.T, m Model) Model { return m }, "Reading the build…"},
		{"no master plan says so", func(t *testing.T, m Model) Model {
			return withBuild(t, m, buildview.Snapshot{Root: "/repo"})
		}, "This project has no master plan."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.prep(t, testModel(sess("alpha")))
			if frame := render(m); !strings.Contains(frame, tc.want) {
				t.Errorf("the Build section does not say %q:\n%s", tc.want, frame)
			}
			m = focusOverview(t, m)
			if body := xansi.Strip(m.detailBody(70)); strings.Contains(body, "Build") {
				t.Errorf("the overview still has a Build tab:\n%s", body)
			}
		})
	}

	t.Run("a plan that cannot be read shows the reason and no rows", func(t *testing.T) {
		problem := "The master plan at /repo/docs/PLAN.md could not be read."
		m := withBuild(t, testModel(sess("alpha")), buildview.Snapshot{Root: "/repo", HasBuild: true, Problem: problem})
		s, _ := m.selected()
		body := xansi.Strip(m.renderBuildSection(120, 20, true, s, true))
		if !strings.Contains(body, problem) {
			t.Errorf("the reason is missing from:\n%s", body)
		}
		for _, bad := range []string{"pending", "no tasks"} {
			if strings.Contains(body, bad) {
				t.Errorf("unexpected %q under a load problem:\n%s", bad, body)
			}
		}
	})

	t.Run("a live agent pane stays drawn under the details, never closed", func(t *testing.T) {
		m := withBuild(t, testModel(runningSess("alpha")), buildTabSnap())
		pane := newFakePane("agent screen")
		m.pane, m.paneName, m.view, m.focus = pane, "alpha", viewAgent, focusRight
		if !m.shouldRenderPane() || m.level() != levelAgent {
			t.Fatal("fixture broken: the pane must be drawn in the Agent")
		}
		m = pressKey(t, m, keyRune('d'))
		if m.pane == nil || pane.closed != 0 {
			t.Fatalf("d closed the pane (closed=%d)", pane.closed)
		}
		if !m.shouldRenderPane() || !m.detailsOpen || m.focus != focusRight {
			t.Fatalf("drawn=%v details=%v focus=%v, want the details over the drawn agent", m.shouldRenderPane(), m.detailsOpen, m.focus)
		}
		if len(pane.keys)+len(pane.texts) != 0 {
			t.Errorf("the leader then d reached the session")
		}
	})
}

// TestOverviewTabKeysAfterBuildLeft pins the overview's tab keys with Build
// gone from its strip (T16.4). The rows are load-bearing for two reasons:
// the digits of the session tabs are what they always were, and while the
// overview has the keyboard tab and the digits are its own and never move the
// project view's section behind it ("2 in the overview is Waves").
func TestOverviewTabKeysAfterBuildLeft(t *testing.T) {
	newModel := func(gs bool) Model {
		m := testModel(sess("alpha"))
		m.stack = stackState{Detected: gs}
		return focusOverview(t, withBuild(t, m, buildTabSnap()))
	}
	press := func(m Model, keys ...tea.Msg) Model {
		for _, k := range keys {
			m, _ = update(t, m, k)
		}
		return m
	}
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	backtab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	cases := []struct {
		name string
		gs   bool
		keys []tea.Msg
		want detailTab
	}{
		{"plain repo: 4 is Diff", false, []tea.Msg{keyRune('4')}, tabDiff},
		{"plain repo: 5 is past the last tab", false, []tea.Msg{keyRune('5')}, tabOverview},
		{"git-spice repo: 5 is Diff", true, []tea.Msg{keyRune('5')}, tabDiff},
		{"shift+tab from the Overview wraps to Diff", false, []tea.Msg{backtab}, tabDiff},
		{"tab from Diff wraps to the Overview", false, []tea.Msg{keyRune('4'), tab}, tabOverview},
		{"2 in the overview is Waves", false, []tea.Msg{keyRune('2')}, tabWaves},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := press(newModel(tc.gs), tc.keys...)
			if got := m.activeDetailTab(); got != tc.want {
				t.Fatalf("active tab = %v, want %v", got, tc.want)
			}
			if got := m.focusedSection(); got != sectionSessions {
				t.Errorf("the overview's keys moved the section behind it to %v", got)
			}
		})
	}
}

// TestBuildSectionDropsOnAProjectSwitch pins that the section goes with the
// project's data and comes back with the new project's. The middle check is
// the load-bearing one: between the switch and the first read the dashboard
// holds no build, and drawing the old project's tasks there would show them
// under the new project's name. The focus goes back to Sessions with them.
func TestBuildSectionDropsOnAProjectSwitch(t *testing.T) {
	m := buildModel(t, nil)
	m.sizeSessionList()
	m = withBuild(t, m, buildTabSnap())
	m.section = sectionBuild

	m, _ = m.switchProject("/beta")
	if m.hasBuild() || m.focusedSection() != sectionSessions {
		t.Fatalf("after the switch hasBuild=%v section=%v, want no build and Sessions", m.hasBuild(), m.focusedSection())
	}
	if frame := render(m); strings.Contains(frame, "T4.1") || strings.Contains(frame, "1/7 done") {
		t.Errorf("the old project's tasks are still drawn after the switch")
	}

	next := buildTabSnap()
	next.Root, next.Done = "/beta", 4
	m = withBuild(t, m, next)
	if !strings.Contains(render(m), "4/7 done") {
		t.Errorf("the section does not show the new project's build")
	}
}

// overviewFrame is m's frame with the details overlay `d` opens over the
// project view and its notice cleared: the Sessions row and the Overview's
// words side by side, which is where both are drawn since the
// project view (T16.4) took the body at Project.
func overviewFrame(t *testing.T, m Model) string {
	t.Helper()
	m = focusOverview(t, m)
	m.clearStatus()
	return render(m)
}

// TestPausedForUsageNeverReadsAsStopped pins plan objective 5. A session the
// daemon paused for usage has no host, which is exactly how a stopped session
// looks, so every place that says "stopped" has to be told. The rows:
//
//   - "paused with a wake time" and "paused with none" are the two wordings.
//   - "not in the ledger's paused set" is the control: without it the test
//     would pass if every dead host were called paused.
//   - "paused in the ledger but running" is the owner having restarted it
//     with `r`: the row must say what the session is doing now.
func TestPausedForUsageNeverReadsAsStopped(t *testing.T) {
	snap := func(paused map[string]string) buildview.Snapshot {
		s := buildTabSnap()
		s.PausedSessions = paused
		return s
	}
	cases := []struct {
		name    string
		sess    Session
		paused  map[string]string
		want    []string
		wantNot []string
		notice  string
	}{
		{
			name:    "paused with a wake time",
			sess:    stoppedSess("alpha"),
			paused:  map[string]string{"alpha": "Mon 5 Jan 15:04"},
			want:    []string{"alpha  paused", "paused for usage until Mon 5 Jan 15:04"},
			wantNot: []string{"stopped"},
			notice:  "is paused for usage",
		},
		{
			name:    "paused with no wake time",
			sess:    stoppedSess("alpha"),
			paused:  map[string]string{"alpha": ""},
			want:    []string{"alpha  paused", "paused for usage"},
			wantNot: []string{"stopped", "until"},
			notice:  "is paused for usage",
		},
		{
			name:    "not in the ledger's paused set",
			sess:    stoppedSess("alpha"),
			paused:  map[string]string{"other": ""},
			want:    []string{"○ stopped", "◐ executing"},
			wantNot: []string{"paused"},
			notice:  "is stopped",
		},
		{
			name:    "paused in the ledger but running",
			sess:    runningSess("alpha"),
			paused:  map[string]string{"alpha": ""},
			want:    []string{"◐ running", "◐ executing"},
			wantNot: []string{"paused", "stopped"},
			notice:  "is stopped",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := withBuild(t, testModel(tc.sess), snap(tc.paused))
			frame := overviewFrame(t, m)
			for _, w := range tc.want {
				if !strings.Contains(frame, w) {
					t.Errorf("frame is missing %q:\n%s", w, frame)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(frame, w) {
					t.Errorf("frame has %q:\n%s", w, frame)
				}
			}
			s, _ := m.selected()
			if got := stoppedText(s); got != tc.notice {
				t.Errorf("stoppedText = %q, want %q", got, tc.notice)
			}
		})
	}

	t.Run("a session load after the build keeps the row paused", func(t *testing.T) {
		m := withBuild(t, testModel(stoppedSess("alpha")), snap(map[string]string{"alpha": ""}))
		m.setSessions([]Session{stoppedSess("alpha")})
		if frame := overviewFrame(t, m); !strings.Contains(frame, "paused for usage") {
			t.Errorf("fresh rows from discovery lost the pause:\n%s", frame)
		}
	})

	t.Run("the pause clears when the ledger drops it", func(t *testing.T) {
		m := withBuild(t, testModel(stoppedSess("alpha")), snap(map[string]string{"alpha": ""}))
		m = withBuild(t, m, snap(nil))
		if frame := overviewFrame(t, m); strings.Contains(frame, "paused") || !strings.Contains(frame, "○ stopped") {
			t.Errorf("the row still reads as paused after the ledger cleared it:\n%s", frame)
		}
	})

	t.Run("the keys on a paused row are unchanged", func(t *testing.T) {
		m := withBuild(t, testModel(stoppedSess("alpha")), snap(map[string]string{"alpha": ""}))
		if !m.restartKeyLive() {
			t.Errorf("r must still restart a row paused for usage (assumption A14)")
		}
	})

	t.Run("the narrow list abbreviates the label", func(t *testing.T) {
		s := stoppedSess("alpha")
		s.UsagePaused = true
		row := xansi.Strip(renderSessionRow(s, false, 26, time.Unix(1_000_000, 0)))
		if !strings.Contains(row, "PAUSE") || strings.Contains(row, "PAUSED") || lipgloss.Width(row) > 26 {
			t.Errorf("narrow row = %q, want the five-cell label within 26 cells", row)
		}
	})
}
