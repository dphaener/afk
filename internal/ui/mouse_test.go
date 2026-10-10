package ui

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/termpane"
)

// --- geometry helpers ---

// TestSidebarRenderedWidthMatchesRenderedList is the arithmetic-honesty test for
// sidebarRenderedWidth: it asserts the computed value against the width the
// sidebar ACTUALLY renders at, so the helper cannot drift if theme.listBox's
// padding ever changes. The mouse path needs this number without rendering
// (Update may not render), which is the only reason the arithmetic exists at
// all.
func TestSidebarRenderedWidthMatchesRenderedList(t *testing.T) {
	m := testModel(sess("alpha"), sess("beta"))
	const bodyHeight = 20

	for _, outerW := range []int{24, 28, 31, 35, 42} {
		rendered := lipgloss.Width(m.renderList(outerW, bodyHeight))
		if got := sidebarRenderedWidth(outerW); got != rendered {
			t.Errorf("sidebarRenderedWidth(%d) = %d, rendered list is %d cells wide",
				outerW, got, rendered)
		}
		// Since the W5.T1 fix cycle the sidebar box fills its outer width
		// exactly (renderList passes sizedBox contentW+4, absorbing
		// theme.listBox's padding): at these unclamped widths anything else
		// means the padding assumption changed and mouse events will land
		// cells away from the click.
		if rendered != outerW {
			t.Errorf("rendered list at outer %d is %d — expected the box to fill its outer width",
				outerW, rendered)
		}
	}
	// sidebarContentWidth floors its result at 10, so below outer 14 the
	// rendered box is WIDER than outerW — the helper must track the render,
	// not collapse to `return outerW`.
	if got, rendered := sidebarRenderedWidth(12), lipgloss.Width(m.renderList(12, bodyHeight)); got != rendered {
		t.Errorf("sidebarRenderedWidth(12) = %d, rendered list is %d cells wide (clamped case)", got, rendered)
	}
}

// TestPaneTitleBlockIsAlwaysOneLine is the assertion paneContentOrigin's Y term
// rests on: every hint variant, at every width, renders exactly one row and
// never exceeds the width it was given.
//
// The rawWidth field pins the measurement each variant's truncation behaviour
// follows from, with a 33-character session name. Note that the three variants
// have three different lengths, so they start truncating at three different
// widths — the widest (focused, 62 cells) is cut at all five widths below,
// attach's 52-cell title only at the narrowest three. That is why the
// truncation-actually-happened check is conditional on rawWidth rather than
// asserted for every pair: a case where the title already fits still has to
// produce one row, and asserting otherwise would be asserting the test's own
// table rather than the helper.
func TestPaneTitleBlockIsAlwaysOneLine(t *testing.T) {
	name := strings.Repeat("s", 33)
	variants := []struct {
		label    string
		hint     string
		rawWidth int
	}{
		{"dashboard focused", paneFocusHint, 62},
		{"dashboard unfocused", "[enter/f focus view]", 57},
		{"attach", attachDetachHint, 52},
	}

	for _, v := range variants {
		raw := "▶ " + name + "  " + v.hint
		if got := lipgloss.Width(raw); got != v.rawWidth {
			t.Errorf("%s: untruncated title is %d cells, expected %d — the hint text changed",
				v.label, got, v.rawWidth)
		}
		for _, contentW := range []int{24, 30, 40, 53, 59} {
			block := paneTitleBlock(name, v.hint, contentW)
			if h := lipgloss.Height(block); h != 1 {
				t.Errorf("%s at contentW %d: block is %d lines, want 1", v.label, contentW, h)
			}
			if w := lipgloss.Width(block); w > contentW {
				t.Errorf("%s at contentW %d: block is %d cells wide, want <= %d",
					v.label, contentW, w, contentW)
			}
			// Where the raw title genuinely overflows, truncation — not luck —
			// is what kept the block to one row, so prove it was cut.
			if v.rawWidth > contentW && block == theme.paneTitle.Render(raw) {
				t.Errorf("%s at contentW %d: %d-cell title was not truncated",
					v.label, contentW, v.rawWidth)
			}
		}
	}

	// Every variant must be truncated at the narrowest widths, or the table has
	// drifted into testing nothing at all.
	for _, v := range variants {
		if v.rawWidth <= 40 {
			t.Errorf("%s: %d-cell title no longer overflows contentW 40 — widen the test's name",
				v.label, v.rawWidth)
		}
	}
}

// TestPaneTitleBlockReproducesLegacyTitles pins the rendered TEXT: converting
// renderPane to the shared helper must not change a single byte for a title that
// already fit, in either focus state. The expectations are the exact strings
// views.go composed inline before the conversion.
func TestPaneTitleBlockReproducesLegacyTitles(t *testing.T) {
	const contentW = 80
	cases := []struct {
		hint   string
		legacy string
	}{
		{paneFocusHint, theme.paneTitle.Render("▶ mysession" + "  [focused · ctrl+\\ detach]")},
		{"[enter/f focus view]", theme.paneTitle.Render("▶ mysession" + "  [enter/f focus view]")},
		{attachDetachHint, theme.paneTitle.Render("▶ mysession" + "  [ctrl+\\ detach]")},
	}
	for _, c := range cases {
		if got := paneTitleBlock("mysession", c.hint, contentW); got != c.legacy {
			t.Errorf("paneTitleBlock(…, %q, %d) = %q, want the legacy %q", c.hint, contentW, got, c.legacy)
		}
	}
}

// TestRenderPaneOutputUnchangedForFittingTitle is the byte-identity check on the
// whole box, not just the title: it re-composes renderPane's pre-conversion body
// and asserts the live renderPane still produces exactly that.
func TestRenderPaneOutputUnchangedForFittingTitle(t *testing.T) {
	fp := newFakePane("SCREEN-BODY")
	m := testModel(sess("a"))
	m.pane = fp
	m.paneName = "a"

	_, mainOuter, bodyHeight := m.layoutDims()

	for _, focused := range []bool{false, true} {
		m.paneFocused = focused

		contentW, _ := paneContentDims(mainOuter, bodyHeight)
		legacyTitleText := "▶ " + m.paneName
		if focused {
			legacyTitleText += "  [focused · ctrl+\\ detach]"
		} else {
			legacyTitleText += "  [enter/f focus view]"
		}
		legacy := sizedBox(m.paneBoxStyle(), contentW+2, bodyHeight-boxBorderCells).
			MaxHeight(bodyHeight).
			Render(lipgloss.JoinVertical(lipgloss.Left,
				theme.paneTitle.Render(legacyTitleText), fp.render))

		if got := m.renderPane(mainOuter, bodyHeight); got != legacy {
			t.Errorf("renderPane changed for focused=%v\n got: %q\nwant: %q", focused, got, legacy)
		}
	}
}

// TestPaneContentOriginTracksBlockHeight covers both terms. The one-line case is
// the real one; the explicit two-line block proves the Y term reads
// lipgloss.Height rather than assuming a constant, so the helper stays correct if
// the title block ever legitimately grows a row.
func TestPaneContentOriginTracksBlockHeight(t *testing.T) {
	block := paneTitleBlock("session", "[enter/f focus view]", 40)
	if h := lipgloss.Height(block); h != 1 {
		t.Fatalf("precondition: title block is %d lines, want 1", h)
	}
	if x, y := paneContentOrigin(38, 2, block); x != 40 || y != 4 {
		t.Errorf("paneContentOrigin(38, 2, one-line block) = (%d, %d), want (40, 4)", x, y)
	}

	// A two-line block pushes the screen down exactly one more row; X is
	// unaffected (border + padding only).
	if x, y := paneContentOrigin(38, 2, "a\nb"); x != 40 || y != 5 {
		t.Errorf(`paneContentOrigin(38, 2, "a\nb") = (%d, %d), want (40, 5)`, x, y)
	}
}

// TestMouseBoundsTakesPerAxisMinimum covers both directions of disagreement: the
// region can outgrow the emulator (a taller modal footer with no Resize) and the
// emulator can outgrow the region (that footer going away). Only the min keeps a
// footer click from landing on a session row.
func TestMouseBoundsTakesPerAxisMinimum(t *testing.T) {
	cases := []struct {
		name                   string
		regionCols, regionRows int
		paneCols, paneRows     int
		wantCols, wantRows     int
	}{
		{"region smaller on both axes", 46, 16, 80, 24, 46, 16},
		{"emulator smaller on both axes", 80, 24, 46, 16, 46, 16},
		{"region narrower, emulator shorter", 46, 24, 80, 16, 46, 16},
		{"emulator narrower, region shorter", 80, 16, 46, 24, 46, 16},
		{"identical", 46, 18, 46, 18, 46, 18},
	}
	for _, c := range cases {
		cols, rows := mouseBounds(c.regionCols, c.regionRows, c.paneCols, c.paneRows)
		if cols != c.wantCols || rows != c.wantRows {
			t.Errorf("%s: mouseBounds(%d, %d, %d, %d) = (%d, %d), want (%d, %d)",
				c.name, c.regionCols, c.regionRows, c.paneCols, c.paneRows,
				cols, rows, c.wantCols, c.wantRows)
		}
	}
}

// --- translation ---

// mouseAt builds a press event for the given absolute terminal cell.
func mouseAt(x, y int) tea.MouseMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// TestPaneMouseEventSubtractsOrigin asserts the translation is pure subtraction
// in both axes — both coordinate systems are 0-based, so there is no ±1 fudge to
// get wrong.
func TestPaneMouseEventSubtractsOrigin(t *testing.T) {
	const originX, originY, cols, rows = 22, 3, 46, 18

	cases := []struct {
		absX, absY   int
		wantX, wantY int
	}{
		{22, 3, 0, 0},
		{23, 3, 1, 0},
		{22, 4, 0, 1},
		{40, 12, 18, 9},
	}
	for _, c := range cases {
		ev, ok := paneMouseEvent(mouseAt(c.absX, c.absY), originX, originY, cols, rows)
		if !ok {
			t.Fatalf("(%d, %d) rejected, want accepted", c.absX, c.absY)
		}
		if got := ev.Mouse(); got.X != c.wantX || got.Y != c.wantY {
			t.Errorf("(%d, %d) translated to (%d, %d), want (%d, %d)",
				c.absX, c.absY, got.X, got.Y, c.wantX, c.wantY)
		}
	}
}

// TestPaneMouseEventBounds walks all four corners of the content area and all
// four cells immediately outside it. Out-of-bounds events must be REJECTED, never
// clamped: a clamped event delivers a real click at a cell the user never
// touched.
func TestPaneMouseEventBounds(t *testing.T) {
	const originX, originY, cols, rows = 22, 3, 46, 18
	const lastX, lastY = originX + cols - 1, originY + rows - 1

	inside := []struct {
		name         string
		absX, absY   int
		wantX, wantY int
	}{
		{"top-left", originX, originY, 0, 0},
		{"top-right", lastX, originY, cols - 1, 0},
		{"bottom-left", originX, lastY, 0, rows - 1},
		{"bottom-right", lastX, lastY, cols - 1, rows - 1},
	}
	for _, c := range inside {
		ev, ok := paneMouseEvent(mouseAt(c.absX, c.absY), originX, originY, cols, rows)
		if !ok {
			t.Fatalf("%s cell (%d, %d) rejected, want accepted", c.name, c.absX, c.absY)
		}
		if got := ev.Mouse(); got.X != c.wantX || got.Y != c.wantY {
			t.Errorf("%s cell translated to (%d, %d), want (%d, %d)",
				c.name, got.X, got.Y, c.wantX, c.wantY)
		}
	}

	outside := []struct {
		name       string
		absX, absY int
	}{
		{"one left of the content area", originX - 1, originY},
		{"one right of the content area", lastX + 1, originY},
		{"one above the content area", originX, originY - 1},
		{"one below the content area", originX, lastY + 1},
	}
	for _, c := range outside {
		if ev, ok := paneMouseEvent(mouseAt(c.absX, c.absY), originX, originY, cols, rows); ok {
			t.Errorf("%s (%d, %d) accepted as %v, want rejected", c.name, c.absX, c.absY, ev.Mouse())
		}
	}
}

// TestPaneMouseEventWheelWinsOverAction is the wheel-notch case: Bubble Tea
// reports a wheel turn with Action == MouseActionPress, so keying off the action
// would send the child a button click instead of a scroll. The button must
// survive too — the emulator encodes the direction from it.
func TestPaneMouseEventWheelWinsOverAction(t *testing.T) {
	cases := []struct {
		teaButton  tea.MouseButton
		wantButton termpane.MouseButton
	}{
		{tea.MouseWheelUp, termpane.MouseWheelUp},
		{tea.MouseWheelDown, termpane.MouseWheelDown},
		{tea.MouseWheelLeft, termpane.MouseWheelLeft},
		{tea.MouseWheelRight, termpane.MouseWheelRight},
	}
	for _, c := range cases {
		msg := tea.MouseClickMsg{X: 5, Y: 5, Button: c.teaButton}
		ev, ok := paneMouseEvent(msg, 0, 0, 46, 18)
		if !ok {
			t.Fatalf("%v rejected in bounds", c.teaButton)
		}
		if _, isWheel := ev.(termpane.MouseWheel); !isWheel {
			t.Errorf("%v with action Press produced %T, want termpane.MouseWheel", c.teaButton, ev)
		}
		if got := ev.Mouse().Button; got != c.wantButton {
			t.Errorf("%v mapped to button %v, want %v", c.teaButton, got, c.wantButton)
		}
	}
}

// TestPaneMouseEventConcreteTypePerAction asserts the three non-wheel actions
// produce three DIFFERENT concrete types. The type is load-bearing, not
// cosmetic: the emulator type-asserts it to decide the motion bit and the SGR
// release terminator, so a MouseClick standing in for a MouseRelease would leave
// the child believing the button is still down.
func TestPaneMouseEventConcreteTypePerAction(t *testing.T) {
	cases := []struct {
		msg      tea.MouseMsg
		wantType string
	}{
		{tea.MouseClickMsg{X: 3, Y: 4, Button: tea.MouseLeft}, "termpane.MouseClick"},
		{tea.MouseReleaseMsg{X: 3, Y: 4, Button: tea.MouseLeft}, "termpane.MouseRelease"},
		{tea.MouseMotionMsg{X: 3, Y: 4, Button: tea.MouseLeft}, "termpane.MouseMotion"},
	}
	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		ev, ok := paneMouseEvent(c.msg, 0, 0, 46, 18)
		if !ok {
			t.Fatalf("%T rejected in bounds", c.msg)
		}
		got := typeName(ev)
		if got != c.wantType {
			t.Errorf("%T produced %s, want %s", c.msg, got, c.wantType)
		}
		if seen[got] {
			t.Errorf("%T reused concrete type %s", c.msg, got)
		}
		seen[got] = true
	}
}

// typeName renders the concrete type of a mouse event with the termpane alias
// names the production code constructs, so a mismatch names both sides.
func typeName(ev termpane.MouseEvent) string {
	switch ev.(type) {
	case termpane.MouseClick:
		return "termpane.MouseClick"
	case termpane.MouseRelease:
		return "termpane.MouseRelease"
	case termpane.MouseWheel:
		return "termpane.MouseWheel"
	case termpane.MouseMotion:
		return "termpane.MouseMotion"
	}
	return fmt.Sprintf("%T", ev)
}

// TestMouseButtonMapsEveryButton covers all twelve buttons plus an unknown
// value. The switch is deliberately not a numeric cast, so this table is what
// proves the two enums still line up.
func TestMouseButtonMapsEveryButton(t *testing.T) {
	cases := []struct {
		in   tea.MouseButton
		want termpane.MouseButton
	}{
		{tea.MouseNone, termpane.MouseNone},
		{tea.MouseLeft, termpane.MouseLeft},
		{tea.MouseMiddle, termpane.MouseMiddle},
		{tea.MouseRight, termpane.MouseRight},
		{tea.MouseWheelUp, termpane.MouseWheelUp},
		{tea.MouseWheelDown, termpane.MouseWheelDown},
		{tea.MouseWheelLeft, termpane.MouseWheelLeft},
		{tea.MouseWheelRight, termpane.MouseWheelRight},
		{tea.MouseBackward, termpane.MouseBackward},
		{tea.MouseForward, termpane.MouseForward},
		{tea.MouseButton10, termpane.MouseButton10},
		{tea.MouseButton11, termpane.MouseButton11},
	}
	if len(cases) != 12 {
		t.Fatalf("table covers %d buttons, want all 12", len(cases))
	}
	for _, c := range cases {
		if got := mouseButton(c.in); got != c.want {
			t.Errorf("mouseButton(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// A button Bubble Tea may add later must degrade to "no button", which the
	// emulator encodes as the motion/none case — never to a real button.
	if got := mouseButton(tea.MouseButton(99)); got != termpane.MouseNone {
		t.Errorf("mouseButton(unknown) = %v, want termpane.MouseNone", got)
	}
}

// TestMouseModsFoldsEveryCombination walks all eight shift/alt/ctrl combinations
// and asserts each one arrives as the emulator's matching bitmask — the same
// vt.Mod* values forwardKeyToPane uses.
func TestMouseModsFoldsEveryCombination(t *testing.T) {
	for i := 0; i < 8; i++ {
		shift, alt, ctrl := i&1 != 0, i&2 != 0, i&4 != 0
		var want vt.KeyMod
		if shift {
			want |= vt.ModShift
		}
		if alt {
			want |= vt.ModAlt
		}
		if ctrl {
			want |= vt.ModCtrl
		}
		var teaMod tea.KeyMod
		if shift {
			teaMod |= tea.ModShift
		}
		if alt {
			teaMod |= tea.ModAlt
		}
		if ctrl {
			teaMod |= tea.ModCtrl
		}
		if got := mouseMods(tea.Mouse{Mod: teaMod}); got != want {
			t.Errorf("mouseMods(shift=%v alt=%v ctrl=%v) = %v, want %v", shift, alt, ctrl, got, want)
		}
	}
}

// TestPaneMouseEventCarriesModifiers is the end-to-end modifier path: the
// translated event must carry the mask, or a ctrl+wheel reaches the child as a
// bare wheel.
func TestPaneMouseEventCarriesModifiers(t *testing.T) {
	msg := tea.MouseWheelMsg{
		X: 10, Y: 6, Button: tea.MouseWheelUp,
		Mod: tea.ModShift | tea.ModCtrl,
	}
	ev, ok := paneMouseEvent(msg, 2, 2, 46, 18)
	if !ok {
		t.Fatalf("event rejected in bounds")
	}
	want := vt.ModShift | vt.ModCtrl
	if got := ev.Mouse().Mod; got != want {
		t.Errorf("translated Mod = %v, want %v", got, want)
	}
}

// TestForwardMouseToPaneDelivers asserts the delivery step hands the event
// through unchanged — it is a synchronous call on the handle, like
// forwardKeyToPane, not a Cmd.
func TestForwardMouseToPaneDelivers(t *testing.T) {
	fp := newFakePane("X")
	ev := termpane.MouseWheel{X: 4, Y: 9, Button: termpane.MouseWheelDown}

	forwardMouseToPane(fp, ev)

	if len(fp.mice) != 1 {
		t.Fatalf("recorded %d mouse events, want 1", len(fp.mice))
	}
	if got := fp.mice[0]; got != termpane.MouseEvent(ev) {
		t.Errorf("delivered %v, want %v", got, ev)
	}
}

// --- motion coalescing ---
//
// forwardMouseToPane is the ONLY place these tests can reach: routeMouse
// (model.go) and attachModel's Update (attachpane.go) both call it as a bare
// two-argument package function with no clock and no per-call state of their
// own, and both files are out of scope for this change. So the coalescing
// policy's only test seam is swapping the package-level mouseCoalescer
// itself, exactly as hostpane.go's wedgeBannerPane is tested by setting its
// unexported `now` field directly (same-package test, same technique). Every
// test below restores the real-clock coalescer on exit so it cannot leak a
// frozen clock into an unrelated test that happens to run afterward.

// fakeMotionClock returns a *time.Time-backed now func alongside the pointer
// a test advances, so a test reads as "moved forward by 2ms" rather than
// juggling a closure's captured variable inline.
func fakeMotionClock() (now func() time.Time, advance func(time.Duration)) {
	t0 := time.Unix(0, 0)
	cur := t0
	return func() time.Time { return cur }, func(d time.Duration) { cur = cur.Add(d) }
}

// withFakeMotionClock installs a deterministic coalescer for the duration of
// a test and restores the real-clock default on cleanup, so a forgotten
// frozen clock can never bleed into a later test in the same binary.
func withFakeMotionClock(t *testing.T) func(time.Duration) {
	t.Helper()
	now, advance := fakeMotionClock()
	mouseCoalescer = newMotionCoalescer(now)
	t.Cleanup(func() { mouseCoalescer = newMotionCoalescer(time.Now) })
	return advance
}

// TestForwardMouseToPaneCoalescesADrag is the acceptance test: a drag's
// MOTION events, delivered at a rate faster than the coalescing window,
// collapse into a bounded number of writes rather than one per cell. 2ms
// between events over mouseMotionCoalesceWindow (16ms) predicts roughly one
// forwarded write per 8 cells crossed; the assertion leaves generous room
// (a quarter of the cells) so it pins the SHAPE of the behaviour — bounded,
// not merely "fewer than all" — without being a change-detector on the exact
// constant.
func TestForwardMouseToPaneCoalescesADrag(t *testing.T) {
	advance := withFakeMotionClock(t)
	fp := newFakePane("X")

	const cells = 100
	for i := 0; i < cells; i++ {
		forwardMouseToPane(fp, termpane.MouseMotion{X: i, Y: 0})
		advance(2 * time.Millisecond)
	}

	if len(fp.mice) == 0 {
		t.Fatalf("every motion event was dropped; a coalesced drag must still make progress")
	}
	if got, want := len(fp.mice), cells/4; got >= want {
		t.Errorf("forwarded %d of %d motion events, want fewer than %d (well below one per cell)",
			got, cells, want)
	}
}

// TestForwardMouseToPaneMotionPassesThroughAfterTheWindowElapses is the
// complement to the drag test: coalescing is a time BOUND, not a drop-most
// policy, so a slower drag — one whose events already arrive past the
// window — must forward every one of them. A coalescer that silently
// thinned unconditionally, rather than gating on elapsed time, would still
// pass the bounded-writes assertion above while failing this one.
func TestForwardMouseToPaneMotionPassesThroughAfterTheWindowElapses(t *testing.T) {
	advance := withFakeMotionClock(t)
	fp := newFakePane("X")

	const events = 5
	for i := 0; i < events; i++ {
		forwardMouseToPane(fp, termpane.MouseMotion{X: i, Y: 0})
		advance(mouseMotionCoalesceWindow + time.Millisecond)
	}

	if len(fp.mice) != events {
		t.Fatalf("recorded %d of %d motion events spaced beyond the window, want all forwarded",
			len(fp.mice), events)
	}
}

// TestForwardMouseToPaneNeverCoalescesPressReleaseOrWheel pins the Must-NOT:
// only motion may be thinned. A press opens the burst, five motion events
// land inside one coalescing window (so the burst itself proves thinning is
// active), a wheel notch fires mid-drag, and a release closes it — press,
// wheel and release must each appear EXACTLY once, and in the order they
// were sent, with the release last.
func TestForwardMouseToPaneNeverCoalescesPressReleaseOrWheel(t *testing.T) {
	withFakeMotionClock(t) // clock frozen throughout: the whole burst is one window

	fp := newFakePane("X")
	press := termpane.MouseClick{X: 0, Y: 0, Button: termpane.MouseLeft}
	wheel := termpane.MouseWheel{X: 5, Y: 0, Button: termpane.MouseWheelDown}
	release := termpane.MouseRelease{X: 10, Y: 0, Button: termpane.MouseLeft}

	forwardMouseToPane(fp, press)
	for i := 1; i <= 5; i++ {
		forwardMouseToPane(fp, termpane.MouseMotion{X: i, Y: 0})
	}
	forwardMouseToPane(fp, wheel)
	forwardMouseToPane(fp, release)

	if len(fp.mice) < 3 {
		t.Fatalf("recorded %d events, want at least press+wheel+release plus any surviving motion: %v",
			len(fp.mice), fp.mice)
	}
	if got := fp.mice[0]; got != termpane.MouseEvent(press) {
		t.Errorf("first event = %v, want the press", got)
	}
	if got := fp.mice[len(fp.mice)-1]; got != termpane.MouseEvent(release) {
		t.Errorf("last event = %v, want the release", got)
	}

	var presses, wheels, releases int
	wheelIdx, releaseIdx := -1, -1
	for i, ev := range fp.mice {
		switch ev.(type) {
		case termpane.MouseClick:
			presses++
		case termpane.MouseWheel:
			wheels++
			wheelIdx = i
		case termpane.MouseRelease:
			releases++
			releaseIdx = i
		}
	}
	if presses != 1 || wheels != 1 || releases != 1 {
		t.Errorf("counts press=%d wheel=%d release=%d, want exactly one each in %v",
			presses, wheels, releases, fp.mice)
	}
	if wheelIdx < 0 || releaseIdx < 0 || wheelIdx >= releaseIdx {
		t.Errorf("wheel (index %d) must precede release (index %d), the order they were sent",
			wheelIdx, releaseIdx)
	}
}

// TestForwardMouseToPaneMotionCoalesceIsPerPane guards against a coalescer
// keyed on something coarser than pane identity (a single shared "last
// motion" timestamp): two panes receiving their FIRST motion at the same
// instant must each get it, and thinning one pane's later motion must not
// touch the other's count at all.
func TestForwardMouseToPaneMotionCoalesceIsPerPane(t *testing.T) {
	withFakeMotionClock(t)

	fpA := newFakePane("A")
	fpB := newFakePane("B")

	forwardMouseToPane(fpA, termpane.MouseMotion{X: 0, Y: 0})
	forwardMouseToPane(fpB, termpane.MouseMotion{X: 0, Y: 0})
	if len(fpA.mice) != 1 || len(fpB.mice) != 1 {
		t.Fatalf("first motion suppressed: A=%d B=%d, want 1 each", len(fpA.mice), len(fpB.mice))
	}

	forwardMouseToPane(fpA, termpane.MouseMotion{X: 1, Y: 0})
	if len(fpA.mice) != 1 {
		t.Errorf("A recorded %d motion events inside one window, want still 1", len(fpA.mice))
	}
	if len(fpB.mice) != 1 {
		t.Errorf("B's count changed to %d from an event sent only to A", len(fpB.mice))
	}
}

// TestFakePaneSizeIsSettableAndDefaultsTo80x24 pins the stub contract every
// bounds assertion depends on: the default must stay 80x24 (existing tests read
// it) and it must be overridable (or a bounds test at a larger terminal passes on
// the stub's limit instead of the geometry's).
func TestFakePaneSizeIsSettableAndDefaultsTo80x24(t *testing.T) {
	fp := newFakePane("X")
	if cols, rows := fp.Size(); cols != 80 || rows != 24 {
		t.Fatalf("newFakePane size = %dx%d, want 80x24", cols, rows)
	}
	fp.sizeCols, fp.sizeRows = 114, 37
	if cols, rows := fp.Size(); cols != 114 || rows != 37 {
		t.Errorf("after override, size = %dx%d, want 114x37", cols, rows)
	}
}

// --- Update wiring ---

// mouseModel builds a dashboard with a live pane and returns it alongside the
// content origin and bounds the mouse path will compute, so a test can aim an
// event at a real cell without duplicating the geometry arithmetic.
func mouseModel(t *testing.T) (Model, *fakePane, int, int, int, int) {
	t.Helper()
	fp := newFakePane("X")
	m := testModel(runningSess("live"))
	m.pane = fp
	m.paneName = "live"

	listOuter, mainOuter, bodyHeight := m.layoutDims()
	cols, rows := paneContentDims(mainOuter, bodyHeight)
	paneCols, paneRows := fp.Size()
	cols, rows = mouseBounds(cols, rows, paneCols, paneRows)
	contentW, _ := paneContentDims(mainOuter, bodyHeight)
	originX, originY := paneContentOrigin(sidebarRenderedWidth(listOuter),
		lipgloss.Height(m.renderHeader()),
		paneTitleBlock(m.paneName, m.paneTitleHint(), contentW))
	return m, fp, originX, originY, cols, rows
}

// TestMouseInPaneReachesTheSessionRegardlessOfFocus is the scroll-follows-mouse
// contract: a wheel notch over the pane reaches the session whether or not the
// KEYBOARD is focused there. Gating this on m.paneFocused would make the common
// gesture — look at the pane, scroll it — a two-step one.
func TestMouseInPaneReachesTheSessionRegardlessOfFocus(t *testing.T) {
	for _, focused := range []bool{false, true} {
		m, fp, originX, originY, _, _ := mouseModel(t)
		m.paneFocused = focused

		msg := tea.MouseWheelMsg{
			X: originX, Y: originY,
			Button: tea.MouseWheelDown,
		}
		if _, cmd := update(t, m, msg); cmd != nil {
			t.Errorf("focused=%v: the mouse path must return no Cmd, got one", focused)
		}
		if len(fp.mice) != 1 {
			t.Fatalf("focused=%v: recorded %d mouse events, want 1", focused, len(fp.mice))
		}
		ev := fp.mice[0]
		if _, isWheel := ev.(termpane.MouseWheel); !isWheel {
			t.Errorf("focused=%v: delivered %T, want termpane.MouseWheel", focused, ev)
		}
		if got := ev.Mouse(); got.X != 0 || got.Y != 0 {
			t.Errorf("focused=%v: the origin cell arrived as (%d, %d), want (0, 0)",
				focused, got.X, got.Y)
		}
	}
}

// TestMouseOutsideThePaneIsDropped covers the four cells just outside the content
// area plus the sidebar. None of them may reach the session: the sidebar has no
// mouse behaviour, so there is nothing else for these events to mean, and clamping
// one would deliver a real click at a cell the user never touched.
func TestMouseOutsideThePaneIsDropped(t *testing.T) {
	m, fp, originX, originY, cols, rows := mouseModel(t)

	cases := []struct {
		name       string
		absX, absY int
	}{
		{"one left of the content area", originX - 1, originY},
		{"one right of the content area", originX + cols, originY},
		{"one above the content area (the title row)", originX, originY - 1},
		{"one below the content area", originX, originY + rows},
		{"the sidebar", 2, originY},
		{"the header", originX, 0},
	}
	for _, c := range cases {
		m, _ = update(t, m, mouseAt(c.absX, c.absY))
		if len(fp.mice) != 0 {
			t.Fatalf("%s (%d, %d) was forwarded as %v, want dropped",
				c.name, c.absX, c.absY, fp.mice)
		}
	}
}

// TestMouseIsDroppedWhenTheDashboardOwnsInput pins the three guards. The reasons
// differ and matter: a nil handle is a queued event arriving after paneExitedMsg;
// formOpen is the ONLY state that removes the pane from the frame; and a modal
// mode still SHOWS the pane but OWNS input, with a taller footer that shrinks the
// region without a Resize.
func TestMouseIsDroppedWhenTheDashboardOwnsInput(t *testing.T) {
	cases := []struct {
		name  string
		apply func(m *Model)
	}{
		{"no pane open", func(m *Model) { m.pane = nil }},
		{"creation form active", func(m *Model) { m.formOpen = true }},
		{"clean confirmation up", func(m *Model) { m.modal = inputConfirmClean }},
		{"spawn-error modal up", func(m *Model) { m.modal = inputSpawnError }},
	}
	for _, c := range cases {
		m, fp, originX, originY, _, _ := mouseModel(t)
		c.apply(&m)

		if _, cmd := update(t, m, mouseAt(originX, originY)); cmd != nil {
			t.Errorf("%s: the mouse path must return no Cmd", c.name)
		}
		if len(fp.mice) != 0 {
			t.Errorf("%s: forwarded %v, want nothing", c.name, fp.mice)
		}
	}
}

// TestMouseChordTogglesRealCaptureFromBothBranches is the toggle's core contract.
// The DECLARED mouse mode is not optional: it is what emits the real DEC
// sequences, and without it the host terminal stays in mouse-reporting mode, so
// native click-drag selection — the entire purpose of the chord — would remain
// broken no matter what the flag says.
//
// Under Bubble Tea v1 that declaration was a Cmd (tea.DisableMouse /
// tea.EnableMouseCellMotion) and this test asserted the returned Cmd's Msg. v2
// moved it onto the frame: View().MouseMode is re-stated every render and the
// renderer writes ?1002/?1006 when it changes, so the toggle correctly returns
// no Cmd at all. The guarantee is identical and still fully observable — only
// where it is observed moved — so the assertion is retargeted rather than
// dropped. Dropping it would leave ctrl+g free to become a routing-only flag
// over a terminal still reporting mouse events, which is the exact regression
// this test exists to catch.
func TestMouseChordTogglesRealCaptureFromBothBranches(t *testing.T) {
	chord := tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}

	for _, focused := range []bool{false, true} {
		m, fp, _, _, _, _ := mouseModel(t)
		m.paneFocused = focused
		if !m.mouseCapture {
			t.Fatalf("focused=%v: precondition — capture starts on", focused)
		}

		m, _ = update(t, m, chord)
		if m.mouseCapture {
			t.Errorf("focused=%v: the first chord should release capture", focused)
		}
		if got := m.View().MouseMode; got != tea.MouseModeNone {
			t.Errorf("focused=%v: after release View declares MouseMode %v, want tea.MouseModeNone (%v)",
				focused, got, tea.MouseModeNone)
		}
		if m.status == "" {
			t.Errorf("focused=%v: releasing capture should notify", focused)
		}
		// The chord is reserved: it must never also reach the session as ctrl+g.
		if len(fp.keys) != 0 || len(fp.texts) != 0 {
			t.Errorf("focused=%v: the chord leaked to the session (keys=%v texts=%v)",
				focused, fp.keys, fp.texts)
		}

		m, _ = update(t, m, chord)
		if !m.mouseCapture {
			t.Errorf("focused=%v: the second chord should take capture back", focused)
		}
		if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
			t.Errorf("focused=%v: after re-capture View declares MouseMode %v, want tea.MouseModeCellMotion (%v)",
				focused, got, tea.MouseModeCellMotion)
		}
	}
}

// TestMouseChordUsesTheKeymapBinding proves the chord is bound declaratively from
// mouseChordKey rather than matched as a bare tea.KeyType, so retargeting it stays
// the one-line change the constant promises.
func TestMouseChordUsesTheKeymapBinding(t *testing.T) {
	m := testModel(sess("a"))
	if got := m.keys.Mouse.Keys(); len(got) != 1 || got[0] != mouseChordKey {
		t.Fatalf("keys.Mouse is bound to %v, want [%s]", got, mouseChordKey)
	}
	if !key.Matches(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}, m.keys.Mouse) {
		t.Errorf("keys.Mouse does not match a real ctrl+g keypress")
	}
}

// TestMouseCaptureStateSurvivesTruncationOnTheFocusedHelpLine asserts the capture
// state is legible at the narrowest terminal anyone runs, in both states — on the
// FOCUSED help line, which is the one line that carries the hint.
//
// Truncation is the whole point of the assertion. renderFooter cuts the help line
// at m.width, so a hint placed past column 80 is invisible on the narrowest
// terminal anyone runs no matter what renderHelp returns. The focused line has
// room (44 cells plus the 18-cell hint and its separator = 65), and it is what the
// user is reading while scrolling a session, which is exactly when they need to
// know where the mouse is going.
//
// The unfocused line deliberately does NOT carry the hint — it is oversubscribed
// at 105 measured cells with a pane open (90 with none), where the hint could only
// be bought by cutting an existing item. TestHelpLineAt80DisplacesNothing pins
// that it stays as main had it.
func TestMouseCaptureStateSurvivesTruncationOnTheFocusedHelpLine(t *testing.T) {
	m := testModel(sess("live"))
	m.pane = newFakePane("X")
	m.paneName = "live"
	m.paneFocused = true

	for _, capture := range []bool{true, false} {
		m.mouseCapture = capture
		want := m.mouseCaptureHint()
		// Stripped: help.Model styles the key and the description separately
		// (theme.helpStyles), so the raw line has an escape between them.
		shown := xansi.Strip(truncate(m.renderHelp(), 80))
		if !strings.Contains(shown, want) {
			t.Errorf("capture=%v: focused help at 80 columns = %q, want it to contain %q",
				capture, shown, want)
		}
	}

	// The two states must be distinguishable, or the indicator reports nothing.
	m.mouseCapture = true
	on := m.mouseCaptureHint()
	m.mouseCapture = false
	if off := m.mouseCaptureHint(); off == on {
		t.Errorf("both capture states render the same hint %q", on)
	}
}

// TestPaneHandleIsSatisfiedByRealPane is the compile-time proof that the seam
// still matches termpane.Pane's surface, which openHostPane's return type
// (hostpane.go:38) also enforces at build time.
func TestPaneHandleIsSatisfiedByRealPane(t *testing.T) {
	var _ paneHandle = (*termpane.Pane)(nil)
}

// --- frame truth ---
//
// The tests below are the ones that can catch a coordinate origin that is
// self-consistently wrong. Every other test in this file compares the helpers
// against each other or against a hand-written expectation; these compare them
// against the string View() actually produced. That distinction is the whole
// point: the pre-fix origin used the sidebar's OUTER width, which is a perfectly
// reasonable-looking expression that is 4 cells too large, and no amount of
// re-applying the production formula would have noticed.

// paneSentinel is the fake pane's whole rendered screen in the frame-truth
// tests. It is deliberately alphanumeric and unlike anything the dashboard
// chrome draws, so a match in the frame is the pane's content and not a border,
// a badge or a help string.
const paneSentinel = "SENTINEL"

// findSentinel returns the RUNE column and the line index at which sentinel
// appears in frame, or (-1, -1) if it does not appear at all.
//
// The rune offset is load-bearing. The box borders are multi-byte (│ is three
// bytes in UTF-8), so strings.Index's byte offset over-counts every border and
// padding cell to the sentinel's left — at 80x24 it reports 30 where the real
// column is 26. A test built on that number would "pass" against an origin that
// is 4 cells wrong, which is exactly the bug this file exists to catch.
//
// The ANSI strip has to happen first for the same reason at larger scale: the
// styled frame carries dozens of escape sequences per line, none of which
// occupy a cell.
func findSentinel(frame, sentinel string) (col, line int) {
	for i, ln := range strings.Split(xansi.Strip(frame), "\n") {
		if b := strings.Index(ln, sentinel); b >= 0 {
			return len([]rune(ln[:b])), i
		}
	}
	return -1, -1
}

// fit renders frame the way Bubble Tea's renderer puts it on the terminal: each
// line truncated to width and stripped of styling. The truncation is the
// renderer's, not the Model's — the renderer draws View's string into a buffer
// bounded by the terminal (cursed_renderer.go:295 builds the frame area, :345
// draws into it) and ultraviolet's printString throws away every cell whose
// position falls outside those bounds instead of wrapping it to the next row
// (styled.go:164-171; the wrap branch at :164 is skipped because Draw passes
// !StyledString.Wrap as `truncate` and Wrap defaults to false, :50-61). That is
// why a View() that returns a 105-cell help line still produces a 24-row frame
// on an 80-column terminal.
//
// Asserting against this rather than against the raw View() is what makes a
// visibility claim about the help line honest: the string renderHelp returns says
// nothing about what the user can read.
func fit(frame string, width int) string {
	lines := strings.Split(xansi.Strip(frame), "\n")
	for i, ln := range lines {
		lines[i] = xansi.Truncate(ln, width, "")
	}
	return strings.Join(lines, "\n")
}

// fitFrame is what the user SEES: both of the renderer's reductions, where fit()
// models only the width one. Height is clipped from the BOTTOM — the first
// `height` rows survive and the tail is dropped — because both front ends set
// AltScreen on every frame, and under AltScreen cursed_renderer.go:295 fixes the
// frame area at Rect(0, 0, width, height) and :345 draws the view into exactly
// those bounds, so printString never writes a cell at y >= height (styled.go:171,
// `pos.In(bounds)`). The renderer's one top-dropping branch (:349-351) guards on
// frameHeight > s.height, where frameHeight is that same frame area's Dy() — equal
// to s.height under AltScreen — so it cannot fire on either of this program's
// frames. Height was the missing axis — every narrow-terminal sweep varied width at
// a fixed height, so no test ever built a frame taller than its terminal.
//
// fit() is left as it was; the two agree whenever the frame fits.
func fitFrame(frame string, width, height int) string {
	lines := strings.Split(xansi.Strip(frame), "\n")
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	for i, ln := range lines {
		lines[i] = xansi.Truncate(ln, width, "")
	}
	return strings.Join(lines, "\n")
}

// TestFitFrameKeepsTheHeadRowsLikeTheRenderer pins the harness against the
// renderer's arithmetic. A fitFrame that kept the TAIL rows instead would make a
// top-cropping origin look correct and the correct one look broken, so every
// taller-than-screen assertion below rests on this one.
func TestFitFrameKeepsTheHeadRowsLikeTheRenderer(t *testing.T) {
	const frame = "AAA\nBBB\nCCC\nDDD"

	if got := fitFrame(frame, 3, 4); got != frame {
		t.Errorf("a frame that fits must be unchanged, got %q", got)
	}
	if got := fitFrame(frame, 3, 10); got != frame {
		t.Errorf("a frame shorter than the terminal must be unchanged, got %q", got)
	}
	if got, want := fitFrame(frame, 3, 2), "AAA\nBBB"; got != want {
		t.Errorf("fitFrame(…, 2) = %q, want %q — the clip must take the HEAD, not the tail", got, want)
	}
	if got, want := fitFrame(frame, 3, 1), "AAA"; got != want {
		t.Errorf("fitFrame(…, 1) = %q, want %q", got, want)
	}
	// A zero-height terminal is not a frame the renderer ever draws (it would
	// clip everything, not nothing). The harness declines to clip so that a
	// caller who forgot to pass a height gets the whole frame back rather than
	// an empty string that would silently satisfy a "not visible" assertion.
	if got := fitFrame(frame, 3, 0); got != frame {
		t.Errorf("height 0 must not clip, got %q", got)
	}
	if got, want := fitFrame("AAAA\nBBBB", 2, 1), "AA"; got != want {
		t.Errorf("fitFrame must truncate width after clipping height: %q, want %q", got, want)
	}
	if a, b := fitFrame(frame, 3, 4), fit(frame, 3); a != b {
		t.Errorf("fitFrame and fit disagree on a fitting frame: %q vs %q", a, b)
	}
}

// dashboardOrigin computes the content origin exactly as Model.routeMouse does,
// so the frame-truth tests can compare the PRODUCTION arithmetic against the
// rendered frame. It duplicates routeMouse's expression on purpose — routeMouse
// returns nothing a test can read, and the alternative (asserting a hand-typed
// constant per terminal size) would pin the measurements without proving the
// shipped code agrees with them.
func dashboardOrigin(m Model) (x, y int) {
	listOuter, mainOuter, bodyHeight, headerRows, _ := m.mouseFrameLayout()
	contentW, _ := paneContentDims(mainOuter, bodyHeight)
	return paneContentOrigin(sidebarRenderedWidth(listOuter), headerRows,
		paneTitleBlock(m.paneName, m.paneTitleHint(), contentW))
}

// attachOrigin is the same duplication on attach's side, for the same reason.
func attachOrigin(m attachModel) (x, y int) {
	cols, _ := m.paneSize()
	return paneContentOrigin(0, 0, m.titleBlock(cols))
}

// TestMouseFrameLayoutAgreesWithLayoutDims is the anti-drift pin for
// mouseFrameLayout repeating layoutDims' arithmetic rather than calling it (which
// is what keeps the mouse path to one header and one footer render per event). If
// the two ever disagree, every forwarded coordinate is wrong by the difference.
//
// frameRows is checked against the height of the frame View actually returns, not
// against the sum again — that is the claim that matters, and it is the one a
// changed View composition would break.
func TestMouseFrameLayoutAgreesWithLayoutDims(t *testing.T) {
	for _, width := range []int{20, 40, 80, 200} {
		for _, height := range []int{4, 5, 6, 7, 10, 24, 60} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				m, _ := frameModel("live", width, height, true)

				wantList, wantMain, wantBody := m.layoutDims()
				listOuter, mainOuter, bodyHeight, headerRows, frameRows := m.mouseFrameLayout()
				if listOuter != wantList || mainOuter != wantMain || bodyHeight != wantBody {
					t.Errorf("mouseFrameLayout returned dims (%d, %d, %d), layoutDims says (%d, %d, %d)",
						listOuter, mainOuter, bodyHeight, wantList, wantMain, wantBody)
				}
				if got := lipgloss.Height(m.renderHeader()); headerRows != got {
					t.Errorf("headerRows = %d, the rendered header is %d rows", headerRows, got)
				}
				if got := lipgloss.Height(render(m)); frameRows != got {
					t.Errorf("frameRows = %d, but View returns %d rows", frameRows, got)
				}
			})
		}
	}
}

// TestPaneRegionSizeMatchesTheMousePathsInlinedDims pins the equivalence W3.T1
// item 3 prescribed and the code inlines: routeMouse calls
// paneContentDims(mainOuter, bodyHeight) directly instead of m.paneRegionSize(),
// to avoid re-entering layoutDims and re-rendering the header and footer on every
// event. That is only sound while the two produce the same numbers — and
// paneRegionSize is what openPaneCmd sizes the real emulator with, so a divergence
// would mean the pane the user clicks is not the size the session was told about.
func TestPaneRegionSizeMatchesTheMousePathsInlinedDims(t *testing.T) {
	for _, width := range []int{10, 24, 40, 80, 200} {
		for _, height := range []int{4, 6, 7, 24, 60} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				m, _ := frameModel("live", width, height, true)

				wantCols, wantRows := m.paneRegionSize()
				_, mainOuter, bodyHeight, _, _ := m.mouseFrameLayout()
				gotCols, gotRows := paneContentDims(mainOuter, bodyHeight)
				if gotCols != wantCols || gotRows != wantRows {
					t.Errorf("the mouse path's inlined region is %dx%d but paneRegionSize is %dx%d",
						gotCols, gotRows, wantCols, wantRows)
				}
			})
		}
	}
}

// frameModel builds a dashboard of the given terminal size whose pane renders
// nothing but paneSentinel, with the emulator sized to the region so the
// mouseBounds minimum never masks a region-vs-emulator disagreement.
func frameModel(name string, width, height int, focused bool) (Model, *fakePane) {
	fp := newFakePane(paneSentinel)
	m := testModel(runningSess(name))
	m.pane = fp
	m.paneName = name
	m.paneFocused = focused
	m.width, m.height = width, height
	_, mainOuter, bodyHeight := m.layoutDims()
	fp.sizeCols, fp.sizeRows = paneContentDims(mainOuter, bodyHeight)
	return m, fp
}

// TestDashboardContentOriginMatchesTheRenderedFrame is the primary assertion of
// this whole change: at every listed terminal size, the origin the mouse path
// computes is the cell the pane's content occupies in the frame View() returned,
// and a mouse event aimed at that cell arrives at the emulator's (0, 0).
//
// The three long-name sizes are not padding. A 33-character session name makes
// the focused title 62 cells, which the BOX wraps onto two rows at 90 columns
// and three at 60 — while lipgloss.Height of the unwrapped string still reports
// 1. Before paneTitleBlock truncated it, the computed Y was 3 at all six sizes
// and the real Y was 3, 3, 5, 4, 4, 4: correct at exactly the two sizes anyone
// would have eyeballed. Keep the long-name rows.
func TestDashboardContentOriginMatchesTheRenderedFrame(t *testing.T) {
	longName := strings.Repeat("s", 33)
	cases := []struct {
		name          string
		width, height int
		wantX, wantY  int
	}{
		// wantX is listOuter (the sidebar fills its full outer width — the
		// W5.T1 fix cycle's renderList change) plus the pane box's border and
		// padding cell. listOuter is sidebarOuterWidth: 38% of the terminal,
		// floored at 30, so 60, 70 and 80 columns all land on the floor.
		{"live", 80, 24, 32, 3},
		{"live", 120, 40, 47, 3},
		{longName, 60, 20, 32, 3},
		{longName, 70, 20, 32, 3},
		{longName, 90, 24, 36, 3},
		{longName, 100, 30, 40, 3},
	}
	for _, c := range cases {
		label := fmt.Sprintf("%dx%d name=%d", c.width, c.height, len(c.name))
		t.Run(label, func(t *testing.T) {
			m, fp := frameModel(c.name, c.width, c.height, true)

			gotX, gotY := findSentinel(render(m), paneSentinel)
			if gotX < 0 {
				t.Fatalf("the pane's content is not in the frame at all:\n%s", xansi.Strip(render(m)))
			}
			// The measured position is asserted against a literal as well as
			// against the helper, so a change that moves BOTH the frame and the
			// arithmetic still fails here rather than agreeing with itself.
			if gotX != c.wantX || gotY != c.wantY {
				t.Errorf("the frame puts the pane's content at (%d, %d), want (%d, %d)",
					gotX, gotY, c.wantX, c.wantY)
			}
			if x, y := dashboardOrigin(m); x != gotX || y != gotY {
				t.Errorf("the mouse path computes origin (%d, %d), but the frame renders the content at (%d, %d)",
					x, y, gotX, gotY)
			}

			// The round trip: the cell the user sees the content's first character
			// in must arrive at the emulator's first cell.
			if _, cmd := update(t, m, mouseAt(gotX, gotY)); cmd != nil {
				t.Errorf("the mouse path must return no Cmd, got one")
			}
			if len(fp.mice) != 1 {
				t.Fatalf("recorded %d mouse events, want 1", len(fp.mice))
			}
			if got := fp.mice[0].Mouse(); got.X != 0 || got.Y != 0 {
				t.Errorf("a click on the content's top-left cell (%d, %d) arrived at (%d, %d), want (0, 0)",
					gotX, gotY, got.X, got.Y)
			}
		})
	}
}

// TestDashboardContentRegionMatchesTheRenderedFrame extends the origin check to
// the region's far corner: the emulator's LAST row must be the last row the
// frame draws inside the box. The origin alone cannot catch a row count that is
// one too large — that error shows up only as a click below the box being
// accepted, which is the assertion at the end of this test.
func TestDashboardContentRegionMatchesTheRenderedFrame(t *testing.T) {
	for _, c := range []struct{ width, height int }{{80, 24}, {120, 40}, {60, 20}} {
		t.Run(fmt.Sprintf("%dx%d", c.width, c.height), func(t *testing.T) {
			m, fp := frameModel("live", c.width, c.height, true)
			_, mainOuter, bodyHeight := m.layoutDims()
			cols, rows := paneContentDims(mainOuter, bodyHeight)

			// A screen with one identifiable marker per row, so the frame can be
			// asked where the emulator's first and last rows actually landed.
			marks := make([]string, rows)
			for i := range marks {
				marks[i] = fmt.Sprintf("R%02dX", i)
			}
			fp.render = strings.Join(marks, "\n")

			frame := render(m)
			_, firstLine := findSentinel(frame, marks[0])
			_, lastLine := findSentinel(frame, marks[rows-1])
			originX, originY := dashboardOrigin(m)
			if firstLine != originY {
				t.Errorf("the emulator's first row renders on line %d, but the origin says %d",
					firstLine, originY)
			}
			if want := originY + rows - 1; lastLine != want {
				t.Errorf("the emulator's last row renders on line %d, but origin+rows-1 is %d",
					lastLine, want)
			}

			// The far corner must be reachable, and the cell past it must not be:
			// the region is [origin, origin+rows) exactly.
			m2, _ := update(t, m, mouseAt(originX+cols-1, originY+rows-1))
			if len(fp.mice) != 1 {
				t.Fatalf("the region's bottom-right cell recorded %d events, want 1", len(fp.mice))
			}
			if got := fp.mice[0].Mouse(); got.X != cols-1 || got.Y != rows-1 {
				t.Errorf("the bottom-right cell arrived at (%d, %d), want (%d, %d)",
					got.X, got.Y, cols-1, rows-1)
			}
			update(t, m2, mouseAt(originX, originY+rows))
			if len(fp.mice) != 1 {
				t.Errorf("the row below the region was forwarded as %v, want dropped", fp.mice[1:])
			}
		})
	}
}

// TestDashboardPaneBoxGeometryUnchanged pins the rendered box's outer width and
// its inner content width, so a future "fix" to the origin cannot pay for itself
// by resizing the box and leaving the geometry wrong in a new way.
//
// The outer width is contentW + 4, NOT mainOuter: mainOuter is 50 at 80x24 while
// the box renders 48 cells wide, and 75 against 73 at 120x40. renderPane styles
// the box at Width(contentW+2) and lipgloss v1's Width includes the padding but
// not the border, so the border's 2 cells are all that gets added.
func TestDashboardPaneBoxGeometryUnchanged(t *testing.T) {
	for _, c := range []struct {
		width, height        int
		wantBoxW, wantInnerW int
	}{
		{80, 24, 48, 44},
		{120, 40, 73, 69},
	} {
		t.Run(fmt.Sprintf("%dx%d", c.width, c.height), func(t *testing.T) {
			m, _ := frameModel("live", c.width, c.height, true)
			_, mainOuter, bodyHeight := m.layoutDims()
			contentW, _ := paneContentDims(mainOuter, bodyHeight)

			if contentW != c.wantInnerW {
				t.Errorf("paneContentDims cols = %d, want %d", contentW, c.wantInnerW)
			}
			box := m.renderPane(mainOuter, bodyHeight)
			if got := lipgloss.Width(box); got != c.wantBoxW {
				t.Errorf("the rendered box is %d cells wide, want %d", got, c.wantBoxW)
			}
			if got := lipgloss.Width(box); got != contentW+4 {
				t.Errorf("the rendered box is %d cells wide, want contentW+4 = %d", got, contentW+4)
			}
			if mainOuter == contentW+4 {
				t.Errorf("mainOuter (%d) now equals the box width — the assertion has stopped distinguishing them",
					mainOuter)
			}
		})
	}
}

// TestDashboardMouseOutsideThePaneIsDropped is the out-of-bounds sweep against
// MEASURED coordinates: the four neighbour cells around the content area, the
// sidebar, the header, and the footer's help line. The two off-by-one neighbours
// (one cell left, one cell above) are the cases a wrong-by-one origin passes
// every other test with.
//
// The help-line row matters for its own reason: it is inside the EMULATOR's
// 24-row size but outside the 18-row region, so accepting it would mean the
// bounds trusted the emulator alone.
func TestDashboardMouseOutsideThePaneIsDropped(t *testing.T) {
	m, fp := frameModel("live", 80, 24, true)
	originX, originY := findSentinel(render(m), paneSentinel)
	if originX < 0 {
		t.Fatalf("precondition: the pane's content is not in the frame")
	}
	_, mainOuter, bodyHeight := m.layoutDims()
	cols, rows := paneContentDims(mainOuter, bodyHeight)

	cases := []struct {
		name       string
		absX, absY int
	}{
		{"one cell left of the content (the box's padding column)", originX - 1, originY},
		{"one cell above the content (the title row)", originX, originY - 1},
		{"one cell right of the content", originX + cols, originY},
		{"one row below the content", originX, originY + rows},
		{"the sidebar", 2, originY},
		{"the header", originX, 0},
		{"the footer's help line", originX, 23},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			update(t, m, mouseAt(c.absX, c.absY))
			if len(fp.mice) != 0 {
				t.Fatalf("(%d, %d) was forwarded as %v, want dropped", c.absX, c.absY, fp.mice)
			}
		})
	}
}

// TestDashboardMouseOnANarrowTerminalFollowsTheFrame covers the widths at which
// the main region collapses: minSidebarWidth gives the sidebar a 24-cell floor,
// so mainOuter is 16 at a 40-column terminal and 0 at 24 or below.
//
// The plan predicted the pane box "does not render at all" at these widths and
// that nothing would be forwarded. That is not what the frame does — see
// issues.md. paneContentDims floors cols at 1 instead of reporting the collapse,
// so the box always renders SOMETHING: a 5-cell box (2 border + 2 padding + 1
// content) whose single content column the frame really does draw, and which a
// user really can click. At 24 columns and below the box even overflows the
// terminal's right edge.
//
// So the assertion is the frame-truthful one rather than the predicted one: the
// set of cells that reach the session must be exactly the content rectangle the
// frame draws, clipped to the cells the terminal can actually report. Every cell
// in the terminal is swept, which is also the no-panic check.
func TestDashboardMouseOnANarrowTerminalFollowsTheFrame(t *testing.T) {
	// One rune, because contentW collapses to 1 and a longer sentinel would be
	// wrapped by the box and unfindable.
	const narrowSentinel = "@"

	for _, width := range []int{40, 36, 30, 24, 20, 10} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			const height = 20
			m, fp := frameModel("live", width, height, true)
			fp.render = narrowSentinel

			frameX, frameY := findSentinel(render(m), narrowSentinel)
			if frameX < 0 {
				t.Fatalf("the box renders no content column at all at width %d:\n%s",
					width, xansi.Strip(render(m)))
			}
			originX, originY := dashboardOrigin(m)
			if frameX != originX || frameY != originY {
				t.Fatalf("the frame draws the content at (%d, %d) but the mouse path computes (%d, %d)",
					frameX, frameY, originX, originY)
			}
			_, mainOuter, bodyHeight := m.layoutDims()
			cols, rows := paneContentDims(mainOuter, bodyHeight)

			// Sweep every cell the terminal can report and record which ones the
			// session saw, keyed by the emulator coordinate they arrived at.
			got := map[[2]int]bool{}
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					fp.mice = nil
					update(t, m, mouseAt(x, y))
					for _, ev := range fp.mice {
						got[[2]int{x, y}] = true
						if mev := ev.Mouse(); mev.X != x-frameX || mev.Y != y-frameY {
							t.Errorf("(%d, %d) arrived at (%d, %d), want (%d, %d)",
								x, y, mev.X, mev.Y, x-frameX, y-frameY)
						}
					}
				}
			}

			want := map[[2]int]bool{}
			for y := frameY; y < frameY+rows && y < height; y++ {
				for x := frameX; x < frameX+cols && x < width; x++ {
					want[[2]int{x, y}] = true
				}
			}
			for cell := range want {
				if !got[cell] {
					t.Errorf("(%d, %d) is inside the rendered content area but was dropped", cell[0], cell[1])
				}
			}
			for cell := range got {
				if !want[cell] {
					t.Errorf("(%d, %d) is outside the rendered content area but was forwarded", cell[0], cell[1])
				}
			}
			// At the narrowest widths the content column is past the terminal's own
			// right edge, so nothing can reach the session at all. Stating it
			// explicitly keeps the sweep above from silently degenerating into an
			// empty-set comparison at every width.
			if frameX >= width && len(got) != 0 {
				t.Errorf("the content column is at %d, off a %d-column terminal, yet %d cells forwarded",
					frameX, width, len(got))
			}
		})
	}
}

// TestDashboardMouseOnAShortTerminalFollowsTheVisibleFrame is the height-axis
// counterpart to the narrow-terminal sweep, and it covers the case that sweep
// structurally could not: layoutDims floors bodyHeight at 4, so at terminal heights
// 4-5 the frame is 6 rows (header 1 + body 4 + footer 1) and the renderer has to
// drop 2 or 1 of them. The overflow boundary used to be 7, when the footer was a
// status row plus the help line; the footer is the help line alone now.
//
// Which end they come off is the whole question, and the answer is the BOTTOM
// (fitFrame's doc cites the renderer): the frame's head rows stay exactly where
// View put them, so a row's index in the string View returned IS its row on
// screen and the origin needs no correction. The rows that vanish are the help
// line and the box's bottom border — which is why, on the shortest terminals, the
// visible bottom row is pane content rather than chrome.
//
// The sweep is over the frame the user actually sees (fitFrame, not View) and the
// forwarded set must be exactly the visible content rectangle. Heights above the
// overflow are included so the same assertion covers the fitting case, where
// nothing is dropped at all.
func TestDashboardMouseOnAShortTerminalFollowsTheVisibleFrame(t *testing.T) {
	const narrowSentinel = "@"

	for _, height := range []int{4, 5, 6, 7, 8, 12} {
		t.Run(fmt.Sprintf("height %d", height), func(t *testing.T) {
			const width = 80
			m, fp := frameModel("live", width, height, true)
			fp.render = narrowSentinel

			_, _, _, _, frameRows := m.mouseFrameLayout()
			if (height < 6) != (frameRows > height) {
				t.Fatalf("precondition: at height %d the frame is %d rows — "+
					"this test is only meaningful if heights below 6 overflow", height, frameRows)
			}

			// The VISIBLE frame: clipped and truncated exactly as the terminal does it.
			seenX, seenY := findSentinel(fitFrame(render(m), width, height), narrowSentinel)
			originX, originY := dashboardOrigin(m)
			if seenX >= 0 && (seenX != originX || seenY != originY) {
				t.Fatalf("the user sees the content at (%d, %d) but the mouse path computes (%d, %d) "+
					"(frame %d rows, terminal %d)",
					seenX, seenY, originX, originY, frameRows, height)
			}

			_, mainOuter, bodyHeight, _, _ := m.mouseFrameLayout()
			cols, rows := paneContentDims(mainOuter, bodyHeight)

			got := map[[2]int]bool{}
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					fp.mice = nil
					update(t, m, mouseAt(x, y))
					for _, ev := range fp.mice {
						got[[2]int{x, y}] = true
						if mev := ev.Mouse(); mev.X != x-originX || mev.Y != y-originY {
							t.Errorf("(%d, %d) arrived at (%d, %d), want (%d, %d)",
								x, y, mev.X, mev.Y, x-originX, y-originY)
						}
					}
				}
			}

			// The visible content rectangle, clipped to the terminal on both axes.
			want := map[[2]int]bool{}
			for y := originY; y < originY+rows && y < height; y++ {
				if y < 0 {
					continue
				}
				for x := originX; x < originX+cols && x < width; x++ {
					want[[2]int{x, y}] = true
				}
			}
			for cell := range want {
				if !got[cell] {
					t.Errorf("(%d, %d) is visible pane content but was dropped", cell[0], cell[1])
				}
			}
			for cell := range got {
				if !want[cell] {
					t.Errorf("(%d, %d) was forwarded but is not visible pane content", cell[0], cell[1])
				}
			}
			if len(want) == 0 {
				t.Errorf("no visible content cell at height %d — the sweep asserts nothing", height)
			}

			// The clip direction stated as what it changes about behaviour: the visible
			// bottom row reaches the session exactly when it is a content row. Under the
			// top-crop model it never could be — the surviving rows were the frame's
			// LAST ones, so the bottom row was always the help line — while under the
			// bottom clip at height 4 that row is content row 0 and dropping it would be
			// the bug. Asserting on a CLIPPED row instead would assert nothing: the sweep
			// only sends y < height, and no terminal reports a row at or past its own
			// height.
			visible := strings.Split(fitFrame(render(m), width, height), "\n")
			last := len(visible) - 1
			wantLast := last >= originY && last < originY+rows
			if forwarded := got[[2]int{originX, last}]; forwarded != wantLast {
				t.Errorf("the visible bottom row %d (%q) forwarded=%v, want %v (content rows %d..%d)",
					last, strings.TrimSpace(visible[last]), forwarded, wantLast, originY, originY+rows-1)
			}
		})
	}
}

// TestDashboardMouseIsDroppedWhileNoPaneIsPainted is the guard for the frames
// View() refuses to draw: before the first tea.WindowSizeMsg it returns the
// loading placeholder, and while quitting it returns "". Neither contains a pane
// box, but paneContentDims and sidebarOuterWidth floor their outputs at positive
// numbers, so the arithmetic answers anyway — a zero-size Model reports an origin
// of (14, 3) — and an event there would reach the session through a box that was
// never rendered.
//
// This is not a theoretical window. The renderer writes ?1002/?1006 as soon as
// the first frame declares MouseModeCellMotion, and the first WindowSizeMsg is
// just another message in the same queue, so a pointer already moving over the
// terminal produces exactly this ordering.
func TestDashboardMouseIsDroppedWhileNoPaneIsPainted(t *testing.T) {
	cases := []struct {
		name  string
		apply func(m *Model)
	}{
		{"before the first WindowSizeMsg", func(m *Model) {
			m.ready = false
			m.width, m.height = 0, 0
		}},
		{"ready but zero width", func(m *Model) { m.width = 0 }},
		{"ready but zero height", func(m *Model) { m.height = 0 }},
		{"quitting", func(m *Model) { m.quitting = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, fp := frameModel("live", 80, 24, true)
			c.apply(&m)
			if col, _ := findSentinel(render(m), paneSentinel); col >= 0 {
				t.Fatalf("precondition: View paints the pane in this state (column %d)", col)
			}
			// The origin the arithmetic still produces, plus the origin it produces
			// at the size this Model would have had: both must be rejected.
			originX, originY := dashboardOrigin(m)
			for _, at := range []tea.MouseMsg{mouseAt(originX, originY), mouseAt(26, 3), mouseAt(0, 0)} {
				update(t, m, at)
			}
			if len(fp.mice) != 0 {
				t.Errorf("forwarded %v while the frame shows no pane (its computed origin is (%d, %d))",
					fp.mice, originX, originY)
			}
		})
	}
}

// TestDashboardMouseIsDroppedWhileThePaneIsOpenButUndrawn covers the one state
// where m.pane is non-nil, the terminal is sized, no modal owns input, and the
// pane is STILL not on screen: the preview left a handle open for one session
// while the cursor sits on another and the keyboard is not focused into it, so
// renderDetail paints the detail panels instead (shouldRenderPane's false branch).
//
// It is the only guard in routeMouse that nothing else implies. Deleting the
// `!m.shouldRenderPane()` check leaves every other test in this file green,
// because they all set paneName to the selected session — which is exactly how
// the guard could be dropped in a refactor without a single failure.
//
// The second row is the same geometry with focus on, and it must FORWARD: focus
// is what makes shouldRenderPane keep an off-cursor pane visible, and a guard
// that ignored that would break scrolling a pane you are typing into.
func TestDashboardMouseIsDroppedWhileThePaneIsOpenButUndrawn(t *testing.T) {
	for _, c := range []struct {
		name        string
		focused     bool
		wantVisible bool
	}{
		{"unfocused — detail panels replace the pane", false, false},
		{"focused — the pane stays drawn off-cursor", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, fp := frameModel("live", 80, 24, c.focused)
			m.setSessions([]Session{runningSess("live"), runningSess("other")})
			m.selectSessionIndex(1)

			col, line := findSentinel(render(m), paneSentinel)
			if c.wantVisible != (col >= 0) {
				t.Fatalf("precondition: pane on screen = %v at (%d, %d), want on screen = %v",
					col >= 0, col, line, c.wantVisible)
			}

			// Aim at the origin the arithmetic produces either way — the point is that
			// the geometry still answers for a box the user cannot see.
			originX, originY := dashboardOrigin(m)
			update(t, m, mouseAt(originX, originY))

			if c.wantVisible && len(fp.mice) != 1 {
				t.Errorf("forwarded %v, want the one event: the pane is drawn at (%d, %d)",
					fp.mice, col, line)
			}
			if !c.wantVisible && len(fp.mice) != 0 {
				t.Errorf("forwarded %v while the frame shows no pane (computed origin (%d, %d))",
					fp.mice, originX, originY)
			}
		})
	}
}

// TestDashboardMouseIsDroppedWhenTheDashboardOwnsInput is the input-OWNERSHIP
// sweep, and the reason each case is here differs.
//
// formOpen is the only one of the three that also removes the pane from the
// frame (View renders the form in the main region instead). In BOTH modal modes
// the pane is still on screen at its usual origin — the assertion below proves
// that rather than assuming it — so these two guards are not about visibility at
// all: a modal owns the keyboard, Update's mode dispatch runs before the global
// keymap for that reason, and the mouse is input like any other.
//
// The spawn-error modal's fields are populated so its footer renders at its real
// four-row height, which shrinks the body region to 46x16 against a 24-row
// emulator. An empty modal would render two rows and quietly test the normal
// geometry instead.
func TestDashboardMouseIsDroppedWhenTheDashboardOwnsInput(t *testing.T) {
	cases := []struct {
		name        string
		apply       func(m *Model)
		paneVisible bool
	}{
		// The form is built, not just flagged: this case RENDERS the frame, and
		// renderNewForm calls the description textarea's View, which nil-derefs on a
		// zero-value newForm (bubbles' textarea.Model). Flagging alone is enough for
		// the non-rendering guard test above, not for this one.
		{"creation form active", func(m *Model) {
			m.formOpen = true
			m.form = newNewForm(m.stack, m.plainBranches, m.plainTrunk)
		}, false},
		{"clean confirmation up", func(m *Model) {
			m.modal = inputConfirmClean
			m.confirmTarget = "live"
			m.confirmPhase = state.PhaseExecuting
		}, true},
		{"spawn-error modal up", func(m *Model) {
			m.modal = inputSpawnError
			m.spawnErrName = "live"
			m.spawnErrHeadline = `afk new "live" failed: exit status 1`
			m.spawnErrTail = []string{"fatal: could not create worktree", "hint: the branch already exists"}
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, fp := frameModel("live", 80, 24, true)
			c.apply(&m)

			col, line := findSentinel(render(m), paneSentinel)
			if c.paneVisible {
				if col != 32 || line != 3 {
					t.Errorf("the pane should still be on screen at (32, 3) — the guard is about input "+
						"ownership, not visibility — but the frame has it at (%d, %d)", col, line)
				}
			} else if col >= 0 {
				t.Errorf("the pane should be gone from the frame, but it renders at (%d, %d)", col, line)
			}

			// Aim at the pane's REAL rendered position where it has one, so the
			// event is rejected by the ownership guard rather than by the bounds
			// check happening to disagree with the frame.
			at := mouseAt(30, 3)
			if col >= 0 {
				at = mouseAt(col, line)
			}
			if _, cmd := update(t, m, at); cmd != nil {
				t.Errorf("the mouse path must return no Cmd")
			}
			if len(fp.mice) != 0 {
				t.Errorf("forwarded %v, want nothing", fp.mice)
			}
		})
	}
}

// TestDashboardMouseWithNoPaneIsANoOp covers the nil handle: paneExitedMsg clears
// m.pane, and a mouse event decoded before that message was dispatched still
// arrives after it. There is nothing to forward to and nothing to panic over.
func TestDashboardMouseWithNoPaneIsANoOp(t *testing.T) {
	m, _ := frameModel("live", 80, 24, true)
	m.pane = nil
	if _, cmd := update(t, m, mouseAt(30, 3)); cmd != nil {
		t.Errorf("a mouse event with no pane must return no Cmd")
	}
}

// TestDashboardMouseForwardsRegardlessOfFocus is scroll-follows-mouse stated as a
// frame-truth assertion: the same MEASURED cell forwards identically whether or
// not the keyboard is focused on the pane. Gating the wheel on m.paneFocused
// would make "glance at the pane, scroll it" a two-step gesture.
//
// The origin is measured per focus state rather than once, because the two states
// render different title hints — a difference in their widths that changed the
// title's wrap would move the origin, and this test would then be comparing two
// different cells.
func TestDashboardMouseForwardsRegardlessOfFocus(t *testing.T) {
	for _, focused := range []bool{false, true} {
		t.Run(fmt.Sprintf("focused=%v", focused), func(t *testing.T) {
			m, fp := frameModel("live", 80, 24, focused)
			originX, originY := findSentinel(render(m), paneSentinel)
			if originX != 32 || originY != 3 {
				t.Fatalf("the frame puts the content at (%d, %d), want (32, 3)", originX, originY)
			}
			msg := tea.MouseWheelMsg{
				X: originX + 5, Y: originY + 2,
				Button: tea.MouseWheelDown,
			}
			update(t, m, msg)
			if len(fp.mice) != 1 {
				t.Fatalf("recorded %d mouse events, want 1", len(fp.mice))
			}
			if _, isWheel := fp.mice[0].(termpane.MouseWheel); !isWheel {
				t.Errorf("delivered %T, want termpane.MouseWheel", fp.mice[0])
			}
			if got := fp.mice[0].Mouse(); got.X != 5 || got.Y != 2 {
				t.Errorf("arrived at (%d, %d), want (5, 2)", got.X, got.Y)
			}
		})
	}
}

// --- afk attach (attachModel) ---
//
// internal/ui had no attachModel test before this file, so the harness below is
// new. It exists for one reason beyond coverage: attach's content origin is
// (0, 0)-based while the dashboard's is offset by the header and the sidebar, so
// a regression in either front end cannot hide behind the other's arithmetic.

// attachUpdate drives attachModel.Update and re-asserts the concrete type, the
// same shape ui_test.go's update helper has for Model.
func attachUpdate(t *testing.T, m attachModel, msg tea.Msg) (attachModel, tea.Cmd) {
	t.Helper()
	nm, cmd := m.Update(msg)
	got, ok := nm.(attachModel)
	if !ok {
		t.Fatalf("Update returned %T, want ui.attachModel", nm)
	}
	return got, cmd
}

// attachFrameModel builds a ready attachModel of the given terminal size whose
// pane renders paneSentinel, with the emulator sized to the region so the
// mouseBounds minimum never stands in for the geometry under test.
//
// ready is set directly rather than by sending a tea.WindowSizeMsg, because that
// path also calls pane.Resize and would leave the fake's recorder pre-populated
// for the resize assertions.
func attachFrameModel(name string, width, height int, status string) (attachModel, *fakePane) {
	fp := newFakePane(paneSentinel)
	m := attachModel{
		pane: fp, name: name, mouseCapture: true,
		width: width, height: height, ready: true, status: status,
	}
	fp.sizeCols, fp.sizeRows = m.paneSize()
	return m, fp
}

// TestAttachContentOriginMatchesTheRenderedFrame is the dashboard origin test's
// counterpart, and the widths are chosen for attach's own failure mode: its title
// is 52 cells with a 33-character name, so the box wrapped it onto a second row
// below 56 columns and pushed the emulator's first row down — measured Y was 4 at
// width 30 and 3 at 40 before paneTitleBlock truncated it. Every width below now
// reports Y=2, and the width-30 row is what proves the truncation is doing that
// rather than luck.
//
// Each width runs with and without a status line. The status line sits BELOW the
// box, so it must not move the origin — but it does shrink paneOuterHeight, which
// changes the region's row count, and asserting only the no-status case would
// leave that coupling untested.
func TestAttachContentOriginMatchesTheRenderedFrame(t *testing.T) {
	longName := strings.Repeat("s", 33)
	for _, width := range []int{30, 40, 56, 80} {
		for _, status := range []string{"", "pane resize failed: connection reset"} {
			t.Run(fmt.Sprintf("width %d status=%v", width, status != ""), func(t *testing.T) {
				m, fp := attachFrameModel(longName, width, 24, status)

				gotX, gotY := findSentinel(render(m), paneSentinel)
				if gotX < 0 {
					t.Fatalf("the pane's content is not in the frame:\n%s", xansi.Strip(render(m)))
				}
				if gotX != 2 || gotY != 2 {
					t.Errorf("the frame puts the content at (%d, %d), want (2, 2) — attach's box is at "+
						"(0, 0), so one border cell plus one padding cell in each axis", gotX, gotY)
				}
				cols, _ := m.paneSize()
				if x, y := paneContentOrigin(0, 0, m.titleBlock(cols)); x != gotX || y != gotY {
					t.Errorf("the mouse path computes origin (%d, %d), but the frame renders the content at (%d, %d)",
						x, y, gotX, gotY)
				}

				if _, cmd := attachUpdate(t, m, mouseAt(gotX, gotY)); cmd != nil {
					t.Errorf("the mouse path must return no Cmd, got one")
				}
				if len(fp.mice) != 1 {
					t.Fatalf("recorded %d mouse events, want 1", len(fp.mice))
				}
				if got := fp.mice[0].Mouse(); got.X != 0 || got.Y != 0 {
					t.Errorf("a click on the content's top-left cell arrived at (%d, %d), want (0, 0)",
						got.X, got.Y)
				}
			})
		}
	}
}

// TestAttachContentRegionMatchesTheRenderedFrame pins the region's far edge the
// way the dashboard test does: the emulator's last row must be the last row the
// box draws, and the row past it must be rejected. Attach is where this matters
// most, because paneOuterHeight — not a WindowSizeMsg — is what changes the row
// count when a status line appears.
func TestAttachContentRegionMatchesTheRenderedFrame(t *testing.T) {
	for _, width := range []int{40, 80} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			m, fp := attachFrameModel("live", width, 24, "")
			cols, rows := m.paneSize()

			marks := make([]string, rows)
			for i := range marks {
				marks[i] = fmt.Sprintf("R%02dX", i)
			}
			fp.render = strings.Join(marks, "\n")

			frame := render(m)
			_, firstLine := findSentinel(frame, marks[0])
			_, lastLine := findSentinel(frame, marks[rows-1])
			originX, originY := paneContentOrigin(0, 0, m.titleBlock(cols))
			if firstLine != originY {
				t.Errorf("the emulator's first row renders on line %d, but the origin says %d",
					firstLine, originY)
			}
			if want := originY + rows - 1; lastLine != want {
				t.Errorf("the emulator's last row renders on line %d, but origin+rows-1 is %d",
					lastLine, want)
			}

			m2, _ := attachUpdate(t, m, mouseAt(originX+cols-1, originY+rows-1))
			if len(fp.mice) != 1 {
				t.Fatalf("the region's bottom-right cell recorded %d events, want 1", len(fp.mice))
			}
			if got := fp.mice[0].Mouse(); got.X != cols-1 || got.Y != rows-1 {
				t.Errorf("the bottom-right cell arrived at (%d, %d), want (%d, %d)",
					got.X, got.Y, cols-1, rows-1)
			}
			attachUpdate(t, m2, mouseAt(originX, originY+rows))
			if len(fp.mice) != 1 {
				t.Errorf("the row below the region was forwarded as %v, want dropped", fp.mice[1:])
			}
		})
	}
}

// TestAttachMouseOutsideThePaneIsDropped is the out-of-bounds sweep, including
// both off-by-one neighbours. Attach has no header and no sidebar, so the cells
// above and left of the content are the box's own border and padding — which is
// precisely why an origin of (0, 0) or (1, 1) would look plausible and be wrong.
func TestAttachMouseOutsideThePaneIsDropped(t *testing.T) {
	m, fp := attachFrameModel("live", 80, 24, "pane resize failed: connection reset")
	originX, originY := findSentinel(render(m), paneSentinel)
	if originX < 0 {
		t.Fatalf("precondition: the pane's content is not in the frame")
	}
	cols, rows := m.paneSize()

	cases := []struct {
		name       string
		absX, absY int
	}{
		{"the box's left border", 0, originY},
		{"one cell left of the content (the box's padding column)", originX - 1, originY},
		{"the box's top border", originX, 0},
		{"one row above the content (the title row)", originX, originY - 1},
		{"one cell right of the content", originX + cols, originY},
		{"one row below the content", originX, originY + rows},
		{"the status line", originX, 23},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			attachUpdate(t, m, mouseAt(c.absX, c.absY))
			if len(fp.mice) != 0 {
				t.Fatalf("(%d, %d) was forwarded as %v, want dropped", c.absX, c.absY, fp.mice)
			}
		})
	}
}

// TestAttachMouseOnANarrowTerminalFollowsTheFrame is the narrow-terminal case on
// attach's side, and it is a different shape of narrow than the dashboard's:
// attach has no sidebar to hold a floor, so paneContentDims is handed m.width
// directly and the box shrinks with the terminal all the way down to its own
// floor of one content column at width 6.
//
// The origin stays (2, 2) at every width — attach's box is at (0, 0) and
// paneTitleBlock truncates the title to one row, so nothing about narrowing the
// terminal can move it. That invariance is the assertion: it is what a
// wrap-sensitive origin would break, and attach is where it used to break,
// because its 52-cell title wrapped below 56 columns.
//
// The sweep over every reportable cell is both the containment assertion — the
// forwarded set must be EXACTLY the content rectangle the frame draws, so
// neither over- nor under-forwarding passes — and the no-panic check.
func TestAttachMouseOnANarrowTerminalFollowsTheFrame(t *testing.T) {
	// One rune, because contentW collapses to 1 at the narrowest widths and a
	// longer sentinel would be wrapped by the box and unfindable.
	const narrowSentinel = "@"

	for _, width := range []int{40, 30, 20, 10, 6} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			const height = 20
			m, fp := attachFrameModel(strings.Repeat("s", 33), width, height, "")
			fp.render = narrowSentinel

			frameX, frameY := findSentinel(render(m), narrowSentinel)
			if frameX < 0 {
				t.Fatalf("the box renders no content column at all at width %d:\n%s",
					width, xansi.Strip(render(m)))
			}
			if frameX != 2 || frameY != 2 {
				t.Errorf("the frame draws the content at (%d, %d), want (2, 2) at every width — "+
					"the truncated title is one row however narrow the box gets", frameX, frameY)
			}
			cols, rows := m.paneSize()
			if x, y := paneContentOrigin(0, 0, m.titleBlock(cols)); x != frameX || y != frameY {
				t.Fatalf("the mouse path computes (%d, %d) but the frame draws the content at (%d, %d)",
					x, y, frameX, frameY)
			}

			got := map[[2]int]bool{}
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					fp.mice = nil
					attachUpdate(t, m, mouseAt(x, y))
					for _, ev := range fp.mice {
						got[[2]int{x, y}] = true
						if mev := ev.Mouse(); mev.X != x-frameX || mev.Y != y-frameY {
							t.Errorf("(%d, %d) arrived at (%d, %d), want (%d, %d)",
								x, y, mev.X, mev.Y, x-frameX, y-frameY)
						}
					}
				}
			}

			want := map[[2]int]bool{}
			for y := frameY; y < frameY+rows && y < height; y++ {
				for x := frameX; x < frameX+cols && x < width; x++ {
					want[[2]int{x, y}] = true
				}
			}
			for cell := range want {
				if !got[cell] {
					t.Errorf("(%d, %d) is inside the rendered content area but was dropped", cell[0], cell[1])
				}
			}
			for cell := range got {
				if !want[cell] {
					t.Errorf("(%d, %d) is outside the rendered content area but was forwarded", cell[0], cell[1])
				}
			}
			// The sweep must not silently degenerate into an empty-set comparison:
			// attach's content column is at 2 and its floor is one column, so there
			// is always at least one reachable cell however narrow the terminal.
			if len(want) == 0 {
				t.Errorf("no cell is inside the content area at width %d — the sweep asserts nothing", width)
			}
		})
	}
}

// TestAttachMouseOnAShortTerminalFollowsTheVisibleFrame is attach's height-axis
// sweep, and its overflow has a different cause than the dashboard's:
// paneOuterHeight floors at 4 while the status row is added BELOW the box, so at
// height 4 with a status set the frame is 5 rows for a 4-row terminal. The row the
// renderer drops is the STATUS row (fitFrame's doc cites the clip), not the box's
// top border: the box is drawn first, so its first row stays at 0 and the content
// origin does not move.
//
// Every height runs with and without a status line, because the status row is the
// whole mechanism: without it nothing overflows at these heights, and asserting only
// the overflowing case would not prove the origin is unmoved when the frame fits.
func TestAttachMouseOnAShortTerminalFollowsTheVisibleFrame(t *testing.T) {
	const narrowSentinel = "@"

	for _, height := range []int{3, 4, 5, 6, 10} {
		for _, status := range []string{"", "pane resize failed: connection reset"} {
			t.Run(fmt.Sprintf("height %d status=%v", height, status != ""), func(t *testing.T) {
				const width = 40
				m, fp := attachFrameModel(strings.Repeat("s", 33), width, height, status)
				fp.render = narrowSentinel

				wantOverflow := status != "" && height <= 4 || status == "" && height < 4
				if (m.frameRows() > height) != wantOverflow {
					t.Fatalf("at height %d (status=%v) the frame is %d rows, want overflow=%v",
						height, status != "", m.frameRows(), wantOverflow)
				}

				seenX, seenY := findSentinel(fitFrame(render(m), width, height), narrowSentinel)
				originX, originY := attachOrigin(m)
				if seenX >= 0 && (seenX != originX || seenY != originY) {
					t.Fatalf("the user sees the content at (%d, %d) but the mouse path computes "+
						"(%d, %d) (frame %d rows, terminal %d)",
						seenX, seenY, originX, originY, m.frameRows(), height)
				}

				cols, rows := m.paneSize()
				got := map[[2]int]bool{}
				for y := 0; y < height; y++ {
					for x := 0; x < width; x++ {
						fp.mice = nil
						attachUpdate(t, m, mouseAt(x, y))
						for _, ev := range fp.mice {
							got[[2]int{x, y}] = true
							if mev := ev.Mouse(); mev.X != x-originX || mev.Y != y-originY {
								t.Errorf("(%d, %d) arrived at (%d, %d), want (%d, %d)",
									x, y, mev.X, mev.Y, x-originX, y-originY)
							}
						}
					}
				}

				want := map[[2]int]bool{}
				for y := originY; y < originY+rows && y < height; y++ {
					if y < 0 {
						continue
					}
					for x := originX; x < originX+cols && x < width; x++ {
						want[[2]int{x, y}] = true
					}
				}
				for cell := range want {
					if !got[cell] {
						t.Errorf("(%d, %d) is visible pane content but was dropped", cell[0], cell[1])
					}
				}
				for cell := range got {
					if !want[cell] {
						t.Errorf("(%d, %d) was forwarded but is not visible pane content", cell[0], cell[1])
					}
				}
				if len(want) == 0 {
					t.Errorf("no visible content cell at height %d — the sweep asserts nothing", height)
				}

				// The clip direction, as behaviour: the visible bottom row reaches the
				// session exactly when it is a content row. It usually is not — the
				// status row sits below the box and is the frame's last row whenever the
				// frame fits — but at height 3 with a status the frame is 5 rows, only
				// rows 0-2 survive, and row 2 is content row 0. A blanket "the bottom row
				// is never forwarded" is the top-crop claim, and it would reject the
				// correct behaviour there. The clipped rows are not assertable at all:
				// the sweep sends only y < height, as a terminal does.
				bottom := height - 1
				wantBottom := bottom >= originY && bottom < originY+rows
				if forwarded := got[[2]int{originX, bottom}]; forwarded != wantBottom {
					t.Errorf("the visible bottom row %d forwarded=%v, want %v (content rows %d..%d)",
						bottom, forwarded, wantBottom, originY, originY+rows-1)
				}
			})
		}
	}
}

// TestAttachMouseBoundsClampToASmallerEmulator pins the COLS half of attach's
// mouseBounds call, which the suite left unpinned: replacing
// mouseBounds(cols, rows, paneCols, paneRows) with plain `cols, rows` kept
// ./internal/ui green, because every existing attach case had an emulator at least
// as wide as the region.
//
// This is not contrived. The emulator is resized only on tea.WindowSizeMsg, so a
// Resize that FAILED — which is the only thing that sets m.status — leaves the
// emulator at its old, smaller size while the box is drawn at the new one. A column
// past the emulator's last must be dropped: the session has no such cell, and
// termpane would be handed a coordinate outside the screen it owns.
func TestAttachMouseBoundsClampToASmallerEmulator(t *testing.T) {
	m, fp := attachFrameModel("live", 80, 24, "")
	regionCols, regionRows := m.paneSize()
	fp.sizeCols, fp.sizeRows = regionCols-5, regionRows-3
	if fp.sizeCols >= regionCols || fp.sizeRows >= regionRows {
		t.Fatalf("precondition: the emulator (%dx%d) must be smaller than the region (%dx%d)",
			fp.sizeCols, fp.sizeRows, regionCols, regionRows)
	}

	originX, originY := findSentinel(render(m), paneSentinel)
	if originX < 0 {
		t.Fatalf("precondition: the pane's content is not in the frame")
	}

	for _, c := range []struct {
		name       string
		absX, absY int
	}{
		{"the first column past the emulator, still inside the box", originX + fp.sizeCols, originY},
		{"the last column the box draws, far past the emulator", originX + regionCols - 1, originY},
		{"the first row past the emulator, still inside the box", originX, originY + fp.sizeRows},
		{"the last row the box draws, past the emulator", originX, originY + regionRows - 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			fp.mice = nil
			attachUpdate(t, m, mouseAt(c.absX, c.absY))
			if len(fp.mice) != 0 {
				t.Fatalf("(%d, %d) was forwarded as %v — the emulator's smaller size was ignored",
					c.absX, c.absY, fp.mice)
			}
		})
	}

	// The cell just inside both must still arrive, or the clamp could be a blanket
	// refusal and this test would not know.
	t.Run("the emulator's own bottom-right cell still arrives", func(t *testing.T) {
		fp.mice = nil
		attachUpdate(t, m, mouseAt(originX+fp.sizeCols-1, originY+fp.sizeRows-1))
		if len(fp.mice) != 1 {
			t.Fatalf("recorded %d events, want 1", len(fp.mice))
		}
		if got := fp.mice[0].Mouse(); got.X != fp.sizeCols-1 || got.Y != fp.sizeRows-1 {
			t.Errorf("arrived at (%d, %d), want (%d, %d)",
				got.X, got.Y, fp.sizeCols-1, fp.sizeRows-1)
		}
	})
}

// TestMouseIsDroppedWhileTheTerminalOwnsIt pins the mouseCapture routing guard in
// both front ends. FIX-1 deliberately left it out, on the argument that turning
// mouse reporting off stops events arriving — and that argument is wrong about
// the ORDERING.
//
// Under v1 the ordering hazard was tea.DisableMouse being a Cmd, executed after
// the Update that returned it. v2 does not remove the hazard, it moves it one
// step later: the mode now lives on the frame (View().MouseMode), so the DEC
// reset is not written until the NEXT render, and bubbletea coalesces renders at
// frame rate. Every event the input reader had already decoded is still queued
// ahead of that write. Those events reach Update with mouseCapture already
// false, and without this guard they are forwarded to the session — after the
// user asked for their mouse back. model.go's own field doc is the contract
// being enforced: "the two must not be allowed to differ".
func TestMouseIsDroppedWhileTheTerminalOwnsIt(t *testing.T) {
	t.Run("dashboard", func(t *testing.T) {
		m, fp := frameModel("live", 80, 24, true)
		originX, originY := findSentinel(render(m), paneSentinel)
		if originX < 0 {
			t.Fatalf("precondition: the pane's content is not in the frame")
		}
		// Released through the real chord, not by setting the field: that is what
		// makes this the state a user can actually be in.
		m, _ = update(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
		if m.mouseCapture {
			t.Fatalf("precondition: ctrl+g must release capture")
		}
		if got := m.View().MouseMode; got != tea.MouseModeNone {
			t.Fatalf("precondition: the chord must leave View declaring MouseMode %v, got %v",
				tea.MouseModeNone, got)
		}

		update(t, m, mouseAt(originX, originY))
		update(t, m, mouseAt(originX+3, originY+2))
		if len(fp.mice) != 0 {
			t.Errorf("forwarded %v while the mouse belonged to the terminal", fp.mice)
		}

		// And taking it back restores forwarding, so the guard is a gate and not a
		// one-way door.
		m, _ = update(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
		if !m.mouseCapture {
			t.Fatalf("the second chord must retake capture")
		}
		update(t, m, mouseAt(originX, originY))
		if len(fp.mice) != 1 {
			t.Errorf("recorded %d events after retaking the mouse, want 1", len(fp.mice))
		}
	})

	t.Run("attach", func(t *testing.T) {
		m, fp := attachFrameModel("live", 80, 24, "")
		originX, originY := findSentinel(render(m), paneSentinel)
		if originX < 0 {
			t.Fatalf("precondition: the pane's content is not in the frame")
		}
		m, _ = attachUpdate(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
		if m.mouseCapture {
			t.Fatalf("precondition: ctrl+g must release capture")
		}
		if got := m.View().MouseMode; got != tea.MouseModeNone {
			t.Fatalf("precondition: the chord must leave View declaring MouseMode %v, got %v",
				tea.MouseModeNone, got)
		}

		attachUpdate(t, m, mouseAt(originX, originY))
		attachUpdate(t, m, mouseAt(originX+3, originY+2))
		if len(fp.mice) != 0 {
			t.Errorf("forwarded %v while the mouse belonged to the terminal", fp.mice)
		}

		m, _ = attachUpdate(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
		if !m.mouseCapture {
			t.Fatalf("the second chord must retake capture")
		}
		attachUpdate(t, m, mouseAt(originX, originY))
		if len(fp.mice) != 1 {
			t.Errorf("recorded %d events after retaking the mouse, want 1", len(fp.mice))
		}
	})
}

// TestAttachMouseIsDroppedWhileNoPaneIsPainted is attach's counterpart to the
// dashboard's unpainted-frame guard, and it has one case the dashboard does not:
// m.ended, set when the connection closes on its own. View goes blank there
// deliberately, so the alt-screen restore does not flash a dead session's final
// frame — and a mouse event still queued behind that message must not be
// delivered to a pane whose host has gone.
//
// paneSize floors both axes at a positive number, so the arithmetic answers even
// at width 0: the origin is still (2, 2) over a 1x3 region.
func TestAttachMouseIsDroppedWhileNoPaneIsPainted(t *testing.T) {
	cases := []struct {
		name  string
		apply func(m *attachModel)
	}{
		{"before the first WindowSizeMsg", func(m *attachModel) {
			m.ready = false
			m.width, m.height = 0, 0
		}},
		{"ready but zero width", func(m *attachModel) { m.width = 0 }},
		{"ready but zero height", func(m *attachModel) { m.height = 0 }},
		{"the connection ended", func(m *attachModel) { m.ended = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, fp := attachFrameModel("live", 80, 24, "")
			c.apply(&m)
			if col, _ := findSentinel(render(m), paneSentinel); col >= 0 {
				t.Fatalf("precondition: View paints the pane in this state (column %d)", col)
			}
			cols, _ := m.paneSize()
			originX, originY := paneContentOrigin(0, 0, m.titleBlock(cols))
			for _, at := range []tea.MouseMsg{mouseAt(originX, originY), mouseAt(2, 2), mouseAt(0, 0)} {
				attachUpdate(t, m, at)
			}
			if len(fp.mice) != 0 {
				t.Errorf("forwarded %v while the frame shows no pane (its computed origin is (%d, %d))",
					fp.mice, originX, originY)
			}
		})
	}
}

// TestAttachMouseWithNoPaneIsANoOp is the nil-handle case: paneExitedMsg's
// handler leaves the program quitting, and an event decoded before it was
// dispatched still arrives. No forward, no panic.
func TestAttachMouseWithNoPaneIsANoOp(t *testing.T) {
	m := attachModel{name: "live", mouseCapture: true, width: 80, height: 24, ready: true}
	if _, cmd := attachUpdate(t, m, mouseAt(2, 2)); cmd != nil {
		t.Errorf("a mouse event with no pane must return no Cmd")
	}
}

// TestAttachMouseGeometryIsTheSameUnderEitherCaptureHint is attach's
// focus-independence equivalent. attachModel has no focus concept at all — every
// key already belongs to the session — so the state that could plausibly move the
// geometry is the one that changes the TITLE: releasing the mouse swaps a 15-cell
// hint for a 36-cell one, and a title whose wrap changed would push the origin down
// a row. The claim is that the rendered content origin and the computed one agree
// under BOTH hints.
//
// The forwarding half is asserted under capture only, and that is a correction to
// this test's original form, which forwarded under both. It argued a routing guard
// on mouseCapture would be dead code because releasing the mouse stops the
// terminal reporting. That is wrong about the ordering — the release only
// reaches the terminal on the next rendered frame, so events already decoded are
// queued ahead of it — and the guard is real:
// TestMouseIsDroppedWhileTheTerminalOwnsIt pins it. What this test is FOR is the
// title's effect on the origin, which needs no forward to assert; the released
// branch therefore asserts geometry plus the drop, and the geometry claim is
// unchanged and still made at both hints.
func TestAttachMouseGeometryIsTheSameUnderEitherCaptureHint(t *testing.T) {
	longName := strings.Repeat("s", 33)
	for _, capture := range []bool{true, false} {
		t.Run(fmt.Sprintf("capture=%v", capture), func(t *testing.T) {
			m, fp := attachFrameModel(longName, 80, 24, "")
			m.mouseCapture = capture

			frame := render(m)
			originX, originY := findSentinel(frame, paneSentinel)
			if originX != 2 || originY != 2 {
				t.Fatalf("the frame puts the content at (%d, %d), want (2, 2)", originX, originY)
			}
			if x, y := attachOrigin(m); x != originX || y != originY {
				t.Errorf("the mouse path computes (%d, %d) but the frame draws the content at "+
					"(%d, %d) — the %d-cell hint moved the origin", x, y, originX, originY,
					lipgloss.Width(m.titleBlock(78)))
			}
			wantHint := attachDetachHint
			if !capture {
				wantHint = attachMouseReleasedHint
			}
			cols, _ := m.paneSize()
			if got := xansi.Strip(m.titleBlock(cols)); !strings.Contains(got, wantHint) {
				t.Errorf("the title is %q, want it to name %q — the title is the only place this "+
					"program can report where the mouse is going", got, wantHint)
			}

			msg := tea.MouseWheelMsg{
				X: originX + 7, Y: originY + 3,
				Button: tea.MouseWheelUp,
			}
			attachUpdate(t, m, msg)
			if !capture {
				if len(fp.mice) != 0 {
					t.Fatalf("forwarded %v while the mouse belonged to the terminal", fp.mice)
				}
				return
			}
			if len(fp.mice) != 1 {
				t.Fatalf("recorded %d mouse events, want 1", len(fp.mice))
			}
			if _, isWheel := fp.mice[0].(termpane.MouseWheel); !isWheel {
				t.Errorf("delivered %T, want termpane.MouseWheel", fp.mice[0])
			}
			if got := fp.mice[0].Mouse(); got.X != 7 || got.Y != 3 {
				t.Errorf("arrived at (%d, %d), want (7, 3)", got.X, got.Y)
			}
		})
	}
}

// TestAttachMouseChordTogglesRealCapture is the toggle contract on attach's side.
// The DECLARED mouse mode is what produces the actual DEC sequence: without it
// the host terminal stays in mouse-reporting mode and native click-drag
// selection — the entire reason the chord exists — stays broken however the flag
// reads. v1 carried that declaration in a Cmd and v2 carries it on the frame, so
// the assertion reads View().MouseMode instead of the returned Cmd; the
// guarantee is unchanged.
//
// The chord must also never reach the session. ctrl+g is BEL, and a session that
// received it would see an abort it was never sent.
func TestAttachMouseChordTogglesRealCapture(t *testing.T) {
	chord := tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}
	m, fp := attachFrameModel("live", 80, 24, "")
	if !m.mouseCapture {
		t.Fatalf("precondition: capture starts on")
	}

	m, _ = attachUpdate(t, m, chord)
	if m.mouseCapture {
		t.Errorf("the first chord should release capture")
	}
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("after release View declares MouseMode %v, want tea.MouseModeNone (%v)",
			got, tea.MouseModeNone)
	}
	if len(fp.keys) != 0 || len(fp.texts) != 0 {
		t.Errorf("the chord leaked to the session (keys=%v texts=%v)", fp.keys, fp.texts)
	}

	m, _ = attachUpdate(t, m, chord)
	if !m.mouseCapture {
		t.Errorf("the second chord should take capture back")
	}
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("after re-capture View declares MouseMode %v, want tea.MouseModeCellMotion (%v)",
			got, tea.MouseModeCellMotion)
	}
	if len(fp.keys) != 0 || len(fp.texts) != 0 {
		t.Errorf("the second chord leaked to the session (keys=%v texts=%v)", fp.keys, fp.texts)
	}
}

// TestAttachDetachChordStillQuits guards the chord that was already reserved:
// adding a second interception must not have shadowed ctrl+\, which is the only
// way out of an attached session.
func TestAttachDetachChordStillQuits(t *testing.T) {
	m, fp := attachFrameModel("live", 80, 24, "")
	_, cmd := attachUpdate(t, m, tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatalf("ctrl+\\ must return tea.Quit")
	}
	if len(fp.keys) != 0 {
		t.Errorf("the detach chord leaked to the session as %v", fp.keys)
	}
}

// TestAttachOrdinaryKeyStillReachesTheSession is the other half of the same
// guard: exactly two chords are reserved, and everything else — Ctrl+C
// included — belongs to the session.
func TestAttachOrdinaryKeyStillReachesTheSession(t *testing.T) {
	m, fp := attachFrameModel("live", 80, 24, "")
	for _, k := range []tea.KeyMsg{keyRune('g'), tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: tea.KeyEnter}} {
		attachUpdate(t, m, k)
	}
	if len(fp.keys)+len(fp.texts) != 3 {
		t.Errorf("the session received %d keys and %d texts, want 3 in total",
			len(fp.keys), len(fp.texts))
	}
}

// TestMouseCaptureStateIsVisibleInTheRenderedFrame is the help-line assertion at
// the level that matters: the state must be legible in the frame View() actually
// produced at 80 columns, not merely present in the string renderHelp() returned.
// The two are different claims, because renderFooter truncates, and the focused
// help line is where the hint lives — so a hint that only appeared above 120
// columns would indicate nothing on the terminal people actually run.
//
// Reading the frame rather than the helper is also what makes this robust to
// where the footer sits: the ANSI strip removes the styling, so the assertion is
// about cells the user can read.
//
// Only focused=true is asserted for the hint, because only the focused branch
// carries it. The 24-row check below runs for BOTH focus states anyway: it is a
// geometry claim, not a hint claim, and the unfocused frame renders a different
// main region (renderDetail or the pane, per shouldRenderPane) whose row count is
// worth pinning independently.
func TestMouseCaptureStateIsVisibleInTheRenderedFrame(t *testing.T) {
	for _, focused := range []bool{false, true} {
		for _, capture := range []bool{true, false} {
			t.Run(fmt.Sprintf("focused=%v capture=%v", focused, capture), func(t *testing.T) {
				m, _ := frameModel("live", 80, 24, focused)
				m.mouseCapture = capture
				want := m.mouseCaptureHint()

				frame := fit(render(m), 80)
				if focused {
					if !strings.Contains(frame, want) {
						t.Errorf("the 80-column frame does not contain %q:\n%s", want, frame)
					}
					// The same claim about the help line in isolation, stripped and cut
					// exactly as renderFooter cuts it, so a failure names which of the
					// two broke.
					if shown := xansi.Strip(truncate(m.renderHelp(), 80)); !strings.Contains(shown, want) {
						t.Errorf("the help line at 80 columns is %q, want it to contain %q", shown, want)
					}
				} else if strings.Contains(frame, "ctrl+g") {
					// The unfocused line must stay as main had it. Asserted as the
					// absence of the CHORD rather than of the hint string, so an
					// abbreviated hint could not sneak the item back onto this line.
					t.Errorf("the unfocused 80-column frame advertises ctrl+g:\n%s", frame)
				}
				// The frame must not GROW rows as it is fitted to the terminal, or
				// the body would shrink and every forwarded mouse event would land a
				// row off. Bubble Tea's renderer discards the cells past the window
				// width rather than wrapping them to the next row (ultraviolet
				// styled.go:164-171, reached from cursed_renderer.go:345), so the row
				// count is preserved by construction — this asserts the frame it is
				// handed already has one row per terminal row, which is the premise
				// layoutDims' height arithmetic rests on.
				if h := lipgloss.Height(frame); h != 24 {
					t.Errorf("the frame is %d rows for a 24-row terminal", h)
				}
			})
		}
	}

	// The two states must be distinguishable in the frame, or the indicator is
	// decorative. Compared as whole frames because that is the level the user sees.
	on, _ := frameModel("live", 80, 24, true)
	on.mouseCapture = true
	off, _ := frameModel("live", 80, 24, true)
	off.mouseCapture = false
	if fit(render(on), 80) == fit(render(off), 80) {
		t.Errorf("the captured and released frames are identical at 80 columns")
	}
}

// TestDashboardMouseChordDoesNotDisturbTheGeometry is the toggle's frame-level
// consequence: ctrl+g changes the help line's text (and, while focused, its
// length), and the help line is inside the footer whose height layoutDims
// subtracts. If a toggled hint ever wrapped the footer onto an extra row, the
// body would shrink and every forwarded mouse event would land a row off — a
// coupling that is invisible in the flag and obvious in the frame.
//
// The geometry claim is asserted after EACH press, released and retaken, since
// either hint could be the one that wraps. The routing claim is asserted after the
// second press, which is where the session owns the mouse again — after the first,
// the mouse belongs to the terminal and the correct behaviour is to drop
// (TestMouseIsDroppedWhileTheTerminalOwnsIt). Both of this test's original claims
// are made; only the state each is made in is now right.
func TestDashboardMouseChordDoesNotDisturbTheGeometry(t *testing.T) {
	chord := tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}
	for _, focused := range []bool{false, true} {
		t.Run(fmt.Sprintf("focused=%v", focused), func(t *testing.T) {
			m, fp := frameModel("live", 80, 24, focused)
			beforeX, beforeY := findSentinel(render(m), paneSentinel)

			for press := 1; press <= 2; press++ {
				m, _ = update(t, m, chord)
				// The chord writes a status whose toast floats over the pane's
				// first rows (toast.go); age it out so the sentinel search reads
				// the pane. The claim here is geometry, not the toast.
				m.statusChangedAt = time.Time{}
				// The press must actually reconfigure the terminal, not just flip a
				// routing flag; under v2 that is the mode the frame declares rather
				// than a returned Cmd. Press 1 releases the mouse to the terminal,
				// press 2 takes it back.
				wantMode := tea.MouseModeNone
				if press == 2 {
					wantMode = tea.MouseModeCellMotion
				}
				if got := m.View().MouseMode; got != wantMode {
					t.Fatalf("press %d: View declares MouseMode %v, want %v", press, got, wantMode)
				}
				afterX, afterY := findSentinel(render(m), paneSentinel)
				if afterX != beforeX || afterY != beforeY {
					t.Fatalf("press %d moved the pane's content from (%d, %d) to (%d, %d)",
						press, beforeX, beforeY, afterX, afterY)
				}
				if x, y := dashboardOrigin(m); x != afterX || y != afterY {
					t.Errorf("after press %d the mouse path computes (%d, %d) but the frame renders (%d, %d)",
						press, x, y, afterX, afterY)
				}

				update(t, m, mouseAt(afterX, afterY))
				if press == 1 {
					if len(fp.mice) != 0 {
						t.Fatalf("forwarded %v while the mouse belonged to the terminal", fp.mice)
					}
					continue
				}
				if len(fp.mice) != 1 {
					t.Fatalf("recorded %d mouse events after the mouse was retaken, want 1", len(fp.mice))
				}
				if got := fp.mice[0].Mouse(); got.X != 0 || got.Y != 0 {
					t.Errorf("the origin cell arrived at (%d, %d), want (0, 0)", got.X, got.Y)
				}
			}
		})
	}
}

// helpLineAt80 returns the dashboard's help row as the user reads it on an
// 80-column terminal: the LAST line of the fitted frame, which is where
// renderFooter puts renderHelp's output.
//
// It reads the frame rather than renderHelp() for the reason fit() exists: the
// unfocused help line is 105 cells with a pane open (90 with none) and the cut
// that makes it 80 belongs to Bubble Tea's renderer, so the string renderHelp
// returns says nothing about legibility.
func helpLineAt80(m Model) string {
	lines := strings.Split(fit(render(m), 80), "\n")
	return lines[len(lines)-1]
}

// TestHelpLineAt80KeepsTheDiscoverableKeysAndTheCaptureState is the 80-column
// budget assertion, split across the two lines that now carry the two claims: the
// keys a user discovers by trying them stay legible on the UNFOCUSED line, and the
// capture state is legible on the FOCUSED one.
//
// Both halves are asserted together on purpose, in one test, because they are one
// tradeoff. The unfocused line is oversubscribed even before help.Model's
// ellipsis truncation runs (renderNormalHelpKeyMap's curated Short set alone
// measures past 80 with a pane open), so each item's visibility is bought with
// another's; putting the mouse-capture hint there would have cost one of these
// keys, and putting it on the focused line (W6.T2's renderPaneHelpKeyMap) is
// what buys them back. A test that only checked the focused line would pass
// for an arrangement that had silently spent the arrows anyway.
//
// The wants are built from the keymap rather than written out, so a relabelled
// binding cannot leave this test asserting a string the dashboard no longer
// draws. Only the leading items are asserted (up/down/focus/toggle) —
// renderNormalHelpKeyMap orders an open pane's Focus/Toggle right after
// movement (ahead of close-pane/new/clean), on the reasoning that a
// key that only matters in the CURRENT state should outrank the
// always-available ones once a width budget forces a choice — comfortably
// inside the 80-column budget regardless of how much of the rest of the line
// an ellipsis eventually eats.
func TestHelpLineAt80KeepsTheDiscoverableKeysAndTheCaptureState(t *testing.T) {
	for _, capture := range []bool{true, false} {
		t.Run(fmt.Sprintf("capture=%v", capture), func(t *testing.T) {
			m, _ := frameModel("live", 80, 24, false)
			m.mouseCapture = capture

			shown := helpLineAt80(m)
			for _, want := range []string{
				m.keys.Up.Help().Key + " " + m.keys.Up.Help().Desc,
				m.keys.Down.Help().Key + " " + m.keys.Down.Help().Desc,
				m.keys.Focus.Help().Key + " " + m.keys.Focus.Help().Desc,
				m.keys.Toggle.Help().Key + " " + m.keys.Toggle.Help().Desc,
			} {
				if !strings.Contains(shown, want) {
					t.Errorf("the unfocused 80-column help line is %q, want it to contain %q",
						shown, want)
				}
			}

			focused, _ := frameModel("live", 80, 24, true)
			focused.mouseCapture = capture
			if got := helpLineAt80(focused); !strings.Contains(got, focused.mouseCaptureHint()) {
				t.Errorf("the focused 80-column help line is %q, want it to contain %q",
					got, focused.mouseCaptureHint())
			}
		})
	}
}

// TestHelpLineAt80DisplacesNothing pins the PRICE of the capture hint on the
// unfocused line, and the price is zero: the hint lives only on the focused
// line (renderPaneHelpKeyMap), so the unfocused line — oversubscribed on its
// own, per TestHelpLineAt80KeepsTheDiscoverableKeysAndTheCaptureState's doc —
// never mentions ctrl+g at all, at any width.
//
// Asserted against the chord rather than a hint string so an abbreviated
// variant could not slip in under a different spelling.
func TestHelpLineAt80DisplacesNothing(t *testing.T) {
	m, _ := frameModel("live", 80, 24, false)

	shown := helpLineAt80(m)
	if strings.Contains(shown, "ctrl+g") {
		t.Errorf("the unfocused 80-column line %q advertises ctrl+g", shown)
	}
}

// TestMouseChordIsReachableFromEveryStateThatOwnsInput is the fix for the chord
// being unreachable in the three states that take input ahead of the global
// keymap: the two modal modes and the creation form, all three of which outrank
// normal mode in modePrecedence and would otherwise never see the chord.
//
// The spawn-error modal is the case that motivates the whole fix: its entire
// content is a failed create's captured output, which is exactly the text a user
// wants to select — and they cannot while the session owns the mouse.
//
// Each case asserts three things, because a toggle that worked but disturbed the
// state would be a worse bug than the one being fixed: the flag flips, the Cmd is
// the REAL DEC-sequence command (a routing-only flag would leave the terminal in
// mouse-reporting mode and change nothing observable), and the state the chord was
// pressed in is untouched afterwards.
func TestMouseChordIsReachableFromEveryStateThatOwnsInput(t *testing.T) {
	chord := tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}

	cases := []struct {
		name string
		// setUp puts the Model into the state under test.
		setUp func(t *testing.T) Model
		// check asserts the state survived the chord unchanged.
		check func(t *testing.T, m Model)
	}{
		{
			name: "clean confirmation",
			setUp: func(t *testing.T) Model {
				s := sess("active-session")
				s.Phase = state.PhaseExecuting
				m := testModel(s)
				m, _ = update(t, m, keyRune('c'))
				if m.modal != inputConfirmClean {
					t.Fatalf("setup: expected the confirm modal, got %v", m.modal)
				}
				return m
			},
			check: func(t *testing.T, m Model) {
				if m.modal != inputConfirmClean {
					t.Errorf("the chord left confirm mode as %v — the modal must stay up", m.modal)
				}
				if m.confirmTarget != "active-session" {
					t.Errorf("the chord cleared confirmTarget (%q) — the modal lost what it was confirming",
						m.confirmTarget)
				}
				if m.confirmPhase != state.PhaseExecuting {
					t.Errorf("the chord changed confirmPhase to %q", m.confirmPhase)
				}
			},
		},
		{
			name: "spawn-error modal",
			setUp: func(t *testing.T) Model {
				m := testModel()
				m, _ = update(t, m, spawnFinishedMsg{
					name: "alpha", err: errors.New("boom"), output: "why it failed\n",
				})
				if m.modal != inputSpawnError {
					t.Fatalf("setup: expected the error modal, got %v", m.modal)
				}
				return m
			},
			check: func(t *testing.T, m Model) {
				if m.modal != inputSpawnError {
					t.Errorf("the chord dismissed the error modal (mode %v) — the output would be lost "+
						"at the moment the user freed the mouse to copy it", m.modal)
				}
				if m.spawnErrHeadline == "" || len(m.spawnErrTail) == 0 {
					t.Errorf("the chord cleared the captured output: headline=%q tail=%v",
						m.spawnErrHeadline, m.spawnErrTail)
				}
			},
		},
		{
			name: "creation form",
			setUp: func(t *testing.T) Model {
				m := testModel(sess("a"))
				m, _ = update(t, m, keyRune('n'))
				m = typeRunes(t, m, "my-session")
				if !m.formOpen || m.form.nameInput.Value() != "my-session" {
					t.Fatalf("setup: form active=%v name=%q", m.formOpen, m.form.nameInput.Value())
				}
				return m
			},
			check: func(t *testing.T, m Model) {
				if !m.formOpen {
					t.Errorf("the chord closed the creation form")
				}
				// The chord must not reach the name field as text. This is the
				// assertion that fails if the rule is dropped from inputForm's spec:
				// the mode falls through to formComponentKey, which for focusIndex 0
				// hands the key to the name input.
				if got := m.form.nameInput.Value(); got != "my-session" {
					t.Errorf("the name buffer is %q, want %q — the chord was typed into the form",
						got, "my-session")
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, wantCapture := range []bool{false, true} {
				m := c.setUp(t)
				m.mouseCapture = !wantCapture

				m, _ = update(t, m, chord)
				if m.mouseCapture != wantCapture {
					t.Fatalf("the chord left mouseCapture=%v, want %v", m.mouseCapture, wantCapture)
				}
				// The frame the chord produces is what reconfigures the terminal, so
				// this asserts the mode the NEXT render will declare. A flag that
				// flipped without the declaration following it would leave the
				// terminal in the wrong mouse mode with mouseCapture claiming
				// otherwise — the same failure the v1 Cmd assertion caught.
				want := tea.MouseModeNone
				if wantCapture {
					want = tea.MouseModeCellMotion
				}
				if got := m.View().MouseMode; got != want {
					t.Errorf("the chord left View declaring MouseMode %v, want %v", got, want)
				}
				if m.quitting {
					t.Errorf("the chord quit the dashboard")
				}
				c.check(t, m)
			}
		})
	}
}

// TestMouseChordDoesNotDisturbTheModalHandlersOwnKeys is the other half of the
// interception: adding a key to three handlers must not have shadowed the keys
// those handlers already owned. Each of these was working before the chord existed
// and is asserted here because the intercept sits AHEAD of all of them.
func TestMouseChordDoesNotDisturbTheModalHandlersOwnKeys(t *testing.T) {
	confirm := func(t *testing.T) Model {
		s := sess("active-session")
		s.Phase = state.PhaseExecuting
		m := testModel(s)
		m, _ = update(t, m, keyRune('c'))
		return m
	}
	spawnErr := func(t *testing.T) Model {
		m := testModel()
		m, _ = update(t, m, spawnFinishedMsg{name: "alpha", err: errors.New("boom"), output: "why\n"})
		return m
	}

	t.Run("confirm y still cleans", func(t *testing.T) {
		got, cmd := update(t, confirm(t), keyRune('y'))
		if got.modal != inputNormal || cmd == nil {
			t.Errorf("y should confirm the clean: mode=%v cmd=%v", got.modal, cmd != nil)
		}
	})
	t.Run("confirm n still cancels", func(t *testing.T) {
		got, cmd := update(t, confirm(t), keyRune('n'))
		if got.modal != inputNormal || cmd != nil {
			t.Errorf("n should cancel: mode=%v cmd=%v", got.modal, cmd != nil)
		}
	})
	t.Run("confirm esc still cancels", func(t *testing.T) {
		got, _ := update(t, confirm(t), tea.KeyPressMsg{Code: tea.KeyEsc})
		if got.modal != inputNormal || got.confirmTarget != "" {
			t.Errorf("esc should cancel and clear: mode=%v target=%q", got.modal, got.confirmTarget)
		}
	})
	t.Run("confirm ctrl+c still quits", func(t *testing.T) {
		got, cmd := update(t, confirm(t), tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if !got.quitting || cmd == nil {
			t.Errorf("ctrl+c must remain the escape hatch: quitting=%v cmd=%v", got.quitting, cmd != nil)
		}
	})
	t.Run("spawn error enter still dismisses", func(t *testing.T) {
		got, _ := update(t, spawnErr(t), keyEnter)
		if got.modal != inputNormal {
			t.Errorf("enter should dismiss the error modal, got mode %v", got.modal)
		}
	})
	t.Run("spawn error ctrl+c still quits", func(t *testing.T) {
		got, cmd := update(t, spawnErr(t), tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if !got.quitting || cmd == nil {
			t.Errorf("ctrl+c must remain the escape hatch: quitting=%v cmd=%v", got.quitting, cmd != nil)
		}
	})
	t.Run("form esc is still the only exit", func(t *testing.T) {
		m := testModel(sess("a"))
		m, _ = update(t, m, keyRune('n'))
		got, _ := update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if got.formOpen {
			t.Errorf("esc should close the form")
		}
	})
	t.Run("form still types an ordinary rune", func(t *testing.T) {
		m := testModel(sess("a"))
		m, _ = update(t, m, keyRune('n'))
		m = typeRunes(t, m, "abc")
		if got := m.form.nameInput.Value(); got != "abc" {
			t.Errorf("the name buffer is %q, want %q", got, "abc")
		}
	})
}

// TestMouseChordIsDroppedFromTheModalsRenderedFrame is the frame-level companion
// to the reachability test: releasing the mouse from a modal must not change what
// the modal draws. The dialog floats over the frame (modal.go) and the status
// line under it stays visible, so the toggle's own report ("mouse released…")
// IS on screen now — which is why this asserts the dialog is unchanged and the
// status line reports the release, rather than the whole frame being identical.
func TestMouseChordIsDroppedFromTheModalsRenderedFrame(t *testing.T) {
	m, _ := frameModel("live", 80, 24, false)
	m.modal = inputSpawnError
	m.spawnErrName = "alpha"
	m.spawnErrHeadline = spawnFailedHeadline("alpha", errors.New("exit status 1"))
	m.spawnErrTail = []string{"fatal: could not read from remote"}

	before := m.renderSpawnErrorBar()
	after, _ := update(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if got := after.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("the chord must leave View declaring MouseMode %v from the error modal, got %v",
			tea.MouseModeNone, got)
	}
	if got := after.renderSpawnErrorBar(); got != before {
		t.Errorf("releasing the mouse changed the dialog.\nbefore:\n%s\nafter:\n%s", before, got)
	}
	if frame := render(after); !strings.Contains(frame, "mouse released") {
		t.Errorf("the status line under the dialog should report the release:\n%s", frame)
	}
	if after.mouseCapture {
		t.Errorf("the chord did not release capture")
	}
}

// TestMouseBoundsMinimumIsAppliedByBothCallers is the caller-level counterpart to
// TestMouseBoundsTakesPerAxisMinimum, which proves only that the helper returns a
// minimum — not that either front end passes it both numbers.
//
// The setup is the real disagreement, not a contrived one: the emulator is
// resized only on tea.WindowSizeMsg, so any layout change that is not a terminal
// resize leaves the emulator LARGER than the region it is drawn into. At an 80x24
// terminal the dashboard's region is 46x18 against an 80x24 emulator, so a click
// 46 columns right of the origin, or 18 rows below it, is inside the emulator and
// outside the box — and delivering it would put a click on the dashboard's help
// line onto the session's row 18.
//
// The frame is what settles which of the two is right: these cells are outside
// the box View() drew, so they must be dropped however plausible the emulator's
// own size makes them look.
func TestMouseBoundsMinimumIsAppliedByBothCallers(t *testing.T) {
	t.Run("dashboard", func(t *testing.T) {
		fp := newFakePane(paneSentinel)
		m := testModel(runningSess("live"))
		m.pane = fp
		m.paneName = "live"
		m.paneFocused = true
		m.width, m.height = 80, 24
		// Left at newFakePane's 80x24 default on purpose: that IS the disagreement.
		_, mainOuter, bodyHeight := m.layoutDims()
		regionCols, regionRows := paneContentDims(mainOuter, bodyHeight)
		paneCols, paneRows := fp.Size()
		if paneCols <= regionCols || paneRows <= regionRows {
			t.Fatalf("precondition: the emulator (%dx%d) must be larger than the region (%dx%d) "+
				"for this test to exercise the minimum at all", paneCols, paneRows, regionCols, regionRows)
		}

		originX, originY := findSentinel(render(m), paneSentinel)
		if originX < 0 {
			t.Fatalf("precondition: the pane's content is not in the frame")
		}
		for _, c := range []struct {
			name       string
			absX, absY int
		}{
			{"the first column past the region, still inside the emulator", originX + regionCols, originY},
			{"the last column the emulator has, far outside the region", originX + paneCols - 1, originY},
			{"the first row past the region, still inside the emulator", originX, originY + regionRows},
			{"the help line, inside the emulator's 24 rows", originX, originY + regionRows + 2},
		} {
			update(t, m, mouseAt(c.absX, c.absY))
			if len(fp.mice) != 0 {
				t.Fatalf("%s: (%d, %d) was forwarded as %v — the region's size was ignored",
					c.name, c.absX, c.absY, fp.mice)
			}
		}
	})

	t.Run("attach", func(t *testing.T) {
		// Attach's own version of the same disagreement: paneOuterHeight shrinks by
		// a row once m.status is set, and the only thing that sets it is a FAILED
		// Resize — so the region and the emulator diverge exactly when something has
		// already gone wrong.
		m, fp := attachFrameModel("live", 80, 24, "")
		regionCols, regionRows := m.paneSize()
		m.status = "pane resize failed: connection reset"
		shrunkCols, shrunkRows := m.paneSize()
		if shrunkRows >= regionRows {
			t.Fatalf("precondition: the status line should shrink the region from %d rows, got %d",
				regionRows, shrunkRows)
		}
		if paneCols, paneRows := fp.Size(); paneCols != regionCols || paneRows != regionRows {
			t.Fatalf("precondition: the emulator is %dx%d, want the pre-status region %dx%d",
				paneCols, paneRows, regionCols, regionRows)
		}

		originX, originY := findSentinel(render(m), paneSentinel)
		if originX < 0 {
			t.Fatalf("precondition: the pane's content is not in the frame")
		}
		// The last row the emulator still has, which the box no longer draws.
		attachUpdate(t, m, mouseAt(originX+shrunkCols-1, originY+shrunkRows))
		if len(fp.mice) != 0 {
			t.Fatalf("the row past the shrunken region was forwarded as %v — "+
				"the emulator's stale size was trusted", fp.mice)
		}
		// The row before it is still inside both and must survive.
		attachUpdate(t, m, mouseAt(originX, originY+shrunkRows-1))
		if len(fp.mice) != 1 {
			t.Fatalf("the region's last row recorded %d events, want 1", len(fp.mice))
		}
	})
}

// quitImmediately ends the program from Init so a real tea.Program can be run
// headless: the modes are applied and their escape sequences written before it
// exits.
//
// It EMBEDS attachModel rather than stubbing View, because under Bubble Tea v2
// the alt screen and the mouse mode are declared by View (attachpane.go) instead
// of by a program option list. A stub View would make the assertion below a
// statement about this type. Init and Update are overridden so the program quits
// on its first tick; View is inherited, so what gets exercised is attach's own.
type quitImmediately struct{ attachModel }

func (q quitImmediately) Init() tea.Cmd                       { return tea.Quit }
func (q quitImmediately) Update(tea.Msg) (tea.Model, tea.Cmd) { return q, tea.Quit }

// TestAttachProgramOptionsAskTheTerminalForMouseReports is the attach-side twin
// of dash_test.go's version, and it is needed for the same reason: every other
// mouse test in this file hands tea.MouseMsg straight to Update, so none of them
// can tell whether the program ever asked the terminal to report mouse events.
// Dropping MouseModeCellMotion from View leaves the suite green while ctrl+g
// toggles a flag over a terminal that reports nothing.
//
// It matters more here than in the dashboard: AttachPane starts the model with
// mouseCapture already true, so a missing option would make the title advertise
// captured state from the first frame, with no wheel event ever arriving.
//
// It goes through newAttachProgram, the constructor AttachPane itself calls,
// rather than reading attachModel.View().MouseMode directly — a constructor that
// never ran the model would satisfy a field assertion and still ship the same
// inert feature.
//
// Asserted on the BYTES written rather than on the tea.View's fields, because
// those fields are a request: only the wire shows the program acted on them.
func TestAttachProgramOptionsAskTheTerminalForMouseReports(t *testing.T) {
	var out bytes.Buffer
	// mouseCapture mirrors AttachPane's own initial value (attachpane.go:41):
	// attach starts with the session owning the mouse.
	p := newAttachProgram(quitImmediately{attachModel{mouseCapture: true}},
		tea.WithInput(strings.NewReader("")),
		tea.WithOutput(&out),
		tea.WithoutSignalHandler(),
	)
	if _, err := p.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, want := range []struct{ seq, why string }{
		{"\x1b[?1002h", "cell-motion mouse reporting (View.MouseMode)"},
		{"\x1b[?1006h", "SGR mouse encoding (implied by MouseModeCellMotion)"},
		{"\x1b[?1049h", "the alternate screen (View.AltScreen)"},
	} {
		if !strings.Contains(out.String(), want.seq) {
			t.Errorf("attach never enabled %s — %q is absent from what it wrote (%q)",
				want.why, want.seq, out.String())
		}
	}
}

// TestMouseCaptureHintStatesAreTheSameWidth pins the equal-width invariant
// mouseCaptureHint's doc comment declares load-bearing. Nothing else did: the
// focused help line is budgeted to the column, and a hint that grew in one state
// would shift every item after it the moment the user pressed ctrl+g — a layout
// jump on a keystroke, in the one state where the line matters most.
//
// The 18 is asserted as well as the equality, because equal-but-longer would
// silently spend the focused line's remaining budget: 44 cells of base text plus
// a separator plus 18 is 65, and the line is measured against 80.
// TestMouseCaptureTextNamesTheRealChord pins the chord STRING users read to
// mouseChordKey, which nothing else did. Every help-line test compares the frame
// against m.mouseCaptureHint() itself, so rewriting both branches to
// "ctrl-x mouse: …" kept the whole package green while the dashboard advertised a
// key that does nothing — the constant is pinned by the toggle tests, the rendered
// text was not.
//
// keys.Mouse's WithHelp is checked here for the same reason and no other: nothing
// renders it (there is no ShortHelp/FullHelp in this package, and renderHelp
// builds the hint from mouseCaptureHint), so it is metadata free to drift from
// the string on screen. Asserting it costs one line and is the only thing
// standing between it and silent staleness. Its seven siblings are unread too;
// this is the one whose key is retargetable in two places at once
// (mouseChordKey here, tea.KeyCtrlG in attachpane.go).
//
// There is no +/- swap any more: the copy wave made every help string in this
// package spell the chord exactly as bubbles/key does, so the displayed text
// IS mouseChordKey and a divergence in either direction is a bug. That is why
// this asserts the constant itself rather than a transformation of it — a
// transformation would have absorbed the very drift being tested for.
func TestMouseCaptureTextNamesTheRealChord(t *testing.T) {
	chord := mouseChordKey

	var m Model
	for _, capture := range []bool{true, false} {
		m.mouseCapture = capture
		if hint := m.mouseCaptureHint(); !strings.Contains(hint, chord) {
			t.Errorf("mouseCaptureHint() = %q with mouseCapture=%v — does not name %q, so the "+
				"dashboard advertises a key that is not the one bound", hint, capture, chord)
		}
	}

	if help := defaultKeyMap().Mouse.Help().Key; help != chord {
		t.Errorf("keys.Mouse's help key is %q, want %q — it has drifted from mouseChordKey (%q)",
			help, chord, mouseChordKey)
	}
}

func TestMouseCaptureHintStatesAreTheSameWidth(t *testing.T) {
	var m Model

	m.mouseCapture = true
	captured := m.mouseCaptureHint()
	m.mouseCapture = false
	released := m.mouseCaptureHint()

	cw, rw := lipgloss.Width(captured), lipgloss.Width(released)
	if cw != rw {
		t.Errorf("the two hint states measure %d (%q) and %d (%q) cells — toggling ctrl+g "+
			"would shift the rest of the focused help line", cw, captured, rw, released)
	}
	if cw != 18 {
		t.Errorf("the hint is %d cells (%q), want 18 — renderHelp's 80-column budget is "+
			"measured against that width", cw, captured)
	}
}

// TestMotionCoalescerEvictsPanesOutsideTheWindow is fix cycle 1's answer to
// the retention finding: last is keyed on paneHandle, an interface value
// backed by a pointer, so an entry that is never deleted pins the whole
// pane → termpane → emulator object graph for the dashboard's lifetime. An
// entry older than mouseMotionCoalesceWindow can no longer change any
// decision (the gate always passes it), so allowMotion evicts everything
// expired each time it admits an event — the map can only ever hold panes
// seen moving within the last window, not every pane the dashboard has ever
// opened.
func TestMotionCoalescerEvictsPanesOutsideTheWindow(t *testing.T) {
	now, advance := fakeMotionClock()
	c := newMotionCoalescer(now)
	a, b := newFakePane("A"), newFakePane("B")

	if !c.allowMotion(a) {
		t.Fatalf("the first motion for a pane must pass")
	}
	advance(mouseMotionCoalesceWindow + time.Millisecond)
	if !c.allowMotion(b) {
		t.Fatalf("motion on a second pane past the window must pass")
	}

	if _, ok := c.last[a]; ok {
		t.Fatalf("pane A's entry is outside the window and decision-inert; keeping it retains A's whole object graph")
	}
	if len(c.last) != 1 {
		t.Fatalf("the map should only hold panes seen inside the window, got %d entries", len(c.last))
	}
}
