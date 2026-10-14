// planapprove.go is the dashboard's "approve the plan" action (T7.8): the
// by-name leader row, the y/n confirm it raises, and the result it shows.
//
// Nothing here writes a file or runs git. The work is Deps.ApprovePlan, which
// `afk dash` wires to the same steps `afk plan approve` runs, so the action and
// the command cannot drift apart. This package cannot import the command
// package, which is why the seam exists and why PlanApproveResult is declared
// here and filled in there.
//
// The seam commits and pushes, so it is called once per y and only inside the
// tea.Cmd approvePlanCmd returns (messages.go), on the discipline
// answerInboxCmd documents: a push can take seconds, and the screen must not
// wait for it.
package ui

import (
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// PlanApproveResult is what one approve did, as the dashboard shows it. The
// root package words it (Text) from the entry point's structured result,
// because only it knows the steps; the dashboard only picks how loudly to
// say it from Outcome.
type PlanApproveResult struct {
	// Outcome is one of the PlanApprove* constants.
	Outcome string
	// Text is the plain-language notice, shown as it is.
	Text string
}

// The outcomes Deps.ApprovePlan reports. They are strings, not an enum, so the
// root package's adapter reads as plainly as the notices it writes.
const (
	// PlanApproveApproved: every step ran; the plan is a build now.
	PlanApproveApproved = "approved"
	// PlanApproveAlready: the plan had been approved before; nothing changed.
	PlanApproveAlready = "already"
	// PlanApproveRefused: the command refused before changing anything.
	PlanApproveRefused = "refused"
	// PlanApproveFailed: a step failed; the ones before it are done, and a
	// second approve finishes the rest.
	PlanApproveFailed = "failed"
)

// The approve's own notices.
const (
	// planApprovingNotice is shown from y until the result is back. The
	// spinner on the help row (hasInFlightWork) carries on after the toast
	// has gone, so a slow push is never mistaken for a finished one.
	planApprovingNotice = "Approving the plan: committing and pushing…"
	// planApproveRunningNotice answers a second approve while one is out:
	// a second commit and push of the same plan would only race the first.
	planApproveRunningNotice = "The plan is already being approved. Wait for it to finish."
	// planApproveUnavailableNotice is y's answer when ApprovePlan is not
	// wired. The row is dimmed then, so only a run that reaches the action
	// some other way says this.
	planApproveUnavailableNotice = "Approving the plan is not available in this dashboard. Run afk plan approve instead."
	// planApproveProjectChanged answers a y given after the project changed
	// under the open confirm: the dialog named the old project.
	planApproveProjectChanged = "The project changed, so the plan was not approved."
	// planApproveNoAnswer stands in for an empty result text, so the owner
	// is never left without word of what the approve did.
	planApproveNoAnswer = "Could not tell whether the plan was approved. Run afk plan status to see where it is."
)

// openPlanApprove is the "approve the plan" action. It raises the confirm and
// does nothing else: no file is read and no seam is called. The status shown
// is the one the refresh last stored, copied so the counts the owner reads
// are the counts y acts on.
//
// It asks planApproveBlockedReason, the function that dims the row, so a run
// that reaches the action some other way gets the same reason in a sentence.
// The in-flight check comes first: while an approve is out the plan is moving
// to approved, and "already being approved" is the true answer.
//
// The root is m.repo, the folder LoadPlanStatus read the status from, so the
// approve resolves the project exactly as the status did.
func (m Model) openPlanApprove() (tea.Model, tea.Cmd) {
	if m.planApproving {
		m.notify(notifyInfo, planApproveRunningNotice)
		return m, nil
	}
	if reason := planApproveBlockedReason(m.leaderConditions()); reason != "" {
		m.notify(notifyInfo, "The plan cannot be approved: "+reason+".")
		return m, nil
	}
	m.modal = inputConfirmPlanApprove
	m.planApprove = m.planStatus
	m.planApproveRoot = m.repo
	m.planApproveGen = m.projectGen
	m.clearStatus()
	return m, nil
}

// confirmPlanApprove is the confirm's y. Plans with errors never get here
// through the keyboard (Model.modes drops the y), and the check below keeps
// it so for any other route: the dialog stays up, saying why, and nothing is
// called.
func (m Model) confirmPlanApprove() (tea.Model, tea.Cmd) {
	if m.planApprove.Summary.Errors > 0 {
		return m, nil
	}
	root, gen := m.planApproveRoot, m.planApproveGen
	m.clearPlanApprove()
	if gen != m.projectGen {
		m.notify(notifyWarn, planApproveProjectChanged)
		return m, nil
	}
	if m.planApproving {
		m.notify(notifyInfo, planApproveRunningNotice)
		return m, nil
	}
	approve := m.deps.ApprovePlan
	if approve == nil {
		m.notify(notifyWarn, planApproveUnavailableNotice)
		return m, nil
	}
	m.planApproving = true
	m.notify(notifyInfo, planApprovingNotice)
	return m, approvePlanCmd(approve, root, gen)
}

// cancelPlanApprove is the confirm's n and esc: nothing was done, so nothing
// is said.
func (m Model) cancelPlanApprove() (tea.Model, tea.Cmd) {
	m.clearPlanApprove()
	return m, nil
}

// clearPlanApprove closes the confirm and forgets what it was raised for.
// planApproving is not touched: it belongs to the approve, not the dialog.
func (m *Model) clearPlanApprove() {
	m.modal = inputNormal
	m.planApprove = PlanStatus{}
	m.planApproveRoot = ""
	m.planApproveGen = 0
}

// applyPlanApproved folds one approve's result into the Model. The in-flight
// flag is cleared whatever happens, the approve being over either way. A
// result for a project the dashboard has since left is dropped without a
// word, as a build read is, and the plan status is read again otherwise so
// the plan line says approved without waiting for the next tick.
func (m Model) applyPlanApproved(msg planApprovedMsg) (tea.Model, tea.Cmd) {
	m.planApproving = false
	if msg.gen != m.projectGen || msg.root != m.repo {
		return m, nil
	}
	kind, text := planApproveNotice(msg.result)
	m.notify(kind, text)
	return m, m.planStatusCmd()
}

// planApproveNotice picks how loudly to say a result. Text is the root
// package's wording; an empty one is replaced so the owner always hears back.
// An outcome this package does not know is told as information, which says
// it without claiming success or failure.
func planApproveNotice(r PlanApproveResult) (notificationKind, string) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return notifyWarn, planApproveNoAnswer
	}
	switch r.Outcome {
	case PlanApproveApproved:
		return notifySuccess, text
	case PlanApproveRefused:
		return notifyWarn, text
	case PlanApproveFailed:
		return notifyError, text
	}
	return notifyInfo, text
}

// withoutPlanApproveYes is the approve confirm's spec with its y taken out:
// the confirm rule, and every binding with that rule's keys. Model.modes uses
// it while the plan has errors.
func withoutPlanApproveYes(s modeSpec) modeSpec {
	var yes []string
	rules := make([]modeRule, 0, len(s.rules))
	for _, r := range s.rules {
		if r.action == actionConfirm {
			yes = r.binding.Keys()
			continue
		}
		rules = append(rules, r)
	}
	// A fresh slice, not s.keys.bindings[:0]: the spec came from a registry
	// built for this call, but writing through its backing array is a trap
	// the next reader should not have to rule out.
	keys := make([]key.Binding, 0, len(s.keys.bindings))
	for _, b := range s.keys.bindings {
		if yes != nil && slices.Equal(b.Keys(), yes) {
			continue
		}
		keys = append(keys, b)
	}
	s.rules = rules
	s.keys = modeKeyMap{bindings: keys}
	return s
}

// planApproveCounts is the checker's counts as the confirm shows them, on two
// lines: what the plan holds, then what the checker found. Errors and
// warnings get a line of their own because errors are what decide whether
// there is a y at all.
func planApproveCounts(c PlanSummary) []string {
	return []string{
		strings.Join([]string{
			planCountNoun(c.Milestones, "milestone"),
			planCountNoun(c.Tasks, "task"),
			planCountNoun(c.OwnerTasks, "owner task"),
			planCountNoun(c.OpenQuestions, "open question"),
		}, " · "),
		planCountNoun(c.Errors, "error") + " · " + planCountNoun(c.Warnings, "warning"),
	}
}

// planApproveErrorsLine is what the confirm says in place of a y when the
// plan has errors: `afk plan approve` would refuse, and the way to see them
// is the check-the-plan row, found by typing "errors".
func planApproveErrorsLine(errors int) string {
	them := "them"
	if errors == 1 {
		them = "it"
	}
	return "The plan has " + planCountNoun(errors, "error") + ". Fix " + them +
		" before approving (" + leaderKey + ", type errors, to see " + them + ")."
}

// planApproveConsequence says what y does, since it commits and pushes, in
// the order the command does it.
const planApproveConsequence = "y writes afk.toml, commits and pushes the plan, starts the build's ledger and lists the project for the daemon. n changes nothing."

// planApproveDialog is the confirm (inputConfirmPlanApprove). It shows the
// project by name and folder, the plan's path and the checker's counts, all
// from the copy taken when it opened. The sentences are wrapped rather than
// cut at the dialog's width, because the one that says why there is no y is
// longer than a line and its end is the part that says what to do.
func (m Model) planApproveDialog() dialog {
	s := m.planApprove
	width := m.dialogContentWidth()
	name := filepath.Base(m.planApproveRoot)
	var lines []string
	if s.Summary.Errors > 0 {
		lines = append(lines, theme.statusLine.Render("The plan for "+name+" cannot be approved yet."))
	} else {
		lines = append(lines, theme.statusLine.Render("Approve the plan for "+name+" and start its build?"))
	}
	lines = append(lines,
		theme.dim.Render(m.planApproveRoot),
		theme.statusLine.Render("Plan: "+s.PlanPath),
	)
	for _, c := range planApproveCounts(s.Summary) {
		lines = append(lines, theme.statusLine.Render(c))
	}
	lines = append(lines, "")
	if s.Summary.Errors > 0 {
		for _, ln := range strings.Split(xansi.Wrap(planApproveErrorsLine(s.Summary.Errors), width, ""), "\n") {
			lines = append(lines, theme.errorText.Render(ln))
		}
	} else {
		for _, ln := range strings.Split(xansi.Wrap(planApproveConsequence, width, ""), "\n") {
			lines = append(lines, theme.dim.Render(ln))
		}
	}
	reg := m.modes()
	return dialog{
		title: "Approve the plan?",
		lines: lines,
		hint:  modalKeyHint(reg.spec(inputConfirmPlanApprove).keys.bindings, "approve", "cancel"),
	}
}
