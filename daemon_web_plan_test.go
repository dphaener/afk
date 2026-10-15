package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// TestWebApprovePlanWith: one row per outcome. Load-bearing rows: "no project"
// (it is a refusal, not a failure, because nothing was changed), "unknown"
// (an outcome this page does not know must read as failed, never as success),
// and the text check on every row (the page shows the dashboard's wording, so
// the text must be approveResultText of the same result).
func TestWebApprovePlanWith(t *testing.T) {
	tests := []struct {
		name string
		in   approveOutcome
		want string
	}{
		{"approved", approveApproved, web.PlanApproved},
		{"already", approveAlreadyApproved, web.PlanApproveAlready},
		{"refused", approveRefused, web.PlanApproveRefused},
		{"no_project", approveNoProject, web.PlanApproveRefused},
		{"failed", approveFailed, web.PlanApproveFailed},
		{"unknown", "other", web.PlanApproveFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var gotRoot string
			res := approveResult{Outcome: tt.in, Reason: "r"}
			approve := func(root string, _ approveDeps) approveResult {
				calls++
				gotRoot = root
				return res
			}
			got := webApprovePlanWith(approve, approveDeps{}, "/proj")
			if calls != 1 || gotRoot != "/proj" {
				t.Errorf("entry point called %d times with %q, want once with /proj", calls, gotRoot)
			}
			if got.Outcome != tt.want || got.Text != approveResultText(res) {
				t.Errorf("got %+v, want outcome %q and the dashboard's text", got, tt.want)
			}
		})
	}
}

// TestWebPlanStatus runs the real file read against temp folders. Load-bearing
// rows: "new at review" (the one approvable state), "extend at review" (a
// finished extend must not offer approving), "damaged" (never approvable),
// "unreadable" (a bad afk.toml is unknown, not "no session"), and the empty,
// never nil, problems on every row (the JSON key must be a list).
func TestWebPlanStatus(t *testing.T) {
	review := func(t *testing.T, root, mode string) {
		writePlanFile(t, root, ".afk/plan/state.json", `{"mode":"`+mode+`","phase":"review","updated_at":"2026-10-12T00:00:00Z"}`)
		for _, f := range []string{"brief.md", "survey.md", "slices.md", "critic.md"} {
			writePlanFile(t, root, ".afk/plan/"+f, "text\n")
		}
		writePlanFile(t, root, ".afk/plan/questions.jsonl", planQuestions("no"))
		writePlanFile(t, root, "docs/PLAN.md", dashPlanCleanText)
	}
	tests := []struct {
		name           string
		setup          func(t *testing.T, root string)
		known, session bool
		damaged        bool
		approvable     bool
		phase          string
	}{
		{"no session", func(t *testing.T, root string) {}, true, false, false, false, ""},
		{"damaged", func(t *testing.T, root string) {
			writePlanFile(t, root, ".afk/plan/state.json", "{not json")
		}, true, true, true, false, ""},
		{"new at review", func(t *testing.T, root string) { review(t, root, "new") }, true, true, false, true, "review"},
		{"new at check", func(t *testing.T, root string) {
			writePlanFile(t, root, ".afk/plan/state.json", planState("check"))
		}, true, true, false, false, ""},
		{"extend at review", func(t *testing.T, root string) { review(t, root, "extend") }, true, true, false, false, ""},
		{"approved", func(t *testing.T, root string) {
			writePlanFile(t, root, ".afk/plan/state.json", planState("approved"))
		}, true, true, false, false, ""},
		{"unreadable", func(t *testing.T, root string) {
			writePlanFile(t, root, "afk.toml", "plan = [")
		}, false, false, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			got := webPlanStatus(root)
			if got.Known != tt.known || got.Session != tt.session || got.Damaged != tt.damaged || got.Approvable != tt.approvable {
				t.Errorf("got %+v, want known=%v session=%v damaged=%v approvable=%v", got, tt.known, tt.session, tt.damaged, tt.approvable)
			}
			if tt.phase != "" && got.Phase != tt.phase {
				t.Errorf("phase = %q, want %q", got.Phase, tt.phase)
			}
			if got.Problems == nil {
				t.Error("problems is nil, want an empty list")
			}
			if got.Known && tt.session && got.Line == "" {
				t.Error("a known session has an empty plan line")
			}
		})
	}
}

// TestWebPlanStatusRunsNoGit: the lookup webPlanStatus hands the loader
// answers "not in a repository", so the root is used as given and a page
// request runs no git. The spy in webPlanStatusWith proves the mapping works
// with any parts; here the real parts' lookup is checked directly.
func TestWebPlanStatusRunsNoGit(t *testing.T) {
	root := t.TempDir()
	calls := 0
	parts := dashPlanStatusParts{gitCommonDir: func(dir string) string { calls++; return "" }}
	got := webPlanStatusWith(parts, root)
	if !got.Known || got.Session {
		t.Errorf("got %+v, want known with no session", got)
	}
	if want := (web.PlanStatus{Known: true, Problems: []string{}, Line: "no planning session"}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestDaemonWebPlanSeams: the daemon's deps offer both plan seams.
func TestDaemonWebPlanSeams(t *testing.T) {
	d := daemonWebDeps(nil, t.TempDir(), usagestate.Deps{}, nil)
	if d.PlanStatus == nil || d.ApprovePlan == nil || d.StartPlan == nil {
		t.Fatalf("a plan seam is nil: %+v", d)
	}
}

// TestWebStartPlanWith: one row per outcome. Load-bearing rows: "unknown
// outcome" (must read as refused, never started), "wait fails on started"
// (the owner is sent to a chat whose host record is missing, so it must be a
// refusal with the shared sentence), "refused does not wait" (a refusal has no
// host to wait for), and the request check on every row (mode, root and text
// pass through exactly, and the provider stays empty).
func TestWebStartPlanWith(t *testing.T) {
	waitErr := errors.New("no host.json")
	tests := []struct {
		name     string
		res      planSurfaceResult
		waitErr  error
		wantWait bool
		want     web.PlanStartResult
	}{
		{"started", planSurfaceResult{Outcome: planSurfaceStarted}, nil, true,
			web.PlanStartResult{Outcome: web.PlanStartStarted}},
		{"resumed", planSurfaceResult{Outcome: planSurfaceResumed}, nil, true,
			web.PlanStartResult{Outcome: web.PlanStartResumed}},
		{"refused", planSurfaceResult{Outcome: planSurfaceRefused, Reason: "nope"}, nil, false,
			web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: "nope"}},
		{"session open", planSurfaceResult{Outcome: planSurfaceRefused, Reason: "open", SessionOpen: true, OpenMode: "extend", OpenPhase: "interview"}, nil, false,
			web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: "open", Open: true, OpenMode: "extend", OpenPhase: "interview"}},
		{"unknown outcome", planSurfaceResult{Outcome: "later"}, nil, false,
			web.PlanStartResult{Outcome: web.PlanStartRefused}},
		{"wait fails on started", planSurfaceResult{Outcome: planSurfaceStarted}, waitErr, true,
			web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: planHostNotReadyReason(waitErr)}},
		{"wait fails on resumed", planSurfaceResult{Outcome: planSurfaceResumed}, waitErr, true,
			web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: planHostNotReadyReason(waitErr)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq planSurfaceRequest
			start := func(r planSurfaceRequest, _ planDeps) planSurfaceResult {
				gotReq = r
				return tt.res
			}
			var waited []string
			wait := func(root string) error {
				waited = append(waited, root)
				return tt.waitErr
			}
			got := webStartPlanWith(start, planDeps{}, wait, "/proj", "amend", "say it")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("result = %+v, want %+v", got, tt.want)
			}
			if gotReq != (planSurfaceRequest{Mode: "amend", Project: "/proj", Text: "say it"}) {
				t.Errorf("request = %+v", gotReq)
			}
			if tt.wantWait != (len(waited) == 1) || (tt.wantWait && waited[0] != "/proj") {
				t.Errorf("waited = %v, want wait %v", waited, tt.wantWait)
			}
		})
	}
}
