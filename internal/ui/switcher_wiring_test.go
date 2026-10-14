package ui

import (
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/dashmeta"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/session"
)

// --- helpers ---

var (
	keyP     = keyRune('p')
	keyR     = keyRune('R')
	keyCtrlT = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	keyCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

// switcherWiringProjects is the list every test here opens the overlay over:
// three basenames that do not collide, so project.DisplayNames leaves them bare
// and an assertion can look for the name it wrote.
func switcherWiringProjects() []project.Project {
	return scannedProjects("/roots/alpha", "/roots/beta", "/roots/gamma")
}

// switcherWiringModel builds the state these assertions need: three projects
// with the first active, two sessions, a VISIBLE pane on the selected one, and a
// populated scan cache.
//
// The visible pane is the load-bearing part of the fixture — most of what this
// file pins is what the overlay must NOT do to a running session behind it — so
// the helper asserts the pane really is on screen before handing the Model back.
func switcherWiringModel(t *testing.T) (Model, *fakePane, *perProjectSpy) {
	t.Helper()
	spy := &perProjectSpy{}
	d := spy.deps()
	// Layer a per-project discovery fixture over the spy's always-nil stub,
	// preserving the spy's own call recording (spy.snapshot() is what
	// TestSwitcherEnterSwitchesTheActiveProject asserts against): beta's real
	// load needs to return an actual HostRunning row, the shape switchProjectCmds'
	// loadCmd sees in production, for
	// TestSwitcherSwitchPreviewsTheFirstSessionOnceItsLoadLands's positive row.
	spyDiscover := d.Discover
	d.Discover = func(repo string) ([]discover.SessionState, error) {
		_, _ = spyDiscover(repo)
		return switcherWiringDiscoverFixture(repo), nil
	}
	d.Projects = switcherWiringProjects()
	d.ActiveProjectIndex = 0
	m := New("/roots/alpha", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40
	m.setSessions([]Session{sess("one"), sess("two")})
	m.lastLoad = time.Unix(1_000_000, 0)
	m.projectSessions = map[string][]discover.SessionState{
		"/roots/alpha": {{}},
		"/roots/beta": {
			{}, {AwaitingInput: true},
			// A findable, HostRunning row: resolveFocusTarget looks up a
			// non-active project's session by name in THIS cache (never in
			// Discover's live result), which is what the focus-request row of
			// TestSwitcherSwitchPreviewsTheFirstSessionOnceItsLoadLands needs to
			// resolve a click on beta's "beta-target" before the switch. No
			// existing test asserts this slice's length or contents (grepped),
			// so extending it here is safe.
			{Name: "beta-target", HostRunning: true, WorktreePath: "/roots/beta/wt/beta-target"},
		},
	}
	fp := newFakePane(paneSentinel)
	m.pane = fp
	m.paneName = "one"
	if !m.shouldRenderPane() {
		t.Fatal("precondition: the pane must be on screen before the overlay opens")
	}
	return m, fp, spy
}

// switcherWiringDiscoverFixture is what the live Discover seam returns per
// project in switcherWiringModel. Only beta carries a real row (a single
// HostRunning session, so "row 0" is unambiguous regardless of any sort the
// list applies) — every other project answers empty, matching the spy's
// original always-nil stub.
func switcherWiringDiscoverFixture(repo string) []discover.SessionState {
	if repo == "/roots/beta" {
		return []discover.SessionState{runningSess("beta-one").SessionState}
	}
	return nil
}

// openSwitcherOverlay presses `p` and asserts it opened, so every test below
// starts from an overlay opened through the real key path rather than from a
// hand-set flag.
func openSwitcherOverlay(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = update(t, m, keyP)
	if !m.switcherOpen {
		t.Fatal("`p` did not open the project switcher")
	}
	return m
}

// switcherSelectByPath moves an open overlay's cursor onto the row whose
// path is target, by pressing down until list.SelectedItem matches. Tests
// use this instead of a fixed number of keyDown presses to reach a row: the
// overlay's rows are ordered by sortSwitcherProjects (switcher.go) — tier by
// awaiting-input, then count, then name — not by scan order, and this
// fixture's beta is unconditionally awaiting-input, so a hardcoded index
// silently means a different project than intended the moment the sort's
// tie-break inputs change. Selecting by identity keeps a test anchored to
// WHICH project it means to land on regardless of where the sort puts it.
func switcherSelectByPath(t *testing.T, m Model, target string) Model {
	t.Helper()
	items := m.switcher.list.Items()
	for i := 0; i < len(items); i++ {
		if it, ok := m.switcher.list.SelectedItem().(switcherItem); ok && it.path == target {
			return m
		}
		m, _ = update(t, m, keyDown)
	}
	t.Fatalf("no switcher row found for path %q among %d rows", target, len(items))
	return m
}

// scanSeams wires the two scan seams onto Deps so scanCmd is live: the pipeline
// is inert without both (see scanEnabled), and the rescan assertion needs a real
// Cmd to collect.
func scanSeams(d Deps, found ...string) Deps {
	d.LoadConfig = func() (SearchSettings, string, error) {
		return SearchSettings{
			Roots:    []string{"/roots"},
			Interval: time.Minute,
			MaxDepth: 3,
		}, "/cfg/config.toml", nil
	}
	d.Scan = func([]string, int) []project.Project { return scannedProjects(found...) }
	return d
}

// --- tests ---

// TestSwitcherOpensOverTheRunningPane is the `p` contract in one frame: the
// switcher floats over the dashboard as a modal (modal.go) with its rows on
// screen, and the pane behind it is left running and connected. The pane may
// or may not still be visible around the box — that depends on where the box
// lands — so nothing is asserted about it; the mouse guard below rests on the
// input MODE (routeMouse drops events unless normal or pane mode owns input),
// not on the pane being off-screen.
func TestSwitcherOpensOverTheRunningPane(t *testing.T) {
	m, fp, _ := switcherWiringModel(t)
	before := render(m)
	if !strings.Contains(before, paneSentinel) {
		t.Fatalf("precondition: the pane should be rendered before `p`:\n%s", before)
	}

	m = openSwitcherOverlay(t, m)

	view := render(m)
	for _, name := range []string{"alpha", "beta", "gamma", "Projects"} {
		if !strings.Contains(view, name) {
			t.Errorf("View is missing the overlay row %q:\n%s", name, view)
		}
	}

	// Opening the switcher is not selecting a project: nothing detaches.
	if m.pane != fp || fp.closed != 0 {
		t.Errorf("pane = %v closed %d times, want the same pane still attached", m.pane, fp.closed)
	}
	if m.paneName != "one" || m.view == viewOverview {
		t.Errorf("paneName = %q viewOverview = %t, want %q and false", m.paneName, m.view == viewOverview, "one")
	}
	if len(fp.keys) != 0 || len(fp.texts) != 0 {
		t.Errorf("`p` was forwarded to the session as %v/%v, want nothing", fp.keys, fp.texts)
	}

	// Esc puts the pane back on screen, proving it stayed alive underneath.
	m, _ = update(t, m, keyEscMsg())
	if m.switcherOpen {
		t.Error("esc did not close the overlay")
	}
	if after := render(m); !strings.Contains(after, paneSentinel) {
		t.Errorf("the pane should be rendered again after esc:\n%s", after)
	}
}

// TestSwitcherKeyPrecedence is the routing table for an OPEN overlay. Every row
// is a specific failure the plan named:
//
//   - ctrl+t is the row this file exists for. The Toggle rule exists in the
//     pane and normal specs but NOT the switcher's, which is how the overlay
//     keeps ctrl+t from hiding the pane behind it.
//   - down/j pin that movement drives the overlay and never the session list;
//     the cursor invariant below is checked for every row for that reason.
//   - q pins that the global Quit binding — which covers BOTH q and ctrl+c — is
//     not consulted here: q is an ordinary key inside the overlay.
//   - ctrl+c is the sole global exception, matching the two modal modes.
//   - n and c pin that neither the creation form nor a clean confirmation can be
//     opened from under the overlay.
func TestSwitcherKeyPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		key            tea.KeyMsg
		wantOpen       bool
		wantIndex      int
		wantQuitting   bool
		wantPaneClosed int
	}{
		{name: "ctrl+t does not toggle the pane behind the overlay", key: keyCtrlT, wantOpen: true},
		{name: "down moves the overlay", key: keyDown, wantOpen: true, wantIndex: 1},
		{name: "j moves the overlay", key: keyRune('j'), wantOpen: true, wantIndex: 1},
		{name: "q is an ordinary key, not quit", key: keyRune('q'), wantOpen: true},
		{name: "n does not open the creation form", key: keyRune('n'), wantOpen: true},
		{name: "c does not start a clean confirmation", key: keyRune('c'), wantOpen: true},
		{name: "esc closes the overlay", key: keyEscMsg()},
		{
			name: "ctrl+c quits", key: keyCtrlC, wantOpen: true,
			wantQuitting: true, wantPaneClosed: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fp, _ := switcherWiringModel(t)
			m = openSwitcherOverlay(t, m)

			m, _ = update(t, m, tt.key)

			if m.switcherOpen != tt.wantOpen {
				t.Errorf("switcherOpen = %t, want %t", m.switcherOpen, tt.wantOpen)
			}
			if got := m.switcher.list.Index(); got != tt.wantIndex {
				t.Errorf("overlay index = %d, want %d", got, tt.wantIndex)
			}
			if m.quitting != tt.wantQuitting {
				t.Errorf("quitting = %t, want %t", m.quitting, tt.wantQuitting)
			}
			if fp.closed != tt.wantPaneClosed {
				t.Errorf("pane closed %d times, want %d", fp.closed, tt.wantPaneClosed)
			}
			if m.listSelection() != 0 {
				t.Errorf("cursor = %d, want 0 — the session list must not move", m.listSelection())
			}
			if m.formOpen {
				t.Error("the creation form must not open from under the overlay")
			}
			if m.modal != inputNormal {
				t.Errorf("mode = %v, want inputNormal", m.modal)
			}
			if m.view == viewOverview {
				t.Error("the pane behind the overlay must not be hidden")
			}
			// Both channels, not just keys: forwardKeyToPane routes a printable
			// rune through SendText and only a special key through SendKey, so
			// checking one of them would miss half the rows in this table.
			if len(fp.keys) != 0 || len(fp.texts) != 0 {
				t.Errorf("forwarded %v/%v to the session, want nothing", fp.keys, fp.texts)
			}
		})
	}
}

// TestSwitcherEnterSwitchesTheActiveProject pins the other half of the pane
// rule: merely OPENING the overlay leaves the pane attached
// (TestSwitcherOpensOverTheRunningPane), but SELECTING a different project
// detaches it, which is W3.T1's specified teardown. The spy assertion is what
// makes "dispatches load/stack/branches" mean "against the NEW project" rather
// than merely "dispatched".
func TestSwitcherEnterSwitchesTheActiveProject(t *testing.T) {
	m, fp, spy := switcherWiringModel(t)
	m = openSwitcherOverlay(t, m)

	m = switcherSelectByPath(t, m, "/roots/beta")
	m, cmd := update(t, m, keyEnter)

	if m.switcherOpen {
		t.Error("choosing a project must close the overlay")
	}
	if m.repo != "/roots/beta" || m.activeProject != 1 {
		t.Errorf("repo = %q activeProject = %d, want %q and 1", m.repo, m.activeProject, "/roots/beta")
	}
	if m.pane != nil || fp.closed != 1 {
		t.Errorf("pane = %v closed %d times, want detached exactly once", m.pane, fp.closed)
	}
	if m.sessions != nil {
		t.Errorf("sessions = %v, want the outgoing project's list cleared", m.sessions)
	}

	msgs := collectMsgs(t, cmd)
	if !hasMsg[sessionsLoadedMsg](msgs) || !hasMsg[stackLoadedMsg](msgs) || !hasMsg[plainBranchesLoadedMsg](msgs) {
		t.Error("a switch must dispatch load, stack, and plain branches")
	}
	discovered, detected, branched := spy.snapshot()
	for _, calls := range [][]string{discovered, detected, branched} {
		if len(calls) != 1 || calls[0] != "/roots/beta" {
			t.Errorf("per-project seam called with %v, want [/roots/beta]", calls)
		}
	}
}

// TestSwitcherEnterOnTheActiveProjectIsANoOp pins that Enter routes through
// switchProject rather than doing its own repointing: the method no-ops on the
// already-active path, which is what keeps the pane holding the session the user
// is watching from being torn down by a stray Enter.
func TestSwitcherEnterOnTheActiveProjectIsANoOp(t *testing.T) {
	m, fp, _ := switcherWiringModel(t)
	m = openSwitcherOverlay(t, m)
	m = switcherSelectByPath(t, m, "/roots/alpha")

	m, cmd := update(t, m, keyEnter)

	if m.switcherOpen {
		t.Error("Enter must close the overlay even when it selects the active project")
	}
	if cmd != nil {
		t.Error("re-selecting the active project must dispatch nothing")
	}
	if m.pane != fp || fp.closed != 0 || len(m.sessions) != 2 {
		t.Error("re-selecting the active project must leave pane and session state alone")
	}
}

// TestSwitcherRescanDispatchesAScanImmediately pins the rescan result: the
// parent turns it into a scan Cmd on the spot rather than waiting out the
// scan ticker, which for a 60s interval is the difference between `R` working
// and `R` looking broken. The overlay deliberately stays open — `R` means
// "refresh this list", not "close it".
func TestSwitcherRescanDispatchesAScanImmediately(t *testing.T) {
	spy := &perProjectSpy{}
	d := scanSeams(spy.deps(), "/roots/alpha", "/roots/beta")
	d.Projects = switcherWiringProjects()
	d.ActiveProjectIndex = 0
	m := New("/roots/alpha", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40

	m = openSwitcherOverlay(t, m)
	m, cmd := update(t, m, keyR)

	if !m.switcherOpen {
		t.Error("a rescan must leave the overlay open")
	}
	if cmd == nil {
		t.Fatal("`R` dispatched no Cmd, want a scan")
	}
	if msgs := collectMsgs(t, cmd); !hasMsg[projectScanMsg](msgs) {
		t.Errorf("collected %v, want a projectScanMsg", msgs)
	}
}

// TestSwitcherMouseIsDroppedWhileTheOverlayIsUp is the mouse half of the
// precedence rule. Both rows aim at the pane's own content origin — the one
// coordinate that is guaranteed to be forwarded when nothing owns input — so a
// missing guard fails here rather than passing by accident on a dropped
// out-of-region event.
func TestSwitcherMouseIsDroppedWhileTheOverlayIsUp(t *testing.T) {
	tests := []struct {
		name  string
		event func(x, y int) tea.MouseMsg
	}{
		{name: "click", event: mouseAt},
		{
			name: "wheel",
			event: func(x, y int) tea.MouseMsg {
				return tea.MouseWheelMsg{
					X: x, Y: y,
					Button: tea.MouseWheelDown,
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fp, originX, originY, _, _ := mouseModel(t)
			m.projects = switcherWiringProjects()
			m = openSwitcherOverlay(t, m)

			m, cmd := update(t, m, tt.event(originX, originY))

			if cmd != nil {
				t.Error("the mouse path must return no Cmd")
			}
			if len(fp.mice) != 0 {
				t.Errorf("forwarded %v to the session, want nothing", fp.mice)
			}
			if m.listSelection() != 0 {
				t.Errorf("cursor = %d, want 0 — the session list has no mouse behaviour", m.listSelection())
			}
		})
	}
}

// TestPWhileThePaneIsFocusedReachesTheSession pins the precondition that makes
// "overlay open AND pane focused" all but unreachable: a focused pane owns every
// key, `p` included, so the overlay cannot be opened from there at all. Without
// this row, the precedence chosen for that combination would look arbitrary.
func TestPWhileThePaneIsFocusedReachesTheSession(t *testing.T) {
	m, fp, _ := switcherWiringModel(t)
	m.focus = focusRight

	m, _ = update(t, m, keyP)

	if m.switcherOpen {
		t.Error("`p` must not open the overlay while the pane owns the keyboard")
	}
	if len(fp.texts) != 1 || fp.texts[0] != "p" {
		t.Fatalf("forwarded %v to the session, want [p]", fp.texts)
	}
}

// TestSpawnErrorModalOutranksTheOverlay pins the third interaction the plan
// asks for explicitly. The modal rows of modePrecedence outrank the switcher, so
// a background create that failed while the overlay was up takes the keyboard:
// esc dismisses the MODAL and the overlay is still there underneath. Pinning it
// is the point — the alternative is discovering it as a bug report.
func TestSpawnErrorModalOutranksTheOverlay(t *testing.T) {
	m, _, _ := switcherWiringModel(t)
	m = openSwitcherOverlay(t, m)

	m, _ = update(t, m, spawnFinishedMsg{name: "three", err: errors.New("boom")})
	if m.modal != inputSpawnError {
		t.Fatalf("mode = %v, want inputSpawnError", m.modal)
	}
	if !m.switcherOpen {
		t.Error("the failed create must not close the overlay")
	}

	m, _ = update(t, m, keyEscMsg())
	if m.modal != inputNormal {
		t.Errorf("mode = %v, want the modal dismissed first", m.modal)
	}
	if !m.switcherOpen {
		t.Error("the first esc belongs to the modal, not to the overlay")
	}

	m, _ = update(t, m, keyEscMsg())
	if m.switcherOpen {
		t.Error("the second esc should close the overlay")
	}
}

// TestSwitcherRebuildsOnlyWhenTheProjectListChanges pins how an OPEN overlay
// tracks a scan that lands underneath it. The component holds a snapshot and
// never refreshes itself (switcher.go's package doc puts that on the parent), so
// both rows matter and for opposite reasons: an unchanged list must NOT rebuild,
// or the 60s ticker yanks the selection out from under a user who is aiming at a
// row; a changed list MUST rebuild, or `R` after creating a project appears to do
// nothing at all.
func TestSwitcherRebuildsOnlyWhenTheProjectListChanges(t *testing.T) {
	tests := []struct {
		name      string
		scanned   []string
		wantRows  int
		wantIndex int
	}{
		{
			name:      "an unchanged list keeps the selection",
			scanned:   []string{"/roots/alpha", "/roots/beta", "/roots/gamma"},
			wantRows:  3,
			wantIndex: 1,
		},
		{
			name:      "a changed list rebuilds the rows",
			scanned:   []string{"/roots/alpha", "/roots/beta", "/roots/gamma", "/roots/delta"},
			wantRows:  4,
			wantIndex: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, _ := switcherWiringModel(t)
			m = openSwitcherOverlay(t, m)
			m, _ = update(t, m, keyDown)
			if m.switcher.list.Index() != 1 {
				t.Fatal("precondition: the overlay's cursor should be on the second row")
			}

			m, _ = update(t, m, projectScanMsg{projects: scannedProjects(tt.scanned...)})

			if got := len(m.switcher.list.Items()); got != tt.wantRows {
				t.Errorf("overlay rows = %d, want %d", got, tt.wantRows)
			}
			if got := m.switcher.list.Index(); got != tt.wantIndex {
				t.Errorf("overlay index = %d, want %d", got, tt.wantIndex)
			}
			if !m.switcherOpen {
				t.Error("a scan result must not close the overlay")
			}
		})
	}
}

// TestSwitcherCountsForTheActiveProject pins where each row's session count
// comes from. The scan cache is refreshed on the 60s scan ticker while
// m.sessions is refreshed every 2s, so for the ONE project whose rows are on
// screen beside the overlay the cache is a stale copy of data the Model already
// holds fresh — and a count that disagrees with the sidebar the user is looking
// at reads as a bug.
//
// The "before the first load" row is the reason the swap is gated on lastLoad
// rather than done unconditionally: a Model whose discovery has not reported yet
// (startup, or the moment after a switch) has an empty session list that would
// otherwise overwrite a perfectly good cached count with zero.
func TestSwitcherCountsForTheActiveProject(t *testing.T) {
	tests := []struct {
		name     string
		sessions []Session
		loaded   bool
		want     string
	}{
		{
			name:     "a loaded active project counts its live sessions",
			sessions: []Session{sess("one"), sess("two")},
			loaded:   true,
			want:     "2 sessions",
		},
		{
			name:   "before the first load the cache stands in",
			loaded: false,
			want:   "1 session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, _ := switcherWiringModel(t)
			m.setSessions(tt.sessions)
			if !tt.loaded {
				m.lastLoad = time.Time{}
			}
			m = openSwitcherOverlay(t, m)

			var alpha string
			for _, line := range strings.Split(render(m), "\n") {
				// The session list box's own top/bottom border now carries the
				// active project's name too (W3.T1's sessionListLabel), and it
				// renders before the switcher overlay's row in render(m)'s line
				// order. Matching "alpha" alone finds that border first and never
				// reaches the row this test actually wants. Skip border lines by
				// their leading corner glyph (after stripping ANSI, since
				// paintEdge colors the corner) rather than by project name, so a
				// future label change to either the border or the overlay row
				// can't silently swap which one this test reads. The overlay's
				// own rows start with the box's left edge (`│`), which must stay
				// eligible — it is a candidate row, not chrome to skip.
				stripped := xansi.Strip(line)
				if strings.HasPrefix(stripped, "╭") || strings.HasPrefix(stripped, "╰") {
					continue
				}
				if strings.Contains(line, "alpha") {
					alpha = line
					break
				}
			}
			if alpha == "" {
				t.Fatalf("no rendered line contains the active project's row:\n%s", render(m))
			}
			if !strings.Contains(alpha, tt.want) {
				t.Errorf("alpha row = %q, want it to contain %q", alpha, tt.want)
			}
		})
	}
}

// TestSwitcherOpensOverAnEmptyProjectList pins that `p` is not silently inert
// for a Model that has no projects — every testModel in this package is one, and
// so is a dashboard launched before its first scan lands. It must open, render,
// and close without panicking; W5.T1 owns what the empty list actually says.
//
// Enter with nothing selected is the component's own no-op (there is no row to
// choose, so it reports switcherNone), and the parent must not paper over that
// with a close of its own: `esc` is the documented dismissal and staying open is
// what makes a keypress into an empty list harmless.
func TestSwitcherOpensOverAnEmptyProjectList(t *testing.T) {
	m := testModel(sess("one"))

	m = openSwitcherOverlay(t, m)
	_ = render(m)

	m, cmd := update(t, m, keyEnter)
	if cmd != nil {
		t.Error("Enter on an empty overlay must dispatch nothing")
	}
	if !m.switcherOpen || m.repo != "/repo" {
		t.Errorf("switcherOpen = %t repo = %q, want the overlay open and the repo untouched",
			m.switcherOpen, m.repo)
	}

	m, _ = update(t, m, keyEscMsg())
	if m.switcherOpen {
		t.Error("esc must close an empty overlay")
	}
}

// --- switch-then-load preview wiring ---

// firstSessionsLoaded finds the first sessionsLoadedMsg in a collected batch,
// the way hasMsg answers a yes/no but this test needs the payload (the session
// list the real switchProjectCmds trio actually loaded) to name row 0.
func firstSessionsLoaded(msgs []tea.Msg) (sessionsLoadedMsg, bool) {
	for _, msg := range msgs {
		if sl, ok := msg.(sessionsLoadedMsg); ok {
			return sl, true
		}
	}
	return sessionsLoadedMsg{}, false
}

// firstPreviewTimer finds the first previewTimerMsg in a collected batch.
func firstPreviewTimer(msgs []tea.Msg) (previewTimerMsg, bool) {
	for _, msg := range msgs {
		if pt, ok := msg.(previewTimerMsg); ok {
			return pt, true
		}
	}
	return previewTimerMsg{}, false
}

// firstPaneAttached finds the first paneAttachedMsg in a collected batch — the
// same-project focus-request race row needs it to drive an explicit open's own
// arrival through Update (clearing attachInFlight) before asserting on what a
// LATER load does, rather than stopping at "a Cmd was dispatched".
func firstPaneAttached(msgs []tea.Msg) (paneAttachedMsg, bool) {
	for _, msg := range msgs {
		if pa, ok := msg.(paneAttachedMsg); ok {
			return pa, true
		}
	}
	return paneAttachedMsg{}, false
}

// previewCollectDeadline is collectMsgs' own cmdDeadline (focusrequest_test.go)
// widened for this file alone. previewTimerCmd arms a REAL tea.Tick of
// previewDelay (messages.go), and previewDelay and cmdDeadline are BOTH exactly
// 300ms — racing this suite's genuinely-armed timer against that deadline would
// be a coin flip. Nothing else in this package executes a real preview timer
// through a collected batch, so widening cmdDeadline itself (shared by every
// other test in the file) is not warranted; a local deadline passed into
// collectMsgsWithDeadline is.
const previewCollectDeadline = previewDelay * 2

// collectMsgsPreview is collectMsgsWithDeadline (focusrequest_test.go) at
// previewCollectDeadline instead of the package's shared cmdDeadline — the two
// rows below that let a previewTimerCmd actually fire need the wider window;
// every other test in this file goes through the ordinary short deadline via
// this same helper.
func collectMsgsPreview(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	return collectMsgsWithDeadline(t, cmd, previewCollectDeadline)
}

// TestSwitcherSwitchPreviewsTheFirstSessionOnceItsLoadLands is the regression
// test for the bug this wave exists to fix: switching projects through the
// switcher clears m.sessions and dispatches the load trio (switchProjectCmds),
// but nothing arms a preview when that load lands — unlike a cursor move, which
// calls schedulePreview() itself. A user who switches projects sees an empty
// pane area until they press a key, even though a session is right there,
// running, at the top of the list.
//
// Row 1 (the positive path) and row 3's second half (the arm surviving a load
// error) are load-bearing in the TDD sense: they exercise the not-yet-built
// one-shot arm flag and MUST fail against today's code, which never calls
// schedulePreview from the sessionsLoadedMsg handler at all. The rest pin
// behavior that must NOT regress once that flag exists, each against a
// specific way of getting there wrong:
//
//   - Row 2 proves a no-op Enter (the already-active project) arms nothing on
//     its next ordinary tick — there was no switch, so there must be no flag.
//     Its reload lands on a HostRunning row that is NOT the one already on
//     screen, so a mutated no-op guard (`if true`) has nothing else to hide
//     behind and this row actually fails under it.
//   - Row 4 proves the flag is one-shot: a SECOND load after the switch (the
//     routine 2s tick) must not re-arm, or every tick after a switch would
//     re-preview row 0 out from under a user who has since moved the cursor.
//   - Row 5 proves a focus-request switch (a click from another project) arms
//     nothing, and drives the click's own explicit open all the way through
//     paneAttachedMsg before asserting: while that open is still outstanding,
//     schedulePreview's OWN "explicit open blocks arming" refusal would hide a
//     wrongly-armed flag (e.g. one moved into switchProjectCmds) just as well
//     as a correctly-unset one, so stopping short of that arrival tests
//     nothing about previewOnNextLoad specifically.
//   - Row 6 is the stale-load race a rapid A→B→C switch (or an ordinary 2s
//     tick for the OUTGOING project still in flight when a switch lands) can
//     produce: a load stamped with the generation the switch just moved past
//     must be dropped whole — neither installed nor allowed to consume the
//     arm — and the correctly-stamped load that follows must still work.
//   - Row 7 is the narrower same-project click race: a focus request for a
//     session in the project the switcher just switched TO never calls
//     switchProjectCmds at all (resolveFocusTarget answers from m.sessions
//     for the active project, so target.project stays empty), so nothing
//     resets previewOnNextLoad on that path — only the explicit open itself,
//     via openPaneFor, can supersede the pending arm.
func TestSwitcherSwitchPreviewsTheFirstSessionOnceItsLoadLands(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "switching projects arms a preview once the new project's first load lands",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, switchCmd := update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}

				loaded, ok := firstSessionsLoaded(collectMsgsPreview(t, switchCmd))
				if !ok {
					t.Fatal("precondition: switching projects must dispatch a sessionsLoadedMsg")
				}
				if len(loaded.sessions) == 0 || !loaded.sessions[0].HostRunning {
					t.Fatalf("precondition: beta's fixture must load a HostRunning row 0, got %+v", loaded.sessions)
				}
				wantName := loaded.sessions[0].Name

				m, armCmd := update(t, m, loaded)
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatalf("switching to a project must arm a preview for its first-loaded row 0 (%q); it never did", wantName)
				}
				if armed.name != wantName {
					t.Errorf("armed preview name = %q, want row 0's %q", armed.name, wantName)
				}
				if armed.gen != m.previewGen {
					t.Errorf("armed preview gen = %d, want the model's current previewGen %d", armed.gen, m.previewGen)
				}

				// Feeding the timer back through Update — rather than stopping at
				// "a previewTimerMsg was produced" — is what proves the dial was
				// actually dispatched as a PREVIEW: attachInFlight.preview is set
				// by openPaneFor from the request applyPreviewTimer builds, and a
				// timer that merely exists but gets dropped by a stale-gen or
				// wrong-selection check would look identical from the outside.
				m, _ = update(t, m, armed)
				if m.attachInFlight.name != wantName || !m.attachInFlight.preview {
					t.Errorf("attachInFlight = %+v, want {name: %q, preview: true}", m.attachInFlight, wantName)
				}
			},
		},
		{
			name: "re-selecting the already-active project arms nothing on its next load",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				// Moved off the paned row ("one") BEFORE opening the overlay. The
				// no-op re-select must not arm anything REGARDLESS of which row the
				// next load selects, but if that row were the one already on
				// screen, schedulePreview's own "already on screen" refusal would
				// silence a previewTimerMsg whether or not the no-op guard held —
				// hiding a mutated guard (`if true`) exactly as well as a correct
				// one. Landing the reload's selection on a DIFFERENT, HostRunning
				// row removes that cover.
				m, _ = update(t, m, keyDown)
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/alpha")
				m, switchCmd := update(t, m, keyEnter)
				if switchCmd != nil {
					t.Fatalf("precondition: re-selecting the active project must dispatch nothing, got a Cmd")
				}

				m, tickCmd := update(t, m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("one"), runningSess("two")},
					at:       time.Unix(2, 0),
				})
				if s, ok := m.selected(); !ok || s.Name != "two" || !s.HostRunning {
					t.Fatalf("precondition: the reloaded selection must be a real, HostRunning, un-paned row — got %+v (ok=%t)", s, ok)
				}
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("an ordinary tick after a no-op switch must not arm a preview: got %v", msgs)
				}
			},
		},
		{
			name: "a failed first load after the switch does not arm, but the next success does",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}

				m, errCmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, err: errors.New("boom"), at: time.Unix(2, 0)})
				if msgs := collectMsgsPreview(t, errCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a failed first load must not arm a preview: got %v", msgs)
				}

				m, okCmd := update(t, m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("beta-one")},
					at:       time.Unix(3, 0),
				})
				if msgs := collectMsgsPreview(t, okCmd); !hasMsg[previewTimerMsg](msgs) {
					t.Fatalf("the arm-on-switch flag must survive a load error and be consumed by the next success: got %v", msgs)
				}
			},
		},
		{
			name: "a second load after the switch does not re-arm — the flag is one-shot",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, switchCmd := update(t, m, keyEnter)

				loaded, ok := firstSessionsLoaded(collectMsgsPreview(t, switchCmd))
				if !ok {
					t.Fatal("precondition: switching projects must dispatch a sessionsLoadedMsg")
				}
				m, armCmd := update(t, m, loaded)
				if _, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd)); !ok {
					t.Fatal("precondition: the first load after a switch must arm a preview")
				}

				m, tickCmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: loaded.sessions, at: time.Unix(9, 0)})
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a routine tick after the arm was already consumed must not re-arm: got %v", msgs)
				}
			},
		},
		{
			name: "a focus-request switch arms nothing — its own explicit open owns the attach",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}
				// A hand-built rendezvous rather than the full fsnotify fixture
				// (focusrequest_test.go's rendezvousFixture): applyFocusRequest only
				// needs a dir that maps back to a project via m.rendezvous, and
				// fromWatch stays false below so there is no watcher to arm or
				// re-arm.
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				m, focusCmd := update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the focus request must switch to beta, got %q", m.repo)
				}

				msgs := collectMsgsPreview(t, focusCmd)
				loaded, ok := firstSessionsLoaded(msgs)
				if !ok {
					t.Fatal("precondition: the focus-request switch must dispatch a sessionsLoadedMsg")
				}
				// Drive the click's own explicit open to completion — see the
				// function doc for why stopping before this arrival would leave
				// the row unable to tell a correct fix from a broken one.
				attached, ok := firstPaneAttached(msgs)
				if !ok {
					t.Fatal("precondition: the focus request's own explicit open must dispatch a paneAttachedMsg")
				}
				m, _ = update(t, m, attached)
				if m.attachInFlight.name != "" {
					t.Fatalf("precondition: the click's own arrival must clear attachInFlight, got %+v", m.attachInFlight)
				}

				m, tickCmd := update(t, m, loaded)
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a focus-request switch must not arm a preview — its own explicit open already owns the attach: got %v", msgs)
				}
			},
		},
		{
			name: "a stale-generation load from before the switch is dropped whole",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				staleGen := m.projectGen
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}
				if m.projectGen == staleGen {
					t.Fatalf("precondition: a real switch must bump projectGen past %d", staleGen)
				}

				// The shape a rapid A→B→C switch (or an ordinary 2s tick for the
				// OUTGOING project still in flight when the switch lands) produces:
				// a load stamped with the generation the switch just moved past,
				// carrying a DIFFERENT project's sessions.
				m, staleCmd := update(t, m, sessionsLoadedMsg{
					gen:      staleGen,
					sessions: []Session{runningSess("alpha-stale")},
					at:       time.Unix(2, 0),
				})
				if len(m.sessions) != 0 {
					t.Errorf("a stale-generation load must not be applied: sessions = %+v, want the switch's cleared list untouched", m.sessions)
				}
				if msgs := collectMsgsPreview(t, staleCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a stale-generation load must not arm a preview: got %v", msgs)
				}

				m, okCmd := update(t, m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("beta-one")},
					at:       time.Unix(3, 0),
				})
				if msgs := collectMsgsPreview(t, okCmd); !hasMsg[previewTimerMsg](msgs) {
					t.Fatalf("the correctly-stamped load must still arm the preview once it lands: got %v", msgs)
				}
			},
		},
		{
			name: "a same-project focus-request click racing the switch's pending arm clears it",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}
				if !m.previewOnNextLoad {
					t.Fatal("precondition: the switch must arm previewOnNextLoad")
				}

				// A click for a session in beta — the project the switch just
				// landed on — while beta's own first load is still outstanding.
				// resolveFocusTarget answers a request for the ACTIVE project from
				// m.sessions, so it is set here directly rather than waited for:
				// the point of this row is the click racing AHEAD of that load,
				// not behind it.
				m.setSessions([]Session{runningSess("beta-target")})
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				m, focusCmd := update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: a same-project click must not switch away from beta, got %q", m.repo)
				}
				// The supersede has to be visible IMMEDIATELY, at dispatch time —
				// openPaneFor clears the flag synchronously — not merely once the
				// click's own arrival lands, or the window the finding describes
				// stays open for anything racing in between.
				if m.previewOnNextLoad {
					t.Error("a same-project explicit open must supersede the switch's pending arm the moment it dispatches")
				}

				attached, ok := firstPaneAttached(collectMsgsPreview(t, focusCmd))
				if !ok {
					t.Fatal("precondition: the same-project click must dispatch its own explicit open")
				}
				m, _ = update(t, m, attached) // drive the open to completion

				m, tickCmd := update(t, m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("beta-target"), runningSess("beta-one")},
					at:       time.Unix(4, 0),
				})
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a same-project click racing the switch's pending arm must not let a later load arm a preview: got %v", msgs)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// TestApplyScanResultPromotionArmsNoPreview pins previewOnNextLoad's own doc
// comment (model.go): applyScanResult's fallback-to-first-project promotion
// (D7 — an active index of -1 resolved to the first discovered project) is
// automatic reconciliation, not a user selection, and must arm no preview the
// way a switcher choice does. The fixture gives the promoted project a real,
// HostRunning row so a wrongly-armed flag would have something to preview —
// an empty list would let this pass for the wrong reason.
func TestApplyScanResultPromotionArmsNoPreview(t *testing.T) {
	d := testDeps()
	d.ActiveProjectIndex = -1
	d.Discover = func(repo string) ([]discover.SessionState, error) {
		if repo == "/roots/a" {
			return []discover.SessionState{runningSess("a-one").SessionState}, nil
		}
		return nil, nil
	}
	m := New("/not-a-repo", time.Second, d)

	got, cmd := m.Update(projectScanMsg{projects: scannedProjects("/roots/a", "/roots/b")})
	m, ok := got.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", got)
	}
	if m.activeProjectPath() != "/roots/a" {
		t.Fatalf("precondition: the promotion must land on the first project, got %q", m.activeProjectPath())
	}
	if m.previewOnNextLoad {
		t.Fatal("precondition: applyScanResult's promotion must not arm previewOnNextLoad itself")
	}

	loaded, ok := firstSessionsLoaded(collectMsgsPreview(t, cmd))
	if !ok {
		t.Fatal("precondition: the promotion must dispatch a sessionsLoadedMsg")
	}
	m, tickCmd := update(t, m, loaded)
	if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
		t.Errorf("applyScanResult's promotion must not arm a preview once its load lands: got %v", msgs)
	}
}

// --- restoring the remembered selection ---------------------------------------

// rememberedLoad is the load every row below hand-builds instead of taking the
// one switchProjectCmds actually dispatched. switcherWiringDiscoverFixture
// answers beta with a SINGLE row, so a load taken from the real trio can only
// ever leave the cursor on row 0 — "the remembered row was restored" and "the
// restore did nothing" would be the same observation, and every row here would
// pass against a no-op.
func rememberedLoad(gen int, sessions ...Session) sessionsLoadedMsg {
	return sessionsLoadedMsg{gen: gen, sessions: sessions, at: time.Unix(2, 0)}
}

// betaRows is the three-row list rows (a), (b), (d) and (e) load: the
// remembered target is the MIDDLE row, so a restore that lands anywhere else
// (row 0, or a clamp to the last row) is visible.
func betaRows() []Session {
	return []Session{runningSess("b-1"), runningSess("b-2"), runningSess("b-3")}
}

// startupSelectionModel is the startup counterpart of switcherWiringModel: a
// New-built Model whose LoadSelections seam remembers one row for the launch
// repo, left deliberately NOT ready. Rows (f) and (g) become ready by sending a
// real tea.WindowSizeMsg through Update — the arrival the consumption guard
// waits on — and row (h) exists precisely to exercise the state before it.
func startupSelectionModel(remembered, initialSession string) Model {
	d := testDeps()
	d.LoadSelections = func() map[string]string { return map[string]string{"/repo": remembered} }
	d.InitialSessionName = initialSession
	d.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane(paneSentinel), nil }
	return New("/repo", time.Second, d)
}

// startupRows is rows (f), (g) and (h)'s session list, shaped like betaRows for
// the same reason: the remembered row is the middle one.
func startupRows() []Session {
	return []Session{runningSess("s-1"), runningSess("s-2"), runningSess("s-3")}
}

// TestSwitcherRestoresTheRememberedSelectionOnLoad is the restore half of the
// selection-memory feature: the row the user was last on comes back, by NAME,
// on the first load after a switcher switch and on the first load at startup.
//
// Rows (a) and (f) are the load-bearing TDD rows — they exercise the restore
// itself and must fail against code that only ever previews row 0. Row (h) is
// the second one: it pins the `m.ready` half of the consumption guard, which a
// restore built for the switcher path alone would not have (ready is always
// true by the time a switch can happen). The rest pin what must NOT change:
//
//   - Row (b) is W3.T1's phantom name (a pending placeholder's guessed name
//     that applyScratchDirs later corrected, or a session since cleaned): an
//     absent remembered row is a plain fall back to row 0, never an error.
//   - Row (c) is the same fall back for a name that IS in the list but only as
//     a worktree-side fault row, which sessionIndex skips by design — a fault
//     row can never satisfy a lookup whose caller goes on to open a pane.
//   - Row (d) is applyFocusRequest's explicit open: it selects the clicked
//     session by name through its OWN arm (never previewOnNextLoad), and
//     still arms no preview — the pane it just opened already owns the
//     attach. Row (e) is applyScanResult's promotion, which restores nothing
//     at all: a restore living inside the previewOnNextLoad block would
//     wrongly inherit that exclusion too, so row (e)'s model is made `ready`
//     first, the same way row (d)'s is, so the guard added for row (h)
//     cannot be what makes it pass.
//   - Row (g) is `--open <name>`: an explicit startup pane request already
//     names the session to show, so New arms nothing and the load must leave
//     both the cursor and that open alone.
func TestSwitcherRestoresTheRememberedSelectionOnLoad(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "(a) a switcher switch restores the remembered row by name and previews it",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the switch must land on beta, got %q", m.repo)
				}

				m, armCmd := update(t, m, rememberedLoad(m.projectGen, betaRows()...))

				if s, ok := m.selected(); !ok || s.Name != "b-2" {
					t.Errorf("selected = %q (ok=%t), want the remembered %q", s.Name, ok, "b-2")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("the restoring load must still arm a preview for the row it restored")
				}
				if armed.name != "b-2" {
					t.Errorf("armed preview name = %q, want the remembered %q", armed.name, "b-2")
				}
			},
		},
		{
			name: "(b) a remembered name that is not in the list falls back to row 0",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-9"}
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)

				m, armCmd := update(t, m, rememberedLoad(m.projectGen, betaRows()...))

				if s, ok := m.selected(); !ok || s.Name != "b-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q", s.Name, ok, "b-1")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("an absent remembered row must still preview row 0")
				}
				if armed.name != "b-1" {
					t.Errorf("armed preview name = %q, want row 0's %q", armed.name, "b-1")
				}
			},
		},
		{
			name: "(c) a remembered name present only as a worktree-fault row falls back to row 0",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)

				m, armCmd := update(t, m, rememberedLoad(m.projectGen,
					runningSess("b-1"),
					faultSess("b-2", session.KindWorktreeMissing, "worktree directory no longer exists"),
					runningSess("b-3"),
				))

				if s, ok := m.selected(); !ok || s.Name != "b-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q", s.Name, ok, "b-1")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("a fault-row remembered name must still preview row 0")
				}
				if armed.name != "b-1" {
					t.Errorf("armed preview name = %q, want row 0's %q", armed.name, "b-1")
				}
			},
		},
		{
			name: "(d) a focus-request switch selects and remembers the focused session on its first load, arming no preview",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				m, focusCmd := update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the focus request must switch to beta, got %q", m.repo)
				}
				// The click's own explicit open is driven to completion for
				// TestSwitcherSwitchPreviewsTheFirstSessionOnceItsLoadLands's row-5
				// reason: while it is outstanding, schedulePreview's own refusal
				// would silence a wrongly-restored row just as well as a
				// correctly-unrestored one.
				attached, ok := firstPaneAttached(collectMsgsPreview(t, focusCmd))
				if !ok {
					t.Fatal("precondition: the focus request's own explicit open must dispatch a paneAttachedMsg")
				}
				m, _ = update(t, m, attached)

				m, tickCmd := update(t, m, rememberedLoad(m.projectGen,
					runningSess("b-1"), runningSess("beta-target"), runningSess("b-3"),
				))

				if s, ok := m.selected(); !ok || s.Name != "beta-target" {
					t.Errorf("selected = %q (ok=%t), want the clicked %q — a focus-request switch selects by name through its own arm, not the stale remembered %q or row 0", s.Name, ok, "beta-target", "b-2")
				}
				if got := m.rememberedSelection["/roots/beta"]; got != "beta-target" {
					t.Errorf("rememberedSelection[/roots/beta] = %q, want the clicked %q", got, "beta-target")
				}
				if got := m.dirtySelections["/roots/beta"]; got != "beta-target" {
					t.Errorf("dirtySelections[/roots/beta] = %q, want the clicked %q", got, "beta-target")
				}
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a focus-request switch must not arm a preview — its own explicit open already owns the attach: got %v", msgs)
				}
			},
		},
		{
			name: "(d2) a focus-request switch whose target is absent from the first load falls back to row 0 and remembers nothing new",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				m, focusCmd := update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the focus request must switch to beta, got %q", m.repo)
				}
				attached, ok := firstPaneAttached(collectMsgsPreview(t, focusCmd))
				if !ok {
					t.Fatal("precondition: the focus request's own explicit open must dispatch a paneAttachedMsg")
				}
				m, _ = update(t, m, attached)

				m, tickCmd := update(t, m, rememberedLoad(m.projectGen, betaRows()...))

				if s, ok := m.selected(); !ok || s.Name != "b-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q — the clicked target is absent from this load", s.Name, ok, "b-1")
				}
				if got := m.rememberedSelection["/roots/beta"]; got != "b-2" {
					t.Errorf("rememberedSelection[/roots/beta] = %q, want the untouched %q", got, "b-2")
				}
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a focus-request switch must not arm a preview: got %v", msgs)
				}
			},
		},
		{
			name: "(d3) a switcher switch after a focus-request switch does not inherit the focus arm",
			run: func(t *testing.T) {
				m, _, _ := switcherWiringModel(t)
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2", "/roots/alpha": "a-2"}
				m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
					return newFakePane("BETA-TARGET"), nil
				}
				m.rendezvous = map[string]focusRendezvous{"/roots/beta": {dir: "/rendezvous/beta"}}

				req := focusRequestMsg{
					found: true,
					request: dashmeta.Request{
						Name:        "beta-target",
						RequestedAt: m.deps.Now().Format(time.RFC3339),
					},
					dir: "/rendezvous/beta",
				}
				// Beta's own load is deliberately never delivered: the point of this
				// row is what a switcher switch does with no help from it, not what
				// happens once it lands.
				m, _ = update(t, m, req)
				if m.repo != "/roots/beta" {
					t.Fatalf("precondition: the focus request must switch to beta, got %q", m.repo)
				}
				// A click moves the focus right at request time (plan W3.T1 e),
				// and `p` is a list key: hand the keyboard back to the list first.
				m, _ = update(t, m, chord('\\'))

				m = openSwitcherOverlay(t, m)
				// The overlay's cursor never seeds from m.activeProject, and beta
				// (still active here) sorts first because it is unconditionally
				// awaiting-input in this fixture — so alpha is selected explicitly
				// to keep this row's intent, a real switch away from beta, working
				// regardless of where the tier sort (sortSwitcherProjects) places
				// either project.
				m = switcherSelectByPath(t, m, "/roots/alpha")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/alpha" {
					t.Fatalf("precondition: the switcher switch must land back on alpha, got %q", m.repo)
				}

				m, armCmd := update(t, m, rememberedLoad(m.projectGen,
					runningSess("a-1"), runningSess("a-2"), runningSess("a-3"),
				))

				if s, ok := m.selected(); !ok || s.Name != "a-2" {
					t.Errorf("selected = %q (ok=%t), want the switcher's own remembered %q, never the stale focus-request target %q", s.Name, ok, "a-2", "beta-target")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("the switcher's own restore must still arm a preview for the row it restored")
				}
				if armed.name != "a-2" {
					t.Errorf("armed preview name = %q, want the switcher's own remembered %q", armed.name, "a-2")
				}
			},
		},
		{
			name: "(e) applyScanResult's promotion restores nothing and previews nothing",
			run: func(t *testing.T) {
				d := testDeps()
				d.ActiveProjectIndex = -1
				m := New("/not-a-repo", time.Second, d)
				// Ready BEFORE the load: without it the guard row (h) pins would
				// be what suppresses the restore here, and this row would pass
				// against a promotion that does arm one.
				m.ready = true
				m.width, m.height = 120, 40
				m.sizeSessionList()
				m.rememberedSelection = map[string]string{"/roots/beta": "b-2"}

				m, _ = update(t, m, projectScanMsg{projects: scannedProjects("/roots/beta", "/roots/gamma")})
				if m.activeProjectPath() != "/roots/beta" {
					t.Fatalf("precondition: the promotion must land on the first project, got %q", m.activeProjectPath())
				}
				if m.previewOnNextLoad {
					t.Fatal("precondition: applyScanResult's promotion must not arm previewOnNextLoad itself")
				}

				m, tickCmd := update(t, m, rememberedLoad(m.projectGen, betaRows()...))

				if s, ok := m.selected(); !ok || s.Name != "b-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q — a promotion restores nothing", s.Name, ok, "b-1")
				}
				if msgs := collectMsgsPreview(t, tickCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("applyScanResult's promotion must not arm a preview: got %v", msgs)
				}
			},
		},
		{
			name: "(f) startup restores the remembered row on its first load and previews it",
			run: func(t *testing.T) {
				m := startupSelectionModel("s-2", "")
				if !m.previewOnNextLoad {
					t.Fatal("precondition: a remembered entry for the launch repo must arm previewOnNextLoad in New")
				}
				m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

				m, armCmd := update(t, m, rememberedLoad(m.projectGen, startupRows()...))

				if s, ok := m.selected(); !ok || s.Name != "s-2" {
					t.Errorf("selected = %q (ok=%t), want the remembered %q", s.Name, ok, "s-2")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("startup with a remembered row must preview it once the first load lands")
				}
				if armed.name != "s-2" {
					t.Errorf("armed preview name = %q, want the remembered %q", armed.name, "s-2")
				}
			},
		},
		{
			name: "(g) startup with InitialSessionName restores nothing — the explicit open owns the pane",
			run: func(t *testing.T) {
				m := startupSelectionModel("s-2", "s-1")
				if m.previewOnNextLoad {
					t.Fatal("precondition: InitialSessionName must leave previewOnNextLoad false in New")
				}
				m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

				m, cmd := update(t, m, rememberedLoad(m.projectGen, startupRows()...))

				if s, ok := m.selected(); !ok || s.Name != "s-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q — an `--open` startup restores nothing", s.Name, ok, "s-1")
				}
				if m.attachInFlight.name != "s-1" || m.attachInFlight.preview {
					t.Errorf("attachInFlight = %+v, want the startup pane's own explicit open for %q", m.attachInFlight, "s-1")
				}
				if msgs := collectMsgsPreview(t, cmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("an `--open` startup must not arm a preview: got %v", msgs)
				}
			},
		},
		{
			name: "(h) a first load before the first WindowSizeMsg leaves the arm set for the next one",
			run: func(t *testing.T) {
				m := startupSelectionModel("s-2", "")
				if !m.previewOnNextLoad || m.ready {
					t.Fatalf("precondition: want an armed, not-yet-ready Model, got armed=%t ready=%t",
						m.previewOnNextLoad, m.ready)
				}

				m, earlyCmd := update(t, m, rememberedLoad(m.projectGen, startupRows()...))

				if !m.previewOnNextLoad {
					t.Error("a load before the first WindowSizeMsg must leave the arm set for the next load")
				}
				if s, ok := m.selected(); !ok || s.Name != "s-1" {
					t.Errorf("selected = %q (ok=%t), want row 0's %q — nothing is restored at width 0", s.Name, ok, "s-1")
				}
				if msgs := collectMsgsPreview(t, earlyCmd); hasMsg[previewTimerMsg](msgs) {
					t.Errorf("a load at width 0 must not arm a preview: got %v", msgs)
				}

				m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
				m, armCmd := update(t, m, rememberedLoad(m.projectGen, startupRows()...))

				if s, ok := m.selected(); !ok || s.Name != "s-2" {
					t.Errorf("selected = %q (ok=%t), want the remembered %q once the size arrived", s.Name, ok, "s-2")
				}
				armed, ok := firstPreviewTimer(collectMsgsPreview(t, armCmd))
				if !ok {
					t.Fatal("the load after the first WindowSizeMsg must consume the arm and preview the restored row")
				}
				if armed.name != "s-2" {
					t.Errorf("armed preview name = %q, want the remembered %q", armed.name, "s-2")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// --- per-project open counts ---------------------------------------------------

// openSpy is the Deps.RecordProjectOpen fake for every row below.
//
// The mutex is not decoration: a tick flush runs inside a tea.Cmd and
// collectMsgs runs batch members on their own goroutines, so the write here and
// the test's read of it are genuinely concurrent under -race. And it never calls
// t.Fatal — a Goexit on a Cmd goroutine parks the collector forever instead of
// failing the test, so every row counts calls and asserts afterwards.
type openSpy struct {
	mu    sync.Mutex
	calls []string
}

func (s *openSpy) record(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, path)
	return nil
}

func (s *openSpy) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// countOpenFlushes counts the projectOpensFlushedMsg values in a collected
// batch, which is how "exactly ONE flush Cmd per tick" is asserted: one flush
// Cmd produces exactly one such message, so the message count IS the Cmd count.
// Counting the spy's calls alone cannot tell one Cmd writing three increments
// from three Cmds writing one each — and only the second shape can lose one.
func countOpenFlushes(msgs []tea.Msg) int {
	n := 0
	for _, msg := range msgs {
		if _, ok := msg.(projectOpensFlushedMsg); ok {
			n++
		}
	}
	return n
}

// projectOpensModel is the fixture the ordering rows need and
// switcherWiringModel cannot be: its beta is unconditionally awaiting input, so
// the awaiting tier decides row 0 there no matter what the counts say. Nothing
// here awaits, which is what lets an increment be OBSERVED in the row order.
//
// opens is what the load seam answers, copied on the way in so a row can assert
// against its own literal afterwards.
func projectOpensModel(t *testing.T, opens map[string]int) (Model, *openSpy) {
	t.Helper()
	spy := &openSpy{}
	d := testDeps()
	d.Projects = switcherWiringProjects()
	d.ActiveProjectIndex = 0
	d.LoadProjectOpens = func() map[string]int {
		seeded := make(map[string]int, len(opens))
		for path, n := range opens {
			seeded[path] = n
		}
		return seeded
	}
	d.RecordProjectOpen = spy.record
	m := New("/roots/alpha", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40
	m.sizeSessionList()
	return m, spy
}

// switcherRowPaths is the order the overlay is currently rendering, by project
// path — the observable the ordering rows assert on, since a row's index is
// exactly what a wrongly-wired counts map would change.
func switcherRowPaths(m Model) []string {
	items := m.switcher.list.Items()
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if it, ok := item.(switcherItem); ok {
			paths = append(paths, it.path)
		}
	}
	return paths
}

// TestProjectOpensLaunchProjectPath is the pure core of New's launch recording:
// the resolved project path when the active index is in range, and "" — record
// nothing — otherwise.
//
// The out-of-range rows are the load-bearing ones. They are where this function
// differs from selectionKeyFor, which falls back to the launch repo: a count
// keyed on a repo spelling no project entry carries would rank a project that
// is not in the switcher's list at all, and leave the real entry at zero.
func TestProjectOpensLaunchProjectPath(t *testing.T) {
	tests := []struct {
		name     string
		projects []project.Project
		active   int
		want     string
	}{
		{
			name: "no projects records nothing",
			want: "",
		},
		{
			name:     "a negative index records nothing",
			projects: []project.Project{{Path: "/repo"}},
			active:   -1,
			want:     "",
		},
		{
			name:     "an out-of-range positive index records nothing",
			projects: []project.Project{{Path: "/repo"}},
			active:   1,
			want:     "",
		},
		{
			name:     "an in-range index returns that project's resolved path",
			projects: []project.Project{{Path: "/repo/sub"}, {Path: "/other"}},
			active:   0,
			want:     "/repo/sub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchProjectPath(tt.projects, tt.active); got != tt.want {
				t.Errorf("launchProjectPath(%+v, %d) = %q, want %q", tt.projects, tt.active, got, tt.want)
			}
		})
	}
}

// TestProjectOpensNewSeedsAndRecordsTheLaunch is the startup half of the
// feature: New loads the counts ONCE through the seam and records the launch
// target on top of what it loaded.
//
// Row 3 is load-bearing twice over. It is the only row where the seam answers a
// count for the launch project, so it is what proves the launch open is an
// INCREMENT on the loaded value rather than a reset to 1 — and it pins that
// only the launch project is left pending, not every project the seam returned,
// which would rewrite the whole file on the first tick of every run.
//
// Row 4 is the launched-outside-any-repo case (index -1): there is no entry to
// count, and the loaded counts must survive untouched so the overlay still
// ranks the projects the first scan finds.
func TestProjectOpensNewSeedsAndRecordsTheLaunch(t *testing.T) {
	tests := []struct {
		name      string
		projects  []project.Project
		active    int
		load      func() map[string]int
		wantOpens map[string]int
		wantDirty map[string]int
	}{
		{
			name:      "no seam and no launch project leaves the tally empty",
			wantOpens: nil,
			wantDirty: nil,
		},
		{
			name:      "no seam still records the launch project",
			projects:  switcherWiringProjects(),
			active:    0,
			wantOpens: map[string]int{"/roots/alpha": 1},
			wantDirty: map[string]int{"/roots/alpha": 1},
		},
		{
			name:     "the launch project increments the count the seam loaded",
			projects: switcherWiringProjects(),
			active:   0,
			load: func() map[string]int {
				return map[string]int{"/roots/alpha": 3, "/roots/beta": 7}
			},
			wantOpens: map[string]int{"/roots/alpha": 4, "/roots/beta": 7},
			wantDirty: map[string]int{"/roots/alpha": 1},
		},
		{
			name:     "launched outside any repo records nothing and keeps the loaded counts",
			projects: switcherWiringProjects(),
			active:   -1,
			load: func() map[string]int {
				return map[string]int{"/roots/beta": 2}
			},
			wantOpens: map[string]int{"/roots/beta": 2},
			wantDirty: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loads := 0
			d := testDeps()
			d.Projects = tt.projects
			d.ActiveProjectIndex = tt.active
			if tt.load != nil {
				d.LoadProjectOpens = func() map[string]int {
					loads++
					return tt.load()
				}
			}

			m := New("/roots/alpha", time.Second, d)

			if tt.load != nil && loads != 1 {
				t.Errorf("LoadProjectOpens called %d times, want exactly 1", loads)
			}
			if !maps.Equal(m.projectOpens, tt.wantOpens) {
				t.Errorf("projectOpens = %#v, want %#v", m.projectOpens, tt.wantOpens)
			}
			if !maps.Equal(m.dirtyOpens, tt.wantDirty) {
				t.Errorf("dirtyOpens = %#v, want %#v", m.dirtyOpens, tt.wantDirty)
			}
		})
	}
}

// TestProjectOpensReachTheSwitcher is the wiring both call sites exist for: the
// counts the Model holds decide the overlay's row order, and an increment
// reorders it on the next open with no rescan in between — which is the whole
// point of holding the tally in memory rather than re-reading the state file.
//
// The seeded fixture is one where usage order and alphabetical order actively
// DISAGREE (gamma opened five times, alpha and beta never), so a call site that
// still passed an empty map would render alpha, beta, gamma and fail row 1
// rather than passing by coincidence.
//
// The m.projects assertion is the hazard this wave was designed around: the
// sorted order must never be written back, because sameProjectPaths compares
// that slice BY POSITION to decide whether an open overlay needs rebuilding —
// a usage-ordered m.projects would make every scan tick look like a reorder.
func TestProjectOpensReachTheSwitcher(t *testing.T) {
	m, _ := projectOpensModel(t, map[string]int{"/roots/gamma": 5})

	m = openSwitcherOverlay(t, m)
	want := []string{"/roots/gamma", "/roots/alpha", "/roots/beta"}
	if got := switcherRowPaths(m); !pathsEqual(got, want) {
		t.Errorf("overlay rows = %v, want %v (gamma most used, alpha opened at launch)", got, want)
	}
	if got, scan := projectPaths(m.projects), []string{"/roots/alpha", "/roots/beta", "/roots/gamma"}; !pathsEqual(got, scan) {
		t.Errorf("m.projects = %v, want the scan order %v left untouched", got, scan)
	}

	m = switcherSelectByPath(t, m, "/roots/beta")
	m, _ = update(t, m, keyEnter)
	if m.repo != "/roots/beta" {
		t.Fatalf("setup: the switch must land on beta, got %q", m.repo)
	}
	if got := m.projectOpens["/roots/beta"]; got != 1 {
		t.Fatalf("setup: beta's count = %d, want the switch to have recorded 1", got)
	}

	m = openSwitcherOverlay(t, m)
	want = []string{"/roots/gamma", "/roots/alpha", "/roots/beta"}
	if got := switcherRowPaths(m); !pathsEqual(got, want) {
		t.Errorf("overlay rows after one open of beta = %v, want %v (beta ties alpha, name breaks it)", got, want)
	}

	m = switcherSelectByPath(t, m, "/roots/beta")
	m, _ = update(t, m, keyEnter)
	if m.switcherOpen {
		t.Fatal("setup: re-selecting the active project must still close the overlay")
	}
	m.projectOpens["/roots/beta"] = 9

	m = openSwitcherOverlay(t, m)
	want = []string{"/roots/beta", "/roots/gamma", "/roots/alpha"}
	if got := switcherRowPaths(m); !pathsEqual(got, want) {
		t.Errorf("overlay rows = %v, want %v — the tally must reorder rows with no rescan", got, want)
	}
	if got, scan := projectPaths(m.projects), []string{"/roots/alpha", "/roots/beta", "/roots/gamma"}; !pathsEqual(got, scan) {
		t.Errorf("m.projects = %v, want the scan order %v left untouched", got, scan)
	}
}

// TestProjectOpensReachTheRefreshedSwitcher is the SECOND call site, and it
// needs its own row because openSwitcher and refreshSwitcher construct the
// component independently: a scan that lands under an open overlay rebuilds it,
// and a rebuild that forgot the counts would silently drop back to alphabetical
// order under the user's hands while the overlay is up.
func TestProjectOpensReachTheRefreshedSwitcher(t *testing.T) {
	m, _ := projectOpensModel(t, map[string]int{"/roots/gamma": 5})
	m = openSwitcherOverlay(t, m)

	m, _ = update(t, m, projectScanMsg{
		projects: scannedProjects("/roots/alpha", "/roots/beta", "/roots/gamma", "/roots/delta"),
	})

	if !m.switcherOpen {
		t.Fatal("setup: a scan result must not close the overlay")
	}
	want := []string{"/roots/gamma", "/roots/alpha", "/roots/beta", "/roots/delta"}
	if got := switcherRowPaths(m); !pathsEqual(got, want) {
		t.Errorf("rebuilt overlay rows = %v, want %v", got, want)
	}
}

// TestProjectOpensRecordsDeliberateActivationsOnly is the rule the counts mean
// anything at all under: an open is the user NAMING a project, and every other
// way the active project can change is not one.
//
// All three rows are load-bearing, and the last two are the ones a naive "bump
// it wherever the active project changes" implementation fails. Enter on the
// already-active project is documented as a no-op, so counting it would let a
// held-down Enter inflate the row the cursor already sits on; and
// applyScanResult's promotion of the first project is automatic reconciliation
// for an active project that vanished, so counting it would rank a project the
// user never chose.
func TestProjectOpensRecordsDeliberateActivationsOnly(t *testing.T) {
	tests := []struct {
		name      string
		act       func(t *testing.T) Model
		wantOpens map[string]int
		wantDirty map[string]int
	}{
		{
			name: "a switcher choice of another project is recorded",
			act: func(t *testing.T) Model {
				m, _ := projectOpensModel(t, nil)
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/beta")
				m, _ = update(t, m, keyEnter)
				if m.repo != "/roots/beta" {
					t.Fatalf("setup: the switch must land on beta, got %q", m.repo)
				}
				return m
			},
			wantOpens: map[string]int{"/roots/alpha": 1, "/roots/beta": 1},
			wantDirty: map[string]int{"/roots/alpha": 1, "/roots/beta": 1},
		},
		{
			name: "re-selecting the active project records nothing",
			act: func(t *testing.T) Model {
				m, _ := projectOpensModel(t, nil)
				m = openSwitcherOverlay(t, m)
				m = switcherSelectByPath(t, m, "/roots/alpha")
				m, cmd := update(t, m, keyEnter)
				if cmd != nil {
					t.Fatal("setup: re-selecting the active project must dispatch nothing")
				}
				return m
			},
			wantOpens: map[string]int{"/roots/alpha": 1},
			wantDirty: map[string]int{"/roots/alpha": 1},
		},
		{
			name: "the first-project fallback promotion records nothing",
			act: func(t *testing.T) Model {
				d := testDeps()
				d.ActiveProjectIndex = -1
				d.LoadProjectOpens = func() map[string]int { return map[string]int{"/roots/b": 4} }
				m := New("/not-a-repo", time.Second, d)
				if len(m.dirtyOpens) != 0 {
					t.Fatalf("setup: a launch outside any repo must record nothing, got %#v", m.dirtyOpens)
				}
				m, _ = update(t, m, projectScanMsg{projects: scannedProjects("/roots/a", "/roots/b")})
				if m.activeProjectPath() != "/roots/a" {
					t.Fatalf("setup: the promotion must land on the first project, got %q", m.activeProjectPath())
				}
				return m
			},
			wantOpens: map[string]int{"/roots/b": 4},
			wantDirty: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.act(t)

			if !maps.Equal(m.projectOpens, tt.wantOpens) {
				t.Errorf("projectOpens = %#v, want %#v", m.projectOpens, tt.wantOpens)
			}
			if !maps.Equal(m.dirtyOpens, tt.wantDirty) {
				t.Errorf("dirtyOpens = %#v, want %#v", m.dirtyOpens, tt.wantDirty)
			}
		})
	}
}

// TestProjectOpensFlushOnTick is the debounce: the 2s discovery tick is the only
// thing that writes, it writes through exactly one Cmd, and it empties the
// pending set so the next tick writes nothing.
//
// Two opens of ALPHA inside the one tick is the load-bearing part. The seam adds
// one per call, so the pending tally has to produce TWO calls for it — a
// per-key set that wrote each dirty project once would leave the file one behind
// the in-memory count, permanently, with nothing to ever re-dirty it.
func TestProjectOpensFlushOnTick(t *testing.T) {
	m, spy := projectOpensModel(t, nil)

	m = openSwitcherOverlay(t, m)
	m = switcherSelectByPath(t, m, "/roots/beta")
	m, _ = update(t, m, keyEnter)
	m = openSwitcherOverlay(t, m)
	m = switcherSelectByPath(t, m, "/roots/alpha")
	m, _ = update(t, m, keyEnter)
	if got, want := m.dirtyOpens, map[string]int{"/roots/alpha": 2, "/roots/beta": 1}; !maps.Equal(got, want) {
		t.Fatalf("setup: dirtyOpens = %#v, want %#v", got, want)
	}

	m, tick := update(t, m, tickMsg(time.Unix(1, 0)))
	if len(m.dirtyOpens) != 0 {
		t.Errorf("dirtyOpens = %#v, want the set emptied by the flush", m.dirtyOpens)
	}
	msgs := collectMsgsPreview(t, tick)
	if n := countOpenFlushes(msgs); n != 1 {
		t.Errorf("tick produced %d flush Cmds, want exactly 1", n)
	}
	calls := spy.snapshot()
	counted := map[string]int{}
	for _, path := range calls {
		counted[path]++
	}
	if want := map[string]int{"/roots/alpha": 2, "/roots/beta": 1}; !maps.Equal(counted, want) {
		t.Errorf("RecordProjectOpen calls = %v (tallied %#v), want %#v", calls, counted, want)
	}

	m, second := update(t, m, tickMsg(time.Unix(2, 0)))
	if n := countOpenFlushes(collectMsgsPreview(t, second)); n != 0 {
		t.Errorf("a second tick with nothing pending produced %d flush Cmds, want 0", n)
	}
	if got := spy.snapshot(); len(got) != len(calls) {
		t.Errorf("RecordProjectOpen calls = %v, want the first tick's %d and no more", got, len(calls))
	}
}

// TestProjectOpensTickWithNilSeamClearsTheSet covers the un-wired dashboard
// (every Model in this package that does not opt in, and any caller that builds
// Deps without the seam): nothing is written, and the pending set is cleared
// anyway rather than growing an entry per open that nothing will ever flush.
func TestProjectOpensTickWithNilSeamClearsTheSet(t *testing.T) {
	d := testDeps()
	d.Projects = switcherWiringProjects()
	d.ActiveProjectIndex = 0
	m := New("/roots/alpha", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40
	if m.deps.RecordProjectOpen != nil {
		t.Fatal("setup: testDeps must leave RecordProjectOpen nil")
	}
	if len(m.dirtyOpens) != 1 {
		t.Fatalf("setup: dirtyOpens = %#v, want the launch project pending", m.dirtyOpens)
	}

	m, tick := update(t, m, tickMsg(time.Unix(1, 0)))
	if len(m.dirtyOpens) != 0 {
		t.Errorf("dirtyOpens = %#v, want the set cleared even with no seam", m.dirtyOpens)
	}
	if n := countOpenFlushes(collectMsgsPreview(t, tick)); n != 0 {
		t.Errorf("a nil seam produced %d flush Cmds, want 0", n)
	}
}

// TestProjectOpensQuitFlushesInline pins that the quit path writes INLINE, not
// through a Cmd: tea.Quit is returned beside it, so a Cmd may never run, and
// quitting right after switching to a project is the shape that makes the
// just-opened project's count the one most worth saving. The assertion is
// deliberately made BEFORE the returned Cmd is touched — running it first would
// let a Cmd-based implementation pass.
//
// The multi-increment row is the same rule the tick flush has: a pending count
// of two is two calls, because the seam adds one per call.
func TestProjectOpensQuitFlushesInline(t *testing.T) {
	tests := []struct {
		name  string
		dirty map[string]int
		want  map[string]int
	}{
		{
			name:  "every pending increment is written before the quit",
			dirty: map[string]int{"/roots/alpha": 2, "/roots/beta": 1},
			want:  map[string]int{"/roots/alpha": 2, "/roots/beta": 1},
		},
		{
			name:  "an empty set writes nothing",
			dirty: nil,
			want:  map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(runningSess("a"))
			spy := &openSpy{}
			m.deps.RecordProjectOpen = spy.record
			m.dirtyOpens = tt.dirty

			nm, cmd := m.requestQuit()

			counted := map[string]int{}
			for _, path := range spy.snapshot() {
				counted[path]++
			}
			if !maps.Equal(counted, tt.want) {
				t.Errorf("RecordProjectOpen calls = %#v, want %#v synchronous calls", counted, tt.want)
			}
			if got, ok := nm.(Model); !ok || !got.quitting {
				t.Errorf("requestQuit must still quit (quitting=%v)", ok && got.quitting)
			}
			if cmd == nil {
				t.Error("requestQuit returned no Cmd, want tea.Quit")
			}
		})
	}
}

// TestProjectOpensQuitWithNilSeamIsInert is the un-wired dashboard on the quit
// path: no seam, no write, no panic.
func TestProjectOpensQuitWithNilSeamIsInert(t *testing.T) {
	m := testModel(runningSess("a"))
	m.dirtyOpens = map[string]int{"/repo": 1}

	nm, cmd := m.requestQuit()

	if got, ok := nm.(Model); !ok || !got.quitting {
		t.Errorf("requestQuit must still quit (quitting=%v)", ok && got.quitting)
	}
	if cmd == nil {
		t.Error("requestQuit returned no Cmd, want tea.Quit")
	}
}
