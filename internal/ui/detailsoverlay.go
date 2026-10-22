// detailsoverlay.go is the details overlay of docs/dash-design.md §4.4
// (T16.5): `d` anywhere a session is selected, and the leader then `d` in the
// Agent. It is a pane 54 columns wide flush to the right edge of the body,
// over the dimmed screen, with the tabs `1 Overview 2 Waves 3 Stack 4 Notes
// 5 Diff` on its first row under the strip and the active tab's body below
// them, scrolled by the Model's detail viewport. `esc` goes back to where it
// was opened: the Project with the keyboard on the section it was on, or the
// Agent with the keyboard in the agent, which kept running and connected
// behind it the whole time.
//
// It replaced the overview `d` used to put beside the session list with the
// keyboard on it. That arrangement was a focus state (the right side focused
// in the overview view), so it had to be derived from the same two fields the
// Agent is, and every non-key path that moved the focus could move the
// keyboard between the agent and the overview. The overlay is a flag of its
// own instead, like the other overlays (tasktokensoverlay.go): opening it
// moves no focus and bumps no preview generation, so closing it finds the
// level exactly as it was, and an input-mode layer above the pane's
// (modePrecedence) is what gives it the keyboard while it is up.
//
// The geometry is one pure function (detailsBoxRect). The drawing, the
// viewport the scroll keys move (syncDetailViewport) and the mouse wheel all
// read it, so the rows the keys can reach are the rows on screen.
package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// detailsOverlayWidth is the overlay's outer width from detailsFullWidthBelow
// columns (§3.8: "the details overlay is 54 columns on the right").
const detailsOverlayWidth = 54

// detailsFullWidthBelow is the terminal width below which the overlay takes
// the full width (§2: "Below 100 columns the agent and the details overlay
// take the full width"). It is the agent box's breakpoint by name, so the two
// cannot come apart: the overlay is often laid over the Agent.
const detailsFullWidthBelow = agentFullWidthBelow

// detailsTabRows is the rows the tab row takes under the title strip. It is
// not scrolled with the body: the tabs are how to get to the rest, so they
// stay in view however far down a tab is read.
const detailsTabRows = 1

// detailsContext is the strip's context (§3.3): the one way out.
const detailsContext = "esc back"

// detailsRestartChip is whether the overlay draws its one chip, `r restart`
// (T16.5, §4.3: a session with no live agent shows its details "with r
// restart"): exactly where `r` inside it acts (detailsRestart), so the chip
// can never offer a key that does nothing.
func (m Model) detailsRestartChip() bool {
	return m.restartKeyLive()
}

// setDetailsChip sets the `r restart` chip into the box's last row, its
// bottom border, at the left (paintEdge, the way labelBorders sets a hint
// into a modal's edge). Not in the strip's context: the strip is
// accent-filled while the overlay has the keyboard, and a chip's key is
// accent-filled too (keyChip), so there it would vanish. Not on a body row of
// its own either: the chip comes and goes with the row's state (a wedge is
// seen on a tick), and a row taken out of the body only while it shows would
// move the viewport's budget under the scroll keys between two syncs
// (syncDetailViewport), leaving the body's last line out of reach.
func setDetailsChip(box, chip string) string {
	lines := strings.Split(box, "\n")
	last := len(lines) - 1
	b := theme.paneBorder(true)
	bs := b.GetBorderStyle()
	if last < 1 || !strings.HasPrefix(xansi.Strip(lines[last]), bs.BottomLeft) {
		return box
	}
	lines[last] = paintEdge(bs.BottomLeft, bs.Bottom, bs.BottomRight, chip,
		lipgloss.Width(lines[last]), b.GetBorderBottomForeground(), lipgloss.Left)
	return strings.Join(lines, "\n")
}

// detailsBoxRect is the overlay's box on screen: x and y its top-left cell, w
// and h its outer size. From detailsFullWidthBelow columns it is
// detailsOverlayWidth wide and flush to the right edge; below that it takes
// the full width. Vertically it covers the body exactly, between the top bar
// and the status bar. headerRows and bodyHeight are layoutDims' rows. It is
// the ONE place this geometry is decided: View draws the box there,
// syncDetailViewport sizes the viewport from it, and the mouse wheel is
// measured against it.
//
// A terminal narrower than the overlay's own width cannot reach here from 100
// columns up, so the only clamp needed is the floor of 1 the other box
// helpers keep.
func detailsBoxRect(width, headerRows, bodyHeight int) (x, y, w, h int) {
	if width < detailsFullWidthBelow {
		return 0, headerRows, max(width, 1), bodyHeight
	}
	return width - detailsOverlayWidth, headerRows, detailsOverlayWidth, bodyHeight
}

// detailsBodyBudget is the rows of tab body the overlay shows in a box outerH
// rows tall holding totalLines of body, and whether its last row is the
// scroll readout. It is detailRowBudget over the box less the tab row, so the
// readout rule (only when the body overflows) is the one the detail pane has
// always had. syncDetailViewport and renderDetailsOverlay both size from it:
// a Model viewport even a row taller than the drawn one would leave the last
// line unreachable (detailRowBudget's doc).
func detailsBodyBudget(outerH, totalLines int) (rows int, readout bool) {
	return detailRowBudget(outerH-detailsTabRows, totalLines)
}

// detailsBox is the overlay's rectangle for the current terminal, with the
// box's y measured from the frame's top (headerRows from the rendered header).
// Update-side callers that only need the size pass 0 for the header and use
// w and h (detailsSize).
func (m Model) detailsBox() (x, y, w, h int) {
	_, _, bodyHeight, headerRows, _ := m.mouseFrameLayout()
	return detailsBoxRect(m.width, headerRows, bodyHeight)
}

// detailsSize is the overlay's outer size: detailsBoxRect's w and h for the
// layout's body height. The header's height moves the box, never its size.
func (m Model) detailsSize() (w, h int) {
	_, _, bodyHeight := m.layoutDims()
	_, _, w, h = detailsBoxRect(m.width, 0, bodyHeight)
	return w, h
}

// openDetails opens the overlay for the selected session, recording the
// level it was opened from and the session it is about. Nothing else moves:
// the focus, the view and the preview generation stay as they were, so the
// agent pane behind it stays installed and keeps its host's client slot, and
// esc returns to exactly what was under it. The tab and the scroll are the
// dashboard's (detailTab, detailViewport), so the overlay opens on the tab it
// was last left on, scrolled to the top for a session it was not last
// showing (resetDetailScroll).
func (m *Model) openDetails(from level, name string) {
	m.detailsOpen = true
	m.detailsFrom = from
	m.detailsFor = name
	m.detailsRestartFor = ""
	m.keyStripOpen = false
	m.resetDetailScroll()
	m.syncDetailViewport()
	// A toast floats over the top right corner, which is where this box
	// sits from 100 columns up.
	m.clearStatus()
}

// closeDetails closes the overlay. The keyboard goes back to what the
// recorded level had: the Project's focused section, or the agent, whose
// pane was never released, so nothing here moves the focus. detailsFrom is
// kept for the notice below and for tests to read.
func (m *Model) closeDetails() {
	m.detailsOpen = false
	m.detailsFor = ""
	m.detailsRestartFor = ""
}

// closeDetailsIfGone closes the overlay when the session it was opened for
// is no longer the selected row: it left the list (cleaned, its worktree
// removed) or the project changed under it. The selection is pinned to it
// while the overlay is open (holdSelection), so a reshuffle alone does not
// get here. Leaving it open would show another session's details under the
// first one's name.
func (m *Model) closeDetailsIfGone() {
	if !m.detailsOpen {
		return
	}
	if s, ok := m.selected(); ok && s.Name == m.detailsFor && m.hasActiveProject() {
		return
	}
	m.closeDetails()
}

// detailsActionStays reports whether action a, run from the `:` search the
// overlay opened, runs with the overlay still open. The ones that do not
// change what is on screen (quit, the mouse, a usage read, the keys overlay,
// details itself) leave it up; everything else opens or changes something
// the overlay would cover, so the overlay closes first and the action runs
// from the level under it.
func detailsActionStays(a modeAction) bool {
	switch a {
	case actionQuit, actionMouseToggle, actionRefreshUsage, actionKeys, actionDetails:
		return true
	}
	return false
}

// detailsWheel is the mouse wheel over the overlay: one row a notch, the
// same scroll the arrow keys make. Anything else from the mouse is dropped
// while the overlay is up, so a click cannot reach the agent or a section
// under it.
func (m Model) detailsWheel(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	wheel, ok := msg.(tea.MouseWheelMsg)
	if !ok {
		return m, nil
	}
	ev := wheel.Mouse()
	x, y, w, h := m.detailsBox()
	if ev.X < x || ev.X >= x+w || ev.Y < y || ev.Y >= y+h {
		return m, nil
	}
	switch ev.Button {
	case tea.MouseWheelUp:
		return m.scrollDetail(actionDetailScrollUp)
	case tea.MouseWheelDown:
		return m.scrollDetail(actionDetailScrollDown)
	}
	return m, nil
}

// detailsTitle is the strip's name: "Details · <session>", or "Details"
// alone when the session cannot be named (never, while it is open with a
// session selected, but a stray draw must not print a dangling separator).
func (m Model) detailsTitle() string {
	s, ok := m.selected()
	if !ok || s.Name == "" {
		return "Details"
	}
	return "Details · " + s.Name
}

// renderDetailsTabRow is the tab row: each visible tab as its number and name
// with a cell of padding each side, the active one filled with strip
// (stripStyle) and the rest in muted, cut to width. The digits are the
// positions among the visible tabs (visibleDetailTabs), which are what the
// number keys index, so `3` is Notes in a repo without git-spice and the row
// says so.
func renderDetailsTabRow(tabs []detailTab, active detailTab, width int) string {
	specs := detailTabSpecs()
	muted := lipgloss.NewStyle().Foreground(tok(theme.tokens.Muted))
	on := theme.stripStyle().Bold(true)
	var b strings.Builder
	for i, t := range tabs {
		label := fmt.Sprintf(" %d %s ", i+1, specs[t].name)
		if t == active {
			b.WriteString(on.Render(label))
		} else {
			b.WriteString(muted.Render(label))
		}
	}
	return padRight(truncate(b.String(), width), width)
}

// renderDetailsOverlay draws the overlay's box, outerW×outerH: the accent
// pane (it has the keyboard), the tab row, then the active tab's body bounded
// to detailsBodyBudget's rows by a copy of the Model's viewport, the readout
// on the last row when the body overflows, and the `r restart` chip set into
// the bottom border when the session can be restarted (setDetailsChip). The viewport copy is
// re-offset from the Model's because View may not mutate the Model
// (detailViewport's doc), and SetContent and SetHeight on the copy can both
// have moved its offset; SetYOffset is the call that clamps against the
// height in force now.
func (m Model) renderDetailsOverlay(outerW, outerH int) string {
	contentW := detailContentWidth(outerW)
	body := m.detailBody(contentW)
	vp := m.detailViewport
	vp.SetWidth(contentW)
	vp.SetContent(body)
	rows, readout := detailsBodyBudget(outerH, vp.TotalLineCount())
	vp.SetHeight(rows)
	vp.SetYOffset(m.detailViewport.YOffset())

	lines := []string{renderDetailsTabRow(visibleDetailTabs(m.stack), m.activeDetailTab(), contentW)}
	lines = append(lines, strings.Split(vp.View(), "\n")...)
	if readout {
		lines = append(lines, scrollReadout(vp.ScrollPercent(), contentW))
	}
	box := theme.pane(m.detailsTitle(), detailsContext, strings.Join(lines, "\n"), outerW, outerH, true)
	if m.detailsRestartChip() {
		box = setDetailsChip(box, theme.keyChip(m.keys.Restart.Help().Key, "restart"))
	}
	return box
}

// composeDetails lays the overlay's box over frame at (x, y), the place
// detailsBoxRect gives it, rather than centred: the wheel is measured from
// that same rectangle. frame is already dimmed by the caller, once (§3.9):
// over the Project the whole frame, over the Agent the Project and the agent
// box together (composeAgent). It is composeProjectOver's placement with the
// dimming left to the caller, because here the caller is the one that knows
// what lies under the box.
func composeDetails(frame, box string, x, y int) string {
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(frame),
		lipgloss.NewLayer(box).X(max(x, 0)).Y(max(y, 0)).Z(1),
	).Render()
}
