package planpatch

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/planlint"
)

// base is a small master plan the checker has nothing to say about, with
// three tasks so a row can hold one, edit another and leave the third
// alone. Every row below is this plan with one patch applied, so what a row
// sees can only come from that patch.
const base = `# Plan

**How to use this document.** Read Context first.

## Context

The owner asked: "build it".

## Decisions

| Topic | Decision |
|---|---|
| Language | Go |

## Milestones

### M1 — One

**Outcome:** it starts.

#### T1.1 — First
- **Depends on:** —
- **Goal:** g
- **Scope:** s1
- **Acceptance:** a

#### T1.2 — Second
- **Depends on:** T1.1
- **Goal:** g
- **Scope:** s2
- **Acceptance:** a

#### T1.3 — Third
- **Depends on:** T1.2
- **Goal:** g
- **Scope:** s3
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

// swap returns plan with the one occurrence of old replaced by new. It
// panics when old is not there exactly once, so a row cannot silently build
// the wrong plan after base is edited.
func swap(plan, old, new string) string {
	if n := strings.Count(plan, old); n != 1 {
		panic(fmt.Sprintf("swap: %q occurs %d times, want 1", old, n))
	}
	return strings.Replace(plan, old, new, 1)
}

// kindOf names the error Apply returned, so a row can say which one it
// wants in one word.
func kindOf(err error) string {
	var many *ManyMatchesError
	var held *HeldError
	var removed *RemovedIDError
	var lint *LintError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrEmptyOld):
		return "empty-old"
	case errors.Is(err, ErrNoMatch):
		return "no-match"
	case errors.Is(err, ErrNoChange):
		return "no-change"
	case errors.As(err, &many):
		return "many"
	case errors.As(err, &held):
		return "held"
	case errors.As(err, &removed):
		return "removed"
	case errors.As(err, &lint):
		return "lint"
	}
	return "other: " + err.Error()
}

const t13Entry = "#### T1.3 — Third\n- **Depends on:** T1.2\n- **Goal:** g\n- **Scope:** s3\n- **Acceptance:** a\n"

// TestApply is the matrix of the guarded patch. The load-bearing rows are
// the held ones and the lint ones. The held rows ("edits the Scope of a
// running task" through "a Depends on bullet is not a continuation") are
// rule 8: a task the daemon is running or shipping must keep the text its
// agent was given, so a patch that changes anything but an added Amended
// line is refused, while a new task placed after it is not a change to it.
// The lint rows ("adds a checker error", "a second open question on an
// unknown task", "an old error whose message holds a line number") are what
// keeps the plan on disk readable by the daemon: a patch that adds an error
// is never written, a plan that already has one can still be patched, and
// an old error that merely moved is not mistaken for a new one.
func TestApply(t *testing.T) {
	running := map[string]string{"T1.2": "running"}
	crlf := strings.ReplaceAll(base, "\n", "\r\n")

	tests := []struct {
		name string
		// plan is the plan to patch; "" means base.
		plan     string
		old, new string
		held     map[string]string
		// wantErr is the kind of error wanted, as kindOf names it; "" means
		// the patch is accepted.
		wantErr string
		// wantCount is the match count a "many" row wants.
		wantCount int
		// wantTask is the task a "held", "removed" or "lint" row wants named.
		wantTask string
		// wantStatus is the status a "held" row wants named.
		wantStatus string
		// wantRule is the rule of the first added finding a "lint" row wants.
		wantRule    string
		wantTouched []string
	}{
		{
			name: "no match",
			old:  "- **Scope:** s9", new: "- **Scope:** s9 more",
			wantErr: "no-match",
		},
		{
			name: "two matches",
			old:  "- **Acceptance:** a\n\n#### T1.", new: "- **Acceptance:** a\n\n\n#### T1.",
			wantErr: "many", wantCount: 2,
		},
		{
			name: "one match",
			old:  "- **Scope:** s2", new: "- **Scope:** s2, and more",
			wantTouched: []string{"T1.2"},
		},
		{
			name: "old equals new",
			old:  "- **Scope:** s2", new: "- **Scope:** s2",
			wantErr: "no-change",
		},
		{
			name: "empty old",
			old:  "", new: "- **Scope:** s2",
			wantErr: "empty-old",
		},
		{
			name: "adds a checker error",
			old:  "- **Depends on:** T1.1\n", new: "- **Depends on:** T9.9\n",
			wantErr: "lint", wantRule: planlint.RuleUnknownDependency, wantTask: "T1.2",
		},
		{
			name: "adds a second open question on an unknown task",
			plan: swap(base, "| Which colour? | T1.2 |\n", "| Which colour? | T1.2 |\n| Which font? | T9.9 |\n"),
			old:  "| Which font? | T9.9 |\n", new: "| Which font? | T9.9 |\n| Which size? | T9.9 |\n",
			wantErr: "lint", wantRule: planlint.RuleOpenQuestionUnknownTask, wantTask: "T9.9",
		},
		{
			name: "a plan with an old error, patch adds none",
			plan: swap(base, "- **Depends on:** T1.2\n", "- **Depends on:** T9.9\n"),
			old:  "- **Scope:** s1", new: "- **Scope:** s1 more",
			wantTouched: []string{"T1.1"},
		},
		{
			name: "an old error whose message holds a line number is not counted as new",
			plan: swap(base, "#### T1.3 — Third", "#### T1.1 — Third"),
			old:  "The owner asked: \"build it\".\n", new: "The owner asked: \"build it\".\nAnd asked again.\n",
		},
		{
			name: "edits the Scope of a running task",
			old:  "- **Scope:** s2", new: "- **Scope:** s2 more",
			held: running, wantErr: "held", wantTask: "T1.2", wantStatus: "running",
		},
		{
			name: "edits the Scope of a shipping task",
			old:  "- **Scope:** s2", new: "- **Scope:** s2 more",
			held: map[string]string{"T1.2": "shipping"}, wantErr: "held", wantTask: "T1.2", wantStatus: "shipping",
		},
		{
			name: "the same edit on a done task",
			old:  "- **Scope:** s2", new: "- **Scope:** s2 more",
			held: map[string]string{"T1.2": "done"}, wantTouched: []string{"T1.2"},
		},
		{
			name: "the same edit on a task absent from Held",
			old:  "- **Scope:** s2", new: "- **Scope:** s2 more",
			held: map[string]string{"T1.1": "running", "T1.3": "shipping"}, wantTouched: []string{"T1.2"},
		},
		{
			name: "appends one Amended line to a running task",
			old:  "- **Scope:** s2\n- **Acceptance:** a\n",
			new:  "- **Scope:** s2\n- **Acceptance:** a\n- **Amended 2026-10-13 (T1.1 done):** the scope is narrower.\n",
			held: running, wantTouched: []string{"T1.2"},
		},
		{
			name: "appends an Amended line with an indented continuation",
			old:  "- **Scope:** s2\n- **Acceptance:** a\n",
			new:  "- **Scope:** s2\n- **Acceptance:** a\n\nAmended 2026-10-13: the scope\n  is narrower now.\n",
			held: running, wantTouched: []string{"T1.2"},
		},
		{
			name: "appends an ordinary line to a running task",
			old:  "- **Scope:** s2\n- **Acceptance:** a\n",
			new:  "- **Scope:** s2\n- **Acceptance:** a\n- **Note:** n\n",
			held: running, wantErr: "held", wantTask: "T1.2", wantStatus: "running",
		},
		{
			name: "a Depends on bullet is not a continuation",
			old:  "#### T1.2 — Second\n",
			new:  "#### T1.2 — Second\nAmended 2026-10-13: waits for nothing now.\n  - **Depends on:** —\n",
			held: running, wantErr: "held", wantTask: "T1.2", wantStatus: "running",
		},
		{
			name: "inserts a new task entry after a running task's entry",
			old:  "- **Scope:** s2\n- **Acceptance:** a\n\n",
			new:  "- **Scope:** s2\n- **Acceptance:** a\n\n#### T1.2a — Between\n- **Depends on:** T1.2\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n\n",
			held: running,
		},
		{
			name: "a heading inserted in the middle of a running task's entry",
			old:  "- **Goal:** g\n- **Scope:** s2\n",
			new:  "- **Goal:** g\n\n#### T1.2a — Between\n- **Depends on:** T1.2\n- **Scope:** s2\n",
			held: running, wantErr: "held", wantTask: "T1.2", wantStatus: "running",
		},
		{
			name:        "a heading inserted in the middle of an entry touches the task",
			old:         "- **Goal:** g\n- **Scope:** s2\n",
			new:         "- **Goal:** g\n\n#### T1.2a — Between\n- **Depends on:** T1.2\n- **Scope:** s2\n",
			wantTouched: []string{"T1.2"},
		},
		{
			name: "changes a running task's Depends on line",
			old:  "- **Depends on:** T1.1\n", new: "- **Depends on:** —\n",
			held: running, wantErr: "held", wantTask: "T1.2", wantStatus: "running",
		},
		{
			name: "renames a task heading's id",
			old:  "#### T1.2 — Second", new: "#### T1.4 — Second",
			wantErr: "removed", wantTask: "T1.2",
		},
		{
			name: "deletes a whole entry",
			old:  "\n" + t13Entry, new: "",
			wantErr: "removed", wantTask: "T1.3",
		},
		{
			name: "a patch in a section outside every task",
			old:  "| Language | Go |", new: "| Language | Rust |",
		},
		{
			name: "an insertion before the first line",
			old:  "# Plan\n", new: "Preface.\n# Plan\n",
		},
		{
			name: "line endings are kept as they are",
			plan: crlf,
			old:  "- **Scope:** s2\r\n", new: "- **Scope:** s2 more\r\n",
			wantTouched: []string{"T1.2"},
		},
		{
			name: "line endings are not normalised to find a match",
			plan: crlf,
			old:  "- **Scope:** s2\n", new: "- **Scope:** s2 more\n",
			wantErr: "no-match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := tt.plan
			if plan == "" {
				plan = base
			}
			got, err := Apply(Request{Plan: []byte(plan), Old: []byte(tt.old), New: []byte(tt.new), Held: tt.held})
			if kind := kindOf(err); kind != tt.wantErr {
				t.Fatalf("Apply() error = %q (%v), want %q", kind, err, tt.wantErr)
			}
			if err != nil {
				if got.Plan != nil || got.Touched != nil {
					t.Errorf("Apply() returned a result with an error: %+v", got)
				}
				switch tt.wantErr {
				case "many":
					var e *ManyMatchesError
					errors.As(err, &e)
					if e.Count != tt.wantCount {
						t.Errorf("Count = %d, want %d", e.Count, tt.wantCount)
					}
				case "held":
					var e *HeldError
					errors.As(err, &e)
					if e.Task != tt.wantTask || e.Status != tt.wantStatus {
						t.Errorf("HeldError = %+v, want task %s status %s", e, tt.wantTask, tt.wantStatus)
					}
				case "removed":
					var e *RemovedIDError
					errors.As(err, &e)
					if e.Task != tt.wantTask {
						t.Errorf("RemovedIDError = %+v, want task %s", e, tt.wantTask)
					}
				case "lint":
					var e *LintError
					errors.As(err, &e)
					if len(e.Added) == 0 {
						t.Fatalf("LintError has no finding")
					}
					if f := e.Added[0]; f.Rule != tt.wantRule || f.Task != tt.wantTask {
						t.Errorf("Added[0] = %+v, want rule %s task %s", f, tt.wantRule, tt.wantTask)
					}
				}
				return
			}
			if want := swap(plan, tt.old, tt.new); string(got.Plan) != want {
				t.Errorf("Plan =\n%s\nwant\n%s", got.Plan, want)
			}
			if !reflect.DeepEqual(got.Touched, tt.wantTouched) {
				t.Errorf("Touched = %v, want %v", got.Touched, tt.wantTouched)
			}
		})
	}
}

// TestErrorMessages pins that every error says what to do next, because the
// command prints the message as is to the owner who wrote the patch.
func TestErrorMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{"empty old", ErrEmptyOld, []string{"empty", "Put the exact text"}},
		{"no match", ErrNoMatch, []string{"not in the plan", "Copy the text"}},
		{"no change", ErrNoChange, []string{"same", "Put the changed text"}},
		{"many", &ManyMatchesError{Count: 3}, []string{"3 times", "Add a line"}},
		{"held", &HeldError{Task: "T1.2", Status: "running"}, []string{"T1.2 is running", "Amended"}},
		{"removed", &RemovedIDError{Task: "T1.2"}, []string{"removes task T1.2", "never removed or renamed", "superseded"}},
		{"lint, one finding", &LintError{Added: []planlint.Finding{{Line: 7, Message: "T1.2 depends on T9.9."}}},
			[]string{"an error", "nothing was changed", "line 7: T1.2 depends on T9.9."}},
		{"lint, whole-file finding", &LintError{Added: []planlint.Finding{{Message: "no task."}, {Message: "no section."}}},
			[]string{"2 errors", "\n  no task.", "\n  no section."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message %q is missing %q", msg, w)
				}
			}
		})
	}
}

// TestLintCleanBase guards the fixture: a row's lint finding must come from
// its patch, which it cannot if base already has one.
func TestLintCleanBase(t *testing.T) {
	if r := planlint.Lint([]byte(base)); len(r.Findings) != 0 {
		t.Fatalf("base has findings: %+v", r.Findings)
	}
}
