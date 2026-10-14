// mode.go is the single mode/dispatch mechanism (built in W4.T1, wired in
// W4.T2): one derived input mode replacing the three parallel overlay-state
// mechanisms the dashboard used to run at once — a `uiMode` modal enum
// dispatched in Update ahead of the global keymap, a `formActive` flag checked
// partway down handleKey, and a `switcherActive` flag checked at the top of
// it — plus the pane-focus state all three composed with by hand-ordered
// if-statements.
//
// The design (recorded in full, with the rejected alternatives, in
// .afk/tui-enhancements/decisions.md under "W4.T1") is a DERIVED mode, not a
// stored one: the overlay-activity facts stay in the fields that already hold
// them, because those fields feed View and are mutated from non-key paths all
// over model.go (applyPaneAttached moves the focus, spawnFinishedMsg raises
// the spawn-error modal, switchProject closes the switcher). activeInputMode
// re-derives who owns the keyboard from a snapshot of those fields on every
// keypress, walking ONE ordered precedence table — so the frame and the
// keyboard cannot disagree, which is the property views.go used to maintain by
// hand-mirroring handleKey's if-order in three places. A stored mode field or a
// mode stack was rejected precisely because every one of those non-key mutation
// sites would have had to maintain it, and one missed update is a keyboard trap.
//
// Model.dispatchKey (model.go) is the one consumer: it snapshots, derives the
// mode, routes the key through that mode's declared rules, and applies the
// action that comes back — so moving a key between modes is a table edit here,
// never a new if-statement in a handler.
package ui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// inputMode identifies which surface owns the keyboard. Every overlay state the
// three retired mechanisms collectively represented is a case of this one enum,
// alongside the pane-focus state that used to compose with them by if-order
// alone. The zero value is inputNormal so a zero modeState derives the default
// mode rather than a modal.
//
// Routing keys by mode BEFORE the global keymap is the property Sentry analysis
// mandated the original modal enum for: the clean confirmation's "n" answer
// collides with the global New binding, so a key dispatched by binding first
// would open the creation form mid-confirmation.
//
// Enum order carries NO precedence — modePrecedence does. Keeping the two
// separate means adding a mode is an enum constant plus an explicit decision
// about where it ranks, rather than an accident of declaration order.
type inputMode int

const (
	// inputNormal: navigation and the global keymap are live.
	inputNormal inputMode = iota
	// inputConfirmClean is the clean confirmation. EVERY selected session
	// passes through it, terminal phases included, so the phase the user was
	// shown is always captured before anything is dispatched (see
	// cleanSelected).
	inputConfirmClean
	// inputSpawnError reports a background create that failed. Its output was
	// captured, not shown live (nothing was suspended), so this modal is the
	// only place the user can read why — dismissed with esc or enter.
	inputSpawnError
	// inputConfirmRestart is the wedged-child restart confirmation: only a
	// session the row already shows as WEDGED reaches it (see
	// restartWedgedSelected), and restarting kills a live process with no undo,
	// so it gets the same explicit y/n gate cleaning already has.
	inputConfirmRestart
	// inputEditLabel is the label-edit prompt (the `l` key): a single-field
	// bubbles/textinput, unlike the two confirmations above, so unclaimed keys
	// route to the component instead of being swallowed — there is text to
	// type, not just an answer to a yes/no question.
	inputEditLabel
	// inputPickProvider is the one-off provider picker (the `o` key): a short
	// list of every registered provider with Claude highlighted. It swallows
	// unclaimed keys like the y/n confirmations, because it has no text to
	// type — only a highlight to move, a choice to confirm, and esc. Nothing is
	// created until enter, which is what lets esc be a clean no-op.
	inputPickProvider
	// inputFilter is the session list's filter prompt (`/`): while it is open
	// the list component owns every unclaimed key, because there is a query to
	// type rather than an answer to give. It is derived from the component's
	// own state (list.Model.SettingFilter), not from a flag beside it.
	inputFilter
	// inputSwitcher is the project-switcher overlay (Model.switcherOpen).
	inputSwitcher
	// inputForm is the session-creation form (Model.formOpen).
	inputForm
	// inputPane is a focused pane: every key but the leader forwards to the
	// child.
	inputPane
	// inputDetail is a FOCUSED detail pane: the summary/diff view in the main
	// region owns the keyboard so its viewport can be scrolled with the keys a
	// user reaches for. It is a second, distinct focus concept from inputPane
	// — that one routes keys into a child session, this one moves a viewport
	// this process owns — and the two are mutually exclusive by construction
	// (see modeSnapshot's two conjunctions).
	inputDetail
	// inputResize is the pane-resize mode (Model.resizing), entered with
	// ctrl+space w: h/l and the arrows move the sidebar/main divider live,
	// ctrl+w commits and persists the split, esc restores the width at entry.
	// It swallows every key it does not bind, so a stray letter cannot open,
	// clean or steal a session mid-resize.
	inputResize
	// inputInbox is the inbox overlay (Model.inboxOpen): a list of the
	// project's inbox items and, one level in, an item with a field to answer
	// it. Every key is the component's, as in the switcher, and it has no
	// parent rule at all — see its spec in newModeRegistry for
	// why not even the quit chord.
	inputInbox
	// inputLeader is the actions dialog the leader key (ctrl+space) opens
	// (Model.leaderOpen, leader.go). Every key is the dialog's: an action's key
	// runs it, other letters filter, enter runs the highlighted row, esc
	// closes. It is opened from the list, the overview (and the agent
	// placeholder) and the agent pane, which is why its place in
	// modePrecedence is above all three.
	inputLeader
	// inputPlanText is the plan actions' one-line form (planstart.go): amend
	// asks for a sentence, reconcile for a document's path, and "start a new
	// plan" for a folder. It is the label
	// prompt's shape — a textinput, so unclaimed keys reach the component —
	// with a purpose (Model.planTextMode) instead of a session.
	inputPlanText
	// inputConfirmPlanOpen is the y/n offer raised when a plan action was
	// refused because a planning session is already open: y opens that
	// session. It swallows unclaimed keys like the other y/n confirmations.
	inputConfirmPlanOpen
	// inputConfirmPlanFolder is the y/n question the new-plan form raises
	// for a bare project name (T7.5): y creates the folder under the
	// profile's project location, makes it a git repository and then starts
	// planning there, or, when a folder of that name already exists, plans in
	// it and creates nothing. It swallows unclaimed keys like the other y/n
	// confirmations, because a folder is made only on an explicit y.
	inputConfirmPlanFolder
	// inputConfirmGitHubRepo is the y/n question asked after a new project's
	// folder is made (T7.6): y makes a private GitHub repository and sets it
	// as the folder's origin, n or esc makes nothing, and planning starts
	// after either. It swallows unclaimed keys, enter included, so the
	// default is no and a repository is made only on an explicit y.
	inputConfirmGitHubRepo
	// inputConfirmPlanApprove is the y/n question the "approve the plan"
	// action raises (T7.8, planapprove.go): it shows the project, the plan
	// and the checker's counts, and y approves, which commits and pushes. It
	// swallows unclaimed keys, enter included, so the default is no. When
	// the plan has errors it has no y at all (Model.modes drops it), because
	// `afk plan approve` would refuse and the dialog says to fix them first.
	inputConfirmPlanApprove
	// inputPlanCheck is the check-the-plan overlay (Model.planCheckOpen,
	// plancheckoverlay.go): the plan checker's counts and findings in a
	// scrollable box. It is an overlay like the inbox, not a footer modal:
	// it asks no question, so nothing about it needs to outrank a create's
	// failure report, and it coexists with the modals the same way the
	// inbox does.
	inputPlanCheck
	// inputAllProjects is the all-projects screen (Model.allProjectsOpen,
	// allprojects.go): one row per project, in the place of the session list
	// and the detail side. It is not a footer modal: it asks nothing, and a
	// create's failure report must still outrank it. Unlike the overlays it
	// keeps the leader, because pause and resume act on its selected row and
	// they are leader actions (T7.2).
	inputAllProjects
	// inputProjectAddFolder and inputProjectAddName are the two steps of the
	// all-projects screen's add form (T7.10, projectedit.go): the folder,
	// then an optional name. Two modes rather than one with a step field so
	// each step's enter carries its own help ("next", "add") and the dialog
	// hint, read off the keymap, says what enter does on that step. They are
	// the plan form's shape: a textinput, so unclaimed keys reach the field.
	inputProjectAddFolder
	inputProjectAddName
	// inputConfirmProjectRemove is the y/n question the all-projects screen's
	// remove raises (T7.10): y takes the selected project off config.toml's
	// list. It swallows unclaimed keys, enter included, so the default is no
	// and nothing is removed without an explicit y.
	inputConfirmProjectRemove
	// inputPickTaskStatus, inputTaskStatusReplacedBy and
	// inputConfirmTaskStatus are the Build tab's "set this task's status"
	// (T7.11, taskstatus.go): the status picker, the one-line step asking for
	// the replacing ids when the choice is superseded, and the y/n confirm.
	// The picker has the provider picker's shape and the confirm the remove
	// confirm's, so both swallow unclaimed keys; the ids step is a textinput,
	// so unclaimed keys reach the field. Three modes rather than one with a
	// step field so each step's keys and hint are its own.
	inputPickTaskStatus
	inputTaskStatusReplacedBy
	inputConfirmTaskStatus
	// inputTaskTokens is the tokens overlay (Model.taskTokensOpen,
	// tasktokensoverlay.go, T7.12): the Build tab cursor's task as `afk
	// tokens` prints it, in a scrollable box. It is the plan check's kind of
	// overlay, not a footer modal: it asks nothing, and a create's failure
	// report must still outrank it.
	inputTaskTokens

	// inputModeCount bounds the enum for exhaustive iteration (the registry
	// completeness tests).
	inputModeCount
)

// String exists for test-failure output; nothing renders it.
func (m inputMode) String() string {
	switch m {
	case inputNormal:
		return "normal"
	case inputConfirmClean:
		return "confirm-clean"
	case inputSpawnError:
		return "spawn-error"
	case inputConfirmRestart:
		return "confirm-restart"
	case inputEditLabel:
		return "edit-label"
	case inputPickProvider:
		return "pick-provider"
	case inputFilter:
		return "filter"
	case inputSwitcher:
		return "switcher"
	case inputForm:
		return "form"
	case inputPane:
		return "pane"
	case inputDetail:
		return "detail"
	case inputResize:
		return "resize"
	case inputInbox:
		return "inbox"
	case inputLeader:
		return "leader"
	case inputPlanText:
		return "plan-text"
	case inputConfirmPlanOpen:
		return "confirm-plan-open"
	case inputConfirmPlanFolder:
		return "confirm-plan-folder"
	case inputConfirmGitHubRepo:
		return "confirm-github-repo"
	case inputConfirmPlanApprove:
		return "confirm-plan-approve"
	case inputPlanCheck:
		return "plan-check"
	case inputAllProjects:
		return "all-projects"
	case inputProjectAddFolder:
		return "project-add-folder"
	case inputProjectAddName:
		return "project-add-name"
	case inputConfirmProjectRemove:
		return "confirm-project-remove"
	case inputPickTaskStatus:
		return "pick-task-status"
	case inputTaskStatusReplacedBy:
		return "task-status-replaced-by"
	case inputConfirmTaskStatus:
		return "confirm-task-status"
	case inputTaskTokens:
		return "task-tokens"
	}
	return "unknown"
}

// isModal reports whether the mode is one of the footer modals — the states the
// retired modal enum represented, and the only values meaningful in Model.modal
// and therefore in modeState.modal. It is the single definition of that subset:
// the exhaustive precedence test iterates it, so a modal added here is
// precedence-tested the moment its constant exists.
func (m inputMode) isModal() bool {
	switch m {
	case inputConfirmClean, inputSpawnError, inputConfirmRestart, inputEditLabel, inputPickProvider,
		inputPlanText, inputConfirmPlanOpen, inputConfirmPlanFolder, inputConfirmGitHubRepo,
		inputConfirmPlanApprove, inputProjectAddFolder, inputProjectAddName, inputConfirmProjectRemove,
		inputPickTaskStatus, inputTaskStatusReplacedBy, inputConfirmTaskStatus:
		return true
	}
	return false
}

// modeState is the snapshot of overlay-activity facts the active input mode is
// derived from. It is plain data — no Model, no components — so the whole
// precedence matrix is table-testable without a TTY, on the same pure-decision
// convention as shouldHandOffToDash (new.go) and routeArgs (main.go).
//
// The fields deliberately mirror where the facts LIVE today rather than
// pretending they are one value: modals are mutually exclusive (one enum
// field), but the switcher, the form, and a focused pane genuinely coexist
// with a modal and with each other, and that coexistence is behavior — a spawn
// error dismissed over an open switcher must return the keyboard to the
// switcher, and a cancelled form must resume a still-focused pane.
type modeState struct {
	// modal is the active footer modal, or inputNormal when none is up. Only
	// the isModal values (or inputNormal) are meaningful here.
	modal inputMode
	// switcherOpen mirrors Model.switcherOpen.
	switcherOpen bool
	// formOpen mirrors Model.formOpen.
	formOpen bool
	// listFiltering is Model.list.SettingFilter() — the session list's own
	// filter-editing state, read from the component rather than mirrored into a
	// flag beside it, so the two cannot drift.
	listFiltering bool
	// focus and view mirror Model.focus and Model.view verbatim: the two axes
	// of the focus model (see paneFocus and rightView in model.go). They are
	// copied raw, not masked, so the derivation below is the ONE place that
	// says which surface a right-focused keyboard lands on — a mask here and
	// a second rule in the precedence table would be two copies of it.
	focus paneFocus
	view  rightView
	// pane is Model.paneForSelection(): a live pane exists AND belongs to the
	// selected row (or there is no selection to disagree with it). It is
	// independent of view on purpose: an installed pane behind the overview
	// still reports true, and the precedence table is what decides that the
	// overview owns the keyboard there. A pane installed for a row the cursor
	// has left reports false, so the keyboard cannot route into a session
	// that is not the one on screen.
	pane bool
	// inboxOpen mirrors Model.inboxOpen.
	inboxOpen bool
	// planCheckOpen mirrors Model.planCheckOpen.
	planCheckOpen bool
	// taskTokensOpen mirrors Model.taskTokensOpen.
	taskTokensOpen bool
	// allProjectsOpen mirrors Model.allProjectsOpen.
	allProjectsOpen bool
	// leaderOpen mirrors Model.leaderOpen.
	leaderOpen bool
	// resizing mirrors Model.resizing. It needs no conjunction: resize mode
	// moves the divider between surfaces that are always drawn, whichever of
	// them happens to be in the main region.
	resizing bool
}

// modeSnapshot builds the modeState the dispatch derives from. This is the
// Model→mechanism bridge; keeping it beside the mechanism (rather than in
// model.go) keeps every reader of the raw flags the dispatch depends on in one
// reviewable file.
func (m Model) modeSnapshot() modeState {
	return modeState{
		modal:           m.modal,
		switcherOpen:    m.switcherOpen,
		formOpen:        m.formOpen,
		listFiltering:   m.list.SettingFilter(),
		focus:           m.focus,
		view:            m.view,
		pane:            m.paneForSelection(),
		inboxOpen:       m.inboxOpen,
		planCheckOpen:   m.planCheckOpen,
		taskTokensOpen:  m.taskTokensOpen,
		allProjectsOpen: m.allProjectsOpen,
		leaderOpen:      m.leaderOpen,
		resizing:        m.resizing,
	}
}

// paneOwnsKeyboard reports whether keystrokes route to the session pane
// absent any overlay: the right side is focused and the pane is the drawn
// surface. It is the Model-side spelling of the inputPane layer's predicate,
// for the frame (border accents, the pane title hint) and the handlers that
// need the answer without deriving a whole mode.
func (m Model) paneOwnsKeyboard() bool {
	return m.focus == focusRight && m.shouldRenderPane()
}

// detailOwnsKeyboard is paneOwnsKeyboard's complement on the right side: the
// right side is focused and the pane is NOT the drawn surface, so the
// overview's viewport is what the keys move. The two are exclusive by
// construction — one focus value, one drawn surface — which is what lets `f`
// mean "focus whatever is in the main region" with no ambiguity.
func (m Model) detailOwnsKeyboard() bool {
	return m.focus == focusRight && !m.shouldRenderPane()
}

// modes builds this Model's dispatch registry from its own keymap.
//
// It is rebuilt per call rather than cached in a field, and both halves of that
// are deliberate. Caching would put ~20 key.Bindings inside a struct Go copies
// by value on every message, to save a construction that costs a few
// allocations at human keypress rate. And a cached field would be EMPTY on a
// Model that never went through New — the zero-value Models this package's
// tests construct — which would silently turn every mode's fall-through into
// routeIgnored (the zero routeKind) and strand keys the form and the pane are
// supposed to receive. Building from m.keys leaves an unconstructed Model
// differing only in the global bindings it never had.
//
// It is newModeRegistry with one state-dependent edit: while the approve
// confirm holds a plan with errors, that confirm has no y. Removing the rule
// makes y a swallowed key like any stray letter, and removing the binding
// keeps it out of the footer and the dialog's hint, which both read this
// registry, so neither offers a key that would do nothing.
func (m Model) modes() modeRegistry {
	r := newModeRegistry(m.keys)
	if m.planApprove.Summary.Errors > 0 {
		r[inputConfirmPlanApprove] = withoutPlanApproveYes(r[inputConfirmPlanApprove])
	}
	return r
}

// modeLayer is one row of the precedence table: a mode and the predicate that
// says whether it is active in a given snapshot.
type modeLayer struct {
	mode   inputMode
	active func(modeState) bool
}

// modePrecedence IS the key-precedence property, as data: activeInputMode
// walks it top to bottom and the first active layer owns the keyboard. This
// replaces the hand-maintained ordering that lived in Update's mode switch
// plus handleKey's if-ladder (switcher → ctrl+t intercept → pane focus → form
// → ctrl+\ → the modeless switch); the two cross-cutting chord intercepts in
// that ladder are per-mode rules in the registry below, not layers here.
//
// Every row reproduces today's behavior except one, and each deviation-free
// row's reachability is documented in decisions.md:
//
//   - Rows 1-7 (the modals) outrank everything, including the switcher —
//     model.go:872-895 documents that in words, and it is reachable because
//     spawnFinishedMsg raises the modal asynchronously under an open overlay
//     or form. Their relative order is immaterial (one enum field holds them).
//   - The actions dialog (the leader key's) sits directly below the modals
//     and above every other overlay. Below the modals for the switcher's
//     reason: a create can fail underneath and its report must get the
//     keyboard. Above everything else because it is opened from the list, the
//     overview and a FOCUSED AGENT PANE, and its mode has to outrank the
//     pane's or the keys typed into the dialog would reach the agent behind
//     it. Nothing else is open when it appears (the leader is a rule only in
//     normal, detail and pane mode), so its rank against the other overlays
//     only matters for an attach that lands focused underneath — and the
//     dialog is what is on screen then.
//   - The switcher outranks the form (unreachable in practice, but views.go
//     orders the frame this way so frame and keys agree) and outranks a
//     focused pane (reachable: a focusOnArrival attach landing under the
//     overlay — "keys go to the overlay, because the overlay is what is on
//     screen").
//   - The inbox overlay sits beside the switcher, directly below it. It is
//     opened only through the actions dialog, so no other overlay is up
//     when it appears and its place among them is declared for the frame's
//     sake: views.go composes the overlays in this order. Below the modals for the switcher's reason (a
//     create can fail underneath and its report must get the keyboard), and
//     above the form, the filter prompt, resize mode and both right-side
//     focuses, so a pane that arrives focused under it cannot take keys
//     meant for the answer field.
//   - The check-the-plan overlay sits directly below the inbox, for the
//     inbox's reasons: it is opened only through the actions dialog, so no
//     other overlay is up when it appears, and it outranks the form, the
//     filter prompt, resize mode and both right-side focuses so a pane that
//     arrives focused under it cannot take the scroll keys.
//   - The tokens overlay (T7.12) sits directly below the check-the-plan
//     overlay, for the same reasons: it is opened only through the actions
//     dialog (or when a read it waited on lands, and then only while the
//     Build tab has the keyboard), so no other overlay is up when it appears.
//   - The all-projects screen sits directly below the tokens overlay. It is
//     opened only through the actions dialog, so no overlay is up when it
//     appears, and over it the dialog runs nothing that opens one;
//     the dialog itself outranks it, which is how pause and resume reach its
//     rows. It outranks the form, the filter prompt, resize mode and both
//     right-side focuses because it takes their place on screen: a pane that
//     arrives focused while it is open (a click on a notification for this
//     project) must not take keys meant for the rows.
//   - The form outranks a focused pane. THIS IS THE ONE DELIBERATE CHANGE:
//     today's if-order lets an invisible focused pane swallow every key while
//     the form is on screen (the known uncovered corner at model.go:1613-1620,
//     "deliberately left to whatever fixes the form/pane precedence
//     generally"). The defined answer follows the switcher's precedent and
//     views.go's stated invariant — whatever owns the keyboard must be ON
//     SCREEN — so no keystroke can land in a running session the user cannot
//     see, and Esc always cancels the form, after which the still-focused pane
//     resumes. It becomes live behavior only when W4.T2 wires the dispatch.
//   - A focused DETAIL pane sits directly above normal mode and below
//     everything else: it is the weakest claim on the keyboard there is, so
//     every modal, the dialog, the switcher, the form, the filter prompt and
//     a focused pane all outrank it. Its position relative to inputPane is
//     declared rather than relied on — the two predicates are exclusive by
//     construction (one focus value; the pane either is or is not the drawn
//     surface) — but a hand-ordered table shadows a misplaced row silently,
//     so the order is pinned by TestActiveInputModePrecedenceMatrix rather
//     than left implicit.
//   - The pane and detail layers are the two-axis focus model as predicates.
//     Right focus in agent view WITH a pane for the selected row is the pane;
//     right focus in anything else is the detail — the overview, or, in agent
//     view with no pane for a row that has an agent, the agent placeholder
//     (connecting, restarting, lost). The placeholder is not a separate layer:
//     Model.dispatchKey intercepts inputDetail when agentPlaceholderDrawn and
//     hands the key to placeholderKey, so it shares detail's place in this
//     table (weakest right-side claim) while answering only its own keys.
//     Either way the keyboard follows the drawn surface and never falls
//     through to normal mode, where typed keys would hit list bindings. List
//     focus is normal mode whatever the right side shows: with the list
//     focused nothing on the right owns the keyboard, however live the pane
//     is.
//   - Resize mode sits below every modal and overlay and the filter prompt,
//     so a spawn error raised mid-resize still gets the keyboard and the
//     resize resumes once it is dismissed. It sits ABOVE a focused pane: it
//     cannot be entered from one, so the only way the two coexist is a
//     focus-on-arrival attach landing mid-resize, and the explicit gesture the
//     user is in the middle of keeps the keyboard — h/l must move the divider,
//     not type into a session that just appeared. It sits above a focused
//     detail pane because it is entered FROM one and leaves the focus on the
//     right, so the detail focus resumes on exit.
var modePrecedence = []modeLayer{
	{inputConfirmClean, func(s modeState) bool { return s.modal == inputConfirmClean }},
	{inputSpawnError, func(s modeState) bool { return s.modal == inputSpawnError }},
	{inputConfirmRestart, func(s modeState) bool { return s.modal == inputConfirmRestart }},
	{inputEditLabel, func(s modeState) bool { return s.modal == inputEditLabel }},
	{inputPickProvider, func(s modeState) bool { return s.modal == inputPickProvider }},
	{inputPlanText, func(s modeState) bool { return s.modal == inputPlanText }},
	{inputConfirmPlanOpen, func(s modeState) bool { return s.modal == inputConfirmPlanOpen }},
	{inputConfirmPlanFolder, func(s modeState) bool { return s.modal == inputConfirmPlanFolder }},
	{inputConfirmGitHubRepo, func(s modeState) bool { return s.modal == inputConfirmGitHubRepo }},
	{inputConfirmPlanApprove, func(s modeState) bool { return s.modal == inputConfirmPlanApprove }},
	{inputProjectAddFolder, func(s modeState) bool { return s.modal == inputProjectAddFolder }},
	{inputProjectAddName, func(s modeState) bool { return s.modal == inputProjectAddName }},
	{inputConfirmProjectRemove, func(s modeState) bool { return s.modal == inputConfirmProjectRemove }},
	{inputPickTaskStatus, func(s modeState) bool { return s.modal == inputPickTaskStatus }},
	{inputTaskStatusReplacedBy, func(s modeState) bool { return s.modal == inputTaskStatusReplacedBy }},
	{inputConfirmTaskStatus, func(s modeState) bool { return s.modal == inputConfirmTaskStatus }},
	{inputLeader, func(s modeState) bool { return s.leaderOpen }},
	{inputSwitcher, func(s modeState) bool { return s.switcherOpen }},
	{inputInbox, func(s modeState) bool { return s.inboxOpen }},
	{inputPlanCheck, func(s modeState) bool { return s.planCheckOpen }},
	{inputTaskTokens, func(s modeState) bool { return s.taskTokensOpen }},
	{inputAllProjects, func(s modeState) bool { return s.allProjectsOpen }},
	{inputForm, func(s modeState) bool { return s.formOpen }},
	{inputFilter, func(s modeState) bool { return s.listFiltering }},
	{inputResize, func(s modeState) bool { return s.resizing }},
	{inputPane, func(s modeState) bool { return s.focus == focusRight && s.view == viewAgent && s.pane }},
	{inputDetail, func(s modeState) bool { return s.focus == focusRight }},
	{inputNormal, func(modeState) bool { return true }},
}

// activeInputMode derives which mode owns the keyboard: the first layer of
// modePrecedence whose predicate holds. The final layer is unconditionally
// active, so the fallback return is unreachable in practice and exists only so
// the function total-covers a hand-edited table.
func activeInputMode(s modeState) inputMode {
	for _, l := range modePrecedence {
		if l.active(s) {
			return l.mode
		}
	}
	return inputNormal
}

// modeAction names, symbolically, what a mode does with a key it claims.
// applyKeyAction (model.go) binds each one to its handler — actionConfirm in
// confirm-clean is confirmClean, actionPaneToggle is toggleView, and so
// on. Keeping the names symbolic here is what makes the routing testable
// without invoking any handler.
type modeAction int

const (
	// actionNone is the zero value: the key was not claimed by a rule.
	actionNone modeAction = iota

	// Cross-cutting actions.
	actionQuit        // requestQuit
	actionMouseToggle // toggleMouseCapture (ctrl+g in overlays; ctrl+space g)

	// Pane actions.
	actionPaneToggle // toggleView (ctrl+space t, right focus only)
	actionPaneClose  // an open pane from the list: ctrl+space \ closes (detaches)

	// actionDetailUnfocus is "back to list": esc on the focused overview, and
	// ctrl+space \ from anywhere on the right side. From the overview it is
	// releaseDetailFocus; from the agent pane and its placeholder the
	// dialog runs those surfaces' own releases instead (runLeaderRow),
	// because each clears different state and says different things.
	actionDetailUnfocus

	// Session-list and overview actions. Since T7.2 the list and the
	// overview keep rules only for moving about, enter and esc; every other
	// action here runs from the leader table (leader.go).
	actionCursorUp
	actionCursorDown
	actionFocusPane
	actionNewSession
	actionScratch
	actionProjects
	actionClean
	actionRestart
	actionEditLabel
	actionPageUp
	actionPageDown
	actionListTop
	actionListBottom
	actionDetailScrollUp
	actionDetailScrollDown
	actionDetailHalfPageUp
	actionDetailHalfPageDown
	// actionDetailPageUp/Down and actionDetailGotoTop/Bottom are the FOCUSED
	// detail pane's pgup/pgdn and home/end verbs (scrollDetail, model.go).
	// They are distinct from actionPageUp/actionPageDown/actionListTop/
	// actionListBottom above, which move the session list on the same
	// physical keys while unfocused — the cursor-vs-viewport split
	// TestDetailScrollKeysRouteToActions pins.
	actionDetailPageUp
	actionDetailPageDown
	actionDetailGotoTop
	actionDetailGotoBottom
	actionDetailTabNext
	actionDetailTabPrev
	actionDetailTabJump
	actionFilterStart
	actionFilterClear
	actionBuild       // ctrl+space b: showBuildTab
	actionInbox       // ctrl+space i: openInbox
	actionPauseBuild  // ctrl+space P: pauseBuild
	actionResumeBuild // ctrl+space U: resumeBuild
	actionEnterResize // ctrl+space w: enterResize
	// actionOpenLeader is ctrl+space, the leader key: it opens the actions
	// dialog (leader.go, openLeader in model.go). It is a rule in normal,
	// detail and pane mode and nowhere else, and it is not in the leader
	// table — the dialog must never list the key that opened it.
	actionOpenLeader
	// actionHelp shows or hides the full help on the session list
	// (toggleFullHelp): ctrl+space ? from anywhere, and esc on the list while
	// the full help is shown (guarded on routeConds.helpShown).
	actionHelp
	// The four plan actions (planstart.go). They have no key after the
	// leader: the dialog reaches them by name only (leaderTable).
	actionPlanExtend    // startPlanExtend
	actionPlanAmend     // openPlanTextForm(amend)
	actionPlanReconcile // openPlanTextForm(reconcile)
	actionPlanNew       // openPlanNewForm: start a new plan in a folder
	// actionPlanCheck is "check the plan" (T7.7): openPlanCheck shows the
	// plan checker's result from the last plan status read. By name only,
	// like the plan actions above.
	actionPlanCheck
	// actionPlanApprove is "approve the plan" (T7.8): openPlanApprove raises
	// the confirm (planapprove.go). By name only, like the plan actions.
	actionPlanApprove
	// actionAllProjects is "all projects" (T7.9, ctrl+space A): it opens the
	// all-projects screen, or closes it when it is open (allprojects.go).
	actionAllProjects
	// The all-projects screen's own keys: move (clamped), switch to the
	// selected project, close.
	actionAllProjectsUp
	actionAllProjectsDown
	actionAllProjectsPageUp
	actionAllProjectsPageDown
	actionAllProjectsTop
	actionAllProjectsBottom
	actionAllProjectsSwitch
	actionAllProjectsClose
	// actionProjectAdd and actionProjectRemove are the all-projects screen's
	// add and remove (T7.10, ctrl+space + and ctrl+space -): add opens the
	// two-step form, remove raises the y/n confirm for the selected row
	// (projectedit.go). They run only while the screen is open.
	actionProjectAdd
	actionProjectRemove
	// actionTaskStatus is "set this task's status" (T7.11, ctrl+space S): it
	// opens the status picker for the Build tab's cursor row
	// (taskstatus.go). It runs only on the Build tab with the right side
	// focused.
	actionTaskStatus
	// actionTaskTokens is "show this task's tokens" (T7.12, ctrl+space T):
	// it opens the tokens overlay for the Build tab's cursor row
	// (tasktokens.go). It runs only on the Build tab with the right side
	// focused.
	actionTaskTokens

	// Resize-mode actions. Exit commits (and persists) the split; cancel
	// restores the width resize mode was entered at. The four nudges move the
	// divider by resizeStep or resizeBigStep percentage points.
	actionExitResize
	actionCancelResize
	actionResizeLeft
	actionResizeRight
	actionResizeLeftBig
	actionResizeRightBig

	// Modal answers.
	actionConfirm      // y/Y in the two y/n confirmations; enter in the provider picker
	actionDeny         // n/N in the two y/n confirmations
	actionCancel       // esc in a modal (clean/restart/label/provider picker)
	actionDismiss      // esc/enter on the spawn-error report
	actionSubmit       // enter in the label prompt
	actionDeleteBranch // d/D in the clean confirmation: clean AND delete the branch
	actionPickerUp     // up/k in the provider and task-status pickers: highlight the previous row
	actionPickerDown   // down/j in the provider and task-status pickers: highlight the next row

	// Form gestures. Each is CONTEXTUAL inside its handler — actionFormCancel
	// is cancelForm, which clears an active base-list filter before it cancels
	// the form; actionFormSubmit is submitFormKey, which applies a live
	// filter, inserts a newline in the description, or submits, depending on
	// component state. The rule row says who owns the key; the handler says
	// what the gesture means right now. Same division of labor the switcher
	// already has (its two-stage Esc and R-except-while-Filtering live in
	// switcher.Update, so they are component fall-through here, not rules).
	actionFormCancel
	actionFormSubmit
	actionFormNextField
	actionFormPrevField
)

// String exists for test-failure output; nothing renders it.
func (a modeAction) String() string {
	switch a {
	case actionNone:
		return "none"
	case actionQuit:
		return "quit"
	case actionMouseToggle:
		return "mouse-toggle"
	case actionPaneToggle:
		return "pane-toggle"
	case actionPaneClose:
		return "pane-close"
	case actionDetailUnfocus:
		return "detail-unfocus"
	case actionCursorUp:
		return "cursor-up"
	case actionCursorDown:
		return "cursor-down"
	case actionFocusPane:
		return "focus-pane"
	case actionNewSession:
		return "new-session"
	case actionScratch:
		return "scratch"
	case actionProjects:
		return "projects"
	case actionClean:
		return "clean"
	case actionRestart:
		return "restart"
	case actionEditLabel:
		return "edit-label"
	case actionPageUp:
		return "page-up"
	case actionPageDown:
		return "page-down"
	case actionListTop:
		return "list-top"
	case actionListBottom:
		return "list-bottom"
	case actionDetailScrollUp:
		return "detail-scroll-up"
	case actionDetailScrollDown:
		return "detail-scroll-down"
	case actionDetailHalfPageUp:
		return "detail-half-page-up"
	case actionDetailHalfPageDown:
		return "detail-half-page-down"
	case actionDetailPageUp:
		return "detail-page-up"
	case actionDetailPageDown:
		return "detail-page-down"
	case actionDetailGotoTop:
		return "detail-goto-top"
	case actionDetailGotoBottom:
		return "detail-goto-bottom"
	case actionFilterStart:
		return "filter-start"
	case actionFilterClear:
		return "filter-clear"
	case actionBuild:
		return "build"
	case actionInbox:
		return "inbox"
	case actionPauseBuild:
		return "pause-build"
	case actionResumeBuild:
		return "resume-build"
	case actionEnterResize:
		return "enter-resize"
	case actionOpenLeader:
		return "open-leader"
	case actionHelp:
		return "help"
	case actionPlanExtend:
		return "plan-extend"
	case actionPlanAmend:
		return "plan-amend"
	case actionPlanReconcile:
		return "plan-reconcile"
	case actionPlanNew:
		return "plan-new"
	case actionPlanCheck:
		return "plan-check"
	case actionPlanApprove:
		return "plan-approve"
	case actionAllProjects:
		return "all-projects"
	case actionAllProjectsUp:
		return "all-projects-up"
	case actionAllProjectsDown:
		return "all-projects-down"
	case actionAllProjectsPageUp:
		return "all-projects-page-up"
	case actionAllProjectsPageDown:
		return "all-projects-page-down"
	case actionAllProjectsTop:
		return "all-projects-top"
	case actionAllProjectsBottom:
		return "all-projects-bottom"
	case actionAllProjectsSwitch:
		return "all-projects-switch"
	case actionAllProjectsClose:
		return "all-projects-close"
	case actionProjectAdd:
		return "project-add"
	case actionProjectRemove:
		return "project-remove"
	case actionTaskStatus:
		return "task-status"
	case actionTaskTokens:
		return "task-tokens"
	case actionExitResize:
		return "exit-resize"
	case actionCancelResize:
		return "cancel-resize"
	case actionResizeLeft:
		return "resize-left"
	case actionResizeRight:
		return "resize-right"
	case actionResizeLeftBig:
		return "resize-left-big"
	case actionResizeRightBig:
		return "resize-right-big"
	case actionConfirm:
		return "confirm"
	case actionDeny:
		return "deny"
	case actionCancel:
		return "cancel"
	case actionDismiss:
		return "dismiss"
	case actionSubmit:
		return "submit"
	case actionDeleteBranch:
		return "delete-branch"
	case actionPickerUp:
		return "picker-up"
	case actionPickerDown:
		return "picker-down"
	case actionFormCancel:
		return "form-cancel"
	case actionFormSubmit:
		return "form-submit"
	case actionFormNextField:
		return "form-next-field"
	case actionFormPrevField:
		return "form-prev-field"
	}
	return "unknown"
}

// routeKind is a mode's disposition for a key: claimed by one of its own
// rules, or falling through to wherever this mode sends unclaimed keys.
type routeKind int

const (
	// routeIgnored: the key means nothing here. The modals swallow unclaimed
	// keys so a stray keystroke cannot dismiss a confirmation, and normal mode
	// ignores unbound keys.
	routeIgnored routeKind = iota
	// routeAction: a rule claimed the key; keyDecision.action names it.
	routeAction
	// routeComponent: the mode's bubbles component gets the key (the form's
	// focused field, the switcher's list, the label prompt's textinput).
	routeComponent
	// routeChild: the key forwards to the focused pane's session.
	routeChild
)

// String exists for test-failure output; nothing renders it.
func (k routeKind) String() string {
	switch k {
	case routeIgnored:
		return "ignored"
	case routeAction:
		return "action"
	case routeComponent:
		return "component"
	case routeChild:
		return "child"
	}
	return "unknown"
}

// keyDecision is routeKey's answer: what happens to one key in one mode.
// action is meaningful only when kind is routeAction.
type keyDecision struct {
	kind   routeKind
	action modeAction
}

// routeConds are the ambient facts a routing rule may be guarded on, evaluated
// by the caller and passed as plain data so routing stays a pure function.
// Both guards are on normal mode's esc, which does one of two things or
// nothing.
type routeConds struct {
	// paneOpen is "an agent pane is installed". No rule is guarded on it
	// since T7.2 moved close-pane under the leader; the dialog's blocked
	// reason reads it (leaderConds.paneOpen).
	paneOpen bool
	// listFiltered is "the session list has a filter applied". Esc clears one,
	// and must fall through to ignored when there is nothing to clear: bound
	// unconditionally it would cancel a pending preview on every stray Esc.
	listFiltered bool
	// helpShown is "the full help is on screen" (Model.helpShowAll). Esc
	// closes it before it clears a filter: the help covers the footer, and
	// the gesture that put it there was ctrl+space ?, so esc is the one key
	// out that needs no lookup.
	helpShown bool
}

// modeRule is one row of a mode's routing table: a binding, the action it
// maps to, and an optional guard.
type modeRule struct {
	binding key.Binding
	action  modeAction
	// needsPane gates the rule on routeConds.paneOpen. The zero value
	// (ungated) is the common case.
	needsPane bool
	// needsFilter gates the rule on routeConds.listFiltered.
	needsFilter bool
	// needsHelp gates the rule on routeConds.helpShown.
	needsHelp bool
}

// modeKeyMap is one mode's action surface for Wave 6's help.Model: the
// bindings available in that mode, in a stable order, with the
// help text the current UI already renders (confirmCleanHint, labelFormHint,
// switcherHelp, formHelp) so reading actions off mode state never duplicates
// — or contradicts — what the footer says. It implements bubbles' help.KeyMap.
//
// ShortHelp returns the full ordered set and FullHelp one group: which subset
// each surface renders, and any per-state SetEnabled dimming, is presentation
// and belongs to Wave 6 — this type is the source of truth for WHAT is
// available, not for how much of it fits on a line.
type modeKeyMap struct {
	bindings []key.Binding
}

var _ help.KeyMap = modeKeyMap{}

func (km modeKeyMap) ShortHelp() []key.Binding  { return km.bindings }
func (km modeKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{km.bindings} }

// modeSpec is one mode's complete dispatch declaration: its routing rules (in
// match order — first match wins), its disposition for unclaimed keys, and its
// help keymap.
type modeSpec struct {
	mode inputMode
	// rules are consulted in order. Ordering only matters where key sets could
	// overlap; today every mode's rule keys are disjoint, and the order
	// preserves the old handlers' check order so the table diffs cleanly
	// against the code it replaces.
	rules []modeRule
	// fall is what happens to a key no rule claims.
	fall routeKind
	// keys is the mode's help surface. It is a superset of the rule
	// bindings for modes whose component owns part of the interaction (the
	// switcher's enter/esc/R live in switcher.Update but must still be
	// advertisable).
	keys modeKeyMap
}

// The detail pane's tab keys, bound only in detail mode: from the list they
// would re-tab the right pane, which list focus must never control (plan
// objective 2). One binding carries all six digits, so help lists "jump to
// tab" as one row. Six is the longest the strip gets: the five session tabs
// in a git-spice repo plus Build (visibleDetailTabs). A digit past the last
// visible tab does nothing (switchDetailTab).
var (
	detailTabNextKey = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next tab"),
	)
	detailTabPrevKey = key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "previous tab"),
	)
	detailTabJumpKey = key.NewBinding(
		key.WithKeys("1", "2", "3", "4", "5", "6"),
		key.WithHelp("1-6", "jump to tab"),
	)
)

// resizeToggleKey is ctrl+w, resize mode's commit chord, named by the keys
// resizeHints looks the commit binding up by. Since T7.2 it is no longer a
// rule outside resize mode: the way in is ctrl+space w (leader.go), and in a
// focused agent pane ctrl+w is the agent's (delete-word in a shell or an
// editor). Inside resize mode the commit is a separate binding with its own
// help text (see newModeRegistry), because the footer must say "save" there.
var resizeToggleKey = key.NewBinding(
	key.WithKeys("ctrl+w"),
	key.WithHelp("ctrl+w", "resize panes"),
)

// unfocusPaneKey is the agent view's "back to the list" as the placeholder's
// footer line names it (renderHelp, views.go). It is help-only: since T7.2
// no surface answers ctrl+\ — the agent pane types it to the agent, and back
// to the list is ctrl+space \ (leader.go) — so nothing matches it; it is
// declared once so the footer lines that name it read the same binding.
var unfocusPaneKey = key.NewBinding(
	key.WithKeys("ctrl+\\"),
	key.WithHelp("ctrl+\\", "return to dashboard"),
)

// The all-projects screen's keys (inputAllProjects). They are declared fresh
// rather than sharing the list's bindings for the reason detailFocusUp gives:
// the list's carry the session list's help text.
var (
	allProjectsUpKey = key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)
	allProjectsDownKey = key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)
	allProjectsPageUpKey = key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "page up"),
	)
	allProjectsPageDownKey = key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdn", "page down"),
	)
	allProjectsTopKey = key.NewBinding(
		key.WithKeys("home"),
		key.WithHelp("home", "first project"),
	)
	allProjectsBottomKey = key.NewBinding(
		key.WithKeys("end"),
		key.WithHelp("end", "last project"),
	)
	allProjectsSwitchKey = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "switch"),
	)
	allProjectsCloseKey = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close"),
	)
)

// modeRegistry holds every mode's spec, indexed by inputMode.
type modeRegistry [inputModeCount]modeSpec

func (r *modeRegistry) spec(m inputMode) modeSpec { return r[m] }

// route answers what one mode does with one key: the first rule (in declared
// order, honoring guards) whose binding matches claims it; otherwise the
// mode's fall-through disposition applies. Matching is key.Matches against
// KeyPressMsg.String(), which returns the key's Text when non-empty (so "y"
// and "Y" match their own bindings) and the modifier-qualified keystroke
// otherwise ("ctrl+g", "shift+tab") — verified against ultraviolet
// key.go:391-412, and identical in observable behavior to the hand-rolled
// Code/Text checks in the old handlers under the legacy decoding this program
// requests.
func (r *modeRegistry) route(mode inputMode, conds routeConds, k tea.KeyPressMsg) keyDecision {
	s := r.spec(mode)
	for _, rule := range s.rules {
		if rule.needsPane && !conds.paneOpen {
			continue
		}
		if rule.needsFilter && !conds.listFiltered {
			continue
		}
		if rule.needsHelp && !conds.helpShown {
			continue
		}
		if key.Matches(k, rule.binding) {
			return keyDecision{kind: routeAction, action: rule.action}
		}
	}
	return keyDecision{kind: s.fall}
}

// newModeRegistry builds the full dispatch table from the dashboard's global
// keymap. Global bindings are shared by reference from keys (so the registry
// and defaultKeyMap cannot name two different keys for one action); the
// mode-local bindings — modal answers, form gestures, the switcher's
// component-owned keys — are declared here, once, with help text mirroring
// the strings views.go renders today.
//
// The rule tables transcribe the old handlers; each mode's comment names its
// source. Since T7.2 the list, the overview and the agent pane carry no
// action rules: every action runs from the leader table (leader.go), the
// owner's choice recorded in docs/decisions.md "## T7.2". Two cross-cutting
// facts the tables encode, both verified against every handler (see
// decisions.md for the audit):
//
//   - ctrl+g (keys.Mouse) is a rule in every overlay, form, prompt, confirm
//     and in resize mode, except the switcher and the inbox overlay. The
//     switcher's omission is documented intent (model.go:2161-2165): the
//     overlay replaces the pane in the main region, so there is no session
//     output behind it worth selecting, and one Esc away the leader's `g`
//     works. In the inbox overlay the chord closes the overlay (see its spec
//     below). On the list, the overview and the agent pane it is ctrl+space g;
//     in the pane ctrl+g itself is the agent's.
//   - ctrl+c quits from the modals, the switcher, the list and the overview,
//     is deliberately component fall-through in the form (the name buffer is
//     worth protecting from a stray chord —
//     TestFormSwallowsQuitKeysEscIsOnlyExit), and forwards to the child in a
//     focused pane (the session owns SIGINT semantics).
func newModeRegistry(keys keyMap) modeRegistry {
	// quitChord is ctrl+c ALONE, not keys.Quit: the modals and the switcher
	// must let `q` through as an ordinary character (it answers nothing in a
	// y/n modal, filters in the switcher, and types into the label prompt),
	// so only normal mode routes through the two-key Quit binding.
	quitChord := key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	)
	// releaseDetail is the focused detail pane's way out. esc is safe to
	// bind unguarded in this mode for the reason it is NOT in normal mode:
	// inputNormal's filterClear owns esc there, and mode precedence — not rule
	// order — keeps an applied filter intact while the detail pane is focused.
	// It was esc/ctrl+\ before T7.2; ctrl+\ is ctrl+space \ now.
	releaseDetail := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back to list"),
	)
	// Modal answers. y/n carry per-modal help ("clean" vs "restart") so Wave 6
	// renders the same words the confirm bars use today.
	confirmClean := key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "clean"),
	)
	confirmRestart := key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "restart"),
	)
	// deleteBranch is the clean confirmation's d/D: the same clean plus the
	// branch. deleteUnmerged is the SAME physical key in the modal's second
	// state — `d` on a branch not known to be merged swaps the footer to the
	// unmerged confirmation, and only a second d deletes. The staging lives in
	// the handler (deleteBranchClean, model.go) behind one routing rule, on the
	// same division of labor as the switcher's two-stage Esc: the rule says who
	// owns the key, the handler says what the gesture means right now. The
	// second binding is help-only, so the unmerged footer's "delete anyway"
	// hint is keymap-derived like every other modal hint.
	deleteBranch := key.NewBinding(
		key.WithKeys("d", "D"),
		key.WithHelp("d", "+delete branch"),
	)
	deleteUnmerged := key.NewBinding(
		key.WithKeys("d", "D"),
		key.WithHelp("d", "delete anyway"),
	)
	deny := key.NewBinding(
		key.WithKeys("n", "N"),
		key.WithHelp("n", "cancel"),
	)
	cancelEsc := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	)
	dismiss := key.NewBinding(
		key.WithKeys("esc", "enter"),
		key.WithHelp("esc/enter", "dismiss"),
	)
	saveLabel := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "save (blank clears it)"),
	)
	// The plan form's enter and the open-it offer's y. Their own bindings
	// rather than saveLabel and confirmRestart, so the dialog hints say what
	// these keys do here.
	startPlanText := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "start"),
	)
	confirmPlanOpen := key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "open it"),
	)
	// The new-plan folder question's y. Its help is a plain "yes" because one
	// binding answers both of that dialog's wordings (create the folder, or
	// plan in the one that exists), and the dialog's own lines say which.
	confirmPlanFolder := key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "yes"),
	)
	// The GitHub question's keys. n and esc get their own bindings rather
	// than deny and cancelEsc, whose help says "cancel": here they are the
	// answer no, and planning still starts after them, so the hint must not
	// suggest the whole new project is being called off.
	confirmGitHubRepo := key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "yes"),
	)
	denyGitHubRepo := key.NewBinding(
		key.WithKeys("n", "N"),
		key.WithHelp("n", "no"),
	)
	escGitHubRepo := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "no"),
	)
	// The approve confirm's y says what it does, since it commits and
	// pushes. Its n and esc are the plain cancel bindings: nothing happens
	// after them, unlike the GitHub question's no.
	confirmPlanApprove := key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "approve"),
	)
	// The add form's enters and the remove confirm's y (T7.10). One enter per
	// step, so the hint says "next" on the folder and "add" on the name; the
	// y says "remove" because that is what it does to the list.
	nextProjectAdd := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "next"),
	)
	submitProjectAdd := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "add"),
	)
	confirmProjectRemove := key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "remove"),
	)
	// The task-status steps' keys (T7.11): the picker's enter chooses, the
	// ids step's enter goes on to the confirm, and the confirm's y says
	// "set" because that is what it does to the ledger.
	chooseTaskStatus := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "choose"),
	)
	nextTaskStatus := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "next"),
	)
	confirmTaskStatus := key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "set"),
	)
	// The provider picker's keys. up/k and down/j are declared fresh rather
	// than sharing keys.Up/keys.Down for the reason detailFocusUp gives: those
	// carry normal mode's session-list help text, which would label a picker
	// move with the wrong words in the dialog hint and the help row.
	pickerUp := key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)
	pickerDown := key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)
	pickerStart := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "start"),
	)
	// Form gestures, help text from formHelp (views.go).
	formCancel := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel filter, then form"),
	)
	formSubmit := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "submit (newline in description)"),
	)
	// shift+tab and tab are DISTINCT binding strings under v2
	// (Keystroke prints "shift+tab"), so unlike the old isShiftTab-before-tab
	// check the old handleFormKey ran there is no ordering hazard between these two —
	// but prev is declared first anyway so the table reads like the code it
	// replaces.
	formPrev := key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "previous field"),
	)
	formNext := key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "cycle"),
	)
	// Switcher component-owned keys: HELP-ONLY, deliberately not rules. The
	// two-stage Esc (clears an active filter first; dismisses only when
	// FilterState() is Unfiltered) and the R-is-rescan-except-while-Filtering
	// rule are staged inside switcher.Update (switcher.go:130-143), and
	// declaring them as parent rules would implement them twice — the exact
	// drift this mechanism exists to eliminate. Help text mirrors
	// switcherHelp (views.go).
	switcherChoose := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "switch"),
	)
	switcherMove := key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "move"),
	)
	switcherFilter := key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter"),
	)
	// switcherRescanKey (switcher.go) is shared by reference, not redeclared
	// here: it is the same value Update matches "R" against, so the help
	// surface and the live chord cannot drift apart.
	switcherClose := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close"),
	)
	// Session-list paging and jumps. Declared here rather than in keyMap because
	// nothing outside this table names them (renderHelp's global line is at its
	// measured-width cap — see keys.Project's comment) and because their keys had
	// to be chosen AGAINST the dashboard's existing single-letter bindings:
	// list.Model's own defaults for these gestures are h/l/f/d/b/u/g/G, and
	// l, f and o are already label, focus-pane and one-off. Only the named keys
	// below are bound, so no collision is possible.
	pageUp := key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "page up"),
	)
	pageDown := key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdn", "page down"),
	)
	listTop := key.NewBinding(
		key.WithKeys("home"),
		key.WithHelp("home", "first session"),
	)
	listBottom := key.NewBinding(
		key.WithKeys("end"),
		key.WithHelp("end", "last session"),
	)
	// Half-page keeps bubbles' viewport.DefaultKeyMap chords verbatim.
	detailHalfPageUp := key.NewBinding(
		key.WithKeys("ctrl+u"),
		key.WithHelp("ctrl+u", "detail half page up"),
	)
	detailHalfPageDown := key.NewBinding(
		key.WithKeys("ctrl+d"),
		key.WithHelp("ctrl+d", "detail half page down"),
	)
	// Focused-detail-pane scroll keys. up/down/j/k, pgup/pgdn and home/end
	// are free in detail mode — the session-list cursor is not bound there —
	// so they get viewport.DefaultKeyMap's own bindings verbatim. They are
	// declared fresh rather than sharing keys.Up/keys.Down/pageUp/pageDown/
	// listTop/listBottom because those carry the list's help text ("page up"
	// for the session list), which would label a scroll gesture wrongly.
	detailFocusUp := key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "scroll up"),
	)
	detailFocusDown := key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "scroll down"),
	)
	detailPageUp := key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "page up"),
	)
	detailPageDown := key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdn", "page down"),
	)
	detailGotoTop := key.NewBinding(
		key.WithKeys("home"),
		key.WithHelp("home", "top"),
	)
	detailGotoBottom := key.NewBinding(
		key.WithKeys("end"),
		key.WithHelp("end", "bottom"),
	)
	// Esc clears an applied session filter, and is GATED on there being one
	// (needsFilter): unbound Esc means nothing in normal mode today, and an
	// ungated rule here would bump previewGen — cancelling a pending preview —
	// on every stray press.
	filterClear := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "clear filter"),
	)
	// The filter prompt's own keys are the COMPONENT's, declared help-only for
	// the same reason the switcher's are: bubbles stages them inside
	// list.Model.Update (esc cancels, enter/tab accept), and re-declaring them as
	// rules here would implement them twice. The key set mirrors
	// list.DefaultKeyMap().AcceptWhileFiltering verbatim (decisions.md's
	// list-owned-key table) rather than "enter" alone: tab, shift+tab, the two
	// ctrl move keys, and up/down all accept the filter too, and the help label
	// stays "enter" on the same shorthand bubbles' own default uses for this
	// binding.
	filterAccept := key.NewBinding(
		key.WithKeys("enter", "tab", "shift+tab", "ctrl+k", "up", "ctrl+j", "down"),
		key.WithHelp("enter", "apply filter"),
	)
	filterCancel := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel filter"),
	)
	// Resize-mode keys (inputResize only). h/l and the arrows are free here
	// for the reason the focused detail pane's scroll keys are: resize mode
	// binds nothing else, so there is no list cursor or label key to collide
	// with. Shift moves five points; H and L are the vim-style spelling of the
	// same thing, since many terminals send shift+h as plain "H".
	resizeCommit := key.NewBinding(
		key.WithKeys("ctrl+w"),
		key.WithHelp("ctrl+w", "save split"),
	)
	resizeCancel := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	)
	resizeLeft := key.NewBinding(
		key.WithKeys("h", "left"),
		key.WithHelp("h/←", "narrow list"),
	)
	resizeRight := key.NewBinding(
		key.WithKeys("l", "right"),
		key.WithHelp("l/→", "widen list"),
	)
	resizeLeftBig := key.NewBinding(
		key.WithKeys("H", "shift+left"),
		key.WithHelp("H/shift+←", "narrow list ×5"),
	)
	resizeRightBig := key.NewBinding(
		key.WithKeys("L", "shift+right"),
		key.WithHelp("L/shift+→", "widen list ×5"),
	)

	// helpClose is esc closing the full help on the list (docs/decisions.md
	// T7.2, A5). It is a rule but not in the keymap below: the full help's
	// own line says how it closes (renderNormalHelpKeyMap appends that), and
	// a second esc row beside clear-filter would read as a duplicate.
	helpClose := key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close help"),
	)

	var r modeRegistry

	// Normal mode is list focus. Since T7.2 it keeps only the keys that move
	// around (the owner's choice, PLAN T7.2): the cursor, paging, enter to
	// focus the right side, esc to close the full help or clear a filter,
	// ctrl+c to quit, and the leader, through which every other action runs
	// (leader.go). Every other key falls through to routeIgnored, and
	// dispatchKey turns a removed letter into a notice naming its new key
	// (removedKeyNotice) — after routing, so no rule here could be shadowed
	// by it.
	//
	// The overview's scroll and tab keys are deliberately absent: with the
	// list focused the user's model is that nothing on the right is under
	// control — "When on the left pane, you can't control anything in the
	// right pane" (plan Context).
	//
	// Esc closes the full help before it clears a filter: the help is the
	// thing on top, and both are guarded, so with neither shown esc is
	// ignored and cancels no pending preview.
	r[inputNormal] = modeSpec{
		mode: inputNormal,
		rules: []modeRule{
			{binding: quitChord, action: actionQuit},
			{binding: keys.Up, action: actionCursorUp},
			{binding: keys.Down, action: actionCursorDown},
			{binding: keys.Focus, action: actionFocusPane},
			{binding: helpClose, action: actionHelp, needsHelp: true},
			{binding: filterClear, action: actionFilterClear, needsFilter: true},
			{binding: pageUp, action: actionPageUp},
			{binding: pageDown, action: actionPageDown},
			{binding: listTop, action: actionListTop},
			{binding: listBottom, action: actionListBottom},
			{binding: keys.Leader, action: actionOpenLeader},
		},
		fall: routeIgnored,
		// Help order is independent of rule order: Focus, the one way to the
		// right side, follows movement immediately.
		keys: modeKeyMap{bindings: []key.Binding{
			keys.Up, keys.Down, keys.Focus,
			pageUp, pageDown, listTop, listBottom,
			filterClear, keys.Leader, quitChord,
		}},
	}

	// Filter mode is the session list's `/` prompt. Only the two chords the
	// dashboard owns everywhere are rules; the query text and the accept/cancel
	// gestures are the component's (see filterAccept's comment). ctrl+c quits,
	// following the switcher's filter rather than the creation form's — a
	// throwaway query is not the name buffer the form protects.
	r[inputFilter] = modeSpec{
		mode: inputFilter,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			filterAccept, filterCancel, keys.Mouse, quitChord,
		}},
	}

	// Pane mode is right focus in agent view with the selected row's pane
	// installed. Since T7.2 exactly one key is the dashboard's: the leader
	// (ctrl+space). Everything else forwards to the child — esc, q, tab,
	// ctrl+c (the session owns SIGINT), and the chords the dashboard used to
	// keep here, ctrl+t, ctrl+\ and ctrl+g, which are ctrl+space t, \ and g
	// now. Adding a rule here takes a key away from every agent the
	// dashboard hosts, which is why the plan pins this set.
	//
	// The agent does not lose the leader either: pressed twice, the dialog
	// closes and sends ctrl+space on (leaderComponentKey, model.go).
	r[inputPane] = modeSpec{
		mode: inputPane,
		rules: []modeRule{
			{binding: keys.Leader, action: actionOpenLeader},
		},
		fall: routeChild,
		keys: modeKeyMap{bindings: []key.Binding{keys.Leader}},
	}

	// Detail mode is a focused detail pane. Its key set is ENUMERATED rather
	// than inherited: the fall-through is routeIgnored, so everything this
	// table omits — enter and every letter — is swallowed, which is the
	// point. A scroll gesture must never open, clean or steal a session, and
	// "ignored" is the only disposition that guarantees it for keys nobody
	// has thought of yet.
	//
	// What survives, and why: esc, the release (or the state is a trap);
	// ctrl+c, so the dashboard can always be left (A3); the three tab keys,
	// because switching tabs is reading — the same gesture as scrolling; the
	// ten scroll keys: up/k, down/j, ctrl+u, ctrl+d, pgup, pgdown, home, end;
	// and the leader, through which the overview's actions run since T7.2
	// (agent or overview, back to list, restart, build tasks, inbox, pause,
	// resume, resize, the mouse and quit — leader.go). These scroll and tab
	// keys are bound in this mode only: from the list they would drive the
	// right pane.
	//
	// The bindings are ordered for TRUNCATION SURVIVAL, not for rule fidelity:
	// renderHelp's default branch renders this whole keymap through one hard
	// truncate (views.go), so position here is the only thing deciding what a
	// narrow terminal still shows. Release first, always; the scroll keys —
	// the least likely to need rescuing from a stray keystroke, and the most
	// numerous — sort last.
	r[inputDetail] = modeSpec{
		mode: inputDetail,
		rules: []modeRule{
			{binding: releaseDetail, action: actionDetailUnfocus},
			{binding: quitChord, action: actionQuit},
			{binding: detailTabNextKey, action: actionDetailTabNext},
			{binding: detailTabPrevKey, action: actionDetailTabPrev},
			{binding: detailTabJumpKey, action: actionDetailTabJump},
			{binding: detailFocusUp, action: actionDetailScrollUp},
			{binding: detailFocusDown, action: actionDetailScrollDown},
			{binding: detailHalfPageUp, action: actionDetailHalfPageUp},
			{binding: detailHalfPageDown, action: actionDetailHalfPageDown},
			{binding: detailPageUp, action: actionDetailPageUp},
			{binding: detailPageDown, action: actionDetailPageDown},
			{binding: detailGotoTop, action: actionDetailGotoTop},
			{binding: detailGotoBottom, action: actionDetailGotoBottom},
			{binding: keys.Leader, action: actionOpenLeader},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			releaseDetail, keys.Leader, quitChord,
			detailTabNextKey, detailTabPrevKey, detailTabJumpKey,
			detailFocusUp, detailFocusDown, detailHalfPageUp, detailHalfPageDown,
			detailPageUp, detailPageDown, detailGotoTop, detailGotoBottom,
		}},
	}

	// Resize mode moves the sidebar/main divider. Like detail mode its key set
	// is ENUMERATED with an ignored fall-through: everything else is swallowed,
	// so no letter can open, clean or steal a session while the user is
	// tapping h/l. q/ctrl+c quit and ctrl+g toggles the mouse; T7.2 left
	// every key here as it was (A4). The leader is deliberately absent:
	// switching the right side or opening anything mid-resize changes what
	// the user is sizing, and one ctrl+w away it works.
	//
	// The bindings are ordered for truncation survival, as detail mode's are:
	// renderHelp's default branch hard-truncates this keymap at the terminal
	// width, so the commit and cancel keys lead and stay legible at 80
	// columns.
	r[inputResize] = modeSpec{
		mode: inputResize,
		rules: []modeRule{
			{binding: resizeCommit, action: actionExitResize},
			{binding: resizeCancel, action: actionCancelResize},
			{binding: resizeLeft, action: actionResizeLeft},
			{binding: resizeRight, action: actionResizeRight},
			{binding: resizeLeftBig, action: actionResizeLeftBig},
			{binding: resizeRightBig, action: actionResizeRightBig},
			{binding: keys.Quit, action: actionQuit},
			{binding: keys.Mouse, action: actionMouseToggle},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			resizeCommit, resizeCancel, resizeLeft, resizeRight,
			resizeLeftBig, resizeRightBig, keys.Quit, keys.Mouse,
		}},
	}

	// Form mode transcribes the old handleFormKey's top-level table (its
	// base-filter submachine stays inside the handlers, behind formBaseFiltering
	// — see the modeAction doc). ctrl+t is deliberately ABSENT: bubbles'
	// textarea binds it to TransposeCharacterBackward, so the rule not
	// existing in this mode is what keeps the chord the textarea's.
	r[inputForm] = modeSpec{
		mode: inputForm,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: formCancel, action: actionFormCancel},
			{binding: formPrev, action: actionFormPrevField},
			{binding: formNext, action: actionFormNextField},
			{binding: formSubmit, action: actionFormSubmit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			formNext, formPrev, formSubmit, switcherFilter, formCancel, keys.Mouse,
		}},
	}

	// Leader mode is the actions dialog (leader.go). It has NO parent rule:
	// every key is the dialog's, decided by leaderKeyDecision, because every
	// key worth asking about is either one of the dialog's own gestures or
	// the key of an action it lists — q and ctrl+c run the quit row, g the
	// mouse row. A parent rule would answer a key before the dialog could. The bindings are help-only, for the footer while it is up.
	r[inputLeader] = modeSpec{
		mode: inputLeader,
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			leaderRunKey, leaderMoveKey, leaderFilterKey, leaderCloseKey, leaderAgainKey,
		}},
	}

	// Switcher mode transcribes the old handleSwitcherKey: ctrl+c is the only parent
	// intercept; everything else — enter/esc/R/filter included — is the
	// component's. ctrl+g is deliberately absent (see newModeRegistry's doc).
	r[inputSwitcher] = modeSpec{
		mode: inputSwitcher,
		rules: []modeRule{
			{binding: quitChord, action: actionQuit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			switcherChoose, switcherMove, switcherFilter, switcherRescanKey,
			switcherClose, quitChord,
		}},
	}

	// Inbox mode has NO parent rule: every key is the component's, staged in
	// inboxOverlay.Update (inboxoverlay.go). The switcher keeps one, the ctrl+c
	// quit, because what they hold is a throwaway query. This
	// overlay holds an answer being written, on a screen where every letter is
	// text, so nothing quits the dashboard from inside it; esc and ctrl+g are
	// the ways out. ctrl+g closes the overlay here and is not the mouse chord,
	// for the switcher's documented reason: the box covers what there was to
	// select, and one press away the chord works again. The keymap lists both
	// screens' keys; the footer shows the ones for the screen that is up
	// (renderHelp).
	r[inputInbox] = modeSpec{
		mode: inputInbox,
		fall: routeComponent,
		keys: modeKeyMap{bindings: inboxModeKeys()},
	}

	// Plan-check mode is the switcher's shape: ctrl+c quits, as it does over
	// the switcher, because the overlay holds nothing typed that quitting
	// could lose; every other key is the overlay's, staged in
	// planCheckOverlay.Update, so the scroll keys and esc are implemented
	// once. ctrl+g is absent for the switcher's documented reason.
	r[inputPlanCheck] = modeSpec{
		mode: inputPlanCheck,
		rules: []modeRule{
			{binding: quitChord, action: actionQuit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: append(planCheckKeys(), quitChord)},
	}

	// Task-tokens mode is the plan check's shape for the plan check's
	// reasons: ctrl+c quits, since the overlay holds nothing typed, and every
	// other key is the overlay's, staged in taskTokensOverlay.Update.
	r[inputTaskTokens] = modeSpec{
		mode: inputTaskTokens,
		rules: []modeRule{
			{binding: quitChord, action: actionQuit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: append(taskTokensKeys(), quitChord)},
	}

	// All-projects mode is the screen's (allprojects.go). Its key set is
	// ENUMERATED with an ignored fall-through, as detail mode's is: the screen
	// has nothing to type, so a stray letter must do nothing rather than
	// reach the list hidden under it. The movement keys are its own bindings,
	// not the list's, so the footer says what they move. ctrl+c quits, as it
	// does over the switcher, because nothing typed can be lost here. The
	// leader is a rule, unlike in the overlays: pause and resume act on the
	// selected row and are reached through it, and the dialog over the screen
	// dims everything else (leaderBlockedReason). Pause and resume are in the
	// keymap as help only, named with their leader chords, so the footer says
	// how to reach them; the bindings are ordered for truncation survival,
	// enter and esc first.
	r[inputAllProjects] = modeSpec{
		mode: inputAllProjects,
		rules: []modeRule{
			{binding: quitChord, action: actionQuit},
			{binding: allProjectsUpKey, action: actionAllProjectsUp},
			{binding: allProjectsDownKey, action: actionAllProjectsDown},
			{binding: allProjectsPageUpKey, action: actionAllProjectsPageUp},
			{binding: allProjectsPageDownKey, action: actionAllProjectsPageDown},
			{binding: allProjectsTopKey, action: actionAllProjectsTop},
			{binding: allProjectsBottomKey, action: actionAllProjectsBottom},
			{binding: allProjectsSwitchKey, action: actionAllProjectsSwitch},
			{binding: allProjectsCloseKey, action: actionAllProjectsClose},
			{binding: keys.Leader, action: actionOpenLeader},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			allProjectsSwitchKey, allProjectsCloseKey, allProjectsUpKey, allProjectsDownKey,
			leaderHelpBinding(actionPauseBuild, "pause", true),
			leaderHelpBinding(actionResumeBuild, "resume", true),
			keys.Leader,
			allProjectsPageUpKey, allProjectsPageDownKey, allProjectsTopKey, allProjectsBottomKey,
			quitChord,
		}},
	}

	// The modals transcribe their update handlers rung for rung: mouse
	// chord first, ctrl+c, then the modal's own answers. Clean and restart
	// swallow unclaimed keys (a stray keystroke must not dismiss a
	// confirmation); the label prompt routes them to its textinput so the
	// user can type.
	r[inputConfirmClean] = modeSpec{
		mode: inputConfirmClean,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: confirmClean, action: actionConfirm},
			{binding: deleteBranch, action: actionDeleteBranch},
			{binding: deny, action: actionDeny},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			confirmClean, deleteBranch, deleteUnmerged, deny, cancelEsc, keys.Mouse, quitChord,
		}},
	}

	r[inputConfirmRestart] = modeSpec{
		mode: inputConfirmRestart,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: confirmRestart, action: actionConfirm},
			{binding: deny, action: actionDeny},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			confirmRestart, deny, cancelEsc, keys.Mouse, quitChord,
		}},
	}

	r[inputSpawnError] = modeSpec{
		mode: inputSpawnError,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: dismiss, action: actionDismiss},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			dismiss, keys.Mouse, quitChord,
		}},
	}

	r[inputEditLabel] = modeSpec{
		mode: inputEditLabel,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: saveLabel, action: actionSubmit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			saveLabel, cancelEsc, keys.Mouse, quitChord,
		}},
	}

	// The plan form is the label prompt's spec with its own enter: unclaimed
	// keys reach the textinput, so the owner can type.
	r[inputPlanText] = modeSpec{
		mode: inputPlanText,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: startPlanText, action: actionSubmit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			startPlanText, cancelEsc, keys.Mouse, quitChord,
		}},
	}

	// The open-it offer is the restart confirmation's spec with its own y:
	// a stray key must neither open the session nor dismiss the question.
	r[inputConfirmPlanOpen] = modeSpec{
		mode: inputConfirmPlanOpen,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: confirmPlanOpen, action: actionConfirm},
			{binding: deny, action: actionDeny},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			confirmPlanOpen, deny, cancelEsc, keys.Mouse, quitChord,
		}},
	}
	r[inputConfirmPlanFolder] = modeSpec{
		mode: inputConfirmPlanFolder,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: confirmPlanFolder, action: actionConfirm},
			{binding: deny, action: actionDeny},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			confirmPlanFolder, deny, cancelEsc, keys.Mouse, quitChord,
		}},
	}
	r[inputConfirmGitHubRepo] = modeSpec{
		mode: inputConfirmGitHubRepo,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: escGitHubRepo, action: actionCancel},
			{binding: confirmGitHubRepo, action: actionConfirm},
			{binding: denyGitHubRepo, action: actionDeny},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			confirmGitHubRepo, denyGitHubRepo, escGitHubRepo, keys.Mouse, quitChord,
		}},
	}
	// The approve confirm is the other y/n confirmations' shape. Its y rule
	// and binding are removed by Model.modes while the plan has errors
	// (withoutPlanApproveYes), so the registry built here is the full one.
	r[inputConfirmPlanApprove] = modeSpec{
		mode: inputConfirmPlanApprove,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: confirmPlanApprove, action: actionConfirm},
			{binding: deny, action: actionDeny},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			confirmPlanApprove, deny, cancelEsc, keys.Mouse, quitChord,
		}},
	}

	// The add form's two steps are the plan form's spec with their own enter
	// (T7.10): unclaimed keys reach the textinput, so the owner can type.
	r[inputProjectAddFolder] = modeSpec{
		mode: inputProjectAddFolder,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: nextProjectAdd, action: actionSubmit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			nextProjectAdd, cancelEsc, keys.Mouse, quitChord,
		}},
	}
	r[inputProjectAddName] = modeSpec{
		mode: inputProjectAddName,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: submitProjectAdd, action: actionSubmit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			submitProjectAdd, cancelEsc, keys.Mouse, quitChord,
		}},
	}
	// The remove confirm is the approve confirm's shape: a stray key must
	// neither remove the project nor dismiss the question.
	r[inputConfirmProjectRemove] = modeSpec{
		mode: inputConfirmProjectRemove,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: confirmProjectRemove, action: actionConfirm},
			{binding: deny, action: actionDeny},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			confirmProjectRemove, deny, cancelEsc, keys.Mouse, quitChord,
		}},
	}

	// The task-status steps (T7.11). The picker is the provider picker's
	// shape: a stray letter must neither choose a status nor dismiss the
	// list. The ids step is the add form's: unclaimed keys reach the field.
	// The confirm is the remove confirm's: nothing is written without y.
	r[inputPickTaskStatus] = modeSpec{
		mode: inputPickTaskStatus,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: chooseTaskStatus, action: actionConfirm},
			{binding: pickerUp, action: actionPickerUp},
			{binding: pickerDown, action: actionPickerDown},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			pickerUp, pickerDown, chooseTaskStatus, cancelEsc, keys.Mouse, quitChord,
		}},
	}
	r[inputTaskStatusReplacedBy] = modeSpec{
		mode: inputTaskStatusReplacedBy,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: nextTaskStatus, action: actionSubmit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			nextTaskStatus, cancelEsc, keys.Mouse, quitChord,
		}},
	}
	r[inputConfirmTaskStatus] = modeSpec{
		mode: inputConfirmTaskStatus,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: confirmTaskStatus, action: actionConfirm},
			{binding: deny, action: actionDeny},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			confirmTaskStatus, deny, cancelEsc, keys.Mouse, quitChord,
		}},
	}

	// The provider picker follows the y/n confirmations' shape: mouse chord,
	// ctrl+c, then its own answers, and every other key swallowed — a stray
	// letter must neither start a session nor dismiss the choice. Enter and
	// esc reuse actionConfirm/actionCancel, which applyKeyAction resolves by
	// mode, so they cannot collide with the clean or restart answers.
	r[inputPickProvider] = modeSpec{
		mode: inputPickProvider,
		rules: []modeRule{
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: quitChord, action: actionQuit},
			{binding: cancelEsc, action: actionCancel},
			{binding: pickerStart, action: actionConfirm},
			{binding: pickerUp, action: actionPickerUp},
			{binding: pickerDown, action: actionPickerDown},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			pickerUp, pickerDown, pickerStart, cancelEsc, keys.Mouse, quitChord,
		}},
	}

	return r
}
