package provider

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestLookup pins the name resolution. Load-bearing rows: "" must resolve to
// Claude (sessions with no recorded provider keep today's behavior), and the
// unknown row must name every valid provider so a typo is self-correcting.
func TestLookup(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantName string
		wantErr  bool
	}{
		{"empty is claude", "", ClaudeName, false},
		{"claude", "claude", ClaudeName, false},
		{"codex", "codex", CodexName, false},
		{"opencode", "opencode", OpenCodeName, false},
		{"unknown", "gemini", "", true},
		{"case sensitive", "Codex", "", true},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			p, err := Lookup(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Lookup(%q) = nil error, want error", tt.in)
				}
				for _, n := range Names() {
					if !strings.Contains(err.Error(), n) {
						t.Errorf("error %q does not list valid name %q", err, n)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Lookup(%q) error: %v", tt.in, err)
			}
			if p.Name() != tt.wantName {
				t.Errorf("Name() = %q, want %q", p.Name(), tt.wantName)
			}
		})
	}
}

// TestNames pins stable order and that Default is a valid name. The order is
// load-bearing: it feeds usage text, which must not shuffle between runs.
func TestNames(t *testing.T) {
	want := []string{"claude", "codex", "opencode"}
	if got := Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if _, err := Lookup(Default); err != nil {
		t.Errorf("Default %q does not resolve: %v", Default, err)
	}
}

// TestLaunchArgv covers both providers across the four mode combinations.
// Load-bearing rows: the Claude rows are byte-for-byte today's childArgv
// output (TestChildArgv), including autonomous being ignored for scratch; the
// Codex conductor rows always carry --autonomous because Codex runs
// autonomous-only; no row may carry an approval or sandbox bypass flag.
func TestLaunchArgv(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		scratch    bool
		autonomous bool
		want       []string
	}{
		{"claude plain", "claude", false, false, []string{"claude", "/afk:go --session s1"}},
		{"claude autonomous", "claude", false, true, []string{"claude", "/afk:go --session s1 --autonomous"}},
		{"claude scratch", "claude", true, false, []string{"claude"}},
		{"claude scratch autonomous", "claude", true, true, []string{"claude"}},
		{"codex plain", "codex", false, false, []string{"codex", "$afk:conductor --session s1 --autonomous"}},
		{"codex autonomous", "codex", false, true, []string{"codex", "$afk:conductor --session s1 --autonomous"}},
		{"codex scratch", "codex", true, false, []string{"codex"}},
		{"codex scratch autonomous", "codex", true, true, []string{"codex"}},
		{"opencode plain", "opencode", false, false, []string{"opencode", "--standalone", "--prompt", "/afk-go --session s1"}},
		{"opencode autonomous", "opencode", false, true, []string{"opencode", "--standalone", "--prompt", "/afk-go --session s1 --autonomous"}},
		{"opencode scratch", "opencode", true, false, []string{"opencode", "--standalone"}},
		{"opencode scratch autonomous", "opencode", true, true, []string{"opencode", "--standalone"}},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			p, err := Lookup(tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			got := p.LaunchArgv("s1", tt.scratch, tt.autonomous)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LaunchArgv = %q, want %q", got, tt.want)
			}
			for _, a := range got {
				for _, banned := range []string{"--dangerously", "danger-full-access", "-a never", "--yolo"} {
					if strings.Contains(a, banned) {
						t.Errorf("argv %q contains bypass flag %q", got, banned)
					}
				}
				// --auto is an exact-match ban: a substring match would
				// flag the conductor's own --autonomous.
				if a == "--auto" {
					t.Errorf("argv %q contains bypass flag --auto", got)
				}
			}
		})
	}
}

// TestResumeArgv: the Claude row is today's TestResumeArgv expectation
// exactly, with the same bans (no fork, no re-launch of the conductor); the
// Codex row is the `codex resume ID` subcommand form.
func TestResumeArgv(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     []string
	}{
		{"claude", "claude", []string{"claude", "--resume", "abc-123"}},
		{"codex", "codex", []string{"codex", "resume", "abc-123"}},
		{"opencode", "opencode", []string{"opencode", "--standalone", "-s", "abc-123"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Lookup(tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			got := p.ResumeArgv("abc-123")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ResumeArgv = %q, want %q", got, tt.want)
			}
			for _, a := range got {
				for _, banned := range []string{"--fork-session", "/afk:go", "--session", "afk:conductor"} {
					if strings.Contains(a, banned) {
						t.Errorf("resume argv %q contains %q", got, banned)
					}
				}
			}
		})
	}
}

// TestPreflight drives each provider with a fake run. Load-bearing rows: the
// Codex "not installed" row (a naive substring match on "installed" would
// pass it) and the run-error rows (preflight is best-effort and must degrade
// to a warning, never panic or report ok). The fake also asserts the exact
// command so the preflight cannot drift to a different subcommand.
func TestPreflight(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		out      string
		err      error
		wantOK   bool
		wantCmd  string
		manual   string
	}{
		{"claude present", "claude", "afk@claude-marche  enabled\n", nil, true, "claude plugin list", ""},
		{"claude present mixed case", "claude", "Afk Plugin\n", nil, true, "claude plugin list", ""},
		{"claude absent", "claude", "other@x\n", nil, false, "claude plugin list", "afk plugin install --target claude"},
		{"claude run error", "claude", "", errors.New("boom"), false, "claude plugin list", "afk plugin install --target claude"},
		{"codex installed", "codex", "other@m  installed\nafk@claude-marche  installed\n", nil, true, "codex plugin list", ""},
		{"codex installed enabled", "codex", "afk@m  installed, enabled\n", nil, true, "codex plugin list", ""},
		{"codex not installed", "codex", "afk@claude-marche  not installed\n", nil, false, "codex plugin list", "afk plugin install --target codex"},
		{"codex absent", "codex", "other@m  installed\n", nil, false, "codex plugin list", "autonomous"},
		{"codex name only elsewhere", "codex", "other@m  afk installed\n", nil, false, "codex plugin list", "afk plugin install --target codex"},
		{"codex empty output", "codex", "", nil, false, "codex plugin list", "afk plugin install --target codex"},
		{"codex run error", "codex", "", errors.New("boom"), false, "codex plugin list", "afk plugin install --target codex"},
		{"opencode installed", "opencode", "ID      VERSION  SOURCE\nother   1.0.0    npm\nafk   0.2.0    local\n", nil, true, "opencode plugin list", ""},
		{"opencode header only", "opencode", "ID      VERSION  SOURCE\n\n", nil, false, "opencode plugin list", "~/.config/opencode/plugins/afk/"},
		{"opencode afk-other id", "opencode", "ID  VERSION  SOURCE\nafk-other  1.0.0  local\n", nil, false, "opencode plugin list", "opencode plugin list"},
		{"opencode run error", "opencode", "", errors.New("boom"), false, "opencode plugin list", "~/.config/opencode/plugins/afk/"},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			p, err := Lookup(tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			var gotCmd, gotDir string
			run := func(dir, name string, args ...string) (string, error) {
				gotDir = dir
				gotCmd = strings.Join(append([]string{name}, args...), " ")
				return tt.out, tt.err
			}
			ok, manual := p.Preflight("/repo", run)
			if gotCmd != tt.wantCmd || gotDir != "/repo" {
				t.Errorf("ran %q in %q, want %q in /repo", gotCmd, gotDir, tt.wantCmd)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok {
				if manual != "" {
					t.Errorf("manual = %q, want empty when ok", manual)
				}
				return
			}
			if !strings.Contains(manual, tt.manual) {
				t.Errorf("manual %q missing %q", manual, tt.manual)
			}
		})
	}
}

// TestClaudePreflightManualText pins the manual-steps text moved from
// session.defaultAfkPreflight so the move stays byte-for-byte.
func TestClaudePreflightManualText(t *testing.T) {
	p, _ := Lookup("claude")
	_, manual := p.Preflight("/r", func(string, string, ...string) (string, error) { return "", nil })
	want := "afk plugin / `/afk:go` not detected in this repo.\n" +
		"      Install it, then re-run, or launch manually:\n" +
		"        1. afk plugin install --target claude\n" +
		"        2. Confirm /afk:go is listed via /help"
	if manual != want {
		t.Errorf("manual =\n%q\nwant\n%q", manual, want)
	}
}

// TestCheckMode pins the provider/mode rule. Load-bearing rows: codex with
// neither flag must return exactly CodexInteractiveConflictMsg (`afk new`
// prints it verbatim and the dashboard form shows it inline), and the claude
// rows must stay nil in every mode so the default provider is never gated.
func TestCheckMode(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		scratch    bool
		autonomous bool
		wantMsg    string
		wantUnk    bool
	}{
		{"claude interactive", "claude", false, false, "", false},
		{"claude autonomous", "claude", false, true, "", false},
		{"claude scratch", "claude", true, false, "", false},
		{"empty interactive", "", false, false, "", false},
		{"codex autonomous", "codex", false, true, "", false},
		{"codex scratch", "codex", true, false, "", false},
		{"codex scratch and autonomous", "codex", true, true, "", false},
		{"codex interactive", "codex", false, false, CodexInteractiveConflictMsg, false},
		{"opencode interactive", "opencode", false, false, "", false},
		{"opencode autonomous", "opencode", false, true, "", false},
		{"opencode scratch", "opencode", true, false, "", false},
		{"unknown name", "gemini", false, false, "", true},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			err := CheckMode(tt.provider, tt.scratch, tt.autonomous)
			switch {
			case tt.wantUnk:
				_, want := Lookup(tt.provider)
				if err == nil || err.Error() != want.Error() {
					t.Errorf("CheckMode(%q) = %v, want Lookup's error %v", tt.provider, err, want)
				}
			case tt.wantMsg != "":
				if err == nil || err.Error() != tt.wantMsg {
					t.Errorf("CheckMode = %v, want %q", err, tt.wantMsg)
				}
			default:
				if err != nil {
					t.Errorf("CheckMode = %v, want nil", err)
				}
			}
		})
	}
}
