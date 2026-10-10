// attachpane_test.go covers attachModel.View's terminal cursor: whether it
// sets tea.View.Cursor at all, and where, given the pane's reported
// termpane.CursorState. cursor.go's paneCursor doc covers the pure decision
// this test exercises; what is under test here is attach-specific plumbing —
// which geometry feeds paneCursor, and which frame states must never set one.
package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/termpane"
)

// TestAttachViewCursor is attach's counterpart to the dashboard's cursor test
// (W3.T1, views.go/model.go — not this file). attachModel has no focus
// concept: forwardKeyToPane routes every non-chord key to the single pane
// regardless of any state (attachModel's doc comment, Update's tea.KeyPressMsg
// case), so unlike a dashboard pane — which must also be the selected,
// focused one — the only gate here is the pane's own reported visibility.
//
// Load-bearing rows: (b) pins the cursor's translation against the v2
// bottom-clip (W1.T2 notepad; cursor.go's paneCursor doc) at exactly the
// short-terminal-plus-status height where a wrong translation would put the
// hardware cursor a row above the cell it belongs in — the same height class
// the mouse geometry bug (fixed in W1.T2) used to live at. (d) pins that the
// connection-ended/not-ready blank frame never sets a cursor, matching
// View's existing "no flash on quit" contract for AltScreen.
func TestAttachViewCursor(t *testing.T) {
	readyCursor := termpane.CursorState{X: 4, Y: 3, Visible: true, Shape: termpane.CursorBlock, Blink: true}

	tests := []struct {
		name string
		// build returns the model under test and the wanted cursor. A nil want
		// means View().Cursor must be nil.
		build func() (attachModel, *tea.Cursor)
	}{
		{
			name: "ready, no status: cursor sits at the content origin plus the pane's cell",
			build: func() (attachModel, *tea.Cursor) {
				fp := newFakePane("child screen")
				fp.cursor = readyCursor
				m := attachModel{pane: fp, name: "live", mouseCapture: true, width: 80, height: 24, ready: true}

				cols, _ := m.paneSize()
				originX, originY := paneContentOrigin(0, 0, m.titleBlock(cols))
				want := &tea.Cursor{
					Position: tea.Position{X: originX + readyCursor.X, Y: originY + readyCursor.Y},
					Shape:    tea.CursorBlock,
					Blink:    true,
				}
				return m, want
			},
		},
		{
			// paneOuterHeight floors back to 4 even with the status row taken out
			// of a 4-row terminal, so the rendered frame is 5 rows tall (box +
			// status) against a 4-row terminal — v2 clips that from the BOTTOM
			// (cursed_renderer.go:295,345; W1.T2 notepad), never by shifting the
			// origin up. paneSize()'s rows collapses to 1 at this height
			// (paneContentDims(80, 4) -> rows=4-3=1), so the shared readyCursor
			// fixture's Y:3 would be correctly rejected by paneCursor's own
			// pane-bounds check (cs.Y >= rows) — a DIFFERENT assertion than this
			// row is making. This row uses a separate fixture at Y:0 specifically
			// so the origin arithmetic is isolated from that bound. It does not
			// claim the frameH bound; W2.T1's paneCursor test already covers that.
			name: "status line squeezes the frame: origin unmoved, cursor lands inside the visible terminal",
			build: func() (attachModel, *tea.Cursor) {
				topCursor := termpane.CursorState{X: 4, Y: 0, Visible: true, Shape: termpane.CursorBlock, Blink: true}
				fp := newFakePane("child screen")
				fp.cursor = topCursor
				m := attachModel{
					pane: fp, name: "live", mouseCapture: true,
					width: 80, height: 4, ready: true, status: "pane resize failed: connection reset",
				}

				cols, _ := m.paneSize()
				originX, originY := paneContentOrigin(0, 0, m.titleBlock(cols))
				want := &tea.Cursor{
					Position: tea.Position{X: originX + topCursor.X, Y: originY + topCursor.Y},
					Shape:    tea.CursorBlock,
					Blink:    true,
				}
				return m, want
			},
		},
		{
			name: "cursor hidden: no cursor drawn",
			build: func() (attachModel, *tea.Cursor) {
				fp := newFakePane("child screen")
				fp.cursor = termpane.CursorState{X: 4, Y: 3, Visible: false, Shape: termpane.CursorBlock, Blink: true}
				m := attachModel{pane: fp, name: "live", mouseCapture: true, width: 80, height: 24, ready: true}
				return m, nil
			},
		},
		{
			name: "connection ended: blank frame, no cursor",
			build: func() (attachModel, *tea.Cursor) {
				fp := newFakePane("child screen")
				fp.cursor = readyCursor
				m := attachModel{pane: fp, name: "live", mouseCapture: true, width: 80, height: 24, ready: true, ended: true}
				return m, nil
			},
		},
		{
			name: "not ready (before the first WindowSizeMsg): blank frame, no cursor",
			build: func() (attachModel, *tea.Cursor) {
				fp := newFakePane("child screen")
				fp.cursor = readyCursor
				m := attachModel{pane: fp, name: "live", mouseCapture: true}
				return m, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, want := tt.build()
			got := m.View().Cursor
			if want == nil {
				if got != nil {
					t.Fatalf("View().Cursor = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("View().Cursor = nil, want %+v", want)
			}
			if *got != *want {
				t.Fatalf("View().Cursor = %+v, want %+v", got, want)
			}
		})
	}
}
