package main

import (
	"errors"
	"strings"
	"testing"
)

// nonProjectLines drops the [[project]] blocks from config text so a test can
// compare everything else (comments, [dash]) byte for byte.
func nonProjectLines(s string) string {
	var out []string
	inProject := false
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			inProject = t == "[[project]]"
		}
		if inProject || (t == "" && len(out) > 0 && out[len(out)-1] == "") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

const commentedConfig = "# my machine\n[dash]\n# where to look\nsearch = [\"~/code\"]\n\n# the daemon's list\n[[project]]\npath = \"/work/alpha\"\n"

// TestAddProjectForSurface covers every outcome of the add entry point. The
// load-bearing rows: "not a repository" and "name clash" (refused, nothing
// written, so the daemon is never given a project it cannot build), "no config
// folder" and "write failure" (failed, and the file is unchanged), and
// "comments and [dash] survive" (the dashboard edits the owner's own file).
func TestAddProjectForSurface(t *testing.T) {
	cases := []struct {
		name        string
		text        string
		exists      bool
		noXDG       bool
		readErr     bool
		writeErr    bool
		dir, give   string
		wantOutcome projectOutcome
		wantName    string
		wantCode    int
		wantStderr  bool
		wantText    string
		wantWrites  int
	}{
		{name: "added", dir: "/work/beta", wantOutcome: projectAdded, wantName: "beta", wantWrites: 1,
			wantText: "Added beta (/work/beta). A running afk daemon picks it up within a few seconds."},
		{name: "added with a name", dir: "/work/beta", give: "web", wantOutcome: projectAdded, wantName: "web", wantWrites: 1,
			wantText: "Added web (/work/beta)."},
		{name: "already listed reports the listed name", text: "[[project]]\npath = \"/work/alpha\"\nname = \"web\"\n", exists: true,
			dir: "/work/alpha", wantOutcome: projectAlreadyListed, wantName: "web", wantText: "web is already in the list. Nothing was changed."},
		{name: "not a folder", dir: "/work/missing", wantOutcome: projectRefused, wantCode: 1, wantStderr: true,
			wantText: "/work/missing is not a folder. Check the path."},
		{name: "not a git repository", dir: "/work/plain", wantOutcome: projectRefused, wantCode: 1, wantStderr: true,
			wantText: "/work/plain is not a git repository, so the daemon could not build in it."},
		{name: "name clash", text: twoProjects, exists: true, dir: "/work/other/alpha", wantOutcome: projectRefused, wantCode: 1, wantStderr: true,
			wantText: `the name "alpha" is already used`},
		{name: "no config folder", noXDG: true, dir: "/work/beta", wantOutcome: projectFailed, wantCode: 1, wantStderr: true,
			wantText: "cannot find afk's config folder"},
		{name: "unreadable config", readErr: true, dir: "/work/beta", wantOutcome: projectFailed, wantCode: 1, wantStderr: true,
			wantText: "/cfg/afk/config.toml: cannot read the file"},
		{name: "write failure", text: "[[project]]\npath = \"/work/alpha\"\n", exists: true, writeErr: true, dir: "/work/beta",
			wantOutcome: projectFailed, wantCode: 1, wantStderr: true, wantText: "The file was not changed"},
		{name: "comments and [dash] survive", text: commentedConfig, exists: true, dir: "/work/beta", wantOutcome: projectAdded, wantName: "beta", wantWrites: 1,
			wantText: "Added beta"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProjectRig()
			r.text, r.exists = tc.text, tc.exists
			env := r.env()
			if tc.noXDG {
				env.Getenv = func(string) string { return "" }
				env.HomeDir = func() (string, error) { return "", errors.New("no home") }
			}
			if tc.readErr {
				env.ReadFile = func(string) ([]byte, error) { return nil, errors.New("permission denied") }
			}
			if tc.writeErr {
				env.write = func(string, []byte) error { return errors.New("disk full") }
			}
			before := r.text
			res := addProjectForSurface(env, tc.dir, tc.give)
			if res.Outcome != tc.wantOutcome || res.Code != tc.wantCode || res.ToStderr != tc.wantStderr {
				t.Fatalf("got %+v, want outcome %d code %d stderr %v", res, tc.wantOutcome, tc.wantCode, tc.wantStderr)
			}
			if res.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", res.Name, tc.wantName)
			}
			if !strings.Contains(res.Text, tc.wantText) {
				t.Errorf("Text = %q, want it to contain %q", res.Text, tc.wantText)
			}
			if strings.HasPrefix(res.Text, "afk project") {
				t.Errorf("Text carries the command prefix: %q", res.Text)
			}
			if r.writes != tc.wantWrites {
				t.Errorf("writes = %d, want %d", r.writes, tc.wantWrites)
			}
			if r.writes == 0 && r.text != before {
				t.Errorf("config changed without a write: %q", r.text)
			}
			if tc.name == "comments and [dash] survive" {
				if got, want := nonProjectLines(r.text), nonProjectLines(commentedConfig); got != want {
					t.Errorf("non-project lines changed:\n%q\nwant\n%q", got, want)
				}
				if !strings.Contains(r.text, "/work/beta") {
					t.Errorf("project not added: %q", r.text)
				}
			}
		})
	}
}

// TestRemoveProjectForSurface covers every outcome of the remove entry point.
// Load-bearing rows: "not listed" (a typo removes nothing and has no prefix),
// "ambiguous key" (refused, nothing written), "write failure" (failed) and
// "comments and [dash] survive".
func TestRemoveProjectForSurface(t *testing.T) {
	cases := []struct {
		name        string
		text        string
		exists      bool
		noXDG       bool
		readErr     bool
		writeErr    bool
		key         string
		wantOutcome projectOutcome
		wantCode    int
		wantStderr  bool
		wantText    string
		wantWrites  int
	}{
		{name: "removed by name", text: twoProjects, exists: true, key: "alpha", wantOutcome: projectRemoved, wantWrites: 1,
			wantText: "Removed alpha from the list. Its files and any session that is running were left alone."},
		{name: "removed by folder", text: twoProjects, exists: true, key: "/work/beta", wantOutcome: projectRemoved, wantWrites: 1,
			wantText: "Removed /work/beta from the list."},
		{name: "not listed", text: twoProjects, exists: true, key: "nosuch", wantOutcome: projectNotListed, wantCode: 1, wantStderr: true,
			wantText: "No project named nosuch. Run `afk project list` to see what is listed."},
		{name: "ambiguous key", text: "[[project]]\npath = \"/a/x\"\nname = \"/b/y\"\n\n[[project]]\npath = \"/b/y\"\n", exists: true,
			key: "/b/y", wantOutcome: projectRefused, wantCode: 1, wantStderr: true, wantText: "matches more than one project"},
		{name: "no config folder", noXDG: true, key: "alpha", wantOutcome: projectFailed, wantCode: 1, wantStderr: true,
			wantText: "cannot find afk's config folder"},
		{name: "unreadable config", readErr: true, key: "alpha", wantOutcome: projectFailed, wantCode: 1, wantStderr: true,
			wantText: "cannot read the file"},
		{name: "write failure", text: twoProjects, exists: true, writeErr: true, key: "alpha", wantOutcome: projectFailed, wantCode: 1, wantStderr: true,
			wantText: "The file was not changed."},
		{name: "comments and [dash] survive", text: commentedConfig, exists: true, key: "alpha", wantOutcome: projectRemoved, wantWrites: 1,
			wantText: "Removed alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newProjectRig()
			r.text, r.exists = tc.text, tc.exists
			env := r.env()
			if tc.noXDG {
				env.Getenv = func(string) string { return "" }
				env.HomeDir = func() (string, error) { return "", errors.New("no home") }
			}
			if tc.readErr {
				env.ReadFile = func(string) ([]byte, error) { return nil, errors.New("permission denied") }
			}
			if tc.writeErr {
				env.write = func(string, []byte) error { return errors.New("disk full") }
			}
			before := r.text
			res := removeProjectForSurface(env, tc.key)
			if res.Outcome != tc.wantOutcome || res.Code != tc.wantCode || res.ToStderr != tc.wantStderr {
				t.Fatalf("got %+v, want outcome %d code %d stderr %v", res, tc.wantOutcome, tc.wantCode, tc.wantStderr)
			}
			if !strings.Contains(res.Text, tc.wantText) {
				t.Errorf("Text = %q, want it to contain %q", res.Text, tc.wantText)
			}
			if r.writes != tc.wantWrites {
				t.Errorf("writes = %d, want %d", r.writes, tc.wantWrites)
			}
			if r.writes == 0 && r.text != before {
				t.Errorf("config changed without a write: %q", r.text)
			}
			if tc.name == "comments and [dash] survive" {
				if got, want := nonProjectLines(r.text), nonProjectLines(commentedConfig); got != want {
					t.Errorf("non-project lines changed:\n%q\nwant\n%q", got, want)
				}
				if strings.Contains(r.text, "/work/alpha") {
					t.Errorf("project not removed: %q", r.text)
				}
			}
		})
	}
}
