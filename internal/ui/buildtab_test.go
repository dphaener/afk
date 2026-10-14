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

// buildRowLine returns the line for a task id, or "" when there is none.
func buildRowLine(lines []string, id string) string {
	for _, ln := range lines {
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
			lines := buildTabLines(snap, PlanStatus{}, tc.width)
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
			want: []string{"paused: U or afk resume starts it again"},
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
			want:    []string{"The master plan at /repo/docs/PLAN.md could not be read.", "1 item needs you · ctrl+space i: inbox"},
			wantNot: []string{"done", "no tasks", "T1.1"},
		},
		{
			name: "blocking items",
			snap: base(func(s *buildview.Snapshot) {
				s.Items = []inbox.Item{item("q-1", true, false), item("q-2", true, false), item("n-1", false, false)}
			}),
			want:    []string{"2 items need you · ctrl+space i: inbox"},
			wantNot: []string{"open items"},
		},
		{
			name: "open items only",
			snap: base(func(s *buildview.Snapshot) {
				s.Items = []inbox.Item{item("n-1", false, false), item("n-2", false, false), item("q-1", true, true)}
			}),
			want:    []string{"2 open items · ctrl+space i: inbox"},
			wantNot: []string{"need you"},
		},
		{
			name: "one open item",
			snap: base(func(s *buildview.Snapshot) { s.Items = []inbox.Item{item("n-1", false, false)} }),
			want: []string{"1 open item · ctrl+space i: inbox"},
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
			got := strings.Join(plain(buildTabLines(tc.snap, PlanStatus{}, 100)), "\n")
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
	lines := plain(buildTabLines(snap, PlanStatus{}, 40))
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

var keyB = keyRune('b')

// TestBuildKey drives `b` through Update. The load-bearing rows:
//
//   - "no session selected" is assumption A2 of the plan: a build with
//     nothing running is when the task list matters most, and the detail pane
//     used to have nothing to draw there.
//   - the two "no build" rows must leave focus, view and tab exactly as they
//     were. A key that moved the keyboard to a tab that is not drawn would
//     strand it.
//   - "a plan that cannot be read" still opens the tab: the reason is on it,
//     and hiding the tab would hide the reason.
//   - "a live agent pane" is hidden, never closed. Closing it would cost the
//     owner the session they were watching, for a look at a list.
func TestBuildKey(t *testing.T) {
	t.Run("from the list focuses the right side on Build", func(t *testing.T) {
		m := withBuild(t, testModel(sess("alpha"), sess("beta")), buildTabSnap())
		m, cmd := pressAction(t, m, keyB)
		if cmd != nil {
			t.Fatalf("b returned a command (%T); it must only change what is shown", cmd())
		}
		if m.focus != focusRight || m.view != viewOverview {
			t.Fatalf("focus=%v view=%v, want the right side on the overview", m.focus, m.view)
		}
		if got := activeInputMode(m.modeSnapshot()); got != inputDetail {
			t.Fatalf("mode = %v, want detail so esc and ctrl+\\ release it", got)
		}
		if got := m.activeDetailTab(); got != tabBuild {
			t.Fatalf("active tab = %v, want Build", got)
		}
		// The body is read directly, not off the frame: focusing the right side
		// raises a toast that floats over the pane's top corner.
		body := xansi.Strip(m.detailBody(70))
		for _, want := range []string{"Build", "1/7 done", "T4.1", "tab/1-5: switch"} {
			if !strings.Contains(body, want) {
				t.Errorf("the pane is missing %q:\n%s", want, body)
			}
		}
		if frame := render(m); !strings.Contains(frame, "─ Build ─") {
			t.Errorf("the box is not titled Build:\n%s", frame)
		}
		m, _ = update(t, m, keyEsc)
		if m.focus != focusList {
			t.Errorf("esc left focus on %v, want the list", m.focus)
		}
		if got := m.activeDetailTab(); got != tabBuild {
			t.Errorf("releasing the focus changed the tab to %v", got)
		}
	})

	t.Run("reads the same on every row", func(t *testing.T) {
		m := withBuild(t, testModel(sess("alpha"), sess("beta")), buildTabSnap())
		m, _ = pressAction(t, m, keyB)
		first := m.detailBody(70)
		m, _ = update(t, m, keyEsc)
		m, _ = update(t, m, keyRune('j'))
		if s, _ := m.selected(); s.Name != "beta" {
			t.Fatalf("fixture broken: selected %q, want beta", s.Name)
		}
		if got := m.detailBody(70); got != first {
			t.Errorf("the Build tab changed with the selection:\n%s\n--- was\n%s", xansi.Strip(got), xansi.Strip(first))
		}
	})

	t.Run("no session selected still shows the tab", func(t *testing.T) {
		m := withBuild(t, testModel(), buildTabSnap())
		if _, ok := m.selected(); ok {
			t.Fatal("fixture broken: a session is selected")
		}
		m, _ = pressAction(t, m, keyB)
		if m.focus != focusRight || m.activeDetailTab() != tabBuild {
			t.Fatalf("focus=%v tab=%v, want the right side on Build", m.focus, m.activeDetailTab())
		}
		frame := render(m)
		for _, want := range []string{"Overview", "Build", "1/7 done", "T1.1", "superseded"} {
			if !strings.Contains(frame, want) {
				t.Errorf("frame is missing %q", want)
			}
		}
		if strings.Contains(frame, "Select a session to see details.") {
			t.Errorf("the Build tab shows the no-selection message")
		}
	})

	t.Run("a placeholder or fault row still shows the tab", func(t *testing.T) {
		pending := Session{Pending: true}
		pending.Name = "being-made"
		m := withBuild(t, testModel(pending), buildTabSnap())
		m, _ = pressAction(t, m, keyB)
		body := xansi.Strip(m.detailBody(70))
		if !strings.Contains(body, "1/7 done") || strings.Contains(body, "creating session") {
			t.Errorf("a placeholder row hid the Build tab:\n%s", body)
		}
		m, _ = update(t, m, keyRune('1'))
		if body = xansi.Strip(m.detailBody(70)); !strings.Contains(body, "creating session") || !strings.Contains(body, "Build") {
			t.Errorf("leaving Build on a placeholder row must show its message under the strip:\n%s", body)
		}
	})

	for _, tc := range []struct {
		name string
		prep func(t *testing.T, m Model) Model
	}{
		{"no build data yet shows the notice and changes nothing", func(_ *testing.T, m Model) Model { return m }},
		{"no master plan shows the notice and changes nothing", func(t *testing.T, m Model) Model {
			return withBuild(t, m, buildview.Snapshot{Root: "/repo"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.prep(t, testModel(sess("alpha")))
			m.detailTab = tabWaves
			before := render(m)
			// The handler directly: the dialog dims the build row with no build
			// and runs nothing, so only this call reaches the refusal.
			m, cmd := runAction(m, actionBuild)
			if cmd != nil {
				t.Fatalf("b returned a command (%T)", cmd())
			}
			if m.status != noBuildNotice {
				t.Errorf("notice = %q, want %q", m.status, noBuildNotice)
			}
			if m.focus != focusList || m.view != viewAgent || m.detailTab != tabWaves {
				t.Errorf("focus=%v view=%v tab=%v moved; the key must change nothing", m.focus, m.view, m.detailTab)
			}
			if strings.Contains(before, "Build") || slicesContainsTab(visibleDetailTabs(m.stack, m.hasBuild()), tabBuild) {
				t.Errorf("a project with no build shows a Build tab")
			}
		})
	}

	t.Run("a plan that cannot be read shows the reason and no rows", func(t *testing.T) {
		problem := "The master plan at /repo/docs/PLAN.md could not be read."
		m := withBuild(t, testModel(sess("alpha")), buildview.Snapshot{Root: "/repo", HasBuild: true, Problem: problem})
		m, _ = pressAction(t, m, keyB)
		if m.activeDetailTab() != tabBuild {
			t.Fatalf("active tab = %v, want Build", m.activeDetailTab())
		}
		body := xansi.Strip(m.detailBody(70))
		if !strings.Contains(body, problem) {
			t.Errorf("the reason is missing from:\n%s", body)
		}
		for _, bad := range []string{"done", "pending", "no tasks"} {
			if strings.Contains(body, bad) {
				t.Errorf("unexpected %q under a load problem:\n%s", bad, body)
			}
		}
	})

	t.Run("a live agent pane is hidden behind the overview, never closed", func(t *testing.T) {
		m := withBuild(t, testModel(runningSess("alpha")), buildTabSnap())
		pane := newFakePane("agent screen")
		m.pane, m.paneName, m.view = pane, "alpha", viewAgent
		if !m.shouldRenderPane() || m.focus != focusList {
			t.Fatal("fixture broken: the pane must be drawn with the list focused")
		}
		m, _ = pressAction(t, m, keyB)
		if m.pane == nil || pane.closed != 0 {
			t.Fatalf("b closed the pane (closed=%d)", pane.closed)
		}
		if m.shouldRenderPane() || m.view != viewOverview || m.focus != focusRight {
			t.Fatalf("view=%v focus=%v, want the overview focused over the hidden pane", m.view, m.focus)
		}
		if !strings.Contains(m.status, "alpha still running") {
			t.Errorf("notice = %q, want it to say the session is still running", m.status)
		}
		if len(pane.keys)+len(pane.texts) != 0 {
			t.Errorf("b reached the session from the list")
		}
	})

	t.Run("from a focused overview jumps to Build", func(t *testing.T) {
		m := focusOverview(t, withBuild(t, testModel(sess("alpha")), buildTabSnap()))
		m, _ = pressAction(t, m, keyB)
		if m.activeDetailTab() != tabBuild || m.focus != focusRight {
			t.Fatalf("tab=%v focus=%v, want Build with the right side still focused", m.activeDetailTab(), m.focus)
		}
	})
}

func slicesContainsTab(tabs []detailTab, want detailTab) bool {
	for _, t := range tabs {
		if t == want {
			return true
		}
	}
	return false
}

// TestBuildTabReachedAndLeftByTheTabKeys pins the Build tab's place in the
// strip through Update. The digit rows are load-bearing: Build is `5` in a
// plain repo and `6` in a git-spice one, and the digit of every tab before it
// is what it was without a build.
func TestBuildTabReachedAndLeftByTheTabKeys(t *testing.T) {
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
		{"plain repo: 5 is Build", false, []tea.Msg{keyRune('5')}, tabBuild},
		{"plain repo: 4 is still Diff", false, []tea.Msg{keyRune('4')}, tabDiff},
		{"plain repo: 6 is past the last tab", false, []tea.Msg{keyRune('6')}, tabOverview},
		{"git-spice repo: 6 is Build", true, []tea.Msg{keyRune('6')}, tabBuild},
		{"git-spice repo: 5 is still Diff", true, []tea.Msg{keyRune('5')}, tabDiff},
		{"shift+tab from the Overview wraps to Build", false, []tea.Msg{backtab}, tabBuild},
		{"tab from Build wraps to the Overview", false, []tea.Msg{keyRune('5'), tab}, tabOverview},
		{"shift+tab from Build is Diff", false, []tea.Msg{keyRune('5'), backtab}, tabDiff},
		{"a digit leaves Build", true, []tea.Msg{leaderKeyPress, keyB, keyRune('2')}, tabWaves},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := press(newModel(tc.gs), tc.keys...).activeDetailTab(); got != tc.want {
				t.Fatalf("active tab = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildTabDropsOnAProjectSwitch pins that the tab goes with the project's
// data and comes back with the new project's. The middle check is the
// load-bearing one: between the switch and the first read the dashboard holds
// no build, and drawing the old project's tasks there would show them under
// the new project's name.
func TestBuildTabDropsOnAProjectSwitch(t *testing.T) {
	m := buildModel(t, nil)
	m.sizeSessionList()
	m = withBuild(t, m, buildTabSnap())
	m, _ = pressAction(t, m, keyB)
	if m.activeDetailTab() != tabBuild {
		t.Fatalf("fixture broken: active tab = %v", m.activeDetailTab())
	}

	m, _ = m.switchProject("/beta")
	if m.hasBuild() || m.activeDetailTab() != tabOverview {
		t.Fatalf("after the switch hasBuild=%v tab=%v, want no build and the Overview", m.hasBuild(), m.activeDetailTab())
	}
	if frame := render(m); strings.Contains(frame, "T4.1") || strings.Contains(frame, "1/7 done") {
		t.Errorf("the old project's tasks are still drawn after the switch")
	}

	next := buildTabSnap()
	next.Root, next.Done = "/beta", 4
	m = withBuild(t, m, next)
	if m.activeDetailTab() != tabBuild {
		t.Fatalf("the Build tab did not come back with the new project's data (tab = %v)", m.activeDetailTab())
	}
	if !strings.Contains(xansi.Strip(m.detailBody(70)), "4/7 done") {
		t.Errorf("the tab does not show the new project's build")
	}
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
			want:    []string{"PAUSED", "paused for usage until Mon 5 Jan 15:04"},
			wantNot: []string{"host stopped", "EXECUTING"},
			notice:  "is paused for usage",
		},
		{
			name:    "paused with no wake time",
			sess:    stoppedSess("alpha"),
			paused:  map[string]string{"alpha": ""},
			want:    []string{"PAUSED", "paused for usage"},
			wantNot: []string{"host stopped", "until"},
			notice:  "is paused for usage",
		},
		{
			name:    "not in the ledger's paused set",
			sess:    stoppedSess("alpha"),
			paused:  map[string]string{"other": ""},
			want:    []string{"host stopped", "EXECUTING"},
			wantNot: []string{"PAUSED", "paused for usage"},
			notice:  "is stopped",
		},
		{
			name:    "paused in the ledger but running",
			sess:    runningSess("alpha"),
			paused:  map[string]string{"alpha": ""},
			want:    []string{"host running", "EXECUTING"},
			wantNot: []string{"PAUSED", "paused for usage", "host stopped"},
			notice:  "is stopped",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := withBuild(t, testModel(tc.sess), snap(tc.paused))
			frame := render(m)
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
		if frame := render(m); !strings.Contains(frame, "PAUSED") {
			t.Errorf("fresh rows from discovery lost the pause:\n%s", frame)
		}
	})

	t.Run("the pause clears when the ledger drops it", func(t *testing.T) {
		m := withBuild(t, testModel(stoppedSess("alpha")), snap(map[string]string{"alpha": ""}))
		m = withBuild(t, m, snap(nil))
		if frame := render(m); strings.Contains(frame, "PAUSED") || !strings.Contains(frame, "host stopped") {
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

// TestBuildKeyIsInTheLeaderTable is the proof that the actions dialog lists
// the build tasks under ctrl+space b and that the key runs it. The dialog's
// rows come from the leader table, so this fails if the row or its help line
// is ever removed. The short footer line must NOT grow: it is at its
// measured-width cap.
func TestBuildKeyIsInTheLeaderTable(t *testing.T) {
	m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
	short := xansi.Strip(m.renderHelp())
	if strings.Contains(short, "build tasks") {
		t.Errorf("the short footer line grew: %q", short)
	}
	rows := leaderRows(m.leaderConditions())
	row, ok := leaderRowFor(rows, actionBuild)
	if !ok {
		t.Fatal("the dialog does not list the build tasks")
	}
	if row.keyLabel != "b" || row.name != "build tasks" || row.blocked != "" {
		t.Errorf("row = %+v, want a runnable \"build tasks\" under b", row)
	}
	if got := leaderKeyDecision(leaderPress("b"), "", rows, 0, false); got.kind != leaderRun || got.row.action != actionBuild {
		t.Errorf("ctrl+space b = %v %v, want the build action", got.kind, got.row.action)
	}
}
