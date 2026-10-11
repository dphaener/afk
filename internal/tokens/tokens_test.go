package tokens

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

var testNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.FixedZone("x", 3600))

// TestWeighted: the "each field alone" rows pin each weight separately, so a
// swapped weight cannot hide behind a sum; "above 2^31" fails if the maths
// is done in int32.
func TestWeighted(t *testing.T) {
	cases := []struct {
		name            string
		in, out, cw, cr int64
		want            float64
	}{
		{"all zeros", 0, 0, 0, 0, 0},
		{"input alone", 100, 0, 0, 0, 100},
		{"output alone", 0, 100, 0, 0, 500},
		{"cache write alone", 0, 0, 100, 0, 125},
		{"cache read alone", 0, 0, 0, 100, 10},
		{"all four together", 100, 100, 100, 100, 735},
		{"value above 2^31", 3_000_000_000, 0, 0, 0, 3_000_000_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Weighted(c.in, c.out, c.cw, c.cr); got != c.want {
				t.Fatalf("Weighted = %v, want %v", got, c.want)
			}
		})
	}
}

func rec(phase, role, model string, in, out, cw, cr int64) provider.TokenRecord {
	return provider.TokenRecord{Phase: phase, Role: role, Model: model, Input: in, Output: out, CacheWrite: cw, CacheRead: cr}
}

// TestAggregate: the two-phase, two-role and two-model rows are the grouping
// key (A9); the shuffled input makes the ordering row fail if the sort is
// missing, and "other" phases must sort after the known five.
func TestAggregate(t *testing.T) {
	t.Run("two phases two roles one role on two models", func(t *testing.T) {
		recs := []provider.TokenRecord{
			rec("review", "critic", "m1", 1, 1, 0, 0),
			rec("execute", "task", "m2", 10, 2, 4, 100),
			rec("execute", "task", "m1", 1, 1, 1, 1),
			rec("execute", "task", "m1", 2, 3, 0, 0),
			rec("execute", "verifier", "m1", 5, 0, 0, 0),
		}
		got := Aggregate(recs, "T1.4", "s", testNow)
		if len(got) != 4 {
			t.Fatalf("got %d lines, want 4: %+v", len(got), got)
		}
		wantKeys := [][3]string{{"execute", "task", "m1"}, {"execute", "task", "m2"}, {"execute", "verifier", "m1"}, {"review", "critic", "m1"}}
		for i, k := range wantKeys {
			if got[i].Phase != k[0] || got[i].Role != k[1] || got[i].Model != k[2] {
				t.Fatalf("line %d = %s/%s/%s, want %v", i, got[i].Phase, got[i].Role, got[i].Model, k)
			}
		}
		l := got[0]
		if l.Calls != 2 || l.Input != 3 || l.Output != 4 || l.CacheWrite != 1 || l.CacheRead != 1 {
			t.Fatalf("sums wrong: %+v", l)
		}
		if l.Weighted != Weighted(3, 4, 1, 1) {
			t.Fatalf("weighted %v", l.Weighted)
		}
		if l.At != "2026-10-11T11:00:00Z" || l.Task != "T1.4" || l.Session != "s" {
			t.Fatalf("stamp wrong: %+v", l)
		}
	})
	t.Run("ordering", func(t *testing.T) {
		recs := []provider.TokenRecord{
			rec("zeta", "a", "m", 1, 0, 0, 0),
			rec("ship", "a", "m", 1, 0, 0, 0),
			rec("alpha", "a", "m", 1, 0, 0, 0),
			rec("fix", "a", "m", 1, 0, 0, 0),
			rec("review", "b", "m", 1, 0, 0, 0),
			rec("review", "a", "m", 1, 0, 0, 0),
			rec("execute", "a", "m", 1, 0, 0, 0),
			rec("plan", "a", "m", 1, 0, 0, 0),
		}
		var order []string
		for _, l := range Aggregate(recs, "", "s", testNow) {
			order = append(order, l.Phase+"/"+l.Role)
		}
		want := "plan/a execute/a review/a review/b fix/a ship/a alpha/a zeta/a"
		if strings.Join(order, " ") != want {
			t.Fatalf("order = %v, want %s", order, want)
		}
	})
	t.Run("no records", func(t *testing.T) {
		if got := Aggregate(nil, "", "s", testNow); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
}

// TestLedger: "second append adds lines without rewriting the first" is the
// load-bearing row, since the ledger is shared and append-only.
func TestLedger(t *testing.T) {
	a := Line{At: "2026-10-11T11:00:00Z", Task: "T1.4", Session: "s1", Phase: "plan", Role: "planner", Model: "m", Calls: 2, Input: 1, Output: 2, CacheWrite: 3, CacheRead: 4, Weighted: 5}
	b := a
	b.Session = "s2"

	t.Run("append then read round-trips and second append adds", func(t *testing.T) {
		path := LedgerPath(t.TempDir())
		if !strings.HasSuffix(path, filepath.Join(".afk", "log", "tokens.jsonl")) {
			t.Fatalf("path %s", path)
		}
		if err := Append(path, []Line{a}); err != nil {
			t.Fatal(err)
		}
		first, _ := os.ReadFile(path)
		if err := Append(path, []Line{b}); err != nil {
			t.Fatal(err)
		}
		second, _ := os.ReadFile(path)
		if !bytes.HasPrefix(second, first) {
			t.Fatal("second append rewrote the first line")
		}
		got, err := Read(path)
		if err != nil || !reflect.DeepEqual(got, []Line{a, b}) {
			t.Fatalf("got %+v, err %v", got, err)
		}
		for _, key := range []string{`"at"`, `"task"`, `"session"`, `"phase"`, `"role"`, `"model"`, `"calls"`, `"input"`, `"output"`, `"cache_write"`, `"cache_read"`, `"weighted"`} {
			if !bytes.Contains(first, []byte(key)) {
				t.Fatalf("missing key %s in %s", key, first)
			}
		}
	})
	t.Run("garbage line is skipped", func(t *testing.T) {
		path := LedgerPath(t.TempDir())
		if err := Append(path, []Line{a}); err != nil {
			t.Fatal(err)
		}
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString("not json at all\n{\"at\": \n\n")
		f.Close()
		if err := Append(path, []Line{b}); err != nil {
			t.Fatal(err)
		}
		got, err := Read(path)
		if err != nil || !reflect.DeepEqual(got, []Line{a, b}) {
			t.Fatalf("got %+v, err %v", got, err)
		}
	})
	t.Run("missing ledger is empty", func(t *testing.T) {
		got, err := Read(filepath.Join(t.TempDir(), "nope", "tokens.jsonl"))
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("unwritable folder names the path and writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "afk-is-a-file")
		os.WriteFile(file, []byte("x"), 0o644)
		path := filepath.Join(file, "log", "tokens.jsonl")
		err := Append(path, []Line{a})
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("err = %v, want one naming %s", err, path)
		}
		if data, _ := os.ReadFile(file); string(data) != "x" {
			t.Fatal("the blocking file was changed")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("extra files written: %v", entries)
		}
	})
	t.Run("appending nothing creates nothing", func(t *testing.T) {
		path := LedgerPath(t.TempDir())
		if err := Append(path, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Dir(path)); err == nil {
			t.Fatal("folder created")
		}
	})
}

func TestHasSessionAndForTask(t *testing.T) {
	lines := []Line{{Session: "s1", Task: "T1.4"}, {Session: "s2", Task: "T4.2a"}}
	t.Run("has session yes", func(t *testing.T) {
		if !HasSession(lines, "s2") {
			t.Fatal("want true")
		}
	})
	t.Run("has session no", func(t *testing.T) {
		if HasSession(lines, "s3") || HasSession(nil, "s1") {
			t.Fatal("want false")
		}
	})
	t.Run("for task ignores case", func(t *testing.T) {
		if got := ForTask(lines, "t4.2A"); len(got) != 1 || got[0].Session != "s2" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("for task no match", func(t *testing.T) {
		if got := ForTask(lines, "T9.9"); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
}

// TestTaskID: "two ids" pins first-wins; "T4.2a" pins the optional letter.
func TestTaskID(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "Implement T1.4 from docs/PLAN.md", "T1.4"},
		{"letter", "do T4.2a now", "T4.2a"},
		{"no id", "Implement the ledger", ""},
		{"two ids", "T1.4 then T2.1", "T1.4"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TaskID(c.in); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestRenderTable: shares must add to 100 within rounding (three equal
// thirds print 33.3 each), and a zero total must not print NaN.
func TestRenderTable(t *testing.T) {
	t.Run("shares add up", func(t *testing.T) {
		lines := []Line{
			{Phase: "plan", Role: "planner", Model: "m", Calls: 1, Input: 100, Weighted: 100},
			{Phase: "execute", Role: "task", Model: "m", Calls: 1, Input: 100, Weighted: 100},
			{Phase: "execute", Role: "verifier", Model: "m", Calls: 1, Input: 100, Weighted: 100},
		}
		var buf bytes.Buffer
		if err := RenderTable(&buf, lines); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "cache write") || !strings.Contains(out, "(subtotal)") || !strings.Contains(out, "total") {
			t.Fatalf("table lacks pieces:\n%s", out)
		}
		var sum float64
		for _, row := range strings.Split(out, "\n") {
			f := strings.Fields(row)
			if len(f) == 0 || f[0] == "total" || f[0] == "phase" || strings.Contains(row, "(subtotal)") {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimSuffix(f[len(f)-1], "%"), 64)
			if err != nil {
				t.Fatalf("share in %q: %v", row, err)
			}
			sum += v
		}
		if sum < 99.8 || sum > 100.2 {
			t.Fatalf("shares sum to %v:\n%s", sum, out)
		}
		if !strings.Contains(out, "66.7%") || !strings.Contains(out, "100.0%") {
			t.Fatalf("subtotal or total share wrong:\n%s", out)
		}
	})
	t.Run("zero total", func(t *testing.T) {
		var buf bytes.Buffer
		if err := RenderTable(&buf, []Line{{Phase: "plan", Role: "planner", Model: "m", Calls: 1}}); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if strings.Contains(out, "NaN") || strings.Contains(out, "Inf") || !strings.Contains(out, "0.0%") {
			t.Fatalf("bad zero table:\n%s", out)
		}
	})
	t.Run("weighted prints whole", func(t *testing.T) {
		var buf bytes.Buffer
		RenderTable(&buf, []Line{{Phase: "plan", Role: "r", Model: "m", Weighted: 12.5}})
		if strings.Contains(buf.String(), "12.5") {
			t.Fatalf("fraction printed:\n%s", buf.String())
		}
	})
	t.Run("empty ledger", func(t *testing.T) {
		var buf bytes.Buffer
		if err := RenderTable(&buf, nil); err != nil || strings.Contains(buf.String(), "NaN") {
			t.Fatalf("err %v out %s", err, buf.String())
		}
	})
}

func TestRenderTasks(t *testing.T) {
	lines := []Line{
		{Task: "T1.4", Session: "a", Calls: 2, Weighted: 300},
		{Task: "T1.4", Session: "b", Calls: 1, Weighted: 100},
		{Task: "T2.1", Session: "c", Calls: 4, Weighted: 400},
		{Task: "", Session: "d", Calls: 1, Weighted: 0},
	}
	var buf bytes.Buffer
	if err := RenderTasks(&buf, lines); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"T1.4", "50.0%", "T2.1", "(none)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	buf.Reset()
	RenderTasks(&buf, []Line{{Task: "T1.4", Session: "a"}})
	if strings.Contains(buf.String(), "NaN") {
		t.Fatalf("NaN:\n%s", buf.String())
	}
}
