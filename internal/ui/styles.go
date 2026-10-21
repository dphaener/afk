// styles.go holds every lipgloss style used by the dashboard. Styles are pure
// values (no I/O, no terminal probing beyond lipgloss's own color-profile
// detection at render time), so they live in one place and are shared by the
// list view, the detail panels, and the chrome (header/help/status lines).
//
// Every colour is a token of one palette.Theme (docs/dash-design.md §1), so a
// theme swap restyles the whole screen at once. Nothing here reads or writes
// files or processes.
package ui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"github.com/charmbracelet/colorprofile"
	xansi "github.com/charmbracelet/x/ansi"

	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/palette"
)

// styleSet is the full palette of styles the views draw from. A single
// package-level instance (theme) is built from one palette.Theme (useTheme); it
// carries no mutable state, so sharing it across the model is safe.
type styleSet struct {
	// tokens is the theme this set was built from. View reads its bg to
	// paint the ground (fillBackground), so the ground and every style
	// always come from the same theme.
	tokens palette.Theme
	// flat says the terminal has 16 colours or fewer (paintsGround false), so
	// the parts leave a pane's interior on the terminal's own background
	// instead of painting panel (§1). It is false for a set newStyleSet
	// returns, which is what every test that builds a set for a theme wants;
	// useThemeFor sets it from the dashboard's colour profile.
	flat bool

	header      lipgloss.Style
	headerCount lipgloss.Style
	errorText   lipgloss.Style

	listBox          lipgloss.Style
	listBoxFocused   lipgloss.Style
	detailBox        lipgloss.Style
	detailBoxFocused lipgloss.Style
	modalBox         lipgloss.Style
	paneBox          lipgloss.Style
	paneBoxFocused   lipgloss.Style
	paneTitle        lipgloss.Style
	rowSelected      lipgloss.Style
	rowNormal        lipgloss.Style
	rowCursor        lipgloss.Style
	sessionName      lipgloss.Style
	hostRunning      lipgloss.Style
	hostStopped      lipgloss.Style
	branch           lipgloss.Style
	awaiting         lipgloss.Style
	awaitingFlag     lipgloss.Style
	columnCaption    lipgloss.Style
	boxTitle         lipgloss.Style
	boxTitleStrong   lipgloss.Style
	staleText        lipgloss.Style
	dim              lipgloss.Style
	panelTitle       lipgloss.Style
	taskComplete     lipgloss.Style
	taskFailed       lipgloss.Style
	taskPending      lipgloss.Style
	diffFileHeader   lipgloss.Style
	diffHunkHeader   lipgloss.Style
	diffAdded        lipgloss.Style
	diffRemoved      lipgloss.Style
	waveHeading      lipgloss.Style
	verdictOK        lipgloss.Style
	verdictBad       lipgloss.Style
	statusLine       lipgloss.Style
	helpLine         lipgloss.Style
	// toastKinds is the toast's per-severity appearance (toast.go's
	// renderToast), indexed by notificationKind — a border colour and leading
	// marker distinct enough to read as info/success/warn/error without
	// parsing the words, plus the text colour they share. It replaces the
	// single toastBox field renderToast used to draw every kind with (now
	// removed as dead); statusLine is untouched and still backs
	// attachpane.go's status row and the confirm/label dialogs, which this
	// task does not touch.
	toastKinds [4]toastKindStyle
	// spinner colours the in-flight spinner that leads the help row.
	spinner lipgloss.Style
	// progressBlend and progressEmpty are the Overview bar's fill gradient
	// (accent → success as it fills) and its empty-cell colour, for
	// bubbles/progress, which takes colours rather than styles.
	progressBlend [2]color.Color
	progressEmpty color.Color
	// helpStyles is bubbles/help's two-tone set: keys in the emphasis colour,
	// descriptions and separators muted, so a help line reads as key→meaning
	// pairs instead of one grey run of words.
	helpStyles help.Styles
	// statusHelpStyles is helpStyles for the status bar (§3.2): the same
	// bold accent keys, with their words in strip-fg because the bar is
	// filled with strip and muted words would sink into it. The overlays'
	// help lines keep helpStyles, since they sit on the ground, not a strip.
	statusHelpStyles help.Styles

	// phaseBadges maps each lifecycle phase to a colored badge style. Unknown
	// or empty phases fall back to phaseUnknown.
	phaseBadges  map[string]lipgloss.Style
	phaseUnknown lipgloss.Style

	// phaseBadgesPlain is the space-saving fallback for phaseBadges: same
	// hue, no background/padding. renderRow switches to this when the badge's
	// own padded/backgrounded form would leave too little room for the
	// session name at narrow sidebar widths (see renderRow in views.go).
	phaseBadgesPlain  map[string]lipgloss.Style
	phaseUnknownPlain lipgloss.Style

	// faultBadges and faultBadgesPlain are the worktree-side fault rows'
	// badges (renderFaultRow), keyed by the label they render. They are a
	// SEPARATE map from phaseBadges rather than two more pseudo-phase keys
	// beside scratchPhaseKey: those two helpers fall back to the "unknown"
	// badge for a key they do not hold, and "unknown" is a real, different
	// verdict on this dashboard — it is what a session with a corrupt
	// status.json wears. A fault row silently degrading into that label would
	// report the wrong problem rather than no problem.
	faultBadges      map[string]lipgloss.Style
	faultBadgesPlain map[string]lipgloss.Style
}

// toastKindStyle is one notification kind's toast appearance: box is the
// bordered style renderToast sizes and renders into, text is the box's
// content colour, and marker is the glyph renderToast leads the message with.
// info's marker is "" — the neutral, unremarkable kind draws no attention to
// itself, and every pre-kind toast call in this package's tests already
// renders the zero-value kind, so leaving it markerless is also what keeps
// those tests' width assertions byte-for-byte unchanged.
type toastKindStyle struct {
	box    lipgloss.Style
	text   lipgloss.Style
	marker string
}

// theme is the shared style set, built from tide until New applies the theme
// the owner chose ([dash] theme, checkTheme) or the terminal's background
// answer picks paper. It is a package variable rather than a Model field
// because every renderer in the package reads it, including pure helpers with
// no Model in reach; the writers are New (once, before anything renders) and
// Update's tea.BackgroundColorMsg case, both on the program's single goroutine.
var theme = newStyleSet(palette.Tide)

// useTheme rebuilds theme from t. It is the one writer of theme, so whatever
// chose t (the setting, or the background answer when the setting is unset)
// cannot leave the package holding a half-built set.
func useTheme(t palette.Theme) {
	theme = newStyleSet(t)
}

// useThemeFor is useTheme for a terminal with colour profile p: the set is
// marked flat when the terminal cannot show the theme's grounds (16 colours or
// fewer, paintsGround), so the panes leave their interiors unpainted the way
// paintGround leaves the frame. The Model calls this one; useTheme stays for
// the tests that only need a theme.
func useThemeFor(t palette.Theme, p colorprofile.Profile) {
	useTheme(t)
	theme.flat = !paintsGround(p)
}

// scratchPhaseKey is the pseudo-phase key phaseBadge/phaseBadgePlain look up
// for a scratch session (discover.SessionState.Scratch): it can't collide
// with a real state.Phase value (planning/executing/reviewing/fixing/done/
// failed), so it shares the same map and helpers without a separate code path.
const scratchPhaseKey = "scratch"

// newStyleSet builds every dashboard style from one theme's tokens
// (docs/dash-design.md §1). Each style takes the token whose MEANING matches
// its job, not the hue it used to have: borders line (accent when focused),
// body fg, quiet text muted, decoration faint, needs-you need, errors bad,
// afk-is-working work, done done, ids and branches meta, the selection
// sel/sel-fg. That one rule is what lets four themes share this function: a
// theme only ever says what each meaning looks like.
func newStyleSet(t palette.Theme) styleSet {
	c := func(hex string) color.Color { return lipgloss.Color(hex) }
	badge := func(fg, bg string) lipgloss.Style {
		return lipgloss.NewStyle().
			Foreground(c(fg)).
			Background(c(bg)).
			Bold(true).
			Padding(0, 1)
	}
	// badgePlain is the unpadded, backgroundless fallback: same hue family as
	// the padded badge (its bg color reused as the plain fg), just narrower.
	badgePlain := func(fg string) lipgloss.Style {
		return lipgloss.NewStyle().
			Foreground(c(fg)).
			Bold(true)
	}
	// toastBoxOf builds one kind's toast border: rounded and padded like
	// every other bordered style in this set, differing only in the colour
	// that carries the kind.
	toastBoxOf := func(border string) lipgloss.Style {
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(c(border)).
			Padding(0, 1)
	}

	return styleSet{
		tokens: t,
		// The header badge is the top bar's [ afk ] badge of §3.1: on-accent
		// text on accent, the one pairing every theme guarantees reads.
		header: lipgloss.NewStyle().Bold(true).
			Foreground(c(t.OnAccent)).
			Background(c(t.Accent)).
			Padding(0, 1),
		headerCount: lipgloss.NewStyle().Foreground(c(t.Muted)),
		errorText:   lipgloss.NewStyle().Foreground(c(t.Bad)).Bold(true),

		listBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(c(t.Line)).
			Padding(0, 1),
		listBoxFocused: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(c(t.Accent)).
			Padding(0, 1),
		detailBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(c(t.Line)).
			Padding(0, 1),
		// detailBoxFocused is detailBox's accent pair (W3.T1): the border
		// listBoxStyle/paneBoxStyle already blur toward when THEIR surface
		// takes the keyboard, worn here while the detail pane does instead.
		// Identical border glyphs and padding to detailBox — the two styles
		// must never diverge in any geometry-affecting property, since
		// detailContentWidth/detailRowBudget (below) assume whichever one
		// renders takes up exactly the same space.
		detailBoxFocused: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(c(t.Accent)).
			Padding(0, 1),
		// modalBox is the floating overlay (modal.go). Its border is the accent
		// the focused surface wears (listBoxFocused, paneBoxFocused): a modal
		// owns the keyboard while it is up, and the border is how the frame
		// says so (§3.8). Same padding as detailBox so contentW arithmetic is
		// shared.
		modalBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(c(t.Accent)).
			Padding(0, 1),
		// toastKinds gives renderToast a distinct look per notificationKind.
		// info gets the neutral line border listBox/detailBox already wear
		// and plain text, no marker — it is the zero value, so every toast
		// call written before kinds existed renders as this entry.
		// success/warn/error each pair a coloured border with the matching
		// bold text colour, taken by meaning: done for success, need for a
		// warning (there is something for you to do), bad for an error. The
		// marker glyphs come from the same set views.go's task/verdict rows
		// use (✓/✗) plus ⚠ for warn.
		toastKinds: [4]toastKindStyle{
			notifyInfo: {
				box:  toastBoxOf(t.Line),
				text: lipgloss.NewStyle(),
			},
			notifySuccess: {
				box:    toastBoxOf(t.Done),
				text:   lipgloss.NewStyle().Foreground(c(t.Done)).Bold(true),
				marker: "✓",
			},
			notifyWarn: {
				box:    toastBoxOf(t.Need),
				text:   lipgloss.NewStyle().Foreground(c(t.Need)).Bold(true),
				marker: "⚠",
			},
			notifyError: {
				box:    toastBoxOf(t.Bad),
				text:   lipgloss.NewStyle().Foreground(c(t.Bad)).Bold(true),
				marker: "✗",
			},
		},
		// The accent border marks the surface that owns keyboard input
		// (§3.3: only one pane has the keyboard at a time): paneBoxFocused
		// while keys route to the hosted child, listBoxFocused while they
		// route to the dashboard, detailBoxFocused (above) while they route
		// to the focused detail pane. Blurred boxes share line.
		// Focused/blurred pairs must keep identical border and padding
		// geometry — renderPane's wrap-width math (contentW + 2) assumes it.
		paneBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(c(t.Line)).
			Padding(0, 1),
		paneBoxFocused: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(c(t.Accent)).
			Padding(0, 1),
		// paneTitle, boxTitle and panelTitle name an active content region,
		// so they share one hue, accent, the colour that marks where to look.
		paneTitle: lipgloss.NewStyle().Bold(true).Foreground(c(t.Accent)),

		rowSelected: lipgloss.NewStyle().
			Foreground(c(t.SelFg)).
			Background(c(t.Sel)).
			Bold(true),
		rowNormal: lipgloss.NewStyle(),
		// rowCursor paints the Build tab's cursor marker (buildtab.go).
		// Foreground only: SGR is additive, so the marker's own sequence
		// leaves rowSelected's background in place under the half of the
		// cell the glyph does not fill, and a background here would only
		// repeat it. accent is the colour a focused box's border already
		// wears, so the marker and the frame around it agree on which colour
		// means "your keys go here". The session list's and the all-projects
		// screen's rail is not drawn with it: their selected rows are the
		// row part's, all sel-fg (rowMarker, views.go).
		rowCursor:   lipgloss.NewStyle().Foreground(c(t.Accent)),
		sessionName: lipgloss.NewStyle().Bold(true),
		// A running host is afk at work (§3.6's ◐ colour), a stopped one is
		// quiet.
		hostRunning: lipgloss.NewStyle().Foreground(c(t.Work)),
		hostStopped: lipgloss.NewStyle().Foreground(c(t.Muted)),
		branch:      lipgloss.NewStyle().Foreground(c(t.Meta)),
		// awaiting is the needs-you badge: on-accent bold text on need, the
		// same pairing as §3.7's alert bar.
		awaiting: lipgloss.NewStyle().
			Foreground(c(t.OnAccent)).
			Background(c(t.Need)).
			Bold(true).
			Padding(0, 1),
		// awaitingFlag is the one-cell form of awaiting: the session row's flag
		// column has no room for a padded badge, and a foreground-only dot
		// keeps the column from bleeding a background into its neighbours.
		awaitingFlag:  lipgloss.NewStyle().Foreground(c(t.Need)).Bold(true),
		columnCaption: lipgloss.NewStyle().Foreground(c(t.Muted)).Bold(true),
		// boxTitle is the label labelBorders sets into a box's top edge. It is
		// brighter than the border it interrupts so the eye finds it, and bold
		// rather than underlined because an underline under a border glyph
		// row reads as a second rule.
		boxTitle: lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		// boxTitleStrong was renderList's session-box title: the active
		// project's name is the frame's primary identifier, so it outweighed
		// boxTitle's accent hue. Since T16.1 the session list and the
		// all-projects screen name themselves in the pane part's title strip
		// (parts.go), whose bold strip-fg on strip does that job, so no screen
		// draws with it now; labelBordersStyled keeps accepting it for a box
		// that wants a stronger title on a plain edge.
		//
		// fg, NOT sel-fg or on-accent. This label lands straight on the
		// painted ground with no background of its own, and those two are
		// foregrounds for text on sel or accent: paper's on-accent is
		// "#ffffff", which on paper's near-white bg would make the frame's
		// primary identifier invisible. fg is the one text token every theme
		// sets against bg. TestTitleStylesStayReadableOnEveryTheme pins it.
		boxTitleStrong: lipgloss.NewStyle().Foreground(c(t.Fg)).Bold(true),
		staleText:      lipgloss.NewStyle().Foreground(c(t.Bad)).Italic(true),
		dim:            lipgloss.NewStyle().Foreground(c(t.Muted)),
		panelTitle:     lipgloss.NewStyle().Bold(true).Underline(true).Foreground(c(t.Accent)),

		taskComplete: lipgloss.NewStyle().Foreground(c(t.Done)),
		taskFailed:   lipgloss.NewStyle().Foreground(c(t.Bad)),
		// A pending task is a quiet state (§3.6's ○ row), so muted: faint
		// is decoration only and a task's name must stay readable.
		taskPending:    lipgloss.NewStyle().Foreground(c(t.Muted)),
		diffFileHeader: lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		diffHunkHeader: lipgloss.NewStyle().Foreground(c(t.Muted)),
		diffAdded:      lipgloss.NewStyle().Foreground(c(t.Done)),
		diffRemoved:    lipgloss.NewStyle().Foreground(c(t.Bad)),
		// A wave heading names a group of task ids, and ids are meta.
		waveHeading: lipgloss.NewStyle().Bold(true).Foreground(c(t.Meta)),

		verdictOK:  lipgloss.NewStyle().Foreground(c(t.Done)).Bold(true),
		verdictBad: lipgloss.NewStyle().Foreground(c(t.Bad)).Bold(true),

		// statusLine backs attachpane.go's status row and the confirm and
		// label dialogs' notes: words asking for the owner's attention.
		statusLine: lipgloss.NewStyle().Foreground(c(t.Need)),
		helpLine:   lipgloss.NewStyle().Foreground(c(t.Muted)),
		// The spinner says afk is working on something.
		spinner: lipgloss.NewStyle().Foreground(c(t.Work)),
		// §3.7: a progress bar's done part is done and its rest is faint.
		// The blend keeps its two slots (bubbles/progress takes a gradient)
		// but both ends are done, so the fill is one colour.
		progressBlend: [2]color.Color{
			c(t.Done),
			c(t.Done),
		},
		progressEmpty: c(t.Faint),
		// Keys are bold accent and their words muted (§3.2); the separators
		// are decoration, so faint.
		helpStyles: help.Styles{
			Ellipsis:       lipgloss.NewStyle().Foreground(c(t.Muted)),
			ShortKey:       lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
			ShortDesc:      lipgloss.NewStyle().Foreground(c(t.Muted)),
			ShortSeparator: lipgloss.NewStyle().Foreground(c(t.Faint)),
			FullKey:        lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
			FullDesc:       lipgloss.NewStyle().Foreground(c(t.Muted)),
			FullSeparator:  lipgloss.NewStyle().Foreground(c(t.Faint)),
		},
		// On the status bar's strip fill the words are strip-fg and the
		// separators muted: faint is chosen to recede on the ground and
		// all but vanishes on strip.
		statusHelpStyles: help.Styles{
			Ellipsis:       lipgloss.NewStyle().Foreground(c(t.StripFg)),
			ShortKey:       lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
			ShortDesc:      lipgloss.NewStyle().Foreground(c(t.StripFg)),
			ShortSeparator: lipgloss.NewStyle().Foreground(c(t.Muted)),
			FullKey:        lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
			FullDesc:       lipgloss.NewStyle().Foreground(c(t.StripFg)),
			FullSeparator:  lipgloss.NewStyle().Foreground(c(t.Muted)),
		},

		// The phase badges take §3.6's colours: every phase where afk is at
		// work is work, done is done, failed is bad. A scratch (one-off)
		// session is quiet, so muted; an unknown phase gets the neutral line.
		phaseBadges: map[string]lipgloss.Style{
			"planning":      badge(t.OnAccent, t.Work),
			"executing":     badge(t.OnAccent, t.Work),
			"reviewing":     badge(t.OnAccent, t.Work),
			"fixing":        badge(t.OnAccent, t.Work),
			"done":          badge(t.OnAccent, t.Done),
			"failed":        badge(t.OnAccent, t.Bad),
			scratchPhaseKey: badge(t.OnAccent, t.Muted),
		},
		phaseUnknown: badge(t.Fg, t.Line),

		phaseBadgesPlain: map[string]lipgloss.Style{
			"planning":      badgePlain(t.Work),
			"executing":     badgePlain(t.Work),
			"reviewing":     badgePlain(t.Work),
			"fixing":        badgePlain(t.Work),
			"done":          badgePlain(t.Done),
			"failed":        badgePlain(t.Bad),
			scratchPhaseKey: badgePlain(t.Muted),
		},
		// muted rather than the padded form's line: plain, the label IS the
		// coloured thing, and line is too close to the ground to read.
		phaseUnknownPlain: badgePlain(t.Muted),

		// A session whose worktree died under it broke (bad); a worktree that
		// was never afk's is yours to deal with (need) — §3.6's meanings.
		faultBadges: map[string]lipgloss.Style{
			brokenBadgeLabel: badge(t.OnAccent, t.Bad),
			orphanBadgeLabel: badge(t.OnAccent, t.Need),
		},
		faultBadgesPlain: map[string]lipgloss.Style{
			brokenBadgeLabel: badgePlain(t.Bad),
			orphanBadgeLabel: badgePlain(t.Need),
		},
	}
}

// spanSentinel is a byte no rendered row can contain (NUL never survives a
// terminal write and nothing in this package emits one), used to split a
// style's opening sequence off its closing one.
const spanSentinel = "\x00"

// The two forms of an SGR reset. lipgloss v2 emits the short one, but a string
// arriving from bubbles, x/ansi or a future lipgloss could carry either, and a
// missed reset is exactly the defect this function exists to fix.
const (
	sgrResetShort = "\x1b[m"
	sgrResetLong  = "\x1b[0m"
)

// spanStyle makes style's colours survive the resets already embedded in line.
//
// lipgloss closes every styled fragment with an SGR reset, and a reset clears
// the ENCLOSING style's background along with the fragment's own. So a row
// assembled from styled cells (composeRow: a coloured host dot, a bold name, a
// phase in its phase hue) wore its selection background only as far as the
// first inner reset — cell three on a session row — and the highlight bar was
// two cells wide no matter how wide the sidebar was.
//
// Re-emitting style's opening sequence immediately after each reset is what
// carries the background across the rest of the line. The inserted sequences
// are zero-width, so measurements upstream (cell, truncate) and the Width()
// padding downstream are unaffected.
func spanStyle(line string, style lipgloss.Style) string {
	open, _, ok := strings.Cut(style.Render(spanSentinel), spanSentinel)
	if !ok || open == "" {
		return line
	}
	// Long form first: "\x1b[0m" does not contain "\x1b[m" as a substring, and
	// open contains neither, so neither pass can rewrite the other's output.
	line = strings.ReplaceAll(line, sgrResetLong, sgrResetLong+open)
	return strings.ReplaceAll(line, sgrResetShort, sgrResetShort+open)
}

// toastStyle returns kind's toast appearance. notificationKind is a closed
// four-value type (notification.go), so every value indexes toastKinds; the
// bounds check only guards a value smuggled in some other way (an int
// conversion) from panicking, falling back to the neutral info entry.
func (s styleSet) toastStyle(kind notificationKind) toastKindStyle {
	if int(kind) < 0 || int(kind) >= len(s.toastKinds) {
		return s.toastKinds[notifyInfo]
	}
	return s.toastKinds[kind]
}

// brokenBadgeLabel and orphanBadgeLabel are the two worktree-side faults' row
// badges. They are uppercase where every phase badge is lowercase because they
// are not phases: a row wearing one has no lifecycle to be at a point in, and
// the only actions it accepts are `c` and the cursor.
const (
	brokenBadgeLabel = "BROKEN"
	orphanBadgeLabel = "ORPHAN"
)

// phaseBadge renders a colored badge for a lifecycle phase, falling back to a
// neutral badge for empty/unknown phases (e.g. a stale status file).
func (s styleSet) phaseBadge(phase string) string {
	label := phase
	if label == "" {
		label = "unknown"
	}
	st, ok := s.phaseBadges[phase]
	if !ok {
		st = s.phaseUnknown
	}
	return st.Render(label)
}

// phaseBadgePlain renders the same phase label as phaseBadge but without the
// background/padding — used at narrow sidebar widths where the name would
// otherwise be squeezed to nothing (see renderRow in views.go).
func (s styleSet) phaseBadgePlain(phase string) string {
	label := phase
	if label == "" {
		label = "unknown"
	}
	st, ok := s.phaseBadgesPlain[phase]
	if !ok {
		st = s.phaseUnknownPlain
	}
	return st.Render(label)
}

// phaseAbbrev is each phase label's short form, for the sidebar rungs where
// rowColumns cannot afford the full word (rowPhaseAbbrevWidth cells). Every
// value fits in four cells so "STALE" — the one five-letter state, drawn by
// renderSessionRow itself — is the widest thing the column ever holds.
var phaseAbbrev = map[string]string{
	"planning":      "PLAN",
	"executing":     "EXEC",
	"reviewing":     "REVW",
	"fixing":        "FIX",
	"done":          "DONE",
	"failed":        "FAIL",
	scratchPhaseKey: "SCR",
}

// phaseText renders a phase for the session list's PHASE column: uppercase,
// foreground colour only, full or abbreviated. Uppercase because the column
// is read as a state, not a word, and the fault labels it shares the column
// with (BROKEN, ORPHAN) were already uppercase. Foreground-only because a
// background badge in a fixed-width column paints its padding too, and the
// padded form is what phaseBadge is for (the detail header still uses it).
// Unknown or empty phases render UNKNOWN / UNKN.
//
// The colour is the phase's §3.6 colour from stateLook, the one mapping every
// screen draws states with (parts.go), so the PHASE column and the detail
// header cannot disagree about what a phase means. An empty phase is a status
// file that could not be read, which §3.6 counts as broken (bad); a phase this
// afk does not know is quiet.
func (s styleSet) phaseText(phase string, abbrev bool) string {
	label := strings.ToUpper(phase)
	if phase == "" {
		label = "UNKNOWN"
	}
	if abbrev {
		if short, ok := phaseAbbrev[phase]; ok {
			label = short
		} else {
			label = "UNKN"
		}
	}
	return s.stateStyle(stateSession, phase).Bold(true).Render(label)
}

// faultAbbrev is faultText's short form of the two worktree-fault labels.
var faultAbbrev = map[string]string{
	brokenBadgeLabel: "BRKN",
	orphanBadgeLabel: "ORPH",
}

// faultText is phaseText's counterpart for a fault row's PHASE column. Like
// faultBadge it returns "" for a label it does not hold rather than falling
// back to a phase hue (see faultBadges' comment for why).
//
// The colour is the fault's §3.6 colour from stateLook (BROKEN broke, ORPHAN
// is yours to do); faultBadgesPlain is only asked whether the label is one of
// the two.
func (s styleSet) faultText(label string, abbrev bool) string {
	if _, ok := s.faultBadgesPlain[label]; !ok {
		return ""
	}
	st := s.stateStyle(stateFault, label).Bold(true)
	if abbrev {
		label = faultAbbrev[label]
	}
	return st.Render(label)
}

// labelBorders writes title into a rendered box's top edge and hint into its
// bottom edge — "╭─ Sessions ────╮" / "╰──── pgup/pgdn ─╯" — and returns the
// box otherwise untouched. lipgloss v2.0.6 has no border-label API (its Style
// draws the edges in renderHorizontalEdge and offers no hook), so this
// rebuilds the two edge lines from the style's own border glyphs and colours
// (GetBorderStyle, GetBorderTopForeground, GetBorderBottomForeground) after
// the box has been rendered.
//
// It must run AFTER any .MaxHeight: lipgloss applies MaxHeight after the
// border, so on an overflowing body the last line is a content row and the
// bottom edge is gone. The bottom label is written only when the last line
// really is the bottom edge (it starts with the style's BottomLeft glyph);
// the top edge is always line 0. Both lines keep their original width to the
// cell, which every box-geometry test in this package measures.
//
// Either label may be empty, in which case that edge is left as rendered. An
// empty box (no lines) is returned as is.
//
// This is labelBordersStyled with titleStyle fixed to theme.boxTitle, which
// is what every caller but renderList's session box wants. Delegating rather
// than duplicating keeps those callers (the detail pane, the switcher and
// form modals) rendering byte-identically to before renderList
// grew its own, stronger title style.
func labelBorders(box string, s lipgloss.Style, title, hint string) string {
	return labelBordersStyled(box, s, title, hint, theme.boxTitle)
}

// labelBordersStyled is labelBorders with the title's style taken as a
// parameter instead of hardcoded, so a caller that wants its label to carry
// more visual weight than the shared theme.boxTitle (renderList's active-
// project label, styled boxTitleStrong) can opt in without touching the
// function every other box title flows through.
func labelBordersStyled(box string, s lipgloss.Style, title, hint string, titleStyle lipgloss.Style) string {
	lines := strings.Split(box, "\n")
	if len(lines) == 0 || lines[0] == "" {
		return box
	}
	b := s.GetBorderStyle()
	width := lipgloss.Width(lines[0])
	if title != "" {
		lines[0] = paintEdge(b.TopLeft, b.Top, b.TopRight, titleStyle.Render(title), width,
			s.GetBorderTopForeground(), lipgloss.Left)
	}
	if hint != "" {
		last := len(lines) - 1
		if strings.HasPrefix(xansi.Strip(lines[last]), b.BottomLeft) {
			lines[last] = paintEdge(b.BottomLeft, b.Bottom, b.BottomRight, theme.dim.Render(hint), width,
				s.GetBorderBottomForeground(), lipgloss.Right)
		}
	}
	return strings.Join(lines, "\n")
}

// paintEdge draws one horizontal box edge of exactly width cells: corner,
// fill, a label set into the fill with one fill glyph and one space on either
// side, more fill, corner. The label is ANSI-truncated to what fits; when
// fewer than two cells remain for it the edge is drawn plain. align puts the
// label at the left (a title) or the right (a hint) end.
func paintEdge(left, fill, right, label string, width int, fg color.Color, align lipgloss.Position) string {
	edge := lipgloss.NewStyle().Foreground(fg)
	inner := width - lipgloss.Width(left) - lipgloss.Width(right)
	if inner < 0 {
		inner = 0
	}
	// Room for the label: the inner run minus one fill glyph and one space on
	// each side of it.
	room := inner - 4
	if room < 2 {
		return edge.Render(left + strings.Repeat(fill, inner) + right)
	}
	label = truncate(label, room)
	rest := inner - lipgloss.Width(label) - 2 // the two spaces around the label
	if align == lipgloss.Right {
		return edge.Render(left+strings.Repeat(fill, rest-1)) + " " + label + " " + edge.Render(fill+right)
	}
	return edge.Render(left+fill) + " " + label + " " + edge.Render(strings.Repeat(fill, rest-1)+right)
}

// boxBorderCells is the width and the height a single-cell border adds to a
// box: one cell on each of the four sides.
const boxBorderCells = 2

// sizedBox sets a bordered style's block size the way lipgloss v1 did: w and h
// are the CONTENT-plus-padding size with the border excluded, and the rendered
// box comes out boxBorderCells wider and taller than that.
//
// It exists because lipgloss v2 changed the meaning of Width/Height on a
// bordered style. v2 subtracts the border from the block before wrapping and
// aligning ("Include borders in block size", lipgloss/v2@v2.0.6 style.go:407-409);
// v1's style.go has no such lines, so the same argument produced a box two cells
// wider and two rows taller. Every geometry helper in this package —
// paneContentDims, sidebarRenderedWidth, layoutDims' body split — was measured
// against v1's rendering, so handing v2 a v1 value silently shrinks the frame:
// the dashboard renders 22 rows in a 24-row terminal and the pane box starts two
// columns left of where routeMouse computes it, which reads as mouse events
// landing on the wrong cell rather than as a layout change.
//
// MaxWidth/MaxHeight are deliberately not folded in here: v2 applies both after
// the border (style.go:505-521, same order as v1), so their arguments are outer
// sizes and did not change.
func sizedBox(s lipgloss.Style, w, h int) lipgloss.Style {
	return s.Width(w + boxBorderCells).Height(h + boxBorderCells)
}
