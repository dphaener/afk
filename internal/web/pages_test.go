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
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/usagestate"
)

const idScript = "kmn345"

// ownScript is the script tags every page carries: the hashed live.js and confirm.js.
var ownScript = regexp.MustCompile(`<script src="/static/[0-9a-f]{12}/(live|confirm)\.js" defer></script>`)

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
		{name: "overview", path: "/", want: []string{`<span class="tid">T1.2</span> executing`, `aria-label="1 of 3 tasks done"`, "Building", "42%", "Details"}},
		{name: "overview with a blocking item", setup: func(rg *rig) { rg.rows[0].Open, rg.rows[0].Blocking = 2, 1 }, path: "/", want: []string{"2 in inbox, 1 holds a task up"}},
		{name: "overview shows paused", setup: func(rg *rig) {
			rg.rows = []projectrows.Row{projectrows.Build(projectrows.Input{Name: "app", Listed: true, HasPlan: true, Paused: true})}
		}, path: "/", want: []string{"badge--paused\">Paused<"}},
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
		{name: "pause redirects to the project", path: "/p/app/pause", wantStatus: 303, wantLoc: "/p/app?done=paused"},
		{name: "resume redirects to the project", path: "/p/app/resume", wantStatus: 303, wantLoc: "/p/app?done=resumed"},
		{name: "project with a space redirects escaped", path: "/p/my%20app/pause", wantStatus: 303, wantLoc: "/p/my%20app?done=paused"},
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
// with the real status, in the design's Error screen: the heading, the
// status and path in mono, Go to all projects and Try again. Load-bearing:
// the 404 rows must be HTML for a page path, and "unknown address with
// markup" must show its path escaped, never as markup.
func TestPageErrors(t *testing.T) {
	tests := []struct {
		name, path string
		want       int
		mono       string
		retry      string
	}{
		{"unknown project", "/p/nope", 404, "404 /p/nope", `href="/p/nope"`},
		{"unknown item", "/p/app/inbox/zzz999", 404, "404 /p/app/inbox/zzz999", `href="/p/app/inbox/zzz999"`},
		{"malformed item id", "/p/app/inbox/..%2f..", 404, "404 /p/app/inbox/", `>Try again</a>`},
		{"unknown address", "/nothing-here", 404, "404 /nothing-here", `href="/nothing-here"`},
		{"unknown address with markup", "/%3Cb%3Ex", 404, "404 /&lt;b&gt;x", `href="/%3Cb%3Ex"`},
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
			body := rec.Body.String()
			for _, w := range []string{"<h1>That didn't load</h1>", `<p class="mono meta">` + tc.mono, `<a class="btn btn--primary" href="/">Go to all projects</a>`, tc.retry} {
				if !strings.Contains(body, w) {
					t.Errorf("the error page lacks %q:\n%s", w, body)
				}
			}
			if strings.Contains(body, "<b>") {
				t.Errorf("the path was drawn as markup")
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
	styleEl     = regexp.MustCompile(`(?i)<style\b`)
	styleAttr   = regexp.MustCompile(`(?i)[\s"'/]style\s*=`)
)

// TestPageTemplatesHaveNoInlineScript keeps the policy (script-src 'self') and
// the source in step: every script tag in a template loads a file through
// the asset function, and nothing carries an inline handler or a javascript:
// address, which the policy would refuse and which would leave a page
// broken only in a browser. It also holds the templates to no inline style
// (no <style> element, no style= attribute), so every look comes from the
// two stylesheets and the policy's 'unsafe-inline' can one day be dropped.
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
			// Every style comes from afk.css or page.css: no style element
			// and no style attribute.
			if styleEl.MatchString(text) {
				t.Error("holds a <style> element")
			}
			if m := styleAttr.FindString(text); m != "" {
				t.Errorf("holds a style attribute %q", m)
			}
		})
	}
	layout, _ := pageFiles.ReadFile("templates/layout.html")
	for _, f := range []string{"live.js", "confirm.js"} {
		if !strings.Contains(string(layout), `<script src="{{asset "`+f+`"}}" defer></script>`) {
			t.Errorf("layout.html does not load %s", f)
		}
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
	scriptRe := regexp.MustCompile(`<script src="/static/[0-9a-f]{12}/(live|confirm)\.js" defer></script>`)
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
			`<span class="live" data-live-status hidden>Live</span>`,
			`<span class="live topnav__live" data-live-mirror hidden>Live</span>`,
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
			// live.js drives exactly one data-live-status (the phone bar's)
			// and mirrors it onto the desktop bar's.
			if n := strings.Count(body, "data-live-status"); n != 1 {
				t.Errorf("%d elements with data-live-status, want 1", n)
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
			want: []string{"That didn't load", "waiting for approval"}},
		{name: "overview lists the plans waiting", path: "/", status: 200,
			want: []string{"Plan waiting for approval", `href="/p/draft/plan"`, `<span class="row__title">draft</span>`}},
		{name: "overview with no plan waiting has no list", path: "/", status: 200,
			setup:   func(rg *rig) { rg.reviews = nil },
			notWant: []string{"Plan waiting for approval"}},
		{name: "overview leaves out a review that is a configured project", path: "/", status: 200,
			setup:   func(rg *rig) { rg.reviews = []Project{{Name: "app-2", Root: rg.root}} },
			notWant: []string{"Plan waiting for approval", "app-2"}},
		{name: "overview leaves out a review with a configured project's name", path: "/", status: 200,
			setup:   func(rg *rig) { rg.reviews = []Project{{Name: "app", Root: rg.draft}} },
			notWant: []string{"Plan waiting for approval"}},
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

// TestPageLayout requests every HTML page and checks it is drawn inside the
// new layout: class="afk" on the body, the hashed afk.css and page.css, no
// inline style, and (except the error page and the refusal) both bars.
//
// Load-bearing rows: "refused" and "error" (the two pages that stand alone,
// and the only ones a broken request reaches, so they must still wear the
// design), and "plan waiting for approval" (its back button must go to /,
// since the project page answers 404 for a plan that is not built yet). The
// row list is compared with pageNames, so a page added later fails here
// until it has a row.
func TestPageLayout(t *testing.T) {
	tests := []struct {
		name     string
		page     string
		path     string
		noToken  bool
		status   int
		bars     bool
		back     string
		current  string
		navTitle string
	}{
		{name: "overview", page: "overview", path: "/", status: 200, bars: true, current: `href="/" aria-current="page"`},
		{name: "usage", page: "usage", path: "/usage", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, current: `href="/usage" aria-current="page"`, navTitle: "Usage"},
		{name: "project", page: "project", path: "/p/app", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, current: `href="/" aria-current="true"`, navTitle: "app"},
		{name: "plan", page: "plan", path: "/p/app/plan", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, current: `href="/" aria-current="true"`, navTitle: "Plan: app"},
		{name: "plan waiting for approval", page: "plan", path: "/p/draft/plan", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, navTitle: "Plan: draft"},
		{name: "item", page: "item", path: "/p/app/inbox/" + idBlocking, status: 200, bars: true, back: `href="/p/app#inbox" aria-label="Back to the inbox of app"`, navTitle: "Which database?"},
		{name: "confirm", page: "confirm", path: "/p/app/confirm/pause", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, navTitle: "Pause project"},
		{name: "error", page: "error", path: "/p/nope", status: 404},
		{name: "refused", path: "/p/app", noToken: true, status: 401},
	}
	covered := map[string]bool{}
	for _, tc := range tests {
		covered[tc.page] = true
	}
	for _, name := range pageNames {
		if !covered[name] {
			t.Errorf("page %q has no row", name)
		}
	}
	cssLink := func(file string) *regexp.Regexp {
		return regexp.MustCompile(`<link rel="stylesheet" href="/static/[0-9a-f]{12}/` + regexp.QuoteMeta(file) + `">`)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			var rec *httptest.ResponseRecorder
			if tc.noToken {
				rec = httptest.NewRecorder()
				rg.h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			} else {
				rec = rg.pageDo("GET", tc.path, "")
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `<body class="afk">`) {
				t.Errorf("no <body class=\"afk\">")
			}
			for _, f := range []string{"afk.css", "page.css"} {
				if !cssLink(f).MatchString(body) {
					t.Errorf("does not link the hashed %s", f)
				}
			}
			if styleEl.MatchString(body) || styleAttr.MatchString(body) {
				t.Errorf("holds an inline style")
			}
			hasBars := strings.Contains(body, `class="app-bar`) && strings.Contains(body, `class="topnav"`)
			if hasBars != tc.bars {
				t.Errorf("bars drawn = %v, want %v", hasBars, tc.bars)
			}
			if !tc.bars {
				if strings.Contains(body, "app-bar") || strings.Contains(body, "topnav") || strings.Contains(body, "data-live-status") {
					t.Errorf("a stand-alone page holds a bar or the live indicator")
				}
				return
			}
			if !strings.Contains(body, `Projects <span class="count count--quiet">2</span>`) {
				t.Errorf("the top bar does not count the two projects")
			}
			for _, w := range []string{tc.back, tc.current} {
				if w != "" && !strings.Contains(body, w) {
					t.Errorf("missing %q", w)
				}
			}
			if tc.navTitle != "" && !strings.Contains(body, `<span class="app-bar__title">`+tc.navTitle+`</span>`) {
				t.Errorf("the phone bar's title is not %q", tc.navTitle)
			}
			if tc.page == "overview" {
				if !strings.Contains(body, `<span class="app-bar__title wordmark">afk</span>`) || strings.Contains(body, "app-bar--back") {
					t.Errorf("the overview's phone bar should hold the wordmark and no back button")
				}
			}
			// One current item in the top bar. The overview's selected row
			// carries its own aria-current (the desktop list beside its
			// detail), so the count is taken over the bar only.
			topnav := body[strings.Index(body, `<header class="topnav">`):]
			topnav = topnav[:strings.Index(topnav, "</header>")]
			if n := strings.Count(topnav, `aria-current=`); tc.current != "" && n != 1 {
				t.Errorf("%d aria-current attributes in the top bar, want 1", n)
			}
		})
	}
}

// TestChromeFor is the bars' decision for each view. Load-bearing rows: "plan
// waiting" (back to /, never to a project page that is a 404), "item" (back
// to the project's inbox, the name path-escaped), "error" (bare: no bars,
// with Try again from retryPath) and "usage" (the only page whose own
// address is a nav item besides the overview).
func TestChromeFor(t *testing.T) {
	tests := []struct {
		name   string
		page   string
		view   any
		method string
		path   string
		want   chrome
	}{
		{name: "overview", page: "overview", view: OverviewView{}, method: "GET", path: "/",
			want: chrome{Overview: true, Title: "All projects", Nav: "projects", Current: "page", Projects: 3, Back: "/", BackLabel: "Back to all projects"}},
		{name: "usage", page: "usage", view: UsageView{}, method: "GET", path: "/usage",
			want: chrome{Title: "Usage", Nav: "usage", Current: "page", Projects: 3, Back: "/", BackLabel: "Back to all projects"}},
		{name: "project", page: "project", view: ProjectView{ProjectSummary: ProjectSummary{Name: "my app"}}, method: "GET", path: "/p/my app",
			want: chrome{Title: "my app", Nav: "projects", Current: "true", Projects: 3, Back: "/", BackLabel: "Back to all projects"}},
		{name: "plan", page: "plan", view: PlanView{Project: "my app"}, method: "GET", path: "/p/my app/plan",
			want: chrome{Title: "Plan: my app", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app", BackLabel: "Back to my app"}},
		{name: "plan waiting", page: "plan", view: PlanView{Project: "draft", WaitingForApproval: true}, method: "GET", path: "/p/draft/plan",
			want: chrome{Title: "Plan: draft", Nav: "projects", Current: "true", Projects: 3, Back: "/", BackLabel: "Back to all projects"}},
		{name: "item", page: "item", view: ItemView{Project: "my app", Item: inbox.Item{Title: "Which?"}}, method: "GET", path: "/p/my app/inbox/x",
			want: chrome{Title: "Which?", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app#inbox", BackLabel: "Back to the inbox of my app"}},
		{name: "error", page: "error", view: ErrorView{Status: 404}, method: "GET", path: "/p/nope",
			want: chrome{Bare: true, Status: 404, Path: "/p/nope", Retry: "/p/nope"}},
		{name: "error after a POST", page: "error", view: ErrorView{Status: 400}, method: "POST", path: "/p/app/inbox/x/answer",
			want: chrome{Bare: true, Status: 400, Path: "/p/app/inbox/x/answer", Retry: "/p/app/inbox/x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := chromeFor(tc.page, tc.view, tc.method, tc.path, 3); got != tc.want {
				t.Errorf("chromeFor = %+v\nwant        %+v", got, tc.want)
			}
		})
	}
}

// TestRetryPath is where Try again goes. Load-bearing rows: the POST rows
// (a link cannot post, so it goes to the form's page), and "double slash" and
// "backslash" (the link must stay on this site whatever path failed).
func TestRetryPath(t *testing.T) {
	tests := []struct {
		name, method, path, want string
	}{
		{"a GET is asked again", "GET", "/p/app", "/p/app"},
		{"a space is escaped", "GET", "/p/my app", "/p/my%20app"},
		{"an answer goes to its item", "POST", "/p/app/inbox/abc/answer", "/p/app/inbox/abc"},
		{"a pause goes to the project", "POST", "/p/app/pause", "/p/app"},
		{"a POST to the top goes to the top", "POST", "/x", "/"},
		{"double slash", "GET", "//other.example/x", "/other.example/x"},
		{"backslash", "GET", `/\other.example`, "/other.example"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryPath(tc.method, tc.path); got != tc.want {
				t.Errorf("retryPath(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// TestConfirmPage covers the no-JS confirm page. Load-bearing rows: "delete"
// (only pause and resume exist, and the answer is the same 404 as an unknown
// project), the /api twin (the page has none, so it is 404 and never the
// page), and "no token" (the page is behind the guard like every other).
func TestConfirmPage(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		noToken    bool
		setup      func(rg *rig)
		wantStatus int
		wantIn     []string
		wantNot    []string
	}{
		{name: "pause", path: "/p/app/confirm/pause", wantStatus: 200,
			wantIn: []string{"Pause app?", "The running task finishes; nothing new starts. Nothing is deleted.", `action="/p/app/pause"`, `href="/p/app"`, "Keep it running"}},
		{name: "resume", path: "/p/app/confirm/resume", wantStatus: 200,
			wantIn: []string{"Resume app?", "afk starts the next ready task.", `action="/p/app/resume"`, `href="/p/app"`}},
		{name: "project with a space", path: "/p/my%20app/confirm/pause", wantStatus: 200,
			wantIn: []string{"Pause my app?", `action="/p/my%20app/pause"`}},
		{name: "delete", path: "/p/app/confirm/delete", wantStatus: 404, wantIn: []string{"That didn't load"}, wantNot: []string{"Delete"}},
		{name: "unknown project", path: "/p/nope/confirm/pause", wantStatus: 404, wantIn: []string{"That didn't load"}},
		{name: "plan waiting has no confirm", path: "/p/draft/confirm/pause", wantStatus: 404},
		{name: "api twin does not exist", path: "/api/p/app/confirm/pause", wantStatus: 404, wantNot: []string{"Pause app?"}},
		{name: "no token", path: "/p/app/confirm/pause", noToken: true, wantStatus: 401, wantNot: []string{"Pause app?", "/p/app"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			var rec *httptest.ResponseRecorder
			if tc.noToken {
				rec = httptest.NewRecorder()
				rg.h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			} else {
				rec = rg.pageDo("GET", tc.path, "")
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			body := rec.Body.String()
			for _, s := range tc.wantIn {
				if !strings.Contains(body, s) {
					t.Errorf("missing %q in\n%s", s, body)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(body, s) {
					t.Errorf("must not contain %q", s)
				}
			}
		})
	}
}

// TestProjectPageConfirm checks the project page holds the link and the
// dialog for the state it is in, and that the plan page holds neither (it
// must stay free of forms and buttons).
func TestProjectPageConfirm(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		paused  bool
		wantIn  []string
		wantNot []string
	}{
		{name: "running", path: "/p/app", wantIn: []string{`<dialog class="confirm-sheet" id="confirm-pause"`, `data-confirm="confirm-pause"`, `href="/p/app/confirm/pause"`, "Pause project&hellip;", "Pause app?", `action="/p/app/pause"`, "autofocus", `data-loading-label="Pausing"`}, wantNot: []string{"confirm-resume"}},
		{name: "paused", path: "/p/app", paused: true, wantIn: []string{`id="confirm-resume"`, `data-confirm="confirm-resume"`, `href="/p/app/confirm/resume"`, "Resume app?", `action="/p/app/resume"`}, wantNot: []string{"confirm-pause"}},
		{name: "plan page has no sheet", path: "/p/app/plan", wantNot: []string{"<dialog", "confirm-"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.paused {
				must(t, daemonstate.Pause(rg.root, testNow))
			}
			body := rg.pageDo("GET", tc.path, "").Body.String()
			for _, s := range tc.wantIn {
				if !strings.Contains(body, s) {
					t.Errorf("missing %q", s)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(body, s) {
					t.Errorf("must not contain %q", s)
				}
			}
		})
	}
}

// TestToast covers the toast after a pause or resume. Load-bearing rows: the
// script and the unknown code (nothing is shown and nothing from the query is
// echoed) and the name with markup (it comes from the project list, escaped).
func TestToast(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantIn  string
		wantNot []string
	}{
		{name: "paused", path: "/p/app?done=paused", wantIn: `role="status" data-toast>Paused app. Nothing new will start.</div>`},
		{name: "resumed", path: "/p/app?done=resumed", wantIn: `role="status" data-toast>Resumed app.</div>`},
		{name: "space in name", path: "/p/my%20app?done=resumed", wantIn: `>Resumed my app.</div>`},
		{name: "script code", path: "/p/app?done=%3Cscript%3E", wantNot: []string{"data-toast", "role=\"status\"", "&lt;script", "alert"}},
		{name: "unknown code", path: "/p/app?done=zzqcode", wantNot: []string{"data-toast", "zzqcode"}},
		{name: "no code", path: "/p/app", wantNot: []string{"data-toast"}},
		{name: "toast text as a code", path: "/p/app?done=Paused+app.", wantNot: []string{"data-toast"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			body := rg.pageDo("GET", tc.path, "").Body.String()
			if tc.wantIn != "" && !strings.Contains(body, tc.wantIn) {
				t.Errorf("missing %q in\n%s", tc.wantIn, body)
			}
			for _, s := range tc.wantNot {
				if strings.Contains(body, s) {
					t.Errorf("must not contain %q", s)
				}
			}
			if strings.Contains(strings.ToLower(noOwnScript(body)), "<script") {
				t.Errorf("a script tag besides the page's own")
			}
		})
	}
}

// TestToastFor is the pure table behind the toast: only a whitelisted code
// gives text.
func TestToastFor(t *testing.T) {
	tests := []struct{ code, project, want string }{
		{"paused", "app", "Paused app. Nothing new will start."},
		{"resumed", "app", "Resumed app."},
		{"", "app", ""},
		{"<script>", "app", ""},
		{"PAUSED", "app", ""},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			if got := toastFor(tc.code, tc.project); got != tc.want {
				t.Errorf("toastFor(%q) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

// TestConfirmScriptServed checks confirm.js is served at its hashed address
// with the long cache and the security headers, and has no inline-eval shape.
func TestConfirmScriptServed(t *testing.T) {
	rg := newRig(t, Config{})
	body := rg.pageDo("GET", "/p/app", "").Body.String()
	m := regexp.MustCompile(`/static/[0-9a-f]{12}/confirm\.js`).FindString(body)
	if m == "" {
		t.Fatal("the project page does not load confirm.js")
	}
	rec := rg.pageDo("GET", m, "")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Content-Security-Policy") != wantCSP {
		t.Errorf("CSP = %q", rec.Header().Get("Content-Security-Policy"))
	}
	if js := rec.Body.String(); strings.Contains(js, "eval(") || strings.Contains(js, "innerHTML") {
		t.Error("confirm.js uses eval or innerHTML")
	}
}

// TestStateBadge maps every projectrows state to the design's badge. Every
// row is load-bearing in the same way: the word is what a colour-blind owner
// reads, so a state drawn with the wrong word misinforms. "unknown state" is
// the fallback that keeps a newer projectrows readable.
func TestStateBadge(t *testing.T) {
	tests := []struct {
		state projectrows.State
		want  badgeView
	}{
		{projectrows.StateRunning, badgeView{"badge--running", "Building"}},
		{projectrows.StateWaiting, badgeView{"badge--needs-you", "Waiting on you"}},
		{projectrows.StatePaused, badgeView{"badge--paused", "Paused"}},
		{projectrows.StateStopped, badgeView{"badge--stopped", "Line stopped"}},
		{projectrows.StateHeld, badgeView{"badge--held", "Held for usage"}},
		{projectrows.StateNothingReady, badgeView{"badge--idle", "Nothing ready"}},
		{projectrows.StateFinished, badgeView{"badge--finished", "Finished"}},
		{projectrows.StateGone, badgeView{"badge--error", "Gone"}},
		{projectrows.StateUnreadable, badgeView{"badge--error", "Can't read"}},
		{projectrows.StateNoPlan, badgeView{"badge--idle", "No plan"}},
		{"unknown state", badgeView{"badge--plain", "unknown state"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := stateBadge(tc.state); got != tc.want {
				t.Errorf("stateBadge(%q) = %+v, want %+v", tc.state, got, tc.want)
			}
		})
	}
}

// TestInboxLine is a row's inbox counts. Load-bearing rows: "nothing open"
// (no line at all, not "0 in inbox") and "one blocking" (the singular verb).
func TestInboxLine(t *testing.T) {
	tests := []struct {
		name           string
		open, blocking int
		want           string
	}{
		{name: "nothing open", want: ""},
		{name: "open, none blocking", open: 3, want: "3 in inbox"},
		{name: "one blocking", open: 3, blocking: 1, want: "3 in inbox, 1 holds a task up"},
		{name: "many blocking", open: 3, blocking: 2, want: "3 in inbox, 2 hold a task up"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inboxLine(tc.open, tc.blocking); got != tc.want {
				t.Errorf("inboxLine(%d, %d) = %q, want %q", tc.open, tc.blocking, got, tc.want)
			}
		})
	}
}

// TestOverviewPageFor is the home page's layout decision.
//
// Load-bearing rows: the three headings (0, 1, many), "nothing at all" (the
// empty state, with no selection), "review named by a row" (one row per
// name, so the plan is not listed twice), "review-only goes after the
// needs-you rows" (it has no config order to sort by), "selection prefers
// needs you" (the desktop detail shows what needs the owner first) and
// "running" (the sub-line is the task and phase, and the row marks Working).
func TestOverviewPageFor(t *testing.T) {
	running := RowView{Name: "run", State: string(projectrows.StateRunning), Task: "T1.2", Title: "Second", Phase: "executing", Done: 1, Total: 3}
	idle := RowView{Name: "idle", State: string(projectrows.StateNothingReady), Detail: "Nothing is ready to start."}
	stopped := RowView{Name: "stop", State: string(projectrows.StateStopped), Detail: "Stopped.", NeedsOwner: true}
	waiting := RowView{Name: "wait", State: string(projectrows.StateWaiting), Detail: "Waiting.", NeedsOwner: true}
	names := func(rs []overviewRow) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Name)
		}
		return out
	}
	tests := []struct {
		name     string
		rows     []RowView
		reviews  []ReviewView
		heading  string
		needs    []string
		others   []string
		selected string
		empty    bool
	}{
		{name: "nothing at all", heading: "Nothing needs you", empty: true},
		{name: "nothing needs you", rows: []RowView{running, idle}, heading: "Nothing needs you", others: []string{"run", "idle"}, selected: "run"},
		{name: "one needs you", rows: []RowView{stopped, running}, heading: "1 project needs you", needs: []string{"stop"}, others: []string{"run"}, selected: "stop"},
		{name: "many need you", rows: []RowView{stopped, waiting, running}, heading: "2 projects need you", needs: []string{"stop", "wait"}, others: []string{"run"}, selected: "stop"},
		{name: "review-only goes after the needs-you rows", rows: []RowView{stopped, running}, reviews: []ReviewView{{Name: "draft"}},
			heading: "2 projects need you", needs: []string{"stop", "draft"}, others: []string{"run"}, selected: "stop"},
		{name: "review named by a row", rows: []RowView{running}, reviews: []ReviewView{{Name: "run"}}, heading: "Nothing needs you", others: []string{"run"}, selected: "run"},
		{name: "review alone", reviews: []ReviewView{{Name: "draft"}}, heading: "1 project needs you", needs: []string{"draft"}, selected: "draft"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := overviewPageFor(tc.rows, tc.reviews)
			if p.Heading != tc.heading {
				t.Errorf("heading = %q, want %q", p.Heading, tc.heading)
			}
			if got := names(p.NeedsYou); strings.Join(got, ",") != strings.Join(tc.needs, ",") {
				t.Errorf("needs you = %v, want %v", got, tc.needs)
			}
			if got := names(p.Others); strings.Join(got, ",") != strings.Join(tc.others, ",") {
				t.Errorf("others = %v, want %v", got, tc.others)
			}
			if p.Empty != tc.empty {
				t.Errorf("empty = %v, want %v", p.Empty, tc.empty)
			}
			switch {
			case tc.selected == "" && p.Selected != nil:
				t.Errorf("selected = %q, want none", p.Selected.Name)
			case tc.selected != "" && (p.Selected == nil || p.Selected.Name != tc.selected || !p.Selected.Selected):
				t.Errorf("selected = %+v, want %q", p.Selected, tc.selected)
			}
			selected := 0
			for _, r := range append(append([]overviewRow{}, p.NeedsYou...), p.Others...) {
				if r.Selected {
					selected++
				}
				switch r.Name {
				case "draft":
					if r.Href != "/p/draft/plan" || r.Live || r.Badge != planWaitingBadge {
						t.Errorf("review-only row = %+v", r)
					}
				case "run":
					if r.Href != "/p/run" || !r.Live || !r.Working || r.Task != "T1.2" || r.Phase != "executing" {
						t.Errorf("running row = %+v", r)
					}
				default:
					if r.Working || r.Task != "" {
						t.Errorf("row %q marked working or given a task line: %+v", r.Name, r)
					}
				}
			}
			if want := map[bool]int{true: 0, false: 1}[tc.empty]; selected != want {
				t.Errorf("%d rows selected, want %d", selected, want)
			}
		})
	}
}

// overviewKinds is one project row of each kind the home page can show,
// built and sorted as the daemon's seam builds them, plus the badge each
// must carry and whether it needs the owner.
func overviewKinds() ([]projectrows.Row, []struct {
	name, class, word string
	needs, working    bool
}) {
	inputs := []projectrows.Input{
		{Name: "k-running", HasPlan: true, Task: "T1.2", Title: "Second", Phase: "executing", Done: 1, Total: 3},
		{Name: "k-paused", HasPlan: true, Paused: true, Total: 3},
		{Name: "k-stopped", HasPlan: true, Stopped: true, StopTask: "T2.6", StopReason: "deploy check failed", Total: 3},
		{Name: "k-waiting", HasPlan: true, Kind: "park", StateKnown: true, Total: 3, Open: 2, Blocking: 1},
		{Name: "k-held", HasPlan: true, Kind: "usage_wait", StateKnown: true, Total: 3},
		{Name: "k-finished", HasPlan: true, Done: 3, Total: 3},
		{Name: "k-nothing", HasPlan: true, Total: 3},
		{Name: "k-noplan"},
		{Name: "k-planwait", HasPlan: true, Total: 2, PlanWaiting: true},
		{Name: "k-cantread", ReadErr: "ledger file is damaged\nmore"},
		{Name: "k-gone", Gone: true},
	}
	var rows []projectrows.Row
	for i, in := range inputs {
		in.Listed, in.Order = true, i
		rows = append(rows, projectrows.Build(in))
	}
	projectrows.Sort(rows)
	return rows, []struct {
		name, class, word string
		needs, working    bool
	}{
		{"k-running", "badge--running", "Building", false, true},
		{"k-paused", "badge--paused", "Paused", false, false},
		{"k-stopped", "badge--stopped", "Line stopped", true, false},
		{"k-waiting", "badge--needs-you", "Waiting on you", true, false},
		{"k-held", "badge--held", "Held for usage", false, false},
		{"k-finished", "badge--finished", "Finished", false, false},
		{"k-nothing", "badge--idle", "Nothing ready", false, false},
		{"k-noplan", "badge--idle", "No plan", false, false},
		{"k-planwait", "badge--needs-you", "Plan waiting for approval", true, false},
		{"k-cantread", "badge--error", "Can&#39;t read", true, false},
		{"k-gone", "badge--error", "Gone", true, false},
	}
}

// TestOverviewRows draws the home page with one row of each kind and checks
// each row's markup.
//
// Load-bearing: every needs-you row comes before every other row (the screen
// exists to put what needs the owner first), each project row carries the
// live markup with its own name (a row without it never updates), only the
// running row says data-working (the live indicator's Working), and the
// review-only plan is a needs-you row that links to its plan.
func TestOverviewRows(t *testing.T) {
	rg := newRig(t, Config{})
	rows, kinds := overviewKinds()
	rg.rows = rows
	rec := rg.pageDo("GET", "/", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	list := body[:strings.Index(body, `class="split__detail"`)]

	// 5 project rows need the owner, and the rig's plan "draft" waits alone.
	for _, w := range []string{"<h1>6 projects need you</h1>", `id="needs-you-title">Needs you</h2>`, `id="others-title">Everything else</h2>`} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q", w)
		}
	}
	if strings.Index(list, "Needs you</h2>") > strings.Index(list, "Everything else</h2>") {
		t.Error("Needs you is not above Everything else")
	}
	lastNeeds, firstOther := -1, len(list)
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			at := strings.Index(list, `id="project-`+k.name+`"`)
			if at < 0 {
				t.Fatalf("no row for %s", k.name)
			}
			if k.needs && at > lastNeeds {
				lastNeeds = at
			}
			if !k.needs && at < firstOther {
				firstOther = at
			}
			start := strings.LastIndex(list[:at], `<a class="row`)
			end := at + strings.Index(list[at:], "</a>")
			row := list[start:end]
			want := []string{
				`href="/p/` + k.name + `"`,
				`id="project-` + k.name + `" data-live="inbox ledger pause sessions planning" data-project="` + k.name + `"`,
				`<span class="row__title">` + k.name + `</span>`,
				`class="badge ` + k.class + `">` + k.word + `<`,
			}
			for _, w := range want {
				if !strings.Contains(row, w) {
					t.Errorf("row does not contain %q\n%s", w, row)
				}
			}
			if got := strings.Contains(row, "row--needs-you"); got != k.needs {
				t.Errorf("row--needs-you = %v, want %v", got, k.needs)
			}
			if got := strings.Contains(row, `data-working="true"`); got != k.working {
				t.Errorf("data-working = %v, want %v", got, k.working)
			}
		})
	}
	if lastNeeds > firstOther {
		t.Error("a needs-you row comes after a row that does not need the owner")
	}
	// The JSON keeps the seam's order: needs-you first.
	jrows := decode(t, rg.do(http.MethodGet, "/api/", "", ""))["rows"].([]any)
	seenOther := false
	for _, r := range jrows {
		needs := r.(map[string]any)["needs_owner"] == true
		if needs && seenOther {
			t.Errorf("rows JSON: a needs-you row after another: %s", dump(jrows))
			break
		}
		seenOther = seenOther || !needs
	}

	// Row details: the running task's id and phase, T7.9's words otherwise,
	// progress without a style attribute, and inbox counts in words.
	for _, w := range []string{
		`<span class="row__sub"><span class="tid">T1.2</span> executing</span>`,
		"the line is stopped after T2.6: deploy check failed",
		"<span class=\"row__sub\">ledger file is damaged</span>",
		`<progress class="progress__track" max="3" value="1" aria-label="1 of 3 tasks done"></progress>`,
		`<span class="row__meta">2 in inbox, 1 holds a task up</span>`,
		`class="badge badge--needs-you">Plan waiting for approval</span>`,
	} {
		if !strings.Contains(list, w) {
			t.Errorf("list does not contain %q", w)
		}
	}
	// The review-only plan: a needs-you row to its plan, no live markup.
	if !strings.Contains(list, `<a class="row row--needs-you" href="/p/draft/plan">`) {
		t.Errorf("the plan waiting alone is not a needs-you row to its plan")
	}
	// Desktop detail: the first needs-you row, selected in the list.
	if !strings.Contains(list, `<a class="row row--needs-you is-selected" href="/p/k-stopped" id="project-k-stopped"`) || !strings.Contains(list, `aria-current="true"`) {
		t.Errorf("the first needs-you row is not the selected one")
	}
	detail := body[strings.Index(body, `class="split__detail"`):]
	for _, w := range []string{`id="selected-k-stopped" data-live="inbox ledger pause sessions planning" data-project="k-stopped"`, "Line stopped", `<a class="btn btn--secondary" href="/p/k-stopped">Open k-stopped</a>`} {
		if !strings.Contains(detail, w) {
			t.Errorf("detail does not contain %q", w)
		}
	}
	if strings.Count(body, "is-selected") != 1 {
		t.Errorf("want exactly one selected row")
	}
	if styleAttr.MatchString(body) {
		t.Error("the overview holds a style attribute")
	}
}

// TestOverviewEmpty is the home page with no project and no plan waiting.
// Load-bearing: it says how to add a project, and has no selected detail.
func TestOverviewEmpty(t *testing.T) {
	rg := newRig(t, Config{})
	rg.rows, rg.reviews = nil, nil
	body := rg.pageDo("GET", "/", "").Body.String()
	for _, w := range []string{"<h1>Nothing needs you</h1>", `<p class="empty__title">No projects yet</p>`, "afk project add &lt;folder&gt;"} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q\n%s", w, body)
		}
	}
	for _, w := range []string{"split__detail", "Needs you</h2>", "Everything else</h2>"} {
		if strings.Contains(body, w) {
			t.Errorf("the empty page holds %q", w)
		}
	}
}

// TestOverviewEdges covers the two ways in that are not a well-formed list.
// Load-bearing: "no rows seam" (a server built without ProjectRows draws the
// empty state and does not panic, while the API's projects are still there)
// and "hostile name" (a project name is text from config.toml and must be
// escaped in the page and in the JSON).
func TestOverviewEdges(t *testing.T) {
	t.Run("no rows seam", func(t *testing.T) {
		h, err := New(Config{Token: testToken}, Deps{
			Projects:  func() []Project { return []Project{{Name: "app", Root: t.TempDir()}} },
			LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil },
			Sessions:  func(string) []SessionView { return nil },
			Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
			Pause:     func(string) error { return nil },
			Resume:    func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		rec := authReq{target: "/", cookie: goodCookie(testToken)}.send(h)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "No projects yet") {
			t.Fatalf("status %d, want 200 with the empty state\n%s", rec.Code, rec.Body.String())
		}
		m := decode(t, authReq{target: "/api/", bearer: testToken}.send(h))
		if rows, ok := m["rows"].([]any); !ok || len(rows) != 0 {
			t.Errorf("rows = %s, want an empty array", dump(m["rows"]))
		}
		if ps, _ := m["projects"].([]any); len(ps) != 1 {
			t.Errorf("projects = %s, want the one project", dump(m["projects"]))
		}
	})
	t.Run("hostile name", func(t *testing.T) {
		rg := newRig(t, Config{})
		const name = "<script>alert(1)</script>"
		rg.rows = []projectrows.Row{projectrows.Build(projectrows.Input{Name: name, Listed: true, Gone: true})}
		body := rg.pageDo("GET", "/", "").Body.String()
		if strings.Contains(strings.ToLower(noOwnScript(body)), "<script") {
			t.Errorf("the name was not escaped in the page:\n%s", body)
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(body, `href="/p/%3Cscript%3Ealert%281%29%3C%2Fscript%3E"`) {
			t.Errorf("the escaped name or its link is missing:\n%s", body)
		}
		raw := rg.do(http.MethodGet, "/api/", "", "").Body.String()
		if strings.Contains(raw, "<script") {
			t.Errorf("the name was not escaped in the JSON:\n%s", raw)
		}
		rows := decode(t, rg.do(http.MethodGet, "/api/", "", ""))["rows"].([]any)
		if rows[0].(map[string]any)["name"] != name {
			t.Errorf("rows = %s, want the name as written", dump(rows))
		}
	})
}
