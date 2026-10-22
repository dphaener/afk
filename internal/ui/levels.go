// levels.go holds the dashboard's three levels, All projects → Project →
// Agent (docs/dash-design.md §5, T16.2), and the pure decision of what a key
// does at each of them.
//
// The level is DERIVED, never stored. mode.go records why the input mode is
// derived rather than held in a field: the facts it is built from (focus,
// view, the all-projects screen, the active project) are changed from many
// paths that are not keys — an attach landing moves the focus, a project
// switch closes the all-projects screen — and every one of those sites would
// have to keep a stored level or level stack in step. One missed update is a
// keyboard trap, where the screen shows one level and the keys act on
// another. currentLevel re-reads the facts on every key instead, so the two
// cannot disagree.
//
// Nothing here touches the Model. currentLevel and decideKey are pure
// functions of plain values, so every branch is a table row (levels_test.go);
// the Model fills levelFacts and applies the keyOutcome it gets back.
package ui

// level is where the dashboard is: one of the three screens §5 stacks, with
// enter going in one and esc coming out one.
type level int

const (
	// levelAllProjects is the home screen: every project on one page.
	levelAllProjects level = iota
	// levelProject is one project: the project view's three sections,
	// Sessions, Build and Inbox (T16.4), or the session list beside the
	// overview while `d` has given the overview the keyboard.
	levelProject
	// levelAgent is one session's agent, drawn as a pane over the dimmed
	// Project. Every key but the leader goes to the agent here.
	levelAgent
)

// String exists for test-failure output; nothing renders it.
func (l level) String() string {
	switch l {
	case levelAllProjects:
		return "all-projects"
	case levelProject:
		return "project"
	case levelAgent:
		return "agent"
	}
	return "unknown"
}

// levelFacts is the snapshot of Model fields the level is derived from. They
// are copied raw, as modeState copies them, so currentLevel is the one place
// that says which level a set of facts is.
type levelFacts struct {
	// allProjectsOpen mirrors Model.allProjectsOpen.
	allProjectsOpen bool
	// hasActiveProject mirrors Model.hasActiveProject. Without a project
	// there is no Project level to be on, so the dashboard is at All
	// projects whatever the other facts say.
	hasActiveProject bool
	// focus and view mirror Model.focus and Model.view.
	focus paneFocus
	view  rightView
}

// currentLevel derives the level from the facts.
//
// The all-projects screen and a missing project are checked first because
// they win over the focus: the screen is drawn over everything else, and a
// focus left on the right from before a project closed must not put the
// keyboard into an agent nobody can see.
//
// The Agent is the right side focused with the agent view, whether or not a
// live pane is installed. A session with no live agent shows the agent
// placeholder there, and that placeholder is still the Agent level (A6): its
// esc goes back to Project like the pane's. modeState's pane fact is
// deliberately not read here — it decides whether keys reach a PTY, which is
// the wiring's business, not which level is on screen.
func currentLevel(f levelFacts) level {
	switch {
	case f.allProjectsOpen || !f.hasActiveProject:
		return levelAllProjects
	case f.focus == focusRight && f.view == viewAgent:
		return levelAgent
	}
	return levelProject
}

// keyOverlay is what, if anything, is open over the level. decideKey only
// needs to know whether one is open: an open overlay gets every key first,
// and what it does with the key is the overlay's own handling. The values
// beyond overlayNone name the overlays so the wiring can say which one it
// snapshot, and so a test row reads as the case it is.
type keyOverlay int

const (
	// overlayNone: nothing is open; the level has the keyboard.
	overlayNone keyOverlay = iota
	// overlayModal is a footer modal: a confirm, a picker or a prompt.
	overlayModal
	// overlayForm is the new-session or new-plan form.
	overlayForm
	// overlayJump is `/` (today's project switcher, A3).
	overlayJump
	// overlayActions is `:`, the actions-by-name search (§4.7).
	overlayActions
	// overlayKeys is `?`, the keys overlay (§4.9).
	overlayKeys
	// overlayInbox is `i`.
	overlayInbox
	// overlayPlanCheck is the plan checker's result.
	overlayPlanCheck
	// overlayTaskTokens is a Build task's tokens.
	overlayTaskTokens
)

// keyOutcomeKind is what decideKey says a key does.
type keyOutcomeKind int

const (
	// outcomeNothing: the key does nothing here, and nothing is sent.
	outcomeNothing keyOutcomeKind = iota
	// outcomeOverlay: an open overlay takes the key and handles it itself.
	outcomeOverlay
	// outcomeRun: run keyOutcome.action.
	outcomeRun
	// outcomeMove: a move key (j k ↑ ↓ pgup pgdn home end) moves the
	// selection; which way is the wiring's, from the key itself.
	outcomeMove
	// outcomeGoIn is enter: in one level, or open what is selected.
	outcomeGoIn
	// outcomeGoBack is esc: out one level.
	outcomeGoBack
	// outcomeQuit leaves the dashboard; every session keeps running.
	outcomeQuit
	// outcomeSendToAgent: the key goes to the agent as typed.
	outcomeSendToAgent
	// outcomeOpenStrip is the leader in the Agent: show the key strip
	// (§4.8) and wait for an afk key.
	outcomeOpenStrip
	// outcomeCloseStrip: the strip was open and the key is not one of its
	// keys, so the strip closes and nothing is sent to the agent.
	outcomeCloseStrip
	// outcomePassLeader is the leader twice: the strip closes and one leader
	// key goes to the agent, so a program in the session that wants the
	// leader's byte can still get it.
	outcomePassLeader
)

// String exists for test-failure output; nothing renders it.
func (k keyOutcomeKind) String() string {
	switch k {
	case outcomeNothing:
		return "nothing"
	case outcomeOverlay:
		return "overlay"
	case outcomeRun:
		return "run"
	case outcomeMove:
		return "move"
	case outcomeGoIn:
		return "go-in"
	case outcomeGoBack:
		return "go-back"
	case outcomeQuit:
		return "quit"
	case outcomeSendToAgent:
		return "send-to-agent"
	case outcomeOpenStrip:
		return "open-strip"
	case outcomeCloseStrip:
		return "close-strip"
	case outcomePassLeader:
		return "pass-leader"
	}
	return "unknown"
}

// keyOutcome is decideKey's answer. action is set only for outcomeRun.
type keyOutcome struct {
	kind   keyOutcomeKind
	action modeAction
}

// quitKey quits at All projects and Project. It is not the leader's business
// and not a table key there: it works even when nothing in the table matches,
// so a stuck dashboard can always be left. In the Agent it goes to the agent
// (§5), where a program in the session may need it.
const quitKey = "ctrl+c"

// decideKey says what key k does at level lv, with ov open over it, with the
// key strip open when leaderArmed, and with leader as the configured leader
// key's name (Model.leaderKey.name). The leader is a parameter rather than
// a constant because config.toml sets it (T9.2).
//
// The order of the checks is the rule order of §5, and is load-bearing:
//
//  1. An open overlay gets the key first — esc closes an overlay before it
//     goes back a level, and a letter typed into `:` must type, not run.
//  2. In the Agent with the strip closed, every key but the leader goes to
//     the agent: ctrl+c, esc and q included. The session is a terminal, and a
//     key the dashboard kept would be a key the agent never sees.
//  3. In the Agent with the strip open: the leader again passes one leader
//     through; a key the table lists at the Agent level runs it (esc is one,
//     and goes back to Project); any other key closes the strip and sends
//     nothing, so a mistyped key after the leader never reaches the agent.
//  4. At All projects and Project, ctrl+c quits and a table key runs alone.
//
// leaderArmed is read only in the Agent, the one level with a strip. At All
// projects and Project the leader does nothing: keys already work alone
// there, and a leader that armed something would make the same letter mean
// two things.
func decideKey(lv level, ov keyOverlay, leaderArmed bool, leader, k string) keyOutcome {
	if ov != overlayNone {
		return keyOutcome{kind: outcomeOverlay}
	}
	if lv == levelAgent {
		switch {
		case !leaderArmed && k == leader:
			return keyOutcome{kind: outcomeOpenStrip}
		case !leaderArmed:
			return keyOutcome{kind: outcomeSendToAgent}
		case k == leader:
			return keyOutcome{kind: outcomePassLeader}
		}
		if row, ok := keyRowFor(levelAgent, k); ok {
			return row.outcome()
		}
		return keyOutcome{kind: outcomeCloseStrip}
	}
	if k == quitKey {
		return keyOutcome{kind: outcomeQuit}
	}
	if row, ok := keyRowFor(lv, k); ok {
		return row.outcome()
	}
	return keyOutcome{kind: outcomeNothing}
}
