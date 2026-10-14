// tasktokensoverlay.go is the "show this task's tokens" overlay (T7.12): the
// Build tab cursor's task, as `afk tokens <task>` prints it.
//
// It is the plan-check overlay's shape (plancheckoverlay.go) with less to do.
// It holds a copy of the record taken when it opened, it never reads a file
// or calls a seam, and it reports only that it was closed. The record is the
// one Deps.LoadTaskTokens last returned (tasktokens.go), read on the command
// goroutine before the owner asked, so opening and drawing it run nothing.
//
// It is not refreshed while open, for the plan check's reason: nothing here
// can be acted on, and a table that rewrote itself would move the row being
// read. Closing and opening it again shows the latest read.
package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// taskTokensKeys is the overlay's help line. They are the plan check's
// bindings and not copies: the two overlays scroll and close with the same
// keys, and one set of values means the footer of either cannot name a key
// the other answers differently. Like the plan check's they are help-only in
// the registry (mode.go) and staged in Update below.
func taskTokensKeys() []key.Binding {
	return planCheckKeys()
}

// taskTokensMinWidth is the narrowest the box's content is made, so the title
// and the scroll hint on its edges still fit beside a short body ("Task T1.1"
// and one sentence). taskTokensMaxRows caps its height before the body
// scrolls; a task has a few roles per phase, so the cap is rarely reached.
const (
	taskTokensMinWidth = 40
	taskTokensMaxRows  = 30
)

// taskTokensOverlay is the overlay's state.
type taskTokensOverlay struct {
	// rec is the copy taken at open, and task the cursor's task then, for a
	// record that does not carry its own id. The body is built from them
	// alone.
	rec    TaskTokens
	task   string
	body   viewport.Model
	width  int
	height int
}

// newTaskTokensOverlay builds the overlay over a record copy. The caller sizes
// it (sizeTaskTokens) before it is drawn.
func newTaskTokensOverlay(rec TaskTokens, task string) taskTokensOverlay {
	o := taskTokensOverlay{rec: rec, task: task, body: viewport.New()}
	o.SetSize(1, 1)
	return o
}

// taskTokensContent is the body for a box whose content can be at most width
// cells wide, before it is sized. It is a pure function of the record so the
// whole choice is table-testable without a Model.
//
// A recorded task shows the command's full table when its widest line fits,
// and the compact table otherwise: the full one cut at the box's edge would
// lose the weighted and share columns, which are the ones worth reading. A
// ledger that cannot be read shows the reader's own sentence, wrapped rather
// than cut because the path in it is what the owner needs, and the ledger's
// path on a line of its own when the sentence does not already name it.
// Anything else, the zero record included, is "nothing recorded yet".
func taskTokensContent(rec TaskTokens, task string, width int) []string {
	width = max(width, 1)
	id := rec.Task
	if id == "" {
		id = task
	}
	switch rec.State {
	case TaskTokensRecorded:
		if len(rec.Lines) > 0 {
			if widestLine(rec.Lines) <= width || len(rec.Compact) == 0 {
				return rec.Lines
			}
			return rec.Compact
		}
	case TaskTokensUnreadable:
		out := []string{"Task " + id, ""}
		reason := rec.Err
		if reason == "" {
			reason = "The token ledger could not be read."
		}
		out = append(out, wrapLines(reason, width)...)
		if rec.Path != "" && !strings.Contains(rec.Err, rec.Path) {
			out = append(out, wrapLines("ledger: "+rec.Path, width)...)
		}
		return out
	}
	return []string{"Task " + id, taskTokensNoneText}
}

// widestLine is the widest of lines in cells.
func widestLine(lines []string) int {
	w := 0
	for _, ln := range lines {
		w = max(w, lipgloss.Width(ln))
	}
	return w
}

// wrapLines wraps one sentence to width and splits it into lines.
func wrapLines(s string, width int) []string {
	return strings.Split(lipgloss.Wrap(s, width, ""), "\n")
}

// taskTokensSizeFor is the box's content size on a width×height terminal,
// and the body for it: as wide as the body's widest line (never under
// taskTokensMinWidth, never over the terminal) and as tall as its lines, up
// to taskTokensMaxRows. The widest the terminal allows is worked out first,
// because which table is shown depends on it.
func taskTokensSizeFor(rec TaskTokens, task string, termW, termH int) (lines []string, width, height int) {
	maxW, _ := modalContentDims(termW, termH, termW, 3)
	lines = taskTokensContent(rec, task, maxW)
	want := max(widestLine(lines), taskTokensMinWidth)
	width, height = modalContentDims(termW, termH, want, min(len(lines), taskTokensMaxRows))
	return lines, width, height
}

// SetSize gives the overlay a terminal size and lays the body out for it. The
// parent calls it from relayout and after opening (sizeTaskTokens), never
// from View: View has a value receiver, and sizing done there would land on a
// copy. The table is chosen again here, so a resize can switch between the
// full and the compact one.
func (o *taskTokensOverlay) SetSize(termW, termH int) {
	lines, w, h := taskTokensSizeFor(o.rec, o.task, termW, termH)
	o.width, o.height = max(w, 1), max(h, 1)
	o.body.SetWidth(o.width)
	o.body.SetHeight(o.height)
	cut := make([]string, len(lines))
	for i, ln := range lines {
		cut[i] = truncate(ln, o.width)
	}
	o.body.SetContentLines(cut)
}

// Update handles one key and reports whether it closed the overlay. Keys it
// does not name do nothing: there is nothing to type here.
func (o *taskTokensOverlay) Update(k tea.KeyPressMsg) (closed bool) {
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

// hint is the text on the box's bottom edge: how far down the reader is, when
// there is more than fits. It is empty when everything is on screen.
func (o taskTokensOverlay) hint() string {
	if o.body.TotalLineCount() <= o.body.Height() {
		return ""
	}
	return fmt.Sprintf("%d%%", int(o.body.ScrollPercent()*100+0.5))
}

// View draws the body, every line cut to the content width and no more lines
// than the content height.
func (o taskTokensOverlay) View() string {
	lines := strings.Split(o.body.View(), "\n")
	if len(lines) > o.height {
		lines = lines[:o.height]
	}
	for i := range lines {
		lines[i] = truncate(lines[i], o.width)
	}
	return strings.Join(lines, "\n")
}

// openTaskTokensOverlay opens the overlay over the held record. Nothing else
// about the dashboard changes, so closing it returns to exactly what was
// under it: the Build tab with the right side focused.
func (m *Model) openTaskTokensOverlay() {
	m.taskTokensOpen = true
	m.taskTokensView = newTaskTokensOverlay(m.taskTokens, m.taskTokensID)
	m.sizeTaskTokens()
	// A toast floats over the top right corner, which is where this box can
	// reach on a narrow terminal.
	m.clearStatus()
}

// sizeTaskTokens applies the current layout to an open overlay, from relayout
// and after opening, for the reason sizePlanCheck exists.
func (m *Model) sizeTaskTokens() {
	if !m.taskTokensOpen || m.width == 0 {
		return
	}
	m.taskTokensView.SetSize(m.width, m.height)
}

// renderTaskTokensModal draws the overlay's box at the size relayout gave it.
// The keys are on the footer's help line (renderHelp).
func (m Model) renderTaskTokensModal() string {
	o := m.taskTokensView
	return renderModalBox("Tokens", o.hint(), o.View(), o.width, o.height)
}

// taskTokensComponentKey is inputTaskTokens' fall-through: every key but
// ctrl+c (a parent rule, mode.go) reaches the overlay.
func (m Model) taskTokensComponentKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.taskTokensView.Update(k) {
		m.taskTokensOpen = false
	}
	return m, nil
}
