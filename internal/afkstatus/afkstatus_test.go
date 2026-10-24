package afkstatus

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/progstatus"
	"github.com/dphaener/afk/internal/projectrows"
)

type want struct {
	state progstatus.State
	kind  progstatus.Kind
	msg   string
}

// TestRecords pins the mapping from rows to records. Load-bearing: the
// blocked-outranks-stopped row (the owner's queue comes first), the
// paused-machine and stopped-line rows (the master plan's examples), and the
// three-blocking row (the "and K more" count and the "things" plural).
func TestRecords(t *testing.T) {
	tests := []struct {
		name  string
		rows  []projectrows.Row
		root  want
		child []want
	}{
		{"nothing running", nil, want{progstatus.StateIdle, "", "Nothing running"}, nil},
		{"nothing ready", []projectrows.Row{{Name: "a", State: projectrows.StateNothingReady, Label: "nothing ready"}},
			want{progstatus.StateIdle, "", "Nothing running"},
			[]want{{progstatus.StateIdle, "", "nothing ready"}}},
		{"task running", []projectrows.Row{{Name: "web", State: projectrows.StateRunning, Task: "T1", Title: "Add form", Phase: "build"}},
			want{progstatus.StateWorking, "", "web: T1 Add form · build"},
			[]want{{progstatus.StateWorking, "", "T1 Add form · build"}}},
		{"running with no phase or title", []projectrows.Row{{Name: "web", State: projectrows.StateRunning, Task: "T1"}},
			want{progstatus.StateWorking, "", "web: T1"},
			[]want{{progstatus.StateWorking, "", "T1"}}},
		{"one inbox item", []projectrows.Row{{Name: "web", State: projectrows.StateRunning, Blocking: 1, Wait: projectrows.Wait{Title: "Pick a name\nmore"}}},
			want{progstatus.StateBlocked, progstatus.KindQuestion, "1 thing needs you: web: Pick a name"},
			[]want{{progstatus.StateBlocked, progstatus.KindQuestion, "Pick a name"}}},
		{"three blocking items", []projectrows.Row{{Name: "web", Blocking: 3, Wait: projectrows.Wait{Title: "Pick a name"}}},
			want{progstatus.StateBlocked, progstatus.KindQuestion, "3 things need you: web: Pick a name and 2 more"},
			[]want{{progstatus.StateBlocked, progstatus.KindQuestion, "Pick a name and 2 more"}}},
		{"plan waiting", []projectrows.Row{{Name: "web", State: projectrows.StateFinished, Label: "finished", PlanWaiting: true}},
			want{progstatus.StateBlocked, progstatus.KindQuestion, "1 thing needs you: web: a plan to approve"},
			[]want{{progstatus.StateBlocked, progstatus.KindQuestion, "a plan to approve"}}},
		{"waiting row without an item", []projectrows.Row{{Name: "web", State: projectrows.StateWaiting, Detail: "Waiting on you: check the login"}},
			want{progstatus.StateBlocked, progstatus.KindQuestion, "1 thing needs you: web: Waiting on you: check the login"},
			[]want{{progstatus.StateBlocked, progstatus.KindQuestion, "Waiting on you: check the login"}}},
		{"waiting row with an item counts once", []projectrows.Row{{Name: "web", State: projectrows.StateWaiting, Detail: "x", Blocking: 1, Wait: projectrows.Wait{Title: "Q"}}},
			want{progstatus.StateBlocked, progstatus.KindQuestion, "1 thing needs you: web: Q"},
			[]want{{progstatus.StateBlocked, progstatus.KindQuestion, "Q"}}},
		{"stopped line", []projectrows.Row{{Name: "web", State: projectrows.StateStopped, Detail: "Stopped at T3: tests failed."}},
			want{progstatus.StateError, "", "web: Stopped at T3: tests failed."},
			[]want{{progstatus.StateError, "", "Stopped at T3: tests failed."}}},
		{"gone project", []projectrows.Row{{Name: "old", State: projectrows.StateGone, Detail: "The project folder does not exist."}},
			want{progstatus.StateError, "", "old: The project folder does not exist."},
			[]want{{progstatus.StateError, "", "The project folder does not exist."}}},
		{"paused machine", []projectrows.Row{
			{Name: "a", State: projectrows.StatePaused, Label: "paused"},
			{Name: "b", State: projectrows.StatePaused, Label: "paused"}},
			want{progstatus.StateIdle, "", "a paused; b paused"},
			[]want{{progstatus.StateIdle, "", "paused"}, {progstatus.StateIdle, "", "paused"}}},
		{"held project", []projectrows.Row{{Name: "a", State: projectrows.StateHeld, Label: "held for usage"}},
			want{progstatus.StateIdle, "", "a held for usage"},
			[]want{{progstatus.StateIdle, "", "held for usage"}}},
		{"blocked outranks stopped", []projectrows.Row{
			{Name: "a", State: projectrows.StateStopped, Detail: "Stopped."},
			{Name: "b", Blocking: 1, Wait: projectrows.Wait{Title: "Q"}}},
			want{progstatus.StateBlocked, progstatus.KindQuestion, "1 thing needs you: b: Q"},
			[]want{{progstatus.StateError, "", "Stopped."}, {progstatus.StateBlocked, progstatus.KindQuestion, "Q"}}},
		{"error outranks working", []projectrows.Row{
			{Name: "a", State: projectrows.StateRunning, Task: "T1"},
			{Name: "b", State: projectrows.StateStopped, Detail: "Stopped."}},
			want{progstatus.StateError, "", "b: Stopped."},
			[]want{{progstatus.StateWorking, "", "T1"}, {progstatus.StateError, "", "Stopped."}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Records(tt.rows)
			if len(got) != len(tt.rows)+1 {
				t.Fatalf("got %d records, want %d", len(got), len(tt.rows)+1)
			}
			check := func(label string, r progstatus.Record, w want, id string) {
				if r.State != w.state || r.Kind != w.kind || r.Msg != w.msg || r.ID != id || r.App != "afk" {
					t.Errorf("%s = %+v, want state=%s kind=%s msg=%q id=%q app=afk", label, r, w.state, w.kind, w.msg, id)
				}
			}
			check("root", got[0], tt.root, "")
			for i, w := range tt.child {
				check("child "+tt.rows[i].Name, got[i+1], w, got[i+1].ID)
				if got[i+1].ID == "" || got[i+1].Title != tt.rows[i].Name {
					t.Errorf("child %d id %q title %q", i, got[i+1].ID, got[i+1].Title)
				}
			}
		})
	}
}

// TestRecordsHostile: hostile names and titles still give records Encode
// accepts. Load-bearing: the clash row (two names, one sanitized id), the
// newline row (a control character never reaches the wire) and the long item
// title (cut under the limit on a rune boundary).
func TestRecordsHostile(t *testing.T) {
	tests := []struct {
		name   string
		rows   []projectrows.Row
		ids    []string
		inMsg  string
		noCtrl bool
	}{
		{"unicode and spaces and slash", []projectrows.Row{{Name: "My App/é ü"}}, []string{"my-app------"}, "", false},
		{"unicode name keeps real title", []projectrows.Row{{Name: "日本語"}}, []string{"---------"}, "", false},
		{"clash", []projectrows.Row{{Name: "a b"}, {Name: "a/b"}, {Name: "A B"}}, []string{"a-b", "a-b-2", "a-b-3"}, "", false},
		{"clash at the length cap", []projectrows.Row{{Name: strings.Repeat("x", 40)}, {Name: strings.Repeat("x", 41)}},
			[]string{strings.Repeat("x", 32), strings.Repeat("x", 30) + "-2"}, "", false},
		{"empty name", []projectrows.Row{{Name: ""}}, []string{"project"}, "", false},
		{"newline in title", []projectrows.Row{{Name: "p\nq", Blocking: 1, Wait: projectrows.Wait{Title: "a\tb\x7f\u0085c"}}}, []string{"p-q"}, "a b  c", true},
		{"long item title", []projectrows.Row{{Name: "p", Blocking: 1, Wait: projectrows.Wait{Title: strings.Repeat("é", 3000)}}}, []string{"p"}, "", false},
		{"long running line", []projectrows.Row{{Name: "p", State: projectrows.StateRunning, Task: strings.Repeat("日", 1000), Phase: "x"}}, []string{"p"}, "", false},
		{"long name", []projectrows.Row{{Name: strings.Repeat("é", 300)}}, []string{strings.Repeat("-", 32)}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := Records(tt.rows)
			for i, id := range tt.ids {
				if recs[i+1].ID != id {
					t.Errorf("id %d = %q, want %q", i, recs[i+1].ID, id)
				}
			}
			for _, r := range recs {
				if _, err := progstatus.Encode(r); err != nil {
					t.Errorf("Encode(%+v): %v", r, err)
				}
				if len(r.Msg) > progstatus.MaxMsgDecoded || len(r.Title) > progstatus.MaxTitleDecoded {
					t.Errorf("over limit: msg %d title %d", len(r.Msg), len(r.Title))
				}
			}
			if tt.inMsg != "" && !strings.Contains(recs[1].Msg, tt.inMsg) {
				t.Errorf("child msg %q lacks %q", recs[1].Msg, tt.inMsg)
			}
		})
	}
}

// TestDiff pins the send-on-change rule. Load-bearing: equal sets give nil
// (nothing re-sent on a quiet tick), a gone project is cleared by id before
// anything else, and only the changed child is written.
func TestDiff(t *testing.T) {
	a := projectrows.Row{Name: "a", State: projectrows.StateNothingReady, Label: "nothing ready"}
	b := projectrows.Row{Name: "b", State: projectrows.StateNothingReady, Label: "nothing ready"}
	bRun := projectrows.Row{Name: "b", State: projectrows.StateRunning, Task: "T1", Phase: "build"}
	enc := func(rs ...progstatus.Record) string {
		var out []byte
		for _, r := range rs {
			x, err := progstatus.Encode(r)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, x...)
		}
		return string(out)
	}
	base := Records([]projectrows.Row{a, b})
	tests := []struct {
		name       string
		prev, next []progstatus.Record
		want       string
		wantNil    bool
	}{
		{"equal", base, Records([]projectrows.Row{a, b}), "", true},
		{"both empty", nil, nil, "", true},
		{"first send", nil, base, enc(base...), false},
		{"one child changed", base, Records([]projectrows.Row{a, bRun}), "", false},
		{"nothing changed while running", Records([]projectrows.Row{a, bRun}), Records([]projectrows.Row{a, bRun}), "", true},
		{"project gone", base, Records([]projectrows.Row{a}), enc(progstatus.Record{State: progstatus.StateClear, ID: "b"}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Diff(tt.prev, tt.next)
			if tt.wantNil {
				if got != nil {
					t.Errorf("Diff = %q, want nil", got)
				}
				return
			}
			switch tt.name {
			case "one child changed":
				next := Records([]projectrows.Row{a, bRun})
				// A running task moves the root as well as child b.
				if string(got) != enc(next[0], next[2]) {
					t.Errorf("Diff = %q, want root and child b only", got)
				}
			case "project gone":
				// Clear first, then the root, which now has one project.
				next := Records([]projectrows.Row{a})
				w := tt.want
				if next[0] != base[0] {
					w += enc(next[0])
				}
				if string(got) != w {
					t.Errorf("Diff = %q, want %q", got, w)
				}
			default:
				if string(got) != tt.want {
					t.Errorf("Diff = %q, want %q", got, tt.want)
				}
			}
		})
	}

	t.Run("only the child changed", func(t *testing.T) {
		// Two running projects: b's phase moves, the root text moves too, but
		// a's record must not be re-sent.
		r1 := []projectrows.Row{{Name: "a", State: projectrows.StateRunning, Task: "T1"}, {Name: "b", State: projectrows.StateRunning, Task: "T2", Phase: "build"}}
		r2 := []projectrows.Row{{Name: "a", State: projectrows.StateRunning, Task: "T1"}, {Name: "b", State: projectrows.StateRunning, Task: "T2", Phase: "review"}}
		p, n := Records(r1), Records(r2)
		if got := string(Diff(p, n)); got != enc(n[0], n[2]) {
			t.Errorf("Diff = %q, want root and b", got)
		}
	})
	t.Run("root changed only", func(t *testing.T) {
		p := []progstatus.Record{{State: progstatus.StateIdle, App: "afk", Msg: "x"}}
		n := []progstatus.Record{{State: progstatus.StateIdle, App: "afk", Msg: "y"}}
		if got := string(Diff(p, n)); got != enc(n[0]) {
			t.Errorf("Diff = %q", got)
		}
	})
	t.Run("a refused record is skipped", func(t *testing.T) {
		n := []progstatus.Record{{State: "bogus"}}
		if got := Diff(nil, n); len(got) != 0 {
			t.Errorf("Diff = %q, want nothing", got)
		}
	})
}

// TestClearAll: the root clear is the bare state=clear report.
func TestClearAll(t *testing.T) {
	if got, want := ClearAll(), []byte("\x1b]7501;state=clear\x1b\\"); !bytes.Equal(got, want) {
		t.Errorf("ClearAll = %q, want %q", got, want)
	}
}
