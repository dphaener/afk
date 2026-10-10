package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNextScratchName is the load-bearing matrix for scratch-name allocation.
// The rows that matter most:
//   - empty set -> "scratch-1" (the base case a fresh repo hits)
//   - a gap left by a cleaned session (scratch-1, scratch-3) is reused as
//     "scratch-2" rather than counting forever upward to "scratch-4"
//   - non-"scratch-" names are ignored, so an unrelated session like
//     "feat-x" never perturbs the allocation
//   - malformed "scratch-" names (scratch- alone, scratch-0, scratch-x,
//     scratch-01) are ignored rather than crashing or corrupting the count —
//     these are names a human or a stale directory could plausibly produce
//     and must not panic strconv.Atoi or be misparsed as a valid index
//   - every returned name passes session.ValidateName, asserted here rather
//     than by restating the kebab-case regex
func TestNextScratchName(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     string
	}{
		{"empty set", nil, "scratch-1"},
		{"empty slice", []string{}, "scratch-1"},
		{"gap reused", []string{"scratch-1", "scratch-3", "feat-x"}, "scratch-2"},
		{"no gap counts up", []string{"scratch-1", "scratch-2"}, "scratch-3"},
		{"unrelated names ignored", []string{"feat-x", "add-billing-webhook"}, "scratch-1"},
		{"malformed: trailing hyphen only", []string{"scratch-"}, "scratch-1"},
		{"malformed: zero index", []string{"scratch-0"}, "scratch-1"},
		{"malformed: non-numeric suffix", []string{"scratch-x"}, "scratch-1"},
		{"malformed: leading zero", []string{"scratch-01"}, "scratch-1"},
		{"malformed names don't block a real gap", []string{"scratch-01", "scratch-2"}, "scratch-1"},
		{"negative-looking suffix ignored", []string{"scratch--1"}, "scratch-1"},
		{"duplicate entries", []string{"scratch-1", "scratch-1"}, "scratch-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextScratchName(tt.existing)
			if got != tt.want {
				t.Errorf("NextScratchName(%v) = %q, want %q", tt.existing, got, tt.want)
			}
			if err := ValidateName(got); err != nil {
				t.Errorf("NextScratchName(%v) = %q, fails ValidateName: %v", tt.existing, got, err)
			}
		})
	}
}

// TestNextScratchNameAlwaysValid is a property check across a wider range of
// existing-name shapes: whatever NextScratchName returns must satisfy
// session.ValidateName, since that result is later used as a branch name, a
// worktree directory, and a plan slug (see nameRE's doc comment).
func TestNextScratchNameAlwaysValid(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{"scratch-1"},
		{"scratch-1", "scratch-2", "scratch-3", "scratch-4", "scratch-5"},
		{"scratch-2", "scratch-4", "scratch-6"},
		{"scratch-", "scratch-0", "scratch-x", "scratch-01", "scratch--1"},
	}
	for _, existing := range cases {
		got := NextScratchName(existing)
		if err := ValidateName(got); err != nil {
			t.Errorf("NextScratchName(%v) = %q, fails ValidateName: %v", existing, got, err)
		}
	}
}

// TestListSessionDirs exercises the impure sibling of NextScratchName: it
// lists the entries of {repo}/.afk/ by name, which is the only I/O this
// feature needs and is kept out of the pure function per this repo's
// pure-decision-function convention (see shouldHandOffToDash in new.go).
func TestListSessionDirs(t *testing.T) {
	t.Run("no .afk directory yields no names, not an error", func(t *testing.T) {
		repo := t.TempDir()
		got, err := ListSessionDirs(repo)
		if err != nil {
			t.Fatalf("ListSessionDirs(%q) error = %v, want nil", repo, err)
		}
		if len(got) != 0 {
			t.Errorf("ListSessionDirs(%q) = %v, want empty", repo, got)
		}
	})

	t.Run("lists only directory entries, ignoring stray files", func(t *testing.T) {
		repo := t.TempDir()
		afkDir := filepath.Join(repo, ".afk")
		for _, name := range []string{"scratch-1", "add-billing-webhook"} {
			if err := os.MkdirAll(filepath.Join(afkDir, name), 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
		}
		if err := os.WriteFile(filepath.Join(afkDir, "stray-file"), []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := ListSessionDirs(repo)
		if err != nil {
			t.Fatalf("ListSessionDirs(%q) error = %v, want nil", repo, err)
		}
		want := map[string]bool{"scratch-1": true, "add-billing-webhook": true}
		if len(got) != len(want) {
			t.Fatalf("ListSessionDirs(%q) = %v, want entries %v", repo, got, want)
		}
		for _, n := range got {
			if !want[n] {
				t.Errorf("ListSessionDirs(%q) returned unexpected entry %q", repo, n)
			}
		}
	})
}
