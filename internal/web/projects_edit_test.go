package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// projectPost sends a POST to path the way the page or the API would: the API
// with the bearer and a JSON body, the page with the cookie and a form.
func (rg *rig) projectPost(path, ctype, body string) *httptest.ResponseRecorder {
	rg.t.Helper()
	if isAPI(path) {
		return rg.do(http.MethodPost, path, ctype, body)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie(testToken)})
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, req)
	return rec
}

// projectSeamCalls is how many times either project seam ran.
func (rg *rig) projectSeamCalls() int {
	return len(rg.addProjectCalls) + len(rg.removeProjectCalls)
}

// TestProjectEditOutcomes walks every outcome either seam can report through
// both the page and the API. Load-bearing: the status of each outcome (a
// refusal must never read as a change), the redirect code of the three that
// redirect (it is fixed text from the outcome, never the request), the
// error page that names the entry point's sentence as it is, pending true
// only for added and removed (only those change the list), and "unknown
// outcome", which proves an answer the page cannot read is a failure and
// never a change. Each row also proves the seam ran exactly once, with the
// trimmed folder and name for add and the listed name for remove.
func TestProjectEditOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		res         ProjectEditResult
		status      int
		location    string
		wantOutcome string
		pending     bool
		text        string
	}{
		{name: "added", res: ProjectEditResult{Outcome: ProjectAdded, Name: "shop", Root: "/srv/shop", Text: "Added shop (/srv/shop) to afk's project list."},
			status: 200, location: "/?done=project_added", wantOutcome: ProjectAdded, pending: true},
		{name: "already", res: ProjectEditResult{Outcome: ProjectAlreadyListed, Name: "shop", Root: "/srv/shop", Text: "shop is already on afk's project list."},
			status: 200, location: "/?done=project_unchanged", wantOutcome: ProjectAlreadyListed},
		{name: "removed", res: ProjectEditResult{Outcome: ProjectRemoved, Name: "shop", Root: "/srv/shop", Text: "Removed shop from afk's project list."},
			status: 200, location: "/?done=project_removed", wantOutcome: ProjectRemoved, pending: true},
		{name: "not listed", res: ProjectEditResult{Outcome: ProjectNotListed, Name: "shop", Text: "shop is not on afk's project list."},
			status: 400, wantOutcome: ProjectNotListed, text: "shop is not on afk's project list."},
		{name: "refused", res: ProjectEditResult{Outcome: ProjectRefused, Text: "/srv/shop is not a git repository. Run git init there first."},
			status: 400, wantOutcome: ProjectRefused, text: "/srv/shop is not a git repository. Run git init there first."},
		{name: "failed", res: ProjectEditResult{Outcome: ProjectFailed, Text: "afk could not write /home/me/.config/afk/config.toml: permission denied."},
			status: 500, wantOutcome: ProjectFailed, text: "afk could not write /home/me/.config/afk/config.toml: permission denied."},
		{name: "refused without a sentence", res: ProjectEditResult{Outcome: ProjectRefused},
			status: 400, wantOutcome: ProjectRefused, text: projectEditNoText},
		{name: "unknown outcome", res: ProjectEditResult{Outcome: "exploded", Name: "shop", Text: "Added it, probably."},
			status: 500, wantOutcome: ProjectFailed, text: projectEditUnknown},
	}
	for _, op := range []string{"add", "remove"} {
		for _, api := range []bool{false, true} {
			for _, tc := range tests {
				where := "page"
				if api {
					where = "api"
				}
				t.Run(op+"/"+where+"/"+tc.name, func(t *testing.T) {
					rg := newRig(t, Config{})
					rg.addProjectResult, rg.removeProjectResult = tc.res, tc.res
					path, ctype, body := "/projects/"+op, formType, ""
					if op == "add" {
						body = url.Values{"confirm": {"yes"}, "folder": {"  ~/code/shop "}, "name": {" shop "}}.Encode()
					} else {
						body = url.Values{"confirm": {"yes"}, "name": {" my app "}}.Encode()
					}
					if api {
						path, ctype = "/api"+path, jsonType
						if op == "add" {
							body = `{"confirm": true, "folder": "  ~/code/shop ", "name": " shop "}`
						} else {
							body = `{"confirm": true, "name": " my app "}`
						}
					}
					rec := rg.projectPost(path, ctype, body)

					if op == "add" {
						if len(rg.addProjectCalls) != 1 || rg.addProjectCalls[0] != [2]string{"~/code/shop", "shop"} || len(rg.removeProjectCalls) != 0 {
							t.Fatalf("seam calls = add %q remove %q, want one add of the trimmed text", rg.addProjectCalls, rg.removeProjectCalls)
						}
					} else if len(rg.removeProjectCalls) != 1 || rg.removeProjectCalls[0] != "my app" || len(rg.addProjectCalls) != 0 {
						t.Fatalf("seam calls = add %q remove %q, want one remove of the listed name", rg.addProjectCalls, rg.removeProjectCalls)
					}

					if api {
						if rec.Code != tc.status {
							t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
						}
						m := decode(t, rec)
						wantKeys(t, m, "result", "pending")
						res, _ := m["result"].(map[string]any)
						wantKeys(t, res, "outcome", "name", "root", "text")
						if res["outcome"] != tc.wantOutcome {
							t.Errorf("outcome = %v, want %s", res["outcome"], tc.wantOutcome)
						}
						if tc.text != "" && res["text"] != tc.text {
							t.Errorf("text = %v, want %q", res["text"], tc.text)
						}
						if tc.text == "" && res["text"] != tc.res.Text {
							t.Errorf("text = %v, want the seam's %q", res["text"], tc.res.Text)
						}
						if m["pending"] != tc.pending {
							t.Errorf("pending = %v, want %v", m["pending"], tc.pending)
						}
						return
					}
					if tc.location != "" {
						if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != tc.location {
							t.Fatalf("answer = %d to %q, want 303 to %q\n%s", rec.Code, rec.Header().Get("Location"), tc.location, rec.Body.String())
						}
						return
					}
					if rec.Code != tc.status {
						t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
					}
					page := rec.Body.String()
					if !strings.Contains(page, "<html") || !strings.Contains(page, htmlEsc(tc.text)) {
						t.Errorf("the page does not draw the sentence %q inside the layout:\n%s", tc.text, page)
					}
				})
			}
		}
	}
}

// htmlEsc is how html/template writes a sentence's apostrophes, so a page
// can be searched for the sentence it was given.
func htmlEsc(s string) string {
	return strings.NewReplacer("'", "&#39;", "&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;").Replace(s)
}

// TestProjectEditNeedsTheConfirm is the task's safety rule: a POST without
// the confirm changes nothing. Load-bearing: every row leaves both seams'
// call counts at zero; "confirm in the query" proves an address someone was
// sent cannot carry the confirm; "confirm as a word" proves JSON must say
// true; the API answers reason confirm_required so a client can tell it from
// a refusal by the entry point.
func TestProjectEditNeedsTheConfirm(t *testing.T) {
	tests := []struct {
		name, path, ctype, body, want string
	}{
		{"add page, no confirm", "/projects/add", formType, "folder=%7E%2Fcode%2Fshop", projectAddNoConfirm},
		{"add page, confirm=no", "/projects/add", formType, "confirm=no&folder=%7E%2Fcode%2Fshop", projectAddNoConfirm},
		{"add page, confirm in the query", "/projects/add?confirm=yes", formType, "folder=%7E%2Fcode%2Fshop", projectAddNoConfirm},
		{"add page, empty body", "/projects/add", formType, "", projectAddNoConfirm},
		{"add api, no confirm", "/api/projects/add", jsonType, `{"folder": "~/code/shop"}`, projectAddNoConfirm},
		{"add api, confirm false", "/api/projects/add", jsonType, `{"confirm": false, "folder": "~/code/shop"}`, projectAddNoConfirm},
		{"add api, confirm as a word", "/api/projects/add", jsonType, `{"confirm": "yes", "folder": "~/code/shop"}`, projectAddNoConfirm},
		{"add api, form body", "/api/projects/add", formType, "folder=%7E%2Fcode%2Fshop", projectAddNoConfirm},
		{"remove page, no confirm", "/projects/remove", formType, "name=app", projectRemoveNoConfirm},
		{"remove page, confirm in the query", "/projects/remove?confirm=yes", formType, "name=app", projectRemoveNoConfirm},
		{"remove api, no confirm", "/api/projects/remove", jsonType, `{"name": "app"}`, projectRemoveNoConfirm},
		{"remove api, confirm false", "/api/projects/remove", jsonType, `{"confirm": false, "name": "app"}`, projectRemoveNoConfirm},
		{"remove api, broken JSON", "/api/projects/remove", jsonType, `{"confirm": true, "name": "app"`, projectRemoveNoConfirm},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := rg.projectPost(tc.path, tc.ctype, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if n := rg.projectSeamCalls(); n != 0 {
				t.Fatalf("a project seam ran %d times without the confirm: %q %q", n, rg.addProjectCalls, rg.removeProjectCalls)
			}
			if isAPI(tc.path) {
				if m := decode(t, rec); m["error"] != tc.want || m["reason"] != "confirm_required" {
					t.Errorf("body = %v", m)
				}
			} else if !strings.Contains(rec.Body.String(), htmlEsc(tc.want)) {
				t.Errorf("the page does not say %q:\n%s", tc.want, rec.Body.String())
			}
		})
	}
}

// TestProjectEditRefusedBeforeTheSeam covers what the page refuses itself,
// before either seam is asked. Load-bearing: "remove of a name not listed"
// (the remove seam is only ever given a name on the daemon's list, so a
// crafted POST cannot reach a project the page does not show), "remove of a
// folder" (a folder is not a name, even the folder of a listed project), and
// "add with an empty folder" (the seam is never asked about nothing).
func TestProjectEditRefusedBeforeTheSeam(t *testing.T) {
	tests := []struct {
		name, path, ctype, body string
		outcome, text           string
	}{
		{"add with an empty folder", "/api/projects/add", jsonType, `{"confirm": true, "folder": "   ", "name": "shop"}`, ProjectRefused, projectAddNoFolder},
		{"add page with an empty folder", "/projects/add", formType, "confirm=yes&folder=+&name=shop", ProjectRefused, projectAddNoFolder},
		{"add with a folder that is not text", "/api/projects/add", jsonType, `{"confirm": true, "folder": 7}`, ProjectRefused, projectAddUnreadable},
		{"remove with no name", "/api/projects/remove", jsonType, `{"confirm": true}`, ProjectNotListed, projectRemoveNoName},
		{"remove with a name that is not text", "/api/projects/remove", jsonType, `{"confirm": true, "name": ["app"]}`, ProjectRefused, projectRemoveUnread},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := rg.projectPost(tc.path, tc.ctype, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if n := rg.projectSeamCalls(); n != 0 {
				t.Fatalf("a project seam ran %d times: %q %q", n, rg.addProjectCalls, rg.removeProjectCalls)
			}
			if !isAPI(tc.path) {
				if !strings.Contains(rec.Body.String(), htmlEsc(tc.text)) {
					t.Errorf("the page does not say %q:\n%s", tc.text, rec.Body.String())
				}
				return
			}
			m := decode(t, rec)
			res, _ := m["result"].(map[string]any)
			if res["outcome"] != tc.outcome || res["text"] != tc.text || m["pending"] != false {
				t.Errorf("body = %v", m)
			}
		})
	}
}

// TestProjectRemoveNotListed is the remove of a name the daemon's list does
// not hold. Load-bearing: the seam is not called, the answer is 400
// not_listed in the plan's sentence, and a listed project's folder posted as
// the name is refused like any other unknown name.
func TestProjectRemoveNotListed(t *testing.T) {
	rg0 := newRig(t, Config{})
	tests := []struct {
		name, posted string
	}{
		{"unknown name", "shop"},
		{"a listed project's folder", rg0.root},
		{"a review's name", "draft"},
		{"case differs", "App"},
	}
	for _, tc := range tests {
		for _, api := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/api=%v", tc.name, api), func(t *testing.T) {
				rg := newRig(t, Config{})
				posted := tc.posted
				if posted == rg0.root {
					posted = rg.root
				}
				want := posted + " is not in afk's project list, so there is nothing to remove."
				var rec *httptest.ResponseRecorder
				if api {
					rec = rg.projectPost("/api/projects/remove", jsonType, fmt.Sprintf(`{"confirm": true, "name": %q}`, posted))
				} else {
					rec = rg.projectPost("/projects/remove", formType, url.Values{"confirm": {"yes"}, "name": {posted}}.Encode())
				}
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
				}
				if len(rg.removeProjectCalls) != 0 {
					t.Fatalf("the remove seam ran for a name not on the list: %q", rg.removeProjectCalls)
				}
				if api {
					m := decode(t, rec)
					res, _ := m["result"].(map[string]any)
					if res["outcome"] != ProjectNotListed || res["text"] != want || m["pending"] != false {
						t.Errorf("body = %v", m)
					}
				} else if !strings.Contains(rec.Body.String(), htmlEsc(want)) {
					t.Errorf("the page does not say %q:\n%s", want, rec.Body.String())
				}
			})
		}
	}
}

// TestProjectEditNilSeams proves a server built without a seam has no such
// POST: both paths answer 404, as any address that does not exist, and the
// other seam's POST still works. Load-bearing: the 404 comes before the body
// is read, so a confirmed POST to a missing seam changes nothing.
func TestProjectEditNilSeams(t *testing.T) {
	tests := []struct {
		name          string
		noAdd, noRm   bool
		path, body    string
		wantStatus    int
		wantSeamCalls int
	}{
		{"add without AddProject", true, false, "/projects/add", `{"confirm": true, "folder": "~/code/shop"}`, 404, 0},
		{"remove without RemoveProject", false, true, "/projects/remove", `{"confirm": true, "name": "app"}`, 404, 0},
		{"remove without AddProject still works", true, false, "/projects/remove", `{"confirm": true, "name": "app"}`, 200, 1},
		{"add without RemoveProject still works", false, true, "/projects/add", `{"confirm": true, "folder": "~/code/shop"}`, 200, 1},
	}
	for _, tc := range tests {
		for _, prefix := range []string{"", "/api"} {
			t.Run(tc.name+prefix, func(t *testing.T) {
				rg := newRig(t, Config{})
				if tc.noAdd {
					rg.srv.d.AddProject = nil
				}
				if tc.noRm {
					rg.srv.d.RemoveProject = nil
				}
				rec := rg.projectPost(prefix+tc.path, jsonType, tc.body)
				got := rec.Code
				if got == http.StatusSeeOther {
					got = http.StatusOK
				}
				if got != tc.wantStatus {
					t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
				}
				if n := rg.projectSeamCalls(); n != tc.wantSeamCalls {
					t.Errorf("seam calls = %d, want %d", n, tc.wantSeamCalls)
				}
			})
		}
	}
}

// TestPendingNotices is the pure decision behind the overview's "not on the
// list yet" lines. Load-bearing: "add not yet listed" and "remove still
// listed" (the notice shows until the daemon's list proves the change),
// "add listed by name" / "add listed by folder" and "remove gone" (it goes
// the moment the list shows it, so the overview never contradicts its own
// rows), "expired" (a change the daemon never picked up stops claiming
// "within a few seconds"), and "cap" (at most 32, the newest kept).
func TestPendingNotices(t *testing.T) {
	added := func(name, root string, ago time.Duration) projectChange {
		return projectChange{Outcome: ProjectAdded, Name: name, Root: root, At: testNow.Add(-ago)}
	}
	removed := func(name, root string, ago time.Duration) projectChange {
		return projectChange{Outcome: ProjectRemoved, Name: name, Root: root, At: testNow.Add(-ago)}
	}
	addText := func(name string) string {
		return name + " was added. It shows here when the daemon next reads the list, within a few seconds."
	}
	rmText := func(name string) string {
		return name + " was removed. Its row goes when the daemon next reads the list, within a few seconds."
	}
	listed := []Project{{Name: "app", Root: "/srv/app"}, {Name: "blog", Root: "/srv/blog"}}
	var many []projectChange
	var manyWant []string
	for i := 0; i < 33; i++ {
		name := fmt.Sprintf("p%02d", i)
		many = append(many, added(name, "/srv/"+name, time.Duration(33-i)*time.Second))
		if i > 0 {
			manyWant = append(manyWant, addText(name))
		}
	}
	tests := []struct {
		name    string
		pending []projectChange
		want    []string
	}{
		{"nothing pending", nil, []string{}},
		{"add not yet listed", []projectChange{added("shop", "/srv/shop", time.Second)}, []string{addText("shop")}},
		{"add listed by name", []projectChange{added("app", "/elsewhere/app", time.Second)}, []string{}},
		{"add listed by folder", []projectChange{added("shop", "/srv/blog/", time.Second)}, []string{}},
		{"remove still listed", []projectChange{removed("blog", "/srv/blog", time.Second)}, []string{rmText("blog")}},
		{"remove gone", []projectChange{removed("shop", "/srv/shop", time.Second)}, []string{}},
		{"expired", []projectChange{added("shop", "/srv/shop", 11*time.Minute), removed("blog", "/srv/blog", 10*time.Minute+time.Second)}, []string{}},
		{"just inside the window", []projectChange{added("shop", "/srv/shop", 10*time.Minute)}, []string{addText("shop")}},
		{"oldest first", []projectChange{added("shop", "/srv/shop", 2*time.Second), removed("blog", "/srv/blog", time.Second)},
			[]string{addText("shop"), rmText("blog")}},
		{"other outcomes say nothing", []projectChange{{Outcome: ProjectAlreadyListed, Name: "shop", At: testNow}}, []string{}},
		{"cap", many, manyWant},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pendingNotices(tc.pending, listed, testNow)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) || len(got) != len(tc.want) || got == nil {
				t.Errorf("pendingNotices =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestProjectPendingRecord covers the server's memory of changes. Load-
// bearing: only added and removed with a name are recorded (so pending is
// false for a refusal and the notice always has a name to say), a newer
// change for a name replaces the older one, the list never holds more than
// 32, and notices drops what the list now shows for good.
func TestProjectPendingRecord(t *testing.T) {
	p := newProjectPending()
	if p.record(ProjectEditResult{Outcome: ProjectRefused, Name: "x"}, testNow) {
		t.Errorf("a refusal was recorded")
	}
	if p.record(ProjectEditResult{Outcome: ProjectAlreadyListed, Name: "x"}, testNow) {
		t.Errorf("an unchanged list was recorded")
	}
	if p.record(ProjectEditResult{Outcome: ProjectAdded, Root: "/srv/x"}, testNow) {
		t.Errorf("a change with no name was recorded")
	}
	if !p.record(ProjectEditResult{Outcome: ProjectRemoved, Name: "shop", Root: "/srv/shop"}, testNow) ||
		!p.record(ProjectEditResult{Outcome: ProjectAdded, Name: "shop", Root: "/srv/shop2"}, testNow.Add(time.Second)) {
		t.Fatalf("a change that went through was not recorded")
	}
	if got := p.notices(nil, testNow.Add(2*time.Second)); len(got) != 1 || !strings.HasPrefix(got[0], "shop was added.") {
		t.Errorf("notices = %q, want the add alone (it replaced the remove)", got)
	}
	for i := 0; i < 40; i++ {
		p.record(ProjectEditResult{Outcome: ProjectAdded, Name: fmt.Sprintf("p%02d", i)}, testNow.Add(time.Duration(i)*time.Second))
	}
	if len(p.entries) != projectPendingMax || p.entries[0].Name != "p08" {
		t.Errorf("entries = %d starting %q, want %d starting p08", len(p.entries), p.entries[0].Name, projectPendingMax)
	}
	listed := []Project{{Name: "p39", Root: "/srv/p39"}}
	if got := p.notices(listed, testNow.Add(time.Minute)); len(got) != 31 {
		t.Errorf("notices = %d, want 31 (p39 is listed now)", len(got))
	}
	if len(p.entries) != 31 {
		t.Errorf("entries = %d, want 31: a listed change is dropped for good", len(p.entries))
	}
}

// TestProjectEditPendingOnTheOverview follows one add and one remove from the
// POST to the overview's JSON. Load-bearing: the notice names the project the
// seam's result named (never the typed text), stays while the daemon's list
// has not caught up, and goes once it has; a refusal leaves nothing pending.
func TestProjectEditPendingOnTheOverview(t *testing.T) {
	rg := newRig(t, Config{})
	pending := func() []any {
		t.Helper()
		m := decode(t, rg.do(http.MethodGet, "/api/", "", ""))
		p, ok := m["pending"].([]any)
		if !ok {
			t.Fatalf("pending is not a list: %v", m["pending"])
		}
		return p
	}
	if p := pending(); len(p) != 0 {
		t.Fatalf("pending before any change = %v", p)
	}
	rg.addProjectResult = ProjectEditResult{Outcome: ProjectRefused, Text: "no"}
	rg.projectPost("/api/projects/add", jsonType, `{"confirm": true, "folder": "/srv/x"}`)
	if p := pending(); len(p) != 0 {
		t.Fatalf("a refusal left a notice: %v", p)
	}

	rg.addProjectResult = ProjectEditResult{Outcome: ProjectAdded, Name: "shop", Root: rg.fresh, Text: "Added."}
	rg.projectPost("/api/projects/add", jsonType, `{"confirm": true, "folder": "~/typed-text", "name": "typed"}`)
	rg.projectPost("/api/projects/remove", jsonType, `{"confirm": true, "name": "my app"}`)
	p := pending()
	if len(p) != 2 || !strings.HasPrefix(p[0].(string), "shop was added.") || !strings.HasPrefix(p[1].(string), "my app was removed.") {
		t.Fatalf("pending = %q", p)
	}
	for _, s := range p {
		if strings.Contains(s.(string), "typed") {
			t.Errorf("a notice echoes the request: %q", s)
		}
	}

	// The daemon's next pass: shop is listed, my app is gone.
	rg.projects = []Project{{Name: "app", Root: rg.root}, {Name: "shop", Root: rg.fresh}}
	if p := pending(); len(p) != 0 {
		t.Errorf("pending after the list caught up = %v", p)
	}
}

// TestProjectPagesRender is the add form, the remove screen and the no-JS
// confirm page. Load-bearing: "add form" (the hidden confirm, both fields and
// their hints, or a typed POST could not change anything), "remove screen"
// (the daemon's own projects with name and folder, and a button that goes to
// the confirm page), "remove screen, empty" (no button when there is nothing
// to remove), "confirm page" (names the project and its folder, says what is
// not touched, and Keep it is the focused button) and the missing inline
// style rule, which the page's CSP refuses.
func TestProjectPagesRender(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		noList  bool
		want    []string
		notWant []string
	}{
		{name: "add form", path: "/projects/add", want: []string{
			`<form method="post" action="/projects/add"`, `<input type="hidden" name="confirm" value="yes">`,
			`name="folder"`, `name="name"`, `A ~ means your home folder. The folder must be a git repository.`,
			`Defaults to the folder name`, `>Add project</button>`, `<a class="btn btn--quiet" href="/">Cancel</a>`}},
		{name: "remove screen", path: "/projects/remove", want: []string{
			`<fieldset>`, `<legend`, `type="radio" name="name" value="app"`, `class="choice__label">app</span>`, `class="choice__meta">`,
			`action="/projects/remove/confirm"`, `>Remove…</button>`, `id="confirm-remove-project"`, `data-fill-text="Remove {name} from afk?"`}},
		{name: "remove screen, empty", path: "/projects/remove", noList: true,
			want:    []string{`there is nothing to remove`},
			notWant: []string{`Remove…`, `type="radio"`, `confirm-remove-project`}},
		{name: "confirm page", path: "/projects/remove/confirm?name=app", want: []string{
			`Remove app from afk?`, `afk stops watching it. These are not touched: the folder `, `, its plan, its sessions. A task that is running is left to finish.`,
			`action="/projects/remove"`, `name="confirm" value="yes"`, `name="name" value="app"`,
			`class="btn btn--danger">Remove</button>`, `<a class="btn btn--secondary" href="/projects/remove" autofocus>Keep it</a>`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.noList {
				rg.projects = nil
			}
			rec := rg.pageDo(http.MethodGet, tc.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d\n%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("the page lacks %q", w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("the page holds %q", w)
				}
			}
			if strings.Contains(body, "style=") || strings.Contains(body, "<style") {
				t.Errorf("the page holds an inline style")
			}
			if tc.path == "/projects/remove" && !tc.noList && !strings.Contains(body, rg.root) {
				t.Errorf("the remove screen does not show the folder %s", rg.root)
			}
			if strings.HasPrefix(tc.path, "/projects/remove/confirm") && !strings.Contains(body, htmlEsc(rg.root)) {
				t.Errorf("the confirm page does not name the folder %s", rg.root)
			}
			if rg.projectSeamCalls() != 0 {
				t.Errorf("a GET called a seam")
			}
		})
	}
}

// TestProjectPagesNotFound: every page of the feature answers 404 without
// its seam or, for the confirm page, for a name that is not on the daemon's
// list, and no seam runs. Load-bearing: "unknown name" and "a folder is not
// a name" (the confirm page only ever speaks about a project the page shows),
// and the nil-seam rows (no button may lead to a 404 and no 404 may be
// reached with a button).
func TestProjectPagesNotFound(t *testing.T) {
	tests := []struct {
		name        string
		noAdd, noRm bool
		path        string
	}{
		{"add form without AddProject", true, false, "/projects/add"},
		{"remove screen without RemoveProject", false, true, "/projects/remove"},
		{"confirm page without RemoveProject", false, true, "/projects/remove/confirm?name=app"},
		{"confirm page, unknown name", false, false, "/projects/remove/confirm?name=nope"},
		{"confirm page, no name", false, false, "/projects/remove/confirm"},
		{"confirm page, a folder is not a name", false, false, "/projects/remove/confirm?name=%2Ftmp"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.noAdd {
				rg.srv.d.AddProject = nil
			}
			if tc.noRm {
				rg.srv.d.RemoveProject = nil
			}
			if rec := rg.pageDo(http.MethodGet, tc.path, ""); rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if rg.projectSeamCalls() != 0 {
				t.Errorf("a seam ran")
			}
		})
	}
}

// TestProjectRefusalRedrawsTheForm: a refused or failed page POST draws the
// form it came from again, at the status, with the entry point's sentence in
// an error notice and the typed values kept. Load-bearing: the typed folder
// survives (the owner fixes one word, not retypes a path) and a remove that
// fails lands on the remove screen, not the error page whose Try again would
// be a 404.
func TestProjectRefusalRedrawsTheForm(t *testing.T) {
	rg := newRig(t, Config{})
	rg.addProjectResult = ProjectEditResult{Outcome: ProjectRefused, Text: "/srv/x is not a git repository."}
	rec := rg.projectPost("/projects/add", formType, "confirm=yes&folder=%2Fsrv%2Fx&name=ex")
	body := rec.Body.String()
	if rec.Code != 400 || !strings.Contains(body, `class="notice notice--error" role="alert"`) || !strings.Contains(body, "/srv/x is not a git repository.") ||
		!strings.Contains(body, `value="/srv/x"`) || !strings.Contains(body, `value="ex"`) || !strings.Contains(body, `action="/projects/add"`) {
		t.Errorf("add redraw: status %d\n%s", rec.Code, body)
	}
	rg.removeProjectResult = ProjectEditResult{Outcome: ProjectFailed, Text: "afk could not write config.toml."}
	rec = rg.projectPost("/projects/remove", formType, "confirm=yes&name=app")
	body = rec.Body.String()
	if rec.Code != 500 || !strings.Contains(body, "afk could not write config.toml.") || !strings.Contains(body, `action="/projects/remove/confirm"`) {
		t.Errorf("remove redraw: status %d\n%s", rec.Code, body)
	}
}

// TestProjectOverviewButtons: the overview's buttons follow the seams.
// Load-bearing: "neither seam" (no footer at all, so no empty stack), "only
// remove" (the footer is drawn for one button), and the app-bar icon, which
// is drawn on the overview only.
func TestProjectOverviewButtons(t *testing.T) {
	icon := `aria-label="Add a project"`
	top := `href="/projects/add">Add a project</a>`
	foot := `<div class="btn-stack overview-foot">`
	addBtn := `<a class="btn btn--secondary" href="/projects/add">Add an existing project</a>`
	rmBtn := `<a class="btn btn--quiet" href="/projects/remove">Remove a project</a>`
	tests := []struct {
		name                       string
		noAdd, noRm, noPlan        bool
		wantIcon, wantTop, wantAdd bool
		wantRm, wantFoot           bool
	}{
		{"all seams", false, false, false, true, true, true, true, true},
		{"no add", true, false, false, false, false, false, true, true},
		{"only remove", true, false, true, false, false, false, true, true},
		{"only add", false, true, true, true, true, true, false, true},
		{"neither", true, true, true, false, false, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.noAdd {
				rg.srv.d.AddProject = nil
			}
			if tc.noRm {
				rg.srv.d.RemoveProject = nil
			}
			if tc.noPlan {
				rg.srv.d.StartPlan, rg.srv.d.CheckPlanFolder = nil, nil
			}
			body := rg.pageDo(http.MethodGet, "/", "").Body.String()
			for _, c := range []struct {
				w    string
				want bool
			}{{icon, tc.wantIcon}, {top, tc.wantTop}, {addBtn, tc.wantAdd}, {rmBtn, tc.wantRm}, {foot, tc.wantFoot}} {
				if strings.Contains(body, c.w) != c.want {
					t.Errorf("%q present = %v, want %v", c.w, !c.want, c.want)
				}
			}
			if other := rg.pageDo(http.MethodGet, "/usage", "").Body.String(); strings.Contains(other, icon) != false || strings.Contains(other, top) != tc.wantTop {
				t.Errorf("another page: icon or top bar action wrong")
			}
		})
	}
}

// TestProjectOverviewPendingAndToast: pending notices are drawn in a polite
// live region and the three project toasts show on the overview only.
// Load-bearing: an unknown ?done= code shows nothing, and the same code on a
// project page shows nothing (toastFor keeps the sets apart).
func TestProjectOverviewPendingAndToast(t *testing.T) {
	rg := newRig(t, Config{})
	rg.projectPost("/projects/add", formType, "confirm=yes&folder=%2Fsrv%2Fshop")
	body := rg.pageDo(http.MethodGet, "/", "").Body.String()
	if !strings.Contains(body, `aria-live="polite"><div class="notice notice--info"><div class="notice__body">shop was added.`) {
		t.Errorf("the pending notice is not drawn:\n%s", body)
	}
	toast := regexp.MustCompile(`data-toast>([^<]*)<`)
	tests := []struct{ path, want string }{
		{"/?done=project_added", "Project added."},
		{"/?done=project_removed", "Project removed."},
		{"/?done=project_unchanged", "Nothing was changed."},
		{"/?done=bogus", ""},
		{"/?done=paused", ""},
		{"/p/app?done=project_added", ""},
		{"/usage?done=project_removed", ""},
		{"/projects/add?done=project_added", ""},
	}
	for _, tc := range tests {
		got := ""
		if m := toast.FindStringSubmatch(rg.pageDo(http.MethodGet, tc.path, "").Body.String()); m != nil {
			got = m[1]
		}
		if got != tc.want {
			t.Errorf("%s: toast = %q, want %q", tc.path, got, tc.want)
		}
	}
}
