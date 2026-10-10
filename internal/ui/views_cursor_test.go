package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/termpane"
)

// paneCursorFixture is the child cursor most rows below install on the fake
// pane. Deliberately NOT the fake's default ({0,0} visible blinking block): a
// non-zero X/Y is what makes "origin + cell" distinguishable from "origin", and
// a bar that does not blink is what makes the shape/blink translation visible
// rather than accidentally equal to tea.Cursor's zero value.
var paneCursorFixture = termpane.CursorState{X: 2, Y: 1, Visible: true, Shape: termpane.CursorBar, Blink: false}

// TestViewCursorFollowsFocusedPane pins View's tea.View.Cursor — the field
// render() (render_test.go) cannot see, because it reads only Content.
//
// Load-bearing rows:
//   - "unfocused pane" and "footer modal owns input" are decision D4: the
//     cursor marks WHERE KEYSTROKES LAND. Both leave the pane fully drawn, and
//     both route the keyboard somewhere else — so a cursor blinking in the
//     session would be pointing at a window the user's typing does not reach.
//     "footer modal" is the row m.paneFocused alone gets wrong, which is why
//     the gate is activeInputMode and not that field.
//   - the three overlay rows (switcher/palette/form) prevent a cursor drawn
//     over a component that replaced the pane in the frame entirely.
//   - "short terminal" pins the no-crop arithmetic: at height 5 the frame is
//     taller than the terminal, v2 clips it from the BOTTOM, so the origin is
//     NOT shifted up and the content row stays headerRows + 1 + titleRows. It
//     carries its own Y:0 fixture because the pane region is a single row
//     there (paneContentDims: rows = outerH - 3), so the shared Y:1 fixture is
//     correctly rejected by paneCursor's own bound and would prove nothing.
func TestViewCursorFollowsFocusedPane(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		height  int
		cursor  termpane.CursorState
		mutate  func(*Model)
		wantSet bool
	}{
		{
			name:    "focused pane on the selected row",
			width:   80,
			height:  24,
			cursor:  paneCursorFixture,
			wantSet: true,
		},
		{
			name:   "unfocused pane",
			width:  80,
			height: 24,
			cursor: paneCursorFixture,
			mutate: func(m *Model) { m.paneFocused = false },
		},
		{
			name:   "hidden pane",
			width:  80,
			height: 24,
			cursor: paneCursorFixture,
			mutate: func(m *Model) { m.paneHidden = true },
		},
		{
			name:   "switcher open",
			width:  80,
			height: 24,
			cursor: paneCursorFixture,
			mutate: func(m *Model) { m.switcherOpen = true },
		},
		{
			name:   "palette open",
			width:  80,
			height: 24,
			cursor: paneCursorFixture,
			mutate: func(m *Model) { m.paletteOpen = true },
		},
		{
			name:   "form open",
			width:  80,
			height: 24,
			cursor: paneCursorFixture,
			// Through openNewForm rather than the bare flag: View renders the
			// real bubbles components, and an unsized textarea panics inside
			// its own View (textarea.go:1074).
			mutate: func(m *Model) {
				opened, _ := m.openNewForm()
				*m = opened.(Model)
			},
		},
		{
			name:   "child hid its cursor",
			width:  80,
			height: 24,
			cursor: termpane.CursorState{X: 2, Y: 1, Visible: false, Shape: termpane.CursorBar},
		},
		{
			name:   "no pane at all",
			width:  80,
			height: 24,
			cursor: paneCursorFixture,
			mutate: func(m *Model) { m.pane = nil },
		},
		{
			name:    "short terminal, frame taller than the screen",
			width:   80,
			height:  5,
			cursor:  termpane.CursorState{X: 2, Y: 0, Visible: true, Shape: termpane.CursorBar, Blink: false},
			wantSet: true,
		},
		{
			name:   "footer modal owns input while the pane stays drawn",
			width:  80,
			height: 24,
			cursor: paneCursorFixture,
			mutate: func(m *Model) { m.modal = inputConfirmClean; m.confirmTarget = "live" },
		},
		{
			name:   "spawn-error modal owns input while the pane stays drawn",
			width:  80,
			height: 24,
			cursor: paneCursorFixture,
			mutate: func(m *Model) { m.modal = inputSpawnError; m.spawnErrHeadline = "boom" },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := frameModel("live", tc.width, tc.height, true)
			fp.cursor = tc.cursor
			if tc.mutate != nil {
				tc.mutate(&m)
			}

			got := m.View().Cursor
			if !tc.wantSet {
				if got != nil {
					t.Fatalf("View().Cursor = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("View().Cursor = nil, want a cursor at the pane's cell")
			}

			// Derived the way routeMouse derives its origin (dashboardOrigin in
			// mouse_test.go duplicates that arithmetic independently), so this
			// assertion pins the cursor path and the mouse path to the SAME
			// cell: a cursor that lands where a click does not is the defect.
			originX, originY := dashboardOrigin(m)
			want := tea.Position{X: originX + tc.cursor.X, Y: originY + tc.cursor.Y}
			if got.Position != want {
				t.Errorf("Cursor.Position = %+v, want %+v", got.Position, want)
			}
			if got.Shape != tea.CursorBar {
				t.Errorf("Cursor.Shape = %v, want %v (tea.CursorBar)", got.Shape, tea.CursorBar)
			}
			if got.Blink {
				t.Error("Cursor.Blink = true, want false — the child asked for a steady cursor")
			}

			// The no-crop claim, stated arithmetically rather than through
			// dashboardOrigin, so a reintroduced crop term cannot pass by
			// moving both sides together.
			_, _, _, headerRows, frameRows := m.mouseFrameLayout()
			regionCols, _ := m.paneRegionSize()
			titleRows := lipgloss.Height(paneTitleBlock(m.paneName, m.paneTitleHint(), regionCols))
			if originY != headerRows+1+titleRows {
				t.Errorf("origin Y = %d, want %d (headerRows %d + 1 border + %d title rows, unshifted)",
					originY, headerRows+1+titleRows, headerRows, titleRows)
			}
			if tc.height == 5 && frameRows <= tc.height {
				t.Fatalf("frame is %d rows at terminal height %d — this row must overflow to test anything",
					frameRows, tc.height)
			}
		})
	}
}
