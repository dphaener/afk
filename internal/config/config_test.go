package config

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

// fakeDeps builds a hermetic Deps: env is consulted only for the keys given,
// home is returned verbatim by HomeDir, and files maps an exact path to its
// contents — any path not present in the map reports fs.ErrNotExist, mirroring
// what a real missing file looks like through os.ReadFile.
func fakeDeps(env map[string]string, home string, files map[string][]byte) Deps {
	return Deps{
		Getenv: func(key string) string { return env[key] },
		HomeDir: func() (string, error) {
			return home, nil
		},
		ReadFile: func(path string) ([]byte, error) {
			if data, ok := files[path]; ok {
				return data, nil
			}
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
		},
	}
}

// TestLoad is table-driven over the config file's presence/absence and
// contents. The "no file" and "malformed JSON" rows are load-bearing: they
// pin down the two ends of the error contract this package promises callers
// — a missing file is the ordinary case (defaults, nil error) while a file
// that exists but doesn't parse is not silently swallowed. The "partial
// file" row is load-bearing too: it guards against a naive json.Unmarshal
// into Config zeroing fields the user didn't set, instead of leaving
// per-field defaults in place.
//
// "explicit max_depth of 0/negative is rejected" and "scan_interval below
// the floor is rejected" are the two rows a reviewer asked for directly: they
// pin the decision that an out-of-range explicit value is an error, never a
// silent substitution of a different number than the user wrote (see Load's
// max_depth and scan_interval checks). "scan_interval exactly at the floor"
// is load-bearing alongside the rejection row: the floor is `<`, not `<=`, so
// this row is what would go red if that comparison were ever tightened.
func TestLoad(t *testing.T) {
	const home = "/home/tester"
	path := filepath.Join(home, ".config", "afk", "config.json")

	tests := []struct {
		name    string
		files   map[string][]byte
		want    Config
		wantErr bool
	}{
		{
			name:  "no file present returns defaults and nil error",
			files: nil,
			want: Config{
				SearchRoots:  nil,
				ScanInterval: 60 * time.Second,
				MaxDepth:     3,
			},
		},
		{
			name: "valid file with tilde and absolute roots",
			files: map[string][]byte{
				path: []byte(`{
					"search_roots": ["~/code", "/already/absolute"],
					"scan_interval": "30s",
					"max_depth": 5
				}`),
			},
			want: Config{
				SearchRoots:  []string{filepath.Join(home, "code"), "/already/absolute"},
				ScanInterval: 30 * time.Second,
				MaxDepth:     5,
			},
		},
		{
			name: "malformed JSON is an error",
			files: map[string][]byte{
				path: []byte(`{not valid json`),
			},
			wantErr: true,
		},
		{
			name: "partial file fills only missing fields with defaults",
			files: map[string][]byte{
				path: []byte(`{"max_depth": 10}`),
			},
			want: Config{
				SearchRoots:  nil,
				ScanInterval: 60 * time.Second,
				MaxDepth:     10,
			},
		},
		{
			name: "relative root is made absolute",
			files: map[string][]byte{
				path: []byte(`{"search_roots": ["relative/dir"]}`),
			},
			want: Config{
				SearchRoots:  []string{mustAbs(t, "relative/dir")},
				ScanInterval: 60 * time.Second,
				MaxDepth:     3,
			},
		},
		{
			name: "explicit max_depth of 0 is rejected",
			files: map[string][]byte{
				path: []byte(`{"max_depth": 0}`),
			},
			wantErr: true,
		},
		{
			name: "explicit negative max_depth is rejected",
			files: map[string][]byte{
				path: []byte(`{"max_depth": -1}`),
			},
			wantErr: true,
		},
		{
			name: "scan_interval below the floor is rejected",
			files: map[string][]byte{
				path: []byte(`{"scan_interval": "1ms"}`),
			},
			wantErr: true,
		},
		{
			name: "scan_interval of zero is rejected",
			files: map[string][]byte{
				path: []byte(`{"scan_interval": "0s"}`),
			},
			wantErr: true,
		},
		{
			name: "negative scan_interval is rejected",
			files: map[string][]byte{
				path: []byte(`{"scan_interval": "-5s"}`),
			},
			wantErr: true,
		},
		{
			name: "scan_interval exactly at the floor is accepted",
			files: map[string][]byte{
				path: []byte(`{"scan_interval": "1s"}`),
			},
			want: Config{
				SearchRoots:  nil,
				ScanInterval: 1 * time.Second,
				MaxDepth:     3,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := fakeDeps(nil, home, tt.files)
			got, err := Load(d)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if got.ScanInterval != tt.want.ScanInterval {
				t.Errorf("ScanInterval = %v, want %v", got.ScanInterval, tt.want.ScanInterval)
			}
			if got.MaxDepth != tt.want.MaxDepth {
				t.Errorf("MaxDepth = %v, want %v", got.MaxDepth, tt.want.MaxDepth)
			}
			if !equalStrings(got.SearchRoots, tt.want.SearchRoots) {
				t.Errorf("SearchRoots = %v, want %v", got.SearchRoots, tt.want.SearchRoots)
			}
		})
	}
}

// TestLoad_UnreadableExistingFile is split out from the table above because
// it needs a ReadFile fake that returns a non-ErrNotExist error (e.g.
// permission denied) rather than the map-based fake's ErrNotExist default —
// exercising the "exists but I/O fails" branch that a missing-file check
// must not swallow.
func TestLoad_UnreadableExistingFile(t *testing.T) {
	d := Deps{
		Getenv: func(string) string { return "" },
		HomeDir: func() (string, error) {
			return "/home/tester", nil
		},
		ReadFile: func(string) ([]byte, error) {
			return nil, errors.New("permission denied")
		},
	}
	if _, err := Load(d); err == nil {
		t.Fatal("Load() error = nil, want error for unreadable existing file")
	}
}

// TestPath is load-bearing on both rows: XDG_CONFIG_HOME honored when set is
// the entire reason this package hand-rolls the lookup instead of calling
// os.UserConfigDir (which on darwin returns ~/Library/Application Support and
// ignores XDG_CONFIG_HOME outright); the unset row pins the fallback that
// makes the honored case meaningful by contrast.
func TestPath(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		home string
		want string
	}{
		{
			name: "XDG_CONFIG_HOME set is honored",
			env:  map[string]string{"XDG_CONFIG_HOME": "/xdg/config"},
			home: "/home/tester",
			want: "/xdg/config/afk/config.json",
		},
		{
			name: "XDG_CONFIG_HOME unset falls back to ~/.config",
			env:  map[string]string{},
			home: "/home/tester",
			want: "/home/tester/.config/afk/config.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := fakeDeps(tt.env, tt.home, nil)
			got, err := Path(d)
			if err != nil {
				t.Fatalf("Path() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Path() = %q, want %q", got, tt.want)
			}
		})
	}
}

// mustAbs computes the absolute form of rel the same way the package under
// test does (filepath.Abs, no injected cwd seam — the plan doesn't call for
// one), so the expectation tracks the actual test process's working
// directory instead of hardcoding it.
func mustAbs(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", rel, err)
	}
	return abs
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
