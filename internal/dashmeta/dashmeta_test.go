package dashmeta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWriteAndReadEntry exercises the full write/read round-trip. The
// cross-worktree row is load-bearing: it verifies the location is stable
// across different worktree cwds, which is the whole reason git common dir is
// used. The dead-pid-self-heal row is load-bearing: it proves the reader
// cleans up stale entries without external intervention.
func TestWriteAndReadEntry(t *testing.T) {
	tests := []struct {
		name      string
		tmux      string
		paneID    string
		clientTTY string
		wantWrite bool
	}{
		{
			name:      "both env vars set",
			tmux:      "/private/tmp/tmux-501/default,91850,3",
			paneID:    "%42",
			clientTTY: "/dev/ttys003",
			wantWrite: true,
		},
		{
			name:      "tmux set but pane empty",
			tmux:      "/private/tmp/tmux-501/default,91850,3",
			paneID:    "",
			clientTTY: "/dev/ttys003",
			wantWrite: false,
		},
		{
			name:      "neither set",
			tmux:      "",
			paneID:    "",
			clientTTY: "",
			wantWrite: false,
		},
		{
			name:      "pane set but tmux empty",
			tmux:      "",
			paneID:    "%42",
			clientTTY: "/dev/ttys003",
			wantWrite: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitCommonDir := t.TempDir()
			startedAt := "2026-08-05T12:00:00Z"

			if err := WriteEntry(gitCommonDir, tt.tmux, tt.paneID, tt.clientTTY, startedAt); err != nil {
				t.Fatalf("WriteEntry: %v", err)
			}

			path := filepath.Join(gitCommonDir, "afk-dash", fmt.Sprintf("%d.json", os.Getpid()))
			_, err := os.Stat(path)
			if tt.wantWrite {
				if err != nil {
					t.Fatalf("expected file at %s to exist: %v", path, err)
				}

				got, found := ReadEntry(gitCommonDir)
				if !found {
					t.Fatal("ReadEntry: expected entry to be found")
				}
				if got.PID != os.Getpid() {
					t.Errorf("PID = %d, want %d", got.PID, os.Getpid())
				}
				if got.Tmux != tt.tmux {
					t.Errorf("Tmux = %q, want %q", got.Tmux, tt.tmux)
				}
				if got.PaneID != tt.paneID {
					t.Errorf("PaneID = %q, want %q", got.PaneID, tt.paneID)
				}
				if got.ClientTTY != tt.clientTTY {
					t.Errorf("ClientTTY = %q, want %q", got.ClientTTY, tt.clientTTY)
				}
				if got.StartedAt != startedAt {
					t.Errorf("StartedAt = %q, want %q", got.StartedAt, startedAt)
				}
			} else {
				if err == nil {
					t.Errorf("expected no file at %s, but it exists", path)
				}
			}
		})
	}
}

// TestCrossWorktreeRoundTrip verifies that the location is stable across
// different worktree cwds. This is the critical test that would have caught
// the original design bug where the location was worktree-relative instead of
// repo-relative via git common dir.
func TestCrossWorktreeRoundTrip(t *testing.T) {
	gitCommonDir := t.TempDir()

	worktreeA := filepath.Join(gitCommonDir, "worktree-a")
	worktreeB := filepath.Join(gitCommonDir, "worktree-b")
	if err := os.MkdirAll(worktreeA, 0o755); err != nil {
		t.Fatalf("creating worktree A: %v", err)
	}
	if err := os.MkdirAll(worktreeB, 0o755); err != nil {
		t.Fatalf("creating worktree B: %v", err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()

	if err := os.Chdir(worktreeA); err != nil {
		t.Fatalf("Chdir to worktree A: %v", err)
	}

	if err := WriteEntry(gitCommonDir, "/tmp/tmux,1,2", "%99", "/dev/ttys042", "2026-08-05T12:00:00Z"); err != nil {
		t.Fatalf("WriteEntry from worktree A: %v", err)
	}

	if err := os.Chdir(worktreeB); err != nil {
		t.Fatalf("Chdir to worktree B: %v", err)
	}

	got, found := ReadEntry(gitCommonDir)
	if !found {
		t.Fatal("ReadEntry from worktree B: expected entry to be found")
	}
	if got.PaneID != "%99" {
		t.Errorf("PaneID = %q, want %q", got.PaneID, "%99")
	}
}

// TestReadEntryDeadPidSelfHeal verifies that the reader removes stale entries
// for dead processes and returns the first live one. This is the self-healing
// mechanism that prevents stale metadata accumulation.
func TestReadEntryDeadPidSelfHeal(t *testing.T) {
	gitCommonDir := t.TempDir()
	dir := filepath.Join(gitCommonDir, "afk-dash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating afk-dash dir: %v", err)
	}

	deadPID := knownDeadPID(t)
	livePID := os.Getpid()

	deadEntry := Entry{PID: deadPID, Tmux: "/tmp/tmux,1,2", PaneID: "%dead", ClientTTY: "/dev/ttys001", StartedAt: "2026-08-05T11:00:00Z"}
	liveEntry := Entry{PID: livePID, Tmux: "/tmp/tmux,1,2", PaneID: "%live", ClientTTY: "/dev/ttys002", StartedAt: "2026-08-05T12:00:00Z"}

	for _, e := range []Entry{deadEntry, liveEntry} {
		data, err := json.MarshalIndent(e, "", "  ")
		if err != nil {
			t.Fatalf("marshaling entry: %v", err)
		}
		path := filepath.Join(dir, fmt.Sprintf("%d.json", e.PID))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("writing entry: %v", err)
		}
	}

	got, found := ReadEntry(gitCommonDir)
	if !found {
		t.Fatal("ReadEntry: expected to find live entry")
	}
	if got.PID != livePID {
		t.Errorf("PID = %d, want %d (live)", got.PID, livePID)
	}
	if got.PaneID != "%live" {
		t.Errorf("PaneID = %q, want %q", got.PaneID, "%live")
	}

	deadPath := filepath.Join(dir, fmt.Sprintf("%d.json", deadPID))
	if _, err := os.Stat(deadPath); err == nil {
		t.Errorf("expected dead entry at %s to be removed, but it still exists", deadPath)
	}
}

// TestReadEntryLeavesTheRequestFileAlone is the Entry/Request boundary test, and
// it is the load-bearing one in this file: the two file kinds share ONE directory,
// so a reader of either that scans by extension reads the other's file too.
//
// Request unmarshals cleanly into Entry — encoding/json ignores unknown fields —
// so a request.json arrived at Entry{PID: 0}, failed alive(0), and was deleted by
// the dead-pid self-heal. Every `afk notify` calls ReadEntry, so a click's
// pending request was destroyed by the next notification before the dashboard
// could read it: click-to-focus worked only when no hook fired in between.
//
// The assertions are separate on purpose. Survival of the request file is the
// blocker; not-found for the entry is the other half (a request must never be
// mistaken FOR an entry and targeted as a pane).
func TestReadEntryLeavesTheRequestFileAlone(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := WriteRequest(gitCommonDir, "target", testPID, time.Now()); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	// Any number of ReadEntry calls (one per `afk notify`) must not consume it.
	for i := 0; i < 3; i++ {
		if _, found := ReadEntry(gitCommonDir); found {
			t.Fatalf("ReadEntry call %d found an entry, but only a request was written: "+
				"a request must never be read as a dashboard entry", i+1)
		}
	}

	got, ok := ReadRequest(gitCommonDir, testPID)
	if !ok {
		t.Fatal("the pending focus request was destroyed by ReadEntry — every `afk notify` " +
			"would silently cancel a click before the dashboard could honor it")
	}
	if got.Name != "target" {
		t.Errorf("ReadRequest name = %q, want target", got.Name)
	}
}

// TestReadEntryIgnoresNonEntryFiles is the general form of the boundary above:
// the scan accepts pid-shaped names ONLY, so any future third file in afk-dash/
// is safe by construction rather than by a blacklist someone has to remember to
// extend.
func TestReadEntryIgnoresNonEntryFiles(t *testing.T) {
	gitCommonDir := t.TempDir()
	dir, err := EnsureDir(gitCommonDir)
	if err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	// Every one of these unmarshals into Entry{PID: 0} without error.
	strangers := []string{RequestFileName(testPID), "request.json", "notes.json", "0.json", "-1.json", "12a.json", "1.2.json"}
	for _, name := range strangers {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"pid":0}`), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	if _, found := ReadEntry(gitCommonDir); found {
		t.Error("ReadEntry found an entry among non-entry files")
	}

	for _, name := range strangers {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("ReadEntry deleted %s, which it does not own: %v", name, err)
		}
	}
}

// TestIsEntryFileName pins the name predicate directly. The pid-shaped rows are
// what makes the scan a positive match; "0.json" is rejected because pid 0 is not
// a process alive() could ever confirm, and the temp-file row keeps the
// atomic-rename discipline's intermediate state out of the scan.
func TestIsEntryFileName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"1.json", true},
		{"27881.json", true},
		{"99999999.json", true},
		{"0.json", false},
		{"-1.json", false},
		{"request.json", false},
		{"12a.json", false},
		{"a12.json", false},
		{"1.2.json", false},
		{".27881.json.tmp.27881", false},
		{"27881", false},
		{"27881.txt", false},
		{".json", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEntryFileName(tt.name); got != tt.want {
				t.Errorf("isEntryFileName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestReadEntryMalformedJSON verifies that unparseable entries are treated as
// not-found, never as an error.
func TestReadEntryMalformedJSON(t *testing.T) {
	gitCommonDir := t.TempDir()
	dir := filepath.Join(gitCommonDir, "afk-dash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating afk-dash dir: %v", err)
	}

	path := filepath.Join(dir, "12345.json")
	if err := os.WriteFile(path, []byte("not valid json{"), 0o644); err != nil {
		t.Fatalf("writing malformed entry: %v", err)
	}

	_, found := ReadEntry(gitCommonDir)
	if found {
		t.Error("ReadEntry: expected not-found for malformed JSON")
	}
}

// TestReadEntryEmptyDirectory verifies that an empty afk-dash directory is
// treated as not-found, never as an error.
func TestReadEntryEmptyDirectory(t *testing.T) {
	gitCommonDir := t.TempDir()
	_, found := ReadEntry(gitCommonDir)
	if found {
		t.Error("ReadEntry: expected not-found for empty directory")
	}
}

// TestReadEntryMissingDirectory verifies that a missing afk-dash directory
// is treated as not-found, never as an error.
func TestReadEntryMissingDirectory(t *testing.T) {
	gitCommonDir := t.TempDir()
	_, found := ReadEntry(gitCommonDir)
	if found {
		t.Error("ReadEntry: expected not-found for missing directory")
	}
}

// TestWriteEntryRelativePath verifies that WriteEntry treats a relative git
// common dir as "no location available" and writes nothing.
func TestWriteEntryRelativePath(t *testing.T) {
	relativePath := "relative/path"
	if err := WriteEntry(relativePath, "/tmp/tmux,1,2", "%42", "/dev/ttys003", "2026-08-05T12:00:00Z"); err != nil {
		t.Fatalf("WriteEntry with relative path: %v", err)
	}

	if _, err := os.Stat(filepath.Join(relativePath, "afk-dash")); err == nil {
		t.Error("expected no afk-dash directory to be created for relative path")
	}
}

// TestReadEntryRelativePath verifies that ReadEntry treats a relative git
// common dir as not-found.
func TestReadEntryRelativePath(t *testing.T) {
	relativePath := "relative/path"
	_, found := ReadEntry(relativePath)
	if found {
		t.Error("ReadEntry: expected not-found for relative path")
	}
}

// TestRemoveEntry verifies clean shutdown removal.
func TestRemoveEntry(t *testing.T) {
	gitCommonDir := t.TempDir()

	if err := WriteEntry(gitCommonDir, "/tmp/tmux,1,2", "%42", "/dev/ttys003", "2026-08-05T12:00:00Z"); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}

	path := filepath.Join(gitCommonDir, "afk-dash", fmt.Sprintf("%d.json", os.Getpid()))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s to exist before removal: %v", path, err)
	}

	if err := RemoveEntry(gitCommonDir); err != nil {
		t.Fatalf("RemoveEntry: %v", err)
	}

	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected file at %s to be removed", path)
	}
}

// TestRemoveEntryMissing verifies that removing a non-existent entry is not an
// error.
func TestRemoveEntryMissing(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := RemoveEntry(gitCommonDir); err != nil {
		t.Errorf("RemoveEntry for non-existent file: unexpected error %v", err)
	}
}

// TestWriteEntryNoLeftoverTempFile verifies the atomic rename leaves no temp
// file behind.
func TestWriteEntryNoLeftoverTempFile(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := WriteEntry(gitCommonDir, "/tmp/tmux,1,2", "%42", "/dev/ttys003", "2026-08-05T12:00:00Z"); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}

	dir := filepath.Join(gitCommonDir, "afk-dash")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	if len(names) != 1 {
		t.Errorf("directory entries = %v, want exactly 1 file", names)
	}
	expectedName := fmt.Sprintf("%d.json", os.Getpid())
	if names[0] != expectedName {
		t.Errorf("entry name = %q, want %q", names[0], expectedName)
	}
	if strings.HasPrefix(names[0], ".") {
		t.Errorf("entry name %q starts with '.', indicating leftover temp file", names[0])
	}
}

func knownDeadPID(t *testing.T) int {
	t.Helper()
	cmd := "/bin/sh"
	if _, err := os.Stat(cmd); err != nil {
		t.Skip("/bin/sh not available to spawn a short-lived process")
	}
	proc, err := os.StartProcess(cmd, []string{cmd, "-c", "true"}, &os.ProcAttr{})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	state, err := proc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !state.Exited() {
		t.Fatal("expected process to have exited")
	}
	return proc.Pid
}
