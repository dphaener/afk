package web

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
)

var summaryNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func planTasks(ids ...string) []masterplan.Task {
	out := make([]masterplan.Task, 0, len(ids))
	for _, id := range ids {
		out = append(out, masterplan.Task{ID: id, Title: "Title of " + id})
	}
	return out
}

// TestSummarize covers every branch of the summary. Each row gives the whole
// expected value, so a field that changes by accident fails the row.
//
// Load-bearing rows:
//   - "running, paused for usage": a session the daemon stopped to save usage
//     has no host and a status.json that still says executing. Without the
//     ledger's usage_pause_at winning, the page would show it as working, or
//     as dead (PLAN T2.4).
//   - "stop marker beats a newer done entry": the line is stopped until the
//     owner resumes; a later "shipped" must not hide that.
//   - "superseded is left out of the total": a superseded task can never be
//     done, so counting it would leave a total that is never reached.
//   - "parked is shown only when nothing is moving": the running task after
//     it in the plan wins, although the parked one comes first.
//   - "plan or ledger error": the request still answers, with the error as
//     the problem and no counts guessed from half the data.
func TestSummarize(t *testing.T) {
	nothing := LastShip{Text: "Nothing shipped yet"}
	loadErr := errors.New("ledger file is damaged: /p/.afk/build/ledger.json: bad JSON. Fix or delete it")

	tests := []struct {
		name string
		in   SummaryInput
		want ProjectSummary
	}{
		{
			name: "empty project",
			in:   SummaryInput{Name: "app", Now: summaryNow},
			want: ProjectSummary{Name: "app", LastShip: nothing},
		},
		{
			name: "pending tasks only",
			in:   SummaryInput{Name: "app", Tasks: planTasks("T1.1", "T1.2"), Now: summaryNow},
			want: ProjectSummary{Name: "app", Total: 2, LastShip: nothing},
		},
		{
			name: "running, phase from the session",
			in: SummaryInput{
				Name:  "app",
				Tasks: planTasks("T1.1", "T1.2"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.1", Status: ledger.Done, FinishedAt: "2026-10-12T09:00:00Z", PR: 7, MergedSHA: "abc123"},
					{ID: "T1.2", Status: ledger.Running, Session: "t1-2"},
				}},
				Sessions: []SessionView{
					{Name: "other", Phase: "planning"},
					{Name: "t1-2", Phase: "executing", HostRunning: true},
				},
				Now: summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T1.2", Title: "Title of T1.2", Status: "running", Session: "t1-2", Phase: "executing", WaitingOn: []string{}},
				Done:    1, Total: 2,
				LastShip: LastShip{Task: "T1.1", PR: 7, MergedSHA: "abc123", FinishedAt: "2026-10-12T09:00:00Z", Text: "T1.1 shipped 3 hours ago"},
			},
		},
		{
			name: "running, session missing",
			in: SummaryInput{
				Name:   "app",
				Tasks:  planTasks("T1.1"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "t1-1"}}},
				Now:    summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T1.1", Title: "Title of T1.1", Status: "running", Session: "t1-1", Phase: PhaseUnknown, WaitingOn: []string{}},
				Total:   1, LastShip: nothing,
			},
		},
		{
			name: "running, session status stale",
			in: SummaryInput{
				Name:     "app",
				Tasks:    planTasks("T1.1"),
				Ledger:   ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running, Session: "t1-1"}}},
				Sessions: []SessionView{{Name: "t1-1", Phase: "executing", Stale: true}},
				Now:      summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T1.1", Title: "Title of T1.1", Status: "running", Session: "t1-1", Phase: PhaseUnknown, WaitingOn: []string{}},
				Total:   1, LastShip: nothing,
			},
		},
		{
			name: "running, paused for usage",
			in: SummaryInput{
				Name:  "app",
				Tasks: planTasks("T1.1"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.1", Status: ledger.Running, Session: "t1-1", UsagePauseAt: "2026-10-12T11:00:00Z"},
				}},
				// The session's files still say it is working and its
				// host is gone: exactly what a usage pause leaves behind.
				Sessions: []SessionView{{Name: "t1-1", Phase: "executing", HostRunning: false}},
				Now:      summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T1.1", Title: "Title of T1.1", Status: "running", Session: "t1-1", Phase: PhaseUsagePaused, WaitingOn: []string{}},
				Total:   1, LastShip: nothing,
			},
		},
		{
			name: "shipping shows the ship word",
			in: SummaryInput{
				Name:   "app",
				Tasks:  planTasks("T1.1"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Ship: "pr_open", PR: 9}}},
				// A session phase must not replace the ship word.
				Sessions: []SessionView{{Name: "t1-1", Phase: "complete"}},
				Now:      summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T1.1", Title: "Title of T1.1", Status: "shipping", Session: "t1-1", Phase: "pr_open", WaitingOn: []string{}},
				Total:   1, LastShip: nothing,
			},
		},
		{
			name: "shipping with no ship word yet",
			in: SummaryInput{
				Name:   "app",
				Tasks:  planTasks("T1.1"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1"}}},
				Now:    summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T1.1", Title: "Title of T1.1", Status: "shipping", Session: "t1-1", Phase: PhaseShipping, WaitingOn: []string{}},
				Total:   1, LastShip: nothing,
			},
		},
		{
			name: "parked, waiting on the owner",
			in: SummaryInput{
				Name:  "app",
				Tasks: planTasks("T1.1", "T1.2", "T1.3"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.2", Status: ledger.Parked, Session: "t1-2", ParkedOn: []string{"abc234", "def567"}},
					{ID: "T1.3", Status: ledger.Parked, ParkedOn: []string{"ghj892"}},
				}},
				Now: summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T1.2", Title: "Title of T1.2", Status: "parked", Session: "t1-2", Phase: PhaseWaiting, WaitingOn: []string{"abc234", "def567"}},
				Total:   3, LastShip: nothing,
			},
		},
		{
			name: "parked is shown only when nothing is moving",
			in: SummaryInput{
				Name:  "app",
				Tasks: planTasks("T1.1", "T1.2", "T1.3"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.1", Status: ledger.Parked, ParkedOn: []string{"abc234"}},
					{ID: "T1.2", Status: ledger.Failed},
					{ID: "T1.3", Status: ledger.Shipping, Ship: "ci_green"},
				}},
				Now: summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T1.3", Title: "Title of T1.3", Status: "shipping", Phase: "ci_green", WaitingOn: []string{}},
				Total:   3, LastShip: nothing,
			},
		},
		{
			name: "two in progress, the first in plan order",
			in: SummaryInput{
				Name: "app",
				// The plan's order decides, not the ledger's.
				Tasks: planTasks("T2.1", "T1.1"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.1", Status: ledger.Shipping, Ship: "pushed"},
					{ID: "T2.1", Status: ledger.Running, Session: "t2-1"},
				}},
				Sessions: []SessionView{{Name: "t2-1", Phase: "reviewing"}},
				Now:      summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Current: &CurrentTask{Task: "T2.1", Title: "Title of T2.1", Status: "running", Session: "t2-1", Phase: "reviewing", WaitingOn: []string{}},
				Total:   2, LastShip: nothing,
			},
		},
		{
			name: "superseded is left out of the total",
			in: SummaryInput{
				Name:  "app",
				Tasks: planTasks("T1.1", "T1.2", "T1.2a", "T1.2b"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.1", Status: ledger.Done, FinishedAt: "2026-10-10T12:00:00Z"},
					{ID: "T1.2", Status: ledger.Superseded, ReplacedBy: []string{"T1.2a", "T1.2b"}},
					{ID: "T1.2a", Status: ledger.Done, FinishedAt: "2026-10-11T12:00:00Z"},
					// Done, but not in the plan: not counted.
					{ID: "T9.9", Status: ledger.Done, FinishedAt: "2026-10-01T12:00:00Z"},
				}},
				Now: summaryNow,
			},
			want: ProjectSummary{
				Name: "app",
				Done: 2, Total: 3,
				LastShip: LastShip{Task: "T1.2a", FinishedAt: "2026-10-11T12:00:00Z", Text: "T1.2a shipped 1 day ago"},
			},
		},
		{
			name: "stop marker beats a newer done entry",
			in: SummaryInput{
				Name:  "app",
				Tasks: planTasks("T1.1", "T1.2"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.1", Status: ledger.Done, FinishedAt: "2026-10-12T11:59:00Z", PR: 4},
					{ID: "T1.2", Status: ledger.Failed},
				}},
				Stopped:    true,
				StopTask:   "T1.2",
				StopReason: "the deploy health check timed out",
				Now:        summaryNow,
			},
			want: ProjectSummary{
				Name:    "app",
				Stopped: &StopView{Task: "T1.2", Reason: "the deploy health check timed out"},
				Done:    1, Total: 2,
				LastShip: LastShip{Task: "T1.2", Stopped: true, Text: "T1.2 stopped the build: the deploy health check timed out"},
			},
		},
		{
			name: "damaged stop marker still reads as stopped",
			in:   SummaryInput{Name: "app", Tasks: planTasks("T1.1"), Stopped: true, Now: summaryNow},
			want: ProjectSummary{
				Name:     "app",
				Stopped:  &StopView{},
				Total:    1,
				LastShip: LastShip{Stopped: true, Text: "The build is stopped. The reason could not be read."},
			},
		},
		{
			name: "newest done entry by finish time, not by order",
			in: SummaryInput{
				Name:  "app",
				Tasks: planTasks("T1.1", "T1.2", "T1.3"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.1", Status: ledger.Done, FinishedAt: "2026-10-12T11:30:00Z"},
					{ID: "T1.2", Status: ledger.Done, FinishedAt: "2026-10-12T11:59:30Z", PR: 12, MergedSHA: "fff000"},
					{ID: "T1.3", Status: ledger.Done, FinishedAt: "2026-10-12T10:00:00Z"},
				}},
				Now: summaryNow,
			},
			want: ProjectSummary{
				Name: "app",
				Done: 3, Total: 3,
				LastShip: LastShip{Task: "T1.2", PR: 12, MergedSHA: "fff000", FinishedAt: "2026-10-12T11:59:30Z", Text: "T1.2 shipped just now"},
			},
		},
		{
			name: "done entries with no finish time: the later task",
			in: SummaryInput{
				Name:  "app",
				Tasks: planTasks("T1.1", "T1.2"),
				Ledger: ledger.Ledger{Tasks: []ledger.Entry{
					{ID: "T1.1", Status: ledger.Done},
					{ID: "T1.2", Status: ledger.Done},
				}},
				Now: summaryNow,
			},
			want: ProjectSummary{
				Name: "app",
				Done: 2, Total: 2,
				LastShip: LastShip{Task: "T1.2", Text: "T1.2 shipped"},
			},
		},
		{
			name: "plan or ledger error",
			in: SummaryInput{
				Name: "app",
				// Half-read data must be ignored when the load failed.
				Tasks:   planTasks("T1.1"),
				Ledger:  ledger.Ledger{Tasks: []ledger.Entry{{ID: "T1.1", Status: ledger.Running}}},
				LoadErr: loadErr,
				Paused:  true,
				Now:     summaryNow,
			},
			want: ProjectSummary{
				Name:     "app",
				Problem:  loadErr.Error(),
				Paused:   true,
				LastShip: LastShip{Text: "Not known, because the plan or the ledger could not be read"},
			},
		},
		{
			name: "plan or ledger error with a stop marker",
			in: SummaryInput{
				Name:       "app",
				LoadErr:    loadErr,
				Stopped:    true,
				StopReason: "the deploy health check timed out",
				Now:        summaryNow,
			},
			want: ProjectSummary{
				Name:     "app",
				Problem:  loadErr.Error(),
				Stopped:  &StopView{Reason: "the deploy health check timed out"},
				LastShip: LastShip{Stopped: true, Text: "The build is stopped: the deploy health check timed out"},
			},
		},
		{
			name: "paused, daemon state and inbox counts",
			in: SummaryInput{
				Name:       "app",
				Paused:     true,
				State:      daemonstate.State{At: "2026-10-12T11:58:00Z", Kind: "wait", Task: "T1.1", Session: "t1-1", Reason: "the project is paused"},
				StateKnown: true,
				Items: []inbox.Item{
					{ID: "abc234", Blocking: true},
					{ID: "def567", Blocking: false},
					{ID: "ghj892", Blocking: true, Answer: "yes"},
					{ID: "kmn345", Blocking: false, Answer: "ok"},
				},
				Now: summaryNow,
			},
			want: ProjectSummary{
				Name:      "app",
				Paused:    true,
				State:     &StateView{Kind: "wait", Reason: "the project is paused", At: "2026-10-12T11:58:00Z"},
				OpenItems: 2, OpenBlocking: 1,
				LastShip: nothing,
			},
		},
		{
			name: "daemon state not read",
			// A State value with StateKnown false is leftover and not shown.
			in:   SummaryInput{Name: "app", State: daemonstate.State{Kind: "idle"}, Now: summaryNow},
			want: ProjectSummary{Name: "app", LastShip: nothing},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Summarize(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Summarize mismatch\n got: %s\nwant: %s", dump(got), dump(tc.want))
			}
		})
	}
}

// TestStopTextShapes covers the stop marker's four shapes. The marker is
// written whole, so a task without a reason should not happen; the rows are
// here because a line that says "stopped" with no sentence after it would
// leave the owner with nothing to act on.
func TestStopTextShapes(t *testing.T) {
	tests := []struct {
		name, task, reason, want string
	}{
		{"task and reason", "T1.2", "deploy failed", "T1.2 stopped the build: deploy failed"},
		{"reason only", "", "deploy failed", "The build is stopped: deploy failed"},
		{"task only", "T1.2", "", "T1.2 stopped the build. The reason was not recorded."},
		{"neither", "", "", "The build is stopped. The reason could not be read."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Summarize(SummaryInput{Stopped: true, StopTask: tc.task, StopReason: tc.reason}).LastShip
			if got.Text != tc.want || !got.Stopped || got.Task != tc.task {
				t.Errorf("LastShip = %+v, want text %q", got, tc.want)
			}
		})
	}
}

// TestSince covers the "how long ago" wording. The load-bearing rows are the
// ones that return nothing: a finish time in the future or a missing one must
// drop the phrase, not print "0 minutes ago" or a negative number.
func TestSince(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		now  time.Time
		want string
	}{
		{"no time", time.Time{}, summaryNow, ""},
		{"no clock", summaryNow, time.Time{}, ""},
		{"in the future", summaryNow.Add(time.Minute), summaryNow, ""},
		{"same instant", summaryNow, summaryNow, "just now"},
		{"under a minute", summaryNow.Add(-59 * time.Second), summaryNow, "just now"},
		{"one minute", summaryNow.Add(-time.Minute), summaryNow, "1 minute ago"},
		{"minutes", summaryNow.Add(-59 * time.Minute), summaryNow, "59 minutes ago"},
		{"one hour", summaryNow.Add(-time.Hour), summaryNow, "1 hour ago"},
		{"hours", summaryNow.Add(-23*time.Hour - 59*time.Minute), summaryNow, "23 hours ago"},
		{"one day", summaryNow.Add(-24 * time.Hour), summaryNow, "1 day ago"},
		{"days", summaryNow.Add(-10 * 24 * time.Hour), summaryNow, "10 days ago"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := since(tc.at, tc.now); got != tc.want {
				t.Errorf("since = %q, want %q", got, tc.want)
			}
		})
	}
}
