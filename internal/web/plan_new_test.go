package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
)

// TestNewPlanPageFor is the pure decision behind the Plan a new project page.
// Load-bearing rows: "first visit" (no error before the owner typed anything),
// "blank given" (the page's own sentence, and the check is never needed),
// "has plan" (the web's own sentence pointing at Change the plan, never a
// confirm card), "refused" (the check's sentence, which names the folder,
// under the field with the text kept), and the three confirm rows, whose
// badges say which kind of folder the owner is about to plan in. "repo
// without a path" and "unknown kind" prove an answer the page cannot read is
// a refusal and never a card that would start planning nowhere.
func TestNewPlanPageFor(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		given      bool
		chk        PlanFolderCheck
		kind       string
		fieldError string
		path       string
		badge      string
		message    string
	}{
		{name: "first visit", kind: "form"},
		{name: "blank given", given: true, text: "   ", kind: "form", fieldError: planNewBlank},
		{name: "too long", given: true, text: strings.Repeat("a", 4097), kind: "form", fieldError: planNewTooLong},
		{name: "repo", given: true, text: "~/code/shop", chk: PlanFolderCheck{Path: "/home/me/code/shop", Kind: PlanFolderRepo},
			kind: "confirm", path: "/home/me/code/shop", badge: "Existing folder"},
		{name: "plain folder", given: true, text: "/srv/shop", chk: PlanFolderCheck{Path: "/srv/shop", Kind: PlanFolderFolder},
			kind: "confirm", path: "/srv/shop", badge: "Existing folder, becomes a git repository"},
		{name: "open session", given: true, text: "/srv/shop", chk: PlanFolderCheck{Path: "/srv/shop", Kind: PlanFolderOpen},
			kind: "confirm", path: "/srv/shop", badge: "Planning already open"},
		{name: "has plan", given: true, text: "/srv/shop", chk: PlanFolderCheck{Path: "/srv/shop", Kind: PlanFolderHasPlan},
			kind: "has_plan", path: "/srv/shop", message: planNewHasPlan},
		{name: "refused", given: true, text: "~/nope", chk: PlanFolderCheck{Kind: PlanFolderRefused, Reason: "There is no folder at /home/me/nope."},
			kind: "form", fieldError: "There is no folder at /home/me/nope."},
		{name: "refused without a reason", given: true, text: "~/nope", chk: PlanFolderCheck{Kind: PlanFolderRefused},
			kind: "form", fieldError: "afk cannot plan in this folder, and no reason was given. Type the full path of a folder that exists."},
		{name: "repo without a path", given: true, text: "x", chk: PlanFolderCheck{Kind: PlanFolderRepo}, kind: "form", fieldError: planNewUnknown},
		{name: "unknown kind", given: true, text: "x", chk: PlanFolderCheck{Path: "/x", Kind: "exploded"}, kind: "form", fieldError: planNewUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newPlanPageFor(tc.text, tc.given, tc.chk)
			if v.Kind != tc.kind || v.FieldError != tc.fieldError || v.Path != tc.path || v.Badge.Word != tc.badge || v.Message != tc.message {
				t.Errorf("newPlanPageFor = %+v", v)
			}
			if v.Text != strings.TrimSpace(tc.text) {
				t.Errorf("Text = %q, want the typed text kept", v.Text)
			}
		})
	}
}

// TestWithProjectLink: a has_plan page links to the configured project with
// that folder (load-bearing: "Open the project" must be one press), and to
// nothing for a folder no project has, since that link would be a 404.
func TestWithProjectLink(t *testing.T) {
	configured := []Project{{Name: "my app", Root: "/srv/app"}}
	tests := []struct {
		name     string
		v        NewPlanPageView
		wantHref string
	}{
		{"has plan, configured", NewPlanPageView{Kind: newPlanKindHasPlan, Path: "/srv/app/"}, "/p/my%20app"},
		{"has plan, not configured", NewPlanPageView{Kind: newPlanKindHasPlan, Path: "/srv/other"}, ""},
		{"confirm is never linked", NewPlanPageView{Kind: newPlanKindConfirm, Path: "/srv/app"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := withProjectLink(tc.v, configured); got.ProjectHref != tc.wantHref {
				t.Errorf("ProjectHref = %q, want %q", got.ProjectHref, tc.wantHref)
			}
		})
	}
}

func planNewForm(path string) string {
	return url.Values{"confirm": {"yes"}, "path": {path}}.Encode()
}

// noPlanNewSeams fails when the start or the list was reached.
func (rg *rig) noPlanNewSeams() {
	rg.t.Helper()
	if len(rg.startCalls)+len(rg.addedCalls) != 0 {
		rg.t.Errorf("a seam was reached: StartPlan %q, AddBeingPlanned %q", rg.startCalls, rg.addedCalls)
	}
}

// TestPlanNewStarts is the task's happy path. Load-bearing: the seam is asked
// once, in mode new, for the full path the card showed (not the typed text);
// the project is then noted under the root the entry point resolved (or the
// checked path when it gave none); the page lands 303 on that project's chat
// with the started or resumed toast, and the API answers the same facts as
// JSON. "configured folder" proves a folder a configured project has lands on
// that project's chat, since the list's own name would be dropped there.
func TestPlanNewStarts(t *testing.T) {
	tests := []struct {
		name     string
		outcome  string
		resRoot  string
		addName  string
		atRoot   bool
		wantRoot string
		wantChat string
	}{
		{name: "started", outcome: PlanStartStarted, addName: "shop", wantChat: "/p/shop/plan/chat"},
		{name: "resumed", outcome: PlanStartResumed, addName: "shop", wantChat: "/p/shop/plan/chat"},
		{name: "resolved root", outcome: PlanStartStarted, resRoot: "/real/shop", addName: "shop-2", wantRoot: "/real/shop", wantChat: "/p/shop-2/plan/chat"},
		{name: "configured folder", outcome: PlanStartStarted, addName: "app-2", atRoot: true, wantChat: "/p/app/plan/chat"},
	}
	for _, tc := range tests {
		setup := func(t *testing.T) *rig {
			rg := newRig(t, Config{})
			if tc.atRoot {
				rg.checkResult = PlanFolderCheck{Path: rg.root, Kind: PlanFolderRepo}
			}
			rg.startResult = PlanStartResult{Outcome: tc.outcome, Root: tc.resRoot}
			rg.addedName = tc.addName
			return rg
		}
		check := func(t *testing.T, rg *rig) {
			t.Helper()
			path := rg.checkResult.Path
			wantRoot := tc.wantRoot
			if wantRoot == "" {
				wantRoot = path
			}
			if len(rg.checkCalls) != 1 || rg.checkCalls[0] != "~/code/shop" {
				t.Errorf("checkCalls = %q, want the typed text once", rg.checkCalls)
			}
			if len(rg.startCalls) != 1 || strings.Join(rg.startCalls[0], "|") != path+"|new|" {
				t.Errorf("startCalls = %q, want one call (%s, new, \"\")", rg.startCalls, path)
			}
			if len(rg.addedCalls) != 1 || rg.addedCalls[0] != wantRoot {
				t.Errorf("addedCalls = %q, want [%s]", rg.addedCalls, wantRoot)
			}
		}
		t.Run("page "+tc.name, func(t *testing.T) {
			rg := setup(t)
			rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("  ~/code/shop "))
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303\n%s", rec.Code, rec.Body.String())
			}
			if got, want := rec.Header().Get("Location"), tc.wantChat+"?done="+tc.outcome; got != want {
				t.Errorf("Location = %q, want %q", got, want)
			}
			check(t, rg)
		})
		t.Run("api "+tc.name, func(t *testing.T) {
			rg := setup(t)
			rec := rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "~/code/shop"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			m := decode(t, rec)
			wantRoot := tc.wantRoot
			if wantRoot == "" {
				wantRoot = rg.checkResult.Path
			}
			project := strings.TrimSuffix(strings.TrimPrefix(tc.wantChat, "/p/"), "/plan/chat")
			if m["path"] != rg.checkResult.Path || m["root"] != wantRoot || m["project"] != project ||
				m["outcome"] != tc.outcome || m["chat"] != tc.wantChat || m["notice"] != "" {
				t.Errorf("body = %v", m)
			}
			check(t, rg)
		})
	}
}

// TestPlanNewNeedsTheConfirm is load-bearing: without confirm=yes (or
// {"confirm": true}) in the body, nothing is checked, started or noted. A
// confirm in the address does not count, since a link someone was sent
// would otherwise start planning on a single press.
func TestPlanNewNeedsTheConfirm(t *testing.T) {
	tests := []struct {
		name, path, ctype, body string
	}{
		{"page, no confirm", "/plan/new", formType, "path=%7E%2Fcode%2Fshop"},
		{"page, confirm=no", "/plan/new", formType, "confirm=no&path=%7E%2Fcode%2Fshop"},
		{"page, confirm in the query", "/plan/new?confirm=yes", formType, "path=%7E%2Fcode%2Fshop"},
		{"page, empty body", "/plan/new", formType, ""},
		{"api, no confirm", "/api/plan/new", jsonType, `{"path": "~/code/shop"}`},
		{"api, confirm false", "/api/plan/new", jsonType, `{"confirm": false, "path": "~/code/shop"}`},
		{"api, confirm as a word", "/api/plan/new", jsonType, `{"confirm": "yes", "path": "~/code/shop"}`},
		{"api, confirm in the query", "/api/plan/new?confirm=yes", jsonType, `{"path": "~/code/shop"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			var rec *httptest.ResponseRecorder
			if isAPI(tc.path) {
				rec = rg.do(http.MethodPost, tc.path, tc.ctype, tc.body)
			} else {
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
				req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie(testToken)})
				req.Header.Set("Content-Type", tc.ctype)
				rec = httptest.NewRecorder()
				rg.h.ServeHTTP(rec, req)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if isAPI(tc.path) {
				if m := decode(t, rec); m["error"] != planNewNoConfirm || m["reason"] != "bad_request" {
					t.Errorf("body = %v", m)
				}
			} else if body := rec.Body.String(); !strings.Contains(body, planNewNoConfirm) || !strings.Contains(body, `name="path"`) {
				t.Errorf("the form was not drawn again with the refusal:\n%s", body)
			}
			rg.noPlanNewSeams()
			if len(rg.checkCalls) != 0 {
				t.Errorf("the folder was checked without the confirm: %q", rg.checkCalls)
			}
		})
	}
}

// TestPlanNewRefusals covers what the page refuses itself: a blank or
// overlong path, before the folder check (load-bearing: the check is never
// asked about nothing), and a body that cannot be read.
func TestPlanNewRefusals(t *testing.T) {
	tests := []struct {
		name, path, ctype, body, want, keep string
	}{
		{name: "page blank", path: "/plan/new", ctype: formType, body: "confirm=yes&path=+++", want: "Nothing started: " + planNewBlank},
		{name: "page too long", path: "/plan/new", ctype: formType, body: planNewForm(strings.Repeat("a", 4097)), want: "Nothing started: " + planNewTooLong},
		{name: "api blank", path: "/api/plan/new", ctype: jsonType, body: `{"confirm": true, "path": ""}`, want: "Nothing started: " + planNewBlank},
		{name: "api no path", path: "/api/plan/new", ctype: jsonType, body: `{"confirm": true}`, want: "Nothing started: " + planNewBlank},
		{name: "api path not a string", path: "/api/plan/new", ctype: jsonType, body: `{"confirm": true, "path": 7}`, want: planNewUnreadable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			var rec *httptest.ResponseRecorder
			if isAPI(tc.path) {
				rec = rg.do(http.MethodPost, tc.path, tc.ctype, tc.body)
			} else {
				rec = rg.pageDo(http.MethodPost, tc.path, tc.body)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if isAPI(tc.path) {
				if m := decode(t, rec); m["error"] != tc.want || m["reason"] != "bad_request" {
					t.Errorf("body = %v", m)
				}
			} else if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("the page does not say %q", tc.want)
			}
			if len(rg.checkCalls) != 0 {
				t.Errorf("the folder was checked: %q", rg.checkCalls)
			}
			rg.noPlanNewSeams()
		})
	}
}

// TestPlanNewNotFound is load-bearing: a folder the check refuses (here, one
// that does not exist) is never started and never created; the check's own
// sentence, naming the folder, is what the owner reads, with the typed path
// kept in the field.
func TestPlanNewNotFound(t *testing.T) {
	const reason = "There is no folder at /home/me/code/nope. Type the path of a folder that exists."
	for _, api := range []bool{false, true} {
		name := "page"
		if api {
			name = "api"
		}
		t.Run(name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.checkResult = PlanFolderCheck{Kind: PlanFolderRefused, Reason: reason}
			var rec *httptest.ResponseRecorder
			if api {
				rec = rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "~/code/nope"}`)
			} else {
				rec = rg.pageDo(http.MethodPost, "/plan/new", planNewForm("~/code/nope"))
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if api {
				if m := decode(t, rec); m["error"] != reason || m["reason"] != "refused" {
					t.Errorf("body = %v", m)
				}
			} else {
				body := rec.Body.String()
				for _, w := range []string{`<p class="field__error" id="plan-new-error" role="alert">` + reason, `value="~/code/nope"`, `aria-invalid="true"`} {
					if !strings.Contains(body, w) {
						t.Errorf("the page does not contain %q", w)
					}
				}
			}
			rg.noPlanNewSeams()
		})
	}
}

// TestPlanNewHasPlan is load-bearing: a folder that already has a plan is
// refused before the seam, pointing at Change the plan, and linked to the
// configured project that has it. A folder no configured project has gets
// the sentence and no link.
func TestPlanNewHasPlan(t *testing.T) {
	tests := []struct {
		name       string
		configured bool
		api        bool
	}{
		{"page, configured", true, false},
		{"page, not configured", false, false},
		{"api", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			path := rg.fresh
			if tc.configured {
				path = rg.root
			}
			rg.checkResult = PlanFolderCheck{Path: path, Kind: PlanFolderHasPlan}
			if tc.api {
				rec := rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "x"}`)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400", rec.Code)
				}
				if m := decode(t, rec); m["error"] != planNewHasPlan || m["reason"] != "has_plan" || m["path"] != path {
					t.Errorf("body = %v", m)
				}
				rg.noPlanNewSeams()
				return
			}
			rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("x"))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, planNewHasPlan) {
				t.Errorf("the page does not say %q", planNewHasPlan)
			}
			if link := `<a class="btn btn--primary" href="/p/app">Open app</a>`; strings.Contains(body, link) != tc.configured {
				t.Errorf("project link drawn = %v, want %v", !tc.configured, tc.configured)
			}
			if strings.Contains(body, "Start planning") {
				t.Errorf("a folder with a plan was offered Start planning")
			}
			rg.noPlanNewSeams()
		})
	}
}

// TestPlanNewSeamRefusals: the check said yes, but the entry point refused
// when it re-checked (a session opened, a plan appeared). Load-bearing: its
// sentence is what the owner reads at 400, the form comes back with the path
// kept, and the project is never noted as being planned.
func TestPlanNewSeamRefusals(t *testing.T) {
	tests := []struct {
		name       string
		res        PlanStartResult
		wantError  string
		wantReason string
	}{
		{"refused", PlanStartResult{Outcome: PlanStartRefused, Reason: "There is already a plan in /srv/shop."}, "There is already a plan in /srv/shop.", "refused"},
		{"session open", PlanStartResult{Outcome: PlanStartRefused, Reason: "A planning session is already open (amend, draft).", Open: true}, "A planning session is already open (amend, draft).", "session_open"},
		{"no reason", PlanStartResult{Outcome: PlanStartRefused}, "Nothing was started, and no reason was given. Run afk plan status in the folder to see where the plan is now.", "refused"},
		{"unknown outcome", PlanStartResult{Outcome: "exploded"}, "Starting the planning session gave an answer this page does not know. This is a fault in afk; please report it. Run afk plan status in the folder to see where the plan is now.", "refused"},
	}
	for _, tc := range tests {
		t.Run("api "+tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.startResult = tc.res
			rec := rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "~/code/shop"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if m := decode(t, rec); m["error"] != tc.wantError || m["reason"] != tc.wantReason {
				t.Errorf("body = %v", m)
			}
			if len(rg.addedCalls) != 0 {
				t.Errorf("a refused start was noted as being planned: %q", rg.addedCalls)
			}
		})
		t.Run("page "+tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.startResult = tc.res
			rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("~/code/shop"))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			body := rec.Body.String()
			for _, w := range []string{`<div class="notice__body">` + tc.wantError, `value="~/code/shop"`} {
				if !strings.Contains(body, w) {
					t.Errorf("the page does not contain %q", w)
				}
			}
			if len(rg.addedCalls) != 0 {
				t.Errorf("a refused start was noted as being planned: %q", rg.addedCalls)
			}
		})
	}
}

// TestPlanNewListFailure: the session started but the list could not be
// written. Load-bearing: the owner is told (a redirect could not carry it),
// the API says it in notice, and no chat link is offered for a name that
// would answer 404.
func TestPlanNewListFailure(t *testing.T) {
	t.Run("page", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.addedName, rg.addedErr = "", errors.New("disk full")
		rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("~/code/shop"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, w := range []string{"Planning started in " + rg.fresh, "could not add the project to this page&#39;s list (disk full)"} {
			if !strings.Contains(body, w) {
				t.Errorf("the page does not contain %q", w)
			}
		}
		if strings.Contains(body, "Open its chat") {
			t.Errorf("a chat link was offered for a project the page cannot find")
		}
	})
	t.Run("api", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.addedName, rg.addedErr = "", errors.New("disk full")
		m := decode(t, rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "~/code/shop"}`))
		if m["outcome"] != PlanStartStarted || m["project"] != "" || m["chat"] != "" || !strings.Contains(m["notice"].(string), "disk full") {
			t.Errorf("body = %v", m)
		}
	})
}

// TestPlanNewPage is the GET: the Where form, and with ?path= the confirm
// card. Load-bearing: the GET never starts or notes anything, whatever it is
// given, and the card carries the confirm and the full path in its form.
func TestPlanNewPage(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		chk       *PlanFolderCheck
		want      []string
		notWant   []string
		wantCheck int
	}{
		{name: "form", path: "/plan/new",
			want: []string{`<label class="field__label" for="plan-new-path">Where</label>`, `class="input input--mono"`, `name="path"`,
				`<span class="field__hint" id="plan-new-hint">Folder path</span>`, `<form method="get" action="/plan/new"`, `href="/">Cancel</a>`},
			notWant: []string{"field__error", "Start planning"}},
		{name: "blank given", path: "/plan/new?path=+",
			want: []string{`<p class="field__error" id="plan-new-error" role="alert">` + planNewBlank, `aria-invalid="true"`, `aria-describedby="plan-new-error plan-new-hint"`}},
		{name: "confirm card", path: "/plan/new?path=%7E%2Fcode%2Fshop", wantCheck: 1,
			want: []string{`<h2 id="plan-new-card-title">afk will plan in</h2>`, `<p class="path">`, `<span class="badge badge--idle">Existing folder</span>`,
				`<form method="post" action="/plan/new"`, `<input type="hidden" name="confirm" value="yes">`, `<input type="hidden" name="path" value="`,
				`<button type="submit" class="btn btn--primary" data-loading-label="Starting planning">Start planning</button>`,
				`<a class="btn btn--quiet" href="/" autofocus>Cancel</a>`}},
		{name: "plain folder", path: "/plan/new?path=x", wantCheck: 1, chk: &PlanFolderCheck{Path: "/srv/x", Kind: PlanFolderFolder},
			want: []string{"Existing folder, becomes a git repository", `<p class="path">/srv/x</p>`}},
		{name: "open session", path: "/plan/new?path=x", wantCheck: 1, chk: &PlanFolderCheck{Path: "/srv/x", Kind: PlanFolderOpen},
			want: []string{`<span class="badge badge--running">Planning already open</span>`}},
		{name: "refused", path: "/plan/new?path=nope", wantCheck: 1, chk: &PlanFolderCheck{Kind: PlanFolderRefused, Reason: "Type a full path, such as ~/code/shop. nope is not one."},
			want: []string{"Type a full path, such as ~/code/shop. nope is not one.", `value="nope"`}, notWant: []string{"Start planning"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.chk != nil {
				rg.checkResult = *tc.chk
			}
			rec := rg.pageDo(http.MethodGet, tc.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("the page does not contain %q", w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("the page contains %q", w)
				}
			}
			if len(rg.checkCalls) != tc.wantCheck {
				t.Errorf("checkCalls = %q, want %d", rg.checkCalls, tc.wantCheck)
			}
			rg.noPlanNewSeams()
		})
	}
}

// TestPlanNewOverviewLinks: the overview's footer and the desktop top bar
// carry Plan a new project when the seams are there, and neither is drawn
// (and both addresses are not found) when they are not, so no button leads
// to a 404.
func TestPlanNewOverviewLinks(t *testing.T) {
	footer := `<div class="btn-stack overview-foot">`
	button := `<a class="btn btn--primary" href="/plan/new">Plan a new project</a>`
	topnav := `<a class="btn btn--primary btn--sm topnav__action" href="/plan/new">Plan a new project</a>`

	t.Run("offered", func(t *testing.T) {
		rg := newRig(t, Config{})
		body := rg.pageDo(http.MethodGet, "/", "").Body.String()
		for _, w := range []string{footer, button, topnav} {
			if !strings.Contains(body, w) {
				t.Errorf("the overview does not contain %q", w)
			}
		}
		if !strings.Contains(rg.pageDo(http.MethodGet, "/usage", "").Body.String(), topnav) {
			t.Errorf("the top bar of another page lacks the action")
		}
	})
	t.Run("not offered", func(t *testing.T) {
		_, h, err := build(Config{Token: testToken}, Deps{
			LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil }, Sessions: func(string) []SessionView { return nil },
			Usage: func() (usagestate.File, bool) { return usagestate.File{}, false },
			Pause: func(string) error { return nil }, Resume: func(string) error { return nil },
		})
		must(t, err)
		get := func(path string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie(testToken)})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}
		if body := get("/").Body.String(); strings.Contains(body, "/plan/new") {
			t.Errorf("the overview offers Plan a new project without the seams")
		}
		if rec := get("/plan/new"); rec.Code != http.StatusNotFound {
			t.Errorf("GET /plan/new = %d, want 404", rec.Code)
		}
	})
}
