package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/decisionlog"
)

// TestResolvePauseTarget pins how an argument becomes a project folder.
//
// Load-bearing rows: "name beats folder" (a stray folder must not redirect a
// pause); both unknown-name rows (the error must tell the owner what to do
// next, whether or not projects are configured).
func TestResolvePauseTarget(t *testing.T) {
	projects := []daemonconfig.Project{{Name: "alpha", Path: "/p/alpha"}, {Name: "beta", Path: "/p/beta"}}
	tests := []struct {
		name      string
		arg, root string
		cwd       string
		projects  []daemonconfig.Project
		want      string
		errHas    []string
	}{
		{name: "name hit", arg: "beta", cwd: "/c", projects: projects, want: "/p/beta"},
		{name: "path hit", arg: "/x/app", root: "/x/app", cwd: "/c", projects: projects, want: "/x/app"},
		{name: "name beats folder", arg: "alpha", root: "/c/alpha", projects: projects, want: "/p/alpha"},
		{name: "no argument", cwd: "/c", projects: projects, want: "/c"},
		{name: "no argument, no folder", errHas: []string{"Pass a project name or a folder"}},
		{name: "unknown name lists known names", arg: "gamma", projects: projects, errHas: []string{`"gamma"`, "alpha, beta"}},
		{name: "unknown name, none configured", arg: "gamma", errHas: []string{"no projects are configured", "config.toml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePauseTarget(tt.arg, tt.root, tt.cwd, tt.projects)
			if len(tt.errHas) > 0 {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				for _, w := range tt.errHas {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q lacks %q", err, w)
					}
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func testPauseEnv(cwd string, projects []daemonconfig.Project, cfgErr error) pauseEnv {
	return pauseEnv{
		cwd:          cwd,
		gitCommonDir: func(string) string { return "" },
		isDir: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		},
		loadConfig: func() (daemonconfig.Config, error) { return daemonconfig.Config{Projects: projects}, cfgErr },
		now:        func() time.Time { return time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC) },
	}
}

// TestPauseResumeCommands runs both commands against a temp project.
//
// Load-bearing rows: pause writes the flag and a decision-log line whose
// chosen_by is owner; resume removes the flag and logs; resume on an unpaused
// project says so, exits 0 and logs nothing.
func TestPauseResumeCommands(t *testing.T) {
	root := t.TempDir()
	env := testPauseEnv(root, nil, nil)
	var so, se bytes.Buffer

	if code := runResume(nil, &so, &se, env); code != 0 || !strings.Contains(so.String(), "was not paused") {
		t.Fatalf("resume unpaused: code=%d out=%q err=%q", code, so.String(), se.String())
	}
	if _, err := os.Stat(decisionlog.Path(root)); err == nil {
		t.Error("resume on an unpaused project wrote a log line")
	}

	so.Reset()
	if code := runPause(nil, &so, &se, env); code != 0 {
		t.Fatalf("pause: code=%d err=%q", code, se.String())
	}
	if !daemonstate.Paused(root) {
		t.Error("pause did not write the flag")
	}
	for _, w := range []string{"No new task will start", "will finish"} {
		if !strings.Contains(so.String(), w) {
			t.Errorf("pause output %q lacks %q", so.String(), w)
		}
	}
	entries, err := decisionlog.Read(decisionlog.Path(root))
	if err != nil || len(entries) != 1 || entries[0].ChosenBy != "owner" || entries[0].Kind != "pause" {
		t.Fatalf("log after pause: %+v, %v", entries, err)
	}

	so.Reset()
	if code := runResume(nil, &so, &se, env); code != 0 || !strings.Contains(so.String(), "Resumed") {
		t.Fatalf("resume: code=%d out=%q err=%q", code, so.String(), se.String())
	}
	if daemonstate.Paused(root) {
		t.Error("resume left the flag")
	}
	entries, _ = decisionlog.Read(decisionlog.Path(root))
	if len(entries) != 2 || entries[1].Kind != "resume" || entries[1].ChosenBy != "owner" {
		t.Errorf("log after resume: %+v", entries)
	}
}

// TestPauseTargets covers the command-level argument handling.
//
// Load-bearing rows: a configured name pauses that project's folder; the
// unknown name and the two-word line write no flag anywhere and exit 1 and 2;
// a folder argument still works when the config is unreadable.
func TestPauseTargets(t *testing.T) {
	proj := t.TempDir()
	other := t.TempDir()
	cwd := t.TempDir()
	configured := []daemonconfig.Project{{Name: "alpha", Path: proj}}
	tests := []struct {
		name    string
		args    []string
		cfgErr  error
		code    int
		paused  string // folder expected to hold the flag, "" for none
		errHas  string
		allDirs []string
	}{
		{name: "by name", args: []string{"alpha"}, code: 0, paused: proj},
		{name: "by folder", args: []string{other}, code: 0, paused: other},
		{name: "folder with unreadable config", args: []string{other}, cfgErr: errors.New("bad toml"), code: 0, paused: other},
		{name: "unknown name", args: []string{"nope"}, code: 1, errHas: "Known projects: alpha"},
		{name: "unknown name, unreadable config", args: []string{"nope"}, cfgErr: errors.New("bad toml"), code: 1, errHas: "bad toml"},
		{name: "two arguments", args: []string{"alpha", "beta"}, code: 2, errHas: "at most one project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, d := range []string{proj, other, cwd} {
				_, _ = daemonstate.Resume(d)
			}
			var so, se bytes.Buffer
			code := runPause(tt.args, &so, &se, testPauseEnv(cwd, configured, tt.cfgErr))
			if code != tt.code {
				t.Fatalf("code=%d want %d; stderr=%q", code, tt.code, se.String())
			}
			if tt.errHas != "" && !strings.Contains(se.String(), tt.errHas) {
				t.Errorf("stderr %q lacks %q", se.String(), tt.errHas)
			}
			for _, d := range []string{proj, other, cwd} {
				if got, want := daemonstate.Paused(d), d == tt.paused; got != want {
					t.Errorf("Paused(%s)=%v want %v", d, got, want)
				}
			}
		})
	}
}
