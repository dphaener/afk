package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// viewer covers both models in this package that return a tea.View: the
// dashboard Model (views.go) and attachModel (attachpane.go). The parameter is
// this interface rather than Model because hostpane_test.go renders an
// attachModel, and a Model-only helper would have needed a second conversion
// for it. switcher.View() still returns a plain string and is deliberately not
// in this set.
type viewer interface {
	View() tea.View
}

// render returns a model's rendered frame as a plain string, and is the only
// place in the package that converts a tea.View back to one.
//
// tea.View is a struct (bubbletea/v2@v2.0.9 tea.go:84) whose frame is reachable
// only through the exported Content field — its sole method, SetContent
// (tea.go:279), writes, and there is no String()/Render() to fall back on.
// Ninety-odd assertions here depended on v1's string return; one funnel keeps
// the next change to that accessor a one-line edit.
//
// The ANSI strip is what makes the string these assertions get the SAME string
// they got under v1, and it is not a relaxation. lipgloss v1 rendered through a
// renderer bound to os.Stdout, so under `go test` it detected a non-TTY, fell
// back to the Ascii profile, and emitted no escape sequences at all — which is
// why assertions here count runes and match substrings against raw frames.
// lipgloss v2 deleted renderers: Style.Render always emits full-fidelity ANSI
// and Bubble Tea downsamples at the writer instead, so the same frame now
// carries dozens of escapes per line, and an underlined heading is split
// escape-per-rune (style.go's useSpaceStyler loop), which defeats a plain
// strings.Contains. Stripping is this package's stand-in for the profile writer
// the program has and a test does not.
//
// Reads of the view's other fields are not string conversions and stay at their
// call sites: the ctrl-g tests assert on View().MouseMode, where v2 moved the
// mouse-mode declaration that used to be a returned tea.Cmd.
func render(v viewer) string { return xansi.Strip(v.View().Content) }

// selectionBg is the SGR parameter run the dark scheme's palette.SelectionBg
// renders to, which is what assertHighlightSpansRow looks for. It is spelled
// out rather than derived from theme so that a palette edit dropping the
// background fails these assertions instead of quietly making them vacuous.
const selectionBg = "48;2;43;47;61"

// assertHighlightSpansRow fails unless the selection background is in force at
// every printable cell of line, and line is exactly width cells wide. Shared by
// the three list delegates' tests (session list, project switcher, command
// palette), which all draw their highlight bar the same way and lost it the
// same way — see spanStyle (styles.go).
func assertHighlightSpansRow(t *testing.T, line string, width int) {
	t.Helper()
	if !strings.Contains(line, selectionBg) {
		t.Fatalf("test premise: no selection background in %q", line)
	}
	if at, ok := bgActiveAtEveryCell(line, selectionBg); !ok {
		t.Errorf("selection background is not in force at byte %d; the highlight bar stops short:\n%q", at, line)
	}
	if got := lipgloss.Width(line); got != width {
		t.Errorf("row width = %d, want %d", got, width)
	}
}

// bgActiveAtEveryCell walks a rendered line as a terminal would and reports
// whether the background SGR param `bg` is in force at every printable cell,
// returning the byte offset of the first cell where it is not.
//
// It is a deliberately tiny SGR interpreter — three transitions — rather than a
// substring count, because the invariant being asserted IS a per-cell one: "the
// highlight bar covers the row". A count of re-asserted sequences would go
// green for a row that re-asserted the background in the wrong places, and the
// defect it guards (a bar that stops at cell three) is exactly a row where most
// cells are unpainted while the escape codes are all present.
func bgActiveAtEveryCell(line, bg string) (int, bool) {
	active := false
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			end := strings.IndexByte(line[i:], 'm')
			if end < 0 {
				break
			}
			seq := line[i : i+end+1]
			switch {
			case seq == sgrResetShort || seq == sgrResetLong:
				active = false
			case strings.Contains(seq, bg):
				active = true
			}
			i += end + 1
			continue
		}
		if !active {
			return i, false
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		i += size
	}
	return -1, true
}
