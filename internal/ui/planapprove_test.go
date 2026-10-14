package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// planApproveStatus is a new plan at review with the given number of errors:
// the one state the approve row runs for.
func planApproveStatus(errors int) PlanStatus {
	return PlanStatus{
		Session: true, Mode: "new", Phase: "review",
		PlanExists: true, PlanPath: "docs/PLAN.md",
		Summary: PlanSummary{Milestones: 2, Tasks: 5, OwnerTasks: 1, OpenQuestions: 0, Errors: errors, Warnings: 1},
	}
}

// approveSeam is a counting fake of Deps.ApprovePlan.
type approveSeam struct {
	calls  []string
	result PlanApproveResult
}

func (a *approveSeam) approve(root string) PlanApproveResult {
	a.calls = append(a.calls, root)
	return a.result
}

// approveModel is the list with status s read and a counting seam wired.
func approveModel(t *testing.T, s PlanStatus) (Model, *approveSeam) {
	t.Helper()
	seam := &approveSeam{result: PlanApproveResult{Outcome: PlanApproveApproved, Text: "Plan approved. The build starts at the daemon's next pass."}}
	m := withPlanStatus(t, testModel(sess("a")), s)
	m.deps.ApprovePlan = seam.approve
	return m, seam
}

// runApproveRow opens the leader, types "approve" and presses enter, the way
// the owner reaches the row.
func runApproveRow(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, _ = update(t, m, leaderKeyPress)
	for _, r := range "approve" {
		m, _ = update(t, m, keyRune(r))
	}
	if !m.leaderOpen {
		t.Fatal("setup: typing approve closed the dialog")
	}
	return update(t, m, keyEnter)
}

// openApprove raises the confirm for s, failing if it does not come up.
func openApprove(t *testing.T, s PlanStatus) (Model, *approveSeam) {
	t.Helper()
	m, seam := approveModel(t, s)
	m, cmd := runApproveRow(t, m)
	if cmd != nil {
		t.Fatal("opening the confirm returned a command; nothing may run before y")
	}
	if m.modal != inputConfirmPlanApprove {
		t.Fatalf("modal = %v, want the approve confirm (status %q)", m.modal, m.status)
	}
	return m, seam
}

// TestPlanApproveBlockedReason is the row's whole dimming matrix, one row per
// reason. Every row is load-bearing, and the order of the conditions is what
// several of them pin:
//   - "not read yet" with a zero status must not say "no planning session":
//     the session may exist, the dashboard just has not looked.
//   - "damaged" and "unreadable" with a new/review phase must still be
//     dimmed: the phase of a file that cannot be trusted means nothing.
//   - "approved" in a new session must say approved, not "not review".
//   - "extend at review" must be dimmed: review is where a change session
//     ends too, and only a new plan is approved.
//   - "new at review" is the one runnable row.
func TestPlanApproveBlockedReason(t *testing.T) {
	ready := leaderConds{
		hasActiveProject: true,
		canApprovePlan:   true,
		planKnown:        true,
		planSession:      true,
		planMode:         "new",
		planPhase:        "review",
	}
	with := func(f func(*leaderConds)) leaderConds {
		c := ready
		f(&c)
		return c
	}
	cases := []struct {
		name  string
		conds leaderConds
		want  string
	}{
		{"no seam", with(func(c *leaderConds) { c.canApprovePlan = false }), "approving is not available here"},
		{"no seam beats everything", leaderConds{}, "approving is not available here"},
		{"no project", with(func(c *leaderConds) { c.hasActiveProject = false }), "no project is open"},
		{"not read yet", with(func(c *leaderConds) { c.planKnown = false; c.planSession = false; c.planMode = ""; c.planPhase = "" }), "the plan status has not been read yet"},
		{"unreadable", with(func(c *leaderConds) { c.planErr = true }), "the plan status cannot be read"},
		{"no session", with(func(c *leaderConds) { c.planSession = false; c.planMode = ""; c.planPhase = "" }), "no planning session"},
		{"damaged", with(func(c *leaderConds) { c.planDamaged = true }), "the planning state file is damaged"},
		{"approved", with(func(c *leaderConds) { c.planPhase = "approved" }), "the plan is already approved"},
		{"extend at review", with(func(c *leaderConds) { c.planMode = "extend" }), "only a new plan is approved; this session is extend"},
		{"new at rounds", with(func(c *leaderConds) { c.planPhase = "rounds" }), "the plan is at rounds, not review"},
		{"new with no phase", with(func(c *leaderConds) { c.planPhase = "" }), "the plan is not at review yet"},
		{"new at review runs", ready, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := leaderBlockedReason(actionPlanApprove, tc.conds); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPlanApproveRowFromTheModel checks the Model fills the conditions from
// its own state, so the dialog dims the row the table above says. Load-
// bearing: "no seam" (a dashboard without ApprovePlan must never offer it),
// "not read yet" (the zero status before the first read), "extend at review"
// (read from planStatus, not assumed) and "new at review" runnable.
func TestPlanApproveRowFromTheModel(t *testing.T) {
	extend := planApproveStatus(0)
	extend.Mode = "extend"
	cases := []struct {
		name  string
		setup func(t *testing.T) Model
		want  string
	}{
		{"no seam", func(t *testing.T) Model {
			return withPlanStatus(t, testModel(sess("a")), planApproveStatus(0))
		}, "approving is not available here"},
		{"not read yet", func(t *testing.T) Model {
			m := testModel(sess("a"))
			m.deps.ApprovePlan = (&approveSeam{}).approve
			return m
		}, "the plan status has not been read yet"},
		{"extend at review", func(t *testing.T) Model { m, _ := approveModel(t, extend); return m }, "only a new plan is approved; this session is extend"},
		{"new at review", func(t *testing.T) Model { m, _ := approveModel(t, planApproveStatus(0)); return m }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := update(t, tc.setup(t), leaderKeyPress)
			var row leaderRow
			found := false
			for _, r := range m.leader.rows {
				if r.action == actionPlanApprove {
					row, found = r, true
				}
			}
			if !found {
				t.Fatal("the dialog has no approve-the-plan row")
			}
			if row.name != "approve the plan" || row.keyLabel != "" {
				t.Fatalf("row = %q key %q, want \"approve the plan\" with no key", row.name, row.keyLabel)
			}
			if row.blocked != tc.want {
				t.Fatalf("blocked = %q, want %q", row.blocked, tc.want)
			}
		})
	}
}

// TestPlanApproveDimmedRowRunsNothing runs the dimmed row by name. Load-
// bearing: enter on a dimmed row must neither raise the confirm nor call the
// seam, and the action reached another way says the same reason.
func TestPlanApproveDimmedRowRunsNothing(t *testing.T) {
	s := planApproveStatus(0)
	s.Phase = "approved"
	m, seam := approveModel(t, s)
	m, cmd := runApproveRow(t, m)
	if m.modal != inputNormal || cmd != nil || len(seam.calls) != 0 {
		t.Fatalf("dimmed row ran: modal %v, cmd %v, calls %v", m.modal, cmd != nil, seam.calls)
	}

	m2, _ := approveModel(t, s)
	got, cmd := m2.openPlanApprove()
	gm := got.(Model)
	if gm.modal != inputNormal || cmd != nil {
		t.Fatalf("openPlanApprove raised modal %v (cmd %v) for an approved plan", gm.modal, cmd != nil)
	}
	if want := "The plan cannot be approved: the plan is already approved."; gm.status != want {
		t.Fatalf("notice = %q, want %q", gm.status, want)
	}
}

// TestLeaderPlanApproveWordIsUnique pins the word the row is found by.
// Load-bearing: "approve" is the query the owner types, so a second row
// saying it would leave two rows and enter would run whichever is first; and
// its first letter must be no row's key, or typing it would run that row
// instead of filtering.
func TestLeaderPlanApproveWordIsUnique(t *testing.T) {
	if !strings.Contains(strings.ToLower(leaderNames[actionPlanApprove]+" "+leaderHelp[actionPlanApprove]), "approve") {
		t.Fatal("the approve row does not say approve")
	}
	if a := leaderActionForKey("a"); a != actionNone {
		t.Fatalf("a is %v's key; typing approve would run it", a)
	}
	for _, e := range leaderTable {
		if e.action == actionPlanApprove {
			continue
		}
		hay := strings.ToLower(leaderNames[e.action] + " " + leaderHelp[e.action] + " " + e.key)
		if strings.Contains(hay, "approve") {
			t.Errorf("%v also says approve: %q", e.action, hay)
		}
	}
}

// TestPlanApproveConfirmShowsTheCounts opens the confirm on a clean plan.
// Load-bearing: the project's name and folder, the plan path and every count
// are what the owner says y to; "y: approve" must be offered; and nothing is
// called by opening.
func TestPlanApproveConfirmShowsTheCounts(t *testing.T) {
	m, seam := openApprove(t, planApproveStatus(0))
	frame := render(m)
	for _, want := range []string{
		"Approve the plan?",
		"Approve the plan for repo and start its build?",
		"/repo",
		"Plan: docs/PLAN.md",
		"2 milestones · 5 tasks · 1 owner task · 0 open questions",
		"0 errors · 1 warning",
		"commits and pushes the plan",
		"y: approve",
		"n/esc: cancel",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("the confirm does not show %q:\n%s", want, frame)
		}
	}
	if len(seam.calls) != 0 {
		t.Fatalf("ApprovePlan called %v by opening the confirm", seam.calls)
	}
}

// TestPlanApproveCounts pins the count lines' wording. Load-bearing: the
// singular rows, since "1 errors" is what a missed planCountNoun would show.
func TestPlanApproveCounts(t *testing.T) {
	cases := []struct {
		name string
		in   PlanSummary
		want []string
	}{
		{"plural", PlanSummary{2, 5, 2, 3, 2, 4}, []string{"2 milestones · 5 tasks · 2 owner tasks · 3 open questions", "2 errors · 4 warnings"}},
		{"singular", PlanSummary{1, 1, 1, 1, 1, 1}, []string{"1 milestone · 1 task · 1 owner task · 1 open question", "1 error · 1 warning"}},
		{"zero", PlanSummary{}, []string{"0 milestones · 0 tasks · 0 owner tasks · 0 open questions", "0 errors · 0 warnings"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planApproveCounts(tc.in)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("counts = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPlanApproveWithErrorsHasNoYes opens the confirm on plans with errors.
// Every row is load-bearing: the confirm must say why there is no y and how
// to see the errors, offer no y in its hint or the footer, and y, Y and
// enter must leave it up and call nothing — the counting fake stays at 0.
// n and esc still close it, also calling nothing.
func TestPlanApproveWithErrorsHasNoYes(t *testing.T) {
	cases := []struct {
		name     string
		errors   int
		key      tea.Msg
		wantOpen bool
		wantText string
	}{
		{"two errors y does nothing", 2, keyRune('y'), true, "The plan has 2 errors. Fix them before approving (ctrl+space, type errors, to see them)."},
		{"two errors Y does nothing", 2, keyRune('Y'), true, "The plan has 2 errors. Fix them before approving (ctrl+space, type errors, to see them)."},
		{"two errors enter does nothing", 2, keyEnter, true, "The plan has 2 errors. Fix them before approving (ctrl+space, type errors, to see them)."},
		{"one error y does nothing", 1, keyRune('y'), true, "The plan has 1 error. Fix it before approving (ctrl+space, type errors, to see it)."},
		{"errors n closes", 2, keyRune('n'), false, ""},
		{"errors esc closes", 2, keyEscMsg(), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, seam := openApprove(t, planApproveStatus(tc.errors))
			if tc.wantText != "" {
				if got := planApproveErrorsLine(tc.errors); got != tc.wantText {
					t.Fatalf("errors line = %q, want %q", got, tc.wantText)
				}
			}
			frame := render(m)
			for _, want := range []string{"cannot be approved yet", "Fix", "before approving", "to see", "n/esc: cancel"} {
				if !strings.Contains(frame, want) {
					t.Errorf("the confirm does not show %q:\n%s", want, frame)
				}
			}
			if strings.Contains(frame, "y: approve") {
				t.Errorf("the confirm offers y with errors:\n%s", frame)
			}
			if help := m.renderHelp(); strings.Contains(help, "approve") {
				t.Errorf("the footer offers y with errors: %q", help)
			}
			reg := m.modes()
			if got := reg.route(inputConfirmPlanApprove, routeConds{}, tea.KeyPressMsg{Code: 'y', Text: "y"}); got.kind != routeIgnored {
				t.Errorf("y routes to %v %v with errors, want ignored", got.kind, got.action)
			}

			m, cmd := update(t, m, tc.key)
			if cmd != nil {
				t.Fatalf("%s returned a command", tc.name)
			}
			if open := m.modal == inputConfirmPlanApprove; open != tc.wantOpen {
				t.Fatalf("confirm open = %v, want %v", open, tc.wantOpen)
			}
			if len(seam.calls) != 0 || m.planApproving {
				t.Fatalf("ApprovePlan calls %v, in flight %v; want none", seam.calls, m.planApproving)
			}
		})
	}
}

// TestPlanApproveWithErrorsConfirmIsInert calls the y handler directly, the
// route the keyboard no longer has. Load-bearing: the handler's own check is
// what stops an approve the command would refuse if anything ever reached it.
func TestPlanApproveWithErrorsConfirmIsInert(t *testing.T) {
	m, seam := openApprove(t, planApproveStatus(3))
	got, cmd := m.confirmPlanApprove()
	gm := got.(Model)
	if cmd != nil || len(seam.calls) != 0 || gm.modal != inputConfirmPlanApprove {
		t.Fatalf("confirm with errors: cmd %v, calls %v, modal %v", cmd != nil, seam.calls, gm.modal)
	}
}

// TestPlanApproveAnswers is the clean plan's y/n. Load-bearing rows:
//   - "y" and "Y": the seam is called exactly once, inside the returned
//     command and not in Update (0 calls after the key, 1 after the command
//     runs), with the folder the dialog named; the working notice is up and
//     an approve is in flight until the result is applied.
//   - "n", "N" and "esc": closed, no command, no call.
//   - "enter" and "a stray key": swallowed with the confirm still up, so the
//     default is no.
func TestPlanApproveAnswers(t *testing.T) {
	cases := []struct {
		name     string
		key      tea.Msg
		wantRun  bool
		wantOpen bool
	}{
		{"y approves", keyRune('y'), true, false},
		{"Y approves", keyRune('Y'), true, false},
		{"n cancels", keyRune('n'), false, false},
		{"N cancels", keyRune('N'), false, false},
		{"esc cancels", keyEscMsg(), false, false},
		{"enter keeps the confirm", keyEnter, false, true},
		{"a stray key keeps the confirm", keyRune('q'), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, seam := openApprove(t, planApproveStatus(0))
			m, cmd := update(t, m, tc.key)
			if len(seam.calls) != 0 {
				t.Fatalf("ApprovePlan called %v inside Update", seam.calls)
			}
			if open := m.modal == inputConfirmPlanApprove; open != tc.wantOpen {
				t.Fatalf("confirm open = %v, want %v", open, tc.wantOpen)
			}
			if !tc.wantRun {
				if cmd != nil || m.planApproving {
					t.Fatalf("%s: cmd %v, in flight %v; want neither", tc.name, cmd != nil, m.planApproving)
				}
				return
			}
			if cmd == nil {
				t.Fatal("y returned no command")
			}
			if !m.planApproving {
				t.Fatal("y did not mark an approve in flight")
			}
			if m.status != planApprovingNotice {
				t.Fatalf("notice = %q, want %q", m.status, planApprovingNotice)
			}
			msg, ok := cmd().(planApprovedMsg)
			if !ok {
				t.Fatalf("the command returned %T, want planApprovedMsg", msg)
			}
			if len(seam.calls) != 1 || seam.calls[0] != "/repo" {
				t.Fatalf("ApprovePlan calls = %v, want exactly [/repo]", seam.calls)
			}
			m, next := update(t, m, msg)
			if m.planApproving {
				t.Fatal("the result did not clear the in-flight flag")
			}
			if m.status != seam.result.Text || m.statusKind != notifySuccess {
				t.Fatalf("notice = %v %q, want success %q", m.statusKind, m.status, seam.result.Text)
			}
			if next == nil {
				t.Fatal("the result did not read the plan status again")
			}
			if _, ok := next().(planStatusLoadedMsg); !ok {
				t.Fatal("the result's command is not a plan status read")
			}
			if len(seam.calls) != 1 {
				t.Fatalf("ApprovePlan called %d times in all, want 1", len(seam.calls))
			}
		})
	}
}

// TestPlanApproveInFlight opens the row again while an approve is out.
// Load-bearing: a second commit and push of the same plan must not start;
// the owner is told one is running, and the seam is not called (the first
// approve's command has not even run here).
func TestPlanApproveInFlight(t *testing.T) {
	m, seam := openApprove(t, planApproveStatus(0))
	m, first := update(t, m, keyRune('y'))
	if first == nil || !m.planApproving {
		t.Fatal("setup: y did not start an approve")
	}
	m, cmd := runApproveRow(t, m)
	if cmd != nil || m.modal != inputNormal {
		t.Fatalf("second open: cmd %v, modal %v; want neither", cmd != nil, m.modal)
	}
	if m.status != planApproveRunningNotice {
		t.Fatalf("notice = %q, want %q", m.status, planApproveRunningNotice)
	}
	if len(seam.calls) != 0 {
		t.Fatalf("ApprovePlan called %v", seam.calls)
	}
}

// TestPlanApproveResultNotice is how loudly each outcome is said. Load-
// bearing: a failure is an error (some steps are done and the owner must act),
// a refusal a warning, and an empty text is replaced so the owner is never
// left without word.
func TestPlanApproveResultNotice(t *testing.T) {
	cases := []struct {
		name     string
		in       PlanApproveResult
		wantKind notificationKind
		wantText string
	}{
		{"approved", PlanApproveResult{PlanApproveApproved, "done"}, notifySuccess, "done"},
		{"already", PlanApproveResult{PlanApproveAlready, "already"}, notifyInfo, "already"},
		{"refused", PlanApproveResult{PlanApproveRefused, "no"}, notifyWarn, "no"},
		{"failed", PlanApproveResult{PlanApproveFailed, "push failed"}, notifyError, "push failed"},
		{"unknown outcome", PlanApproveResult{"odd", "text"}, notifyInfo, "text"},
		{"empty text", PlanApproveResult{PlanApproveApproved, "  "}, notifyWarn, planApproveNoAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, text := planApproveNotice(tc.in)
			if kind != tc.wantKind || text != tc.wantText {
				t.Fatalf("notice = %v %q, want %v %q", kind, text, tc.wantKind, tc.wantText)
			}
		})
	}
}

// TestPlanApproveAcrossAProjectSwitch covers the two ways a switch crosses an
// approve. Load-bearing: a result that lands after the switch is dropped
// without a word (it is the other project's) yet still clears the in-flight
// flag, and a y given after the switch under a still-open confirm approves
// nothing, because the dialog named the old project. The switch is the
// generation bump switchProject makes, applied directly.
func TestPlanApproveAcrossAProjectSwitch(t *testing.T) {
	t.Run("a stale result is dropped", func(t *testing.T) {
		m, _ := openApprove(t, planApproveStatus(0))
		m, cmd := update(t, m, keyRune('y'))
		msg := cmd().(planApprovedMsg)
		m.projectGen++
		m.clearStatus()
		m, next := update(t, m, msg)
		if next != nil {
			t.Fatal("a stale result issued a command")
		}
		if m.status != "" {
			t.Fatalf("a stale result was shown: %q", m.status)
		}
		if m.planApproving {
			t.Fatal("a stale result left the approve in flight")
		}
	})
	t.Run("y after a switch approves nothing", func(t *testing.T) {
		m, seam := openApprove(t, planApproveStatus(0))
		m.projectGen++
		m, cmd := update(t, m, keyRune('y'))
		if cmd != nil || len(seam.calls) != 0 || m.planApproving {
			t.Fatalf("y after a switch: cmd %v, calls %v, in flight %v", cmd != nil, seam.calls, m.planApproving)
		}
		if m.status != planApproveProjectChanged {
			t.Fatalf("notice = %q, want %q", m.status, planApproveProjectChanged)
		}
	})
}

// TestPlanApproveSeamGoneAtY covers a seam that is nil when y is pressed (the
// row was run with it wired). Load-bearing: a nil seam must be a sentence,
// never a nil-function call.
func TestPlanApproveSeamGoneAtY(t *testing.T) {
	m, _ := openApprove(t, planApproveStatus(0))
	m.deps.ApprovePlan = nil
	m, cmd := update(t, m, keyRune('y'))
	if cmd != nil || m.planApproving {
		t.Fatalf("y with no seam: cmd %v, in flight %v", cmd != nil, m.planApproving)
	}
	if m.status != planApproveUnavailableNotice {
		t.Fatalf("notice = %q, want %q", m.status, planApproveUnavailableNotice)
	}
}

// TestPlanApproveConfirmKeys pins the confirm's routing at the table level,
// as TestGitHubRepoConfirmKeys does for the GitHub question. Load-bearing:
// y/Y confirm and n/N/esc cancel in the full registry, every other key,
// enter included, is swallowed, and the confirm is a modal that outranks
// everything.
func TestPlanApproveConfirmKeys(t *testing.T) {
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
		{"ctrl+c quits", chord('c'), keyDecision{routeAction, actionQuit}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reg.route(inputConfirmPlanApprove, routeConds{}, tc.key); got != tc.want {
				t.Fatalf("route(%q) = {%v %v}, want {%v %v}", tc.key.String(), got.kind, got.action, tc.want.kind, tc.want.action)
			}
		})
	}
	if !(inputConfirmPlanApprove).isModal() {
		t.Error("the approve confirm is not a modal")
	}
	if got := activeInputMode(modeState{modal: inputConfirmPlanApprove, leaderOpen: true, focus: focusRight, pane: true}); got != inputConfirmPlanApprove {
		t.Errorf("activeInputMode = %v, want the approve confirm", got)
	}
}
