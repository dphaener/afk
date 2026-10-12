package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
)

const ledgerCmdPlan = `# Demo

#### T1.1 — First

- **Depends on:** none

#### T1.2 — Second

- **Depends on:** T1.1

#### T1.2a — Second, part a

- **Depends on:** T1.1

#### T1.2b — Second, part b

- **Depends on:** T1.1
`

// ledgerCmdProject writes the fixture plan to docs/PLAN.md in a temp project.
// plan "" skips the plan, for the missing-plan row.
func ledgerCmdProject(t *testing.T, plan string) string {
	t.Helper()
	project := t.TempDir()
	if plan != "" {
		p := filepath.Join(project, "docs", "PLAN.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(plan), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

func writeLedgerFile(t *testing.T, project, content string) string {
	t.Helper()
	p := ledger.Path(project)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runLedgerCmd(t *testing.T, project string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var so, se bytes.Buffer
	code = runLedgerSet(append([]string{"--project", project}, args...), &so, &se, project, "", fakeCommonDir(""))
	return code, so.String(), se.String()
}

// TestLedgerSetOutcomes pins the success path and the messages.
//
// Load-bearing rows: "keeps other fields" is the promise that the command
// never erases the daemon's record of a task; "leaving superseded clears
// replacements" because a stale replaced_by would make dependents wait on
// tasks that no longer stand in for it; "same call twice" because agents
// retry.
func TestLedgerSetOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		existing   string
		args       []string
		wantOut    string
		wantInFile []string
		wantNot    []string
	}{
		{name: "set a new task done", args: []string{"T1.1", "done"},
			wantOut: "T1.1: pending → done\n", wantInFile: []string{`"id": "T1.1"`, `"status": "done"`}},
		{name: "flags after the words", args: []string{"T1.1", "running", "--project", "PROJECT"},
			wantOut: "T1.1: pending → running\n", wantInFile: []string{`"status": "running"`}},
		{name: "keeps other fields", existing: `{"tasks":[{"id":"T1.1","status":"running","session":"s1","attempts":2,"started_at":"2026-01-01T00:00:00Z"}]}`,
			args: []string{"T1.1", "failed"}, wantOut: "T1.1: running → failed\n",
			wantInFile: []string{`"status": "failed"`, `"session": "s1"`, `"attempts": 2`, `"started_at": "2026-01-01T00:00:00Z"`}},
		{name: "superseded with replacements", args: []string{"T1.2", "superseded", "--replaced-by", "T1.2a,T1.2b"},
			wantOut: "T1.2: pending → superseded (replaced by T1.2a, T1.2b)\n", wantInFile: []string{`"status": "superseded"`, `"T1.2a"`, `"T1.2b"`}},
		{name: "leaving superseded clears them", existing: `{"tasks":[{"id":"T1.2","status":"superseded","replaced_by":["T1.2a"]}]}`,
			args: []string{"T1.2", "pending"}, wantOut: "T1.2: superseded → pending\n", wantNot: []string{"replaced_by", "T1.2a"}},
		{name: "same call twice", existing: `{"tasks":[{"id":"T1.1","status":"done"}]}`,
			args: []string{"T1.1", "done"}, wantOut: "T1.1 is already done\n", wantInFile: []string{`"status": "done"`}},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := ledgerCmdProject(t, ledgerCmdPlan)
			if tt.existing != "" {
				writeLedgerFile(t, project, tt.existing)
			}
			args := make([]string, len(tt.args))
			for i, a := range tt.args {
				args[i] = strings.ReplaceAll(a, "PROJECT", project)
			}
			var so, se bytes.Buffer
			if !strings.Contains(strings.Join(args, " "), "--project") {
				args = append([]string{"--project", project}, args...)
			}
			if code := runLedgerSet(args, &so, &se, project, "", fakeCommonDir("")); code != 0 {
				t.Fatalf("exit = %d, stderr: %s", code, se.String())
			}
			if so.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", so.String(), tt.wantOut)
			}
			if se.Len() != 0 {
				t.Errorf("stderr = %q, want empty", se.String())
			}
			data, err := os.ReadFile(ledger.Path(project))
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.wantInFile {
				if !strings.Contains(string(data), w) {
					t.Errorf("ledger missing %q:\n%s", w, data)
				}
			}
			for _, w := range tt.wantNot {
				if strings.Contains(string(data), w) {
					t.Errorf("ledger should not contain %q:\n%s", w, data)
				}
			}
		})
	}
}

// TestLedgerSetErrors pins exit codes and advice for every refusal.
//
// Load-bearing rows: "damaged ledger" must exit 1 and leave the file byte for
// byte as it was, or the command would destroy the evidence the person needs;
// "unknown task id" must not write an entry for a typo.
func TestLedgerSetErrors(t *testing.T) {
	const damaged = `{"tasks": [oops`
	cases := []struct {
		name       string
		plan       string
		existing   string
		args       []string
		wantCode   int
		wantErr    []string
		wantNoFile bool
	}{
		{name: "unknown status", plan: ledgerCmdPlan, args: []string{"T1.1", "finished"}, wantCode: 2,
			wantErr: []string{`unknown status "finished"`, "pending, running, shipping, parked, done, failed, superseded"}, wantNoFile: true},
		{name: "one word only", plan: ledgerCmdPlan, args: []string{"T1.1"}, wantCode: 2,
			wantErr: []string{"got 1 word(s)", "Usage: afk ledger set"}, wantNoFile: true},
		{name: "unknown task id", plan: ledgerCmdPlan, args: []string{"T9.9", "done"}, wantCode: 1,
			wantErr: []string{"T9.9 is not a task in", "PLAN.md", `"afk tasks"`}, wantNoFile: true},
		{name: "unknown replacement id", plan: ledgerCmdPlan, args: []string{"T1.2", "superseded", "--replaced-by", "T1.2a,T7"}, wantCode: 1,
			wantErr: []string{"T7 is not a task in", `"afk tasks"`}, wantNoFile: true},
		{name: "replaced-by without superseded", plan: ledgerCmdPlan, args: []string{"T1.2", "done", "--replaced-by", "T1.2a"}, wantCode: 2,
			wantErr: []string{"--replaced-by only goes with the status superseded"}, wantNoFile: true},
		{name: "superseded without replaced-by", plan: ledgerCmdPlan, args: []string{"T1.2", "superseded"}, wantCode: 2,
			wantErr: []string{"needs the tasks that replace it", "--replaced-by"}, wantNoFile: true},
		{name: "replaced by itself", plan: ledgerCmdPlan, args: []string{"T1.2", "superseded", "--replaced-by", "T1.2"}, wantCode: 2,
			wantErr: []string{"cannot be replaced by itself"}, wantNoFile: true},
		{name: "damaged ledger", plan: ledgerCmdPlan, existing: damaged, args: []string{"T1.1", "done"}, wantCode: 1,
			wantErr: []string{"ledger file is damaged", "Fix or delete"}},
		{name: "missing plan", plan: "", args: []string{"T1.1", "done"}, wantCode: 1,
			wantErr: []string{"no master plan at", "afk.toml"}, wantNoFile: true},
		{name: "unknown flag", plan: ledgerCmdPlan, args: []string{"--nope", "T1.1", "done"}, wantCode: 2,
			wantErr: []string{"flag provided but not defined"}, wantNoFile: true},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := ledgerCmdProject(t, tt.plan)
			if tt.existing != "" {
				writeLedgerFile(t, project, tt.existing)
			}
			code, stdout, stderr := runLedgerCmd(t, project, tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, tt.wantCode, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr missing %q:\n%s", w, stderr)
				}
			}
			data, err := os.ReadFile(ledger.Path(project))
			switch {
			case tt.wantNoFile && err == nil:
				t.Errorf("ledger file was created:\n%s", data)
			case tt.existing != "" && string(data) != tt.existing:
				t.Errorf("ledger changed to %q, want it untouched", data)
			}
		})
	}
}

// TestLedgerSetLinkedWorktree pins that a command run from a session's linked
// worktree writes the main checkout's ledger, not a private one in the
// worktree. The plan is only in the main checkout, as for a real repository
// whose worktree has no docs/PLAN.md of its own to rely on.
func TestLedgerSetLinkedWorktree(t *testing.T) {
	main := ledgerCmdProject(t, ledgerCmdPlan)
	worktree := t.TempDir()
	var so, se bytes.Buffer
	code := runLedgerSet([]string{"T1.1", "done"}, &so, &se, worktree, "", fakeCommonDir(filepath.Join(main, ".git")))
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, se.String())
	}
	if _, err := os.Stat(ledger.Path(worktree)); err == nil {
		t.Errorf("ledger written inside the worktree")
	}
	data, err := os.ReadFile(filepath.Join(main, ".afk", "build", "ledger.json"))
	if err != nil {
		t.Fatalf("main checkout has no ledger: %v", err)
	}
	if !strings.Contains(string(data), `"status": "done"`) {
		t.Errorf("main ledger = %s", data)
	}
}

// TestLedgerSetHelp pins that -h exits 0 and prints usage.
func TestLedgerSetHelp(t *testing.T) {
	var so, se bytes.Buffer
	if code := runLedgerSet([]string{"-h"}, &so, &se, t.TempDir(), "", fakeCommonDir("")); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(se.String(), ledgerSetUsageLine) {
		t.Errorf("usage missing:\n%s", se.String())
	}
}

func runLedgerInitCmd(project string, args ...string) (code int, stdout, stderr string) {
	var so, se bytes.Buffer
	code = runLedgerInit(append([]string{"--project", project}, args...), &so, &se, project, "", fakeCommonDir(""))
	return code, so.String(), se.String()
}

// TestLedgerInit pins what init writes and refuses.
//
// Load-bearing rows: "unknown id in done-through" and "unknown id in done"
// must leave no file, because an empty ledger says "nothing is built";
// "existing ledger" must leave the file byte for byte as it was, since init
// never merges.
func TestLedgerInit(t *testing.T) {
	const existing = `{"tasks":[{"id":"T1.1","status":"running"}]}`
	cases := []struct {
		name     string
		existing string
		args     []string
		wantCode int
		wantOut  string
		wantErr  []string
		wantDone []string
		wantFile bool
	}{
		{name: "done-through", args: []string{"--done-through", "T1.2"}, wantOut: "2 task(s) marked done", wantDone: []string{"T1.1", "T1.2"}, wantFile: true},
		{name: "done list", args: []string{"--done", "T1.1,T1.2b"}, wantOut: "2 task(s) marked done", wantDone: []string{"T1.1", "T1.2b"}, wantFile: true},
		{name: "both combine", args: []string{"--done-through", "T1.1", "--done", "T1.2b,T1.1"}, wantOut: "2 task(s) marked done", wantDone: []string{"T1.1", "T1.2b"}, wantFile: true},
		{name: "neither makes an empty ledger", wantOut: "0 task(s) marked done", wantFile: true},
		{name: "unknown id in done-through", args: []string{"--done-through", "T9.9"}, wantCode: 1, wantErr: []string{"T9.9 is not a task in", "Nothing was written"}},
		{name: "unknown id in done", args: []string{"--done", "T1.1,T9.9"}, wantCode: 1, wantErr: []string{"T9.9 is not a task in", "Nothing was written"}},
		{name: "existing ledger", existing: existing, args: []string{"--done-through", "T1.2"}, wantCode: 1, wantErr: []string{"already exists", "ledger.json", `"afk ledger set"`}, wantFile: true},
		{name: "positional word", args: []string{"T1.1"}, wantCode: 2, wantErr: []string{"unexpected word", "Usage: afk ledger init"}},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := ledgerCmdProject(t, ledgerCmdPlan)
			if tt.existing != "" {
				writeLedgerFile(t, project, tt.existing)
			}
			code, stdout, stderr := runLedgerInitCmd(project, tt.args...)
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, tt.wantCode, stderr)
			}
			if !strings.Contains(stdout, tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, tt.wantOut)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr missing %q:\n%s", w, stderr)
				}
			}
			data, err := os.ReadFile(ledger.Path(project))
			if (err == nil) != tt.wantFile {
				t.Fatalf("ledger file exists = %v, want %v", err == nil, tt.wantFile)
			}
			if tt.existing != "" && string(data) != tt.existing {
				t.Errorf("ledger changed to %q, want it untouched", data)
			}
			if tt.existing == "" && tt.wantFile {
				l, err := ledger.Load(project)
				if err != nil {
					t.Fatal(err)
				}
				if len(l.Tasks) != len(tt.wantDone) {
					t.Fatalf("tasks = %v, want %v", l.Tasks, tt.wantDone)
				}
				for _, id := range tt.wantDone {
					if l.Status(id) != ledger.Done {
						t.Errorf("%s = %s, want done", id, l.Status(id))
					}
				}
			}
		})
	}
}

// TestLedgerInitLinkedWorktree pins that init from a linked worktree creates
// the main checkout's ledger, like set.
func TestLedgerInitLinkedWorktree(t *testing.T) {
	main := ledgerCmdProject(t, ledgerCmdPlan)
	worktree := t.TempDir()
	var so, se bytes.Buffer
	code := runLedgerInit([]string{"--done", "T1.1"}, &so, &se, worktree, "", fakeCommonDir(filepath.Join(main, ".git")))
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, se.String())
	}
	if _, err := os.Stat(ledger.Path(worktree)); err == nil {
		t.Errorf("ledger written inside the worktree")
	}
	if !ledger.Exists(main) {
		t.Errorf("main checkout has no ledger")
	}
}

// TestLedgerSetSeveralIds pins the multi-id form.
//
// Load-bearing rows: "one unknown among three" must write nothing, or a typo
// would half-apply a batch; "replaced-by with two ids" is a usage error
// because one replacement list cannot belong to two tasks.
func TestLedgerSetSeveralIds(t *testing.T) {
	t.Run("three ids", func(t *testing.T) {
		project := ledgerCmdProject(t, ledgerCmdPlan)
		code, stdout, stderr := runLedgerCmd(t, project, "T1.1", "T1.2", "T1.2a", "done")
		if code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, stderr)
		}
		want := "T1.1: pending → done\nT1.2: pending → done\nT1.2a: pending → done\n"
		if stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		l, _ := ledger.Load(project)
		for _, id := range []string{"T1.1", "T1.2", "T1.2a"} {
			if l.Status(id) != ledger.Done {
				t.Errorf("%s = %s, want done", id, l.Status(id))
			}
		}
	})
	t.Run("one unknown among three", func(t *testing.T) {
		project := ledgerCmdProject(t, ledgerCmdPlan)
		code, stdout, stderr := runLedgerCmd(t, project, "T1.1", "T9.9", "T1.2", "done")
		if code != 1 || stdout != "" || !strings.Contains(stderr, "T9.9 is not a task in") {
			t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if ledger.Exists(project) {
			t.Errorf("ledger was created")
		}
	})
	t.Run("replaced-by with two ids", func(t *testing.T) {
		project := ledgerCmdProject(t, ledgerCmdPlan)
		code, stdout, stderr := runLedgerCmd(t, project, "T1.2", "T1.1", "superseded", "--replaced-by", "T1.2a")
		if code != 2 || stdout != "" || !strings.Contains(stderr, "--replaced-by") || !strings.Contains(stderr, "2 task ids") {
			t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if ledger.Exists(project) {
			t.Errorf("ledger was created")
		}
	})
}
