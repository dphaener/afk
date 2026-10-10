package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/termpane"
)

// TestPaneCursorPlacement covers paneCursor's whole decision surface. Two rows
// are load-bearing beyond the arithmetic they look like:
//
//   - "hidden cursor is nil" is the DECTCEM case: a child that hides its
//     cursor (private mode 25) must produce no on-screen cursor at all, not a
//     stale one at a stale cell. paneCursor's nil return is how that reaches
//     the renderer — see the doc comment on paneCursor for why nil is the
//     right signal rather than some "invisible" Cursor value.
//   - "beyond the frame is nil" is the bottom-clip case: bubbletea v2.0.9
//     under AltScreen clips an overflowing frame from the BOTTOM
//     (cursed_renderer.go:295-351, and internal/ui's own W1.T2 learnings), so
//     a cursor whose translated position lands at or past frameW/frameH sits
//     on a row the renderer never draws — or, worse, on a row now showing
//     unrelated content after a shrink. Both in-pane-bounds and in-frame-bounds
//     checks are required; neither subsumes the other.
func TestPaneCursorPlacement(t *testing.T) {
	cases := []struct {
		name             string
		cs               termpane.CursorState
		originX, originY int
		cols, rows       int
		frameW, frameH   int
		want             *tea.Cursor
	}{
		{
			name:    "visible cursor translates origin plus cell",
			cs:      termpane.CursorState{X: 3, Y: 2, Visible: true, Shape: termpane.CursorBlock, Blink: true},
			originX: 10, originY: 4,
			cols: 80, rows: 24,
			frameW: 80, frameH: 24,
			want: &tea.Cursor{Position: tea.Position{X: 13, Y: 6}, Shape: tea.CursorBlock, Blink: true},
		},
		{
			// DECTCEM case: hidden must be nil regardless of an otherwise
			// in-bounds position.
			name:    "hidden cursor is nil",
			cs:      termpane.CursorState{X: 3, Y: 2, Visible: false, Shape: termpane.CursorBlock, Blink: true},
			originX: 10, originY: 4,
			cols: 80, rows: 24,
			frameW: 80, frameH: 24,
			want: nil,
		},
		{
			name:    "X at cols is out of the pane and nil",
			cs:      termpane.CursorState{X: 80, Y: 2, Visible: true},
			originX: 10, originY: 4,
			cols: 80, rows: 24,
			frameW: 200, frameH: 200,
			want: nil,
		},
		{
			name:    "Y at rows is out of the pane and nil",
			cs:      termpane.CursorState{X: 3, Y: 24, Visible: true},
			originX: 10, originY: 4,
			cols: 80, rows: 24,
			frameW: 200, frameH: 200,
			want: nil,
		},
		{
			name:    "negative X is nil",
			cs:      termpane.CursorState{X: -1, Y: 2, Visible: true},
			originX: 10, originY: 4,
			cols: 80, rows: 24,
			frameW: 200, frameH: 200,
			want: nil,
		},
		{
			name:    "negative Y is nil",
			cs:      termpane.CursorState{X: 3, Y: -1, Visible: true},
			originX: 10, originY: 4,
			cols: 80, rows: 24,
			frameW: 200, frameH: 200,
			want: nil,
		},
		{
			// Bottom-clip case: the cell is well within the pane's own cols x
			// rows (Y=5 < rows=24), but origin 20 + cell 5 = 25 lands at or
			// past a 24-row frame — a row the renderer's AltScreen clip
			// (cursed_renderer.go:295-351) never draws.
			name:    "translated Y at or beyond frameH is nil",
			cs:      termpane.CursorState{X: 3, Y: 5, Visible: true},
			originX: 10, originY: 20,
			cols: 80, rows: 24,
			frameW: 80, frameH: 24,
			want: nil,
		},
		{
			// Same case on the X axis: cell is within cols (X=5 < 80), but
			// origin 78 + cell 5 = 83 is at or past an 80-wide frame.
			name:    "translated X at or beyond frameW is nil",
			cs:      termpane.CursorState{X: 5, Y: 2, Visible: true},
			originX: 78, originY: 4,
			cols: 80, rows: 24,
			frameW: 80, frameH: 24,
			want: nil,
		},
		{
			name:    "underline shape maps through",
			cs:      termpane.CursorState{X: 0, Y: 0, Visible: true, Shape: termpane.CursorUnderline, Blink: true},
			originX: 0, originY: 0,
			cols: 80, rows: 24,
			frameW: 80, frameH: 24,
			want: &tea.Cursor{Position: tea.Position{X: 0, Y: 0}, Shape: tea.CursorUnderline, Blink: true},
		},
		{
			name:    "bar shape maps through and blink false carries through",
			cs:      termpane.CursorState{X: 0, Y: 0, Visible: true, Shape: termpane.CursorBar, Blink: false},
			originX: 0, originY: 0,
			cols: 80, rows: 24,
			frameW: 80, frameH: 24,
			want: &tea.Cursor{Position: tea.Position{X: 0, Y: 0}, Shape: tea.CursorBar, Blink: false},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := paneCursor(c.cs, c.originX, c.originY, c.cols, c.rows, c.frameW, c.frameH)
			assertCursorEqual(t, got, c.want)
		})
	}
}

// assertCursorEqual compares two *tea.Cursor values field by field rather than
// with reflect.DeepEqual, so a Color mismatch (which must always be nil on
// the produced side, per paneCursor's doc comment) is reported by name
// instead of as an opaque struct diff.
func assertCursorEqual(t *testing.T, got, want *tea.Cursor) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
		return
	}
	if got == nil {
		t.Fatalf("got nil, want %+v", want)
	}
	if got.Position != want.Position {
		t.Errorf("Position = %+v, want %+v", got.Position, want.Position)
	}
	if got.Shape != want.Shape {
		t.Errorf("Shape = %v, want %v", got.Shape, want.Shape)
	}
	if got.Blink != want.Blink {
		t.Errorf("Blink = %v, want %v", got.Blink, want.Blink)
	}
	if got.Color != nil {
		t.Errorf("Color = %v, want nil", got.Color)
	}
}
