package ui

import (
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
)

// This file is how the dashboard learns the planning session's state and the
// plan checker's result. Like builddata.go it only fetches and stores; the
// reading is Deps.LoadPlanStatus, wired by the root package, which knows where
// a project's plan files are. Nothing here or in the views reads a file.

// planStatusLoadedMsg carries one finished read. gen is the project generation
// the read was started under, so a read that crosses a project switch is
// dropped and never shows the old project's plan under the new project's name.
type planStatusLoadedMsg struct {
	status PlanStatus
	gen    int
}

// planStatusCmd reads the plan status for the active project. It returns nil
// when no loader is wired, so an unwired dashboard issues no command.
//
// The loader runs inside the returned closure, on the command goroutine, never
// on the Update goroutine or in a View. The guard follows buildCmd's: one slow
// read (a stalled disk) must not be piled up behind by the refresh ticker, and
// it is released in this goroutine so a dropped message cannot leave it held.
func (m Model) planStatusCmd() tea.Cmd {
	load := m.deps.LoadPlanStatus
	if load == nil {
		return nil
	}
	repo := m.repo
	gen := m.projectGen
	refreshing := m.planStatusRefreshing
	return func() tea.Msg {
		// A nil guard is a Model built as a bare struct literal, which only a
		// test does.
		if refreshing != nil {
			if !refreshing.CompareAndSwap(false, true) {
				return nil
			}
			defer refreshing.Store(false)
		}
		return planStatusLoadedMsg{status: load(repo), gen: gen}
	}
}

// applyPlanStatusLoaded stores a finished read unless it belongs to a project
// the dashboard has since left. Known is set here and not by the loader, so a
// loader cannot forget it and leave the line hidden.
func (m Model) applyPlanStatusLoaded(msg planStatusLoadedMsg) Model {
	if msg.gen != m.projectGen {
		return m
	}
	msg.status.Known = true
	m.planStatus = msg.status
	// The Build tab's lines come from the status, so the viewport the scroll
	// keys move is re-seeded, as applyBuildLoaded does.
	m.syncDetailViewport()
	return m
}

// forgetPlanStatus drops the status on a project switch so the outgoing
// project's plan is not shown under the incoming project's name. The guard is
// replaced, not reset, for the reason forgetBuild gives.
func (m *Model) forgetPlanStatus() {
	m.planStatus = PlanStatus{}
	if m.planStatusRefreshing != nil {
		m.planStatusRefreshing = new(atomic.Bool)
	}
}
