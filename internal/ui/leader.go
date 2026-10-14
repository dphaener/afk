// leader.go holds the actions dialog the leader key (ctrl+space) opens: every
// dashboard action with its key and one line of help, from the session list,
// the overview and the agent pane alike (T7.1).
//
// Like the `:` palette (palette.go) its rows are DERIVED from mode.go's
// dispatch table, so an action appears here because the dispatch can perform
// it. It differs from the palette in three ways, each for a reason:
//
//   - It merges inputNormal's and inputDetail's rules, because it is opened
//     from both sides of the screen and from the agent pane, and the owner
//     should see one list wherever they are, not a list that changes shape
//     with the focus.
//   - It shows actions that cannot run right now, dimmed with the reason,
//     where the palette hides them. A person who opens the dialog to find a
//     key learns the key and why it is not doing anything, which a missing
//     row cannot tell them.
//   - Its rows carry the modeAction itself, not a keypress to re-press. The
//     dialog is opened from the agent pane too, where re-pressing a key would
//     type it to the agent; the Model (W2.T1) runs the row's action through
//     applyKeyAction instead, the same function the key reaches.
//
// Nothing here touches the Model. The row builder, the blocked reasons and the
// key decision are pure functions of plain data, so every branch is a table
// row (leader_test.go); the Model fills leaderConds from its live state and
// applies the leaderOutcome it gets back.
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
// see it. Its arrival from the terminal is recorded in docs/decisions.md
// (T7.1); here it only has to equal what String() prints for
// {Code: KeySpace, Mod: ModCtrl}, which TestLeaderKeyDecision exercises.
const leaderKey = "ctrl+space"

// The dialog's own gestures, declared HELP-ONLY on the palette's precedent
// (palette.go): leaderKeyDecision stages them, and declaring them as parent
// rules in the registry would implement them twice. They exist so the footer
// says what the keys do while the dialog is up. leaderFilterKey is bound to
// backspace because a binding needs a key to be shown at all (help.Model
// skips a binding with none), and backspace is the one filter key that has a
// name; its label says what a person actually does, which is type.
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

// leaderRow is one action in the dialog.
type leaderRow struct {
	action modeAction
	// mode is the mode the action runs as: inputNormal, unless the action is
	// a rule only in inputDetail. applyKeyAction is called with it, so an
	// action runs exactly as its own key does in that mode.
	mode inputMode
	// name is what the row is called, and what the filter matches first.
	name string
	// keyLabel is the binding's help key ("enter/f", "esc/ctrl+\\").
	keyLabel string
	// keys are every key the binding answers to; keys[0] is the binding's
	// first key, the one a press is synthesized from when the action needs
	// the key itself (the filter's `/` is handed on to the list).
	keys []string
	// help is one line of plain language from leaderHelp.
	help string
	// blocked is why the action cannot run right now, or "" when it can.
	blocked string
}

// leaderExcluded is every action the dialog leaves out: movement and
// navigation, which are things you hold a key down for rather than look up,
// and the leader itself, which would open the dialog it is listed in. The
// list is of exclusions, not inclusions, so an action added to inputNormal or
// inputDetail later appears in the dialog without anyone touching this file —
// and fails TestLeaderRowsFromTheRealRegistry until its help line is written.
var leaderExcluded = []modeAction{
	actionCursorUp, actionCursorDown,
	actionPageUp, actionPageDown, actionListTop, actionListBottom,
	actionDetailScrollUp, actionDetailScrollDown,
	actionDetailHalfPageUp, actionDetailHalfPageDown,
	actionDetailPageUp, actionDetailPageDown,
	actionDetailGotoTop, actionDetailGotoBottom,
	actionDetailTabNext, actionDetailTabPrev, actionDetailTabJump,
	actionOpenLeader,
}

// leaderNames renames the few actions whose help description is too terse
// to read as a name in a list of every action ("label", "projects"). The
// footer and `?` keep the short words; they sit next to context the dialog
// does not have. An action with no entry here uses its binding's description.
var leaderNames = map[modeAction]string{
	actionClean:       "clean session",
	actionRestart:     "restart agent",
	actionEditLabel:   "edit label",
	actionScratch:     "one-off session",
	actionProjects:    "switch project",
	actionFilterStart: "filter sessions",
	actionPaneToggle:  "agent or overview",
}

// leaderHelp is the one line of help each listed action shows. Every action
// the dialog lists must have a line; the test that checks it is what makes a
// later action's missing line a failure rather than a blank row.
var leaderHelp = map[modeAction]string{
	actionPaneClose:     "Close the agent pane; the session keeps running",
	actionQuit:          "Leave the dashboard; every session keeps running",
	actionFocusPane:     "Move the keyboard to the right side",
	actionMouseToggle:   "Let the terminal select text, or take the mouse back",
	actionNewSession:    "Start a new session from a plan or a branch",
	actionScratch:       "Start a one-off session with no plan",
	actionProjects:      "Switch to another project",
	actionClean:         "Clean the selected session and its worktree",
	actionRestart:       "Restart the selected session's stopped or stuck agent",
	actionEditLabel:     "Give the selected session a display label",
	actionFilterStart:   "Filter the session list by name",
	actionFilterClear:   "Show every session again",
	actionPalette:       "Open the command palette",
	actionBuild:         "Show the build's tasks",
	actionInbox:         "Read and answer the inbox",
	actionPauseBuild:    "Hold the build so no new task starts",
	actionResumeBuild:   "Let a paused build carry on",
	actionEnterResize:   "Move the divider between the list and the right side",
	actionDetailUnfocus: "Hand the keyboard back to the session list",
	actionPaneToggle:    "Switch the right side between the agent and the overview",
}

// leaderRows builds the dialog's rows from the registry: one per action of
// inputNormal and inputDetail, in each mode's help order, inputNormal first.
//
// Help order, not rule order, for the palette's reason (paletteEntries):
// rules are ordered for match fidelity and read like nothing a person would
// scan. An action that is a rule in both modes (build, restart, quit and
// others) is listed once, from inputNormal, which is the mode a person
// thinks of it in; only an action that exists solely in inputDetail runs as
// inputDetail. The blocked reason is filled from conds here so the rows the
// dialog holds already say what cannot run.
func leaderRows(reg modeRegistry, conds leaderConds) []leaderRow {
	var rows []leaderRow
	seen := map[modeAction]bool{}
	for _, mode := range []inputMode{inputNormal, inputDetail} {
		spec := reg.spec(mode)
		position := func(b key.Binding) int {
			for i, kb := range spec.keys.bindings {
				if slices.Equal(kb.Keys(), b.Keys()) {
					return i
				}
			}
			return len(spec.keys.bindings)
		}
		rules := slices.Clone(spec.rules)
		slices.SortStableFunc(rules, func(a, b modeRule) int {
			return position(a.binding) - position(b.binding)
		})
		for _, rule := range rules {
			if seen[rule.action] || slices.Contains(leaderExcluded, rule.action) {
				continue
			}
			keys := rule.binding.Keys()
			if len(keys) == 0 {
				continue
			}
			seen[rule.action] = true
			h := rule.binding.Help()
			name := h.Desc
			if n, ok := leaderNames[rule.action]; ok {
				name = n
			}
			rows = append(rows, leaderRow{
				action:   rule.action,
				mode:     mode,
				name:     name,
				keyLabel: h.Key,
				keys:     slices.Clone(keys),
				help:     leaderHelp[rule.action],
				blocked:  leaderBlockedReason(rule.action, conds),
			})
		}
	}
	return rows
}

// leaderConds is the live state the blocked reasons are decided from, as
// plain booleans the Model fills when the dialog opens. Each one mirrors a
// guard an action's own handler applies, so the dialog dims exactly the rows
// whose key would do nothing (or only say why it did nothing).
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
	// restartLive is Model.restartKeyLive, the gate `?` and the palette use.
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
// openNewForm, focusRightSide, toggleView) and the rule guards needsPane and
// needsFilter. A reason here that a handler does not enforce would dim a row
// whose key works, so nothing is added that the handlers do not check — with
// two exceptions, both about where the keyboard is: agent/overview and back
// to list are overview keys, and from the list (where neither is a rule) the
// dialog must not let them reach across to the right side.
func leaderBlockedReason(a modeAction, c leaderConds) string {
	const noSession = "no session selected"
	switch a {
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
	case actionNewSession, actionScratch:
		if !c.hasActiveProject {
			return "no project is open"
		}
	case actionProjects:
		if !c.hasActiveProject {
			return "no projects to switch between"
		}
	case actionClean:
		switch {
		case !c.selected:
			return noSession
		case c.paneOpen:
			return "close the pane first (ctrl+\\)"
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
	// leaderPassThrough closes the dialog and types ctrl+space to the agent:
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
//  2. ctrl+space closes; from the agent pane it also passes ctrl+space on.
//  3. up and down move the highlight.
//  4. enter runs the highlighted row. enter and esc are checked before any
//     row's keys, so Focus (enter/f) and back-to-list (esc/ctrl+\) answer
//     only to their other key here.
//  5. backspace edits the query.
//  6. With nothing typed, a key that is a row's key runs that row. Among
//     rows sharing a key (ctrl+\ is both close-pane and back-to-list) the
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
// when it opened and never refreshes itself, for the palette's reason
// (newPalette): the discovery tick fires every two seconds, and rebuilding
// under the user's hands would move the highlight they are aiming with.
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
// the name, the help line and the key label. A substring rather than the
// palette's fuzzy match, because the dialog's list is short and a person
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
// highlight back on the first row, as the palette's SetFilterText does, so
// the highlight can never index past a list the filter just shortened.
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
		// Wraps at both ends, like the palette's list (InfiniteScrolling).
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
		// keys, as the palette's rows do. The cut above is measured on the
		// plain text, so styling the key cannot change where it falls.
		if lipgloss.Width(r.keyLabel) <= width && strings.HasPrefix(line, r.keyLabel) {
			return theme.helpStyles.ShortKey.Render(r.keyLabel) + strings.TrimPrefix(line, r.keyLabel)
		}
		return line
	}
}
