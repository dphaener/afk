package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewBinary has a row per branch. The load-bearing rows are the three
// "carry on" ones (start unknown, unreadable now, both unknown): a daemon
// that stopped on a doubt would be restarted by its service manager on every
// glitch, so each must stay false.
func TestNewBinary(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	start := Binary{Path: "/bin/afk", Size: 100, ModTime: at}
	cases := []struct {
		name  string
		start Binary
		now   Binary
		err   error
		want  bool
	}{
		{"unchanged", start, start, nil, false},
		{"size changed", start, Binary{Path: "/bin/afk", Size: 101, ModTime: at}, nil, true},
		{"time changed", start, Binary{Path: "/bin/afk", Size: 100, ModTime: at.Add(time.Second)}, nil, true},
		{"start unknown", Binary{}, Binary{Path: "/bin/afk", Size: 5, ModTime: at}, nil, false},
		{"unreadable now", start, Binary{}, errors.New("gone"), false},
		{"unreadable with a stale value", start, Binary{Size: 1}, errors.New("gone"), false},
		{"both unknown", Binary{}, Binary{}, nil, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewBinary(tt.start, tt.now, tt.err); got != tt.want {
				t.Errorf("NewBinary = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReadBinary: a real file reads back its size and time; a missing file, a
// folder and an empty path are errors with a zero Binary.
func TestReadBinary(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afk")
	if err := os.WriteFile(file, []byte("12345"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		path    string
		wantErr bool
		size    int64
	}{
		{"a real file", file, false, 5},
		{"a symlink is followed", link, false, 5},
		{"a missing file", filepath.Join(dir, "nope"), true, 0},
		{"a directory", dir, true, 0},
		{"an empty path", "", true, 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b, err := ReadBinary(tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if b != (Binary{}) {
					t.Errorf("Binary = %+v, want zero on error", b)
				}
				return
			}
			if b.Path != tt.path || b.Size != tt.size || b.ModTime.IsZero() {
				t.Errorf("Binary = %+v", b)
			}
		})
	}
}
