package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dphaener/afk/internal/host"
)

// TestUnattendedMarker is the marker's round trip and its reader's edges.
// Load-bearing rows: "written marker is read back" ties the writer and the
// reader to one path, and that path to host.UnattendedMarkerName, which is
// the name the host looks for when it restarts a child; if the two packages
// ever disagreed, a restarted session would silently lose the mode. "missing
// marker" is every session that exists today and must stay off.
func TestUnattendedMarker(t *testing.T) {
	const name = "test-session"
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		mkdir bool
		want  bool
	}{
		{
			name:  "written marker is read back",
			mkdir: true,
			setup: func(t *testing.T, dir string) {
				if err := WriteUnattendedMarker(dir); err != nil {
					t.Fatalf("WriteUnattendedMarker: %v", err)
				}
				info, err := os.Stat(filepath.Join(dir, host.UnattendedMarkerName))
				if err != nil {
					t.Fatalf("marker not at the name the host reads: %v", err)
				}
				if info.Size() != 0 {
					t.Errorf("marker size = %d, want an empty file", info.Size())
				}
			},
			want: true,
		},
		{
			name:  "writing twice is not an error",
			mkdir: true,
			setup: func(t *testing.T, dir string) {
				for i := 0; i < 2; i++ {
					if err := WriteUnattendedMarker(dir); err != nil {
						t.Fatalf("WriteUnattendedMarker #%d: %v", i+1, err)
					}
				}
			},
			want: true,
		},
		{
			name:  "missing marker",
			mkdir: true,
			setup: func(*testing.T, string) {},
			want:  false,
		},
		{
			name:  "missing session dir",
			setup: func(*testing.T, string) {},
			want:  false,
		},
		{
			name:  "a directory at the path counts",
			mkdir: true,
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, host.UnattendedMarkerName), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
		{
			name:  "the autonomous marker is not the unattended one",
			mkdir: true,
			setup: func(t *testing.T, dir string) {
				if err := WriteAutonomousMarker(dir); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			dir := filepath.Join(worktree, ".afk", name)
			if tt.mkdir {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tt.setup(t, dir)
			if got := IsUnattended(worktree, name); got != tt.want {
				t.Errorf("IsUnattended = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWriteUnattendedMarkerNeedsTheSessionDir: the writer never creates the
// session directory, so a marker cannot conjure a half-made session.
func TestWriteUnattendedMarkerNeedsTheSessionDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".afk", "nope")
	if err := WriteUnattendedMarker(dir); err == nil {
		t.Fatal("WriteUnattendedMarker into a missing dir succeeded, want an error")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("session dir was created (err=%v)", err)
	}
}
