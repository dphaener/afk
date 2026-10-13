package masterplan

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// one builds a single-task plan whose Depends on value is dep, preceded by
// two earlier tasks so "all prior" has something to list.
func one(dep string) string {
	return "#### T1.1 — First\n- **Depends on:** —\n\n#### T1.2 — Second\n- **Depends on:** T1.1\n\n" +
		"#### T1.3 — Third\n- **Depends on:** " + dep + "\n"
}

// TestParse covers the Depends on reading rules. Load-bearing rows:
// "unclosed paren" (a wrapped line must still yield its ids and never the
// words of the next line), "prose gives note" (a line with no task id must
// not silently become "no dependencies" without a Note), "all prior" (it
// must expand in plan order), "first depends line only" (later bullets that
// repeat the label are body text), and the fence rows (an example inside a
// fence is not a task). Each error row asserts its sentinel.
func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		plan     string
		wantDeps []string // dependencies of the LAST task
		wantNote string
		wantN    int
		wantErr  error
	}{
		{name: "em dash", plan: one("—"), wantN: 3},
		{name: "en dash", plan: one("–"), wantN: 3},
		{name: "hyphen", plan: one("-"), wantN: 3},
		{name: "None any case", plan: one("NoNe"), wantN: 3},
		{name: "empty value", plan: one(""), wantN: 3},
		{name: "none with parenthetical", plan: one("— (nothing needed)"), wantN: 3},
		{name: "single id", plan: one("T1.2"), wantDeps: []string{"T1.2"}, wantN: 3},
		{name: "letter suffix", plan: "#### T1.1a — A\n- **Depends on:** —\n\n#### T1.2 — B\n- **Depends on:** T1.1a\n", wantDeps: []string{"T1.1a"}, wantN: 2},
		{name: "parentheticals between ids", plan: one("T1.1 (the model), T1.2 (and its UI)"), wantDeps: []string{"T1.1", "T1.2"}, wantN: 3},
		{name: "nested parentheses", plan: one("T1.1 (see (note)), T1.2"), wantDeps: []string{"T1.1", "T1.2"}, wantN: 3},
		{name: "unclosed paren", plan: one("T1.1, T1.2 (the photos, and the"), wantDeps: []string{"T1.1", "T1.2"}, wantN: 3},
		{name: "trailing prose after ids", plan: one("T1.1, T1.2, and then some prose"), wantDeps: []string{"T1.1", "T1.2"}, wantN: 3},
		{name: "repeats dropped", plan: one("T1.2, T1.1, T1.2"), wantDeps: []string{"T1.2", "T1.1"}, wantN: 3},
		{name: "all prior", plan: one("all prior"), wantDeps: []string{"T1.1", "T1.2"}, wantN: 3},
		{name: "All Prior any case", plan: one("All prior tasks"), wantDeps: []string{"T1.1", "T1.2"}, wantN: 3},
		{name: "prose gives note", plan: one("M3–M8 as available"), wantNote: "M3–M8 as available", wantN: 3},
		{name: "prose with parenthetical keeps raw note", plan: one("nothing new (added today)"), wantNote: "nothing new (added today)", wantN: 3},
		{
			name:     "first depends line only",
			plan:     "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.2 — B\n- **Depends on:** T1.1\n- **Depends on:** T9.9\n",
			wantDeps: []string{"T1.1"}, wantN: 2,
		},
		{
			name:     "depends line after body text",
			plan:     "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.2 — B\n\nSome text.\n\n##### Sub\n- **Depends on:** T1.1\n",
			wantDeps: []string{"T1.1"}, wantN: 2,
		},
		{
			name:     "fenced heading ignored",
			plan:     "#### T1.1 — A\n- **Depends on:** —\n\n```\n#### T1.2 — Example\n- **Depends on:** T1.1\n```\n",
			wantDeps: nil, wantN: 1,
		},
		{
			name:     "fenced depends ignored",
			plan:     "#### T1.1 — A\n~~~\n- **Depends on:** T9.9\n~~~\n- **Depends on:** —\n",
			wantDeps: nil, wantN: 1,
		},
		{
			name:     "unclosed fence hides the rest",
			plan:     "#### T1.1 — A\n- **Depends on:** —\n```\n#### T1.2 — B\n- **Depends on:** —\n",
			wantDeps: nil, wantN: 1,
		},
		{name: "CRLF line endings", plan: strings.ReplaceAll(one("T1.2"), "\n", "\r\n"), wantDeps: []string{"T1.2"}, wantN: 3},
		{name: "no tasks", plan: "# Title\n\nJust prose.\n", wantErr: ErrNoTasks},
		{name: "empty input", plan: "", wantErr: ErrNoTasks},
		{name: "bad heading, no dash", plan: "#### T1.1 Missing dash\n- **Depends on:** —\n", wantErr: ErrBadHeading},
		{name: "bad heading, no title", plan: "#### T1.1 —\n- **Depends on:** —\n", wantErr: ErrBadHeading},
		{name: "bad heading, bad id", plan: "#### T1 — A\n- **Depends on:** —\n", wantErr: ErrBadHeading},
		{name: "duplicate id", plan: "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.1 — B\n- **Depends on:** —\n", wantErr: ErrDuplicateTask},
		{name: "missing depends line", plan: "#### T1.1 — A\n\nText.\n\n#### T1.2 — B\n- **Depends on:** —\n", wantErr: ErrNoDependsLine},
		{name: "missing depends line at end", plan: "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.2 — B\n", wantErr: ErrNoDependsLine},
		{name: "depends line after the next level-3 heading", plan: "#### T1.1 — A\n### Wave\n- **Depends on:** —\n", wantErr: ErrNoDependsLine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.plan))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.wantN {
				t.Fatalf("got %d tasks, want %d", len(got), tt.wantN)
			}
			last := got[len(got)-1]
			if len(last.DependsOn) != len(tt.wantDeps) || (len(tt.wantDeps) > 0 && !reflect.DeepEqual(last.DependsOn, tt.wantDeps)) {
				t.Errorf("DependsOn = %v, want %v", last.DependsOn, tt.wantDeps)
			}
			if last.Note != tt.wantNote {
				t.Errorf("Note = %q, want %q", last.Note, tt.wantNote)
			}
		})
	}
}

// TestParseErrorNamesLine checks the message carries the line and the id,
// which is all a person has to find the mistake.
func TestParseErrorNamesLine(t *testing.T) {
	_, err := Parse([]byte("# T\n\n#### T1.1 — A\n- **Depends on:** —\n\n#### T1.1 — B\n- **Depends on:** —\n"))
	if err == nil || !strings.Contains(err.Error(), "line 6") || !strings.Contains(err.Error(), "T1.1") {
		t.Fatalf("error %v should name line 6 and T1.1", err)
	}
}

func TestParseHeadingFields(t *testing.T) {
	got, err := Parse([]byte("# P\n\n#### T4.2b — Task UI — and more\n- **Depends on:** —\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != "T4.2b" || got[0].Title != "Task UI — and more" || got[0].Line != 3 {
		t.Errorf("got %+v", got[0])
	}
}

// TestValidate: the cycle rows are load-bearing, since a cycle would make a
// runner wait forever, and the self-cycle row because it is the shortest.
func TestValidate(t *testing.T) {
	mk := func(spec ...string) []Task {
		var ts []Task
		for i, s := range spec {
			id, deps, _ := strings.Cut(s, ":")
			task := Task{ID: id, Line: i + 1}
			if deps != "" {
				task.DependsOn = strings.Split(deps, ",")
			}
			ts = append(ts, task)
		}
		return ts
	}
	tests := []struct {
		name    string
		tasks   []Task
		wantErr error
		wantMsg string
	}{
		{name: "valid", tasks: mk("T1.1", "T1.2:T1.1", "T1.3:T1.1,T1.2")},
		{name: "forward reference is fine", tasks: mk("T1.1:T1.2", "T1.2")},
		{name: "unknown dependency", tasks: mk("T1.1", "T1.2:T9.9"), wantErr: ErrUnknownDependency, wantMsg: "T1.2 depends on T9.9"},
		{name: "two-task cycle", tasks: mk("T1.1:T1.2", "T1.2:T1.1"), wantErr: ErrCycle, wantMsg: "T1.1 -> T1.2 -> T1.1"},
		{name: "three-task cycle", tasks: mk("T1.1", "T1.2:T1.3", "T1.3:T1.4", "T1.4:T1.2"), wantErr: ErrCycle, wantMsg: "T1.2 -> T1.3 -> T1.4 -> T1.2"},
		{name: "self cycle", tasks: mk("T1.1:T1.1"), wantErr: ErrCycle, wantMsg: "T1.1 -> T1.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.tasks)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("message %q should contain %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestValidID(t *testing.T) {
	for s, want := range map[string]bool{
		"T1.1": true, "T4.2b": true, "T10.12": true, "T1": false, "t1.1": false,
		"T1.1B": false, "T1.1bc": false, "": false, "T1.1 ": false,
	} {
		if got := ValidID(s); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestParseNebariPlan is the real-world check: nebari's plan, byte for byte,
// must give 40 tasks with these exact dependencies. T9.1 and T9.3 to T9.6 are
// the load-bearing rows (prose with no ids: no dependencies and a Note), T9.2
// ("all prior") and T2.6 (a wrapped line with an unclosed parenthesis).
func TestParseNebariPlan(t *testing.T) {
	want := []struct {
		id   string
		deps []string
	}{
		{"T0.1", nil},
		{"T0.2", []string{"T0.1"}},
		{"T0.3", []string{"T0.2"}},
		{"T1.1", []string{"T0.2"}},
		{"T1.2", []string{"T1.1"}},
		{"T1.3", []string{"T1.2"}},
		{"T1.4", []string{"T1.2"}},
		{"T2.1", []string{"T0.1"}},
		{"T2.2", []string{"T2.1"}},
		{"T2.3", []string{"T2.2"}},
		{"T2.4", []string{"T2.3"}},
		{"T2.5", []string{"T2.3", "T1.3"}},
		{"T2.6", []string{"T2.5", "T0.3", "T2.4"}},
		{"T3.1", []string{"T1.1", "T2.2"}},
		{"T3.2", []string{"T3.1"}},
		{"T3.3", []string{"T3.1"}},
		{"T3.3a", []string{"T3.2", "T3.3"}},
		{"T3.4", []string{"T3.1", "T2.3"}},
		{"T3.5", []string{"T3.2", "T3.3"}},
		{"T4.1", []string{"T1.3", "T2.2"}},
		{"T4.2", []string{"T4.1", "T3.3", "T3.5"}},
		{"T4.2a", []string{"T4.1", "T3.3"}},
		{"T4.2b", []string{"T4.2a", "T3.5"}},
		{"T4.3", []string{"T4.1", "T1.4", "T3.1"}},
		{"T4.4", []string{"T4.2b", "T4.3"}},
		{"T5.1", []string{"T3.2", "T3.3", "T4.1"}},
		{"T5.2", []string{"T5.1"}},
		{"T5.3", []string{"T3.3", "T2.1"}},
		{"T6.1", []string{"T4.2", "T4.3"}},
		{"T7.1", []string{"T1.1", "T0.2"}},
		{"T7.2", []string{"T3.2"}},
		{"T7.3", []string{"T3.5"}},
		{"T8.1", []string{"T3.3"}},
		{"T8.2", []string{"T8.1"}},
		{"T9.1", nil},
		{"T9.2", nil}, // filled below: the 35 ids before it
		{"T9.3", nil},
		{"T9.4", nil},
		{"T9.5", nil},
		{"T9.6", nil},
	}
	for i := range want {
		if want[i].id == "T9.2" {
			for _, w := range want[:i] {
				want[i].deps = append(want[i].deps, w.id)
			}
			if len(want[i].deps) != 35 {
				t.Fatalf("test table: T9.2 should have 35 earlier ids, has %d", len(want[i].deps))
			}
		}
	}

	tasks, err := ParseFile("testdata/nebari-plan.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 40 || len(want) != 40 {
		t.Fatalf("got %d tasks (table has %d), want 40", len(tasks), len(want))
	}
	for i, w := range want {
		got := tasks[i]
		if got.ID != w.id {
			t.Fatalf("task %d is %s, want %s", i, got.ID, w.id)
		}
		if len(got.DependsOn) != len(w.deps) || (len(w.deps) > 0 && !reflect.DeepEqual(got.DependsOn, w.deps)) {
			t.Errorf("%s DependsOn = %v, want %v", got.ID, got.DependsOn, w.deps)
		}
		noteWanted := w.id == "T9.1" || (w.id >= "T9.3" && w.id <= "T9.6")
		if noteWanted != (got.Note != "") {
			t.Errorf("%s Note = %q, want non-empty: %v", got.ID, got.Note, noteWanted)
		}
		if got.Title == "" || got.Line == 0 {
			t.Errorf("%s has empty title or line: %+v", got.ID, got)
		}
	}
	if err := Validate(tasks); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestParseOwnPlan keeps afk's own plan readable by its own parser.
func TestParseOwnPlan(t *testing.T) {
	tasks, err := ParseFile("../../docs/PLAN.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(tasks); err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == "T1.1" {
			return
		}
	}
	t.Fatal("docs/PLAN.md does not contain T1.1")
}

// TestTaskOwner: the "(owner + agent)" and "Owner inbox (owner approval)"
// rows are load-bearing. They are ordinary tasks, and reading them as owner
// tasks would make the daemon skip agent work.
func TestTaskOwner(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  bool
	}{
		{"owner with note", "T0.0 — Stand up the VM (owner; not an agent task)", true},
		{"owner alone", "Stand up the VM (owner)", true},
		{"capitalised", "Stand up the VM (Owner)", true},
		{"spaces inside bracket", "Stand up the VM ( owner )", true},
		{"spaces around semicolon", "Stand up the VM (owner ; by hand)", true},
		{"owner plus agent", "T4.1 — Nebari on the VM (owner + agent)", false},
		{"owner and agent", "Nebari on the VM (owner and agent)", false},
		{"owners", "Collect keys (owners)", false},
		{"owner inbox owner approval", "Owner inbox (owner approval)", false},
		{"no brackets", "Stand up the VM", false},
		{"owner word without brackets", "owner; stand up the VM", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Task{Title: tt.title}).Owner(); got != tt.want {
				t.Errorf("Owner() for %q = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}

// TestDesignSystem: the "Design system (owner + agent)" and "Design system"
// rows are load-bearing. Each is a task the owner must not be asked to do:
// the first is an agent's, the second has no owner bracket at all. Reading
// either as the design task would send the owner a reconcile step for work
// that is not the design hand-off. The "for the admin pages" row guards the
// other direction: a title that only starts with "Design system" is a
// different task, so the match must be the whole title, not a prefix.
func TestDesignSystem(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  bool
	}{
		{"owner bracket alone", "Design system (owner)", true},
		{"owner with note", "Design system (owner; half a day)", true},
		{"capitalised words and bracket", "Design System ( owner )", true},
		{"owner plus agent", "Design system (owner + agent)", false},
		{"no owner bracket", "Design system", false},
		{"longer title", "Design system for the admin pages (owner)", false},
		{"other owner task", "Create the hosting account (owner)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Task{Title: tt.title}).DesignSystem(); got != tt.want {
				t.Errorf("DesignSystem() for %q = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}

// TestScan covers what Scan adds over Parse: it carries on after a problem
// and it reports the headings and the fenced lines. Load-bearing rows:
// "three problems together" (Scan must not stop at the first, and the
// order must be the order Parse would meet them), "duplicate keeps the
// first copy's dependencies" (if the second copy's Depends on line were
// read, the checker would judge dependencies the daemon never sees),
// "bad heading opens no task" (the Depends on line under it must not be
// given to the task before it), and "unclosed fence" (everything after it
// is hidden, as it is for the daemon).
func TestScan(t *testing.T) {
	type wantTask struct {
		id   string
		deps []string
	}
	type wantHeading struct {
		level int
		text  string
		line  int
	}
	type wantProblem struct {
		line int
		task string
		err  error
	}
	tests := []struct {
		name         string
		plan         string
		wantTasks    []wantTask
		wantProblems []wantProblem
		wantHeadings []wantHeading // nil means the row does not check headings
		wantFenced   []bool        // nil means the row does not check fences
		sameAsParse  bool          // Tasks must equal what Parse returns
	}{
		{
			name:        "clean plan",
			plan:        one("T1.1, T1.2"),
			wantTasks:   []wantTask{{"T1.1", nil}, {"T1.2", []string{"T1.1"}}, {"T1.3", []string{"T1.1", "T1.2"}}},
			sameAsParse: true,
		},
		{
			name: "three problems together",
			plan: "#### T1.1 — A\n- **Depends on:** —\n\n" + // line 1
				"#### T1.2 Missing dash\n- **Depends on:** —\n\n" + // line 4
				"#### T1.1 — Again\n- **Depends on:** —\n\n" + // line 7
				"#### T1.3 — C\n\nText.\n\n" + // line 10
				"#### T1.4 — D\n- **Depends on:** T1.3\n", // line 14
			wantTasks: []wantTask{{"T1.1", nil}, {"T1.3", nil}, {"T1.4", []string{"T1.3"}}},
			wantProblems: []wantProblem{
				{line: 4, task: "", err: ErrBadHeading},
				{line: 7, task: "T1.1", err: ErrDuplicateTask},
				{line: 10, task: "T1.3", err: ErrNoDependsLine},
			},
		},
		{
			name: "duplicate keeps the first copy's dependencies",
			plan: "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.2 — B\n- **Depends on:** T1.1\n\n" +
				"#### T1.2 — B again\n- **Depends on:** T9.9\n",
			wantTasks:    []wantTask{{"T1.1", nil}, {"T1.2", []string{"T1.1"}}},
			wantProblems: []wantProblem{{line: 7, task: "T1.2", err: ErrDuplicateTask}},
		},
		{
			name:         "duplicate with no depends line is one problem",
			plan:         "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.1 — Again\n\nText.\n",
			wantTasks:    []wantTask{{"T1.1", nil}},
			wantProblems: []wantProblem{{line: 4, task: "T1.1", err: ErrDuplicateTask}},
		},
		{
			name:      "bad heading opens no task",
			plan:      "#### T1.1 — A\n#### T1 — Bad id\n- **Depends on:** T9.9\n",
			wantTasks: []wantTask{{"T1.1", nil}},
			wantProblems: []wantProblem{
				{line: 1, task: "T1.1", err: ErrNoDependsLine},
				{line: 2, task: "", err: ErrBadHeading},
			},
		},
		{
			name:         "missing depends line at end of file",
			plan:         "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.2 — B\n",
			wantTasks:    []wantTask{{"T1.1", nil}, {"T1.2", nil}},
			wantProblems: []wantProblem{{line: 4, task: "T1.2", err: ErrNoDependsLine}},
		},
		{
			name:         "no tasks is not a problem",
			plan:         "# Title\n\nJust prose.\n",
			wantHeadings: []wantHeading{{1, "Title", 1}},
			wantFenced:   []bool{false, false, false, false},
		},
		{
			name:         "empty input is one unfenced line",
			plan:         "",
			wantHeadings: []wantHeading{},
			wantFenced:   []bool{false},
		},
		{
			name:         "fenced example is not a task",
			plan:         "#### T1.1 — A\n- **Depends on:** —\n\n```markdown\n#### T9.9 — Example\n- **Depends on:** T1.1\n```\nAfter.\n",
			wantTasks:    []wantTask{{"T1.1", nil}},
			wantHeadings: []wantHeading{{4, "T1.1 — A", 1}},
			wantFenced:   []bool{false, false, false, true, true, true, true, false, false},
			sameAsParse:  true,
		},
		{
			name:        "tilde fence, indented",
			plan:        "#### T1.1 — A\n  ~~~\n- **Depends on:** T9.9\n  ~~~\n- **Depends on:** —\n",
			wantTasks:   []wantTask{{"T1.1", nil}},
			wantFenced:  []bool{false, true, true, true, false, false},
			sameAsParse: true,
		},
		{
			name:      "headings of levels 1 to 4, not 5",
			plan:      "# Plan\n\n## 8. Milestones\n### M1 — First\n#### T1.1 — A\n##### Sub heading\n- **Depends on:** —\n###### Six\n#NoSpace\n####\n",
			wantTasks: []wantTask{{"T1.1", nil}},
			wantHeadings: []wantHeading{
				{1, "Plan", 1},
				{2, "8. Milestones", 3},
				{3, "M1 — First", 4},
				{4, "T1.1 — A", 5},
			},
			sameAsParse: true,
		},
		{
			name:      "problem headings are still listed",
			plan:      "#### T1.1 — A\n- **Depends on:** —\n#### T1.1 — Again\n#### T2 — Bad\n",
			wantTasks: []wantTask{{"T1.1", nil}},
			wantProblems: []wantProblem{
				{line: 3, task: "T1.1", err: ErrDuplicateTask},
				{line: 4, task: "", err: ErrBadHeading},
			},
			wantHeadings: []wantHeading{{4, "T1.1 — A", 1}, {4, "T1.1 — Again", 3}, {4, "T2 — Bad", 4}},
		},
		{
			name:         "unclosed fence",
			plan:         "#### T1.1 — A\n- **Depends on:** —\n```\n#### T1.2 — B\n## Hidden\n",
			wantTasks:    []wantTask{{"T1.1", nil}},
			wantHeadings: []wantHeading{{4, "T1.1 — A", 1}},
			wantFenced:   []bool{false, false, true, true, true, true},
			sameAsParse:  true,
		},
		{
			name:         "CRLF line endings",
			plan:         "## Section\r\n#### T1.1 — A\r\n- **Depends on:** —\r\n",
			wantTasks:    []wantTask{{"T1.1", nil}},
			wantHeadings: []wantHeading{{2, "Section", 1}, {4, "T1.1 — A", 2}},
			wantFenced:   []bool{false, false, false, false},
			sameAsParse:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan([]byte(tt.plan))

			if len(got.Tasks) != len(tt.wantTasks) {
				t.Fatalf("got %d tasks %+v, want %d", len(got.Tasks), got.Tasks, len(tt.wantTasks))
			}
			for i, w := range tt.wantTasks {
				g := got.Tasks[i]
				if g.ID != w.id {
					t.Errorf("task %d is %s, want %s", i, g.ID, w.id)
				}
				if len(g.DependsOn) != len(w.deps) || (len(w.deps) > 0 && !reflect.DeepEqual(g.DependsOn, w.deps)) {
					t.Errorf("%s DependsOn = %v, want %v", g.ID, g.DependsOn, w.deps)
				}
			}

			if len(got.Problems) != len(tt.wantProblems) {
				t.Fatalf("got %d problems %+v, want %d", len(got.Problems), got.Problems, len(tt.wantProblems))
			}
			for i, w := range tt.wantProblems {
				g := got.Problems[i]
				if g.Line != w.line || g.Task != w.task {
					t.Errorf("problem %d is line %d task %q, want line %d task %q", i, g.Line, g.Task, w.line, w.task)
				}
				if !errors.Is(g.Err, w.err) {
					t.Errorf("problem %d err = %v, want %v", i, g.Err, w.err)
				}
			}

			if tt.wantHeadings != nil {
				var gotH []wantHeading
				for _, h := range got.Headings {
					gotH = append(gotH, wantHeading{h.Level, h.Text, h.Line})
				}
				if len(gotH) != len(tt.wantHeadings) || (len(gotH) > 0 && !reflect.DeepEqual(gotH, tt.wantHeadings)) {
					t.Errorf("Headings = %+v, want %+v", gotH, tt.wantHeadings)
				}
			}
			if tt.wantFenced != nil && !reflect.DeepEqual(got.Fenced, tt.wantFenced) {
				t.Errorf("Fenced = %v, want %v", got.Fenced, tt.wantFenced)
			}
			if n := len(strings.Split(tt.plan, "\n")); len(got.Fenced) != n {
				t.Errorf("Fenced has %d entries, want one per line (%d)", len(got.Fenced), n)
			}

			if tt.sameAsParse {
				parsed, err := Parse([]byte(tt.plan))
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if !reflect.DeepEqual(got.Tasks, parsed) {
					t.Errorf("Tasks = %+v, Parse gave %+v", got.Tasks, parsed)
				}
			}
		})
	}
}

// TestParseMatchesScan pins the promise that the daemon (Parse) and the
// checker (Scan) cannot disagree about a plan: Parse's error is the first
// Problem's, message and all, and a plan with no Problem gives the same
// tasks from both. The inputs are the error rows of TestParse, copied
// because that table is local to its test, plus the two real plans.
// Load-bearing rows: "depends line after the next level-3 heading" (the
// problem is found at a later line than the one it is reported at) and
// the two files (the plans the daemon actually reads).
func TestParseMatchesScan(t *testing.T) {
	file := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	tests := []struct {
		name string
		plan string
	}{
		{"no tasks", "# Title\n\nJust prose.\n"},
		{"empty input", ""},
		{"bad heading, no dash", "#### T1.1 Missing dash\n- **Depends on:** —\n"},
		{"bad heading, no title", "#### T1.1 —\n- **Depends on:** —\n"},
		{"bad heading, bad id", "#### T1 — A\n- **Depends on:** —\n"},
		{"duplicate id", "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.1 — B\n- **Depends on:** —\n"},
		{"missing depends line", "#### T1.1 — A\n\nText.\n\n#### T1.2 — B\n- **Depends on:** —\n"},
		{"missing depends line at end", "#### T1.1 — A\n- **Depends on:** —\n\n#### T1.2 — B\n"},
		{"depends line after the next level-3 heading", "#### T1.1 — A\n### Wave\n- **Depends on:** —\n"},
		{"missing depends line then bad heading", "#### T1.1 — A\n#### T1 — Bad\n"},
		{"clean", one("all prior")},
		{"nebari plan", file("testdata/nebari-plan.md")},
		{"own plan", file("../../docs/PLAN.md")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tasks, err := Parse([]byte(tt.plan))
			o := Scan([]byte(tt.plan))
			switch {
			case len(o.Problems) > 0:
				first := o.Problems[0].Err
				if err == nil || err.Error() != first.Error() {
					t.Fatalf("Parse err = %v, first Problem = %v", err, first)
				}
				if tasks != nil {
					t.Errorf("Parse returned tasks with an error: %+v", tasks)
				}
				for _, sentinel := range []error{ErrBadHeading, ErrDuplicateTask, ErrNoDependsLine} {
					if errors.Is(err, sentinel) != errors.Is(first, sentinel) {
						t.Errorf("Parse and Scan disagree on %v", sentinel)
					}
				}
			case len(o.Tasks) == 0:
				if !errors.Is(err, ErrNoTasks) {
					t.Fatalf("Parse err = %v, want %v", err, ErrNoTasks)
				}
			default:
				if err != nil {
					t.Fatalf("Parse err = %v, Scan found no problem", err)
				}
				if !reflect.DeepEqual(tasks, o.Tasks) {
					t.Errorf("Parse tasks differ from Scan's")
				}
			}
		})
	}
}
