package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
)

const idScript = "kmn345"

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
			if !strings.Contains(body, `name="viewport"`) || strings.Contains(strings.ToLower(body), "<script") {
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
		if strings.Contains(strings.ToLower(body), "<script") {
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

// TestPageTemplatesHaveNoScript keeps the promise of a script-free page at
// the source: nothing in the templates may open a script tag.
func TestPageTemplatesHaveNoScript(t *testing.T) {
	entries, err := pageFiles.ReadDir("templates")
	if err != nil || len(entries) == 0 {
		t.Fatalf("templates: %v", err)
	}
	for _, e := range entries {
		b, _ := pageFiles.ReadFile("templates/" + e.Name())
		if strings.Contains(strings.ToLower(string(b)), "<script") {
			t.Errorf("%s holds a script tag", e.Name())
		}
	}
}
