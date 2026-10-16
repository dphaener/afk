package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

var (
	keyCtrlW      = tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}
	keyEsc        = tea.KeyPressMsg{Code: tea.KeyEsc}
	keyLeft       = tea.KeyPressMsg{Code: tea.KeyLeft}
	keyRight      = tea.KeyPressMsg{Code: tea.KeyRight}
	keyShiftLeft  = tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift}
	keyShiftRight = tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
)

// recordSidebarPct installs a fake RememberSidebarPct seam on m and returns
// the slice it appends every write to.
func recordSidebarPct(m *Model, err error) *[]int {
	calls := &[]int{}
	m.deps.RememberSidebarPct = func(pct int) error {
		*calls = append(*calls, pct)
		return err
	}
	return calls
}

// drainCmd runs cmd and any batch it expands to, returning every message
// produced. It is how these tests prove what a key did NOT write: the seam is
// only ever reached from inside a Cmd.
func drainCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drainCmd(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// TestLeaderWEntersResizeModeFromNormal is ctrl+] w from the list, the
// way into resize mode since T7.2 (ctrl+w on the list is nothing now).
func TestLeaderWEntersResizeModeFromNormal(t *testing.T) {
	m := testModel(runningSess("alpha"))

	m, _ = update(t, m, leaderKeyPress)
	m, cmd := update(t, m, keyRune('w'))

	if !m.resizing {
		t.Fatal("ctrl+] w did not enter resize mode")
	}
	if got := activeInputMode(m.modeSnapshot()); got != inputResize {
		t.Fatalf("active mode = %v, want %v", got, inputResize)
	}
	if m.resizeStartPct != sidebarWidthPct {
		t.Errorf("resizeStartPct = %d, want the effective default %d", m.resizeStartPct, sidebarWidthPct)
	}
	if cmd != nil {
		t.Errorf("entering resize mode dispatched a Cmd")
	}
	if !strings.Contains(m.status, "ctrl+w") || !strings.Contains(m.status, "esc") {
		t.Errorf("entry toast = %q, want it to name the commit and cancel keys", m.status)
	}
}

// TestLeaderWEntersResizeModeFromDetailAndResumesDetailFocus is ctrl+] w
// from the focused overview: an overview action, so it runs in place and the
// overview has the keyboard again when the resize ends. Inside resize mode
// ctrl+w still commits and esc still cancels (T7.2 left resize mode's keys
// alone).
func TestLeaderWEntersResizeModeFromDetailAndResumesDetailFocus(t *testing.T) {
	// enter on a RUNNING row in the agent view connects (the dial state is
	// the agent placeholder); focusOverview focuses the overview itself.
	m := focusOverview(t, testModel(runningSess("alpha")))
	if activeInputMode(m.modeSnapshot()) != inputDetail {
		t.Fatal("precondition: the overview should be focused")
	}

	m = pressLeader(t, m, keyRune('w'))
	if !m.resizing {
		t.Fatal("ctrl+] w did not enter resize mode from detail focus")
	}
	if !m.detailOwnsKeyboard() {
		t.Fatal("entering resize mode dropped detail focus")
	}
	if got := activeInputMode(m.modeSnapshot()); got != inputResize {
		t.Fatalf("active mode = %v, want %v", got, inputResize)
	}

	m, _ = update(t, m, keyCtrlW)
	if m.resizing {
		t.Fatal("ctrl+w did not leave resize mode")
	}
	if got := activeInputMode(m.modeSnapshot()); got != inputDetail {
		t.Fatalf("after exit the active mode = %v, want detail focus to resume", got)
	}

	m = pressLeader(t, m, keyRune('w'))
	m, _ = update(t, m, keyEsc)
	if got := activeInputMode(m.modeSnapshot()); got != inputDetail {
		t.Fatalf("after cancel the active mode = %v, want detail focus to resume", got)
	}
}

func TestCtrlWIsForwardedToAFocusedPane(t *testing.T) {
	m, fp := frameModel("live", 120, 40, true)
	if activeInputMode(m.modeSnapshot()) != inputPane {
		t.Fatal("precondition: the pane should own the keyboard")
	}

	m, _ = update(t, m, keyCtrlW)

	if m.resizing {
		t.Fatal("ctrl+w entered resize mode from a focused pane; it belongs to the child")
	}
	if len(fp.keys) != 1 || fp.keys[0].Code != 'w' {
		t.Fatalf("pane received keys %+v, want the one ctrl+w", fp.keys)
	}
}

func TestResizeNudgesMoveTheDividerLive(t *testing.T) {
	cases := []struct {
		name  string
		key   tea.KeyPressMsg
		delta int
	}{
		{"l widens by one", keyRune('l').(tea.KeyPressMsg), 1},
		{"right widens by one", keyRight, 1},
		{"h narrows by one", keyRune('h').(tea.KeyPressMsg), -1},
		{"left narrows by one", keyLeft, -1},
		{"L widens by five", keyRune('L').(tea.KeyPressMsg), 5},
		{"shift+right widens by five", keyShiftRight, 5},
		{"H narrows by five", keyRune('H').(tea.KeyPressMsg), -5},
		{"shift+left narrows by five", keyShiftLeft, -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := frameModel("live", 120, 40, false)
			if !m.shouldRenderPane() {
				t.Fatal("precondition: the pane should be on screen")
			}
			m = pressLeader(t, m, keyRune('w'))
			beforeList, _, _ := m.layoutDims()
			fp.resizes = nil

			m, cmd := update(t, m, tc.key)

			want := sidebarWidthPct + tc.delta
			if m.sidebarPct != want {
				t.Fatalf("sidebarPct = %d, want %d", m.sidebarPct, want)
			}
			if cmd != nil {
				t.Errorf("a nudge dispatched a Cmd; nothing may persist before commit")
			}
			listOuter, _, _ := m.layoutDims()
			if wantList := sidebarOuterWidthFor(120, want); listOuter != wantList {
				t.Errorf("listOuter = %d, want %d", listOuter, wantList)
			}
			if (tc.delta > 0) != (listOuter > beforeList) {
				t.Errorf("listOuter moved from %d to %d, want direction %+d", beforeList, listOuter, tc.delta)
			}
			if len(fp.resizes) != 1 {
				t.Fatalf("pane resized %d times, want exactly once on the keypress: %v", len(fp.resizes), fp.resizes)
			}
			wantCols, wantRows := m.paneRegionSize()
			if fp.resizes[0] != [2]int{wantCols, wantRows} {
				t.Errorf("pane resized to %v, want the new region %dx%d", fp.resizes[0], wantCols, wantRows)
			}
			if !strings.Contains(m.status, "%") {
				t.Errorf("nudge toast = %q, want it to name the resulting percentage", m.status)
			}
		})
	}
}

func TestResizeClampsAtTheExtremes(t *testing.T) {
	m, _ := frameModel("live", 400, 40, false)
	m = pressLeader(t, m, keyRune('w'))

	for range 30 {
		m, _ = update(t, m, keyRune('L'))
	}
	if m.sidebarPct != maxSidebarPct {
		t.Fatalf("after many L presses sidebarPct = %d, want the max %d", m.sidebarPct, maxSidebarPct)
	}
	if listOuter, _, _ := m.layoutDims(); listOuter != 340 {
		t.Errorf("listOuter = %d, want 340 (85%% of 400)", listOuter)
	}
	m, _ = update(t, m, keyRune('l'))
	if m.sidebarPct != maxSidebarPct {
		t.Errorf("l past the max moved sidebarPct to %d", m.sidebarPct)
	}

	for range 30 {
		m, _ = update(t, m, keyRune('H'))
	}
	if m.sidebarPct != minSidebarPct {
		t.Fatalf("after many H presses sidebarPct = %d, want the min %d", m.sidebarPct, minSidebarPct)
	}
	if listOuter, _, _ := m.layoutDims(); listOuter != 60 {
		t.Errorf("listOuter = %d, want 60 (15%% of 400)", listOuter)
	}
	m, _ = update(t, m, keyRune('h'))
	if m.sidebarPct != minSidebarPct {
		t.Errorf("h past the min moved sidebarPct to %d", m.sidebarPct)
	}
}

func TestResizeCommitPersistsTheFinalPctOnce(t *testing.T) {
	m := testModel(runningSess("alpha"))
	calls := recordSidebarPct(&m, nil)

	m = pressLeader(t, m, keyRune('w'))
	m, _ = update(t, m, keyRune('l'))
	m, _ = update(t, m, keyRune('L'))
	m, _ = update(t, m, keyRune('h'))
	if len(*calls) != 0 {
		t.Fatalf("seam called %v before commit", *calls)
	}

	m, cmd := update(t, m, keyCtrlW)
	if m.resizing {
		t.Fatal("ctrl+w did not leave resize mode")
	}
	if cmd == nil {
		t.Fatal("commit dispatched no Cmd to persist the split")
	}
	msgs := drainCmd(cmd)
	if len(*calls) != 1 || (*calls)[0] != 43 {
		t.Fatalf("seam calls = %v, want exactly [43]", *calls)
	}
	if m.sidebarPct != 43 {
		t.Errorf("sidebarPct after commit = %d, want 43", m.sidebarPct)
	}
	for _, msg := range msgs {
		m, _ = update(t, m, msg)
	}
	if m.statusKind == notifyError {
		t.Errorf("a successful save raised an error toast: %q", m.status)
	}
}

func TestResizeCommitWithoutASeamDispatchesNothing(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m = pressLeader(t, m, keyRune('w'))
	m, _ = update(t, m, keyRune('l'))
	m, cmd := update(t, m, keyCtrlW)
	if cmd != nil {
		t.Fatal("commit with no RememberSidebarPct seam dispatched a Cmd")
	}
	if m.sidebarPct != 39 {
		t.Errorf("sidebarPct = %d, want the committed 39", m.sidebarPct)
	}
}

func TestResizeSaveErrorRaisesAnErrorToast(t *testing.T) {
	m := testModel(runningSess("alpha"))
	recordSidebarPct(&m, errors.New("disk full"))

	m = pressLeader(t, m, keyRune('w'))
	m, cmd := update(t, m, keyCtrlW)
	for _, msg := range drainCmd(cmd) {
		m, _ = update(t, m, msg)
	}
	if m.statusKind != notifyError || !strings.Contains(m.status, "disk full") {
		t.Fatalf("status = %q (kind %v), want an error toast carrying the write error", m.status, m.statusKind)
	}
}

func TestResizeEscRestoresTheStartWidthWithoutPersisting(t *testing.T) {
	m, fp := frameModel("live", 120, 40, false)
	calls := recordSidebarPct(&m, nil)
	m.setSidebarPct(50)
	startList, _, _ := m.layoutDims()

	m = pressLeader(t, m, keyRune('w'))
	m, _ = update(t, m, keyRune('L'))
	m, _ = update(t, m, keyRune('L'))
	if m.sidebarPct != 60 {
		t.Fatalf("precondition: sidebarPct = %d, want 60", m.sidebarPct)
	}
	fp.resizes = nil

	m, cmd := update(t, m, keyEsc)

	if m.resizing {
		t.Fatal("esc did not leave resize mode")
	}
	if m.sidebarPct != 50 {
		t.Errorf("sidebarPct = %d, want the start value 50", m.sidebarPct)
	}
	if listOuter, _, _ := m.layoutDims(); listOuter != startList {
		t.Errorf("listOuter = %d, want the start width %d", listOuter, startList)
	}
	if len(fp.resizes) != 1 {
		t.Errorf("pane resized %d times on cancel, want once back to the start region", len(fp.resizes))
	}
	drainCmd(cmd)
	if len(*calls) != 0 {
		t.Errorf("esc persisted the split: seam calls = %v", *calls)
	}
}

func TestResizeEscFromTheDefaultRestoresTheDefault(t *testing.T) {
	m := testModel(runningSess("alpha"))
	before, _, _ := m.layoutDims()
	m = pressLeader(t, m, keyRune('w'))
	m, _ = update(t, m, keyRune('H'))
	m, _ = update(t, m, keyEsc)
	if after, _, _ := m.layoutDims(); after != before {
		t.Errorf("listOuter after cancel = %d, want the default %d", after, before)
	}
}

func TestResizeQuitDoesNotPersist(t *testing.T) {
	m := testModel(runningSess("alpha"))
	calls := recordSidebarPct(&m, nil)
	m = pressLeader(t, m, keyRune('w'))
	m, _ = update(t, m, keyRune('L'))

	m, cmd := update(t, m, keyRune('q'))
	if !m.quitting && cmd == nil {
		t.Fatal("q did not quit from resize mode")
	}
	drainCmd(cmd)
	if len(*calls) != 0 {
		t.Errorf("quitting mid-resize persisted the split: seam calls = %v", *calls)
	}
}

func TestResizeModeIgnoresUnlistedKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"n", keyRune('n').(tea.KeyPressMsg)},
		{"j", keyRune('j').(tea.KeyPressMsg)},
		{"k", keyRune('k').(tea.KeyPressMsg)},
		{"c", keyRune('c').(tea.KeyPressMsg)},
		{"p", keyRune('p').(tea.KeyPressMsg)},
		{"colon", keyRune(':').(tea.KeyPressMsg)},
		{"slash", keyRune('/').(tea.KeyPressMsg)},
		{"question mark", keyRune('?').(tea.KeyPressMsg)},
		{"enter", keyEnter},
		{"down", keyDown},
		{"ctrl+t", tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("alpha"), runningSess("beta"))
			m = pressLeader(t, m, keyRune('w'))
			cursor := m.list.Index()

			m, cmd := update(t, m, tc.key)

			if cmd != nil {
				t.Errorf("%s dispatched a Cmd in resize mode", tc.name)
			}
			if !m.resizing {
				t.Errorf("%s left resize mode", tc.name)
			}
			if m.formOpen || m.leaderOpen || m.switcherOpen || m.modal != inputNormal || m.list.SettingFilter() || m.helpShowAll {
				t.Errorf("%s acted: form=%v leader=%v switcher=%v modal=%v filter=%v help=%v",
					tc.name, m.formOpen, m.leaderOpen, m.switcherOpen, m.modal, m.list.SettingFilter(), m.helpShowAll)
			}
			if m.list.Index() != cursor {
				t.Errorf("%s moved the session cursor from %d to %d", tc.name, cursor, m.list.Index())
			}
			if m.sidebarPct != 0 {
				t.Errorf("%s changed sidebarPct to %d", tc.name, m.sidebarPct)
			}
		})
	}
}

func TestResizeFooterShowsTheResizeHints(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m.width = 80
	m = pressLeader(t, m, keyRune('w'))

	footer := xansi.Strip(m.renderHelp())
	if got := lipgloss.Width(footer); got > m.width {
		t.Fatalf("resize footer is %d cells wide, want at most %d", got, m.width)
	}
	for _, want := range []string{"ctrl+w save split", "esc cancel", "h/← narrow list", "l/→ widen list"} {
		if !strings.Contains(footer, want) {
			t.Errorf("resize footer at width 80 = %q, missing %q", footer, want)
		}
	}

	wide := m
	wide.width = 200
	footer = xansi.Strip(wide.renderHelp())
	for _, want := range []string{"H/shift+← narrow list ×5", "L/shift+→ widen list ×5"} {
		if !strings.Contains(footer, want) {
			t.Errorf("resize footer at width 200 = %q, missing %q", footer, want)
		}
	}

	m, _ = update(t, m, keyEsc)
	if footer := m.renderHelp(); strings.Contains(footer, "save split") {
		t.Errorf("normal footer still shows the resize hints after cancel: %q", footer)
	}
}

func TestResizeDetailBoxEdgeNamesTheResizeKeys(t *testing.T) {
	m := focusOverview(t, testModel(runningSess("alpha")))
	m = pressLeader(t, m, keyRune('w'))

	_, hint := m.detailBoxLabels()
	if !strings.Contains(hint, "ctrl+w: save split") || !strings.Contains(hint, "esc: cancel") {
		t.Fatalf("detail edge hint while resizing = %q, want the commit and cancel keys", hint)
	}
	if strings.Contains(hint, "back to list") {
		t.Errorf("detail edge hint while resizing = %q advertises the detail release, which esc no longer does", hint)
	}
}

// TestResizeIsRunnableFromTheLeader finds the resize row by typing in the
// dialog's filter, the way the `:` palette used to reach it, and runs it with
// enter. The query starts with `d`, a key no row has, so the typing filters
// rather than running an action at its first letter.
func TestResizeIsRunnableFromTheLeader(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m, _ = update(t, m, leaderKeyPress)
	for _, r := range "divider" {
		m, _ = update(t, m, keyRune(r))
	}
	if vis := m.leader.visible(); len(vis) != 1 || vis[0].action != actionEnterResize {
		t.Fatalf("filtering for \"divider\" left %d rows, want only resize", len(vis))
	}
	m, _ = update(t, m, keyEnter)
	if m.leaderOpen {
		t.Fatal("running the resize row left the dialog open")
	}
	if !m.resizing {
		t.Fatal("the dialog's resize row did not enter resize mode")
	}
}

func TestResizeSuppressesAPendingPreview(t *testing.T) {
	m := testModel(runningSess("alpha"), runningSess("beta"))
	m.resizing = true
	_, cmd := m.applyPreviewTimer(previewTimerMsg{gen: m.previewGen, name: "alpha"})
	if cmd != nil {
		t.Fatal("a preview timer fired mid-resize and dispatched a pane open")
	}
}
