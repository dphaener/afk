package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/plansession"
)

// TestChangeRefusal is the pure part of the change POST's checks. Load-bearing
// rows: "new" (a new plan is started from the dashboard; accepting it here
// would start a second plan over a built one), the blank amend and reconcile
// rows (the seam must never be asked to amend nothing), and "extend with text"
// (extend takes no text, so none of it is a refusal).
func TestChangeRefusal(t *testing.T) {
	tests := []struct {
		name, mode, text, want string
	}{
		{"extend", "extend", "", ""},
		{"extend with text", "extend", "ignored", ""},
		{"amend", "amend", "Add a CSV export task.", ""},
		{"reconcile", "reconcile", "docs/design-system.md", ""},
		{"new", "new", "", "Nothing started: new is not a way to change a plan. Use extend, amend or reconcile."},
		{"empty mode", "", "x", "Nothing started:  is not a way to change a plan. Use extend, amend or reconcile."},
		{"unknown mode", "delete", "x", "Nothing started: delete is not a way to change a plan. Use extend, amend or reconcile."},
		{"blank amend", "amend", "", "Nothing started: say what to change in the plan."},
		{"blank reconcile", "reconcile", "", "Nothing started: name the document to reconcile the plan with, as a path inside the project."},
		{"too long", "amend", strings.Repeat("é", 5001), "Nothing started: that is too long. Keep it under 5000 characters."},
		{"at the limit", "amend", strings.Repeat("é", 5000), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := changeRefusal(tc.mode, tc.text); got != tc.want {
				t.Errorf("changeRefusal(%q, %q) = %q, want %q", tc.mode, tc.text, got, tc.want)
			}
		})
	}
}

func changeForm(mode, text string) string {
	v := url.Values{"confirm": {"yes"}, "mode": {mode}}
	if text != "" {
		v.Set("text", text)
	}
	return v.Encode()
}

// TestPlanChangeStarts is load-bearing for the three modes: each reaches the
// seam once with the project's own root, the mode and the trimmed text (extend
// sends none), and the page answers a 303 to the chat with the matching toast
// code while the API answers 200 JSON naming the chat. The "resumed" rows
// prove a resumed session is answered like a started one.
func TestPlanChangeStarts(t *testing.T) {
	tests := []struct {
		name, mode, text, sent, outcome string
	}{
		{"extend", "extend", "", "", PlanStartStarted},
		{"extend drops text", "extend", "ignored", "", PlanStartStarted},
		{"amend trimmed", "amend", "  Add a CSV export task.  ", "Add a CSV export task.", PlanStartStarted},
		{"reconcile", "reconcile", "docs/design-system.md", "docs/design-system.md", PlanStartStarted},
		{"resumed", "amend", "Add a thing", "Add a thing", PlanStartResumed},
	}
	for _, tc := range tests {
		t.Run("page "+tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.startResult = PlanStartResult{Outcome: tc.outcome}
			rec := rg.pageDo(http.MethodPost, "/p/app/plan/change", changeForm(tc.mode, tc.text))
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303\n%s", rec.Code, rec.Body.String())
			}
			if got, want := rec.Header().Get("Location"), "/p/app/plan/chat?done="+tc.outcome; got != want {
				t.Errorf("Location = %q, want %q", got, want)
			}
			if len(rg.startCalls) != 1 || strings.Join(rg.startCalls[0], "|") != rg.root+"|"+tc.mode+"|"+tc.sent {
				t.Errorf("startCalls = %q, want one call (%s, %s, %q)", rg.startCalls, rg.root, tc.mode, tc.sent)
			}
		})
		t.Run("api "+tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.startResult = PlanStartResult{Outcome: tc.outcome}
			body := `{"confirm": true, "mode": "` + tc.mode + `", "text": "` + tc.text + `"}`
			rec := rg.do(http.MethodPost, "/api/p/app/plan/change", jsonType, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			m := decode(t, rec)
			if m["project"] != "app" || m["mode"] != tc.mode || m["outcome"] != tc.outcome || m["text"] != tc.sent ||
				m["chat"] != "/p/app/plan/chat" || m["session_open"] != false {
				t.Errorf("body = %v", m)
			}
			if len(rg.startCalls) != 1 || strings.Join(rg.startCalls[0], "|") != rg.root+"|"+tc.mode+"|"+tc.sent {
				t.Errorf("startCalls = %q", rg.startCalls)
			}
		})
	}
}

// TestPlanChangeSeamRefusals is load-bearing: the seam's own sentence is what
// the owner reads, uncut, at 400, and an open session is told apart in the
// JSON (session_open, with its mode and phase and a link to the chat). An
// unknown outcome must never read as a success, and an empty reason must not
// leave a blank page.
func TestPlanChangeSeamRefusals(t *testing.T) {
	tests := []struct {
		name       string
		res        PlanStartResult
		wantError  string
		wantReason string
		open       bool
	}{
		{"refused", PlanStartResult{Outcome: PlanStartRefused, Reason: "There is no plan to change yet."},
			"There is no plan to change yet.", "refused", false},
		{"session open", PlanStartResult{Outcome: PlanStartRefused, Reason: "A planning session is already open (amend, draft).", Open: true, OpenMode: "amend", OpenPhase: "draft"},
			"A planning session is already open (amend, draft).", "session_open", true},
		{"unknown outcome", PlanStartResult{Outcome: "exploded"},
			"Starting the planning session gave an answer this page does not know. This is a fault in afk; please report it. Run afk plan status in the project folder to see where the plan is now.", "refused", false},
		{"no reason", PlanStartResult{Outcome: PlanStartRefused},
			"Nothing was started, and no reason was given. Run afk plan status in the project folder to see where the plan is now.", "refused", false},
	}
	for _, tc := range tests {
		t.Run("api "+tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.startResult = tc.res
			rec := rg.do(http.MethodPost, "/api/p/app/plan/change", jsonType, `{"confirm": true, "mode": "extend"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			m := decode(t, rec)
			if m["error"] != tc.wantError || m["reason"] != tc.wantReason || m["session_open"] != tc.open || m["chat"] != "/p/app/plan/chat" {
				t.Errorf("body = %v", m)
			}
			if tc.open && (m["open_mode"] != tc.res.OpenMode || m["open_phase"] != tc.res.OpenPhase) {
				t.Errorf("open fields = %v / %v", m["open_mode"], m["open_phase"])
			}
		})
		t.Run("page "+tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.startResult = tc.res
			rec := rg.pageDo(http.MethodPost, "/p/app/plan/change", changeForm("extend", ""))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), strings.ReplaceAll(tc.wantError, "'", "&#39;")) {
				t.Errorf("page lacks %q\n%s", tc.wantError, rec.Body.String())
			}
		})
	}
}

// TestPlanChangeRefusesBeforeTheSeam is load-bearing: every row is a request
// the page itself must turn away with a 400 and zero seam calls, because the
// seam would start a real planning agent. The "new" and unknown-mode rows
// keep a hand-written request from starting anything the three buttons do
// not offer; the blank rows keep an amend or reconcile from starting empty.
func TestPlanChangeRefusesBeforeTheSeam(t *testing.T) {
	tests := []struct {
		name, mode, text, want string
	}{
		{"unknown mode", "delete", "x", "delete is not a way to change a plan"},
		{"new", "new", "", "new is not a way to change a plan"},
		{"empty mode", "", "", "is not a way to change a plan"},
		{"blank amend", "amend", "", "say what to change in the plan"},
		{"whitespace amend", "amend", "    ", "say what to change in the plan"},
		{"blank reconcile", "reconcile", "", "name the document to reconcile"},
		{"whitespace reconcile", "reconcile", "  ", "name the document to reconcile"},
		{"too long", "amend", strings.Repeat("x", 5001), "too long"},
	}
	for _, tc := range tests {
		t.Run("page "+tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			v := url.Values{"confirm": {"yes"}, "mode": {tc.mode}, "text": {tc.text}}
			rec := rg.pageDo(http.MethodPost, "/p/app/plan/change", v.Encode())
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("status = %d, want 400 with %q\n%s", rec.Code, tc.want, rec.Body.String())
			}
			if len(rg.startCalls) != 0 {
				t.Errorf("the seam was called: %v", rg.startCalls)
			}
		})
		t.Run("api "+tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			body := `{"confirm": true, "mode": "` + tc.mode + `", "text": "` + tc.text + `"}`
			rec := rg.do(http.MethodPost, "/api/p/app/plan/change", jsonType, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			m := decode(t, rec)
			if e, _ := m["error"].(string); !strings.Contains(e, tc.want) || m["reason"] != "bad_request" {
				t.Errorf("body = %v", m)
			}
			if len(rg.startCalls) != 0 {
				t.Errorf("the seam was called: %v", rg.startCalls)
			}
		})
	}
}

// TestPlanChangeNeedsTheConfirm is load-bearing: starting a planning agent
// is an action, so a POST without the confirm in its body changes nothing.
// The address-only rows matter most: a link someone was sent must not carry
// the confirm with it.
func TestPlanChangeNeedsTheConfirm(t *testing.T) {
	tests := []struct {
		name, path, contentType, body string
	}{
		{"form without confirm", "/p/app/plan/change", formType, "mode=extend"},
		{"form with confirm=no", "/p/app/plan/change", formType, "mode=extend&confirm=no"},
		{"confirm in the address only", "/p/app/plan/change?confirm=yes&mode=extend", formType, "mode=extend"},
		{"API confirm in the address only", "/api/p/app/plan/change?confirm=yes", jsonType, `{"mode": "extend"}`},
		{"API confirm false", "/api/p/app/plan/change", jsonType, `{"confirm": false, "mode": "extend"}`},
		{"API confirm as a string", "/api/p/app/plan/change", jsonType, `{"confirm": "yes", "mode": "extend"}`},
		{"API unreadable JSON", "/api/p/app/plan/change", jsonType, `{"confirm": tr`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := rg.do(http.MethodPost, tc.path, tc.contentType, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if len(rg.startCalls) != 0 {
				t.Errorf("the seam was called without the confirm: %v", rg.startCalls)
			}
		})
	}
}

// TestPlanChangeNotFound is load-bearing for the project lookup and the
// optional seam: a name that is not a configured project (a plan on the
// review list is not a change target) and a daemon with no seam both answer
// 404 and start nothing.
func TestPlanChangeNotFound(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*rig)
		path  string
	}{
		{"unknown project", func(*rig) {}, "/api/p/nope/plan/change"},
		{"page unknown project", func(*rig) {}, "/p/nope/plan/change"},
		{"nil seam", func(rg *rig) { rg.srv.d.StartPlan = nil }, "/api/p/app/plan/change"},
		{"page nil seam", func(rg *rig) { rg.srv.d.StartPlan = nil }, "/p/app/plan/change"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			tc.setup(rg)
			rec := rg.do(http.MethodPost, tc.path, jsonType, `{"confirm": true, "mode": "extend"}`)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404\n%s", rec.Code, rec.Body.String())
			}
			if len(rg.startCalls) != 0 {
				t.Errorf("the seam was called: %v", rg.startCalls)
			}
		})
	}
}

// TestPlanChatToasts is load-bearing for the shared toast codes: "started"
// and "resumed" also mean other things on other screens, so the chat must
// read its own sentences, and an unknown code shows nothing.
func TestPlanChatToasts(t *testing.T) {
	tests := []struct{ code, want string }{
		{"started", "The planning session started. Its first message appears here when the agent has read the plan."},
		{"resumed", "The planning session was already open; here it is."},
		{"sent", "Sent to the planning agent."},
		{"paused", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			if got := chatToast(tc.code); got != tc.want {
				t.Errorf("chatToast(%q) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

// TestChangePageFor is the change page's whole decision. Load-bearing rows:
// "open session" (no form: a second session cannot start, and the one thing
// to do is open the chat), "blank text" (the field error and no POST form, so
// nothing is posted), "no plan" (no form either), and "text is escaped" (the
// sentence is the owner's, shown back to them inside a page). "no plan" is
// listed before "open session" to show the order: a project with no plan
// never gets a chat link.
func TestChangePageFor(t *testing.T) {
	plan := changeState{HasPlan: true}
	open := changeState{HasPlan: true, Open: true, OpenMode: "amend", OpenPhase: "draft"}
	tests := []struct {
		name, mode, text string
		given            bool
		st               changeState
		want             ChangePageView
	}{
		{name: "no plan", mode: "extend", st: changeState{Open: true, OpenMode: "x", OpenPhase: "y"},
			want: ChangePageView{Kind: changeKindNone, Title: "Change the plan", Message: "There is no plan to change yet."}},
		{name: "open session", mode: "amend", text: "x", given: true, st: open,
			want: ChangePageView{Kind: changeKindOpen, Title: "Change the plan", Message: "A planning session is already open (amend, draft).", Text: "x"}},
		{name: "unknown mode", mode: "new", st: plan,
			want: ChangePageView{Kind: changeKindUnknown, Title: "Change the plan", Message: "Nothing started: new is not a way to change a plan. Use extend, amend or reconcile."}},
		{name: "no mode", mode: "", st: plan,
			want: ChangePageView{Kind: changeKindUnknown, Title: "Change the plan", Message: "Nothing started:  is not a way to change a plan. Use extend, amend or reconcile."}},
		{name: "extend", mode: "extend", text: "ignored", given: true, st: plan,
			want: ChangePageView{Kind: changeKindConfirm, Title: "Extend the plan for app?", Lede: "A planning agent reads the plan and asks you what to add. Its chat opens next."}},
		{name: "amend, first visit", mode: "amend", st: plan,
			want: ChangePageView{Kind: changeKindForm, Title: "Amend the plan for app", Label: "What should change?", Textarea: true}},
		{name: "reconcile, first visit", mode: "reconcile", st: plan,
			want: ChangePageView{Kind: changeKindForm, Title: "Reconcile the plan for app", Label: "Document to reconcile with", Hint: "A path inside the project, such as docs/design-system.md."}},
		{name: "amend, blank text", mode: "amend", text: "   ", given: true, st: plan,
			want: ChangePageView{Kind: changeKindForm, Title: "Amend the plan for app", Label: "What should change?", Textarea: true, FieldError: "Say what to change."}},
		{name: "reconcile, blank text", mode: "reconcile", text: "", given: true, st: plan,
			want: ChangePageView{Kind: changeKindForm, Title: "Reconcile the plan for app", Label: "Document to reconcile with", Hint: "A path inside the project, such as docs/design-system.md.", FieldError: "Name a document."}},
		{name: "amend with text", mode: "amend", text: "  Add a CSV export.  ", given: true, st: plan,
			want: ChangePageView{Kind: changeKindConfirm, Title: "Amend the plan for app?", Lede: "A planning agent reads what you wrote, makes the change with you and opens its chat next.", Label: "What should change?", Textarea: true, Text: "Add a CSV export."}},
		{name: "reconcile with text", mode: "reconcile", text: "docs/design-system.md", given: true, st: plan,
			want: ChangePageView{Kind: changeKindConfirm, Title: "Reconcile the plan for app with docs/design-system.md?", Lede: "A planning agent reads that document and asks you what to change in the plan. Its chat opens next.", Label: "Document to reconcile with", Hint: "A path inside the project, such as docs/design-system.md.", Text: "docs/design-system.md"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := changePageFor("app", tc.mode, tc.text, tc.given, tc.st)
			want := tc.want
			want.Project, want.Mode = "app", tc.mode
			want.Action, want.Chat, want.Back = "/p/app/plan/change", "/p/app/plan/chat", "/p/app"
			if got != want {
				t.Errorf("changePageFor =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// TestPlanChangePageGET is load-bearing for three things: a GET never reaches
// the seam (a link someone was sent must not start a planning agent), a blank
// text draws the field error with no POST form, and the owner's text is
// escaped in both the card and the hidden field.
func TestPlanChangePageGET(t *testing.T) {
	tests := []struct {
		name, path string
		setup      func(*rig)
		status     int
		want       []string
		notWant    []string
	}{
		{name: "extend card", path: "/p/app/plan/change?mode=extend", status: 200,
			want: []string{"Extend the plan for app?", "A planning agent reads the plan and asks you what to add. Its chat opens next.",
				`<form method="post" action="/p/app/plan/change"`, `name="mode" value="extend"`, `name="confirm" value="yes"`,
				`>Start planning</button>`, `<a class="btn btn--quiet" href="/p/app" autofocus>Cancel</a>`}},
		{name: "amend form", path: "/p/app/plan/change?mode=amend", status: 200,
			want:    []string{`<form method="get" action="/p/app/plan/change"`, `name="mode" value="amend"`, "What should change?", `<textarea class="textarea" id="change-text" name="text"`, ">Continue</button>"},
			notWant: []string{`method="post"`, "field__error", `name="confirm"`}},
		{name: "reconcile form", path: "/p/app/plan/change?mode=reconcile", status: 200,
			want:    []string{"Document to reconcile with", `class="input input--mono"`, "A path inside the project, such as docs/design-system.md.", ">Continue</button>"},
			notWant: []string{`method="post"`, "<textarea"}},
		{name: "blank amend text", path: "/p/app/plan/change?mode=amend&text=+++", status: 200,
			want:    []string{`<p class="field__error" id="change-error" role="alert">Say what to change.</p>`},
			notWant: []string{`method="post"`, `name="confirm"`}},
		{name: "blank reconcile text", path: "/p/app/plan/change?mode=reconcile&text=", status: 200,
			want:    []string{"Name a document."},
			notWant: []string{`method="post"`}},
		{name: "amend card escapes", path: "/p/app/plan/change?mode=amend&text=" + url.QueryEscape(`<script>alert(1)</script> "q"`), status: 200,
			want:    []string{"Amend the plan for app?", `<p class="session-quote">&lt;script&gt;alert(1)&lt;/script&gt; &#34;q&#34;</p>`, `name="text" value="&lt;script&gt;alert(1)&lt;/script&gt; &#34;q&#34;"`, `name="confirm" value="yes"`},
			notWant: []string{"<script>alert"}},
		{name: "reconcile card", path: "/p/app/plan/change?mode=reconcile&text=docs/design-system.md", status: 200,
			want: []string{"Reconcile the plan for app with docs/design-system.md?", `name="text" value="docs/design-system.md"`}},
		{name: "unknown mode", path: "/p/app/plan/change?mode=new", status: 400,
			want:    []string{"Nothing started: new is not a way to change a plan.", `href="/p/app" aria-label="Back to app"`},
			notWant: []string{`method="post"`, ">Back to app</a>"}},
		{name: "no plan", path: "/p/app/plan/change?mode=extend", status: 200, setup: func(rg *rig) { delete(rg.plans, rg.root) },
			want:    []string{"There is no plan to change yet."},
			notWant: []string{"<form", "Start planning"}},
		{name: "session open", path: "/p/app/plan/change?mode=extend", status: 200, setup: func(rg *rig) {
			must(rg.t, plansession.WriteState(rg.root, plansession.State{Mode: "amend", Phase: "draft", UpdatedAt: "2026-10-12T11:00:00Z"}))
		}, want: []string{"A planning session is already open (amend, draft).", `<a class="btn btn--primary" href="/p/app/plan/chat">Open its chat</a>`},
			notWant: []string{"<form", "Start planning"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.pageDo(http.MethodGet, tc.path, "")
			body := rec.Body.String()
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, body)
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(noSignOut(body), w) {
					t.Errorf("must not contain %q", w)
				}
			}
			if len(rg.startCalls) != 0 {
				t.Errorf("a GET called the seam: %v", rg.startCalls)
			}
		})
	}
}

// TestPlanChangeRefusalRedraw is load-bearing: a refused POST answers 400
// with the change page again, the refusal's sentence above it, the typed text
// still in place, and, when the refusal is that a session is open, the link
// to its chat and no form to try again.
func TestPlanChangeRefusalRedraw(t *testing.T) {
	tests := []struct {
		name    string
		res     PlanStartResult
		form    string
		want    []string
		notWant []string
	}{
		{name: "seam refusal keeps the text", res: PlanStartResult{Outcome: PlanStartRefused, Reason: "The plan changed under you."}, form: changeForm("amend", "Add a CSV export."),
			want:    []string{`<div class="notice__body">The plan changed under you.</div>`, `name="text" value="Add a CSV export."`, "Add a CSV export.", "Start planning"},
			notWant: []string{"Open its chat"}},
		{name: "session open links to the chat", res: PlanStartResult{Outcome: PlanStartRefused, Reason: "A planning session is already open (reconcile, round).", Open: true, OpenMode: "reconcile", OpenPhase: "round"}, form: changeForm("extend", ""),
			want:    []string{"A planning session is already open (reconcile, round).", `href="/p/app/plan/chat">Open its chat</a>`},
			notWant: []string{"Start planning", "<form"}},
		{name: "blank amend shows the field error", form: changeForm("amend", ""),
			want:    []string{`<div class="notice__body">Nothing started: say what to change in the plan.</div>`, `<p class="field__error" id="change-error" role="alert">Say what to change.</p>`},
			notWant: []string{`method="post"`}},
		{name: "unknown mode says it once", form: changeForm("delete", "x"),
			want: []string{"delete is not a way to change a plan"}, notWant: []string{"Start planning"}},
		{name: "missing confirm", form: "mode=amend&text=" + url.QueryEscape("Keep me"),
			want: []string{"changing the plan needs the confirm"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.res.Outcome != "" {
				rg.startResult = tc.res
			}
			rec := rg.pageDo(http.MethodPost, "/p/app/plan/change", tc.form)
			body := rec.Body.String()
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, body)
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(noSignOut(body), w) {
					t.Errorf("must not contain %q", w)
				}
			}
			if tc.name == "unknown mode says it once" && strings.Count(body, "is not a way to change a plan") != 1 {
				t.Errorf("the sentence appears %d times, want once", strings.Count(body, "is not a way to change a plan"))
			}
		})
	}
}
