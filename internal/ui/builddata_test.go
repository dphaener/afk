package ui

import (
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/state"
)

// buildSnap is a snapshot a test can tell apart from the zero value and from
// another project's.
func buildSnap(root string) buildview.Snapshot {
	return buildview.Snapshot{
		Root:     root,
		HasBuild: true,
		Rows:     []buildview.Row{{ID: "T1.1", Title: "First", Status: "done"}},
		Done:     1,
		Total:    1,
	}
}

// buildModel is a two-project Model whose loader answers with the folder it
// was asked about, and counts its calls.
func buildModel(t *testing.T, calls *int) Model {
	t.Helper()
	d := testDeps()
	d.Projects = project.ProjectsFromPaths([]string{"/alpha", "/beta"})
	d.ActiveProjectIndex = 0
	d.LoadBuild = func(repo string, _ []BuildSession) buildview.Snapshot {
		if calls != nil {
			*calls++
		}
		return buildSnap(repo)
	}
	m := New("/alpha", time.Second, d)
	m.ready = true
	m.width = 120
	m.height = 40
	return m
}

// TestBuildLoadedMsg is the table for what Update does with a finished build
// read. The load-bearing rows:
//
//   - "stale gen dropped" is the one a project switch depends on. A read
//     cannot be cancelled once issued, so without the generation check the
//     outgoing project's tasks and inbox would land under the incoming
//     project's name, and an answer typed there would go to the wrong inbox.
//   - "project switch clears the snapshot" is the other half: the check above
//     only stops a late arrival, it does not remove what is already held.
//   - "a newer read replaces the older one" pins that the snapshot is replaced
//     whole and never merged, so a task that left the plan leaves the screen.
func TestBuildLoadedMsg(t *testing.T) {
	tests := []struct {
		name string
		// run drives the model and returns it for the checks below.
		run       func(t *testing.T, m Model) Model
		wantKnown bool
		wantRoot  string
		wantRows  int
	}{
		{
			name: "matching gen stored",
			run: func(t *testing.T, m Model) Model {
				m, _ = update(t, m, buildLoadedMsg{snap: buildSnap("/alpha"), gen: m.projectGen})
				return m
			},
			wantKnown: true,
			wantRoot:  "/alpha",
			wantRows:  1,
		},
		{
			name: "stale gen dropped",
			run: func(t *testing.T, m Model) Model {
				m, _ = update(t, m, buildLoadedMsg{snap: buildSnap("/alpha"), gen: m.projectGen - 1})
				return m
			},
			wantKnown: false,
		},
		{
			name: "a newer read replaces the older one",
			run: func(t *testing.T, m Model) Model {
				m, _ = update(t, m, buildLoadedMsg{snap: buildSnap("/alpha"), gen: m.projectGen})
				m, _ = update(t, m, buildLoadedMsg{snap: buildview.Snapshot{Root: "/alpha"}, gen: m.projectGen})
				return m
			},
			wantKnown: true,
			wantRoot:  "/alpha",
			wantRows:  0,
		},
		{
			name: "project switch clears the snapshot",
			run: func(t *testing.T, m Model) Model {
				m, _ = update(t, m, buildLoadedMsg{snap: buildSnap("/alpha"), gen: m.projectGen})
				if !m.buildKnown {
					t.Fatal("precondition: the snapshot was not stored before the switch")
				}
				m, _ = m.switchProject("/beta")
				return m
			},
			wantKnown: false,
		},
		{
			name: "a read started before the switch is dropped after it",
			run: func(t *testing.T, m Model) Model {
				before := m.buildCmd()
				m, _ = m.switchProject("/beta")
				m, _ = update(t, m, before())
				return m
			},
			wantKnown: false,
		},
		{
			name: "the first read after the switch is the new project's",
			run: func(t *testing.T, m Model) Model {
				m, _ = update(t, m, buildLoadedMsg{snap: buildSnap("/alpha"), gen: m.projectGen})
				m, cmds := m.switchProjectCmds("/beta")
				for _, msg := range drainCmds(cmds) {
					if b, ok := msg.(buildLoadedMsg); ok {
						m, _ = update(t, m, b)
					}
				}
				return m
			},
			wantKnown: true,
			wantRoot:  "/beta",
			wantRows:  1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.run(t, buildModel(t, nil))
			if m.buildKnown != tc.wantKnown {
				t.Fatalf("buildKnown = %v, want %v", m.buildKnown, tc.wantKnown)
			}
			if m.build.Root != tc.wantRoot {
				t.Errorf("build.Root = %q, want %q", m.build.Root, tc.wantRoot)
			}
			if len(m.build.Rows) != tc.wantRows {
				t.Errorf("build has %d rows, want %d", len(m.build.Rows), tc.wantRows)
			}
			if !tc.wantKnown && !reflect.DeepEqual(m.build, buildview.Snapshot{}) {
				t.Errorf("build = %+v, want the zero snapshot when nothing is known", m.build)
			}
		})
	}
}

// drainCmds runs each command and returns what they produced, skipping the
// ones that return nothing.
func drainCmds(cmds []tea.Cmd) []tea.Msg {
	var out []tea.Msg
	for _, c := range cmds {
		out = append(out, drainCmd(c)...)
	}
	return out
}

// TestBuildCmdIssued is the table for when a build read is issued at all. The
// load-bearing rows:
//
//   - "nil seam" is what keeps every other test in this package, and a
//     dashboard built with zero Deps, from reading a real project: no loader
//     means no command, not a command that returns an empty snapshot.
//   - "no active project" is a dashboard started outside any repository, which
//     has no folder to read.
//   - "start, tick and switch" are the three places the plan names. Each goes
//     through projectCmds, so this row fails if the read is ever moved to only
//     one of them.
func TestBuildCmdIssued(t *testing.T) {
	t.Run("nil seam issues no command", func(t *testing.T) {
		m := testModel()
		if m.deps.LoadBuild != nil {
			t.Fatal("precondition: testDeps must leave LoadBuild nil")
		}
		if cmd := m.buildCmd(); cmd != nil {
			t.Fatal("buildCmd() returned a command with no loader wired")
		}
		if got := len(m.projectCmds()); got != 3 {
			t.Fatalf("projectCmds() has %d commands with no loader, want the usual 3", got)
		}
		if hasMsg[buildLoadedMsg](drainCmds(m.projectCmds())) {
			t.Fatal("a build read was issued with no loader wired")
		}
	})

	t.Run("withDefaults leaves both seams nil", func(t *testing.T) {
		d := Deps{}.withDefaults()
		if d.LoadBuild != nil {
			t.Error("withDefaults set LoadBuild; it reads real files and must stay opt-in")
		}
		if d.AnswerInbox != nil {
			t.Error("withDefaults set AnswerInbox; it writes a real inbox and must stay opt-in")
		}
	})

	t.Run("no active project issues no command", func(t *testing.T) {
		calls := 0
		d := testDeps()
		d.ActiveProjectIndex = -1
		d.LoadBuild = func(string, []BuildSession) buildview.Snapshot { calls++; return buildview.Snapshot{} }
		m := New("", time.Second, d)
		drainCmds(m.projectCmds())
		if calls != 0 {
			t.Fatalf("the loader ran %d times with no active project, want 0", calls)
		}
	})

	t.Run("start, tick and switch each read once", func(t *testing.T) {
		calls := 0
		m := buildModel(t, &calls)

		if !hasMsg[buildLoadedMsg](collectMsgs(t, m.Init())) {
			t.Fatal("Init issued no build read")
		}
		if calls != 1 {
			t.Fatalf("after Init the loader ran %d times, want 1", calls)
		}

		_, tickCmd := update(t, m, tickMsg(time.Unix(2_000_000, 0)))
		if !hasMsg[buildLoadedMsg](collectMsgs(t, tickCmd)) {
			t.Fatal("a tick issued no build read")
		}
		if calls != 2 {
			t.Fatalf("after one tick the loader ran %d times, want 2", calls)
		}

		_, cmds := m.switchProjectCmds("/beta")
		var got buildLoadedMsg
		for _, msg := range drainCmds(cmds) {
			if b, ok := msg.(buildLoadedMsg); ok {
				got = b
			}
		}
		if got.snap.Root != "/beta" {
			t.Fatalf("the read after a switch was for %q, want /beta", got.snap.Root)
		}
		if calls != 3 {
			t.Fatalf("after the switch the loader ran %d times, want 3", calls)
		}
	})
}

// TestBuildCmdGuard pins the in-flight guard. Both rows are load-bearing: the
// first is the point of the guard (a read that outlasts the tick must not have
// a second one stacked behind it), and the second is what stops the guard from
// becoming a permanent "no build data" once the slow read ends.
func TestBuildCmdGuard(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	m := buildModel(t, nil)
	m.deps.LoadBuild = func(repo string, _ []BuildSession) buildview.Snapshot {
		calls++
		if calls == 1 {
			close(started)
			<-release
		}
		return buildSnap(repo)
	}

	first := make(chan tea.Msg, 1)
	go func() { first <- m.buildCmd()() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first read never started")
	}

	t.Run("a second read is held back while one is running", func(t *testing.T) {
		if msg := m.buildCmd()(); msg != nil {
			t.Fatalf("a second read ran while the first was in flight: %#v", msg)
		}
	})

	// A switch must not leave the new project waiting on the old project's
	// slow read: it gets a guard of its own.
	t.Run("a project switch is not held back by the old read", func(t *testing.T) {
		switched, _ := m.switchProject("/beta")
		msg, ok := switched.buildCmd()().(buildLoadedMsg)
		if !ok || msg.snap.Root != "/beta" {
			t.Fatalf("the new project's first read = %#v, want a snapshot for /beta", msg)
		}
	})

	close(release)
	select {
	case msg := <-first:
		if _, ok := msg.(buildLoadedMsg); !ok {
			t.Fatalf("the first read returned %#v, want a buildLoadedMsg", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first read never returned")
	}

	t.Run("the guard is released when the read ends", func(t *testing.T) {
		if msg := m.buildCmd()(); msg == nil {
			t.Fatal("a read after the first one finished was dropped too")
		}
	})

	t.Run("a nil guard still reads", func(t *testing.T) {
		bare := m
		bare.buildRefreshing = nil
		if msg := bare.buildCmd()(); msg == nil {
			t.Fatal("buildCmd() with a nil guard returned no message")
		}
	})
}

// TestBuildSessions pins what the loader is told about each row. The one-off
// row is load-bearing: it has no status.json, so its phase is empty without
// being stale, and passing it on as fresh would show a running task with a
// blank where its phase should be. The placeholder row is too: only its name
// is filled in, and it must not be reported as a session with no host.
func TestBuildSessions(t *testing.T) {
	executing := Session{SessionState: discover.SessionState{
		Name: "t1-1", Phase: state.PhaseExecuting, AwaitingInput: true, HostRunning: true,
	}}
	stale := Session{SessionState: discover.SessionState{Name: "t1-2", StatusStale: true}}
	oneOff := Session{SessionState: discover.SessionState{Name: "scratch", Scratch: true, HostRunning: true}}
	placeholder := Session{SessionState: discover.SessionState{Name: "soon"}, Pending: true}

	tests := []struct {
		name     string
		sessions []Session
		want     []BuildSession
	}{
		{name: "no sessions", sessions: nil, want: nil},
		{
			name:     "a session with a phase",
			sessions: []Session{executing},
			want:     []BuildSession{{Name: "t1-1", Phase: "executing", AwaitingInput: true, HostRunning: true}},
		},
		{
			name:     "a stale status",
			sessions: []Session{stale},
			want:     []BuildSession{{Name: "t1-2", Stale: true}},
		},
		{
			name:     "a one-off session reads as stale",
			sessions: []Session{oneOff},
			want:     []BuildSession{{Name: "scratch", Stale: true, HostRunning: true}},
		},
		{
			name:     "a placeholder row is left out",
			sessions: []Session{placeholder, executing},
			want:     []BuildSession{{Name: "t1-1", Phase: "executing", AwaitingInput: true, HostRunning: true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel()
			m.sessions = tc.sessions
			if got := m.buildSessions(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("buildSessions() = %+v, want %+v", got, tc.want)
			}
		})
	}

	// The rows reach the loader, and as a copy: the closure runs on another
	// goroutine while Update is free to replace the Model's slice.
	t.Run("the loader is handed the rows", func(t *testing.T) {
		var got []BuildSession
		m := buildModel(t, nil)
		m.deps.LoadBuild = func(_ string, s []BuildSession) buildview.Snapshot {
			got = s
			return buildview.Snapshot{}
		}
		m.sessions = []Session{executing}
		cmd := m.buildCmd()
		m.sessions[0].Name = "changed-after-the-command-was-built"
		cmd()
		if len(got) != 1 || got[0].Name != "t1-1" {
			t.Fatalf("the loader got %+v, want the one row as it was when the command was built", got)
		}
	})
}
