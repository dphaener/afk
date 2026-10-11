package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// example is the afk.toml from docs/PLAN.md section 4.6, verbatim apart from
// the ntfy_url line, which each row fills in.
const example = `plan = "docs/PLAN.md"

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

// TestLoad is table-driven over file contents. Load-bearing rows: the
// verbatim section 4.6 example (the other keys and the multi-line notes must
// not break the reader), the rejections (each must name afk.toml and must not
// echo the password in the URL), and the missing file (no error, because most
// projects have none).
func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil means no file
		want    string
		errHas  []string
	}{
		{name: "missing file", content: nil, want: ""},
		{name: "empty file", content: ptr(""), want: ""},
		{name: "plan example with empty url", content: ptr(sprintf(example, `""`)), want: ""},
		{name: "plan example with url", content: ptr(sprintf(example, `"https://ntfy.example/afk-nebari"`)), want: "https://ntfy.example/afk-nebari"},
		{name: "surrounding spaces trimmed", content: ptr("[notify]\nntfy_url = \"  https://ntfy.sh/t  \"\n"), want: "https://ntfy.sh/t"},
		{name: "http allowed", content: ptr("[notify]\nntfy_url = \"http://localhost:8080/t\"\n"), want: "http://localhost:8080/t"},
		{name: "ftp scheme", content: ptr("[notify]\nntfy_url = \"ftp://user:hunter2@ntfy.sh/t\"\n"), errHas: []string{"afk.toml", "[notify] ntfy_url"}},
		{name: "no host", content: ptr("[notify]\nntfy_url = \"https:///t\"\n"), errHas: []string{"afk.toml", "[notify] ntfy_url"}},
		{name: "no scheme", content: ptr("[notify]\nntfy_url = \"ntfy.sh/t\"\n"), errHas: []string{"afk.toml", "[notify] ntfy_url"}},
		{name: "unparseable url", content: ptr("[notify]\nntfy_url = \"http://user:hunter2@ntfy.sh:bad/t\"\n"), errHas: []string{"afk.toml", "[notify] ntfy_url"}},
		{name: "wrong type", content: ptr("[notify]\nntfy_url = 5\n"), errHas: []string{"afk.toml"}},
		{name: "broken toml", content: ptr("[notify\nntfy_url = \n"), errHas: []string{"afk.toml"}},
		{name: "unknown tables ignored", content: ptr("[mystery]\nx = 1\n[notify]\nother = true\nntfy_url = \"https://ntfy.sh/t\"\n[more.nested]\ny = [1, 2]\n"), want: "https://ntfy.sh/t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.content != nil {
				if err := os.WriteFile(filepath.Join(root, "afk.toml"), []byte(*tt.content), 0o644); err != nil {
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

func ptr(s string) *string { return &s }

func sprintf(format, v string) string { return strings.Replace(format, "%s", v, 1) }
