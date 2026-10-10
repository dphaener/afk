package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProviderRoundTrip pins the write/read contract: the name written by
// WriteProviderMarker comes back unchanged from ReadProvider, and the file on
// disk carries a trailing newline so it reads cleanly with `cat` and editors.
func TestProviderRoundTrip(t *testing.T) {
	worktree := t.TempDir()
	sessionDir := filepath.Join(worktree, ".afk", "test-session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}

	if err := WriteProviderMarker(sessionDir, "codex"); err != nil {
		t.Fatalf("WriteProviderMarker() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(sessionDir, "provider"))
	if err != nil {
		t.Fatalf("failed to read provider sidecar: %v", err)
	}
	if string(raw) != "codex\n" {
		t.Errorf("provider sidecar = %q, want %q", raw, "codex\n")
	}

	if got := ReadProvider(worktree, "test-session"); got != "codex" {
		t.Errorf("ReadProvider() = %q, want %q", got, "codex")
	}
}

// TestProviderMissingFile pins the default: a session with no sidecar (every
// session created before this feature, plus every Claude session that never
// wrote one) reads as "", which callers resolve to Claude. A missing session
// dir takes the same path, because the read error is swallowed either way.
func TestProviderMissingFile(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, worktree string)
	}{
		{
			name: "session dir without provider file",
			setup: func(t *testing.T, worktree string) {
				if err := os.MkdirAll(filepath.Join(worktree, ".afk", "test-session"), 0o755); err != nil {
					t.Fatalf("failed to create session dir: %v", err)
				}
			},
		},
		{
			name: "no session dir at all",
			setup: func(t *testing.T, worktree string) {
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			tt.setup(t, worktree)

			if got := ReadProvider(worktree, "test-session"); got != "" {
				t.Errorf("ReadProvider() = %q, want \"\"", got)
			}
		})
	}
}

// TestProviderWhitespaceTrimmed pins the trim: the sidecar is hand-editable and
// may carry stray spaces, tabs, CRLF, or blank lines around the name, and the
// reader must return only the bare name so callers can look it up directly.
func TestProviderWhitespaceTrimmed(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "trailing newline", contents: "codex\n", want: "codex"},
		{name: "leading and trailing spaces", contents: "  codex  ", want: "codex"},
		{name: "CRLF line ending", contents: "codex\r\n", want: "codex"},
		{name: "surrounding blank lines and tabs", contents: "\n\t codex \t\n\n", want: "codex"},
		{name: "empty file", contents: "", want: ""},
		{name: "whitespace only", contents: " \n\t\n", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			sessionDir := filepath.Join(worktree, ".afk", "test-session")
			if err := os.MkdirAll(sessionDir, 0o755); err != nil {
				t.Fatalf("failed to create session dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(sessionDir, "provider"), []byte(tt.contents), 0o644); err != nil {
				t.Fatalf("failed to write provider sidecar: %v", err)
			}

			if got := ReadProvider(worktree, "test-session"); got != tt.want {
				t.Errorf("ReadProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}
