package ui

import (
	"slices"
	"testing"
)

// TestPlanStatusLine pins the wording of the plan line. Load-bearing rows:
// "damaged" and "load error" (they must beat the phase wording, since a
// damaged file's phase is untrustworthy), "new at review" versus "extend at
// review" (only a new plan waits for approval), and the two rounds rows
// (singular and plural).
func TestPlanStatusLine(t *testing.T) {
	cases := []struct {
		name string
		in   PlanStatus
		want string
	}{
		{"load error", PlanStatus{Err: "afk.toml: bad", Session: true, Mode: "new", Phase: "review"}, "plan status unreadable: afk.toml: bad"},
		{"no session", PlanStatus{}, "no planning session"},
		{"damaged", PlanStatus{Session: true, Damaged: true, Mode: "new", Phase: "review", Problems: []string{"state.json is not JSON", "other"}}, "the planning state file is damaged: state.json is not JSON"},
		{"new in middle phase", PlanStatus{Session: true, Mode: "new", Phase: "survey", Waiting: "The survey is not written yet."}, "new · survey · the survey is not written yet"},
		{"extend in middle phase", PlanStatus{Session: true, Mode: "extend", Phase: "brief", Waiting: "Waiting for the brief."}, "extend · brief · waiting for the brief"},
		{"amend in middle phase", PlanStatus{Session: true, Mode: "amend", Phase: "survey", Waiting: "Needs a survey"}, "amend · survey · needs a survey"},
		{"reconcile in middle phase", PlanStatus{Session: true, Mode: "reconcile", Phase: "draft", Waiting: "The draft is open."}, "reconcile · draft · the draft is open"},
		{"rounds one open question", PlanStatus{Session: true, Mode: "extend", Phase: "rounds", OpenQuestions: 1, Questions: 3}, "extend · rounds · 1 question has no answer yet"},
		{"rounds two open questions", PlanStatus{Session: true, Mode: "new", Phase: "rounds", OpenQuestions: 2, Questions: 2}, "new · rounds · 2 questions have no answer yet"},
		{"rounds none open uses waiting", PlanStatus{Session: true, Mode: "new", Phase: "rounds", Waiting: "Ready to draft."}, "new · rounds · ready to draft"},
		{"new at review", PlanStatus{Session: true, Mode: "new", Phase: "review"}, "plan waiting for your approval"},
		{"extend at review is finished", PlanStatus{Session: true, Mode: "extend", Phase: "review"}, "extend · finished · the changes are in the plan"},
		{"reconcile at review is finished", PlanStatus{Session: true, Mode: "reconcile", Phase: "review"}, "reconcile · finished · the changes are in the plan"},
		{"approved", PlanStatus{Session: true, Mode: "new", Phase: "approved"}, "plan approved"},
		{"empty phase", PlanStatus{Session: true, Mode: "new", Waiting: "Start the session."}, "new · Start the session."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planStatusLine(tc.in); got != tc.want {
				t.Errorf("planStatusLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPlanFindingLine pins the finding wording to `afk plan lint`'s. The
// no-line row is load-bearing: a ":0" there would differ from the command.
func TestPlanFindingLine(t *testing.T) {
	cases := []struct {
		name string
		in   PlanFinding
		want string
	}{
		{"line and task", PlanFinding{Level: "error", Line: 12, Task: "T1.1", Rule: "task-field", Message: "missing Files"}, "docs/PLAN.md:12: error: T1.1: missing Files [task-field]"},
		{"line no task", PlanFinding{Level: "warning", Line: 3, Rule: "r", Message: "odd"}, "docs/PLAN.md:3: warning: odd [r]"},
		{"task no line", PlanFinding{Level: "error", Task: "T2", Rule: "r", Message: "m"}, "docs/PLAN.md: error: T2: m [r]"},
		{"neither", PlanFinding{Level: "warning", Rule: "r", Message: "m"}, "docs/PLAN.md: warning: m [r]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planFindingLine("docs/PLAN.md", tc.in); got != tc.want {
				t.Errorf("planFindingLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPlanCheckLines pins the overlay body. Load-bearing rows: "errors and
// warnings" (errors first, plan order kept inside each level, so a warning
// that comes earlier in the plan still lists after the errors), the singular
// "one error" row, and "no plan" (the notice the action raises).
func TestPlanCheckLines(t *testing.T) {
	counts := "3 milestones · 9 tasks · 2 owner tasks · 1 open questions"
	base := PlanStatus{PlanExists: true, PlanPath: "docs/PLAN.md", Summary: PlanSummary{Milestones: 3, Tasks: 9, OwnerTasks: 2, OpenQuestions: 1}}
	with := func(sum func(*PlanSummary), fs ...PlanFinding) PlanStatus {
		s := base
		sum(&s.Summary)
		s.Findings = fs
		return s
	}
	w1 := PlanFinding{Level: "warning", Line: 5, Rule: "w", Message: "w1"}
	e1 := PlanFinding{Level: "error", Line: 9, Task: "T1", Rule: "e", Message: "e1"}
	w2 := PlanFinding{Level: "warning", Line: 11, Rule: "w", Message: "w2"}
	e2 := PlanFinding{Level: "error", Line: 20, Rule: "e", Message: "e2"}
	cases := []struct {
		name string
		in   PlanStatus
		want []string
	}{
		{"no plan", PlanStatus{PlanPath: "docs/PLAN.md"}, []string{"There is no plan to check at docs/PLAN.md."}},
		{"zero findings", base, []string{counts, "docs/PLAN.md: no problems found", ""}},
		{"warnings only", with(func(s *PlanSummary) { s.Warnings = 2 }, w1, w2), []string{counts, "docs/PLAN.md: 0 errors, 2 warnings", "",
			"docs/PLAN.md:5: warning: w1 [w]", "docs/PLAN.md:11: warning: w2 [w]"}},
		{"one error", with(func(s *PlanSummary) { s.Errors = 1 }, e1), []string{counts, "docs/PLAN.md: 1 error, 0 warnings", "",
			"docs/PLAN.md:9: error: T1: e1 [e]"}},
		{"errors and warnings", with(func(s *PlanSummary) { s.Errors = 2; s.Warnings = 2 }, w1, e1, w2, e2), []string{counts, "docs/PLAN.md: 2 errors, 2 warnings", "",
			"docs/PLAN.md:9: error: T1: e1 [e]", "docs/PLAN.md:20: error: e2 [e]",
			"docs/PLAN.md:5: warning: w1 [w]", "docs/PLAN.md:11: warning: w2 [w]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planCheckLines(tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("planCheckLines =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
