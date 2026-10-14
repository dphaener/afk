package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dphaener/afk/internal/ui"
)

// TestDashPlanAdapter pins how the entry point's result becomes the
// dashboard's. Load-bearing rows: "refused" (reason carried, no open flag, so
// no offer to open) and "unknown outcome" (falls to refused, so no pane opens
// by accident); "refused because open" keeps the mode and phase for the offer.
func TestDashPlanAdapter(t *testing.T) {
	tests := []struct {
		name string
		res  planSurfaceResult
		want ui.PlanStartResult
	}{
		{"started", planSurfaceResult{Outcome: planSurfaceStarted, Root: "/p", Archived: "/p/.afk/old"},
			ui.PlanStartResult{Outcome: ui.PlanStarted, Root: "/p", Archive: "/p/.afk/old"}},
		{"resumed", planSurfaceResult{Outcome: planSurfaceResumed, Root: "/p"},
			ui.PlanStartResult{Outcome: ui.PlanResumed, Root: "/p"}},
		{"refused", planSurfaceResult{Outcome: planSurfaceRefused, Reason: "no plan here.", Root: "/p"},
			ui.PlanStartResult{Outcome: ui.PlanRefused, Reason: "no plan here.", Root: "/p"}},
		{"refused because open", planSurfaceResult{Outcome: planSurfaceRefused, Reason: "open", SessionOpen: true, OpenMode: "extend", OpenPhase: "rounds", Root: "/p"},
			ui.PlanStartResult{Outcome: ui.PlanRefused, Reason: "open", Open: true, OpenMode: "extend", OpenPhase: "rounds", Root: "/p"}},
		{"unknown outcome", planSurfaceResult{Outcome: "other"},
			ui.PlanStartResult{Outcome: ui.PlanRefused}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got planSurfaceRequest
			start := func(req planSurfaceRequest, _ planDeps) planSurfaceResult {
				got = req
				return tt.res
			}
			out := dashStartPlanWith(start, planDeps{}, "/proj", "amend", "say it")
			if out != tt.want {
				t.Errorf("result = %+v, want %+v", out, tt.want)
			}
			if got.Mode != "amend" || got.Project != "/proj" || got.Text != "say it" {
				t.Errorf("request = %+v, want mode amend, project /proj, text 'say it'", got)
			}
		})
	}
}

// TestDashPlanDepsSetsSeam checks dashDeps wires StartPlan; left nil, the
// three actions would say the feature is not available.
func TestDashPlanDepsSetsSeam(t *testing.T) {
	if dashDeps("", dashLocation{}, nil, -1).StartPlan == nil {
		t.Fatal("dashDeps(...).StartPlan is nil, want dashStartPlan")
	}
}

// TestWaitPlanHostReady pins when the dashboard waits for host.json. The
// started and resumed rows are load-bearing: the pane opens right after, so
// they must wait. A refusal must not wait (nothing was started), and a wait
// that fails must become a refusal so no pane is opened on a missing host.
func TestWaitPlanHostReady(t *testing.T) {
	tests := []struct {
		name      string
		in        ui.PlanStartResult
		waitErr   error
		wantWaits int
		want      ui.PlanStartOutcome
	}{
		{"started waits", ui.PlanStartResult{Outcome: ui.PlanStarted, Root: "/p"}, nil, 1, ui.PlanStarted},
		{"resumed waits", ui.PlanStartResult{Outcome: ui.PlanResumed, Root: "/p"}, nil, 1, ui.PlanResumed},
		{"refused does not wait", ui.PlanStartResult{Outcome: ui.PlanRefused, Root: "/p"}, nil, 0, ui.PlanRefused},
		{"wait fails", ui.PlanStartResult{Outcome: ui.PlanStarted, Root: "/p"}, errors.New("no host.json"), 1, ui.PlanRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waits := 0
			out := waitPlanHostReady(tt.in, func(root string) error {
				waits++
				if root != "/p" {
					t.Errorf("waited on %q, want /p", root)
				}
				return tt.waitErr
			})
			if waits != tt.wantWaits || out.Outcome != tt.want {
				t.Errorf("waits=%d outcome=%v, want waits=%d outcome=%v", waits, out.Outcome, tt.wantWaits, tt.want)
			}
			if tt.waitErr != nil && out.Reason == "" {
				t.Error("failed wait gave no reason")
			}
		})
	}
}

// TestDashPlanNewRootResolved pins that the new mode's root is symlink
// resolved (the switcher compares resolved paths) and that the other modes'
// roots are passed through untouched. The "new" row is load-bearing: a link in
// the typed folder would otherwise add a second switcher row for the project.
func TestDashPlanNewRootResolved(t *testing.T) {
	real := t.TempDir()
	if r, err := filepath.EvalSymlinks(real); err == nil {
		real = r
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, mode, root, want string
	}{
		{"new resolves a link", planSurfaceNew, link, real},
		{"new keeps a root that cannot be resolved", planSurfaceNew, filepath.Join(real, "gone"), filepath.Join(real, "gone")},
		{"new keeps an empty root", planSurfaceNew, "", ""},
		{"extend keeps the root as it is", "extend", link, link},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := func(planSurfaceRequest, planDeps) planSurfaceResult {
				return planSurfaceResult{Outcome: planSurfaceStarted, Root: tt.root}
			}
			if got := dashStartPlanWith(start, planDeps{}, "/x", tt.mode, "").Root; got != tt.want {
				t.Errorf("root = %q, want %q", got, tt.want)
			}
		})
	}
}
