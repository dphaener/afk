// leader.go holds the leader key and the `:` actions search (§4.7, T16.2):
// every dashboard action with its name, its key if it has one and one line of
// help, the ones reached by name only included, at every level.
//
// The leader was Ctrl+Space until T9.1 and is ctrl+] since (docs/decisions.md
// "## T9.1"). ctrl+] is a byte of its own, 0x1D, that neither tmux's default
// prefix (ctrl+b) nor a common custom one (ctrl+a) takes. Ctrl+Space is not:
// a terminal sends Ctrl+Space, ctrl+@ and ctrl+2 all as the one NUL byte, and
// on macOS Raycast takes Ctrl+Space before the terminal sees it at all. Now
// Ctrl+Space is an ordinary key, which the agent pane forwards as NUL.
//
// From T7.2 to T16.2 the leader was the ONE way to run a dashboard action and
// opened a dialog whose rows ran on their first letter. Since T16.2 keys work
// alone at All projects and Project, and in the Agent the leader shows the key
// strip (keystrip.go) and the next key runs that action. The dialog became
// the `:` search: its rows come from the key table (actionRows, keytable.go),
// and letters always type into the query, so no word has to avoid another
// row's first letter.
//
// Two properties of the T7.1 dialog are kept, each for its reason:
//
//   - It shows actions that cannot run right now, dimmed with the reason. A
//     person who searches for an action learns its key and why it is not
//     doing anything, which a missing row cannot tell them.
//   - Its rows carry the modeAction itself, not a keypress to re-press. The
//     search is opened from the Agent too, where re-pressing a key would
//     type it to the agent; the Model runs the row's action through
//     runLevelAction instead, the function every level key reaches.
//
// Nothing here touches the Model. The blocked reasons and the key decision
// are pure functions of plain data, so every branch is a table row
// (leader_test.go); the Model fills leaderConds from its live state and
// applies the leaderOutcome it gets back.
package ui

import (
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// defaultLeaderKey is the default leader key's string as key.Matches and
// KeyPressMsg.String see it, used when config.toml's [dash] leader is unset
// or refused (checkLeader, leaderconfig.go). The Model holds the leader it
// actually uses (Model.leaderKey); nothing but checkLeader reads this
// constant outside tests, so every hint names the configured key. How it arrives from the terminal is recorded in docs/decisions.md
// (T9.1, "How ctrl+] arrives"): ultraviolet decodes the legacy byte 0x1D and
// the kitty-protocol CSI 93;5u alike to {Code: ']', Mod: ModCtrl}, whose
// String() is "ctrl+]", so this one string matches both. It is not
// Ctrl+Space because that key is the NUL byte ctrl+@ and ctrl+2 also send,
// and Raycast takes it on macOS (see the file comment). TestLeaderKeyDecision
// exercises the match.
const defaultLeaderKey = "ctrl+]"

// The search's own gestures, declared HELP-ONLY: leaderKeyDecision stages
// them, and declaring them as parent rules in the registry would implement
// them twice. They exist so the footer says what the keys do while the search
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
)

// leaderBinding is the leader's binding with leader as its key: keyMap.Leader,
// which levelKey matches the leader against (through decideKey) and the
// footers name.
func leaderBinding(leader string) key.Binding {
	return key.NewBinding(
		key.WithKeys(leader),
		key.WithHelp(leader, "afk keys"),
	)
}

// leaderRow is one action in the `:` search, built by actionRows
// (keytable.go) from the key table for the level the search was opened at.
type leaderRow struct {
	action modeAction
	// name is what the row is called, and what the query matches first.
	name string
	// keyLabel is the action's key as it is pressed at the opening level
	// ("n", or "ctrl+] m" in the Agent), or "" for an action reached by name
	// only.
	keyLabel string
	// help is the row's one line of plain language.
	help string
	// blocked is why the action cannot run right now, or "" when it can.
	blocked string
}

// leaderConds is the live state the blocked reasons are decided from, as
// plain booleans the Model fills when the search opens. Each one mirrors a
// guard an action's own handler applies, so the search dims exactly the rows
// that would do nothing (or only say why it did nothing).
type leaderConds struct {
	// leader is the Model's leader key name (Model.leaderKey.name). It is
	// here, with the facts, because a few reasons name a key and the leader
	// is configurable (T9.2).
	leader string
	// hasActiveProject is Model.hasActiveProject: the new-session form and
	// the one-off picker are inert without one.
	hasActiveProject bool
	// canJump is Model.canJump: the jump search has something to list (a
	// project open, a project in the list, or a cached home-screen row).
	canJump bool
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
	// allProjectsOpen is "the dashboard is at All projects" (currentLevel):
	// the screen open, or no project open at all. Add and remove a project
	// act on that level's rows (projectEditBlockedReason).
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
}

// leaderBlockedReason says why action a cannot run under c, in a few plain
// words, or "" when it can. The checks copy the handlers' own guards
// (cleanSelected, restartKeyLive, openLabelForm, pauseBuild, resumeBuild,
// openInbox, openJump, openNewForm). A reason here that a handler does not
// enforce would dim a row that works, so nothing is added that the handlers
// do not check. Where a key works is not decided here: the key table's levels
// say that (actionRows dims a row away from its levels with levelsReason).
//
// Add and remove a project (T7.10) are decided by projectEditBlockedReason,
// the reason their actions ask again when they run.
func leaderBlockedReason(a modeAction, c leaderConds) string {
	const noSession = "no session selected"
	switch a {
	case actionProjectAdd, actionProjectRemove:
		return projectEditBlockedReason(a, c)
	case actionNewSession, actionScratch, actionPlanExtend, actionPlanAmend, actionPlanReconcile, actionPlanFix:
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
		// Not "no project is open": the jump search works at All projects
		// before any project is open (A7). Only an empty dashboard, the
		// empty state, has nothing to list.
		if !c.canJump {
			return "there is nothing to jump to yet"
		}
	case actionClean:
		// An open pane does not dim clean since T16.2: the pane is detached
		// when the clean is confirmed (dispatchConfirmedClean), because no key
		// closes a pane on its own any more.
		switch {
		case !c.selected:
			return noSession
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
	case actionPauseToggle:
		if !c.hasActiveProject || !c.hasBuild {
			return "this project has no build"
		}
	case actionInbox:
		if !c.buildKnown {
			return "the inbox has not been read yet"
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

// leaderOutcomeKind is what one key in the search does.
type leaderOutcomeKind int

const (
	// leaderIgnore is the zero value: nothing happens and the search stays
	// open, so a caller that forgets to check does nothing rather than
	// something wrong.
	leaderIgnore leaderOutcomeKind = iota
	// leaderRun closes the search and runs leaderOutcome.row's action.
	leaderRun
	// leaderFilter edits the query: appends leaderOutcome.text, or drops the
	// last character when leaderOutcome.backspace is set.
	leaderFilter
	// leaderClose closes the search and does nothing else.
	leaderClose
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

// leaderKeyDecision decides what key k does in the `:` search. rows are the
// rows on screen (already filtered by query) and highlight indexes them.
//
// The rules run in a fixed order, and the order is the design:
//
//  1. esc closes, whatever is typed — one key out, always.
//  2. The leader closes too: it is how the search was reached from the
//     Agent, and pressing it again reads as "never mind".
//  3. up and down move the highlight.
//  4. enter runs the highlighted row.
//  5. backspace edits the query.
//  6. Any other printable key types into the query. Letters ALWAYS type
//     (§4.7): until T16.2 a letter that was a row's key ran that row from an
//     empty query, so every row had to be found by a word whose first letter
//     no other row used. Now every name can be typed from its first letter.
//
// Anything else is ignored. A run that lands on a dimmed row is ignored too:
// the search stays open and the row keeps saying why.
//
// leader is the Model's leader key name. It is compared before the switch
// rather than as a case because it is a value, not a constant; checkLeader
// refuses esc, up, down, enter and backspace as leaders (reserved, or with no
// modifier), so testing it first cannot shadow the search's own keys.
func leaderKeyDecision(k tea.KeyPressMsg, leader string, rows []leaderRow, highlight int) leaderOutcome {
	s := k.String()
	if s == leader {
		return leaderOutcome{kind: leaderClose}
	}
	switch s {
	case "esc":
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

// leaderDialog is the `:` search's state. It holds a snapshot of the rows
// taken when it opened and never refreshes itself: the discovery tick fires
// every two seconds, and rebuilding under the user's hands would move the
// highlight they are aiming with.
type leaderDialog struct {
	rows      []leaderRow
	query     string
	highlight int
	// leader is the Model's leader key (config.toml sets it), which closes
	// the search when pressed in it.
	leader string
}

func newLeaderDialog(rows []leaderRow, leader string) leaderDialog {
	return leaderDialog{rows: rows, leader: leader}
}

// visible is the rows the query keeps: a case-insensitive substring match on
// the name, the help line and the key label. A substring rather than a
// fuzzy match, because the list is short and a person typing a word expects
// rows containing that word, not rows that happen to contain its letters in
// order.
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

// decide is leaderKeyDecision against the search's own state.
func (d leaderDialog) decide(k tea.KeyPressMsg) leaderOutcome {
	return leaderKeyDecision(k, d.leader, d.visible(), d.highlight)
}

// apply carries out the two outcomes that change the search itself, filter
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

// leaderDialogWidth is the search's preferred content width: the widest
// name, key label and help line side by side with room to spare at 120
// columns. On an 80-column terminal modalContentDims narrows it and the help
// is cut.
const leaderDialogWidth = 96

// leaderHint is the bottom edge's key hint.
const leaderHint = "type to search · enter runs · esc closes"

// view renders the search as a modal box for a terminal of width×height —
// the box itself, ready for composeModal. The box is sized by
// modalContentDims, so it fits inside the terminal less the modal margin;
// every line is truncated to the content width, so nothing wraps past the
// border, and the rows scroll to keep the highlight on screen.
func (d leaderDialog) view(width, height int) string {
	rows := d.visible()
	// Query line, a blank line, then the rows.
	contentW, contentH := modalContentDims(width, height, leaderDialogWidth, max(len(d.rows), 1)+2)
	area := max(contentH-2, 1)

	query := theme.dim.Render("type an action's name")
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

// leaderRowLine is one row: the name, the key (blank for an action reached
// by name only), then the help — or, for a dimmed row, the reason it cannot
// run, which is the more useful of the two when both will not fit (§4.7).
// The whole line is cut to width before it is styled as selected, and padded
// so the highlight bar spans the box.
func leaderRowLine(r leaderRow, keyW, nameW, width int, selected bool) string {
	tail := r.help
	if r.blocked != "" {
		tail = r.blocked
	}
	name := padRight(r.name, nameW) + "  "
	plain := name + padRight(r.keyLabel, keyW) + "  — " + tail
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
		rest, ok := strings.CutPrefix(line, name)
		if r.keyLabel != "" && ok && strings.HasPrefix(rest, r.keyLabel) {
			return name + theme.helpStyles.ShortKey.Render(r.keyLabel) + strings.TrimPrefix(rest, r.keyLabel)
		}
		return line
	}
}
