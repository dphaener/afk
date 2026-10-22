// views.go is the render layer: it turns the pure Model into a string for
// Bubble Tea to draw. It performs no I/O — every value it renders was placed
// on the Model by Update from a message. The detail pane is composed of
// ordered `panel`s (the seam W5.T4 extends with stack + diff panels).
package ui

import (
	"fmt"
	"github.com/dphaener/afk/internal/spice"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/usagetext"
)

// View renders the whole dashboard: header, then the list pane beside the
// detail pane, then the footer (the help line), with any fresh notification
// floated over it all as a toast.
//
// It also DECLARES the two terminal modes the dashboard runs in. Under Bubble
// Tea v2 the alternate screen and mouse reporting are fields on the returned
// tea.View rather than tea.Program options, so they are re-stated on every
// frame and the program follows whatever the latest frame says — which is what
// makes ctrl+g's runtime mouse toggle a plain state flip (toggleMouseCapture in
// model.go) with no Cmd to issue. They are set before the early returns so the
// modes survive the loading frame; nothing else about those frames changes.
//
// The terminal CURSOR is a third such per-frame declaration, set at the very
// bottom from the hosted child's own cursor. It is deliberately NOT set before
// the early returns: unlike the two modes, its default of nil is the correct
// answer for a loading or quitting frame (there is no pane to point into).
func (m Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	// MouseMode is the whole mouse feature's foothold: MouseModeCellMotion is
	// what asks the terminal for ?1002 + ?1006 (wheel notches, presses,
	// releases, drag motion — but not button-less pointer movement, which is
	// MouseModeAllMotion). MouseModeNone hands the mouse back to the host
	// terminal so its native selection works; mouseCapture tracks which.
	v.MouseMode = tea.MouseModeNone
	if m.mouseCapture {
		v.MouseMode = tea.MouseModeCellMotion
	}

	if m.quitting {
		return v
	}
	if !m.ready || m.width == 0 || m.height == 0 {
		v.SetContent("Loading afk dashboard…")
		return v
	}

	header := m.renderHeader()
	footer := m.renderFooter()
	listOuter, mainOuter, bodyHeight := m.layoutDims()

	// The body is the level under any Agent: All projects, the empty state
	// (which stands in for All projects with no project at all), or the
	// Project, which is the session list beside the overview. At Project the
	// right side ALWAYS draws the overview (W3.T1 of T16.2): the agent is a
	// level of its own, drawn over this frame below, so a pane connected for
	// the selected row stays connected behind the overview and is simply not
	// drawn until enter goes into it (A5).
	var body string
	// paneShown records whether the Agent below actually PAINTED the pane, so
	// the cursor block after SetContent cannot be reached from any other
	// branch. "m.pane != nil" is not the same question — a pane behind the
	// Project leaves the handle live while the overview is drawn.
	var paneShown bool
	if m.allProjectsOpen {
		// The all-projects screen takes the whole body, the list's and the
		// detail side's place alike, whether or not a project is active: it
		// lists the projects in config.toml, which a dashboard started
		// outside any project can still show. A pane stays connected behind
		// it and is simply not drawn, so no cursor is placed.
		body = m.renderAllProjects()
	} else if !m.hasActiveProject() {
		// hasActiveProject is false only for a launch outside any repo with no
		// scan result yet — zero known projects, nothing to list or show detail
		// for — so the whole two-region split is replaced by one explanatory
		// block. Full body width, matching layoutDims' own split.
		body = m.renderEmptyState(listOuter+mainOuter, bodyHeight)
	} else {
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			m.renderList(listOuter, bodyHeight), m.renderDetail(mainOuter, bodyHeight))
	}

	frame := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)

	// The Agent (§4.3) is a pane over the dimmed Project at §3.8's width
	// (agentBoxRect): the live pane, or the agent placeholder for a row whose
	// pane is not installed yet or was lost (A6). It is drawn only at the
	// Agent level, which is what the keyboard is on, so the frame and the
	// keys cannot disagree about whether the agent is in front.
	var agent string
	var agentX, agentY int
	if m.level() == levelAgent {
		x, y, w, h := agentBoxRect(m.width, lipgloss.Height(header), bodyHeight)
		agentX, agentY = x, y
		if m.shouldRenderPane() {
			agent = m.renderPane(w, h)
			paneShown = true
		} else {
			agent = m.renderAgentPlaceholder(w, h)
		}
	}

	// The overlays float over the finished frame (modal.go) rather than
	// replacing a region of it, so the list and the pane a person was reading
	// stay on screen behind the box. The order matches modePrecedence — the
	// `:` actions search outranks the keys overlay, which outranks the
	// switcher, which outranks the inbox, which outranks the plan check,
	// which outranks the tokens overlay, which outranks the form — so the
	// frame and the key routing agree on which surface is on top. They are
	// mutually exclusive in practice (each owns every key while it is up), so
	// this is fidelity to the precedence table, not a state a user can reach.
	// Whatever owns the keyboard must be ON SCREEN, and that holds in the
	// empty state too: a scan can empty the project list under an open
	// overlay, and a component that still routes `q` while invisible would be
	// a keyboard trap.
	var overlay string
	switch {
	case m.leaderOpen:
		// The `:` actions search outranks every overlay in modePrecedence. Its
		// view takes the terminal size and sizes its own box (leader.go), from a
		// snapshot of rows taken at open, so drawing it reads no file and
		// runs nothing.
		overlay = m.leader.view(m.width, m.height)
	case m.keysOpen:
		overlay = m.renderKeysModal()
	case m.switcherOpen:
		overlay = m.renderSwitcherModal()
	case m.inboxOpen:
		overlay = m.renderInboxModal()
	case m.planCheckOpen:
		overlay = m.renderPlanCheckModal()
	case m.taskTokensOpen:
		overlay = m.renderTaskTokensModal()
	case m.formOpen:
		overlay = m.renderNewFormModal()
	}
	d, dialogUp := m.activeDialog()
	// The key strip is not one of the centred overlays: it sits in the
	// bottom-right corner (keystrip.go), and it is shown only in the Agent,
	// where none of them is open.
	strip := overlay == "" && !dialogUp && m.keyStripShown()

	// Every layer under the top one is dimmed exactly once (§3.9). Over the
	// Agent that means the Project is dimmed when the agent goes on, and the
	// agent box itself is dimmed only by what is laid over it: the key strip
	// dims it the strip's lighter amount, an overlay or a dialog the full
	// one. The overlay then goes on without dimming the frame again.
	if agent != "" {
		boxDim := 0.0
		keepStatus := true
		switch {
		case overlay != "" || dialogUp:
			boxDim, keepStatus = overlayDim, false
		case strip:
			boxDim = keyStripDim
		}
		frame = composeAgent(frame, agent, agentX, agentY, boxDim, keepStatus, m.deps.ColorProfile)
		if overlay != "" {
			frame = layModal(frame, overlay, m.width, m.height)
		}
	} else if overlay != "" {
		frame = composeModal(frame, overlay, m.width, m.height, m.deps.ColorProfile)
	}
	if strip {
		frame = composeKeyStrip(frame, renderKeyStrip(m.leaderKey.name, m.width), m.width, m.height)
	}
	// A confirmation or prompt (m.modal) outranks every overlay in
	// modePrecedence, so it is composed last and lands on top — a create that
	// fails while the switcher is open reports over the switcher. Over an
	// overlay it dims only that overlay's box (composeModalOver): the screen
	// under both is already dimmed once. Over the Agent alone the agent box
	// was dimmed above, so the dialog goes on as it is.
	if dialogUp {
		switch {
		case overlay != "":
			frame = composeModalOver(frame, overlay, m.renderDialog(d), m.width, m.height, m.deps.ColorProfile)
		case agent != "":
			frame = layModal(frame, m.renderDialog(d), m.width, m.height)
		default:
			frame = composeModal(frame, m.renderDialog(d), m.width, m.height, m.deps.ColorProfile)
		}
	}
	// A fresh status floats above all of it (toast.go), and nothing keeps it
	// once the toast has gone — this is the only surface a notification has.
	if m.toastVisible() {
		frame = composeToast(frame, renderToast(m.statusKind, m.status, m.width), m.width)
	}
	// The ground goes on last, over the whole finished frame (§1): every cell
	// nothing else coloured gets the theme's bg, so the dashboard looks the
	// same in any terminal. Last, because the overlays and the toast above
	// are composited cell by cell and leave their own unpainted cells, which
	// need the ground as much as the frame's do.
	//
	// Each row is first padded out to the terminal's width. Every row leaves
	// its renderer full width, but the Compositor behind composeModal and
	// composeToast trims a row's bare trailing spaces (lipgloss Canvas.Render),
	// and paintGround paints a row only as far as it reaches; without the pad
	// the top bar and the status bar would stop short of the right edge
	// whenever an overlay or a toast is up. Not on a 16-colour terminal, where
	// there is no ground to paint and the blanks would only be more bytes.
	if paintsGround(m.deps.ColorProfile) {
		frame = padRows(frame, m.width)
	}
	frame = paintGround(frame, theme.tokens, m.deps.ColorProfile)

	v.SetContent(frame)
	// Terminal integration, for terminals that draw it: the tab title names
	// the project and how many sessions are waiting, and the tab's progress
	// bar tracks the fleet's task completion (toast.go's two helpers).
	v.WindowTitle = m.windowTitle()
	v.ProgressBar = m.terminalProgress()

	// The hardware cursor is a FIELD on the frame, not a glyph in the content:
	// leaving v.Cursor nil is the only vocabulary bubbletea has for "hide it"
	// (cursed_renderer.go:538-539 derives both showCursor and hideCursor from a
	// bare nil check), which is why the gate below never assigns at all in the
	// hiding cases, and why paneCursor returns nil rather than a hidden Cursor.
	//
	// The gate is the INPUT MODE, not m.focus, and the difference is the
	// decision this feature turns on (D4): the cursor marks where keystrokes
	// land. A footer modal leaves the pane fully drawn and still focused while
	// owning the keyboard, so the focus alone would blink a cursor inside a
	// session that is receiving nothing the user types. activeInputMode is the
	// same predicate routeMouse asks (model.go), for the same reason.
	//
	// The geometry is paneScreenGeometry — shared with routeMouse rather than
	// re-derived here — because the two translate across the same boundary in
	// opposite directions and must agree: a cursor drawn where a click does not
	// land would be a worse bug than the missing cursor this fixes.
	if paneShown && activeInputMode(m.modeSnapshot()) == inputPane {
		originX, originY, cols, rows := m.paneScreenGeometry()
		v.Cursor = paneCursor(m.pane.Cursor(), originX, originY, cols, rows, m.width, m.height)
	}
	return v
}

// padRows pads every row of frame shorter than width with spaces out to width,
// leaving longer rows alone (they are the renderer's to cut, not this one's).
func padRows(frame string, width int) string {
	lines := strings.Split(frame, "\n")
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w < width {
			lines[i] = ln + strings.Repeat(" ", width-w)
		}
	}
	return strings.Join(lines, "\n")
}

// paneForSelection reports whether a live pane belongs to the selected row:
// one is installed AND either the cursor is on its row or there is no
// selection at all (selected() → ok=false, reachable when the paned session's
// row leaves discovery and the list empties; without this the pane stops
// rendering while cleanRequested still blocks `c` pointing at an invisible
// pane). It is the "pane" half of the focus model — modeSnapshot copies it
// into modeState.pane — and it deliberately does not consult focus: a
// pane for a row the cursor has left is not the selection's pane whoever
// holds the keyboard, so the frame and the dispatch cannot disagree about
// which session is on screen.
func (m Model) paneForSelection() bool {
	if m.pane == nil {
		return false
	}
	s, ok := m.selected()
	if !ok {
		return true
	}
	return s.Name == m.paneName
}

// shouldRenderPane decides whether View draws the live pane: the right side
// has the keyboard in the agent view AND a pane belongs to the selected row.
// That is the Agent level with a pane (levelFacts). Since W3.T1 of T16.2 the
// focus is part of it: at Project the right side always draws the overview,
// and a pane connected for the row (a preview, or one left by going back)
// stays connected behind it without being drawn (A5).
//
// There is no "keyboard routed to the pane" clause keeping a pane drawn after
// a discovery tick moves the cursor off its row. The two-axis model cannot
// tell that case apart from enter pressed on a row whose pane is elsewhere,
// and the latter must not draw that other session; keeping the paned row
// selected across ticks is the pane-lifecycle work this derivation leaves to
// selection pinning rather than to a render-time exception.
func (m Model) shouldRenderPane() bool {
	return m.focus == focusRight && m.view == viewAgent && m.paneForSelection()
}

// detailDrawn is "the detail box is the surface in the main region": the
// single predicate `f` branches on (applyKeyAction's actionFocusPane case)
// and the gate that decides whether `f` is advertised at all
// (normalBindingEnabled). Defining it once is the point — three
// near-identical spellings of "the detail pane is drawn" is exactly the drift
// that leaves the key advertised where it does nothing.
//
// Both terms are load-bearing. !shouldRenderPane alone is NOT enough: it is
// false whenever pane == nil, so `m.pane != nil || !m.shouldRenderPane()`
// reduces to true and would advertise `f` over the empty state. View (above)
// draws the detail box under exactly this conjunction.
func (m Model) detailDrawn() bool {
	return m.detailInMain() && m.hasActiveProject()
}

// detailInMain is "neither the pane nor the agent placeholder is drawn": with
// either agent surface up (the Agent level) the overview is under the agent
// box, so there is no tab strip or overview to act on.
func (m Model) detailInMain() bool {
	return !m.shouldRenderPane() && !m.agentPlaceholderDrawn()
}

// agentPlaceholderDrawn reports whether the right side draws the agent
// placeholder: the agent view with no pane for the selected row, for a row
// that HAS an agent to show — running, or with a restart of ours in flight.
//
// A row with no live agent (stopped, host gone, pending, worktree fault,
// cleaning, finished) never gets the placeholder: the right side draws its
// OVERVIEW whatever the sticky view says, because "no agent running" is not
// something the user can read anything from, and the overview is. The view
// preference itself is left alone, so the next running row still shows its
// agent. `r` is how such a row gets an agent again (restartSelected), never
// enter.
//
// For a row that has an agent it is drawn only with the right side focused:
// the keyboard is on the agent surface, connecting or lost, which is the
// Agent level. With the list focused the Project draws the overview, whose
// summary says what the row's agent is doing (W3.T1 of T16.2: the Agent is a
// level of its own, so nothing on the Project stands in for it).
func (m Model) agentPlaceholderDrawn() bool {
	if m.focus != focusRight || m.view != viewAgent || m.paneForSelection() || !m.hasActiveProject() {
		return false
	}
	s, ok := m.selected()
	return !ok || m.rowHasAgent(s)
}

// agentCanRun is "this row has a live host an attach could reach".
func (m Model) agentCanRun(s Session) bool {
	return s.HostRunning && !s.Pending && !isWorktreeFault(s.Kind) && !m.cleaningSessions[cleanIDFor(s)]
}

// rowHasAgent is "the agent view has an agent to show for this row": a live
// host, or one this dashboard is restarting right now. It is the predicate
// that sends a row's right side to the overview (agentPlaceholderDrawn), stops
// enter from connecting it (focusRightSide), and makes ctrl+t inert on it
// (toggleView).
func (m Model) rowHasAgent(s Session) bool {
	return m.agentCanRun(s) || m.restartingRow(s.Name)
}

// restartingRow reports whether a host restart this dashboard dispatched for
// name, in the current project, has not reported back yet.
func (m Model) restartingRow(name string) bool {
	return name != "" && m.restartInFlight.name == name && m.restartInFlight.gen == m.projectGen
}

// agentPlaceholderText is the placeholder's title and its two lines: what the
// agent view is showing instead of an agent, and the action that changes
// that, empty where there is none. The order is the order of specificity: an
// outstanding restart or dial, then a lost connection, says more about the
// row than its discovered state does. Rows with no live agent never reach here (they draw
// the overview — see agentPlaceholderDrawn).
func (m Model) agentPlaceholderText() (title, line, action string) {
	s, ok := m.selected()
	if !ok {
		switch {
		case m.attachInFlight.name != "":
			return m.attachInFlight.name, "connecting to " + m.attachInFlight.name + "…", ""
		case m.restartingRow(m.restartInFlight.name):
			return m.restartInFlight.name, "restarting " + m.restartInFlight.name + "…", ""
		}
		return "Agent", "no session selected", ""
	}
	title = displayName(s)
	switch {
	case m.restartingRow(s.Name):
		return title, "restarting " + s.Name + "…", ""
	case m.attachInFlight.name == s.Name:
		// Ahead of the lost case: paneLost survives until the reconnect
		// installs, and while that dial is out enter only joins it, so
		// offering "enter to reconnect" would advertise a key that does
		// nothing new (placeholderConnectLive reads this action line).
		return title, "connecting to " + s.Name + "…", ""
	case s.Name == m.paneLost.name:
		return title, m.paneLost.message, "enter to reconnect"
	}
	return title, s.Name + " is running", "enter to connect"
}

// placeholderConnectLive is whether enter in the agent placeholder would
// start a new connect. It reads agentPlaceholderText's action line rather
// than re-deriving the states, so the footer and the box offer enter in
// exactly the same states: not while the row's connect or restart is already
// out (enter only joins it there), and not with no row selected.
func (m Model) placeholderConnectLive() bool {
	_, _, action := m.agentPlaceholderText()
	return action != ""
}

// renderAgentPlaceholder draws the agent view's stand-in: the pane's box and
// title line, so the view reads as the agent surface it replaces, with the
// state and the action in place of the emulator screen.
//
// It is the same pane part renderPane draws, in the same agent box, with the
// same title strip ("Agent · name" on the left), so switching between the
// agent and its placeholder moves nothing on screen. Its context names the
// Agent's way back and `d`, not "keys go to the agent": there is no agent
// here to take them, and enter and r act alone (placeholderKey, A6).
func (m Model) renderAgentPlaceholder(outerW, outerH int) string {
	contentW, _ := agentContentDims(outerW, outerH)
	title, line, action := m.agentPlaceholderText()
	hint := keyName(m.leaderKey.name, levelAgent, actionDetails) + " details" +
		" · " + keyName(m.leaderKey.name, levelAgent, actionGoBack) + " back"
	lines := []string{"", truncate(line, contentW)}
	if action != "" {
		lines = append(lines, theme.dim.Render(truncate(action, contentW)))
	}
	return theme.pane(paneStripName(title), hint, strings.Join(lines, "\n"),
		paneOuterWidth(contentW), outerH, true)
}

// agentFullWidthBelow is the terminal width below which the agent box takes
// the full width instead of leaving a column each side (§3.8): on a narrow
// terminal the two columns are worth more to the agent than the glimpse of
// the Project they would show.
const agentFullWidthBelow = 100

// agentBoxRect is the Agent's box on screen (§3.8: the screen less 1 column
// each side, the full width below agentFullWidthBelow columns), from the top
// bar's last row to the status bar: x and y are its top-left cell, w and h
// its outer size. headerRows and bodyHeight are layoutDims' rows, so the box
// covers exactly the body, and the top bar and the status bar stay readable
// around it. It is the ONE place the box's geometry is decided: View draws
// the box there, paneRegionSize sizes the PTY to its inside, and
// paneScreenGeometry places the mouse and the cursor in it.
func agentBoxRect(width, headerRows, bodyHeight int) (x, y, w, h int) {
	if width < agentFullWidthBelow {
		return 0, headerRows, max(width, 1), bodyHeight
	}
	return 1, headerRows, width - 2, bodyHeight
}

// layoutDims computes the dashboard body layout from the current terminal
// size: the sidebar (list) outer width, the main-region outer width, and the
// body height between the header and footer. It is the single source of truth
// for the split so View and the pane-geometry helpers agree exactly on the
// region the emulator is sized to.
func (m Model) layoutDims() (listOuter, mainOuter, bodyHeight int) {
	bodyHeight = m.height - lipgloss.Height(m.renderHeader()) - lipgloss.Height(m.renderFooter())
	if bodyHeight < 4 {
		bodyHeight = 4
	}
	listOuter = sidebarOuterWidthFor(m.width, m.sidebarPct)
	mainOuter = m.width - listOuter
	return listOuter, mainOuter, bodyHeight
}

// paneRegionSize returns the emulator's target size (cols x rows) for the
// current layout: the inside of the agent box (agentBoxRect), so openPaneCmd
// starts the child at exactly the geometry renderPane will draw it into, and
// relayout resizes the PTY to it on every terminal resize. A pane connected
// behind the Project is sized the same, so going into the Agent never
// resizes the agent's terminal.
func (m Model) paneRegionSize() (cols, rows int) {
	_, _, bodyHeight := m.layoutDims()
	_, _, w, h := agentBoxRect(m.width, 0, bodyHeight)
	return agentContentDims(w, h)
}

// mouseFrameLayout is layoutDims plus the two row counts layoutDims measures and
// then discards: headerRows (which is the pane box's Y) and frameRows (the height
// of the string View returns).
//
// It repeats layoutDims' arithmetic instead of calling it so that the header and
// the footer are each rendered exactly ONCE. Calling layoutDims and then measuring
// the header again — which is what routeMouse did before, for 3 renders on every
// mouse event — costs more, and a mouse event arrives per cell of movement. The
// duplication is pinned to layoutDims by TestMouseFrameLayoutAgreesWithLayoutDims,
// so the two cannot drift.
//
// frameRows is DERIVED from View's composition — JoinVertical(header, body,
// footer) — rather than measured from View's output, because Update must stay pure
// and may not render. bodyHeight's floor of 4 is what makes frameRows exceed
// m.height on a short terminal; the mouse path discards the value rather than
// correcting anything with it, because the rows that do not fit come off the
// BOTTOM (routeMouse's boxY comment cites the renderer). What it is for is the
// tests, which measure the real frame against it.
func (m Model) mouseFrameLayout() (listOuter, mainOuter, bodyHeight, headerRows, frameRows int) {
	headerRows = lipgloss.Height(m.renderHeader())
	footerRows := lipgloss.Height(m.renderFooter())
	bodyHeight = m.height - headerRows - footerRows
	if bodyHeight < 4 {
		bodyHeight = 4
	}
	listOuter = sidebarOuterWidthFor(m.width, m.sidebarPct)
	return listOuter, m.width - listOuter, bodyHeight,
		headerRows, headerRows + bodyHeight + footerRows
}

// paneContentDims converts a pane box's outer dimensions into the emulator's
// content size. Width: theme.paneBox's border (1 cell each side) and
// horizontal padding (Padding(0,1)) trim 4 cells, and a lipgloss block size
// INCLUDES padding — renderPane sizes the box at contentW+2 (via sizedBox, which
// adds the border on top) so its wrap point lands exactly at contentW. The
// emulator, the PTY, and the box's text-wrap width must all agree, or every
// full-width emulator row wraps and MaxHeight truncates the pane (the
// fix-cycle-2 geometry blocker). Height: the box border rows plus the pane's
// title strip (paneStripRows, the pane part's first inner row) trim the
// height. Both are floored at 1 (termpane requires a positive size).
func paneContentDims(outerW, outerH int) (cols, rows int) {
	cols = outerW - 6 // 2 border + 2 padding + 2 (Width() includes padding; wrap at Width-2)
	if cols < 1 {
		cols = 1
	}
	rows = outerH - boxBorderCells - paneStripRows
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// agentContentDims is the emulator's size inside the agent box (agentBoxRect)
// of outer size outerW×outerH: the box less its border and one cell of padding
// each side across, less the border rows and the title strip down. Unlike
// paneContentDims it gives back a box that is exactly outerW wide once
// paneOuterWidth adds the padding and border again, because the agent box's
// edges are §3.8's (the screen less one column each side) and must land on
// those columns. paneContentDims keeps afk attach's geometry, which its own
// tests pin; both are floored at 1 (termpane requires a positive size).
func agentContentDims(outerW, outerH int) (cols, rows int) {
	cols = max(outerW-2-boxBorderCells, 1)
	rows = max(outerH-boxBorderCells-paneStripRows, 1)
	return cols, rows
}

// paneOuterWidth is the outer width the pane part is handed for a body
// contentW cells wide: the body plus its cell of padding each side
// (paneBody) plus the border. It is the box the screens drew before they used
// the pane part (sizedBox(style, contentW+2, …), whose argument includes the
// padding), so sidebarRenderedWidth and every geometry helper measured
// against that box still hold.
func paneOuterWidth(contentW int) int {
	return contentW + 2 + boxBorderCells
}

// sidebarRenderedWidth returns how many cells the sidebar actually OCCUPIES for
// a given sidebar outer width — which is where the pane box begins, since View
// composes the body as JoinHorizontal(Top, list, main).
//
// It is sidebarContentWidth(outerW) + 4 — the content width plus the 2 padding
// cells the pane part lays around each row plus the 2 border cells
// (paneOuterWidth). That equals outerW itself except when sidebarContentWidth's floor of 10
// kicks in (outerW < 14), so it stays a call into the same arithmetic renderList
// uses rather than becoming `return outerW`. Measured
// lipgloss.Width(renderList(...)): outer 24 → 24, 28 → 28, 31 → 31, 35 → 35,
// 42 → 42; outer 12 → 14 (floored).
//
// It exists as a named helper with an equivalence test (mouse_test.go) rather
// than as arithmetic at the call site because a wrong value puts every
// forwarded mouse event cells away from where the user clicked — and callers
// must never re-render the sidebar to measure it, since Update may not render.
func sidebarRenderedWidth(outerW int) int {
	return sidebarContentWidth(outerW) + 4
}

// paneTitleBlock renders afk attach's pane title line: the session name, a
// hint, and nothing else, truncated to exactly one row. Until T16.1 it was the
// one place a pane title was produced for both front ends; the dashboard now
// draws its agent pane's title as the pane part's title strip (paneStrip),
// and this stays as attach's title, byte for byte.
//
// The hint is a parameter rather than a `focused bool` because there are three
// variants across two programs and only one of them is about focus: the
// dashboard's focused "[focused · ctrl+\ detach]", its unfocused focus-binding
// hint, and afk attach's unconditional "[ctrl+\ detach]".
// A bool would force attach to pass a meaningless value, and a mode enum would
// have to grow a case for every future hint.
//
// The truncation is load-bearing, not cosmetic. It is what makes
// paneContentOrigin's Y term true: the title is a single newline-free string, so
// lipgloss.Height reports 1 no matter how wide it is, while the BOX wraps it at
// render time and pushes the emulator's screen down a row per wrapped line.
// Measured with a 33-character session name, the dashboard's focused title is 62
// cells and wraps to 2 rows at a 90-column terminal and 3 at 60; attach's is 52
// cells (47 for the shorter name measured during planning) and wraps below a
// 58-column terminal — paneContentDims gives the box 6 fewer columns than the
// terminal, so 52 cells need 58. Cutting the string here means the rendered
// block and the measured block are the same one row.
func paneTitleBlock(name, hint string, contentW int) string {
	return theme.paneTitle.Render(truncate("▶ "+name+"  "+hint, contentW))
}

// paneFocusHint is the context on the Agent's title strip (§4.3): "keys go
// to the agent · ctrl+] esc back". It says the one thing a person in the
// Agent must know (every key is the agent's) and the way out, read from the
// key table (keyName), so it names the configured leader (T9.2). esc and
// ctrl+\ belong to the agent.
//
// attachDetachHint is afk attach's title hint while it holds the mouse
// (attachpane.go's titleBlock swaps in attachMouseReleasedHint when it does
// not); it stays a const and stays ctrl+\, because afk attach has no leader.
// TestPaneTitleBlockReproducesLegacyTitles pins it byte for byte.
func paneFocusHint(leader string) string {
	return "keys go to the agent · " + keyName(leader, levelAgent, actionGoBack) + " back"
}

const attachDetachHint = "[ctrl+\\ detach]"

// paneTitleHint is the agent pane's strip context. The pane is drawn only at
// the Agent level (shouldRenderPane), where it has the keyboard, so there is
// one hint; before T16.2's W3.T1 a pane drawn beside the list also had an
// unfocused one.
func (m Model) paneTitleHint() string {
	return paneFocusHint(m.leaderKey.name)
}

// focusBindingHint is the one display form for the normal-mode focus binding
// outside help.Model. The footer renders Binding.Help directly; the detail
// edge needs its own punctuation, so it
// call this helper or the same Help value instead of spelling a second key
// contract by hand.
func focusBindingHint(b key.Binding) string {
	h := b.Help()
	return "[" + h.Key + " " + h.Desc + "]"
}

// paneScreenGeometry returns the terminal cell the pane's emulator screen
// begins at and the size of the REGION renderPane draws it into. It is the one
// place the pane's on-screen origin is computed, and both consumers of that
// origin call it: routeMouse (model.go), which subtracts it to put an event in
// the child's coordinate space, and View, which adds it to put the child's
// cursor back in the frame's. Those two must agree cell for cell — a cursor
// that blinks where a click does not land is worse than either bug alone — and
// that is the same single-source argument paneContentOrigin's own doc makes one
// level down.
//
// rows/cols is the region, NOT the bounds the mouse path checks against: that
// path takes the per-axis minimum of this region and the emulator's own size
// (mouseBounds), which is a mouse-specific judgement about two untrustworthy
// sources and stays at its call site.
func (m Model) paneScreenGeometry() (originX, originY, cols, rows int) {
	_, _, bodyHeight, headerRows, _ := m.mouseFrameLayout()
	// The pane box is the agent box (agentBoxRect), the one View lays over
	// the Project: its X is the column §3.8 leaves free on each side.
	boxX, boxY, boxW, boxH := agentBoxRect(m.width, headerRows, bodyHeight)
	// The box's Y on SCREEN is its Y in the frame, with no correction for a frame
	// taller than the terminal — at heights 4-6 layoutDims' bodyHeight floor makes
	// the frame 7 rows, and the rows that do not fit come off the BOTTOM. Under
	// AltScreen, which View sets on every frame, the renderer pins the frame area
	// to Rect(0, 0, width, height) and draws the view into exactly that
	// (cursed_renderer.go:295, :345); ultraviolet's printString writes nothing
	// outside those bounds (styled.go:170-171), and the renderer's one
	// top-dropping branch (:349-351) guards on a frame height that under AltScreen
	// IS the terminal height, so it never fires. Subtracting a crop here would
	// move the origin ABOVE the visible pane content, which is the bug this
	// replaced: it accepted clicks on the visible help line and dropped them on
	// the visible pane. agentBoxRect's y is headerRows, uncorrected.
	//
	// agentContentDims over the box just computed is what paneRegionSize
	// returns, by construction — called directly here so this path measures
	// the layout ONCE. paneRegionSize would re-enter layoutDims and render the
	// header and footer a second time per event, and a drag delivers one event
	// per cell the pointer crosses.
	cols, rows = agentContentDims(boxW, boxH)

	// The origin must be measured from the SAME title strip renderPane's pane
	// draws (paneStrip), exactly one row — a raw title reports Height 1 while
	// a box would wrap it onto two or three rows, which would put every event
	// a row or more high. m.paneTitleHint() is the shared accessor renderPane
	// uses, so the two cannot pick different hints and disagree about the
	// strip's width. cols IS renderPane's contentW: both come from
	// agentContentDims over the agent box.
	originX, originY = paneContentOrigin(boxX, boxY,
		paneStrip(m.paneName, m.paneTitleHint(), cols, m.paneOwnsKeyboard()))
	return originX, originY, cols, rows
}

// paneStrip is the agent pane's title strip (§3.3) as the pane part draws it
// on the pane's first inner row: "Agent · name" on the left, the context on
// the right, accent while the agent has the keyboard. It spans the pane's whole
// inner width (contentW plus the two padding cells) with its own cell of
// padding, so "▶" stays in the column the emulator's rows start in.
//
// It stands in the row paneTitleBlock used to fill and keeps that row's
// one-row contract, because paneContentOrigin reads its height to place the
// emulator's first row; renderPane hands the pane part the same name and hint,
// so the strip measured here is the strip drawn. paneTitleBlock itself stays
// as it was: afk attach draws its own title with it.
func paneStrip(name, hint string, contentW int, focused bool) string {
	return theme.titleStrip(paneStripName(name), hint, paneOuterWidth(contentW)-boxBorderCells, focused)
}

// paneStripName is the agent pane's name as its strip shows it (§4.3):
// "Agent · <session>".
func paneStripName(name string) string {
	return "Agent · " + name
}

// renderPane draws the embedded terminal pane as the Agent's box
// (agentBoxRect, outerW×outerH): the title strip naming the hosted session
// above the emulator's current screen. The screen is already sized (via
// paneRegionSize) to the content area, so it is placed verbatim; the box pads
// to fill any slack.
func (m Model) renderPane(outerW, outerH int) string {
	contentW, _ := agentContentDims(outerW, outerH)

	screen := ""
	if m.pane != nil {
		screen = m.pane.Render()
	}

	// The pane part draws the title strip on the first inner row and lays the
	// screen out at exactly contentW — the emulator/PTY width from
	// paneContentDims — inside one cell of padding and the border, the
	// geometry sizedBox(paneBox, contentW+2, …) gave, so a full-width emulator
	// row neither wraps (which would double the line count) nor shifts the
	// origin routeMouse computes. It cuts the screen to the rows under the
	// strip, which is what MaxHeight used to bound.
	return theme.pane(paneStripName(m.paneName), m.paneTitleHint(), screen,
		paneOuterWidth(contentW), outerH, m.paneOwnsKeyboard())
}

// paneBoxStyle picks the pane border: accent while the child owns keyboard
// input, neutral otherwise.
func (m Model) paneBoxStyle() lipgloss.Style {
	if m.paneOwnsKeyboard() {
		return theme.paneBoxFocused
	}
	return theme.paneBox
}

// listBoxStyle picks the sidebar border: accent while the dashboard owns
// keyboard input — the list is focused and no creation form is open (the
// form owns input while active, so the sidebar goes neutral with it). Any
// right focus, pane or overview, turns it neutral: two boxes must never
// claim the keyboard at once (styles.go's accent convention doc).
func (m Model) listBoxStyle() lipgloss.Style {
	if !m.listOwnsKeyboard() {
		return theme.listBox
	}
	return theme.listBoxFocused
}

// detailBoxStyle picks the detail pane's border: accent while it owns
// keyboard input (detailOwnsKeyboard), neutral otherwise. Mirrors paneBoxStyle
// above; renderDetail hands the one returned style to both boxDetailBody
// (which colors all four border sides via sizedBox) and labelBorders (which
// repaints only the top/bottom edges FROM THE SAME STYLE) — a mismatched pair
// of calls here is exactly how an indicator ends up with an accent top/bottom
// and gray verticals (see boxDetailBody's doc).
func (m Model) detailBoxStyle() lipgloss.Style {
	if m.detailOwnsKeyboard() {
		return theme.detailBoxFocused
	}
	return theme.detailBox
}

// renderNewForm draws the in-TUI session-creation form (newform.go) in the main
// region: the name input, the description textarea, the base list, the
// Autonomous checkbox, the Provider selector, an inline
// validation/submission error when present, and a help line. Components are
// sized in openNewForm and on resize, NOT here — View takes a value receiver, so
// sizing applied here lands on a copy and is discarded. It reuses the existing
// detailBox/theme styles rather than introducing new ones. sizedBox gets
// contentW+2, not contentW: its argument includes detailBox's Padding(0, 1) —
// see renderSwitcher's doc for the full arithmetic.
func (m Model) renderNewFormModal() string {
	contentW, contentH := m.formModalDims()

	var b strings.Builder
	nameLabel := "Name:"
	if m.form.focusIndex == formFocusName {
		b.WriteString(theme.sessionName.Render(nameLabel))
	} else {
		b.WriteString(theme.dim.Render(nameLabel))
	}
	b.WriteString("\n")
	b.WriteString(m.form.nameInput.View())
	b.WriteString("\n\n")

	descLabel := "Description:"
	if m.form.focusIndex == formFocusDescription {
		b.WriteString(theme.sessionName.Render(descLabel))
	} else {
		b.WriteString(theme.dim.Render(descLabel))
	}
	b.WriteString("\n")
	b.WriteString(m.form.description.View())
	b.WriteString("\n\n")

	baseLabel := "Base:"
	if m.form.focusIndex == formFocusBase {
		b.WriteString(theme.sessionName.Render(baseLabel))
	} else {
		b.WriteString(theme.dim.Render(baseLabel))
	}
	b.WriteString("\n")
	b.WriteString(m.form.baseList.View())
	b.WriteString("\n\n")

	// The checkbox is its own label, so focus is shown the way the other
	// fields show it: the whole row takes the focused-label style.
	box := "[ ] "
	if m.form.autonomous {
		box = "[x] "
	}
	autoRow := box + "Autonomous"
	if m.form.focusIndex == formFocusAutonomous {
		b.WriteString(theme.sessionName.Render(autoRow))
	} else {
		b.WriteString(theme.dim.Render(autoRow))
	}
	b.WriteString("\n")

	// The selector is one row, label and value together, with the same
	// whole-row focus treatment as the checkbox. The ‹ › chevrons say the
	// value cycles; formComponentKey's provider arm is what cycles it.
	provRow := "Provider: ‹ " + m.form.providerName() + " ›"
	if m.form.focusIndex == formFocusProvider {
		b.WriteString(theme.sessionName.Render(provRow))
	} else {
		b.WriteString(theme.dim.Render(provRow))
	}

	// No trailing newline after the selector and no blank line before the
	// error: either one is a row formChromeRows would have to count (or
	// MaxHeight would clip the box's bottom border), and the error, which is
	// not counted, needs the one row of slack MaxHeight leaves it. It costs
	// the bottom border while it shows, as it always has.
	if m.form.err != "" {
		b.WriteString("\n")
		b.WriteString(theme.errorText.Render(m.form.err))
	}

	// The form's keys are on the footer's help line, which stays on screen
	// under the modal and already follows the active mode.
	return renderModalBox("New session", "", b.String(), contentW, contentH)
}

// renderSwitcher draws the project-switcher overlay into the MAIN region — the
// same region renderNewForm and renderPane take over, inside the existing
// two-region split, with no layout math of its own. The list was already sized to
// this space by switcherListSize (View cannot size it: its value receiver would
// throw the mutation away), so all that happens here is composition.
//
// contentW is the box's final usable text width — outerW less the border (2)
// and theme.detailBox's own Padding(0,1) (2) — and switcherListSize computes
// the SAME number for the list it sizes, so the two must never diverge. But
// sizedBox's argument is not that number: a lipgloss block size still includes
// padding (renderPane's contentW+2 does the identical thing for paneBox), so
// handing it contentW directly asks for a box whose wrap point sits 2 columns
// inside contentW — every row the list fills to width then wraps inside its
// own box. Passing contentW+2 puts the wrap point exactly at contentW, matching
// what the list was told its width is.
func (m Model) renderSwitcherModal() string {
	contentW, listH := m.switcherListSize()

	// The switcher's keys are on the footer's help line, which stays on screen
	// under the modal and already follows the active mode.
	return renderModalBox("Projects", "", m.switcher.View(), contentW, listH+switcherChromeRows)
}

// emptyStateConfigExample is the TOML the empty state shows the user to add to
// their config file, one entry per line. It names search_roots — the only key
// a first-time user needs — rather than the whole `[dash]` table
// (scan_interval, max_depth), which would bury the one line that unblocks
// them under keys they can leave at their defaults. The `[dash]` line is part
// of the example because search_roots outside that table is not read.
//
// It is a list of lines rather than one string so each line can be cut to the
// width on its own, and so a test can look for each line in the rendered
// frame (the block is centred, so the lines are never adjacent in the output).
var emptyStateConfigExample = []string{
	"[dash]",
	`search_roots = ["~/code"]`,
}

// genericConfigIntro is what renderEmptyState shows when configPath is still
// unset — the scan pipeline hasn't completed a pass yet (Init's batched scan
// Cmd hasn't resolved, or scanEnabled() is false). It deliberately names no
// path at all rather than a guessed default: the settings reader
// (internal/daemonconfig) resolves the path through XDG_CONFIG_HOME, which
// this render-only file cannot consult (and must not — see the package doc),
// so a hardcoded guess could disagree with the one the running binary
// actually reads.
const genericConfigIntro = "Add search_roots to your afk config, e.g.:"

// renderEmptyState draws the placeholder body for a dashboard with zero known
// projects: a launch outside any repo where no configured search root has
// produced anything (yet, or ever). It replaces View's whole two-region split
// rather than leaving an empty sidebar next to an empty detail pane, because
// there is no session list and no selection to show either of those for.
//
// It performs no I/O: the config path comes from m.configPath, which
// applyProjectScan already populated from LoadConfig's result — this file's
// package doc forbids reading the settings file from the render layer.
//
// The example's lines are joined left-aligned before the block is centred:
// centred one by one, `[dash]` would sit over the middle of the search_roots
// line and stop reading as a table header above its key.
func (m Model) renderEmptyState(outerW, outerH int) string {
	contentW := outerW - 4
	if contentW < 10 {
		contentW = 10
	}

	intro := genericConfigIntro
	if m.configPath != "" {
		intro = "Add search_roots to " + m.configPath + ", e.g.:"
	}

	example := make([]string, len(emptyStateConfigExample))
	for i, line := range emptyStateConfigExample {
		example[i] = theme.dim.Render(truncate(line, contentW))
	}

	lines := []string{
		theme.sessionName.Render("No projects found"),
		"",
		theme.dim.Render(truncate(intro, contentW)),
		lipgloss.JoinVertical(lipgloss.Left, example...),
		"",
		theme.dim.Render(truncate("Running afk from inside a repo also works.", contentW)),
		"",
		theme.helpLine.Render(keyName(m.leaderKey.name, levelAllProjects, actionQuit) + ": quit"),
	}
	block := lipgloss.JoinVertical(lipgloss.Center, lines...)

	return lipgloss.Place(outerW, outerH, lipgloss.Center, lipgloss.Center, block)
}

// renderHeader renders the dashboard's header: an accent badge, the
// discovery-staleness signal, the cross-project attention line, and the
// project-switch hint — laid out on EXACTLY ONE ROW in every state.
//
// The one-row guarantee is load-bearing, not cosmetic. layoutDims,
// mouseFrameLayout and paneScreenGeometry each measure this function's
// height independently (lipgloss.Height(m.renderHeader())), so a second row
// in any state desynchronises mouse routing from layout and every click
// lands a row off. Nothing here may emit a newline: headerLine sheds pieces
// rather than wrapping them, and TestRenderHeaderIsExactlyOneRow pins the
// result across all five states.
//
// What the header deliberately no longer carries: the session count (the
// list below it is the count), the `(N of M projects)` position counter, the
// `refreshed Xs ago · every Ys` cadence — an implementation detail reported
// as news, and one that lies outright while discovery is failing, which is
// the only condition the staleness piece speaks up for — and the ACTIVE
// project's own awaiting count, which the list's
// own flag column already marks row by row. The active project's NAME moved
// to the session list box's border label, where it describes the box that
// actually holds that project's sessions.
//
// What it gained in their place is the one fact no other part of the frame
// can show: which OTHER projects have sessions waiting on a person. afk's
// whole premise is detached hosts that outlive the UI, so work continues in
// projects this dashboard is not pointed at, and without this line the only
// way to find out is to open the switcher and read it.
//
// It no longer defers to the switcher either. The old `if m.switcherOpen`
// branch blanked the project name and position counter so the header would
// not repeat what the overlay below it already listed; with those pieces
// gone there is nothing left to blank, and the attention line is worth
// keeping on screen precisely while the user is choosing where to go.
func (m Model) renderHeader() string {
	return headerLine(m.headerPieces(), m.width)
}

// headerBadgeText is the dashboard's brand mark. A named constant rather
// than a literal so the tests that assert it and the code that renders it
// cannot drift apart.
const headerBadgeText = "afk Dashboard"

// headerSwitchHint is the header's one affordance, in theme.dim — the
// register the box-edge hints already use, because this is something the
// user can press, not something that happened.
//
// It names the key as it is pressed at level lv (keyName): alone at All
// projects and Project, after the leader in the Agent. A function of the
// leader because config.toml sets it (T9.2).
func headerSwitchHint(leader string, lv level) string {
	return keyName(leader, lv, actionProjects) + ": projects"
}

// headerInboxHint is the inbox segment's affordance, styled and named the way
// headerSwitchHint is, for the same reason: it is a key the reader can press.
func headerInboxHint(leader string, lv level) string {
	return keyName(leader, lv, actionInbox) + ": inbox"
}

// headerPieces are the header's parts before headerLine's width ladder
// decides which survive. Each is independently zeroable to "" and headerLine
// copes with any combination of them, and each arrives pre-styled so
// headerLine measures the cells it will actually draw.
type headerPieces struct {
	badge string
	// stale says the list under this header has stopped moving, and for how
	// long — "stale · discovery failing 12s". It is "" whenever discovery is
	// healthy, which is almost always.
	//
	// It exists because applyLoaded keeps the previous sessions on an error (a
	// git hiccup must not blank the dashboard) and stamps lastLoad BEFORE the
	// error check, so every freshness signal the header could offer would keep
	// ticking over a list that is frozen. The header no longer carries a
	// refreshed-ago at all — the cadence was an implementation detail reported
	// as news — and the footer's sticky discovery error is gone too; the
	// failure toasts once, on the transition, and then the screen goes quiet
	// while staying wrong. This piece is therefore the ONLY standing signal
	// that what is on screen is no longer true, and the elapsed time is the
	// part that distinguishes a blink from an outage. Deleting it restores
	// neither of the two signals it replaced.
	stale string
	// attention names the projects — "● 2 awaiting in gamma · 1 in delta" —
	// while attentionShort collapses them into one phrase —
	// "● 6 awaiting in 4 other projects". The ladder trades the first for the
	// second when the width runs out. BOTH are "" when nothing is awaiting
	// elsewhere: an empty attention line is the correct rendering of a quiet
	// day, and "0 awaiting" is not a fact worth a row.
	attention      string
	attentionShort string
	// usage is the governor's stored decision and the tightest usage window —
	// "usage: paused until Mon 2 Jan 15:04 · Week (all models) 91%" — while
	// usageShort is the same in a few words. Both are "" when the model holds
	// no usage reading (no loader wired, no usage.json yet), which keeps every
	// header without a build exactly as it was.
	usage      string
	usageShort string
	// inbox is the active project's open inbox items — "inbox 3", or
	// "inbox 3 · 2 blocking" when some of them hold a task up — and
	// inboxShort is the blocking part alone, "2 blocking", or "" when nothing
	// blocks. The ladder trades the first for the second, so items that only
	// wait to be read leave the row before items that stop the build do.
	// inboxHint is "i: inbox". All three are "" when no item is open or no
	// build snapshot has arrived, which keeps a quiet project's header exactly
	// as it was before the inbox existed.
	inbox      string
	inboxShort string
	inboxHint  string
	hint       string // "" with one or zero known projects
	// leaderForms is the leader line, longest first, when config.toml's
	// [dash] leader was refused (leaderHeaderForms); nil when the leader is
	// fine, which is every dashboard whose owner never set one. It is not one
	// more rung of the ladder: headerLine gives it the row first and lays
	// everything else out in what is left, because it is the only place the
	// owner learns their setting did nothing, and it stays for as long as the
	// dashboard runs (the leader is read once, at start).
	leaderForms []string
}

// headerPieces gathers the parts from the Model. Zero or one known project
// is the DEFAULT case, not an edge case — it is what every unconfigured user
// sees — and it carries no hint and, having no other project to report on,
// no attention line either: just the badge.
func (m Model) headerPieces() headerPieces {
	p := headerPieces{badge: theme.header.Render(headerBadgeText)}
	for _, f := range leaderHeaderForms(m.leaderKey) {
		p.leaderForms = append(p.leaderForms, theme.awaitingFlag.Render(f))
	}
	if len(m.projects) > 1 {
		p.hint = theme.dim.Render(headerSwitchHint(m.leaderKey.name, m.level()))
	}
	// Measured from loadFailingSince (the FIRST failing pass of the current
	// run), not from lastLoad, so the number says how long the screen has
	// been wrong rather than how long ago the last attempt was made.
	if !m.loadFailingSince.IsZero() {
		p.stale = theme.errorText.Render(fmt.Sprintf("stale · discovery failing %s",
			durationShort(m.deps.Now().Sub(m.loadFailingSince))))
	}

	p.usage, p.usageShort = m.usagePieces()
	// Before the early return below: the inbox belongs to the active project,
	// so it has nothing to do with whether any OTHER project is awaiting.
	p.inbox, p.inboxShort, p.inboxHint = m.inboxPieces()

	others := m.awaitingElsewhere()
	if len(others) == 0 {
		return p
	}
	p.attention = attentionNamed(others)
	p.attentionShort = attentionAggregate(others)
	// A "shorter" form that is not shorter is no degradation. With a single
	// other project the aggregate spells out "in 1 other project" (18 cells)
	// where the named form spells that project's actual name, which is
	// usually fewer — so adopting it unconditionally would make rung 2 of the
	// ladder unreachable and, worse, hand the narrowest rung the WIDER string
	// to truncate. The aggregate is taken only when it earns its place, which
	// for one project means a name longer than the phrase that replaces it.
	// That is exactly when the singular "in 1 other project" is worth having.
	if lipgloss.Width(p.attentionShort) >= lipgloss.Width(p.attention) {
		p.attentionShort = p.attention
	}
	return p
}

// usagePieces styles usagetext.Segment's two forms. The segment is read from
// the snapshot the model already holds (never from usage.json here, because
// View must not touch the disk). It is dim while the governor says run or has
// said nothing, and in the warning style (the one the awaiting flag uses)
// when it is holding the build back or cannot tell, because that is the
// state the owner needs to notice.
func (m Model) usagePieces() (long, short string) {
	known := m.buildKnown && m.build.UsageKnown
	l, s := usagetext.Segment(m.build.Usage, known, m.deps.Now(), time.Local)
	if l == "" && s == "" {
		return "", ""
	}
	style := theme.dim
	if g := m.build.Usage.Governor; g != nil && g.Action != "" && g.Action != string(governor.ActionRun) {
		style = theme.awaitingFlag
	}
	return style.Render(l), style.Render(s)
}

// inboxPieces styles the active project's inbox counts for the header. They
// come from the build snapshot the refresh tick already loads (m.build), the
// same read the Build tab's summary line counts with buildview.OpenCounts, so
// the header and the tab can never disagree and View still touches no file.
//
// It asks only for m.buildKnown, not m.build.HasBuild: the inbox is read
// whether or not the project has a master plan, and an owner with a question
// waiting needs to hear about it either way. Files the loader could not read
// (m.build.Unreadable) are not items and are not counted; the Build tab is
// where they are reported.
//
// The hint is left off while the agent pane owns the keyboard, because there
// `i` is typed to the agent (inputPane forwards every unclaimed key to the
// child) and advertising it would promise a key that does something else.
func (m Model) inboxPieces() (long, short, hint string) {
	if !m.buildKnown {
		return "", "", ""
	}
	open, blocking := buildview.OpenCounts(m.build.Items)
	if open == 0 {
		return "", "", ""
	}
	long = theme.dim.Render(fmt.Sprintf("inbox %d", open))
	if blocking > 0 {
		short = theme.awaitingFlag.Render(fmt.Sprintf("%d blocking", blocking))
		long += theme.dim.Render(" · ") + short
	}
	if !m.paneOwnsKeyboard() {
		hint = theme.dim.Render(headerInboxHint(m.leaderKey.name, m.level()))
	}
	return long, short, hint
}

// attentionNamed is the attention line with the projects named: a warning
// dot, then one clause per project in awaitingElsewhere's order — which is
// the switcher's own order, so the header and the overlay can never disagree
// about which project matters most.
//
// Only the first clause carries the word "awaiting". Repeating it per
// project buys nothing once the reader has the frame, and the header's
// budget is cells.
func attentionNamed(others []otherProjectAwaiting) string {
	if len(others) == 0 {
		return ""
	}
	parts := make([]string, 0, len(others))
	for i, o := range others {
		clause := fmt.Sprintf("%d in %s", o.count, o.name)
		if i == 0 {
			clause = fmt.Sprintf("%d awaiting in %s", o.count, o.name)
		}
		parts = append(parts, theme.headerCount.Render(clause))
	}
	return theme.awaitingFlag.Render(awaitingGlyph) + " " +
		strings.Join(parts, theme.headerCount.Render(" · "))
}

// attentionAggregate is the attention line collapsed to one phrase for the
// widths that cannot hold the names: the total awaiting across every other
// project, and how many projects that is. It reuses awaitingGlyph and
// theme.awaitingFlag so the dot means the same thing here as it does in the
// list's flag column and in the switcher's rows.
func attentionAggregate(others []otherProjectAwaiting) string {
	if len(others) == 0 {
		return ""
	}
	total := 0
	for _, o := range others {
		total += o.count
	}
	noun := "projects"
	if len(others) == 1 {
		noun = "project"
	}
	return theme.awaitingFlag.Render(awaitingGlyph) + " " +
		theme.headerCount.Render(fmt.Sprintf("%d awaiting in %d other %s", total, len(others), noun))
}

// headerLine lays the pieces out for width: the badge, the staleness signal
// and the attention line on the left; the inbox counts, the inbox hint, the
// usage text and the switch hint on the right, in that order and right-aligned;
// and at least headerMinGap cells between the halves so they never read as one
// run of text. It tries the candidates richest-first and takes the first that
// fits, so a narrowing terminal sheds the least useful piece first and never
// wraps or cuts a name mid-word until there is no other choice. Pure, so the
// whole ladder is table-testable from plain unstyled pieces
// (TestHeaderLineDegradesInOrder, TestHeaderLineDegradesWithInbox).
//
// There is one drop sequence for every combination of pieces. Each step takes
// one piece away (or swaps it for its short form); a step whose piece is
// absent changes nothing and adds no rung. Weakest first:
//
//  1. the switch hint
//  2. the inbox hint
//  3. the attention line's names, for the aggregate
//  4. the long usage text, for the short
//  5. the long inbox text, for the blocking count alone (items that only
//     wait to be read leave the row here)
//  6. the badge
//  7. the short usage text
//  8. the attention line
//  9. the blocking count
//  10. and the staleness signal is what is left, truncated if it must be
//
// With the switch hint alone on the right (no usage reading and no open
// inbox item, the common case for anyone not running a build), steps 1 and 3
// swap: the names collapse before the hint goes, the order this header has
// always had there. Once the right half carries anything else, the hint is
// its weakest piece, since it only repeats what the key help says.
//
// The blocking count outlasts the badge, both hints and the short usage
// because it asks the owner to act in THIS project: a task is stopped until
// they answer, and no other part of the frame says so outside the Build tab.
// The short usage outlives the badge because it says whether the build is
// being held back, which the reader cannot see anywhere else on the row,
// while the badge says what they already know. The usage goes before the
// attention line and the blocking count because those ask the reader to act,
// and a usage note does not.
//
// The badge going before the alerts looks wrong and is not. An alert you can
// act on outranks a brand mark: the badge tells the reader that they launched
// this program, while "● 3 awaiting in gamma" asks them to go do something in
// a project they cannot otherwise see from here.
//
// The attention line goes before the blocking count because the count is the
// active project's own, read from its files on this tick, while the attention
// line's counts lag by up to a minute (awaitingElsewhere) and can be read
// from the switcher. Both then go before the staleness signal, because
// staleness is not one more fact competing with the others: it is the
// statement that the others cannot be trusted. A frozen list invalidates
// everything else on the frame, so a header showing "● 3 awaiting in gamma"
// over a dead refresh is worse than one showing nothing at all, and the last
// cells belong to the piece that says so. It is also the rarest piece by far
// (discovery is healthy nearly always), so surrendering these rungs to it
// costs the common case nothing.
//
// The badge is the last thing standing only when nothing else is there at
// all, which is also the only case where it is the only thing there is.
//
// Every fitting rung pads out to the full width, the empty-right-half rungs
// included, so the string this function RETURNS is width cells wide in every
// state.
//
// What lands on screen is not always. With a modal or a toast up, the frame
// goes through lipgloss's Canvas, and Canvas.Render is documented to trim
// trailing spaces (lipgloss/v2@v2.0.6 canvas.go:85 — `uv.TrimSpace`), so a
// header with nothing on its right half arrives at badge width instead. The
// golden frames under a modal show exactly that case: line 1 is the badge
// alone, against the full width in the frames with no overlay and in those
// whose right half reaches the edge. The trimmed cells were blank either way,
// so nothing shifts — but the padding is this function's contract, not the
// rendered frame's, and a reader measuring a fixture against this comment has
// to know which of the two they are looking at.
func headerLine(p headerPieces, width int) string {
	if len(p.leaderForms) > 0 {
		return headerLineWithLeader(p, width)
	}
	// alerts is the badgeless left half: the staleness signal, then the
	// attention line. Two plain spaces between them, not the " · " the
	// attention line joins its own clauses with, which would read as one more
	// clause of the same list rather than as a separate warning.
	alerts := func(attention string) string {
		switch {
		case p.stale == "":
			return attention
		case attention == "":
			return p.stale
		}
		return p.stale + "  " + attention
	}
	// keep is which pieces a rung still shows. The staleness signal has no
	// field because no step removes it: only the truncating fallback can.
	type keep struct {
		hint, inboxHint, named, usageLong, inboxLong bool
		badge, usage, attention, blocking            bool
	}
	type rung struct{ l, r string }
	draw := func(k keep) rung {
		attention := ""
		if k.attention {
			attention = p.attentionShort
			if k.named {
				attention = p.attention
			}
		}
		l := alerts(attention)
		if k.badge && p.badge != "" {
			// One plain space, not the " · " the clauses themselves join
			// with: theme.header's Padding(0, 1) already lays a cell of
			// accent background after the badge text, and a separator glyph
			// butted against that edge reads as part of the badge.
			if l == "" {
				l = p.badge
			} else {
				l = p.badge + " " + l
			}
		}
		var right []string
		add := func(on bool, s string) {
			if on && s != "" {
				right = append(right, s)
			}
		}
		if k.inboxLong {
			add(true, p.inbox)
		} else {
			add(k.blocking, p.inboxShort)
		}
		add(k.inboxHint, p.inboxHint)
		if k.usageLong {
			add(true, p.usage)
		} else {
			add(k.usage, p.usageShort)
		}
		add(k.hint, p.hint)
		// Two plain spaces between right-half pieces, the gap the halves
		// themselves keep, so each reads as its own item.
		return rung{l, strings.Join(right, "  ")}
	}

	dropHint := func(k *keep) { k.hint = false }
	dropInboxHint := func(k *keep) { k.inboxHint = false }
	collapseNames := func(k *keep) { k.named = false }
	steps := []func(*keep){dropHint, dropInboxHint, collapseNames}
	if p.usage == "" && p.usageShort == "" && p.inbox == "" {
		steps = []func(*keep){collapseNames, dropHint, dropInboxHint}
	}
	steps = append(steps,
		func(k *keep) { k.usageLong = false },
		func(k *keep) { k.inboxLong = false },
		func(k *keep) { k.badge = false },
		func(k *keep) { k.usage = false },
		func(k *keep) { k.attention = false },
		func(k *keep) { k.blocking = false },
	)

	k := keep{true, true, true, true, true, true, true, true, true}
	candidates := []rung{draw(k)}
	for _, step := range steps {
		step(&k)
		c := draw(k)
		// A step whose piece is absent repeats the rung above, which would
		// only be dead width arithmetic. And a rung with both halves empty
		// "fits" at every width: it would swallow the last piece standing
		// into a row of spaces before the truncating fallback ever got the
		// chance at it. The first rung is exempt from neither on purpose —
		// it has no rung above — but it is only empty when every piece is.
		if c == candidates[len(candidates)-1] || (c.l == "" && c.r == "") {
			continue
		}
		candidates = append(candidates, c)
	}
	for _, c := range candidates {
		minGap := headerMinGap
		// With an empty right half there is nothing for headerMinGap to hold
		// apart; the left half only has to fit, and the slack becomes pad.
		if c.r == "" {
			minGap = 0
		}
		if gap := width - lipgloss.Width(c.l) - lipgloss.Width(c.r); gap >= minGap {
			// The top bar (§3.1) lays the rung out: left at the start, right
			// at the end, width cells in all. The rung already fits, so it
			// cuts nothing.
			return theme.topBar(c.l, c.r, width)
		}
	}
	// Last resort: one piece, truncated. The staleness signal still comes
	// first, for the reasons the drop order above gives. The attention line
	// is ahead of the blocking count here, the reverse of the ladder, which
	// is the order T6.4's plan recorded; it only decides anything below the
	// width of the blocking count alone (about ten cells), where every piece
	// is cut to a few characters and the attention line's leading ● is the
	// one mark still recognisable. truncate's own contract returns "" at
	// width <= 0, which is what keeps a zero-width frame from emitting a
	// stray row.
	for _, last := range []string{p.stale, p.attentionShort, p.inboxShort, p.usageShort, p.badge} {
		if last != "" {
			return truncate(last, width)
		}
	}
	return truncate("", width)
}

// headerLeaderRestMin is the fewest cells headerLineWithLeader hands the
// rest of the header beside the leader line. Below it the rest could show
// only a piece cut to a letter or two and an ellipsis, which says nothing, so
// the leader line takes the row alone instead.
const headerLeaderRestMin = 16

// headerLineWithLeader is headerLine when the leader line is up. The leader
// line outranks every other piece, the staleness signal included: it is the
// one standing statement that a setting the owner made is being ignored, and
// no other part of the frame repeats it. So its longest form that fits goes
// first on the left, and the usual ladder lays the other pieces out in the
// cells that remain (two cells apart, the gap the halves keep). A form leaves
// the row only when it does not fit even alone, and the shortest form is cut
// as the last resort. The result is width cells wide, one row, like
// headerLine's.
func headerLineWithLeader(p headerPieces, width int) string {
	rest := p
	rest.leaderForms = nil
	for _, f := range p.leaderForms {
		fw := lipgloss.Width(f)
		if restW := width - fw - headerMinGap; restW >= headerLeaderRestMin {
			return f + strings.Repeat(" ", headerMinGap) + headerLine(rest, restW)
		}
		if fw <= width {
			return f + strings.Repeat(" ", width-fw)
		}
	}
	return truncate(p.leaderForms[len(p.leaderForms)-1], width)
}

// leaderHeaderForms is the header's leader line for a refused leader, in
// three lengths, longest first, unstyled; nil when the leader was accepted or
// never set. Every form names the value as written, config.toml and the key
// in use, because those are what the owner needs: what was wrong, where to
// fix it, and what to press meanwhile. The longest carries checkLeader's
// whole sentence; the middle one its few words (leaderSetting.why) and an
// example to copy; the shortest only the three facts. The value is quoted
// with %q, so a value holding a newline cannot break the header's one row,
// and cut to leaderHeaderValueMax so a long typo cannot crowd the row out.
func leaderHeaderForms(l leaderSetting) []string {
	if l.refused == "" {
		return nil
	}
	value := fmt.Sprintf("%q", truncate(l.configured, leaderHeaderValueMax))
	return []string{
		fmt.Sprintf("leader not used: %s · set leader in [dash] of config.toml · using %s", l.refused, l.name),
		fmt.Sprintf(`leader %s not used (%s): set e.g. leader = "ctrl+o" in [dash] of config.toml · using %s`, value, l.why, l.name),
		fmt.Sprintf("leader %s not used: fix [dash] in config.toml · using %s", value, l.name),
	}
}

// leaderHeaderValueMax is how many cells of the configured leader the
// header's shorter forms quote.
const leaderHeaderValueMax = 20

// headerMinGap is the fewest cells headerLine leaves between the left and right
// halves before it sheds a piece, so the two never read as one run of text.
const headerMinGap = 2

// awaitingCount is how many rows of the ACTIVE project would show the
// awaiting flag — the same predicate the row itself uses
// (showsAwaitingInput), so this number and the dots in the list can never
// disagree.
//
// The header no longer reports it: the flag column already marks those rows
// a few lines below, and the header's attention line is about the projects
// you CANNOT see. windowTitle (toast.go) is the remaining caller, where the
// number does earn its place — it reaches a user who has switched to another
// terminal tab entirely.
func (m Model) awaitingCount() int {
	n := 0
	for _, s := range m.sessions {
		if showsAwaitingInput(s) {
			n++
		}
	}
	return n
}

// otherProjectAwaiting is one entry of awaitingElsewhere's result: a project
// other than the active one, and how many of its sessions are awaiting
// input. W2.T1 renders these into the header's attention line.
type otherProjectAwaiting struct {
	name  string
	count int
}

// awaitingElsewhere reports which projects OTHER than the active one have at
// least one session awaiting input, each paired with how many. It is pure
// and reads only in-memory state — no dialing, no IPC, no file reads — because
// it runs on the dashboard's render path, which must never block.
//
// The result is ordered by sortSwitcherProjects (switcher.go), the exact
// comparator newSwitcher itself sorts by, so the header and the switcher can
// never disagree about which project matters most. Unlike newSwitcher's own
// per-project awaiting bool (also reused here, from its construction loop),
// this counts awaiting sessions rather than reducing to true/false, because
// the header wants to say how many. A project with zero awaiting sessions is
// omitted entirely — the caller distinguishes "nothing awaiting elsewhere"
// (nil/empty result) from "some other project has zero" this way, so it must
// never render "0 awaiting" or "0 elsewhere".
//
// The active project is excluded by path, not by index: when m.activeProject
// is out of range, activePath below comes out "" (no entry in m.projects has
// that path), so every known project counts as "other" — the same tolerance
// headerPieces already gives an out-of-range index, rather than a panic.
//
// The data source is m.projectSessions, which the ~60s scanTickMsg handler
// (applyProjectScan) writes — NOT the 2s tickMsg discovery path that keeps
// m.sessions and m.lastLoad current for the active project. So these counts
// can lag the active project's own by up to a minute; that is by design, not
// a bug to reconcile, which is why this reads neither m.interval nor
// m.lastLoad (both describe the 2s tick's freshness, not this data's).
func (m Model) awaitingElsewhere() []otherProjectAwaiting {
	activePath := ""
	if idx := m.activeProject; idx >= 0 && idx < len(m.projects) {
		activePath = m.projects[idx].Path
	}

	sorted := sortSwitcherProjects(m.projects, m.projectSessions, m.projectOpens)

	var others []otherProjectAwaiting
	for _, p := range sorted {
		if p.Path == activePath {
			continue
		}
		n := 0
		for _, s := range m.projectSessions[p.Path] {
			if s.AwaitingInput {
				n++
			}
		}
		if n == 0 {
			continue
		}
		others = append(others, otherProjectAwaiting{name: p.Name, count: n})
	}
	return others
}

// renderFooter is the help line, and nothing else.
//
// It used to carry a status row above it as well, which made every
// notification paint two surfaces at once: this row and the toast (toast.go)
// that lifts the same text into the corner. The toast is now the only place a
// notification appears, so outcome detail is ephemeral by design — the archive
// path of a clean is rediscoverable by listing the archive directory, a
// recovery SHA through `git reflog`, and neither is worth a permanent row.
// A whole-dashboard discovery failure used to be sticky here too; it is an
// error-kind notification now (notifyDiscoveryError), with the header saying
// that the list is frozen for as long as the failure lasts.
//
// A modal does not replace the footer either: its dialog floats over the frame
// (activeDialog, modal.go) and the help line follows the active input mode, so
// mid-modal it lists the modal's own keys.
//
// The invariant is that this function's height is exactly renderHelp's —
// nothing is added to it. Since `?` opens the keys overlay rather than the
// full help that was one row per binding, that is one row.
//
// It is drawn as the status bar (§3.2): each row of the help is a status bar,
// strip edge to edge, so the bar reads as one band across the bottom of the
// screen. The help row is the bar's left half, where it has always started;
// its words and keys are renderHelp's, unchanged, and already cut to the
// width by bubbles/help.
func (m Model) renderFooter() string {
	rows := strings.Split(m.renderHelp(), "\n")
	for i, r := range rows {
		rows[i] = theme.statusBar(r, "", m.width)
	}
	return strings.Join(rows, "\n")
}

// spawnErrorDialog is the failed-background-create report. It is the only
// place the child's captured output is ever shown: the create ran in the
// background with its stdout/stderr piped, so nothing reached the terminal live.
func (m Model) spawnErrorDialog() dialog {
	lines := []string{theme.errorText.Render(m.spawnErrHeadline)}
	for _, ln := range m.spawnErrTail {
		lines = append(lines, theme.dim.Render(ln))
	}
	reg := m.modes()
	return dialog{
		title: "Session failed to start",
		lines: lines,
		hint:  modalKeyHint(reg.spec(inputSpawnError).keys.bindings, "dismiss"),
	}
}

// renderSpawnErrorBar is the rendered spawnErrorDialog. The name survives from
// when this was a footer bar; the tests that read it still do.
func (m Model) renderSpawnErrorBar() string { return m.renderDialog(m.spawnErrorDialog()) }

// renderConfirmCleanBar is the rendered clean-confirmation dialog. Like
// renderSpawnErrorBar, the name survives from when this was a footer bar; the
// dialog now floats over the frame (activeDialog, modal.go) and the footer
// keeps its help line beneath it. The name/phase shown are the ones remembered
// at confirm-entry time (m.confirmTarget/m.confirmPhase), never a fresh lookup
// of the selected session.
//
// Every session confirms here now, `done` and `failed` ones included, so the
// phase is presented as plain information rather than as something to be talked
// out of: no "force" framing, and the second line states the consequence next
// to the guarantee. That sentence is per-ROUTE, not one string: the four
// teardowns internal/session runs keep different promises, and each false one
// is false on a destructive confirmation the user answers with a single key. An
// empty confirmPhase is exactly how a stale row presents (a non-stale session
// always has a non-empty phase); it shows as "unknown" inside the same sentence
// rather than as a separate one.
//
// The hint line is length-budgeted, not just worded: 80 columns is the narrowest
// terminal anyone runs, truncate() cuts at m.width, and the key hint sits at the
// END — so a hint that fits only a wide terminal loses the one part of the modal
// that says how to answer it. confirmCleanConsequence plus the keymap-derived key
// portion stays under that budget, matched by
// TestConfirmCleanBarFitsAnEightyColumnTerminal.
func (m Model) confirmCleanDialog() dialog {
	// A one-off target gets its own copy on BOTH lines, not just the hint. Its
	// empty phase is not the stale-session "unknown" (it has no lifecycle at
	// all — sessionPhaseKey makes the same distinction for the badge), and its
	// clean stops the host and removes .afk/{name}/ only, so the standard
	// consequence "Worktree goes; archive kept." would be false on a
	// destructive-action confirmation.
	if m.confirmScratch {
		return dialog{
			title: "Clean session",
			lines: []string{
				theme.statusLine.Render(fmt.Sprintf("Clean one-off session %q?", m.confirmTarget)),
				theme.dim.Render(confirmCleanScratchConsequence),
			},
			hint: m.confirmCleanKeyHint(),
		}
	}
	question := confirmCleanLine(m.confirmTarget, m.confirmPhase, m.confirmKind, m.confirmMerge)
	consequence := confirmCleanConsequence
	keyHint := m.confirmCleanFullKeyHint()
	switch {
	case m.confirmKind == session.KindStatusWithoutWorktree:
		// The one non-scratch row whose hint drops `d`: this route deletes no
		// branch, so advertising the key would advertise a no-op (see
		// deleteBranchClean).
		consequence = confirmCleanStrandedConsequence
		keyHint = m.confirmCleanKeyHint()
	case m.confirmKind.WorktreeFault():
		consequence = confirmCleanFaultConsequence
	}
	title := "Clean session"
	if m.confirmDeleteUnmerged {
		title = "Delete an unmerged branch?"
		consequence = confirmCleanUnmergedConsequence
		keyHint = m.confirmCleanUnmergedKeyHint()
	}
	return dialog{
		title: title,
		lines: []string{theme.statusLine.Render(question), theme.dim.Render(consequence)},
		hint:  keyHint,
	}
}

// renderConfirmCleanBar is the rendered confirmCleanDialog. The name survives
// from when this was a footer bar; the tests that read it still do.
func (m Model) renderConfirmCleanBar() string { return m.renderDialog(m.confirmCleanDialog()) }

// confirmRestartDialog is the wedged-child restart confirmation. It warns about
// disconnection because the restart dial STEALS the session's single client
// slot: a `afk attach` watching this session in another terminal is silently
// detached the moment the request is sent — and the user must learn that
// before pressing y, not from a confused teammate afterwards.
func (m Model) confirmRestartDialog() dialog {
	reg := m.modes()
	return dialog{
		title: "Restart session",
		lines: []string{
			theme.statusLine.Render(fmt.Sprintf("Restart %q?", m.confirmRestartTarget)),
			theme.dim.Render(confirmRestartConsequence),
		},
		hint: modalKeyHint(reg.spec(inputConfirmRestart).keys.bindings, "restart", "cancel"),
	}
}

// confirmRestartConsequence is the restart dialog's second line.
const confirmRestartConsequence = "Replaces its child process and disconnects any attached client."

// confirmCleanConsequence is the confirm modal's consequence sentence — the
// prose half of the hint line; the key half is keymap-derived
// (confirmCleanFullKeyHint), so only what cannot drift from a binding lives in
// a constant. The branch is no longer among the guarantees — `d` deletes it —
// so the sentence states only what stays true unconditionally.
const confirmCleanConsequence = "Worktree goes; archive kept."

// confirmCleanUnmergedConsequence replaces confirmCleanConsequence once `d` has
// been pressed on a branch that is not known to be merged. It is the only
// friction in front of a delete whose commits exist nowhere else, so it leads
// with what is lost rather than with the key — and the matching key hint keeps
// `y` off the line deliberately: this is a question about the branch, and the
// two answers offered are the two that answer it.
const confirmCleanUnmergedConsequence = "Unmerged commits will be lost."

// confirmCleanFaultConsequence replaces confirmCleanConsequence on a
// worktree-side fault row. Those routes set ArchiveSkipped unconditionally — a
// missing directory took its .afk/{name}/ with it, and a foreign worktree
// never had one — so "archive kept" is a promise this clean cannot keep. It is
// stated on the same destructive confirmation the user answers with one key,
// which is the one place a false guarantee costs the most. Same 80-column
// budget as the others, key hint last.
const confirmCleanFaultConsequence = "Worktree goes; nothing archived."

// confirmCleanStrandedConsequence replaces confirmCleanConsequence on a
// KindStatusWithoutWorktree row. Its teardown archives `.afk/{name}/`,
// removes that directory, and issues no git command at all — the worktree
// carrying the directory is usually the user's own checkout, so "Worktree
// goes" would promise on this one-keypress confirmation to delete the tree
// they are sitting in.
//
// 48 columns, which with " · " and the 24-column scratch-style key hint leaves
// the same margin the scratch line keeps inside the 80-column budget
// TestConfirmCleanHintsFitEightyColumns enforces. The hint is the scratch one
// deliberately: `d` is inert here, so the full hint would advertise it.
const confirmCleanStrandedConsequence = "Session dir archived and removed; worktree kept."

// confirmCleanLine is the modal's first line: what is being cleaned, in what
// condition, and — once the ancestry check has answered — whether its branch
// is already merged, into a base that is INTERPOLATED. Hardcoding "main" here
// would read as correct in this repository and lie in every one that ships
// from a release branch, on the single line a user reads before pressing a key
// that deletes a branch.
//
// Pure, with the environment fact (the merge state) passed in, per this repo's
// convention: the ancestry check itself is a git call and lives in a tea.Cmd,
// so the whole matrix of what this line can say stays table-testable with no
// repository at all.
//
// Merge state that is not known — the check is still in flight, or could not
// name a base — renders the line exactly as it read before `d` existed. A
// half-written sentence is worse than the question it replaces.
func confirmCleanLine(target string, phase state.Phase, kind session.Kind, merge cleanMergeState) string {
	subject := fmt.Sprintf("Clean session %q (%s)", target, confirmCleanCondition(phase, kind))
	if !merge.known || merge.base == "" {
		return subject + "?"
	}
	if merge.merged {
		return subject + " — branch merged to " + merge.base
	}
	return subject + " — branch not merged to " + merge.base
}

// confirmCleanCondition is the parenthetical: the session's phase, or the
// fault that replaces it.
//
// A worktree-side fault row has an empty phase because its status file is gone
// or was never there, not because the file failed to parse — and "unknown" on
// this dashboard is a real, different verdict, the one a session with a corrupt
// status.json wears. Reporting it on a row the user selected precisely because
// it was badged BROKEN would name the wrong problem, so each fault names its
// own.
func confirmCleanCondition(phase state.Phase, kind session.Kind) string {
	switch kind {
	case session.KindWorktreeMissing:
		return "worktree missing"
	case session.KindWorktreeNoSession:
		return "no afk session"
	}
	if phase == "" {
		return "phase: unknown"
	}
	return "phase: " + string(phase)
}

// confirmCleanScratchConsequence is confirmCleanConsequence's one-off variant:
// a scratch clean touches no worktree and keeps no branch or archive, so the
// consequence it states is the only true one — the host stops and
// .afk/{name}/ goes.
const confirmCleanScratchConsequence = "Stops the host and removes the session directory."

// confirmCleanKeyHint builds the "y: clean · n/esc: cancel" key portion the
// retired confirmCleanHint/confirmCleanScratchHint constants used to
// hand-write, from mode.go's inputConfirmClean keymap instead — so the two
// cannot drift, and so a future rebind of y/n/esc shows up here with no
// second edit. It reproduces the exact old phrasing (colon after the key,
// "n/esc" combined) because TestConfirmCleanBarScratchStatesTheTrueConsequence
// pins it; help.Model's own separator/format is intentionally NOT used here
// (that would grow the line with keys — mouse release, quit — this modal
// never advertised).
func (m Model) confirmCleanKeyHint() string {
	reg := m.modes()
	return confirmModalKeyHint(reg.spec(inputConfirmClean).keys.bindings, "clean")
}

// confirmCleanFullKeyHint is the non-scratch variant: it advertises the `d`
// offer between the confirm and the cancels ("y: clean · d: +delete branch ·
// n/esc: cancel"). Scratch confirmations keep confirmCleanKeyHint, which never
// mentions `d` — a one-off has no branch and deleteBranchClean makes the key
// inert there, so this split is what keeps the footer honest about it.
func (m Model) confirmCleanFullKeyHint() string {
	reg := m.modes()
	return modalKeyHint(reg.spec(inputConfirmClean).keys.bindings, "clean", "+delete branch", "cancel")
}

// confirmCleanUnmergedKeyHint is the second confirmation's key portion:
// "d: delete anyway · n/esc: cancel", read off the same keymap. `y` is
// deliberately absent — see confirmCleanUnmergedConsequence.
func (m Model) confirmCleanUnmergedKeyHint() string {
	reg := m.modes()
	return modalKeyHint(reg.spec(inputConfirmClean).keys.bindings, "delete anyway", "cancel")
}

// confirmModalKeyHint is the two-answer modal hint, shared with the label bar
// and reusable by an inputConfirmRestart footer if one ever grows a dedicated
// bar: the mode's confirm-answer description, then its cancels.
func confirmModalKeyHint(bindings []key.Binding, confirmDesc string) string {
	return modalKeyHint(bindings, confirmDesc, "cancel")
}

// modalKeyHint builds a modal hint's key portion from a mode keymap: each
// requested description, in the order given, rendered as "keys: desc", with
// the keys of every binding sharing a description joined by "/" — which is
// what folds the n and esc cancel bindings into "n/esc: cancel". Selecting by
// Help().Desc keeps the keys themselves single-sourced in mode.go: a rebind of
// y/n/esc/d shows up here with no second edit. A description no binding
// carries contributes nothing rather than an empty "…: " fragment.
func modalKeyHint(bindings []key.Binding, descs ...string) string {
	parts := make([]string, 0, len(descs))
	for _, d := range descs {
		var keys []string
		for _, b := range bindings {
			if h := b.Help(); h.Desc == d {
				keys = append(keys, h.Key)
			}
		}
		if len(keys) == 0 {
			continue
		}
		parts = append(parts, strings.Join(keys, "/")+": "+d)
	}
	return strings.Join(parts, " · ")
}

// labelDialog is the label-edit prompt (inputEditLabel): a line naming the
// session being labeled and bubbles/textinput's own View() showing the
// live-edited value beneath it.
func (m Model) labelDialog() dialog {
	reg := m.modes()
	return dialog{
		title: "Label session",
		lines: []string{
			theme.statusLine.Render("Label for " + m.labelTarget + ":"),
			m.labelInput.View(),
		},
		hint: confirmModalKeyHint(reg.spec(inputEditLabel).keys.bindings, "save (blank clears it)"),
	}
}

// providerPickDialog is the one-off provider picker (inputPickProvider): one
// row per provider in provider.Names() order, the highlighted one marked with
// "›" and bright, the rest dim. Names are shown raw, exactly as the `n` form's
// Provider selector shows them, so the word a user picks here is the word
// they would type after `afk new --provider`. provider.Default carries
// "(default)" because it is the only row whose choice adds nothing to the
// create's argv.
func (m Model) providerPickDialog() dialog {
	names := provider.Names()
	lines := make([]string, 0, len(names)+1)
	lines = append(lines, theme.statusLine.Render("Start a one-off session with:"))
	for i, n := range names {
		label := n
		if n == provider.Default {
			label += " (default)"
		}
		if i == m.providerPick {
			lines = append(lines, theme.sessionName.Render("› "+label))
		} else {
			lines = append(lines, theme.dim.Render("  "+label))
		}
	}
	reg := m.modes()
	return dialog{
		title: "New one-off session",
		lines: lines,
		hint:  modalKeyHint(reg.spec(inputPickProvider).keys.bindings, "up", "down", "start", "cancel"),
	}
}

// renderLabelFormBar is the rendered labelDialog. The name survives from when
// this was a footer bar; the tests that read it still do.
func (m Model) renderLabelFormBar() string { return m.renderDialog(m.labelDialog()) }

// mouseCaptureHint reports where mouse events are currently going, in the two
// shortest strings that still name the chord and the destination. "pane" is the
// dashboard's own word for where a hosted session is drawn (`enter focus view` sits
// on the same line), and "term" is the user's own terminal, where releasing
// capture makes native click-drag selection work again.
//
// Both states are deliberately the SAME width (20 cells). The focused help line
// is budgeted to the column (see renderHelp), and a hint that changed length
// between states would move every item after it as the user toggled — so one
// measurement covers both states only because the two strings measure equal.
func (m Model) mouseCaptureHint() string {
	if m.mouseCapture {
		return keyName(m.leaderKey.name, levelAgent, actionMouseToggle) + " mouse: pane"
	}
	return keyName(m.leaderKey.name, levelAgent, actionMouseToggle) + " mouse: term"
}

// normalBindingEnabled reports whether an inputNormal binding is currently
// worth offering: three of them are no-ops in some states, and this is the
// SINGLE definition of which three and when. Both surfaces that present the
// mode's action set read it — help.Model omits any !Enabled() binding from
// both Short and Full help — so no surface can advertise a different set,
// which is exactly the drift a second copy of this policy would eventually
// produce.
//
// Every other binding keeps whatever enabled bit it arrived with, so callers
// can apply this uniformly across a whole keymap.
//
// It answers about a COPY of the mode's bindings in every caller (m.modes()
// rebuilds that slice, with copied Binding values, on every call — see mode.go's
// doc), never m.keys or the dispatch table newModeRegistry builds separately
// from the same fields, so applying it can never desync a key's help text from
// whether it still dispatches.
func (m Model) normalBindingEnabled(b key.Binding) bool {
	switch {
	// The combined binding focuses whichever surface is drawn in the main
	// region.
	case slices.Equal(b.Keys(), m.keys.Focus.Keys()):
		return m.pane != nil || m.detailDrawn() || m.agentPlaceholderDrawn()
	case slices.Equal(b.Keys(), m.keys.Restart.Keys()):
		return m.restartKeyLive()
	}
	return b.Enabled()
}

// restartKeyLive is whether `r` acts on the selected row right now — the
// same gates restartSelected (model.go) applies, so help and the `:` search name
// the key exactly where it does something: a stopped row it would restart
// straight away, or a running row the dashboard already shows as WEDGED, where
// it asks for confirmation first.
func (m Model) restartKeyLive() bool {
	s, ok := m.selected()
	if !ok {
		return false
	}
	if m.restartableStopped(s) {
		return true
	}
	if !s.HostRunning || s.Pending || m.cleaningSessions[cleanIDFor(s)] {
		return false
	}
	_, wedged := showsWedged(s, m.deps.Now())
	return wedged
}

// findBinding returns the first binding in bindings whose Keys() equal keys,
// or the zero Binding (Enabled() == false, since its keys are nil) if none
// matches — which is what lets renderNormalHelpKeyMap build an ordered
// priority list by lookup and have an absent-or-disabled entry drop out of
// help.Model's rendering with no separate filter step.
func findBinding(bindings []key.Binding, keys []string) key.Binding {
	for _, b := range bindings {
		if slices.Equal(b.Keys(), keys) {
			return b
		}
	}
	return key.Binding{}
}

// curatedKeyMap is a help.KeyMap whose short and full views can differ — the
// mechanism inputNormal and inputPane need and modeKeyMap (mode.go) does not
// provide, because modeKeyMap's ShortHelp/FullHelp are always the same set
// (every other mode's short line already showed its whole keymap before this
// task, and still does). Full is always the mode's complete registered set
// (modulo the same enabled bits Short respects), so `?`'s full-help view
// never omits a key that only Short chose not to lead with.
type curatedKeyMap struct {
	short []key.Binding
	full  []key.Binding
}

func (k curatedKeyMap) ShortHelp() []key.Binding  { return k.short }
func (k curatedKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.full} }

// renderModeHelp is renderHelp's and the two overlays' (renderNewForm,
// renderSwitcher) shared final step: hand a help.KeyMap to bubbles/help and
// get back the existing footer voice — one muted color for the whole line (no
// per-key/per-desc two-tone; Styles is the zero value so nothing but the
// outer theme.helpLine.Render call adds color, which is the byte-for-byte
// look this task's Must-NOT-restyle constraint asks for) and the " · "
// separator every retired hand-written constant used. width is the caller's
// own budget — the footer's full terminal width for renderHelp, but the
// box's CONTENT width for the two overlays, whose help line renders inside
// their border, not the footer.
//
// The short-help path adds one backstop help.Model does not: verified against
// bubbles/v2@v2.2.1's source, ShortHelpView's shouldAddItem gives up on
// truncating gracefully once even its own ellipsis no longer fits the
// remaining budget, and from that point on appends every remaining item
// UNBOUNDED — at a narrow enough width (40 columns reproduces it with this
// mode's item widths) the "short" line stops being short at all. truncate()
// (this file) is the hard cut that keeps "fits within m.width" true
// regardless, which is why it is applied only here and not to FullHelp's
// output below: that can be legitimately multi-line (one row per binding),
// and a single-line truncate would corrupt it rather than bound it.
func renderModeHelp(km help.KeyMap, showAll bool, width int) string {
	return renderModeHelpStyled(km, showAll, width, theme.helpStyles)
}

// renderStatusHelp is renderModeHelp for the status bar: the same line in
// theme.statusHelpStyles, whose words are readable on the bar's strip fill.
// renderHelp is its only caller; the overlays' help lines sit on the ground
// and keep renderModeHelp.
func renderStatusHelp(km help.KeyMap, showAll bool, width int) string {
	return renderModeHelpStyled(km, showAll, width, theme.statusHelpStyles)
}

// renderModeHelpStyled is renderModeHelp with the help styles passed in.
func renderModeHelpStyled(km help.KeyMap, showAll bool, width int, styles help.Styles) string {
	h := help.New()
	h.Styles = styles
	h.ShortSeparator = " · "
	h.FullSeparator = " · "
	h.ShowAll = showAll
	if width < 0 {
		width = 0
	}
	h.SetWidth(width)
	rendered := h.View(km)
	if !showAll {
		rendered = truncate(rendered, width)
	}
	return rendered
}

// tableHelp is a help entry for action a from the key table at level lv:
// its key as it is pressed there, desc as its word, and dropped from the line
// while its blocked reason holds, on the rule that help never offers a key
// that would do nothing.
func (m Model) tableHelp(lv level, a modeAction, desc string, c leaderConds) key.Binding {
	enabled := true
	if row, ok := keyRowForAction(lv, a); ok {
		enabled = row.blockedReason(c) == ""
	}
	return keyHelpBinding(m.leaderKey.name, lv, a, desc, enabled)
}

// renderNormalHelpKeyMap builds the help.KeyMap for the session list at
// Project (inputNormal with a project open). Short is the movement keys, the
// two level keys (enter opens the selected session's agent, esc goes to All
// projects), the two actions a person most often comes to the list for (a new
// session, a session's details), restart only while it acts on the selected
// row (help never offers a key that would do nothing, and a stopped or stuck
// agent is the state a person comes to the footer to find a key for), and
// the two keys that find everything else: `:` and `?`. There is no full
// help: `?` opens the keys overlay (keysoverlay.go) instead.
//
// The item order matters, since help.Model truncates from the right.
func (m Model) renderNormalHelpKeyMap() curatedKeyMap {
	reg := m.modes()
	moves := append([]key.Binding(nil), reg.spec(inputNormal).keys.bindings...)
	c := m.leaderConditions()
	up := findBinding(moves, m.keys.Up.Keys())
	down := findBinding(moves, m.keys.Down.Keys())
	focus := m.keys.Focus
	focus.SetEnabled(m.normalBindingEnabled(focus))

	short := []key.Binding{up, down, focus, m.tableHelp(levelProject, actionGoBack, "all projects", c)}
	short = append(short, m.tableHelp(levelProject, actionNewSession, "new", c))
	if m.restartKeyLive() {
		short = append(short, m.tableHelp(levelProject, actionRestart, "restart", c))
	}
	short = append(short,
		m.tableHelp(levelProject, actionDetails, "details", c),
		m.tableHelp(levelProject, actionActionsSearch, "actions", c),
		m.tableHelp(levelProject, actionKeys, "keys", c),
	)
	return curatedKeyMap{short: short, full: short}
}

// renderAllProjectsHelpKeyMap is All projects' help: the all-projects
// screen's (inputAllProjects) and, with no project open, the empty state's,
// which stands in for the screen. enter opens the selected project (or, in
// the empty state, the screen), then the moves where there are rows, then the
// level's own actions, `:`, `?` and quit.
func (m Model) renderAllProjectsHelpKeyMap(mode inputMode) curatedKeyMap {
	reg := m.modes()
	c := m.leaderConditions()
	var moves []key.Binding
	if mode == inputAllProjects {
		moves = append(moves, reg.spec(inputAllProjects).keys.bindings...)
	}
	enter := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open"))
	if mode != inputAllProjects {
		enter.SetHelp("enter", "all projects")
		enter.SetEnabled(m.deps.LoadAllProjects != nil)
	}
	short := []key.Binding{enter}
	if len(moves) >= 2 {
		short = append(short, moves[0], moves[1])
	}
	short = append(short,
		m.tableHelp(levelAllProjects, actionPlanNew, "new plan", c),
		m.tableHelp(levelAllProjects, actionProjectAdd, "add", c),
		m.tableHelp(levelAllProjects, actionProjectRemove, "remove", c),
		m.tableHelp(levelAllProjects, actionActionsSearch, "actions", c),
		m.tableHelp(levelAllProjects, actionKeys, "keys", c),
		m.tableHelp(levelAllProjects, actionQuit, "quit", c),
	)
	return curatedKeyMap{short: short, full: short}
}

// renderDetailHelpKeyMap is the focused overview's help, at Project: esc
// back to the list first (or the state is a trap), enter to open the agent,
// restart while it acts, then the overview's own tab and scroll keys, and
// `:` and `?` last, where truncation drops them before the way out.
func (m Model) renderDetailHelpKeyMap() curatedKeyMap {
	reg := m.modes()
	c := m.leaderConditions()
	own := reg.spec(inputDetail).keys.bindings
	back := key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back to list"))
	short := []key.Binding{back}
	if s, ok := m.selected(); ok && (m.paneForSelection() || m.rowHasAgent(s)) {
		short = append(short, key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open agent")))
	}
	if m.restartKeyLive() {
		short = append(short, m.tableHelp(levelProject, actionRestart, "restart", c))
	}
	short = append(short, own...)
	short = append(short,
		m.tableHelp(levelProject, actionActionsSearch, "actions", c),
		m.tableHelp(levelProject, actionKeys, "keys", c),
	)
	return curatedKeyMap{short: short, full: short}
}

// renderPaneHelpKeyMap builds inputPane's (the Agent's) help.KeyMap, plus
// the literal "input → session · " prefix (not a keybinding, so it is not
// modeled as one). The leader is the one key the dashboard owns in the
// Agent, so the line is the leader (which shows the key strip) and the mouse
// state. The line is budgeted to the column for that state (see
// mouseCaptureHint's doc), so it does not grow a third item.
//
// The mouse entry reads the LIVE capture state (mouseCaptureHint, minus its
// own key prefix) rather than a static "release mouse" — this is the one
// line where that state is worth reading, because it is what the user is
// looking at while typing into the session. It names ctrl+] m, not ctrl+g,
// which the agent owns in a pane.
func (m Model) renderPaneHelpKeyMap() (curatedKeyMap, string) {
	mouseKey := keyName(m.leaderKey.name, levelAgent, actionMouseToggle)
	desc := strings.TrimPrefix(m.mouseCaptureHint(), mouseKey+" ")
	mouse := keyHelpBinding(m.leaderKey.name, levelAgent, actionMouseToggle, desc, true)
	full := []key.Binding{m.keys.Leader, mouse}
	short := []key.Binding{m.keys.Leader, mouse}

	return curatedKeyMap{short: short, full: full}, "input → session · "
}

// keyStripWaiting is the status bar's words while the key strip is open
// (§4.8).
const keyStripWaiting = "waiting for an afk key"

// renderKeyStripHelpKeyMap is the status bar while the key strip is up: what
// it is waiting for, then the two keys that are not an action — esc back to
// Project, and the leader again to send it to the agent.
func (m Model) renderKeyStripHelpKeyMap() (curatedKeyMap, string) {
	leader := m.leaderKey.name
	back := key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	again := key.NewBinding(key.WithKeys(leader), key.WithHelp(leader, "sends "+leader+" to the agent"))
	short := []key.Binding{back, again}
	return curatedKeyMap{short: short, full: short}, keyStripWaiting + " · "
}

// renderHelp renders the footer's help line for whichever mode
// activeInputMode derives: the level modes' keymaps above (built from the key
// table, so the footer names exactly the keys levelKey answers to), the
// overlays' own (mode.go's modeKeyMap), through help.Model, which does the
// width-aware truncation the retired ladder hand-implemented rung by rung.
// The line is always the short help: `?` opens the keys overlay
// (keysoverlay.go), which lists every key, rather than growing the footer.
func (m Model) renderHelp() string {
	mode := activeInputMode(m.modeSnapshot())
	// Every branch subtracts its prefixes from the budget BEFORE rendering,
	// never after: renderModeHelp truncates short help to the width it is
	// handed, so a prefix glued on afterwards is width the line has already
	// spent and the row overflows m.width.
	spin := m.helpSpinnerPrefix()
	width := m.width - lipgloss.Width(spin)

	switch {
	case mode == inputKeyStrip:
		km, prefix := m.renderKeyStripHelpKeyMap()
		return theme.helpLine.Render(spin + prefix + renderStatusHelp(km, false, width-lipgloss.Width(prefix)))
	case mode == inputDetail && m.agentPlaceholderDrawn():
		// The agent placeholder: inputDetail by derivation, but only
		// placeholderKey's keys act there (model.go levelKey). enter and
		// `r` are advertised only where they would act, on the
		// never-advertise-a-no-op rule: while a connect or restart for the
		// row is already out, enter only joins it.
		var short []key.Binding
		if m.placeholderConnectLive() {
			short = append(short, m.keys.Focus)
		}
		if m.restartKeyLive() {
			short = append(short, m.keys.Restart)
		}
		short = append(short, m.keys.Leader)
		return theme.helpLine.Render(spin + renderStatusHelp(curatedKeyMap{short: short}, false, width))
	case mode == inputPane:
		km, prefix := m.renderPaneHelpKeyMap()
		return theme.helpLine.Render(spin + prefix + renderStatusHelp(km, false, width-lipgloss.Width(prefix)))
	case mode == inputAllProjects, mode == inputNormal && !m.hasActiveProject():
		return theme.helpLine.Render(spin + renderStatusHelp(m.renderAllProjectsHelpKeyMap(mode), false, width))
	case mode == inputNormal:
		return theme.helpLine.Render(spin + renderStatusHelp(m.renderNormalHelpKeyMap(), false, width))
	case mode == inputDetail:
		return theme.helpLine.Render(spin + renderStatusHelp(m.renderDetailHelpKeyMap(), false, width))
	case mode == inputInbox:
		// The overlay has two screens with different keys, and the registry's
		// keymap lists both. The line shows the ones for the screen that is
		// up, read off the same bindings the overlay matches against.
		return theme.helpLine.Render(spin + renderStatusHelp(modeKeyMap{bindings: m.inbox.helpKeys()}, false, width))
	case mode == inputPlanCheck:
		// The registry's keymap is the whole surface; it is named here only
		// so the overlay's line does not depend on the default arm's shape.
		reg := m.modes()
		return theme.helpLine.Render(spin + renderStatusHelp(reg.spec(inputPlanCheck).keys, false, width))
	case mode == inputTaskTokens:
		// Named for the plan check's reason: the line is the registry's
		// keymap, whatever the default arm becomes.
		reg := m.modes()
		return theme.helpLine.Render(spin + renderStatusHelp(reg.spec(inputTaskTokens).keys, false, width))
	default:
		reg := m.modes()
		return theme.helpLine.Render(spin + renderStatusHelp(reg.spec(mode).keys, false, width))
	}
}

// helpSpinnerPrefix is the in-flight spinner's current frame, ready to lead the
// help row, or "" when nothing this dashboard dispatched is outstanding.
//
// The spinner lives on the help row and NOT in the toast, which is the surface
// that took over every other notification: a create or a clean routinely runs
// longer than toastTTL, so a toast-hosted spinner would vanish mid-operation
// and take the only "something is still running" signal with it.
//
// The gate is hasInFlightWork alone. It used to also require a non-empty
// status, because the spinner rode beside the status text and a preview dial
// sets none — but hasInFlightWork already excludes that dial (see its doc), and
// there is no message here for a stray spinner to attach itself to.
func (m Model) helpSpinnerPrefix() string {
	if !m.hasInFlightWork() {
		return ""
	}
	return m.spinner.View() + " "
}

// minSidebarWidth is the sidebar's floor: even on a very narrow terminal it
// keeps enough room for a marker, a truncated name, and the awaiting-input
// indicator without collapsing to nothing. 30 is the outer width whose content
// (sidebarContentWidth: 26) lands on rowColumns' 24–31 rung — an abbreviated
// PHASE column and a 14-cell name — which is the narrowest row that still
// reads as a table. An 80-column terminal gets exactly this floor.
const minSidebarWidth = 30

// sidebarWidthPct is the sidebar's DEFAULT proportional share of the terminal
// width, used whenever Model.sidebarPct is unset (a split persisted before
// resize mode was removed still overrides it). The width is re-derived from termWidth on
// every View() call, so it tracks tea.WindowSizeMsg. 38% puts a
// 120-column terminal at outer 45 / content 41, just over rowColumns' 40-cell
// rung where the PHASE column shows the full word.
const sidebarWidthPct = 38

// minSidebarPct and maxSidebarPct bound the sidebar's percentage share so
// neither region can be dragged out of existence; minMainWidth is the main
// region's own column floor, applied before minSidebarWidth so the sidebar's
// floor still wins on a terminal too narrow for both.
const (
	minSidebarPct = 15
	maxSidebarPct = 85
	minMainWidth  = 30
)

// clampSidebarPct maps a stored sidebar percentage onto [minSidebarPct,
// maxSidebarPct]; zero or negative means "unset" and yields the default.
func clampSidebarPct(pct int) int {
	if pct <= 0 {
		return sidebarWidthPct
	}
	return min(max(pct, minSidebarPct), maxSidebarPct)
}

// sidebarOuterWidth is the sidebar's outer (bordered) width for a given
// terminal width at the default split.
func sidebarOuterWidth(termWidth int) int {
	return sidebarOuterWidthFor(termWidth, sidebarWidthPct)
}

// sidebarOuterWidthFor is the sidebar's outer (bordered) width for a terminal
// width and a split percentage: the clamped proportional share, held back so
// the main region keeps minMainWidth columns, floored at minSidebarWidth so
// the list never collapses on a narrow terminal (that floor wins over the main
// region's), and capped at termWidth so it never claims more than exists.
func sidebarOuterWidthFor(termWidth, pct int) int {
	w := termWidth * clampSidebarPct(pct) / 100
	if w > termWidth-minMainWidth {
		w = termWidth - minMainWidth
	}
	if w < minSidebarWidth {
		w = minSidebarWidth
	}
	if w > termWidth {
		w = termWidth
	}
	return w
}

// sessionListFallbackLabel is renderList's box title when no project is
// known. It is load-bearing, not defensive padding: every zero-Deps Model in
// this package's test suite never sets Deps.Projects, so this fallback is
// what most of the suite's frames already render, not a rare corner — a
// blank box label would be worse than the word it replaces.
const sessionListFallbackLabel = "Sessions"

// sessionListLabel is renderList's box title: the active project's name when
// m.activeProject resolves to one, sessionListFallbackLabel otherwise. There
// is deliberately no len(m.projects) > 1 gate — the box holds exactly the
// active project's sessions regardless of how many other projects are known,
// so the label names it even when it is the only project configured.
func (m Model) sessionListLabel() string {
	if idx := m.activeProject; idx >= 0 && idx < len(m.projects) {
		return m.projects[idx].Name
	}
	return sessionListFallbackLabel
}

// renderList draws the session list inside a bordered box of the given outer
// width/height: a caption line naming the columns, then the list component's
// windowed page of one-line rows.
//
// The box is contentW wide inside one cell of padding on each side and the
// border, the size sizedBox(theme.listBox, contentW+2, …) gives (a lipgloss
// block size INCLUDES padding, so the wrap point lands exactly at contentW and
// no full-width row wraps inside its own box). sidebarRenderedWidth mirrors
// this arithmetic and must move with it.
//
// The pane part draws the box: it lays the rows out at contentW inside one
// cell of padding, the same geometry, and carries the selected row's fill out
// to the border (§3.5).
//
// The pane's title strip (§3.3) is its first inner row: the project's name on
// the left, since this box is the one place on the frame that says which
// project you are looking at, and the paging hint on the right. The strip
// takes a row from the list (listBodyRows; sizeSessionList sizes the stored
// list from the same helper), and titleStrip cuts the name with "…" to the
// room there is.
func (m Model) renderList(outerW, outerH int) string {
	contentW := sidebarContentWidth(outerW)
	rows := listBodyRows(outerH)
	return theme.pane(m.sessionListLabel(), m.listPagingHint(contentW, rows),
		m.renderRowsBody(contentW, rows), paneOuterWidth(contentW), outerH, m.listOwnsKeyboard())
}

// listBodyRows is how many rows the session list's pane has under its title
// strip for a pane outerH rows tall. renderList draws that many and
// sizeSessionList (model.go) sizes the stored list from it, so the paging
// keys move by the page the frame shows.
func listBodyRows(outerH int) int {
	return outerH - boxBorderCells - paneStripRows
}

// listOwnsKeyboard is listBoxStyle's question on its own: the list is focused
// and no creation form is open. The list's border and its title strip both
// follow it, so they cannot disagree.
func (m Model) listOwnsKeyboard() bool {
	return !m.formOpen && m.focus != focusRight
}

// listPagingHint is the context on the session box's title strip: the page
// position and the keys that move it, shown only when the list actually pages. It sizes a
// copy of the list exactly as renderRowsBody does so the page count it reports
// is the one the rows are drawn from. The key names are literal because the
// paging bindings are mode-local (newModeRegistry) and not on keyMap.
func (m Model) listPagingHint(contentW, contentH int) string {
	if len(m.sessions) == 0 {
		return ""
	}
	l := m.list
	l.SetSize(contentW, contentH-sidebarCaptionRows)
	if l.Paginator.TotalPages <= 1 {
		return ""
	}
	return fmt.Sprintf("page %d/%d · pgup/pgdn", l.Paginator.Page+1, l.Paginator.TotalPages)
}

// sidebarContentWidth converts the sidebar's outer (bordered) width into the
// inner content width renderRowsBody draws into: 2 cells for the border plus
// 1 cell of horizontal padding on each side (theme.listBox's Padding(0, 1)).
func sidebarContentWidth(outerW int) int {
	contentW := outerW - 4
	if contentW < 10 {
		contentW = 10
	}
	return contentW
}

// renderRowsBody builds the session list's inner content (everything
// theme.listBox's border/padding wraps around): either the empty-state
// message, or the list component's windowed page of rows.
//
// The empty state is drawn HERE rather than through list.Model's own NoItems
// view because bubbles renders a single styled "No items." line and this
// dashboard's empty state is two lines that name the key that fixes it.
//
// The list is sized on a COPY, per frame, instead of being resized only when
// tea.WindowSizeMsg arrives: the body height also moves when the header or the
// footer changes line count (a modal's taller footer, a load error in the
// header), and those carry no resize. The copy is safe against selection drift
// because list.updatePagination re-derives page and cursor from Index(), so a
// PerPage change preserves which item is selected — the stored Model and this
// copy always agree on that, and it is Index() the delegate highlights.
func (m Model) renderRowsBody(contentW, contentH int) string {
	if len(m.sessions) == 0 {
		var b strings.Builder
		b.WriteString(theme.dim.Render("No afk sessions found."))
		b.WriteString("\n")
		b.WriteString(theme.dim.Render("Press " + keyName(m.leaderKey.name, levelProject, actionNewSession) + " to start one."))
		return b.String()
	}
	l := m.list
	l.SetSize(contentW, contentH-sidebarCaptionRows)
	return lipgloss.JoinVertical(lipgloss.Left, renderRowCaptions(contentW), l.View())
}

// sidebarCaptionRows is how many rows renderRowsBody spends above the list
// component — the column captions. The filter prompt's row is NOT counted
// here: list.Model reserves that one itself inside whatever height it is
// given (newSessionList's doc), and it doubles as the gap between the captions
// and the first row while no filter is being typed. sizeSessionList (model.go)
// must subtract the same number, or the paging keys move by a page the frame
// does not show.
const sidebarCaptionRows = 1

// renderRowCaptions is the table's header line, laid out by the same
// rowColumns answer the rows use so each caption sits over its column. The
// marker and host-dot columns have no caption; they are self-explanatory
// once a row is under them.
func renderRowCaptions(width int) string {
	l := rowColumns(width)
	return theme.columnCaption.Render(composeRow(l, "  ", " ", "NAME", "PHASE", "TASKS", " ", width))
}

// sessionPhaseKey returns the phase key renderRow's badges key off, folding a
// stale status to the neutral "unknown" badge. A scratch session (no
// lifecycle at all, per discover.SessionState.Scratch) is checked first and
// ahead of StatusStale, since it is never stale — it simply has no status.json
// to be stale about — and must render the scratch badge, not "unknown".
func sessionPhaseKey(s Session) string {
	if s.Scratch {
		return scratchPhaseKey
	}
	if s.StatusStale {
		return ""
	}
	return string(s.Phase)
}

// displayName returns what a row or the detail header should show as s's
// primary name: the mutable label (discover.SessionState.Label, W4.T1) when
// one is set, falling back to the real on-disk name otherwise. Every caller
// that shows this text must also show s.Name somewhere nearby (dimmed) when
// a label is set — the plan's explicit "the on-disk identity is never
// hidden" requirement — since this function alone cannot be told apart from
// a session that simply has no label.
func displayName(s Session) string {
	if s.Label != "" {
		return s.Label
	}
	return s.Name
}

// hostRunningGlyph / hostStoppedGlyph are the row's host-liveness glyphs for
// a session still in its lifecycle: ◐ for a session whose `afk _host` is
// alive, ○ for a stopped host. The column is independent of the phase word on
// purpose — status.json can still say "executing" long after the host died,
// and the glyph is what keeps that stale word from reading as a live session.
//
// Both come from stateLook (§3.6): a live host is ◐ running ("running" is one
// of §3.6's working words), a stopped one is ○ quiet.
var (
	hostRunningGlyph = stateLook(stateSession, hostRunningState).glyph
	hostStoppedGlyph = stateLook(stateSession, hostStoppedState).glyph
)

// hostIndicator is a session row's glyph: §3.6's look for the session, from
// stateLook, the one mapping. A session that has ended says so whatever its
// host is doing (✓ done, ✕ failed), as does a worktree fault (✕ BROKEN,
// ● ORPHAN); a session still in its lifecycle shows its host's liveness, ◐
// running or ○ stopped, which is what tells a live session from a stale
// "executing" word. A usage pause is quiet (§3.6 lists paused with the quiet
// states); the PHASE column beside it is what says PAUSED, in the warning
// colour. A pending placeholder has no host yet, so it is quiet too.
func hostIndicator(s Session) string {
	kind, value := sessionGlyphState(s)
	return theme.renderGlyph(kind, value)
}

// sessionGlyphState is hostIndicator's choice of which §3.6 state a row's
// glyph shows, in stateLook's terms, split out so the choice is a pure
// function of the session with every branch a table row
// (TestHostIndicatorUsesTheStateMapping).
func sessionGlyphState(s Session) (stateKind, string) {
	switch {
	case s.Pending:
		return stateSession, hostStoppedState
	case isWorktreeFault(s.Kind):
		return stateFault, faultBadgeLabel(s.Kind)
	case !s.StatusStale && !s.Scratch && s.Phase.Terminal():
		return stateSession, string(s.Phase)
	case s.HostRunning:
		return stateSession, hostRunningState
	case usagePaused(s):
		return stateSession, "paused"
	}
	return stateSession, hostStoppedState
}

// showsAwaitingInput reports whether the row should draw the awaiting-input
// badge. status-schema.md's C4 requires the write that sets `done`/`failed` to
// clear awaiting_input, so a terminal phase carrying the flag is stale — and
// permanently so, since hooks stop matching terminal files and the conductor
// won't rewrite them.
//
// Do NOT extend this to suppress on !HostRunning: a standalone `/afk:go`
// session has no host.json and so reports false forever, which would hide the
// badge from the sessions that most need it. No StatusStale branch is needed
// either — discover.go:261-265 returns before assigning AwaitingInput.
func showsAwaitingInput(s Session) bool {
	return s.AwaitingInput && !s.Phase.Terminal()
}

// wedgedGlyph marks a row whose child has gone silent past the host's probe
// deadline (internal/host/liveness.go's `wedged_since`, W3.T1), mirrored onto
// Session via discover.SessionState.WedgedSince (W5.T1).
const wedgedGlyph = "⚠"

// showsWedged reports whether renderRow should draw the wedged badge for s,
// and if so, how long ago the host's liveness monitor set WedgedSince.
//
// WedgedSince == "" means "not wedged", which reads the same as "unknown"
// (no host.json, or a pre-W1.T2 host that never wrote the field — see
// SessionState.WedgedSince's doc) — both render exactly as they do today,
// with no badge. A non-empty value that fails to parse (the host only ever
// writes RFC3339, so this should not happen in practice) is treated the
// same as unknown rather than fabricated into a wedged state with an age
// that isn't real.
//
// HostRunning gates the verdict because it is only as live as the host that
// wrote it: a SIGKILLed host never removes its host.json, so its recorded
// wedged_since reads back forever, and a badge with an ever-growing age on a
// session whose real state is "stopped" is dishonest twice over — the row's
// liveness dot already says the host is gone, and the restart the badge
// advertises would dial a socket nobody serves. HostRunning is advisory (a
// recycled pid can fake it), and the failure mode of that false positive is
// today's behavior, not a new one.
func showsWedged(s Session, now time.Time) (age time.Duration, ok bool) {
	if !s.HostRunning {
		return 0, false
	}
	if s.WedgedSince == "" {
		return 0, false
	}
	since, err := time.Parse(time.RFC3339, s.WedgedSince)
	if err != nil {
		return 0, false
	}
	return now.Sub(since), true
}

// Column geometry for a session row. Every row is ONE line: a selection
// marker, the host-liveness dot, the name, then the PHASE and TASKS columns and
// a one-cell flag for awaiting-input / wedged. Only the name is elastic; the
// rest are fixed so the columns line up down the list — which the two-line
// cards this replaced, with a badge trailing a variable-width name, never did.
const (
	rowMarkerCells      = 2 // cursorRail + " ", or two spaces
	rowHostCells        = 2 // liveness dot + space
	rowFlagCells        = 1 // "●" awaiting, "⚠" wedged, "↻" autonomous, or blank
	rowPhaseFullWidth   = 9 // "EXECUTING", "REVIEWING"
	rowPhaseAbbrevWidth = 5 // "REVW", "STALE", "BRKN" — see phaseAbbrev
	rowTasksWidth       = 5 // "13/20"
)

// rowLayout is rowColumns' answer: the width of each column, 0 for a column
// dropped at this width, and whether PHASE shows its abbreviated form.
type rowLayout struct {
	name, phase, tasks int
	abbrevPhase        bool
}

// rowColumns decides which columns a session row shows at width and how wide
// each is. It is pure so the ladder is table-testable, and renderRowCaptions
// uses the same answer so captions sit over their columns.
//
// The rungs: at 40+ cells the PHASE column holds the full word; 32–39 abbreviates
// it; 24–31 also drops TASKS; below 24 only the name and the flag remain. The
// name gets whatever is left, floored at one cell so a row is never empty.
func rowColumns(width int) rowLayout {
	var l rowLayout
	switch {
	case width >= 40:
		l.phase, l.tasks = rowPhaseFullWidth, rowTasksWidth
	case width >= 32:
		l.phase, l.tasks, l.abbrevPhase = rowPhaseAbbrevWidth, rowTasksWidth, true
	case width >= 24:
		l.phase, l.abbrevPhase = rowPhaseAbbrevWidth, true
	}
	// Fixed overhead: marker, dot + space, the gap before the flag, the flag.
	l.name = width - rowMarkerCells - rowHostCells - 1 - rowFlagCells
	if l.phase > 0 {
		l.name -= l.phase + 1
	}
	if l.tasks > 0 {
		l.name -= l.tasks + 1
	}
	if l.name < 1 {
		l.name = 1
	}
	return l
}

// composeRow lays cells out in rowColumns' order — marker, dot, name, then the
// optional PHASE and TASKS cells, a gap, and the flag. Every cell is truncated
// and padded to its own column (cell), so styling or a long value inside one
// can never shift the next column. dot is the one-cell liveness glyph; the
// space after it is added here. The closing truncate is a backstop for the
// sub-24 rung, where the arithmetic already sums to width but the name column
// can be a single cell.
func composeRow(l rowLayout, marker, dot, name, phase, tasks, flag string, width int) string {
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString(dot)
	b.WriteString(" ")
	b.WriteString(cell(name, l.name, lipgloss.Left))
	if l.phase > 0 {
		b.WriteString(" ")
		b.WriteString(cell(phase, l.phase, lipgloss.Left))
	}
	if l.tasks > 0 {
		b.WriteString(" ")
		b.WriteString(cell(tasks, l.tasks, lipgloss.Right))
	}
	b.WriteString(" ")
	b.WriteString(cell(flag, rowFlagCells, lipgloss.Left))
	return truncate(b.String(), width)
}

// cell truncates s to exactly w cells and pads the remainder on the side align
// leaves open. Both steps are ANSI-aware (truncate, lipgloss.Width), so a
// styled value measures as its visible glyphs.
func cell(s string, w int, align lipgloss.Position) string {
	s = truncate(s, w)
	pad := w - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	if align == lipgloss.Right {
		return strings.Repeat(" ", pad) + s
	}
	return s + strings.Repeat(" ", pad)
}

// awaitingGlyph is the flag column's awaiting-input mark. It is the same dot
// the project switcher draws for a project with a waiting session, so the two
// surfaces say "needs you" the same way.
//
// It is §3.6's needs-you glyph, read from stateLook so it cannot drift from
// the mapping.
var awaitingGlyph = stateLook(stateSession, awaitingInputState).glyph

// autonomousGlyph marks a row whose session runs in autonomous mode (the
// `.afk/{name}/autonomous` marker, surfaced as SessionState.Autonomous).
// U+21BB is East Asian Neutral, so it is one cell in CJK-locale terminals
// where the Ambiguous-width "●", "◆" and "∞" can render as two and shear the
// fixed row columns; it is also in common monospace fonts, unlike the
// dingbat/emoji clock and robot glyphs that fall back to a wide emoji font.
// The loop arrow reads as "keeps going on its own". It is drawn muted
// (theme.dim): autonomy is a standing property, not something that needs
// attention, so it must never compete with the wedged/awaiting flags.
const autonomousGlyph = "↻"

// cursorRail is the selected row's left edge: a half-block, which reads as a continuous rail down the sidebar where the chevron it
// replaced read as one more piece of punctuation among the row's other glyphs
// (the liveness dot, the awaiting dot, the wedged warning). It measures one
// cell, so it fits rowMarkerCells' budget exactly as "›" did —
// TestCursorRailIsOneCell pins that, because a two-cell marker would shift
// every column right on the selected row alone.
const cursorRail = "▌"

// rowMarker renders a row's marker column. Both branches are exactly
// rowMarkerCells wide: the unselected row pays for the rail's cell in
// whitespace so the two line up and the name column never shifts under the
// cursor.
//
// The rail is plain: the only rows that draw it are selected ones, and the
// row part redraws a selected row in sel-fg on sel (§3.5), so a colour given
// here would be stripped before it reached the screen.
func rowMarker(selected bool) string {
	if selected {
		return cursorRail + " "
	}
	return "  "
}

// renderRow renders one session as a one-line table row (rowColumns): host
// liveness dot, name, PHASE, TASKS, and the awaiting/wedged flag. Everything
// that used to ride the row's second line — the progress bar, the branch, the
// stale reason, the wedged age, a label's on-disk name — lives in the detail
// pane now, where there is room to read it.
func (m Model) renderRow(s Session, selected bool, width int) string {
	return renderSessionRow(s, selected, width, m.deps.Now())
}

// renderSessionRow is renderRow with the one ambient fact it reads passed in,
// so sessionDelegate can draw a row without holding a Model.
func renderSessionRow(s Session, selected bool, width int, now time.Time) string {
	if width < 1 {
		width = 1
	}
	marker := rowMarker(selected)
	l := rowColumns(width)

	if s.Pending {
		return renderPendingRow(s, selected, width, marker, l)
	}
	if isWorktreeFault(s.Kind) {
		return renderFaultRow(s, selected, width, marker, l)
	}

	name := renderRowName(s, l.name)

	// A stale status has no trustworthy phase, so the column says so instead
	// of showing the neutral UNKNOWN a corrupt file would otherwise wear —
	// the two are different faults with different fixes.
	//
	// A session paused for usage says that in place of its phase. status.json
	// still holds the phase it was stopped in, and "EXECUTING" beside a hollow
	// dot is how a stopped session reads; the owner has nothing to fix here.
	var phase string
	switch {
	case s.StatusStale:
		phase = theme.staleText.Render("STALE")
	case usagePaused(s):
		phase = theme.awaitingFlag.Render(usagePausedLabel(l.abbrevPhase))
	default:
		phase = theme.phaseText(sessionPhaseKey(s), l.abbrevPhase)
	}

	// Wedged outranks awaiting in the single flag cell: a wedged child is not
	// going to read the answer anyway, and `r` is the key that helps.
	var flag string
	switch {
	case wedgedNow(s, now):
		flag = theme.errorText.Render(wedgedGlyph)
	case showsAwaitingInput(s):
		flag = theme.awaitingFlag.Render(awaitingGlyph)
	case s.Autonomous:
		// Lowest priority: both attention flags above need the user, this one
		// only informs. The column stays one cell (rowFlagCells).
		flag = theme.dim.Render(autonomousGlyph)
	}

	line := composeRow(l, marker, hostIndicator(s), name, phase, taskCount(s), flag, width)
	return padSessionRow(line, selected, width)
}

// rowProviderMinName is the fewest name cells a row keeps before it drops the
// provider tag: a tag that leaves a one-cell name trades the identifying
// fact for a decoration.
const rowProviderMinName = 3

// providerTag is the dim " codex" suffix a row's name cell carries for a
// session on a non-default provider, "" for the default (Provider is "" for
// claude, see discover.SessionState.Provider).
func providerTag(s Session) string {
	if s.Provider == "" {
		return ""
	}
	return " " + s.Provider
}

// renderRowName draws the name cell's content within budget cells: the name,
// then the provider tag. The name is truncated first so the tag survives and
// the cell never exceeds budget, which keeps rowColumns' column math (and so
// every column to the right) unchanged. When even a short name cannot keep
// rowProviderMinName cells beside the tag, the tag is dropped instead.
func renderRowName(s Session, budget int) string {
	name := displayName(s)
	tag := providerTag(s)
	tagW := lipgloss.Width(tag)
	if tag == "" || budget-tagW < min(lipgloss.Width(name), rowProviderMinName) {
		return theme.sessionName.Render(truncate(name, budget))
	}
	return theme.sessionName.Render(truncate(name, budget-tagW)) + theme.dim.Render(tag)
}

// wedgedNow is showsWedged's boolean form for callers that do not draw the age.
func wedgedNow(s Session, now time.Time) bool {
	_, ok := showsWedged(s, now)
	return ok
}

// taskCount is the TASKS cell: "done/total", red when any task failed (the
// count itself is the only room the cell has to say so — the detail pane names
// the failures), and a dim dash for a session with nothing to count: a scratch
// session, a plan not yet written, or a stale status whose progress was zeroed.
func taskCount(s Session) string {
	if s.Scratch || s.StatusStale || s.Progress.Total == 0 {
		return theme.dim.Render("–")
	}
	label := fmt.Sprintf("%d/%d", s.Progress.Complete, s.Progress.Total)
	if s.Progress.Failed > 0 {
		return theme.taskFailed.Render(label)
	}
	return theme.dim.Render(label)
}

// padSessionRow pads a finished row out to the row width — BOTH branches, not
// just the selected one. An unselected row left at its natural length renders
// shorter than the highlight bar, which is the switcher's W7.T1 defect (2)
// replayed on this surface: the padded row was the only one ever exactly as
// wide as the containing box's wrap point, so the wrap read as selection-only.
// The unselected branch changes only trailing whitespace, never how a row
// looks.
//
// composeRow already sums its cells to width, so in practice this only applies
// the selection; it stays as the invariant's owner in case a caller ever
// hands it a shorter line. It cannot wrap: the row part cuts the line to width
// and adds no border or padding (the pane's padding is the row's).
//
// Both branches are the row part (§3.5). A selected row is redrawn from its
// plain text in sel-fg on sel, filled edge to edge, so every word on it —
// the state glyph, the PHASE colour, the flag glyph — turns sel-fg and the
// fill always reads; the flag keeps its glyph, which is what still tells
// awaiting (●) from wedged (⚠) and autonomous (↻) on the selection.
func padSessionRow(line string, selected bool, width int) string {
	return theme.row(line, width, selected, "")
}

// renderPendingRow renders the placeholder row for a session whose background
// `afk new` is still running. Its PHASE cell says so and its TASKS cell is
// the dash, on purpose: nothing about the session exists to report yet, and a
// 0/0 count would read as a real session making no progress.
func renderPendingRow(s Session, selected bool, width int, marker string, l rowLayout) string {
	name := theme.sessionName.Render(truncate(s.Name, l.name))
	phase := theme.dim.Render(truncate(pendingBadge, l.phase))
	line := composeRow(l, marker, hostIndicator(s), name, phase, theme.dim.Render("–"), "", width)
	return padSessionRow(line, selected, width)
}

// pendingBadge is the placeholder row's stand-in for a phase.
const pendingBadge = "creating…"

// faultBadgeLabel maps a worktree-side fault to the label its row wears. It
// reads the Kind constants rather than re-typing their string values, which do
// not match the label (KindWorktreeMissing is "worktree-no-directory") and are
// internal/session's to change.
func faultBadgeLabel(kind session.Kind) string {
	if kind == session.KindWorktreeMissing {
		return brokenBadgeLabel
	}
	return orphanBadgeLabel
}

// renderFaultRow renders the row for a worktree-side fault: a directory git
// still has registered but that no longer exists, or a worktree carrying no
// session of its own. The fault label takes the PHASE column — this entry has
// no status.json and no plan, so a phase could only be the UNKNOWN a CORRUPT
// status file wears, a different fault with a different fix — and the reason
// itself, the one thing that says what is wrong, is in the detail pane.
//
// A free function like renderPendingRow, not a method: renderSessionRow is
// what sessionDelegate calls to draw a row without holding a Model, and this
// branch has no ambient Model fact to read.
func renderFaultRow(s Session, selected bool, width int, marker string, l rowLayout) string {
	name := theme.sessionName.Render(truncate(displayName(s), l.name))
	phase := theme.faultText(faultBadgeLabel(s.Kind), l.abbrevPhase)
	line := composeRow(l, marker, hostIndicator(s), name, phase, theme.dim.Render("–"), "", width)
	return padSessionRow(line, selected, width)
}

// renderDetail draws the detail pane for the selected session, composed of
// the tab strip and the active tab's panels and handed to boxDetailBody, which bounds it to the
// box's row budget BEFORE the box is composed — the bound-content-then-box
// discipline renderList (via the list's pagination) and renderPane (via the
// emulator's row count) already follow.
//
// A trailing MaxHeight cannot stand in for that, which is what the arrangement
// here before got wrong: lipgloss v2.0.6 applies the border first
// (style.go:501) and only then slices for MaxHeight (style.go:516-522), while
// Height() merely PADS short content and returns a too-tall block unchanged
// (style.go:480-483 → align.go:61-65). An overlong plan therefore reached
// MaxHeight already bordered and lost its trailing rows — the row COUNT came
// out exactly right, so a height-only test stayed green, but the row that fell
// off the end was the closing `╰───╯` and the box hung open mid-panel.
//
// Both dimensions come from the pure helpers below (detailContentWidth,
// detailRowBudget) and from nowhere else, because syncDetailViewport
// (model.go) sizes the Model's viewport — the one the scroll keys move — from
// the same two helpers. See detailRowBudget's doc for what happens when the
// two sides disagree by even one row.
//
// The title is the pane's title strip (§3.3), its first inner row, accent
// while the box has the keyboard; detailRowBudget gives the body the rows
// under it. The keys stay on the bottom edge (labelBorders), drawn in
// detailBoxStyle, whose border glyphs and colour are the pane's for the same
// focus (both rounded, line or accent by detailOwnsKeyboard).
func (m Model) renderDetail(outerW, outerH int) string {
	contentW := detailContentWidth(outerW)
	title, hint := m.detailBoxLabels()
	box := m.boxDetailBody(m.detailBody(contentW), contentW, outerH, title, m.detailOwnsKeyboard())
	return labelBorders(box, m.detailBoxStyle(), "", hint)
}

// detailBoxLabels is the detail box's edge text: the selected session's
// display name on top, and along the bottom the per-session keys that act on
// it, spelled from the keymap so the edge and the dispatch cannot disagree.
// A fault row offers only clean (it cannot be opened or labelled), a
// placeholder nothing yet, and no selection a generic title.
//
// Focused (W3.T1), the bottom edge is the release hint REGARDLESS of the
// selected row's kind: enter/c/l are all dead while inputDetail owns the
// keyboard (its rule table, mode.go, omits them), so the per-session hint
// below would advertise keys that do nothing. The title still names whatever
// is selected — focus does not change what the box is looking at, only
// which keys act on it.
func (m Model) detailBoxLabels() (title, hint string) {
	s, ok := m.selected()
	title = "Details"
	if ok {
		title = displayName(s)
	}
	if m.detailDrawn() && m.activeDetailTab() == tabBuild {
		// The Build tab is about the project, so the box does not name a
		// session over it.
		title = "Build"
	}
	if m.detailOwnsKeyboard() {
		return title, m.detailReleaseHint()
	}
	if !ok {
		return title, ""
	}
	switch {
	case s.Pending:
		focus := m.keys.Focus.Help()
		return title, fmt.Sprintf("%s: %s", focus.Key, focus.Desc)
	case isWorktreeFault(s.Kind):
		focus := m.keys.Focus.Help()
		return title, fmt.Sprintf("%s: %s · %s: clean",
			focus.Key, focus.Desc, keyName(m.leaderKey.name, levelProject, actionClean))
	}
	focus := m.keys.Focus.Help()
	return title, fmt.Sprintf("%s: %s · %s: clean · %s: label",
		focus.Key, focus.Desc, keyName(m.leaderKey.name, levelProject, actionClean), keyName(m.leaderKey.name, levelProject, actionEditLabel))
}

// detailReleaseHint is detailBoxLabels' focused-mode hint: the one live way
// out, read from the key table (keyName) rather than a duplicated literal, so
// the footer and this edge cannot say two different things about the same
// key.
func (m Model) detailReleaseHint() string {
	return keyName(m.leaderKey.name, levelProject, actionGoBack) + ": back to list"
}

// detailContentWidth is the detail box's usable text width for a box outerW
// cells wide: the border (one cell a side) and detailBox's Padding(0, 1) trim
// four, floored at 10 so a very narrow terminal still gets a readable column.
// boxDetailBody hands sizedBox contentW+2, not contentW: sizedBox's argument
// includes detailBox's Padding(0, 1), and the panels draw lines up to contentW
// wide — see renderSwitcher's doc for the full arithmetic.
//
// Pure, and the ONLY place this arithmetic lives: renderDetail sizes the frame
// with it and syncDetailViewport (model.go) seeds the Model's viewport with it,
// so the lines the keys scroll are byte-for-byte the lines the frame draws.
func detailContentWidth(outerW int) int {
	contentW := outerW - 4
	if contentW < 10 {
		contentW = 10
	}
	return contentW
}

// detailRowBudget decides how many rows of body the detail box shows and
// whether its last inner row is given to the scroll readout, for a box outerH
// rows tall (border included — the pane part has horizontal padding only, so
// the border and the title strip, paneStripRows, are the whole vertical
// overhead) holding totalLines of body.
// The readout row is reserved ONLY when the body overflows: when it fits there
// is nothing to report and the row is worth more as content. A one-row box
// never reserves it, since a readout with no content above it says nothing.
//
// Pure, and the single source of truth for the pane's visible row count. Both
// sides of the scroll MUST size from it: the Model's viewport that the keys
// move (syncDetailViewport, model.go) and the per-frame copy boxDetailBody
// draws. bubbles' viewport clamps its offset to TotalLineCount()-Height()
// (viewport.go maxYOffset), so a Model viewport even one row taller than the
// drawn one has a ceiling one row too low, and the last row of content is
// unreachable from the keyboard no matter how often the user scrolls. That is
// exactly how this plan's first cut failed review: the Model side took
// bodyHeight (the box's OUTER height) while the frame drew bodyHeight-3 rows,
// so the readout stalled at 97% and the final panel line never appeared.
func detailRowBudget(outerH, totalLines int) (rows int, readout bool) {
	innerH := outerH - boxBorderCells - paneStripRows
	if innerH < 1 {
		innerH = 1
	}
	if totalLines > innerH && innerH > 1 {
		return innerH - 1, true
	}
	return innerH, false
}

// detailBody is the detail pane's body for the selected session at the given
// content width: composeDetailBody's text, soft-wrapped at contentW with the
// same lipgloss.Wrap call a bordered style's Width() makes (lipgloss/v2@v2.0.6
// style.go:411-414, wrapAt = width - padding), so an over-wide line — the
// pending/fault/no-selection messages on an 80-column terminal, the
// untruncated session/branch head, verdictPanel's lines — folds onto the next
// row exactly as it did when sizedBox wrapped the content itself. Without this
// the viewport, whose SoftWrap is off (see boxDetailBody), would ansi.Cut those
// lines instead and silently drop their tails ("...is running in the" with no
// "background"). Wrapping here rather than in the viewport keeps the height
// exact — every line is at most contentW wide before the row budget is
// applied, so nothing can fold again after it.
//
// It is the one text both consumers see: renderDetail draws it and
// syncDetailViewport (model.go) seeds the Model's viewport with it before a
// scroll key moves the offset (View may not: it takes a value receiver, so a
// mutation there lands on a copy the caller discards — see the detailViewport
// field's doc). It is safe for Update to call: it only reads
// m.selected()/m.sessions/m.stack/m.detailTab, and every panel.Render call
// is pure.
func (m Model) detailBody(contentW int) string {
	return lipgloss.Wrap(m.composeDetailBody(contentW), contentW, "")
}

// composeDetailBody builds the detail pane's unwrapped, unbounded text: the
// no-selection, placeholder and worktree-fault messages, or the tab strip plus
// the active tab's panels. detailBody is its only caller.
//
// A project with a build draws the tab strip in every one of those states.
// The Build tab is about the project and not the selected row, so it has to
// be reachable, and read the same, with a placeholder or a fault selected and
// with no session at all. A build with nothing running is exactly when the
// task list is the thing to look at. Without a build the three messages stand
// alone, as they always have.
func (m Model) composeDetailBody(contentW int) string {
	var b strings.Builder
	s, ok := m.selected()
	hasBuild := m.hasBuild()
	notice := rowNotice(m.leaderKey.name, s, ok)
	if notice != "" && !hasBuild {
		return notice
	}

	// The tab strip and its rule, then the active tab's panels. The session's
	// head (name, branch, phase) is the Overview's first lines rather than a
	// fixture above every tab: the strip is the pane's title row now, and a
	// head repeated over Waves or Stack would cost two rows on the tab that
	// most needs them.
	// The strip and the rule are detailStripLines lines; the Build tab's
	// cursor counts on that (revealBuildCursor).
	tabs := visibleDetailTabs(m.stack, hasBuild)
	active := m.activeDetailTab()
	b.WriteString(renderTabStrip(tabs, active, contentW))
	b.WriteString("\n")
	b.WriteString(theme.dim.Render(strings.Repeat("─", contentW)))
	b.WriteString("\n")

	if notice != "" && active != tabBuild {
		// Every other tab is about the selected session, and this row has
		// none to show: the message takes the tab's place under the strip.
		b.WriteString(notice)
		return b.String()
	}

	ctx := panelContext{
		Session: s, Sessions: m.sessions, Stack: m.stack, Now: m.deps.Now(), Build: m.build, PlanStatus: m.planStatus,
		BuildCursorShown: m.buildCursorShown(), BuildCursor: m.buildCursorIndex,
		BuildCursorTotal: m.buildCursorTotal(),
		Leader:           m.leaderKey.name,
	}
	spec := detailTabSpecs()[active]
	rendered := 0
	for _, p := range spec.panels {
		lines := p.Render(ctx, contentW)
		if len(lines) == 0 {
			continue
		}
		rendered++
		// A single-panel tab is titled by the strip; only a tab that stacks
		// several panels (Notes) needs a heading over each.
		if len(spec.panels) > 1 {
			b.WriteString("\n")
			b.WriteString(theme.panelTitle.Render(p.Title()))
			b.WriteString("\n")
		}
		for _, ln := range lines {
			b.WriteString(ln)
			b.WriteString("\n")
		}
	}
	if rendered == 0 {
		// Every panel declined (panel.Render's nil means "hide me"). Inside a
		// tab the user chose, an empty box would read as a rendering fault, so
		// the tab says in one line why there is nothing here.
		b.WriteString("\n")
		b.WriteString(theme.dim.Render(spec.empty(s)))
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// rowNotice is what the detail pane says in place of the session tabs when the
// selection has no session state to show: nothing selected, a placeholder for
// a session still being created, or a worktree fault. It is "" for an ordinary
// session row.
func rowNotice(leader string, s Session, ok bool) string {
	var b strings.Builder
	if !ok {
		b.WriteString(theme.dim.Render("Select a session to see details."))
		return b.String()
	}
	if s.Pending {
		// A placeholder has no worktree, so there is no plan, no notepad and no
		// verdict to read — running the panels over it would render a wall of
		// "No plan file."-style misses for a session that is merely being born.
		b.WriteString(theme.sessionName.Render(s.Name))
		b.WriteString("\n")
		b.WriteString(theme.dim.Render("creating session — `afk new` is running in the background"))
		b.WriteString("\n\n")
		b.WriteString(theme.dim.Render("The dashboard stays live; this row fills in once the session exists."))
		return b.String()
	}
	if isWorktreeFault(s.Kind) {
		// Same argument as the placeholder above, one step further: this entry
		// has no `.afk/{name}/` of its own at all, so the panels would render
		// a wall of "No plan file."-style misses and the fault itself — the
		// only thing there is to say about it — would appear nowhere. The
		// registered path is shown here rather than on the row because this is
		// the surface with room for it, and it is what a user needs to check
		// the worktree by hand before pressing `c`.
		b.WriteString(theme.sessionName.Render(s.Name))
		b.WriteString("  ")
		b.WriteString(theme.renderState(stateFault, faultBadgeLabel(s.Kind)))
		b.WriteString("\n")
		b.WriteString(theme.staleText.Render(s.Reason))
		b.WriteString("\n\n")
		b.WriteString(theme.dim.Render("worktree: " + s.WorktreePath))
		b.WriteString("\n")
		b.WriteString(theme.dim.Render("branch: " + s.Branch))
		b.WriteString("\n\n")
		b.WriteString(theme.dim.Render("No session state to show. Press " + keyName(leader, levelProject, actionClean) + " to clean it up."))
		return b.String()
	}
	return ""
}

// --- detail tabs ---

// detailTab names one tab of the detail pane. The values are stable
// identities, not strip positions: visibleDetailTabs decides which appear in a
// given repo (Stack needs git-spice), and Model.detailTab stores the identity
// so a stack that finishes loading after the first frame cannot shift the
// user's chosen tab under them.
type detailTab int

const (
	tabOverview detailTab = iota
	tabWaves
	tabStack
	tabNotes
	tabDiff
	// tabBuild is the project's build: the master plan's tasks with their
	// ledger status (buildtab.go). It is last so the digit keys of the tabs
	// before it never move, and it is shown only when the project has a build.
	tabBuild
	detailTabCount
)

// detailTabSpec is one tab's name, the panels it stacks, and the line it shows
// when every one of them renders nil.
type detailTabSpec struct {
	name   string
	panels []panel
	empty  func(Session) string
}

// detailTabSpecs is the tab table, indexed by detailTab. Tabs 2–5 reuse the
// panels the old single-column detail pane stacked; only the Overview is new.
func detailTabSpecs() [detailTabCount]detailTabSpec {
	return [detailTabCount]detailTabSpec{
		tabOverview: {name: "Overview", panels: []panel{overviewPanel{}},
			empty: func(Session) string { return "Nothing to show." }},
		tabWaves: {name: "Waves", panels: []panel{wavesPanel{}},
			empty: func(Session) string { return "A one-off session has no plan." }},
		tabStack: {name: "Stack", panels: []panel{stackPanel{}},
			empty: func(Session) string { return "git-spice is not detected in this repo." }},
		tabNotes: {name: "Notes", panels: []panel{
			notepadPanel{title: "Learnings (last 10)", pick: func(s Session) []string { return s.LearningsTail }},
			notepadPanel{title: "Issues (last 10)", pick: func(s Session) []string { return s.IssuesTail }},
			verdictPanel{},
		}, empty: func(Session) string { return "No learnings, issues or review verdict yet." }},
		tabDiff: {name: "Diff", panels: []panel{diffPanel{}},
			empty: func(s Session) string {
				if s.Scratch {
					return "A one-off session has no branch to diff."
				}
				return "No base branch recorded to diff against."
			}},
		tabBuild: {name: "Build", panels: []panel{buildPanel{}},
			empty: func(Session) string { return "No build to show." }},
	}
}

// visibleDetailTabs is the strip, in order, for a repo whose stack state is
// st: every session tab except Stack, which appears only when git-spice is
// detected — the same rule stackPanel itself follows ("never a placeholder
// panel for non-gs users") — and then Build, only when the project has a
// build to show (hasBuild, Model.hasBuild). The digit keys index THIS slice,
// so `3` is Notes in a plain git repo and Stack in a git-spice one. Build is
// appended last for that reason: a build arriving, or a project switch taking
// it away, must not move the digit of a tab the user already knows.
func visibleDetailTabs(st stackState, hasBuild bool) []detailTab {
	tabs := []detailTab{tabOverview, tabWaves}
	if st.Detected {
		tabs = append(tabs, tabStack)
	}
	tabs = append(tabs, tabNotes, tabDiff)
	if hasBuild {
		tabs = append(tabs, tabBuild)
	}
	return tabs
}

// activeDetailTab is m.detailTab when the current project can show it, else
// the Overview — the case is a Stack tab chosen before a rescan found
// git-spice gone, a Build tab whose project was switched away from (the tab
// comes back by itself when the new project's build data arrives, because the
// stored choice is kept), or a zero-value Model.
func (m Model) activeDetailTab() detailTab {
	if slices.Contains(visibleDetailTabs(m.stack, m.hasBuild()), m.detailTab) {
		return m.detailTab
	}
	return tabOverview
}

// detailStripLines is the lines composeDetailBody writes above a tab's
// content: the tab strip (exactly one row, renderTabStrip) and the rule under
// it. The Build tab's cursor adds it to the row's place in the tab to find the
// row's line in the detail body.
const detailStripLines = 2

// renderTabStrip draws the tab names on one line, the active one in the panel
// title style and the rest dim, with a right-aligned key hint when the width
// allows. Exactly one row, width cells or fewer: the strip is the first line
// of the body detailBody wraps, and a strip that folded would push every tab's
// content down a row.
func renderTabStrip(tabs []detailTab, active detailTab, width int) string {
	specs := detailTabSpecs()
	var b strings.Builder
	b.WriteString("  ")
	for i, t := range tabs {
		if i > 0 {
			b.WriteString("   ")
		}
		if t == active {
			b.WriteString(theme.panelTitle.Render(specs[t].name))
		} else {
			b.WriteString(theme.dim.Render(specs[t].name))
		}
	}
	strip := b.String()
	hint := theme.dim.Render(fmt.Sprintf("tab/1-%d: switch", len(tabs)))
	if gap := width - lipgloss.Width(strip) - lipgloss.Width(hint); gap >= 2 {
		strip += strings.Repeat(" ", gap) + hint
	}
	return truncate(strip, width)
}

// overviewPanel is the Overview tab: the session's head and status line, then
// the one-line facts a person checks before opening the pane — whether it is
// waiting on them, how far along it is, the diff size, the verdict — the tasks
// ready to run and any failures, where the branch sits in the stack, and the
// last few issues.
// Everything here is a summary of what the other tabs show in full.
type overviewPanel struct{}

func (overviewPanel) Title() string { return "Overview" }

func (overviewPanel) Render(ctx panelContext, width int) []string {
	s := ctx.Session
	var out []string

	// Head: name (or label, with the real name kept visible right beside it —
	// see displayName's doc comment), branch → base.
	head := theme.sessionName.Render(displayName(s))
	if s.Label != "" {
		head += "  " + theme.dim.Render(s.Name)
	}
	if s.Branch != "" {
		sub := s.Branch
		if s.BaseBranch != "" {
			sub += " → " + s.BaseBranch
		}
		head += "  " + theme.branch.Render(sub)
	}
	// The autonomous tag lives on the head line, not the status line below,
	// because the status line is replaced by stale text when StatusStale and
	// the tag must survive that. The head is truncated as a whole, so on a
	// narrow pane with a long label/branch the tag is cut first; accepted, the
	// row glyph still carries the signal there.
	if s.Autonomous {
		head += "  " + theme.dim.Render(autonomousGlyph+" autonomous")
	}
	// Same placement and truncation rule as the autonomous tag above.
	if s.Provider != "" {
		head += "  " + theme.dim.Render(s.Provider)
	}
	out = append(out, truncate(head, width))

	// Status line: phase, loop, updated_at, host liveness, wedged age. A stale
	// status has none of those worth trusting and says so instead.
	if s.StatusStale {
		out = append(out, theme.staleText.Render("status.json is stale: "+string(s.StaleReason)))
	} else {
		// The phase as §3.6 draws a state: its glyph and word in its colour.
		meta := theme.renderState(stateSession, sessionPhaseKey(s))
		// A session paused for usage says so straight after the phase, ahead
		// of the loop count and the update time. The line is cut at the pane's
		// width, and on a default terminal that cut used to land inside the
		// wake time, which is the one fact on the line the owner is waiting
		// for. "host stopped" is not said at all here: the daemon stopped
		// this host on purpose and will start it again, and the words would
		// send the owner looking for a fault that is not there.
		paused := usagePaused(s)
		if paused {
			meta += "  " + theme.renderGlyph(stateSession, "paused") + theme.awaitingFlag.Render(" "+usagePausedText(s))
		}
		if s.LoopCount > 0 {
			meta += theme.dim.Render(fmt.Sprintf("  loop %d", s.LoopCount))
		}
		if s.UpdatedAt != "" {
			meta += theme.dim.Render("  updated " + s.UpdatedAt)
		}
		// The host's own glyph, not the row's (hostIndicator): the phase at
		// the head of this line already says done or failed, and this part
		// says only whether the host process is alive — ◐ running (one of
		// §3.6's working words) or ○ stopped.
		switch {
		case paused:
		case s.HostRunning:
			meta += "  " + theme.renderGlyph(stateSession, hostRunningState) + theme.dim.Render(" host running")
		default:
			meta += "  " + theme.renderGlyph(stateSession, hostStoppedState) + theme.dim.Render(" host stopped")
		}
		if age, ok := showsWedged(s, ctx.Now); ok {
			meta += "  " + theme.errorText.Render(wedgedGlyph+" wedged "+durationShort(age))
		}
		out = append(out, truncate(meta, width))
	}

	// Only the planning session's overview carries the plan line: the status
	// is about the project's plan, which no other session owns.
	if s.Name == session.PlanSessionName && ctx.PlanStatus.Known {
		out = append(out, theme.dim.Render(truncate("plan: "+planStatusLine(ctx.PlanStatus), width)))
	}

	if showsAwaitingInput(s) {
		out = append(out, "", theme.awaitingFlag.Render(awaitingGlyph+" Awaiting input")+
			theme.dim.Render(" · press enter to answer"))
	}

	// Facts. A scratch session has none of these by design, and a stale
	// status's progress and diff were zeroed by discovery, so both skip the
	// block rather than print zeros that look like measurements.
	if !s.Scratch && !s.StatusStale {
		var facts []string
		if s.Plan != nil || s.Progress.Total > 0 {
			facts = append(facts, overviewFact("Progress", renderOverviewBar(s.Progress)))
		}
		if s.BaseBranch != "" {
			if s.DiffStats.BaseMissing {
				facts = append(facts, overviewFact("Diff", theme.staleText.Render(
					fmt.Sprintf("base branch '%s' no longer exists", s.BaseBranch))))
			} else if s.DiffStats.Err != "" {
				facts = append(facts, overviewFact("Diff", theme.staleText.Render("error: "+s.DiffStats.Err)))
			} else {
				d := s.DiffStats
				facts = append(facts, overviewFact("Diff", theme.dim.Render(
					fmt.Sprintf("+%d/-%d · %d file(s) vs %s", d.Added, d.Removed, d.Files, s.BaseBranch))))
			}
		}
		switch {
		case s.VerdictError != "":
			facts = append(facts, overviewFact("Verdict", theme.staleText.Render("parse error: "+s.VerdictError)))
		case s.Verdict != nil:
			v := s.Verdict
			style := theme.verdictOK
			if v.Verdict != state.VerdictApprove {
				style = theme.verdictBad
			}
			facts = append(facts, overviewFact("Verdict", style.Render(string(v.Verdict))+
				theme.dim.Render(fmt.Sprintf("  compliance %s · quality %s · qa %s", v.Compliance, v.Quality, v.QA))))
		default:
			facts = append(facts, overviewFact("Verdict", theme.dim.Render("— not reviewed yet")))
		}
		out = append(out, "")
		for _, f := range facts {
			out = append(out, truncate(f, width))
		}
	}

	// What it is doing right now: the scheduler's ready tasks, blocked count
	// and failures (overviewTaskBlock). The full plan is the Waves tab's.
	if !s.Scratch {
		switch {
		case s.PlanError != "":
			out = append(out, "", theme.staleText.Render("plan parse error: "+s.PlanError))
		case s.Plan == nil:
			out = append(out, "")
			out = append(out, wavesPanel{}.Render(ctx, width)...)
		default:
			if block := overviewTaskBlock(s.Plan, width); len(block) > 0 {
				out = append(out, "")
				out = append(out, block...)
			}
		}
	}

	// Where the branch sits: trunk down to this branch, one line. The full
	// tree with every sibling is the Stack tab's.
	if ctx.Stack.Detected && s.Branch != "" {
		if path := stackPath(ctx.Stack.Branches, s.Branch); path != "" {
			out = append(out, "", overviewFact("Stack", theme.branch.Render(path)))
		}
	}

	if n := len(s.IssuesTail); n > 0 {
		out = append(out, "", theme.panelTitle.Render("Issues (last 3)"))
		for _, ln := range s.IssuesTail[max(0, n-3):] {
			out = append(out, theme.dim.Render(truncate("  "+ln, width)))
		}
	}

	return out
}

// overviewBarWidth is the Overview's progress bar in glyphs — fixed, because
// the fact lines share one left column and a bar that flexed with the pane
// would make the label after it jump around on resize.
const overviewBarWidth = 20

// overviewFact is one "Label   value" line, the label padded to a common width
// so the values line up.
func overviewFact(label, value string) string {
	return theme.dim.Render(fmt.Sprintf("%-9s", label)) + " " + value
}

// overviewReadyCap and overviewFailedCap bound the Overview's two task lists.
// A wide DAG can have dozens of ready tasks; the Overview is a summary and the
// Waves tab lists them all, so past the cap a "+N more" line stands in.
const (
	overviewReadyCap  = 5
	overviewFailedCap = 3
)

// overviewTaskBlock is the Overview's task block, built from state.Schedule
// rather than from "the current wave". Under a DAG plan a Wave 3 task can
// finish while a Wave 2 task is still `[ ]`, so the first wave with
// unfinished work is no longer where the session is; the scheduler's ready
// set is. Lines, in order:
//
//   - one top line: "All N tasks complete"; "N of M complete, K failed" when
//     every task is terminal; the "Ready (N)" heading and its tasks; or, with
//     `[ ]` tasks left and none ready, "No tasks ready" (every one is blocked
//     by a failure) or "No tasks ready — waiting on unmet dependencies". The
//     heading is never "Ready (0)": an empty heading reads as "nothing to do"
//     when the real answer is one of the other lines.
//   - "K blocked by failed dependencies", muted, when Schedule reports any.
//     In DAG mode the upstream engine rewrites blocked dependents to `[!]`
//     itself, so a non-zero count mostly means a stale or crashed engine run
//     (see state.ScheduleView.Blocked).
//   - "Failed (K)" and each failed task with its FAILED reason.
//
// Ready tasks render with no deps (a ready task has none unmet by
// definition). A plan with no tasks yields nothing.
func overviewTaskBlock(plan *state.Plan, width int) []string {
	view := state.Schedule(plan)
	var total, complete, incomplete int
	var failed []state.Task
	for _, w := range plan.Waves {
		for _, t := range w.Tasks {
			total++
			switch t.State {
			case state.CheckComplete:
				complete++
			case state.CheckFailed:
				failed = append(failed, t)
			default:
				incomplete++
			}
		}
	}
	if total == 0 {
		return nil
	}

	heading := func(text string) string { return theme.waveHeading.Render(truncate(text, width)) }
	var out []string
	switch {
	case complete == total:
		out = append(out, heading(fmt.Sprintf("All %d tasks complete", total)))
	case incomplete == 0:
		out = append(out, heading(fmt.Sprintf("%d of %d complete, %d failed", complete, total, len(failed))))
	case len(view.Ready) > 0:
		out = append(out, heading(fmt.Sprintf("Ready (%d)", len(view.Ready))))
		out = append(out, cappedTaskLines(view.Ready, overviewReadyCap, nil, width)...)
	case len(view.Blocked) == incomplete:
		// Blocked is a subset of the `[ ]` tasks, so equal counts mean every
		// remaining task waits on a failure — "waiting" would promise progress
		// that cannot come without a human.
		out = append(out, heading("No tasks ready"))
	default:
		out = append(out, heading("No tasks ready — waiting on unmet dependencies"))
	}

	if n := len(view.Blocked); n > 0 {
		out = append(out, theme.dim.Render(truncate(fmt.Sprintf("%d blocked by failed dependencies", n), width)))
	}

	if len(failed) > 0 {
		out = append(out, "", heading(fmt.Sprintf("Failed (%d)", len(failed))))
		out = append(out, cappedTaskLines(failed, overviewFailedCap, newTaskDeps(plan, view), width)...)
	}
	return out
}

// cappedTaskLines renders at most limit tasks, then a muted "+N more" line for
// the rest.
func cappedTaskLines(tasks []state.Task, limit int, deps *taskDeps, width int) []string {
	var out []string
	for _, t := range tasks[:min(len(tasks), limit)] {
		out = append(out, renderTaskLine(t, deps, width))
	}
	if more := len(tasks) - limit; more > 0 {
		out = append(out, theme.dim.Render(truncate(fmt.Sprintf("  +%d more", more), width)))
	}
	return out
}

// taskDeps is what renderTaskLine needs to name a pending task's unmet
// dependencies: the plan's set of complete task IDs plus the effective deps
// state.Schedule computed. renderTaskLine sees one task, so it cannot know
// its deps' states without this. Built once per render from the plan, never
// cached across renders — the plan file changes under the dashboard every
// discovery tick.
//
// The deps come from ScheduleView.EffectiveDeps rather than Task.DependsOn so
// the Waves suffix and the Overview's ready set are answers to the same
// question; a mixed-presence plan, which Schedule treats as legacy, then
// shows no suffix in either place.
type taskDeps struct {
	effective map[string][]string
	complete  map[string]bool
}

// newTaskDeps returns the deps for a DAG plan and nil otherwise. Legacy (and
// mixed-presence) plans get no suffix: their deps are synthesized "the whole
// previous wave", which the Waves tab's wave headings already show, and a
// suffix listing a full wave on every row would be noise.
func newTaskDeps(plan *state.Plan, view state.ScheduleView) *taskDeps {
	if !view.DAG {
		return nil
	}
	d := &taskDeps{effective: view.EffectiveDeps, complete: map[string]bool{}}
	for _, w := range plan.Waves {
		for _, t := range w.Tasks {
			if t.State == state.CheckComplete {
				d.complete[t.ID] = true
			}
		}
	}
	return d
}

// unmet lists t's dependencies that are not complete, in declared order. Only
// a `[ ]` task gets an answer: a complete task's deps are history, and a
// failed task already shows its FAILED reason. An unknown ID counts as
// unmet, matching state.Schedule. A nil receiver means "no suffix".
func (d *taskDeps) unmet(t state.Task) []string {
	if d == nil || t.State != state.CheckIncomplete {
		return nil
	}
	var out []string
	for _, id := range d.effective[t.ID] {
		if !d.complete[id] {
			out = append(out, id)
		}
	}
	return out
}

// stackPath is the chain of branches from the trunk down to name, joined with
// " › " — "main › feat-auth › feat-billing" — or "" when name is not a tracked
// branch. It walks Down links only (the tree's parent pointers), and stops on a
// repeated name so a malformed stream cannot loop it.
func stackPath(branches []spice.Branch, name string) string {
	byName := make(map[string]spice.Branch, len(branches))
	for _, b := range branches {
		byName[b.Name] = b
	}
	if _, ok := byName[name]; !ok {
		return ""
	}
	var chain []string
	seen := make(map[string]bool, len(branches))
	for cur := name; cur != "" && !seen[cur]; {
		seen[cur] = true
		chain = append(chain, cur)
		b, ok := byName[cur]
		if !ok || b.Down == nil {
			break
		}
		cur = b.Down.Name
	}
	slices.Reverse(chain)
	return strings.Join(chain, " › ")
}

// boxDetailBody bounds body to the rows the pane has room for and only then
// draws the pane (title in its strip, accent when focused), so the box is
// always asked for exactly the height it was given — see renderDetail's doc
// for why the reverse order loses the bottom border. focused changes only the
// border's and the strip's colour, never this function's arithmetic.
//
// The bounding is m.detailViewport's, on a value copy (renderDetail's doc: View
// may not mutate the Model). Two of its behaviours are load-bearing here and
// are the reason nothing clips the result afterwards. With SoftWrap left off,
// visibleLines returns at most Height() lines and, once any line is wider than
// the viewport, CUTS every line to that width with ansi.Cut
// (bubbles/v2@v2.2.1 viewport/viewport.go:332-364); detailBody has already
// wrapped every line to contentW, so the cut never fires, and it is the wrap
// rather than the cut that keeps a wide line from pushing the box past its
// budget. View then pads that block back out to exactly Width x Height
// (viewport.go:728-750). The result is a block of precisely rows x contentW,
// which is what detailBox wants and what makes MaxHeight unnecessary.
//
// The row count is detailRowBudget's, computed from the SAME line count the
// Model's viewport holds (SoftWrap off makes TotalLineCount width-independent:
// calculateLine returns len(lines)), so this copy's Height equals the Model
// viewport's Height and the offset the keys reached is the offset drawn here.
func (m Model) boxDetailBody(body string, contentW, outerH int, title string, focused bool) string {
	vp := m.detailViewport
	vp.SetWidth(contentW)
	vp.SetContent(body)

	rows, readout := detailRowBudget(outerH, vp.TotalLineCount())
	vp.SetHeight(rows)
	// Re-read the offset from the Model rather than trusting the copy's: both
	// SetContent and the SetHeight above can have moved it (SetContent snaps to
	// the bottom when the old offset no longer fits), and SetYOffset is the only
	// call that clamps against the height in force now.
	vp.SetYOffset(m.detailViewport.YOffset())

	block := vp.View()
	innerH := rows
	if readout {
		block = lipgloss.JoinVertical(lipgloss.Left, block, scrollReadout(vp.ScrollPercent(), contentW))
		innerH++
	}
	// The pane part is sizedBox(detailBox, contentW+2, innerH) cell for cell
	// plus the title strip's row, with the interior painted panel and a
	// selected row's fill (the Build tab's cursor) carried to the border.
	return theme.pane(title, "", block, paneOuterWidth(contentW),
		innerH+paneStripRows+boxBorderCells, focused)
}

// scrollReadout renders the detail pane's position indicator — a right-aligned
// percentage on its own row. It is the chosen overflow affordance instead of a
// scrollbar column, which would cost a cell of width on every row to say the
// same thing.
func scrollReadout(percent float64, width int) string {
	label := truncate(fmt.Sprintf("%d%%", int(percent*100+0.5)), width)
	if pad := width - lipgloss.Width(label); pad > 0 {
		return strings.Repeat(" ", pad) + theme.dim.Render(label)
	}
	return theme.dim.Render(label)
}

// --- panels (the detail-pane composition seam) ---

// panel renders one section of the detail pane for the selected session.
// W5.T4's stack panel is the first consumer that needs more than the
// selected session (it annotates every tracked branch with the session(s)
// that own it), which is why Render takes the wider panelContext rather than
// a bare Session — every other panel just reads ctx.Session and ignores the
// rest. Render returns nil/empty to hide the panel entirely (e.g. a verdict
// panel for a session that has no verdict yet, or the stack panel for a
// non-git-spice repo).
type panel interface {
	Title() string
	Render(ctx panelContext, width int) []string
}

// panelContext is what every panel's Render receives: the selected session,
// the full session list (for cross-session annotations, e.g. stack branch
// ownership), and the repo-wide git-spice stack state (refreshed
// independently of session discovery — see stack.go's stackCmd).
type panelContext struct {
	Session  Session
	Sessions []Session
	Stack    stackState
	// Now is the clock a panel measures ages against (the Overview's wedged
	// age). It is passed in rather than read from time.Now so a panel stays a
	// pure function of its context — the same reason sessionDelegate carries
	// Deps.Now instead of calling the clock itself.
	Now time.Time
	// Build is the project's build snapshot, for the Build tab. It is the one
	// field that is not about the selected session.
	Build buildview.Snapshot
	// PlanStatus is the project's planning status and plan check, for the
	// plan line on the master-plan overview and the Build tab. Like Build it is
	// about the project and not the selected session.
	PlanStatus PlanStatus
	// BuildCursorShown and BuildCursor are the Build tab's cursor: whether it
	// is drawn (Model.buildCursorShown) and the index of its row. They are a
	// separate flag and index, not one index with a "none" value, so a
	// panelContext built without them draws the tab with no cursor.
	BuildCursorShown bool
	BuildCursor      int
	// BuildCursorTotal is the short token total the cursor's row ends with
	// when there is room (Model.buildCursorTotal), or "" for none. It is a
	// string and not the record, so the tab is drawn from what is held and
	// nothing here can read the ledger.
	BuildCursorTotal string
	// Leader is the leader key's name (Model.leaderKey.name), for the Build
	// tab's inbox line, which names the inbox chord. The leader is set in
	// config.toml (T9.2), so it is passed in like everything else a panel
	// draws.
	Leader string
}

// wavesPanel renders the plan's waves and tasks with their checkbox states.
type wavesPanel struct{}

func (wavesPanel) Title() string { return "Waves" }

func (wavesPanel) Render(ctx panelContext, width int) []string {
	s := ctx.Session
	if s.Scratch {
		// A scratch session was never planned, by design (internal/session's
		// scratch mode: a bare claude, no plan file ever written) — not a
		// session that failed to plan, so this hides the panel entirely
		// rather than falling through to the "no plan yet" messages below.
		return nil
	}
	if s.PlanError != "" {
		return []string{theme.staleText.Render("plan parse error: " + s.PlanError)}
	}
	if s.Plan == nil {
		if s.Phase == state.PhasePlanning {
			return []string{theme.dim.Render("No plan yet (planning).")}
		}
		return []string{theme.dim.Render("No plan file.")}
	}
	deps := newTaskDeps(s.Plan, state.Schedule(s.Plan))
	var out []string
	for _, w := range s.Plan.Waves {
		out = append(out, theme.waveHeading.Render(fmt.Sprintf("Wave %d: %s", w.Number, w.Name)))
		for _, t := range w.Tasks {
			out = append(out, renderTaskLine(t, deps, width))
		}
	}
	return out
}

// depsSuffixMin is the narrowest room worth spending on a deps suffix: the
// " ← " separator plus one character. Below that the suffix would be an arrow
// pointing at an ellipsis, so it is dropped instead.
const depsSuffixMin = 4

// renderTaskLine renders one task row. deps, when non-nil, adds a dim
// "← W2.T1, W2.T4" suffix naming a pending task's unmet dependencies (see
// taskDeps.unmet); nil means no suffix. The glyph, ID and title keep the room
// they always had, and the suffix takes only what is left, so a narrow pane
// cuts the suffix first and the row still reads as the same task.
func renderTaskLine(t state.Task, deps *taskDeps, width int) string {
	// Truncate the raw text first, then style — truncating a styled string
	// can cut through an ANSI escape sequence and corrupt the output.
	var raw string
	style := theme.taskPending
	switch t.State {
	case state.CheckComplete:
		raw = fmt.Sprintf("  ✓ %s %s", t.ID, t.Title)
		style = theme.taskComplete
	case state.CheckFailed:
		raw = fmt.Sprintf("  ✗ %s %s", t.ID, t.Title)
		if t.FailureReason != "" {
			raw += " — " + t.FailureReason
		}
		style = theme.taskFailed
	default:
		raw = fmt.Sprintf("  ○ %s %s", t.ID, t.Title)
	}
	line := style.Render(truncate(raw, width))
	unmet := deps.unmet(t)
	if room := width - lipgloss.Width(raw); len(unmet) > 0 && room >= depsSuffixMin {
		line += theme.dim.Render(truncate(" ← "+strings.Join(unmet, ", "), room))
	}
	return line
}

// notepadPanel renders the last-N-lines tail of one execution notepad.
type notepadPanel struct {
	title string
	pick  func(Session) []string
}

func (p notepadPanel) Title() string { return p.title }

func (p notepadPanel) Render(ctx panelContext, width int) []string {
	tail := p.pick(ctx.Session)
	if len(tail) == 0 {
		return nil
	}
	out := make([]string, len(tail))
	for i, ln := range tail {
		out[i] = theme.dim.Render(truncate(ln, width))
	}
	return out
}

// verdictPanel renders the review verdict summary when one exists.
type verdictPanel struct{}

func (verdictPanel) Title() string { return "Review Verdict" }

func (verdictPanel) Render(ctx panelContext, width int) []string {
	s := ctx.Session
	if s.VerdictError != "" {
		return []string{theme.staleText.Render("verdict parse error: " + s.VerdictError)}
	}
	v := s.Verdict
	if v == nil {
		return nil
	}
	var out []string
	verdictStyle := theme.verdictOK
	if v.Verdict != state.VerdictApprove {
		verdictStyle = theme.verdictBad
	}
	head := verdictStyle.Render(string(v.Verdict))
	if !v.Consistent() {
		head += theme.errorText.Render("  (inconsistent with checks)")
	}
	out = append(out, head)
	out = append(out, theme.dim.Render(fmt.Sprintf("  Compliance %s · Quality %s · QA %s",
		v.Compliance, v.Quality, v.QA)))
	for _, f := range v.Findings {
		line := fmt.Sprintf("  [%s] %s: %s", f.Severity, f.TaskID, f.Description)
		out = append(out, theme.taskFailed.Render(truncate(line, width)))
	}
	return out
}

// --- small render helpers ---

// progressLabel returns the raw (unstyled) "done/total" suffix text for
// progress p, plus a "(N failed)" note when any tasks failed. Kept separate
// from the bar glyphs so callers can measure the label's width and size the
// bar to whatever room remains.
func progressLabel(p discover.Progress) string {
	if p.Total == 0 {
		return " 0/0"
	}
	label := fmt.Sprintf(" %d/%d", p.Complete, p.Total)
	if p.Failed > 0 {
		label += fmt.Sprintf(" (%d failed)", p.Failed)
	}
	return label
}

// renderOverviewBar is the Overview's progress bar: bubbles/progress, drawn
// once through ViewAs (no animation, so View stays pure) with the palette's
// accent→success blend and the palette's empty-cell colour, followed by the
// done/total label. The label goes red when any task failed, since a blended
// bar has no segment of its own to show a failure in — progressLabel already
// says how many.
func renderOverviewBar(p discover.Progress) string {
	bar := progress.New(
		progress.WithWidth(overviewBarWidth),
		progress.WithoutPercentage(),
		progress.WithColors(theme.progressBlend[0], theme.progressBlend[1]),
		// Full blocks, as the hand-drawn bar this replaced used; the package's
		// default fill is a half block that reads as a stripe.
		progress.WithFillCharacters('█', '░'),
	)
	bar.EmptyColor = theme.progressEmpty
	pct := 0.0
	if p.Total > 0 {
		pct = float64(p.Complete) / float64(p.Total)
	}
	label := theme.dim.Render(progressLabel(p))
	if p.Failed > 0 {
		label = theme.taskFailed.Render(progressLabel(p))
	}
	return bar.ViewAs(pct) + label
}

// truncate shortens s (measured by display width, ANSI-aware) to max cells,
// appending an ellipsis when it had to cut. A non-positive max returns "".
//
// s may already carry ANSI styling (e.g. a fully-composed row line built
// from several styled fragments): xansi.Truncate walks the string as
// escape-sequences + graphemes, so cutting never lands mid-escape-sequence
// or drops a trailing reset — unlike a naive byte/rune slice, which can
// corrupt the styling of whatever renders after it.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	return xansi.Truncate(s, max, "…")
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func durationShort(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
