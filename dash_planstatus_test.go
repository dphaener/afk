package main

import (
	"github.com/dphaener/afk/internal/ui"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// dashPlanGot is the loader's result under a short name for the check closures.
type dashPlanGot = ui.PlanStatus

// dashPlanCleanText has no lint error (only warnings), so a session can reach review.
const dashPlanCleanText = "# Plan\n\n### Milestone 1\n\n#### T1.1 — First\n\n- **Depends on:** none\n"

const dashPlanErrorText = "# Plan\n\n### Milestone 1\n\n#### T1.1 First\n\n- **Depends on:** none\n"

func dashPlanRoundsQuestions() string {
	return `{"id":"q1","round":1,"kind":"setup","question":"One?","answer":"a"}` + "\n" +
		`{"id":"q2","round":1,"kind":"setup","question":"Two?"}` + "\n" +
		`{"id":"q3","round":1,"kind":"setup","question":"Three?"}` + "\n"
}

// TestDashPlanStatus runs the real loader against temp folders with a fake
// git lookup. Load-bearing rows: damaged (a damaged state file must read as a
// damaged session, never as no session, or the dashboard would say there is
// nothing to look at), worktree (the plan is read from the main checkout, not
// from the session's own empty .afk/), unreadable (a bad afk.toml is an Err,
// not a quiet "no plan"), and the tree check on every row (the dashboard
// refreshes this every two seconds and must never write under .afk/plan/).
func TestDashPlanStatus(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		// repo, when set, returns the folder the dashboard is shown instead of root.
		repo  func(t *testing.T, root string) (repo, commonDir string)
		check func(t *testing.T, got dashPlanGot)
	}{
		{
			name:  "no plan folder",
			setup: func(t *testing.T, root string) {},
			check: func(t *testing.T, g dashPlanGot) {
				if g.Session || g.PlanExists || g.Err != "" || g.Damaged {
					t.Errorf("got %+v, want nothing found", g)
				}
			},
		},
		{
			name: "no plan folder but a plan file",
			setup: func(t *testing.T, root string) {
				writePlanFile(t, root, "docs/PLAN.md", dashPlanErrorText)
			},
			check: func(t *testing.T, g dashPlanGot) {
				if g.Session || !g.PlanExists || g.PlanPath != filepath.Join("docs", "PLAN.md") {
					t.Errorf("got %+v, want no session and the plan at docs/PLAN.md", g)
				}
			},
		},
		{
			name: "extend at rounds with two of three questions open",
			setup: func(t *testing.T, root string) {
				writePlanFile(t, root, ".afk/plan/state.json", `{"mode":"extend","phase":"rounds","updated_at":"2026-10-12T00:00:00Z"}`)
				writePlanFile(t, root, ".afk/plan/brief.md", "brief\n")
				writePlanFile(t, root, ".afk/plan/survey.md", "survey\n")
				writePlanFile(t, root, ".afk/plan/questions.jsonl", dashPlanRoundsQuestions())
			},
			check: func(t *testing.T, g dashPlanGot) {
				if !g.Session || g.Mode != "extend" || g.Phase != "rounds" || g.Recorded != "rounds" {
					t.Errorf("got %+v, want an extend session at rounds", g)
				}
				if g.OpenQuestions != 2 || g.Questions != 3 || g.Damaged {
					t.Errorf("questions = %d open of %d, damaged %v; want 2 of 3, not damaged", g.OpenQuestions, g.Questions, g.Damaged)
				}
			},
		},
		{
			name: "new at review",
			setup: func(t *testing.T, root string) {
				writePlanFile(t, root, ".afk/plan/state.json", planState("review"))
				for _, f := range []string{"brief.md", "survey.md", "slices.md", "critic.md"} {
					writePlanFile(t, root, ".afk/plan/"+f, "text\n")
				}
				writePlanFile(t, root, ".afk/plan/questions.jsonl", planQuestions("no"))
				writePlanFile(t, root, "docs/PLAN.md", dashPlanCleanText)
			},
			check: func(t *testing.T, g dashPlanGot) {
				if !g.Session || g.Mode != "new" || g.Phase != "review" || g.Damaged {
					t.Errorf("got %+v, want a sound new session at review", g)
				}
				if g.OpenQuestions != 0 || g.Questions != 2 {
					t.Errorf("questions = %d open of %d, want 0 of 2", g.OpenQuestions, g.Questions)
				}
			},
		},
		{
			name: "damaged state file",
			setup: func(t *testing.T, root string) {
				writePlanFile(t, root, ".afk/plan/state.json", "{not json")
			},
			check: func(t *testing.T, g dashPlanGot) {
				if !g.Session || !g.Damaged || len(g.Problems) == 0 {
					t.Errorf("got %+v, want a damaged session with problems", g)
				}
			},
		},
		{
			name: "lint error",
			setup: func(t *testing.T, root string) {
				writePlanFile(t, root, "docs/PLAN.md", dashPlanErrorText)
			},
			check: func(t *testing.T, g dashPlanGot) {
				if g.Summary.Errors != 1 {
					t.Fatalf("Summary.Errors = %d, want 1 (%+v)", g.Summary.Errors, g.Summary)
				}
				var errs int
				for _, f := range g.Findings {
					if f.Level == "error" {
						errs++
						if f.Line != 5 || f.Rule != "bad-task-heading" {
							t.Errorf("error finding = %+v, want line 5 bad-task-heading", f)
						}
					}
				}
				if errs != 1 {
					t.Errorf("%d error findings, want 1", errs)
				}
				if g.Summary.Warnings == 0 || len(g.Findings) != g.Summary.Errors+g.Summary.Warnings {
					t.Errorf("findings %d do not match summary %+v", len(g.Findings), g.Summary)
				}
			},
		},
		{
			name: "worktree resolves to the main checkout",
			setup: func(t *testing.T, root string) {
				writePlanFile(t, root, ".afk/plan/state.json", planState("rounds"))
				writePlanFile(t, root, "docs/PLAN.md", dashPlanErrorText)
			},
			repo: func(t *testing.T, root string) (string, string) {
				return filepath.Join(t.TempDir(), "wt"), filepath.Join(root, ".git")
			},
			check: func(t *testing.T, g dashPlanGot) {
				if !g.Session || !g.PlanExists || g.Summary.Errors != 1 {
					t.Errorf("got %+v, want the main checkout's session and plan", g)
				}
				if g.PlanPath != filepath.Join("docs", "PLAN.md") {
					t.Errorf("PlanPath = %q, want it relative to the main checkout", g.PlanPath)
				}
			},
		},
		{
			name: "unreadable afk.toml",
			setup: func(t *testing.T, root string) {
				writePlanFile(t, root, "afk.toml", "plan = [unclosed\n")
				writePlanFile(t, root, ".afk/plan/state.json", planState("rounds"))
			},
			check: func(t *testing.T, g dashPlanGot) {
				if g.Err == "" || !strings.Contains(g.Err, "afk.toml") {
					t.Errorf("Err = %q, want a message naming afk.toml", g.Err)
				}
				if g.Session {
					t.Errorf("Session = true with a load error")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			before := dashTreeListing(t, root)

			repo, common := root, ""
			if tt.repo != nil {
				repo, common = tt.repo(t, root)
			}
			p := dashPlanStatusParts{gitCommonDir: func(string) string { return common }}
			got := loadDashPlanStatus(p, repo)
			tt.check(t, got)

			if after := dashTreeListing(t, root); !reflect.DeepEqual(before, after) {
				t.Errorf("the loader changed .afk/: before %v, after %v", before, after)
			}
		})
	}
}
