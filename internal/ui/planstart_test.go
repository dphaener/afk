package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/session"
)

// planCall is one StartPlan call as the recording seam saw it.
type planCall struct{ root, mode, text string }

// planSeams records every StartPlan call and the pane dials, and holds the
// result the test wants the seam to give, on pauseSeams' pattern.
type planSeams struct {
	calls []planCall
	dials []hostPaneConfig
	reply PlanStartResult
}

// planModel is a dashboard started in /repo whose active project is
// /projects/api, so a test reading the root off the call can tell the
// project's path from the folder the dashboard was started in. The seam
// records into the returned struct; OpenHostPane is a fake, so a pane open
// dials nothing real.
func planModel(t *testing.T, sessions ...Session) (Model, *planSeams) {
	t.Helper()
	rec := &planSeams{}
	m := testModel(sessions...)
	m.projects = []project.Project{{Path: "/projects/api", Name: "api"}}
	m.activeProject = 0
	// The search found it, as it would have in `afk dash`: m.projects is
	// always a merge, and a start in another folder merges again.
	m.found = m.projects
	m.deps.StartPlan = func(root, mode, text string) PlanStartResult {
		rec.calls = append(rec.calls, planCall{root, mode, text})
		return rec.reply
	}
	m.deps.OpenHostPane = func(cfg hostPaneConfig) (paneHandle, error) {
		rec.dials = append(rec.dials, cfg)
		return newFakePane("PLANNER"), nil
	}
	return m, rec
}

// masterPlanSess is the planning session's row as discovery lists it: in the
// project's main checkout, host running.
func masterPlanSess() Session {
	return Session{SessionState: discover.SessionState{
		Name:         session.PlanSessionName,
		WorktreePath: "/projects/api",
		HostRunning:  true,
		HostPID:      5150,
	}}
}

// typeText types s one key at a time, as a person does.
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = update(t, m, keyRune(r))
	}
	return m
}

// runPlanAction is ctrl+space, the query, enter: the way the plan actions
// are reached, since they have no key after the leader.
func runPlanAction(t *testing.T, m Model, query string) (Model, tea.Cmd) {
	t.Helper()
	m, _ = update(t, m, leaderKeyPress)
	if !m.leaderOpen {
		t.Fatal("setup: ctrl+space did not open the actions dialog")
	}
	m = typeText(t, m, query)
	return update(t, m, keyEnter)
}

// planStarted runs cmd, which must be the seam's command, and returns the
// message it produced.
func planStarted(t *testing.T, cmd tea.Cmd) planStartedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command: the seam was not called")
	}
	msg, ok := cmd().(planStartedMsg)
	if !ok {
		t.Fatalf("command produced %T, want planStartedMsg", msg)
	}
	return msg
}

// paneDial finds the pane open in cmd (applyPlanStarted batches it with a
// reload) and runs it, returning the attach message it produced.
func paneDial(t *testing.T, cmd tea.Cmd) (paneAttachedMsg, bool) {
	t.Helper()
	if cmd == nil {
		return paneAttachedMsg{}, false
	}
	switch msg := cmd().(type) {
	case paneAttachedMsg:
		return msg, true
	case tea.BatchMsg:
		for _, c := range msg {
			if c == nil {
				continue
			}
			if got, ok := paneDial(t, c); ok {
				return got, true
			}
		}
	}
	return paneAttachedMsg{}, false
}

// TestPlanStartActionsCallTheSeamOnce runs each plan action from the dialog
// by name and checks the seam was called exactly once, with the active
// project's path (not the folder the dashboard started in), the mode, and
// the text as typed. Every row is load-bearing: each is one of the three
// actions, and "once" is A6 (no pre-check; the start finds an open session
// itself). Reconcile is reached by "document", and by its full name only
// when the selected agent is running: the name begins with r, restart's key,
// and a dimmed restart swallows the r where a live one would run.
func TestPlanStartActionsCallTheSeamOnce(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		text    string
		want    planCall
		running bool
	}{
		{"extend", "extend", "", planCall{"/projects/api", PlanModeExtend, ""}, false},
		{"extend by its full name", "extend the plan", "", planCall{"/projects/api", PlanModeExtend, ""}, false},
		{"amend", "amend", "Add a search page.", planCall{"/projects/api", PlanModeAmend, "Add a search page."}, false},
		{"amend by its full name", "amend the plan", "Drop the export task.", planCall{"/projects/api", PlanModeAmend, "Drop the export task."}, false},
		{"reconcile", "document", "docs/new-design.md", planCall{"/projects/api", PlanModeReconcile, "docs/new-design.md"}, false},
		{"reconcile by its full name with restart dimmed", "reconcile the plan with a document", "docs/a.md", planCall{"/projects/api", PlanModeReconcile, "docs/a.md"}, true},
		// Spaces around the text are the field's, not the owner's.
		{"amend text is trimmed", "amend", "  Rename the CLI.  ", planCall{"/projects/api", PlanModeAmend, "Rename the CLI."}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := sess("a")
			if tc.running {
				row = runningSess("a")
			}
			m, rec := planModel(t, row)
			m, cmd := runPlanAction(t, m, tc.query)
			if m.leaderOpen {
				t.Fatal("the dialog stayed open")
			}
			if tc.want.mode != PlanModeExtend {
				if m.modal != inputPlanText {
					t.Fatalf("modal = %v, want the plan form", m.modal)
				}
				if len(rec.calls) != 0 {
					t.Fatalf("the seam was called before the text was typed: %v", rec.calls)
				}
				m = typeText(t, m, tc.text)
				m, cmd = update(t, m, keyEnter)
				if m.modal != inputNormal {
					t.Fatalf("modal = %v after enter, want the form closed", m.modal)
				}
			}
			planStarted(t, cmd)
			if len(rec.calls) != 1 {
				t.Fatalf("seam called %d times, want once: %v", len(rec.calls), rec.calls)
			}
			if rec.calls[0] != tc.want {
				t.Errorf("call = %+v, want %+v", rec.calls[0], tc.want)
			}
		})
	}
}

// TestPlanStartRootFallsBackToTheRepo covers planRoot's second branch: a
// dashboard handed a repo but no project list still has an active project
// (hasActiveProject keys on the repo), and the start is sent there.
func TestPlanStartRootFallsBackToTheRepo(t *testing.T) {
	m, rec := planModel(t, sess("a"))
	m.projects = nil
	_, cmd := runPlanAction(t, m, "extend")
	planStarted(t, cmd)
	if len(rec.calls) != 1 || rec.calls[0].root != "/repo" {
		t.Fatalf("calls = %v, want one call for /repo", rec.calls)
	}
}

// TestPlanStartEmptyTextIsRefusedInTheForm submits blank text in each form.
// Load-bearing: every row must keep the form open, say what is missing, and
// call the seam zero times — a blank sentence or path must never reach the
// start, and closing the form would make the owner open it again.
func TestPlanStartEmptyTextIsRefusedInTheForm(t *testing.T) {
	cases := []struct {
		name  string
		query string
		text  string
		note  string
	}{
		{"empty sentence", "amend", "", planAmendEmpty},
		{"blank sentence", "amend", "    ", planAmendEmpty},
		{"empty path", "document", "", planReconcileEmpty},
		{"blank path", "document", "  ", planReconcileEmpty},
		{"empty folder", "start a new plan", "", planNewEmpty},
		{"blank folder", "start a new plan", "   ", planNewEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := planModel(t, sess("a"))
			m, _ = runPlanAction(t, m, tc.query)
			m = typeText(t, m, tc.text)
			m, cmd := update(t, m, keyEnter)
			if cmd != nil {
				if _, ok := cmd().(planStartedMsg); ok {
					t.Fatal("blank text produced a start")
				}
			}
			if len(rec.calls) != 0 {
				t.Fatalf("seam called %d times for blank text, want 0", len(rec.calls))
			}
			if m.modal != inputPlanText {
				t.Fatalf("modal = %v, want the form still open", m.modal)
			}
			if m.planTextNote != tc.note {
				t.Errorf("note = %q, want %q", m.planTextNote, tc.note)
			}
			if !strings.Contains(render(m), tc.note) {
				t.Errorf("the form does not show %q", tc.note)
			}
			// The form still works: typing the text now starts it.
			m = typeText(t, m, "x.md")
			_, cmd = update(t, m, keyEnter)
			planStarted(t, cmd)
			if len(rec.calls) != 1 {
				t.Fatalf("seam called %d times after the text was typed, want 1", len(rec.calls))
			}
		})
	}
}

// TestPlanStartFormEscCancels: esc closes either form with nothing called.
func TestPlanStartFormEscCancels(t *testing.T) {
	for _, q := range []string{"amend", "document"} {
		t.Run(q, func(t *testing.T) {
			m, rec := planModel(t, sess("a"))
			m, _ = runPlanAction(t, m, q)
			m = typeText(t, m, "half a thought")
			m, cmd := update(t, m, keyEsc)
			if m.modal != inputNormal {
				t.Fatalf("modal = %v, want closed", m.modal)
			}
			if cmd != nil {
				if _, ok := cmd().(planStartedMsg); ok {
					t.Fatal("esc started a session")
				}
			}
			if len(rec.calls) != 0 {
				t.Fatalf("seam called %d times, want 0", len(rec.calls))
			}
		})
	}
}

// TestPlanStartResults folds each kind of result into the dashboard.
//
// Load-bearing rows:
//   - "refused": the reason is shown in the command's words and no pane
//     opens, no dial goes out and the keyboard stays on the list.
//   - "started, row listed": the master-plan row is selected and its pane
//     dialled and installed focused, as enter on that row does.
//   - "started, row not listed yet": the dial goes out at once from the
//     project root, and the row is selected by the next load that lists it —
//     the pane is drawn only for the selected row, so without that it would
//     connect and never show.
//   - "stale project": a result from before a project switch opens nothing.
func TestPlanStartResults(t *testing.T) {
	refusal := "This project has no plan yet. Run afk plan first."
	cases := []struct {
		name     string
		listed   bool
		reply    PlanStartResult
		mode     string
		staleGen bool
		wantPane bool
		wantText string
	}{
		{name: "refused", reply: PlanStartResult{Outcome: PlanRefused, Reason: refusal, Root: "/projects/api"},
			mode: PlanModeExtend, wantText: refusal},
		{name: "refused with no reason", reply: PlanStartResult{Outcome: PlanRefused},
			mode: PlanModeAmend, wantText: "could not be started"},
		{name: "started, row listed", listed: true, reply: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"},
			mode: PlanModeExtend, wantPane: true, wantText: "Started a planning session to extend the plan."},
		{name: "started, row not listed yet", reply: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"},
			mode: PlanModeAmend, wantPane: true, wantText: "to amend the plan"},
		{name: "started with an archive", listed: true,
			reply: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api", Archive: "/projects/api/.afk/plan-archive/2026-10-14"},
			mode:  PlanModeReconcile, wantPane: true, wantText: "moved to /projects/api/.afk/plan-archive/2026-10-14"},
		{name: "resumed", listed: true, reply: PlanStartResult{Outcome: PlanResumed, Root: "/projects/api"},
			mode: PlanModeResume, wantPane: true, wantText: "already open"},
		{name: "stale project", listed: true, reply: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"},
			mode: PlanModeExtend, staleGen: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := []Session{sess("a")}
			if tc.listed {
				rows = append(rows, masterPlanSess())
			}
			m, rec := planModel(t, rows...)
			before := m.status
			gen := m.projectGen
			if tc.staleGen {
				gen--
			}
			m, cmd := update(t, m, planStartedMsg{mode: tc.mode, res: tc.reply, gen: gen})
			if m.modal != inputNormal {
				t.Fatalf("modal = %v, want none", m.modal)
			}
			if tc.staleGen {
				if cmd != nil || m.status != before || m.attachInFlight.name != "" {
					t.Fatalf("a stale result did something: cmd=%v status=%q dial=%q", cmd != nil, m.status, m.attachInFlight.name)
				}
				return
			}
			if !strings.Contains(m.status, tc.wantText) {
				t.Errorf("status = %q, want it to contain %q", m.status, tc.wantText)
			}
			if !tc.wantPane {
				if m.attachInFlight.name != "" || m.focus != focusList {
					t.Fatalf("a refusal opened something: dial=%q focus=%v", m.attachInFlight.name, m.focus)
				}
				if _, ok := paneDial(t, cmd); ok || len(rec.dials) != 0 {
					t.Fatal("a refusal dialled a pane")
				}
				return
			}
			if m.focus != focusRight || m.view != viewAgent {
				t.Fatalf("focus=%v view=%v, want the agent view focused", m.focus, m.view)
			}
			attached, ok := paneDial(t, cmd)
			if !ok {
				t.Fatal("no pane was dialled")
			}
			if len(rec.dials) != 1 || rec.dials[0].Name != session.PlanSessionName || rec.dials[0].Worktree != "/projects/api" {
				t.Fatalf("dials = %+v, want one for master-plan in /projects/api", rec.dials)
			}
			if !tc.listed {
				if m.planSelectPending != session.PlanSessionName {
					t.Fatalf("planSelectPending = %q, want the row armed", m.planSelectPending)
				}
				m, _ = update(t, m, sessionsLoadedMsg{sessions: []Session{sess("a"), masterPlanSess()}, at: time.Unix(1_000_000, 0), gen: m.projectGen})
				if m.planSelectPending != "" {
					t.Fatal("the armed selection was not consumed when the row was listed")
				}
			}
			if s, ok := m.selected(); !ok || s.Name != session.PlanSessionName {
				t.Fatalf("selected = %q, want master-plan", s.Name)
			}
			m, _ = update(t, m, attached)
			if m.pane == nil || m.paneName != session.PlanSessionName {
				t.Fatalf("pane = %q, want master-plan installed", m.paneName)
			}
			if !m.paneOwnsKeyboard() {
				t.Fatal("the planning pane is not drawn with the keyboard")
			}
		})
	}
}

// TestPlanStartArmedSelectionYieldsToTheOwner: once the owner goes back to
// the list, a later load must not drag the cursor to master-plan.
func TestPlanStartArmedSelectionYieldsToTheOwner(t *testing.T) {
	m, _ := planModel(t, sess("a"), sess("b"))
	m, _ = update(t, m, planStartedMsg{mode: PlanModeExtend, res: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"}, gen: m.projectGen})
	m.focus = focusList
	m, _ = update(t, m, sessionsLoadedMsg{sessions: []Session{sess("a"), sess("b"), masterPlanSess()}, at: time.Unix(1_000_000, 0), gen: m.projectGen})
	if s, _ := m.selected(); s.Name == session.PlanSessionName {
		t.Fatal("the cursor was moved to master-plan after the owner went back to the list")
	}
	if m.planSelectPending != "" {
		t.Fatal("the armed selection outlived the owner leaving the right side")
	}
}

// TestPlanStartOpenSessionOffer is the refusal "a planning session is
// already open". Load-bearing rows: the offer names the open session's mode
// and phase; yes sends the resume request (no mode, no text) to the same
// root — a second call, never a new start — and its result opens the pane;
// no and esc call nothing and open nothing.
func TestPlanStartOpenSessionOffer(t *testing.T) {
	open := PlanStartResult{
		Outcome: PlanRefused, Open: true, OpenMode: "amend", OpenPhase: "questions",
		Reason: "a planning session is already open", Root: "/projects/api",
	}
	cases := []struct {
		name      string
		answer    tea.KeyPressMsg
		wantCalls int
	}{
		{"yes opens it", keyRune('y').(tea.KeyPressMsg), 1},
		{"Y opens it", keyRune('Y').(tea.KeyPressMsg), 1},
		{"no does nothing", keyRune('n').(tea.KeyPressMsg), 0},
		{"esc does nothing", keyEsc, 0},
		{"a stray key does nothing and keeps the offer", keyRune('q').(tea.KeyPressMsg), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := planModel(t, sess("a"), masterPlanSess())
			rec.reply = open
			m, cmd := runPlanAction(t, m, "extend")
			m, _ = update(t, m, planStarted(t, cmd))
			if m.modal != inputConfirmPlanOpen {
				t.Fatalf("modal = %v, want the open-it offer", m.modal)
			}
			if m.attachInFlight.name != "" {
				t.Fatal("a refusal dialled a pane before the owner answered")
			}
			frame := render(m)
			for _, want := range []string{"A planning session is already open (mode amend, at questions).", "y: open it"} {
				if !strings.Contains(frame, want) {
					t.Errorf("the offer does not show %q", want)
				}
			}
			rec.calls = nil
			rec.reply = PlanStartResult{Outcome: PlanResumed, Root: "/projects/api"}
			m, cmd = update(t, m, tc.answer)
			if tc.wantCalls == 0 {
				if cmd != nil {
					if _, ok := cmd().(planStartedMsg); ok {
						t.Fatal("the answer called the seam")
					}
				}
				if len(rec.calls) != 0 || m.attachInFlight.name != "" {
					t.Fatalf("calls=%v dial=%q, want nothing", rec.calls, m.attachInFlight.name)
				}
				return
			}
			if m.modal != inputNormal {
				t.Fatalf("modal = %v after yes, want closed", m.modal)
			}
			m, cmd = update(t, m, planStarted(t, cmd))
			if len(rec.calls) != 1 || rec.calls[0] != (planCall{"/projects/api", PlanModeResume, ""}) {
				t.Fatalf("calls = %v, want one resume request for /projects/api", rec.calls)
			}
			if _, ok := paneDial(t, cmd); !ok {
				t.Fatal("the resumed session's pane was not dialled")
			}
			if s, _ := m.selected(); s.Name != session.PlanSessionName {
				t.Fatalf("selected = %q, want master-plan", s.Name)
			}
		})
	}
}

// TestPlanStartWithoutTheSeam: a dashboard built with no StartPlan answers
// each action with a sentence and calls nothing, as PauseBuild's nil does.
func TestPlanStartWithoutTheSeam(t *testing.T) {
	for _, q := range []string{"extend", "amend", "document"} {
		t.Run(q, func(t *testing.T) {
			m, _ := planModel(t, sess("a"))
			m.deps.StartPlan = nil
			m, cmd := runPlanAction(t, m, q)
			if cmd != nil {
				t.Fatal("a command was issued with no seam")
			}
			if m.modal != inputNormal {
				t.Fatalf("modal = %v, want no form", m.modal)
			}
			if m.status != planStartUnavailableNotice {
				t.Errorf("status = %q, want the not-available notice", m.status)
			}
		})
	}
}

// TestPlanStartFormDroppedAcrossAProjectSwitch: the text was typed for the
// project on screen when the form opened, so a switch underneath it (a
// click-to-focus request can switch projects while a modal is up) refuses
// the submit instead of starting a session in the other project.
func TestPlanStartFormDroppedAcrossAProjectSwitch(t *testing.T) {
	m, rec := planModel(t, sess("a"))
	m, _ = runPlanAction(t, m, "amend")
	m = typeText(t, m, "Add a search page.")
	m.projectGen++
	m, cmd := update(t, m, keyEnter)
	if cmd != nil {
		if _, ok := cmd().(planStartedMsg); ok {
			t.Fatal("the submit started a session after a project switch")
		}
	}
	if len(rec.calls) != 0 {
		t.Fatalf("seam called %d times, want 0", len(rec.calls))
	}
	if m.modal != inputNormal || !strings.Contains(m.status, "project changed") {
		t.Fatalf("modal=%v status=%q, want the form closed with a notice", m.modal, m.status)
	}
}

// TestPlanStartNewCallsTheSeamOnce runs "start a new plan" by name, types a
// folder and checks the seam was called exactly once with the folder as
// typed, mode new and no text. Load-bearing rows: "a typed ~ path" (the UI
// passes ~/ through unexpanded; the start expands it), "the default folder"
// (enter on the pre-filled field starts there), and "no project open" (the
// action needs only the seam, so a dashboard outside any project can start
// one; guarding it on a project as the change actions are would refuse here).
func TestPlanStartNewCallsTheSeamOnce(t *testing.T) {
	cases := []struct {
		name      string
		noProject bool
		noPlan    bool
		text      string
		want      planCall
	}{
		{name: "a typed full path", text: "/code/fresh", want: planCall{"/code/fresh", PlanModeNew, ""}},
		{name: "a typed ~ path", text: "~/code/fresh", want: planCall{"~/code/fresh", PlanModeNew, ""}},
		{name: "spaces around the folder are the field's", text: "  /code/fresh ", want: planCall{"/code/fresh", PlanModeNew, ""}},
		{name: "the default folder", noPlan: true, want: planCall{"/projects/api", PlanModeNew, ""}},
		{name: "a subfolder typed after the default", noPlan: true, text: "/web", want: planCall{"/projects/api/web", PlanModeNew, ""}},
		{name: "no project open", noProject: true, text: "/code/fresh", want: planCall{"/code/fresh", PlanModeNew, ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := planModel(t, sess("a"))
			if tc.noProject {
				m.projects = nil
				m.activeProject = -1
				m.repo = ""
			}
			if tc.noPlan {
				m.buildKnown = true
			}
			m, cmd := runPlanAction(t, m, "start a new plan")
			if m.modal != inputPlanText || m.planTextMode != PlanModeNew {
				t.Fatalf("modal = %v mode = %q, want the new-plan form", m.modal, m.planTextMode)
			}
			if cmd != nil {
				if _, ok := cmd().(planStartedMsg); ok {
					t.Fatal("the seam was called before the folder was typed")
				}
			}
			m = typeText(t, m, tc.text)
			m, cmd = update(t, m, keyEnter)
			if m.modal != inputNormal {
				t.Fatalf("modal = %v after enter, want the form closed", m.modal)
			}
			got := planStarted(t, cmd)
			if got.mode != PlanModeNew {
				t.Errorf("message mode = %q, want %q", got.mode, PlanModeNew)
			}
			if len(rec.calls) != 1 || rec.calls[0] != tc.want {
				t.Fatalf("calls = %+v, want exactly %+v", rec.calls, tc.want)
			}
		})
	}
}

// TestPlanStartNewFormDefault pins the new-plan form's starting text. The
// load-bearing row is "build known, no plan": the field holds the active
// project's path with the cursor at its end. The others are load-bearing in
// the other direction: a project with a plan would only be refused, and a
// build not read yet is a guess, so both start empty, as does no project.
func TestPlanStartNewFormDefault(t *testing.T) {
	cases := []struct {
		name      string
		known     bool
		hasPlan   bool
		noProject bool
		want      string
	}{
		{name: "build known, no plan", known: true, want: "/projects/api"},
		{name: "build known, has a plan", known: true, hasPlan: true, want: ""},
		{name: "build not read yet", want: ""},
		{name: "no project open", known: true, noProject: true, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := planModel(t, sess("a"))
			m.buildKnown = tc.known
			m.build.HasBuild = tc.hasPlan
			if tc.noProject {
				m.projects = nil
				m.activeProject = -1
				m.repo = ""
			}
			m, _ = runPlanAction(t, m, "start a new plan")
			if m.modal != inputPlanText {
				t.Fatalf("modal = %v, want the form", m.modal)
			}
			if got := m.planTextInput.Value(); got != tc.want {
				t.Errorf("field = %q, want %q", got, tc.want)
			}
			if got := m.planTextInput.Position(); got != len(tc.want) {
				t.Errorf("cursor at %d, want the end (%d)", got, len(tc.want))
			}
			frame := render(m)
			for _, want := range []string{"Start a new plan", planNewQuestion} {
				if !strings.Contains(frame, want) {
					t.Errorf("the form does not show %q", want)
				}
			}
		})
	}
}

// TestPlanStartNewWithoutTheSeam: with no StartPlan the dialog dims the row,
// so it cannot be reached by name, but the action can still be run directly
// (applyKeyAction), and it must answer with a sentence pointing at the
// terminal and open no form.
func TestPlanStartNewWithoutTheSeam(t *testing.T) {
	m, _ := planModel(t, sess("a"))
	m.deps.StartPlan = nil
	nm, cmd := m.openPlanNewForm()
	m = nm.(Model)
	if cmd != nil {
		t.Fatal("a command was issued with no seam")
	}
	if m.modal != inputNormal || m.status != planNewUnavailableNotice {
		t.Fatalf("modal = %v status = %q, want no form and the not-available notice", m.modal, m.status)
	}
}

// newPlanRoot is the folder the new-plan tests start in: another project,
// in neither the switcher's list nor any search folder.
const newPlanRoot = "/code/fresh"

// TestPlanStartNewResults folds each kind of new-plan result into the
// dashboard.
//
// Load-bearing rows:
//   - "started in another folder": the dashboard switches to it, lists it in
//     the switcher (not Pinned), records the open, and dials master-plan in
//     that root with the row armed for the next load. Opening before the
//     switch would be undone by it, so the dial's root is the proof of order.
//   - "resumed in another folder": a reopened session switches too.
//   - "refused": the reason is shown, nothing switches, nothing is dialled.
//   - "started in the active project": no switch and no started row; the
//     pane opens as the change actions open it.
//   - "started in the main checkout of the worktree the dashboard was
//     started in": the same project under its other spelling, so no second
//     row and no switch away from the folder the owner opened.
//   - "stale project": a result from before a project switch does nothing.
func TestPlanStartNewResults(t *testing.T) {
	refusal := "this folder already has a plan (/code/fresh/.afk/plan/master-plan.md). Nothing was started."
	cases := []struct {
		name         string
		reply        PlanStartResult
		launchWT     bool
		staleGen     bool
		wantActive   string
		wantStarted  []string
		wantDialRoot string
		wantText     string
	}{
		{name: "started in another folder", reply: PlanStartResult{Outcome: PlanStarted, Root: newPlanRoot},
			wantActive: newPlanRoot, wantStarted: []string{newPlanRoot}, wantDialRoot: newPlanRoot,
			wantText: "Started a planning session for a new plan in " + newPlanRoot + "."},
		{name: "resumed in another folder", reply: PlanStartResult{Outcome: PlanResumed, Root: newPlanRoot},
			wantActive: newPlanRoot, wantStarted: []string{newPlanRoot}, wantDialRoot: newPlanRoot,
			wantText: "already open"},
		{name: "refused", reply: PlanStartResult{Outcome: PlanRefused, Reason: refusal, Root: newPlanRoot},
			wantActive: "/projects/api", wantText: refusal},
		{name: "started in the active project", reply: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"},
			wantActive: "/projects/api", wantDialRoot: "/projects/api", wantText: "new plan in /projects/api"},
		{name: "started in the main checkout of the worktree the dashboard was started in", launchWT: true,
			reply:      PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"},
			wantActive: "/projects/api/.wt/feature", wantDialRoot: "/projects/api", wantText: "new plan in /projects/api"},
		{name: "stale project", reply: PlanStartResult{Outcome: PlanStarted, Root: newPlanRoot}, staleGen: true,
			wantActive: "/projects/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := planModel(t, sess("a"))
			if tc.launchWT {
				m.projects = []project.Project{{Path: "/projects/api/.wt/feature", Name: "feature", Pinned: true}}
				m.repo = "/projects/api/.wt/feature"
				m.found = nil
				m.deps.LaunchMainCheckout = "/projects/api"
			}
			beforeRows := len(m.projects)
			before := m.status
			gen := m.projectGen
			if tc.staleGen {
				gen--
			}
			m, cmd := update(t, m, planStartedMsg{mode: PlanModeNew, res: tc.reply, gen: gen})
			if got := m.activeProjectPath(); got != tc.wantActive {
				t.Fatalf("active project = %q, want %q", got, tc.wantActive)
			}
			if !pathsEqual(m.startedProjects, tc.wantStarted) {
				t.Errorf("started = %v, want %v", m.startedProjects, tc.wantStarted)
			}
			if tc.staleGen {
				if cmd != nil || m.status != before || m.attachInFlight.name != "" {
					t.Fatalf("a stale result did something: cmd=%v status=%q dial=%q", cmd != nil, m.status, m.attachInFlight.name)
				}
				return
			}
			if !strings.Contains(m.status, tc.wantText) {
				t.Errorf("status = %q, want it to contain %q", m.status, tc.wantText)
			}
			if tc.wantDialRoot == "" {
				if len(m.projects) != beforeRows || m.attachInFlight.name != "" {
					t.Fatalf("a refusal changed something: rows=%d dial=%q", len(m.projects), m.attachInFlight.name)
				}
				if _, ok := paneDial(t, cmd); ok || len(rec.dials) != 0 {
					t.Fatal("a refusal dialled a pane")
				}
				return
			}
			if len(tc.wantStarted) == 0 && len(m.projects) != beforeRows {
				t.Fatalf("projects = %v, want no row added", projectPaths(m.projects))
			}
			if len(tc.wantStarted) > 0 {
				i, ok := indexOfProject(m.projects, newPlanRoot)
				if !ok {
					t.Fatalf("projects = %v, want %s listed", projectPaths(m.projects), newPlanRoot)
				}
				if m.projects[i].Pinned {
					t.Error("the started project is Pinned")
				}
				if m.repo != newPlanRoot {
					t.Errorf("repo = %q, want the switch to repoint it", m.repo)
				}
				if m.projectOpens[newPlanRoot] != 1 {
					t.Errorf("opens = %v, want the switch recorded once", m.projectOpens)
				}
				if _, ok := indexOfProject(m.projects, "/projects/api"); !ok {
					t.Errorf("projects = %v, the project left must stay listed", projectPaths(m.projects))
				}
			}
			if m.focus != focusRight || m.view != viewAgent {
				t.Fatalf("focus=%v view=%v, want the agent view focused", m.focus, m.view)
			}
			if m.planSelectPending != session.PlanSessionName {
				t.Fatalf("planSelectPending = %q, want the master-plan row armed", m.planSelectPending)
			}
			if _, ok := paneDial(t, cmd); !ok {
				t.Fatal("no pane was dialled")
			}
			if len(rec.dials) != 1 || rec.dials[0].Name != session.PlanSessionName || rec.dials[0].Worktree != tc.wantDialRoot {
				t.Fatalf("dials = %+v, want one for master-plan in %s", rec.dials, tc.wantDialRoot)
			}
		})
	}
}

// TestPlanStartNewEndToEnd is the action through the dialog, the form and
// the result, then the switcher: the started folder is the project on
// screen, the master-plan row is selected once discovery lists it, and the
// switcher (ctrl+space p) shows a row for it.
func TestPlanStartNewEndToEnd(t *testing.T) {
	m, rec := planModel(t, sess("a"))
	rec.reply = PlanStartResult{Outcome: PlanStarted, Root: newPlanRoot}
	m, _ = runPlanAction(t, m, "start a new plan")
	m = typeText(t, m, newPlanRoot)
	m, cmd := update(t, m, keyEnter)
	m, cmd = update(t, m, planStarted(t, cmd))
	if m.activeProjectPath() != newPlanRoot {
		t.Fatalf("active project = %q, want %q", m.activeProjectPath(), newPlanRoot)
	}
	attached, ok := paneDial(t, cmd)
	if !ok {
		t.Fatal("no pane was dialled")
	}
	plan := masterPlanSess()
	plan.WorktreePath = newPlanRoot
	m, _ = update(t, m, sessionsLoadedMsg{sessions: []Session{plan}, at: time.Unix(1_000_000, 0), gen: m.projectGen})
	if s, ok := m.selected(); !ok || s.Name != session.PlanSessionName {
		t.Fatalf("selected = %q, want master-plan", s.Name)
	}
	m, _ = update(t, m, attached)
	if m.pane == nil || m.paneName != session.PlanSessionName {
		t.Fatalf("pane = %q, want master-plan installed", m.paneName)
	}
	m.focus = focusList
	m, _ = update(t, m, leaderKeyPress)
	m, _ = update(t, m, keyRune('p'))
	if !m.switcherOpen {
		t.Fatal("ctrl+space p did not open the switcher")
	}
	if frame := render(m); !strings.Contains(frame, "fresh") || !strings.Contains(frame, "api") {
		t.Errorf("the switcher does not list both projects:\n%s", frame)
	}
}

// TestPlanStartNewRowSurvivesLaterReads: the started row is held for the run,
// so neither a later scan that does not find it nor a project-list read that
// does not name it drops it or moves the dashboard off it. Without the
// started source the next scan, a minute later, would switch the owner away
// from the project they just started.
func TestPlanStartNewRowSurvivesLaterReads(t *testing.T) {
	m, _ := planModel(t, sess("a"))
	m, _ = update(t, m, planStartedMsg{mode: PlanModeNew, res: PlanStartResult{Outcome: PlanStarted, Root: newPlanRoot}, gen: m.projectGen})
	if m.activeProjectPath() != newPlanRoot {
		t.Fatalf("setup: active project = %q, want %q", m.activeProjectPath(), newPlanRoot)
	}
	m, _ = update(t, m, projectScanMsg{projects: scannedProjects("/projects/api", "/roots/other")})
	m, _ = update(t, m, listedProjectsMsg{entries: []ListedProject{{Path: "/code/listed", Name: "listed"}}, path: listedConfigPath})
	if m.activeProjectPath() != newPlanRoot {
		t.Fatalf("active project = %q after the reads, want %q", m.activeProjectPath(), newPlanRoot)
	}
	want := []string{"/projects/api", "/roots/other", "/code/listed", newPlanRoot}
	if !pathsEqual(projectPaths(m.projects), want) {
		t.Fatalf("projects = %v, want %v", projectPaths(m.projects), want)
	}
	// A second start in the same folder adds no second row.
	m, _ = update(t, m, planStartedMsg{mode: PlanModeNew, res: PlanStartResult{Outcome: PlanResumed, Root: newPlanRoot}, gen: m.projectGen})
	if !pathsEqual(m.startedProjects, []string{newPlanRoot}) || !pathsEqual(projectPaths(m.projects), want) {
		t.Fatalf("started = %v projects = %v, want one row for %s", m.startedProjects, projectPaths(m.projects), newPlanRoot)
	}
}
