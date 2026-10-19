package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/usagestate"
)

const idScript = "kmn345"

// ownScript is the script tags every page carries: the hashed live.js, confirm.js, submit.js and chat.js.
var ownScript = regexp.MustCompile(`<script src="/static/[0-9a-f]{12}/(live|confirm|submit|chat)\.js" defer></script>`)

// noOwnScript removes that tag, so the tests that look for injected markup
// still see any other script tag a hostile text managed to open.
func noOwnScript(body string) string { return ownScript.ReplaceAllString(body, "") }

// pageDo sends one request with the cookie, the way a browser that has
// signed in does.
func (rg *rig) pageDo(method, path, body string) *httptest.ResponseRecorder {
	rg.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
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
		name  string
		setup func(*rig)
		path  string
		// status is the answer's status; zero means 200.
		status  int
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
		{name: "project", setup: (*rig).seedInbox, path: "/p/app", want: []string{
			`<span class="badge badge--running project__card-badge">Building</span>`, `<time class="meta" data-updated datetime="2026-10-12T12:00:00Z">Updated just now</time>`,
			"<h2>1 question holds up T1.2</h2>", "<p>T1.2 Second: executing</p>", "1 of 3 tasks done",
			`<a class="btn btn--primary" href="/p/app/inbox">Answer in the inbox</a>`, `data-working="true"`,
			"Running now", `<span class="tid">T1.2</span> <span class="row__title">Title of T1.2</span>`, "Session t1-2 · executing",
			`data-confirm="confirm-pause">Pause project&hellip;</a>`, `action="/p/app/pause"`, `action="/p/app/resume"`,
			`<span class="row__title">Read the plan</span>`,
			`<a class="nav-item" href="/p/app/inbox"><span>Inbox</span><span class="count">2</span></a>`,
			`<a class="nav-item" href="/p/app/tasks"><span>Tasks</span><span class="meta">1 of 3</span></a>`,
			`<a class="nav-item" href="/p/app/sessions"><span>Sessions</span><span class="count">`,
			`<a class="nav-item" href="/p/app/plan"><span>Plan</span><span class="meta">1 of 3</span></a>`,
			`<a class="row" href="/p/app/sessions/t1-2">`,
			`<a class="nav-item" href="/usage"><span>Usage</span></a>`,
			`<dt>Folder</dt><dd class="path">`, "<dt>Repository</dt><dd>None set</dd>",
		}, notWant: []string{"Which database?", ">Chat<", "A planning session is open", "There is no plan to change yet."}},
		{name: "project offers the three plan changes", path: "/p/app", want: []string{
			`<h2 class="section-head__title" id="change-title">Change the plan</h2>`, `id="project-change" data-live="planning" data-project="app"`,
			`<a class="row" href="/p/app/plan/change?mode=extend">`, `<span class="row__title">Extend</span>`, "Add new work to the plan.",
			`<a class="row" href="/p/app/plan/change?mode=amend">`, `<span class="row__title">Amend</span>`, "Change one thing in the plan, in your words.",
			`<a class="row" href="/p/app/plan/change?mode=reconcile">`, `<span class="row__title">Reconcile</span>`, "Bring the plan in line with a document, such as a design system.",
		}, notWant: []string{"Open its chat", "<textarea"}},
		{name: "project with a session open says so instead", setup: func(rg *rig) {
			must(rg.t, plansession.WriteState(rg.root, plansession.State{Mode: "amend", Phase: "draft", UpdatedAt: "2026-10-12T11:00:00Z"}))
		}, path: "/p/app", want: []string{"A planning session is open (amend, draft).", `<a class="btn btn--secondary" href="/p/app/plan/chat">Open its chat</a>`},
			notWant: []string{"plan/change?mode=", "Add new work to the plan."}},
		{name: "project with no plan has no change rows", setup: func(rg *rig) { delete(rg.plans, rg.root) }, path: "/p/app", want: []string{"Change the plan", "There is no plan to change yet."},
			notWant: []string{"plan/change?mode=", "Open its chat"}},
		{name: "project with nothing open has a quiet inbox count", path: "/p/app", want: []string{`<span>Inbox</span><span class="count count--quiet">0</span>`, "<h2>Building T1.2</h2>"}, notWant: []string{"Answer in the inbox"}},
		{name: "project paused shows Resume", setup: func(rg *rig) {
			_ = daemonstate.Pause(rg.root, testNow)
			rg.rows = []projectrows.Row{projectrows.Build(projectrows.Input{Name: "app", Root: rg.root, Listed: true, HasPlan: true, Paused: true, Done: 1, Total: 3})}
		}, path: "/p/app", want: []string{"<h2>Paused by you</h2>", `<a class="btn btn--primary" href="/p/app/confirm/resume" data-confirm="confirm-resume">Resume</a>`, `data-confirm="confirm-resume">Resume project&hellip;</a>`, `action="/p/app/resume"`}, notWant: []string{"Pause project&hellip;"}},
		{name: "project with a repository", setup: func(rg *rig) { rg.repository = "https://github.com/o/app.git" }, path: "/p/app", want: []string{"<dt>Repository</dt><dd>https://github.com/o/app.git</dd>"}, notWant: []string{"None set"}},
		{name: "project with planning open", setup: func(rg *rig) {
			must(rg.t, plansession.WriteState(rg.root, plansession.State{Mode: "amend", Phase: "draft", UpdatedAt: "2026-10-12T11:00:00Z"}))
		}, path: "/p/app", want: []string{`<span class="row__title">Planning: amend</span> <span class="badge badge--running">draft</span>`}, notWant: []string{"Read the plan<"}},
		{name: "project whose plan waits", setup: func(rg *rig) { rg.rows[0].PlanWaiting = true }, path: "/p/app", want: []string{`<a class="btn btn--primary" href="/p/app/plan">Read and approve the plan</a>`, `<span class="row__title">Plan waiting for your approval</span>`}},
		{name: "project with no running session", setup: func(rg *rig) { rg.sessions = nil }, path: "/p/app", want: []string{`id="project-running" data-live="sessions ledger" data-project="app"></div>`}, notWant: []string{"Running now"}},
		{name: "project with a space in its name", path: "/p/my%20app", want: []string{"my app", `action="/p/my%20app/pause"`, `href="/p/my%20app/inbox"`}},
		{name: "item with a blank answer form", setup: (*rig).seedInbox, path: "/p/app/inbox/" + idBlocking, want: []string{"Which database?", "Pick one.", "<textarea", `action="/p/app/inbox/` + idBlocking + `/answer"`, `class="badge badge--blocking"`, `<span class="tid">T1.2</span>`, "<h2>Which database?</h2>", `class="prose item-body"`, "Question from t1-2 · 1 hour ago", "data-enhance", "data-answer-form", `data-live="inbox"`, `data-send>Send answer`}, notWant: []string{"type=\"radio\"", "option--recommended", "Mark as done", "disabled"}},
		{name: "item with options", setup: (*rig).seedInbox, path: "/p/app/inbox/" + idOptions, want: []string{`class="option option--recommended" name="choice" value="tabs" aria-pressed="false"`, `class="option" name="choice" value="spaces" aria-pressed="false"`, `<span class="option__mark">Recommended</span>`, "Your answer", "Or write your own", `name="answer"`, `data-send>Send answer`}, notWant: []string{"type=\"radio\"", "checked", "(recommended)", "disabled", "required"}},
		{name: "owner task shows Mark as done only", setup: (*rig).seedOwnerTask, path: "/p/app/inbox/" + idOwner, want: []string{"Create the account.\nThen come back.", `<input type="hidden" name="answer" value="done">`, "Mark as done", "Your task from t1-2 · 1 hour ago"}, notWant: []string{"<textarea", "Send answer", "class=\"option", "name=\"choice\""}},
		{name: "answered item", setup: (*rig).seedInbox, path: "/p/app/inbox/" + idAnswered, want: []string{"seen", "through cli"}, notWant: []string{"<form", "<textarea"}},
		{name: "inbox list", setup: (*rig).seedInbox, path: "/p/app/inbox",
			want:    []string{"<h1>Inbox</h1>", `id="inbox-list" data-live="inbox" data-project="app"`, "1 of 2 hold a task up", `badge badge--blocking`, `<span class="tid">T1.2</span>`, `<span class="row__title">Which database?</span>`, `href="/p/app/inbox/` + idBlocking + `"`, "Question from t1-2 · 1 hour ago", "Answered (1)", "<details"},
			notWant: []string{"Nothing needs you"}},
		{name: "tasks list, open by default", setup: (*rig).useTasksPlan, path: "/p/app/tasks", want: []string{
			"<h1>Tasks</h1>", `aria-label="2 of 5 tasks done"`,
			`<a href="/p/app/tasks?show=open" aria-current="page">Open</a>`, `<a href="/p/app/tasks?show=done">Done</a>`, `<a href="/p/app/tasks?show=all">All</a>`,
			`>M1 — Foundations</h2><span class="meta">1 of 2 done</span>`, `>M2 — Polish</h2><span class="meta">0 of 2 done</span>`,
			`<a class="row" href="/p/app/tasks/T1.2">`, `<span class="tid">T1.2</span> <span class="row__title">Second</span> <span class="badge badge--running">running</span>`,
			`<span class="tid">T2.1</span> <span class="row__title">Fourth &lt;b&gt;bold&lt;/b&gt;</span> <span class="badge badge--pending">pending</span>`,
			`<span class="row__meta">Waits for T1.2</span>`, `<span class="badge badge--failed">failed</span>`,
		}, notWant: []string{"T0.1", "T1.1", "T1.3", ">Tasks</h2>", "M1 notes", "No open tasks", "<b>bold</b>"}},
		{name: "tasks list, done", setup: (*rig).useTasksPlan, path: "/p/app/tasks?show=done", want: []string{
			`<a href="/p/app/tasks?show=done" aria-current="page">Done</a>`,
			`>Tasks</h2><span class="meta">1 of 1 done</span>`, `>M1 — Foundations</h2><span class="meta">1 of 2 done</span>`,
			`<span class="tid">T1.3</span> <span class="row__title">Third</span> <span class="badge badge--superseded">superseded (by T2.1)</span>`,
			`<span class="badge badge--done">done</span>`,
		}, notWant: []string{"T1.2", "T2.2", "M2 — Polish", "Waits for"}},
		{name: "tasks list, all", setup: (*rig).useTasksPlan, path: "/p/app/tasks?show=all", want: []string{
			`<a href="/p/app/tasks?show=all" aria-current="page">All</a>`,
			">T0.1<", ">T1.1<", ">T1.2<", ">T1.3<", ">T2.1<", ">T2.2<", `>M2 — Polish</h2><span class="meta">0 of 2 done</span>`,
		}},
		{name: "tasks list with nothing open", setup: func(rg *rig) {
			rg.useTasksPlan()
			for i := range rg.ledger.Tasks {
				rg.ledger.Tasks[i].Status = ledger.Done
			}
			rg.ledger.Tasks = append(rg.ledger.Tasks, ledger.Entry{ID: "T2.1", Status: ledger.Done})
		}, path: "/p/app/tasks", want: []string{`<p class="empty__title">No open tasks</p>`, `aria-label="6 of 6 tasks done"`}, notWant: []string{`class="section"`}},
		{name: "tasks list with nothing done", setup: func(rg *rig) { rg.useTasksPlan(); rg.ledger = ledger.Ledger{} }, path: "/p/app/tasks?show=done",
			want: []string{`<p class="empty__title">Nothing done yet</p>`}, notWant: []string{`class="section"`}},
		{name: "tasks list of a project with no tasks", path: "/p/my%20app/tasks", want: []string{`<p class="empty__title">No tasks in the plan</p>`}},
		{name: "tasks list whose ledger cannot be read", setup: func(rg *rig) { rg.loadErr = errors.New("the ledger is damaged") }, path: "/p/app/tasks",
			want: []string{"The plan or the ledger could not be read: the ledger is damaged"}, notWant: []string{"empty__title"}},
		{name: "task with a record", setup: func(rg *rig) {
			rg.useTasksPlan()
			rg.tokens = map[string]TaskTokens{"T1.2": recordedTokens}
		}, path: "/p/app/tasks/T1.2", want: []string{
			`<h1 class="tid task__id">T1.2</h1>`, `<span class="badge badge--running">running</span>`, "<h2>Second</h2>",
			"<p>Running in session t1-2, attempt 2.</p>", "Depends on T1.1.",
			`id="task-status" data-live="ledger sessions" data-project="app"`, `<a href="/p/app/sessions/t1-2">Open its session</a>`,
			`<h2 class="section-head__title" id="tokens-title">Tokens</h2><span class="meta">1.0K weighted</span>`,
			`<div class="split-bar" role="img" aria-label="Tokens by phase: plan 40.0%, execute 59.5%, review 0.5%"><span class="split-bar__seg split-bar__seg--plan w-40"></span><span class="split-bar__seg split-bar__seg--execute w-59"></span><span class="split-bar__seg split-bar__seg--review w-1"></span></div>`,
			`<span class="legend__swatch split-bar__seg--review" aria-hidden="true"></span>review<span class="legend__value">5 · 0.5%</span>`,
			`<th scope="col">role</th><th scope="col">calls</th><th scope="col">input</th><th scope="col">output</th><th scope="col">weighted</th>`,
			"<tr><td>executor</td><td>3</td><td>1200</td><td>300</td><td>595</td></tr>",
			`<a class="btn btn--secondary" href="/p/app/tasks/T1.2/status" data-confirm="confirm-status">Set status&hellip;</a>`,
		}, notWant: []string{"No tokens recorded"}},
		// Load-bearing: the ledger names t1-2 but discovery lists no such
		// session, so a link would lead to a 404 and must not be drawn.
		{name: "task page whose session is gone", setup: func(rg *rig) {
			rg.useTasksPlan()
			rg.sessions = []SessionView{{Name: "other", Phase: "executing", HostRunning: true}}
		}, path: "/p/app/tasks/T1.2", want: []string{"Running in session t1-2"}, notWant: []string{"Open its session"}},
		{name: "task without a record", setup: (*rig).useTasksPlan, path: "/p/app/tasks/T2.1", want: []string{
			`<span class="badge badge--pending">pending</span>`, "Waiting for T1.2 to be done first.", "Depends on T1.2.",
			`<p class="muted">No tokens recorded for this task yet.</p>`,
		}, notWant: []string{"split-bar", "<table", "notice--error"}},
		{name: "task whose tokens cannot be read", setup: func(rg *rig) {
			rg.useTasksPlan()
			rg.tokens = map[string]TaskTokens{"T2.2": {State: TokensUnreadable, Err: "The token ledger is damaged at line 3."}}
		}, path: "/p/app/tasks/T2.2", want: []string{
			"Failed. The build loop will not start it again until its status is set back to pending.",
			`<div class="notice notice--error" role="alert"><div class="notice__body">The token ledger is damaged at line 3.</div></div>`,
		}, notWant: []string{"split-bar", "No tokens recorded"}},
		{name: "task superseded", setup: (*rig).useTasksPlan, path: "/p/app/tasks/T1.3", want: []string{
			`<span class="badge badge--superseded">superseded (by T2.1)</span>`, "Superseded by T2.1, which do its work instead.",
		}},
		{name: "task that is not in the plan", setup: (*rig).useTasksPlan, path: "/p/app/tasks/T9.9", status: 404, want: []string{
			"There is no task with that id in this project&#39;s plan.",
		}, notWant: []string{"<h2>", "Tokens"}},
		{name: "inbox list with nothing open", path: "/p/app/inbox", want: []string{"Nothing needs you", "When a task has a question for you"}, notWant: []string{"hold a task up", "<details"}},
		{name: "inbox list with no blocking item", setup: func(rg *rig) {
			rg.seedInbox()
			if _, err := rg.store().Answer(idBlocking, "x", inbox.ViaCLI); err != nil {
				rg.t.Fatal(err)
			}
		}, path: "/p/app/inbox", want: []string{"Tabs or spaces?", "Answered (2)"}, notWant: []string{"hold a task up"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.pageDo("GET", tc.path, "")
			wantStatus := tc.status
			if wantStatus == 0 {
				wantStatus = 200
			}
			if rec.Code != wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, wantStatus, rec.Body.String())
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
				if strings.Contains(noSignOut(body), w) {
					t.Errorf("page contains %q, which it should not", w)
				}
			}
			if strings.Contains(body, testPassword) {
				t.Errorf("the page contains the password")
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
	for _, path := range []string{"/p/app/inbox/" + idScript, "/p/app/inbox"} {
		body := rg.pageDo("GET", path, "").Body.String()
		if strings.Contains(strings.ToLower(noOwnScript(body)), "<script") {
			t.Errorf("%s: markup was not escaped:\n%s", path, body)
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("%s: the escaped title is missing:\n%s", path, body)
		}
	}

	// The project screen lists no inbox item, so its text reaches the page
	// through the task's title (Running now), the row's detail, the
	// repository and the folder instead.
	rg.tasks[1].Title = "<script>alert(3)</script>"
	rg.rows[0].Detail = "<img src=x onerror=alert(4)>"
	rg.repository = `https://h/<b>"r`
	body := rg.pageDo("GET", "/p/app", "").Body.String()
	if strings.Contains(strings.ToLower(noOwnScript(body)), "<script") || strings.Contains(body, "<img") || strings.Contains(body, "<b>") {
		t.Errorf("/p/app: markup was not escaped:\n%s", body)
	}
	for _, w := range []string{"&lt;script&gt;alert(3)&lt;/script&gt;", "&lt;img src=x onerror=alert(4)&gt;", "https://h/&lt;b&gt;&#34;r"} {
		if !strings.Contains(body, w) {
			t.Errorf("/p/app: the escaped %q is missing:\n%s", w, body)
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
		{name: "answer redirects to the item", path: answer(idBlocking), body: "answer=Use+Postgres", wantStatus: 303, wantLoc: "/p/app/inbox?done=answered&item=" + idBlocking, wantAnswer: "Use Postgres"},
		{name: "radio only", path: answer(idOptions), body: "choice=tabs&answer=", wantStatus: 303, wantLoc: "/p/app/inbox?done=answered&item=" + idOptions, wantAnswer: "tabs"},
		{name: "owner task records done", path: answer(idOwner), body: "answer=done", wantStatus: 303, wantLoc: "/p/app/inbox?done=done&item=" + idOwner, wantAnswer: "done"},
		{name: "text beats radio", path: answer(idOptions), body: "choice=tabs&answer=neither", wantStatus: 303, wantLoc: "/p/app/inbox?done=answered&item=" + idOptions, wantAnswer: "neither"},
		{name: "empty answer shows the error page", path: answer(idBlocking), body: "answer=", wantStatus: 400, wantText: "The answer was empty"},
		{name: "second answer shows the first", path: answer(idAnswered), body: "answer=again", wantStatus: 409, wantText: "first answer is the one that is kept"},
		{name: "unknown item", path: answer("zzz999"), body: "answer=x", wantStatus: 404, wantText: "no inbox item"},
		{name: "pause redirects to the project", path: "/p/app/pause", body: "confirm=yes", wantStatus: 303, wantLoc: "/p/app?done=paused"},
		{name: "resume redirects to the project", path: "/p/app/resume", body: "confirm=yes", wantStatus: 303, wantLoc: "/p/app?done=resumed"},
		{name: "project with a space redirects escaped", path: "/p/my%20app/pause", body: "confirm=yes", wantStatus: 303, wantLoc: "/p/my%20app?done=paused"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			rg.seedOwnerTask()
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
	if rec := rg.pageDo("POST", "/p/app/pause", "confirm=yes"); rec.Code != 303 {
		t.Fatalf("pause: %d", rec.Code)
	}
	if len(rg.paused) != 1 || !daemonstate.Paused(rg.root) {
		t.Errorf("pause called %d times, paused = %v", len(rg.paused), daemonstate.Paused(rg.root))
	}
	if rec := rg.pageDo("POST", "/p/app/resume", "confirm=yes"); rec.Code != 303 {
		t.Fatalf("resume: %d", rec.Code)
	}
	if len(rg.resumed) != 1 || daemonstate.Paused(rg.root) {
		t.Errorf("resume called %d times, paused = %v", len(rg.resumed), daemonstate.Paused(rg.root))
	}
	rg.pauseErr = http.ErrAbortHandler
	rec := rg.pageDo("POST", "/p/app/pause", "confirm=yes")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "could not be paused") {
		t.Errorf("failing pause: %d\n%s", rec.Code, rec.Body.String())
	}
}

// TestPagePauseNeedsConfirm is the server-side half of the confirm sheet:
// load-bearing rows are the empty body and confirm=no, which used to pause
// the project, and the confirm in the address, which must not count. None may
// call Pause or Resume or change the paused flag.
func TestPagePauseNeedsConfirm(t *testing.T) {
	tests := []struct {
		name, path, body, wantText string
		startPaused                bool
	}{
		{name: "pause with no body", path: "/p/app/pause", wantText: "pausing needs the confirm"},
		{name: "pause with confirm=no", path: "/p/app/pause", body: "confirm=no", wantText: "Nothing changed"},
		{name: "pause with confirm in the address", path: "/p/app/pause?confirm=yes", wantText: "Nothing changed"},
		{name: "resume with no body", path: "/p/app/resume", startPaused: true, wantText: "resuming needs the confirm"},
		{name: "resume with confirm=no", path: "/p/app/resume", body: "confirm=no", startPaused: true, wantText: "press Resume project"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.startPaused {
				must(t, daemonstate.Pause(rg.root, testNow))
			}
			rec := rg.pageDo("POST", tc.path, tc.body)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.wantText) {
				t.Fatalf("status %d, want 400 with %q\n%s", rec.Code, tc.wantText, rec.Body.String())
			}
			if len(rg.paused)+len(rg.resumed) != 0 || daemonstate.Paused(rg.root) != tc.startPaused {
				t.Errorf("pause calls %v, resume calls %v, paused = %v", rg.paused, rg.resumed, daemonstate.Paused(rg.root))
			}
		})
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

// TestPageSignInForm: a page asked for without a cookie gets the sign-in
// form at its own address, an old token query visit included now that the
// token is gone: no cookie is handed out and nothing redirects.
// Load-bearing: the token query row, the old way in, which must open nothing.
func TestPageSignInForm(t *testing.T) {
	rg := newRig(t, Config{})
	for _, target := range []string{"/p/app", "/p/app?" + url.Values{"token": {"anything"}, "x": {"1"}}.Encode()} {
		rec := httptest.NewRecorder()
		rg.h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		body := rec.Body.String()
		if rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %q", target, rec.Code, rec.Header().Get("Content-Type"))
		}
		if !strings.Contains(body, `action="/signin"`) || !strings.Contains(body, `type="password"`) {
			t.Errorf("%s: not the sign-in form\n%s", target, body)
		}
		if setCookie(rec) != nil || rec.Header().Get("Location") != "" {
			t.Errorf("%s: a cookie or a redirect was handed out", target)
		}
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
	for _, f := range []string{"live.js", "confirm.js", "submit.js"} {
		if !strings.Contains(string(layout), `<script src="{{asset "`+f+`"}}" defer></script>`) {
			t.Errorf("layout.html does not load %s", f)
		}
	}
}

// idAttr finds every id attribute on a page.
var idAttr = regexp.MustCompile(` id="([^"]*)"`)

// TestPageLiveMarkup checks that each page loads the hashed script and that
// the live regions exist with the parts and project the script matches on.
//
// Load-bearing rows: "project", where the inbox list and the summary carry
// the project's own name (a region with the wrong project would never be
// swapped), and "usage", whose project is empty (machine-wide). The page with
// the space in its name proves the attribute is escaped, not path-escaped.
func TestPageLiveMarkup(t *testing.T) {
	scriptRe := regexp.MustCompile(`<script src="/static/[0-9a-f]{12}/(live|confirm|chat)\.js" defer></script>`)
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
			`aria-live="polite" aria-label="State of app">` + "\n" + `<div class="card" id="project-state" data-live="ledger pause sessions planning inbox" data-project="app"`,
			`id="project-badge" data-live="ledger pause sessions planning inbox" data-project="app"`,
			`id="project-running" data-live="sessions ledger" data-project="app"`,
			`id="project-pause" data-live="pause" data-project="app"`,
			`id="project-plan" data-live="planning" data-project="app"`,
			`id="project-nav" data-live="inbox ledger sessions" data-project="app"`,
			`<span class="live" data-live-status hidden>Live</span>`,
			`<span class="live topnav__live" data-live-mirror hidden>Live</span>`,
		}},
		{name: "usage", path: "/usage", want: []string{`id="usage-reading" data-live="usage" data-project=""`}},
		{name: "tasks", path: "/p/my%20app/tasks", want: []string{
			`id="tasks-progress" data-live="ledger" data-project="my app"`,
			`id="tasks-list" data-live="ledger" data-project="my app" aria-live="polite"`,
		}},
		{name: "task", path: "/p/app/tasks/T1.2", want: []string{`<div class="card" id="task-status" data-live="ledger sessions" data-project="app">`}},
		{name: "sessions", path: "/p/app/sessions", want: []string{`id="sessions-list" data-live="sessions" data-project="app" aria-live="polite"`}},
		{name: "planning chat", path: "/p/my%20app/plan/chat", want: []string{
			`<div id="chat-thread" class="thread" aria-live="polite" data-live="planning" data-project="my app"`,
			`<div id="chat-reply" class="chat-reply" data-live="planning" data-project="my app" data-live-form>`,
		}},
		{name: "session", path: "/p/app/sessions/t1-2", want: []string{
			`id="session-badges" data-live="sessions" data-project="app"`,
			`aria-live="polite" aria-label="State of t1-2">` + "\n" + `<div class="card" id="session-state" data-live="sessions" data-project="app">`,
		}},
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
			// live.js swaps a region by id, so two elements with one id
			// would swap the wrong one.
			seen := map[string]bool{}
			for _, m := range idAttr.FindAllStringSubmatch(body, -1) {
				if seen[m[1]] {
					t.Errorf("id %q is used twice", m[1])
				}
				seen[m[1]] = true
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
	tests := []struct {
		name    string
		setup   func(*rig)
		path    string
		status  int
		want    []string
		notWant []string
	}{
		{name: "configured project", path: "/p/app/plan", status: 200,
			want: []string{"Plan: app", "docs/PLAN.md", "1 milestone,", "2 tasks", "(1 for you to do yourself)", "1 open question.", "No problems found", `<span class="stat__value">2</span><span class="stat__label">Tasks</span>`,
				`<span class="stat__value">0</span><span class="stat__label">Errors</span>`, "Which colour?", "Needed by: T1.2", `<h4 id="T1.1">T1.1 — First</h4>`, "<strong>Goal:</strong> g", "<th>Topic</th>", `href="/p/app"`},
			notWant: []string{"Waiting for approval", "Which font?</li>", "Errors to fix"}},
		{name: "configured project is not waiting", path: "/p/app/plan", status: 200,
			setup:   func(rg *rig) { rg.reviews = append(rg.reviews, Project{Name: "app-2", Root: rg.root}) },
			notWant: []string{"Waiting for approval"}},
		{name: "review-only project", path: "/p/draft/plan", status: 200,
			want:    []string{"Plan · draft", `<h1 id="draft-plan">Draft plan</h1>`, "A plan nobody has approved yet.", `badge--error">`, `href="/" aria-label="Back to all projects"`},
			notWant: []string{`href="/p/draft"`, "Back to the overview", "Waiting for approval", "run afk plan approve"}},
		{name: "project name with a space", path: "/p/my%20app/plan", status: 200,
			want: []string{"Plan: my app", `href="/p/my%20app"`, "Problem: The plan could not be read"}},
		{name: "plan that cannot be read", path: "/p/app/plan", status: 200,
			setup:   func(rg *rig) { rg.planErr = errors.New("permission denied") },
			want:    []string{"Problem: The plan could not be read: permission denied."},
			notWant: []string{"milestone", "Open questions", `class="plan"`}},
		{name: "plan with errors", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) {
				rg.planStatus[rg.root] = approvable()
				rg.plans[rg.root] = strings.Replace(testPlan, "- **Depends on:** T1.1\n", "- **Depends on:** T7.7\n", 1)
			},
			want: []string{"T7.7", `badge--error">1 error<`, `<span class="badge badge--error">Error</span>`, `href="#T1.2"`, "Fix 1 error before approving.", `class="notice notice--error" role="alert"`}},
		{name: "plan with no open question", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.plans[rg.root] = strings.Replace(testPlan, "| Which colour? | T1.2 |\n", "", 1) },
			want:  []string{"No question in the plan is open.", "0 open questions."}},
		{name: "hostile plan", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.plans[rg.root] = hostilePlan },
			want: []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "&lt;script&gt;alert(5)&lt;/script&gt; or &lt;b&gt;this&lt;/b&gt;?",
				"&lt;img src=x onerror=alert(4)&gt;", "onclick=&#34;alert(3)&#34;"},
			notWant: []string{"<script", "<img", "<b>", `onclick="`, `href="javascript:`}},
		{name: "no problems has no finding list", path: "/p/app/plan", status: 200,
			notWant: []string{`class="finding"`, "plan-findings"}},
		{name: "warnings only", path: "/p/app/plan", status: 200,
			setup:   func(rg *rig) { rg.plans[rg.root] = strings.Replace(testPlan, "- **Scope:** s\n", "", 1) },
			want:    []string{`badge--warning">`, `<span class="badge badge--warning">Warning</span>`, "[missing-task-field]"},
			notWant: []string{"badge--error", "notice--error"}},
		{name: "plan line for a new plan in a middle phase", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) {
				rg.planStatus[rg.root] = planLine("new", "check", "Plan · new · check — the checker is running")
			},
			want:    []string{`<p class="plan-line"><a href="/p/app/plan/chat">Plan · new · check — the checker is running</a></p>`, "The plan can be approved once the planning session reaches review; it is at check now."},
			notWant: []string{"data-confirm", "<dialog"}},
		{name: "plan line for a new plan at review", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.planStatus[rg.root] = approvable() },
			want:  []string{`<p class="plan-line"><a href="/p/app/plan/chat">Plan · new · review — waiting for your approval</a></p>`, `data-confirm="plan-approve"`}},
		{name: "plan line for an extend session", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.planStatus[rg.root] = planLine("extend", "review", "Plan · extend · review") },
			want:  []string{`<p class="plan-line"><a href="/p/app/plan/chat">Plan · extend · review</a></p>`, "changes a plan that is already being built"}, notWant: []string{"<dialog"}},
		{name: "plan line for an amend session", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.planStatus[rg.root] = planLine("amend", "round", "Plan · amend · round") },
			want:  []string{`<p class="plan-line"><a href="/p/app/plan/chat">Plan · amend · round</a></p>`}},
		{name: "plan line for a reconcile session", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.planStatus[rg.root] = planLine("reconcile", "round", "Plan · reconcile · round") },
			want:  []string{`<p class="plan-line"><a href="/p/app/plan/chat">Plan · reconcile · round</a></p>`}},
		{name: "plan line for an approved plan", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) { rg.planStatus[rg.root] = planLine("new", "approved", "Plan approved") },
			want:  []string{`<p class="plan-line"><a href="/p/app/plan/chat">Plan approved</a></p>`, "This plan is already approved."}, notWant: []string{"<dialog"}},
		{name: "damaged planning state shows its line and the reason", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) {
				rg.planStatus[rg.root] = PlanStatus{Known: true, Session: true, Damaged: true, Line: "Plan · damaged"}
			},
			want: []string{`<p class="plan-line"><a href="/p/app/plan/chat">Plan · damaged</a></p>`, "state file is damaged"}},
		{name: "no planning session has no plan line", path: "/p/app/plan", status: 200,
			want:    []string{"There is no planning session, so there is nothing to approve."},
			notWant: []string{"plan-line"}},
		{name: "review-list plan shows its plan line and the approve action", path: "/p/draft/plan", status: 200,
			setup: func(rg *rig) {
				rg.plans[rg.draft] = testPlan
				rg.planStatus[rg.draft] = approvable()
			},
			want: []string{`<p class="plan-line">`, `data-confirm="plan-approve"`, `href="/p/draft/plan/approve"`}},
		{name: "project page carries the plan line", path: "/p/app", status: 200,
			setup: func(rg *rig) { rg.planStatus[rg.root] = approvable() },
			want:  []string{`<span class="row__meta">Plan · new · review — waiting for your approval</span>`}},
		{name: "plan line is plain text without a session", path: "/p/app/plan", status: 200,
			setup: func(rg *rig) {
				rg.planStatus[rg.root] = PlanStatus{Known: true, Line: "Plan · no session"}
			},
			want:    []string{`<p class="plan-line">Plan · no session</p>`},
			notWant: []string{"plan/chat"}},
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
			want: []string{`<a class="row" href="/p/app/plan">`, `<span class="row__title">Read the plan</span>`}},
		{name: "project page link is escaped", path: "/p/my%20app", status: 200,
			want: []string{`<a class="row" href="/p/my%20app/plan">`}},
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
			if strings.Contains(body, testPassword) {
				t.Errorf("the page contains the password")
			}
			if !strings.Contains(body, `name="viewport"`) || strings.Contains(strings.ToLower(noOwnScript(body)), "<script") {
				t.Errorf("page must have the viewport tag and no script tag")
			}
		})
	}
}

// planLine is the planning status of a session in a mode and phase, with its
// plan line already worded (the root words it; the page shows it as given).
func planLine(mode, phase, line string) PlanStatus {
	return PlanStatus{Known: true, Session: true, Mode: mode, Phase: phase, Line: line, Problems: []string{}}
}

// TestPlanPageFindingsErrorsFirst is load-bearing: the draft plan's findings
// come from the checker with warnings and an error mixed, and the page must
// list the errors before the warnings, whatever order the checker gave them.
func TestPlanPageFindingsErrorsFirst(t *testing.T) {
	rg := newRig(t, Config{})
	body := rg.pageDo("GET", "/p/draft/plan", "").Body.String()
	e := strings.Index(body, `<span class="badge badge--error">Error</span>`)
	w := strings.Index(body, `<span class="badge badge--warning">Warning</span>`)
	if e < 0 || w < 0 {
		t.Fatalf("the draft plan should give an error and a warning (error at %d, warning at %d)\n%s", e, w, body)
	}
	if e > w {
		t.Errorf("a warning is listed before the first error")
	}
}

// TestPlanPageApproveArea is load-bearing for the confirm: Approve the plan…
// is only an action behind data-confirm and a dialog whose safe button has
// the focus, it is absent whenever the plan cannot be approved (each reason
// shows its sentence), and with checker errors the red notice stands in its
// place with no link, form or dialog. The drawn page's own links must start
// with / and answer 200, so none is a dead end.
func TestPlanPageApproveArea(t *testing.T) {
	withErrors := func(rg *rig) {
		rg.plans[rg.root] = strings.Replace(strings.Replace(testPlan, "- **Depends on:** T1.1\n", "- **Depends on:** T7.7\n", 1), "- **Scope:** s\n", "", -1)
		rg.planStatus[rg.root] = approvable()
	}
	noAction := []string{"data-confirm", "<dialog", "<form", "/plan/approve"}
	tests := []struct {
		name    string
		setup   func(*rig)
		path    string
		want    []string
		notWant []string
	}{
		{name: "approvable", path: "/p/app/plan",
			setup: func(rg *rig) { rg.planStatus[rg.root] = approvable() },
			want: []string{`href="/p/app/plan/approve" data-confirm="plan-approve"`, `<dialog class="confirm-sheet" id="plan-approve"`,
				"Approve the plan for app?", `autofocus>Not yet</button>`, "Approve and start", `action="/p/app/plan/approve"`, `name="confirm" value="yes"`},
			notWant: []string{"notice--error"}},
		{name: "errors", path: "/p/app/plan", setup: withErrors,
			want:    []string{`<div class="notice notice--error" role="alert"><div class="notice__body">Fix 1 error before approving. Ask the planning agent in the chat, or edit the plan.</div></div>`},
			notWant: noAction},
		{name: "no session", path: "/p/app/plan",
			want: []string{"There is no planning session, so there is nothing to approve."}, notWant: noAction},
		{name: "damaged", path: "/p/app/plan",
			setup: func(rg *rig) { rg.planStatus[rg.root] = PlanStatus{Known: true, Session: true, Damaged: true} },
			want:  []string{"The planning session&#39;s state file is damaged, so the plan cannot be approved here."}, notWant: noAction},
		{name: "status unknown", path: "/p/app/plan",
			setup: func(rg *rig) { rg.planStatus[rg.root] = PlanStatus{} },
			want:  []string{"The planning status could not be read."}, notWant: noAction},
		{name: "already approved", path: "/p/app/plan",
			setup: func(rg *rig) { rg.planStatus[rg.root] = planLine("new", "approved", "Plan approved") },
			want:  []string{"This plan is already approved."}, notWant: noAction},
		{name: "not new", path: "/p/app/plan",
			setup: func(rg *rig) { rg.planStatus[rg.root] = planLine("amend", "review", "x") },
			want:  []string{"changes a plan that is already being built"}, notWant: noAction},
		{name: "not at review", path: "/p/app/plan",
			setup: func(rg *rig) { rg.planStatus[rg.root] = planLine("new", "round", "x") },
			want:  []string{"it is at round now."}, notWant: noAction},
		{name: "errors in the plan of a session not at review show the session's reason", path: "/p/app/plan",
			setup: func(rg *rig) { withErrors(rg); rg.planStatus[rg.root] = planLine("new", "round", "x") },
			want:  []string{"it is at round now."}, notWant: append([]string{"notice--error"}, noAction...)},
		{name: "no approve seam", path: "/p/app/plan",
			setup: func(rg *rig) { rg.planStatus[rg.root] = approvable(); rg.srv.d.ApprovePlan = nil },
			want:  []string{"Approving is not available on this page."}, notWant: noAction},
	}
	linkRE := regexp.MustCompile(`<a [^>]*href="([^"]*)"`)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			body := rg.pageDo("GET", tc.path, "").Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("page does not contain %q\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(noOwnScript(noSignOut(body)), w) {
					t.Errorf("page contains %q, which it should not", w)
				}
			}
			for _, m := range linkRE.FindAllStringSubmatch(body, -1) {
				href := m[1]
				if strings.HasPrefix(href, "#") || strings.HasPrefix(href, "https://example.com/") {
					continue
				}
				if !strings.HasPrefix(href, "/") {
					t.Errorf("unexpected link %q", href)
					continue
				}
				if rec := rg.pageDo("GET", href, ""); rec.Code != 200 {
					t.Errorf("the link %q answers %d", href, rec.Code)
				}
			}
		})
	}

	// The routes about a plan that take anything but GET are the approve POST,
	// the change POST (T8.14), the new-plan POST (T8.15) and its GitHub
	// answer (T8.15b), which all need the confirm (TestPlanApproveNeedsTheConfirm,
	// TestPlanChangeNeedsTheConfirm, TestPlanNewNeedsTheConfirm,
	// TestPlanNewGitHubRefusals).
	for _, rt := range routes {
		if strings.Contains(rt.path, "plan") && rt.method != http.MethodGet &&
			(rt.method != http.MethodPost || (rt.path != "/p/{project}/plan/approve" && rt.path != "/p/{project}/plan/change" && rt.path != "/plan/new" && rt.path != "/plan/new/github")) {
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
	if strings.Contains(strings.ToLower(noOwnScript(body)), "<script") {
		t.Errorf("the page of the real plan holds a script tag")
	}
}

// TestPageLayout requests every HTML page and checks it is drawn inside the
// new layout: class="afk" on the body, the hashed afk.css and page.css, no
// inline style, and (except the error page and the sign-in form) both bars.
//
// Load-bearing rows: "refused" (the sign-in form) and "error" (the two pages
// that stand alone,
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
		noCookie bool
		// form, when set, makes the row a page POST with that body: the
		// plan approve's result page is only ever the answer to one.
		form     string
		status   int
		bars     bool
		back     string
		current  string
		navTitle string
	}{
		{name: "overview", page: "overview", path: "/", status: 200, bars: true, current: `href="/" aria-current="page"`},
		{name: "usage", page: "usage", path: "/usage", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, current: `href="/usage" aria-current="page"`, navTitle: "Usage"},
		{name: "project", page: "project", path: "/p/app", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, current: `href="/" aria-current="true"`, navTitle: "app"},
		{name: "plan", page: "plan", path: "/p/app/plan", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, current: `href="/" aria-current="true"`, navTitle: "Plan · app"},
		{name: "plan waiting for approval", page: "plan", path: "/p/draft/plan", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, navTitle: "Plan · draft"},
		{name: "item", page: "item", path: "/p/app/inbox/" + idBlocking, status: 200, bars: true, back: `href="/p/app/inbox" aria-label="Back to the inbox of app"`, navTitle: "Which database?"},
		{name: "inbox", page: "inbox", path: "/p/app/inbox", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, current: `href="/" aria-current="true"`, navTitle: "Inbox"},
		{name: "tasks", page: "tasks", path: "/p/app/tasks", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, current: `href="/" aria-current="true"`, navTitle: "Tasks"},
		{name: "task", page: "task", path: "/p/app/tasks/T1.2", status: 200, bars: true, back: `href="/p/app/tasks" aria-label="Back to the tasks of app"`, current: `href="/" aria-current="true"`, navTitle: "T1.2"},
		{name: "task status, no-JS", page: "task_status", path: "/p/app/tasks/T1.2/status", status: 200, bars: true, back: `href="/p/app/tasks/T1.2" aria-label="Back to T1.2"`, current: `href="/" aria-current="true"`, navTitle: "Set status"},
		{name: "task status, no-JS, step two", page: "task_status", path: "/p/app/tasks/T1.2/status?to=done", status: 200, bars: true, back: `href="/p/app/tasks/T1.2" aria-label="Back to T1.2"`, navTitle: "Set status"},
		{name: "sessions", page: "sessions", path: "/p/app/sessions", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, current: `href="/" aria-current="true"`, navTitle: "Sessions"},
		{name: "session", page: "session", path: "/p/app/sessions/t1-2", status: 200, bars: true, back: `href="/p/app/sessions" aria-label="Back to the sessions of app"`, current: `href="/" aria-current="true"`, navTitle: "t1-2"},
		{name: "session action, no-JS", page: "session_confirm", path: "/p/app/sessions/t1-2/confirm/clean", status: 200, bars: true, back: `href="/p/app/sessions/t1-2" aria-label="Back to t1-2"`, current: `href="/" aria-current="true"`, navTitle: "Clean"},
		{name: "new session, no-JS", page: "session_confirm", path: "/p/app/sessions/new?name=fix-login&description=Fix+it", status: 200, bars: true, back: `href="/p/app/sessions" aria-label="Back to the sessions of app"`, navTitle: "New session"},
		{name: "confirm", page: "confirm", path: "/p/app/confirm/pause", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, navTitle: "Pause project"},
		{name: "plan approve, no-JS", page: "plan_confirm", path: "/p/app/plan/approve", status: 400, bars: true, back: `href="/p/app/plan" aria-label="Back to the plan of app"`, navTitle: "Approve the plan"},
		{name: "plan approve result", page: "plan_result", path: "/p/app/plan/approve", form: "confirm=yes", status: 200, bars: true, back: `href="/p/app/plan" aria-label="Back to the plan of app"`, navTitle: "Plan approved"},
		{name: "planning chat", page: "plan_chat", path: "/p/app/plan/chat", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, current: `href="/" aria-current="true"`, navTitle: "Planning app"},
		{name: "plan change, no-JS", page: "plan_change", path: "/p/app/plan/change?mode=extend", status: 200, bars: true, back: `href="/p/app" aria-label="Back to app"`, current: `href="/" aria-current="true"`, navTitle: "Change the plan"},
		{name: "plan a new project, no-JS", page: "plan_new", path: "/plan/new", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, current: `href="/" aria-current="true"`, navTitle: "Plan a new project"},
		{name: "plan a new project, confirm", page: "plan_new", path: "/plan/new?path=~/code/shop", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, navTitle: "Plan a new project"},
		{name: "add a project", page: "project_add", path: "/projects/add", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, current: `href="/" aria-current="true"`, navTitle: "Add a project"},
		{name: "remove a project", page: "project_remove", path: "/projects/remove", status: 200, bars: true, back: `href="/" aria-label="Back to all projects"`, current: `href="/" aria-current="true"`, navTitle: "Remove a project"},
		{name: "remove a project, confirm", page: "project_remove_confirm", path: "/projects/remove/confirm?name=app", status: 200, bars: true, back: `href="/projects/remove" aria-label="Back to the project list"`, navTitle: "Remove a project"},
		{name: "error", page: "error", path: "/p/nope", status: 404},
		{name: "refused", page: "signin", path: "/p/app", noCookie: true, status: 401},
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
			if tc.noCookie {
				rec = httptest.NewRecorder()
				rg.h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			} else if tc.form != "" {
				rec = rg.pageDo("POST", tc.path, tc.form)
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
			// A page without bars has no sign-out button; a page with them has
			// exactly one form per bar, posting to /signout. An unsigned
			// browser reaching a bare page must not be offered one either.
			if n := strings.Count(body, `action="/signout"`); (tc.bars && n != 2) || (!tc.bars && n != 0) {
				t.Errorf("%d sign-out forms, want %d", n, map[bool]int{true: 2, false: 0}[tc.bars])
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

// TestDesktopBackRow checks the desktop back row on every page chromeFor
// knows: it holds the phone bar's back address, spoken name and title, and is
// absent from the overview, the bare error page and the sign-in form.
//
// Load-bearing rows: "overview", "error" and "sign-in" (no row: the overview
// is the top, the error page and the sign-in form have no bars), "plan waiting for approval" (back to /, since
// the project page answers 404 for a plan not built yet) and "item" (back to
// the project's inbox). The row list is compared with pageNames, so a page
// added later fails here until it has a row.
func TestDesktopBackRow(t *testing.T) {
	tests := []struct {
		name   string
		page   string
		path   string
		form   string
		status int
		// back is empty when the page has no row.
		back, label, title string
	}{
		{name: "overview", page: "overview", path: "/", status: 200},
		{name: "usage", page: "usage", path: "/usage", status: 200, back: "/", label: "Back to all projects", title: "Usage"},
		{name: "project", page: "project", path: "/p/app", status: 200, back: "/", label: "Back to all projects", title: "app"},
		{name: "plan", page: "plan", path: "/p/app/plan", status: 200, back: "/p/app", label: "Back to app", title: "Plan · app"},
		{name: "plan waiting for approval", page: "plan", path: "/p/draft/plan", status: 200, back: "/", label: "Back to all projects", title: "Plan · draft"},
		{name: "inbox", page: "inbox", path: "/p/app/inbox", status: 200, back: "/p/app", label: "Back to app", title: "Inbox"},
		{name: "item", page: "item", path: "/p/app/inbox/" + idBlocking, status: 200, back: "/p/app/inbox", label: "Back to the inbox of app", title: "Which database?"},
		{name: "tasks", page: "tasks", path: "/p/app/tasks", status: 200, back: "/p/app", label: "Back to app", title: "Tasks"},
		{name: "task", page: "task", path: "/p/app/tasks/T1.2", status: 200, back: "/p/app/tasks", label: "Back to the tasks of app", title: "T1.2"},
		{name: "task status", page: "task_status", path: "/p/app/tasks/T1.2/status", status: 200, back: "/p/app/tasks/T1.2", label: "Back to T1.2", title: "Set status"},
		{name: "sessions", page: "sessions", path: "/p/app/sessions", status: 200, back: "/p/app", label: "Back to app", title: "Sessions"},
		{name: "session", page: "session", path: "/p/app/sessions/t1-2", status: 200, back: "/p/app/sessions", label: "Back to the sessions of app", title: "t1-2"},
		{name: "session action", page: "session_confirm", path: "/p/app/sessions/t1-2/confirm/clean", status: 200, back: "/p/app/sessions/t1-2", label: "Back to t1-2", title: "Clean"},
		{name: "new session", page: "session_confirm", path: "/p/app/sessions/new?name=fix-login&description=Fix+it", status: 200, back: "/p/app/sessions", label: "Back to the sessions of app", title: "New session"},
		{name: "confirm", page: "confirm", path: "/p/app/confirm/pause", status: 200, back: "/p/app", label: "Back to app", title: "Pause project"},
		{name: "plan approve", page: "plan_confirm", path: "/p/app/plan/approve", status: 400, back: "/p/app/plan", label: "Back to the plan of app", title: "Approve the plan"},
		{name: "plan approve result", page: "plan_result", path: "/p/app/plan/approve", form: "confirm=yes", status: 200, back: "/p/app/plan", label: "Back to the plan of app", title: "Plan approved"},
		{name: "planning chat", page: "plan_chat", path: "/p/app/plan/chat", status: 200, back: "/p/app", label: "Back to app", title: "Planning app"},
		{name: "plan change", page: "plan_change", path: "/p/app/plan/change?mode=extend", status: 200, back: "/p/app", label: "Back to app", title: "Change the plan"},
		{name: "plan a new project", page: "plan_new", path: "/plan/new", status: 200, back: "/", label: "Back to all projects", title: "Plan a new project"},
		{name: "add a project", page: "project_add", path: "/projects/add", status: 200, back: "/", label: "Back to all projects", title: "Add a project"},
		{name: "remove a project", page: "project_remove", path: "/projects/remove", status: 200, back: "/", label: "Back to all projects", title: "Remove a project"},
		{name: "remove a project confirm", page: "project_remove_confirm", path: "/projects/remove/confirm?name=app", status: 200, back: "/projects/remove", label: "Back to the project list", title: "Remove a project"},
		{name: "error", page: "error", path: "/p/nope", status: 404},
		// A wrong password redraws the sign-in form, which has no bars.
		{name: "sign-in", page: "signin", path: "/signin", form: "password=wrong-wrong", status: 401},
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
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			method := "GET"
			if tc.form != "" {
				method = "POST"
			}
			rec := rg.pageDo(method, tc.path, tc.form)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			body := rec.Body.String()
			const open = `<nav class="app-bar app-bar--back app-bar--desktop" aria-label="Back">`
			start := strings.Index(body, `<nav class="app-bar app-bar--back app-bar--desktop"`)
			if tc.back == "" {
				if start >= 0 || strings.Contains(body, "app-bar--desktop") {
					t.Errorf("the page holds a desktop back bar")
				}
				return
			}
			if start < 0 {
				t.Fatalf("the page has no back row")
			}
			row := body[start:]
			end := strings.Index(row, "</nav>")
			if end < 0 {
				t.Fatalf("the back row is not closed")
			}
			row = row[:end]
			// A labelled nav landmark, not a bare div: axe's region rule wants
			// all content inside a landmark, and the top bar's nav is "Main".
			if w := open; !strings.HasPrefix(row, w) {
				t.Errorf("the back row does not open with %q:\n%s", w, row)
			}
			if w := `href="` + tc.back + `" aria-label="` + tc.label + `"`; !strings.Contains(row, w) {
				t.Errorf("the back row lacks %q:\n%s", w, row)
			}
			if w := `<span class="app-bar__title">` + tc.title + `</span>`; !strings.Contains(row, w) {
				t.Errorf("the back row lacks %q:\n%s", w, row)
			}
			if strings.Index(body, `<header class="topnav">`) > start {
				t.Errorf("the back row is not under the top bar")
			}
			// No page but the planning chat, seeded below, has a meta line.
			if strings.Contains(row, `<span class="meta">`) {
				t.Errorf("the back bar holds a meta line it should not:\n%s", row)
			}
		})
	}
}

// TestInPageBackLinksRemoved checks that a page whose bar already holds a
// back button does not repeat it in its content, and that the links that stay
// are still there. A content link counts as a repeat when its text starts
// with "Back": the bar's own buttons are icons named by aria-label, so they
// carry no text.
//
// Load-bearing rows: "plan result" and "plan confirm, cannot be approved"
// (their only way back was the repeated link, so a template that put it back
// would still pass the older tests), "new plan started, no chat" (the row
// that held the link must not be left empty), "remove a project, empty list"
// (the page then holds only a sentence and the bar), and the kept rows
// ("plan confirm, available", "session action", "task status", "new
// session, form", "plan change, open"), which fail if a later edit removes a
// link that is the safe choice beside an action. Every row also checks the
// bar's back button holds the same address the removed link had.
func TestInPageBackLinksRemoved(t *testing.T) {
	contentBack := regexp.MustCompile(`<a [^>]*>\s*Back[^<]*</a>`)
	emptyRow := regexp.MustCompile(`<div class="btn-row">\s*</div>`)
	type result struct {
		rg  *rig
		rec *httptest.ResponseRecorder
	}
	get := func(setup func(*rig), path string) func(*testing.T) result {
		return func(t *testing.T) result {
			rg := newRig(t, Config{})
			if setup != nil {
				setup(rg)
			}
			return result{rg, rg.pageDo(http.MethodGet, path, "")}
		}
	}
	started := func(t *testing.T) result {
		rg, f := ghRig(t)
		f.outcome = GitHubOutcome{Kind: GitHubFailed, Repo: "me/demo", Text: "gh could not make it"}
		ghAsk(t, rg)
		return result{rg, rg.pageDo(http.MethodPost, "/plan/new/github", ghAnswerForm(rg.createPath, "yes"))}
	}
	tests := []struct {
		name   string
		do     func(*testing.T) result
		status int
		// back is the bar's back button, as href and aria-label.
		back string
		// kept is whether the content holds a link whose text starts with
		// "Back", which only the kept rows do.
		kept bool
		want []string
	}{
		{name: "plan", do: get(nil, "/p/app/plan"), status: 200, back: `href="/p/app" aria-label="Back to app"`},
		{name: "plan waiting for approval", do: get(nil, "/p/draft/plan"), status: 200, back: `href="/" aria-label="Back to all projects"`},
		{name: "plan result", status: 200, back: `href="/p/app/plan" aria-label="Back to the plan of app"`,
			do: func(t *testing.T) result {
				rg := newRig(t, Config{})
				return result{rg, rg.pageDo(http.MethodPost, "/p/app/plan/approve", "confirm=yes")}
			}},
		{name: "plan confirm, cannot be approved", status: 400, back: `href="/p/app/plan" aria-label="Back to the plan of app"`,
			do: get(func(rg *rig) {
				st := approvable()
				st.Phase, st.Approvable = "draft", false
				rg.planStatus[rg.root] = st
			}, "/p/app/plan/approve")},
		{name: "plan change, none", status: 200, back: `href="/p/app" aria-label="Back to app"`,
			do: get(func(rg *rig) { delete(rg.plans, rg.root) }, "/p/app/plan/change?mode=extend")},
		{name: "plan change, open", status: 200, back: `href="/p/app" aria-label="Back to app"`,
			do: get(func(rg *rig) {
				must(rg.t, plansession.WriteState(rg.root, plansession.State{Mode: "amend", Phase: "draft", UpdatedAt: "2026-10-12T11:00:00Z"}))
			}, "/p/app/plan/change?mode=extend"),
			want: []string{`<a class="btn btn--primary" href="/p/app/plan/chat">Open its chat</a>`}},
		{name: "plan change, unknown", do: get(nil, "/p/app/plan/change?mode=new"), status: 400, back: `href="/p/app" aria-label="Back to app"`},
		{name: "new plan started, with a chat", do: started, status: 200, back: `href="/" aria-label="Back to all projects"`,
			want: []string{`<a class="btn btn--primary" href="/p/demo/plan/chat">Open its chat</a>`}},
		{name: "new plan started, no chat", status: 200, back: `href="/" aria-label="Back to all projects"`,
			do: func(t *testing.T) result {
				rg := newRig(t, Config{})
				rg.addedName, rg.addedErr = "", errors.New("disk full")
				return result{rg, rg.pageDo(http.MethodPost, "/plan/new", planNewForm("~/code/shop"))}
			}},
		{name: "remove a project, empty list", do: get(func(rg *rig) { rg.projects = nil }, "/projects/remove"), status: 200,
			back: `href="/" aria-label="Back to all projects"`, want: []string{"there is nothing to remove"}},
		{name: "session action, last branch", do: func(t *testing.T) result {
			rg := actionRig(t)
			return result{rg, rg.pageDo(http.MethodGet, "/p/app/sessions/busy/confirm/restart", "")}
		}, status: 200, back: `href="/p/app/sessions/busy" aria-label="Back to busy"`, want: []string{"there is nothing to restart"}},

		{name: "plan confirm, available", status: 200, kept: false, back: `href="/p/app/plan" aria-label="Back to the plan of app"`,
			do:   get(func(rg *rig) { rg.planStatus[rg.root] = approvable() }, "/p/app/plan/approve"),
			want: []string{`href="/p/app/plan" autofocus>`, "Approve and start"}},
		{name: "session action", status: 200, back: `href="/p/app/sessions/finished" aria-label="Back to finished"`,
			do: func(t *testing.T) result {
				rg := actionRig(t)
				return result{rg, rg.pageDo(http.MethodGet, "/p/app/sessions/finished/confirm/clean", "")}
			},
			want: []string{`<a class="btn btn--secondary" href="/p/app/sessions/finished" autofocus>Keep it</a>`}},
		{name: "task status", do: get((*rig).useTasksPlan, "/p/app/tasks/T2.2/status?to=pending"), status: 200, kept: true, back: `href="/p/app/tasks/T2.2" aria-label="Back to T2.2"`,
			want: []string{`<a class="btn btn--secondary" href="/p/app/tasks/T2.2/status">Back</a>`}},
		{name: "new session, form", status: 200, kept: true, back: `href="/p/app/sessions" aria-label="Back to the sessions of app"`,
			do: func(t *testing.T) result {
				rg := actionRig(t)
				return result{rg, rg.pageDo(http.MethodGet, "/p/app/sessions/new", "")}
			},
			want: []string{`<a class="btn btn--secondary" href="/p/app/sessions">Back</a>`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.do(t)
			if res.rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", res.rec.Code, tc.status, res.rec.Body.String())
			}
			body := res.rec.Body.String()
			for _, bar := range []string{`<nav class="app-bar app-bar--back app-bar--desktop"`, `class="app-bar`} {
				if !strings.Contains(body, bar) {
					t.Errorf("the page lacks the bar %q", bar)
				}
			}
			if !strings.Contains(body, `<a class="icon-btn" `+tc.back+`>`) {
				t.Errorf("the bar lacks the back button %q", tc.back)
			}
			if got := contentBack.FindString(body); (got != "") != tc.kept {
				t.Errorf("content back link = %q, want one: %v", got, tc.kept)
			}
			if emptyRow.MatchString(body) {
				t.Errorf("the page holds an empty button row")
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("the page lacks %q", w)
				}
			}
		})
	}
}

// TestDesktopBarMeta checks the planning chat's desktop bar carries its mode
// and phase line as meta, the one page that has one. The row is load-bearing:
// the bar sits outside the live regions, so the line is drawn on the render.
func TestDesktopBarMeta(t *testing.T) {
	rg := newRig(t, Config{})
	rg.planStatus[rg.root] = PlanStatus{Known: true, Session: true, Mode: plansession.ModeNew, Phase: plansession.PhaseRounds, Problems: []string{}}
	body := rg.pageDo(http.MethodGet, "/p/app/plan/chat", "").Body.String()
	start := strings.Index(body, `<nav class="app-bar app-bar--back app-bar--desktop"`)
	if start < 0 {
		t.Fatalf("the chat page has no desktop bar")
	}
	row := body[start:]
	row = row[:strings.Index(row, "</nav>")]
	if w := `<span class="meta">New plan · Rounds</span>`; !strings.Contains(row, w) {
		t.Errorf("the desktop bar lacks %q:\n%s", w, row)
	}
}

// TestDesktopBarStyles checks the style sheets: page.css hides the desktop
// bar at 760px and below and the phone bar (not the desktop one) above it,
// afk.css holds the design's desktop rules, and no .back-row is left. The
// min-width row is load-bearing: a plain .app-bar rule there hides the new
// bar on the desktop, where it is the only way back.
func TestDesktopBarStyles(t *testing.T) {
	read := func(name string) string {
		b, err := staticFiles.ReadFile("static/" + name)
		if err != nil {
			t.Fatalf("the binary holds no %s: %v", name, err)
		}
		return string(b)
	}
	page, afk := read("page.css"), read("afk.css")
	tests := []struct {
		name string
		css  string
		re   string
	}{
		{name: "hidden on the phone", css: page, re: `@media \(max-width: 760px\)\s*\{\s*\.app-bar--desktop\s*\{\s*display:\s*none;`},
		{name: "phone bar hidden on the desktop only", css: page, re: `@media \(min-width: 761px\)\s*\{\s*\.app-bar:not\(\.app-bar--desktop\)\s*\{\s*display:\s*none;`},
		{name: "static and 52px", css: afk, re: `\.app-bar--desktop\s*\{\s*position:\s*static;\s*min-height:\s*52px;`},
		{name: "title does not grow", css: afk, re: `\.app-bar--desktop \.app-bar__title\s*\{\s*flex:\s*0 1 auto;`},
		{name: "meta margin", css: afk, re: `\.app-bar--desktop \.meta\s*\{\s*margin-left:\s*var\(--space-2\);`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !regexp.MustCompile(tc.re).MatchString(tc.css) {
				t.Errorf("the style sheet does not match %s", tc.re)
			}
		})
	}
	for name, css := range map[string]string{"page.css": page, "afk.css": afk} {
		if strings.Contains(css, "back-row") {
			t.Errorf("%s still mentions back-row", name)
		}
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
			want: chrome{Title: "Plan · my app", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app", BackLabel: "Back to my app"}},
		{name: "plan waiting", page: "plan", view: PlanView{Project: "draft", WaitingForApproval: true}, method: "GET", path: "/p/draft/plan",
			want: chrome{Title: "Plan · draft", Nav: "projects", Current: "true", Projects: 3, Back: "/", BackLabel: "Back to all projects"}},
		{name: "item", page: "item", view: ItemView{Project: "my app", Item: inbox.Item{Title: "Which?"}}, method: "GET", path: "/p/my app/inbox/x",
			want: chrome{Title: "Which?", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app/inbox", BackLabel: "Back to the inbox of my app"}},
		{name: "inbox", page: "inbox", view: InboxView{Project: "my app"}, method: "GET", path: "/p/my app/inbox",
			want: chrome{Title: "Inbox", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app", BackLabel: "Back to my app"}},
		{name: "tasks", page: "tasks", view: TasksView{Project: "my app"}, method: "GET", path: "/p/my app/tasks",
			want: chrome{Title: "Tasks", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app", BackLabel: "Back to my app"}},
		{name: "task", page: "task", view: TaskView{Project: "my app", ID: "T1.2", Title: "Second"}, method: "GET", path: "/p/my app/tasks/T1.2",
			want: chrome{Title: "T1.2", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app/tasks", BackLabel: "Back to the tasks of my app"}},
		{name: "task status", page: "task_status", view: TaskStatusPageView{Project: "my app", ID: "T1.2"}, method: "GET", path: "/p/my app/tasks/T1.2/status",
			want: chrome{Title: "Set status", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app/tasks/T1.2", BackLabel: "Back to T1.2"}},
		{name: "sessions", page: "sessions", view: SessionsView{Project: "my app"}, method: "GET", path: "/p/my app/sessions",
			want: chrome{Title: "Sessions", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app", BackLabel: "Back to my app"}},
		{name: "session", page: "session", view: SessionScreenView{Project: "my app", SessionFacts: SessionFacts{Name: "t1-2"}}, method: "GET", path: "/p/my app/sessions/t1-2",
			want: chrome{Title: "t1-2", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app/sessions", BackLabel: "Back to the sessions of my app"}},
		{name: "session action", page: "session_confirm", view: SessionConfirmView{Project: "my app", Name: "t1-2", Back: "/p/my%20app/sessions/t1-2", Sheet: sessionSheet{Action: actionRestart}}, method: "GET", path: "/p/my app/sessions/t1-2/confirm/restart",
			want: chrome{Title: "Restart", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app/sessions/t1-2", BackLabel: "Back to t1-2"}},
		{name: "new session, form step", page: "session_confirm", view: SessionConfirmView{Project: "my app", Back: "/p/my%20app/sessions"}, method: "GET", path: "/p/my app/sessions/new",
			want: chrome{Title: "New session", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app/sessions", BackLabel: "Back to the sessions of my app"}},
		{name: "planning chat", page: "plan_chat", view: ChatView{Chat: planchat.Chat{Project: "my app"}, Page: chatPage{Sub: "New plan · Rounds"}}, method: "GET", path: "/p/my app/plan/chat",
			want: chrome{Title: "Planning my app", Meta: "New plan · Rounds", Nav: "projects", Current: "true", Projects: 3, Back: "/p/my%20app", BackLabel: "Back to my app"}},
		{name: "plan a new project", page: "plan_new", view: NewPlanPageView{Kind: "form"}, method: "GET", path: "/plan/new",
			want: chrome{Title: "Plan a new project", Nav: "projects", Current: "true", Projects: 3, Back: "/", BackLabel: "Back to all projects"}},
		{name: "add a project", page: "project_add", view: ProjectAddPageView{}, method: "GET", path: "/projects/add",
			want: chrome{Title: "Add a project", Nav: "projects", Current: "true", Projects: 3, Back: "/", BackLabel: "Back to all projects"}},
		{name: "remove a project", page: "project_remove", view: ProjectRemovePageView{}, method: "GET", path: "/projects/remove",
			want: chrome{Title: "Remove a project", Nav: "projects", Current: "true", Projects: 3, Back: "/", BackLabel: "Back to all projects"}},
		{name: "remove a project, confirm", page: "project_remove_confirm", view: ProjectRemoveConfirmView{Name: "app"}, method: "GET", path: "/projects/remove/confirm",
			want: chrome{Title: "Remove a project", Nav: "projects", Current: "true", Projects: 3, Back: "/projects/remove", BackLabel: "Back to the project list"}},
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
		noCookie   bool
		setup      func(rg *rig)
		wantStatus int
		wantIn     []string
		wantNot    []string
	}{
		{name: "pause", path: "/p/app/confirm/pause", wantStatus: 200,
			wantIn: []string{"Pause app?", "The running task finishes; nothing new starts. Nothing is deleted.", `action="/p/app/pause"`, `<input type="hidden" name="confirm" value="yes">`, `href="/p/app"`, "Keep it running"}},
		{name: "resume", path: "/p/app/confirm/resume", wantStatus: 200,
			wantIn: []string{"Resume app?", "afk starts the next ready task again; a stopped line is cleared. Nothing is deleted.", `action="/p/app/resume"`, `<input type="hidden" name="confirm" value="yes">`, `href="/p/app"`}},
		{name: "project with a space", path: "/p/my%20app/confirm/pause", wantStatus: 200,
			wantIn: []string{"Pause my app?", `action="/p/my%20app/pause"`}},
		{name: "delete", path: "/p/app/confirm/delete", wantStatus: 404, wantIn: []string{"That didn't load"}, wantNot: []string{"Delete"}},
		{name: "unknown project", path: "/p/nope/confirm/pause", wantStatus: 404, wantIn: []string{"That didn't load"}},
		{name: "plan waiting has no confirm", path: "/p/draft/confirm/pause", wantStatus: 404},
		{name: "api twin does not exist", path: "/api/p/app/confirm/pause", wantStatus: 404, wantNot: []string{"Pause app?"}},
		{name: "no cookie", path: "/p/app/confirm/pause", noCookie: true, wantStatus: 401, wantNot: []string{"Pause app?", `action="/p/app`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			var rec *httptest.ResponseRecorder
			if tc.noCookie {
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

// TestProjectPageConfirm checks the project page holds the link for the state
// it is in and both dialogs, and that the plan page holds neither (it must
// stay free of forms and buttons).
//
// Load-bearing: both sheets on either page (the link lives in the pause live
// region and swaps from Pause to Resume without a reload, so the sheet it
// then names must already be there) and the check that every dialog and form
// comes after the last live region (live.js skips a region holding a form,
// and a swapped region would take an open dialog with it).
func TestProjectPageConfirm(t *testing.T) {
	bothSheets := []string{
		`<dialog class="confirm-sheet" id="confirm-pause"`, "Pause app?", `action="/p/app/pause" data-enhance>`,
		`<dialog class="confirm-sheet" id="confirm-resume"`, "Resume app?", `action="/p/app/resume" data-enhance>`,
		`<input type="hidden" name="confirm" value="yes">`,
	}
	tests := []struct {
		name    string
		path    string
		paused  bool
		wantIn  []string
		wantNot []string
	}{
		{name: "running", path: "/p/app", wantIn: append([]string{`data-confirm="confirm-pause"`, `href="/p/app/confirm/pause"`, "Pause project&hellip;", "autofocus", `data-loading-label="Pausing"`}, bothSheets...), wantNot: []string{`data-confirm="confirm-resume"`, "Resume project&hellip;"}},
		{name: "paused", path: "/p/app", paused: true, wantIn: append([]string{`data-confirm="confirm-resume">Resume project&hellip;`, `href="/p/app/confirm/resume"`}, bothSheets...), wantNot: []string{`data-confirm="confirm-pause"`, "Pause project&hellip;"}},
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
				if strings.Contains(noSignOut(body), s) {
					t.Errorf("must not contain %q", s)
				}
			}
			body = noSignOut(body)
			if last := strings.LastIndex(body, "data-live="); last >= 0 {
				for _, tag := range []string{"<dialog", "<form", "<input", "<textarea"} {
					if i := strings.Index(body, tag); i >= 0 && i < last {
						t.Errorf("%s comes before the last live region", tag)
					}
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
		{name: "chat sent", path: "/p/app/plan/chat?done=sent", wantIn: `role="status" data-toast>Sent to the planning agent.</div>`},
		{name: "chat code on the project", path: "/p/app?done=sent", wantNot: []string{"data-toast"}},
		{name: "project code on the chat", path: "/p/app/plan/chat?done=paused", wantNot: []string{"data-toast"}},
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
	tests := []struct{ code, project, task, want string }{
		{"paused", "app", "", "Paused app. Nothing new will start."},
		{"resumed", "app", "", "Resumed app."},
		{"answered", "app", "", "Answer sent."},
		{"ready", "app", "T1.2", "Answer sent. T1.2 is ready again."},
		{"ready", "app", "", ""}, // no task to name: no toast, never "{task}"
		{"done", "app", "T1.2", "Marked as done."},
		{"", "app", "", ""},
		{"<script>", "app", "", ""},
		{"PAUSED", "app", "", ""},
		// The status codes name a status only a task's screen can read, so
		// any other page shows nothing rather than "{status}".
		{"status", "app", "T1.2", ""},
		{"already", "app", "T1.2", ""},
		// The chat's code belongs to the chat screen (chatToast).
		{"sent", "app", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.code+"_"+tc.task, func(t *testing.T) {
			if got := toastFor(tc.code, tc.project, tc.task); got != tc.want {
				t.Errorf("toastFor(%q) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

// TestConfirmScriptServed checks each page script the layout loads is served
// at its hashed address with the long cache and the security headers, and
// has no eval or innerHTML: the policy refuses eval, and innerHTML is how
// text from the server (an inbox answer, a plan title) would become markup.
//
// Load-bearing rows: "submit.js" (it parses whole fetched pages, so it is the
// script most tempted by innerHTML; document.importNode is the allowed way)
// and "confirm.js"; "live.js" keeps the rule for the third script too.
func TestConfirmScriptServed(t *testing.T) {
	rg := newRig(t, Config{})
	body := rg.pageDo("GET", "/p/app", "").Body.String()
	for _, name := range []string{"confirm.js", "submit.js", "live.js"} {
		t.Run(name, func(t *testing.T) {
			m := regexp.MustCompile(`/static/[0-9a-f]{12}/` + regexp.QuoteMeta(name)).FindString(body)
			if m == "" {
				t.Fatalf("the project page does not load %s", name)
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
				t.Errorf("%s uses eval or innerHTML", name)
			}
		})
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
		h, err := New(Config{}, Deps{
			Password:  testPasswordSeam,
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
		rec := authReq{target: "/", cookie: goodCookie()}.send(h)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "No projects yet") {
			t.Fatalf("status %d, want 200 with the empty state\n%s", rec.Code, rec.Body.String())
		}
		m := decode(t, authReq{target: "/api/", cookie: goodCookie()}.send(h))
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

// TestReadyAgain is the pure decision behind the "ready again" toast.
// Load-bearing rows: "another open item in parked_on" and "another open
// blocking item for the task" (the task is still waiting, so saying it is
// ready would be untrue), and "task running not parked" (nothing to un-park).
func TestReadyAgain(t *testing.T) {
	answered := inbox.Item{ID: "aaa111", Task: "T1.2", Blocking: true}
	parked := func(on ...string) *ledger.Entry {
		return &ledger.Entry{ID: "T1.2", Status: ledger.Parked, ParkedOn: on}
	}
	tests := []struct {
		name     string
		answered inbox.Item
		entry    *ledger.Entry
		items    []inbox.Item
		want     bool
	}{
		{name: "parked and alone", answered: answered, entry: parked("aaa111"), items: []inbox.Item{{ID: "aaa111", Task: "T1.2", Answer: "x"}}, want: true},
		{name: "answered item still listed as open is skipped", answered: answered, entry: parked("aaa111"), items: []inbox.Item{answered}, want: true},
		{name: "another open item in parked_on", answered: answered, entry: parked("aaa111", "bbb222"), items: []inbox.Item{{ID: "bbb222", Task: "T9"}}, want: false},
		{name: "another answered item in parked_on", answered: answered, entry: parked("aaa111", "bbb222"), items: []inbox.Item{{ID: "bbb222", Answer: "y"}}, want: true},
		{name: "another open blocking item for the task", answered: answered, entry: parked("aaa111"), items: []inbox.Item{{ID: "ccc333", Task: "T1.2", Blocking: true}}, want: false},
		{name: "another open non-blocking item for the task", answered: answered, entry: parked("aaa111"), items: []inbox.Item{{ID: "ccc333", Task: "T1.2"}}, want: true},
		{name: "open blocking item for another task", answered: answered, entry: parked("aaa111"), items: []inbox.Item{{ID: "ddd444", Task: "T3", Blocking: true}}, want: true},
		{name: "task running not parked", answered: answered, entry: &ledger.Entry{ID: "T1.2", Status: ledger.Running}, want: false},
		{name: "no task", answered: inbox.Item{ID: "aaa111"}, entry: parked(), want: false},
		{name: "no ledger entry", answered: answered, entry: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := readyAgain(tc.answered, tc.entry, tc.items); got != tc.want {
				t.Errorf("readyAgain = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIsOwnerTask is the owner-task test. The load-bearing row is the
// ordinary external action: only the daemon's "Your task: " title makes an
// answer a "done".
func TestIsOwnerTask(t *testing.T) {
	tests := []struct {
		name string
		it   inbox.Item
		want bool
	}{
		{"owner task", inbox.Item{Kind: "external_action", Blocking: true, Task: "T1.2", Title: daemon.OwnerTitlePrefix + "T1.2"}, true},
		{"ordinary external action", inbox.Item{Kind: "external_action", Blocking: true, Task: "T1.2", Title: "Create the bucket"}, false},
		{"not blocking", inbox.Item{Kind: "external_action", Task: "T1.2", Title: daemon.OwnerTitlePrefix + "T1.2"}, false},
		{"no task", inbox.Item{Kind: "external_action", Blocking: true, Title: daemon.OwnerTitlePrefix + "T1.2"}, false},
		{"a question", inbox.Item{Kind: "question", Blocking: true, Task: "T1.2", Title: daemon.OwnerTitlePrefix + "T1.2"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOwnerTask(tc.it); got != tc.want {
				t.Errorf("isOwnerTask = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAnswerCodes drives the answer redirect through the handler. Load-
// bearing rows: "ready" (a parked task whose only item is answered) and "not
// ready" (a second blocking item keeps it waiting), and "owner task".
func TestAnswerCodes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(rg *rig)
		id    string
		want  string
	}{
		{name: "no ledger entry", id: idBlocking, want: "answered"},
		{name: "ready", id: idBlocking, setup: func(rg *rig) {
			rg.ledger = ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.2", Status: ledger.Parked, ParkedOn: []string{idBlocking}}}}
		}, want: "ready"},
		{name: "not ready: another open blocking item", id: idBlocking, setup: func(rg *rig) {
			rg.ledger = ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.2", Status: ledger.Parked, ParkedOn: []string{idBlocking}}}}
			st := inbox.Store{Dir: inbox.Dir(rg.root), Now: func() time.Time { return testNow }, NewID: func() string { return "pqr456" }}
			if _, err := st.Add(inbox.NewItem{Task: "T1.2", Kind: "question", Blocking: true, Title: "Second"}); err != nil {
				rg.t.Fatal(err)
			}
		}, want: "answered"},
		{name: "load error", id: idBlocking, setup: func(rg *rig) {
			rg.ledger = ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.2", Status: ledger.Parked}}}
			rg.loadErr = errors.New("broken")
		}, want: "answered"},
		{name: "owner task", id: idOwner, setup: func(rg *rig) {
			st := inbox.Store{Dir: inbox.Dir(rg.root), Now: func() time.Time { return testNow }, NewID: func() string { return idOwner }}
			if _, err := st.Add(inbox.NewItem{Task: "T1.2", Kind: "external_action", Blocking: true, Title: daemon.OwnerTitlePrefix + "T1.2"}); err != nil {
				rg.t.Fatal(err)
			}
		}, want: "done"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.pageDo("POST", "/p/app/inbox/"+tc.id+"/answer", "answer=done")
			want := "/p/app/inbox?done=" + tc.want + "&item=" + tc.id
			if rec.Code != 303 || rec.Header().Get("Location") != want {
				t.Fatalf("status %d, Location %q; want 303 to %q", rec.Code, rec.Header().Get("Location"), want)
			}
		})
	}
}

// TestInboxToast covers the toast on the inbox list. Load-bearing rows: the
// two refusals. An unknown item and an unknown code show nothing, and
// nothing from the request is printed in the page.
func TestInboxToast(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    string
		wantNot []string
	}{
		{name: "answered", query: "?done=answered&item=" + idBlocking, want: "Answer sent."},
		{name: "ready names the task from the item", query: "?done=ready&item=" + idBlocking, want: "Answer sent. T1.2 is ready again."},
		{name: "done", query: "?done=done&item=" + idOptions, want: "Marked as done."},
		{name: "ready for an item without a task", query: "?done=ready&item=" + idOptions, wantNot: []string{"data-toast"}},
		{name: "unknown item", query: "?done=answered&item=zzz999", wantNot: []string{"data-toast", "zzz999"}},
		{name: "malformed item", query: "?done=answered&item=%3Cb%3Ex", wantNot: []string{"data-toast", "<b>x"}},
		{name: "unknown code", query: "?done=%3Cscript%3Ealert(1)&item=" + idBlocking, wantNot: []string{"data-toast", "alert(1)"}},
		{name: "no item", query: "?done=answered", wantNot: []string{"data-toast"}},
		{name: "no query", query: "", wantNot: []string{"data-toast"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			rec := rg.pageDo("GET", "/p/app/inbox"+tc.query, "")
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			if tc.want != "" && !strings.Contains(body, `<div class="toast" role="status" data-toast>`+tc.want+`</div>`) {
				t.Errorf("no toast %q in\n%s", tc.want, body)
			}
			for _, w := range tc.wantNot {
				if strings.Contains(body, w) {
					t.Errorf("page contains %q", w)
				}
			}
		})
	}
}

// TestInboxJSONUnchanged keeps the API's inbox answer free of the page's
// layout: the keys are the same three as before.
func TestInboxJSONUnchanged(t *testing.T) {
	rg := newRig(t, Config{})
	rg.seedInbox()
	rec := rg.do("GET", "/api/p/app/inbox", "", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got["items"] == nil || got["unreadable"] == nil || got["problem"] == nil {
		t.Errorf("keys = %v, want items, unreadable, problem", got)
	}
}

// TestItemPageFor covers the option order and the owner-task switch.
// Load-bearing rows: "recommended moves first" (the page lists it first and
// marks it) and "recommended not among the options" (no button is marked
// that does not exist).
func TestItemPageFor(t *testing.T) {
	tests := []struct {
		name      string
		it        inbox.Item
		wantOpts  []itemOption
		wantOwner bool
	}{
		{name: "no options", it: inbox.Item{Kind: "question"}},
		{name: "recommended moves first", it: inbox.Item{Kind: "question", Options: []string{"a", "b", "c"}, Recommended: "c"},
			wantOpts: []itemOption{{"c", true}, {"a", false}, {"b", false}}},
		{name: "no recommended", it: inbox.Item{Kind: "question", Options: []string{"a", "b"}},
			wantOpts: []itemOption{{"a", false}, {"b", false}}},
		{name: "recommended not among the options", it: inbox.Item{Kind: "question", Options: []string{"a"}, Recommended: "z"},
			wantOpts: []itemOption{{"a", false}}},
		{name: "owner task has no options", it: inbox.Item{Kind: "external_action", Blocking: true, Task: "T1.2", Title: daemon.OwnerTitlePrefix + "T1.2", Options: []string{"a"}}, wantOwner: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := itemPageFor(tc.it, testNow)
			if got.OwnerTask != tc.wantOwner || !reflect.DeepEqual(got.Options, tc.wantOpts) {
				t.Errorf("itemPageFor = %+v, want owner %v options %v", got, tc.wantOwner, tc.wantOpts)
			}
		})
	}
}

// TestProjectPageNilDeps builds the page with no ProjectRows and no
// Repository seam, as an older or a partial wiring would. Load-bearing: the
// screen must still draw, with the state in projectrows' words built from the
// summary and "None set" for the repository, never a 500 or a blank.
func TestProjectPageNilDeps(t *testing.T) {
	root := t.TempDir()
	must(t, daemonstate.Pause(root, testNow))
	h, err := New(Config{}, Deps{
		Password: testPasswordSeam,
		Projects: func() []Project { return []Project{{Name: "app", Root: root}} },
		LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) {
			return planTasks("T1.1", "T1.2"), ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Done}}}, nil
		},
		Sessions: func(string) []SessionView { return nil },
		Usage:    func() (usagestate.File, bool) { return usagestate.File{}, false },
		Pause:    func(string) error { return nil },
		Resume:   func(string) error { return nil },
		Now:      func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest("GET", "/p/app", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`badge--paused project__card-badge">Paused</span>`, "<h2>Paused by you</h2>",
		"Paused: no new task will start until you resume it.", "1 of 2 tasks done",
		`data-confirm="confirm-resume">Resume</a>`, "<dt>Repository</dt><dd>None set</dd>",
		`<span class="row__title">Read the plan</span>`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q\n%s", w, body)
		}
	}
}

// TestUsagePage draws /usage in each state the rebuilt screen has and checks
// the design-system markup: the decision badge, one meter per window with
// its aria values, the Refresh usage form and its three looks, the failure
// notice, the empty state and the details.
//
// Load-bearing rows:
//   - "a warn and a full window": the colour class is not the only signal,
//     but a window at 85% that still looked blue (or one at 100% that did not
//     turn red) would hide the very thing the page exists to show; the
//     bar's width class and aria-valuenow must agree and be clamped.
//   - "refreshing": is-loading, aria-busy and data-working together, since
//     the bar's Working word comes from data-working, and the button must
//     not offer "Refresh usage" while one runs.
//   - "failed reading keeps the old one": the notice and the old meters must
//     show together, because a failure writes no file.
//   - "no seam": a daemon that cannot refresh draws no button at all.
func TestUsagePage(t *testing.T) {
	reset := testNow.Add(time.Hour)
	tests := []struct {
		name    string
		setup   func(rg *rig)
		want    []string
		notWant []string
	}{
		{name: "a reading", want: []string{
			`<span class="badge badge--running">Run</span>`, "plenty left", "What afk is doing", "Usage windows",
			`<div class="meter">`, `<span class="meter__name">Session</span><span class="meter__value">42% used</span>`,
			`role="progressbar" aria-label="Session usage" aria-valuemin="0" aria-valuemax="100" aria-valuenow="42"><span class="meter__fill w-42"></span>`,
			`<span class="meter__name">Weekly (all)</span>`, `aria-valuenow="12"`, "Resets 12 Oct 2026 14:00 UTC", "Reset time not known", "Extra usage: on",
			`id="usage-reading" data-live="usage" data-project=""`, `id="usage-refresh" data-live="usage" data-project="" data-live-buttons>`,
			`<form method="post" action="/usage/refresh" data-enhance`, `id="usage-refresh-button" class="btn btn--secondary">Refresh usage</button>`,
			"<summary>What the five decisions mean</summary>", "Launch nothing new", "<strong>Hold</strong>", "<strong>Refuse</strong>", "[governor]",
		}, notWant: []string{"Refreshing", "notice--error", `style=`, "<script>"}},
		{name: "a warn and a full window", setup: func(rg *rig) {
			rg.usage.Windows = []usagestate.Window{{Kind: "session", UsedPct: 85}, {Kind: "weekly", UsedPct: 100}, {Kind: "extra", UsedPct: 140.4}}
		}, want: []string{
			`<div class="meter meter--warn">`, `<div class="meter meter--full">`, `<span class="meter__value">85% used</span>`, `aria-valuenow="85"`,
			`<span class="meter__value">100% used</span>`, `aria-valuenow="100"`, `class="meter__fill w-100"`, `<span class="meter__value">140% used</span>`,
		}},
		{name: "every decision has a badge", setup: func(rg *rig) {
			rg.usage.Governor = &usagestate.Governor{Action: "no_launch", Reason: "Nearly full.", At: testNow, WakeAt: &reset}
		},
			want: []string{`<span class="badge badge--held">Launch nothing new</span>`, "Nearly full. It wakes 12 Oct 2026 13:00 UTC."}},
		{name: "an unknown decision", setup: func(rg *rig) { rg.usage.Governor.Action = "sleep" },
			want: []string{`<span class="badge badge--plain">sleep</span>`}},
		{name: "no decision", setup: func(rg *rig) { rg.usage.Governor = nil },
			want: []string{"No decision is recorded yet."}, notWant: []string{`class="badge `}},
		{name: "no reading", setup: func(rg *rig) { rg.usageOK = false; rg.usage = usagestate.File{} },
			want:    []string{"No usage reading exists yet", "No decision is recorded yet.", `id="usage-reading" data-live="usage"`, "Refresh usage"},
			notWant: []string{`class="meter`, "% used", "Extra usage:"}},
		{name: "refreshing", setup: func(rg *rig) {
			rg.blockRefresh()
			rg.do(http.MethodPost, "/api/usage/refresh", "", "")
		}, want: []string{
			`data-live-buttons data-working="true">`, `class="btn btn--secondary is-loading" aria-busy="true"><span class="spinner" aria-hidden="true"></span>Refreshing</button>`,
		}, notWant: []string{">Refresh usage<", "Checked just now"}},
		{name: "just refreshed", setup: func(rg *rig) {
			rg.do(http.MethodPost, "/api/usage/refresh", "", "")
			rg.waitRefresh()
		}, want: []string{">Checked just now</button>"}, notWant: []string{"is-loading", "notice--error", "data-working"}},
		{name: "failed reading keeps the old one", setup: func(rg *rig) {
			rg.refreshResult = UsageRefreshResult{Outcome: UsageRefreshFailed, Text: "The agent did not answer."}
			rg.do(http.MethodPost, "/api/usage/refresh", "", "")
			rg.waitRefresh()
		}, want: []string{
			`<div class="notice notice--error" role="alert"><div class="notice__body">The agent did not answer. The reading above is the last one that worked.`,
			">Refresh usage</button>", "42% used", `aria-valuenow="42"`,
		}, notWant: []string{"Checked just now", "Refreshing"}},
		{name: "unsupported reading", setup: func(rg *rig) {
			rg.refreshResult = UsageRefreshResult{Outcome: UsageRefreshUnsupported, Text: "This agent cannot report usage."}
			rg.do(http.MethodPost, "/api/usage/refresh", "", "")
			rg.waitRefresh()
		}, want: []string{"notice--error", "This agent cannot report usage."}},
		{name: "no seam", setup: func(rg *rig) { rg.srv.d.RefreshUsage = nil },
			want: []string{"Usage windows", "What the five decisions mean"}, notWant: []string{"usage-refresh", "Refresh usage", "/usage/refresh"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tt.setup != nil {
				tt.setup(rg)
			}
			rec := rg.pageDo("GET", "/usage", "")
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q\n%s", w, body)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(body, w) {
					t.Errorf("unexpected %q", w)
				}
			}
		})
	}
}

// TestSignOutButtons checks where the sign-out form sits: once inside each
// bar (phone and top), a plain post with no data-enhance (submit.js would
// otherwise take it over, and signing out must work the same without
// JavaScript), and nowhere on the sign-in page.
//
// Load-bearing rows: "phone bar" and "top bar" (one form in each, so a
// template edit that drops or doubles one fails), "no data-enhance" (the
// native post), and "sign-in page" (the form must not be offered to someone
// who is not signed in).
func TestSignOutButtons(t *testing.T) {
	rg := newRig(t, Config{})
	rec := rg.pageDo("GET", "/", "")
	body := rec.Body.String()
	phone := body[strings.Index(body, `<header class="app-bar`):strings.Index(body, `<header class="topnav">`)]
	top := body[strings.Index(body, `<header class="topnav">`):]
	top = top[:strings.Index(top, "</header>")]
	form := regexp.MustCompile(`<form[^>]*action="/signout"[^>]*>`)
	for _, tc := range []struct {
		name string
		part string
		want string
	}{
		{"phone bar", phone, `class="btn btn--quiet" type="submit">Sign out`},
		{"top bar", top, `class="btn btn--quiet btn--sm" type="submit">Sign out`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forms := form.FindAllString(tc.part, -1)
			if len(forms) != 1 || !strings.Contains(forms[0], `method="post"`) {
				t.Fatalf("sign-out forms = %q, want one post form", forms)
			}
			if !strings.Contains(tc.part, tc.want) {
				t.Errorf("missing button %q", tc.want)
			}
		})
	}
	t.Run("no data-enhance", func(t *testing.T) {
		for _, f := range form.FindAllString(body, -1) {
			if strings.Contains(f, "data-enhance") {
				t.Errorf("sign-out form is enhanced: %s", f)
			}
		}
	})
	t.Run("sign-in page", func(t *testing.T) {
		out := httptest.NewRecorder()
		rg.h.ServeHTTP(out, httptest.NewRequest("GET", "/signin", nil))
		if strings.Contains(out.Body.String(), "/signout") {
			t.Errorf("the sign-in page offers sign-out")
		}
	})
}

// noSignOut drops the bars' sign-out forms from a page. The older page tests
// assert that a page holds no form (or no post form) of its own, and the bars
// now carry two sign-out forms on every page that has bars; TestSignOutButtons
// is where those are checked.
func noSignOut(body string) string {
	return signOutForm.ReplaceAllString(body, "")
}

var signOutForm = regexp.MustCompile(`(?s)<form[^>]*action="/signout"[^>]*>.*?</form>`)
