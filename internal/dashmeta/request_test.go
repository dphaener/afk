package dashmeta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testPID is the target dashboard pid these tests address requests to. A literal
// rather than os.Getpid() so the expected filename is a constant the assertions
// can spell out.
const testPID = 4242

// TestWriteAndReadRequest is the round-trip. The empty-name row is load-bearing:
// a request naming no session can never be honored, so nothing may reach disk for
// it — otherwise the dashboard's fallback poll re-reads an unhonorable file
// forever.
func TestWriteAndReadRequest(t *testing.T) {
	tests := []struct {
		name      string
		session   string
		wantWrite bool
	}{
		{name: "ordinary session name", session: "improve-desktop-notifications", wantWrite: true},
		{name: "empty name writes nothing", session: "", wantWrite: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitCommonDir := t.TempDir()
			at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

			if err := WriteRequest(gitCommonDir, tt.session, testPID, at); err != nil {
				t.Fatalf("WriteRequest: %v", err)
			}

			got, ok := ReadRequest(gitCommonDir, testPID)
			if ok != tt.wantWrite {
				t.Fatalf("ReadRequest ok = %v, want %v", ok, tt.wantWrite)
			}
			if !tt.wantWrite {
				if _, err := os.Stat(RequestPath(gitCommonDir, testPID)); err == nil {
					t.Errorf("expected no request file for an empty name")
				}
				return
			}
			if got.Name != tt.session {
				t.Errorf("Name = %q, want %q", got.Name, tt.session)
			}
			if got.RequestedAt != at.Format(time.RFC3339) {
				t.Errorf("RequestedAt = %q, want %q", got.RequestedAt, at.Format(time.RFC3339))
			}
		})
	}
}

// TestWriteRequestPathAndDirectory pins the location the notify click path and
// the dashboard watcher both have to agree on: <git-common-dir>/afk-dash/
// request.<pid>.json, beside the per-pid Entry files.
func TestWriteRequestPathAndDirectory(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := WriteRequest(gitCommonDir, "s", testPID, time.Now()); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	want := filepath.Join(gitCommonDir, "afk-dash", "request.4242.json")
	if got := RequestPath(gitCommonDir, testPID); got != want {
		t.Errorf("RequestPath() = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected the request at %s: %v", want, err)
	}
	if got := Dir(gitCommonDir); got != filepath.Dir(want) {
		t.Errorf("Dir() = %q, want %q", got, filepath.Dir(want))
	}
}

// TestWriteRequestCreatesDirectory covers the dashboard-outside-tmux case: no
// Entry was ever written, so afk-dash/ does not exist and the request write has
// to create it.
func TestWriteRequestCreatesDirectory(t *testing.T) {
	gitCommonDir := t.TempDir()
	if _, err := os.Stat(Dir(gitCommonDir)); err == nil {
		t.Fatal("precondition: afk-dash/ should not exist yet")
	}
	if err := WriteRequest(gitCommonDir, "s", testPID, time.Now()); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	if _, err := os.Stat(Dir(gitCommonDir)); err != nil {
		t.Errorf("expected afk-dash/ to be created: %v", err)
	}
}

// TestWriteRequestNoLeftoverTempFile guards the atomic-rename discipline. A temp
// file left in afk-dash/ would also be an fsnotify event the dashboard wakes
// on, so the leak is not merely untidy.
func TestWriteRequestNoLeftoverTempFile(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := WriteRequest(gitCommonDir, "s", testPID, time.Now()); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	entries, err := os.ReadDir(Dir(gitCommonDir))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temp file %q in afk-dash/", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != RequestFileName(testPID) {
		t.Errorf("afk-dash/ contents = %v, want just %s", names(entries), RequestFileName(testPID))
	}
}

// TestWriteRequestOverwritesPending pins last-click-wins. Two clicks in a row
// must leave exactly one request, naming the second session — a pending file is
// replaced by the rename, not appended to or refused.
func TestWriteRequestOverwritesPending(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := WriteRequest(gitCommonDir, "first", testPID, time.Now()); err != nil {
		t.Fatalf("WriteRequest first: %v", err)
	}
	if err := WriteRequest(gitCommonDir, "second", testPID, time.Now()); err != nil {
		t.Fatalf("WriteRequest second: %v", err)
	}

	got, ok := ReadRequest(gitCommonDir, testPID)
	if !ok || got.Name != "second" {
		t.Errorf("ReadRequest = %+v (ok=%v), want the second request", got, ok)
	}
	entries, err := os.ReadDir(Dir(gitCommonDir))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("afk-dash/ contents = %v, want exactly one request file", names(entries))
	}
}

// TestReadRequestDegradesSilently is the "every failure path does nothing" table.
// The malformed and no-name rows are load-bearing twice over: they must report
// not-found rather than an error, AND they must delete the offending file, or the
// dashboard's fallback poll re-reads it on every tick for the life of the repo.
func TestReadRequestDegradesSilently(t *testing.T) {
	tests := []struct {
		name       string
		contents   string
		write      bool
		wantRemove bool
	}{
		{name: "missing file", write: false},
		{name: "malformed json", contents: "{not json", write: true, wantRemove: true},
		{name: "valid json, no name", contents: `{"requested_at":"2026-08-05T12:00:00Z"}`, write: true, wantRemove: true},
		{name: "empty file", contents: "", write: true, wantRemove: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitCommonDir := t.TempDir()
			if tt.write {
				if _, err := EnsureDir(gitCommonDir); err != nil {
					t.Fatalf("EnsureDir: %v", err)
				}
				if err := os.WriteFile(RequestPath(gitCommonDir, testPID), []byte(tt.contents), 0o644); err != nil {
					t.Fatalf("writing request: %v", err)
				}
			}

			if _, ok := ReadRequest(gitCommonDir, testPID); ok {
				t.Fatalf("ReadRequest: expected not-found")
			}
			if tt.wantRemove {
				if _, err := os.Stat(RequestPath(gitCommonDir, testPID)); err == nil {
					t.Errorf("expected the unparseable request to be removed")
				}
			}
		})
	}
}

// TestRequestRelativeGitCommonDir verifies a non-absolute git common dir is "no
// location available" on every entry point rather than a directory created
// somewhere unexpected — the same refusal WriteEntry/ReadEntry make.
func TestRequestRelativeGitCommonDir(t *testing.T) {
	rel := "relative/path"
	if err := WriteRequest(rel, "s", testPID, time.Now()); err == nil {
		t.Error("WriteRequest with a relative dir should refuse")
	}
	if _, ok := ReadRequest(rel, testPID); ok {
		t.Error("ReadRequest with a relative dir should report not-found")
	}
	if err := RemoveRequest(rel, testPID); err != nil {
		t.Errorf("RemoveRequest with a relative dir: %v", err)
	}
	if got := RequestPath(rel, testPID); got != "" {
		t.Errorf("RequestPath(%q) = %q, want empty", rel, got)
	}
	if _, err := os.Stat(filepath.Join(rel, "afk-dash")); err == nil {
		t.Error("no afk-dash/ may be created for a relative dir")
	}
}

// TestRemoveRequest covers consumption and the missing-file case. Removal after
// handling is what makes a request honored at most once.
func TestRemoveRequest(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := WriteRequest(gitCommonDir, "s", testPID, time.Now()); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	if err := RemoveRequest(gitCommonDir, testPID); err != nil {
		t.Fatalf("RemoveRequest: %v", err)
	}
	if _, ok := ReadRequest(gitCommonDir, testPID); ok {
		t.Error("ReadRequest after RemoveRequest should report not-found")
	}
	if err := RemoveRequest(gitCommonDir, testPID); err != nil {
		t.Errorf("RemoveRequest on a missing file should not error: %v", err)
	}
}

// TestRequestFresh is the staleness cutoff matrix. Load-bearing rows: exactly-at
// the cutoff (inclusive, so the boundary is defined rather than accidental); past
// it (the whole point — a click made minutes ago must not yank focus); an
// unparseable and an empty stamp (a request that cannot say when it happened
// cannot be shown to be recent); and a future stamp, which is clock skew and must
// not read as infinitely fresh.
func TestRequestFresh(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	maxAge := 60 * time.Second

	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	tests := []struct {
		name      string
		at        string
		wantFresh bool
	}{
		{name: "just now", at: stamp(0), wantFresh: true},
		{name: "one second old", at: stamp(-time.Second), wantFresh: true},
		{name: "exactly at the cutoff", at: stamp(-maxAge), wantFresh: true},
		{name: "one second past the cutoff", at: stamp(-maxAge - time.Second), wantFresh: false},
		{name: "ten minutes old", at: stamp(-10 * time.Minute), wantFresh: false},
		{name: "unparseable stamp", at: "not a timestamp", wantFresh: false},
		{name: "empty stamp", at: "", wantFresh: false},
		{name: "slight future skew", at: stamp(2 * time.Second), wantFresh: true},
		{name: "far future is not fresh", at: stamp(10 * time.Minute), wantFresh: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RequestFresh(Request{Name: "s", RequestedAt: tt.at}, now, maxAge)
			if got != tt.wantFresh {
				t.Errorf("RequestFresh(%q) = %v, want %v", tt.at, got, tt.wantFresh)
			}
		})
	}
}

// TestRequestMaxAgeIsBounded pins the cutoff as a real, short-lived window: a
// zero or negative value would reject every request (dead feature), and an
// unbounded one would let an ancient click yank focus.
func TestRequestMaxAgeIsBounded(t *testing.T) {
	if RequestMaxAge <= 0 {
		t.Fatalf("RequestMaxAge = %v, must be positive or no request is ever honored", RequestMaxAge)
	}
	if RequestMaxAge > 5*time.Minute {
		t.Fatalf("RequestMaxAge = %v, too long: a click that old has missed its moment", RequestMaxAge)
	}
}

func names(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestRequestsAreAddressedToOneDashboard is the two-dashboard test at the storage
// layer, and it is the row the shared-file design could not pass. Two dashboards
// run concurrently against ONE afk-dash/ directory (verified real: pids 2121 and
// 83745 on this machine), a click publishes one request, and exactly one of them
// may see it.
//
// With a single shared request.json both dashboards read the same file, both called
// openPaneFor on the same session, and because internal/host serves exactly one
// client the second connection stole the session out of the first's pane. Scoping
// the file to the target pid is what makes "exactly one honors it" a property of
// the storage rather than a race.
func TestRequestsAreAddressedToOneDashboard(t *testing.T) {
	gitCommonDir := t.TempDir()
	const dashA, dashB = 1111, 2222

	if err := WriteRequest(gitCommonDir, "target", dashA, time.Now()); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	if _, ok := ReadRequest(gitCommonDir, dashA); !ok {
		t.Error("the addressed dashboard must see the request")
	}
	if got, ok := ReadRequest(gitCommonDir, dashB); ok {
		t.Errorf("the OTHER dashboard saw request %+v; both would open the session and "+
			"the second would steal it from the first", got)
	}
}

// TestRemoveRequestLeavesOtherDashboardsAlone is the consumption half of the same
// guarantee. Each dashboard removes only its own request, so one dashboard handling
// its click cannot cancel a click addressed to another.
func TestRemoveRequestLeavesOtherDashboardsAlone(t *testing.T) {
	gitCommonDir := t.TempDir()
	const dashA, dashB = 1111, 2222

	if err := WriteRequest(gitCommonDir, "for-a", dashA, time.Now()); err != nil {
		t.Fatalf("WriteRequest A: %v", err)
	}
	if err := WriteRequest(gitCommonDir, "for-b", dashB, time.Now()); err != nil {
		t.Fatalf("WriteRequest B: %v", err)
	}

	if err := RemoveRequest(gitCommonDir, dashA); err != nil {
		t.Fatalf("RemoveRequest A: %v", err)
	}
	if _, ok := ReadRequest(gitCommonDir, dashA); ok {
		t.Error("A's request survived its own consumption")
	}
	got, ok := ReadRequest(gitCommonDir, dashB)
	if !ok || got.Name != "for-b" {
		t.Errorf("B's request = %+v (ok=%v), want it untouched by A's consumption", got, ok)
	}
}

// TestIsRequestFileName is the name matrix, and its whole purpose is that the two
// file kinds sharing afk-dash/ never claim each other's names.
//
// The load-bearing rows are the two cross-kind ones: a bare "<pid>.json" must NOT
// read as a request (it is an Entry), and no name this accepts may be accepted by
// isEntryFileName. That second property is what stops ReadEntry's dead-pid
// self-heal from deleting request files — the exact bug that once made every
// `afk notify` cancel the click the user had just made.
func TestIsRequestFileName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "request.4242.json", want: true},
		{name: "request.1.json", want: true},
		{name: "4242.json", want: false},
		{name: "request.json", want: false},
		{name: "request..json", want: false},
		{name: "request.0.json", want: false},
		{name: "request.04242.json", want: false},
		{name: "request.42a.json", want: false},
		{name: "request.4242.json.tmp.99", want: false},
		{name: ".request.4242.json.tmp.99", want: false},
		{name: "request.4242", want: false},
		{name: "notes.json", want: false},
		{name: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRequestFileName(tt.name); got != tt.want {
				t.Errorf("IsRequestFileName(%q) = %v, want %v", tt.name, got, tt.want)
			}
			if IsRequestFileName(tt.name) && isEntryFileName(tt.name) {
				t.Errorf("%q reads as BOTH a request and an entry; ReadEntry's self-heal would delete it", tt.name)
			}
		})
	}
}

// TestRequestFileNameIsNotAnEntryName is the positive-match guarantee stated
// directly against the generated name, so a change to either naming scheme that
// made them overlap fails here rather than as a mysteriously vanishing click.
func TestRequestFileNameIsNotAnEntryName(t *testing.T) {
	for _, pid := range []int{1, 42, 4242, 99999} {
		name := RequestFileName(pid)
		if !IsRequestFileName(name) {
			t.Errorf("RequestFileName(%d) = %q, which IsRequestFileName rejects", pid, name)
		}
		if isEntryFileName(name) {
			t.Errorf("RequestFileName(%d) = %q reads as an Entry file; ReadEntry would delete it", pid, name)
		}
	}
}

// TestSweepStaleRequests covers the cleanup that per-dashboard addressing makes
// necessary: a live dashboard only ever deletes its OWN request, so a request
// written for a dashboard that died before reading it has no other collector.
//
// Load-bearing rows: the live pid's request SURVIVES (sweeping it would delete a
// click before the dashboard could honor it), and the entry file survives (the
// sweep must not repeat ReadEntry's mistake of touching a neighbouring file kind).
func TestSweepStaleRequests(t *testing.T) {
	gitCommonDir := t.TempDir()
	dir, err := EnsureDir(gitCommonDir)
	if err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	live := os.Getpid()
	const dead = 999999

	if err := WriteRequest(gitCommonDir, "for-live", live, time.Now()); err != nil {
		t.Fatalf("WriteRequest live: %v", err)
	}
	if err := WriteRequest(gitCommonDir, "for-dead", dead, time.Now()); err != nil {
		t.Fatalf("WriteRequest dead: %v", err)
	}
	entryName := filepath.Join(dir, "4242.json")
	if err := os.WriteFile(entryName, []byte(`{"pid":4242}`), 0o644); err != nil {
		t.Fatalf("writing entry: %v", err)
	}

	SweepStaleRequests(gitCommonDir)

	if _, ok := ReadRequest(gitCommonDir, live); !ok {
		t.Error("the LIVE dashboard's request was swept; its click would never be honored")
	}
	if _, err := os.Stat(RequestPath(gitCommonDir, dead)); err == nil {
		t.Error("the dead dashboard's request survived the sweep and would accumulate forever")
	}
	if _, err := os.Stat(entryName); err != nil {
		t.Errorf("the sweep deleted a neighbouring Entry file: %v", err)
	}
}
