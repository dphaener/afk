package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadRateLimited is a table test. Load-bearing rows: "valid" pins the
// marker's time as the returned value; "missing" and "not JSON" pin ok false
// for the two cases the daemon meets on every healthy session; "no at key"
// and "bad time" pin that a marker with no usable time never counts, since
// the caller cannot tell it from a marker left over before the last restart.
// Each row writes its own worktree under t.TempDir, so no real session is
// touched.
func TestReadRateLimited(t *testing.T) {
	const session = "demo"
	want := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name    string
		content *string // nil means no marker file at all
		wantAt  time.Time
		wantOK  bool
	}{
		{name: "valid", content: strPtr(`{"at":"2026-10-12T09:30:00Z"}` + "\n"), wantAt: want, wantOK: true},
		{name: "missing", content: nil, wantOK: false},
		{name: "empty", content: strPtr(""), wantOK: false},
		{name: "not JSON", content: strPtr("rate limited"), wantOK: false},
		{name: "JSON without at", content: strPtr(`{"when":"2026-10-12T09:30:00Z"}`), wantOK: false},
		{name: "empty at", content: strPtr(`{"at":""}`), wantOK: false},
		{name: "bad time", content: strPtr(`{"at":"yesterday afternoon"}`), wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			if tt.content != nil {
				dir := filepath.Join(worktree, ".afk", session)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".rate-limited"), []byte(*tt.content), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			at, ok := ReadRateLimited(worktree, session)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (at=%v)", ok, tt.wantOK, at)
			}
			if tt.wantOK && !at.Equal(tt.wantAt) {
				t.Errorf("at = %v, want %v", at, tt.wantAt)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
