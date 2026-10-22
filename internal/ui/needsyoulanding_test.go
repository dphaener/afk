package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/state"
)

// needsWorld is a dashboard on /repo whose switcher knows /repo, /p/a and
// /p/b. Each listed project's sessions are what its scan cache holds and what
// its discovery returns, so a switch loads the same sessions the order was
// built from. Rows are the every-project cache and what the home screen
// reads. Opened records every pane dial.
type needsWorld struct {
	m      Model
	opened []hostPaneConfig
	panes  map[string]*fakePane
}

// waitingState is a running session waiting on the owner.
func waitingState(name string) discover.SessionState {
	st := sampleState(name)
	st.AwaitingInput = true
	st.HostRunning = true
	st.HostPID = 4242
	return st
}

// needsRow is a plain home-screen row for root, listed at order.
func needsRow(name, root string, order int, edit func(*projectrows.Input)) projectrows.Row {
	in := projectrows.Input{Name: name, Root: root, Listed: true, Order: order, HasPlan: true, Done: 1, Total: 3}
	if edit != nil {
		edit(&in)
	}
	return projectrows.Build(in)
}

func newNeedsWorld(t *testing.T, sessions map[string][]discover.SessionState, rows []projectrows.Row) *needsWorld {
	t.Helper()
	w := &needsWorld{panes: map[string]*fakePane{}}
	m := testModel()
	m.projects = []project.Project{
		{Path: "/repo", Name: "repo", Pinned: true},
		{Path: "/p/a", Name: "a"},
		{Path: "/p/b", Name: "b"},
	}
	m.activeProject = 0
	m.projectSessions = map[string][]discover.SessionState{}
	for path, states := range sessions {
		if path != "/repo" {
			m.projectSessions[path] = states
		}
	}
	var home []Session
	for _, st := range sessions["/repo"] {
		home = append(home, Session{SessionState: st})
	}
	m.setSessions(home)
	m.deps.Discover = func(repo string) ([]discover.SessionState, error) { return sessions[repo], nil }
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		w.opened = append(w.opened, cfg)
		p := newFakePane(cfg.Name)
		w.panes[cfg.Name] = p
		return p, nil
	}
	seam := &allProjectsSeam{res: AllProjects{Rows: rows, ConfigPath: "/cfg/config.toml"}}
	m.deps.LoadAllProjects = seam.load
	m.everyProject = AllProjects{Rows: rows}
	w.m = m
	return w
}

// settle lands what cmd produced that moves the dashboard: the session loads
// first, then the pane arrivals, as a real switch sees them (the load is a
// file read, the dial a socket wait).
func (w *needsWorld) settle(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	msgs := collectMsgs(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(sessionsLoadedMsg); ok {
			w.m, _ = update(t, w.m, msg)
		}
	}
	for _, msg := range msgs {
		if _, ok := msg.(paneAttachedMsg); ok {
			w.m, _ = update(t, w.m, msg)
		}
	}
}

// press presses k where the dashboard is (the leader first in the Agent) and
// settles what it started.
func (w *needsWorld) press(t *testing.T, k tea.Msg) tea.Cmd {
	t.Helper()
	var cmd tea.Cmd
	w.m, cmd = pressAction(t, w.m, k)
	w.settle(t, cmd)
	return cmd
}

func (w *needsWorld) openHome(t *testing.T, root string) {
	t.Helper()
	w.m = cursorOn(t, openAllProjects(t, w.m), root)
}

var (
	keyNeedNext = keyRune(']')
	keyNeedPrev = keyRune('[')
)

// TestNeedsYouLandingAcceptancePath is the plan's acceptance path, end to
// end through Update: two projects each with one waiting session. From
// project A's row on All projects `]` opens A's agent (a dial for A's
// worktree), `]` from that Agent (after the leader) opens B's, and `[` goes
// back to A's. Every step is load-bearing: the first proves a project row
// steps into its own first item, the second that the leader then `]` lands
// on another project's agent with the keyboard on it, the third the reverse
// direction.
func TestNeedsYouLandingAcceptancePath(t *testing.T) {
	sessions := map[string][]discover.SessionState{
		"/p/a": {waitingState("sa")},
		"/p/b": {waitingState("sb")},
	}
	rows := []projectrows.Row{needsRow("a", "/p/a", 0, nil), needsRow("b", "/p/b", 1, nil)}
	w := newNeedsWorld(t, sessions, rows)
	w.openHome(t, "/p/a")

	steps := []struct {
		name     string
		key      tea.Msg
		repo     string
		pane     string
		worktree string
	}{
		{"] from A's row opens A's agent", keyNeedNext, "/p/a", "sa", "/wt/sa"},
		{"] from A's agent opens B's", keyNeedNext, "/p/b", "sb", "/wt/sb"},
		{"[ from B's agent goes back to A's", keyNeedPrev, "/p/a", "sa", "/wt/sa"},
	}
	for i, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			w.press(t, st.key)
			if w.m.repo != st.repo {
				t.Fatalf("repo = %q, want %q", w.m.repo, st.repo)
			}
			if len(w.opened) != i+1 || w.opened[i].Name != st.pane || w.opened[i].Worktree != st.worktree {
				t.Fatalf("dials = %+v, want dial %d for %s at %s", w.opened, i+1, st.pane, st.worktree)
			}
			if w.m.level() != levelAgent || w.m.paneName != st.pane || !w.m.paneOwnsKeyboard() {
				t.Fatalf("level = %v, pane = %q, owns keyboard = %v; want the Agent on %s with the keyboard",
					w.m.level(), w.m.paneName, w.m.paneOwnsKeyboard(), st.pane)
			}
			if w.m.previewOnNextLoad {
				t.Error("an agent landing armed a preview, which would race a second dial")
			}
		})
	}
}

// TestNeedsYouLandingKinds pins where each kind lands (A3), from All
// projects with the cursor on the home project's row. Load-bearing rows:
// "a blocking item" (switches with the follow set after the switch, which
// clears it), "a plan to approve" (Project level, with enter's preview
// armed), "a broken session" (the session selected on the first load, no
// preview), "a gone project" (stays on All projects with its row selected
// and the notice, nothing switched), "a waiting session in the active
// project" (selected and connected without a switch) and "a stopped
// waiting session elsewhere" (its details over the Project, no dial).
func TestNeedsYouLandingKinds(t *testing.T) {
	failed := sampleState("broke")
	failed.Phase = state.PhaseFailed
	stopped := waitingState("asleep")
	stopped.HostRunning = false
	stopped.HostPID = 0

	cases := []struct {
		name     string
		sessions map[string][]discover.SessionState
		rows     []projectrows.Row
		check    func(t *testing.T, w *needsWorld)
	}{
		{
			name: "a blocking item opens its project's inbox on it",
			rows: []projectrows.Row{needsRow("b", "/p/b", 1, func(in *projectrows.Input) {
				in.Open, in.Blocking = 1, 1
				in.Wait = projectrows.Wait{ItemID: "q1", Kind: "question", CreatedAt: "2026-10-20T09:00:00Z", Title: "Which?"}
			})},
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/p/b" || w.m.inboxFollow != "q1" || w.m.allProjectsOpen {
					t.Fatalf("repo = %q, follow = %q, screen open = %v; want /p/b following q1", w.m.repo, w.m.inboxFollow, w.m.allProjectsOpen)
				}
			},
		},
		{
			name: "a plan to approve opens its project",
			rows: []projectrows.Row{needsRow("b", "/p/b", 1, func(in *projectrows.Input) { in.HasPlan = false; in.PlanWaiting = true })},
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/p/b" || w.m.level() != levelProject {
					t.Fatalf("repo = %q, level = %v; want /p/b at Project", w.m.repo, w.m.level())
				}
				if len(w.opened) != 0 {
					t.Fatalf("dials = %+v, want none before the preview's own timer", w.opened)
				}
			},
		},
		{
			name:     "a broken session opens its project with it selected",
			sessions: map[string][]discover.SessionState{"/p/b": {sampleState("fine"), failed}},
			rows:     []projectrows.Row{needsRow("b", "/p/b", 1, nil)},
			check: func(t *testing.T, w *needsWorld) {
				s, ok := w.m.selected()
				if w.m.repo != "/p/b" || w.m.level() != levelProject || !ok || s.Name != "broke" {
					t.Fatalf("repo = %q, level = %v, selected = %q; want /p/b at Project on broke", w.m.repo, w.m.level(), s.Name)
				}
				if w.m.previewOnNextLoad || len(w.opened) != 0 {
					t.Fatalf("preview armed = %v, dials = %+v; want neither", w.m.previewOnNextLoad, w.opened)
				}
			},
		},
		{
			name: "a gone project stays on All projects with its row and notice",
			rows: []projectrows.Row{
				projectrows.Build(projectrows.Input{Name: "b", Root: "/p/b", Listed: true, Order: 1, Gone: true}),
				needsRow("repo", "/repo", 2, nil),
			},
			check: func(t *testing.T, w *needsWorld) {
				r, ok := w.m.selectedProjectRow()
				if w.m.repo != "/repo" || !w.m.allProjectsOpen || !ok || r.Root != "/p/b" {
					t.Fatalf("repo = %q, screen open = %v, row = %q; want All projects on /p/b", w.m.repo, w.m.allProjectsOpen, r.Root)
				}
				if w.m.status != allProjectsGoneNotice("b") {
					t.Fatalf("status = %q, want the gone notice", w.m.status)
				}
			},
		},
		{
			name:     "a waiting session in the active project is selected and connected",
			sessions: map[string][]discover.SessionState{"/repo": {sampleState("calm"), waitingState("ask")}},
			rows:     []projectrows.Row{needsRow("repo", "/repo", 2, nil)},
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/repo" || w.m.allProjectsOpen || w.m.paneName != "ask" || !w.m.paneOwnsKeyboard() {
					t.Fatalf("repo = %q, screen open = %v, pane = %q; want /repo's agent on ask", w.m.repo, w.m.allProjectsOpen, w.m.paneName)
				}
			},
		},
		{
			name:     "a stopped waiting session elsewhere opens its details",
			sessions: map[string][]discover.SessionState{"/p/b": {stopped}},
			rows:     []projectrows.Row{needsRow("b", "/p/b", 1, nil)},
			check: func(t *testing.T, w *needsWorld) {
				if w.m.repo != "/p/b" || !w.m.detailsOpen || w.m.detailsFor != "asleep" || len(w.opened) != 0 {
					t.Fatalf("repo = %q, details open = %v on %q, dials = %d; want /p/b's details on asleep and no dial",
						w.m.repo, w.m.detailsOpen, w.m.detailsFor, len(w.opened))
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newNeedsWorld(t, c.sessions, c.rows)
			w.openHome(t, c.rows[len(c.rows)-1].Root)
			w.press(t, keyNeedNext)
			c.check(t, w)
		})
	}
}

// TestNeedsYouLandingNothingNeedsYou pins the empty list: `]` and `[` say so
// in a notice and change nothing, at every level (no switch, no dial, the
// level unchanged). Load-bearing because a key that silently did nothing
// would read as broken.
func TestNeedsYouLandingNothingNeedsYou(t *testing.T) {
	sessions := map[string][]discover.SessionState{"/repo": {runningSess("calm").SessionState}}
	rows := []projectrows.Row{needsRow("a", "/p/a", 0, nil), needsRow("repo", "/repo", 2, nil)}
	for _, k := range []tea.Msg{keyNeedNext, keyNeedPrev} {
		for _, home := range []bool{true, false} {
			w := newNeedsWorld(t, sessions, rows)
			if home {
				w.openHome(t, "/p/a")
			}
			before := w.m.level()
			cmd := w.press(t, k)
			if w.m.status != nothingNeedsYouNotice {
				t.Errorf("home=%v: status = %q, want %q", home, w.m.status, nothingNeedsYouNotice)
			}
			if cmd != nil || w.m.repo != "/repo" || w.m.level() != before || len(w.opened) != 0 {
				t.Errorf("home=%v: cmd = %v, repo = %q, level %v -> %v, dials = %d; want nothing changed",
					home, cmd != nil, w.m.repo, before, w.m.level(), len(w.opened))
			}
		}
	}
}

// TestNeedsYouLandingKeepsTheLeaderRule pins that `]` is an afk key in the
// Agent only after the leader: alone it goes to the agent, as every key but
// the leader does, and nothing switches. Load-bearing because the agent must
// keep `]` and `[` (a person types them into code).
func TestNeedsYouLandingKeepsTheLeaderRule(t *testing.T) {
	sessions := map[string][]discover.SessionState{
		"/repo": {waitingState("here")},
		"/p/b":  {waitingState("sb")},
	}
	w := newNeedsWorld(t, sessions, []projectrows.Row{needsRow("b", "/p/b", 1, nil)})
	w.press(t, keyEnter)
	if w.m.level() != levelAgent || w.m.paneName != "here" {
		t.Fatalf("setup: level = %v, pane = %q; want the Agent on here", w.m.level(), w.m.paneName)
	}
	var cmd tea.Cmd
	w.m, cmd = update(t, w.m, keyNeedNext)
	w.settle(t, cmd)
	if w.m.repo != "/repo" || w.m.paneName != "here" || len(w.opened) != 1 {
		t.Fatalf("repo = %q, pane = %q, dials = %d; want ] typed into the agent", w.m.repo, w.m.paneName, len(w.opened))
	}
	if p := w.panes["here"]; len(p.keys)+len(p.texts) != 1 || (len(p.texts) == 1 && p.texts[0] != "]") ||
		(len(p.keys) == 1 && p.keys[0].Code != ']') {
		t.Fatalf("agent got keys %+v, text %q; want the one ]", p.keys, p.texts)
	}
	w.press(t, keyNeedNext)
	if w.m.repo != "/p/b" || w.m.paneName != "sb" {
		t.Fatalf("after the leader: repo = %q, pane = %q; want b's agent", w.m.repo, w.m.paneName)
	}
}

// TestNeedsYouLandingStepsPastAPlan pins needAt (W1.T1's needHere.At): after
// `]` lands on a plan to approve, the project view selects no plan, so the
// next `]` must go past the plan instead of landing on it again. The second
// row proves a cursor move forgets the landing: from the moved-to session,
// `]` starts again from there.
func TestNeedsYouLandingStepsPastAPlan(t *testing.T) {
	sessions := map[string][]discover.SessionState{
		"/p/a": {waitingState("sa"), runningSess("calm").SessionState},
		"/p/b": {waitingState("sb")},
	}
	rows := []projectrows.Row{
		needsRow("a", "/p/a", 0, func(in *projectrows.Input) { in.HasPlan = false; in.PlanWaiting = true }),
		needsRow("b", "/p/b", 1, nil),
	}

	t.Run("the next ] goes past the plan", func(t *testing.T) {
		w := newNeedsWorld(t, sessions, rows)
		w.press(t, keyNeedNext) // /repo has nothing: the top of the list, A's sa
		w.press(t, keyNeedNext) // A's plan, after the leader
		if w.m.repo != "/p/a" || w.m.level() != levelProject {
			t.Fatalf("setup: repo = %q, level = %v; want A's plan at Project", w.m.repo, w.m.level())
		}
		w.press(t, keyNeedNext)
		if w.m.repo != "/p/b" || w.m.paneName != "sb" {
			t.Fatalf("repo = %q, pane = %q; want b's agent past the plan", w.m.repo, w.m.paneName)
		}
	})
	t.Run("a cursor move forgets the landing", func(t *testing.T) {
		w := newNeedsWorld(t, sessions, rows)
		w.press(t, keyNeedNext)
		w.press(t, keyNeedNext)
		if w.m.needAt == (needKey{}) {
			t.Fatal("setup: the plan landing left no needAt")
		}
		w.m, _ = update(t, w.m, keyDown)
		if w.m.needAt != (needKey{}) {
			t.Fatalf("needAt = %+v after a cursor move, want it cleared", w.m.needAt)
		}
	})
}

// TestNeedsYouNowOrder pins the Model's one builder: the active project's
// sessions come from m.sessions (fresh), not its stale scan-cache entry, and
// rows keyed by the pinned start-up row's main checkout are the switcher's
// path, so a landing can switch to them. Load-bearing because the band, the
// top bar and the keys all read this one method.
func TestNeedsYouNowOrder(t *testing.T) {
	w := newNeedsWorld(t, map[string][]discover.SessionState{
		"/repo": {waitingState("fresh")},
		"/p/a":  {waitingState("sa")},
	}, []projectrows.Row{needsRow("a", "/p/a", 0, nil)})
	w.m.projectSessions["/repo"] = []discover.SessionState{waitingState("stale")}
	var got []string
	for _, it := range w.m.needsYouNow() {
		got = append(got, it.String())
	}
	want := "session/a/sa,session/repo/fresh"
	if strings.Join(got, ",") != want {
		t.Fatalf("needsYouNow = %v, want %s", got, want)
	}
}
