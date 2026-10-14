package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/tokens"
	"github.com/dphaener/afk/internal/ui"
)

// TestDashLoadTaskTokens pins the dashboard adapter over taskTokensForSurface.
//
// Load-bearing rows: "recorded" (Lines must equal what `afk tokens T1.2`
// prints through runTokens on the same root, so the overlay cannot drift from
// the command), "unreadable" (must not collapse into none, which would hide a
// broken ledger) and "no ledger file" and "nothing for the task" (both none,
// with no content, so the row shows no total).
func TestDashLoadTaskTokens(t *testing.T) {
	recorded := func(t *testing.T) string {
		return writeSurfaceLedger(t, []tokens.Line{
			{Task: "T1.2", Session: "a", Phase: "execute", Role: "task", Model: "m1", Calls: 3, Input: 1000, Output: 200, CacheWrite: 400, CacheRead: 5000, Weighted: 3000},
			{Task: "T1.2", Session: "a", Phase: "review", Role: "verifier", Model: "m2", Calls: 1, Input: 50, Output: 10, Weighted: 100},
		})
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) string
		task  string
		want  ui.TaskTokensState
	}{
		{"recorded", recorded, "T1.2", ui.TaskTokensRecorded},
		{"nothing for the task", recorded, "T1.9", ui.TaskTokensNone},
		{"no ledger file", func(t *testing.T) string { return t.TempDir() }, "T1.2", ui.TaskTokensNone},
		{"unreadable", func(t *testing.T) string {
			root := t.TempDir()
			if err := os.MkdirAll(tokens.LedgerPath(root), 0o755); err != nil {
				t.Fatal(err)
			}
			return root
		}, "T1.2", ui.TaskTokensUnreadable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := c.setup(t)
			got := dashLoadTaskTokens(root, c.task)
			if got.State != c.want || got.Task != c.task || got.Path != tokens.LedgerPath(root) {
				t.Fatalf("got %+v", got)
			}
			switch c.want {
			case ui.TaskTokensRecorded:
				var stdout, stderr bytes.Buffer
				code := runTokens([]string{c.task}, &stdout, &stderr, root, "", tokensNow, fakeTokensEnv(root, nil))
				if code != 0 {
					t.Fatalf("runTokens exit %d: %s", code, stderr.String())
				}
				if want := strings.Join(got.Lines, "\n") + "\n"; stdout.String() != want {
					t.Fatalf("Lines differ from the command:\n%q\n%q", want, stdout.String())
				}
				if got.Short != "3.1k weighted" || len(got.Compact) == 0 || got.Err != "" {
					t.Fatalf("recorded fields: %+v", got)
				}
			case ui.TaskTokensUnreadable:
				if !strings.HasPrefix(got.Err, "reading ledger "+got.Path) || got.Lines != nil || got.Short != "" {
					t.Fatalf("unreadable fields: %+v", got)
				}
			default:
				if got.Lines != nil || got.Compact != nil || got.Short != "" || got.Err != "" {
					t.Fatalf("none carries content: %+v", got)
				}
			}
		})
	}
}
