// planstart.go is the dashboard's three plan actions: extend the plan, amend
// the plan and reconcile the plan with a document. Each starts a change
// session on the active project the way `afk plan extend`, `afk plan amend`
// and `afk plan reconcile` do, and opens it in the agent pane (T7.3).
//
// Nothing here decides whether a session may start, and nothing here writes a
// file. The actions call the Deps.StartPlan seam, which `afk dash` wires to
// the same start path the command runs, so the dashboard and the terminal
// refuse for the same reasons in the same words and write the same files.
// This package cannot import the command package, which is why the seam
// exists and why PlanStartResult is declared here and filled in there.
//
// The seam is called from inside a tea.Cmd and never from Update or View, on
// the discipline answerInboxCmd documents: a start reads and writes the
// project's files and spawns a host, and none of that may stall the screen.
//
// Each action calls the seam exactly once. There is no pre-check for an open
// planning session (docs/decisions.md "## T7.3", A6): the start itself finds
// one and refuses, and only then does the dashboard ask whether to open it,
// whose yes is a second, separate request that only opens.
package ui

import (
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/session"
)

// The modes the StartPlan seam takes. They are the words `afk plan` takes
// after it, so the adapter in `afk dash` passes them on unchanged.
// PlanModeResume is the request with no mode: open a planning session that is
// already open, and never start a new one.
const (
	PlanModeResume    = ""
	PlanModeExtend    = "extend"
	PlanModeAmend     = "amend"
	PlanModeReconcile = "reconcile"
)

// PlanStartOutcome is what one StartPlan call did.
type PlanStartOutcome int

const (
	// PlanRefused is the zero value on purpose: a result the seam forgot to
	// fill opens no pane, which is the safe direction.
	PlanRefused PlanStartOutcome = iota
	// PlanStarted means a new change session was seeded and its host spawned.
	PlanStarted
	// PlanResumed means a planning session that was already open is running:
	// its host was alive, or was spawned again on the saved state.
	PlanResumed
)

// PlanStartResult is what a StartPlan call reports. It mirrors what the
// start path in the command package returns, declared here because that
// package cannot be imported from this one.
type PlanStartResult struct {
	Outcome PlanStartOutcome
	// Reason is why the start was refused, in the command's own words
	// without its "afk plan <mode>:" prefix. Empty unless refused.
	Reason string
	// Open is true when the refusal is "a planning session is already open".
	// The dashboard then offers to open that session instead of only saying
	// no, because opening it is almost always what the owner wants next.
	Open bool
	// OpenMode and OpenPhase describe the open session (its mode, empty for
	// a new plan, and the phase it is at), for the offer's wording.
	OpenMode  string
	OpenPhase string
	// Root is the project's main checkout, where the planning session's
	// host runs and where its files live. The pane is opened from it.
	Root string
	// Archive is the folder an earlier planning session's files were moved
	// to before this one started, or "" when there were none.
	Archive string
}

// planStartUnavailableNotice is what the three actions say when the seam is
// not wired. `afk dash` always wires it, so the owner should never read this;
// it exists so a dashboard built without it answers with a sentence and not
// a nil-function panic, as buildPauseUnavailableNotice does.
const planStartUnavailableNotice = "Starting a plan change is not available in this dashboard. Use afk plan extend, amend or reconcile in a terminal."

// planNoProjectNotice is the actions' own guard when no project is open. The
// dialog dims the rows then, so this is only reachable from a test or a
// project that went away between opening the dialog and running the row.
const planNoProjectNotice = "No project is open, so there is no plan to change."

// The form's questions, one per mode that asks for text.
const (
	planAmendQuestion     = "What do you want to change? Say it in a sentence."
	planReconcileQuestion = "Which document? Give its path inside the project."
)

// The form's refusals of blank text. The form stays open with the line
// shown under the field, so the owner can type it and press enter again.
const (
	planAmendEmpty     = "Type a sentence first, or press esc to cancel."
	planReconcileEmpty = "Type the document's path first, or press esc to cancel."
)

// planVerbs is the word each mode's notice uses.
var planVerbs = map[string]string{
	PlanModeExtend:    "extend",
	PlanModeAmend:     "amend",
	PlanModeReconcile: "reconcile",
}

// planStartedMsg carries one StartPlan call's result back to Update. gen is
// the project generation the action ran under, so a result that crosses a
// project switch is dropped and no pane opens in a project the owner has
// left (see Model.projectGen).
type planStartedMsg struct {
	mode string
	res  PlanStartResult
	gen  int
}

// planRoot is the folder the seam is given: the active project's path, or
// the repo the dashboard was started in when there is no project list. The
// seam finds the main checkout itself, as `afk plan` does from any folder of
// the project.
func (m Model) planRoot() string {
	if p := m.activeProjectPath(); p != "" {
		return p
	}
	return m.repo
}

// planStartCmd calls the seam off the drawing path. It is the only place the
// seam is called, so "each action calls it once" is a property of the
// callers alone.
func (m Model) planStartCmd(root, mode, text string) tea.Cmd {
	start := m.deps.StartPlan
	gen := m.projectGen
	return func() tea.Msg {
		return planStartedMsg{mode: mode, res: start(root, mode, text), gen: gen}
	}
}

// planActionGuard is the check every action makes before anything else. It
// returns false, with the notice already raised, when the action cannot run.
func (m *Model) planActionGuard() bool {
	if !m.hasActiveProject() {
		m.notify(notifyInfo, planNoProjectNotice)
		return false
	}
	if m.deps.StartPlan == nil {
		m.notify(notifyWarn, planStartUnavailableNotice)
		return false
	}
	return true
}

// startPlanExtend is "extend the plan". It asks for nothing, so it calls the
// seam straight away.
func (m Model) startPlanExtend() (tea.Model, tea.Cmd) {
	if !m.planActionGuard() {
		return m, nil
	}
	m.notify(notifyInfo, "Starting a planning session to extend the plan…")
	return m, m.planStartCmd(m.planRoot(), PlanModeExtend, "")
}

// openPlanTextForm is "amend the plan" and "reconcile the plan with a
// document": a one-line form asking for the sentence or the path, on the
// label form's pattern (openLabelForm). The root and the project generation
// are captured now, because the text typed is about the project on screen
// now; a project switch while the form is up makes submitPlanText refuse.
func (m Model) openPlanTextForm(mode string) (tea.Model, tea.Cmd) {
	if !m.planActionGuard() {
		return m, nil
	}
	m.modal = inputPlanText
	m.planTextMode = mode
	m.planTextRoot = m.planRoot()
	m.planTextGen = m.projectGen
	m.planTextNote = ""
	in := textinput.New()
	if mode == PlanModeReconcile {
		in.Placeholder = "a path, such as docs/design.md"
		in.CharLimit = 1024
	} else {
		in.Placeholder = "one sentence about the change"
		in.CharLimit = 1024
	}
	if m.width > 4 {
		// The dialog's content width less the input's "> " prompt and a cell
		// of slack, as openLabelForm sizes its field.
		in.SetWidth(m.dialogContentWidth() - 4)
	}
	m.planTextInput = in
	m.clearStatus()
	return m, m.planTextInput.Focus()
}

// clearPlanText drops everything the form captured.
func (m *Model) clearPlanText() {
	m.modal = inputNormal
	m.planTextMode = ""
	m.planTextRoot = ""
	m.planTextGen = 0
	m.planTextNote = ""
}

// submitPlanText is the form's enter. Blank text keeps the form open with a
// plain line saying what is missing and calls nothing: the command refuses an
// empty sentence too, and a refusal toast after the form closed would make
// the owner open it and type everything again.
func (m Model) submitPlanText() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.planTextInput.Value())
	if text == "" {
		if m.planTextMode == PlanModeReconcile {
			m.planTextNote = planReconcileEmpty
		} else {
			m.planTextNote = planAmendEmpty
		}
		return m, nil
	}
	mode, root, gen := m.planTextMode, m.planTextRoot, m.planTextGen
	m.clearPlanText()
	if gen != m.projectGen {
		m.notify(notifyWarn, "The project changed while the form was open, so nothing was started.")
		return m, nil
	}
	m.notify(notifyInfo, "Starting a planning session to "+planVerbs[mode]+" the plan…")
	return m, m.planStartCmd(root, mode, text)
}

// cancelPlanText is the form's esc: it closes with nothing started.
func (m Model) cancelPlanText() (tea.Model, tea.Cmd) {
	m.clearPlanText()
	m.notify(notifyInfo, "Nothing was started.")
	return m, nil
}

// applyPlanStarted folds a StartPlan result into the Model.
//
//   - started or resumed: open the planning session's pane (openPlanPane).
//   - refused because a planning session is already open: ask whether to
//     open it (inputConfirmPlanOpen). Nothing was started.
//   - any other refusal: say why, in the command's words, and open nothing.
func (m Model) applyPlanStarted(msg planStartedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen {
		return m, nil
	}
	res := msg.res
	switch res.Outcome {
	case PlanStarted, PlanResumed:
		// The notice goes after the open, which says "opening master-plan…"
		// itself; what was started is the more useful line while the pane
		// connects. The arrival then words the pane as every connect does.
		cmd := m.openPlanPane(res.Root)
		m.notify(notifySuccess, planOpenedNotice(msg.mode, res))
		return m, tea.Batch(m.loadCmd(), cmd)
	}
	if res.Open {
		m.modal = inputConfirmPlanOpen
		m.planOffer = res
		m.planOfferGen = m.projectGen
		if m.planOffer.Root == "" {
			m.planOffer.Root = m.planRoot()
		}
		m.clearStatus()
		return m, nil
	}
	reason := strings.TrimSpace(res.Reason)
	if reason == "" {
		reason = "The planning session could not be started."
	}
	m.notify(notifyWarn, reason)
	return m, nil
}

// planOpenedNotice words a start or a resume. The archive folder is named
// when one was used, because the earlier session's files moved there and
// the owner may go looking for them.
func planOpenedNotice(mode string, res PlanStartResult) string {
	if res.Outcome == PlanResumed {
		return "Opening the planning session that was already open."
	}
	text := "Started a planning session to " + planVerbs[mode] + " the plan."
	if planVerbs[mode] == "" {
		text = "Started a planning session."
	}
	if res.Archive != "" {
		text += " The last planning session's files were moved to " + res.Archive + "."
	}
	return text
}

// openPlanPane selects the master-plan row and opens its pane, as enter on
// that row does (connect). The planning session's host runs in the project's
// main checkout, so root is its worktree.
//
// The row is often not listed yet: a host spawned a moment ago is found by
// the next discovery pass, not this one. The open does not wait for it — a
// dial needs only the worktree, as the post-create open in the
// spawnFinishedMsg case shows — and the selection is armed instead
// (planSelectPending), so the row is selected the moment discovery lists it
// and the pane, which is drawn only for the selected row, comes on screen.
func (m *Model) openPlanPane(root string) tea.Cmd {
	name := session.PlanSessionName
	req := paneOpenRequest{name: name, worktree: root}
	if i, ok := m.sessionIndex(name); ok {
		m.selectPlanRow(i)
		if wt := m.sessions[i].WorktreePath; wt != "" {
			req.worktree = wt
		}
		req.scratch = m.sessions[i].Scratch
	} else {
		m.planSelectPending = name
	}
	// Already installed: there is nothing to dial, only to show and focus. A
	// second connection would be opened and then discarded as surplus.
	if m.pane != nil && m.paneName == name {
		m.showAndFocusPane()
		return nil
	}
	return m.connect(req)
}

// selectPlanRow puts the cursor on the row at index i, as a click-to-focus
// request does (applyFocusRequest): an applied filter that hides the row is
// cleared, because the owner asked for this session by name, and the row is
// remembered. Not schedulePreview, for the click's reason: a preview would
// dial the session the open is already dialing.
func (m *Model) selectPlanRow(i int) {
	if m.list.FilterState() != list.Unfiltered || m.list.SettingFilter() {
		m.list.ResetFilter()
	}
	m.selectSessionIndex(i)
	m.resetDetailScroll()
	m.rememberSelectedRow()
}

// applyPlanSelectPending is the armed selection, run on every successful
// discovery load. It is kept only while the right side has the keyboard,
// which is where the open left it: once the owner goes back to the list they
// have chosen a row of their own, and a later load must not move the cursor
// off it.
func (m *Model) applyPlanSelectPending() {
	if m.planSelectPending == "" {
		return
	}
	if m.focus != focusRight {
		m.planSelectPending = ""
		return
	}
	if i, ok := m.sessionIndex(m.planSelectPending); ok {
		m.planSelectPending = ""
		m.selectPlanRow(i)
	}
}

// confirmPlanOpen is the open-it offer's y: a second request with no mode,
// which only opens the session that is open and never starts a new one.
func (m Model) confirmPlanOpen() (tea.Model, tea.Cmd) {
	root, gen := m.planOffer.Root, m.planOfferGen
	m.clearPlanOffer()
	if gen != m.projectGen {
		m.notify(notifyWarn, "The project changed, so nothing was opened.")
		return m, nil
	}
	m.notify(notifyInfo, "Opening the planning session…")
	return m, m.planStartCmd(root, PlanModeResume, "")
}

// cancelPlanOpen is the offer's n and esc: nothing is opened or started.
func (m Model) cancelPlanOpen() (tea.Model, tea.Cmd) {
	m.clearPlanOffer()
	return m, nil
}

func (m *Model) clearPlanOffer() {
	m.modal = inputNormal
	m.planOffer = PlanStartResult{}
	m.planOfferGen = 0
}

// planOpenDescription is the open session as the offer names it: its mode
// and phase when the start reported them.
func planOpenDescription(r PlanStartResult) string {
	var parts []string
	if r.OpenMode != "" {
		parts = append(parts, "mode "+r.OpenMode)
	} else {
		parts = append(parts, "a new plan")
	}
	if r.OpenPhase != "" {
		parts = append(parts, "at "+r.OpenPhase)
	}
	return strings.Join(parts, ", ")
}

// planTextDialog is the form (inputPlanText): the question, the field, and
// under it the line saying what is missing after a blank enter. The line
// takes a row only once there is something to say, so a fresh form is the
// question and the field alone.
func (m Model) planTextDialog() dialog {
	title, question := "Amend the plan", planAmendQuestion
	if m.planTextMode == PlanModeReconcile {
		title, question = "Reconcile the plan", planReconcileQuestion
	}
	lines := []string{
		theme.statusLine.Render(question),
		m.planTextInput.View(),
	}
	if m.planTextNote != "" {
		lines = append(lines, theme.errorText.Render(m.planTextNote))
	}
	reg := m.modes()
	return dialog{
		title: title,
		lines: lines,
		hint:  confirmModalKeyHint(reg.spec(inputPlanText).keys.bindings, "start"),
	}
}

// planOpenDialog is the open-it offer (inputConfirmPlanOpen): the start was
// refused because a planning session is already open, and y opens that one.
func (m Model) planOpenDialog() dialog {
	reg := m.modes()
	return dialog{
		title: "Planning session open",
		lines: []string{
			theme.statusLine.Render("A planning session is already open (" + planOpenDescription(m.planOffer) + ")."),
			theme.dim.Render("Nothing new was started. Open the one that is open?"),
		},
		hint: modalKeyHint(reg.spec(inputConfirmPlanOpen).keys.bindings, "open it", "cancel"),
	}
}
