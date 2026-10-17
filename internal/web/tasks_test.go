package web

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
)

// recordedTokens is a token record with a phase under half a percent, so the
// split bar's "every phase with a cost gets a segment" rule is on show.
var recordedTokens = TaskTokens{
	State: TokensRecorded, Total: 1000, Short: "1.0K weighted",
	Phases: []TokenPhase{{Phase: "plan", Weighted: 400, Share: 40}, {Phase: "execute", Weighted: 595, Share: 59.5}, {Phase: "review", Weighted: 5, Share: 0.5}},
	Roles: []TokenRole{
		{Role: "executor", Calls: 3, Input: 1200, Output: 300, Weighted: 595},
		{Role: "planner", Calls: 1, Input: 800, Output: 100, Weighted: 400},
		{Role: "reviewer", Calls: 1, Input: 10, Output: 2, Weighted: 5},
	},
}

// sectionsOf is the sections as "title done/total: ids", for comparing whole
// layouts in one line.
func sectionsOf(v TasksView) []string {
	var out []string
	for _, sec := range v.Sections {
		var ids []string
		for _, r := range sec.Tasks {
			ids = append(ids, r.ID)
		}
		out = append(out, sec.Title+" "+strconv.Itoa(sec.Done)+"/"+strconv.Itoa(sec.Total)+": "+strings.Join(ids, ","))
	}
	return out
}

// TestTasksPageFor is the tasks list's layout.
//
// Load-bearing rows:
//   - "open", "done", "all": the filter decides which rows show, but a
//     section's "N of M done" and the plan's progress count every task, so
//     switching filters never changes a count;
//   - "superseded is left out of the counts": the plan's total must be one
//     that can be reached, as on the project page;
//   - "a level 4 heading is not a milestone" and "tasks before any
//     milestone": the grouping is planlint's rule, not "any heading";
//   - "plan cannot be read": every task in one "Tasks" section rather than
//     an error, since the tasks themselves came from LoadBuild;
//   - "an unknown show reads as open": the address can never make the page
//     show something other than the three filters.
func TestTasksPageFor(t *testing.T) {
	tasks, err := masterplan.Parse([]byte(tasksPlan))
	if err != nil {
		t.Fatal(err)
	}
	l := ledger.Ledger{Tasks: []ledger.Entry{
		{ID: "T0.1", Status: ledger.Done},
		{ID: "T1.1", Status: ledger.Done},
		{ID: "T1.2", Status: ledger.Running},
		{ID: "T1.3", Status: ledger.Superseded, ReplacedBy: []string{"T2.1"}},
		{ID: "T2.2", Status: ledger.Failed},
	}}
	tests := []struct {
		name      string
		plan      string
		tasks     []masterplan.Task
		ledger    ledger.Ledger
		show      string
		wantShow  string
		done      int
		total     int
		sections  []string
		wantEmpty string
	}{
		{name: "open", plan: tasksPlan, tasks: tasks, ledger: l, show: "open", wantShow: "open", done: 2, total: 5,
			sections: []string{"M1 — Foundations 1/2: T1.2", "M2 — Polish 0/2: T2.1,T2.2"}},
		{name: "open is the default", plan: tasksPlan, tasks: tasks, ledger: l, show: "", wantShow: "open", done: 2, total: 5,
			sections: []string{"M1 — Foundations 1/2: T1.2", "M2 — Polish 0/2: T2.1,T2.2"}},
		{name: "an unknown show reads as open", plan: tasksPlan, tasks: tasks, ledger: l, show: "<script>", wantShow: "open", done: 2, total: 5,
			sections: []string{"M1 — Foundations 1/2: T1.2", "M2 — Polish 0/2: T2.1,T2.2"}},
		{name: "done", plan: tasksPlan, tasks: tasks, ledger: l, show: "done", wantShow: "done", done: 2, total: 5,
			sections: []string{"Tasks 1/1: T0.1", "M1 — Foundations 1/2: T1.1,T1.3"}},
		{name: "all", plan: tasksPlan, tasks: tasks, ledger: l, show: "all", wantShow: "all", done: 2, total: 5,
			sections: []string{"Tasks 1/1: T0.1", "M1 — Foundations 1/2: T1.1,T1.2,T1.3", "M2 — Polish 0/2: T2.1,T2.2"}},
		{name: "superseded is left out of the counts", plan: tasksPlan, tasks: tasks[1:4],
			ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Superseded}, {ID: "T1.2", Status: ledger.Done}}}, show: "all", wantShow: "all", done: 1, total: 2,
			sections: []string{"M1 — Foundations 1/2: T1.1,T1.2,T1.3"}},
		{name: "a level 4 heading is not a milestone", plan: "### M1 — One\n\n#### T1.1 — A\n- **Depends on:** —\n\n#### M2 — Not a milestone\n\n#### T1.2 — B\n- **Depends on:** —\n",
			tasks: []masterplan.Task{{ID: "T1.1", Title: "A"}, {ID: "T1.2", Title: "B"}}, show: "all", wantShow: "all", total: 2,
			sections: []string{"M1 — One 0/2: T1.1,T1.2"}},
		{name: "a level 3 heading that is not a milestone", plan: "### M1 — One\n\n#### T1.1 — A\n- **Depends on:** —\n\n### 4.2 Ledger\n\n#### T1.2 — B\n- **Depends on:** —\n",
			tasks: []masterplan.Task{{ID: "T1.1", Title: "A"}, {ID: "T1.2", Title: "B"}}, show: "all", wantShow: "all", total: 2,
			sections: []string{"M1 — One 0/2: T1.1,T1.2"}},
		{name: "tasks before any milestone", plan: "#### T0.1 — Early\n- **Depends on:** —\n\n### M1 — One\n\n#### T1.1 — A\n- **Depends on:** —\n",
			tasks: []masterplan.Task{{ID: "T0.1", Title: "Early"}, {ID: "T1.1", Title: "A"}}, show: "all", wantShow: "all", total: 2,
			sections: []string{"Tasks 0/1: T0.1", "M1 — One 0/1: T1.1"}},
		{name: "plan cannot be read", plan: "", tasks: tasks, ledger: l, show: "all", wantShow: "all", done: 2, total: 5,
			sections: []string{"Tasks 2/5: T0.1,T1.1,T1.2,T1.3,T2.1,T2.2"}},
		{name: "a task the scan lacks uses its own line", plan: "### M1 — One\n\n### M2 — Two\n",
			tasks: []masterplan.Task{{ID: "T1.1", Title: "A", Line: 2}, {ID: "T2.1", Title: "B", Line: 4}}, show: "all", wantShow: "all", total: 2,
			sections: []string{"M1 — One 0/1: T1.1", "M2 — Two 0/1: T2.1"}},
		{name: "nothing open", plan: tasksPlan, tasks: tasks[:2], ledger: l, show: "open", wantShow: "open", done: 2, total: 2, wantEmpty: "No open tasks"},
		{name: "nothing done", plan: tasksPlan, tasks: tasks, show: "done", wantShow: "done", total: 6, wantEmpty: "Nothing done yet"},
		{name: "no tasks", plan: tasksPlan, show: "all", wantShow: "all", wantEmpty: "No tasks in the plan"},
		{name: "no tasks, open", plan: tasksPlan, show: "open", wantShow: "open", wantEmpty: "No tasks in the plan"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := tasksPageFor("app", []byte(tc.plan), tc.tasks, tc.ledger, tc.show)
			if v.Show != tc.wantShow || v.Done != tc.done || v.Total != tc.total || v.Empty != tc.wantEmpty || v.Project != "app" {
				t.Errorf("show %q done %d total %d empty %q, want %q %d %d %q", v.Show, v.Done, v.Total, v.Empty, tc.wantShow, tc.done, tc.total, tc.wantEmpty)
			}
			if got := sectionsOf(v); !reflect.DeepEqual(got, tc.sections) {
				t.Errorf("sections = %q\nwant       %q", got, tc.sections)
			}
			if v.Sections == nil {
				t.Errorf("sections is nil; the JSON must hold an empty array")
			}
			current := 0
			for _, f := range v.Filters {
				if f.Current {
					current++
					if !strings.HasSuffix(f.Href, "?show="+tc.wantShow) {
						t.Errorf("the current filter links to %s, want show=%s", f.Href, tc.wantShow)
					}
				}
			}
			if len(v.Filters) != 3 || current != 1 {
				t.Errorf("filters = %+v, want three with one current", v.Filters)
			}
		})
	}
}

// TestTasksPageForRows checks what one row carries. Load-bearing: the waits
// line comes from nexttask.Unmet and only for a task that is neither done nor
// superseded (as `afk tasks` prints "-" for those), and the superseded word
// names its replacements as the command does.
func TestTasksPageForRows(t *testing.T) {
	tasks := []masterplan.Task{
		{ID: "T1.1", Title: "A"},
		{ID: "T1.2", Title: "B", DependsOn: []string{"T1.1"}},
		{ID: "T1.3", Title: "C", DependsOn: []string{"T1.1", "T1.2"}},
		{ID: "T1.4", Title: "D", DependsOn: []string{"T1.3"}},
		{ID: "T1.5", Title: "E", DependsOn: []string{"T1.2"}},
	}
	l := ledger.Ledger{Tasks: []ledger.Entry{
		{ID: "T1.2", Status: ledger.Failed},
		{ID: "T1.4", Status: ledger.Superseded, ReplacedBy: []string{"T1.5", "T1.6"}},
		{ID: "T1.5", Status: ledger.Done},
	}}
	v := tasksPageFor("my app", nil, tasks, l, "all")
	want := []TaskRow{
		{ID: "T1.1", Title: "A", Status: "pending", Unmet: []string{}, ReplacedBy: []string{}, Href: "/p/my%20app/tasks/T1.1", Word: "pending", Badge: "badge--pending"},
		{ID: "T1.2", Title: "B", Status: "failed", Unmet: []string{"T1.1"}, ReplacedBy: []string{}, Href: "/p/my%20app/tasks/T1.2", Word: "failed", Badge: "badge--failed", Waits: "Waits for T1.1"},
		{ID: "T1.3", Title: "C", Status: "pending", Unmet: []string{"T1.1", "T1.2"}, ReplacedBy: []string{}, Href: "/p/my%20app/tasks/T1.3", Word: "pending", Badge: "badge--pending", Waits: "Waits for T1.1, T1.2"},
		{ID: "T1.4", Title: "D", Status: "superseded", Unmet: []string{}, ReplacedBy: []string{"T1.5", "T1.6"}, Href: "/p/my%20app/tasks/T1.4", Word: "superseded (by T1.5, T1.6)", Badge: "badge--superseded"},
		{ID: "T1.5", Title: "E", Status: "done", Unmet: []string{}, ReplacedBy: []string{}, Href: "/p/my%20app/tasks/T1.5", Word: "done", Badge: "badge--done"},
	}
	if len(v.Sections) != 1 || !reflect.DeepEqual(v.Sections[0].Tasks, want) {
		t.Errorf("rows = %+v\nwant  %+v", v.Sections, want)
	}
	if v.Done != 1 || v.Total != 4 {
		t.Errorf("done %d of %d, want 1 of 4", v.Done, v.Total)
	}
}

// TestTaskPageFor is the line on why a task is in its state, one row per
// status. Load-bearing rows: "failed" (it must say the loop will not start
// it until it is set back to pending, which is the reason to open this
// screen), "pending waiting" (names the ids, from nexttask.Unmet) and
// "parked with no items" (nothing the owner answers will release it).
func TestTaskPageFor(t *testing.T) {
	task := masterplan.Task{ID: "T3.4", Title: "Ship it", DependsOn: []string{"T3.2", "T3.3"}}
	done := []ledger.Entry{{ID: "T3.2", Status: ledger.Done}, {ID: "T3.3", Status: ledger.Done}}
	with := func(e ledger.Entry, others ...ledger.Entry) ledger.Ledger {
		return ledger.Ledger{Tasks: append(append([]ledger.Entry{}, others...), e)}
	}
	tests := []struct {
		name   string
		ledger ledger.Ledger
		why    string
		word   string
		unmet  []string
	}{
		{name: "pending ready", ledger: ledger.Ledger{Tasks: done}, why: "Ready: afk starts it when nothing else is running.", word: "pending", unmet: []string{}},
		{name: "pending waiting", ledger: ledger.Ledger{Tasks: done[:1]}, why: "Waiting for T3.3 to be done first.", word: "pending", unmet: []string{"T3.3"}},
		{name: "running with attempt", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Running, Session: "t3-4", Attempts: 2}, done...), why: "Running in session t3-4, attempt 2.", word: "running", unmet: []string{}},
		{name: "running with nothing recorded", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Running}, done...), why: "Running.", word: "running", unmet: []string{}},
		{name: "shipping with its step", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Shipping, Ship: "ci_wait"}, done...), why: "Shipping: ci wait.", word: "shipping", unmet: []string{}},
		{name: "shipping with no step", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Shipping}, done...), why: "Shipping; its first step is not recorded yet.", word: "shipping", unmet: []string{}},
		{name: "parked on one item", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Parked, ParkedOn: []string{"abc234"}}, done...), why: "Parked, waiting on 1 inbox item.", word: "parked", unmet: []string{}},
		{name: "parked on two items", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Parked, ParkedOn: []string{"abc234", "def567"}}, done...), why: "Parked, waiting on 2 inbox items.", word: "parked", unmet: []string{}},
		{name: "parked with no items", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Parked}, done...), why: "Parked, waiting on no inbox item, so only a change to its status starts it again.", word: "parked", unmet: []string{}},
		{name: "done", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Done}), why: "Done.", word: "done", unmet: []string{}},
		{name: "failed", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Failed}), why: "Failed. The build loop will not start it again until its status is set back to pending.", word: "failed", unmet: []string{"T3.2", "T3.3"}},
		{name: "superseded with replacements", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Superseded, ReplacedBy: []string{"T3.4a", "T3.4b"}}), why: "Superseded by T3.4a, T3.4b, which do its work instead.", word: "superseded (by T3.4a, T3.4b)", unmet: []string{}},
		{name: "superseded with none", ledger: with(ledger.Entry{ID: "T3.4", Status: ledger.Superseded}), why: "Superseded. No replacing tasks were recorded.", word: "superseded", unmet: []string{}},
		{name: "a status this binary does not know", ledger: with(ledger.Entry{ID: "T3.4", Status: "frozen"}, done...), why: `T3.4 has the status "frozen", which this version of afk does not know.`, word: "frozen", unmet: []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := taskPageFor("app", task, tc.ledger, TaskTokens{State: TokensNone}, nil)
			if v.Why != tc.why || v.Page.Word != tc.word || !reflect.DeepEqual(v.Unmet, tc.unmet) {
				t.Errorf("why %q word %q unmet %q\nwant %q %q %q", v.Why, v.Page.Word, v.Unmet, tc.why, tc.word, tc.unmet)
			}
			if !reflect.DeepEqual(v.DependsOn, []string{"T3.2", "T3.3"}) || v.Page.Depends != "Depends on T3.2, T3.3." {
				t.Errorf("depends %q line %q", v.DependsOn, v.Page.Depends)
			}
		})
	}
	if got := taskPageFor("app", masterplan.Task{ID: "T1.1"}, ledger.Ledger{}, TaskTokens{}, nil); got.Page.Depends != "Depends on no other task." || got.DependsOn == nil || got.Tokens.Phases == nil || got.Tokens.Roles == nil {
		t.Errorf("a task with no dependencies: %+v", got)
	}
}

// TestTaskPageSplitWidths is the split bar's widths. Load-bearing rows:
// "tiny phase" (a phase with a cost under half a percent still gets a
// visible segment, and the point comes from the widest so the bar is still
// 100), "thirds" (plain rounding gives 99) and "zero weight" (a phase with no
// cost gets no width at all).
func TestTaskPageSplitWidths(t *testing.T) {
	tests := []struct {
		name    string
		weights []float64
		want    []int
	}{
		{name: "none", weights: nil, want: []int{}},
		{name: "all zero", weights: []float64{0, 0}, want: []int{0, 0}},
		{name: "one phase", weights: []float64{7}, want: []int{100}},
		{name: "exact", weights: []float64{40, 60}, want: []int{40, 60}},
		{name: "thirds", weights: []float64{1, 1, 1}, want: []int{34, 33, 33}},
		{name: "largest remainder wins", weights: []float64{12.6, 30.2, 57.2}, want: []int{13, 30, 57}},
		{name: "tiny phase", weights: []float64{400, 595, 5}, want: []int{40, 59, 1}},
		{name: "two tiny phases", weights: []float64{1, 1, 998}, want: []int{1, 1, 98}},
		{name: "zero weight", weights: []float64{50, 0, 50}, want: []int{50, 0, 50}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitWidths(tc.weights)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("widths = %v, want %v", got, tc.want)
			}
			sum, any := 0, false
			for i, w := range got {
				sum += w
				if tc.weights[i] > 0 {
					any = true
					if w < 1 {
						t.Errorf("phase %d has a cost but no width", i)
					}
				}
			}
			if any && sum != 100 {
				t.Errorf("widths sum to %d, want 100", sum)
			}
		})
	}
}

// TestTaskPageTokensCard is the Tokens card for each state. Load-bearing:
// "none" says there is nothing yet (not an error), and "unreadable" shows the
// reader's own words as a fault.
func TestTaskPageTokensCard(t *testing.T) {
	rec := tokensCardFor(recordedTokens)
	if !rec.Recorded || rec.Total != "1.0K weighted" || rec.Label != "Tokens by phase: plan 40.0%, execute 59.5%, review 0.5%" {
		t.Errorf("recorded card = %+v", rec)
	}
	wantSegs := []splitSegment{{"plan", 40}, {"execute", 59}, {"review", 1}}
	if !reflect.DeepEqual(rec.Segments, wantSegs) {
		t.Errorf("segments = %+v, want %+v", rec.Segments, wantSegs)
	}
	if rec.Legend[2] != (legendRow{Phase: "review", Class: "review", Value: "5", Share: "0.5%"}) || len(rec.Roles) != 3 || rec.Roles[0] != (roleRow{"executor", "3", "1200", "300", "595"}) {
		t.Errorf("legend %+v roles %+v", rec.Legend, rec.Roles)
	}
	odd := tokensCardFor(TaskTokens{State: TokensRecorded, Total: 10, Phases: []TokenPhase{{Phase: "triage", Weighted: 10, Share: 100}}})
	if odd.Total != "10 weighted" || odd.Segments[0] != (splitSegment{"other", 100}) {
		t.Errorf("a phase this binary does not know: %+v", odd)
	}
	if c := tokensCardFor(TaskTokens{State: TokensNone}); c.Recorded || c.Error || c.Message != "No tokens recorded for this task yet." {
		t.Errorf("none = %+v", c)
	}
	if c := tokensCardFor(TaskTokens{State: TokensUnreadable, Err: "bad line 3."}); c.Recorded || !c.Error || c.Message != "bad line 3." {
		t.Errorf("unreadable = %+v", c)
	}
}

// TestTasks_unknownIDAndShowFilter is the failure side of the tasks pages.
// Load-bearing: an id the plan does not hold answers the same 404 as an
// unknown project, with a plain sentence and no task data; an id that is not
// shaped like one is never looked up; and an unknown ?show= reads as open
// and is never echoed into the page.
func TestTasks_unknownIDAndShowFilter(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		status  int
		want    []string
		notWant []string
	}{
		{name: "unknown id, page", path: "/p/app/tasks/T9.9", status: 404, want: []string{"There is no task with that id in this project&#39;s plan."}, notWant: []string{"Tokens", "task-status"}},
		{name: "unknown id, api", path: "/api/p/app/tasks/T9.9", status: 404, want: []string{`{"error":"There is no task with that id in this project's plan. Check the address, or go back to the project's tasks and pick it from the list."}`}, notWant: []string{`"id"`, `"tokens"`}},
		{name: "an id shaped like a path", path: "/api/p/app/tasks/..%2F..%2Fetc", status: 404, want: []string{"There is no task with that id"}},
		{name: "an inbox id is not a task id", path: "/api/p/app/tasks/" + idBlocking, status: 404, want: []string{"There is no task with that id"}},
		{name: "unknown project", path: "/p/nope/tasks/T1.1", status: 404, want: []string{"There is no project with that name"}},
		{name: "unknown show, page", path: "/p/app/tasks?show=%3Cb%3Eevil", status: 200, want: []string{`<a href="/p/app/tasks?show=open" aria-current="page">Open</a>`, ">T1.2<"}, notWant: []string{"evil", "T0.1"}},
		{name: "unknown show, api", path: "/api/p/app/tasks?show=everything", status: 200, want: []string{`"show":"open"`}, notWant: []string{"everything", `"T0.1"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.useTasksPlan()
			var rec *httptest.ResponseRecorder
			if strings.HasPrefix(tc.path, "/api/") {
				rec = rg.do(http.MethodGet, tc.path, "", "")
			} else {
				rec = rg.pageDo(http.MethodGet, tc.path, "")
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.status, rec.Body.String())
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("holds %q, which it should not", w)
				}
			}
			if len(rg.tokenCalls) != 0 && tc.status != 200 {
				t.Errorf("tokens were read for a task that does not exist: %v", rg.tokenCalls)
			}
			if len(rg.statusCalls) != 0 {
				t.Errorf("a status was set by a GET: %v", rg.statusCalls)
			}
		})
	}
}

// TestTaskPageTokensNotLiveAndNoSeams: the tokens card is read on page load
// only (it sits in no live region), and a daemon wired without the two task
// seams still serves the task screen, saying the tokens cannot be shown.
// Load-bearing: the nil seams must not panic, since both are optional.
func TestTaskPageTokensNotLiveAndNoSeams(t *testing.T) {
	rg := newRig(t, Config{})
	rg.useTasksPlan()
	rg.tokens = map[string]TaskTokens{"T1.2": recordedTokens}
	body := rg.pageDo(http.MethodGet, "/p/app/tasks/T1.2", "").Body.String()
	card := body[strings.Index(body, `<section class="card task__tokens"`):]
	card = card[:strings.Index(card, "</section>")]
	if strings.Contains(card, "data-live") {
		t.Errorf("the tokens card is in a live region:\n%s", card)
	}
	if len(rg.tokenCalls) != 1 || rg.tokenCalls[0] != [2]string{rg.root, "T1.2"} {
		t.Errorf("token reads = %v, want one for T1.2 in app", rg.tokenCalls)
	}

	tasks, err := masterplan.Parse([]byte(tasksPlan))
	must(t, err)
	h, err := New(Config{}, Deps{
		Password:  testPasswordSeam,
		Projects:  func() []Project { return []Project{{Name: "app", Root: t.TempDir()}} },
		LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return tasks, ledger.Ledger{}, nil },
		Sessions:  func(string) []SessionView { return nil },
		Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
		Pause:     func(string) error { return nil },
		Resume:    func(string) error { return nil },
		Now:       func() time.Time { return testNow },
	})
	must(t, err)
	for _, path := range []string{"/p/app/tasks/T1.2", "/api/p/app/tasks/T1.2", "/p/app/tasks"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status = %d\n%s", path, rec.Code, rec.Body.String())
		}
		if strings.HasSuffix(path, "T1.2") && !strings.Contains(rec.Body.String(), "without a way to read token records") {
			t.Errorf("%s does not say the tokens cannot be shown:\n%s", path, rec.Body.String())
		}
	}
}

// TestTaskPageSessionLink is the Open its session link. Load-bearing rows:
// "named but gone" (a ledger session discovery no longer lists must not
// become a link to a 404) and "no session" (a pending task has nothing to
// open).
func TestTaskPageSessionLink(t *testing.T) {
	running := ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.2", Status: ledger.Running, Session: "t1-2"}}}
	tests := []struct {
		name     string
		ledger   ledger.Ledger
		sessions []string
		want     string
	}{
		{name: "found", ledger: running, sessions: []string{"side", "t1-2"}, want: "/p/app/sessions/t1-2"},
		{name: "named but gone", ledger: running, sessions: []string{"side"}, want: ""},
		{name: "named with no sessions at all", ledger: running, want: ""},
		{name: "no session", ledger: ledger.Ledger{}, sessions: []string{"t1-2"}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := taskPageFor("app", masterplan.Task{ID: "T1.2"}, tc.ledger, TaskTokens{}, tc.sessions)
			if v.Page.SessionHref != tc.want {
				t.Errorf("href = %q, want %q", v.Page.SessionHref, tc.want)
			}
		})
	}
}
