package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

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

// runPlanAction is ctrl+], the query, enter: the way the plan actions
// are reached, since they have no key after the leader.
func runPlanAction(t *testing.T, m Model, query string) (Model, tea.Cmd) {
	t.Helper()
	m, _ = update(t, m, leaderKeyPress)
	if !m.leaderOpen {
		t.Fatal("setup: ctrl+] did not open the actions dialog")
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
		// T13.6: fix is reached by "a bug": its name starts with f, focus's
		// key, and "broken" with b, build's, either of which would run that
		// action from an empty query instead of filtering.
		{"fix", "a bug", "The export button does nothing.", planCall{"/projects/api", PlanModeFix, "The export button does nothing."}, false},
		{"fix by its help line", "asks what is broken", "Search crashes.", planCall{"/projects/api", PlanModeFix, "Search crashes."}, false},
		{"fix text is trimmed", "a bug", "  Login hangs.  ", planCall{"/projects/api", PlanModeFix, "Login hangs."}, false},
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
		// T13.6: fix has its own note, saying what is broken is missing.
		{"empty fix", "a bug", "", planFixEmpty},
		{"blank fix", "a bug", "   ", planFixEmpty},
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
	for _, q := range []string{"amend", "document", "a bug"} {
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
		// T13.6: fix's notices are worded on their own, never "to  the plan".
		{name: "fix started", listed: true, reply: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"},
			mode: PlanModeFix, wantPane: true, wantText: "Started a planning session to fix a bug."},
		{name: "fix refused in the seam's words", reply: PlanStartResult{Outcome: PlanRefused, Reason: "You did not say what is broken.", Root: "/projects/api"},
			mode: PlanModeFix, wantText: "You did not say what is broken."},
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
	for _, q := range []string{"extend", "amend", "document", "a bug"} {
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
// switcher (ctrl+] p) shows a row for it.
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
		t.Fatal("ctrl+] p did not open the switcher")
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

// folderSeams records every PlanFolder and CreatePlanFolder call and holds
// what the test wants each to answer, on planSeams' pattern.
type folderSeams struct {
	decides   []string
	creates   []string
	decision  PlanFolderDecision
	createErr error
}

// folderModel is planModel with both T7.5 seams wired to recording fakes.
func folderModel(t *testing.T) (Model, *planSeams, *folderSeams) {
	t.Helper()
	m, rec := planModel(t, sess("a"))
	fs := &folderSeams{}
	m.deps.PlanFolder = func(text string) PlanFolderDecision {
		fs.decides = append(fs.decides, text)
		return fs.decision
	}
	m.deps.CreatePlanFolder = func(path string) error {
		fs.creates = append(fs.creates, path)
		return fs.createErr
	}
	return m, rec, fs
}

// folderPath is where the tests' profile location puts a new project.
const folderPath = "/code/projects/demo"

// submitPlanFolder runs "start a new plan", types text, presses enter and
// folds the PlanFolder decision back in, returning the model after it and
// whatever command the decision produced.
func submitPlanFolder(t *testing.T, m Model, text string) (Model, tea.Cmd) {
	t.Helper()
	m, _ = runPlanAction(t, m, "start a new plan")
	m = typeText(t, m, text)
	m, cmd := update(t, m, keyEnter)
	if cmd == nil {
		t.Fatal("enter issued no command: PlanFolder was not asked")
	}
	msg, ok := cmd().(planFolderDecidedMsg)
	if !ok {
		t.Fatalf("enter's command produced %T, want planFolderDecidedMsg", msg)
	}
	return update(t, m, msg)
}

// noStart fails when cmd, run, would call StartPlan or CreatePlanFolder.
func noStart(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case planStartedMsg:
		t.Fatal("a command started a planning session")
	case planFolderCreatedMsg:
		t.Fatal("a command created a folder")
	case tea.BatchMsg:
		for _, c := range msg {
			noStart(t, c)
		}
	}
}

// TestPlanFolderNameConfirmThenCreateThenStart types a bare name and answers
// the confirm each way. Load-bearing rows:
//   - "y" and "Y": the folder is created exactly once with the full path the
//     dialog showed, and only after the create's success is StartPlan called,
//     once, with (path, new, ""). The create and the start being two messages
//     is the T7.6 seam: a question goes between them later.
//   - "n" and "esc": neither seam is called and the notice says nothing was
//     created. A create on either would make a folder the owner refused.
//   - "a stray key": swallowed with the dialog still up, so a mistyped key
//     neither creates nor dismisses.
func TestPlanFolderNameConfirmThenCreateThenStart(t *testing.T) {
	cases := []struct {
		name       string
		answer     tea.Msg
		wantCreate bool
		wantOpen   bool
	}{
		{name: "y creates then starts", answer: keyRune('y'), wantCreate: true},
		{name: "Y creates then starts", answer: keyRune('Y'), wantCreate: true},
		{name: "n creates nothing", answer: keyRune('n')},
		{name: "N creates nothing", answer: keyRune('N')},
		{name: "esc creates nothing", answer: keyEsc},
		{name: "a stray key keeps the question", answer: keyRune('q'), wantOpen: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, fs := folderModel(t)
			fs.decision = PlanFolderDecision{Kind: PlanFolderCreate, Path: folderPath}
			m, cmd := submitPlanFolder(t, m, "demo")
			noStart(t, cmd)
			if len(fs.decides) != 1 || fs.decides[0] != "demo" {
				t.Fatalf("PlanFolder calls = %q, want exactly [demo]", fs.decides)
			}
			if m.modal != inputConfirmPlanFolder {
				t.Fatalf("modal = %v, want the folder confirm", m.modal)
			}
			frame := render(m)
			for _, want := range []string{"Create a new project", folderPath, "git repository", "y: yes", "n/esc: cancel"} {
				if !strings.Contains(frame, want) {
					t.Errorf("the confirm does not show %q:\n%s", want, frame)
				}
			}
			if len(fs.creates) != 0 || len(rec.calls) != 0 {
				t.Fatalf("creates=%v starts=%v before the answer, want none", fs.creates, rec.calls)
			}

			m, cmd = update(t, m, tc.answer)
			if tc.wantOpen {
				noStart(t, cmd)
				if m.modal != inputConfirmPlanFolder {
					t.Fatalf("modal = %v after a stray key, want the confirm still up", m.modal)
				}
				return
			}
			if m.modal != inputNormal {
				t.Fatalf("modal = %v after the answer, want closed", m.modal)
			}
			if !tc.wantCreate {
				noStart(t, cmd)
				if len(fs.creates) != 0 || len(rec.calls) != 0 {
					t.Fatalf("creates=%v starts=%v after no, want none", fs.creates, rec.calls)
				}
				if m.status != "Nothing was created." {
					t.Errorf("status = %q, want %q", m.status, "Nothing was created.")
				}
				return
			}
			if m.status != "Creating "+folderPath+"…" {
				t.Errorf("status = %q, want the creating notice", m.status)
			}
			if cmd == nil {
				t.Fatal("yes issued no command")
			}
			created, ok := cmd().(planFolderCreatedMsg)
			if !ok {
				t.Fatalf("yes produced %T, want planFolderCreatedMsg", created)
			}
			if len(fs.creates) != 1 || fs.creates[0] != folderPath {
				t.Fatalf("CreatePlanFolder calls = %q, want exactly [%s]", fs.creates, folderPath)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("StartPlan called %v before the create's result, want after", rec.calls)
			}
			m, cmd = update(t, m, created)
			if wantMade := "Made " + folderPath + " and made it a git repository. Starting a planning session in it…"; m.status != wantMade {
				t.Errorf("status = %q, want %q", m.status, wantMade)
			}
			got := planStarted(t, cmd)
			if got.mode != PlanModeNew {
				t.Errorf("start mode = %q, want %q", got.mode, PlanModeNew)
			}
			want := planCall{folderPath, PlanModeNew, ""}
			if len(rec.calls) != 1 || rec.calls[0] != want {
				t.Fatalf("StartPlan calls = %+v, want exactly %+v", rec.calls, want)
			}
			if len(fs.creates) != 1 {
				t.Fatalf("CreatePlanFolder called %d times, want 1", len(fs.creates))
			}
		})
	}
}

// TestPlanFolderExistingFolderOffer: a name whose folder is already there is
// refused with Exists, and the dashboard offers to plan in it. Load-bearing:
// the yes calls StartPlan once with the path and never CreatePlanFolder (the
// folder is there; making it again would fail or, worse, touch it), and the
// no calls neither. The T7.6 GitHub seams are wired and never called on any
// row: the question is for a folder just made, and one that already existed
// may well have a remote of its own.
func TestPlanFolderExistingFolderOffer(t *testing.T) {
	cases := []struct {
		name      string
		answer    tea.Msg
		wantStart bool
	}{
		{name: "yes plans in it", answer: keyRune('y'), wantStart: true},
		{name: "no does nothing", answer: keyRune('n')},
		{name: "esc does nothing", answer: keyEsc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, fs := folderModel(t)
			gs := wireGitHub(&m)
			defer func() {
				if len(gs.offers) != 0 || len(gs.creates) != 0 {
					t.Errorf("GitHub seams called for a folder that exists: offers=%v creates=%v", gs.offers, gs.creates)
				}
			}()
			fs.decision = PlanFolderDecision{
				Kind: PlanFolderRefused, Exists: true, Path: folderPath,
				Reason: folderPath + " already exists. Nothing was created.",
			}
			m, cmd := submitPlanFolder(t, m, "demo")
			noStart(t, cmd)
			if m.modal != inputConfirmPlanFolder {
				t.Fatalf("modal = %v, want the plan-in-it offer", m.modal)
			}
			frame := render(m)
			for _, want := range []string{"already exists", folderPath, "Plan in it?"} {
				if !strings.Contains(frame, want) {
					t.Errorf("the offer does not show %q:\n%s", want, frame)
				}
			}
			m, cmd = update(t, m, tc.answer)
			if m.modal != inputNormal {
				t.Fatalf("modal = %v after the answer, want closed", m.modal)
			}
			if !tc.wantStart {
				noStart(t, cmd)
				if len(rec.calls) != 0 || len(fs.creates) != 0 {
					t.Fatalf("starts=%v creates=%v, want none", rec.calls, fs.creates)
				}
				return
			}
			planStarted(t, cmd)
			want := planCall{folderPath, PlanModeNew, ""}
			if len(rec.calls) != 1 || rec.calls[0] != want {
				t.Fatalf("StartPlan calls = %+v, want exactly %+v", rec.calls, want)
			}
			if len(fs.creates) != 0 {
				t.Fatalf("CreatePlanFolder called %v for a folder that exists", fs.creates)
			}
		})
	}
}

// TestPlanFolderUseStartsAsTyped: a path is used as T7.4 used it: StartPlan
// once with the path, no confirm, nothing created. Load-bearing: the "~/"
// row, because the start expands it and the decision must hand it on as
// typed (A6), and the "empty Path" row, which falls back to the text rather
// than starting in "".
func TestPlanFolderUseStartsAsTyped(t *testing.T) {
	cases := []struct {
		name string
		text string
		path string
		want string
	}{
		{name: "a full path", text: "/code/fresh", path: "/code/fresh", want: "/code/fresh"},
		{name: "a ~/ path", text: "~/code/fresh", path: "~/code/fresh", want: "~/code/fresh"},
		{name: "empty Path falls back to the text", text: "/code/fresh", path: "", want: "/code/fresh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, fs := folderModel(t)
			fs.decision = PlanFolderDecision{Kind: PlanFolderUse, Path: tc.path}
			m, cmd := submitPlanFolder(t, m, tc.text)
			if m.modal != inputNormal {
				t.Fatalf("modal = %v, want no confirm for a path", m.modal)
			}
			if m.status != "Starting a planning session in "+tc.want+"…" {
				t.Errorf("status = %q, want T7.4's starting notice", m.status)
			}
			planStarted(t, cmd)
			want := planCall{tc.want, PlanModeNew, ""}
			if len(rec.calls) != 1 || rec.calls[0] != want {
				t.Fatalf("StartPlan calls = %+v, want exactly %+v", rec.calls, want)
			}
			if len(fs.creates) != 0 {
				t.Fatalf("CreatePlanFolder called %v for a path", fs.creates)
			}
		})
	}
}

// TestPlanFolderRefusalShowsTheReason: a refusal shows its reason as a
// warning and opens nothing. Load-bearing: "a bad name" (the commonest
// refusal, whose reason names the allowed characters), "a create with no
// path" (a decision the seam filled badly must not open a confirm for ""),
// and "no reason", which still says something rather than leaving the owner
// with a silent dashboard.
func TestPlanFolderRefusalShowsTheReason(t *testing.T) {
	bad := `"my app" is not a name a new project can have: use letters, digits, ".", "_" and "-", starting with a letter or digit. Nothing was created.`
	cases := []struct {
		name     string
		decision PlanFolderDecision
		want     string
	}{
		{name: "a bad name", decision: PlanFolderDecision{Kind: PlanFolderRefused, Reason: bad}, want: bad},
		{name: "no location set", decision: PlanFolderDecision{Kind: PlanFolderRefused, Reason: "No project location is set. Nothing was created."}, want: "No project location is set. Nothing was created."},
		{name: "exists with no path", decision: PlanFolderDecision{Kind: PlanFolderRefused, Exists: true, Reason: "It exists. Nothing was created."}, want: "It exists. Nothing was created."},
		{name: "a create with no path", decision: PlanFolderDecision{Kind: PlanFolderCreate}, want: "That cannot be used as a folder for a new plan. Nothing was created."},
		{name: "no reason", decision: PlanFolderDecision{}, want: "That cannot be used as a folder for a new plan. Nothing was created."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, fs := folderModel(t)
			fs.decision = tc.decision
			m, cmd := submitPlanFolder(t, m, "my app")
			noStart(t, cmd)
			if m.modal != inputNormal {
				t.Fatalf("modal = %v, want nothing opened", m.modal)
			}
			if m.status != tc.want {
				t.Errorf("status = %q, want %q", m.status, tc.want)
			}
			if len(rec.calls) != 0 || len(fs.creates) != 0 {
				t.Fatalf("starts=%v creates=%v, want none", rec.calls, fs.creates)
			}
		})
	}
}

// TestPlanFolderCreateErrorStartsNothing: a create that fails part-way shows
// the seam's sentence as it is (it says what was left on disk) and never
// starts a planning session in a folder that is not a git repository.
func TestPlanFolderCreateErrorStartsNothing(t *testing.T) {
	m, rec, fs := folderModel(t)
	fs.decision = PlanFolderDecision{Kind: PlanFolderCreate, Path: folderPath}
	fs.createErr = errors.New("The folder " + folderPath + " was made and is left on disk, but git init failed: no git. Nothing was removed.")
	m, _ = submitPlanFolder(t, m, "demo")
	m, cmd := update(t, m, keyRune('y'))
	created, ok := cmd().(planFolderCreatedMsg)
	if !ok {
		t.Fatalf("yes produced %T, want planFolderCreatedMsg", created)
	}
	m, cmd = update(t, m, created)
	noStart(t, cmd)
	if m.status != fs.createErr.Error() {
		t.Errorf("status = %q, want the create's error", m.status)
	}
	if len(fs.creates) != 1 {
		t.Fatalf("CreatePlanFolder called %d times, want 1", len(fs.creates))
	}
	if len(rec.calls) != 0 {
		t.Fatalf("StartPlan called %v after a failed create", rec.calls)
	}
}

// TestPlanFolderCreateUnavailable: PlanFolder wired but CreatePlanFolder not
// (only a half-built dashboard): the y says so and creates nothing.
func TestPlanFolderCreateUnavailable(t *testing.T) {
	m, rec, fs := folderModel(t)
	m.deps.CreatePlanFolder = nil
	fs.decision = PlanFolderDecision{Kind: PlanFolderCreate, Path: folderPath}
	m, _ = submitPlanFolder(t, m, "demo")
	m, cmd := update(t, m, keyRune('y'))
	noStart(t, cmd)
	if m.modal != inputNormal || m.status != planFolderUnavailableNotice {
		t.Fatalf("modal=%v status=%q, want closed with the not-available notice", m.modal, m.status)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("StartPlan called %v", rec.calls)
	}
}

// TestPlanFolderNilKeepsTheT74Form: with no PlanFolder seam the form is
// exactly T7.4's: the text, even a bare name, goes to StartPlan unchanged
// and no decision is asked for.
func TestPlanFolderNilKeepsTheT74Form(t *testing.T) {
	for _, text := range []string{"demo", "/code/fresh"} {
		t.Run(text, func(t *testing.T) {
			m, rec := planModel(t, sess("a"))
			m, _ = runPlanAction(t, m, "start a new plan")
			m = typeText(t, m, text)
			m, cmd := update(t, m, keyEnter)
			planStarted(t, cmd)
			want := planCall{text, PlanModeNew, ""}
			if len(rec.calls) != 1 || rec.calls[0] != want {
				t.Fatalf("StartPlan calls = %+v, want exactly %+v", rec.calls, want)
			}
			if m.status != "Starting a planning session in "+text+"…" {
				t.Errorf("status = %q, want T7.4's notice", m.status)
			}
		})
	}
}

// TestPlanFolderDroppedAcrossAProjectSwitch: each step that crosses a
// project switch does the safe thing. Load-bearing rows:
//   - "between submit and decision": the decision is dropped, no confirm
//     pops up over the other project, nothing is called.
//   - "between confirm and yes": the yes creates nothing.
//   - "between create and its result": the folder exists, so the owner is
//     told, but no planning session starts and moves them off the project
//     they switched to.
func TestPlanFolderDroppedAcrossAProjectSwitch(t *testing.T) {
	t.Run("between submit and decision", func(t *testing.T) {
		m, rec, fs := folderModel(t)
		fs.decision = PlanFolderDecision{Kind: PlanFolderCreate, Path: folderPath}
		m, _ = runPlanAction(t, m, "start a new plan")
		m = typeText(t, m, "demo")
		m, cmd := update(t, m, keyEnter)
		msg := cmd().(planFolderDecidedMsg)
		m.projectGen++
		m, cmd = update(t, m, msg)
		noStart(t, cmd)
		if m.modal != inputNormal {
			t.Fatalf("modal = %v, want the decision dropped", m.modal)
		}
		if len(rec.calls) != 0 || len(fs.creates) != 0 {
			t.Fatalf("starts=%v creates=%v, want none", rec.calls, fs.creates)
		}
	})
	t.Run("between confirm and yes", func(t *testing.T) {
		m, rec, fs := folderModel(t)
		fs.decision = PlanFolderDecision{Kind: PlanFolderCreate, Path: folderPath}
		m, _ = submitPlanFolder(t, m, "demo")
		m.projectGen++
		m, cmd := update(t, m, keyRune('y'))
		noStart(t, cmd)
		if !strings.Contains(m.status, "nothing was created") {
			t.Errorf("status = %q, want the project-changed notice", m.status)
		}
		if len(rec.calls) != 0 || len(fs.creates) != 0 {
			t.Fatalf("starts=%v creates=%v, want none", rec.calls, fs.creates)
		}
	})
	t.Run("between create and its result", func(t *testing.T) {
		m, rec, fs := folderModel(t)
		fs.decision = PlanFolderDecision{Kind: PlanFolderCreate, Path: folderPath}
		m, _ = submitPlanFolder(t, m, "demo")
		m, cmd := update(t, m, keyRune('y'))
		created := cmd().(planFolderCreatedMsg)
		m.projectGen++
		m, cmd = update(t, m, created)
		noStart(t, cmd)
		if !strings.Contains(m.status, "Made "+folderPath) || !strings.Contains(m.status, "no planning session was started") {
			t.Errorf("status = %q, want the made-but-not-started notice", m.status)
		}
		if len(rec.calls) != 0 {
			t.Fatalf("StartPlan called %v after a project switch", rec.calls)
		}
	})
}

// TestPlanFolderConfirmKeys pins the folder confirm's routing at the table
// level, as TestRouteKeyMatrix does for the other y/n modals. Load-bearing:
// y/Y confirm and n/esc deny, and every other key, enter included, is
// swallowed, so only an explicit y can create a folder.
func TestPlanFolderConfirmKeys(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	cases := []struct {
		name string
		key  tea.KeyPressMsg
		want keyDecision
	}{
		{"y confirms", press('y', "y"), keyDecision{routeAction, actionConfirm}},
		{"Y confirms", press('Y', "Y"), keyDecision{routeAction, actionConfirm}},
		{"n denies", press('n', "n"), keyDecision{routeAction, actionDeny}},
		{"N denies", press('N', "N"), keyDecision{routeAction, actionDeny}},
		{"esc cancels", tea.KeyPressMsg{Code: tea.KeyEsc}, keyDecision{routeAction, actionCancel}},
		{"enter is swallowed", tea.KeyPressMsg{Code: tea.KeyEnter}, keyDecision{kind: routeIgnored}},
		{"q is swallowed", press('q', "q"), keyDecision{kind: routeIgnored}},
		{"ctrl+space is swallowed", tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl}, keyDecision{kind: routeIgnored}},
		{"ctrl+] (the leader) is swallowed", tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}, keyDecision{kind: routeIgnored}},
		{"ctrl+c quits", chord('c'), keyDecision{routeAction, actionQuit}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reg.route(inputConfirmPlanFolder, routeConds{}, tc.key); got != tc.want {
				t.Fatalf("route(%q) = {%v %v}, want {%v %v}", tc.key.String(), got.kind, got.action, tc.want.kind, tc.want.action)
			}
		})
	}
	if !(inputConfirmPlanFolder).isModal() {
		t.Error("the folder confirm is not a modal")
	}
	if got := (modeState{modal: inputConfirmPlanFolder}); activeInputMode(got) != inputConfirmPlanFolder {
		t.Errorf("activeInputMode = %v, want the folder confirm", activeInputMode(got))
	}
}

// TestPlanFolderFormSaysANameWorks: with the seam wired the form says a name
// is accepted and where it goes, each line whole (a dialog line is cut at
// the dialog's width, so a sentence that does not fit loses its end).
func TestPlanFolderFormSaysANameWorks(t *testing.T) {
	m, _, _ := folderModel(t)
	m, _ = runPlanAction(t, m, "start a new plan")
	frame := render(m)
	for _, want := range []string{planNewQuestion, planNewNameNote} {
		if !strings.Contains(frame, want) {
			t.Errorf("the form does not show %q:\n%s", want, frame)
		}
	}
}

// githubSeams records every GitHubOffer and CreateGitHubRepo call (T7.6) and
// holds the answers the test wants them to give.
type githubSeams struct {
	offers  []string
	creates []githubCreateCall
	offer   GitHubOffer
	line    string
}

// githubCreateCall is one CreateGitHubRepo call as the fake saw it.
type githubCreateCall struct{ path, repo string }

// githubRepoName is the repository the tests' offer names.
const githubRepoName = "owner/demo"

// githubCreatedLine is the outcome line the fake gives for a made repository.
const githubCreatedLine = "Made the private GitHub repository owner/demo and set it as origin. Nothing was pushed."

// wireGitHub wires both T7.6 seams on m to recording fakes. By default the
// offer asks about githubRepoName and the create reports it made.
func wireGitHub(m *Model) *githubSeams {
	gs := &githubSeams{
		offer: GitHubOffer{Ask: true, Repo: githubRepoName},
		line:  githubCreatedLine,
	}
	m.deps.GitHubOffer = func(path string) GitHubOffer {
		gs.offers = append(gs.offers, path)
		return gs.offer
	}
	m.deps.CreateGitHubRepo = func(path, repo string) string {
		gs.creates = append(gs.creates, githubCreateCall{path, repo})
		return gs.line
	}
	return gs
}

// githubModel is folderModel with the GitHub seams wired too.
func githubModel(t *testing.T) (Model, *planSeams, *folderSeams, *githubSeams) {
	t.Helper()
	m, rec, fs := folderModel(t)
	gs := wireGitHub(&m)
	return m, rec, fs, gs
}

// makeFolder types demo, says y to the folder confirm and folds the create's
// success back in, returning the model and the command the create's result
// produced (the GitHubOffer call, when the seam is wired). It asserts the
// T7.6 step did not start planning on its own.
func makeFolder(t *testing.T, m Model, fs *folderSeams) (Model, tea.Cmd) {
	t.Helper()
	fs.decision = PlanFolderDecision{Kind: PlanFolderCreate, Path: folderPath}
	m, _ = submitPlanFolder(t, m, "demo")
	m, cmd := update(t, m, keyRune('y'))
	created, ok := cmd().(planFolderCreatedMsg)
	if !ok {
		t.Fatalf("yes produced %T, want planFolderCreatedMsg", created)
	}
	return update(t, m, created)
}

// askGitHub runs makeFolder and then the offer, returning the model with
// whatever the offer raised.
func askGitHub(t *testing.T, m Model, fs *folderSeams) (Model, tea.Cmd) {
	t.Helper()
	m, cmd := makeFolder(t, m, fs)
	if want := "Made " + folderPath + " and made it a git repository. Checking GitHub…"; m.status != want {
		t.Errorf("status after the create = %q, want %q", m.status, want)
	}
	if cmd == nil {
		t.Fatal("the create's result issued no command: GitHubOffer was not asked")
	}
	msg, ok := cmd().(githubOfferMsg)
	if !ok {
		t.Fatalf("the create's result produced %T, want githubOfferMsg", msg)
	}
	return update(t, m, msg)
}

// finishStart runs cmd, which must be the start, checks StartPlan was called
// once in the new folder with lead carried on the message, then folds a
// started result back in and returns the notice it left.
func finishStart(t *testing.T, m Model, cmd tea.Cmd, rec *planSeams, lead string) string {
	t.Helper()
	rec.reply = PlanStartResult{Outcome: PlanStarted, Root: folderPath}
	got := planStarted(t, cmd)
	want := planCall{folderPath, PlanModeNew, ""}
	if len(rec.calls) != 1 || rec.calls[0] != want {
		t.Fatalf("StartPlan calls = %+v, want exactly %+v", rec.calls, want)
	}
	if got.lead != lead {
		t.Errorf("start lead = %q, want %q", got.lead, lead)
	}
	m, _ = update(t, m, got)
	return m.status
}

// TestGitHubRepoQuestionAnswers answers the GitHub question each way after a
// folder is made by name. Load-bearing rows:
//   - "y" and "Y": CreateGitHubRepo is called exactly once with the folder and
//     the repository the dialog named, and only after its line is back does
//     planning start, with that line leading the notice once the pane opens.
//     A start before the create's answer would let the plan notice bury the
//     outcome; a second create would make (or fail on) a second repository.
//   - "n", "N" and "esc": nothing is made on GitHub and planning starts all
//     the same, the notice leading with the declined line. A create here
//     would make a repository the owner refused; no start would leave the
//     owner with a folder and nothing running.
//   - "enter" and "a stray key": swallowed with the question still up, so the
//     default is no and a mistyped key neither makes nor dismisses.
func TestGitHubRepoQuestionAnswers(t *testing.T) {
	cases := []struct {
		name       string
		answer     tea.Msg
		wantCreate bool
		wantOpen   bool
	}{
		{name: "y makes the repository then starts", answer: keyRune('y'), wantCreate: true},
		{name: "Y makes the repository then starts", answer: keyRune('Y'), wantCreate: true},
		{name: "n makes nothing and starts", answer: keyRune('n')},
		{name: "N makes nothing and starts", answer: keyRune('N')},
		{name: "esc makes nothing and starts", answer: keyEsc},
		{name: "enter keeps the question", answer: keyEnter, wantOpen: true},
		{name: "a stray key keeps the question", answer: keyRune('q'), wantOpen: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, fs, gs := githubModel(t)
			m, cmd := askGitHub(t, m, fs)
			noStart(t, cmd)
			if len(gs.offers) != 1 || gs.offers[0] != folderPath {
				t.Fatalf("GitHubOffer calls = %q, want exactly [%s]", gs.offers, folderPath)
			}
			if m.modal != inputConfirmGitHubRepo {
				t.Fatalf("modal = %v, want the GitHub question", m.modal)
			}
			frame := render(m)
			for _, want := range []string{"Create a GitHub repository?", githubRepoName, "private", "Nothing is pushed", "y: yes", "n/esc: no"} {
				if !strings.Contains(frame, want) {
					t.Errorf("the question does not show %q:\n%s", want, frame)
				}
			}
			if len(gs.creates) != 0 || len(rec.calls) != 0 {
				t.Fatalf("creates=%v starts=%v before the answer, want none", gs.creates, rec.calls)
			}

			m, cmd = update(t, m, tc.answer)
			if tc.wantOpen {
				noStart(t, cmd)
				if m.modal != inputConfirmGitHubRepo {
					t.Fatalf("modal = %v after %s, want the question still up", m.modal, tc.name)
				}
				if len(gs.creates) != 0 {
					t.Fatalf("CreateGitHubRepo called %v", gs.creates)
				}
				return
			}
			if m.modal != inputNormal {
				t.Fatalf("modal = %v after the answer, want closed", m.modal)
			}
			if !tc.wantCreate {
				if len(gs.creates) != 0 {
					t.Fatalf("CreateGitHubRepo called %v after no", gs.creates)
				}
				if m.status != githubRepoDeclined+" Starting the planning session…" {
					t.Errorf("status = %q, want the declined line then starting", m.status)
				}
				notice := finishStart(t, m, cmd, rec, githubRepoDeclined)
				if !strings.HasPrefix(notice, "No GitHub repository was made. Started a planning session") {
					t.Errorf("notice once started = %q, want it to lead with the declined line", notice)
				}
				return
			}
			if m.status != "Making "+githubRepoName+" on GitHub…" {
				t.Errorf("status = %q, want the making notice", m.status)
			}
			if cmd == nil {
				t.Fatal("yes issued no command")
			}
			made, ok := cmd().(githubRepoCreatedMsg)
			if !ok {
				t.Fatalf("yes produced %T, want githubRepoCreatedMsg", made)
			}
			want := githubCreateCall{folderPath, githubRepoName}
			if len(gs.creates) != 1 || gs.creates[0] != want {
				t.Fatalf("CreateGitHubRepo calls = %+v, want exactly %+v", gs.creates, want)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("StartPlan called %v before the create's line, want after", rec.calls)
			}
			m, cmd = update(t, m, made)
			notice := finishStart(t, m, cmd, rec, githubCreatedLine)
			if !strings.HasPrefix(notice, githubCreatedLine+" Started a planning session") {
				t.Errorf("notice once started = %q, want it to lead with the outcome line", notice)
			}
			if len(gs.creates) != 1 || len(gs.offers) != 1 {
				t.Fatalf("offers=%d creates=%d, want one each", len(gs.offers), len(gs.creates))
			}
		})
	}
}

// TestGitHubOfferNotAsked: when the offer says not to ask, no question is
// raised and planning starts with the offer's notice as the lead. Load-bearing
// rows: "gh missing" and "logged out" (the two the README promises: the owner
// is told why and what to run, instead of being asked), "ask with no repo" (a
// badly filled offer must not raise a question naming nothing), and "no
// notice" (still starts, worded as T7.5 would).
func TestGitHubOfferNotAsked(t *testing.T) {
	missing := "No GitHub repository offered: gh (the GitHub command) is not installed. Install it from https://cli.github.com, run `gh auth login`, then `gh repo create --private` in the folder."
	loggedOut := "No GitHub repository offered: gh is not logged in. Run `gh auth login`, then `gh repo create --private` in the folder."
	cases := []struct {
		name  string
		offer GitHubOffer
		lead  string
	}{
		{name: "gh missing", offer: GitHubOffer{Notice: missing}, lead: missing},
		{name: "logged out", offer: GitHubOffer{Notice: loggedOut}, lead: loggedOut},
		{name: "ask with no repo", offer: GitHubOffer{Ask: true, Notice: "No account."}, lead: "No account."},
		{name: "no notice", offer: GitHubOffer{}, lead: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, fs, gs := githubModel(t)
			gs.offer = tc.offer
			m, cmd := askGitHub(t, m, fs)
			if m.modal != inputNormal {
				t.Fatalf("modal = %v, want no question", m.modal)
			}
			starting := "Starting a planning session in " + folderPath + "…"
			if tc.lead != "" {
				// The path is left out after a lead, so the toast's three
				// lines hold the lead whole (startPlanAfterGitHub).
				starting = "Starting the planning session…"
			}
			if m.status != withLead(tc.lead, starting) {
				t.Errorf("status = %q, want the offer's notice then starting", m.status)
			}
			notice := finishStart(t, m, cmd, rec, tc.lead)
			if !strings.HasPrefix(notice, withLead(tc.lead, "Started a planning session for a new plan in "+folderPath)) {
				t.Errorf("notice once started = %q, want it to lead with %q", notice, tc.lead)
			}
			if len(gs.creates) != 0 {
				t.Fatalf("CreateGitHubRepo called %v without a question", gs.creates)
			}
		})
	}
}

// TestGitHubRepoFailureStillStarts: whatever line the create returns is shown
// and planning starts. Load-bearing: "name taken" and "network error" (the
// folder is fine without a remote, so a failure must not strand the owner),
// and "empty line", which still says something rather than nothing.
func TestGitHubRepoFailureStillStarts(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "name taken", line: "GitHub already has a repository named owner/demo, so none was made. The folder is left as it is.",
			want: "GitHub already has a repository named owner/demo, so none was made. The folder is left as it is."},
		{name: "network error", line: "Could not make the GitHub repository owner/demo: error connecting to api.github.com. The folder is left as it is.",
			want: "Could not make the GitHub repository owner/demo: error connecting to api.github.com. The folder is left as it is."},
		{name: "no origin", line: "Made the private GitHub repository owner/demo, but could not set it as origin: boom. Run `git remote add origin https://github.com/owner/demo.git` in " + folderPath + ".",
			want: "Made the private GitHub repository owner/demo, but could not set it as origin: boom."},
		{name: "empty line", line: "  ", want: githubRepoNoAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, fs, gs := githubModel(t)
			gs.line = tc.line
			m, _ = askGitHub(t, m, fs)
			m, cmd := update(t, m, keyRune('y'))
			made := cmd().(githubRepoCreatedMsg)
			m, cmd = update(t, m, made)
			if !strings.HasPrefix(m.status, tc.want) {
				t.Errorf("status = %q, want it to lead with %q", m.status, tc.want)
			}
			lead := strings.TrimSpace(tc.line)
			if lead == "" {
				lead = githubRepoNoAnswer
			}
			notice := finishStart(t, m, cmd, rec, lead)
			if !strings.HasPrefix(notice, tc.want) || !strings.Contains(notice, "Started a planning session") {
				t.Errorf("notice once started = %q, want the line then the start", notice)
			}
		})
	}
}

// TestGitHubCreateUnavailable: GitHubOffer wired but CreateGitHubRepo not
// (only a half-built dashboard): the y says so, makes nothing, and planning
// still starts.
func TestGitHubCreateUnavailable(t *testing.T) {
	m, rec, fs, gs := githubModel(t)
	m.deps.CreateGitHubRepo = nil
	m, _ = askGitHub(t, m, fs)
	m, cmd := update(t, m, keyRune('y'))
	if m.modal != inputNormal {
		t.Fatalf("modal = %v, want closed", m.modal)
	}
	notice := finishStart(t, m, cmd, rec, githubRepoUnavailable)
	if !strings.HasPrefix(notice, githubRepoUnavailable+" Started") {
		t.Errorf("notice = %q, want the unavailable line then the start", notice)
	}
	if len(gs.creates) != 0 {
		t.Fatalf("CreateGitHubRepo called %v", gs.creates)
	}
}

// TestGitHubNotAskedForOtherFlows: only a folder made by name gets the
// question. Load-bearing rows: "a folder that exists" (planning in a folder
// the owner already had; it may have a remote) and "a typed path" (T7.4's
// flow, which makes nothing), each with the GitHub seams wired so a call
// would be seen; and "nil GitHubOffer", where a wired CreateGitHubRepo is
// still never reached and T7.5's notice is unchanged.
func TestGitHubNotAskedForOtherFlows(t *testing.T) {
	t.Run("a folder that exists", func(t *testing.T) {
		m, rec, fs, gs := githubModel(t)
		fs.decision = PlanFolderDecision{Kind: PlanFolderRefused, Exists: true, Path: folderPath}
		m, _ = submitPlanFolder(t, m, "demo")
		m, cmd := update(t, m, keyRune('y'))
		planStarted(t, cmd)
		if len(rec.calls) != 1 || len(gs.offers) != 0 || len(gs.creates) != 0 {
			t.Fatalf("starts=%v offers=%v creates=%v, want one start and no GitHub call", rec.calls, gs.offers, gs.creates)
		}
		if m.modal != inputNormal {
			t.Fatalf("modal = %v", m.modal)
		}
	})
	t.Run("a typed path", func(t *testing.T) {
		m, rec, fs, gs := githubModel(t)
		fs.decision = PlanFolderDecision{Kind: PlanFolderUse, Path: "/code/fresh"}
		_, cmd := submitPlanFolder(t, m, "/code/fresh")
		planStarted(t, cmd)
		if len(rec.calls) != 1 || len(gs.offers) != 0 || len(gs.creates) != 0 {
			t.Fatalf("starts=%v offers=%v creates=%v, want one start and no GitHub call", rec.calls, gs.offers, gs.creates)
		}
	})
	t.Run("nil GitHubOffer", func(t *testing.T) {
		m, rec, fs, gs := githubModel(t)
		m.deps.GitHubOffer = nil
		m, cmd := makeFolder(t, m, fs)
		if want := "Made " + folderPath + " and made it a git repository. Starting a planning session in it…"; m.status != want {
			t.Errorf("status = %q, want T7.5's %q", m.status, want)
		}
		got := planStarted(t, cmd)
		if got.lead != "" || len(rec.calls) != 1 || len(gs.creates) != 0 {
			t.Fatalf("lead=%q starts=%v creates=%v, want T7.5's start alone", got.lead, rec.calls, gs.creates)
		}
	})
}

// TestGitHubDroppedAcrossAProjectSwitch: each GitHub step that crosses a
// project switch makes and starts nothing it should not. Load-bearing rows:
//   - "between create and offer": no question pops up over the other
//     project and nothing starts; the owner is told the folder was made.
//   - "between question and yes": nothing is made on GitHub.
//   - "between create and its line": the line is still said (the repository
//     exists whatever the owner did), but planning does not start.
//   - "between start and its result": the lead is still said, since nothing
//     else will ever show it.
func TestGitHubDroppedAcrossAProjectSwitch(t *testing.T) {
	t.Run("between create and offer", func(t *testing.T) {
		m, rec, fs, gs := githubModel(t)
		m, cmd := makeFolder(t, m, fs)
		msg := cmd().(githubOfferMsg)
		m.projectGen++
		m, cmd = update(t, m, msg)
		noStart(t, cmd)
		if m.modal != inputNormal {
			t.Fatalf("modal = %v, want no question", m.modal)
		}
		if !strings.Contains(m.status, "Made "+folderPath) || !strings.Contains(m.status, "no planning session was started") {
			t.Errorf("status = %q, want the made-but-not-started notice", m.status)
		}
		if len(rec.calls) != 0 || len(gs.creates) != 0 {
			t.Fatalf("starts=%v creates=%v, want none", rec.calls, gs.creates)
		}
	})
	t.Run("between question and yes", func(t *testing.T) {
		m, rec, fs, gs := githubModel(t)
		m, _ = askGitHub(t, m, fs)
		m.projectGen++
		m, cmd := update(t, m, keyRune('y'))
		noStart(t, cmd)
		if cmd != nil {
			t.Fatal("a stale yes issued a command")
		}
		if m.status != "The project changed, so no GitHub repository was made." {
			t.Errorf("status = %q, want the project-changed notice", m.status)
		}
		if len(rec.calls) != 0 || len(gs.creates) != 0 {
			t.Fatalf("starts=%v creates=%v, want none", rec.calls, gs.creates)
		}
	})
	t.Run("between create and its line", func(t *testing.T) {
		m, rec, fs, _ := githubModel(t)
		m, _ = askGitHub(t, m, fs)
		m, cmd := update(t, m, keyRune('y'))
		made := cmd().(githubRepoCreatedMsg)
		m.projectGen++
		m, cmd = update(t, m, made)
		noStart(t, cmd)
		if !strings.HasPrefix(m.status, githubCreatedLine) || !strings.Contains(m.status, "no planning session was started") {
			t.Errorf("status = %q, want the line and not started", m.status)
		}
		if len(rec.calls) != 0 {
			t.Fatalf("StartPlan called %v after a project switch", rec.calls)
		}
	})
	t.Run("between start and its result", func(t *testing.T) {
		m, _, fs, _ := githubModel(t)
		m, _ = askGitHub(t, m, fs)
		m, cmd := update(t, m, keyRune('n'))
		got := planStarted(t, cmd)
		m.projectGen++
		m, cmd = update(t, m, got)
		if cmd != nil {
			t.Fatal("a stale start did something")
		}
		if m.status != githubRepoDeclined {
			t.Errorf("status = %q, want the lead alone", m.status)
		}
	})
}

// TestPlanStartLead: a lead goes before every notice applyPlanStarted sets,
// and an empty one changes nothing. Load-bearing rows: "refused" (the
// outcome must survive a refusal too), "open offer" (the question's modal
// would otherwise clear it), and "no lead", which is every start before T7.6
// and must read exactly as it did.
func TestPlanStartLead(t *testing.T) {
	const lead = "No GitHub repository was made."
	cases := []struct {
		name  string
		res   PlanStartResult
		lead  string
		want  string
		modal inputMode
	}{
		{name: "started", res: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"}, lead: lead,
			want: lead + " Started a planning session for a new plan in /projects/api."},
		{name: "refused", res: PlanStartResult{Outcome: PlanRefused, Reason: "No."}, lead: lead, want: lead + " No."},
		{name: "open offer", res: PlanStartResult{Outcome: PlanRefused, Open: true, Root: "/projects/api"}, lead: lead,
			want: lead, modal: inputConfirmPlanOpen},
		{name: "no lead", res: PlanStartResult{Outcome: PlanStarted, Root: "/projects/api"},
			want: "Started a planning session for a new plan in /projects/api."},
		{name: "no lead refused", res: PlanStartResult{Outcome: PlanRefused, Reason: "No."}, want: "No."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := planModel(t, sess("a"))
			m, _ = update(t, m, planStartedMsg{mode: PlanModeNew, res: tc.res, gen: m.projectGen, lead: tc.lead})
			if m.status != tc.want {
				t.Errorf("status = %q, want %q", m.status, tc.want)
			}
			if m.modal != tc.modal {
				t.Errorf("modal = %v, want %v", m.modal, tc.modal)
			}
		})
	}
}

// TestGitHubRepoConfirmKeys pins the GitHub question's routing at the table
// level, as TestPlanFolderConfirmKeys does for the folder confirm.
// Load-bearing: y/Y confirm, n/N deny and esc cancels, and every other key,
// enter included, is swallowed, so the default is no and only an explicit y
// makes a repository.
func TestGitHubRepoConfirmKeys(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	cases := []struct {
		name string
		key  tea.KeyPressMsg
		want keyDecision
	}{
		{"y confirms", press('y', "y"), keyDecision{routeAction, actionConfirm}},
		{"Y confirms", press('Y', "Y"), keyDecision{routeAction, actionConfirm}},
		{"n denies", press('n', "n"), keyDecision{routeAction, actionDeny}},
		{"N denies", press('N', "N"), keyDecision{routeAction, actionDeny}},
		{"esc cancels", tea.KeyPressMsg{Code: tea.KeyEsc}, keyDecision{routeAction, actionCancel}},
		{"enter is swallowed", tea.KeyPressMsg{Code: tea.KeyEnter}, keyDecision{kind: routeIgnored}},
		{"q is swallowed", press('q', "q"), keyDecision{kind: routeIgnored}},
		{"ctrl+space is swallowed", tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl}, keyDecision{kind: routeIgnored}},
		{"ctrl+] (the leader) is swallowed", tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}, keyDecision{kind: routeIgnored}},
		{"ctrl+c quits", chord('c'), keyDecision{routeAction, actionQuit}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reg.route(inputConfirmGitHubRepo, routeConds{}, tc.key); got != tc.want {
				t.Fatalf("route(%q) = {%v %v}, want {%v %v}", tc.key.String(), got.kind, got.action, tc.want.kind, tc.want.action)
			}
		})
	}
	if !(inputConfirmGitHubRepo).isModal() {
		t.Error("the GitHub question is not a modal")
	}
	if got := (modeState{modal: inputConfirmGitHubRepo}); activeInputMode(got) != inputConfirmGitHubRepo {
		t.Errorf("activeInputMode = %v, want the GitHub question", activeInputMode(got))
	}
}

// arriveAfterStart folds a started result for the new folder into m, the
// way the start's command delivers it, then dials the master-plan pane the
// result opened and delivers its arrival. It returns the model after the
// arrival, with the notice the arrival left.
func arriveAfterStart(t *testing.T, m Model, cmd tea.Cmd, rec *planSeams) Model {
	t.Helper()
	rec.reply = PlanStartResult{Outcome: PlanStarted, Root: folderPath}
	m, cmd = update(t, m, planStarted(t, cmd))
	if m.activeProjectPath() != folderPath {
		t.Fatalf("active project = %q, want the new folder %q", m.activeProjectPath(), folderPath)
	}
	attached, ok := paneDial(t, cmd)
	if !ok {
		t.Fatal("the start dialled no pane")
	}
	m, _ = update(t, m, attached)
	if m.pane == nil || m.paneName != session.PlanSessionName {
		t.Fatalf("pane = %q, want master-plan installed", m.paneName)
	}
	return m
}

// TestGitHubOutcomeOutlastsThePaneArrival follows each GitHub outcome past
// the planStartedMsg to the master-plan pane's arrival, which is where the
// outcome used to be lost: the arrival's "input goes to master-plan" replaced
// the notice the moment the pane connected (seen against a built binary).
// Load-bearing rows:
//   - "yes made the repository", "no", "logged out": the three outcomes the
//     README promises stay on screen; the arrival notice must still start
//     with the line, and the arrival's own words follow it.
//   - "gh missing": the longest notice, still first.
//   - "failure": a gh failure's reason is what the owner most needs to read.
//
// Every row also checks the lead is disarmed after the arrival, so a later
// open of master-plan by hand is not prefixed with an old outcome, and that
// the lead fits the toast whole wherever it can (three lines of 48 cells).
func TestGitHubOutcomeOutlastsThePaneArrival(t *testing.T) {
	missing := "No GitHub repository offered: gh (the GitHub command) is not installed. Install it from https://cli.github.com, run `gh auth login`, then `gh repo create --private` in the folder."
	loggedOut := "No GitHub repository offered: gh is not logged in. Run `gh auth login`, then `gh repo create --private` in the folder."
	failed := "Could not make the GitHub repository owner/demo: HTTP 422. The folder is left as it is."
	cases := []struct {
		name     string
		offer    GitHubOffer
		answer   tea.Msg // nil when no question is asked
		line     string  // what CreateGitHubRepo returns, on y
		lead     string
		fitToast bool
	}{
		{name: "yes made the repository", offer: GitHubOffer{Ask: true, Repo: githubRepoName}, answer: keyRune('y'),
			line: githubCreatedLine, lead: githubCreatedLine, fitToast: true},
		{name: "no", offer: GitHubOffer{Ask: true, Repo: githubRepoName}, answer: keyRune('n'),
			lead: githubRepoDeclined, fitToast: true},
		{name: "logged out", offer: GitHubOffer{Notice: loggedOut}, lead: loggedOut, fitToast: true},
		{name: "gh missing", offer: GitHubOffer{Notice: missing}, lead: missing},
		{name: "failure", offer: GitHubOffer{Ask: true, Repo: githubRepoName}, answer: keyRune('y'),
			line: failed, lead: failed, fitToast: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, fs, gs := githubModel(t)
			gs.offer = tc.offer
			gs.line = tc.line
			m, cmd := askGitHub(t, m, fs)
			if tc.answer != nil {
				m, cmd = update(t, m, tc.answer)
				if made, ok := cmd().(githubRepoCreatedMsg); ok {
					m, cmd = update(t, m, made)
				}
			}
			m = arriveAfterStart(t, m, cmd, rec)
			want := tc.lead + " Input goes to " + session.PlanSessionName
			if !strings.HasPrefix(m.status, want) {
				t.Errorf("notice after the pane arrived = %q, want it to start with %q", m.status, want)
			}
			if m.pendingPaneLead != "" {
				t.Errorf("pendingPaneLead = %q after the arrival, want it disarmed", m.pendingPaneLead)
			}
			if tc.fitToast {
				toast := xansi.Strip(renderToast(m.statusKind, m.status, 120))
				// Compared with the spaces out: the wrap may break a line
				// inside a word ("--private"), and the border is drawn
				// between the pieces.
				squash := func(s string) string {
					s = strings.NewReplacer("│", "", "╭", "", "╮", "", "╰", "", "╯", "", "─", "").Replace(s)
					return strings.Join(strings.Fields(s), "")
				}
				if !strings.Contains(squash(toast), squash(tc.lead)) {
					t.Errorf("the toast cuts the lead %q:\n%s", tc.lead, toast)
				}
			}
		})
	}
}

// TestPaneLeadIsSaidOnce pins when an armed lead is dropped rather than
// shown. Load-bearing rows:
//   - "project switch": the lead belongs to the project the start opened;
//     after a switch the incoming project's master-plan arrival must not
//     carry it.
//   - "another session arrives first": the owner moved on, so the lead goes,
//     and a later master-plan arrival is worded as any other.
//   - "the dial fails": the failure notice carries the lead, since it is the
//     last word about the pane the start opened.
//   - "no lead": nothing is armed, which is every plan start without the
//     GitHub step.
func TestPaneLeadIsSaidOnce(t *testing.T) {
	planArrival := func(m Model) paneAttachedMsg {
		return paneAttachedMsg{pane: newFakePane("PLANNER"), name: session.PlanSessionName, worktree: folderPath, focusOnArrival: true, gen: m.projectGen}
	}
	start := func(t *testing.T, lead string) (Model, tea.Cmd) {
		t.Helper()
		m, _ := planModel(t, sess("a"))
		m, cmd := update(t, m, planStartedMsg{mode: PlanModeNew, res: PlanStartResult{Outcome: PlanStarted, Root: folderPath}, gen: m.projectGen, lead: lead})
		return m, cmd
	}
	t.Run("project switch", func(t *testing.T) {
		m, _ := start(t, githubRepoDeclined)
		if m.pendingPaneLead != githubRepoDeclined {
			t.Fatalf("setup: pendingPaneLead = %q, want the lead armed", m.pendingPaneLead)
		}
		m, _ = m.switchProject("/projects/api")
		if m.pendingPaneLead != "" {
			t.Fatalf("pendingPaneLead = %q after a project switch, want it dropped", m.pendingPaneLead)
		}
		m.focus = focusRight
		m, _ = update(t, m, planArrival(m))
		if strings.Contains(m.status, githubRepoDeclined) {
			t.Errorf("notice = %q, an old project's lead turned up", m.status)
		}
	})
	t.Run("another session arrives first", func(t *testing.T) {
		m, _ := start(t, githubRepoDeclined)
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("A"), name: "a", worktree: "/wt/a", focusOnArrival: true, gen: m.projectGen})
		if m.pendingPaneLead != "" {
			t.Fatalf("pendingPaneLead = %q after another session arrived, want it dropped", m.pendingPaneLead)
		}
		if strings.Contains(m.status, githubRepoDeclined) {
			t.Errorf("notice = %q, the lead went before another session's arrival", m.status)
		}
		m.focus = focusRight
		m, _ = update(t, m, planArrival(m))
		if strings.Contains(m.status, githubRepoDeclined) {
			t.Errorf("notice = %q, a disarmed lead turned up", m.status)
		}
	})
	t.Run("the dial fails", func(t *testing.T) {
		m, _ := start(t, githubRepoDeclined)
		msg := planArrival(m)
		msg.pane = nil
		msg.err = errors.New("dial: refused")
		m, _ = update(t, m, msg)
		if !strings.HasPrefix(m.status, githubRepoDeclined+" ") {
			t.Errorf("notice = %q, want the failure to lead with the outcome", m.status)
		}
		if m.pendingPaneLead != "" {
			t.Errorf("pendingPaneLead = %q, want it disarmed", m.pendingPaneLead)
		}
	})
	t.Run("no lead", func(t *testing.T) {
		m, _ := start(t, "")
		if m.pendingPaneLead != "" {
			t.Fatalf("pendingPaneLead = %q with no lead, want nothing armed", m.pendingPaneLead)
		}
		m, _ = update(t, m, planArrival(m))
		if want := focusRequestHonoredStatus(defaultLeaderKey, session.PlanSessionName); m.status != want {
			t.Errorf("notice = %q, want the arrival worded as before (%q)", m.status, want)
		}
	})
}

// TestPlanFixForm covers what "fix a bug" (T13.6) adds over amend's form.
// Load-bearing rows: "opens the form" (the dialog must ask what is broken,
// under its own title, or the owner cannot tell it from amend), "starting
// notice" (fix has no planVerbs entry, so without its own wording the line
// would read "to  the plan"), and "project switch" (the description was typed
// for the project on screen, so a switch must start nothing, as amend's does).
func TestPlanFixForm(t *testing.T) {
	cases := []struct {
		name     string
		switched bool
	}{
		{"opens the form and starts with its own notice", false},
		{"project switch while open starts nothing", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := planModel(t, sess("a"))
			m, _ = runPlanAction(t, m, "a bug")
			if m.modal != inputPlanText || m.planTextMode != PlanModeFix {
				t.Fatalf("modal=%v mode=%q, want the fix form", m.modal, m.planTextMode)
			}
			screen := render(m)
			for _, want := range []string{"Fix a bug", planFixQuestion} {
				if !strings.Contains(screen, want) {
					t.Errorf("the form does not show %q", want)
				}
			}
			if m.planTextInput.Placeholder != "one sentence about what is broken" {
				t.Errorf("placeholder = %q", m.planTextInput.Placeholder)
			}
			m = typeText(t, m, "The export button does nothing.")
			if tc.switched {
				m.projectGen++
			}
			m, cmd := update(t, m, keyEnter)
			if tc.switched {
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
				return
			}
			if m.status != "Starting a planning session to fix a bug…" {
				t.Errorf("status = %q, want fix's own starting notice", m.status)
			}
			planStarted(t, cmd)
			if len(rec.calls) != 1 || rec.calls[0] != (planCall{"/projects/api", PlanModeFix, "The export button does nothing."}) {
				t.Fatalf("calls = %+v, want one fix call with the sentence", rec.calls)
			}
		})
	}
}
