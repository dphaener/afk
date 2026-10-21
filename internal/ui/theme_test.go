package ui

import (
	"errors"
	"image/color"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/palette"
)

// restoreTide puts the package theme back to tide when a test that applies
// another one ends. theme is package state (styles.go), and every colour test
// in this package (selectionBg, the badge rows) is written against tide.
func restoreTide(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { useTheme(palette.Tide) })
}

// TestCheckTheme pins how the [dash] theme value becomes the theme drawn.
// Load-bearing rows: "unknown" is the failure path — tide is drawn, the theme
// is pinned so a light terminal does not swap it, and the notice names the
// file, the key and every theme so the owner can fix it; "unset" is the only
// unpinned row, the one case the background answer may change; "padded" and
// "upper case" pin palette.ThemeNamed's trim-only rule from this side, so a
// typo is reported rather than quietly forgiven.
func TestCheckTheme(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantTheme  string
		wantPinned bool
		wantNotice string
	}{
		{name: "tide", in: "tide", wantTheme: "tide", wantPinned: true},
		{name: "fjord", in: "fjord", wantTheme: "fjord", wantPinned: true},
		{name: "paper", in: "paper", wantTheme: "paper", wantPinned: true},
		{name: "phosphor", in: "phosphor", wantTheme: "phosphor", wantPinned: true},
		{name: "unset", in: "", wantTheme: "tide", wantPinned: false},
		{name: "blank", in: "   ", wantTheme: "tide", wantPinned: false},
		{name: "padded", in: "  fjord ", wantTheme: "fjord", wantPinned: true},
		{
			name: "unknown", in: "nope", wantTheme: "tide", wantPinned: true,
			wantNotice: `config.toml: dash.theme is "nope", which is not a theme afk has; use tide, fjord, paper or phosphor. Showing tide.`,
		},
		{
			name: "upper case", in: "Paper", wantTheme: "tide", wantPinned: true,
			wantNotice: `config.toml: dash.theme is "Paper", which is not a theme afk has; use tide, fjord, paper or phosphor. Showing tide.`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkTheme(tt.in)
			if got.theme.Name != tt.wantTheme {
				t.Errorf("theme = %q, want %q", got.theme.Name, tt.wantTheme)
			}
			if got.pinned != tt.wantPinned {
				t.Errorf("pinned = %v, want %v", got.pinned, tt.wantPinned)
			}
			if got.notice != tt.wantNotice {
				t.Errorf("notice = %q, want %q", got.notice, tt.wantNotice)
			}
		})
	}
}

// TestThemeOnBackground pins the background answer's effect. Load-bearing
// rows: "unset on light" is the only swap (tide to paper); "chosen on light"
// and "unknown on light" prove a pinned theme stays put, which is what makes
// [dash] theme a choice rather than a first guess.
func TestThemeOnBackground(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		isDark bool
		want   string
	}{
		{name: "unset on dark", in: "", isDark: true, want: "tide"},
		{name: "unset on light", in: "", isDark: false, want: "paper"},
		{name: "chosen on light", in: "phosphor", isDark: false, want: "phosphor"},
		{name: "chosen paper on dark", in: "paper", isDark: true, want: "paper"},
		{name: "unknown on light", in: "nope", isDark: false, want: "tide"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkTheme(tt.in).onBackground(tt.isDark).theme.Name; got != tt.want {
				t.Errorf("theme after background = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPaintsGround pins which colour depths get the painted ground. Load-
// bearing rows: "unknown (zero)" must paint, because it is what every test's
// zero Deps carries and the design promises a painted ground by default;
// "ansi" must not, because §1 says a 16-colour terminal keeps its own ground.
func TestPaintsGround(t *testing.T) {
	tests := []struct {
		name string
		p    colorprofile.Profile
		want bool
	}{
		{name: "unknown (zero)", p: colorprofile.Unknown, want: true},
		{name: "true colour", p: colorprofile.TrueColor, want: true},
		{name: "256 colours", p: colorprofile.ANSI256, want: true},
		{name: "ansi", p: colorprofile.ANSI, want: false},
		{name: "ascii", p: colorprofile.ASCII, want: false},
		{name: "no tty", p: colorprofile.NoTTY, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paintsGround(tt.p); got != tt.want {
				t.Errorf("paintsGround(%v) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
}

// cellBgs re-reads a rendered block onto a canvas and returns each cell's
// background, row by row, skipping wide-character continuation cells. It is
// how a test sees what a terminal would paint without parsing SGR by hand.
func cellBgs(block string, w, h int) [][]color.Color {
	canvas := lipgloss.NewCanvas(w, h)
	canvas.Compose(lipgloss.NewLayer(block))
	rows := make([][]color.Color, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cell := canvas.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
			}
			rows[y] = append(rows[y], cell.Style.Bg)
		}
	}
	return rows
}

// TestFillBackground pins the ground pass. Load-bearing rows: "trailing
// spaces" is the reason the pass works on cells (a string-level fill would
// not reach past the last word, and Canvas.Render would trim bare spaces);
// "own background kept" is what keeps a badge or the selection bar from being
// painted over; "ragged lines" proves a short line is painted across its own
// width and no further, so the ANSI-stripped text is exactly the text that
// came in (the golden frames depend on it: painting the canvas cells after a
// short line would add trailing spaces to every frame with a modal or a
// toast); "wide character" proves a two-cell rune survives the pass.
func TestFillBackground(t *testing.T) {
	ground := lipgloss.Color(palette.Tide.Bg)
	badgeBg := lipgloss.Color(palette.Tide.Need)
	tests := []struct {
		name    string
		block   string
		w, h    int
		lines   []string // the ANSI-stripped lines, trailing spaces kept
		ownBgAt int      // index of a first-line cell that must keep badgeBg, -1 for none
	}{
		{name: "plain", block: "abc", w: 3, h: 1, lines: []string{"abc"}, ownBgAt: -1},
		{name: "trailing spaces", block: "ab   ", w: 5, h: 1, lines: []string{"ab   "}, ownBgAt: -1},
		{name: "styled word", block: lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Tide.Work)).Render("ab") + " c", w: 4, h: 1, lines: []string{"ab c"}, ownBgAt: -1},
		{name: "own background kept", block: "a" + lipgloss.NewStyle().Background(badgeBg).Render("b") + "c", w: 3, h: 1, lines: []string{"abc"}, ownBgAt: 1},
		{name: "ragged lines", block: "abcd\nab", w: 4, h: 2, lines: []string{"abcd", "ab"}, ownBgAt: -1},
		{name: "wide character", block: "日x", w: 3, h: 1, lines: []string{"日x"}, ownBgAt: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := fillBackground(tt.block, ground)
			got := strings.Split(xansi.Strip(out), "\n")
			if strings.Join(got, "\n") != strings.Join(tt.lines, "\n") {
				t.Errorf("text = %q, want %q (the pass must not add or drop a cell)", got, tt.lines)
			}
			for y, row := range cellBgs(out, tt.w, tt.h) {
				// cellBgs skips wide-character continuations, so a line's
				// cells are its runes (every fixture rune is one grapheme).
				n := utf8.RuneCountInString(tt.lines[y])
				for x, bg := range row {
					if x >= n {
						if bg != nil {
							t.Errorf("cell (%d,%d) past the line's end background = %v, want none", x, y, bg)
						}
						continue
					}
					want := ground
					if y == 0 && x == tt.ownBgAt {
						want = badgeBg
					}
					if bg == nil || !sameColor(bg, want) {
						t.Errorf("cell (%d,%d) background = %v, want %v", x, y, bg, want)
					}
				}
			}
		})
	}
}

// TestPaintGround pins View's last step at the two profile classes.
// Load-bearing: the ANSI row must return the frame byte for byte, because a
// 16-colour terminal keeps today's look (§1); the true-colour row must put
// tide's bg (#0a1222) on an empty cell.
func TestPaintGround(t *testing.T) {
	frame := "ab  "
	tests := []struct {
		name      string
		p         colorprofile.Profile
		unchanged bool
	}{
		{name: "true colour paints", p: colorprofile.TrueColor},
		{name: "ansi leaves the frame", p: colorprofile.ANSI, unchanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paintGround(frame, palette.Tide, tt.p)
			if tt.unchanged {
				if got != frame {
					t.Errorf("paintGround = %q, want the frame unchanged", got)
				}
				return
			}
			if !strings.Contains(got, "48;2;10;18;34") {
				t.Errorf("paintGround = %q, want tide's bg on the empty cells", got)
			}
		})
	}
}

// TestViewPaintsThemeGround pins the wiring end to end: a dashboard's frame
// carries its theme's bg, a chosen theme's bg when one is set, and none at 16
// colours. Load-bearing: "paper chosen" proves Deps.Theme reaches View; "ansi"
// proves Deps.ColorProfile does.
func TestViewPaintsThemeGround(t *testing.T) {
	tests := []struct {
		name    string
		theme   string
		profile colorprofile.Profile
		want    string // an SGR run that must be present, "" for none
	}{
		{name: "default tide", want: "48;2;10;18;34"},
		{name: "paper chosen", theme: "paper", want: "48;2;246;245;241"},
		{name: "ansi", theme: "paper", profile: colorprofile.ANSI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreTide(t)
			d := testDeps()
			d.Theme = tt.theme
			d.ColorProfile = tt.profile
			m := New("/repo", time.Second, d)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
			m.setSessions([]Session{sess("a")})
			raw := m.View().Content
			if tt.want == "" {
				for _, g := range []string{palette.Tide.Bg, palette.Paper.Bg} {
					if bg := sgrBg(g); strings.Contains(raw, bg) {
						t.Errorf("frame carries the ground %s at 16 colours", bg)
					}
				}
				return
			}
			if !strings.Contains(raw, tt.want) {
				t.Errorf("frame lacks the ground %s", tt.want)
			}
		})
	}
}

// sgrBg is the SGR background parameter run for a "#rrggbb" token.
func sgrBg(hex string) string {
	c := lipgloss.Color(hex)
	r, g, b := rgb8(c)
	return "48;2;" + itoa(int(r)) + ";" + itoa(int(g)) + ";" + itoa(int(b))
}

// TestNewAppliesThemeSetting pins Deps.Theme through New and the background
// answer. Load-bearing rows: "chosen stays on light" (a pinned theme ignores
// the background answer), "unset follows light" (the only swap), and
// "unknown" (tide is drawn and the notice is raised as an error toast, the
// kind a bad [dash] value gets).
func TestNewAppliesThemeSetting(t *testing.T) {
	light := tea.BackgroundColorMsg{Color: color.White}
	tests := []struct {
		name       string
		in         string
		background *tea.BackgroundColorMsg
		want       string
		wantToast  string
	}{
		{name: "chosen fjord", in: "fjord", want: "fjord"},
		{name: "chosen stays on light", in: "phosphor", background: &light, want: "phosphor"},
		{name: "unset follows light", in: "", background: &light, want: "paper"},
		{name: "unset stays tide", in: "", want: "tide"},
		{name: "unknown", in: "nope", background: &light, want: "tide", wantToast: "dash.theme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreTide(t)
			d := testDeps()
			d.Theme = tt.in
			m := New("/repo", time.Second, d)
			if tt.wantToast != "" {
				if !strings.Contains(m.status, tt.wantToast) || m.statusKind != notifyError {
					t.Errorf("status = %q kind = %v, want an error toast naming %s", m.status, m.statusKind, tt.wantToast)
				}
				for _, name := range palette.ThemeNames() {
					if !strings.Contains(m.status, name) {
						t.Errorf("status = %q, want it to name %q", m.status, name)
					}
				}
			} else if m.status != "" {
				t.Errorf("status = %q, want no toast", m.status)
			}
			if tt.background != nil {
				m, _ = update(t, m, *tt.background)
			}
			if theme.tokens.Name != tt.want {
				t.Errorf("theme = %q, want %q", theme.tokens.Name, tt.want)
			}
			if !sameColor(m.spinner.Style.GetForeground(), lipgloss.Color(theme.tokens.Work)) {
				t.Errorf("spinner was not re-pointed at the %s theme", theme.tokens.Name)
			}
		})
	}
}

// TestDashThemeWrongKindSaysWhatToFix pins the other half of the setting: a
// theme that is not text in quotes is daemonconfig's DashErr, which `afk dash`
// hands back as the folder search's load error (loadDashConfig), so it
// reaches the owner as the scan-error toast. The toast must keep the file,
// the key and the fix word for word.
func TestDashThemeWrongKindSaysWhatToFix(t *testing.T) {
	m := testModel(sess("a"))
	dashErr := `/home/me/.config/afk/config.toml: dash.theme is 3, which is not text in quotes; write it like "tide"`
	m, _ = update(t, m, projectScanMsg{err: errors.New(dashErr)})
	if !strings.Contains(m.status, dashErr) || m.statusKind != notifyError {
		t.Errorf("status = %q kind = %v, want the DashErr text as an error toast", m.status, m.statusKind)
	}
}
