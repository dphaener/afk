// Package palette defines the colors used throughout the afk CLI dashboard
// and prompts, as truecolor hex values organized by semantic use (UI chrome,
// status feedback, phase badges). It is a leaf dependency — other packages
// import colors from here but do not export them further.
//
// Two schemes: Dark for the dark terminals most people run, Light for a light
// background, where the same hues need more depth to stay readable. The
// dashboard asks the terminal which it has (tea.RequestBackgroundColor) and
// rebuilds its styles from For(isDark); the constants below are the Dark
// values, for callers that style once at init (internal/prompt).
package palette

// Scheme is one complete set of the palette's colors.
type Scheme struct {
	White, Black string

	// Strongest is the highest-contrast foreground for the terminal
	// background this scheme is for, usable with NO background of its own.
	// It is NOT a fixed hue: on Dark it is near-white, on Light it is
	// near-black, because "strongest" is defined against the surface the
	// glyph lands on and the two surfaces are opposites.
	//
	// White is not a substitute. White is the foreground for text that sits
	// on one of this package's own badge backgrounds (HeaderAccent,
	// SelectionBg, the phase hues), where it is legible in both schemes
	// precisely because the background travels with it. Light.White is
	// "#ffffff", so the same value used bare on a light terminal is
	// invisible. Use Strongest whenever a style sets a foreground and no
	// background.
	Strongest string

	// UI structure — borders, selection, headers.
	Border, SelectionBg, HeaderAccent string

	// Semantic/status foregrounds.
	Muted, Error, Success, Pending, Branch, Emphasis, Warning, Gold string

	// Phase badge backgrounds. Each of the six lifecycle phases gets its own
	// hue, deliberately kept off the values the semantic colors carry:
	// reusing one would make a phase badge that is merely mid-pipeline
	// (executing, fixing) read as if it shared meaning with an unrelated
	// signal (Success, Error) elsewhere on the same screen. ScratchBg marks a
	// kind, not a phase, and sits outside the progression on a hue none of
	// the six occupy.
	PhasePlanningBg, PhaseExecutingBg, PhaseReviewingBg, PhaseFixingBg, PhaseDoneBg, PhaseFailedBg string
	ScratchBg                                                                                      string
}

// The Dark scheme's values, as constants for one-shot stylers.
const (
	White = "#f4f4f8"
	Black = "#101014"

	// Strongest on a dark terminal is White; on a light one it is that
	// scheme's Black. See Scheme.Strongest.
	Strongest = White

	Border       = "#4a4d5c"
	SelectionBg  = "#2b2f3d"
	HeaderAccent = "#6f74f5"

	Muted    = "#7d8394"
	Error    = "#ff5f87"
	Success  = "#3fd68f"
	Pending  = "#5c6070"
	Branch   = "#7fb8cf"
	Emphasis = "#8fb4ff"
	Warning  = "#ffb454"
	Gold     = "#e2bd63"

	PhasePlanningBg  = "#6d7bff"
	PhaseExecutingBg = "#25b7b4"
	PhaseReviewingBg = "#e2bd63"
	PhaseFixingBg    = "#ec8a3f"
	PhaseDoneBg      = "#2fae6f"
	PhaseFailedBg    = "#f24b4b"
	ScratchBg        = "#3d8ef7"
)

// Dark is the scheme for a dark terminal background.
var Dark = Scheme{
	White: White, Black: Black, Strongest: Strongest,
	Border: Border, SelectionBg: SelectionBg, HeaderAccent: HeaderAccent,
	Muted: Muted, Error: Error, Success: Success, Pending: Pending,
	Branch: Branch, Emphasis: Emphasis, Warning: Warning, Gold: Gold,
	PhasePlanningBg: PhasePlanningBg, PhaseExecutingBg: PhaseExecutingBg,
	PhaseReviewingBg: PhaseReviewingBg, PhaseFixingBg: PhaseFixingBg,
	PhaseDoneBg: PhaseDoneBg, PhaseFailedBg: PhaseFailedBg, ScratchBg: ScratchBg,
}

// Light is the scheme for a light terminal background: the same hues, pulled
// darker so muted text and thin borders keep contrast against white, and the
// badge backgrounds saturated enough for white text to read on them.
var Light = Scheme{
	White: "#ffffff", Black: "#16161c", Strongest: "#16161c",
	Border: "#c3c6d3", SelectionBg: "#e4e7f2", HeaderAccent: "#4d52d9",
	Muted: "#6b7080", Error: "#d13a68", Success: "#1f8f5c", Pending: "#9a9eae",
	Branch: "#2f7f9d", Emphasis: "#3352cc", Warning: "#b8720b", Gold: "#9c7d17",
	PhasePlanningBg: "#4f5ee6", PhaseExecutingBg: "#168f8c", PhaseReviewingBg: "#a7801a",
	PhaseFixingBg: "#c96a1e", PhaseDoneBg: "#1f8f5c", PhaseFailedBg: "#d12a3a", ScratchBg: "#2a6fd6",
}

// For picks the scheme for a terminal background.
func For(isDark bool) Scheme {
	if isDark {
		return Dark
	}
	return Light
}
