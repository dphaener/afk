// dim.go dims whatever is under an overlay (docs/dash-design.md §3.9): every
// cell's text and background colour is blended most of the way toward the
// theme's bg and bold is turned off, so the box on top is the one thing on
// screen that reads at full strength. It works on the rendered frame, cell by
// cell, for the same reason fillBackground does (theme.go): a frame string is
// a run of styled fragments each closed by an SGR reset, and only the cells
// know which colour each one actually wears.
package ui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/dphaener/afk/internal/palette"
)

// overlayDim is how far the frame under an overlay is blended toward bg
// (§3.9: 70%). Far enough that the overlay is plainly the thing to read, not
// so far that the frame vanishes: the owner should still see which session
// the dialog is about.
const overlayDim = 0.7

// keyStripDim is the lighter dimming the ctrl+] key strip will use (§3.9: 50%),
// because the strip is open for a moment and the agent's output under it
// should stay readable. Nothing draws the key strip yet; T16.2 does, and it
// passes this to dimFrame instead of overlayDim.
const keyStripDim = 0.5

// dimFrame returns frame with every cell dimmed amount (0..1) of the way
// toward t's bg, bold cleared. A cell with no colour of its own is treated as
// t's fg on t's bg first (§3.9), since that is how the dashboard draws it: the
// agent's output is mostly default-coloured, and without this step it would
// stay at full strength under the dialog.
//
// At 16 colours or fewer (paintsGround false) it sets the faint attribute
// instead and leaves colours and bold alone: a blend toward a navy that the
// terminal cannot show would round to a named colour the theme never meant.
//
// Bare cells (a space with no style and no link) are left exactly as they are.
// Their dimmed look is the same as their plain look (a space shows no fg, and
// bg blended toward bg is bg, which paintGround puts on them later), and
// keeping them bare is what lets the Compositor in composeModal go on trimming
// the frame's trailing blanks as it always has: styling them would add
// trailing spaces to every ANSI-stripped golden with a dialog open.
//
// Like fillBackground, cells are changed through the pointer CellAt returns
// (SetCell would blank a wide character's continuation column), continuation
// cells (Width 0) are skipped, and each line is touched across its own width
// only, so no cell that was not in the frame gains a style.
func dimFrame(frame string, t palette.Theme, amount float64, profile colorprofile.Profile) string {
	w, h := lipgloss.Width(frame), lipgloss.Height(frame)
	if w == 0 || h == 0 {
		return frame
	}
	full := paintsGround(profile)
	bg := lipgloss.Color(t.Bg)
	fg := lipgloss.Color(t.Fg)
	canvas := lipgloss.NewCanvas(w, h)
	canvas.Compose(lipgloss.NewLayer(frame))
	for y, line := range strings.Split(frame, "\n") {
		lw := lipgloss.Width(line)
		for x := 0; x < lw; x++ {
			cell := canvas.CellAt(x, y)
			if cell == nil || cell.Width == 0 || bareCell(cell) {
				continue
			}
			if !full {
				cell.Style.Attrs |= uv.AttrFaint
				continue
			}
			if cell.Style.Fg == nil {
				cell.Style.Fg = fg
			}
			if cell.Style.Bg == nil {
				cell.Style.Bg = bg
			}
			cell.Style.Fg = blend(cell.Style.Fg, bg, amount)
			cell.Style.Bg = blend(cell.Style.Bg, bg, amount)
			cell.Style.Attrs &^= uv.AttrBold
		}
	}
	return canvas.Render()
}

// bareCell reports whether c is a plain space: no style and no link. See
// dimFrame for why such a cell is left alone.
func bareCell(c *uv.Cell) bool {
	return c.Content == " " && c.Style.IsZero() && c.Link.IsZero()
}

// blend moves from amount of the way toward to, channel by channel in sRGB,
// rounding to the nearest value. sRGB rather than a perceptual space because
// §3.9 states the dimming as a plain 70% blend and the theme hexes were chosen
// in sRGB; the result only has to read as "pushed back", not be exact in
// lightness.
//
// It goes through RGBA() so every colour a cell can carry works the same way:
// the theme's hexes, and the 16 and 256 indexed colours an agent writes, which
// x/ansi resolves to their usual RGB values.
func blend(from, to color.Color, amount float64) color.Color {
	fr, fgc, fb, _ := from.RGBA()
	tr, tg, tb, _ := to.RGBA()
	mix := func(a, b uint32) uint8 {
		x, y := float64(a>>8), float64(b>>8)
		return uint8(math.Round(x + (y-x)*amount))
	}
	return color.RGBA{R: mix(fr, tr), G: mix(fgc, tg), B: mix(fb, tb), A: 0xff}
}
