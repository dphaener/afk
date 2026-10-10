// cursor.go turns a pane's terminal cursor state into the frame-relative
// cursor bubbletea v2 draws. Like mouse.go, every function here is PURE: no
// model access, no rendering, no I/O — the environment (origin, bounds, frame
// size) is passed in exactly the way paneMouseEvent takes it, so the whole
// decision matrix is table-testable without a live pane or a real terminal.
package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/termpane"
)

// paneCursor decides where — or whether — to draw the child's cursor, given
// its state in pane-local cells (cs), the screen cell the pane's content
// begins at (originX, originY), the pane's own size (cols, rows), and the
// current frame's size (frameW, frameH).
//
// A nil result is how "no cursor" is signaled to bubbletea: view.Cursor == nil
// is exactly what cursed_renderer.go's flush checks to decide visibility
// (v2.0.9 cursed_renderer.go:538-539, `showCursor := view.Cursor != nil;
// hideCursor := !showCursor`) and to skip the MoveTo call entirely
// (cursed_renderer.go:509-511). There is no separate "hidden" field on
// tea.Cursor to set instead — nil is the only vocabulary the renderer has for
// it, so every rejecting branch below returns nil rather than some
// zero-value Cursor.
//
// Both bounds checks are necessary and neither subsumes the other, for the
// same reason mouseBounds takes the per-axis minimum of two disagreeing
// sources instead of trusting either alone: cs.X/cs.Y come from the emulator
// and are trustworthy only against the emulator's OWN cols x rows, while a
// resize that shrinks the frame without yet resizing the emulator (or a
// modal shrinking the drawn region) can leave an in-emulator-bounds cursor
// translating to a screen cell the frame no longer has. Bubbletea v2.0.9
// clips an overflowing frame from the BOTTOM under AltScreen
// (cursed_renderer.go:295-351 — the frame area is fixed at Rect(0, 0, width,
// height) and ultraviolet's printString drops any cell outside it), so a
// translated position at or beyond frameW/frameH is a row or column the
// renderer never draws, or — worse, after a shrink — one now showing
// unrelated content.
//
// No crop offset is applied when translating: tea.Cursor.Position is already
// frame-relative (tea.go:365-367, "relative to the top left corner of the
// frame"), both front ends set AltScreen unconditionally so frame == screen
// (see mouse.go's package doc and the W1.T2 notepad entry), and flush hands
// the position straight to MoveTo with no further adjustment
// (cursed_renderer.go:509-511). Any extra translation here would double-count
// an offset the renderer itself never applies.
func paneCursor(cs termpane.CursorState, originX, originY, cols, rows, frameW, frameH int) *tea.Cursor {
	if !cs.Visible {
		return nil
	}
	if cs.X < 0 || cs.Y < 0 || cs.X >= cols || cs.Y >= rows {
		return nil
	}

	x, y := originX+cs.X, originY+cs.Y
	if x < 0 || y < 0 || x >= frameW || y >= frameH {
		return nil
	}

	return &tea.Cursor{
		Position: tea.Position{X: x, Y: y},
		Shape:    cursorShape(cs.Shape),
		Blink:    cs.Blink,
	}
}

// cursorShape maps the emulator's DECSCUSR shape onto bubbletea's. An
// unrecognized value (there are only three today) defaults to the block,
// matching both packages' own zero value for "no shape selected yet"
// (vt's zero Cursor and tea.NewCursor both start at CursorBlock).
func cursorShape(s termpane.CursorShape) tea.CursorShape {
	switch s {
	case termpane.CursorUnderline:
		return tea.CursorUnderline
	case termpane.CursorBar:
		return tea.CursorBar
	default:
		return tea.CursorBlock
	}
}
