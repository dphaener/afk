// keytable.go holds the one key table of the three-level dashboard
// (docs/dash-design.md §5, T16.2): every action with its key, the levels the
// key works at, its name for the `:` search and one line of help for `?` and
// `:`. It replaced the leader table of T7.2, which listed the key after the
// leader for each action because the leader was then the one way to run
// anything. With the agent on a level of its own, keys work alone at All
// projects and Project, and in the Agent the leader then the same key does
// the same thing, so one table says both.
//
// Everything that names a key reads it from here: decideKey (levels.go) for
// what a key does, the `:` search's rows (actionRows), the footers and the
// hints in notices (keyName), so no surface can name a key the dashboard
// does not answer to.
//
// Like leader.go, nothing here touches the Model. A row's blocked reason is a
// function over the same leaderConds facts the leader dialog dims its rows
// with, so the `:` search can say why an action cannot run now in the words
// the dashboard already uses.
package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
)

// levelSet is a set of levels, one bit per level.
type levelSet uint8

const (
	atAllProjects levelSet = 1 << levelAllProjects
	atProject     levelSet = 1 << levelProject
	atAgent       levelSet = 1 << levelAgent
	atEveryLevel           = atAllProjects | atProject | atAgent
)

// has reports whether l is in the set.
func (s levelSet) has(l level) bool {
	return s&(1<<l) != 0
}

// keyRow is one action in the key table.
type keyRow struct {
	action modeAction
	// key is the key as key.Matches and KeyPressMsg.String see it ("n",
	// "esc", "+"), or "" for an action reached by name only, which answers
	// to no key and is run from the `:` search.
	key string
	// also is any key the row answers to besides key. Only the move row and
	// quit have them.
	also []string
	// label is how the key is shown when it is not key itself: the move row
	// stands for eight keys, and "↑" reads better than "up".
	label string
	// levels is where the row runs: by its key alone at All projects and
	// Project, by the leader then its key in the Agent, and, for a by-name
	// row, from the `:` search.
	levels levelSet
	// name is what the `:` search finds the row by.
	name string
	// help is the one line `?` and `:` show beside the key.
	help string
	// blocked says why the action cannot run now, or "" when it can. nil
	// means it always can.
	blocked func(leaderConds) string
}

// keyLabel is the key as a row shows it.
func (r keyRow) keyLabel() string {
	if r.label != "" {
		return r.label
	}
	return r.key
}

// answers reports whether the row's key, or one of its others, is k. A
// by-name row answers to nothing, the empty key included.
func (r keyRow) answers(k string) bool {
	if r.key == "" {
		return false
	}
	if r.key == k {
		return true
	}
	for _, a := range r.also {
		if a == k {
			return true
		}
	}
	return false
}

// outcome is what pressing the row's key does. Moving, going in, going back
// and quitting are outcomes of their own rather than actions to run, because
// what they do depends on the level, which the wiring holds, not the row.
func (r keyRow) outcome() keyOutcome {
	switch r.action {
	case actionMove:
		return keyOutcome{kind: outcomeMove}
	case actionGoIn:
		return keyOutcome{kind: outcomeGoIn}
	case actionGoBack:
		return keyOutcome{kind: outcomeGoBack}
	case actionQuit:
		return keyOutcome{kind: outcomeQuit}
	}
	return keyOutcome{kind: outcomeRun, action: r.action}
}

// blockedReason is the row's reason it cannot run under c, or "".
func (r keyRow) blockedReason(c leaderConds) string {
	if r.blocked == nil {
		return ""
	}
	return r.blocked(c)
}

// keyRowFor is the row whose key is k at level lv, if there is one. The
// table holds no two rows with one key at one level (TestKeyTable), so the
// first match is the only one.
func keyRowFor(lv level, k string) (keyRow, bool) {
	for _, r := range keyTable {
		if r.levels.has(lv) && r.answers(k) {
			return r, true
		}
	}
	return keyRow{}, false
}

// reasonAsLeader is a row's blocked reason taken from leaderBlockedReason,
// the handlers' own guards. Where a key works is the level's to decide (the
// row's levels), not the reason's, so nothing here gates on the screen.
func reasonAsLeader(a modeAction) func(leaderConds) string {
	return func(c leaderConds) string {
		return leaderBlockedReason(a, c)
	}
}

// reasonPauseToggle is `p`'s reason. At Project it is the leader's guard (an
// open project with a build). At All projects the key acts on the selected
// row, not on the active project, so the guard would wrongly dim it whenever
// the row is not the project that happens to be open; the handler words the
// row's own refusal (rowPauseRefusal) instead.
func reasonPauseToggle(c leaderConds) string {
	if c.allProjectsOpen {
		return ""
	}
	return leaderBlockedReason(actionPauseToggle, c)
}

// reasonAsProjectEdit is add and remove a project's reason. They are keys of
// the All projects level only, so the screen counts as open: the level is
// what says so now, and a dashboard started outside any project is at All
// projects without the screen's flag set.
func reasonAsProjectEdit(a modeAction) func(leaderConds) string {
	return func(c leaderConds) string {
		c.allProjectsOpen = true
		return projectEditBlockedReason(a, c)
	}
}

// detailsBlockedReason is `d`'s reason: there must be a session to show.
func detailsBlockedReason(c leaderConds) string {
	if !c.selected {
		return "no session selected"
	}
	return ""
}

// keyTable is every dashboard action, in the order `?` lists them: the keys
// that work everywhere, then All projects', then Project's, then the Agent's,
// then the actions reached by name only. It is docs/dash-design.md §5 row
// for row, less what later tasks build:
//
//   - `a` works at All projects only until T16.7 (A4).
//   - `p` works at Project only until T16.3 gives All projects a selected
//     project to pause (A4).
//   - `s` and `t` (the leader's S and T) need the project view's Build
//     section to have the keyboard (T16.4); their blocked reasons say so.
//   - tab, shift+tab and 1–3 change the project view's section (T16.4). The
//     details overlay `d` opens (T16.5) uses the same keys for its own tabs
//     (1–5) while it is up: its input mode outranks the level's, as an
//     overlay owns its keys.
//
// "start a new plan" has two rows for one action: `n` at All projects, as
// §5 binds it, and by name at Project and the Agent, where `n` is new
// session, so the `:` search still finds it from inside a project as the
// leader dialog does today. The two never meet at one level.
//
// It is filled in init rather than in its declaration because the rows'
// blocked reasons name keys (projectEditOpenScreenFirst, the task-status
// reason) through keyName, which reads this table: Go refuses that as an
// initialization cycle, though nothing reads the table until a key is
// pressed. init runs after every package-level variable is set, so the
// reasons find the table filled when they are first asked.
var keyTable []keyRow

func init() { keyTable = keyTableRows() }

// keyTableRows is keyTable's rows; see keyTable.
func keyTableRows() []keyRow {
	return []keyRow{
		{
			action: actionMove, key: "j", also: []string{"k", "up", "down", "pgup", "pgdown", "home", "end"},
			label:  "j k ↑ ↓ pgup pgdn home end",
			levels: atAllProjects | atProject,
			name:   "move", help: "Move the selection",
		},
		{
			// Not in the Agent: there is no level below it, and the leader then
			// enter closing the strip is what a person who pressed it meant.
			action: actionGoIn, key: "enter", levels: atAllProjects | atProject,
			name: "go in", help: "Go in a level, or open what is selected",
		},
		{
			// In the Agent this is the leader then esc: back to Project.
			action: actionGoBack, key: "esc", levels: atEveryLevel,
			name: "go back", help: "Go back a level, or close what is open",
		},
		{
			// In the Agent, the leader then `]` can land on another agent
			// with the keyboard kept on the agent (agentActionStays).
			action: actionNeedNext, key: "]", levels: atEveryLevel,
			name: "next needing you", help: "Go to the next thing that needs you, across every project",
		},
		{
			action: actionNeedPrev, key: "[", levels: atEveryLevel,
			name: "prev needing you", help: "Go to the previous thing that needs you, across every project",
		},
		{
			// The jump search (§4.6), also at All projects with no project
			// open. In the Agent the leader then `/` opens it over the
			// Project, as every overlay key does (agentActionStays).
			action: actionProjects, key: "/", levels: atEveryLevel,
			name: "jump", help: "Jump to a project, session or task",
			blocked: reasonAsLeader(actionProjects),
		},
		{
			action: actionActionsSearch, key: ":", levels: atEveryLevel,
			name: "actions", help: "Search every action by name, the ones with no key too",
		},
		{
			action: actionInbox, key: "i", levels: atEveryLevel,
			name: "inbox", help: "Read and answer the inbox",
			blocked: reasonAsLeader(actionInbox),
		},
		{
			action: actionDetails, key: "d", levels: atProject | atAgent,
			name: "details", help: "Show the selected session's details",
			blocked: detailsBlockedReason,
		},
		{
			action: actionRefreshUsage, key: "R", levels: atEveryLevel,
			name: "read usage", help: "Take a fresh usage reading for the header, as afk usage --refresh does",
			blocked: reasonAsLeader(actionRefreshUsage),
		},
		{
			action: actionKeys, key: "?", levels: atEveryLevel,
			name: "keys", help: "Show every key, level by level",
		},
		{
			// ctrl+c too, so the leader then ctrl+c still leaves a stuck
			// dashboard from the Agent, as it leaves the leader dialog today.
			// ctrl+c alone in the Agent goes to the agent (decideKey).
			action: actionQuit, key: "q", also: []string{quitKey}, levels: atEveryLevel,
			name: "quit", help: "Leave the dashboard; every session keeps running",
		},
		{
			action: actionPlanNew, key: "n", levels: atAllProjects,
			name: "start a new plan", help: "Plan a project that has no plan; asks for its folder or a new name",
			blocked: reasonAsLeader(actionPlanNew),
		},
		{
			action: actionProjectAdd, key: "+", levels: atAllProjects,
			name: "add a project", help: "Put a folder on the daemon's list, as afk project add does",
			blocked: reasonAsProjectEdit(actionProjectAdd),
		},
		{
			action: actionProjectRemove, key: "-", levels: atAllProjects,
			name: "remove the project", help: "Take the selected project off the daemon's list; its files stay",
			blocked: reasonAsProjectEdit(actionProjectRemove),
		},
		{
			// In the Agent this is the leader then `a`: the answer screen
			// opens over the Project, as `i` does (agentActionStays).
			action: actionAnswerOldest, key: "a", levels: atEveryLevel,
			name: "answer the oldest", help: "Answer the oldest inbox item that blocks a task (every project at All projects, this project elsewhere)",
		},
		{
			action: actionPauseToggle, key: "p", levels: atAllProjects | atProject,
			name: "pause or resume the build", help: "Hold the build so no new task starts, or let a paused one carry on",
			blocked: reasonPauseToggle,
		},
		{
			action: actionNewSession, key: "n", levels: atProject,
			name: "new session", help: "Start a new session from a plan or a branch",
			blocked: reasonAsLeader(actionNewSession),
		},
		{
			action: actionScratch, key: "o", levels: atProject,
			name: "one-off session", help: "Start a one-off session with no plan",
			blocked: reasonAsLeader(actionScratch),
		},
		{
			action: actionClean, key: "c", levels: atProject,
			name: "clean session", help: "Clean the selected session and its worktree",
			blocked: reasonAsLeader(actionClean),
		},
		{
			action: actionRestart, key: "r", levels: atProject,
			name: "restart agent", help: "Restart the selected session's stopped or stuck agent",
			blocked: reasonAsLeader(actionRestart),
		},
		{
			action: actionEditLabel, key: "l", levels: atProject,
			name: "edit label", help: "Give the selected session a display label",
			blocked: reasonAsLeader(actionEditLabel),
		},
		{
			action: actionSectionNext, key: "tab", levels: atProject,
			name: "next section", help: "Move to the next section: Sessions, Build, Inbox",
		},
		{
			action: actionSectionPrev, key: "shift+tab", levels: atProject,
			name: "previous section", help: "Move to the previous section",
		},
		{
			action: actionSectionSessions, key: "1", levels: atProject,
			name: "sessions section", help: "Move to the Sessions section",
		},
		{
			action: actionSectionBuild, key: "2", levels: atProject,
			name: "build section", help: "Move to the Build section, the plan's tasks",
		},
		{
			action: actionSectionInbox, key: "3", levels: atProject,
			name: "inbox section", help: "Move to the Inbox section, the open items",
		},
		{
			action: actionTaskStatus, key: "s", levels: atProject,
			name: "set this task's status", help: "Change the selected Build task's status, as afk ledger set does",
			blocked: reasonAsLeader(actionTaskStatus),
		},
		{
			action: actionTaskTokens, key: "t", levels: atProject,
			name: "show this task's tokens", help: "Show the selected Build task's details and the tokens it used, as afk tokens does",
			blocked: reasonAsLeader(actionTaskTokens),
		},
		{
			action: actionMouseToggle, key: "m", levels: atAgent,
			name: "mouse", help: "Let the terminal select text, or take the mouse back",
		},
		// The plan actions have no key, as in the leader dialog: the master
		// plan said "by name only". They act on the project, so they run at
		// Project and from the Agent (the leader then `:`), not at All projects,
		// where no one project is the one in view.
		{
			action: actionPlanExtend, levels: atProject | atAgent,
			name: "extend the plan", help: "Start a planning session that adds work to the plan",
			blocked: reasonAsLeader(actionPlanExtend),
		},
		{
			action: actionPlanAmend, levels: atProject | atAgent,
			name: "amend the plan", help: "Change the plan; asks what to change in a sentence",
			blocked: reasonAsLeader(actionPlanAmend),
		},
		{
			action: actionPlanFix, levels: atProject | atAgent,
			name: "fix a bug", help: "Find and fix a bug; asks what is broken",
			blocked: reasonAsLeader(actionPlanFix),
		},
		{
			action: actionPlanReconcile, levels: atProject | atAgent,
			name: "reconcile the plan with a document", help: "Fit the plan to a document; asks for its path",
			blocked: reasonAsLeader(actionPlanReconcile),
		},
		{
			action: actionPlanCheck, levels: atProject | atAgent,
			name: "check the plan", help: "Show the plan's errors and warnings, as afk plan lint does",
			blocked: reasonAsLeader(actionPlanCheck),
		},
		{
			action: actionPlanApprove, levels: atProject | atAgent,
			name: "approve the plan", help: "Approve a new plan and start its build, as afk plan approve does",
			blocked: reasonAsLeader(actionPlanApprove),
		},
		{
			action: actionPlanNew, levels: atProject | atAgent,
			name: "start a new plan", help: "Plan a project that has no plan; asks for its folder or a new name",
			blocked: reasonAsLeader(actionPlanNew),
		},
	}
}

// rowKeyAt is how row r's key is named at level lv: the key alone at All
// projects and Project, where keys work alone, and the leader then the key
// in the Agent, where every key but the leader goes to the agent (§5). A
// by-name row has no key to name and gives "".
func rowKeyAt(r keyRow, lv level, leader string) string {
	if r.key == "" {
		return ""
	}
	if lv == levelAgent {
		return leader + " " + r.keyLabel()
	}
	return r.keyLabel()
}

// keyName is how a hint or a notice names the key that runs action a, seen
// from level lv: "n" at Project, "ctrl+] m" in the Agent. It reads the key
// from keyTable, so a hint cannot name a key the dashboard does not answer
// to. An action with no key at lv is named by its key where it has one, with
// the leader when that is only in the Agent, which still tells the reader
// which key to look for once they are there. An action reached by name only
// gives "": the caller words that case itself.
//
// leader is a parameter, never a package value, because the owner can set
// it in config.toml (T9.2) and every caller passes the Model's.
func keyName(leader string, lv level, a modeAction) string {
	for _, r := range keyTable {
		if r.action == a && r.key != "" && r.levels.has(lv) {
			return rowKeyAt(r, lv, leader)
		}
	}
	for _, r := range keyTable {
		if r.action == a && r.key != "" {
			if r.levels == atAgent {
				return rowKeyAt(r, levelAgent, leader)
			}
			return r.keyLabel()
		}
	}
	return ""
}

// keyHelpBinding is a help entry naming action a's key as keyName does at
// level lv, with the given short description, for the footer and the full
// help. Its match key is the plain key: nothing dispatches on it (levelKey
// does, from keyTable), it only has to be non-empty because help.Model skips
// a binding that has no key. enabled false drops it from the line, on the
// rule that help never offers a key that would do nothing.
func keyHelpBinding(leader string, lv level, a modeAction, desc string, enabled bool) key.Binding {
	hint := keyName(leader, lv, a)
	match := hint
	if match == "" {
		match = desc
	}
	b := key.NewBinding(
		key.WithKeys(match),
		key.WithHelp(hint, desc),
	)
	b.SetEnabled(enabled)
	return b
}

// levelsReason is why a row cannot run at a level it is not listed at,
// naming where it does work, so a person who finds an action in the `:`
// search learns where to go for it rather than only that it is dimmed.
func levelsReason(s levelSet, leader string) string {
	var where []string
	if s.has(levelAllProjects) {
		where = append(where, "on All projects")
	}
	if s.has(levelProject) {
		where = append(where, "in a project's view")
	}
	if s.has(levelAgent) {
		where = append(where, "in the agent, after "+leader)
	}
	return "works " + strings.Join(where, " or ")
}

// searchSkips are the two rows the `:` search leaves out, because neither can
// be run from a list of actions: move is eight keys whose direction is the
// key itself, and `:` is the search the list is in.
func searchSkips(a modeAction) bool {
	return a == actionMove || a == actionActionsSearch
}

// actionRows builds the `:` search's rows for level lv: every action in the
// key table but searchSkips', once each, in the table's order. An action
// with a row at lv shows that row (so "start a new plan" is `n` at All
// projects and by name inside a project), its key as keyName names it there
// and its blocked reason under c. An action with no row at lv is still
// listed, dimmed with where it works: §4.7 searches every action, and a
// person looking for one should learn where it is rather than find nothing.
func actionRows(lv level, leader string, c leaderConds) []leaderRow {
	var rows []leaderRow
	seen := map[modeAction]bool{}
	for _, r := range keyTable {
		if searchSkips(r.action) || seen[r.action] {
			continue
		}
		seen[r.action] = true
		row := r
		if !row.levels.has(lv) {
			for _, alt := range keyTable {
				if alt.action == r.action && alt.levels.has(lv) {
					row = alt
					break
				}
			}
		}
		lr := leaderRow{action: row.action, name: row.name, help: row.help}
		if row.levels.has(lv) {
			lr.keyLabel = rowKeyAt(row, lv, leader)
			lr.blocked = row.blockedReason(c)
		} else {
			lr.keyLabel = row.keyLabel()
			lr.blocked = levelsReason(row.levels, leader)
		}
		rows = append(rows, lr)
	}
	return rows
}
