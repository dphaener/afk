// mouse.go is the mouse translation layer: it turns a Bubble Tea mouse event
// into the emulator's mouse event, in the pane's own coordinate space. Every
// function here is PURE — no I/O, no rendering, no tea.Cmds.
//
// The purity is a discipline worth keeping, but not for the reason it might
// appear: it does NOT save a repaint. Bubble Tea calls View() after every
// message regardless, so each mouse event costs one full frame whatever these
// helpers do. What the discipline buys is that the mouse path does not render
// the EXPENSIVE things a second time — renderList, renderPane and View are never
// reached from Update, and a drag delivers one event per cell the pointer
// traverses. routeMouse is not render-free either, and honesty about that is the
// point of saying so here: it reaches renderHeader() and renderFooter() once
// each, through Model.mouseFrameLayout, which measures both to get bodyHeight
// and returns the header's height, which IS the box's Y. mouseFrameLayout also
// returns the frame's total height; this path discards it, because an
// overflowing frame loses its BOTTOM rows (cursed_renderer.go:295-351 — under
// AltScreen the frame area is fixed at the terminal's size and cells outside it
// are simply not drawn), so a row's index in the frame is its row on screen and
// the origin needs no correction. Both values are prescribed by the geometry and
// the two renders behind them are the cheapest in the package; anything added to
// this path should be measured against that budget rather than assumed free.
//
// It mirrors forwardKeyToPane's posture (messages.go): the UI classifies the
// event and hands IDENTITY — a cell, a button, modifiers, and which of the four
// event kinds it is — to the emulator, which owns the encoding (X10 vs SGR, the
// motion bit, the release terminator) and the child's-mode gate. Nothing here
// emits an escape byte.
package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/dphaener/afk/internal/termpane"
)

// paneContentOrigin returns the terminal cell where the emulator's screen
// begins, given the pane BOX's top-left corner and the title block that box
// renders above the screen. It is the single source of truth for that origin:
// both front ends call it rather than re-deriving border/padding offsets, so
// there is exactly one place a coordinate error could live.
//
// X adds 2: one border cell plus one padding cell (theme.paneBox /
// theme.paneBoxFocused are both Padding(0, 1) — styles.go:107-114 documents that
// the focused/blurred pair must keep identical geometry, and this is one of the
// two things that depends on it).
//
// Y adds 1 border row plus however many rows the title block occupies.
//
// LOAD-BEARING: lipgloss.Height of a RAW title string is ALWAYS 1, including
// when the title visibly fills two or three rows — the string contains no
// newline, and the box wraps it at render time, after this arithmetic would
// have run. Measured with a 33-character session name, the dashboard's real
// content-origin Y is 3 at 80x24 but 4 at 90x24 and 5 at 60x20, while
// 1+Height(rawTitle) says 3 every time. That is why this function must be
// handed the already-truncated block from paneTitleBlock and never a raw title:
// truncation, not measurement, is what makes the formula true. The
// lipgloss.Height call stays instead of a hardcoded 1 so the function remains
// correct if the block ever legitimately grows a line.
func paneContentOrigin(boxX, boxY int, titleBlock string) (x, y int) {
	return boxX + 2, boxY + 1 + lipgloss.Height(titleBlock)
}

// paneMouseEvent translates a Bubble Tea mouse event into the emulator's, in
// the pane's coordinate space: it subtracts the content origin and rejects
// anything outside the cols x rows screen. Both coordinate systems are 0-based
// (Bubble Tea decodes SGR to 0-based, and ansi.MouseSgr adds 1 back on the way
// out), so the translation is pure subtraction with no ±1 fudge.
//
// An out-of-bounds event returns ok=false and is DROPPED by the caller, never
// clamped. Clamping would deliver a real event at the wrong cell — a click on
// the dashboard's help line arriving as a click on the session's last row is
// worse than no click at all.
//
// KNOWN CONSEQUENCE of that rule, and it is a real one: a drag that STARTS inside
// the pane and is released outside it delivers the press and the motion but no
// release, so the child can be left believing the button is still held. A
// terminal app tracking a selection will keep extending it until the next press.
//
// Clamping the release to the last in-bounds cell is not the remedy, even though
// it looks like one. It would invent an event the user never produced, at a cell
// they never released over, and it would do so for EVERY out-of-region event —
// the function cannot tell "the tail of a drag I saw begin" from "a click on the
// sidebar", because it is pure and holds no gesture state. Trading a stuck button
// for fabricated coordinates on every stray event is the worse trade, and a
// correct fix needs drag state owned by the caller, not a clamp here.
//
// The concrete type is load-bearing, not decoration: termpane.MouseEvent is an
// INTERFACE, and the emulator's SendMouse type-asserts the value to decide
// whether to set the motion bit and whether to use the SGR release terminator.
// So the four kinds are constructed as four different literals rather than one
// payload struct with a mode field. A wheel button wins over the message kind
// (see below); the motion branch is deliberate rather than a hedge, since
// claude sets ?1003 (any-event tracking) and has therefore asked to be told
// about motion.
//
// Under Bubble Tea v2 the parameter is the tea.MouseMsg INTERFACE
// (bubbletea/v2 mouse.go:46-51), not v1's single struct: v2 splits the event
// into tea.MouseClickMsg / MouseReleaseMsg / MouseWheelMsg / MouseMotionMsg,
// all four of which are defined types over tea.Mouse and satisfy that
// interface. The interface is what lets a single `case tea.MouseMsg:` in each
// Update catch all four (v2's own doc comment presents it as the catch-all),
// and msg.Mouse() is how the X/Y/Button/Mod payload is reached — the v1 Action
// field is gone, replaced by the message's own type.
func paneMouseEvent(msg tea.MouseMsg, originX, originY, cols, rows int) (termpane.MouseEvent, bool) {
	m := msg.Mouse()
	x := m.X - originX
	y := m.Y - originY
	if x < 0 || x >= cols || y < 0 || y >= rows {
		return nil, false
	}

	btn := mouseButton(m.Button)
	mod := mouseMods(m)

	// The wheel is recognised by its BUTTON as well as by its message type, and
	// the button test is not redundant belt-and-braces. v1 reported a wheel
	// notch as MouseActionPress carrying a wheel button, so keying off the kind
	// alone sent the child a button click instead of a scroll; v2's decoder
	// happens to classify wheel buttons into MouseWheelMsg first
	// (ultraviolet decoder.go:1609-1617, 1646-1653, `isWheel` at :1703), but
	// that is the DECODER's rule, not the type system's — nothing stops a
	// MouseClickMsg from carrying MouseWheelUp. Keeping the button test makes
	// the direction of the notch, not the sender's classification, decide.
	if _, wheelMsg := msg.(tea.MouseWheelMsg); wheelMsg || isWheelButton(m.Button) {
		return termpane.MouseWheel{X: x, Y: y, Button: btn, Mod: mod}, true
	}

	switch msg.(type) {
	case tea.MouseReleaseMsg:
		return termpane.MouseRelease{X: x, Y: y, Button: btn, Mod: mod}, true
	case tea.MouseMotionMsg:
		return termpane.MouseMotion{X: x, Y: y, Button: btn, Mod: mod}, true
	default: // tea.MouseClickMsg
		return termpane.MouseClick{X: x, Y: y, Button: btn, Mod: mod}, true
	}
}

// isWheelButton reports whether b is one of the four scroll-wheel buttons.
//
// It is spelled out here because there is nowhere to borrow it from: v1's
// tea.MouseEvent.IsWheel() went away with the struct, ultraviolet's own isWheel
// (decoder.go:1703) is unexported, and x/ansi's MouseButton carries no such
// method. The range is the same one both of those use — the X11 codes 4-7 sit
// contiguously between MouseWheelUp and MouseWheelRight — and the constants are
// named rather than numeric so a reorder upstream is a compile error here.
func isWheelButton(b tea.MouseButton) bool {
	return b >= tea.MouseWheelUp && b <= tea.MouseWheelRight
}

// mouseBounds returns the per-axis minimum of the region the pane is DRAWN into
// and the size the emulator actually IS. Neither input is trustworthy alone,
// and they disagree in both directions for the same reason: Resize fires only
// on tea.WindowSizeMsg, so any layout change that is not a terminal resize
// moves the region without telling the emulator.
//
// Region larger than the emulator: the spawn-error modal's taller footer
// shrinks the body (renderSpawnErrorBar in views.go), measured as region 46x16
// against an 80x24 emulator at an 80x24 terminal — trusting the emulator's rows
// there accepts a click on the help line and delivers it to the session at row
// 20. Emulator larger than the region: the same modal being dismissed, before any resize.
// The min is what keeps both from reaching the child.
func mouseBounds(regionCols, regionRows, paneCols, paneRows int) (cols, rows int) {
	cols, rows = regionCols, regionRows
	if paneCols < cols {
		cols = paneCols
	}
	if paneRows < rows {
		rows = paneRows
	}
	return cols, rows
}

// mouseButton maps a Bubble Tea button onto the emulator's. Both enums are
// named after and ordered by the X11 button codes across all twelve values, so
// this switch is total and lossless — and it is written as a switch rather than
// a numeric cast precisely because that correspondence is a coincidence of two
// upstream packages: a reorder in either one must be a compile error here, not
// silent corruption that sends "wheel down" when the user pressed the right
// button. Anything unrecognized becomes termpane.MouseNone, which the emulator
// encodes as the no-button case.
func mouseButton(b tea.MouseButton) termpane.MouseButton {
	switch b {
	case tea.MouseNone:
		return termpane.MouseNone
	case tea.MouseLeft:
		return termpane.MouseLeft
	case tea.MouseMiddle:
		return termpane.MouseMiddle
	case tea.MouseRight:
		return termpane.MouseRight
	case tea.MouseWheelUp:
		return termpane.MouseWheelUp
	case tea.MouseWheelDown:
		return termpane.MouseWheelDown
	case tea.MouseWheelLeft:
		return termpane.MouseWheelLeft
	case tea.MouseWheelRight:
		return termpane.MouseWheelRight
	case tea.MouseBackward:
		return termpane.MouseBackward
	case tea.MouseForward:
		return termpane.MouseForward
	case tea.MouseButton10:
		return termpane.MouseButton10
	case tea.MouseButton11:
		return termpane.MouseButton11
	}
	return termpane.MouseNone
}

// mouseMods folds Bubble Tea's modifier set into the emulator's modifier
// bitmask, the same vt.Mod* values forwardKeyToPane hands to SendKey.
//
// It takes a tea.Mouse rather than a message because v2 moved the modifiers off
// the message and onto that payload, and turned v1's three Shift/Alt/Ctrl
// BOOLEANS into a single tea.KeyMod bitmask.
//
// That bitmask is, today, the SAME type as the emulator's: bubbletea/v2
// mod.go:6-12 and x/vt key.go:11-17 both alias ultraviolet's KeyMod, and Go
// resolves one ultraviolet version for the build, so `vt.KeyMod(m.Mod)` would
// compile and would even be a no-op cast. This function stays a per-flag
// translation anyway, and the reason is not paranoia about the alias: v1 could
// only ever express shift/alt/ctrl, while ultraviolet's KeyMod also carries
// meta, hyper, super, caps-lock and num-lock. Passing the mask through whole
// would start handing the child modifier bits it never used to receive. The
// three explicit branches are what keep the set forwarded identical to v1's.
// The emulator turns them into the shift/alt/ctrl bits of the encoded button
// byte (vt's SendMouse → ansi.EncodeMouseButton), so a ctrl+wheel the child
// reads as "zoom" arrives as a ctrl+wheel and not a bare one.
//
// The vt import is for the modifier CONSTANTS only — every mouse type on this
// path comes from the termpane aliases, so internal/ui still has one owner for
// the emulator's mouse surface.
func mouseMods(m tea.Mouse) vt.KeyMod {
	var mod vt.KeyMod
	if m.Mod.Contains(tea.ModShift) {
		mod |= vt.ModShift
	}
	if m.Mod.Contains(tea.ModAlt) {
		mod |= vt.ModAlt
	}
	if m.Mod.Contains(tea.ModCtrl) {
		mod |= vt.ModCtrl
	}
	return mod
}

// mouseMotionCoalesceWindow bounds how often a MOTION event actually reaches
// the child during a drag. Bubble Tea decodes one MouseActionMotion per cell
// the pointer crosses (this file's package doc), and forwardMouseToPane is
// the single unbatched producer into termpane's SendMouse → the response
// pump → the client's write queue that also carries every other frame to the
// host's socket — a fast drag across an 80-column pane can otherwise queue
// dozens of writes in the time it takes the pointer to cross it. 16ms
// (~60Hz) is chosen to sit below what a human drag or a terminal app's
// selection-tracking can perceive as missing resolution, while still cutting
// a multi-cell-per-frame drag down by an order of magnitude or more.
const mouseMotionCoalesceWindow = 16 * time.Millisecond

// motionCoalescer bounds how many MOTION events reach the child per pane
// within mouseMotionCoalesceWindow, while leaving press/release/wheel
// entirely alone. It exists as its own type, rather than two loose package
// vars, so a test can swap in a fully independent instance (deterministic
// clock, empty state) without partially resetting shared globals and risking
// a leftover frozen clock bleeding into an unrelated test.
//
// The map is keyed on paneHandle's identity (an interface value backed by a
// pointer, from either termpane.Pane or a test's fakePane) rather than
// folded into a single shared "last motion" instant, because routeMouse
// (dashboard) and attachModel's Update (attach) both call forwardMouseToPane
// against whichever pane handle they currently hold, and two different panes
// starting to move at the same instant must not suppress each other's first
// event.
//
// No mutex: forwardMouseToPane's own doc comment states the invariant this
// relies on — Update invokes it directly and synchronously, never from a
// goroutine — so there is exactly one caller in flight at a time for the
// lifetime of either program.
type motionCoalescer struct {
	now  func() time.Time
	last map[paneHandle]time.Time
}

// newMotionCoalescer builds a coalescer against the given clock. Production
// always calls this with time.Now; tests substitute a deterministic one to
// make the window's boundary exact rather than a source of flakiness.
func newMotionCoalescer(now func() time.Time) *motionCoalescer {
	return &motionCoalescer{now: now, last: make(map[paneHandle]time.Time)}
}

// allowMotion reports whether a MOTION event for p should be forwarded now,
// and records that decision so the next call within the window is denied.
// The first motion ever seen for a pane always passes — a coalescer that
// suppressed it too would mean a drag that never gets started.
//
// Admitting an event also evicts every entry that has aged past the window.
// An expired entry is decision-inert (the gate always passes it), but its KEY
// is a paneHandle — an interface value backed by a pointer — so keeping it
// pins that pane's whole termpane/emulator object graph for the map's
// lifetime, one dead pane per session ever dragged over. Eviction happens
// only on the admit path, which suffices: during any drag fast enough to be
// suppressed, an admit still occurs at least once per window. The residual
// retention is bounded at the panes seen moving within the last window (in
// practice one), never one entry per pane the dashboard has ever opened.
func (c *motionCoalescer) allowMotion(p paneHandle) bool {
	now := c.now()
	if last, ok := c.last[p]; ok && now.Sub(last) < mouseMotionCoalesceWindow {
		return false
	}
	for pane, last := range c.last {
		if now.Sub(last) >= mouseMotionCoalesceWindow {
			delete(c.last, pane)
		}
	}
	c.last[p] = now
	return true
}

// mouseCoalescer is the production coalescer forwardMouseToPane consults.
// Package-level rather than owned by Model or attachModel because both of
// those are out of this change's scope (see mouse_test.go's motion-coalescing
// section for why that constraint is load-bearing here, not incidental) and
// forwardMouseToPane's call sites pass only a pane handle and an event — no
// room to thread a coalescer through as a parameter without editing them.
var mouseCoalescer = newMotionCoalescer(time.Now)

// forwardMouseToPane delivers a translated mouse event to the hosted child.
// Like forwardKeyToPane, this is a synchronous in-memory call on the pane
// handle (the emulator encodes and queues the bytes; the response pump writes
// them), not a tea.Cmd, so Update invokes it directly. Callers own the
// nil-pane check and the input-ownership decision — this function is the
// delivery step and nothing else.
//
// MOTION events pass through mouseCoalescer first and are dropped when they
// arrive faster than mouseMotionCoalesceWindow allows; every other kind
// (press, release, wheel) is delivered unconditionally. Only motion is
// redundant in the sense that matters here — an intermediate cell on the way
// to wherever the pointer ends up — while a dropped or reordered press,
// release or wheel notch would change what button-state or scroll-state the
// child believes is true, which is a protocol violation and never just a
// resolution loss.
func forwardMouseToPane(p paneHandle, ev termpane.MouseEvent) {
	if _, motion := ev.(termpane.MouseMotion); motion && !mouseCoalescer.allowMotion(p) {
		return
	}
	p.SendMouse(ev)
}
