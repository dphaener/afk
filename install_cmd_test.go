package main

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/install"
)

// strictInstallEnv is an env in which every seam that could change the
// machine fails the test. --dry-run and the refusals run against it, which
// is the proof that they change nothing.
func strictInstallEnv(t *testing.T, terminal bool, facts install.Facts) (installEnv, *int) {
	t.Helper()
	probes := 0
	bad := func(what string) { t.Errorf("%s was called; this path must change nothing", what) }
	return installEnv{
		isTerminal: func() bool { return terminal },
		probe:      func() install.Facts { probes++; return facts },
		runProgram: func(tea.Model) (tea.Model, error) { bad("runProgram"); return nil, errors.New("no") },
		handover:   func([]string, func(error) tea.Msg) tea.Cmd { bad("handover"); return nil },
		run:        func([]string) ([]byte, error) { bad("run"); return nil, errors.New("no") },
		executable: func() (string, error) { return "/fake/afk", nil },
		home:       "/home/u",
		goos:       "linux",
		uid:        func() int { return 501 },
		getenv:     func(string) string { return "" },
		setenv:     func(string, string) error { bad("setenv"); return nil },
		mkdirAll:   func(string, fs.FileMode) error { bad("mkdirAll"); return nil },
		writeText:  func(string, []byte) error { bad("writeText"); return nil },
		writeUnit:  func(string, []byte) error { bad("writeUnit"); return nil },
		writeUnattended: func() error {
			bad("writeUnattended")
			return nil
		},
	}, &probes
}

// TestRunInstallCommand covers the command around the screen. The
// load-bearing rows are the dry-run ones: --dry-run must print the plan with
// no terminal (a CI runner calls it, T11.4) and must reach no seam that
// could change anything; and "not a terminal" must refuse before the screen
// starts, because a half-answered question would hang a script.
func TestRunInstallCommand(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		terminal   bool
		wantCode   int
		wantOut    []string
		wantErr    []string
		wantProbes int
	}{
		{name: "dry run without a terminal prints thirteen steps", args: []string{"--dry-run"}, terminal: false, wantCode: 0,
			wantOut: []string{"nothing was changed", "[to do]  Install the tools afk needs", "[done]   Check that this machine", "Run afk install in a terminal"}, wantProbes: 1},
		{name: "dry run on a terminal is the same", args: []string{"--dry-run"}, terminal: true, wantCode: 0,
			wantOut: []string{"[to do]  Start the afk service"}, wantProbes: 1},
		{name: "not a terminal refuses with the plain message", args: nil, terminal: false, wantCode: 1,
			wantErr: []string{installNotTerminal}},
		{name: "--yes is not a way round the terminal", args: []string{"--yes"}, terminal: false, wantCode: 1,
			wantErr: []string{installNotTerminal}},
		{name: "bad flag", args: []string{"--bogus"}, terminal: true, wantCode: 2, wantErr: []string{"flag provided but not defined"}},
		{name: "help flag", args: []string{"-h"}, terminal: true, wantCode: 0, wantErr: []string{"afk install [--dry-run] [--yes]"}},
		{name: "help word", args: []string{"help"}, terminal: true, wantCode: 0, wantOut: []string{"safety screen comes first"}},
		{name: "stray argument", args: []string{"now"}, terminal: true, wantCode: 2, wantErr: []string{"takes no arguments"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, probes := strictInstallEnv(t, tt.terminal, freshInstallFacts())
			var out, errb bytes.Buffer
			code := runInstall(tt.args, &out, &errb, env)
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.wantCode, out.String(), errb.String())
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("stdout missing %q:\n%s", w, out.String())
				}
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(errb.String(), w) {
					t.Errorf("stderr missing %q:\n%s", w, errb.String())
				}
			}
			if *probes != tt.wantProbes {
				t.Errorf("probes = %d, want %d", *probes, tt.wantProbes)
			}
		})
	}
}

// TestRunInstallDryRunLines pins the dry-run shape: exactly one line per
// step, thirteen of them, and the fix on a cannot line.
func TestRunInstallDryRunLines(t *testing.T) {
	f := freshInstallFacts()
	f.BinHasAfk = false
	env, _ := strictInstallEnv(t, false, f)
	var out, errb bytes.Buffer
	if code := runInstall([]string{"--dry-run"}, &out, &errb, env); code != 0 {
		t.Fatalf("exit = %d: %s", code, errb.String())
	}
	n := 0
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(l, "[") {
			n++
			if strings.HasPrefix(l, "[cannot]") && !strings.Contains(l, "To fix: ") {
				t.Errorf("cannot line has no fix: %q", l)
			}
		}
	}
	if n != 13 {
		t.Errorf("got %d step lines, want 13:\n%s", n, out.String())
	}
}

// TestRunInstallExitCode checks the screen's own result becomes the exit
// code, and that a home folder afk cannot find stops the run before the
// screen.
func TestRunInstallExitCode(t *testing.T) {
	tests := []struct {
		name     string
		home     string
		final    int
		runErr   error
		wantCode int
		wantRun  bool
	}{
		{name: "finished run exits 0", home: "/home/u", final: 0, wantCode: 0, wantRun: true},
		{name: "failed step exits 1", home: "/home/u", final: 1, wantCode: 1, wantRun: true},
		{name: "screen error exits 1", home: "/home/u", runErr: errors.New("tty gone"), wantCode: 1, wantRun: true},
		{name: "no home folder exits 1 before the screen", home: "", wantCode: 1, wantRun: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := strictInstallEnv(t, true, freshInstallFacts())
			env.home = tt.home
			ran := false
			env.runProgram = func(m tea.Model) (tea.Model, error) {
				ran = true
				im := m.(*installModel)
				im.exitCode = tt.final
				return im, tt.runErr
			}
			var out, errb bytes.Buffer
			if code := runInstall(nil, &out, &errb, env); code != tt.wantCode {
				t.Errorf("exit = %d, want %d (%s)", code, tt.wantCode, errb.String())
			}
			if ran != tt.wantRun {
				t.Errorf("screen ran = %v, want %v", ran, tt.wantRun)
			}
		})
	}
}

// TestInstallTailscaleURL: the first https address wins and loses its
// trailing slash, because config.toml's public_url is written without one.
func TestInstallTailscaleURL(t *testing.T) {
	tests := []struct{ name, out, want string }{
		{"serve output", "Available within your tailnet:\n\nhttps://afk.tail1234.ts.net/\n|-- / proxy http://127.0.0.1:4253\n", "https://afk.tail1234.ts.net"},
		{"first of two", "https://a.ts.net/ https://b.ts.net/", "https://a.ts.net"},
		{"plain http is not taken", "http://127.0.0.1:4253", ""},
		{"nothing", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := installTailscaleURL(tt.out); got != tt.want {
				t.Errorf("installTailscaleURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestInstallIdentity: git's own values win over GitHub's, and the no-reply
// address is only built when GitHub gives no email. The no-reply row is
// load-bearing: most GitHub accounts keep their email private, so without it
// the --yes run would stop on the form on most machines.
func TestInstallIdentity(t *testing.T) {
	gh := installGitHubUser{name: "Ada GH", email: "ada@gh.example", id: "42", login: "ada"}
	tests := []struct {
		name               string
		gitName, gitEmail  string
		gh                 installGitHubUser
		wantName, wantMail string
	}{
		{"git values kept", "Ada", "ada@git.example", gh, "Ada", "ada@git.example"},
		{"github fills both", "", "", gh, "Ada GH", "ada@gh.example"},
		{"private email gives no-reply", "", "", installGitHubUser{name: "Ada", id: "42", login: "ada"}, "Ada", "42+ada@users.noreply.github.com"},
		{"nothing known stays empty", "", "", installGitHubUser{}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, e := installIdentity(tt.gitName, tt.gitEmail, tt.gh)
			if n != tt.wantName || e != tt.wantMail {
				t.Errorf("installIdentity() = %q, %q; want %q, %q", n, e, tt.wantName, tt.wantMail)
			}
		})
	}
}

// TestInstallExpandLocation: a relative location is refused here because
// planfolder would refuse it later, far from where it was typed.
func TestInstallExpandLocation(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"default", "~/code", "/home/u/code", false},
		{"home itself", "~", "/home/u", false},
		{"absolute cleaned", "/srv/projects/", "/srv/projects", false},
		{"relative refused", "code", "", true},
		{"empty refused", "  ", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := installExpandLocation(tt.in, "/home/u")
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("installExpandLocation(%q) = %q, %v; want %q, err %v", tt.in, got, err, tt.want, tt.wantErr)
			}
		})
	}
}
