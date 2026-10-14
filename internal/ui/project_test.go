package ui

import (
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/spice"
)

// --- helpers ---

// scannedProjects builds the shape internal/scan returns: display names
// computed over the whole set, Pinned left false.
func scannedProjects(paths ...string) []project.Project {
	return project.ProjectsFromPaths(paths)
}

// pinnedProject builds the shape dash.go's pickActiveProject produces for the
// cwd-derived project no search root covers (D6).
func pinnedProject(path string) project.Project {
	p := project.ProjectsFromPaths([]string{path})[0]
	p.Pinned = true
	return p
}

// projectModel builds a Model the way dash.go does — projects and the active
// index arrive through Deps, never as New parameters — so these tests exercise
// the same wiring the binary uses.
func projectModel(repo string, active int, projects ...project.Project) Model {
	d := testDeps()
	d.Projects = projects
	d.ActiveProjectIndex = active
	m := New(repo, time.Second, d)
	m.ready = true
	m.width = 120
	m.height = 40
	return m
}

func projectPaths(projects []project.Project) []string {
	paths := make([]string, len(projects))
	for i, p := range projects {
		paths[i] = p.Path
	}
	return paths
}

func pathsEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// perProjectSpy records which repo each per-project seam was called with, so a
// test can prove a Cmd was dispatched AGAINST THE NEW PROJECT rather than
// merely dispatched. The Cmds run on their own goroutines (collectMsgs), hence
// the mutex.
type perProjectSpy struct {
	mu         sync.Mutex
	discovered []string
	detected   []string
	branched   []string
}

func (s *perProjectSpy) deps() Deps {
	d := testDeps()
	d.Discover = func(repo string) ([]discover.SessionState, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.discovered = append(s.discovered, repo)
		return nil, nil
	}
	d.SpiceDetect = func(repo string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.detected = append(s.detected, repo)
		return false
	}
	d.PlainBranches = func(repo string) ([]string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.branched = append(s.branched, repo)
		return nil, nil
	}
	return d
}

func (s *perProjectSpy) snapshot() (discovered, detected, branched []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.discovered...),
		append([]string(nil), s.detected...),
		append([]string(nil), s.branched...)
}

// hasMsg reports whether a collected batch carried a message of type T, which
// is how these tests assert WHICH Cmds a dispatch site issued.
func hasMsg[T tea.Msg](msgs []tea.Msg) bool {
	for _, msg := range msgs {
		if _, ok := msg.(T); ok {
			return true
		}
	}
	return false
}

// --- tests ---

// TestApplyProjectScan is the union table, and every row is load-bearing for a
// specific failure the plan calls out:
//
//   - "pinned entry the scan never finds" is the headline case: for an
//     unconfigured user the pinned cwd project is the ENTIRE list, so an apply
//     that replaced the list wholesale would empty the switcher on first rescan.
//   - "pinned entry the scan also finds" pins de-duplication AND the survival
//     of the Pinned flag — losing the flag lets a later config change drop
//     the project the user is standing in.
//   - "reordered" pins that the active project is preserved by PATH, not index.
//   - "vanished" and "promoted from -1" are different cases (never-existed vs
//     disappeared) that must both end on a real project.
//   - "collision introduced by the union" pins that display names are
//     recomputed over the MERGED set, not carried over from the scan.
func TestApplyProjectScan(t *testing.T) {
	tests := []struct {
		name       string
		repo       string
		active     int
		prev       []project.Project
		scanned    []project.Project
		wantPaths  []string
		wantPinned []string
		wantNames  []string
		wantActive string
		wantRepo   string
		wantSwitch bool
	}{
		{
			name:       "pinned entry survives a scan that never finds it",
			repo:       "/pin",
			active:     0,
			prev:       []project.Project{pinnedProject("/pin")},
			scanned:    scannedProjects("/roots/a", "/roots/b"),
			wantPaths:  []string{"/roots/a", "/roots/b", "/pin"},
			wantPinned: []string{"/pin"},
			wantActive: "/pin",
			wantRepo:   "/pin",
		},
		{
			name:       "unconfigured user keeps the pinned entry as the whole list",
			repo:       "/pin",
			active:     0,
			prev:       []project.Project{pinnedProject("/pin")},
			scanned:    nil,
			wantPaths:  []string{"/pin"},
			wantPinned: []string{"/pin"},
			wantActive: "/pin",
			wantRepo:   "/pin",
		},
		{
			name:       "pinned entry the scan also finds appears once and stays pinned",
			repo:       "/roots/a",
			active:     0,
			prev:       []project.Project{pinnedProject("/roots/a")},
			scanned:    scannedProjects("/roots/a", "/roots/b"),
			wantPaths:  []string{"/roots/a", "/roots/b"},
			wantPinned: []string{"/roots/a"},
			wantActive: "/roots/a",
			wantRepo:   "/roots/a",
		},
		{
			name:       "unpinned entry the scan no longer finds is dropped",
			repo:       "/roots/a",
			active:     0,
			prev:       scannedProjects("/roots/a", "/roots/gone"),
			scanned:    scannedProjects("/roots/a"),
			wantPaths:  []string{"/roots/a"},
			wantActive: "/roots/a",
			wantRepo:   "/roots/a",
		},
		{
			name:       "active project preserved across a rescan that reorders entries",
			repo:       "/roots/b",
			active:     1,
			prev:       scannedProjects("/roots/a", "/roots/b"),
			scanned:    scannedProjects("/roots/b", "/roots/c"),
			wantPaths:  []string{"/roots/b", "/roots/c"},
			wantActive: "/roots/b",
			wantRepo:   "/roots/b",
		},
		{
			name:       "vanished active project falls back to the first entry",
			repo:       "/roots/gone",
			active:     0,
			prev:       scannedProjects("/roots/gone", "/roots/b"),
			scanned:    scannedProjects("/roots/b", "/roots/c"),
			wantPaths:  []string{"/roots/b", "/roots/c"},
			wantActive: "/roots/b",
			wantRepo:   "/roots/b",
			wantSwitch: true,
		},
		{
			name:       "an active index of -1 is promoted to the first entry",
			repo:       "/not-a-repo",
			active:     -1,
			prev:       nil,
			scanned:    scannedProjects("/roots/a", "/roots/b"),
			wantPaths:  []string{"/roots/a", "/roots/b"},
			wantActive: "/roots/a",
			wantRepo:   "/roots/a",
			wantSwitch: true,
		},
		{
			name:       "nothing found and nothing pinned leaves no active project",
			repo:       "/not-a-repo",
			active:     -1,
			prev:       nil,
			scanned:    nil,
			wantPaths:  nil,
			wantActive: "",
			wantRepo:   "/not-a-repo",
		},
		{
			name:       "the union recomputes display names over the merged set",
			repo:       "/pin/api",
			active:     0,
			prev:       []project.Project{pinnedProject("/pin/api")},
			scanned:    scannedProjects("/roots/api"),
			wantPaths:  []string{"/roots/api", "/pin/api"},
			wantNames:  []string{"roots/api", "pin/api"},
			wantPinned: []string{"/pin/api"},
			wantActive: "/pin/api",
			wantRepo:   "/pin/api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &perProjectSpy{}
			d := spy.deps()
			d.Projects = tt.prev
			d.ActiveProjectIndex = tt.active
			m := New(tt.repo, time.Second, d)

			msg := projectScanMsg{
				projects:   tt.scanned,
				sessions:   map[string][]discover.SessionState{"/roots/a": {sampleState("one")}},
				configPath: "/cfg/config.toml",
				interval:   90 * time.Second,
			}
			got, cmd := m.Update(msg)
			m, ok := got.(Model)
			if !ok {
				t.Fatalf("Update returned %T, want ui.Model", got)
			}

			if !pathsEqual(projectPaths(m.projects), tt.wantPaths) {
				t.Errorf("projects = %v, want %v", projectPaths(m.projects), tt.wantPaths)
			}
			if tt.wantNames != nil {
				names := make([]string, len(m.projects))
				for i, p := range m.projects {
					names[i] = p.Name
				}
				if !pathsEqual(names, tt.wantNames) {
					t.Errorf("display names = %v, want %v", names, tt.wantNames)
				}
			}
			for _, path := range tt.wantPinned {
				found := false
				for _, p := range m.projects {
					if p.Path == path && p.Pinned {
						found = true
					}
				}
				if !found {
					t.Errorf("%s is not pinned in %+v", path, m.projects)
				}
			}
			if got := m.activeProjectPath(); got != tt.wantActive {
				t.Errorf("active project = %q, want %q", got, tt.wantActive)
			}
			if m.repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", m.repo, tt.wantRepo)
			}
			if m.configPath != "/cfg/config.toml" {
				t.Errorf("configPath = %q, want the path the scan resolved", m.configPath)
			}
			if m.scanInterval != 90*time.Second {
				t.Errorf("scanInterval = %v, want the configured 90s", m.scanInterval)
			}
			if len(m.projectSessions) != 1 {
				t.Errorf("projectSessions = %v, want the scan's cache", m.projectSessions)
			}

			// A switch — and only a switch — repoints the per-project commands.
			msgs := collectMsgs(t, cmd)
			gotSwitch := hasMsg[sessionsLoadedMsg](msgs)
			if gotSwitch != tt.wantSwitch {
				t.Errorf("per-project commands dispatched = %t, want %t", gotSwitch, tt.wantSwitch)
			}
			if !tt.wantSwitch {
				return
			}
			if !hasMsg[stackLoadedMsg](msgs) || !hasMsg[plainBranchesLoadedMsg](msgs) {
				t.Error("a promotion must dispatch the stack and plain-branch refreshes too")
			}
			discovered, detected, branched := spy.snapshot()
			for _, calls := range [][]string{discovered, detected, branched} {
				if len(calls) != 1 || calls[0] != tt.wantActive {
					t.Errorf("per-project seam called with %v, want [%s]", calls, tt.wantActive)
				}
			}
		})
	}
}

// TestApplyProjectScanKeepsStateWhenTheScanFailed pins the config-error path:
// a config file that went malformed under a running dashboard must not empty
// the switcher, and the resolved path still has to reach the Model — W5.T1's
// empty state names that file, and views.go performs no I/O.
func TestApplyProjectScanKeepsStateWhenTheScanFailed(t *testing.T) {
	m := projectModel("/roots/a", 0, scannedProjects("/roots/a", "/roots/b")...)
	m.projectSessions = map[string][]discover.SessionState{"/roots/a": {sampleState("one")}}

	got, cmd := m.Update(projectScanMsg{
		configPath: "/cfg/config.toml",
		err:        errors.New("invalid character 'x'"),
	})
	m = got.(Model)

	if len(m.projects) != 2 {
		t.Errorf("projects = %v, want the previous list kept", projectPaths(m.projects))
	}
	if len(m.projectSessions) != 1 {
		t.Errorf("projectSessions = %v, want the previous cache kept", m.projectSessions)
	}
	if m.configPath != "/cfg/config.toml" {
		t.Errorf("configPath = %q, want the path even on the error path", m.configPath)
	}
	if m.status == "" {
		t.Error("a failed scan must say so in the status line")
	}
	if cmd != nil {
		t.Error("a failed scan dispatches nothing")
	}
}

// TestScanCmdWalksTheConfiguredRootsAndDiscoversEveryProject pins three things
// at once, each of which has bitten a downstream task in this plan:
// the seam carries max_depth (the wrapper in dash.go is built before any config
// load, so it cannot close over the depth); the per-project counts come from
// discover.Discover and nothing else; and PINNED projects are discovered too —
// for an unconfigured user the pinned entry is the only row the switcher has.
func TestScanCmdWalksTheConfiguredRootsAndDiscoversEveryProject(t *testing.T) {
	var gotRoots []string
	gotDepth := 0
	awaiting := discover.SessionState{Name: "waiting", AwaitingInput: true}

	d := testDeps()
	d.LoadConfig = func() (SearchSettings, string, error) {
		return SearchSettings{
			Roots:    []string{"/roots"},
			MaxDepth: 5,
			Interval: 30 * time.Second,
		}, "/cfg/config.toml", nil
	}
	d.Scan = func(roots []string, maxDepth int) []project.Project {
		gotRoots = roots
		gotDepth = maxDepth
		return scannedProjects("/roots/a", "/roots/b")
	}
	d.Discover = func(repo string) ([]discover.SessionState, error) {
		switch repo {
		case "/roots/a":
			return []discover.SessionState{sampleState("one"), awaiting}, nil
		case "/pin":
			return []discover.SessionState{sampleState("pinned-one")}, nil
		default:
			return nil, errors.New("not a repo")
		}
	}
	d.Projects = []project.Project{pinnedProject("/pin")}
	m := New("/pin", time.Second, d)

	msg, ok := m.scanCmd()().(projectScanMsg)
	if !ok {
		t.Fatal("scanCmd must produce a projectScanMsg")
	}

	if !pathsEqual(gotRoots, []string{"/roots"}) || gotDepth != 5 {
		t.Errorf("Scan called with roots=%v depth=%d, want [/roots] and the configured 5", gotRoots, gotDepth)
	}
	if !pathsEqual(projectPaths(msg.projects), []string{"/roots/a", "/roots/b"}) {
		t.Errorf("projects = %v, want the scan's result verbatim (the union happens in Update)", projectPaths(msg.projects))
	}
	if msg.configPath != "/cfg/config.toml" || msg.interval != 30*time.Second {
		t.Errorf("msg carried path %q interval %v, want the config's", msg.configPath, msg.interval)
	}
	if got := len(msg.sessions["/roots/a"]); got != 2 {
		t.Errorf("cache holds %d sessions for /roots/a, want 2 from the injected Discover", got)
	}
	if !msg.sessions["/roots/a"][1].AwaitingInput {
		t.Error("the awaiting-input flag must survive into the cache — the switcher's dot reads it")
	}
	if got := len(msg.sessions["/pin"]); got != 1 {
		t.Errorf("cache holds %d sessions for the pinned project, want 1", got)
	}
	if _, ok := msg.sessions["/roots/b"]; ok {
		t.Error("a project whose discovery failed must contribute no cache entry")
	}
}

// TestScanPipelineIsInertWithoutItsSeams is the gate that makes the rest of
// internal/ui's suite pass unmodified: a Model built without the Scan and
// LoadConfig seams must never read a config file or walk a disk, and its scan
// ticker must issue no work. The zero-value row covers a Model nobody
// constructed through New at all.
func TestScanPipelineIsInertWithoutItsSeams(t *testing.T) {
	tests := []struct {
		name string
		mdl  func() Model
	}{
		{name: "testModel-shaped model", mdl: func() Model { return testModel(sess("one")) }},
		{name: "zero-value model", mdl: func() Model { return Model{} }},
		{
			name: "Scan present but LoadConfig absent",
			mdl: func() Model {
				d := testDeps()
				d.Scan = func([]string, int) []project.Project {
					t.Error("Scan must not run without a config to drive it")
					return nil
				}
				return New("/repo", time.Second, d)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.mdl()
			if cmd := m.scanCmd(); cmd != nil {
				t.Error("scanCmd must be nil when the seams are absent")
			}
			if cmd := m.scanTickCmd(); cmd != nil {
				t.Error("scanTickCmd must be nil when the seams are absent — an armed ticker is work")
			}
			if hasMsg[projectScanMsg](collectMsgs(t, m.Init())) {
				t.Error("Init scanned without the seams being wired")
			}
			if m.projectSessions != nil {
				t.Error("the per-project cache starts empty")
			}
		})
	}
}

// TestZeroValueModelIsSingleProject pins the acceptance criterion directly: the
// zero value has no project layer at all, which is what "today's behavior is
// preserved" means for the 168 testModel call sites.
func TestZeroValueModelIsSingleProject(t *testing.T) {
	var m Model
	if len(m.projects) != 0 || m.projectSessions != nil {
		t.Error("a zero-value Model must carry no project state")
	}
	if m.activeProjectPath() != "" {
		t.Error("a zero-value Model has no active project path")
	}
	if m.hasActiveProject() {
		t.Error("a zero-value Model has no repo, so it has no active project")
	}
}

// TestInitBatchesAnImmediateScan pins the "populated at once, not after a full
// scan interval" requirement: with the seams wired, Init's batch carries a scan
// alongside the existing discovery pass.
func TestInitBatchesAnImmediateScan(t *testing.T) {
	d := testDeps()
	d.LoadConfig = func() (SearchSettings, string, error) {
		return SearchSettings{Interval: time.Minute, MaxDepth: 3}, "/cfg/config.toml", nil
	}
	d.Scan = func([]string, int) []project.Project { return scannedProjects("/roots/a") }
	d.Projects = []project.Project{pinnedProject("/pin")}
	d.ActiveProjectIndex = 0
	m := New("/pin", time.Second, d)

	msgs := collectMsgs(t, m.Init())
	if !hasMsg[projectScanMsg](msgs) {
		t.Error("Init must batch an immediate first scan")
	}
	if !hasMsg[sessionsLoadedMsg](msgs) {
		t.Error("Init must still dispatch the existing discovery pass")
	}
	if hasMsg[scanTickMsg](msgs) {
		t.Error("the scan ticker must not fire immediately — that is what the batched scan is for")
	}
}

// TestScanTickRescansAndRearms pins the second ticker's independence: a scan
// tick rescans and re-arms itself, and the 2s discovery tick triggers no scan
// at all (a whole-disk walk on the UI's refresh cadence is the failure this
// prevents).
func TestScanTickRescansAndRearms(t *testing.T) {
	newModel := func() Model {
		d := testDeps()
		d.LoadConfig = func() (SearchSettings, string, error) {
			return SearchSettings{Interval: time.Minute, MaxDepth: 3}, "/cfg/config.toml", nil
		}
		d.Scan = func([]string, int) []project.Project { return scannedProjects("/roots/a") }
		d.Projects = []project.Project{pinnedProject("/pin")}
		d.ActiveProjectIndex = 0
		return New("/pin", time.Second, d)
	}

	t.Run("scan tick rescans", func(t *testing.T) {
		m := newModel()
		_, cmd := m.Update(scanTickMsg(time.Unix(1, 0)))
		if !hasMsg[projectScanMsg](collectMsgs(t, cmd)) {
			t.Error("a scan tick must run the scan")
		}
	})

	t.Run("discovery tick issues no scan", func(t *testing.T) {
		m := newModel()
		_, cmd := m.Update(tickMsg(time.Unix(1, 0)))
		msgs := collectMsgs(t, cmd)
		if hasMsg[projectScanMsg](msgs) {
			t.Error("the 2s discovery tick must never walk the disk")
		}
		if !hasMsg[sessionsLoadedMsg](msgs) {
			t.Error("the discovery tick must still refresh sessions")
		}
	})
}

// TestNoActiveProjectSuppressesPerProjectCommands is the row set the plan
// singles out. The first row is the load-bearing one: a testModel-shaped Model
// has an EMPTY project list and a non-empty repo, and existing tests assert it
// dispatches loadCmd — keying suppression on len(projects) instead of on the
// active repo would break them. The -1 row is launch case (c): a cwd that is
// not a repo, where discovery would leave a permanent footer error underneath
// W5.T1's empty state.
func TestNoActiveProjectSuppressesPerProjectCommands(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		active   int
		projects []project.Project
		want     bool
	}{
		{name: "empty project list with a real repo still refreshes", repo: "/repo", active: 0, want: true},
		{name: "active project present", repo: "/roots/a", active: 0, projects: scannedProjects("/roots/a"), want: true},
		{name: "active index of -1 suppresses", repo: "/not-a-repo", active: -1, want: false},
		{name: "empty repo suppresses", repo: "", active: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := projectModel(tt.repo, tt.active, tt.projects...)
			if m.hasActiveProject() != tt.want {
				t.Fatalf("hasActiveProject = %t, want %t", m.hasActiveProject(), tt.want)
			}
			for _, site := range []struct {
				name string
				cmd  tea.Cmd
			}{
				{name: "Init", cmd: m.Init()},
				{name: "tick", cmd: func() tea.Cmd { _, c := m.Update(tickMsg(time.Unix(1, 0))); return c }()},
			} {
				msgs := collectMsgs(t, site.cmd)
				for kind, got := range map[string]bool{
					"sessionsLoadedMsg":      hasMsg[sessionsLoadedMsg](msgs),
					"stackLoadedMsg":         hasMsg[stackLoadedMsg](msgs),
					"plainBranchesLoadedMsg": hasMsg[plainBranchesLoadedMsg](msgs),
				} {
					if got != tt.want {
						t.Errorf("%s dispatched %s = %t, want %t", site.name, kind, got, tt.want)
					}
				}
			}
		})
	}
}

// TestSwitchProject covers the three outcomes of the switch method. The
// same-path row is load-bearing beyond tidiness: W5.T3's focus-request handler
// drives this method for a request in the CURRENT project, and clearing the
// pane there would close the session the user just clicked on.
func TestSwitchProject(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		wantRepo   string
		wantActive int
		wantSwitch bool
	}{
		{name: "another project", target: "/roots/b", wantRepo: "/roots/b", wantActive: 1, wantSwitch: true},
		{name: "the already-active project", target: "/roots/a", wantRepo: "/roots/a", wantActive: 0},
		{name: "a path not in the list", target: "/roots/nope", wantRepo: "/roots/a", wantActive: 0},
		{name: "an empty path", target: "", wantRepo: "/roots/a", wantActive: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &perProjectSpy{}
			d := spy.deps()
			d.Projects = scannedProjects("/roots/a", "/roots/b")
			d.ActiveProjectIndex = 0
			m := New("/roots/a", time.Second, d)
			pane := newFakePane("live")
			m.setSessions([]Session{sess("one"), sess("two")})
			m.selectSessionIndex(1)
			m.pane = pane
			m.paneName = "one"
			m.focus = focusRight
			m.stack = stackState{Detected: true, Branches: []spice.Branch{{Name: "one"}}}
			m.plainBranches = []string{"main"}
			m.plainTrunk = "main"
			m.loadErr = "boom"
			m.lastLoad = time.Unix(5, 0)
			m.pendingSpawns = []string{"three"}
			m.attachInFlight = inFlightOpen{name: "one"}
			gen := m.previewGen

			got, cmd := m.switchProject(tt.target)

			if got.repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", got.repo, tt.wantRepo)
			}
			if got.activeProject != tt.wantActive {
				t.Errorf("activeProject = %d, want %d", got.activeProject, tt.wantActive)
			}
			if !tt.wantSwitch {
				if cmd != nil {
					t.Error("a no-op switch dispatches nothing")
				}
				if got.pane == nil || pane.closed != 0 {
					t.Error("a no-op switch must leave the open pane alone")
				}
				if len(got.sessions) != 2 {
					t.Error("a no-op switch must not clear session state")
				}
				return
			}

			// The outgoing pane is DETACHED, not ended: its host is a separate
			// process and keeps running.
			if pane.closed != 1 {
				t.Errorf("outgoing pane closed %d times, want 1", pane.closed)
			}
			if got.pane != nil || got.paneName != "" || got.paneOwnsKeyboard() {
				t.Error("switching project must leave no pane state behind")
			}
			if got.sessions != nil || got.listSelection() != 0 {
				t.Errorf("sessions = %v cursor = %d, want the list cleared", got.sessions, got.listSelection())
			}
			if got.stack.Detected || got.plainBranches != nil || got.plainTrunk != "" {
				t.Error("stack and branch state are repo-scoped and must be cleared")
			}
			if got.loadErr != "" || !got.lastLoad.IsZero() {
				t.Error("the outgoing project's load state must not carry over")
			}
			if got.pendingSpawns != nil {
				t.Error("pending spawns belong to the outgoing project")
			}
			if got.attachInFlight.name != "" {
				t.Error("an in-flight open belongs to the outgoing project")
			}
			if got.previewGen == gen {
				t.Error("previewGen must be bumped so a timer armed on the outgoing list is inert")
			}

			msgs := collectMsgs(t, cmd)
			if !hasMsg[sessionsLoadedMsg](msgs) || !hasMsg[stackLoadedMsg](msgs) || !hasMsg[plainBranchesLoadedMsg](msgs) {
				t.Error("a switch must dispatch load, stack, and plain branches")
			}
			discovered, detected, branched := spy.snapshot()
			for _, calls := range [][]string{discovered, detected, branched} {
				if len(calls) != 1 || calls[0] != tt.wantRepo {
					t.Errorf("per-project seam called with %v, want [%s]", calls, tt.wantRepo)
				}
			}
		})
	}
}

// TestScanHealsTheRepoSpellingForASubdirectoryLaunch pins the reconciliation of
// the Model's two spellings of one repo: a dashboard launched from a repo
// SUBDIRECTORY carries that subdirectory as m.repo (New copies the cwd/--project
// value verbatim) while the pinned project entry holds the normalized git
// toplevel. Nothing switches on the kept-active scan path, so before the heal
// the two diverged for the life of the process — benign for git subprocesses,
// but a standing violation of the one-spelling assumption every path comparison
// in the package makes. The first scan pass must settle m.repo on the entry's
// normalized form.
func TestScanHealsTheRepoSpellingForASubdirectoryLaunch(t *testing.T) {
	m := projectModel("/repo/subdir", 0, pinnedProject("/repo"))

	nm, _ := update(t, m, projectScanMsg{})
	if nm.repo != "/repo" {
		t.Fatalf("repo = %q after the scan, want the active entry's normalized %q", nm.repo, "/repo")
	}
	if nm.repo != nm.activeProjectPath() {
		t.Fatalf("repo = %q and activeProjectPath = %q still disagree", nm.repo, nm.activeProjectPath())
	}
}

// TestTogglePaneDialOutstandingAcrossAProjectSwitchIsDropped pins projectGen's whole
// contract on the reviewer's worst case: BOTH projects hold a session named
// "same", the old project's dial is still outstanding when the user switches
// and re-dials, and the stale arrival lands FIRST. Every assertion is
// load-bearing for a distinct regression:
//
//   - "no pane after the stale arrival" — without the guard it installs the
//     OUTGOING project's session, which is not in m.sessions.
//   - "old connection closed" — dropping without closing leaks the old host's
//     single client slot with no handle left anywhere to release it.
//   - "attachInFlight survives the stale arrival" — the stale message matches
//     the NEW dial's record by name+preview, so a guard placed after the clear
//     would let it steal the record (and its joined focus) mid-dial.
//   - "the fresh arrival installs and keeps ITS pane" — without the guard the
//     stale pane installs first and the same-name surplus branch then closes
//     the NEW connection and keeps the old, routing `f` keystrokes into a
//     different project's Claude session.
func TestTogglePaneDialOutstandingAcrossAProjectSwitchIsDropped(t *testing.T) {
	oldPane := newFakePane("OLD")
	newPane := newFakePane("NEW")
	d := testDeps()
	d.Projects = scannedProjects("/roots/a", "/roots/b")
	d.ActiveProjectIndex = 0
	d.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return oldPane, nil }
	m := New("/roots/a", time.Second, d)
	m.ready, m.width, m.height = true, 120, 40

	oldSess := runningSess("same")
	oldSess.WorktreePath = "/roots/a/wt/same"
	m.setSessions([]Session{oldSess})
	m, staleCmd := update(t, m, keyEnter)
	if staleCmd == nil {
		t.Fatal("precondition: enter on a running session must dispatch a dial")
	}

	m, _ = m.switchProject("/roots/b")

	newSess := runningSess("same")
	newSess.WorktreePath = "/roots/b/wt/same"
	m.setSessions([]Session{newSess})
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) { return newPane, nil }
	m, freshCmd := update(t, m, keyEnter)
	if freshCmd == nil {
		t.Fatal("precondition: the re-dial in the new project must dispatch")
	}

	m, _ = update(t, m, drainForPaneAttached(t, staleCmd))
	if m.pane != nil {
		t.Fatal("the stale arrival installed a pane for the outgoing project's session")
	}
	if oldPane.closed != 1 {
		t.Errorf("stale connection closed %d times, want 1 — dropping without closing leaks the old host's client slot", oldPane.closed)
	}
	if m.attachInFlight.name != "same" {
		t.Error("the stale arrival cleared the incoming project's in-flight dial record")
	}

	m, _ = update(t, m, drainForPaneAttached(t, freshCmd))
	if m.pane != newPane {
		t.Fatal("the new project's connection is not the installed pane — keystrokes would route into the old project's session")
	}
	if newPane.closed != 0 {
		t.Errorf("new connection closed %d times, want 0 — the surplus branch treated it as the extra", newPane.closed)
	}
	if m.paneName != "same" || !m.paneOwnsKeyboard() {
		t.Errorf("paneName=%q focused=%v, want the fresh enter's pane installed and focused", m.paneName, m.paneOwnsKeyboard())
	}
}

// TestRestartOutstandingAcrossAProjectSwitchIsDropped covers the chain the
// arrival guard alone cannot: a hostRestartedMsg is consumed by Update, which
// REBUILDS the pane request, and openPaneFor stamps rebuilt requests with the
// CURRENT generation — so without the drop in the hostRestartedMsg case, a
// pre-switch restart would be laundered into a post-switch open that sails past
// applyPaneAttached. The load-bearing assertions are "no Cmd" and "the dial was
// never placed"; the status assertion pins that a stale restart says nothing
// about a session the current list does not show.
func TestRestartOutstandingAcrossAProjectSwitchIsDropped(t *testing.T) {
	d := testDeps()
	d.Projects = scannedProjects("/roots/a", "/roots/b")
	d.ActiveProjectIndex = 0
	m := New("/roots/a", time.Second, d)
	m.ready, m.width, m.height = true, 120, 40

	stopped := sess("same")
	stopped.WorktreePath = "/roots/a/wt/same"
	m.setSessions([]Session{stopped})
	m, restartCmd := pressAction(t, m, keyRune('r'))
	if restartCmd == nil {
		t.Fatal("precondition: r on a stopped session must dispatch the restart")
	}

	m, _ = m.switchProject("/roots/b")
	m.status = ""
	dialed := false
	m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		dialed = true
		return newFakePane("X"), nil
	}

	var restarted *hostRestartedMsg
	for _, msg := range collectMsgs(t, restartCmd) {
		if hr, ok := msg.(hostRestartedMsg); ok {
			restarted = &hr
		}
	}
	if restarted == nil {
		t.Fatal("precondition: the restart Cmd must report a hostRestartedMsg")
	}

	m, cmd := update(t, m, *restarted)
	if cmd != nil {
		t.Error("a stale restart must chain into nothing — the Cmd it returns is a dial for the outgoing project")
	}
	runAllCmds(t, cmd)
	if dialed {
		t.Error("a stale restart placed a dial for the outgoing project's session")
	}
	if m.attachInFlight.name != "" {
		t.Errorf("attachInFlight = %+v, want empty — nothing was legitimately opened", m.attachInFlight)
	}
	if m.status != "" {
		t.Errorf("status = %q, want silence about a session the current list does not show", m.status)
	}
}

// TestRestartInSwitchedToProjectChainsIntoThePaneOpen is the SUCCESS direction
// TestRestartOutstandingAcrossAProjectSwitchIsDropped cannot see: `r` on a
// stopped session pressed AFTER a project switch, whose restart must sail
// through the generation guard in Update's hostRestartedMsg case and chain into
// the pane open. It exists to pin the `req.gen = m.projectGen` stamp in
// restartHostCmd — with the stamp deleted the request carries gen 0 while a
// switched-to dashboard's projectGen is >= 1, so the guard drops the user's own
// legitimate restart and the keypress silently does nothing. The load-bearing
// assertions are the non-nil chain Cmd out of hostRestartedMsg and the pane
// actually installing: any test that stops at "a hostRestartedMsg was produced"
// passes with the stamp gone. The projectGen precondition is load-bearing too —
// at generation 0 a missing stamp is indistinguishable from a present one, which
// is exactly why every launch-project restart test stays green under the
// mutation.
func TestRestartInSwitchedToProjectChainsIntoThePaneOpen(t *testing.T) {
	d := testDeps()
	d.Projects = scannedProjects("/roots/a", "/roots/b")
	d.ActiveProjectIndex = 0
	m := New("/roots/a", time.Second, d)
	m.ready, m.width, m.height = true, 120, 40

	m, _ = m.switchProject("/roots/b")
	if m.projectGen == 0 {
		t.Fatal("precondition: the switch must move projectGen past the zero value an unstamped request carries")
	}

	stopped := sess("target") // HostRunning is false
	stopped.WorktreePath = "/roots/b/wt/target"
	m.setSessions([]Session{stopped})

	var spawnedName, spawnedWorktree string
	m.deps.SpawnHost = func(name, worktree string) (session.HostProcess, error) {
		spawnedName, spawnedWorktree = name, worktree
		return session.HostProcess{PID: 4242}, nil
	}
	fp := newFakePane("B-PANE")
	var gotCfg hostPaneConfig
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		gotCfg = cfg
		return fp, nil
	}

	m, restartCmd := pressAction(t, m, keyRune('r'))
	if restartCmd == nil {
		t.Fatal("precondition: r on a stopped session must dispatch the restart")
	}
	restarted := drainForHostRestarted(t, restartCmd)
	if restarted.err != nil {
		t.Fatalf("hostRestartedMsg.err = %v, want a clean spawn", restarted.err)
	}
	if spawnedName != "target" || spawnedWorktree != "/roots/b/wt/target" {
		t.Fatalf("SpawnHost called with (%q, %q), want the switched-to project's (%q, %q)",
			spawnedName, spawnedWorktree, "target", "/roots/b/wt/target")
	}

	m, cmd := update(t, m, restarted)
	if cmd == nil {
		t.Fatal("the restart was dropped as stale in the project that dispatched it — r on a stopped session silently did nothing")
	}
	attached := drainForPaneAttached(t, cmd)
	if attached.focusOnArrival || !attached.selectionBound {
		t.Errorf("list r's focusOnArrival=false/selectionBound=true was lost across the restart chain: %+v", attached)
	}
	if gotCfg.Name != "target" || gotCfg.Worktree != "/roots/b/wt/target" {
		t.Fatalf("opened %+v, want the switched-to project's target worktree", gotCfg)
	}

	m, _ = update(t, m, attached)
	if m.pane != fp || m.paneName != "target" || m.paneOwnsKeyboard() {
		t.Fatalf("pane=%v name=%q focused=%v, want the restarted session's pane installed with list ownership",
			m.pane == fp, m.paneName, m.paneOwnsKeyboard())
	}
}

// TestScanErrorStillHealsTheRepoSpelling is the config-load-error half of
// TestScanHealsTheRepoSpellingForASubdirectoryLaunch: the heal reads the list
// the Model already holds, not anything the scan produced, so a pass whose
// config never loaded must settle m.repo all the same — otherwise a
// subdirectory launch with a broken config keeps the divergent spelling for
// the life of the process.
func TestScanErrorStillHealsTheRepoSpelling(t *testing.T) {
	m := projectModel("/repo/subdir", 0, pinnedProject("/repo"))

	nm, _ := update(t, m, projectScanMsg{err: errors.New("boom"), configPath: "/cfg/config.toml"})
	if nm.repo != "/repo" {
		t.Fatalf("repo = %q after a failed scan, want the active entry's normalized %q", nm.repo, "/repo")
	}
	if nm.status == "" {
		t.Fatal("the scan failure itself must still surface in the footer")
	}
}
