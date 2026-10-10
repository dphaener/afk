// attachpane.go is `afk attach`'s renderer: a Bubble Tea program whose whole
// UI is one embedded pane over a session's detached host.
//
// It exists so a session looks the same however you reach it. The dashboard
// renders sessions in a box, and bare attach used to hand the raw terminal
// straight to the host — same session, two different-feeling front ends, and the
// full-takeover one is the behavior that made a session "no longer rendered
// inside the TUI". Both paths now go through internal/termpane and hostpane.go's
// socket-backed child, so the pane box, the geometry math, the input routing and
// the detach semantics are shared code rather than parallel implementations.
package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/host"
)

// AttachPane connects to the session named name in worktree and renders it
// full-terminal inside a pane box until the user detaches (ctrl+\), another
// client steals the session, or the session ends. The returned DetachReason
// carries the outcome; `afk attach` turns it into the process exit code via
// its ExitCode method — the shared exit-code contract defined in
// internal/host/exit.go.
//
// wait is the retry-dial window (host.DefaultAttachWait when non-positive),
// which is the readiness gate for attaching to a host that was only just
// spawned. A dial failure is returned as-is (wrapping host.ErrHostNotReachable
// when the window expired) with no program ever started, so the caller can
// report "not running" without a terminal having been touched.
func AttachPane(name, worktree string, wait time.Duration) (host.DetachReason, error) {
	cfg, wedgeHint := attachPaneConfig(name, worktree, wait)
	pane, child, err := dialHostPane(cfg, wedgeHint)
	if err != nil {
		return 0, err
	}

	m := attachModel{pane: pane, name: name, mouseCapture: true}
	if _, err := newAttachProgram(m).Run(); err != nil {
		_ = pane.Close()
		return 0, err
	}
	// Close before reading the reason: it is only settled once the connection
	// has actually ended, and on the ctrl+\ path nothing has closed it yet.
	_ = pane.Close()
	return child.detachReason(), nil
}

// attachPaneConfig is the exact dial input AttachPane uses, split out as a
// pure function so this surface's wedge-banner wiring is pinned by a test
// without a live socket. The hint is wedgeHintAttach and must stay so: this
// program forwards every non-chord key to the child, so a banner naming the
// dashboard's recovery key here would instruct the user to type it into the
// wedged child (the fix-cycle-1 blocker on W6.T2).
//
// The 80x24 start is not a guess at the terminal: the pane's real geometry
// arrives with the first tea.WindowSizeMsg, which cannot happen before the
// program is running — and the program cannot run without a pane. The resize
// corrects it, and the session sees exactly one SIGWINCH more than it
// otherwise would.
func attachPaneConfig(name, worktree string, wait time.Duration) (hostPaneConfig, string) {
	return hostPaneConfig{
		Name:     name,
		Worktree: worktree,
		Cols:     80,
		Rows:     24,
		Wait:     wait,
	}, wedgeHintAttach
}

// newAttachProgram builds attach's tea.Program, for the same reason the
// dashboard has newDashProgram (dash.go): AttachPane and every test that runs a
// real program go through one constructor, so they cannot drift.
//
// It carries no option list any more. The alt screen and mouse reporting used to
// be tea.ProgramOptions applied here; under Bubble Tea v2 they are fields on the
// tea.View that View() returns, so the declaration moved into the model and this
// is a plain constructor. What that buys is the ctrl+g toggle below: a model that
// re-states its modes every frame can change them, where a startup option list
// could only be set once.
//
// extra is passed straight through so a test can add headless input and an
// output buffer; the real attach passes none.
func newAttachProgram(m tea.Model, extra ...tea.ProgramOption) *tea.Program {
	return tea.NewProgram(m, extra...)
}

// attachModel is the single-pane program's state. It is the same pane plumbing
// the dashboard uses (paneHandle, paneEventCmd, forwardKeyToPane), minus every
// dashboard concern: no session list, no discovery ticker, no modes.
type attachModel struct {
	pane paneHandle
	name string

	width  int
	height int
	ready  bool

	// ended records that the pane's connection closed on its own (a steal, or
	// the session finishing). The program quits on that, and View goes blank so
	// the alt-screen restore does not flash a dead session's final frame.
	ended bool

	// status carries a resize failure — the only in-band message this program
	// has to show, since everything else it could say is an exit code.
	status string

	// mouseCapture tracks whether the hosted session or the user's terminal owns
	// mouse events. True means the terminal reports mouse activity and this
	// program forwards it to the child (native selection is broken); false means
	// the terminal does not report and the user's selection works normally.
	// Toggled with ctrl+g (mouseChordKey, defined in model.go). It is not a
	// routing-only flag: View reads it to set tea.View.MouseMode, so flipping it
	// reconfigures the TERMINAL on the next frame — without that, the terminal
	// would stay in mouse-reporting mode and native selection would stay broken.
	// Initialized true at program startup.
	mouseCapture bool
}

func (m attachModel) Init() tea.Cmd {
	return paneEventCmd(m.pane)
}

func (m attachModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		cols, rows := m.paneSize()
		if err := m.pane.Resize(cols, rows); err != nil {
			m.status = "pane resize failed: " + err.Error()
		}
		return m, nil

	case tea.PasteMsg:
		// A bracketed paste is its own message type in v2 (it was a flag on the
		// key event in v1), so it is routed here rather than inside
		// forwardKeyToPane. The nil guard mirrors the mouse case below, which
		// documents why a handle can be absent while input is still in flight.
		if m.pane == nil {
			return m, nil
		}
		forwardPasteToPane(m.pane, msg.Content)
		return m, nil

	case tea.KeyPressMsg:
		// ctrl+\ is the reserved detach chord, matching both the dashboard's
		// focus-release key and the chord of the raw-terminal client this pane
		// replaced (host.Attach, which no afk command calls today). ctrl+g is the
		// mouse-toggle chord (mouseChordKey, defined in model.go). These two are
		// the only chords the session never sees; every other key — Ctrl+C
		// included — belongs to the session.
		//
		// v2 deleted the named ctrl-chord key constants (KeyCtrlBackslash,
		// KeyCtrlG); a chord is now the unmodified code plus ModCtrl.
		if isCtrlChord(msg, '\\') {
			return m, tea.Quit
		}
		if isCtrlChord(msg, 'g') {
			// No Cmd: View re-declares MouseMode from mouseCapture on the next
			// frame, which is what actually reconfigures the terminal.
			m.mouseCapture = !m.mouseCapture
			return m, nil
		}
		forwardKeyToPane(m.pane, msg)
		return m, nil

	// v2's tea.MouseMsg is an interface, so this single case binds all four of
	// MouseClickMsg / MouseReleaseMsg / MouseWheelMsg / MouseMotionMsg; see the
	// dashboard's matching case in model.go for why it must not be split.
	case tea.MouseMsg:
		// The handle can already be nil while an event is in flight:
		// paneExitedMsg clears it, and a queued mouse event decoded before that
		// message is dispatched still arrives afterwards.
		if m.pane == nil {
			return m, nil
		}
		// The same applies to the frame itself. View returns "" until the first
		// tea.WindowSizeMsg and again once the connection has ended, but paneSize
		// floors both axes at a positive number — so a width of 0 still yields a
		// content origin of (2, 2) and a 1x3 region, and a click there would reach
		// the session through a box that was never drawn. This condition mirrors
		// View's; the test pins them together against the rendered frame.
		if m.ended || !m.ready || m.width == 0 || m.height == 0 {
			return m, nil
		}
		// The same window the dashboard's routeMouse guards: under v2 the released
		// mouse mode reaches the terminal on the next FRAME, so an event decoded
		// before that write is still queued ahead of it and must not reach the
		// session after ctrl+g.
		if !m.mouseCapture {
			return m, nil
		}

		// paneScreenGeometry's cols IS the contentW View styles the box at — both
		// are paneContentDims(m.width, m.paneOuterHeight()) — so measuring the
		// title block at cols measures it at exactly the width it is rendered at,
		// with one value rather than two that could drift.
		//
		// The box's top-left is (0, 0) even when the frame does NOT fit, and that is
		// the half worth stating: this program draws the box first and the optional
		// status line below it, and the renderer clips an overflowing frame from the
		// BOTTOM — under AltScreen, which View sets on every frame, the frame area is
		// Rect(0, 0, width, height) and cells outside it are never drawn
		// (cursed_renderer.go:295, :345; ultraviolet styled.go:170-171). So at height 4
		// with a status line, where paneOuterHeight's floor of 4 plus the status row
		// makes a 5-row frame, the row the user loses is the STATUS row. The box's
		// first row is still 0, and shifting the origin up would put every forwarded
		// event a row above the cell the user clicked.
		originX, originY, cols, rows := m.paneScreenGeometry()

		// The bounds are the per-axis minimum of that region and the size the
		// emulator actually is. Neither is trustworthy alone here for a reason
		// specific to this program: paneOuterHeight() shrinks once m.status is
		// set, and the only thing that sets it is a FAILED Resize — so the
		// computed region disagrees with the emulator exactly when something has
		// already gone wrong, which is the worst moment to trust either one.
		paneCols, paneRows := m.pane.Size()
		boundCols, boundRows := mouseBounds(cols, rows, paneCols, paneRows)

		ev, ok := paneMouseEvent(msg, originX, originY, boundCols, boundRows)
		if !ok {
			return m, nil
		}
		forwardMouseToPane(m.pane, ev)
		return m, nil

	case paneRedrawMsg:
		if msg.pane != m.pane {
			return m, nil
		}
		return m, paneEventCmd(m.pane)

	case paneExitedMsg:
		if msg.pane != m.pane {
			return m, nil
		}
		m.ended = true
		return m, tea.Quit
	}
	return m, nil
}

// paneScreenGeometry is the content origin and the pane's own size, computed
// the one place both the mouse handler (above) and View (below) can share it
// from. It used to be inlined at the mouse case's call site; it moved out
// once View needed the identical numbers for the cursor, and a second copy
// is exactly the kind of drift paneContentOrigin's own doc comment (mouse.go)
// warns a caller not to introduce — a click origin and a cursor origin
// computed by two different expressions could disagree the next time either
// one is edited, even though nothing here would announce it.
func (m attachModel) paneScreenGeometry() (originX, originY, cols, rows int) {
	cols, rows = m.paneSize()
	originX, originY = paneContentOrigin(0, 0, m.titleBlock(cols))
	return originX, originY, cols, rows
}

// View renders the pane, and declares the terminal modes this program runs in.
// Under Bubble Tea v2 the alternate screen and mouse reporting are fields on the
// returned tea.View rather than tea.Program options (they used to be set in
// attachProgramOptions), so they are re-stated every frame and ctrl+g's toggle
// is just mouseCapture changing what this function declares.
//
// They are set before the blank-frame early return on purpose: that return is
// the "connection ended" frame, and dropping AltScreen there would restore the
// user's scrollback one frame before the program actually quits, flashing the
// terminal it exists to leave undisturbed.
func (m attachModel) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeNone
	if m.mouseCapture {
		v.MouseMode = tea.MouseModeCellMotion
	}

	if m.ended || !m.ready || m.width == 0 || m.height == 0 {
		return v
	}
	footer := ""
	if m.status != "" {
		footer = theme.statusLine.Render(truncate(m.status, m.width))
	}

	contentW, _ := paneContentDims(m.width, m.paneOuterHeight())
	title := m.titleBlock(contentW)
	body := lipgloss.JoinVertical(lipgloss.Left, title, m.pane.Render())
	box := sizedBox(theme.paneBoxFocused, contentW+2, m.paneOuterHeight()-boxBorderCells).
		MaxWidth(m.width).
		MaxHeight(m.paneOuterHeight()).
		Render(body)

	if footer == "" {
		v.SetContent(box)
	} else {
		v.SetContent(lipgloss.JoinVertical(lipgloss.Left, box, footer))
	}

	// attachModel has no focus state to gate this on — every key already
	// forwards to the single pane (Update's tea.KeyPressMsg case) — so
	// visibility of the child's own cursor is the only condition that matters.
	// paneScreenGeometry is the exact computation the mouse handler above
	// uses, shared rather than duplicated so the forwarded-click coordinate
	// space and the drawn cursor position cannot drift apart from each other.
	originX, originY, cols, rows := m.paneScreenGeometry()
	v.Cursor = paneCursor(m.pane.Cursor(), originX, originY, cols, rows, m.width, m.height)
	return v
}

// attachMouseReleasedHint is the title hint while the mouse belongs to the host
// terminal. It names ctrl+g because the title is the ONLY place this program can
// say anything — there is no help line to fall back on, and a user who has just
// released the mouse needs to know what gives it back.
//
// ctrl+g comes FIRST for that reason, ahead of the detach chord. paneTitleBlock
// truncates the whole "▶ name  hint" string from the right, so the tail of this
// hint is what a narrow terminal drops — and with a long session name it drops a
// lot: measured with a 28-character name, the hint's last cell survives only from
// width 74 up. Leading with ctrl+g moves the threshold for the part that matters
// from 62 columns down to 46 (28-character name; 38 at 20 characters, 51 at 33).
// The detach chord is what is sacrificed instead, and it is the right thing to
// sacrifice: it is also on the title in the CAPTURED state, which is the state
// this program starts in and spends nearly all its time in, so a user has already
// seen it — whereas the way back from a released mouse appears nowhere else.
//
// It does not fit at every width, and no arrangement would: the name is rendered
// before the hint and can consume the line by itself. The residual limits are
// recorded in issues.md.
//
// It is only used for the released state on purpose. Capture is on at startup
// and is the overwhelmingly common state, and its title must stay byte-identical
// to what this program rendered before the mouse existed
// (attachDetachHint, pinned by TestPaneTitleBlockReproducesLegacyTitles), so
// the hint mentions the mouse exactly when the mouse is somewhere surprising.
const attachMouseReleasedHint = "[ctrl+g mouse: term · ctrl+\\ detach]"

// titleBlock builds the pane title through paneTitleBlock so View and the mouse
// case cannot drift. This is not a tidy: both MUST measure the same string,
// because paneContentOrigin derives the emulator's first row from that block's
// height, and a title measured at a different width than the one on screen puts
// every forwarded event a row off. It also truncates, which is what stops
// attach's 47-cell title from wrapping below 56 columns and moving the origin
// down a row on its own.
func (m attachModel) titleBlock(contentW int) string {
	hint := attachDetachHint
	if !m.mouseCapture {
		hint = attachMouseReleasedHint
	}
	return paneTitleBlock(m.name, hint, contentW)
}

// paneOuterHeight is the terminal height minus the status row, which is only
// reserved when there is a status to show.
func (m attachModel) paneOuterHeight() int {
	h := m.height
	if m.status != "" {
		h--
	}
	if h < 4 {
		h = 4
	}
	return h
}

// paneSize is the emulator geometry for the current terminal, via the same
// paneContentDims the dashboard uses so the box, the emulator and the session's
// PTY agree on one width.
func (m attachModel) paneSize() (cols, rows int) {
	return paneContentDims(m.width, m.paneOuterHeight())
}

// frameRows is the height of the string View returns: the box, plus the status row
// when there is one. It mirrors View's composition rather than measuring View's
// output, because the mouse path runs in Update and may not render.
//
// paneOuterHeight floors at 4 while the status row is added on top of it, so this
// exceeds m.height at height 4 with a status set. The mouse path does not consult
// it — the overflow costs the frame its BOTTOM row, so no origin moves — but the
// number is what makes that claim checkable, and the short-terminal sweep in
// mouse_test.go uses it to assert the frame really does overflow at the heights it
// sweeps.
func (m attachModel) frameRows() int {
	rows := m.paneOuterHeight()
	if m.status != "" {
		rows++
	}
	return rows
}
