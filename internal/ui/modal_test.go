package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/palette"
)

// colouredFrame is a width×height frame of 'f' cells in white on black, so
// any cell left at those colours after compositing was not dimmed.
func colouredFrame(width, height int) string {
	st := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#000000"))
	row := st.Render(strings.Repeat("f", width))
	rows := make([]string, height)
	for i := range rows {
		rows[i] = row
	}
	return strings.Join(rows, "\n")
}

// TestModalDimsFrameUnderDialog pins §3.9 at the one place every overlay goes
// through. Load-bearing rows: "true colour" — a frame cell outside the box is
// blended 70% toward tide's bg while the box's own border keeps its full
// accent, so the overlay is what reads; "ANSI" — at 16 colours the frame is
// faint and keeps its colours; "toast does not dim" — composeToast is news,
// not an overlay, and must leave the frame at full strength.
func TestModalDimsFrameUnderDialog(t *testing.T) {
	restoreTide(t)
	useTheme(palette.Tide)
	const width, height = 40, 10
	box := renderModalBox("Confirm", "", "hello", 10, 1)
	boxX := (width - lipgloss.Width(box)) / 2
	boxY := (height - lipgloss.Height(box)) / 2

	tests := []struct {
		name      string
		compose   func(frame string) string
		wantFg    string
		wantBg    string
		wantFaint bool
	}{
		{
			name: "true colour",
			compose: func(f string) string {
				return composeModal(f, box, width, height, colorprofile.TrueColor)
			},
			wantFg: "#545964", wantBg: "#070d18",
		},
		{
			name: "ANSI",
			compose: func(f string) string {
				return composeModal(f, box, width, height, colorprofile.ANSI)
			},
			wantFg: "#ffffff", wantBg: "#000000", wantFaint: true,
		},
		{
			name: "toast does not dim",
			compose: func(f string) string {
				return composeToast(f, renderToast(notifyInfo, "saved", width), width)
			},
			wantFg: "#ffffff", wantBg: "#000000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.compose(colouredFrame(width, height))
			// (0, height-1): bottom-left, clear of both the centred box and
			// the toast in the top-right corner.
			c := cellOf(t, got, 0, height-1)
			if g := hexOf(c.Style.Fg); g != tt.wantFg {
				t.Errorf("frame fg = %s, want %s", g, tt.wantFg)
			}
			if g := hexOf(c.Style.Bg); g != tt.wantBg {
				t.Errorf("frame bg = %s, want %s", g, tt.wantBg)
			}
			if faint := c.Style.Attrs&uv.AttrFaint != 0; faint != tt.wantFaint {
				t.Errorf("frame faint = %v, want %v", faint, tt.wantFaint)
			}
		})
	}

	t.Run("box keeps its colours", func(t *testing.T) {
		got := composeModal(colouredFrame(width, height), box, width, height, colorprofile.TrueColor)
		c := cellOf(t, got, boxX, boxY) // the box's top-left corner glyph
		if g := hexOf(c.Style.Fg); g != palette.Tide.Accent {
			t.Errorf("box border fg = %s, want tide accent %s", g, palette.Tide.Accent)
		}
		if c.Style.Attrs&uv.AttrFaint != 0 {
			t.Error("box border is faint; only the frame under it should dim")
		}
	})
}

// TestModalDimKeepsFrameText pins that dimming changes colours only: with a
// dialog open, the ANSI-stripped screen is exactly what it was before dimming
// existed, trailing blanks trimmed the same way. The goldens are ANSI-stripped
// frames with dialogs open, so this is the property that keeps them still.
func TestModalDimKeepsFrameText(t *testing.T) {
	restoreTide(t)
	useTheme(palette.Tide)
	const width, height = 30, 6
	// A frame shaped like the dashboard's: styled words, then plain padding to
	// the full width (JoinVertical pads short lines with bare spaces).
	word := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("word")
	lines := make([]string, height)
	for i := range lines {
		lines[i] = word + strings.Repeat(" ", width-4)
	}
	frame := strings.Join(lines, "\n")
	box := renderModalBox("T", "", "x", 6, 1)
	undimmed := lipgloss.NewCompositor(
		lipgloss.NewLayer(frame),
		lipgloss.NewLayer(box).
			X(max(0, (width-lipgloss.Width(box))/2)).
			Y(max(0, (height-lipgloss.Height(box))/2)).Z(1),
	).Render()
	got := composeModal(frame, box, width, height, colorprofile.TrueColor)
	if xansi.Strip(got) != xansi.Strip(undimmed) {
		t.Errorf("dimming changed the text:\n got %q\nwant %q", xansi.Strip(got), xansi.Strip(undimmed))
	}
}

// TestDialogOverOverlayDimsOnce pins composeModalOver, the path View takes
// for a dialog opened over another overlay (a confirm over the switcher).
// Load-bearing rows: "screen under both" — a frame cell outside both boxes is
// dimmed exactly once (the same colour a lone dialog leaves it), not pushed a
// second 70% toward bg; "overlay under the dialog" — the first overlay's own
// border, now under the dialog, is dimmed once like the screen; "dialog on
// top" keeps its full accent.
func TestDialogOverOverlayDimsOnce(t *testing.T) {
	restoreTide(t)
	useTheme(palette.Tide)
	const width, height = 60, 16
	overlay := renderModalBox("Switcher", "", "projects", 30, 6)
	dialog := renderModalBox("Confirm", "", "hello", 10, 1)
	ox := (width - lipgloss.Width(overlay)) / 2
	oy := (height - lipgloss.Height(overlay)) / 2
	dx := (width - lipgloss.Width(dialog)) / 2
	dy := (height - lipgloss.Height(dialog)) / 2

	frame := composeModal(colouredFrame(width, height), overlay, width, height, colorprofile.TrueColor)
	got := composeModalOver(frame, overlay, dialog, width, height, colorprofile.TrueColor)

	onceDimmed := composeModal(colouredFrame(width, height), dialog, width, height, colorprofile.TrueColor)
	dimmedOverlay := dimFrame(overlay, palette.Tide, overlayDim, colorprofile.TrueColor)
	tests := []struct {
		name   string
		x, y   int
		want   uv.Cell
		accent bool
	}{
		{"screen under both", 0, height - 1, cellOf(t, onceDimmed, 0, height-1), false},
		{"overlay under the dialog", ox, oy, cellOf(t, dimmedOverlay, 0, 0), false},
		{"dialog on top", dx, dy, uv.Cell{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cellOf(t, got, tt.x, tt.y)
			if tt.accent {
				if g := hexOf(c.Style.Fg); g != palette.Tide.Accent {
					t.Errorf("dialog border fg = %s, want tide accent %s", g, palette.Tide.Accent)
				}
				return
			}
			if hexOf(c.Style.Fg) != hexOf(tt.want.Style.Fg) || hexOf(c.Style.Bg) != hexOf(tt.want.Style.Bg) {
				t.Errorf("cell fg %s bg %s, want dimmed once: fg %s bg %s",
					hexOf(c.Style.Fg), hexOf(c.Style.Bg), hexOf(tt.want.Style.Fg), hexOf(tt.want.Style.Bg))
			}
		})
	}
}

// TestModalBoxFillsWithPanel pins docs/dash-design.md §1 and §3.8: an overlay
// is a pane, so an empty cell inside any modal's box is panel, not the bg
// that paintGround would otherwise fill it with. The probed cell is the right
// padding cell of the first content row, which no text ever reaches. Load-
// bearing rows: each modal goes through renderModalBox by its own renderer
// (the ctrl+] strip, actions, jump), plus a bare box standing for the keys,
// answer and inbox overlays, which call it directly; "ANSI" — at 16 colours
// nothing is painted, so the cell keeps the terminal's own background.
func TestModalBoxFillsWithPanel(t *testing.T) {
	restoreTide(t)
	boxes := []struct {
		name string
		draw func() string
	}{
		{"key strip", func() string { return renderKeyStrip("ctrl+]", 80) }},
		{"actions", func() string { return leaderDialog{leader: "ctrl+]"}.view(80, 24) }},
		{"jump", func() string { return jumpDialog{leader: "ctrl+]"}.view(80, 24) }},
		{"keys, answer and inbox", func() string { return renderModalBox("keys", "esc", "hi", 30, 3) }},
	}
	profiles := []struct {
		name    string
		profile colorprofile.Profile
		want    string
	}{
		{"true colour", colorprofile.TrueColor, "#0c1629"},
		{"ANSI", colorprofile.ANSI, "nil"},
	}
	for _, p := range profiles {
		for _, b := range boxes {
			t.Run(p.name+"/"+b.name, func(t *testing.T) {
				useThemeFor(palette.Tide, p.profile)
				box := b.draw()
				got := cellOf(t, box, lipgloss.Width(box)-3, 1)
				if hexOf(got.Style.Bg) != p.want {
					t.Errorf("padding cell bg = %s, want %s", hexOf(got.Style.Bg), p.want)
				}
			})
		}
	}
}
