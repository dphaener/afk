package decisionlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// entry returns a valid entry whose question names n, so tests can tell
// entries apart after a round trip.
func entry(n string) Entry {
	return Entry{
		At:       "2026-10-11T09:00:00Z",
		Task:     "T2.1",
		Session:  "t2-1",
		Kind:     "planner_question",
		Question: n,
		Options:  []string{"yes", "no"},
		Chosen:   "yes",
		ChosenBy: ChosenByPolicy,
		Rule:     "default-yes",
	}
}

// TestAppendRead covers the round trip, the order of two appends, and the
// missing-file case. Load-bearing: the order row is what makes the log a
// history rather than a set, and the missing-file row is what lets a fresh
// project read an empty log without an error.
func TestAppendRead(t *testing.T) {
	t.Run("append then read back", func(t *testing.T) {
		path := Path(t.TempDir())
		want := entry("q1")
		if err := Append(path, want); err != nil {
			t.Fatalf("Append: %v", err)
		}
		got, err := Read(path)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d entries, want 1", len(got))
		}
		if got[0].Question != want.Question || got[0].Chosen != want.Chosen ||
			strings.Join(got[0].Options, ",") != "yes,no" {
			t.Errorf("round trip changed the entry: got %+v", got[0])
		}
	})

	t.Run("two appends keep order", func(t *testing.T) {
		path := Path(t.TempDir())
		for _, q := range []string{"first", "second"} {
			if err := Append(path, entry(q)); err != nil {
				t.Fatalf("Append %s: %v", q, err)
			}
		}
		got, err := Read(path)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(got) != 2 || got[0].Question != "first" || got[1].Question != "second" {
			t.Errorf("order not kept: %+v", got)
		}
	})

	t.Run("missing file reads as empty", func(t *testing.T) {
		got, err := Read(Path(t.TempDir()))
		if err != nil {
			t.Fatalf("Read of a missing log: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d entries from a missing log, want 0", len(got))
		}
	})

	t.Run("nil options are written as an empty array", func(t *testing.T) {
		// Load-bearing: every key is always present, so a reader never
		// has to tell null from [] and an options-less decision still has
		// the key.
		path := Path(t.TempDir())
		e := entry("no options")
		e.Options = nil
		if err := Append(path, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(`"options":[]`)) {
			t.Errorf("options not written as []: %s", data)
		}
	})
}

// TestAppendValidation covers the rows the log refuses. Load-bearing: a refused
// entry must leave no file behind, so a bad call cannot leave a half-written
// log. The unknown chosen_by row is the one the owner relies on: a decision
// with no known actor is a decision nobody can audit.
func TestAppendValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Entry)
	}{
		{"no time", func(e *Entry) { e.At = "" }},
		{"time not RFC 3339", func(e *Entry) { e.At = "11 Oct 2026" }},
		{"time not in UTC", func(e *Entry) { e.At = "2026-10-11T09:00:00+02:00" }},
		{"no kind", func(e *Entry) { e.Kind = "" }},
		{"chosen_by unknown", func(e *Entry) { e.ChosenBy = "robot" }},
		{"chosen_by empty", func(e *Entry) { e.ChosenBy = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := Path(t.TempDir())
			e := entry("bad")
			tc.mutate(&e)
			if err := Append(path, e); err == nil {
				t.Fatal("Append accepted an invalid entry")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("a refused entry created the log (stat err %v)", err)
			}
		})
	}

	t.Run("each chosen_by value is accepted", func(t *testing.T) {
		for _, by := range []string{ChosenByPolicy, ChosenByAgent, ChosenByOwner} {
			e := entry("ok")
			e.ChosenBy = by
			if err := Validate(e); err != nil {
				t.Errorf("chosen_by %q refused: %v", by, err)
			}
		}
	})
}

// TestReadDamaged covers a log that does not parse. Load-bearing: a damaged
// line is an error with its number and nothing is returned, because the
// caller must not act on a log it cannot read whole. The blank-line row
// separates a damaged middle line from the single newline that ends every
// file.
func TestReadDamaged(t *testing.T) {
	good, err := json.Marshal(entry("good"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		body     string
		wantLine string
	}{
		{"torn last line", string(good) + "\n{\"at\":\"2026", "line 2"},
		{"blank line in the middle", string(good) + "\n\n" + string(good) + "\n", "line 2"},
		{"not JSON on line 1", "garbage\n", "line 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "decisions.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := Read(path)
			if err == nil {
				t.Fatalf("Read accepted a damaged log, returned %d entries", len(got))
			}
			if got != nil {
				t.Errorf("Read returned entries alongside an error: %d", len(got))
			}
			if !strings.Contains(err.Error(), tc.wantLine) {
				t.Errorf("error %q does not name %s", err, tc.wantLine)
			}
		})
	}
}

// TestAppendConcurrent runs many writers at once. Load-bearing: the lock and
// the single write per entry are what keep two lines from interleaving. Under
// -race this also checks the test's own writers share nothing unsafely.
func TestAppendConcurrent(t *testing.T) {
	path := Path(t.TempDir())
	const writers = 24
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := entry(strings.Repeat("x", 500))
			e.Question = e.Question + string(rune('a'+i%26))
			errs <- Append(path, e)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Append: %v", err)
		}
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read after concurrent appends: %v", err)
	}
	if len(got) != writers {
		t.Errorf("got %d entries, want %d", len(got), writers)
	}
}
