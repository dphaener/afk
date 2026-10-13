package main

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/daemonconfig"
)

// projectRig is a fake machine: one config file held in memory, a set of
// folders that exist and a set of folders that are git repositories.
type projectRig struct {
	cfgPath string
	text    string
	exists  bool
	writes  int
	madeDir string
	dirs    map[string]bool // folders that exist
	repos   map[string]bool // folders that are repositories
}

func newProjectRig() *projectRig {
	return &projectRig{
		cfgPath: "/cfg/afk/config.toml",
		dirs:    map[string]bool{"/work/alpha": true, "/work/beta": true, "/work/plain": true, "/work/other/alpha": true},
		repos:   map[string]bool{"/work/alpha": true, "/work/beta": true, "/work/other/alpha": true},
	}
}

func (r *projectRig) env() projectEnv {
	return projectEnv{
		Deps: daemonconfig.Deps{
			Getenv:  func(k string) string { return map[string]string{"XDG_CONFIG_HOME": "/cfg"}[k] },
			HomeDir: func() (string, error) { return "/home/u", nil },
			ReadFile: func(p string) ([]byte, error) {
				if p != r.cfgPath || !r.exists {
					return nil, fs.ErrNotExist
				}
				return []byte(r.text), nil
			},
		},
		write: func(p string, data []byte) error {
			r.text, r.exists = string(data), true
			r.writes++
			return nil
		},
		mkdirAll: func(p string, _ fs.FileMode) error { r.madeDir = p; return nil },
		cwd:      func() (string, error) { return "/work/alpha", nil },
		gitCommonDir: func(dir string) string {
			if r.repos[dir] {
				return filepath.Join(dir, ".git")
			}
			return ""
		},
		isDir: func(p string) bool { return r.dirs[p] },
	}
}

func (r *projectRig) run(args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = runProject(args, &o, &e, r.env())
	return code, o.String(), e.String()
}

const twoProjects = "[[project]]\npath = \"/work/alpha\"\n\n[[project]]\npath = \"/work/beta\"\n"

// TestRunProject covers every word. The load-bearing rows: "add to a missing
// file" (a fresh machine has no config folder, so the folder must be made and
// the file written), "add when present" (exit 0 and no write, so repeating
// the command is safe), "add a non-repository" (the daemon cannot build
// there, so nothing may be written), "add with a clashing name" (Load would
// refuse the whole file, which would stop the daemon), "remove by folder" and
// "remove unknown" (a typo must not remove anything).
func TestRunProject(t *testing.T) {
	cases := []struct {
		name       string
		text       string // "" with exists=false means no file
		exists     bool
		args       []string
		wantCode   int
		wantOut    string
		wantErr    string
		wantWrites int
		wantText   func(string) bool
	}{
		{name: "list empty", args: []string{"list"}, wantCode: 0, wantOut: "No projects are listed yet. Add one with `afk project add"},
		{name: "list two", text: twoProjects, exists: true, args: []string{"list"}, wantCode: 0, wantOut: "alpha  /work/alpha\nbeta  /work/beta\n"},
		{name: "list bad config", text: "[[project", exists: true, args: []string{"list"}, wantCode: 1, wantErr: "not valid TOML"},
		{name: "list extra word", args: []string{"list", "x"}, wantCode: 2, wantErr: "Usage: afk project list"},
		{
			name: "add to a missing file", args: []string{"add", "/work/alpha"}, wantCode: 0, wantWrites: 1,
			wantOut:  "Added alpha (/work/alpha). A running afk daemon picks it up within a few seconds.\n",
			wantText: func(s string) bool { return strings.Contains(s, "[[project]]\npath = \"/work/alpha\"") },
		},
		{
			name: "add defaults to the current folder", args: []string{"add"}, wantCode: 0, wantWrites: 1,
			wantOut: "Added alpha (/work/alpha).",
		},
		{
			name: "add with a name", args: []string{"add", "--name", "web", "/work/beta"}, wantCode: 0, wantWrites: 1,
			wantOut:  "Added web (/work/beta).",
			wantText: func(s string) bool { return strings.Contains(s, `name = "web"`) },
		},
		{
			name: "add with the flag after the folder", args: []string{"add", "/work/beta", "--name", "web"}, wantCode: 0, wantWrites: 1,
			wantOut: "Added web (/work/beta).",
		},
		{
			name: "add when present", text: twoProjects, exists: true, args: []string{"add", "/work/alpha"}, wantCode: 0,
			wantOut: "alpha is already in the list. Nothing was changed.\n",
		},
		{
			name: "add a non-repository", args: []string{"add", "/work/plain"}, wantCode: 1,
			wantErr: "is not a git repository",
		},
		{
			name: "add a folder that is not there", args: []string{"add", "/work/missing"}, wantCode: 1,
			wantErr: "is not a folder",
		},
		{
			name: "add with a clashing name", text: twoProjects, exists: true, args: []string{"add", "/work/other/alpha"}, wantCode: 1,
			wantErr: `the name "alpha" is already used`,
		},
		{
			name: "add from a worktree lists the main checkout", args: []string{"add", "/work/alpha"}, wantCode: 0, wantWrites: 1,
			wantOut: "(/work/alpha)",
		},
		{name: "add two folders", args: []string{"add", "/work/alpha", "/work/beta"}, wantCode: 2, wantErr: "Usage: afk project add"},
		{
			name: "remove by name", text: twoProjects, exists: true, args: []string{"remove", "alpha"}, wantCode: 0, wantWrites: 1,
			wantOut:  "Removed alpha from the list. Its files and any session that is running were left alone.\n",
			wantText: func(s string) bool { return !strings.Contains(s, "alpha") && strings.Contains(s, "beta") },
		},
		{
			name: "remove by folder", text: twoProjects, exists: true, args: []string{"remove", "/work/beta"}, wantCode: 0, wantWrites: 1,
			wantText: func(s string) bool { return !strings.Contains(s, "beta") && strings.Contains(s, "alpha") },
		},
		{
			name: "remove unknown", text: twoProjects, exists: true, args: []string{"remove", "nothing-here"}, wantCode: 1,
			wantErr: "No project named nothing-here. Run `afk project list`",
		},
		{name: "remove with no file", args: []string{"remove", "alpha"}, wantCode: 1, wantErr: "No project named alpha"},
		{name: "remove with no word", args: []string{"remove"}, wantCode: 2, wantErr: "Usage: afk project remove"},
		{name: "no word", args: nil, wantCode: 2, wantErr: "list, add or remove"},
		{name: "unknown word", args: []string{"frobnicate"}, wantCode: 2, wantErr: "unknown subcommand"},
		{name: "help", args: []string{"--help"}, wantCode: 0, wantOut: "afk project add [--name NAME] [DIR]"},
		{name: "bad flag", args: []string{"add", "--nope"}, wantCode: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProjectRig()
			r.text, r.exists = tc.text, tc.exists
			code, out, errOut := r.run(tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, out, errOut)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out, tc.wantOut)
			}
			if !strings.Contains(errOut, tc.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut, tc.wantErr)
			}
			if r.writes != tc.wantWrites {
				t.Errorf("writes = %d, want %d", r.writes, tc.wantWrites)
			}
			if tc.wantText != nil && !tc.wantText(r.text) {
				t.Errorf("config text after = %q", r.text)
			}
		})
	}
}

// TestProjectAddMakesConfigFolder: a fresh machine has no afk config folder;
// the add must create it before the atomic write, which needs the folder.
func TestProjectAddMakesConfigFolder(t *testing.T) {
	r := newProjectRig()
	if code, _, e := r.run("add", "/work/alpha"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if r.madeDir != "/cfg/afk" {
		t.Errorf("mkdirAll(%q), want /cfg/afk", r.madeDir)
	}
}

// TestAddProjectToConfig covers the shared helper approve calls: the name it
// reports, and that a repeat reports the listed name without writing.
func TestAddProjectToConfig(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		root, give string
		wantAdded  bool
		wantName   string
		wantErr    bool
	}{
		{name: "new, folder name", root: "/work/alpha", wantAdded: true, wantName: "alpha"},
		{name: "new, given name", root: "/work/alpha", give: "web", wantAdded: true, wantName: "web"},
		{name: "present under another name", text: "[[project]]\npath = \"/work/alpha\"\nname = \"web\"\n", root: "/work/alpha", wantName: "web"},
		{name: "clash", text: twoProjects, root: "/work/other/alpha", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProjectRig()
			r.text, r.exists = tc.text, tc.text != ""
			added, name, err := addProjectToConfig(r.env(), tc.root, tc.give)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if added != tc.wantAdded || (!tc.wantErr && name != tc.wantName) {
				t.Errorf("got (%v, %q), want (%v, %q)", added, name, tc.wantAdded, tc.wantName)
			}
			if wantWrites := map[bool]int{true: 1, false: 0}[tc.wantAdded]; r.writes != wantWrites {
				t.Errorf("writes = %d, want %d", r.writes, wantWrites)
			}
		})
	}
}

func TestProjectWriteFailureKeepsFile(t *testing.T) {
	r := newProjectRig()
	env := r.env()
	env.write = func(string, []byte) error { return errors.New("disk full") }
	var o, e bytes.Buffer
	if code := runProject([]string{"add", "/work/alpha"}, &o, &e, env); code != 1 || !strings.Contains(e.String(), "The file was not changed") {
		t.Errorf("code %d, stderr %q", code, e.String())
	}
}
