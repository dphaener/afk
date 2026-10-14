package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ui"
)

func dashNoLinks(p string) (string, error) { return p, nil }

// TestDashProjectAdd runs the dashboard's add seam over the command rig.
// Load-bearing rows: "not a repository" proves the refusal carries the
// command's own words and writes nothing; "again" proves a second add is
// "already" and not a second entry.
func TestDashProjectAdd(t *testing.T) {
	cases := []struct {
		name       string
		dir, give  string
		before     func(r *projectRig)
		wantOut    string
		wantText   string
		wantWrites int
	}{
		{name: "a repo", dir: "/work/beta", wantOut: ui.ProjectAdded, wantText: "Added beta (/work/beta).", wantWrites: 1},
		{name: "with a name", dir: "/work/beta", give: "web", wantOut: ui.ProjectAdded, wantText: "Added web (/work/beta).", wantWrites: 1},
		{name: "again", dir: "/work/alpha", before: func(r *projectRig) { r.text, r.exists = twoProjects, true },
			wantOut: ui.ProjectAlreadyListed, wantText: "alpha is already in the list. Nothing was changed."},
		{name: "not a repository", dir: "/work/plain", wantOut: ui.ProjectRefused,
			wantText: "/work/plain is not a git repository, so the daemon could not build in it."},
		{name: "not a folder", dir: "/work/missing", wantOut: ui.ProjectRefused, wantText: "/work/missing is not a folder."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProjectRig()
			if tc.before != nil {
				tc.before(r)
			}
			got := dashAddProjectWith(r.env(), tc.dir, tc.give)
			if got.Outcome != tc.wantOut || !strings.Contains(got.Text, tc.wantText) {
				t.Errorf("got %+v, want outcome %q containing %q", got, tc.wantOut, tc.wantText)
			}
			if strings.HasPrefix(got.Text, "afk project") {
				t.Errorf("Text has a command prefix: %q", got.Text)
			}
			if r.writes != tc.wantWrites {
				t.Errorf("writes = %d, want %d", r.writes, tc.wantWrites)
			}
		})
	}
}

// TestDashProjectRemove runs the remove seam. Load-bearing rows: "comments and
// dash table kept" proves the edit is the text edit, not a rewrite; "not
// listed" and "unreadable" prove no write; "linked folder" proves the row's
// resolved root finds an entry written another way.
func TestDashProjectRemove(t *testing.T) {
	const withExtras = "# my projects\n[dash]\nsearch = [\"/code\"]\n\n[[project]]\npath = \"/work/alpha\"\n\n# keep me\n[[project]]\npath = \"/work/beta\"\n"
	cases := []struct {
		name       string
		text       string
		root       string
		links      func(string) (string, error)
		readErr    bool
		wantOut    string
		wantText   string
		wantWrites int
		wantKept   []string
		wantGone   string
	}{
		{name: "by root", text: twoProjects, root: "/work/beta", wantOut: ui.ProjectRemoved, wantText: "Removed beta from the list.", wantWrites: 1, wantGone: "/work/beta"},
		{name: "comments and dash table kept", text: withExtras, root: "/work/beta", wantOut: ui.ProjectRemoved, wantWrites: 1,
			wantKept: []string{"# my projects", "[dash]", "search = [\"/code\"]", "/work/alpha"}, wantGone: "/work/beta"},
		{name: "linked folder", text: "[[project]]\npath = \"/link/beta\"\nname = \"beta\"\n", root: "/work/beta", wantOut: ui.ProjectRemoved, wantWrites: 1,
			links: func(p string) (string, error) {
				if p == "/link/beta" {
					return "/work/beta", nil
				}
				return p, nil
			}},
		{name: "not listed", text: twoProjects, root: "/work/other/alpha", wantOut: ui.ProjectNotListed,
			wantText: "/work/other/alpha is not in config.toml"},
		{name: "unreadable", text: twoProjects, root: "/work/beta", readErr: true, wantOut: ui.ProjectFailed, wantText: "config.toml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProjectRig()
			r.text, r.exists = tc.text, true
			r.dirs["/link/beta"] = true
			r.repos["/link/beta"] = true
			env := r.env()
			if tc.readErr {
				env.Deps.ReadFile = func(string) ([]byte, error) { return nil, errors.New("disk on fire") }
			}
			links := tc.links
			if links == nil {
				links = dashNoLinks
			}
			got := dashRemoveProjectWith(env, links, tc.root)
			if got.Outcome != tc.wantOut || !strings.Contains(got.Text, tc.wantText) {
				t.Errorf("got %+v, want outcome %q containing %q", got, tc.wantOut, tc.wantText)
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

// TestDashDepsWiresProjectSeams pins the wiring: a dashboard built without
// the two seams says the + and - rows are not available, and no other test
// would notice.
func TestDashDepsWiresProjectSeams(t *testing.T) {
	deps := dashDeps("", dashLocation{}, nil, -1)
	if deps.AddProject == nil || deps.RemoveProject == nil {
		t.Errorf("AddProject nil = %v, RemoveProject nil = %v, want both wired", deps.AddProject == nil, deps.RemoveProject == nil)
	}
}
