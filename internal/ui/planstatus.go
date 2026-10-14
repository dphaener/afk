package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PlanStatus is what the dashboard knows about the planning session and the
// plan checker's result for one project. The types here mirror what the root
// package reads from plansession and planlint: internal/ui never imports the
// root package, and does not import planlint either, so the adapter in the
// root package maps one to the other (the same split as BuildSession).
type PlanStatus struct {
	// Known is set by the model when a load has finished. A zero PlanStatus
	// means "not read yet", which is not the same as "no planning session".
	Known bool
	// Err is a load error, such as an unreadable afk.toml. It is shown as is.
	Err string
	// Session is true when a planning session exists under .afk/plan.
	Session bool
	// Damaged is true when the session's state file cannot be trusted; the
	// reasons are in Problems.
	Damaged bool
	// Mode and Phase are the session's mode (new, extend, amend, reconcile)
	// and its phase. T7.8 reads Session, Mode and Phase to enable approving
	// only a new plan at review.
	Mode, Phase string
	// Recorded is the phase written in the state file, which can differ from
	// Phase when the files on disk say an earlier phase is unfinished.
	Recorded string
	// Waiting is one plain sentence saying what the phase is waiting for.
	Waiting string
	// OpenQuestions counts questions with no answer; Questions counts all.
	OpenQuestions, Questions int
	// Problems are the plain-language reasons the state is damaged.
	Problems []string
	// PlanExists is true when the master plan file is there to check.
	PlanExists bool
	// PlanPath is the plan path relative to the project root (docs/PLAN.md),
	// so every line the dashboard shows reads as `afk plan lint` reads.
	PlanPath string
	// Summary and Findings are the checker's result. T7.8 shows the counts.
	Summary  PlanSummary
	Findings []PlanFinding
}

// PlanSummary is the checker's counts.
type PlanSummary struct {
	Milestones, Tasks, OwnerTasks, OpenQuestions, Errors, Warnings int
}

// PlanFinding is one thing the checker found. Level is "error" or "warning".
type PlanFinding struct {
	Level               string
	Line                int
	Task, Rule, Message string
}

// planStatusLine is the one-line wording of the planning session, shown in
// the master-plan overview and the Build tab. It is pure so every case is a
// table row. The order of the cases is load-bearing: a load error and a
// damaged file must win over any phase wording, because the phase of a
// damaged file cannot be trusted.
func planStatusLine(s PlanStatus) string {
	switch {
	case s.Err != "":
		return "plan status unreadable: " + s.Err
	case !s.Session:
		return "no planning session"
	case s.Damaged:
		first := ""
		if len(s.Problems) > 0 {
			first = s.Problems[0]
		}
		return "the planning state file is damaged: " + first
	case s.Mode == "new" && s.Phase == "review":
		return "plan waiting for your approval"
	case s.Phase == "approved":
		return "plan approved"
	case s.Phase == "review":
		return s.Mode + " · finished · the changes are in the plan"
	case s.Phase == "rounds" && s.OpenQuestions > 0:
		if s.OpenQuestions == 1 {
			return s.Mode + " · rounds · 1 question has no answer yet"
		}
		return fmt.Sprintf("%s · rounds · %d questions have no answer yet", s.Mode, s.OpenQuestions)
	case s.Phase == "":
		return s.Mode + " · " + s.Waiting
	}
	return s.Mode + " · " + s.Phase + " · " + waitingClause(s.Waiting)
}

// waitingClause turns the waiting sentence into a clause that fits after the
// phase: no trailing period, first letter lower-cased.
func waitingClause(w string) string {
	w = strings.TrimSuffix(strings.TrimSpace(w), ".")
	r, n := utf8.DecodeRuneInString(w)
	if n == 0 {
		return w
	}
	return string(unicode.ToLower(r)) + w[n:]
}

// planFindingLine words one finding exactly as `afk plan lint` prints it:
// path[:line]: level: [task: ]message [rule]. The line number is left out
// when the checker has none (Line 0).
func planFindingLine(path string, f PlanFinding) string {
	loc := path
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", path, f.Line)
	}
	task := ""
	if f.Task != "" {
		task = f.Task + ": "
	}
	return fmt.Sprintf("%s: %s: %s%s [%s]", loc, f.Level, task, f.Message, f.Rule)
}

// planCountNoun is `plural` of plan_lint_cmd.go: "1 error", "2 errors".
func planCountNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// planCheckLines is the body of the check-the-plan overlay. Errors come first
// because they are what stops a plan from being used; the sort is stable so
// the plan's own order is kept inside each level.
func planCheckLines(s PlanStatus) []string {
	if !s.PlanExists {
		return []string{fmt.Sprintf("There is no plan to check at %s.", s.PlanPath)}
	}
	c := s.Summary
	lines := []string{
		fmt.Sprintf("%d milestones · %d tasks · %d owner tasks · %d open questions", c.Milestones, c.Tasks, c.OwnerTasks, c.OpenQuestions),
	}
	if c.Errors == 0 && c.Warnings == 0 {
		lines = append(lines, s.PlanPath+": no problems found")
	} else {
		lines = append(lines, fmt.Sprintf("%s: %s, %s", s.PlanPath, planCountNoun(c.Errors, "error"), planCountNoun(c.Warnings, "warning")))
	}
	lines = append(lines, "")
	fs := append([]PlanFinding(nil), s.Findings...)
	sort.SliceStable(fs, func(i, j int) bool {
		return fs[i].Level == "error" && fs[j].Level != "error"
	})
	for _, f := range fs {
		lines = append(lines, planFindingLine(s.PlanPath, f))
	}
	return lines
}
