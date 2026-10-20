package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const planCmdFixture = `# Demo

## Waves

### Wave 1: Only

- [ ] (W1.T1) First task

  **Model**: light

- [ ] (W1.T2) Second task

  **Model**: light
`

// planCmdProject writes the fixture as session "demo" under a temp project.
func planCmdProject(t *testing.T) (project, planPath string) {
	t.Helper()
	project = t.TempDir()
	planPath = filepath.Join(project, ".afk", "plans", "demo.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte(planCmdFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return project, planPath
}

func planCmdNow() time.Time { return time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC) }

// TestRunPlanCheckUsageErrors pins exit code 2 for every command line that
// could never work, and that none of them touches the plan.
//
// Load-bearing rows: "failed without a reason" and "failed with a blank
// reason" are the ones a caller actually hits, and writing the line anyway
// would produce a `[!]` with no reason, which the plan parser rejects for the
// whole file. "failed without a reason on an already failed task" is not
// here on purpose: usage is checked before the plan is read, so it is exit 2
// like the others, and TestRunPlanCheckOutcomes covers the repeat with a
// reason.
func TestRunPlanCheckUsageErrors(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no arguments", args: nil, wantErr: "got 0 word(s)"},
		{name: "two words", args: []string{"demo", "W1.T1"}, wantErr: "got 2 word(s)"},
		{name: "four words", args: []string{"demo", "W1.T1", "done", "extra"}, wantErr: "got 4 word(s)"},
		{name: "unknown state", args: []string{"demo", "W1.T1", "passed"}, wantErr: "must be done or failed"},
		{name: "bad task id", args: []string{"demo", "task-one", "done"}, wantErr: "is not a task id"},
		{name: "bad session name", args: []string{"../demo", "W1.T1", "done"}, wantErr: "is not a session name"},
		{name: "failed without a reason", args: []string{"demo", "W1.T1", "failed"}, wantErr: "needs a reason"},
		{name: "failed with a blank reason", args: []string{"--reason", " \n ", "demo", "W1.T1", "failed"}, wantErr: "needs a reason"},
		{name: "unknown flag", args: []string{"--nope", "demo", "W1.T1", "done"}, wantErr: "flag provided but not defined"},
		{name: "reason flag with no value", args: []string{"demo", "W1.T1", "failed", "--reason"}, wantErr: "flag needs an argument"},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project, planPath := planCmdProject(t)
			var stdout, stderr bytes.Buffer
			args := append([]string{"--project", project}, tt.args...)
			if code := runPlanCheck(args, &stdout, &stderr, project, "", planCmdNow); code != 2 {
				t.Errorf("exit = %d, want 2 (stderr: %s)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, stderr.String())
			}
			if !strings.Contains(stderr.String(), "afk plan check") {
				t.Errorf("stderr does not show the usage:\n%s", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if got, _ := os.ReadFile(planPath); string(got) != planCmdFixture {
				t.Error("a usage error changed the plan")
			}
		})
	}
}

// TestRunPlanCheckOutcomes drives the command end to end against a temp
// project: exit codes 0 and 1 of contract C1, the messages, and the file.
//
// Load-bearing rows: "flags after the words" (Go's flag package stops at the
// first word, so without the parse loop --reason there is a fourth word and
// the natural way to type the command fails), "repeat failed keeps the first
// reason" (exit 0, line untouched), and "missing plan" / "unknown task"
// (exit 1, not 2: the command was typed correctly and the caller needs to
// tell that apart from a typo in the command itself).
func TestRunPlanCheckOutcomes(t *testing.T) {
	type step struct {
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}
	cases := []struct {
		name     string
		steps    []step
		wantLine string // a line the plan must contain afterwards
		wantSame bool   // the plan must be byte-identical to the fixture
	}{
		{name: "done", steps: []step{
			{args: []string{"demo", "W1.T1", "done"}, wantStdout: "Marked W1.T1 done"},
		}, wantLine: "- [x] (W1.T1) First task"},
		{name: "done twice", steps: []step{
			{args: []string{"demo", "(W1.T1)", "done"}, wantStdout: "Marked W1.T1 done"},
			{args: []string{"demo", "(W1.T1)", "done"}, wantStdout: "already marked done"},
		}, wantLine: "- [x] (W1.T1) First task"},
		{name: "flags after the words", steps: []step{
			{args: []string{"demo", "W1.T2", "failed", "--reason", "tests fail"}, wantStdout: "Marked W1.T2 failed"},
		}, wantLine: "- [!] (W1.T2) Second task FAILED: tests fail"},
		{name: "flag between the words", steps: []step{
			{args: []string{"demo", "--reason=tests fail", "W1.T2", "failed"}, wantStdout: "Marked W1.T2 failed"},
		}, wantLine: "- [!] (W1.T2) Second task FAILED: tests fail"},
		{name: "repeat failed keeps the first reason", steps: []step{
			{args: []string{"--reason", "first", "demo", "W1.T2", "failed"}, wantStdout: "Marked W1.T2 failed"},
			{args: []string{"--reason", "second", "demo", "W1.T2", "failed"}, wantStdout: "first reason is kept"},
		}, wantLine: "- [!] (W1.T2) Second task FAILED: first"},
		{name: "done over failed", steps: []step{
			{args: []string{"--reason", "first", "demo", "W1.T2", "failed"}, wantStdout: "Marked W1.T2 failed"},
			{args: []string{"demo", "W1.T2", "done"}, wantStdout: "Marked W1.T2 done"},
		}, wantLine: "- [x] (W1.T2) Second task"},
		{name: "reason ignored for done", steps: []step{
			{args: []string{"--reason", "not used", "demo", "W1.T1", "done"}, wantStdout: "Marked W1.T1 done"},
		}, wantLine: "- [x] (W1.T1) First task"},
		{name: "missing plan", steps: []step{
			{args: []string{"other", "W1.T1", "done"}, wantCode: 1, wantStderr: "there is no plan at"},
		}, wantSame: true},
		{name: "unknown task", steps: []step{
			{args: []string{"demo", "W7.T7", "done"}, wantCode: 1, wantStderr: "has no task W7.T7"},
		}, wantSame: true},
		{name: "help", steps: []step{
			{args: []string{"--help"}, wantStderr: "afk plan check [--project DIR]"},
		}, wantSame: true},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project, planPath := planCmdProject(t)
			for i, s := range tt.steps {
				var stdout, stderr bytes.Buffer
				args := append([]string{"--project", project}, s.args...)
				// cwd is a directory with no plan, so a pass proves
				// --project was honoured and not the working directory.
				code := runPlanCheck(args, &stdout, &stderr, t.TempDir(), "", planCmdNow)
				if code != s.wantCode {
					t.Fatalf("step %d: exit = %d, want %d (stderr: %s)", i, code, s.wantCode, stderr.String())
				}
				if s.wantStdout != "" && !strings.Contains(stdout.String(), s.wantStdout) {
					t.Errorf("step %d: stdout missing %q: %s", i, s.wantStdout, stdout.String())
				}
				if s.wantStderr != "" && !strings.Contains(stderr.String(), s.wantStderr) {
					t.Errorf("step %d: stderr missing %q: %s", i, s.wantStderr, stderr.String())
				}
				if s.wantCode != 0 && stdout.Len() != 0 {
					t.Errorf("step %d: stdout = %q on a failure, want empty", i, stdout.String())
				}
			}
			got, err := os.ReadFile(planPath)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantSame && string(got) != planCmdFixture {
				t.Errorf("plan changed:\n%s", got)
			}
			if tt.wantLine != "" {
				found := false
				for _, line := range strings.Split(string(got), "\n") {
					if line == tt.wantLine {
						found = true
					}
				}
				if !found {
					t.Errorf("plan has no line %q:\n%s", tt.wantLine, got)
				}
				if len(strings.Split(string(got), "\n")) != len(strings.Split(planCmdFixture, "\n")) {
					t.Error("line count changed")
				}
			}
		})
	}
}

// TestRunPlanCheckUsesCwdWithoutProject pins the fallback: with no --project
// the plan is looked for under the working directory passed in, which is how
// an agent running inside the session's worktree calls it.
func TestRunPlanCheckUsesCwdWithoutProject(t *testing.T) {
	project, planPath := planCmdProject(t)
	var stdout, stderr bytes.Buffer
	if code := runPlanCheck([]string{"demo", "W1.T1", "failed", "--reason", "boom"}, &stdout, &stderr, project, "", planCmdNow); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	got, _ := os.ReadFile(planPath)
	if !strings.Contains(string(got), "- [!] (W1.T1) First task FAILED: boom\n") {
		t.Errorf("plan not marked:\n%s", got)
	}
	issues, err := os.ReadFile(filepath.Join(project, ".afk", "demo", "issues.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "## (W1.T1) FAILED\n\n2026-10-10T09:00:00Z: boom\n"; string(issues) != want {
		t.Errorf("issues.md = %q, want %q", issues, want)
	}
}

// TestPlanUsageNamesTheCommand keeps the usage text and the dispatcher in
// step: the help must show every form routePlanArgs accepts (the planning
// session with its flags, the three commands that change a plan, status,
// advance, approve, patch, insert, depend, next-id, lint and check), and for check both
// states and all three exit codes.
func TestPlanUsageNamesTheCommand(t *testing.T) {
	var b bytes.Buffer
	printPlanUsage(&b)
	for _, want := range []string{
		planSessionUsageLine, planStatusUsageLine, planAdvanceUsageLine, "--dry-run",
		planApproveUsageLine, "approve is your \"go\"", "creates the task ledger", "Running it\nagain is safe", "--provider NAME", "needs a terminal",
		planExtendUsageLine, planAmendUsageLine, planReconcileUsageLine, planFixUsageLine, "Fixes milestone", "--yes", ".afk/plan/archive/",
		planPatchUsageLine, planInsertUsageLine, planDependUsageLine, planNextIDUsageLine,
		"--new-milestone", "--message TEXT", "Nothing was changed.", "afk-plan-changes",
		"afk plan lint", "afk plan check", "--project", "--reason", "done|failed", "Exit codes: 0", "1 the plan", "2 the command",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("plan usage missing %q:\n%s", want, b.String())
		}
	}
}
