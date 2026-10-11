package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
)

// seedLedger records statuses through ledger.Update, the way the real writers
// do. Each entry is "id=status" or "id=superseded:A,B".
func seedLedger(t *testing.T, project string, entries ...string) {
	t.Helper()
	err := ledger.Update(project, func(l *ledger.Ledger) error {
		for _, e := range entries {
			id, rest, _ := strings.Cut(e, "=")
			status, repl, _ := strings.Cut(rest, ":")
			st, err := ledger.ParseStatus(status)
			if err != nil {
				return err
			}
			var rb []string
			if repl != "" {
				rb = strings.Split(repl, ",")
			}
			if _, err := l.Set(id, st, rb); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func runTasksCmd(t *testing.T, project string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var so, se bytes.Buffer
	code = runTasks(append([]string{"--project", project}, args...), &so, &se, project, "", fakeCommonDir(""))
	return code, so.String(), se.String()
}

// TestTasksTable pins the whole table and the notes after it.
//
// Load-bearing rows: "mixed ledger" compares the entire stdout, so column
// order, the "-" for done and superseded tasks, the "pending" default and the
// "superseded (by ...)" cell cannot drift; "note for a task naming no ids"
// and "ledger entry outside the plan" are the two owner-facing warnings;
// "afk.toml points at another plan" proves the table reads the configured
// plan, not docs/PLAN.md.
func TestTasksTable(t *testing.T) {
	const noteCmdPlan = `# Demo

#### T1.1 — First

- **Depends on:** none

#### T1.2 — Second

- **Depends on:** as available
`
	tests := []struct {
		name   string
		plan   string
		toml   string
		planAt string
		ledger []string
		want   string
	}{
		{
			name:   "mixed ledger",
			plan:   ledgerCmdPlan,
			ledger: []string{"T1.1=done", "T1.2=superseded:T1.2a,T1.2b", "T1.2a=running"},
			want: "ID     TITLE           STATUS                        UNMET DEPENDENCIES\n" +
				"T1.1   First           done                          -\n" +
				"T1.2   Second          superseded (by T1.2a, T1.2b)  -\n" +
				"T1.2a  Second, part a  running                       -\n" +
				"T1.2b  Second, part b  pending                       -\n",
		},
		{
			name: "nothing recorded",
			plan: ledgerCmdPlan,
			want: "ID     TITLE           STATUS   UNMET DEPENDENCIES\n" +
				"T1.1   First           pending  -\n" +
				"T1.2   Second          pending  T1.1\n" +
				"T1.2a  Second, part a  pending  T1.1\n" +
				"T1.2b  Second, part b  pending  T1.1\n",
		},
		{
			name:   "note for a task naming no ids",
			plan:   noteCmdPlan,
			ledger: []string{"T1.1=done"},
			want: "ID    TITLE   STATUS   UNMET DEPENDENCIES\n" +
				"T1.1  First   done     -\n" +
				"T1.2  Second  pending  -\n" +
				"Note: T1.2 depends on \"as available\", which names no task ids, so afk treats it as having no dependencies.\n",
		},
		{
			name:   "ledger entry outside the plan",
			plan:   ledgerCmdPlan,
			ledger: []string{"T9.9=done"},
			want: "ID     TITLE           STATUS   UNMET DEPENDENCIES\n" +
				"T1.1   First           pending  -\n" +
				"T1.2   Second          pending  T1.1\n" +
				"T1.2a  Second, part a  pending  T1.1\n" +
				"T1.2b  Second, part b  pending  T1.1\n" +
				"Note: the ledger has an entry for T9.9, which is not in the plan.\n",
		},
		{
			name:   "afk.toml points at another plan",
			plan:   "",
			toml:   "plan = \"plans/build.md\"\n",
			planAt: "plans/build.md",
			want: "ID    TITLE  STATUS   UNMET DEPENDENCIES\n" +
				"T1.1  Solo   pending  -\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := ledgerCmdProject(t, tt.plan)
			if tt.toml != "" {
				writeBuildFile(t, project, "afk.toml", tt.toml)
				writeBuildFile(t, project, tt.planAt, "#### T1.1 — Solo\n\n- **Depends on:** none\n")
			}
			seedLedger(t, project, tt.ledger...)
			code, stdout, stderr := runTasksCmd(t, project)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			if stdout != tt.want {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, tt.want)
			}
		})
	}
}

// TestTasksErrors pins exit codes and messages.
//
// Load-bearing rows: the cycle row must name the ids so the owner can find
// the loop; "damaged ledger" must say what to do and leave the file alone.
func TestTasksErrors(t *testing.T) {
	const cyclePlan = `#### T1.1 — A

- **Depends on:** T1.2

#### T1.2 — B

- **Depends on:** T1.1
`
	tests := []struct {
		name  string
		plan  string
		args  []string
		setup func(t *testing.T, project string)
		code  int
		want  []string
	}{
		{name: "positional word", plan: ledgerCmdPlan, args: []string{"extra"}, code: 2, want: []string{"unexpected word \"extra\"", "Usage: afk tasks"}},
		{name: "unknown flag", plan: ledgerCmdPlan, args: []string{"--json"}, code: 2},
		{name: "missing plan", plan: "", code: 1, want: []string{"no master plan at", "docs/PLAN.md"}},
		{name: "cycle", plan: cyclePlan, code: 1, want: []string{"T1.1", "T1.2", "cycle"}},
		{
			name: "damaged ledger", plan: ledgerCmdPlan, code: 1,
			setup: func(t *testing.T, project string) { writeLedgerFile(t, project, "{not json") },
			want:  []string{"damaged", "Fix or delete"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := ledgerCmdProject(t, tt.plan)
			if tt.setup != nil {
				tt.setup(t, project)
			}
			code, stdout, stderr := runTasksCmd(t, project, tt.args...)
			if code != tt.code {
				t.Fatalf("code=%d want %d; stderr=%q", code, tt.code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout should be empty, got %q", stdout)
			}
			for _, w := range tt.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr %q lacks %q", stderr, w)
				}
			}
		})
	}
}

func TestTasksHelp(t *testing.T) {
	var so, se bytes.Buffer
	if code := runTasks([]string{"-h"}, &so, &se, t.TempDir(), "", fakeCommonDir("")); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(se.String(), "afk tasks") {
		t.Errorf("no usage: %q", se.String())
	}
}

// TestTasksLinkedWorktree proves the ledger is read from the main checkout
// when the command runs inside a linked worktree.
func TestTasksLinkedWorktree(t *testing.T) {
	main := ledgerCmdProject(t, ledgerCmdPlan)
	seedLedger(t, main, "T1.1=done")
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	var so, se bytes.Buffer
	code := runTasks([]string{"--project", wt}, &so, &se, wt, "", fakeCommonDir(filepath.Join(main, ".git")))
	if code != 0 || !strings.Contains(so.String(), "T1.1   First           done") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, so.String(), se.String())
	}
}
