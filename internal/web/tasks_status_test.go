package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/taskstatus"
	"github.com/dphaener/afk/internal/usagestate"
)

// noSeamHandler is a server with every required seam but no SetTaskStatus,
// over the rig's tasks plan: what a daemon that cannot set a status serves.
func noSeamHandler(t *testing.T, rg *rig) http.Handler {
	t.Helper()
	h, err := New(Config{Token: testToken}, Deps{
		Projects:  func() []Project { return rg.projects },
		LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return rg.tasks, rg.ledger, nil },
		Sessions:  func(string) []SessionView { return nil },
		Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
		Pause:     func(string) error { return nil },
		Resume:    func(string) error { return nil },
		Now:       func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// statusSheetHTML is the Set status… dialog cut out of a page.
func statusSheetHTML(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<dialog class="confirm-sheet" id="confirm-status"`)
	if start < 0 {
		t.Fatalf("no status sheet in\n%s", body)
	}
	end := strings.Index(body[start:], "</dialog>")
	return body[start : start+end]
}

// TestStatusLines is the pure sentence for one choice. Load-bearing rows:
// "running to pending" (the agent-working warning comes first, or the owner
// may think the change stops the agent), "current status" (says already, as
// the dashboard's picker does) and "superseded again" (not already: its ids
// may be what changes).
func TestStatusLines(t *testing.T) {
	tests := []struct {
		name, old, to string
		ids           []string
		want          []string
	}{
		{name: "failed to pending", old: "failed", to: "pending", want: []string{"T1.2: failed → pending.", "The build loop may start it on its next pass."}},
		{name: "running to pending", old: "running", to: "pending", want: []string{
			"An agent is working on T1.2 now; changing its status does not stop it.", "T1.2: running → pending.", "The build loop may start it on its next pass."}},
		{name: "shipping to done", old: "shipping", to: "done", want: []string{
			"An agent is working on T1.2 now; changing its status does not stop it.", "T1.2: shipping → done.", "Tasks that depend on it may start."}},
		{name: "current status", old: "pending", to: "pending", want: []string{"T1.2 is already pending."}},
		{name: "running is already running", old: "running", to: "running", want: []string{"T1.2 is already running."}},
		{name: "superseded with ids", old: "pending", to: "superseded", ids: []string{"T1.2a", "T1.2b"}, want: []string{
			"T1.2: pending → superseded (replaced by T1.2a, T1.2b).", "Tasks that depend on it will wait for the tasks that replace it."}},
		{name: "superseded again", old: "superseded", to: "superseded", ids: []string{"T9"}, want: []string{
			"T1.2: superseded → superseded (replaced by T9).", "Tasks that depend on it will wait for the tasks that replace it."}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusLines("T1.2", tc.old, tc.to, tc.ids); !slices.Equal(got, tc.want) {
				t.Errorf("statusLines = %q\nwant          %q", got, tc.want)
			}
		})
	}
}

// TestStatusSheetAndPageAgree is the acceptance rule that the sheet and the
// no-JS page show the same sentence for the same choice, for every status
// from every status. Superseded is checked with ids filled in the way
// confirm.js fills them (the placeholder replaced by the ids joined with
// ", "), which is the load-bearing case: the script must produce exactly the
// words the server would.
func TestStatusSheetAndPageAgree(t *testing.T) {
	task := masterplan.Task{ID: "T1.2", Title: "Second"}
	ids := "T1.2a, T1.2b"
	for _, old := range taskstatus.Choices() {
		sheet := statusSheetFor("app", task.ID, old)
		if len(sheet.Choices) != 7 {
			t.Fatalf("%d choices, want 7", len(sheet.Choices))
		}
		for _, c := range sheet.Choices {
			t.Run(old+"_to_"+c.Status, func(t *testing.T) {
				page := taskStatusPageFor("app", task, old, c.Status, ids)
				if page.Chosen != c.Status {
					t.Fatalf("the page did not reach step two: %+v", page)
				}
				want := c.Lines
				if c.Superseded() {
					want = nil
					for _, l := range c.IDsLines {
						want = append(want, strings.ReplaceAll(l, idsPlaceholder, "T1.2a, T1.2b"))
					}
				}
				if !slices.Equal(page.Choice.Lines, want) {
					t.Errorf("page says %q\nsheet says %q", page.Choice.Lines, want)
				}
				if page.Choice.Action != c.Action || c.Action != "Mark "+c.Status {
					t.Errorf("action labels %q and %q", page.Choice.Action, c.Action)
				}
				if c.Current != (c.Status == old) {
					t.Errorf("current = %v", c.Current)
				}
			})
		}
	}
}

// TestStatusSheet is the sheet's markup on the task screen. Load-bearing
// rows: "running task" (seven native radios, running checked and marked
// current, and every sentence starting with the agent-working line), "outside
// the live region" (a live swap must never take an open sheet away) and "no
// seam" (no action at all when the daemon cannot set a status).
func TestStatusSheet(t *testing.T) {
	t.Run("running task", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.useTasksPlan()
		body := rg.pageDo("GET", "/p/app/tasks/T1.2", "").Body.String()
		sheet := statusSheetHTML(t, body)
		if n := strings.Count(sheet, `<input type="radio" name="status"`); n != 7 {
			t.Errorf("%d radios, want 7", n)
		}
		if strings.Contains(sheet, "disabled") {
			t.Errorf("a choice is disabled")
		}
		for _, w := range []string{
			`data-status-sheet`, `<form method="post" action="/p/app/tasks/T1.2/status" id="confirm-status-form" class="stack" data-enhance>`,
			`<input type="hidden" name="confirm" value="yes">`,
			`<fieldset>`, `<legend class="field__label">New status</legend>`, `<div class="choice-list">`,
			`<input type="radio" name="status" value="running" data-action-label="Mark running" checked><span class="choice__label">running</span><span class="choice__meta">current</span>`,
			`<input type="radio" name="status" value="pending" data-action-label="Mark pending"><span class="choice__label">pending</span></label>`,
			`<input class="input input--mono" type="text" id="confirm-status-ids" name="replaced_by"`,
			`Ids of the tasks that replace it, for example T3.4a, T3.4b`,
			`<div data-sentence="running"><p>T1.2 is already running.</p></div>`,
			`<div data-sentence="pending" hidden><p>An agent is working on T1.2 now; changing its status does not stop it.</p><p>T1.2: running → pending.</p>`,
			`data-ids-template="T1.2: running → superseded (replaced by {ids})." data-ids-default="T1.2: running → superseded.">T1.2: running → superseded.</p>`,
			`<p data-ids-empty hidden>Type the ids of the tasks that replace T1.2, for example T1.2a, T1.2b.</p>`,
			`<button type="submit" form="confirm-status-form" class="btn btn--primary" data-status-action data-loading-label="Saving">Mark running</button>`,
			`<button type="submit" class="btn btn--secondary" autofocus>Leave it as it is</button>`,
		} {
			if !strings.Contains(sheet, w) {
				t.Errorf("sheet lacks %q", w)
			}
		}
		if n := strings.Count(sheet, ">An agent is working on T1.2 now"); n != 6 {
			t.Errorf("the agent-working line appears %d times, want 6 (every change but running itself)", n)
		}
		if styleEl.MatchString(body) || styleAttr.MatchString(body) {
			t.Errorf("the page holds an inline style")
		}
	})
	t.Run("ids field shown for a superseded task", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.useTasksPlan()
		sheet := statusSheetHTML(t, rg.pageDo("GET", "/p/app/tasks/T1.3", "").Body.String())
		if !strings.Contains(sheet, `<div class="field" data-ids-field>`) {
			t.Errorf("the ids field is hidden for a superseded task")
		}
	})
	t.Run("outside the live region", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.useTasksPlan()
		body := rg.pageDo("GET", "/p/app/tasks/T2.2", "").Body.String()
		start := strings.Index(body, `<dialog class="confirm-sheet" id="confirm-status"`)
		link := strings.Index(body, `data-confirm="confirm-status"`)
		live := strings.Index(body, `id="task-status" data-live="ledger"`)
		liveEnd := live + strings.Index(body[live:], "</section>")
		if start < 0 || link < 0 || live < 0 {
			t.Fatalf("sheet %d, link %d, live region %d", start, link, live)
		}
		if start < liveEnd || link < liveEnd {
			t.Errorf("the sheet or its link is inside the live status region")
		}
		if strings.Contains(statusSheetHTML(t, body), "data-live") {
			t.Errorf("the sheet holds a live part")
		}
	})
	t.Run("no seam", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.useTasksPlan()
		h := noSeamHandler(t, rg)
		req := httptest.NewRequest("GET", "/p/app/tasks/T2.2", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
		if b := rec.Body.String(); strings.Contains(b, "Set status") || strings.Contains(b, "confirm-status") {
			t.Errorf("a task screen without the seam offers Set status")
		}
		for _, tc := range []struct{ method, path string }{
			{"POST", "/api/p/app/tasks/T2.2/status"},
			{"POST", "/p/app/tasks/T2.2/status"},
			{"GET", "/p/app/tasks/T2.2/status"},
		} {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("confirm=yes&status=pending"))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Content-Type", formType)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
			}
		}
	})
}

// TestTaskStatusPage is the no-JS page in both steps. Load-bearing rows:
// "step two" (the POST form carries the confirm, the status and the action
// label, so a browser without the script can still set a status),
// "superseded without ids" and "unknown status" (each goes back to step one
// with the reason, never a form that would only be refused), and "unknown
// task" (404, no form at all).
func TestTaskStatusPage(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		status  int
		want    []string
		notWant []string
	}{
		{name: "step one", path: "/p/app/tasks/T2.2/status", status: 200, want: []string{
			"<h1>Set the status of T2.2?</h1>", `<form method="get" action="/p/app/tasks/T2.2/status" class="stack">`,
			`<input type="radio" name="to" value="failed" checked><span class="choice__label">failed</span><span class="choice__meta">current</span>`,
			`<input type="radio" name="to" value="pending"><span class="choice__label">pending</span></label>`,
			`name="replaced_by"`, "Ids of the tasks that replace it, for example T3.4a, T3.4b",
			`<button type="submit" class="btn btn--primary">Continue</button>`,
			`<a class="btn btn--secondary" href="/p/app/tasks/T2.2">Leave it as it is</a>`,
		}, notWant: []string{`method="post"`, "notice--error", `name="confirm"`}},
		{name: "step two", path: "/p/app/tasks/T2.2/status?to=pending", status: 200, want: []string{
			"<h1>Set the status of T2.2?</h1>",
			`<div class="status-sentences"><p>T2.2: failed → pending.</p><p>The build loop may start it on its next pass.</p></div>`,
			`<form method="post" action="/p/app/tasks/T2.2/status">`, `<input type="hidden" name="confirm" value="yes">`,
			`<input type="hidden" name="status" value="pending">`, `<input type="hidden" name="replaced_by" value="">`,
			`<button type="submit" class="btn btn--primary">Mark pending</button>`,
			`<a class="btn btn--secondary" href="/p/app/tasks/T2.2/status">Back</a>`,
		}, notWant: []string{`type="radio"`}},
		{name: "step two, running task", path: "/p/app/tasks/T1.2/status?to=done", status: 200, want: []string{
			"<p>An agent is working on T1.2 now; changing its status does not stop it.</p><p>T1.2: running → done.</p>",
			`<button type="submit" class="btn btn--primary">Mark done</button>`,
		}},
		{name: "step two, superseded with ids", path: "/p/app/tasks/T2.2/status?to=superseded&replaced_by=T3.1,+T3.2", status: 200, want: []string{
			"<p>T2.2: failed → superseded (replaced by T3.1, T3.2).</p>",
			`<input type="hidden" name="replaced_by" value="T3.1 T3.2">`, `<button type="submit" class="btn btn--primary">Mark superseded</button>`,
		}},
		{name: "ids dropped for another status", path: "/p/app/tasks/T2.2/status?to=done&replaced_by=T3.1", status: 200, want: []string{
			"<p>T2.2: failed → done.</p>", `<input type="hidden" name="replaced_by" value="">`,
		}, notWant: []string{"replaced by"}},
		{name: "superseded without ids", path: "/p/app/tasks/T2.2/status?to=superseded&replaced_by=+", status: 200, want: []string{
			`<div class="notice__body">Type the ids of the tasks that replace T2.2, for example T2.2a, T2.2b.</div>`,
			`<input type="radio" name="to" value="superseded" checked>`,
		}, notWant: []string{`method="post"`, `value="failed" checked`}},
		{name: "unknown status", path: "/p/app/tasks/T2.2/status?to=finished", status: 200, want: []string{
			`<div class="notice__body">unknown status &#34;finished&#34; (want one of pending, running, shipping, parked, done, failed, superseded).</div>`,
			`<input type="radio" name="to" value="failed" checked>`,
		}, notWant: []string{`method="post"`}},
		{name: "markup in to", path: "/p/app/tasks/T2.2/status?to=%3Cscript%3E", status: 200, want: []string{"&lt;script&gt;"}, notWant: []string{"<script>"}},
		{name: "unknown task", path: "/p/app/tasks/T9.9/status", status: 404, want: []string{"There is no task with that id"}, notWant: []string{"<form"}},
		{name: "unknown project", path: "/p/nope/tasks/T2.2/status", status: 404},
		{name: "no API twin", path: "/api/p/app/tasks/T2.2/status", status: 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.useTasksPlan()
			rec := rg.pageDo("GET", tc.path, "")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q in\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(noOwnScript(body), w) {
					t.Errorf("must not contain %q", w)
				}
			}
			if len(rg.statusCalls) != 0 {
				t.Errorf("the no-JS page set a status: %v", rg.statusCalls)
			}
		})
	}
	t.Run("needs the token", func(t *testing.T) {
		rg := newRig(t, Config{})
		rec := httptest.NewRecorder()
		rg.h.ServeHTTP(rec, httptest.NewRequest("GET", "/p/app/tasks/T1.2/status?to=done", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
}

// TestTaskStatusPost is each outcome of the status POST, on the page and
// under /api. Load-bearing rows: "changed" (the page goes to the task with
// ?done=status, and the API's answer is read after the write: the fake
// changes the ledger and the JSON must show it), "refused" and "failed"
// (the command's own Text, with 400 and 500), and "superseded with ids"
// (the typed ids reach the seam split as the dashboard splits them).
func TestTaskStatusPost(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		form     string
		json     string
		result   TaskStatusResult
		wantArgs []string
		page     int
		location string
		api      int
		wantText string
	}{
		{name: "changed", id: "T2.2", form: "confirm=yes&status=pending", json: `{"status":"pending","confirm":true}`,
			result: TaskStatusResult{Outcome: StatusChanged, Old: "failed", New: "pending", Text: "T2.2: failed → pending"}, wantArgs: []string{"T2.2", "pending"},
			page: 303, location: "/p/app/tasks/T2.2?done=status", api: 200},
		{name: "already", id: "T2.2", form: "confirm=yes&status=failed", json: `{"status":"failed","confirm":true}`,
			result: TaskStatusResult{Outcome: StatusAlready, Old: "failed", New: "failed", Text: "T2.2 is already failed"}, wantArgs: []string{"T2.2", "failed"},
			page: 303, location: "/p/app/tasks/T2.2?done=already", api: 200},
		{name: "refused unknown status", id: "T2.2", form: "confirm=yes&status=finished", json: `{"status":"finished","confirm":true}`,
			result: TaskStatusResult{Outcome: StatusRefused, Text: `unknown status "finished" (want one of pending, running, shipping, parked, done, failed, superseded)`}, wantArgs: []string{"T2.2", "finished"},
			page: 400, api: 400, wantText: `unknown status "finished"`},
		{name: "superseded with ids", id: "T2.2", form: "confirm=yes&status=superseded&replaced_by=T3.1,+T3.2", json: `{"status":"superseded","replaced_by":["T3.1","T3.2"],"confirm":true}`,
			result: TaskStatusResult{Outcome: StatusChanged, Old: "failed", New: "superseded", Text: "T2.2: failed → superseded (replaced by T3.1, T3.2)"}, wantArgs: []string{"T2.2", "superseded", "T3.1", "T3.2"},
			page: 303, location: "/p/app/tasks/T2.2?done=status", api: 200},
		{name: "superseded without ids", id: "T2.2", form: "confirm=yes&status=superseded&replaced_by=", json: `{"status":"superseded","confirm":true}`,
			result: TaskStatusResult{Outcome: StatusRefused, Text: "superseded needs --replaced-by"}, wantArgs: []string{"T2.2", "superseded"},
			page: 400, api: 400, wantText: "superseded needs --replaced-by"},
		{name: "unknown id", id: "T9.9", form: "confirm=yes&status=done", json: `{"status":"done","confirm":true}`,
			result: TaskStatusResult{Outcome: StatusRefused, Text: "T9.9 is not a task in docs/PLAN.md."}, wantArgs: []string{"T9.9", "done"},
			page: 400, api: 400, wantText: "T9.9 is not a task in docs/PLAN.md."},
		{name: "failed", id: "T2.2", form: "confirm=yes&status=done", json: `{"status":"done","confirm":true}`,
			result: TaskStatusResult{Outcome: StatusFailed, Text: "could not update .afk/build/ledger.json: disk full"}, wantArgs: []string{"T2.2", "done"},
			page: 500, api: 500, wantText: "could not update .afk/build/ledger.json: disk full"},
		{name: "an outcome this page does not know", id: "T2.2", form: "confirm=yes&status=done", json: `{"status":"done","confirm":true}`,
			result: TaskStatusResult{Outcome: "exploded"}, wantArgs: []string{"T2.2", "done"},
			page: 500, api: 500, wantText: "This is a fault in afk"},
	}
	for _, tc := range tests {
		for _, api := range []bool{false, true} {
			kind := "page"
			if api {
				kind = "api"
			}
			t.Run(tc.name+"_"+kind, func(t *testing.T) {
				rg := newRig(t, Config{})
				rg.useTasksPlan()
				rg.statusResult = tc.result
				if tc.result.Outcome == StatusChanged {
					rg.onStatus = func(id, status string, replacedBy []string) {
						for i := range rg.ledger.Tasks {
							if rg.ledger.Tasks[i].ID == id {
								rg.ledger.Tasks[i].Status = ledger.Status(status)
								rg.ledger.Tasks[i].ReplacedBy = replacedBy
							}
						}
					}
				}
				var rec *httptest.ResponseRecorder
				if api {
					rec = rg.do("POST", "/api/p/app/tasks/"+tc.id+"/status", jsonType, tc.json)
				} else {
					rec = rg.pageDo("POST", "/p/app/tasks/"+tc.id+"/status", tc.form)
				}
				want := tc.page
				if api {
					want = tc.api
				}
				if rec.Code != want {
					t.Fatalf("status = %d, want %d\n%s", rec.Code, want, rec.Body.String())
				}
				if len(rg.statusCalls) != 1 || !slices.Equal(rg.statusCalls[0], append([]string{rg.root}, tc.wantArgs...)) {
					t.Errorf("seam calls = %q, want one with %q", rg.statusCalls, tc.wantArgs)
				}
				if !api && want == 303 {
					if loc := rec.Header().Get("Location"); loc != tc.location {
						t.Errorf("Location = %q, want %q", loc, tc.location)
					}
					return
				}
				if api && want == 200 {
					m := decode(t, rec)
					wantKeys(t, m, "project", "id", "title", "status", "depends_on", "unmet", "why", "tokens", "result")
					res, _ := m["result"].(map[string]any)
					if res["outcome"] != tc.result.Outcome || res["old"] != tc.result.Old || res["new"] != tc.result.New || res["text"] != tc.result.Text {
						t.Errorf("result = %v, want %+v", res, tc.result)
					}
					if m["id"] != tc.id {
						t.Errorf("id = %v", m["id"])
					}
					// Read after the call: the fake's write shows here.
					wantStatus := "failed"
					if tc.result.Outcome == StatusChanged {
						wantStatus = tc.wantArgs[1]
					}
					if m["status"] != wantStatus {
						t.Errorf("status = %v, want %v", m["status"], wantStatus)
					}
					return
				}
				body := rec.Body.String()
				if api {
					if msg, _ := decode(t, rec)["error"].(string); !strings.Contains(msg, tc.wantText) {
						t.Errorf("error = %q, want %q", msg, tc.wantText)
					}
				} else if !strings.Contains(html.UnescapeString(body), tc.wantText) {
					t.Errorf("the error page lacks %q:\n%s", tc.wantText, body)
				}
			})
		}
	}
}

// TestTaskStatusNeedsConfirm is the server's confirm rule for the status
// POST, on the page and under /api. Every row is load-bearing: each is a way
// a request could look confirmed without being so (the confirm in the
// address, as text in JSON, or "no"), and none may reach the seam.
func TestTaskStatusNeedsConfirm(t *testing.T) {
	tests := []struct {
		name, path, ctype, body string
	}{
		{name: "page, no body", path: "/p/app/tasks/T2.2/status", ctype: formType},
		{name: "page, no confirm", path: "/p/app/tasks/T2.2/status", ctype: formType, body: "status=pending"},
		{name: "page, confirm=no", path: "/p/app/tasks/T2.2/status", ctype: formType, body: "confirm=no&status=pending"},
		{name: "page, confirm in the address", path: "/p/app/tasks/T2.2/status?confirm=yes", ctype: formType, body: "status=pending"},
		{name: "api, no body", path: "/api/p/app/tasks/T2.2/status"},
		{name: "api, no confirm", path: "/api/p/app/tasks/T2.2/status", ctype: jsonType, body: `{"status":"pending"}`},
		{name: "api, confirm false", path: "/api/p/app/tasks/T2.2/status", ctype: jsonType, body: `{"status":"pending","confirm":false}`},
		{name: "api, confirm as text", path: "/api/p/app/tasks/T2.2/status", ctype: jsonType, body: `{"status":"pending","confirm":"yes"}`},
		{name: "api, confirm in the address", path: "/api/p/app/tasks/T2.2/status?confirm=yes", ctype: jsonType, body: `{"status":"pending"}`},
		{name: "api, form confirm=no", path: "/api/p/app/tasks/T2.2/status", ctype: formType, body: "confirm=no&status=pending"},
		{name: "api, broken JSON", path: "/api/p/app/tasks/T2.2/status", ctype: jsonType, body: `{"confirm":true`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.useTasksPlan()
			rg.statusResult = TaskStatusResult{Outcome: StatusChanged}
			var rec *httptest.ResponseRecorder
			if strings.HasPrefix(tc.path, "/api/") {
				rec = rg.do("POST", tc.path, tc.ctype, tc.body)
			} else {
				rec = rg.pageDo("POST", tc.path, tc.body)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			want := "Nothing changed: setting a task's status needs the confirm. Open the task and press Set status…"
			body := rec.Body.String()
			if strings.HasPrefix(tc.path, "/api/") {
				if msg, _ := decode(t, rec)["error"].(string); msg != want {
					t.Errorf("error = %q", msg)
				}
			} else if !strings.Contains(html.UnescapeString(body), want) {
				t.Errorf("the page lacks the sentence:\n%s", body)
			}
			if len(rg.statusCalls) != 0 {
				t.Errorf("the seam was called %d times without the confirm: %v", len(rg.statusCalls), rg.statusCalls)
			}
		})
	}
	t.Run("api, confirmed but replaced_by is not a list", func(t *testing.T) {
		rg := newRig(t, Config{})
		rec := rg.do("POST", "/api/p/app/tasks/T1.2/status", jsonType, `{"status":"superseded","replaced_by":"T3.1","confirm":true}`)
		if rec.Code != http.StatusBadRequest || len(rg.statusCalls) != 0 {
			t.Errorf("status = %d, seam calls %v", rec.Code, rg.statusCalls)
		}
		if msg, _ := decode(t, rec)["error"].(string); !strings.HasPrefix(msg, "Nothing changed") {
			t.Errorf("error = %q", msg)
		}
	})
}

// TestTaskStatusToast is the toast on a task's screen after the redirect.
// Load-bearing rows: the id and status come from the server's read of the
// ledger (the status in the sentence is the ledger's, whatever the address
// says), and codes that belong to other pages show nothing here.
func TestTaskStatusToast(t *testing.T) {
	tests := []struct {
		name, path, want string
	}{
		{name: "status", path: "/p/app/tasks/T2.2?done=status", want: "T2.2 is now failed."},
		{name: "already", path: "/p/app/tasks/T1.2?done=already", want: "T1.2 was already running. Nothing changed."},
		{name: "status in the address is ignored", path: "/p/app/tasks/T2.2?done=status&status=done", want: "T2.2 is now failed."},
		{name: "a pause code", path: "/p/app/tasks/T2.2?done=paused"},
		{name: "an inbox code", path: "/p/app/tasks/T2.2?done=done"},
		{name: "markup", path: "/p/app/tasks/T2.2?done=%3Cscript%3E"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.useTasksPlan()
			body := rg.pageDo("GET", tc.path, "").Body.String()
			if tc.want == "" {
				if strings.Contains(body, "data-toast") {
					t.Errorf("a toast was drawn")
				}
				return
			}
			if w := `<div class="toast" role="status" data-toast>` + tc.want + `</div>`; !strings.Contains(body, w) {
				t.Errorf("missing %q", w)
			}
		})
	}
	t.Run("not on other pages", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.seedInbox()
		for _, path := range []string{"/p/app?done=status", "/p/app/inbox?done=status&item=" + idBlocking} {
			if body := rg.pageDo("GET", path, "").Body.String(); strings.Contains(body, "data-toast") || strings.Contains(body, "{status}") {
				t.Errorf("%s drew a toast", path)
			}
		}
	})
}

// TestStatusSheetScript checks confirm.js, as served, still drives the
// pause sheet and now the status sheet: the hooks the template carries must
// be the ones the script reads, or the sheet would show every sentence at
// once.
func TestStatusSheetScript(t *testing.T) {
	rg := newRig(t, Config{})
	rg.useTasksPlan()
	body := rg.pageDo("GET", "/p/app/tasks/T2.2", "").Body.String()
	addr := regexp.MustCompile(`/static/[0-9a-f]{12}/confirm\.js`).FindString(body)
	if addr == "" {
		t.Fatalf("the task screen does not load confirm.js")
	}
	rec := rg.pageDo("GET", addr, "")
	if rec.Code != 200 {
		t.Fatalf("confirm.js: status %d", rec.Code)
	}
	js := rec.Body.String()
	for _, hook := range []string{"a[data-confirm]", "showModal", "data-status-sheet", "data-sentence", "data-ids-template", "data-ids-default", "data-ids-empty", "data-ids-field", "data-status-action", "data-action-label", "{ids}", "data-toast"} {
		if !strings.Contains(js, hook) {
			t.Errorf("confirm.js does not read %q", hook)
		}
		if strings.HasPrefix(hook, "data-") && hook != "data-toast" && !strings.Contains(body, hook) {
			t.Errorf("the task screen carries no %q", hook)
		}
	}
	if !strings.Contains(js, idsPlaceholder) {
		t.Errorf("confirm.js does not fill %q", idsPlaceholder)
	}
}
