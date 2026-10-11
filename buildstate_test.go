package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
)

// fakeCommonDir returns a gitCommonDir lookup that answers with a fixed value
// and records nothing else: tests never run git.
func fakeCommonDir(answer string) func(string) string {
	return func(string) string { return answer }
}

// TestResolveBuildRoot checks the two steps compose: the flag wins over cwd,
// and the result is then mapped to the main checkout.
func TestResolveBuildRoot(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		cwd     string
		common  string
		want    string
		wantErr bool
	}{
		{name: "cwd in linked worktree", cwd: "/repo/wt", common: "/repo/.git", want: "/repo"},
		{name: "flag beats cwd", flag: "/other", cwd: "/repo/wt", common: "", want: "/other"},
		{name: "flag in linked worktree", flag: "/repo/wt", cwd: "/x", common: "/repo/.git", want: "/repo"},
		{name: "neither flag nor cwd", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveBuildRoot(tc.flag, tc.cwd, "/home/u", fakeCommonDir(tc.common))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("resolveBuildRoot = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

const buildPlanOK = `# Plan

### Milestone 1

#### T1.1 — First

- **Depends on:** none

#### T1.2 — Second

- **Depends on:** T1.1
`

// writeBuildFile writes content at root/rel, creating directories.
func writeBuildFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadBuildState covers the success paths and every failure the commands
// report.
//
// Load-bearing rows: "missing plan file", "dependency cycle" and "damaged
// ledger" each assert that the message names the file and says what to do,
// because the person reading it has nothing else to go on. "missing ledger is
// empty" guards the first run of a project, and "damaged ledger" also
// asserts the file is left alone: loading must never repair or delete it.
func TestLoadBuildState(t *testing.T) {
	cases := []struct {
		name      string
		files     map[string]string
		wantPlan  string // path relative to root
		wantTasks int
		wantLedge int
		wantErr   []string // substrings, all required
		wantIs    error
	}{
		{
			name:      "defaults with no afk.toml",
			files:     map[string]string{"docs/PLAN.md": buildPlanOK},
			wantPlan:  "docs/PLAN.md",
			wantTasks: 2,
		},
		{
			name: "plan pointing elsewhere",
			files: map[string]string{
				"afk.toml":        "plan = \"plans/master.md\"\n",
				"plans/master.md": buildPlanOK,
			},
			wantPlan:  "plans/master.md",
			wantTasks: 2,
		},
		{
			name: "missing ledger is empty",
			files: map[string]string{
				"docs/PLAN.md": buildPlanOK,
			},
			wantPlan:  "docs/PLAN.md",
			wantTasks: 2,
			wantLedge: 0,
		},
		{
			name: "existing ledger is loaded",
			files: map[string]string{
				"docs/PLAN.md":           buildPlanOK,
				".afk/build/ledger.json": `{"tasks":[{"id":"T1.1","status":"done"}]}`,
			},
			wantPlan:  "docs/PLAN.md",
			wantTasks: 2,
			wantLedge: 1,
		},
		{
			name:    "missing plan file",
			files:   map[string]string{},
			wantErr: []string{"no master plan at", filepath.Join("docs", "PLAN.md"), "set `plan` in afk.toml or create the file"},
		},
		{
			name: "missing plan file named by afk.toml",
			files: map[string]string{
				"afk.toml": "plan = \"nope.md\"\n",
			},
			wantErr: []string{"no master plan at", "nope.md"},
		},
		{
			name:    "bad afk.toml",
			files:   map[string]string{"afk.toml": "plan = [", "docs/PLAN.md": buildPlanOK},
			wantErr: []string{"afk.toml", "Fix"},
		},
		{
			name: "plan error carries its line",
			files: map[string]string{
				"docs/PLAN.md": "# Plan\n\n#### T1.1 — First\n\nno deps line\n\n#### T1.2 — Second\n\n- **Depends on:** none\n",
			},
			wantErr: []string{"PLAN.md", "line 3", "Fix the plan"},
			wantIs:  masterplan.ErrNoDependsLine,
		},
		{
			name: "unknown dependency",
			files: map[string]string{
				"docs/PLAN.md": "# Plan\n\n#### T1.1 — First\n\n- **Depends on:** T9.9\n",
			},
			wantErr: []string{"PLAN.md", "T9.9", "Fix the plan"},
			wantIs:  masterplan.ErrUnknownDependency,
		},
		{
			name: "dependency cycle",
			files: map[string]string{
				"docs/PLAN.md": "# Plan\n\n#### T1.1 — First\n\n- **Depends on:** T1.2\n\n#### T1.2 — Second\n\n- **Depends on:** T1.1\n",
			},
			wantErr: []string{"PLAN.md", "T1.1 -> T1.2 -> T1.1", "Fix the plan"},
			wantIs:  masterplan.ErrCycle,
		},
		{
			name: "damaged ledger",
			files: map[string]string{
				"docs/PLAN.md":           buildPlanOK,
				".afk/build/ledger.json": `{"tasks": [`,
			},
			wantErr: []string{"damaged", filepath.Join(".afk", "build", "ledger.json"), "Fix or delete", "forgets every recorded task"},
			wantIs:  ledger.ErrCorrupt,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tc.files {
				writeBuildFile(t, root, rel, content)
			}
			ledgerFile := filepath.Join(root, ".afk", "build", "ledger.json")
			before, _ := os.ReadFile(ledgerFile)

			bs, err := loadBuildState(root)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("want an error, got state %+v", bs)
				}
				for _, s := range tc.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err.Error(), s)
					}
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Errorf("error %q does not wrap %v", err.Error(), tc.wantIs)
				}
				after, _ := os.ReadFile(ledgerFile)
				if string(before) != string(after) {
					t.Errorf("ledger file changed: %q -> %q", before, after)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bs.Root != root {
				t.Errorf("Root = %q, want %q", bs.Root, root)
			}
			if want := filepath.Join(root, filepath.FromSlash(tc.wantPlan)); bs.PlanPath != want {
				t.Errorf("PlanPath = %q, want %q", bs.PlanPath, want)
			}
			if len(bs.Tasks) != tc.wantTasks {
				t.Errorf("%d tasks, want %d", len(bs.Tasks), tc.wantTasks)
			}
			if len(bs.Ledger.Tasks) != tc.wantLedge {
				t.Errorf("%d ledger entries, want %d", len(bs.Ledger.Tasks), tc.wantLedge)
			}
		})
	}
}
