package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/planlint"
)

const idScript = "kmn345"

// ownScript is the one script tag every page carries: the hashed live.js.
var ownScript = regexp.MustCompile(`<script src="/static/[0-9a-f]{12}/live\.js" defer></script>`)

// noOwnScript removes that tag, so the tests that look for injected markup
// still see any other script tag a hostile text managed to open.
func noOwnScript(body string) string { return ownScript.ReplaceAllString(body, "") }

// pageDo sends one request with the cookie, the way a browser that has
// already visited with ?token= does.
func (rg *rig) pageDo(method, path, body string) *httptest.ResponseRecorder {
	rg.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie(testToken)})
	if body != "" {
		req.Header.Set("Content-Type", formType)
	}
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, req)
	return rec
}

// seedScript adds an open item whose title and body hold markup.
func (rg *rig) seedScript() {
	rg.t.Helper()
	st := inbox.Store{Dir: inbox.Dir(rg.root), Now: func() time.Time { return testNow }, NewID: func() string { return idScript }}
	if _, err := st.Add(inbox.NewItem{Kind: "question", Blocking: true, Title: "<script>alert(1)</script>", Body: "<script>alert(2)</script> & more"}); err != nil {
		rg.t.Fatalf("seeding: %v", err)
	}
}

// TestPages requests every page with the cookie and checks it is HTML that
// holds data from the fakes.
//
// Load-bearing rows: "usage with no reading" (a sentence, not an empty
// table, so no reading is not read as zero percent), "project with a space
// in its name" (links must be escaped and still resolve), and "answered
// item" (shows the answer and where it came from, with no form).
func TestPages(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*rig)
		path    string
		want    []string
		notWant []string
	}{
		{name: "overview", path: "/", want: []string{"T1.2", "executing", "1 of 3 tasks done", "T1.1 shipped", "Nothing is blocked on you", "42%", "Details"}},
		{name: "overview with a blocking item", setup: (*rig).seedInbox, path: "/", want: []string{"1 open question", "blocking work"}},
		{name: "overview shows paused", setup: func(rg *rig) { _ = daemonstate.Pause(rg.root, testNow) }, path: "/", want: []string{"Paused"}},
		{name: "usage", path: "/usage", want: []string{"42%", "12%", "all", "Extra usage: on", "plenty left", "Resets 12 Oct 2026 14:00 UTC"}},
		{name: "usage with no reading", setup: func(rg *rig) { rg.usageOK = false }, path: "/usage", want: []string{"No usage reading exists yet"}, notWant: []string{"% used"}},
		{name: "project", setup: (*rig).seedInbox, path: "/p/app", want: []string{"Which database?", "Tabs or spaces?", "open, blocking", "Pause", `action="/p/app/pause"`, "1 of 3 tasks done"}},
		{name: "project paused shows Resume", setup: func(rg *rig) { _ = daemonstate.Pause(rg.root, testNow) }, path: "/p/app", want: []string{"Resume", `action="/p/app/resume"`}, notWant: []string{">Pause<"}},
		{name: "project with a space in its name", path: "/p/my%20app", want: []string{"my app", `action="/p/my%20app/pause"`}},
		{name: "item with a blank answer form", setup: (*rig).seedInbox, path: "/p/app/inbox/" + idBlocking, want: []string{"Which database?", "Pick one.", "<textarea", `action="/p/app/inbox/` + idBlocking + `/answer"`}, notWant: []string{"type=\"radio\""}},
		{name: "item with options", setup: (*rig).seedInbox, path: "/p/app/inbox/" + idOptions, want: []string{`value="tabs" checked`, "(recommended)", `value="spaces">`, `name="answer"`}},
		{name: "answered item", setup: (*rig).seedInbox, path: "/p/app/inbox/" + idAnswered, want: []string{"seen", "through cli"}, notWant: []string{"<form", "<textarea"}},
		{name: "inbox list goes to the project page", setup: (*rig).seedInbox, path: "/p/app/inbox"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.pageDo("GET", tc.path, "")
			if tc.name == "inbox list goes to the project page" {
				if rec.Code != 303 || rec.Header().Get("Location") != "/p/app#inbox" {
					t.Fatalf("status %d, Location %q; want 303 to /p/app#inbox", rec.Code, rec.Header().Get("Location"))
				}
				return
			}
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("page does not contain %q\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("page contains %q, which it should not", w)
				}
			}
			if strings.Contains(body, testToken) {
				t.Errorf("the page contains the token")
			}
			if !strings.Contains(body, `name="viewport"`) || strings.Contains(strings.ToLower(noOwnScript(body)), "<script") {
				t.Errorf("page must have the viewport tag and no script tag")
			}
		})
	}
}

// TestPageEscapesMarkup is load-bearing: an inbox item's text comes from an
// agent or a stranger's issue, and it must be shown, not run.
func TestPageEscapesMarkup(t *testing.T) {
	rg := newRig(t, Config{})
	rg.seedScript()
	for _, path := range []string{"/p/app/inbox/" + idScript, "/p/app"} {
		body := rg.pageDo("GET", path, "").Body.String()
		if strings.Contains(strings.ToLower(noOwnScript(body)), "<script") {
			t.Errorf("%s: markup was not escaped:\n%s", path, body)
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("%s: the escaped title is missing:\n%s", path, body)
		}
	}
}

// TestPagePosts covers the form buttons. Load-bearing rows: the answer
// redirect (a reload must not send the form again), "radio only" (the
// preselected option is the answer when the text box is blank), "text beats
// radio", and the two failures, which must render the error page with the
// real status and not redirect.
func TestPagePosts(t *testing.T) {
	answer := func(id string) string { return "/p/app/inbox/" + id + "/answer" }
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantLoc    string
		wantAnswer string
		wantText   string
	}{
		{name: "answer redirects to the item", path: answer(idBlocking), body: "answer=Use+Postgres", wantStatus: 303, wantLoc: "/p/app/inbox/" + idBlocking, wantAnswer: "Use Postgres"},
		{name: "radio only", path: answer(idOptions), body: "choice=tabs&answer=", wantStatus: 303, wantLoc: "/p/app/inbox/" + idOptions, wantAnswer: "tabs"},
		{name: "text beats radio", path: answer(idOptions), body: "choice=tabs&answer=neither", wantStatus: 303, wantLoc: "/p/app/inbox/" + idOptions, wantAnswer: "neither"},
		{name: "empty answer shows the error page", path: answer(idBlocking), body: "answer=", wantStatus: 400, wantText: "The answer was empty"},
		{name: "second answer shows the first", path: answer(idAnswered), body: "answer=again", wantStatus: 409, wantText: "first answer is the one that is kept"},
		{name: "unknown item", path: answer("zzz999"), body: "answer=x", wantStatus: 404, wantText: "no inbox item"},
		{name: "pause redirects to the project", path: "/p/app/pause", wantStatus: 303, wantLoc: "/p/app"},
		{name: "resume redirects to the project", path: "/p/app/resume", wantStatus: 303, wantLoc: "/p/app"},
		{name: "project with a space redirects escaped", path: "/p/my%20app/pause", wantStatus: 303, wantLoc: "/p/my%20app"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			rec := rg.pageDo("POST", tc.path, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if loc := rec.Header().Get("Location"); loc != tc.wantLoc {
				t.Errorf("Location = %q, want %q", loc, tc.wantLoc)
			}
			if tc.wantStatus != 303 {
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
					t.Errorf("Content-Type = %q, want the error page as HTML", ct)
				}
				if !strings.Contains(rec.Body.String(), tc.wantText) {
					t.Errorf("error page lacks %q\n%s", tc.wantText, rec.Body.String())
				}
			}
			if tc.wantAnswer != "" {
				id := tc.path[strings.Index(tc.path, "inbox/")+len("inbox/"):]
				id = strings.TrimSuffix(id, "/answer")
				it, err := rg.store().Get(id)
				if err != nil {
					t.Fatal(err)
				}
				if it.Answer != tc.wantAnswer || it.AnsweredVia != inbox.ViaWeb {
					t.Errorf("stored answer %q via %q, want %q via web", it.Answer, it.AnsweredVia, tc.wantAnswer)
				}
			}
		})
	}
}

func (rg *rig) store() inbox.Store {
	return inbox.Store{Dir: inbox.Dir(rg.root), Now: func() time.Time { return testNow }}
}

// TestPagePauseButtonsAct checks the page buttons reach the injected
// functions exactly once, and that a failing one renders the error page.
func TestPagePauseButtonsAct(t *testing.T) {
	rg := newRig(t, Config{})
	if rec := rg.pageDo("POST", "/p/app/pause", ""); rec.Code != 303 {
		t.Fatalf("pause: %d", rec.Code)
	}
	if len(rg.paused) != 1 || !daemonstate.Paused(rg.root) {
		t.Errorf("pause called %d times, paused = %v", len(rg.paused), daemonstate.Paused(rg.root))
	}
	if rec := rg.pageDo("POST", "/p/app/resume", ""); rec.Code != 303 {
		t.Fatalf("resume: %d", rec.Code)
	}
	if len(rg.resumed) != 1 || daemonstate.Paused(rg.root) {
		t.Errorf("resume called %d times, paused = %v", len(rg.resumed), daemonstate.Paused(rg.root))
	}
	rg.pauseErr = http.ErrAbortHandler
	rec := rg.pageDo("POST", "/p/app/pause", "")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "could not be paused") {
		t.Errorf("failing pause: %d\n%s", rec.Code, rec.Body.String())
	}
}

// TestPageErrors: unknown project and unknown item render the error page
// with the real status. Load-bearing: the 404 rows must be HTML for a page
// path and still say nothing a request sent.
func TestPageErrors(t *testing.T) {
	tests := []struct {
		name, path string
		want       int
	}{
		{"unknown project", "/p/nope", 404},
		{"unknown item", "/p/app/inbox/zzz999", 404},
		{"malformed item id", "/p/app/inbox/..%2f..", 404},
		{"unknown address", "/nothing-here", 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := rg.pageDo("GET", tc.path, "")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q", ct)
			}
			if !strings.Contains(rec.Body.String(), "Something went wrong (404)") {
				t.Errorf("not the error page:\n%s", rec.Body.String())
			}
		})
	}
}

// TestPageTokenVisit: a ?token= visit on a page sets the cookie and goes to
// the same address without the token; another query value survives. A page
// with no token gets the self-contained 401 page.
func TestPageTokenVisit(t *testing.T) {
	rg := newRig(t, Config{})
	req := httptest.NewRequest("GET", "/p/app?token="+url.QueryEscape(testToken)+"&x=1", nil)
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, req)
	if rec.Code != 303 {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/p/app?x=1" {
		t.Errorf("Location = %q, want /p/app?x=1", loc)
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Set-Cookie"), ";"), cookieName+"="+goodCookie(testToken)) {
		t.Errorf("cookie not set: %v", rec.Header().Values("Set-Cookie"))
	}

	rec = httptest.NewRecorder()
	rg.h.ServeHTTP(rec, httptest.NewRequest("GET", "/p/app", nil))
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "?token=") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("no-token page: %d %q\n%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

// scriptTag, handlerAttr and jsURL find what an inline script would be made
// of. The handler pattern needs a space or quote before "on" so words such as
// "button" or "action" do not match.
var (
	scriptTag   = regexp.MustCompile(`(?i)<script\b[^>]*>`)
	handlerAttr = regexp.MustCompile(`(?i)[\s"'/]on[a-z]+\s*=`)
	jsURL       = regexp.MustCompile(`(?i)javascript:`)
)

// TestPageTemplatesHaveNoInlineScript keeps the policy (script-src 'self') and
// the source in step: every script tag in a template loads a file through
// the asset function, and nothing carries an inline handler or a javascript:
// address, which the policy would refuse and which would leave a page
// broken only in a browser.
//
// Load-bearing rows: a layout script tag with src="{{asset (the one allowed
// shape), and the scan over every template file so a page added later is
// covered without a new row.
func TestPageTemplatesHaveNoInlineScript(t *testing.T) {
	entries, err := pageFiles.ReadDir("templates")
	if err != nil || len(entries) == 0 {
		t.Fatalf("templates: %v", err)
	}
	for _, e := range entries {
		b, _ := pageFiles.ReadFile("templates/" + e.Name())
		text := string(b)
		t.Run(e.Name(), func(t *testing.T) {
			for _, tag := range scriptTag.FindAllString(text, -1) {
				if !strings.Contains(tag, `src="{{asset `) {
					t.Errorf("script tag without src=\"{{asset: %s", tag)
				}
			}
			if m := handlerAttr.FindString(text); m != "" {
				t.Errorf("inline event handler %q", m)
			}
			if jsURL.MatchString(text) {
				t.Error("holds a javascript: address")
			}
		})
	}
	layout, _ := pageFiles.ReadFile("templates/layout.html")
	if !strings.Contains(string(layout), `<script src="{{asset "live.js"}}" defer></script>`) {
		t.Error("layout.html does not load live.js")
	}
}

// TestPageLiveMarkup checks that each page loads the hashed script and that
// the live regions exist with the parts and project the script matches on.
//
// Load-bearing rows: "project", where the inbox list and the summary carry
// the project's own name (a region with the wrong project would never be
// swapped), and "usage", whose project is empty (machine-wide). The page with
// the space in its name proves the attribute is escaped, not path-escaped.
func TestPageLiveMarkup(t *testing.T) {
	scriptRe := regexp.MustCompile(`<script src="/static/[0-9a-f]{12}/live\.js" defer></script>`)
	tests := []struct {
		name string
		path string
		want []string
	}{
		{name: "overview", path: "/", want: []string{
			`id="project-app" data-live="inbox ledger pause sessions planning" data-project="app"`,
			`id="project-my%20app" data-live="inbox ledger pause sessions planning" data-project="my app"`,
			`id="usage-reading" data-live="usage" data-project=""`,
		}},
		{name: "project", path: "/p/app", want: []string{
			`id="summary" data-live="ledger pause sessions planning" data-project="app"`,
			`id="inbox-list" data-live="inbox" data-project="app" aria-live="polite"`,
			`data-live-status hidden`,
		}},
		{name: "usage", path: "/usage", want: []string{`id="usage-reading" data-live="usage" data-project=""`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := rg.pageDo("GET", tt.path, "")
			body := rec.Body.String()
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			if !scriptRe.MatchString(body) {
				t.Errorf("no hashed live.js script tag:\n%s", body)
			}
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q", w)
				}
			}
		})
	}
}

// hostilePlan holds markup in every place the plan page shows plan text: the
// rendered plan, an open question (drawn by the template, not the renderer)
// and a checker finding's message (which quotes the heading).
const hostilePlan = `# Plan <script>alert(1)</script>

<a href="javascript:alert(2)" onclick="alert(3)">tap</a>

#### T1.1 — First <img src=x onerror=alert(4)>
- **Depends on:** T9.9

#### T1.1 — Again

## Open questions

| Question | Needed by |
|---|---|
| <script>alert(5)</script> or <b>this</b>? | T1.1 |
`

// TestPlanPage requests the plan view as a page.
//
// Load-bearing rows: "review-only project" (the waiting line is there, word
// for word, with the command to run), "configured project is not waiting"
// (the line must go once the plan is approved, or the owner approves twice),
// "plan that cannot be read" (the problem is shown and no plan or count
// beside it), "plan with errors" (the checker's errors are on the page, since
// approve will refuse them), and "hostile plan" (plan text reaches the page
// three ways, and none may be markup).
func TestPlanPage(t *testing.T) {
	const waiting = "Waiting for approval. To approve it, run afk plan approve in the project folder."
	tests := []struct {
		name    string
		setup   func(*rig)
		path    string
		status  int
		want    []string
		notWant []string
	}{
		{name: "configured project", path: "/p/app/plan", status: 200,
			want: []string{"Plan: app", "docs/PLAN.md", "1 milestone,", "2 tasks", "(1 for you to do yourself)", "1 open question.", "0 errors and 0 warnings",
				"Which colour?", "Needed by: T1.2", `<h4 id="T1.1">T1.1 — First</h4>`, "<strong>Goal:</strong> g", "<th>Topic</th>", `href="/p/app"`},
			notWant: []string{"Waiting for approval", "Which font?</li>", "Errors to fix"}},
		{name: "configured project is not waiting", path: "/p/app/plan", status: 200,
			setup:   func(rg *rig) { rg.reviews = append(rg.reviews, Project{Name: "app-2", Root: rg.root}) },
			notWant: []string{"Waiting for approval"}},
		{name: "review-only project", path: "/p/draft/plan", status: 200,
			want:    []string{"Plan: draft", waiting, `<h1 id="draft-plan">Draft plan</h1>`, "A plan nobody has approved yet.", "Errors to fix", `<a href="/">Back to the overview</a>`},
			notWant: []string{`href="/p/draft"`}},
		{name: "project name with a space", path: "/p/my%20app/plan", status: 200,
			want: []string{"Plan: my app", `href="/p/my%20app"`, "Problem: The plan could not be read"}},
		{name: "plan that cannot be read", path: "/p/app/plan", status: 200,
			setup:   func(rg *rig) { rg.planErr = errors.New("permission denied") },
			want:    []string{"Problem: The plan could not be read: permission denied."},
			notWant: []string{"milestone", "Open questions", `class="plan"`}},
		{name: "plan with errors", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) {
				rg.plans[rg.root] = strings.Replace(testPlan, "- **Depends on:** T1.1\n", "- **Depends on:** T7.7\n", 1)
			},
			want: []string{"Errors to fix before this plan can be built", "T7.7", "1 error and", `class="warn"`}},
		{name: "plan with no open question", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.plans[rg.root] = strings.Replace(testPlan, "| Which colour? | T1.2 |\n", "", 1) },
			want:  []string{"No question in the plan is open.", "0 open questions."}},
		{name: "hostile plan", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.plans[rg.root] = hostilePlan },
			want: []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "&lt;script&gt;alert(5)&lt;/script&gt; or &lt;b&gt;this&lt;/b&gt;?",
				"&lt;img src=x onerror=alert(4)&gt;", "onclick=&#34;alert(3)&#34;"},
			notWant: []string{"<script", "<img", "<b>", `onclick="`, `href="javascript:`}},
		{name: "unknown project", path: "/p/nothing-here/plan", status: 404,
			want: []string{"Something went wrong (404)", "waiting for approval"}},
		{name: "overview lists the plans waiting", path: "/", status: 200,
			want: []string{"Plans waiting for approval", `<a href="/p/draft/plan">draft</a>`}},
		{name: "overview with no plan waiting has no list", path: "/", status: 200,
			setup:   func(rg *rig) { rg.reviews = nil },
			notWant: []string{"Plans waiting for approval"}},
		{name: "overview leaves out a review that is a configured project", path: "/", status: 200,
			setup:   func(rg *rig) { rg.reviews = []Project{{Name: "app-2", Root: rg.root}} },
			notWant: []string{"Plans waiting for approval", "app-2"}},
		{name: "overview leaves out a review with a configured project's name", path: "/", status: 200,
			setup:   func(rg *rig) { rg.reviews = []Project{{Name: "app", Root: rg.draft}} },
			notWant: []string{"Plans waiting for approval"}},
		{name: "overview escapes a review name in its link", path: "/", status: 200,
			setup: func(rg *rig) { rg.reviews = []Project{{Name: `a b/<i>"c`, Root: rg.draft}} },
			want:  []string{`href="/p/a%20b%2F%3Ci%3E%22c/plan"`, "a b/&lt;i&gt;&#34;c"}, notWant: []string{"<i>"}},
		{name: "project page links to its plan", path: "/p/app", status: 200,
			want: []string{`<a href="/p/app/plan">Read the plan</a>`}},
		{name: "project page link is escaped", path: "/p/my%20app", status: 200,
			want: []string{`<a href="/p/my%20app/plan">Read the plan</a>`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.pageDo("GET", tc.path, "")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("page does not contain %q\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(strings.ToLower(noOwnScript(body)), strings.ToLower(w)) {
					t.Errorf("page contains %q, which it should not\n%s", w, body)
				}
			}
			if strings.Contains(body, testToken) {
				t.Errorf("the page contains the token")
			}
			if !strings.Contains(body, `name="viewport"`) || strings.Contains(strings.ToLower(noOwnScript(body)), "<script") {
				t.Errorf("page must have the viewport tag and no script tag")
			}
		})
	}
}

// TestPlanPageCannotApprove is load-bearing: approving a plan from the web
// page is forbidden. Approve commits and pushes in the owner's project and
// starts a build that spends their usage, and that takes someone at the
// project's own terminal, not whoever holds a phone with the cookie. So the
// plan page, for a plan that is waiting and for one that is not, has nothing
// that sends anything: no form, no button, no input. The template is checked
// as well as the drawn page, so a form inside a branch no fixture reaches is
// caught too, and the only links on the drawn page are plain GETs to pages
// that exist.
func TestPlanPageCannotApprove(t *testing.T) {
	src, err := pageFiles.ReadFile("templates/plan.html")
	must(t, err)
	acting := []string{"<form", "<button", "<input", "<textarea", "<select", "method=", "action=", "formaction"}
	for _, w := range acting {
		if strings.Contains(strings.ToLower(string(src)), w) {
			t.Errorf("templates/plan.html holds %q", w)
		}
	}

	linkRE := regexp.MustCompile(`<a href="([^"]*)"`)
	for _, path := range []string{"/p/draft/plan", "/p/app/plan"} {
		rg := newRig(t, Config{})
		// A plan that mentions approving and holds a link, to make sure the
		// only way either reaches the page is as text and as a plain link.
		rg.plans[rg.draft] = draftPlan + "\nRun afk plan approve. See https://example.com/docs.\n"
		body := rg.pageDo("GET", path, "").Body.String()
		for _, w := range acting {
			if strings.Contains(strings.ToLower(body), w) {
				t.Errorf("%s: the page holds %q", path, w)
			}
		}
		for _, m := range linkRE.FindAllStringSubmatch(body, -1) {
			href := m[1]
			if strings.HasPrefix(href, "https://example.com/") {
				continue
			}
			if !strings.HasPrefix(href, "/") || strings.Contains(href, "approve") {
				t.Errorf("%s: unexpected link %q", path, href)
				continue
			}
			if rec := rg.pageDo("GET", href, ""); rec.Code != 200 {
				t.Errorf("%s: the link %q answers %d", path, href, rec.Code)
			}
		}
	}

	// No route about a plan takes anything but GET.
	for _, rt := range routes {
		if strings.Contains(rt.path, "plan") && rt.method != http.MethodGet {
			t.Errorf("the route %s %s can change something about a plan", rt.method, rt.path)
		}
	}
}

// TestPlanPageRendersThisRepositorysPlan is the task's acceptance against
// the real file: the plan view of this repository's docs/PLAN.md, drawn by
// the real handler, with the summary the checker gives for it and its open
// questions. The counts are compared with planlint's own, never with numbers
// typed here, because the plan grows with every task.
func TestPlanPageRendersThisRepositorysPlan(t *testing.T) {
	data, err := os.ReadFile("../../docs/PLAN.md")
	must(t, err)
	rg := newRig(t, Config{})
	rg.plans[rg.root] = string(data)

	want := planlint.Lint(data).Summary
	m := decode(t, rg.do(http.MethodGet, "/api/p/app/plan", "", ""))
	sum := m["summary"].(map[string]any)
	if sum["tasks"] != float64(want.Tasks) || sum["milestones"] != float64(want.Milestones) || sum["owner_tasks"] != float64(want.OwnerTasks) ||
		sum["open_questions"] != float64(want.OpenQuestions) || sum["errors"] != float64(want.Errors) || sum["warnings"] != float64(want.Warnings) {
		t.Errorf("summary = %s, want %+v", dump(sum), want)
	}
	if want.Tasks == 0 || want.OpenQuestions == 0 {
		t.Fatalf("this repository's plan reads as having no tasks or no open questions (%+v); the test proves nothing", want)
	}
	if got := len(m["questions"].([]any)); got != want.OpenQuestions {
		t.Errorf("questions listed = %d, want the summary's %d", got, want.OpenQuestions)
	}

	rec := rg.pageDo("GET", "/p/app/plan", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, w := range []string{
		`<h4 id="T5.7">T5.7 — Approve and go</h4>`,
		"<table",
		fmt.Sprintf("%d tasks", want.Tasks),
		fmt.Sprintf("%d open questions.", want.OpenQuestions),
		"Licence for the open-source release.",
		"Needed by: T2.4",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("the page does not contain %q", w)
		}
	}
	if strings.Contains(strings.ToLower(noOwnScript(body)), "<script") || strings.Contains(body, "<form") {
		t.Errorf("the page of the real plan holds a script tag or a form")
	}
}
