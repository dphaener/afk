package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/project"
)

// allProjectsModel is a dashboard on /repo whose switcher knows /repo and
// /p/alpha, with a recording LoadAllProjects returning the fixture and the
// pause seams recording into rec. The screen is closed.
func allProjectsModel(t *testing.T) (Model, *allProjectsSeam, *pauseSeams) {
	t.Helper()
	m, rec := buildPauseModel(t, sess("a"))
	m.projects = []project.Project{
		{Path: "/repo", Name: "repo", Pinned: true},
		{Path: "/p/alpha", Name: "alpha"},
		{Path: "/p/beta", Name: "beta"},
	}
	m.activeProject = 0
	seam := &allProjectsSeam{res: AllProjects{Rows: allProjectsFixture(), ConfigPath: "/cfg/config.toml"}}
	m.deps.LoadAllProjects = seam.load
	return m, seam, rec
}

// openAllProjects opens the screen through the leader and lands its first
// read.
func openAllProjects(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := pressAction(t, m, keyEscMsg())
	if !m.allProjectsOpen {
		t.Fatal("setup: esc did not open the all-projects screen")
	}
	for _, msg := range collectMsgs(t, cmd) {
		m, _ = update(t, m, msg)
	}
	if !m.allProjectsKnown {
		t.Fatal("setup: the first read did not land")
	}
	return m
}

// cursorOn moves the screen's cursor to the row for root, failing if there
// is none.
func cursorOn(t *testing.T, m Model, root string) Model {
	t.Helper()
	for i, r := range m.allProjectsRows {
		if r.Root == root {
			m.allProjectsCursor = i
			return m
		}
	}
	t.Fatalf("setup: no row for %s", root)
	return m
}

// TestAllProjectsOpenAndClose drives the screen's ways in and out since
// T16.2, where it is the All projects level: esc from a project's session
// list opens it, and enter on a row leaves it. Load-bearing rows: "opening
// reads once and sorts" (the screen is read when it opens, and the rows
// needing the owner come first), "esc stays" (All projects is the top level,
// so esc there must not drop the owner into a project they did not pick),
// "enter on the active project goes back to it", and "unwired says so", which
// must open nothing: an empty screen would look like a machine with no
// projects.
func TestAllProjectsOpenAndClose(t *testing.T) {
	t.Run("opening reads once and sorts", func(t *testing.T) {
		m, seam, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		if seam.calls != 1 {
			t.Fatalf("the reader ran %d times on open, want 1", seam.calls)
		}
		if mode := activeInputMode(m.modeSnapshot()); mode != inputAllProjects {
			t.Fatalf("the keyboard is with %v, want the screen", mode)
		}
		seenQuiet := false
		for _, r := range m.allProjectsRows {
			if !r.NeedsOwner {
				seenQuiet = true
			} else if seenQuiet {
				t.Fatalf("%s needs the owner but comes after a row that does not", r.Name)
			}
		}
		if m.allProjectsRows[0].Root != "/p/beta" {
			t.Fatalf("first row %s, want beta (needs the owner, first in config order)", m.allProjectsRows[0].Name)
		}
	})
	t.Run("esc stays", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		m, _ = update(t, m, keyEsc)
		if !m.allProjectsOpen {
			t.Fatal("esc at All projects closed the screen")
		}
	})
	t.Run("enter on the active project goes back to it", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = cursorOn(t, openAllProjects(t, m), "/repo")
		m, _ = update(t, m, keyEnter)
		if m.allProjectsOpen {
			t.Fatal("enter on the active project left the screen open")
		}
		if mode := activeInputMode(m.modeSnapshot()); mode != inputNormal {
			t.Fatalf("after enter the keyboard is with %v, want the list", mode)
		}
	})
	t.Run("unwired says so", func(t *testing.T) {
		m := testModel(sess("a"))
		m, cmd := update(t, m, keyEsc)
		if m.allProjectsOpen || cmd != nil {
			t.Fatalf("open %v, cmd %v; want nothing opened and nothing run", m.allProjectsOpen, cmd != nil)
		}
		if m.status != allProjectsUnwiredNotice {
			t.Fatalf("status = %q, want the unwired notice", m.status)
		}
	})
	t.Run("the screen replaces the list and the detail side", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		frame := render(m)
		if !strings.Contains(frame, allProjectsTitle) {
			t.Fatal("the screen's box is not on screen")
		}
		if strings.Contains(frame, "Sessions") || strings.Contains(frame, "NAME") {
			t.Fatalf("the session list is still drawn:\n%s", frame)
		}
	})
}

// TestAllProjectsMovement pins the movement keys: each moves, and both ends
// clamp rather than wrap. The clamp rows are load-bearing: the top rows are
// the ones that need the owner, and a list that wrapped would carry a
// held-down up-arrow off them.
func TestAllProjectsMovement(t *testing.T) {
	n := len(allProjectsFixture())
	cases := []struct {
		name  string
		start int
		keys  []tea.Msg
		want  int
	}{
		{"down moves", 0, []tea.Msg{keyDown}, 1},
		{"j moves", 0, []tea.Msg{keyRune('j')}, 1},
		{"up moves", 2, []tea.Msg{keyUp}, 1},
		{"k moves", 2, []tea.Msg{keyRune('k')}, 1},
		{"up clamps at the top", 0, []tea.Msg{keyUp}, 0},
		{"down clamps at the bottom", n - 1, []tea.Msg{keyDown}, n - 1},
		{"end goes to the last", 0, []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnd}}, n - 1},
		{"home goes to the first", 4, []tea.Msg{tea.KeyPressMsg{Code: tea.KeyHome}}, 0},
		{"pgdown clamps", 0, []tea.Msg{tea.KeyPressMsg{Code: tea.KeyPgDown}, tea.KeyPressMsg{Code: tea.KeyPgDown}}, n - 1},
		{"pgup clamps", n - 1, []tea.Msg{tea.KeyPressMsg{Code: tea.KeyPgUp}, tea.KeyPressMsg{Code: tea.KeyPgUp}}, 0},
		{"a letter does nothing", 3, []tea.Msg{keyRune('x')}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := allProjectsModel(t)
			m = openAllProjects(t, m)
			m.allProjectsCursor = tc.start
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			if m.allProjectsCursor != tc.want {
				t.Fatalf("cursor = %d, want %d", m.allProjectsCursor, tc.want)
			}
			if !m.allProjectsOpen {
				t.Fatal("a movement key closed the screen")
			}
		})
	}
}

// TestAllProjectsEnter drives enter on each kind of row. Load-bearing rows:
// "switches" (the active repo changes, the screen closes, and the switch is
// the switcher's own, preview arm and open count included), "gone" and
// "unreadable" (no switch, a notice saying why), and "not in the switcher
// yet" (the switcher's entry is what a switch needs; the screen must not
// invent one).
func TestAllProjectsEnter(t *testing.T) {
	cases := []struct {
		name       string
		root       string
		wantRepo   string
		wantOpen   bool
		wantStatus string
	}{
		{name: "switches", root: "/p/alpha", wantRepo: "/p/alpha"},
		{name: "the active project only closes", root: "/repo", wantRepo: "/repo"},
		{name: "gone does not switch", root: "/p/gamma", wantRepo: "/repo", wantOpen: true,
			wantStatus: "The folder of gamma is gone. Fix its path in config.toml or remove it with afk project remove."},
		{name: "unreadable does not switch", root: "/p/delta", wantRepo: "/repo", wantOpen: true,
			wantStatus: "The files of delta could not be read"},
		{name: "not in the switcher yet does not switch", root: "/p/zeta", wantRepo: "/repo", wantOpen: true,
			wantStatus: allProjectsNotListedNotice("zeta")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := allProjectsModel(t)
			m = cursorOn(t, openAllProjects(t, m), tc.root)
			m, _ = update(t, m, keyEnter)
			if m.repo != tc.wantRepo {
				t.Fatalf("repo = %q, want %q", m.repo, tc.wantRepo)
			}
			if m.allProjectsOpen != tc.wantOpen {
				t.Fatalf("screen open = %v, want %v", m.allProjectsOpen, tc.wantOpen)
			}
			if tc.wantStatus != "" && !strings.HasPrefix(m.status, tc.wantStatus) {
				t.Fatalf("status = %q, want it to start %q", m.status, tc.wantStatus)
			}
			if tc.wantRepo != "/repo" {
				if !m.previewOnNextLoad {
					t.Error("the switch did not arm the preview, as the switcher's does")
				}
				if m.projectOpens[tc.wantRepo] != 1 {
					t.Errorf("open count for %s = %d, want 1", tc.wantRepo, m.projectOpens[tc.wantRepo])
				}
			}
		})
	}
}

// TestAllProjectsPauseResume pins that pause and resume on the screen act on
// the selected row's project, call the seam once, word the result with the
// project's name, and read the rows and the Build tab again. The root check
// is the load-bearing one: the active project is /repo (its build is /main),
// so a pause that went to the active project's build would record /main.
// The refusal rows keep the seam uncalled.
func TestAllProjectsPauseResume(t *testing.T) {
	// Driven through the handlers: `p` at All projects is T16.3's (A4), so
	// until then the row's pause and resume have no key of their own, and
	// what is pinned here is what that key will run.
	pause := func(m Model) (tea.Model, tea.Cmd) { return m.pauseSelectedProject() }
	resume := func(m Model) (tea.Model, tea.Cmd) { return m.resumeSelectedProject() }
	cases := []struct {
		name       string
		root       string
		run        func(Model) (tea.Model, tea.Cmd)
		wantRoots  []string
		wantStatus string
	}{
		{name: "pause acts on the selected row", root: "/p/alpha", run: pause,
			wantRoots: []string{"/p/alpha"}, wantStatus: "alpha: Build paused."},
		{name: "resume acts on the selected row", root: "/p/beta", run: resume,
			wantRoots: []string{"/p/beta"}, wantStatus: "beta: Build resumed."},
		{name: "pause refuses a gone row", root: "/p/gamma", run: pause,
			wantStatus: allProjectsGoneNotice("gamma")},
		{name: "resume refuses a row with no plan", root: "/p/epsilon", run: resume,
			wantStatus: "epsilon: " + noBuildToResumeNotice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, seam, rec := allProjectsModel(t)
			rec.resumed = ResumeResult{WasPaused: true}
			m = withBuild(t, m, buildPauseSnap())
			m = cursorOn(t, openAllProjects(t, m), tc.root)
			readsBefore, loadsBefore := seam.calls, rec.loads

			next, cmd := tc.run(m)
			m = next.(Model)
			for _, msg := range collectMsgs(t, cmd) {
				var next tea.Cmd
				m, next = update(t, m, msg)
				for _, again := range collectMsgs(t, next) {
					m, _ = update(t, m, again)
				}
			}
			got := append(append([]string(nil), rec.pauseRoots...), rec.resumeRoots...)
			if strings.Join(got, ",") != strings.Join(tc.wantRoots, ",") {
				t.Fatalf("seam called with %v, want %v", got, tc.wantRoots)
			}
			if !strings.HasPrefix(m.status, tc.wantStatus) {
				t.Fatalf("status = %q, want it to start %q", m.status, tc.wantStatus)
			}
			if len(tc.wantRoots) > 0 {
				if seam.calls != readsBefore+1 {
					t.Errorf("the rows were read %d times after the result, want once", seam.calls-readsBefore)
				}
				if rec.loads != loadsBefore+1 {
					t.Errorf("the Build tab was read %d times after the result, want once", rec.loads-loadsBefore)
				}
			}
			if !m.allProjectsOpen {
				t.Error("pausing from the screen closed it")
			}
		})
	}
}

// TestAllProjectsPauseClosedUnchanged pins that with the screen closed the
// pause key acts on the active project's build exactly as before (T6.3).
func TestAllProjectsPauseClosedUnchanged(t *testing.T) {
	m, _, rec := allProjectsModel(t)
	m = withBuild(t, m, buildPauseSnap())
	_, cmd := pressAction(t, m, keyPause)
	if !hasMsg[buildPausedMsg](collectMsgs(t, cmd)) {
		t.Fatal("with the screen closed the pause key did not run T6.3's pause")
	}
	if strings.Join(rec.pauseRoots, ",") != "/main" {
		t.Fatalf("paused %v, want the active project's /main", rec.pauseRoots)
	}
}

// TestAllProjectsReads pins when the seam is called. Load-bearing: "a tick
// while closed reads nothing" (the screen costs nothing when it is not up),
// "a tick while open reads", and the two dropped rows — a read that lands
// after the screen closed, or for an earlier opening, must not fill rows.
func TestAllProjectsReads(t *testing.T) {
	tick := tickMsg(time.Unix(2_000_000, 0))
	t.Run("a tick while closed reads nothing", func(t *testing.T) {
		m, seam, _ := allProjectsModel(t)
		_, cmd := update(t, m, tick)
		if hasMsg[allProjectsLoadedMsg](collectMsgs(t, cmd)) || seam.calls != 0 {
			t.Fatalf("a tick with the screen closed read the projects (%d calls)", seam.calls)
		}
	})
	t.Run("a tick while open reads", func(t *testing.T) {
		m, seam, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		_, cmd := update(t, m, tick)
		if !hasMsg[allProjectsLoadedMsg](collectMsgs(t, cmd)) || seam.calls != 2 {
			t.Fatalf("a tick with the screen open did not read the projects (%d calls)", seam.calls)
		}
	})
	t.Run("a read after closing is dropped", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		gen := m.allProjectsGen
		m = closeAllProjects(t, m)
		m, _ = update(t, m, allProjectsLoadedMsg{gen: gen, res: AllProjects{Rows: allProjectsFixture()[:1]}})
		if len(m.allProjectsRows) == 1 {
			t.Fatal("a read that landed after the screen closed replaced the rows")
		}
	})
	t.Run("a read from an earlier opening is dropped", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		old := m.allProjectsGen
		m = closeAllProjects(t, m)
		m = openAllProjects(t, m)
		m, _ = update(t, m, allProjectsLoadedMsg{gen: old, res: AllProjects{Rows: allProjectsFixture()[:1]}})
		if len(m.allProjectsRows) == 1 {
			t.Fatal("a read from an earlier opening replaced the rows")
		}
	})
	t.Run("the cursor stays on its project", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = cursorOn(t, openAllProjects(t, m), "/p/alpha")
		rows := allProjectsFixture()
		// alpha now needs the owner, so the sort moves it up.
		rows[0].NeedsOwner = true
		m, _ = update(t, m, allProjectsLoadedMsg{gen: m.allProjectsGen, res: AllProjects{Rows: rows}})
		if r, _ := m.selectedProjectRow(); r.Root != "/p/alpha" {
			t.Fatalf("the cursor is on %s, want alpha", r.Root)
		}
	})
	t.Run("an error keeps the rows", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		n := len(m.allProjectsRows)
		m, _ = update(t, m, allProjectsLoadedMsg{gen: m.allProjectsGen, res: AllProjects{Err: errors.New("bad toml")}})
		if len(m.allProjectsRows) != n || m.allProjectsErr != "bad toml" {
			t.Fatalf("rows %d (want %d), err %q", len(m.allProjectsRows), n, m.allProjectsErr)
		}
	})
	t.Run("a project switch closes the screen", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		m, _ = m.switchProject("/p/beta")
		if m.allProjectsOpen {
			t.Fatal("a switch from elsewhere left the screen open")
		}
	})
}
