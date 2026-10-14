package ui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
)

// --- helpers ---

// listedConfigPath is the settings file every fake loader here claims to have
// read. No test opens it: the loader is the injected seam.
const listedConfigPath = "/cfg/afk/config.toml"

// listedSeam wires Deps.LoadListed to answer with entries, and counts its
// calls so a test can tell a command that ran from one that was never issued.
func listedSeam(d Deps, entries ...ListedProject) (Deps, *listedCalls) {
	calls := &listedCalls{}
	d.LoadListed = func() ([]ListedProject, string, error) {
		calls.add()
		return entries, listedConfigPath, nil
	}
	return d, calls
}

type listedCalls struct {
	mu sync.Mutex
	n  int
}

func (c *listedCalls) add() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *listedCalls) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// noSearchFolders wires the two scan seams the way a machine whose config.toml
// has no [dash] search folders behaves: the read succeeds, names no folder,
// and the walk finds nothing.
func noSearchFolders(d Deps) Deps {
	d.LoadConfig = func() (SearchSettings, string, error) {
		return SearchSettings{Interval: time.Minute, MaxDepth: 3}, "/cfg/afk/config.toml", nil
	}
	d.Scan = func([]string, int) []project.Project { return nil }
	return d
}

func projectNames(projects []project.Project) []string {
	names := make([]string, len(projects))
	for i, p := range projects {
		names[i] = p.Name
	}
	return names
}

// applyMsgsOfScanAndListed feeds the project-list and scan messages in msgs to
// the model, listed reads first, and returns the commands they produced. The
// order is fixed here because a batch's members finish in any order, and the
// tests using this assert the end state either order must reach.
func applyMsgsOfScanAndListed(t *testing.T, m Model, msgs []tea.Msg) (Model, []tea.Cmd) {
	t.Helper()
	var cmds []tea.Cmd
	for _, msg := range msgs {
		if _, ok := msg.(listedProjectsMsg); ok {
			var cmd tea.Cmd
			m, cmd = update(t, m, msg)
			cmds = append(cmds, cmd)
		}
	}
	for _, msg := range msgs {
		if _, ok := msg.(projectScanMsg); ok {
			var cmd tea.Cmd
			m, cmd = update(t, m, msg)
			cmds = append(cmds, cmd)
		}
	}
	return m, cmds
}

// --- the pure merge ---

// TestMergeProjects is the table for the switcher's list: mergeProjects over
// the start-up row, the listed set and the found set, with every listed set
// first passed through foldListedRead the way the model passes it.
//
// The load-bearing rows, and what each would let through if it were missing:
//
//   - "listed only" is the headline: a machine with no search folders and no
//     start-up project must still show every project `afk project list` prints.
//   - "both listed and found" and "start-up project listed" pin one row per
//     path. A second row for one project is two cursors onto one set of
//     sessions, and Enter on the wrong one detaches the open pane.
//   - "started in a worktree of a listed project" pins the stand-in: the row
//     keeps the worktree's PATH (the active project is found by path, so
//     swapping it would move the owner off the folder they opened) and takes
//     the listed name, and the main checkout gets no row even when the search
//     finds it.
//   - "a listed folder that is gone" must produce no row and no notice.
//   - "an unreadable file" pins that a failed read keeps the HELD set: a merge
//     that took the failed read's empty entries would empty the switcher over
//     a typo. Its notice has to name the file, since that is what to fix.
//   - "a good read after an unreadable one" pins the other half: keeping the
//     held set must not turn into never dropping anything.
//   - "a found project the search no longer finds" pins that listing did not
//     change today's rule for rows nobody named.
//   - "a listed name beside a found project with the same folder name" pins
//     that the listed row shows the listed name while the found row keeps the
//     widened label that tells the two apart.
//
// Every row is merged twice and the two results compared, and "mixed sources"
// exists for that check alone: sameProjectPaths compares by position, so an
// order that moved between calls would rebuild an open switcher on every read
// and reset the owner's cursor.
func TestMergeProjects(t *testing.T) {
	type read struct {
		entries []ListedProject
		path    string
		err     error
	}
	good := func(entries ...ListedProject) read {
		return read{entries: entries, path: listedConfigPath}
	}
	tests := []struct {
		name         string
		held         []project.Project
		mainCheckout string
		found        []project.Project
		heldListed   []ListedProject
		reads        []read
		wantPaths    []string
		wantNames    []string
		wantPinned   []string
		// wantNotice lists what the LAST read's notice must contain; nil means
		// the last read must raise no notice.
		wantNotice []string
	}{
		{
			name: "listed only",
			reads: []read{good(
				ListedProject{Path: "/code/afk", Name: "afk"},
				ListedProject{Path: "/code/shop", Name: "shop"},
			)},
			wantPaths: []string{"/code/afk", "/code/shop"},
			wantNames: []string{"afk", "shop"},
		},
		{
			name:      "found only",
			found:     scannedProjects("/roots/a", "/roots/b"),
			reads:     []read{good()},
			wantPaths: []string{"/roots/a", "/roots/b"},
			wantNames: []string{"a", "b"},
		},
		{
			name:  "both listed and found is one row",
			found: scannedProjects("/roots/a", "/roots/b"),
			reads: []read{good(
				ListedProject{Path: "/roots/b", Name: "bee"},
				ListedProject{Path: "/code/c", Name: "c"},
			)},
			wantPaths: []string{"/roots/a", "/roots/b", "/code/c"},
			wantNames: []string{"a", "bee", "c"},
		},
		{
			name:       "the start-up project not listed",
			held:       []project.Project{pinnedProject("/pin")},
			reads:      []read{good(ListedProject{Path: "/code/afk", Name: "afk"})},
			wantPaths:  []string{"/code/afk", "/pin"},
			wantNames:  []string{"afk", "pin"},
			wantPinned: []string{"/pin"},
		},
		{
			name:       "the start-up project listed is one pinned row with the listed name",
			held:       []project.Project{pinnedProject("/code/afk")},
			reads:      []read{good(ListedProject{Path: "/code/afk", Name: "robot"})},
			wantPaths:  []string{"/code/afk"},
			wantNames:  []string{"robot"},
			wantPinned: []string{"/code/afk"},
		},
		{
			name:         "started in a worktree of a listed project",
			held:         []project.Project{pinnedProject("/code/afk/.wt/feature")},
			mainCheckout: "/code/afk",
			reads:        []read{good(ListedProject{Path: "/code/afk", Name: "robot"})},
			wantPaths:    []string{"/code/afk/.wt/feature"},
			wantNames:    []string{"robot"},
			wantPinned:   []string{"/code/afk/.wt/feature"},
		},
		{
			name:         "started in a worktree of a listed project the search also finds",
			held:         []project.Project{pinnedProject("/code/afk/.wt/feature")},
			mainCheckout: "/code/afk",
			found:        scannedProjects("/code/afk", "/code/shop"),
			reads:        []read{good(ListedProject{Path: "/code/afk", Name: "robot"})},
			wantPaths:    []string{"/code/shop", "/code/afk/.wt/feature"},
			wantNames:    []string{"shop", "robot"},
			wantPinned:   []string{"/code/afk/.wt/feature"},
		},
		{
			name:         "started in a worktree of a project that is not listed keeps today's rows",
			held:         []project.Project{pinnedProject("/code/afk/.wt/feature")},
			mainCheckout: "/code/afk",
			found:        scannedProjects("/code/afk"),
			reads:        []read{good(ListedProject{Path: "/code/shop", Name: "shop"})},
			wantPaths:    []string{"/code/afk", "/code/shop", "/code/afk/.wt/feature"},
			wantNames:    []string{"afk", "shop", "feature"},
			wantPinned:   []string{"/code/afk/.wt/feature"},
		},
		{
			name: "a listed folder that is gone",
			reads: []read{good(
				ListedProject{Path: "/code/afk", Name: "afk"},
				ListedProject{Path: "/code/deleted", Name: "deleted", Gone: true},
			)},
			wantPaths: []string{"/code/afk"},
			wantNames: []string{"afk"},
		},
		{
			name:      "a name from config.toml",
			reads:     []read{good(ListedProject{Path: "/code/some-long-folder", Name: "Shop"})},
			wantPaths: []string{"/code/some-long-folder"},
			wantNames: []string{"Shop"},
		},
		{
			name:      "a listed entry with no name keeps the folder label",
			reads:     []read{good(ListedProject{Path: "/code/afk"})},
			wantPaths: []string{"/code/afk"},
			wantNames: []string{"afk"},
		},
		{
			name:      "a listed name beside a found project with the same folder name",
			found:     scannedProjects("/roots/app"),
			reads:     []read{good(ListedProject{Path: "/code/app", Name: "app"})},
			wantPaths: []string{"/roots/app", "/code/app"},
			wantNames: []string{"roots/app", "app"},
		},
		{
			name: "an unreadable file keeps the held rows and names the file",
			heldListed: []ListedProject{
				{Path: "/code/afk", Name: "afk"},
				{Path: "/code/shop", Name: "shop"},
			},
			reads: []read{{
				path: listedConfigPath,
				err:  errors.New("permission denied"),
			}},
			wantPaths:  []string{"/code/afk", "/code/shop"},
			wantNames:  []string{"afk", "shop"},
			wantNotice: []string{"Could not read the project list: " + listedConfigPath + ": permission denied.", "The switcher keeps the projects it had."},
		},
		{
			name:       "an unreadable file whose error already names it is not named twice",
			heldListed: []ListedProject{{Path: "/code/afk", Name: "afk"}},
			reads: []read{{
				path: listedConfigPath,
				err:  errors.New(listedConfigPath + ": line 3: expected '='\nnear \"path\"."),
			}},
			wantPaths:  []string{"/code/afk"},
			wantNames:  []string{"afk"},
			wantNotice: []string{"Could not read the project list: " + listedConfigPath + ": line 3: expected '=' · near \"path\". The switcher"},
		},
		{
			name: "a good read after an unreadable one drops a removed project",
			heldListed: []ListedProject{
				{Path: "/code/afk", Name: "afk"},
				{Path: "/code/shop", Name: "shop"},
			},
			reads: []read{
				{path: listedConfigPath, err: errors.New("permission denied")},
				good(ListedProject{Path: "/code/afk", Name: "afk"}),
			},
			wantPaths: []string{"/code/afk"},
			wantNames: []string{"afk"},
		},
		{
			name:      "a found project the search no longer finds is dropped",
			held:      scannedProjects("/roots/old", "/roots/a"),
			found:     scannedProjects("/roots/a"),
			reads:     []read{good(ListedProject{Path: "/code/afk", Name: "afk"})},
			wantPaths: []string{"/roots/a", "/code/afk"},
			wantNames: []string{"a", "afk"},
		},
		{
			name:  "mixed sources give found, then listed, then the start-up row",
			held:  []project.Project{pinnedProject("/pin"), {Path: "/roots/b", Name: "b"}},
			found: scannedProjects("/roots/b", "/roots/a"),
			reads: []read{good(
				ListedProject{Path: "/code/z", Name: "z"},
				ListedProject{Path: "/roots/a", Name: "ay"},
				ListedProject{Path: "/code/y", Name: "y"},
				ListedProject{Path: "/code/z", Name: "again"},
			)},
			wantPaths:  []string{"/roots/b", "/roots/a", "/code/z", "/code/y", "/pin"},
			wantNames:  []string{"b", "ay", "z", "y", "pin"},
			wantPinned: []string{"/pin"},
		},
		{
			name:  "nothing anywhere is an empty list",
			reads: []read{good()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listed, notice := tt.heldListed, ""
			for _, r := range tt.reads {
				listed, notice = foldListedRead(listed, r.entries, r.path, r.err)
			}
			in := mergeInput{
				held:               tt.held,
				launchMainCheckout: tt.mainCheckout,
				listed:             listed,
				found:              tt.found,
			}
			got := mergeProjects(in)

			if !pathsEqual(projectPaths(got), tt.wantPaths) {
				t.Errorf("paths = %v, want %v", projectPaths(got), tt.wantPaths)
			}
			if !pathsEqual(projectNames(got), tt.wantNames) {
				t.Errorf("names = %v, want %v", projectNames(got), tt.wantNames)
			}
			var gotPinned []string
			for _, p := range got {
				if p.Pinned {
					gotPinned = append(gotPinned, p.Path)
				}
			}
			if !pathsEqual(gotPinned, tt.wantPinned) {
				t.Errorf("pinned = %v, want %v", gotPinned, tt.wantPinned)
			}

			if tt.wantNotice == nil && notice != "" {
				t.Errorf("notice = %q, want none", notice)
			}
			for _, want := range tt.wantNotice {
				if !strings.Contains(notice, want) {
					t.Errorf("notice = %q, want it to contain %q", notice, want)
				}
			}
			if strings.Count(notice, listedConfigPath) > 1 {
				t.Errorf("notice = %q, names the file more than once", notice)
			}
			if strings.Contains(notice, "\n") {
				t.Errorf("notice = %q, must be one line", notice)
			}

			// The same input twice, and the result fed back as the held rows: both
			// must give the same rows in the same order.
			again := mergeProjects(in)
			if !sameProjectPaths(got, again) {
				t.Errorf("second merge = %v, want the same order as %v", projectPaths(again), projectPaths(got))
			}
			in.held = got
			fed := mergeProjects(in)
			if !sameProjectPaths(got, fed) {
				t.Errorf("merge over its own result = %v, want %v", projectPaths(fed), projectPaths(got))
			}
		})
	}
}

// TestScanExtraPaths pins which rows a scan pass covers beyond its own walk.
// The gone and stood-in-for rows are the load-bearing ones: neither has a row,
// and covering them would open a click-to-focus watcher on every pass only for
// applyRendezvous to close it again.
func TestScanExtraPaths(t *testing.T) {
	listed := []ListedProject{
		{Path: "/code/afk", Name: "afk"},
		{Path: "/code/deleted", Name: "deleted", Gone: true},
		{Path: "/code/main", Name: "main"},
	}
	tests := []struct {
		name string
		rows []project.Project
		want []string
	}{
		{
			name: "pinned and listed rows are covered, a found-only row is not",
			rows: []project.Project{{Path: "/roots/a"}, {Path: "/code/afk"}, {Path: "/pin", Pinned: true}},
			want: []string{"/code/afk", "/pin"},
		},
		{
			name: "a gone listed folder is not covered even if a row carries its path",
			rows: []project.Project{{Path: "/code/deleted"}},
			want: nil,
		},
		{
			name: "a listed project with no row is not covered",
			rows: []project.Project{{Path: "/code/main/.wt/x", Pinned: true}},
			want: []string{"/code/main/.wt/x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanExtraPaths(tt.rows, listed); !pathsEqual(got, tt.want) {
				t.Errorf("scanExtraPaths = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- the model ---

// TestListedProjectsFillTheSwitcherWithNoSearchFolders is the task's headline
// through the real paths: two listed projects, no search folders and no
// start-up project. Running the Init commands and pressing `p` must show both
// rows under their listed names.
func TestListedProjectsFillTheSwitcherWithNoSearchFolders(t *testing.T) {
	d, calls := listedSeam(noSearchFolders(testDeps()),
		ListedProject{Path: "/code/afk", Name: "robot"},
		ListedProject{Path: "/code/some-long-folder", Name: "Shop"},
	)
	d.ActiveProjectIndex = -1
	m := New("/elsewhere", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40

	msgs := collectMsgs(t, m.Init())
	if !hasMsg[listedProjectsMsg](msgs) {
		t.Fatalf("Init collected %v, want a listedProjectsMsg", msgs)
	}
	if calls.count() != 1 {
		t.Fatalf("LoadListed called %d times by Init, want 1", calls.count())
	}
	m, _ = applyMsgsOfScanAndListed(t, m, msgs)

	m = openSwitcherOverlay(t, m)
	rows := switcherRowPaths(m)
	if len(rows) != 2 {
		t.Fatalf("switcher rows = %v, want both listed projects", rows)
	}
	for _, want := range []string{"/code/afk", "/code/some-long-folder"} {
		found := false
		for _, got := range rows {
			found = found || got == want
		}
		if !found {
			t.Errorf("switcher rows = %v, missing %s", rows, want)
		}
	}
	view := render(m)
	for _, name := range []string{"robot", "Shop"} {
		if !strings.Contains(view, name) {
			t.Errorf("View is missing the listed name %q:\n%s", name, view)
		}
	}
	if strings.Contains(view, "some-long-folder") {
		t.Errorf("View shows the folder name of a project that has a listed name:\n%s", view)
	}
	// Started outside any project, the first listed one becomes the project on
	// screen, exactly as the first found one does.
	if got := m.activeProjectPath(); got != "/code/afk" {
		t.Errorf("active project = %q, want the first listed project", got)
	}
}

// TestListedReadShowsRowsBeforeTheScanAnswers pins that the listed rows do not
// wait for the folder walk: only the listed message is applied, the scan's is
// never delivered, and the rows are there. The second half pins the open
// switcher: it holds a snapshot, so a read that adds a row has to rebuild it.
func TestListedReadShowsRowsBeforeTheScanAnswers(t *testing.T) {
	d := scanSeams(testDeps(), "/roots/alpha")
	d.Projects = []project.Project{pinnedProject("/pin")}
	d.ActiveProjectIndex = 0
	m := New("/pin", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40

	m = openSwitcherOverlay(t, m)
	if rows := switcherRowPaths(m); len(rows) != 1 {
		t.Fatalf("precondition: switcher rows = %v, want the start-up row alone", rows)
	}

	m, _ = update(t, m, listedProjectsMsg{
		entries: []ListedProject{{Path: "/code/afk", Name: "afk"}, {Path: "/code/shop", Name: "shop"}},
		path:    listedConfigPath,
	})

	if want := []string{"/code/afk", "/code/shop", "/pin"}; !pathsEqual(projectPaths(m.projects), want) {
		t.Errorf("projects = %v, want %v", projectPaths(m.projects), want)
	}
	if rows := switcherRowPaths(m); len(rows) != 3 {
		t.Errorf("open switcher rows = %v, want it rebuilt with the listed projects", rows)
	}
	if got := m.activeProjectPath(); got != "/pin" {
		t.Errorf("active project = %q, want the start-up project kept", got)
	}
}

// TestListedReadErrorKeepsRowsAndToastsOnce is the unreadable-file path through
// the model: the rows held stay, the toast names the file, and a second
// identical failure is silent. clearStatus between the passes is what proves
// notifiedListedErr's own tracking rather than notify's identical-text skip,
// the same reason TestScanErrorNotifiesOncePerFailure clears. The last stanzas
// pin the re-arm: a good read lets the same failure be reported again, and
// that good read is also what drops a project removed from the file.
func TestListedReadErrorKeepsRowsAndToastsOnce(t *testing.T) {
	d := testDeps()
	d.ActiveProjectIndex = -1
	m := New("/elsewhere", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40
	two := []ListedProject{{Path: "/code/afk", Name: "afk"}, {Path: "/code/shop", Name: "shop"}}
	m, _ = update(t, m, listedProjectsMsg{entries: two, path: listedConfigPath})
	if len(m.projects) != 2 {
		t.Fatalf("precondition: projects = %v, want the two listed", projectPaths(m.projects))
	}
	m.clearStatus()

	fail := listedProjectsMsg{path: listedConfigPath, err: errors.New(listedConfigPath + ": line 2: bad key")}
	m, cmd := update(t, m, fail)
	if want := []string{"/code/afk", "/code/shop"}; !pathsEqual(projectPaths(m.projects), want) {
		t.Errorf("projects after an unreadable file = %v, want %v kept", projectPaths(m.projects), want)
	}
	if got := m.activeProjectPath(); got != "/code/afk" {
		t.Errorf("active project = %q, want it unchanged by a failed read", got)
	}
	if cmd != nil {
		t.Error("a failed read dispatches nothing")
	}
	wantToast := "Could not read the project list: " + listedConfigPath + ": line 2: bad key. The switcher keeps the projects it had."
	if m.status != wantToast || m.statusKind != notifyError {
		t.Fatalf("status = %q kind = %v, want %q as an error", m.status, m.statusKind, wantToast)
	}
	t.Logf("rows kept: %v", projectPaths(m.projects))
	t.Logf("toast: %s", m.status)

	m.clearStatus()
	m, _ = update(t, m, fail)
	if m.status != "" {
		t.Fatalf("status = %q, want silence for a second identical failure", m.status)
	}
	if len(m.projects) != 2 {
		t.Errorf("projects = %v, want the rows still kept", projectPaths(m.projects))
	}

	m, _ = update(t, m, listedProjectsMsg{entries: two[:1], path: listedConfigPath})
	if want := []string{"/code/afk"}; !pathsEqual(projectPaths(m.projects), want) {
		t.Errorf("projects after a good read = %v, want %v", projectPaths(m.projects), want)
	}
	m.clearStatus()
	m, _ = update(t, m, fail)
	if m.status != wantToast {
		t.Fatalf("status = %q, want the same failure reported again after a good read", m.status)
	}
}

// TestListedCommandIsIssuedOnlyWhenTheSeamIsWired pins the inertness contract
// and its other side. The "every seam nil" row is the load-bearing one: a
// default for LoadListed, or a listed command that did not gate on it, would
// make every zero-Deps Model in this suite read the developer's real
// config.toml. "listed alone" pins that the tick is armed without the scan
// seams, since a machine with no search settings still has to notice a project
// added to the list.
func TestListedCommandIsIssuedOnlyWhenTheSeamIsWired(t *testing.T) {
	tests := []struct {
		name       string
		deps       func() Deps
		wantListed bool
		wantScan   bool
		wantTick   bool
	}{
		{
			name: "every seam nil",
			deps: testDeps,
		},
		{
			name:     "scan seams alone",
			deps:     func() Deps { return scanSeams(testDeps()) },
			wantScan: true,
			wantTick: true,
		},
		{
			name: "listed alone",
			deps: func() Deps {
				d, _ := listedSeam(testDeps(), ListedProject{Path: "/code/afk", Name: "afk"})
				return d
			},
			wantListed: true,
			wantTick:   true,
		},
		{
			name: "listed and scan",
			deps: func() Deps {
				d, _ := listedSeam(scanSeams(testDeps()), ListedProject{Path: "/code/afk", Name: "afk"})
				return d
			},
			wantListed: true,
			wantScan:   true,
			wantTick:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.deps()
			d.ActiveProjectIndex = -1
			m := New("/elsewhere", time.Second, d)

			if got := m.listedCmd() != nil; got != tt.wantListed {
				t.Errorf("listedCmd issued = %t, want %t", got, tt.wantListed)
			}
			if got := m.scanCmd() != nil; got != tt.wantScan {
				t.Errorf("scanCmd issued = %t, want %t", got, tt.wantScan)
			}
			if got := m.scanTickCmd() != nil; got != tt.wantTick {
				t.Errorf("scanTickCmd armed = %t, want %t", got, tt.wantTick)
			}

			initMsgs := collectMsgs(t, m.Init())
			if got := hasMsg[listedProjectsMsg](initMsgs); got != tt.wantListed {
				t.Errorf("Init read the project list = %t, want %t", got, tt.wantListed)
			}
			_, tickCmd := update(t, m, scanTickMsg(time.Unix(1_000_000, 0)))
			tickMsgs := collectMsgs(t, tickCmd)
			if got := hasMsg[listedProjectsMsg](tickMsgs); got != tt.wantListed {
				t.Errorf("scan tick read the project list = %t, want %t", got, tt.wantListed)
			}
			if got := hasMsg[projectScanMsg](tickMsgs); got != tt.wantScan {
				t.Errorf("scan tick scanned = %t, want %t", got, tt.wantScan)
			}
		})
	}
}

// TestWithDefaultsLeavesLoadListedNil pins the omission in withDefaults by
// name, beside the row above that pins its consequence.
func TestWithDefaultsLeavesLoadListedNil(t *testing.T) {
	if (Deps{}).withDefaults().LoadListed != nil {
		t.Fatal("withDefaults wired LoadListed; a zero-Deps Model would read the real config.toml")
	}
}

// TestListedOnlyModelListsRowsWithoutTheScanSeams pins the last sentence of the
// scan-coverage rule: with LoadListed wired and the scan seams nil, the listed
// read alone fills the list, and it asks for no scan because there is none.
func TestListedOnlyModelListsRowsWithoutTheScanSeams(t *testing.T) {
	d, _ := listedSeam(testDeps(),
		ListedProject{Path: "/code/afk", Name: "afk"},
		ListedProject{Path: "/code/shop", Name: "shop"},
	)
	d.Projects = []project.Project{pinnedProject("/pin")}
	d.ActiveProjectIndex = 0
	m := New("/pin", time.Second, d)

	msg, ok := m.listedCmd()().(listedProjectsMsg)
	if !ok {
		t.Fatal("listedCmd did not produce a listedProjectsMsg")
	}
	m, cmd := update(t, m, msg)
	if want := []string{"/code/afk", "/code/shop", "/pin"}; !pathsEqual(projectPaths(m.projects), want) {
		t.Errorf("projects = %v, want %v", projectPaths(m.projects), want)
	}
	if msgs := collectMsgs(t, cmd); hasMsg[projectScanMsg](msgs) {
		t.Error("a listed read asked for a scan on a model with no scan seams")
	}
}

// TestListedReadKeepsTheActiveProject pins that a read of the project list
// never repoints the dashboard. Every row but the last moves the active project
// to a different INDEX, which is the trap: an index carried across the rebuild
// would land on another project, and the per-project commands would then run
// against the wrong folder. No switch may be dispatched either, since a switch
// detaches the open pane.
func TestListedReadKeepsTheActiveProject(t *testing.T) {
	tests := []struct {
		name       string
		projects   []project.Project
		active     int
		before     []ListedProject
		read       []ListedProject
		wantActive string
		wantPaths  []string
		// sameIndex marks the one row whose active project keeps its index;
		// every other row must move it, or it is not testing the trap.
		sameIndex bool
	}{
		{
			name:       "the start-up row is pushed down by newly listed projects",
			projects:   []project.Project{pinnedProject("/pin")},
			active:     0,
			read:       []ListedProject{{Path: "/code/afk", Name: "afk"}, {Path: "/code/shop", Name: "shop"}},
			wantActive: "/pin",
			wantPaths:  []string{"/code/afk", "/code/shop", "/pin"},
		},
		{
			name:       "a listed project moves up when the one above it is removed",
			before:     []ListedProject{{Path: "/code/afk", Name: "afk"}, {Path: "/code/shop", Name: "shop"}},
			active:     1,
			read:       []ListedProject{{Path: "/code/shop", Name: "shop"}},
			wantActive: "/code/shop",
			wantPaths:  []string{"/code/shop"},
		},
		{
			name:       "the file's order is reversed",
			before:     []ListedProject{{Path: "/code/afk", Name: "afk"}, {Path: "/code/shop", Name: "shop"}},
			active:     0,
			read:       []ListedProject{{Path: "/code/shop", Name: "shop"}, {Path: "/code/afk", Name: "afk"}},
			wantActive: "/code/afk",
			wantPaths:  []string{"/code/shop", "/code/afk"},
		},
		{
			name:       "a found project stays active when listed projects are read",
			projects:   scannedProjects("/roots/a", "/roots/b"),
			active:     1,
			read:       []ListedProject{{Path: "/code/afk", Name: "afk"}},
			wantActive: "/roots/b",
			wantPaths:  []string{"/roots/a", "/roots/b", "/code/afk"},
			sameIndex:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &perProjectSpy{}
			d := spy.deps()
			d.Projects = tt.projects
			d.ActiveProjectIndex = tt.active
			if tt.projects == nil {
				d.ActiveProjectIndex = -1
			}
			m := New("/start", time.Second, d)
			if tt.before != nil {
				m, _ = update(t, m, listedProjectsMsg{entries: tt.before, path: listedConfigPath})
				nm, _ := m.switchProject(tt.wantActive)
				m = nm
			}
			if got := m.activeProjectPath(); got != tt.wantActive {
				t.Fatalf("precondition: active project = %q, want %q", got, tt.wantActive)
			}
			wasIndex, wasRepo := m.activeProject, m.repo

			m, cmd := update(t, m, listedProjectsMsg{entries: tt.read, path: listedConfigPath})

			if !pathsEqual(projectPaths(m.projects), tt.wantPaths) {
				t.Errorf("projects = %v, want %v", projectPaths(m.projects), tt.wantPaths)
			}
			if got := m.activeProjectPath(); got != tt.wantActive {
				t.Errorf("active project = %q, want %q", got, tt.wantActive)
			}
			if !tt.sameIndex && m.activeProject == wasIndex {
				t.Errorf("active index stayed %d: this row is meant to move it", wasIndex)
			}
			if m.repo != wasRepo {
				t.Errorf("repo = %q, want it left at %q", m.repo, wasRepo)
			}
			if msgs := collectMsgs(t, cmd); hasMsg[sessionsLoadedMsg](msgs) {
				t.Error("a listed read dispatched a project switch")
			}
		})
	}
}

// TestScanCoversListedProjects pins the scan's coverage of listed rows and the
// merge on its result. Three things, each a way a listed row would look broken:
// the pass must run discovery for a listed project the walk does not find (or
// its row has no session counts), must not touch a listed folder that is gone,
// and its result must not drop the listed row from the list just because the
// search did not find it.
func TestScanCoversListedProjects(t *testing.T) {
	spy := &perProjectSpy{}
	d := scanSeams(spy.deps(), "/roots/alpha")
	d.Discover = func(repo string) ([]discover.SessionState, error) {
		spy.mu.Lock()
		spy.discovered = append(spy.discovered, repo)
		spy.mu.Unlock()
		if repo == "/code/afk" {
			return []discover.SessionState{sampleState("one"), sampleState("two")}, nil
		}
		return nil, nil
	}
	d.Projects = []project.Project{pinnedProject("/pin")}
	d.ActiveProjectIndex = 0
	m := New("/pin", time.Second, d)

	m, cmd := update(t, m, listedProjectsMsg{
		entries: []ListedProject{
			{Path: "/code/afk", Name: "afk"},
			{Path: "/code/deleted", Name: "deleted", Gone: true},
		},
		path: listedConfigPath,
	})
	// A read that changed the listed folders asks for a scan pass, so the new
	// row gets its counts without waiting a minute for the ticker.
	var scan projectScanMsg
	gotScan := false
	for _, msg := range collectMsgs(t, cmd) {
		if s, ok := msg.(projectScanMsg); ok {
			scan, gotScan = s, true
		}
	}
	if !gotScan {
		t.Fatal("a listed read that added a project asked for no scan")
	}
	if len(scan.sessions["/code/afk"]) != 2 {
		t.Errorf("scan sessions for the listed project = %v, want its two sessions", scan.sessions["/code/afk"])
	}
	discovered, _, _ := spy.snapshot()
	for _, repo := range discovered {
		if repo == "/code/deleted" {
			t.Error("the scan ran discovery in a listed folder that is gone")
		}
	}

	m, _ = update(t, m, scan)
	if want := []string{"/roots/alpha", "/code/afk", "/pin"}; !pathsEqual(projectPaths(m.projects), want) {
		t.Errorf("projects after the scan = %v, want %v", projectPaths(m.projects), want)
	}
	if got := m.activeProjectPath(); got != "/pin" {
		t.Errorf("active project = %q, want /pin", got)
	}

	// The same listed folders again: nothing changed, so no extra walk.
	_, cmd = update(t, m, listedProjectsMsg{
		entries: []ListedProject{
			{Path: "/code/afk", Name: "afk"},
			{Path: "/code/deleted", Name: "deleted", Gone: true},
		},
		path: listedConfigPath,
	})
	if msgs := collectMsgs(t, cmd); hasMsg[projectScanMsg](msgs) {
		t.Error("an unchanged listed read asked for another scan")
	}
}

// TestSwitcherRescanReadsTheProjectListToo pins `R`: it re-reads the project
// list beside the scan, so a project added with `afk project add` while the
// switcher is open appears on `R` rather than a minute later.
func TestSwitcherRescanReadsTheProjectListToo(t *testing.T) {
	d, calls := listedSeam(scanSeams(testDeps(), "/roots/alpha"),
		ListedProject{Path: "/code/afk", Name: "afk"})
	d.Projects = scannedProjects("/roots/alpha")
	d.ActiveProjectIndex = 0
	m := New("/roots/alpha", time.Second, d)
	m.ready = true
	m.width, m.height = 120, 40

	m = openSwitcherOverlay(t, m)
	m, cmd := update(t, m, keyR)
	msgs := collectMsgs(t, cmd)
	if !hasMsg[listedProjectsMsg](msgs) || !hasMsg[projectScanMsg](msgs) {
		t.Fatalf("`R` collected %v, want a listed read and a scan", msgs)
	}
	if calls.count() != 1 {
		t.Errorf("LoadListed called %d times by `R`, want 1", calls.count())
	}
	m, _ = applyMsgsOfScanAndListed(t, m, msgs)
	if !m.switcherOpen {
		t.Error("`R` must leave the switcher open")
	}
	if rows := switcherRowPaths(m); len(rows) != 2 {
		t.Errorf("switcher rows after `R` = %v, want the found and the listed project", rows)
	}
}
