package masterplan

import (
	"errors"
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
