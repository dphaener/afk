package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/tokens"
)

// writeSurfaceLedger writes lines into a scratch project's token ledger and
// returns the project root.
func writeSurfaceLedger(t *testing.T, lines []tokens.Line) string {
	t.Helper()
	root := t.TempDir()
	if err := tokens.Append(tokens.LedgerPath(root), lines); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestTaskTokensForSurface pins the dashboard's entry point.
//
// Load-bearing rows: "full record" (Lines must equal what the command prints,
// byte for byte, so the overlay and `afk tokens T1.2` cannot drift),
// "nothing for the task" versus "no ledger file" (the flag that tells them
// apart), "unreadable" (a directory at the ledger path: the state must not be
// read as "none", which would hide a broken ledger behind a friendly
// sentence) and "case-insensitive id".
func TestTaskTokensForSurface(t *testing.T) {
	full := []tokens.Line{
		{Task: "T1.2", Session: "a", Phase: "execute", Role: "task", Model: "m1", Calls: 3, Input: 1000, Output: 200, CacheWrite: 400, CacheRead: 5000, Weighted: 3000},
		{Task: "T1.2", Session: "a", Phase: "review", Role: "verifier", Model: "m2", Calls: 1, Input: 50, Output: 10, Weighted: 100},
		{Task: "T1.3", Session: "b", Phase: "execute", Role: "task", Model: "m1", Calls: 1, Input: 10, Weighted: 10},
	}
	onePhase := []tokens.Line{
		{Task: "T1.2", Session: "a", Phase: "execute", Role: "task", Model: "m1", Calls: 2, Input: 400000, Output: 100000, Weighted: 2871239},
	}

	cases := []struct {
		name         string
		setup        func(t *testing.T) string
		task         string
		wantState    tokensState
		wantNoLedger bool
		wantShort    string
		wantCompact  int // rows in Compact, header "Task" line included
		wantErr      string
	}{
		{name: "full record", task: "T1.2", wantState: tokensRecorded, wantShort: "3.1k weighted", wantCompact: 7,
			setup: func(t *testing.T) string { return writeSurfaceLedger(t, full) }},
		{name: "one phase only", task: "T1.2", wantState: tokensRecorded, wantShort: "2.9M weighted", wantCompact: 5,
			setup: func(t *testing.T) string { return writeSurfaceLedger(t, onePhase) }},
		{name: "case-insensitive id", task: "t1.2", wantState: tokensRecorded, wantShort: "3.1k weighted", wantCompact: 7,
			setup: func(t *testing.T) string { return writeSurfaceLedger(t, full) }},
		{name: "no ledger file", task: "T1.2", wantState: tokensNone, wantNoLedger: true,
			setup: func(t *testing.T) string { return t.TempDir() }},
		{name: "nothing for the task", task: "T1.9", wantState: tokensNone,
			setup: func(t *testing.T) string { return writeSurfaceLedger(t, full) }},
		{name: "unreadable ledger", task: "T1.2", wantState: tokensUnreadable, wantErr: "reading ledger ",
			setup: func(t *testing.T) string {
				root := t.TempDir()
				if err := os.MkdirAll(tokens.LedgerPath(root), 0o755); err != nil {
					t.Fatal(err)
				}
				return root
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := c.setup(t)
			res := taskTokensForSurface(root, c.task)
			if res.State != c.wantState {
				t.Fatalf("state = %v, want %v (%+v)", res.State, c.wantState, res)
			}
			if res.Path != tokens.LedgerPath(root) || res.Task != c.task {
				t.Fatalf("path/task = %q/%q", res.Path, res.Task)
			}
			if res.NoLedger != c.wantNoLedger {
				t.Fatalf("NoLedger = %v", res.NoLedger)
			}
			switch c.wantState {
			case tokensUnreadable:
				if !strings.HasPrefix(res.Err, c.wantErr+res.Path) || res.Lines != nil {
					t.Fatalf("err %q lines %v", res.Err, res.Lines)
				}
			case tokensNone:
				if res.Lines != nil || res.Compact != nil || res.Short != "" || res.Err != "" {
					t.Fatalf("none result carries content: %+v", res)
				}
			case tokensRecorded:
				var out, errOut bytes.Buffer
				env := fakeTokensEnv(root, nil)
				code := runTokens([]string{"--project", root, c.task}, &out, &errOut, root, root, tokensNow, env)
				if code != 0 {
					t.Fatalf("command exit %d: %s", code, errOut.String())
				}
				if got := strings.Join(res.Lines, "\n") + "\n"; got != out.String() {
					t.Fatalf("Lines differ from the command's stdout:\n%q\n%q", got, out.String())
				}
				if res.Lines[0] != "Task "+c.task || res.Compact[0] != "Task "+c.task {
					t.Fatalf("first lines %q / %q", res.Lines[0], res.Compact[0])
				}
				if len(res.Compact) != c.wantCompact {
					t.Fatalf("compact has %d lines:\n%s", len(res.Compact), strings.Join(res.Compact, "\n"))
				}
				if strings.Contains(strings.Join(res.Compact, "\n"), "cache") {
					t.Fatalf("compact has wide columns")
				}
				if res.Short != c.wantShort {
					t.Fatalf("Short = %q, want %q", res.Short, c.wantShort)
				}
				if last := res.Lines[len(res.Lines)-1]; !strings.Contains(last, "total") {
					t.Fatalf("last line %q is not the total row", last)
				}
			}
		})
	}
}

// TestTaskTokensForSurfaceReadsOnlyTheLedger: calling it must not create any
// file, which is what lets the dashboard call it on every refresh.
func TestTaskTokensForSurfaceReadsOnlyTheLedger(t *testing.T) {
	root := t.TempDir()
	taskTokensForSurface(root, "T1.2")
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("created %v", entries)
	}
}
