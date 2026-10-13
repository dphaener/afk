package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadTurnFailed is a table test in the shape of TestReadRateLimited.
// Load-bearing rows: "valid" pins the marker's time and reason as the
// returned value; "valid without reason" pins that a missing reason still
// counts, since the time alone is enough to wake a session. "missing" and
// "not JSON" pin ok false for the two cases the daemon meets on every healthy
// session. "JSON without at", "empty at" and "bad time" pin that a marker
// with no usable time never counts, because the daemon cannot tell it from a
// marker left over from an earlier failure. Each row writes its own worktree
// under t.TempDir, so no real session is touched.
func TestReadTurnFailed(t *testing.T) {
	const session = "demo"
	want := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name       string
		content    *string // nil means no marker file at all
		wantAt     time.Time
		wantReason string
		wantOK     bool
	}{
		{name: "valid", content: strPtr(`{"at":"2026-10-13T09:30:00Z","reason":"server_error"}` + "\n"), wantAt: want, wantReason: "server_error", wantOK: true},
		{name: "valid without reason", content: strPtr(`{"at":"2026-10-13T09:30:00Z"}` + "\n"), wantAt: want, wantOK: true},
		{name: "missing", content: nil, wantOK: false},
		{name: "empty", content: strPtr(""), wantOK: false},
		{name: "not JSON", content: strPtr("connection lost"), wantOK: false},
		{name: "JSON without at", content: strPtr(`{"when":"2026-10-13T09:30:00Z","reason":"unknown"}`), wantOK: false},
		{name: "empty at", content: strPtr(`{"at":"","reason":"unknown"}`), wantOK: false},
		{name: "bad time", content: strPtr(`{"at":"yesterday afternoon","reason":"unknown"}`), wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			if tt.content != nil {
				dir := filepath.Join(worktree, ".afk", session)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".turn-failed"), []byte(*tt.content), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			got, ok := ReadTurnFailed(worktree, session)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got=%+v)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				if !got.At.IsZero() || got.Reason != "" {
					t.Errorf("not ok but got %+v, want zero value", got)
				}
				return
			}
			if !got.At.Equal(tt.wantAt) {
				t.Errorf("at = %v, want %v", got.At, tt.wantAt)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}
