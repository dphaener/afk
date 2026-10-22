// parts.go holds the drawing parts of docs/dash-design.md §3: the top bar,
// the status bar, panes with their title strips, sections, rows, group
// headings, progress bars, bands, key chips and alert bars, plus the one
// mapping from a state to its glyph, colour and word (§3.6).
//
// Each part is a method on styleSet, so it draws with whichever theme the set
// was built from (the package theme on screen, any theme in a test) and two
// parts can never disagree on what a token looks like. Every part takes the
// width it must fill and returns exactly that many cells per row: the screens
// are laid out by width arithmetic, and a part that came out a cell short
// would leave a hole in the ground and shift everything after it.
//
// Long text is cut with "…" (truncate, x/ansi's Truncate), never wrapped: a
// part is a fixed number of rows.
package ui

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// tok turns a theme token's hex into a colour lipgloss can paint.
func tok(hex string) color.Color { return lipgloss.Color(hex) }

// fillLine lays line on st's background across exactly width cells: cut to
// width, padded to width, and the background carried across every inner SGR
// reset by spanStyle (without it a fill stops at the first styled word).
func fillLine(line string, st lipgloss.Style, width int) string {
	if width <= 0 {
		return ""
	}
	line = truncate(line, width)
	return st.Width(width).Render(spanStyle(line, st))
}

// spread puts left at the start and right at the end of width cells. When
// both do not fit, right wins and left is cut with "…", because the right side
// of every part that uses this (a count, a context, the keys) is the short
// part that says what to do, and the left side is the long one.
func spread(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	rw := lipgloss.Width(right)
	if rw >= width {
		return truncate(right, width)
	}
	room := width - rw
	if right != "" {
		room-- // keep one cell between the two sides
	}
	left = truncate(left, max(room, 0))
	gap := width - lipgloss.Width(left) - rw
	return left + strings.Repeat(" ", max(gap, 0)) + right
}

// topBar is §3.1's top row: left at the start and right at the end, across
// exactly width cells. It does no narrowing of its own: the dashboard's header
// (headerLine, views.go) already decides which pieces survive a narrowing
// terminal, in an order the owner knows (docs/dashboard.md), and hands topBar
// the halves that fit; topBar only cuts the left half with "…" when even they
// do not fit. topBarPath builds the left half the design describes (the badge,
// then where you are).
func (s styleSet) topBar(left, right string, width int) string {
	return spread(left, right, width)
}

// topBarPath is a top bar's left half (§3.1): the badge (on-accent on accent),
// then where you are as a path in muted with the last part in bold fg,
// separated by " ▸ ".
func (s styleSet) topBarPath(badge string, path []string) string {
	muted := lipgloss.NewStyle().Foreground(tok(s.tokens.Muted))
	last := lipgloss.NewStyle().Foreground(tok(s.tokens.Fg)).Bold(true)
	parts := make([]string, len(path))
	for i, p := range path {
		if i == len(path)-1 {
			parts[i] = last.Render(p)
		} else {
			parts[i] = muted.Render(p)
		}
	}
	left := s.header.Render(badge)
	if len(parts) > 0 {
		left += " " + strings.Join(parts, muted.Render(" ▸ "))
	}
	return left
}

// keyHint is one key on the status bar: the key as it is pressed and the word
// for what it does.
type keyHint struct {
	key, word string
}

// statusBar is §3.2's last row: filled with strip edge to edge across
// exactly width cells, left (the most urgent thing, or a notice) at the start
// and right (the keys, statusKeys) at the end.
//
// It has no padding cell of its own: the dashboard's footer (renderFooter,
// views.go) lays its help line as the left half, and that line has always
// started in the screen's first column, where the owner's eye already looks
// for the keys. The keys move to the right half when the status bar gains its
// urgent message (a later M16 task).
func (s styleSet) statusBar(left, right string, width int) string {
	return fillLine(spread(left, right, width), s.stripStyle(), width)
}

// statusKeys lays keys out as the status bar's right half: each key in bold
// accent followed by its word, two spaces between keys.
func (s styleSet) statusKeys(keys []keyHint) string {
	key := lipgloss.NewStyle().Foreground(tok(s.tokens.Accent)).Bold(true)
	hints := make([]string, len(keys))
	for i, k := range keys {
		hints[i] = key.Render(k.key) + " " + k.word
	}
	return strings.Join(hints, "  ")
}

// stripStyle is the strip fill with strip-fg text: the status bar's, the
// band's and an unfocused title strip's ground.
func (s styleSet) stripStyle() lipgloss.Style {
	return lipgloss.NewStyle().Background(tok(s.tokens.Strip)).Foreground(tok(s.tokens.StripFg))
}

// titleStrip is a pane's first row (§3.3): the name on the left in bold, the
// context on the right, one cell of padding at each end. Unfocused it is
// strip-fg on strip; the pane with the keyboard wears on-accent on accent, so
// the eye finds it without reading.
//
// When the two do not both fit, the context gives way whole and then the name
// is cut with "…" — the reverse of spread's rule, because a pane's name is
// what says which pane this is (the session list's strip is the one place on
// the frame that names the project), and the context (a paging hint, a focus
// hint) is said again elsewhere.
func (s styleSet) titleStrip(name, context string, width int, focused bool) string {
	st := s.titleStripStyle(focused)
	if width < 2 {
		return fillLine("", st, width)
	}
	inner := width - 2
	if context != "" && lipgloss.Width(name)+1+lipgloss.Width(context) > inner {
		context = ""
	}
	return fillLine(" "+spread(truncate(name, inner), context, inner)+" ", st, width)
}

// titleStripStyle is a title strip's fill and text: bold strip-fg on strip,
// or bold on-accent on accent for the pane with the keyboard.
func (s styleSet) titleStripStyle(focused bool) lipgloss.Style {
	if focused {
		return lipgloss.NewStyle().Bold(true).
			Background(tok(s.tokens.Accent)).Foreground(tok(s.tokens.OnAccent))
	}
	return s.stripStyle().Bold(true)
}

// paneStripRows is how many rows a pane's title strip takes: the first
// inner row, under the top border (§3.3). Every helper that sizes a pane's
// body (sizeSessionList, detailRowBudget, allProjectsBox, paneContentDims)
// takes it off the pane's inner height, so the rows they plan for are the
// rows pane draws.
const paneStripRows = 1

// pane is §3.3's box: a one-cell border (line, or accent when focused), the
// title strip on its first inner row, and body below it. w and h are the
// OUTER size, border included, and the box comes out exactly that size.
//
// The body is laid out at w-4 cells inside one cell of padding on each side,
// the padding §3.5 gives every row: a body row that fills its first or last
// cell (a selected row's sel) carries that fill out through the padding to
// the border, and every other cell is painted panel (paintRows). The strip
// spans the whole inner width with its own cell of padding, so the name
// starts in the column the body's text does.
//
// body is cut to the rows and the width that fit, so an overlong line cannot
// wrap inside the box and push it a row taller. A pane too small to hold a
// border comes back empty rather than as a broken box.
func (s styleSet) pane(name, context, body string, w, h int, focused bool) string {
	return s.framedPane(s.titleStrip(name, context, w-2, focused), body, w, h, focused)
}

// paneBorder is the border a pane wears: rounded, line, or accent for the
// pane with the keyboard. It matches the screens' own box styles
// (listBox, detailBox, paneBox and their focused pairs, styles.go) glyph for
// glyph and colour for colour, which is what lets renderDetail set its keys
// into the bottom edge with labelBorders and detailBoxStyle.
func (s styleSet) paneBorder(focused bool) lipgloss.Style {
	border := tok(s.tokens.Line)
	if focused {
		border = tok(s.tokens.Accent)
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(border)
}

// section is §3.4's pane inside a pane: the same box, with the section's
// number leading its name in the strip ("1 Sessions") so the number key that
// jumps to it is on screen. Same focus rule as a pane.
func (s styleSet) section(n int, name, context, body string, w, h int, focused bool) string {
	return s.pane(fmt.Sprintf("%d %s", n, name), context, body, w, h, focused)
}

// sectionTabs is §4.2's narrow section: below 100 columns one section fills
// the project view's body, and its strip names every section in order
// ("1 Sessions · 2 Build · 3 Inbox"), the one shown in bold and the others
// muted, so the digit that brings each one back is still on screen. current
// is the 0-based index of the section shown. Same box and focus rule as
// section.
func (s styleSet) sectionTabs(names []string, current int, context, body string, w, h int, focused bool) string {
	return s.framedPane(s.sectionTabsStrip(names, current, context, w-2, focused), body, w, h, focused)
}

// sectionTabsStrip is sectionTabs' strip: titleStrip's fill and padding with
// every section named. The shown name is bold; the others are muted on the
// strip fill, and faint on the accent fill, where muted would be a blue on a
// blue that cannot be read. When the names and the context do not both fit,
// the context gives way first, as titleStrip's does, and then the names are
// cut with "…".
func (s styleSet) sectionTabsStrip(names []string, current int, context string, width int, focused bool) string {
	st := s.titleStripStyle(focused).Bold(false)
	if width < 2 {
		return fillLine("", st, width)
	}
	other := lipgloss.NewStyle().Foreground(tok(s.tokens.Muted))
	if focused {
		other = lipgloss.NewStyle().Faint(true)
	}
	parts := make([]string, len(names))
	plain := 0
	for i, n := range names {
		label := fmt.Sprintf("%d %s", i+1, n)
		plain += lipgloss.Width(label)
		if i == current {
			parts[i] = lipgloss.NewStyle().Bold(true).Render(label)
		} else {
			parts[i] = other.Render(label)
		}
	}
	const sep = " · "
	plain += len(sep) * max(len(names)-1, 0)
	inner := width - 2
	if context != "" && plain+1+lipgloss.Width(context) > inner {
		context = ""
	}
	return fillLine(" "+spread(truncate(strings.Join(parts, sep), inner), context, inner)+" ", st, width)
}

// readOnlyPane is §3.4's read-only pane (the selected session's summary): a
// plain strip with no fill and muted text, a rule in line under it, then the
// body. It never takes the keyboard, so it has no focused form.
func (s styleSet) readOnlyPane(name, context, body string, w, h int) string {
	inner := w - 2
	if inner < 2 {
		return s.framedPane("", "", w, h, false)
	}
	muted := lipgloss.NewStyle().Foreground(tok(s.tokens.Muted))
	rule := lipgloss.NewStyle().Foreground(tok(s.tokens.Line)).Render(strings.Repeat("─", inner))
	strip := muted.Render(" "+spread(name, context, inner-2)+" ") + "\n" + rule
	return s.framedPane(strip, body, w, h, false)
}

// framedPane draws the border around head (one or more full-width rows) and
// body, the body cut and padded to what is left and laid inside one cell of
// padding (paneBody).
func (s styleSet) framedPane(head, body string, w, h int, focused bool) string {
	inner, rows := w-2, h-2
	if inner < 1 || rows < 1 {
		return ""
	}
	headRows := 0
	if head != "" {
		headRows = min(lipgloss.Height(head), rows)
		head = strings.Join(strings.Split(head, "\n")[:headRows], "\n")
	}
	content := head
	if bodyRows := rows - headRows; bodyRows > 0 {
		b := s.paneBody(body, inner, bodyRows)
		if content != "" {
			content += "\n" + b
		} else {
			content = b
		}
	}
	return s.paneBorder(focused).Render(content)
}

// paneBody lays body out as rows lines of exactly inner cells: each line cut
// to inner-2 and set inside one cell of padding on each side, short bodies
// padded with blank rows, then painted (paintRows) so a filled row reaches
// the border and every other cell is panel. Blank rows are padded here
// rather than by a style's Height, which would add bare rows after the
// painting and leave the bottom of a short list on the terminal's background.
// A pane too narrow for the padding gets its lines cut to inner instead.
func (s styleSet) paneBody(body string, inner, rows int) string {
	lines := strings.Split(body, "\n")
	if body == "" {
		lines = nil
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	if inner < 3 {
		for i, ln := range lines {
			lines[i] = padRight(truncate(ln, inner), inner)
		}
		return strings.Join(lines, "\n")
	}
	for i, ln := range lines {
		lines[i] = " " + padRight(truncate(ln, inner-2), inner-2) + " "
	}
	return s.paintRows(strings.Join(lines, "\n"))
}

// paintRows gives each row's first and last cell (the padding paneBody added)
// the background of the cell beside it, then paints panel on every cell still
// without a background, flat sets excepted. It works on cells for
// fillBackground's reason (theme.go): only the cells know which background
// each one wears once the row's SGR resets are in.
func (s styleSet) paintRows(block string) string {
	w, h := lipgloss.Width(block), lipgloss.Height(block)
	if w < 3 || h == 0 {
		return block
	}
	canvas := lipgloss.NewCanvas(w, h)
	canvas.Compose(lipgloss.NewLayer(block))
	panel := tok(s.tokens.Panel)
	for y := 0; y < h; y++ {
		if c, in := canvas.CellAt(0, y), canvas.CellAt(1, y); c != nil && in != nil && in.Style.Bg != nil {
			c.Style.Bg = in.Style.Bg
		}
		// The cell before the last can be a wide character's continuation
		// (Width 0); its background is the wide character's, one further left.
		in := canvas.CellAt(w-2, y)
		if in != nil && in.Width == 0 {
			in = canvas.CellAt(w-3, y)
		}
		if c := canvas.CellAt(w-1, y); c != nil && in != nil && in.Style.Bg != nil {
			c.Style.Bg = in.Style.Bg
		}
		if s.flat {
			continue
		}
		for x := 0; x < w; x++ {
			c := canvas.CellAt(x, y)
			if c == nil || c.Width == 0 {
				continue
			}
			if c.Style.Bg == nil {
				c.Style.Bg = panel
			}
		}
	}
	return canvas.Render()
}

// row is §3.5's list row, exactly width cells. Its cell of padding on each
// side is the pane's (paneBody), which takes the row's fill out to the
// border, so a row drawn here lines up with every other line of the pane's
// body.
//
// A selected row is drawn from its plain text (styles stripped) in the
// selection style, sel-fg on sel, filled edge to edge: a coloured word on it
// (a flag glyph, a state's colour) would otherwise fight the fill, and §3.5
// says every word on the selection turns sel-fg so the fill always reads. A
// linked row (linkedTo names the selection it belongs to) is filled with
// strip and ends with "◂ name" in accent. Selected outranks linked.
func (s styleSet) row(text string, width int, selected bool, linkedTo string) string {
	if width <= 0 {
		return ""
	}
	switch {
	case selected:
		return fillLine(xansi.Strip(text), s.rowSelected, width)
	case linkedTo != "":
		st := lipgloss.NewStyle().Background(tok(s.tokens.Strip))
		link := lipgloss.NewStyle().Foreground(tok(s.tokens.Accent)).Render("◂ " + linkedTo)
		return fillLine(spread(text, link, width), st, width)
	default:
		return padRight(truncate(text, width), width)
	}
}

// groupHeading is §3.5's group heading: bold, in the group's colour, a fold
// mark (▾ open, ▸ folded) before the label and the count at the right end.
func (s styleSet) groupHeading(label string, count int, tone stateTone, folded bool, width int) string {
	st := lipgloss.NewStyle().Foreground(s.toneColor(tone)).Bold(true)
	mark := "▾"
	if folded {
		mark = "▸"
	}
	inner := max(width-2, 0)
	if width <= 0 {
		return ""
	}
	return " " + st.Render(spread(mark+" "+label, fmt.Sprint(count), inner)) + " "
}

// progressBar is §3.7's bar: frac (0..1) of the bar in █ done, the rest ░
// faint, then label ("3/12", "62%") after one space, width cells in all. The
// label is kept whole and the bar takes what is left, because the numbers are
// what the owner reads; the bar is the at-a-glance part.
func (s styleSet) progressBar(frac float64, label string, width int) string {
	frac = math.Max(0, math.Min(1, frac))
	barW := width - lipgloss.Width(label) - 1
	if barW < 1 {
		return truncate(label, width)
	}
	full := int(math.Round(frac * float64(barW)))
	done := lipgloss.NewStyle().Foreground(tok(s.tokens.Done)).Render(strings.Repeat("█", full))
	rest := lipgloss.NewStyle().Foreground(tok(s.tokens.Faint)).Render(strings.Repeat("░", barW-full))
	return done + rest + " " + label
}

// progressCount is progressBar for done of total tasks, labelled "3/12". A
// total of 0 is an empty bar labelled "0/0", not a division by zero.
func (s styleSet) progressCount(done, total, width int) string {
	frac := 0.0
	if total > 0 {
		frac = float64(done) / float64(total)
	}
	return s.progressBar(frac, fmt.Sprintf("%d/%d", done, total), width)
}

// progressPercent is progressBar for a percentage (usage), labelled "62%".
func (s styleSet) progressPercent(pct, width int) string {
	return s.progressBar(float64(pct)/100, fmt.Sprintf("%d%%", pct), width)
}

// band is §3.7's one row filled with strip that sums up a screen, text in
// strip-fg with a cell of padding each side.
func (s styleSet) band(text string, width int) string {
	st := lipgloss.NewStyle().Background(tok(s.tokens.Strip)).Foreground(tok(s.tokens.StripFg))
	return fillLine(" "+truncate(text, max(width-2, 0))+" ", st, width)
}

// keyChip is §3.7's key chip: the key in bold on-accent on accent with a cell
// of padding each side, then the action in muted.
func (s styleSet) keyChip(key, action string) string {
	k := lipgloss.NewStyle().Bold(true).
		Foreground(tok(s.tokens.OnAccent)).Background(tok(s.tokens.Accent)).
		Padding(0, 1)
	return k.Render(key) + " " + lipgloss.NewStyle().Foreground(tok(s.tokens.Muted)).Render(action)
}

// alertBar is §3.7's alert bar: a full-width row in need with bold on-accent
// text, for the one thing on a screen that is waiting on you.
func (s styleSet) alertBar(text string, width int) string {
	st := lipgloss.NewStyle().Bold(true).
		Foreground(tok(s.tokens.OnAccent)).Background(tok(s.tokens.Need))
	return fillLine(" "+truncate(text, max(width-2, 0))+" ", st, width)
}

// stateTone is a state's meaning in §3.6, which picks its glyph and colour.
// The zero value is quiet, so a state nobody mapped draws as the quiet ○ in
// muted rather than as something alarming.
type stateTone int

const (
	toneQuiet stateTone = iota // ○ muted: paused, pending, nothing ready
	toneNeed                   // ● need: needs you
	toneBad                    // ✕ bad: something broke
	toneWork                   // ◐ work: afk is working
	toneDone                   // ✓ done
)

// toneGlyph is each tone's glyph (§3.6).
var toneGlyph = map[stateTone]string{
	toneQuiet: "○",
	toneNeed:  "●",
	toneBad:   "✕",
	toneWork:  "◐",
	toneDone:  "✓",
}

// toneColor is each tone's colour token in this set's theme.
func (s styleSet) toneColor(t stateTone) color.Color {
	switch t {
	case toneNeed:
		return tok(s.tokens.Need)
	case toneBad:
		return tok(s.tokens.Bad)
	case toneWork:
		return tok(s.tokens.Work)
	case toneDone:
		return tok(s.tokens.Done)
	default:
		return tok(s.tokens.Muted)
	}
}

// stateKind says which vocabulary a state value comes from, because the same
// word can mean different things in two of them ("paused" is a usage pause on
// a session and an owner's pause on a project; both are quiet today, but the
// tables stay apart so one can change without moving the other).
type stateKind int

const (
	// stateSession is a session's lifecycle phase (state.Phase values), the
	// one-off and pending stand-ins the session list shows in its place, and
	// awaitingInputState.
	stateSession stateKind = iota
	// stateFault is a fault label from the session list's PHASE column
	// (STALE, BROKEN, ORPHAN, UNKNOWN).
	stateFault
	// stateProject is a project row's state label (projectrows.State values),
	// plus the words the all-projects screen will show for a build.
	stateProject
)

// awaitingInputState is the stateSession value for a session whose agent is
// asking the owner something (status.json's awaiting_input). It is not a
// phase: a session awaits input in whatever phase it is in, and the caller
// asks for this look when awaiting outranks the phase.
const awaitingInputState = "awaiting input"

// hostRunningState and hostStoppedState are the stateSession values for a
// session's host, live or not. Like awaitingInputState they are not phases:
// the host column shows them beside whatever phase the session is in.
const (
	hostRunningState = "host running"
	hostStoppedState = "host stopped"
)

// stateAppearance is how one state is drawn: its glyph, its tone (the colour,
// through toneColor) and the word that goes with it.
type stateAppearance struct {
	glyph string
	tone  stateTone
	word  string
}

// look makes the table rows short: the glyph always follows from the tone.
func look(t stateTone, word string) stateAppearance {
	return stateAppearance{glyph: toneGlyph[t], tone: t, word: word}
}

// stateLooks is the ONE table behind §3.6 ("the mapping … lives in one
// function, not at each call site"). Keys are lower-case; the words are the
// words each screen shows for the state today, so drawing through this table
// changes no text on screen.
var stateLooks = map[stateKind]map[string]stateAppearance{
	stateSession: {
		"planning":         look(toneWork, "planning"),
		"executing":        look(toneWork, "executing"),
		"reviewing":        look(toneWork, "reviewing"),
		"fixing":           look(toneWork, "fixing"),
		"done":             look(toneDone, "done"),
		"failed":           look(toneBad, "failed"),
		awaitingInputState: look(toneNeed, "awaiting input"),
		// A one-off session has no lifecycle; §3.6 lists one-off as quiet.
		scratchPhaseKey: look(toneQuiet, scratchPhaseKey),
		// A session the usage governor paused, and one `afk new` is still
		// creating: both quiet, nothing for the owner to do.
		"paused":     look(toneQuiet, "paused"),
		pendingBadge: look(toneQuiet, pendingBadge),
		// A session's host: running is afk at work, stopped is quiet (a host
		// stops when its session ends, which is not a fault). The words are
		// the ones the detail header shows beside the glyph.
		hostRunningState: look(toneWork, hostRunningState),
		hostStoppedState: look(toneQuiet, hostStoppedState),
		// An empty phase is a status file that could not be read (the
		// session list shows "unknown"): §3.6's "can't read", so it broke.
		"": look(toneBad, "unknown"),
	},
	stateFault: {
		"stale":   look(toneBad, "STALE"),
		"broken":  look(toneBad, brokenBadgeLabel),
		"unknown": look(toneBad, "UNKNOWN"),
		// A worktree with no afk session is not broken; it is the owner's
		// to deal with (§3.6's "yours to do"), as its badge colour already
		// says (styles.go faultBadges).
		"orphan": look(toneNeed, orphanBadgeLabel),
	},
	stateProject: {
		"gone":            look(toneBad, "gone"),
		"can't read":      look(toneBad, "can't read"),
		"stopped":         look(toneBad, "stopped"),
		"failed":          look(toneBad, "failed"),
		"waiting on you":  look(toneNeed, "waiting on you"),
		"plan to approve": look(toneNeed, "plan to approve"),
		"parked":          look(toneNeed, "parked"),
		"running":         look(toneWork, "running"),
		"executing":       look(toneWork, "executing"),
		"shipping":        look(toneWork, "shipping"),
		"finished":        look(toneDone, "finished"),
		"done":            look(toneDone, "done"),
		"paused":          look(toneQuiet, "paused"),
		"held for usage":  look(toneQuiet, "held for usage"),
		"nothing ready":   look(toneQuiet, "nothing ready"),
		"no plan":         look(toneQuiet, "no plan"),
		"pending":         look(toneQuiet, "pending"),
		"one-off":         look(toneQuiet, "one-off"),
	},
}

// stateLook is the §3.6 mapping: the glyph, tone and word for value in kind's
// vocabulary. Matching ignores case and surrounding space (the session list
// writes faults in capitals, the tables key them in lower case). A value the
// table does not hold draws as the quiet ○ with the value itself as its word:
// a new state a newer afk writes should show up plainly, not crash a render or
// borrow an alarming colour.
func stateLook(kind stateKind, value string) stateAppearance {
	key := strings.ToLower(strings.TrimSpace(value))
	if a, ok := stateLooks[kind][key]; ok {
		return a
	}
	return look(toneQuiet, strings.TrimSpace(value))
}

// renderState draws a state's glyph and word in its colour ("◐ executing").
func (s styleSet) renderState(kind stateKind, value string) string {
	a := stateLook(kind, value)
	st := lipgloss.NewStyle().Foreground(s.toneColor(a.tone))
	if a.word == "" {
		return st.Render(a.glyph)
	}
	return st.Render(a.glyph + " " + a.word)
}

// renderGlyph draws a state's glyph alone in its colour, for the places that
// have one cell for it (a session row's host column).
func (s styleSet) renderGlyph(kind stateKind, value string) string {
	return s.stateStyle(kind, value).Render(stateLook(kind, value).glyph)
}

// stateStyle is the foreground a state is drawn in, for a caller that draws
// the state's word in a form of its own (the PHASE column's capitals).
func (s styleSet) stateStyle(kind stateKind, value string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(s.toneColor(stateLook(kind, value).tone))
}
