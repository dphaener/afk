package state

import (
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
