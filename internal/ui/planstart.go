// planstart.go is the dashboard's five plan actions: extend the plan, amend
// the plan and reconcile the plan with a document, each of which starts a
// change session on the active project the way `afk plan extend`, `afk plan
// amend` and `afk plan reconcile` do (T7.3), "fix a bug", which starts the
// `afk plan fix` session on it (T13.6), and "start a new plan", which
// starts a planning session in a folder the owner types the way bare
// `afk plan` does there and moves the dashboard to that project (T7.4). Each
// opens the planning session in the agent pane.
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
//
// "Start a new plan" also takes a bare project name (T7.5). What the typed
// text means is decided by the Deps.PlanFolder seam and a new folder is made
// by Deps.CreatePlanFolder, both wired by `afk dash` and both called only
// inside a tea.Cmd. Nothing is created without the owner's y in a dialog
// showing the full path, and the create and the start are two separate
// steps, so a later question (T7.6) can go between them.
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
// PlanModeNew is "start a new plan": root is then the folder as typed (the
// start expands `~/` and refuses a relative path, so this package does
// neither), and the start behaves as bare `afk plan` does in that folder.
const (
	PlanModeResume    = ""
	PlanModeExtend    = "extend"
	PlanModeAmend     = "amend"
	PlanModeReconcile = "reconcile"
	PlanModeFix       = "fix"
	PlanModeNew       = "new"
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

// planStartUnavailableNotice is what the four change actions say when the seam is
// not wired. `afk dash` always wires it, so the owner should never read this;
// it exists so a dashboard built without it answers with a sentence and not
// a nil-function panic, as buildPauseUnavailableNotice does.
const planStartUnavailableNotice = "Starting a plan change is not available in this dashboard. Use afk plan extend, amend, reconcile or fix in a terminal."

// planNewUnavailableNotice is planStartUnavailableNotice for "start a new
// plan", which the terminal does with bare `afk plan` rather than a mode.
const planNewUnavailableNotice = "Starting a plan is not available in this dashboard. Run afk plan in the project's folder in a terminal."

// planNoProjectNotice is the actions' own guard when no project is open. The
// dialog dims the rows then, so this is only reachable from a test or a
// project that went away between opening the dialog and running the row.
const planNoProjectNotice = "No project is open, so there is no plan to change."

// The form's questions, one per mode that asks for text.
const (
	planAmendQuestion     = "What do you want to change? Say it in a sentence."
	planReconcileQuestion = "Which document? Give its path inside the project."
	planFixQuestion       = "What is broken? Say it in a sentence."
	planNewQuestion       = "Which folder? A full path or ~/..., or a new project's name."
)

// planNewNameNote is the new-plan form's second line, saying where a name
// goes. It is its own line because a dialog line is cut at the dialog's
// width (72 cells at most), and the question with this folded in would lose
// its end. It is shown only when the PlanFolder seam is wired, since without
// it a name is not created (T7.4's form).
const planNewNameNote = "A name is made into a folder under your project location, after a yes."

// The form's refusals of blank text. The form stays open with the line
// shown under the field, so the owner can type it and press enter again.
const (
	planAmendEmpty     = "Type a sentence first, or press esc to cancel."
	planReconcileEmpty = "Type the document's path first, or press esc to cancel."
	planFixEmpty       = "Type what is broken first, or press esc to cancel."
	planNewEmpty       = "Type the folder or the new project's name, or press esc to cancel."
)

// planVerbs is the word each change mode's notice uses. The new and fix
// modes have no entry: "to start the plan" and "to fix the plan" read
// wrongly (a fix mends the code, not the plan), so their notices are worded
// on their own (submitPlanText, planStartingNotice, planOpenedNotice).
var planVerbs = map[string]string{
	PlanModeExtend:    "extend",
	PlanModeAmend:     "amend",
	PlanModeReconcile: "reconcile",
}

// planStartedMsg carries one StartPlan call's result back to Update. gen is
// the project generation the action ran under, so a result that crosses a
// project switch is dropped and no pane opens in a project the owner has
// left (see Model.projectGen).
//
// lead is a line to put before every notice the result raises (T7.6). The
// GitHub question's outcome travels here rather than being shown before the
// start, because applyPlanStarted replaces the notice once the start returns:
// an outcome set first would be gone before the owner could read it. Empty
// for every start but the one after a new folder's GitHub step.
type planStartedMsg struct {
	mode string
	res  PlanStartResult
	gen  int
	lead string
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
	return m.planStartLeadCmd(root, mode, text, "")
}

// planStartLeadCmd is planStartCmd carrying a lead line for the result's
// notice (planStartedMsg.lead).
func (m Model) planStartLeadCmd(root, mode, text, lead string) tea.Cmd {
	start := m.deps.StartPlan
	gen := m.projectGen
	return func() tea.Msg {
		return planStartedMsg{mode: mode, res: start(root, mode, text), gen: gen, lead: lead}
	}
}

// withLead puts lead before text, so the GitHub step's outcome stays on
// screen in the notice that replaces it. An empty lead leaves text exactly as
// it was, which is what keeps every start without one worded as before.
func withLead(lead, text string) string {
	if lead == "" {
		return text
	}
	if text == "" {
		return lead
	}
	return lead + " " + text
}

// armPaneLead keeps lead for the master-plan pane the caller has just asked
// to open (openPlanPane), so the pane's arrival notice can carry it too. The
// lead is put before the start's notice already, but that notice lives only
// until the pane connects: applyPaneAttached then says "input goes to
// master-plan" with notify, which keeps no history, and the GitHub step's
// outcome would be on screen for the fraction of a second the dial takes
// (seen against a built binary; T7.6). Nothing is armed for an empty lead,
// and nothing when the pane is already installed: openPlanPane then only
// focuses it, no arrival follows, and the start's notice is the last word.
func (m *Model) armPaneLead(lead string) {
	if lead == "" || (m.pane != nil && m.paneName == session.PlanSessionName) {
		return
	}
	m.pendingPaneLead = lead
}

// takePaneLead returns the armed lead when name is the pane it was armed
// for, and disarms it whatever name is. It is called once per explicit
// arrival (installed or failed): the lead is said once, with the first word
// about a pane, so it cannot turn up later in front of an open of
// master-plan the owner makes by hand. An arrival for another session means
// the owner moved on, and the lead goes with it.
func (m *Model) takePaneLead(name string) string {
	lead := m.pendingPaneLead
	m.pendingPaneLead = ""
	if name != session.PlanSessionName {
		return ""
	}
	return lead
}

// withPaneLead is an arrival notice with the armed lead in front, for name's
// pane (takePaneLead). The arrival notices start lower case, being fragments
// ("input goes to master-plan · …"); after a lead, which ends in a full stop,
// the first letter is raised so the two read as sentences.
func (m *Model) withPaneLead(name, text string) string {
	return leadSentence(m.takePaneLead(name), text)
}

// leadSentence is withLead with text's first letter raised when there is a
// lead (withPaneLead).
func leadSentence(lead, text string) string {
	if lead == "" || text == "" {
		return withLead(lead, text)
	}
	return lead + " " + strings.ToUpper(text[:1]) + text[1:]
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

// openPlanTextForm is "amend the plan", "reconcile the plan with a
// document" and "fix a bug": a one-line form asking for the sentence, the
// path or what is broken, on the
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
	switch mode {
	case PlanModeReconcile:
		in.Placeholder = "a path, such as docs/design.md"
		in.CharLimit = 1024
	case PlanModeFix:
		in.Placeholder = "one sentence about what is broken"
		in.CharLimit = 1024
	default:
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

// openPlanNewForm is "start a new plan": the same one-line form, asking for
// a folder. It needs only the seam, not an active project, because the
// folder is typed: a dashboard started outside any project can start one.
//
// The field starts with the active project's path when that project is known
// to have no plan (its build has been read and there is none), since that is
// the folder the owner most likely means; the cursor goes at the end so a
// subfolder can be typed straight on. A project with a plan, or one whose
// build has not been read yet, gets an empty field: a pre-filled folder there
// would only be refused (it has a plan) or be a guess.
//
// The project generation is captured as the other forms capture it, because
// the default was the project on screen when the form opened.
func (m Model) openPlanNewForm() (tea.Model, tea.Cmd) {
	if m.deps.StartPlan == nil {
		m.notify(notifyWarn, planNewUnavailableNotice)
		return m, nil
	}
	m.modal = inputPlanText
	m.planTextMode = PlanModeNew
	m.planTextRoot = ""
	m.planTextGen = m.projectGen
	m.planTextNote = ""
	in := textinput.New()
	in.Placeholder = "a folder, such as ~/code/my-app, or a name, such as my-app"
	in.CharLimit = 4096
	if m.width > 4 {
		in.SetWidth(m.dialogContentWidth() - 4)
	}
	if def := m.planNewDefault(); def != "" {
		in.SetValue(def)
		in.CursorEnd()
	}
	m.planTextInput = in
	m.clearStatus()
	return m, m.planTextInput.Focus()
}

// planNewDefault is the new-plan form's starting text: the active project's
// path when its build is known and has no plan, else "".
func (m Model) planNewDefault() string {
	if !m.buildKnown || m.build.HasBuild {
		return ""
	}
	return m.activeProjectPath()
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
		switch m.planTextMode {
		case PlanModeReconcile:
			m.planTextNote = planReconcileEmpty
		case PlanModeNew:
			m.planTextNote = planNewEmpty
		case PlanModeFix:
			m.planTextNote = planFixEmpty
		default:
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
	if mode == PlanModeNew {
		if m.deps.PlanFolder != nil {
			// What the text means (a folder, or a name to create) is decided
			// off the drawing path, because deciding reads the profile and
			// the disk; applyPlanFolderDecided acts on the answer.
			m.notify(notifyInfo, "Checking "+text+"…")
			return m, m.planFolderCmd(text)
		}
		// The typed folder is the root and there is no text: the start looks
		// at the folder as bare `afk plan` does, which takes no brief.
		return m.startPlanNewIn(text)
	}
	m.notify(notifyInfo, planStartingNotice(mode))
	return m, m.planStartCmd(root, mode, text)
}

// planStartingNotice is the line shown while a change mode's start runs.
// Fix is worded on its own, as planVerbs says; every other mode keeps the
// "<verb> the plan" wording it always had.
func planStartingNotice(mode string) string {
	if mode == PlanModeFix {
		return "Starting a planning session to fix a bug…"
	}
	return "Starting a planning session to " + planVerbs[mode] + " the plan…"
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
//     For "start a new plan" in another project, switch to it first
//     (applyPlanNewOpened).
//   - refused because a planning session is already open: ask whether to
//     open it (inputConfirmPlanOpen). Nothing was started.
//   - any other refusal: say why, in the command's words, and open nothing.
//
// A non-empty lead goes before each of those notices (withLead).
func (m Model) applyPlanStarted(msg planStartedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen {
		// A lead is said even so: it reports something that happened on
		// GitHub (a repository made, or not), which the project switch does
		// not undo, and nothing else will ever show it.
		if msg.lead != "" {
			m.notify(notifyInfo, msg.lead)
		}
		return m, nil
	}
	res := msg.res
	switch res.Outcome {
	case PlanStarted, PlanResumed:
		if msg.mode == PlanModeNew {
			return m.applyPlanNewOpened(msg)
		}
		// The notice goes after the open, which says "opening master-plan…"
		// itself; what was started is the more useful line while the pane
		// connects. The arrival then words the pane as every connect does.
		cmd := m.openPlanPane(res.Root)
		m.notify(notifySuccess, withLead(msg.lead, planOpenedNotice(msg.mode, res)))
		m.armPaneLead(msg.lead)
		return m, tea.Batch(m.loadCmd(), cmd)
	}
	if res.Open {
		m.modal = inputConfirmPlanOpen
		m.planOffer = res
		m.planOfferGen = m.projectGen
		if m.planOffer.Root == "" {
			m.planOffer.Root = m.planRoot()
		}
		// The other modals clear the notice so a stale one does not sit
		// under the question; a lead is not stale, it is the only report of
		// the GitHub step, so it stays.
		if msg.lead != "" {
			m.notify(notifyInfo, msg.lead)
		} else {
			m.clearStatus()
		}
		return m, nil
	}
	reason := strings.TrimSpace(res.Reason)
	if reason == "" {
		reason = "The planning session could not be started."
	}
	m.notify(notifyWarn, withLead(msg.lead, reason))
	return m, nil
}

// applyPlanNewOpened is a started or resumed "start a new plan". When its
// root is another project, the dashboard moves there the way the switcher's
// enter does, and only then opens the pane: switchProjectCmds forgets the
// pane and the armed master-plan selection of the project it leaves, so an
// open made first would be undone by the switch.
//
// The root is recorded as a started project before the switch, because
// switchProjectCmds only switches to a path the list holds, and nothing else
// may list it: a folder a new plan was just started in is in neither
// config.toml nor, often, a search folder. It stays for the rest of the run
// (mergeInput.started). One scan pass is asked for when it is new, as
// applyListedResult asks for one when the listed folders change, so its row
// has session counts without waiting a minute for the scan ticker.
//
// The switch records the open (recordProjectOpen) as the jump search's does,
// since the owner chose this project. It does NOT arm previewOnNextLoad as
// the jump search does: that flag previews the remembered or first row on the
// next load, and this switch already makes an explicit open of master-plan
// (openPaneFor clears the flag for exactly that reason, as it does for a
// click-to-focus switch).
func (m Model) applyPlanNewOpened(msg planStartedMsg) (tea.Model, tea.Cmd) {
	res := msg.res
	var cmds []tea.Cmd
	switched := false
	if res.Root != "" && !m.isActiveProjectRoot(res.Root) {
		added := m.addStartedProject(res.Root)
		was := m.activeProjectPath()
		gen := m.projectGen
		m.projects = m.mergedProjects()
		if was != "" {
			// The merge can move the active row (a started row goes before the
			// pinned one), so the index is re-found by path first; the switch
			// below compares against activeProjectPath.
			var settle []tea.Cmd
			m, settle = m.settleActiveProject(was)
			cmds = append(cmds, settle...)
		}
		var switchCmds []tea.Cmd
		m, switchCmds = m.switchProjectCmds(res.Root)
		cmds = append(cmds, switchCmds...)
		// Every real switch bumps projectGen, so this is "the dashboard moved",
		// however it got there (settling can already have landed on root).
		if m.projectGen != gen && m.activeProjectPath() == res.Root {
			switched = true
			m.recordProjectOpen(res.Root)
		}
		if added {
			if scan := m.scanCmd(); scan != nil {
				cmds = append(cmds, scan)
			}
		}
	}
	if !switched {
		cmds = append(cmds, m.loadCmd())
	}
	cmds = append(cmds, m.openPlanPane(res.Root))
	m.notify(notifySuccess, withLead(msg.lead, planOpenedNotice(msg.mode, res)))
	// After the switch, never before it: switchProjectCmds drops the lead
	// along with everything else the outgoing project armed.
	m.armPaneLead(msg.lead)
	return m, tea.Batch(cmds...)
}

// isActiveProjectRoot reports whether root, a main checkout the start
// reported, is the project already on screen. Two spellings mean yes: the
// active project's own path, and, when the dashboard was started in a linked
// worktree, that worktree's main checkout (Deps.LaunchMainCheckout) while
// the start-up row is active. The second matters because the start always
// answers with the main checkout, and the start-up row keeps the worktree's
// path (see mergeProjects); taking it for another project would add a second
// row for one project and switch away from the folder the owner opened. A
// dashboard with no project list has its repo as the active project, as
// planRoot treats it.
func (m Model) isActiveProjectRoot(root string) bool {
	active := m.activeProjectPath()
	if active == "" {
		return len(m.projects) == 0 && root == m.repo
	}
	if root == active {
		return true
	}
	mc := m.deps.LaunchMainCheckout
	return mc != "" && root == mc && m.projects[m.activeProject].Pinned
}

// addStartedProject records root as a folder a new plan was started in, once,
// and reports whether it was new.
func (m *Model) addStartedProject(root string) bool {
	for _, p := range m.startedProjects {
		if p == root {
			return false
		}
	}
	m.startedProjects = append(m.startedProjects, root)
	return true
}

// planOpenedNotice words a start or a resume. The archive folder is named
// when one was used, because the earlier session's files moved there and
// the owner may go looking for them.
func planOpenedNotice(mode string, res PlanStartResult) string {
	if res.Outcome == PlanResumed {
		return "Opening the planning session that was already open."
	}
	text := "Started a planning session to " + planVerbs[mode] + " the plan."
	switch {
	case mode == PlanModeFix:
		text = "Started a planning session to fix a bug."
	case mode == PlanModeNew && res.Root != "":
		text = "Started a planning session for a new plan in " + res.Root + "."
	case planVerbs[mode] == "":
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
	switch m.planTextMode {
	case PlanModeReconcile:
		title, question = "Reconcile the plan", planReconcileQuestion
	case PlanModeNew:
		title, question = "Start a new plan", planNewQuestion
	case PlanModeFix:
		title, question = "Fix a bug", planFixQuestion
	}
	lines := []string{theme.statusLine.Render(question)}
	if m.planTextMode == PlanModeNew && m.deps.PlanFolder != nil {
		lines = append(lines, theme.dim.Render(planNewNameNote))
	}
	lines = append(lines, m.planTextInput.View())
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

// startPlanNewIn is T7.4's start of a new plan in a folder: the folder is the
// root and there is no text, because the start looks at the folder as bare
// `afk plan` does, which takes no brief. A typed path, a folder a name was
// just turned into, and an existing folder the owner chose to plan in all go
// through here, so they read and behave the same.
func (m Model) startPlanNewIn(folder string) (tea.Model, tea.Cmd) {
	m.notify(notifyInfo, "Starting a planning session in "+folder+"…")
	return m, m.planStartCmd(folder, PlanModeNew, "")
}

// PlanFolderKind is what the new-plan form's text means (T7.5).
type PlanFolderKind int

const (
	// PlanFolderRefused is the zero value on purpose, as PlanRefused is: a
	// decision the seam forgot to fill creates nothing and starts nothing.
	PlanFolderRefused PlanFolderKind = iota
	// PlanFolderUse means the text is a folder (a full or ~/ path) to start
	// in as typed, exactly as T7.4 did before names existed.
	PlanFolderUse
	// PlanFolderCreate means the text is a new project's name and Path is the
	// folder under the profile's project location it would become.
	PlanFolderCreate
)

// PlanFolderDecision is what a PlanFolder call reports. It mirrors
// internal/planfolder's Decision, declared here because this package must
// not import that one (the two are built and tested apart, and `afk dash`
// maps one into the other).
type PlanFolderDecision struct {
	Kind PlanFolderKind
	// Path is the folder to use or create. For a refusal it is set only with
	// Exists.
	Path string
	// Reason is why the text was refused, as a plain sentence. Empty unless
	// refused.
	Reason string
	// Exists is set on a refusal because a folder of that name is already
	// there. The dashboard then offers to plan in it instead of only saying
	// no, because that is almost always what the owner wants next.
	Exists bool
}

// planFolderDecidedMsg carries one PlanFolder call's answer back to Update,
// with the project generation it was asked under, as planStartedMsg does.
type planFolderDecidedMsg struct {
	text string
	d    PlanFolderDecision
	gen  int
}

// planFolderCreatedMsg carries one CreatePlanFolder call's result back to
// Update.
type planFolderCreatedMsg struct {
	path string
	err  error
	gen  int
}

// planFolderUnavailableNotice is the y's answer when CreatePlanFolder is not
// wired. `afk dash` always wires it with PlanFolder; this exists so a
// dashboard built with one and not the other says so and creates nothing.
const planFolderUnavailableNotice = "Creating a project folder is not available in this dashboard. Nothing was created. Type the full path of a folder that exists instead."

// planFolderNothingCreated is the confirm's no and esc.
const planFolderNothingCreated = "Nothing was created."

// planFolderCmd asks the PlanFolder seam what text means, off the drawing
// path.
func (m Model) planFolderCmd(text string) tea.Cmd {
	decide := m.deps.PlanFolder
	gen := m.projectGen
	return func() tea.Msg {
		return planFolderDecidedMsg{text: text, d: decide(text), gen: gen}
	}
}

// applyPlanFolderDecided acts on what the typed text means.
//
//   - use: start in it as T7.4 does.
//   - create: ask y/n, showing the full path, before anything is made.
//   - refused because the folder exists: ask whether to plan in it; its yes
//     only starts, and creates nothing.
//   - any other refusal: say why and do nothing.
//
// A decision that crosses a project switch is dropped, as a start result is:
// the owner has moved on, and a dialog popping up over another project
// would be about something they are no longer looking at.
func (m Model) applyPlanFolderDecided(msg planFolderDecidedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen {
		return m, nil
	}
	d := msg.d
	switch {
	case d.Kind == PlanFolderUse:
		folder := d.Path
		if folder == "" {
			folder = msg.text
		}
		return m.startPlanNewIn(folder)
	case d.Kind == PlanFolderCreate && d.Path != "",
		d.Kind == PlanFolderRefused && d.Exists && d.Path != "":
		m.modal = inputConfirmPlanFolder
		m.planFolder = d
		m.planFolderGen = m.projectGen
		m.clearStatus()
		return m, nil
	}
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "That cannot be used as a folder for a new plan. Nothing was created."
	}
	m.notify(notifyWarn, reason)
	return m, nil
}

// confirmPlanFolder is the folder question's y. For a folder that exists it
// only starts planning there. For a new name it creates the folder first,
// and planning starts only once the create has succeeded
// (applyPlanFolderCreated): create and start stay two separate steps, so a
// later question can be asked between them.
func (m Model) confirmPlanFolder() (tea.Model, tea.Cmd) {
	d, gen := m.planFolder, m.planFolderGen
	m.clearPlanFolder()
	if gen != m.projectGen {
		m.notify(notifyWarn, "The project changed, so nothing was created.")
		return m, nil
	}
	if d.Kind != PlanFolderCreate {
		return m.startPlanNewIn(d.Path)
	}
	if m.deps.CreatePlanFolder == nil {
		m.notify(notifyWarn, planFolderUnavailableNotice)
		return m, nil
	}
	m.notify(notifyInfo, "Creating "+d.Path+"…")
	create := m.deps.CreatePlanFolder
	path := d.Path
	genNow := m.projectGen
	return m, func() tea.Msg {
		return planFolderCreatedMsg{path: path, err: create(path), gen: genNow}
	}
}

// applyPlanFolderCreated is the create's result. An error is shown as the
// seam wrote it (it says what was left on disk) and nothing starts. On
// success the folder is announced and planning starts in it.
//
// Unlike a decision, a result that crosses a project switch is not dropped
// silently: the folder now exists whatever the owner did meanwhile, so they
// are told, but no planning session starts, since starting one would move
// the dashboard off the project they switched to.
func (m Model) applyPlanFolderCreated(msg planFolderCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notify(notifyWarn, msg.err.Error())
		return m, nil
	}
	made := planFolderMade(msg.path)
	if msg.gen != m.projectGen {
		m.notify(notifyInfo, made+" "+planFolderNotStarted)
		return m, nil
	}
	if m.deps.GitHubOffer != nil {
		// T7.6: the GitHub question goes between the create and the start.
		// Whether to ask runs gh, so it is found out off the drawing path;
		// applyGitHubOffer acts on the answer and every branch of it starts
		// planning (or says why it did not).
		m.notify(notifyInfo, made+" Checking GitHub…")
		offer := m.deps.GitHubOffer
		path := msg.path
		gen := m.projectGen
		return m, func() tea.Msg {
			return githubOfferMsg{path: path, offer: offer(path), gen: gen}
		}
	}
	// One notice for both steps rather than startPlanNewIn's, which would
	// replace "made" before the owner could read it.
	m.notify(notifySuccess, made+" Starting a planning session in it…")
	return m, m.planStartCmd(msg.path, PlanModeNew, "")
}

// planFolderMade is the first half of every notice after a folder was made.
func planFolderMade(path string) string {
	return "Made " + path + " and made it a git repository."
}

// planFolderNotStarted ends a notice for a step that finished after the
// owner switched project: the folder exists, but planning is not started in
// it, since starting would move the dashboard off the project they chose.
const planFolderNotStarted = "The project changed, so no planning session was started; type the full path to plan in it."

// cancelPlanFolder is the folder question's n and esc: nothing is created
// and nothing starts.
func (m Model) cancelPlanFolder() (tea.Model, tea.Cmd) {
	m.clearPlanFolder()
	m.notify(notifyInfo, planFolderNothingCreated)
	return m, nil
}

func (m *Model) clearPlanFolder() {
	m.modal = inputNormal
	m.planFolder = PlanFolderDecision{}
	m.planFolderGen = 0
}

// planFolderDialog is the folder question (inputConfirmPlanFolder). The full
// path is on a line of its own so a long one is cut at the dialog's edge
// rather than mid-sentence, and the owner sees exactly where the folder
// will go before saying yes.
func (m Model) planFolderDialog() dialog {
	reg := m.modes()
	hint := modalKeyHint(reg.spec(inputConfirmPlanFolder).keys.bindings, "yes", "cancel")
	if m.planFolder.Kind != PlanFolderCreate {
		return dialog{
			title: "Folder already exists",
			lines: []string{
				theme.statusLine.Render("This folder already exists:"),
				theme.statusLine.Render(m.planFolder.Path),
				theme.dim.Render("Nothing will be created. Plan in it?"),
			},
			hint: hint,
		}
	}
	return dialog{
		title: "Create a new project",
		lines: []string{
			theme.statusLine.Render("Create this folder?"),
			theme.statusLine.Render(m.planFolder.Path),
			theme.dim.Render("It will be made a git repository, then planning starts in it."),
		},
		hint: hint,
	}
}

// GitHubOffer is what a GitHubOffer call reports (T7.6): whether to ask the
// owner about a GitHub repository for a folder just made, and, when not, why
// not. It mirrors the decision in internal/ghrepo, declared here because this
// package must not run gh or import the package that does; `afk dash` maps
// one into the other, as it does PlanFolderDecision.
type GitHubOffer struct {
	// Ask is set when gh is installed and logged in, so the question can be
	// asked. It is asked only with a Repo to name.
	Ask bool
	// Repo is the repository the question names, as <account>/<folder name>.
	Repo string
	// Notice is why nothing was asked and what to run instead, as a plain
	// sentence. It leads the planning notice when Ask is false.
	Notice string
}

// githubOfferMsg carries one GitHubOffer call's answer back to Update with
// the project generation the folder's create arrived under.
type githubOfferMsg struct {
	path  string
	offer GitHubOffer
	gen   int
}

// githubRepoCreatedMsg carries one CreateGitHubRepo call's outcome line back
// to Update.
type githubRepoCreatedMsg struct {
	path string
	line string
	gen  int
}

// githubRepoDeclined is the question's n and esc. It is internal/ghrepo's
// declined wording, kept here as its own constant because this package cannot
// import that one; the two must say the same thing.
const githubRepoDeclined = "No GitHub repository was made."

// githubRepoUnavailable is the y's lead when CreateGitHubRepo is not wired.
// `afk dash` wires it with GitHubOffer; this exists so a dashboard built with
// one and not the other says so, makes nothing, and still starts planning.
const githubRepoUnavailable = "Making a GitHub repository is not available in this dashboard, so none was made."

// githubRepoNoAnswer stands in for an empty outcome line, so the owner is
// never left without word of what the GitHub step did.
const githubRepoNoAnswer = "Could not tell whether the GitHub repository was made. Check GitHub before making one by hand."

// applyGitHubOffer acts on whether to ask about a GitHub repository.
//
//   - the project changed meanwhile: say the folder was made, as T7.5 does,
//     and start nothing.
//   - ask, with a repository to name: raise inputConfirmGitHubRepo.
//   - anything else (gh missing, logged out, no account, or an Ask with no
//     Repo, which a badly filled offer could send): start planning with the
//     offer's Notice as the lead, since the question is optional and planning
//     is what the owner asked for.
func (m Model) applyGitHubOffer(msg githubOfferMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen {
		m.notify(notifyInfo, planFolderMade(msg.path)+" "+planFolderNotStarted)
		return m, nil
	}
	o := msg.offer
	if o.Ask && strings.TrimSpace(o.Repo) != "" {
		m.modal = inputConfirmGitHubRepo
		m.githubPath = msg.path
		m.githubRepo = o.Repo
		m.githubGen = m.projectGen
		m.clearStatus()
		return m, nil
	}
	return m.startPlanAfterGitHub(msg.path, strings.TrimSpace(o.Notice))
}

// startPlanAfterGitHub starts planning in the new folder with lead, the
// GitHub step's one line, in front of the notices: the interim one now, and
// the result's through planStartedMsg.lead.
//
// With a lead the interim line leaves out the path: the toast is cut at three
// lines of 48 cells, and a lead followed by a path would push the path (or,
// with a long gh reason, the end of the lead) off it. The folder is named
// again in the started notice and in the pane's title.
func (m Model) startPlanAfterGitHub(path, lead string) (tea.Model, tea.Cmd) {
	interim := "Starting a planning session in " + path + "…"
	if lead != "" {
		interim = "Starting the planning session…"
	}
	m.notify(notifyInfo, withLead(lead, interim))
	return m, m.planStartLeadCmd(path, PlanModeNew, "", lead)
}

// confirmGitHubRepo is the question's y. The repository is made only here,
// and only inside a tea.Cmd, because it runs gh and git. Planning starts once
// the outcome is back (applyGitHubRepoCreated), whatever it is.
func (m Model) confirmGitHubRepo() (tea.Model, tea.Cmd) {
	path, repo, gen := m.githubPath, m.githubRepo, m.githubGen
	m.clearGitHubRepo()
	if gen != m.projectGen {
		m.notify(notifyWarn, "The project changed, so no GitHub repository was made.")
		return m, nil
	}
	if m.deps.CreateGitHubRepo == nil {
		return m.startPlanAfterGitHub(path, githubRepoUnavailable)
	}
	m.notify(notifyInfo, "Making "+repo+" on GitHub…")
	create := m.deps.CreateGitHubRepo
	genNow := m.projectGen
	return m, func() tea.Msg {
		return githubRepoCreatedMsg{path: path, line: create(path, repo), gen: genNow}
	}
}

// cancelGitHubRepo is the question's n and esc: nothing is made on GitHub,
// and planning starts as it would have without the question.
func (m Model) cancelGitHubRepo() (tea.Model, tea.Cmd) {
	path := m.githubPath
	m.clearGitHubRepo()
	return m.startPlanAfterGitHub(path, githubRepoDeclined)
}

// applyGitHubRepoCreated is the create's outcome line. A failure is a line
// like any other: the folder is fine without a remote, so planning starts
// either way. Across a project switch the line is still said, since the
// repository exists (or not) whatever the owner did meanwhile, but planning
// is not started, on applyPlanFolderCreated's terms.
func (m Model) applyGitHubRepoCreated(msg githubRepoCreatedMsg) (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(msg.line)
	if line == "" {
		line = githubRepoNoAnswer
	}
	if msg.gen != m.projectGen {
		m.notify(notifyInfo, line+" "+planFolderNotStarted)
		return m, nil
	}
	return m.startPlanAfterGitHub(msg.path, line)
}

func (m *Model) clearGitHubRepo() {
	m.modal = inputNormal
	m.githubPath = ""
	m.githubRepo = ""
	m.githubGen = 0
}

// githubRepoDialog is the GitHub question (inputConfirmGitHubRepo). The
// repository is on a line of its own, as planFolderDialog puts the path, so
// a dialog line cut at its width (72 cells) loses none of the question and
// the owner sees the account and name in full before saying yes. It says n
// changes nothing, because enter is not bound: the default answer is no.
func (m Model) githubRepoDialog() dialog {
	reg := m.modes()
	return dialog{
		title: "Create a GitHub repository?",
		lines: []string{
			theme.statusLine.Render("Make this private repository on GitHub and set it as origin?"),
			theme.statusLine.Render(m.githubRepo),
			theme.dim.Render("It will be private. Nothing is pushed. n changes nothing."),
		},
		hint: modalKeyHint(reg.spec(inputConfirmGitHubRepo).keys.bindings, "yes", "no"),
	}
}
