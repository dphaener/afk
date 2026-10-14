package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/ui"
)

// TestLoadDashListed pins the real reader's rules against temp folders and a
// counting git fake. Load-bearing rows:
//   - "gone folder": a deleted project is marked Gone and no git is run for it
//     (a dashboard tick must not spawn a process per dead folder).
//   - "linked worktree": the row must be the main checkout, or the switcher
//     shows a second row for a project it already has.
//   - "symlinked path": the merge compares strings, so the path must be
//     spelled the way internal/scan spells it (resolved).
//   - "same checkout twice": one project, one row, first entry wins.
//   - "invalid TOML" and "bad value": the error names the file and no entries
//     come back, so the dashboard keeps the rows it holds.
func TestLoadDashListed(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkdir := func(name string) string {
		p := filepath.Join(tmp, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := mkdir("alpha"), mkdir("beta")
	main := mkdir("main")
	wt := mkdir("wt-main")
	realDir := mkdir("real")
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tmp, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(tmp, "gone")

	toml := func(entries ...string) string { return strings.Join(entries, "\n") }
	proj := func(path, name string) string {
		s := "[[project]]\npath = \"" + path + "\"\n"
		if name != "" {
			s += "name = \"" + name + "\"\n"
		}
		return s
	}

	tests := []struct {
		name        string
		file        *string // nil: no file
		commonDirs  map[string]string
		realSymlink bool
		want        []ui.ListedProject
		wantErr     string // substring of the error, "" for none
		wantGit     []string
	}{
		{name: "no file", want: nil},
		{name: "two entries in file order",
			file: ptr(toml(proj(a, ""), proj(b, ""))),
			want: []ui.ListedProject{{Path: a, Name: "alpha"}, {Path: b, Name: "beta"}}, wantGit: []string{a, b}},
		{name: "name key",
			file: ptr(proj(a, "Shop")),
			want: []ui.ListedProject{{Path: a, Name: "Shop"}}, wantGit: []string{a}},
		{name: "gone folder",
			file: ptr(toml(proj(gone, "old"), proj(a, ""))),
			want: []ui.ListedProject{{Path: gone, Name: "old", Gone: true}, {Path: a, Name: "alpha"}}, wantGit: []string{a}},
		{name: "path is a file",
			file: ptr(proj(file, "")),
			want: []ui.ListedProject{{Path: file, Name: "afile", Gone: true}}},
		{name: "linked worktree",
			file:       ptr(proj(wt, "")),
			commonDirs: map[string]string{wt: filepath.Join(main, ".git")},
			want:       []ui.ListedProject{{Path: main, Name: "wt-main"}}, wantGit: []string{wt}},
		{name: "symlinked path", realSymlink: true,
			file: ptr(proj(link, "")),
			want: []ui.ListedProject{{Path: realDir, Name: "link"}}, wantGit: []string{link}},
		{name: "same checkout twice",
			file:       ptr(toml(proj(main, "first"), proj(wt, "second"))),
			commonDirs: map[string]string{wt: filepath.Join(main, ".git"), main: filepath.Join(main, ".git")},
			want:       []ui.ListedProject{{Path: main, Name: "first"}}, wantGit: []string{main, wt}},
		{name: "invalid TOML",
			file: ptr("[[project\npath = 1"), wantErr: "config.toml"},
		{name: "bad value elsewhere in the file",
			file:    ptr(toml(proj(a, ""), "[daemon]\ntick = \"banana\"")),
			wantErr: "config.toml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			dir := filepath.Join(cfgHome, "afk")
			wantPath := filepath.Join(dir, "config.toml")
			if tt.file != nil {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(wantPath, []byte(*tt.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var gitCalls []string
			parts := dashProjectParts{
				cfg: daemonconfig.Deps{
					Getenv:   func(k string) string { return map[string]string{"XDG_CONFIG_HOME": cfgHome}[k] },
					HomeDir:  func() (string, error) { return "", errors.New("no home") },
					ReadFile: os.ReadFile,
				},
				isDir: func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() },
				gitCommonDir: func(d string) string {
					gitCalls = append(gitCalls, d)
					return tt.commonDirs[d]
				},
				evalSymlinks: func(p string) (string, error) { return p, nil },
			}
			if tt.realSymlink {
				parts.evalSymlinks = filepath.EvalSymlinks
			}

			got, path, err := loadDashListed(parts)
			if path != wantPath {
				t.Errorf("path = %q, want %q", path, wantPath)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, tt.wantErr)
				}
				if len(got) != 0 {
					t.Errorf("entries on error = %+v, want none", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("entries = %+v, want %+v", got, tt.want)
			}
			if tt.wantGit != nil || len(gitCalls) > 0 {
				if !reflect.DeepEqual(gitCalls, tt.wantGit) {
					t.Errorf("git called for %v, want %v", gitCalls, tt.wantGit)
				}
			}
		})
	}
}

// TestLaunchMainCheckout: only the pinned row counts, and no pinned row means
// no git call and an empty answer.
func TestLaunchMainCheckout(t *testing.T) {
	id := func(p string) (string, error) { return p, nil }
	git := func(d string) string {
		if d == "/wt/app" {
			return "/main/app/.git"
		}
		return ""
	}
	tests := []struct {
		name     string
		projects []project.Project
		want     string
	}{
		{"none", nil, ""},
		{"unpinned only", []project.Project{{Path: "/wt/app"}}, ""},
		{"pinned worktree", []project.Project{{Path: "/x"}, {Path: "/wt/app", Pinned: true}}, "/main/app"},
		{"pinned plain folder", []project.Project{{Path: "/solo", Pinned: true}}, "/solo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchMainCheckout(tt.projects, git, id); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
