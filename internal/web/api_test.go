package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
)

// testToken is long and unusual so that "the token is not in the body" can
// be checked with a plain substring search.
const testToken = "tok-4f9a1c77d2e84b0a9c3d5e6f70812345"

// The inbox ids the rig hands out, in order.
const (
	idBlocking = "abc234"
	idOptions  = "def567"
	idAnswered = "ghj892"
)

var testNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func dump(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(b)
}

// rig is one handler over two temp-folder projects with fake dependencies.
// Nothing here opens a port, runs a command or reads a real project.
type rig struct {
	t       *testing.T
	h       http.Handler
	root    string // project "app"
	other   string // project "my app", which has nothing in it
	tasks   []masterplan.Task
	ledger  ledger.Ledger
	loadErr error
	usage   usagestate.File
	usageOK bool
	// pauseErr and resumeErr make the injected functions fail.
	pauseErr  error
	resumeErr error
	paused    []string
	resumed   []string
	sessions  []SessionView
}

func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	rg := &rig{t: t, root: t.TempDir(), other: t.TempDir()}
	rg.tasks = planTasks("T1.1", "T1.2", "T1.3")
	rg.ledger = ledger.Ledger{Tasks: []ledger.Entry{
		{ID: "T1.1", Status: ledger.Done, FinishedAt: "2026-10-12T10:00:00Z", PR: 3, MergedSHA: "cafe01"},
		{ID: "T1.2", Status: ledger.Running, Session: "t1-2"},
	}}
	rg.sessions = []SessionView{{Name: "t1-2", Phase: "executing", HostRunning: true}}
	reset := testNow.Add(2 * time.Hour)
	rg.usage = usagestate.File{
		At:             testNow.Add(-5 * time.Minute),
		Provider:       "stored-provider-word",
		Windows:        []usagestate.Window{{Kind: "session", UsedPct: 41.5, ResetsAt: &reset}, {Kind: "weekly", UsedPct: 12, Scope: "all"}},
		OverageEnabled: true,
		Source:         "probe",
		Governor:       &usagestate.Governor{Action: "run", Reason: "plenty left", At: testNow.Add(-5 * time.Minute)},
	}
	rg.usageOK = true

	if cfg.Token == "" {
		cfg.Token = testToken
	}
	h, err := New(cfg, Deps{
		Projects: []Project{{Name: "app", Root: rg.root}, {Name: "my app", Root: rg.other}},
		LoadBuild: func(root string) ([]masterplan.Task, ledger.Ledger, error) {
			if root != rg.root {
				return nil, ledger.Ledger{}, nil
			}
			return rg.tasks, rg.ledger, rg.loadErr
		},
		Sessions: func(root string) []SessionView {
			if root != rg.root {
				return nil
			}
			return rg.sessions
		},
		Usage: func() (usagestate.File, bool) { return rg.usage, rg.usageOK },
		// The fakes do the real file change, so the summary that comes
		// back proves it was read after the call and not before.
		Pause: func(root string) error {
			rg.paused = append(rg.paused, root)
			if rg.pauseErr != nil {
				return rg.pauseErr
			}
			return daemonstate.Pause(root, testNow)
		},
		Resume: func(root string) error {
			rg.resumed = append(rg.resumed, root)
			if rg.resumeErr != nil {
				return rg.resumeErr
			}
			_, err := daemonstate.Resume(root)
			return err
		},
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rg.h = h
	return rg
}

// seedInbox adds three items to project "app": a blocking question, a
// question with options, and one that is already answered.
func (rg *rig) seedInbox() {
	rg.t.Helper()
	ids := []string{idBlocking, idOptions, idAnswered}
	st := inbox.Store{Dir: inbox.Dir(rg.root), Now: func() time.Time { return testNow.Add(-time.Hour) }, NewID: func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}}
	for _, n := range []inbox.NewItem{
		{Task: "T1.2", Session: "t1-2", Kind: "question", Blocking: true, Title: "Which database?", Body: "Pick one."},
		{Kind: "question", Title: "Tabs or spaces?", Options: []string{"tabs", "spaces"}, Recommended: "tabs"},
		{Kind: "notice", Title: "Heads up"},
	} {
		if _, err := st.Add(n); err != nil {
			rg.t.Fatalf("seeding the inbox: %v", err)
		}
	}
	if _, err := st.Answer(idAnswered, "seen", inbox.ViaCLI); err != nil {
		rg.t.Fatalf("answering the seeded item: %v", err)
	}
}

// do sends one request with the bearer token and returns the recorder.
func (rg *rig) do(method, path, contentType, body string) *httptest.ResponseRecorder {
	rg.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not a JSON object: %v\n%s", err, rec.Body.String())
	}
	return m
}

func wantKeys(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing from %s", k, dump(m))
		}
	}
}

const (
	formType = "application/x-www-form-urlencoded"
	jsonType = "application/json"
)

// TestAPIGets asks each GET route for its JSON and checks the keys a reader
// relies on are there, with values that prove the data came from the fakes.
//
// Load-bearing rows: "usage with no reading" (known must be false, with an
// empty array and no year-1 time, so a reader cannot take it for a reading of
// zero) and "project name with a space" (the name arrives escaped in the
// address and must still match).
func TestAPIGets(t *testing.T) {
	summaryKeys := []string{"name", "problem", "paused", "stopped", "current", "done", "total", "last_ship", "state", "open_items", "open_blocking"}

	tests := []struct {
		name  string
		setup func(*rig)
		path  string
		check func(*testing.T, map[string]any)
	}{
		{
			name: "overview",
			path: "/api/",
			check: func(t *testing.T, m map[string]any) {
				wantKeys(t, m, "projects", "usage")
				ps := m["projects"].([]any)
				if len(ps) != 2 {
					t.Fatalf("projects = %d, want 2", len(ps))
				}
				p := ps[0].(map[string]any)
				wantKeys(t, p, summaryKeys...)
				if p["name"] != "app" || p["done"] != 1.0 || p["total"] != 3.0 || p["open_items"] != 2.0 || p["open_blocking"] != 1.0 {
					t.Errorf("first project = %s", dump(p))
				}
				cur := p["current"].(map[string]any)
				wantKeys(t, cur, "task", "title", "status", "session", "phase", "waiting_on")
				if cur["task"] != "T1.2" || cur["phase"] != "executing" {
					t.Errorf("current = %s", dump(cur))
				}
				ls := p["last_ship"].(map[string]any)
				wantKeys(t, ls, "task", "pr", "merged_sha", "finished_at", "stopped", "text")
				if ls["text"] != "T1.1 shipped 2 hours ago" {
					t.Errorf("last_ship = %s", dump(ls))
				}
				if _, ok := p["inbox"]; ok {
					t.Errorf("the overview should not carry each project's inbox")
				}
				if m["usage"].(map[string]any)["known"] != true {
					t.Errorf("usage = %s", dump(m["usage"]))
				}
			},
		},
		{
			name: "usage",
			path: "/api/usage",
			check: func(t *testing.T, m map[string]any) {
				wantKeys(t, m, "known", "at", "provider", "windows", "overage_enabled", "source", "governor")
				if m["known"] != true || m["provider"] != "stored-provider-word" || m["source"] != "probe" || m["overage_enabled"] != true {
					t.Errorf("usage = %s", dump(m))
				}
				ws := m["windows"].([]any)
				if len(ws) != 2 {
					t.Fatalf("windows = %d, want 2", len(ws))
				}
				w0 := ws[0].(map[string]any)
				if w0["kind"] != "session" || w0["used_pct"] != 41.5 || w0["resets_at"] != "2026-10-12T14:00:00Z" {
					t.Errorf("first window = %s", dump(w0))
				}
				if ws[1].(map[string]any)["scope"] != "all" {
					t.Errorf("second window = %s", dump(ws[1]))
				}
				g := m["governor"].(map[string]any)
				wantKeys(t, g, "action", "reason", "at")
				if g["action"] != "run" || g["reason"] != "plenty left" {
					t.Errorf("governor = %s", dump(g))
				}
			},
		},
		{
			name:  "usage with no reading",
			setup: func(rg *rig) { rg.usageOK = false },
			path:  "/api/usage",
			check: func(t *testing.T, m map[string]any) {
				if m["known"] != false {
					t.Errorf("known = %v, want false", m["known"])
				}
				if ws, ok := m["windows"].([]any); !ok || len(ws) != 0 {
					t.Errorf("windows = %v, want an empty array", m["windows"])
				}
				if _, ok := m["at"]; ok {
					t.Errorf("at = %v, want it left out", m["at"])
				}
				if m["governor"] != nil {
					t.Errorf("governor = %v, want null", m["governor"])
				}
			},
		},
		{
			name: "usage with a wake time",
			setup: func(rg *rig) {
				wake := testNow.Add(time.Hour)
				rg.usage.Governor = &usagestate.Governor{Action: "pause", Reason: "window nearly used", WakeAt: &wake, At: testNow}
			},
			path: "/api/usage",
			check: func(t *testing.T, m map[string]any) {
				g := m["governor"].(map[string]any)
				if g["action"] != "pause" || g["wake_at"] != "2026-10-12T13:00:00Z" || g["at"] != "2026-10-12T12:00:00Z" {
					t.Errorf("governor = %s", dump(g))
				}
			},
		},
		{
			name: "project",
			path: "/api/p/app",
			check: func(t *testing.T, m map[string]any) {
				wantKeys(t, m, append(summaryKeys, "inbox")...)
				if m["name"] != "app" {
					t.Errorf("name = %v", m["name"])
				}
				box := m["inbox"].(map[string]any)
				wantKeys(t, box, "items", "unreadable", "problem")
				if len(box["items"].([]any)) != 3 {
					t.Errorf("inbox = %s", dump(box))
				}
			},
		},
		{
			name: "project name with a space",
			path: "/api/p/my%20app",
			check: func(t *testing.T, m map[string]any) {
				if m["name"] != "my app" || m["total"] != 0.0 || m["current"] != nil {
					t.Errorf("project = %s", dump(m))
				}
				if items := m["inbox"].(map[string]any)["items"].([]any); len(items) != 0 {
					t.Errorf("items = %v, want an empty array", items)
				}
			},
		},
		{
			name: "project with daemon state, pause flag and stop marker",
			setup: func(rg *rig) {
				must(rg.t, daemonstate.Pause(rg.root, testNow))
				must(rg.t, daemonstate.Stop(rg.root, "T1.2", "the deploy check timed out", testNow))
				must(rg.t, daemonstate.WriteState(rg.root, daemonstate.State{At: "2026-10-12T11:59:00Z", Kind: "wait", Task: "T1.2", Session: "t1-2", Reason: "the line is stopped"}))
			},
			path: "/api/p/app",
			check: func(t *testing.T, m map[string]any) {
				if m["paused"] != true {
					t.Errorf("paused = %v", m["paused"])
				}
				st := m["stopped"].(map[string]any)
				if st["task"] != "T1.2" || st["reason"] != "the deploy check timed out" {
					t.Errorf("stopped = %s", dump(st))
				}
				state := m["state"].(map[string]any)
				wantKeys(t, state, "kind", "reason", "at")
				if state["kind"] != "wait" || state["reason"] != "the line is stopped" || state["at"] != "2026-10-12T11:59:00Z" {
					t.Errorf("state = %s", dump(state))
				}
				if ls := m["last_ship"].(map[string]any); ls["stopped"] != true || ls["task"] != "T1.2" {
					t.Errorf("last_ship = %s", dump(ls))
				}
			},
		},
		{
			name: "inbox list, in the store's order, counting unreadable files",
			setup: func(rg *rig) {
				must(rg.t, os.WriteFile(filepath.Join(inbox.Dir(rg.root), "zzz999.json"), []byte("{not json"), 0o644))
			},
			path: "/api/p/app/inbox",
			check: func(t *testing.T, m map[string]any) {
				wantKeys(t, m, "items", "unreadable", "problem")
				if m["unreadable"] != 1.0 {
					t.Errorf("unreadable = %v, want 1", m["unreadable"])
				}
				var got []string
				for _, it := range m["items"].([]any) {
					got = append(got, it.(map[string]any)["id"].(string))
				}
				// Open and blocking, then open, then answered.
				if want := []string{idBlocking, idOptions, idAnswered}; strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("order = %v, want %v", got, want)
				}
			},
		},
		{
			name: "item",
			path: "/api/p/app/inbox/" + idOptions,
			check: func(t *testing.T, m map[string]any) {
				// The item exactly as stored: all fourteen keys, no more.
				keys := []string{"id", "created_at", "task", "session", "kind", "blocking", "title", "body", "options", "recommended", "assumed", "answer", "answered_at", "answered_via"}
				wantKeys(t, m, keys...)
				if len(m) != len(keys) {
					t.Errorf("item has %d keys, want %d: %s", len(m), len(keys), dump(m))
				}
				if m["title"] != "Tabs or spaces?" || m["recommended"] != "tabs" || len(m["options"].([]any)) != 2 {
					t.Errorf("item = %s", dump(m))
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.do(http.MethodGet, tc.path, "", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			tc.check(t, decode(t, rec))
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestAPIErrors covers the refusals that are not about the token.
//
// Load-bearing rows: the bad-id rows (an id from the address must never be
// used as a path: "../x" style values get the same 404 as a missing item)
// and "inbox folder cannot be read" (the inbox route fails loudly, because an
// empty list there would read as "nothing needs you").
func TestAPIErrors(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*rig)
		method     string
		path       string
		ctype      string
		body       string
		wantStatus int
	}{
		{name: "unknown project", method: "GET", path: "/api/p/nope", wantStatus: 404},
		{name: "unknown project, inbox", method: "GET", path: "/api/p/nope/inbox", wantStatus: 404},
		{name: "unknown project, item", method: "GET", path: "/api/p/nope/inbox/" + idBlocking, wantStatus: 404},
		{name: "unknown project, answer", method: "POST", path: "/api/p/nope/inbox/" + idBlocking + "/answer", ctype: formType, body: "answer=x", wantStatus: 404},
		{name: "unknown project, pause", method: "POST", path: "/api/p/nope/pause", wantStatus: 404},
		{name: "unknown project, resume", method: "POST", path: "/api/p/nope/resume", wantStatus: 404},
		{name: "item id not shaped like an id", method: "GET", path: "/api/p/app/inbox/NOTANID", wantStatus: 404},
		{name: "item id that is a path", method: "GET", path: "/api/p/app/inbox/..%2F..%2Fx", wantStatus: 404},
		{name: "item that does not exist", method: "GET", path: "/api/p/app/inbox/zzz222", wantStatus: 404},
		{name: "answer to a bad id", method: "POST", path: "/api/p/app/inbox/NOTANID/answer", ctype: formType, body: "answer=x", wantStatus: 404},
		{name: "answer to a missing item", method: "POST", path: "/api/p/app/inbox/zzz222/answer", ctype: formType, body: "answer=x", wantStatus: 404},
		{
			name: "damaged item file",
			setup: func(rg *rig) {
				must(rg.t, os.WriteFile(filepath.Join(inbox.Dir(rg.root), "zzz999.json"), []byte("{"), 0o644))
			},
			method: "GET", path: "/api/p/app/inbox/zzz999", wantStatus: 500,
		},
		{
			name: "inbox folder cannot be read",
			setup: func(rg *rig) {
				// A file where the folder should be: reading it as a
				// folder fails for any user, root included.
				must(rg.t, os.RemoveAll(inbox.Dir(rg.root)))
				must(rg.t, os.WriteFile(inbox.Dir(rg.root), []byte("x"), 0o644))
			},
			method: "GET", path: "/api/p/app/inbox", wantStatus: 500,
		},
		// The pause route never reads its body, so only the declared
		// length can refuse this one; nothing may be paused by it.
		{name: "pause with a body over 64 KB", method: "POST", path: "/api/p/app/pause", ctype: formType, body: strings.Repeat("a", maxBody+1), wantStatus: 413},
		{name: "no such address", method: "GET", path: "/api/nothing-here", wantStatus: 404},
		{name: "wrong method for an address", method: "POST", path: "/api/usage", wantStatus: 404},
		{name: "pause with GET changes nothing", method: "GET", path: "/api/p/app/pause", wantStatus: 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.do(tc.method, tc.path, tc.ctype, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			m := decode(t, rec)
			msg, _ := m["error"].(string)
			if !strings.HasSuffix(msg, ".") || len(msg) < 20 {
				t.Errorf("error = %q, want a plain sentence", msg)
			}
			if len(rg.paused)+len(rg.resumed) != 0 {
				t.Errorf("pause or resume was called: %v %v", rg.paused, rg.resumed)
			}
		})
	}
}

// TestProjectShowsInboxProblem: the project route keeps answering when the
// inbox cannot be read, with the reason where the list would be. It is the
// counterpart of the "inbox folder cannot be read" row above.
func TestProjectShowsInboxProblem(t *testing.T) {
	rg := newRig(t, Config{})
	must(t, os.MkdirAll(filepath.Dir(inbox.Dir(rg.root)), 0o755))
	must(t, os.WriteFile(inbox.Dir(rg.root), []byte("x"), 0o644))

	rec := rg.do(http.MethodGet, "/api/p/app", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	m := decode(t, rec)
	box := m["inbox"].(map[string]any)
	if p, _ := box["problem"].(string); !strings.Contains(p, "could not be read") {
		t.Errorf("inbox problem = %q", p)
	}
	if m["current"] == nil {
		t.Errorf("the rest of the project should still be shown: %s", dump(m))
	}
}

// TestAnswer covers the answer route.
//
// Load-bearing rows:
//   - "form answer" and "JSON answer": the stored file says answered_via web.
//   - "second answer": 409, the first answer kept on disk, and the stored
//     item returned so the caller can show the answer that stands.
//   - the three "over 64 KB" rows: a declared length, an undeclared one and a
//     JSON body each take a different path to the same refusal, and none may
//     store anything.
//   - "answer in the address is ignored": only the body counts.
func TestAnswer(t *testing.T) {
	big := strings.Repeat("a", maxBody+1)
	answerPath := "/api/p/app/inbox/" + idBlocking + "/answer"

	tests := []struct {
		name       string
		path       string
		ctype      string
		body       string
		noLength   bool
		twice      bool
		wantStatus int
		wantAnswer string // what the file holds afterwards
		wantVia    string
	}{
		{name: "form answer", path: answerPath, ctype: formType, body: "answer=Use+Postgres", wantStatus: 200, wantAnswer: "Use Postgres", wantVia: "web"},
		{name: "JSON answer", path: answerPath, ctype: jsonType, body: `{"answer": "Use Postgres"}`, wantStatus: 200, wantAnswer: "Use Postgres", wantVia: "web"},
		{name: "JSON answer with a charset", path: answerPath, ctype: "application/json; charset=utf-8", body: `{"answer": "Use Postgres"}`, wantStatus: 200, wantAnswer: "Use Postgres", wantVia: "web"},
		{name: "answer is trimmed", path: answerPath, ctype: formType, body: "answer=++yes++", wantStatus: 200, wantAnswer: "yes", wantVia: "web"},
		{name: "a number picks the option", path: "/api/p/app/inbox/" + idOptions + "/answer", ctype: formType, body: "answer=2", wantStatus: 200, wantAnswer: "spaces", wantVia: "web"},
		{name: "empty form answer", path: answerPath, ctype: formType, body: "answer=", wantStatus: 400},
		{name: "blank form answer", path: answerPath, ctype: formType, body: "answer=+++", wantStatus: 400},
		{name: "no answer field", path: answerPath, ctype: formType, body: "other=x", wantStatus: 400},
		{name: "empty JSON answer", path: answerPath, ctype: jsonType, body: `{"answer": ""}`, wantStatus: 400},
		{name: "JSON that does not parse", path: answerPath, ctype: jsonType, body: `{"answer": `, wantStatus: 400},
		{name: "no content type", path: answerPath, body: "answer=x", wantStatus: 400},
		{name: "answer in the address is ignored", path: answerPath + "?answer=from-the-address", ctype: formType, body: "", wantStatus: 400},
		{name: "second answer", path: answerPath, ctype: formType, body: "answer=first", twice: true, wantStatus: 409, wantAnswer: "first", wantVia: "web"},
		{name: "already answered elsewhere", path: "/api/p/app/inbox/" + idAnswered + "/answer", ctype: formType, body: "answer=again", wantStatus: 409, wantAnswer: "seen", wantVia: "cli"},
		{name: "form over 64 KB, length declared", path: answerPath, ctype: formType, body: "answer=" + big, wantStatus: 413},
		{name: "form over 64 KB, no length", path: answerPath, ctype: formType, body: "answer=" + big, noLength: true, wantStatus: 413},
		{name: "JSON over 64 KB, no length", path: answerPath, ctype: jsonType, body: `{"answer": "` + big + `"}`, noLength: true, wantStatus: 413},
		{name: "form just under the limit", path: answerPath, ctype: formType, body: "answer=" + strings.Repeat("a", maxBody-len("answer=")), wantStatus: 200, wantAnswer: strings.Repeat("a", maxBody-len("answer=")), wantVia: "web"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			id := strings.Split(strings.TrimPrefix(tc.path, "/api/p/app/inbox/"), "/")[0]

			send := func(body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, tc.path, io.NopCloser(strings.NewReader(body)))
				if !tc.noLength {
					req.ContentLength = int64(len(body))
				}
				req.Header.Set("Authorization", "Bearer "+testToken)
				if tc.ctype != "" {
					req.Header.Set("Content-Type", tc.ctype)
				}
				rec := httptest.NewRecorder()
				rg.h.ServeHTTP(rec, req)
				return rec
			}

			rec := send(tc.body)
			if tc.twice {
				if rec.Code != http.StatusOK {
					t.Fatalf("first answer: status = %d\n%s", rec.Code, rec.Body.String())
				}
				rec = send("answer=second")
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%.300s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			m := decode(t, rec)

			stored, err := inbox.Store{Dir: inbox.Dir(rg.root)}.Get(id)
			must(t, err)
			if stored.Answer != tc.wantAnswer || stored.AnsweredVia != tc.wantVia {
				t.Errorf("stored answer = %.40q via %q, want %.40q via %q", stored.Answer, stored.AnsweredVia, tc.wantAnswer, tc.wantVia)
			}

			switch tc.wantStatus {
			case http.StatusOK:
				if m["id"] != id || m["answered_via"] != "web" || m["answer"] != tc.wantAnswer || m["answered_at"] != "2026-10-12T12:00:00Z" {
					t.Errorf("answered item = %.400s", dump(m))
				}
			case http.StatusConflict:
				it, ok := m["item"].(map[string]any)
				if !ok || it["answer"] != tc.wantAnswer || it["answered_via"] != tc.wantVia {
					t.Errorf("409 body should carry the stored item: %s", dump(m))
				}
				fallthrough
			default:
				if msg, _ := m["error"].(string); !strings.HasSuffix(msg, ".") {
					t.Errorf("error = %q, want a plain sentence", msg)
				}
			}
		})
	}
}

// TestAnswerLeavesTheLedgerAlone pins the package's third rule from the
// outside: after an answer, nothing exists under .afk/build. The fake
// LoadBuild never writes, so any file there would have been put there by a
// handler acting for the build loop.
func TestAnswerLeavesTheLedgerAlone(t *testing.T) {
	rg := newRig(t, Config{})
	rg.seedInbox()
	rec := rg.do(http.MethodPost, "/api/p/app/inbox/"+idBlocking+"/answer", formType, "answer=yes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(rg.root, ".afk", "build")); !os.IsNotExist(err) {
		t.Errorf(".afk/build exists after an answer (err = %v); a handler must not write there", err)
	}
}

// TestPauseResume covers the two buttons.
//
// Load-bearing rows: "pause" and "resume" check both that the injected
// function was called exactly once with the project's folder and that the
// summary in the answer was read after the call (the fake really writes the
// flag, so `paused` flips). The failure rows check the error's own text
// reaches the owner and nothing claims success.
func TestPauseResume(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*rig)
		path        string
		wantStatus  int
		wantPaused  any // the `paused` key of the answer, for a 200
		wantPauses  int
		wantResumes int
		wantInError string
	}{
		{name: "pause", path: "/api/p/app/pause", wantStatus: 200, wantPaused: true, wantPauses: 1},
		{
			name:  "resume",
			setup: func(rg *rig) { must(rg.t, daemonstate.Pause(rg.root, testNow)) },
			path:  "/api/p/app/resume", wantStatus: 200, wantPaused: false, wantResumes: 1,
		},
		{name: "resume when not paused", path: "/api/p/app/resume", wantStatus: 200, wantPaused: false, wantResumes: 1},
		{
			name:  "pause fails",
			setup: func(rg *rig) { rg.pauseErr = errors.New("writing pause flag: disk is full") },
			path:  "/api/p/app/pause", wantStatus: 500, wantPauses: 1, wantInError: "writing pause flag: disk is full",
		},
		{
			name:  "resume fails",
			setup: func(rg *rig) { rg.resumeErr = errors.New("removing pause flag: not allowed") },
			path:  "/api/p/app/resume", wantStatus: 500, wantResumes: 1, wantInError: "removing pause flag: not allowed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			if tc.setup != nil {
				tc.setup(rg)
			}
			rec := rg.do(http.MethodPost, tc.path, "", "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			m := decode(t, rec)
			if len(rg.paused) != tc.wantPauses || len(rg.resumed) != tc.wantResumes {
				t.Errorf("pause calls = %v, resume calls = %v; want %d and %d", rg.paused, rg.resumed, tc.wantPauses, tc.wantResumes)
			}
			for _, root := range append(rg.paused, rg.resumed...) {
				if root != rg.root {
					t.Errorf("called with %q, want the project's folder %q", root, rg.root)
				}
			}
			if tc.wantStatus == http.StatusOK {
				wantKeys(t, m, "name", "paused", "current", "inbox")
				if m["paused"] != tc.wantPaused {
					t.Errorf("paused = %v, want %v", m["paused"], tc.wantPaused)
				}
				return
			}
			if msg, _ := m["error"].(string); !strings.Contains(msg, tc.wantInError) {
				t.Errorf("error = %q, want it to carry %q", msg, tc.wantInError)
			}
		})
	}
}

// TestDamagedLedgerIsAProblemNotAFailure: a plan or ledger that cannot be
// read shows as the project's problem on every route that carries a summary,
// and the request still answers 200, so one broken project cannot blank the
// overview of the others. The inbox still works, which is how the owner
// keeps answering while the file is being fixed.
func TestDamagedLedgerIsAProblemNotAFailure(t *testing.T) {
	const problem = "ledger file is damaged: .afk/build/ledger.json: unexpected end. Fix or delete it"
	rg := newRig(t, Config{})
	rg.seedInbox()
	rg.loadErr = errors.New(problem)

	rec := rg.do(http.MethodGet, "/api/", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("overview status = %d\n%s", rec.Code, rec.Body.String())
	}
	ps := decode(t, rec)["projects"].([]any)
	first, second := ps[0].(map[string]any), ps[1].(map[string]any)
	if first["problem"] != problem || first["current"] != nil || first["total"] != 0.0 {
		t.Errorf("damaged project = %s", dump(first))
	}
	if first["open_blocking"] != 1.0 {
		t.Errorf("open_blocking = %v, want the inbox still counted", first["open_blocking"])
	}
	if second["problem"] != "" {
		t.Errorf("the other project should be untouched: %s", dump(second))
	}

	rec = rg.do(http.MethodGet, "/api/p/app", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("project status = %d\n%s", rec.Code, rec.Body.String())
	}
	if m := decode(t, rec); m["problem"] != problem {
		t.Errorf("project = %s", dump(m))
	}

	rec = rg.do(http.MethodPost, "/api/p/app/inbox/"+idBlocking+"/answer", formType, "answer=yes")
	if rec.Code != http.StatusOK {
		t.Errorf("answer status = %d, want 200 even with a damaged ledger\n%s", rec.Code, rec.Body.String())
	}
}

// TestNoCacheBetweenRequests changes the files and the fakes between two
// requests to the same handler and expects the second answer to show it.
func TestNoCacheBetweenRequests(t *testing.T) {
	rg := newRig(t, Config{})
	first := decode(t, rg.do(http.MethodGet, "/api/p/app", "", ""))
	if first["paused"] != false || first["open_items"] != 0.0 {
		t.Fatalf("before = %s", dump(first))
	}

	rg.seedInbox()
	must(t, daemonstate.Pause(rg.root, testNow))
	rg.ledger = ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Parked, ParkedOn: []string{idBlocking}}}}
	rg.usageOK = false

	second := decode(t, rg.do(http.MethodGet, "/api/p/app", "", ""))
	if second["paused"] != true || second["open_items"] != 2.0 {
		t.Errorf("after = %s", dump(second))
	}
	if cur := second["current"].(map[string]any); cur["phase"] != PhaseWaiting {
		t.Errorf("current = %s", dump(cur))
	}
	if u := decode(t, rg.do(http.MethodGet, "/api/usage", "", "")); u["known"] != false {
		t.Errorf("usage = %s", dump(u))
	}
}
