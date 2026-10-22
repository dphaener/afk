package ui

import "testing"

// TestCurrentLevel is the level derivation's full matrix. The load-bearing
// rows are the ones where a fact the focus does not show wins: the
// all-projects screen open over a focused agent, and no active project with
// the focus left on the right, both All projects — otherwise the keyboard
// would go into an agent nobody can see. "agent placeholder" pins A6: the
// agent view focused with no live pane is still the Agent, because the level
// does not read the pane.
func TestCurrentLevel(t *testing.T) {
	tests := []struct {
		name  string
		facts levelFacts
		want  level
	}{
		{"no project, list focus", levelFacts{}, levelAllProjects},
		{"no project, agent focused (load-bearing)", levelFacts{focus: focusRight, view: viewAgent}, levelAllProjects},
		{"screen open, list focus", levelFacts{allProjectsOpen: true, hasActiveProject: true}, levelAllProjects},
		{"screen open over a focused agent (load-bearing)", levelFacts{allProjectsOpen: true, hasActiveProject: true, focus: focusRight, view: viewAgent}, levelAllProjects},
		{"project, list focus, agent view", levelFacts{hasActiveProject: true, focus: focusList, view: viewAgent}, levelProject},
		{"project, list focus, overview view", levelFacts{hasActiveProject: true, focus: focusList, view: viewOverview}, levelProject},
		{"project, overview focused", levelFacts{hasActiveProject: true, focus: focusRight, view: viewOverview}, levelProject},
		{"project, agent placeholder focused (load-bearing)", levelFacts{hasActiveProject: true, focus: focusRight, view: viewAgent}, levelAgent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := currentLevel(tc.facts); got != tc.want {
				t.Fatalf("currentLevel(%+v) = %v, want %v", tc.facts, got, tc.want)
			}
		})
	}
}

// Outcome builders, so a table row reads as what the key does.
var (
	doNothing   = keyOutcome{kind: outcomeNothing}
	doOverlay   = keyOutcome{kind: outcomeOverlay}
	doMove      = keyOutcome{kind: outcomeMove}
	doGoIn      = keyOutcome{kind: outcomeGoIn}
	doGoBack    = keyOutcome{kind: outcomeGoBack}
	doQuit      = keyOutcome{kind: outcomeQuit}
	doSend      = keyOutcome{kind: outcomeSendToAgent}
	doOpenStrip = keyOutcome{kind: outcomeOpenStrip}
	doClose     = keyOutcome{kind: outcomeCloseStrip}
	doPass      = keyOutcome{kind: outcomePassLeader}
)

func doRun(a modeAction) keyOutcome { return keyOutcome{kind: outcomeRun, action: a} }

// String exists for test-failure output: modeAction's own String does not
// know the key table's new actions, so a run names its row instead.
func (o keyOutcome) String() string {
	if o.kind != outcomeRun {
		return o.kind.String()
	}
	for _, r := range keyTable {
		if r.action == o.action {
			return "run " + r.name
		}
	}
	return "run " + o.action.String()
}

// TestDecideKey holds decideKey to §5. The first table is the load-bearing
// rows, each a rule a person relies on without looking:
//
//   - a letter and esc in the Agent go to the agent (the session is a
//     terminal, and esc is a key many agents use);
//   - the leader then esc goes back to Project;
//   - the leader then a key the strip does not list closes the strip and
//     sends nothing, so a mistyped key never reaches the agent;
//   - the leader twice sends one leader to the agent;
//   - ctrl+c quits at All projects and Project and goes to the agent in the
//     Agent;
//   - an open overlay takes every key first, esc, q and ctrl+c included.
//
// The second table is every key §5 lists at every level, with the strip
// closed and open in the Agent, so a key moved between levels in the table
// fails here by name.
func TestDecideKey(t *testing.T) {
	const leader = defaultLeaderKey
	type call struct {
		lv    level
		ov    keyOverlay
		armed bool
		key   string
	}
	loadBearing := []struct {
		name string
		in   call
		want keyOutcome
	}{
		{"agent letter goes to the agent", call{levelAgent, overlayNone, false, "q"}, doSend},
		{"agent esc goes to the agent", call{levelAgent, overlayNone, false, "esc"}, doSend},
		{"agent ctrl+c goes to the agent", call{levelAgent, overlayNone, false, "ctrl+c"}, doSend},
		{"agent leader opens the strip", call{levelAgent, overlayNone, false, leader}, doOpenStrip},
		{"leader then esc goes back to project", call{levelAgent, overlayNone, true, "esc"}, doGoBack},
		{"leader then unlisted key closes the strip", call{levelAgent, overlayNone, true, "x"}, doClose},
		{"leader then a project-only key closes the strip", call{levelAgent, overlayNone, true, "c"}, doClose},
		{"leader twice passes one through", call{levelAgent, overlayNone, true, leader}, doPass},
		{"leader then q quits", call{levelAgent, overlayNone, true, "q"}, doQuit},
		{"leader then m toggles the mouse", call{levelAgent, overlayNone, true, "m"}, doRun(actionMouseToggle)},
		{"all projects ctrl+c quits", call{levelAllProjects, overlayNone, false, "ctrl+c"}, doQuit},
		{"project ctrl+c quits", call{levelProject, overlayNone, false, "ctrl+c"}, doQuit},
		{"project letter runs alone", call{levelProject, overlayNone, false, "n"}, doRun(actionNewSession)},
		{"all projects letter runs alone", call{levelAllProjects, overlayNone, false, "n"}, doRun(actionPlanNew)},
		{"project leader does nothing", call{levelProject, overlayNone, false, leader}, doNothing},
		{"all projects leader does nothing", call{levelAllProjects, overlayNone, false, leader}, doNothing},
		{"armed is ignored off the agent", call{levelProject, overlayNone, true, "x"}, doNothing},
		{"overlay takes esc at project", call{levelProject, overlayModal, false, "esc"}, doOverlay},
		{"overlay takes q at all projects", call{levelAllProjects, overlayKeys, false, "q"}, doOverlay},
		{"overlay takes ctrl+c at project", call{levelProject, overlayActions, false, "ctrl+c"}, doOverlay},
		{"overlay takes letters in the agent", call{levelAgent, overlayActions, false, "a"}, doOverlay},
		{"overlay takes the leader in the agent", call{levelAgent, overlayJump, true, leader}, doOverlay},
	}
	for _, tc := range loadBearing {
		t.Run(tc.name, func(t *testing.T) {
			got := decideKey(tc.in.lv, tc.in.ov, tc.in.armed, leader, tc.in.key)
			if got != tc.want {
				t.Fatalf("decideKey(%v, %v, armed=%v, %q) = %v, want %v", tc.in.lv, tc.in.ov, tc.in.armed, tc.in.key, got, tc.want)
			}
		})
	}

	// Every key of §5. agentOpen is the Agent with the strip open (the
	// leader then the key); with the strip closed every key but the leader
	// goes to the agent, which the loop checks for every row.
	matrix := []struct {
		key                         string
		allProjects, project, agent keyOutcome
	}{
		{"j", doMove, doMove, doClose},
		{"k", doMove, doMove, doClose},
		{"up", doMove, doMove, doClose},
		{"down", doMove, doMove, doClose},
		{"pgup", doMove, doMove, doClose},
		{"pgdown", doMove, doMove, doClose},
		{"home", doMove, doMove, doClose},
		{"end", doMove, doMove, doClose},
		{"enter", doGoIn, doGoIn, doClose},
		{"esc", doGoBack, doGoBack, doGoBack},
		// ] [ are not bound yet (A4): T16.6 builds them. a is bound at All
		// projects only (T16.3); Project's a is T16.7's.
		{"]", doNothing, doNothing, doClose},
		{"[", doNothing, doNothing, doClose},
		{"a", doRun(actionAnswerOldest), doNothing, doClose},
		{"/", doRun(actionProjects), doRun(actionProjects), doRun(actionProjects)},
		{":", doRun(actionActionsSearch), doRun(actionActionsSearch), doRun(actionActionsSearch)},
		{"i", doRun(actionInbox), doRun(actionInbox), doRun(actionInbox)},
		{"d", doNothing, doRun(actionDetails), doRun(actionDetails)},
		{"R", doRun(actionRefreshUsage), doRun(actionRefreshUsage), doRun(actionRefreshUsage)},
		{"?", doRun(actionKeys), doRun(actionKeys), doRun(actionKeys)},
		{"q", doQuit, doQuit, doQuit},
		{"p", doRun(actionPauseToggle), doRun(actionPauseToggle), doClose},
		{"n", doRun(actionPlanNew), doRun(actionNewSession), doClose},
		{"+", doRun(actionProjectAdd), doNothing, doClose},
		{"-", doRun(actionProjectRemove), doNothing, doClose},
		// tab, shift+tab and 1–3 change the project view's section (T16.4);
		// 4 and 5 are the Details tabs' (T16.5's surface) and no key here.
		{"tab", doNothing, doRun(actionSectionNext), doClose},
		{"shift+tab", doNothing, doRun(actionSectionPrev), doClose},
		{"1", doNothing, doRun(actionSectionSessions), doClose},
		{"2", doNothing, doRun(actionSectionBuild), doClose},
		{"3", doNothing, doRun(actionSectionInbox), doClose},
		{"4", doNothing, doNothing, doClose},
		{"5", doNothing, doNothing, doClose},
		{"o", doNothing, doRun(actionScratch), doClose},
		{"c", doNothing, doRun(actionClean), doClose},
		{"r", doNothing, doRun(actionRestart), doClose},
		{"l", doNothing, doRun(actionEditLabel), doClose},
		{"s", doNothing, doRun(actionTaskStatus), doClose},
		{"t", doNothing, doRun(actionTaskTokens), doClose},
		{"m", doNothing, doNothing, doRun(actionMouseToggle)},
		// y and n answer a confirm, which is an overlay; with none open, y
		// is no key.
		{"y", doNothing, doNothing, doClose},
		{"ctrl+c", doQuit, doQuit, doQuit},
		// Keys the leader retired (§5 "What it replaces") are no keys now,
		// and say nothing: no "moved" notice comes from here.
		{"P", doNothing, doNothing, doClose},
		{"U", doNothing, doNothing, doClose},
		{"A", doNothing, doNothing, doClose},
		{"x", doNothing, doNothing, doClose},
		{"\\", doNothing, doNothing, doClose},
		{"f", doNothing, doNothing, doClose},
		{"g", doNothing, doNothing, doClose},
		{"w", doNothing, doNothing, doClose},
		{"b", doNothing, doNothing, doClose},
		{leader, doNothing, doNothing, doPass},
	}
	for _, row := range matrix {
		cases := []struct {
			where string
			in    call
			want  keyOutcome
		}{
			{"all-projects", call{levelAllProjects, overlayNone, false, row.key}, row.allProjects},
			{"project", call{levelProject, overlayNone, false, row.key}, row.project},
			{"agent-strip-open", call{levelAgent, overlayNone, true, row.key}, row.agent},
			{"agent-strip-closed", call{levelAgent, overlayNone, false, row.key}, doSend},
			{"overlay-open", call{levelProject, overlayInbox, false, row.key}, doOverlay},
		}
		if row.key == leader {
			cases[3].want = doOpenStrip
		}
		for _, tc := range cases {
			t.Run(tc.where+" key "+row.key, func(t *testing.T) {
				got := decideKey(tc.in.lv, tc.in.ov, tc.in.armed, leader, tc.in.key)
				if got != tc.want {
					t.Fatalf("decideKey(%v, armed=%v, %q) = %v, want %v", tc.in.lv, tc.in.armed, tc.in.key, got, tc.want)
				}
			})
		}
	}
}

// TestDecideKeyConfiguredLeader pins that the leader is the configured key,
// not ctrl+]: with ctrl+a set, ctrl+] in the Agent is an ordinary key for the
// agent and ctrl+a opens the strip. Load-bearing because config.toml sets
// the leader (T9.2) and a hard-coded ctrl+] would trap anyone who moved it.
func TestDecideKeyConfiguredLeader(t *testing.T) {
	tests := []struct {
		name  string
		armed bool
		key   string
		want  keyOutcome
	}{
		{"default leader goes to the agent", false, defaultLeaderKey, doSend},
		{"configured leader opens the strip", false, "ctrl+a", doOpenStrip},
		{"configured leader twice passes through", true, "ctrl+a", doPass},
		{"default leader after the configured one closes the strip", true, defaultLeaderKey, doClose},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideKey(levelAgent, overlayNone, tc.armed, "ctrl+a", tc.key); got != tc.want {
				t.Fatalf("decideKey(agent, armed=%v, %q) = %v, want %v", tc.armed, tc.key, got, tc.want)
			}
		})
	}
}
