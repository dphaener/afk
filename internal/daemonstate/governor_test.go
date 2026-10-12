package daemonstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestGovernorRoundTrip: the never-checked row is load-bearing because empty
// times and false booleans must survive as real values, not be treated as
// missing; the unknown-login row pins that LoginKnown false is kept apart from
// LoggedIn false, since an unknown login is not logged out.
func TestGovernorRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		rec  GovernorRecord
	}{
		{"never checked", GovernorRecord{}},
		{"logged in and probed", GovernorRecord{
			ProbeTriedAt:   "2026-10-12T09:00:00Z",
			LoginCheckedAt: "2026-10-12T08:30:00Z",
			LoginKnown:     true,
			LoggedIn:       true,
		}},
		{"logged out", GovernorRecord{
			LoginCheckedAt: "2026-10-12T08:30:00Z",
			LoginKnown:     true,
		}},
		{"login check could not run", GovernorRecord{
			ProbeTriedAt: "2026-10-12T09:00:00Z",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteGovernor(dir, tt.rec); err != nil {
				t.Fatal(err)
			}
			got, ok := ReadGovernor(dir)
			if !ok {
				t.Fatal("ReadGovernor false after WriteGovernor")
			}
			if got != tt.rec {
				t.Errorf("got %+v, want %+v", got, tt.rec)
			}
		})
	}
}

// TestGovernorKeys: the row is load-bearing because every key must be written
// even when zero, so a reader never has to tell absent from zero.
func TestGovernorKeys(t *testing.T) {
	dir := t.TempDir()
	if err := WriteGovernor(dir, GovernorRecord{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(GovernorPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"probe_tried_at", "login_checked_at", "login_known", "logged_in"} {
		if _, ok := top[k]; !ok {
			t.Errorf("missing key %q in %s", k, raw)
		}
	}
	if len(top) != 4 {
		t.Errorf("keys = %d, want 4", len(top))
	}
}

// TestGovernorPath: the row is load-bearing because the record sits directly
// in the state folder beside usage.json, not in a project's .afk/build/.
func TestGovernorPath(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		want string
	}{
		{"state folder", "/home/u/.local/state/afk", "/home/u/.local/state/afk/governor.json"},
		{"relative folder", "state", "state/governor.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GovernorPath(tt.dir); got != tt.want {
				t.Errorf("GovernorPath = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGovernorMissing: the row is load-bearing because a fresh machine has no
// record yet, and that must read as "not there" rather than an error.
func TestGovernorMissing(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadGovernor(dir); ok {
		t.Error("ReadGovernor ok for a missing file")
	}
}

// TestGovernorDamaged: every row must report "not there" without a panic. The
// garbage and empty rows are the cases a torn or hand-edited file produces;
// the truncated row is a JSON prefix that a reader of a half-written file
// would meet.
func TestGovernorDamaged(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"garbage", "not json {{"},
		{"empty file", ""},
		{"truncated", `{"probe_tried_at":"2026-10-12T09:00:00Z","logged_`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "governor.json"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok := ReadGovernor(dir); ok {
				t.Error("ReadGovernor ok for a damaged file")
			}
		})
	}
}
