// plancheckoverlay.go implements the check-the-plan overlay (the "check the
// plan" row of the `:` actions search, T7.7): the plan checker's counts and one
// finding per line, errors first, worded as `afk plan lint` words them.
//
// It follows the inbox overlay's shape (inboxoverlay.go) with less to do: it
// holds a copy of the plan status taken when it opened, it never reads a
// file, and it reports only that it was closed. The status is the one the
// refresh last read through Deps.LoadPlanStatus (planstatusdata.go), so
// opening the overlay and drawing it run nothing; the checker ran on the
// command goroutine before the owner asked.
//
// Unlike the inbox it is NOT refreshed while open. A finding list that
// rewrote itself under the reader would move the line they were reading, and
// nothing here can be acted on, so a stale copy cannot lead to a refused
// action. Closing and opening it again shows the latest read.
package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The overlay's keys. They are staged inside planCheckOverlay.Update and are
// help-only in the registry (mode.go), on the inbox's precedent: a parent
// rule for any of them would implement the gesture twice. Update matches
// against these same values, so the footer cannot name a key the overlay
// does not answer.
var (
	planCheckUpKey = key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)
	planCheckDownKey = key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)
	planCheckPageUpKey = key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "page up"),
	)
	planCheckPageDownKey = key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdn", "page down"),
	)
	planCheckTopKey = key.NewBinding(
		key.WithKeys("home"),
		key.WithHelp("home", "top"),
	)
	planCheckBottomKey = key.NewBinding(
		key.WithKeys("end"),
		key.WithHelp("end", "bottom"),
	)
	planCheckCloseKey = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close"),
	)
)

// planCheckKeys is the overlay's help line, in the order the footer shows it.
func planCheckKeys() []key.Binding {
	return []key.Binding{
		planCheckUpKey, planCheckDownKey, planCheckPageUpKey, planCheckPageDownKey,
		planCheckTopKey, planCheckBottomKey, planCheckCloseKey,
	}
}

// What the action says when it opens nothing. Constants because the tests
// and the README quote them.
const (
	planCheckNotReadNotice  = "The plan has not been read yet; try again in a moment."
	planCheckUnwiredNotice  = "This dashboard cannot read the plan. Run afk plan lint in the project instead."
	planCheckUnreadablePref = "The plan could not be checked: "
)

// planCheckModalWidth is the overlay's preferred content width: wide enough
// that most findings (path, line, level, task, message and rule) fit on one
// row. planCheckMaxRows caps its height before the body scrolls.
// modalContentDims clamps both to the terminal.
const (
	planCheckModalWidth = 100
	planCheckMaxRows    = 30
)

// planCheckOverlay is the overlay's state.
type planCheckOverlay struct {
	// status is the copy taken at open; the body is built from it alone.
	status PlanStatus
	body   viewport.Model
	width  int
	height int
}

// newPlanCheckOverlay builds the overlay over a status copy.
func newPlanCheckOverlay(s PlanStatus, width, height int) planCheckOverlay {
	o := planCheckOverlay{status: s, body: viewport.New()}
	o.SetSize(width, height)
	return o
}

// lines is the body wrapped to a content width. A finding is wrapped rather
// than cut, because its message and its rule are at the end of the line and
// are the part a reader needs; each finding starts with the plan path, so a
// wrapped one still reads as one finding.
func (o planCheckOverlay) lines(width int) []string {
	width = max(width, 1)
	var out []string
	for _, ln := range planCheckLines(o.status) {
		if ln == "" {
			out = append(out, "")
			continue
		}
		for _, w := range strings.Split(lipgloss.Wrap(ln, width, ""), "\n") {
			out = append(out, truncate(w, width))
		}
	}
	return out
}

// wantRows is how many content rows the body would like at a content width,
// so a plan with two findings does not float in a tall empty box. The width
// is a parameter for the reason inboxOverlay.wantRows gives: the parent asks
// before it sets the size.
func (o planCheckOverlay) wantRows(width int) int {
	return max(3, min(len(o.lines(width)), planCheckMaxRows))
}

// SetSize gives the overlay its content size. The parent calls it from
// relayout and after opening (sizePlanCheck), never from View: View has a
// value receiver, and sizing done there would land on a copy.
func (o *planCheckOverlay) SetSize(width, height int) {
	o.width, o.height = max(width, 1), max(height, 1)
	o.body.SetWidth(o.width)
	o.body.SetHeight(o.height)
	o.body.SetContentLines(o.lines(o.width))
}

// Update handles one key and reports whether it closed the overlay. Keys it
// does not name do nothing: there is nothing to type here.
func (o *planCheckOverlay) Update(k tea.KeyPressMsg) (closed bool) {
	switch {
	case key.Matches(k, planCheckCloseKey):
		return true
	case key.Matches(k, planCheckUpKey):
		o.body.ScrollUp(1)
	case key.Matches(k, planCheckDownKey):
		o.body.ScrollDown(1)
	case key.Matches(k, planCheckPageUpKey):
		o.body.PageUp()
	case key.Matches(k, planCheckPageDownKey):
		o.body.PageDown()
	case key.Matches(k, planCheckTopKey):
		o.body.GotoTop()
	case key.Matches(k, planCheckBottomKey):
		o.body.GotoBottom()
	}
	return false
}

// title is the text on the box's top edge.
func (o planCheckOverlay) title() string {
	return "Plan check · " + o.status.PlanPath
}

// hint is the text on the box's bottom edge: how far down the reader is, when
// there is more than fits. It is empty when everything is on screen.
func (o planCheckOverlay) hint() string {
	if o.body.TotalLineCount() <= o.body.Height() {
		return ""
	}
	return fmt.Sprintf("%d%%", int(o.body.ScrollPercent()*100+0.5))
}

// View draws the body, every line cut to the content width and no more lines
// than the content height.
func (o planCheckOverlay) View() string {
	lines := strings.Split(o.body.View(), "\n")
	if len(lines) > o.height {
		lines = lines[:o.height]
	}
	for i := range lines {
		lines[i] = truncate(lines[i], o.width)
	}
	return strings.Join(lines, "\n")
}

// openPlanCheck is the "check the plan" action. It opens the overlay only
// when there is a plan to show; every other case is one notice and nothing
// opened, so esc is never needed to get rid of a box that says nothing.
//
// It reads no file and calls no seam: the status is the one the refresh last
// stored. Before the first read there is no telling a missing plan from one
// not looked at yet, so the notice says to try again, and the next read is at
// most one tick away.
//
// Nothing else about the dashboard changes, so closing the overlay returns to
// exactly what was under it.
func (m Model) openPlanCheck() (tea.Model, tea.Cmd) {
	s := m.planStatus
	switch {
	case m.deps.LoadPlanStatus == nil:
		// The dialog dims the row in this case; this is for a run that
		// reaches the action some other way.
		m.notify(notifyInfo, planCheckUnwiredNotice)
		return m, nil
	case !s.Known:
		m.notify(notifyInfo, planCheckNotReadNotice)
		return m, nil
	case s.Err != "":
		// The project's settings could not be read, so where the plan is
		// is not known either; "no plan at <empty path>" would be wrong.
		m.notify(notifyError, planCheckUnreadablePref+s.Err)
		return m, nil
	case !s.PlanExists:
		m.notify(notifyInfo, planCheckLines(s)[0])
		return m, nil
	}
	m.planCheckOpen = true
	m.planCheck = newPlanCheckOverlay(s, 1, 1)
	m.sizePlanCheck()
	// A toast floats over the top right corner, which is where this box is.
	m.clearStatus()
	return m, nil
}

// planCheckSize is the overlay's content size for the current terminal. The
// width comes first: the rows the body wants depend on how it wraps.
func (m Model) planCheckSize() (width, height int) {
	width, _ = modalContentDims(m.width, m.height, planCheckModalWidth, 3)
	return modalContentDims(m.width, m.height, planCheckModalWidth, m.planCheck.wantRows(width))
}

// sizePlanCheck applies the current layout to an open overlay, from relayout
// and after opening, for the reason sizeInbox exists.
func (m *Model) sizePlanCheck() {
	if !m.planCheckOpen || m.width == 0 {
		return
	}
	m.planCheck.SetSize(m.planCheckSize())
}

// renderPlanCheckModal draws the overlay's box. The keys are on the footer's
// help line (renderHelp).
func (m Model) renderPlanCheckModal() string {
	width, height := m.planCheckSize()
	return renderModalBox(m.planCheck.title(), m.planCheck.hint(), m.planCheck.View(), width, height)
}

// planCheckComponentKey is inputPlanCheck's fall-through: every key but
// ctrl+c (a parent rule, mode.go) reaches the overlay.
func (m Model) planCheckComponentKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.planCheck.Update(k) {
		m.planCheckOpen = false
	}
	return m, nil
}
