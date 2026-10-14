// taskstatus.go is the Build tab's "set this task's status" (T7.11): the
// leader row (ctrl+space S), the status picker it opens, the one-line step
// asking for the replacing ids when the choice is superseded, the y/n confirm,
// and the notice its result becomes.
//
// Nothing here reads or writes the ledger. The write is Deps.SetTaskStatus,
// which `afk dash` wires to the code behind `afk ledger set`, so the dashboard
// and the command check ids against the plan, take the ledger's lock and
// refuse in the same words. This package cannot import the command package,
// which is why the seam exists and why TaskStatusResult is declared here and
// filled in there.
//
// The seam is called once per y, and only inside the tea.Cmd setTaskStatusCmd
// returns (messages.go), on the discipline answerInboxCmd documents: it takes
// a file lock and writes a file, and the screen must not wait for either.
package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/taskstatus"
)

// TaskStatusResult is what one status change did, as the dashboard shows it.
// The root package words it (Text) in `afk ledger set`'s own sentences,
// without the "afk ledger set: " prefix the command puts before its
// refusals; the dashboard only picks how loudly to say it from Outcome.
type TaskStatusResult struct {
	// Outcome is one of the TaskStatus* constants.
	Outcome string
	// Text is the plain-language notice, shown as it is.
	Text string
}

// The outcomes the seam reports. Strings, not an enum, on ProjectEditResult's
// pattern, so the root package's adapter reads as plainly as the notices.
const (
	// TaskStatusChanged: the ledger holds the new status now.
	TaskStatusChanged = "changed"
	// TaskStatusAlready: the task had that status already; nothing changed.
	TaskStatusAlready = "already"
	// TaskStatusRefused: the command refused before writing (an id the plan
	// does not have, replacing ids that are wrong).
	TaskStatusRefused = "refused"
	// TaskStatusFailed: the plan or the ledger could not be read or written.
	TaskStatusFailed = "failed"
)

// The action's own words. Constants because the tests quote them.
const (
	// taskStatusNoSeam is the row's reason when the seam is not wired.
	taskStatusNoSeam = "this dashboard cannot change the ledger"
	// taskStatusNoTask is the row's reason on a Build tab with no task under
	// the cursor (an empty plan, or a plan that cannot be read).
	taskStatusNoTask = "no task to set"
	// taskStatusCancelled is n and esc said back, on every step, so the
	// owner knows the ledger was left as it was.
	taskStatusCancelled = "Nothing was changed."
	// taskStatusBusy is a second S while a change is still being written:
	// one write at a time, so two changes cannot race for the same task.
	taskStatusBusy = "A task's status is still being set. Wait for it to finish, then try again."
	// taskStatusProjectChanged is a y that lands after a project switch: the
	// confirm named a task in a project the dashboard has left.
	taskStatusProjectChanged = "The project changed, so nothing was set. Open the Build tab and try again."
	// taskStatusNoAnswer stands in for an empty result text, so the owner is
	// never left without word of what the change did.
	taskStatusNoAnswer = "Could not tell whether the task's status changed. Run afk ledger list to see it."
)

// taskStatusOpenBuildFirst is the row's reason away from the Build tab, or on
// it with the session list holding the keyboard: the cursor that names the
// task is drawn only when the right side has the keyboard (buildCursorShown).
// The key is read from the leader table, so the reason cannot name a key that
// does not open the tab.
func taskStatusOpenBuildFirst() string {
	return "open the Build tab first (" + leaderChord(actionBuild) + ")"
}

// taskStatusAlreadyNotice is the picker's answer when the current status is
// chosen: nothing would change, so nothing is called.
func taskStatusAlreadyNotice(id, status string) string {
	return taskstatus.Already(id, status)
}

// taskStatusEmptyIDs is the replacing-ids step's answer to a blank enter. The
// step stays open, so the owner can type the ids and press enter again.
func taskStatusEmptyIDs(id string) string {
	return taskstatus.EmptyIDsPrompt(id)
}

// taskStatusTarget is the task the picker was opened for, copied from the
// cursor's row when it opened: the two-second build read may move or drop the
// row meanwhile, and y must change exactly the task the dialog named. root
// and gen are the project's main checkout and generation it was opened
// under, so a result that crosses a project switch is not told as the new
// project's.
type taskStatusTarget struct {
	root  string
	gen   int
	id    string
	title string
	old   string
}

// taskStatusBlockedReason is the row's reason, and the action asks it again
// when it runs, so the row and the action cannot disagree.
//
// The seam comes first: without it opening the Build tab would not help, and
// the reason must not send the owner there. The task comes last because only
// on the Build tab with the right side focused is there a cursor to have one.
func taskStatusBlockedReason(c leaderConds) string {
	switch {
	case !c.canSetTaskStatus:
		return taskStatusNoSeam
	case !c.buildTabFocused:
		return taskStatusOpenBuildFirst()
	case !c.buildTaskSelected:
		return taskStatusNoTask
	}
	return ""
}

// taskStatusChoices is the picker's list: every status the ledger accepts,
// in lifecycle order, as `afk ledger set` takes them. The words live in
// internal/taskstatus so the web page says the same.
func taskStatusChoices() []string { return taskstatus.Choices() }

// splitTaskIDs is the replacing-ids step's text as ids.
func splitTaskIDs(text string) []string { return taskstatus.SplitIDs(text) }

// taskStatusConfirmText is the confirm's words, one sentence to a line.
func taskStatusConfirmText(id, old, new string, replacedBy []string) string {
	return taskstatus.ConfirmText(id, old, new, replacedBy)
}

// openTaskStatus is ctrl+space S on the Build tab: the picker for the task
// under the cursor, highlighted on its current status. No file is read and no
// seam is called.
//
// The in-flight check comes first: while a change is out the ledger is moving,
// and a picker showing the old status as "now" would be wrong.
func (m Model) openTaskStatus() (tea.Model, tea.Cmd) {
	if m.taskStatusSetting {
		m.notify(notifyInfo, taskStatusBusy)
		return m, nil
	}
	if reason := taskStatusBlockedReason(m.leaderConditions()); reason != "" {
		m.notify(notifyInfo, reasonSentence(reason))
		return m, nil
	}
	row := m.build.Rows[m.buildCursorIndex]
	m.taskStatus = taskStatusTarget{
		root:  m.build.Root,
		gen:   m.projectGen,
		id:    row.ID,
		title: row.Title,
		old:   row.Status,
	}
	m.taskStatusPick = 0
	for i, s := range taskStatusChoices() {
		if s == row.Status {
			m.taskStatusPick = i
		}
	}
	m.taskStatusNew = ""
	m.taskStatusReplacedBy = nil
	m.modal = inputPickTaskStatus
	m.clearStatus()
	return m, nil
}

// moveTaskStatusPick moves the picker's highlight, clamped at both ends like
// the provider picker's.
func (m Model) moveTaskStatusPick(delta int) (tea.Model, tea.Cmd) {
	m.taskStatusPick = clampInt(m.taskStatusPick+delta, 0, len(taskStatusChoices())-1)
	return m, nil
}

// chooseTaskStatus is the picker's enter. The current status (other than
// superseded, whose replacing ids may be what the owner wants to change) is
// "already" and calls nothing; superseded asks for the replacing ids next;
// any other goes straight to the confirm.
func (m Model) chooseTaskStatus() (tea.Model, tea.Cmd) {
	st := taskStatusChoices()[m.taskStatusPick]
	switch {
	case st == m.taskStatus.old && st != string(ledger.Superseded):
		id := m.taskStatus.id
		m.clearTaskStatus()
		m.notify(notifyInfo, taskStatusAlreadyNotice(id, st))
		return m, nil
	case st == string(ledger.Superseded):
		m.modal = inputTaskStatusReplacedBy
		m.taskStatusNote = ""
		in := textinput.New()
		in.Placeholder = "the replacing task ids, such as " + m.taskStatus.id + "a, " + m.taskStatus.id + "b"
		in.CharLimit = 1024
		if m.width > 4 {
			in.SetWidth(m.dialogContentWidth() - 4)
		}
		m.taskStatusInput = in
		return m, m.taskStatusInput.Focus()
	}
	m.taskStatusNew = st
	m.modal = inputConfirmTaskStatus
	return m, nil
}

// submitTaskStatusReplacedBy is the replacing-ids step's enter. A blank
// answer keeps the step open with a line saying what to type, as the add
// form does: a refusal after the dialog closed would make the owner start
// again from the picker.
func (m Model) submitTaskStatusReplacedBy() (tea.Model, tea.Cmd) {
	ids := splitTaskIDs(m.taskStatusInput.Value())
	if len(ids) == 0 {
		m.taskStatusNote = taskStatusEmptyIDs(m.taskStatus.id)
		return m, nil
	}
	m.taskStatusNew = string(ledger.Superseded)
	m.taskStatusReplacedBy = ids
	m.taskStatusInput = textinput.Model{}
	m.taskStatusNote = ""
	m.modal = inputConfirmTaskStatus
	return m, nil
}

// confirmTaskStatus is the confirm's y, the only way the seam is reached.
func (m Model) confirmTaskStatus() (tea.Model, tea.Cmd) {
	t, st, ids := m.taskStatus, m.taskStatusNew, m.taskStatusReplacedBy
	m.clearTaskStatus()
	if t.gen != m.projectGen {
		m.notify(notifyWarn, taskStatusProjectChanged)
		return m, nil
	}
	if m.taskStatusSetting {
		m.notify(notifyInfo, taskStatusBusy)
		return m, nil
	}
	set := m.deps.SetTaskStatus
	if set == nil {
		m.notify(notifyWarn, reasonSentence(taskStatusNoSeam))
		return m, nil
	}
	m.taskStatusSetting = true
	m.notify(notifyInfo, "Setting "+t.id+" to "+st+"…")
	return m, setTaskStatusCmd(set, t.root, t.id, st, ids, t.gen)
}

// cancelTaskStatus is esc on any step and n on the confirm.
func (m Model) cancelTaskStatus() (tea.Model, tea.Cmd) {
	m.clearTaskStatus()
	m.notify(notifyInfo, taskStatusCancelled)
	return m, nil
}

// clearTaskStatus closes whichever step is open and forgets what it held.
// taskStatusSetting is not touched: it belongs to the write, not the dialog.
func (m *Model) clearTaskStatus() {
	m.modal = inputNormal
	m.taskStatus = taskStatusTarget{}
	m.taskStatusPick = 0
	m.taskStatusNew = ""
	m.taskStatusReplacedBy = nil
	m.taskStatusInput = textinput.Model{}
	m.taskStatusNote = ""
}

// applyTaskStatusSet tells one change's result and reads the build again, so
// the row shows the new status without waiting for the tick. The in-flight
// flag is cleared whatever happens, the write being over either way. A result
// for a project the dashboard has since left is dropped without a word, as a
// build read is: its notice would name a task the screen no longer shows.
func (m Model) applyTaskStatusSet(msg taskStatusSetMsg) (tea.Model, tea.Cmd) {
	m.taskStatusSetting = false
	if msg.gen != m.projectGen {
		return m, nil
	}
	kind, text := taskStatusNotice(msg.result)
	m.notify(kind, text)
	return m, m.buildCmd()
}

// taskStatusNotice picks how loudly to say a result: success for a change,
// information for "nothing to change", a warning for a refusal and an error
// for a ledger that could not be read or written. An empty text is replaced
// so the owner always hears back, and an unknown outcome is told as
// information, which says it without claiming success or failure.
func taskStatusNotice(r TaskStatusResult) (notificationKind, string) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return notifyWarn, taskStatusNoAnswer
	}
	switch r.Outcome {
	case TaskStatusChanged:
		return notifySuccess, text
	case TaskStatusAlready:
		return notifyInfo, text
	case TaskStatusRefused:
		return notifyWarn, text
	case TaskStatusFailed:
		return notifyError, text
	}
	return notifyInfo, text
}

// taskStatusHeading is the task the dialogs are about, as one line.
func (m Model) taskStatusHeading() string {
	t := m.taskStatus
	if t.title == "" {
		return t.id
	}
	return t.id + " " + t.title
}

// taskStatusPickDialog is the picker (inputPickTaskStatus): one row per
// status, the highlighted one marked with "›" and bright, the task's current
// one carrying "(now)", as the provider picker marks its default.
func (m Model) taskStatusPickDialog() dialog {
	choices := taskStatusChoices()
	lines := make([]string, 0, len(choices)+2)
	lines = append(lines,
		theme.statusLine.Render(m.taskStatusHeading()),
		theme.dim.Render("Choose its new status:"),
	)
	for i, s := range choices {
		label := s
		if s == m.taskStatus.old {
			label += " (now)"
		}
		if i == m.taskStatusPick {
			lines = append(lines, theme.sessionName.Render("› "+label))
		} else {
			lines = append(lines, theme.dim.Render("  "+label))
		}
	}
	reg := m.modes()
	return dialog{
		title: "Set the task's status",
		lines: lines,
		hint:  modalKeyHint(reg.spec(inputPickTaskStatus).keys.bindings, "up", "down", "choose", "cancel"),
	}
}

// taskStatusReplacedByDialog is the replacing-ids step: the question, a line
// of context, the field, and under it the line saying what to type after a
// blank enter.
func (m Model) taskStatusReplacedByDialog() dialog {
	lines := []string{
		theme.statusLine.Render("Which tasks replace " + m.taskStatus.id + "?"),
		theme.dim.Render("Their ids, separated by commas or spaces. They are checked against the plan."),
		m.taskStatusInput.View(),
	}
	if m.taskStatusNote != "" {
		lines = append(lines, theme.errorText.Render(m.taskStatusNote))
	}
	reg := m.modes()
	return dialog{
		title: "Replaced by",
		lines: lines,
		hint:  confirmModalKeyHint(reg.spec(inputTaskStatusReplacedBy).keys.bindings, "next"),
	}
}

// taskStatusConfirmDialog is the confirm (inputConfirmTaskStatus), from the
// copy taken when the picker opened. The sentences are wrapped rather than
// cut, as the remove confirm's are, because a long list of replacing ids
// would otherwise cut off the part that says what follows.
func (m Model) taskStatusConfirmDialog() dialog {
	t := m.taskStatus
	width := m.dialogContentWidth()
	lines := []string{theme.statusLine.Render(m.taskStatusHeading()), ""}
	for _, sentence := range strings.Split(taskStatusConfirmText(t.id, t.old, m.taskStatusNew, m.taskStatusReplacedBy), "\n") {
		for _, ln := range strings.Split(xansi.Wrap(sentence, width, ""), "\n") {
			lines = append(lines, theme.statusLine.Render(ln))
		}
	}
	reg := m.modes()
	return dialog{
		title: "Set the task's status?",
		lines: lines,
		hint:  modalKeyHint(reg.spec(inputConfirmTaskStatus).keys.bindings, "set", "cancel"),
	}
}
