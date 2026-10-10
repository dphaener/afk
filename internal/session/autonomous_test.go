package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsAutonomous(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, worktree string)
		expected bool
	}{
		{
			name: "present file returns true",
			setup: func(t *testing.T, worktree string) {
				sessionDir := filepath.Join(worktree, ".afk", "test-session")
				if err := os.MkdirAll(sessionDir, 0o755); err != nil {
					t.Fatalf("failed to create session dir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(sessionDir, "autonomous"), []byte{}, 0o644); err != nil {
					t.Fatalf("failed to write autonomous marker: %v", err)
				}
			},
			expected: true,
		},
		{
			name: "missing file returns false",
			setup: func(t *testing.T, worktree string) {
				sessionDir := filepath.Join(worktree, ".afk", "test-session")
				if err := os.MkdirAll(sessionDir, 0o755); err != nil {
					t.Fatalf("failed to create session dir: %v", err)
				}
				// Don't create the marker file
			},
			expected: false,
		},
		{
			name: "directory at path returns true",
			setup: func(t *testing.T, worktree string) {
				sessionDir := filepath.Join(worktree, ".afk", "test-session")
				if err := os.MkdirAll(sessionDir, 0o755); err != nil {
					t.Fatalf("failed to create session dir: %v", err)
				}
				if err := os.MkdirAll(filepath.Join(sessionDir, "autonomous"), 0o755); err != nil {
					t.Fatalf("failed to create autonomous directory: %v", err)
				}
			},
			expected: true,
		},
		{
			name: "missing session dir returns false",
			setup: func(t *testing.T, worktree string) {
				// Don't create anything
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			tt.setup(t, worktree)

			got := IsAutonomous(worktree, "test-session")
			if got != tt.expected {
				t.Errorf("IsAutonomous() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestWriteAutonomousMarker(t *testing.T) {
	worktree := t.TempDir()
	sessionDir := filepath.Join(worktree, ".afk", "test-session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}

	if err := WriteAutonomousMarker(sessionDir); err != nil {
		t.Fatalf("WriteAutonomousMarker() failed: %v", err)
	}

	markerPath := filepath.Join(sessionDir, "autonomous")
	if _, err := os.Stat(markerPath); err != nil {
		t.Errorf("marker file not created: %v", err)
	}

	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Errorf("failed to read marker: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("marker file should be empty, got %d bytes", len(data))
	}

	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatalf("failed to stat marker: %v", err)
	}
	if info.Mode() != 0o644 {
		t.Errorf("marker file perms = %o, want 0644", info.Mode())
	}
}
