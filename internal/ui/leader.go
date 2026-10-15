// leader.go holds the actions dialog the leader key (ctrl+]) opens: every
// dashboard action with its key and one line of help, from the session list,
// the overview and the agent pane alike (T7.1).
//
// The leader was Ctrl+Space until T9.1 and is ctrl+] since (docs/decisions.md
// "## T9.1"). ctrl+] is a byte of its own, 0x1D, that neither tmux's default
// prefix (ctrl+b) nor a common custom one (ctrl+a) takes. Ctrl+Space is not:
// a terminal sends Ctrl+Space, ctrl+@ and ctrl+2 all as the one NUL byte, and
// on macOS Raycast takes Ctrl+Space before the terminal sees it at all. Now
// Ctrl+Space is an ordinary key, which the agent pane forwards as NUL.
//
// Since T7.2 the leader is the ONE way to run a dashboard action. The owner
// chose that the session list keeps only the keys that move around (PLAN
// T7.2, docs/decisions.md "## T7.2"): the bare letters, the dashboard chords
// and the `:` palette are gone, and an action is a key pressed after
// the leader. So the dialog's rows no longer come from mode.go's rules —
// there are no rules for these actions left to derive them from — but from
// leaderTable below, which holds the key after the leader for each action and
// the side of the screen it runs on.
//
// Two properties of the T7.1 dialog are kept, each for its reason:
//
//   - It shows actions that cannot run right now, dimmed with the reason. A
//     person who opens the dialog to find a key learns the key and why it is
//     not doing anything, which a missing row cannot tell them.
//   - Its rows carry the modeAction itself, not a keypress to re-press. The
//     dialog is opened from the agent pane too, where re-pressing a key would
//     type it to the agent; the Model runs the row's action through
//     applyKeyAction instead, the function every key reaches.
//
// Nothing here touches the Model. The row builder, the blocked reasons, the
// removed-key notice and the key decision are pure functions of plain data,
// so every branch is a table row (leader_test.go); the Model fills
// leaderConds from its live state and applies the leaderOutcome it gets back.
package ui

import (
	"slices"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// leaderKey is the leader key's string as key.Matches and KeyPressMsg.String
// see it. How it arrives from the terminal is recorded in docs/decisions.md
// (T9.1, "How ctrl+] arrives"): ultraviolet decodes the legacy byte 0x1D and
// the kitty-protocol CSI 93;5u alike to {Code: ']', Mod: ModCtrl}, whose
// String() is "ctrl+]", so this one string matches both. It is not
// Ctrl+Space because that key is the NUL byte ctrl+@ and ctrl+2 also send,
// and Raycast takes it on macOS (see the file comment). TestLeaderKeyDecision
// exercises the match.
const leaderKey = "ctrl+]"

// The dialog's own gestures, declared HELP-ONLY: leaderKeyDecision stages
// them, and declaring them as parent rules in the registry would implement
// them twice. They exist so the footer says what the keys do while the dialog
// is up. leaderFilterKey is bound to backspace because a binding needs a key
// to be shown at all (help.Model skips a binding with none), and backspace is
// the one filter key that has a name; its label says what a person actually
// does, which is type.
var (
	leaderRunKey = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "run"),
	)
	leaderMoveKey = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "move"),
	)
	leaderFilterKey = key.NewBinding(
		key.WithKeys("backspace"),
		key.WithHelp("type", "filter"),
	)
	leaderCloseKey = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close"),
	)
	leaderAgainKey = key.NewBinding(
		key.WithKeys(leaderKey),
		key.WithHelp(leaderKey, "close, or pass it to the agent"),
	)
)

// leaderReach is where a leader action runs from, which decides whether the
// keyboard moves to the session list before it runs. It reproduces where
// each action's old key was a rule (T7.1's runLeaderRow ran a row in place
// exactly when the opening mode had a rule for it), so moving every action
// under the leader changes how it is reached and nothing about what it does.
type leaderReach int

const (
	// reachList is a session-list action (new, one-off, switch project,
	// clean, label, filter, clear filter, focus, close pane, help). From the
	// right side the keyboard goes to the list first and the action runs as
	// from the list, as it did when it was a list key: an overlay it opens is
	// then over the list, and closing it leaves the list with the keyboard.
	reachList leaderReach = iota
	// reachOverview runs where it was opened on the list and the overview
	// (and the agent placeholder, which stands in the overview's mode), and
	// from the agent pane goes through the list first. These were rules in
	// normal and detail mode and never in the pane, whose keys were the
	// agent's: build tasks, inbox, pause, resume, restart, resize.
	reachOverview
	// reachAnywhere runs where it was opened, the agent pane included: quit
	// and the mouse, which do not care where the keyboard is.
	reachAnywhere
	// reachRight is a right-side action (agent or overview, back to list):
	// it runs where it was opened, as inputDetail, and is dimmed from the
	// list, where nothing on the right is under control.
	reachRight
)

// leaderEntry is one action in the leader table.
type leaderEntry struct {
	// key is what is pressed after the leader. It is a key.Matches string
	// ("n", "P", "\\", "?"), so a dialog key is compared with KeyPressMsg's
	// String() exactly as a binding would be. It is "" for an action reached
	// by name only (the plan actions): such a row shows no key, answers to no
	// key, and is run by typing its name in the filter and pressing enter.
	key    string
	action modeAction
	reach  leaderReach
	// also is any key the row answers to in the dialog besides key, and is
	// not shown. Only quit has one: ctrl+c still leaves the dashboard from
	// inside the dialog, so a stuck dashboard can always be left (A3).
	also []string
}

// leaderTable is every dashboard action and its key after the leader, in the
// order the dialog lists them. It is the table in docs/decisions.md "## T7.2"
// row for row, followed by the three by-name plan actions of "## T7.3",
// the by-name "start a new plan" of "## T7.4", the by-name "check the
// plan" of "## T7.7" and the by-name "approve the plan" of T7.8, with
// T7.9's "all projects" on A after switch project, its neighbour in meaning,
// T7.10's add and remove a project on + and - right after it, and T7.12's
// "show this task's tokens" on T right after T7.11's S, and T7.13's
// "refresh usage" on R right after resume;
// TestLeaderTableIsTheDecidedTable holds the two together.
//
// Back to list comes before close pane on purpose: both answer `\`, and among
// rows sharing a key the first one that can run wins (leaderKeyDecision). On
// the right side back to list can run, so `\` gives the keyboard back as
// ctrl+\ did there; on the list it is dimmed, so `\` closes the pane as
// ctrl+\ did there. The other order would close the agent pane from inside it.
var leaderTable = []leaderEntry{
	{key: "n", action: actionNewSession, reach: reachList},
	{key: "o", action: actionScratch, reach: reachList},
	{key: "p", action: actionProjects, reach: reachList},
	// "all projects" (T7.9) is a list action because the screen it opens
	// takes the list's place; from the right side the keyboard comes to the
	// list first, so esc on the screen leaves the list focused. Upper-case A,
	// so lower-case a stays free for typing "approve" from an empty query.
	{key: "A", action: actionAllProjects, reach: reachList},
	// Add and remove a project (T7.10) run only over the all-projects screen
	// (leaderBlockedReason), whose rows they change. Keyed rows, because a
	// name cannot be typed for them: "remove" starts with r, restart's key,
	// and "add" with a, which must stay free for "approve". + and - are no
	// other row's key and read as what they do.
	{key: "+", action: actionProjectAdd, reach: reachList},
	{key: "-", action: actionProjectRemove, reach: reachList},
	{key: "c", action: actionClean, reach: reachList},
	{key: "r", action: actionRestart, reach: reachOverview},
	{key: "l", action: actionEditLabel, reach: reachList},
	{key: "/", action: actionFilterStart, reach: reachList},
	{key: "x", action: actionFilterClear, reach: reachList},
	{key: "b", action: actionBuild, reach: reachOverview},
	{key: "i", action: actionInbox, reach: reachOverview},
	{key: "P", action: actionPauseBuild, reach: reachOverview},
	{key: "U", action: actionResumeBuild, reach: reachOverview},
	// "refresh usage" (T7.13) takes a fresh usage reading for the header.
	// Upper-case R: lower-case r is restart's key, and a free lower-case
	// letter would run the row from an empty query instead of starting a
	// filter. It sits after pause and resume, the other rows about the
	// build's use of the agent.
	{key: "R", action: actionRefreshUsage, reach: reachOverview},
	// "set this task's status" (T7.11) acts on the Build tab's cursor row,
	// which is drawn only while the right side has the keyboard, so it is a
	// right-side action. Upper-case S: lower-case s would run it from an
	// empty query instead of filtering for "start a new plan".
	{key: "S", action: actionTaskStatus, reach: reachRight},
	// "show this task's tokens" (T7.12) reads the same cursor row, so it is
	// a right-side action beside S. Upper-case T: lower-case t is the
	// agent-or-overview toggle, and a free lower-case letter would run the
	// row from an empty query instead of starting a filter.
	{key: "T", action: actionTaskTokens, reach: reachRight},
	{key: "f", action: actionFocusPane, reach: reachList},
	{key: "t", action: actionPaneToggle, reach: reachRight},
	{key: "\\", action: actionDetailUnfocus, reach: reachRight},
	{key: "\\", action: actionPaneClose, reach: reachList},
	{key: "g", action: actionMouseToggle, reach: reachAnywhere},
	{key: "w", action: actionEnterResize, reach: reachOverview},
	{key: "?", action: actionHelp, reach: reachList},
	{key: "q", action: actionQuit, reach: reachAnywhere, also: []string{"ctrl+c"}},
	// The plan actions (T7.3) have no key: the master plan says "by name
	// only", and the dashboard is short of free letters. They are list
	// actions because amend and reconcile open a form, which belongs over the
	// list; a start then moves the keyboard to the planning session's pane.
	{key: "", action: actionPlanExtend, reach: reachList},
	{key: "", action: actionPlanAmend, reach: reachList},
	{key: "", action: actionPlanReconcile, reach: reachList},
	// "start a new plan" (T7.4) is by name for the same reasons. It is a list
	// action because it opens a form, and a start moves the dashboard to the
	// folder's project and the keyboard to its planning pane.
	{key: "", action: actionPlanNew, reach: reachList},
	// "check the plan" (T7.7) is by name for the same reasons. It is a list
	// action because its overlay floats over the list, as the inbox's does;
	// it changes nothing, so where the keyboard was matters only for where
	// esc returns it.
	{key: "", action: actionPlanCheck, reach: reachList},
	// "approve the plan" (T7.8) is by name for the same reasons. It is a list
	// action because its confirm is a footer modal over the list; on y the
	// work runs in the background and the keyboard stays on the list.
	{key: "", action: actionPlanApprove, reach: reachList},
}

// mode is the mode a reach's action runs as through applyKeyAction:
// inputDetail for the right side's own actions, inputNormal for the rest.
// Only the modal answers read the mode there, so for every action in the
// table this records the side, not a branch.
func (r leaderReach) mode() inputMode {
	if r == reachRight {
		return inputDetail
	}
	return inputNormal
}

// leaderMovesToList reports whether running an action of reach r from mode
// `from` first gives the keyboard to the session list. from is the mode the
// leader was pressed in: inputNormal, inputDetail (the overview or the agent
// placeholder) or inputPane.
//
// A list action from the right side moves the keyboard, as its old list key
// could only be pressed there; an overview action moves it only from the
// agent pane, where its old letter was typed to the agent instead. Running a
// list action with the agent still focused would be worse than odd: an
// overlay it opens would close back onto the agent, and keys meant for the
// dashboard would be typed into the session.
func leaderMovesToList(r leaderReach, from inputMode) bool {
	switch r {
	case reachList:
		return from != inputNormal
	case reachOverview:
		return from == inputPane
	}
	return false
}

// leaderNames is each listed action's row name, which is also what the
// filter matches first. The names are the plain words of the T7.2 key table
// rather than the old bindings' terse help ("label", "projects"), which sat
// next to context the dialog does not have.
var leaderNames = map[modeAction]string{
	actionNewSession:    "new session",
	actionScratch:       "one-off session",
	actionProjects:      "switch project",
	actionAllProjects:   "all projects",
	actionProjectAdd:    "add a project",
	actionProjectRemove: "remove the project",
	actionClean:         "clean session",
	actionRestart:       "restart agent",
	actionEditLabel:     "edit label",
	actionFilterStart:   "filter sessions",
	actionFilterClear:   "clear filter",
	actionBuild:         "build tasks",
	actionInbox:         "inbox",
	actionPauseBuild:    "pause the build",
	actionResumeBuild:   "resume the build",
	actionTaskStatus:    "set this task's status",
	actionTaskTokens:    "show this task's tokens",
	actionRefreshUsage:  "refresh usage",
	actionFocusPane:     "focus the right side",
	actionPaneToggle:    "agent or overview",
	actionDetailUnfocus: "back to list",
	actionPaneClose:     "close pane",
	actionMouseToggle:   "mouse",
	actionEnterResize:   "resize panes",
	actionHelp:          "help",
	actionQuit:          "quit",
	actionPlanExtend:    "extend the plan",
	actionPlanAmend:     "amend the plan",
	actionPlanReconcile: "reconcile the plan with a document",
	actionPlanNew:       "start a new plan",
	actionPlanCheck:     "check the plan",
	actionPlanApprove:   "approve the plan",
}

// leaderHelp is the one line of help each listed action shows. Every action
// the table lists must have a line; the test that checks it is what makes a
// later action's missing line a failure rather than a blank row.
var leaderHelp = map[modeAction]string{
	actionPaneClose:     "Close the agent pane; the session keeps running",
	actionQuit:          "Leave the dashboard; every session keeps running",
	actionFocusPane:     "Move the keyboard to the right side",
	actionMouseToggle:   "Let the terminal select text, or take the mouse back",
	actionNewSession:    "Start a new session from a plan or a branch",
	actionScratch:       "Start a one-off session with no plan",
	actionProjects:      "Switch to another project",
	actionAllProjects:   "Every project's build and inbox on one screen; enter switches",
	actionClean:         "Clean the selected session and its worktree",
	actionRestart:       "Restart the selected session's stopped or stuck agent",
	actionEditLabel:     "Give the selected session a display label",
	actionFilterStart:   "Filter the session list by name",
	actionFilterClear:   "Show every session again",
	actionBuild:         "Show the build's tasks",
	actionInbox:         "Read and answer the inbox",
	actionPauseBuild:    "Hold the build so no new task starts",
	actionResumeBuild:   "Let a paused build carry on",
	actionEnterResize:   "Move the divider between the list and the right side",
	actionDetailUnfocus: "Hand the keyboard back to the session list",
	actionPaneToggle:    "Switch the right side between the agent and the overview",
	actionHelp:          "Show every key on the session list, or hide them again",
	actionPlanExtend:    "Start a planning session that adds work to the plan",
	actionPlanAmend:     "Change the plan; asks what to change in a sentence",
	actionPlanReconcile: "Fit the plan to a document; asks for its path",
	actionPlanNew:       "Plan a project that has no plan; asks for its folder or a new name",
	// "errors" is the word this row is found by. Its name cannot be typed:
	// c is clean's key and runs it from an empty query, and "plan" starts
	// with p, the switcher's. The help line's other leading words are no
	// better — "warnings" starts with w (resize, which always runs) and
	// "list" with l (the label, which a dimmed row still swallows). No other
	// row's name or help says "errors" or "warnings"
	// (TestLeaderPlanCheckWordsAreUnique).
	actionPlanCheck: "Show the plan's errors and warnings, as afk plan lint does",
	// "approve" is the word this row is found by, and its first letter, a,
	// is no row's key, so typing it from an empty query filters rather than
	// running anything. No other row's name or help says "approve"
	// (TestLeaderPlanApproveWordIsUnique). The line must not say "errors"
	// or "warnings", which are the check-the-plan row's.
	actionPlanApprove: "Approve a new plan and start its build, as afk plan approve does",
	// The two lines must not say "approve", "errors" or "document", which
	// are the words other rows are found by (TestLeaderPlanApproveWordIsUnique
	// and its neighbours).
	actionProjectAdd:    "Put a folder on the daemon's list, as afk project add does",
	actionProjectRemove: "Take the selected project off the daemon's list; its files stay",
	// Must not say "approve", "errors", "warnings" or "document", the words
	// other rows are found by.
	actionTaskStatus: "Change the selected Build task's status, as afk ledger set does",
	// The same words to avoid as the line above.
	actionTaskTokens: "Show how many tokens the selected Build task used, as afk tokens does",
	// The same words to avoid as the two lines above.
	actionRefreshUsage: "Take a fresh usage reading for the header, as afk usage --refresh does",
}

// leaderRow is one action in the dialog.
type leaderRow struct {
	action modeAction
	// mode is the mode the action runs as (leaderReach.mode).
	mode inputMode
	// reach is where it runs from (leaderMovesToList).
	reach leaderReach
	// name is what the row is called, and what the filter matches first.
	name string
	// keyLabel is the key after the leader, as the row shows it.
	keyLabel string
	// keys are every key the row answers to in the dialog; keys[0] is
	// keyLabel's key, the one a press is synthesized from when the action
	// needs the key itself (the filter's `/` is handed on to the list). It is
	// empty for a row reached by name only, so no typed key can match it.
	keys []string
	// help is one line of plain language from leaderHelp.
	help string
	// blocked is why the action cannot run right now, or "" when it can.
	blocked string
}

// leaderRows builds the dialog's rows from leaderTable, in its order, with
// each row's blocked reason filled from conds so the rows the dialog holds
// already say what cannot run.
func leaderRows(conds leaderConds) []leaderRow {
	rows := make([]leaderRow, 0, len(leaderTable))
	for _, e := range leaderTable {
		// A by-name row gets no keys at all rather than [""]: an empty string
		// in keys would be one more thing every matcher has to know never to
		// match.
		var keys []string
		if e.key != "" {
			keys = append([]string{e.key}, e.also...)
		}
		rows = append(rows, leaderRow{
			action:   e.action,
			mode:     e.reach.mode(),
			reach:    e.reach,
			name:     leaderNames[e.action],
			keyLabel: e.key,
			keys:     keys,
			help:     leaderHelp[e.action],
			blocked:  leaderBlockedReason(e.action, conds),
		})
	}
	return rows
}

// leaderKeyFor is the key after the leader that runs action a, or "" when
// the table has none.
func leaderKeyFor(a modeAction) string {
	for _, e := range leaderTable {
		if e.action == a {
			return e.key
		}
	}
	return ""
}

// removedListKeys are the keys the session list used to answer with an
// action before T7.2: every bare letter that moved under the leader, and `:`,
// the palette's key. Pressing one now does nothing but raise a notice naming
// the new key (removedKeyNotice), so a hand that still remembers the old key
// learns the new one instead of seeing nothing happen. Only the list gets
// these: on the overview a stray letter was always ignored, and the old
// chords now belong to the agent in the agent pane, so a notice for them
// would be wrong in the one place they are pressed most.
var removedListKeys = []string{"n", "o", "p", "c", "r", "l", "/", "b", "i", "q", "f", "?", "P", "U", ":"}

// removedKeyNotice is the notice for key s pressed on the session list, and
// whether s is one of removedListKeys. Every removed letter kept its letter
// under the leader; the table is still asked, so a letter that ever left the
// table would lose its notice rather than name a key that does nothing.
func removedKeyNotice(s string) (string, bool) {
	if !slices.Contains(removedListKeys, s) {
		return "", false
	}
	if s == ":" {
		return ": moved: press " + leaderKey + ", then type to filter the actions", true
	}
	if leaderActionForKey(s) == actionNone {
		return "", false
	}
	return s + " moved: press " + leaderKey + " then " + s, true
}

// leaderChord is how a hint or a notice names the key that runs action a:
// "ctrl+] n". It reads the key from leaderTable, so a hint cannot name a
// key the dialog does not answer to. An action the table does not list gives
// the leader alone, which still tells the reader where to look.
func leaderChord(a modeAction) string {
	if k := leaderKeyFor(a); k != "" {
		return leaderKey + " " + k
	}
	return leaderKey
}

// leaderHelpBinding is a help entry that shows "ctrl+] X" for action a
// with the given short description, for the footer and the full help. Its
// match key is the plain key after the leader: nothing dispatches on it (the
// dialog does, from leaderTable), it only has to be non-empty because
// help.Model skips a binding that has no key. enabled false drops it from the
// line, on the rule that help never offers a key that would do nothing.
func leaderHelpBinding(a modeAction, desc string, enabled bool) key.Binding {
	b := key.NewBinding(
		key.WithKeys(leaderKeyFor(a)),
		key.WithHelp(leaderChord(a), desc),
	)
	b.SetEnabled(enabled)
	return b
}

// leaderActionForKey is the first table action whose key is s, or
// actionNone. An empty s is nobody's key: the by-name rows store "" and must
// not be found by it.
func leaderActionForKey(s string) modeAction {
	if s == "" {
		return actionNone
	}
	for _, e := range leaderTable {
		if e.key == s {
			return e.action
		}
	}
	return actionNone
}

// leaderConds is the live state the blocked reasons are decided from, as
// plain booleans the Model fills when the dialog opens. Each one mirrors a
// guard an action's own handler applies, so the dialog dims exactly the rows
// that would do nothing (or only say why it did nothing).
type leaderConds struct {
	// hasActiveProject is Model.hasActiveProject: the new-session form, the
	// one-off picker and the switcher are inert without one.
	hasActiveProject bool
	// selected is "a session row is selected" (Model.selected).
	selected bool
	// selectedPending is the selected row's create still running.
	selectedPending bool
	// selectedFault is the selected row being a worktree fault
	// (isWorktreeFault), which openLabelForm refuses.
	selectedFault bool
	// selectedCleaning is the selected row being cleaned already.
	selectedCleaning bool
	// paneOpen is "an agent pane is installed", visible or behind the
	// overview (Model.pane != nil). It is routeConds.paneOpen.
	paneOpen bool
	// rightFocused is "the right side has the keyboard": the focused overview
	// or the focused agent pane (Model.focus == focusRight).
	rightFocused bool
	// listFiltered is routeConds.listFiltered.
	listFiltered bool
	// hasBuild is Model.hasBuild: the project has a master plan.
	hasBuild bool
	// buildKnown is Model.buildKnown: the build files have been read once,
	// which openInbox needs before it has anything to show.
	buildKnown bool
	// canStartPlan is "the StartPlan seam is wired", openPlanNewForm's only
	// guard.
	canStartPlan bool
	// canCheckPlan is "the LoadPlanStatus seam is wired": without it no
	// plan status ever arrives, and openPlanCheck could only ever say the
	// plan has not been read yet.
	canCheckPlan bool
	// canApprovePlan is "the ApprovePlan seam is wired".
	canApprovePlan bool
	// canAllProjects is "the LoadAllProjects seam is wired": without it the
	// all-projects screen has nothing to show, and toggleAllProjects only
	// says so.
	canAllProjects bool
	// allProjectsOpen is Model.allProjectsOpen. Over the screen the dialog
	// runs only what makes sense there (leaderBlockedReason).
	allProjectsOpen bool
	// canAddProject and canRemoveProject are "the AddProject / RemoveProject
	// seam is wired" (T7.10).
	canAddProject, canRemoveProject bool
	// projectRowSelected is "the all-projects screen has a row under its
	// cursor"; projectRowListed and projectRowName are that row's Listed and
	// Name, which the remove row's reason reads.
	projectRowSelected, projectRowListed bool
	projectRowName                       string
	// canLoadTaskTokens is "the LoadTaskTokens seam is wired" (T7.12).
	canLoadTaskTokens bool
	// canRefreshUsage is "the RefreshUsage seam is wired" (T7.13).
	canRefreshUsage bool
	// canSetTaskStatus is "the SetTaskStatus seam is wired" (T7.11).
	// buildTabFocused is "the Build tab is drawn on the right and the right
	// side has the keyboard", and buildTaskSelected "its cursor is on a
	// task" (buildCursorShown and a cursor id in the rows): the picker acts
	// on that task, and the cursor is not drawn anywhere else.
	canSetTaskStatus, buildTabFocused, buildTaskSelected bool
	// planKnown, planErr, planSession, planDamaged, planMode and planPhase
	// are Model.planStatus as the approve row needs it: read at least once,
	// a load error, a planning session, a damaged state file, and the
	// session's mode and phase. They are the facts `afk plan approve` gates
	// on, so the row is dimmed for exactly the plans the command refuses for
	// their state; the command still has the last word (it re-reads them).
	planKnown, planErr, planSession, planDamaged bool
	planMode, planPhase                          string
	// restartLive is Model.restartKeyLive, the gate the help uses.
	restartLive bool
	// canFocus is normalBindingEnabled's answer for the Focus binding: a pane,
	// an overview or the agent placeholder is drawn on the right.
	canFocus bool
	// canToggleView is toggleView's own gate: the selected row has a live
	// agent or a pane, so flipping the right side changes something.
	canToggleView bool
}

// leaderBlockedReason says why action a cannot run under c, in a few plain
// words, or "" when it can. The checks copy the handlers' own guards
// (cleanRequested and cleanSelected, restartKeyLive, openLabelForm,
// showBuildTab, pauseBuild, resumeBuild, openInbox, openSwitcher,
// openNewForm, focusRightSide, toggleView) and the two guards the old rules
// carried — close pane needed a pane, clear filter needed a filter — which
// went with the rules and live here now. A reason here that a handler does
// not enforce would dim a row that works, so nothing is added that the
// handlers do not check — with two exceptions, both about where the keyboard
// is: agent/overview and back to list are right-side actions, and from the
// list the dialog must not let them reach across to the right side.
//
// Add and remove a project (T7.10) are decided first, by
// projectEditBlockedReason, because they are the reverse of every other row:
// they run only over the all-projects screen. Then, over the screen, one rule
// decides alone: pause, resume, all projects (which closes the screen), the
// mouse and quit run, and every other row is dimmed. Those rows act on the
// session list, the detail side or the active project, none of which is on
// screen. Pause and resume
// are not dimmed there for the active project's build: they act on the
// selected row, and pauseSelectedProject says in a sentence when that row has
// nothing to pause.
func leaderBlockedReason(a modeAction, c leaderConds) string {
	const noSession = "no session selected"
	switch a {
	case actionProjectAdd, actionProjectRemove:
		// Decided here whether or not the screen is open: they are the two
		// rows that run only over it.
		return projectEditBlockedReason(a, c)
	}
	if c.allProjectsOpen {
		switch a {
		case actionPauseBuild, actionResumeBuild, actionAllProjects, actionMouseToggle, actionQuit:
			return ""
		}
		return allProjectsCloseFirst
	}
	switch a {
	case actionAllProjects:
		// Not dimmed for "no project is open": the screen lists the projects
		// in config.toml, which a dashboard started outside any project can
		// still show and switch to.
		if !c.canAllProjects {
			return "this dashboard was started without the project reader"
		}
	case actionPaneClose:
		if !c.paneOpen {
			return "no agent pane is open"
		}
	case actionFocusPane:
		switch {
		case !c.hasActiveProject:
			return "no project is open"
		case c.rightFocused:
			return "the right side already has the keyboard"
		case !c.canFocus:
			return "nothing to focus on the right"
		}
	case actionNewSession, actionScratch, actionPlanExtend, actionPlanAmend, actionPlanReconcile:
		// The plan actions are dimmed for nothing else. Whether a change
		// session may start (a plan exists, none is open, the agent can plan)
		// is the start's own decision, and its refusal is shown in its own
		// words; a guess here would dim a row that works.
		if !c.hasActiveProject {
			return "no project is open"
		}
	case actionPlanNew:
		// Not dimmed for "no project is open": the folder is typed, so a
		// dashboard started outside any project can still start one, and that
		// is the case the action matters most for. Only a missing seam stops
		// it, and openPlanNewForm says so in a sentence if it is run anyway.
		if !c.canStartPlan {
			return "starting a plan is not available here"
		}
	case actionPlanCheck:
		// Not dimmed for "no project is open" or "not read yet": both are
		// said in a sentence by openPlanCheck, which is more use than a
		// dimmed row. Only a missing seam stops it for good.
		if !c.canCheckPlan {
			return "checking the plan is not available here"
		}
	case actionPlanApprove:
		return planApproveBlockedReason(c)
	case actionTaskStatus:
		return taskStatusBlockedReason(c)
	case actionTaskTokens:
		return taskTokensBlockedReason(c)
	case actionRefreshUsage:
		return usageRefreshBlockedReason(c)
	case actionProjects:
		if !c.hasActiveProject {
			return "no projects to switch between"
		}
	case actionClean:
		switch {
		case !c.selected:
			return noSession
		case c.paneOpen:
			return "close the pane first (" + leaderKey + " \\)"
		case c.selectedPending:
			return "this session is still being created"
		case c.selectedCleaning:
			return "this session is already being cleaned"
		}
	case actionRestart:
		switch {
		case !c.selected:
			return noSession
		case !c.restartLive:
			return "only a stopped or stuck agent can be restarted"
		}
	case actionEditLabel:
		switch {
		case !c.selected:
			return noSession
		case c.selectedPending:
			return "this session is still being created"
		case c.selectedFault:
			return "this row has no worktree to label"
		}
	case actionFilterClear:
		if !c.listFiltered {
			return "no filter is applied"
		}
	case actionBuild, actionPauseBuild, actionResumeBuild:
		if !c.hasActiveProject || !c.hasBuild {
			return "this project has no build"
		}
	case actionInbox:
		if !c.buildKnown {
			return "the inbox has not been read yet"
		}
	case actionDetailUnfocus:
		if !c.rightFocused {
			return "the session list already has the keyboard"
		}
	case actionPaneToggle:
		switch {
		case !c.rightFocused:
			return "focus the right side first (enter)"
		case !c.canToggleView:
			return "this session has no live agent"
		}
	}
	return ""
}

// planApproveBlockedReason is the "approve the plan" row's reason, the one
// row dimmed for the plan's state rather than the dashboard's. Only a new plan
// at review can be approved; the reasons follow the command's own refusals
// (plan_approve_cmd.go) in plainer words, and openPlanApprove asks this same
// function, so the row and the action cannot disagree.
//
// The order is load-bearing. The seam and the project come first because
// without them nothing about the plan is known. A status not read yet must
// not be worded as "no planning session", which it may well have. A load
// error and a damaged file win over any phase, because the phase of a file
// that cannot be read or trusted means nothing. Approved is checked before
// the mode, so an approved plan says so rather than that it is the wrong
// kind of session.
func planApproveBlockedReason(c leaderConds) string {
	switch {
	case !c.canApprovePlan:
		return "approving is not available here"
	case !c.hasActiveProject:
		return "no project is open"
	case !c.planKnown:
		return "the plan status has not been read yet"
	case c.planErr:
		return "the plan status cannot be read"
	case !c.planSession:
		return "no planning session"
	case c.planDamaged:
		return "the planning state file is damaged"
	case c.planPhase == "approved":
		return "the plan is already approved"
	case c.planMode != "new":
		return "only a new plan is approved; this session is " + c.planMode
	case c.planPhase == "":
		// planStatusLine words an empty phase by what it waits for; here
		// "at , not review" would be all that was left.
		return "the plan is not at review yet"
	case c.planPhase != "review":
		return "the plan is at " + c.planPhase + ", not review"
	}
	return ""
}

// leaderNamedKeys maps the bubbles/key name of every non-printable key a
// leader-table row or a test could carry to its tea key code. Only keys this
// package could plausibly carry are listed; anything else fails the
// round-trip in leaderPressFor rather than being guessed at.
var leaderNamedKeys = map[string]rune{
	"enter":     tea.KeyEnter,
	"esc":       tea.KeyEsc,
	"tab":       tea.KeyTab,
	"space":     tea.KeySpace,
	"backspace": tea.KeyBackspace,
	"delete":    tea.KeyDelete,
	"insert":    tea.KeyInsert,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
}

// leaderPressFor builds the tea.KeyPressMsg that a key.Binding key string
// names, so a row whose handler reads the key (startSessionFilter hands `/`
// on to the list) gets the key a press of it would have delivered.
//
// The reported ok is a ROUND-TRIP assertion, not a parse-succeeded flag: the
// constructed message is accepted only when its String() reproduces the exact
// string it was built from — the same String() key.Matches compares against
// (bubbles/v2@v2.2.1 key.go:Matches). That is what makes a wrong parse fail
// closed: a mis-decoded key would otherwise reach a handler as some OTHER key.
//
// Text is set for an unmodified printable rune and left empty otherwise,
// mirroring how the terminal's own decoding arrives: Key.String returns Text
// when it is non-empty (ultraviolet key.go:391), and Keystroke's
// modifier-qualified form otherwise.
func leaderPressFor(name string) (tea.KeyPressMsg, bool) {
	rest := name
	var mod tea.KeyMod
	for parsing := true; parsing; {
		switch {
		case strings.HasPrefix(rest, "ctrl+"):
			mod |= tea.ModCtrl
			rest = strings.TrimPrefix(rest, "ctrl+")
		case strings.HasPrefix(rest, "alt+"):
			mod |= tea.ModAlt
			rest = strings.TrimPrefix(rest, "alt+")
		case strings.HasPrefix(rest, "shift+"):
			mod |= tea.ModShift
			rest = strings.TrimPrefix(rest, "shift+")
		default:
			parsing = false
		}
	}

	var k tea.KeyPressMsg
	if code, ok := leaderNamedKeys[rest]; ok {
		k = tea.KeyPressMsg{Code: code, Mod: mod}
	} else {
		runes := []rune(rest)
		if len(runes) != 1 {
			return tea.KeyPressMsg{}, false
		}
		k = tea.KeyPressMsg{Code: runes[0], Mod: mod}
		if mod == 0 {
			k.Text = rest
		}
	}
	if k.String() != name {
		return tea.KeyPressMsg{}, false
	}
	return k, true
}

// leaderOutcomeKind is what one key in the dialog does.
type leaderOutcomeKind int

const (
	// leaderIgnore is the zero value: nothing happens and the dialog stays
	// open, so a caller that forgets to check does nothing rather than
	// something wrong.
	leaderIgnore leaderOutcomeKind = iota
	// leaderRun closes the dialog and runs leaderOutcome.row's action.
	leaderRun
	// leaderFilter edits the query: appends leaderOutcome.text, or drops the
	// last character when leaderOutcome.backspace is set.
	leaderFilter
	// leaderClose closes the dialog and does nothing else.
	leaderClose
	// leaderPassThrough closes the dialog and types ctrl+] to the agent:
	// the leader pressed twice from the agent pane is how the agent still gets
	// the key the dashboard took for itself.
	leaderPassThrough
	// leaderMove moves the highlight by leaderOutcome.delta.
	leaderMove
)

// String exists for test-failure output; nothing renders it.
func (k leaderOutcomeKind) String() string {
	switch k {
	case leaderIgnore:
		return "ignore"
	case leaderRun:
		return "run"
	case leaderFilter:
		return "filter"
	case leaderClose:
		return "close"
	case leaderPassThrough:
		return "pass-through"
	case leaderMove:
		return "move"
	}
	return "unknown"
}

// leaderOutcome is leaderKeyDecision's answer. Only the fields its kind names
// are meaningful.
type leaderOutcome struct {
	kind      leaderOutcomeKind
	row       leaderRow
	text      string
	backspace bool
	delta     int
}

// leaderKeyDecision decides what key k does in the dialog. rows are the rows
// on screen (already filtered by query) and highlight indexes them.
//
// The rules run in a fixed order, and the order is the design:
//
//  1. esc closes, whatever is typed — one key out, always.
//  2. The leader closes; from the agent pane it also passes the leader on.
//  3. up and down move the highlight.
//  4. enter runs the highlighted row. enter and esc are checked before any
//     row's keys; no row carries either (rows carry the key after the
//     leader), and the order keeps it so if one ever did.
//  5. backspace edits the query.
//  6. With nothing typed, a key that is a row's key runs that row. Among
//     rows sharing a key (`\` is both back-to-list and close-pane) the
//     first one that can run wins, so a dimmed row cannot swallow the key
//     from the runnable one.
//  7. A printable key filters: one that is nobody's key starts the query, and
//     once there is a query every printable key is part of it — typing
//     "clean" must not open the creation form at its `n`.
//
// Anything else is ignored. A run that lands on a dimmed row is ignored too:
// the dialog stays open and the row keeps saying why.
func leaderKeyDecision(k tea.KeyPressMsg, query string, rows []leaderRow, highlight int, openedFromPane bool) leaderOutcome {
	s := k.String()
	switch s {
	case "esc":
		return leaderOutcome{kind: leaderClose}
	case leaderKey:
		if openedFromPane {
			return leaderOutcome{kind: leaderPassThrough}
		}
		return leaderOutcome{kind: leaderClose}
	case "up":
		return leaderOutcome{kind: leaderMove, delta: -1}
	case "down":
		return leaderOutcome{kind: leaderMove, delta: 1}
	case "enter":
		if highlight < 0 || highlight >= len(rows) {
			return leaderOutcome{}
		}
		return leaderRunOutcome(rows[highlight])
	case "backspace":
		return leaderOutcome{kind: leaderFilter, backspace: true}
	}
	if query == "" {
		matched := false
		for _, r := range rows {
			if !slices.Contains(r.keys, s) {
				continue
			}
			matched = true
			if r.blocked == "" {
				return leaderOutcome{kind: leaderRun, row: r}
			}
		}
		if matched {
			return leaderOutcome{}
		}
	}
	if k.Text != "" {
		return leaderOutcome{kind: leaderFilter, text: k.Text}
	}
	return leaderOutcome{}
}

// leaderRunOutcome runs r unless it is dimmed.
func leaderRunOutcome(r leaderRow) leaderOutcome {
	if r.blocked != "" {
		return leaderOutcome{}
	}
	return leaderOutcome{kind: leaderRun, row: r}
}

// leaderDialog is the dialog's state. It holds a snapshot of the rows taken
// when it opened and never refreshes itself: the discovery tick fires every
// two seconds, and rebuilding under the user's hands would move the
// highlight they are aiming with.
type leaderDialog struct {
	rows      []leaderRow
	query     string
	highlight int
	// openedFromPane records that the agent pane had the keyboard when the
	// leader was pressed. The dialog's own mode outranks the pane's, so this
	// cannot be worked out again later and has to be kept.
	openedFromPane bool
}

func newLeaderDialog(rows []leaderRow, openedFromPane bool) leaderDialog {
	return leaderDialog{rows: rows, openedFromPane: openedFromPane}
}

// visible is the rows the query keeps: a case-insensitive substring match on
// the name, the help line and the key label. A substring rather than a
// fuzzy match, because the dialog's list is short and a person
// typing a word expects rows containing that word, not rows that happen to
// contain its letters in order.
func (d leaderDialog) visible() []leaderRow {
	q := strings.ToLower(strings.TrimSpace(d.query))
	if q == "" {
		return d.rows
	}
	var out []leaderRow
	for _, r := range d.rows {
		hay := strings.ToLower(r.name + " " + r.help + " " + r.keyLabel)
		if strings.Contains(hay, q) {
			out = append(out, r)
		}
	}
	return out
}

// decide is leaderKeyDecision against the dialog's own state.
func (d leaderDialog) decide(k tea.KeyPressMsg) leaderOutcome {
	return leaderKeyDecision(k, d.query, d.visible(), d.highlight, d.openedFromPane)
}

// apply carries out the two outcomes that change the dialog itself, filter
// and move; the others are the Model's to act on. A query edit puts the
// highlight back on the first row, so the highlight can never index past a
// list the filter just shortened.
func (d *leaderDialog) apply(o leaderOutcome) {
	switch o.kind {
	case leaderFilter:
		if o.backspace {
			if d.query != "" {
				_, size := utf8.DecodeLastRuneInString(d.query)
				d.query = d.query[:len(d.query)-size]
			}
		} else {
			d.query += o.text
		}
		d.highlight = 0
	case leaderMove:
		n := len(d.visible())
		if n == 0 {
			d.highlight = 0
			return
		}
		// Wraps at both ends, so the last row is one up-arrow away.
		d.highlight = ((d.highlight+o.delta)%n + n) % n
	}
}

// leaderDialogWidth is the dialog's preferred content width: the widest key
// label, name and help line side by side with room to spare at 120 columns.
// On an 80-column terminal modalContentDims narrows it and the help is cut.
const leaderDialogWidth = 96

// leaderHint is the bottom edge's key hint.
const leaderHint = "key runs · enter runs · type to filter · esc closes"

// view renders the dialog as a modal box for a terminal of width×height —
// the box itself, ready for composeModal. The box is sized by
// modalContentDims, so it fits inside the terminal less the modal margin;
// every line is truncated to the content width, so nothing wraps past the
// border, and the rows scroll to keep the highlight on screen.
func (d leaderDialog) view(width, height int) string {
	rows := d.visible()
	// Query line, a blank line, then the rows.
	contentW, contentH := modalContentDims(width, height, leaderDialogWidth, max(len(d.rows), 1)+2)
	area := max(contentH-2, 1)

	query := theme.dim.Render("type to filter, or press an action's key")
	if d.query != "" {
		query = "> " + d.query
	}
	lines := []string{truncate(query, contentW), ""}

	if len(rows) == 0 {
		lines = append(lines, theme.dim.Render(truncate("no action matches", contentW)))
	} else {
		// Columns are measured over every row, not just the visible ones, so
		// they do not jump about as the query narrows the list.
		keyW, nameW := 0, 0
		for _, r := range d.rows {
			keyW = max(keyW, lipgloss.Width(r.keyLabel))
			nameW = max(nameW, lipgloss.Width(r.name))
		}
		start := 0
		if d.highlight >= area {
			start = d.highlight - area + 1
		}
		end := min(start+area, len(rows))
		for i := start; i < end; i++ {
			lines = append(lines, leaderRowLine(rows[i], keyW, nameW, contentW, i == d.highlight))
		}
	}
	return renderModalBox("actions", leaderHint, strings.Join(lines, "\n"), contentW, contentH)
}

// leaderRowLine is one row: key, name, then the help — or, for a dimmed row,
// the reason it cannot run, which is the more useful of the two when both
// will not fit. The whole line is cut to width before it is styled as
// selected, and padded so the highlight bar spans the box.
func leaderRowLine(r leaderRow, keyW, nameW, width int, selected bool) string {
	tail := r.help
	if r.blocked != "" {
		tail = r.blocked
	}
	plain := padRight(r.keyLabel, keyW) + "  " + padRight(r.name, nameW) + "  — " + tail
	line := truncate(plain, width)
	switch {
	case selected:
		return theme.rowSelected.Width(width).Render(spanStyle(line, theme.rowSelected))
	case r.blocked != "":
		return theme.dim.Render(line)
	default:
		// The key column in the help key's colour so the eye can run down the
		// keys. The cut above is measured on the plain text, so styling the
		// key cannot change where it falls. A by-name row has no key to
		// colour.
		if r.keyLabel != "" && lipgloss.Width(r.keyLabel) <= width && strings.HasPrefix(line, r.keyLabel) {
			return theme.helpStyles.ShortKey.Render(r.keyLabel) + strings.TrimPrefix(line, r.keyLabel)
		}
		return line
	}
}
