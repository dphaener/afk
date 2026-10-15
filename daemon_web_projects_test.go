package main

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// TestWebAddProject runs the page's add seam over the command rig.
// Load-bearing rows: "comments and dash table kept" proves the edit is the
// text edit, not a rewrite; "relative" and "no home" prove nothing is written
// for a path afk cannot place; "unreadable" proves the failure names the
// file; "tilde" proves ~ is expanded before the entry point sees it.
func TestWebAddProject(t *testing.T) {
	const withExtras = "# my projects\n[dash]\nsearch = [\"/code\"]\n\n[[project]]\npath = \"/work/alpha\"\n"
	cases := []struct {
		name       string
		text       string
		exists     bool
		folder     string
		give       string
		noHome     bool
		readErr    bool
		wantOut    string
		wantText   string
		wantName   string
		wantWrites int
		wantKept   []string
	}{
		{name: "added", folder: "/work/beta", wantOut: web.ProjectAdded, wantName: "beta", wantText: "Added beta (/work/beta).", wantWrites: 1},
		{name: "comments and dash table kept", text: withExtras, exists: true, folder: "/work/beta", wantOut: web.ProjectAdded, wantName: "beta", wantWrites: 1,
			wantKept: []string{"# my projects\n[dash]\nsearch = [\"/code\"]\n\n[[project]]\npath = \"/work/alpha\"\n"}},
		{name: "with a name and spaces", folder: "  /work/beta  ", give: " web ", wantOut: web.ProjectAdded, wantName: "web", wantText: "Added web (/work/beta).", wantWrites: 1},
		{name: "already listed", text: twoProjects, exists: true, folder: "/work/alpha", wantOut: web.ProjectAlreadyListed, wantName: "alpha", wantText: "alpha is already in the list."},
		{name: "not a folder", folder: "/work/missing", wantOut: web.ProjectRefused, wantText: "/work/missing is not a folder."},
		{name: "not a repository", folder: "/work/plain", wantOut: web.ProjectRefused, wantText: "/work/plain is not a git repository"},
		{name: "name taken", text: twoProjects, exists: true, folder: "/work/other/alpha", wantOut: web.ProjectRefused},
		{name: "empty", folder: "   ", wantOut: web.ProjectRefused, wantText: "Give the folder"},
		{name: "relative", folder: "work/beta", wantOut: web.ProjectRefused, wantText: "Give the full path to the folder, or start it with ~"},
		{name: "tilde", folder: "~/beta", wantOut: web.ProjectAdded, wantName: "beta", wantText: "Added beta (/home/u/beta).", wantWrites: 1},
		{name: "no home", folder: "~/beta", noHome: true, wantOut: web.ProjectRefused, wantText: "home folder"},
		{name: "unreadable", text: twoProjects, exists: true, folder: "/work/beta", readErr: true, wantOut: web.ProjectFailed, wantText: "config.toml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProjectRig()
			r.text, r.exists = tc.text, tc.exists
			r.dirs["/home/u/beta"], r.repos["/home/u/beta"] = true, true
			env := r.env()
			if tc.noHome {
				env.Deps.HomeDir = func() (string, error) { return "", errors.New("no home") }
			}
			if tc.readErr {
				env.Deps.ReadFile = func(string) ([]byte, error) { return nil, errors.New("disk on fire") }
			}
			got := webAddProjectWith(env, tc.folder, tc.give)
			if got.Outcome != tc.wantOut || !strings.Contains(got.Text, tc.wantText) {
				t.Errorf("got %+v, want outcome %q containing %q", got, tc.wantOut, tc.wantText)
			}
			if tc.wantName != "" && got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if tc.wantOut == web.ProjectAdded && got.Root == "" {
				t.Errorf("an added project has no Root: %+v", got)
			}
			if strings.HasPrefix(got.Text, "afk project") {
				t.Errorf("Text has a command prefix: %q", got.Text)
			}
			if r.writes != tc.wantWrites {
				t.Errorf("writes = %d, want %d", r.writes, tc.wantWrites)
			}
			for _, k := range tc.wantKept {
				if !strings.Contains(r.text, k) {
					t.Errorf("config lost %q:\n%s", k, r.text)
				}
			}
		})
	}
}

// TestWebRemoveProject: "comments and dash table kept" proves the text edit;
// "not listed" and "unreadable" prove no write.
func TestWebRemoveProject(t *testing.T) {
	const withExtras = "# my projects\n[dash]\nsearch = [\"/code\"]\n\n[[project]]\npath = \"/work/alpha\"\n\n# keep me\n[[project]]\npath = \"/work/beta\"\n"
	cases := []struct {
		name       string
		text       string
		key        string
		readErr    bool
		wantOut    string
		wantText   string
		wantWrites int
		wantKept   []string
		wantGone   string
	}{
		{name: "removed", text: twoProjects, key: "beta", wantOut: web.ProjectRemoved, wantText: "Removed beta from the list.", wantWrites: 1, wantGone: "/work/beta"},
		{name: "comments and dash table kept", text: withExtras, key: "beta", wantOut: web.ProjectRemoved, wantWrites: 1,
			wantKept: []string{"# my projects", "[dash]", "search = [\"/code\"]", "/work/alpha"}, wantGone: "/work/beta"},
		{name: "not listed", text: twoProjects, key: "gamma", wantOut: web.ProjectNotListed, wantText: "No project named gamma."},
		{name: "unreadable", text: twoProjects, key: "beta", readErr: true, wantOut: web.ProjectFailed, wantText: "config.toml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProjectRig()
			r.text, r.exists = tc.text, true
			env := r.env()
			if tc.readErr {
				env.Deps.ReadFile = func(string) ([]byte, error) { return nil, errors.New("disk on fire") }
			}
			got := webRemoveProjectWith(env, tc.key)
			if got.Outcome != tc.wantOut || !strings.Contains(got.Text, tc.wantText) {
				t.Errorf("got %+v, want outcome %q containing %q", got, tc.wantOut, tc.wantText)
			}
			if got.Outcome != web.ProjectFailed && got.Name != tc.key {
				t.Errorf("Name = %q, want %q", got.Name, tc.key)
			}
			if r.writes != tc.wantWrites {
				t.Errorf("writes = %d, want %d", r.writes, tc.wantWrites)
			}
			for _, k := range tc.wantKept {
				if !strings.Contains(r.text, k) {
					t.Errorf("config lost %q:\n%s", k, r.text)
				}
			}
			if tc.wantGone != "" && strings.Contains(r.text, tc.wantGone) {
				t.Errorf("config still has %q:\n%s", tc.wantGone, r.text)
			}
		})
	}
}

// TestWebProjectsSerialised proves overlapping adds do not lose an entry: the
// rig's file is shared, so without the mutex two read-modify-writes could
// each start from the old text. Run with -race this also catches unguarded
// access to the rig.
func TestWebProjectsSerialised(t *testing.T) {
	r := newProjectRig()
	r.dirs["/work/gamma"], r.repos["/work/gamma"] = true, true
	env := r.env()
	var wg sync.WaitGroup
	for _, d := range []string{"/work/alpha", "/work/beta", "/work/gamma"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			webAddProjectWith(env, d, "")
		}()
	}
	wg.Wait()
	for _, n := range []string{"/work/alpha", "/work/beta", "/work/gamma"} {
		if !strings.Contains(r.text, n) {
			t.Errorf("config lost %q:\n%s", n, r.text)
		}
	}
}

// TestDaemonWebDepsWiresProjectSeams pins the wiring: a page built without
// the seams hides its buttons and answers 404, and no other test notices.
func TestDaemonWebDepsWiresProjectSeams(t *testing.T) {
	deps := daemonWebDeps(nil, t.TempDir(), usagestate.Deps{}, nil)
	if deps.AddProject == nil || deps.RemoveProject == nil {
		t.Errorf("AddProject nil = %v, RemoveProject nil = %v, want both wired", deps.AddProject == nil, deps.RemoveProject == nil)
	}
}
