package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/tokens"
)

const (
	// tokensFixtureSID is the session id of the trimmed real transcript in
	// internal/provider/testdata/claude-home.
	tokensFixtureSID  = "66065044-0f91-4d8d-9972-5f1cd1878494"
	tokensFixtureHome = "internal/provider/testdata/claude-home"
)

func tokensNow() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }

// fakeTokensEnv serves one session (or none) and a fixed main checkout, so no
// test touches git or a real session.
func fakeTokensEnv(main string, s *tokensSession) tokensEnv {
	return tokensEnv{
		MainCheckout: func(string) (string, error) { return main, nil },
		Session: func(_, name string) (tokensSession, bool, error) {
			if s == nil || name != "demo" {
				return tokensSession{}, false, nil
			}
			return *s, true, nil
		},
	}
}

// TestChooseTokensForm pins which reading each argument list gets.
//
// Load-bearing rows: "task id with a letter suffix" and "lower-case task id"
// (both are real task ids and must not fall through to the session branch,
// where a dot is an error), and "session name that starts like a task" (t1-4
// is the real session name of the task T1.4 and must stay a session).
func TestChooseTokensForm(t *testing.T) {
	cases := []struct {
		name     string
		words    []string
		record   bool
		wantForm tokensForm
		wantArg  string
		wantErr  bool
	}{
		{name: "no argument", wantForm: tokensFormTotals},
		{name: "task id", words: []string{"T1.4"}, wantForm: tokensFormTask, wantArg: "T1.4"},
		{name: "task id with a letter suffix", words: []string{"T0.4b"}, wantForm: tokensFormTask, wantArg: "T0.4b"},
		{name: "lower-case task id", words: []string{"t1.4"}, wantForm: tokensFormTask, wantArg: "t1.4"},
		{name: "session name", words: []string{"my-session"}, wantForm: tokensFormSession, wantArg: "my-session"},
		{name: "session name that starts like a task", words: []string{"t1-4"}, wantForm: tokensFormSession, wantArg: "t1-4"},
		{name: "wave task id is not a task id here", words: []string{"W1.T1"}, wantForm: tokensFormSession, wantArg: "W1.T1"},
		{name: "record with a session", words: []string{"demo"}, record: true, wantForm: tokensFormSession, wantArg: "demo"},
		{name: "record with nothing", record: true, wantErr: true},
		{name: "record with a task id", words: []string{"T1.4"}, record: true, wantErr: true},
		{name: "two words", words: []string{"a", "b"}, wantErr: true},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			form, arg, err := chooseTokensForm(tt.words, tt.record)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (form != tt.wantForm || arg != tt.wantArg) {
				t.Errorf("got (%v, %q), want (%v, %q)", form, arg, tt.wantForm, tt.wantArg)
			}
		})
	}
}

// TestRunTokensUsageErrors pins exit 2 and the usage line, and that help is
// exit 0.
func TestRunTokensUsageErrors(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "help", args: []string{"--help"}, wantCode: 0, wantErr: "Usage:"},
		{name: "short help", args: []string{"-h"}, wantCode: 0, wantErr: "Usage:"},
		{name: "record alone", args: []string{"--record"}, wantCode: 2, wantErr: "--record needs a session name"},
		{name: "two words", args: []string{"a", "b"}, wantCode: 2, wantErr: "at most one"},
		{name: "bad session name", args: []string{"Bad_Name"}, wantCode: 2, wantErr: "is not a session name or a task id"},
		{name: "record with a task", args: []string{"--record", "T1.4"}, wantCode: 2, wantErr: "not a task id"},
		{name: "unknown flag", args: []string{"--nope"}, wantCode: 2, wantErr: "flag provided but not defined"},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := t.TempDir()
			var stdout, stderr bytes.Buffer
			code := runTokens(tt.args, &stdout, &stderr, project, "", tokensNow, fakeTokensEnv(project, nil))
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, stderr.String())
			}
			if _, err := os.Stat(filepath.Join(project, ".afk")); err == nil {
				t.Errorf("a usage error wrote under .afk/")
			}
		})
	}
}

// TestRunTokensFailures pins exit 1 and a plain message for each way a read
// can fail, and that none of them writes the ledger.
//
// Load-bearing rows: "session not found" must say that a cleaned session is
// read by task id, and the two ledger rows must say how to record.
func TestRunTokensFailures(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		session *tokensSession
		home    string
		wantErr string
	}{
		{name: "session not found", args: []string{"demo"}, wantErr: "by its task id"},
		{name: "no session id yet", args: []string{"demo"}, session: &tokensSession{}, wantErr: "has not started"},
		{name: "no transcript", args: []string{"demo"}, session: &tokensSession{SessionID: "nope"}, home: tokensFixtureHome, wantErr: "no transcript found"},
		{name: "unsupported agent cli", args: []string{"demo"}, session: &tokensSession{SessionID: "x", Provider: "codex"}, home: tokensFixtureHome, wantErr: "cannot be read for this session's agent CLI yet"},
		{name: "empty ledger", args: nil, wantErr: "afk tokens --record SESSION"},
		{name: "task with no lines", args: []string{"T9.9"}, wantErr: "nothing for T9.9"},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := t.TempDir()
			if tt.session != nil {
				tt.session.MainCheckout = project
				tt.session.Worktree = project
			}
			var stdout, stderr bytes.Buffer
			code := runTokens(tt.args, &stdout, &stderr, project, tt.home, tokensNow, fakeTokensEnv(project, tt.session))
			if code != 1 {
				t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, stderr.String())
			}
			if _, err := os.Stat(filepath.Join(project, ".afk")); err == nil {
				t.Errorf("a failed read wrote under .afk/")
			}
		})
	}
}

// TestRunTokensFixture reads the trimmed real transcript, checks the printed
// table against the numbers in internal/provider/testdata/README.md, records
// twice, and reads the same totals back by task id.
//
// The expected rows are literals. They were computed by hand with jq (README
// table (c)), never by this repository's Go code, so a bug in the reader or
// the aggregation cannot hide by being on both sides. Weighted is the README's
// value rounded to a whole number, as the table prints it. The load-bearing
// rows are the TOTAL row (143 calls, the four sums, weighted, 100%) and the
// split rows (conductor in three phases, task on two models).
func TestRunTokensFixture(t *testing.T) {
	project := t.TempDir()
	sessDir := filepath.Join(project, ".afk", "demo")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "description.md"), []byte("Ship T0.4 detach fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := fakeTokensEnv(project, &tokensSession{Worktree: project, MainCheckout: project, SessionID: tokensFixtureSID})

	// phase, role, model, calls, input, output, cache write, cache read, weighted, share
	want := [][]string{
		{"execute", "conductor", "claude-opus-5-5", "6", "14", "4496", "16195", "1196648", "162403", "5.7%"},
		{"execute", "task", "claude-opus-5-5", "44", "88", "62039", "274846", "2796297", "933470", "32.5%"},
		{"execute", "task", "claude-sonnet-5-5", "17", "34", "9323", "86739", "717783", "226851", "7.9%"},
		{"execute", "verifier", "claude-sonnet-5-5", "18", "38", "3782", "104781", "679825", "217907", "7.6%"},
		{"plan", "planner", "claude-opus-5-5", "12", "26", "24006", "164921", "1788598", "505067", "17.6%"},
		{"plan", "research", "claude-sonnet-5-5", "7", "14", "8114", "78599", "330015", "171834", "6.0%"},
		{"review", "conductor", "claude-opus-5-5", "2", "4", "723", "8023", "428189", "56467", "2.0%"},
		{"review", "review:compliance", "claude-sonnet-5-5", "4", "8", "1554", "57300", "139017", "93305", "3.2%"},
		{"review", "review:qa", "claude-sonnet-5-5", "9", "18", "2417", "41538", "438831", "107909", "3.8%"},
		{"review", "review:quality", "claude-sonnet-5-5", "3", "6", "890", "24209", "93606", "44078", "1.5%"},
		{"ship", "conductor", "claude-opus-5-5", "21", "48", "10387", "48352", "2395261", "351949", "12.3%"},
		{"total", "143", "298", "127731", "905503", "11004070", "2871239", "100.0%"},
	}

	run := func(args ...string) (string, string, int) {
		var stdout, stderr bytes.Buffer
		code := runTokens(args, &stdout, &stderr, project, tokensFixtureHome, tokensNow, env)
		return stdout.String(), stderr.String(), code
	}
	ledger := tokens.LedgerPath(project)

	out, errOut, code := run("demo")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "Session demo, task T0.4, 143 calls") {
		t.Errorf("header missing:\n%s", out)
	}
	rows := map[string][]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && f[1] != "(subtotal)" {
			rows[f[0]+"/"+f[1]+"/"+f[2]] = f
		}
	}
	for _, w := range want {
		key := w[0] + "/" + w[1] + "/" + w[2]
		if w[0] == "total" {
			key = "total/143/298"
		}
		got := rows[key]
		if strings.Join(got, " ") != strings.Join(w, " ") {
			t.Errorf("row %s:\n got  %v\n want %v", key, got, w)
		}
	}
	if _, err := os.Stat(ledger); err == nil {
		t.Fatalf("reading without --record wrote the ledger")
	}

	out, errOut, code = run("--record", "demo")
	if code != 0 {
		t.Fatalf("record: exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "Recorded 11 lines") {
		t.Errorf("first record did not append:\n%s", out)
	}
	first, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(first), "\n"); n != 11 {
		t.Errorf("ledger has %d lines, want 11", n)
	}

	out, errOut, code = run("demo", "--record")
	if code != 0 {
		t.Fatalf("second record: exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "already recorded") {
		t.Errorf("second record did not say already recorded:\n%s", out)
	}
	second, _ := os.ReadFile(ledger)
	if !bytes.Equal(first, second) {
		t.Errorf("second record changed the ledger")
	}

	taskOut, errOut, code := run("T0.4")
	if code != 0 {
		t.Fatalf("task: exit = %d, stderr: %s", code, errOut)
	}
	for _, s := range []string{"143", "298", "127731", "905503", "11004070", "2871239", "100.0%"} {
		if !strings.Contains(taskOut, s) {
			t.Errorf("task output missing %q:\n%s", s, taskOut)
		}
	}

	totals, errOut, code := run()
	if code != 0 {
		t.Fatalf("totals: exit = %d, stderr: %s", code, errOut)
	}
	if f := strings.Fields(totals); !strings.Contains(totals, "T0.4") || !strings.Contains(strings.Join(f, " "), "T0.4 1 143 2871239 100.0%") {
		t.Errorf("totals row wrong:\n%s", totals)
	}
}

// TestReadTaskID pins the missing-file and size-cap behaviour.
//
// Load-bearing row: "id past the cap" — a task id beyond 10 KB is not found,
// which proves the read is bounded.
func TestReadTaskID(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name, path, want string
	}{
		{name: "missing file", path: filepath.Join(dir, "none.md"), want: ""},
		{name: "id in the text", path: write("a.md", "Do T2.3b now"), want: "T2.3b"},
		{name: "no id", path: write("b.md", "nothing here"), want: ""},
		{name: "id past the cap", path: write("c.md", strings.Repeat("x", descriptionReadCap)+" T1.1"), want: ""},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			if got := readTaskID(tt.path); got != tt.want {
				t.Errorf("readTaskID = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRunTokensEnvError makes sure a failing lookup is reported, not hidden.
func TestRunTokensEnvError(t *testing.T) {
	project := t.TempDir()
	env := tokensEnv{
		MainCheckout: func(string) (string, error) { return "", errors.New("boom") },
		Session:      func(_, _ string) (tokensSession, bool, error) { return tokensSession{}, false, errors.New("boom") },
	}
	for _, args := range [][]string{{"demo"}, {}} {
		var stdout, stderr bytes.Buffer
		if code := runTokens(args, &stdout, &stderr, project, "", tokensNow, env); code != 1 || !strings.Contains(stderr.String(), "boom") {
			t.Errorf("args %v: exit %d, stderr %q", args, code, stderr.String())
		}
	}
}
