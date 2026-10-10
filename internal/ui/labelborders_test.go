package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// TestLabelBordersKeepsGeometry is the contract every box-width test in this
// package rests on: labelling a box changes the glyphs on its two edge rows
// and nothing else — same width to the cell, same height, same interior. The
// long-title row proves the label is cut to fit rather than widening the edge.
func TestLabelBordersKeepsGeometry(t *testing.T) {
	body := "alpha\nbeta\ngamma"
	box := sizedBox(theme.detailBox, 20, 5).Render(body)

	for _, tc := range []struct{ title, hint string }{
		{"Sessions", ""},
		{"", "c: clean"},
		{"Sessions", "enter: open · c: clean"},
		{strings.Repeat("x", 60), strings.Repeat("y", 60)},
	} {
		got := labelBorders(box, theme.detailBox, tc.title, tc.hint)
		want, gotLines := strings.Split(box, "\n"), strings.Split(got, "\n")
		if len(gotLines) != len(want) {
			t.Fatalf("labelBorders changed the height: %d → %d", len(want), len(gotLines))
		}
		for i := range want {
			if w, g := lipgloss.Width(want[i]), lipgloss.Width(gotLines[i]); w != g {
				t.Errorf("line %d width %d → %d for title=%q hint=%q:\n%s", i, w, g, tc.title, tc.hint, got)
			}
			if i > 0 && i < len(want)-1 && gotLines[i] != want[i] {
				t.Errorf("interior line %d changed for title=%q hint=%q", i, tc.title, tc.hint)
			}
		}
		// paintEdge's label room: the edge minus two corners, a fill glyph and a
		// space on each side of the label.
		room := lipgloss.Width(want[0]) - 6
		plain := xansi.Strip(got)
		if tc.title != "" && !strings.Contains(plain, "╭─ "+truncate(tc.title, room)+" ") {
			t.Errorf("top edge lacks the title: %q", strings.Split(plain, "\n")[0])
		}
		if tc.hint != "" && !strings.Contains(plain, " "+truncate(tc.hint, room)+" ─╯") {
			t.Errorf("bottom edge lacks the hint: %q", strings.Split(plain, "\n")[len(want)-1])
		}
	}
}

// TestLabelBordersSkipsAClippedBottomEdge covers the MaxHeight hazard the
// helper's doc names: on an overflowing body lipgloss slices the bottom edge
// off, so the last line is content and must be left alone rather than
// overwritten with a border it never had.
func TestLabelBordersSkipsAClippedBottomEdge(t *testing.T) {
	body := strings.Repeat("row\n", 10)
	box := sizedBox(theme.detailBox, 20, 3).MaxHeight(5).Render(body)
	lines := strings.Split(xansi.Strip(box), "\n")
	if strings.HasPrefix(lines[len(lines)-1], "╰") {
		t.Fatalf("fixture broken: MaxHeight left the bottom edge in place:\n%s", box)
	}

	got := labelBorders(box, theme.detailBox, "T", "hint")
	gotLines := strings.Split(got, "\n")
	if gotLines[len(gotLines)-1] != strings.Split(box, "\n")[len(lines)-1] {
		t.Fatalf("the clipped last line was rewritten as a border:\n%s", got)
	}
	if !strings.Contains(xansi.Strip(gotLines[0]), "╭─ T ") {
		t.Fatalf("the top edge should still carry the title:\n%s", got)
	}
}
