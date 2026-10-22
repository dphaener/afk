package ui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/cursor"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/spice"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/termpane"
)

// --- fake pane (the OpenHostPane seam) ---

// fakePane is an in-memory paneHandle: it records the input/close calls the
// dashboard makes and lets tests drive redraw/exit through its channels,
// without a real PTY, emulator, or child process.
type fakePane struct {
	render  string
	changed chan struct{}
	done    chan struct{}
	err     error
	closed  int
	keys    []termpane.KeyEvent
	texts   []string
	pastes  []string
	mice    []termpane.MouseEvent
	resizes [][2]int

	// sizeCols/sizeRows are what Size() reports, defaulted to 80x24 by
	// newFakePane so every existing test keeps its previous behaviour. They are
	// settable because the mouse bounds check takes the per-axis minimum of the
	// rendered region and the emulator's own size (mouseBounds): a hardcoded
	// 80x24 stub clips a bounds assertion at a larger terminal, so the test
	// would pass on the STUB's limit rather than on the geometry under test.
	sizeCols, sizeRows int

	// cursor is what Cursor() reports, defaulted by newFakePane to what a
	// fresh real *termpane.Pane reports (see W1.T1's Cursor(): visible,
	// block, blinking, at the origin) so every existing test — none of which
	// sets this field — keeps its previous behaviour.
	cursor termpane.CursorState
}

func newFakePane(render string) *fakePane {
	return &fakePane{
		render:   render,
		changed:  make(chan struct{}, 1),
		done:     make(chan struct{}),
		sizeCols: 80,
		sizeRows: 24,
		cursor:   termpane.CursorState{Visible: true, Shape: termpane.CursorBlock, Blink: true},
	}
}

func (f *fakePane) Render() string                  { return f.render }
func (f *fakePane) SendKey(k termpane.KeyEvent)     { f.keys = append(f.keys, k) }
func (f *fakePane) SendText(s string)               { f.texts = append(f.texts, s) }
func (f *fakePane) Paste(s string)                  { f.pastes = append(f.pastes, s) }
func (f *fakePane) SendMouse(m termpane.MouseEvent) { f.mice = append(f.mice, m) }
func (f *fakePane) Resize(c, r int) error           { f.resizes = append(f.resizes, [2]int{c, r}); return nil }
func (f *fakePane) Size() (int, int)                { return f.sizeCols, f.sizeRows }
func (f *fakePane) Changed() <-chan struct{}        { return f.changed }
func (f *fakePane) Done() <-chan struct{}           { return f.done }
func (f *fakePane) Err() error                      { return f.err }
func (f *fakePane) Close() error                    { f.closed++; return nil }
func (f *fakePane) Cursor() termpane.CursorState    { return f.cursor }

// --- helpers ---

// testDeps returns a fully-stubbed Deps so tests never shell out to real
// git/gs, even indirectly (e.g. loadCmd's per-session diff stats, stackCmd's
// detect/log-short). Individual tests override whichever field they're
// exercising.
func testDeps() Deps {
	return Deps{
		Discover:       func(string) ([]discover.SessionState, error) { return nil, nil },
		ReadTail:       func(string, int) []string { return nil },
		SpawnHost:      func(string, string) (session.HostProcess, error) { return session.HostProcess{}, nil },
		WaitHostReady:  func(string, string) error { return nil },
		Now:            func() time.Time { return time.Unix(1_000_000, 0) },
		SpiceDetect:    func(string) bool { return false },
		SpiceLogShort:  func(string) ([]spice.Branch, error) { return nil, nil },
		PlainBranches:  func(string) ([]string, error) { return nil, nil },
		PlainTrunk:     func(string) (string, error) { return "main", nil },
		ResolveBaseRef: resolveQualified,
		DiffFull:       func(string, string) (string, error) { return "", nil },
		DiffNumstat:    func(string, string) (string, error) { return "", nil },
		CleanSession: func(string, string, string, bool, session.Kind, session.CleanOptions) (session.CleanResult, error) {
			return session.CleanResult{}, nil
		},
		BranchMerged: func(string, string, string) (bool, string) { return false, "main" },
		WriteLabel:   func(string, string, string) error { return nil },
	}
}

func testModel(sessions ...Session) Model {
	m := New("/repo", time.Second, testDeps())
	m.ready = true
	m.width = 120
	m.height = 40
	// sizeSessionList before setSessions so the list has a real page size when
	// the items land: list.Select divides by Paginator.PerPage, and a 0x0 list
	// pages one row at a time, which is not the geometry the dashboard renders.
	m.sizeSessionList()
	m.setSessions(sessions)
	return m
}

func sampleState(name string) discover.SessionState {
	return discover.SessionState{
		Name:         name,
		WorktreePath: "/wt/" + name,
		Branch:       name,
		BaseBranch:   "main",
		Phase:        state.PhaseExecuting,
		UpdatedAt:    "2026-07-10T00:00:00Z",
		Progress:     discover.Progress{Total: 4, Complete: 2},
	}
}

func sess(name string) Session { return Session{SessionState: sampleState(name)} }

// namedIndex is the position of the row called name in m.sessions, a fault
// row included. The list is kept in the project view's screen order (T16.4:
// Needs you, Working, Done), so a test that puts rows of different groups in
// a list finds them by name rather than by the order it gave them in.
func namedIndex(t *testing.T, m Model, name string) int {
	t.Helper()
	for i := range m.sessions {
		if m.sessions[i].Name == name {
			return i
		}
	}
	t.Fatalf("no row named %q", name)
	return -1
}

// staleSess is the row internal/discover produces for a `.afk/{name}/`
// whose status.json its parser rejected: every status-derived field zeroed,
// StatusStale set, and KindStaleStatus — a kind internal/session does NOT
// necessarily agree with for the same file (see internal/worktree's package
// doc), which is what keeps it out of the clean-target identity match.
func staleSess(name string) Session {
	return Session{SessionState: discover.SessionState{
		Name:         name,
		WorktreePath: "/wt/" + name,
		Kind:         session.KindStaleStatus,
		StatusStale:  true,
		StaleReason:  state.StaleUnparseable,
	}}
}

// strandedSess is the row internal/discover produces for a `.afk/{name}/`
// whose carrying worktree is checked out on another branch — a standalone
// /afk:go session in the primary checkout being the common case.
//
// Every status-derived field is populated, Branch included, because that is the
// point of the kind: unlike a worktree fault this is an ORDINARY row with a
// live lifecycle, and its WorktreePath is the user's own checkout. The Branch
// here is the one status.json names, not the carrier's checked-out branch, and
// it is deliberately non-empty: session.Discover's own entry for the same
// directory carries Branch "", so a test that leaned on an empty Branch to
// tell the two apart would prove nothing about the kind.
func strandedSess(name string) Session {
	return Session{SessionState: discover.SessionState{
		Name:         name,
		WorktreePath: "/repo",
		Branch:       name,
		BaseBranch:   "main",
		Phase:        state.PhaseDone,
		Kind:         session.KindStatusWithoutWorktree,
		Reason:       "status file lives in a worktree checked out on another branch",
		HostRunning:  true,
		HostPID:      4242,
	}}
}

// cleanIDByName is the (name, KindHealthy) key, for the many tests that mark a
// HEALTHY row's clean in flight and then assert the set recorded it. It is a
// test helper and not a production one on purpose: no code path keys the
// in-flight set by name any more (see Model.cleaningByName), because a
// pane-capable row can now carry a non-healthy clean kind. focusrequest_test.go
// marks its fixtures with it too.
func cleanIDByName(name string) cleanID {
	return cleanID{name: name, kind: session.KindHealthy}
}

func keyRune(r rune) tea.KeyMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// assertConfirmOnly is what "entering the clean modal cleans nothing yet" has
// to be spelled as now that `c` returns a Cmd: the ancestry check behind the
// `d` offer runs off the event loop, so a non-nil Cmd here is expected and the
// property worth pinning is what it PRODUCES. Running it is hermetic —
// testDeps' BranchMerged fake touches no repository.
func assertConfirmOnly(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if msg := cmd(); !isCleanMergeState(msg) {
		t.Fatalf("entering confirm mode dispatched %T (%+v), want at most the merge-state request", msg, msg)
	}
}

func isCleanMergeState(msg tea.Msg) bool {
	_, ok := msg.(cleanMergeStateMsg)
	return ok
}

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
)

func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	nm, cmd := m.Update(msg)
	got, ok := nm.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", nm)
	}
	return got, cmd
}

// --- navigation ---
//
// TestMoveCursorClamping moved to sessionlist_test.go's
// TestSessionListWrapsSelection when the cursor field became a list.Model:
// its six rows were reworked into wrap expectations (from the original
// clamp expectations), plus the paging gestures the windowed list added.

// --- window size ---

func TestWindowSizeSetsDims(t *testing.T) {
	m := New("/repo", time.Second, testDeps())
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 200, Height: 50})
	if m.width != 200 || m.height != 50 || !m.ready {
		t.Fatalf("got width=%d height=%d ready=%v", m.width, m.height, m.ready)
	}
}

// --- quit ---

func TestQuit(t *testing.T) {
	for _, k := range []tea.KeyMsg{keyRune('q'), tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}} {
		m := testModel(sess("a"))
		got, cmd := pressQuitKey(t, m, k)
		if !got.quitting {
			t.Fatalf("%v: quitting not set", k)
		}
		if cmd == nil {
			t.Fatalf("%v: expected tea.Quit cmd, got nil", k)
		}
	}
}

// --- discovery result folding ---

func TestApplyLoadedClampsCursor(t *testing.T) {
	m := testModel(sess("a"), sess("b"), sess("c"))
	m.selectSessionIndex(2)
	// New result has only one session; cursor must clamp to 0.
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("only")}, at: time.Unix(5, 0)})
	if len(m.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(m.sessions))
	}
	if m.listSelection() != 0 {
		t.Fatalf("cursor = %d, want 0", m.listSelection())
	}
	if m.lastLoad != time.Unix(5, 0) {
		t.Fatalf("lastLoad not recorded")
	}
	if m.loadErr != "" {
		t.Fatalf("loadErr = %q, want empty", m.loadErr)
	}
}

func TestApplyLoadedEmptyList(t *testing.T) {
	m := testModel(sess("a"), sess("b"))
	m.selectSessionIndex(1)
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: nil, at: time.Unix(5, 0)})
	if len(m.sessions) != 0 {
		t.Fatalf("sessions = %d, want 0", len(m.sessions))
	}
	if m.listSelection() != 0 {
		t.Fatalf("cursor = %d, want 0", m.listSelection())
	}
	if _, ok := m.selected(); ok {
		t.Fatalf("selected() should be false with empty list")
	}
}

func TestApplyLoadedErrorKeepsSessions(t *testing.T) {
	m := testModel(sess("a"), sess("b"))
	m.selectSessionIndex(1)
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, err: errors.New("git blew up"), at: time.Unix(7, 0)})
	if len(m.sessions) != 2 {
		t.Fatalf("sessions dropped on error: got %d, want 2", len(m.sessions))
	}
	if m.listSelection() != 1 {
		t.Fatalf("cursor moved on error: got %d, want 1", m.listSelection())
	}
	if !strings.Contains(m.loadErr, "git blew up") {
		t.Fatalf("loadErr = %q, want to contain the error", m.loadErr)
	}
}

// TestDiscoveryErrorNotifiesOncePerFailure is the transition rule. Discovery
// re-runs every 2s and the error has no sticky surface left, so the only
// choices are "notify on the transition" and "toast forever"; this pins the
// first.
//
// The clearStatus between the two identical failing passes is the whole point
// of the test and not incidental setup. Without it the second pass would be
// suppressed by notify's own identical-text rule (notification.go) and the
// test would prove nothing about notifyDiscoveryError's own tracking — and a
// cleared status is exactly the state a user reaches by pressing any key, or
// by opening a modal, while discovery is down.
//
// The last two stanzas are the other half of the contract: a CHANGED error is
// a new fact and must be reported, and so is the same error returning after a
// pass that succeeded.
func TestDiscoveryErrorNotifiesOncePerFailure(t *testing.T) {
	m := testModel(sess("a"))
	fail := func(m Model, text string, at int64) Model {
		t.Helper()
		nm, _ := update(t, m, sessionsLoadedMsg{gen: m.projectGen, err: errors.New(text), at: time.Unix(at, 0)})
		return nm
	}

	m = fail(m, "git worktree list: exit status 128", 1)
	if !strings.Contains(m.status, "git worktree list") || m.statusKind != notifyError {
		t.Fatalf("status = %q kind = %v, want the discovery failure raised as an error", m.status, m.statusKind)
	}

	m.clearStatus()
	m = fail(m, "git worktree list: exit status 128", 3)
	if m.status != "" {
		t.Fatalf("status = %q, want silence on a repeat of the same failing pass", m.status)
	}

	m = fail(m, "git worktree list: exit status 129", 5)
	if !strings.Contains(m.status, "129") {
		t.Fatalf("status = %q, want a changed error text reported", m.status)
	}

	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("a")}, at: time.Unix(7, 0)})
	m.clearStatus()
	m = fail(m, "git worktree list: exit status 129", 9)
	if !strings.Contains(m.status, "129") {
		t.Fatalf("status = %q, want the same error reported again after discovery recovered", m.status)
	}
}

// TestBranchListingFailureNotifies pins the genuine-failure half of
// notifyBranchesError's disambiguation: plainBranchesCmd's fetch itself
// erroring (stack.go) leaves msg.branches at its nil zero value, which is
// the one observable difference from a trunk-only failure — see
// TestTrunkResolutionFailureDoesNotNotifyAsError for the other half.
func TestBranchListingFailureNotifies(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, plainBranchesLoadedMsg{err: errors.New("git for-each-ref: exit status 128")})
	if !strings.Contains(m.status, "exit status 128") || m.statusKind != notifyError {
		t.Fatalf("status = %q kind = %v, want the branch-listing failure raised as an error", m.status, m.statusKind)
	}
}

// TestTrunkResolutionFailureDoesNotNotifyAsError covers the plan's called-out
// case: a repo with no origin/HEAD and no main/master fails trunk resolution
// on every startup with branches fully intact — nothing is actually broken,
// so per notifyBranchesError this stays SILENT: no status, nothing handed to
// the arbiter, no suppression armed. The zero-value kind is notifyInfo, so
// asserting only "not an error" would pass on a fresh model whatever the
// handler did; the positive assertions below are what make the test bite.
func TestTrunkResolutionFailureDoesNotNotifyAsError(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, plainBranchesLoadedMsg{
		branches: []string{"develop", "feature-x"},
		err:      errors.New("could not determine a trunk branch"),
	})
	if m.statusKind == notifyError {
		t.Fatalf("status = %q kind = %v, want no error notification for a trunk-only failure with branches intact", m.status, m.statusKind)
	}
	if m.status != "" {
		t.Fatalf("status = %q, want silence for a trunk-only failure with branches intact", m.status)
	}
	if len(m.pendingBackground) != 0 {
		t.Fatalf("pending = %+v, want nothing handed to the arbiter for a trunk-only failure", m.pendingBackground)
	}
	if m.notifiedBranchesErr != "" {
		t.Fatalf("notifiedBranchesErr = %q, want no suppression armed for a failure that was never raised", m.notifiedBranchesErr)
	}
}

// TestStackErrorNotifiesAndKeepsStackTabRendering pins both halves of
// notifyStackError's contract: the failure raises an error-kind notification
// AND m.stack.Err is left populated, since stackPanel.Render (stack.go) still
// reads it for the Stack tab — this adds a notification, it does not move
// the existing rendering.
func TestStackErrorNotifiesAndKeepsStackTabRendering(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, stackLoadedMsg{stack: stackState{Detected: true, Err: "gs log short --json: exit status 1"}})
	if !strings.Contains(m.status, "exit status 1") || m.statusKind != notifyError {
		t.Fatalf("status = %q kind = %v, want the stack failure raised as an error", m.status, m.statusKind)
	}
	if m.stack.Err == "" {
		t.Fatalf("m.stack.Err emptied by the notification path, want it kept for the Stack tab")
	}
}

// TestStackErrorNotifiesOncePerFailure is notifyDiscoveryError's
// one-per-distinct-failure rule, pinned for the stack path: stackCmd re-runs
// every 2s tick via projectCmds, so ten consecutive failing passes with
// identical text must produce exactly one notification. clearStatus between
// passes is what proves notifiedStackErr's own tracking rather than notify's
// identical-text skip (notification.go) — the same reason
// TestDiscoveryErrorNotifiesOncePerFailure clears between passes.
func TestStackErrorNotifiesOncePerFailure(t *testing.T) {
	m := testModel(sess("a"))
	fail := stackState{Detected: true, Err: "gs log short --json: exit status 1"}

	m, _ = update(t, m, stackLoadedMsg{stack: fail})
	if !strings.Contains(m.status, "exit status 1") {
		t.Fatalf("status = %q, want the first failing pass to notify", m.status)
	}

	for i := 0; i < 9; i++ {
		m.clearStatus()
		m, _ = update(t, m, stackLoadedMsg{stack: fail})
		if m.status != "" {
			t.Fatalf("pass %d: status = %q, want silence on a repeat of the same failing pass", i+2, m.status)
		}
	}
}

// TestSwitchProjectResetsStackAndBranchesSuppression: a stale suppression
// from the outgoing project's stack failure must not swallow the incoming
// project's first report of the identical text — switchProject already
// resets notifiedLoadErr for the same reason, and notifiedStackErr /
// notifiedBranchesErr are reset alongside it.
func TestSwitchProjectResetsStackAndBranchesSuppression(t *testing.T) {
	d := testDeps()
	d.Projects = scannedProjects("/roots/a", "/roots/b")
	d.ActiveProjectIndex = 0
	m := New("/roots/a", time.Second, d)
	fail := stackState{Detected: true, Err: "gs log short --json: exit status 1"}

	m, _ = update(t, m, stackLoadedMsg{stack: fail})
	if !strings.Contains(m.status, "exit status 1") {
		t.Fatalf("status = %q, want the first failing pass on project a to notify", m.status)
	}
	m.clearStatus()

	m, _ = m.switchProject("/roots/b")
	m, _ = update(t, m, stackLoadedMsg{stack: fail})
	if !strings.Contains(m.status, "exit status 1") {
		t.Fatalf("status = %q, want the identical failure reported again on the incoming project", m.status)
	}
}

// TestScanErrorNotifiesOncePerFailure is notifyDiscoveryError's
// one-per-distinct-failure rule, pinned for the project-scan config path:
// scanTickMsg re-drives applyProjectScan every scan interval, so a config file
// that stays malformed must notify once, not on every pass. clearStatus
// between passes is what proves notifiedScanErr's own tracking rather than
// notify's identical-text skip (notification.go) — the same reason
// TestDiscoveryErrorNotifiesOncePerFailure clears between passes. The last two
// stanzas pin the other half: a changed error text is a new fact and must be
// reported, and so is the same error returning after a scan that succeeded.
func TestScanErrorNotifiesOncePerFailure(t *testing.T) {
	m := testModel(sess("a"))
	fail := func(m Model, text string) Model {
		t.Helper()
		nm, _ := update(t, m, projectScanMsg{err: errors.New(text)})
		return nm
	}

	m = fail(m, "config.toml: dash.max_depth is 0; use 1 or more")
	if !strings.Contains(m.status, "dash.max_depth") || m.statusKind != notifyError {
		t.Fatalf("status = %q kind = %v, want the scan failure raised as an error", m.status, m.statusKind)
	}

	m.clearStatus()
	m = fail(m, "config.toml: dash.max_depth is 0; use 1 or more")
	if m.status != "" {
		t.Fatalf("status = %q, want silence on a repeat of the same failing pass", m.status)
	}

	m = fail(m, "config.toml: dash.scan_interval is 1ms, which is too short")
	if !strings.Contains(m.status, "dash.scan_interval") {
		t.Fatalf("status = %q, want a changed error text reported", m.status)
	}

	m, _ = update(t, m, projectScanMsg{})
	m.clearStatus()
	m = fail(m, "config.toml: dash.scan_interval is 1ms, which is too short")
	if !strings.Contains(m.status, "dash.scan_interval") {
		t.Fatalf("status = %q, want the same error reported again after a successful scan", m.status)
	}
}

// --- ctrl+t / attach + restart ---

// runningSess is a session whose host is live: the attach-straight-away case.
// HostPID is populated because that is what a real host.json-backed session
// looks like, and the dead-PID correction keys on it.
func runningSess(name string) Session {
	s := sess(name)
	s.HostRunning = true
	s.HostPID = 4242
	return s
}

// TestListFocusCtrlTIsInert is row (a) of the W2.T1 key table: with the list
// focused, ctrl+t does nothing at all — no Cmd, and not one field of focus,
// view, pane or dial state moves. The user's model is that the list cannot
// control the right side "even if the overview pane is currently focused on
// the right side", and ctrl+t is a right-side key.
//
// Every row is a state where the OLD ctrl+t did something from the list, and
// each is load-bearing for the meaning it used to carry: attach and restart
// (running/stopped rows with no pane), hide (a drawn pane), reveal (a hidden
// pane), replace (the cursor off the paned row), and claim (a preview dial in
// flight).
func TestListFocusCtrlTIsInert(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) Model
	}{
		{"no selection", func(t *testing.T) Model { return testModel() }},
		{"running row, no pane", func(t *testing.T) Model { return testModel(runningSess("live")) }},
		{"stopped row, no pane", func(t *testing.T) Model { return testModel(stoppedSess("live")) }},
		{"pane drawn", func(t *testing.T) Model {
			m := testModel(runningSess("live"))
			m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "live"})
			return m
		}},
		{"pane behind the overview", func(t *testing.T) Model {
			m := testModel(runningSess("live"))
			m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "live"})
			m.view = viewOverview
			return m
		}},
		{"cursor off the paned row", func(t *testing.T) Model {
			m := testModel(runningSess("a"), runningSess("b"))
			m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "a"})
			m.selectSessionIndex(1)
			return m
		}},
		{"preview dial in flight", func(t *testing.T) Model {
			m := testModel(runningSess("live"))
			m, dial := update(t, m, previewTimerMsg{gen: m.previewGen, name: "live"})
			if dial == nil {
				t.Fatal("setup: the preview must dispatch its dial")
			}
			return m
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				t.Error("ctrl+t from the list opened a pane")
				return newFakePane("unexpected"), nil
			}
			m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
				t.Error("ctrl+t from the list spawned a host")
				return session.HostProcess{}, nil
			}
			if m.focus != focusList {
				t.Fatalf("setup: focus = %v, want the list", m.focus)
			}
			before := m

			// Bare ctrl+t: the key itself must stay inert on the list. The
			// leader's t row is dimmed there (leader tests cover that).
			got, cmd := update(t, m, keyCtrlT)
			if cmd != nil {
				t.Fatalf("ctrl+t from the list returned a Cmd (%T)", cmd())
			}
			if got.focus != before.focus || got.view != before.view ||
				got.pane != before.pane || got.paneName != before.paneName ||
				got.attachInFlight != before.attachInFlight || got.previewGen != before.previewGen ||
				got.status != before.status {
				t.Fatalf("ctrl+t from the list changed state:\n before focus=%v view=%v pane=%q inFlight=%+v gen=%d status=%q\n after  focus=%v view=%v pane=%q inFlight=%+v gen=%d status=%q",
					before.focus, before.view, before.paneName, before.attachInFlight, before.previewGen, before.status,
					got.focus, got.view, got.paneName, got.attachInFlight, got.previewGen, got.status)
			}
		})
	}
}

// TestEnterWithNoPaneOpensAndFocusesRunningSelectedSession is row (a)'s
// connect: enter on a HostRunning row with no pane dials it through
// openPaneFor exactly once and moves the focus right at the keypress, and the
// arriving pane owns the keyboard. The focus moving at the KEYPRESS is the
// load-bearing half — waiting for the arrival is what let typed keys hit list
// bindings during the dial.
func TestEnterWithNoPaneOpensAndFocusesRunningSelectedSession(t *testing.T) {
	m := testModel(runningSess("live"))
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		t.Error("enter on a running session must not spawn a host")
		return session.HostProcess{}, nil
	}
	fp := newFakePane("LIVE")
	opens := 0
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		opens++
		return fp, nil
	}

	got, cmd := update(t, m, keyEnter)
	if cmd == nil {
		t.Fatalf("enter on a running session should dispatch the attach cmd")
	}
	if got.focus != focusRight || got.view != viewAgent {
		t.Fatalf("focus/view = %v/%v at the keypress, want right/agent", got.focus, got.view)
	}
	if got.attachInFlight.name != "live" || got.attachInFlight.preview {
		t.Fatalf("attachInFlight = %+v, want an explicit dial for live", got.attachInFlight)
	}
	attached := drainForPaneAttached(t, cmd)
	if !attached.focusOnArrival {
		t.Fatal("enter's dial must carry focusOnArrival — the pane lands focused")
	}
	got, _ = update(t, got, attached)
	if got.pane != fp || got.paneName != "live" || !got.paneOwnsKeyboard() {
		t.Fatalf("pane=%v name=%q focused=%v, want live's pane installed and focused",
			got.pane == fp, got.paneName, got.paneOwnsKeyboard())
	}
	if opens != 1 {
		t.Fatalf("OpenHostPane called %d times, want exactly 1", opens)
	}
	if s, ok := got.selected(); !ok || s.Name != "live" {
		t.Fatalf("selected = %q (ok=%v), want the list to retain live", s.Name, ok)
	}
}

// TestToggleOnStaleSessionWithLiveHostStillAttaches locks in that attach keys on
// HostRunning alone: a stale status.json does not block an attach, because the
// host is found via host.json, independently of status.json.
func TestToggleOnStaleSessionWithLiveHostStillAttaches(t *testing.T) {
	s := runningSess("stale-but-live")
	s.StatusStale = true
	s.StaleReason = state.StaleUnparseable
	m := testModel(s)
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		t.Fatal("a stale session with a live host must be attached, not restarted")
		return session.HostProcess{}, nil
	}

	_, cmd := update(t, m, keyEnter)
	if cmd == nil {
		t.Fatalf("enter on a stale session with a live host should still attach")
	}
}

// TestRestartKeyFromTheListRestartsAStoppedSession walks the whole two-step
// restart chain from `r` on the session list: spawn the host, then the open
// hostRestartedMsg chains into. The keyboard stays on the list — `r` is a
// restart, not a focus gesture — so both legs carry selectionBound rather than
// focusOnArrival, and the restarted pane is SHOWN in the agent view with the
// list still owning input.
func TestRestartKeyFromTheListRestartsAStoppedSession(t *testing.T) {
	var gotName, gotWorktree string
	m := testModel(sess("stopped")) // HostRunning is false
	m.deps.SpawnHost = func(name, worktree string) (session.HostProcess, error) {
		gotName, gotWorktree = name, worktree
		return session.HostProcess{PID: 4242}, nil
	}
	fp := newFakePane("RESTARTED")
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return fp, nil }

	afterKey, cmd := pressAction(t, m, keyRune('r'))
	if cmd == nil {
		t.Fatalf("r on a stopped session should dispatch the restart cmd")
	}
	if !strings.Contains(afterKey.status, "restarting stopped") {
		t.Fatalf("status = %q, want restarting notice while the spawn runs", afterKey.status)
	}
	if afterKey.focus != focusList {
		t.Fatalf("focus = %v at the keypress, want the list", afterKey.focus)
	}

	msg := cmd()
	hr, ok := msg.(hostRestartedMsg)
	if !ok {
		t.Fatalf("restart cmd produced %T, want hostRestartedMsg", msg)
	}
	if gotName != "stopped" || gotWorktree != "/wt/stopped" {
		t.Fatalf("SpawnHost called with (%q, %q), want (%q, %q)", gotName, gotWorktree, "stopped", "/wt/stopped")
	}
	if hr.err != nil {
		t.Fatalf("hostRestartedMsg.err = %v, want nil", hr.err)
	}
	if hr.focusOnArrival || !hr.selectionBound {
		t.Fatalf("list r's restart = focusOnArrival:%v selectionBound:%v, want false/true", hr.focusOnArrival, hr.selectionBound)
	}

	afterSpawn, attachCmd := update(t, afterKey, hr)
	if attachCmd == nil {
		t.Fatalf("a successful restart should chain into the attach cmd")
	}
	if !strings.Contains(afterSpawn.status, "restarted stopped") {
		t.Fatalf("status = %q, want the restarted notice", afterSpawn.status)
	}
	attached := drainForPaneAttached(t, attachCmd)
	if attached.focusOnArrival || !attached.selectionBound {
		t.Fatalf("chained attach = focusOnArrival:%v selectionBound:%v, want false/true", attached.focusOnArrival, attached.selectionBound)
	}
	afterSpawn, _ = update(t, afterSpawn, attached)
	if afterSpawn.pane != fp || afterSpawn.paneName != "stopped" || !afterSpawn.paneForSelection() || afterSpawn.focus != focusList {
		t.Fatalf("pane=%v name=%q connected=%v focus=%v, want stopped's agent installed for the row, list-focused",
			afterSpawn.pane == fp, afterSpawn.paneName, afterSpawn.paneForSelection(), afterSpawn.focus)
	}
}

// TestRestartKeyArrivalForALeftRowIsDropped pins selectionBound's purpose: a
// list `r` whose row the cursor has left by the time the pane lands must not
// install a pane for an unselected row, holding the host's one client slot
// undrawn (plan W3.T1 a). The connection is closed, not leaked.
func TestRestartKeyArrivalForALeftRowIsDropped(t *testing.T) {
	m := testModel(sess("stopped"), sess("other"))
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		return session.HostProcess{PID: 4242}, nil
	}
	fp := newFakePane("RESTARTED")
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return fp, nil }

	m, cmd := pressAction(t, m, keyRune('r'))
	if cmd == nil {
		t.Fatal("setup: r on a stopped session should dispatch the restart")
	}
	hr := cmd().(hostRestartedMsg)
	m, _ = update(t, m, keyDown)
	m, open := update(t, m, hr)
	if open == nil {
		t.Fatal("setup: the restart should chain into the open")
	}
	m, _ = update(t, m, drainForPaneAttached(t, open))
	if m.pane != nil {
		t.Fatalf("pane installed for %q with the cursor on another row", m.paneName)
	}
	if fp.closed != 1 {
		t.Errorf("abandoned connection closed %d times, want 1", fp.closed)
	}
}

// TestRestartFailureShowsHostLogGuidance proves a failed spawn never dead-ends:
// it names the host.log that records why, and dispatches no attach.
func TestRestartFailureShowsHostLogGuidance(t *testing.T) {
	m := testModel(sess("stopped"))
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		return session.HostProcess{}, errors.New("fork/exec: permission denied")
	}

	// From the stopped row's focused overview, so the failure's focus rule
	// is exercised too.
	m, _ = update(t, m, keyEnter)
	m, cmd := pressAction(t, m, keyRune('r'))
	if cmd == nil {
		t.Fatal("r on a stopped session should dispatch the restart")
	}
	hr := cmd().(hostRestartedMsg)
	if hr.err == nil {
		t.Fatalf("expected a spawn error on hostRestartedMsg")
	}

	got, reload := update(t, m, hr)
	if reload == nil {
		t.Fatalf("a failed restart should still trigger a reload")
	}
	// The row still has no live agent, so the right side still draws its
	// overview — worth reading, and `r` retries from there. The keyboard stays.
	if got.focus != focusRight || !got.detailDrawn() {
		t.Fatalf("focus = %v detailDrawn = %v after a failed restart, want the focused overview kept", got.focus, got.detailDrawn())
	}
	for _, want := range []string{"stopped", "permission denied", "/wt/stopped/.afk/stopped/host.log"} {
		if !strings.Contains(got.status, want) {
			t.Fatalf("status = %q, want it to contain %q", got.status, want)
		}
	}
}

// TestPaneAttachFailedStatus table-drives the failure-status contract. The
// load-bearing distinction is host.ErrHostNotReachable versus anything else:
// only an unreachable host proves the advisory HostRunning flag lied, and only
// then does the phrasing turn on whether a restart was already tried.
func TestPaneAttachFailedStatus(t *testing.T) {
	unreachable := fmt.Errorf("attaching to the svc session: %w", host.ErrHostNotReachable)
	tests := []struct {
		name         string
		err          error
		restarted    bool
		wantContains []string
	}{
		{
			name:         "unreachable on a first attempt offers the restart",
			err:          unreachable,
			wantContains: []string{"svc", "isn't running", "r to restart"},
		},
		{
			name:         "unreachable right after a restart points at host.log",
			err:          unreachable,
			restarted:    true,
			wantContains: []string{"svc", "didn't start", "/wt/svc/.afk/svc/host.log"},
		},
		{
			name:         "any other failure is generic, with guidance",
			err:          errors.New("could not open the socket"),
			wantContains: []string{"svc", "could not open the socket", "/wt/svc/.afk/svc/host.log"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := paneAttachFailedStatus(defaultLeaderKey, paneAttachedMsg{
				name: "svc", worktree: "/wt/svc", restarted: tt.restarted, err: tt.err,
			})
			for _, want := range tt.wantContains {
				if !strings.Contains(status, want) {
					t.Fatalf("status = %q, want it to contain %q", status, want)
				}
			}
		})
	}
}

// TestPaneAttachFailedStatusTextsAreDistinct guards against two outcomes
// collapsing onto the same status line — the whole point of distinguishing them
// is that the user can tell "your host record was stale, press enter" from "the
// host I just started died, read its log".
func TestPaneAttachFailedStatusTextsAreDistinct(t *testing.T) {
	unreachable := fmt.Errorf("dial: %w", host.ErrHostNotReachable)
	seen := map[string]string{}
	for _, tt := range []struct {
		label string
		msg   paneAttachedMsg
	}{
		{"unreachable", paneAttachedMsg{name: "svc", worktree: "/wt/svc", err: unreachable}},
		{"unreachable after restart", paneAttachedMsg{name: "svc", worktree: "/wt/svc", restarted: true, err: unreachable}},
		{"generic", paneAttachedMsg{name: "svc", worktree: "/wt/svc", err: errors.New("boom")}},
	} {
		status := paneAttachFailedStatus(defaultLeaderKey, tt.msg)
		if prev, dup := seen[status]; dup {
			t.Fatalf("%s and %s produce the same status %q", tt.label, prev, status)
		}
		seen[status] = tt.label
	}
}

// TestUnreachableHostFlipsSessionToStopped is the false-positive correction: a
// session the list showed as RUNNING whose host does not answer is shown as
// stopped, so the restart key now acts on it instead of a connect being retried
// that cannot work.
func TestUnreachableHostFlipsSessionToStopped(t *testing.T) {
	m := testModel(runningSess("ghost"))
	msg := paneAttachedMsg{
		name:     "ghost",
		worktree: "/wt/ghost",
		err:      fmt.Errorf("attaching to the ghost session: %w", host.ErrHostNotReachable),
	}

	got, reload := update(t, m, msg)
	if reload == nil {
		t.Fatalf("a failed connect should trigger a reload")
	}
	if got.sessions[0].HostRunning {
		t.Fatalf("an unreachable host on a running-marked session should flip HostRunning to false")
	}
	if !strings.Contains(got.status, "r to restart") {
		t.Fatalf("status = %q, want it to offer the restart", got.status)
	}

	// And `r` now takes the restart branch.
	var spawned bool
	got.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		spawned = true
		return session.HostProcess{}, nil
	}
	_, cmd := pressAction(t, got, keyRune('r'))
	if cmd == nil {
		t.Fatalf("expected a restart cmd after the session was marked stopped")
	}
	if _, ok := cmd().(hostRestartedMsg); !ok || !spawned {
		t.Fatalf("r after the correction should re-spawn the host (spawned=%v)", spawned)
	}
}

// TestDeadHostCorrectionSurvivesTheNextDiscoveryTick is the regression test for
// the retry-forever trap: discovery recomputes HostRunning from the same stale
// host.json every couple of seconds, so a correction that only touched the
// current session list would be reverted before the user could act on the
// restart offer.
func TestDeadHostCorrectionSurvivesTheNextDiscoveryTick(t *testing.T) {
	m := testModel(runningSess("ghost"))
	m, _ = update(t, m, paneAttachedMsg{
		name: "ghost", worktree: "/wt/ghost",
		err: fmt.Errorf("dial: %w", host.ErrHostNotReachable),
	})

	// A fresh discovery pass still believes the recycled PID is a live host.
	stale := runningSess("ghost")
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{stale}, at: time.Unix(1_000_001, 0)})
	if m.sessions[0].HostRunning {
		t.Fatalf("the correction must survive a reload that reports the same dead PID")
	}

	// `r` must therefore still take the restart branch.
	var spawned bool
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		spawned = true
		return session.HostProcess{}, nil
	}
	_, cmd := pressAction(t, m, keyRune('r'))
	if cmd == nil {
		t.Fatal("r after the reload should dispatch the restart")
	}
	if _, ok := cmd().(hostRestartedMsg); !ok || !spawned {
		t.Fatalf("r after a reload should still restart (spawned=%v)", spawned)
	}
}

// TestDeadHostCorrectionLapsesWhenTheHostPIDChanges is the other half of the
// contract: the correction is scoped to the PID it disproved, so a genuinely
// new host (a different PID in host.json — restarted here or by a `afk new`
// elsewhere) is trusted at once and never stuck showing as stopped.
func TestDeadHostCorrectionLapsesWhenTheHostPIDChanges(t *testing.T) {
	m := testModel(runningSess("ghost"))
	m, _ = update(t, m, paneAttachedMsg{
		name: "ghost", worktree: "/wt/ghost",
		err: fmt.Errorf("dial: %w", host.ErrHostNotReachable),
	})

	reborn := runningSess("ghost")
	reborn.HostPID = 9999
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{reborn}, at: time.Unix(1_000_002, 0)})
	if !m.sessions[0].HostRunning {
		t.Fatalf("a host.json naming a different PID should be trusted again")
	}
	if len(m.deadHostPIDs) != 0 {
		t.Fatalf("the lapsed correction should be dropped, got %v", m.deadHostPIDs)
	}
}

// TestHelpAdvertisesOpenAndDetach pins the list footer contract: enter/f is
// the way to the right side (and so the way to open an agent), ctrl+\ is
// advertised as the way to close a pane (the retired detachKey's role —
// mode.go's real closePane binding, W6.T2), ctrl+t is NOT advertised because
// it is not a list key, and no stale multiplexer wording survives anywhere in
// the rendered dashboard.
func TestHelpAdvertisesOpenAndDetach(t *testing.T) {
	m := testModel(runningSess("live"))
	reg := m.modes()
	closePane := findBinding(reg.spec(inputNormal).keys.bindings, []string{"ctrl+\\"})

	// Stripped: help.Model styles the key and the description separately
	// (theme.helpStyles), so the raw line has an escape between them.
	help := xansi.Strip(m.renderHelp())
	for _, want := range []string{
		m.keys.Focus.Help().Key + " " + m.keys.Focus.Help().Desc,
		closePane.Help().Key + " " + closePane.Help().Desc,
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help = %q, want it to contain %q", help, want)
		}
	}
	if strings.Contains(help, "ctrl+t") {
		t.Fatalf("help = %q, want no ctrl+t — it does nothing while the list is focused", help)
	}
	// The needle is split so a residue check (`grep -rni` for the literal
	// multiplexer name) matches only real usages, never this negative
	// assertion about it.
	banned := "tm" + "ux"
	if strings.Contains(strings.ToLower(render(m)), banned) {
		t.Fatalf("the dashboard should no longer mention %s:\n%s", banned, render(m))
	}
}

// TestHelpAdvertisesTheFocusKeyOnlyWithAFocusableSurface proves the combined
// Enter/f binding is advertised exactly when it does something. It focuses
// whichever surface is drawn in the main region, so it is live with a pane and
// with the detail box drawn, and a no-op only in the empty state, where View
// paints renderEmptyState instead.
//
// The empty-state stanza is the load-bearing one. Without it the test passes
// for `m.pane != nil || !m.shouldRenderPane()`, which reduces to true
// (shouldRenderPane is false whenever pane == nil) and advertises `f`
// everywhere.
func TestHelpAdvertisesTheFocusKeyOnlyWithAFocusableSurface(t *testing.T) {
	m := testModel(runningSess("live"))
	focus := m.keys.Focus.Help().Key + " " + m.keys.Focus.Help().Desc
	if help := xansi.Strip(m.renderHelp()); !strings.Contains(help, focus) {
		t.Fatalf("the combined focus key should be advertised with the detail pane drawn:\n%s", help)
	}

	m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "live"})
	if help := xansi.Strip(m.renderHelp()); !strings.Contains(help, focus) {
		t.Fatalf("the combined focus key should be advertised once a pane is open:\n%s", help)
	}

	empty := testModel()
	empty.activeProject = -1
	if help := xansi.Strip(empty.renderHelp()); strings.Contains(help, focus) {
		t.Fatalf("the combined focus key must not be advertised in the empty state — nothing is focusable:\n%s", help)
	}
}

// TestTogglePaneSummary covers ctrl+t's state matrix under the two-axis model:
// ctrl+t is a right-side key that flips the VIEW and never the focus, plus the
// surroundings that must keep working while the agent sits behind the
// overview.
//
// The load-bearing rows are: the flip keeps focus right in both directions
// (objective 3 — the old hide dropped focus and a reveal restored it from a
// stored flag); ctrl+t into the agent view with no pane connects under
// enter's rules; ctrl+t is intercepted and never forwarded to the child;
// ctrl+t belongs to the creation form while it is open (bubbles' textarea binds
// it to TransposeCharacterBackward); clean is refused while a pane is
// installed behind the overview; resize runs even while the pane is not drawn
// (so the emulator grid stays current); a honored click shows the agent
// view, because a focused-but-undrawn pane routes the keyboard into a child
// that is not on screen; and an UNFOCUSED arrival leaves the overview in
// place, because only ctrl+t changes the view.
func TestTogglePaneSummary(t *testing.T) {
	focusedAgent := func(t *testing.T, m Model) Model {
		t.Helper()
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "sess"})
		m, _ = update(t, m, keyEnter)
		if !m.paneOwnsKeyboard() {
			t.Fatalf("setup: enter should have focused the installed pane")
		}
		return m
	}
	behindOverview := func(t *testing.T, m Model) Model {
		t.Helper()
		m = focusedAgent(t, m)
		m, _ = flipView(t, m)
		m, _ = pressAction(t, m, keyEscMsg())
		if m.view != viewOverview || m.focus != focusList || m.pane == nil {
			t.Fatalf("setup: view=%v focus=%v pane=%v, want the pane behind the overview with the list focused",
				m.view, m.focus, m.pane != nil)
		}
		return m
	}
	tests := []struct {
		name   string
		setup  func(*testing.T, Model) Model
		act    func(*testing.T, Model) (Model, tea.Cmd)
		checks func(*testing.T, Model, tea.Cmd)
	}{
		{
			name:  "ctrl+t from the focused agent: overview drawn, focus stays right, pane kept (load-bearing: ctrl+t never moves focus)",
			setup: focusedAgent,
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return flipView(t, m)
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil {
					t.Fatalf("a view flip with the pane installed must dispatch nothing, got %T", cmd())
				}
				if m.pane == nil {
					t.Fatalf("ctrl+t should not close or detach the pane")
				}
				if m.view != viewOverview || m.focus != focusRight {
					t.Fatalf("view/focus = %v/%v, want overview/right", m.view, m.focus)
				}
				if m.paneOwnsKeyboard() || !m.detailOwnsKeyboard() {
					t.Fatalf("pane=%v detail=%v, want the overview to own the keyboard", m.paneOwnsKeyboard(), m.detailOwnsKeyboard())
				}
				// The flip writes a status, whose toast floats over the detail
				// box's top-right corner — where the tab strip is. Age the
				// toast out so the assertion reads the strip, not the toast.
				m.statusChangedAt = time.Time{}
				view := render(m)
				if !strings.Contains(view, "Overview") || !strings.Contains(view, "Waves") {
					t.Fatalf("View() should render the detail pane (its tab strip) behind ctrl+t:\n%s", view)
				}
			},
		},
		{
			name:  "ctrl+t twice from the focused agent: back on the agent with the keyboard (load-bearing: round trip is lossless)",
			setup: focusedAgent,
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				m, _ = flipView(t, m)
				return flipView(t, m)
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil {
					t.Fatalf("revealing the installed pane must dispatch nothing, got %T", cmd())
				}
				if m.view != viewAgent || !m.paneOwnsKeyboard() {
					t.Fatalf("view=%v paneOwnsKeyboard=%v, want the agent drawn and focused", m.view, m.paneOwnsKeyboard())
				}
				if !strings.Contains(m.status, "input goes to sess") {
					t.Fatalf("status = %q, want it to say where input goes", m.status)
				}
			},
		},
		{
			name: "ctrl+t from the focused overview with no pane: connects under enter's rules, focus stays right",
			setup: func(t *testing.T, m Model) Model {
				m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("SESS"), nil }
				m, _ = update(t, m, keyRune('d'))
				if !m.detailOwnsKeyboard() {
					t.Fatalf("setup: d should focus the overview")
				}
				return m
			},
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return flipView(t, m)
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd == nil || m.attachInFlight.name != "sess" || !m.attachInFlight.focusOnArrival {
					t.Fatalf("ctrl+t into the agent view should dial sess focused: cmd=%v inFlight=%+v", cmd != nil, m.attachInFlight)
				}
				if m.view != viewAgent || m.focus != focusRight {
					t.Fatalf("view/focus = %v/%v, want agent/right", m.view, m.focus)
				}
				m, _ = update(t, m, drainForPaneAttached(t, cmd))
				if !m.paneOwnsKeyboard() {
					t.Fatalf("the connected pane should land owning the keyboard")
				}
			},
		},
		{
			name: "ctrl+t while pane focused is not forwarded to child (load-bearing: Claude Code must not act on it)",
			setup: func(t *testing.T, m Model) Model {
				return focusedAgent(t, m)
			},
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return flipView(t, m)
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				fp := m.pane.(*fakePane)
				if len(fp.keys) != 0 || len(fp.texts) != 0 {
					t.Fatalf("ctrl+t was forwarded to the child (%d keys, %d texts), but it must be intercepted", len(fp.keys), len(fp.texts))
				}
			},
		},
		{
			name:  "c while the pane is behind the overview: asks first, and y detaches the pane before cleaning (load-bearing: the worktree goes, the host is its own process)",
			setup: behindOverview,
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				m, _ = pressAction(t, m, keyRune('c'))
				if m.modal != inputConfirmClean || m.pane == nil {
					t.Fatalf("c: modal %v, pane %v; want the confirmation with the pane still there", m.modal, m.pane != nil)
				}
				return update(t, m, keyRune('y'))
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.pane != nil {
					t.Fatalf("the pane on the cleaned session is still attached")
				}
				if cmd == nil {
					t.Fatalf("a confirmed clean must dispatch the clean")
				}
			},
		},
		{
			name: "ctrl+t while the creation form is open: the form owns the key (load-bearing: bubbles binds ctrl+t in the textarea)",
			setup: func(t *testing.T, m Model) Model {
				m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "sess"})
				m, _ = pressAction(t, m, keyRune('n'))
				if !m.formOpen {
					t.Fatalf("setup: the creation form should be open")
				}
				m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
				if m.form.focusIndex != 1 {
					t.Fatalf("setup: focusIndex = %d, want 1 (description)", m.form.focusIndex)
				}
				m.form.description.SetValue("ab")
				return m
			},
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				// Bare ctrl+t: the form is a modal, so the leader cannot open
				// and the key goes to the textarea.
				return update(t, m, keyCtrlT)
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.view == viewOverview {
					t.Fatalf("ctrl+t must not flip the view behind the form")
				}
				if !m.formOpen {
					t.Fatalf("ctrl+t must not close the form")
				}
				if got := m.form.description.Value(); got != "ba" {
					t.Fatalf("description = %q, want %q — the textarea's ctrl+t (TransposeCharacterBackward) must still reach it", got, "ba")
				}
			},
		},
		{
			name:  "WindowSizeMsg while the pane is behind the overview: resize still called (load-bearing: prevents stale-grid regression)",
			setup: behindOverview,
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return update(t, m, tea.WindowSizeMsg{Width: 200, Height: 60})
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				fp := m.pane.(*fakePane)
				if len(fp.resizes) == 0 {
					t.Fatalf("WindowSizeMsg should still Resize the undrawn pane (got %d resizes)", len(fp.resizes))
				}
			},
		},
		{
			name:  "enter with the pane behind the overview: opens the agent, the pane it was connected to all along",
			setup: behindOverview,
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return update(t, m, keyEnter)
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil {
					t.Fatalf("revealing the installed pane must dispatch nothing, got %T", cmd())
				}
				if m.view != viewAgent || !m.paneOwnsKeyboard() {
					t.Fatalf("view=%v pane=%v, want the agent focused", m.view, m.paneOwnsKeyboard())
				}
			},
		},
		{
			name: "enter from the focused overview reveals the installed agent with the keyboard",
			setup: func(t *testing.T, m Model) Model {
				m = behindOverview(t, m)
				m, _ = update(t, m, keyRune('d'))
				return m
			},
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return flipView(t, m)
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil {
					t.Fatalf("revealing the installed pane must dispatch nothing, got %T", cmd())
				}
				if m.view != viewAgent || !m.paneOwnsKeyboard() {
					t.Fatalf("view=%v paneOwnsKeyboard=%v, want the agent drawn and focused", m.view, m.paneOwnsKeyboard())
				}
			},
		},
		{
			name:  "click-to-focus on the hidden pane's own session: reveals it (load-bearing: focus must never point at an invisible pane)",
			setup: behindOverview,
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return update(t, m, freshRequestMsg("sess"))
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.pane == nil {
					t.Fatalf("a honored click must not close the pane")
				}
				if !m.paneOwnsKeyboard() {
					t.Fatalf("paneOwnsKeyboard = false, want true — the click hands the keyboard to the session")
				}
				if m.view == viewOverview {
					t.Fatalf("view = overview with paneOwnsKeyboard = true: the click routes keystrokes into a child that is not on screen")
				}
				if fp := m.pane.(*fakePane); fp.closed != 0 {
					t.Fatalf("the installed pane was closed %d times; a focus request must not detach it", fp.closed)
				}
			},
		},
		{
			name: "switching sessions while hidden: the arriving pane installs behind the overview (load-bearing: only ctrl+t changes the view)",
			setup: func(t *testing.T, m Model) Model {
				// The list is kept in screen order (name order inside a
				// group), so "other" sorts first: sess is selected by name.
				m.setSessions([]Session{runningSess("sess"), runningSess("other")})
				i, _ := m.sessionIndex("sess")
				m.selectSessionIndex(i)
				return behindOverview(t, m)
			},
			act: func(t *testing.T, m Model) (Model, tea.Cmd) {
				// The cursor moves with the switch, because shouldRenderPane renders a
				// pane only for the SELECTED session: leaving the cursor on "sess" would
				// hide the arriving pane for that reason instead of for the view, and
				// the row would pass while asserting nothing.
				i, _ := m.sessionIndex("other")
				m.selectSessionIndex(i)
				return update(t, m, paneAttachedMsg{pane: newFakePane("OTHER"), name: "other"})
			},
			checks: func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.paneName != "other" {
					t.Fatalf("paneName = %q, want other — the switch should install the arriving pane", m.paneName)
				}
				// An unfocused arrival is passive: the view is the user's ctrl+t
				// choice, so the overview stays and the pane waits behind it.
				if m.view != viewOverview {
					t.Fatalf("view = %v after an unfocused arrival, want the overview kept", m.view)
				}
				if view := render(m); strings.Contains(view, "OTHER") {
					t.Fatalf("the arrival should sit behind the overview, not be drawn:\n%s", view)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(runningSess("sess"))
			m = tt.setup(t, m)
			got, cmd := tt.act(t, m)
			tt.checks(t, got, cmd)
		})
	}
}

// TestRightFocusKeyTable is rows (b) and (c) of the W2.T1 key table, driven
// through Update with the right side focused.
//
// Agent view, load-bearing in both directions: the leader (ctrl+]) is the
// ONLY key the dashboard takes, so esc, q, ctrl+c, tab, ctrl+w and the old
// ctrl+t and ctrl+\ chords — every one of which the dashboard once bound —
// must reach the child, and must neither quit nor move focus. A rule added
// to inputPane for any of them takes the key away from every hosted agent.
//
// Overview view (Project since T16.2): enter opens the agent with focus kept
// right; esc returns to the list; q and ctrl+c quit, as at every level but
// the Agent. The Project letters acting from the overview are
// TestOverviewAnswersTheProjectKeys's.
func TestRightFocusKeyTable(t *testing.T) {
	type want struct {
		forwarded bool
		focus     paneFocus
		view      rightView
		quit      bool
	}
	agent := func(t *testing.T) (Model, *fakePane) {
		t.Helper()
		fp := newFakePane("X")
		m := testModel(runningSess("live"))
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "live"})
		m, _ = update(t, m, keyEnter)
		if !m.paneOwnsKeyboard() {
			t.Fatalf("setup: enter should have focused the installed pane")
		}
		return m, fp
	}
	overview := func(t *testing.T) (Model, *fakePane) {
		t.Helper()
		m, fp := agent(t)
		m = flipViewKey(t, m)
		if !m.detailOwnsKeyboard() {
			t.Fatalf("setup: ctrl+] d should have put the focused overview on the right")
		}
		return m, fp
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) (Model, *fakePane)
		key   tea.KeyPressMsg
		// leader, when set, presses ctrl+] and then this rune instead of
		// sending key: the way the pane's own moves are reached now. strip
		// presses ctrl+] and then key, for a key that is not a rune (esc).
		leader rune
		strip  bool
		want   want
	}{
		{"agent: esc reaches the child", agent, tea.KeyPressMsg{Code: tea.KeyEsc}, 0, false, want{forwarded: true, focus: focusRight, view: viewAgent}},
		{"agent: q reaches the child", agent, tea.KeyPressMsg{Code: 'q', Text: "q"}, 0, false, want{forwarded: true, focus: focusRight, view: viewAgent}},
		{"agent: ctrl+c reaches the child", agent, chord('c'), 0, false, want{forwarded: true, focus: focusRight, view: viewAgent}},
		{"agent: tab reaches the child", agent, tea.KeyPressMsg{Code: tea.KeyTab}, 0, false, want{forwarded: true, focus: focusRight, view: viewAgent}},
		{"agent: ctrl+w reaches the child", agent, chord('w'), 0, false, want{forwarded: true, focus: focusRight, view: viewAgent}},
		{"agent: ctrl+t reaches the child", agent, chord('t'), 0, false, want{forwarded: true, focus: focusRight, view: viewAgent}},
		{"agent: ctrl+\\ reaches the child", agent, chord('\\'), 0, false, want{forwarded: true, focus: focusRight, view: viewAgent}},
		{"agent: ctrl+] d gives the overview the keyboard", agent, tea.KeyPressMsg{}, 'd', false, want{focus: focusRight, view: viewOverview}},
		{"agent: ctrl+] esc returns to the list", agent, tea.KeyPressMsg{Code: tea.KeyEsc}, 0, true, want{focus: focusList, view: viewAgent}},

		{"overview: enter opens the agent", overview, tea.KeyPressMsg{Code: tea.KeyEnter}, 0, false, want{focus: focusRight, view: viewAgent}},
		{"overview: esc returns to the list", overview, tea.KeyPressMsg{Code: tea.KeyEsc}, 0, false, want{focus: focusList, view: viewOverview}},
		{"overview: q quits", overview, tea.KeyPressMsg{Code: 'q', Text: "q"}, 0, false, want{focus: focusRight, view: viewOverview, quit: true}},
		{"overview: ctrl+c quits", overview, chord('c'), 0, false, want{focus: focusRight, view: viewOverview, quit: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.setup(t)
			var got Model
			var cmd tea.Cmd
			switch {
			case tc.leader != 0:
				got, cmd = leaderThen(t, m, tc.leader)
			case tc.strip:
				got, cmd = pressAction(t, m, tc.key)
			default:
				got, cmd = update(t, m, tc.key)
			}

			forwarded := len(fp.keys)+len(fp.texts) > 0
			if forwarded != tc.want.forwarded {
				t.Errorf("forwarded to the child = %v (keys=%v texts=%v), want %v", forwarded, fp.keys, fp.texts, tc.want.forwarded)
			}
			if got.quitting != tc.want.quit {
				t.Errorf("quitting = %v, want %v", got.quitting, tc.want.quit)
			}
			if tc.want.quit {
				return // quit tears the pane down; focus is moot
			}
			if got.focus != tc.want.focus || got.view != tc.want.view {
				t.Errorf("focus/view = %v/%v, want %v/%v", got.focus, got.view, tc.want.focus, tc.want.view)
			}
			if got.pane != fp {
				t.Errorf("the pane was replaced or closed (pane=%v)", got.pane)
			}
			if got.formOpen || got.modal != inputNormal {
				t.Errorf("a list action fired: formOpen=%v modal=%v", got.formOpen, got.modal)
			}
			if cmd != nil && !tc.want.forwarded {
				t.Errorf("returned a Cmd (%T), want none", cmd())
			}
		})
	}
}

// TestToggleKeyAdvertisedOnlyWhereItActs pins that no footer advertises
// ctrl+t, which is the agent's key since T7.2 — not the list's before a pane
// exists, with one drawn, or with one behind the overview, and not the
// right-side modes' — and that the focused overview's footer names enter,
// the way to the agent since T16.2.
func TestToggleKeyAdvertisedOnlyWhereItActs(t *testing.T) {
	toggle := m0Toggle()
	m := testModel(runningSess("live"))
	if help := xansi.Strip(m.renderHelp()); strings.Contains(help, toggle) {
		t.Fatalf("list help before a pane exists advertises %q:\n%s", toggle, help)
	}

	m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "live"})
	if help := xansi.Strip(m.renderHelp()); strings.Contains(help, toggle) {
		t.Fatalf("list help with a drawn pane advertises %q:\n%s", toggle, help)
	}

	m.view = viewOverview
	if help := xansi.Strip(m.renderHelp()); strings.Contains(help, toggle) {
		t.Fatalf("list help with the pane behind the overview advertises %q:\n%s", toggle, help)
	}

	// The right side: ctrl+t is no longer a key there at all (T7.2), so neither
	// right-side keymap may carry it, or the footer would advertise a key that
	// is forwarded to the agent. The way to the toggle is the leader, and the
	// footer must point at it.
	m, _ = update(t, m, keyRune('d'))
	reg := m.modes()
	for _, mode := range []inputMode{inputDetail, inputPane} {
		if b := findBinding(reg.spec(mode).keys.bindings, []string{"ctrl+t"}); b.Help().Key == "ctrl+t" {
			t.Fatalf("%v keymap still carries ctrl+t, a key that no longer acts", mode)
		}
	}
	if derivedMode(m) != inputDetail {
		t.Fatalf("setup: d should focus the overview, mode = %v", derivedMode(m))
	}
	if help := xansi.Strip(m.renderHelp()); !strings.Contains(help, "enter open agent") || strings.Contains(help, "ctrl+t") {
		t.Fatalf("focused overview footer should name enter, the way to the agent, and not ctrl+t:\n%s", help)
	}
}

// m0Toggle is ctrl+t's old footer label, which no footer may carry: ctrl+t
// is the agent's key since T7.2.
func m0Toggle() string {
	return "ctrl+t agent/overview"
}

// TestHelpLineSurvivesTruncationWithAPane is the width budget on the help line,
// the same contract TestConfirmCleanBarFitsAnEightyColumnTerminal pins on the
// modal. With the pane behind the overview and the list focused, the way back
// to the agent starts with enter/f (focus the right side, then ctrl+t there),
// so enter/f must survive truncate()'s cut from the END of the line.
//
// Both rows are load-bearing, at the two widths that matter: 80, and 120 —
// what the rest of the suite renders at and what a great many terminals are.
func TestHelpLineSurvivesTruncationWithAPane(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := testModel(runningSess("live"))
			m.width = width
			m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "live"})
			m.view = viewOverview

			// truncate() is what View applies to the footer; renderHelp itself is
			// unbounded, so assert on the same cut the user sees.
			help := truncate(m.renderHelp(), m.width)
			if w := lipgloss.Width(help); w > m.width {
				t.Fatalf("help line exceeds width %d (got %d): %q", m.width, w, help)
			}
			for _, want := range []string{m.keys.Focus.Help().Key, m.keys.Focus.Help().Desc} {
				if !strings.Contains(help, want) {
					t.Fatalf("help line at width %d lost %q — the way back to a pane behind the overview must survive truncation:\n%s", m.width, want, help)
				}
			}
		})
	}
}

// TestPaneTeardownHandsFocusBack pins forgetPane's focus rule at both teardown
// sites. The pane is the only right-side surface that can vanish under a
// focused keyboard: the overview is always drawable, so a right focus that
// outlives its pane in AGENT view would leave typed keys landing on a surface
// the user never chose. Both rows are load-bearing in the same way — the
// first because a session ending is the common path, the second because quit
// tears the pane down on a path that never goes through paneExitedMsg. The
// view is deliberately NOT asserted reset: it is a dashboard-wide preference,
// not state about the pane that ended.
func TestPaneTeardownHandsFocusBack(t *testing.T) {
	focused := func(t *testing.T) (Model, *fakePane) {
		fp := newFakePane("X")
		m := testModel(runningSess("live"))
		m.focus = focusRight // connect moves the focus at request time
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "live", focusOnArrival: true})
		if !m.paneOwnsKeyboard() {
			t.Fatalf("setup: pane should own the keyboard")
		}
		return m, fp
	}

	t.Run("session ends while focused", func(t *testing.T) {
		m, fp := focused(t)
		m, _ = update(t, m, paneExitedMsg{pane: fp, err: nil})
		if m.pane != nil {
			t.Fatalf("pane should be gone after paneExitedMsg")
		}
		if m.focus != focusList {
			t.Fatalf("focus = %v after the focused pane ended, want %v", m.focus, focusList)
		}
	})

	t.Run("quit while focused", func(t *testing.T) {
		m, _ := focused(t)
		m = pressKey(t, m, keyEscMsg())
		m = pressKey(t, m, keyRune('q'))
		if m.pane != nil || m.focus != focusList {
			t.Fatalf("quit left pane=%v focus=%v, want no pane and list focus", m.pane != nil, m.focus)
		}
	})
}

// --- n / c ---

// TestNewKeyOpensForm covers the W7.T1 replacement for the old suspend-and-
// exec `n` behavior: it opens the in-TUI form rather than shelling out.
//
// The returned Cmd is the name field's cursor blink, not an exec: bubbletea's
// execMsg is unexported, so the assertion is the positive one — the Cmd yields
// a cursor.BlinkMsg — which proves both that nothing shells out and that the
// cursor actually blinks on open rather than only after the first Tab.
func TestNewKeyOpensForm(t *testing.T) {
	m := testModel(sess("a"))
	got, cmd := pressAction(t, m, keyRune('n'))
	if !got.formOpen {
		t.Fatalf("n should open the creation form")
	}
	if cmd == nil {
		t.Fatalf("n should return the focus Cmd so the name field's cursor blinks")
	}
	if _, isBlink := cmd().(cursor.BlinkMsg); !isBlink {
		t.Fatalf("n's Cmd = %T, want cursor.BlinkMsg (in-process focus, not a shell-out)", cmd())
	}
}

// TestScratchKeyNoActiveProjectIsNoOp mirrors TestCleanWithNothingSelectedIsInertWithStatus's
// shape for `o`: with no active project (hasActiveProject's own zero-value
// case — see project_test.go), the one-off key must not dispatch a create,
// must not touch pendingSpawns or sessions, and must still say why rather
// than silently swallowing the keypress.
func TestScratchKeyNoActiveProjectIsNoOp(t *testing.T) {
	m := New("", time.Second, testDeps())

	got, cmd := refuseAction(m, "o")
	if cmd != nil {
		t.Fatalf("o with no active project must not dispatch a Cmd, got %v", cmd)
	}
	if got.modal != inputNormal {
		t.Fatalf("o with no active project must open no picker, got modal %v", got.modal)
	}
	if got.status == "" {
		t.Fatalf("o with no active project must set a status line, not be silently ignored")
	}
	if len(got.pendingSpawns) != 0 {
		t.Fatalf("o with no active project must not record a pending spawn, got %v", got.pendingSpawns)
	}
	if len(got.sessions) != 0 {
		t.Fatalf("o with no active project must not open a form or a row, got %v", got.sessions)
	}
}

// TestScratchKeyDispatchesCreateAndPendingRow proves confirming the `o`
// picker with an active project follows submitNewForm's exact shape without
// ever opening the form: a dispatched Cmd, a pending-row merged in on THIS
// frame (not the next tick), and a "creating…" status naming it — all on the
// enter that confirms the picker's default.
func TestScratchKeyDispatchesCreateAndPendingRow(t *testing.T) {
	m := testModel(sess("a"))

	got, cmd := pressScratch(t, m)
	if cmd == nil {
		t.Fatalf("o with an active project should dispatch the background create")
	}
	if got.formOpen {
		t.Fatalf("o must open no form")
	}
	if len(got.pendingSpawns) != 1 || got.pendingSpawns[0] != "scratch-1" {
		t.Fatalf("pendingSpawns = %v, want [scratch-1]", got.pendingSpawns)
	}
	idx, ok := got.sessionIndex("scratch-1")
	if !ok || !got.sessions[idx].Pending {
		t.Fatalf("expected a pending scratch-1 row merged in immediately, got %+v", got.sessions)
	}
	if !strings.Contains(got.status, "scratch-1") {
		t.Fatalf("status = %q, want it to name scratch-1", got.status)
	}
}

// TestScratchKeyKeepsDiskIOOffTheUpdatePath is the regression test for the
// review finding that openScratch ran session.ListSessionDirs (os.ReadDir)
// synchronously inside Update, breaking the off-loop-I/O rule writeLabelCmd's
// own doc comment states. Three assertions, all load-bearing: the seam is
// NOT called during the keypress Update (the I/O moved into the returned
// Cmd); the same-frame placeholder is guessed from memory alone (so a disk
// dir invisible to m.sessions cannot have been consulted synchronously); and
// once the Cmd's message lands, the disk knowledge corrects the placeholder
// to the name afk new will actually allocate and dispatches the create.
func TestScratchKeyKeepsDiskIOOffTheUpdatePath(t *testing.T) {
	m := testModel(sess("a"))
	calls := 0
	m.deps.ListSessionDirs = func(repo string) ([]string, error) {
		calls++
		if repo != "/repo" {
			t.Errorf("ListSessionDirs got repo %q, want /repo", repo)
		}
		return []string{"scratch-1"}, nil
	}

	m, cmd := pressScratch(t, m)
	if calls != 0 {
		t.Fatalf("ListSessionDirs ran %d time(s) on the Update path, want 0 (I/O belongs in the Cmd)", calls)
	}
	if _, ok := m.sessionIndex("scratch-1"); !ok {
		t.Fatalf("same-frame placeholder should be the in-memory guess scratch-1, got %+v", m.sessions)
	}
	if cmd == nil {
		t.Fatal("o should return the Cmd that carries the disk listing")
	}

	msg := cmd()
	if calls != 1 {
		t.Fatalf("running the Cmd should perform the listing exactly once, got %d", calls)
	}

	m, next := update(t, m, msg)
	if _, ok := m.sessionIndex("scratch-1"); ok {
		t.Fatalf("the placeholder should be corrected away from the on-disk name, got %+v", m.sessions)
	}
	idx, ok := m.sessionIndex("scratch-2")
	if !ok || !m.sessions[idx].Pending {
		t.Fatalf("expected a corrected pending scratch-2 row, got %+v", m.sessions)
	}
	if !strings.Contains(m.status, "scratch-2") {
		t.Fatalf("status = %q, want it to name the corrected scratch-2", m.status)
	}
	if next == nil {
		t.Fatal("the corrected create must still be dispatched")
	}
}

// TestScratchKeyListingErrorClearsThePlaceholder pins the error half of the
// deferred listing: a failed disk read must take the already-merged
// placeholder down with it (a permanent "creating…" row for a create that was
// never dispatched is the exact leak clearPendingSpawn exists to prevent) and
// say why in the status line.
func TestScratchKeyListingErrorClearsThePlaceholder(t *testing.T) {
	m := testModel(sess("a"))
	m.deps.ListSessionDirs = func(string) ([]string, error) {
		return nil, errors.New("permission denied")
	}

	m, cmd := pressScratch(t, m)
	if cmd == nil {
		t.Fatal("o should return the listing Cmd")
	}
	m, next := update(t, m, cmd())
	if next != nil {
		t.Fatalf("a failed listing must not dispatch the create, got %v", next)
	}
	if _, ok := m.sessionIndex("scratch-1"); ok {
		t.Fatalf("the placeholder must be cleared on a listing failure, got %+v", m.sessions)
	}
	if len(m.pendingSpawns) != 0 {
		t.Fatalf("pendingSpawns = %v, want empty after a listing failure", m.pendingSpawns)
	}
	if !strings.Contains(m.status, "permission denied") {
		t.Fatalf("status = %q, want the listing error surfaced", m.status)
	}
}

// TestScratchKeyGuessesDistinctNamesAcrossRepeatedPresses locks in that
// openScratch guesses against m.sessions, not just m.pendingSpawns: two `o`
// presses in the same tick, before ListSessionDirs can see the first one's
// directory on disk, must still produce two distinct placeholder rows.
// pendingSpawns alone cannot be trusted for this — mergePendingSpawns drops a
// name from it the moment the row is inserted (see openScratch's comment) —
// so guessing against it alone would let both presses land on "scratch-1"
// and race session.Create's own "directory already exists" guard.
func TestScratchKeyGuessesDistinctNamesAcrossRepeatedPresses(t *testing.T) {
	m := testModel(sess("a"))

	m, _ = pressScratch(t, m)
	m, cmd := pressScratch(t, m)
	if cmd == nil {
		t.Fatalf("second o should also dispatch a create")
	}
	var pendingNames []string
	for _, s := range m.sessions {
		if s.Pending {
			pendingNames = append(pendingNames, s.Name)
		}
	}
	if len(pendingNames) != 2 {
		t.Fatalf("pending rows = %v, want two distinct entries", pendingNames)
	}
	if pendingNames[0] == pendingNames[1] {
		t.Fatalf("two rapid o presses guessed the same name: %v", pendingNames)
	}
}

// pressScratch is the whole `o` gesture since the provider picker: press `o`,
// check the picker came up on the default provider, then enter to accept it.
// It returns what the ENTER returned, which is what the bare `o` keypress used
// to return before the picker existed, so the scratch tests that predate the
// picker keep their original assertions unchanged.
func pressScratch(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, cmd := pressAction(t, m, keyRune('o'))
	if m.modal != inputPickProvider {
		t.Fatalf("o should open the provider picker, got modal %v", m.modal)
	}
	if cmd != nil {
		t.Fatalf("opening the provider picker must dispatch nothing, got %v", cmd)
	}
	return update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// pendingRows lists the placeholder rows in m.sessions.
func pendingRows(m Model) []string {
	var names []string
	for _, s := range m.sessions {
		if s.Pending {
			names = append(names, s.Name)
		}
	}
	return names
}

// scratchArgvFor is the one-off argv a create on providerName must carry.
func scratchArgvFor(providerName string) []string {
	want := []string{"new", "--no-dash", "--scratch"}
	if providerName != provider.Default {
		want = append(want, "--provider", providerName)
	}
	return want
}

// TestScratchKeyOpensProviderPicker pins `o`'s first half: the picker comes
// up with Claude highlighted, and NOTHING else happens — no Cmd, no
// placeholder row, no pending spawn. That emptiness is load-bearing: it is
// what makes esc a clean cancel, with nothing to unwind.
func TestScratchKeyOpensProviderPicker(t *testing.T) {
	m := testModel(sess("a"))

	got, cmd := pressAction(t, m, keyRune('o'))
	if got.modal != inputPickProvider {
		t.Fatalf("o should open the provider picker, got modal %v", got.modal)
	}
	if cmd != nil {
		t.Fatalf("opening the picker must dispatch nothing, got %v", cmd)
	}
	if name := provider.Names()[got.providerPick]; name != provider.Default {
		t.Fatalf("picker highlights %q, want the default %q", name, provider.Default)
	}
	if rows := pendingRows(got); len(rows) != 0 || len(got.pendingSpawns) != 0 {
		t.Fatalf("opening the picker must add no placeholder, got rows %v pending %v", rows, got.pendingSpawns)
	}
	if got.formOpen {
		t.Fatal("o must not open the creation form")
	}
	d, ok := got.activeDialog()
	if !ok || d.title != "New one-off session" {
		t.Fatalf("activeDialog() = %+v, %v; want the provider picker", d, ok)
	}
	body := xansi.Strip(got.renderDialog(d))
	if !strings.Contains(body, "› "+provider.Default+" (default)") {
		t.Fatalf("picker should mark the default row as highlighted:\n%s", body)
	}
	for _, n := range provider.Names() {
		if !strings.Contains(body, n) {
			t.Fatalf("picker should list %q:\n%s", n, body)
		}
	}
}

// TestProviderPickerEscCancels proves esc closes the picker with nothing
// created: back to normal mode, no Cmd, no placeholder row, no pending spawn.
func TestProviderPickerEscCancels(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = pressAction(t, m, keyRune('o'))
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyDown})

	got, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got.modal != inputNormal {
		t.Fatalf("esc should close the picker, got modal %v", got.modal)
	}
	if cmd != nil {
		t.Fatalf("esc must dispatch nothing, got %v", cmd)
	}
	if rows := pendingRows(got); len(rows) != 0 || len(got.pendingSpawns) != 0 {
		t.Fatalf("esc must leave no placeholder, got rows %v pending %v", rows, got.pendingSpawns)
	}
}

// TestProviderPickerStrayKeysAreSwallowed proves the picker answers nothing
// it does not bind: q neither quits nor closes it, and a second `o` neither
// stacks a create nor resets the highlight.
func TestProviderPickerStrayKeysAreSwallowed(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = pressAction(t, m, keyRune('o'))
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	for _, r := range []rune{'q', 'o', 'y', 'n'} {
		var cmd tea.Cmd
		m, cmd = update(t, m, keyRune(r))
		if cmd != nil {
			t.Fatalf("%q in the picker dispatched %v, want nothing", r, cmd)
		}
		if m.modal != inputPickProvider {
			t.Fatalf("%q closed the picker (modal %v)", r, m.modal)
		}
		if m.providerPick != 1 {
			t.Fatalf("%q moved the highlight to %d, want 1", r, m.providerPick)
		}
	}
	if rows := pendingRows(m); len(rows) != 0 {
		t.Fatalf("stray keys created rows %v", rows)
	}
}

// TestProviderPickerConfirmDispatchesChosenProvider drives the picker end to
// end through Update and the exec seam: the keys pressed before enter decide
// the provider, and the `afk new` argv the create finally runs carries
// --provider for a non-default choice and nothing for Claude. RunSpawn
// captures the exec.Cmd, so no child ever starts.
//
// Load-bearing rows: "enter alone" (the default adds no --provider, so a
// Claude one-off's argv is byte-identical to the pre-picker one), "down"
// (the second provider gets --provider), and the two wrap rows (up from
// Claude lands on the last provider; len(Names()) downs come back to Claude),
// which pin the modulo arithmetic in both directions.
func TestProviderPickerConfirmDispatchesChosenProvider(t *testing.T) {
	names := provider.Names()
	if len(names) < 2 {
		t.Fatalf("need at least two providers to exercise the picker, got %v", names)
	}
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	downs := make([]tea.Msg, len(names))
	for i := range downs {
		downs[i] = down
	}
	cases := []struct {
		name string
		keys []tea.Msg
		want string
	}{
		{"enter alone takes the default", nil, provider.Default},
		{"down takes the second provider", []tea.Msg{down}, names[1]},
		{"j takes the second provider", []tea.Msg{keyRune('j')}, names[1]},
		{"down then up returns to the default", []tea.Msg{down, up}, provider.Default},
		{"down then k returns to the default", []tea.Msg{down, keyRune('k')}, provider.Default},
		{"up from the default wraps to the last", []tea.Msg{up}, names[len(names)-1]},
		{"a full lap of downs wraps to the default", downs, provider.Default},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("a"))
			var ran []*exec.Cmd
			m.deps.RunSpawn = func(c *exec.Cmd) ([]byte, error) {
				ran = append(ran, c)
				return nil, nil
			}
			m, _ = pressAction(t, m, keyRune('o'))
			for _, k := range tc.keys {
				var cmd tea.Cmd
				m, cmd = update(t, m, k)
				if cmd != nil {
					t.Fatalf("moving the highlight dispatched %v, want nothing", cmd)
				}
			}
			if got := names[m.providerPick]; got != tc.want {
				t.Fatalf("highlight on %q before enter, want %q", got, tc.want)
			}

			m, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if m.modal != inputNormal {
				t.Fatalf("enter should close the picker, got modal %v", m.modal)
			}
			if cmd == nil {
				t.Fatal("enter should dispatch the one-off's listing Cmd")
			}
			if rows := pendingRows(m); len(rows) != 1 || rows[0] != "scratch-1" {
				t.Fatalf("pending rows = %v, want [scratch-1]", rows)
			}
			listed, ok := cmd().(scratchDirsListedMsg)
			if !ok {
				t.Fatalf("enter's Cmd produced %T, want scratchDirsListedMsg", cmd())
			}
			if listed.provider != tc.want {
				t.Fatalf("listing carries provider %q, want %q", listed.provider, tc.want)
			}
			m, next := update(t, m, listed)
			if next == nil {
				t.Fatal("the listing should dispatch the create")
			}
			if _, ok := next().(spawnFinishedMsg); !ok {
				t.Fatal("the create Cmd should report a spawnFinishedMsg")
			}
			if len(ran) != 1 {
				t.Fatalf("RunSpawn ran %d times, want 1", len(ran))
			}
			if got, want := ran[0].Args[1:], scratchArgvFor(tc.want); !slicesEqual(got, want) {
				t.Fatalf("argv = %v, want %v", got, want)
			}
		})
	}
}

// TestProviderPickerResetsToDefaultOnReopen pins the reset-on-every-open rule
// openProviderPicker documents: a highlight left on another provider — by a
// cancel or by a confirmed pick — must not survive into the next `o`, or a
// reflexive `o`, enter would start a one-off on whatever was chosen last.
func TestProviderPickerResetsToDefaultOnReopen(t *testing.T) {
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	for _, tc := range []struct {
		name  string
		close tea.Msg
	}{
		{"after esc", tea.KeyPressMsg{Code: tea.KeyEsc}},
		{"after a confirmed non-default pick", tea.KeyPressMsg{Code: tea.KeyEnter}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("a"))
			m.deps.RunSpawn = func(*exec.Cmd) ([]byte, error) { return nil, nil }
			m, _ = pressAction(t, m, keyRune('o'))
			m, _ = update(t, m, down)
			if m.providerPick == defaultProviderIndex() {
				t.Fatal("setup: down should have moved the highlight off the default")
			}
			m, _ = update(t, m, tc.close)
			if m.modal != inputNormal {
				t.Fatalf("setup: picker still open (modal %v)", m.modal)
			}

			m, _ = pressAction(t, m, keyRune('o'))
			if m.modal != inputPickProvider {
				t.Fatalf("o should reopen the picker, got modal %v", m.modal)
			}
			if got := provider.Names()[m.providerPick]; got != provider.Default {
				t.Fatalf("reopened picker highlights %q, want %q", got, provider.Default)
			}
		})
	}
}

// TestCleanEntersConfirmMode is the universal-confirmation contract, over every
// phase class `c` can land on. Terminal phases have NO fast path any more: a
// done/failed session confirms exactly like an active or a stale one, which is
// what guarantees confirmPhase is always the phase the user was shown before
// anything is dispatched. Each case also pins the remembered name/phase, since
// the `y` handler reads those and never the live selection.
func TestCleanEntersConfirmMode(t *testing.T) {
	for _, tt := range []struct {
		class     string
		name      string
		phase     state.Phase
		stale     bool
		wantPhase state.Phase
	}{
		{"terminal", "done-session", state.PhaseDone, false, state.PhaseDone},
		{"active", "active-session", state.PhaseExecuting, false, state.PhaseExecuting},
		{"stale", "stale-session", "", true, ""},
	} {
		t.Run(tt.class, func(t *testing.T) {
			s := sess(tt.name)
			s.Phase = tt.phase
			s.StatusStale = tt.stale
			m := testModel(s)

			// Clean runs from the leader (ctrl+] then c) since T7.2.
			got, cmd := pressAction(t, m, keyRune('c'))
			if got.modal != inputConfirmClean {
				t.Fatalf("c on a %s-phase session should enter confirm mode, got mode %v", tt.class, got.modal)
			}
			assertConfirmOnly(t, cmd)
			if got.confirmTarget != tt.name {
				t.Fatalf("confirmTarget = %q, want %q", got.confirmTarget, tt.name)
			}
			if got.confirmPhase != tt.wantPhase {
				t.Fatalf("confirmPhase = %q, want %q", got.confirmPhase, tt.wantPhase)
			}
		})
	}
}

// TestCleanWithNothingSelectedIsInertWithStatus is the inverse of the old
// bare-list behavior: `c` with an empty session list dispatches nothing at all
// (the shell-out that listed sessions is gone, and its stdout was discarded on
// alt-screen restore anyway) but still says why, because a silently-ignored
// keypress is the one thing every other blocked path in handleKey avoids.
func TestCleanWithNothingSelectedIsInertWithStatus(t *testing.T) {
	got, cmd := refuseAction(testModel(), "c")
	if cmd != nil {
		t.Fatalf("c on an empty list must not dispatch anything, got %v", cmd)
	}
	if got.modal != inputNormal {
		t.Fatalf("c on an empty list should stay in normal mode, got %v", got.modal)
	}
	if got.status == "" {
		t.Fatalf("c on an empty list must set a status line, not be silently ignored")
	}
}

// TestConfirmCleanYDispatchesCleanSession proves y/Y in confirm mode dispatches
// the in-process clean through the Deps.CleanSession seam, carrying the name and
// the phase REMEMBERED at modal-entry time, and returns to normal mode with those
// fields cleared. Running the Cmd is safe and touches no git repo — the seam is a
// stub. Table-driven over both case variants since updateConfirmClean matches 'y'
// and 'Y' identically.
func TestConfirmCleanYDispatchesCleanSession(t *testing.T) {
	for _, r := range []rune{'y', 'Y'} {
		t.Run(string(r), func(t *testing.T) {
			s := sess("active-session")
			s.Phase = state.PhaseExecuting
			m := testModel(s)
			var gotRepo, gotName, gotPhase string
			m.deps.CleanSession = func(repo, name, phase string, _ bool, _ session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
				gotRepo, gotName, gotPhase = repo, name, phase
				return session.CleanResult{Name: name}, nil
			}
			m, _ = pressAction(t, m, keyRune('c'))
			if m.modal != inputConfirmClean {
				t.Fatalf("setup: expected confirm mode, got %v", m.modal)
			}

			got, cmd := update(t, m, keyRune(r))
			if got.modal != inputNormal {
				t.Fatalf("%q should return to normal mode, got %v", r, got.modal)
			}
			if cmd == nil {
				t.Fatalf("%q should dispatch a clean cmd", r)
			}
			if got.confirmTarget != "" || got.confirmPhase != "" {
				t.Fatalf("%q should clear confirmTarget/confirmPhase, got %q/%q", r, got.confirmTarget, got.confirmPhase)
			}

			msg, ok := cmd().(cleanFinishedMsg)
			if !ok {
				t.Fatalf("the clean cmd produced %T, want cleanFinishedMsg", cmd())
			}
			if gotRepo != "/repo" || gotName != "active-session" || gotPhase != string(state.PhaseExecuting) {
				t.Fatalf("CleanSession got (%q, %q, %q), want (/repo, active-session, executing)",
					gotRepo, gotName, gotPhase)
			}
			if msg.name != "active-session" || msg.err != nil {
				t.Fatalf("cleanFinishedMsg = %+v, want name=active-session with no error", msg)
			}
		})
	}
}

// TestConfirmCleanCarriesScratchKindToSeam pins that the kind the user
// CONFIRMED (confirmScratch, set from the row at modal entry) travels all the
// way to the Deps.CleanSession seam, for both kinds. Load-bearing because the
// seam's default (defaultCleanSession → resolveCleanTarget) uses it to match
// the target: a dropped or hardcoded kind here would let a confirmed scratch
// clean resolve to a status-backed session of the same name and run
// snapshot/archive/worktree-remove on work the user never agreed to touch.
func TestConfirmCleanCarriesScratchKindToSeam(t *testing.T) {
	for _, tt := range []struct {
		name    string
		row     Session
		scratch bool
	}{
		{"scratch row confirms as scratch", scratchSess("twin"), true},
		{"planned row confirms as planned", sess("twin"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(tt.row)
			var gotName string
			var gotScratch bool
			m.deps.CleanSession = func(_, name, _ string, scratch bool, _ session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
				gotName, gotScratch = name, scratch
				return session.CleanResult{Name: name}, nil
			}
			m, _ = pressAction(t, m, keyRune('c'))
			if m.modal != inputConfirmClean {
				t.Fatalf("setup: expected confirm mode, got %v", m.modal)
			}
			m, cmd := update(t, m, keyRune('y'))
			if cmd == nil {
				t.Fatal("y should dispatch the clean cmd")
			}
			cmd()
			if gotName != "twin" || gotScratch != tt.scratch {
				t.Fatalf("CleanSession got (%q, scratch=%v), want (twin, scratch=%v)",
					gotName, gotScratch, tt.scratch)
			}
		})
	}
}

// TestConfirmCleanNCancelsWithoutOpeningForm is the n-collision test: n/N in
// confirm mode must cancel back to normal mode with no exec, and critically
// must NOT open the creation form (the collision Sentry flagged between the
// confirm dialog's "n" answer and the global New binding). Table-driven over
// both case variants since updateConfirmClean matches 'n' and 'N' identically.
func TestConfirmCleanNCancelsWithoutOpeningForm(t *testing.T) {
	for _, r := range []rune{'n', 'N'} {
		t.Run(string(r), func(t *testing.T) {
			s := sess("active-session")
			s.Phase = state.PhaseExecuting
			m := testModel(s)
			m, _ = pressAction(t, m, keyRune('c'))

			got, cmd := update(t, m, keyRune(r))
			if got.modal != inputNormal {
				t.Fatalf("%q in confirm mode should return to normal mode, got %v", r, got.modal)
			}
			if got.formOpen {
				t.Fatalf("%q in confirm mode must never open the creation form", r)
			}
			if cmd != nil {
				t.Fatalf("%q in confirm mode should not dispatch a cmd, got %v", r, cmd)
			}
		})
	}
}

// TestConfirmCleanIgnoresUnrecognizedKeys proves any key other than y/Y/n/N/
// esc/ctrl+c leaves the confirm modal open with no Cmd dispatched — per
// updateConfirmClean's doc comment, "every other key is ignored".
func TestConfirmCleanIgnoresUnrecognizedKeys(t *testing.T) {
	for _, k := range []tea.KeyMsg{keyEnter, keyRune('x'), keyRune('z'), keyUp, keyDown} {
		t.Run(fmt.Sprintf("%v", k), func(t *testing.T) {
			s := sess("active-session")
			s.Phase = state.PhaseExecuting
			m := testModel(s)
			m, _ = pressAction(t, m, keyRune('c'))

			got, cmd := update(t, m, k)
			if got.modal != inputConfirmClean {
				t.Fatalf("unrecognized key %v should leave confirm modal open, got mode %v", k, got.modal)
			}
			if cmd != nil {
				t.Fatalf("unrecognized key %v should not dispatch a cmd, got %v", k, cmd)
			}
			if got.confirmTarget != "active-session" {
				t.Fatalf("unrecognized key %v should not clear confirmTarget, got %q", k, got.confirmTarget)
			}
		})
	}
}

// TestConfirmCleanEscCancels proves esc cancels confirm mode with no Cmd.
func TestConfirmCleanEscCancels(t *testing.T) {
	s := sess("active-session")
	s.Phase = state.PhaseExecuting
	m := testModel(s)
	m, _ = pressAction(t, m, keyRune('c'))

	got, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got.modal != inputNormal {
		t.Fatalf("esc should return to normal mode, got %v", got.modal)
	}
	if cmd != nil {
		t.Fatalf("esc cancel should return no cmd")
	}
}

// TestConfirmCleanCtrlCQuits proves the universal escape hatch works inside
// the confirm modal.
func TestConfirmCleanCtrlCQuits(t *testing.T) {
	s := sess("active-session")
	s.Phase = state.PhaseExecuting
	m := testModel(s)
	m, _ = pressAction(t, m, keyRune('c'))

	got, cmd := update(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !got.quitting {
		t.Fatalf("ctrl+c in confirm mode must quit")
	}
	if cmd == nil {
		t.Fatalf("ctrl+c should return a tea.Quit cmd")
	}
}

// confirmClean drives `c` then `y` on the session at the cursor, the way a user
// does, and returns the post-confirmation Model plus the dispatched clean Cmd.
func confirmClean(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, _ = pressAction(t, m, keyRune('c'))
	if m.modal != inputConfirmClean {
		t.Fatalf("setup: c should have entered confirm mode, got %v", m.modal)
	}
	m, cmd := update(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("setup: y should have dispatched a clean cmd")
	}
	return m, cmd
}

// TestCleanFinishedSuccessReportsArchivePathFirst pins the success message's
// content AND its field order for a clean that KEPT its branch — the case with
// no recovery command, where the archive is the leading datum.
//
// The order is driven by the toast's 3-line clip, not by the footer truncation
// that used to justify it: what leads is what survives the cut. Within a kept
// branch clean the archive is the only irrecoverable item, so it comes
// straight after the outcome, ahead of the snapshot note and the branch note —
// both of which git still holds. It is named by basename now; the archive root
// is one listing away, and the toast has no room to spend on a path prefix.
//
// The deleted-branch case inverts nothing here — it inserts the recovery
// command AHEAD of the archive, which
// TestCleanSucceededStatusPutsRecoveryAheadOfTheArchive pins separately.
func TestCleanFinishedSuccessReportsArchivePathFirst(t *testing.T) {
	m := testModel(sess("a"))
	got, cmd := update(t, m, cleanFinishedMsg{
		name: "a",
		result: session.CleanResult{
			Name:              "a",
			Branch:            "a",
			WorktreePath:      "/wt/a",
			ArchivePath:       "/archive/repo-a-2026-07-29",
			SnapshotCommitted: true,
		},
	})
	if cmd == nil {
		t.Fatalf("a finished clean must dispatch a reload — that is what removes the row")
	}
	if !strings.Contains(got.status, "archived as repo-a-2026-07-29") {
		t.Fatalf("status = %q, want the archive basename", got.status)
	}
	if strings.Contains(got.status, "/archive/") {
		t.Fatalf("status = %q, want the archive named by basename, not by full path", got.status)
	}
	if !strings.Contains(got.status, "snapshot") {
		t.Fatalf("status = %q, want the snapshot commit mentioned", got.status)
	}
	if !strings.Contains(got.status, "branch a kept") {
		t.Fatalf("status = %q, want the surviving branch mentioned", got.status)
	}
	archiveAt := strings.Index(got.status, "archived as")
	if snapAt := strings.Index(got.status, "snapshot"); archiveAt > snapAt {
		t.Fatalf("status = %q, want the archive BEFORE the snapshot note", got.status)
	}
	if branchAt := strings.Index(got.status, "branch a kept"); archiveAt > branchAt {
		t.Fatalf("status = %q, want the archive BEFORE the branch note", got.status)
	}
}

// TestCleanFinishedSuccessOmitsUnsetResultFields proves a clean that committed
// nothing says nothing about a snapshot — the summary reports what happened, not
// a fixed set of fields.
func TestCleanFinishedSuccessOmitsUnsetResultFields(t *testing.T) {
	m := testModel(sess("a"))
	got, _ := update(t, m, cleanFinishedMsg{
		name:   "a",
		result: session.CleanResult{Name: "a", Branch: "a", ArchivePath: "/archive/a"},
	})
	if strings.Contains(got.status, "snapshot") {
		t.Fatalf("status = %q, must not claim a snapshot commit that did not happen", got.status)
	}
}

// TestCleanFinishedErrorShowsFoldedErrorOnStatus covers the failure branch: the
// status names the session and carries the seam's error text (which is what
// surfaces resolveCleanTarget's refusals), folded to a single row the way the
// retired shell-out folded its stderr tail. The reload still fires — the row is
// unchanged, but its state on disk may not be.
func TestCleanFinishedErrorShowsFoldedErrorOnStatus(t *testing.T) {
	m := testModel(sess("a"))
	got, cmd := update(t, m, cleanFinishedMsg{
		name: "a",
		err:  errors.New("removing worktree: line one\nline two"),
	})
	if cmd == nil {
		t.Fatalf("a failed clean must still dispatch a reload")
	}
	if !strings.Contains(got.status, "a") || !strings.Contains(got.status, "failed") {
		t.Fatalf("status = %q, want it to name the session and mention failure", got.status)
	}
	if !strings.Contains(got.status, "removing worktree") {
		t.Fatalf("status = %q, want the error text preserved", got.status)
	}
	if strings.Contains(got.status, "\n") {
		t.Fatalf("status = %q, must not contain a raw newline", got.status)
	}
	if !strings.Contains(got.status, "line one · line two") {
		t.Fatalf("status = %q, want newlines folded to \" · \"", got.status)
	}
}

// TestCleanFinishedErrorReportsArchivePathFirst is the error branch's ordering
// contract. A clean that archived and THEN failed (the `git worktree remove` step
// is the realistic case) has already deleted the source `.afk/{name}/`, so the
// archive is the only copy — and the row survives, still suggesting nothing
// happened. The toast clips at 3 lines, so the archive must lead and a long git
// error must be what the clip drops.
//
// It is named by basename, as in the success message. The two describe the same
// directory, and the whole point of the copy pass is that they cannot disagree
// about how to write it; the archive root is one listing away either way.
func TestCleanFinishedErrorReportsArchivePathFirst(t *testing.T) {
	m := testModel(sess("a"))
	got, cmd := update(t, m, cleanFinishedMsg{
		name: "a",
		result: session.CleanResult{
			Name:         "a",
			Branch:       "a",
			WorktreePath: "/wt/a",
			ArchivePath:  "/archive/repo-a-2026-07-29",
		},
		err: errors.New("git worktree remove /wt/a: exit status 128"),
	})
	if cmd == nil {
		t.Fatalf("a failed clean must still dispatch a reload")
	}
	if !strings.Contains(got.status, "archiving as repo-a-2026-07-29") {
		t.Fatalf("status = %q, want the archive of a clean that archived before failing", got.status)
	}
	archiveAt := strings.Index(got.status, "archiving as")
	if errAt := strings.Index(got.status, "exit status 128"); archiveAt > errAt {
		t.Fatalf("status = %q, want the archive BEFORE the error text", got.status)
	}
}

// TestCleanFinishedErrorOmitsArchivePathWhenNothingWasArchived pins the other
// half of that ordering: a refusal, or any failure before the archive step, has
// an empty ArchivePath, and an "after archiving to" clause there would be a lie
// about what happened on disk.
func TestCleanFinishedErrorOmitsArchivePathWhenNothingWasArchived(t *testing.T) {
	m := testModel(sess("a"))
	got, _ := update(t, m, cleanFinishedMsg{
		name: "a",
		err:  errors.New(`no session named "a" found`),
	})
	if strings.Contains(got.status, "archiv") {
		t.Fatalf("status = %q, must not mention archiving when nothing was archived", got.status)
	}
	if !strings.Contains(got.status, `no session named "a" found`) {
		t.Fatalf("status = %q, want the refusal text", got.status)
	}
}

// TestCleanFinishedStatusTruncatesToModelWidth is the geometry assertion ported
// off the retired TestExecFinishedLongStderrTruncatesToModelWidth. What it
// guarded is that an arbitrarily long archive path or git error cannot corrupt
// the footer's height math on a narrow terminal — it used to hold because
// renderFooter truncated the status row it printed, and it holds now because
// the footer prints no message at all: the text goes to the toast, which wraps
// into a box of its own.
//
// The success row's length no longer comes from the archive path — the copy
// pass shortened that to a basename, so an arbitrarily deep archive directory
// produces a SHORT message now. It comes from the skipped-step notes instead,
// which are the one clean field with no bound on its length and the one the
// value ordering deliberately puts last. Keep a long field in this row or it
// stops measuring anything: the whole claim is that no clean result, however
// long, can reach the footer's height math.
func TestCleanFinishedStatusTruncatesToModelWidth(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  cleanFinishedMsg
	}{
		{"success", cleanFinishedMsg{name: "a", result: session.CleanResult{
			Name:        "a",
			Branch:      "a",
			ArchivePath: "/archive/" + strings.Repeat("deep/", 40) + "a",
			Notes:       strings.Repeat("a skipped step ", 20),
		}}},
		{"failure", cleanFinishedMsg{
			name: "a",
			err:  errors.New(strings.Repeat("failure detail ", 20)),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("a"))
			m.width = 20
			got, _ := update(t, m, tt.msg)
			if got.status == "" {
				t.Fatalf("a finished clean must say something — there is nothing to measure otherwise")
			}
			footer := xansi.Strip(got.renderFooter())
			if h := lipgloss.Height(footer); h != 1 {
				t.Fatalf("footer is %d rows after a %s clean at width %d, want 1: %q",
					h, tt.name, got.width, footer)
			}
			if w := lipgloss.Width(strings.TrimRight(footer, " ")); w > got.width {
				t.Fatalf("footer from cleanFinishedMsg exceeds width %d (got %d): %q",
					got.width, w, footer)
			}
		})
	}
}

// TestSecondCleanOnACleaningSessionIsRefused is the in-flight guard: pressing
// `c` again on a session whose clean has not reported back must NOT re-enter the
// modal and must NOT dispatch, because two concurrent cleans of one session mean
// two `git add -A`/`commit` against one index and two `git worktree remove`.
// The old suspended shell-out made this impossible; in-process only this set does.
func TestSecondCleanOnACleaningSessionIsRefused(t *testing.T) {
	m := testModel(sess("a"))
	calls := 0
	m.deps.CleanSession = func(_, name, _ string, _ bool, _ session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
		calls++
		return session.CleanResult{Name: name}, nil
	}
	m, first := confirmClean(t, m)

	got, cmd := refuseAction(m, "c")
	if cmd != nil {
		t.Fatalf("a second c on a cleaning session must not dispatch, got %v", cmd)
	}
	if got.modal != inputNormal {
		t.Fatalf("a second c on a cleaning session must not open the modal, got mode %v", got.modal)
	}
	if !strings.Contains(got.status, "already cleaning") {
		t.Fatalf("status = %q, want the refusal stated, never silence", got.status)
	}
	// Only the first dispatch ever reaches the seam.
	first()
	if calls != 1 {
		t.Fatalf("CleanSession called %d times, want exactly 1", calls)
	}
}

// TestEnterOnACleaningSessionOpensNoPane guards the other side of the
// in-flight set: a clean stops the session's host and deletes its worktree, so
// opening a pane over it would put a live view on a host the clean is about to
// SIGKILL. A cleaning row has no live agent, so enter focuses its overview —
// which is where the clean's progress is read — and dials nothing.
func TestEnterOnACleaningSessionOpensNoPane(t *testing.T) {
	m := testModel(runningSess("a"))
	opens := 0
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		opens++
		return newFakePane("X"), nil
	}
	m, _ = confirmClean(t, m)

	got, cmd := update(t, m, keyEnter)
	if cmd != nil {
		t.Fatalf("enter on a cleaning session must not dispatch, got %v", cmd)
	}
	if got.pane != nil || opens != 0 {
		t.Fatalf("no pane should be opened over a cleaning session (opens=%d)", opens)
	}
	if got.focus != focusRight || !got.detailDrawn() {
		t.Fatalf("focus = %v detailDrawn = %v, want the cleaning row's overview focused", got.focus, got.detailDrawn())
	}
}

// TestEveryPaneEntranceRefusesACleaningSession is the in-flight guard over EVERY
// path that reaches attachPaneCmd, not just the keypresses. Guarding only the
// keypresses leaves the async chains open, and the sharpest of those needs no
// unusual timing at all: `r` on a STOPPED session dispatches a host restart,
// and `c`,`y` on it while that restart is in flight lands hostRestartedMsg on a
// session the clean is about to SIGKILL. The post-create auto-open and the
// startup pane are the same shape. Enter is the one entrance that does not
// state a refusal: a cleaning row has no live agent, so enter focuses its
// overview, which says what is happening.
//
// Each case drives the real entrance rather than calling openPaneFor directly, so
// a future caller that bypasses the gate fails here.
func TestEveryPaneEntranceRefusesACleaningSession(t *testing.T) {
	startupPane := func(t *testing.T, m Model) (Model, tea.Cmd) {
		m.initialSession = "a"
		m.firstLoadDone = true
		nm, cmd := m.maybeOpenInitialPane()
		got, ok := nm.(Model)
		if !ok {
			t.Fatalf("maybeOpenInitialPane returned %T, want ui.Model", nm)
		}
		return got, cmd
	}

	for _, tt := range []struct {
		entrance string
		// stopped starts the session with no host, so the entrance's own
		// restart-first branch is the one under test rather than the direct open.
		stopped bool
		// arrive drives the entrance with "a" already mid-clean and returns the
		// resulting Model plus Cmd.
		arrive func(t *testing.T, m Model) (Model, tea.Cmd)
		// focusesOverview marks enter: no refusal toast, the overview is focused.
		focusesOverview bool
	}{
		{
			entrance: "enter",
			arrive: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return update(t, m, keyEnter)
			},
			focusesOverview: true,
		},
		{
			entrance: "r on a stopped session (must not even restart the host)",
			stopped:  true,
			arrive: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return refuseAction(m, "r")
			},
		},
		{
			entrance: "hostRestartedMsg (the restart chain)",
			arrive: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return update(t, m, hostRestartedMsg{name: "a", worktree: "/wt/a"})
			},
		},
		{
			entrance: "spawnFinishedMsg (the post-create auto-open)",
			arrive: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return update(t, m, spawnFinishedMsg{name: "a"})
			},
		},
		{
			entrance: "maybeOpenInitialPane (the startup pane)",
			arrive:   startupPane,
		},
		{
			entrance: "maybeOpenInitialPane on a stopped session (must not even restart the host)",
			stopped:  true,
			arrive:   startupPane,
		},
	} {
		t.Run(tt.entrance, func(t *testing.T) {
			s := runningSess("a")
			if tt.stopped {
				s = sess("a")
			}
			m := testModel(s)
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				t.Fatalf("%s must never open a pane over a cleaning session", tt.entrance)
				return nil, nil
			}
			m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
				t.Fatalf("%s must never restart the host of a cleaning session", tt.entrance)
				return session.HostProcess{}, nil
			}
			m, _ = confirmClean(t, m)
			if !m.cleaningSessions[cleanIDByName("a")] {
				t.Fatalf("setup: a should be mid-clean")
			}

			got, cmd := tt.arrive(t, m)
			// Running whatever Cmd came back is the assertion: OpenHostPane
			// fails the test if the gate let an attach through. A reload Cmd is
			// fine and expected on the async entrances.
			if cmd != nil {
				drainBatch(t, cmd)
			}
			if got.pane != nil {
				t.Fatalf("%s opened a pane over a cleaning session", tt.entrance)
			}
			if tt.focusesOverview {
				if got.focus != focusRight || !got.detailDrawn() {
					t.Fatalf("%s: focus = %v detailDrawn = %v, want the overview focused", tt.entrance, got.focus, got.detailDrawn())
				}
				return
			}
			if !strings.Contains(got.status, "can't open a") && !strings.Contains(got.status, "can't restart a") {
				t.Fatalf("%s: status = %q, want the refusal stated, never silence", tt.entrance, got.status)
			}
		})
	}
}

// TestPaneArrivingForACleaningSessionIsDroppedNotInstalled covers the entrance no
// dispatch-time gate can see: the connection was already in flight when `c`,`y`
// confirmed the clean, so openPaneFor had nothing to refuse. Installing it would
// put a live pane on a host the clean is about to SIGKILL, in a worktree it is
// deleting. The arriving handle must also be CLOSED, or the surplus connection
// leaks a client slot on a host with no handle left to close it.
func TestPaneArrivingForACleaningSessionIsDroppedNotInstalled(t *testing.T) {
	m := testModel(runningSess("a"))
	m, _ = confirmClean(t, m)
	if !m.cleaningSessions[cleanIDByName("a")] {
		t.Fatalf("setup: a should be mid-clean")
	}

	arriving := newFakePane("late")
	got, cmd := update(t, m, paneAttachedMsg{name: "a", pane: arriving})
	if cmd != nil {
		drainBatch(t, cmd)
	}

	if got.pane != nil {
		t.Fatalf("a pane arriving for a cleaning session must not be installed")
	}
	if got.paneName != "" {
		t.Fatalf("paneName = %q, want it left empty", got.paneName)
	}
	if arriving.closed != 1 {
		t.Fatalf("the dropped connection was closed %d times, want exactly 1", arriving.closed)
	}
	if !strings.Contains(got.status, "can't open a") {
		t.Fatalf("status = %q, want the refusal stated, never silence", got.status)
	}
}

// TestRestartLandingForACleaningSessionOpensNoPane is the restart chain's half of
// the same race. The respawn cannot be un-dispatched — the host is back up by the
// time this message lands — but the clean stops it again on its way through, so
// what must not happen is a pane opening onto it or a status claiming success.
func TestRestartLandingForACleaningSessionOpensNoPane(t *testing.T) {
	m := testModel(sess("a"))
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		t.Fatal("a restart landing mid-clean must never open a pane")
		return nil, nil
	}
	m, _ = confirmClean(t, m)

	got, cmd := update(t, m, hostRestartedMsg{name: "a", worktree: "/wt/a"})
	if cmd != nil {
		drainBatch(t, cmd)
	}
	if got.pane != nil {
		t.Fatalf("a restart landing mid-clean must not install a pane")
	}
	if strings.Contains(got.status, "restarted") {
		t.Fatalf("status = %q, must not claim a successful restart mid-clean", got.status)
	}
	if !strings.Contains(got.status, "can't open a") {
		t.Fatalf("status = %q, want the refusal stated", got.status)
	}
}

// TestCleanFinishedUnderAModalKeepsItsStatusUntilDismissal covers the message
// that lands while a confirmation dialog is up. The dialog floats over the
// frame (modal.go) and the toast composes above it (View), so the clean's
// archive — a datum nothing else reports — is on screen the moment it lands;
// and dismissing the dialog leaves the status alone, so within toastTTL the
// same toast is still there afterwards.
func TestCleanFinishedUnderAModalKeepsItsStatusUntilDismissal(t *testing.T) {
	m := testModel(sess("a"), sess("b"))
	m, _ = confirmClean(t, m) // cleans "a"; cursor is still on it

	// A second confirm modal goes up (the user moved on to another row) while
	// the first clean is still running.
	m.selectSessionIndex(1)
	m, _ = pressAction(t, m, keyRune('c'))
	if m.modal != inputConfirmClean {
		t.Fatalf("setup: expected a second confirm modal, got mode %v", m.modal)
	}

	m, _ = update(t, m, cleanFinishedMsg{
		name:   "a",
		result: session.CleanResult{Name: "a", Branch: "a", ArchivePath: "/archive/repo-a"},
	})
	if !strings.Contains(render(m), "archived as repo-a") {
		t.Fatalf("the toast over the dialog should already show the clean's archive:\n%s", render(m))
	}

	// Cancelling the dialog leaves the status as it was — the archive stays.
	got, _ := update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !strings.Contains(got.status, "archived as repo-a") {
		t.Fatalf("status = %q, want the deferred archive once the modal closed", got.status)
	}
	if !strings.Contains(render(got), "archived as repo-a") {
		t.Fatalf("the toast should still show the clean's archive once the modal closed:\n%s", render(got))
	}
}

// TestQuitDuringACleanQuitsImmediately pins the deliberate absence of a mid-clean
// quit guard. Quitting abandons the clean's Cmd goroutine, which can leave a
// half-applied clean — but that window cannot be closed without a
// context.Context in session.CleanSession's fixed signature, and a two-press
// warning was tried and withdrawn because a single never-reset flag warned once
// per process rather than once per clean. An honest immediate quit is the
// documented behavior in README.md and `dash --help`.
func TestQuitDuringACleanQuitsImmediately(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = confirmClean(t, m)
	if !m.cleaningSessions[cleanIDByName("a")] {
		t.Fatalf("setup: a should be marked in-flight after y")
	}

	got, cmd := pressAction(t, m, keyRune('q'))
	if !got.quitting || cmd == nil {
		t.Fatalf("q should quit immediately even mid-clean: quitting=%v cmd=%v", got.quitting, cmd)
	}
}

// TestCleanFinishedClearsTheInFlightEntry proves the guard releases on BOTH
// branches. A failure that left the entry set would lock the row out of `c` and
// `enter` for the rest of the process's life, so the retry path matters more than
// the success path here.
func TestCleanFinishedClearsTheInFlightEntry(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{{"success", nil}, {"failure", errors.New("boom")}} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("a"))
			m, _ = confirmClean(t, m)
			if !m.cleaningSessions[cleanIDByName("a")] {
				t.Fatalf("setup: a should be marked in-flight after y")
			}

			got, _ := update(t, m, cleanFinishedMsg{name: "a", err: tt.err})
			if got.cleaningSessions[cleanIDByName("a")] {
				t.Fatalf("the in-flight entry must be cleared once the clean reports back")
			}
			// The row is now free to confirm again.
			again, cmd := pressAction(t, got, keyRune('c'))
			if again.modal != inputConfirmClean {
				t.Fatalf("c should confirm again after the clean finished: mode=%v", again.modal)
			}
			assertConfirmOnly(t, cmd)
		})
	}
}

// TestCleanEndToEndWalkRemovesTheRow is the full happy path in one test:
// c → modal → y → the seam sees the confirmed name and phase → the message lands
// on Update → the reload it returns delivers a session list without the cleaned
// session → the row is gone and the cursor is clamped rather than panicking.
func TestCleanEndToEndWalkRemovesTheRow(t *testing.T) {
	m := testModel(sess("a"), sess("b"))
	m.selectSessionIndex(1) // clean the LAST row, so removing it must clamp the cursor
	var gotName, gotPhase string
	m.deps.CleanSession = func(_, name, phase string, _ bool, _ session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
		gotName, gotPhase = name, phase
		return session.CleanResult{
			Name:        name,
			Branch:      name,
			ArchivePath: "/archive/repo-b",
		}, nil
	}

	m, cmd := confirmClean(t, m)
	if !strings.Contains(m.status, "cleaning b") {
		t.Fatalf("status = %q, want the in-progress notice while the clean runs", m.status)
	}

	msg, ok := cmd().(cleanFinishedMsg)
	if !ok {
		t.Fatalf("the clean cmd produced %T, want cleanFinishedMsg", cmd())
	}
	if gotName != "b" || gotPhase != string(state.PhaseExecuting) {
		t.Fatalf("the seam saw (%q, %q), want (b, executing)", gotName, gotPhase)
	}

	m, reload := update(t, m, msg)
	if reload == nil {
		t.Fatalf("the finished clean must dispatch a reload")
	}
	if !strings.Contains(m.status, "archived as repo-b") {
		t.Fatalf("status = %q, want the archive named", m.status)
	}

	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("a")}})
	if names := sessionNames(m); len(names) != 1 || names[0] != "a" {
		t.Fatalf("sessions = %v, want just [a] once the clean is discovered", names)
	}
	if m.listSelection() != 0 {
		t.Fatalf("cursor = %d, want it clamped to 0 after the last row went away", m.listSelection())
	}
	if _, ok := m.selected(); !ok {
		t.Fatalf("selected() must stay valid after the cleaned row was removed")
	}
}

// TestCleanEndToEndFailureWalkKeepsTheRow is the mirror image: the seam fails,
// the error reaches the status line, the reload still fires, and the session is
// still there and still cleanable.
func TestCleanEndToEndFailureWalkKeepsTheRow(t *testing.T) {
	m := testModel(sess("a"), sess("b"))
	m.selectSessionIndex(1)
	m.deps.CleanSession = func(_, name, phase string, _ bool, _ session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
		return session.CleanResult{}, fmt.Errorf(
			"session %q changed phase from %q to %q since you confirmed — not cleaning",
			name, phase, "done")
	}

	m, cmd := confirmClean(t, m)
	msg, ok := cmd().(cleanFinishedMsg)
	if !ok {
		t.Fatalf("the clean cmd produced %T, want cleanFinishedMsg", cmd())
	}
	if msg.err == nil {
		t.Fatalf("the seam's error must ride the message")
	}

	m, reload := update(t, m, msg)
	if reload == nil {
		t.Fatalf("a failed clean must still dispatch a reload")
	}
	if !strings.Contains(m.status, "changed phase") {
		t.Fatalf("status = %q, want the seam's refusal surfaced", m.status)
	}

	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("a"), sess("b")}})
	if names := sessionNames(m); len(names) != 2 {
		t.Fatalf("sessions = %v, want the refused session still present", names)
	}
	got, again := pressAction(t, m, keyRune('c'))
	if got.modal != inputConfirmClean {
		t.Fatalf("a refused clean must be retryable: mode=%v", got.modal)
	}
	assertConfirmOnly(t, again)
}

// --- background spawn: placeholders, chaining, error modal ---

// submitForm drives the creation form the way a user does: `n`, the name typed
// rune by rune, then enter. It returns the post-submit Model and the dispatched
// Cmd.
//
// The returned Cmd is deliberately NEVER called by these tests. Calling it would
// run CombinedOutput on afkBinary(), which under `go test` is the test binary
// itself — i.e. it would re-execute the test suite with argv "new --no-dash …".
func submitForm(t *testing.T, m Model, name string) (Model, tea.Cmd) {
	t.Helper()
	m, _ = pressAction(t, m, keyRune('n'))
	m = typeRunes(t, m, name)
	return update(t, m, keyEnter)
}

// TestFormSubmitShowsPlaceholderInTheCanonicalSlice is the core non-blocking
// contract: submitting the name dispatches a Cmd and puts a "creating…" row in
// m.sessions ITSELF — not in a render-time overlay — because the list's items,
// selected() and every keybinding index that one slice.
func TestFormSubmitShowsPlaceholderInTheCanonicalSlice(t *testing.T) {
	m := testModel(sess("aaa"), sess("zzz"))
	got, cmd := submitForm(t, m, "mmm")

	if cmd == nil {
		t.Fatalf("submitting a valid name should dispatch the spawn cmd")
	}
	if got.formOpen {
		t.Fatalf("submitting should close the form")
	}
	if got.modal != inputNormal {
		t.Fatalf("submitting should stay in normal mode, got %v", got.modal)
	}
	if len(got.pendingSpawns) != 1 || got.pendingSpawns[0] != "mmm" {
		t.Fatalf("pendingSpawns = %v, want [mmm]", got.pendingSpawns)
	}
	// Sorted insertion: discover returns sessions name-sorted, so the placeholder
	// belongs between aaa and zzz, not appended at the end.
	names := sessionNames(got)
	if !slicesEqual(names, []string{"aaa", "mmm", "zzz"}) {
		t.Fatalf("sessions = %v, want the placeholder spliced in sorted order", names)
	}
	idx, ok := got.sessionIndex("mmm")
	if !ok || !got.sessions[idx].Pending {
		t.Fatalf("the mmm row must be marked Pending; got ok=%v row=%+v", ok, got.sessions[idx])
	}
	if !strings.Contains(got.status, "creating mmm") {
		t.Fatalf("status = %q, want a creating notice", got.status)
	}
	if !strings.Contains(render(got), "creating…") {
		t.Fatalf("View() should render the placeholder row:\n%s", render(got))
	}
}

// TestFormSubmitKeepsTheCursorOnTheSelectedSession proves the sorted insertion
// does not silently move the selection: inserting a row at or before the cursor
// shifts the selected session's index, and the cursor must follow it.
func TestFormSubmitKeepsTheCursorOnTheSelectedSession(t *testing.T) {
	m := testModel(sess("aaa"), sess("zzz"))
	m.selectSessionIndex(1) // zzz
	got, _ := submitForm(t, m, "mmm")

	s, ok := got.selected()
	if !ok || s.Name != "zzz" {
		t.Fatalf("selected() = %+v (ok=%v), want zzz to still be selected", s, ok)
	}
}

// TestPlaceholderSurvivesDiscoveryTicksThenYieldsToTheRealSession is the
// regression test for the merge living inside applyLoaded: a one-time merge at
// keypress time is wiped by the next 2s tick, which would make the placeholder
// flicker away seconds before the session actually exists.
func TestPlaceholderSurvivesDiscoveryTicksThenYieldsToTheRealSession(t *testing.T) {
	m := testModel(sess("aaa"))
	m, _ = submitForm(t, m, "mmm")

	// Two ticks' worth of discovery results that do not know about mmm yet.
	for i := 0; i < 2; i++ {
		m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("aaa")}, at: time.Unix(10, 0)})
		idx, ok := m.sessionIndex("mmm")
		if !ok || !m.sessions[idx].Pending {
			t.Fatalf("tick %d: the placeholder was wiped by applyLoaded; sessions = %v", i, sessionNames(m))
		}
	}

	// Discovery finally reports the real session: the placeholder must yield to
	// it rather than sitting alongside it as a duplicate row.
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen,
		sessions: []Session{sess("aaa"), runningSess("mmm")},
		at:       time.Unix(12, 0),
	})
	if len(m.pendingSpawns) != 0 {
		t.Fatalf("pendingSpawns = %v, want empty once discovery reports the session", m.pendingSpawns)
	}
	if got := sessionNames(m); !slicesEqual(got, []string{"aaa", "mmm"}) {
		t.Fatalf("sessions = %v, want no duplicate mmm row", got)
	}
	idx, _ := m.sessionIndex("mmm")
	if m.sessions[idx].Pending {
		t.Fatalf("the real mmm row must not be marked Pending")
	}
}

// TestPlaceholderRowIsInert proves the per-session keys ignore a placeholder.
// `c` is the dangerous one: it must NOT fall through to the empty-selection
// branch that runs a bare `afk clean`. `r` must not spawn a host for a row
// that has none yet. Enter is not in this list: a pending row has no live
// agent, so enter focuses its overview (no Cmd, no spawn — see
// TestEnterAndFocusKeyCurrentViewMatrix), which is a focus move, not inertness.
func TestPlaceholderRowIsInert(t *testing.T) {
	for _, k := range []tea.KeyMsg{keyCtrlT, keyRune('r'), keyRune('c')} {
		t.Run(fmt.Sprintf("%v", k), func(t *testing.T) {
			m := testModel()
			m, _ = submitForm(t, m, "mmm")
			m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
				t.Fatal("a placeholder row must never trigger a host spawn")
				return session.HostProcess{}, nil
			}
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				t.Fatal("a placeholder row must never open a pane")
				return nil, nil
			}
			m.status = ""

			// Bare ctrl+t is a dead key now; r and c are reached through the
			// handler the leader row runs (the dialog may dim them, and this
			// test is about what the handler does with a placeholder).
			got, cmd := update(t, m, k)
			if k != keyCtrlT {
				got, cmd = refuseAction(m, k.String())
			}
			if cmd != nil {
				t.Fatalf("%v on a placeholder should dispatch nothing, got a cmd", k)
			}
			if got.modal != inputNormal {
				t.Fatalf("%v on a placeholder should not open a modal, got mode %v", k, got.modal)
			}
			if got.status != "" {
				t.Fatalf("%v on a placeholder should not set a status, got %q", k, got.status)
			}
		})
	}
}

// TestSpawnFinishedSuccessReloadsAndChainsThePaneOpen proves the success path
// does BOTH: reload (so the real session lands in the list) and connect (so the
// user ends up watching the session they just created, inside the pane). Both
// sub-Cmds are inspected through drainBatch — tea.BatchMsg is exported, the
// individual Cmds are not otherwise distinguishable.
func TestSpawnFinishedSuccessReloadsAndChainsThePaneOpen(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m, _ = submitForm(t, m, "alpha")
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		if cfg.Name != "alpha" {
			t.Fatalf("OpenHostPane called for %q, want alpha", cfg.Name)
		}
		return newFakePane("NEW"), nil
	}

	got, cmd := update(t, m, spawnFinishedMsg{name: "alpha"})
	if cmd == nil {
		t.Fatalf("a successful spawn should dispatch reload+connect")
	}
	if got.modal == inputSpawnError {
		t.Fatalf("a successful spawn must not enter the error modal")
	}
	if !strings.Contains(got.status, "created alpha") || !strings.Contains(got.status, "connecting") {
		t.Fatalf("status = %q, want it to announce the connect", got.status)
	}
	if len(got.pendingSpawns) != 0 {
		t.Fatalf("pendingSpawns = %v, want empty after the spawn finished", got.pendingSpawns)
	}
	for _, s := range got.sessions {
		if s.Pending {
			t.Fatalf("the placeholder must be gone once the spawn finished: %v", sessionNames(got))
		}
	}

	var sawLoad, sawAttach bool
	for _, msg := range drainBatch(t, cmd) {
		switch msg := msg.(type) {
		case sessionsLoadedMsg:
			sawLoad = true
		case paneAttachedMsg:
			sawAttach = true
			if msg.err != nil || msg.name != "alpha" {
				t.Fatalf("paneAttachedMsg = %+v, want a successful open for alpha", msg)
			}
			if msg.restarted {
				t.Fatalf("a create's own connect is not a restart")
			}
		}
	}
	if !sawLoad {
		t.Fatalf("the batch is missing the reload cmd")
	}
	if !sawAttach {
		t.Fatalf("the batch is missing the pane-connect cmd")
	}
}

// TestSpawnFinishedSuccessWithAPaneOpenOnlyReloads pins the pane's one-at-a-time
// invariant against a background create finishing: the new session's host runs
// regardless, so the dashboard says where it went rather than displacing the
// session the user is currently watching.
func TestSpawnFinishedSuccessWithAPaneOpenOnlyReloads(t *testing.T) {
	m := testModel(runningSess("watching"))
	m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "watching"})
	m, _ = submitForm(t, m, "alpha")
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		t.Fatalf("a create finishing must not displace the open pane")
		return nil, nil
	}

	got, cmd := update(t, m, spawnFinishedMsg{name: "alpha"})
	if got.paneName != "watching" {
		t.Fatalf("paneName = %q, want the pane left on watching", got.paneName)
	}
	if cmd == nil {
		t.Fatalf("the spawn finishing should still trigger a reload")
	}
	if _, ok := cmd().(tea.BatchMsg); ok {
		t.Fatalf("spawn success with a pane open must not batch a connect — reload only")
	}
	if _, ok := cmd().(sessionsLoadedMsg); !ok {
		t.Fatalf("expected the reload cmd, got %T", cmd())
	}
	if !strings.Contains(got.status, "background") || !strings.Contains(got.status, got.keys.Focus.Help().Key+" to open") {
		t.Fatalf("status = %q, want the where-it-went guidance", got.status)
	}
}

// TestSpawnFinishedSuccessShowsWarningsInStatus proves a successful background
// create surfaces its best-effort "warning:" lines (Plan.Run's bestEffort branch) on the
// status row instead of disappearing silently: the primary failure mode this
// closes is an untracked base picked on a wt+gs machine, where the create
// exits 0 with no output shown anywhere else.
func TestSpawnFinishedSuccessShowsWarningsInStatus(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []string
		notWant []string
	}{
		{
			name:   "no warnings leaves status unchanged from today",
			output: "afk new: creating worktree\n",
			want:   nil,
			notWant: []string{
				"warning", "more)",
			},
		},
		{
			name:   "one warning is appended",
			output: `warning: base "side" is not git-spice-tracked (no status.json for base)` + "\n",
			want: []string{
				`base "side" is not git-spice-tracked (no status.json for base)`,
			},
			notWant: []string{"more)"},
		},
		{
			name: "a second warning collapses to a count",
			output: `warning: base "side" is not git-spice-tracked (no status.json for base)` + "\n" +
				`warning: Track branch "e2e" with git-spice (base "side"): base "side" is not tracked; skipping` + "\n",
			want: []string{
				`base "side" is not git-spice-tracked (no status.json for base)`,
				"(+1 more)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(runningSess("alpha"))
			m, _ = submitForm(t, m, "alpha")
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				return newFakePane("NEW"), nil
			}

			got, _ := update(t, m, spawnFinishedMsg{name: "alpha", output: tt.output})

			if got.modal == inputSpawnError {
				t.Fatalf("a successful spawn must not enter the error modal")
			}
			if !strings.Contains(got.status, "created alpha") {
				t.Fatalf("status = %q, want it to still announce the create", got.status)
			}
			for _, w := range tt.want {
				if !strings.Contains(got.status, w) {
					t.Fatalf("status = %q, want it to contain %q", got.status, w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got.status, w) {
					t.Fatalf("status = %q, must not contain %q", got.status, w)
				}
			}
		})
	}
}

// TestSpawnFinishedFailureEntersTheErrorModal covers the whole failure path: the
// modal opens with the captured output's tail (the only record of why — nothing
// was ever printed to the terminal), the placeholder is dropped, a reload is
// still triggered, and esc dismisses.
func TestSpawnFinishedFailureEntersTheErrorModal(t *testing.T) {
	m := testModel()
	m, _ = submitForm(t, m, "alpha")

	output := "afk new: creating worktree\n\nafk new: fatal: branch 'alpha' already exists\n"
	got, reload := update(t, m, spawnFinishedMsg{
		name:   "alpha",
		err:    errors.New("exit status 1"),
		output: output,
	})

	if got.modal != inputSpawnError {
		t.Fatalf("a failed spawn should enter the error modal, got mode %v", got.modal)
	}
	if reload == nil {
		t.Fatalf("a failed spawn should still trigger a reload")
	}
	if len(got.pendingSpawns) != 0 {
		t.Fatalf("pendingSpawns = %v, want empty after a failed spawn", got.pendingSpawns)
	}
	for _, s := range got.sessions {
		if s.Pending {
			t.Fatalf("a failed spawn must not leave a permanent placeholder row")
		}
	}
	view := render(got)
	for _, want := range []string{"alpha", "exit status 1", "branch 'alpha' already exists"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the error modal should show %q:\n%s", want, view)
		}
	}

	dismissed, cmd := update(t, got, tea.KeyPressMsg{Code: tea.KeyEsc})
	if dismissed.modal != inputNormal {
		t.Fatalf("esc should dismiss the error modal, got mode %v", dismissed.modal)
	}
	if cmd != nil {
		t.Fatalf("dismissing should dispatch nothing, got a cmd")
	}
	if dismissed.spawnErrHeadline != "" || len(dismissed.spawnErrTail) != 0 || dismissed.spawnErrName != "" {
		t.Fatalf("dismissing should clear the modal's content, got %+v", dismissed)
	}
}

// TestSpawnErrorModalKeyHandling pins the mode-dispatch ordering the error modal
// depends on: while it is up, enter also dismisses, `n` must NOT open the
// creation form (the global keymap is unreachable), and an unrecognized key
// leaves the modal — and therefore the captured output — in place.
func TestSpawnErrorModalKeyHandling(t *testing.T) {
	base := func() Model {
		m := testModel()
		m, _ = update(t, m, spawnFinishedMsg{name: "alpha", err: errors.New("boom"), output: "why\n"})
		if m.modal != inputSpawnError {
			t.Fatalf("setup: expected the error modal, got %v", m.modal)
		}
		return m
	}

	if got, _ := update(t, base(), keyEnter); got.modal != inputNormal {
		t.Fatalf("enter should dismiss the error modal, got mode %v", got.modal)
	}

	for _, k := range []tea.KeyMsg{keyRune('n'), keyRune('c'), keyRune('q'), keyRune('x'), keyUp} {
		got, cmd := update(t, base(), k)
		if got.modal != inputSpawnError {
			t.Fatalf("%v should leave the error modal up, got mode %v", k, got.modal)
		}
		if got.formOpen {
			t.Fatalf("%v must not open the creation form from inside the error modal", k)
		}
		if got.quitting {
			t.Fatalf("%v must not quit from inside the error modal", k)
		}
		if cmd != nil {
			t.Fatalf("%v inside the error modal should dispatch nothing", k)
		}
	}

	if got, cmd := update(t, base(), tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); !got.quitting || cmd == nil {
		t.Fatalf("ctrl+c must remain the universal escape hatch: quitting=%v cmd=%v", got.quitting, cmd != nil)
	}
}

func TestSpawnOutputTail(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "\n  \n\t\n", nil},
		{"single line", "boom", []string{"boom"}},
		{"blank lines dropped", "a\n\n\nb\n", []string{"a", "b"}},
		{
			"keeps only the last four",
			"1\n2\n3\n4\n5\n6\n",
			[]string{"3", "4", "5", "6"},
		},
		{"trailing CR trimmed", "a\r\nb\r\n", []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := spawnOutputTail(tt.in)
			if !slicesEqual(got, tt.want) {
				t.Fatalf("spawnOutputTail(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestSpawnWarnings pins spawnWarnings' extraction of Plan.Run's bestEffort
// "warning: " lines from a successful background create's captured output:
// prefix stripped, the Plan.Run "warning: <step Desc>: <err>" shape
// collapsed to just the trailing error, duplicates dropped, and a mid-line
// "warning:" left alone.
func TestSpawnWarnings(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"no warnings", "afk new: creating worktree\nafk new: done\n", nil},
		{
			"bare warning kept minus the prefix",
			`warning: base "side" is not git-spice-tracked (no status.json for base)` + "\n",
			[]string{`base "side" is not git-spice-tracked (no status.json for base)`},
		},
		{
			"Plan.Run shape keeps only the error",
			`warning: Track branch "e2e" with git-spice (base "side"): base "side" is not tracked; skipping` + "\n",
			[]string{`base "side" is not tracked; skipping`},
		},
		{
			"duplicate warning lines collapse to one",
			"warning: boom: bang\nwarning: boom: bang\n",
			[]string{"bang"},
		},
		{
			"warning appearing mid-line is not matched",
			"afk new: saw a warning: base is untracked\n",
			nil,
		},
		{
			"CRLF tolerated",
			"warning: base \"side\" is not git-spice-tracked\r\n",
			[]string{`base "side" is not git-spice-tracked`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := spawnWarnings(tt.in)
			if !slicesEqual(got, tt.want) {
				t.Fatalf("spawnWarnings(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// sessionNames is the readable form of the canonical slice for assertions about
// placeholder insertion order.
func sessionNames(m Model) []string {
	out := make([]string, len(m.sessions))
	for i, s := range m.sessions {
		out[i] = s.Name
	}
	return out
}

// TestConfirmCleanBarRendersPhaseOrStale proves the confirm modal's rendered
// message reflects the remembered phase for a non-stale session and renders a
// stale one's empty phase as "unknown" (never as an empty phase string), and
// that both carry the consequence-and-guarantee line. The scare framing is gone:
// the modal now greets terminal-phase sessions too, so "force clean" would be
// wrong copy — asserted negatively so it cannot creep back.
func TestConfirmCleanBarRendersPhaseOrStale(t *testing.T) {
	for _, tt := range []struct {
		name      string
		stale     bool
		wantPhase string
	}{
		{"active-session", false, "executing"},
		{"stale-session", true, "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := sess(tt.name)
			if tt.stale {
				s.StatusStale = true
				s.Phase = ""
			} else {
				s.Phase = state.PhaseExecuting
			}
			m := testModel(s)
			m, _ = pressAction(t, m, keyRune('c'))

			// JoinVertical pads narrow lines with trailing spaces, so every
			// rendered line is TrimRight'd before it is matched or measured.
			var lines []string
			for _, ln := range strings.Split(render(m), "\n") {
				lines = append(lines, strings.TrimRight(ln, " "))
			}
			out := strings.Join(lines, "\n")

			if !strings.Contains(out, fmt.Sprintf("Clean session %q (phase: %s)?", tt.name, tt.wantPhase)) {
				t.Fatalf("confirm bar should show the name and phase %q; got:\n%s", tt.wantPhase, out)
			}
			if !strings.Contains(out, "Worktree goes; archive kept.") {
				t.Fatalf("confirm bar should state the consequence and the guarantee; got:\n%s", out)
			}
			if !strings.Contains(out, "y: clean") || !strings.Contains(out, "n/esc: cancel") {
				t.Fatalf("confirm bar should show the key hint; got:\n%s", out)
			}
			if strings.Contains(out, "force clean") {
				t.Fatalf("confirm bar must not use the force-clean framing; got:\n%s", out)
			}
		})
	}
}

// TestConfirmCleanBarFitsAnEightyColumnTerminal is the width budget on the modal
// copy. 80 columns is the narrowest terminal anyone runs; truncate() cuts at
// m.width, and the key hint sits at the END of the second line — so copy that
// only fits a wide terminal costs a user on a stock 80-column window the one part
// of the modal that says how to answer it. The rest of the suite renders at 120
// and cannot see this.
func TestConfirmCleanBarFitsAnEightyColumnTerminal(t *testing.T) {
	s := sess("a-session-with-a-fairly-long-name")
	s.Phase = state.PhaseExecuting
	m := testModel(s)
	m.width = 80
	m, _ = pressAction(t, m, keyRune('c'))

	// JoinVertical pads narrow lines with trailing spaces, so trim before any
	// width assertion.
	for _, ln := range strings.Split(m.renderConfirmCleanBar(), "\n") {
		trimmed := strings.TrimRight(ln, " ")
		if w := lipgloss.Width(trimmed); w > m.width {
			t.Fatalf("confirm bar line exceeds width %d (got %d): %q", m.width, w, trimmed)
		}
	}
	// The whole hint survives at 80 columns — the key hint especially, since it
	// is what truncation would eat first.
	rendered := strings.ReplaceAll(m.renderConfirmCleanBar(), "\n", " ")
	for _, want := range []string{"Worktree goes", "archive kept", "y: clean", "d: +delete branch", "n/esc: cancel"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("confirm bar at width 80 lost %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "…") {
		t.Fatalf("confirm bar must not be truncated at width 80:\n%s", rendered)
	}
}

// --- the in-process clean seam (cleanRunner) ---

// TestResolveCleanTargetMatchesScratchKind pins the name-collision half of
// the scratch clean's safety: `afk new --scratch <name>` can collide with a
// planned session of the same name (their `.afk/` dirs live in different
// worktrees), and session.Discover then reports BOTH. The scratch rows are
// the load-bearing ones — before the kind joined the match, a confirmed
// scratch clean took the first name match, which sortSessions orders
// non-scratch first, so it could run snapshot/archive/worktree-remove on the
// status-backed session the user never confirmed. The stale-phase row is the
// dangerous shape from the review verdict: phase "" on both sides passes the
// fresh-phase gate, so the kind match is the ONLY thing standing between the
// confirmation and the wrong session.
func TestResolveCleanTargetMatchesScratchKind(t *testing.T) {
	discovered := []session.Session{
		{
			Name:         "twin",
			WorktreePath: "/wt/twin",
			Branch:       "twin",
			StatusPath:   "/wt/twin/.afk/twin/status.json",
		},
		{Name: "twin", Scratch: true},
	}
	staleFresh := func(session.Session) string { return "" }

	tests := []struct {
		name        string
		target      string
		scratch     bool
		wantScratch bool
		wantErr     bool
	}{
		{"confirmed scratch clean resolves the scratch twin, never the stale planned one", "twin", true, true, false},
		{"confirmed planned clean resolves the planned twin, never the scratch one", "twin", false, false, false},
		{"confirmed scratch clean refuses a name with no scratch session", "solo", true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all := append([]session.Session{}, discovered...)
			all = append(all, session.Session{
				Name:         "solo",
				WorktreePath: "/wt/solo",
				Branch:       "solo",
				StatusPath:   "/wt/solo/.afk/solo/status.json",
			})
			got, err := resolveCleanTarget(all, tt.target, "", tt.scratch, session.KindHealthy, staleFresh)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveCleanTarget(%q, scratch=%v) = %+v, want a refusal", tt.target, tt.scratch, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCleanTarget(%q, scratch=%v) failed: %v", tt.target, tt.scratch, err)
			}
			if got == nil || got.Scratch != tt.wantScratch {
				t.Fatalf("resolved %+v, want the %v-scratch session named %q", got, tt.wantScratch, tt.target)
			}
		})
	}
}

// TestCleanRunnerWiring covers what defaultCleanSession does around the two
// session-package calls, which the pure resolveCleanTarget tests cannot reach: the
// shared capture buffer handed to BOTH Stdout and Stderr, the Discover-before-clean
// order, and cleanErr's join. The real defaultCleanRunner would need a git repo, a
// worktree and possibly a live host, so the runner's three fields are substituted;
// defaultCleanSession is exactly defaultCleanRunner().run.
func TestCleanRunnerWiring(t *testing.T) {
	discovered := []session.Session{{
		Name:         "alpha",
		WorktreePath: "/wt/alpha",
		Branch:       "alpha",
		StatusPath:   "/wt/alpha/.afk/alpha/status.json",
		Status:       session.Status{Phase: "executing"},
	}}

	t.Run("the clean deps capture writes instead of painting the alt-screen", func(t *testing.T) {
		var gotStdout, gotStderr any
		var cleaned session.Session
		r := cleanRunner{
			discover: func(_ string, d session.CleanDeps) ([]session.Session, error) {
				gotStdout, gotStderr = d.Stdout, d.Stderr
				return discovered, nil
			},
			freshPhase: func(session.Session) string { return "executing" },
			clean: func(_ string, s session.Session, d session.CleanDeps, _ session.CleanOptions) (session.CleanResult, error) {
				cleaned = s
				if d.Stdout != gotStdout || d.Stderr != gotStderr {
					t.Fatalf("the clean must share the SAME buffer Discover got")
				}
				return session.CleanResult{Name: s.Name, ArchivePath: "/archive/alpha"}, nil
			},
		}

		res, err := r.run("/repo", "alpha", "executing", false, session.KindHealthy, session.CleanOptions{})
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
		if res.ArchivePath != "/archive/alpha" {
			t.Fatalf("result = %+v, want the clean's own result returned", res)
		}
		if gotStdout == nil || gotStdout != gotStderr {
			t.Fatalf("Stdout and Stderr must be one shared buffer, got %v / %v", gotStdout, gotStderr)
		}
		if _, ok := gotStdout.(*bytes.Buffer); !ok {
			t.Fatalf("Stdout = %T, want a *bytes.Buffer — never os.Stdout or io.Discard", gotStdout)
		}
		// The session handed to the clean is Discover's, with the worktree and
		// branch Discover resolved — never a hand-built one.
		if cleaned.WorktreePath != "/wt/alpha" || cleaned.Branch != "alpha" {
			t.Fatalf("cleaned %+v, want the session session.Discover resolved", cleaned)
		}
	})

	t.Run("a refusal never reaches the clean", func(t *testing.T) {
		r := cleanRunner{
			discover: func(string, session.CleanDeps) ([]session.Session, error) {
				return discovered, nil
			},
			freshPhase: func(session.Session) string { return "reviewing" },
			clean: func(string, session.Session, session.CleanDeps, session.CleanOptions) (session.CleanResult, error) {
				t.Fatal("a refused target must never be cleaned")
				return session.CleanResult{}, nil
			},
		}
		if _, err := r.run("/repo", "alpha", "executing", false, session.KindHealthy, session.CleanOptions{}); err == nil {
			t.Fatalf("a changed phase must be refused")
		}
		if _, err := r.run("/repo", "nope", "executing", false, session.KindHealthy, session.CleanOptions{}); err == nil {
			t.Fatalf("an unresolvable name must be refused")
		}
	})

	t.Run("a Discover failure is reported and nothing is cleaned", func(t *testing.T) {
		r := cleanRunner{
			discover: func(string, session.CleanDeps) ([]session.Session, error) {
				return nil, errors.New("git worktree list --porcelain: exit status 128")
			},
			freshPhase: func(session.Session) string { return "executing" },
			clean: func(string, session.Session, session.CleanDeps, session.CleanOptions) (session.CleanResult, error) {
				t.Fatal("a failed Discover must never be followed by a clean")
				return session.CleanResult{}, nil
			},
		}
		_, err := r.run("/repo", "alpha", "executing", false, session.KindHealthy, session.CleanOptions{})
		if err == nil || !strings.Contains(err.Error(), "exit status 128") {
			t.Fatalf("err = %v, want the Discover failure surfaced", err)
		}
	})

	t.Run("the clean's own error and partial result both survive", func(t *testing.T) {
		want := errors.New("git worktree remove /wt/alpha: exit status 128")
		r := cleanRunner{
			discover: func(string, session.CleanDeps) ([]session.Session, error) {
				return discovered, nil
			},
			freshPhase: func(session.Session) string { return "executing" },
			clean: func(_ string, s session.Session, _ session.CleanDeps, _ session.CleanOptions) (session.CleanResult, error) {
				return session.CleanResult{Name: s.Name, ArchivePath: "/archive/alpha"}, want
			},
		}
		res, err := r.run("/repo", "alpha", "executing", false, session.KindHealthy, session.CleanOptions{})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want the clean's error wrapped, not replaced", err)
		}
		// The partial result must ride along: the archive already happened, and
		// its path is what the status line leads with on the error branch.
		if res.ArchivePath != "/archive/alpha" {
			t.Fatalf("result = %+v, want the partial result preserved on failure", res)
		}
	})

	t.Run("captured buffer output joins the error only when non-empty", func(t *testing.T) {
		want := errors.New("terminating session host: boom")
		newRunner := func(write string) cleanRunner {
			return cleanRunner{
				discover: func(string, session.CleanDeps) ([]session.Session, error) {
					return discovered, nil
				},
				freshPhase: func(session.Session) string { return "executing" },
				clean: func(_ string, s session.Session, d session.CleanDeps, _ session.CleanOptions) (session.CleanResult, error) {
					fmt.Fprint(d.Stderr, write)
					return session.CleanResult{Name: s.Name}, want
				},
			}
		}

		_, err := newRunner("").run("/repo", "alpha", "executing", false, session.KindHealthy, session.CleanOptions{})
		if err == nil || err.Error() != want.Error() {
			t.Fatalf("err = %v, want the bare error when nothing was captured", err)
		}

		_, err = newRunner("afk clean: removing /tmp/sock: permission denied\n").
			run("/repo", "alpha", "executing", false, session.KindHealthy, session.CleanOptions{})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want the original error still unwrappable", err)
		}
		if !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("err = %v, want the captured buffer appended", err)
		}
	})
}

// TestDashboardPhaseReadsThroughTheDashboardsParser is the parser-identity
// contract behind the stale-row fix. internal/session's discovery only
// json-decodes status.json, so a file internal/state rejects still yields a
// populated Status.Phase there — and comparing THAT against the dashboard's ""
// refused every stale row forever. dashboardPhase re-reads with internal/state, so
// both sides of the comparison speak one vocabulary.
//
// This reads real files, but only files it writes itself in t.TempDir(): no git
// repo, no worktree, no socket.
func TestDashboardPhaseReadsThroughTheDashboardsParser(t *testing.T) {
	for _, tt := range []struct {
		name string
		// json is the status.json content; "" means write no file at all.
		json      string
		wantFresh string
	}{
		{
			name:      "a schema-valid file reports its phase",
			json:      `{"session_id":"s1","plan_name":"alpha","phase":"executing","branch":"alpha","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"2026-07-29T00:00:00Z"}`,
			wantFresh: "executing",
		},
		{
			name: "a phase outside the enum is stale, not that phase",
			// internal/session decodes this into Status.Phase
			// "not-a-real-phase"; internal/state rejects it as stale, which is
			// what the dashboard row shows.
			json:      `{"session_id":"s1","plan_name":"alpha","phase":"not-a-real-phase","branch":"alpha","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"2026-07-29T00:00:00Z"}`,
			wantFresh: "",
		},
		{
			name:      "an unknown key is stale per the schema's reader rules",
			json:      `{"session_id":"s1","plan_name":"alpha","phase":"executing","branch":"alpha","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"2026-07-29T00:00:00Z","surprise":1}`,
			wantFresh: "",
		},
		{
			name:      "a missing file is stale",
			wantFresh: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), ".afk", "alpha")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("seeding the session dir: %v", err)
			}
			statusPath := filepath.Join(dir, "status.json")
			if tt.json != "" {
				if err := os.WriteFile(statusPath, []byte(tt.json), 0o644); err != nil {
					t.Fatalf("writing status.json: %v", err)
				}
			}

			// The Status field is deliberately populated with the LENIENT parse,
			// so a fix that reads it instead of the file fails here.
			sess := session.Session{
				Name:       "alpha",
				StatusPath: statusPath,
				Status:     session.Status{Phase: "not-a-real-phase"},
			}
			if got := dashboardPhase(sess); got != tt.wantFresh {
				t.Fatalf("dashboardPhase = %q, want %q", got, tt.wantFresh)
			}
		})
	}
}

// --- spawnNewArgs (pure argv-builder seam) ---

// TestSpawnNewArgs is the argv-order contract for the background create. The
// flags-before-positional ordering is the load-bearing part: Go's flag package
// stops parsing at the first non-flag token, so `new <name> --no-dash` would make
// --no-dash a second positional and fail `afk new`'s arity check. The
// autonomous rows are load-bearing for the same reason — `--autonomous` after
// the name would be a second positional — and the unchecked rows prove the
// flag is absent rather than sent as `--autonomous=false`. The provider rows
// are load-bearing in both directions: every pre-provider row passes "" and
// the "explicit claude" rows pass provider.Default, and all of them must keep
// the exact argv they had before the selector existed (no --provider at all);
// the codex rows pin `--provider codex` after --autonomous and before the name.
func TestSpawnNewArgs(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		base       string
		desc       string
		autonomous bool
		provider   string
		want       []string
	}{
		{"no base, no description", "my-session", "", "", false, "", []string{"new", "--no-dash", "my-session"}},
		{"with base only", "my-session", "main", "", false, "", []string{"new", "--no-dash", "--base", "main", "my-session"}},
		{"with description only", "my-session", "", "task context", false, "", []string{"new", "--no-dash", "--description", "task context", "my-session"}},
		{"with both", "my-session", "main", "task context", false, "", []string{"new", "--no-dash", "--base", "main", "--description", "task context", "my-session"}},
		{"base with slashes", "s", "feat/foo", "", false, "", []string{"new", "--no-dash", "--base", "feat/foo", "s"}},
		{"whitespace-only description omitted", "s", "main", "  \n  ", false, "", []string{"new", "--no-dash", "--base", "main", "s"}},
		{"autonomous only", "my-session", "", "", true, "", []string{"new", "--no-dash", "--autonomous", "my-session"}},
		{"autonomous with base", "my-session", "main", "", true, "", []string{"new", "--no-dash", "--base", "main", "--autonomous", "my-session"}},
		{"autonomous with everything", "my-session", "main", "task context", true, "", []string{"new", "--no-dash", "--base", "main", "--description", "task context", "--autonomous", "my-session"}},
		{"explicit claude is omitted", "my-session", "", "", false, provider.ClaudeName, []string{"new", "--no-dash", "my-session"}},
		{"explicit claude with everything is omitted", "my-session", "main", "task context", true, provider.ClaudeName, []string{"new", "--no-dash", "--base", "main", "--description", "task context", "--autonomous", "my-session"}},
		{"codex autonomous only", "my-session", "", "", true, provider.CodexName, []string{"new", "--no-dash", "--autonomous", "--provider", "codex", "my-session"}},
		{"codex with base", "my-session", "main", "", true, provider.CodexName, []string{"new", "--no-dash", "--base", "main", "--autonomous", "--provider", "codex", "my-session"}},
		{"codex with everything", "my-session", "main", "task context", true, provider.CodexName, []string{"new", "--no-dash", "--base", "main", "--description", "task context", "--autonomous", "--provider", "codex", "my-session"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := spawnNewArgs(tt.in, tt.base, tt.desc, tt.autonomous, tt.provider)
			if !slicesEqual(got, tt.want) {
				t.Fatalf("spawnNewArgs(%q, %q, %q, %v, %q) = %v, want %v", tt.in, tt.base, tt.desc, tt.autonomous, tt.provider, got, tt.want)
			}
			// Every flag must precede the name, wherever the name ends up.
			nameAt := -1
			for i, a := range got {
				if a == tt.in {
					nameAt = i
					break
				}
			}
			if nameAt != len(got)-1 {
				t.Fatalf("spawnNewArgs(%q, %q, %q, %v, %q) = %v: the positional name must come last", tt.in, tt.base, tt.desc, tt.autonomous, tt.provider, got)
			}
		})
	}
}

// scratchProviderRows are the provider cases shared by the one-off argv
// tests. Load-bearing rows: "" and claude must add NO --provider (a claude
// one-off's argv stays byte-identical to the pre-picker one, and `afk new`
// resolves the default itself); codex and opencode must add exactly the pair.
var scratchProviderRows = []struct {
	name     string
	provider string
	want     []string
}{
	{"empty", "", []string{"new", "--no-dash", "--scratch"}},
	{"claude", provider.Default, []string{"new", "--no-dash", "--scratch"}},
	{"codex", provider.CodexName, []string{"new", "--no-dash", "--scratch", "--provider", "codex"}},
	{"opencode", provider.OpenCodeName, []string{"new", "--no-dash", "--scratch", "--provider", "opencode"}},
}

// TestSpawnScratchArgs pins the one-off argv exactly: no base, no
// description, no positional name at all (afk new allocates a scratch
// session's own name — see openScratch and decisions.md, W3.T1), and
// --provider only for a non-default provider.
func TestSpawnScratchArgs(t *testing.T) {
	for _, tt := range scratchProviderRows {
		t.Run(tt.name, func(t *testing.T) {
			if got := spawnScratchArgs(tt.provider); !slicesEqual(got, tt.want) {
				t.Fatalf("spawnScratchArgs(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

// TestSpawnScratchExecCmdWiring mirrors TestSpawnNewExecCmdWiring's contract
// for the one-off path: same repo Dir, same nil Stdout/Stderr (CombinedOutput
// refuses to run a Cmd that already has either set), for every provider.
func TestSpawnScratchExecCmdWiring(t *testing.T) {
	for _, tt := range scratchProviderRows {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel()
			c := m.spawnScratchExecCmd(tt.provider)

			if got := c.Args[1:]; !slicesEqual(got, tt.want) {
				t.Fatalf("argv = %v, want %v", got, tt.want)
			}
			if c.Dir != m.repo {
				t.Fatalf("Dir = %q, want the repo root %q", c.Dir, m.repo)
			}
			if c.Stdout != nil || c.Stderr != nil {
				t.Fatalf("Stdout/Stderr must stay nil for CombinedOutput, got %v/%v", c.Stdout, c.Stderr)
			}
		})
	}
}

// TestScratchDirsListedCarriesProviderToSpawnArgv proves the provider chosen
// at `o` survives the async listing hop: a scratchDirsListedMsg carrying it
// dispatches a create whose argv has --provider for a non-default provider and
// none otherwise. The Deps.RunSpawn seam captures the exec.Cmd instead of
// running it, so no child ever starts (afkBinary() is this test binary).
func TestScratchDirsListedCarriesProviderToSpawnArgv(t *testing.T) {
	for _, tt := range scratchProviderRows {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("a"))
			var ran []*exec.Cmd
			m.deps.RunSpawn = func(c *exec.Cmd) ([]byte, error) {
				ran = append(ran, c)
				return nil, nil
			}
			m.addPendingSpawn("scratch-1")
			m.mergePendingSpawnsNow()

			_, cmd := update(t, m, scratchDirsListedMsg{guess: "scratch-1", provider: tt.provider})
			if cmd == nil {
				t.Fatal("scratchDirsListedMsg should dispatch the create")
			}
			if len(ran) != 0 {
				t.Fatalf("the create ran during Update (%d), want it deferred to the Cmd", len(ran))
			}
			msg, ok := cmd().(spawnFinishedMsg)
			if !ok || msg.name != "scratch-1" {
				t.Fatalf("Cmd result = %#v, want a spawnFinishedMsg for scratch-1", msg)
			}
			if len(ran) != 1 {
				t.Fatalf("RunSpawn ran %d times, want 1", len(ran))
			}
			if got := ran[0].Args[1:]; !slicesEqual(got, tt.want) {
				t.Fatalf("argv = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSpawnNewExecCmdWiring pins the background spawn's exec.Cmd contract. The
// nil Stdout/Stderr assertions are load-bearing in the opposite direction from
// the attach's: CombinedOutput REFUSES to run a Cmd that already has either set,
// so pre-setting them would turn every create into an immediate error.
func TestSpawnNewExecCmdWiring(t *testing.T) {
	m := testModel()
	c := m.spawnNewExecCmd("alpha", "main", "task description", true, provider.CodexName)

	want := []string{"new", "--no-dash", "--base", "main", "--description", "task description", "--autonomous", "--provider", "codex", "alpha"}
	if got := c.Args[1:]; !slicesEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	if c.Dir != m.repo {
		t.Fatalf("Dir = %q, want the repo root %q", c.Dir, m.repo)
	}
	if c.Stdout != nil || c.Stderr != nil {
		t.Fatalf("Stdout/Stderr must stay nil for CombinedOutput; got %v/%v", c.Stdout, c.Stderr)
	}
	if c.Stdin != nil {
		t.Fatalf("Stdin = %v, want nil (/dev/null) — the child must never expect a TTY", c.Stdin)
	}
}

// TestCleanSessionCmdPassesThroughAndReportsResult proves cleanSessionCmd hands
// the repo, the name, and the phase the user confirmed to the seam untouched,
// and folds whatever the seam returns into a cleanFinishedMsg. Both directions
// are covered: a success carrying a CleanResult, and a failure carrying the
// seam's error with a zero result.
func TestCleanSessionCmdPassesThroughAndReportsResult(t *testing.T) {
	boom := errors.New("git worktree remove: exit status 1")
	tests := []struct {
		name       string
		result     session.CleanResult
		err        error
		wantResult session.CleanResult
	}{
		{
			name: "success carries the seam's result",
			result: session.CleanResult{
				Name:              "alpha",
				Branch:            "alpha",
				WorktreePath:      "/wt/alpha",
				ArchivePath:       "/archive/alpha",
				SnapshotCommitted: true,
			},
			wantResult: session.CleanResult{
				Name:              "alpha",
				Branch:            "alpha",
				WorktreePath:      "/wt/alpha",
				ArchivePath:       "/archive/alpha",
				SnapshotCommitted: true,
			},
		},
		{
			name: "failure carries the seam's error",
			err:  boom,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRepo, gotName, gotPhase string
			calls := 0
			m := testModel(sess("alpha"))
			m.deps.CleanSession = func(repo, name, phase string, _ bool, _ session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
				calls++
				gotRepo, gotName, gotPhase = repo, name, phase
				return tt.result, tt.err
			}

			cmd := m.cleanSessionCmd("alpha", "executing", false, session.KindHealthy, session.CleanOptions{})
			if calls != 0 {
				t.Fatalf("the seam ran at Cmd-construction time (%d calls); it must only run inside the closure", calls)
			}

			msg := cmd()
			done, ok := msg.(cleanFinishedMsg)
			if !ok {
				t.Fatalf("clean cmd produced %T, want cleanFinishedMsg", msg)
			}
			if calls != 1 {
				t.Fatalf("seam called %d times, want exactly 1", calls)
			}
			if gotRepo != "/repo" || gotName != "alpha" || gotPhase != "executing" {
				t.Fatalf("seam called with (%q, %q, %q), want (%q, %q, %q)",
					gotRepo, gotName, gotPhase, "/repo", "alpha", "executing")
			}
			if done.name != "alpha" {
				t.Fatalf("cleanFinishedMsg.name = %q, want %q", done.name, "alpha")
			}
			if done.result != tt.wantResult {
				t.Fatalf("cleanFinishedMsg.result = %+v, want %+v", done.result, tt.wantResult)
			}
			if !errors.Is(done.err, tt.err) {
				t.Fatalf("cleanFinishedMsg.err = %v, want %v", done.err, tt.err)
			}
		})
	}
}

// TestCleanSessionCmdCapturesRepoAtConstruction pins the capture-at-construction
// invariant cleanSessionCmd shares with loadCmd: a Model mutated after the Cmd
// was built cannot change what the clean runs against.
func TestCleanSessionCmdCapturesRepoAtConstruction(t *testing.T) {
	var gotRepo string
	m := testModel(sess("alpha"))
	m.deps.CleanSession = func(repo, _, _ string, _ bool, _ session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
		gotRepo = repo
		return session.CleanResult{}, nil
	}

	cmd := m.cleanSessionCmd("alpha", "executing", false, session.KindHealthy, session.CleanOptions{})
	m.repo = "/somewhere/else"
	m.deps.CleanSession = func(string, string, string, bool, session.Kind, session.CleanOptions) (session.CleanResult, error) {
		t.Fatal("the Cmd must use the seam captured at construction time")
		return session.CleanResult{}, nil
	}
	cmd()

	if gotRepo != "/repo" {
		t.Fatalf("seam got repo %q, want the repo captured at construction (%q)", gotRepo, "/repo")
	}
}

// TestResolveCleanTargetRefusals covers defaultCleanSession's two refusal paths
// — the reason this seam resolves through session.Discover instead of trusting
// the dashboard's row. An unknown name is a row whose session is simply not
// there any more: the dashboard's list can be a full refresh interval old,
// so the directory behind the confirmed row can have been removed (by a
// concurrent `afk clean`, or by hand) while the modal was up. A changed
// phase is the fresh-phase gate the dashboard's cached phase cannot provide.
//
// A standalone /afk:go session in the PRIMARY checkout is deliberately NOT
// the unknown-name example any more. It used to be the load-bearing one, back
// when session.Discover refused that shape outright; it is now an ordinary
// entry carrying KindStatusWithoutWorktree, resolves here like any other, and
// is covered by TestResolveCleanTargetStatusWithoutWorktree. It cannot join
// this table either way: the tail assertion below treats an empty Branch as a
// broken resolution, and an empty Branch is exactly what that kind carries.
//
// The fresh phase comes from the injected freshPhase func, standing in for
// dashboardPhase, and NOT from session.Session.Status.Phase. That distinction is
// the subject of the "a stale row the dashboard cannot parse" case: the two
// packages parse status.json to different strictness, so a row the dashboard
// shows as stale can carry a populated Status.Phase here. Comparing against that
// field refused every stale row forever; comparing against what the dashboard's
// own parser reads makes it cleanable.
func TestResolveCleanTargetRefusals(t *testing.T) {
	discovered := []session.Session{
		{
			Name:         "alpha",
			WorktreePath: "/wt/alpha",
			Branch:       "alpha",
			StatusPath:   "/wt/alpha/.afk/alpha/status.json",
			Status:       session.Status{Phase: "executing"},
		},
		{
			Name:         "stale",
			WorktreePath: "/wt/stale",
			Branch:       "stale",
			StatusPath:   "/wt/stale/.afk/stale/status.json",
			// A file internal/session decoded happily and internal/state
			// rejects: the phase is not in the six-value enum, so the dashboard
			// row is stale with Phase "" while this field is populated.
			Status: session.Status{Phase: "not-a-real-phase"},
		},
	}
	// freshPhase stands in for dashboardPhase: internal/state's strict read,
	// keyed by StatusPath, reporting a stale file as the empty phase.
	freshPhase := map[string]string{
		"/wt/alpha/.afk/alpha/status.json": "executing",
		"/wt/stale/.afk/stale/status.json": "",
	}

	tests := []struct {
		name           string
		target         string
		confirmedPhase string
		wantErr        []string
		wantResolved   string
	}{
		{
			name:           "an accepted session in the confirmed phase resolves",
			target:         "alpha",
			confirmedPhase: "executing",
			wantResolved:   "alpha",
		},
		{
			name:           "a stale row the dashboard cannot parse is still cleanable",
			target:         "stale",
			confirmedPhase: "",
			wantResolved:   "stale",
		},
		{
			name:           "a name Discover does not report at all is refused",
			target:         "removed-under-the-modal",
			confirmedPhase: "executing",
			wantErr:        []string{`no session named "removed-under-the-modal" found`},
		},
		{
			name:           "a phase that moved on since the confirmation is refused",
			target:         "alpha",
			confirmedPhase: "reviewing",
			wantErr:        []string{`"alpha"`, `"reviewing"`, `"executing"`, "not cleaning"},
		},
		{
			name:           "a row that stopped being stale since the confirmation is refused",
			target:         "alpha",
			confirmedPhase: "",
			wantErr:        []string{`"alpha"`, `"executing"`, "not cleaning"},
		},
		{
			name:           "a row that WENT stale since the confirmation is refused",
			target:         "stale",
			confirmedPhase: "executing",
			wantErr:        []string{`"stale"`, `"executing"`, "not cleaning"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCleanTarget(discovered, tt.target, tt.confirmedPhase, false, session.KindHealthy,
				func(s session.Session) string { return freshPhase[s.StatusPath] })
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("resolveCleanTarget(%q, %q) = %+v, want a refusal", tt.target, tt.confirmedPhase, got)
				}
				if got != nil {
					t.Fatalf("a refusal must resolve no target; got %+v", got)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q does not mention %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCleanTarget(%q, %q) failed: %v", tt.target, tt.confirmedPhase, err)
			}
			if got == nil || got.Name != tt.wantResolved {
				t.Fatalf("resolved %+v, want the session named %q", got, tt.wantResolved)
			}
			// The whole point of resolving: the worktree and branch come from
			// session.Discover, never from the dashboard's row.
			if got.WorktreePath == "" || got.Branch == "" {
				t.Fatalf("resolved session missing Discover-supplied fields: %+v", got)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- stderrTail ---

func TestStderrTail(t *testing.T) {
	t.Run("multi-line input keeps last 3 lines", func(t *testing.T) {
		in := "l1\nl2\nl3\nl4\nl5"
		got := stderrTail(in)
		if got != "l3\nl4\nl5" {
			t.Fatalf("stderrTail(%q) = %q, want %q", in, got, "l3\nl4\nl5")
		}
	})

	t.Run("over 200 chars hard capped", func(t *testing.T) {
		in := strings.Repeat("a", 250)
		got := stderrTail(in)
		if len(got) != 200 {
			t.Fatalf("stderrTail long input length = %d, want 200", len(got))
		}
		if got != strings.Repeat("a", 200) {
			t.Fatalf("stderrTail should keep the tail 200 chars")
		}
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		in := "\n\n  boom  \n\n"
		got := stderrTail(in)
		if got != "boom" {
			t.Fatalf("stderrTail(%q) = %q, want %q", in, got, "boom")
		}
	})

	t.Run("empty input yields empty output", func(t *testing.T) {
		if got := stderrTail(""); got != "" {
			t.Fatalf("stderrTail(\"\") = %q, want empty", got)
		}
	})
}

// --- embedded pane over a session's detached host ---

// TestEnterOpenedPaneRendersInView is the acceptance-criteria test: a pane
// opened over a session's host by enter appears in View() and owns the
// keyboard once it lands.
func TestEnterOpenedPaneRendersInView(t *testing.T) {
	fp := newFakePane("HELLO-FROM-PANE")
	m := testModel(runningSess("live"))
	var gotCfg hostPaneConfig
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		gotCfg = cfg
		return fp, nil
	}

	m2, cmd := update(t, m, keyEnter)
	if cmd == nil {
		t.Fatalf("enter on a running session should return a connect cmd")
	}
	msg := cmd()
	attached, ok := msg.(paneAttachedMsg)
	if !ok {
		t.Fatalf("connect cmd produced %T, want paneAttachedMsg", msg)
	}
	// The pane's child is the session's HOST, addressed by name + worktree —
	// there is no argv here, because this dashboard starts no process.
	if gotCfg.Name != "live" || gotCfg.Worktree != "/wt/live" {
		t.Fatalf("hostPaneConfig = %+v, want name=live worktree=/wt/live", gotCfg)
	}
	if gotCfg.Cols < 1 || gotCfg.Rows < 1 {
		t.Fatalf("hostPaneConfig size = %dx%d, want positive", gotCfg.Cols, gotCfg.Rows)
	}

	m3, subCmd := update(t, m2, attached)
	if subCmd == nil {
		t.Fatalf("paneAttachedMsg should return a redraw/exit subscription cmd")
	}
	if m3.pane == nil || m3.paneName != "live" {
		t.Fatalf("pane state not stored: pane=%v name=%q", m3.pane != nil, m3.paneName)
	}
	if !attached.focusOnArrival {
		t.Fatal("enter must attach with focusOnArrival=true")
	}
	if !m3.paneOwnsKeyboard() {
		t.Fatalf("an enter-opened pane must own the keyboard once it lands")
	}
	out := render(m3)
	if !strings.Contains(out, "HELLO-FROM-PANE") {
		t.Fatalf("pane content missing from View output:\n%s", out)
	}
	if !strings.Contains(out, "live") {
		t.Fatalf("pane title (session name) missing from View output:\n%s", out)
	}
}

// TestPaneOpenFailureShowsErrorStatus covers a failure that is NOT an
// unreachable host (that case, and its markHostStopped correction, is covered by
// TestPaneAttachFailedStatus above): the error is surfaced verbatim with the
// host.log pointer, and no pane is stored.
func TestPaneOpenFailureShowsErrorStatus(t *testing.T) {
	m := testModel(runningSess("a"))
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		return nil, errors.New("socket permission denied")
	}
	_, cmd := update(t, m, keyEnter)
	if cmd == nil {
		t.Fatalf("expected a connect cmd")
	}
	msg := cmd()
	fail, ok := msg.(paneAttachedMsg)
	if !ok {
		t.Fatalf("connect cmd produced %T, want paneAttachedMsg", msg)
	}
	if fail.err == nil {
		t.Fatalf("the seam's error must propagate onto the message")
	}
	got, reload := update(t, m, fail)
	if got.pane != nil {
		t.Fatalf("no pane should be set after a failed open")
	}
	if reload == nil {
		t.Fatalf("a failed open should still trigger a reload")
	}
	if !strings.Contains(got.status, "socket permission denied") {
		t.Fatalf("status = %q, want the open error surfaced", got.status)
	}
	// A non-ErrHostNotReachable failure proves nothing about the host, so the
	// session must NOT be flipped to stopped.
	if !got.sessions[0].HostRunning {
		t.Fatalf("a generic open failure must not flip HostRunning to false")
	}
}

// TestEnterOnAnotherSessionSwitchesThePane is the multi-session acceptance
// test: enter on a session that is NOT the one in the pane switches to it. The
// outgoing session is detached (Close), never terminated — its host is a
// separate process that keeps running — so switching back is another enter.
func TestEnterOnAnotherSessionSwitchesThePane(t *testing.T) {
	first := newFakePane("SESSION-A")
	second := newFakePane("SESSION-B")
	m := testModel(runningSess("a"), runningSess("b"))
	m, _ = update(t, m, paneAttachedMsg{pane: first, name: "a"})

	var gotCfg hostPaneConfig
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		gotCfg = cfg
		return second, nil
	}
	m.selectSessionIndex(1) // select "b", the session NOT in the pane

	mid, cmd := update(t, m, keyEnter)
	if cmd == nil {
		t.Fatalf("enter on a different running session should dispatch a connect")
	}
	if !strings.Contains(mid.status, "switching to b") {
		t.Fatalf("status = %q, want the switching notice", mid.status)
	}
	// The outgoing pane stays up until the new connection actually lands: a
	// failed dial must not cost the user the session they were watching.
	if mid.pane != paneHandle(first) || mid.paneName != "a" {
		t.Fatalf("the outgoing pane must stay installed until the connect succeeds: name=%q", mid.paneName)
	}
	if first.closed != 0 {
		t.Fatalf("the outgoing pane must not be closed before the new one connects, got %d", first.closed)
	}

	attached, ok := cmd().(paneAttachedMsg)
	if !ok {
		t.Fatalf("connect cmd produced %T, want paneAttachedMsg", cmd())
	}
	if gotCfg.Name != "b" || gotCfg.Worktree != "/wt/b" {
		t.Fatalf("hostPaneConfig = %+v, want name=b worktree=/wt/b", gotCfg)
	}

	got, subCmd := update(t, mid, attached)
	if got.pane != paneHandle(second) || got.paneName != "b" {
		t.Fatalf("the pane should now host b: pane==second=%v name=%q", got.pane == paneHandle(second), got.paneName)
	}
	if first.closed != 1 {
		t.Fatalf("the outgoing session should be detached exactly once, got %d", first.closed)
	}
	if second.closed != 0 {
		t.Fatalf("the incoming pane must not be closed, got %d", second.closed)
	}
	if subCmd == nil {
		t.Fatalf("the new pane needs its own redraw/exit subscription armed")
	}
	if !attached.focusOnArrival {
		t.Fatal("enter must switch agent surfaces with focusOnArrival=true")
	}
	if !got.paneOwnsKeyboard() {
		t.Fatalf("an enter-switched pane must own the keyboard once it lands")
	}
	if !strings.Contains(got.status, "switched to b") || !strings.Contains(got.status, "a still running") {
		t.Fatalf("status = %q, want it to name both the new and the backgrounded session", got.status)
	}
	if !strings.Contains(render(got), "SESSION-B") {
		t.Fatalf("View should render the switched-to session:\n%s", render(got))
	}
}

// TestToggleWithHiddenPaneTargetsTheSelectedAgent ensures a pane left behind
// the overview never becomes a global target. With the cursor on another row,
// enter opens THAT row's agent (T16.2): a running row connects, replacing the
// old pane only once the new one arrives — and arriving focused. A row with
// no live agent has no agent to open (plan W3.T1 d), so enter gives its
// overview the keyboard: a stopped row is restarted by `r` instead, and a
// pending, faulted or cleaning row stays on its overview. Either way the old
// pane stays undrawn: it belongs to a different row.
func TestToggleWithHiddenPaneTargetsTheSelectedAgent(t *testing.T) {
	hiddenOldPane := func(t *testing.T, target Session) Model {
		t.Helper()
		m := testModel(runningSess("a"), target)
		// The list is in screen order, where a fault row comes first, so the
		// rows are selected by name.
		m.selectSessionIndex(namedIndex(t, m, "a"))
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("A"), name: "a"})
		m, _ = update(t, m, keyEnter)
		m, _ = flipView(t, m)
		m, _ = pressAction(t, m, keyEscMsg())
		if m.view != viewOverview || m.focus != focusList || m.paneName != "a" {
			t.Fatalf("setup: view=%v focus=%v pane=%q, want a's pane behind the overview with the list focused",
				m.view, m.focus, m.paneName)
		}
		m.selectSessionIndex(namedIndex(t, m, target.Name))
		return m
	}
	// toSelectedAgent is the user's route: enter opens the selected row's
	// agent.
	toSelectedAgent := func(t *testing.T, m Model) (Model, tea.Cmd) {
		t.Helper()
		return update(t, m, keyEnter)
	}

	t.Run("running selected row replaces the hidden pane and lands focused", func(t *testing.T) {
		m := hiddenOldPane(t, runningSess("b"))
		old := m.pane
		incoming := newFakePane("B")
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return incoming, nil }

		mid, cmd := toSelectedAgent(t, m)
		if cmd == nil {
			t.Fatal("enter must attach the selected running session")
		}
		if mid.pane != old || mid.view != viewAgent || mid.focus != focusRight || mid.paneOwnsKeyboard() {
			t.Fatalf("before the arrival: same=%v view=%v focus=%v paneFocus=%v, want a kept undrawn, agent view, right focus",
				mid.pane == old, mid.view, mid.focus, mid.paneOwnsKeyboard())
		}
		attached := drainForPaneAttached(t, cmd)
		if attached.name != "b" || !attached.focusOnArrival {
			t.Fatalf("attached request = name:%q focus:%v, want b, focused", attached.name, attached.focusOnArrival)
		}
		got, _ := update(t, mid, attached)
		if got.pane != incoming || got.paneName != "b" || !got.paneOwnsKeyboard() {
			t.Fatalf("selected pane result = incoming:%v name:%q paneFocus:%v",
				got.pane == incoming, got.paneName, got.paneOwnsKeyboard())
		}
		if old.(*fakePane).closed != 1 {
			t.Fatalf("the old pane should be detached exactly once by the swap, got %d", old.(*fakePane).closed)
		}
	})

	t.Run("stopped selected row: enter shows its overview, r restarts and replaces the hidden pane", func(t *testing.T) {
		m := hiddenOldPane(t, sess("b"))
		old := m.pane
		incoming := newFakePane("B")
		m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
			return session.HostProcess{PID: 4242}, nil
		}
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return incoming, nil }

		mid, cmd := toSelectedAgent(t, m)
		if cmd != nil || mid.pane != old || !mid.detailOwnsKeyboard() || mid.focus != focusRight {
			t.Fatalf("enter on a stopped row must start nothing: cmd=%v same=%v detail=%v focus=%v",
				cmd != nil, mid.pane == old, mid.detailOwnsKeyboard(), mid.focus)
		}
		mid, cmd = pressAction(t, mid, keyRune('r'))
		if cmd == nil || mid.pane != old {
			t.Fatalf("r must restart, leaving the hidden pane installed until replacement: cmd=%v same=%v",
				cmd != nil, mid.pane == old)
		}
		restarted, ok := cmd().(hostRestartedMsg)
		if !ok {
			t.Fatalf("restart command returned an unexpected message")
		}
		if restarted.name != "b" || !restarted.focusOnArrival {
			t.Fatalf("restart request = name:%q focus:%v, want b, focused (r from the right side)", restarted.name, restarted.focusOnArrival)
		}
		mid, attachCmd := update(t, mid, restarted)
		if attachCmd == nil || mid.pane != old {
			t.Fatal("successful restart must attach b without revealing a")
		}
		got, _ := update(t, mid, drainForPaneAttached(t, attachCmd))
		// enter left the view the agent's, so b's agent lands focused once
		// it is back; the swap still detaches a.
		if got.pane != incoming || got.paneName != "b" || got.view != viewAgent || !got.paneOwnsKeyboard() {
			t.Fatalf("restarted pane result = incoming:%v name:%q view:%v paneFocus:%v",
				got.pane == incoming, got.paneName, got.view, got.paneOwnsKeyboard())
		}
		if old.(*fakePane).closed != 1 {
			t.Fatalf("the old pane should be detached exactly once by the swap, got %d", old.(*fakePane).closed)
		}
	})

	invalid := []struct {
		name   string
		target Session
		setup  func(*Model)
	}{
		{"pending selected row", func() Session { s := runningSess("b"); s.Pending = true; return s }(), func(*Model) {}},
		{"fault selected row", faultSess("b", session.KindWorktreeMissing, "gone"), func(*Model) {}},
		{"cleaning selected row", runningSess("b"), func(m *Model) {
			m.cleaningSessions = map[cleanID]bool{cleanIDFor(m.sessions[namedIndex(t, *m, "b")]): true}
		}},
	}
	for _, tc := range invalid {
		t.Run(tc.name+": enter starts nothing and does not reveal the old pane", func(t *testing.T) {
			m := hiddenOldPane(t, tc.target)
			tc.setup(&m)
			old := m.pane
			opens, spawns := 0, 0
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { opens++; return nil, nil }
			m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
				spawns++
				return session.HostProcess{}, nil
			}

			got, cmd := toSelectedAgent(t, m)
			if cmd != nil {
				t.Fatal("inert selected row dispatched lifecycle work")
			}
			if got.pane != old || got.shouldRenderPane() || got.paneOwnsKeyboard() {
				t.Fatalf("old pane was revealed or focused: same=%v drawn=%v paneFocus=%v",
					got.pane == old, got.shouldRenderPane(), got.paneOwnsKeyboard())
			}
			// No live agent: the right side has the keyboard on the row's
			// overview, which is what the agent view draws for it.
			if got.focus != focusRight || !got.detailOwnsKeyboard() {
				t.Fatalf("focus = %v detail=%v, want right on the overview", got.focus, got.detailOwnsKeyboard())
			}
			if opens != 0 || spawns != 0 {
				t.Fatalf("lifecycle calls = open:%d spawn:%d, want none", opens, spawns)
			}
		})
	}
}

// TestSwitchBackAndForthNeverEndsASession walks A→B→A, the loop the whole
// multi-session feature exists for. Every switch detaches and nothing
// accumulates: each pane is closed exactly once, when it is switched away from.
func TestSwitchBackAndForthNeverEndsASession(t *testing.T) {
	paneA1, paneB, paneA2 := newFakePane("A1"), newFakePane("B"), newFakePane("A2")
	queue := []*fakePane{paneB, paneA2}

	m := testModel(runningSess("a"), runningSess("b"))
	m, _ = update(t, m, paneAttachedMsg{pane: paneA1, name: "a"})
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		next := queue[0]
		queue = queue[1:]
		return next, nil
	}

	for _, step := range []struct {
		cursor int
		want   string
	}{{1, "b"}, {0, "a"}} {
		// Back to the list first: the previous step's pane landed focused,
		// and enter there would go to the session, not to the dashboard.
		m, _ = pressAction(t, m, keyEscMsg())
		m.selectSessionIndex(step.cursor)
		var cmd tea.Cmd
		m, cmd = update(t, m, keyEnter)
		if cmd == nil {
			t.Fatalf("switching to %s should dispatch a connect", step.want)
		}
		m, _ = update(t, m, cmd().(paneAttachedMsg))
		if m.paneName != step.want {
			t.Fatalf("paneName = %q, want %q", m.paneName, step.want)
		}
	}

	if paneA1.closed != 1 {
		t.Fatalf("a's first pane should be detached exactly once, got %d", paneA1.closed)
	}
	if paneB.closed != 1 {
		t.Fatalf("b's pane should be detached exactly once, got %d", paneB.closed)
	}
	if paneA2.closed != 0 {
		t.Fatalf("the currently-installed pane must be open, got closed=%d", paneA2.closed)
	}
}

// TestRestartKeyOnAStoppedSessionWhileAPaneIsOpenSwitchesAfterTheRestart
// covers the switch whose target has no host yet: `r` runs the restart first
// and the pane swap happens off its result, with the session on screen
// untouched throughout. From the list the keyboard stays on the list.
func TestRestartKeyOnAStoppedSessionWhileAPaneIsOpenSwitchesAfterTheRestart(t *testing.T) {
	live, restarted := newFakePane("LIVE"), newFakePane("RESTARTED")
	m := testModel(runningSess("a"), sess("b")) // b's host is stopped
	m, _ = update(t, m, paneAttachedMsg{pane: live, name: "a"})
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		return session.HostProcess{PID: 4242}, nil
	}
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return restarted, nil }
	m.selectSessionIndex(1)

	m, cmd := pressAction(t, m, keyRune('r'))
	if cmd == nil {
		t.Fatalf("r on a stopped session should dispatch the restart")
	}
	hr, ok := cmd().(hostRestartedMsg)
	if !ok {
		t.Fatalf("produced %T, want hostRestartedMsg", cmd())
	}
	if m.paneName != "a" || live.closed != 0 {
		t.Fatalf("the open session must be untouched while the restart runs: name=%q closed=%d",
			m.paneName, live.closed)
	}

	m, attachCmd := update(t, m, hr)
	if attachCmd == nil {
		t.Fatalf("a successful restart should chain into the connect")
	}
	attached := attachCmd().(paneAttachedMsg)
	if attached.focusOnArrival || !attached.selectionBound {
		t.Fatalf("list r's restarted switch = focusOnArrival:%v selectionBound:%v, want false/true",
			attached.focusOnArrival, attached.selectionBound)
	}
	m, _ = update(t, m, attached)
	if m.paneName != "b" {
		t.Fatalf("paneName = %q, want the restarted session b", m.paneName)
	}
	if live.closed != 1 {
		t.Fatalf("the outgoing session should be detached once, got %d", live.closed)
	}
	if !m.paneForSelection() || m.focus != focusList {
		t.Fatalf("connected=%v focus=%v, want the restarted target connected with the list keeping the keyboard",
			m.paneForSelection(), m.focus)
	}
}

// TestPaneAttachedDuplicateAndSameNameRacesDoNotDisturbTheOpenPane covers the
// two arrivals that must NOT be treated as a switch: a duplicate delivery of the
// installed pane's own handle (closing it would detach the session on screen),
// and a second connection to the same session from a raced open (the surplus
// connection has to be closed or it leaks a client slot on the host).
func TestPaneAttachedDuplicateAndSameNameRacesDoNotDisturbTheOpenPane(t *testing.T) {
	installed := newFakePane("INSTALLED")
	m := testModel(runningSess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: installed, name: "a"})

	got, cmd := update(t, m, paneAttachedMsg{pane: installed, name: "a"})
	if got.pane != paneHandle(installed) {
		t.Fatalf("a duplicate delivery must leave the installed pane in place")
	}
	if installed.closed != 0 {
		t.Fatalf("a duplicate delivery must never close the installed pane, got %d", installed.closed)
	}
	if cmd != nil {
		t.Fatalf("a duplicate delivery must not arm a second subscription")
	}

	surplus := newFakePane("SURPLUS")
	got, cmd = update(t, got, paneAttachedMsg{pane: surplus, name: "a"})
	if got.pane != paneHandle(installed) || got.paneName != "a" {
		t.Fatalf("a raced second connection to the same session must not replace the pane")
	}
	if surplus.closed != 1 {
		t.Fatalf("the surplus connection must be closed exactly once, got %d", surplus.closed)
	}
	if installed.closed != 0 {
		t.Fatalf("the installed pane must stay open, got closed=%d", installed.closed)
	}
	if cmd != nil {
		t.Fatalf("no subscription may be armed for a rejected connection")
	}
}

// TestNewKeyAllowedWhilePaneActive pins the deliberate asymmetry with clean: a
// background create gets its own host and never touches what is in the pane, so
// `n` stays available (see openNewForm).
func TestNewKeyAllowedWhilePaneActive(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "a"})
	got, _ := pressAction(t, m, keyRune('n'))
	if !got.formOpen {
		t.Fatalf("n should still open the creation form while a pane is active")
	}
}

// TestCleanWithAPaneOpenAsksThenDetaches pins clean on a session with its
// pane open since T16.2, when no key closes a pane on its own: `c` asks, as
// for every session, and only the confirmation's y detaches the pane before
// the clean runs. Load-bearing: "n leaves the pane" — a cancelled clean must
// not have cost the owner the session they were watching.
func TestCleanWithAPaneOpenAsksThenDetaches(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "a"})
	got, _ := pressAction(t, m, keyRune('c'))
	if got.modal != inputConfirmClean || got.pane == nil {
		t.Fatalf("c: modal %v, pane %v; want the confirmation with the pane kept", got.modal, got.pane != nil)
	}
	cancelled, _ := update(t, got, keyRune('n'))
	if cancelled.pane == nil {
		t.Fatal("n left no pane; a cancelled clean must not detach it")
	}
	confirmed, cmd := update(t, got, keyRune('y'))
	if confirmed.pane != nil || cmd == nil {
		t.Fatalf("y: pane %v, cmd %v; want the pane detached and the clean dispatched", confirmed.pane != nil, cmd != nil)
	}
}

// --- in-TUI creation form (W7.T1) ---

// typeRunes feeds each rune of s into m as a separate KeyRunes keypress,
// returning the resulting Model. Used to drive the form's hand-rolled name
// field (see newform.go's updateName) the same way a real terminal would,
// one keystroke at a time.
func typeRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = update(t, m, keyRune(r))
	}
	return m
}

// drainBatch runs cmd and, if it produced a tea.BatchMsg (from tea.Batch),
// runs every sub-command too, returning every resulting message. A plain
// (non-batch) result is returned as the sole element.
func drainBatch(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			if c == nil {
				continue
			}
			out = append(out, c())
		}
		return out
	}
	return []tea.Msg{msg}
}

func TestFormOpenAndCancel(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = pressAction(t, m, keyRune('n'))
	if !m.formOpen {
		t.Fatalf("n should open the creation form")
	}

	got, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got.formOpen {
		t.Fatalf("esc should close the form")
	}
	if cmd != nil {
		t.Fatalf("esc should not return a cmd")
	}
	if !strings.Contains(got.status, "cancel") {
		t.Fatalf("status = %q, want a cancellation message", got.status)
	}
}

// TestFormSwallowsQuitKeysEscIsOnlyExit pins the form's documented modal
// behavior (see formBaseFiltering's routing table): Esc is the ONLY exit. `q` is a legitimate
// session-name character and types into the name buffer; ctrl+c is a
// deliberate no-op (it neither quits the dashboard nor closes the form).
func TestFormSwallowsQuitKeysEscIsOnlyExit(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = pressAction(t, m, keyRune('n'))

	// q types into the name field — no quit.
	m, _ = update(t, m, keyRune('q'))
	if m.quitting {
		t.Fatalf("q while the form is open must not quit")
	}
	if got := m.form.nameInput.Value(); got != "q" {
		t.Fatalf("q should type into the name buffer, got %q", got)
	}
	if !m.formOpen {
		t.Fatalf("q must not close the form")
	}

	// ctrl+c is a no-op: no quit, form and buffer untouched.
	m, _ = update(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if m.quitting {
		t.Fatalf("ctrl+c while the form is open must not quit")
	}
	if !m.formOpen || m.form.nameInput.Value() != "q" {
		t.Fatalf("ctrl+c must leave the form open and the buffer untouched: active=%v name=%q",
			m.formOpen, m.form.nameInput.Value())
	}

	// Esc is the only exit.
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.formOpen || m.quitting {
		t.Fatalf("esc should close the form without quitting: active=%v quitting=%v", m.formOpen, m.quitting)
	}
}

func TestFormInvalidNameShowsInlineErrorAndStaysOpen(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = pressAction(t, m, keyRune('n'))
	m = typeRunes(t, m, "Not-Kebab-Case")

	got, cmd := update(t, m, keyEnter)
	if cmd != nil {
		t.Fatalf("an invalid name must not return a submit cmd")
	}
	if !got.formOpen {
		t.Fatalf("the form should stay open after a validation failure")
	}
	if got.form.err == "" {
		t.Fatalf("expected an inline validation error on the form")
	}
	if got.form.nameInput.Value() != "Not-Kebab-Case" {
		t.Fatalf("the typed name should be preserved for correction, got %q", got.form.nameInput.Value())
	}
}

func TestFormBaseSelectorUsesSpiceBranchesWhenDetected(t *testing.T) {
	m := testModel(sess("a"))
	mainRef := spice.BranchRef{Name: "main"}
	m.stack = stackState{Detected: true, Branches: []spice.Branch{
		{Name: "feature-x", Down: &mainRef},
		{Name: "main"},
	}}

	m, _ = pressAction(t, m, keyRune('n'))
	if sel := m.form.baseList.SelectedItem(); sel == nil || sel.(baseItem) != "main" {
		got := ""
		if sel != nil {
			got = string(sel.(baseItem))
		}
		t.Fatalf("selected base = %q, want trunk-first (main)", got)
	}

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.form.focusIndex != 2 {
		t.Fatalf("tab tab should focus the base selector, got focusIndex=%d", m.form.focusIndex)
	}
	m, _ = update(t, m, keyDown)
	if sel := m.form.baseList.SelectedItem(); sel == nil || sel.(baseItem) != "feature-x" {
		got := ""
		if sel != nil {
			got = string(sel.(baseItem))
		}
		t.Fatalf("selected after down = %q, want feature-x", got)
	}
}

func TestFormBaseDefaultsToTrunkPlaceholderWithoutSpice(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"feature", "main"}
	// Resolved via Deps.PlainTrunk, never assumed — "main" here is this repo's
	// answer, not a default.
	m.plainTrunk = "main"
	m, _ = pressAction(t, m, keyRune('n'))
	items := m.form.baseList.Items()
	if len(items) != 2 {
		t.Fatalf("plain-git repo got %d base options, want 2 (reversed from spike-scope limit)", len(items))
	}
	if sel := m.form.baseList.SelectedItem(); sel == nil || sel.(baseItem) != "main" {
		got := ""
		if sel != nil {
			got = string(sel.(baseItem))
		}
		t.Fatalf("selected base = %q, want trunk-first (main)", got)
	}
}

func TestFormRendersInView(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = pressAction(t, m, keyRune('n'))
	m = typeRunes(t, m, "abc")

	out := render(m)
	if !strings.Contains(out, "New session") {
		t.Fatalf("expected the form title in View output:\n%s", out)
	}
	if !strings.Contains(out, "abc") {
		t.Fatalf("expected the typed name in View output:\n%s", out)
	}
}

// TestQuitClosesPaneAndQuitsImmediately pins requestQuit's documented contract:
// there is NO quit confirmation, because closing a pane detaches from a host
// that is a separate process — quitting the dashboard cannot end a session, so
// there is nothing to confirm. The pane is closed exactly once (never leaked)
// and the pane state is cleared.
func TestQuitClosesPaneAndQuitsImmediately(t *testing.T) {
	for _, k := range []tea.KeyMsg{keyRune('q'), tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}} {
		fp := newFakePane("X")
		m := testModel(sess("a"))
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})

		got, cmd := pressQuitKey(t, m, k)
		if !got.quitting {
			t.Fatalf("%v with a pane active should quit immediately", k)
		}
		if cmd == nil {
			t.Fatalf("%v should return tea.Quit", k)
		}
		if fp.closed != 1 {
			t.Fatalf("%v should Close (detach) the pane exactly once, got %d", k, fp.closed)
		}
		if got.pane != nil || got.paneName != "" || got.paneOwnsKeyboard() {
			t.Fatalf("%v should clear the pane state: pane=%v name=%q focused=%v",
				k, got.pane != nil, got.paneName, got.paneOwnsKeyboard())
		}
	}
}

func TestQuitWithoutPaneQuitsImmediately(t *testing.T) {
	// The no-pane path is unchanged: q and ctrl+c quit at once.
	for _, k := range []tea.KeyMsg{keyRune('q'), tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}} {
		m := testModel(sess("a"))
		got, cmd := pressQuitKey(t, m, k)
		if !got.quitting || cmd == nil {
			t.Fatalf("%v without a pane should quit immediately", k)
		}
	}
}

func TestPaneExitedTriggersReload(t *testing.T) {
	fp := newFakePane("X")
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})

	got, cmd := update(t, m, paneExitedMsg{pane: fp, err: nil})
	if cmd == nil {
		t.Fatalf("paneExitedMsg should trigger a reload cmd")
	}
	if got.pane != nil || got.paneName != "" {
		t.Fatalf("pane state should be cleared after exit: pane=%v name=%q", got.pane != nil, got.paneName)
	}
	if fp.closed != 1 {
		t.Fatalf("pane should be Closed exactly once on exit, got %d", fp.closed)
	}
	if !strings.Contains(got.status, "session ended") || !strings.Contains(got.status, "a") {
		t.Fatalf("status = %q, want a session-ended message naming the session", got.status)
	}
	// The reload cmd is loadCmd, which produces a sessionsLoadedMsg.
	if _, ok := cmd().(sessionsLoadedMsg); !ok {
		t.Fatalf("reload cmd should produce a sessionsLoadedMsg")
	}
}

// TestPaneExitedMarksTheHostStoppedSoTheDiffStaysQuiet: a session ending
// under its pane is told once, as "session ended". The host exits with its
// child, so the next discovery pass reads HostRunning true→false on a row with
// no terminal phase (scratch sessions never have one) — a transition the
// snapshot diff would otherwise report as "a host died" four seconds later.
// The exit path marks the host stopped in place, so the pass after it finds
// no transition; and a stale host.json re-asserting the old PID for a tick
// is overridden the same way (applyDeadHostOverrides), so neither shape of
// the following pass produces a second notification.
func TestPaneExitedMarksTheHostStoppedSoTheDiffStaysQuiet(t *testing.T) {
	for _, tc := range []struct {
		name string
		next Session
	}{
		{"host.json already gone", stoppedSess("a")},
		{"stale host.json still names the PID", runningSess("a")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePane("X")
			m := testModel(runningSess("a"))
			now, adv := advancingClock(time.Unix(1_000_000, 0))
			m.deps.Now = now
			m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})

			m, _ = update(t, m, paneExitedMsg{pane: fp, err: nil})
			if !strings.Contains(m.status, "session ended: a") {
				t.Fatalf("status = %q, want the session-ended notice", m.status)
			}
			if m.sessions[0].HostRunning {
				t.Fatalf("HostRunning still true after the pane's session ended; markHostStopped was not applied")
			}

			adv(toastTTL + time.Second)
			m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{tc.next}, at: now()})
			if len(m.pendingBackground) != 0 {
				t.Fatalf("pending = %+v after the next pass, want no host-death event for a session already reported ended", m.pendingBackground)
			}
			if m.status != "session ended: a" {
				t.Fatalf("status = %q after the next pass, want the session-ended notice left alone", m.status)
			}
			if m.sessions[0].HostRunning {
				t.Fatalf("HostRunning re-asserted by the next pass; the override should hold while host.json names the disproved PID")
			}
		})
	}
}

// TestPaneExitedReasonDecidesWhetherTheHostIsMarkedStopped pins W4.T1's rule
// 6: a steal is not a death. hostChild.Read collapses host.Stolen and
// host.Exit into the same io.EOF, so both reach paneExitedMsg with err == nil
// and only the carried reason separates them. The `stolen` row is the
// load-bearing one — it is the regression from review-fix-1, where
// markHostStopped ran on a steal and pinned a live host's PID into
// deadHostPIDs, holding the row at "stopped" until the PID changed. The
// `zero reason` row is load-bearing in the other direction: every fakePane
// in this suite sends reason 0, and the existing double-report fix
// (TestPaneExitedMarksTheHostStoppedSoTheDiffStaysQuiet) depends on 0 being
// read as a real exit, so a default that flipped the other way would pass
// this table's `stolen` row while silently re-opening the "x host died"
// double report everywhere else. The `exited` row keeps the explicit
// DetachExited value on the exit path so the two named reasons are pinned
// to opposite outcomes rather than one of them riding the default. The
// `dropped` row is the codex-attach regression: a host that drops a slow
// client closes with no Exit frame and keeps serving, and pinning its PID made
// every later enter spawn a second host whose bind probe stole the new pane.
//
// Each row is followed by a discovery pass whose host.json still names PID
// 4242, the shape that shows deadHostPIDs doing work (or, for the steal,
// deliberately not doing it): a stolen session's row must survive that pass
// still running, and an ended session's must stay stopped.
func TestPaneExitedReasonDecidesWhetherTheHostIsMarkedStopped(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reason      host.DetachReason
		wantRunning bool
		wantStatus  string
	}{
		{"stolen", host.DetachStolen, true, "a was taken over by another window or the web page; press enter to reopen it"},
		{"dropped", host.DetachDropped, true, "connection to a dropped — it may still be running · enter to reopen the agent"},
		{"exited", host.DetachExited, false, "session ended: a"},
		{"zero reason reads as exited", 0, false, "session ended: a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePane("X")
			m := testModel(runningSess("a"))
			now, adv := advancingClock(time.Unix(1_000_000, 0))
			m.deps.Now = now
			m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})

			m, cmd := update(t, m, paneExitedMsg{pane: fp, err: nil, reason: tc.reason})
			if cmd == nil {
				t.Fatalf("paneExitedMsg should trigger a reload cmd whatever the reason")
			}
			if m.pane != nil || m.paneName != "" || fp.closed != 1 {
				t.Fatalf("pane not torn down: pane=%v name=%q closed=%d — a steal and an exit both end this pane", m.pane != nil, m.paneName, fp.closed)
			}
			if m.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", m.status, tc.wantStatus)
			}
			if m.sessions[0].HostRunning != tc.wantRunning {
				t.Fatalf("HostRunning = %v after the pane ended (%s), want %v", m.sessions[0].HostRunning, tc.reason, tc.wantRunning)
			}
			if _, pinned := m.deadHostPIDs["a"]; pinned == tc.wantRunning {
				t.Fatalf("deadHostPIDs[a] present = %v after %s, want %v — the PID pin is what holds a row at stopped across ticks", pinned, tc.reason, !tc.wantRunning)
			}

			adv(toastTTL + time.Second)
			m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{runningSess("a")}, at: now()})
			if m.sessions[0].HostRunning != tc.wantRunning {
				t.Fatalf("HostRunning = %v after the next pass (host.json still names PID 4242), want %v", m.sessions[0].HostRunning, tc.wantRunning)
			}
			if len(m.pendingBackground) != 0 {
				t.Fatalf("pending = %+v after the next pass, want nothing — neither a steal nor an already-reported exit is a host death", m.pendingBackground)
			}
			if m.status != tc.wantStatus {
				t.Fatalf("status = %q after the next pass, want %q left alone", m.status, tc.wantStatus)
			}
		})
	}
}

// TestPaneEventCmdCarriesTheDetachReasonFromTheHostChild pins the wiring
// that makes the table above reachable in production: paneEventCmd asks the
// handle for its reason through the optional detachReasoner surface, and the
// wedgeBannerPane every dial returns answers from the hostChild behind it.
// The `stolen` row is load-bearing — without the type assertion every real
// steal arrives as reason 0 and takes the exit branch, which is exactly the
// review-fix-1 regression. The other two rows pin the safe default: a handle
// without the surface (every fakePane in this suite) and a banner pane built
// over a fake with no connection behind it both report 0, not a guess.
//
// The hostChild has no socket: markExited is the whole of what Read does with
// a Stolen frame besides returning io.EOF, and it touches only the reason
// and the exited channel.
func TestPaneEventCmdCarriesTheDetachReasonFromTheHostChild(t *testing.T) {
	stolen := &hostChild{exited: make(chan struct{})}
	stolen.markExited(host.DetachStolen)
	exited := &hostChild{exited: make(chan struct{})}
	exited.markExited(host.DetachExited)
	dropped := &hostChild{exited: make(chan struct{})}
	dropped.markExited(host.DetachDropped)

	bare := newFakePane("X")
	overStolen := newFakePane("X")
	overExited := newFakePane("X")
	overDropped := newFakePane("X")
	overNothing := newFakePane("X")
	for _, tc := range []struct {
		name string
		pane paneHandle
		done *fakePane
		want host.DetachReason
	}{
		{"stolen", &wedgeBannerPane{paneHandle: overStolen, child: stolen}, overStolen, host.DetachStolen},
		{"exited", &wedgeBannerPane{paneHandle: overExited, child: exited}, overExited, host.DetachExited},
		{"dropped", &wedgeBannerPane{paneHandle: overDropped, child: dropped}, overDropped, host.DetachDropped},
		{"banner pane with no connection behind it", &wedgeBannerPane{paneHandle: overNothing}, overNothing, 0},
		{"handle without the surface", bare, bare, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			close(tc.done.done)
			got, ok := paneEventCmd(tc.pane)().(paneExitedMsg)
			if !ok {
				t.Fatalf("paneEventCmd with Done closed should produce a paneExitedMsg")
			}
			if got.pane != tc.pane {
				t.Fatalf("paneExitedMsg.pane is not the handle the cmd was armed on")
			}
			if got.reason != tc.want {
				t.Fatalf("reason = %v (%d), want %v (%d)", got.reason, int(got.reason), tc.want, int(tc.want))
			}
		})
	}
}

func TestPaneExitedWithErrorSurfacesError(t *testing.T) {
	fp := newFakePane("X")
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	got, _ := update(t, m, paneExitedMsg{pane: fp, err: errors.New("boom")})
	if !strings.Contains(got.status, "disconnected from a") || !strings.Contains(got.status, "boom") {
		t.Fatalf("status = %q, want a disconnected notice carrying the error", got.status)
	}
}

// TestToggleFullPaneLifecycleOpenRedrawExitReload chains the whole pane message
// flow in one continuous test, entirely through fakes: open a pane, observe
// its initial render in View, observe a redraw update View again, then
// observe the conductor exit produce the session-ended status and a reload
// cmd. The piecewise tests above cover each transition in isolation; this
// one asserts the sequence composes correctly end to end.
func TestToggleFullPaneLifecycleOpenRedrawExitReload(t *testing.T) {
	fp := newFakePane("BOOTING")
	m := testModel(runningSess("a"))
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return fp, nil }

	m, openCmd := update(t, m, keyEnter)
	if openCmd == nil {
		t.Fatalf("enter on a running session should return a connect cmd")
	}
	openMsg := openCmd()
	attached, ok := openMsg.(paneAttachedMsg)
	if !ok {
		t.Fatalf("connect cmd produced %T, want paneAttachedMsg", openMsg)
	}

	m, subCmd := update(t, m, attached)
	if subCmd == nil {
		t.Fatalf("paneAttachedMsg should arm the redraw/exit subscription")
	}
	if !strings.Contains(render(m), "BOOTING") {
		t.Fatalf("initial render missing from View:\n%s", render(m))
	}

	// The session produced new output; a real subscription cmd would deliver a
	// paneRedrawMsg carrying the same pane handle once Changed() fires.
	fp.render = "RUNNING-OUTPUT"
	m, redrawCmd := update(t, m, paneRedrawMsg{pane: fp})
	if redrawCmd == nil {
		t.Fatalf("a redraw from the active pane should re-arm the subscription")
	}
	if !strings.Contains(render(m), "RUNNING-OUTPUT") {
		t.Fatalf("redrawn content missing from View:\n%s", render(m))
	}

	// The session ends: the pane is closed, a session-ended status names the
	// session, and a reload is triggered.
	m, exitCmd := update(t, m, paneExitedMsg{pane: fp, err: nil})
	if m.pane != nil {
		t.Fatalf("pane state should be cleared after exit")
	}
	if fp.closed != 1 {
		t.Fatalf("pane should be Closed exactly once on exit, got %d", fp.closed)
	}
	if !strings.Contains(m.status, "session ended") || !strings.Contains(m.status, "a") {
		t.Fatalf("status = %q, want a session-ended message naming the session", m.status)
	}
	if exitCmd == nil {
		t.Fatalf("paneExitedMsg should trigger a reload cmd")
	}
	if _, ok := exitCmd().(sessionsLoadedMsg); !ok {
		t.Fatalf("reload cmd should produce a sessionsLoadedMsg")
	}
}

func TestPaneRedrawReArmsAndIgnoresStale(t *testing.T) {
	fp := newFakePane("X")
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})

	// A redraw from the active pane re-arms the subscription.
	_, cmd := update(t, m, paneRedrawMsg{pane: fp})
	if cmd == nil {
		t.Fatalf("redraw from the active pane should re-arm the subscription")
	}
	// A redraw from a different (already-closed) pane is ignored.
	stale := newFakePane("Y")
	_, cmd2 := update(t, m, paneRedrawMsg{pane: stale})
	if cmd2 != nil {
		t.Fatalf("a stale redraw should be ignored (nil cmd)")
	}
}

func TestStalePaneExitedIgnored(t *testing.T) {
	fp := newFakePane("X")
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	other := newFakePane("Y")
	got, cmd := update(t, m, paneExitedMsg{pane: other, err: nil})
	if cmd != nil {
		t.Fatalf("exit from a non-active pane should be ignored (nil cmd)")
	}
	if got.pane == nil {
		t.Fatalf("active pane should be untouched by a stale exit")
	}
	if fp.closed != 0 || other.closed != 0 {
		t.Fatalf("no pane should be closed by a stale exit")
	}
}

// --- focus routing, input forwarding, resize (W4.T1) ---

// refuseAction runs the action behind Project key k by calling its handler,
// past the key's blocked reason (runLevelAction answers a blocked key with the
// reason and runs nothing). A test of a handler's own refusal (a status line
// naming why nothing happened) goes through here; a test of what the key does
// goes through pressAction.
func refuseAction(m Model, k string) (Model, tea.Cmd) {
	r, ok := keyRowFor(levelProject, k)
	if !ok {
		panic("refuseAction: " + k + " is no key at Project")
	}
	return runAction(m, r.action)
}

// pressQuitKey sends k the way the user quits with it: alone at All projects
// and Project, after the leader in the Agent (pressAction).
func pressQuitKey(t *testing.T, m Model, k tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	if k == keyRune('q') {
		return pressAction(t, m, k)
	}
	return update(t, m, k)
}

// leaderThen presses the one-rune key k as a dashboard key (pressAction):
// alone at All projects and Project, after the leader in the Agent. It returns
// the Cmd the action produced, so a test can still assert on the work it
// queued.
func leaderThen(t *testing.T, m Model, k rune) (Model, tea.Cmd) {
	t.Helper()
	return pressAction(t, m, keyRune(k))
}

// flipView is the old agent-or-overview chord (ctrl+] t, retired by T16.2)
// done the T16.2 way: from the Agent the leader then d gives the overview
// the keyboard (A2), and from the focused overview enter opens the agent.
// Anywhere else it presses t, which at Project is the Build task's tokens key
// and only says why it does nothing off the Build tab, so a test that
// pressed the old chord from the list still sees nothing flip.
func flipView(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	switch {
	case m.level() == levelAgent:
		return pressAction(t, m, keyRune('d'))
	case m.detailOwnsKeyboard():
		return update(t, m, keyEnter)
	}
	return flipView(t, m)
}

// flipViewKey is flipView for a caller that wants the model alone.
func flipViewKey(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = flipView(t, m)
	return m
}

// focusedModel opens a pane hosting "a" and focuses it via the enter key,
// returning the model in focused state with the fake pane attached.
func focusedModel(t *testing.T, fp *fakePane) Model {
	t.Helper()
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	m, _ = update(t, m, keyEnter)
	if !m.paneOwnsKeyboard() {
		t.Fatalf("precondition: pane should be focused after pressing enter")
	}
	return m
}

func TestFocusKeyFocusesActivePane(t *testing.T) {
	fp := newFakePane("X")
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	if m.paneOwnsKeyboard() {
		t.Fatalf("a freshly opened pane starts unfocused")
	}
	got, cmd := update(t, m, keyEnter)
	if !got.paneOwnsKeyboard() {
		t.Fatalf("enter should focus the active pane")
	}
	if cmd != nil {
		t.Fatalf("focusing is pure state, expected nil cmd")
	}
	if !strings.Contains(got.status, "ctrl+] esc") {
		t.Fatalf("status = %q, want the way back named", got.status)
	}
}

// TestFocusKeyWithNoPaneFocusesTheDetailPane pins `d`, the details key, which
// until T16.5 gives the overview the keyboard (A2) — the job `f` had in the
// overview view before T16.2. testModel HAS an active project and a selected
// session, so the detail box is there to focus.
//
// The empty-state subtest is the load-bearing half: with no project the
// dashboard is at All projects, where `d` is no key, so it must do nothing
// at all and say nothing.
func TestFocusKeyWithNoPaneFocusesTheDetailPane(t *testing.T) {
	t.Run("detail pane drawn", func(t *testing.T) {
		m := testModel(sess("a"))
		got, cmd := pressAction(t, m, keyRune('d'))
		if !got.detailOwnsKeyboard() {
			t.Fatalf("d should focus the detail pane")
		}
		if got.paneOwnsKeyboard() {
			t.Fatalf("d must not set pane focus when there is no pane")
		}
		if cmd != nil {
			t.Fatalf("focusing is pure state, expected nil cmd")
		}
		if !strings.Contains(got.status, "esc") {
			t.Fatalf("status = %q, want the release chord named", got.status)
		}
	})

	t.Run("empty state is a genuine no-op", func(t *testing.T) {
		m := testModel()
		m.activeProject = -1
		got, cmd := update(t, m, keyRune('d'))
		if got.detailOwnsKeyboard() || got.paneOwnsKeyboard() {
			t.Fatalf("d must focus nothing with no detail box and no pane")
		}
		if cmd != nil {
			t.Fatalf("expected nil cmd")
		}
		if got.status != "" {
			t.Fatalf("status = %q, want a silent no-op", got.status)
		}
	})
}

func TestEnterOnHostedSessionFocusesPane(t *testing.T) {
	fp := newFakePane("X")
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	// The selected session ("a") is the one hosted in the pane: enter focuses.
	got, cmd := update(t, m, keyEnter)
	if cmd != nil {
		t.Fatalf("enter on the hosted session should focus (no jump cmd), got a cmd")
	}
	if !got.paneOwnsKeyboard() {
		t.Fatalf("enter on the hosted session should focus the pane")
	}
}

func TestEnterOnHostedSessionFocusesEvenIfStale(t *testing.T) {
	// A live pane means the session is running regardless of on-disk status;
	// enter must focus it, not report staleness. Guards the ordering: the
	// hosted-focus branch precedes the stale check.
	fp := newFakePane("X")
	s := sess("a")
	s.StatusStale = true
	s.StaleReason = state.StaleUnparseable
	m := testModel(s)
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	got, cmd := update(t, m, keyEnter)
	if cmd != nil || !got.paneOwnsKeyboard() {
		t.Fatalf("enter on the hosted (but stale-on-disk) session should focus: focused=%v cmd=%v", got.paneOwnsKeyboard(), cmd != nil)
	}
	if strings.Contains(got.status, "stale") {
		t.Fatalf("status = %q, should not report staleness for the hosted pane", got.status)
	}
}

func TestFocusedPaneForwardsText(t *testing.T) {
	fp := newFakePane("X")
	m := focusedModel(t, fp)
	got, cmd := update(t, m, keyRune('x'))
	if cmd != nil {
		t.Fatalf("forwarding is synchronous, expected nil cmd")
	}
	if !got.paneOwnsKeyboard() {
		t.Fatalf("typing should not change focus")
	}
	if len(fp.texts) != 1 || fp.texts[0] != "x" {
		t.Fatalf("SendText calls = %v, want [\"x\"]", fp.texts)
	}
	if len(fp.keys) != 0 {
		t.Fatalf("a plain rune should route through SendText, not SendKey; keys=%v", fp.keys)
	}
}

func TestFocusedPaneForwardsSpaceAsText(t *testing.T) {
	fp := newFakePane("X")
	m := focusedModel(t, fp)
	space := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	update(t, m, space)
	if len(fp.texts) != 1 || fp.texts[0] != " " {
		t.Fatalf("space SendText calls = %v, want [\" \"]", fp.texts)
	}
}

func TestFocusedPaneForwardsNavKeysToChild(t *testing.T) {
	// Arrows/enter/backspace go to the child (not the dashboard's cursor
	// navigation) while focused, encoded via SendKey so the emulator applies
	// mode-aware encoding.
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want termpane.KeyEvent
	}{
		{"up", keyUp, termpane.KeyEvent{Code: vt.KeyUp}},
		{"down", keyDown, termpane.KeyEvent{Code: vt.KeyDown}},
		{"enter", keyEnter, termpane.KeyEvent{Code: vt.KeyEnter}},
		{"tab", tea.KeyPressMsg{Code: tea.KeyTab}, termpane.KeyEvent{Code: vt.KeyTab}},
		{"shift+tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, termpane.KeyEvent{Code: vt.KeyTab, Mod: vt.ModShift}},
		{"backspace", tea.KeyPressMsg{Code: tea.KeyBackspace}, termpane.KeyEvent{Code: vt.KeyBackspace}},
		{"esc", tea.KeyPressMsg{Code: tea.KeyEsc}, termpane.KeyEvent{Code: vt.KeyEscape}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePane("X")
			m := focusedModel(t, fp)
			startCur := m.listSelection()
			got, _ := update(t, m, tc.msg)
			if got.listSelection() != startCur {
				t.Fatalf("focused nav must not move the dashboard cursor: got %d want %d", got.listSelection(), startCur)
			}
			if len(fp.keys) != 1 || fp.keys[0] != tc.want {
				t.Fatalf("SendKey calls = %v, want [%v]", fp.keys, tc.want)
			}
			if len(fp.texts) != 0 {
				t.Fatalf("special key must not route through SendText; texts=%v", fp.texts)
			}
		})
	}
}

func TestFocusedPaneForwardsCtrlCToChildNotQuit(t *testing.T) {
	// Crucible: while focused, Ctrl+C belongs to the child (SIGINT), NOT the
	// dashboard's quit — it must forward and never set quitting.
	fp := newFakePane("X")
	m := focusedModel(t, fp)
	got, cmd := update(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if got.quitting {
		t.Fatalf("Ctrl+C while focused must not quit the dashboard")
	}
	if cmd != nil {
		t.Fatalf("Ctrl+C while focused must not return tea.Quit")
	}
	want := termpane.KeyEvent{Code: 'c', Mod: vt.ModCtrl}
	if len(fp.keys) != 1 || fp.keys[0] != want {
		t.Fatalf("SendKey calls = %v, want ctrl+c [%v]", fp.keys, want)
	}
}

func TestFocusedPaneForwardsAltRune(t *testing.T) {
	fp := newFakePane("X")
	m := focusedModel(t, fp)
	update(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt})
	want := termpane.KeyEvent{Code: 'b', Mod: vt.ModAlt}
	if len(fp.keys) != 1 || fp.keys[0] != want {
		t.Fatalf("alt+b SendKey calls = %v, want [%v]", fp.keys, want)
	}
	if len(fp.texts) != 0 {
		t.Fatalf("alt-modified rune must route through SendKey, not SendText; texts=%v", fp.texts)
	}
}

func TestEscapeChordDetachesFocus(t *testing.T) {
	fp := newFakePane("X")
	m := focusedModel(t, fp)
	got, cmd := pressAction(t, m, keyEscMsg())
	if got.paneOwnsKeyboard() {
		t.Fatalf("ctrl+\\ should detach focus back to the dashboard")
	}
	if cmd != nil {
		t.Fatalf("detach is pure state, expected nil cmd")
	}
	// The chord is consumed by the dashboard, NOT forwarded to the child.
	if len(fp.keys) != 0 || len(fp.texts) != 0 {
		t.Fatalf("the escape chord must not reach the child: keys=%v texts=%v", fp.keys, fp.texts)
	}
	if got.pane == nil {
		t.Fatalf("detaching must not close the pane; the session keeps running")
	}
	// After detaching, dashboard keybindings work again (e.g. q quits).
	got2, cmd2 := pressAction(t, got, keyRune('q'))
	if !got2.quitting || cmd2 == nil {
		t.Fatalf("after detach, q should quit the dashboard; quitting=%v cmd=%v", got2.quitting, cmd2 != nil)
	}
}

func TestFocusedPaneForwardsPaste(t *testing.T) {
	fp := newFakePane("X")
	m := focusedModel(t, fp)
	paste := tea.PasteMsg{Content: "line one\nline two"}
	update(t, m, paste)
	if len(fp.pastes) != 1 || fp.pastes[0] != "line one\nline two" {
		t.Fatalf("Paste calls = %v, want the multi-line payload through the paste path", fp.pastes)
	}
	if len(fp.texts) != 0 || len(fp.keys) != 0 {
		t.Fatalf("a paste must route through Paste only; texts=%v keys=%v", fp.texts, fp.keys)
	}
}

func TestWindowSizePropagatesResizeToPane(t *testing.T) {
	fp := newFakePane("X")
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	if len(fp.resizes) != 0 {
		t.Fatalf("opening a pane should not itself resize it: %v", fp.resizes)
	}
	got, _ := update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if got.width != 100 || got.height != 30 {
		t.Fatalf("window size not recorded: %dx%d", got.width, got.height)
	}
	if len(fp.resizes) != 1 {
		t.Fatalf("WindowSizeMsg should resize the pane exactly once, got %d: %v", len(fp.resizes), fp.resizes)
	}
	if fp.resizes[0][0] < 1 || fp.resizes[0][1] < 1 {
		t.Fatalf("resize dims must be positive, got %v", fp.resizes[0])
	}
	// The pane region is narrower than the full terminal (sidebar + borders).
	if fp.resizes[0][0] >= 100 {
		t.Fatalf("pane cols %d should be less than the terminal width (sidebar+chrome)", fp.resizes[0][0])
	}
}

func TestWindowSizeWithoutPaneDoesNotPanic(t *testing.T) {
	m := testModel(sess("a"))
	got, _ := update(t, m, tea.WindowSizeMsg{Width: 90, Height: 25})
	if got.width != 90 || got.height != 25 || !got.ready {
		t.Fatalf("window size not applied without a pane: %dx%d ready=%v", got.width, got.height, got.ready)
	}
}

func TestFocusStateReflectedInView(t *testing.T) {
	fp := newFakePane("PANE-BODY")
	m := testModel(sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})

	// Unfocused: the help and title advertise how to focus.
	unfocused := render(m)
	if !strings.Contains(unfocused, "focus") {
		t.Fatalf("unfocused View should mention focusing; out:\n%s", unfocused)
	}

	// Focused: the help/title advertise the detach chord.
	m, _ = update(t, m, keyEnter)
	focused := render(m)
	if !strings.Contains(focused, "ctrl+] esc") {
		t.Fatalf("focused View should show the ctrl+] esc back-to-list chord; out:\n%s", focused)
	}
}

// TestPaneFullWidthContentDoesNotWrap is the fix-cycle-2 geometry regression
// test: the emulator/PTY are sized by paneContentDims, and the pane box must
// wrap text at EXACTLY that width. lipgloss v1's Style.Width includes
// horizontal padding (text wraps at Width minus padding), so a box styled at
// the emulator's width would wrap every full-width emulator row — doubling
// the line count and letting MaxHeight truncate the screen. Short-string
// renders cannot catch that class of bug, so this test pushes rows of exactly
// the emulator's width through the real renderPane style/geometry path.
func TestPaneFullWidthContentDoesNotWrap(t *testing.T) {
	m := testModel(sess("a"))
	_, mainOuter, bodyHeight := m.layoutDims()
	cols, rows := paneContentDims(mainOuter, bodyHeight)
	if cols < 10 || rows < 3 {
		t.Fatalf("unexpectedly small pane content dims %dx%d for a 120x40 terminal", cols, rows)
	}

	// A full screen of full-width rows; the final row carries a distinct
	// marker so truncation (wrapping's observable failure mode) is provable.
	lines := make([]string, rows)
	for i := range lines {
		lines[i] = strings.Repeat("x", cols)
	}
	lastRow := "Z" + strings.Repeat("z", cols-2) + "Z"
	lines[rows-1] = lastRow
	fp := newFakePane(strings.Join(lines, "\n"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})

	out := m.renderPane(mainOuter, bodyHeight)

	// If any full-width row wrapped, the content grows past the box height
	// and MaxHeight drops the bottom — the final row's marker disappears (or
	// is split across lines). It must survive contiguous on one line.
	if !strings.Contains(out, lastRow) {
		t.Fatalf("full-width final emulator row missing or wrapped in the rendered pane box (emulator %dx%d, box outer %dx%d):\n%s",
			cols, rows, mainOuter, bodyHeight, out)
	}
	// The box must fill the body region exactly — no wrap-induced growth.
	if got := lipgloss.Height(out); got != bodyHeight {
		t.Fatalf("pane box height = %d, want exactly the body height %d", got, bodyHeight)
	}
	// And no rendered line may exceed the main region's outer width.
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > mainOuter {
			t.Fatalf("line %d exceeds the main region width %d (got %d): %q", i, mainOuter, w, line)
		}
	}
}

// --- startup pane handoff (W6.T1) ---

// TestInitialPaneOpenWaitsForLoadAndSize is the acceptance-criteria test: the
// startup pane (Deps.InitialSessionName — afk new's TTY handoff, or `afk
// dash --open`) only opens once BOTH the first discovery load and the first
// tea.WindowSizeMsg have arrived. Here WindowSizeMsg arrives first.
func TestInitialPaneOpenWaitsForLoadAndSize(t *testing.T) {
	fp := newFakePane("STARTUP-PANE")
	deps := testDeps()
	deps.InitialSessionName = "fresh"
	var gotCfg hostPaneConfig
	deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		gotCfg = cfg
		return fp, nil
	}
	m := New("/repo", time.Second, deps)

	// WindowSizeMsg alone (no discovery load yet) must not open a pane.
	m, cmd := update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd != nil {
		t.Fatalf("expected no cmd before the first discovery load")
	}
	if m.pane != nil {
		t.Fatalf("pane opened before the first discovery load")
	}

	// The first discovery load (session found, host live) is the second
	// arrival: it should now trigger the connect.
	m, cmd = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{runningSess("fresh")}, at: time.Unix(1, 0)})
	if cmd == nil {
		t.Fatalf("expected a connect cmd once both load and size have arrived")
	}
	msg := cmd()
	attached, ok := msg.(paneAttachedMsg)
	if !ok {
		t.Fatalf("connect cmd produced %T, want paneAttachedMsg", msg)
	}
	if gotCfg.Name != "fresh" || gotCfg.Worktree != "/wt/fresh" {
		t.Fatalf("hostPaneConfig = %+v, want the session's name and worktree", gotCfg)
	}
	if gotCfg.Cols < 1 || gotCfg.Rows < 1 {
		t.Fatalf("hostPaneConfig size = %dx%d, want positive", gotCfg.Cols, gotCfg.Rows)
	}

	m, _ = update(t, m, attached)
	if m.pane == nil || m.paneName != "fresh" {
		t.Fatalf("startup pane not stored: pane=%v name=%q", m.pane != nil, m.paneName)
	}
}

// TestInitialPaneOpenLoadThenSize covers the other arrival order: the first
// discovery load arrives before the first tea.WindowSizeMsg.
func TestInitialPaneOpenLoadThenSize(t *testing.T) {
	fp := newFakePane("STARTUP-PANE")
	deps := testDeps()
	deps.InitialSessionName = "fresh"
	deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return fp, nil }
	m := New("/repo", time.Second, deps)

	m, cmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{runningSess("fresh")}, at: time.Unix(1, 0)})
	if cmd != nil {
		t.Fatalf("expected no cmd before the first WindowSizeMsg")
	}
	if m.pane != nil {
		t.Fatalf("pane opened before the first resize")
	}

	m, cmd = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd == nil {
		t.Fatalf("expected a connect cmd once size arrives after load")
	}
	if _, ok := cmd().(paneAttachedMsg); !ok {
		t.Fatalf("expected paneAttachedMsg from the connect cmd")
	}
}

// TestInitialPaneOpenRestartsAStoppedHostFirst is the `afk new` handoff's
// common case, not an edge one: the dashboard starts microseconds after the
// host was spawned, so the first discovery pass routinely predates host.json.
// The startup pane must take the restart branch (exactly as ctrl+t does) rather
// than reporting a failure.
func TestInitialPaneOpenRestartsAStoppedHostFirst(t *testing.T) {
	var spawned string
	deps := testDeps()
	deps.InitialSessionName = "fresh"
	deps.SpawnHost = func(name, worktree string) (session.HostProcess, error) {
		spawned = name
		return session.HostProcess{PID: 4242}, nil
	}
	deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("X"), nil }
	m := New("/repo", time.Second, deps)

	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	// sess() has HostRunning false — the freshly-spawned-host case.
	m, cmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("fresh")}, at: time.Unix(1, 0)})
	if cmd == nil {
		t.Fatalf("expected a restart cmd for a session whose host is not up yet")
	}
	hr, ok := cmd().(hostRestartedMsg)
	if !ok {
		t.Fatalf("startup cmd produced %T, want hostRestartedMsg", cmd())
	}
	if spawned != "fresh" {
		t.Fatalf("SpawnHost called with %q, want fresh", spawned)
	}
	if !strings.Contains(m.status, "restarting fresh") {
		t.Fatalf("status = %q, want the restarting notice", m.status)
	}

	// And the restart chains into the pane open, marked as a restart so an
	// unreachable host is phrased as "did not start" rather than "stale record".
	m, attachCmd := update(t, m, hr)
	if attachCmd == nil {
		t.Fatalf("a successful startup restart should chain into the connect")
	}
	attached, ok := attachCmd().(paneAttachedMsg)
	if !ok {
		t.Fatalf("chained cmd produced %T, want paneAttachedMsg", attachCmd())
	}
	if !attached.restarted {
		t.Fatalf("the chained connect must be marked restarted")
	}
}

// TestInitialPaneOpenSessionNotFoundShowsErrorAndDoesNotRetry covers the
// "session isn't found in the load" fallback and the at-most-once guarantee:
// a later load that DOES contain the session must not retry the open.
func TestInitialPaneOpenSessionNotFoundShowsErrorAndDoesNotRetry(t *testing.T) {
	openCalls := 0
	deps := testDeps()
	deps.InitialSessionName = "missing"
	deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		openCalls++
		return newFakePane("X"), nil
	}
	m := New("/repo", time.Second, deps)

	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("other")}, at: time.Unix(1, 0)})
	if cmd != nil {
		t.Fatalf("expected no connect cmd when the named session is not found")
	}
	if !strings.Contains(m.status, "missing") || !strings.Contains(m.status, "no such session") {
		t.Fatalf("status = %q, want it to name the missing session", m.status)
	}

	m, cmd = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{runningSess("missing")}, at: time.Unix(2, 0)})
	if cmd != nil {
		t.Fatalf("expected no retry once the attempt has already been made")
	}
	if openCalls != 0 {
		t.Fatalf("OpenHostPane should never have been called, got %d calls", openCalls)
	}
}

// TestInitialPaneOpenSkippedWhileFormActive is the startup/form race guard: the
// creation form can be opened (n) before the first load + first size both
// arrive, and the startup pane must then be SKIPPED — never opened behind the
// modal form. The attempt is consumed (not deferred), so a later load after the
// form closes must not retry either.
func TestInitialPaneOpenSkippedWhileFormActive(t *testing.T) {
	openCalls := 0
	deps := testDeps()
	deps.InitialSessionName = "fresh"
	deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		openCalls++
		return newFakePane("X"), nil
	}
	m := New("/repo", time.Second, deps)

	// The user opens the creation form before startup gating completes.
	m, _ = pressAction(t, m, keyRune('n'))
	if !m.formOpen {
		t.Fatalf("precondition: the form should be open")
	}

	m, cmd := update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd != nil {
		t.Fatalf("expected no cmd before the first discovery load")
	}
	m, cmd = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{runningSess("fresh")}, at: time.Unix(1, 0)})
	if cmd != nil {
		t.Fatalf("no startup pane may open behind the active form")
	}
	if m.pane != nil || openCalls != 0 {
		t.Fatalf("startup pane opened behind the form: pane=%v openCalls=%d", m.pane != nil, openCalls)
	}
	if !m.formOpen {
		t.Fatalf("the form must stay open")
	}
	if !strings.Contains(m.status, "skipped") || !strings.Contains(m.status, "fresh") {
		t.Fatalf("status = %q, want a skipped-startup-pane message naming the session", m.status)
	}

	// The attempt is consumed: closing the form and delivering another load
	// must not retry the startup open.
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, cmd = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{runningSess("fresh")}, at: time.Unix(2, 0)})
	if cmd != nil || openCalls != 0 || m.pane != nil {
		t.Fatalf("consumed startup attempt must not retry after the form closes: cmd=%v openCalls=%d",
			cmd != nil, openCalls)
	}
}

// TestNoInitialSessionNeverOpensAPane is the control: with
// Deps.InitialSessionName unset (every other test in this file), load+resize
// must never open a pane on their own.
func TestNoInitialSessionNeverOpensAPane(t *testing.T) {
	m := testModel(sess("a"))
	m, cmd := update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd != nil {
		t.Fatalf("expected no cmd with no initial session configured")
	}
	m, cmd = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("a")}, at: time.Unix(1, 0)})
	if cmd != nil {
		t.Fatalf("expected no connect cmd with no initial session configured")
	}
	if m.pane != nil {
		t.Fatalf("no pane should have opened")
	}
}

// --- tick reschedules + reloads ---

func TestTickReturnsCmd(t *testing.T) {
	m := testModel(sess("a"))
	_, cmd := update(t, m, tickMsg(time.Unix(1, 0)))
	if cmd == nil {
		t.Fatalf("tickMsg should return a batch (reload + next tick)")
	}
}

// --- spinner tick lifecycle (W7.T2) ---
//
// The spinner's tick loop is armed from the discovery tick (tickMsg's case
// in Update), not from the dispatch sites that add an in-flight operation —
// see startSpinnerIfNeeded's doc comment for why: those sites are each
// under an existing test that runs the returned Cmd directly and
// type-asserts a specific message out of it (e.g. TestToggleWithNoPaneRestartsStoppedSelectedSession
// wants cmd() to BE a hostRestartedMsg), so adding a second Cmd there would
// turn every one of those into a tea.BatchMsg and break them. These four
// rows are therefore the load-bearing ones for the whole mechanism: arming
// on tickMsg while something is outstanding, NOT arming while idle, and the
// spinner.TickMsg case itself sustaining or killing the self-perpetuating
// chain.

// hasSpinnerTick flattens one level of cmd's batch (collectMsgs, defined in
// focusrequest_test.go) and reports whether a spinner.TickMsg is among the
// results.
func hasSpinnerTick(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	for _, msg := range collectMsgs(t, cmd) {
		if _, ok := msg.(spinner.TickMsg); ok {
			return true
		}
	}
	return false
}

// TestSpinnerTickArmsWhileAnOperationIsInFlight drives hasInFlightWork
// through pendingSpawns — the same tracker submitNewForm's own dispatch
// populates — rather than through a dispatch site's Cmd, per the package
// comment above.
func TestSpinnerTickArmsWhileAnOperationIsInFlight(t *testing.T) {
	m := testModel(sess("a"))
	m.pendingSpawns = []string{"creating-one"}

	_, cmd := update(t, m, tickMsg(time.Unix(1, 0)))
	if cmd == nil {
		t.Fatal("the tick must still dispatch its usual batch")
	}
	if !hasSpinnerTick(t, cmd) {
		t.Fatal("expected the discovery tick to arm the spinner while a spawn is pending")
	}
}

// TestSpinnerTickDoesNotArmWhileIdle is the plan's own must-not: an idle
// dashboard must not pay a redraw loop it does not need.
func TestSpinnerTickDoesNotArmWhileIdle(t *testing.T) {
	m := testModel(sess("a"))

	_, cmd := update(t, m, tickMsg(time.Unix(1, 0)))
	if hasSpinnerTick(t, cmd) {
		t.Fatal("an idle dashboard must not arm the spinner's tick loop")
	}
}

// TestSpinnerTickStopsOnceEverythingFinishes is the acceptance criterion's
// other half: once nothing is left in flight, a spinner.TickMsg already in
// flight must not be re-issued — the self-perpetuating chain has to die
// rather than keep ticking forever, and spinnerActive must clear so a later
// operation re-arms a fresh loop instead of finding the flag already set.
func TestSpinnerTickStopsOnceEverythingFinishes(t *testing.T) {
	m := testModel(sess("a"))
	m.spinnerActive = true // as if some earlier tick had armed the loop

	next, cmd := update(t, m, spinner.TickMsg{})
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil once hasInFlightWork is false", cmd)
	}
	if next.spinnerActive {
		t.Fatal("spinnerActive should clear once the loop stops")
	}
}

// TestSpinnerTickContinuesWhileStillInFlight is the inverse: the loop must
// keep sustaining itself for as long as something is outstanding.
func TestSpinnerTickContinuesWhileStillInFlight(t *testing.T) {
	m := testModel(sess("a"))
	m.spinnerActive = true
	m.cleaningSessions = map[cleanID]bool{cleanIDFor(sess("a")): true}

	_, cmd := update(t, m, spinner.TickMsg{})
	if cmd == nil {
		t.Fatal("expected the tick chain to continue while a clean is in flight")
	}
}

// --- spinner render (W7.T2 fix) ---
//
// The four tests above cover only the tick LIFECYCLE — whether a Cmd is
// issued — never the render output. The spinner's render branch could gate on
// the wrong condition, displace a help item, or be truncated away, and all
// four would still pass. These two tests are the load-bearing pair for the
// render half of the acceptance criterion: the first proves the spinner frame
// actually reaches the footer; the second proves it does NOT when nothing is
// in flight, which is what would catch an inverted or dropped
// hasInFlightWork() gate.
//
// The surface moved with this wave. The spinner used to lead the footer's
// status row, which no longer exists; it leads the help row now (see
// helpSpinnerPrefix), and deliberately not the toast, which expires after
// toastTTL while a create or a clean is still running. So the assertion is
// about the help row and no longer mentions any status text.

// TestSpinnerRendersOnTheHelpRow drives hasInFlightWork() through
// pendingSpawns, exactly as TestSpinnerTickArmsWhileAnOperationIsInFlight
// does above, and asserts the spinner's current frame leads the rendered
// footer with the help line intact behind it.
//
// m.spinner is never ticked here, so its frame is frame 0 of MiniDot. The
// comparison is against m.spinner.View() rather than a literal glyph so the
// test does not pin bubbles' frame set, and the ANSI strip is what makes the
// "leads the row" claim about cells the user sees rather than about where
// lipgloss chose to put an escape sequence.
func TestSpinnerRendersOnTheHelpRow(t *testing.T) {
	// 41 and 80 are the load-bearing widths: at both, the help line lands
	// exactly on its budget, so a spinner glued on AFTER renderModeHelp's cut
	// overflows the row by its own two cells. Verified by mutation — dropping
	// the budget subtraction in renderHelp fails both and leaves 120, where
	// the line has slack, passing. 120 is kept as the ordinary case.
	for _, width := range []int{41, 80, 120} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			m := testModel(sess("a"))
			m.width = width
			m.pendingSpawns = []string{"a"}

			footer := xansi.Strip(m.renderFooter())
			want := xansi.Strip(m.spinner.View()) + " "
			if !strings.HasPrefix(footer, want) {
				t.Fatalf("footer = %q, want it to lead with the spinner frame %q", footer, want)
			}
			if !strings.Contains(footer, "j/k move") {
				t.Fatalf("footer = %q, want the help line still rendered behind the spinner", footer)
			}
			if w := lipgloss.Width(strings.TrimRight(footer, " ")); w > m.width {
				t.Fatalf("footer is %d cells wide with the spinner showing, want at most %d", w, m.width)
			}
		})
	}
}

// TestSpinnerAbsentWhenNothingInFlight is the negative case the gate exists to
// guard against: with nothing outstanding in pendingSpawns, cleaningSessions,
// attachInFlight or otherOpsInFlight, the help row renders on its own.
//
// A status is set and asserted ABSENT as well, which is the second half of
// this wave's contract: the footer carries help and nothing else, whatever is
// in the status slot.
func TestSpinnerAbsentWhenNothingInFlight(t *testing.T) {
	m := testModel(sess("a"))
	m.notify(notifyInfo, "created a — running in the background · ctrl+t to open")

	footer := xansi.Strip(m.renderFooter())
	if strings.HasPrefix(footer, xansi.Strip(m.spinner.View())) {
		t.Fatalf("footer = %q, should not lead with a spinner frame when hasInFlightWork() is false", footer)
	}
	if strings.Contains(footer, "running in the background") {
		t.Fatalf("footer = %q, must not carry notification text", footer)
	}
}

// --- loadCmd wiring (Cmd producer, exercised directly) ---

func TestLoadCmdBuildsSessionsWithTails(t *testing.T) {
	var fullCalls, numstatCalls []string
	m := New("/repo", time.Second, Deps{
		Discover: func(repo string) ([]discover.SessionState, error) {
			if repo != "/repo" {
				t.Fatalf("Discover got repo %q, want /repo", repo)
			}
			return []discover.SessionState{sampleState("a"), sampleState("b")}, nil
		},
		ReadTail: func(path string, n int) []string {
			if n != tailLines {
				t.Fatalf("ReadTail n = %d, want %d", n, tailLines)
			}
			if strings.HasSuffix(path, "learnings.md") {
				return []string{"learned x"}
			}
			return []string{"issue y"}
		},
		Now:            func() time.Time { return time.Unix(42, 0) },
		ResolveBaseRef: func(_, base string) (string, bool) { return "refs/heads/" + base, true },
		DiffFull: func(dir, ref string) (string, error) {
			fullCalls = append(fullCalls, dir)
			return "", nil
		},
		DiffNumstat: func(dir, base string) (string, error) {
			numstatCalls = append(numstatCalls, dir)
			return "", nil
		},
	})

	msg := m.loadCmd()().(sessionsLoadedMsg)
	if msg.err != nil {
		t.Fatalf("unexpected err: %v", msg.err)
	}
	if len(msg.sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(msg.sessions))
	}
	if got := msg.sessions[0].LearningsTail; len(got) != 1 || got[0] != "learned x" {
		t.Fatalf("LearningsTail = %v", got)
	}
	if got := msg.sessions[0].IssuesTail; len(got) != 1 || got[0] != "issue y" {
		t.Fatalf("IssuesTail = %v", got)
	}
	if msg.at != time.Unix(42, 0) {
		t.Fatalf("at not from deps.Now()")
	}
	// Nothing is selected (empty cursor on an empty list at Cmd-creation
	// time), so neither session is "focused" — both must take the cheap
	// numstat path, and the expensive full-diff path must never run.
	if len(fullCalls) != 0 {
		t.Fatalf("DiffFull called %v times for unfocused sessions, want 0", fullCalls)
	}
	if len(numstatCalls) != 2 {
		t.Fatalf("DiffNumstat called %d times, want 2", len(numstatCalls))
	}
}

func TestLoadCmdFocusedSessionGetsFullDiff(t *testing.T) {
	var fullCalls, numstatCalls []string
	m := New("/repo", time.Second, Deps{
		Discover: func(string) ([]discover.SessionState, error) {
			return []discover.SessionState{sampleState("a"), sampleState("b")}, nil
		},
		ReadTail:       func(string, int) []string { return nil },
		Now:            time.Now,
		ResolveBaseRef: func(_, base string) (string, bool) { return "refs/heads/" + base, true },
		DiffFull: func(dir, ref string) (string, error) {
			fullCalls = append(fullCalls, dir)
			return "", nil
		},
		DiffNumstat: func(dir, base string) (string, error) {
			numstatCalls = append(numstatCalls, dir)
			return "", nil
		},
	})
	// Discovery hasn't run yet in this test, so seed m.sessions/cursor by
	// hand to select "a" — mirrors what a real tick sees (the previous
	// pass's session list) without depending on loadCmd itself.
	m.setSessions([]Session{sess("a"), sess("b")})
	m.selectSessionIndex(0)

	msg := m.loadCmd()().(sessionsLoadedMsg)
	if msg.err != nil {
		t.Fatalf("unexpected err: %v", msg.err)
	}
	if len(fullCalls) != 1 || fullCalls[0] != "/wt/a" {
		t.Fatalf("DiffFull calls = %v, want exactly one call for /wt/a", fullCalls)
	}
	if len(numstatCalls) != 1 || numstatCalls[0] != "/wt/b" {
		t.Fatalf("DiffNumstat calls = %v, want exactly one call for /wt/b", numstatCalls)
	}
}

func TestLoadCmdSurfacesDiscoverError(t *testing.T) {
	m := New("/repo", time.Second, Deps{
		Discover: func(string) ([]discover.SessionState, error) { return nil, errors.New("no git") },
		Now:      func() time.Time { return time.Unix(1, 0) },
	})
	msg := m.loadCmd()().(sessionsLoadedMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "no git") {
		t.Fatalf("err = %v, want 'no git'", msg.err)
	}
}

// --- View smoke: exercises list + all detail panels without a TTY ---

func TestViewDoesNotPanic(t *testing.T) {
	rich := Session{
		SessionState: discover.SessionState{
			Name:         "rich",
			WorktreePath: "/wt/rich",
			Branch:       "rich",
			BaseBranch:   "main",
			Phase:        state.PhaseReviewing,
			LoopCount:    1,
			UpdatedAt:    "2026-07-10T00:00:00Z",
			Progress:     discover.Progress{Total: 3, Complete: 1, Failed: 1, Incomplete: 1},
			Plan: &state.Plan{
				Title: "Rich",
				Waves: []state.Wave{{
					Number: 1, Name: "Setup",
					Tasks: []state.Task{
						{ID: "W1.T1", Title: "Do", State: state.CheckComplete},
						{ID: "W1.T2", Title: "Broke", State: state.CheckFailed, FailureReason: "boom"},
						{ID: "W1.T3", Title: "Todo", State: state.CheckIncomplete},
					},
				}},
			},
			Verdict: &state.ReviewVerdict{
				Verdict: state.VerdictReject, Compliance: state.CheckFail,
				Quality: state.CheckPass, QA: state.CheckPass,
				Findings: []state.Finding{{Severity: state.SeverityBlocker, TaskID: "W1.T2", Description: "broke"}},
			},
		},
		LearningsTail: []string{"line 1", "line 2"},
		IssuesTail:    []string{"issue 1"},
	}
	stale := Session{SessionState: discover.SessionState{
		Name: "stale", WorktreePath: "/wt/stale",
		StatusStale: true, StaleReason: state.StaleUnknownField,
	}}
	awaiting := sess("awaiting")
	awaiting.AwaitingInput = true
	pending := Session{SessionState: discover.SessionState{Name: "pending"}, Pending: true}

	m := testModel(rich, stale, awaiting, pending)
	// The overview's panels are what this exercises; in the agent view a row
	// with no live agent draws the agent placeholder instead. The overview is
	// drawn with the keyboard on it, as `d` gives it (the project view takes
	// the body otherwise, T16.4), and rich is selected by name because the
	// list is in screen order.
	m.view, m.focus = viewOverview, focusRight
	m.selectSessionIndex(namedIndex(t, m, "rich"))
	m.lastLoad = time.Unix(999_000, 0)
	m.status = "some status"

	out := render(m)
	if out == "" {
		t.Fatalf("View returned empty string")
	}
	for _, want := range []string{headerBadgeText, "rich", "stale", "awaiting", "pending", "creating…", "Overview", "Verdict"} {
		if !strings.Contains(out, want) {
			t.Errorf("View output missing %q", want)
		}
	}

	// The placeholder's detail pane is a separate branch from the panel loop, so
	// it needs the cursor parked on it to be rendered at all.
	onPending := testModel(rich, pending)
	onPending.view, onPending.focus = viewOverview, focusRight
	onPending.selectSessionIndex(namedIndex(t, onPending, "pending"))
	pendingOut := render(onPending)
	for _, want := range []string{"pending", "creating session"} {
		if !strings.Contains(pendingOut, want) {
			t.Errorf("pending-selected View missing %q", want)
		}
	}

	// Empty-list and not-ready paths must not panic either.
	if got := render(testModel()); got == "" {
		t.Fatalf("empty-list View returned empty")
	}
	notReady := New("/repo", time.Second, testDeps())
	if got := render(notReady); got == "" {
		t.Fatalf("not-ready View returned empty")
	}

	// The creation form takes over the main region and the confirm modal
	// replaces the whole footer (renderFooter) — a tiny terminal must not
	// panic with either active. Cover a battery of pathologically small
	// width/height combinations, including 0 (the truncate(s, 0) == "" edge
	// case) and 1.
	for _, dims := range []struct{ w, h int }{{0, 0}, {1, 1}, {1, 40}, {40, 1}, {5, 3}, {24, 8}} {
		activeForm := testModel(rich)
		activeForm.width, activeForm.height = dims.w, dims.h
		activeForm.formOpen = true
		activeForm.form = newNewForm(activeForm.stack, activeForm.plainBranches, activeForm.plainTrunk)
		activeForm.form.err = "invalid session name: some very long validation message here"

		activeConfirm := testModel(rich)
		activeConfirm.width, activeConfirm.height = dims.w, dims.h
		activeConfirm.modal = inputConfirmClean
		activeConfirm.confirmTarget = "rich"
		activeConfirm.confirmPhase = state.PhaseExecuting

		activeSpawnErr := testModel(rich, pending)
		activeSpawnErr.width, activeSpawnErr.height = dims.w, dims.h
		activeSpawnErr.modal = inputSpawnError
		activeSpawnErr.spawnErrName = "alpha"
		activeSpawnErr.spawnErrHeadline = spawnFailedHeadline("alpha", errors.New("exit status 1"))
		activeSpawnErr.spawnErrTail = []string{
			"afk new: branch alpha already exists and is checked out somewhere else",
			"a second, quite long line of captured child output to force truncation",
		}

		for _, mm := range []struct {
			name string
			m    Model
		}{{"form", activeForm}, {"confirm", activeConfirm}, {"spawnErr", activeSpawnErr}} {
			t.Run(fmt.Sprintf("%s/w=%d/h=%d", mm.name, dims.w, dims.h), func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("View() panicked at width=%d height=%d mode=%s: %v", dims.w, dims.h, mm.name, r)
					}
				}()
				_ = render(mm.m)
			})
		}
	}
}

// --- small helpers ---

func TestReadTail(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/n.md"
	if err := os.WriteFile(path, []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readTail(path, 2)
	if len(got) != 2 || got[0] != "l3" || got[1] != "l4" {
		t.Fatalf("readTail = %v, want [l3 l4]", got)
	}
	// Missing file → nil, not an error.
	if got := readTail(dir+"/missing.md", 5); got != nil {
		t.Fatalf("readTail(missing) = %v, want nil", got)
	}
	// Empty file → nil.
	empty := dir + "/empty.md"
	if err := os.WriteFile(empty, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readTail(empty, 5); got != nil {
		t.Fatalf("readTail(empty) = %v, want nil", got)
	}
}

func TestOverviewBarZeroTotal(t *testing.T) {
	// Must not divide by zero, and the bar keeps its width with nothing done.
	got := xansi.Strip(renderOverviewBar(discover.Progress{}))
	if !strings.Contains(got, "0/0") {
		t.Fatalf("zero-total bar = %q, want 0/0", got)
	}
	if w := lipgloss.Width(got); w != overviewBarWidth+len(" 0/0") {
		t.Fatalf("zero-total bar is %d cells, want the %d-cell bar plus its label", w, overviewBarWidth)
	}
	failed := xansi.Strip(renderOverviewBar(discover.Progress{Total: 4, Complete: 1, Failed: 1}))
	if !strings.Contains(failed, "1/4 (1 failed)") {
		t.Fatalf("bar with a failure = %q, want the failed count in its label", failed)
	}
}

// --- sidebar layout (cramped-dashboard fix) ---

// maxLineWidth returns the widest ANSI-aware display width across s's lines.
func maxLineWidth(s string) int {
	max := 0
	for _, line := range strings.Split(s, "\n") {
		if w := lipgloss.Width(line); w > max {
			max = w
		}
	}
	return max
}

// TestDashboardSidebarNeverWraps is the literal contract from the fix brief:
// a session list containing a 20+ char session name, rendered via the
// sidebar (renderList) at the sidebar width the dashboard derives for
// terminal widths 40/60/100, must never produce a line that exceeds that
// sidebar width — the long name/branch must be ellipsized, not wrapped onto
// a stray line outside the row's styling. Scoped to the sidebar box itself
// rather than the whole View(): the header/footer chrome has its own,
// unrelated pre-existing overflow at narrow widths, out of scope for this
// fix (which is specifically about the cramped session list).
func TestDashboardSidebarNeverWraps(t *testing.T) {
	longName := "session-a-stacked-with-a-very-long-branch-name"
	a := sess("session-a")    // branch == name: deduped from the row
	stacked := sess(longName) // 48 chars: must ellipsize, never wrap
	b := sess("session-b")

	for _, termWidth := range []int{40, 60, 100} {
		t.Run(fmt.Sprintf("width=%d", termWidth), func(t *testing.T) {
			m := testModel(a, stacked, b)
			listOuter := sidebarOuterWidth(termWidth)

			out := m.renderList(listOuter, 20)
			for i, line := range strings.Split(out, "\n") {
				if w := lipgloss.Width(line); w > listOuter {
					t.Fatalf("line %d exceeds sidebar width %d (got %d): %q", i, listOuter, w, line)
				}
			}
			if strings.Contains(out, longName) {
				t.Errorf("long name %q rendered in full at sidebar width %d; want it ellipsis-truncated", longName, listOuter)
			}
			if !strings.Contains(out, "…") {
				t.Errorf("expected an ellipsis-truncated line at sidebar width %d, found none", listOuter)
			}
		})
	}
}

// TestRenderRowNeverExceedsWidth exercises renderRow directly (both selected
// and unselected, which take different code paths — only the selected row is
// wrapped in a fixed-width background style) across a battery of widths and
// session shapes: a long name, a differing branch, a deduped (name==branch)
// session, an awaiting-input badge, and a stale session. No line of any
// combination may exceed its row's width.
func TestRenderRowNeverExceedsWidth(t *testing.T) {
	longName := "session-a-stacked-with-a-very-long-branch-name-indeed"

	differingBranch := sess(longName)
	differingBranch.Branch = "feature/" + longName + "-and-then-some-more-branch-text"

	awaiting := sess(longName)
	awaiting.AwaitingInput = true

	stale := Session{SessionState: discover.SessionState{
		Name: longName, StatusStale: true, StaleReason: state.StaleUnknownField,
	}}

	cases := []struct {
		name string
		s    Session
	}{
		{"deduped branch==name", sess(longName)},
		{"differing branch", differingBranch},
		{"awaiting input", awaiting},
		{"stale", stale},
	}

	m := testModel()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, width := range []int{8, 16, 20, 24, 40, 60, 100} {
				for _, selected := range []bool{false, true} {
					row := m.renderRow(tc.s, selected, width)
					if w := maxLineWidth(row); w > width {
						t.Errorf("width=%d selected=%v: row line exceeds width (got %d): %q",
							width, selected, w, row)
					}
				}
			}
		})
	}
}

// TestRenderRowShowsHostLiveness pins the running/stopped indicator (plan
// Objective 2): every row carries exactly one liveness dot, filled for a live
// host and hollow for a stopped one — including a STALE session, whose row
// otherwise shows no phase badge worth trusting, and a stopped session whose
// status.json still claims "executing" (the misleading-badge case the dot
// exists to disambiguate). Pending placeholders have no host yet and always
// render the hollow form.
func TestRenderRowShowsHostLiveness(t *testing.T) {
	staleRunning := Session{SessionState: discover.SessionState{
		Name: "alpha", StatusStale: true, StaleReason: state.StaleUnknownField,
		HostRunning: true, HostPID: 4242,
	}}
	pending := Session{SessionState: discover.SessionState{Name: "alpha"}, Pending: true}

	tests := []struct {
		name         string
		s            Session
		want, banned string
	}{
		{"running host shows the filled dot", runningSess("alpha"), hostRunningGlyph, hostStoppedGlyph},
		{"stopped host with a stale executing badge shows the hollow dot", sess("alpha"), hostStoppedGlyph, hostRunningGlyph},
		{"stale status with a live host still shows the filled dot", staleRunning, hostRunningGlyph, hostStoppedGlyph},
		{"pending placeholder shows the hollow dot", pending, hostStoppedGlyph, hostRunningGlyph},
	}
	m := testModel()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, selected := range []bool{false, true} {
				row := m.renderRow(tc.s, selected, 60)
				if !strings.Contains(row, tc.want) {
					t.Errorf("selected=%v: row is missing the %q liveness dot: %q", selected, tc.want, row)
				}
				if strings.Contains(row, tc.banned) {
					t.Errorf("selected=%v: row shows the wrong liveness dot %q: %q", selected, tc.banned, row)
				}
			}
		})
	}
}

// wedgedSess returns a session flagged wedged by the host's liveness monitor
// (internal/host/liveness.go's `wedged_since`, W3.T1), with WedgedSince set
// `age` before the fixed test clock (testDeps' Now returns
// time.Unix(1_000_000, 0)), so the row's rendered age is deterministic.
func wedgedSess(name string, age time.Duration) Session {
	s := runningSess(name)
	s.WedgedSince = time.Unix(1_000_000, 0).Add(-age).UTC().Format(time.RFC3339)
	return s
}

// TestRenderRowShowsWedgedIndicator is W6.T1's acceptance test (plan
// bug-hung-session, W6.T1): a wedged row (WedgedSince set, from W5.T1's
// discovery passthrough) renders a distinct indicator in its flag cell,
// alongside — not in place of — the existing host-liveness dot and phase
// label. The age derived from WedgedSince is the detail pane's to show; a
// one-line table row has no room for it. A healthy row (WedgedSince empty,
// output recent) and an unknown-liveness row (WedgedSince AND LastOutputAt
// both empty — no host.json, or a pre-W1.T2 host that never wrote either
// field) must render exactly as they do today: no fabricated wedge state
// just because liveness is unknown.
func TestRenderRowShowsWedgedIndicator(t *testing.T) {
	wedged := wedgedSess("alpha", 12*time.Minute)

	healthy := runningSess("alpha")
	healthy.LastOutputAt = time.Unix(1_000_000, 0).Add(-3 * time.Second).UTC().Format(time.RFC3339)

	unknown := sess("alpha") // no host.json at all: both fields empty

	tests := []struct {
		name string
		s    Session
		want bool
	}{
		{"wedged row shows the indicator", wedged, true},
		{"healthy row renders unchanged", healthy, false},
		{"unknown-liveness row renders unchanged", unknown, false},
	}

	m := testModel()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, selected := range []bool{false, true} {
				row := m.renderRow(tc.s, selected, 60)
				got := strings.Contains(row, wedgedGlyph)
				if got != tc.want {
					t.Errorf("selected=%v: wedged glyph shown=%v, want %v: %q", selected, got, tc.want, row)
				}
			}
		})
	}
}

// TestRenderRowWedgedNeverExceedsWidth is TestRenderRowNeverExceedsWidth's
// sibling for the wedged badge, kept as its own test rather than folded into
// that table — the task requires existing row-rendering tests to pass
// unmodified. No combination of width and selection may push any line past
// its budget once the wedged badge is appended.
func TestRenderRowWedgedNeverExceedsWidth(t *testing.T) {
	longName := "session-a-stacked-with-a-very-long-branch-name-indeed"
	s := wedgedSess(longName, 90*time.Minute)

	m := testModel()
	for _, width := range []int{8, 16, 20, 24, 40, 60, 100} {
		for _, selected := range []bool{false, true} {
			row := m.renderRow(s, selected, width)
			if w := maxLineWidth(row); w > width {
				t.Errorf("width=%d selected=%v: wedged row line exceeds width (got %d): %q",
					width, selected, w, row)
			}
		}
	}
}

// TestRenderRowDedupesBranchWhenSameAsName covers fix requirement #2: when
// branch == session name, the name appears exactly once on the row. Since
// the branch left the row for the detail pane this holds for any branch, and
// the test stays as the guard that nothing sneaks the name in twice.
func TestRenderRowDedupesBranchWhenSameAsName(t *testing.T) {
	m := testModel()
	s := sess("session-a") // sampleState sets Branch: name
	row := m.renderRow(s, false, 60)
	if got := strings.Count(row, "session-a"); got != 1 {
		t.Fatalf("expected \"session-a\" to appear exactly once (deduped), got %d in: %q", got, row)
	}
}

// TestRenderRowShowsDifferingBranch covers the complement: a branch that
// differs from the name is NOT on the row — the one-line table has no column
// for it — and IS in the detail pane, which is where a reader looks for it.
func TestRenderRowShowsDifferingBranch(t *testing.T) {
	s := sess("session-a")
	s.Branch = "feature/short"
	m := testModel(s)
	row := m.renderRow(s, false, 60)
	if strings.Contains(row, "feature/short") {
		t.Fatalf("the row has no branch column; got: %q", row)
	}
	if detail := xansi.Strip(m.composeDetailBody(80)); !strings.Contains(detail, "feature/short") {
		t.Fatalf("expected the branch in the detail pane, got: %q", detail)
	}
}

// TestPhaseBadgePlainIsNarrower covers fix requirement #4 (addendum): the
// plain badge variant renderRow falls back to at narrow widths must actually
// be narrower than the padded/backgrounded default, or the fallback would be
// pointless.
func TestPhaseBadgePlainIsNarrower(t *testing.T) {
	for _, phase := range []string{"planning", "executing", "reviewing", "fixing", "done", "failed", ""} {
		padded := theme.phaseBadge(phase)
		plain := theme.phaseBadgePlain(phase)
		if pw, sw := lipgloss.Width(padded), lipgloss.Width(plain); sw >= pw {
			t.Errorf("phase %q: plain badge width %d not narrower than padded %d", phase, sw, pw)
		}
	}
}

// TestRowColumnsLadder pins rowColumns' rungs. The load-bearing rows are 41
// and 26 — the content widths a 120- and an 80-column terminal actually give
// the sidebar (sidebarOuterWidth 45 and 30, minus the box's 4 cells) — and
// the boundary pairs (39/40, 31/32, 23/24) that prove each rung starts where
// its doc says. Every row also checks the cells sum to the width, which is
// what lets composeRow skip padding the line as a whole.
func TestRowColumnsLadder(t *testing.T) {
	cases := []struct {
		width int
		want  rowLayout
	}{
		{41, rowLayout{name: 19, phase: 9, tasks: 5}},
		{40, rowLayout{name: 18, phase: 9, tasks: 5}},
		{39, rowLayout{name: 21, phase: 5, tasks: 5, abbrevPhase: true}},
		{32, rowLayout{name: 14, phase: 5, tasks: 5, abbrevPhase: true}},
		{31, rowLayout{name: 19, phase: 5, abbrevPhase: true}},
		{26, rowLayout{name: 14, phase: 5, abbrevPhase: true}},
		{24, rowLayout{name: 12, phase: 5, abbrevPhase: true}},
		{23, rowLayout{name: 17}},
		{10, rowLayout{name: 4}},
		{3, rowLayout{name: 1}}, // floored: the arithmetic goes negative here
	}
	for _, tc := range cases {
		got := rowColumns(tc.width)
		if got != tc.want {
			t.Errorf("rowColumns(%d) = %+v, want %+v", tc.width, got, tc.want)
		}
		sum := rowMarkerCells + rowHostCells + got.name + 1 + rowFlagCells
		if got.phase > 0 {
			sum += got.phase + 1
		}
		if got.tasks > 0 {
			sum += got.tasks + 1
		}
		if tc.width >= 8 && sum != tc.width {
			t.Errorf("rowColumns(%d): cells sum to %d, want the width", tc.width, sum)
		}
	}
}

// TestRenderRowAbbreviatesPhaseWhenNarrow confirms renderRow follows
// rowColumns' ladder: the full phase word at a comfortable width, the
// abbreviation where the PHASE column is five cells, and no phase at all —
// rather than a mangled one — below the rung that has a PHASE column.
func TestRenderRowAbbreviatesPhaseWhenNarrow(t *testing.T) {
	m := testModel()
	s := sess("a-fairly-long-session-name-for-this-narrow-width-test")

	wide := xansi.Strip(m.renderRow(s, false, 60))
	if !strings.Contains(wide, "EXECUTING") {
		t.Errorf("expected the full phase at a comfortable width; row=%q", wide)
	}

	narrow := xansi.Strip(m.renderRow(s, false, 34))
	if !strings.Contains(narrow, "EXEC") || strings.Contains(narrow, "EXECUTING") {
		t.Errorf("expected the abbreviated phase at width 34; row=%q", narrow)
	}

	tiny := xansi.Strip(m.renderRow(s, false, 18))
	if strings.Contains(tiny, "EXEC") {
		t.Errorf("expected no phase column at width 18; row=%q", tiny)
	}
}

// TestSidebarRowsAreContiguousUnderCaptions pins the sidebar body's shape:
// the caption line, the filter prompt's reserved row (blank while unfiltered,
// doubling as the gap under the captions), then one line per session with no
// separators — a table, not a stack of cards.
func TestSidebarRowsAreContiguousUnderCaptions(t *testing.T) {
	m := testModel(sess("session-a"), sess("session-b"))
	// 5 = the caption line + the filter row + a 3-row page (availHeight 3 /
	// (Height 1 + Spacing 0)); two sessions leave one padded blank line.
	body := xansi.Strip(m.renderRowsBody(40, 5))
	lines := strings.Split(body, "\n")
	for i := range lines {
		// lipgloss.JoinVertical right-pads every short line to the block width,
		// so a "blank" line arrives full of spaces.
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	if len(lines) != 5 {
		t.Fatalf("renderRowsBody(2 sessions) = %d lines, want 5 (captions + filter line + a 3-line page): %q", len(lines), body)
	}
	for _, caption := range []string{"NAME", "PHASE", "TASKS"} {
		if !strings.Contains(lines[0], caption) {
			t.Errorf("the first line is the column captions and must name %s, got %q", caption, lines[0])
		}
	}
	if lines[1] != "" {
		t.Errorf("the second line is the reserved filter prompt and must be blank while unfiltered, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "session-a") || !strings.Contains(lines[3], "session-b") {
		t.Errorf("rows out of place: %q", lines)
	}
}

// TestSidebarContentWidthAccountsForPadding covers fix requirement #1
// (addendum): the sidebar keeps 1 cell of horizontal padding on each side
// (theme.listBox.Padding(0, 1)) plus its 2-cell border, so content never
// touches the pane edges.
func TestSidebarContentWidthAccountsForPadding(t *testing.T) {
	const outerW = 40
	got := sidebarContentWidth(outerW)
	want := outerW - 4 // 2 border cells + 2×1 padding cells
	if got != want {
		t.Fatalf("sidebarContentWidth(%d) = %d, want %d", outerW, got, want)
	}
}

// TestSidebarWidthResponsive covers fix requirement #4: the sidebar is
// proportional to terminal width with a floor, and never exceeds the
// terminal width itself.
func TestSidebarWidthResponsive(t *testing.T) {
	tests := []struct {
		termWidth int
		want      int
	}{
		{40, minSidebarWidth}, // 38% of 40 = 15, floored to minSidebarWidth
		{80, minSidebarWidth}, // 38% of 80 = 30, exactly the floor
		{120, 45},             // 38% of 120 = 45, above the floor
		{200, 76},             // 38% of 200 = 76
		{10, 10},              // narrower than the floor: capped to terminal width
	}
	for _, tt := range tests {
		if got := sidebarOuterWidth(tt.termWidth); got != tt.want {
			t.Errorf("sidebarOuterWidth(%d) = %d, want %d", tt.termWidth, got, tt.want)
		}
	}
}

// --- the footer carries no message text ---
//
// These three used to pin the footer's own render-time truncation of a long
// status and a long loadErr. That branch is gone with the status row: the
// coverage is re-scoped to the guarantee that replaced it — an arbitrarily
// long message never reaches the footer at all, and never widens the frame,
// because the toast is now the only surface it renders on and the toast wraps.

// TestLongStatusNeverReachesTheFooter is the status half. A message long
// enough to have needed truncation is set, and the footer is asserted free of
// it and inside the model's width.
//
// The frame as a whole is deliberately not measured here. A toast composed
// over a 30-column frame already overhangs it — composeToast's box is sized
// from toastMaxWidth, not from the terminal — which is pre-existing and
// belongs to the toast, not to the footer this test is about.
func TestLongStatusNeverReachesTheFooter(t *testing.T) {
	m := testModel(sess("a"))
	m.width = 30
	m.notify(notifyError, "afk clean failed: exit status 1 · "+strings.Repeat("x", 200))

	footer := xansi.Strip(m.renderFooter())
	if strings.Contains(footer, "afk clean failed") || strings.Contains(footer, "xxxxx") {
		t.Fatalf("footer = %q, must not carry the notification text", footer)
	}
	for _, ln := range strings.Split(footer, "\n") {
		if w := lipgloss.Width(strings.TrimRight(ln, " ")); w > m.width {
			t.Fatalf("footer line exceeds width %d (got %d): %q", m.width, w, ln)
		}
	}
}

// TestLongLoadErrNotifiesInsteadOfWideningTheFooter is the discovery-error
// half. The error used to be a footer branch of its own; it is an error-kind
// notification now, so the assertion is that it reaches the status slot and
// stays off the footer.
func TestLongLoadErrNotifiesInsteadOfWideningTheFooter(t *testing.T) {
	m := testModel(sess("a"))
	m.width = 25
	m, _ = update(t, m, sessionsLoadedMsg{
		gen: m.projectGen,
		err: errors.New("git worktree list failed: " + strings.Repeat("y", 200)),
		at:  time.Unix(9, 0),
	})

	if !strings.Contains(m.status, "git worktree list failed") {
		t.Fatalf("status = %q, want the discovery error raised as a notification", m.status)
	}
	if m.statusKind != notifyError {
		t.Fatalf("statusKind = %v, want notifyError for a discovery failure", m.statusKind)
	}
	footer := xansi.Strip(m.renderFooter())
	if strings.Contains(footer, "git worktree list failed") || strings.Contains(footer, "yyyyy") {
		t.Fatalf("footer = %q, must not carry the discovery error", footer)
	}
	for _, ln := range strings.Split(footer, "\n") {
		if w := lipgloss.Width(strings.TrimRight(ln, " ")); w > m.width {
			t.Fatalf("footer line exceeds width %d (got %d): %q", m.width, w, ln)
		}
	}
}

// TestRenderFooterWidthZeroPinned deliberately pins the width-0 edge case:
// truncate(s, 0) yields "" (see truncate's non-positive-max branch), so a
// zero-width model's footer renders as no cells at all. This test calls
// renderFooter directly rather than View(), because View() early-returns
// "Loading afk dashboard…" before width 0 is ever reached. The ANSI strip is
// what makes it a claim about cells: theme.helpLine still wraps the empty
// string in its escape pair.
func TestRenderFooterWidthZeroPinned(t *testing.T) {
	m := testModel(sess("a"))
	m.width = 0
	m.notify(notifyInfo, "some status that would otherwise render")

	footer := strings.TrimRight(xansi.Strip(m.renderFooter()), " ")
	if footer != "" {
		t.Fatalf("footer at width 0 = %q, want empty (truncate(s, 0) == \"\")", footer)
	}
}

func TestDurationShort(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "0s"},
		{2 * time.Second, "2s"},
		{90 * time.Second, "1m"},
		{2 * time.Hour, "2h"},
	}
	for _, tt := range tests {
		if got := durationShort(tt.d); got != tt.want {
			t.Errorf("durationShort(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestFocusBorderTracksInputOwner(t *testing.T) {
	accent := theme.paneBoxFocused.GetBorderTopForeground()

	m := testModel(sess("a"))
	if m.listBoxStyle().GetBorderTopForeground() != accent {
		t.Error("no pane: sidebar should carry the accent border")
	}

	fp := newFakePane("X")
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	if m.paneBoxStyle().GetBorderTopForeground() == accent {
		t.Error("unfocused pane must not carry the accent border")
	}
	if m.listBoxStyle().GetBorderTopForeground() != accent {
		t.Error("pane open but unfocused: sidebar keeps the accent")
	}

	m, _ = update(t, m, keyEnter)
	if m.paneBoxStyle().GetBorderTopForeground() != accent {
		t.Error("focused pane should carry the accent border")
	}
	if m.listBoxStyle().GetBorderTopForeground() == accent {
		t.Error("focused pane: sidebar must drop the accent")
	}

	m, _ = pressAction(t, m, keyEscMsg())
	if m.paneBoxStyle().GetBorderTopForeground() == accent {
		t.Error("detach: pane must drop the accent border")
	}
	if m.listBoxStyle().GetBorderTopForeground() != accent {
		t.Error("detach: sidebar should regain the accent border")
	}
}

func TestFormActiveDropsSidebarAccent(t *testing.T) {
	accent := theme.listBoxFocused.GetBorderTopForeground()
	m := testModel(sess("a"))
	m, _ = pressAction(t, m, keyRune('n'))
	if !m.formOpen {
		t.Fatalf("precondition: n should open the creation form")
	}
	if m.listBoxStyle().GetBorderTopForeground() == accent {
		t.Error("open form owns input: sidebar must drop the accent border")
	}
}

func TestRenderRowAwaitingInputBadge(t *testing.T) {
	withPhase := func(name string, p state.Phase, awaiting, hostUp bool) Session {
		st := sampleState(name)
		st.Phase = p
		st.AwaitingInput = awaiting
		st.HostRunning = hostUp
		if hostUp {
			st.HostPID = 4242
		}
		return Session{SessionState: st}
	}

	tests := []struct {
		name string
		s    Session
		want bool
	}{
		{"executing with a live host shows the badge", withPhase("a", state.PhaseExecuting, true, true), true},
		{"executing with no host still shows the badge", withPhase("a", state.PhaseExecuting, true, false), true},
		{"flag unset shows no badge", withPhase("a", state.PhaseExecuting, false, true), false},
		{"planning shows the badge", withPhase("a", state.PhasePlanning, true, true), true},
		{"reviewing shows the badge", withPhase("a", state.PhaseReviewing, true, true), true},
		{"fixing shows the badge", withPhase("a", state.PhaseFixing, true, true), true},
		{"done shows no badge", withPhase("a", state.PhaseDone, true, true), false},
		{"failed shows no badge", withPhase("a", state.PhaseFailed, true, true), false},
	}

	// The flag is asserted in its STYLED form on an unselected row: the
	// awaiting dot is the same glyph an ORPHAN row's state wears, and only the
	// colour (theme.awaitingFlag) and the column tell them apart. On the
	// selected row every word is sel-fg (§3.5), so there it is the flag
	// column's glyph (rowFlagShown).
	flag := theme.awaitingFlag.Render(awaitingGlyph)
	m := testModel()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, selected := range []bool{false, true} {
				row := m.renderRow(tc.s, selected, 60)
				if got := rowFlagShown(t, row, flag, awaitingGlyph, selected); got != tc.want {
					t.Errorf("selected=%v: awaiting flag shown=%v, want %v: %q", selected, got, tc.want, row)
				}
			}
		})
	}
}

// rowFlagShown reports whether a session row shows the flag whose styled
// form is styled and whose glyph is glyph. An unselected row must show the
// styled form, the flag's own colour. A selected row is redrawn in sel-fg on
// sel (the row part, §3.5), so there the flag is its glyph in the flag column,
// the row's last cell, and the flag's own colour must be gone: a selected row
// that kept it would be a word on the selection not in sel-fg.
func rowFlagShown(t *testing.T, row, styled, glyph string, selected bool) bool {
	t.Helper()
	if !selected {
		return strings.Contains(row, styled)
	}
	if strings.Contains(row, styled) {
		t.Errorf("selected row kept the flag's own colour instead of sel-fg: %q", row)
	}
	return strings.HasSuffix(strings.TrimRight(xansi.Strip(row), " "), glyph)
}

func TestFormBaseFilterAcceptsInput(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main", "feature-alpha", "feature-beta", "bugfix-123"}
	m, _ = pressAction(t, m, keyRune('n'))

	totalItems := len(m.form.baseList.Items())
	if totalItems != 4 {
		t.Fatalf("base list has %d items, want 4", totalItems)
	}

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.form.focusIndex != 2 {
		t.Fatalf("tab tab should focus base, got focusIndex=%d", m.form.focusIndex)
	}

	m, _ = update(t, m, keyRune('/'))
	if m.form.baseList.FilterState() != list.Filtering {
		t.Fatalf("/ should activate filtering, got FilterState=%v", m.form.baseList.FilterState())
	}

	var cmd tea.Cmd
	for _, r := range "fea" {
		m, cmd = update(t, m, keyRune(r))
		m = pumpForm(t, m, cmd, 0)
	}

	filterVal := m.form.baseList.FilterInput.Value()
	if filterVal != "fea" {
		t.Fatalf("FilterInput.Value() = %q, want 'fea' - filter input not routing properly", filterVal)
	}

	if m.form.baseList.FilterState() != list.Filtering {
		t.Fatalf("should still be in Filtering state after typing, got %v", m.form.baseList.FilterState())
	}

	// The load-bearing assertion: the list must actually narrow, not merely
	// accept keystrokes. bubbles/list only populates filteredItems when a
	// FilterMatchesMsg reaches Update (list.go's FilterMatchesMsg case), and
	// that message arrives inside a tea.BatchMsg returned as a Cmd. Discarding
	// those Cmds leaves VisibleItems() at full length while FilterInput and
	// FilterState both look correct — a filter that reads as working and
	// filters nothing. pumpForm is what bubbletea's event loop does for real.
	if got := len(m.form.baseList.VisibleItems()); got != 2 {
		t.Errorf("VisibleItems() = %d after filtering %q, want 2 (feature-alpha, feature-beta)", got, filterVal)
	}
}

// TestBasePickerArrowsWrapAtTheEnds documents the wrap behavior
// InfiniteScrolling is meant to add to the base-branch picker: bubbles' list
// clamps by default; these lists set InfiniteScrolling so CursorUp/CursorDown
// wrap (bubbles v2.2.1 list/list.go:516-565). The load-bearing assertion is
// the first one — up from the first branch must land on the LAST branch
// rather than staying put; the second (down from the last branch returns to
// the first) is the return trip and would pass vacuously on its own if the
// list never moved.
func TestBasePickerArrowsWrapAtTheEnds(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"feature-alpha", "feature-beta", "main"}
	m.plainTrunk = "main"
	m, _ = pressAction(t, m, keyRune('n'))

	items := m.form.baseList.Items()
	if len(items) != 3 {
		t.Fatalf("base list has %d items, want 3", len(items))
	}
	last, ok := items[len(items)-1].(baseItem)
	if !ok {
		t.Fatal("the base list's last item is not a baseItem")
	}

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.form.focusIndex != 2 {
		t.Fatalf("tab tab should focus the base selector, got focusIndex=%d", m.form.focusIndex)
	}

	m.form.baseList.Select(0)
	first, ok := m.form.baseList.SelectedItem().(baseItem)
	if !ok {
		t.Fatal("the base list has no selection after Select(0)")
	}

	// Load-bearing: up from the first branch must wrap to the last, not clamp.
	m, _ = update(t, m, keyUp)
	if got, ok := m.form.baseList.SelectedItem().(baseItem); !ok || got != last {
		gotName := ""
		if ok {
			gotName = string(got)
		}
		t.Fatalf("up from the first branch (%q) landed on %q, want it to wrap to the last branch (%q)", first, gotName, last)
	}

	m, _ = update(t, m, keyDown)
	if got, ok := m.form.baseList.SelectedItem().(baseItem); !ok || got != first {
		gotName := ""
		if ok {
			gotName = string(got)
		}
		t.Fatalf("down from the last branch landed on %q, want it to wrap back to the first branch (%q)", gotName, first)
	}
}

// pumpForm feeds a Cmd's messages back into the Model the way bubbletea's
// event loop does, unwrapping tea.BatchMsg. Tests that call Update directly and
// drop the returned Cmd cannot observe any bubbles behavior that completes
// asynchronously — list filtering above being the case that matters here.
func pumpForm(t *testing.T, m Model, c tea.Cmd, depth int) Model {
	t.Helper()
	if c == nil || depth > 5 {
		return m
	}
	msg, ok := runCmdFast(c)
	if !ok {
		return m
	}
	if _, isBlink := msg.(cursor.BlinkMsg); isBlink {
		return m
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, sub := range msg {
			m = pumpForm(t, m, sub, depth+1)
		}
	case nil:
	default:
		next, cmd := update(t, m, msg)
		m = pumpForm(t, next, cmd, depth+1)
	}
	return m
}

// runCmdFast evaluates c and abandons it if it does not resolve promptly,
// reporting ok=false. The Cmds that matter to a form test — list filtering's
// FilterMatchesMsg, batches — return immediately; the cursor's blink Cmd sleeps
// on a real ~530ms timer, and pumpForm recurses per keystroke, which is what
// turned this package's suite from sub-second into ~40s. Abandoning a blocked
// Cmd is safe here: a test that needs a timer's message should drive it by
// sending the message directly rather than waiting out the clock.
func runCmdFast(c tea.Cmd) (tea.Msg, bool) {
	done := make(chan tea.Msg, 1)
	go func() { done <- c() }()
	select {
	case msg := <-done:
		return msg, true
	case <-time.After(5 * time.Millisecond):
		return nil, false
	}
}

func TestFormCancelAndRerenderDoesNotPanic(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main", "develop"}

	m, _ = pressAction(t, m, keyRune('n'))
	if !m.formOpen {
		t.Fatalf("n should open the form")
	}

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.formOpen {
		t.Fatalf("esc should close the form")
	}

	m, _ = pressAction(t, m, keyRune('n'))
	if !m.formOpen {
		t.Fatalf("second n should re-open the form after cancel")
	}

	_ = render(m)
}

// TestFormRoutingTable pins the handleFormKey routing table as documented in
// model.go ~768-779. Load-bearing rows: Enter submitting from name/base while
// inserting a newline in description (splits the form's exit behavior);
// Esc clearing a filter without closing the form (the filter-active branch);
// Tab cycling focus rather than applying an empty filter (the empty-filter
// dead-key trap). The checkbox rows are load-bearing in both directions: space
// toggles ONLY on the checkbox (formComponentKey's checkbox arm), while the
// name and description rows prove a space there is still typed as a literal
// space and leaves the checkbox alone; enter on the checkbox submits
// (submitFormKey's non-description arm) rather than toggling. The provider
// rows mirror the checkbox's: space/→/← cycle ONLY on the selector, the cycle
// to codex ticks Autonomous, a modified or unrelated key is inert, and enter
// submits rather than cycling.
func TestFormRoutingTable(t *testing.T) {
	space := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	tests := []struct {
		name           string
		setupFocus     int
		filtering      bool
		filterValue    string
		key            tea.KeyMsg
		wantSubmit     bool
		wantFormActive bool
		wantFilterClr  bool
		wantDescValue  string
		wantNameValue  string
		wantAutonomous bool
		// wantProvider is checked only while the form stays open; "" means
		// the default, which every row that never touches the selector keeps.
		wantProvider string
	}{
		{
			name:           "enter from name submits",
			setupFocus:     0,
			key:            keyEnter,
			wantSubmit:     true,
			wantFormActive: false,
		},
		{
			name:           "enter from description inserts newline and leaves form open",
			setupFocus:     1,
			key:            keyEnter,
			wantFormActive: true,
			wantDescValue:  "\n",
		},
		{
			name:           "enter from base submits",
			setupFocus:     2,
			key:            keyEnter,
			wantSubmit:     true,
			wantFormActive: false,
		},
		{
			name:           "esc with active filter clears filter and leaves form open",
			setupFocus:     2,
			filtering:      true,
			filterValue:    "feat",
			key:            tea.KeyPressMsg{Code: tea.KeyEsc},
			wantSubmit:     false,
			wantFormActive: true,
			wantFilterClr:  true,
		},
		{
			name:           "esc with no filter cancels form",
			setupFocus:     0,
			filtering:      false,
			key:            tea.KeyPressMsg{Code: tea.KeyEsc},
			wantSubmit:     false,
			wantFormActive: false,
		},
		{
			name:           "tab with empty filter clears filter and cycles focus",
			setupFocus:     2,
			filtering:      true,
			filterValue:    "",
			key:            tea.KeyPressMsg{Code: tea.KeyTab},
			wantSubmit:     false,
			wantFormActive: true,
			wantFilterClr:  true,
		},
		{
			name:           "space on the checkbox toggles it on and leaves the form open",
			setupFocus:     formFocusAutonomous,
			key:            space,
			wantFormActive: true,
			wantAutonomous: true,
		},
		{
			name:           "enter on the checkbox submits",
			setupFocus:     formFocusAutonomous,
			key:            keyEnter,
			wantSubmit:     true,
			wantFormActive: false,
		},
		{
			name:           "space in the name types a literal space and does not toggle",
			setupFocus:     formFocusName,
			key:            space,
			wantFormActive: true,
			wantNameValue:  "test-session ",
		},
		{
			name:           "space in the description types a literal space and does not toggle",
			setupFocus:     formFocusDescription,
			key:            space,
			wantFormActive: true,
			wantDescValue:  " ",
		},
		{
			name:           "a letter on the checkbox is inert",
			setupFocus:     formFocusAutonomous,
			key:            keyRune('x'),
			wantFormActive: true,
		},
		{
			name:           "ctrl+space on the checkbox is inert",
			setupFocus:     formFocusAutonomous,
			key:            tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl},
			wantFormActive: true,
		},
		{
			name:           "space on the base list does not toggle",
			setupFocus:     formFocusBase,
			key:            space,
			wantFormActive: true,
		},
		{
			name:           "space on the provider selector cycles to codex and ticks autonomous",
			setupFocus:     formFocusProvider,
			key:            space,
			wantFormActive: true,
			wantAutonomous: true,
			wantProvider:   provider.CodexName,
		},
		{
			name:           "right on the provider selector cycles to codex",
			setupFocus:     formFocusProvider,
			key:            tea.KeyPressMsg{Code: tea.KeyRight},
			wantFormActive: true,
			wantAutonomous: true,
			wantProvider:   provider.CodexName,
		},
		{
			name:           "left on the provider selector cycles back, wrapping to opencode",
			setupFocus:     formFocusProvider,
			key:            tea.KeyPressMsg{Code: tea.KeyLeft},
			wantFormActive: true,
			wantProvider:   provider.OpenCodeName,
		},
		{
			name:           "ctrl+space on the provider selector is inert",
			setupFocus:     formFocusProvider,
			key:            tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl},
			wantFormActive: true,
		},
		{
			name:           "shift+right on the provider selector is inert",
			setupFocus:     formFocusProvider,
			key:            tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift},
			wantFormActive: true,
		},
		{
			name:           "a letter on the provider selector is inert",
			setupFocus:     formFocusProvider,
			key:            keyRune('c'),
			wantFormActive: true,
		},
		{
			name:           "enter on the provider selector submits",
			setupFocus:     formFocusProvider,
			key:            keyEnter,
			wantSubmit:     true,
			wantFormActive: false,
		},
		{
			name:           "right in the name moves the cursor and does not cycle",
			setupFocus:     formFocusName,
			key:            tea.KeyPressMsg{Code: tea.KeyRight},
			wantFormActive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("a"))
			m.plainBranches = []string{"main", "feature-alpha", "feature-beta"}
			m.plainTrunk = "main"
			// The base list is sized on open and on resize (openNewForm /
			// sizeFormComponents), so a form opened at zero width leaves it 0x0 —
			// and a zero-height list refuses to enter filtering at all.
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			m, _ = pressAction(t, m, keyRune('n'))
			m = typeRunes(t, m, "test-session")

			for i := 0; i < tt.setupFocus; i++ {
				m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			}
			if m.form.focusIndex != tt.setupFocus {
				t.Fatalf("setup: focusIndex = %d, want %d", m.form.focusIndex, tt.setupFocus)
			}

			if tt.filtering {
				m, _ = update(t, m, keyRune('/'))
				if tt.filterValue != "" {
					for _, r := range tt.filterValue {
						var cmd tea.Cmd
						m, cmd = update(t, m, keyRune(r))
						m = pumpForm(t, m, cmd, 0)
					}
				}
				if m.form.baseList.FilterState() != list.Filtering {
					t.Fatalf("setup: FilterState = %v, want Filtering", m.form.baseList.FilterState())
				}
			}

			got, cmd := update(t, m, tt.key)

			if tt.wantSubmit {
				if cmd == nil {
					t.Fatalf("expected a submit cmd")
				}
			}

			if got.formOpen != tt.wantFormActive {
				t.Fatalf("formOpen = %v, want %v", got.formOpen, tt.wantFormActive)
			}

			if tt.wantFilterClr {
				if got.form.baseList.FilterState() == list.Filtering {
					t.Fatalf("filter should be cleared, still Filtering")
				}
				if got.form.baseList.FilterInput.Value() != "" {
					t.Fatalf("FilterInput.Value() = %q, want empty after clear", got.form.baseList.FilterInput.Value())
				}
			}

			if tt.wantDescValue != "" {
				if got.form.description.Value() != tt.wantDescValue {
					t.Fatalf("description.Value() = %q, want %q", got.form.description.Value(), tt.wantDescValue)
				}
			}

			if tt.wantNameValue != "" {
				if got.form.nameInput.Value() != tt.wantNameValue {
					t.Fatalf("nameInput.Value() = %q, want %q", got.form.nameInput.Value(), tt.wantNameValue)
				}
			}

			// Only meaningful while the form is still open: a submit resets
			// m.form to a fresh (unchecked) form.
			if tt.wantFormActive && got.form.autonomous != tt.wantAutonomous {
				t.Fatalf("autonomous = %v, want %v", got.form.autonomous, tt.wantAutonomous)
			}
			if tt.wantFormActive {
				want := tt.wantProvider
				if want == "" {
					want = provider.Default
				}
				if gotP := got.form.providerName(); gotP != want {
					t.Fatalf("provider = %q, want %q", gotP, want)
				}
			}
		})
	}
}

// TestFormFocusCycle pins the Tab/Shift+Tab focus cycle through the five-field
// form. Load-bearing: the wrap at both ends (name -> provider on Shift+Tab,
// provider -> name on Tab), and autonomous -> provider, the step a four-field
// modulus would turn into a wrap.
func TestFormFocusCycle(t *testing.T) {
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	shiftTab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	tests := []struct {
		name       string
		startFocus int
		key        tea.KeyMsg
		wantFocus  int
	}{
		{"tab from name to description", formFocusName, tab, formFocusDescription},
		{"tab from description to base", formFocusDescription, tab, formFocusBase},
		{"tab from base to autonomous", formFocusBase, tab, formFocusAutonomous},
		{"tab from autonomous to provider", formFocusAutonomous, tab, formFocusProvider},
		{"tab from provider wraps to name", formFocusProvider, tab, formFocusName},
		{"shift-tab from provider to autonomous", formFocusProvider, shiftTab, formFocusAutonomous},
		{"shift-tab from autonomous to base", formFocusAutonomous, shiftTab, formFocusBase},
		{"shift-tab from base to description", formFocusBase, shiftTab, formFocusDescription},
		{"shift-tab from description to name", formFocusDescription, shiftTab, formFocusName},
		{"shift-tab from name wraps to provider", formFocusName, shiftTab, formFocusProvider},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("a"))
			m.plainBranches = []string{"main"}
			m, _ = pressAction(t, m, keyRune('n'))

			for i := 0; i < tt.startFocus; i++ {
				m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			}
			if m.form.focusIndex != tt.startFocus {
				t.Fatalf("setup: focusIndex = %d, want %d", m.form.focusIndex, tt.startFocus)
			}

			got, _ := update(t, m, tt.key)
			if got.form.focusIndex != tt.wantFocus {
				t.Fatalf("focusIndex = %d, want %d", got.form.focusIndex, tt.wantFocus)
			}
		})
	}
}

// TestFormFieldCount pins the five-field contract (name, description, base,
// autonomous, provider). Load-bearing: the tab cycle visits exactly
// formFieldCount indices, and formFieldCount is 5 — so a field added without
// growing the constant, or a cycle that skips one, both fail.
func TestFormFieldCount(t *testing.T) {
	if formFieldCount != 5 {
		t.Fatalf("formFieldCount = %d, want 5", formFieldCount)
	}
	m := testModel(sess("a"))
	m.plainBranches = []string{"main"}
	m, _ = pressAction(t, m, keyRune('n'))

	seen := map[int]bool{0: true}
	for i := 0; i < 10; i++ {
		m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
		seen[m.form.focusIndex] = true
		if m.form.focusIndex == 0 && i > 0 {
			break
		}
	}

	if len(seen) != 5 {
		t.Fatalf("form has %d unique focus indices, want exactly 5 (0-4)", len(seen))
	}
	for _, idx := range []int{formFocusName, formFocusDescription, formFocusBase, formFocusAutonomous, formFocusProvider} {
		if !seen[idx] {
			t.Fatalf("focus index %d never seen during tab cycle", idx)
		}
	}
}

// TestFormAutonomousCheckbox walks the checkbox end to end: it renders
// unchecked inside the modal, space toggles it both ways, the state survives
// focus leaving and returning, and validate hands it to spawnNewArgs. The
// modal rows are load-bearing: they are what proves the modal grew for the
// checkbox row. formChromeRows left at its three-field value sizes the base
// list too tall, so MaxHeight clips the box — the checkbox can still show
// while the bottom border is gone — hence the bottom-border assertion, and the
// error assertion proves the checkbox did not push a validation error out of
// view either. The branch list is long enough to fill the list's height, at
// the golden's 120x40 and at the clamped 80x24 alike.
func TestFormAutonomousCheckbox(t *testing.T) {
	space := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	branches := []string{"main"}
	for i := 1; i <= 12; i++ {
		branches = append(branches, fmt.Sprintf("feature-%02d", i))
	}
	modalLines := func(m Model) []string {
		return strings.Split(xansi.Strip(m.renderNewFormModal()), "\n")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 80, Height: 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m := testModel(sess("a"))
			m.plainBranches = branches
			m.plainTrunk = "main"
			m, _ = update(t, m, size)
			m, _ = pressAction(t, m, keyRune('n'))

			// An invalid name first: the error line must survive alongside
			// the checkbox row.
			m = typeRunes(t, m, "Bad_Name")
			m, _ = update(t, m, keyEnter)
			if m.form.err == "" {
				t.Fatal("setup: an invalid name should have produced an inline error")
			}
			// The error wraps inside the modal and only its first line was
			// ever guaranteed visible (MaxHeight clips the rest), so the
			// assertion is on that first line's opening words.
			if box := strings.Join(modalLines(m), "\n"); !strings.Contains(box, "[ ] Autonomous") || !strings.Contains(box, "invalid session name") {
				t.Fatalf("with an error showing, want both the checkbox row and the error's first line in the modal:\n%s", box)
			}
			for range "Bad_Name" {
				m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
			}
			m = typeRunes(t, m, "auto-check")

			lines := modalLines(m)
			if box := strings.Join(lines, "\n"); !strings.Contains(box, "[ ] Autonomous") {
				t.Fatalf("the unchecked checkbox row is not visible in the modal:\n%s", box)
			}
			if last := lines[len(lines)-1]; !strings.Contains(last, "╰") {
				t.Fatalf("the modal's bottom border was clipped (last line %q):\n%s", last, strings.Join(lines, "\n"))
			}
			if out := render(m); !strings.Contains(out, "[ ] Autonomous") {
				t.Fatalf("the checkbox row is not visible in the composed frame:\n%s", out)
			}

			for i := 0; i < formFocusAutonomous; i++ {
				m, _ = update(t, m, tab)
			}
			m, _ = update(t, m, space)
			if !m.form.autonomous {
				t.Fatal("space on the checkbox did not check it")
			}
			if out := render(m); !strings.Contains(out, "[x] Autonomous") {
				t.Fatalf("the checked checkbox row is not visible in the modal:\n%s", out)
			}

			// Leaving and returning must not reset it: the field is form
			// state, not focus state.
			for i := 0; i < formFieldCount; i++ {
				m, _ = update(t, m, tab)
			}
			if m.form.focusIndex != formFocusAutonomous {
				t.Fatalf("setup: a full tab cycle landed on focus %d, want the checkbox", m.form.focusIndex)
			}
			if !m.form.autonomous {
				t.Fatal("a full tab cycle cleared the checkbox")
			}

			name, base, description, autonomous, providerName, err := m.form.validate()
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if !autonomous {
				t.Fatal("validate dropped the checked flag")
			}
			args := spawnNewArgs(name, base, description, autonomous, providerName)
			if len(args) < 2 || args[len(args)-2] != "--autonomous" || args[len(args)-1] != "auto-check" {
				t.Fatalf("argv = %v, want --autonomous immediately before the name", args)
			}

			m, _ = update(t, m, space)
			if m.form.autonomous {
				t.Fatal("a second space did not uncheck the checkbox")
			}
			if _, _, _, autonomous, _, _ := m.form.validate(); autonomous {
				t.Fatal("validate reported an unchecked box as autonomous")
			}
		})
	}
}

// TestFormProviderCodex walks the codex path end to end through the form:
// the selector renders inside the modal without clipping the bottom border,
// cycling to codex ticks Autonomous, unticking it and submitting is refused
// inline with provider.CodexInteractiveConflictMsg (the CLI's own text, not a
// copy) and dispatches nothing, re-ticking clears that error, and the
// validated values reach spawnNewArgs as `--autonomous --provider codex name`.
// Load-bearing: the bottom-border rows (formChromeRows must count the
// selector row, or MaxHeight clips the box) and the nil-Cmd refusal (a codex
// create without --autonomous must never shell out to an exit-2 failure).
func TestFormProviderCodex(t *testing.T) {
	space := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	shiftTab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	branches := []string{"main"}
	for i := 1; i <= 12; i++ {
		branches = append(branches, fmt.Sprintf("feature-%02d", i))
	}
	modalLines := func(m Model) []string {
		return strings.Split(xansi.Strip(m.renderNewFormModal()), "\n")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 80, Height: 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m := testModel(sess("a"))
			m.plainBranches = branches
			m.plainTrunk = "main"
			m, _ = update(t, m, size)
			m, _ = pressAction(t, m, keyRune('n'))
			m = typeRunes(t, m, "codex-run")

			lines := modalLines(m)
			if box := strings.Join(lines, "\n"); !strings.Contains(box, "Provider: ‹ claude ›") {
				t.Fatalf("the default provider row is not visible in the modal:\n%s", box)
			}
			if last := lines[len(lines)-1]; !strings.Contains(last, "╰") {
				t.Fatalf("the modal's bottom border was clipped (last line %q):\n%s", last, strings.Join(lines, "\n"))
			}

			for i := 0; i < formFocusProvider; i++ {
				m, _ = update(t, m, tab)
			}
			m, _ = update(t, m, space)
			if got := m.form.providerName(); got != provider.CodexName {
				t.Fatalf("provider = %q after one cycle, want codex", got)
			}
			if !m.form.autonomous {
				t.Fatal("choosing codex did not tick Autonomous")
			}
			lines = modalLines(m)
			box := strings.Join(lines, "\n")
			if !strings.Contains(box, "Provider: ‹ codex ›") || !strings.Contains(box, "[x] Autonomous") {
				t.Fatalf("want the codex selector and a ticked checkbox in the modal:\n%s", box)
			}
			if last := lines[len(lines)-1]; !strings.Contains(last, "╰") {
				t.Fatalf("the modal's bottom border was clipped with codex selected (last line %q):\n%s", last, box)
			}

			// Untick Autonomous and submit: refused inline, nothing dispatched.
			m, _ = update(t, m, shiftTab)
			m, _ = update(t, m, space)
			if m.form.autonomous {
				t.Fatal("setup: space did not untick Autonomous")
			}
			m, cmd := update(t, m, keyEnter)
			if cmd != nil {
				t.Fatal("codex without Autonomous dispatched a Cmd; want an inline refusal and nothing async")
			}
			if !m.formOpen {
				t.Fatal("codex without Autonomous closed the form; want it open with an inline error")
			}
			if m.form.err != provider.CodexInteractiveConflictMsg {
				t.Fatalf("form err = %q, want provider.CodexInteractiveConflictMsg %q", m.form.err, provider.CodexInteractiveConflictMsg)
			}
			if box := strings.Join(modalLines(m), "\n"); !strings.Contains(box, "--provider codex requires") {
				t.Fatalf("the refusal's first line is not visible in the modal:\n%s", box)
			}

			// Re-ticking is the fix, so it clears the stale error.
			m, _ = update(t, m, space)
			if m.form.err != "" {
				t.Fatalf("re-ticking Autonomous left the error %q on screen", m.form.err)
			}

			name, base, description, autonomous, providerName, err := m.form.validate()
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			args := spawnNewArgs(name, base, description, autonomous, providerName)
			want := []string{"new", "--no-dash", "--base", "main", "--autonomous", "--provider", "codex", "codex-run"}
			if !slicesEqual(args, want) {
				t.Fatalf("argv = %v, want %v", args, want)
			}

			m, cmd = update(t, m, keyEnter)
			if cmd == nil || m.formOpen {
				t.Fatalf("codex with Autonomous did not submit (cmd nil: %v, formOpen: %v, err %q)", cmd == nil, m.formOpen, m.form.err)
			}
		})
	}
}

// TestFormQuitKeysDurability pins the regression test for the list.go:767
// revert trap: the base list's Quit.Enabled() must still be false AFTER a
// SetHeight/SetItems/Update round-trip. DisableQuitKeybindings() at
// construction (newform.go:79) is the only durability mechanism; setting
// KeyMap.Quit.SetEnabled(false) directly would be silently reverted by
// updateKeybindings (list.go:767), called from ten sites. Load-bearing:
// both Quit (q) and ForceQuit (ctrl+c) stay disabled after a list operation.
func TestFormQuitKeysDurability(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main", "feature"}
	m, _ = pressAction(t, m, keyRune('n'))

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.form.focusIndex != 2 {
		t.Fatalf("setup: focusIndex = %d, want 2 (base)", m.form.focusIndex)
	}

	m.form.baseList.SetHeight(10)
	items := []list.Item{baseItem("main"), baseItem("feature")}
	m.form.baseList.SetItems(items)
	m, _ = update(t, m, keyDown)

	if m.form.baseList.KeyMap.Quit.Enabled() {
		t.Fatalf("KeyMap.Quit.Enabled() = true after SetHeight/SetItems/Update, want false — regression: q would kill the dashboard")
	}
	if m.form.baseList.KeyMap.ForceQuit.Enabled() {
		t.Fatalf("KeyMap.ForceQuit.Enabled() = true after SetHeight/SetItems/Update, want false — regression: ctrl+c would kill the dashboard")
	}

	got, cmd := update(t, m, keyRune('q'))
	if got.quitting {
		t.Fatalf("q in the form should not quit, regression detected")
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatalf("q in the form returned tea.Quit, regression detected")
		}
	}

	got, cmd = update(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if got.quitting {
		t.Fatalf("ctrl+c in the form should not quit, regression detected")
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatalf("ctrl+c in the form returned tea.Quit, regression detected")
		}
	}
}

// TestFormBaseTrunkIsResolvedNotAssumed guards a master-trunk repo. The form
// previously hardcoded "main" for the plain-git path, which invents an option
// that does not exist and preselects it — the entry-point drift the exported
// session seams were added to close. The load-bearing row is the master one:
// asserting only against a main-trunk repo passes against a hardcoded default.
func TestFormBaseTrunkIsResolvedNotAssumed(t *testing.T) {
	tests := []struct {
		name     string
		branches []string
		trunk    string
		wantHead string
	}{
		{"master trunk", []string{"feature-x", "master"}, "master", "master"},
		{"main trunk", []string{"feature-x", "main"}, "main", "main"},
		{"unconventional trunk", []string{"feature-x", "develop"}, "develop", "develop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("a"))
			m.stack = stackState{Detected: false}
			m.plainBranches = tt.branches
			m.plainTrunk = tt.trunk
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			m, _ = pressAction(t, m, keyRune('n'))

			items := m.form.baseList.Items()
			got := make([]string, len(items))
			for i, it := range items {
				got[i] = string(it.(baseItem))
			}
			if len(got) == 0 || got[0] != tt.wantHead {
				t.Fatalf("base options = %v, want %q first", got, tt.wantHead)
			}
			if len(got) != len(tt.branches) {
				t.Errorf("base options = %v, want exactly the %d real branches (a phantom entry means a trunk was invented)", got, len(tt.branches))
			}
			if sel := m.form.baseList.SelectedItem(); sel == nil || string(sel.(baseItem)) != tt.wantHead {
				t.Errorf("selected = %v, want %q", sel, tt.wantHead)
			}
		})
	}
}

// --- auto-preview: scheduling, the three race guards, and render selection ---

// recordingOpen is the OpenHostPane seam as a recorder: it appends every config
// it is asked for and hands back p. Tests assert on the RECORDED configs rather
// than on a bare call count, because "opened the wrong session" and "opened with
// the wrong dial window" are the two failures this feature can produce and a
// counter cannot tell them apart.
func recordingOpen(opened *[]hostPaneConfig, p *fakePane) func(hostPaneConfig) (paneHandle, error) {
	return func(cfg hostPaneConfig) (paneHandle, error) {
		*opened = append(*opened, cfg)
		return p, nil
	}
}

// refusingOpen fails the test if a pane is opened at all. It reports with
// Errorf rather than Fatalf so the caller's own cmd-is-nil assertion still runs
// and names WHICH guard let the open through.
func refusingOpen(t *testing.T, why string) func(hostPaneConfig) (paneHandle, error) {
	t.Helper()
	return func(cfg hostPaneConfig) (paneHandle, error) {
		t.Errorf("%s: OpenHostPane was called for %q", why, cfg.Name)
		return newFakePane("unexpected"), nil
	}
}

// TestPreviewArmsOnCursorMoveAndOpensThatSession is the feature's happy path:
// the cursor settling on a running row opens that row's pane on its own.
//
// The load-bearing assertions are the three the message carries rather than the
// mere fact of an open: the config must name the row the cursor LANDED on (not
// the one it left), Wait must be previewDialWait (a preview must not hold the
// UI for the full five-second explicit-attach window), and focusOnArrival must
// be false — a cursor move must never move the keyboard off the dashboard.
//
// The armed tea.Tick Cmd is deliberately never called. Doing so would block for
// the real previewDelay and put the wall clock back into the test; the timer's
// generation is read off the Model instead (same package), which is exactly what
// the real Cmd would have captured.
func TestPreviewArmsOnCursorMoveAndOpensThatSession(t *testing.T) {
	fp := newFakePane("PREVIEW-OF-B")
	var opened []hostPaneConfig
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = recordingOpen(&opened, fp)

	m, armed := update(t, m, keyDown)
	if armed == nil {
		t.Fatalf("a cursor move onto a running session must arm a preview")
	}
	if m.previewGen != 1 {
		t.Fatalf("previewGen = %d, want 1 after one move", m.previewGen)
	}
	if len(opened) != 0 {
		t.Fatalf("arming must not open anything yet, got %+v", opened)
	}

	m, open := update(t, m, previewTimerMsg{gen: m.previewGen, name: "b"})
	if open == nil {
		t.Fatalf("the armed timer must open the previewed pane")
	}
	msg, ok := open().(paneAttachedMsg)
	if !ok {
		t.Fatalf("preview open produced %T, want paneAttachedMsg", open())
	}
	if len(opened) != 1 || opened[0].Name != "b" || opened[0].Worktree != "/wt/b" {
		t.Fatalf("opened = %+v, want exactly one open of b at /wt/b", opened)
	}
	if opened[0].Wait != previewDialWait {
		t.Fatalf("dial wait = %v, want previewDialWait (%v)", opened[0].Wait, previewDialWait)
	}
	if !msg.preview {
		t.Fatalf("a preview-initiated attach must be marked preview on the message")
	}
	if msg.focusOnArrival {
		t.Fatalf("a preview must not carry focus — a cursor move cannot move the keyboard")
	}

	m, _ = update(t, m, msg)
	if m.pane == nil || m.paneName != "b" {
		t.Fatalf("pane state after the preview landed: pane=%v name=%q", m.pane != nil, m.paneName)
	}
	if m.paneOwnsKeyboard() {
		t.Fatalf("a previewed pane must arrive with the keyboard still on the dashboard")
	}
}

// TestPreviewTimerFromASupersededGenerationIsInert covers the generation check in
// isolation. tea.Tick Cmds cannot be cancelled, so scrolling N rows arms N timers
// and all N fire; previewGen is the only thing that makes the superseded ones
// inert.
//
// BOTH rows are load-bearing because both are constructed so the NAME check
// cannot be what drops the timer — the fixture verifies that before delivering.
// The obvious two-move fixture (down, up) does NOT work: schedulePreview arms for
// the row the cursor lands ON, so from row 0 it arms first for "b" then for "a",
// and the gen-1 timer's name ("b") already disagrees with the selection ("a").
// Such a test would pass with the generation check deleted.
//
//   - "three moves" returns the cursor to the row the stale timer was armed for.
//   - "two rows sharing a name" makes the name check structurally incapable of
//     firing (the trick TestMoveCursorClamping already uses).
func TestPreviewTimerFromASupersededGenerationIsInert(t *testing.T) {
	tests := []struct {
		name     string
		sessions []Session
		keys     []tea.KeyMsg
		stale    previewTimerMsg
	}{
		{
			name:     "three moves land back on the row the stale timer was armed for",
			sessions: []Session{runningSess("a"), runningSess("b")},
			keys:     []tea.KeyMsg{keyDown, keyUp, keyDown},
			stale:    previewTimerMsg{gen: 1, name: "b"},
		},
		{
			name:     "two rows sharing a name, so the name check cannot fire",
			sessions: []Session{runningSess("s"), runningSess("s")},
			keys:     []tea.KeyMsg{keyDown, keyUp},
			stale:    previewTimerMsg{gen: 1, name: "s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(tt.sessions...)
			m.deps.OpenHostPane = refusingOpen(t, "a superseded preview timer")
			for _, k := range tt.keys {
				m, _ = update(t, m, k)
			}
			// The fixture's whole point: the generation must be the ONLY
			// disagreement between the stale timer and the live Model.
			if m.previewGen == tt.stale.gen {
				t.Fatalf("fixture broken: previewGen = %d, must differ from the stale timer's gen", m.previewGen)
			}
			s, ok := m.selected()
			if !ok || s.Name != tt.stale.name {
				t.Fatalf("fixture broken: selection = %q ok=%v, must equal the stale timer's name %q", s.Name, ok, tt.stale.name)
			}

			got, cmd := update(t, m, tt.stale)
			if cmd != nil {
				t.Fatalf("a superseded preview timer must return no Cmd")
			}
			if got.pane != nil {
				t.Fatalf("a superseded preview timer must not open a pane")
			}
		})
	}
}

// TestDetailScrollKeysMoveTheViewportOffsetByExpectedAmounts drives the
// focused overview's line and half-page keys (down/up, ctrl+d/ctrl+u) through
// Update against a 100x24 bigPlanSession and checks the offset after each key
// against the row budget the frame actually draws.
//
// Every step is load-bearing on its own, not just the sequence as a whole:
// mode.go's dispatch already proves the right action fires (mode_test.go's
// TestDetailScrollKeysRouteToActions), but W3.T1 found that these bindings
// were fully wired and still completely dead — detailViewport had no content,
// so every scroll clamped straight back to offset 0 regardless of direction.
// A test that only checked "the offset changed" would still pass if, say,
// down and up were swapped, or if ctrl+d's half-page magnitude
// were wrong; checking the exact number after each individual key pins the
// direction (up vs down) and the magnitude (1 line vs Height()/2).
//
// The half-page magnitude is derived from detailRowBudget — the visible row
// count with the readout row already subtracted — and NOT from bodyHeight,
// because the review of this plan's first cut found the Model viewport sized
// to bodyHeight (21 here) while the frame drew 18 rows: ctrl+d moved 10 rows
// against an 18-row page and the last rows of content were unreachable. The
// bodyHeight guard makes the geometry this test runs under explicit (22 at
// 100x24 — header 1 + footer 1), so a change to the frame's chrome shows up
// as a fixture failure here rather than as a mysteriously different half-page.
//
// The final round-trip render comparison is the other half of the "not
// dead" assertion: renderDetail must actually show different content while
// scrolled, and identical content once back at the top — otherwise the
// numeric offset could be moving on a viewport nothing ever reads from.
func TestDetailScrollKeysMoveTheViewportOffsetByExpectedAmounts(t *testing.T) {
	m := testModel(bigPlanSession("alpha"))
	m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focusOverview(t, m)

	_, mainOuter, bodyHeight := m.layoutDims()
	if bodyHeight != 22 {
		t.Fatalf("fixture geometry drifted: bodyHeight = %d at 100x24, want 22", bodyHeight)
	}
	rows, readout := detailRowBudget(bodyHeight, m.detailViewport.TotalLineCount())
	if !readout || rows != bodyHeight-boxBorderCells-paneStripRows-1 {
		t.Fatalf("fixture broken: detailRowBudget = (%d, %v), want (%d, true) for an overflowing body", rows, readout, bodyHeight-boxBorderCells-paneStripRows-1)
	}
	if got := m.detailViewport.Height(); got != rows {
		t.Fatalf("Model viewport Height = %d after WindowSizeMsg, want the frame's %d rows", got, rows)
	}
	half := rows / 2
	renderAt := func(mm Model) string { return mm.renderDetail(mainOuter, bodyHeight) }

	if got := m.detailViewport.YOffset(); got != 0 {
		t.Fatalf("initial YOffset = %d, want 0", got)
	}
	topRender := renderAt(m)

	m, _ = update(t, m, keyDown)
	if got := m.detailViewport.YOffset(); got != 1 {
		t.Fatalf("after down, YOffset = %d, want 1", got)
	}

	m, _ = update(t, m, chord('d')) // ctrl+d
	if got := m.detailViewport.YOffset(); got != 1+half {
		t.Fatalf("after ctrl+d, YOffset = %d, want 1+%d/2 = %d", got, rows, 1+half)
	}
	midRender := renderAt(m)
	if midRender == topRender {
		t.Fatalf("renderDetail is identical scrolled (YOffset=%d) and unscrolled; the scroll has no visible effect", 1+half)
	}

	m, _ = update(t, m, keyUp)
	if got := m.detailViewport.YOffset(); got != half {
		t.Fatalf("after up, YOffset = %d, want %d", got, half)
	}

	m, _ = update(t, m, chord('u')) // ctrl+u
	if got := m.detailViewport.YOffset(); got != 0 {
		t.Fatalf("after ctrl+u, YOffset = %d, want 0 (clamped at the top)", got)
	}
	if got := renderAt(m); got != topRender {
		t.Fatalf("renderDetail at YOffset=0 differs after a scroll round-trip:\nwant:\n%s\ngot:\n%s", topRender, got)
	}
}

// TestDetailScrollReachesTheLastLineOfContent is the programmatic stand-in for
// the plan's manual check (Success Criterion 2: content taller than the pane
// is reachable), and the regression guard for the blocker the review of this
// plan's first cut found: the Model viewport was sized to bodyHeight while the
// frame drew bodyHeight-3 rows, so the keys clamped three rows short, the
// readout stalled at 97% and the body's final line was never drawn at any
// terminal size.
//
// The property is asserted by assertDetailScrollReachesLastLine (below),
// shared across every tab that can overflow rather than forked per tab, per
// W6.T1's note that an earlier draft of this test hard-coded tabWaves and a
// Diff row would otherwise have needed a parallel copy of the whole body.
// The Waves case builds its fixture directly (bigPlanSession, a synchronous
// Model mutation); the Diff case (diffscroll_test.go) cannot — its content
// only exists once a real `git diff` round-trips through loadCmd and a
// delivered sessionsLoadedMsg, which diffScrollModel drives through the
// genuine message flow rather than poking DiffText onto the Session by hand.
//
// The three sizes are the ones TestDetailPaneClipsToItsHeightBudget uses;
// each is load-bearing because the off-by-three was constant across all of
// them while the row budgets differ, so any one of them catches a
// re-divergence of the two heights.
func TestDetailScrollReachesTheLastLineOfContent(t *testing.T) {
	for _, tc := range []struct{ width, height int }{
		{100, 24},
		{80, 10},
		{120, 40},
	} {
		t.Run(fmt.Sprintf("%dx%d/waves", tc.width, tc.height), func(t *testing.T) {
			m := testModel(bigPlanSession("alpha"))
			m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
			m, _ = update(t, m, tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			assertDetailScrollReachesLastLine(t, focusOverview(t, m))
		})
		t.Run(fmt.Sprintf("%dx%d/diff", tc.width, tc.height), func(t *testing.T) {
			m := diffScrollModel(t, tc.width, tc.height)
			assertDetailScrollReachesLastLine(t, m)
		})
	}
}

// assertDetailScrollReachesLastLine drives ctrl+d through Update until the
// offset stops moving, then asserts on the RENDERED frame — not on the
// offset — that the last line of m's active tab's body is on screen and the
// readout reads 100%. Asserting on the render is what makes the test honest:
// an offset can look right on a viewport the frame sizes differently. The
// down-at-the-bottom row pins that the ceiling is a ceiling and not
// merely where ctrl+d happened to stop. Callers must have already sized m
// (a WindowSizeMsg), focused the overview, and put it on the tab under test
// with content taller than the pane; a fixture that doesn't overflow fails
// fast below rather than passing vacuously.
func assertDetailScrollReachesLastLine(t *testing.T, m Model) {
	t.Helper()
	_, mainOuter, bodyHeight := m.layoutDims()

	body := strings.Split(xansi.Strip(m.detailBody(detailContentWidth(mainOuter))), "\n")
	last := strings.TrimSpace(body[len(body)-1])
	if last == "" {
		t.Fatalf("fixture broken: detailBody ends in a blank line")
	}
	top := xansi.Strip(m.renderDetail(mainOuter, bodyHeight))
	if strings.Contains(top, last) {
		t.Fatalf("fixture broken: last body line %q is visible before any scroll", last)
	}

	for i := 0; i < 1000; i++ {
		before := m.detailViewport.YOffset()
		m, _ = update(t, m, chord('d'))
		if m.detailViewport.YOffset() == before {
			break
		}
	}
	m, _ = update(t, m, keyDown)
	bottomOffset := m.detailViewport.YOffset()

	frame := xansi.Strip(m.renderDetail(mainOuter, bodyHeight))
	lines := strings.Split(frame, "\n")
	if got := len(lines); got != bodyHeight {
		t.Fatalf("scrolled frame is %d rows, want the %d-row budget:\n%s", got, bodyHeight, frame)
	}
	if !strings.Contains(frame, last) {
		t.Errorf("last body line %q never reached the frame at YOffset=%d:\n%s", last, bottomOffset, frame)
	}
	readoutRow := lines[len(lines)-2]
	if !strings.Contains(readoutRow, "100%") {
		t.Errorf("readout at the bottom reads %q, want 100%%", strings.TrimSpace(strings.Trim(readoutRow, "│ ")))
	}

	m, _ = update(t, m, keyDown)
	if got := m.detailViewport.YOffset(); got != bottomOffset {
		t.Errorf("down at the bottom moved YOffset %d -> %d; the ceiling is not a ceiling", bottomOffset, got)
	}

	for i := 0; i < 1000 && m.detailViewport.YOffset() > 0; i++ {
		m, _ = update(t, m, chord('u'))
	}
	if got := xansi.Strip(m.renderDetail(mainOuter, bodyHeight)); got != top {
		t.Errorf("frame after scrolling back to the top differs from the initial frame:\nwant:\n%s\ngot:\n%s", top, got)
	}
}

// TestDetailScrollOffsetResetsWhenSelectionChanges pins W2.T1's scroll reset,
// folded into schedulePreview: a session's scroll position has no meaning
// applied to a DIFFERENT session's content, so moving the cursor must snap
// detailViewport back to the top. This is the key-driven half of the reset;
// TestDetailScrollOffsetResetsWhenATickChangesTheSelectedSession covers the
// other funnel.
func TestDetailScrollOffsetResetsWhenSelectionChanges(t *testing.T) {
	m := testModel(bigPlanSession("alpha"), bigPlanSession("bravo"))
	m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	m = focusOverview(t, m)
	for i := 0; i < 3; i++ {
		m, _ = update(t, m, chord('d'))
	}
	m, _ = update(t, m, keyEsc)
	if got := m.detailViewport.YOffset(); got == 0 {
		t.Fatalf("fixture broken: scrolling alpha's detail pane left YOffset at 0")
	}

	m, _ = update(t, m, keyDown)

	if s, ok := m.selected(); !ok || s.Name != "bravo" {
		t.Fatalf("fixture broken: selection = %q ok=%v, want bravo", s.Name, ok)
	}
	if got := m.detailViewport.YOffset(); got != 0 {
		t.Fatalf("selection changed to bravo but detailViewport.YOffset() = %d, want 0 (reset)", got)
	}
}

// TestDetailScrollOffsetResetsWhenATickChangesTheSelectedSession pins the
// tick-driven half of the reset, the gap W2.T1's issues.md logged: a
// discovery load that drops the selected session leaves the cursor's INDEX
// where it was (rebuildListItems holds the index deliberately — see its doc),
// so a different session now sits under an unmoved cursor with no key
// pressed and no schedulePreview call. Its detail must start at the top, not
// at the vanished session's offset. The load-bearing assertion is the second:
// the first only proves the fixture really did put bravo under the cursor.
func TestDetailScrollOffsetResetsWhenATickChangesTheSelectedSession(t *testing.T) {
	m := testModel(bigPlanSession("alpha"), bigPlanSession("bravo"))
	m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	m = focusOverview(t, m)
	for i := 0; i < 3; i++ {
		m, _ = update(t, m, chord('d'))
	}
	m, _ = update(t, m, keyEsc)
	if got := m.detailViewport.YOffset(); got == 0 {
		t.Fatalf("fixture broken: scrolling alpha's detail pane left YOffset at 0")
	}

	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{bigPlanSession("bravo")}, at: time.Unix(5, 0)})

	if s, ok := m.selected(); !ok || s.Name != "bravo" {
		t.Fatalf("fixture broken: selection after the tick = %q ok=%v, want bravo at the unmoved index", s.Name, ok)
	}
	if got := m.detailViewport.YOffset(); got != 0 {
		t.Fatalf("a tick replaced alpha with bravo under the cursor but detailViewport.YOffset() = %d, want 0 (reset)", got)
	}
}

// TestDetailViewportOffsetClampedOnResize pins syncDetailViewport's trailing
// SetYOffset(YOffset()) call (model.go's doc: SetHeight/SetWidth never touch
// yOffset themselves; only SetYOffset clamps, against the viewport's CURRENT
// height), driven entirely through Update so the content the clamp runs
// against is what the WindowSizeMsg path itself seeded — not something the
// test poked in first.
//
// The ceiling is computed from detailRowBudget, the visible row count with the
// readout row subtracted, because that is the height the frame draws and the
// height syncDetailViewport must give the Model viewport. The first cut of
// this test computed TotalLineCount()-bodyHeight and thereby pinned the
// off-by-three the review caught; a ceiling written any other way than through
// the shared helper is the bug coming back.
//
// The "grows taller" row is load-bearing: max offset is (total lines -
// rows), which SHRINKS as rows GROW, so an offset valid at the smaller height
// lands past the new, smaller max — exactly what the trailing SetYOffset call
// exists to catch. Deleting that call leaves yOffset untouched at its old,
// now-too-large value and this row goes red.
//
// The "shrinks shorter" row is kept for parity with the plan's literal
// wording ("a resize that shrinks the pane") and the sidebar/detail-height
// tests' convention of covering both directions, but it is NOT load-bearing
// by itself: shrinking a viewport's height INCREASES its max offset, so an
// offset already valid at the taller size stays valid with no reclamp needed.
// It documents that a shrink is safe, it does not prove the fix.
func TestDetailViewportOffsetClampedOnResize(t *testing.T) {
	s := bigPlanSession("alpha")

	for _, tc := range []struct {
		name                   string
		fromW, fromH, toW, toH int
	}{
		{"grows taller: offset clamps down to the new, smaller max", 100, 10, 100, 40},
		{"shrinks shorter: offset already valid stays valid", 100, 40, 100, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(s)
			m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
			m, _ = update(t, m, tea.WindowSizeMsg{Width: tc.fromW, Height: tc.fromH})
			m = focusOverview(t, m)

			for i := 0; i < 1000; i++ {
				before := m.detailViewport.YOffset()
				m, _ = update(t, m, chord('d'))
				if m.detailViewport.YOffset() == before {
					break
				}
			}
			before := m.detailViewport.YOffset()
			if before == 0 {
				t.Fatalf("fixture broken: ctrl+d to the bottom left YOffset at 0 at the starting size")
			}

			m, _ = update(t, m, tea.WindowSizeMsg{Width: tc.toW, Height: tc.toH})

			_, _, bodyHeight := m.layoutDims()
			rows, _ := detailRowBudget(bodyHeight, m.detailViewport.TotalLineCount())
			if got := m.detailViewport.Height(); got != rows {
				t.Fatalf("Model viewport Height = %d after the resize, want the frame's %d rows", got, rows)
			}
			newMax := m.detailViewport.TotalLineCount() - rows
			if newMax < 0 {
				newMax = 0
			}
			want := before
			if want > newMax {
				want = newMax
			}
			if got := m.detailViewport.YOffset(); got != want {
				t.Fatalf("after resizing %dx%d -> %dx%d, YOffset = %d, want %d",
					tc.fromW, tc.fromH, tc.toW, tc.toH, got, want)
			}
		})
	}
}

// TestPageUpDownStillPageTheSessionListNotTheDetailPane pins W4.T1's disjoint
// key-set design goal at the Update/behavior level — mode_test.go's
// TestDetailScrollKeysRouteToActions already pins the dispatch-table half.
// Both rows are load-bearing, for halves neither one alone would catch: the
// first proves pgdown still pages the list (a dead key fails it), the second
// that it never touches a scrolled detail viewport (a pgdown that also
// scrolled fails it).
//
// The second row deliberately uses ONE session, because paging a multi-session
// list changes the selection and a selection change snaps the offset back to
// the top by design (resetDetailScroll) — which would mask a pgdown that had
// scrolled the pane on its way there. Its fixture must also actually overflow:
// against content that fits, YOffset is clamped to 0 and the assertion holds no
// matter what pgdown does, which is how this row was vacuous before. The
// explicit fixture-broken guards below are what keep it honest.
//
// The third row is W2.T1's inverted case: with the detail pane FOCUSED, pgdown
// must flip to scrolling the viewport and leave the session list untouched —
// the opposite pairing of the first two rows, over the same key.
func TestPageUpDownStillPageTheSessionListNotTheDetailPane(t *testing.T) {
	t.Run("pgdown pages the session list", func(t *testing.T) {
		sessions := make([]Session, 0, 40)
		for i := 0; i < 40; i++ {
			sessions = append(sessions, sess(fmt.Sprintf("s%02d", i)))
		}
		m := testModel(sessions...)
		m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

		before, ok := m.selected()
		if !ok {
			t.Fatalf("fixture broken: nothing selected before pgdown")
		}

		m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})

		after, ok := m.selected()
		if !ok || after.Name == before.Name {
			t.Fatalf("pgdown did not page the session list: selection is still %q", before.Name)
		}
	})

	t.Run("pgdown leaves a scrolled detail pane alone", func(t *testing.T) {
		m := testModel(bigPlanSession("alpha"))
		m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
		m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
		m = focusOverview(t, m)
		m, _ = update(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		m, _ = update(t, m, keyEsc)

		scrolled := m.detailViewport.YOffset()
		if scrolled == 0 {
			t.Fatalf("fixture broken: the detail pane did not scroll, so the offset assertion below proves nothing")
		}
		before, ok := m.selected()
		if !ok {
			t.Fatalf("fixture broken: nothing selected before pgdown")
		}

		m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})

		if after, _ := m.selected(); after.Name != before.Name {
			t.Fatalf("fixture broken: pgdown changed the selection to %q, which resets the offset by design", after.Name)
		}
		if got := m.detailViewport.YOffset(); got != scrolled {
			t.Errorf("pgdown moved the detail viewport (YOffset = %d), want it left at %d", got, scrolled)
		}
	})

	t.Run("focused: pgdown scrolls the detail pane, not the list", func(t *testing.T) {
		sessions := make([]Session, 0, 3)
		sessions = append(sessions, bigPlanSession("alpha"))
		for i := 0; i < 2; i++ {
			sessions = append(sessions, sess(fmt.Sprintf("s%02d", i)))
		}
		m := testModel(sessions...)
		m.detailTab = tabWaves
		m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
		m = focusOverview(t, m)

		before, ok := m.selected()
		if !ok {
			t.Fatalf("fixture broken: nothing selected before pgdown")
		}

		m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})

		if after, _ := m.selected(); after.Name != before.Name {
			t.Errorf("pgdown moved the session cursor from %q to %q while focused", before.Name, after.Name)
		}
		if got := m.detailViewport.YOffset(); got == 0 {
			t.Errorf("pgdown left the detail viewport at 0 while focused; want it scrolled")
		}
	})
}

// TestDetailScrollKeysAreInertWhileThePaneIsOnScreen pins that a scroll key
// with the right side focused moves the overview only when the overview is
// what is drawn: with a live pane for the selected session on screen, ctrl+d
// belongs to the session and the offset stays put. Both rows are load-bearing
// as a pair — the second proves the gate is the PANE's presence and not the
// key being dead, by putting the pane behind the overview and scrolling.
func TestDetailScrollKeysAreInertWhileThePaneIsOnScreen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hidden bool
		want   func(offset int) bool
	}{
		{"pane on screen: ctrl+d leaves the offset alone", false, func(o int) bool { return o == 0 }},
		{"pane hidden behind the summary: ctrl+d scrolls", true, func(o int) bool { return o > 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(bigPlanSession("alpha"))
			m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
			m.pane = newFakePane("x")
			m.paneName = "alpha"
			m.focus = focusRight
			if tc.hidden {
				m.view = viewOverview
			}
			if m.shouldRenderPane() == tc.hidden {
				t.Fatalf("fixture broken: shouldRenderPane() = %v with viewOverview=%v", m.shouldRenderPane(), tc.hidden)
			}

			m, _ = update(t, m, chord('d'))
			if got := m.detailViewport.YOffset(); !tc.want(got) {
				t.Fatalf("after ctrl+d with viewOverview=%v, YOffset = %d", tc.hidden, got)
			}
		})
	}
}

// TestFormEscClearsAppliedFilterBeforeCancelling covers the second of Esc's two
// levels. FilterState has three values: Tab/Enter promote a live filter from
// Filtering to FilterApplied, and a guard that only recognizes Filtering lets
// Esc fall through to the global cancel — closing the whole form on the user's
// first press after they narrowed the list, which contradicts both the help
// line and the README.
func TestFormEscClearsAppliedFilterBeforeCancelling(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main", "feature-alpha", "feature-beta"}
	m.plainTrunk = "main"
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := pressAction(t, m, keyRune('n'))
	m = pumpForm(t, m, cmd, 0)
	for i := 0; i < 2; i++ {
		m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
		m = pumpForm(t, m, cmd, 0)
	}
	m, cmd = update(t, m, keyRune('/'))
	m = pumpForm(t, m, cmd, 0)
	for _, r := range "feat" {
		m, cmd = update(t, m, keyRune(r))
		m = pumpForm(t, m, cmd, 0)
	}
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = pumpForm(t, m, cmd, 0)
	if m.form.baseList.FilterState() != list.FilterApplied {
		t.Fatalf("setup: FilterState = %v, want FilterApplied after Tab", m.form.baseList.FilterState())
	}

	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = pumpForm(t, m, cmd, 0)
	if !m.formOpen {
		t.Error("Esc with an APPLIED filter closed the form; it must clear the filter first")
	}
	if m.form.baseList.FilterState() != list.Unfiltered {
		t.Errorf("FilterState = %v after Esc, want Unfiltered", m.form.baseList.FilterState())
	}

	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = pumpForm(t, m, cmd, 0)
	if m.formOpen {
		t.Error("second Esc should cancel the form")
	}
}

// TestFormComponentsSizedOnOpen guards against sizing the components in View.
// View has a value receiver, so mutations there land on a copy: the list stays
// 0x0, renders nothing, and refuses to filter — while every assertion about
// input routing still passes.
func TestFormComponentsSizedOnOpen(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main", "feature-x"}
	m.plainTrunk = "main"
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = pressAction(t, m, keyRune('n'))
	if w := m.form.baseList.Width(); w <= 0 {
		t.Errorf("baseList width = %d immediately after open, want > 0", w)
	}
	if h := m.form.baseList.Height(); h <= 0 {
		t.Errorf("baseList height = %d immediately after open, want > 0", h)
	}
	if w := m.form.nameInput.Width(); w <= 0 {
		t.Errorf("nameInput width = %d, want > 0", w)
	}
}

// TestPlainBranchesKeptWhenTrunkResolutionFails covers a repo whose trunk
// cannot be determined: no origin/HEAD, no main or master, detached HEAD.
// plainBranchesCmd returns the branch list and the trunk error in one message,
// so gating both assignments on the error left the form with no base options at
// all — and made newNewForm's unresolved-trunk arm unreachable, since nothing
// could ever populate branches without also setting a trunk.
func TestPlainBranchesKeptWhenTrunkResolutionFails(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, plainBranchesLoadedMsg{
		branches: []string{"develop", "feature-x"},
		trunk:    "",
		err:      errors.New("could not determine a trunk branch"),
	})
	if len(m.plainBranches) != 2 {
		t.Errorf("plainBranches = %v, want both branches kept despite the trunk error", m.plainBranches)
	}
	if m.plainTrunk != "" {
		t.Errorf("plainTrunk = %q, want empty when resolution failed", m.plainTrunk)
	}

	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = pressAction(t, m, keyRune('n'))
	if got := len(m.form.baseList.Items()); got != 2 {
		t.Errorf("form offered %d base options, want 2 — an unresolved trunk must not empty the picker", got)
	}
}

// TestFormStaleValidationErrorClearsOnEdit pins the promise in newForm's struct
// comment. The hand-rolled editor this form replaced cleared the error inside
// every insert and delete; nothing inherited that, so a failed submit left an
// error naming a name the user had already corrected.
func TestFormStaleValidationErrorClearsOnEdit(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main"}
	m.plainTrunk = "main"
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = pressAction(t, m, keyRune('n'))
	m = typeRunes(t, m, "Bad_Name")
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.form.err == "" {
		t.Fatal("setup: an invalid name should have produced an inline error")
	}
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.form.err != "" {
		t.Errorf("err = %q after editing the name, want it cleared", m.form.err)
	}
}

// TestFormEmptyFilterEnterSubmits closes the last of the empty-filter dead
// keys. bubbles disables AcceptWhileFiltering while the filter input is empty,
// so Enter arrives already swallowed; Tab and Shift+Tab were given an escape
// route and Enter was not, leaving it inert until pressed a second time.
func TestFormEmptyFilterEnterSubmits(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main", "feat-a"}
	m.plainTrunk = "main"
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := pressAction(t, m, keyRune('n'))
	m = pumpForm(t, m, cmd, 0)
	m = typeRunes(t, m, "valid-name")
	for i := 0; i < 2; i++ {
		m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
		m = pumpForm(t, m, cmd, 0)
	}
	m, cmd = update(t, m, keyRune('/'))
	m = pumpForm(t, m, cmd, 0)

	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.formOpen && cmd == nil {
		t.Error("Enter on an empty filter neither submitted nor cancelled — dead key")
	}
}

// TestFormEscKeepsBaseSelection is the one whose failure was silent rather than
// inert: bubbles' ResetFilter snaps the cursor to index 0, so narrowing to a
// branch and pressing Esc to get the full list back moved the selection to
// trunk. Submitting then created the session off a base the user never chose,
// with nothing on screen to show it had changed.
func TestFormEscKeepsBaseSelection(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main", "feature-alpha", "feature-beta"}
	m.plainTrunk = "main"
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := pressAction(t, m, keyRune('n'))
	m = pumpForm(t, m, cmd, 0)
	for i := 0; i < 2; i++ {
		m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
		m = pumpForm(t, m, cmd, 0)
	}
	m, cmd = update(t, m, keyRune('/'))
	m = pumpForm(t, m, cmd, 0)
	for _, r := range "beta" {
		m, cmd = update(t, m, keyRune(r))
		m = pumpForm(t, m, cmd, 0)
	}
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = pumpForm(t, m, cmd, 0)

	narrowed := m.form.baseList.SelectedItem()
	if narrowed == nil || string(narrowed.(baseItem)) != "feature-beta" {
		t.Fatalf("setup: selected %v after filtering, want feature-beta", narrowed)
	}

	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = pumpForm(t, m, cmd, 0)
	if got := m.form.baseList.SelectedItem(); got == nil || string(got.(baseItem)) != "feature-beta" {
		t.Errorf("selected %v after clearing the filter, want feature-beta preserved", got)
	}
}

// TestFormDescriptionReachesSpawnArgv walks the whole dashboard path: typed
// into the form, through validate and submitNewForm, out to the argv the
// background `afk new --no-dash` is exec'd with. TestSpawnNewArgs covers the
// builder with literals, which proves the flag ordering but not that the form's
// textarea value actually arrives there — and a multi-line description must
// survive as ONE argv element, since the plan forbids it reaching a shell.
func TestFormDescriptionReachesSpawnArgv(t *testing.T) {
	m := testModel(sess("a"))
	m.plainBranches = []string{"main", "feature-x"}
	m.plainTrunk = "main"
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := pressAction(t, m, keyRune('n'))
	m = pumpForm(t, m, cmd, 0)

	m = typeRunes(t, m, "argv-check")
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = pumpForm(t, m, cmd, 0)
	m = typeRunes(t, m, "line one")
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pumpForm(t, m, cmd, 0)
	m = typeRunes(t, m, "line two")

	name, base, description, autonomous, providerName, err := m.form.validate()
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if description != "line one\nline two" {
		t.Fatalf("description = %q, want the two typed lines", description)
	}

	args := spawnNewArgs(name, base, description, autonomous, providerName)
	var found string
	for i, a := range args {
		if a == "--description" && i+1 < len(args) {
			found = args[i+1]
		}
	}
	if found != "line one\nline two" {
		t.Errorf("--description argv element = %q, want the description as one element with a real newline", found)
	}
	if args[len(args)-1] != "argv-check" {
		t.Errorf("argv = %v, want the session name last", args)
	}
}

// TestFormEmptyGitSpiceStackDoesNotFabricateBase covers a git-spice repo with
// nothing tracked yet, or whose `gs log short` failed: stackState.Detected is
// true while Branches is empty, and session.TrunkOf falls back to a hardcoded
// "main" on an empty stack. Keying the arm on Detected alone therefore offered a
// single invented option, preselected — possibly a branch the repo does not
// have — and every session created from the form would go to a nonexistent
// base. The plain-git arm had the same defect and was fixed; this pins the other
// half.
func TestFormEmptyGitSpiceStackDoesNotFabricateBase(t *testing.T) {
	tests := []struct {
		name  string
		stack stackState
	}{
		{"detected, nothing tracked yet", stackState{Detected: true}},
		{"detected, gs log short errored", stackState{Detected: true, Err: "gs log short --json: exit 1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("a"))
			m.stack = tt.stack
			m.plainBranches = []string{"develop", "feature-x"}
			m.plainTrunk = "develop"
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			m, _ = pressAction(t, m, keyRune('n'))

			items := m.form.baseList.Items()
			got := make([]string, len(items))
			for i, it := range items {
				got[i] = string(it.(baseItem))
			}
			for _, g := range got {
				if g == "main" {
					t.Errorf("base options = %v, want no fabricated 'main' — the repo has %v", got, m.plainBranches)
				}
			}
			if len(got) != 2 {
				t.Errorf("base options = %v, want the 2 real branches", got)
			}
			if sel := m.form.baseList.SelectedItem(); sel == nil || string(sel.(baseItem)) != "develop" {
				t.Errorf("selected = %v, want the resolved trunk 'develop'", sel)
			}
		})
	}
}

// TestPreviewTimerDroppedWhenATickReshuffledTheRow is the sharpest failure this
// feature can produce, and the reason the name check is not redundant with the
// generation check.
//
// applyLoaded replaces m.sessions wholesale every 2s and rebuildListItems keeps
// the same INDEX, so a session appearing or disappearing changes which session the
// cursor points at with NO cursor move — hence no generation bump. The armed
// generation still matches. Without the name check, the timer opens "z", a
// session the user never selected.
//
// "z" is deliberately HostRunning: the fresh-value re-check must not be what
// saves this, or the test would pass with the name check deleted.
func TestPreviewTimerDroppedWhenATickReshuffledTheRow(t *testing.T) {
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = refusingOpen(t, "a timer whose row was reshuffled by a discovery tick")

	m, armed := update(t, m, keyDown)
	if armed == nil {
		t.Fatalf("setup: the move onto b must arm a preview")
	}
	gen := m.previewGen

	// The tick: index 1 now holds "z" instead of "b". No cursor move, so no bump.
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen,
		sessions: []Session{runningSess("a"), runningSess("z")},
		at:       time.Unix(9, 0),
	})
	if m.previewGen != gen {
		t.Fatalf("a discovery tick must not bump previewGen: %d → %d", gen, m.previewGen)
	}
	if s, _ := m.selected(); s.Name != "z" {
		t.Fatalf("setup: selection after the tick = %q, want z", s.Name)
	}

	got, cmd := update(t, m, previewTimerMsg{gen: gen, name: "b"})
	if cmd != nil {
		t.Fatalf("a timer for a row the tick moved away must return no Cmd")
	}
	if got.pane != nil || got.paneName != "" {
		t.Fatalf("nothing must open: pane=%v name=%q", got.pane != nil, got.paneName)
	}
}

// TestSchedulePreviewRefusalsStillInvalidate covers the rows where a cursor move
// arms NOTHING, and pins the thing that is easy to get wrong about them: the
// generation is bumped anyway.
//
// Both rows are load-bearing for that reason. A refusal that skipped the bump
// would leave the PREVIOUS row's timer live, so moving from a running session
// onto a stopped one would still open the session the cursor just left — which
// is exactly what the delivery half of each row proves cannot happen.
//
//   - "stopped host" also asserts no restart Cmd and never spawns: a cursor move
//     must not start processes.
//   - "being cleaned" is the dial that would put a live pane on a host the
//     clean is about to SIGKILL. (The old "already in the pane" row is no
//     longer reachable by a cursor move: moving off the paned row detaches its
//     pane — see TestMovingOffThePanedRowDetachesIt.)
func TestSchedulePreviewRefusalsStillInvalidate(t *testing.T) {
	tests := []struct {
		name string
		// setup installs whatever state makes the landing row unpreviewable and
		// returns the model with the cursor on the row BEFORE it.
		setup func(t *testing.T) Model
		// armedFor is the row the first move (keyUp) arms, whose timer must be
		// invalidated by the refused second move (keyDown).
		armedFor string
		landsOn  string
	}{
		{
			name: "stopped host: never restart from a cursor move",
			setup: func(t *testing.T) Model {
				m := testModel(runningSess("a"), sess("b"))
				m.selectSessionIndex(1)
				m.deps.SpawnHost = func(name, _ string) (session.HostProcess, error) {
					t.Errorf("a cursor move must never spawn a host (got %q)", name)
					return session.HostProcess{}, nil
				}
				return m
			},
			armedFor: "a",
			landsOn:  "b",
		},
		{
			name: "being cleaned: never dial a host the clean is about to stop",
			setup: func(t *testing.T) Model {
				m := testModel(runningSess("a"), runningSess("b"))
				m.markCleaning(cleanIDFor(runningSess("b")))
				m.selectSessionIndex(1)
				return m
			},
			armedFor: "a",
			landsOn:  "b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup(t)
			m.deps.OpenHostPane = refusingOpen(t, "a refused cursor move")

			m, armed := update(t, m, keyUp)
			if armed == nil {
				t.Fatalf("setup: the move onto %s must arm a preview", tt.armedFor)
			}
			armedGen := m.previewGen

			m, refused := update(t, m, keyDown)
			if s, _ := m.selected(); s.Name != tt.landsOn {
				t.Fatalf("setup: landed on %q, want %q", s.Name, tt.landsOn)
			}
			if refused != nil {
				t.Fatalf("this move must arm nothing and return no Cmd")
			}
			if m.previewGen == armedGen {
				t.Fatalf("previewGen = %d, want it bumped past %d even on a refusal", m.previewGen, armedGen)
			}

			// The bump's purpose: the timer armed for the row we left is now inert.
			got, cmd := update(t, m, previewTimerMsg{gen: armedGen, name: tt.armedFor})
			if cmd != nil {
				t.Fatalf("the timer for %s must have been invalidated by the refused move", tt.armedFor)
			}
			if got.paneName == tt.armedFor {
				t.Fatalf("the invalidated timer opened %q anyway", tt.armedFor)
			}
		})
	}
}

// TestPreviewTimerDroppedByModelState covers the four states in which the arriving
// timer is still current (right generation, right name) but opening a pane would
// be wrong.
//
// All four rows are load-bearing because each guards a different surface:
// formOpen and both modal modes own the main region or the footer, so a pane
// appearing under them is invisible; paneOwnsKeyboard means the user is TYPING into a
// session, and swapping the pane out from under a keystroke is the most hostile
// thing this feature could do. The paneOwnsKeyboard row deliberately names the pane
// "a" while the cursor sits on "b", so the already-in-pane check cannot be what
// drops the timer.
func TestPreviewTimerDroppedByModelState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m *Model)
	}{
		{"creation form open", func(m *Model) { m.formOpen = true }},
		{"keyboard routed into another pane", func(m *Model) {
			m.pane = newFakePane("A-BEING-TYPED-INTO")
			m.paneName = "a"
			m.focus = focusRight
		}},
		{"clean confirmation modal up", func(m *Model) {
			m.modal = inputConfirmClean
			m.confirmTarget = "b"
		}},
		{"spawn error modal up", func(m *Model) { m.modal = inputSpawnError }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(runningSess("a"), runningSess("b"))
			m.deps.OpenHostPane = refusingOpen(t, tt.name)

			m, armed := update(t, m, keyDown)
			if armed == nil {
				t.Fatalf("setup: the move onto b must arm a preview")
			}
			gen := m.previewGen
			tt.mutate(&m)

			got, cmd := update(t, m, previewTimerMsg{gen: gen, name: "b"})
			if cmd != nil {
				t.Fatalf("the timer must return no Cmd while %s", tt.name)
			}
			if got.paneName == "b" {
				t.Fatalf("the timer opened b while %s", tt.name)
			}
		})
	}
}

// TestPreviewTimerAfterACleanIsConfirmedIsSilent is the cleaning re-check.
// Confirming a clean returns mode to inputNormal and sets no flag applyPreviewTimer's
// other guards look at, so a timer armed before the confirmation arrives fully
// current.
//
// The load-bearing assertion is the STATUS one, not "opens nothing". Without the
// cleaning re-check the timer reaches openPaneFor, whose own refusal writes
// "can't open b — it's being cleaned" — refusal text produced by nothing but a cursor
// move, on a session the user did ask to clean. openPaneFor also returns a nil
// Cmd on that refusal, so "opens nothing" passes either way and would miss it
// entirely.
func TestPreviewTimerAfterACleanIsConfirmedIsSilent(t *testing.T) {
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = refusingOpen(t, "a timer for a session being cleaned")

	m, armed := update(t, m, keyDown)
	if armed == nil {
		t.Fatalf("setup: the move onto b must arm a preview")
	}
	gen := m.previewGen

	m, _ = confirmClean(t, m) // the cursor is on b, so b is what gets cleaned
	if !m.cleaningSessions[cleanIDByName("b")] {
		t.Fatalf("setup: b should be mid-clean")
	}
	if m.modal != inputNormal {
		t.Fatalf("setup: confirming a clean returns to normal mode, got %v", m.modal)
	}
	before := m.status

	got, cmd := update(t, m, previewTimerMsg{gen: gen, name: "b"})
	if cmd != nil {
		t.Fatalf("a timer for a cleaning session must return no Cmd")
	}
	if got.pane != nil {
		t.Fatalf("a timer for a cleaning session must not open a pane")
	}
	if got.status != before {
		t.Fatalf("status = %q, want it untouched (%q) — a cursor move must not produce error text", got.status, before)
	}
}

// TestToggleRacingAPreview covers both orders of the race between an explicit
// ctrl+t and a preview timer already in flight, plus the case that proves the
// generation bump inside openPaneFor is load-bearing.
//
// All three subtests are load-bearing and none of them substitutes for another:
//
//   - "stale timer arrives mid-attach" deliberately does NOT deliver the
//     paneAttachedMsg first. Once the pane is installed the already-in-pane
//     re-check drops the timer regardless of the generation, which would make the
//     test non-diagnostic for the bump.
//   - "cursor move while an attach is in flight" asserts the Cmd from the
//     keypress is NIL. Asserting "b never opens" would pass with the guard
//     deleted, because no timer is ever delivered here — nothing could open b
//     either way.
//   - "stale timer after a FAILED attach" is the only case where the previewGen
//     bump is the sole remaining guard: the failure clears attachInFlight,
//     installs no pane, and (being a generic error, not a wrapped
//     host.ErrHostNotReachable) leaves HostRunning true, so every other condition
//     in applyPreviewTimer passes.
func TestToggleRacingAPreview(t *testing.T) {
	t.Run("stale timer arrives mid-attach and opens no second connection", func(t *testing.T) {
		var opened []hostPaneConfig
		m := testModel(runningSess("a"), runningSess("b"))
		m.selectSessionIndex(1)
		m.deps.OpenHostPane = recordingOpen(&opened, newFakePane("A"))

		m, armed := update(t, m, keyUp) // lands on a, arms a preview for a
		if armed == nil {
			t.Fatalf("setup: the move onto a must arm a preview")
		}
		staleGen := m.previewGen

		m, attach := update(t, m, keyEnter)
		if attach == nil {
			t.Fatalf("setup: ctrl+t on a running session must dispatch a connect")
		}
		if _, ok := attach().(paneAttachedMsg); !ok {
			t.Fatalf("setup: the connect Cmd must produce a paneAttachedMsg")
		}
		if m.attachInFlight.name != "a" || m.attachInFlight.preview {
			t.Fatalf("setup: attachInFlight = %+v, want an explicit open of a", m.attachInFlight)
		}
		if len(opened) != 1 {
			t.Fatalf("setup: opens = %d, want exactly 1 from ctrl+t", len(opened))
		}

		// The attach is still in flight — no paneAttachedMsg delivered.
		got, cmd := update(t, m, previewTimerMsg{gen: staleGen, name: "a"})
		if cmd != nil {
			t.Fatalf("a preview timer must not dial a second connection to an attach in flight")
		}
		if len(opened) != 1 {
			t.Fatalf("opens = %d (%+v), want still exactly 1", len(opened), opened)
		}
		if got.pane != nil {
			t.Fatalf("nothing should be installed yet")
		}
	})

	t.Run("cursor move while an attach is in flight arms nothing", func(t *testing.T) {
		var opened []hostPaneConfig
		m := testModel(runningSess("a"), runningSess("b"))
		m.deps.OpenHostPane = recordingOpen(&opened, newFakePane("A"))

		m, attach := update(t, m, keyEnter)
		if attach == nil {
			t.Fatalf("setup: ctrl+t on a running session must dispatch a connect")
		}
		if m.attachInFlight.name != "a" || m.attachInFlight.preview {
			t.Fatalf("setup: attachInFlight = %+v, want an explicit open of a", m.attachInFlight)
		}

		// Enter focused the right side; the cursor is the list's, so the
		// user goes back to it while the dial is still out.
		m, _ = pressAction(t, m, keyEscMsg())
		m, armed := update(t, m, keyDown)
		if s, _ := m.selected(); s.Name != "b" {
			t.Fatalf("setup: the move must still land on b, got %q", s.Name)
		}
		// The Cmd itself, not its eventual effect: this is the only observable
		// difference the attachInFlight guard makes here.
		if armed != nil {
			t.Fatalf("a cursor move must arm no preview while an explicit attach is in flight")
		}
	})

	t.Run("stale timer after a FAILED attach opens no second connection", func(t *testing.T) {
		var opened []hostPaneConfig
		m := testModel(runningSess("a"), runningSess("b"))
		m.selectSessionIndex(1)
		m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
			opened = append(opened, cfg)
			// Generic, NOT a wrapped host.ErrHostNotReachable: that would flip
			// HostRunning to false via markHostStopped and the fresh-value
			// re-check, not the generation, would be what drops the timer.
			return nil, errors.New("socket permission denied")
		}

		m, armed := update(t, m, keyUp) // lands on a, arms a preview for a
		if armed == nil {
			t.Fatalf("setup: the move onto a must arm a preview")
		}
		staleGen := m.previewGen

		m, attach := update(t, m, keyEnter)
		if attach == nil {
			t.Fatalf("setup: enter must dispatch a connect")
		}
		failed, ok := attach().(paneAttachedMsg)
		if !ok || failed.err == nil {
			t.Fatalf("setup: want a failed paneAttachedMsg, got %#v", failed)
		}
		m, _ = update(t, m, failed) // clears attachInFlight, installs nothing
		if m.attachInFlight.name != "" {
			t.Fatalf("setup: a failed attach must clear attachInFlight, got %+v", m.attachInFlight)
		}
		if m.pane != nil {
			t.Fatalf("setup: a failed attach must install no pane")
		}
		if !m.sessions[0].HostRunning {
			t.Fatalf("setup: a generic failure must leave HostRunning true, or this test proves nothing")
		}
		if len(opened) != 1 {
			t.Fatalf("setup: opens = %d, want exactly 1", len(opened))
		}

		got, cmd := update(t, m, previewTimerMsg{gen: staleGen, name: "a"})
		if cmd != nil {
			t.Fatalf("the timer superseded by ctrl+t must return no Cmd")
		}
		if len(opened) != 1 {
			t.Fatalf("opens = %d (%+v), want still exactly 1 — ctrl+t's previewGen bump is what stops the second dial", len(opened), opened)
		}
		if got.pane != nil {
			t.Fatalf("nothing should be installed")
		}
	})
}

// TestPaneVersusDetailRenderSelection walks every branch of shouldRenderPane
// through the real View, because the pane and the detail view compete for the
// same region and picking wrong is silent — the screen just shows the wrong
// session.
//
// All four rows are load-bearing:
//   - "cursor moved off the paned session" must show the row's DETAIL, never
//     the pane, or the screen names one session while showing another. The
//     cursor is moved directly, not by a key: a key move also detaches the
//     pane (TestMovingOffThePanedRowDetachesIt), and this row pins the render
//     rule on its own.
//   - "cursor back on the paned session" is the return trip, in the Agent,
//     the one level that draws a pane.
//   - "a tick reshuffles the list under a focused pane" is the hostile case: a
//     discovery pass that reorders rows must not move the selection off the
//     session the user is typing into. The selection is pinned by name while
//     the right side is focused, so the pane keeps rendering and owning input.
//   - "no selection at all" is reachable when the paned session's row leaves
//     discovery (an external `afk clean`); the pane must keep rendering, or the
//     user is left with an invisible pane the keymap still guards against.
func TestPaneVersusDetailRenderSelection(t *testing.T) {
	const paneBody = "PANE-BODY-A"
	// b's detail header is the only surface that prints "b → main": the sidebar
	// row elides a branch equal to the session name.
	const bDetail = "b → main"

	tests := []struct {
		name     string
		arrange  func(t *testing.T, m *Model)
		wantPane bool
	}{
		{
			// The overview has the keyboard, as `d` gives it: at Project the
			// body is otherwise the project view (T16.4), which draws no
			// overview to name the row in.
			name: "cursor moved off the paned session renders that row's detail",
			arrange: func(_ *testing.T, m *Model) {
				m.selectSessionIndex(1)
				m.focus, m.view = focusRight, viewOverview
			},
			wantPane: false,
		},
		{
			// In the Agent: since W3.T1 of T16.2 the pane is drawn only there.
			name: "cursor back on the paned session renders the pane",
			arrange: func(_ *testing.T, m *Model) {
				m.selectSessionIndex(0)
				m.focus = focusRight
			},
			wantPane: true,
		},
		{
			name: "a tick reshuffles the list under a focused pane",
			arrange: func(t *testing.T, m *Model) {
				*m, _ = update(t, *m, keyEnter)
				if !m.paneOwnsKeyboard() {
					t.Fatalf("setup: enter should focus the installed pane")
				}
				*m, _ = update(t, *m, sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: []Session{runningSess("b"), runningSess("a")},
					at:       time.Unix(3, 0),
				})
				if s, _ := m.selected(); s.Name != "a" {
					t.Fatalf("selection after the reshuffle = %q, want the paned session a pinned", s.Name)
				}
				if !m.paneOwnsKeyboard() {
					t.Fatalf("the focused pane lost the keyboard to a reshuffle")
				}
			},
			wantPane: true,
		},
		{
			name: "no selection at all renders the pane",
			arrange: func(_ *testing.T, m *Model) {
				m.focus = focusRight
				*m = m.applyLoaded(sessionsLoadedMsg{gen: m.projectGen, sessions: nil, at: time.Unix(3, 0)})
			},
			wantPane: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Running rows: in the agent view a list-focused row with no live
			// agent draws the agent placeholder, and this test is about the
			// pane-versus-overview choice.
			m := testModel(runningSess("a"), runningSess("b"))
			m, _ = update(t, m, paneAttachedMsg{pane: newFakePane(paneBody), name: "a"})
			if m.pane == nil {
				t.Fatalf("setup: the pane must be installed")
			}
			tt.arrange(t, &m)

			if got := m.shouldRenderPane(); got != tt.wantPane {
				t.Fatalf("shouldRenderPane() = %v, want %v", got, tt.wantPane)
			}
			out := render(m)
			if got := strings.Contains(out, paneBody); got != tt.wantPane {
				t.Fatalf("View renders the pane = %v, want %v:\n%s", got, tt.wantPane, out)
			}
			if !tt.wantPane && !strings.Contains(out, bDetail) {
				t.Fatalf("View should render b's detail instead of the pane:\n%s", out)
			}
		})
	}
}

// TestEnterAttachFocusContract covers enter's focus-on-arrival contract and
// the leak a failed attach must not create.
//
// The LEAK half is the load-bearing one. Both halves pass if focusOnArrival is a
// Model flag set by connectSelected; only the leak half fails, because a flag on
// the Model survives the failed attach and gets consumed by the next arrival to
// come along — which, cursor movement being what it does, is a preview. The user
// would find their keystrokes going into a session they never asked to focus,
// after an attach they watched fail. The failure must also hand the keyboard
// back to the list: the right side has nothing to receive it, and enter — the
// retry — is a list key.
func TestEnterAttachFocusContract(t *testing.T) {
	t.Run("enter opens the agent focused with a preview still pending", func(t *testing.T) {
		fp := newFakePane("A")
		m := testModel(runningSess("a"), runningSess("b"))
		m.selectSessionIndex(1)
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return fp, nil }

		// The preview armed here is never delivered: enter must not wait on it.
		m, armed := update(t, m, keyUp)
		if armed == nil {
			t.Fatalf("setup: the move onto a must arm a preview")
		}

		m, attach := update(t, m, keyEnter)
		if attach == nil {
			t.Fatalf("setup: enter must dispatch a connect")
		}
		msg, ok := attach().(paneAttachedMsg)
		if !ok {
			t.Fatalf("setup: want a paneAttachedMsg, got %T", attach())
		}
		if !msg.focusOnArrival {
			t.Fatalf("enter's attach must carry focusOnArrival=true on the message")
		}
		if m.focus != focusRight || m.paneOwnsKeyboard() {
			t.Fatalf("while the dial is in flight: focus=%v paneFocus=%v, want right with no pane yet", m.focus, m.paneOwnsKeyboard())
		}

		m, _ = update(t, m, msg)
		if m.paneName != "a" || !m.paneOwnsKeyboard() {
			t.Fatalf("after arrival: name=%q focused=%v, want a/true", m.paneName, m.paneOwnsKeyboard())
		}
	})

	t.Run("a failed enter does not leak focus onto a later preview", func(t *testing.T) {
		m := testModel(runningSess("a"))
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
			return nil, errors.New("socket permission denied")
		}

		// The Model the keypress RETURNS has to be the one carried forward, not
		// the one it was given. Discarding it would discard exactly the state a
		// Model-held focus flag would have been written into, and this subtest
		// would then pass with the flag on the Model — the mutation it exists to
		// catch.
		m, attach := update(t, m, keyEnter)
		if attach == nil {
			t.Fatalf("setup: enter must dispatch a connect")
		}
		failed, ok := attach().(paneAttachedMsg)
		if !ok || failed.err == nil {
			t.Fatalf("setup: want a failed paneAttachedMsg, got %#v", failed)
		}
		// Deliberately NOT asserting failed.focusOnArrival here. That assertion
		// pins the MESSAGE's shape, and under exactly the mutation this subtest
		// exists to catch (the flag living on the Model instead) it would fail
		// first, pre-empting the leak assertion below and reporting the test as
		// diagnostic for the wrong reason. The behavior is the contract; the
		// first subtest covers the wire.
		m, _ = update(t, m, failed)
		if m.pane != nil || m.paneOwnsKeyboard() {
			t.Fatalf("after the failure: pane=%v focused=%v, want nil/false", m.pane != nil, m.paneOwnsKeyboard())
		}
		if m.focus != focusList {
			t.Fatalf("after the failure: focus = %v, want the keyboard handed back to the list", m.focus)
		}

		// The next arrival is a preview's, which carries no focus.
		got, _ := update(t, m, paneAttachedMsg{
			pane:    newFakePane("A-PREVIEW"),
			name:    "a",
			preview: true,
		})
		if got.paneName != "a" {
			t.Fatalf("setup: the preview should have installed, name=%q", got.paneName)
		}
		if got.paneOwnsKeyboard() {
			t.Fatalf("a preview must arrive unfocused — the failed enter's focus must not survive it")
		}
	})
}

// TestFailedPreviewIsSilent pins all four things a preview's failed dial must NOT
// do. Each would be wrong for its own reason and every one of them is a
// load-bearing row:
//
//   - status: error text produced by nothing but a cursor move.
//   - deadHostPIDs / HostRunning: markHostStopped's justification is the
//     five-second explicit dial being proof the flag lied. A preview's ~300ms
//     budget is not that proof, and the override is pinned against a PID a LIVE
//     host keeps re-asserting — a merely slow host would read as stopped
//     permanently, and ctrl+t would then spawn a SECOND host for it.
//   - loadCmd: browsing N stale rows would fire N extra full discovery passes on
//     top of the ticker.
//
// The error is built with %w so errors.Is MATCHES. An error merely containing
// the sentinel's text would make the markHostStopped assertions pass for the
// wrong reason — the branch would never have been reachable.
func TestFailedPreviewIsSilent(t *testing.T) {
	m := testModel(runningSess("a"))
	m.status = "SENTINEL-STATUS"

	got, cmd := update(t, m, paneAttachedMsg{
		name:    "a",
		preview: true,
		err:     fmt.Errorf("attaching to the a session: %w", host.ErrHostNotReachable),
	})

	if cmd != nil {
		t.Fatalf("a failed preview must return no Cmd (no reload)")
	}
	if got.status != "SENTINEL-STATUS" {
		t.Fatalf("status = %q, want it untouched", got.status)
	}
	if len(got.deadHostPIDs) != 0 {
		t.Fatalf("deadHostPIDs = %v, want empty — a preview's dial window is not proof the host is gone", got.deadHostPIDs)
	}
	if !got.sessions[0].HostRunning {
		t.Fatalf("a failed preview must not flip the row to stopped")
	}
	if got.pane != nil {
		t.Fatalf("a failed preview must install no pane")
	}
}

// TestCleanRefusedOpenDoesNotWedgePreviews covers the leak that would disable this
// whole feature for the life of the process.
//
// openPaneFor's cleaning refusal returns a nil Cmd, so no paneAttachedMsg is ever
// delivered and nothing ever clears attachInFlight. Setting the flag ABOVE that
// refusal instead of below it therefore pins it forever, and schedulePreview's
// attachInFlight guard then refuses every future preview — silently, with no
// error anywhere.
//
// spawnFinishedMsg is the entrance used because it is the one that reaches
// openPaneFor with a cleaning session at all: ctrl+t, the restart chain and the
// startup pane each refuse earlier, before openPaneFor is called.
//
// BOTH assertions are load-bearing: the flag being empty is the direct
// invariant, and the subsequent cursor move arming a preview is the user-visible
// consequence that would otherwise be lost.
func TestCleanRefusedOpenDoesNotWedgePreviews(t *testing.T) {
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = refusingOpen(t, "a cleaning session")

	m, _ = confirmClean(t, m) // the cursor is on a
	if !m.cleaningSessions[cleanIDByName("a")] {
		t.Fatalf("setup: a should be mid-clean")
	}

	m, _ = update(t, m, spawnFinishedMsg{name: "a"})
	if !strings.Contains(m.status, "can't open a") {
		t.Fatalf("setup: status = %q, want openPaneFor's refusal — the wrong entrance was used", m.status)
	}
	if m.attachInFlight.name != "" {
		t.Fatalf("attachInFlight = %+v, want empty: a refused open dispatches no Cmd, so nothing would ever clear it", m.attachInFlight)
	}

	_, armed := update(t, m, keyDown)
	if armed == nil {
		t.Fatalf("a cursor move after a refused open must still arm a preview")
	}
}

// --- fix cycle: the four blockers the first pass shipped ---

// TestEnterDuringAPreviewDialClaimsItWithoutDialingAgain is blocker 1, and
// the heart of the retired 300ms race. A preview whose dial is outstanding is
// a connection to the session's host; dialing a SECOND time makes the host
// send Stolen to the first, so enter racing a preview would tear down the
// very pane it was pressed to focus.
//
// Both assertions are load-bearing and neither implies the other:
//   - the open COUNT staying at 1 is the Objective-4 invariant: at most one attach
//     in flight PER SESSION. Without the record tracking previews, ctrl+t dials
//     again. It is not "at most one attach in flight" outright — two opens naming
//     different sessions both dispatch, and the second overwrites attachInFlight,
//     which is how docs/performance/pane-preview-races.md's B1 happens.
//   - the claimed preview arriving FOCUSED is enter's contract. A guard that
//     merely refuses a second dial but leaves the preview stale drops the one
//     connection enter is waiting on — which is what the old detail-ownership
//     clause in stalePreview did, because right focus with no pane yet reads as
//     detail ownership.
func TestEnterDuringAPreviewDialClaimsItWithoutDialingAgain(t *testing.T) {
	var opened []hostPaneConfig
	fp := newFakePane("B-PREVIEW")
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = recordingOpen(&opened, fp)

	m, armed := update(t, m, keyDown)
	if armed == nil {
		t.Fatalf("setup: the move onto b must arm a preview")
	}
	m, dial := update(t, m, previewTimerMsg{gen: m.previewGen, name: "b"})
	if dial == nil {
		t.Fatalf("setup: the timer must dial the preview")
	}
	attached, ok := dial().(paneAttachedMsg)
	if !ok {
		t.Fatalf("setup: the preview dial produced %T, want paneAttachedMsg", dial())
	}
	if !attached.preview {
		t.Fatalf("setup: the dial must be marked preview, got %+v", attached)
	}
	if m.attachInFlight.name != "b" || !m.attachInFlight.preview {
		t.Fatalf("setup: attachInFlight = %+v, want an outstanding preview of b", m.attachInFlight)
	}
	if len(opened) != 1 {
		t.Fatalf("setup: opens = %d, want exactly 1 from the preview", len(opened))
	}

	// The preview's result has NOT been delivered: its dial is still outstanding,
	// which is the whole window this test is about.
	m, cmd := update(t, m, keyEnter)
	if cmd != nil {
		t.Fatalf("enter must not dispatch a second dial to a host already being dialed")
	}
	if len(opened) != 1 {
		t.Fatalf("opens = %d (%+v), want still exactly 1 — a second connection would get the first Stolen", len(opened), opened)
	}
	if !strings.Contains(m.status, "opening b") {
		t.Fatalf("status = %q, want enter to claim the in-flight preview as its explicit open", m.status)
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want the claim to move focus right at the keypress", m.focus)
	}

	m, _ = update(t, m, attached)
	if m.paneName != "b" || fp.closed != 0 {
		t.Fatalf("paneName = %q closed=%d, want the claimed preview's pane installed", m.paneName, fp.closed)
	}
	if !m.paneOwnsKeyboard() {
		t.Fatalf("enter must land in the claimed preview with the keyboard")
	}
}

// TestEnterDifferentTargetBeatsStalePreviewInEitherDeliveryOrder pins the two
// result orders for preview A racing an explicit enter on selected target B.
// The preview is the loser in both orders: its handle is closed, it cannot
// clear B's outstanding record when B has not arrived yet, and it cannot
// replace B after B is visible.
func TestEnterDifferentTargetBeatsStalePreviewInEitherDeliveryOrder(t *testing.T) {
	for _, previewFirst := range []bool{true, false} {
		name := "explicit B arrives before stale preview A"
		if previewFirst {
			name = "stale preview A arrives before explicit B"
		}
		t.Run(name, func(t *testing.T) {
			paneA, paneB := newFakePane("A-PREVIEW"), newFakePane("B-EXPLICIT")
			queue := []paneHandle{paneA, paneB}
			m := testModel(runningSess("a"), runningSess("b"))
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				pane := queue[0]
				queue = queue[1:]
				return pane, nil
			}

			m, previewCmd := update(t, m, previewTimerMsg{gen: m.previewGen, name: "a"})
			if previewCmd == nil {
				t.Fatal("setup: current preview timer for a must dispatch its dial")
			}
			previewA := drainForPaneAttached(t, previewCmd)
			m.selectSessionIndex(1)
			m, explicitCmd := update(t, m, keyEnter)
			if explicitCmd == nil {
				t.Fatal("enter on selected b must dispatch the explicit target dial")
			}
			explicitB := drainForPaneAttached(t, explicitCmd)
			if !explicitB.focusOnArrival {
				t.Fatal("enter's explicit target must carry focusOnArrival")
			}
			if m.attachInFlight.name != "b" || m.attachInFlight.preview {
				t.Fatalf("attachInFlight = %+v, want explicit b", m.attachInFlight)
			}

			if previewFirst {
				m, _ = update(t, m, previewA)
				if paneA.closed != 1 || m.pane != nil {
					t.Fatalf("stale preview A must be closed without installation: closed=%d pane=%v", paneA.closed, m.pane != nil)
				}
				if m.attachInFlight.name != "b" || m.attachInFlight.preview {
					t.Fatalf("stale A cleared B's in-flight record: %+v", m.attachInFlight)
				}
				m, _ = update(t, m, explicitB)
			} else {
				m, _ = update(t, m, explicitB)
				if m.pane != paneB || m.paneName != "b" {
					t.Fatalf("explicit B did not become visible: pane=%v name=%q", m.pane == paneB, m.paneName)
				}
				m, _ = update(t, m, previewA)
				if paneA.closed != 1 {
					t.Fatalf("late stale preview A closed %d times, want 1", paneA.closed)
				}
			}

			if m.pane != paneB || m.paneName != "b" || !m.paneOwnsKeyboard() {
				t.Fatalf("final pane=%v name=%q focused=%v, want visible focused B",
					m.pane == paneB, m.paneName, m.paneOwnsKeyboard())
			}
			if paneB.closed != 0 {
				t.Fatalf("winning pane B closed %d times, want 0", paneB.closed)
			}
			if s, ok := m.selected(); !ok || s.Name != "b" {
				t.Fatalf("selected = %q (ok=%v), want b throughout the race", s.Name, ok)
			}
		})
	}
}

// TestSurplusArrivalStillHonoursExplicitFocusRequest is blocker 2: the reverse arrival
// order. A preview's pane for X installs first, so a focus-request attach for X
// arrives as the same-name surplus — and the connection being surplus does not
// make the keypress surplus.
//
// The surplus must STILL be closed (it would otherwise leak a client slot on the
// host with no handle left to close it), so both assertions are load-bearing:
// closing without focusing is the bug, focusing without closing is a leak. The
// messages are constructed directly, as the 31 other paneAttachedMsg literals in
// this file are, because the arrival ORDER is the subject — driving it through a
// keypress would exercise the join path instead (see the test above).
func TestSurplusArrivalStillHonoursExplicitFocusRequest(t *testing.T) {
	installed, surplus := newFakePane("B-PREVIEW"), newFakePane("B-FROM-ENTER")
	m := testModel(runningSess("b"), runningSess("a"))
	// The list is kept in name order inside a group, so b is selected by name.
	m.selectSessionIndex(namedIndex(t, m, "b"))

	m, _ = update(t, m, paneAttachedMsg{pane: installed, name: "b", preview: true})
	if m.paneName != "b" || m.paneOwnsKeyboard() {
		t.Fatalf("setup: want b installed and unfocused, got name=%q focused=%v", m.paneName, m.paneOwnsKeyboard())
	}

	m.focus = focusRight // connect moves the focus at request time
	got, cmd := update(t, m, paneAttachedMsg{pane: surplus, name: "b", focusOnArrival: true})
	if cmd != nil {
		t.Fatalf("no subscription may be armed for a rejected connection")
	}
	if got.pane != paneHandle(installed) {
		t.Fatalf("the installed pane must stay in place")
	}
	if surplus.closed != 1 {
		t.Fatalf("the surplus connection must be closed exactly once, got %d", surplus.closed)
	}
	if installed.closed != 0 {
		t.Fatalf("the installed pane must not be closed, got %d", installed.closed)
	}
	if !got.paneOwnsKeyboard() {
		t.Fatalf("the request that produced the surplus asked for the keyboard — dropping its focus loses the explicit focus gesture")
	}
}

// TestPreviewArrivingForACleaningSessionIsSilent is blocker 3, and the STATUS
// assertion is the whole point: openPaneFor gates dispatch, so a clean confirmed
// while a preview's dial was already outstanding lands here, and an ungated
// status write puts "can't open b — it's being cleaned" on screen produced by nothing
// but a cursor move (Objective 5).
//
// The surplus connection must still be CLOSED — silence is not the same as
// ignoring it, and a dropped handle leaks a client slot on a host the clean is
// about to SIGKILL. The explicit half of this branch keeps its message and is
// covered by TestPaneArrivingForACleaningSessionIsDroppedNotInstalled.
func TestPreviewArrivingForACleaningSessionIsSilent(t *testing.T) {
	m := testModel(runningSess("a"), runningSess("b"))
	m.selectSessionIndex(1)
	m, _ = confirmClean(t, m)
	if !m.cleaningSessions[cleanIDByName("b")] {
		t.Fatalf("setup: b should be mid-clean")
	}
	m.status = "SENTINEL-STATUS"

	arriving := newFakePane("late-preview")
	got, cmd := update(t, m, paneAttachedMsg{pane: arriving, name: "b", preview: true})
	if cmd != nil {
		t.Fatalf("a preview arriving for a cleaning session must return no Cmd")
	}
	if got.pane != nil || got.paneName != "" {
		t.Fatalf("it must not be installed: pane=%v name=%q", got.pane != nil, got.paneName)
	}
	if arriving.closed != 1 {
		t.Fatalf("the dropped connection was closed %d times, want exactly 1", arriving.closed)
	}
	if got.status != "SENTINEL-STATUS" {
		t.Fatalf("status = %q, want it untouched — a cursor move must never produce error text", got.status)
	}
}

// TestRestartCarriesTheFocusRequestAcrossBothBoundaries pins the stopped-host
// path: the focus request must survive both async boundaries (the spawn, then
// the open hostRestartedMsg chains into) in both directions.
//
// The startup row is why the flag is carried rather than hardcoded: `afk dash
// --open` and `afk new`'s handoff restart the same way and must NOT grab the
// keyboard, since nobody pressed a key for that pane — while `r` pressed on the
// right side (the stopped row's focused overview), on the same path, must land
// focused.
func TestRestartCarriesTheFocusRequestAcrossBothBoundaries(t *testing.T) {
	t.Run("r from the right side lands focused", func(t *testing.T) {
		m := testModel(sess("stopped")) // sess() has HostRunning false
		m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
			return session.HostProcess{PID: 4242}, nil
		}
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
			return newFakePane("RESTARTED"), nil
		}

		m, _ = update(t, m, keyEnter)
		m, restart := pressAction(t, m, keyRune('r'))
		if restart == nil {
			t.Fatalf("setup: r on a stopped session's overview must dispatch the restart")
		}
		hr, ok := restart().(hostRestartedMsg)
		if !ok {
			t.Fatalf("setup: restart produced %T, want hostRestartedMsg", restart())
		}
		if !hr.focusOnArrival {
			t.Fatalf("a right-side r restart must carry focusOnArrival=true: %+v", hr)
		}

		m, open := update(t, m, hr)
		if open == nil {
			t.Fatalf("setup: a successful restart must chain into the connect")
		}
		attached, ok := open().(paneAttachedMsg)
		if !ok {
			t.Fatalf("setup: the chained cmd produced %T, want paneAttachedMsg", open())
		}
		if !attached.focusOnArrival {
			t.Fatalf("the request built off the restart must keep focusOnArrival=true: %+v", attached)
		}

		m, _ = update(t, m, attached)
		if m.paneName != "stopped" || !m.paneOwnsKeyboard() {
			t.Fatalf("after the restart landed: name=%q focused=%v, want stopped/true", m.paneName, m.paneOwnsKeyboard())
		}
	})

	t.Run("the startup pane's restart does not", func(t *testing.T) {
		deps := testDeps()
		deps.InitialSessionName = "fresh"
		deps.SpawnHost = func(string, string) (session.HostProcess, error) {
			return session.HostProcess{PID: 4242}, nil
		}
		deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("X"), nil }
		m := New("/repo", time.Second, deps)

		m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		m, restart := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("fresh")}, at: time.Unix(1, 0)})
		if restart == nil {
			t.Fatalf("setup: a stopped startup session must be restarted first")
		}
		hr, ok := restart().(hostRestartedMsg)
		if !ok {
			t.Fatalf("setup: restart produced %T, want hostRestartedMsg", restart())
		}
		if hr.focusOnArrival {
			t.Fatalf("nobody pressed a key for the startup pane — it must arrive unfocused: %+v", hr)
		}

		m, open := update(t, m, hr)
		if open == nil {
			t.Fatalf("setup: the restart must chain into the connect")
		}
		m, _ = update(t, m, open().(paneAttachedMsg))
		if m.paneName != "fresh" {
			t.Fatalf("setup: paneName = %q, want fresh", m.paneName)
		}
		if m.paneOwnsKeyboard() {
			t.Fatalf("the startup pane must arrive with the keyboard on the dashboard")
		}
	})
}

// TestSuccessfulPreviewArrivalWritesNoStatus pins the last way a bare cursor move
// could speak over the user: the install path's "opened X" / "switched to X"
// lines. A preview was not requested, so it has no standing to clobber whatever
// was being read — and nothing is lost, because renderPane's title carries the
// session name and the focus hint.
//
// Both rows matter: the FIRST-pane row is the common browse, and the SWITCH row is
// the one where the outgoing session still has to be detached exactly once. The
// switch row asserts that, so silencing the status can never be mistaken for
// skipping the swap.
func TestSuccessfulPreviewArrivalWritesNoStatus(t *testing.T) {
	t.Run("first pane", func(t *testing.T) {
		m := testModel(runningSess("a"), runningSess("b"))
		m.status = "SENTINEL-STATUS"

		got, cmd := update(t, m, paneAttachedMsg{pane: newFakePane("B"), name: "b", preview: true})
		if cmd == nil {
			t.Fatalf("the installed pane still needs its redraw/exit subscription")
		}
		if got.paneName != "b" {
			t.Fatalf("setup: paneName = %q, want b installed", got.paneName)
		}
		if got.status != "SENTINEL-STATUS" {
			t.Fatalf("status = %q, want it untouched by a pane nobody asked for", got.status)
		}
	})

	t.Run("switching panes still detaches the outgoing session", func(t *testing.T) {
		first := newFakePane("A")
		m := testModel(runningSess("a"), runningSess("b"))
		m, _ = update(t, m, paneAttachedMsg{pane: first, name: "a", preview: true})
		m.status = "SENTINEL-STATUS"

		got, _ := update(t, m, paneAttachedMsg{pane: newFakePane("B"), name: "b", preview: true})
		if got.paneName != "b" {
			t.Fatalf("setup: paneName = %q, want the pane switched to b", got.paneName)
		}
		if first.closed != 1 {
			t.Fatalf("the outgoing session must still be detached exactly once, got %d", first.closed)
		}
		if got.status != "SENTINEL-STATUS" {
			t.Fatalf("status = %q, want it untouched", got.status)
		}
	})
}

// TestHelpLineAdvertisesPreviewWithoutGrowing is W6.T2's rewrite of the
// pinned width contract: renderHelp is now itself width-bound (help.Model.
// SetWidth, not a caller's separate truncate() call), so "never grows past
// its budget" is checked directly at the two widths that matter — 80 (the
// narrowest terminal anyone runs) and 40 (the plan's floor for this
// assertion) — via lipgloss.Width, rather than pinning one legacy column
// count (92) that only ever described the OLD hand-rolled ladder's output.
// The movement keys — this test's original point, when they were labelled
// "preview" — still have to survive both: since the project view (T16.4) the
// line leads with the section keys and "j/k move", which every section moves
// with, so those are what the narrowest line must keep.
func TestHelpLineAdvertisesPreviewWithoutGrowing(t *testing.T) {
	for _, width := range []int{80, 40} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := testModel(runningSess("live"))
			m.width = width
			help := xansi.Strip(m.renderHelp())

			for _, want := range []string{projectSectionKeysLabel, "j/k", "move"} {
				if !strings.Contains(help, want) {
					t.Fatalf("help = %q, want it to contain %q", help, want)
				}
			}
			if got := lipgloss.Width(help); got > width {
				t.Fatalf("help line is %d columns, want no more than the %d-column budget:\n%s",
					got, width, help)
			}
		})
	}
}

// --- wedge banner (W6.T2) ---
//
// wedgeBannerPane wraps the paneHandle openHostPane returns so the pane's own
// Render() — the one call site views.go's renderPane reaches (out of scope
// for this task) — draws the banner without renderPane having to know
// anything changed. These tests construct the type directly (same package)
// rather than through openHostPane, so they never need a real host.json or
// socket: the acceptance criteria are about what Render() produces given a
// wedge verdict, not about how that verdict is discovered.

// TestWedgeBannerPaneShowsBannerWhenWedged is this task's core acceptance
// test: a wedged pane's Render() must still contain the frozen screen
// verbatim (the plan's "do not clear the emulator's last screen state"),
// PLUS a banner naming the recovery key and the wedge age. All three are
// load-bearing — losing any one is exactly the incident this wave exists to
// fix (frozen screen, no explanation) or a regression the plan forbids
// (screen cleared).
func TestWedgeBannerPaneShowsBannerWhenWedged(t *testing.T) {
	inner := newFakePane("frozen screen: assistant was mid-reply")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	since := now.Add(-12 * time.Minute)
	p := &wedgeBannerPane{
		paneHandle:  inner,
		hint:        wedgeHintDashboard(defaultLeaderKey),
		now:         func() time.Time { return now },
		wedgedSince: since.Format(time.RFC3339),
	}

	got := p.Render()

	if !strings.Contains(got, "frozen screen: assistant was mid-reply") {
		t.Fatalf("wedge banner must not clear the prior screen content, got %q", got)
	}
	if !strings.Contains(got, "12m") {
		t.Fatalf("wedge banner missing the wedge age (want \"12m\"): %q", got)
	}
	if !strings.Contains(got, wedgeRecoveryKey) {
		t.Fatalf("wedge banner does not name the recovery key %q: %q", wedgeRecoveryKey, got)
	}
}

// TestWedgeBannerPaneHealthyPaneRendersUnchanged is the negative half of the
// acceptance criteria: a pane with no wedge verdict (wedgedSince == "", the
// zero value — matching a healthy or unknown-liveness session, same
// "absent is not an error" convention as internal/discover's own liveness
// fields) draws no banner and returns the inner Render() output byte for
// byte.
func TestWedgeBannerPaneHealthyPaneRendersUnchanged(t *testing.T) {
	inner := newFakePane("healthy screen content")
	p := &wedgeBannerPane{
		paneHandle: inner,
		now:        time.Now,
	}

	if got := p.Render(); got != "healthy screen content" {
		t.Fatalf("healthy pane must render unchanged, got %q", got)
	}
}

// TestWedgeBannerPanePollOnceReadsHostMetaWedgedSince covers how the wrapper
// learns about a wedge verdict in production: pollOnce (called off Render,
// see the type's doc) reads host.Meta.WedgedSince through the injected seam
// exactly once per call and records it for Render to pick up without any
// I/O of its own.
func TestWedgeBannerPanePollOnceReadsHostMetaWedgedSince(t *testing.T) {
	inner := newFakePane("x")
	calls := 0
	p := &wedgeBannerPane{
		paneHandle: inner,
		worktree:   "/repo",
		name:       "sess",
		now:        time.Now,
		readMeta: func(worktree, name string) (host.Meta, error) {
			calls++
			if worktree != "/repo" || name != "sess" {
				t.Fatalf("pollOnce called readMeta with (%q, %q), want (/repo, sess)", worktree, name)
			}
			return host.Meta{WedgedSince: "2020-01-01T00:00:00Z"}, nil
		},
	}

	p.pollOnce()

	if calls != 1 {
		t.Fatalf("pollOnce must call readMeta exactly once, got %d", calls)
	}
	if _, ok := p.wedgeAge(); !ok {
		t.Fatalf("pollOnce did not record the wedged verdict read from host.Meta")
	}
}

// TestWedgeBannerPanePollOnceKeepsLastVerdictOnReadError guards against a
// transient host.json read failure (host mid-rewrite, or a momentary
// filesystem hiccup) flapping the banner off: internal/host's own contract
// treats a missing/unparseable file as stale, never as a fact to act on, and
// pollOnce must honour that by leaving the previous verdict alone rather than
// clearing it.
func TestWedgeBannerPanePollOnceKeepsLastVerdictOnReadError(t *testing.T) {
	inner := newFakePane("x")
	p := &wedgeBannerPane{
		paneHandle:  inner,
		now:         time.Now,
		wedgedSince: time.Now().Add(-time.Minute).Format(time.RFC3339),
		readMeta: func(string, string) (host.Meta, error) {
			return host.Meta{}, errors.New("boom")
		},
	}

	p.pollOnce()

	if _, ok := p.wedgeAge(); !ok {
		t.Fatalf("a transient host.json read error must not clear the wedge verdict")
	}
}

// TestWedgeBannerPaneCloseStopsPollingAndClosesInner exercises the production
// constructor end to end (no host.json present, so readMeta's default —
// host.ReadMeta — errors on every poll and pollOnce is a no-op): Close must
// still stop the poll goroutine and delegate to the wrapped pane's Close, or
// a closed pane would leak a goroutine forever.
func TestWedgeBannerPaneCloseStopsPollingAndClosesInner(t *testing.T) {
	inner := newFakePane("x")
	p := newWedgeBannerPane(inner, t.TempDir(), "does-not-exist", wedgeHintDashboard(defaultLeaderKey))

	if err := p.Close(); err != nil {
		t.Fatalf("Close returned an error: %v", err)
	}
	if inner.closed != 1 {
		t.Fatalf("Close must close the wrapped pane exactly once, got %d", inner.closed)
	}
}

// --- W6.T3: restart keybinding ---

// TestRestartKeyInertOnHealthySession is the "must not offer the key for a
// session that is not wedged" acceptance row: a session showsWedged reports
// false for (no WedgedSince at all, and WedgedSince set but stale-cleared)
// must leave the key a complete no-op — no mode change, no Cmd, no status
// write, exactly as an unadvertised key would be.
func TestRestartKeyInertOnHealthySession(t *testing.T) {
	healthy := runningSess("alpha")
	healthy.LastOutputAt = time.Unix(1_000_000, 0).Add(-3 * time.Second).UTC().Format(time.RFC3339)
	m := testModel(healthy)

	got, cmd := pressAction(t, m, keyRune('r'))
	if got.modal != inputNormal {
		t.Fatalf("r on a healthy session should stay in normal mode, got %v", got.modal)
	}
	if cmd != nil {
		t.Fatalf("r on a healthy session must not dispatch anything, got %v", cmd)
	}
	// Since T16.2 the key runs alone, and a key that cannot act says why
	// (runLevelAction), so a healthy row's r answers with the restart row's
	// reason rather than doing nothing silently.
	if got.status != "Only a stopped or stuck agent can be restarted." {
		t.Fatalf("r on a healthy session wrote %q, want the reason it cannot restart", got.status)
	}
}

// TestRestartKeyInertWithNothingSelected covers the empty-list edge of the
// same "not wedged" rule: there is no row to check showsWedged against, so
// the key does nothing rather than panicking on an empty selection.
func TestRestartKeyInertWithNothingSelected(t *testing.T) {
	got, cmd := pressAction(t, testModel(), keyRune('r'))
	if got.modal != inputNormal || cmd != nil {
		t.Fatalf("r with nothing selected should be a total no-op, got mode=%v cmd=%v", got.modal, cmd)
	}
}

// TestRestartKeyEntersConfirmModeOnWedgedSession is the "on a wedged session
// it prompts for confirmation" acceptance row.
func TestRestartKeyEntersConfirmModeOnWedgedSession(t *testing.T) {
	wedged := wedgedSess("alpha", 12*time.Minute)
	m := testModel(wedged)

	// Restart runs from the leader (ctrl+] then r) since T7.2.
	got, cmd := pressAction(t, m, keyRune('r'))
	if got.modal != inputConfirmRestart {
		t.Fatalf("r on a wedged session should enter the restart confirmation, got mode %v", got.modal)
	}
	if cmd != nil {
		t.Fatalf("entering restart confirmation must not dispatch a Restart frame yet, got %v", cmd)
	}
	if got.confirmRestartTarget != "alpha" {
		t.Fatalf("confirmRestartTarget = %q, want alpha", got.confirmRestartTarget)
	}
	if got.confirmRestartWorktree != wedged.WorktreePath {
		t.Fatalf("confirmRestartWorktree = %q, want %q", got.confirmRestartWorktree, wedged.WorktreePath)
	}
	if frame := render(got); !strings.Contains(frame, "Restart session") || !strings.Contains(frame, `Restart "alpha"?`) {
		t.Fatalf("entering the restart confirmation should show its dialog:\n%s", frame)
	}
}

// confirmRestart drives `r` then `y` on the session at the cursor, mirroring
// confirmClean, and returns the post-confirmation Model plus the dispatched
// Cmd.
func confirmRestart(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, _ = pressAction(t, m, keyRune('r'))
	if m.modal != inputConfirmRestart {
		t.Fatalf("setup: r should have entered the restart confirmation, got %v", m.modal)
	}
	m, cmd := update(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("setup: y should have dispatched a restart cmd")
	}
	return m, cmd
}

// TestConfirmRestartYDispatchesExactlyOneRestartFrame is the "confirming
// sends exactly one Restart frame" AND "the send happens off the synchronous
// Update path" acceptance rows together: the fake records zero calls right
// after Update returns (proving the dial/write never ran on the Update
// goroutine), and exactly one call, with the right worktree and name, once
// the returned Cmd is actually invoked.
func TestConfirmRestartYDispatchesExactlyOneRestartFrame(t *testing.T) {
	wedged := wedgedSess("alpha", 12*time.Minute)
	m := testModel(wedged)
	var calls int
	var gotWorktree, gotName string
	m.deps.SendRestart = func(worktree, name string) (bool, error) {
		calls++
		gotWorktree, gotName = worktree, name
		return false, nil
	}

	got, cmd := confirmRestart(t, m)
	if got.modal != inputNormal {
		t.Fatalf("y should return to normal mode, got %v", got.modal)
	}
	if got.confirmRestartTarget != "" || got.confirmRestartWorktree != "" {
		t.Fatalf("y should clear confirmRestartTarget/confirmRestartWorktree, got %q/%q",
			got.confirmRestartTarget, got.confirmRestartWorktree)
	}
	if calls != 0 {
		t.Fatalf("SendRestart must not run on the synchronous Update path, but it was already called %d time(s)", calls)
	}

	msg, ok := cmd().(restartChildRequestedMsg)
	if !ok {
		t.Fatalf("the restart cmd produced %T, want restartChildRequestedMsg", cmd())
	}
	if calls != 1 {
		t.Fatalf("SendRestart call count = %d, want exactly 1", calls)
	}
	if gotWorktree != wedged.WorktreePath || gotName != "alpha" {
		t.Fatalf("SendRestart got (%q, %q), want (%q, alpha)", gotWorktree, gotName, wedged.WorktreePath)
	}
	if msg.name != "alpha" || msg.err != nil {
		t.Fatalf("restartChildRequestedMsg = %+v, want name=alpha with no error", msg)
	}
}

// TestConfirmRestartYClosesAnAlreadyOpenPaneOnTheTarget proves the dashboard
// detaches its own view of the target BEFORE dialing to send the frame,
// rather than leaving that pane to be silently stolen by the new connection
// (see updateConfirmRestart's doc comment).
func TestConfirmRestartYClosesAnAlreadyOpenPaneOnTheTarget(t *testing.T) {
	wedged := wedgedSess("alpha", 12*time.Minute)
	m := testModel(wedged)
	fp := newFakePane("wedged screen")
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "alpha"})
	if m.pane == nil || m.paneName != "alpha" {
		t.Fatalf("setup: pane should be attached to alpha")
	}
	m.deps.SendRestart = func(string, string) (bool, error) { return false, nil }

	got, cmd := confirmRestart(t, m)
	if cmd == nil {
		t.Fatalf("y should still dispatch a restart cmd")
	}
	if fp.closed != 1 {
		t.Fatalf("the dashboard's own pane on the target should be closed exactly once, got %d", fp.closed)
	}
	if got.pane != nil || got.paneName != "" {
		t.Fatalf("closing the pane should clear pane/paneName, got pane=%v paneName=%q", got.pane, got.paneName)
	}
}

// TestConfirmRestartNCancelsWithoutSending is the "declining sends nothing"
// acceptance row (the n/N half).
func TestConfirmRestartNCancelsWithoutSending(t *testing.T) {
	for _, r := range []rune{'n', 'N'} {
		t.Run(string(r), func(t *testing.T) {
			m := testModel(wedgedSess("alpha", 12*time.Minute))
			var calls int
			m.deps.SendRestart = func(string, string) (bool, error) { calls++; return false, nil }
			m, _ = pressAction(t, m, keyRune('r'))

			got, cmd := update(t, m, keyRune(r))
			if got.modal != inputNormal {
				t.Fatalf("%q should return to normal mode, got %v", r, got.modal)
			}
			if cmd != nil {
				t.Fatalf("%q should not dispatch a cmd, got %v", r, cmd)
			}
			if got.confirmRestartTarget != "" || got.confirmRestartWorktree != "" {
				t.Fatalf("%q should clear confirmRestartTarget/confirmRestartWorktree, got %q/%q",
					r, got.confirmRestartTarget, got.confirmRestartWorktree)
			}
			if calls != 0 {
				t.Fatalf("declining must send nothing, but SendRestart was called %d time(s)", calls)
			}
		})
	}
}

// TestConfirmRestartEscCancels is the "declining sends nothing" row's esc
// variant.
func TestConfirmRestartEscCancels(t *testing.T) {
	m := testModel(wedgedSess("alpha", 12*time.Minute))
	var calls int
	m.deps.SendRestart = func(string, string) (bool, error) { calls++; return false, nil }
	m, _ = pressAction(t, m, keyRune('r'))

	got, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got.modal != inputNormal {
		t.Fatalf("esc should return to normal mode, got %v", got.modal)
	}
	if cmd != nil {
		t.Fatalf("esc cancel should return no cmd")
	}
	if calls != 0 {
		t.Fatalf("esc must send nothing, but SendRestart was called %d time(s)", calls)
	}
}

// TestConfirmRestartCtrlCQuits proves the universal escape hatch works
// inside this modal too.
func TestConfirmRestartCtrlCQuits(t *testing.T) {
	m := testModel(wedgedSess("alpha", 12*time.Minute))
	m, _ = pressAction(t, m, keyRune('r'))

	got, cmd := update(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !got.quitting {
		t.Fatalf("ctrl+c in the restart confirmation must quit")
	}
	if cmd == nil {
		t.Fatalf("ctrl+c should return a tea.Quit cmd")
	}
}

// TestConfirmRestartIgnoresUnrecognizedKeys mirrors
// TestConfirmCleanIgnoresUnrecognizedKeys: any key other than y/Y/n/N/esc/
// ctrl+c leaves the modal open with no Cmd dispatched.
func TestConfirmRestartIgnoresUnrecognizedKeys(t *testing.T) {
	for _, k := range []tea.KeyMsg{keyEnter, keyRune('x'), keyRune('z'), keyUp, keyDown} {
		t.Run(fmt.Sprintf("%v", k), func(t *testing.T) {
			m := testModel(wedgedSess("alpha", 12*time.Minute))
			m, _ = pressAction(t, m, keyRune('r'))

			got, cmd := update(t, m, k)
			if got.modal != inputConfirmRestart {
				t.Fatalf("unrecognized key %v should leave the restart modal open, got mode %v", k, got.modal)
			}
			if cmd != nil {
				t.Fatalf("unrecognized key %v should not dispatch a cmd, got %v", k, cmd)
			}
			if got.confirmRestartTarget != "alpha" {
				t.Fatalf("unrecognized key %v should not clear confirmRestartTarget, got %q", k, got.confirmRestartTarget)
			}
		})
	}
}

// TestRestartChildRequestedReportsOutcome is the "Report the outcome
// (restart requested ... or failed) in the dashboard's status area"
// acceptance row.
func TestRestartChildRequestedReportsOutcome(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		m := testModel(wedgedSess("alpha", 12*time.Minute))
		got, cmd := update(t, m, restartChildRequestedMsg{name: "alpha"})
		if got.status == "" {
			t.Fatalf("a successful restart request must report an outcome in the status line")
		}
		if !strings.Contains(got.status, "alpha") {
			t.Fatalf("status should name the session, got %q", got.status)
		}
		if cmd == nil {
			t.Fatalf("the outcome should refresh discovery")
		}
	})

	t.Run("failure", func(t *testing.T) {
		m := testModel(wedgedSess("alpha", 12*time.Minute))
		failErr := errors.New("dial unix ...: connection refused")
		got, cmd := update(t, m, restartChildRequestedMsg{name: "alpha", err: failErr})
		if !strings.Contains(got.status, "alpha") || !strings.Contains(got.status, failErr.Error()) {
			t.Fatalf("failure status should name the session and the error, got %q", got.status)
		}
		if cmd == nil {
			t.Fatalf("the outcome should refresh discovery even on failure")
		}
	})
}

// TestUnfocusKeyStillWorksOnAWedgedPane is the "the unfocus key still works
// while a pane is wedged" acceptance row: adding the restart key must not
// disturb ctrl+\'s existing precedence over a focused, wedged pane.
func TestUnfocusKeyStillWorksOnAWedgedPane(t *testing.T) {
	fp := newFakePane("wedged screen")
	m := testModel(wedgedSess("alpha", 12*time.Minute))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "alpha"})
	m, _ = update(t, m, keyEnter)
	if !m.paneOwnsKeyboard() {
		t.Fatalf("setup: pane should be focused after pressing f")
	}

	got, cmd := pressAction(t, m, keyEscMsg())
	if got.paneOwnsKeyboard() {
		t.Fatalf("ctrl+\\ should still detach focus back to the dashboard even on a wedged pane")
	}
	if cmd != nil {
		t.Fatalf("detach is pure state, expected nil cmd")
	}
	if len(fp.keys) != 0 || len(fp.texts) != 0 {
		t.Fatalf("the escape chord must not reach the child: keys=%v texts=%v", fp.keys, fp.texts)
	}
	if got.pane == nil {
		t.Fatalf("detaching must not close the pane; the session keeps running")
	}
}

// TestConfirmRestartYRevalidatesTheWedgeVerdict is fix cycle 1's answer to
// the confirm-race finding: the 2s discovery ticker keeps running under the
// y/n prompt, so a session can recover (or vanish) between the keypress and
// the confirm. `y` must re-check the CURRENT snapshot with the same
// showsWedged predicate that gated entry, and send nothing when the verdict
// no longer holds — a healthy child must never be signal-laddered off a
// stale prompt. The still-wedged row proves the re-check reads the refreshed
// snapshot rather than cancelling after any reload.
func TestConfirmRestartYRevalidatesTheWedgeVerdict(t *testing.T) {
	t.Run("recovered while the prompt was open", func(t *testing.T) {
		m := testModel(wedgedSess("alpha", 12*time.Minute))
		var calls int
		m.deps.SendRestart = func(string, string) (bool, error) { calls++; return false, nil }
		m, _ = pressAction(t, m, keyRune('r'))
		if m.modal != inputConfirmRestart {
			t.Fatalf("setup: r should have entered the restart confirmation, got %v", m.modal)
		}

		m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{runningSess("alpha")}, at: time.Unix(1_000_000, 0)})

		got, cmd := update(t, m, keyRune('y'))
		if got.modal != inputNormal {
			t.Fatalf("y should still close the modal, got mode %v", got.modal)
		}
		if cmd != nil {
			t.Fatalf("confirming a restart for a session that recovered must dispatch nothing, got a cmd")
		}
		if calls != 0 {
			t.Fatalf("SendRestart must not run for a recovered session, called %d time(s)", calls)
		}
		if !strings.Contains(got.status, "alpha") {
			t.Fatalf("the cancelled restart must explain itself in the status line, got %q", got.status)
		}
	})

	t.Run("cleaned away while the prompt was open", func(t *testing.T) {
		m := testModel(wedgedSess("alpha", 12*time.Minute))
		var calls int
		m.deps.SendRestart = func(string, string) (bool, error) { calls++; return false, nil }
		m, _ = pressAction(t, m, keyRune('r'))

		m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: nil, at: time.Unix(1_000_000, 0)})

		got, cmd := update(t, m, keyRune('y'))
		if cmd != nil || calls != 0 {
			t.Fatalf("confirming a restart for a session that no longer exists must send nothing (cmd=%v calls=%d)", cmd != nil, calls)
		}
		if got.modal != inputNormal {
			t.Fatalf("y should close the modal even when the target is gone, got %v", got.modal)
		}
	})

	t.Run("still wedged after a reload sends", func(t *testing.T) {
		m := testModel(wedgedSess("alpha", 12*time.Minute))
		var calls int
		m.deps.SendRestart = func(string, string) (bool, error) { calls++; return false, nil }
		m, _ = pressAction(t, m, keyRune('r'))

		m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{wedgedSess("alpha", 13*time.Minute)}, at: time.Unix(1_000_000, 0)})

		_, cmd := update(t, m, keyRune('y'))
		if cmd == nil {
			t.Fatalf("a session still wedged in the refreshed snapshot must still be restartable")
		}
		if _, ok := cmd().(restartChildRequestedMsg); !ok {
			t.Fatalf("y on a still-wedged session should dispatch the restart cmd")
		}
		if calls != 1 {
			t.Fatalf("SendRestart call count = %d, want 1", calls)
		}
	})
}

// TestWedgedBadgeRequiresARunningHost is fix cycle 1's answer to the
// stale-verdict finding: a SIGKILLed host leaves its host.json (and the
// wedged_since inside it) on disk forever, so a verdict is only as live as
// the host that wrote it. With HostRunning false the badge must not render,
// and the restart key must NOT take the wedged path — the Restart frame would
// dial a socket nobody serves. The row is simply stopped, so `r` respawns its
// host (restartSelected's stopped branch), with no confirmation and no frame.
func TestWedgedBadgeRequiresARunningHost(t *testing.T) {
	s := wedgedSess("alpha", 12*time.Minute)
	s.HostRunning = false
	m := testModel(s)
	frames := 0
	m.deps.SendRestart = func(string, string) (bool, error) { frames++; return true, nil }
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		return session.HostProcess{PID: 4242}, nil
	}

	row := m.renderRow(s, false, 60)
	if strings.Contains(row, wedgedGlyph) {
		t.Fatalf("a session whose host is gone must not show a WEDGED badge — its wedged_since is stale evidence from a dead host: %q", row)
	}

	got, cmd := pressAction(t, m, keyRune('r'))
	if got.modal == inputConfirmRestart {
		t.Fatal("the wedged confirmation must not open when the host is gone")
	}
	if cmd == nil {
		t.Fatal("r on a stopped row must restart its host")
	}
	if _, ok := cmd().(hostRestartedMsg); !ok {
		t.Fatal("r on a stopped row must run the host restart, not the wedged child restart")
	}
	if frames != 0 {
		t.Fatalf("SendRestart called %d times, want 0 — no host serves that socket", frames)
	}
}

// TestHelpAdvertisesRestartWhereItActs restores keyMap's "help can never drift
// from handled keys" invariant for the Restart binding: the help line names it
// exactly when `r` acts on the selection — a stopped row it restarts at once, a
// running row the dashboard shows as WEDGED — and never on a healthy running
// row, where the key is inert (the unfocused line is already oversubscribed at
// 80 columns, so an unconditional entry would displace keys that are always
// live). The focused overview applies the same gate.
func TestHelpAdvertisesRestartWhereItActs(t *testing.T) {
	want := "r restart"

	wedged := testModel(wedgedSess("alpha", 12*time.Minute))
	if help := xansi.Strip(wedged.renderHelp()); !strings.Contains(help, want) {
		t.Fatalf("help must list the restart key while the selected session is wedged, got %q", help)
	}

	healthy := testModel(runningSess("alpha"))
	if help := xansi.Strip(healthy.renderHelp()); strings.Contains(help, want) {
		t.Fatalf("help must not advertise the restart key for a healthy selection, got %q", help)
	}
	if help := xansi.Strip(focusOverview(t, healthy).renderHelp()); strings.Contains(help, want) {
		t.Fatalf("the focused overview must not advertise the restart key for a healthy selection, got %q", help)
	}

	deadHost := wedgedSess("alpha", 12*time.Minute)
	deadHost.HostRunning = false
	stopped := testModel(deadHost)
	if help := xansi.Strip(stopped.renderHelp()); !strings.Contains(help, want) {
		t.Fatalf("help must advertise the restart key on a stopped selection, got %q", help)
	}
	stopped, _ = update(t, stopped, keyEnter)
	if help := xansi.Strip(stopped.renderHelp()); !strings.Contains(help, want) {
		t.Fatalf("the stopped row's focused overview must advertise the restart key, got %q", help)
	}

	pending := testModel(Session{SessionState: sampleState("alpha"), Pending: true})
	if help := xansi.Strip(pending.renderHelp()); strings.Contains(help, want) {
		t.Fatalf("help must not advertise the restart key on a pending row, got %q", help)
	}
}

// TestHelpToggleShowsFullHelp pins `?` since W3.T1 of T16.2: it opens the
// keys overlay over the Project (the path line is on screen), the footer
// stays one line rather than growing a full help, and a second `?` closes
// the overlay. The footer row is load-bearing: the old full help was one row
// per binding, and nothing may bring that back now the overlay lists keys.
func TestHelpToggleShowsFullHelp(t *testing.T) {
	m := testModel(runningSess("live"))
	m = pressKey(t, m, keyRune('?'))
	if !m.keysOpen {
		t.Fatal("`?` did not open the keys overlay")
	}
	if got := xansi.Strip(m.View().Content); !strings.Contains(got, keysPath) {
		t.Fatalf("the keys overlay is not on screen:\n%s", got)
	}
	if rows := lipgloss.Height(m.renderFooter()); rows != 1 {
		t.Fatalf("the footer is %d rows with the overlay open, want 1", rows)
	}
	m, _ = update(t, m, keyRune('?'))
	if m.keysOpen {
		t.Fatal("a second `?` did not close the keys overlay")
	}
}

// TestHelpToggleOnlyFiresInNormalMode pins the overlay gate decideKey applies
// to `?` (levels.go): an open overlay gets every key first, and the `:`
// search has free text to type ("?" is a legitimate query character), so `?`
// must reach the COMPONENT there, not toggle a view out from under whatever
// the user is typing.
func TestHelpToggleOnlyFiresInNormalMode(t *testing.T) {
	m := testModel(sess("alpha"))
	m = pressKey(t, m, keyRune(':'))
	if !m.leaderOpen {
		t.Fatal("precondition: `:` should open the actions search")
	}

	m, _ = update(t, m, keyRune('?'))
	if m.keysOpen {
		t.Fatal("`?` opened the keys overlay while the search owns the keyboard")
	}
	if got := m.leader.query; got != "?" {
		t.Fatalf("`?` should have been typed into the search's query, got %q", got)
	}
}

// fullHelpHasItem reports whether rendered (a FullHelpView column, or
// anything else help.Model produced) names a binding with the given key and
// description. It checks LINE BY LINE rather than the whole blob for one
// substring: FullHelpView renders one binding per line but column-ALIGNS the
// key and description with padding (lipgloss.JoinHorizontal on each of the
// key and description sub-columns), so "f focus pane" (a single space) is
// not a substring of the padded "f      focus pane" the real renderer
// produces — verified against bubbles/v2@v2.2.1's FullHelpView source.
// ShortHelpView needs no such helper: it joins Key+" "+Desc literally with no
// padding, so the exact-substring checks elsewhere in this file are correct
// as written.
func fullHelpHasItem(rendered, key, desc string) bool {
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, key) && strings.Contains(line, desc) {
			return true
		}
	}
	return false
}

// TestFullHelpOmitsNoEnabledBinding is the completeness test the acceptance
// criteria asks for: walk every mode's registered keymap (mode.go) — which,
// per W6.T1, already folds in the keys list.Model owns on the session list —
// and confirm every ENABLED binding's help text is present once that mode's
// help.KeyMap is rendered with ShowAll forced on. A very wide width rules out
// help.Model's own truncation as a confound; the point here is completeness
// of content, not legibility at any particular terminal size.
func TestFullHelpOmitsNoEnabledBinding(t *testing.T) {
	m := testModel(runningSess("live"))
	reg := m.modes()

	for mode := inputMode(0); mode < inputModeCount; mode++ {
		t.Run(mode.String(), func(t *testing.T) {
			km := reg.spec(mode).keys
			rendered := xansi.Strip(renderModeHelp(km, true, 100000))
			for _, b := range km.bindings {
				if !b.Enabled() {
					continue
				}
				if !fullHelpHasItem(rendered, b.Help().Key, b.Help().Desc) {
					t.Errorf("mode %v: full help = %q, missing enabled binding %q/%q",
						mode, rendered, b.Help().Key, b.Help().Desc)
				}
			}
		})
	}
}

// TestNormalModeFullHelpReflectsPaneAndWedgeState is
// TestFullHelpOmitsNoEnabledBinding's complement for the session list
// specifically: that generic test walks mode.go's own keymap, which knows
// nothing about live Model state, so it cannot exercise
// renderNormalHelpKeyMap's dynamic enabling (enter live wherever there is a
// right side, restart only where it acts). This asserts full help tracks it
// in both directions — present when the key does something, absent when it
// would be a no-op. Since W3.T1 of T16.2 there is no full help (`?` opens the
// keys overlay), so this reads the footer's one line. ctrl+t is asserted ABSENT throughout: it is the agent's
// key and the list's full help must never offer it.
func TestNormalModeFullHelpReflectsPaneAndWedgeState(t *testing.T) {
	toggle := m0Toggle()
	noPane := testModel(runningSess("live"))
	full := xansi.Strip(renderModeHelp(noPane.renderNormalHelpKeyMap(), false, 100000))
	if strings.Contains(full, toggle) {
		t.Errorf("no pane: full help = %q, must not advertise %q from the list", full, toggle)
	}
	if !fullHelpHasItem(full, noPane.keys.Focus.Help().Key, noPane.keys.Focus.Help().Desc) {
		t.Errorf("no pane: full help = %q, want it to advertise %q/%q",
			full, noPane.keys.Focus.Help().Key, noPane.keys.Focus.Help().Desc)
	}

	empty := testModel()
	empty.activeProject = -1
	emptyFull := xansi.Strip(renderModeHelp(empty.renderNormalHelpKeyMap(), false, 100000))
	if fullHelpHasItem(emptyFull, empty.keys.Focus.Help().Key, empty.keys.Focus.Help().Desc) {
		t.Errorf("empty state: full help = %q, must not advertise %q/%q",
			emptyFull, empty.keys.Focus.Help().Key, empty.keys.Focus.Help().Desc)
	}

	withPane, _ := update(t, noPane, paneAttachedMsg{pane: newFakePane("X"), name: "live"})
	full = xansi.Strip(renderModeHelp(withPane.renderNormalHelpKeyMap(), false, 100000))
	if !fullHelpHasItem(full, withPane.keys.Focus.Help().Key, withPane.keys.Focus.Help().Desc) {
		t.Errorf("pane open: full help = %q, want it to contain %q/%q",
			full, withPane.keys.Focus.Help().Key, withPane.keys.Focus.Help().Desc)
	}
	if strings.Contains(full, toggle) {
		t.Errorf("pane open: full help = %q, must not advertise %q from the list", full, toggle)
	}

	// Restart is `r` alone at Project since T16.2, offered only where it
	// acts: a wedged selection gets the entry, a healthy one does not.
	wedged := testModel(wedgedSess("alpha", 12*time.Minute))
	full = xansi.Strip(renderModeHelp(wedged.renderNormalHelpKeyMap(), false, 100000))
	if !fullHelpHasItem(full, "r", "restart") {
		t.Errorf("wedged selection: full help = %q, want the r restart entry", full)
	}
	healthy := xansi.Strip(renderModeHelp(testModel(runningSess("alpha")).renderNormalHelpKeyMap(), false, 100000))
	if fullHelpHasItem(healthy, "r", "restart") {
		t.Errorf("healthy selection: full help = %q, must not advertise a restart that does nothing", healthy)
	}
}

// TestRestartOutcomeStatusesAreDistinct pins the three-way contract fix
// cycle 2's blocker restored: W6.T3's What-to-do names requested, restarted
// and failed as separate outcomes, so the status line must say which one
// actually happened — a "requested" that renders identically to "restarted"
// is the fire-and-ignore defect wearing a new message.
func TestRestartOutcomeStatusesAreDistinct(t *testing.T) {
	m := testModel(wedgedSess("alpha", 12*time.Minute))

	requested, _ := update(t, m, restartChildRequestedMsg{name: "alpha"})
	confirmed, _ := update(t, m, restartChildRequestedMsg{name: "alpha", restarted: true})
	failed, _ := update(t, m, restartChildRequestedMsg{name: "alpha", err: errors.New("status.json is not usable (missing)")})

	if !strings.Contains(confirmed.status, "restarted") {
		t.Errorf("confirmed status = %q, want it to say the child was restarted", confirmed.status)
	}
	if !strings.Contains(requested.status, "requested") || strings.Contains(requested.status, "restarted ") {
		t.Errorf("requested status = %q, want it to claim only that the request was sent", requested.status)
	}
	if !strings.Contains(failed.status, "restart failed") || !strings.Contains(failed.status, "not usable") {
		t.Errorf("failed status = %q, want the failure and the host's reason", failed.status)
	}
	if requested.status == confirmed.status || requested.status == failed.status || confirmed.status == failed.status {
		t.Errorf("the three outcomes must render distinctly, got %q / %q / %q", requested.status, confirmed.status, failed.status)
	}
}

// TestConfirmRestartPromptWarnsAboutDisconnectingAttachedClients covers fix
// cycle 2's steal-warning finding: the restart dial takes the session's
// single client slot, silently detaching a `afk attach` in another
// terminal, and the user must learn that BEFORE pressing y, not from a
// confused teammate afterwards.
func TestConfirmRestartPromptWarnsAboutDisconnectingAttachedClients(t *testing.T) {
	m := testModel(wedgedSess("alpha", 12*time.Minute))
	got, _ := pressAction(t, m, keyRune('r'))
	if got.modal != inputConfirmRestart {
		t.Fatalf("setup: r should have entered the restart confirmation, got %v", got.modal)
	}
	if frame := render(got); !strings.Contains(frame, "disconnects any attached client") {
		t.Errorf("dialog should warn that confirming disconnects any attached client:\n%s", frame)
	}
}

// --- label edit prompt (W4.T1) -------------------------------------------

// TestLabelKeyOpensEditFormPrefilled is the "l opens a single-field prompt,
// pre-filled with the current label" acceptance row, covering both the
// unlabeled and already-labeled starting points — load-bearing because a
// prefill bug would only show up on the SECOND edit of an already-labeled
// session, not the first.
func TestLabelKeyOpensEditFormPrefilled(t *testing.T) {
	tests := []struct {
		name      string
		label     string
		wantValue string
	}{
		{"no existing label", "", ""},
		{"existing label", "spike: pty timing", "spike: pty timing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := sess("alpha")
			s.Label = tt.label
			m := testModel(s)

			got, cmd := pressAction(t, m, keyRune('l'))
			if got.modal != inputEditLabel {
				t.Fatalf("l should enter inputEditLabel, got %v", got.modal)
			}
			if cmd == nil {
				t.Fatalf("opening the label form should focus the input (a non-nil Cmd)")
			}
			if got.labelTarget != "alpha" {
				t.Fatalf("labelTarget = %q, want alpha", got.labelTarget)
			}
			if got.labelWorktree != s.WorktreePath {
				t.Fatalf("labelWorktree = %q, want %q", got.labelWorktree, s.WorktreePath)
			}
			if got.labelInput.Value() != tt.wantValue {
				t.Fatalf("labelInput.Value() = %q, want %q", got.labelInput.Value(), tt.wantValue)
			}
		})
	}
}

// TestLabelKeyInertWithNothingSelected mirrors
// TestRestartKeyInertWithNothingSelected: there is no row to label, so the
// key is a total no-op rather than opening an unusable prompt.
func TestLabelKeyInertWithNothingSelected(t *testing.T) {
	got, cmd := pressAction(t, testModel(), keyRune('l'))
	if got.modal != inputNormal || cmd != nil {
		t.Fatalf("l with nothing selected should be a total no-op, got mode=%v cmd=%v", got.modal, cmd)
	}
}

// TestLabelKeyInertOnPendingRow covers the placeholder-row edge: a Pending
// session has no real .afk/{name}/ directory yet (only Name is populated,
// per Session's own doc comment), so there is nothing to write a label
// sidecar into.
func TestLabelKeyInertOnPendingRow(t *testing.T) {
	pending := Session{SessionState: discover.SessionState{Name: "alpha"}, Pending: true}
	got, cmd := pressAction(t, testModel(pending), keyRune('l'))
	if got.modal != inputNormal || cmd != nil {
		t.Fatalf("l on a pending row should be a total no-op, got mode=%v cmd=%v", got.modal, cmd)
	}
}

// TestLabelFormEscCancelsWithoutWriting is the "esc cancels, no write" row.
func TestLabelFormEscCancelsWithoutWriting(t *testing.T) {
	m := testModel(sess("alpha"))
	var calls int
	m.deps.WriteLabel = func(string, string, string) error { calls++; return nil }
	m, _ = pressAction(t, m, keyRune('l'))
	if m.modal != inputEditLabel {
		t.Fatalf("setup: l should have opened the label form, got %v", m.modal)
	}

	got, cmd := update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got.modal != inputNormal {
		t.Fatalf("esc should return to normal mode, got %v", got.modal)
	}
	if cmd != nil {
		t.Fatalf("esc should dispatch nothing, got %v", cmd)
	}
	if got.labelTarget != "" || got.labelWorktree != "" {
		t.Fatalf("esc should clear labelTarget/labelWorktree, got %q/%q", got.labelTarget, got.labelWorktree)
	}
	if calls != 0 {
		t.Fatalf("esc must not write anything, but WriteLabel was called %d time(s)", calls)
	}
}

// TestLabelFormCtrlCQuits mirrors TestConfirmCleanCtrlCQuits /
// TestConfirmRestartCtrlCQuits: ctrl+c is the universal escape hatch even
// mid-edit.
func TestLabelFormCtrlCQuits(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = pressAction(t, m, keyRune('l'))

	got, cmd := update(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !got.quitting {
		t.Fatalf("ctrl+c should quit even while the label form is open")
	}
	if cmd == nil {
		t.Fatalf("ctrl+c should return a tea.Quit cmd")
	}
}

// TestLabelFormTypingUpdatesTheField proves ordinary keys reach the
// textinput rather than being swallowed by the modal's own key switch.
func TestLabelFormTypingUpdatesTheField(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = pressAction(t, m, keyRune('l'))

	for _, r := range []rune("hi") {
		m, _ = update(t, m, keyRune(r))
	}
	if got := m.labelInput.Value(); got != "hi" {
		t.Fatalf("labelInput.Value() = %q, want %q", got, "hi")
	}
}

// TestLabelFormEnterDispatchesWriteOffTheUpdatePath is
// TestConfirmRestartYDispatchesExactlyOneRestartFrame's sibling for the
// label write: WriteLabel must not run on the synchronous Update path (zero
// calls right after Update returns), and running the returned Cmd calls it
// exactly once with the captured worktree/name and the field's current
// value — captured at open time, never a fresh m.selected(), on the same
// reshuffling-tick invariant confirmTarget documents.
func TestLabelFormEnterDispatchesWriteOffTheUpdatePath(t *testing.T) {
	s := sess("alpha")
	m := testModel(s)
	var calls int
	var gotWorktree, gotName, gotLabel string
	m.deps.WriteLabel = func(worktree, name, label string) error {
		calls++
		gotWorktree, gotName, gotLabel = worktree, name, label
		return nil
	}
	m, _ = pressAction(t, m, keyRune('l'))
	for _, r := range []rune("spike") {
		m, _ = update(t, m, keyRune(r))
	}

	got, cmd := update(t, m, keyEnter)
	if got.modal != inputNormal {
		t.Fatalf("enter should return to normal mode, got %v", got.modal)
	}
	if got.labelTarget != "" || got.labelWorktree != "" {
		t.Fatalf("enter should clear labelTarget/labelWorktree, got %q/%q", got.labelTarget, got.labelWorktree)
	}
	if calls != 0 {
		t.Fatalf("WriteLabel must not run on the synchronous Update path, but it was already called %d time(s)", calls)
	}
	if cmd == nil {
		t.Fatalf("enter should dispatch a write cmd")
	}

	msg, ok := cmd().(labelSavedMsg)
	if !ok {
		t.Fatalf("the write cmd produced %T, want labelSavedMsg", cmd())
	}
	if calls != 1 {
		t.Fatalf("WriteLabel call count = %d, want exactly 1", calls)
	}
	if gotWorktree != s.WorktreePath || gotName != "alpha" || gotLabel != "spike" {
		t.Fatalf("WriteLabel got (%q, %q, %q), want (%q, alpha, spike)", gotWorktree, gotName, gotLabel, s.WorktreePath)
	}
	if msg.name != "alpha" || msg.err != nil {
		t.Fatalf("labelSavedMsg = %+v, want name=alpha with no error", msg)
	}
}

// TestLabelSavedMsgSetsStatusAndReloads covers both outcomes of a completed
// write: success reports it and refreshes discovery so the new label
// appears without waiting for the next tick; failure reports the reason and
// still refreshes, since a stale list is worth re-confirming either way.
func TestLabelSavedMsgSetsStatusAndReloads(t *testing.T) {
	m := testModel(sess("alpha"))

	ok, cmd := update(t, m, labelSavedMsg{name: "alpha"})
	if !strings.Contains(ok.status, "saved") || !strings.Contains(ok.status, "alpha") {
		t.Errorf("success status = %q, want it to name the session and say it was saved", ok.status)
	}
	if cmd == nil {
		t.Errorf("a successful save should refresh discovery")
	}

	failed, cmd2 := update(t, m, labelSavedMsg{name: "alpha", err: errors.New("permission denied")})
	if !strings.Contains(failed.status, "failed") || !strings.Contains(failed.status, "permission denied") {
		t.Errorf("failure status = %q, want it to say the save failed and why", failed.status)
	}
	if cmd2 == nil {
		t.Errorf("a failed save should still refresh discovery")
	}
}

// TestRenderRowShowsLabelWithNameStillVisible is the plan's core rendering
// acceptance row: a labeled session's row shows the label as its primary
// text AND the real name is still on screen — dimmed, in the detail pane's
// header — so the on-disk identity is never hidden. The row has one name
// cell and shows only the label; the detail pane is where displayName's
// "never hidden" contract is honoured.
func TestRenderRowShowsLabelWithNameStillVisible(t *testing.T) {
	s := sess("scratch-1")
	s.Label = "spike: pty timing"
	m := testModel(s)

	row := m.renderRow(s, false, 60)
	if !strings.Contains(row, "spike: pty timing") {
		t.Fatalf("row should show the label, got: %q", row)
	}
	if detail := xansi.Strip(m.composeDetailBody(80)); !strings.Contains(detail, "scratch-1") {
		t.Fatalf("detail pane should still show the real name, got: %q", detail)
	}
}

// TestRenderRowFallsBackToNameWithNoLabel pins the "no label" degenerate
// case: with Label unset, the row shows the name exactly once — no
// parenthesized duplicate of a name that was never overridden.
func TestRenderRowFallsBackToNameWithNoLabel(t *testing.T) {
	m := testModel()
	s := sess("session-a")
	row := m.renderRow(s, false, 60)
	if got := strings.Count(row, "session-a"); got != 1 {
		t.Fatalf("expected \"session-a\" to appear exactly once with no label, got %d in: %q", got, row)
	}
}

// TestRenderRowWithLabelNeverExceedsWidth is TestRenderRowNeverExceedsWidth's
// sibling for the label addition: appending the dimmed real name must never
// push a line past its width budget, at any width or selection state.
func TestRenderRowWithLabelNeverExceedsWidth(t *testing.T) {
	longName := "session-a-stacked-with-a-very-long-branch-name-indeed"
	s := sess(longName)
	s.Label = "a fairly long display label that competes for the same budget"

	m := testModel()
	for _, width := range []int{8, 16, 20, 24, 40, 60, 100} {
		for _, selected := range []bool{false, true} {
			row := m.renderRow(s, selected, width)
			if w := maxLineWidth(row); w > width {
				t.Errorf("width=%d selected=%v: labeled row line exceeds width (got %d): %q",
					width, selected, w, row)
			}
		}
	}
}

// TestRenderDetailHeaderShowsLabelAndName is renderDetail's analogue of
// TestRenderRowShowsLabelWithNameStillVisible: the detail header must show
// both the label and the real name.
func TestRenderDetailHeaderShowsLabelAndName(t *testing.T) {
	s := sess("scratch-1")
	s.Label = "spike: pty timing"
	m := testModel(s)

	detail := m.renderDetail(80, 30)
	if !strings.Contains(detail, "spike: pty timing") {
		t.Fatalf("detail header should show the label, got:\n%s", detail)
	}
	if !strings.Contains(detail, "scratch-1") {
		t.Fatalf("detail header should still show the real name, got:\n%s", detail)
	}
}

// TestRenderDetailHeaderNoLabelShowsOnlyName is the detail pane's fallback
// row: with no label, the header shows the name once with no dimmed
// parenthetical echo of itself. Branch is set to something other than the
// name (sampleState defaults Branch to the name, which would otherwise make
// the branch sub-line's own, pre-existing repetition indistinguishable from
// a bug in this task's addition).
//
// It counts within the BODY: the box's top edge also carries the name as its
// title (detailBoxLabels), by design.
func TestRenderDetailHeaderNoLabelShowsOnlyName(t *testing.T) {
	s := sess("session-a")
	s.Branch = "feature/unrelated"
	m := testModel(s)
	body := xansi.Strip(m.composeDetailBody(76))
	if got := strings.Count(body, "session-a"); got != 1 {
		t.Fatalf("expected \"session-a\" to appear exactly once with no label in the header, got %d occurrences:\n%s", got, body)
	}
}

// TestRenderLabelFormBarShowsPromptFieldAndHint covers the footer bar's
// content and its 80-column budget, on the same precedent as
// TestConfirmCleanBarFitsAnEightyColumnTerminal.
func TestRenderLabelFormBarShowsPromptFieldAndHint(t *testing.T) {
	m := testModel(sess("alpha"))
	m.width = 80
	m = pressKey(t, m, keyRune('l'))

	out := m.renderLabelFormBar()
	for _, ln := range strings.Split(out, "\n") {
		if w := lipgloss.Width(ln); w > 80 {
			t.Errorf("label form bar line exceeds 80 columns (got %d): %q", w, ln)
		}
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("label form bar should name the session being labeled, got:\n%s", out)
	}
	if !strings.Contains(out, "enter: save") || !strings.Contains(out, "esc: cancel") {
		t.Fatalf("label form bar should show the key hint, got:\n%s", out)
	}
}

// --- scratch sessions through the restart paths (review-fix pass) ---

// stoppedScratchSess is scratchSess with the host down, which is the exact
// state the restart paths exist for. Kept separate from scratchSess (which
// mirrors a freshly-discovered one-off) so the seam tests read as "stopped,
// therefore restarted".
func stoppedScratchSess(name string) Session {
	return scratchSess(name)
}

// spawnSeamRecorder wires BOTH spawn seams into m and records which one ran:
// the conductor seam failing the test outright is the assertion every case
// below shares, because a scratch session relaunched through it becomes a
// `/afk:go` conductor that writes a status.json, which stops the session
// being a one-off and hands it a different teardown — the blocker this pass
// fixes.
func spawnSeamRecorder(t *testing.T, m *Model) (scratchCalls *int, gotName, gotWorktree *string) {
	t.Helper()
	var calls int
	var name, worktree string
	m.deps.SpawnHost = func(n, w string) (session.HostProcess, error) {
		t.Errorf("SpawnHost (conductor seam) called for a scratch session (%q, %q); want SpawnScratchHost", n, w)
		return session.HostProcess{PID: 4242}, nil
	}
	m.deps.SpawnScratchHost = func(n, w string) (session.HostProcess, error) {
		calls++
		name, worktree = n, w
		return session.HostProcess{PID: 4242}, nil
	}
	return &calls, &name, &worktree
}

// TestRestartKeyOnStoppedScratchSessionRestartsThroughTheScratchSeam is the
// regression test for the rejected blocker: `r` on a stopped scratch row
// must respawn its host with HostSpawn.Scratch set (via the scratch seam),
// never through the conductor seam. Both halves are load-bearing — the
// scratch seam being called with the row's own name/worktree, and the
// conductor seam not being called at all.
func TestRestartKeyOnStoppedScratchSessionRestartsThroughTheScratchSeam(t *testing.T) {
	m := testModel(stoppedScratchSess("scratch-1"))
	calls, gotName, gotWorktree := spawnSeamRecorder(t, &m)

	after, cmd := pressAction(t, m, keyRune('r'))
	if cmd == nil {
		t.Fatal("r on a stopped scratch session should dispatch the restart cmd")
	}
	if !strings.Contains(after.status, "restarting scratch-1") {
		t.Fatalf("status = %q, want restarting notice", after.status)
	}
	msg := cmd()
	if _, ok := msg.(hostRestartedMsg); !ok {
		t.Fatalf("restart cmd produced %T, want hostRestartedMsg", msg)
	}
	if *calls != 1 {
		t.Fatalf("SpawnScratchHost called %d times, want 1", *calls)
	}
	if *gotName != "scratch-1" || *gotWorktree != "/wt/scratch-1" {
		t.Fatalf("SpawnScratchHost called with (%q, %q), want (%q, %q)",
			*gotName, *gotWorktree, "scratch-1", "/wt/scratch-1")
	}
}

// TestStartupPaneOnStoppedScratchSessionRestartsThroughTheScratchSeam covers
// the second restart dispatch site (maybeOpenInitialPane): `afk dash --open
// scratch-1` microseconds after its host stopped must take the same scratch
// seam `r` does.
func TestStartupPaneOnStoppedScratchSessionRestartsThroughTheScratchSeam(t *testing.T) {
	m := testModel(stoppedScratchSess("scratch-1"))
	calls, gotName, _ := spawnSeamRecorder(t, &m)
	m.initialSession = "scratch-1"
	m.firstLoadDone = true

	nm, cmd := m.maybeOpenInitialPane()
	if _, ok := nm.(Model); !ok {
		t.Fatalf("maybeOpenInitialPane returned %T, want ui.Model", nm)
	}
	if cmd == nil {
		t.Fatal("the startup pane on a stopped scratch session should dispatch the restart cmd")
	}
	if msg := cmd(); msg != nil {
		if _, ok := msg.(hostRestartedMsg); !ok {
			t.Fatalf("restart cmd produced %T, want hostRestartedMsg", msg)
		}
	}
	if *calls != 1 || *gotName != "scratch-1" {
		t.Fatalf("SpawnScratchHost calls = %d with name %q, want 1 call for scratch-1", *calls, *gotName)
	}
}

// TestResolveFocusTargetCarriesScratch covers the third restart dispatch site
// at its decision seam: a click-to-focus resolves through focusTarget, and a
// target that loses the Scratch flag on the way would restart through the
// conductor seam no matter what restartHostCmd does. Both rows are
// load-bearing — the active project answers from m.sessions, any other
// project from the scan cache, and each is a separate copy of the flag.
func TestResolveFocusTargetCarriesScratch(t *testing.T) {
	t.Run("active project", func(t *testing.T) {
		m := testModel(stoppedScratchSess("scratch-1"))
		target, ok := m.resolveFocusTarget("", "scratch-1")
		if !ok {
			t.Fatal("resolveFocusTarget should find the scratch session")
		}
		if !target.scratch {
			t.Fatalf("focusTarget.scratch = false for a scratch session: %+v", target)
		}
	})
	t.Run("cached other project", func(t *testing.T) {
		m := testModel()
		m.projectSessions = map[string][]discover.SessionState{
			"/other": {{Name: "scratch-2", WorktreePath: "/other", Scratch: true}},
		}
		target, ok := m.resolveFocusTarget("/other", "scratch-2")
		if !ok {
			t.Fatal("resolveFocusTarget should find the cached scratch session")
		}
		if !target.scratch {
			t.Fatalf("focusTarget.scratch = false for a cached scratch session: %+v", target)
		}
	})
}

// TestRestartWedgedKeyIsInertOnAScratchSession is the regression test for the
// rejected blocker on `r`: a wedged scratch session must never reach the
// restart confirmation, because the host resumes via the session id in
// status.json and a scratch session has no status.json — confirming would
// close the pane and steal the client slot for a restart that cannot succeed.
// The non-scratch control row proves the key is otherwise live under exactly
// the same wedge verdict, so the guard (not a broken keybinding) is what the
// scratch row observes.
func TestRestartWedgedKeyIsInertOnAScratchSession(t *testing.T) {
	wedgedScratch := scratchSess("scratch-1")
	wedgedScratch.HostRunning = true
	wedgedScratch.HostPID = 4242
	wedgedScratch.WedgedSince = time.Unix(1_000_000, 0).Add(-12 * time.Minute).UTC().Format(time.RFC3339)

	t.Run("scratch session refuses with guidance", func(t *testing.T) {
		m := testModel(wedgedScratch)
		got, cmd := pressAction(t, m, keyRune('r'))
		if got.modal == inputConfirmRestart {
			t.Fatal("`r` on a wedged scratch session entered the restart confirmation")
		}
		if cmd != nil {
			t.Fatal("`r` on a wedged scratch session dispatched a Cmd, want none")
		}
		if !strings.Contains(got.status, "one-off") {
			t.Fatalf("status = %q, want it to explain the one-off refusal (the WEDGED badge advertises this key)", got.status)
		}
	})
	t.Run("non-scratch control still confirms", func(t *testing.T) {
		m := testModel(wedgedSess("alpha", 12*time.Minute))
		got, _ := pressAction(t, m, keyRune('r'))
		if got.modal != inputConfirmRestart {
			t.Fatal("`r` on a wedged non-scratch session should still enter the confirmation")
		}
	})
}

// TestCleanSucceededStatusScratchClaimsNoArchive is the fourth scratch-blind
// call site of the class the review rejected (false worktree/branch/archive
// claims for a one-off session), found by the fix pass's own sweep: a scratch
// clean's CleanResult carries only Name (session.cleanScratchSession archives
// nothing), so the success footer must not read "archived to " with a
// dangling empty path. The lifecycle row is load-bearing too — it pins the
// existing archive-first copy so the scratch branch cannot eat it.
func TestCleanSucceededStatusScratchClaimsNoArchive(t *testing.T) {
	t.Run("scratch result names no archive", func(t *testing.T) {
		got := cleanSucceededStatus(session.CleanResult{Name: "scratch-1"})
		if strings.Contains(got, "archived") {
			t.Fatalf("cleanSucceededStatus for a scratch clean claims an archive: %q", got)
		}
		if !strings.Contains(got, "cleaned scratch-1") || !strings.Contains(got, "session directory removed") {
			t.Fatalf("cleanSucceededStatus = %q, want the scratch clean's true consequence", got)
		}
	})
	t.Run("lifecycle result keeps the archive-first copy", func(t *testing.T) {
		got := cleanSucceededStatus(session.CleanResult{
			Name:        "alpha",
			Branch:      "alpha",
			ArchivePath: "/archive/repo-alpha",
		})
		if !strings.Contains(got, "cleaned alpha · archived as repo-alpha") {
			t.Fatalf("cleanSucceededStatus = %q, want the existing archive-first copy preserved", got)
		}
		if !strings.Contains(got, "branch alpha kept") {
			t.Fatalf("cleanSucceededStatus = %q, want the kept-branch note preserved", got)
		}
	})
}

// formPaneCornerModel builds the one state W4.T1 left undefined and W4.T2
// resolved: the creation form open WITH a pane already focused. It is reachable
// only asynchronously — openNewForm deliberately allows an open pane (the create
// is a background spawn), and a focus REQUEST from another process reaches
// applyPaneAttached, which sets paneOwnsKeyboard with no form guard.
func formPaneCornerModel(t *testing.T) (Model, *fakePane) {
	t.Helper()
	m := testModel(sess("a"))
	m = pressKey(t, m, keyRune('n'))
	// The form goes up FIRST and the focused pane arrives underneath it, which
	// is the only order that reaches this state: a focused pane forwards `n` to
	// its child, so the form can never be opened from one.
	fp := newFakePane("X")
	m.focus = focusRight // connect moves the focus at request time
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a", focusOnArrival: true})
	m.pane = fp
	if !m.formOpen || !m.paneOwnsKeyboard() || m.pane == nil {
		t.Fatalf("setup: want the form open over a focused pane, got formOpen=%v paneOwnsKeyboard=%v pane=%v",
			m.formOpen, m.paneOwnsKeyboard(), m.pane != nil)
	}
	return m, fp
}

// TestFormOutranksAFocusedPane pins the ONE deliberate precedence change in the
// move to the unified dispatch (decisions.md, W4.T1: "the corner").
//
// The old if-order ran the pane-focus branch before the form check, so with both
// active every keystroke — the characters the user believed they were typing
// into the form included — was forwarded to a child session that is NOT ON
// SCREEN (View renders the form in the main region whenever formOpen; the pane
// is invisible). Esc went to the child too, which made the form uncancellable.
//
// modePrecedence puts inputForm above inputPane instead, on views.go's stated
// invariant that whatever owns the keyboard must be on screen. Every assertion
// here is load-bearing:
//
//   - the printable key reaching the name buffer and NOT the child is the
//     defect itself: no keystroke may land in a session the user cannot see;
//   - esc cancelling proves the form is escapable again;
//   - the pane resuming afterwards proves nothing is stranded — paneOwnsKeyboard was
//     never cleared, so the child gets the keyboard back the moment the form
//     that outranked it closes.
func TestFormOutranksAFocusedPane(t *testing.T) {
	m, fp := formPaneCornerModel(t)

	m, _ = update(t, m, keyRune('z'))
	if got := m.form.nameInput.Value(); got != "z" {
		t.Errorf("name buffer = %q, want %q — the key went somewhere other than the form on screen", got, "z")
	}
	if len(fp.keys) != 0 || len(fp.texts) != 0 {
		t.Errorf("the child received %d keys and %d texts; a key typed into a visible form must never reach an invisible session",
			len(fp.keys), len(fp.texts))
	}

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.formOpen {
		t.Fatal("esc did not cancel the form — the form is uncancellable, which is the bug this precedence fixes")
	}
	if len(fp.keys) != 0 {
		t.Errorf("esc was forwarded to the child (%d keys), but it belongs to the form", len(fp.keys))
	}

	// The pane layer resumes: paneOwnsKeyboard was never cleared, so with the form
	// gone the child owns the keyboard again.
	if !m.paneOwnsKeyboard() || m.pane == nil {
		t.Fatalf("cancelling the form stranded the pane: paneOwnsKeyboard=%v pane=%v", m.paneOwnsKeyboard(), m.pane != nil)
	}
	m, _ = update(t, m, keyRune('z'))
	if len(fp.keys)+len(fp.texts) == 0 {
		t.Error("the child got nothing after the form closed — the pane layer must resume")
	}
}

// TestPasteFollowsTheModeThatOwnsInput is the non-key half of the same
// invariant. tea.PasteMsg has its own case in Update (v2 delivers a bracketed
// paste as its own message type, so it never reaches the key dispatch), and that
// case used to test the raw pane flags — which under form-wins would paste a
// buffer into an invisible child while the user was looking at a form.
func TestPasteFollowsTheModeThatOwnsInput(t *testing.T) {
	t.Run("focused pane owns input: the paste reaches the child", func(t *testing.T) {
		m := testModel(sess("a"))
		fp := newFakePane("X")
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
		m.pane = fp
		m.focus = focusRight

		m, _ = update(t, m, tea.PasteMsg{Content: "hello"})
		if len(fp.pastes) != 1 || fp.pastes[0] != "hello" {
			t.Errorf("pastes = %v, want the child to receive one \"hello\"", fp.pastes)
		}
	})

	t.Run("form outranks the pane: the paste reaches nothing", func(t *testing.T) {
		m, fp := formPaneCornerModel(t)
		m, _ = update(t, m, tea.PasteMsg{Content: "hello"})
		if len(fp.pastes) != 0 {
			t.Errorf("pastes = %v, want none — the child is not on screen and does not own input", fp.pastes)
		}
	})

	t.Run("a modal outranks the pane: the paste reaches nothing", func(t *testing.T) {
		m := testModel(sess("a"))
		fp := newFakePane("X")
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "sess"})
		m.pane = fp
		m.focus = focusRight
		m.modal = inputSpawnError

		m, _ = update(t, m, tea.PasteMsg{Content: "hello"})
		if len(fp.pastes) != 0 {
			t.Errorf("pastes = %v, want none — the modal owns input", fp.pastes)
		}
	})
}

// --- worktree-side fault rows: identity and inertness -------------------------

// TestCleanTargetKindPromotesEveryIdentityRelevantKind pins the discriminator
// the clean seam matches on, and its deliberate asymmetry.
//
// It is NOT the worktree-fault test any more: KindStatusWithoutWorktree is not
// a worktree fault (its row keeps a pane, a preview and an attach) and is
// still identity-relevant, because a healthy X in its own worktree and a
// stranded `.afk/X/` in the main checkout are two sessions with opposite
// teardowns. The two questions parted ways here; session.Kind answers each
// with its own method and this function must ask IdentityRelevant.
//
// The stale-status row is the load-bearing one, and it is why this is a Kind
// and not a bool. internal/state calls four status.json shapes stale that
// internal/session decodes cleanly (trailing data, a plan_name mismatch, an
// unknown phase, an empty session_id outside `planning`), so the dashboard's
// row says KindStaleStatus for the very same file internal/session reports as
// KindHealthy. Carrying that kind into the identity match would make every one
// of those rows fail to resolve with `no session named %q found` — the exact
// over-refusal dashboardPhase exists to have already fixed once.
func TestCleanTargetKindPromotesEveryIdentityRelevantKind(t *testing.T) {
	tests := []struct {
		name string
		in   session.Kind
		want session.Kind
	}{
		{"healthy stays healthy", session.KindHealthy, session.KindHealthy},
		{"stale status is NOT identity-relevant", session.KindStaleStatus, session.KindHealthy},
		{"missing worktree directory is", session.KindWorktreeMissing, session.KindWorktreeMissing},
		{"worktree without a session is", session.KindWorktreeNoSession, session.KindWorktreeNoSession},
		{
			"a status dir without a worktree of its own is, though it is no fault",
			session.KindStatusWithoutWorktree, session.KindStatusWithoutWorktree,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanTargetKind(tt.in); got != tt.want {
				t.Fatalf("cleanTargetKind(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestFaultRowIsInertForToggleAndAutoPreview pins the two paths that would dial.
//
// There is no host to dial for either fault kind — one worktree's directory is
// gone and the other never had a session — and dialing is the one thing the
// dashboard's never-block contract cannot afford to do on a cursor move. ctrl+t
// must therefore be a no-op with no dial in flight, and schedulePreview must
// arm no timer at all rather than arming one that refuses at fire time.
func TestFaultRowIsInertForToggleAndAutoPreview(t *testing.T) {
	for _, kind := range []session.Kind{session.KindWorktreeMissing, session.KindWorktreeNoSession} {
		t.Run(string(kind), func(t *testing.T) {
			s := faultSess("busted", kind, "worktree is broken")
			// HostRunning true is the hostile setup: a stale host.json naming a
			// recycled PID is exactly how a row claims liveness it does not
			// have, and the refusal must not depend on that field being false.
			s.HostRunning = true
			m := testModel(s)

			got, cmd := update(t, m, keyEnter)
			if cmd != nil {
				t.Errorf("ctrl+t on a %s row dispatched %T, want no cmd", kind, cmd())
			}
			if got.attachInFlight.name != "" {
				t.Errorf("ctrl+t on a %s row left a dial in flight for %q", kind, got.attachInFlight.name)
			}

			pm := m
			if c := pm.schedulePreview(); c != nil {
				t.Errorf("schedulePreview armed a timer for a %s row", kind)
			}
		})
	}
}

// TestStrandedRowIsNotInertForToggleAndAutoPreview is the negative of the test
// above, and the reason KindStatusWithoutWorktree is identity-relevant WITHOUT
// being a worktree fault.
//
// A stranded `.afk/{name}/` is very often a live standalone /afk:go
// session: the carrier worktree is on disk, host.json is real, and the pane is
// the only place the user can see what the session is doing — which is usually
// how they find out why they want to clean it. Broadening isWorktreeFault to
// cover the kind would be the natural-looking way to implement the clean fix,
// and it would go dark on exactly the sessions this vocabulary was extended
// for, so the inertness has to be pinned from the other side too.
func TestStrandedRowIsNotInertForToggleAndAutoPreview(t *testing.T) {
	m := testModel(strandedSess("standalone"))

	got, cmd := update(t, m, keyEnter)
	if cmd == nil {
		t.Fatal("ctrl+t on a stranded row dispatched nothing, want the attach")
	}
	if got.attachInFlight.name != "standalone" {
		t.Fatalf("attachInFlight = %+v, want a dial for the stranded row", got.attachInFlight)
	}

	pm := m
	if c := pm.schedulePreview(); c == nil {
		t.Fatal("schedulePreview armed no timer for a stranded row")
	}
}

// TestResolveCleanTargetMatchesFaultKind is the dup-name coin flip, in the
// shape broken entries introduce.
//
// Once a worktree-side fault joins the sessions slice, one name can resolve to
// TWO non-scratch entries whose phases both read "" — the healthy-but-stale
// session and the fault. First-match-by-name then cleans whichever sortSessions
// happened to order first, and the two need opposite teardowns. The kind is the
// only thing that tells them apart, which is why the confirming modal captures
// it alongside the phase.
//
// The last row is the regression the plan calls out by name: a stale row must
// keep resolving exactly as it does today, because its dashboard kind and its
// internal/session kind legitimately disagree for the same file.
func TestResolveCleanTargetMatchesFaultKind(t *testing.T) {
	discovered := []session.Session{
		{
			Name:         "twin",
			WorktreePath: "/wt/twin",
			Branch:       "twin",
			StatusPath:   "/wt/twin/.afk/twin/status.json",
		},
		{
			Name:         "twin",
			WorktreePath: "/wt/twin-gone",
			Branch:       "twin",
			Kind:         session.KindWorktreeMissing,
		},
		{
			Name:         "stale-one",
			WorktreePath: "/wt/stale-one",
			Branch:       "stale-one",
			StatusPath:   "/wt/stale-one/.afk/stale-one/status.json",
			Kind:         session.KindStaleStatus,
		},
	}
	staleFresh := func(session.Session) string { return "" }

	tests := []struct {
		name         string
		target       string
		kind         session.Kind
		wantWorktree string
		wantErr      bool
	}{
		{
			name:         "a confirmed BROKEN row resolves the fault entry, not the healthy twin",
			target:       "twin",
			kind:         session.KindWorktreeMissing,
			wantWorktree: "/wt/twin-gone",
		},
		{
			name:         "a confirmed healthy row resolves the healthy twin, not the fault entry",
			target:       "twin",
			kind:         session.KindHealthy,
			wantWorktree: "/wt/twin",
		},
		{
			name:    "a confirmed BROKEN row refuses a name with no fault entry",
			target:  "stale-one",
			kind:    session.KindWorktreeMissing,
			wantErr: true,
		},
		{
			name:         "a stale row still resolves under the healthy kind",
			target:       "stale-one",
			kind:         session.KindHealthy,
			wantWorktree: "/wt/stale-one",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCleanTarget(discovered, tt.target, "", false, tt.kind, staleFresh)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveCleanTarget(%q, kind=%q) = %+v, want a refusal", tt.target, tt.kind, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCleanTarget(%q, kind=%q) failed: %v", tt.target, tt.kind, err)
			}
			if got == nil || got.WorktreePath != tt.wantWorktree {
				t.Fatalf("resolved %+v, want the entry at %q", got, tt.wantWorktree)
			}
		})
	}
}

// TestResolveCleanTargetStatusWithoutWorktree is the same dup-name coin flip in
// the shape that actually costs the user something.
//
// Both entries here are non-scratch and BOTH carry phase `done` — a stranded
// `.afk/dup/` in the primary checkout is very often a live standalone
// /afk:go session, so unlike a worktree fault it has a real, matching phase
// and the fresh-phase gate cannot separate them either. The kind is the only
// discriminator, and getting it wrong is not a cosmetic error: the stranded
// entry's WorktreePath is the user's own checkout, so resolving the healthy
// confirmation to it would `git add -A && git commit` there.
//
// Both directions are asserted because only one of them fails on a
// half-applied fix: matching the kind but neglecting to promote it in
// cleanTargetKind resolves BOTH confirmations to whichever entry the slice
// orders first.
func TestResolveCleanTargetStatusWithoutWorktree(t *testing.T) {
	discovered := []session.Session{
		{
			Name:         "dup",
			WorktreePath: "/wt/dup",
			Branch:       "dup",
			StatusPath:   "/wt/dup/.afk/dup/status.json",
			Status:       session.Status{Phase: "done"},
		},
		{
			Name:         "dup",
			WorktreePath: "/repo",
			Branch:       "",
			Kind:         session.KindStatusWithoutWorktree,
			StatusPath:   "/repo/.afk/dup/status.json",
			Status:       session.Status{Phase: "done"},
			Reason:       `session "dup" has its status file under worktree /repo, which is checked out on branch "main", not "dup"`,
		},
	}
	freshDone := func(session.Session) string { return "done" }

	for _, tt := range []struct {
		name         string
		kind         session.Kind
		wantWorktree string
		wantBranch   string
	}{
		{
			name:         "a stranded confirmation resolves the carrier entry, not the healthy twin",
			kind:         session.KindStatusWithoutWorktree,
			wantWorktree: "/repo",
		},
		{
			name:         "a healthy confirmation still resolves the healthy twin",
			kind:         session.KindHealthy,
			wantWorktree: "/wt/dup",
			wantBranch:   "dup",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCleanTarget(discovered, "dup", "done", false, tt.kind, freshDone)
			if err != nil {
				t.Fatalf("resolveCleanTarget(dup, kind=%q) failed: %v", tt.kind, err)
			}
			if got.WorktreePath != tt.wantWorktree {
				t.Fatalf("resolved the entry at %q, want %q", got.WorktreePath, tt.wantWorktree)
			}
			// The empty Branch is what makes deleteSessionBranch a no-op on the
			// stranded route, so it has to survive resolution rather than be
			// filled in from the row.
			if got.Branch != tt.wantBranch {
				t.Fatalf("resolved Branch = %q, want %q", got.Branch, tt.wantBranch)
			}
			if got.Kind != tt.kind {
				t.Fatalf("resolved Kind = %q, want %q — the kind is what routes the teardown", got.Kind, tt.kind)
			}
		})
	}
}

// TestConfirmCleanCarriesFaultKindToSeam pins that the kind the user CONFIRMED
// travels from the row, through the modal's captured state, to the
// Deps.CleanSession seam — the half of the identity match a rendering-only
// change silently misses. The seam's default resolves the target with it
// (resolveCleanTarget), so a dropped kind here reopens the dup-name coin flip
// no matter how correct the matching function is.
func TestConfirmCleanCarriesFaultKindToSeam(t *testing.T) {
	for _, tt := range []struct {
		name string
		row  Session
		want session.Kind
	}{
		{"BROKEN row", faultSess("twin", session.KindWorktreeMissing, "worktree directory no longer exists"), session.KindWorktreeMissing},
		{"ORPHAN row", faultSess("twin", session.KindWorktreeNoSession, "worktree has no afk session"), session.KindWorktreeNoSession},
		{"healthy row", sess("twin"), session.KindHealthy},
		{"stale row confirms as healthy", staleSess("twin"), session.KindHealthy},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(tt.row)
			var gotName string
			var gotKind session.Kind
			m.deps.CleanSession = func(_, name, _ string, _ bool, kind session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
				gotName, gotKind = name, kind
				return session.CleanResult{Name: name}, nil
			}
			m, _ = pressAction(t, m, keyRune('c'))
			if m.modal != inputConfirmClean {
				t.Fatalf("setup: expected confirm mode, got %v", m.modal)
			}
			if m.confirmKind != tt.want {
				t.Fatalf("confirmKind = %q, want %q", m.confirmKind, tt.want)
			}
			m, cmd := update(t, m, keyRune('y'))
			if cmd == nil {
				t.Fatal("y should dispatch the clean cmd")
			}
			cmd()
			if gotName != "twin" || gotKind != tt.want {
				t.Fatalf("CleanSession got (%q, kind=%q), want (twin, kind=%q)", gotName, gotKind, tt.want)
			}
			if m.confirmKind != session.KindHealthy {
				t.Fatalf("confirmKind = %q after dispatch, want it cleared", m.confirmKind)
			}
		})
	}
}

// TestConfirmCleanKindClearsOnEveryExit is confirmKind's leak guard, the exact
// shape TestConfirmCleanScratchStateClearsOnEveryExit already pins for
// confirmScratch: a kind left set after a cancelled modal would be applied to
// the NEXT session the user confirms, and a healthy session cleaned under
// KindWorktreeMissing resolves to nothing at all.
func TestConfirmCleanKindClearsOnEveryExit(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"esc", tea.KeyPressMsg{Code: tea.KeyEsc}},
		{"n", keyRune('n')},
		{"N", keyRune('N')},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(faultSess("busted", session.KindWorktreeMissing, "gone"))
			m, _ = pressAction(t, m, keyRune('c'))
			if m.confirmKind != session.KindWorktreeMissing {
				t.Fatalf("setup: confirmKind = %q, want %q", m.confirmKind, session.KindWorktreeMissing)
			}
			m, _ = update(t, m, tt.key)
			if m.confirmKind != session.KindHealthy {
				t.Fatalf("confirmKind = %q after %s, want it cleared", m.confirmKind, tt.name)
			}
		})
	}
}

// TestCleanRunnerResolvesAFaultRowEndToEnd is the acceptance criterion stated
// as a test: `c` then `y` on a BROKEN row must complete the teardown rather
// than fail with `no session named %q found`.
//
// It exercises the whole seam body (cleanRunner.run), not just the matcher, so
// the kind has to survive every hop: the modal's captured state, the Cmd, the
// Discover call, and resolveCleanTarget. The stale row rides along because it
// is the case the same discriminator must NOT break.
func TestCleanRunnerResolvesAFaultRowEndToEnd(t *testing.T) {
	discovered := []session.Session{
		{
			Name:         "busted",
			WorktreePath: "/wt/busted",
			Branch:       "busted",
			Kind:         session.KindWorktreeMissing,
		},
		{
			Name:         "stale-one",
			WorktreePath: "/wt/stale-one",
			Branch:       "stale-one",
			StatusPath:   "/wt/stale-one/.afk/stale-one/status.json",
			Kind:         session.KindStaleStatus,
		},
	}

	for _, tt := range []struct {
		name         string
		target       string
		kind         session.Kind
		wantWorktree string
	}{
		{"BROKEN row", "busted", session.KindWorktreeMissing, "/wt/busted"},
		{"stale row", "stale-one", session.KindHealthy, "/wt/stale-one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var cleaned session.Session
			r := cleanRunner{
				discover: func(string, session.CleanDeps) ([]session.Session, error) {
					return discovered, nil
				},
				// Every row here confirms with an empty phase, and the fresh
				// read agrees: a fault entry has no status.json at all and a
				// stale one has an unreadable one.
				freshPhase: func(session.Session) string { return "" },
				clean: func(_ string, sess session.Session, _ session.CleanDeps, _ session.CleanOptions) (session.CleanResult, error) {
					cleaned = sess
					return session.CleanResult{Name: sess.Name}, nil
				},
			}

			res, err := r.run("/repo", tt.target, "", false, tt.kind, session.CleanOptions{})
			if err != nil {
				t.Fatalf("run(%q, kind=%q) failed: %v", tt.target, tt.kind, err)
			}
			if res.Name != tt.target {
				t.Fatalf("CleanResult.Name = %q, want %q", res.Name, tt.target)
			}
			if cleaned.WorktreePath != tt.wantWorktree {
				t.Fatalf("cleaned the entry at %q, want %q", cleaned.WorktreePath, tt.wantWorktree)
			}
		})
	}
}

// TestCleanSucceededStatusFaultRouteClaimsNoSessionDirectory is the fifth call
// site of the class TestCleanSucceededStatusScratchClaimsNoArchive names: a
// footer making a claim that is false for the route that just ran.
//
// Both fault routes report an EMPTY ArchivePath, which until this row landed
// in the same branch as a scratch clean and read "session directory removed" —
// false for a missing worktree, whose `.afk/{name}/` died with the directory
// and was never touched (internal/session reports exactly that on
// ArchiveSkipped). The scratch row rides along because it is the branch this
// one splits off from and must leave byte-identical.
func TestCleanSucceededStatusFaultRouteClaimsNoSessionDirectory(t *testing.T) {
	t.Run("skipped archive says what actually happened", func(t *testing.T) {
		got := cleanSucceededStatus(session.CleanResult{
			Name:           "busted",
			Branch:         "busted",
			Kind:           session.KindWorktreeMissing,
			ArchiveSkipped: true,
		})
		if strings.Contains(got, "session directory removed") {
			t.Fatalf("cleanSucceededStatus = %q, want no claim about a directory that was already gone", got)
		}
		if strings.Contains(got, "archived") {
			t.Fatalf("cleanSucceededStatus = %q, must not claim an archive it skipped", got)
		}
		if !strings.Contains(got, "cleaned busted") || !strings.Contains(got, "worktree removed") {
			t.Fatalf("cleanSucceededStatus = %q, want the worktree teardown reported", got)
		}
		if !strings.Contains(got, "branch busted kept") {
			t.Fatalf("cleanSucceededStatus = %q, want the kept-branch note", got)
		}
	})
	t.Run("scratch result is unchanged", func(t *testing.T) {
		got := cleanSucceededStatus(session.CleanResult{Name: "scratch-1"})
		if !strings.Contains(got, "cleaned scratch-1") || !strings.Contains(got, "session directory removed") {
			t.Fatalf("cleanSucceededStatus = %q, want the scratch copy untouched", got)
		}
	})
	// The stranded route is the fourth teardown this one footer reports, and
	// the only one whose CleanResult has an archive path AND no worktree to
	// claim: it archives `.afk/{name}/`, removes it, and runs no git. The
	// empty Branch is what keeps branchOutcome silent — internal/session
	// leaves it empty for the kind precisely because there is no branch of
	// its own — so this row is also the pin on that field surviving the hop.
	t.Run("stranded route names the archive and no worktree", func(t *testing.T) {
		got := cleanSucceededStatus(session.CleanResult{
			Name:        "standalone",
			Kind:        session.KindStatusWithoutWorktree,
			ArchivePath: "/arch/standalone",
		})
		if !strings.Contains(got, "cleaned standalone") || !strings.Contains(got, "archived as standalone") {
			t.Fatalf("cleanSucceededStatus = %q, want the archive path named", got)
		}
		for _, banned := range []string{"worktree", "branch"} {
			if strings.Contains(got, banned) {
				t.Fatalf("cleanSucceededStatus = %q, must not mention %q on a route that touches neither", got, banned)
			}
		}
	})
}

// TestNonKeyMessagesFollowTheModeThatOwnsInput pins Update's default case on the
// same derived mode the key path uses, which is the other half of unifying mode
// ownership: the components' own blink traffic must follow the keyboard, or a
// surface that no longer owns input keeps animating a cursor.
//
// tea.FocusMsg is the probe because it is an EXPORTED message a test can
// construct that a focused bubbles component demonstrably acts on: textinput's
// Update forwards it to its virtual cursor, whose tea.FocusMsg case returns a
// blink Cmd (bubbles/v2 cursor.go:126-127). A non-nil Cmd back therefore means
// the message reached that component; a nil one means it did not.
func TestNonKeyMessagesFollowTheModeThatOwnsInput(t *testing.T) {
	t.Run("the form owns input: its focused field is fed", func(t *testing.T) {
		m := testModel(sess("a"))
		m = pressKey(t, m, keyRune('n'))
		if _, cmd := update(t, m, tea.FocusMsg{}); cmd == nil {
			t.Error("the open form's focused field got nothing")
		}
	})

	t.Run("a modal outranks an open form: the form is not fed", func(t *testing.T) {
		m := testModel(sess("a"))
		m = pressKey(t, m, keyRune('n'))
		if !m.formOpen {
			t.Fatal("setup: ctrl+] then n did not open the form")
		}
		m.modal = inputSpawnError
		if _, cmd := update(t, m, tea.FocusMsg{}); cmd != nil {
			t.Error("the form's cursor was fed while a modal held the keyboard")
		}
	})

	t.Run("the label prompt owns input: its textinput is fed", func(t *testing.T) {
		m := testModel(sess("a"))
		m = pressKey(t, m, keyRune('l'))
		if m.modal != inputEditLabel {
			t.Fatalf("setup: modal = %v, want the label prompt open", m.modal)
		}
		if _, cmd := update(t, m, tea.FocusMsg{}); cmd == nil {
			t.Error("the label prompt's textinput got nothing")
		}
	})
}

// TestFaultRowIsInertForTheLabelKey covers the third per-session key, on the
// same reasoning ctrl+t and the preview timer already refuse a fault row —
// with a sharper consequence, which is why it is asserted separately.
//
// `l` writes `.afk/{name}/label` under the row's worktree. For a
// missing-directory row that path does not resolve; for a worktree-with-no-
// session row it resolves into a worktree afk did not create, and
// WriteLabel's own MkdirAll would leave a `.afk/` directory behind inside a
// stranger's checkout. Refusing to CREATE state in a foreign worktree is the
// same rule internal/session's route 2 follows when it refuses to
// snapshot-commit one.
func TestFaultRowIsInertForTheLabelKey(t *testing.T) {
	for _, kind := range []session.Kind{session.KindWorktreeMissing, session.KindWorktreeNoSession} {
		t.Run(string(kind), func(t *testing.T) {
			m := testModel(faultSess("busted", kind, "worktree is broken"))
			got, _ := pressAction(t, m, keyRune('l'))
			if got.modal == inputEditLabel {
				t.Fatalf("`l` on a %s row opened the label form", kind)
			}
			if got.labelTarget != "" {
				t.Fatalf("`l` on a %s row captured %q as a label target", kind, got.labelTarget)
			}
		})
	}
}

// --- the clean modal's merge state and the `d` key ---

// TestConfirmCleanLineReportsMergeState is the modal's first line as a pure
// function of what was captured at confirm-entry. Every row is load-bearing:
// the merged/unmerged pair pins that the BASE IS INTERPOLATED (a hardcoded
// "main" would read as correct on this repo and lie on every other one); the
// pending row pins that a modal opened before the ancestry check answers still
// renders today's line rather than a half-written sentence; the empty-base row
// pins that an unresolvable trunk degrades to that same pending line instead of
// "merged to " with nothing after it; and the two fault rows pin that a row the
// user selected BECAUSE it was badged BROKEN no longer reports "phase: unknown",
// which is a corrupt-status.json verdict this row has not earned.
func TestConfirmCleanLineReportsMergeState(t *testing.T) {
	for _, tt := range []struct {
		name  string
		phase state.Phase
		kind  session.Kind
		merge cleanMergeState
		want  string
	}{
		{
			"merged names the base it was checked against",
			state.PhaseDone, session.KindHealthy,
			cleanMergeState{known: true, merged: true, base: "release/24"},
			`Clean session "twin" (phase: done) — branch merged to release/24`,
		},
		{
			"unmerged names the same base",
			state.PhaseDone, session.KindHealthy,
			cleanMergeState{known: true, merged: false, base: "release/24"},
			`Clean session "twin" (phase: done) — branch not merged to release/24`,
		},
		{
			"merge state still pending",
			state.PhaseDone, session.KindHealthy,
			cleanMergeState{},
			`Clean session "twin" (phase: done)?`,
		},
		{
			"unnameable base is not merge state",
			state.PhaseDone, session.KindHealthy,
			cleanMergeState{known: true, merged: true, base: ""},
			`Clean session "twin" (phase: done)?`,
		},
		{
			"stale status still reads unknown",
			"", session.KindHealthy,
			cleanMergeState{},
			`Clean session "twin" (phase: unknown)?`,
		},
		{
			"BROKEN row names its fault instead of an unknown phase",
			"", session.KindWorktreeMissing,
			cleanMergeState{known: true, merged: false, base: "main"},
			`Clean session "twin" (worktree missing) — branch not merged to main`,
		},
		{
			"ORPHAN row names its fault instead of an unknown phase",
			"", session.KindWorktreeNoSession,
			cleanMergeState{known: true, merged: true, base: "main"},
			`Clean session "twin" (no afk session) — branch merged to main`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := confirmCleanLine("twin", tt.phase, tt.kind, tt.merge); got != tt.want {
				t.Fatalf("confirmCleanLine = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestConfirmCleanHintsFitEightyColumns is the width budget on the hints this
// feature adds or rewrites, asserted on the composed lines the bar actually
// renders (consequence constant + keymap-derived key portion — the constants
// alone no longer carry the keys) so a later reword or rebind cannot quietly
// overflow: 80 columns is the narrowest terminal anyone runs, truncate() cuts
// at m.width, and the key hint sits at the END, so the first thing a too-long
// hint loses is the part that says how to answer it.
func TestConfirmCleanHintsFitEightyColumns(t *testing.T) {
	m := testModel()
	for _, tt := range []struct {
		name string
		hint string
	}{
		{"clean hint", confirmCleanConsequence + " · " + m.confirmCleanFullKeyHint()},
		{"unmerged second confirmation", confirmCleanUnmergedConsequence + " · " + m.confirmCleanUnmergedKeyHint()},
		{"worktree-fault hint", confirmCleanFaultConsequence + " · " + m.confirmCleanFullKeyHint()},
		// The scratch-style hint, not the full one: `d` is inert on this kind.
		{"stranded status-dir hint", confirmCleanStrandedConsequence + " · " + m.confirmCleanKeyHint()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if w := lipgloss.Width(tt.hint); w > 80 {
				t.Fatalf("%s is %d columns, over the 80-column budget: %q", tt.name, w, tt.hint)
			}
			if !strings.HasSuffix(tt.hint, "n/esc: cancel") {
				t.Fatalf("%s must end with the key hint, got %q", tt.name, tt.hint)
			}
		})
	}
}

// TestConfirmCleanBarSwapsToTheUnmergedConfirmation renders the real footer at
// 80 columns in both of the modal's states. The second state is the only
// friction in front of an unrecoverable delete, so it has to say what is lost
// and how to answer, in full, on a stock terminal.
func TestConfirmCleanBarSwapsToTheUnmergedConfirmation(t *testing.T) {
	s := sess("twin")
	s.Phase = state.PhaseDone
	m := testModel(s)
	m.width = 80
	m, _ = pressAction(t, m, keyRune('c'))
	m, _ = update(t, m, cleanMergeStateMsg{target: "twin", base: "release/24"})

	first := strings.ReplaceAll(m.renderConfirmCleanBar(), "\n", " ")
	for _, want := range []string{
		`Clean session "twin" (phase: done) — branch not merged to release/24`,
		"y: clean", "d: +delete branch", "n/esc: cancel",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("first confirmation lost %q:\n%s", want, first)
		}
	}

	m, _ = update(t, m, keyRune('d'))
	second := m.renderConfirmCleanBar()
	joined := strings.ReplaceAll(second, "\n", " ")
	for _, want := range []string{"Unmerged commits will be lost.", "d: delete anyway", "n/esc: cancel"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("second confirmation lost %q:\n%s", want, second)
		}
	}
	if strings.Contains(joined, "…") {
		t.Fatalf("second confirmation must not be truncated at width 80:\n%s", second)
	}
	for _, ln := range strings.Split(second, "\n") {
		trimmed := strings.TrimRight(ln, " ")
		if w := lipgloss.Width(trimmed); w > 80 {
			t.Fatalf("second confirmation line exceeds width 80 (got %d): %q", w, trimmed)
		}
	}
}

// TestConfirmCleanDeleteKeyRoutesOnMergeState is the `d` decision table, and
// every row is a different way to get the destructive answer wrong.
//
// The merged row is the one-press path. The unmerged row must NOT delete on the
// first press and must NOT refuse either — the second confirmation is the only
// friction, and it always leads to a delete. The pending row is the safety
// direction the plan names explicitly: a keypress that arrives before the
// ancestry check answers is treated as UNMERGED, never held and never assumed
// merged. The last two rows have no branch of their own, so `d` is inert on
// both and the footer never offers it: a one-off never had a branch, and a
// stranded `.afk/{name}/` is carried by a worktree checked out on somebody
// else's branch — the entry session.Discover reports for it has Branch "", so
// there is nothing a delete could name.
func TestConfirmCleanDeleteKeyRoutesOnMergeState(t *testing.T) {
	for _, tt := range []struct {
		name         string
		row          Session
		merge        *cleanMergeStateMsg
		wantDispatch bool
		wantOpts     session.CleanOptions
		wantSecond   bool
	}{
		{
			name:         "merged deletes on one press",
			row:          sess("twin"),
			merge:        &cleanMergeStateMsg{target: "twin", base: "main", merged: true},
			wantDispatch: true,
			wantOpts:     session.CleanOptions{DeleteBranch: true},
		},
		{
			name:       "unmerged asks a second time",
			row:        sess("twin"),
			merge:      &cleanMergeStateMsg{target: "twin", base: "main"},
			wantSecond: true,
		},
		{
			name:       "merge state not back yet is treated as unmerged",
			row:        sess("twin"),
			merge:      nil,
			wantSecond: true,
		},
		{
			name:  "scratch has no branch to delete",
			row:   scratchSess("twin"),
			merge: nil,
		},
		{
			name:  "a stranded session has no branch of its own to delete",
			row:   strandedSess("twin"),
			merge: nil,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(tt.row)
			var gotOpts session.CleanOptions
			called := false
			m.deps.CleanSession = func(_, name, _ string, _ bool, _ session.Kind, opts session.CleanOptions) (session.CleanResult, error) {
				called, gotOpts = true, opts
				return session.CleanResult{Name: name}, nil
			}
			m, _ = pressAction(t, m, keyRune('c'))
			if tt.merge != nil {
				m, _ = update(t, m, *tt.merge)
			}

			m, cmd := update(t, m, keyRune('d'))
			if !tt.wantDispatch {
				if cmd != nil {
					t.Fatalf("`d` should not dispatch a clean here, got a cmd")
				}
				if m.modal != inputConfirmClean {
					t.Fatalf("`d` should leave the modal up, got mode %v", m.modal)
				}
				if m.confirmDeleteUnmerged != tt.wantSecond {
					t.Fatalf("confirmDeleteUnmerged = %v, want %v", m.confirmDeleteUnmerged, tt.wantSecond)
				}
				return
			}
			if cmd == nil {
				t.Fatal("`d` on a merged branch should dispatch the clean")
			}
			cmd()
			if !called {
				t.Fatal("the dispatched cmd never reached the CleanSession seam")
			}
			if gotOpts != tt.wantOpts {
				t.Fatalf("CleanSession got opts %+v, want %+v", gotOpts, tt.wantOpts)
			}
			if m.modal != inputNormal {
				t.Fatalf("a dispatching `d` should return to normal mode, got %v", m.modal)
			}
		})
	}
}

// TestConfirmCleanSecondDeleteConfirmsUnmerged is the far side of the second
// confirmation: the ONLY way ConfirmedUnmerged reaches internal/session, which
// is the bit that lets it delete a branch whose commits are nowhere else.
func TestConfirmCleanSecondDeleteConfirmsUnmerged(t *testing.T) {
	m := testModel(sess("twin"))
	var gotOpts session.CleanOptions
	m.deps.CleanSession = func(_, name, _ string, _ bool, _ session.Kind, opts session.CleanOptions) (session.CleanResult, error) {
		gotOpts = opts
		return session.CleanResult{Name: name}, nil
	}
	m, _ = pressAction(t, m, keyRune('c'))
	m, _ = update(t, m, cleanMergeStateMsg{target: "twin", base: "main"})

	m, cmd := update(t, m, keyRune('d'))
	if cmd != nil {
		t.Fatal("the first `d` on an unmerged branch must not dispatch anything")
	}
	m, cmd = update(t, m, keyRune('d'))
	if cmd == nil {
		t.Fatal("the second `d` should dispatch the clean")
	}
	cmd()
	want := session.CleanOptions{DeleteBranch: true, ConfirmedUnmerged: true}
	if gotOpts != want {
		t.Fatalf("CleanSession got opts %+v, want %+v", gotOpts, want)
	}
	if m.modal != inputNormal || m.confirmDeleteUnmerged {
		t.Fatalf("the second `d` should close the modal, got mode %v second=%v", m.modal, m.confirmDeleteUnmerged)
	}
}

// TestConfirmCleanUnmergedConfirmationCancels pins the escape from the second
// state. It is the only friction in front of a delete that leaves the commits
// reflog-only, so both advertised cancels must work FROM it, not merely from
// the first confirmation.
func TestConfirmCleanUnmergedConfirmationCancels(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"esc", tea.KeyPressMsg{Code: tea.KeyEsc}},
		{"n", keyRune('n')},
		{"N", keyRune('N')},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("twin"))
			m.deps.CleanSession = func(string, string, string, bool, session.Kind, session.CleanOptions) (session.CleanResult, error) {
				t.Fatal("cancelling the second confirmation must never clean")
				return session.CleanResult{}, nil
			}
			m, _ = pressAction(t, m, keyRune('c'))
			m, _ = update(t, m, cleanMergeStateMsg{target: "twin", base: "main"})
			m, _ = update(t, m, keyRune('d'))
			if !m.confirmDeleteUnmerged {
				t.Fatal("setup: the first `d` should have entered the second confirmation")
			}

			got, cmd := update(t, m, tt.key)
			if cmd != nil {
				t.Fatalf("%s should not dispatch a cmd", tt.name)
			}
			if got.modal != inputNormal {
				t.Fatalf("%s should return to normal mode, got %v", tt.name, got.modal)
			}
			if got.confirmDeleteUnmerged || got.confirmTarget != "" {
				t.Fatalf("%s left the modal state set: second=%v target=%q",
					tt.name, got.confirmDeleteUnmerged, got.confirmTarget)
			}
		})
	}
}

// TestConfirmCleanYIsUnchangedByTheDeleteOffer is the guarantee the plan states
// twice: `y` behaves exactly as today. It dispatches ZERO CleanOptions even
// when the modal knows the branch is merged and is actively offering to delete
// it — the offer is `d`'s alone, and a `y` that quietly started deleting
// branches would break the one answer every existing user already trusts.
func TestConfirmCleanYIsUnchangedByTheDeleteOffer(t *testing.T) {
	m := testModel(sess("twin"))
	var gotOpts session.CleanOptions
	m.deps.CleanSession = func(_, name, _ string, _ bool, _ session.Kind, opts session.CleanOptions) (session.CleanResult, error) {
		gotOpts = opts
		return session.CleanResult{Name: name}, nil
	}
	m, _ = pressAction(t, m, keyRune('c'))
	m, _ = update(t, m, cleanMergeStateMsg{target: "twin", base: "main", merged: true})

	m, cmd := update(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatal("y should still dispatch the clean")
	}
	cmd()
	if (gotOpts != session.CleanOptions{}) {
		t.Fatalf("y dispatched %+v, want the zero CleanOptions", gotOpts)
	}
	if m.modal != inputNormal {
		t.Fatalf("y should return to normal mode, got %v", m.modal)
	}
}

// TestCleanMergeStateIsGuardedOnTheConfirmedTarget is the async-identity guard.
// The ancestry check runs off the event loop, so its answer can land after the
// user cancelled one modal and opened another: without this guard, session A's
// result labels session B's confirmation, and the label is the whole basis on
// which the user then presses `d`.
func TestCleanMergeStateIsGuardedOnTheConfirmedTarget(t *testing.T) {
	for _, tt := range []struct {
		name  string
		msg   cleanMergeStateMsg
		apply bool
	}{
		{"the confirmed target", cleanMergeStateMsg{target: "b", base: "main", merged: true}, true},
		{"a cancelled modal's late answer", cleanMergeStateMsg{target: "a", base: "main", merged: true}, false},
		{"the same name, a different kind", cleanMergeStateMsg{target: "b", kind: session.KindWorktreeMissing, base: "main", merged: true}, false},
		{"an unnameable base", cleanMergeStateMsg{target: "b", base: ""}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("a"), sess("b"))
			m.selectSessionIndex(1)
			m, _ = pressAction(t, m, keyRune('c'))
			if m.confirmTarget != "b" {
				t.Fatalf("setup: confirmTarget = %q, want b", m.confirmTarget)
			}
			got, _ := update(t, m, tt.msg)
			if got.confirmMerge.known != tt.apply {
				t.Fatalf("confirmMerge.known = %v, want %v", got.confirmMerge.known, tt.apply)
			}
		})
	}
}

// TestCleanMergeStateClearsOnEveryExit is the leak guard confirmScratch and
// confirmKind already have, extended to this task's two additions: a merge
// verdict left set would label the NEXT session's modal with the PREVIOUS
// branch's merge state, and a leaked second-confirmation flag would turn the
// next modal's first `d` into an immediate unmerged delete.
func TestCleanMergeStateClearsOnEveryExit(t *testing.T) {
	for _, tt := range []struct {
		name string
		keys []tea.KeyMsg
	}{
		{"esc", []tea.KeyMsg{tea.KeyPressMsg{Code: tea.KeyEsc}}},
		{"n", []tea.KeyMsg{keyRune('n')}},
		{"y", []tea.KeyMsg{keyRune('y')}},
		{"d then d", []tea.KeyMsg{keyRune('d'), keyRune('d')}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(sess("twin"))
			m.deps.CleanSession = func(_, name, _ string, _ bool, _ session.Kind, _ session.CleanOptions) (session.CleanResult, error) {
				return session.CleanResult{Name: name}, nil
			}
			m, _ = pressAction(t, m, keyRune('c'))
			m, _ = update(t, m, cleanMergeStateMsg{target: "twin", base: "main"})
			if !m.confirmMerge.known {
				t.Fatal("setup: the merge answer should have been applied")
			}
			for _, k := range tt.keys {
				m, _ = update(t, m, k)
			}
			if (m.confirmMerge != cleanMergeState{}) {
				t.Fatalf("confirmMerge leaked past the %s exit: %+v", tt.name, m.confirmMerge)
			}
			if m.confirmDeleteUnmerged {
				t.Fatalf("confirmDeleteUnmerged leaked past the %s exit", tt.name)
			}
		})
	}
}

// TestCleanSelectedRequestsMergeStateOffTheEventLoop pins that the ancestry
// check is a tea.Cmd, not a synchronous call inside Update: it is a git
// invocation, and one of those on the event loop stalls the whole dashboard
// (every row, every ticker) for as long as git takes.
//
// The three nil rows are the cases with nothing to ask: a one-off has no branch
// by construction, a row whose status.json did not parse has no branch NAME to
// hand git — asking anyway would run `git merge-base --is-ancestor ""` — and a
// stranded `.afk/{name}/` has a branch NAME (status.json supplies one) but no
// branch of its own to delete, so the answer would label an offer `d` never
// makes. That row is the one a Branch-only check gets wrong.
func TestCleanSelectedRequestsMergeStateOffTheEventLoop(t *testing.T) {
	for _, tt := range []struct {
		name    string
		row     Session
		wantCmd bool
	}{
		{"healthy row", sess("twin"), true},
		{"fault row", faultSess("twin", session.KindWorktreeMissing, "gone"), true},
		{"scratch row", scratchSess("twin"), false},
		{"stale row has no branch name", staleSess("twin"), false},
		{"stranded row has a branch name but no branch of its own", strandedSess("twin"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(tt.row)
			var gotRepo, gotBranch, gotBase string
			m.deps.BranchMerged = func(repo, branch, base string) (bool, string) {
				gotRepo, gotBranch, gotBase = repo, branch, base
				return true, "resolved-base"
			}
			m, cmd := pressAction(t, m, keyRune('c'))
			if !tt.wantCmd {
				if cmd != nil {
					t.Fatalf("`c` on a %s should not ask git anything", tt.name)
				}
				return
			}
			if cmd == nil {
				t.Fatal("`c` should dispatch the merge-state cmd")
			}
			msg, ok := cmd().(cleanMergeStateMsg)
			if !ok {
				t.Fatalf("the merge-state cmd produced %T, want cleanMergeStateMsg", cmd())
			}
			if gotRepo != "/repo" || gotBranch != tt.row.Branch || gotBase != tt.row.BaseBranch {
				t.Fatalf("BranchMerged got (%q, %q, %q), want (/repo, %q, %q)",
					gotRepo, gotBranch, gotBase, tt.row.Branch, tt.row.BaseBranch)
			}
			if msg.target != "twin" || msg.kind != m.confirmKind || !msg.merged || msg.base != "resolved-base" {
				t.Fatalf("cleanMergeStateMsg = %+v, want the seam's answer carried whole", msg)
			}
		})
	}
}

// TestCleanSucceededStatusReportsTheBranchOutcome covers the footer's one row
// after a clean that deleted a branch. The SHA is the load-bearing part: a
// deleted branch is only reflog-recoverable, so the summary line is the one
// place the user can read the handle back off, and it must survive next to the
// archive path rather than instead of it. The notes row folds in the steps
// internal/session reported and skipped, which had no surface at all before.
func TestCleanSucceededStatusReportsTheBranchOutcome(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result session.CleanResult
		want   []string
		absent []string
	}{
		{
			name: "deleted branch reports the recovery handle",
			result: session.CleanResult{
				Name: "twin", Branch: "feat/twin", ArchivePath: "/arch/twin",
				BranchDeleted: true, DeletedBranchSHA: "0123456789abcdef0123456789abcdef01234567",
			},
			want:   []string{"archived as twin", "branch deleted", "recover: git branch feat/twin", "0123456789ab"},
			absent: []string{"kept"},
		},
		{
			name:   "kept branch is unchanged",
			result: session.CleanResult{Name: "twin", Branch: "feat/twin", ArchivePath: "/arch/twin"},
			want:   []string{"branch feat/twin kept"},
			absent: []string{"deleted"},
		},
		{
			name: "skipped steps are reported",
			result: session.CleanResult{
				Name: "twin", Branch: "feat/twin", ArchivePath: "/arch/twin",
				Notes: "not deleting branch \"feat/twin\": it has not been merged\nand a second line",
			},
			want: []string{"it has not been merged", "and a second line"},
		},
		// A git-spice delete with --restack=none silently moves upstack
		// branches' parent pointer; this message is the only surface that
		// tells the user those branches were retargeted and not restacked,
		// so the retargeted names must appear here.
		{
			name: "retargeted upstack branches are reported",
			result: session.CleanResult{
				Name: "twin", Branch: "feat/twin", ArchivePath: "/arch/twin",
				BranchDeleted: true, DeletedBranchSHA: "0123456789abcdef0123456789abcdef01234567",
				RetargetedBranches: "up-a, up-b",
			},
			want: []string{"branch deleted", "recover: git branch feat/twin", "retargeted", "up-a, up-b"},
		},
		{
			name: "no retarget notice without retargets",
			result: session.CleanResult{
				Name: "twin", Branch: "feat/twin", ArchivePath: "/arch/twin",
				BranchDeleted: true, DeletedBranchSHA: "0123456789abcdef0123456789abcdef01234567",
			},
			absent: []string{"retargeted"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanSucceededStatus(tt.result)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("status %q is missing %q", got, want)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(got, absent) {
					t.Fatalf("status %q should not contain %q", got, absent)
				}
			}
			if strings.Contains(got, "\n") {
				t.Fatalf("a newline would cost the toast one of its three lines; status must not contain one: %q", got)
			}
		})
	}
}

// TestCleanSucceededStatusPutsRecoveryAheadOfTheArchive is the value-ordering
// contract at the level of the string: for a clean that DELETED its branch,
// the recovery command outranks the archive, which outranks every cosmetic
// clause.
//
// Both items are irrecoverable once the message is gone, and the ranking
// between them is not arbitrary. The archive's directory can be listed; the
// deleted branch's SHA exists nowhere on screen and nowhere on disk except the
// reflog, which a user has to know to reach for. So the SHA wins the earlier
// position, and the archive takes the one after it.
func TestCleanSucceededStatusPutsRecoveryAheadOfTheArchive(t *testing.T) {
	got := cleanSucceededStatus(session.CleanResult{
		Name: "twin", Branch: "feat/twin", ArchivePath: "/arch/twin-2026-07-29",
		BranchDeleted: true, DeletedBranchSHA: "0123456789abcdef0123456789abcdef01234567",
		SnapshotCommitted:  true,
		RetargetedBranches: "up-a",
		Notes:              "not deleting worktree: it is foreign",
	})
	order := []string{
		"cleaned twin",
		"recover: git branch feat/twin 0123456789ab",
		"archived as twin-2026-07-29",
		"snapshot-committed",
		"upstack retargeted",
		"it is foreign",
	}
	at := -1
	for _, want := range order {
		i := strings.Index(got, want)
		if i < 0 {
			t.Fatalf("status %q is missing %q", got, want)
		}
		if i < at {
			t.Fatalf("status %q has %q out of value order (it must follow everything before it in %v)",
				got, want, order)
		}
		at = i
	}
}

// flattenToastContent undoes renderToast's box framing so a token search can
// treat the wrapped message as one continuous run of text. The border
// (`│`/`─`/the corner glyphs) and the Padding(0, 1) inset toastBoxOf gives
// every line carry no message content, but they DO sit between two halves of
// a word the wrap broke — renderToast's wrap treats a hyphen as a break
// point, same as a space, so a long branch name can split mid-word (observed:
// "...onboarding-" / "flow..."). Searching the raw bordered string for that
// word as one substring fails even though every character of it reached the
// screen; stripping the frame and rejoining the lines is what makes "visible
// in the rendered output" mean what the sentence says.
func flattenToastContent(shown string) string {
	var b strings.Builder
	for _, line := range strings.Split(shown, "\n") {
		if !strings.HasPrefix(line, "│") {
			continue // the top/bottom border row, all dashes and corners
		}
		line = strings.TrimPrefix(line, "│")
		line = strings.TrimSuffix(line, "│")
		line = strings.TrimRight(line, " ")  // the box's right-fill padding
		line = strings.TrimPrefix(line, " ") // the box's left inset
		b.WriteString(line)
	}
	return b.String()
}

// TestCleanSucceededToastKeepsTheRecoveryCommand is the ordering rule enforced
// where it actually matters: in RENDERED output, past renderToast's wrap and
// its 3-line clip.
//
// Asserting the source string is not enough and never was. cleanSucceededStatus
// can contain every field and still show a user none of them — the toast wraps
// to 48 cells and hard-clips at 3 lines (toast.go), which is roughly 144
// characters of a message that routinely runs longer, and nothing keeps a
// second copy now that the footer's status row is gone. Before the reorder the
// recovery command sat LAST and was the first thing cut; this is the test that
// would have caught that, and the one that fails if a later edit moves a
// cosmetic clause back in front of it.
//
// Three rows, all load-bearing. "short branch name" is the original fixture and
// stays for regression coverage, but on its own it proves less than it looks
// like it does: a 12-character branch leaves so much of the 144-character clip
// budget spare that the recovery command survives almost by accident, ordering
// or not. "long branch name" is the case that actually exercises the ordering
// rule — a branch name past 25 characters makes the recovery clause itself
// compete for the clip's budget. It used to fail here: branchOutcome spent the
// branch name TWICE ("branch X deleted — recover: git branch X sha"), so a
// 33-character name alone could exhaust the clip before the SHA. Naming the
// branch once, inside the actual command, fixed it and this row proves it.
//
// "branch name past the fixed budget" is the hostile row the fix does NOT
// rescue, on purpose: probing branchOutcome directly (session notepad
// post-approval-fixes-2) found the real ceiling is 35 characters of branch
// name — one past that and the SHA is genuinely gone, not merely wrapped.
// The task forbids widening the toast, the clip, or the SHA to buy more room,
// so a 70-character name is asserted for what it actually does: the outcome
// and the start of the recovery command ("recover: git branch") still land,
// the SHA does not. That is the honest remaining limit, not a bug this row
// pretends isn't there.
//
// The recovery command is checked token by token, against flattened content
// rather than the raw bordered string, because the wrap can put two halves of
// one word (or the SHA) on either side of a line break — seeing every part of
// it reach the screen in order is the claim, not that it landed on one line
// or survived the border's own characters. The notes are asserted ABSENT in
// the rescued rows for the other half of the claim: the clip is still taking
// something, so the leading position is doing real work.
func TestCleanSucceededToastKeepsTheRecoveryCommand(t *testing.T) {
	cases := []struct {
		name    string
		branch  string
		wantSHA bool // false only for the row past the documented ceiling
	}{
		{name: "short branch name", branch: "feat/billing", wantSHA: true},
		{name: "long branch name", branch: "feature/redesign-onboarding-flow", wantSHA: true},
		{
			name:    "branch name past the fixed budget",
			branch:  "feature/completely-overhaul-the-legacy-billing-reconciliation-pipeline",
			wantSHA: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := cleanSucceededStatus(session.CleanResult{
				Name: "billing-rework", Branch: tc.branch, ArchivePath: "/arch/repo-billing-2026-07-29",
				BranchDeleted: true, DeletedBranchSHA: "0123456789abcdef0123456789abcdef01234567",
				SnapshotCommitted:  true,
				RetargetedBranches: "up-a, up-b",
				Notes:              "not deleting branch \"feat/billing\": it has not been merged",
			})
			shown := xansi.Strip(renderToast(notifySuccess, status, 120))
			if lipgloss.Height(shown) > 5 {
				t.Fatalf("the toast grew past its 3 content lines plus a border:\n%s", shown)
			}
			flat := flattenToastContent(shown)

			if !tc.wantSHA {
				// The documented, accepted limit: the command's start reaches
				// the screen, the SHA does not. Anything less (losing "recover:"
				// or "git branch" too) would be a worse regression than the one
				// this row is pinning.
				if !strings.Contains(flat, "recover:") || !strings.Contains(flat, "git branch") {
					t.Fatalf("lost the start of the recovery command too — this row should only lose the SHA:\n%s", shown)
				}
				if strings.Contains(flat, "0123456789ab") {
					t.Fatalf("the SHA survived a branch name past the documented 35-character ceiling — "+
						"the ceiling moved, update the doc comment and this row's expectation:\n%s", shown)
				}
				return
			}
			at := -1
			for _, tok := range []string{"recover:", "git", "branch", tc.branch, "0123456789ab"} {
				i := strings.Index(flat[at+1:], tok)
				if i < 0 {
					t.Fatalf("the rendered toast lost %q of the recovery command — it is the one datum "+
						"nothing else persists:\n%s", tok, shown)
				}
				at += 1 + i
			}
			if strings.Contains(flat, "has not been merged") {
				t.Fatalf("the notes survived the clip, so this fixture is not the worst case the "+
					"ordering exists for:\n%s", shown)
			}
		})
	}
}

// TestCleanMergeStateNeverLabelsAScratchModal extends the async-identity guard
// to the third captured half of the modal's identity: scratch-ness. A scratch
// modal never ASKS for merge state (see cleanSelected), so any answer arriving
// while one is up belongs to some other modal — here, a cancelled modal on the
// healthy session sharing the scratch one's name and reduced kind, the one
// collision (Name equal, cleanTargetKind equal) that target and kind alone
// cannot separate. Applying it would store a merge verdict no scratch surface
// requested and none should ever render.
func TestCleanMergeStateNeverLabelsAScratchModal(t *testing.T) {
	m := testModel(sess("twin"), scratchSess("twin"))

	// Open the modal on the healthy twin — this is the modal whose ancestry
	// check produces the in-flight answer — then cancel it.
	m, _ = pressAction(t, m, keyRune('c'))
	if m.confirmTarget != "twin" || m.confirmScratch {
		t.Fatalf("setup: confirmTarget = %q (scratch %v), want the healthy twin", m.confirmTarget, m.confirmScratch)
	}
	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	// Open a modal on the scratch twin: same target, same reduced kind.
	m.selectSessionIndex(1)
	m, _ = pressAction(t, m, keyRune('c'))
	if m.confirmTarget != "twin" || !m.confirmScratch {
		t.Fatalf("setup: confirmTarget = %q (scratch %v), want the scratch twin", m.confirmTarget, m.confirmScratch)
	}

	got, _ := update(t, m, cleanMergeStateMsg{target: "twin", base: "main", merged: true})
	if got.confirmMerge.known {
		t.Fatal("the healthy twin's late merge answer labelled the scratch twin's modal: scratch-ness must be part of the identity guard")
	}
}

// TestNameLookupsSkipFaultRows extends attach.go's rule to the dashboard's own
// by-name resolvers: a worktree-side fault row can never host a session, so it
// must never satisfy a name lookup. Without the skip, a fault row named after
// its branch outranks a scratch session sharing that name (sortSessionStates
// orders non-scratch first), so a startup pane or focus deep-link resolves to
// a row with no host and a dead or foreign worktree — a failed pane open where
// the scratch session should simply have opened.
func TestNameLookupsSkipFaultRows(t *testing.T) {
	fault := faultSess("dup", session.KindWorktreeNoSession, "worktree has no afk session")
	fault.WorktreePath = "/wt/foreign-dup"
	twin := scratchSess("dup")

	t.Run("sessionByName resolves the scratch twin, not the fault row", func(t *testing.T) {
		m := testModel(fault, twin)
		s, ok := m.sessionByName("dup")
		if !ok || !s.Scratch {
			t.Fatalf("sessionByName(dup) = %+v (ok %v), want the scratch twin", s, ok)
		}
	})
	t.Run("worktreeFor answers with the scratch twin's worktree", func(t *testing.T) {
		m := testModel(fault, twin)
		if got := m.worktreeFor("dup"); got != "/wt/dup" {
			t.Fatalf("worktreeFor(dup) = %q, want the scratch twin's /wt/dup", got)
		}
	})
	t.Run("a name matching only fault rows is not found", func(t *testing.T) {
		m := testModel(fault)
		if _, ok := m.sessionByName("dup"); ok {
			t.Fatal("sessionByName(dup) found a fault-only name")
		}
		if _, ok := m.sessionIndex("dup"); ok {
			t.Fatal("sessionIndex(dup) found a fault-only name")
		}
	})
}

// TestStartupPaneNeverResolvesToAFaultRow drives the same rule through the
// startup-pane entrance (`afk dash --open <name>`): a fault-only name must
// report "no such session" rather than restart a host inside a worktree that
// is gone or was never afk's, and a collision must open the scratch twin.
func TestStartupPaneNeverResolvesToAFaultRow(t *testing.T) {
	fault := faultSess("dup", session.KindWorktreeMissing, "worktree directory no longer exists")
	m := testModel(fault)
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		t.Fatal("the startup pane restarted a host for a fault row")
		return session.HostProcess{}, nil
	}
	m.initialSession = "dup"
	m.firstLoadDone = true

	nm, cmd := m.maybeOpenInitialPane()
	got, ok := nm.(Model)
	if !ok {
		t.Fatalf("maybeOpenInitialPane returned %T, want ui.Model", nm)
	}
	if cmd != nil {
		drainBatch(t, cmd)
	}
	if !strings.Contains(got.status, "no such session") {
		t.Fatalf("status = %q, want the not-found refusal", got.status)
	}
}

// TestResolveFocusTargetSkipsFaultRows covers the focus deep-link resolver on
// both of its sources: the active project answers from m.sessions and any
// other project from the scan cache, and each is a separate name-only loop
// that would otherwise hand a click a fault row to open.
func TestResolveFocusTargetSkipsFaultRows(t *testing.T) {
	t.Run("active project: the scratch twin wins the collision", func(t *testing.T) {
		fault := faultSess("dup", session.KindWorktreeNoSession, "worktree has no afk session")
		m := testModel(fault, scratchSess("dup"))
		target, ok := m.resolveFocusTarget("", "dup")
		if !ok || !target.scratch {
			t.Fatalf("resolveFocusTarget = %+v (ok %v), want the scratch twin", target, ok)
		}
	})
	t.Run("active project: a fault-only name is unopenable", func(t *testing.T) {
		m := testModel(faultSess("dup", session.KindWorktreeMissing, "worktree directory no longer exists"))
		if _, ok := m.resolveFocusTarget("", "dup"); ok {
			t.Fatal("resolveFocusTarget found a fault-only name")
		}
	})
	t.Run("cached other project: a fault row is unopenable", func(t *testing.T) {
		m := testModel()
		m.projectSessions = map[string][]discover.SessionState{
			"/other": {{Name: "dup", WorktreePath: "/other/wt", Kind: session.KindWorktreeNoSession}},
		}
		if _, ok := m.resolveFocusTarget("/other", "dup"); ok {
			t.Fatal("resolveFocusTarget found a cached fault row")
		}
	})
}

// TestConfirmCleanBarFaultRowDoesNotPromiseAnArchive is the review finding this
// fixes, stated as a test: confirmCleanHint ends "archive kept", and on a
// worktree-side fault row nothing is archived — cleanMissingWorktree and
// cleanForeignWorktree both set ArchiveSkipped unconditionally, one because the
// directory took its .afk/{name}/ with it and the other because a foreign
// worktree never had one.
//
// A stale guarantee is worse here than anywhere else in the UI: it is the last
// line a user reads before answering a destructive prompt with a single key,
// and "archive kept" is precisely the sentence that would make them answer it
// faster.
func TestConfirmCleanBarFaultRowDoesNotPromiseAnArchive(t *testing.T) {
	for _, kind := range []session.Kind{session.KindWorktreeMissing, session.KindWorktreeNoSession} {
		t.Run(string(kind), func(t *testing.T) {
			m := testModel(faultSess("broken", kind, "worktree directory no longer exists"))
			m.width = 80
			m, _ = pressAction(t, m, keyRune('c'))
			if m.modal != inputConfirmClean {
				t.Fatal("`c` on a fault row should confirm before cleaning")
			}

			out := m.renderConfirmCleanBar()
			joined := strings.ReplaceAll(out, "\n", " ")

			if strings.Contains(joined, "archive kept") {
				t.Errorf("confirm bar for %s promises %q, but this route always sets ArchiveSkipped:\n%s",
					kind, "archive kept", out)
			}
			if !strings.Contains(joined, "nothing archived") {
				t.Errorf("confirm bar for %s should say nothing is archived, got:\n%s", kind, out)
			}
			for _, want := range []string{"y: clean", "d: +delete branch", "n/esc: cancel"} {
				if !strings.Contains(joined, want) {
					t.Errorf("confirm bar for %s lost %q, got:\n%s", kind, want, out)
				}
			}
			for _, ln := range strings.Split(out, "\n") {
				if w := lipgloss.Width(strings.TrimRight(ln, " ")); w > 80 {
					t.Errorf("fault confirm bar line exceeds width 80 (got %d)", w)
				}
			}
		})
	}
}

// TestMarkHostStoppedSkipsFaultRows covers the last by-name loop in this
// package that had no fault-row skip. Under the documented fault/scratch name
// collision it would flip HostRunning on whichever row sortSessions ordered
// first — and a fault row has no host to disprove, so the flip landed on the
// wrong entry and left a stale restart hint on it.
//
// Non-destructive, which is exactly why it is worth pinning: nothing else in
// the suite would have noticed.
func TestMarkHostStoppedSkipsFaultRows(t *testing.T) {
	fault := faultSess("twin", session.KindWorktreeMissing, "worktree directory no longer exists")
	healthy := Session{SessionState: discover.SessionState{
		Name: "twin", WorktreePath: "/wt/twin", HostRunning: true, HostPID: 4242,
	}}
	m := testModel(fault, healthy)

	m.markHostStopped("twin")

	var sawHealthy bool
	for _, s := range m.sessions {
		if isWorktreeFault(s.Kind) {
			continue
		}
		sawHealthy = true
		if s.HostRunning {
			t.Error("markHostStopped left the healthy twin running — it skipped the wrong row")
		}
	}
	if !sawHealthy {
		t.Fatal("fixture broken: no non-fault row named twin")
	}
	if m.deadHostPIDs["twin"] != 4242 {
		t.Errorf("deadHostPIDs[twin] = %d, want the healthy row's 4242 — the fault row has no PID to remember",
			m.deadHostPIDs["twin"])
	}
}

// TestCleaningSetIsKeyedByNameAndKind pins the in-flight clean set's identity.
// Keyed by name alone, one entry's clean blocked `c` on a same-named entry of a
// different kind for the clean's whole duration — never a wrong teardown, but a
// row the user could not touch for no reason they could see.
//
// The KEY is still the pair, and this is the test that says so. Only the
// name-only paths ask the looser question (see cleaningByName and
// TestNameOnlyPathsRefuseAnySameNameClean); `c` itself, markCleaning and
// dispatchConfirmedClean all still speak in (name, kind).
func TestCleaningSetIsKeyedByNameAndKind(t *testing.T) {
	var m Model
	faultKey := cleanID{name: "twin", kind: session.KindWorktreeMissing}

	m.markCleaning(faultKey)

	if !m.cleaningSessions[faultKey] {
		t.Fatal("markCleaning did not record the fault entry")
	}
	if m.cleaningSessions[cleanID{name: "twin", kind: session.KindHealthy}] {
		t.Error("a fault entry's in-flight clean also blocks the healthy twin — the set is keyed by name alone")
	}

	m.clearCleaning(faultKey)
	if m.cleaningSessions[faultKey] {
		t.Error("clearCleaning did not remove the fault entry")
	}
}

// TestNameOnlyPathsRefuseAnySameNameClean is the half of the in-flight check
// that a (name, kind) key gets WRONG once a pane-capable row can carry a
// non-healthy clean kind.
//
// Every path here has only a name to go on — a restarted host, an arriving pane
// connection, a focus request from another process — and the old key filled the
// missing half in with KindHealthy. That was sound while only worktree faults
// were identity-relevant (those rows are inert for ctrl+t and preview, so no
// pane path could reach one), and it is false for KindStatusWithoutWorktree: a
// stranded `.afk/X/` renders as an ordinary row with a live host, so its
// clean is registered under the stranded kind while these paths look up the
// healthy one, miss, and open a pane onto a host the clean is about to SIGKILL.
//
// The last subtest is the accepted OVER-refusal, pinned deliberately so it is
// not "fixed" later: a healthy X and a stranded X block each other on these
// paths for the duration of one clean (~11s worst case). Neither name-only path
// can tell which entry a bare name meant, and refusing a pane for 11 seconds is
// the cheap side of that trade.
func TestNameOnlyPathsRefuseAnySameNameClean(t *testing.T) {
	strandedKey := cleanID{name: "twin", kind: session.KindStatusWithoutWorktree}

	t.Run("the restart chain opens no pane over a stranded clean", func(t *testing.T) {
		m := testModel(strandedSess("twin"))
		opens := 0
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
			opens++
			return newFakePane("X"), nil
		}
		m.markCleaning(strandedKey)

		got, cmd := update(t, m, hostRestartedMsg{name: "twin", worktree: "/repo"})
		if cmd != nil {
			drainBatch(t, cmd)
		}
		if opens != 0 {
			t.Fatalf("OpenHostPane called %d times by the restart chain, want 0", opens)
		}
		if got.pane != nil {
			t.Fatal("the restart chain opened a pane over a cleaning session")
		}
		if !strings.Contains(got.status, "can't open twin") {
			t.Fatalf("status = %q, want the refusal stated, never silence", got.status)
		}
	})

	t.Run("an arriving pane connection is dropped and closed", func(t *testing.T) {
		m := testModel(strandedSess("twin"))
		m.markCleaning(strandedKey)

		arriving := newFakePane("late")
		// The Cmd is deliberately not drained: installing the pane (the bug)
		// re-arms paneEventCmd, which blocks on the fake's channels forever, so
		// draining here turns a failure into a hung test. The installed pane
		// itself is the assertion.
		got, _ := update(t, m, paneAttachedMsg{name: "twin", pane: arriving})
		if got.pane != nil {
			t.Fatal("a pane arriving for a cleaning session must not be installed")
		}
		if arriving.closed != 1 {
			t.Fatalf("arriving pane closed %d times, want exactly 1 — a dropped connection leaks a client slot",
				arriving.closed)
		}
	})

	t.Run("a focus request from another process is ignored", func(t *testing.T) {
		w := newFakeFocusWatcher()
		m, _ := focusRequestModel(t, w, strandedSess("twin"))
		// Counted rather than t.Fatal'd from inside the fake: collectMsgs runs
		// batch members on their own goroutines, and a Fatal there Goexits a
		// goroutine it is still waiting on, which hangs the test instead of
		// failing it.
		opens := 0
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
			opens++
			return newFakePane("X"), nil
		}
		m.markCleaning(strandedKey)

		got, cmd := update(t, m, freshRequestMsg("twin"))
		runAllCmds(t, cmd)

		if opens != 0 {
			t.Fatalf("OpenHostPane called %d times for a focus request, want 0", opens)
		}
		if got.pane != nil || got.paneOwnsKeyboard() {
			t.Fatal("no pane may be opened or focused over a cleaning session")
		}
	})

	// Driven through enter, not openPaneFor directly, because the row
	// under the cursor is the healthy twin: connectSelected's own row-keyed
	// check (cleanIDFor) misses the stranded key by design, so this passes only
	// if the name-only gate inside openPaneFor holds.
	t.Run("the accepted over-refusal: enter on the healthy twin is blocked too", func(t *testing.T) {
		m := testModel(runningSess("twin"))
		m.markCleaning(strandedKey)

		got, cmd := update(t, m, keyEnter)
		if cmd != nil {
			t.Fatalf("enter dispatched %T while a same-named clean was in flight", cmd())
		}
		if got.attachInFlight.name != "" {
			t.Fatalf("enter left a dial in flight for %q", got.attachInFlight.name)
		}
		if !strings.Contains(got.status, "can't open twin") {
			t.Fatalf("status = %q, want the refusal stated", got.status)
		}
	})
}

// --- the discovery tick's session diff (W4.T1) ---

// phased returns a copy of s in phase p, for the diff's terminal-crossing rows.
func phased(s Session, p state.Phase) Session {
	s.Phase = p
	return s
}

// awaiting returns a copy of s flagged as waiting on the user. It leaves the
// phase alone deliberately: showsAwaitingInput suppresses the flag on a
// terminal phase, so a fixture that set both would be testing that rule
// instead of this one.
func awaiting(s Session) Session {
	s.AwaitingInput = true
	return s
}

// stoppedSess is runningSess' counterpart: the same row, same recorded PID,
// with the host no longer answering. The PID has to match for the pair to read
// as one host dying rather than as a different host appearing.
func stoppedSess(name string) Session {
	s := runningSess(name)
	s.HostRunning = false
	return s
}

// TestApplyLoadedNotifiesSessionTransitions feeds successive discovery
// snapshots through applyLoaded and asserts the background events the diff
// hands to the arbiter. It reads pendingBackground rather than m.status
// because that is the seam this task owns — coalescing and the slot rule are
// W3.T1's and are exercised through Update in
// TestTwoTransitionsInOneTickCoalesce.
//
// Which rows are load-bearing, and why:
//
//   - done / failed / awaiting-input / host-died: the four events that were
//     silent before this task. Each also pins its KIND, because worst-wins
//     styling makes a mis-assigned kind a visible bug in every batch the
//     event joins.
//   - no-change-tick and already-terminal: the diff fires on transitions, not
//     on states. Without these an unchanged dashboard toasts every 2s.
//   - first-sight-terminal: a session already Done the first time it is seen
//     is not a transition. Without this, opening the dashboard on a directory
//     of finished sessions fires a burst.
//   - stale-round-trip: the suppression that is impossible to get right by
//     accident. discover leaves Phase "" and AwaitingInput false on a stale
//     read, so a single transient stale tick makes a finished session appear
//     to re-cross into Done the moment it recovers.
//   - terminal-and-host-lost-together: a session reaching Done normally loses
//     its host; two notifications would describe one event twice.
//   - host-dies-while-cleaning: clean.go stops the host before `git worktree
//     remove`, and the two routinely straddle a tick. Fires on EVERY clean of
//     a running session if unsuppressed.
//   - host-death-already-reported: the attach-failure path. In production
//     markHostStopped's in-place edit usually removes the transition before
//     the diff sees it; this row pins the guarantee independently, with the
//     outgoing snapshot still reading the host as alive, so a refactor that
//     stopped mutating in place cannot make it silently regress.
//   - awaiting-input-on-the-open-pane: every conductor turn boundary flips
//     awaiting_input, so without this the attached session toasts forever.
//   - awaiting-input-on-the-hidden-pane: the suppression is for the session
//     being LOOKED AT. Behind the ctrl+t summary the user is not, so the
//     event fires; a rule keyed on paneName alone would swallow it.
func TestApplyLoadedNotifiesSessionTransitions(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(m *Model)
		beforeTick func(i int, m *Model)
		snapshots  [][]Session
		want       []backgroundEvent
	}{
		{
			name:      "running-to-done",
			snapshots: [][]Session{{sess("a")}, {phased(sess("a"), state.PhaseDone)}},
			want:      []backgroundEvent{{notifySuccess, bgFinished, "a finished"}},
		},
		{
			name:      "running-to-failed",
			snapshots: [][]Session{{sess("a")}, {phased(sess("a"), state.PhaseFailed)}},
			want:      []backgroundEvent{{notifyError, bgFailed, "a failed"}},
		},
		{
			name:      "working-to-awaiting-input",
			snapshots: [][]Session{{sess("a")}, {awaiting(sess("a"))}},
			want:      []backgroundEvent{{notifyWarn, bgAwaitingInput, "a is awaiting input"}},
		},
		{
			name:      "host-alive-to-dead",
			snapshots: [][]Session{{runningSess("a")}, {stoppedSess("a")}},
			want:      []backgroundEvent{{notifyWarn, bgHostDied, "a host died"}},
		},
		{
			name:      "no-change-tick",
			snapshots: [][]Session{{runningSess("a")}, {runningSess("a")}, {runningSess("a")}},
			want:      nil,
		},
		{
			// done→failed is a crossing BETWEEN terminal phases, which a
			// !old.Phase.Terminal() guard would swallow. A review verdict
			// landing after a session reported itself done is exactly that
			// sequence, and it is the half of the pair worth hearing about.
			name: "done-to-failed",
			snapshots: [][]Session{
				{phased(sess("a"), state.PhaseDone)},
				{phased(sess("a"), state.PhaseFailed)},
			},
			want: []backgroundEvent{{notifyError, bgFailed, "a failed"}},
		},
		{
			name: "already-terminal-in-both",
			snapshots: [][]Session{
				{phased(sess("a"), state.PhaseDone)},
				{phased(sess("a"), state.PhaseDone)},
			},
			want: nil,
		},
		{
			name:      "first-sight-terminal",
			snapshots: [][]Session{{phased(sess("a"), state.PhaseDone)}},
			want:      nil,
		},
		{
			name: "stale-round-trip",
			snapshots: [][]Session{
				{phased(sess("a"), state.PhaseDone)},
				{staleSess("a")},
				{phased(sess("a"), state.PhaseDone)},
			},
			want: nil,
		},
		{
			name: "awaiting-input-through-a-stale-tick",
			snapshots: [][]Session{
				{awaiting(sess("a"))},
				{staleSess("a")},
				{awaiting(sess("a"))},
			},
			want: nil,
		},
		{
			name: "terminal-and-host-lost-together",
			snapshots: [][]Session{
				{runningSess("a")},
				{phased(stoppedSess("a"), state.PhaseDone)},
			},
			want: []backgroundEvent{{notifySuccess, bgFinished, "a finished"}},
		},
		{
			name:  "host-dies-while-cleaning",
			setup: func(m *Model) { m.markCleaning(cleanIDByName("a")) },
			snapshots: [][]Session{
				{runningSess("a")},
				{stoppedSess("a")},
			},
			want: nil,
		},
		{
			name: "host-death-already-reported",
			// The PID is disproved BETWEEN the two ticks, so the outgoing
			// snapshot still reads the host as alive and there is a real
			// true→false transition for the rule to swallow. Setting it up
			// front instead would have the first tick's own
			// applyDeadHostOverrides flip the outgoing row, and the row would
			// then pass on markHostStopped's in-place edit rather than on the
			// rule under test.
			beforeTick: func(i int, m *Model) {
				if i == 1 {
					m.deadHostPIDs = map[string]int{"a": 4242}
				}
			},
			snapshots: [][]Session{
				{runningSess("a")},
				{runningSess("a")},
			},
			want: nil,
		},
		{
			name:  "awaiting-input-on-the-open-pane",
			setup: func(m *Model) { m.paneName = "a" },
			snapshots: [][]Session{
				{sess("a"), sess("b")},
				{awaiting(sess("a")), awaiting(sess("b"))},
			},
			want: []backgroundEvent{{notifyWarn, bgAwaitingInput, "b is awaiting input"}},
		},
		{
			name:  "awaiting-input-on-the-hidden-pane",
			setup: func(m *Model) { m.paneName = "a"; m.view = viewOverview },
			snapshots: [][]Session{
				{sess("a")},
				{awaiting(sess("a"))},
			},
			want: []backgroundEvent{{notifyWarn, bgAwaitingInput, "a is awaiting input"}},
		},
		{
			name: "two-sessions-transitioning-in-one-tick",
			snapshots: [][]Session{
				{sess("a"), runningSess("b")},
				{phased(sess("a"), state.PhaseDone), stoppedSess("b")},
			},
			// The events come in the list's order, which is the project
			// view's screen order (T16.4): b's dead host is Needs you, ahead
			// of a, which is Done.
			want: []backgroundEvent{
				{notifyWarn, bgHostDied, "b host died"},
				{notifySuccess, bgFinished, "a finished"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel()
			if tc.setup != nil {
				tc.setup(&m)
			}
			for i, snap := range tc.snapshots {
				if tc.beforeTick != nil {
					tc.beforeTick(i, &m)
				}
				m = m.applyLoaded(sessionsLoadedMsg{
					gen:      m.projectGen,
					sessions: snap,
					at:       time.Unix(int64(1_000_000+i), 0),
				})
			}
			if !slices.Equal(m.pendingBackground, tc.want) {
				t.Fatalf("background events = %+v, want %+v", m.pendingBackground, tc.want)
			}
		})
	}
}

// TestTwoTransitionsInOneTickCoalesce is the production path the table test
// above deliberately stops short of: two transitions from one snapshot have
// to reach the screen as ONE message, which only happens if the diff hands
// both to the arbiter inside a single Update. Driving applyLoaded directly
// would never exercise the flush.
func TestTwoTransitionsInOneTickCoalesce(t *testing.T) {
	m := testModel(sess("a"), runningSess("b"))
	m, _ = update(t, m, sessionsLoadedMsg{
		gen:      m.projectGen,
		sessions: []Session{phased(sess("a"), state.PhaseDone), stoppedSess("b")},
		at:       time.Unix(1_000_001, 0),
	})
	if want := "1 finished, 1 host died"; m.status != want {
		t.Fatalf("status = %q, want the coalesced %q", m.status, want)
	}
	if m.statusKind != notifyWarn {
		t.Fatalf("statusKind = %d, want notifyWarn (worst of success and warn)", m.statusKind)
	}
	if len(m.pendingBackground) != 0 {
		t.Fatalf("%d events still pending after the flush, want 0", len(m.pendingBackground))
	}
}

// TestSingleTransitionKeepsItsOwnText pins the other half of the coalescing
// contract through the same production path: a lone transition must land
// verbatim rather than as "1 finished".
func TestSingleTransitionKeepsItsOwnText(t *testing.T) {
	m := testModel(sess("a"))
	m, _ = update(t, m, sessionsLoadedMsg{
		gen:      m.projectGen,
		sessions: []Session{phased(sess("a"), state.PhaseFailed)},
		at:       time.Unix(1_000_001, 0),
	})
	if want := "a failed"; m.status != want {
		t.Fatalf("status = %q, want %q", m.status, want)
	}
	if m.statusKind != notifyError {
		t.Fatalf("statusKind = %d, want notifyError", m.statusKind)
	}
}

// --- DAG-aware Overview block and Waves deps suffix ---

// dagTask builds a plan task carrying a `**Depends on**` field. deps is
// copied into a non-nil slice even when empty, because state.Schedule tells a
// DAG plan from a legacy one by nil-ness alone: a dagTask with no deps is a
// `**Depends on**: none` task, never a legacy one.
func dagTask(id, title string, st state.CheckState, deps ...string) state.Task {
	return state.Task{ID: id, Title: title, State: st, DependsOn: append([]string{}, deps...)}
}

// legacyTask builds a plan task with no `**Depends on**` field (nil DependsOn).
func legacyTask(id, title string, st state.CheckState) state.Task {
	return state.Task{ID: id, Title: title, State: st}
}

// planOf groups tasks into waves by the number in their "W<n>." ID prefix, in
// first-appearance order, so a table row can list tasks flat.
func planOf(tasks ...state.Task) *state.Plan {
	p := &state.Plan{}
	index := map[int]int{}
	for _, t := range tasks {
		var n int
		fmt.Sscanf(t.ID, "W%d.", &n)
		i, ok := index[n]
		if !ok {
			i = len(p.Waves)
			index[n] = i
			p.Waves = append(p.Waves, state.Wave{Number: n, Name: fmt.Sprintf("Wave%d", n)})
		}
		p.Waves[i].Tasks = append(p.Waves[i].Tasks, t)
	}
	return p
}

// overviewLines renders the Overview for a session holding plan and returns
// its ANSI-stripped lines.
func overviewLines(plan *state.Plan, width int) []string {
	s := sess("dag")
	s.Plan = plan
	return strings.Split(xansi.Strip(strings.Join(overviewPanel{}.Render(panelContext{Session: s}, width), "\n")), "\n")
}

// TestOverviewTaskBlock pins the Overview's task block built from
// state.Schedule. Load-bearing rows: "dag W3 ready while W2 pending" is the
// reason the block exists (a current-wave block would show W2 only);
// "all terminal with failures" and "nothing ready, all blocked" guard against
// a `Ready (0)` heading; "legacy" proves the W2.T1 legacy rule (a failed
// previous wave does not block, and legacy never reports Blocked); the two cap
// rows pin the `+N more` lines. `order` is the sequence the lines must appear
// in, which is how (a) top line, (b) blocked count, (c) Failed section are
// pinned.
func TestOverviewTaskBlock(t *testing.T) {
	X, F, P := state.CheckComplete, state.CheckFailed, state.CheckIncomplete
	failedWith := func(task state.Task, reason string) state.Task {
		task.FailureReason = reason
		return task
	}

	cases := []struct {
		name   string
		plan   *state.Plan
		lines  []string // exact (trimmed-right) lines that must be present
		order  []string // substrings that must appear in this order
		banned []string
	}{
		{
			name: "dag W3 ready while W2 pending",
			plan: planOf(
				dagTask("W1.T1", "base", X),
				dagTask("W2.T1", "slow", P, "W1.T1"),
				dagTask("W3.T1", "fast", P, "W1.T1"),
				dagTask("W3.T2", "after slow", P, "W2.T1"),
			),
			lines:  []string{"Ready (2)", "  ○ W2.T1 slow", "  ○ W3.T1 fast"},
			order:  []string{"Ready (2)", "W2.T1", "W3.T1"},
			banned: []string{"W3.T2", "W1.T1", "Wave ", "←", "blocked", "Failed"},
		},
		{
			name: "ready capped at 5 with +N more",
			plan: planOf(
				dagTask("W1.T1", "a", P), dagTask("W1.T2", "b", P), dagTask("W1.T3", "c", P),
				dagTask("W1.T4", "d", P), dagTask("W1.T5", "e", P), dagTask("W1.T6", "f", P),
				dagTask("W1.T7", "g", P),
			),
			lines:  []string{"Ready (7)", "  +2 more"},
			order:  []string{"Ready (7)", "W1.T1", "W1.T5", "+2 more"},
			banned: []string{"W1.T6", "W1.T7"},
		},
		{
			name:   "all complete",
			plan:   planOf(legacyTask("W1.T1", "a", X), legacyTask("W2.T1", "b", X), legacyTask("W2.T2", "c", X)),
			lines:  []string{"All 3 tasks complete"},
			banned: []string{"Ready", "W1.T1", "W2.T1", "Failed", "blocked"},
		},
		{
			name: "all terminal with failures",
			plan: planOf(
				dagTask("W1.T1", "a", X),
				failedWith(dagTask("W2.T1", "b", F, "W1.T1"), "tests red"),
				dagTask("W2.T2", "c", X, "W1.T1"),
			),
			lines:  []string{"2 of 3 complete, 1 failed", "Failed (1)", "  ✗ W2.T1 b — tests red"},
			order:  []string{"2 of 3 complete, 1 failed", "Failed (1)", "W2.T1"},
			banned: []string{"Ready", "No tasks ready", "All 3"},
		},
		{
			name: "nothing ready, waiting on unmet deps",
			plan: planOf(
				dagTask("W1.T1", "a", X),
				dagTask("W2.T1", "b", P, "W9.T9"),
			),
			lines:  []string{"No tasks ready — waiting on unmet dependencies"},
			banned: []string{"Ready (", "blocked", "W2.T1"},
		},
		{
			name: "nothing ready, all blocked",
			plan: planOf(
				failedWith(dagTask("W1.T1", "a", F), "boom"),
				dagTask("W2.T1", "b", P, "W1.T1"),
			),
			lines:  []string{"No tasks ready", "1 blocked by failed dependencies", "Failed (1)", "  ✗ W1.T1 a — boom"},
			order:  []string{"No tasks ready", "1 blocked by failed dependencies", "Failed (1)", "W1.T1"},
			banned: []string{"waiting", "Ready (", "W2.T1"},
		},
		{
			name: "blocked count beside ready",
			plan: planOf(
				failedWith(dagTask("W1.T1", "a", F), "boom"),
				dagTask("W1.T2", "free", P),
				dagTask("W2.T1", "b", P, "W1.T1"),
				dagTask("W3.T1", "c", P, "W2.T1"),
			),
			lines: []string{"Ready (1)", "  ○ W1.T2 free", "2 blocked by failed dependencies", "Failed (1)"},
			order: []string{"Ready (1)", "W1.T2", "2 blocked by failed dependencies", "Failed (1)", "W1.T1"},
			// The Ready list passes no deps, so no suffix there.
			banned: []string{"←", "W2.T1", "W3.T1"},
		},
		{
			name: "failed capped at 3 with +N more",
			plan: planOf(
				dagTask("W1.T1", "a", F), dagTask("W1.T2", "b", F), dagTask("W1.T3", "c", F),
				dagTask("W1.T4", "d", F), dagTask("W1.T5", "e", F), dagTask("W1.T6", "f", X),
			),
			lines:  []string{"1 of 6 complete, 5 failed", "Failed (5)", "  +2 more"},
			order:  []string{"Failed (5)", "W1.T1", "W1.T3", "+2 more"},
			banned: []string{"W1.T4", "W1.T5", "W1.T6"},
		},
		{
			name: "legacy",
			plan: planOf(
				legacyTask("W1.T1", "first", P),
				legacyTask("W2.T1", "mid", X),
				legacyTask("W2.T2", "mid2", X),
				legacyTask("W3.T1", "last", P),
			),
			lines:  []string{"Ready (2)", "  ○ W1.T1 first", "  ○ W3.T1 last"},
			order:  []string{"Ready (2)", "W1.T1", "W3.T1"},
			banned: []string{"W2.T1", "blocked", "←"},
		},
		{
			name: "legacy failed wave does not block",
			plan: planOf(
				failedWith(legacyTask("W1.T1", "a", F), "boom"),
				legacyTask("W2.T1", "b", P),
			),
			lines:  []string{"Ready (1)", "  ○ W2.T1 b", "Failed (1)", "  ✗ W1.T1 a — boom"},
			order:  []string{"Ready (1)", "W2.T1", "Failed (1)", "W1.T1"},
			banned: []string{"blocked", "No tasks ready"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := overviewLines(tc.plan, 80)
			out := strings.Join(lines, "\n")
			have := map[string]bool{}
			for _, ln := range lines {
				have[strings.TrimRight(ln, " ")] = true
			}
			for _, want := range tc.lines {
				if !have[want] {
					t.Errorf("Overview missing line %q:\n%s", want, out)
				}
			}
			at := 0
			for _, want := range tc.order {
				i := strings.Index(out[at:], want)
				if i < 0 {
					t.Errorf("Overview missing %q after offset %d (order %q):\n%s", want, at, tc.order, out)
					break
				}
				at += i + len(want)
			}
			for _, banned := range append(tc.banned, "Ready (0)") {
				if strings.Contains(out, banned) {
					t.Errorf("Overview should not show %q:\n%s", banned, out)
				}
			}
		})
	}
}

// TestWavesDepsSuffix pins the dim `← deps` suffix on Waves rows.
// Load-bearing rows: "dag" (only unmet deps are listed, only on `[ ]` rows —
// complete and failed rows stay as they were even with an unmet dep), and
// "mixed presence" (state.Schedule displays it as legacy, so the Waves tab
// must agree with the Overview and show no suffix). "narrow" proves the
// suffix is the part that gets cut, never the glyph or the ID.
func TestWavesDepsSuffix(t *testing.T) {
	X, F, P := state.CheckComplete, state.CheckFailed, state.CheckIncomplete
	dag := planOf(
		dagTask("W1.T1", "done", X),
		dagTask("W1.T2", "broke", F),
		dagTask("W2.T1", "met", P, "W1.T1"),
		dagTask("W2.T2", "unmet", P, "W1.T1", "W1.T2", "W2.T1"),
		dagTask("W2.T3", "complete row", X, "W9.T9"),
		dagTask("W2.T4", "failed row", F, "W9.T9"),
		dagTask("W2.T5", "unknown dep", P, "W9.T9"),
	)

	cases := []struct {
		name  string
		plan  *state.Plan
		width int
		want  map[string]string // task ID -> exact rendered line (trimmed right)
	}{
		{
			name:  "dag",
			plan:  dag,
			width: 80,
			want: map[string]string{
				"W1.T1": "  ✓ W1.T1 done",
				"W1.T2": "  ✗ W1.T2 broke",
				"W2.T1": "  ○ W2.T1 met",
				"W2.T2": "  ○ W2.T2 unmet ← W1.T2, W2.T1",
				"W2.T3": "  ✓ W2.T3 complete row",
				"W2.T4": "  ✗ W2.T4 failed row",
				"W2.T5": "  ○ W2.T5 unknown dep ← W9.T9",
			},
		},
		{
			name:  "legacy",
			plan:  planOf(legacyTask("W1.T1", "a", P), legacyTask("W2.T1", "b", P)),
			width: 80,
			want:  map[string]string{"W1.T1": "  ○ W1.T1 a", "W2.T1": "  ○ W2.T1 b"},
		},
		{
			name:  "mixed presence",
			plan:  planOf(dagTask("W1.T1", "a", P), legacyTask("W2.T1", "b", P), dagTask("W3.T1", "c", P, "W2.T1")),
			width: 80,
			want:  map[string]string{"W1.T1": "  ○ W1.T1 a", "W2.T1": "  ○ W2.T1 b", "W3.T1": "  ○ W3.T1 c"},
		},
		{
			name:  "narrow",
			plan:  dag,
			width: 21,
			want: map[string]string{
				// The title keeps its room; the suffix takes what is left.
				"W2.T2": "  ○ W2.T2 unmet ← W1…",
				// The title fills the line exactly: no room, no suffix.
				"W2.T5": "  ○ W2.T5 unknown dep",
				"W2.T3": "  ✓ W2.T3 complete r…",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sess("dag")
			s.Plan = tc.plan
			rendered := wavesPanel{}.Render(panelContext{Session: s}, tc.width)
			got := map[string]string{}
			for _, ln := range rendered {
				if w := lipgloss.Width(ln); w > tc.width {
					t.Errorf("line %q is %d wide, want <= %d", xansi.Strip(ln), w, tc.width)
				}
				plain := strings.TrimRight(xansi.Strip(ln), " ")
				// A task row is "  <glyph> <ID> ...": the ID is the second field.
				if f := strings.Fields(plain); len(f) >= 2 && strings.HasPrefix(plain, "  ") {
					got[f[1]] = plain
				}
			}
			for id, want := range tc.want {
				if got[id] != want {
					t.Errorf("%s row = %q, want %q", id, got[id], want)
				}
			}
		})
	}
}

// TestRenderRowAutonomousFlag pins the single flag cell's priority:
// wedged > awaiting > autonomous > blank. Load-bearing rows: "wedged beats
// autonomous" and "awaiting beats autonomous" (the attention flags must never
// be displaced by an informational one), and "not autonomous" (no stray
// glyph). Every row must also keep the row width, because the glyph has to
// fit rowFlagCells.
func TestRenderRowAutonomousFlag(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	mk := func(autonomous, awaiting, wedged bool) Session {
		st := sampleState("a")
		st.Phase = state.PhaseExecuting
		st.HostRunning = true
		st.HostPID = 4242
		st.Autonomous = autonomous
		st.AwaitingInput = awaiting
		if wedged {
			st.WedgedSince = now.Add(-time.Minute).Format(time.RFC3339)
		}
		return Session{SessionState: st}
	}
	tests := []struct {
		name                    string
		s                       Session
		wantAuto, wantW, wantAw bool
	}{
		{"autonomous alone shows the glyph", mk(true, false, false), true, false, false},
		{"not autonomous shows no glyph", mk(false, false, false), false, false, false},
		{"awaiting beats autonomous", mk(true, true, false), false, false, true},
		{"wedged beats autonomous", mk(true, false, true), false, true, false},
		{"wedged beats awaiting and autonomous", mk(true, true, true), false, true, false},
	}
	auto := theme.dim.Render(autonomousGlyph)
	awaiting := theme.awaitingFlag.Render(awaitingGlyph)
	wedged := theme.errorText.Render(wedgedGlyph)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, selected := range []bool{false, true} {
				row := renderSessionRow(tc.s, selected, 60, now)
				if got := rowFlagShown(t, row, auto, autonomousGlyph, selected); got != tc.wantAuto {
					t.Errorf("selected=%v: autonomous glyph shown=%v, want %v: %q", selected, got, tc.wantAuto, row)
				}
				if got := rowFlagShown(t, row, wedged, wedgedGlyph, selected); got != tc.wantW {
					t.Errorf("selected=%v: wedged glyph shown=%v, want %v: %q", selected, got, tc.wantW, row)
				}
				if got := rowFlagShown(t, row, awaiting, awaitingGlyph, selected); got != tc.wantAw {
					t.Errorf("selected=%v: awaiting flag shown=%v, want %v: %q", selected, got, tc.wantAw, row)
				}
				if w := lipgloss.Width(strings.TrimRight(row, " ")); w > 60 {
					t.Errorf("selected=%v: row width %d exceeds 60: %q", selected, w, row)
				}
			}
		})
	}
	if w := lipgloss.Width(autonomousGlyph); w != 1 {
		t.Errorf("autonomousGlyph is %d cells wide, want 1 (rowFlagCells)", w)
	}
}

// TestRenderRowAutonomousKeepsColumns proves the glyph does not shift any
// column: an autonomous row and an otherwise identical plain row are the same
// width at every width rowColumns handles, so the flag cell really is the cell
// the blank case already reserved.
func TestRenderRowAutonomousKeepsColumns(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, width := range []int{20, 40, 60, 80, 120} {
		plain := sess("alpha")
		auto := sess("alpha")
		auto.Autonomous = true
		a := lipgloss.Width(renderSessionRow(plain, false, width, now))
		b := lipgloss.Width(renderSessionRow(auto, false, width, now))
		if a != b {
			t.Errorf("width %d: plain row %d cells, autonomous row %d", width, a, b)
		}
	}
}

// TestOverviewAutonomousTag pins the head-line tag. The stale row is the
// load-bearing one: the status line is replaced by stale text there, so a tag
// parked on it would vanish exactly when the status is least trustworthy.
func TestOverviewAutonomousTag(t *testing.T) {
	tests := []struct {
		name       string
		autonomous bool
		stale      bool
		want       bool
	}{
		{"autonomous live", true, false, true},
		{"autonomous stale", true, true, true},
		{"not autonomous live", false, false, false},
		{"not autonomous stale", false, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := sess("feat-a")
			s.Autonomous = tc.autonomous
			s.StatusStale = tc.stale
			s.StaleReason = state.StaleUnparseable
			lines := overviewPanel{}.Render(panelContext{Session: s}, 80)
			head := xansi.Strip(lines[0])
			if got := strings.Contains(head, "autonomous"); got != tc.want {
				t.Errorf("head %q: tag present=%v, want %v", head, got, tc.want)
			}
			for _, l := range lines[1:] {
				if strings.Contains(xansi.Strip(l), "autonomous") {
					t.Errorf("tag leaked below the head line: %q", l)
				}
			}
		})
	}
}

// TestRenderRowProviderTag pins the row's provider tag. Load-bearing rows:
// "claude row unchanged" (the default provider must never print a tag, so
// every existing golden stays put) and "long name truncates before the tag"
// (the tag survives, the name pays).
func TestRenderRowProviderTag(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	codex := sess("alpha")
	codex.Provider = "codex"
	long := sess("a-very-long-session-name-indeed")
	long.Provider = "codex"

	if row := xansi.Strip(renderSessionRow(codex, false, 60, now)); !strings.Contains(row, "alpha codex") {
		t.Errorf("codex row missing tag: %q", row)
	}
	if row := xansi.Strip(renderSessionRow(sess("alpha"), false, 60, now)); strings.Contains(row, "codex") {
		t.Errorf("claude row grew a tag: %q", row)
	}
	row := xansi.Strip(renderSessionRow(long, false, 40, now))
	if !strings.Contains(row, "… codex") || strings.Contains(row, "indeed") {
		t.Errorf("long name should truncate and keep the tag: %q", row)
	}
	// Too narrow for tag plus a usable name: the tag yields, the name stays.
	if row := xansi.Strip(renderSessionRow(long, false, 12, now)); strings.Contains(row, "codex") || !strings.Contains(row, "a-ve") {
		t.Errorf("narrow row should drop the tag and keep the name: %q", row)
	}
}

// TestRenderRowProviderKeepsColumns proves the tag costs no column: a codex
// row and an otherwise identical claude row are the same width, and PHASE
// starts at the same cell, at every width rowColumns handles.
func TestRenderRowProviderKeepsColumns(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, width := range []int{20, 24, 32, 40, 60, 80, 120} {
		for _, name := range []string{"alpha", "a-very-long-session-name-indeed"} {
			plain := sess(name)
			cx := sess(name)
			cx.Provider = "codex"
			a := xansi.Strip(renderSessionRow(plain, false, width, now))
			b := xansi.Strip(renderSessionRow(cx, false, width, now))
			if lipgloss.Width(a) != lipgloss.Width(b) {
				t.Errorf("width %d %q: claude row %d cells, codex row %d", width, name, lipgloss.Width(a), lipgloss.Width(b))
			}
			if i, j := strings.Index(a, "EXEC"), strings.Index(b, "EXEC"); i != j {
				t.Errorf("width %d %q: phase column moved %d -> %d", width, name, i, j)
			}
		}
	}
}

// TestOverviewProviderTag pins the head-line tag, on a stale status too (the
// status line is replaced there, so the tag must live on the head).
func TestOverviewProviderTag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		stale    bool
		want     bool
	}{
		{"codex live", "codex", false, true},
		{"codex stale", "codex", true, true},
		{"claude live", "", false, false},
		{"claude stale", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := sess("feat-a")
			s.Provider = tc.provider
			s.StatusStale = tc.stale
			s.StaleReason = state.StaleUnparseable
			lines := overviewPanel{}.Render(panelContext{Session: s}, 80)
			if got := strings.Contains(xansi.Strip(lines[0]), "codex"); got != tc.want {
				t.Errorf("head %q: tag present=%v, want %v", xansi.Strip(lines[0]), got, tc.want)
			}
			for _, l := range lines[1:] {
				if strings.Contains(xansi.Strip(l), "codex") {
					t.Errorf("tag leaked below the head line: %q", l)
				}
			}
		})
	}
}

// TestSessionFilterValueIncludesProvider: typing "codex" must find a codex
// row, with or without a label, and must not match a claude row.
func TestSessionFilterValueIncludesProvider(t *testing.T) {
	cx := sess("alpha")
	cx.Provider = "codex"
	lab := sess("beta")
	lab.Label = "Pretty"
	lab.Provider = "codex"
	for _, s := range []Session{cx, lab} {
		if v := (sessionItem{session: s}).FilterValue(); !strings.Contains(v, "codex") || !strings.Contains(v, s.Name) {
			t.Errorf("FilterValue %q missing provider or name", v)
		}
	}
	if v := (sessionItem{session: sess("alpha")}).FilterValue(); strings.Contains(v, "codex") || v != "alpha" {
		t.Errorf("claude FilterValue = %q, want %q", v, "alpha")
	}
}

// TestRestartToastNamesProvider: the success toast names the session's
// provider, defaulting to claude for an empty one or an unknown session.
func TestRestartToastNamesProvider(t *testing.T) {
	cx := wedgedSess("alpha", 12*time.Minute)
	cx.Provider = "codex"
	for _, tc := range []struct {
		name, msgName string
		m             Model
		want, banned  string
	}{
		{"codex", "alpha", testModel(cx), "codex is resuming its conversation", "claude"},
		{"claude", "alpha", testModel(wedgedSess("alpha", 12*time.Minute)), "claude is resuming its conversation", "codex"},
		{"session gone", "ghost", testModel(wedgedSess("alpha", 12*time.Minute)), "claude is resuming its conversation", "codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := update(t, tc.m, restartChildRequestedMsg{name: tc.msgName, restarted: true})
			if !strings.Contains(got.status, tc.want) || strings.Contains(got.status, tc.banned) {
				t.Errorf("status %q: want %q, not %q", got.status, tc.want, tc.banned)
			}
		})
	}
}

// TestMovingOffThePanedRowDetachesIt pins W3.T1(a): with the list focused, the
// pane follows the selection. A pane whose row the selection leaves is
// detached at once, so it never holds its host's single client slot while
// undrawn. Rows:
//
//   - "cursor move" is the common path (browsing away), and also proves the
//     preview for the new row still arms: detaching must not cost the browse.
//   - "behind the overview" is load-bearing because the undrawn-slot hazard
//     is WORST there: the pane is invisible in either case.
//   - "tick reshuffle" moves the selection with no key; holdSelection, not
//     schedulePreview, has to catch it.
func TestMovingOffThePanedRowDetachesIt(t *testing.T) {
	attached := func(t *testing.T) (Model, *fakePane) {
		fp := newFakePane("A")
		m := testModel(runningSess("a"), runningSess("b"))
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
		if m.pane != fp || m.focus != focusList {
			t.Fatalf("setup: pane a should be installed with the list focused")
		}
		return m, fp
	}

	t.Run("cursor move", func(t *testing.T) {
		m, fp := attached(t)
		m, cmd := update(t, m, keyDown)
		if m.pane != nil || m.paneName != "" || fp.closed != 1 {
			t.Fatalf("after the move: pane=%v name=%q closed=%d, want detached exactly once", m.pane != nil, m.paneName, fp.closed)
		}
		if cmd == nil {
			t.Fatalf("the move onto running b must still arm its preview")
		}
	})

	t.Run("behind the overview", func(t *testing.T) {
		m, fp := attached(t)
		m.view = viewOverview
		m, _ = update(t, m, keyDown)
		if m.pane != nil || fp.closed != 1 {
			t.Fatalf("a pane behind the overview survived the move: pane=%v closed=%d", m.pane != nil, fp.closed)
		}
	})

	t.Run("tick reshuffle", func(t *testing.T) {
		m, fp := attached(t)
		// The list is kept in screen order (T16.4), so the reshuffle is b
		// starting to ask the owner something: it moves to Needs you, ahead
		// of a, and the held index lands on it.
		asks := runningSess("b")
		asks.AwaitingInput = true
		m, _ = update(t, m, sessionsLoadedMsg{
			gen:      m.projectGen,
			sessions: []Session{runningSess("a"), asks},
			at:       time.Unix(3, 0),
		})
		if s, _ := m.selected(); s.Name != "b" {
			t.Fatalf("setup: with the list focused the index holds, so b should now be selected; got %q", s.Name)
		}
		if m.pane != nil || fp.closed != 1 {
			t.Fatalf("the tick moved the selection off a but its pane survived: pane=%v closed=%d", m.pane != nil, fp.closed)
		}
	})
}

// TestLostPaneKeepsTheKeyboardOnTheAgentPlaceholder pins W3.T1(c): a pane that
// ends under a FOCUSED keyboard without its session ending — stolen, dropped,
// or a read error — keeps focus right in the agent view, draws a reconnect
// placeholder, swallows everything but its own keys, and reconnects with one
// dial on enter. Every row is load-bearing: the three reasons reach
// paneExitedMsg with different fields (reason vs err), and the q/c/n presses
// are exactly the list bindings (quit, clean, new) a keyboard handed back to
// the list would have fired. The session-ended path is the counterpart and
// stays in TestPaneTeardownHandsFocusBack.
func TestLostPaneKeepsTheKeyboardOnTheAgentPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exit     func(p paneHandle) paneExitedMsg
		wantLine string
	}{
		{"stolen", func(p paneHandle) paneExitedMsg {
			return paneExitedMsg{pane: p, reason: host.DetachStolen}
		}, "a was taken over by another window"},
		{"dropped", func(p paneHandle) paneExitedMsg {
			return paneExitedMsg{pane: p, reason: host.DetachDropped}
		}, "disconnected from a"},
		{"read error", func(p paneHandle) paneExitedMsg {
			return paneExitedMsg{pane: p, err: errors.New("boom")}
		}, "disconnected from a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("a"), runningSess("b"))
			opens := 0
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				opens++
				return newFakePane("A"), nil
			}
			m, cmd := update(t, m, keyEnter)
			m = settleConnect(t, m, cmd)
			if !m.paneOwnsKeyboard() || opens != 1 {
				t.Fatalf("setup: enter should land a focused pane with one dial (focused=%v opens=%d)", m.paneOwnsKeyboard(), opens)
			}

			m, _ = update(t, m, tc.exit(m.pane))
			if m.pane != nil || m.focus != focusRight || m.view != viewAgent {
				t.Fatalf("after the loss: pane=%v focus=%v view=%v, want no pane, right, agent", m.pane != nil, m.focus, m.view)
			}
			out := render(m)
			for _, want := range []string{tc.wantLine, "enter to reconnect"} {
				if !strings.Contains(out, want) {
					t.Fatalf("placeholder missing %q:\n%s", want, out)
				}
			}

			for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'c', Text: "c"}, {Code: 'n', Text: "n"}, {Code: 'j', Text: "j"}} {
				var kc tea.Cmd
				m, kc = update(t, m, k)
				if kc != nil || m.quitting || m.formOpen || m.modal != inputNormal {
					t.Fatalf("%q leaked to a list binding: cmd=%v quitting=%v form=%v modal=%v", k.String(), kc != nil, m.quitting, m.formOpen, m.modal)
				}
				if s, _ := m.selected(); s.Name != "a" {
					t.Fatalf("%q moved the selection to %q", k.String(), s.Name)
				}
			}

			m, cmd = update(t, m, keyEnter)
			if cmd == nil {
				t.Fatalf("enter on the placeholder must reconnect")
			}
			m = settleConnect(t, m, cmd)
			if opens != 2 {
				t.Fatalf("dials = %d after the reconnect, want exactly one more (2)", opens)
			}
			if m.paneName != "a" || !m.paneOwnsKeyboard() {
				t.Fatalf("reconnect landed paneName=%q focused=%v, want a focused", m.paneName, m.paneOwnsKeyboard())
			}
			if strings.Contains(render(m), "enter to reconnect") {
				t.Fatalf("the lost-pane placeholder outlived the reconnect")
			}
		})
	}

	t.Run("ctrl+t and ctrl+\\ still act", func(t *testing.T) {
		m := testModel(runningSess("a"))
		fp := newFakePane("A")
		m.focus = focusRight // connect moves the focus at request time
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a", focusOnArrival: true})
		m, _ = update(t, m, paneExitedMsg{pane: fp, reason: host.DetachStolen})

		m, cmd := flipView(t, m)
		if cmd != nil || m.view != viewOverview || m.focus != focusRight {
			t.Fatalf("ctrl+t: cmd=%v view=%v focus=%v, want a pure flip to the overview", cmd != nil, m.view, m.focus)
		}
		// Back to the agent view: a lost row has no pane, so this reconnects
		// (the Cmd is left unsettled). While that dial is out the placeholder
		// says so and offers no enter, because enter would only join it.
		m, cmd = flipView(t, m)
		if cmd == nil {
			t.Fatalf("ctrl+t back to the agent view should reconnect the lost row")
		}
		if _, line, action := m.agentPlaceholderText(); line != "connecting to a…" || action != "" {
			t.Fatalf("with the reconnect out: line=%q action=%q, want the dial named and no enter offer", line, action)
		}
		m, _ = pressAction(t, m, keyEscMsg())
		if m.focus != focusList || m.view != viewAgent {
			t.Fatalf("ctrl+\\: focus=%v view=%v, want list in the agent view", m.focus, m.view)
		}
		// The lost note outlives the release: if the reconnect fails, the
		// placeholder goes back to naming why the row was lost the next time
		// enter goes into the Agent. At Project the overview is drawn, not
		// the placeholder (W3.T1 of T16.2).
		if m.agentPlaceholderDrawn() || m.paneLost.name != "a" || m.paneLost.message != "a was taken over by another window or the web page; press enter to reopen it" {
			t.Fatalf("the lost note should stay on the row after the keyboard leaves: drawn=%v lost=%+v", m.agentPlaceholderDrawn(), m.paneLost)
		}
	})
}

// TestNoLiveAgentRowDrawsItsOverviewInTheAgentView pins W3.T1(d): a row with
// no live agent (stopped, finished, pending, worktree fault, cleaning) draws
// its OVERVIEW on the right whatever the sticky view says, with the list or
// the right side focused, and the view preference itself is left on the agent
// so the next running row still shows its agent. The two running rows are the
// contrast: list-focused it is one preview away from its agent and shows the
// overview meanwhile; right-focused with no pane it is the agent placeholder.
func TestNoLiveAgentRowDrawsItsOverviewInTheAgentView(t *testing.T) {
	done := stoppedSess("d")
	done.Phase = state.PhaseDone
	fault := runningSess("f")
	fault.Kind = session.KindWorktreeMissing
	for _, tc := range []struct {
		name            string
		row             Session
		arrange         func(m *Model)
		wantPlaceholder bool
		// focusOnly pins the row to the focus arrange set; the others are
		// asserted with the list AND the right side focused.
		focusOnly bool
		want      []string
	}{
		{name: "stopped", row: stoppedSess("s")},
		{name: "finished", row: done},
		{name: "pending", row: Session{SessionState: discover.SessionState{Name: "p"}, Pending: true}},
		{name: "worktree fault", row: fault},
		{name: "cleaning", row: runningSess("c"),
			arrange: func(m *Model) { m.markCleaning(cleanIDByName("c")) }},
		{name: "running, list focused", row: runningSess("r"), focusOnly: true},
		{name: "running, right focused", row: runningSess("r"),
			arrange:         func(m *Model) { m.focus = focusRight },
			wantPlaceholder: true, focusOnly: true, want: []string{"r is running", "enter to connect"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(tc.row)
			if tc.arrange != nil {
				tc.arrange(&m)
			}
			focuses := []paneFocus{focusList, focusRight}
			if tc.focusOnly {
				focuses = []paneFocus{m.focus}
			}
			for _, focus := range focuses {
				m.focus = focus
				if got := m.agentPlaceholderDrawn(); got != tc.wantPlaceholder {
					t.Fatalf("focus=%v: placeholder drawn = %v, want %v", focus, got, tc.wantPlaceholder)
				}
				if !tc.wantPlaceholder && !m.detailDrawn() {
					t.Fatalf("focus=%v: the overview is not drawn", focus)
				}
				if m.view != viewAgent {
					t.Fatalf("focus=%v: view = %v, want the sticky agent preference untouched", focus, m.view)
				}
			}
			out := render(m)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q:\n%s", w, out)
				}
			}
		})
	}
}

// TestSecondRestartKeyIsInertWhileOneIsInFlight: `r` twice on a stopped row
// spawns ONE host — a second spawn's bind probe steals the first host's pane.
// While the restart runs the row has an agent coming, so the agent view draws
// the "restarting" placeholder, and enter JOINS the restart (focus right, no
// second dispatch) so the arrival lands focused.
func TestSecondRestartKeyIsInertWhileOneIsInFlight(t *testing.T) {
	m := testModel(stoppedSess("a"))
	spawns := 0
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		spawns++
		return session.HostProcess{PID: 4242}, nil
	}
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("A"), nil }

	m, first := pressAction(t, m, keyRune('r'))
	if first == nil {
		t.Fatalf("r on a stopped row must dispatch the restart")
	}
	if !strings.Contains(render(m), "restarting a…") {
		t.Fatalf("placeholder should say the restart is in flight:\n%s", render(m))
	}
	// The dialog dims restart while one is in flight; the handler's own
	// refusal is what this row pins (see refuseAction).
	m, second := refuseAction(m, "r")
	if second != nil {
		t.Fatalf("a second r during the restart dispatched %T, want it inert", second())
	}
	m, joined := update(t, m, keyEnter)
	if joined != nil || m.focus != focusRight {
		t.Fatalf("enter during the restart: cmd=%v focus=%v, want it to join (no Cmd, focus right)", joined != nil, m.focus)
	}
	m = settleConnect(t, m, first)
	if spawns != 1 {
		t.Fatalf("spawns = %d, want 1", spawns)
	}
	if m.paneName != "a" || !m.paneOwnsKeyboard() {
		t.Fatalf("settled paneName=%q focused=%v, want a focused", m.paneName, m.paneOwnsKeyboard())
	}
}

// TestFocusedConnectAbandonedByACursorMoveIsDropped: enter on a, ctrl+\ back
// to the list, then a move to b before a's dial lands. The arrival belongs to
// a row that is no longer selected; installing it would hold a's client slot
// undrawn, and focusing it would put the keyboard in a session the frame does
// not show.
func TestFocusedConnectAbandonedByACursorMoveIsDropped(t *testing.T) {
	fp := newFakePane("A")
	m := testModel(runningSess("a"), runningSess("b"))
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return fp, nil }

	m, dial := update(t, m, keyEnter)
	if dial == nil {
		t.Fatalf("setup: enter must dial a")
	}
	m, _ = pressAction(t, m, keyEscMsg())
	m, _ = update(t, m, keyDown)
	m, _ = update(t, m, dial())
	if m.pane != nil || fp.closed != 1 {
		t.Fatalf("abandoned arrival: pane=%v closed=%d, want dropped and closed once", m.pane != nil, fp.closed)
	}
	if m.focus != focusList {
		t.Fatalf("focus = %v, want the list the user went back to", m.focus)
	}
}

// TestRestartKeyMatrix pins `r` as the one restart key (plan W3.T1 d2) across
// every surface it can be pressed on. Load-bearing rows:
//
//   - "stopped, list" and "stopped, focused overview" are the acceptance
//     pair: exactly one restart chain (one spawn, then one open) from either
//     side, no confirmation — respawning a stopped host destroys nothing. The
//     two differ only in where the keyboard ends up: the list keeps it, the
//     right side gets the arriving agent.
//   - "live agent pane" is the negative that keeps `r` typeable: in a focused
//     pane it is the child's key, so it forwards and restarts nothing.
//   - the two "wedged" rows keep today's confirm flow for a running host,
//     from the list and from the overview alike.
//   - "healthy running" is inert: there is nothing to restart.
func TestRestartKeyMatrix(t *testing.T) {
	type outcome struct {
		spawns, opens int
		modal         inputMode
		forwarded     bool
		focus         paneFocus
		ownsKeyboard  bool
	}
	for _, tc := range []struct {
		name  string
		row   Session
		setup func(t *testing.T, m Model) Model
		// bare sends a plain r instead of ctrl+] r: the pane row's point
		// is that the letter is still the child's.
		bare bool
		want outcome
	}{
		{
			name: "stopped, list",
			row:  stoppedSess("a"),
			want: outcome{spawns: 1, opens: 1, focus: focusList},
		},
		{
			name: "stopped, focused overview",
			row:  stoppedSess("a"),
			setup: func(t *testing.T, m Model) Model {
				m, _ = update(t, m, keyEnter)
				if !m.detailOwnsKeyboard() {
					t.Fatal("setup: enter on a stopped row must focus its overview")
				}
				return m
			},
			want: outcome{spawns: 1, opens: 1, focus: focusRight, ownsKeyboard: true},
		},
		{
			name: "live agent pane",
			row:  runningSess("a"),
			setup: func(t *testing.T, m Model) Model {
				m, cmd := update(t, m, keyEnter)
				m = settleConnect(t, m, cmd)
				if !m.paneOwnsKeyboard() {
					t.Fatal("setup: enter on a running row must land a focused pane")
				}
				return m
			},
			bare: true,
			want: outcome{opens: 1, forwarded: true, focus: focusRight, ownsKeyboard: true},
		},
		{
			name: "wedged, list",
			row:  wedgedSess("a", 12*time.Minute),
			want: outcome{modal: inputConfirmRestart, focus: focusList},
		},
		{
			name: "wedged, focused overview",
			row:  wedgedSess("a", 12*time.Minute),
			setup: func(t *testing.T, m Model) Model {
				return focusOverview(t, m)
			},
			want: outcome{modal: inputConfirmRestart, focus: focusRight},
		},
		{
			name: "healthy running, list",
			row:  runningSess("a"),
			want: outcome{focus: focusList},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(tc.row)
			var got outcome
			var pane *fakePane
			m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
				got.spawns++
				return session.HostProcess{PID: 4242}, nil
			}
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				got.opens++
				pane = newFakePane("A")
				return pane, nil
			}
			if tc.setup != nil {
				m = tc.setup(t, m)
			}

			var cmd tea.Cmd
			if tc.bare {
				m, cmd = update(t, m, keyRune('r'))
			} else {
				m, cmd = pressAction(t, m, keyRune('r'))
			}
			m = settleConnect(t, m, cmd)

			got.modal = m.modal
			got.focus = m.focus
			got.ownsKeyboard = m.paneOwnsKeyboard()
			got.forwarded = pane != nil && len(pane.keys)+len(pane.texts) > 0
			if got != tc.want {
				t.Fatalf("r = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestTakenOverNoticeSaysWhatHappenedAndHowToReopen pins the plain-words
// sentence for a pane the host reports as Stolen. The load-bearing rows are
// the shared text in both places (status notice and placeholder, so they
// cannot drift), the absence of any claim that it was the web page (the
// Stolen frame cannot say), and the host staying running: a steal is not a
// death.
func TestTakenOverNoticeSaysWhatHappenedAndHowToReopen(t *testing.T) {
	const want = "a was taken over by another window or the web page; press enter to reopen it"
	fp := newFakePane("X")
	m := testModel(runningSess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	m, _ = update(t, m, paneExitedMsg{pane: fp, reason: host.DetachStolen})

	if m.status != want {
		t.Fatalf("status notice = %q, want %q", m.status, want)
	}
	if m.paneLost.name != "a" || m.paneLost.message != want {
		t.Fatalf("placeholder note = %+v, want name a and message %q", m.paneLost, want)
	}
	if !m.sessions[0].HostRunning {
		t.Fatalf("HostRunning = false after a steal; the session is still running under the other client")
	}
	if strings.Contains(m.status, "another client") {
		t.Fatalf("status %q uses the old jargon", m.status)
	}
}

// --- start level (T16.2 W4.T1) ---

// startLevelModel builds a Model as `afk dash` does for a start with the given
// level and no project in view (index -1), with a wired All projects reader.
func startLevelModel(level StartLevel) Model {
	deps := testDeps()
	deps.StartLevel = level
	deps.ActiveProjectIndex = -1
	deps.LoadAllProjects = func() AllProjects { return AllProjects{} }
	return New("/tmp", time.Second, deps)
}

// TestStartAllProjectsDoesNotPromoteTheFirstProject pins the change from
// "launched outside a repo, the first project becomes active" to "All projects
// is shown and no project is active". Load-bearing rows: the second scan (the
// minute-by-minute rebuild must not promote either), the zero StartLevel (the
// older promotion stays for a Model built without a decision) and the missing
// reader (without it the screen cannot be drawn, so promotion is the only way
// to show a project).
func TestStartAllProjectsDoesNotPromoteTheFirstProject(t *testing.T) {
	scan := projectScanMsg{projects: scannedProjects("/roots/a", "/roots/b")}

	t.Run("elsewhere with projects opens All projects", func(t *testing.T) {
		m := startLevelModel(StartAllProjects)
		m, cmd := update(t, m, scan)
		if !m.allProjectsOpen || m.hasActiveProject() || m.level() != levelAllProjects {
			t.Fatalf("open=%t active=%t level=%v, want All projects with no active project",
				m.allProjectsOpen, m.hasActiveProject(), m.level())
		}
		if cmd == nil {
			t.Error("opening the screen must start its first read")
		}
		gen := m.allProjectsGen
		m, _ = update(t, m, scan)
		if !m.allProjectsOpen || m.hasActiveProject() || m.allProjectsGen != gen {
			t.Errorf("a second scan changed the start: open=%t active=%t gen %d -> %d",
				m.allProjectsOpen, m.hasActiveProject(), gen, m.allProjectsGen)
		}
	})
	t.Run("elsewhere with none keeps the empty state", func(t *testing.T) {
		m := startLevelModel(StartAllProjects)
		m, _ = update(t, m, projectScanMsg{})
		if m.allProjectsOpen || m.hasActiveProject() {
			t.Errorf("open=%t active=%t, want the empty state (screen closed, no project)",
				m.allProjectsOpen, m.hasActiveProject())
		}
	})
	t.Run("zero StartLevel still promotes the first project", func(t *testing.T) {
		m := startLevelModel(StartProject)
		m, _ = update(t, m, scan)
		if m.allProjectsOpen || m.activeProjectPath() != "/roots/a" {
			t.Errorf("open=%t active=%q, want /roots/a promoted", m.allProjectsOpen, m.activeProjectPath())
		}
	})
	t.Run("no reader still promotes the first project", func(t *testing.T) {
		m := startLevelModel(StartAllProjects)
		m.deps.LoadAllProjects = nil
		m, _ = update(t, m, scan)
		if m.allProjectsOpen || m.activeProjectPath() != "/roots/a" {
			t.Errorf("open=%t active=%q, want /roots/a promoted", m.allProjectsOpen, m.activeProjectPath())
		}
	})
}

// TestStartAgentOpensTheAgentWithTheKeyboardOnIt pins --open: the named
// session's Agent, keyboard on it (before W4.T1 the pane connected behind the
// list). Rows: a running host, a stopped host (restarted first, the keyboard
// moving on arrival) and a name that does not exist (the toast stays and the
// owner is left on the project's view).
func TestStartAgentOpensTheAgentWithTheKeyboardOnIt(t *testing.T) {
	build := func(open string) Model {
		deps := testDeps()
		deps.StartLevel = StartAgent
		deps.InitialSessionName = open
		deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("X"), nil }
		deps.SpawnHost = func(name, worktree string) (session.HostProcess, error) {
			return session.HostProcess{PID: 4242}, nil
		}
		m := New("/repo", time.Second, deps)
		m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		return m
	}

	t.Run("running host", func(t *testing.T) {
		m := build("fresh")
		m, cmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{runningSess("fresh")}, at: time.Unix(1, 0)})
		if cmd == nil {
			t.Fatal("expected a connect cmd")
		}
		if m.focus != focusRight || m.view != viewAgent {
			t.Fatalf("focus=%v view=%v at request time, want the right side in the agent view", m.focus, m.view)
		}
		m, _ = update(t, m, cmd())
		if m.pane == nil || m.focus != focusRight || m.level() != levelAgent {
			t.Errorf("pane=%t focus=%v level=%v, want the Agent with the keyboard on it", m.pane != nil, m.focus, m.level())
		}
	})
	t.Run("stopped host", func(t *testing.T) {
		m := build("fresh")
		m, cmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("fresh")}, at: time.Unix(1, 0)})
		if cmd == nil || !strings.Contains(m.status, "restarting fresh") {
			t.Fatalf("cmd=%v status=%q, want a restart", cmd != nil, m.status)
		}
		m, attach := update(t, m, cmd())
		if attach == nil {
			t.Fatal("a successful restart should chain into the connect")
		}
		m, _ = update(t, m, attach())
		if m.pane == nil || m.level() != levelAgent {
			t.Errorf("pane=%t level=%v, want the Agent after the restart", m.pane != nil, m.level())
		}
	})
	t.Run("missing name", func(t *testing.T) {
		m := build("no-such-session")
		m, cmd := update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("other")}, at: time.Unix(1, 0)})
		if cmd != nil || m.pane != nil {
			t.Fatalf("cmd=%v pane=%t, want nothing opened", cmd != nil, m.pane != nil)
		}
		if !strings.Contains(m.status, "no-such-session") || !strings.Contains(m.status, "no such session") {
			t.Errorf("status = %q, want the not-found toast", m.status)
		}
		if m.level() != levelProject {
			t.Errorf("level = %v, want Project", m.level())
		}
	})
}
