package inbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixedNow is the clock every test uses unless it needs time to move.
var fixedNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))

// scripted returns an id generator that hands out ids in order and fails the
// test when asked for more than it was given, so a test also pins how many
// ids a call draws.
func scripted(t *testing.T, ids ...string) func() string {
	t.Helper()
	var mu sync.Mutex
	i := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if i >= len(ids) {
			t.Errorf("NewID called %d times, only %d ids scripted", i+1, len(ids))
			return "zzzzzz"
		}
		id := ids[i]
		i++
		return id
	}
}

// counterID turns n into a distinct valid id, for tests that need many.
func counterID(n int) string {
	b := make([]byte, idLength)
	for i := idLength - 1; i >= 0; i-- {
		b[i] = idAlphabet[n%len(idAlphabet)]
		n /= len(idAlphabet)
	}
	return string(b)
}

func newStore(t *testing.T, ids ...string) Store {
	t.Helper()
	return Store{
		Dir:   filepath.Join(t.TempDir(), ".afk", "inbox"),
		Now:   func() time.Time { return fixedNow },
		NewID: scripted(t, ids...),
	}
}

func writeRaw(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// itemJSON is a well-formed item file for id, used as the base that the
// unreadable rows break one thing in.
func itemJSON(id string) string {
	return `{
  "id": "` + id + `",
  "created_at": "2026-10-11T10:00:00Z",
  "task": "",
  "session": "",
  "kind": "notice",
  "blocking": false,
  "title": "hello",
  "body": "",
  "options": [],
  "recommended": "",
  "assumed": "",
  "answer": "",
  "answered_at": "",
  "answered_via": ""
}
`
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func ids(items []Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// TestValidate has one row per validation rule and one valid row per kind,
// and runs every row through Store.Add as well as Validate.
//
// The load-bearing rows:
//   - "assumed on a blocking item": an assumed answer says the task carried
//     on, a blocking item says it stopped. Letting both through would park a
//     task while telling the owner nothing is waiting on them.
//   - "unknown kind" and "empty kind": the kind decides whether the owner's
//     phone buzzes, so a misspelt one must be refused, never stored.
//   - "one option", "recommended not among the options": the answer command
//     picks an option by number, so the list on disk has to be a real choice.
//   - every failing row also asserts that Add created nothing, not even the
//     inbox folder: a refused item must leave no trace for List to trip on.
//   - "task with a trailing newline": pins that the patterns match the whole
//     string, so a value cannot smuggle a second line into the file.
func TestValidate(t *testing.T) {
	long := strings.Repeat("é", maxTitleChars)
	tests := []struct {
		name    string
		item    NewItem
		wantErr string // fragment of the message; "" means valid
	}{
		{"valid question", NewItem{Kind: "question", Title: "Which queue?"}, ""},
		{"valid question with options, recommended and assumed", NewItem{Kind: "question", Title: "Which queue?", Options: []string{"sidekiq", "solid queue"}, Recommended: "sidekiq", Assumed: "sidekiq", Task: "T4.2a", Session: "t1-2", Body: "why"}, ""},
		{"valid blocking question", NewItem{Kind: "question", Title: "Which queue?", Blocking: true, Options: []string{"a", "b"}}, ""},
		{"valid env_var", NewItem{Kind: "env_var", Title: "Set STRIPE_KEY", Blocking: true, Task: "T4.3"}, ""},
		{"valid external_action", NewItem{Kind: "external_action", Title: "Approve the DNS change", Blocking: true}, ""},
		{"valid verification", NewItem{Kind: "verification", Title: "Check the login page"}, ""},
		{"valid failure", NewItem{Kind: "failure", Title: "CI failed twice", Blocking: true}, ""},
		{"valid notice", NewItem{Kind: "notice", Title: "Idea: dark mode"}, ""},
		{"valid assumed on a non-question", NewItem{Kind: "notice", Title: "x", Assumed: "went ahead"}, ""},
		{"valid title of exactly 200 multi-byte characters", NewItem{Kind: "notice", Title: long}, ""},
		{"valid title with surrounding blank lines", NewItem{Kind: "notice", Title: "\n  x  \n"}, ""},
		{"valid session starting with a digit", NewItem{Kind: "notice", Title: "x", Session: "2fast"}, ""},

		{"empty kind", NewItem{Title: "x"}, "needs a kind"},
		{"unknown kind", NewItem{Kind: "nonsense", Title: "x"}, `"nonsense" is not a kind`},
		{"kind in the wrong case", NewItem{Kind: "Question", Title: "x"}, "is not a kind"},
		{"empty title", NewItem{Kind: "notice"}, "needs a title"},
		{"title of only spaces", NewItem{Kind: "notice", Title: " \t\n "}, "needs a title"},
		{"title with a newline", NewItem{Kind: "notice", Title: "one\ntwo"}, "single line"},
		{"title with a carriage return", NewItem{Kind: "notice", Title: "one\rtwo"}, "single line"},
		{"title of 201 characters", NewItem{Kind: "notice", Title: long + "é"}, "201 characters"},
		{"options on a non-question", NewItem{Kind: "env_var", Title: "x", Options: []string{"a", "b"}}, "options only belong on a question"},
		{"recommended on a non-question", NewItem{Kind: "notice", Title: "x", Recommended: "a"}, "recommended option only belongs on a question"},
		{"one option", NewItem{Kind: "question", Title: "x", Options: []string{"a"}}, "at least two"},
		{"empty option", NewItem{Kind: "question", Title: "x", Options: []string{"a", ""}}, "option 2 is empty"},
		{"option of only spaces", NewItem{Kind: "question", Title: "x", Options: []string{"  ", "b"}}, "option 1 is empty"},
		{"duplicate option", NewItem{Kind: "question", Title: "x", Options: []string{"a", "b", "a"}}, "given twice"},
		{"recommended not among the options", NewItem{Kind: "question", Title: "x", Options: []string{"a", "b"}, Recommended: "c"}, "not one of the options"},
		{"recommended differs only in case", NewItem{Kind: "question", Title: "x", Options: []string{"a", "b"}, Recommended: "A"}, "not one of the options"},
		{"recommended with no options", NewItem{Kind: "question", Title: "x", Recommended: "a"}, "no options to choose from"},
		{"assumed on a blocking item", NewItem{Kind: "question", Title: "x", Blocking: true, Assumed: "a"}, "assumed answer"},
		{"task without the T", NewItem{Kind: "notice", Title: "x", Task: "1.2"}, "not a task id"},
		{"task with a wave id", NewItem{Kind: "notice", Title: "x", Task: "W1.T1"}, "not a task id"},
		{"task with two letters", NewItem{Kind: "notice", Title: "x", Task: "T1.2ab"}, "not a task id"},
		{"task with a trailing newline", NewItem{Kind: "notice", Title: "x", Task: "T1.2\n"}, "not a task id"},
		{"session with an uppercase letter", NewItem{Kind: "notice", Title: "x", Session: "T1-2"}, "not a session name"},
		{"session starting with a hyphen", NewItem{Kind: "notice", Title: "x", Session: "-x"}, "not a session name"},
		{"session with a slash", NewItem{Kind: "notice", Title: "x", Session: "a/b"}, "not a session name"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.item)
			s := Store{
				Dir:   filepath.Join(t.TempDir(), ".afk", "inbox"),
				Now:   func() time.Time { return fixedNow },
				NewID: func() string { return counterID(i) },
			}
			_, addErr := s.Add(tt.item)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				if addErr != nil {
					t.Fatalf("Add() = %v, want nil", addErr)
				}
				return
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Validate() = %v (%T), want a *ValidationError", err, err)
			}
			if !strings.Contains(ve.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", ve.Error(), tt.wantErr)
			}
			// One plain sentence: no line break, and it ends like one.
			if strings.Contains(ve.Error(), "\n") || !strings.HasSuffix(ve.Error(), ".") {
				t.Errorf("message is not one sentence: %q", ve.Error())
			}
			if !errors.As(addErr, &ve) || addErr.Error() != err.Error() {
				t.Errorf("Add() = %v, want the same validation error %v", addErr, err)
			}
			if _, statErr := os.Stat(s.Dir); !os.IsNotExist(statErr) {
				t.Errorf("a refused Add created %s (stat err %v); it must write nothing", s.Dir, statErr)
			}
		})
	}
}

// TestAddWritesFourteenKeys pins the file format other readers depend on.
// Load-bearing: the key set is compared exactly (a fifteenth key would break
// every strict reader, a missing one every reader that trusts "always
// present"), and `options` is checked on the raw bytes, because `null` and
// `[]` are the same to Go and different to the web page.
func TestAddWritesFourteenKeys(t *testing.T) {
	want := []string{
		"answer", "answered_at", "answered_via", "assumed", "blocking", "body",
		"created_at", "id", "kind", "options", "recommended", "session", "task", "title",
	}
	tests := []struct {
		name string
		item NewItem
	}{
		{"minimal item, nil options", NewItem{Kind: "notice", Title: "Idea: dark mode"}},
		{"full item", NewItem{Kind: "question", Title: "Which queue?", Body: "b", Task: "T4.3", Session: "t1-2", Options: []string{"sidekiq", "solid queue"}, Recommended: "sidekiq", Assumed: "sidekiq"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t, "abc234")
			it, err := s.Add(tt.item)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(s.Dir, "abc234.json"))
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("file is not a JSON object: %v\n%s", err, data)
			}
			var got []string
			for k := range raw {
				got = append(got, k)
			}
			slices.Sort(got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("keys = %v\nwant   %v", got, want)
			}
			if len(tt.item.Options) == 0 && string(raw["options"]) != "[]" {
				t.Errorf(`options = %s, want []`, raw["options"])
			}
			if string(raw["blocking"]) != "false" {
				t.Errorf("blocking = %s, want the boolean false", raw["blocking"])
			}
			if string(raw["created_at"]) != `"2026-10-11T10:00:00Z"` {
				t.Errorf("created_at = %s, want the fixed clock in UTC", raw["created_at"])
			}
			for _, k := range []string{"answer", "answered_at", "answered_via"} {
				if string(raw[k]) != `""` {
					t.Errorf(`%s = %s, want ""`, k, raw[k])
				}
			}
			if !strings.HasSuffix(string(data), "}\n") || !strings.Contains(string(data), "\n  \"id\": ") {
				t.Errorf("file is not indented with a trailing newline:\n%s", data)
			}
			if it.Options == nil {
				t.Error("returned Options is nil, want an empty slice")
			}
			// What was written is what a read gives back.
			back, err := s.Get("abc234")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, it) {
				t.Errorf("Get() = %+v\nAdd()   %+v", back, it)
			}
			// Only the item and the lock are left: no temp file.
			if got := names(t, s.Dir); !reflect.DeepEqual(got, []string{".lock", "abc234.json"}) {
				t.Errorf("folder holds %v, want only the lock and the item", got)
			}
		})
	}
}

// TestAddStoresTrimmedText covers what Add changes on the way in. The row
// that matters is the options one: the stored slice must be a copy, or a
// caller reusing its slice would change an item it was already handed.
func TestAddStoresTrimmedText(t *testing.T) {
	t.Run("title and body are trimmed, prose is not escaped", func(t *testing.T) {
		s := newStore(t, "abc234")
		it, err := s.Add(NewItem{Kind: "notice", Title: "  A & B <now>  ", Body: "\nline one\nline two\n\n"})
		if err != nil {
			t.Fatal(err)
		}
		if it.Title != "A & B <now>" || it.Body != "line one\nline two" {
			t.Errorf("title %q body %q, want both trimmed", it.Title, it.Body)
		}
		data, _ := os.ReadFile(filepath.Join(s.Dir, "abc234.json"))
		if !strings.Contains(string(data), `"title": "A & B <now>"`) {
			t.Errorf("title was escaped in the file:\n%s", data)
		}
	})
	t.Run("options are copied", func(t *testing.T) {
		s := newStore(t, "abc234")
		opts := []string{"a", "b"}
		it, err := s.Add(NewItem{Kind: "question", Title: "x", Options: opts})
		if err != nil {
			t.Fatal(err)
		}
		opts[0] = "changed"
		if it.Options[0] != "a" {
			t.Errorf("the returned item shares the caller's slice: %v", it.Options)
		}
	})
	t.Run("zero Store has no folder to write to", func(t *testing.T) {
		if _, err := (Store{}).Add(NewItem{Kind: "notice", Title: "x"}); err == nil {
			t.Error("Add on a Store with no Dir succeeded; it would have written into the working directory")
		}
	})
}

// TestAddIDCollision covers the search for a free id. Load-bearing: "first id
// is taken" must leave the existing item byte-for-byte alone (a collision
// that overwrote would silently lose something the owner was asked), and
// "every id is taken" must stop after twenty tries rather than spin while
// holding the lock every other writer is waiting on.
func TestAddIDCollision(t *testing.T) {
	t.Run("first id is taken", func(t *testing.T) {
		s := newStore(t, "aaaaaa", "aaaaaa", "bbbbbb")
		first, err := s.Add(NewItem{Kind: "notice", Title: "first"})
		if err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(s.Dir, "aaaaaa.json"))
		second, err := s.Add(NewItem{Kind: "notice", Title: "second"})
		if err != nil {
			t.Fatal(err)
		}
		if first.ID != "aaaaaa" || second.ID != "bbbbbb" {
			t.Errorf("ids = %q, %q; want aaaaaa, bbbbbb", first.ID, second.ID)
		}
		after, _ := os.ReadFile(filepath.Join(s.Dir, "aaaaaa.json"))
		if string(before) != string(after) {
			t.Error("the item that already held the id was rewritten")
		}
	})
	t.Run("every id is taken", func(t *testing.T) {
		calls := 0
		s := Store{
			Dir:   filepath.Join(t.TempDir(), "inbox"),
			Now:   func() time.Time { return fixedNow },
			NewID: func() string { calls++; return "aaaaaa" },
		}
		if _, err := s.Add(NewItem{Kind: "notice", Title: "first"}); err != nil {
			t.Fatal(err)
		}
		calls = 0
		_, err := s.Add(NewItem{Kind: "notice", Title: "second"})
		if err == nil || !strings.Contains(err.Error(), "20 tries") {
			t.Fatalf("Add() = %v, want an error after 20 tries", err)
		}
		if calls != maxIDTries {
			t.Errorf("NewID called %d times, want %d", calls, maxIDTries)
		}
		var ve *ValidationError
		if errors.As(err, &ve) {
			t.Error("running out of ids is not a validation error: the caller did nothing wrong")
		}
		if got := names(t, s.Dir); !reflect.DeepEqual(got, []string{".lock", "aaaaaa.json"}) {
			t.Errorf("folder holds %v, want only the lock and the first item", got)
		}
	})
	t.Run("generator returns something that is not an id", func(t *testing.T) {
		s := Store{
			Dir:   filepath.Join(t.TempDir(), "inbox"),
			Now:   func() time.Time { return fixedNow },
			NewID: func() string { return "../oops" },
		}
		if _, err := s.Add(NewItem{Kind: "notice", Title: "x"}); err == nil {
			t.Fatal("Add accepted an id that is not an id")
		}
		if got := names(t, s.Dir); !reflect.DeepEqual(got, []string{".lock"}) {
			t.Errorf("folder holds %v, want only the lock", got)
		}
	})
}

// TestConcurrentAdd runs 20 adds across 4 goroutines against one folder, each
// through its own Store value so each opens the lock file itself, as separate
// processes would. The id generator is the load-bearing part: it hands every
// id out twice, so without the lock two adds would race for the same file and
// one item would be lost. With the lock, every collision is seen and retried,
// and all 20 items survive.
func TestConcurrentAdd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".afk", "inbox")
	var n atomic.Int64
	newID := func() string { return counterID(int(n.Add(1)-1) / 2) }

	const workers, each = 4, 5
	var wg sync.WaitGroup
	got := make(chan Item, workers*each)
	errs := make(chan error, workers*each)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := Store{Dir: dir, Now: func() time.Time { return fixedNow }, NewID: newID}
			for i := range each {
				it, err := s.Add(NewItem{Kind: "notice", Title: "item " + counterID(w*each+i)})
				if err != nil {
					errs <- err
					continue
				}
				got <- it
			}
		}()
	}
	wg.Wait()
	close(got)
	close(errs)
	for err := range errs {
		t.Errorf("Add() = %v", err)
	}

	seen := map[string]bool{}
	titles := map[string]bool{}
	for it := range got {
		if seen[it.ID] {
			t.Errorf("id %s was handed to two items", it.ID)
		}
		seen[it.ID] = true
		titles[it.Title] = true
	}
	if len(seen) != workers*each {
		t.Fatalf("%d distinct ids, want %d", len(seen), workers*each)
	}

	items, unreadable, err := Store{Dir: dir}.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 0 {
		t.Errorf("unreadable files after concurrent adds: %v", unreadable)
	}
	if len(items) != workers*each {
		t.Fatalf("List() has %d items, want %d", len(items), workers*each)
	}
	for _, it := range items {
		if !seen[it.ID] || !titles[it.Title] {
			t.Errorf("item on disk %s %q was not returned by an Add", it.ID, it.Title)
		}
		delete(titles, it.Title)
	}
	if len(titles) != 0 {
		t.Errorf("titles returned by Add but not on disk (overwritten): %v", titles)
	}
	for _, name := range names(t, dir) {
		if name != ".lock" && !strings.HasSuffix(name, ".json") {
			t.Errorf("leftover file %s", name)
		}
	}
}

// TestListOrder pins the order the owner reads the inbox in. Load-bearing:
// an open blocking item comes before an older open non-blocking one (a task
// is stopped on it), an answered blocking item drops below every open item
// (it no longer needs anyone), and two items from the same second fall back
// to id so the order never flickers between two reads.
func TestListOrder(t *testing.T) {
	now := fixedNow
	s := Store{
		Dir: filepath.Join(t.TempDir(), "inbox"),
		Now: func() time.Time { return now },
	}
	add := func(id string, minute int, blocking bool) {
		t.Helper()
		now = fixedNow.Add(time.Duration(minute) * time.Minute)
		s.NewID = scripted(t, id)
		if _, err := s.Add(NewItem{Kind: "question", Title: id, Blocking: blocking}); err != nil {
			t.Fatal(err)
		}
	}
	add("aaaaaa", 1, false)
	add("bbbbbb", 3, true)
	add("cccccc", 2, true)
	add("dddddd", 0, false)
	add("eeeeee", 4, true)
	add("gggggg", 5, false)
	add("ffffff", 5, false)
	for _, id := range []string{"eeeeee", "dddddd"} {
		if _, err := s.Answer(id, "ok", "cli"); err != nil {
			t.Fatal(err)
		}
	}

	items, unreadable, err := s.List()
	if err != nil || len(unreadable) != 0 {
		t.Fatalf("List() err %v unreadable %v", err, unreadable)
	}
	want := []string{
		"cccccc", "bbbbbb", // open and blocking, oldest first
		"aaaaaa", "ffffff", "gggggg", // open; the last two share a second
		"dddddd", "eeeeee", // answered, by creation time whatever their blocking flag
	}
	if got := ids(items); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v\nwant    %v", got, want)
	}

	t.Run("times are compared as times, not text", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "inbox")
		// 13:00+02:00 is 11:00 UTC, earlier than 12:00Z, but later as text.
		writeRaw(t, dir, "aaaaaa.json", strings.Replace(itemJSON("aaaaaa"), "2026-10-11T10:00:00Z", "2026-10-11T12:00:00Z", 1))
		writeRaw(t, dir, "bbbbbb.json", strings.Replace(itemJSON("bbbbbb"), "2026-10-11T10:00:00Z", "2026-10-11T13:00:00+02:00", 1))
		items, _, err := Store{Dir: dir}.List()
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(items); !reflect.DeepEqual(got, []string{"bbbbbb", "aaaaaa"}) {
			t.Errorf("order = %v, want [bbbbbb aaaaaa]", got)
		}
	})

	t.Run("a missing folder is an empty inbox", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "never", "made")
		items, unreadable, err := Store{Dir: dir}.List()
		if err != nil || len(items) != 0 || len(unreadable) != 0 {
			t.Errorf("List() = %v, %v, %v; want nothing and no error", items, unreadable, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Error("List created the folder; reading must not write")
		}
	})
}

// TestListUnreadable puts one good item in a folder with one other file per
// row and says which of three things happens to that file: it is listed, it
// is reported as unreadable, or it is ignored.
//
// The load-bearing rows:
//   - "unknown key" and the two "trailing" rows: strictness is what stops a
//     half-understood item being shown and then written back by Answer.
//   - "status.json" against "notes.json": the folder sits beside session
//     folders, so stray files are expected. notes.json is ignored like any
//     other name, but "status" is six characters from the id alphabet, so it
//     is taken for an item and reported. Both outcomes are fine; what must
//     never happen is either one failing the whole list.
//   - "temp file from a killed writer" and "the lock": the store's own
//     droppings must never show up as items or as damage.
//
// In every row the good item is still listed and the error is nil.
func TestListUnreadable(t *testing.T) {
	const (
		listed = iota
		unreadable
		ignored
	)
	tests := []struct {
		name    string
		file    string
		content string
		want    int
	}{
		{"a second good item", "bbbbbb.json", itemJSON("bbbbbb"), listed},
		{"options null is read as an empty list", "bbbbbb.json", strings.Replace(itemJSON("bbbbbb"), `"options": []`, `"options": null`, 1), listed},

		{"unknown key", "bbbbbb.json", strings.Replace(itemJSON("bbbbbb"), `"body": "",`, `"body": "", "priority": 5,`, 1), unreadable},
		{"trailing second object", "bbbbbb.json", itemJSON("bbbbbb") + itemJSON("bbbbbb"), unreadable},
		{"trailing closing brace", "bbbbbb.json", itemJSON("bbbbbb") + "}\n", unreadable},
		{"trailing text", "bbbbbb.json", itemJSON("bbbbbb") + "done\n", unreadable},
		{"id differs from the file name", "bbbbbb.json", itemJSON("cccccc"), unreadable},
		{"unknown kind", "bbbbbb.json", strings.Replace(itemJSON("bbbbbb"), `"kind": "notice"`, `"kind": "reminder"`, 1), unreadable},
		{"wrong type for blocking", "bbbbbb.json", strings.Replace(itemJSON("bbbbbb"), `"blocking": false`, `"blocking": "no"`, 1), unreadable},
		{"cut off half way", "bbbbbb.json", itemJSON("bbbbbb")[:40], unreadable},
		{"empty file", "bbbbbb.json", "", unreadable},
		{"not JSON", "bbbbbb.json", "hello\n", unreadable},
		{"JSON null", "bbbbbb.json", "null\n", unreadable},
		{"status.json", "status.json", `{"phase": "executing", "loop_count": 1}`, unreadable},

		{"notes.json", "notes.json", `{"note": "remember"}`, ignored},
		{"the lock", ".lock", "", ignored},
		{"temp file from a killed writer", ".bbbbbb.json.tmp.4242", itemJSON("bbbbbb")[:40], ignored},
		{"id with an uppercase letter", "BBBBBB.json", itemJSON("BBBBBB"), ignored},
		{"id of five characters", "bbbbb.json", itemJSON("bbbbb"), ignored},
		{"id of seven characters", "bbbbbbb.json", itemJSON("bbbbbbb"), ignored},
		{"id with a character outside the alphabet", "bbbbb1.json", itemJSON("bbbbb1"), ignored},
		{"valid id, wrong extension", "bbbbbb.txt", itemJSON("bbbbbb"), ignored},
		{"valid id, no extension", "bbbbbb", itemJSON("bbbbbb"), ignored},
		{"markdown notes", "decisions.md", "# notes\n", ignored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "inbox")
			writeRaw(t, dir, "aaaaaa.json", itemJSON("aaaaaa"))
			writeRaw(t, dir, tt.file, tt.content)

			items, bad, err := Store{Dir: dir}.List()
			if err != nil {
				t.Fatalf("List() error = %v; a bad file must never be an error", err)
			}
			wantItems, wantBad := []string{"aaaaaa"}, []string(nil)
			switch tt.want {
			case listed:
				wantItems = append(wantItems, strings.TrimSuffix(tt.file, ".json"))
			case unreadable:
				wantBad = []string{tt.file}
			}
			if got := ids(items); !reflect.DeepEqual(got, wantItems) {
				t.Errorf("items = %v, want %v", got, wantItems)
			}
			if !reflect.DeepEqual(bad, wantBad) {
				t.Errorf("unreadable = %v, want %v", bad, wantBad)
			}
			for _, it := range items {
				if it.Options == nil {
					t.Errorf("item %s has nil Options, want an empty slice", it.ID)
				}
			}
		})
	}

	t.Run("a subfolder is ignored and a folder named like an item is unreadable", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "inbox")
		writeRaw(t, dir, "aaaaaa.json", itemJSON("aaaaaa"))
		for _, sub := range []string{"t1-2", "bbbbbb.json"} {
			if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		items, bad, err := Store{Dir: dir}.List()
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(items); !reflect.DeepEqual(got, []string{"aaaaaa"}) || !reflect.DeepEqual(bad, []string{"bbbbbb.json"}) {
			t.Errorf("items %v unreadable %v; want [aaaaaa] and [bbbbbb.json]", got, bad)
		}
	})
}

// TestGetUnreadable separates the two ways Get fails, because the command
// says different things for them. Load-bearing: an id that is not shaped
// like one ("../status") is ErrNotFound before any path is built, so a typed
// id can never reach a file outside the inbox.
func TestGetUnreadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inbox")
	writeRaw(t, dir, "aaaaaa.json", itemJSON("aaaaaa"))
	writeRaw(t, dir, "bbbbbb.json", strings.Replace(itemJSON("bbbbbb"), `"body": "",`, `"body": "", "extra": 1,`, 1))
	writeRaw(t, filepath.Dir(dir), "status.json", itemJSON("status"))
	s := Store{Dir: dir}

	tests := []struct {
		name         string
		id           string
		wantNotFound bool
		wantErr      bool
	}{
		{"a good item", "aaaaaa", false, false},
		{"no such item", "cccccc", true, true},
		{"empty id", "", true, true},
		{"id that is a path", "../status", true, true},
		{"id with the extension typed", "aaaaaa.json", true, true},
		{"id in uppercase", "AAAAAA", true, true},
		{"file with an unknown key", "bbbbbb", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it, err := s.Get(tt.id)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Get() error = %v, wantErr %v", err, tt.wantErr)
			}
			if errors.Is(err, ErrNotFound) != tt.wantNotFound {
				t.Errorf("Get() error = %v, want ErrNotFound: %v", err, tt.wantNotFound)
			}
			if !tt.wantErr && it.ID != tt.id {
				t.Errorf("Get() id = %q, want %q", it.ID, tt.id)
			}
			if tt.wantErr && !tt.wantNotFound && !strings.Contains(err.Error(), tt.id) {
				t.Errorf("unreadable error %q does not name the item", err)
			}
		})
	}

	t.Run("a missing folder is not found", func(t *testing.T) {
		if _, err := (Store{Dir: filepath.Join(t.TempDir(), "none")}).Get("aaaaaa"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get() = %v, want ErrNotFound", err)
		}
	})
}

// TestAnswer covers every way an answer is taken or refused. Each row starts
// from a fresh folder holding one question with three options (aaaaaa) and
// one notice with none (bbbbbb).
//
// The load-bearing rows:
//   - "a second answer is refused": the first answer is kept, and the stored
//     item comes back with the error so the command can show what stands. An
//     agent may already have acted on it.
//   - "empty" and "only spaces": an empty answer would leave the item open
//     (open means no answer) while stamping it answered, so it is refused.
//   - "unknown via": answered_via is read by later tools; a free-text value
//     must not get onto disk.
//   - every refused row asserts the file is byte-for-byte unchanged and that
//     an unknown id creates nothing.
func TestAnswer(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		first      string // when set, the item is answered with this beforehand
		text       string
		via        string
		wantAnswer string
		wantErr    error // a sentinel, or errAny
	}{
		{"by number", "aaaaaa", "", "2", "cli", "solid queue", nil},
		{"by number with spaces around it", "aaaaaa", "", " 3\n", "web", "good job", nil},
		{"by the option's text", "aaaaaa", "", "sidekiq", "tui", "sidekiq", nil},
		{"by other text", "aaaaaa", "", "  neither, use cron  ", "cli", "neither, use cron", nil},
		{"number zero is kept as text", "aaaaaa", "", "0", "cli", "0", nil},
		{"number past the last option is kept as text", "aaaaaa", "", "4", "cli", "4", nil},
		{"signed number is kept as text", "aaaaaa", "", "+2", "cli", "+2", nil},
		{"number on an item without options is kept as text", "bbbbbb", "", "2", "cli", "2", nil},
		{"several lines", "bbbbbb", "", "done\nand checked", "cli", "done\nand checked", nil},

		{"empty", "aaaaaa", "", "", "cli", "", ErrEmptyAnswer},
		{"only spaces", "aaaaaa", "", " \t\n", "cli", "", ErrEmptyAnswer},
		{"a second answer is refused", "aaaaaa", "1", "2", "cli", "sidekiq", ErrAlreadyAnswered},
		{"a second, empty answer is still already answered", "aaaaaa", "1", "", "cli", "sidekiq", ErrAlreadyAnswered},
		{"no such item", "cccccc", "", "ok", "cli", "", ErrNotFound},
		{"id that is a path", "../inbox/aaaaaa", "", "ok", "cli", "", ErrNotFound},
		{"unknown via", "aaaaaa", "", "ok", "email", "", errAny},
		{"empty via", "aaaaaa", "", "ok", "", "", errAny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := fixedNow
			s := Store{
				Dir:   filepath.Join(t.TempDir(), "inbox"),
				Now:   func() time.Time { return now },
				NewID: scripted(t, "aaaaaa", "bbbbbb"),
			}
			if _, err := s.Add(NewItem{Kind: "question", Title: "Which queue?", Options: []string{"sidekiq", "solid queue", "good job"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Add(NewItem{Kind: "notice", Title: "Check it"}); err != nil {
				t.Fatal(err)
			}
			now = fixedNow.Add(30 * time.Minute)
			if tt.first != "" {
				if _, err := s.Answer(tt.id, tt.first, "web"); err != nil {
					t.Fatal(err)
				}
			}
			now = fixedNow.Add(time.Hour)
			path := filepath.Join(s.Dir, tt.id+".json")
			before, _ := os.ReadFile(path)
			namesBefore := names(t, s.Dir)

			it, err := s.Answer(tt.id, tt.text, tt.via)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("Answer() succeeded with %+v, want an error", it)
				}
				if tt.wantErr != errAny && !errors.Is(err, tt.wantErr) {
					t.Fatalf("Answer() error = %v, want %v", err, tt.wantErr)
				}
				after, _ := os.ReadFile(path)
				if string(before) != string(after) {
					t.Errorf("a refused answer changed the file:\n%s", after)
				}
				if got := names(t, s.Dir); !reflect.DeepEqual(got, namesBefore) {
					t.Errorf("a refused answer changed the folder: %v, was %v", got, namesBefore)
				}
				if tt.wantErr == ErrAlreadyAnswered {
					if it.Answer != tt.wantAnswer || it.AnsweredVia != "web" || it.AnsweredAt != "2026-10-11T10:30:00Z" {
						t.Errorf("stored item = %+v, want the first answer %q returned unchanged", it, tt.wantAnswer)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("Answer() error = %v", err)
			}
			if it.Answer != tt.wantAnswer {
				t.Errorf("answer = %q, want %q", it.Answer, tt.wantAnswer)
			}
			if it.AnsweredAt != "2026-10-11T11:00:00Z" || it.AnsweredVia != tt.via {
				t.Errorf("answered_at %q via %q; want 2026-10-11T11:00:00Z and %q", it.AnsweredAt, it.AnsweredVia, tt.via)
			}
			if it.Open() {
				t.Error("an answered item still reports Open")
			}
			back, err := s.Get(tt.id)
			if err != nil {
				t.Fatalf("the answered file does not read back: %v", err)
			}
			if !reflect.DeepEqual(back, it) {
				t.Errorf("on disk %+v\nreturned %+v", back, it)
			}
			if back.CreatedAt != "2026-10-11T10:00:00Z" || back.Title == "" {
				t.Errorf("answering changed fields it does not own: %+v", back)
			}
			if got := names(t, s.Dir); !reflect.DeepEqual(got, namesBefore) {
				t.Errorf("folder holds %v after an answer, was %v (temp file left?)", got, namesBefore)
			}
		})
	}

	t.Run("an inbox that does not exist is not created", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "inbox")
		if _, err := (Store{Dir: dir}).Answer("aaaaaa", "ok", "cli"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Answer() = %v, want ErrNotFound", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Error("answering a missing item created the inbox folder")
		}
	})

	t.Run("an unreadable item cannot be answered", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "inbox")
		content := strings.Replace(itemJSON("aaaaaa"), `"body": "",`, `"body": "", "extra": 1,`, 1)
		writeRaw(t, dir, "aaaaaa.json", content)
		_, err := Store{Dir: dir}.Answer("aaaaaa", "ok", "cli")
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Answer() = %v, want a read error", err)
		}
		if after, _ := os.ReadFile(filepath.Join(dir, "aaaaaa.json")); string(after) != content {
			t.Error("an unreadable item was rewritten; the key it did not understand is gone")
		}
	})

	t.Run("of many answers at once, one is kept", func(t *testing.T) {
		s := newStore(t, "aaaaaa")
		if _, err := s.Add(NewItem{Kind: "notice", Title: "x"}); err != nil {
			t.Fatal(err)
		}
		const n = 8
		var wg sync.WaitGroup
		var won, refused atomic.Int64
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := Store{Dir: s.Dir, Now: s.Now}.Answer("aaaaaa", "answer "+counterID(i), "cli")
				switch {
				case err == nil:
					won.Add(1)
				case errors.Is(err, ErrAlreadyAnswered):
					refused.Add(1)
				default:
					t.Errorf("Answer() = %v", err)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 || refused.Load() != n-1 {
			t.Errorf("%d answers accepted and %d refused, want 1 and %d", won.Load(), refused.Load(), n-1)
		}
	})
}

// errAny marks a TestAnswer row that must fail without being one of the
// sentinels.
var errAny = errors.New("any error")

// TestResolveAnswer is the pure half of answering. Load-bearing: a number
// only picks an option when the item has options and the number is in range;
// everywhere else the owner's text is kept, because "3" is a perfectly good
// answer to "how many workers?".
func TestResolveAnswer(t *testing.T) {
	with := Item{Options: []string{"sidekiq", "solid queue"}}
	without := Item{Options: []string{}}
	tests := []struct {
		name string
		item Item
		raw  string
		want string
	}{
		{"first option", with, "1", "sidekiq"},
		{"last option", with, "2", "solid queue"},
		{"number with spaces", with, "  2 ", "solid queue"},
		{"leading zero still picks", with, "02", "solid queue"},
		{"zero", with, "0", "0"},
		{"past the end", with, "3", "3"},
		{"negative", with, "-1", "-1"},
		{"signed", with, "+1", "+1"},
		{"decimal", with, "1.0", "1.0"},
		{"number with a word", with, "1 please", "1 please"},
		{"too large for an int", with, "99999999999999999999999", "99999999999999999999999"},
		{"option text", with, "solid queue", "solid queue"},
		{"free text is trimmed", with, "\n neither \n", "neither"},
		{"empty", with, "", ""},
		{"only spaces", with, "   ", ""},
		{"number without options", without, "1", "1"},
		{"number with nil options", Item{}, "1", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveAnswer(tt.item, tt.raw); got != tt.want {
				t.Errorf("ResolveAnswer(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestValidID guards the one check that stands between a typed id and a file
// path. Load-bearing: every row with a dot, a slash or a wrong length is
// false, and "status" being true is recorded on purpose (see
// TestListUnreadable).
func TestValidID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"abc234", true},
		{"zzzzzz", true},
		{"999999", true},
		{"status", true},
		{"", false},
		{"abc23", false},
		{"abc2345", false},
		{"ABC234", false},
		{"abc23i", false},
		{"abc23l", false},
		{"abc23o", false},
		{"abc230", false},
		{"abc231", false},
		{"abc 23", false},
		{"../abc", false},
		{"ab/cde", false},
		{"abc.js", false},
		{"abcdé", false}, // six bytes, five characters
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := ValidID(tt.id); got != tt.want {
				t.Errorf("ValidID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// TestDefaultID is the only test that uses real randomness and the real
// clock: it checks what the zero Store falls back to. 500 ids make a missing
// alphabet character or a stuck generator all but certain to show.
func TestDefaultID(t *testing.T) {
	if len(idAlphabet) != 31 || strings.ContainsAny(idAlphabet, "ilo01") {
		t.Fatalf("alphabet %q is not the 31 unambiguous characters", idAlphabet)
	}
	s := Store{}
	seen := map[string]bool{}
	chars := map[rune]bool{}
	for range 500 {
		id := s.newID()
		if !ValidID(id) {
			t.Fatalf("default generator returned %q", id)
		}
		seen[id] = true
		for _, c := range id {
			chars[c] = true
		}
	}
	if len(seen) < 490 {
		t.Errorf("only %d distinct ids in 500", len(seen))
	}
	if len(chars) != len(idAlphabet) {
		t.Errorf("only %d of %d alphabet characters ever appeared", len(chars), len(idAlphabet))
	}

	before := time.Now().UTC().Add(-time.Second)
	got, err := time.Parse(time.RFC3339, s.now())
	if err != nil {
		t.Fatalf("default clock gave %q: %v", s.now(), err)
	}
	if got.Before(before) || got.After(before.Add(time.Minute)) || got.Location() != time.UTC {
		t.Errorf("default clock gave %v, want now in UTC", got)
	}
}

// TestDirAndOpen pins the two small helpers other packages build paths and
// markers from.
func TestDirAndOpen(t *testing.T) {
	if got, want := Dir("/srv/app"), filepath.Join("/srv/app", ".afk", "inbox"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if !(Item{}).Open() {
		t.Error("an item with no answer is not Open")
	}
	if (Item{Answer: "done"}).Open() {
		t.Error("an item with an answer is Open")
	}
}
