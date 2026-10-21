package ui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/dphaener/afk/internal/palette"
)

// themeSetting is what checkTheme decides for the theme set in config.toml's
// [dash] table (docs/dash-design.md §1).
type themeSetting struct {
	// theme is the theme to draw with now.
	theme palette.Theme
	// pinned says the owner chose the theme, so the terminal's background
	// answer (tea.BackgroundColorMsg) must not swap it. An unknown name is
	// pinned too: the owner asked for a fixed theme, and tide standing in for
	// it should not flip to paper on a light terminal behind the notice's back.
	pinned bool
	// notice is the plain-language error to show once at start when the name
	// is not a theme afk has, "" otherwise.
	notice string
}

// checkTheme decides which theme the dashboard draws with, given the raw
// [dash] theme value from config.toml. It is pure, like checkLeader: the
// caller reads the file once at start, and a wrong name never stops the
// dashboard, it draws tide and says why.
//
// The name is checked here and not in daemonconfig because a bad value there
// (DashErr) resets the whole [dash] table, folder search included, and a typo
// in a colour name should not cost the owner their search folders.
//
// Unset gives tide, unpinned: tide is the dark default, and the background
// answer (onBackground) swaps it for paper on a light terminal.
func checkTheme(configured string) themeSetting {
	name := strings.TrimSpace(configured)
	if name == "" {
		return themeSetting{theme: palette.ThemeFor(true)}
	}
	if t, ok := palette.ThemeNamed(name); ok {
		return themeSetting{theme: t, pinned: true}
	}
	return themeSetting{
		theme:  palette.Tide,
		pinned: true,
		notice: fmt.Sprintf("config.toml: dash.theme is %q, which is not a theme afk has; use %s. Showing tide.",
			name, themeChoices()),
	}
}

// themeChoices names the themes for the notice ("tide, fjord, paper or
// phosphor"), from palette.ThemeNames so the words cannot fall out of step
// with the themes that exist.
func themeChoices() string {
	names := palette.ThemeNames()
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// onBackground is the setting after the terminal answers the background
// request. A pinned theme stays as it is; an unpinned one follows the
// terminal: tide on a dark background, paper on a light one.
func (s themeSetting) onBackground(isDark bool) themeSetting {
	if s.pinned {
		return s
	}
	s.theme = palette.ThemeFor(isDark)
	return s
}

// paintsGround reports whether the dashboard paints the theme's bg on every
// cell for a terminal with this colour profile. It does not at 16 colours or
// fewer (§1): there the nearest named colour to a dark navy is plain black or
// blue, which would turn the whole screen a colour the theme never meant.
//
// colorprofile.Unknown is the Profile type's zero value (colorprofile
// profile.go: Unknown = iota, then NoTTY, ASCII, ANSI, ANSI256, TrueColor), so
// it is what a Deps that never set ColorProfile carries. It counts as true
// colour, which keeps every test in this package painting without having to
// say so. `afk dash` always sets a detected profile, and Detect does not answer
// Unknown on Linux or macOS: envColorProfile (colorprofile env.go) starts
// every answer at NoTTY or ANSI and only raises it.
func paintsGround(p colorprofile.Profile) bool {
	switch p {
	case colorprofile.NoTTY, colorprofile.ASCII, colorprofile.ANSI:
		return false
	default:
		return true
	}
}

// fillBackground paints c on every cell of block that has no background of its
// own, including the spaces after the last word on a line, and returns the
// block re-rendered.
//
// It works on cells (a lipgloss Canvas) rather than on the string because the
// string cannot be fixed up: every styled fragment ends in an SGR reset, and
// a reset clears the background along with the fragment's own colours, so a
// ground wrapped around the text would stop at the first coloured word. On
// the canvas each cell carries its own background, and a nil one is exactly
// "the terminal's default", the cell to paint.
//
// The cell is changed through the pointer CellAt returns (it points into the
// canvas's line, ultraviolet buffer.go Line.At) rather than by SetCell:
// SetCell on the first column of a wide character blanks its continuation
// columns first (Line.Set), which would erase the character. Zero-width
// continuation cells are skipped, because a continuation with a style set is
// no longer the zero cell and would render as a cell of its own.
//
// Each line is painted across its own width only, never past its last cell.
// The canvas is as wide as the widest line, so a shorter line (the header and
// the help row in some frames, and every row a modal or toast was composited
// onto, since Compositor.Render trims bare trailing spaces) has blank canvas
// cells after it that were never part of the block. Painting those would make
// Canvas.Render keep them (it trims bare trailing spaces only), adding visible
// width to the frame: every ANSI-stripped golden would gain trailing spaces
// though no word moved. The line's own trailing spaces are painted and kept,
// so the text is exactly the text that came in. View pads every row to the
// screen width first (padRows), so in a full frame no row is short.
func fillBackground(block string, c color.Color) string {
	w, h := lipgloss.Width(block), lipgloss.Height(block)
	if w == 0 || h == 0 {
		return block
	}
	canvas := lipgloss.NewCanvas(w, h)
	canvas.Compose(lipgloss.NewLayer(block))
	for y, line := range strings.Split(block, "\n") {
		lw := lipgloss.Width(line)
		for x := 0; x < lw; x++ {
			cell := canvas.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
			}
			if cell.Style.Bg == nil {
				cell.Style.Bg = c
			}
		}
	}
	return canvas.Render()
}

// paintGround is View's last step: the theme's bg on every unpainted cell of
// the finished frame, toasts and overlays included, unless the terminal has 16
// colours or fewer (paintsGround).
func paintGround(frame string, t palette.Theme, p colorprofile.Profile) string {
	if !paintsGround(p) {
		return frame
	}
	return fillBackground(frame, lipgloss.Color(t.Bg))
}

// raiseThemeNotice shows checkTheme's notice once, at start, as an error
// toast. It goes the way a bad [dash] value does (notifyScanError: error kind,
// the bgScanError category), because it is the same kind of news: a [dash]
// setting in config.toml the dashboard could not use. New is its only caller,
// so like raiseStartupNotice it needs no "already raised" field.
//
// It is flushed at once for raiseStartupNotice's reason: left pending, it
// would be collapsed into a count with whatever the first Update raises, and
// its words (which setting, which names work) would be lost. When the start-up
// notice is already on screen, the flush leaves this one waiting its turn
// behind that toast rather than merging the two.
func (m *Model) raiseThemeNotice(text string) {
	if text == "" {
		return
	}
	m.notifyBackground(notifyError, bgScanError, text)
	m.flushBackground()
}
