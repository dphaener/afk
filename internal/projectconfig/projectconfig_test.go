package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fullExample is the afk.toml from docs/PLAN.md section 4.6, verbatim apart
// from the ntfy_url line, whose %s each row fills in.
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
ntfy_url = %s                   # e.g. https://ntfy.example/afk-nebari
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
		{name: "full example", file: ptr(sprintf(fullExample, `""`)), want: "docs/PLAN.md"},
		{name: "no plan key but other sections", file: ptr("[ship]\nbase = \"main\"\n"), want: DefaultPlan},
		{name: "only a notify table keeps the default plan", file: ptr("[notify]\nntfy_url = \"https://ntfy.sh/t\"\n"), want: DefaultPlan},
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

// TestLoadNotify is table-driven over file contents and checks the ntfy
// address. Load-bearing rows: the verbatim section 4.6 example (the other
// keys and the multi-line notes must not break the reader), the rejections
// (each must name afk.toml and must not echo the password in the URL), and
// the missing file (no error, because most projects have none).
func TestLoadNotify(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil means no file
		want    string
		errHas  []string
	}{
		{name: "missing file", content: nil, want: ""},
		{name: "empty file", content: ptr(""), want: ""},
		{name: "plan example with empty url", content: ptr(sprintf(fullExample, `""`)), want: ""},
		{name: "plan example with url", content: ptr(sprintf(fullExample, `"https://ntfy.example/afk-nebari"`)), want: "https://ntfy.example/afk-nebari"},
		{name: "surrounding spaces trimmed", content: ptr("[notify]\nntfy_url = \"  https://ntfy.sh/t  \"\n"), want: "https://ntfy.sh/t"},
		{name: "http allowed", content: ptr("[notify]\nntfy_url = \"http://localhost:8080/t\"\n"), want: "http://localhost:8080/t"},
		{name: "ftp scheme", content: ptr("[notify]\nntfy_url = \"ftp://user:hunter2@ntfy.sh/t\"\n"), errHas: []string{"afk.toml", "[notify] ntfy_url"}},
		{name: "no host", content: ptr("[notify]\nntfy_url = \"https:///t\"\n"), errHas: []string{"afk.toml", "[notify] ntfy_url"}},
		{name: "no scheme", content: ptr("[notify]\nntfy_url = \"ntfy.sh/t\"\n"), errHas: []string{"afk.toml", "[notify] ntfy_url"}},
		{name: "unparseable url", content: ptr("[notify]\nntfy_url = \"http://user:hunter2@ntfy.sh:bad/t\"\n"), errHas: []string{"afk.toml", "[notify] ntfy_url"}},
		{name: "wrong type", content: ptr("[notify]\nntfy_url = 5\n"), errHas: []string{"afk.toml"}},
		{name: "broken toml", content: ptr("[notify\nntfy_url = \n"), errHas: []string{"afk.toml"}},
		{name: "broken toml beside a secret", content: ptr("[notify]\nntfy_url = \"http://user:hunter2@ntfy.sh/t\" junk\n"), errHas: []string{"afk.toml"}},
		{name: "wrong type beside a secret", content: ptr("plan = \"http://user:hunter2@x\"\n[notify]\nntfy_url = 5\n"), errHas: []string{"afk.toml"}},
		{name: "unknown tables ignored", content: ptr("[mystery]\nx = 1\n[notify]\nother = true\nntfy_url = \"https://ntfy.sh/t\"\n[more.nested]\ny = [1, 2]\n"), want: "https://ntfy.sh/t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.content != nil {
				if err := os.WriteFile(filepath.Join(root, FileName), []byte(*tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Load(root)
			if len(tt.errHas) > 0 {
				if err == nil {
					t.Fatalf("Load = %+v, want an error", got)
				}
				for _, s := range tt.errHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q does not mention %q", err, s)
					}
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Errorf("error leaks the URL password: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.Notify.NtfyURL != tt.want {
				t.Errorf("NtfyURL = %q, want %q", got.Notify.NtfyURL, tt.want)
			}
		})
	}
}

// TestLoadBothKeys: the fold's reason to exist. One file with plan and
// [notify] must give both values, and each half must default independently
// when the other is the only thing present. A missing file must give the
// default plan with a zero Notify, since callers use the plan unconditionally.
func TestLoadBothKeys(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil means no file
		want    Config
	}{
		{"plan and notify together", ptr("plan = \"plans/m.md\"\n[notify]\nntfy_url = \"https://ntfy.sh/t\"\n"), Config{Plan: "plans/m.md", Notify: Notify{NtfyURL: "https://ntfy.sh/t"}}},
		{"only notify keeps default plan", ptr("[notify]\nntfy_url = \"https://ntfy.sh/t\"\n"), Config{Plan: DefaultPlan, Notify: Notify{NtfyURL: "https://ntfy.sh/t"}}},
		{"only plan leaves notify empty", ptr("plan = \"plans/m.md\"\n"), Config{Plan: "plans/m.md"}},
		{"missing file", nil, Config{Plan: DefaultPlan}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.content != nil {
				if err := os.WriteFile(filepath.Join(root, FileName), []byte(*tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Load(root)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got != tt.want {
				t.Errorf("Load = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadOverlay: the load-bearing rows are "local overrides committed" (the
// reason the overlay exists), "local sets only ntfy while committed sets only
// plan" (the merge is per key, not per file), "empty local ntfy wins" (present
// but empty must beat a committed address, so a private file can switch
// notifications off) and the two parse-error rows (each error must name the
// file that is broken, not its sibling). The bad-local row logs the error text
// for the QA evidence.
func TestLoadOverlay(t *testing.T) {
	tests := []struct {
		name      string
		committed *string
		local     *string
		want      Config
		errHas    string
		errNot    string
	}{
		{name: "both missing", want: Config{Plan: DefaultPlan}},
		{name: "committed only", committed: ptr("plan = \"a.md\"\n[notify]\nntfy_url = \"https://ntfy.sh/c\"\n"), want: Config{Plan: "a.md", Notify: Notify{NtfyURL: "https://ntfy.sh/c"}}},
		{name: "local only", local: ptr("plan = \"b.md\"\n[notify]\nntfy_url = \"https://ntfy.sh/l\"\n"), want: Config{Plan: "b.md", Notify: Notify{NtfyURL: "https://ntfy.sh/l"}}},
		{name: "local overrides committed", committed: ptr("plan = \"a.md\"\n[notify]\nntfy_url = \"https://ntfy.sh/c\"\n"), local: ptr("plan = \"b.md\"\n[notify]\nntfy_url = \"https://ntfy.sh/l\"\n"), want: Config{Plan: "b.md", Notify: Notify{NtfyURL: "https://ntfy.sh/l"}}},
		{name: "local sets only ntfy, committed only plan", committed: ptr("plan = \"a.md\"\n"), local: ptr("[notify]\nntfy_url = \"https://ntfy.sh/l\"\n"), want: Config{Plan: "a.md", Notify: Notify{NtfyURL: "https://ntfy.sh/l"}}},
		{name: "empty local ntfy wins over committed address", committed: ptr("[notify]\nntfy_url = \"https://ntfy.sh/c\"\n"), local: ptr("[notify]\nntfy_url = \"\"\n"), want: Config{Plan: DefaultPlan}},
		{name: "empty local file keeps committed", committed: ptr("plan = \"a.md\"\n"), local: ptr(""), want: Config{Plan: "a.md"}},
		{name: "local file does not parse", committed: ptr("plan = \"a.md\"\n"), local: ptr("plan = \n[[["), errHas: LocalFileName},
		{name: "committed file does not parse", committed: ptr("plan = \n[[["), local: ptr("plan = \"b.md\"\n"), errHas: FileName, errNot: LocalFileName},
		{name: "bad address in local names local", committed: ptr("[notify]\nntfy_url = \"https://ntfy.sh/c\"\n"), local: ptr("[notify]\nntfy_url = \"ftp://user:hunter2@ntfy.sh/t\"\n"), errHas: LocalFileName + ": [notify] ntfy_url"},
		{name: "bad address in committed names committed", committed: ptr("[notify]\nntfy_url = \"ntfy.sh/t\"\n"), local: ptr("plan = \"b.md\"\n"), errHas: FileName + ": [notify] ntfy_url", errNot: LocalFileName},
		{name: "bad plan in local names local", local: ptr("plan = \"../x.md\"\n"), errHas: LocalFileName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range map[string]*string{FileName: tt.committed, LocalFileName: tt.local} {
				if content == nil {
					continue
				}
				if err := os.WriteFile(filepath.Join(root, name), []byte(*content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Load(root)
			if tt.errHas != "" {
				if err == nil {
					t.Fatalf("Load = %+v, want an error", got)
				}
				t.Log(err)
				if !strings.Contains(err.Error(), tt.errHas) {
					t.Errorf("error %q lacks %q", err, tt.errHas)
				}
				if tt.errNot != "" && strings.Contains(err.Error(), tt.errNot) {
					t.Errorf("error %q wrongly names %q", err, tt.errNot)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Errorf("error leaks the URL password: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got != tt.want {
				t.Errorf("Load = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadUnreadable: a directory where the file should be makes the read
// fail with an error that is not "does not exist"; it must name the file.
func TestLoadUnreadable(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, FileName), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), FileName) {
		t.Fatalf("Load error = %v, want one naming %s", err, FileName)
	}
}

// TestPlanPath: the default and a custom plan, with and without a trailing
// slash on the root, both resolve under the root.
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

func sprintf(format, v string) string { return strings.Replace(format, "%s", v, 1) }
