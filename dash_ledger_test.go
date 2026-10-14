package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/ui"
)

// TestDashSetTaskStatus pins the adapter between the dashboard's seam and the
// shared ledger entry point.
//
// Load-bearing rows: "superseded without ids" because the picker's refusal
// must arrive as a refusal in the command's words, not a write; "unknown
// status" because the adapter parses the word itself; "unwritable ledger"
// because a failed write must not read as a change.
func TestDashSetTaskStatus(t *testing.T) {
	cases := []struct {
		name         string
		existing     string
		readOnly     bool
		id, status   string
		repl         []string
		wantOutcome  string
		wantText     string
		wantNoPrefix bool
	}{
		{name: "changed", existing: `{"tasks":[{"id":"T1.1","status":"failed"}]}`, id: "T1.1", status: "pending",
			wantOutcome: ui.TaskStatusChanged, wantText: "T1.1: failed → pending"},
		{name: "already", existing: `{"tasks":[{"id":"T1.1","status":"pending"}]}`, id: "T1.1", status: "pending",
			wantOutcome: ui.TaskStatusAlready, wantText: "T1.1 is already pending"},
		{name: "unknown id refused", id: "T9.9", status: "pending",
			wantOutcome: ui.TaskStatusRefused, wantText: "T9.9 is not a task in"},
		{name: "unknown status", id: "T1.1", status: "bogus",
			wantOutcome: ui.TaskStatusRefused, wantText: "bogus"},
		{name: "superseded without ids", id: "T1.2", status: "superseded",
			wantOutcome: ui.TaskStatusRefused, wantText: "needs the tasks that replace it"},
		{name: "superseded with ids", id: "T1.2", status: "superseded", repl: []string{"T1.2a"},
			wantOutcome: ui.TaskStatusChanged, wantText: "T1.2: pending → superseded (replaced by T1.2a)"},
		{name: "unwritable ledger", readOnly: true, id: "T1.1", status: "done",
			wantOutcome: ui.TaskStatusFailed, wantText: "could not update"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.readOnly && os.Geteuid() == 0 {
				t.Skip("root ignores folder permissions")
			}
			project := ledgerCmdProject(t, ledgerCmdPlan)
			if c.existing != "" {
				writeLedgerFile(t, project, c.existing)
			}
			if c.readOnly {
				dir := filepath.Dir(ledger.Path(project))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			}
			got := dashSetTaskStatus(project, c.id, c.status, c.repl)
			if got.Outcome != c.wantOutcome {
				t.Errorf("outcome = %q, want %q (text %q)", got.Outcome, c.wantOutcome, got.Text)
			}
			if !strings.Contains(got.Text, c.wantText) {
				t.Errorf("text = %q, want it to contain %q", got.Text, c.wantText)
			}
			if strings.Contains(got.Text, "afk ledger set:") || strings.Contains(got.Text, "Usage:") {
				t.Errorf("text carries the command's framing: %q", got.Text)
			}
		})
	}
}
