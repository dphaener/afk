package projectconfig

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestLoadIgnoresNtfyURL: a settings file written for an older afk may still
// hold [notify] ntfy_url, and it must load as if the line were not there.
// Load-bearing rows: "malformed value" and "wrong type" (the old reader
// refused both, so a regression that re-adds validation shows here), "in both
// files" (the overlay must not trip on it either) and "the verbatim section
// 4.6 example" (the rest of the file still reads). Every row also checks that
// the log stays silent and that [ship] is unchanged.
func TestLoadIgnoresNtfyURL(t *testing.T) {
	const ship = "[ship]\nbase = \"trunk\"\n"
	tests := []struct {
		name      string
		committed *string
		local     *string
	}{
		{name: "afk.toml only", committed: ptr(ship + "[notify]\nntfy_url = \"https://ntfy.sh/t\"\n")},
		{name: "afk.local.toml only", committed: ptr(ship), local: ptr("[notify]\nntfy_url = \"https://ntfy.sh/t\"\n")},
		{name: "in both files", committed: ptr(ship + "[notify]\nntfy_url = \"https://ntfy.sh/c\"\n"), local: ptr("[notify]\nntfy_url = \"https://ntfy.sh/l\"\n")},
		{name: "malformed value", committed: ptr(ship + "[notify]\nntfy_url = \"ftp://x\"\n")},
		{name: "wrong type", committed: ptr(ship + "[notify]\nntfy_url = 5\n")},
		{name: "empty string", committed: ptr(ship + "[notify]\nntfy_url = \"\"\n")},
		{name: "the verbatim section 4.6 example", committed: ptr(strings.Replace(sprintf(fullExample, `"https://ntfy.example/afk-nebari"`), `base = "main"`, `base = "trunk"`, 1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged bytes.Buffer
			log.SetOutput(&logged)
			defer log.SetOutput(os.Stderr)

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
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := Config{Plan: DefaultPlan, Ship: defaultShip()}
			want.Ship.Base = "trunk"
			if tt.name == "the verbatim section 4.6 example" {
				want = Config{Plan: "docs/PLAN.md", Ship: want.Ship}
				want.Ship.DeployTimeout = 15 * time.Minute
			}
			if got.Plan != want.Plan || got.Ship.Base != want.Ship.Base {
				t.Errorf("Load = %+v, want plan %q and ship base %q", got, want.Plan, want.Ship.Base)
			}
			if logged.Len() != 0 {
				t.Errorf("Load logged %q, want nothing", logged.String())
			}
		})
	}
}

// TestLoadBothKeys: the fold's reason to exist. One file with plan and
// [ship] must give both values, and each half must default independently
// when the other is the only thing present. A missing file must give the
// default plan, since callers use the plan unconditionally.
func TestLoadBothKeys(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil means no file
		want    Config
	}{
		{"plan and ship together", ptr("plan = \"plans/m.md\"\n[ship]\nbase = \"trunk\"\n"), Config{Plan: "plans/m.md", Ship: func() Ship { s := defaultShip(); s.Base = "trunk"; return s }()}},
		{"only plan", ptr("plan = \"plans/m.md\"\n"), Config{Plan: "plans/m.md"}},
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
			if tt.want.Ship == (Ship{}) {
				tt.want.Ship = defaultShip()
			}
			if got != tt.want {
				t.Errorf("Load = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadOverlay: the load-bearing rows are "local overrides committed" (the
// reason the overlay exists), "local sets only ship base while committed sets
// only plan" (the merge is per key, not per file), and the two parse-error
// rows (each error must name the file that is broken, not its sibling). The bad-local row logs the error text
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
		{name: "committed only", committed: ptr("plan = \"a.md\"\n"), want: Config{Plan: "a.md"}},
		{name: "local only", local: ptr("plan = \"b.md\"\n"), want: Config{Plan: "b.md"}},
		{name: "local overrides committed", committed: ptr("plan = \"a.md\"\n"), local: ptr("plan = \"b.md\"\n"), want: Config{Plan: "b.md"}},
		{name: "local sets only ship base, committed only plan", committed: ptr("plan = \"a.md\"\n"), local: ptr("[ship]\nbase = \"trunk\"\n"), want: Config{Plan: "a.md", Ship: func() Ship { s := defaultShip(); s.Base = "trunk"; return s }()}},
		{name: "empty local file keeps committed", committed: ptr("plan = \"a.md\"\n"), local: ptr(""), want: Config{Plan: "a.md"}},
		{name: "local file does not parse", committed: ptr("plan = \"a.md\"\n"), local: ptr("plan = \n[[["), errHas: LocalFileName},
		{name: "committed file does not parse", committed: ptr("plan = \n[[["), local: ptr("plan = \"b.md\"\n"), errHas: FileName, errNot: LocalFileName},
		{name: "bad after_merge_timeout in local names local", committed: ptr("[ship]\nafter_merge_timeout = \"5m\"\n"), local: ptr("[ship]\nafter_merge_timeout = \"soon\"\n"), errHas: LocalFileName + ": [ship] after_merge_timeout", errNot: FileName + ":"},
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
			if tt.want.Ship == (Ship{}) {
				tt.want.Ship = defaultShip()
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

// TestLoadShip: the load-bearing rows are "no file" (every default, since the
// daemon runs on projects with no afk.toml), "local overrides committed" (the
// overlay works key by key, and a local false/0 must beat a committed value),
// and the error rows, each of which must name the file the bad value came
// from and, for merge_method, list the three allowed words. For T2.2a the
// load-bearing new rows are the default (no command, 10m: a project with no
// after_merge must run nothing), the spaces-only command (it must read as none,
// not as a blank command to run), the local empty command beating a committed
// one (the owner's way to switch it off on one machine), and the bad timeout
// rows (they must name the file and key, and never repeat the command text).
func TestLoadShip(t *testing.T) {
	defaults := defaultShip()
	with := func(f func(*Ship)) Ship { s := defaults; f(&s); return s }
	tests := []struct {
		name      string
		committed *string
		local     *string
		want      Ship
		errHas    []string
		errNot    string
	}{
		{name: "no file", want: defaults},
		{name: "empty table", committed: ptr("[ship]\n"), want: defaults},
		{name: "unknown ship key tolerated", committed: ptr("[ship]\nmystery = 1\n"), want: defaults},
		{name: "base", committed: ptr("[ship]\nbase = \"develop\"\n"), want: with(func(s *Ship) { s.Base = "develop" })},
		{name: "merge_method squash", committed: ptr("[ship]\nmerge_method = \"squash\"\n"), want: with(func(s *Ship) { s.MergeMethod = "squash" })},
		{name: "merge_method rebase", committed: ptr("[ship]\nmerge_method = \"rebase\"\n"), want: with(func(s *Ship) { s.MergeMethod = "rebase" })},
		{name: "ci_reruns zero", committed: ptr("[ship]\nci_reruns = 0\n"), want: with(func(s *Ship) { s.CIReruns = 0 })},
		{name: "ci_fix_cycles", committed: ptr("[ship]\nci_fix_cycles = 5\n"), want: with(func(s *Ship) { s.CIFixCycles = 5 })},
		{name: "deploys false", committed: ptr("[ship]\ndeploys = false\n"), want: with(func(s *Ship) { s.Deploys = false })},
		{name: "health_url", committed: ptr("[ship]\nhealth_url = \"https://x.example/health\"\n"), want: with(func(s *Ship) { s.HealthURL = "https://x.example/health" })},
		{name: "deploy_timeout", committed: ptr("[ship]\ndeploy_timeout = \"1h30m\"\n"), want: with(func(s *Ship) { s.DeployTimeout = 90 * time.Minute })},
		{name: "after_merge default is none, 10m", committed: ptr("[ship]\n"), want: with(func(s *Ship) { s.AfterMerge = ""; s.AfterMergeTimeout = 10 * time.Minute })},
		{name: "after_merge set", committed: ptr("[ship]\nafter_merge = \"make install\"\n"), want: with(func(s *Ship) { s.AfterMerge = "make install" })},
		{name: "after_merge only spaces is none", committed: ptr("[ship]\nafter_merge = \"   \"\n"), want: defaults},
		{name: "after_merge is trimmed", committed: ptr("[ship]\nafter_merge = \"  make install \"\n"), want: with(func(s *Ship) { s.AfterMerge = "make install" })},
		{name: "after_merge_timeout set", committed: ptr("[ship]\nafter_merge_timeout = \"2m\"\n"), want: with(func(s *Ship) { s.AfterMergeTimeout = 2 * time.Minute })},
		{name: "local empty after_merge wins over committed", committed: ptr("[ship]\nafter_merge = \"make install\"\nafter_merge_timeout = \"3m\"\n"), local: ptr("[ship]\nafter_merge = \"\"\n"), want: with(func(s *Ship) { s.AfterMergeTimeout = 3 * time.Minute })},
		{name: "bad after_merge_timeout", committed: ptr("[ship]\nafter_merge_timeout = \"soon\"\n"), errHas: []string{FileName, "[ship] after_merge_timeout", "length of time"}},
		{name: "zero after_merge_timeout", committed: ptr("[ship]\nafter_merge_timeout = \"0s\"\n"), errHas: []string{FileName, "[ship] after_merge_timeout", "length of time"}},
		{name: "bad after_merge_timeout does not echo the command", committed: ptr("[ship]\nafter_merge = \"hunter2 --go\"\nafter_merge_timeout = \"soon\"\n"), errHas: []string{FileName, "after_merge_timeout"}},
		{name: "required_env_pattern", committed: ptr("[ship]\nrequired_env_pattern = \"^NEBARI_[A-Z]+=\"\n"), want: with(func(s *Ship) { s.RequiredEnvPattern = "^NEBARI_[A-Z]+=" })},
		{name: "full example", committed: ptr(sprintf(fullExample, `""`)), want: with(func(s *Ship) { s.Base = "main" })},
		{name: "local overrides committed key by key", committed: ptr("[ship]\nmerge_method = \"squash\"\nci_reruns = 3\ndeploys = true\n"), local: ptr("[ship]\nci_reruns = 0\ndeploys = false\n"), want: with(func(s *Ship) { s.MergeMethod = "squash"; s.CIReruns = 0; s.Deploys = false })},
		{name: "local empty health_url wins", committed: ptr("[ship]\nhealth_url = \"https://x.example/h\"\n"), local: ptr("[ship]\nhealth_url = \"\"\n"), want: defaults},
		{name: "bad merge_method", committed: ptr("[ship]\nmerge_method = \"fast\"\n"), errHas: []string{FileName, "merge, squash or rebase"}, errNot: LocalFileName},
		{name: "bad merge_method in local names local", local: ptr("[ship]\nmerge_method = \"fast\"\n"), errHas: []string{LocalFileName, "merge, squash or rebase"}},
		{name: "negative ci_reruns", committed: ptr("[ship]\nci_reruns = -1\n"), errHas: []string{FileName, "ci_reruns"}},
		{name: "negative ci_fix_cycles", committed: ptr("[ship]\nci_fix_cycles = -2\n"), errHas: []string{FileName, "ci_fix_cycles"}},
		{name: "bad deploy_timeout", committed: ptr("[ship]\ndeploy_timeout = \"soon\"\n"), errHas: []string{FileName, "deploy_timeout"}},
		{name: "zero deploy_timeout", committed: ptr("[ship]\ndeploy_timeout = \"0s\"\n"), errHas: []string{FileName, "deploy_timeout"}},
		{name: "negative deploy_timeout", committed: ptr("[ship]\ndeploy_timeout = \"-5m\"\n"), errHas: []string{FileName, "deploy_timeout"}},
		{name: "health_url not http", committed: ptr("[ship]\nhealth_url = \"ftp://user:hunter2@x.example/h\"\n"), errHas: []string{FileName, "health_url"}},
		{name: "health_url no host", committed: ptr("[ship]\nhealth_url = \"https://\"\n"), errHas: []string{FileName, "health_url"}},
		{name: "pattern does not compile", committed: ptr("[ship]\nrequired_env_pattern = \"([\"\n"), errHas: []string{FileName, "required_env_pattern", "regular expression"}},
		{name: "pattern does not compile in local names local", committed: ptr("[ship]\nbase = \"main\"\n"), local: ptr("[ship]\nrequired_env_pattern = \"(\"\n"), errHas: []string{LocalFileName, "required_env_pattern"}},
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
			if len(tt.errHas) > 0 {
				if err == nil {
					t.Fatalf("Load = %+v, want an error", got)
				}
				t.Log(err)
				for _, h := range tt.errHas {
					if !strings.Contains(err.Error(), h) {
						t.Errorf("error %q lacks %q", err, h)
					}
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
			if got.Ship != tt.want {
				t.Errorf("Ship = %+v, want %+v", got.Ship, tt.want)
			}
		})
	}
}

// TestShipEnvPattern: nil for an empty pattern (the common case, which the
// ship step must read as "no check"), a working matcher otherwise.
func TestShipEnvPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		nilWant bool
		match   string
		matches bool
	}{
		{name: "empty is nil", pattern: "", nilWant: true},
		{name: "matches an added line", pattern: "^\\+NEBARI_[A-Z]+=", match: "+NEBARI_KEY=1", matches: true},
		{name: "does not match", pattern: "^\\+NEBARI_[A-Z]+=", match: "+OTHER=1", matches: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re := Ship{RequiredEnvPattern: tt.pattern}.EnvPattern()
			if tt.nilWant {
				if re != nil {
					t.Fatalf("EnvPattern = %v, want nil", re)
				}
				return
			}
			if re == nil {
				t.Fatal("EnvPattern = nil, want a matcher")
			}
			if re.MatchString(tt.match) != tt.matches {
				t.Errorf("MatchString(%q) = %v, want %v", tt.match, !tt.matches, tt.matches)
			}
		})
	}
}

// TestLoadPlanning covers [planning] via_pr. Load-bearing rows: absent is
// false (most projects write plans straight to the checkout), the local file
// beating the committed one with false (the owner's way to turn it off on one
// machine), and the two error rows (each must name the file and the key and
// say how to fix it, so a hobbyist who wrote [plan] or "yes" knows what to do).
func TestLoadPlanning(t *testing.T) {
	tests := []struct {
		name      string
		committed *string
		local     *string
		want      bool
		errHas    []string
	}{
		{name: "via_pr absent is false", committed: ptr("[ship]\nbase = \"main\"\n"), want: false},
		{name: "via_pr true in afk.toml", committed: ptr("[planning]\nvia_pr = true\n"), want: true},
		{name: "via_pr false in local over true", committed: ptr("[planning]\nvia_pr = true\n"), local: ptr("[planning]\nvia_pr = false\n"), want: false},
		{name: "via_pr string is an error naming the key", committed: ptr("[planning]\nvia_pr = \"yes\"\n"), errHas: []string{FileName, "[planning] via_pr", "true or false"}},
		{name: "plan table gives the new advice", committed: ptr("[plan]\nvia_pr = true\n"), errHas: []string{FileName, "path of the master plan", "[planning] via_pr"}},
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
			if len(tt.errHas) > 0 {
				if err == nil {
					t.Fatalf("Load = %+v, want an error", got)
				}
				t.Log(err)
				for _, h := range tt.errHas {
					if !strings.Contains(err.Error(), h) {
						t.Errorf("error %q lacks %q", err, h)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.Planning.ViaPR != tt.want {
				t.Errorf("Planning.ViaPR = %v, want %v", got.Planning.ViaPR, tt.want)
			}
		})
	}
}
