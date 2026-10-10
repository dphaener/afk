package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestReadLabel is the load-bearing table for the reader's contract: present,
// absent, empty, and oversized all need distinct handling, and only "present"
// may report ok=true with the file's own text.
func TestReadLabel(t *testing.T) {
	tests := []struct {
		name      string
		fileBody  string // "" means never create the file
		wantLabel string
		wantOK    bool
	}{
		{
			name:      "present",
			fileBody:  "spike: pty timing\n",
			wantLabel: "spike: pty timing",
			wantOK:    true,
		},
		{
			name:     "absent",
			fileBody: "",
			wantOK:   false,
		},
		{
			name:     "empty file",
			fileBody: " \n\t\n",
			wantOK:   false,
		},
		{
			name:      "oversized is truncated, not rejected",
			fileBody:  strings.Repeat("x", maxLabelBytes+1000),
			wantLabel: strings.Repeat("x", maxLabelBytes),
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			dir := filepath.Join(worktree, ".afk", "billing")
			if tt.name != "absent" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "label"), []byte(tt.fileBody), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			gotLabel, gotOK := ReadLabel(worktree, "billing")
			if gotOK != tt.wantOK {
				t.Fatalf("ReadLabel ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotOK && gotLabel != tt.wantLabel {
				t.Errorf("ReadLabel label = %q (len %d), want %q (len %d)", gotLabel, len(gotLabel), tt.wantLabel, len(tt.wantLabel))
			}
			if !gotOK && gotLabel != "" {
				t.Errorf("ReadLabel label = %q, want empty when ok=false", gotLabel)
			}
		})
	}
}

// TestReadLabelFoldsEmbeddedNewlines guards renderRow's/renderDetail's
// fixed-line-count contract: a label reaching either with a raw newline
// would grow a row from two lines to three. Only reachable via a hand-edited
// file (the dashboard's textinput editor cannot produce a newline).
func TestReadLabelFoldsEmbeddedNewlines(t *testing.T) {
	worktree := t.TempDir()
	dir := filepath.Join(worktree, ".afk", "billing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "label"), []byte("first line\nsecond line\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, ok := ReadLabel(worktree, "billing")
	if !ok {
		t.Fatalf("ReadLabel ok = false, want true")
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("ReadLabel label contains a raw newline: %q", got)
	}
	if got != "first line second line" {
		t.Errorf("ReadLabel label = %q, want %q", got, "first line second line")
	}
}

// TestReadLabelFoldsLoneCarriageReturn guards the same fixed-line-count
// contract as TestReadLabelFoldsEmbeddedNewlines, for the newline variant
// that replacing "\r\n" then "\n" alone does not catch: a bare "\r" with no
// paired "\n" (a CR-only line ending), reachable only via a hand-edited file.
func TestReadLabelFoldsLoneCarriageReturn(t *testing.T) {
	worktree := t.TempDir()
	dir := filepath.Join(worktree, ".afk", "billing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "label"), []byte("left\rright\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, ok := ReadLabel(worktree, "billing")
	if !ok {
		t.Fatalf("ReadLabel ok = false, want true")
	}
	if strings.ContainsRune(got, '\r') {
		t.Fatalf("ReadLabel label contains a raw carriage return: %q", got)
	}
	if got != "left right" {
		t.Errorf("ReadLabel label = %q, want %q", got, "left right")
	}
}

// TestReadLabelStripsControlAndEscapeBytes covers the other half of the
// hand-edited-file threat model: C0 control bytes (including ESC, which is
// what would otherwise let a label smuggle an ANSI escape sequence into
// whatever terminal renders it) must never reach the caller.
func TestReadLabelStripsControlAndEscapeBytes(t *testing.T) {
	tests := []struct {
		name     string
		fileBody string
		want     string
	}{
		{
			name:     "ansi escape sequence",
			fileBody: "\x1b[31mdanger\x1b[0m\n",
			want:     "[31mdanger[0m",
		},
		{
			name:     "c0 control bytes",
			fileBody: "a\x01\x02\x07b\n",
			want:     "ab",
		},
		{
			name:     "bell and backspace",
			fileBody: "click\x07\x08!\n",
			want:     "click!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			dir := filepath.Join(worktree, ".afk", "billing")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "label"), []byte(tt.fileBody), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			got, ok := ReadLabel(worktree, "billing")
			if !ok {
				t.Fatalf("ReadLabel ok = false, want true")
			}
			if got != tt.want {
				t.Errorf("ReadLabel label = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReadLabelTruncatesOnRuneBoundary pins the 4096-byte cap
// (io.LimitReader(maxLabelBytes)) to never split a multi-byte UTF-8 rune: a
// label whose byte 4096 lands inside a rune must drop the whole rune rather
// than hand back a truncated, invalid trailing byte.
func TestReadLabelTruncatesOnRuneBoundary(t *testing.T) {
	// filler fills bytes [0, maxLabelBytes-1); the euro sign ("€", 3 bytes:
	// 0xE2 0x82 0xAC) starts at byte maxLabelBytes-1, so io.LimitReader's
	// maxLabelBytes-byte read includes only its first byte (0xE2) — exactly
	// the mid-rune cut this test exists to catch.
	filler := strings.Repeat("a", maxLabelBytes-1)
	body := filler + "€" + strings.Repeat("b", 200)

	worktree := t.TempDir()
	dir := filepath.Join(worktree, ".afk", "billing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "label"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, ok := ReadLabel(worktree, "billing")
	if !ok {
		t.Fatalf("ReadLabel ok = false, want true")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("ReadLabel label is not valid UTF-8: %q", got)
	}
	if got != filler {
		t.Errorf("ReadLabel label = %q (len %d), want the %d-byte filler with the split rune dropped entirely", got, len(got), len(filler))
	}
}

// TestWriteLabelRoundTrips covers the writer's happy path and its atomic-write
// discipline: no leftover temp file, matching dashmeta/request_test.go:96's
// TestWriteRequestNoLeftoverTempFile.
func TestWriteLabelRoundTrips(t *testing.T) {
	worktree := t.TempDir()
	if err := WriteLabel(worktree, "billing", "spike: pty timing"); err != nil {
		t.Fatalf("WriteLabel: %v", err)
	}

	got, ok := ReadLabel(worktree, "billing")
	if !ok || got != "spike: pty timing" {
		t.Fatalf("ReadLabel after WriteLabel = %q (ok=%v), want %q (ok=true)", got, ok, "spike: pty timing")
	}

	dir := filepath.Join(worktree, ".afk", "billing")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temp file %q in %s", e.Name(), dir)
		}
	}
	if len(entries) != 1 || entries[0].Name() != "label" {
		t.Errorf("%s contents = %v, want just \"label\"", dir, entries)
	}
}

// TestWriteLabelOverwrites pins last-write-wins: a second WriteLabel call
// replaces the first rather than appending to or refusing it.
func TestWriteLabelOverwrites(t *testing.T) {
	worktree := t.TempDir()
	if err := WriteLabel(worktree, "billing", "first"); err != nil {
		t.Fatalf("WriteLabel first: %v", err)
	}
	if err := WriteLabel(worktree, "billing", "second"); err != nil {
		t.Fatalf("WriteLabel second: %v", err)
	}

	got, ok := ReadLabel(worktree, "billing")
	if !ok || got != "second" {
		t.Fatalf("ReadLabel = %q (ok=%v), want %q", got, ok, "second")
	}
}

// TestWriteLabelBlankRemovesSidecar pins the "clear the label" gesture: a
// blank write removes the file (reverting display to the session's real
// name) instead of leaving an empty file behind.
func TestWriteLabelBlankRemovesSidecar(t *testing.T) {
	worktree := t.TempDir()
	if err := WriteLabel(worktree, "billing", "spike: pty timing"); err != nil {
		t.Fatalf("WriteLabel: %v", err)
	}
	if err := WriteLabel(worktree, "billing", "   "); err != nil {
		t.Fatalf("WriteLabel blank: %v", err)
	}

	if _, err := os.Stat(filepath.Join(worktree, ".afk", "billing", "label")); !os.IsNotExist(err) {
		t.Fatalf("expected the label file to be removed, stat err = %v", err)
	}
	if got, ok := ReadLabel(worktree, "billing"); ok {
		t.Errorf("ReadLabel after blank write = %q (ok=true), want ok=false", got)
	}
}

// TestWriteLabelBlankOnAbsentFileIsNotAnError covers clearing a label that
// was never set — a session with no label sidecar at all, cleared anyway
// (e.g. a no-op confirm on the editor).
func TestWriteLabelBlankOnAbsentFileIsNotAnError(t *testing.T) {
	worktree := t.TempDir()
	if err := WriteLabel(worktree, "billing", ""); err != nil {
		t.Fatalf("WriteLabel on a session with no prior label: %v", err)
	}
}

// TestWriteLabelNeverInterpolatesIntoAnyCommand guards the plan's explicit
// "never interpolated into a command line" requirement the hardest possible
// way: a label containing shell metacharacters must still be written and read
// back byte-for-byte (modulo the trim/newline handling), never executed.
func TestWriteLabelNeverInterpolatesIntoAnyCommand(t *testing.T) {
	worktree := t.TempDir()
	dangerous := `$(rm -rf /); echo "pwned" && true`
	if err := WriteLabel(worktree, "billing", dangerous); err != nil {
		t.Fatalf("WriteLabel: %v", err)
	}
	got, ok := ReadLabel(worktree, "billing")
	if !ok || got != dangerous {
		t.Fatalf("ReadLabel = %q (ok=%v), want %q unmodified", got, ok, dangerous)
	}
}
