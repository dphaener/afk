package main

import (
	"fmt"
	"strings"

	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ui"
)

// dashApprovePlan is the dashboard's ApprovePlan seam: it runs the steps of
// `afk plan approve` for the project at root and words the result as one
// notice. It commits and pushes, so the dashboard calls it only after the
// owner's yes, and only inside a command, never while drawing.
func dashApprovePlan(root string) ui.PlanApproveResult {
	return dashApprovePlanWith(approvePlanForSurface, realApproveDeps(), root)
}

// dashApprovePlanWith is dashApprovePlan with the entry point and its
// dependencies passed in, so a test can check the mapping without git.
func dashApprovePlanWith(approve func(string, approveDeps) approveResult, deps approveDeps, root string) ui.PlanApproveResult {
	res := approve(root, deps)
	out := ui.PlanApproveFailed
	switch res.Outcome {
	case approveApproved:
		out = ui.PlanApproveApproved
	case approveAlreadyApproved:
		out = ui.PlanApproveAlready
	case approveRefused, approveNoProject:
		out = ui.PlanApproveRefused
	}
	return ui.PlanApproveResult{Outcome: out, Text: approveResultText(res)}
}

// approveResultText words a result as one notice for the dashboard. The
// command's own lines are written for a terminal (several per run, two
// streams, the command's name in front); a notice has room for little, so
// this says the same things shorter and keeps only the first line of any git
// error.
func approveResultText(r approveResult) string {
	switch r.Outcome {
	case approveApproved:
		return approvedText(r)
	case approveAlreadyApproved:
		return "The plan is already approved. Nothing was changed."
	case approveRefused, approveNoProject:
		return sentence(r.Reason) + " Nothing was changed."
	case approveFailed:
		return failedText(r)
	}
	return "Could not tell whether the plan was approved. Run afk plan approve to find out."
}

func approvedText(r approveResult) string {
	var b strings.Builder
	if r.Counts.Read {
		s := r.Counts.Summary
		fmt.Fprintf(&b, "Approved the plan at %s: %s in %s, %s for you to do, %s.", r.PlanRel,
			plural(s.Tasks, "task"), plural(s.Milestones, "milestone"), plural(s.OwnerTasks, "task"), plural(s.OpenQuestions, "open question"))
	} else {
		fmt.Fprintf(&b, "Approved the plan at %s.", r.PlanRel)
	}
	if r.DaemonRunning {
		b.WriteString(" A running afk daemon starts the first ready task within a few seconds.")
	} else {
		b.WriteString(" No afk daemon seems to be running. Start it with afk daemon, and it starts the first ready task.")
	}
	switch r.PlanHost.Outcome {
	case approvePlanHostEnded, approvePlanHostGone:
		b.WriteString(" The planning session has ended.")
	case approvePlanHostFailed:
		fmt.Fprintf(&b, " The planning session could not be ended (%s). Run afk clean %s --project %s to end it.", firstLine(r.PlanHost.Err), session.PlanSessionName, r.Root)
	}
	switch r.Push.Outcome {
	case approvePushViaPR:
		b.WriteString(" The commit stays on your machine, because this project sends plan changes as pull requests.")
	case approvePushNoOrigin:
		b.WriteString(" The commit was not pushed: this repository has no origin.")
	case approvePushNoBranch:
		b.WriteString(" The commit was not pushed: the folder is not on a branch.")
	case approvePushFailed:
		fmt.Fprintf(&b, " The commit was not pushed (%s). To push it, run: git -C %s push origin %s", firstLine(r.Push.Err), r.Root, r.Push.Branch)
	}
	return b.String()
}

func failedText(r approveResult) string {
	done := "nothing"
	if len(r.Done) > 0 {
		parts := make([]string, len(r.Done))
		for i, s := range r.Done {
			parts[i] = s.did()
		}
		done = strings.Join(parts, "; ")
	}
	left := make([]string, len(r.Left))
	for i, s := range r.Left {
		left[i] = s.todo()
	}
	return fmt.Sprintf("Could not %s: %s. Done: %s; not done: %s. The plan is not approved yet. Fix that and approve again here, or run afk plan approve --project %s to finish.",
		r.Failed.todo(), firstLine(r.Err), done, strings.Join(left, "; "), r.Root)
}

// firstLine is the first non-empty line of err's text, trimmed, with no
// closing full stop so a sentence can add its own. A git error runs to many
// lines; the first says what went wrong.
func firstLine(err error) string {
	if err == nil {
		return "no reason was given"
	}
	for _, l := range strings.Split(err.Error(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return strings.TrimRight(l, ".")
		}
	}
	return "no reason was given"
}

// sentence ends s with a full stop unless it already ends a sentence, and
// keeps only what is before a line break: the refusal reasons can run to a
// hint on a second line, which a notice has no room for.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "The plan cannot be approved."
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}
