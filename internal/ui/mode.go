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
	// inputSwitcher is the project-switcher overlay (Model.switcherOpen).
	inputSwitcher
	// inputForm is the session-creation form (Model.formOpen).
	inputForm
	// inputPane is a focused pane, the Agent level: every key but the leader
	// forwards to the child.
	inputPane
	// inputDetail is a FOCUSED detail pane: the summary/diff view in the main
	// region owns the keyboard so its viewport can be scrolled with the keys a
	// user reaches for. It is a second, distinct focus concept from inputPane
	// — that one routes keys into a child session, this one moves a viewport
	// this process owns — and the two are mutually exclusive by construction
	// (see modeSnapshot's two conjunctions).
	inputDetail
	// inputInbox is the inbox overlay (Model.inboxOpen): a list of the
	// project's inbox items and, one level in, an item with a field to answer
	// it. Every key is the component's, as in the switcher, and it has no
	// parent rule at all — see its spec in newModeRegistry for
	// why not even the quit chord.
	inputInbox
	// inputLeader is the `:` actions search (Model.leaderOpen, leader.go,
	// §4.7). Every key but ctrl+c is the search's: letters always type into
	// the query, enter runs the highlighted row, esc closes. It is opened at
	// every level (the leader then `:` in the Agent), which is why its place
	// in modePrecedence is above the list, the overview and the agent pane.
	inputLeader
	// inputPlanText is the plan actions' one-line form (planstart.go): amend
	// asks for a sentence, reconcile for a document's path, "fix a bug" for
	// what is broken, and "start a new plan" for a folder. It is the label
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
	// inputKeyStrip is the key strip the leader shows in the Agent
	// (Model.keyStripOpen, keystrip.go, §4.8): the next key runs an Agent
	// key, the leader again sends one leader to the agent, esc goes back to
	// Project, and any other key closes the strip and sends nothing.
	inputKeyStrip
	// inputKeys is the keys overlay `?` opens (Model.keysOpen,
	// keysoverlay.go, §4.9): every key, level by level, with `/` narrowing
	// the rows. Every key but ctrl+c is the overlay's, as in the plan check.
	inputKeys

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
	case inputSwitcher:
		return "switcher"
	case inputForm:
		return "form"
	case inputPane:
		return "pane"
	case inputDetail:
		return "detail"
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
	case inputKeyStrip:
		return "key-strip"
	case inputKeys:
		return "keys"
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
	// keyStrip mirrors Model.keyStripOpen.
	keyStrip bool
	// keysOpen mirrors Model.keysOpen.
	keysOpen bool
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
		focus:           m.focus,
		view:            m.view,
		pane:            m.paneForSelection(),
		inboxOpen:       m.inboxOpen,
		planCheckOpen:   m.planCheckOpen,
		taskTokensOpen:  m.taskTokensOpen,
		allProjectsOpen: m.allProjectsOpen,
		leaderOpen:      m.leaderOpen,
		keyStrip:        m.keyStripOpen,
		keysOpen:        m.keysOpen,
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
//   - The `:` actions search sits directly below the modals and above every
//     other overlay. Below the modals for the switcher's reason: a create can
//     fail underneath and its report must get the keyboard. Above everything
//     else because it is opened at every level, the Agent included, and its
//     mode has to outrank the pane's or the letters typed into the query
//     would reach the agent behind it. Nothing else is open when it appears,
//     so its rank against the other overlays only matters for an attach that
//     lands focused underneath — and the search is what is on screen then.
//   - The keys overlay (`?`, T16.2) sits directly below the search, for the
//     search's reason: it is opened at every level, the Agent included (the
//     leader then `?`), and the keyboard stays in the Agent behind it, so it
//     must outrank the pane or the letters typed into its filter would reach
//     the agent. It outranks the key strip because the strip closes on the
//     key that opens it.
//   - The key strip (T16.2) sits directly below the keys overlay. It is shown only
//     in the Agent, so its predicate carries the Agent's facts (the right
//     side focused in the agent view) as well as its flag: a flag left set
//     after a non-key path moved the focus (an agent that exited) must not
//     take the keyboard from the list. It outranks the pane, whose every key
//     goes to the agent, because the strip is what the next key is for.
//   - The switcher outranks the form (unreachable in practice, but views.go
//     orders the frame this way so frame and keys agree) and outranks a
//     focused pane (reachable: a focusOnArrival attach landing under the
//     overlay — "keys go to the overlay, because the overlay is what is on
//     screen").
//   - The inbox overlay sits beside the switcher, directly below it. It is
//     opened by `i`, which closes nothing, so no other overlay is up when it
//     appears and its place among them is declared for the frame's sake:
//     views.go composes the overlays in this order. Below the modals for the
//     switcher's reason (a create can fail underneath and its report must get
//     the keyboard), and above the form and both right-side
//     focuses, so a pane that arrives focused under it cannot take keys meant
//     for the answer field.
//   - The check-the-plan overlay sits directly below the inbox, for the
//     inbox's reasons: it is opened only from the `:` search, so no other
//     overlay is up when it appears, and it outranks the form
//     and both right-side focuses so a pane that arrives focused under it
//     cannot take the scroll keys.
//   - The tokens overlay (T7.12) sits directly below the check-the-plan
//     overlay, for the same reasons: it is opened only by `t` on the Build
//     tab (or when a read it waited on lands, and then only while the Build
//     tab has the keyboard), so no other overlay is up when it appears.
//   - The all-projects screen sits directly below the tokens overlay. It is
//     the All projects level, opened by esc from the session list, so no
//     overlay is up when it appears. It outranks the form and
//     both right-side focuses because it takes their place on screen: a pane
//     that arrives focused while it is open (a click on a notification for
//     this project) must not take keys meant for the rows.
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
//     every modal, the search, the switcher, the form and a focused pane all
//     outrank it. Its position relative to inputPane is
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
//     Model.levelKey intercepts inputDetail when agentPlaceholderDrawn and
//     hands the key to placeholderKey, so it shares detail's place in this
//     table (weakest right-side claim) while answering only its own keys.
//     Either way the keyboard follows the drawn surface and never falls
//     through to normal mode, where typed keys would hit list bindings. List
//     focus is normal mode whatever the right side shows: with the list
//     focused nothing on the right owns the keyboard, however live the pane
//     is.
//   - There is no resize mode since W3.T1 of T16.2: the Agent is drawn over
//     the Project rather than beside the list, so nothing on screen is split
//     and there is no divider to move.
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
	{inputKeys, func(s modeState) bool { return s.keysOpen }},
	{inputKeyStrip, func(s modeState) bool { return s.keyStrip && s.focus == focusRight && s.view == viewAgent }},
	{inputSwitcher, func(s modeState) bool { return s.switcherOpen }},
	{inputInbox, func(s modeState) bool { return s.inboxOpen }},
	{inputPlanCheck, func(s modeState) bool { return s.planCheckOpen }},
	{inputTaskTokens, func(s modeState) bool { return s.taskTokensOpen }},
	{inputAllProjects, func(s modeState) bool { return s.allProjectsOpen }},
	{inputForm, func(s modeState) bool { return s.formOpen }},
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
// confirm-clean is confirmClean, actionDetails is showDetails, and so on.
// Keeping the names symbolic here is what makes the routing testable without
// invoking any handler.
type modeAction int

const (
	// actionNone is the zero value: the key was not claimed by a rule.
	actionNone modeAction = iota

	// Cross-cutting actions.
	actionQuit        // requestQuit
	actionMouseToggle // toggleMouseCapture (ctrl+g in overlays; ctrl+] m in the Agent)

	// The key table's level actions (keytable.go, T16.2). Moving, going in
	// and going back are outcomes of decideKey rather than actions to run,
	// because what they do depends on the level; they are actions too so
	// the key table's rows can name them.
	//
	// actionMove is the move keys (j k ↑ ↓ pgup pgdn home end). Which way is
	// read from the key itself, so one action stands for all of them.
	actionMove
	// actionGoIn is enter: in one level, or open what is selected.
	actionGoIn
	// actionGoBack is esc: out one level.
	actionGoBack
	// actionActionsSearch is `:`, the actions-by-name search (§4.7).
	actionActionsSearch
	// actionKeys is `?`: the keys overlay (openKeys, keysoverlay.go).
	actionKeys
	// actionPauseToggle is `p`, one toggle replacing the leader's P and U:
	// pause a running build, resume a paused or stopped one.
	actionPauseToggle
	// actionAnswerOldest is `a` at All projects: open the inbox of the
	// project holding the oldest blocking item (answerOldest, allprojects.go).
	actionAnswerOldest
	// actionDetails is `d`, the selected session's details. Until T16.5
	// builds the details overlay it gives the overview the keyboard (A2).
	actionDetails

	// Session-list and overview actions.
	actionCursorUp
	actionCursorDown
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
	actionInbox // i: openInbox
	// The five plan actions (planstart.go). They have no key: the `:`
	// search reaches them by name only (keyTable).
	actionPlanExtend    // startPlanExtend
	actionPlanAmend     // openPlanTextForm(amend)
	actionPlanReconcile // openPlanTextForm(reconcile)
	actionPlanFix       // openPlanTextForm(fix): "fix a bug" (T13.6)
	actionPlanNew       // openPlanNewForm: start a new plan in a folder
	// actionPlanCheck is "check the plan" (T7.7): openPlanCheck shows the
	// plan checker's result from the last plan status read. By name only,
	// like the plan actions above.
	actionPlanCheck
	// actionPlanApprove is "approve the plan" (T7.8): openPlanApprove raises
	// the confirm (planapprove.go). By name only, like the plan actions.
	actionPlanApprove
	// The all-projects screen's move keys, clamped (moveAllProjects).
	actionAllProjectsUp
	actionAllProjectsDown
	actionAllProjectsPageUp
	actionAllProjectsPageDown
	actionAllProjectsTop
	actionAllProjectsBottom
	// actionProjectAdd and actionProjectRemove are All projects' `+` and `-`
	// (T7.10): add opens the two-step form, remove raises the y/n confirm for
	// the selected row (projectedit.go).
	actionProjectAdd
	actionProjectRemove
	// actionTaskStatus is "set this task's status" (T7.11, `s`): it opens
	// the status picker for the Build tab's cursor row (taskstatus.go). It
	// runs only on the Build tab with the right side focused.
	actionTaskStatus
	// actionTaskTokens is "show this task's tokens" (T7.12, `t`): it opens
	// the tokens overlay for the Build tab's cursor row (tasktokens.go). It
	// runs only on the Build tab with the right side focused.
	actionTaskTokens
	// actionRefreshUsage is "read usage" (T7.13, `R`): it takes a fresh
	// usage reading through Deps.RefreshUsage, off the screen-drawing path,
	// and the header shows it when it lands (usagerefresh.go).
	actionRefreshUsage

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

// actionNames is each action's name for test-failure output; nothing
// renders it.
var actionNames = map[modeAction]string{
	actionNone:                "none",
	actionQuit:                "quit",
	actionMouseToggle:         "mouse-toggle",
	actionMove:                "move",
	actionGoIn:                "go-in",
	actionGoBack:              "go-back",
	actionActionsSearch:       "actions-search",
	actionKeys:                "keys",
	actionPauseToggle:         "pause-toggle",
	actionAnswerOldest:        "answer-oldest",
	actionDetails:             "details",
	actionCursorUp:            "cursor-up",
	actionCursorDown:          "cursor-down",
	actionNewSession:          "new-session",
	actionScratch:             "scratch",
	actionProjects:            "projects",
	actionClean:               "clean",
	actionRestart:             "restart",
	actionEditLabel:           "edit-label",
	actionPageUp:              "page-up",
	actionPageDown:            "page-down",
	actionListTop:             "list-top",
	actionListBottom:          "list-bottom",
	actionDetailScrollUp:      "detail-scroll-up",
	actionDetailScrollDown:    "detail-scroll-down",
	actionDetailHalfPageUp:    "detail-half-page-up",
	actionDetailHalfPageDown:  "detail-half-page-down",
	actionDetailPageUp:        "detail-page-up",
	actionDetailPageDown:      "detail-page-down",
	actionDetailGotoTop:       "detail-goto-top",
	actionDetailGotoBottom:    "detail-goto-bottom",
	actionDetailTabNext:       "detail-tab-next",
	actionDetailTabPrev:       "detail-tab-prev",
	actionDetailTabJump:       "detail-tab-jump",
	actionInbox:               "inbox",
	actionPlanExtend:          "plan-extend",
	actionPlanAmend:           "plan-amend",
	actionPlanReconcile:       "plan-reconcile",
	actionPlanFix:             "plan-fix",
	actionPlanNew:             "plan-new",
	actionPlanCheck:           "plan-check",
	actionPlanApprove:         "plan-approve",
	actionAllProjectsUp:       "all-projects-up",
	actionAllProjectsDown:     "all-projects-down",
	actionAllProjectsPageUp:   "all-projects-page-up",
	actionAllProjectsPageDown: "all-projects-page-down",
	actionAllProjectsTop:      "all-projects-top",
	actionAllProjectsBottom:   "all-projects-bottom",
	actionProjectAdd:          "project-add",
	actionProjectRemove:       "project-remove",
	actionTaskStatus:          "task-status",
	actionTaskTokens:          "task-tokens",
	actionRefreshUsage:        "refresh-usage",
	actionConfirm:             "confirm",
	actionDeny:                "deny",
	actionCancel:              "cancel",
	actionDismiss:             "dismiss",
	actionSubmit:              "submit",
	actionDeleteBranch:        "delete-branch",
	actionPickerUp:            "picker-up",
	actionPickerDown:          "picker-down",
	actionFormCancel:          "form-cancel",
	actionFormSubmit:          "form-submit",
	actionFormNextField:       "form-next-field",
	actionFormPrevField:       "form-prev-field",
}

// String exists for test-failure output; nothing renders it.
func (a modeAction) String() string {
	if n, ok := actionNames[a]; ok {
		return n
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
// Since T16.2 the level keys (keytable.go) decide enter and esc, which were
// the guarded rules, so no rule reads a guard; paneOpen stays for the
// blocked reasons (leaderConds.paneOpen) and for a later guarded rule.
type routeConds struct {
	// paneOpen is "an agent pane is installed".
	paneOpen bool
}

// modeRule is one row of a mode's routing table: a binding, the action it
// maps to, and an optional guard.
type modeRule struct {
	binding key.Binding
	action  modeAction
	// needsPane gates the rule on routeConds.paneOpen. The zero value
	// (ungated) is the common case.
	needsPane bool
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
// source. Since T16.2 the level modes — the list, the overview, All projects,
// the agent pane and the key strip — carry no action rules: the key table
// (keytable.go) and decideKey (levels.go) say what a key does there, and
// Model.levelKey runs it. Their specs keep only the keys that are the
// surface's own (the list's cursor, the overview's scroll and tabs, the
// screen's rows), which levelKey routes a move key and an unlisted key
// through. Two cross-cutting facts the tables encode, both verified against
// every handler (see decisions.md for the audit):
//
//   - ctrl+g (keys.Mouse) is a rule in every overlay, form, prompt and
//     confirm, except the switcher and the inbox overlay. The
//     switcher's omission is documented intent (model.go:2161-2165): the
//     overlay replaces the pane in the main region, so there is no session
//     output behind it worth selecting. In the inbox overlay the chord closes
//     the overlay (see its spec below). In the Agent it is ctrl+] m; in the
//     pane ctrl+g itself is the agent's.
//   - ctrl+c quits from the modals, the switcher, the actions search and,
//     through decideKey, All projects and Project; it is deliberately
//     component fall-through in the form (the name buffer is worth
//     protecting from a stray chord — TestFormSwallowsQuitKeysEscIsOnlyExit),
//     and goes to the agent in the Agent (the session owns SIGINT semantics).
func newModeRegistry(keys keyMap) modeRegistry {
	// quitChord is ctrl+c ALONE, not q as well: the modals, the switcher and
	// the actions search must let `q` through as an ordinary character (it
	// answers nothing in a y/n modal, filters in the switcher and the search,
	// and types into the label prompt).
	quitChord := key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
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
	var r modeRegistry

	// Normal mode is list focus at Project, and the empty state at All
	// projects (no project open). Since T16.2 its rules are only the list's
	// cursor and paging keys: levelKey (model.go) asks decideKey first, and a
	// move key comes here to say which way the list moves. Every other key is
	// the key table's (keytable.go) — enter, esc, ctrl+c and the action
	// letters — or nothing, so a key the table does not list falls through
	// to routeIgnored.
	//
	// The overview's scroll and tab keys are deliberately absent: with the
	// list focused the user's model is that nothing on the right is under
	// control — "When on the left pane, you can't control anything in the
	// right pane" (plan Context).
	r[inputNormal] = modeSpec{
		mode: inputNormal,
		rules: []modeRule{
			{binding: keys.Up, action: actionCursorUp},
			{binding: keys.Down, action: actionCursorDown},
			{binding: pageUp, action: actionPageUp},
			{binding: pageDown, action: actionPageDown},
			{binding: listTop, action: actionListTop},
			{binding: listBottom, action: actionListBottom},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			keys.Up, keys.Down, pageUp, pageDown, listTop, listBottom,
		}},
	}

	// Pane mode is the Agent: right focus in agent view with the selected
	// row's pane installed. Exactly one key is the dashboard's: the leader
	// (ctrl+] by default), which shows the key strip (inputKeyStrip).
	// Everything else forwards to the child — esc, q, tab, ctrl+c (the
	// session owns SIGINT), Ctrl+Space (sent on as NUL), and every letter.
	// decideKey (levels.go) is what says so; this spec has no rule, and its
	// fall-through is the child for a caller that routes through it. Adding
	// a rule here takes a key away from every agent the dashboard hosts,
	// which is why the plan pins this set.
	r[inputPane] = modeSpec{
		mode: inputPane,
		fall: routeChild,
		keys: modeKeyMap{bindings: []key.Binding{keys.Leader}},
	}

	// The key strip (keystrip.go) is the leader's in the Agent. Every key is
	// decideKey's: an Agent key runs, the leader passes one leader on, any
	// other key closes the strip. The bindings are help-only, for the status
	// bar while it is up.
	r[inputKeyStrip] = modeSpec{
		mode: inputKeyStrip,
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{keys.Leader}},
	}

	// Detail mode is a focused overview, at Project. Its rule set is
	// ENUMERATED rather than inherited: the fall-through is routeIgnored, so
	// everything this table and the key table omit is swallowed, which is the
	// point. A scroll gesture must never open, clean or steal a session, and
	// "ignored" is the only disposition that guarantees it for keys nobody
	// has thought of yet.
	//
	// The rules are the overview's own keys: the three tab keys, because
	// switching tabs is reading — the same gesture as scrolling — and the ten
	// scroll keys: up/k, down/j, ctrl+u, ctrl+d, pgup, pgdown, home, end.
	// esc, ctrl+c and the action letters are the key table's, which levelKey
	// asks first. These scroll and tab keys are bound in this mode only: from
	// the list they would drive the right pane.
	//
	// The bindings are ordered for TRUNCATION SURVIVAL, not for rule fidelity:
	// position here decides what a narrow terminal still shows, so the tab
	// keys lead and the scroll keys — the most numerous — sort last.
	r[inputDetail] = modeSpec{
		mode: inputDetail,
		rules: []modeRule{
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
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			detailTabNextKey, detailTabPrevKey, detailTabJumpKey,
			detailFocusUp, detailFocusDown, detailHalfPageUp, detailHalfPageDown,
			detailPageUp, detailPageDown, detailGotoTop, detailGotoBottom,
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

	// Leader mode is the `:` actions search (leader.go). Its one parent
	// rule is ctrl+c, so the dashboard can always be left: since T16.2
	// letters always type into the query (§4.7), so q is a letter here and
	// the quit row is run by name. Every other key is the search's, decided
	// by leaderKeyDecision. The other bindings are help-only, for the footer
	// while it is up.
	r[inputLeader] = modeSpec{
		mode: inputLeader,
		rules: []modeRule{
			{binding: quitChord, action: actionQuit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			leaderRunKey, leaderMoveKey, leaderFilterKey, leaderCloseKey, quitChord,
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

	// Keys mode (`?`, keysoverlay.go) is the plan check's shape for the plan
	// check's reasons: ctrl+c quits, since the overlay holds nothing worth
	// keeping, and every other key is the overlay's, staged in
	// keysKeyDecision, so esc's two steps and the filter are implemented
	// once. q is not a rule: the filter types it.
	r[inputKeys] = modeSpec{
		mode: inputKeys,
		rules: []modeRule{
			{binding: quitChord, action: actionQuit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: append(keysOverlayKeys(), quitChord)},
	}

	// All-projects mode is the screen's (allprojects.go), at the All
	// projects level. Its rules are only the screen's move keys, which
	// levelKey routes a move key through: enter, esc, ctrl+c and the action
	// keys are the key table's, asked first. The fall-through is ignored, as
	// detail mode's is: the screen has nothing to type, so a stray letter
	// must do nothing rather than reach the list hidden under it. The
	// movement keys are its own bindings, not the list's, so the footer says
	// what they move.
	r[inputAllProjects] = modeSpec{
		mode: inputAllProjects,
		rules: []modeRule{
			{binding: allProjectsUpKey, action: actionAllProjectsUp},
			{binding: allProjectsDownKey, action: actionAllProjectsDown},
			{binding: allProjectsPageUpKey, action: actionAllProjectsPageUp},
			{binding: allProjectsPageDownKey, action: actionAllProjectsPageDown},
			{binding: allProjectsTopKey, action: actionAllProjectsTop},
			{binding: allProjectsBottomKey, action: actionAllProjectsBottom},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			allProjectsUpKey, allProjectsDownKey,
			allProjectsPageUpKey, allProjectsPageDownKey, allProjectsTopKey, allProjectsBottomKey,
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
