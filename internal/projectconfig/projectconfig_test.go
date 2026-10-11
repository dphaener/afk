package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fullExample is the afk.toml from docs/PLAN.md section 4.6, verbatim.
const fullExample = `plan = "docs/PLAN.md"

[ship]
base = "main"
merge_method = "merge"          # merge | squash | rebase
ci_reruns = 1                   # automatic re-runs before the failure goes back to the session
ci_fix_cycles = 2               # times a red CI is handed back before the task parks
deploys = true
health_url = ""                 # polled after merge when deploys = true
deploy_timeout = "15m"
required_env_pattern = ""       # regex; a diff adding a match parks the task before merge (§5.4)

[policy]
notes = """
Free text appended to the autonomous policy the agent reads. Example:
Live API calls are allowed for smoke checks and recording fixtures. Log each one.
"""

[notify]
ntfy_url = ""                   # e.g. https://ntfy.example/afk-nebari
`

// TestLoad: the load-bearing rows are "full example" (every other section
// must be tolerated, including the multi-line notes), "plan cleaned" (the
// stored value is the cleaned relative path) and the error rows, each of
// which must name afk.toml so the owner knows which file to edit.
func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		file    *string // nil means no file
		want    string
		wantErr []string // substrings the error must contain
	}{
		{name: "missing file", file: nil, want: DefaultPlan},
		{name: "empty file", file: ptr(""), want: DefaultPlan},
		{name: "plan set", file: ptr(`plan = "plans/master.md"`), want: "plans/master.md"},
		{name: "plan cleaned", file: ptr(`plan = "./docs//sub/../PLAN.md"`), want: "docs/PLAN.md"},
		{name: "plan inside with dotdot kept inside", file: ptr(`plan = "a/../b.md"`), want: "b.md"},
		{name: "full example", file: ptr(fullExample), want: "docs/PLAN.md"},
		{name: "no plan key but other sections", file: ptr("[ship]\nbase = \"main\"\n"), want: DefaultPlan},
		{name: "unknown keys and tables", file: ptr("mystery = 3\n[unknown]\nx = [1, 2]\n[ship.nested]\ny = true\n"), want: DefaultPlan},
		{name: "invalid toml", file: ptr("plan = \n[[["), wantErr: []string{FileName, "TOML"}},
		{name: "plan not a string", file: ptr("plan = 42"), wantErr: []string{FileName, "plan", "string"}},
		{name: "plan is a table", file: ptr("[plan]\nx = 1\n"), wantErr: []string{FileName, "plan", "string"}},
		{name: "empty plan", file: ptr(`plan = ""`), wantErr: []string{FileName, "plan", "empty"}},
		{name: "whitespace plan", file: ptr(`plan = "   "`), wantErr: []string{FileName, "plan", "empty"}},
		{name: "absolute plan", file: ptr(`plan = "/etc/plan.md"`), wantErr: []string{FileName, "plan", "relative"}},
		{name: "parent escape", file: ptr(`plan = "../PLAN.md"`), wantErr: []string{FileName, "plan", "inside"}},
		{name: "escape after clean", file: ptr(`plan = "docs/../../PLAN.md"`), wantErr: []string{FileName, "plan", "inside"}},
		{name: "bare parent", file: ptr(`plan = ".."`), wantErr: []string{FileName, "plan", "inside"}},
		{name: "project root itself", file: ptr(`plan = "."`), wantErr: []string{FileName, "plan", "file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.file != nil {
				if err := os.WriteFile(filepath.Join(root, FileName), []byte(*tt.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Load(root)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("Load = %+v, want error", got)
				}
				for _, s := range tt.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.Plan != tt.want {
				t.Errorf("Plan = %q, want %q", got.Plan, tt.want)
			}
		})
	}
}

func TestLoadUnreadable(t *testing.T) {
	root := t.TempDir()
	// A directory where the file should be makes the read fail with an
	// error that is not "does not exist".
	if err := os.Mkdir(filepath.Join(root, FileName), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), FileName) {
		t.Fatalf("Load error = %v, want one naming %s", err, FileName)
	}
}

func TestPlanPath(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		root string
		want string
	}{
		{"default", Config{Plan: DefaultPlan}, "/proj", "/proj/docs/PLAN.md"},
		{"custom", Config{Plan: "plans/m.md"}, "/proj/", "/proj/plans/m.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.PlanPath(tt.root); got != filepath.FromSlash(tt.want) {
				t.Errorf("PlanPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
