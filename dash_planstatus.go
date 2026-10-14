package main

import (
	"os"
	"path/filepath"

	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/ui"
)

// This file is the real version of the dashboard's plan status seam
// (ui.Deps.LoadPlanStatus). It lives here and not in internal/ui because the
// pieces it joins are in this package: the main checkout lookup and
// loadPlanSession, which `afk plan status` and `afk plan lint` share.
//
// It only reads. It calls plansession.Load (through loadPlanSession) and the
// pure Decide, and never Advance, WriteState or Archive, so opening the
// dashboard on a project cannot move a planning session along or seed a state
// file over a damaged one. Like the build seam it runs inside a command on the
// dashboard's refresh tick, so it dials nothing and runs no git beyond the
// bounded common-dir lookup.

// dashPlanStatusParts are the lookups outside the project's own files,
// injected so the loader can be tested against temp folders.
type dashPlanStatusParts struct {
	// gitCommonDir is the git common dir as seen from dir, "" when dir is not
	// in a repository.
	gitCommonDir func(dir string) string
}

func realDashPlanStatusParts() dashPlanStatusParts {
	return dashPlanStatusParts{gitCommonDir: resolveGitCommonDirIn}
}

// dashLoadPlanStatus is ui.Deps.LoadPlanStatus for the real dashboard.
func dashLoadPlanStatus(repo string) ui.PlanStatus {
	return loadDashPlanStatus(realDashPlanStatusParts(), repo)
}

// loadDashPlanStatus reads the planning session and the plan checker's result
// for the project shown. repo can be a session's linked worktree, whose own
// git-ignored .afk/ holds no planning session, so the main checkout is worked
// out first (the same step loadDashBuild takes).
func loadDashPlanStatus(p dashPlanStatusParts, repo string) ui.PlanStatus {
	root := mainCheckout(repo, p.gitCommonDir(repo))
	// PlanErrors feeds Decide's phase gates, so the same checker runs here as
	// in `afk plan status`; the findings below are read from the same text.
	files, planPath, err := loadPlanSession(root, planDeps{lintErrors: func(b []byte) int { return planlint.Lint(b).Summary.Errors }})
	if err != nil {
		return ui.PlanStatus{Err: err.Error()}
	}
	st := plansession.Decide(files)

	out := ui.PlanStatus{
		Session:    st.Session,
		Damaged:    files.StateProblem != "",
		Mode:       st.Mode,
		Phase:      st.Phase,
		Recorded:   st.Recorded,
		Waiting:    st.Waiting,
		Problems:   st.Problems,
		Questions:  len(files.Questions),
		PlanExists: files.PlanExists,
		PlanPath:   planPath,
	}
	for _, q := range files.Questions {
		if q.Answer == "" {
			out.OpenQuestions++
		}
	}
	if rel, err := filepath.Rel(root, planPath); err == nil {
		out.PlanPath = rel
	}
	if files.PlanExists {
		if data, err := os.ReadFile(planPath); err == nil {
			rep := planlint.Lint(data)
			s := rep.Summary
			out.Summary = ui.PlanSummary{
				Milestones: s.Milestones, Tasks: s.Tasks, OwnerTasks: s.OwnerTasks,
				OpenQuestions: s.OpenQuestions, Errors: s.Errors, Warnings: s.Warnings,
			}
			for _, f := range rep.Findings {
				out.Findings = append(out.Findings, ui.PlanFinding{
					Level: f.Level, Line: f.Line, Task: f.Task, Rule: f.Rule, Message: f.Message,
				})
			}
		}
	}
	return out
}
