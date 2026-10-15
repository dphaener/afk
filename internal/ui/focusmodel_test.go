package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/session"
)

// scenarioHarness is the fake-seam rig behind TestFocusScenario. Both seams
// only COUNT: a fake that called t.Fatal would Goexit on a tea.Batch goroutine
// and park the Cmd collector forever, so every assertion happens in the test
// body, on the counters, after Update returns.
type scenarioHarness struct {
	t      *testing.T
	m      Model
	opens  int
	spawns int
	panes  []*fakePane

	// gens records previewGen after every step, so a later step can fire the
	// preview timer an earlier cursor move armed.
	gens []int
}

func newScenarioHarness(t *testing.T, rows ...Session) *scenarioHarness {
	t.Helper()
	h := &scenarioHarness{t: t, m: testModel(rows...)}
	h.m.deps.SpawnHost = func(string, string) (session.HostProcess, error) {
		h.spawns++
		return session.HostProcess{PID: 4242}, nil
	}
	h.m.deps.OpenHostPane = func(hostPaneConfig) (paneHandle, error) {
		h.opens++
		p := newFakePane("agent")
		h.panes = append(h.panes, p)
		return p, nil
	}
	return h
}

// forwarded is the total number of keys and text runs every fake pane has
// received so far. Steps assert the per-step delta.
func (h *scenarioHarness) forwarded() int {
	n := 0
	for _, p := range h.panes {
		n += len(p.keys) + len(p.texts)
	}
	return n
}

// scenarioState is what every step asserts. owner collapses the three
// predicates that decide where a keypress goes into one word so a table row
// reads as the user's model: list, agent pane, overview, placeholder.
type scenarioState struct {
	focus     paneFocus
	view      rightView
	owner     string
	selected  string
	forwarded int  // keys and text runs delivered to any pane during this step
	dials     int  // OpenHostPane calls during this step
	spawns    int  // SpawnHost calls during this step
	cmd       bool // the step's Update returned a Cmd
	leaked    bool // a dashboard binding fired: quit, or a form/modal opened
}

func (h *scenarioHarness) owner() string {
	switch {
	case h.m.paneOwnsKeyboard():
		return "agent"
	case h.m.agentPlaceholderDrawn() && h.m.focus == focusRight:
		return "placeholder"
	case h.m.detailOwnsKeyboard():
		return "overview"
	default:
		return "list"
	}
}

// scenarioStep is one thing the user (or the runtime) does. Exactly one of
// msg and act is set. settle delivers the Cmd's connect chain back through
// Update the way the runtime would; steps that leave a Cmd unsettled (a cursor
// move arming a preview timer) leave it false on purpose.
type scenarioStep struct {
	name string
	msg  tea.Msg
	act  func(h *scenarioHarness)
	// leader presses ctrl+] first, so msg is the key after the leader: how
	// every dashboard action is reached now that no bare letter acts.
	leader bool
	settle bool
	want   scenarioState
}

type scenarioCase struct {
	name  string
	rows  []Session
	steps []scenarioStep
}

func (h *scenarioHarness) run(step scenarioStep) scenarioState {
	h.t.Helper()
	dials, spawns, fwd := h.opens, h.spawns, h.forwarded()
	var cmd tea.Cmd
	switch {
	case step.act != nil:
		step.act(h)
	default:
		if step.leader {
			h.m, _ = update(h.t, h.m, leaderKeyPress)
			if !h.m.leaderOpen {
				h.t.Fatalf("step %q: ctrl+] did not open the actions dialog", step.name)
			}
		}
		h.m, cmd = update(h.t, h.m, step.msg)
		if step.settle {
			h.m = settleConnect(h.t, h.m, cmd)
		}
	}
	h.gens = append(h.gens, h.m.previewGen)
	sel, _ := h.m.selected()
	return scenarioState{
		focus:     h.m.focus,
		view:      h.m.view,
		owner:     h.owner(),
		selected:  sel.Name,
		forwarded: h.forwarded() - fwd,
		dials:     h.opens - dials,
		spawns:    h.spawns - spawns,
		cmd:       cmd != nil,
		leaked:    h.m.quitting || h.m.formOpen || h.m.modal != inputNormal,
	}
}

// pressDimmedToggle presses ctrl+] t where the toggle is dimmed (a stopped
// row has no agent to flip to). The dialog refuses the key and stays up, so
// the step closes it with esc, leaving the model as it found it.
func pressDimmedToggle(h *scenarioHarness) {
	h.t.Helper()
	h.m = pressLeader(h.t, h.m, keyRune('t'))
	if !h.m.leaderOpen {
		h.t.Fatalf("the dimmed toggle row closed the actions dialog")
	}
	h.m, _ = update(h.t, h.m, keyEscMsg())
}

// stealPane delivers the host's Stolen exit for the installed pane.
func stealPane(h *scenarioHarness) {
	h.m, _ = update(h.t, h.m, paneExitedMsg{pane: h.m.pane, reason: host.DetachStolen})
}

// settlePreview fires the preview timer for the current selection and settles
// the dial it returns, the way a cursor that stayed put for previewDelay does.
func settlePreview(h *scenarioHarness) {
	name, gen := h.selectedName(), h.m.previewGen
	var cmd tea.Cmd
	h.m, cmd = update(h.t, h.m, previewTimerMsg{gen: gen, name: name})
	h.m = settleConnect(h.t, h.m, cmd)
}

func (h *scenarioHarness) selectedName() string {
	s, _ := h.m.selected()
	return s.Name
}

var (
	scenarioJ = keyRune('j')
	scenarioK = keyRune('k')
	scenarioX = keyRune('x')
	scenarioQ = keyRune('q')
	scenarioR = keyRune('r')
	scenarioB = keyRune('b')
	scenarioI = keyRune('i')
)

// loadScenarioBuild hands the model a build with one task, so `b` has a tab to
// open wherever the dashboard, and not the agent, has the keyboard.
func loadScenarioBuild(h *scenarioHarness) {
	h.m, _ = update(h.t, h.m, buildLoadedMsg{snap: buildSnap("/repo"), gen: h.m.projectGen})
}

// assertNotOnBuildTab fails the scenario if a key meant for the agent moved
// the detail tab behind it.
func assertNotOnBuildTab(h *scenarioHarness) {
	h.t.Helper()
	if h.m.detailTab == tabBuild {
		h.t.Errorf("b typed into the agent moved the detail tab to Build")
	}
}

// loadScenarioInbox hands the model a build read with one open inbox item, so
// `i` has an inbox to open wherever the dashboard has the keyboard.
func loadScenarioInbox(h *scenarioHarness) {
	h.m, _ = update(h.t, h.m, buildLoadedMsg{snap: inboxSnap(), gen: h.m.projectGen})
}

// assertInboxClosed fails the scenario if a key meant for the agent opened
// the inbox overlay over it.
func assertInboxClosed(h *scenarioHarness) {
	h.t.Helper()
	if h.m.inboxOpen {
		h.t.Errorf("i typed into the agent opened the inbox overlay")
	}
}

// assertInboxOpen fails the scenario if `i` from the list did not open the
// overlay, and closes it again so the row's last state is the list's.
func assertInboxOpen(h *scenarioHarness) {
	h.t.Helper()
	if !h.m.inboxOpen {
		h.t.Errorf("i from the list did not open the inbox overlay")
	}
	h.m, _ = update(h.t, h.m, keyEsc)
}

// TestFocusScenario drives whole key sequences through Update and asserts the
// two-axis state (focus, view), who owns the keyboard, what reached the agent,
// and how many dials and spawns each step caused. The unit tests pin one
// transition each; this pins that the transitions compose, which is where the
// old four-flag model failed.
//
// Load-bearing rows, and why:
//
//   - "browse with list focus": j/k and a settled preview prove the list never
//     forwards a byte, even with a live pane drawn beside it. This is the
//     user's rule that list focus controls nothing on the right.
//   - "ctrl+t round trip": focus stays right through BOTH flips and the second
//     flip does not redial. A regression to the old ctrl+t (attach/hide/reveal)
//     shows up as a changed focus or a second dial.
//   - "ctrl+backslash then ctrl+t": the no-op must emit no Cmd and move
//     neither axis, or ctrl+t has regained a list-side meaning.
//   - "enter on a stopped row": zero spawns is the point. enter is pure focus;
//     restart belongs to r. The overview must own the keyboard, not the agent
//     placeholder.
//   - "r from the list" vs "r from the right overview": same one spawn and one
//     dial, but focus differs. From the list the keyboard stays on the list.
//   - "b typed into a focused agent": the Build tab's key is a list and
//     overview key. Forwarded once, no tab change, no view change is the proof
//     the agent pane lost none of its keys to it; the last step shows the same
//     key working from the list, hiding the agent without closing it.
//   - "i typed into a focused agent": the same proof for the inbox key. The
//     overlay staying closed is the point; the last steps show the key
//     working from the list with the agent still drawn and untouched.
//   - "steal while typing": q after a steal must not quit. The fixture types
//     first so the keyboard really is inside the agent when the host takes the
//     client away; a lost pane that handed focus to the list would quit here.
//   - "enter after a cursor move": the preview timer is armed but never fired
//     before enter, then fires stale. One dial total and a stable pane is the
//     retired 300ms race staying retired.
//   - "a cursor move under the overview": view is the user's ctrl+t choice and
//     nothing else may write it. A preview there would dial a pane nobody can
//     see and, on arrival, flip the right side back to the agent. Zero dials
//     and view=overview after a FIRED timer pins both refusals (arming and
//     dialing); its agent-view twin proves the gate is on view, not a blanket
//     preview kill.
func TestFocusScenario(t *testing.T) {
	list := scenarioState{focus: focusList, view: viewAgent, owner: "list"}
	with := func(base scenarioState, edit func(*scenarioState)) scenarioState {
		edit(&base)
		return base
	}

	cases := []scenarioCase{
		{
			name: "browse j/k with list focus forwards nothing",
			rows: []Session{runningSess("alpha"), runningSess("beta")},
			steps: []scenarioStep{
				{name: "j", msg: scenarioJ, want: with(list, func(s *scenarioState) { s.selected = "beta"; s.cmd = true })},
				{name: "k", msg: scenarioK, want: with(list, func(s *scenarioState) { s.selected = "alpha"; s.cmd = true })},
				{name: "preview settles", act: settlePreview, want: with(list, func(s *scenarioState) { s.selected = "alpha"; s.dials = 1 })},
				{name: "j with a live preview", msg: scenarioJ, want: with(list, func(s *scenarioState) { s.selected = "beta"; s.cmd = true })},
			},
		},
		{
			name: "enter connects the agent",
			rows: []Session{runningSess("alpha")},
			steps: []scenarioStep{
				{name: "enter", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "alpha", 1, true
				})},
				{name: "typing reaches the agent, q does not quit", msg: scenarioX, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.forwarded = focusRight, "agent", "alpha", 1
				})},
				{name: "q is typing", msg: scenarioQ, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.forwarded = focusRight, "agent", "alpha", 1
				})},
			},
		},
		{
			// The build data is loaded first so `b` WOULD open the Build tab
			// from the list: the row then proves the agent's focus is what
			// stops it, not a missing build.
			name: "b typed into a focused agent goes to the agent and does nothing else",
			rows: []Session{runningSess("alpha")},
			steps: []scenarioStep{
				{name: "the build arrives", act: loadScenarioBuild, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
				{name: "enter", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "alpha", 1, true
				})},
				{name: "b is typing", msg: scenarioB, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.forwarded = focusRight, "agent", "alpha", 1
				})},
				{name: "the tab did not move", act: assertNotOnBuildTab, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected = focusRight, "agent", "alpha"
				})},
				{name: "ctrl+backslash", msg: keyRune('\\'), leader: true, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
				{name: "b from the list hides the agent behind the build tasks", leader: true, msg: scenarioB, want: scenarioState{
					focus: focusRight, view: viewOverview, owner: "overview", selected: "alpha",
				}},
			},
		},
		{
			// The inbox is loaded first so `i` WOULD open the overlay from the
			// list: the row then proves the agent's focus is what stops it.
			name: "i typed into a focused agent goes to the agent and does nothing else",
			rows: []Session{runningSess("alpha")},
			steps: []scenarioStep{
				{name: "the inbox arrives", act: loadScenarioInbox, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
				{name: "enter", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "alpha", 1, true
				})},
				{name: "i is typing", msg: scenarioI, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.forwarded = focusRight, "agent", "alpha", 1
				})},
				{name: "the overlay did not open", act: assertInboxClosed, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected = focusRight, "agent", "alpha"
				})},
				{name: "ctrl+backslash", msg: keyRune('\\'), leader: true, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
				{name: "i from the list opens the overlay over the agent", leader: true, msg: scenarioI, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
				{name: "the overlay opened, and esc closes it", act: assertInboxOpen, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
			},
		},
		{
			name: "ctrl+t round trip keeps focus right",
			rows: []Session{runningSess("alpha")},
			steps: []scenarioStep{
				{name: "enter", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "alpha", 1, true
				})},
				{name: "ctrl+t to overview", msg: keyRune('t'), leader: true, want: scenarioState{
					focus: focusRight, view: viewOverview, owner: "overview", selected: "alpha",
				}},
				{name: "ctrl+t back to agent", msg: keyRune('t'), leader: true, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "agent", selected: "alpha",
				}},
				{name: "the agent is live again", msg: scenarioX, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "agent", selected: "alpha", forwarded: 1,
				}},
			},
		},
		{
			name: "ctrl+backslash then ctrl+t is a no-op",
			rows: []Session{runningSess("alpha")},
			steps: []scenarioStep{
				{name: "enter", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "alpha", 1, true
				})},
				{name: "ctrl+backslash", msg: keyRune('\\'), leader: true, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
				{name: "ctrl+t from the list", msg: keyRune('t'), leader: true, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
				{name: "typing hits the list only", msg: scenarioJ, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
			},
		},
		{
			name: "enter on a stopped row focuses the overview without restarting",
			rows: []Session{stoppedSess("alpha")},
			steps: []scenarioStep{
				{name: "enter", msg: keyEnter, settle: true, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "overview", selected: "alpha",
				}},
				{name: "ctrl+] t is inert", act: pressDimmedToggle, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "overview", selected: "alpha",
				}},
				{name: "ctrl+backslash releases", msg: keyRune('\\'), leader: true, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
			},
		},
		{
			name: "r restarts a stopped row from the list",
			rows: []Session{stoppedSess("alpha")},
			steps: []scenarioStep{
				{name: "r", leader: true, msg: scenarioR, settle: true, want: with(list, func(s *scenarioState) {
					s.selected, s.spawns, s.dials, s.cmd = "alpha", 1, 1, true
				})},
			},
		},
		{
			name: "r restarts a stopped row from the right overview",
			rows: []Session{stoppedSess("alpha")},
			steps: []scenarioStep{
				{name: "enter", msg: keyEnter, settle: true, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "overview", selected: "alpha",
				}},
				{name: "r", leader: true, msg: scenarioR, settle: true, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "agent", selected: "alpha", spawns: 1, dials: 1, cmd: true,
				}},
			},
		},
		{
			name: "steal while typing then q does not quit",
			rows: []Session{runningSess("alpha"), runningSess("beta")},
			steps: []scenarioStep{
				{name: "enter", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "alpha", 1, true
				})},
				{name: "typing", msg: scenarioX, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.forwarded = focusRight, "agent", "alpha", 1
				})},
				{name: "steal", act: stealPane, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "placeholder", selected: "alpha",
				}},
				{name: "q is swallowed", msg: scenarioQ, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "placeholder", selected: "alpha",
				}},
				{name: "j does not move the list", msg: scenarioJ, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "placeholder", selected: "alpha",
				}},
				{name: "enter reconnects with one dial", msg: keyEnter, settle: true, want: scenarioState{
					focus: focusRight, view: viewAgent, owner: "agent", selected: "alpha", dials: 1, cmd: true,
				}},
			},
		},
		{
			name: "enter within 300ms of a cursor move connects",
			rows: []Session{runningSess("alpha"), runningSess("beta")},
			steps: []scenarioStep{
				{name: "j arms a preview timer", msg: scenarioJ, want: with(list, func(s *scenarioState) { s.selected = "beta"; s.cmd = true })},
				{name: "enter before the timer fires", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "beta", 1, true
				})},
				{name: "the stale timer fires", act: func(h *scenarioHarness) {
					var cmd tea.Cmd
					h.m, cmd = update(h.t, h.m, previewTimerMsg{gen: h.gens[0], name: "beta"})
					h.m = settleConnect(h.t, h.m, cmd)
				}, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected = focusRight, "agent", "beta"
				})},
			},
		},
		{
			name: "a cursor move under the overview previews nothing",
			rows: []Session{runningSess("alpha"), runningSess("beta")},
			steps: []scenarioStep{
				{name: "enter", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "alpha", 1, true
				})},
				{name: "ctrl+t to overview", msg: keyRune('t'), leader: true, want: scenarioState{
					focus: focusRight, view: viewOverview, owner: "overview", selected: "alpha",
				}},
				{name: "ctrl+backslash", msg: keyRune('\\'), leader: true, want: scenarioState{
					focus: focusList, view: viewOverview, owner: "list", selected: "alpha",
				}},
				{name: "j arms no preview", msg: scenarioJ, want: scenarioState{
					focus: focusList, view: viewOverview, owner: "list", selected: "beta",
				}},
				{name: "a fired timer dials nothing", act: settlePreview, want: scenarioState{
					focus: focusList, view: viewOverview, owner: "list", selected: "beta",
				}},
			},
		},
		{
			name: "a cursor move under the agent view still previews",
			rows: []Session{runningSess("alpha"), runningSess("beta")},
			steps: []scenarioStep{
				{name: "enter", msg: keyEnter, settle: true, want: with(list, func(s *scenarioState) {
					s.focus, s.owner, s.selected, s.dials, s.cmd = focusRight, "agent", "alpha", 1, true
				})},
				{name: "ctrl+backslash", msg: keyRune('\\'), leader: true, want: with(list, func(s *scenarioState) { s.selected = "alpha" })},
				{name: "j arms a preview", msg: scenarioJ, want: with(list, func(s *scenarioState) { s.selected = "beta"; s.cmd = true })},
				{name: "the preview settles", act: settlePreview, want: with(list, func(s *scenarioState) { s.selected = "beta"; s.dials = 1 })},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newScenarioHarness(t, tc.rows...)
			for _, step := range tc.steps {
				got := h.run(step)
				if got != step.want {
					t.Fatalf("after %q:\n got  %+v\n want %+v", step.name, got, step.want)
				}
			}
		})
	}
}

// TestPassivePreviewNeverChangesTheView pins the rule that `view` changes only
// through ctrl+t on the right side. The review's reproduction is the overview
// row: enter, ctrl+t to the overview, ctrl+\ to the list, j, and the preview
// timer fires. A preview there used to dial the new row and its arrival
// rewrote view to agent, so the overview the user chose vanished on a cursor
// move. The agent row is the control: the same keys must still preview, or
// the gate has turned into a blanket preview kill.
//
// The timer is fired with the CURRENT generation whether or not j armed one,
// so the overview row proves applyPreviewTimer refuses on its own, not only
// that schedulePreview declined to arm.
func TestPassivePreviewNeverChangesTheView(t *testing.T) {
	cases := []struct {
		name      string
		overview  bool
		wantView  rightView
		wantDials int
		wantPane  string
		wantTimer bool
	}{
		{name: "overview: no timer, no dial, overview kept", overview: true, wantView: viewOverview, wantDials: 0, wantPane: "", wantTimer: false},
		{name: "agent control: j previews the new row", overview: false, wantView: viewAgent, wantDials: 1, wantPane: "beta", wantTimer: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newScenarioHarness(t, runningSess("alpha"), runningSess("beta"))
			var cmd tea.Cmd
			h.m, cmd = update(t, h.m, keyEnter)
			h.m = settleConnect(t, h.m, cmd)
			if tc.overview {
				h.m = pressLeader(t, h.m, keyRune('t'))
			}
			h.m = pressLeader(t, h.m, keyRune('\\'))
			before := h.opens

			h.m, cmd = update(t, h.m, scenarioJ)
			if got := cmd != nil; got != tc.wantTimer {
				t.Errorf("j returned a Cmd = %v, want %v (the preview timer)", got, tc.wantTimer)
			}
			settlePreview(h)

			if h.m.view != tc.wantView {
				t.Errorf("view = %v, want %v", h.m.view, tc.wantView)
			}
			if h.m.focus != focusList {
				t.Errorf("focus = %v, want focusList", h.m.focus)
			}
			if got := h.opens - before; got != tc.wantDials {
				t.Errorf("dials after j = %d, want %d", got, tc.wantDials)
			}
			gotPane := ""
			if h.m.pane != nil {
				gotPane = h.m.paneName
			}
			if gotPane != tc.wantPane {
				t.Errorf("installed pane = %q, want %q", gotPane, tc.wantPane)
			}
		})
	}
}

// TestUnfocusedArrivalKeepsTheView covers the arrival half of the same rule,
// independent of the preview gate: a pane nobody focused (a post-create
// auto-open, or a dial already out when the view changed) installs behind
// whatever view the user chose, with the keyboard on the list. Writing
// view=agent here is exactly the bug the review reproduced; the overview row
// fails if it comes back.
func TestUnfocusedArrivalKeepsTheView(t *testing.T) {
	for _, view := range []rightView{viewOverview, viewAgent} {
		m := testModel(runningSess("alpha"))
		m.view = view
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
		if m.pane == nil || m.paneName != "alpha" {
			t.Fatalf("view %v: the arrival was not installed", view)
		}
		if m.view != view {
			t.Errorf("view %v: arrival rewrote view to %v", view, m.view)
		}
		if m.focus != focusList {
			t.Errorf("view %v: focus = %v, want focusList", view, m.focus)
		}
	}
}

// TestStartupPaneBelongsToTheSelectedRow pins the startup pane (`afk dash
// --open <name>`, and the hand-off from `afk new`) to the focus model's rule
// that the pane always belongs to the selected row. The first load leaves the
// cursor on row 0, and a pane is drawn only for the selected row, so opening
// any other row used to connect a pane nobody could see: the right side drew
// row 0's overview, enter focused row 0, and the hidden connection held the
// named session's one client slot. The walk-through against a built binary
// found it (docs/decisions.md, T0.6).
//
// Load-bearing rows, and why:
//
//   - "running, not the first row": the bug itself. Before the fix the pane
//     installed for beta with alpha selected, so drawn is false.
//   - "stopped, not the first row": the restart branch returns on its own
//     path, so a fix placed only ahead of openPaneFor would miss it. This is
//     also the usual `afk new` hand-off, where the host is not up yet.
//   - "running, the first row": the control. It passed before the fix and
//     must go on passing with the cursor where it was.
//   - "no such session": nothing is found, so the cursor must not move and
//     nothing may be dialed or spawned.
//
// Every found row also asserts the keyboard stays on the list (a startup
// pane is not a focus gesture) and that the row is remembered, so the next
// plain `afk dash` comes back to it.
func TestStartupPaneBelongsToTheSelectedRow(t *testing.T) {
	cases := []struct {
		name         string
		rows         []Session
		open         string
		wantSelected string
		wantDrawn    bool
		wantDials    int
		wantSpawns   int
	}{
		{
			name: "running, the first row",
			rows: []Session{runningSess("alpha"), runningSess("beta")},
			open: "alpha", wantSelected: "alpha", wantDrawn: true, wantDials: 1,
		},
		{
			name: "running, not the first row",
			rows: []Session{runningSess("alpha"), runningSess("beta")},
			open: "beta", wantSelected: "beta", wantDrawn: true, wantDials: 1,
		},
		{
			name: "stopped, not the first row",
			rows: []Session{runningSess("alpha"), stoppedSess("beta")},
			open: "beta", wantSelected: "beta", wantDrawn: true, wantDials: 1, wantSpawns: 1,
		},
		{
			name: "no such session",
			rows: []Session{runningSess("alpha"), runningSess("beta")},
			open: "gamma", wantSelected: "alpha",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newScenarioHarness(t, tc.rows...)
			h.m.initialSession = tc.open
			h.m.firstLoadDone = true

			nm, cmd := h.m.maybeOpenInitialPane()
			h.m = settleConnect(t, nm.(Model), cmd)

			if got := h.selectedName(); got != tc.wantSelected {
				t.Errorf("selected = %q, want %q", got, tc.wantSelected)
			}
			if got := h.m.shouldRenderPane(); got != tc.wantDrawn {
				t.Errorf("pane drawn = %v, want %v (pane installed for %q)", got, tc.wantDrawn, h.m.paneName)
			}
			if h.opens != tc.wantDials || h.spawns != tc.wantSpawns {
				t.Errorf("dials = %d, spawns = %d, want %d and %d", h.opens, h.spawns, tc.wantDials, tc.wantSpawns)
			}
			if h.m.focus != focusList {
				t.Errorf("focus = %v, want the list: a startup pane is not a focus gesture", h.m.focus)
			}
			if !tc.wantDrawn {
				return
			}
			if h.m.paneName != tc.open {
				t.Errorf("pane belongs to %q, want %q", h.m.paneName, tc.open)
			}
			if got := h.m.rememberedSelection[h.m.selectionKey()]; got != tc.open {
				t.Errorf("remembered row = %q, want %q", got, tc.open)
			}
		})
	}
}

// TestAgentKeyHintNamesAKeyThatWorks pins the key a toast names for getting
// back to the agent. With the overview focused, enter is not bound, so "enter/f
// to reopen the agent" named a key that did nothing; ctrl+t is the key that
// works there. The walk-through against a built binary found it after a
// restart confirmed from the focused overview (docs/decisions.md, T0.6).
//
// Load-bearing rows, and why:
//
//   - the four "overview focused" rows: one per toast that can be raised while
//     the overview has the keyboard. Each said enter/f before the fix.
//   - the "list focused" and "agent pane focused" rows: the controls. enter/f
//     is right there and must stay, or the fix has become a blanket rename.
//
// The last two checks press the keys: enter from the focused overview must
// leave the overview owning the keyboard, and ctrl+t must reach the agent.
// They are what makes the hint true, so a later change that binds enter in
// the overview fails here and the hint can be reconsidered.
func TestAgentKeyHintNamesAKeyThatWorks(t *testing.T) {
	wedgedScratch := func() Session {
		s := scratchSess("alpha")
		s.HostRunning = true
		s.HostPID = 4242
		s.WedgedSince = time.Unix(1_000_000, 0).Add(-time.Minute).UTC().Format(time.RFC3339)
		return s
	}
	focusOverview := func(h *scenarioHarness) {
		var cmd tea.Cmd
		h.m, cmd = update(h.t, h.m, keyEnter)
		h.m = settleConnect(h.t, h.m, cmd)
		h.m, _ = pressAction(h.t, h.m, keyRune('t'))
		if h.owner() != "overview" {
			h.t.Fatalf("precondition: owner = %q, want the focused overview", h.owner())
		}
	}
	focusAgent := func(h *scenarioHarness) {
		var cmd tea.Cmd
		h.m, cmd = update(h.t, h.m, keyEnter)
		h.m = settleConnect(h.t, h.m, cmd)
		if h.owner() != "agent" {
			h.t.Fatalf("precondition: owner = %q, want the focused agent pane", h.owner())
		}
	}
	dropPane := func(h *scenarioHarness) tea.Msg {
		return paneExitedMsg{pane: h.m.pane, reason: host.DetachDropped}
	}
	cases := []struct {
		name  string
		row   Session
		setup func(h *scenarioHarness)
		msg   func(h *scenarioHarness) tea.Msg
		want  string
		wrong string
		// viaLeader sends msg as ctrl+] then the key, the only way a
		// dashboard action is reached since T7.2.
		viaLeader bool
	}{
		{
			name: "restart done, list focused",
			row:  runningSess("alpha"),
			msg: func(*scenarioHarness) tea.Msg {
				return restartChildRequestedMsg{name: "alpha", restarted: true}
			},
			want: "enter to reopen the agent", wrong: "ctrl+] t",
		},
		{
			name:  "restart done, overview focused",
			row:   runningSess("alpha"),
			setup: focusOverview,
			msg: func(*scenarioHarness) tea.Msg {
				return restartChildRequestedMsg{name: "alpha", restarted: true}
			},
			want: "ctrl+] t to reopen the agent", wrong: "enter",
		},
		{
			name:  "restart with no outcome, overview focused",
			row:   runningSess("alpha"),
			setup: focusOverview,
			msg: func(*scenarioHarness) tea.Msg {
				return restartChildRequestedMsg{name: "alpha"}
			},
			want: "ctrl+] t to reconnect the agent", wrong: "enter",
		},
		{
			name: "one-off restart refused, list focused",
			row:  wedgedScratch(),
			msg:  func(*scenarioHarness) tea.Msg { return scenarioR },
			want: "enter to show the agent", wrong: "ctrl+] t", viaLeader: true,
		},
		{
			name:  "one-off restart refused, overview focused",
			row:   wedgedScratch(),
			setup: focusOverview,
			msg:   func(*scenarioHarness) tea.Msg { return scenarioR },
			want:  "ctrl+] t to show the agent", wrong: "enter", viaLeader: true,
		},
		{
			name:  "connection dropped behind the focused overview",
			row:   runningSess("alpha"),
			setup: focusOverview,
			msg:   dropPane,
			want:  "ctrl+] t to reopen the agent", wrong: "enter",
		},
		{
			name:  "connection dropped under the focused agent pane",
			row:   runningSess("alpha"),
			setup: focusAgent,
			msg:   dropPane,
			want:  "enter to reopen the agent", wrong: "ctrl+] t",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newScenarioHarness(t, tc.row)
			if tc.setup != nil {
				tc.setup(h)
			}
			if tc.viaLeader {
				h.m, _ = pressAction(t, h.m, tc.msg(h))
			} else {
				h.m, _ = update(t, h.m, tc.msg(h))
			}
			if !strings.Contains(h.m.status, tc.want) {
				t.Errorf("status = %q, want it to contain %q", h.m.status, tc.want)
			}
			if strings.Contains(h.m.status, tc.wrong) {
				t.Errorf("status = %q, must not name %q: that key does not reach the agent from here", h.m.status, tc.wrong)
			}
		})
	}

	t.Run("from the focused overview enter does nothing and ctrl+t reaches the agent", func(t *testing.T) {
		h := newScenarioHarness(t, runningSess("alpha"))
		focusOverview(h)
		h.m, _ = update(t, h.m, keyEnter)
		if got := h.owner(); got != "overview" {
			t.Fatalf("after enter: owner = %q, want the overview still (enter is not bound there)", got)
		}
		h.m, _ = pressAction(t, h.m, keyRune('t'))
		if got := h.owner(); got != "agent" {
			t.Fatalf("after ctrl+t: owner = %q, want the agent", got)
		}
	})
}
