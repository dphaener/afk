package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/tokens"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// TestWebSetTaskStatus pins the web page's status seam against a scratch
// project.
//
// Load-bearing rows: "unknown status" because the word never reaches the
// writer and the answer is the parser's own text; "superseded without
// replacements" and "unknown id" because they are refusals that must leave no
// ledger file; "ledger cannot be written" because "failed" with the file named
// is the only way the page can tell the write did not happen; "failed to
// pending" because Old must be the status in the file, not a guess.
func TestWebSetTaskStatus(t *testing.T) {
	cases := []struct {
		name        string
		existing    string
		readOnly    bool
		id, status  string
		repl        []string
		wantOutcome string
		wantOld     string
		wantNew     string
		wantText    string
		wantNoFile  bool
	}{
		{name: "pending to running", id: "T1.1", status: "running", wantOutcome: web.StatusChanged, wantOld: "pending", wantNew: "running", wantText: "T1.1: pending → running"},
		{name: "pending to shipping", id: "T1.1", status: "shipping", wantOutcome: web.StatusChanged, wantOld: "pending", wantNew: "shipping", wantText: "pending → shipping"},
		{name: "pending to parked", id: "T1.1", status: "parked", wantOutcome: web.StatusChanged, wantOld: "pending", wantNew: "parked", wantText: "pending → parked"},
		{name: "pending to done", id: "T1.1", status: "done", wantOutcome: web.StatusChanged, wantOld: "pending", wantNew: "done", wantText: "pending → done"},
		{name: "pending to failed", id: "T1.1", status: "failed", wantOutcome: web.StatusChanged, wantOld: "pending", wantNew: "failed", wantText: "pending → failed"},
		{name: "failed to pending", existing: `{"tasks":[{"id":"T1.1","status":"failed"}]}`, id: "T1.1", status: "pending",
			wantOutcome: web.StatusChanged, wantOld: "failed", wantNew: "pending", wantText: "T1.1: failed → pending"},
		{name: "already at status", existing: `{"tasks":[{"id":"T1.1","status":"done"}]}`, id: "T1.1", status: "done",
			wantOutcome: web.StatusAlready, wantOld: "done", wantNew: "done", wantText: "T1.1 is already done"},
		{name: "superseded with replacements", id: "T1.2", status: "superseded", repl: []string{"T1.2a", "T1.2b"},
			wantOutcome: web.StatusChanged, wantOld: "pending", wantNew: "superseded", wantText: "replaced by T1.2a, T1.2b"},
		{name: "superseded without replacements", id: "T1.2", status: "superseded",
			wantOutcome: web.StatusRefused, wantText: "needs the tasks that replace it", wantNoFile: true},
		{name: "replacements with another status", id: "T1.2", status: "done", repl: []string{"T1.2a"},
			wantOutcome: web.StatusRefused, wantText: "only goes with the status superseded", wantNoFile: true},
		{name: "unknown id", id: "T9.9", status: "done",
			wantOutcome: web.StatusRefused, wantText: "T9.9 is not a task in", wantNoFile: true},
		{name: "unknown status", id: "T1.1", status: "finished",
			wantOutcome: web.StatusRefused, wantText: "finished", wantNoFile: true},
		{name: "ledger cannot be written", readOnly: true, id: "T1.1", status: "done",
			wantOutcome: web.StatusFailed, wantText: "could not update", wantNoFile: true},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := ledgerCmdProject(t, ledgerCmdPlan)
			switch {
			case tt.readOnly:
				if os.Geteuid() == 0 {
					t.Skip("root ignores folder permissions")
				}
				dir := filepath.Dir(ledger.Path(project))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(dir, 0o755) })
			case tt.existing != "":
				writeLedgerFile(t, project, tt.existing)
			}
			got := webSetTaskStatus(project, tt.id, tt.status, tt.repl)
			if got.Outcome != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q (text: %s)", got.Outcome, tt.wantOutcome, got.Text)
			}
			if got.Old != tt.wantOld || got.New != tt.wantNew {
				t.Errorf("old/new = %q/%q, want %q/%q", got.Old, got.New, tt.wantOld, tt.wantNew)
			}
			if !strings.Contains(got.Text, tt.wantText) {
				t.Errorf("text %q missing %q", got.Text, tt.wantText)
			}
			_, err := os.Stat(ledger.Path(project))
			if tt.wantNoFile && err == nil {
				t.Error("ledger file was created")
			}
			if tt.wantOutcome == web.StatusChanged {
				l, err := ledger.Load(project)
				if err != nil {
					t.Fatal(err)
				}
				if string(l.Status(tt.id)) != tt.wantNew {
					t.Errorf("ledger status = %q, want %q", l.Status(tt.id), tt.wantNew)
				}
				e, _ := l.Get(tt.id)
				if tt.wantNew == "superseded" && !slices.Equal(e.ReplacedBy, tt.repl) {
					t.Errorf("replaced by = %v, want %v", e.ReplacedBy, tt.repl)
				}
			}
		})
	}
}

// TestWebTaskTokens pins the tokens seam. Load-bearing rows: "no ledger" and
// "nothing for this task" both read "none" (the page has one empty state);
// "unreadable" carries the reader's words, so a broken file is not shown as
// an empty one; "recorded" must agree with the command's total and phases.
func TestWebTaskTokens(t *testing.T) {
	line := func(task, phase, role string, in, out int64) tokens.Line {
		return tokens.Line{Task: task, Session: "s", Phase: phase, Role: role, Model: "m", Calls: 1, Input: in, Output: out,
			Weighted: tokens.Weighted(in, out, 0, 0)}
	}
	cases := []struct {
		name      string
		lines     []tokens.Line
		unread    bool
		wantState string
		wantTotal float64
		wantPh    []string
		wantRoles []string
	}{
		{name: "recorded", lines: []tokens.Line{line("T1.1", "execute", "worker", 100, 10), line("T1.1", "review", "reviewer", 50, 0), line("T1.2", "execute", "worker", 9, 9)},
			wantState: web.TokensRecorded, wantTotal: 200, wantPh: []string{"execute", "review"}, wantRoles: []string{"worker", "reviewer"}},
		{name: "no ledger", wantState: web.TokensNone},
		{name: "nothing for this task", lines: []tokens.Line{line("T1.2", "execute", "worker", 9, 9)}, wantState: web.TokensNone},
		{name: "unreadable", unread: true, wantState: web.TokensUnreadable},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := t.TempDir()
			path := tokens.LedgerPath(project)
			if tt.unread {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if tt.lines != nil {
				if err := tokens.Append(path, tt.lines); err != nil {
					t.Fatal(err)
				}
			}
			got := webTaskTokens(project, "T1.1")
			if got.State != tt.wantState {
				t.Fatalf("state = %q, want %q", got.State, tt.wantState)
			}
			if tt.unread && got.Err == "" {
				t.Error("unreadable gave no error text")
			}
			if !tt.unread && got.Err != "" {
				t.Errorf("error = %q, want none", got.Err)
			}
			if got.Total != tt.wantTotal {
				t.Errorf("total = %v, want %v", got.Total, tt.wantTotal)
			}
			var ph, ro []string
			for _, p := range got.Phases {
				ph = append(ph, p.Phase)
			}
			for _, r := range got.Roles {
				ro = append(ro, r.Role)
			}
			if !slices.Equal(ph, tt.wantPh) || !slices.Equal(ro, tt.wantRoles) {
				t.Errorf("phases %v roles %v, want %v %v", ph, ro, tt.wantPh, tt.wantRoles)
			}
			if tt.wantState == web.TokensRecorded && got.Short == "" {
				t.Error("recorded gave no short total")
			}
		})
	}
}

// TestDaemonWebTaskSeams: the daemon fills both task seams, so the Tasks
// screens are live and not "cannot show tokens" (the nil-seam wording).
func TestDaemonWebTaskSeams(t *testing.T) {
	d := daemonWebDeps(nil, t.TempDir(), usagestate.Deps{}, nil)
	if d.TaskTokens == nil || d.SetTaskStatus == nil {
		t.Fatalf("seams not filled: tokens %v status %v", d.TaskTokens != nil, d.SetTaskStatus != nil)
	}
}
