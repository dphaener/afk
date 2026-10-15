package main

import (
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/web"
)

// This file joins the web page's plan status and approve seams to the
// dashboard's own: the page words the plan line with ui.PlanStatus.Line and
// the result with approveResultText, so the two surfaces say the same things.

// webPlanStatus is web.Deps.PlanStatus. It is the dashboard's file read with
// one change: the main checkout lookup returns root as given. The page only
// asks about a configured or listed project, whose root already is its main
// checkout, so a page request runs no git and no network.
func webPlanStatus(root string) web.PlanStatus {
	return webPlanStatusWith(dashPlanStatusParts{gitCommonDir: func(string) string { return "" }}, root)
}

// webPlanStatusWith is webPlanStatus with the parts passed in, so a test can
// check the mapping without reading a project.
func webPlanStatusWith(parts dashPlanStatusParts, root string) web.PlanStatus {
	s := loadDashPlanStatus(parts, root)
	if s.Err != "" {
		// An unreadable status says nothing else; the page words it as
		// "could not be read".
		return web.PlanStatus{Problems: []string{}}
	}
	problems := s.Problems
	if problems == nil {
		problems = []string{}
	}
	return web.PlanStatus{
		Known:      true,
		Session:    s.Session,
		Damaged:    s.Damaged,
		Mode:       s.Mode,
		Phase:      s.Phase,
		Line:       s.Line(),
		Approvable: s.Session && !s.Damaged && s.Mode == "new" && s.Phase == "review",
		Problems:   problems,
	}
}

// webApprovePlan is web.Deps.ApprovePlan: the steps of afk plan approve for
// the project at root. It commits and pushes, so the page calls it only after
// the owner's confirm.
func webApprovePlan(root string) web.PlanApproveResult {
	return webApprovePlanWith(approvePlanForSurface, realApproveDeps(), root)
}

// webApprovePlanWith is webApprovePlan with the entry point and its
// dependencies passed in, so a test can check the mapping without git. It maps
// outcomes as dashApprovePlanWith does; any outcome it does not know is a
// failure, the safe reading.
func webApprovePlanWith(approve func(string, approveDeps) approveResult, deps approveDeps, root string) web.PlanApproveResult {
	res := approve(root, deps)
	out := web.PlanApproveFailed
	switch res.Outcome {
	case approveApproved:
		out = web.PlanApproved
	case approveAlreadyApproved:
		out = web.PlanApproveAlready
	case approveRefused, approveNoProject:
		out = web.PlanApproveRefused
	}
	return web.PlanApproveResult{Outcome: out, Text: approveResultText(res)}
}

// webStartPlan is web.Deps.StartPlan: afk plan extend, amend or reconcile for
// the project at root, minus the terminal. It never sets a provider (the
// default agent runs) and never writes amend's yes marker, so the page cannot
// skip the owner's read of the sentence.
func webStartPlan(root, mode, text string) web.PlanStartResult {
	return webStartPlanWith(startPlanForSurface, realPlanDeps(), func(root string) error {
		_, err := readMetaWithRetry(root, session.PlanSessionName, attachWait)
		return err
	}, root, mode, text)
}

// webStartPlanWith is webStartPlan with the entry point, its dependencies and
// the host wait passed in. A started or resumed result is held until the host
// has written host.json, because the page sends the owner to the chat view at
// once and the chat reads that record. An outcome it does not know is a
// refusal, so nothing reads as started that the entry point did not say.
func webStartPlanWith(start func(planSurfaceRequest, planDeps) planSurfaceResult, deps planDeps, wait func(root string) error, root, mode, text string) web.PlanStartResult {
	res := start(planSurfaceRequest{Mode: mode, Project: root, Text: text}, deps)
	out := web.PlanStartResult{
		Outcome:   web.PlanStartRefused,
		Reason:    res.Reason,
		Open:      res.SessionOpen,
		OpenMode:  res.OpenMode,
		OpenPhase: res.OpenPhase,
	}
	switch res.Outcome {
	case planSurfaceStarted:
		out.Outcome = web.PlanStartStarted
	case planSurfaceResumed:
		out.Outcome = web.PlanStartResumed
	}
	if out.Outcome == web.PlanStartRefused {
		return out
	}
	if err := wait(root); err != nil {
		return web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: planHostNotReadyReason(err)}
	}
	return out
}
