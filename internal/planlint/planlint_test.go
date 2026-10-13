package planlint

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/masterplan"
)

var update = flag.Bool("update", false, "rewrite testdata/nebari-findings.txt from the checker's output")

// base is a small master plan the checker has nothing to say about. Every
// row below is this plan with one thing changed, so a row's finding can only
// come from that change.
const base = `# Plan

**How to use this document.** Read Context first.

## Context

The owner asked: "build it".

## Decisions

| Topic | Decision |
|---|---|
| Language | Go |

## 8. Milestones

### M1 — One

**Outcome:** it starts.

#### T1.1 — First
- **Depends on:** —
- **Goal:** g
- **Scope:** s
- **Acceptance:** a

#### T1.2 — Second
- **Depends on:** T1.1
- **Goal:** g
- **Scope:** s
- **Acceptance:** a

## Deferred

- Nothing.

## Open questions

| Question | Needed by |
|---|---|
| Which colour? | T1.2 |

## Definition of done

- Tests pass.
`

// swap returns base with the one occurrence of old replaced by new. It
// panics when old is not there exactly once, so a row cannot silently test
// the unchanged base after base is edited.
func swap(old, new string) string {
	if strings.Count(base, old) != 1 {
		panic(fmt.Sprintf("swap: %q occurs %d times in base, want 1", old, strings.Count(base, old)))
	}
	return strings.Replace(base, old, new, 1)
}

// lineOf returns the 1-based line of the first line of plan that starts with
// prefix, so a row names the line it expects by its text and not by a number
// that goes stale when base changes. An empty prefix means line 0, a finding
// about the whole file.
func lineOf(t *testing.T, plan, prefix string) int {
	t.Helper()
	if prefix == "" {
		return 0
	}
	for i, l := range strings.Split(plan, "\n") {
		if strings.HasPrefix(l, prefix) {
			return i + 1
		}
	}
	t.Fatalf("no line of the plan starts with %q", prefix)
	return 0
}

const t12Block = "#### T1.2 — Second\n- **Depends on:** T1.1\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n"

type ruleRow struct {
	rule  string
	level string
	// bad makes the rule fire; good is the same plan with only the fix.
	bad, good string
	// at is the start of the line the finding must point at ("" for 0).
	at   string
	task string
	// parser marks the six rules the daemon's parser also enforces, for
	// TestLintAgreesWithParser.
	parser bool
}

func ruleRows() []ruleRow {
	noTasks := "# Plan\n\n**How to use this document.** Read it.\n\n## Context\n\n## Decisions\n\n## Milestones\n\n" +
		"%s## Deferred\n\n## Open questions\n\n## Definition of done\n"
	return []ruleRow{
		{
			rule: RuleNoTasks, level: LevelError, parser: true,
			bad:  fmt.Sprintf(noTasks, ""),
			good: fmt.Sprintf(noTasks, "#### T1.1 — First\n- **Depends on:** —\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n\n"),
		},
		{
			rule: RuleBadTaskHeading, level: LevelError, parser: true,
			bad:  swap("#### T1.2 — Second", "#### T1.2 Second"),
			good: base, at: "#### T1.2 Second",
		},
		{
			rule: RuleDuplicateTask, level: LevelError, parser: true,
			bad:  swap("#### T1.2 — Second", "#### T1.1 — Second"),
			good: base, at: "#### T1.1 — Second", task: "T1.1",
		},
		{
			rule: RuleNoDependsLine, level: LevelError, parser: true,
			bad:  swap("- **Depends on:** T1.1\n", ""),
			good: base, at: "#### T1.2", task: "T1.2",
		},
		{
			rule: RuleUnknownDependency, level: LevelError, parser: true,
			bad:  swap("- **Depends on:** T1.1\n", "- **Depends on:** T9.9\n"),
			good: base, at: "#### T1.2", task: "T1.2",
		},
		{
			rule: RuleDependencyCycle, level: LevelError, parser: true,
			bad:  swap("- **Depends on:** —\n", "- **Depends on:** T1.2\n"),
			good: base, at: "#### T1.1", task: "T1.1",
		},
		{
			rule: RuleOpenQuestionUnknownTask, level: LevelError,
			bad:  swap("| Which colour? | T1.2 |", "| Which colour? | T9.9 |"),
			good: base, at: "| Which colour?", task: "T9.9",
		},
		{
			rule: RuleDependsNoTaskID, level: LevelWarning,
			bad:  swap("- **Depends on:** T1.1\n", "- **Depends on:** M1 as available\n"),
			good: base, at: "#### T1.2", task: "T1.2",
		},
		{
			rule: RuleMissingTaskField, level: LevelWarning,
			bad:  swap("- **Depends on:** T1.1\n- **Goal:** g\n", "- **Depends on:** T1.1\n"),
			good: base, at: "#### T1.2", task: "T1.2",
		},
		{
			rule: RuleMissingSection, level: LevelWarning,
			bad:  swap("## Deferred\n", "## Later\n"),
			good: base,
		},
		{
			rule: RuleOpenQuestionNoTask, level: LevelWarning,
			bad:  swap("| Which colour? | T1.2 |", "| Which colour? | before release |"),
			good: base, at: "| Which colour?",
		},
	}
}

func findingsOf(r Report, rule string) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// render writes findings one per line as "line level task rule", with "-"
// for no task so the columns stay put.
func render(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		task := f.Task
		if task == "" {
			task = "-"
		}
		out = append(out, fmt.Sprintf("%d %s %s %s", f.Line, f.Level, task, f.Rule))
	}
	return out
}

// TestLintRules has, for each of the eleven rules, a plan on which the rule
// fires and the same plan with only the fix, on which it does not. Every row
// is load-bearing in the same way: the "fails" half pins the level, line and
// task a caller will print, and the "passes" half is what stops a rule from
// firing on a correct plan (a checker that cries wolf gets ignored). The
// first sub-test, that the base plan is clean, is what makes the "fails"
// halves mean anything: each differs from base by one edit.
func TestLintRules(t *testing.T) {
	t.Run("base plan is clean", func(t *testing.T) {
		r := Lint([]byte(base))
		if len(r.Findings) != 0 {
			t.Fatalf("base plan has findings: %v", render(r.Findings))
		}
		if r.Findings == nil {
			t.Fatal("Findings is nil for a clean plan; it must be an empty slice so JSON shows []")
		}
	})

	rows := ruleRows()
	covered := map[string]bool{}
	for _, row := range rows {
		covered[row.rule] = true
		t.Run(row.rule+"/fails", func(t *testing.T) {
			got := findingsOf(Lint([]byte(row.bad)), row.rule)
			if len(got) != 1 {
				t.Fatalf("got %d %s findings, want 1: %v", len(got), row.rule, got)
			}
			f := got[0]
			if f.Level != row.level || f.Line != lineOf(t, row.bad, row.at) || f.Task != row.task {
				t.Errorf("got level %q line %d task %q, want level %q line %d task %q",
					f.Level, f.Line, f.Task, row.level, lineOf(t, row.bad, row.at), row.task)
			}
			if strings.TrimSpace(f.Message) == "" {
				t.Error("the finding has no message")
			}
		})
		t.Run(row.rule+"/passes", func(t *testing.T) {
			if got := findingsOf(Lint([]byte(row.good)), row.rule); len(got) != 0 {
				t.Errorf("%s fired on the fixed plan: %v", row.rule, got)
			}
		})
	}
	for _, rule := range Rules {
		if !covered[rule] {
			t.Errorf("rule %s has no row", rule)
		}
	}
	if len(Rules) != 11 {
		t.Errorf("len(Rules) = %d, want 11", len(Rules))
	}
}

// TestLintAgreesWithParser holds the checker to the daemon: where one of the
// six parser-backed rules fires, masterplan.Parse then Validate must refuse
// the plan too, and on the fixed plan neither may. Without it the checker
// could pass a plan the daemon refuses, which is the one failure the package
// exists to prevent.
func TestLintAgreesWithParser(t *testing.T) {
	refused := func(plan string) error {
		tasks, err := masterplan.Parse([]byte(plan))
		if err != nil {
			return err
		}
		return masterplan.Validate(tasks)
	}
	n := 0
	for _, row := range ruleRows() {
		if !row.parser {
			continue
		}
		n++
		t.Run(row.rule, func(t *testing.T) {
			if err := refused(row.bad); err == nil {
				t.Errorf("the checker reports %s but the parser accepts the plan", row.rule)
			}
			if err := refused(row.good); err != nil {
				t.Errorf("the parser refuses the fixed plan: %v", err)
			}
			if got := Lint([]byte(row.good)).Summary.Errors; got != 0 {
				t.Errorf("the checker reports %d errors on the fixed plan", got)
			}
		})
	}
	if n != 6 {
		t.Errorf("%d parser-backed rows, want 6", n)
	}
}

// TestLintCases covers the shapes the rule rows do not, each compared
// against the complete list of findings so an extra finding fails the row as
// surely as a missing one. Load-bearing rows:
//   - "three hashes" and "lower-case t": headings the daemon reads as plain
//     headings, so the task would silently never run (assumption A2);
//   - "a heading that only starts with T": the look-alike rule must not fire
//     on ordinary prose headings, or both real plans would show errors;
//   - "fenced examples": a plan that documents its own format must stay
//     clean;
//   - "answered row" and "answer on its own row": the **Answered rule is the
//     one T5.6 will hold tasks by, so it must be exact in both directions;
//   - "escaped pipe and backtick span": a wrong cell split would move
//     "Needed by" onto the question text;
//   - "sorted": callers print the findings in the order returned.
func TestLintCases(t *testing.T) {
	type want struct {
		at, level, task, rule string
	}
	tests := []struct {
		name string
		plan string
		want []want
	}{
		{
			name: "two errors of different kinds",
			plan: swap(t12Block, "#### T1.2 — Second\n- **Depends on:** T9.9\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n\n#### T1.1 — Again\n- **Depends on:** —\n"),
			want: []want{
				{"#### T1.2", LevelError, "T1.2", RuleUnknownDependency},
				{"#### T1.1 — Again", LevelError, "T1.1", RuleDuplicateTask},
			},
		},
		{
			name: "every parser problem is reported, not only the first",
			plan: swap(t12Block, "#### T1.2 Second\n\n#### T1.1 — Again\n- **Depends on:** —\n\n#### T1.3 — Third\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n"),
			want: []want{
				{"#### T1.2 Second", LevelError, "", RuleBadTaskHeading},
				{"#### T1.1 — Again", LevelError, "T1.1", RuleDuplicateTask},
				{"#### T1.3", LevelError, "T1.3", RuleNoDependsLine},
				{"| Which colour?", LevelError, "T1.2", RuleOpenQuestionUnknownTask},
			},
		},
		{
			name: "three hashes",
			plan: swap("\n## Deferred", "\n### T1.3 — x\n\n## Deferred"),
			want: []want{{"### T1.3 — x", LevelError, "", RuleBadTaskHeading}},
		},
		{
			name: "five hashes",
			plan: swap("\n## Deferred", "\n##### T1.3 — x\n\n## Deferred"),
			want: []want{{"##### T1.3 — x", LevelError, "", RuleBadTaskHeading}},
		},
		{
			name: "lower-case t",
			plan: swap("\n## Deferred", "\n#### t1.3 — x\n\n## Deferred"),
			want: []want{{"#### t1.3 — x", LevelError, "", RuleBadTaskHeading}},
		},
		{
			name: "two spaces after the hashes",
			plan: swap("\n## Deferred", "\n####  T1.3 — x\n\n## Deferred"),
			want: []want{{"####  T1.3 — x", LevelError, "", RuleBadTaskHeading}},
		},
		{
			name: "a heading that only starts with T",
			plan: swap("\n## Deferred", "\n### Three layers\n\n### Tasks\n\n##### T is for task\n\n## Deferred"),
		},
		{
			name: "fenced examples",
			plan: swap("- **Depends on:** T1.1\n- **Goal:** g\n",
				"- **Depends on:** T1.1\n\n```\n#### T9.9 — Example\n### T9.8 — Wrong level\n- **Depends on:** T7.7\n- **Goal:** fenced\n```\n"),
			want: []want{{"#### T1.2", LevelWarning, "T1.2", RuleMissingTaskField}},
		},
		{
			name: "all three fields missing is one warning",
			plan: swap("- **Depends on:** T1.1\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n", "- **Depends on:** T1.1\n"),
			want: []want{{"#### T1.2", LevelWarning, "T1.2", RuleMissingTaskField}},
		},
		{
			name: "a field label must be exact",
			plan: swap("- **Depends on:** T1.1\n- **Goal:** g\n", "- **Depends on:** T1.1\n- **Goal.** g\n"),
			want: []want{{"#### T1.2", LevelWarning, "T1.2", RuleMissingTaskField}},
		},
		{
			name: "a field under the next heading belongs to nobody",
			plan: swap("- **Depends on:** T1.1\n- **Goal:** g\n", "- **Depends on:** T1.1\n\n### Notes\n- **Goal:** g\n"),
			want: []want{{"#### T1.2", LevelWarning, "T1.2", RuleMissingTaskField}},
		},
		{
			name: "after T1.2 names T1.2",
			plan: swap("| Which colour? | T1.2 |", "| Which colour? | after T1.2 |"),
		},
		{
			name: "after an unknown task names it",
			plan: swap("| Which colour? | T1.2 |", "| Which colour? | after T2.4 |"),
			want: []want{{"| Which colour?", LevelError, "T2.4", RuleOpenQuestionUnknownTask}},
		},
		{
			name: "an id inside a longer word is not an id",
			plan: swap("| Which colour? | T1.2 |", "| Which colour? | XT1.2 and T1.2bc |"),
			want: []want{{"| Which colour?", LevelWarning, "", RuleOpenQuestionNoTask}},
		},
		{
			name: "one known and two unknown ids",
			plan: swap("| Which colour? | T1.2 |", "| Which colour? | T1.2, T7.1, T7.2a, T7.1 |"),
			want: []want{
				{"| Which colour?", LevelError, "T7.1", RuleOpenQuestionUnknownTask},
				{"| Which colour?", LevelError, "T7.2a", RuleOpenQuestionUnknownTask},
			},
		},
		{
			name: "answered row naming an unknown task",
			plan: swap("| Which colour? | T1.2 |", "| Which colour? **Answered 2026-10-14:** blue. | T9.9 |"),
		},
		{
			name: "answered row naming no task",
			plan: swap("| Which colour? | T1.2 |", "| **Answered 2026-10-14:** blue. | before release |"),
		},
		{
			name: "answer on its own row leaves the question open",
			plan: swap("| Which colour? | T1.2 |", "| Which colour? | T9.9 |\n| **Answered 2026-10-14:** blue. | T9.9 |"),
			want: []want{{"| Which colour?", LevelError, "T9.9", RuleOpenQuestionUnknownTask}},
		},
		{
			name: "done is not answered",
			plan: swap("| Which colour? | T1.2 |", "| Which colour? **Done 2026-10-14:** blue. | T9.9 |"),
			want: []want{{"| Which colour?", LevelError, "T9.9", RuleOpenQuestionUnknownTask}},
		},
		{
			name: "escaped pipe and backtick span",
			plan: swap("| Which colour? | T1.2 |", "| Is `a | b` or c \\| d right, T8.8? | `T9.9` |"),
			want: []want{{"| Is `a", LevelError, "T9.9", RuleOpenQuestionUnknownTask}},
		},
		{
			name: "a stray backtick does not swallow the row",
			plan: swap("| Which colour? | T1.2 |", "| Why ` here? | T9.9 |"),
			want: []want{{"| Why", LevelError, "T9.9", RuleOpenQuestionUnknownTask}},
		},
		{
			name: "columns in the other order, aligned separator",
			plan: swap("| Question | Needed by |\n|---|---|\n| Which colour? | T1.2 |",
				"| needed by | Owner | Question |\n|:---|:-:|---:|\n| T9.9 | me | Which colour? |\n| T9.8 | me | **Answered 2026-10-14:** blue. |"),
			want: []want{{"| T9.9", LevelError, "T9.9", RuleOpenQuestionUnknownTask}},
		},
		{
			name: "a table without Needed by is not the table",
			plan: swap("| Question | Needed by |\n|---|---|\n| Which colour? | T1.2 |",
				"| Term | Meaning |\n|---|---|\n| T9.9 | nothing |\n\n| Question | Needed by |\n|---|---|\n| Which colour? | T9.7 |"),
			want: []want{{"| Which colour?", LevelError, "T9.7", RuleOpenQuestionUnknownTask}},
		},
		{
			name: "no table gives no open-question finding",
			plan: swap("| Question | Needed by |\n|---|---|\n| Which colour? | T1.2 |", "None, but see T9.9."),
		},
		{
			name: "a table in another section is not read",
			plan: swap("- Nothing.", "| Question | Needed by |\n|---|---|\n| Later? | T9.9 |"),
		},
		{
			name: "a fenced table is not read",
			plan: swap("| Question | Needed by |\n|---|---|\n| Which colour? | T1.2 |",
				"```\n| Question | Needed by |\n|---|---|\n| Which colour? | T9.9 |\n```"),
		},
		{
			name: "numbered section title with extra words",
			plan: swap("## Deferred\n", "## 9. Deferred / later\n"),
		},
		{
			name: "section names ignore case",
			plan: swap("## Open questions\n", "## 10. OPEN QUESTIONS to resolve before the relevant task\n"),
		},
		{
			name: "a level 3 heading is not the section",
			plan: swap("## Deferred\n", "### Deferred\n"),
			want: []want{{"", LevelWarning, "", RuleMissingSection}},
		},
		{
			name: "how to use after the first section",
			plan: swap("**How to use this document.** Read Context first.\n\n## Context\n", "## Context\n\n**How to use this document.** Read Context first.\n"),
			want: []want{{"", LevelWarning, "", RuleMissingSection}},
		},
		{
			name: "how to use in a fence",
			plan: swap("**How to use this document.** Read Context first.\n", "```\n**How to use this document.** Read Context first.\n```\n"),
			want: []want{{"", LevelWarning, "", RuleMissingSection}},
		},
		{
			name: "how to use ignores case",
			plan: swap("**How to use this document.**", "**HOW TO USE this plan.**"),
		},
		{
			name: "an empty file",
			plan: "",
			want: []want{
				{"", LevelError, "", RuleNoTasks},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
			},
		},
		{
			name: "only a malformed heading is not no-tasks",
			plan: "#### T1.1 Missing dash\n",
			want: []want{
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"", LevelWarning, "", RuleMissingSection},
				{"#### T1.1", LevelError, "", RuleBadTaskHeading},
			},
		},
		{
			name: "CRLF line endings",
			plan: strings.ReplaceAll(base, "\n", "\r\n"),
		},
		{
			name: "a cycle with an unknown dependency beside it",
			plan: swap("- **Depends on:** —\n", "- **Depends on:** T9.9, T1.2\n"),
			want: []want{
				{"#### T1.1", LevelError, "T1.1", RuleDependencyCycle},
				{"#### T1.1", LevelError, "T1.1", RuleUnknownDependency},
			},
		},
		{
			name: "sorted: line, then errors first, then rule",
			plan: swap(t12Block, "#### T1.2 — Second\n- **Depends on:** T9.9\n- **Goal:** g\n\n#### T1.3 — Third\n- **Depends on:** soon\n"),
			want: []want{
				{"", LevelWarning, "", RuleMissingSection}, // filled in below
				{"#### T1.2", LevelError, "T1.2", RuleUnknownDependency},
				{"#### T1.2", LevelWarning, "T1.2", RuleMissingTaskField},
				{"#### T1.3", LevelWarning, "T1.3", RuleDependsNoTaskID},
				{"#### T1.3", LevelWarning, "T1.3", RuleMissingTaskField},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := tt.plan
			if strings.HasPrefix(tt.name, "sorted") {
				// Drop a section too, so the list starts with a line 0 row.
				plan = strings.Replace(plan, "## Deferred\n", "## Later\n", 1)
			}
			want := []string{}
			for _, w := range tt.want {
				task := w.task
				if task == "" {
					task = "-"
				}
				want = append(want, fmt.Sprintf("%d %s %s %s", lineOf(t, plan, w.at), w.level, task, w.rule))
			}
			r := Lint([]byte(plan))
			got := render(r.Findings)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("findings:\n got  %q\n want %q", got, want)
			}
			if !sort.SliceIsSorted(r.Findings, func(i, j int) bool {
				a, b := r.Findings[i], r.Findings[j]
				if a.Line != b.Line {
					return a.Line < b.Line
				}
				if a.Level != b.Level {
					return a.Level == LevelError
				}
				return a.Rule < b.Rule
			}) {
				t.Errorf("findings are not sorted: %q", got)
			}
			if again := render(Lint([]byte(plan)).Findings); !reflect.DeepEqual(again, got) {
				t.Errorf("a second run differs:\n first  %q\n second %q", got, again)
			}
		})
	}
}

// TestLintMessages pins the parts of a message a person acts on. Load-bearing
// rows: the cycle path (the only place the loop is spelled out), the
// "exactly four" hint (the fix differs from the other malformed headings),
// and the quoted Depends on text (the reader has to see what afk ignored).
func TestLintMessages(t *testing.T) {
	tests := []struct {
		name string
		plan string
		rule string
		want []string
	}{
		{
			name: "unknown dependency says what to do",
			plan: swap("- **Depends on:** T1.1\n", "- **Depends on:** T9.9\n"),
			rule: RuleUnknownDependency,
			want: []string{"T1.2 depends on T9.9, which is not a task in this plan. Fix the id or add the task."},
		},
		{
			name: "cycle shows the path",
			plan: swap("- **Depends on:** —\n", "- **Depends on:** T1.2\n"),
			rule: RuleDependencyCycle,
			want: []string{"T1.1 -> T1.2 -> T1.1"},
		},
		{
			name: "wrong number of hashes",
			plan: swap("\n## Deferred", "\n### T1.3 — x\n\n## Deferred"),
			rule: RuleBadTaskHeading,
			want: []string{"exactly four", "3 hashes"},
		},
		{
			name: "lower-case t is not a hash problem",
			plan: swap("\n## Deferred", "\n#### t1.3 — x\n\n## Deferred"),
			rule: RuleBadTaskHeading,
			want: []string{"capital T"},
		},
		{
			name: "duplicate names the first line",
			plan: swap("#### T1.2 — Second", "#### T1.1 — Second"),
			rule: RuleDuplicateTask,
			want: []string{"T1.1 is already a task on line 21"},
		},
		{
			name: "prose dependency is quoted",
			plan: swap("- **Depends on:** T1.1\n", "- **Depends on:** M1 as available\n"),
			rule: RuleDependsNoTaskID,
			want: []string{`"M1 as available"`, "no dependencies"},
		},
		{
			name: "missing fields are all named",
			plan: swap("- **Depends on:** T1.1\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n", "- **Depends on:** T1.1\n- **Scope:** s\n"),
			rule: RuleMissingTaskField,
			want: []string{"T1.2 has no Goal or Acceptance line"},
		},
		{
			name: "missing section is named",
			plan: swap("## Definition of done\n", "## Done\n"),
			rule: RuleMissingSection,
			want: []string{`"## Definition of done"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findingsOf(Lint([]byte(tt.plan)), tt.rule)
			if len(got) != 1 {
				t.Fatalf("got %d %s findings, want 1: %v", len(got), tt.rule, got)
			}
			if strings.Contains(tt.name, "lower-case") && strings.Contains(got[0].Message, "exactly four") {
				t.Errorf("message blames the hashes: %q", got[0].Message)
			}
			for _, w := range tt.want {
				if !strings.Contains(got[0].Message, w) {
					t.Errorf("message %q does not contain %q", got[0].Message, w)
				}
			}
		})
	}
}

// TestLintSummary checks the counts afk plan lint --summary prints.
// Load-bearing: "(owner + agent)" is not an owner task (the daemon starts an
// agent for it), a "### M" heading without a dash and title is not a
// milestone, and an answered row is not an open question.
func TestLintSummary(t *testing.T) {
	plan := `# Plan

**How to use this document.** Read it.

## Context

## Decisions

## Milestones

### M1 — One

#### T1.1 — Set up the server (owner; not an agent task)
- **Depends on:** —
- **Goal:** g
- **Scope:** s
- **Acceptance:** a

#### T1.2 — Pair on it (owner + agent)
- **Depends on:** T1.1
- **Goal:** g
- **Scope:** s
- **Acceptance:** a

### M2 - Two

#### T2.1 — Third
- **Depends on:** T9.9
- **Goal:** g

### Milestone notes

### M3

## Deferred

## Open questions

| Question | Needed by |
|---|---|
| One? | T1.1 |
| Two? | later |
| Three? **Answered 2026-10-14:** yes. | T1.2 |

## Definition of done
`
	got := Lint([]byte(plan)).Summary
	want := Summary{Milestones: 2, Tasks: 3, OwnerTasks: 1, OpenQuestions: 2, Errors: 1, Warnings: 2}
	if got != want {
		t.Errorf("summary = %+v, want %+v", got, want)
	}
}

// TestLintOwnPlan runs the checker on this repository's own master plan.
// Zero errors is the T5.1 acceptance, and the task count must be the
// parser's, since afk plan lint --summary has to agree with afk tasks. The
// warnings are not pinned: the plan changes with every task.
func TestLintOwnPlan(t *testing.T) {
	data, err := os.ReadFile("../../docs/PLAN.md")
	if err != nil {
		t.Fatal(err)
	}
	r := Lint(data)
	for _, f := range r.Findings {
		if f.Level == LevelError {
			t.Errorf("line %d: %s: %s", f.Line, f.Rule, f.Message)
		}
	}
	tasks, err := masterplan.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Tasks != len(tasks) {
		t.Errorf("Summary.Tasks = %d, the parser reads %d", r.Summary.Tasks, len(tasks))
	}
	if r.Summary.Errors != 0 {
		t.Errorf("Summary.Errors = %d, want 0", r.Summary.Errors)
	}
}

// TestLintNebariPlan runs the checker on the frozen nebari plan and compares
// every finding with a golden file. The fixture never changes, so any change
// to the list is a change in a rule, and the diff shows exactly which
// findings a rule change adds or drops. Run with -update to rewrite the
// file, then read the diff: each line must be a true finding.
func TestLintNebariPlan(t *testing.T) {
	data, err := os.ReadFile("../masterplan/testdata/nebari-plan.md")
	if err != nil {
		t.Fatal(err)
	}
	r := Lint(data)
	if r.Summary.Errors != 0 {
		t.Errorf("Summary.Errors = %d, want 0", r.Summary.Errors)
	}
	if r.Summary.Tasks != 40 {
		t.Errorf("Summary.Tasks = %d, want 40", r.Summary.Tasks)
	}
	got := strings.Join(render(r.Findings), "\n") + "\n"
	const golden = "testdata/nebari-findings.txt"
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("findings differ from %s (run with -update after reading the change):\n got:\n%s\n want:\n%s", golden, got, want)
	}
}

const formatDoc = "../../plugins/afk/skills/master-plan/plan-format.md"

// TestFormatDocExample lints the worked example of the format document. The
// document tells people (and the planning session) what a correct plan looks
// like, so its own example must pass the checker with nothing to report.
func TestFormatDocExample(t *testing.T) {
	data, err := os.ReadFile(formatDoc)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "<!-- worked-example -->" {
			start = i + 1
			break
		}
	}
	if start < 0 || start >= len(lines) || !strings.HasPrefix(lines[start], "````markdown") {
		t.Fatal("no four-backtick markdown fence directly after the <!-- worked-example --> marker")
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "````" {
			end = i
			break
		}
	}
	if end < 0 {
		t.Fatal("the worked example's fence is never closed")
	}
	r := Lint([]byte(strings.Join(lines[start+1:end], "\n") + "\n"))
	if len(r.Findings) != 0 {
		for _, f := range r.Findings {
			t.Errorf("line %d of the example: %s: %s", f.Line, f.Rule, f.Message)
		}
	}
	if r.Summary.Tasks < 4 {
		t.Errorf("the example has %d tasks, want at least 4", r.Summary.Tasks)
	}
}

// TestFormatDocListsEveryRule keeps the rule ids and the document's rule
// table from drifting apart: a rule the document does not explain is a
// finding nobody can look up.
func TestFormatDocListsEveryRule(t *testing.T) {
	data, err := os.ReadFile(formatDoc)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range Rules {
		if !strings.Contains(string(data), "`"+rule+"`") {
			t.Errorf("the format document does not mention `%s`", rule)
		}
	}
}
