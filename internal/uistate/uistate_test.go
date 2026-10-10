package uistate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFS is a hermetic in-memory filesystem for Deps: it tracks reads,
// writes, renames, and mkdirs so tests can assert Remember's same-directory
// temp-then-rename sequence without touching a real disk (per the plan:
// "use a fake in-memory filesystem via the Deps closures rather than
// t.TempDir()").
type fakeFS struct {
	files   map[string][]byte
	writes  []string // paths passed to WriteFile, in order
	renames []struct{ oldpath, newpath string }
	mkdirs  []string
	removes []string // paths passed to Remove, in order
}

func newFakeFS(files map[string][]byte) *fakeFS {
	f := &fakeFS{files: map[string][]byte{}}
	for k, v := range files {
		f.files[k] = v
	}
	return f
}

func (f *fakeFS) readFile(path string) ([]byte, error) {
	if data, ok := f.files[path]; ok {
		return data, nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (f *fakeFS) writeFile(path string, data []byte, _ fs.FileMode) error {
	f.writes = append(f.writes, path)
	f.files[path] = data
	return nil
}

func (f *fakeFS) rename(oldpath, newpath string) error {
	f.renames = append(f.renames, struct{ oldpath, newpath string }{oldpath, newpath})
	data, ok := f.files[oldpath]
	if !ok {
		return &fs.PathError{Op: "rename", Path: oldpath, Err: fs.ErrNotExist}
	}
	delete(f.files, oldpath)
	f.files[newpath] = data
	return nil
}

func (f *fakeFS) mkdirAll(path string, _ fs.FileMode) error {
	f.mkdirs = append(f.mkdirs, path)
	return nil
}

func (f *fakeFS) remove(path string) error {
	f.removes = append(f.removes, path)
	delete(f.files, path)
	return nil
}

// fakeDeps builds hermetic Deps over a fakeFS plus a fixed home/env,
// mirroring config_test.go's fakeDeps helper.
func fakeDeps(fsys *fakeFS, env map[string]string, home string) Deps {
	return Deps{
		Getenv: func(key string) string { return env[key] },
		HomeDir: func() (string, error) {
			return home, nil
		},
		ReadFile: fsys.readFile,
		WriteFile: func(path string, data []byte, mode fs.FileMode) error {
			return fsys.writeFile(path, data, mode)
		},
		Rename: func(oldpath, newpath string) error {
			return fsys.rename(oldpath, newpath)
		},
		MkdirAll: func(path string, mode fs.FileMode) error {
			return fsys.mkdirAll(path, mode)
		},
		Remove: func(path string) error {
			return fsys.remove(path)
		},
	}
}

// TestPath is load-bearing on both rows for the same reason as config.Path's
// test: XDG_STATE_HOME honored is the whole point of hand-rolling this
// instead of calling an os.UserConfigDir-style platform helper, and the
// unset row pins the fallback that makes the honored case meaningful.
func TestPath(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		home string
		want string
	}{
		{
			name: "XDG_STATE_HOME set is honored",
			env:  map[string]string{"XDG_STATE_HOME": "/xdg/state"},
			home: "/home/tester",
			want: "/xdg/state/afk/state.json",
		},
		{
			name: "XDG_STATE_HOME unset falls back to ~/.local/state",
			env:  map[string]string{},
			home: "/home/tester",
			want: "/home/tester/.local/state/afk/state.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := newFakeFS(nil)
			d := fakeDeps(fsys, tt.env, tt.home)
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

// TestLoad covers the presence/absence/shape matrix. The "{}" and
// "{\"selected\":null}" rows are load-bearing: both unmarshal Selected to a
// nil map, and Remember's `Selected[projectPath] = sessionName` assignment
// panics on a nil map, so Load normalizing it is what stands between a
// malformed-looking-but-valid file and a crashed flush goroutine. The "no
// opened key" row is the equivalent pin for the new field: it proves Load
// normalizes a wholly absent "opened" key to a non-nil map, not just an
// explicit null, which is the shape every pre-existing state file on disk
// has today.
func TestLoad(t *testing.T) {
	const home = "/home/tester"
	path := filepath.Join(home, ".local", "state", "afk", "state.json")

	tests := []struct {
		name       string
		files      map[string][]byte
		want       map[string]string
		wantOpened map[string]int
		wantErr    bool
	}{
		{
			name:       "missing file returns empty non-nil map and nil error",
			files:      nil,
			want:       map[string]string{},
			wantOpened: map[string]int{},
		},
		{
			name:       "empty object returns empty non-nil map",
			files:      map[string][]byte{path: []byte(`{}`)},
			want:       map[string]string{},
			wantOpened: map[string]int{},
		},
		{
			name:       "explicit null selected returns empty non-nil map",
			files:      map[string][]byte{path: []byte(`{"selected":null}`)},
			want:       map[string]string{},
			wantOpened: map[string]int{},
		},
		{
			name: "valid file round-trips",
			files: map[string][]byte{
				path: []byte(`{"selected":{"/repo/a":"session-a","/repo/b":"session-b"}}`),
			},
			want:       map[string]string{"/repo/a": "session-a", "/repo/b": "session-b"},
			wantOpened: map[string]int{},
		},
		{
			name:    "malformed JSON is an error",
			files:   map[string][]byte{path: []byte(`{not valid json`)},
			wantErr: true,
		},
		{
			name: "unknown keys are tolerated",
			files: map[string][]byte{
				path: []byte(`{"selected":{"/repo/a":"session-a"},"future_field":123}`),
			},
			want:       map[string]string{"/repo/a": "session-a"},
			wantOpened: map[string]int{},
		},
		{
			name: "file with no opened key returns non-nil empty opened map",
			files: map[string][]byte{
				path: []byte(`{"selected":{"/repo/a":"session-a"}}`),
			},
			want:       map[string]string{"/repo/a": "session-a"},
			wantOpened: map[string]int{},
		},
		{
			name: "opened counts round-trip",
			files: map[string][]byte{
				path: []byte(`{"selected":{},"opened":{"/repo/a":3,"/repo/b":1}}`),
			},
			want:       map[string]string{},
			wantOpened: map[string]int{"/repo/a": 3, "/repo/b": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := newFakeFS(tt.files)
			d := fakeDeps(fsys, nil, home)
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
			if got.Selected == nil {
				t.Fatalf("Load().Selected = nil, want non-nil map")
			}
			if !maps.Equal(got.Selected, tt.want) {
				t.Errorf("Load().Selected = %v, want %v", got.Selected, tt.want)
			}
			if got.Opened == nil {
				t.Fatalf("Load().Opened = nil, want non-nil map")
			}
			if !maps.Equal(got.Opened, tt.wantOpened) {
				t.Errorf("Load().Opened = %v, want %v", got.Opened, tt.wantOpened)
			}
		})
	}
}

// TestLoad_UnreadableExistingFile mirrors config's split-out test: it needs
// a ReadFile fake returning a non-ErrNotExist error, which the map-based
// fakeFS's default (ErrNotExist) can't produce.
func TestLoad_UnreadableExistingFile(t *testing.T) {
	d := Deps{
		Getenv: func(string) string { return "" },
		HomeDir: func() (string, error) {
			return "/home/tester", nil
		},
		ReadFile: func(string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}
		},
	}
	if _, err := Load(d); err == nil {
		t.Fatal("Load() error = nil, want error for unreadable existing file")
	}
}

// TestRemember is the full matrix for the write path. The
// "merges into an existing file with other projects' entries intact" row is
// load-bearing: it is the only row that proves a second dashboard's entry
// survives a concurrent Remember rather than being clobbered by a
// read-then-blind-overwrite. The "malformed existing file" row is pinned
// separately from Load's malformed-JSON row on purpose: Load must error on
// that file, but Remember must overwrite it — both semantics need their own
// assertion or one could silently regress into the other.
func TestRemember(t *testing.T) {
	const home = "/home/tester"
	path := filepath.Join(home, ".local", "state", "afk", "state.json")
	dir := filepath.Dir(path)

	t.Run("returns error and writes nothing on a non-JSON read error", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := Deps{
			Getenv:  func(string) string { return "" },
			HomeDir: func() (string, error) { return home, nil },
			ReadFile: func(string) ([]byte, error) {
				return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
			},
			WriteFile: fsys.writeFile,
			Rename:    fsys.rename,
			MkdirAll:  fsys.mkdirAll,
			Remove:    fsys.remove,
		}
		err := Remember(d, "/repo/a", "session-a")
		if err == nil {
			t.Fatal("Remember() error = nil, want error")
		}
		if len(fsys.writes) != 0 {
			t.Errorf("Remember() wrote %v, want no writes", fsys.writes)
		}
	})

	t.Run("merges into an existing file with other projects' entries intact", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{
			path: []byte(`{"selected":{"/repo/other":"session-other"}}`),
		})
		d := fakeDeps(fsys, nil, home)
		if err := Remember(d, "/repo/a", "session-a"); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after Remember unexpected error: %v", err)
		}
		want := map[string]string{"/repo/other": "session-other", "/repo/a": "session-a"}
		if !maps.Equal(got.Selected, want) {
			t.Errorf("after Remember, Selected = %v, want %v", got.Selected, want)
		}
	})

	t.Run("writes the temp file in the same directory then renames", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := Remember(d, "/repo/a", "session-a"); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		if len(fsys.writes) != 1 {
			t.Fatalf("writes = %v, want exactly one", fsys.writes)
		}
		tmp := fsys.writes[0]
		if filepath.Dir(tmp) != dir {
			t.Errorf("temp file dir = %q, want %q", filepath.Dir(tmp), dir)
		}
		if !strings.Contains(filepath.Base(tmp), ".state.json.tmp.") {
			t.Errorf("temp file name = %q, want to contain %q", filepath.Base(tmp), ".state.json.tmp.")
		}
		if len(fsys.renames) != 1 {
			t.Fatalf("renames = %v, want exactly one", fsys.renames)
		}
		if fsys.renames[0].oldpath != tmp {
			t.Errorf("rename oldpath = %q, want %q", fsys.renames[0].oldpath, tmp)
		}
		if fsys.renames[0].newpath != path {
			t.Errorf("rename newpath = %q, want %q", fsys.renames[0].newpath, path)
		}
		if _, ok := fsys.files[path]; !ok {
			t.Errorf("final file %q not present after rename", path)
		}
	})

	t.Run("creates the parent directory", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := Remember(d, "/repo/a", "session-a"); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		found := false
		for _, m := range fsys.mkdirs {
			if m == dir {
				found = true
			}
		}
		if !found {
			t.Errorf("mkdirs = %v, want to contain %q", fsys.mkdirs, dir)
		}
	})

	t.Run("no-ops on empty project path", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := Remember(d, "", "session-a"); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		if len(fsys.writes) != 0 || len(fsys.renames) != 0 {
			t.Errorf("Remember() with empty projectPath wrote/renamed, want no-op")
		}
	})

	t.Run("no-ops on empty session name", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := Remember(d, "/repo/a", ""); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		if len(fsys.writes) != 0 || len(fsys.renames) != 0 {
			t.Errorf("Remember() with empty sessionName wrote/renamed, want no-op")
		}
	})

	t.Run("overwrites a malformed existing file with only the new entry", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{
			path: []byte(`{not valid json`),
		})
		d := fakeDeps(fsys, nil, home)
		if err := Remember(d, "/repo/a", "session-a"); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after Remember unexpected error: %v", err)
		}
		want := map[string]string{"/repo/a": "session-a"}
		if !maps.Equal(got.Selected, want) {
			t.Errorf("after Remember over malformed file, Selected = %v, want %v", got.Selected, want)
		}
	})

	// The wrong-type row is not a duplicate of the malformed one above: the two
	// reach the overwrite branch through DIFFERENT errors.As arms — a truncated
	// file yields *json.SyntaxError, a well-formed file whose "selected" is a
	// string yields *json.UnmarshalTypeError — and a Remember that classified
	// only the first would return this file's error and write nothing, silently
	// stranding the user on row 0 forever with no way to clear it from the UI.
	t.Run("overwrites a wrong-typed existing file with only the new entry", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{
			path: []byte(`{"selected":"str"}`),
		})
		d := fakeDeps(fsys, nil, home)

		_, loadErr := Load(d)
		var typeErr *json.UnmarshalTypeError
		if !errors.As(loadErr, &typeErr) {
			t.Fatalf("Load() error = %v, want one wrapping *json.UnmarshalTypeError (the branch this row exists for)", loadErr)
		}

		if err := Remember(d, "/repo/a", "session-a"); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after Remember unexpected error: %v", err)
		}
		want := map[string]string{"/repo/a": "session-a"}
		if !maps.Equal(got.Selected, want) {
			t.Errorf("after Remember over wrong-typed file, Selected = %v, want %v", got.Selected, want)
		}
	})

	// This row is load-bearing for tmpCounter, not the clock: two Remember
	// calls issued back-to-back, with no artificial delay between them, must
	// still land on distinct temp names. A clock-derived suffix would make
	// this row flaky (or outright fail) on darwin, where UnixNano has only
	// microsecond granularity and back-to-back calls routinely land on the
	// same tick.
	t.Run("two back-to-back Remember calls use distinct temp names regardless of the clock", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := Remember(d, "/repo/a", "session-a"); err != nil {
			t.Fatalf("Remember() #1 unexpected error: %v", err)
		}
		if err := Remember(d, "/repo/b", "session-b"); err != nil {
			t.Fatalf("Remember() #2 unexpected error: %v", err)
		}
		if len(fsys.writes) != 2 {
			t.Fatalf("writes = %v, want exactly two", fsys.writes)
		}
		if fsys.writes[0] == fsys.writes[1] {
			t.Errorf("temp names were identical across calls: %q", fsys.writes[0])
		}
	})

	// Pins the minor fix-2 finding: every other atomic writer in this repo
	// (internal/dashmeta/dashmeta.go, internal/session/create.go,
	// internal/session/label.go) removes its temp file when Rename fails, so
	// a persistent rename failure doesn't leak one temp file per call.
	t.Run("removes the temp file when Rename fails", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		d.Rename = func(oldpath, newpath string) error {
			return &fs.PathError{Op: "rename", Path: oldpath, Err: fs.ErrPermission}
		}
		err := Remember(d, "/repo/a", "session-a")
		if err == nil {
			t.Fatal("Remember() error = nil, want error from Rename")
		}
		if len(fsys.writes) != 1 {
			t.Fatalf("writes = %v, want exactly one", fsys.writes)
		}
		tmp := fsys.writes[0]
		if len(fsys.removes) != 1 || fsys.removes[0] != tmp {
			t.Errorf("removes = %v, want exactly [%q]", fsys.removes, tmp)
		}
		if _, ok := fsys.files[tmp]; ok {
			t.Errorf("temp file %q still present after failed Rename, want removed", tmp)
		}
	})

	// Load-bearing per the plan: Remember and RecordOpen read-modify-write the
	// SAME file, so a Remember call must never blank out counts RecordOpen
	// already wrote — that is exactly the failure the shared selectionWriteMu
	// in dash.go exists to prevent, and this row is the unit-level half of
	// that guarantee (the mutex itself is dash.go's, not this package's).
	t.Run("preserves existing Opened counts when merging a new Selected entry", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{
			path: []byte(`{"selected":{},"opened":{"/repo/a":2}}`),
		})
		d := fakeDeps(fsys, nil, home)
		if err := Remember(d, "/repo/a", "session-a"); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after Remember unexpected error: %v", err)
		}
		wantOpened := map[string]int{"/repo/a": 2}
		if !maps.Equal(got.Opened, wantOpened) {
			t.Errorf("after Remember, Opened = %v, want %v", got.Opened, wantOpened)
		}
	})
}

// TestRecordOpen mirrors TestRemember's shape for the write path RecordOpen
// shares with it. The "increments to 2" row is load-bearing: a naive
// re-implementation that always set Opened[path] = 1 instead of
// incrementing would pass every other row here and still not build a
// frequency ranking. The "preserves existing Selected entry" row is the
// mirror-image pin of Remember's new "preserves Opened" row above — one
// function guarding the other's field.
func TestRecordOpen(t *testing.T) {
	const home = "/home/tester"
	path := filepath.Join(home, ".local", "state", "afk", "state.json")
	dir := filepath.Dir(path)

	t.Run("a fresh file gains a count of 1", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := RecordOpen(d, "/repo/a"); err != nil {
			t.Fatalf("RecordOpen() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after RecordOpen unexpected error: %v", err)
		}
		want := map[string]int{"/repo/a": 1}
		if !maps.Equal(got.Opened, want) {
			t.Errorf("after RecordOpen, Opened = %v, want %v", got.Opened, want)
		}
	})

	t.Run("a second RecordOpen on the same path increments to 2", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := RecordOpen(d, "/repo/a"); err != nil {
			t.Fatalf("RecordOpen() #1 unexpected error: %v", err)
		}
		if err := RecordOpen(d, "/repo/a"); err != nil {
			t.Fatalf("RecordOpen() #2 unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after RecordOpen unexpected error: %v", err)
		}
		want := map[string]int{"/repo/a": 2}
		if !maps.Equal(got.Opened, want) {
			t.Errorf("after two RecordOpen calls, Opened = %v, want %v", got.Opened, want)
		}
	})

	t.Run("preserves an existing Selected entry", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{
			path: []byte(`{"selected":{"/repo/a":"session-a"},"opened":{}}`),
		})
		d := fakeDeps(fsys, nil, home)
		if err := RecordOpen(d, "/repo/a"); err != nil {
			t.Fatalf("RecordOpen() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after RecordOpen unexpected error: %v", err)
		}
		wantSelected := map[string]string{"/repo/a": "session-a"}
		if !maps.Equal(got.Selected, wantSelected) {
			t.Errorf("after RecordOpen, Selected = %v, want %v", got.Selected, wantSelected)
		}
	})

	t.Run("no-ops on empty project path", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := RecordOpen(d, ""); err != nil {
			t.Fatalf("RecordOpen() unexpected error: %v", err)
		}
		if len(fsys.writes) != 0 || len(fsys.renames) != 0 {
			t.Errorf("RecordOpen() with empty projectPath wrote/renamed, want no-op")
		}
	})

	t.Run("overwrites a malformed existing file with only the new count", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{
			path: []byte(`{not valid json`),
		})
		d := fakeDeps(fsys, nil, home)
		if err := RecordOpen(d, "/repo/a"); err != nil {
			t.Fatalf("RecordOpen() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after RecordOpen unexpected error: %v", err)
		}
		want := map[string]int{"/repo/a": 1}
		if !maps.Equal(got.Opened, want) {
			t.Errorf("after RecordOpen over malformed file, Opened = %v, want %v", got.Opened, want)
		}
		if !maps.Equal(got.Selected, map[string]string{}) {
			t.Errorf("after RecordOpen over malformed file, Selected = %v, want empty", got.Selected)
		}
	})

	t.Run("returns error and writes nothing on a non-JSON read error", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := Deps{
			Getenv:  func(string) string { return "" },
			HomeDir: func() (string, error) { return home, nil },
			ReadFile: func(string) ([]byte, error) {
				return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
			},
			WriteFile: fsys.writeFile,
			Rename:    fsys.rename,
			MkdirAll:  fsys.mkdirAll,
			Remove:    fsys.remove,
		}
		err := RecordOpen(d, "/repo/a")
		if err == nil {
			t.Fatal("RecordOpen() error = nil, want error")
		}
		if len(fsys.writes) != 0 {
			t.Errorf("RecordOpen() wrote %v, want no writes", fsys.writes)
		}
	})

	t.Run("writes the temp file in the same directory then renames", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := RecordOpen(d, "/repo/a"); err != nil {
			t.Fatalf("RecordOpen() unexpected error: %v", err)
		}
		if len(fsys.writes) != 1 {
			t.Fatalf("writes = %v, want exactly one", fsys.writes)
		}
		tmp := fsys.writes[0]
		if filepath.Dir(tmp) != dir {
			t.Errorf("temp file dir = %q, want %q", filepath.Dir(tmp), dir)
		}
		if !strings.Contains(filepath.Base(tmp), ".state.json.tmp.") {
			t.Errorf("temp file name = %q, want to contain %q", filepath.Base(tmp), ".state.json.tmp.")
		}
		if len(fsys.renames) != 1 {
			t.Fatalf("renames = %v, want exactly one", fsys.renames)
		}
	})
}

// TestErrorsWrapUnderlying pins the %w contract the task block calls out
// explicitly: Remember classifies Load's error via errors.As, so a %v or
// message-only wrap would make every malformed file behave like "write
// nothing" instead of "treat as empty and overwrite".
func TestErrorsWrapUnderlying(t *testing.T) {
	const home = "/home/tester"
	path := filepath.Join(home, ".local", "state", "afk", "state.json")
	fsys := newFakeFS(map[string][]byte{path: []byte(`{not valid json`)})
	d := fakeDeps(fsys, nil, home)

	_, err := Load(d)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("Load() error %v does not wrap *json.SyntaxError via %%w", err)
	}
}

// TestLoad_SidebarPct pins that rawState mirrors SidebarPct: Load decodes
// through rawState, so a field that exists only on State would silently read
// back as zero. The absent-key row is the shape every pre-existing state
// file on disk has today.
func TestLoad_SidebarPct(t *testing.T) {
	const home = "/home/tester"
	path := filepath.Join(home, ".local", "state", "afk", "state.json")

	tests := []struct {
		name  string
		files map[string][]byte
		want  int
	}{
		{
			name:  "missing file yields zero",
			files: nil,
			want:  0,
		},
		{
			name:  "file without the field yields zero",
			files: map[string][]byte{path: []byte(`{"selected":{"/repo/a":"session-a"}}`)},
			want:  0,
		},
		{
			name:  "file with the field round-trips",
			files: map[string][]byte{path: []byte(`{"selected":{},"opened":{},"sidebar_pct":52}`)},
			want:  52,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := fakeDeps(newFakeFS(tt.files), nil, home)
			got, err := Load(d)
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if got.SidebarPct != tt.want {
				t.Errorf("Load().SidebarPct = %d, want %d", got.SidebarPct, tt.want)
			}
		})
	}
}

// TestRememberSidebarPct mirrors TestRemember/TestRecordOpen for the third
// writer on the shared file. The preserve rows are load-bearing: all three
// writers read-modify-write the same file, so none may blank another's field.
func TestRememberSidebarPct(t *testing.T) {
	const home = "/home/tester"
	path := filepath.Join(home, ".local", "state", "afk", "state.json")

	t.Run("round trip yields the stored value", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{
			path: []byte(`{"selected":{"/repo/a":"session-a"},"opened":{"/repo/a":2}}`),
		})
		d := fakeDeps(fsys, nil, home)
		if err := RememberSidebarPct(d, 50); err != nil {
			t.Fatalf("RememberSidebarPct() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after RememberSidebarPct unexpected error: %v", err)
		}
		if got.SidebarPct != 50 {
			t.Errorf("SidebarPct = %d, want 50", got.SidebarPct)
		}
	})

	t.Run("preserves existing selected and opened entries", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{
			path: []byte(`{"selected":{"/repo/a":"session-a"},"opened":{"/repo/a":2}}`),
		})
		d := fakeDeps(fsys, nil, home)
		if err := RememberSidebarPct(d, 50); err != nil {
			t.Fatalf("RememberSidebarPct() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after RememberSidebarPct unexpected error: %v", err)
		}
		if want := map[string]string{"/repo/a": "session-a"}; !maps.Equal(got.Selected, want) {
			t.Errorf("Selected = %v, want %v", got.Selected, want)
		}
		if want := map[string]int{"/repo/a": 2}; !maps.Equal(got.Opened, want) {
			t.Errorf("Opened = %v, want %v", got.Opened, want)
		}
	})

	t.Run("a later Remember and RecordOpen preserve the stored pct", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		if err := RememberSidebarPct(d, 61); err != nil {
			t.Fatalf("RememberSidebarPct() unexpected error: %v", err)
		}
		if err := Remember(d, "/repo/a", "session-a"); err != nil {
			t.Fatalf("Remember() unexpected error: %v", err)
		}
		if err := RecordOpen(d, "/repo/a"); err != nil {
			t.Fatalf("RecordOpen() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if got.SidebarPct != 61 {
			t.Errorf("SidebarPct = %d, want 61 after other writers ran", got.SidebarPct)
		}
	})

	t.Run("no-ops on zero and negative pct", func(t *testing.T) {
		for _, pct := range []int{0, -5} {
			fsys := newFakeFS(nil)
			d := fakeDeps(fsys, nil, home)
			if err := RememberSidebarPct(d, pct); err != nil {
				t.Fatalf("RememberSidebarPct(%d) unexpected error: %v", pct, err)
			}
			if len(fsys.writes) != 0 || len(fsys.renames) != 0 {
				t.Errorf("RememberSidebarPct(%d) wrote/renamed, want no-op", pct)
			}
		}
	})

	t.Run("overwrites a malformed existing file with only the new pct", func(t *testing.T) {
		fsys := newFakeFS(map[string][]byte{path: []byte(`{not valid json`)})
		d := fakeDeps(fsys, nil, home)
		if err := RememberSidebarPct(d, 44); err != nil {
			t.Fatalf("RememberSidebarPct() unexpected error: %v", err)
		}
		got, err := Load(d)
		if err != nil {
			t.Fatalf("Load() after RememberSidebarPct unexpected error: %v", err)
		}
		if got.SidebarPct != 44 {
			t.Errorf("SidebarPct = %d, want 44", got.SidebarPct)
		}
	})

	t.Run("returns error and writes nothing on a non-JSON read error", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := Deps{
			Getenv:  func(string) string { return "" },
			HomeDir: func() (string, error) { return home, nil },
			ReadFile: func(string) ([]byte, error) {
				return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
			},
			WriteFile: fsys.writeFile,
			Rename:    fsys.rename,
			MkdirAll:  fsys.mkdirAll,
			Remove:    fsys.remove,
		}
		err := RememberSidebarPct(d, 50)
		if err == nil {
			t.Fatal("RememberSidebarPct() error = nil, want error")
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("RememberSidebarPct() error %v does not wrap fs.ErrPermission via %%w", err)
		}
		if len(fsys.writes) != 0 {
			t.Errorf("RememberSidebarPct() wrote %v, want no writes", fsys.writes)
		}
	})

	t.Run("wraps a Rename failure and removes the temp file", func(t *testing.T) {
		fsys := newFakeFS(nil)
		d := fakeDeps(fsys, nil, home)
		d.Rename = func(oldpath, newpath string) error {
			return &fs.PathError{Op: "rename", Path: oldpath, Err: fs.ErrPermission}
		}
		err := RememberSidebarPct(d, 50)
		if err == nil {
			t.Fatal("RememberSidebarPct() error = nil, want error from Rename")
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("RememberSidebarPct() error %v does not wrap fs.ErrPermission via %%w", err)
		}
		if len(fsys.writes) != 1 || len(fsys.removes) != 1 || fsys.removes[0] != fsys.writes[0] {
			t.Errorf("writes = %v, removes = %v, want the temp file removed", fsys.writes, fsys.removes)
		}
	})
}
