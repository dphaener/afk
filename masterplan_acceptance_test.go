package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNebariSeededLedgerNextIsT42b is T1.1's acceptance from docs/PLAN.md: a
// project whose master plan is nebari's, with 21 tasks finished and T4.2 split
// into T4.2a and T4.2b, must be told to start T4.2b.
//
// Load-bearing assertions: `next` naming T4.2b (T4.2 is superseded and T4.2a
// is done, so the first unfinished task in plan order is T4.2b, and a parser or
// ledger that mishandled either would name something else); the 40 task rows
// (the fixture's whole task list parsed); and T6.1 still listing T4.2 as unmet
// until T4.3 is done, because T6.1 names the superseded id and must wait for
// its replacements, not for T4.2 itself.
func TestNebariSeededLedgerNextIsT42b(t *testing.T) {
	plan, err := os.ReadFile(filepath.Join("internal", "masterplan", "testdata", "nebari-plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "docs", "PLAN.md"), plan, 0o644); err != nil {
		t.Fatal(err)
	}

	set := func(args ...string) {
		t.Helper()
		var so, se bytes.Buffer
		full := append([]string{"--project", project}, args...)
		if code := runLedgerSet(full, &so, &se, project, "", fakeCommonDir("")); code != 0 {
			t.Fatalf("ledger set %v: exit %d: %s", args, code, se.String())
		}
	}
	next := func() string {
		t.Helper()
		code, out, errOut := runNextCmd(t, project)
		if code != 0 {
			t.Fatalf("next: exit %d: %s", code, errOut)
		}
		return out
	}
	tasks := func() string {
		t.Helper()
		code, out, errOut := runTasksCmd(t, project)
		if code != 0 {
			t.Fatalf("tasks: exit %d: %s", code, errOut)
		}
		return out
	}
	rowOf := func(out, id string) string {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, id+" ") {
				return line
			}
		}
		t.Fatalf("no row for %s in:\n%s", id, out)
		return ""
	}

	done := []string{"T0.1", "T0.2", "T0.3", "T1.1", "T1.2", "T1.3", "T1.4", "T2.1", "T2.2", "T2.3", "T2.4", "T2.5", "T2.6", "T3.1", "T3.2", "T3.3", "T3.3a", "T3.4", "T3.5", "T4.1", "T4.2a"}
	if len(done) != 21 {
		t.Fatalf("seed list has %d ids, want 21", len(done))
	}
	for _, id := range done {
		set(id, "done")
	}
	set("T4.2", "superseded", "--replaced-by", "T4.2a,T4.2b")

	if got := strings.Fields(next()); len(got) == 0 || got[0] != "T4.2b" {
		t.Fatalf("next = %q, want first field T4.2b", got)
	}
	out := tasks()
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 1 && line[0] == 'T' && line[1] >= '0' && line[1] <= '9' {
			rows++
		}
	}
	if rows != 40 {
		t.Errorf("tasks printed %d task rows, want 40:\n%s", rows, out)
	}

	set("T4.2b", "done")
	if got := strings.Fields(next()); len(got) == 0 || got[0] != "T4.3" {
		t.Fatalf("after T4.2b done, next = %q, want first field T4.3", got)
	}
	if row := rowOf(tasks(), "T6.1"); !strings.Contains(row, "T4.3") {
		t.Errorf("T6.1 should still wait on T4.3: %s", row)
	}
	set("T4.3", "done")
	if row := rowOf(tasks(), "T6.1"); strings.Contains(row, "T4.2") {
		t.Errorf("T6.1 should no longer list T4.2 as unmet: %s", row)
	}
}
