package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/project"
)

// planCheckStatusErrors is a plan with two errors and a warning, listed in
// plan order with the warning first, so a frame that kept plan order across
// levels would show it above the errors.
func planCheckStatusErrors() PlanStatus {
	return PlanStatus{
		Session: true, Mode: "extend", Phase: "rounds",
		PlanExists: true, PlanPath: "docs/PLAN.md",
		Summary: PlanSummary{Milestones: 2, Tasks: 5, OwnerTasks: 1, OpenQuestions: 0, Errors: 2, Warnings: 1},
		Findings: []PlanFinding{
			{Level: "warning", Line: 3, Rule: "open-question", Message: "the plan has an open question"},
			{Level: "error", Line: 10, Task: "T1.1", Rule: "missing-acceptance", Message: "the task has no acceptance criteria"},
			{Level: "error", Rule: "no-milestone", Message: "the plan has no milestone heading"},
		},
	}
}

// planCheckStatusWarnings is a plan whose only findings are warnings.
func planCheckStatusWarnings() PlanStatus {
	s := planCheckStatusErrors()
	s.Summary.Errors = 0
	s.Findings = s.Findings[:1]
	return s
}

// planCheckStatusClean is a plan the checker found nothing wrong with.
func planCheckStatusClean() PlanStatus {
	s := planCheckStatusErrors()
	s.Summary.Errors, s.Summary.Warnings = 0, 0
	s.Findings = nil
	return s
}

// withPlanStatus wires a stand-in seam that answers s and lands one read of
// it, the way the refresh does. The seam is never called by anything under
// test here; it is wired because the action is dimmed without it.
func withPlanStatus(t *testing.T, m Model, s PlanStatus) Model {
	t.Helper()
	m.deps.LoadPlanStatus = func(string) PlanStatus { return s }
	m, _ = update(t, m, planStatusLoadedMsg{status: s, gen: m.projectGen})
	return m
}

// planCheckSized is testModel at a terminal size, laid out as a resize lays it out.
func planCheckSized(t *testing.T, w, h int) Model {
	t.Helper()
	m := testModel(sess("a"))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// TestLeaderPlanCheckWordsAreUnique pins the words the row is found by.
// Load-bearing: "errors" is the query the dialog is reached with (its name
// starts with c, clean's key), so a second row saying it would leave two rows
// and enter would run whichever is first; "warnings" is held unique too, for
// a reader who types it after a space.
func TestLeaderPlanCheckWordsAreUnique(t *testing.T) {
	for _, word := range []string{"errors", "warnings"} {
		t.Run(word, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(leaderHelp[actionPlanCheck]), word) {
				t.Fatalf("the check-the-plan help %q does not say %q", leaderHelp[actionPlanCheck], word)
			}
			for _, e := range leaderTable {
				if e.action == actionPlanCheck {
					continue
				}
				hay := strings.ToLower(leaderNames[e.action] + " " + leaderHelp[e.action] + " " + e.key)
				if strings.Contains(hay, word) {
					t.Errorf("%v also says %q: %q", e.action, word, hay)
				}
			}
		})
	}
}

// TestPlanCheckDialogOpensTheOverlay walks the gesture: ctrl+space, "errors",
// enter. Typing the word must leave exactly the one row, and enter must open
// the overlay with the keyboard on it.
func TestPlanCheckDialogOpensTheOverlay(t *testing.T) {
	m := withPlanStatus(t, testModel(sess("a")), planCheckStatusErrors())
	m, _ = update(t, m, leaderKeyPress)
	m = typeText(t, m, "errors")
	if v := m.leader.visible(); len(v) != 1 || v[0].action != actionPlanCheck {
		t.Fatalf("\"errors\" leaves %d rows, want only check the plan", len(v))
	}
	m, cmd := update(t, m, keyEnter)
	if !m.planCheckOpen {
		t.Fatal("enter did not open the overlay")
	}
	if cmd != nil {
		t.Error("opening the overlay issued a command; it must read nothing")
	}
	if got := activeInputMode(m.modeSnapshot()); got != inputPlanCheck {
		t.Fatalf("mode = %v, want %v", got, inputPlanCheck)
	}
}

// TestPlanCheckOverlayFrames pins the overlay's body at both sizes for each
// shape of result. Load-bearing rows: "errors and warnings" (errors must come
// before the warning that precedes them in the plan), and the 80x24 rows,
// where findings wrap and a line wider than the box would break the frame.
func TestPlanCheckOverlayFrames(t *testing.T) {
	cases := []struct {
		name    string
		status  PlanStatus
		summary string
		// order is substrings that must appear in this order in the body.
		order []string
	}{
		{
			name:    "zero findings",
			status:  planCheckStatusClean(),
			summary: "docs/PLAN.md: no problems found",
		},
		{
			name:    "warnings only",
			status:  planCheckStatusWarnings(),
			summary: "docs/PLAN.md: 0 errors, 1 warning",
			order:   []string{"docs/PLAN.md:3: warning:"},
		},
		{
			name:    "errors and warnings",
			status:  planCheckStatusErrors(),
			summary: "docs/PLAN.md: 2 errors, 1 warning",
			order:   []string{"docs/PLAN.md:10: error: T1.1:", "docs/PLAN.md: error:", "docs/PLAN.md:3: warning:"},
		},
	}
	sizes := []struct{ w, h int }{{80, 24}, {120, 40}}
	for _, tc := range cases {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", tc.name, sz.w, sz.h), func(t *testing.T) {
				m := withPlanStatus(t, planCheckSized(t, sz.w, sz.h), tc.status)
				m, _ = runPlanAction(t, m, "errors")
				if !m.planCheckOpen {
					t.Fatal("the overlay did not open")
				}
				cw, _ := m.planCheckSize()
				box := xansi.Strip(m.renderPlanCheckModal())
				for i, ln := range strings.Split(box, "\n") {
					if w := lipgloss.Width(ln); w > cw+4 {
						t.Errorf("box line %d is %d wide, the box is %d: %q", i, w, cw+4, ln)
					}
				}
				for i, ln := range strings.Split(render(m), "\n") {
					if w := lipgloss.Width(ln); w > sz.w {
						t.Errorf("frame line %d is %d wide, the terminal %d", i, w, sz.w)
					}
				}
				if !strings.Contains(box, "Plan check · docs/PLAN.md") {
					t.Errorf("the box has no title:\n%s", box)
				}
				// The body is read off the wrapped lines with the wraps
				// joined back, so a finding split across rows still counts.
				body := strings.Join(m.planCheck.lines(cw), " ")
				for _, want := range []string{"2 milestones · 5 tasks · 1 owner tasks · 0 open questions", tc.summary} {
					if !strings.Contains(body, want) {
						t.Errorf("body lacks %q:\n%s", want, body)
					}
				}
				at := -1
				for _, want := range tc.order {
					i := strings.Index(body, want)
					if i < 0 {
						t.Fatalf("body lacks %q:\n%s", want, body)
					}
					if i < at {
						t.Errorf("%q comes before the line above it in the expected order:\n%s", want, body)
					}
					at = i
				}
			})
		}
	}
}

// TestPlanCheckOverlayKeys pins the overlay's own keys: the scroll keys move
// the body on a plan too long for the box, and esc closes it and gives the
// keyboard back to the list. Load-bearing: end and home, which no other
// overlay here binds, and esc, the only way out.
func TestPlanCheckOverlayKeys(t *testing.T) {
	s := planCheckStatusErrors()
	for i := 0; i < 40; i++ {
		s.Findings = append(s.Findings, PlanFinding{Level: "warning", Line: 100 + i, Rule: "r", Message: "another warning"})
	}
	m := withPlanStatus(t, planCheckSized(t, 80, 24), s)
	m, _ = runPlanAction(t, m, "errors")
	if !m.planCheckOpen {
		t.Fatal("the overlay did not open")
	}
	if m.planCheck.hint() != "0%" {
		t.Fatalf("hint at the top = %q, want 0%%", m.planCheck.hint())
	}
	steps := []struct {
		name string
		k    tea.KeyPressMsg
		want func(y int) bool
	}{
		{"down moves one row", tea.KeyPressMsg{Code: tea.KeyDown}, func(y int) bool { return y == 1 }},
		{"up moves back", tea.KeyPressMsg{Code: tea.KeyUp}, func(y int) bool { return y == 0 }},
		{"pgdown moves a page", tea.KeyPressMsg{Code: tea.KeyPgDown}, func(y int) bool { return y > 1 }},
		{"pgup moves back a page", tea.KeyPressMsg{Code: tea.KeyPgUp}, func(y int) bool { return y == 0 }},
		{"end goes to the bottom", tea.KeyPressMsg{Code: tea.KeyEnd}, func(y int) bool { return y > 0 }},
	}
	for _, st := range steps {
		m, _ = update(t, m, st.k)
		if y := m.planCheck.body.YOffset(); !st.want(y) {
			t.Fatalf("%s: offset = %d", st.name, y)
		}
	}
	if m.planCheck.hint() != "100%" {
		t.Errorf("hint at the bottom = %q, want 100%%", m.planCheck.hint())
	}
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	if y := m.planCheck.body.YOffset(); y != 0 {
		t.Fatalf("home: offset = %d, want 0", y)
	}
	m, _ = update(t, m, keyEsc)
	if m.planCheckOpen {
		t.Fatal("esc did not close the overlay")
	}
	if got := activeInputMode(m.modeSnapshot()); got != inputNormal {
		t.Errorf("mode after esc = %v, want normal", got)
	}
	if strings.Contains(render(m), "Plan check") {
		t.Error("the closed overlay is still drawn")
	}
}

// TestPlanCheckNoPlan pins that a project with no plan file opens nothing and
// says there is no plan to check, naming where the plan would be.
func TestPlanCheckNoPlan(t *testing.T) {
	s := PlanStatus{PlanPath: "docs/PLAN.md"}
	m := withPlanStatus(t, testModel(sess("a")), s)
	m, cmd := runPlanAction(t, m, "errors")
	if m.planCheckOpen || cmd != nil {
		t.Fatalf("open=%v cmd=%v, want no overlay and no command", m.planCheckOpen, cmd != nil)
	}
	if want := "There is no plan to check at docs/PLAN.md."; m.status != want {
		t.Errorf("notice = %q, want %q", m.status, want)
	}
}

// TestPlanCheckNotKnown pins the notice before the first read lands: with the
// seam wired but nothing read yet, a missing plan cannot be told from one not
// looked at, so the action opens nothing and says to try again.
func TestPlanCheckNotKnown(t *testing.T) {
	m := testModel(sess("a"))
	m.deps.LoadPlanStatus = func(string) PlanStatus {
		t.Error("the action called the seam; it must use the stored status")
		return PlanStatus{}
	}
	m, cmd := runPlanAction(t, m, "errors")
	if m.planCheckOpen || cmd != nil {
		t.Fatalf("open=%v cmd=%v, want no overlay and no command", m.planCheckOpen, cmd != nil)
	}
	if m.status != planCheckNotReadNotice {
		t.Errorf("notice = %q, want %q", m.status, planCheckNotReadNotice)
	}
}

// TestPlanCheckUnreadable pins that a status read that failed (an unreadable
// afk.toml) says why rather than "no plan to check at" an empty path.
func TestPlanCheckUnreadable(t *testing.T) {
	m := withPlanStatus(t, testModel(sess("a")), PlanStatus{Err: "afk.toml: bad value"})
	m, _ = runPlanAction(t, m, "errors")
	if m.planCheckOpen {
		t.Fatal("the overlay opened over a failed read")
	}
	if want := planCheckUnreadablePref + "afk.toml: bad value"; m.status != want {
		t.Errorf("notice = %q, want %q", m.status, want)
	}
}

// TestPlanCheckUnwired pins a dashboard built without the seam: the row is
// listed dimmed with its reason, enter on it does nothing, and the action
// itself, reached some other way, says the dashboard cannot read the plan.
func TestPlanCheckUnwired(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, leaderKeyPress)
	m = typeText(t, m, "errors")
	v := m.leader.visible()
	if len(v) != 1 || v[0].action != actionPlanCheck {
		t.Fatalf("\"errors\" leaves %d rows, want only check the plan", len(v))
	}
	if !strings.Contains(v[0].blocked, "not available") {
		t.Fatalf("blocked = %q, want the row dimmed", v[0].blocked)
	}
	after, cmd := update(t, m, keyEnter)
	if after.planCheckOpen || cmd != nil || !after.leaderOpen {
		t.Fatalf("enter on the dimmed row: open=%v cmd=%v dialog=%v, want nothing done", after.planCheckOpen, cmd != nil, after.leaderOpen)
	}
	got, _ := testModel(sess("a")).openPlanCheck()
	if gm := got.(Model); gm.planCheckOpen || gm.status != planCheckUnwiredNotice {
		t.Errorf("open=%v notice=%q, want closed with %q", gm.planCheckOpen, gm.status, planCheckUnwiredNotice)
	}
}

// TestPlanCheckIgnoresTheLeader pins that ctrl+space opens nothing while the
// overlay is up, as over the inbox: the overlay is not a surface the leader
// is a rule on.
func TestPlanCheckIgnoresTheLeader(t *testing.T) {
	m := withPlanStatus(t, testModel(sess("a")), planCheckStatusErrors())
	m, _ = runPlanAction(t, m, "errors")
	m, _ = update(t, m, leaderKeyPress)
	if m.leaderOpen || !m.planCheckOpen {
		t.Fatalf("dialog=%v overlay=%v, want the overlay alone", m.leaderOpen, m.planCheckOpen)
	}
}

// TestPlanCheckClosesOnAProjectSwitch pins that the outgoing project's plan
// is not left on screen under the incoming project's name.
func TestPlanCheckClosesOnAProjectSwitch(t *testing.T) {
	m := testModel(sess("a"))
	m.projects = []project.Project{{Path: "/repo", Name: "api"}, {Path: "/beta", Name: "beta"}}
	m.activeProject = 0
	m = withPlanStatus(t, m, planCheckStatusErrors())
	m, _ = runPlanAction(t, m, "errors")
	if !m.planCheckOpen {
		t.Fatal("setup: the overlay did not open")
	}
	m, _ = m.switchProject("/beta")
	if m.repo != "/beta" {
		t.Fatalf("fixture broken: repo = %q after the switch", m.repo)
	}
	if m.planCheckOpen || activeInputMode(m.modeSnapshot()) == inputPlanCheck {
		t.Fatal("the overlay stayed open across a project switch")
	}
}
