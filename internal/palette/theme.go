package palette

import "strings"

// Theme is one complete set of the dashboard's colour tokens (docs/dash-design.md
// §1). Every colour the dashboard draws is one of these seventeen tokens, so a
// theme swap changes the whole screen at once and nothing keeps a stray hue.
//
// The dashboard paints its own ground (Bg, and Panel inside panes) on every cell
// instead of leaving the terminal's background showing. That is why the tokens
// carry a background at all: an overlay can then dim the frame under it toward a
// known colour, which is impossible when the ground belongs to the terminal.
// Scheme above stays for the prompt, clean and install, which do not paint a
// ground and still need the old per-background constants.
//
// Values are "#rrggbb" strings, the same form the rest of this package uses, so
// a caller that needs a colour.Color converts once at its own edge.
type Theme struct {
	Name string

	Bg, Panel, Strip, StripFg, Line, Accent, OnAccent, Fg, Muted, Faint, Sel, SelFg, Need, Bad, Work, Done, Meta string
}

// Tide is the default on a dark terminal.
var Tide = Theme{
	Name:     "tide",
	Bg:       "#0a1222",
	Panel:    "#0c1629",
	Strip:    "#16274a",
	StripFg:  "#a9bde0",
	Line:     "#22385f",
	Accent:   "#63b3f2",
	OnAccent: "#061020",
	Fg:       "#d8e3f4",
	Muted:    "#8496b8",
	Faint:    "#2a3f66",
	Sel:      "#1e4b86",
	SelFg:    "#ffffff",
	Need:     "#f3a35c",
	Bad:      "#f2727c",
	Work:     "#63b3f2",
	Done:     "#7fd3a2",
	Meta:     "#b8a2f5",
}

// Fjord is the Nord-like theme: cooler and greyer than tide.
var Fjord = Theme{
	Name:     "fjord",
	Bg:       "#2b303b",
	Panel:    "#2e3440",
	Strip:    "#3b4252",
	StripFg:  "#d8dee9",
	Line:     "#434c5e",
	Accent:   "#88c0d0",
	OnAccent: "#232831",
	Fg:       "#e5e9f0",
	Muted:    "#a3adbf",
	Faint:    "#4c566a",
	Sel:      "#4c566a",
	SelFg:    "#eceff4",
	Need:     "#e6a283",
	Bad:      "#e8848c",
	Work:     "#88c0d0",
	Done:     "#a3be8c",
	Meta:     "#c9a6c4",
}

// Paper is the default on a light terminal.
var Paper = Theme{
	Name:     "paper",
	Bg:       "#f6f5f1",
	Panel:    "#fdfdfb",
	Strip:    "#ebe9e2",
	StripFg:  "#4a4740",
	Line:     "#d6d3c9",
	Accent:   "#2f5fb3",
	OnAccent: "#ffffff",
	Fg:       "#1c1b18",
	Muted:    "#66635b",
	Faint:    "#cfccc1",
	Sel:      "#dbe5f6",
	SelFg:    "#10213f",
	Need:     "#a54f00",
	Bad:      "#b42318",
	Work:     "#2f5fb3",
	Done:     "#2c7a4b",
	Meta:     "#6b4fb3",
}

// Phosphor is the green-on-black terminal look.
var Phosphor = Theme{
	Name:     "phosphor",
	Bg:       "#050a06",
	Panel:    "#07100a",
	Strip:    "#0f2414",
	StripFg:  "#9fe6ad",
	Line:     "#1b3a22",
	Accent:   "#46e07a",
	OnAccent: "#04140a",
	Fg:       "#c4f5cd",
	Muted:    "#6fae7c",
	Faint:    "#1f4228",
	Sel:      "#15532a",
	SelFg:    "#eaffee",
	Need:     "#f5c35a",
	Bad:      "#ff7a6b",
	Work:     "#8fd8ff",
	Done:     "#46e07a",
	Meta:     "#b6efc0",
}

// themes is the order ThemeNames reports, which is the order the docs list them.
var themes = []Theme{Tide, Fjord, Paper, Phosphor}

// ThemeNamed returns the theme with the given name. The match is exact and in
// lower case, after surrounding spaces are trimmed: the setting is typed by hand
// in config.toml, so the trimming forgives stray spaces, and anything looser
// (case folding, prefixes) would hide a typo that the dashboard should report.
func ThemeNamed(name string) (Theme, bool) {
	name = strings.TrimSpace(name)
	for _, t := range themes {
		if t.Name == name {
			return t, true
		}
	}
	return Theme{}, false
}

// ThemeNames lists the theme names in the order tide, fjord, paper, phosphor, so
// a message that names the choices always shows them the same way.
func ThemeNames() []string {
	names := make([]string, 0, len(themes))
	for _, t := range themes {
		names = append(names, t.Name)
	}
	return names
}

// ThemeFor is the default when [dash] theme is unset: tide on a dark terminal and
// paper on a light one, matching the background check the dashboard already runs.
func ThemeFor(isDark bool) Theme {
	if isDark {
		return Tide
	}
	return Paper
}
