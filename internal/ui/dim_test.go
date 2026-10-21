package ui

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/palette"
)

// cellOf re-reads a rendered block onto a canvas and returns the cell at
// (x, y), so a test sees the colours a terminal would draw there without
// parsing SGR by hand.
func cellOf(t *testing.T, block string, x, y int) uv.Cell {
	t.Helper()
	w, h := lipgloss.Width(block), lipgloss.Height(block)
	canvas := lipgloss.NewCanvas(max(w, x+1), max(h, y+1))
	canvas.Compose(lipgloss.NewLayer(block))
	c := canvas.CellAt(x, y)
	if c == nil {
		t.Fatalf("no cell at (%d,%d) in %q", x, y, block)
	}
	return *c
}

// hexOf is a colour as "#rrggbb", or "nil" for the terminal default, so a
// failing row prints something a person can compare with the theme table.
func hexOf(c color.Color) string {
	if c == nil {
		return "nil"
	}
	r, g, b := rgb8(c)
	const digits = "0123456789abcdef"
	out := []byte{'#'}
	for _, v := range []float64{r, g, b} {
		n := int(v)
		out = append(out, digits[n>>4], digits[n&15])
	}
	return string(out)
}

// TestDimFrame pins §3.9's dimming, worked out by hand against tide (bg
// #0a1222 = 10,18,34; fg #d8e3f4 = 216,227,244). Load-bearing rows:
// "70% blend" is the exact arithmetic (each channel 70% of the way to bg,
// rounded to nearest: 255→84/89/100 and 0→7/13/24), so a change to the
// amount, the direction or the rounding turns it red; "default colours" is the
// agent's output case — no colour of its own is treated as fg on bg before
// blending, or it would stay at full strength under a dialog; "bold cleared"
// is §3.9's "bold is turned off"; "ANSI profile" is the 16-colour path, which
// must set faint and leave colours and bold exactly as they were; "wide
// character" proves a two-cell rune survives and the cell after it is still
// reached; "bare space" is what keeps the goldens still — a plain space stays
// unstyled so the Compositor goes on trimming trailing blanks; "key strip
// amount" proves the amount is a parameter, not a constant baked in.
func TestDimFrame(t *testing.T) {
	white, black := lipgloss.Color("#ffffff"), lipgloss.Color("#000000")
	tests := []struct {
		name      string
		frame     string
		x         int
		amount    float64
		profile   colorprofile.Profile
		wantFg    string
		wantBg    string
		wantBold  bool
		wantFaint bool
		wantText  string
	}{
		{
			name:   "70% blend",
			frame:  lipgloss.NewStyle().Foreground(white).Background(black).Render("x"),
			amount: overlayDim, profile: colorprofile.TrueColor,
			wantFg: "#545964", wantBg: "#070d18", wantText: "x",
		},
		{
			name:   "default colours",
			frame:  "x",
			amount: overlayDim, profile: colorprofile.TrueColor,
			wantFg: "#485161", wantBg: "#0a1222", wantText: "x",
		},
		{
			name:   "unknown profile counts as true colour",
			frame:  "x",
			amount: overlayDim, profile: colorprofile.Unknown,
			wantFg: "#485161", wantBg: "#0a1222", wantText: "x",
		},
		{
			name:   "bold cleared",
			frame:  lipgloss.NewStyle().Bold(true).Foreground(white).Render("B"),
			amount: overlayDim, profile: colorprofile.TrueColor,
			wantFg: "#545964", wantBg: "#0a1222", wantBold: false, wantText: "B",
		},
		{
			name:   "ANSI profile",
			frame:  lipgloss.NewStyle().Bold(true).Foreground(white).Background(black).Render("A"),
			amount: overlayDim, profile: colorprofile.ANSI,
			wantFg: "#ffffff", wantBg: "#000000", wantBold: true, wantFaint: true, wantText: "A",
		},
		{
			name:   "wide character",
			frame:  lipgloss.NewStyle().Foreground(white).Render("界x"),
			x:      2,
			amount: overlayDim, profile: colorprofile.TrueColor,
			wantFg: "#545964", wantBg: "#0a1222", wantText: "界x",
		},
		{
			name:   "bare space",
			frame:  " x",
			amount: overlayDim, profile: colorprofile.TrueColor,
			wantFg: "nil", wantBg: "nil", wantText: " x",
		},
		{
			name:   "key strip amount",
			frame:  lipgloss.NewStyle().Foreground(white).Render("k"),
			amount: keyStripDim, profile: colorprofile.TrueColor,
			wantFg: "#858991", wantBg: "#0a1222", wantText: "k",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dimFrame(tt.frame, palette.Tide, tt.amount, tt.profile)
			if text := xansi.Strip(got); text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
			c := cellOf(t, got, tt.x, 0)
			if g := hexOf(c.Style.Fg); g != tt.wantFg {
				t.Errorf("fg = %s, want %s", g, tt.wantFg)
			}
			if g := hexOf(c.Style.Bg); g != tt.wantBg {
				t.Errorf("bg = %s, want %s", g, tt.wantBg)
			}
			if bold := c.Style.Attrs&uv.AttrBold != 0; bold != tt.wantBold {
				t.Errorf("bold = %v, want %v", bold, tt.wantBold)
			}
			if faint := c.Style.Attrs&uv.AttrFaint != 0; faint != tt.wantFaint {
				t.Errorf("faint = %v, want %v", faint, tt.wantFaint)
			}
		})
	}
}

// TestDimFrameEmpty pins that an empty frame comes back as it went in rather
// than as a canvas of nothing.
func TestDimFrameEmpty(t *testing.T) {
	if got := dimFrame("", palette.Tide, overlayDim, colorprofile.TrueColor); got != "" {
		t.Errorf("dimFrame(\"\") = %q, want \"\"", got)
	}
}
