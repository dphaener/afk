package state

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParsePlanFile_Valid(t *testing.T) {
	plan, err := ParsePlanFile(filepath.Join("testdata", "plan", "valid.md"))
	if err != nil {
		t.Fatalf("ParsePlanFile: %v", err)
	}

	if plan.Title != "Add billing webhook support" {
		t.Errorf("Title = %q, want %q", plan.Title, "Add billing webhook support")
	}

	if len(plan.Waves) != 3 {
		t.Fatalf("len(Waves) = %d, want 3", len(plan.Waves))
	}

	wantWaves := []struct {
		number int
		name   string
		tasks  int
	}{
		{1, "Data layer", 2},
		{2, "API layer", 2},
		{3, "UI and notification gating", 1},
	}
	for i, w := range wantWaves {
		gw := plan.Waves[i]
		if gw.Number != w.number {
			t.Errorf("Waves[%d].Number = %d, want %d", i, gw.Number, w.number)
		}
		if gw.Name != w.name {
			t.Errorf("Waves[%d].Name = %q, want %q", i, gw.Name, w.name)
		}
		if len(gw.Tasks) != w.tasks {
			t.Errorf("Waves[%d].Tasks = %d, want %d", i, len(gw.Tasks), w.tasks)
		}
	}

	// Wave 1, Task 1: complete, light.
	t1 := plan.Waves[0].Tasks[0]
	if t1.ID != "W1.T1" || t1.Wave != 1 || t1.Num != 1 {
		t.Errorf("Waves[0].Tasks[0] identity = %+v", t1)
	}
	if t1.Title != "Create notification preferences schema" {
		t.Errorf("Waves[0].Tasks[0].Title = %q", t1.Title)
	}
	if t1.State != CheckComplete {
		t.Errorf("Waves[0].Tasks[0].State = %v, want CheckComplete", t1.State)
	}
	if t1.Model != ModelLight {
		t.Errorf("Waves[0].Tasks[0].Model = %v, want light", t1.Model)
	}
	if t1.FailureReason != "" {
		t.Errorf("Waves[0].Tasks[0].FailureReason = %q, want empty", t1.FailureReason)
	}

	// Wave 1, Task 2: failed, with reason.
	t2 := plan.Waves[0].Tasks[1]
	if t2.ID != "W1.T2" {
		t.Errorf("Waves[0].Tasks[1].ID = %q, want W1.T2", t2.ID)
	}
	if t2.State != CheckFailed {
		t.Errorf("Waves[0].Tasks[1].State = %v, want CheckFailed", t2.State)
	}
	if t2.Title != "Add NotificationPreferences model" {
		t.Errorf("Waves[0].Tasks[1].Title = %q", t2.Title)
	}
	if t2.FailureReason != "could not resolve merge conflict after 3 retries" {
		t.Errorf("Waves[0].Tasks[1].FailureReason = %q", t2.FailureReason)
	}

	// Wave 2 tasks: incomplete, balanced.
	for _, task := range plan.Waves[1].Tasks {
		if task.State != CheckIncomplete {
			t.Errorf("task %s State = %v, want CheckIncomplete", task.ID, task.State)
		}
		if task.Model != ModelBalanced {
			t.Errorf("task %s Model = %v, want balanced", task.ID, task.Model)
		}
	}

	// Wave 3 task: reasoning tier.
	w3t1 := plan.Waves[2].Tasks[0]
	if w3t1.Model != ModelReasoning {
		t.Errorf("Waves[2].Tasks[0].Model = %v, want reasoning", w3t1.Model)
	}

	// Success Criteria checklist items (no task IDs) must NOT be parsed as
	// tasks or waves.
	for _, w := range plan.Waves {
		for _, task := range w.Tasks {
			if task.ID == "" {
				t.Errorf("found a task with empty ID — Success Criteria checklist bled into task parsing")
			}
		}
	}
}

func TestParsePlan_CheckStateString(t *testing.T) {
	tests := []struct {
		state CheckState
		want  string
	}{
		{CheckIncomplete, "incomplete"},
		{CheckComplete, "complete"},
		{CheckFailed, "failed"},
		{CheckState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("CheckState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestParsePlanFile_Malformed(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"missing task ID", "malformed_missing_task_id.md"},
		{"invalid checkbox char", "malformed_invalid_checkbox.md"},
		{"missing Model tier", "malformed_missing_model.md"},
		{"empty wave", "malformed_empty_wave.md"},
		{"duplicate task ID", "malformed_duplicate_id.md"},
		{"failed task with no reason", "malformed_failed_no_reason.md"},
		{"invalid Model tier value", "malformed_invalid_model.md"},
		{"missing plan title", "malformed_no_title.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := ParsePlanFile(filepath.Join("testdata", "plan", tt.file))
			if err == nil {
				t.Fatalf("ParsePlanFile(%s) = %+v, nil; want a non-nil error", tt.file, plan)
			}
			if plan != nil {
				t.Errorf("ParsePlanFile(%s) returned a non-nil plan alongside an error", tt.file)
			}
		})
	}
}

func TestParsePlanFile_MissingFile(t *testing.T) {
	_, err := ParsePlanFile(filepath.Join("testdata", "plan", "does-not-exist.md"))
	if err == nil {
		t.Fatal("expected an error for a missing plan file, got nil")
	}
}

func TestParsePlan_EmptyInputDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParsePlan panicked on empty input: %v", r)
		}
	}()
	if _, err := ParsePlan([]byte("")); err == nil {
		t.Fatal("expected an error for empty input, got nil")
	}
}

// TestParsePlanFile_Depends covers the DAG fixture end to end: wrapped
// value joined, `none`/`N/A` → empty non-nil, per-item none dropped,
// backticks stripped, and `- [A1]` Assumptions bullets after Success
// Criteria yielding no tasks. The nil-vs-empty distinction is the
// load-bearing part: nil means a legacy plan, empty means "no deps".
func TestParsePlanFile_Depends(t *testing.T) {
	plan, err := ParsePlanFile(filepath.Join("testdata", "plan", "depends.md"))
	if err != nil {
		t.Fatalf("ParsePlanFile: %v", err)
	}
	want := map[string][]string{
		"W1.T1": {},
		"W1.T2": {},
		"W2.T1": {"W1.T1", "W1.T2"},
		"W2.T2": {"W1.T1"},
		"W3.T1": {"W2.T1", "W2.T2"},
	}
	n := 0
	for _, w := range plan.Waves {
		for _, task := range w.Tasks {
			n++
			exp, ok := want[task.ID]
			if !ok {
				t.Errorf("unexpected task %s (Assumptions bullet parsed as a task?)", task.ID)
				continue
			}
			if task.DependsOn == nil {
				t.Errorf("%s DependsOn = nil, want non-nil %v", task.ID, exp)
				continue
			}
			if !reflect.DeepEqual(task.DependsOn, exp) {
				t.Errorf("%s DependsOn = %#v, want %#v", task.ID, task.DependsOn, exp)
			}
		}
	}
	if n != len(want) {
		t.Errorf("parsed %d tasks, want %d", n, len(want))
	}
}

// TestParsePlanFile_ValidHasNoDepends proves a legacy plan (no field)
// leaves DependsOn nil on every task so callers can keep wave barriers.
func TestParsePlanFile_ValidHasNoDepends(t *testing.T) {
	plan, err := ParsePlanFile(filepath.Join("testdata", "plan", "valid.md"))
	if err != nil {
		t.Fatalf("ParsePlanFile: %v", err)
	}
	for _, w := range plan.Waves {
		for _, task := range w.Tasks {
			if task.DependsOn != nil {
				t.Errorf("%s DependsOn = %#v, want nil", task.ID, task.DependsOn)
			}
		}
	}
}

// TestParsePlan_DependsEdges is table-driven over inline plans. Load-bearing
// rows: "label line ends continuation" (a following **Files** must not be
// joined), "first wins" (upstream plan_to_args.rb takes the first
// occurrence), "stray before any task" (must be ignored, not an error like
// **Model**), and the nil/empty rows.
func TestParsePlan_DependsEdges(t *testing.T) {
	const head = "# P\n\n## Waves\n\n### Wave 1: A\n\n"
	const task = "- [ ] (W1.T1) One\n\n  **Model**: light\n"
	cases := []struct {
		name string
		body string // appended after head+task, still inside W1.T1
		want []string
		nil_ bool
	}{
		{name: "absent", body: "", nil_: true},
		{name: "single", body: "\n  **Depends on**: W0.T1\n", want: []string{"W0.T1"}},
		{name: "wrapped joined with space", body: "\n  **Depends on**: W1.T1,\n  W1.T2\n", want: []string{"W1.T1", "W1.T2"}},
		{name: "label line right after is not joined", body: "\n  **Depends on**: W1.T2\n  **Files**: `a, b`\n", want: []string{"W1.T2"}},
		{name: "unknown label line is not joined", body: "\n  **Depends on**: W1.T2\n  **Notes**: W9.T9\n", want: []string{"W1.T2"}},
		{name: "blank line ends continuation", body: "\n  **Depends on**: W1.T2\n\n  trailing prose\n", want: []string{"W1.T2"}},
		{name: "none", body: "\n  **Depends on**: none\n", want: []string{}},
		{name: "N/A uppercase", body: "\n  **Depends on**: N/A\n", want: []string{}},
		{name: "per-item none dropped", body: "\n  **Depends on**: W1.T1, none\n", want: []string{"W1.T1"}},
		{name: "backticks stripped", body: "\n  **Depends on**: `W1.T1`, `W1.T2`\n", want: []string{"W1.T1", "W1.T2"}},
		{name: "empty value", body: "\n  **Depends on**:\n", want: []string{}},
		{name: "duplicate line first wins", body: "\n  **Depends on**: W1.T1\n  **Depends on**: W1.T2\n  more\n", want: []string{"W1.T1"}},
		{name: "duplicate separated by label first wins", body: "\n  **Depends on**: W1.T1\n\n  **Files**: x\n\n  **Depends on**: W1.T2\n", want: []string{"W1.T1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParsePlan([]byte(head + task + tc.body))
			if err != nil {
				t.Fatalf("ParsePlan: %v", err)
			}
			got := plan.Waves[0].Tasks[0].DependsOn
			if tc.nil_ {
				if got != nil {
					t.Fatalf("DependsOn = %#v, want nil", got)
				}
				return
			}
			if got == nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DependsOn = %#v, want %#v", got, tc.want)
			}
		})
	}

	t.Run("stray before any task is ignored", func(t *testing.T) {
		src := head + "  **Depends on**: W9.T9\n\n" + task
		plan, err := ParsePlan([]byte(src))
		if err != nil {
			t.Fatalf("ParsePlan: %v", err)
		}
		if got := plan.Waves[0].Tasks[0].DependsOn; got != nil {
			t.Errorf("DependsOn = %#v, want nil (stray line leaked onto next task)", got)
		}
	})

	t.Run("stray continuation after wave heading does not leak", func(t *testing.T) {
		src := head + "  **Depends on**: W9.T9\n  wrapped\n" + task
		if _, err := ParsePlan([]byte(src)); err != nil {
			t.Fatalf("ParsePlan: %v", err)
		}
	})

	t.Run("continuation stops at next task line", func(t *testing.T) {
		src := head + task + "\n  **Depends on**: W0.T1\n- [ ] (W1.T2) Two\n\n  **Model**: light\n"
		plan, err := ParsePlan([]byte(src))
		if err != nil {
			t.Fatalf("ParsePlan: %v", err)
		}
		ts := plan.Waves[0].Tasks
		if !reflect.DeepEqual(ts[0].DependsOn, []string{"W0.T1"}) {
			t.Errorf("T1 DependsOn = %#v", ts[0].DependsOn)
		}
		if ts[1].DependsOn != nil {
			t.Errorf("T2 DependsOn = %#v, want nil", ts[1].DependsOn)
		}
	})

	t.Run("continuation stops at heading", func(t *testing.T) {
		src := head + task + "\n  **Depends on**: W0.T1\n### Wave 2: B\n\n- [ ] (W2.T1) Two\n\n  **Model**: light\n"
		plan, err := ParsePlan([]byte(src))
		if err != nil {
			t.Fatalf("ParsePlan: %v", err)
		}
		if got := plan.Waves[0].Tasks[0].DependsOn; !reflect.DeepEqual(got, []string{"W0.T1"}) {
			t.Errorf("DependsOn = %#v (heading joined?)", got)
		}
	})

	t.Run("tolerates unknown, same-wave and cyclic deps", func(t *testing.T) {
		src := head + task + "\n  **Depends on**: W9.T9, W1.T1\n"
		if _, err := ParsePlan([]byte(src)); err != nil {
			t.Fatalf("ParsePlan rejected a tolerated plan: %v", err)
		}
	})
}

// TestParsePlanFile_QA covers the qa.md fixture. Load-bearing rows: the
// wrapped Steps value that contains a colon and the word "Expected" (it must
// stay inside Steps, not open a field), the line range stripped from Files
// but kept in FileRefs, a scenario missing fields parsed without error, and
// N/A files yielding nil. The parser must never fail on these. The Case values
// on T1 pin the fifth field in its documented place (after Tool); T2 has no
// Case line and must come back with an empty Case, not an error.
func TestParsePlanFile_QA(t *testing.T) {
	plan, err := ParsePlanFile(filepath.Join("testdata", "plan", "qa.md"))
	if err != nil {
		t.Fatalf("ParsePlanFile: %v", err)
	}
	tasks := plan.Waves[0].Tasks
	if len(tasks) != 3 {
		t.Fatalf("parsed %d tasks, want 3", len(tasks))
	}

	t1 := tasks[0]
	wantFiles := []string{"internal/state/plan.go", "internal/state/plan_test.go", "internal/state/testdata/"}
	if !reflect.DeepEqual(t1.Files, wantFiles) {
		t.Errorf("T1 Files = %#v, want %#v", t1.Files, wantFiles)
	}
	wantRefs := []string{"internal/state/plan.go:107-125", "internal/state/plan_test.go", "internal/state/testdata/"}
	if !reflect.DeepEqual(t1.FileRefs, wantRefs) {
		t.Errorf("T1 FileRefs = %#v, want %#v", t1.FileRefs, wantRefs)
	}
	wantQA := []QAScenario{
		{Tool: "Bash", Case: "happy", Steps: "run `go test ./internal/state/ -count=1`", Expected: "exit 0 and an `ok` line", Evidence: ".afk/qa/evidence/w1-t1-tests.txt"},
		{Tool: "Bash", Case: "failure", Steps: "run the binary and note: Expected output is a table, then check the exit code", Expected: "exit 0", Evidence: ".afk/qa/evidence/w1-t1-run.txt"},
	}
	if !reflect.DeepEqual(t1.QA, wantQA) {
		t.Errorf("T1 QA = %#v, want %#v", t1.QA, wantQA)
	}
	if t1.NoFailureCase != "" {
		t.Errorf("T1 NoFailureCase = %q, want empty (the fixture has no waiver line)", t1.NoFailureCase)
	}
	if !reflect.DeepEqual(t1.DependsOn, []string{}) || t1.Model != ModelBalanced {
		t.Errorf("T1 Depends/Model disturbed: %#v %q", t1.DependsOn, t1.Model)
	}

	t2 := tasks[1]
	if !reflect.DeepEqual(t2.QA, []QAScenario{{Tool: "Read", Steps: "open the file"}}) {
		t.Errorf("T2 QA = %#v, want one partial scenario", t2.QA)
	}
	if t2.Model != ModelLight {
		t.Errorf("T2 Model = %q", t2.Model)
	}

	t3 := tasks[2]
	if t3.QA != nil || t3.Files != nil || t3.FileRefs != nil {
		t.Errorf("T3 = QA %#v Files %#v FileRefs %#v, want all nil", t3.QA, t3.Files, t3.FileRefs)
	}
}

// TestParsePlanFile_OldPlanUnchanged proves a six-subsection plan with no
// QA Scenarios (valid.md) parses with nil QA on every task.
func TestParsePlanFile_OldPlanUnchanged(t *testing.T) {
	plan, err := ParsePlanFile(filepath.Join("testdata", "plan", "valid.md"))
	if err != nil {
		t.Fatalf("ParsePlanFile: %v", err)
	}
	for _, w := range plan.Waves {
		for _, task := range w.Tasks {
			if task.QA != nil {
				t.Errorf("%s QA = %#v, want nil", task.ID, task.QA)
			}
		}
	}
}

// TestParsePlan_QAEdges is table-driven over inline subsections. Load-bearing
// rows: "stray lines before first Tool" (ignored, no error), "fenced example
// ignored" (a code fence inside the subsection is not read as scenarios),
// "first QA label wins", and the Case and waiver rows:
//   - "no Case" and "unknown Case kept verbatim": the parser records and never
//     judges, so the dashboard still shows a plan plan_to_args.rb would refuse.
//     A row that started returning an error here would blank that plan.
//   - "waiver after the last scenario": the waiver line sits right under an
//     Evidence line, and without its own match it is a continuation, so the
//     evidence path would silently grow " No failure case: ..." on the end.
//   - "text after the waiver is not evidence": the waiver ends the open
//     field, so a wrapped reason cannot be glued onto Evidence either.
//   - "waiver with no scenarios": the line is read before any `- Tool:`,
//     where every other line is dropped.
func TestParsePlan_QAEdges(t *testing.T) {
	const head = "# P\n\n## Waves\n\n### Wave 1: A\n\n- [ ] (W1.T1) One\n\n"
	const tail = "\n  **Model**: light\n"
	cases := []struct {
		name       string
		body       string
		want       []QAScenario
		wantWaiver string
	}{
		{name: "absent", body: "", want: nil},
		{name: "N/A value", body: "  **QA Scenarios**: N/A\n", want: nil},
		{name: "stray lines before first Tool", body: "  **QA Scenarios**:\n  junk\n  - Tool: Bash\n    Steps: s\n", want: []QAScenario{{Tool: "Bash", Steps: "s"}}},
		{name: "fenced example ignored", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Steps: s\n  ```\n  - Tool: Fake\n  ```\n", want: []QAScenario{{Tool: "Bash", Steps: "s"}}},
		{name: "first QA label wins", body: "  **QA Scenarios**:\n  - Tool: A\n\n  **QA Scenarios**:\n  - Tool: B\n", want: []QAScenario{{Tool: "A"}}},
		{name: "all five fields", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Case: happy\n    Steps: s\n    Expected: e\n    Evidence: `x.txt`\n",
			want: []QAScenario{{Tool: "Bash", Case: "happy", Steps: "s", Expected: "e", Evidence: "x.txt"}}},
		{name: "no Case", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Steps: s\n    Expected: e\n    Evidence: `x.txt`\n",
			want: []QAScenario{{Tool: "Bash", Steps: "s", Expected: "e", Evidence: "x.txt"}}},
		{name: "unknown Case kept verbatim", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Case: Sad path\n    Steps: s\n",
			want: []QAScenario{{Tool: "Bash", Case: "Sad path", Steps: "s"}}},
		{name: "wrapped Case value", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Case: happy\n      path\n    Steps: s\n",
			want: []QAScenario{{Tool: "Bash", Case: "happy path", Steps: "s"}}},
		{name: "waiver after the last scenario", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Case: happy\n    Steps: s\n    Expected: e\n    Evidence: `x.txt`\n  No failure case: it only renames a file\n",
			want:       []QAScenario{{Tool: "Bash", Case: "happy", Steps: "s", Expected: "e", Evidence: "x.txt"}},
			wantWaiver: "it only renames a file"},
		{name: "waiver written as a list item", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Evidence: `x.txt`\n  - No failure case: docs only\n",
			want:       []QAScenario{{Tool: "Bash", Evidence: "x.txt"}},
			wantWaiver: "docs only"},
		{name: "text after the waiver is not evidence", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Evidence: `x.txt`\n  No failure case: docs only\n    and nothing runs\n",
			want:       []QAScenario{{Tool: "Bash", Evidence: "x.txt"}},
			wantWaiver: "docs only"},
		{name: "waiver with no scenarios", body: "  **QA Scenarios**:\n  No failure case: nothing runs\n", want: nil, wantWaiver: "nothing runs"},
		{name: "waiver with an empty reason", body: "  **QA Scenarios**:\n  - Tool: Bash\n    Evidence: `x.txt`\n  No failure case:\n",
			want: []QAScenario{{Tool: "Bash", Evidence: "x.txt"}}},
		{name: "waiver outside QA Scenarios is not read", body: "  **Acceptance Criteria**: ok\n  No failure case: wrong place\n", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParsePlan([]byte(head + tc.body + tail))
			if err != nil {
				t.Fatalf("ParsePlan: %v", err)
			}
			task := plan.Waves[0].Tasks[0]
			if !reflect.DeepEqual(task.QA, tc.want) {
				t.Fatalf("QA = %#v, want %#v", task.QA, tc.want)
			}
			if task.NoFailureCase != tc.wantWaiver {
				t.Fatalf("NoFailureCase = %q, want %q", task.NoFailureCase, tc.wantWaiver)
			}
		})
	}
}

// TestParsePlan_Fences pins that nothing inside a fenced code block is plan
// structure: not a task line, a wave heading or a section heading. Mark in
// internal/plancheck and plan_to_args.rb read fences the same way, and the
// three must agree or `afk plan check` ticks a task the dashboard cannot see.
//
// Load-bearing rows:
//   - "fenced task line inside a task body": the quoted W1.T2 has no Model, so
//     reading it as a task fails the whole plan ("missing a **Model** tier").
//   - "fenced task line before the first real task": no task is open yet, which
//     is where the fence used to go untracked.
//   - "fenced wave heading": read as structure it opens an empty Wave 2, which
//     is an error, and W1.T2 would land in the wrong wave.
//   - "fenced section heading": read as structure it ends the Waves section
//     and W1.T2 disappears without any error.
//   - "fence stays open across a task line": the fence state is not reset by
//     a new task, so a fence that opens before a task and closes after it
//     hides that task.
//   - "unclosed fence hides the rest": matches Mark, which also never closes
//     it; the parser must return what came before, not panic or error.
func TestParsePlan_Fences(t *testing.T) {
	const head = "# P\n\n## Waves\n\n### Wave 1: A\n\n"
	const t1 = "- [ ] (W1.T1) One\n\n  **Model**: light\n\n"
	const t2 = "- [x] (W1.T2) Two\n\n  **Model**: heavy\n"
	cases := []struct {
		name string
		src  string
		// want is "wave:id" for every task the parser must report, in order.
		want []string
	}{
		{name: "no fence", src: head + t1 + t2, want: []string{"1:W1.T1", "1:W1.T2"}},
		{name: "fenced task line inside a task body",
			src:  head + t1 + "  ```\n- [ ] (W1.T2) A quoted example\n  ```\n\n" + t2,
			want: []string{"1:W1.T1", "1:W1.T2"}},
		{name: "fenced task line before the first real task",
			src:  head + "```\n- [ ] (W1.T2) A quoted example\n```\n\n" + t1 + t2,
			want: []string{"1:W1.T1", "1:W1.T2"}},
		{name: "fenced task line before any wave",
			src:  "# P\n\n## Waves\n\n```\n- [ ] (W1.T2) A quoted example\n```\n\n### Wave 1: A\n\n" + t1 + t2,
			want: []string{"1:W1.T1", "1:W1.T2"}},
		{name: "fenced wave heading",
			src:  head + t1 + "  ```\n### Wave 2: Quoted\n  ```\n\n" + t2,
			want: []string{"1:W1.T1", "1:W1.T2"}},
		{name: "fenced section heading",
			src:  head + t1 + "  ```\n## Success Criteria\n  ```\n\n" + t2,
			want: []string{"1:W1.T1", "1:W1.T2"}},
		{name: "tilde fence",
			src:  head + t1 + "  ~~~\n- [ ] (W1.T2) A quoted example\n### Wave 2: Quoted\n  ~~~\n\n" + t2,
			want: []string{"1:W1.T1", "1:W1.T2"}},
		{name: "fence with an info string",
			src:  head + t1 + "  ```markdown\n- [ ] (W1.T2) A quoted example\n  ```\n\n" + t2,
			want: []string{"1:W1.T1", "1:W1.T2"}},
		{name: "fenced Model line is not the task's tier",
			src:  head + "- [ ] (W1.T1) One\n\n  ```\n  **Model**: bogus\n  ```\n\n  **Model**: light\n",
			want: []string{"1:W1.T1"}},
		{name: "fence stays open across a task line",
			src:  head + t1 + "```\n" + t2 + "```\n",
			want: []string{"1:W1.T1"}},
		{name: "fence outside Waves still counts",
			src:  "# P\n\n## Context\n\n```\n## Waves\n### Wave 1: Quoted\n- [ ] (W1.T9) A quoted example\n```\n\n## Waves\n\n### Wave 1: A\n\n" + t1,
			want: []string{"1:W1.T1"}},
		{name: "unclosed fence hides the rest",
			src:  head + t1 + "  ```\n" + t2 + "\n### Wave 2: B\n\n- [ ] (W2.T1) Three\n\n  **Model**: bogus\n",
			want: []string{"1:W1.T1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParsePlan([]byte(tc.src))
			if err != nil {
				t.Fatalf("ParsePlan: %v", err)
			}
			var got []string
			for _, w := range plan.Waves {
				for _, task := range w.Tasks {
					got = append(got, fmt.Sprintf("%d:%s", w.Number, task.ID))
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tasks = %v, want %v", got, tc.want)
			}
			if plan.Waves[0].Tasks[0].Model != ModelLight {
				t.Errorf("W1.T1 Model = %q, want light", plan.Waves[0].Tasks[0].Model)
			}
		})
	}
}
