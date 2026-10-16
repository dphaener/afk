package web

import (
	"html"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/planlint"
)

// approvable is the planning status of a new plan at review: the one state
// the page offers Approve the plan… for.
func approvable() PlanStatus {
	return PlanStatus{Known: true, Session: true, Mode: "new", Phase: "review", Line: "Plan · new · review — waiting for your approval", Approvable: true, Problems: []string{}}
}

// TestPlanApproveFor is one row per reason the plan page gives for not
// offering Approve the plan…, plus the one row where it is offered.
//
// Load-bearing rows: "errors" and "one error" (a plan with checker errors
// must never be offered, whatever its phase), "extend at review" (only a new
// plan is approved; a change to a plan being built is already in it, so an
// approve there would mean nothing) and the order rows ("no seam beats
// everything", "phase beats errors"), because the order decides which fix the
// owner is sent to.
func TestPlanApproveFor(t *testing.T) {
	clean := planlint.Summary{Tasks: 2, Milestones: 1}
	with := func(f func(*PlanStatus)) PlanStatus {
		st := approvable()
		f(&st)
		return st
	}
	tests := []struct {
		name    string
		status  PlanStatus
		planOK  bool
		summary planlint.Summary
		seam    bool
		want    string
	}{
		{"available", approvable(), true, clean, true, ""},
		{"no seam beats everything", PlanStatus{}, false, planlint.Summary{Errors: 3}, false, "Approving is not available on this page."},
		{"status unknown", PlanStatus{}, true, clean, true, "The planning status could not be read."},
		{"damaged", with(func(s *PlanStatus) { s.Damaged = true }), true, clean, true, "The planning session's state file is damaged, so the plan cannot be approved here."},
		{"no session", PlanStatus{Known: true}, true, clean, true, "There is no planning session, so there is nothing to approve."},
		{"already approved", with(func(s *PlanStatus) { s.Phase, s.Approvable = "approved", false }), true, clean, true, "This plan is already approved."},
		{"extend at review", with(func(s *PlanStatus) { s.Mode, s.Approvable = "extend", false }), true, clean, true, "This planning session changes a plan that is already being built; its changes are in the plan, so there is nothing to approve."},
		{"amend in draft", with(func(s *PlanStatus) { s.Mode, s.Phase, s.Approvable = "amend", "draft", false }), true, clean, true, "This planning session changes a plan that is already being built; its changes are in the plan, so there is nothing to approve."},
		{"new in a middle phase", with(func(s *PlanStatus) { s.Phase, s.Approvable = "draft", false }), true, clean, true, "The plan can be approved once the planning session reaches review; it is at draft now."},
		{"phase beats errors", with(func(s *PlanStatus) { s.Phase, s.Approvable = "check", false }), true, planlint.Summary{Errors: 2}, true, "The plan can be approved once the planning session reaches review; it is at check now."},
		{"at review but not approvable", with(func(s *PlanStatus) { s.Approvable = false }), true, clean, true, "The planning status could not be read."},
		{"plan unreadable", approvable(), false, planlint.Summary{}, true, "The plan could not be read."},
		{"errors", approvable(), true, planlint.Summary{Tasks: 2, Errors: 2, Warnings: 5}, true, "Fix 2 errors before approving."},
		{"one error", approvable(), true, planlint.Summary{Tasks: 2, Errors: 1}, true, "Fix 1 error before approving."},
		{"warnings only", approvable(), true, planlint.Summary{Tasks: 2, Warnings: 4}, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planApproveFor(tc.status, tc.planOK, tc.summary, tc.seam)
			if got.Reason != tc.want || got.Available != (tc.want == "") {
				t.Errorf("planApproveFor = %+v, want reason %q", got, tc.want)
			}
		})
	}
}

// TestPlanApproveSheetFor checks the confirm's words. Load-bearing: the
// singular rows, because "1 tasks" or "1 errors" in the one question asked
// before a build starts reads as a fault; and the path, which must be the
// plan's own so the owner knows which file is about to be built.
func TestPlanApproveSheetFor(t *testing.T) {
	tests := []struct {
		name    string
		summary planlint.Summary
		body    string
	}{
		{"plural", planlint.Summary{Tasks: 12, Milestones: 3, Errors: 0, Warnings: 2},
			"afk starts building 12 tasks in 3 milestones from /x/docs/PLAN.md. The checker found 0 errors and 2 warnings."},
		{"singular", planlint.Summary{Tasks: 1, Milestones: 1, Errors: 1, Warnings: 1},
			"afk starts building 1 task in 1 milestone from /x/docs/PLAN.md. The checker found 1 error and 1 warning."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sh := planApproveSheetFor("my app", "/x/docs/PLAN.md", tc.summary)
			if sh.Body != tc.body {
				t.Errorf("body = %q, want %q", sh.Body, tc.body)
			}
			want := ConfirmSheet{ID: "plan-approve", Title: "Approve the plan for my app?", Body: tc.body,
				Action: "Approve and start", Loading: "Approving…", Method: "post",
				Path: "/p/my%20app/plan/approve", Safe: "Not yet"}
			if sh != want {
				t.Errorf("sheet = %+v\nwant    %+v", sh, want)
			}
		})
	}
}

// TestPlanApproveNeedsTheConfirm is load-bearing: approving commits and pushes
// in the owner's project and starts a build, so a POST without confirm=yes in
// its body (or {"confirm": true}) changes nothing and never reaches the
// seam, on the page and on the API. The address-only row matters most: a
// link someone was sent must not carry the confirm with it.
func TestPlanApproveNeedsTheConfirm(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
	}{
		{"form without confirm", "/p/app/plan/approve", formType, "approve=yes"},
		{"form with confirm=no", "/p/app/plan/approve", formType, "confirm=no"},
		{"empty form", "/p/app/plan/approve", formType, ""},
		{"confirm in the address only", "/p/app/plan/approve?confirm=yes", formType, ""},
		{"API confirm in the address only", "/api/p/app/plan/approve?confirm=yes", jsonType, `{}`},
		{"API confirm false", "/api/p/app/plan/approve", jsonType, `{"confirm": false}`},
		{"API confirm as a string", "/api/p/app/plan/approve", jsonType, `{"confirm": "yes"}`},
		{"API no confirm", "/api/p/app/plan/approve", jsonType, `{}`},
		{"API unreadable JSON", "/api/p/app/plan/approve", jsonType, `{"confirm": tr`},
		{"review-list plan without confirm", "/api/p/draft/plan/approve", jsonType, `{"confirm": false}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := rg.do(http.MethodPost, tc.path, tc.contentType, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if len(rg.approveCalls) != 0 {
				t.Errorf("ApprovePlan ran without the confirm: %v", rg.approveCalls)
			}
			if !strings.Contains(html.UnescapeString(rec.Body.String()), "Nothing changed: approving the plan needs the confirm.") {
				t.Errorf("body = %s", rec.Body.String())
			}
		})
	}
}

// TestPlanApproveOutcomes runs each outcome of the seam through the page and
// the API. Load-bearing: the status codes (a refusal is not a success, and an
// outcome this page does not know is a failure and never a success), the
// text shown whole (it names what the daemon does next and any push problem,
// so cutting it would hide the part the owner needs), and that the page is a
// result page and not a redirect.
func TestPlanApproveOutcomes(t *testing.T) {
	long := "Approved the plan for app and committed it as docs/PLAN.md. afk starts building it with T1.1 within a minute. " +
		strings.Repeat("The push to origin did not finish in time, so the commit is only on this machine for now; run git push in the project folder. ", 4) + "End of the result."
	tests := []struct {
		name     string
		res      PlanApproveResult
		status   int
		outcome  string
		title    string
		wantText string
	}{
		{"approved", PlanApproveResult{Outcome: PlanApproved, Text: long}, http.StatusOK, "approved", "Plan approved", long},
		{"already", PlanApproveResult{Outcome: PlanApproveAlready, Text: "This plan was approved on 12 Oct. Nothing changed."}, http.StatusOK, "already", "Already approved", "This plan was approved on 12 Oct. Nothing changed."},
		{"refused", PlanApproveResult{Outcome: PlanApproveRefused, Text: "The checker found 2 errors. Fix them first."}, http.StatusBadRequest, "refused", "Not approved", "The checker found 2 errors. Fix them first."},
		{"failed", PlanApproveResult{Outcome: PlanApproveFailed, Text: "The plan could not be committed: disk full."}, http.StatusInternalServerError, "failed", "Approval did not finish", "The plan could not be committed: disk full."},
		{"refused with no text", PlanApproveResult{Outcome: PlanApproveRefused}, http.StatusBadRequest, "refused", "Not approved", "The plan was not approved, and no reason was given."},
		{"approved with no text", PlanApproveResult{Outcome: PlanApproved}, http.StatusOK, "approved", "Plan approved", "The plan is approved. afk starts building it."},
		{"an outcome this page does not know", PlanApproveResult{Outcome: "maybe", Text: "Who knows."}, http.StatusInternalServerError, "failed", "Approval did not finish", "This is a fault in afk"},
	}
	for _, tc := range tests {
		t.Run(tc.name+" API", func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.approveResult = tc.res
			rec := rg.do(http.MethodPost, "/api/p/app/plan/approve", jsonType, `{"confirm": true}`)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			m := decode(t, rec)
			wantKeys(t, m, "project", "result")
			res := m["result"].(map[string]any)
			wantKeys(t, res, "outcome", "text")
			if m["project"] != "app" || res["outcome"] != tc.outcome || !strings.Contains(res["text"].(string), tc.wantText) {
				t.Errorf("answer = %s", dump(m))
			}
			if len(rg.approveCalls) != 1 || rg.approveCalls[0] != rg.root {
				t.Errorf("approveCalls = %v, want one call with %s", rg.approveCalls, rg.root)
			}
		})
		t.Run(tc.name+" page", func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.approveResult = tc.res
			rec := rg.pageDo(http.MethodPost, "/p/app/plan/approve", "confirm=yes")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("the page redirected to %q; it must show the result", loc)
			}
			body := html.UnescapeString(rec.Body.String())
			for _, want := range []string{"<h1>" + tc.title + "</h1>", tc.wantText, `href="/p/app/plan" aria-label="Back to the plan of app"`} {
				if !strings.Contains(body, want) {
					t.Errorf("page lacks %q\n%s", want, body)
				}
			}
			if strings.Contains(body, ">Back to the plan</a>") {
				t.Errorf("the page repeats the bar's back button in its content\n%s", body)
			}
			if len(rg.approveCalls) != 1 {
				t.Errorf("approveCalls = %v, want one", rg.approveCalls)
			}
		})
	}
}

// TestPlanApproveWhichProject covers where the approve goes. Load-bearing:
// "review-list plan" (a plan whose project is not configured yet is approved
// through its own folder, the only way such a plan is ever approved from the
// page), "fields in the body are ignored" (no request names a folder), and
// "no seam" (a daemon without the seam has no approve at all: 404, no call).
func TestPlanApproveWhichProject(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*rig)
		path   string
		body   string
		status int
		calls  func(*rig) []string
	}{
		{"configured project", nil, "/api/p/app/plan/approve", `{"confirm": true}`, http.StatusOK,
			func(rg *rig) []string { return []string{rg.root} }},
		{"project name with a space", nil, "/api/p/my%20app/plan/approve", `{"confirm": true}`, http.StatusOK,
			func(rg *rig) []string { return []string{rg.other} }},
		{"review-list plan", nil, "/api/p/draft/plan/approve", `{"confirm": true}`, http.StatusOK,
			func(rg *rig) []string { return []string{rg.draft} }},
		{"fields in the body are ignored", nil, "/api/p/app/plan/approve", `{"confirm": true, "root": "/etc", "path": "/etc/passwd"}`, http.StatusOK,
			func(rg *rig) []string { return []string{rg.root} }},
		{"unknown project", nil, "/api/p/nope/plan/approve", `{"confirm": true}`, http.StatusNotFound,
			func(*rig) []string { return nil }},
		{"no seam", func(rg *rig) { rg.srv.d.ApprovePlan = nil }, "/api/p/app/plan/approve", `{"confirm": true}`, http.StatusNotFound,
			func(*rig) []string { return nil }},
		{"body too large", nil, "/api/p/app/plan/approve", `{"confirm": true, "x": "` + strings.Repeat("a", maxBody) + `"}`, http.StatusRequestEntityTooLarge,
			func(*rig) []string { return nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.do(http.MethodPost, tc.path, jsonType, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			if want := tc.calls(rg); strings.Join(rg.approveCalls, "|") != strings.Join(want, "|") {
				t.Errorf("approveCalls = %v, want %v", rg.approveCalls, want)
			}
		})
	}
}

// TestPlanApprovePage covers the no-JS confirm page. Load-bearing: the
// available row (it is the only way to approve without the script, so it
// must post confirm=yes to the approve address with Not yet focused) and the
// refused rows (the page asks nothing and says why, with no form), and the
// API row (the page has no JSON twin).
func TestPlanApprovePage(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*rig)
		path   string
		status int
		want   []string
		absent []string
	}{
		{
			name:   "available",
			setup:  func(rg *rig) { rg.planStatus[rg.root] = approvable() },
			path:   "/p/app/plan/approve",
			status: http.StatusOK,
			want: []string{
				"<h1>Approve the plan for app?</h1>",
				"afk starts building 2 tasks in 1 milestone from ",
				`<form method="post" action="/p/app/plan/approve"`,
				`<input type="hidden" name="confirm" value="yes">`,
				">Approve and start</button>",
				`href="/p/app/plan" autofocus>Not yet</a>`,
			},
		},
		{
			name: "review-list plan available",
			setup: func(rg *rig) {
				rg.plans[rg.draft] = testPlan
				rg.planStatus[rg.draft] = approvable()
			},
			path:   "/p/draft/plan/approve",
			status: http.StatusOK,
			want:   []string{"<h1>Approve the plan for draft?</h1>", `action="/p/draft/plan/approve"`},
		},
		{
			name: "refused in a middle phase",
			setup: func(rg *rig) {
				st := approvable()
				st.Phase, st.Approvable = "draft", false
				rg.planStatus[rg.root] = st
			},
			path:   "/p/app/plan/approve",
			status: http.StatusBadRequest,
			want:   []string{"The plan can be approved once the planning session reaches review; it is at draft now.", `href="/p/app/plan" aria-label="Back to the plan of app"`},
			absent: []string{"<form", "Approve and start", ">Back to the plan</a>"},
		},
		{
			name:   "refused with no seam",
			setup:  func(rg *rig) { rg.planStatus[rg.root] = approvable(); rg.srv.d.ApprovePlan = nil },
			path:   "/p/app/plan/approve",
			status: http.StatusBadRequest,
			want:   []string{"Approving is not available on this page."},
			absent: []string{"<form"},
		},
		{
			name:   "unknown project",
			path:   "/p/nope/plan/approve",
			status: http.StatusNotFound,
		},
		{
			name:   "API has no confirm page",
			setup:  func(rg *rig) { rg.planStatus[rg.root] = approvable() },
			path:   "/api/p/app/plan/approve",
			status: http.StatusNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.pageDo(http.MethodGet, tc.path, "")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			body := html.UnescapeString(rec.Body.String())
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("page lacks %q\n%s", w, body)
				}
			}
			for _, w := range tc.absent {
				if strings.Contains(body, w) {
					t.Errorf("page holds %q", w)
				}
			}
			if len(rg.approveCalls) != 0 {
				t.Errorf("a GET approved the plan: %v", rg.approveCalls)
			}
		})
	}
}

// TestPlanApproveInThePlanView checks the plan view's two new keys. Load-
// bearing: "no status seam" (known false and problems [] rather than null,
// so a reader can tell "not asked" from a status) and "approvable" (the API
// says available with no reason, so a client can draw the button), and that
// every existing key is still there.
func TestPlanApproveInThePlanView(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*rig)
		path      string
		known     bool
		line      string
		available bool
		reason    string
	}{
		{"approvable", func(rg *rig) { rg.planStatus[rg.root] = approvable() }, "/api/p/app/plan", true, approvable().Line, true, ""},
		{"no planning session", nil, "/api/p/app/plan", true, "", false, "There is no planning session, so there is nothing to approve."},
		{"no status seam", func(rg *rig) { rg.srv.d.PlanStatus = nil }, "/api/p/app/plan", false, "", false, "The planning status could not be read."},
		{"review-list plan with errors", func(rg *rig) { rg.planStatus[rg.draft] = approvable() }, "/api/p/draft/plan", true, approvable().Line, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			m := decode(t, rg.do(http.MethodGet, tc.path, "", ""))
			wantKeys(t, m, "project", "path", "waiting_for_approval", "summary", "findings", "questions", "plan", "problem", "status", "approve")
			st := m["status"].(map[string]any)
			wantKeys(t, st, "known", "session", "damaged", "mode", "phase", "line", "approvable", "problems")
			if st["known"] != tc.known || st["line"] != tc.line {
				t.Errorf("status = %s", dump(st))
			}
			if ps, ok := st["problems"].([]any); !ok || len(ps) != 0 {
				t.Errorf("problems = %v, want []", st["problems"])
			}
			ap := m["approve"].(map[string]any)
			wantKeys(t, ap, "available", "reason")
			if ap["available"] != tc.available {
				t.Errorf("approve = %s", dump(ap))
			}
			if tc.reason != "" && ap["reason"] != tc.reason {
				t.Errorf("reason = %v, want %q", ap["reason"], tc.reason)
			}
			if tc.name == "review-list plan with errors" {
				// draftPlan has no milestones, so the checker finds errors and
				// the reason is the error count, whatever it is.
				if r, _ := ap["reason"].(string); !strings.HasPrefix(r, "Fix ") {
					t.Errorf("reason = %q, want the errors' count", r)
				}
			}
			if m["path"] != filepath.Join(map[string]string{"/api/p/app/plan": rg.root, "/api/p/draft/plan": rg.draft}[tc.path], "docs", "PLAN.md") {
				t.Errorf("path = %v", m["path"])
			}
		})
	}
}
