package ledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// writeLedger puts content at the ledger path of a fresh temp project and
// returns the project directory.
func writeLedger(t *testing.T, content string) string {
	t.Helper()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(Path(project)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(project), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return project
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// setTo is the usual Update callback: put one task in one status.
func setTo(id string, st Status, replacedBy ...string) func(*Ledger) error {
	return func(l *Ledger) error {
		_, err := l.Set(id, st, replacedBy)
		return err
	}
}

func TestPath(t *testing.T) {
	got := Path(filepath.Join("some", "project"))
	want := filepath.Join("some", "project", ".afk", "build", "ledger.json")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

// TestStatuses pins the order and the on-disk words. The order is what a
// command prints when it lists the valid statuses, and the words are the file
// format (PLAN §4.2), so neither may drift.
func TestStatuses(t *testing.T) {
	want := []Status{"pending", "running", "shipping", "parked", "done", "failed", "superseded"}
	got := Statuses()
	if !slices.Equal(got, want) {
		t.Fatalf("Statuses() = %v, want %v", got, want)
	}
	consts := []Status{Pending, Running, Shipping, Parked, Done, Failed, Superseded}
	if !slices.Equal(consts, want) {
		t.Errorf("constants = %v, want %v", consts, want)
	}
	// The caller may sort or trim what it is given without changing the next
	// caller's list.
	got[0] = "changed"
	if Statuses()[0] != Pending {
		t.Error("Statuses() returned a shared slice")
	}
}

// TestParseStatus: the load-bearing rows are the rejections. "Done" and
// " done" look harmless, but accepting them would let two spellings of one
// status into the file, and a reader comparing against the constant would
// then see a finished task as not done.
func TestParseStatus(t *testing.T) {
	for _, st := range Statuses() {
		t.Run("accepts "+string(st), func(t *testing.T) {
			got, err := ParseStatus(string(st))
			if err != nil || got != st {
				t.Errorf("ParseStatus(%q) = %q, %v", st, got, err)
			}
		})
	}
	for _, bad := range []string{"", "Done", " done", "finished", "complete"} {
		t.Run(fmt.Sprintf("rejects %q", bad), func(t *testing.T) {
			_, err := ParseStatus(bad)
			if !errors.Is(err, ErrUnknownStatus) {
				t.Fatalf("ParseStatus(%q) error = %v, want ErrUnknownStatus", bad, err)
			}
			// The message is shown to the owner as is, so it has to say
			// what would have been accepted.
			for _, st := range Statuses() {
				if !strings.Contains(err.Error(), string(st)) {
					t.Errorf("error %q does not list %q", err, st)
				}
			}
		})
	}
}

// TestLoad covers every way a ledger file can be read.
//
// The load-bearing rows are the corruption ones: each must be an error
// wrapping ErrCorrupt and never an empty ledger, because an empty ledger says
// "every task is pending" and the runner would start finished tasks again.
// "missing file" is the single case that is allowed to be empty. "unknown key"
// and "unknown status" guard against a typo in a hand edit silently dropping
// a field ("sesion") or inventing a state nothing knows how to treat.
// "trailing data" catches two ledgers concatenated by a bad merge, where the
// decoder would otherwise stop happily after the first.
func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil: no file at all
		want    Ledger
		wantErr string // substring of the error; empty means no error
	}{
		{
			name:    "missing file is an empty ledger",
			content: nil,
			want:    Ledger{},
		},
		{
			name:    "valid",
			content: ptr(`{"tasks":[{"id":"T0.1","status":"done","merged_sha":"abc123"},{"id":"T4.2","status":"superseded","replaced_by":["T4.2a","T4.2b"]}]}`),
			want: Ledger{Tasks: []Entry{
				{ID: "T0.1", Status: Done, MergedSHA: "abc123"},
				{ID: "T4.2", Status: Superseded, ReplacedBy: []string{"T4.2a", "T4.2b"}},
			}},
		},
		{
			name:    "valid with no tasks",
			content: ptr("{\"tasks\": []}\n"),
			want:    Ledger{Tasks: []Entry{}},
		},
		{
			name:    "valid with trailing whitespace",
			content: ptr("{\"tasks\":[{\"id\":\"T0.1\",\"status\":\"done\"}]}\n\n  \n"),
			want:    Ledger{Tasks: []Entry{{ID: "T0.1", Status: Done}}},
		},
		{
			name:    "corrupt: invalid JSON",
			content: ptr(`{not json`),
			wantErr: "not valid JSON",
		},
		{
			name:    "corrupt: empty file",
			content: ptr(""),
			wantErr: "the file is empty",
		},
		{
			name:    "corrupt: not an object",
			content: ptr("null\n"),
			wantErr: "JSON object",
		},
		{
			name:    "corrupt: wrong type for a field",
			content: ptr(`{"tasks":[{"id":"T0.1","status":"done","pr":"12"}]}`),
			wantErr: "not valid JSON",
		},
		{
			name:    "corrupt: unknown key in an entry",
			content: ptr(`{"tasks":[{"id":"T0.1","status":"done","sesion":"x"}]}`),
			wantErr: "sesion",
		},
		{
			name:    "corrupt: unknown key at the top",
			content: ptr(`{"tasks":[],"version":2}`),
			wantErr: "version",
		},
		{
			name:    "corrupt: trailing data",
			content: ptr(`{"tasks":[]}{"tasks":[]}`),
			wantErr: "after the end",
		},
		{
			name:    "corrupt: empty id",
			content: ptr(`{"tasks":[{"id":"T0.1","status":"done"},{"id":"","status":"done"}]}`),
			wantErr: "entry 2 has no id",
		},
		{
			name:    "corrupt: missing id",
			content: ptr(`{"tasks":[{"status":"done"}]}`),
			wantErr: "entry 1 has no id",
		},
		{
			name:    "corrupt: repeated id",
			content: ptr(`{"tasks":[{"id":"T0.1","status":"done"},{"id":"T0.2","status":"done"},{"id":"T0.1","status":"failed"}]}`),
			wantErr: `"T0.1" appears more than once`,
		},
		{
			name:    "corrupt: unknown status",
			content: ptr(`{"tasks":[{"id":"T0.1","status":"finished"}]}`),
			wantErr: `"T0.1" has unknown status "finished"`,
		},
		{
			name:    "corrupt: empty status",
			content: ptr(`{"tasks":[{"id":"T0.1","status":""}]}`),
			wantErr: `"T0.1" has no status`,
		},
		{
			name:    "corrupt: missing status",
			content: ptr(`{"tasks":[{"id":"T0.1"}]}`),
			wantErr: `"T0.1" has no status`,
		},
		{
			name:    "corrupt: status in the wrong letter case",
			content: ptr(`{"tasks":[{"id":"T0.1","status":"Done"}]}`),
			wantErr: `unknown status "Done"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			if tt.content != nil {
				project = writeLedger(t, *tt.content)
			}
			got, err := Load(project)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Load: unexpected error: %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("Load = %#v, want %#v", got, tt.want)
				}
				return
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Load error = %v, want one wrapping ErrCorrupt", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not say %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), Path(project)) {
				t.Errorf("error %q does not name the file %s", err, Path(project))
			}
			if len(got.Tasks) != 0 {
				t.Errorf("a corrupt ledger returned tasks: %#v", got)
			}
		})
	}
}

// TestLoadUnreadableIsNotCorrupt: a ledger that cannot be read at all (here,
// a directory where the file should be) is an error, but not ErrCorrupt. The
// commands tell the owner to fix or delete a corrupt file; that advice is
// wrong for a file whose content was never seen. It must still not be an
// empty ledger.
func TestLoadUnreadableIsNotCorrupt(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(Path(project), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Load(project)
	if err == nil {
		t.Fatal("Load of a directory returned no error")
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("error %v wraps ErrCorrupt, want a plain read error", err)
	}
	if !strings.Contains(err.Error(), Path(project)) {
		t.Errorf("error %q does not name the file", err)
	}
}

// TestLoadCorruptLedgerStopsUpdateAndStaysUntouched: Update on a damaged
// ledger must fail with ErrCorrupt before fn runs and leave the file exactly
// as it was. Rewriting it would replace whatever the owner could still have
// recovered by hand with a ledger holding one task.
func TestLoadCorruptLedgerStopsUpdateAndStaysUntouched(t *testing.T) {
	const damaged = "{\"tasks\":[{\"id\":\"T0.1\",\"status\":\"done\"},\n  {\"id\":\"T0.2\",\"status\":\"do"
	project := writeLedger(t, damaged)

	called := false
	err := Update(project, func(l *Ledger) error {
		called = true
		_, err := l.Set("T0.3", Done, nil)
		return err
	})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Update error = %v, want one wrapping ErrCorrupt", err)
	}
	if called {
		t.Error("fn was called on a corrupt ledger")
	}
	if got := readFile(t, Path(project)); got != damaged {
		t.Errorf("corrupt ledger was changed:\n got %q\nwant %q", got, damaged)
	}
	assertOnlyLedgerFiles(t, project, true)
}

func ptr(s string) *string { return &s }

func TestGetAndStatus(t *testing.T) {
	l := Ledger{Tasks: []Entry{
		{ID: "T0.1", Status: Done, Session: "t0-1"},
		{ID: "T0.2", Status: Parked, ParkedOn: []string{"q1"}},
	}}
	if e, ok := l.Get("T0.1"); !ok || e.Session != "t0-1" || e.Status != Done {
		t.Errorf("Get(T0.1) = %+v, %v", e, ok)
	}
	if e, ok := l.Get("T9.9"); ok || !reflect.DeepEqual(e, Entry{}) {
		t.Errorf("Get(T9.9) = %+v, %v, want zero and false", e, ok)
	}
	if got := l.Status("T0.2"); got != Parked {
		t.Errorf("Status(T0.2) = %q, want parked", got)
	}
	// PLAN §4.2: a task absent from the ledger is pending. Everything that
	// decides what to run next leans on this.
	if got := l.Status("T9.9"); got != Pending {
		t.Errorf("Status of an absent id = %q, want pending", got)
	}
	if got := (Ledger{}).Status("T0.1"); got != Pending {
		t.Errorf("Status on an empty ledger = %q, want pending", got)
	}
}

// TestSet is the table for the one mutation the package offers.
//
// Load-bearing rows: "status change keeps the other fields" (a hand edit with
// `afk ledger set` must not wipe the session, branch or attempt count the
// daemon recorded), "leaving superseded clears replaced_by" (a stale list on
// a pending task would be read as history that is not true), "superseded
// without replacements" and "self-replacement" (a superseded task with nothing
// valid to wait for blocks every task that depends on it for good), and the
// "unchanged" rows, which are what lets Update skip the write.
func TestSet(t *testing.T) {
	full := Entry{
		ID: "T1.1", Status: Running, Session: "t1-1", Branch: "t1-1", PR: 7, Attempts: 2,
		ParkedOn: []string{"q3"}, StartedAt: "2026-10-11T08:00:00Z", FinishedAt: "2026-10-11T09:00:00Z",
		MergedSHA: "deadbeef",
	}
	withStatus := func(e Entry, st Status, replacedBy []string) Entry {
		e.Status, e.ReplacedBy = st, replacedBy
		return e
	}

	tests := []struct {
		name        string
		start       []Entry
		id          string
		status      Status
		replacedBy  []string
		want        []Entry
		wantChanged bool
		wantErr     error
	}{
		{
			name:   "new entry in an empty ledger",
			id:     "T0.1",
			status: Done,
			want:   []Entry{{ID: "T0.1", Status: Done}},

			wantChanged: true,
		},
		{
			name:   "new entry is added after the existing ones",
			start:  []Entry{{ID: "T0.1", Status: Done}},
			id:     "T0.2",
			status: Failed,
			want:   []Entry{{ID: "T0.1", Status: Done}, {ID: "T0.2", Status: Failed}},

			wantChanged: true,
		},
		{
			name:   "status change keeps the other fields",
			start:  []Entry{{ID: "T0.1", Status: Done}, full},
			id:     "T1.1",
			status: Done,
			want:   []Entry{{ID: "T0.1", Status: Done}, withStatus(full, Done, nil)},

			wantChanged: true,
		},
		{
			name:       "superseded with replacements",
			start:      []Entry{{ID: "T4.2", Status: Pending, Attempts: 1}},
			id:         "T4.2",
			status:     Superseded,
			replacedBy: []string{"T4.2a", "T4.2b"},
			want:       []Entry{{ID: "T4.2", Status: Superseded, Attempts: 1, ReplacedBy: []string{"T4.2a", "T4.2b"}}},

			wantChanged: true,
		},
		{
			name:       "superseded as a new entry",
			id:         "T4.2",
			status:     Superseded,
			replacedBy: []string{"T4.2a"},
			want:       []Entry{{ID: "T4.2", Status: Superseded, ReplacedBy: []string{"T4.2a"}}},

			wantChanged: true,
		},
		{
			name:    "superseded without replacements",
			start:   []Entry{{ID: "T4.2", Status: Done}},
			id:      "T4.2",
			status:  Superseded,
			want:    []Entry{{ID: "T4.2", Status: Done}},
			wantErr: ErrNoReplacement,
		},
		{
			name:       "superseded with an empty replacement id",
			start:      []Entry{{ID: "T4.2", Status: Done}},
			id:         "T4.2",
			status:     Superseded,
			replacedBy: []string{"T4.2a", ""},
			want:       []Entry{{ID: "T4.2", Status: Done}},
			wantErr:    ErrNoReplacement,
		},
		{
			name:       "self-replacement rejected",
			start:      []Entry{{ID: "T4.2", Status: Done}},
			id:         "T4.2",
			status:     Superseded,
			replacedBy: []string{"T4.2a", "T4.2"},
			want:       []Entry{{ID: "T4.2", Status: Done}},
			wantErr:    ErrSelfReplacement,
		},
		{
			name:       "repeated replacement ids are kept once",
			id:         "T4.2",
			status:     Superseded,
			replacedBy: []string{"T4.2a", "T4.2b", "T4.2a"},
			want:       []Entry{{ID: "T4.2", Status: Superseded, ReplacedBy: []string{"T4.2a", "T4.2b"}}},

			wantChanged: true,
		},
		{
			name:       "superseded with a different replacement list is a change",
			start:      []Entry{{ID: "T4.2", Status: Superseded, ReplacedBy: []string{"T4.2a"}}},
			id:         "T4.2",
			status:     Superseded,
			replacedBy: []string{"T4.2a", "T4.2b"},
			want:       []Entry{{ID: "T4.2", Status: Superseded, ReplacedBy: []string{"T4.2a", "T4.2b"}}},

			wantChanged: true,
		},
		{
			name:   "leaving superseded clears replaced_by",
			start:  []Entry{{ID: "T4.2", Status: Superseded, Session: "t4-2", ReplacedBy: []string{"T4.2a", "T4.2b"}}},
			id:     "T4.2",
			status: Pending,
			want:   []Entry{{ID: "T4.2", Status: Pending, Session: "t4-2"}},

			wantChanged: true,
		},
		{
			name:       "replacements given with another status are not stored",
			id:         "T4.3",
			status:     Done,
			replacedBy: []string{"T4.2a"},
			want:       []Entry{{ID: "T4.3", Status: Done}},

			wantChanged: true,
		},
		{
			name:   "same status again is unchanged",
			start:  []Entry{full},
			id:     "T1.1",
			status: Running,
			want:   []Entry{full},
		},
		{
			name:       "same superseded call again is unchanged",
			start:      []Entry{{ID: "T4.2", Status: Superseded, ReplacedBy: []string{"T4.2a", "T4.2b"}}},
			id:         "T4.2",
			status:     Superseded,
			replacedBy: []string{"T4.2a", "T4.2b"},
			want:       []Entry{{ID: "T4.2", Status: Superseded, ReplacedBy: []string{"T4.2a", "T4.2b"}}},
		},
		{
			name:    "empty id rejected",
			id:      "",
			status:  Done,
			wantErr: ErrEmptyID,
		},
		{
			name:    "unknown status rejected",
			start:   []Entry{{ID: "T0.1", Status: Done}},
			id:      "T0.1",
			status:  Status("finished"),
			want:    []Entry{{ID: "T0.1", Status: Done}},
			wantErr: ErrUnknownStatus,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := Ledger{Tasks: slices.Clone(tt.start)}
			changed, err := l.Set(tt.id, tt.status, tt.replacedBy)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Set error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Set: unexpected error: %v", err)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if len(l.Tasks) != len(tt.want) {
				t.Fatalf("tasks = %#v, want %#v", l.Tasks, tt.want)
			}
			for i := range tt.want {
				if !reflect.DeepEqual(l.Tasks[i], tt.want[i]) {
					t.Errorf("task %d = %#v, want %#v", i, l.Tasks[i], tt.want[i])
				}
			}
		})
	}
}

// TestSetTwiceIsUnchanged runs the same call twice on one ledger, which is
// what `afk ledger set` does when the owner repeats a command.
func TestSetTwiceIsUnchanged(t *testing.T) {
	var l Ledger
	for i, want := range []bool{true, false} {
		changed, err := l.Set("T4.2", Superseded, []string{"T4.2a", "T4.2b"})
		if err != nil || changed != want {
			t.Errorf("call %d: changed = %v, err = %v; want %v, nil", i+1, changed, err, want)
		}
	}
	if len(l.Tasks) != 1 {
		t.Errorf("tasks = %#v, want one entry", l.Tasks)
	}
}

// TestSetDoesNotKeepTheCallersSlice: the stored list must be a copy, or a
// caller reusing its slice would edit the ledger behind Set's back.
func TestSetDoesNotKeepTheCallersSlice(t *testing.T) {
	var l Ledger
	repl := []string{"T4.2a", "T4.2b"}
	if _, err := l.Set("T4.2", Superseded, repl); err != nil {
		t.Fatal(err)
	}
	repl[0] = "T9.9"
	if got := l.Tasks[0].ReplacedBy; !slices.Equal(got, []string{"T4.2a", "T4.2b"}) {
		t.Errorf("replaced_by followed the caller's slice: %v", got)
	}
}

// TestSortOrder: ids sort by milestone, then task number, then letter, each
// number compared as a number. The load-bearing pairs are T4.2 < T4.2a (no
// letter first), T4.2b < T4.10 and T4.10 < T10.1 (a plain string sort gets
// both wrong), and anything that is not a task id going last so one odd hand
// edit cannot scatter the rest.
func TestSortOrder(t *testing.T) {
	in := []string{"zeta", "T4.10", "T4.2a", "T10.1", "T4.2", "T4.2b", "T0.3", "alpha", "T4.1", "T4.2aa", "t1.1"}
	want := []string{"T0.3", "T4.1", "T4.2", "T4.2a", "T4.2b", "T4.10", "T10.1", "T4.2aa", "alpha", "t1.1", "zeta"}

	l := Ledger{}
	for _, id := range in {
		l.Tasks = append(l.Tasks, Entry{ID: id, Status: Pending})
	}
	sortTasks(l.Tasks)
	var got []string
	for _, e := range l.Tasks {
		got = append(got, e.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("sorted ids = %v\n        want %v", got, want)
	}
}

// assertOnlyLedgerFiles fails if .afk/build holds anything but the lock file
// and, when wantLedger is set, the ledger. A leftover temp file is the thing
// being looked for.
func assertOnlyLedgerFiles(t *testing.T, project string, wantLedger bool) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(Path(project)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{".ledger.lock"}
	if wantLedger {
		want = append(want, "ledger.json")
	}
	if !slices.Equal(got, want) {
		t.Errorf(".afk/build holds %v, want %v", got, want)
	}
}

// TestUpdateCreatesDirectoryAndFile: the first write in a project has nothing
// to start from, not even .afk/build. The file must end with a newline, use
// two-space indentation and leave no temp file beside it.
func TestUpdateCreatesDirectoryAndFile(t *testing.T) {
	project := t.TempDir()
	if err := Update(project, setTo("T0.1", Done)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	const want = `{
  "tasks": [
    {
      "id": "T0.1",
      "status": "done"
    }
  ]
}
`
	if got := readFile(t, Path(project)); got != want {
		t.Errorf("ledger file:\n%s\nwant:\n%s", got, want)
	}
	assertOnlyLedgerFiles(t, project, true)
}

// TestUpdateWritesSortedAndReloadsEqual: several updates in a scrambled order
// leave a file sorted by id that ends with a newline and loads back to the
// same ledger a second writer sees.
func TestUpdateWritesSortedAndReloadsEqual(t *testing.T) {
	project := t.TempDir()
	for _, id := range []string{"T4.10", "T4.2a", "T10.1", "T4.2b", "T0.3"} {
		if err := Update(project, setTo(id, Done)); err != nil {
			t.Fatalf("Update %s: %v", id, err)
		}
	}
	if err := Update(project, setTo("T4.2", Superseded, "T4.2a", "T4.2b")); err != nil {
		t.Fatal(err)
	}

	var seen Ledger
	if err := Update(project, func(l *Ledger) error { seen = *l; return nil }); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, seen) {
		t.Errorf("Load = %#v\nUpdate saw %#v", loaded, seen)
	}
	var ids []string
	for _, e := range loaded.Tasks {
		ids = append(ids, e.ID)
	}
	want := []string{"T0.3", "T4.2", "T4.2a", "T4.2b", "T4.10", "T10.1"}
	if !slices.Equal(ids, want) {
		t.Errorf("ids in the file = %v, want %v", ids, want)
	}
	if e, _ := loaded.Get("T4.2"); !slices.Equal(e.ReplacedBy, []string{"T4.2a", "T4.2b"}) {
		t.Errorf("T4.2 replaced_by = %v", e.ReplacedBy)
	}
	if data := readFile(t, Path(project)); !strings.HasSuffix(data, "}\n") {
		t.Errorf("file does not end with a newline: %q", data[max(0, len(data)-10):])
	}
	assertOnlyLedgerFiles(t, project, true)
}

// TestUpdateRoundTripEveryField: every field of PLAN §4.2 survives a write
// and a read. A field with a wrong or missing JSON tag would vanish here.
func TestUpdateRoundTripEveryField(t *testing.T) {
	want := Entry{
		ID: "T4.2", Status: Superseded, Session: "t4-2", Branch: "feat/t4-2", PR: 41, Attempts: 3,
		ParkedOn: []string{"q1", "q2"}, StartedAt: "2026-10-11T08:00:00Z", FinishedAt: "2026-10-11T09:30:00Z",
		MergedSHA: "0123abcd", ReplacedBy: []string{"T4.2a", "T4.2b"},
		Ship: "pr_open", ShipAt: "2026-10-11T09:00:00Z", CIReruns: 1, CIFixCycles: 2,
	}
	// Guard the test itself: if a field is added to Entry and not set above,
	// this round trip would no longer cover every field.
	v := reflect.ValueOf(want)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Fatalf("test entry leaves %s unset", v.Type().Field(i).Name)
		}
	}

	project := t.TempDir()
	err := Update(project, func(l *Ledger) error {
		l.Tasks = append(l.Tasks, want)
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := Load(project)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, Ledger{Tasks: []Entry{want}}) {
		t.Errorf("round trip = %#v\n      want %#v", got.Tasks, want)
	}
	data := readFile(t, Path(project))
	for _, key := range []string{"id", "status", "session", "branch", "pr", "attempts", "parked_on", "started_at", "finished_at", "merged_sha", "replaced_by", "ship", "ship_at", "ci_reruns", "ci_fix_cycles"} {
		if !strings.Contains(data, `"`+key+`": `) {
			t.Errorf("file has no %q key:\n%s", key, data)
		}
	}
	if !strings.Contains(data, `"pr": 41,`) || !strings.Contains(data, `"attempts": 3,`) {
		t.Errorf("pr and attempts are not written as numbers:\n%s", data)
	}
}

// TestUpdateFnErrorWritesNothing: an error from fn is returned as it is and
// the ledger on disk does not change, whether or not there was one.
func TestUpdateFnErrorWritesNothing(t *testing.T) {
	boom := errors.New("boom")
	failAfterSet := func(l *Ledger) error {
		if _, err := l.Set("T0.2", Done, nil); err != nil {
			return err
		}
		return boom
	}

	t.Run("no ledger yet", func(t *testing.T) {
		project := t.TempDir()
		if err := Update(project, failAfterSet); !errors.Is(err, boom) {
			t.Fatalf("Update error = %v, want boom", err)
		}
		if _, err := os.Stat(Path(project)); !os.IsNotExist(err) {
			t.Errorf("ledger file exists after fn failed (stat error %v)", err)
		}
		assertOnlyLedgerFiles(t, project, false)
	})

	t.Run("existing ledger", func(t *testing.T) {
		const before = "{\"tasks\":[{\"id\":\"T0.1\",\"status\":\"done\"}]}"
		project := writeLedger(t, before)
		if err := Update(project, failAfterSet); !errors.Is(err, boom) {
			t.Fatalf("Update error = %v, want boom", err)
		}
		if got := readFile(t, Path(project)); got != before {
			t.Errorf("ledger changed after fn failed: %q", got)
		}
		assertOnlyLedgerFiles(t, project, true)
	})
}

// TestUpdateSkipsTheWriteWhenNothingChanges: an update that leaves the bytes
// as they are must not replace the file. The rename gives the file a new
// inode, so "same file as before" is checked with os.SameFile; comparing
// content alone could not tell a skipped write from an identical rewrite.
func TestUpdateSkipsTheWriteWhenNothingChanges(t *testing.T) {
	project := t.TempDir()
	if err := Update(project, setTo("T0.1", Done)); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if err := Update(project, setTo("T0.1", Done)); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("an unchanged ledger was rewritten")
	}

	// And a real change does replace it, so the check above can fail.
	if err := Update(project, setTo("T0.1", Failed)); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Stat(Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, changed) {
		t.Error("a changed ledger was written in place, not renamed over")
	}
}

// TestUpdateNoOpOnMissingLedgerCreatesNoFile: reading through Update in a
// project that has no ledger must not leave an empty one behind.
func TestUpdateNoOpOnMissingLedgerCreatesNoFile(t *testing.T) {
	project := t.TempDir()
	if err := Update(project, func(*Ledger) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(project)); !os.IsNotExist(err) {
		t.Errorf("ledger file exists after a no-op update (stat error %v)", err)
	}
}

// TestUpdateRefusesToWriteALedgerLoadWouldReject: fn can do anything to the
// ledger it is handed. If it leaves a repeated id or an unknown status,
// writing that would make every later Load fail, so Update must refuse and
// keep the file as it was.
func TestUpdateRefusesToWriteALedgerLoadWouldReject(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*Ledger) error
	}{
		{"repeated id", func(l *Ledger) error {
			l.Tasks = append(l.Tasks, Entry{ID: "T0.1", Status: Failed})
			return nil
		}},
		{"unknown status", func(l *Ledger) error {
			l.Tasks = append(l.Tasks, Entry{ID: "T0.2", Status: "finished"})
			return nil
		}},
		{"empty id", func(l *Ledger) error {
			l.Tasks = append(l.Tasks, Entry{Status: Done})
			return nil
		}},
		{"unknown ship state", func(l *Ledger) error {
			l.Tasks = append(l.Tasks, Entry{ID: "T0.2", Status: Shipping, Ship: "flying"})
			return nil
		}},
		{"negative ci_reruns", func(l *Ledger) error {
			l.Tasks = append(l.Tasks, Entry{ID: "T0.2", Status: Shipping, CIReruns: -1})
			return nil
		}},
		{"negative ci_fix_cycles", func(l *Ledger) error {
			l.Tasks = append(l.Tasks, Entry{ID: "T0.2", Status: Shipping, CIFixCycles: -1})
			return nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			if err := Update(project, setTo("T0.1", Done)); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, Path(project))
			err := Update(project, tt.fn)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Update error = %v, want one wrapping ErrInvalid", err)
			}
			if got := readFile(t, Path(project)); got != before {
				t.Errorf("ledger changed:\n%s", got)
			}
			if _, err := Load(project); err != nil {
				t.Errorf("ledger no longer loads: %v", err)
			}
			assertOnlyLedgerFiles(t, project, true)
		})
	}
}

// TestUpdateOverwritesALeftoverTempFile: a writer killed between writing the
// temp file and renaming it leaves that file behind. The next writer with the
// same pid must not trip over it or fold its content into the ledger.
func TestUpdateOverwritesALeftoverTempFile(t *testing.T) {
	project := writeLedger(t, `{"tasks":[]}`)
	tmp := filepath.Join(filepath.Dir(Path(project)), fmt.Sprintf(".ledger.json.tmp.%d", os.Getpid()))
	if err := os.WriteFile(tmp, []byte("half a ledg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Update(project, setTo("T0.1", Done)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	l, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if l.Status("T0.1") != Done || len(l.Tasks) != 1 {
		t.Errorf("ledger = %#v", l)
	}
	assertOnlyLedgerFiles(t, project, true)
}

// TestUpdateConcurrentWritersAllSurvive is the reason Update takes a lock.
// Many writers each record a different task at the same moment; every one
// must be in the file afterwards.
//
// Without the flock in Update this fails: each writer reads the ledger, adds
// its own entry and renames, and a later rename replaces an earlier writer's
// file, dropping that task with no error. (Checked by taking the Flock call
// out: the test then fails. In one process the writers also share a temp file
// name, so the failure can show up as a failed rename before it shows up as a
// lost entry.) Each goroutine opens its own descriptor on the lock file, and
// flock locks belong to the open file description, so goroutines in one
// process exclude each other exactly as separate `afk ledger set` processes
// do.
//
// The ids run past 9 on purpose: the file must come out in numeric order
// (T1.9 before T1.10) however the writers were interleaved.
//
// Run with -count=20 when touching Update.
func TestUpdateConcurrentWritersAllSurvive(t *testing.T) {
	const writers = 24
	project := t.TempDir()

	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 1; i <= writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("T1.%d", i)
			<-start
			errs <- Update(project, func(l *Ledger) error {
				changed, err := l.Set(id, Done, nil)
				if err == nil && !changed {
					err = fmt.Errorf("%s: reported unchanged", id)
				}
				return err
			})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	l, err := Load(project)
	if err != nil {
		t.Fatalf("Load after the writers: %v", err)
	}
	if len(l.Tasks) != writers {
		t.Errorf("%d tasks in the ledger, want %d", len(l.Tasks), writers)
	}
	for i := 1; i <= writers; i++ {
		id := fmt.Sprintf("T1.%d", i)
		if got := l.Status(id); got != Done {
			t.Errorf("%s: status %q, want done (its write was lost)", id, got)
		}
		if i <= len(l.Tasks) && l.Tasks[i-1].ID != id {
			t.Errorf("position %d holds %s, want %s", i, l.Tasks[i-1].ID, id)
		}
	}
	t.Logf("all %d ids present in %s", len(l.Tasks), filepath.Base(Path(project)))
	assertOnlyLedgerFiles(t, project, true)
}

// TestShipFieldsValidation: the ship fields are checked on read as well as on
// write, and the error names the task. The byte-for-byte row is load-bearing:
// the shipper rewrites entries constantly and a field that reformatted would
// churn the file.
func TestShipFieldsValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"valid round trips", "{\n  \"tasks\": [\n    {\n      \"id\": \"T1.1\",\n      \"status\": \"shipping\",\n      \"ship\": \"pr_open\",\n      \"ci_reruns\": 1\n    }\n  ]\n}\n", ""},
		{"unknown ship state", `{"tasks":[{"id":"T1.1","status":"shipping","ship":"flying"}]}`, `task "T1.1"`},
		{"negative reruns", `{"tasks":[{"id":"T1.1","status":"shipping","ci_reruns":-1}]}`, `task "T1.1"`},
		{"negative fix cycles", `{"tasks":[{"id":"T1.1","status":"shipping","ci_fix_cycles":-2}]}`, `task "T1.1"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := writeLedger(t, tt.content)
			l, err := Load(project)
			if tt.wantErr != "" {
				if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load error = %v, want ErrCorrupt naming %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// Rewriting through Update with a no-op must leave the bytes alone.
			if err := Update(project, func(*Ledger) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, Path(project)); got != tt.content {
				t.Errorf("file changed:\n%s", got)
			}
			if e, _ := l.Get("T1.1"); e.Ship != "pr_open" || e.CIReruns != 1 {
				t.Errorf("entry = %+v", e)
			}
		})
	}
}

func TestShipStates(t *testing.T) {
	got := ShipStates()
	if len(got) != 8 || got[0] != "committed" || got[7] != "stopped" {
		t.Errorf("ShipStates = %v", got)
	}
	got[0] = "x"
	if ShipStates()[0] != "committed" {
		t.Error("ShipStates shares its slice")
	}
}

// TestSetLeavesShipFieldsAlone: Set changes status only; the shipper's record
// must survive an owner running `afk ledger set`.
func TestSetLeavesShipFieldsAlone(t *testing.T) {
	l := Ledger{Tasks: []Entry{{ID: "T1.1", Status: Shipping, Ship: "pushed", ShipAt: "2026-10-11T09:00:00Z", CIReruns: 1, CIFixCycles: 1}}}
	if _, err := l.Set("T1.1", Parked, nil); err != nil {
		t.Fatal(err)
	}
	e := l.Tasks[0]
	if e.Ship != "pushed" || e.ShipAt == "" || e.CIReruns != 1 || e.CIFixCycles != 1 {
		t.Errorf("entry = %+v", e)
	}
}

// TestExistsAndInit: the existing-file rows are load-bearing. Init over any
// file, damaged included, must refuse and leave the bytes alone, because a
// replaced ledger forgets finished work. The repeated-id row covers
// --done-through plus --done naming one task twice.
func TestExistsAndInit(t *testing.T) {
	tests := []struct {
		name       string
		existing   *string
		ids        []string
		wantExists bool
		wantErr    error
		wantFile   string
	}{
		{"no file with ids", nil, []string{"T0.2", "T0.1"}, false, nil,
			"{\n  \"tasks\": [\n    {\n      \"id\": \"T0.1\",\n      \"status\": \"done\"\n    },\n    {\n      \"id\": \"T0.2\",\n      \"status\": \"done\"\n    }\n  ]\n}\n"},
		{"no file without ids", nil, nil, false, nil, "{\n  \"tasks\": []\n}\n"},
		{"repeated ids kept once", nil, []string{"T0.2", "T0.2"}, false, nil,
			"{\n  \"tasks\": [\n    {\n      \"id\": \"T0.2\",\n      \"status\": \"done\"\n    }\n  ]\n}\n"},
		{"existing file", ptr(`{"tasks":[]}`), []string{"T0.1"}, true, ErrExists, `{"tasks":[]}`},
		{"existing damaged file", ptr(`not json`), nil, true, ErrExists, `not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			if tt.existing != nil {
				project = writeLedger(t, *tt.existing)
			}
			if got := Exists(project); got != tt.wantExists {
				t.Errorf("Exists = %v, want %v", got, tt.wantExists)
			}
			err := Init(project, tt.ids)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Init error = %v, want %v", err, tt.wantErr)
			}
			if got := readFile(t, Path(project)); got != tt.wantFile {
				t.Errorf("file = %q, want %q", got, tt.wantFile)
			}
			if !Exists(project) {
				t.Error("Exists is false after Init")
			}
			if tt.wantErr == nil {
				if _, err := Load(project); err != nil {
					t.Errorf("Load after Init: %v", err)
				}
			}
			assertOnlyLedgerFiles(t, project, true)
		})
	}
}

// TestExistsDirectoryIsNotALedger: a directory at the ledger path is not a
// regular file.
func TestExistsDirectoryIsNotALedger(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(Path(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if Exists(project) {
		t.Error("Exists is true for a directory")
	}
}
