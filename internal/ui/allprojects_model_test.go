package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/projectrows"
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
	// Driven through the handlers `p` runs (TestAllProjectsPauseKey pins the
	// key itself).
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

// TestOldestBlockingRow pins which project `a` goes to. Load-bearing rows:
// "the older wins" (not the first row, not the one with more items) and "a tie
// goes to config order" (rows arrive in screen order, so the first of two equal
// stamps is the one listed first).
func TestOldestBlockingRow(t *testing.T) {
	w := func(id, at string) projectrows.Wait { return projectrows.Wait{ItemID: id, CreatedAt: at} }
	cases := []struct {
		name string
		rows []projectrows.Row
		want string
		ok   bool
	}{
		{name: "no rows"},
		{name: "rows with no wait", rows: []projectrows.Row{{Name: "a"}, {Name: "b"}}},
		{name: "one wait", rows: []projectrows.Row{{Name: "a"}, {Name: "b", Wait: w("x", "2026-10-20T09:00:00Z")}}, want: "b", ok: true},
		{name: "the older wins", rows: []projectrows.Row{
			{Name: "a", Wait: w("x", "2026-10-20T09:00:00Z")},
			{Name: "b", Wait: w("y", "2026-10-19T09:00:00Z")},
		}, want: "b", ok: true},
		{name: "a tie goes to config order", rows: []projectrows.Row{
			{Name: "a", Wait: w("x", "2026-10-20T09:00:00Z")},
			{Name: "b", Wait: w("y", "2026-10-20T09:00:00Z")},
		}, want: "a", ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := oldestBlockingRow(tc.rows)
			if ok != tc.ok || r.Name != tc.want {
				t.Fatalf("oldestBlockingRow = %q, %v; want %q, %v", r.Name, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestAllProjectsPauseKey pins `p` at All projects, pressed as a key. The
// state of the selected row decides, never the active project's snapshot
// (which here is not paused): a paused row and a stopped row resume, a running
// row pauses, and a gone row is refused in the words enter and the pause
// handlers use, with the seams untouched. The first two rows are the
// load-bearing ones, since the active project's flags would pause them.
func TestAllProjectsPauseKey(t *testing.T) {
	cases := []struct {
		name       string
		root       string
		state      projectrows.State
		wantPause  []string
		wantResume []string
		wantStatus string
	}{
		{name: "paused resumes", root: "/p/beta", wantResume: []string{"/p/beta"}, wantStatus: "beta: Build resumed."},
		{name: "stopped resumes", root: "/p/alpha", state: projectrows.StateStopped, wantResume: []string{"/p/alpha"}, wantStatus: "alpha: Build resumed."},
		{name: "running pauses", root: "/p/alpha", wantPause: []string{"/p/alpha"}, wantStatus: "alpha: Build paused."},
		{name: "gone is refused", root: "/p/gamma", wantStatus: allProjectsGoneNotice("gamma")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, rec := allProjectsModel(t)
			rec.resumed = ResumeResult{WasPaused: true}
			m = withBuild(t, m, buildPauseSnap())
			m = cursorOn(t, openAllProjects(t, m), tc.root)
			if tc.state != "" {
				m.allProjectsRows[m.allProjectsCursor].State = tc.state
			}
			m, cmd := pressAction(t, m, keyPause)
			for _, msg := range collectMsgs(t, cmd) {
				var next tea.Cmd
				m, next = update(t, m, msg)
				for _, again := range collectMsgs(t, next) {
					m, _ = update(t, m, again)
				}
			}
			if strings.Join(rec.pauseRoots, ",") != strings.Join(tc.wantPause, ",") {
				t.Errorf("paused %v, want %v", rec.pauseRoots, tc.wantPause)
			}
			if strings.Join(rec.resumeRoots, ",") != strings.Join(tc.wantResume, ",") {
				t.Errorf("resumed %v, want %v", rec.resumeRoots, tc.wantResume)
			}
			if !strings.HasPrefix(m.status, tc.wantStatus) {
				t.Errorf("status = %q, want it to start %q", m.status, tc.wantStatus)
			}
		})
	}
}

// TestAllProjectsAnswerKey pins `a`. Load-bearing rows: "switches and opens
// the inbox on the item" (beta holds the fixture's wait; the overlay is on
// its item screen only once that project's snapshot lands, and not before),
// "the active project closes the screen and opens at once", and "nothing
// blocking says so and switches nothing" (the screen stays up, the project
// does not change).
func TestAllProjectsAnswerKey(t *testing.T) {
	snap := buildview.Snapshot{Root: "/p/beta", HasBuild: true, Items: []inbox.Item{
		{ID: "aaaaa2", CreatedAt: "2026-10-18T09:00:00Z", Kind: "notice", Title: "fyi"},
		{ID: "bbbbb3", CreatedAt: "2026-10-20T09:00:00Z", Kind: "question", Blocking: true, Title: "Which database should the cache use?"},
	}}
	t.Run("switches and opens the inbox on the item", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		m, _ = pressAction(t, m, keyRune('a'))
		if m.repo != "/p/beta" || m.allProjectsOpen {
			t.Fatalf("repo = %q, screen open = %v; want /p/beta with the screen closed", m.repo, m.allProjectsOpen)
		}
		if m.inboxOpen {
			t.Fatal("the inbox opened before the project's snapshot landed")
		}
		m = withBuild(t, m, snap)
		if !m.inboxOpen || m.inbox.screen != inboxScreenItem || m.inbox.item.ID != "bbbbb3" {
			t.Fatalf("inbox open = %v, screen = %v, item = %q; want it open on bbbbb3", m.inboxOpen, m.inbox.screen, m.inbox.item.ID)
		}
		if m.inboxFollow != "" {
			t.Error("the follow was not cleared once used")
		}
	})
	t.Run("the active project closes the screen and opens at once", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		snap := snap
		snap.Root = "/main"
		m = withBuild(t, m, snap)
		m = openAllProjects(t, m)
		for i := range m.allProjectsRows {
			m.allProjectsRows[i].Wait = projectrows.Wait{}
		}
		m.allProjectsRows[0].Root = "/repo"
		m.allProjectsRows[0].Wait = projectrows.Wait{ItemID: "bbbbb3", CreatedAt: "2026-10-20T09:00:00Z"}
		m, _ = pressAction(t, m, keyRune('a'))
		if m.repo != "/repo" || m.allProjectsOpen || !m.inboxOpen || m.inbox.item.ID != "bbbbb3" {
			t.Fatalf("repo = %q, screen open = %v, inbox open = %v, item = %q", m.repo, m.allProjectsOpen, m.inboxOpen, m.inbox.item.ID)
		}
	})
	t.Run("nothing blocking says so and switches nothing", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		for i := range m.allProjectsRows {
			m.allProjectsRows[i].Wait = projectrows.Wait{}
		}
		m, _ = pressAction(t, m, keyRune('a'))
		if m.status != nothingBlockingNotice {
			t.Fatalf("status = %q, want %q", m.status, nothingBlockingNotice)
		}
		if m.repo != "/repo" || !m.allProjectsOpen || m.inboxOpen {
			t.Fatalf("repo = %q, screen open = %v, inbox open = %v; want nothing changed", m.repo, m.allProjectsOpen, m.inboxOpen)
		}
	})
}

// TestAllProjectsEveryProjectCache pins the cache that holds every project's
// row and task list at every level (T16.6). Load-bearing rows: "a scan tick
// at Project level fills it" (the whole point: the screen is closed), "a read
// landing after the screen closed" (fills the cache but the screen's rows stay
// as they were, so T16.3's gen rule is unchanged), "a read in flight blocks a
// second" (reads never pile up behind a slow disk), "an older read landing
// late" (must not roll the cache back), "an error keeps the cache", and "nil
// seam" (no reader means no cache and no panic).
func TestAllProjectsEveryProjectCache(t *testing.T) {
	scan := scanTickMsg(time.Unix(2_000_000, 0))
	tasks := map[string][]JumpTask{"/p/alpha": {{ID: "T1.1", Title: "First", Status: "pending"}}}

	t.Run("a scan tick at Project level fills it", func(t *testing.T) {
		m, seam, _ := allProjectsModel(t)
		seam.res.Tasks = tasks
		if m.allProjectsOpen {
			t.Fatal("setup: the screen is open")
		}
		m, cmd := update(t, m, scan)
		var loaded []allProjectsLoadedMsg
		for _, msg := range collectMsgs(t, cmd) {
			if lm, ok := msg.(allProjectsLoadedMsg); ok {
				loaded = append(loaded, lm)
				m, _ = update(t, m, msg)
			}
		}
		if len(loaded) != 1 || seam.calls != 1 {
			t.Fatalf("reads = %d, calls = %d, want 1 each", len(loaded), seam.calls)
		}
		if len(m.everyProject.Rows) != len(allProjectsFixture()) || len(m.everyProject.Tasks["/p/alpha"]) != 1 {
			t.Fatalf("cache = %+v, want the fixture rows and alpha's task", m.everyProject)
		}
		if m.everyProjectAt.IsZero() {
			t.Error("the cache has no read time")
		}
		if len(m.allProjectsRows) != 0 || m.allProjectsKnown {
			t.Error("a read with the screen closed filled the screen's rows")
		}
	})
	t.Run("a read landing after the screen closed fills only the cache", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m = openAllProjects(t, m)
		rows := len(m.allProjectsRows)
		gen := m.allProjectsGen
		m = closeAllProjects(t, m)
		m, _ = update(t, m, allProjectsLoadedMsg{gen: gen, seq: 9, res: AllProjects{Rows: allProjectsFixture()[:1]}})
		if len(m.everyProject.Rows) != 1 {
			t.Fatalf("cache rows = %d, want 1", len(m.everyProject.Rows))
		}
		if len(m.allProjectsRows) != rows {
			t.Fatalf("screen rows = %d, want %d unchanged", len(m.allProjectsRows), rows)
		}
	})
	t.Run("a read in flight blocks a second", func(t *testing.T) {
		m, seam, _ := allProjectsModel(t)
		m.allProjectsRefreshing.Store(true)
		_, cmd := update(t, m, scan)
		if hasMsg[allProjectsLoadedMsg](collectMsgs(t, cmd)) || seam.calls != 0 {
			t.Fatalf("a second read started while one was in flight (%d calls)", seam.calls)
		}
	})
	t.Run("an older read landing late is ignored", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m, _ = update(t, m, allProjectsLoadedMsg{seq: 5, res: AllProjects{Rows: allProjectsFixture()}})
		m, _ = update(t, m, allProjectsLoadedMsg{seq: 4, res: AllProjects{Rows: allProjectsFixture()[:1]}})
		if len(m.everyProject.Rows) != len(allProjectsFixture()) {
			t.Fatalf("cache rows = %d, an older read replaced the newer", len(m.everyProject.Rows))
		}
	})
	t.Run("an error keeps the cache", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m, _ = update(t, m, allProjectsLoadedMsg{seq: 1, res: AllProjects{Rows: allProjectsFixture()}})
		m, _ = update(t, m, allProjectsLoadedMsg{seq: 2, res: AllProjects{Err: errors.New("bad toml")}})
		if len(m.everyProject.Rows) != len(allProjectsFixture()) {
			t.Fatal("a failed read emptied the cache")
		}
	})
	t.Run("nil seam is harmless", func(t *testing.T) {
		m, _, _ := allProjectsModel(t)
		m.deps.LoadAllProjects = nil
		m, cmd := update(t, m, scan)
		if hasMsg[allProjectsLoadedMsg](collectMsgs(t, cmd)) || len(m.everyProject.Rows) != 0 {
			t.Fatal("a Model with no reader read the projects")
		}
		if m.Init() == nil {
			t.Fatal("Init returned no Cmd")
		}
	})
	t.Run("start-up reads once", func(t *testing.T) {
		m, seam, _ := allProjectsModel(t)
		for _, msg := range collectMsgs(t, m.Init()) {
			m, _ = update(t, m, msg)
		}
		if seam.calls != 1 || len(m.everyProject.Rows) == 0 {
			t.Fatalf("calls = %d, cache rows = %d, want one read and a filled cache", seam.calls, len(m.everyProject.Rows))
		}
	})
}
