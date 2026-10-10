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
// over model.go (applyPaneAttached sets paneFocused, spawnFinishedMsg raises
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
	// inputPane is a focused pane: every unclaimed key forwards to the child.
	inputPane
	// inputPalette is the `:` command palette (Model.paletteOpen). Its rows are
	// derived from inputNormal's rules (palette.go), which is also why it is the
	// only mode a user can reach ONLY from normal mode: every other mode either
	// has free text to type, where `:` is a legitimate character, or a child
	// session that owns the key.
	inputPalette
	// inputDetail is a FOCUSED detail pane: the summary/diff view in the main
	// region owns the keyboard so its viewport can be scrolled with the keys a
	// user reaches for. It is a second, distinct focus concept from inputPane
	// — that one routes keys into a child session, this one moves a viewport
	// this process owns — and the two are mutually exclusive by construction
	// (see modeSnapshot's two conjunctions).
	inputDetail
	// inputResize is the ctrl+w pane-resize mode (Model.resizing): h/l and the
	// arrows move the sidebar/main divider live, ctrl+w commits and persists
	// the split, esc restores the width at entry. It is entered only from
	// inputNormal and inputDetail — a focused pane keeps ctrl+w for its child
	// — and swallows every key it does not bind, so a stray letter cannot
	// open, clean or steal a session mid-resize.
	inputResize

	// inputModeCount bounds the enum for exhaustive iteration (registry
	// completeness tests, and Wave 6 enumerating modes for the palette).
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
	case inputPalette:
		return "palette"
	case inputDetail:
		return "detail"
	case inputResize:
		return "resize"
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
	case inputConfirmClean, inputSpawnError, inputConfirmRestart, inputEditLabel, inputPickProvider:
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
	// paneFocused is the CONJUNCTION m.paneFocused && m.pane != nil, exactly
	// the guard the old handleKey's pane branch used — a focused flag with no
	// pane must not claim the keyboard.
	paneFocused bool
	// paletteOpen mirrors Model.paletteOpen.
	paletteOpen bool
	// detailFocused is the CONJUNCTION m.detailFocused && !m.shouldRenderPane(),
	// the detail pane's analogue of paneFocused above and for the same reason:
	// a focus flag whose surface is not on screen must not claim the keyboard.
	// A focus-on-arrival attach can install a pane under a focused detail view,
	// and this is what stops the keyboard staying on a view that is no longer
	// drawn. Masking is only half the job — Update reconciles the Model field
	// itself, see Model.reconcileDetailFocus.
	detailFocused bool
	// resizing mirrors Model.resizing. Unlike the two focus flags it needs no
	// conjunction: resize mode moves the divider between surfaces that are
	// always drawn, whichever of them happens to be in the main region.
	resizing bool
}

// modeSnapshot builds the modeState the dispatch derives from. This is the
// Model→mechanism bridge; keeping it beside the mechanism (rather than in
// model.go) keeps every reader of the raw flags the dispatch depends on in one
// reviewable file.
func (m Model) modeSnapshot() modeState {
	return modeState{
		modal:         m.modal,
		switcherOpen:  m.switcherOpen,
		formOpen:      m.formOpen,
		listFiltering: m.list.SettingFilter(),
		paneFocused:   m.paneFocused && m.pane != nil,
		paletteOpen:   m.paletteOpen,
		detailFocused: m.detailFocused && !m.shouldRenderPane(),
		resizing:      m.resizing,
	}
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
func (m Model) modes() modeRegistry { return newModeRegistry(m.keys) }

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
//   - Rows 1-5 (the modals) outrank everything, including the switcher —
//     model.go:872-895 documents that in words, and it is reachable because
//     spawnFinishedMsg raises the modal asynchronously under an open overlay
//     or form. Their relative order is immaterial (one enum field holds them).
//   - The palette sits directly below the modals and above every other
//     overlay. Below the modals for the switcher's reason verbatim: a
//     background create can fail asynchronously and raise a modal over it, and
//     the modal is the only place that output is readable. Above the rest
//     because it can only be OPENED from normal mode, so nothing else is up
//     when it appears — and if something arrives underneath (a focusOnArrival
//     attach), the palette is what is on screen.
//   - The switcher outranks the form (unreachable in practice, but views.go
//     orders the frame this way so frame and keys agree) and outranks a
//     focused pane (reachable: a focusOnArrival attach landing under the
//     overlay — "keys go to the overlay, because the overlay is what is on
//     screen").
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
//     every modal, the palette, the switcher, the form, the filter prompt and
//     a focused pane all outrank it. Its position relative to inputPane is
//     declared rather than relied on — the two snapshot conjunctions
//     (paneFocused needs a pane, detailFocused needs the pane NOT to be the
//     drawn surface) make them mutually exclusive by construction — but a
//     hand-ordered table shadows a misplaced row silently, so the order is
//     pinned by TestActiveInputModePrecedenceMatrix rather than left implicit.
//   - Resize mode sits below every modal and overlay and the filter prompt,
//     so a spawn error raised mid-resize still gets the keyboard and the
//     resize resumes once it is dismissed. It sits ABOVE a focused pane: it
//     cannot be entered from one, so the only way the two coexist is a
//     focus-on-arrival attach landing mid-resize, and the explicit gesture the
//     user is in the middle of keeps the keyboard — h/l must move the divider,
//     not type into a session that just appeared. It sits above a focused
//     detail pane because it is entered FROM one and leaves detailFocused set,
//     so the detail focus resumes on exit.
var modePrecedence = []modeLayer{
	{inputConfirmClean, func(s modeState) bool { return s.modal == inputConfirmClean }},
	{inputSpawnError, func(s modeState) bool { return s.modal == inputSpawnError }},
	{inputConfirmRestart, func(s modeState) bool { return s.modal == inputConfirmRestart }},
	{inputEditLabel, func(s modeState) bool { return s.modal == inputEditLabel }},
	{inputPickProvider, func(s modeState) bool { return s.modal == inputPickProvider }},
	{inputPalette, func(s modeState) bool { return s.paletteOpen }},
	{inputSwitcher, func(s modeState) bool { return s.switcherOpen }},
	{inputForm, func(s modeState) bool { return s.formOpen }},
	{inputFilter, func(s modeState) bool { return s.listFiltering }},
	{inputResize, func(s modeState) bool { return s.resizing }},
	{inputPane, func(s modeState) bool { return s.paneFocused }},
	{inputDetail, func(s modeState) bool { return s.detailFocused }},
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
// confirm-clean is confirmClean, actionPaneToggle is togglePaneSummary, and so
// on. Keeping the names symbolic here is what makes the routing testable
// without invoking any handler.
type modeAction int

const (
	// actionNone is the zero value: the key was not claimed by a rule.
	actionNone modeAction = iota

	// Cross-cutting actions.
	actionQuit        // requestQuit
	actionMouseToggle // toggleMouseCapture (ctrl+g)

	// Pane actions.
	actionPaneToggle  // togglePaneSummary (ctrl+t)
	actionPaneUnfocus // focused pane: ctrl+\ returns input to the dashboard
	actionPaneClose   // unfocused open pane: ctrl+\ closes (detaches)

	// actionDetailUnfocus is the focused DETAIL pane's release (esc or
	// ctrl+\): releaseDetailFocus, which hands the keyboard back to the
	// session list. It is its own action rather than a second meaning for
	// actionPaneUnfocus because the two clear different fields and say
	// different things to the user.
	actionDetailUnfocus

	// Normal-mode actions, one per global binding.
	actionCursorUp
	actionCursorDown
	actionFocusPane
	actionNewSession
	actionScratch
	actionProjects
	actionClean
	actionRestartWedged
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
	actionPalette
	actionEnterResize // ctrl+w in normal and detail: enterResize

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
	actionPickerUp     // up/k in the provider picker: highlight the previous provider
	actionPickerDown   // down/j in the provider picker: highlight the next provider

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
	case actionPaneUnfocus:
		return "pane-unfocus"
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
	case actionRestartWedged:
		return "restart-wedged"
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
	case actionPalette:
		return "palette"
	case actionEnterResize:
		return "enter-resize"
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
// There is exactly one today: normal mode's ctrl+\ closes a pane only when one
// exists (with no pane the chord falls through to ignored, as it does now).
type routeConds struct {
	paneOpen bool
	// listFiltered is "the session list has a filter applied". Esc clears one,
	// and must fall through to ignored when there is nothing to clear: bound
	// unconditionally it would cancel a pending preview on every stray Esc,
	// which normal mode does not do today.
	listFiltered bool
	// detailShown is "the detail pane, not a session pane, is in the main
	// region" (!shouldRenderPane). The tab keys are gated on it: with a pane on
	// screen there is no tab strip to move, and an ungated tab would look bound
	// in `?` and the palette while doing nothing.
	detailShown bool
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
	// needsDetail gates the rule on routeConds.detailShown.
	needsDetail bool
}

// modeKeyMap is one mode's action surface for Wave 6's help.Model and command
// palette: the bindings available in that mode, in a stable order, with the
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
	// keys is the mode's help/palette surface. It is a superset of the rule
	// bindings for modes whose component owns part of the interaction (the
	// switcher's enter/esc/R live in switcher.Update but must still be
	// advertisable).
	keys modeKeyMap
}

// The detail pane's tab keys. Package-level like helpToggleKey (views.go)
// rather than local to newModeRegistry, because normalBindingEnabled must
// recognise the same three bindings to hide them from `?` while a session
// pane is on screen — the routing guard (needsDetail) hides them from the
// palette, but help.Model never sees guards, only Enabled().
//
// tab and shift+tab are unbound in normal mode today (the form and the
// palette use them, in their own modes), and no digit is bound anywhere. One
// binding carries all five digits: paletteEntries lists one row per RULE and
// re-presses its first key, so the palette offers "jump to tab" as one row
// that lands on tab 1 — acceptable, since tab/shift+tab sit right beside it.
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
		key.WithKeys("1", "2", "3", "4", "5"),
		key.WithHelp("1-5", "jump to tab"),
	)
)

// resizeToggleKey is ctrl+w, the way into resize mode from inputNormal and
// inputDetail. It is package-level so tests and the resize-mode notifications
// name the one binding the dispatch matches. It is deliberately NOT a rule in
// inputPane: a focused session owns ctrl+w (delete-word in a shell or an
// editor), so there it falls through to the child like every other key.
// Inside resize mode the same chord commits; that is a separate binding with
// its own help text (see newModeRegistry), because the footer must say "save"
// there rather than "resize panes".
var resizeToggleKey = key.NewBinding(
	key.WithKeys("ctrl+w"),
	key.WithHelp("ctrl+w", "resize panes"),
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
		if rule.needsDetail && !conds.detailShown {
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
// The rule tables transcribe the old handlers exactly; each mode's comment
// names its source. Two cross-cutting facts the tables encode, both verified
// against every handler (see decisions.md for the audit):
//
//   - ctrl+g (keys.Mouse) is a rule in EVERY mode except the switcher. The
//     switcher's omission is documented intent (model.go:2161-2165): the
//     overlay replaces the pane in the main region, so there is no session
//     output behind it worth selecting, and one Esc away the chord works.
//   - ctrl+c quits from the modals and the switcher, is deliberately
//     component fall-through in the form (the name buffer is worth protecting
//     from a stray chord — TestFormSwallowsQuitKeysEscIsOnlyExit), forwards
//     to the child in a focused pane (the session owns SIGINT semantics), and
//     quits in normal via keys.Quit.
func newModeRegistry(keys keyMap) modeRegistry {
	// quitChord is ctrl+c ALONE, not keys.Quit: the modals and the switcher
	// must let `q` through as an ordinary character (it answers nothing in a
	// y/n modal, filters in the switcher, and types into the label prompt),
	// so only normal mode routes through the two-key Quit binding.
	quitChord := key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	)
	// unfocusPane and closePane are both ctrl+\ — the chord escalates rather
	// than changing meaning (focused: keyboard back; unfocused: pane away),
	// which is why they are two rules in two modes, not one binding.
	unfocusPane := key.NewBinding(
		key.WithKeys("ctrl+\\"),
		key.WithHelp("ctrl+\\", "return to dashboard"),
	)
	closePane := key.NewBinding(
		key.WithKeys("ctrl+\\"),
		key.WithHelp("ctrl+\\", "close pane"),
	)
	// releaseDetail is the focused detail pane's way out. It carries BOTH
	// keys on one binding rather than two rules, because unlike unfocusPane /
	// closePane above there is no escalation here — esc and ctrl+\ are the
	// same gesture, and one binding is one footer item saying so. esc is safe
	// to bind unguarded in this mode for the reason it is NOT in normal mode:
	// inputNormal's filterClear owns esc there, and mode precedence — not rule
	// order — keeps an applied filter intact while the detail pane is focused.
	releaseDetail := key.NewBinding(
		key.WithKeys("esc", "ctrl+\\"),
		key.WithHelp("esc/ctrl+\\", "back to list"),
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
	// Detail-pane scroll keys, chosen against the same constraint pageUp/
	// pageDown were: they must avoid every single letter this mode already
	// spends AND leave pgup/pgdn/home/end on the session list, since that
	// pairing (dedicated detail keys, untouched list keys) is the whole point
	// of adding new bindings rather than repointing the existing ones.
	// viewport.DefaultKeyMap (bubbles/v2) binds line-at-a-time scroll to plain
	// up/down/j/k and half-page to ctrl+u/ctrl+d. All four of those line keys
	// are taken here — keys.Up is "up"/"k" and keys.Down is "down"/"j"
	// (defaultKeyMap, model.go), the session-list cursor — so line-at-a-time
	// instead gets the shift-modified arrows (shift+up/shift+down are their
	// own distinct key strings under v2, the same fact that already lets
	// shift+tab and tab differ — see the comment on formPrev above).
	// Half-page keeps bubbles' own ctrl+u/ctrl+d verbatim: this mode's only
	// other ctrl chords are ctrl+t/ctrl+\/ctrl+g/ctrl+c, so both are free.
	detailScrollUp := key.NewBinding(
		key.WithKeys("shift+up"),
		key.WithHelp("shift+↑", "detail up"),
	)
	detailScrollDown := key.NewBinding(
		key.WithKeys("shift+down"),
		key.WithHelp("shift+↓", "detail down"),
	)
	detailHalfPageUp := key.NewBinding(
		key.WithKeys("ctrl+u"),
		key.WithHelp("ctrl+u", "detail half page up"),
	)
	detailHalfPageDown := key.NewBinding(
		key.WithKeys("ctrl+d"),
		key.WithHelp("ctrl+d", "detail half page down"),
	)
	// Focused-detail-pane scroll keys (inputDetail only). up/down/j/k, pgup/
	// pgdn and home/end are FREE here in a way they are not in inputNormal:
	// with the detail pane focused these keys have no session-list cursor to
	// collide with (list keys are absent from this mode's whole rule table),
	// so they get viewport.DefaultKeyMap's own bindings verbatim rather than
	// the shift-modified workaround above. They are declared fresh rather
	// than sharing keys.Up/keys.Down/pageUp/pageDown/listTop/listBottom
	// because those carry help text for what the key does in NORMAL mode
	// ("preview↑", "page up" for the session list) — reused here it would
	// render a scroll gesture under a stale label. ctrl+u/ctrl+d are the one
	// exception: detailHalfPageUp/Down above are reused as-is, because they
	// already mean "half-page the detail viewport" in both modes.
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

	var r modeRegistry

	// Normal mode transcribes the old handleKey's unfocused path: the ctrl+t
	// intercept, the ctrl+\ close (guarded on an open pane), then the
	// modeless switch in its original order. Enter and f deliberately live on
	// ONE binding and ONE rule: they are two physical gestures for focusing
	// whichever main-region surface is already drawn, not separate lifecycle
	// and focus operations.
	r[inputNormal] = modeSpec{
		mode: inputNormal,
		rules: []modeRule{
			{binding: keys.Toggle, action: actionPaneToggle},
			{binding: closePane, action: actionPaneClose, needsPane: true},
			{binding: keys.Quit, action: actionQuit},
			{binding: keys.Up, action: actionCursorUp},
			{binding: keys.Down, action: actionCursorDown},
			{binding: keys.Focus, action: actionFocusPane},
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: keys.New, action: actionNewSession},
			{binding: keys.Scratch, action: actionScratch},
			{binding: keys.Project, action: actionProjects},
			{binding: keys.Clean, action: actionClean},
			{binding: keys.Restart, action: actionRestartWedged},
			{binding: keys.Label, action: actionEditLabel},
			{binding: keys.Filter, action: actionFilterStart},
			{binding: filterClear, action: actionFilterClear, needsFilter: true},
			{binding: keys.Palette, action: actionPalette},
			{binding: pageUp, action: actionPageUp},
			{binding: pageDown, action: actionPageDown},
			{binding: listTop, action: actionListTop},
			{binding: listBottom, action: actionListBottom},
			{binding: detailScrollUp, action: actionDetailScrollUp},
			{binding: detailScrollDown, action: actionDetailScrollDown},
			{binding: detailHalfPageUp, action: actionDetailHalfPageUp},
			{binding: detailHalfPageDown, action: actionDetailHalfPageDown},
			{binding: detailTabNextKey, action: actionDetailTabNext, needsDetail: true},
			{binding: detailTabPrevKey, action: actionDetailTabPrev, needsDetail: true},
			{binding: detailTabJumpKey, action: actionDetailTabJump, needsDetail: true},
			{binding: resizeToggleKey, action: actionEnterResize},
		},
		fall: routeIgnored,
		// Help/palette order is intentionally independent of routing-rule order.
		// The command palette shows only its first viewport without scrolling,
		// so the two live-state surface controls follow movement immediately:
		// Focus chooses keyboard ownership and Toggle chooses agent versus detail.
		// Every unrelated binding retains its prior relative order after them,
		// with resizeToggleKey last because the curated short help omits it and
		// full help/palette should rank it behind the more common actions.
		keys: modeKeyMap{bindings: []key.Binding{
			keys.Up, keys.Down, keys.Focus, keys.Toggle,
			pageUp, pageDown, listTop, listBottom,
			detailScrollUp, detailScrollDown, detailHalfPageUp, detailHalfPageDown,
			detailTabNextKey, detailTabPrevKey, detailTabJumpKey,
			keys.New, keys.Scratch,
			keys.Project, keys.Clean, keys.Restart, keys.Label,
			keys.Filter, filterClear, keys.Palette,
			closePane, keys.Mouse, keys.Quit, resizeToggleKey,
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

	// Pane mode transcribes the focused-pane branch plus the ctrl+t intercept
	// that today sits just above it: toggle, unfocus, mouse, and everything
	// else — ctrl+c included, the session owns SIGINT — forwards to the child.
	r[inputPane] = modeSpec{
		mode: inputPane,
		rules: []modeRule{
			{binding: keys.Toggle, action: actionPaneToggle},
			{binding: unfocusPane, action: actionPaneUnfocus},
			{binding: keys.Mouse, action: actionMouseToggle},
		},
		fall: routeChild,
		keys: modeKeyMap{bindings: []key.Binding{
			unfocusPane, keys.Mouse, keys.Toggle,
		}},
	}

	// Detail mode is a focused detail pane. Its key set is ENUMERATED rather
	// than inherited: the fall-through is routeIgnored, so everything normal
	// mode binds and this table omits — enter, n, c, l, r, p, /, o, : — is
	// swallowed, which is the point. A scroll gesture must never open, clean
	// or steal a session, and "ignored" is the only disposition that
	// guarantees it for keys nobody has thought of yet.
	//
	// What survives, and why: the release chords (or the state is a trap), the
	// universal q/ctrl+c/ctrl+g, ctrl+t (the way back to a hidden pane, and the
	// way in for a running session), the three tab keys, because switching
	// tabs is reading — the same gesture as scrolling — and, finally, the ten
	// scroll keys W2.T1 adds: up/k, down/j, ctrl+u, ctrl+d, pgup, pgdown,
	// home, end — plus ctrl+w into resize mode, which keeps detailFocused set
	// so the focus resumes when the resize ends. The tab rules are deliberately ungated: needsDetail asks "is
	// the detail pane drawn", which this mode's own snapshot conjunction has
	// already answered yes.
	//
	// The bindings are ordered for TRUNCATION SURVIVAL, not for rule fidelity:
	// renderHelp's default branch renders this whole keymap through one hard
	// truncate (views.go), so position here is the only thing deciding what a
	// narrow terminal still shows. Release first, always; the scroll keys —
	// the least likely to need rescuing from a stray keystroke, and the most
	// numerous — sort last. W3.T1 asserts the truncated result at width 80.
	r[inputDetail] = modeSpec{
		mode: inputDetail,
		rules: []modeRule{
			{binding: releaseDetail, action: actionDetailUnfocus},
			{binding: keys.Quit, action: actionQuit},
			{binding: keys.Mouse, action: actionMouseToggle},
			{binding: keys.Toggle, action: actionPaneToggle},
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
			{binding: resizeToggleKey, action: actionEnterResize},
		},
		fall: routeIgnored,
		keys: modeKeyMap{bindings: []key.Binding{
			releaseDetail, keys.Quit, keys.Mouse, keys.Toggle,
			detailTabNextKey, detailTabPrevKey, detailTabJumpKey,
			detailFocusUp, detailFocusDown, detailHalfPageUp, detailHalfPageDown,
			detailPageUp, detailPageDown, detailGotoTop, detailGotoBottom,
			resizeToggleKey,
		}},
	}

	// Resize mode moves the sidebar/main divider. Like detail mode its key set
	// is ENUMERATED with an ignored fall-through: everything else is swallowed,
	// so no letter can open, clean or steal a session while the user is
	// tapping h/l. q/ctrl+c quit and ctrl+g toggles the mouse, as in detail
	// mode. ctrl+t is deliberately absent: swapping the pane and the summary
	// mid-resize changes what the user is sizing, and one ctrl+w away it works.
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
	// — see the modeAction doc). ctrl+t is deliberately ABSENT: bubbles' textarea binds it to
	// TransposeCharacterBackward, and the old ctrl+t intercept was gated on
	// !formOpen for exactly that reason (model.go:1673-1680) — here that
	// gate is simply the rule not existing in this mode.
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

	// Palette mode follows the switcher's shape exactly: ctrl+c is the only
	// parent rule, and every other key — the query text, enter, the two-stage
	// esc, up/down — is the component's, staged in palette.Update (palette.go)
	// so the gestures are implemented once. ctrl+g is deliberately absent for
	// the switcher's documented reason: the overlay replaces the pane in the
	// main region, so there is no session output behind it worth selecting.
	r[inputPalette] = modeSpec{
		mode: inputPalette,
		rules: []modeRule{
			{binding: quitChord, action: actionQuit},
		},
		fall: routeComponent,
		keys: modeKeyMap{bindings: []key.Binding{
			paletteRunKey, paletteMoveKey, paletteCloseKey, quitChord,
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
