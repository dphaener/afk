package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/projectrows"
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
	// idOwner is the owner-task item seedOwnerTask adds.
	idOwner = "uvw789"
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
	// srv is the server behind h, so a test can wait for a background usage
	// reading to finish (srv.refresher.wait).
	srv *server
	// refreshCalls counts calls of the fake RefreshUsage; it is atomic
	// because the fake runs on the refresher's goroutine. The fake answers
	// refreshResult, after waiting on refreshGate when it is set (see
	// blockRefresh) and after running onRefresh when that is set; it panics
	// when refreshPanic is true.
	refreshCalls  atomic.Int32
	refreshResult UsageRefreshResult
	refreshGate   chan struct{}
	refreshOnce   sync.Once
	refreshPanic  bool
	onRefresh     func()
	// pauseErr and resumeErr make the injected functions fail.
	pauseErr  error
	resumeErr error
	paused    []string
	resumed   []string
	sessions  []SessionView
	// projects and reviews are what the two list functions return. A test
	// changes them after the handler is built, which is the point: the
	// handler must ask again on every request.
	projects []Project
	reviews  []Project
	// beingPlanned is what Deps.BeingPlanned returns.
	beingPlanned []BeingPlannedView
	// draft is the folder of "draft", a plan waiting for approval that is
	// not a configured project.
	draft string
	// plans holds each root's plan text; a root with no entry has no plan
	// file. planErr makes every read fail. planReads counts the reads.
	plans     map[string]string
	planErr   error
	planReads int
	// chat is what the fake PlanChat returns, with its project filled in;
	// chatCalls records the (name, root) pairs it was asked for.
	chat      planchat.Chat
	chatCalls [][2]string
	// replies records what the fake PlanReply was given; replyErr is what
	// it returns.
	replies  []planchat.Reply
	replyErr error
	// rows is what the fake ProjectRows returns, as the daemon's seam
	// would: already built and sorted. The rig starts with one row per
	// project (rigRows); a test that wants other rows replaces it.
	rows []projectrows.Row
	// repository is what the fake Repository says for project "app"; every
	// other folder has none.
	repository string
	// tokens is what the fake TaskTokens returns per task id; a task with
	// no entry has none recorded. tokenCalls records the (root, task) pairs
	// it was asked for.
	tokens     map[string]TaskTokens
	tokenCalls [][2]string
	// statusCalls records every call of the fake SetTaskStatus, which
	// answers statusResult. Nothing in the tasks pages may call it.
	statusCalls  [][]string
	statusResult TaskStatusResult
	// cleanCalls, restartCalls, labelCalls and newCalls record every call of
	// the four session action fakes, as (root, name, and the value posted:
	// the phase seen, the label or the description). Each fake answers its
	// result, which starts as done; a test sets another to see a refusal or
	// a failure through.
	cleanCalls, restartCalls, labelCalls, newCalls     [][]string
	cleanResult, restartResult, labelResult, newResult SessionActionResult
	// planStatus is what the fake PlanStatus returns per root; a root with
	// no entry gets a known status with no planning session. approveCalls
	// records the roots the fake ApprovePlan was called with, and
	// approveResult is what it answers (approved, until a test says
	// otherwise).
	planStatus    map[string]PlanStatus
	approveCalls  []string
	approveResult PlanApproveResult
	// startCalls records every call of the fake StartPlan as (root, mode,
	// text); startResult is what it answers (started, until a test says
	// otherwise).
	startCalls  [][]string
	startResult PlanStartResult
	// checkCalls records every call of the fake CheckPlanFolder (the text
	// it was given); checkResult is what it answers (an existing repository
	// at rg.fresh, until a test says otherwise).
	checkCalls  []string
	checkResult PlanFolderCheck
	// addedCalls records every call of the fake AddBeingPlanned (the root);
	// addedName and addedErr are what it answers.
	addedCalls []string
	addedName  string
	addedErr   error
	// fresh is an empty folder the fake check calls a repository.
	fresh string
	// onStatus, when set, runs inside the fake SetTaskStatus before it
	// answers, so a test can change rg.ledger as the real write would and
	// prove the answer was read after the call.
	onStatus func(id, status string, replacedBy []string)
}

// tasksPlan is a plan whose tasks sit in two milestones, with one task above
// both, for the tasks list's grouping. useTasksPlan gives it to project
// "app" with a ledger that holds every status the list draws differently.
const tasksPlan = `# Plan

## 8. Milestones

#### T0.1 — Before any milestone
- **Depends on:** —

### M1 — Foundations

#### T1.1 — First
- **Depends on:** —

#### T1.2 — Second
- **Depends on:** T1.1

#### T1.3 — Third
- **Depends on:** T1.1

#### M1 notes, a level 4 heading that is not a milestone

### M2 — Polish

#### T2.1 — Fourth <b>bold</b>
- **Depends on:** T1.2

#### T2.2 — Fifth
- **Depends on:** T2.1
`

// useTasksPlan points project "app" at tasksPlan: T0.1 and T1.1 done, T1.2
// running, T1.3 superseded by T2.1, T2.1 pending (waiting for T1.2) and T2.2
// failed. Five tasks count (superseded is left out), two of them done.
func (rg *rig) useTasksPlan() {
	rg.t.Helper()
	tasks, err := masterplan.Parse([]byte(tasksPlan))
	if err != nil {
		rg.t.Fatalf("tasksPlan does not parse: %v", err)
	}
	rg.plans[rg.root] = tasksPlan
	rg.tasks = tasks
	rg.ledger = ledger.Ledger{Tasks: []ledger.Entry{
		{ID: "T0.1", Status: ledger.Done, FinishedAt: "2026-10-12T09:00:00Z"},
		{ID: "T1.1", Status: ledger.Done, FinishedAt: "2026-10-12T10:00:00Z"},
		{ID: "T1.2", Status: ledger.Running, Session: "t1-2", Attempts: 2},
		{ID: "T1.3", Status: ledger.Superseded, ReplacedBy: []string{"T2.1"}},
		{ID: "T2.2", Status: ledger.Failed},
	}}
}

// rigRows is the rows the daemon's seam would build for the rig's two
// projects: app running T1.2 (1 of 3 done), "my app" with no plan.
func rigRows(rg *rig) []projectrows.Row {
	rows := []projectrows.Row{
		projectrows.Build(projectrows.Input{Name: "app", Root: rg.root, Listed: true, Order: 0, HasPlan: true,
			Done: 1, Total: 3, Task: "T1.2", Title: "Second", Phase: "executing"}),
		projectrows.Build(projectrows.Input{Name: "my app", Root: rg.other, Listed: true, Order: 1}),
	}
	projectrows.Sort(rows)
	return rows
}

// testPlan is a small master plan the checker has nothing to say about, with
// one open question and one answered one.
const testPlan = `# Plan

**How to use this document.** Read Context first.

## Context

The owner asked: "build it".

## Decisions

| Topic | Decision |
|---|---|
| Language | Go |

## 8. Milestones

### M1 — One

**Outcome:** it starts.

#### T1.1 — First
- **Depends on:** —
- **Goal:** g
- **Scope:** s
- **Acceptance:** a

#### T1.2 — Second (owner)
- **Depends on:** T1.1
- **Goal:** g
- **Scope:** s
- **Acceptance:** a

## Deferred

- Nothing.

## Open questions

| Question | Needed by |
|---|---|
| Which colour? | T1.2 |
| Which font? **Answered 2026-10-12: serif.** | T1.1 |

## Definition of done

- Tests pass.
`

// draftPlan is the plan of the review-only project. Its text is unlike
// testPlan's so a test can tell which plan it was given.
const draftPlan = "# Draft plan\n\nA plan nobody has approved yet.\n"

func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	rg := &rig{t: t, root: t.TempDir(), other: t.TempDir(), draft: t.TempDir()}
	rg.projects = []Project{{Name: "app", Root: rg.root}, {Name: "my app", Root: rg.other}}
	rg.reviews = []Project{{Name: "draft", Root: rg.draft}}
	rg.plans = map[string]string{rg.root: testPlan, rg.draft: draftPlan}
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
	rg.rows = rigRows(rg)

	if cfg.Token == "" {
		cfg.Token = testToken
	}
	rg.refreshResult = UsageRefreshResult{Outcome: UsageRefreshed, Text: "Usage read: 41% of the session used."}
	done := SessionActionResult{Outcome: SessionActionDone, Text: "Done."}
	rg.cleanResult, rg.restartResult, rg.labelResult, rg.newResult = done, done, done, done
	rg.planStatus = map[string]PlanStatus{}
	rg.approveResult = PlanApproveResult{Outcome: PlanApproved, Text: "Approved the plan. afk starts building it."}
	rg.startResult = PlanStartResult{Outcome: PlanStartStarted}
	rg.fresh = filepath.Join(t.TempDir(), "shop")
	rg.checkResult = PlanFolderCheck{Path: rg.fresh, Kind: PlanFolderRepo}
	rg.addedName = "shop"
	srv, h, err := build(cfg, Deps{
		Projects:     func() []Project { return rg.projects },
		Reviews:      func() []Project { return rg.reviews },
		BeingPlanned: func() []BeingPlannedView { return rg.beingPlanned },
		ReadPlan: func(root string) (string, []byte, error) {
			rg.planReads++
			path := filepath.Join(root, "docs", "PLAN.md")
			if rg.planErr != nil {
				return path, nil, rg.planErr
			}
			text, ok := rg.plans[root]
			if !ok {
				return path, nil, os.ErrNotExist
			}
			return path, []byte(text), nil
		},
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
		PlanChat: func(project, root string) planchat.Chat {
			rg.chatCalls = append(rg.chatCalls, [2]string{project, root})
			c := rg.chat
			c.Project = project
			return c
		},
		PlanReply: func(project, root string, r planchat.Reply) error {
			rg.chatCalls = append(rg.chatCalls, [2]string{project, root})
			rg.replies = append(rg.replies, r)
			return rg.replyErr
		},
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
		ProjectRows: func() []projectrows.Row { return rg.rows },
		Repository: func(root string) string {
			if root != rg.root {
				return ""
			}
			return rg.repository
		},
		TaskTokens: func(root, task string) TaskTokens {
			rg.tokenCalls = append(rg.tokenCalls, [2]string{root, task})
			if tok, ok := rg.tokens[task]; ok {
				return tok
			}
			return TaskTokens{State: TokensNone}
		},
		SetTaskStatus: func(root, id, status string, replacedBy []string) TaskStatusResult {
			rg.statusCalls = append(rg.statusCalls, append([]string{root, id, status}, replacedBy...))
			if rg.onStatus != nil {
				rg.onStatus(id, status, replacedBy)
			}
			return rg.statusResult
		},
		CleanSession: func(root, name, phaseSeen string) SessionActionResult {
			rg.cleanCalls = append(rg.cleanCalls, []string{root, name, phaseSeen})
			return rg.cleanResult
		},
		RestartSession: func(root, name string) SessionActionResult {
			rg.restartCalls = append(rg.restartCalls, []string{root, name})
			return rg.restartResult
		},
		SetSessionLabel: func(root, name, label string) SessionActionResult {
			rg.labelCalls = append(rg.labelCalls, []string{root, name, label})
			return rg.labelResult
		},
		NewSession: func(root, name, description string) SessionActionResult {
			rg.newCalls = append(rg.newCalls, []string{root, name, description})
			return rg.newResult
		},
		PlanStatus: func(root string) PlanStatus {
			if st, ok := rg.planStatus[root]; ok {
				return st
			}
			return PlanStatus{Known: true, Problems: []string{}}
		},
		ApprovePlan: func(root string) PlanApproveResult {
			rg.approveCalls = append(rg.approveCalls, root)
			return rg.approveResult
		},
		StartPlan: func(root, mode, text string) PlanStartResult {
			rg.startCalls = append(rg.startCalls, []string{root, mode, text})
			return rg.startResult
		},
		CheckPlanFolder: func(text string) PlanFolderCheck {
			rg.checkCalls = append(rg.checkCalls, text)
			return rg.checkResult
		},
		AddBeingPlanned: func(root string) (string, error) {
			rg.addedCalls = append(rg.addedCalls, root)
			return rg.addedName, rg.addedErr
		},
		RefreshUsage: func() UsageRefreshResult {
			rg.refreshCalls.Add(1)
			if rg.refreshGate != nil {
				<-rg.refreshGate
			}
			if rg.onRefresh != nil {
				rg.onRefresh()
			}
			if rg.refreshPanic {
				panic("the fake reading broke")
			}
			return rg.refreshResult
		},
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rg.h = h
	rg.srv = srv
	// No reading goroutine may outlive its test: let a blocked one go and
	// wait for it.
	t.Cleanup(func() {
		rg.releaseRefresh()
		rg.waitRefresh()
	})
	return rg
}

// blockRefresh makes the fake RefreshUsage wait until releaseRefresh. Call it
// before the reading starts.
func (rg *rig) blockRefresh() {
	rg.refreshGate = make(chan struct{})
}

// sessionCalls is how many times any of the four session action fakes ran.
func (rg *rig) sessionCalls() int {
	return len(rg.cleanCalls) + len(rg.restartCalls) + len(rg.labelCalls) + len(rg.newCalls)
}

// releaseRefresh lets a blocked fake reading finish. Safe to call more than
// once, and when nothing was blocked.
func (rg *rig) releaseRefresh() {
	rg.refreshOnce.Do(func() {
		if rg.refreshGate != nil {
			close(rg.refreshGate)
		}
	})
}

// waitRefresh waits until no reading goroutine runs.
func (rg *rig) waitRefresh() {
	rg.srv.refresher.wait()
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

// seedOwnerTask adds the blocking item afk raises for a task only the owner
// can do. It is separate from seedInbox so the counts the other tests assert
// (open, blocking, answered) stay what they are.
func (rg *rig) seedOwnerTask() {
	rg.t.Helper()
	st := inbox.Store{Dir: inbox.Dir(rg.root), Now: func() time.Time { return testNow.Add(-time.Hour) }, NewID: func() string { return idOwner }}
	if _, err := st.Add(inbox.NewItem{Task: "T1.2", Session: "t1-2", Kind: "external_action", Blocking: true, Title: daemon.OwnerTitlePrefix + "T1.2", Body: "Create the account.\nThen come back."}); err != nil {
		rg.t.Fatalf("seeding the owner task: %v", err)
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
				if dump(m["reviews"]) != dump([]map[string]string{{"name": "draft"}}) {
					t.Errorf("reviews = %s", dump(m["reviews"]))
				}
				// rows is new beside the three keys above, which are
				// unchanged. Every key is present on every row.
				rows := m["rows"].([]any)
				if len(rows) != 2 {
					t.Fatalf("rows = %s, want 2", dump(rows))
				}
				r := rows[0].(map[string]any)
				wantKeys(t, r, "name", "state", "label", "detail", "task", "title", "phase", "done", "total", "open", "blocking", "plan_waiting", "needs_owner")
				if r["name"] != "app" || r["state"] != "running" || r["task"] != "T1.2" || r["phase"] != "executing" || r["done"] != 1.0 || r["total"] != 3.0 || r["needs_owner"] != false {
					t.Errorf("first row = %s", dump(r))
				}
				if len(r) != 13 {
					t.Errorf("a row has %d keys, want 13 (no folder, order or listed): %s", len(r), dump(r))
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
				// The project screen's layout is page-only: the JSON has
				// exactly the summary's keys and the inbox, as before it.
				if len(m) != len(summaryKeys)+1 {
					t.Errorf("project has %d keys, want %d: %s", len(m), len(summaryKeys)+1, dump(m))
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
			name:  "tasks, every task in sections",
			setup: (*rig).useTasksPlan,
			path:  "/api/p/app/tasks?show=all",
			check: func(t *testing.T, m map[string]any) {
				wantKeys(t, m, "project", "done", "total", "show", "sections", "problem")
				if len(m) != 6 {
					t.Errorf("tasks has %d keys, want 6 (the page's layout is left out): %s", len(m), dump(m))
				}
				if m["project"] != "app" || m["done"] != 2.0 || m["total"] != 5.0 || m["show"] != "all" || m["problem"] != "" {
					t.Errorf("tasks = %s", dump(m))
				}
				var titles, ids []string
				for _, sec := range m["sections"].([]any) {
					sm := sec.(map[string]any)
					wantKeys(t, sm, "title", "done", "total", "tasks")
					titles = append(titles, fmt.Sprintf("%s %v/%v", sm["title"], sm["done"], sm["total"]))
					for _, row := range sm["tasks"].([]any) {
						r := row.(map[string]any)
						wantKeys(t, r, "id", "title", "status", "unmet", "replaced_by")
						if len(r) != 5 {
							t.Errorf("a task row has %d keys, want 5: %s", len(r), dump(r))
						}
						ids = append(ids, r["id"].(string))
					}
				}
				if got, want := strings.Join(titles, "; "), "Tasks 1/1; M1 — Foundations 1/2; M2 — Polish 0/2"; got != want {
					t.Errorf("sections = %q, want %q", got, want)
				}
				if got, want := strings.Join(ids, ","), "T0.1,T1.1,T1.2,T1.3,T2.1,T2.2"; got != want {
					t.Errorf("tasks = %s, want %s", got, want)
				}
				rows := m["sections"].([]any)[2].(map[string]any)["tasks"].([]any)
				if dump(rows[0]) != dump(map[string]any{"id": "T2.1", "title": "Fourth <b>bold</b>", "status": "pending", "unmet": []string{"T1.2"}, "replaced_by": []string{}}) {
					t.Errorf("T2.1 = %s", dump(rows[0]))
				}
				super := m["sections"].([]any)[1].(map[string]any)["tasks"].([]any)[2]
				if dump(super) != dump(map[string]any{"id": "T1.3", "title": "Third", "status": "superseded", "unmet": []string{}, "replaced_by": []string{"T2.1"}}) {
					t.Errorf("T1.3 = %s", dump(super))
				}
			},
		},
		{
			name:  "tasks, open by default",
			setup: (*rig).useTasksPlan,
			path:  "/api/p/app/tasks",
			check: func(t *testing.T, m map[string]any) {
				if m["show"] != "open" || len(m["sections"].([]any)) != 2 {
					t.Errorf("tasks = %s", dump(m))
				}
			},
		},
		{
			name: "task",
			setup: func(rg *rig) {
				rg.useTasksPlan()
				rg.tokens = map[string]TaskTokens{"T1.2": recordedTokens}
			},
			path: "/api/p/app/tasks/T1.2",
			check: func(t *testing.T, m map[string]any) {
				wantKeys(t, m, "project", "id", "title", "status", "depends_on", "unmet", "why", "tokens")
				if len(m) != 8 {
					t.Errorf("task has %d keys, want 8: %s", len(m), dump(m))
				}
				if m["project"] != "app" || m["id"] != "T1.2" || m["title"] != "Second" || m["status"] != "running" || m["why"] != "Running in session t1-2, attempt 2." {
					t.Errorf("task = %s", dump(m))
				}
				if dump(m["depends_on"]) != dump([]string{"T1.1"}) || dump(m["unmet"]) != dump([]string{}) {
					t.Errorf("depends_on %s unmet %s", dump(m["depends_on"]), dump(m["unmet"]))
				}
				tok := m["tokens"].(map[string]any)
				wantKeys(t, tok, "state", "total", "short", "phases", "roles", "error")
				if tok["state"] != "recorded" || tok["total"] != 1000.0 || tok["short"] != "1.0K weighted" {
					t.Errorf("tokens = %s", dump(tok))
				}
				if p := tok["phases"].([]any)[0].(map[string]any); p["phase"] != "plan" || p["weighted"] != 400.0 || p["share"] != 40.0 {
					t.Errorf("first phase = %s", dump(p))
				}
				r := tok["roles"].([]any)[0].(map[string]any)
				wantKeys(t, r, "role", "calls", "input", "output", "weighted")
				if r["role"] != "executor" || r["calls"] != 3.0 || r["input"] != 1200.0 {
					t.Errorf("first role = %s", dump(r))
				}
			},
		},
		{
			name:  "task with no record",
			setup: (*rig).useTasksPlan,
			path:  "/api/p/app/tasks/T2.1",
			check: func(t *testing.T, m map[string]any) {
				tok := m["tokens"].(map[string]any)
				if tok["state"] != "none" || dump(tok["phases"]) != "[]" || dump(tok["roles"]) != "[]" {
					t.Errorf("tokens = %s", dump(tok))
				}
				if dump(m["unmet"]) != dump([]string{"T1.2"}) {
					t.Errorf("unmet = %s", dump(m["unmet"]))
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
		{name: "unknown project, tasks", method: "GET", path: "/api/p/nope/tasks", wantStatus: 404},
		{name: "unknown project, task", method: "GET", path: "/api/p/nope/tasks/T1.1", wantStatus: 404},
		{name: "task not in the plan", method: "GET", path: "/api/p/app/tasks/T9.9", wantStatus: 404},
		{name: "task id not shaped like an id", method: "GET", path: "/api/p/app/tasks/..%2Fx", wantStatus: 404},
		{name: "task when the ledger cannot be read", setup: func(rg *rig) { rg.loadErr = errors.New("the ledger is damaged") }, method: "GET", path: "/api/p/app/tasks/T1.1", wantStatus: 500},
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
			if len(rg.statusCalls) != 0 {
				t.Errorf("a task's status was set: %v", rg.statusCalls)
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
			rec := rg.do(http.MethodPost, tc.path, "application/json", `{"confirm":true}`)
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

// TestAPIPlan asks for the plan view's JSON.
//
// Load-bearing rows: "review-only project" (a plan nobody has approved is
// readable, and says it is waiting), "review-only project has no project
// page" and "review-only project cannot be paused" (the review list opens
// the plan view and nothing else: a folder that is not a configured project
// must not get a build page or a button that writes into it), "a review
// entry for a configured project" (the configured one wins and is not
// waiting, so the page stops saying "waiting" the moment approve has added
// the project, whether or not the review list has caught up), "a review
// entry with a configured project's name" (the address means the configured
// project, never the other folder), and "plan that cannot be read" (200 with
// the problem, as a damaged ledger is, so the owner sees what to fix).
func TestAPIPlan(t *testing.T) {
	planKeys := []string{"project", "path", "waiting_for_approval", "summary", "findings", "questions", "plan", "problem"}

	tests := []struct {
		name   string
		setup  func(*rig)
		method string
		path   string
		status int
		check  func(*testing.T, *rig, map[string]any)
	}{
		{
			name: "configured project", path: "/api/p/app/plan", status: http.StatusOK,
			check: func(t *testing.T, rg *rig, m map[string]any) {
				wantKeys(t, m, planKeys...)
				if m["project"] != "app" || m["waiting_for_approval"] != false || m["problem"] != "" {
					t.Errorf("view = %s", dump(m))
				}
				if m["path"] != filepath.Join(rg.root, "docs", "PLAN.md") {
					t.Errorf("path = %v", m["path"])
				}
				if m["plan"] != testPlan {
					t.Errorf("plan is not the text as written:\n%v", m["plan"])
				}
				sum := m["summary"].(map[string]any)
				wantKeys(t, sum, "milestones", "tasks", "owner_tasks", "open_questions", "errors", "warnings")
				if sum["milestones"] != 1.0 || sum["tasks"] != 2.0 || sum["owner_tasks"] != 1.0 || sum["open_questions"] != 1.0 || sum["errors"] != 0.0 || sum["warnings"] != 0.0 {
					t.Errorf("summary = %s", dump(sum))
				}
				if fs := m["findings"].([]any); len(fs) != 0 {
					t.Errorf("findings = %s, want none", dump(fs))
				}
				qs := m["questions"].([]any)
				if len(qs) != 1 {
					t.Fatalf("questions = %s, want the one open row", dump(qs))
				}
				q := qs[0].(map[string]any)
				wantKeys(t, q, "text", "needed_by", "line")
				if q["text"] != "Which colour?" || dump(q["needed_by"]) != dump([]string{"T1.2"}) || q["line"] != 41.0 {
					t.Errorf("question = %s", dump(q))
				}
			},
		},
		{
			name: "review-only project", path: "/api/p/draft/plan", status: http.StatusOK,
			check: func(t *testing.T, rg *rig, m map[string]any) {
				wantKeys(t, m, planKeys...)
				if m["project"] != "draft" || m["waiting_for_approval"] != true || m["plan"] != draftPlan {
					t.Errorf("view = %s", dump(m))
				}
				// A draft the checker objects to is still shown, with what
				// it found: that is what the owner is reviewing.
				if m["summary"].(map[string]any)["errors"] == 0.0 || len(m["findings"].([]any)) == 0 {
					t.Errorf("the draft's findings are missing: %s", dump(m))
				}
				f := m["findings"].([]any)[0].(map[string]any)
				wantKeys(t, f, "level", "line", "task", "rule", "message")
				if qs, ok := m["questions"].([]any); !ok || len(qs) != 0 {
					t.Errorf("questions = %s, want an empty list and not null", dump(m["questions"]))
				}
			},
		},
		{name: "review-only project has no project page", path: "/api/p/draft", status: http.StatusNotFound},
		{name: "review-only project has no inbox", path: "/api/p/draft/inbox", status: http.StatusNotFound},
		{
			name: "review-only project cannot be paused", method: http.MethodPost, path: "/api/p/draft/pause", status: http.StatusNotFound,
			check: func(t *testing.T, rg *rig, m map[string]any) {
				if len(rg.paused) != 0 || daemonstate.Paused(rg.draft) {
					t.Errorf("a pause reached a project that is only on the review list")
				}
			},
		},
		{
			name: "unknown project", path: "/api/p/nothing-here/plan", status: http.StatusNotFound,
			check: func(t *testing.T, rg *rig, m map[string]any) {
				if msg, _ := m["error"].(string); !strings.Contains(msg, "waiting for approval") {
					t.Errorf("error = %q, want it to say neither list has the name", msg)
				}
				if rg.planReads != 0 {
					t.Errorf("a plan was read for a name that is on neither list")
				}
			},
		},
		{
			name: "project name with a space", path: "/api/p/my%20app/plan", status: http.StatusOK,
			check: func(t *testing.T, rg *rig, m map[string]any) {
				// "my app" has no plan file: a known project, so 200 and
				// the problem.
				if m["project"] != "my app" || !strings.Contains(m["problem"].(string), "could not be read") {
					t.Errorf("view = %s", dump(m))
				}
			},
		},
		{
			name: "plan that cannot be read", path: "/api/p/app/plan", status: http.StatusOK,
			setup: func(rg *rig) { rg.planErr = errors.New("afk.toml is damaged at line 3") },
			check: func(t *testing.T, rg *rig, m map[string]any) {
				wantKeys(t, m, planKeys...)
				problem := m["problem"].(string)
				if !strings.Contains(problem, "afk.toml is damaged at line 3") || !strings.Contains(problem, "Check that") {
					t.Errorf("problem = %q, want the reason and what to do", problem)
				}
				if m["plan"] != "" || len(m["findings"].([]any)) != 0 || len(m["questions"].([]any)) != 0 {
					t.Errorf("an unreadable plan should carry no text and empty lists: %s", dump(m))
				}
			},
		},
		{
			name: "a review entry for a configured project", path: "/api/p/app/plan", status: http.StatusOK,
			setup: func(rg *rig) { rg.reviews = append(rg.reviews, Project{Name: "app-2", Root: rg.root}) },
			check: func(t *testing.T, rg *rig, m map[string]any) {
				if m["waiting_for_approval"] != false {
					t.Errorf("a configured project reads as waiting for approval")
				}
				if rec := rg.do(http.MethodGet, "/api/p/app-2/plan", "", ""); rec.Code != http.StatusNotFound {
					t.Errorf("the same folder answers under its review name too: %d", rec.Code)
				}
			},
		},
		{
			name: "a review entry with a configured project's name", path: "/api/p/app/plan", status: http.StatusOK,
			setup: func(rg *rig) { rg.reviews = []Project{{Name: "app", Root: rg.draft}} },
			check: func(t *testing.T, rg *rig, m map[string]any) {
				if m["plan"] != testPlan || m["waiting_for_approval"] != false {
					t.Errorf("the address gave the review entry's plan, not the configured project's: %s", dump(m))
				}
			},
		},
		{
			name: "no review list at all", path: "/api/p/draft/plan", status: http.StatusNotFound,
			setup: func(rg *rig) { rg.reviews = nil },
		},
		{name: "a plan cannot be posted to", method: http.MethodPost, path: "/api/p/app/plan", status: http.StatusNotFound},
		// T8.11 added the approve POST; a daemon without the approve seam
		// still has no approve address.
		{
			name: "there is no approve address without the seam", method: http.MethodPost, path: "/api/p/draft/plan/approve", status: http.StatusNotFound,
			setup: func(rg *rig) { rg.srv.d.ApprovePlan = nil },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			rec := rg.do(method, tc.path, "", "")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			m := decode(t, rec)
			if tc.status != http.StatusOK {
				if _, ok := m["error"]; !ok {
					t.Errorf("body = %s, want {\"error\": ...}", rec.Body.String())
				}
			}
			if strings.Contains(rec.Body.String(), testToken) {
				t.Errorf("the answer contains the token")
			}
			if tc.check != nil {
				tc.check(t, rg, m)
			}
		})
	}
}

// TestPlanRouteNeedsTheToken says for the plan view what the walk of the
// route table (TestAuthEveryRouteNeedsTheToken) proves for every route. It
// is repeated by name because a plan waiting for approval is the one thing
// the page shows about a project the machine config does not list, and
// because nobody may read a plan, approved or not, without the token.
func TestPlanRouteNeedsTheToken(t *testing.T) {
	for _, target := range []string{"/api/p/app/plan", "/api/p/draft/plan", "/p/app/plan", "/p/draft/plan", "/api/p/nothing-here/plan"} {
		for _, bad := range []struct {
			name string
			req  authReq
		}{
			{"no token", authReq{target: target}},
			{"wrong bearer", authReq{target: target, bearer: otherToken}},
			{"wrong cookie", authReq{target: target, cookie: goodCookie(otherToken)}},
		} {
			t.Run(target+" "+bad.name, func(t *testing.T) {
				rg := newRig(t, Config{})
				rec := bad.req.send(rg.h)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401\n%s", rec.Code, rec.Body.String())
				}
				if rg.planReads != 0 {
					t.Errorf("the plan was read for a request without the token")
				}
				body := rec.Body.String()
				if strings.Contains(body, "Draft plan") || strings.Contains(body, "Which colour?") {
					t.Errorf("a refused request was shown plan text:\n%s", body)
				}
				noSecrets(t, rec)
			})
		}
	}
}

// TestPlanNoCacheBetweenRequests is load-bearing: the planning session
// rewrites the plan while the owner reads it, and the two lists change when
// a plan is approved. The same handler must show each change on the next
// request, so nothing here may be remembered: not the text, not the checker's
// counts, not the open questions and not who is on which list.
func TestPlanNoCacheBetweenRequests(t *testing.T) {
	rg := newRig(t, Config{})
	first := decode(t, rg.do(http.MethodGet, "/api/p/app/plan", "", ""))
	if first["summary"].(map[string]any)["open_questions"] != 1.0 || first["summary"].(map[string]any)["errors"] != 0.0 {
		t.Fatalf("before = %s", dump(first["summary"]))
	}

	// The question is answered and a task heading is duplicated.
	changed := strings.Replace(testPlan, "| Which colour? | T1.2 |", "| Which colour? **Answered 2026-10-13: blue.** | T1.2 |", 1)
	changed = strings.Replace(changed, "#### T1.2 — Second (owner)", "#### T1.1 — Second (owner)", 1)
	rg.plans[rg.root] = changed

	second := decode(t, rg.do(http.MethodGet, "/api/p/app/plan", "", ""))
	sum := second["summary"].(map[string]any)
	if second["plan"] != changed || sum["open_questions"] != 0.0 || sum["errors"] == 0.0 || len(second["questions"].([]any)) != 0 {
		t.Errorf("after the plan changed = %s", dump(second))
	}
	if rg.planReads != 2 {
		t.Errorf("the plan was read %d times for two requests", rg.planReads)
	}

	// The draft is approved: it leaves the review list and becomes a
	// configured project, with the handler still the one built at the start.
	if ov := decode(t, rg.do(http.MethodGet, "/api/", "", "")); dump(ov["reviews"]) != dump([]map[string]string{{"name": "draft"}}) || len(ov["projects"].([]any)) != 2 {
		t.Fatalf("overview before = %s", dump(ov))
	}
	if rec := rg.do(http.MethodGet, "/api/p/draft", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("draft has a project page before it is configured: %d", rec.Code)
	}
	rg.projects = append(rg.projects, Project{Name: "draft", Root: rg.draft})
	rg.reviews = nil

	ov := decode(t, rg.do(http.MethodGet, "/api/", "", ""))
	if rv, ok := ov["reviews"].([]any); !ok || len(rv) != 0 || len(ov["projects"].([]any)) != 3 {
		t.Errorf("overview after = %s", dump(ov))
	}
	if rec := rg.do(http.MethodGet, "/api/p/draft", "", ""); rec.Code != http.StatusOK {
		t.Errorf("a project added after the handler was built has no page: %d", rec.Code)
	}
	if m := decode(t, rg.do(http.MethodGet, "/api/p/draft/plan", "", "")); m["waiting_for_approval"] != false {
		t.Errorf("the approved plan still reads as waiting: %s", dump(m))
	}

	// And a project taken out of the list is gone on the next request.
	rg.projects = rg.projects[:1]
	if rec := rg.do(http.MethodGet, "/api/p/draft/plan", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a removed project still has a plan view: %d", rec.Code)
	}
}

// TestPlanWithoutAReader: a handler built with no ReadPlan still answers,
// and says what is wrong instead of failing or showing an empty plan as if
// it were the real one.
func TestPlanWithoutAReader(t *testing.T) {
	root := t.TempDir()
	h, err := New(Config{Token: testToken}, Deps{
		Projects:  func() []Project { return []Project{{Name: "app", Root: root}} },
		LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil },
		Sessions:  func(string) []SessionView { return nil },
		Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
		Pause:     func(string) error { return nil },
		Resume:    func(string) error { return nil },
	})
	must(t, err)
	rec := authReq{target: "/api/p/app/plan", bearer: testToken}.send(h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body.String())
	}
	if m := decode(t, rec); !strings.Contains(m["problem"].(string), "without a way to read plans") || m["plan"] != "" {
		t.Errorf("view = %s", dump(m))
	}
	// With no review list either, the overview has an empty one, not null.
	if rv, ok := decode(t, authReq{target: "/api/", bearer: testToken}.send(h))["reviews"].([]any); !ok || len(rv) != 0 {
		t.Errorf("reviews is not an empty list")
	}
}

// TestPauseResumeNeedsConfirm: load-bearing rows are the empty body and
// {"confirm":false}, which used to pause the project. None may call Pause or
// Resume or change the paused flag, and each answers 400 with the sentence.
func TestPauseResumeNeedsConfirm(t *testing.T) {
	tests := []struct {
		name, path, contentType, body string
		startPaused                   bool
	}{
		{name: "pause with no body", path: "/api/p/app/pause"},
		{name: "pause with confirm false", path: "/api/p/app/pause", contentType: "application/json", body: `{"confirm":false}`},
		{name: "pause with confirm as text", path: "/api/p/app/pause", contentType: "application/json", body: `{"confirm":"yes"}`},
		{name: "pause with a form confirm=no", path: "/api/p/app/pause", contentType: "application/x-www-form-urlencoded", body: "confirm=no"},
		{name: "resume with no body", path: "/api/p/app/resume", startPaused: true},
		{name: "resume with confirm false", path: "/api/p/app/resume", contentType: "application/json", body: `{"confirm":false}`, startPaused: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.startPaused {
				must(t, daemonstate.Pause(rg.root, testNow))
			}
			rec := rg.do(http.MethodPost, tc.path, tc.contentType, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if msg, _ := decode(t, rec)["error"].(string); !strings.Contains(msg, "Nothing changed") {
				t.Errorf("error = %q", msg)
			}
			if len(rg.paused)+len(rg.resumed) != 0 || daemonstate.Paused(rg.root) != tc.startPaused {
				t.Errorf("pause calls %v, resume calls %v, paused = %v", rg.paused, rg.resumed, daemonstate.Paused(rg.root))
			}
		})
	}
}

// TestConfirmed is the pure decision. Load-bearing rows: a JSON body is read
// only as JSON (a form-looking value does not count), and a form needs the
// exact value yes.
func TestConfirmed(t *testing.T) {
	tests := []struct {
		name, ct, json string
		form           url.Values
		want           bool
	}{
		{name: "form yes", form: url.Values{"confirm": {"yes"}}, want: true},
		{name: "form no", form: url.Values{"confirm": {"no"}}},
		{name: "form true is not yes", form: url.Values{"confirm": {"true"}}},
		{name: "form empty", form: url.Values{}},
		{name: "nil form", form: nil},
		{name: "json true", ct: "application/json", json: `{"confirm":true}`, want: true},
		{name: "json false", ct: "application/json", json: `{"confirm":false}`},
		{name: "json string", ct: "application/json", json: `{"confirm":"yes"}`},
		{name: "json broken", ct: "application/json", json: `{`},
		{name: "json empty", ct: "application/json"},
		{name: "json ignores the form", ct: "application/json", json: `{}`, form: url.Values{"confirm": {"yes"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := confirmed(tc.ct, tc.form, []byte(tc.json)); got != tc.want {
				t.Errorf("confirmed = %v, want %v", got, tc.want)
			}
		})
	}
}
