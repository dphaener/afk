package main

import (
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ui"
)

// dashStartPlan is the dashboard's three plan actions. It runs the same start
// path `afk plan extend|amend|reconcile` run, through startPlanForSurface, so a
// refusal reads as the command's words and a start writes the command's files.
// internal/ui cannot import this package, so it only sees a function of plain
// strings and its own result type.
//
// realPlanDeps is used for its folder, git, host and spawn calls only. Its ask
// and attach are never reached: the entry point has no prompt and the
// dashboard's pane is the terminal.
func dashStartPlan(root, mode, text string) ui.PlanStartResult {
	out := dashStartPlanWith(startPlanForSurface, realPlanDeps(), root, mode, text)
	return waitPlanHostReady(out, func(root string) error {
		_, err := readMetaWithRetry(root, session.PlanSessionName, attachWait)
		return err
	})
}

// waitPlanHostReady holds a started or resumed result until the planning host
// has written host.json. The dashboard opens the pane the moment this returns,
// and the host publishes host.json a few milliseconds after the spawn, so
// without the wait the first open fails with "reading the host record" and
// only a second try works. A host that never writes it turns the result into a
// refusal that says so, rather than opening a pane that cannot connect.
func waitPlanHostReady(out ui.PlanStartResult, wait func(root string) error) ui.PlanStartResult {
	if out.Outcome != ui.PlanStarted && out.Outcome != ui.PlanResumed {
		return out
	}
	if err := wait(out.Root); err != nil {
		return ui.PlanStartResult{
			Outcome: ui.PlanRefused,
			Root:    out.Root,
			Reason:  "The planning session started but did not write its host record (" + err.Error() + "). Try again; if it keeps happening, look at .afk/master-plan/host.log.",
		}
	}
	return out
}

// dashStartPlanWith is dashStartPlan with the entry point and its dependencies
// passed in, so a test can pin what the adapter maps without a real project.
func dashStartPlanWith(start func(planSurfaceRequest, planDeps) planSurfaceResult, deps planDeps, root, mode, text string) ui.PlanStartResult {
	res := start(planSurfaceRequest{Mode: mode, Project: root, Text: text}, deps)
	out := ui.PlanStartResult{
		Reason:    res.Reason,
		Open:      res.SessionOpen,
		OpenMode:  res.OpenMode,
		OpenPhase: res.OpenPhase,
		Root:      res.Root,
		Archive:   res.Archived,
	}
	// An outcome this adapter does not know stays the zero value, refused, so
	// a new outcome added to the entry point opens no pane until it is mapped.
	switch res.Outcome {
	case planSurfaceStarted:
		out.Outcome = ui.PlanStarted
	case planSurfaceResumed:
		out.Outcome = ui.PlanResumed
	default:
		out.Outcome = ui.PlanRefused
	}
	return out
}
