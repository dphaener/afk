package daemon

import (
	"testing"
	"time"

	"github.com/dphaener/afk/internal/agentreport"
)

// TestAgentWaitSince is agentWaitSince as a table: when a session is waiting
// on the owner, and from when.
//
// Load-bearing rows, and what breaks without them:
//
//   - "no hook and no report is not waiting": a zero report must leave the
//     stall rule exactly as it was, and "hook alone starts at its reference"
//     is the same rule with the hook on.
//   - "fresh blocked report alone starts at its time": the report is a wait
//     with no hook write, measured from when the host saw it.
//   - "report earlier than the hook wins" and "hook earlier than the report
//     wins": the earlier of the two starts the wait, whichever it is.
//   - "report equal to the last exit is stale", "report before the host
//     start is stale" and "working report is not a wait": only a fresh
//     blocked report counts, so a dead agent's record never pages anyone.
//   - "floor later than the report starts at the floor": a wake or an answer
//     restarts the clock of a report that keeps its time while blocked.
//   - "floor never moves the hook": the hook's reference is lastActivity,
//     which already holds the floor, and is used as it is.
func TestAgentWaitSince(t *testing.T) {
	hostStart := now.Add(-time.Hour)
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	blocked := func(kind string, d time.Duration) agentreport.Report {
		return agentreport.Report{State: "blocked", Kind: kind, Msg: "Approve?", At: at(d)}
	}

	cases := []struct {
		name      string
		view      SessionView
		hookRef   time.Time
		floor     time.Time
		wantOK    bool
		wantSince time.Time
	}{
		{
			name:    "no hook and no report is not waiting",
			view:    SessionView{HostStartedAt: hostStart},
			hookRef: at(time.Minute),
			floor:   hostStart,
		},
		{
			name:      "hook alone starts at its reference",
			view:      SessionView{AwaitingInput: true, HostStartedAt: hostStart},
			hookRef:   at(5 * time.Minute),
			floor:     hostStart,
			wantOK:    true,
			wantSince: at(5 * time.Minute),
		},
		{
			name:      "fresh blocked report alone starts at its time",
			view:      SessionView{Report: blocked("question", 7*time.Minute), HostStartedAt: hostStart},
			hookRef:   at(time.Second),
			floor:     hostStart,
			wantOK:    true,
			wantSince: at(7 * time.Minute),
		},
		{
			name:      "every blocked kind counts",
			view:      SessionView{Report: blocked("auth", 7*time.Minute), HostStartedAt: hostStart},
			floor:     hostStart,
			wantOK:    true,
			wantSince: at(7 * time.Minute),
		},
		{
			name: "report earlier than the hook wins",
			view: SessionView{
				AwaitingInput: true, Report: blocked("question", 12*time.Minute), HostStartedAt: hostStart,
			},
			hookRef:   at(5 * time.Minute),
			floor:     hostStart,
			wantOK:    true,
			wantSince: at(12 * time.Minute),
		},
		{
			name: "hook earlier than the report wins",
			view: SessionView{
				AwaitingInput: true, Report: blocked("permission", 2*time.Minute), HostStartedAt: hostStart,
			},
			hookRef:   at(5 * time.Minute),
			floor:     hostStart,
			wantOK:    true,
			wantSince: at(5 * time.Minute),
		},
		{
			name: "working report is not a wait",
			view: SessionView{
				Report:        agentreport.Report{State: "working", Msg: "Approve?", At: at(30 * time.Minute)},
				HostStartedAt: hostStart,
			},
			floor: hostStart,
		},
		{
			name: "report equal to the last exit is stale",
			view: SessionView{
				Report: blocked("question", 30*time.Minute), HostStartedAt: hostStart, LastExitAt: at(30 * time.Minute),
			},
			floor: hostStart,
		},
		{
			name: "report before the host start is stale",
			view: SessionView{
				Report: blocked("question", 30*time.Minute), HostStartedAt: at(20 * time.Minute),
			},
			floor: hostStart,
		},
		{
			name: "stale report leaves the hook alone",
			view: SessionView{
				AwaitingInput: true, Report: blocked("question", 30*time.Minute),
				HostStartedAt: hostStart, LastExitAt: at(25 * time.Minute),
			},
			hookRef:   at(3 * time.Minute),
			floor:     hostStart,
			wantOK:    true,
			wantSince: at(3 * time.Minute),
		},
		{
			name:      "floor later than the report starts at the floor",
			view:      SessionView{Report: blocked("question", 30*time.Minute), HostStartedAt: hostStart},
			floor:     at(5 * time.Minute),
			wantOK:    true,
			wantSince: at(5 * time.Minute),
		},
		{
			name: "floor never moves the hook",
			view: SessionView{
				AwaitingInput: true, Report: blocked("question", 30*time.Minute), HostStartedAt: hostStart,
			},
			hookRef:   at(8 * time.Minute),
			floor:     at(5 * time.Minute),
			wantOK:    true,
			wantSince: at(8 * time.Minute),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.view
			since, ok := agentWaitSince(&v, tc.hookRef, tc.floor)
			if ok != tc.wantOK {
				t.Fatalf("waiting = %v, want %v", ok, tc.wantOK)
			}
			if ok && !since.Equal(tc.wantSince) {
				t.Errorf("since = %s, want %s", since.Format(time.RFC3339), tc.wantSince.Format(time.RFC3339))
			}
		})
	}
}
