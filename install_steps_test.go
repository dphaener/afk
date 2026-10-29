package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/install"
)

const rexConfigPath = fakeHome + "/.config/afk/config.toml"

// TestRunRex drives the Rex offer through the runner against the fake
// machine. Load-bearing rows: "a damaged file is refused" must leave the file
// byte for byte as it was and name config.toml, because a half-edited config
// would stop the daemon from loading; "no" must write nothing; the comment
// rows prove the edit is a text edit and not a rewrite of the file.
func TestRunRex(t *testing.T) {
	const comments = "# my machine\n[web]\nbind = \"127.0.0.1:4253\" # local only\n"
	tests := []struct {
		name      string
		config    string // "" means no file
		key       tea.Msg
		wantFile  string // exact file text afterwards
		wantWrite bool
		wantPhase installPhase
		wantScr   []string
	}{
		{
			name: "yes writes the key and keeps comments", config: comments, key: keyRune('y'),
			wantFile:  comments + "\n[runtime]\nbackend = \"rex\"\n",
			wantWrite: true, wantPhase: installDone,
		},
		{
			name: "yes with no config file creates it", config: "", key: keyRune('y'),
			wantFile:  "[runtime]\nbackend = \"rex\"\n",
			wantWrite: true, wantPhase: installDone,
		},
		{
			name: "yes replaces an afk backend in place and keeps comments", key: keyRune('y'),
			config:    "# top\n[runtime]\n# where sessions run\nbackend = \"afk\"\n\n[web]\nbind = \"0.0.0.0:4253\"\n",
			wantFile:  "# top\n[runtime]\n# where sessions run\nbackend = \"rex\"\n\n[web]\nbind = \"0.0.0.0:4253\"\n",
			wantWrite: true, wantPhase: installDone,
		},
		{
			name: "yes adds the key to a runtime table that lacks it", key: keyRune('y'),
			config:    "[runtime]\n# nothing yet\n\n[web]\nbind = \"0.0.0.0:4253\"\n",
			wantFile:  "[runtime]\n# nothing yet\nbackend = \"rex\"\n\n[web]\nbind = \"0.0.0.0:4253\"\n",
			wantWrite: true, wantPhase: installDone,
		},
		{
			name: "no leaves the file untouched", config: comments, key: keyRune('n'),
			wantFile: comments, wantPhase: installFailed,
			wantScr: []string{"You said no"},
		},
		{
			name: "a damaged file is refused and left as it was", config: "[web\nbind = = \n", key: keyRune('y'),
			wantFile: "[web\nbind = = \n", wantPhase: installFailed,
			wantScr: []string{"config.toml", "mistake"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeInstall(t)
			f.facts = doneInstallFacts()
			f.facts.RexFound, f.facts.RexServer = true, true
			f.effects["write "+rexConfigPath] = func(fa *install.Facts) { fa.RuntimeBackend = "rex" }
			if tt.config != "" {
				f.files[rexConfigPath] = tt.config
			}
			m := startInstall(t, f, false, 120)
			drive(t, m, keyEnter, tt.key)
			if m.phase != tt.wantPhase {
				t.Fatalf("phase = %v, want %v\n%s", m.phase, tt.wantPhase, m.View().Content)
			}
			got, ok := f.files[rexConfigPath]
			if !ok && tt.config != "" {
				t.Fatal("config file vanished")
			}
			if got != tt.wantFile {
				t.Errorf("config.toml = %q, want %q", got, tt.wantFile)
			}
			wrote := false
			for _, l := range f.log {
				if l == "write "+rexConfigPath {
					wrote = true
				}
			}
			if wrote != tt.wantWrite {
				t.Errorf("wrote = %v, want %v (log %v)", wrote, tt.wantWrite, f.log)
			}
			scr := m.View().Content
			for _, w := range tt.wantScr {
				if !strings.Contains(scr, w) {
					t.Errorf("screen missing %q:\n%s", w, scr)
				}
			}
		})
	}
}

// TestRunRexOnlyWhenToDo: a Cannot Rex step (no Rex) and a Done one (already
// rex) never reach the offer, so config.toml is not written.
func TestRunRexOnlyWhenToDo(t *testing.T) {
	for name, edit := range map[string]func(*install.Facts){
		"no rex":      func(*install.Facts) {},
		"already rex": func(f *install.Facts) { f.RexFound, f.RexServer, f.RuntimeBackend = true, true, "rex" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeInstall(t)
			f.facts = doneInstallFacts()
			edit(&f.facts)
			m := startInstall(t, f, true, 120)
			drive(t, m, keyEnter)
			for _, l := range f.log {
				if strings.HasPrefix(l, "write ") {
					t.Errorf("unexpected write: %v", f.log)
				}
			}
		})
	}
}
