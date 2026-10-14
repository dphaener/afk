package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// actionRows is a project holding one session of every kind the actions
// treat differently.
func actionRows() []SessionView {
	return []SessionView{
		{Name: "finished", Phase: "done", WorktreePath: "/work/app/finished", Label: "login fix"},
		{Name: "busy", Phase: "executing", HostRunning: true, WorktreePath: "/work/app/busy"},
		{Name: "stuck", Phase: "executing", HostRunning: true, Wedged: true, WorktreePath: "/work/app/stuck"},
		{Name: "scratchy", Scratch: true, Stale: true, HostRunning: true, WorktreePath: "/work/app"},
		{Name: "master-plan", Scratch: true, Stale: true, Planning: true, HostRunning: true},
		{Name: "lost", Problem: "Its worktree folder is missing"},
		{Name: "stray", Phase: "failed", Stranded: true, WorktreePath: "/work/app"},
	}
}

// actionRig is the rig with actionRows as project "app"'s sessions.
func actionRig(t *testing.T) *rig {
	t.Helper()
	rg := newRig(t, Config{})
	rg.sessions = actionRows()
	return rg
}

// TestOffersFor is which actions each kind of row offers. Load-bearing rows:
// "healthy running host" (restarting it would cut a working agent off, so
// it is never offered), "one-off" and "planning session" (a restart would
// bring back an attended session the owner cannot talk to here), "worktree
// problem" (no label and no restart, but Clean, which is how it is tidied
// up), and "stopped but marked wedged" (a stopped host is restartable
// whatever mark its host.json kept).
func TestOffersFor(t *testing.T) {
	tests := []struct {
		name                  string
		s                     SessionView
		restart, label, clean bool
		why                   string
	}{
		{name: "stopped planned session", s: SessionView{Name: "a", Phase: "done"}, restart: true, label: true, clean: true},
		{name: "wedged running host", s: SessionView{Name: "a", HostRunning: true, Wedged: true}, restart: true, label: true, clean: true},
		{name: "stopped but marked wedged", s: SessionView{Name: "a", Wedged: true}, restart: true, label: true, clean: true},
		{name: "status unknown and stopped", s: SessionView{Name: "a", StatusStale: true, Stale: true}, restart: true, label: true, clean: true},
		{name: "healthy running host", s: SessionView{Name: "a", HostRunning: true}, label: true, clean: true, why: "Its host is running and its agent is working"},
		{name: "one-off", s: SessionView{Name: "a", Scratch: true}, label: true, clean: true, why: "A one-off session is restarted from afk dash"},
		{name: "planning session", s: SessionView{Name: "master-plan", Scratch: true, Planning: true}, label: true, clean: true, why: "The planning session is restarted from afk dash"},
		{name: "stranded entry", s: SessionView{Name: "a", Stranded: true}, label: true, clean: true, why: "not in a worktree of its own"},
		{name: "worktree problem", s: SessionView{Name: "a", Problem: "gone"}, clean: true, why: "This is a problem with a worktree"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := offersFor(tc.s)
			if o.Restart != tc.restart || o.Label != tc.label || o.Clean != tc.clean {
				t.Errorf("offers = restart %v label %v clean %v, want %v %v %v", o.Restart, o.Label, o.Clean, tc.restart, tc.label, tc.clean)
			}
			if tc.restart != (o.RestartWhy == "") {
				t.Errorf("RestartWhy = %q with restart %v", o.RestartWhy, o.Restart)
			}
			if !strings.Contains(o.RestartWhy, tc.why) {
				t.Errorf("RestartWhy = %q, want it to say %q", o.RestartWhy, tc.why)
			}
			if tc.label != (o.LabelWhy == "") {
				t.Errorf("LabelWhy = %q with label %v", o.LabelWhy, o.Label)
			}
		})
	}
}

// bodyText is a sheet's sentences joined, for comparing in one line.
func bodyText(sh sessionSheet) string {
	var out []string
	for _, l := range sh.Body {
		out = append(out, l.Text)
	}
	return strings.Join(out, " ")
}

// TestSessionSheetWords is each confirm's words for each kind of row, which
// are the dashboard's. Load-bearing rows: "clean worktree problem" and
// "clean stranded entry" (the two whose clean keeps something else than the
// usual archive, so the usual sentence would promise what clean does not
// do), "clean running session" (says the host is stopped first), and the two
// restart rows (only a running host has a client to disconnect).
func TestSessionSheetWords(t *testing.T) {
	tests := []struct {
		name        string
		sheet       sessionSheet
		title, body string
	}{
		{name: "clean planned session", sheet: cleanSheetFor("app", SessionView{Name: "t1-2", Phase: "done"}),
			title: `Clean session "t1-2" (phase: done)?`, body: "Worktree goes; archive kept. The branch is kept."},
		{name: "clean running session", sheet: cleanSheetFor("app", SessionView{Name: "t1-2", Phase: "executing", HostRunning: true}),
			title: `Clean session "t1-2" (phase: executing)?`, body: "Worktree goes; archive kept. The branch is kept. Its host is running, so it is stopped first."},
		{name: "clean with no phase", sheet: cleanSheetFor("app", SessionView{Name: "t1-3", StatusStale: true}),
			title: `Clean session "t1-3" (phase: unknown)?`, body: "Worktree goes; archive kept. The branch is kept."},
		{name: "clean one-off", sheet: cleanSheetFor("app", SessionView{Name: "s", Scratch: true, HostRunning: true}),
			title: `Clean one-off session "s"?`, body: "Stops the host and removes the session directory."},
		{name: "clean stranded entry", sheet: cleanSheetFor("app", SessionView{Name: "s", Phase: "failed", Stranded: true}),
			title: `Clean session "s" (phase: failed)?`, body: "Session dir archived and removed; worktree kept."},
		{name: "clean worktree problem", sheet: cleanSheetFor("app", SessionView{Name: "s", Problem: "gone"}),
			title: `Clean session "s" (worktree problem)?`, body: "Worktree goes; nothing archived. The branch is kept."},
		{name: "restart wedged", sheet: restartSheetFor("app", SessionView{Name: "s", HostRunning: true, Wedged: true}),
			title: `Restart "s"?`, body: "Replaces its child process and disconnects any attached client. The agent resumes its conversation."},
		{name: "restart stopped", sheet: restartSheetFor("app", SessionView{Name: "s"}),
			title: `Restart "s"?`, body: "Starts its host again; the agent resumes from its files."},
		{name: "label", sheet: labelSheetFor("app", SessionView{Name: "s", Label: "old"}),
			title: "Label for s:", body: ""},
		{name: "new, filled", sheet: newSheetFor("app", "fix-login", "Fix it"),
			title: `Start autonomous session "fix-login"?`, body: "It works on its own, from plan to reviewed commits on branch fix-login, with nobody asked along the way. Its terminal opens only from afk dash or afk attach."},
		{name: "new, for the script", sheet: newSheetFor("app", "", ""),
			title: `Start autonomous session ""?`, body: "It works on its own, from plan to reviewed commits on branch , with nobody asked along the way. Its terminal opens only from afk dash or afk attach."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sheet.Title != tc.title {
				t.Errorf("title = %q, want %q", tc.sheet.Title, tc.title)
			}
			if got := bodyText(tc.sheet); got != tc.body {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
			if strings.Contains(bodyText(tc.sheet), "Its copy of the project is deleted") {
				t.Errorf("the design's sample sentence is used")
			}
		})
	}
	t.Run("buttons", func(t *testing.T) {
		for _, c := range []struct {
			sh                    sessionSheet
			button, loading, safe string
			danger                bool
		}{
			{cleanSheetFor("app", SessionView{Name: "s"}), "Clean session", "Cleaning", "Keep it", true},
			{restartSheetFor("app", SessionView{Name: "s"}), "Restart", "Restarting", "Leave it", false},
			{labelSheetFor("app", SessionView{Name: "s"}), "Save label", "Saving", "Leave it", false},
			{newSheetFor("app", "s", "d"), "Start session", "Starting", "Don't start", false},
		} {
			if c.sh.Button != c.button || c.sh.Loading != c.loading || c.sh.Safe != c.safe || c.sh.Danger != c.danger {
				t.Errorf("%s: %q %q %q danger %v", c.sh.ID, c.sh.Button, c.sh.Loading, c.sh.Safe, c.sh.Danger)
			}
		}
	})
	t.Run("clean posts the phase shown", func(t *testing.T) {
		sh := cleanSheetFor("my app", SessionView{Name: "t1-2", Phase: "done"})
		if !reflect.DeepEqual(sh.Fields, []sheetField{{Name: "phase", Value: "done"}}) || sh.Path != "/p/my%20app/sessions/t1-2/clean" || sh.Page != "/p/my%20app/sessions/t1-2/confirm/clean" {
			t.Errorf("sheet = %+v", sh)
		}
	})
	t.Run("the script's words are the server's", func(t *testing.T) {
		sh := newSheetFor("app", "", "")
		if sh.TitleFill != newTitle || sh.Body[0].Fill != newBodyRun || !sh.QuoteFill {
			t.Errorf("sheet = %+v", sh)
		}
		if filled := newSheetFor("app", "x", "y"); filled.QuoteFill || filled.Quote != "y" {
			t.Errorf("a filled sheet = %+v", filled)
		}
	})
}

// TestSessionActionsFor is the Actions rows: the design's order, only what
// is offered, only what the server can do. Load-bearing: "no seams" (a server
// without the seams draws no action at all) and "worktree problem" (Clean
// alone).
func TestSessionActionsFor(t *testing.T) {
	all := actionSeams{Clean: true, Restart: true, Label: true, New: true}
	labels := func(a sessionActions) []string {
		out := []string{}
		for _, r := range a.Rows {
			out = append(out, r.Label)
		}
		return out
	}
	tests := []struct {
		name string
		s    SessionView
		have actionSeams
		want []string
	}{
		{name: "stopped session", s: SessionView{Name: "a", Phase: "done"}, have: all, want: []string{"Restart…", "Label…", "Clean…"}},
		{name: "healthy running host", s: SessionView{Name: "a", HostRunning: true}, have: all, want: []string{"Label…", "Clean…"}},
		{name: "worktree problem", s: SessionView{Name: "a", Problem: "gone"}, have: all, want: []string{"Clean…"}},
		{name: "no seams", s: SessionView{Name: "a", Phase: "done"}, have: actionSeams{}, want: []string{}},
		{name: "only label", s: SessionView{Name: "a", Phase: "done"}, have: actionSeams{Label: true}, want: []string{"Label…"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := sessionActionsFor("app", tc.s, tc.have)
			if got := labels(a); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rows = %v, want %v", got, tc.want)
			}
			if len(a.Sheets) != len(a.Rows) {
				t.Fatalf("%d sheets for %d rows", len(a.Sheets), len(a.Rows))
			}
			for i, r := range a.Rows {
				if r.Sheet != a.Sheets[i].ID || r.Href != a.Sheets[i].Page {
					t.Errorf("row %q opens %q at %q, sheet is %q at %q", r.Label, r.Sheet, r.Href, a.Sheets[i].ID, a.Sheets[i].Page)
				}
				if r.Danger != (r.Label == "Clean…") {
					t.Errorf("row %q danger = %v", r.Label, r.Danger)
				}
			}
		})
	}
}

// actionPost is one POST of a session action, as a form and as JSON.
type actionPost struct {
	name, path, form, json string
	calls                  func(*rig) int
}

func actionPosts() []actionPost {
	return []actionPost{
		{name: "clean", path: "/p/app/sessions/finished/clean", form: "phase=done", json: `{"phase":"done"`, calls: func(rg *rig) int { return len(rg.cleanCalls) }},
		{name: "restart", path: "/p/app/sessions/finished/restart", form: "", json: `{`, calls: func(rg *rig) int { return len(rg.restartCalls) }},
		{name: "label", path: "/p/app/sessions/finished/label", form: "label=new", json: `{"label":"new"`, calls: func(rg *rig) int { return len(rg.labelCalls) }},
		{name: "new", path: "/p/app/sessions", form: "name=fix-login&description=Fix+it", json: `{"name":"fix-login","description":"Fix it"`, calls: func(rg *rig) int { return len(rg.newCalls) }},
	}
}

// withConfirm closes a JSON body, with the confirm or without it.
func withConfirm(open string, confirm bool) string {
	sep := ","
	if open == "{" {
		sep = ""
	}
	if confirm {
		return open + sep + `"confirm":true}`
	}
	if open == "{" {
		return "{}"
	}
	return open + "}"
}

// TestSessionActionNeedsTheConfirm is every action's POST without the
// confirm, as a form and as JSON: 400, "Nothing changed", and no seam
// called. Every row is load-bearing: an action reached without the confirm
// would clean, restart, relabel or start a session from one stray request.
// The "query does not count" rows prove an address someone was sent cannot
// carry the confirm with it.
func TestSessionActionNeedsTheConfirm(t *testing.T) {
	for _, ap := range actionPosts() {
		for _, c := range []struct {
			kind, path, ct, body string
		}{
			{"form", ap.path, formType, ap.form},
			{"json", "/api" + ap.path, "application/json", withConfirm(ap.json, false)},
			{"json confirm false", "/api" + ap.path, "application/json", strings.TrimSuffix(withConfirm(ap.json, true), `"confirm":true}`) + `"confirm":false}`},
			{"query does not count", ap.path + "?confirm=yes", formType, ap.form},
			{"confirm=no", ap.path, formType, strings.TrimPrefix(ap.form+"&confirm=no", "&")},
		} {
			t.Run(ap.name+" "+c.kind, func(t *testing.T) {
				rg := actionRig(t)
				rec := rg.do("POST", c.path, c.ct, c.body)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "Nothing changed") || !strings.Contains(rec.Body.String(), "needs the confirm") {
					t.Errorf("body does not say nothing changed for want of the confirm:\n%s", rec.Body.String())
				}
				if n := rg.sessionCalls(); n != 0 {
					t.Errorf("%d seam calls without the confirm (%s: %d)", n, ap.name, ap.calls(rg))
				}
				answer := "an error page"
				if strings.HasPrefix(c.path, "/api") {
					answer = strings.TrimSpace(rec.Body.String())
				}
				t.Logf("POST %s (%s) -> %d, %s seam calls: %d, answer: %s", c.path, c.kind, rec.Code, ap.name, ap.calls(rg), answer)
			})
		}
	}
}

// TestSessionActionNotFound: an unknown session, an unknown project and a
// missing seam are each a 404 that calls nothing, even with the confirm.
// Load-bearing: "no seam" (the T8.8 rule: an action the server cannot do is
// not there at all, not a 500 when pressed).
func TestSessionActionNotFound(t *testing.T) {
	for _, ap := range actionPosts() {
		// Starting a session names no existing one, so it has no such row.
		t.Run(ap.name+" unknown session", func(t *testing.T) {
			if ap.name == "new" {
				return
			}
			rg := actionRig(t)
			path := strings.Replace(ap.path, "/finished/", "/no-such/", 1)
			for _, p := range []string{path, "/api" + path} {
				rec := rg.do("POST", p, "application/json", withConfirm(ap.json, true))
				if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "There is no session with that name") {
					t.Errorf("%s: status %d\n%s", p, rec.Code, rec.Body.String())
				}
			}
			if n := rg.sessionCalls(); n != 0 {
				t.Errorf("%d seam calls for an unknown session", n)
			}
		})
		t.Run(ap.name+" unknown project", func(t *testing.T) {
			rg := actionRig(t)
			rec := rg.do("POST", "/api"+strings.Replace(ap.path, "/p/app/", "/p/nope/", 1), "application/json", withConfirm(ap.json, true))
			if rec.Code != http.StatusNotFound || rg.sessionCalls() != 0 {
				t.Errorf("status %d, %d calls", rec.Code, rg.sessionCalls())
			}
		})
		t.Run(ap.name+" no seam", func(t *testing.T) {
			rg := actionRig(t)
			switch ap.name {
			case "clean":
				rg.srv.d.CleanSession = nil
			case "restart":
				rg.srv.d.RestartSession = nil
			case "label":
				rg.srv.d.SetSessionLabel = nil
			case "new":
				rg.srv.d.NewSession = nil
			}
			for _, p := range []string{ap.path, "/api" + ap.path} {
				rec := rg.do("POST", p, "application/json", withConfirm(ap.json, true))
				if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "There is nothing at this address") {
					t.Errorf("%s: status %d\n%s", p, rec.Code, rec.Body.String())
				}
			}
			if n := rg.sessionCalls(); n != 0 {
				t.Errorf("%d seam calls", n)
			}
			page := rg.pageDo("GET", "/p/app/sessions/finished", "").Body.String() + rg.pageDo("GET", "/p/app/sessions", "").Body.String()
			gone := map[string]string{"clean": `id="confirm-clean"`, "restart": `id="confirm-restart"`, "label": `id="confirm-label"`, "new": `id="confirm-new"`}[ap.name]
			if strings.Contains(page, gone) {
				t.Errorf("the page still draws %s without its seam", gone)
			}
			if ap.name == "new" && strings.Contains(page, "Start a new autonomous session") {
				t.Errorf("the list still offers a new session without its seam")
			}
		})
	}
}

// TestSessionActionRefusedBeforeTheSeam is every request the page refuses on
// its own, with the confirm: no seam call, 400, and the reason in the
// result. Load-bearing: "restart a healthy host" and "restart a one-off"
// (an address typed by hand must not do what the page would not offer),
// "label too long" and the description rows (the dashboard's caps).
func TestSessionActionRefusedBeforeTheSeam(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	tests := []struct {
		name, path, body, want string
	}{
		{name: "restart a healthy host", path: "/api/p/app/sessions/busy/restart", body: `{"confirm":true}`, want: "Its host is running and its agent is working"},
		{name: "restart a one-off", path: "/api/p/app/sessions/scratchy/restart", body: `{"confirm":true}`, want: "A one-off session is restarted from afk dash"},
		{name: "restart the planning session", path: "/api/p/app/sessions/master-plan/restart", body: `{"confirm":true}`, want: "The planning session is restarted from afk dash"},
		{name: "restart a worktree problem", path: "/api/p/app/sessions/lost/restart", body: `{"confirm":true}`, want: "nothing to restart"},
		{name: "restart a stranded entry", path: "/api/p/app/sessions/stray/restart", body: `{"confirm":true}`, want: "not in a worktree of its own"},
		{name: "label a worktree problem", path: "/api/p/app/sessions/lost/label", body: `{"label":"x","confirm":true}`, want: "it has no label to set"},
		{name: "label too long", path: "/api/p/app/sessions/finished/label", body: `{"label":"` + long(129) + `","confirm":true}`, want: "The label is 129 characters long; the most is 128."},
		{name: "blank description", path: "/api/p/app/sessions", body: `{"name":"fix-login","description":"   ","confirm":true}`, want: "Say what the session should do."},
		{name: "description too long", path: "/api/p/app/sessions", body: `{"name":"fix-login","description":"` + long(5001) + `","confirm":true}`, want: "The description is 5001 characters long; the most is 5000."},
		{name: "no name", path: "/api/p/app/sessions", body: `{"description":"Fix it","confirm":true}`, want: "Give the session a name"},
		{name: "a name with capitals", path: "/api/p/app/sessions", body: `{"name":"Fix-Login","description":"Fix it","confirm":true}`, want: `"Fix-Login" cannot be a session name`},
		{name: "a name with a slash", path: "/api/p/app/sessions", body: `{"name":"../etc","description":"Fix it","confirm":true}`, want: "cannot be a session name"},
		{name: "a name starting with a dash", path: "/api/p/app/sessions", body: `{"name":"-x","description":"Fix it","confirm":true}`, want: "cannot be a session name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := actionRig(t)
			rec := rg.do("POST", tc.path, "application/json", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			var v SessionActionView
			if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
				t.Fatalf("body: %v\n%s", err, rec.Body.String())
			}
			if v.Project != "app" || v.Result.Outcome != SessionActionRefused || !strings.Contains(v.Result.Text, tc.want) {
				t.Errorf("answer = %+v, want refused saying %q", v, tc.want)
			}
			if n := rg.sessionCalls(); n != 0 {
				t.Errorf("%d seam calls", n)
			}
			// The same refusal as a page: the error page with the sentence.
			page := rg.pageDo("POST", strings.TrimPrefix(tc.path, "/api"), jsonToForm(t, tc.body))
			if page.Code != http.StatusBadRequest || !strings.Contains(page.Body.String(), "That didn't load") {
				t.Errorf("page: status %d\n%s", page.Code, page.Body.String())
			}
			if n := rg.sessionCalls(); n != 0 {
				t.Errorf("%d seam calls from the page", n)
			}
		})
	}
	t.Run("a label of 128 characters is sent", func(t *testing.T) {
		rg := actionRig(t)
		rec := rg.do("POST", "/api/p/app/sessions/finished/label", "application/json", `{"label":"`+long(128)+`","confirm":true}`)
		if rec.Code != http.StatusOK || len(rg.labelCalls) != 1 {
			t.Errorf("status %d, calls %v", rec.Code, rg.labelCalls)
		}
	})
}

// jsonToForm turns a flat JSON body into the form the page would post.
func jsonToForm(t *testing.T, body string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("jsonToForm: %v", err)
	}
	f := url.Values{}
	for k, v := range m {
		switch x := v.(type) {
		case bool:
			if x {
				f.Set(k, "yes")
			}
		case string:
			f.Set(k, x)
		}
	}
	return f.Encode()
}

// TestSessionActionOutcomes maps each seam answer to the page's and the
// API's. Load-bearing rows: the done redirects (each lands on the screen its
// toast belongs to, and reloading it posts nothing), "refused" and "failed"
// (the seam's own sentence reaches the owner, with 400 and 500), and "an
// outcome this page does not know" (a fault, never a success).
func TestSessionActionOutcomes(t *testing.T) {
	type outcome struct {
		res      SessionActionResult
		status   int
		location string
	}
	refused := SessionActionResult{Outcome: SessionActionRefused, Text: "It changed since you looked."}
	failed := SessionActionResult{Outcome: SessionActionFailed, Text: "The disk is full."}
	odd := SessionActionResult{Outcome: "maybe", Text: "?"}
	for _, ap := range actionPosts() {
		dones := map[string]string{
			"clean":   "/p/app/sessions?done=cleaned&session=finished",
			"restart": "/p/app/sessions/finished?done=restarted",
			"label":   "/p/app/sessions/finished?done=labelled",
			"new":     "/p/app/sessions/fix-login?done=started",
		}
		for name, o := range map[string]outcome{
			"done":    {res: SessionActionResult{Outcome: SessionActionDone, Text: "Done."}, status: http.StatusSeeOther, location: dones[ap.name]},
			"refused": {res: refused, status: http.StatusBadRequest},
			"failed":  {res: failed, status: http.StatusInternalServerError},
			"odd":     {res: odd, status: http.StatusInternalServerError},
		} {
			t.Run(ap.name+" "+name, func(t *testing.T) {
				rg := actionRig(t)
				rg.cleanResult, rg.restartResult, rg.labelResult, rg.newResult = o.res, o.res, o.res, o.res
				page := rg.pageDo("POST", ap.path, strings.TrimPrefix(ap.form+"&confirm=yes", "&"))
				if page.Code != o.status {
					t.Fatalf("page status = %d, want %d\n%s", page.Code, o.status, page.Body.String())
				}
				if got := page.Header().Get("Location"); got != o.location {
					t.Errorf("Location = %q, want %q", got, o.location)
				}
				body := page.Body.String()
				switch name {
				case "refused", "failed":
					if !strings.Contains(body, o.res.Text) {
						t.Errorf("the page does not show the seam's sentence:\n%s", body)
					}
				case "odd":
					if !strings.Contains(body, "This is a fault in afk") {
						t.Errorf("an unknown outcome is not shown as a fault:\n%s", body)
					}
				}
				if ap.calls(rg) != 1 {
					t.Errorf("seam called %d times, want 1", ap.calls(rg))
				}

				api := rg.do("POST", "/api"+ap.path, "application/json", withConfirm(ap.json, true))
				wantAPI := o.status
				if wantAPI == http.StatusSeeOther {
					wantAPI = http.StatusOK
				}
				if api.Code != wantAPI {
					t.Fatalf("api status = %d, want %d\n%s", api.Code, wantAPI, api.Body.String())
				}
				m := decode(t, api)
				wantKeys(t, m, "project", "session", "result")
				var v SessionActionView
				must(t, json.Unmarshal(api.Body.Bytes(), &v))
				wantName := "finished"
				if ap.name == "new" {
					wantName = "fix-login"
				}
				wantOutcome := o.res.Outcome
				if name == "odd" {
					wantOutcome = SessionActionFailed
				}
				if v.Project != "app" || v.Session != wantName || v.Result.Outcome != wantOutcome {
					t.Errorf("api answer = %+v", v)
				}
			})
		}
	}
}

// TestSessionActionSeamArguments is what each seam is given: the project's
// root, the session's name from the address, and the posted value trimmed,
// never a folder from the request. Load-bearing: "clean" (the phase shown
// travels with it, so a session that moved on is refused behind the seam)
// and "new ignores a posted worktree" (no request field picks the folder).
func TestSessionActionSeamArguments(t *testing.T) {
	rg := actionRig(t)
	rg.do("POST", "/api/p/app/sessions/finished/clean", "application/json", `{"phase":"done","worktree":"/etc","confirm":true}`)
	rg.pageDo("POST", "/p/app/sessions/stuck/restart", "confirm=yes&worktree=/etc")
	rg.do("POST", "/api/p/app/sessions/finished/label", "application/json", `{"label":"  new label  ","confirm":true}`)
	rg.pageDo("POST", "/p/app/sessions/finished/label", "label=&confirm=yes")
	rg.pageDo("POST", "/p/app/sessions", "name=+fix-login+&description=+Fix+it+&worktree=/etc&base=main&unattended=yes&confirm=yes")
	want := map[string][][]string{
		"clean":   {{rg.root, "finished", "done"}},
		"restart": {{rg.root, "stuck"}},
		"label":   {{rg.root, "finished", "new label"}, {rg.root, "finished", ""}},
		"new":     {{rg.root, "fix-login", "Fix it"}},
	}
	got := map[string][][]string{"clean": rg.cleanCalls, "restart": rg.restartCalls, "label": rg.labelCalls, "new": rg.newCalls}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v\nwant    %v", got, want)
	}
}

// TestSessionActionPages is what the session screen and the list draw.
// Load-bearing: the sheets sit outside the live regions with the safe button
// autofocused, each row links to its no-JS page and names its sheet, and
// Clean is drawn in danger text.
func TestSessionActionPages(t *testing.T) {
	t.Run("session screen", func(t *testing.T) {
		rg := actionRig(t)
		body := rg.pageDo("GET", "/p/app/sessions/stuck", "").Body.String()
		for _, w := range []string{
			`<h2 class="section-head__title" id="actions-title">Actions</h2>`,
			`<a class="row" href="/p/app/sessions/stuck/confirm/restart" data-confirm="confirm-restart">`,
			`<a class="row" href="/p/app/sessions/stuck/confirm/label" data-confirm="confirm-label">`,
			`<a class="row" href="/p/app/sessions/stuck/confirm/clean" data-confirm="confirm-clean">`,
			`<span class="row__title action--danger">Clean…</span>`,
			`<dialog class="confirm-sheet" id="confirm-clean"`,
			`Clean session &#34;stuck&#34; (phase: executing)?`,
			`<input type="hidden" name="phase" value="executing">`,
			`action="/p/app/sessions/stuck/clean" id="confirm-clean-form" class="stack" data-enhance>`,
			`class="btn btn--danger" data-loading-label="Cleaning">Clean session</button>`,
			`<button type="submit" class="btn btn--secondary" autofocus>Keep it</button>`,
			`Replaces its child process and disconnects any attached client. The agent resumes its conversation.`,
			`data-loading-label="Restarting">Restart</button>`,
			`<input class="input" type="text" id="confirm-label-input" name="label" value="" maxlength="128"`,
			`<span class="field__hint" id="confirm-label-hint">blank clears it</span>`,
		} {
			if !strings.Contains(body, w) {
				t.Errorf("missing %q", w)
			}
		}
		// Every sheet comes after both live regions close.
		lastLive := strings.LastIndex(body, `data-live=`)
		if i := strings.Index(body, "<dialog"); i < lastLive {
			t.Errorf("a sheet is drawn before the last live region")
		}
		if n := strings.Count(body, "autofocus"); n != 3 {
			t.Errorf("%d autofocus attributes, want one safe button per sheet (3)", n)
		}
	})
	t.Run("healthy host offers no restart", func(t *testing.T) {
		rg := actionRig(t)
		body := rg.pageDo("GET", "/p/app/sessions/busy", "").Body.String()
		if strings.Contains(body, "confirm-restart") {
			t.Errorf("a healthy host is offered a restart")
		}
		if !strings.Contains(body, "Its host is running, so it is stopped first.") {
			t.Errorf("the clean sheet does not say the running host is stopped first")
		}
	})
	t.Run("list", func(t *testing.T) {
		rg := actionRig(t)
		body := rg.pageDo("GET", "/p/app/sessions", "").Body.String()
		for _, w := range []string{
			`<details class="card sessions__new">`,
			`<summary>Start a new autonomous session&hellip;</summary>`,
			`<form method="get" action="/p/app/sessions/new" class="stack" data-confirm-form="confirm-new">`,
			`<input class="input input--mono" type="text" id="new-session-name" name="name" value="" required pattern=`,
			`<label class="field__label" for="new-session-description">What should it do?</label>`,
			`<textarea class="textarea" id="new-session-description" name="description" required maxlength="5000"`,
			`<button type="submit" class="btn btn--primary">Start session</button>`,
			`<dialog class="confirm-sheet" id="confirm-new"`,
			`data-fill-text="Start autonomous session &#34;{name}&#34;?"`,
			`<input type="hidden" name="name" value="" data-fill-field="name">`,
			`<input type="hidden" name="description" value="" data-fill-field="description">`,
			`<p class="session-quote" data-fill-text="{description}">`,
			`action="/p/app/sessions" id="confirm-new-form" class="stack" data-enhance>`,
			`<button type="submit" class="btn btn--secondary" autofocus>Don&#39;t start</button>`,
		} {
			if !strings.Contains(body, w) {
				t.Errorf("missing %q", w)
			}
		}
		live := strings.Index(body, `id="sessions-list"`)
		if form := strings.Index(body, `data-confirm-form`); form > live {
			t.Errorf("the new-session form is inside the live list")
		}
	})
	t.Run("the plan page holds no session action", func(t *testing.T) {
		rg := actionRig(t)
		body := rg.pageDo("GET", "/p/app/plan", "").Body.String()
		for _, w := range []string{"confirm-new", "confirm-clean", "Start session"} {
			if strings.Contains(body, w) {
				t.Errorf("the plan page holds %q", w)
			}
		}
	})
}

// TestSessionConfirmPages is the no-JS confirm pages. Load-bearing rows:
// "restart not offered" (the page explains and offers no form, as the POST
// would refuse), "new, invalid" (the form comes back holding what was typed,
// with what to fix) and "new, valid" (the POST form carries the values and
// the confirm; nothing has started yet).
func TestSessionConfirmPages(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		status  int
		want    []string
		notWant []string
	}{
		{name: "clean", path: "/p/app/sessions/finished/confirm/clean", status: 200, want: []string{
			`<h1>Clean session &#34;finished&#34; (phase: done)?</h1>`, `<p>Worktree goes; archive kept. The branch is kept.</p>`,
			`<form method="post" action="/p/app/sessions/finished/clean" class="stack">`, `<input type="hidden" name="confirm" value="yes">`,
			`<input type="hidden" name="phase" value="done">`, `<button type="submit" class="btn btn--danger">Clean session</button>`,
			`<a class="btn btn--secondary" href="/p/app/sessions/finished" autofocus>Keep it</a>`,
		}},
		{name: "restart", path: "/p/app/sessions/stuck/confirm/restart", status: 200, want: []string{`<h1>Restart &#34;stuck&#34;?</h1>`, `action="/p/app/sessions/stuck/restart"`}},
		{name: "restart not offered", path: "/p/app/sessions/busy/confirm/restart", status: 200,
			want:    []string{`<div class="notice notice--error" role="alert"><div class="notice__body">Its host is running and its agent is working, so there is nothing to restart.</div></div>`, `<a class="btn btn--secondary" href="/p/app/sessions/busy">Back</a>`},
			notWant: []string{`action="/p/app/sessions/busy/restart"`}},
		{name: "label", path: "/p/app/sessions/finished/confirm/label", status: 200, want: []string{
			`<h1>Label for finished:</h1>`, `<input class="input" type="text" id="label-input" name="label" value="login fix" maxlength="128"`,
			`<span class="field__hint" id="label-hint">blank clears it</span>`, `<button type="submit" class="btn btn--primary">Save label</button>`,
		}},
		{name: "unknown action", path: "/p/app/sessions/finished/confirm/explode", status: 404},
		{name: "unknown session", path: "/p/app/sessions/no-such/confirm/clean", status: 404, want: []string{"There is no session with that name"}},
		{name: "new, empty", path: "/p/app/sessions/new", status: 200, want: []string{`<h1>Start a new autonomous session</h1>`, `<form method="get" action="/p/app/sessions/new" class="stack">`},
			notWant: []string{`method="post"`, "notice--error"}},
		{name: "new, invalid", path: "/p/app/sessions/new?name=Bad+Name&description=Do+%3Cb%3Eit%3C%2Fb%3E", status: 200, want: []string{
			`&#34;Bad Name&#34; cannot be a session name`, `name="name" value="Bad Name"`, `Do &lt;b&gt;it&lt;/b&gt;</textarea>`,
		}, notWant: []string{`method="post"`, "<b>it</b>"}},
		{name: "new, valid", path: "/p/app/sessions/new?name=fix-login&description=Fix+the+form%0Aand+test+it", status: 200, want: []string{
			`<h1>Start autonomous session &#34;fix-login&#34;?</h1>`,
			`<p>It works on its own, from plan to reviewed commits on branch fix-login, with nobody asked along the way.</p>`,
			`<p class="session-quote">Fix the form` + "\n" + `and test it</p>`,
			`<form method="post" action="/p/app/sessions" class="stack">`, `<input type="hidden" name="name" value="fix-login">`,
			`<button type="submit" class="btn btn--primary">Start session</button>`, `href="/p/app/sessions" autofocus>Don&#39;t start</a>`,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := actionRig(t)
			rec := rg.pageDo("GET", tc.path, "")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("holds %q", w)
				}
			}
			if rg.sessionCalls() != 0 {
				t.Errorf("a confirm page called a seam")
			}
		})
	}
	t.Run("no seam is not found", func(t *testing.T) {
		rg := actionRig(t)
		rg.srv.d.CleanSession, rg.srv.d.NewSession = nil, nil
		for _, p := range []string{"/p/app/sessions/finished/confirm/clean", "/p/app/sessions/new?name=a&description=b"} {
			if rec := rg.pageDo("GET", p, ""); rec.Code != http.StatusNotFound {
				t.Errorf("%s: status %d", p, rec.Code)
			}
		}
	})
	t.Run("no api twin", func(t *testing.T) {
		rg := actionRig(t)
		if rec := rg.do("GET", "/api/p/app/sessions/finished/confirm/clean", "", ""); rec.Code != http.StatusNotFound {
			t.Errorf("status %d", rec.Code)
		}
	})
}

// TestSessionToasts is the toasts the actions lead to. Load-bearing rows:
// "cleaned, still listed" (a name still on the list was not cleaned, so it is
// not named), "cleaned, odd name" (the query is never echoed unless it is a
// plain name), and "a session code on the project page" (toastFor shows
// none of them anywhere else).
func TestSessionToasts(t *testing.T) {
	tests := []struct {
		name, path, want string
	}{
		{name: "cleaned", path: "/p/app/sessions?done=cleaned&session=gone-now", want: "Cleaned gone-now."},
		{name: "cleaned, still listed", path: "/p/app/sessions?done=cleaned&session=finished", want: "Session cleaned."},
		{name: "cleaned, odd name", path: "/p/app/sessions?done=cleaned&session=%3Cb%3EHi", want: "Session cleaned."},
		{name: "restarted", path: "/p/app/sessions/stuck?done=restarted", want: "Restarted stuck."},
		{name: "labelled", path: "/p/app/sessions/stuck?done=labelled", want: "Label saved."},
		{name: "started", path: "/p/app/sessions/finished?done=started", want: "Started finished. It works on its own; open its terminal from afk dash or afk attach."},
		{name: "cleaned on a session's screen", path: "/p/app/sessions/stuck?done=cleaned", want: ""},
		{name: "restarted on the list", path: "/p/app/sessions?done=restarted", want: ""},
		{name: "a session code on the project page", path: "/p/app?done=started", want: ""},
		{name: "a session code on the inbox", path: "/p/app/inbox?done=cleaned&session=x", want: ""},
		{name: "a project code on the list", path: "/p/app/sessions?done=paused", want: ""},
	}
	toast := regexp.MustCompile(`<div class="toast" role="status" data-toast>([^<]*)</div>`)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := actionRig(t)
			body := rg.pageDo("GET", tc.path, "").Body.String()
			got := ""
			if m := toast.FindStringSubmatch(body); m != nil {
				got = m[1]
			}
			if got != tc.want {
				t.Errorf("toast = %q, want %q", got, tc.want)
			}
		})
	}
	for _, code := range []string{"cleaned", "restarted", "labelled", "started"} {
		if got := toastFor(code, "app", "T1.2"); got != "" {
			t.Errorf("toastFor(%q) = %q, want none", code, got)
		}
	}
}

// TestSessionSheetScript checks confirm.js, as served, still reads the hooks
// the new-session sheet relies on, so a rename on one side shows here.
func TestSessionSheetScript(t *testing.T) {
	rg := actionRig(t)
	body := rg.pageDo("GET", "/p/app/sessions", "").Body.String()
	m := regexp.MustCompile(`/static/[0-9a-f]{12}/confirm\.js`).FindString(body)
	if m == "" {
		t.Fatalf("the list does not load confirm.js")
	}
	js := rg.pageDo("GET", m, "").Body.String()
	for _, hook := range []string{"data-confirm-form", "data-fill-field", "data-fill-text", `\{(name|description)\}`, "showModal", "textContent"} {
		if !strings.Contains(js, hook) {
			t.Errorf("confirm.js does not read %q", hook)
		}
	}
}
