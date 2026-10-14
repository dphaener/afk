package planfolder

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/profile"
)

type fakeInfo struct {
	dir bool
}

func (f fakeInfo) Name() string       { return "x" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

// fakeStat answers from a map: "dir", "file", or an error for anything else.
func fakeStat(disk map[string]string) func(string) (fs.FileInfo, error) {
	return func(p string) (fs.FileInfo, error) {
		switch disk[p] {
		case "dir":
			return fakeInfo{dir: true}, nil
		case "file":
			return fakeInfo{}, nil
		case "boom":
			return nil, errors.New("permission denied")
		}
		return nil, fs.ErrNotExist
	}
}

// TestDecide covers every branch of the decision. Load-bearing rows: "name
// with location" (the only way to Create), "exists as folder" (Exists drives
// the dashboard's offer), "full path" and "tilde path" (must pass through
// untouched so T7.4's own words apply), and the bad-character rows (a name
// must never become a hidden folder, a parent reference or a flag).
func TestDecide(t *testing.T) {
	loc := Location{Path: "/code", Set: true}
	disk := map[string]string{"/code": "dir", "/code/there": "dir", "/code/afile": "file", "/code/locked": "boom"}
	cases := []struct {
		name   string
		text   string
		loc    Location
		disk   map[string]string
		kind   Kind
		path   string
		exists bool
		reason string
	}{
		{name: "name with location", text: "demo", loc: loc, disk: disk, kind: KindCreate, path: "/code/demo"},
		{name: "name is trimmed", text: "  demo ", loc: loc, disk: disk, kind: KindCreate, path: "/code/demo"},
		{name: "no location", text: "demo", loc: Location{}, disk: disk, kind: KindRefuse, reason: `afk profile set "project location" ~/code`},
		{name: "bad space", text: "my app", loc: loc, disk: disk, kind: KindRefuse, reason: "letters, digits"},
		{name: "bad dotdot", text: "..", loc: loc, disk: disk, kind: KindRefuse, reason: "letters, digits"},
		{name: "bad leading dot", text: ".hidden", loc: loc, disk: disk, kind: KindRefuse, reason: "letters, digits"},
		{name: "bad leading dash", text: "-rf", loc: loc, disk: disk, kind: KindRefuse, reason: "letters, digits"},
		{name: "too long", text: strings.Repeat("a", 101), loc: loc, disk: disk, kind: KindRefuse, reason: "100 characters"},
		{name: "exactly max length", text: strings.Repeat("a", 100), loc: loc, disk: disk, kind: KindCreate, path: "/code/" + strings.Repeat("a", 100)},
		{name: "exists as folder", text: "there", loc: loc, disk: disk, kind: KindRefuse, path: "/code/there", exists: true, reason: "already exists"},
		{name: "exists as file", text: "afile", loc: loc, disk: disk, kind: KindRefuse, reason: "not a folder"},
		{name: "stat error", text: "locked", loc: loc, disk: disk, kind: KindRefuse, reason: "could not be checked"},
		{name: "location missing", text: "demo", loc: Location{Path: "/gone", Set: true}, disk: disk, kind: KindRefuse, reason: "/gone does not exist"},
		{name: "location is a file", text: "demo", loc: Location{Path: "/code/afile", Set: true}, disk: disk, kind: KindRefuse, reason: "is not a folder"},
		{name: "location unreadable", text: "demo", loc: Location{Path: "/code/locked", Set: true}, disk: disk, kind: KindRefuse, reason: "could not be checked"},
		{name: "location problem", text: "demo", loc: Location{Set: true, Problem: "Your project location \"x\" is not a full path."}, disk: disk, kind: KindRefuse, reason: "not a full path"},
		{name: "full path", text: "/work/thing", loc: loc, disk: disk, kind: KindUse, path: "/work/thing"},
		{name: "tilde path", text: "~/x", loc: loc, disk: disk, kind: KindUse, path: "~/x"},
		{name: "full path without location", text: "/work/thing", loc: Location{}, disk: disk, kind: KindUse, path: "/work/thing"},
		{name: "blank", text: "   ", loc: loc, disk: disk, kind: KindRefuse, reason: "Type a folder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(c.text, c.loc, fakeStat(c.disk))
			if d.Kind != c.kind || d.Path != c.path || d.Exists != c.exists {
				t.Fatalf("got %+v, want kind %v path %q exists %v", d, c.kind, c.path, c.exists)
			}
			if c.kind == KindRefuse {
				if !strings.Contains(d.Reason, c.reason) {
					t.Errorf("reason %q lacks %q", d.Reason, c.reason)
				}
				if !strings.HasSuffix(d.Reason, "Nothing was created.") {
					t.Errorf("reason %q must end with %q", d.Reason, "Nothing was created.")
				}
			} else if d.Reason != "" {
				t.Errorf("unexpected reason %q", d.Reason)
			}
		})
	}
}

func TestZeroDecisionRefuses(t *testing.T) {
	if (Decision{}).Kind != KindRefuse {
		t.Fatal("the zero Decision must be a refusal")
	}
}

func TestIsName(t *testing.T) {
	for text, want := range map[string]bool{"demo": true, " demo ": true, "a/b": false, "~": false, "~/x": false, "": true} {
		if got := IsName(text); got != want {
			t.Errorf("IsName(%q) = %v, want %v", text, got, want)
		}
	}
}

// TestLocationFrom: the load-bearing rows are "kind entry ignored" (a
// kind-specific location must not apply to a project whose kind is unknown)
// and "load error" (a broken profile must explain itself, not look unset).
func TestLocationFrom(t *testing.T) {
	entry := func(topic, value, kind string) profile.Entry {
		return profile.Entry{Topic: topic, Value: value, Strength: profile.StrengthRule, Kind: kind}
	}
	cases := []struct {
		name    string
		p       profile.Profile
		found   bool
		err     error
		want    Location
		problem string
	}{
		{name: "set", p: profile.Profile{Entries: []profile.Entry{entry("project location", "/srv/code", "")}}, found: true, want: Location{Path: "/srv/code", Set: true}},
		{name: "topic case and spacing", p: profile.Profile{Entries: []profile.Entry{entry("Project  Location", "/srv/code", "")}}, found: true, want: Location{Path: "/srv/code", Set: true}},
		{name: "unset", p: profile.Profile{Entries: []profile.Entry{entry("editor", "vim", "")}}, found: true, want: Location{}},
		{name: "no profile file", want: Location{}},
		{name: "no preference", p: profile.Profile{Entries: []profile.Entry{entry("project location", "no preference", "")}}, found: true, want: Location{}},
		{name: "empty value", p: profile.Profile{Entries: []profile.Entry{entry("project location", "  ", "")}}, found: true, want: Location{}},
		{name: "tilde slash expanded", p: profile.Profile{Entries: []profile.Entry{entry("project location", "~/code", "")}}, found: true, want: Location{Path: "/home/me/code", Set: true}},
		{name: "bare tilde expanded", p: profile.Profile{Entries: []profile.Entry{entry("project location", "~", "")}}, found: true, want: Location{Path: "/home/me", Set: true}},
		{name: "relative value", p: profile.Profile{Entries: []profile.Entry{entry("project location", "code", "")}}, found: true, want: Location{Set: true}, problem: `afk profile set "project location" ~/code`},
		{name: "kind entry ignored", p: profile.Profile{Entries: []profile.Entry{entry("project location", "/web", "web")}}, found: true, want: Location{}},
		{name: "load error", err: errors.New("bad toml"), want: Location{}, problem: "afk profile check"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LocationFrom(c.p, c.found, c.err, "/home/me")
			if got.Path != c.want.Path || got.Set != c.want.Set {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if c.problem == "" && got.Problem != "" {
				t.Errorf("unexpected problem %q", got.Problem)
			}
			if c.problem != "" && !strings.Contains(got.Problem, c.problem) {
				t.Errorf("problem %q lacks %q", got.Problem, c.problem)
			}
		})
	}
}

func osMkdir(p string) error { return os.Mkdir(p, 0o755) }

func realGitInit(p string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = p
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

func TestCreateMakesGitRepository(t *testing.T) {
	loc := t.TempDir()
	path := filepath.Join(loc, "demo")
	if err := Create(path, osMkdir, realGitInit); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(path, ".git")); err != nil || !fi.IsDir() {
		t.Fatalf("want %s/.git folder, got %v %v", path, fi, err)
	}
}

func TestCreateGitInitFailureLeavesFolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo")
	err := Create(path, osMkdir, func(string) error { return errors.New("git exploded") })
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{path, "left on disk", "git exploded", "Nothing was removed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if fi, serr := os.Stat(path); serr != nil || !fi.IsDir() {
		t.Fatalf("folder must remain: %v", serr)
	}
}

func TestCreateExistingFolderUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep.txt")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	err := Create(path, osMkdir, func(string) error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "Nothing was created") {
		t.Fatalf("want a could-not-be-made error, got %v", err)
	}
	if called {
		t.Error("git init must not run when mkdir failed")
	}
	if _, serr := os.Stat(marker); serr != nil {
		t.Errorf("existing contents must be untouched: %v", serr)
	}
}

func TestCreateDoesNotMakeMissingLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nolocation", "demo")
	if err := Create(path, osMkdir, func(string) error { return nil }); err == nil {
		t.Fatal("want an error: the location must never be created")
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		t.Error("location was created")
	}
}
