package ui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/dphaener/afk/internal/dashmeta"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/session"
)

// fakeFocusWatcher is an in-memory focusWatcher: tests push events onto its
// channel to drive the whole request pipeline without a real kqueue/inotify
// watch, and count Close calls to assert the descriptor is released on quit.
type fakeFocusWatcher struct {
	events chan fsnotify.Event
	closed int
}

func newFakeFocusWatcher() *fakeFocusWatcher {
	return &fakeFocusWatcher{events: make(chan fsnotify.Event, 4)}
}

func (f *fakeFocusWatcher) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeFocusWatcher) Close() error                  { f.closed++; return nil }

// focusRequestModel builds a dashboard wired to a real git-common-dir temp
// directory and a fake watcher, with the given sessions already loaded.
func focusRequestModel(t *testing.T, watcher focusWatcher, sessions ...Session) (Model, string) {
	t.Helper()
	return focusRequestModelForPID(t, t.TempDir(), dashPID, watcher, sessions...)
}

// dashPID and otherDashPID are two distinct dashboards for the concurrency rows.
// A dashboard's pid is what a focus request is addressed to, so two Models sharing
// one afk-dash/ directory need distinct ones to be distinguishable at all.
//
// They are REAL live pids (this process and its parent) rather than invented
// numbers, because the fallback poll sweeps requests addressed to dead pids: an
// invented pid would have its request collected before the test could read it,
// which is correct production behavior and a confusing test failure.
var (
	dashPID      = os.Getpid()
	otherDashPID = os.Getppid()
)

// focusRequestModelForPID builds a dashboard with an explicit pid and rendezvous
// directory, so a test can run TWO dashboards against one directory — the case a
// single shared request file got wrong.
func focusRequestModelForPID(t *testing.T, dir string, pid int, watcher focusWatcher, sessions ...Session) (Model, string) {
	t.Helper()
	deps := testDeps()
	deps.GitCommonDir = dir
	deps.Pid = func() int { return pid }
	deps.StartFocusWatcher = func(string) focusWatcher { return watcher }
	m := New("/repo", time.Second, deps)
	m.setSessions(sessions)
	m.ready = true
	m.width = 120
	m.height = 40
	return m, dir
}

// writePendingRequest publishes a request stamped relative to testDeps' fixed
// clock, so staleness is deterministic.
func writePendingRequest(t *testing.T, dir, name string, age time.Duration) {
	t.Helper()
	writePendingRequestFor(t, dir, name, dashPID, age)
}

// writePendingRequestFor publishes a request addressed to targetPID, stamped
// relative to testDeps' fixed clock.
func writePendingRequestFor(t *testing.T, dir, name string, targetPID int, age time.Duration) {
	t.Helper()
	now := time.Unix(1_000_000, 0)
	if err := dashmeta.WriteRequest(dir, name, targetPID, now.Add(-age)); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
}

// TestDecideFocusRequest is the whole request-decision matrix, pure and with no
// filesystem behind it.
//
// Load-bearing rows:
//
//   - "fresh, known" — the only HONOR row. It is the feature: a click lands the
//     user inside the named session.
//   - "stale past the cutoff" — DISCARD, not honor. The click missed its moment
//     and yanking focus for it is worse than nothing; discarding rather than
//     ignoring is what stops the fallback poll re-deciding it every 2s forever.
//   - "unknown session" — DISCARD. The session was cleaned, or this dashboard
//     watches another repo. Nothing to open, and never an error.
//   - "pending placeholder" — DISCARD. A placeholder row has no worktree and no
//     host yet, so it is exactly as unopenable as an unknown name.
//   - "not found" and "empty name" — IGNORE, which leaves the file alone: there
//     was nothing on disk to consume.
func TestDecideFocusRequest(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	maxAge := 60 * time.Second
	stamp := func(age time.Duration) string { return now.Add(-age).Format(time.RFC3339) }

	tests := []struct {
		name  string
		msg   focusRequestMsg
		known bool
		want  focusRequestDecision
	}{
		{
			name:  "fresh, known session",
			msg:   focusRequestMsg{found: true, request: dashmeta.Request{Name: "s", RequestedAt: stamp(0)}},
			known: true,
			want:  focusRequestHonor,
		},
		{
			name:  "fresh, known, at the cutoff boundary",
			msg:   focusRequestMsg{found: true, request: dashmeta.Request{Name: "s", RequestedAt: stamp(maxAge)}},
			known: true,
			want:  focusRequestHonor,
		},
		{
			name:  "stale past the cutoff",
			msg:   focusRequestMsg{found: true, request: dashmeta.Request{Name: "s", RequestedAt: stamp(maxAge + time.Second)}},
			known: true,
			want:  focusRequestDiscard,
		},
		{
			name:  "very stale (clicked minutes ago)",
			msg:   focusRequestMsg{found: true, request: dashmeta.Request{Name: "s", RequestedAt: stamp(10 * time.Minute)}},
			known: true,
			want:  focusRequestDiscard,
		},
		{
			name:  "unknown session",
			msg:   focusRequestMsg{found: true, request: dashmeta.Request{Name: "gone", RequestedAt: stamp(0)}},
			known: false,
			want:  focusRequestDiscard,
		},
		{
			name:  "unparseable timestamp",
			msg:   focusRequestMsg{found: true, request: dashmeta.Request{Name: "s", RequestedAt: "nope"}},
			known: true,
			want:  focusRequestDiscard,
		},
		{
			name:  "no request pending",
			msg:   focusRequestMsg{found: false},
			known: true,
			want:  focusRequestIgnore,
		},
		{
			name:  "found but empty name",
			msg:   focusRequestMsg{found: true, request: dashmeta.Request{RequestedAt: stamp(0)}},
			known: true,
			want:  focusRequestIgnore,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideFocusRequest(tt.msg, tt.known, now, maxAge)
			if got != tt.want {
				t.Errorf("decideFocusRequest(%+v, known=%v) = %v, want %v",
					tt.msg, tt.known, got, tt.want)
			}
		})
	}
}

// TestIsRequestEvent pins which filesystem events wake the pipeline. Load-bearing
// rows: the Entry-file row (afk-dash/ holds one <pid>.json per dashboard, and
// those writes must NOT be read as focus requests), and the Rename row, which
// MUST wake it — dashmeta.WriteRequest publishes by atomic rename, and some
// fsnotify backends report that as a Rename on the destination name, so filtering
// it would silently drop real clicks.
func TestIsRequestEvent(t *testing.T) {
	tests := []struct {
		name string
		ev   fsnotify.Event
		want bool
	}{
		{"create of this dashboard's request", fsnotify.Event{Name: requestEventName(dashPID), Op: fsnotify.Create}, true},
		{"write to this dashboard's request", fsnotify.Event{Name: requestEventName(dashPID), Op: fsnotify.Write}, true},
		{"rename onto this dashboard's request", fsnotify.Event{Name: requestEventName(dashPID), Op: fsnotify.Rename}, true},
		{"remove of this dashboard's request", fsnotify.Event{Name: requestEventName(dashPID), Op: fsnotify.Remove}, true},
		{"chmod only", fsnotify.Event{Name: requestEventName(dashPID), Op: fsnotify.Chmod}, false},
		{"a dashboard entry file", fsnotify.Event{Name: "/g/afk-dash/27881.json", Op: fsnotify.Create}, false},
		{"another dashboard's request", fsnotify.Event{Name: requestEventName(otherDashPID), Op: fsnotify.Create}, false},
		{"the write's temp file", fsnotify.Event{Name: "/g/afk-dash/." + dashmeta.RequestFileName(dashPID) + ".tmp.123", Op: fsnotify.Create}, false},
		{"an unrelated path", fsnotify.Event{Name: "/g/afk-dash/other", Op: fsnotify.Write}, false},
		{"empty name", fsnotify.Event{Op: fsnotify.Create}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRequestEvent(tt.ev, dashPID); got != tt.want {
				t.Errorf("isRequestEvent(%+v) = %v, want %v", tt.ev, got, tt.want)
			}
		})
	}
}

// TestFocusRequestOpensAndFocusesNamedSession is the acceptance test for the
// whole feature: a fresh request for a known, running session must open THAT
// session's pane and leave input routed to it, so the user's keystrokes reach
// Claude rather than the dashboard's list.
//
// It drives the real pipeline end to end — an fsnotify event on the fake
// watcher, through the subscription Cmd, into Update, out to the pane-open Cmd,
// and back through paneAttachedMsg — because the open and the focus happen in
// two different handlers and only the round trip proves they connect.
func TestFocusRequestOpensAndFocusesNamedSession(t *testing.T) {
	w := newFakeFocusWatcher()
	fp := newFakePane("SESSION-PANE")
	m, dir := focusRequestModel(t, w, sess("other"), runningSess("target"))
	var gotCfg hostPaneConfig
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		gotCfg = cfg
		return fp, nil
	}

	writePendingRequest(t, dir, "target", 0)
	w.events <- fsnotify.Event{Name: dashmeta.RequestPath(dir, dashPID), Op: fsnotify.Create}

	// The subscription Cmd turns the event into the message Update consumes.
	msg := focusRequestWatchCmd(w, dir, dashPID)()
	fr, ok := msg.(focusRequestMsg)
	if !ok {
		t.Fatalf("watch cmd produced %T, want focusRequestMsg", msg)
	}
	if !fr.found || fr.request.Name != "target" {
		t.Fatalf("watch cmd read %+v, want a found request naming target", fr)
	}
	if !fr.fromWatch {
		t.Fatal("a message from the watch subscription must be marked fromWatch, or the re-arm is skipped")
	}

	m, cmd := update(t, m, fr)
	if cmd == nil {
		t.Fatal("a honored request must dispatch the pane open")
	}

	attached := drainForPaneAttached(t, cmd)
	// The focus now rides the request instead of a Model field, so this is where
	// "the open knows it must focus" is checked: on the message the dial produced.
	// Both flags matter and neither implies the other — focusOnArrival gives the
	// pane the keyboard, focusRequest picks the arrival wording (`enter` sets the
	// first and not the second).
	if !attached.focusOnArrival || !attached.focusRequest {
		t.Fatalf("attached = {focusOnArrival:%v focusRequest:%v}, want both true",
			attached.focusOnArrival, attached.focusRequest)
	}
	if gotCfg.Name != "target" || gotCfg.Worktree != "/wt/target" {
		t.Fatalf("hostPaneConfig = %+v, want the NAMED session, not the selected row", gotCfg)
	}

	m, _ = update(t, m, attached)
	if m.pane == nil || m.paneName != "target" {
		t.Fatalf("pane not installed: pane=%v name=%q", m.pane != nil, m.paneName)
	}
	if !m.paneOwnsKeyboard() {
		t.Fatal("the pane must be FOCUSED: without it the click only opens a view and keys still go to the dashboard")
	}
	if !strings.Contains(m.status, "input goes to target") {
		t.Errorf("status = %q, want the focused-input notice naming target", m.status)
	}
	// What "consumed" means now that the focus rides the message: the request died
	// with the paneAttachedMsg that carried it, so the NEXT arrival — a preview,
	// usually, cursor movement being what it does — inherits nothing. That is
	// checked directly rather than by reading a field back, because there is no
	// longer a field to read.
	preview := paneAttachedMsg{pane: newFakePane("OTHER"), name: "other", preview: true}
	m, _ = update(t, m, preview)
	if m.paneName != "other" {
		t.Fatalf("setup: paneName = %q, want the preview installed", m.paneName)
	}
	if m.paneOwnsKeyboard() {
		t.Error("the preview inherited the click's focus: a consumed request must not focus the next pane to arrive")
	}
}

// TestFocusRequestArrivalWording pins when a click-to-focus arrival may say
// "input goes to X". Load-bearing rows: the two not-honoured ones. The click's
// connect moves focus right at request time, then dials; if the user goes back
// to the list or switches to the overview before the pane arrives, the arrival
// leaves focus and view as the user left them, so the keyboard is NOT in the
// session and the toast must not claim it is. The honoured row guards the
// other direction: the gate must not silence the true wording.
func TestFocusRequestArrivalWording(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(m *Model)
		wantInput bool
		wantFocus paneFocus
		wantView  rightView
	}{
		{
			name:      "focus honoured",
			setup:     func(m *Model) { m.focus = focusRight },
			wantInput: true,
			wantFocus: focusRight,
			wantView:  viewAgent,
		},
		{
			name:      "user returned to the list before the pane arrived",
			setup:     func(m *Model) { m.focus = focusList },
			wantInput: false,
			wantFocus: focusList,
			wantView:  viewAgent,
		},
		{
			name: "user switched to the overview before the pane arrived",
			setup: func(m *Model) {
				m.focus = focusRight
				m.view = viewOverview
			},
			wantInput: false,
			wantFocus: focusRight,
			wantView:  viewOverview,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := focusRequestModel(t, newFakeFocusWatcher(), runningSess("target"))
			tc.setup(&m)
			attached := paneAttachedMsg{
				pane:           newFakePane("SESSION-PANE"),
				name:           "target",
				focusOnArrival: true,
				focusRequest:   true,
			}
			m, _ = update(t, m, attached)
			if m.pane == nil || m.paneName != "target" {
				t.Fatalf("pane not installed: pane=%v name=%q", m.pane != nil, m.paneName)
			}
			if got := strings.Contains(m.status, "input goes to"); got != tc.wantInput {
				t.Errorf("status = %q, contains \"input goes to\" = %v, want %v", m.status, got, tc.wantInput)
			}
			if m.focus != tc.wantFocus {
				t.Errorf("focus = %v, want %v", m.focus, tc.wantFocus)
			}
			if m.view != tc.wantView {
				t.Errorf("view = %v, want %v", m.view, tc.wantView)
			}
		})
	}
}

// TestFocusRequestSwitchingPanesClosesTheOutgoingOne is the leak test, and it is
// the load-bearing one for the CLICK path specifically: enter and a click reach
// applyPaneAttached through the same swap, but only the click path had an early
// return above it. Enter then ctrl+\ establishes the outgoing pane drawn with
// the keyboard on the list; the click is what takes keyboard ownership on B's
// arrival.
//
// The honored-focus branch returned as soon as it had focused the arriving pane,
// jumping over the `previous.Close()` that detaches the pane being replaced. After
// that return nothing holds a handle to A, so its connection and its client slot
// on A's host leak for the life of the dashboard — and because a host serves
// exactly one client, the slot is not merely wasted, it is the one a later attach
// has to steal.
//
// Both assertions matter: A closed exactly once (not zero, not twice — the swap is
// a detach, and a double Close on a real hostChild would be a second detach on a
// pane already gone), and B still focused, since fixing the leak must not cost the
// focus that is the whole point of the click.
func TestFocusRequestSwitchingPanesClosesTheOutgoingOne(t *testing.T) {
	w := newFakeFocusWatcher()
	paneA := newFakePane("PANE-A")
	paneB := newFakePane("PANE-B")
	m, dir := focusRequestModel(t, w, runningSess("a"), runningSess("b"))

	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return paneA, nil }
	m, cmd := update(t, m, keyEnter)
	m, _ = update(t, m, drainForPaneAttached(t, cmd))
	if m.pane != paneA || m.paneName != "a" || !m.paneOwnsKeyboard() {
		t.Fatalf("precondition: enter must install pane A focused (pane==A: %v, name=%q, focused=%v)",
			m.pane == paneA, m.paneName, m.paneOwnsKeyboard())
	}
	m = pressKey(t, m, keyEscMsg())
	if m.paneOwnsKeyboard() || m.pane != paneA {
		t.Fatal("precondition: ctrl+] esc must leave pane A connected with keyboard ownership on the list")
	}

	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return paneB, nil }
	m, cmd = update(t, m, freshRequestMsg("b"))
	m, _ = update(t, m, drainForPaneAttached(t, cmd))

	if paneA.closed != 1 {
		t.Errorf("outgoing pane A was closed %d times, want exactly 1 — a click that switches "+
			"panes leaks A's connection and its client slot on A's host, with no handle left to close it",
			paneA.closed)
	}
	if m.pane != paneB || m.paneName != "b" {
		t.Fatalf("pane B not installed: pane==B %v, name=%q", m.pane == paneB, m.paneName)
	}
	if !m.paneOwnsKeyboard() {
		t.Error("pane B must still be FOCUSED: closing the outgoing pane must not cost the click its focus")
	}
	if paneB.closed != 0 {
		t.Errorf("arriving pane B was closed %d times, want 0", paneB.closed)
	}
	_ = dir
}

// TestFocusRequestEntersThroughTheSameDoorAsEnter pins W3.T1(e): a click lands
// focus=right, view=agent AT REQUEST TIME, exactly as enter does, rather than
// waiting for the arrival. Each row is load-bearing:
//
//   - "from the overview" proves the click selects the agent view: a click on a
//     notification is a request to be IN the session, whatever view the user
//     last left the right side on.
//   - "keys during the dial" proves the request-time focus is real: q, c and n
//     typed while the dial is out reach the agent placeholder and are
//     swallowed, where the old arrival-time focus left them on the list's
//     quit, clean and new-session bindings.
//   - "arrival" proves the dispatched dial still lands the pane focused.
func TestFocusRequestEntersThroughTheSameDoorAsEnter(t *testing.T) {
	w := newFakeFocusWatcher()
	m, _ := focusRequestModel(t, w, runningSess("a"), runningSess("b"))
	m.view = viewOverview
	opens := 0
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		opens++
		return newFakePane("B"), nil
	}

	m, cmd := update(t, m, freshRequestMsg("b"))
	if m.focus != focusRight || m.view != viewAgent {
		t.Fatalf("from the overview: focus/view = %v/%v at request time, want right/agent", m.focus, m.view)
	}
	if s, _ := m.selected(); s.Name != "b" {
		t.Fatalf("selection = %q, want the clicked session b", s.Name)
	}

	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'c', Text: "c"}, {Code: 'n', Text: "n"}} {
		var kc tea.Cmd
		m, kc = update(t, m, k)
		if kc != nil || m.quitting || m.formOpen || m.modal != inputNormal {
			t.Fatalf("keys during the dial: %q leaked to a list binding (cmd=%v quitting=%v form=%v modal=%v)",
				k.String(), kc != nil, m.quitting, m.formOpen, m.modal)
		}
	}

	m, _ = update(t, m, drainForPaneAttached(t, cmd))
	if opens != 1 {
		t.Fatalf("arrival: %d dials, want exactly 1", opens)
	}
	if m.paneName != "b" || !m.paneOwnsKeyboard() {
		t.Fatalf("arrival: paneName=%q focused=%v, want b focused", m.paneName, m.paneOwnsKeyboard())
	}
}

// TestFocusRequestIgnoresTheSelectedRow is the point of carrying the name in a
// file: the request must open the session it NAMES, never whatever the cursor
// happens to be on. Row order shifts as sessions change phase, which is exactly
// why keystroke-counting was rejected.
func TestFocusRequestIgnoresTheSelectedRow(t *testing.T) {
	w := newFakeFocusWatcher()
	m, dir := focusRequestModel(t, w, runningSess("aaa"), runningSess("bbb"), runningSess("ccc"))
	m.selectSessionIndex(0)
	var opened []string
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		opened = append(opened, cfg.Name)
		return newFakePane("X"), nil
	}

	_, cmd := update(t, m, freshRequestMsg("ccc"))
	if cmd == nil {
		t.Fatal("expected the pane open for the named session")
	}
	drainForPaneAttached(t, cmd)

	if len(opened) != 1 || opened[0] != "ccc" {
		t.Fatalf("opened %v, want exactly [ccc] — the NAMED session, not the row under the cursor", opened)
	}
	_ = dir
}

// TestFocusRequestConsumesTheFile covers deletion-after-handling for every
// decision that reached one. A request left on disk would be re-honored by the
// next fsnotify event and by every fallback poll, so consuming it is what makes a
// click act exactly once.
func TestFocusRequestConsumesTheFile(t *testing.T) {
	tests := []struct {
		name     string
		session  string
		age      time.Duration
		sessions []Session
	}{
		{name: "honored request", session: "target", age: 0, sessions: []Session{runningSess("target")}},
		{name: "stale request", session: "target", age: 10 * time.Minute, sessions: []Session{runningSess("target")}},
		{name: "unknown session", session: "gone", age: 0, sessions: []Session{runningSess("target")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newFakeFocusWatcher()
			m, dir := focusRequestModel(t, w, tt.sessions...)
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("X"), nil }
			writePendingRequest(t, dir, tt.session, tt.age)

			r, found := dashmeta.ReadRequest(dir, dashPID)
			if !found {
				t.Fatalf("precondition: expected a pending request")
			}
			_, cmd := update(t, m, focusRequestMsg{request: r, found: true, fromWatch: true})
			runAllCmds(t, cmd)

			if _, still := dashmeta.ReadRequest(dir, dashPID); still {
				t.Errorf("the request file survived handling; it would be honored again on the next event")
			}
		})
	}
}

// TestFocusRequestStaleAndUnknownDoNothing pins the silent-degradation contract:
// a request past the cutoff or naming a session discovery does not know must not
// open a pane, must not focus anything, and must not report an error. The user's
// keyboard stays where it was.
func TestFocusRequestStaleAndUnknownDoNothing(t *testing.T) {
	tests := []struct {
		name    string
		session string
		age     time.Duration
	}{
		{name: "stale past the cutoff", session: "target", age: 10 * time.Minute},
		{name: "unknown session", session: "no-such-session", age: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newFakeFocusWatcher()
			m, dir := focusRequestModel(t, w, runningSess("target"))
			opens := 0
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				opens++
				return newFakePane("X"), nil
			}
			writePendingRequest(t, dir, tt.session, tt.age)
			r, _ := dashmeta.ReadRequest(dir, dashPID)

			m, cmd := update(t, m, focusRequestMsg{request: r, found: true, fromWatch: true})
			runAllCmds(t, cmd)

			if opens != 0 {
				t.Errorf("OpenHostPane called %d times, want 0", opens)
			}
			if m.pane != nil || m.paneOwnsKeyboard() {
				t.Errorf("pane=%v focused=%v, want neither", m.pane != nil, m.paneOwnsKeyboard())
			}
			// The focus, once request-borne, has no Model residue to check: the
			// refusal returns no Cmd, so no paneAttachedMsg carrying focusRequest is
			// ever produced. "Nothing was left armed" is the opens==0 assertion above.
			if m.status != "" {
				t.Errorf("status = %q, want silence — a click that cannot be honored says nothing", m.status)
			}
		})
	}
}

// TestFocusRequestMalformedAndMissingAreNoOps covers the two on-disk failure
// modes end to end, through the real reader rather than a synthesized message: a
// malformed file and no file at all must both produce a no-op with no error.
func TestFocusRequestMalformedAndMissingAreNoOps(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		write    bool
	}{
		{name: "malformed json", contents: "{{{not json", write: true},
		{name: "valid json, no name", contents: `{"requested_at":"2026-08-05T12:00:00Z"}`, write: true},
		{name: "missing file", write: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newFakeFocusWatcher()
			m, dir := focusRequestModel(t, w, runningSess("target"))
			opens := 0
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
				opens++
				return newFakePane("X"), nil
			}
			if tt.write {
				if _, err := dashmeta.EnsureDir(dir); err != nil {
					t.Fatalf("EnsureDir: %v", err)
				}
				if err := os.WriteFile(dashmeta.RequestPath(dir, dashPID), []byte(tt.contents), 0o644); err != nil {
					t.Fatalf("writing request: %v", err)
				}
			}

			// Through the real poll Cmd, so the reader's own degradation is what
			// the model sees.
			msg := focusRequestPollCmd(dir, dashPID)()
			fr, ok := msg.(focusRequestMsg)
			if !ok {
				t.Fatalf("poll cmd produced %T, want focusRequestMsg", msg)
			}
			if fr.found {
				t.Fatalf("poll cmd reported found for %s", tt.name)
			}

			m, cmd := update(t, m, fr)
			runAllCmds(t, cmd)
			if opens != 0 || m.pane != nil || m.paneOwnsKeyboard() || m.status != "" {
				t.Errorf("expected a complete no-op: opens=%d pane=%v focused=%v status=%q",
					opens, m.pane != nil, m.paneOwnsKeyboard(), m.status)
			}
		})
	}
}

// TestFocusRequestReArmsTheSubscription is the load-bearing re-arm check, in the
// same spirit as the paneRedrawMsg re-arm: without it the dashboard honors exactly
// one click per run and then goes silent, with nothing to point at.
//
// Both rows matter. A message from the WATCH re-arms even when it decided to do
// nothing (an ignored event must not retire the fast path). A message from the
// fallback POLL must NOT re-arm, or it would multiply against the tick that
// already reschedules itself.
func TestFocusRequestReArmsTheSubscription(t *testing.T) {
	tests := []struct {
		name      string
		msg       focusRequestMsg
		wantReArm bool
	}{
		{
			name:      "watch event, nothing pending",
			msg:       focusRequestMsg{fromWatch: true},
			wantReArm: true,
		},
		{
			name: "watch event, stale request",
			msg: focusRequestMsg{fromWatch: true, found: true, request: dashmeta.Request{
				Name: "target", RequestedAt: time.Unix(1_000_000, 0).Add(-time.Hour).Format(time.RFC3339),
			}},
			wantReArm: true,
		},
		{
			name:      "fallback poll, nothing pending",
			msg:       focusRequestMsg{},
			wantReArm: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newFakeFocusWatcher()
			m, _ := focusRequestModel(t, w, runningSess("target"))

			_, cmd := update(t, m, tt.msg)
			if !tt.wantReArm {
				if cmd != nil && producesWatchMsg(t, cmd) {
					t.Error("a fallback-poll message must not re-arm the watch subscription")
				}
				return
			}
			if cmd == nil {
				t.Fatal("expected the watch subscription to be re-armed")
			}
			// The re-armed Cmd must be live: pushing another event through it has
			// to produce another message.
			w.events <- fsnotify.Event{Name: requestEventName(dashPID), Op: fsnotify.Create}
			if !producesWatchMsg(t, cmd) {
				t.Error("the re-armed subscription did not deliver the next event")
			}
		})
	}
}

// TestFocusRequestOnTheOpenPaneJustFocuses covers the request naming the session
// already on screen: there is nothing to open, so it collapses to the focus alone
// rather than opening a second connection that would be immediately discarded.
func TestFocusRequestOnTheOpenPaneJustFocuses(t *testing.T) {
	w := newFakeFocusWatcher()
	fp := newFakePane("ALREADY-OPEN")
	m, _ := focusRequestModel(t, w, runningSess("target"))
	opens := 0
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		opens++
		return newFakePane("X"), nil
	}
	m.pane = fp
	m.paneName = "target"

	m, cmd := update(t, m, freshRequestMsg("target"))
	runAllCmds(t, cmd)

	if opens != 0 {
		t.Errorf("OpenHostPane called %d times, want 0 for the session already in the pane", opens)
	}
	if !m.paneOwnsKeyboard() {
		t.Error("the already-open pane must be focused")
	}
	if m.pane != fp {
		t.Error("the installed pane must be left alone")
	}
	if fp.closed != 0 {
		t.Errorf("the open pane was closed %d times; a focus request must not detach it", fp.closed)
	}
}

// TestFocusRequestOnAStoppedHostFocusesItsOverview pins the click on a
// session whose host is down to enter's rule (plan W3.T1 d/e): the row has no
// live agent, so its details overlay opens over the Project (T16.5), and
// nothing is spawned. A notification can be clicked long after its session's
// host exited, so this is an ordinary case — and `r` is what brings the agent
// back: from the Project once the details are closed, until W2.T1 of T16.5
// gives the overlay its own r.
func TestFocusRequestOnAStoppedHostFocusesItsOverview(t *testing.T) {
	w := newFakeFocusWatcher()
	m, _ := focusRequestModel(t, w, sess("target"))
	spawns := 0
	m.deps.SpawnHost = func(name, worktree string) (session.HostProcess, error) {
		spawns++
		return session.HostProcess{PID: 4242}, nil
	}
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("X"), nil }

	m, cmd := update(t, m, freshRequestMsg("target"))
	if hasHostRestarted(t, cmd) {
		t.Fatal("a click on a stopped session must not restart its host")
	}
	if spawns != 0 {
		t.Fatalf("SpawnHost called %d times, want 0", spawns)
	}
	if m.focus != focusList || m.view != viewAgent || !m.detailsOpen || m.detailsFor != "target" {
		t.Fatalf("focus/view = %v/%v details=%v for %q, want the target's details over the list",
			m.focus, m.view, m.detailsOpen, m.detailsFor)
	}
	if m.agentPlaceholderDrawn() {
		t.Fatal("a stopped row must not draw the agent placeholder")
	}

	m, _ = update(t, m, keyEsc)
	m, restart := pressAction(t, m, keyRune('r'))
	if restart == nil {
		t.Fatal("r on the clicked stopped session must dispatch the restart")
	}
	m, openCmd := update(t, m, drainForHostRestarted(t, restart))
	if openCmd == nil {
		t.Fatal("the restart must chain into the pane open")
	}
	m, _ = update(t, m, drainForPaneAttached(t, openCmd))
	if m.pane == nil || m.paneName != "target" || spawns != 1 {
		t.Fatalf("after r the pane must be open: name=%q spawns=%d", m.paneName, spawns)
	}
}

// TestFocusRequestOnAStoppedHostDetachesAnotherRowsPane pins the pane-follows-
// selection rule (plan W3.T1(a), objective 5) on the one selection change that
// skips schedulePreview: a click on a STOPPED session. The click moves the
// cursor onto the stopped row and opens nothing, so a pane installed for
// another row would stay connected but undrawn — holding that host's single
// client slot — and `c` on the clicked row would then be refused for a pane
// nobody can see.
//
// Both rows are load-bearing: "list focus" is pane A released with ctrl+\
// before the click; "pane focused" proves the detach does not depend on the
// list holding the keyboard when the click lands, which followSelection's own
// focus gate would otherwise let through.
func TestFocusRequestOnAStoppedHostDetachesAnotherRowsPane(t *testing.T) {
	for _, tc := range []struct {
		name      string
		releaseFn bool
	}{
		{name: "list focus", releaseFn: true},
		{name: "pane focused", releaseFn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newFakeFocusWatcher()
			paneA := newFakePane("PANE-A")
			m, _ := focusRequestModel(t, w, runningSess("a"), sess("target"))
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return paneA, nil }
			m, cmd := update(t, m, keyEnter)
			m, _ = update(t, m, drainForPaneAttached(t, cmd))
			if m.pane != paneA || m.paneName != "a" {
				t.Fatalf("precondition: enter must install pane A (name=%q)", m.paneName)
			}
			if tc.releaseFn {
				m = pressKey(t, m, keyEscMsg())
			}

			m, _ = update(t, m, freshRequestMsg("target"))
			if s, _ := m.selected(); s.Name != "target" {
				t.Fatalf("selection = %q, want the clicked session target", s.Name)
			}
			if m.pane != nil || m.paneName != "" {
				t.Fatalf("pane for %q still installed after a click selected target", m.paneName)
			}
			if paneA.closed != 1 {
				t.Fatalf("pane A closed %d times, want exactly 1", paneA.closed)
			}
			if !m.detailsOpen || m.detailsFor != "target" {
				t.Fatalf("details=%v for %q, want the stopped row's details open",
					m.detailsOpen, m.detailsFor)
			}

			m.setSessions([]Session{runningSess("a"), sess("target")})
			if m.pane != nil || paneA.closed != 1 {
				t.Fatalf("after a tick: pane=%v closed=%d", m.pane != nil, paneA.closed)
			}

			m, _ = update(t, m, keyEsc)
			m = pressKey(t, m, keyRune('c'))
			if strings.Contains(m.status, "close the pane first") {
				t.Fatalf("c on target refused for an invisible pane: %q", m.status)
			}
			if m.confirmTarget != "target" {
				t.Fatalf("confirmTarget = %q, want the clean confirm for target", m.confirmTarget)
			}
		})
	}
}

// hasHostRestarted reports whether running cmd (and any batch it expands to)
// produces a hostRestartedMsg. Unlike drainForHostRestarted it does not fail
// when there is none — the absence is what the caller asserts.
func hasHostRestarted(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	for _, msg := range collectMsgs(t, cmd) {
		if _, ok := msg.(hostRestartedMsg); ok {
			return true
		}
	}
	return false
}

// TestFocusRequestOnAStoppedHostInvalidatesAPendingPreview covers the one
// selection change with no schedulePreview of its own: a click. Every keyboard
// route to a row passes through a cursor move first, and schedulePreview bumps
// the generation on every move. A click arrives from a file watcher with no
// cursor move at all, so a timer armed for the row the user last sat on is
// still live, and without a bump it fires after the click and installs a pane
// for a session nobody clicked.
//
// Both halves are load-bearing:
//   - the timer being inert is the fix, asserted through applyPreviewTimer rather
//     than by reading previewGen back. That is deliberate: a bump written to a
//     discarded copy (a value-receiver trap) would still satisfy a field read
//     taken from the returned Model, but cannot satisfy this.
//   - the clicked session still arriving open once `r` restarts it is what
//     stops the fix from being "cancel everything".
func TestFocusRequestOnAStoppedHostInvalidatesAPendingPreview(t *testing.T) {
	w := newFakeFocusWatcher()
	m, _ := focusRequestModel(t, w, sess("target"), runningSess("other"))
	var opened []hostPaneConfig
	m.deps.OpenHostPane = recordingOpen(&opened, newFakePane("TARGET"))
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		return session.HostProcess{PID: 4242}, nil
	}

	// The move lands on a RUNNING row, so it genuinely arms a preview — the
	// live timer the click has to invalidate. The list is kept in name order
	// inside a group (T16.4), so other is above target and the move is up.
	m.selectSessionIndex(namedIndex(t, m, "target"))
	m, armed := update(t, m, keyUp)
	if armed == nil {
		t.Fatalf("setup: the move onto a running row must arm a preview")
	}
	staleGen := m.previewGen
	staleName := m.sessions[m.listSelection()].Name

	m, cmd := update(t, m, freshRequestMsg("target"))
	runAllCmds(t, cmd)

	// The pre-click timer fires after the click.
	m, fired := update(t, m, previewTimerMsg{gen: staleGen, name: staleName})
	if fired != nil {
		t.Errorf("the pre-click preview timer dispatched a dial for %q; the click must invalidate it", staleName)
	}
	if len(opened) != 0 {
		t.Errorf("opens = %d (%+v), want 0 — the stale timer dialed after the click", len(opened), opened)
	}

	// The click opened the target's details; esc closes them, and r from
	// the Project restarts it.
	m, _ = update(t, m, keyEsc)
	m, restart := pressAction(t, m, keyRune('r'))
	if restart == nil {
		t.Fatal("r on the clicked stopped session must dispatch the restart")
	}
	m, openCmd := update(t, m, drainForHostRestarted(t, restart))
	if openCmd == nil {
		t.Fatal("the restart must chain into the pane open")
	}
	m, _ = update(t, m, drainForPaneAttached(t, openCmd))
	if m.paneName != "target" || m.pane == nil {
		t.Fatalf("paneName = %q pane=%v, want the clicked session open", m.paneName, m.pane != nil)
	}
}

func TestWaitForRestartedHostMetaRetriesTransientRecords(t *testing.T) {
	now := time.Unix(0, 0)
	var sleeps []time.Duration
	reads := 0
	read := func(string, string) (host.Meta, error) {
		reads++
		switch reads {
		case 1:
			return host.Meta{}, os.ErrNotExist
		case 2:
			return host.Meta{}, errors.New("partial host record")
		default:
			return host.Meta{}, nil
		}
	}
	err := waitForRestartedHostMeta(read, "/worktree", "alpha", 500*time.Millisecond,
		func() time.Time { return now },
		func(d time.Duration) { sleeps = append(sleeps, d); now = now.Add(d) },
	)
	if err != nil {
		t.Fatalf("retry returned %v, want eventual success", err)
	}
	if reads != 3 || len(sleeps) != 2 || sleeps[0] != 10*time.Millisecond || sleeps[1] != 20*time.Millisecond {
		t.Fatalf("reads=%d sleeps=%v, want 3 reads with 10ms/20ms backoff", reads, sleeps)
	}
}

func TestWaitForRestartedHostMetaReturnsFinalErrorAtBound(t *testing.T) {
	now := time.Unix(0, 0)
	finalErr := errors.New("host record still incomplete")
	reads := 0
	var sleeps []time.Duration
	err := waitForRestartedHostMeta(
		func(string, string) (host.Meta, error) { reads++; return host.Meta{}, finalErr },
		"/worktree", "alpha", 25*time.Millisecond,
		func() time.Time { return now },
		func(d time.Duration) { sleeps = append(sleeps, d); now = now.Add(d) },
	)
	if !errors.Is(err, finalErr) {
		t.Fatalf("error = %v, want final metadata error %v", err, finalErr)
	}
	if reads != 3 || len(sleeps) != 2 || sleeps[0] != 10*time.Millisecond || sleeps[1] != 15*time.Millisecond {
		t.Fatalf("reads=%d sleeps=%v, want bounded 25ms retry", reads, sleeps)
	}
}

// TestStoppedRestartWaitsForMetadataBeforeOpening pins the restart chain's
// order for `r` on a stopped row: spawn, then host-record readiness, then the
// pane open — and the no-focus request riding through all three, since the
// keyboard is on the Project (its details, opened by enter, closed first).
func TestStoppedRestartWaitsForMetadataBeforeOpening(t *testing.T) {
	m := testModel(stoppedSess("alpha"))
	var order []string
	m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		order = append(order, "spawn")
		return session.HostProcess{PID: 4242}, nil
	}
	m.deps.WaitHostReady = func(worktree, name string) error {
		order = append(order, "metadata")
		if worktree != m.sessions[0].WorktreePath || name != "alpha" {
			t.Errorf("readiness target = %q/%q, want selected stopped session", worktree, name)
		}
		return nil
	}
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		order = append(order, "open")
		return newFakePane("X"), nil
	}

	m, _ = update(t, m, keyEnter)
	if !m.detailsOpen {
		t.Fatalf("setup: enter on a stopped row must open its details (focus=%v)", m.focus)
	}
	m, _ = update(t, m, keyEsc)
	m, restart := pressAction(t, m, keyRune('r'))
	if restart == nil {
		t.Fatal("r did not dispatch the stopped-host restart")
	}
	restarted, ok := restart().(hostRestartedMsg)
	if !ok || restarted.err != nil || restarted.focusOnArrival {
		t.Fatalf("restart result = %#v, want an unfocused success", restarted)
	}
	if len(order) != 2 || order[0] != "spawn" || order[1] != "metadata" {
		t.Fatalf("pre-open order = %v, want spawn then metadata", order)
	}
	m, open := update(t, m, restarted)
	if open == nil {
		t.Fatal("ready restart did not chain into pane open")
	}
	attached := open().(paneAttachedMsg)
	if attached.focusOnArrival {
		t.Fatal("r's chained open gained a focus request the keypress did not make")
	}
	if len(order) != 3 || order[2] != "open" {
		t.Fatalf("order = %v, want open only after metadata readiness", order)
	}
}

// TestFocusRequestSkipsASessionBeingCleaned reuses the clean gate every other
// pane-open entrance honors: a clean SIGKILLs the session's host and deletes its
// worktree, so a pane must never be opened over one — a click cannot be allowed to
// bypass that.
func TestFocusRequestSkipsASessionBeingCleaned(t *testing.T) {
	w := newFakeFocusWatcher()
	m, _ := focusRequestModel(t, w, runningSess("target"))
	opens := 0
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		opens++
		return newFakePane("X"), nil
	}
	m.markCleaning(cleanIDByName("target"))

	m, cmd := update(t, m, freshRequestMsg("target"))
	runAllCmds(t, cmd)

	if opens != 0 {
		t.Errorf("OpenHostPane called %d times for a session being cleaned, want 0", opens)
	}
	if m.pane != nil || m.paneOwnsKeyboard() {
		t.Error("no pane may be opened or focused over a session being cleaned")
	}
}

// TestFocusRequestPendingPlaceholderIsIgnored covers a request naming a session
// whose background create has not finished: the placeholder row has no worktree
// and no host, so it is as unopenable as an unknown name.
func TestFocusRequestPendingPlaceholderIsIgnored(t *testing.T) {
	w := newFakeFocusWatcher()
	m, _ := focusRequestModel(t, w)
	m.addPendingSpawn("target")
	m.mergePendingSpawnsNow()
	opens := 0
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		opens++
		return newFakePane("X"), nil
	}

	m, cmd := update(t, m, freshRequestMsg("target"))
	runAllCmds(t, cmd)

	if opens != 0 || m.pane != nil || m.paneOwnsKeyboard() {
		t.Errorf("a placeholder row must not be opened: opens=%d pane=%v focused=%v",
			opens, m.pane != nil, m.paneOwnsKeyboard())
	}
}

// TestTickDispatchesTheFallbackPoll is the degradation guarantee: with no
// fsnotify watch at all, the existing 2s discovery tick must still carry requests
// through. A silent no-watch has to degrade to SLOW, never to dead.
func TestTickDispatchesTheFallbackPoll(t *testing.T) {
	m, dir := focusRequestModel(t, nil, runningSess("target"))
	if m.focusWatch != nil {
		t.Fatal("precondition: this model must have no watcher")
	}
	writePendingRequest(t, dir, "target", 0)

	_, cmd := update(t, m, tickMsg(time.Unix(1, 0)))
	if cmd == nil {
		t.Fatal("the tick must dispatch its batch")
	}

	var got focusRequestMsg
	for _, msg := range collectMsgs(t, cmd) {
		if fr, ok := msg.(focusRequestMsg); ok {
			got = fr
		}
	}
	if !got.found || got.request.Name != "target" {
		t.Fatalf("the tick's fallback poll produced %+v, want the pending request for target", got)
	}
	if got.fromWatch {
		t.Error("a poll-produced message must not claim to be from the watch, or it would re-arm a subscription that does not exist")
	}
}

// TestInitArmsTheWatchSubscriptionOnlyWhenAvailable pins both startup shapes: a
// watcher present means the fast path is armed, and a nil watcher must still start
// the dashboard normally rather than panicking or dropping the other Cmds.
func TestInitArmsTheWatchSubscriptionOnlyWhenAvailable(t *testing.T) {
	t.Run("watcher available", func(t *testing.T) {
		w := newFakeFocusWatcher()
		m, dir := focusRequestModel(t, w, runningSess("target"))
		cmd := m.Init()
		if cmd == nil {
			t.Fatal("Init must return its batch")
		}
		writePendingRequest(t, dir, "target", 0)
		w.events <- fsnotify.Event{Name: dashmeta.RequestPath(dir, dashPID), Op: fsnotify.Create}

		found := false
		for _, msg := range collectMsgs(t, cmd) {
			if fr, ok := msg.(focusRequestMsg); ok && fr.fromWatch {
				found = true
			}
		}
		if !found {
			t.Error("Init did not arm the watch subscription")
		}
	})

	t.Run("no watcher available", func(t *testing.T) {
		m, _ := focusRequestModel(t, nil, runningSess("target"))
		if cmd := m.Init(); cmd == nil {
			t.Fatal("Init must still return the rest of its batch with no watcher")
		}
	})
}

// TestStartFocusWatcherDegradesSilently pins that watch creation never reports an
// error the caller has to handle: an empty or relative git common dir yields nil,
// which the whole pipeline already treats as "fall back to the tick".
func TestStartFocusWatcherDegradesSilently(t *testing.T) {
	tests := []struct {
		name         string
		gitCommonDir string
	}{
		{name: "empty dir", gitCommonDir: ""},
		{name: "relative dir", gitCommonDir: "relative/path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := startFocusWatcher(tt.gitCommonDir); w != nil {
				_ = w.Close()
				t.Errorf("startFocusWatcher(%q) returned a watcher, want nil", tt.gitCommonDir)
			}
		})
	}
}

// TestStartFocusWatcherOnARealDirectory exercises the real fsnotify path once, so
// the seam is not only ever tested through its fake: it must create the
// afk-dash/ directory (a dashboard outside tmux never wrote an Entry, so it can
// legitimately be absent) and deliver an event for a real atomic request write.
func TestStartFocusWatcherOnARealDirectory(t *testing.T) {
	dir := t.TempDir()
	w := startFocusWatcher(dir)
	if w == nil {
		t.Skip("no fsnotify watch available in this environment; the fallback poll covers it")
	}
	defer func() { _ = w.Close() }()

	if _, err := os.Stat(filepath.Join(dir, "afk-dash")); err != nil {
		t.Fatalf("startFocusWatcher must create afk-dash/: %v", err)
	}

	if err := dashmeta.WriteRequest(dir, "target", dashPID, time.Now()); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- focusRequestWatchCmd(w, dir, dashPID)() }()

	select {
	case msg := <-done:
		fr, ok := msg.(focusRequestMsg)
		if !ok {
			t.Fatalf("watch cmd produced %T, want focusRequestMsg", msg)
		}
		if !fr.found || fr.request.Name != "target" {
			t.Errorf("watch cmd read %+v, want the request naming target", fr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event delivered for a real atomic request write")
	}
}

// TestQuitClosesTheFocusWatcher pins the descriptor/goroutine release. The
// subscription Cmd blocks on the watcher's channel, and Close is what ends it —
// leaving it open leaks a kqueue descriptor per dashboard run.
func TestQuitClosesTheFocusWatcher(t *testing.T) {
	w := newFakeFocusWatcher()
	m, _ := focusRequestModel(t, w, runningSess("target"))

	m = pressKey(t, m, keyRune('q'))
	if w.closed != 1 {
		t.Errorf("watcher Close called %d times on quit, want 1", w.closed)
	}
	if m.focusWatch != nil {
		t.Error("the closed watcher must be cleared so a second quit cannot double-close it")
	}
}

// TestClosedWatcherRetiresTheSubscription covers the watcher going away under the
// subscription: the Cmd must return a harmless no-op message rather than blocking
// forever or spinning, and that message must not re-arm.
func TestClosedWatcherRetiresTheSubscription(t *testing.T) {
	w := newFakeFocusWatcher()
	close(w.events)

	msg := focusRequestWatchCmd(w, t.TempDir(), dashPID)()
	fr, ok := msg.(focusRequestMsg)
	if !ok {
		t.Fatalf("watch cmd produced %T, want focusRequestMsg", msg)
	}
	if fr.found || fr.fromWatch {
		t.Errorf("a closed watcher produced %+v, want a not-found message that does not re-arm", fr)
	}
}

// TestNoGitCommonDirDisablesTheMechanism is the control: with no rendezvous
// directory the request Cmds must not be produced at all, so a dashboard that
// cannot locate one spends nothing on a mechanism that cannot fire.
func TestNoGitCommonDirDisablesTheMechanism(t *testing.T) {
	if cmd := focusRequestPollCmd("", dashPID); cmd != nil {
		t.Error("focusRequestPollCmd(\"\") must be nil")
	}
	if cmd := focusRequestWatchCmd(nil, "", dashPID); cmd != nil {
		t.Error("focusRequestWatchCmd(nil, ...) must be nil")
	}
	if cmd := removeFocusRequestCmd("", dashPID); cmd != nil {
		t.Error("removeFocusRequestCmd(\"\") must be nil")
	}
}

// --- helpers ---

// requestEventName is the path fsnotify would report for the request addressed to
// pid, built from dashmeta's own generator so a rename there fails these rows
// rather than silently making them match nothing.
func requestEventName(pid int) string {
	return "/g/afk-dash/" + dashmeta.RequestFileName(pid)
}

// freshRequestMsg is a watch-sourced request for name, stamped at testDeps' fixed
// clock so it is unambiguously fresh.
func freshRequestMsg(name string) focusRequestMsg {
	return focusRequestMsg{
		found:     true,
		fromWatch: true,
		request: dashmeta.Request{
			Name:        name,
			RequestedAt: time.Unix(1_000_000, 0).Format(time.RFC3339),
		},
	}
}

// collectMsgs runs cmd, flattens one level of tea.BatchMsg, and returns the
// messages its members produced, bounding each member by cmdDeadline.
//
// It is collectMsgsWithDeadline with this package's short, shared deadline —
// every OTHER test in this file wants that one, so the parameterized version
// stays unexported to this pair of callers (it and the switcher wiring suite's
// previewCollectDeadline) rather than every call site here having to spell out
// cmdDeadline itself.
func collectMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	return collectMsgsWithDeadline(t, cmd, cmdDeadline)
}

// cmdDeadline bounds how long collectMsgs waits on one batch member. Every Cmd
// these tests care about is an in-memory closure over a fake seam (a fake pane
// open, a temp-dir file read), so it completes in microseconds; the deadline
// exists only to get past the re-armed subscription, which is SUPPOSED to block
// forever. It is therefore kept short — a per-test 2s wait multiplied across the
// file turned a fast suite into a 24-second one.
const cmdDeadline = 300 * time.Millisecond

// collectMsgsWithDeadline runs cmd, flattens one level of tea.BatchMsg, and
// returns the messages its members produced within deadline — the shape
// collectMsgs needs at cmdDeadline, and switcher_wiring_test.go needs at its
// own, wider previewCollectDeadline (that file arms a REAL previewTimerCmd,
// which is also exactly cmdDeadline long, so racing it against this package's
// short deadline would be a coin flip). Parameterizing here is what lets that
// file reuse this instead of keeping its own copy of it.
//
// Members run CONCURRENTLY, each against its own deadline, because Bubble Tea
// runs them that way and one of them genuinely never returns: the re-armed
// watch subscription blocks on its channel until the next filesystem event,
// which is the whole point of it. Running the batch sequentially would hang
// the test on that member and never reach the pane-open beside it.
func collectMsgsWithDeadline(t *testing.T, cmd tea.Cmd, deadline time.Duration) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}

	first := runWithDeadline(cmd, deadline)
	batch, ok := first.(tea.BatchMsg)
	if !ok {
		if first == nil {
			return nil
		}
		return []tea.Msg{first}
	}

	results := make(chan tea.Msg, len(batch))
	for _, c := range batch {
		if c == nil {
			results <- nil
			continue
		}
		go func(c tea.Cmd) { results <- runWithDeadline(c, deadline) }(c)
	}

	var out []tea.Msg
	for range batch {
		if msg := <-results; msg != nil {
			out = append(out, msg)
		}
	}
	return out
}

// runWithDeadline runs cmd on its own goroutine and returns nil if it has not
// produced a message within deadline. A blocked Cmd is expected (see
// collectMsgsWithDeadline), so a timeout here is a normal outcome rather than
// a failure.
//
// The goroutine is deliberately abandoned on timeout rather than waited for: it is
// parked on a channel receive that a closed watcher will release, and blocking the
// test on it would defeat the deadline.
func runWithDeadline(cmd tea.Cmd, deadline time.Duration) tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(deadline):
		return nil
	}
}

// runAllCmds executes cmd and its batch members for their side effects.
func runAllCmds(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	collectMsgs(t, cmd)
}

// drainForPaneAttached runs cmd and returns the single paneAttachedMsg it
// produced, failing if there is none.
func drainForPaneAttached(t *testing.T, cmd tea.Cmd) paneAttachedMsg {
	t.Helper()
	for _, msg := range collectMsgs(t, cmd) {
		if attached, ok := msg.(paneAttachedMsg); ok {
			return attached
		}
	}
	t.Fatal("no paneAttachedMsg among the produced messages")
	return paneAttachedMsg{}
}

// drainForHostRestarted runs cmd and returns the single hostRestartedMsg it
// produced, failing if there is none.
func drainForHostRestarted(t *testing.T, cmd tea.Cmd) hostRestartedMsg {
	t.Helper()
	for _, msg := range collectMsgs(t, cmd) {
		if hr, ok := msg.(hostRestartedMsg); ok {
			return hr
		}
	}
	t.Fatal("no hostRestartedMsg among the produced messages")
	return hostRestartedMsg{}
}

// producesWatchMsg reports whether cmd yields a watch-sourced focusRequestMsg,
// without blocking the test if it does not: the subscription Cmd blocks on its
// channel by design, so it runs on its own goroutine here.
func producesWatchMsg(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	out := make(chan bool, 1)
	go func() {
		for _, msg := range collectMsgs(t, cmd) {
			if fr, ok := msg.(focusRequestMsg); ok && fr.fromWatch {
				out <- true
				return
			}
		}
		out <- false
	}()
	select {
	case got := <-out:
		return got
	case <-time.After(2 * time.Second):
		return false
	}
}

// TestOnlyOneOfTwoDashboardsHonorsAClick is the two-dashboard test the review
// asked for, at the level where the damage actually happened.
//
// Two dashboards run concurrently against ONE afk-dash/ directory — the real
// configuration on this machine, where W1.T2 recorded pids 2121 and 83745 live —
// and a click publishes one request. Exactly one dashboard may open the session.
//
// Why the shared file was worse than wasteful: both dashboards read it, both
// dispatched openPaneFor for the same session, and internal/host serves exactly ONE
// client — so the second connection STOLE the session, leaving the first dashboard
// showing a pane whose host had moved on, with the user's keyboard pointed at it.
//
// Both halves are asserted: the addressed dashboard opens and focuses the session
// (the feature must still work), and the other opens nothing at all and leaves its
// keyboard where it was.
func TestOnlyOneOfTwoDashboardsHonorsAClick(t *testing.T) {
	dir := t.TempDir()
	watcherA, watcherB := newFakeFocusWatcher(), newFakeFocusWatcher()
	dashA, _ := focusRequestModelForPID(t, dir, dashPID, watcherA, runningSess("target"))
	dashB, _ := focusRequestModelForPID(t, dir, otherDashPID, watcherB, runningSess("target"))

	var openedByA, openedByB []string
	dashA.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		openedByA = append(openedByA, cfg.Name)
		return newFakePane("PANE-A"), nil
	}
	dashB.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		openedByB = append(openedByB, cfg.Name)
		return newFakePane("PANE-B"), nil
	}

	// One click, addressed to dashboard A — the pid `afk notify` took from the
	// same dash Entry it read the pane and client tty from.
	writePendingRequestFor(t, dir, "target", dashPID, 0)

	// Both dashboards watch the same DIRECTORY, so both really do see the event.
	// The name filter is what separates them.
	ev := fsnotify.Event{Name: dashmeta.RequestPath(dir, dashPID), Op: fsnotify.Create}
	watcherA.events <- ev
	watcherB.events <- ev

	// BOTH dashboards read before EITHER acts, which is both the real interleaving
	// (they wake on the same event within microseconds of each other) and the only
	// ordering that can observe the bug: whichever dashboard handles the request
	// first DELETES it, so reading B after A had already consumed a shared file
	// would show B finding nothing for the wrong reason.
	msgA := readWatchMsg(t, focusRequestWatchCmd(watcherA, dir, dashPID))
	msgB := readWatchMsg(t, focusRequestWatchCmd(watcherB, dir, otherDashPID))
	// B's fallback poll is checked here too, before any consumption, or the slow
	// path could reintroduce the race the fast path no longer has.
	pollB := collectMsgs(t, focusRequestPollCmd(dir, otherDashPID))

	if msgA == nil {
		t.Fatal("the addressed dashboard's subscription produced nothing")
	}
	if msgB != nil && msgB.found {
		t.Errorf("the second dashboard saw request %+v addressed to another dashboard", msgB.request)
	}
	for _, msg := range pollB {
		if fr, ok := msg.(focusRequestMsg); ok && fr.found {
			t.Errorf("the second dashboard's fallback poll found request %+v", fr.request)
		}
	}

	dashA, cmdA := update(t, dashA, *msgA)
	dashA, _ = update(t, dashA, drainForPaneAttached(t, cmdA))

	// B handles whatever it read, if anything. With per-dashboard requests it read
	// nothing and this is a no-op; with a shared file it opens the same session and
	// steals it, which openedByB below is what catches.
	if msgB != nil {
		var cmdB tea.Cmd
		dashB, cmdB = update(t, dashB, *msgB)
		runAllCmds(t, cmdB)
	}

	if len(openedByA) != 1 || openedByA[0] != "target" {
		t.Errorf("addressed dashboard opened %v, want exactly [target]", openedByA)
	}
	if !dashA.paneOwnsKeyboard() {
		t.Error("the addressed dashboard must focus the pane — that is the whole click")
	}
	if len(openedByB) != 0 {
		t.Errorf("the OTHER dashboard opened %v; its connection would steal the session "+
			"out of the addressed dashboard's pane (internal/host serves one client)", openedByB)
	}
	if dashB.pane != nil || dashB.paneOwnsKeyboard() {
		t.Errorf("the other dashboard changed state: pane=%v focused=%v", dashB.pane != nil, dashB.paneOwnsKeyboard())
	}
}

// TestPendingFocusIsReleasedOnEveryPaneAttachedPath covers the leak on
// applyPaneAttached's non-honoring exits.
//
// A honored request rides focusOnArrival on the paneAttachedMsg and waits for the
// pane. When that open FAILS, or is dropped because a clean landed mid-flight, a
// focus held on the MODEL used to stay set — so the next pane to arrive inherited
// a focus meant for a session that never opened. That next pane is whatever the
// user opened themselves with `enter`, which is why this matters: a dead click
// silently steals a later deliberate action's keyboard.
//
// Carrying the focus on the message is what closes this structurally — the flag
// dies with the message that failed — so what each row asserts is the OUTCOME
// rather than a field being cleared: after a request whose open never installed a
// pane, a subsequent unrelated open does not come up focused. Kept as a
// regression test even though no field remains to leak, because the failure it
// describes is a property of the feature, not of the old implementation.
func TestPendingFocusIsReleasedOnEveryPaneAttachedPath(t *testing.T) {
	tests := []struct {
		name     string
		attached func(p paneHandle) paneAttachedMsg
		cleaning bool
	}{
		{
			name: "open failed",
			attached: func(paneHandle) paneAttachedMsg {
				return paneAttachedMsg{name: "target", err: errors.New("dial failed")}
			},
		},
		{
			name:     "dropped because a clean landed mid-flight",
			attached: func(p paneHandle) paneAttachedMsg { return paneAttachedMsg{name: "target", pane: p} },
			cleaning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newFakeFocusWatcher()
			m, dir := focusRequestModel(t, w, runningSess("target"), runningSess("other"))
			m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("X"), nil }
			writePendingRequest(t, dir, "target", 0)

			m, cmd := update(t, m, freshRequestMsg("target"))
			// The precondition is that the dispatched open actually carries the
			// focus — the message-borne equivalent of the old "the name is set"
			// check, and what makes the rest of this row meaningful.
			if attached := drainForPaneAttached(t, cmd); !attached.focusOnArrival {
				t.Fatalf("precondition: dispatched open = %+v, want focusOnArrival", attached)
			}

			if tt.cleaning {
				m.markCleaning(cleanIDByName("target"))
			}
			m, _ = update(t, m, tt.attached(newFakePane("DOOMED")))

			// The proof the leak is closed: an ordinary `enter` on a DIFFERENT
			// session must not arrive focused.
			later := newFakePane("LATER")
			m, _ = update(t, m, paneAttachedMsg{name: "other", pane: later})
			if m.paneOwnsKeyboard() {
				t.Error("an unrelated later open inherited the dead click's focus")
			}
		})
	}
}

// readWatchMsg runs a watch subscription Cmd and returns its message, or nil if it
// produces none promptly. It runs on its own goroutine because the subscription
// blocks on its channel by design.
func readWatchMsg(t *testing.T, cmd tea.Cmd) *focusRequestMsg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	out := make(chan focusRequestMsg, 1)
	go func() {
		if msg, ok := cmd().(focusRequestMsg); ok {
			out <- msg
		}
	}()
	select {
	case got := <-out:
		return &got
	case <-time.After(time.Second):
		return nil
	}
}

// TestFsnotifyErrorsAreDrained is the fix for a comment that asserted something
// the dependency's source contradicts.
//
// Verified in fsnotify@v1.10.1: fsnotify.go:280 builds the watcher with
// `make(chan error)` and backend_kqueue.go:186 sets defaultBufferSize = 0, so the
// Errors channel is UNBUFFERED; shared.go's sendError blocks in a select on
// `w.Errors <- err`; and backend_kqueue.go's readEvents calls sendError and
// sendEvent from the same goroutine. So one undrained error stops event delivery
// for the rest of the run — the fast path silently degrades to the 2s poll.
//
// This asserts the observable consequence rather than the mechanism (the error path
// cannot be provoked on demand): the watcher keeps delivering events, and Close
// ends the drain rather than leaking its goroutine. The goroutine count is the part
// that would catch a drain that never exits.
func TestFsnotifyErrorsAreDrained(t *testing.T) {
	dir := t.TempDir()
	before := runtime.NumGoroutine()

	w := startFocusWatcher(dir)
	if w == nil {
		t.Skip("no fsnotify watch available in this environment; the fallback poll covers it")
	}

	// Several requests in a row: with the backend goroutine stalled on an
	// undrained error, delivery would stop partway through.
	for i, name := range []string{"one", "two", "three"} {
		if err := dashmeta.WriteRequest(dir, name, dashPID, time.Now()); err != nil {
			t.Fatalf("WriteRequest %d: %v", i, err)
		}
		got := readWatchMsg(t, focusRequestWatchCmd(w, dir, dashPID))
		if got == nil {
			t.Fatalf("no event delivered for request %d (%q): the backend goroutine stalled", i+1, name)
		}
		if err := dashmeta.RemoveRequest(dir, dashPID); err != nil {
			t.Fatalf("RemoveRequest: %v", err)
		}
	}

	// The wiring itself, asserted directly: something must be RECEIVING on
	// Errors. A send that does not block proves the drain goroutine exists — and
	// this is the only check that fails when the `go drainErrors(...)` call is
	// removed, since a real watch error cannot be provoked on demand.
	//
	// Sending as the backend does (an unbuffered channel the test owns nothing of)
	// is safe here precisely because the drain discards: the value is never read
	// back, only accepted.
	fw, ok := w.(fsnotifyWatcher)
	if !ok {
		t.Fatalf("startFocusWatcher returned %T, want fsnotifyWatcher", w)
	}
	sent := make(chan struct{})
	go func() {
		fw.w.Errors <- errors.New("synthetic watch error")
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("a send on fsnotify's Errors channel blocked: nothing is draining it, so " +
			"the backend goroutine that also delivers events would stall on the first real error")
	}

	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// The drain must exit on Close, and the count must come back to where it
	// started — not "within one of it". A tolerance of +1 is exactly the size of
	// the leak being tested for, so it would pass a drain that never exits.
	// Polling rather than sleeping once: the goroutine ends when the backend
	// closes the channel, which is not instantaneous.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Errorf("goroutines = %d after Close, started at %d: the Errors drain did not exit", got, before)
	}
}

// --- the multi-project rendezvous ---

// rendezvousFixture is a dashboard over N projects, each with its own git common
// dir and its own watcher, assembled through the SCAN rather than by hand: the
// per-project rendezvous only exists because a scan pass resolved it, so a
// fixture that installed the map directly would test a state production never
// reaches.
//
// Project paths are synthetic ("/proj/alpha"); only the git common dirs are real
// directories, because those are what dashmeta writes into and what fsnotify
// would watch.
type rendezvousFixture struct {
	// dirs maps project path to the git common dir the RunGit seam answers with.
	// A project absent from this map is one whose rev-parse fails — the
	// unresolvable-rendezvous row.
	dirs map[string]string
	// watchers maps git common dir to the fake watcher handed out for it, so a
	// test can push an event onto a named project's watcher.
	watchers map[string]*fakeFocusWatcher
	// sessions is what Discover answers per project: the active project's rows AND
	// the scan cache's rows both come from here, exactly as production does.
	sessions map[string][]discover.SessionState
	// scanned is what the Scan seam returns; a test narrows it to simulate a
	// project leaving the search roots.
	scanned []string
	// noWatch makes StartFocusWatcher return nil for every project — the degraded
	// path where the fallback poll is the whole mechanism.
	noWatch bool
	// gitArgs records the argv the RunGit seam was called with, per project.
	gitArgs map[string][]string
	// msgs receives every message the Model's OWN dispatched Cmds produce, the
	// way bubbletea's runtime would deliver them. It is what fire reads a click's
	// focusRequestMsg from, and that routing is the fixture's whole defense
	// against the bug that shipped once already: fire used to build its own
	// focusRequestWatchCmd around the watcher, so the pipeline worked in tests
	// even though applyRendezvous never armed an adopted watcher in production.
	// Now the ONLY thing pumping a watcher is a subscription the production code
	// dispatched — remove that dispatch and fire times out.
	msgs chan tea.Msg
}

// newRendezvousFixture creates a real git-common-dir directory per project and
// returns a fixture whose first project is the LAUNCH one (pinned, active, and
// the owner of Deps.GitCommonDir), the way dash.go hands them over.
func newRendezvousFixture(t *testing.T, projects ...string) *rendezvousFixture {
	t.Helper()
	f := &rendezvousFixture{
		dirs:     map[string]string{},
		watchers: map[string]*fakeFocusWatcher{},
		sessions: map[string][]discover.SessionState{},
		scanned:  projects,
		gitArgs:  map[string][]string{},
		msgs:     make(chan tea.Msg, 128),
	}
	// mkdtemp under /tmp rather than t.TempDir(): a rendezvous path is also a
	// socket-length-sensitive path elsewhere in this repo, and /tmp keeps these
	// short and symlink-free enough to compare as strings.
	base := t.TempDir()
	for _, p := range projects {
		dir := filepath.Join(base, filepath.Base(p)+".git")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		f.dirs[p] = dir
	}
	return f
}

// deps builds the Deps a dashboard over this fixture's projects gets.
func (f *rendezvousFixture) deps() Deps {
	d := testDeps()
	// A recordable tmux location, so dashmeta.WriteEntry actually writes: its own
	// gates no-op on an empty tmux or pane id.
	d.Tmux, d.PaneID = "/tmp/tmux-501/default,1,0", "%7"
	d.ClientTTY, d.StartedAt = "/dev/ttys004", "2026-08-19T12:00:00Z"
	d.Discover = func(repo string) ([]discover.SessionState, error) {
		return f.sessions[repo], nil
	}
	d.RunGit = func(dir string, args ...string) (string, error) {
		f.gitArgs[dir] = args
		got, ok := f.dirs[dir]
		if !ok {
			return "", errors.New("not a git repository: " + dir)
		}
		return got + "\n", nil
	}
	d.StartFocusWatcher = func(dir string) focusWatcher {
		if f.noWatch {
			return nil
		}
		w := newFakeFocusWatcher()
		f.watchers[dir] = w
		return w
	}
	d.LoadConfig = func() (SearchSettings, string, error) {
		return SearchSettings{Roots: []string{"/roots"}, Interval: time.Minute, MaxDepth: 3}, "/cfg/config.toml", nil
	}
	d.Scan = func([]string, int) []project.Project { return scannedProjects(f.scanned...) }
	return d
}

// model builds the dashboard: the first project pinned and active, its git
// common dir as the launch rendezvous.
func (f *rendezvousFixture) model(t *testing.T, launch string) Model {
	t.Helper()
	d := f.deps()
	d.Projects = []project.Project{pinnedProject(launch)}
	d.ActiveProjectIndex = 0
	d.GitCommonDir = f.dirs[launch]
	m := New(launch, time.Second, d)
	m.ready = true
	m.width = 120
	m.height = 40
	// The launch watcher's subscription is Init's dispatch in production
	// (model.go's Init, pinned by TestInitArmsTheWatchSubscriptionOnlyWhenAvailable).
	// The fixture pumps that same Cmd rather than running the whole Init batch,
	// whose own scanCmd member would race the explicit scans the tests drive and
	// register orphan watchers over the same directories.
	f.pump(focusRequestWatchCmd(m.focusWatch, m.gitCommonDir, m.pid))
	return m
}

// pump runs cmd on its own goroutine and forwards its message to f.msgs — the
// contract bubbletea's runtime gives every dispatched Cmd.
func (f *rendezvousFixture) pump(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if msg := cmd(); msg != nil {
			f.msgs <- msg
		}
	}()
}

// scan runs one real scan pass through Update and executes the Cmds it returns,
// so the entry writes and removals actually hit the disk.
//
// Members that block past cmdDeadline — the watch subscriptions applyRendezvous
// arms for adopted watchers — are NOT abandoned the way collectMsgs abandons
// them: their eventual messages are forwarded to f.msgs, because those armed
// subscriptions are the only pump fire may read a click through. Everything that
// finishes promptly (entry writes and removals) is waited for, so assertions
// against the disk right after scan stay deterministic.
func (f *rendezvousFixture) scan(t *testing.T, m Model) Model {
	t.Helper()
	cmd := m.scanCmd()
	if cmd == nil {
		t.Fatal("the fixture's scan seams must be live")
	}
	msg, ok := cmd().(projectScanMsg)
	if !ok {
		t.Fatal("scanCmd must produce a projectScanMsg")
	}
	m, applied := update(t, m, msg)
	f.runForwarding(t, applied)
	return m
}

// runForwarding executes every member of cmd's batch on its own goroutine and
// waits up to cmdDeadline for them; members still parked then keep running and
// their eventual messages land on f.msgs. A non-batch cmd is simply run — its
// side effects are what the callers need.
func (f *rendezvousFixture) runForwarding(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	first := cmd()
	batch, ok := first.(tea.BatchMsg)
	if !ok {
		return
	}
	done := make(chan tea.Msg)
	remaining := 0
	for _, c := range batch {
		if c == nil {
			continue
		}
		remaining++
		go func(c tea.Cmd) { done <- c() }(c)
	}
	deadline := time.After(cmdDeadline)
	for remaining > 0 {
		select {
		case <-done:
			remaining--
		case <-deadline:
			// done is unbuffered, so this forwarder is what lets each parked
			// subscription goroutine hand off its message instead of leaking
			// blocked on the send.
			n := remaining
			go func() {
				for i := 0; i < n; i++ {
					if msg := <-done; msg != nil {
						f.msgs <- msg
					}
				}
			}()
			return
		}
	}
}

// fire publishes a request for name into project's rendezvous, pushes the
// filesystem event, and returns the focusRequestMsg the Model's OWN armed
// subscription produces for it. It deliberately arms nothing itself: if
// production never dispatched focusRequestWatchCmd for this project's watcher
// (Init for the launch project, applyRendezvous for an adopted one), nothing is
// receiving on the watcher's channel and this fails — which is the point.
func (f *rendezvousFixture) fire(t *testing.T, m Model, project, name string) focusRequestMsg {
	t.Helper()
	dir := f.dirs[project]
	writePendingRequest(t, dir, name, 0)
	w := f.watchers[dir]
	if w == nil {
		t.Fatalf("no watcher for project %s (dir %s)", project, dir)
	}
	w.events <- fsnotify.Event{Name: dashmeta.RequestPath(dir, m.pid), Op: fsnotify.Create}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-f.msgs:
			if fr, ok := msg.(focusRequestMsg); ok && fr.fromWatch && fr.dir == dir {
				return fr
			}
		case <-deadline:
			t.Fatalf("no armed subscription consumed %s's watcher event — the production code never dispatched focusRequestWatchCmd for it", project)
		}
	}
}

// rendezvousSession is a running session inside project, with a worktree under
// it so the opened pane's config identifies which project it came from.
func rendezvousSession(project, name string) discover.SessionState {
	st := runningSess(name).SessionState
	st.WorktreePath = project + "/wt/" + name
	return st
}

// TestScanWritesAnEntryForEveryProject is acceptance criterion one, and it is
// also the argv check: the rev-parse must carry --path-format=absolute, without
// which --git-common-dir answers a RELATIVE path from anywhere but the repo root
// (verified on git 2.51 in W1.T2) and dashmeta.WriteEntry silently declines it.
//
// The launch project's entry counts too: Init writes that one, and the scan
// rewriting it is harmless because the payload is identical.
func TestScanWritesAnEntryForEveryProject(t *testing.T) {
	f := newRendezvousFixture(t, "/proj/alpha", "/proj/beta")
	m := f.model(t, "/proj/alpha")
	m = f.scan(t, m)

	for _, p := range []string{"/proj/alpha", "/proj/beta"} {
		if _, found := dashmeta.ReadEntry(f.dirs[p]); !found {
			t.Errorf("no dashmeta entry under %s's rendezvous (%s)", p, f.dirs[p])
		}
		want := []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}
		if got := f.gitArgs[p]; !pathsEqual(got, want) {
			t.Errorf("rev-parse for %s called with %v, want %v", p, got, want)
		}
	}
	if len(m.rendezvous) != 2 {
		t.Errorf("rendezvous = %v, want one per project", m.rendezvous)
	}
}

// TestFocusRequestFromAnotherProjectSwitchesThenOpens is the feature: a click on
// a notification from a project that is NOT active has to switch the dashboard
// there before it can open the session, because every per-project command closes
// over m.repo.
//
// The worktree assertion is the load-bearing one — it comes from the scan cache
// for the firing project, which is the only place a session outside the active
// project is known at all (switchProject clears m.sessions).
func TestFocusRequestFromAnotherProjectSwitchesThenOpens(t *testing.T) {
	f := newRendezvousFixture(t, "/proj/alpha", "/proj/beta")
	f.sessions["/proj/alpha"] = []discover.SessionState{rendezvousSession("/proj/alpha", "here")}
	f.sessions["/proj/beta"] = []discover.SessionState{rendezvousSession("/proj/beta", "target")}
	m := f.model(t, "/proj/alpha")
	m = f.scan(t, m)

	var gotCfg hostPaneConfig
	fp := newFakePane("BETA-PANE")
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		gotCfg = cfg
		return fp, nil
	}

	msg := f.fire(t, m, "/proj/beta", "target")
	if msg.dir != f.dirs["/proj/beta"] {
		t.Fatalf("message carried dir %q, want beta's rendezvous %q", msg.dir, f.dirs["/proj/beta"])
	}

	m, cmd := update(t, m, msg)
	if m.repo != "/proj/beta" || m.activeProjectPath() != "/proj/beta" {
		t.Fatalf("active project = %q repo = %q, want the firing project", m.activeProjectPath(), m.repo)
	}
	attached := drainForPaneAttached(t, cmd)
	if !attached.focusOnArrival || !attached.focusRequest {
		t.Errorf("attached = {focusOnArrival:%v focusRequest:%v}, want both true", attached.focusOnArrival, attached.focusRequest)
	}
	if gotCfg.Name != "target" || gotCfg.Worktree != "/proj/beta/wt/target" {
		t.Fatalf("opened %+v, want beta's target worktree", gotCfg)
	}

	m, _ = update(t, m, attached)
	if m.pane == nil || m.paneName != "target" || !m.paneOwnsKeyboard() {
		t.Fatalf("pane=%v name=%q focused=%v, want beta's target focused", m.pane != nil, m.paneName, m.paneOwnsKeyboard())
	}
	if _, found := dashmeta.ReadRequest(f.dirs["/proj/beta"], m.pid); found {
		t.Error("the honored request must be consumed from the project it arrived in")
	}
}

// TestFocusRequestResolvesTheFiringProject is why the project comes from the
// WATCHER and not from the request: `afk notify` writes a session NAME, and
// dashmeta.Request has no project field (deliberately — changing it would change
// afk notify's target resolution). Two projects holding a session with the same
// name is therefore only decidable by which rendezvous the file landed in.
//
// Both rows are load-bearing: the non-active project must win when its watcher
// fires (the bug would open the active project's same-named session), and the
// active project must stay put when its own watcher fires (the bug would be a
// switch to nowhere, tearing down the pane).
func TestFocusRequestResolvesTheFiringProject(t *testing.T) {
	tests := []struct {
		name         string
		firing       string
		wantActive   string
		wantWorktree string
	}{
		{
			name:         "the non-active project's watcher wins",
			firing:       "/proj/beta",
			wantActive:   "/proj/beta",
			wantWorktree: "/proj/beta/wt/dup",
		},
		{
			name:         "the active project's own watcher stays put",
			firing:       "/proj/alpha",
			wantActive:   "/proj/alpha",
			wantWorktree: "/proj/alpha/wt/dup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRendezvousFixture(t, "/proj/alpha", "/proj/beta")
			f.sessions["/proj/alpha"] = []discover.SessionState{rendezvousSession("/proj/alpha", "dup")}
			f.sessions["/proj/beta"] = []discover.SessionState{rendezvousSession("/proj/beta", "dup")}
			m := f.model(t, "/proj/alpha")
			m = f.scan(t, m)
			// The active project's rows come from m.sessions, which only a discovery
			// pass populates — the same one the launch dashboard runs on its own tick.
			m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen,
				sessions: []Session{{SessionState: rendezvousSession("/proj/alpha", "dup")}},
				at:       m.deps.Now(),
			})

			var gotCfg hostPaneConfig
			m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
				gotCfg = cfg
				return newFakePane("PANE"), nil
			}

			m, cmd := update(t, m, f.fire(t, m, tt.firing, "dup"))
			if got := m.activeProjectPath(); got != tt.wantActive {
				t.Errorf("active project = %q, want %q", got, tt.wantActive)
			}
			drainForPaneAttached(t, cmd)
			if gotCfg.Worktree != tt.wantWorktree {
				t.Errorf("opened worktree %q, want %q", gotCfg.Worktree, tt.wantWorktree)
			}
		})
	}
}

// TestRescanRefreshesTheRendezvousSet pins the add/remove half: the set follows
// the project list, or a dashboard that ran for a day would be watching
// directories for projects the user deleted and advertising a pane into them.
//
// The kept project's watcher must be the SAME instance — recreating it on every
// pass would burn a kqueue descriptor a minute and drop the events arriving
// between the two.
func TestRescanRefreshesTheRendezvousSet(t *testing.T) {
	f := newRendezvousFixture(t, "/proj/alpha", "/proj/beta")
	m := f.model(t, "/proj/alpha")
	m = f.scan(t, m)

	alphaWatch := f.watchers[f.dirs["/proj/alpha"]]
	betaWatch := f.watchers[f.dirs["/proj/beta"]]
	if betaWatch == nil {
		t.Fatal("precondition: beta must have its own watcher")
	}
	if _, found := dashmeta.ReadEntry(f.dirs["/proj/beta"]); !found {
		t.Fatal("precondition: beta must have an entry to remove")
	}
	keptBefore := m.rendezvous["/proj/alpha"]

	// Beta leaves the search roots. Alpha is pinned, so it survives regardless.
	f.scanned = []string{"/proj/alpha"}
	m = f.scan(t, m)

	if _, ok := m.rendezvous["/proj/beta"]; ok {
		t.Error("beta's rendezvous outlived beta")
	}
	if betaWatch.closed != 1 {
		t.Errorf("beta's watcher closed %d times, want 1", betaWatch.closed)
	}
	if _, found := dashmeta.ReadEntry(f.dirs["/proj/beta"]); found {
		t.Error("beta's dashmeta entry must be removed with it — a click would aim at a project this dashboard no longer watches")
	}
	if m.rendezvous["/proj/alpha"] != keptBefore {
		t.Error("the surviving project's rendezvous was rebuilt instead of kept")
	}
	if alphaWatch != nil && alphaWatch.closed != 0 {
		t.Errorf("the launch watcher was closed by a rescan (%d times)", alphaWatch.closed)
	}
	if _, found := dashmeta.ReadEntry(f.dirs["/proj/alpha"]); !found {
		t.Error("the surviving project's entry was removed")
	}

	// Beta comes back: the set has to grow again, not just shrink.
	f.scanned = []string{"/proj/alpha", "/proj/beta"}
	m = f.scan(t, m)
	if _, ok := m.rendezvous["/proj/beta"]; !ok {
		t.Error("a project that reappeared got no rendezvous")
	}
	if _, found := dashmeta.ReadEntry(f.dirs["/proj/beta"]); !found {
		t.Error("a project that reappeared got no entry")
	}
}

// TestQuitClearsEveryProjectsRendezvous is the teardown: requestQuit closed one
// watcher and removed one entry when there was only ever one project. Leaving any
// behind leaks a descriptor and, worse, leaves a stale entry aiming clicks at a
// tmux pane with no dashboard in it.
func TestQuitClearsEveryProjectsRendezvous(t *testing.T) {
	f := newRendezvousFixture(t, "/proj/alpha", "/proj/beta", "/proj/gamma")
	m := f.model(t, "/proj/alpha")
	m = f.scan(t, m)
	for _, p := range []string{"/proj/alpha", "/proj/beta", "/proj/gamma"} {
		if _, found := dashmeta.ReadEntry(f.dirs[p]); !found {
			t.Fatalf("precondition: %s has no entry to remove", p)
		}
	}

	m = pressKey(t, m, keyRune('q'))

	for dir, w := range f.watchers {
		if w.closed != 1 {
			t.Errorf("watcher for %s closed %d times, want 1", dir, w.closed)
		}
	}
	for _, p := range []string{"/proj/alpha", "/proj/beta", "/proj/gamma"} {
		if _, found := dashmeta.ReadEntry(f.dirs[p]); found {
			t.Errorf("%s's entry survived the quit", p)
		}
	}
	if m.rendezvous != nil || m.focusWatch != nil {
		t.Error("the rendezvous set must be cleared so a second quit cannot double-close it")
	}
}

// TestPollSweepsEveryProject is the degradation rule from this file's package
// doc, generalized: no-watch degrades to SLOW, never to dead. With no fsnotify
// anywhere, the 2s tick has to check EVERY project's rendezvous, and the message
// it produces has to say which one it came from — the request schema cannot.
func TestPollSweepsEveryProject(t *testing.T) {
	f := newRendezvousFixture(t, "/proj/alpha", "/proj/beta")
	f.noWatch = true
	f.sessions["/proj/beta"] = []discover.SessionState{rendezvousSession("/proj/beta", "target")}
	m := f.model(t, "/proj/alpha")
	if m.focusWatch != nil {
		t.Fatal("precondition: this dashboard must have no fast path at all")
	}
	m = f.scan(t, m)
	writePendingRequest(t, f.dirs["/proj/beta"], "target", 0)

	_, cmd := update(t, m, tickMsg(time.Unix(1, 0)))
	var got focusRequestMsg
	polled := map[string]bool{}
	for _, msg := range collectMsgs(t, cmd) {
		fr, ok := msg.(focusRequestMsg)
		if !ok {
			continue
		}
		polled[fr.dir] = true
		if fr.found {
			got = fr
		}
	}
	for _, p := range []string{"/proj/alpha", "/proj/beta"} {
		if !polled[f.dirs[p]] {
			t.Errorf("the tick did not poll %s's rendezvous (%s); polled %v", p, f.dirs[p], polled)
		}
	}
	if !got.found || got.request.Name != "target" || got.dir != f.dirs["/proj/beta"] {
		t.Fatalf("poll produced %+v, want the pending request tagged with beta's rendezvous", got)
	}
	if got.fromWatch {
		t.Error("a poll-produced message must not claim to be from a watch")
	}

	var gotCfg hostPaneConfig
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		gotCfg = cfg
		return newFakePane("PANE"), nil
	}
	m, honored := update(t, m, got)
	if m.activeProjectPath() != "/proj/beta" {
		t.Errorf("active project = %q, want the polled project", m.activeProjectPath())
	}
	drainForPaneAttached(t, honored)
	if gotCfg.Worktree != "/proj/beta/wt/target" {
		t.Errorf("opened %+v, want beta's target", gotCfg)
	}
}

// TestRendezvousDegradesPerProject is the isolation rule: notification absence is
// never an error, so one project whose rev-parse fails or whose afk-dash/
// cannot be created must cost that project its click-to-focus and nothing else.
//
// Load-bearing rows: "unresolvable" is a directory that is not a repo at all (a
// search root can hold anything), and "unwritable" is a rendezvous whose
// afk-dash/ cannot be created — the two independent failures the plan names.
// Both assert the OTHER project still works, which is what "degrade alone" means.
func TestRendezvousDegradesPerProject(t *testing.T) {
	tests := []struct {
		name   string
		broken func(t *testing.T, f *rendezvousFixture)
	}{
		{
			name: "unresolvable git common dir",
			broken: func(t *testing.T, f *rendezvousFixture) {
				t.Helper()
				delete(f.dirs, "/proj/broken")
			},
		},
		{
			name: "unwritable rendezvous directory",
			broken: func(t *testing.T, f *rendezvousFixture) {
				t.Helper()
				// A regular FILE where afk-dash/ has to be: EnsureDir fails, so both
				// the watcher and the entry write fail, for this project alone.
				blocked := filepath.Join(f.dirs["/proj/broken"], "afk-dash")
				if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
					t.Fatalf("blocking afk-dash/: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRendezvousFixture(t, "/proj/alpha", "/proj/broken")
			f.sessions["/proj/alpha"] = []discover.SessionState{rendezvousSession("/proj/alpha", "target")}
			m := f.model(t, "/proj/alpha")
			tt.broken(t, f)
			// The real watcher, so an unwritable directory fails the way it would in
			// production rather than through a fake that cannot fail.
			m.deps.StartFocusWatcher = startFocusWatcher
			m = f.scan(t, m)

			if m.status != "" {
				t.Errorf("status = %q, want silence: a project with no rendezvous is not an error", m.status)
			}
			if len(m.projects) != 2 {
				t.Errorf("projects = %v, want both — a broken rendezvous must not drop the project", projectPaths(m.projects))
			}
			if rz, ok := m.rendezvous["/proj/broken"]; ok && rz.watch != nil {
				t.Error("the broken project got a working watcher")
			}

			// The healthy project still honors a click, which is the whole point.
			var gotCfg hostPaneConfig
			m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
				gotCfg = cfg
				return newFakePane("PANE"), nil
			}
			m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen,
				sessions: []Session{{SessionState: rendezvousSession("/proj/alpha", "target")}},
				at:       m.deps.Now(),
			})
			dir := f.dirs["/proj/alpha"]
			writePendingRequest(t, dir, "target", 0)
			msg := *readWatchMsg(t, focusRequestPollCmd(dir, m.pid))
			_, cmd := update(t, m, msg)
			drainForPaneAttached(t, cmd)
			if gotCfg.Name != "target" {
				t.Errorf("opened %+v, want the healthy project's session", gotCfg)
			}
		})
	}
}

// TestRendezvousDirsDedupesTheLaunchDirectory is pure and covers the one case
// that would double every rendezvous side effect: the launch project's git common
// dir is ALSO resolved by the scan, so without the dedupe the tick would poll it
// twice, the scan would write its entry twice, and quit would remove it twice.
//
// Load-bearing rows: "launch project also scanned" is the ordinary case (a user
// standing in a project their search roots cover); "no launch dir" is the
// outside-a-repo launch, where the launch half contributes nothing.
func TestRendezvousDirsDedupesTheLaunchDirectory(t *testing.T) {
	tests := []struct {
		name       string
		launchDir  string
		rendezvous map[string]focusRendezvous
		want       []string
	}{
		{
			name:       "launch project also scanned",
			launchDir:  "/g/a",
			rendezvous: map[string]focusRendezvous{"/p/a": {dir: "/g/a"}, "/p/b": {dir: "/g/b"}},
			want:       []string{"/g/a", "/g/b"},
		},
		{
			name:       "no launch dir",
			launchDir:  "",
			rendezvous: map[string]focusRendezvous{"/p/a": {dir: "/g/a"}, "/p/b": {dir: "/g/b"}},
			want:       []string{"/g/a", "/g/b"},
		},
		{
			name:       "launch dir with no scanned project of its own",
			launchDir:  "/g/launch",
			rendezvous: map[string]focusRendezvous{"/p/b": {dir: "/g/b"}},
			want:       []string{"/g/launch", "/g/b"},
		},
		{
			name:      "no rendezvous at all",
			launchDir: "/g/a",
			want:      []string{"/g/a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				gitCommonDir: tt.launchDir,
				projects:     scannedProjects("/p/a", "/p/b"),
				rendezvous:   tt.rendezvous,
			}
			if got := m.rendezvousDirs(); !pathsEqual(got, tt.want) {
				t.Errorf("rendezvousDirs = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestScanArmsAdoptedWatchSubscriptions is the direct pin on the dispatch this
// fixture exists to detect: applyRendezvous must arm a focusRequestWatchCmd for
// every watcher it adopts from a scan, or the fsnotify fast path is dead for
// every project but the launch one — the watcher's events channel has no
// receiver, one kqueue descriptor plus two goroutines sit idle per project, and
// only the 2s poll carries clicks. fire arms nothing itself (see its doc), so
// this fails by timeout if the production dispatch is ever removed again.
func TestScanArmsAdoptedWatchSubscriptions(t *testing.T) {
	f := newRendezvousFixture(t, "/proj/alpha", "/proj/beta")
	f.sessions["/proj/beta"] = []discover.SessionState{rendezvousSession("/proj/beta", "target")}
	m := f.model(t, "/proj/alpha")
	m = f.scan(t, m)

	if rz, ok := m.rendezvous["/proj/beta"]; !ok || rz.watch == nil {
		t.Fatal("precondition: the scan must have adopted a live watcher for beta")
	}

	msg := f.fire(t, m, "/proj/beta", "target")
	if !msg.fromWatch {
		t.Error("the adopted watcher's message must carry fromWatch, or Update will never re-arm it")
	}
	if msg.dir != f.dirs["/proj/beta"] {
		t.Errorf("message dir = %q, want beta's rendezvous %q", msg.dir, f.dirs["/proj/beta"])
	}
	if !msg.found || msg.request.Name != "target" {
		t.Errorf("message = %+v, want the pending request for target", msg)
	}
}

// TestSharedGitCommonDirClickStaysInTheActiveProject covers two project entries
// resolving to ONE git common dir — a pinned worktree beside its scanned main
// checkout, confirmed reachable on this disk (issues.md, W5.T3). The request
// file cannot name a project, so attribution used to fall to project-list order,
// which puts the scanned sibling first and made a click SWITCH the user away
// from the worktree they launched in. The active project must win the shared
// directory: both entries enumerate the same repo, so staying put opens the same
// session under the label the user is standing in.
func TestSharedGitCommonDirClickStaysInTheActiveProject(t *testing.T) {
	f := newRendezvousFixture(t, "/proj/wt", "/proj/main")
	// One directory for both projects, and only the sibling is scanned — the
	// launch worktree survives as the pinned entry, LAST in unionProjects order,
	// which is exactly the ordering that used to lose the attribution.
	f.dirs["/proj/main"] = f.dirs["/proj/wt"]
	f.scanned = []string{"/proj/main"}
	shared := rendezvousSession("/proj/wt", "target")
	f.sessions["/proj/wt"] = []discover.SessionState{shared}
	f.sessions["/proj/main"] = []discover.SessionState{shared}
	m := f.model(t, "/proj/wt")
	m = f.scan(t, m)

	if got := m.activeProjectPath(); got != "/proj/wt" {
		t.Fatalf("precondition: active project = %q, want the pinned launch worktree", got)
	}
	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen,
		sessions: []Session{{SessionState: shared}},
		at:       m.deps.Now(),
	})

	var gotCfg hostPaneConfig
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		gotCfg = cfg
		return newFakePane("PANE"), nil
	}

	m, cmd := update(t, m, f.fire(t, m, "/proj/wt", "target"))
	if got := m.activeProjectPath(); got != "/proj/wt" {
		t.Errorf("active project = %q — the click switched to the sibling sharing the git common dir", got)
	}
	drainForPaneAttached(t, cmd)
	if gotCfg.Name != "target" {
		t.Errorf("opened %+v, want the clicked session", gotCfg)
	}
}

// TestWatchRegistrationFailureIsRetriedOnALaterScan pins the recovery half of
// per-project degradation: a project whose fsnotify registration fails outright
// is adopted poll-only, and before the retry that verdict was PERMANENT — no
// later scan re-attempted the watch, undisclosed and at odds with how a failed
// dir resolution already retried. Three load-bearing checks: the second scan
// must land a live watcher on the degraded project; fire proves it was ARMED,
// not merely held (fire arms nothing itself, so an unarmed upgrade times out
// here); and the launch project is exempt — its watch is nil by design, so the
// gitArgs assertion fails if the retry starts re-resolving it every pass.
func TestWatchRegistrationFailureIsRetriedOnALaterScan(t *testing.T) {
	f := newRendezvousFixture(t, "/proj/alpha", "/proj/beta")
	f.sessions["/proj/beta"] = []discover.SessionState{rendezvousSession("/proj/beta", "target")}
	m := f.model(t, "/proj/alpha")

	f.noWatch = true
	m = f.scan(t, m)
	if rz, ok := m.rendezvous["/proj/beta"]; !ok || rz.watch != nil {
		t.Fatalf("precondition: beta must be adopted poll-only (rendezvous %+v, held %v)", m.rendezvous["/proj/beta"], ok)
	}

	f.noWatch = false
	f.gitArgs = map[string][]string{}
	m = f.scan(t, m)

	if rz := m.rendezvous["/proj/beta"]; rz.watch == nil {
		t.Fatal("a later scan never retried beta's watch registration — permanent poll-only degradation")
	}
	if _, resolved := f.gitArgs["/proj/alpha"]; resolved {
		t.Error("the launch project was re-resolved: its nil watch is by design (focusWatch covers it), not a failure to retry")
	}
	if rz := m.rendezvous["/proj/alpha"]; rz.watch != nil {
		t.Error("the launch directory grew a second watcher — two watchers on one directory wake twice per click")
	}

	msg := f.fire(t, m, "/proj/beta", "target")
	if !msg.fromWatch || !msg.found || msg.request.Name != "target" {
		t.Errorf("message = %+v, want the retried watcher's armed subscription to deliver the click", msg)
	}
}

// TestFocusRequestSelectsAndRemembersTheNamedSession pins the half of the
// feature applyFocusRequest does not yet touch: a click has to move the
// CURSOR onto the named session, not merely open its pane, or the dashboard's
// list still shows the row the user was on before the notification landed —
// and schedulePreview's own remember step (model.go, the block ahead of every
// refusal) never runs, because applyFocusRequest never calls it.
//
// Row (a) is load-bearing: it is the plain, ordinary case — a running host,
// cursor sitting on an unrelated row — and it is expected to FAIL right now,
// with m.selected() still reading the row the cursor was on ("aaa") rather
// than the clicked "ccc". Rows (c), (d) and (e) extend the same missing
// behavior into the shapes that already have their own tests for the pane
// side (an already-open pane, a stopped host, an active filter); row (b)
// pins what must NOT regress alongside the fix (no stray preview timer), and
// row (f) pins the no-op guard schedulePreview already has for a name that's
// already remembered.
func TestFocusRequestSelectsAndRemembersTheNamedSession(t *testing.T) {
	t.Run("(a) live host, cursor on row 0", func(t *testing.T) {
		w := newFakeFocusWatcher()
		m, _ := focusRequestModel(t, w, runningSess("aaa"), runningSess("bbb"), runningSess("ccc"))
		m.selectSessionIndex(0)
		if s, ok := m.selected(); !ok || s.Name != "aaa" {
			t.Fatalf("precondition: selected = %q (ok=%v), want aaa", s.Name, ok)
		}
		var opened []hostPaneConfig
		m.deps.OpenHostPane = recordingOpen(&opened, newFakePane("CCC"))

		m, cmd := update(t, m, freshRequestMsg("ccc"))
		if cmd == nil {
			t.Fatal("expected the pane open for the named session")
		}
		drainForPaneAttached(t, cmd)

		if s, ok := m.selected(); !ok || s.Name != "ccc" {
			t.Errorf("selected = %q (ok=%v), want the clicked ccc", s.Name, ok)
		}
		if got := m.rememberedSelection["/repo"]; got != "ccc" {
			t.Errorf("rememberedSelection[/repo] = %q, want ccc", got)
		}
		if got := m.dirtySelections["/repo"]; got != "ccc" {
			t.Errorf("dirtySelections[/repo] = %q, want ccc", got)
		}
	})

	t.Run("(b) no preview armed", func(t *testing.T) {
		w := newFakeFocusWatcher()
		m, _ := focusRequestModel(t, w, runningSess("aaa"), runningSess("bbb"), runningSess("ccc"))
		m.selectSessionIndex(0)
		var opened []hostPaneConfig
		m.deps.OpenHostPane = recordingOpen(&opened, newFakePane("CCC"))

		_, cmd := update(t, m, freshRequestMsg("ccc"))
		if cmd == nil {
			t.Fatal("expected the pane open for the named session")
		}
		if msgs := collectMsgsPreview(t, cmd); hasMsg[previewTimerMsg](msgs) {
			t.Error("a focus request armed a previewTimerMsg; a click must not race its own open against a preview dial")
		}
	})

	t.Run("(c) pane already installed for the target", func(t *testing.T) {
		w := newFakeFocusWatcher()
		fp := newFakePane("ALREADY-OPEN")
		m, _ := focusRequestModel(t, w, runningSess("aaa"), runningSess("bbb"), runningSess("ccc"))
		m.selectSessionIndex(0)
		m.pane = fp
		m.paneName = "ccc"
		opens := 0
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
			opens++
			return newFakePane("X"), nil
		}

		m, cmd := update(t, m, freshRequestMsg("ccc"))
		runAllCmds(t, cmd)

		if opens != 0 {
			t.Errorf("OpenHostPane called %d times, want 0 for the session already in the pane", opens)
		}
		if s, ok := m.selected(); !ok || s.Name != "ccc" {
			t.Errorf("selected = %q (ok=%v), want ccc even though the pane was already open", s.Name, ok)
		}
		if got := m.rememberedSelection["/repo"]; got != "ccc" {
			t.Errorf("rememberedSelection[/repo] = %q, want ccc", got)
		}
		if got := m.dirtySelections["/repo"]; got != "ccc" {
			t.Errorf("dirtySelections[/repo] = %q, want ccc", got)
		}
	})

	t.Run("(d) stopped host", func(t *testing.T) {
		w := newFakeFocusWatcher()
		m, _ := focusRequestModel(t, w, runningSess("aaa"), runningSess("bbb"), sess("ccc"))
		m.selectSessionIndex(0)
		m.deps.SpawnHost = func(name, worktree string) (session.HostProcess, error) {
			return session.HostProcess{PID: 4242}, nil
		}
		m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newFakePane("X"), nil }

		m, cmd := update(t, m, freshRequestMsg("ccc"))
		if cmd == nil {
			t.Fatal("expected the restart to be dispatched for the stopped host")
		}

		// Checked BEFORE the restart lands: the selection and the memory both
		// update at request time, not once hostRestartedMsg eventually arrives.
		if s, ok := m.selected(); !ok || s.Name != "ccc" {
			t.Errorf("selected = %q (ok=%v), want ccc before the restart lands", s.Name, ok)
		}
		if got := m.rememberedSelection["/repo"]; got != "ccc" {
			t.Errorf("rememberedSelection[/repo] = %q, want ccc", got)
		}
		if got := m.dirtySelections["/repo"]; got != "ccc" {
			t.Errorf("dirtySelections[/repo] = %q, want ccc", got)
		}
	})

	t.Run("(f) already-remembered name is a no-op for dirty", func(t *testing.T) {
		w := newFakeFocusWatcher()
		m, _ := focusRequestModel(t, w, runningSess("aaa"), runningSess("bbb"), runningSess("ccc"))
		m.selectSessionIndex(0)
		m.rememberedSelection = map[string]string{"/repo": "ccc"}
		var opened []hostPaneConfig
		m.deps.OpenHostPane = recordingOpen(&opened, newFakePane("CCC"))

		m, cmd := update(t, m, freshRequestMsg("ccc"))
		runAllCmds(t, cmd)

		if _, ok := m.dirtySelections["/repo"]; ok {
			t.Errorf("dirtySelections[/repo] = %q, want no entry — the name was already remembered", m.dirtySelections["/repo"])
		}
	})
}
