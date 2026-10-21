package palette

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"testing"
)

// TestThemeNamed covers the name lookup behind [dash] theme. The load-bearing rows
// are "nope" and "" — an unknown or empty name must report false, because the
// dashboard uses that false to raise the "not a theme" notice and fall back to
// tide; a lookup that guessed would hide a typo. The padded and upper-case rows
// pin the rule that only surrounding spaces are forgiven (see ThemeNamed).
func TestThemeNamed(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{name: "tide", in: "tide", want: "tide", wantOK: true},
		{name: "fjord", in: "fjord", want: "fjord", wantOK: true},
		{name: "paper", in: "paper", want: "paper", wantOK: true},
		{name: "phosphor", in: "phosphor", want: "phosphor", wantOK: true},
		{name: "padded", in: "  paper \t", want: "paper", wantOK: true},
		{name: "upper case is not a match", in: "Tide", wantOK: false},
		{name: "nope", in: "nope", wantOK: false},
		{name: "empty", in: "", wantOK: false},
		{name: "only spaces", in: "   ", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ThemeNamed(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ThemeNamed(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if tt.wantOK && got.Name != tt.want {
				t.Fatalf("ThemeNamed(%q).Name = %q, want %q", tt.in, got.Name, tt.want)
			}
			if !tt.wantOK && got != (Theme{}) {
				t.Fatalf("ThemeNamed(%q) returned a non-zero theme with ok=false", tt.in)
			}
		})
	}
}

// TestThemeNames pins the order the names are listed in. The rows are the whole
// contract: a message that lists the choices reads them in this order everywhere.
func TestThemeNames(t *testing.T) {
	want := []string{"tide", "fjord", "paper", "phosphor"}
	got := ThemeNames()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ThemeNames() = %v, want %v", got, want)
	}
}

// TestThemeFor covers the unset default. The rows are the two backgrounds: dark
// gives tide and light gives paper, the pair the dashboard swaps between when the
// terminal answers its background colour request.
func TestThemeFor(t *testing.T) {
	tests := []struct {
		name   string
		isDark bool
		want   string
	}{
		{name: "dark terminal", isDark: true, want: "tide"},
		{name: "light terminal", isDark: false, want: "paper"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ThemeFor(tt.isDark).Name; got != tt.want {
				t.Fatalf("ThemeFor(%v).Name = %q, want %q", tt.isDark, got, tt.want)
			}
		})
	}
}

var hexColour = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// tokens lists every token of a theme by name, so a test can walk all seventeen
// and report which one is wrong.
func tokens(t Theme) map[string]string {
	return map[string]string{
		"bg": t.Bg, "panel": t.Panel, "strip": t.Strip, "strip-fg": t.StripFg,
		"line": t.Line, "accent": t.Accent, "on-accent": t.OnAccent, "fg": t.Fg,
		"muted": t.Muted, "faint": t.Faint, "sel": t.Sel, "sel-fg": t.SelFg,
		"need": t.Need, "bad": t.Bad, "work": t.Work, "done": t.Done, "meta": t.Meta,
	}
}

// TestThemeTokensAreHex checks that every token of every theme is a lower-case
// "#rrggbb" string, the form the rest of the package and lipgloss expect. A
// missing or mistyped token would otherwise surface only as a wrong colour on
// screen. Each theme is its own row, so the failure names the theme.
func TestThemeTokensAreHex(t *testing.T) {
	for _, th := range []Theme{Tide, Fjord, Paper, Phosphor} {
		t.Run(th.Name, func(t *testing.T) {
			if len(tokens(th)) != 17 {
				t.Fatalf("theme has %d tokens, want 17", len(tokens(th)))
			}
			for name, hex := range tokens(th) {
				if !hexColour.MatchString(hex) {
					t.Errorf("token %s = %q, want #rrggbb", name, hex)
				}
			}
		})
	}
}

// relativeLuminance is the WCAG 2.x relative luminance of a "#rrggbb" colour.
func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	ch := func(i int) float64 {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatalf("bad hex %q: %v", hex, err)
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(0) + 0.7152*ch(1) + 0.0722*ch(2)
}

// contrast is the WCAG contrast ratio between two "#rrggbb" colours.
func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := relativeLuminance(t, a), relativeLuminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestMutedContrast checks the §1 promise that `muted` meets 4.5:1 on `panel`,
// the ratio WCAG asks for body-sized text. Each theme is a row, because the
// promise is per theme and a single bad palette must name itself in the failure.
func TestMutedContrast(t *testing.T) {
	for _, th := range []Theme{Tide, Fjord, Paper, Phosphor} {
		t.Run(th.Name, func(t *testing.T) {
			if got := contrast(t, th.Muted, th.Panel); got < 4.5 {
				t.Fatalf("muted %s on panel %s contrast = %.2f, want >= 4.5", th.Muted, th.Panel, got)
			}
		})
	}
}
