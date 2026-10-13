package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const lintCmdClean = `# Demo

**How to use this document.** Read it.

## Context

x

## Decisions

x

## Milestones

### M1 — One

#### T1.1 — First

- **Goal:** g
- **Scope:** s
- **Acceptance:** a
- **Depends on:** none

## Deferred

x

## Open questions

| # | Question | Needed by |
|---|----------|-----------|

## Definition of done

x
`

// lintCmdWarn has no task fields, so it gives warnings and no errors.
const lintCmdWarn = `# Demo

#### T1.1 — First

- **Depends on:** none
`

// lintCmdBad depends on a task that does not exist (line 3).
const lintCmdBad = `# Demo

#### T1.1 — First

- **Depends on:** T99.9
`

func lintProject(t *testing.T, plan string) string {
	t.Helper()
	p := t.TempDir()
	if err := os.MkdirAll(filepath.Join(p, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "docs", "PLAN.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runLint(args []string, cwd string) (code int, stdout, stderr string) {
	var so, se bytes.Buffer
	code = runPlanLint(args, &so, &se, cwd, "", fakeCommonDir(""))
	return code, so.String(), se.String()
}

// TestPlanLintExitCodes pins the exit code and the key words of each path.
//
// Load-bearing rows: "unknown dependency" (an error fails the command and the
// line carries task, line number and rule), "warnings only" (warnings must
// not fail it), "file and project" and "two files" (bad usage is 2, not a
// silent pick), "missing plan" (names the path), "afk.toml plan" (the
// configured plan is honoured, as afk tasks does).
func TestPlanLintExitCodes(t *testing.T) {
	tests := []struct {
		name       string
		plan       string
		toml       string
		extraFile  string // written as other.md in the project
		args       func(p string) []string
		cwd        func(p string) string
		want       int
		wantOut    []string
		wantErr    []string
		removePlan bool
	}{
		{name: "clean by project", plan: lintCmdClean, args: func(p string) []string { return []string{"--project", p} },
			want: 0, wantOut: []string{"docs/PLAN.md: no problems found"}},
		{name: "warnings only", plan: lintCmdWarn, args: func(p string) []string { return []string{"--project", p} },
			want: 0, wantOut: []string{"warning:", "0 errors, "}},
		{name: "unknown dependency", plan: lintCmdBad, args: func(p string) []string { return []string{"--project", p} },
			want: 1, wantOut: []string{":3: error: T1.1: ", "[unknown-dependency]", "1 error, "}},
		{name: "file given", plan: lintCmdClean, extraFile: lintCmdClean, args: func(p string) []string { return []string{filepath.Join(p, "other.md")} },
			want: 0, wantOut: []string{"other.md: no problems found"}},
		{name: "file relative to cwd", plan: lintCmdClean, extraFile: lintCmdBad, args: func(string) []string { return []string{"other.md"} },
			cwd: func(p string) string { return p }, want: 1, wantOut: []string{"other.md:3: error:"}},
		{name: "file and project", plan: lintCmdClean, args: func(p string) []string { return []string{"--project", p, "x.md"} },
			want: 2, wantErr: []string{"afk plan lint: give a file or --project, not both"}},
		{name: "two files", plan: lintCmdClean, args: func(string) []string { return []string{"a", "b"} },
			want: 2, wantErr: []string{"afk plan lint:", "Usage:"}},
		{name: "unknown flag", plan: lintCmdClean, args: func(string) []string { return []string{"--nope"} },
			want: 2, wantErr: []string{"nope"}},
		{name: "missing plan", removePlan: true, args: func(p string) []string { return []string{"--project", p} },
			want: 1, wantErr: []string{"no master plan at ", filepath.Join("docs", "PLAN.md")}},
		{name: "broken afk.toml", plan: lintCmdClean, toml: "plan = [", args: func(p string) []string { return []string{"--project", p} },
			want: 1, wantErr: []string{"afk.toml"}},
		{name: "afk.toml plan", plan: lintCmdBad, toml: "plan = \"PLAN.md\"\n", args: func(p string) []string { return []string{"--project", p} },
			want: 1, wantOut: []string{"PLAN.md:3: error:"}},
		{name: "help", plan: lintCmdClean, args: func(string) []string { return []string{"--help"} },
			want: 0, wantErr: []string{"afk plan lint [--project DIR]"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := lintProject(t, tc.plan)
			if tc.toml != "" {
				if err := os.WriteFile(filepath.Join(p, "afk.toml"), []byte(tc.toml), 0o644); err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(tc.toml, "plan = \"PLAN.md\"") {
					if err := os.WriteFile(filepath.Join(p, "PLAN.md"), []byte(tc.plan), 0o644); err != nil {
						t.Fatal(err)
					}
					os.Remove(filepath.Join(p, "docs", "PLAN.md"))
				}
			}
			if tc.extraFile != "" {
				if err := os.WriteFile(filepath.Join(p, "other.md"), []byte(tc.extraFile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.removePlan {
				os.Remove(filepath.Join(p, "docs", "PLAN.md"))
			}
			cwd := p
			if tc.cwd != nil {
				cwd = tc.cwd(p)
			}
			code, out, errOut := runLint(tc.args(p), cwd)
			if code != tc.want {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.want, out, errOut)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out, w) {
					t.Errorf("stdout lacks %q:\n%s", w, out)
				}
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(errOut, w) {
					t.Errorf("stderr lacks %q:\n%s", w, errOut)
				}
			}
		})
	}
}

// TestPlanLintSummary pins the six lines in order, and that an error plan
// still prints the counts while exiting 1.
func TestPlanLintSummary(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		p := lintProject(t, lintCmdClean)
		code, out, _ := runLint([]string{"--project", p, "--summary"}, p)
		want := "Milestones: 1\nTasks: 1\nOwner tasks: 0\nOpen questions: 0\nErrors: 0\nWarnings: 0\n"
		if code != 0 || out != want {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
	})
	t.Run("with an error", func(t *testing.T) {
		p := lintProject(t, lintCmdBad)
		code, out, _ := runLint([]string{"--project", p, "--summary"}, p)
		if code != 1 || !strings.Contains(out, "Tasks: 1\n") || !strings.Contains(out, "Errors: 1\n") || strings.Contains(out, "unknown-dependency") {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
	})
}

// TestPlanLintJSON: findings is [] and not null for a clean plan, the field
// names are the contract, and --summary does not change the shape.
func TestPlanLintJSON(t *testing.T) {
	p := lintProject(t, lintCmdClean)
	code, out, _ := runLint([]string{"--project", p, "--json"}, p)
	if code != 0 || !strings.HasSuffix(out, "}\n") || !strings.Contains(out, `"findings": []`) {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	var sum map[string]int
	if err := json.Unmarshal(got["summary"], &sum); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"milestones", "tasks", "owner_tasks", "open_questions", "errors", "warnings"} {
		if _, ok := sum[k]; !ok {
			t.Errorf("summary lacks %q", k)
		}
	}

	p = lintProject(t, lintCmdBad)
	code, out, _ = runLint([]string{"--project", p, "--json", "--summary"}, p)
	var rep struct {
		File     string                       `json:"file"`
		Findings []map[string]json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if code != 1 || len(rep.Findings) == 0 || rep.File == "" {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	for _, k := range []string{"level", "line", "task", "rule", "message"} {
		if _, ok := rep.Findings[0][k]; !ok {
			t.Errorf("finding lacks %q", k)
		}
	}
}

// TestPlanLintSummaryMatchesTasks is T5.1's acceptance and is load-bearing:
// the lint's task count and afk tasks' row count are read by different code,
// and they must agree on the repository's own plan and on the nebari plan.
func TestPlanLintSummaryMatchesTasks(t *testing.T) {
	for _, src := range []string{
		filepath.Join("docs", "PLAN.md"),
		filepath.Join("internal", "masterplan", "testdata", "nebari-plan.md"),
	} {
		t.Run(filepath.Base(src), func(t *testing.T) {
			data, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			p := lintProject(t, string(data))

			var so, se bytes.Buffer
			if code := runTasks([]string{"--project", p}, &so, &se, p, "", fakeCommonDir("")); code != 0 {
				t.Fatalf("tasks exit %d: %s", code, se.String())
			}
			rows := 0
			for i, line := range strings.Split(so.String(), "\n") {
				f := strings.Fields(line)
				if i > 0 && len(f) > 0 && len(f[0]) > 1 && f[0][0] == 'T' && f[0][1] >= '0' && f[0][1] <= '9' {
					rows++
				}
			}

			code, out, errOut := runLint([]string{"--project", p, "--summary"}, p)
			if code != 0 {
				t.Fatalf("lint exit %d: %s%s", code, out, errOut)
			}
			if want := fmt.Sprintf("Tasks: %d\n", rows); !strings.Contains(out, want) {
				t.Errorf("want %q in:\n%s", want, out)
			}
			if rows == 0 {
				t.Error("counted no task rows")
			}
		})
	}
}
