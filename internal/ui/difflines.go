// difflines.go holds the pure, git-diff-line-level functions the Diff tab's
// rendering pipeline is built from: tab expansion, control-character
// stripping, per-line classification, header-path extraction, line capping,
// and the composed per-line renderer. All six are pure (no I/O, no styling
// side effects beyond returning a styled string) so the whole matrix is
// table-testable without a terminal or a git checkout — see
// difflines_test.go.
package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// diffLineKind classifies one raw line of `git diff` output for rendering
// purposes. diffLineContext is the zero value deliberately: an unrecognized
// or plain-context line (including "\ No newline at end of file") should
// fall through to the least surprising treatment rather than any specific
// styled kind.
type diffLineKind int

const (
	diffLineContext diffLineKind = iota
	diffLineFileHeader
	diffLineExtHeader
	diffLineMarker
	diffLineHunkHeader
	diffLineAdded
	diffLineRemoved
)

// diffLineCap is the maximum number of retained diff lines (Objectives #3).
// capDiffLines enforces it; diffTruncationMarker names it on screen.
const diffLineCap = 2000

// diffTabWidth is the tab stop width used by expandTabs when rendering diff
// lines for the detail pane. 4, not the conventional 8: the detail pane is
// only ~76 cells wide on a 120-column terminal, and 8-wide stops push nested
// Go code off the right edge before any of it is visible.
const diffTabWidth = 4

// diffTruncationMarker is the line rendered in place of the lines
// capDiffLines drops, once the cap has bitten. It names both the cap
// (diffLineCap) and the tool the user can fall back to for the untruncated
// output ("git diff"), so a truncated view never reads as a complete one.
var diffTruncationMarker = fmt.Sprintf("… output truncated at %d lines — run `git diff` for the rest", diffLineCap)

// expandTabs expands every tab in s to the next multiple of tabWidth,
// tab-stop aware rather than a flat replace, so columns still line up when a
// tab follows content mid-line (`"a\tb"` and `"\tb"` land on different
// numbers of spaces at tabWidth 4: 3 and 4 respectively).
//
// s carries the 1-char `+`/`-`/space diff-line prefix, so a tab at file
// column 0 sits at string column 1 and expands relative to that offset, not
// to true column 0. That keeps every diff line aligned with its neighbors
// (they all carry the same 1-char prefix) at the cost of the whole block
// being one column off from the file's real indentation. Chosen over
// stripping the prefix first and re-adding it, which would need a second
// pass; the offset is documented here rather than corrected because
// relative alignment across diff lines is what rendering needs, not
// absolute fidelity to the source file's columns.
func expandTabs(s string, tabWidth int) string {
	var b strings.Builder
	b.Grow(len(s))
	col := 0
	for _, r := range s {
		if r == '\t' {
			spaces := tabWidth - (col % tabWidth)
			for i := 0; i < spaces; i++ {
				b.WriteByte(' ')
			}
			col += spaces
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

// stripControls removes C0 control characters (0x00-0x1F) from s. It exists
// because a bare \r surviving from a CRLF-terminated file returns the
// cursor to column 0 when rendered and overwrites the detail box's left
// border.
//
// Must run AFTER expandTabs, never before: \t (0x09) is itself a C0
// control, so stripping first would delete every tab before expansion gets
// a chance to run, which is the exact defect this function pair exists to
// prevent.
//
// DEL (0x7F) and the C1 controls (U+0080-U+009F) deliberately pass through
// unstripped: xansi.StringWidth measures them as zero-width, so they cannot
// overrun a rendered line's box, and handling them is out of scope for this
// task.
func stripControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r <= 0x1F {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// classifyDiffLine maps one raw `git diff` output line to the diffLineKind
// it should render as.
//
// The `---`/`+++` file-marker pair is deliberately distinguished from
// added/removed content: `parseFullDiff` (diff.go:159-160) documents the
// same trap — treating them as ordinary +/- lines paints every file header
// green or red. They classify as diffLineMarker only when followed by
// " a/", " b/", " /dev/null", " \"a/", or " \"b/" — the unqualified prefix
// alone is not enough, or a removed SQL comment line (`-- x`, which git
// diff renders as `--- x`) would be misclassified as a file marker. The
// quoted forms cover a non-ASCII filename, which git C-quotes by default
// under core.quotePath (e.g. `--- "a/café.md"`).
//
// classifyDiffLine assumes W1.T2's hardened `--default-prefix` argv, which
// guarantees the a/ and b/ prefixes this rule depends on are present.
//
// `diff --git a/foo bar.go b/foo bar.go` (spaces in an unquoted path) is
// ambiguous — there is no way to tell where the a/ path ends and the b/
// path begins without knowing the filename in advance — and is deliberately
// punted: no special-case here, it falls through like any other file
// header line.
func classifyDiffLine(line string) diffLineKind {
	// Order matters: longer, more specific prefixes must be tested before
	// shorter, more general ones. In particular "+++"/"---" must be tested
	// before the bare "+"/"-" prefixes they share — parseFullDiff
	// (diff.go:159-160) documents the same trap: treating a file-marker line
	// as ordinary added/removed content paints every file header green or
	// red.
	switch {
	case strings.HasPrefix(line, "diff --git "):
		return diffLineFileHeader
	case strings.HasPrefix(line, "index "),
		strings.HasPrefix(line, "new file mode "),
		strings.HasPrefix(line, "deleted file mode "),
		strings.HasPrefix(line, "similarity index "),
		strings.HasPrefix(line, "rename from "),
		strings.HasPrefix(line, "rename to "),
		strings.HasPrefix(line, "old mode "),
		strings.HasPrefix(line, "new mode "),
		strings.HasPrefix(line, "Binary files "):
		return diffLineExtHeader
	case isDiffMarkerLine(line):
		return diffLineMarker
	case strings.HasPrefix(line, "@@"):
		return diffLineHunkHeader
	case strings.HasPrefix(line, "+"):
		return diffLineAdded
	case strings.HasPrefix(line, "-"):
		return diffLineRemoved
	default:
		return diffLineContext
	}
}

// isDiffMarkerLine reports whether line is a "+++"/"---" file-marker line
// (as opposed to an ordinary added/removed content line that happens to
// start with the same one or three characters, e.g. a removed SQL comment
// "-- x" which git renders as "--- x"). The unqualified "+++"/"---" prefix
// alone is not enough to tell the two apart; requiring one of these
// well-known suffixes after it is. The quoted forms cover a non-ASCII
// filename, which git C-quotes by default under core.quotePath.
func isDiffMarkerLine(line string) bool {
	var rest string
	switch {
	case strings.HasPrefix(line, "+++"):
		rest = line[3:]
	case strings.HasPrefix(line, "---"):
		rest = line[3:]
	default:
		return false
	}
	for _, suffix := range []string{" a/", " b/", " /dev/null", ` "a/`, ` "b/`} {
		if strings.HasPrefix(rest, suffix) {
			return true
		}
	}
	return false
}

// diffHeaderPath extracts the display path from a `diff --git a/P b/P`
// line: P for an unchanged path, "old → new" for a rename (a/old b/new).
// Lines that do not parse as a `diff --git` header are returned unchanged,
// so a caller can pass any line through without a separate classification
// check first.
func diffHeaderPath(line string) string {
	const prefix = "diff --git "
	if !strings.HasPrefix(line, prefix) {
		return line
	}
	// A path with an unquoted space is ambiguous — there is no way to tell
	// where the a/ path ends and the b/ path begins without knowing the
	// filename in advance. strings.Fields splits on every space, so an
	// unambiguous "a/P b/P" line yields exactly two fields and an ambiguous
	// one yields more; the latter is punted, not guessed at.
	fields := strings.Fields(line[len(prefix):])
	if len(fields) != 2 {
		return line
	}
	a := strings.TrimPrefix(fields[0], "a/")
	b := strings.TrimPrefix(fields[1], "b/")
	if a == b {
		return a
	}
	return a + " → " + b
}

// capDiffLines returns at most max lines from lines, plus whether it had to
// drop any. Fenceposts matter here: exactly max lines is NOT truncated
// (truncated == false), and it never returns more than max lines even when
// max+1 or more were given.
func capDiffLines(lines []string, max int) (kept []string, truncated bool) {
	if len(lines) <= max {
		return lines, false
	}
	return lines[:max], true
}

// renderDiffLine composes the full per-line rendering pipeline: expandTabs,
// then stripControls, then classify, then restyle (rewriting a file header
// to its display path and applying the classified kind's theme style), then
// truncate to width last, so ANSI-aware truncation sees the fully styled
// string and measures one that no longer contains any tabs.
//
// Tested here for its contract; implemented in W3.T1, which also adds the
// benchmark for this path (see that task's plan entry for why the
// benchmark undercounts detailBody's real per-frame cost).
func renderDiffLine(line string, width int) string {
	// expandTabs before stripControls: \t is itself a C0 control, so the
	// reverse order would delete every tab before expansion runs, and
	// classify/restyle before truncate: ANSI-aware truncation must see the
	// fully styled string and one that no longer contains any tabs.
	expanded := expandTabs(line, diffTabWidth)
	stripped := stripControls(expanded)
	kind := classifyDiffLine(stripped)
	display := stripped
	if kind == diffLineFileHeader {
		display = diffHeaderPath(stripped)
	}
	styled := diffLineStyle(kind).Render(display)
	return truncate(styled, width)
}

// diffLineStyle maps a classified line kind to its theme style. Kinds with
// no dedicated visual treatment (context, marker, extended header) render
// unstyled — they exist for classification correctness (see
// classifyDiffLine), not for their own color.
func diffLineStyle(kind diffLineKind) lipgloss.Style {
	switch kind {
	case diffLineFileHeader:
		return theme.diffFileHeader
	case diffLineHunkHeader:
		return theme.diffHunkHeader
	case diffLineAdded:
		return theme.diffAdded
	case diffLineRemoved:
		return theme.diffRemoved
	default:
		return lipgloss.NewStyle()
	}
}
