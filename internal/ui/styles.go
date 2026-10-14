// styles.go holds every lipgloss style used by the dashboard. Styles are pure
// values (no I/O, no terminal probing beyond lipgloss's own color-profile
// detection at render time), so they live in one place and are shared by the
// list view, the detail panels, and the chrome (header/help/status lines).
//
// Colors are adaptive where it matters so the dashboard is legible on both
// light and dark terminals. Nothing here reads or writes files or processes.
package ui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	xansi "github.com/charmbracelet/x/ansi"

	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/palette"
)

// styleSet is the full palette of styles the views draw from. A single
// package-level instance (theme) is built once at init; it carries no mutable
// state, so sharing it across the model is safe.
type styleSet struct {
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

// theme is the shared style set, built for a dark terminal until the terminal
// reports otherwise (applyTheme). It is a package variable rather than a Model
// field because every renderer in the package reads it, including pure
// helpers with no Model in reach; the one writer is Update's
// tea.BackgroundColorMsg case, on the program's single goroutine.
var theme = newStyleSet(palette.Dark)

// applyTheme rebuilds theme for a light or dark terminal background. Called
// once, when the terminal answers tea.RequestBackgroundColor; a terminal that
// never answers keeps the dark set, which is the one the palette was drawn for.
func applyTheme(isDark bool) {
	theme = newStyleSet(palette.For(isDark))
}

// scratchPhaseKey is the pseudo-phase key phaseBadge/phaseBadgePlain look up
// for a scratch session (discover.SessionState.Scratch): it can't collide
// with a real state.Phase value (planning/executing/reviewing/fixing/done/
// failed), so it shares the same map and helpers without a separate code path.
const scratchPhaseKey = "scratch"

func newStyleSet(p palette.Scheme) styleSet {
	badge := func(fg, bg string) lipgloss.Style {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(fg)).
			Background(lipgloss.Color(bg)).
			Bold(true).
			Padding(0, 1)
	}
	// badgePlain is the unpadded, backgroundless fallback: same hue family as
	// the padded badge (its bg color reused as the plain fg), just narrower.
	badgePlain := func(fg string) lipgloss.Style {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(fg)).
			Bold(true)
	}
	// toastBoxOf builds one kind's toast border: rounded and padded like
	// every other bordered style in this set, differing only in the colour
	// that carries the kind.
	toastBoxOf := func(border string) lipgloss.Style {
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(lipgloss.Color(border)).
			Padding(0, 1)
	}

	return styleSet{
		header: lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(p.White)).
			Background(lipgloss.Color(p.HeaderAccent)).
			Padding(0, 1),
		headerCount: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
		errorText:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.Error)).Bold(true),

		listBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(lipgloss.Color(p.Border)).
			Padding(0, 1),
		listBoxFocused: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(lipgloss.Color(p.HeaderAccent)).
			Padding(0, 1),
		detailBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(lipgloss.Color(p.Border)).
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
			BorderForeground(lipgloss.Color(p.HeaderAccent)).
			Padding(0, 1),
		// modalBox is the floating overlay (modal.go). Its border is the accent
		// the focused surface wears (listBoxFocused, paneBoxFocused): a modal
		// owns the keyboard while it is up, and the border is how the frame
		// says so. Same padding as detailBox so contentW arithmetic is shared.
		modalBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(lipgloss.Color(p.HeaderAccent)).
			Padding(0, 1),
		// toastKinds gives renderToast a distinct look per notificationKind.
		// info gets the neutral border listBox/detailBox already wear (a
		// single warning-hue border was all the corner notice had before
		// kinds existed, back when severity was not a thing it could
		// express) and plain text, no marker — it is the zero value, so
		// every toast call written before kinds existed renders as this
		// entry. success/warn/error each pair a coloured border with the
		// matching bold text colour (the same fg-only, bold pairing
		// verdictOK/verdictBad already use for "this is a severity, not
		// decoration") and a marker glyph from the same set views.go's
		// task/verdict rows use (✓/✗) plus ⚠ for warn.
		toastKinds: [4]toastKindStyle{
			notifyInfo: {
				box:  toastBoxOf(p.Border),
				text: lipgloss.NewStyle(),
			},
			notifySuccess: {
				box:    toastBoxOf(p.Success),
				text:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.Success)).Bold(true),
				marker: "✓",
			},
			notifyWarn: {
				box:    toastBoxOf(p.Warning),
				text:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.Warning)).Bold(true),
				marker: "⚠",
			},
			notifyError: {
				box:    toastBoxOf(p.Error),
				text:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.Error)).Bold(true),
				marker: "✗",
			},
		},
		// The accent border (p.HeaderAccent — the same accent
		// internal/prompt's focused-field border and internal/session's arrow
		// use, per prompt/theme.go's doc) marks the surface that owns keyboard
		// input: paneBoxFocused while keys route to the hosted child,
		// listBoxFocused while they route to the dashboard, detailBoxFocused
		// (above) while they route to the focused detail pane. Blurred boxes
		// share p.Border, the same neutral gray as detailBox.
		// Focused/blurred pairs must keep identical border and padding
		// geometry — renderPane's wrap-width math (contentW + 2) assumes it.
		paneBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(lipgloss.Color(p.Border)).
			Padding(0, 1),
		paneBoxFocused: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(lipgloss.Color(p.HeaderAccent)).
			Padding(0, 1),
		// p.Emphasis is also panelTitle's color (below) — the pane's
		// title bar and the detail pane's section titles are the same kind of
		// thing (the name of an active content region), so they now share
		// one hue instead of the pane title carrying a one-off magenta ("213")
		// that meant nothing anywhere else in the palette.
		paneTitle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.Emphasis)),

		rowSelected: lipgloss.NewStyle().
			Foreground(lipgloss.Color(p.White)).
			Background(lipgloss.Color(p.SelectionBg)).
			Bold(true),
		rowNormal: lipgloss.NewStyle(),
		// rowCursor paints the selected row's left rail (cursorRail, views.go).
		// Foreground only: SGR is additive, so the rail's own sequence leaves
		// rowSelected's background in place under the half of the cell the
		// glyph does not fill, and a background here would only repeat it.
		// p.HeaderAccent is the accent the focused list box's border already
		// wears, so the rail and the frame around it agree on which colour
		// means "your keys go here".
		rowCursor:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.HeaderAccent)),
		sessionName: lipgloss.NewStyle().Bold(true),
		hostRunning: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Success)),
		hostStopped: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
		branch:      lipgloss.NewStyle().Foreground(lipgloss.Color(p.Branch)),
		awaiting: lipgloss.NewStyle().
			Foreground(lipgloss.Color(p.Black)).
			Background(lipgloss.Color(p.Warning)).
			Bold(true).
			Padding(0, 1),
		// awaitingFlag is the one-cell form of awaiting: the session row's flag
		// column has no room for a padded badge, and a foreground-only dot
		// keeps the column from bleeding a background into its neighbours.
		awaitingFlag:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.Warning)).Bold(true),
		columnCaption: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)).Bold(true),
		// boxTitle is the label labelBorders sets into a box's top edge. It is
		// brighter than the border it interrupts so the eye finds it, and bold
		// rather than underlined because an underline under a border glyph
		// row reads as a second rule.
		boxTitle: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Emphasis)).Bold(true),
		// boxTitleStrong is renderList's session-box title only: the active
		// project's name is now the frame's primary identifier (the one label
		// that says which project you are looking at), so it outweighs
		// boxTitle's accent hue rather than sharing the same weight as a
		// detail pane or a modal's title. A sibling style, not a change to
		// boxTitle itself, is what keeps every other labelBorders caller
		// rendering byte-identically.
		//
		// p.Strongest, NOT p.White. This label lands straight on the
		// terminal background with no background of its own, and
		// palette.Light.White is "#ffffff" — white-on-white, the frame's
		// primary identifier rendered invisible on a light terminal. Every
		// other p.White in this file pairs it with a Background
		// (header/HeaderAccent, rowSelected/SelectionBg) and is fine;
		// this one cannot. TestTitleStylesStayReadableOnBothSchemes pins it.
		boxTitleStrong: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Strongest)).Bold(true),
		staleText:      lipgloss.NewStyle().Foreground(lipgloss.Color(p.Error)).Italic(true),
		dim:            lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
		panelTitle:     lipgloss.NewStyle().Bold(true).Underline(true).Foreground(lipgloss.Color(p.Emphasis)),

		taskComplete:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.Success)),
		taskFailed:     lipgloss.NewStyle().Foreground(lipgloss.Color(p.Error)),
		taskPending:    lipgloss.NewStyle().Foreground(lipgloss.Color(p.Pending)),
		diffFileHeader: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Emphasis)).Bold(true),
		diffHunkHeader: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
		diffAdded:      lipgloss.NewStyle().Foreground(lipgloss.Color(p.Success)),
		diffRemoved:    lipgloss.NewStyle().Foreground(lipgloss.Color(p.Error)),
		waveHeading:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.Gold)),

		verdictOK:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.Success)).Bold(true),
		verdictBad: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Error)).Bold(true),

		statusLine: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Warning)),
		helpLine:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
		spinner:    lipgloss.NewStyle().Foreground(lipgloss.Color(p.HeaderAccent)),
		progressBlend: [2]color.Color{
			lipgloss.Color(p.HeaderAccent),
			lipgloss.Color(p.Success),
		},
		progressEmpty: lipgloss.Color(p.Pending),
		helpStyles: help.Styles{
			Ellipsis:       lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
			ShortKey:       lipgloss.NewStyle().Foreground(lipgloss.Color(p.Emphasis)).Bold(true),
			ShortDesc:      lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
			ShortSeparator: lipgloss.NewStyle().Foreground(lipgloss.Color(p.Border)),
			FullKey:        lipgloss.NewStyle().Foreground(lipgloss.Color(p.Emphasis)).Bold(true),
			FullDesc:       lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
			FullSeparator:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.Border)),
		},

		phaseBadges: map[string]lipgloss.Style{
			"planning":      badge(p.White, p.PhasePlanningBg),
			"executing":     badge(p.Black, p.PhaseExecutingBg),
			"reviewing":     badge(p.Black, p.PhaseReviewingBg),
			"fixing":        badge(p.White, p.PhaseFixingBg),
			"done":          badge(p.Black, p.PhaseDoneBg),
			"failed":        badge(p.White, p.PhaseFailedBg),
			scratchPhaseKey: badge(p.White, p.ScratchBg),
		},
		phaseUnknown: badge(p.White, p.Border),

		phaseBadgesPlain: map[string]lipgloss.Style{
			"planning":      badgePlain(p.PhasePlanningBg),
			"executing":     badgePlain(p.PhaseExecutingBg),
			"reviewing":     badgePlain(p.PhaseReviewingBg),
			"fixing":        badgePlain(p.PhaseFixingBg),
			"done":          badgePlain(p.PhaseDoneBg),
			"failed":        badgePlain(p.PhaseFailedBg),
			scratchPhaseKey: badgePlain(p.ScratchBg),
		},
		phaseUnknownPlain: badgePlain(p.Border),

		// Both hues are borrowed from the phase palette on purpose rather
		// than added to it: a fault row never carries a phase badge, so the
		// two can never appear on the same line and be confused for each
		// other. Red for a session whose worktree died under it, amber for a
		// worktree that was never afk's to begin with — the same
		// severity ordering the phase badges already use.
		faultBadges: map[string]lipgloss.Style{
			brokenBadgeLabel: badge(p.White, p.PhaseFailedBg),
			orphanBadgeLabel: badge(p.Black, p.Warning),
		},
		faultBadgesPlain: map[string]lipgloss.Style{
			brokenBadgeLabel: badgePlain(p.PhaseFailedBg),
			orphanBadgeLabel: badgePlain(p.Warning),
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

// faultBadge renders a worktree-side fault row's badge. Unlike phaseBadge it
// has no fallback and returns "" for a label it does not hold: its callers
// only ever pass the two constants above, and a silent fallback here would
// paint a fault row with a phase verdict (see faultBadges' own comment).
func (s styleSet) faultBadge(label string) string {
	st, ok := s.faultBadges[label]
	if !ok {
		return ""
	}
	return st.Render(label)
}

// faultBadgePlain is faultBadge's narrow-width form, on the same
// no-background/no-padding trade renderRow already makes for phase badges.
func (s styleSet) faultBadgePlain(label string) string {
	st, ok := s.faultBadgesPlain[label]
	if !ok {
		return ""
	}
	return st.Render(label)
}

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
// Unknown or empty phases render UNKNOWN / UNKN in the neutral hue, the same
// fallback phaseBadge makes.
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
	st, ok := s.phaseBadgesPlain[phase]
	if !ok {
		st = s.phaseUnknownPlain
	}
	return st.Render(label)
}

// faultAbbrev is faultText's short form of the two worktree-fault labels.
var faultAbbrev = map[string]string{
	brokenBadgeLabel: "BRKN",
	orphanBadgeLabel: "ORPH",
}

// faultText is phaseText's counterpart for a fault row's PHASE column. Like
// faultBadge it returns "" for a label it does not hold rather than falling
// back to a phase hue (see faultBadges' comment for why).
func (s styleSet) faultText(label string, abbrev bool) string {
	st, ok := s.faultBadgesPlain[label]
	if !ok {
		return ""
	}
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
