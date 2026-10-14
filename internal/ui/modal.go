// modal.go is the dashboard's one overlay mechanism: a box composited over the
// finished frame, centred, with the frame still visible around it. Before this
// file every "overlay" REPLACED a region — the switcher, palette and creation
// form took the main region, the confirmations took the footer — and a person
// opening the project switcher lost the detail pane they were reading.
//
// The compositing is lipgloss v2's Canvas/Layer (lipgloss/v2@v2.0.6 canvas.go,
// layer.go): the frame is one layer, the box a second at a higher Z and an
// x/y offset, and Canvas.Render flattens the two into one styled string. Only
// the frame WITH a modal open goes through the canvas; the plain frame is
// returned exactly as JoinVertical built it, so every geometry test that
// measures the plain frame is untouched by this file.
package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// modalMargin is how many cells a modal keeps clear of each terminal edge, so
// the frame it floats over stays recognisable around it.
const modalMargin = 2

// modalContentDims clamps a modal's preferred CONTENT size to the terminal:
// the box (content + 2 padding + 2 border in each dimension) must fit inside
// the terminal less modalMargin on every side, and the content never drops
// below the floors, which are what the components need to draw anything at
// all (a list needs 3 rows; a text input needs a few cells).
func modalContentDims(termW, termH, wantW, wantH int) (w, h int) {
	w = min(wantW, termW-2*modalMargin-4)
	h = min(wantH, termH-2*modalMargin-2)
	return max(w, 10), max(h, 3)
}

// renderModalBox draws a modal's box: theme.modalBox around body, sized to
// exactly contentW×contentH inside (the same sizedBox contract every other box
// here follows — the argument is content plus padding), with title on the top
// edge and hint on the bottom one (labelBorders). MaxHeight is the bound
// against a body taller than it was sized for; labelBorders runs after it.
func renderModalBox(title, hint, body string, contentW, contentH int) string {
	box := sizedBox(theme.modalBox, contentW+2, contentH).
		MaxHeight(contentH + boxBorderCells).
		Render(body)
	return labelBorders(box, theme.modalBox, title, hint)
}

// composeModal centres box over frame and returns the composited screen. The
// offsets are floored at 0 so a box that somehow outgrew the terminal is
// clipped at the bottom-right rather than placed off-screen.
//
// It goes through a Compositor, not Canvas.Compose on each layer: Compose
// hands a Drawable the WHOLE canvas as its area, and Layer.Draw paints its
// content at the area's origin and clears the rest (lipgloss/v2@v2.0.6
// layer.go:153, canvas.go:72), so composing the box that way put it at (0,0)
// and erased the frame under it. The Compositor is what honours a layer's
// X/Y/Z — it flattens the layers into per-layer bounds (layer.go:245) and draws
// each inside its own rectangle. Its Render sizes a canvas to the union of the
// bounds, which is the frame's full width×height.
//
// Compositor.Render trims trailing whitespace from every row (ultraviolet's
// TrimSpace), which is why the composited frame's lines can be shorter than
// the plain frame's; the renderer clears to end of line either way.
func composeModal(frame, box string, width, height int) string {
	x := max(0, (width-lipgloss.Width(box))/2)
	y := max(0, (height-lipgloss.Height(box))/2)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(frame),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	).Render()
}

// dialog is a small modal with a title, a few body lines and a key hint: the
// shape every confirmation and prompt on this dashboard takes (clean, restart,
// the failed-create report, the label editor, the one-off provider picker). Before this type they were
// hand-built two- and three-line bars that replaced the footer, and the
// restart confirmation had no bar at all and rode the status line.
type dialog struct {
	title string
	// lines are pre-styled; renderDialog truncates each to the content width.
	lines []string
	// hint is the key portion for the bottom edge, keymap-derived by the
	// builders (modalKeyHint) so it can never name a key the mode lacks.
	hint string
}

// dialogWidth is a dialog's preferred content width. Wide enough for the
// clean confirmation's longest line ("Session dir archived and removed;
// worktree kept." plus margin) without wrapping on a 100-column terminal.
const dialogWidth = 72

// dialogContentWidth is the dialog content width for the current terminal.
func (m Model) dialogContentWidth() int {
	w, _ := modalContentDims(m.width, m.height, dialogWidth, 3)
	return w
}

// renderDialog boxes d: a blank row above and below the body so the text does
// not touch the border, every line cut to the content width, the title on the
// top edge and the hint on the bottom one.
func (m Model) renderDialog(d dialog) string {
	contentW := m.dialogContentWidth()
	lines := make([]string, 0, len(d.lines)+2)
	lines = append(lines, "")
	for _, ln := range d.lines {
		lines = append(lines, truncate(ln, contentW))
	}
	lines = append(lines, "")
	return renderModalBox(d.title, d.hint, strings.Join(lines, "\n"), contentW, len(lines))
}

// activeDialog is the dialog for the modal that owns input, if one does. The
// zero modal (inputNormal) has none.
func (m Model) activeDialog() (dialog, bool) {
	switch m.modal {
	case inputConfirmClean:
		return m.confirmCleanDialog(), true
	case inputConfirmRestart:
		return m.confirmRestartDialog(), true
	case inputSpawnError:
		return m.spawnErrorDialog(), true
	case inputEditLabel:
		return m.labelDialog(), true
	case inputPickProvider:
		return m.providerPickDialog(), true
	case inputPlanText:
		return m.planTextDialog(), true
	case inputConfirmPlanOpen:
		return m.planOpenDialog(), true
	}
	return dialog{}, false
}
