package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
)

// badDepPlan builds a plan of n tasks that each depend on a task that does not
// exist, so the checker reports n unknown-dependency errors (and, because the
// tasks have no Goal line, warnings besides).
func badDepPlan(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "#### T1.%d — Task %d\n- **Depends on:** T9.%d\n\n", i, i, i)
	}
	return b.String()
}

func readerOf(text string) func(string) ([]byte, error) {
	return func(string) ([]byte, error) { return []byte(text), nil }
}

func bulletLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "- ") {
			out = append(out, l)
		}
	}
	return out
}

// TestWithPlanFindings holds the rules for the daemon's bad-plan item.
// Load-bearing rows: "a non-plan error" and "the plan file cannot be read"
// (nothing is appended to an error the checker has nothing to add to), "three
// errors" (all are listed after the original message and errors.Is still
// matches), "25 errors" (the cap, with the count left over), and "same text on
// every call" (the daemon skips a repeat item only on an identical body, so a
// changing one would raise a new item on every pass).
func TestWithPlanFindings(t *testing.T) {
	planErr := func() error {
		return fmt.Errorf("the master plan p has a problem: %w. Fix the plan and try again", masterplan.ErrUnknownDependency)
	}
	failing := func(string) ([]byte, error) { return nil, os.ErrNotExist }

	t.Run("nil error", func(t *testing.T) {
		if got := withPlanFindings(nil, "p", readerOf(badDepPlan(3))); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("a non-plan error", func(t *testing.T) {
		in := errors.New("cannot read the task ledger: permission denied")
		got := withPlanFindings(in, "p", readerOf(badDepPlan(3)))
		if got != in {
			t.Fatalf("got %v, want the same error back", got)
		}
	})

	t.Run("the plan file cannot be read", func(t *testing.T) {
		in := planErr()
		if got := withPlanFindings(in, "p", failing); got != in {
			t.Fatalf("got %v, want the same error back", got)
		}
	})

	t.Run("the checker finds no error", func(t *testing.T) {
		in := planErr()
		clean := "#### T1.1 — A\n- **Depends on:** —\n"
		if got := withPlanFindings(in, "p", readerOf(clean)); got != in {
			t.Fatalf("got %v, want the same error back", got)
		}
	})

	t.Run("three errors", func(t *testing.T) {
		in := planErr()
		got := withPlanFindings(in, "p", readerOf(badDepPlan(3)))
		if !errors.Is(got, masterplan.ErrUnknownDependency) {
			t.Fatalf("errors.Is lost the sentinel: %v", got)
		}
		text := got.Error()
		head := in.Error() + "\n\nEvery problem `afk plan lint` found in the plan:\n"
		if !strings.HasPrefix(text, head) {
			t.Fatalf("text does not start with the message and the heading line:\n%s", text)
		}
		bullets := bulletLines(text)
		if len(bullets) != 3 {
			t.Fatalf("got %d bullets, want 3:\n%s", len(bullets), text)
		}
		for i, l := range bullets {
			want := fmt.Sprintf("- line %d: ", 1+3*i)
			if !strings.HasPrefix(l, want) {
				t.Errorf("bullet %d = %q, want prefix %q", i, l, want)
			}
		}
	})

	t.Run("warnings are not listed", func(t *testing.T) {
		plan := badDepPlan(2)
		rep := planlint.Lint([]byte(plan))
		if rep.Summary.Warnings == 0 {
			t.Fatal("fixture should produce warnings")
		}
		got := withPlanFindings(planErr(), "p", readerOf(plan)).Error()
		if n := len(bulletLines(got)); n != rep.Summary.Errors {
			t.Fatalf("listed %d lines, want only the %d errors:\n%s", n, rep.Summary.Errors, got)
		}
		for _, f := range rep.Findings {
			if f.Level == planlint.LevelWarning && strings.Contains(got, f.Message) {
				t.Errorf("warning listed: %s", f.Message)
			}
		}
	})

	t.Run("25 errors", func(t *testing.T) {
		got := withPlanFindings(planErr(), "p", readerOf(badDepPlan(25))).Error()
		bullets := bulletLines(got)
		if len(bullets) != 21 {
			t.Fatalf("got %d bullets, want 20 plus the last line:\n%s", len(bullets), got)
		}
		wantLast := "- and 5 more. Run `afk plan lint` to see them all."
		if bullets[20] != wantLast {
			t.Errorf("last line = %q, want %q", bullets[20], wantLast)
		}
	})

	t.Run("exactly 20 errors", func(t *testing.T) {
		got := withPlanFindings(planErr(), "p", readerOf(badDepPlan(20))).Error()
		if n := len(bulletLines(got)); n != 20 || strings.Contains(got, "more.") {
			t.Fatalf("got %d bullets or a spurious more line:\n%s", n, got)
		}
	})

	t.Run("a finding with no line", func(t *testing.T) {
		in := fmt.Errorf("x: %w", masterplan.ErrNoTasks)
		got := withPlanFindings(in, "p", readerOf("# Plan\n")).Error()
		for _, l := range bulletLines(got) {
			if strings.HasPrefix(l, "- line 0") {
				t.Errorf("line 0 printed as a line: %q", l)
			}
		}
		if len(bulletLines(got)) == 0 {
			t.Fatalf("no-tasks plan listed nothing:\n%s", got)
		}
	})

	t.Run("same text on every call", func(t *testing.T) {
		a := withPlanFindings(planErr(), "p", readerOf(badDepPlan(25))).Error()
		b := withPlanFindings(planErr(), "p", readerOf(badDepPlan(25))).Error()
		if a != b {
			t.Fatalf("text changed between calls:\n%s\n---\n%s", a, b)
		}
	})
}

// TestDaemonLoadPlanError runs the closure's error path against a temp project
// whose plan has an unknown dependency: the text keeps the loader's message
// and adds the list. Load-bearing: the nil row (a good plan is never checked)
// and the broken afk.toml row (the helper is skipped, the error passes through).
func TestDaemonLoadPlanError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "PLAN.md"), []byte(badDepPlan(2)), 0o644); err != nil {
		t.Fatal(err)
	}

	_, loadErr := loadBuildState(root)
	if loadErr == nil {
		t.Fatal("fixture should fail to load")
	}

	got := daemonLoadPlanError(root, loadErr, os.ReadFile)
	if !errors.Is(got, masterplan.ErrUnknownDependency) {
		t.Fatalf("lost the sentinel: %v", got)
	}
	text := got.Error()
	if !strings.Contains(text, "has a problem") || !strings.Contains(text, "Every problem `afk plan lint` found") || len(bulletLines(text)) != 2 {
		t.Fatalf("text missing the message or the list:\n%s", text)
	}

	if daemonLoadPlanError(root, nil, os.ReadFile) != nil {
		t.Error("nil error must stay nil")
	}

	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "afk.toml"), []byte("plan = [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := daemonLoadPlanError(bad, loadErr, os.ReadFile); got != loadErr {
		t.Errorf("unreadable afk.toml: got %v, want the error unchanged", got)
	}
}
