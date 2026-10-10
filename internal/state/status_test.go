package state

import (
	"path/filepath"
	"testing"
)

func statusPath(name string) string {
	return filepath.Join("testdata", "status", name, "status.json")
}

func TestReadStatus_Valid(t *testing.T) {
	res := ReadStatus(statusPath("valid-session"))
	if res.Stale {
		t.Fatalf("Stale = true, Reason = %q, Detail = %q; want a valid read", res.Reason, res.Detail)
	}
	want := Status{
		SessionID:     "a9313053-40f5-4d3c-8081-1202a1520d34",
		PlanName:      "valid-session",
		Phase:         PhaseExecuting,
		Branch:        "valid-session",
		BaseBranch:    "main",
		LoopCount:     0,
		AwaitingInput: false,
		TmuxWindow:    "@12",
		UpdatedAt:     "2026-07-09T21:14:03Z",
	}
	if res.Status != want {
		t.Errorf("Status = %+v, want %+v", res.Status, want)
	}
}

func TestReadStatus_PlanningPhaseAllowsEmptySessionID(t *testing.T) {
	res := ReadStatus(statusPath("planning-session"))
	if res.Stale {
		t.Fatalf("Stale = true, Reason = %q; planning phase must tolerate an empty session_id", res.Reason)
	}
	if res.Status.Phase != PhasePlanning {
		t.Errorf("Phase = %q, want planning", res.Status.Phase)
	}
}

func TestReadStatus_StaleCases(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantReason StaleReason
	}{
		{"missing file", statusPath("does-not-exist"), StaleMissing},
		{"truncated JSON (torn read)", statusPath("truncated-session"), StaleUnparseable},
		{"unknown field", statusPath("unknown-key-session"), StaleUnknownField},
		{"invalid phase", statusPath("invalid-phase-session"), StaleInvalidPhase},
		{"plan_name mismatch", statusPath("mismatched-name"), StaleNameMismatch},
		{"invariant 2 violation", statusPath("invariant-violation-session"), StaleInvariant},
		{"trailing garbage after object", statusPath("trailing-garbage-session"), StaleTrailingData},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ReadStatus(tt.path)
			if !res.Stale {
				t.Fatalf("Stale = false, want true (Status = %+v)", res.Status)
			}
			if res.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q (Detail = %q)", res.Reason, tt.wantReason, res.Detail)
			}
			if res.Status != (Status{}) {
				t.Errorf("Status = %+v, want zero value on a stale result", res.Status)
			}
		})
	}
}

func TestPhase_Terminal(t *testing.T) {
	tests := []struct {
		phase Phase
		want  bool
	}{
		{PhasePlanning, false},
		{PhaseExecuting, false},
		{PhaseReviewing, false},
		{PhaseFixing, false},
		{PhaseDone, true},
		{PhaseFailed, true},
	}
	for _, tt := range tests {
		if got := tt.phase.Terminal(); got != tt.want {
			t.Errorf("Phase(%q).Terminal() = %v, want %v", tt.phase, got, tt.want)
		}
	}
}

func TestReadStatus_NeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReadStatus panicked: %v", r)
		}
	}()
	// A path whose parent directory doesn't even exist.
	ReadStatus(filepath.Join("testdata", "status", "no-such-dir", "status.json"))
}
