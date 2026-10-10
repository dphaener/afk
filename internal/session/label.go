// label.go implements the mutable display-label sidecar, .afk/{name}/label:
// plain UTF-8 text that overrides what the dashboard SHOWS for a session
// without ever touching the session's on-disk identity (directory name,
// branch, socket path). It follows description.md's sidecar shape
// (create.go's writeDescriptionSidecar) for the write side, but unlike
// description.md a label is mutable after creation — the dashboard's editor
// rewrites it any time the session is selected — so the write goes through
// the same-directory temp-file+rename discipline host.WriteMeta uses
// (internal/host/meta.go:39-60) rather than the create-time-only write
// seedStatus performs once.
//
// Reading and writing both live here, in internal/session, rather than
// splitting the reader into internal/discover: a label file is exactly as
// much "session state" as description.md, and internal/discover already
// reads another package's file (host.json, via internal/host.ReadMeta) for
// the identical reason — one definition of the sidecar's shape, read by
// whichever package needs it.
package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxLabelBytes bounds how much of a label file ReadLabel will ever read. It
// is a defensive cap against a huge or corrupt file, not a UX limit — the
// dashboard's own editor constrains what a user can type far below this
// (textinput.CharLimit on the label prompt, internal/ui). A file beyond the
// cap is truncated rather than rejected: the bytes that ARE there are still
// perfectly valid label text, just longer than any real label needs to be.
const maxLabelBytes = 4096

// labelPath returns the sidecar's location: .afk/{name}/label under
// worktree, alongside status.json, host.json and description.md.
func labelPath(worktree, name string) string {
	return filepath.Join(worktree, ".afk", name, "label")
}

// ReadLabel reads the label sidecar for {name} under worktree. A missing,
// empty (after trimming), or unreadable file all report ("", false) —
// deliberately indistinguishable, matching this package's standing rule
// (writeDescriptionSidecar, host.ReadMeta) that a sidecar file's absence is
// never an error and never a staleness signal: there is nothing a caller
// should do differently in any of those cases beyond falling back to the
// session's real name.
//
// Embedded newlines (only reachable via a hand-edited file — the dashboard's
// textinput editor cannot produce one) are folded to spaces: a label is
// rendered as part of a single display line (the list row, the detail
// header), and a raw newline reaching either would corrupt those callers'
// fixed line-count contracts. A hand-edited file can also carry a lone `\r`
// (no paired `\n` — e.g. a CR-only line ending), ANSI/C0 control bytes
// (which a raw label would hand straight to the terminal rendering it), or
// simply be too long: sanitizeLabel and truncateToRuneBoundary handle those,
// so nothing past this function ever has to re-derive the same rules.
func ReadLabel(worktree, name string) (string, bool) {
	f, err := os.Open(labelPath(worktree, name))
	if err != nil {
		return "", false
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxLabelBytes))
	if err != nil {
		return "", false
	}
	data = truncateToRuneBoundary(data, maxLabelBytes)
	label := strings.TrimSpace(string(data))
	if label == "" {
		return "", false
	}
	label = sanitizeLabel(label)
	if label == "" {
		return "", false
	}
	return label, true
}

// truncateToRuneBoundary drops trailing bytes of data back to the start of
// the last complete UTF-8 rune, but only when data is exactly limit bytes
// long — the shape io.LimitReader(f, limit) produces when the underlying
// file is longer than limit and the read was cut off mid-rune. A shorter
// read is the whole file and is never touched here. No valid UTF-8 rune is
// wider than utf8.UTFMax bytes, so at most that many trailing bytes are ever
// inspected.
func truncateToRuneBoundary(data []byte, limit int) []byte {
	if len(data) != limit {
		return data
	}
	for i := 0; i < utf8.UTFMax && len(data) > 0; i++ {
		r, size := utf8.DecodeLastRune(data)
		if r != utf8.RuneError || size != 1 {
			break
		}
		data = data[:len(data)-1]
	}
	return data
}

// sanitizeLabel makes label safe to hand to a terminal renderer as literal
// text. It folds every newline variant — "\r\n", a bare "\n", and a lone
// "\r" (a CR-only line ending, reachable only via a hand-edited file since
// the dashboard's own editor cannot produce any of the three) — to a single
// space, then drops every remaining C0 control byte (0x00-0x1F) and DEL
// (0x7F). Dropping C0 also removes ESC (0x1B) specifically because that is
// what would otherwise let a hand-edited label smuggle an ANSI escape
// sequence into the renderer: without the ESC byte, the rest of the
// sequence (e.g. "[31m") is inert, ordinary printable text instead of a
// control code, so this needs no CSI-sequence parser to be effective.
func sanitizeLabel(label string) string {
	label = strings.ReplaceAll(label, "\r\n", " ")
	label = strings.ReplaceAll(label, "\n", " ")
	label = strings.ReplaceAll(label, "\r", " ")

	var b strings.Builder
	b.Grow(len(label))
	for _, r := range label {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// WriteLabel persists label as the display-label sidecar for {name} under
// worktree via a same-directory temp-file+rename (mirrors host.WriteMeta,
// internal/host/meta.go:39-60), so a reader never observes a torn file.
//
// A blank (after trimming) label removes the sidecar instead of writing an
// empty file: that is what lets the dashboard's editor "clear" a label back
// to showing the session's real name, and os.Remove on an already-absent
// file is treated as success since the end state (no label file) is
// identical either way.
func WriteLabel(worktree, name, label string) error {
	path := labelPath(worktree, name)
	dir := filepath.Dir(path)

	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing label: %w", err)
		}
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	text := trimmed + "\n"
	tmp := filepath.Join(dir, fmt.Sprintf(".label.tmp.%d", os.Getpid()))
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("writing temp label: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming label into place: %w", err)
	}
	return nil
}
