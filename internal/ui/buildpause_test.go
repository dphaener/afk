package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
)

// pressAction runs a dashboard action the way a person does since T7.2:
// ctrl+], then the action's key. It returns what the action's key
// returned, so a test can still read the command it produced, and it fails the
// test if the dialog did not open, so a later assertion is about the action.
func pressAction(t *testing.T, m Model, k tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	m, _ = update(t, m, leaderKeyPress)
	if !m.leaderOpen {
		t.Fatal("setup: ctrl+] did not open the actions dialog")
	}
	return update(t, m, k)
}

// runAction applies action a as it runs from the session list, without the
// dialog. It is for a handler's own refusal, which the dialog never lets a
// person reach for a dimmed row.
func runAction(m Model, a modeAction) (Model, tea.Cmd) {
	next, cmd := m.applyKeyAction(inputNormal, a, tea.KeyPressMsg{})
	return next.(Model), cmd
}

var (
	keyPause  = keyRune('P')
	keyResume = keyRune('U')
)

// pauseSeams records every call to the two seams and holds the replies the
// test wants them to give.
type pauseSeams struct {
	pauseRoots  []string
	resumeRoots []string
	loads       int

	already   bool
	pauseErr  error
	resumed   ResumeResult
	resumeErr error
}

// buildPauseSnap is a project with a build, whose main checkout is /main. The
// model below is started in /repo, so a test that reads the root off the call
// can tell the snapshot's folder from the dashboard's.
func buildPauseSnap() buildview.Snapshot {
	return buildview.Snapshot{Root: "/main", HasBuild: true, Total: 3, Done: 1}
}

// buildPauseModel is a Model started in /repo with both seams recording into
// the returned struct and a counting build loader. It holds no build snapshot
// yet; most tests add one with withBuild.
func buildPauseModel(t *testing.T, sessions ...Session) (Model, *pauseSeams) {
	t.Helper()
	rec := &pauseSeams{}
	d := testDeps()
	d.PauseBuild = func(root string) (bool, error) {
		rec.pauseRoots = append(rec.pauseRoots, root)
		return rec.already, rec.pauseErr
	}
	d.ResumeBuild = func(root string) (ResumeResult, error) {
		rec.resumeRoots = append(rec.resumeRoots, root)
		return rec.resumed, rec.resumeErr
	}
	d.LoadBuild = func(string, []BuildSession) buildview.Snapshot {
		rec.loads++
		return buildPauseSnap()
	}
	m := New("/repo", time.Second, d)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.setSessions(sessions)
	return m, rec
}

// TestBuildPauseNotices is the wording, one row per outcome the two keys can
// report. Every row is load-bearing in the plain sense that it is a sentence
// the owner reads, but three carry more than their words:
//
//   - "already paused" and "nothing to resume" are info, not success: nothing
//     changed, and a green toast would say something did.
//   - "fresh pause" must say a running task finishes. "Paused" alone reads as
//     "stopped now", and the owner would wait for a session to end that the
//     pause never touches.
//   - the two "no task known" rows keep the sentence whole when the stop
//     marker names no task; "the stop after ." would be the bug.
func TestBuildPauseNotices(t *testing.T) {
	boom := errors.New("the folder /main/.afk/build is read-only")
	cases := []struct {
		name     string
		got      func() (notificationKind, string)
		wantKind notificationKind
		wantText string
	}{
		{
			name:     "fresh pause",
			got:      func() (notificationKind, string) { return pauseNotice(defaultLeaderKey, false) },
			wantKind: notifySuccess,
			wantText: "Build paused. A task that is running finishes; nothing new starts. Press ctrl+] U to resume.",
		},
		{
			name:     "already paused",
			got:      func() (notificationKind, string) { return pauseNotice(defaultLeaderKey, true) },
			wantKind: notifyInfo,
			wantText: "The build was already paused. Press ctrl+] U to resume.",
		},
		{
			name:     "resumed",
			got:      func() (notificationKind, string) { return resumeNotice(ResumeResult{WasPaused: true}) },
			wantKind: notifySuccess,
			wantText: "Build resumed. The daemon may start new tasks again.",
		},
		{
			name: "stop cleared, task known",
			got: func() (notificationKind, string) {
				return resumeNotice(ResumeResult{WasStopped: true, StopTask: "T1.2"})
			},
			wantKind: notifySuccess,
			wantText: "Cleared the stop after T1.2. The daemon carries on at its next pass.",
		},
		{
			name:     "stop cleared, no task known",
			got:      func() (notificationKind, string) { return resumeNotice(ResumeResult{WasStopped: true}) },
			wantKind: notifySuccess,
			wantText: "Cleared the stop. The daemon carries on at its next pass.",
		},
		{
			name: "paused and stopped, task known",
			got: func() (notificationKind, string) {
				return resumeNotice(ResumeResult{WasPaused: true, WasStopped: true, StopTask: "T1.2"})
			},
			wantKind: notifySuccess,
			wantText: "Build resumed, and the stop after T1.2 is cleared. The daemon carries on at its next pass.",
		},
		{
			name: "paused and stopped, no task known",
			got: func() (notificationKind, string) {
				return resumeNotice(ResumeResult{WasPaused: true, WasStopped: true})
			},
			wantKind: notifySuccess,
			wantText: "Build resumed, and the stop is cleared. The daemon carries on at its next pass.",
		},
		{
			name:     "nothing to resume",
			got:      func() (notificationKind, string) { return resumeNotice(ResumeResult{}) },
			wantKind: notifyInfo,
			wantText: "The build was not paused; nothing changed.",
		},
		{
			// A task name with nothing stopped is not a stop: the name alone
			// must not turn "nothing changed" into "cleared".
			name:     "nothing to resume, a stray task name",
			got:      func() (notificationKind, string) { return resumeNotice(ResumeResult{StopTask: "T1.2"}) },
			wantKind: notifyInfo,
			wantText: "The build was not paused; nothing changed.",
		},
		{
			name:     "pause failed",
			got:      func() (notificationKind, string) { return notifyError, pauseFailedNotice(boom) },
			wantKind: notifyError,
			wantText: "Pausing the build did not work: the folder /main/.afk/build is read-only",
		},
		{
			name:     "resume failed",
			got:      func() (notificationKind, string) { return notifyError, resumeFailedNotice(boom) },
			wantKind: notifyError,
			wantText: "Resuming the build did not work: the folder /main/.afk/build is read-only",
		},
		{
			name:     "no build to pause",
			got:      func() (notificationKind, string) { return notifyInfo, noBuildToPauseNotice },
			wantKind: notifyInfo,
			wantText: "This project has no master plan, so there is no build to pause. Set plan in afk.toml.",
		},
		{
			name:     "no build to resume",
			got:      func() (notificationKind, string) { return notifyInfo, noBuildToResumeNotice },
			wantKind: notifyInfo,
			wantText: "This project has no master plan, so there is no build to resume. Set plan in afk.toml.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, text := tc.got()
			if kind != tc.wantKind {
				t.Errorf("kind = %v, want %v", kind, tc.wantKind)
			}
			if text != tc.wantText {
				t.Errorf("text = %q\nwant   %q", text, tc.wantText)
			}
		})
	}
}

// TestBuildPauseKeysCallTheSeam drives `P` and `U` through Update, from the
// list and from a focused overview. Load-bearing:
//
//   - the seam gets the SNAPSHOT's root (/main), not the folder the dashboard
//     was started in (/repo): started inside a session's worktree, the
//     dashboard must still pause the project, whose flag is in the main
//     checkout;
//   - nothing is written on the Update goroutine: the call count is zero
//     until the returned command runs;
//   - one press is one call, and `P` never reaches the resume seam nor `U`
//     the pause seam;
//   - the outcome asks for a fresh read of the build, so the Build tab's
//     summary does not wait for the two-second tick.
func TestBuildPauseKeysCallTheSeam(t *testing.T) {
	cases := []struct {
		name       string
		key        tea.Msg
		fromDetail bool
		wantPause  int
		wantResume int
		wantNotice string
	}{
		{"P from the list", keyPause, false, 1, 0, "Build paused. A task that is running finishes; nothing new starts. Press ctrl+] U to resume."},
		{"P from a focused overview", keyPause, true, 1, 0, "Build paused. A task that is running finishes; nothing new starts. Press ctrl+] U to resume."},
		{"U from the list", keyResume, false, 0, 1, "Build resumed. The daemon may start new tasks again."},
		{"U from a focused overview", keyResume, true, 0, 1, "Build resumed. The daemon may start new tasks again."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := buildPauseModel(t, sess("alpha"))
			rec.resumed = ResumeResult{WasPaused: true}
			m = withBuild(t, m, buildPauseSnap())
			wantMode := inputNormal
			if tc.fromDetail {
				m, _ = pressAction(t, m, keyB)
				wantMode = inputDetail
			}
			if got := activeInputMode(m.modeSnapshot()); got != wantMode {
				t.Fatalf("fixture broken: mode = %v, want %v", got, wantMode)
			}
			focus, view, tab := m.focus, m.view, m.detailTab

			m, cmd := pressAction(t, m, tc.key)
			if cmd == nil {
				t.Fatal("the key returned no command")
			}
			if len(rec.pauseRoots)+len(rec.resumeRoots) != 0 {
				t.Fatal("the seam was called from Update, not from a command")
			}
			if m.focus != focus || m.view != view || m.detailTab != tab {
				t.Errorf("the key moved the screen: focus=%v view=%v tab=%v", m.focus, m.view, m.detailTab)
			}

			msgs := collectMsgs(t, cmd)
			if len(msgs) != 1 {
				t.Fatalf("the key produced %d messages, want one", len(msgs))
			}
			if len(rec.pauseRoots) != tc.wantPause || len(rec.resumeRoots) != tc.wantResume {
				t.Fatalf("pause calls = %v, resume calls = %v, want %d and %d",
					rec.pauseRoots, rec.resumeRoots, tc.wantPause, tc.wantResume)
			}
			for _, root := range append(rec.pauseRoots, rec.resumeRoots...) {
				if root != "/main" {
					t.Errorf("the seam was called with %q, want the snapshot's root /main", root)
				}
			}

			loadsBefore := rec.loads
			m, refresh := update(t, m, msgs[0])
			if m.status != tc.wantNotice {
				t.Errorf("notice = %q, want %q", m.status, tc.wantNotice)
			}
			if m.statusKind != notifySuccess {
				t.Errorf("notice kind = %v, want success", m.statusKind)
			}
			if !hasMsg[buildLoadedMsg](collectMsgs(t, refresh)) || rec.loads != loadsBefore+1 {
				t.Errorf("the outcome did not ask for a fresh read of the build (loads %d -> %d)", loadsBefore, rec.loads)
			}
		})
	}
}

// TestBuildPauseOutcomes is every reply a seam can give, taken through the
// key and back through Update. The two "failed" rows are the load-bearing
// ones: the reason the seam gave is on screen, the notice is an error, and
// the build is STILL read again, because a failure to write the decision log
// comes after the flag was written, so "failed" does not mean "unchanged".
func TestBuildPauseOutcomes(t *testing.T) {
	cases := []struct {
		name     string
		key      tea.Msg
		set      func(*pauseSeams)
		wantKind notificationKind
		wantText string
	}{
		{"pause, fresh", keyPause, func(*pauseSeams) {}, notifySuccess,
			"Build paused. A task that is running finishes; nothing new starts. Press ctrl+] U to resume."},
		{"pause, already paused", keyPause, func(r *pauseSeams) { r.already = true }, notifyInfo,
			"The build was already paused. Press ctrl+] U to resume."},
		{"pause, seam error", keyPause, func(r *pauseSeams) { r.pauseErr = errors.New("permission denied") }, notifyError,
			"Pausing the build did not work: permission denied"},
		{"pause, seam error after the flag was written", keyPause,
			func(r *pauseSeams) {
				r.already, r.pauseErr = false, errors.New("paused, but the decision log was not written")
			},
			notifyError, "Pausing the build did not work: paused, but the decision log was not written"},
		{"resume, was paused", keyResume, func(r *pauseSeams) { r.resumed = ResumeResult{WasPaused: true} }, notifySuccess,
			"Build resumed. The daemon may start new tasks again."},
		{"resume, stop cleared", keyResume,
			func(r *pauseSeams) { r.resumed = ResumeResult{WasStopped: true, StopTask: "T1.2"} }, notifySuccess,
			"Cleared the stop after T1.2. The daemon carries on at its next pass."},
		{"resume, paused and stopped", keyResume,
			func(r *pauseSeams) { r.resumed = ResumeResult{WasPaused: true, WasStopped: true, StopTask: "T1.2"} },
			notifySuccess, "Build resumed, and the stop after T1.2 is cleared. The daemon carries on at its next pass."},
		{"resume, nothing to resume", keyResume, func(*pauseSeams) {}, notifyInfo,
			"The build was not paused; nothing changed."},
		{"resume, seam error", keyResume, func(r *pauseSeams) { r.resumeErr = errors.New("permission denied") }, notifyError,
			"Resuming the build did not work: permission denied"},
		{"resume, seam error with a result", keyResume,
			func(r *pauseSeams) {
				r.resumed, r.resumeErr = ResumeResult{WasPaused: true}, errors.New("resumed, but the decision log was not written")
			},
			notifyError, "Resuming the build did not work: resumed, but the decision log was not written"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := buildPauseModel(t, sess("alpha"))
			tc.set(rec)
			m = withBuild(t, m, buildPauseSnap())
			m, cmd := pressAction(t, m, tc.key)
			msgs := collectMsgs(t, cmd)
			if len(msgs) != 1 {
				t.Fatalf("the key produced %d messages, want one", len(msgs))
			}
			loadsBefore := rec.loads
			m, refresh := update(t, m, msgs[0])
			if m.status != tc.wantText {
				t.Errorf("notice = %q\nwant     %q", m.status, tc.wantText)
			}
			if m.statusKind != tc.wantKind {
				t.Errorf("notice kind = %v, want %v", m.statusKind, tc.wantKind)
			}
			if !hasMsg[buildLoadedMsg](collectMsgs(t, refresh)) || rec.loads != loadsBefore+1 {
				t.Errorf("the build was not read again (loads %d -> %d)", loadsBefore, rec.loads)
			}
		})
	}
}

// TestBuildPauseStaleResultIsDropped is an outcome that arrives after the
// owner switched project. Dropped whole: no notice, because it would describe
// a project the screen no longer shows, and no read, because the switch
// already asked for the new project's. The "same project" row is the control
// that shows the message itself is one Update acts on.
func TestBuildPauseStaleResultIsDropped(t *testing.T) {
	cases := []struct {
		name string
		msg  func(gen int) tea.Msg
	}{
		{"a pause", func(gen int) tea.Msg { return buildPausedMsg{gen: gen} }},
		{"a pause that failed", func(gen int) tea.Msg { return buildPausedMsg{err: errors.New("boom"), gen: gen} }},
		{"a resume", func(gen int) tea.Msg { return buildResumedMsg{res: ResumeResult{WasPaused: true}, gen: gen} }},
		{"a resume that failed", func(gen int) tea.Msg { return buildResumedMsg{err: errors.New("boom"), gen: gen} }},
	}
	for _, tc := range cases {
		t.Run(tc.name+" from the project before a switch", func(t *testing.T) {
			m, rec := buildPauseModel(t, sess("alpha"))
			m = withBuild(t, m, buildPauseSnap())
			old := m.projectGen
			m.projectGen++
			loadsBefore := rec.loads
			m, cmd := update(t, m, tc.msg(old))
			if cmd != nil {
				t.Errorf("a stale outcome issued a command (%T)", cmd())
			}
			if m.status != "" {
				t.Errorf("a stale outcome was reported: %q", m.status)
			}
			if rec.loads != loadsBefore {
				t.Errorf("a stale outcome read the build")
			}
		})
		t.Run(tc.name+" from the same project is acted on", func(t *testing.T) {
			m, _ := buildPauseModel(t, sess("alpha"))
			m = withBuild(t, m, buildPauseSnap())
			m, cmd := update(t, m, tc.msg(m.projectGen))
			if m.status == "" || cmd == nil {
				t.Errorf("status=%q cmd=%v, want a notice and a read", m.status, cmd != nil)
			}
		})
	}
}

// TestBuildPauseRefusals is the presses that must not reach a seam.
//
//   - "no build data yet" and "no master plan" are the plan's "there is no
//     build to pause": the seam is NOT called, because the snapshot's root is
//     the only folder the key may act on and there is none to trust yet.
//   - "nil seam" is a dashboard wired without the seams. The press must be
//     answered with a sentence; calling a nil function would take the whole
//     dashboard down, sessions' screens with it.
//
// None of them returns a command, and none moves the screen.
func TestBuildPauseRefusals(t *testing.T) {
	keys := []struct {
		name        string
		key         tea.Msg
		action      modeAction
		noBuildText string
	}{
		{"P", keyPause, actionPauseBuild, noBuildToPauseNotice},
		{"U", keyResume, actionResumeBuild, noBuildToResumeNotice},
	}
	for _, k := range keys {
		noBuild := []struct {
			name string
			prep func(t *testing.T, m Model) Model
		}{
			{"no build data yet", func(_ *testing.T, m Model) Model { return m }},
			{"no master plan", func(t *testing.T, m Model) Model {
				return withBuild(t, m, buildview.Snapshot{Root: "/main"})
			}},
		}
		for _, tc := range noBuild {
			t.Run(k.name+" with "+tc.name+" says there is no build", func(t *testing.T) {
				m, rec := buildPauseModel(t, sess("alpha"))
				m = tc.prep(t, m)
				focus, view := m.focus, m.view
				// Through the action, not the dialog: with no build the
				// dialog dims these rows and runs nothing, so the handler's
				// own refusal is reachable only by calling it, and it is the
				// guard that still has to answer in words.
				m, cmd := runAction(m, k.action)
				if cmd != nil {
					t.Fatalf("the key returned a command (%T)", cmd())
				}
				if len(rec.pauseRoots)+len(rec.resumeRoots) != 0 {
					t.Fatalf("a seam was called with no build: pause=%v resume=%v", rec.pauseRoots, rec.resumeRoots)
				}
				if m.status != k.noBuildText || m.statusKind != notifyInfo {
					t.Errorf("notice = %v %q, want info %q", m.statusKind, m.status, k.noBuildText)
				}
				if m.focus != focus || m.view != view {
					t.Errorf("the key moved the screen")
				}
			})
		}

		t.Run(k.name+" with a nil seam gives a notice and no panic", func(t *testing.T) {
			m := withBuild(t, testModel(sess("alpha")), buildPauseSnap())
			if m.deps.PauseBuild != nil || m.deps.ResumeBuild != nil {
				t.Fatal("precondition: testDeps must leave the two seams nil")
			}
			m, cmd := pressAction(t, m, k.key)
			if cmd != nil {
				t.Fatalf("the key returned a command (%T)", cmd())
			}
			if m.status != buildPauseUnavailableNotice || m.statusKind != notifyWarn {
				t.Errorf("notice = %v %q, want warn %q", m.statusKind, m.status, buildPauseUnavailableNotice)
			}
		})
	}
}

// TestBuildPauseKeysBelongToWhoeverHasTheKeyboard is the two places the keys
// must do nothing of their own. Both rows are load-bearing, and for the same
// reason: a capital P or U is ordinary text.
//
//   - "agent pane": the owner is typing to an agent. The letter must reach
//     the agent, as text, and the project's build must not be paused by a
//     sentence that happened to contain it.
//   - "inbox": on the item screen each is a letter of the answer; on the list
//     screen each is a key the overlay does not use. Neither reaches a seam.
func TestBuildPauseKeysBelongToWhoeverHasTheKeyboard(t *testing.T) {
	t.Run("agent pane focus types the letters to the agent", func(t *testing.T) {
		fp := newFakePane("X")
		m, rec := buildPauseModel(t, sess("a"))
		m = withBuild(t, m, buildPauseSnap())
		m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
		m, _ = update(t, m, keyEnter)
		if !m.paneOwnsKeyboard() {
			t.Fatal("fixture broken: the pane does not have the keyboard")
		}
		for _, k := range []tea.Msg{keyPause, keyResume} {
			var cmd tea.Cmd
			m, cmd = update(t, m, k)
			if cmd != nil {
				t.Fatalf("%v returned a command in agent focus", k)
			}
		}
		if len(rec.pauseRoots)+len(rec.resumeRoots) != 0 {
			t.Fatalf("a seam was called from agent focus: pause=%v resume=%v", rec.pauseRoots, rec.resumeRoots)
		}
		if got := strings.Join(fp.texts, ""); got != "PU" {
			t.Errorf("the agent was sent %q, want PU", got)
		}
		if strings.Contains(m.status, "paused") || strings.Contains(m.status, "resume") {
			t.Errorf("a typed letter raised a build notice: %q", m.status)
		}
	})

	t.Run("inbox list screen ignores the letters", func(t *testing.T) {
		m, rec := buildPauseModel(t, sess("alpha"))
		m = withBuild(t, m, inboxSnap())
		m, _ = pressAction(t, m, keyI)
		if !m.inboxOpen || m.inbox.screen != inboxScreenList {
			t.Fatal("fixture broken: the inbox list is not open")
		}
		for _, k := range []tea.Msg{keyPause, keyResume} {
			var cmd tea.Cmd
			m, cmd = update(t, m, k)
			if msgs := collectMsgs(t, cmd); hasMsg[buildPausedMsg](msgs) || hasMsg[buildResumedMsg](msgs) {
				t.Fatalf("%v acted on the build from the inbox list", k)
			}
		}
		if len(rec.pauseRoots)+len(rec.resumeRoots) != 0 {
			t.Fatalf("a seam was called from the inbox list: pause=%v resume=%v", rec.pauseRoots, rec.resumeRoots)
		}
		if !m.inboxOpen || m.inbox.screen != inboxScreenList {
			t.Errorf("the letters moved the overlay (open=%v screen=%v)", m.inboxOpen, m.inbox.screen)
		}
	})

	t.Run("inbox item screen takes the letters as the answer", func(t *testing.T) {
		m, rec := buildPauseModel(t, sess("alpha"))
		m = withBuild(t, m, inboxSnap())
		m = openInboxItem(t, m, 0)
		for _, k := range []tea.Msg{keyPause, keyResume} {
			var cmd tea.Cmd
			m, cmd = update(t, m, k)
			if msgs := collectMsgs(t, cmd); hasMsg[buildPausedMsg](msgs) || hasMsg[buildResumedMsg](msgs) {
				t.Fatalf("%v acted on the build from the answer field", k)
			}
		}
		if len(rec.pauseRoots)+len(rec.resumeRoots) != 0 {
			t.Fatalf("a seam was called from the answer field: pause=%v resume=%v", rec.pauseRoots, rec.resumeRoots)
		}
		if got := m.inbox.input.Value(); got != "PU" {
			t.Errorf("the answer field holds %q, want PU", got)
		}
	})
}

// TestBuildPauseKeysAreInTheLeaderTable is the proof that the actions dialog
// lists both actions and that their key runs them. Since T7.2 the dialog's
// rows come from the leader table, so nothing was added to it by hand; this
// fails if a table row or its help line is removed. The short footer line
// must NOT grow: it is at its measured-width cap. The last check is the one
// that makes the key safe to press: P and U are capitals, and the dialog's
// key decision must tell them from p (switch project) and u.
func TestBuildPauseKeysAreInTheLeaderTable(t *testing.T) {
	cases := []struct {
		desc   string
		label  string
		action modeAction
	}{
		{"pause the build", "P", actionPauseBuild},
		{"resume the build", "U", actionResumeBuild},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
			short := xansi.Strip(m.renderHelp())
			if strings.Contains(short, tc.desc) {
				t.Errorf("the short footer line grew: %q", short)
			}
			rows := leaderRows(m.leaderConditions())
			row, ok := leaderRowFor(rows, tc.action)
			if !ok {
				t.Fatalf("the dialog does not list %v", tc.action)
			}
			if row.name != tc.desc || row.keyLabel != tc.label {
				t.Errorf("row = %q under %q, want %q under %q", row.name, row.keyLabel, tc.desc, tc.label)
			}
			if row.blocked != "" {
				t.Errorf("the row is dimmed (%q) on a project with a build", row.blocked)
			}
			got := leaderKeyDecision(leaderPress(tc.label), defaultLeaderKey, "", rows, 0, false)
			if got.kind != leaderRun || got.row.action != tc.action {
				t.Errorf("ctrl+] %s = %v %v, want it to run %v", tc.label, got.kind, got.row.action, tc.action)
			}
		})
	}
}

// TestBuildPauseKeysSitAfterTheInboxKey pins where the two actions are in the
// leader table, which is the dialog's order. They go straight after the
// inbox so the three build actions read together, as the decided key table
// lists them.
func TestBuildPauseKeysSitAfterTheInboxKey(t *testing.T) {
	var order []string
	for _, e := range leaderTable {
		order = append(order, e.key)
	}
	at := -1
	for i, k := range order {
		if k == "i" {
			at = i
		}
	}
	if at < 0 || at+2 >= len(order) {
		t.Fatalf("no room after the inbox key in %q", order)
	}
	if order[at+1] != "P" || order[at+2] != "U" {
		t.Errorf("after i come %q and %q, want P and U (order %q)", order[at+1], order[at+2], order)
	}
}
