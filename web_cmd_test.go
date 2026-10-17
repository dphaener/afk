package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/webpass"
)

// testWebEnv returns an env with a temp state home, a fake terminal check and
// a reader that returns the queued answers, plus a counter of reads.
func testWebEnv(t *testing.T, terminal bool, answers ...string) (webEnv, *int) {
	t.Helper()
	home := t.TempDir()
	reads := 0
	env := webEnv{
		state: usagestate.Deps{
			Getenv:  func(k string) string { return map[string]string{"XDG_STATE_HOME": home}[k] },
			HomeDir: func() (string, error) { return home, nil },
		},
		isTerminal: func() bool { return terminal },
		readPassword: func() ([]byte, error) {
			reads++
			if len(answers) == 0 {
				return nil, errors.New("no more answers")
			}
			a := answers[0]
			answers = answers[1:]
			return []byte(a), nil
		},
		rand:       bytes.NewReader(bytes.Repeat([]byte{7}, 1024)),
		iterations: 1000,
	}
	return env, &reads
}

// TestRunWeb: "not a terminal" is load-bearing (no prompt, no read, no file:
// a piped password would be echoed into logs); "mismatch", "too short" and
// "empty" each must save nothing; "saved" pins mode 0600, no plaintext in
// the file and a working Verify; "saved again" pins that a second run
// replaces the record so old browsers are signed out.
func TestRunWeb(t *testing.T) {
	const pw = "correct-horse"
	tests := []struct {
		name      string
		terminal  bool
		answers   []string
		wantCode  int
		wantErr   string
		wantFile  bool
		wantReads int
	}{
		{"not a terminal", false, []string{pw, pw}, 1, "must be run in a terminal", false, 0},
		{"mismatch", true, []string{pw, "something-else"}, 1, "did not match", false, 2},
		{"too short", true, []string{"short", "short"}, 1, "at least 8 characters", false, 2},
		{"empty", true, []string{"", ""}, 1, "at least 8 characters", false, 2},
		{"read fails", true, []string{pw}, 1, "Could not read the password", false, 2},
		{"saved", true, []string{pw, pw}, 0, "New password: ", true, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, reads := testWebEnv(t, tt.terminal, tt.answers...)
			var out, errb bytes.Buffer
			code := runWeb([]string{"password"}, &out, &errb, env)
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tt.wantCode, errb.String())
			}
			if !strings.Contains(errb.String(), tt.wantErr) {
				t.Errorf("stderr %q lacks %q", errb.String(), tt.wantErr)
			}
			if *reads != tt.wantReads {
				t.Errorf("reads = %d, want %d", *reads, tt.wantReads)
			}
			if !tt.terminal && errb.Len() > 0 && strings.Contains(errb.String(), "New password") {
				t.Error("prompted although stdin is not a terminal")
			}
			if strings.Contains(out.String()+errb.String(), pw) {
				t.Error("the password appeared in output")
			}
			path, err := webpass.Path(env.state)
			if err != nil {
				t.Fatal(err)
			}
			st, statErr := os.Stat(path)
			if (statErr == nil) != tt.wantFile {
				t.Fatalf("file exists = %v, want %v", statErr == nil, tt.wantFile)
			}
			if !tt.wantFile {
				return
			}
			if st.Mode().Perm() != 0o600 {
				t.Errorf("mode = %o, want 600", st.Mode().Perm())
			}
			b, _ := os.ReadFile(path)
			if bytes.Contains(b, []byte(pw)) {
				t.Error("file holds the password")
			}
			rec, err := webpass.Read(env.state)
			if err != nil || !rec.Verify(pw) {
				t.Errorf("saved record does not verify: %v", err)
			}
			for _, want := range []string{"saved", "signed out", "restart"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("stdout %q lacks %q", out.String(), want)
				}
			}
		})
	}

	t.Run("saved again replaces the record", func(t *testing.T) {
		env, _ := testWebEnv(t, true, pw, pw)
		var out, errb bytes.Buffer
		if code := runWeb([]string{"password"}, &out, &errb, env); code != 0 {
			t.Fatalf("first run exit %d: %s", code, errb.String())
		}
		first, _ := webpass.Read(env.state)
		answers := []string{"another-secret", "another-secret"}
		env.readPassword = func() ([]byte, error) {
			a := answers[0]
			answers = answers[1:]
			return []byte(a), nil
		}
		if code := runWeb([]string{"password"}, &out, &errb, env); code != 0 {
			t.Fatalf("second run exit %d: %s", code, errb.String())
		}
		second, _ := webpass.Read(env.state)
		if first.CookieValue() == second.CookieValue() {
			t.Error("cookie value did not change")
		}
		if second.Verify(pw) || !second.Verify("another-secret") {
			t.Error("record does not hold the new password")
		}
	})

	t.Run("save fails", func(t *testing.T) {
		env, _ := testWebEnv(t, true, pw, pw)
		blocker := t.TempDir() + "/file"
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		env.state.Getenv = func(k string) string { return map[string]string{"XDG_STATE_HOME": blocker}[k] }
		var out, errb bytes.Buffer
		if code := runWeb([]string{"password"}, &out, &errb, env); code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(errb.String(), "Could not save") || strings.Contains(errb.String(), pw) {
			t.Errorf("stderr = %q", errb.String())
		}
	})

	for _, tt := range []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"help", []string{"--help"}, 0, "Usage:"},
		{"help word", []string{"help"}, 0, "Usage:"},
		{"no subcommand", nil, 2, "Usage:"},
		{"unknown subcommand", []string{"nope"}, 2, "unknown command"},
		{"extra argument", []string{"password", "x"}, 2, "takes no arguments"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, reads := testWebEnv(t, true)
			var out, errb bytes.Buffer
			code := runWeb(tt.args, &out, &errb, env)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(out.String()+errb.String(), tt.wantOut) {
				t.Errorf("output %q lacks %q", out.String()+errb.String(), tt.wantOut)
			}
			if *reads != 0 {
				t.Error("prompted")
			}
		})
	}
}
