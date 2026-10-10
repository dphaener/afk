package ui

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// TestExpandTabsDiffLine pins tab-stop-aware expansion, not a flat replace.
// The last two rows are load-bearing: they are the exact two examples the
// plan cites to prove expansion depends on the tab's starting column, not
// just its presence — a flat "\t" -> 4-spaces replace would pass the second
// row and fail the first.
func TestExpandTabsDiffLine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		s        string
		tabWidth int
		want     string
	}{
		{"tab after one char snaps to the next stop, not a full stop", "a\tb", 4, "a   b"},
		{"tab at string start consumes a full stop", "\tb", 4, "    b"},
		{"tab after the 1-char diff prefix behaves like tab-after-one-char", "+\tfoo", 4, "+   foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandTabs(tc.s, tc.tabWidth); got != tc.want {
				t.Errorf("expandTabs(%q, %d) = %q, want %q", tc.s, tc.tabWidth, got, tc.want)
			}
		})
	}
}

// TestDiffLineExpandBeforeStrip proves the composition order this package's
// plan calls load-bearing: expandTabs must run before stripControls. \t is
// itself a C0 control, so composing in the other order would delete every
// tab before it ever became a space. Running expandTabs first turns the tab
// into ordinary space characters, which stripControls then leaves alone.
func TestDiffLineExpandBeforeStrip(t *testing.T) {
	const line = "+\tfoo"
	want := "+   foo"
	if got := stripControls(expandTabs(line, diffTabWidth)); got != want {
		t.Fatalf("stripControls(expandTabs(%q)) = %q, want %q (tab must survive as spaces)", line, got, want)
	}
}

// TestStripControlsDiffLine pins C0 removal. The CRLF row is the reason this
// function exists at all: a bare \r left over from a CRLF file returns the
// render cursor to column 0 and overwrites the detail box's left border. The
// DEL/C1 row pins the documented exception — those pass through untouched
// because xansi.StringWidth measures them as zero-width and cannot overrun.
func TestStripControlsDiffLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
		want string
	}{
		{"CRLF line loses its bare trailing CR", "foo\r", "foo"},
		{"C0 controls are removed wherever they appear", "a\x00b\x01c", "abc"},
		{"DEL and C1 controls pass through unstripped", "a\x7fb", "a\x7fb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripControls(tc.s); got != tc.want {
				t.Errorf("stripControls(%q) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}

// TestClassifyDiffLineKind pins the classifier's full matrix. The marker
// rows are the ones review found real defects in: `+++ b/file` and
// `--- a/file` must classify as diffLineMarker, never as added/removed, or
// every file header paints green or red (the exact trap
// parseFullDiff(diff.go:159-160) already documents). The "--- x" row is the
// other direction of that same trap: without requiring " a/"/" b/"/
// " /dev/null" after the marker, a removed SQL comment line ("-- x", which
// git renders "--- x") would be misclassified as a file marker instead of a
// removed line.
func TestClassifyDiffLineKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want diffLineKind
	}{
		{"file header", "diff --git a/x b/x", diffLineFileHeader},
		{"file header with an ambiguous unquoted space in the path still classifies as a header", "diff --git a/foo bar.go b/foo bar.go", diffLineFileHeader},
		{"index extended header", "index 83db48f..bf269c9 100644", diffLineExtHeader},
		{"new file mode extended header", "new file mode 100644", diffLineExtHeader},
		{"deleted file mode extended header", "deleted file mode 100644", diffLineExtHeader},
		{"similarity index extended header", "similarity index 100%", diffLineExtHeader},
		{"rename from extended header", "rename from old.go", diffLineExtHeader},
		{"rename to extended header", "rename to new.go", diffLineExtHeader},
		{"old mode extended header", "old mode 100644", diffLineExtHeader},
		{"new mode extended header", "new mode 100755", diffLineExtHeader},
		{"binary files extended header", "Binary files a/x and b/x differ", diffLineExtHeader},
		{"+++ file marker must not classify as added", "+++ b/file", diffLineMarker},
		{"--- file marker must not classify as removed", "--- a/file", diffLineMarker},
		{"--- /dev/null marker for a deleted file", "--- /dev/null", diffLineMarker},
		{"+++ marker with a C-quoted non-ASCII path", `+++ "b/café.md"`, diffLineMarker},
		{"a removed SQL comment must NOT be misread as a file marker", "--- x", diffLineRemoved},
		{"hunk header classifies despite a trailing code fragment", "@@ -1,4 +1,6 @@ func Foo()", diffLineHunkHeader},
		{"added line", "+some code", diffLineAdded},
		{"removed line", "-some code", diffLineRemoved},
		{"context line", " unchanged", diffLineContext},
		{"no-newline marker is context", `\ No newline at end of file`, diffLineContext},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDiffLine(tc.line); got != tc.want {
				t.Errorf("classifyDiffLine(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestDiffLineHeaderPath pins diffHeaderPath's contract: unchanged path,
// rename display form, and the pass-through fallback for anything that does
// not parse as a `diff --git` header — including the deliberately-punted
// ambiguous unquoted-space-in-path case, which gets no special-case parsing
// and so falls through unchanged rather than guessing.
func TestDiffLineHeaderPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{"unchanged path", "diff --git a/foo.go b/foo.go", "foo.go"},
		{"rename renders as old → new", "diff --git a/old.go b/new.go", "old.go → new.go"},
		{"non-header line passes through unchanged", "not a diff header", "not a diff header"},
		{"ambiguous unquoted space in path is punted, not guessed at", "diff --git a/foo bar.go b/foo bar.go", "diff --git a/foo bar.go b/foo bar.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := diffHeaderPath(tc.line); got != tc.want {
				t.Errorf("diffHeaderPath(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// TestCapDiffLinesFencepost pins the fencepost behavior around the cap:
// exactly max lines is NOT truncated, and one line over the cap keeps
// exactly max lines and reports truncation. Getting either fencepost wrong
// either truncates one line too early (max lines wrongly flagged truncated)
// or lets the cap slip by one (max+1 lines kept).
func TestCapDiffLinesFencepost(t *testing.T) {
	mk := func(n int) []string {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = "line"
		}
		return lines
	}

	for _, tc := range []struct {
		name          string
		lines         []string
		max           int
		wantKept      int
		wantTruncated bool
	}{
		{"nil input", nil, 5, 0, false},
		{"empty input", []string{}, 5, 0, false},
		{"exactly max is not truncated", mk(5), 5, 5, false},
		{"max+1 keeps exactly max and reports truncation", mk(6), 5, 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, truncated := capDiffLines(tc.lines, tc.max)
			if len(kept) != tc.wantKept || truncated != tc.wantTruncated {
				t.Errorf("capDiffLines(%d lines, max %d) = (%d lines, %v), want (%d lines, %v)",
					len(tc.lines), tc.max, len(kept), truncated, tc.wantKept, tc.wantTruncated)
			}
		})
	}
}

// TestDiffLineTruncationMarker pins that the on-screen truncation marker
// names both the cap and the fallback tool, per the plan's requirement — a
// truncated view must never read as if it were the complete diff, and a
// marker that doesn't name the cap or `git diff` gives the reader no way to
// find the rest.
func TestDiffLineTruncationMarker(t *testing.T) {
	if !strings.Contains(diffTruncationMarker, "2000") {
		t.Errorf("diffTruncationMarker = %q, want it to name the cap (2000)", diffTruncationMarker)
	}
	if !strings.Contains(diffTruncationMarker, "git diff") {
		t.Errorf("diffTruncationMarker = %q, want it to name `git diff`", diffTruncationMarker)
	}
}

// TestRenderDiffLineWidth pins renderDiffLine's width contract using the
// xansi.Strip + xansi.StringWidth technique TestScrollReadout
// (views_test.go) already uses to measure a styled string. A line longer
// than width must come back at exactly width visible cells; a shorter line
// must NOT be padded out to width, since the caller (the viewport body) is
// what decides whether padding happens, not this function.
func TestRenderDiffLineWidth(t *testing.T) {
	t.Run("a line longer than width is truncated to exactly width visible cells", func(t *testing.T) {
		long := "+" + strings.Repeat("x", 200)
		got := renderDiffLine(long, 20)
		if w := xansi.StringWidth(xansi.Strip(got)); w != 20 {
			t.Fatalf("renderDiffLine(long, 20) width = %d, want 20", w)
		}
	})

	t.Run("a line shorter than width is not padded", func(t *testing.T) {
		short := "+short"
		got := renderDiffLine(short, 80)
		if w := xansi.StringWidth(xansi.Strip(got)); w >= 80 {
			t.Fatalf("renderDiffLine(short, 80) width = %d, want less than 80 (not padded)", w)
		}
	})
}

// TestRenderDiffLineColor is the ONLY place colour is verified — goldens
// strip ANSI by design (see the plan's Verification Strategy). Each row
// compares renderDiffLine's output against the theme style's own Render of
// the same content, which works regardless of the test renderer's colour
// profile (lipgloss v2 emits full-fidelity ANSI under test, per
// render_test.go:53's comment on the same point).
//
// The hunk-header row does NOT discriminate: theme.diffHunkHeader is
// p.Muted, byte-identical to theme.dim (see W1.T1's learnings), so this row
// cannot tell "styled as hunk header" from "styled as anything dim". It is
// included anyway for coverage, but the added/removed/file-header rows
// below are the real check.
func TestRenderDiffLineColor(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{"added line uses diffAdded", "+x", theme.diffAdded.Render("+x")},
		{"removed line uses diffRemoved", "-x", theme.diffRemoved.Render("-x")},
		{"file header uses diffFileHeader on the extracted display path", "diff --git a/foo.go b/foo.go", theme.diffFileHeader.Render("foo.go")},
		{"hunk header uses diffHunkHeader (non-discriminating from dim, see comment above)", "@@ -1,4 +1,6 @@ func Foo()", theme.diffHunkHeader.Render("@@ -1,4 +1,6 @@ func Foo()")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderDiffLine(tc.line, 80); got != tc.want {
				t.Errorf("renderDiffLine(%q, 80) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// TestRenderDiffLineTabIndentedAddedLine pins the exact composed output for
// a tab-indented added line — the case the strip-before-expand bug would
// have broken — against a pinned expected string AND a pinned expected
// xansi.StringWidth, per the plan's instruction that a loose "looks right"
// assertion here is exactly what would let that bug back in.
func TestRenderDiffLineTabIndentedAddedLine(t *testing.T) {
	const line = "+\tfoo"
	want := theme.diffAdded.Render("+   foo")
	got := renderDiffLine(line, 80)
	if got != want {
		t.Fatalf("renderDiffLine(%q, 80) = %q, want %q", line, got, want)
	}
	const wantWidth = 7 // "+   foo" -> '+', 3 spaces, "foo" = 7 cells
	if w := xansi.StringWidth(xansi.Strip(got)); w != wantWidth {
		t.Fatalf("renderDiffLine(%q, 80) width = %d, want %d", line, w, wantWidth)
	}
}

// benchmarkDiffLines builds diffLineCap representative git-diff lines: a
// file header and a hunk header up front, then a repeating cycle of
// tab-indented added/removed/context lines, so the benchmark exercises every
// pipeline stage (expandTabs, stripControls, classify, restyle, truncate)
// rather than just one line kind.
func benchmarkDiffLines() []string {
	lines := make([]string, 0, diffLineCap)
	lines = append(lines, "diff --git a/internal/ui/model.go b/internal/ui/model.go", "@@ -10,7 +10,7 @@ func Update()")
	cycle := []string{
		"+\tfoo := bar(baz)",
		"-\tfoo := bar(baz, qux)",
		" \tunchanged(context)",
	}
	for len(lines) < diffLineCap {
		lines = append(lines, cycle[len(lines)%len(cycle)])
	}
	return lines[:diffLineCap]
}

// BenchmarkRenderDiffLines measures renderDiffLine's per-frame cost at a
// realistic detail-pane width (76 cells — this repo's ~120-column golden
// frame's detail box, per diffTabWidth's doc comment). It measures
// renderDiffLine only and therefore undercounts the real per-frame cost:
// detailBody's lipgloss.Wrap over the whole styled body (views.go:1878) is
// not included here.
func BenchmarkRenderDiffLines(b *testing.B) {
	lines := benchmarkDiffLines()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, line := range lines {
			renderDiffLine(line, 76)
		}
	}
}
