package web

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// sessionAt is the time the session fixtures were updated.
var sessionAt = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// runningSession is an ordinary session at work: a host, a status, a work
// plan part done and no review yet. The other fixtures change one thing.
func runningSession() SessionView {
	return SessionView{
		Name: "t1-2", Phase: "executing", HostRunning: true,
		SessionID: "abcdef0123456789", Branch: "t1-2", BaseBranch: "main", LoopCount: 2,
		UpdatedAt: sessionAt, StartedAt: sessionAt.Add(-time.Hour), WorktreePath: "/work/app/t1-2",
		PlanTotal: 10, PlanDone: 3, PlanFailed: 1,
	}
}

// sessionWith returns runningSession changed by f.
func sessionWith(f func(*SessionView)) SessionView {
	s := runningSession()
	f(&s)
	return s
}

// badgesOf is a row's badges as "class word", for comparing in one line.
func badgesOf(bs []badgeView) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Class+" "+b.Word)
	}
	return out
}

// TestSessionKind is what a row is. Load-bearing rows: "problem wins over
// everything" (a worktree problem is not a session, so a stale or one-off
// flag on it means nothing), "planning before one-off" (the planning host
// usually has no status.json, and "Planning" says more than "One-off"),
// "one-off is never status unknown" (no status.json is a one-off's normal
// shape, and the project page's folded Stale must not leak into this) and
// "an unknown phase word" (shown as written, never dropped).
func TestSessionKind(t *testing.T) {
	tests := []struct {
		name  string
		s     SessionView
		kind  string
		badge string
		line  string
	}{
		{name: "problem", s: SessionView{Name: "x", Problem: "Its worktree folder is missing"}, kind: "problem", badge: "badge--failed Problem", line: "Its worktree folder is missing"},
		{name: "problem wins over everything", s: SessionView{Name: "x", Problem: "No session in this worktree", Planning: true, Scratch: true, StatusStale: true}, kind: "problem", badge: "badge--failed Problem", line: "No session in this worktree"},
		{name: "planning", s: SessionView{Name: "master-plan", Planning: true, Phase: "planning"}, kind: "planning", badge: "badge--plain Planning", line: "The planning session for this project's master plan"},
		{name: "planning before one-off", s: SessionView{Name: "master-plan", Planning: true, Scratch: true, Stale: true}, kind: "planning", badge: "badge--plain Planning", line: "The planning session for this project's master plan"},
		{name: "one-off", s: SessionView{Name: "scratch-1", Scratch: true, Stale: true}, kind: "one_off", badge: "badge--plain One-off", line: "A one-off session with no plan: only a terminal"},
		{name: "one-off is never status unknown", s: SessionView{Name: "scratch-1", Scratch: true, Stale: true, StatusStale: true, StaleReason: "missing"}, kind: "one_off", badge: "badge--plain One-off", line: "A one-off session with no plan: only a terminal"},
		{name: "stale status", s: SessionView{Name: "t1-3", Stale: true, StatusStale: true, StaleReason: "unparseable JSON"}, kind: "status_unknown", badge: "badge--warning Status unknown", line: "status.json could not be read: unparseable JSON"},
		{name: "stale status with no reason", s: SessionView{Name: "t1-3", Stale: true, StatusStale: true}, kind: "status_unknown", badge: "badge--warning Status unknown", line: "status.json could not be read: no reason was given"},
		{name: "phase planning", s: SessionView{Phase: "planning"}, kind: "session", badge: "badge--running planning"},
		{name: "phase executing", s: SessionView{Phase: "executing"}, kind: "session", badge: "badge--running executing"},
		{name: "phase reviewing", s: SessionView{Phase: "reviewing"}, kind: "session", badge: "badge--running reviewing"},
		{name: "phase fixing", s: SessionView{Phase: "fixing"}, kind: "session", badge: "badge--running fixing"},
		{name: "phase done", s: SessionView{Phase: "done"}, kind: "session", badge: "badge--done done"},
		{name: "phase failed", s: SessionView{Phase: "failed"}, kind: "session", badge: "badge--failed failed"},
		{name: "an unknown phase word", s: SessionView{Phase: "dreaming"}, kind: "session", badge: "badge--plain dreaming"},
		{name: "no phase", s: SessionView{}, kind: "session", badge: "badge--plain unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := sessionKind(tc.s)
			if k.Kind != tc.kind || k.Badge.Class+" "+k.Badge.Word != tc.badge || k.Line != tc.line {
				t.Errorf("sessionKind = %+v\nwant kind %q badge %q line %q", k, tc.kind, tc.badge, tc.line)
			}
		})
	}
}

// TestSessionsPageFor is one row of the list per kind of session.
//
// Load-bearing rows:
//   - "waiting" and "waiting but done": Waits for you follows the
//     dashboard's rule, so a finished session's left-over flag is not shown;
//   - "dead host" and "wedged on a dead host": a stopped host says so, and
//     its left-over quiet mark is not repeated;
//   - "problem": a worktree problem gets no Stopped badge, since it has no
//     host to stop;
//   - "verdict reject" and "verdict approve": the verdict is a badge of its
//     own beside the phase, never in its place;
//   - "one-off" and "stale status": the badge and line say what the row is
//     rather than showing a blank phase.
func TestSessionsPageFor(t *testing.T) {
	tests := []struct {
		name   string
		s      SessionView
		kind   string
		badges []string
		lines  []string
		short  string
		total  int
	}{
		{name: "running", s: runningSession(), kind: "session", badges: []string{"badge--running executing"}, short: "abcdef01", total: 10},
		{name: "waiting", s: sessionWith(func(s *SessionView) { s.AwaitingInput = true }), kind: "session",
			badges: []string{"badge--running executing", "badge--needs-you Waits for you"}, short: "abcdef01", total: 10},
		{name: "waiting but done", s: sessionWith(func(s *SessionView) { s.AwaitingInput = true; s.Phase = "done" }), kind: "session",
			badges: []string{"badge--done done"}, short: "abcdef01", total: 10},
		{name: "stale status", s: SessionView{Name: "t1-3", Stale: true, StatusStale: true, StaleReason: "unparseable JSON", HostRunning: true}, kind: "status_unknown",
			badges: []string{"badge--warning Status unknown"}, lines: []string{"status.json could not be read: unparseable JSON"}},
		{name: "one-off", s: SessionView{Name: "scratch-1", Stale: true, Scratch: true, HostRunning: true}, kind: "one_off",
			badges: []string{"badge--plain One-off"}, lines: []string{"A one-off session with no plan: only a terminal"}},
		{name: "planning", s: SessionView{Name: "master-plan", Stale: true, Scratch: true, Planning: true, HostRunning: true}, kind: "planning",
			badges: []string{"badge--plain Planning"}, lines: []string{"The planning session for this project's master plan"}},
		{name: "dead host", s: sessionWith(func(s *SessionView) { s.HostRunning = false }), kind: "session",
			badges: []string{"badge--running executing", "badge--idle Stopped"}, lines: []string{"Its host is not running"}, short: "abcdef01", total: 10},
		{name: "wedged", s: sessionWith(func(s *SessionView) { s.Wedged = true }), kind: "session",
			badges: []string{"badge--running executing"}, lines: []string{"Its agent has gone quiet"}, short: "abcdef01", total: 10},
		{name: "wedged on a dead host", s: sessionWith(func(s *SessionView) { s.Wedged = true; s.HostRunning = false }), kind: "session",
			badges: []string{"badge--running executing", "badge--idle Stopped"}, lines: []string{"Its host is not running"}, short: "abcdef01", total: 10},
		{name: "problem", s: SessionView{Name: "old-branch", Problem: "Its worktree folder is missing"}, kind: "problem",
			badges: []string{"badge--failed Problem"}, lines: []string{"Its worktree folder is missing"}},
		{name: "verdict approve", s: sessionWith(func(s *SessionView) { s.Phase = "done"; s.Verdict = "APPROVE" }), kind: "session",
			badges: []string{"badge--done done", "badge--pass Approved"}, short: "abcdef01", total: 10},
		{name: "verdict reject", s: sessionWith(func(s *SessionView) { s.Phase = "fixing"; s.Verdict = "REJECT" }), kind: "session",
			badges: []string{"badge--running fixing", "badge--failed Changes asked"}, short: "abcdef01", total: 10},
		{name: "verdict none", s: sessionWith(func(s *SessionView) { s.Verdict = "" }), kind: "session",
			badges: []string{"badge--running executing"}, short: "abcdef01", total: 10},
		{name: "plan error", s: sessionWith(func(s *SessionView) {
			s.PlanTotal, s.PlanDone, s.PlanFailed, s.PlanError = 0, 0, 0, "line 3: bad checkbox"
		}), kind: "session",
			badges: []string{"badge--running executing"}, short: "abcdef01"},
		{name: "a short session id is shown whole", s: sessionWith(func(s *SessionView) { s.SessionID = "s-0405" }), kind: "session",
			badges: []string{"badge--running executing"}, short: "s-0405", total: 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := sessionsPageFor("my app", []SessionView{tc.s})
			if v.Project != "my app" || len(v.Sessions) != 1 || v.Page.Empty != "" {
				t.Fatalf("view = %+v", v)
			}
			r := v.Sessions[0]
			if r.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", r.Kind, tc.kind)
			}
			if got := badgesOf(r.Badges); !reflect.DeepEqual(got, tc.badges) {
				t.Errorf("badges = %q\nwant     %q", got, tc.badges)
			}
			if !reflect.DeepEqual(r.Lines, tc.lines) {
				t.Errorf("lines = %q, want %q", r.Lines, tc.lines)
			}
			if r.ShortID != tc.short || r.Total != tc.total || r.Done != tc.s.PlanDone {
				t.Errorf("short %q done %d total %d, want %q %d %d", r.ShortID, r.Done, r.Total, tc.short, tc.s.PlanDone, tc.total)
			}
			if want := "/p/my%20app/sessions/" + tc.s.Name; r.Href != want {
				t.Errorf("href = %q, want %q", r.Href, want)
			}
			if r.VerdictChecks == nil {
				t.Errorf("verdict_checks is nil; the JSON must hold an empty array")
			}
		})
	}
	if v := sessionsPageFor("app", nil); v.Sessions == nil || len(v.Sessions) != 0 || v.Page.Empty != "No sessions yet" {
		t.Errorf("no sessions: %+v", v)
	}
	labelled := sessionsPageFor("app", []SessionView{sessionWith(func(s *SessionView) { s.Label = "Fix the login" })})
	if got := labelled.Sessions[0].Title; got != "Fix the login" {
		t.Errorf("title = %q, want the label", got)
	}
	if got := sessionsPageFor("app", []SessionView{runningSession()}).Sessions[0].Title; got != "t1-2" {
		t.Errorf("title = %q, want the name when there is no label", got)
	}
}

// TestSessionPageFor is the session screen's kv list and Summary.
//
// Load-bearing rows: "running" (the progress sentence counts failed tasks
// and the times come from status.json and host.json), "verdict reject"
// (every check is named, in the file's order), "plan error" and "verdict
// error" (a read error is said, never shown as "no plan" or "not
// reviewed"), "one-off" (no phase, plan or review to speak of, rather than
// "unknown"), "stale status" (the reason is in the summary) and "problem"
// (it says it is not a session).
func TestSessionPageFor(t *testing.T) {
	tests := []struct {
		name    string
		s       SessionView
		kv      map[string]string
		summary []string
	}{
		{name: "running", s: runningSession(),
			kv:      map[string]string{"Phase": "executing", "Loop count": "2", "Started": "14 Oct 2026 11:00 UTC", "Updated": "14 Oct 2026 12:00 UTC", "Label": "none", "Branch": "t1-2 → main", "Folder": "/work/app/t1-2", "Host": "running"},
			summary: []string{"It is carrying out its work plan.", "It is not waiting for you.", "3 of 10 work-plan tasks done (1 failed).", "Not reviewed yet.", "Its host is running."}},
		{name: "waiting", s: sessionWith(func(s *SessionView) { s.AwaitingInput = true; s.PlanFailed = 0; s.Label = "Fix the login" }),
			kv:      map[string]string{"Label": "Fix the login"},
			summary: []string{"It is carrying out its work plan.", "It is waiting for you: open its terminal to answer.", "3 of 10 work-plan tasks done.", "Not reviewed yet.", "Its host is running."}},
		{name: "verdict approve", s: sessionWith(func(s *SessionView) {
			s.Phase, s.Verdict = "done", "APPROVE"
			s.VerdictChecks = []SessionCheck{{"Compliance", "PASS"}, {"Quality", "PASS"}, {"QA", "PASS"}}
		}),
			summary: []string{"It has finished.", "3 of 10 work-plan tasks done (1 failed).", "Its review approved the work: compliance PASS · quality PASS · qa PASS.", "Its host is running."}},
		{name: "verdict reject", s: sessionWith(func(s *SessionView) {
			s.Phase, s.Verdict = "fixing", "REJECT"
			s.VerdictChecks = []SessionCheck{{"Compliance", "PASS"}, {"Quality", "FAIL"}, {"QA", "PASS"}}
		}),
			summary: []string{"It is fixing what its review found.", "It is not waiting for you.", "3 of 10 work-plan tasks done (1 failed).", "Its review asked for changes: compliance PASS · quality FAIL · qa PASS.", "Its host is running."}},
		{name: "verdict error", s: sessionWith(func(s *SessionView) { s.Phase = "reviewing"; s.VerdictError = "no Verdict line." }),
			summary: []string{"It is reviewing its own work.", "It is not waiting for you.", "3 of 10 work-plan tasks done (1 failed).", "Its review verdict could not be read: no Verdict line.", "Its host is running."}},
		{name: "plan error", s: sessionWith(func(s *SessionView) {
			s.PlanTotal, s.PlanDone, s.PlanFailed, s.PlanError = 0, 0, 0, "line 3: bad checkbox"
		}),
			summary: []string{"It is carrying out its work plan.", "It is not waiting for you.", "Its work plan could not be read: line 3: bad checkbox.", "Not reviewed yet.", "Its host is running."}},
		{name: "no work plan yet", s: sessionWith(func(s *SessionView) { s.Phase = "planning"; s.PlanTotal, s.PlanDone, s.PlanFailed = 0, 0, 0 }),
			summary: []string{"It is planning its work.", "It is not waiting for you.", "It has no work plan yet.", "Not reviewed yet.", "Its host is running."}},
		{name: "dead host", s: sessionWith(func(s *SessionView) { s.HostRunning = false; s.StartedAt = time.Time{} }),
			kv:      map[string]string{"Host": "not running", "Started": "unknown"},
			summary: []string{"It is carrying out its work plan.", "It is not waiting for you.", "3 of 10 work-plan tasks done (1 failed).", "Not reviewed yet.", "Its host is not running."}},
		{name: "wedged", s: sessionWith(func(s *SessionView) { s.Wedged = true }),
			kv:      map[string]string{"Host": "running, but its agent has gone quiet"},
			summary: []string{"It is carrying out its work plan.", "It is not waiting for you.", "3 of 10 work-plan tasks done (1 failed).", "Not reviewed yet.", "Its host is running, but its agent has gone quiet."}},
		{name: "stale status", s: SessionView{Name: "t1-3", Stale: true, StatusStale: true, StaleReason: "unparseable JSON", HostRunning: true, WorktreePath: "/work/app/t1-3"},
			kv:      map[string]string{"Phase": "unknown", "Loop count": "unknown", "Updated": "unknown", "Branch": "none"},
			summary: []string{"Its status.json could not be read (unparseable JSON), so its phase is unknown.", "It has no work plan yet.", "Not reviewed yet.", "Its host is running."}},
		{name: "one-off", s: SessionView{Name: "scratch-1", Stale: true, Scratch: true, HostRunning: true, WorktreePath: "/work/app"},
			kv:      map[string]string{"Phase": "none: a one-off session has no phases", "Loop count": "none", "Updated": "none", "Folder": "/work/app"},
			summary: []string{"It is a one-off session with no plan, so it has no phase, work plan or review.", "Its host is running."}},
		{name: "planning", s: SessionView{Name: "master-plan", Stale: true, Scratch: true, Planning: true, HostRunning: true},
			kv:      map[string]string{"Folder": "unknown"},
			summary: []string{"It is a one-off session with no plan, so it has no phase, work plan or review.", "Its host is running."}},
		{name: "problem", s: SessionView{Name: "old-branch", Problem: "Its worktree folder is missing"},
			kv:      map[string]string{"Phase": "none: this is not a session", "Host": "none"},
			summary: []string{"This is not a session but a problem with a worktree: Its worktree folder is missing.", "afk clean can tidy it up."}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := sessionPageFor("app", tc.s)
			if v.Project != "app" || v.Name != tc.s.Name {
				t.Errorf("project %q name %q", v.Project, v.Name)
			}
			got := map[string]string{}
			for _, row := range v.Page.KV {
				got[row.Label] = row.Value
			}
			if len(v.Page.KV) != 8 {
				t.Errorf("%d kv rows, want 8: %+v", len(v.Page.KV), v.Page.KV)
			}
			for k, want := range tc.kv {
				if got[k] != want {
					t.Errorf("kv %s = %q, want %q", k, got[k], want)
				}
			}
			if !reflect.DeepEqual(v.Page.Summary, tc.summary) {
				t.Errorf("summary = %q\nwant      %q", v.Page.Summary, tc.summary)
			}
		})
	}
}

// sessionRigRows are the rows the handler tests serve: one of every kind,
// and a one-off sharing a planned session's name after it, as discovery
// orders such a pair.
func sessionRigRows() []SessionView {
	return []SessionView{
		sessionWith(func(s *SessionView) { s.AwaitingInput = true }),
		{Name: "t1-2", Stale: true, Scratch: true, HostRunning: true, WorktreePath: "/work/app"},
		{Name: "t1-3", Stale: true, StatusStale: true, StaleReason: "unparseable JSON"},
		{Name: "master-plan", Stale: true, Scratch: true, Planning: true, HostRunning: true},
		{Name: "old-branch", Problem: "Its worktree folder is missing"},
	}
}

// sessionKeys are the keys every session answer holds, in the list and on
// the session's screen.
var sessionKeys = []string{
	"name", "label", "kind", "phase", "waits_for_you", "awaiting_input", "loop_count", "host_running", "wedged",
	"one_off", "planning", "autonomous", "status_stale", "stale_reason", "problem", "session_id", "branch",
	"base_branch", "worktree_path", "started_at", "updated_at", "plan_total", "plan_done", "plan_failed",
	"plan_error", "verdict", "verdict_checks", "verdict_error",
}

// TestSessionHandlers asks for the list and a session as JSON and as pages.
//
// Load-bearing rows: "name collision picks the first row" (the planned
// session, as the dashboard and attach resolve the name, never the one-off
// listed after it), "the api keys" (every key present, lists as [] and times
// as RFC 3339 or "", so a reader never meets null), and "planning page"
// (says its chat comes later rather than offering a way in).
func TestSessionHandlers(t *testing.T) {
	t.Run("the api keys", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.sessions = sessionRigRows()
		rec := rg.do("GET", "/api/p/app/sessions", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d\n%s", rec.Code, rec.Body.String())
		}
		m := decode(t, rec)
		wantKeys(t, m, "project", "sessions")
		list, _ := m["sessions"].([]any)
		if len(list) != 5 {
			t.Fatalf("%d sessions, want 5", len(list))
		}
		for _, raw := range list {
			row := raw.(map[string]any)
			wantKeys(t, row, sessionKeys...)
			if len(row) != len(sessionKeys) {
				t.Errorf("row has %d keys, want %d (page-only fields leaked?): %v", len(row), len(sessionKeys), row)
			}
			if row["verdict_checks"] == nil {
				t.Errorf("verdict_checks is null in %v", row)
			}
		}
		first := list[0].(map[string]any)
		if first["updated_at"] != "2026-10-14T12:00:00Z" || first["started_at"] != "2026-10-14T11:00:00Z" || first["waits_for_you"] != true || first["kind"] != "session" {
			t.Errorf("first row = %v", first)
		}
		if k := list[4].(map[string]any)["kind"]; k != "problem" {
			t.Errorf("last row kind = %v, want problem", k)
		}
		if s := list[2].(map[string]any)["started_at"]; s != "" {
			t.Errorf("an unknown started_at = %v, want \"\"", s)
		}
	})
	t.Run("no sessions answers an empty list", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.sessions = nil
		rec := rg.do("GET", "/api/p/app/sessions", "", "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sessions":[]`) {
			t.Errorf("status %d body %s", rec.Code, rec.Body.String())
		}
		page := rg.pageDo("GET", "/p/app/sessions", "").Body.String()
		if !strings.Contains(page, `<p class="empty__title">No sessions yet</p>`) {
			t.Errorf("no empty state:\n%s", page)
		}
	})
	t.Run("name collision picks the first row", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.sessions = sessionRigRows()
		m := decode(t, rg.do("GET", "/api/p/app/sessions/t1-2", "", ""))
		wantKeys(t, m, append([]string{"project"}, sessionKeys...)...)
		if m["project"] != "app" || m["kind"] != "session" || m["phase"] != "executing" || m["one_off"] != false {
			t.Errorf("answer = %v, want the planned session", m)
		}
	})
	t.Run("session api", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.sessions = sessionRigRows()
		rec := rg.do("GET", "/api/p/app/sessions/old-branch", "", "")
		var v map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != 200 {
			t.Fatalf("status %d: %v", rec.Code, err)
		}
		if v["kind"] != "problem" || v["problem"] != "Its worktree folder is missing" {
			t.Errorf("answer = %v", v)
		}
	})
	t.Run("list page", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.sessions = sessionRigRows()
		body := rg.pageDo("GET", "/p/app/sessions", "").Body.String()
		for _, w := range []string{
			`<a class="row" href="/p/app/sessions/t1-2">`,
			`<span class="badge badge--needs-you">Waits for you</span>`,
			`<span class="tid" title="abcdef0123456789">abcdef01</span>`,
			`aria-label="3 of 10 tasks done"`,
			`<span class="badge badge--plain">One-off</span>`,
			`<span class="badge badge--warning">Status unknown</span>`,
			`<span class="row__meta">status.json could not be read: unparseable JSON</span>`,
			`<span class="badge badge--plain">Planning</span>`,
			`<span class="badge badge--failed">Problem</span>`,
			`<span class="row__meta">Its host is not running</span>`,
		} {
			if !strings.Contains(body, w) {
				t.Errorf("missing %q", w)
			}
		}
		// The rows themselves offer no action: the only form is the new
		// session's, outside the live list.
		list := body[strings.Index(body, `id="sessions-list"`):]
		list = list[:strings.Index(list, `<dialog`)]
		for _, w := range []string{"<form", "<button", "data-confirm"} {
			if strings.Contains(list, w) {
				t.Errorf("the list holds %q; its rows offer no action", w)
			}
		}
	})
	t.Run("session page", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.sessions = sessionRigRows()
		body := rg.pageDo("GET", "/p/app/sessions/t1-2", "").Body.String()
		for _, w := range []string{
			`<h1 class="tid session__id">t1-2</h1>`,
			`<dt>Folder</dt><dd><code class="path">/work/app/t1-2</code></dd>`,
			`<dt>Branch</dt><dd>t1-2 → main</dd>`,
			`<p>It is waiting for you: open its terminal to answer.</p>`,
			`<code>afk attach t1-2</code>`,
		} {
			if !strings.Contains(body, w) {
				t.Errorf("missing %q", w)
			}
		}
		// The live regions hold no action: the Actions and their sheets sit
		// outside them, so a live swap never takes an open sheet away.
		state := body[strings.Index(body, `id="session-badges"`):]
		state = state[:strings.Index(state, `</section>`)]
		for _, w := range []string{"<form", "<button", "data-confirm", "chat view"} {
			if strings.Contains(state, w) {
				t.Errorf("the session's live regions hold %q", w)
			}
		}
		if strings.Contains(body, "chat view") {
			t.Errorf("an ordinary session's page mentions a chat view")
		}
	})
	t.Run("planning page", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.sessions = sessionRigRows()
		body := rg.pageDo("GET", "/p/app/sessions/master-plan", "").Body.String()
		if !strings.Contains(body, "Its chat view on this page comes with a later update.") {
			t.Errorf("the planning session's page does not say its chat comes later")
		}
	})
	t.Run("a name with markup is escaped", func(t *testing.T) {
		rg := newRig(t, Config{})
		rg.sessions = []SessionView{{Name: "<b>x</b>", Label: "<script>alert(1)</script>", HostRunning: true, Phase: "executing"}}
		for _, path := range []string{"/p/app/sessions", "/p/app/sessions/%3Cb%3Ex%3C%2Fb%3E"} {
			rec := rg.pageDo("GET", path, "")
			if rec.Code != 200 {
				t.Fatalf("%s: status %d", path, rec.Code)
			}
			body := noOwnScript(rec.Body.String())
			if strings.Contains(body, "<script>alert") || strings.Contains(body, "<b>x</b>") {
				t.Errorf("%s: markup reached the page", path)
			}
		}
	})
}

// TestSessionPages404 is an address that names nothing: an unknown project
// and an unknown session, as a page and under /api. Load-bearing: "unknown
// session, api" must be a 404 with an error, not a 200 with an empty
// session, or a reader would take a cleaned session for a blank one.
func TestSessionPages404(t *testing.T) {
	tests := []struct {
		name string
		path string
		api  bool
		want string
	}{
		{name: "unknown project list, page", path: "/p/nope/sessions", want: "There is no project with that name"},
		{name: "unknown project list, api", path: "/api/p/nope/sessions", api: true, want: "There is no project with that name"},
		{name: "unknown project session, page", path: "/p/nope/sessions/t1-2", want: "There is no project with that name"},
		{name: "unknown project session, api", path: "/api/p/nope/sessions/t1-2", api: true, want: "There is no project with that name"},
		{name: "unknown session, page", path: "/p/app/sessions/no-such-session", want: "There is no session with that name in this project."},
		{name: "unknown session, api", path: "/api/p/app/sessions/no-such-session", api: true, want: "There is no session with that name in this project."},
		{name: "a session of the other project", path: "/api/p/my%20app/sessions/t1-2", api: true, want: "There is no session with that name in this project."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			var body string
			if tc.api {
				rec := rg.do("GET", tc.path, "", "")
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404", rec.Code)
				}
				m := decode(t, rec)
				body, _ = m["error"].(string)
			} else {
				rec := rg.pageDo("GET", tc.path, "")
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404", rec.Code)
				}
				body = rec.Body.String()
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("answer %q does not say %q", body, tc.want)
			}
		})
	}
}
