package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/profile"
)

// testProfileEnv returns an env whose config folder and home are fresh temp
// folders, so nothing reaches the real config, and the profile path.
func testProfileEnv(t *testing.T) (profileEnv, string) {
	t.Helper()
	xdg, home := t.TempDir(), t.TempDir()
	env := profileEnv{
		Deps: profile.Deps{
			Getenv:  func(k string) string { return map[string]string{"XDG_CONFIG_HOME": xdg}[k] },
			HomeDir: func() (string, error) { return home, nil },
		},
		stat:   os.Stat,
		write:  profile.Write,
		create: profile.Create,
	}
	return env, profile.Path(env.Deps)
}

func runP(t *testing.T, env profileEnv, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runProfile(args, &out, &errb, env)
	return code, out.String(), errb.String()
}

func putProfile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readP(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const sampleProfile = `rule_files = ["/x/rules.md"]

[[entry]]
topic = "database"
value = "PostgreSQL"
strength = "rule"
reason = "house rule"

[[entry]]
topic = "database"
value = "SQLite"
kind = "cli"
`

// TestProfileList: load-bearing rows are "no file" (exit 0 and names init: a
// fresh machine must not see an error), "file with an error" (exit 1 and no
// entries on stdout: a plan must never read a half-valid profile) and the
// two --kind rows (a kind-specific entry beats a general one; with none, the
// general entries apply and stderr says so).
func TestProfileList(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		args     []string
		wantCode int
		wantOut  []string
		notOut   []string
		wantErr  []string
	}{
		{name: "no file", wantCode: 0, wantOut: []string{"afk profile init"}},
		{name: "entries", file: sampleProfile, wantCode: 0,
			wantOut: []string{"Profile: ", "Rule files:", "/x/rules.md", "rule  database: PostgreSQL — house rule", "default  database: SQLite [cli]"}},
		{name: "kind picks specific", file: sampleProfile, args: []string{"--kind", "cli"}, wantCode: 0,
			wantOut: []string{"default  database: SQLite [cli]"}, notOut: []string{"PostgreSQL"}},
		{name: "kind without entry uses general", file: sampleProfile, args: []string{"--kind", "web-app"}, wantCode: 0,
			wantOut: []string{"rule  database: PostgreSQL — house rule"}, notOut: []string{"SQLite"},
			wantErr: []string{"No entry is specific to web-app", "general entries apply"}},
		{name: "bad kind", file: sampleProfile, args: []string{"--kind", "Web App"}, wantCode: 2},
		{name: "file with error", file: "[[entry]]\ntopic = \"x\"\nstrength = \"maybe\"\nvalue = \"v\"\n", wantCode: 1,
			notOut: []string{"x: v", "Profile:"}, wantErr: []string{"afk profile check"}},
		{name: "no entries", file: "rule_files = []\n", wantCode: 0, wantOut: []string{"afk profile set <topic>"}},
		{name: "syntax error", file: "[[entry\n", wantCode: 1, wantErr: []string{"profile.toml"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, path := testProfileEnv(t)
			if tc.file != "" {
				putProfile(t, path, tc.file)
			}
			code, out, errs := runP(t, env, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.wantCode, out, errs)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out, w) {
					t.Errorf("stdout missing %q:\n%s", w, out)
				}
			}
			for _, w := range tc.notOut {
				if strings.Contains(out, w) {
					t.Errorf("stdout has %q:\n%s", w, out)
				}
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(errs, w) {
					t.Errorf("stderr missing %q:\n%s", w, errs)
				}
			}
		})
	}
}

// TestProfileInit: the second init must leave the file byte-identical.
func TestProfileInit(t *testing.T) {
	env, path := testProfileEnv(t)
	code, out, errs := runP(t, env, "init")
	if code != 0 || !strings.Contains(out, path) {
		t.Fatalf("first init: exit %d\n%s\n%s", code, out, errs)
	}
	if readP(t, path) != profile.Template() {
		t.Error("file is not the template")
	}
	before := readP(t, path)
	code, _, errs = runP(t, env, "init")
	if code != 1 || !strings.Contains(errs, "already exists") || !strings.Contains(errs, "not changed") {
		t.Fatalf("second init: exit %d\n%s", code, errs)
	}
	if readP(t, path) != before {
		t.Error("second init changed the file")
	}
	if code, _, _ := runP(t, env, "init", "extra"); code != 2 {
		t.Errorf("init extra: exit %d, want 2", code)
	}
}

// TestProfileSet: load-bearing rows are the existing-topic ones (strength and
// reason must survive a plain value change, --rule changes only the strength,
// --reason "" clears) and the flag-position rows (agents type the reason last).
func TestProfileSet(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		args     []string
		wantCode int
		wantOut  string
		inFile   []string
		notFile  []string
	}{
		{name: "missing file", args: []string{"set", "database", "PostgreSQL"}, wantOut: "Set database as a default",
			inFile: []string{"topic = \"database\"", "value = \"PostgreSQL\"", "strength = \"default\""}},
		{name: "new topic", file: sampleProfile, args: []string{"set", "deploy", "a host", "--kind", "web-app"}, wantOut: "Set deploy [web-app] as a default",
			inFile: []string{"value = \"a host\"", "kind = \"web-app\"", "rule_files = "}},
		{name: "existing keeps strength and reason", file: sampleProfile, args: []string{"set", "Database", "MySQL"}, wantOut: "Changed Database as a rule",
			inFile: []string{"value = \"MySQL\"", "strength = \"rule\"", "house rule"}, notFile: []string{"value = \"PostgreSQL\""}},
		{name: "existing --default changes strength", file: sampleProfile, args: []string{"set", "database", "PostgreSQL", "--default"}, wantOut: "as a default",
			inFile: []string{"house rule"}},
		{name: "existing --rule", file: sampleProfile, args: []string{"set", "database", "SQLite", "--kind", "cli", "--rule"}, wantOut: "Changed database [cli] as a rule",
			inFile: []string{"value = \"SQLite\""}},
		{name: "empty reason clears", file: sampleProfile, args: []string{"set", "database", "PostgreSQL", "--reason", ""}, wantOut: "Changed",
			notFile: []string{"house rule"}},
		{name: "flags before", args: []string{"set", "--rule", "--reason", "because", "database", "PostgreSQL"}, wantOut: "as a rule",
			inFile: []string{"because"}},
		{name: "flags between and after", args: []string{"set", "database", "--kind", "cli", "PostgreSQL", "--reason", "because"}, wantOut: "Set database [cli]",
			inFile: []string{"because", "kind = \"cli\""}},
		{name: "comments kept", file: "# my note\n" + sampleProfile, args: []string{"set", "deploy", "x"}, wantOut: "Set deploy",
			inFile: []string{"# my note"}},
		{name: "one word", args: []string{"set", "database"}, wantCode: 2},
		{name: "three words", args: []string{"set", "a", "b", "c"}, wantCode: 2},
		{name: "empty value", args: []string{"set", "a", ""}, wantCode: 2},
		{name: "rule and default", args: []string{"set", "a", "b", "--rule", "--default"}, wantCode: 2},
		{name: "bad kind", args: []string{"set", "a", "b", "--kind", "Web App"}, wantCode: 2},
		{name: "unreadable toml", file: "[[entry\n", args: []string{"set", "a", "b"}, wantCode: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, path := testProfileEnv(t)
			if tc.file != "" {
				putProfile(t, path, tc.file)
			}
			code, out, errs := runP(t, env, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, out, errs)
			}
			if tc.wantCode != 0 {
				if tc.file == "" {
					if _, err := os.Stat(path); err == nil {
						t.Error("a refused set created the file")
					}
				} else if readP(t, path) != tc.file {
					t.Error("a refused set changed the file")
				}
				if tc.wantCode == 2 && !strings.Contains(errs, "afk profile set <topic> <value>") {
					t.Errorf("no usage line:\n%s", errs)
				}
				return
			}
			if !strings.Contains(out, tc.wantOut) || !strings.Contains(out, path) {
				t.Errorf("stdout %q missing %q or the path", out, tc.wantOut)
			}
			got := readP(t, path)
			for _, w := range tc.inFile {
				if !strings.Contains(got, w) {
					t.Errorf("file missing %q:\n%s", w, got)
				}
			}
			for _, w := range tc.notFile {
				if strings.Contains(got, w) {
					t.Errorf("file has %q:\n%s", w, got)
				}
			}
			if _, _, err := profile.Parse([]byte(got)); err != nil {
				t.Errorf("result does not parse: %v", err)
			}
		})
	}
}

// TestProfileSetRefusesSecrets: load-bearing is that the secret text appears
// nowhere in the output and the file is untouched (or still missing), for
// each of the three fields.
func TestProfileSetRefusesSecrets(t *testing.T) {
	tok := "gh" + "p_a1B2a1B2a1B2a1B2a1B2a1B2a1B2a1B2a1B2"
	tests := []struct {
		name  string
		args  []string
		field string
	}{
		{"value", []string{"set", "deploy key", tok}, "value"},
		{"topic", []string{"set", tok, "v"}, "topic"},
		{"reason", []string{"set", "deploy", "v", "--reason", tok}, "reason"},
	}
	for _, tc := range tests {
		for _, existing := range []bool{false, true} {
			name := tc.name + " missing file"
			if existing {
				name = tc.name + " existing file"
			}
			t.Run(name, func(t *testing.T) {
				env, path := testProfileEnv(t)
				if existing {
					putProfile(t, path, sampleProfile)
				}
				code, out, errs := runP(t, env, tc.args...)
				if code != 1 {
					t.Fatalf("exit %d, want 1", code)
				}
				if strings.Contains(out+errs, tok) || strings.Contains(out+errs, tok[:12]) {
					t.Errorf("output holds the secret:\n%s\n%s", out, errs)
				}
				for _, w := range []string{"the " + tc.field, "key or token", "Nothing was saved", "environment"} {
					if !strings.Contains(errs, w) {
						t.Errorf("stderr missing %q:\n%s", w, errs)
					}
				}
				if existing && readP(t, path) != sampleProfile {
					t.Error("file changed")
				}
				if !existing {
					if _, err := os.Stat(path); err == nil {
						t.Error("file created")
					}
				}
			})
		}
	}
}

// TestProfileCheck: the secret row must exit 1 without printing the secret;
// unknown key and missing rule file are warnings and exit 0.
func TestProfileCheck(t *testing.T) {
	tok := "gh" + "p_a1B2a1B2a1B2a1B2a1B2a1B2a1B2a1B2a1B2"
	tests := []struct {
		name     string
		file     string
		args     []string
		wantCode int
		want     []string
		not      []string
	}{
		{name: "clean", file: "[[entry]]\ntopic = \"a\"\nvalue = \"b\"\n", wantCode: 0, want: []string{"The profile is fine: 1 entries, 0 rule files."}},
		{name: "secret", file: "[[entry]]\ntopic = \"a\"\nvalue = \"" + tok + "\"\n", wantCode: 1,
			want: []string{"error:", "[secret]", "1 errors, 0 warnings"}, not: []string{tok}},
		{name: "duplicate", file: "[[entry]]\ntopic = \"a\"\nvalue = \"b\"\n[[entry]]\ntopic = \"A \"\nvalue = \"c\"\n", wantCode: 1,
			want: []string{"[duplicate-entry]"}},
		{name: "unknown key", file: "colour = 1\n[[entry]]\ntopic = \"a\"\nvalue = \"b\"\n", wantCode: 0,
			want: []string{"warning:", "[unknown-key]", "0 errors, 1 warnings"}},
		{name: "missing rule file", file: "rule_files = [\"~/nope.md\"]\n", wantCode: 0,
			want: []string{"warning:", "[missing-rule-file]", "0 errors, 1 warnings"}},
		{name: "present rule file", file: "rule_files = [\"~/there.md\"]\n", wantCode: 0, want: []string{"The profile is fine: 0 entries, 1 rule files."}},
		{name: "no file", wantCode: 0, want: []string{"afk profile init"}},
		{name: "extra word", args: []string{"x"}, wantCode: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, path := testProfileEnv(t)
			home, _ := env.HomeDir()
			if err := os.WriteFile(filepath.Join(home, "there.md"), []byte("rules"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.file != "" {
				putProfile(t, path, tc.file)
			}
			code, out, errs := runP(t, env, append([]string{"check"}, tc.args...)...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, out, errs)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("stdout missing %q:\n%s", w, out)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(out+errs, w) {
					t.Errorf("output has %q", w)
				}
			}
		})
	}
}

func TestProfileRouting(t *testing.T) {
	env, _ := testProfileEnv(t)
	if code, _, errs := runP(t, env, "bogus"); code != 2 || !strings.Contains(errs, `unknown subcommand "bogus"`) {
		t.Errorf("unknown: exit %d\n%s", code, errs)
	}
	for _, h := range []string{"-h", "--help", "help"} {
		if code, out, _ := runP(t, env, h); code != 0 || !strings.Contains(out, "afk profile set <topic> <value>") {
			t.Errorf("%s: exit %d\n%s", h, code, out)
		}
	}
}

// TestProfilePathBesideConfig pins that profile.toml lands in the folder
// config.toml uses, in every environment shape (the two Path functions are
// separate copies of one rule).
func TestProfilePathBesideConfig(t *testing.T) {
	home := t.TempDir()
	tests := []struct {
		name string
		xdg  string
		home func() (string, error)
	}{
		{"default", "", func() (string, error) { return home, nil }},
		{"absolute xdg", t.TempDir(), func() (string, error) { return home, nil }},
		{"relative xdg", "rel/dir", func() (string, error) { return home, nil }},
		{"no home", "", func() (string, error) { return "", os.ErrNotExist }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(string) string { return tc.xdg }
			a := profile.Path(profile.Deps{Getenv: getenv, HomeDir: tc.home})
			b := daemonconfig.Path(daemonconfig.Deps{Getenv: getenv, HomeDir: tc.home})
			if (a == "") != (b == "") {
				t.Fatalf("one path empty: %q vs %q", a, b)
			}
			if a != "" && filepath.Dir(a) != filepath.Dir(b) {
				t.Errorf("profile in %s, config in %s", filepath.Dir(a), filepath.Dir(b))
			}
		})
	}
}
