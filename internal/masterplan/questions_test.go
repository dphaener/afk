package masterplan

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// oq wraps table rows in a plan with an Open questions section.
func oq(rows ...string) string {
	return "# Plan\n\n## 9. Open questions\n\n| Question | Needed by |\n|---|---|\n" + strings.Join(rows, "\n") + "\n\n## 10. Done\n"
}

// TestQuestions covers the table reader that the checker and the daemon
// share. Load-bearing rows: "answered inside the cell" and "answer as its own
// row" (the open rule is the marker in the question cell and nothing looser),
// "no task id" (an empty NeededBy is what lets a row hold nothing), "escaped
// pipe" and "pipe in a code span" (a wrong split shifts Needed by onto the
// wrong text), "table in a code fence" and "only the first table" (the
// checker's old rules), and the nebari fixture (a real plan).
func TestQuestions(t *testing.T) {
	type want struct {
		line     int
		text     string
		neededBy []string
		open     bool
	}
	tests := []struct {
		name string
		plan string
		want []want
	}{
		{"answered inside the cell", oq("| Pick a name **Answered 2026-01-02:** foo | T1.1 |"),
			[]want{{7, "Pick a name **Answered 2026-01-02:** foo", []string{"T1.1"}, false}}},
		{"answer as its own row", oq("| Pick a name | T1.1 |", "| **Answered:** foo | T1.1 |"),
			[]want{{7, "Pick a name", []string{"T1.1"}, true}, {8, "**Answered:** foo", []string{"T1.1"}, false}}},
		{"no task id", oq("| Pick a name | before launch |"),
			[]want{{7, "Pick a name", nil, true}}},
		{"after names the task", oq("| Pick a name | after T2.4 |"),
			[]want{{7, "Pick a name", []string{"T2.4"}, true}}},
		{"two ids", oq("| Pick a name | T1.1, T1.2 and T1.1 |"),
			[]want{{7, "Pick a name", []string{"T1.1", "T1.2"}, true}}},
		{"id inside backticks", oq("| Pick a name | `T3.1` |"),
			[]want{{7, "Pick a name", []string{"T3.1"}, true}}},
		{"escaped pipe", oq(`| a \| b | T1.1 |`),
			[]want{{7, "a | b", []string{"T1.1"}, true}}},
		{"pipe in a code span", oq("| run `a | b` now | T1.1 |"),
			[]want{{7, "run `a | b` now", []string{"T1.1"}, true}}},
		{"table in a code fence", "## Open questions\n\n```\n| Question | Needed by |\n|---|---|\n| x | T1.1 |\n```\n", nil},
		{"only the first table", oq("| one | T1.1 |") + "\n## Open questions\n\n| Question | Needed by |\n|---|---|\n| two | T1.2 |\n",
			[]want{{7, "one", []string{"T1.1"}, true}}},
		{"no open questions section", "# Plan\n\n| Question | Needed by |\n|---|---|\n| x | T1.1 |\n", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Questions([]byte(tc.plan))
			var gw []want
			for _, q := range got {
				gw = append(gw, want{q.Line, q.Text, q.NeededBy, q.Open})
				lines := strings.Split(tc.plan, "\n")
				if q.Row != lines[q.Line-1] {
					t.Errorf("Row = %q, want %q", q.Row, lines[q.Line-1])
				}
			}
			if !reflect.DeepEqual(gw, tc.want) {
				t.Errorf("got %+v, want %+v", gw, tc.want)
			}
		})
	}

	t.Run("nebari fixture", func(t *testing.T) {
		data, err := os.ReadFile("testdata/nebari-plan.md")
		if err != nil {
			t.Fatal(err)
		}
		open, t94 := 0, 0
		for _, q := range Questions(data) {
			if !q.Open {
				continue
			}
			open++
			for _, id := range q.NeededBy {
				if id == "T9.4" {
					t94++
				}
			}
		}
		if open != 7 || t94 != 2 {
			t.Errorf("open = %d, T9.4 rows = %d; want 7 and 2", open, t94)
		}
	})
}

// TestAnswerRow covers the answered form of a row. Load-bearing rows: "a
// question that is not in the plan", "a row already answered" and "two open
// rows with the same text" (each must refuse and leave the plan alone, since
// the daemon treats ok=false as "do not write"), "newline and pipe" (the
// answer is one line and its pipe cannot end the cell), and "padded" (padding
// is left alone). Every ok row is also read back: the row is no longer open,
// keeps its NeededBy, and cannot be answered a second time.
func TestAnswerRow(t *testing.T) {
	tests := []struct {
		name     string
		plan     string
		question string
		answer   string
		wantNew  string // the whole new line; empty when ok is false
	}{
		{"a plain row", oq("| Pick a name | T1.1 |"), "Pick a name", "Foo",
			"| Pick a name **Answered 2026-05-06:** Foo | T1.1 |"},
		{"newline and pipe", oq("| Pick a name | T1.1 |"), "Pick a name", " use a\n\n b | c\t",
			`| Pick a name **Answered 2026-05-06:** use a b \| c | T1.1 |`},
		{"padded", oq("| Pick a name        | T1.1 |"), "Pick a name", "Foo",
			"| Pick a name **Answered 2026-05-06:** Foo        | T1.1 |"},
		{"escaped pipe in the question", oq(`| a \| b | T1.1 |`), "a | b", "Foo",
			`| a \| b **Answered 2026-05-06:** Foo | T1.1 |`},
		{"answer ends in a backslash", oq("| Pick a name | T1.1 |"), "Pick a name", `C:\`,
			`| Pick a name **Answered 2026-05-06:** C:\ | T1.1 |`},
		{"answer ends in a backslash, no padding", oq("| Pick a name| T1.1 |"), "Pick a name", `C:\`,
			`| Pick a name **Answered 2026-05-06:** C:\ | T1.1 |`},
		{"a question that is not in the plan", oq("| Pick a name | T1.1 |"), "Other", "Foo", ""},
		{"a row already answered", oq("| Pick a name **Answered 2026-01-01:** x | T1.1 |"), "Pick a name **Answered 2026-01-01:** x", "Foo", ""},
		{"two open rows with the same text", oq("| Pick a name | T1.1 |", "| Pick a name | T1.2 |"), "Pick a name", "Foo", ""},
		{"a stray backtick cannot swallow the pipes", oq("| Pick a name | `T1.1` |"), "Pick a name", "use ` here", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := []byte(tc.plan)
			before := string(plan)
			old, nw, ok := AnswerRow(plan, tc.question, "2026-05-06", tc.answer)
			if string(plan) != before {
				t.Fatal("AnswerRow changed its input")
			}
			if tc.wantNew == "" {
				if ok || old != nil || nw != nil {
					t.Fatalf("ok = %v, old = %q, new = %q; want a refusal", ok, old, nw)
				}
				return
			}
			if !ok {
				t.Fatal("ok = false, want true")
			}
			if string(nw) != tc.wantNew {
				t.Errorf("new = %q, want %q", nw, tc.wantNew)
			}
			if !strings.Contains(tc.plan, string(old)+"\n") {
				t.Errorf("old = %q is not a line of the plan", old)
			}
			patched := strings.Replace(tc.plan, string(old), string(nw), 1)
			orig, now := Questions(plan), Questions([]byte(patched))
			if len(orig) != len(now) {
				t.Fatalf("row count changed: %d to %d", len(orig), len(now))
			}
			for i := range orig {
				if orig[i].Text == tc.question && orig[i].Open {
					if now[i].Open {
						t.Errorf("row still open after the answer")
					}
					if !reflect.DeepEqual(now[i].NeededBy, orig[i].NeededBy) {
						t.Errorf("NeededBy %v, want %v", now[i].NeededBy, orig[i].NeededBy)
					}
					if !strings.HasPrefix(now[i].Text, orig[i].Text) {
						t.Errorf("question lost: %q", now[i].Text)
					}
				}
			}
			if _, _, again := AnswerRow([]byte(patched), now[0].Text, "2026-05-07", "Bar"); again {
				t.Error("a second AnswerRow on the patched text returned ok")
			}
		})
	}
}
