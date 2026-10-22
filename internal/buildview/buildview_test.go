package buildview

import (
	"reflect"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
)

func task(id, title string, deps ...string) masterplan.Task {
	return masterplan.Task{ID: id, Title: title, DependsOn: deps}
}

// TestRows is a table over the row for each ledger status and each detail
// variant. Load-bearing rows: the parked rows (an item with no file must still
// be listed as waited on, or the owner is told nothing is needed), the stray
// ledger entry (not shown), and the usage pause rows (the task stays running,
// so only the entry's pause field tells the reader it is held back).
func TestRows(t *testing.T) {
	loc := time.UTC
	tasks := []masterplan.Task{
		task("T1.1", "First"),
		task("T1.2", "Second", "T1.1"),
		task("T1.3", "Third", "T1.1", "T1.2"),
	}
	cases := []struct {
		name string
		in   RowsInput
		want []Row
	}{
		{"empty input", RowsInput{}, nil},
		{"empty plan with ledger", RowsInput{Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Done}}}}, nil},
		{"missing from ledger is pending and ready", RowsInput{Tasks: tasks[:1]},
			[]Row{{ID: "T1.1", Title: "First", Status: "pending", Detail: "ready"}}},
		{"pending with no entry has no session", RowsInput{Tasks: tasks[:1], Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T9.9", Session: "t9-9"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "pending", Detail: "ready"}}},
		{"running with a session carries it", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "t1-1"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "running", Detail: "t1-1", Session: "t1-1"}}},
		{"done keeps its session", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Done, Session: "t1-1", PR: 7}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "done", Detail: "PR #7", Session: "t1-1"}}},
		{"pending with unmet dependencies", RowsInput{Tasks: tasks, Ledger: ledger.Ledger{}},
			[]Row{
				{ID: "T1.1", Title: "First", Status: "pending", Detail: "ready"},
				{ID: "T1.2", Title: "Second", Status: "pending", Detail: "waiting for T1.1"},
				{ID: "T1.3", Title: "Third", Status: "pending", Detail: "waiting for T1.1, T1.2"},
			}},
		{"owner task pending", RowsInput{Tasks: []masterplan.Task{task("T0.0", "Stand up the VM (owner)")}},
			[]Row{{ID: "T0.0", Title: "Stand up the VM (owner)", Status: "pending", Detail: "ready · yours to do", Owner: true}}},
		{"owner task waiting", RowsInput{Tasks: []masterplan.Task{task("T0.0", "VM (owner)", "T9.9")}},
			[]Row{{ID: "T0.0", Title: "VM (owner)", Status: "pending", Detail: "waiting for T9.9 · yours to do", Owner: true}}},
		{"running with phase", RowsInput{Tasks: tasks[:1],
			Ledger:   ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "t1-1"}}},
			Sessions: map[string]string{"t1-1": "executing"}},
			[]Row{{ID: "T1.1", Title: "First", Status: "running", Detail: "t1-1 · executing", Session: "t1-1"}}},
		{"running with unknown phase", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "t1-1"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "running", Detail: "t1-1", Session: "t1-1"}}},
		{"running paused for usage with wake", RowsInput{Tasks: tasks[:1], Loc: loc,
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "t1-1",
				UsagePauseAt: "2026-01-02T10:00:00Z", UsageWakeAt: "2026-01-02T15:04:00Z"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "running", Detail: "t1-1 · paused for usage until Fri 2 Jan 15:04", Session: "t1-1"}}},
		{"running paused for usage without wake", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "t1-1", UsagePauseAt: "2026-01-02T10:00:00Z"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "running", Detail: "t1-1 · paused for usage", Session: "t1-1"}}},
		{"running paused with unparseable wake", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "t1-1", UsagePauseAt: "x", UsageWakeAt: "soon"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "running", Detail: "t1-1 · paused for usage", Session: "t1-1"}}},
		{"shipping with PR", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Shipping, Ship: "ci_green", PR: 12}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "shipping", Detail: "ci green · PR #12"}}},
		{"shipping without PR", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Shipping, Ship: "committed"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "shipping", Detail: "committed"}}},
		{"parked with a mix of open and answered", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Parked, ParkedOn: []string{"a1", "b2", "c3"}}}},
			Items:  []inbox.Item{{ID: "a1"}, {ID: "b2", Answer: "yes"}, {ID: "c3"}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "parked", Detail: "waiting on you: a1, c3"}}},
		{"parked with all answered", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Parked, ParkedOn: []string{"a1"}}}},
			Items:  []inbox.Item{{ID: "a1", Answer: "yes"}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "parked", Detail: "answered, starting again soon"}}},
		{"parked whose item file is missing", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Parked, ParkedOn: []string{"gone"}}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "parked", Detail: "waiting on you: gone"}}},
		{"done with PR and sha", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Done, Session: "t1-1", PR: 7, MergedSHA: "0123456789abcdef"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "done", Detail: "PR #7 · 0123456", Session: "t1-1"}}},
		{"done with only sha", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Done, MergedSHA: "abc"}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "done", Detail: "abc"}}},
		{"done with nothing recorded", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Done}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "done", Detail: ""}}},
		{"failed with attempts", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Failed, Attempts: 3}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "failed", Detail: "attempt 3"}}},
		{"failed without attempts", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Failed}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "failed", Detail: ""}}},
		{"superseded", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Superseded, ReplacedBy: []string{"T1.2", "T1.3"}}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "superseded", Detail: "replaced by T1.2, T1.3"}}},
		{"ledger entry for a task the plan no longer has is not shown", RowsInput{Tasks: tasks[:1],
			Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T9.9", Status: ledger.Done}}}},
			[]Row{{ID: "T1.1", Title: "First", Status: "pending", Detail: "ready"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Rows(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Rows() = %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// TestRowsLocation checks the wake time is shown in the zone given, not the
// machine's. Load-bearing: a zone-less format would show UTC to a reader who
// expects local time.
func TestRowsLocation(t *testing.T) {
	loc := time.FixedZone("plus2", 2*3600)
	got := Rows(RowsInput{
		Tasks: []masterplan.Task{task("T1.1", "First")},
		Loc:   loc,
		Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "s",
			UsagePauseAt: "2026-01-02T10:00:00Z", UsageWakeAt: "2026-01-02T13:04:00Z"}}},
	})
	if want := "s · paused for usage until Fri 2 Jan 15:04"; got[0].Detail != want {
		t.Errorf("Detail = %q, want %q", got[0].Detail, want)
	}
}

// TestRowsStatusWords checks every ledger status is produced as the ledger's
// own word, so the dashboard never invents one.
func TestRowsStatusWords(t *testing.T) {
	for _, st := range ledger.Statuses() {
		t.Run(string(st), func(t *testing.T) {
			rows := Rows(RowsInput{
				Tasks:  []masterplan.Task{task("T1.1", "First")},
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: st}}},
			})
			if len(rows) != 1 || rows[0].Status != string(st) {
				t.Errorf("rows = %#v, want status %q", rows, st)
			}
		})
	}
}

func TestPausedSessions(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		name string
		l    ledger.Ledger
		want map[string]string
	}{
		{"empty", ledger.Ledger{}, map[string]string{}},
		{"running with pause and wake", ledger.Ledger{Tasks: []ledger.Entry{{ID: "a", Status: ledger.Running, Session: "s", UsagePauseAt: "2026-01-02T10:00:00Z", UsageWakeAt: "2026-01-02T15:04:00Z"}}},
			map[string]string{"s": "Fri 2 Jan 15:04"}},
		{"running with pause and no wake", ledger.Ledger{Tasks: []ledger.Entry{{ID: "a", Status: ledger.Running, Session: "s", UsagePauseAt: "2026-01-02T10:00:00Z"}}},
			map[string]string{"s": ""}},
		{"no session is left out", ledger.Ledger{Tasks: []ledger.Entry{{ID: "a", Status: ledger.Running, UsagePauseAt: "2026-01-02T10:00:00Z"}}}, map[string]string{}},
		{"running without pause", ledger.Ledger{Tasks: []ledger.Entry{{ID: "a", Status: ledger.Running, Session: "s"}}}, map[string]string{}},
		{"not running", ledger.Ledger{Tasks: []ledger.Entry{{ID: "a", Status: ledger.Parked, Session: "s", UsagePauseAt: "2026-01-02T10:00:00Z"}}}, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PausedSessions(tc.l, loc); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PausedSessions() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOpenCounts(t *testing.T) {
	cases := []struct {
		name           string
		items          []inbox.Item
		open, blocking int
	}{
		{"none", nil, 0, 0},
		{"mixed", []inbox.Item{{ID: "a", Blocking: true}, {ID: "b"}, {ID: "c", Blocking: true, Answer: "x"}, {ID: "d", Answer: "y"}}, 2, 1},
		{"all answered", []inbox.Item{{ID: "a", Answer: "x", Blocking: true}}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, b := OpenCounts(tc.items)
			if o != tc.open || b != tc.blocking {
				t.Errorf("OpenCounts() = %d, %d, want %d, %d", o, b, tc.open, tc.blocking)
			}
		})
	}
}
