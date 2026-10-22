// projectedit.go is the all-projects screen's add and remove (T7.10): the
// `+` and `-` keys at All projects, the two-step form add opens,
// the y/n confirm remove raises, and the notices their results become.
//
// Nothing here reads or writes config.toml. The work is Deps.AddProject and
// Deps.RemoveProject, which `afk dash` wires to the code behind `afk project
// add` and `afk project remove`, so the screen and the commands refuse for the
// same reasons in the same words and edit the file the same way (as text, so
// comments and the [dash] table survive). This package cannot import the
// command package, which is why the seams exist and why ProjectEditResult is
// declared here and filled in there.
//
// Each seam is called once per submit (add) or per y (remove), and only inside
// the tea.Cmd addProjectCmd or removeProjectCmd returns (messages.go), on the
// discipline answerInboxCmd documents: the add runs git on the folder and both
// write a file, and the screen must not wait for either.
package ui

import (
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/projectrows"
)

// ProjectEditResult is what one add or remove did, as the dashboard shows it.
// The root package words it (Text) in the command's own sentences, without
// the "afk project add: " prefix the command puts before some of them; the
// dashboard only picks how loudly to say it from Outcome.
type ProjectEditResult struct {
	// Outcome is one of the Project* constants.
	Outcome string
	// Text is the plain-language notice, shown as it is.
	Text string
}

// The outcomes the two seams report. They are strings, not an enum, on
// PlanApproveResult's pattern, so the root package's adapter reads as plainly
// as the notices it writes.
const (
	// ProjectAdded: the folder is on the list now.
	ProjectAdded = "added"
	// ProjectAlreadyListed: the folder was on the list already; nothing
	// changed.
	ProjectAlreadyListed = "already"
	// ProjectRemoved: the project is off the list now.
	ProjectRemoved = "removed"
	// ProjectNotListed: there was no entry to remove; nothing changed.
	ProjectNotListed = "not_listed"
	// ProjectRefused: the command refused before changing anything (not a
	// folder, not a git repository, a name that clashes, a mistake in the
	// file).
	ProjectRefused = "refused"
	// ProjectFailed: the file could not be read or written.
	ProjectFailed = "failed"
)

// projectEditOpenScreenFirst is both rows' reason away from All projects:
// they act on its rows, which are not on screen. A function and not one of
// the constants below because it names the key that goes there, read from the
// key table (esc from a project's view), so it cannot name a key that does
// not.
func projectEditOpenScreenFirst(leader string) string {
	return "Go back to All projects first (" + keyName(leader, levelProject, actionGoBack) + ")."
}

// The add form's and the remove confirm's own words. Constants because the
// tests and the README quote them.
const (
	// projectAddFolderQuestion and projectAddFolderNote are the form's first
	// step.
	projectAddFolderQuestion = "Which folder? Give its full path, or start it with ~/."
	projectAddFolderNote     = "It goes on the daemon's list in config.toml, as afk project add does."
	// projectAddEmpty is the first step's answer to a blank enter. The form
	// stays open, so the owner can type the folder and press enter again.
	projectAddEmpty = "Type the project's folder, or press esc to cancel."
	// projectAddRelative refuses a relative path before the seam is called.
	// The dashboard's working folder is not one the owner can see, so a
	// relative path would name a folder nobody meant.
	projectAddRelative = "Give the folder's full path, or start it with ~/."
	// projectAddNoHome is the refusal of a ~ path when the home folder is
	// not known.
	projectAddNoHome = "Your home folder is not known here, so ~ cannot be used. Give the folder's full path."
	// projectAddCancelled and projectRemoveCancelled are esc (and n) said
	// back, so the owner knows the file was left as it was.
	projectAddCancelled    = "Nothing was added."
	projectRemoveCancelled = "Nothing was removed."
	// projectRemoveKeeps says what remove does not do, because "remove" next
	// to a project's name reads as deleting it.
	projectRemoveKeeps = "Its folder, its plan and its sessions are not touched; a task that is running is left to finish."
	// projectEditNoAnswer stands in for an empty result text, so the owner
	// is never left without word of what the add or remove did.
	projectEditNoAnswer = "Could not tell whether the project list changed. Run afk project list to see it."
)

// projectRemoveNotListed is the remove row's reason on a row that is not in
// config.toml: the start-up project, shown on the screen without an entry.
func projectRemoveNotListed(name string) string {
	return name + " is not in config.toml, so there is nothing to remove."
}

// projectEditBlockedReason is the add and remove rows' reason, the two rows
// that run only over the all-projects screen; leaderBlockedReason asks it
// before anything else, and the two actions ask it again when they run, so
// the row and the action cannot disagree.
//
// The seam comes first: without it opening the screen would not help, and the
// reason must not send the owner there. Remove then needs a row under the
// cursor, and a listed one: the unlisted start-up project has no entry, and
// removing nothing would be a confirm that can only fail.
func projectEditBlockedReason(a modeAction, c leaderConds) string {
	switch {
	case a == actionProjectAdd && !c.canAddProject:
		return "adding a project is not available here"
	case a == actionProjectRemove && !c.canRemoveProject:
		return "removing a project is not available here"
	case !c.allProjectsOpen:
		return projectEditOpenScreenFirst(c.leader)
	case a == actionProjectRemove && !c.projectRowSelected:
		return "no project is selected"
	case a == actionProjectRemove && !c.projectRowListed:
		return projectRemoveNotListed(c.projectRowName)
	}
	return ""
}

// reasonSentence turns a dimmed row's reason into a notice. Most reasons are
// fragments ("no project is selected"), which get a capital and a full stop;
// a reason that is a sentence already is left alone, since it may start with
// a project's name, whose case is the owner's.
func reasonSentence(reason string) string {
	if strings.HasSuffix(reason, ".") {
		return reason
	}
	return strings.ToUpper(reason[:1]) + reason[1:] + "."
}

// openProjectAdd is `+` at All projects: the add form's
// first step, asking for the folder. No file is read and no seam is called.
func (m Model) openProjectAdd() (tea.Model, tea.Cmd) {
	if reason := projectEditBlockedReason(actionProjectAdd, m.leaderConditions()); reason != "" {
		m.notify(notifyInfo, reasonSentence(reason))
		return m, nil
	}
	m.modal = inputProjectAddFolder
	m.projectAddDir = ""
	m.projectAddNote = ""
	in := m.newProjectAddInput("a folder, such as ~/code/my-app")
	if def := m.projectAddDefault(); def != "" {
		in.SetValue(def)
		in.CursorEnd()
	}
	m.projectAddInput = in
	m.clearStatus()
	return m, m.projectAddInput.Focus()
}

// newProjectAddInput is the form's field, sized as openPlanTextForm sizes its
// own: the dialog's content width less the "> " prompt and a cell of slack.
func (m Model) newProjectAddInput(placeholder string) textinput.Model {
	in := textinput.New()
	in.Placeholder = placeholder
	in.CharLimit = 4096
	if m.width > 4 {
		in.SetWidth(m.dialogContentWidth() - 4)
	}
	return in
}

// projectAddDefault is the folder step's starting text: the active project's
// main checkout when that project is not on the list, else "".
//
// The unlisted active project is the one folder the screen shows that the
// owner is most likely to want added: it has a row only because the dashboard
// was started in it. A listed one would only come back "already in the list".
// The start-up row keeps the folder the dashboard was started in, which for a
// linked worktree is not the main checkout, so it is resolved through
// Deps.LaunchMainCheckout as isActiveProjectRoot does; the list holds main
// checkouts, and a worktree's path would never match it.
//
// Listed is asked of both the switcher's listed read and the screen's rows,
// since they are read on different ticks and either may know first.
func (m Model) projectAddDefault() string {
	root := m.activeProjectPath()
	switch {
	case root == "" && len(m.projects) == 0:
		root = m.repo
	case root != "" && m.deps.LaunchMainCheckout != "" && m.projects[m.activeProject].Pinned:
		root = m.deps.LaunchMainCheckout
	}
	if root == "" {
		return ""
	}
	for _, e := range m.listed {
		if e.Path == root {
			return ""
		}
	}
	for _, r := range m.allProjectsRows {
		if r.Root == root && r.Listed {
			return ""
		}
	}
	return root
}

// expandProjectDir is what the folder step makes of the typed text: an
// absolute folder, or the line to show under the field instead. `~` and `~/…`
// are the home folder; any other relative path is refused, because a
// relative path would be read against the dashboard's own working folder,
// which the owner cannot see. home is asked only for a `~` path.
//
// `~name/…` (another user's home) is refused as relative: the command does
// not expand it either, and guessing would list a folder nobody typed.
func expandProjectDir(text string, home func() (string, error)) (string, string) {
	if text == "~" || strings.HasPrefix(text, "~/") {
		h, err := home()
		if err != nil || h == "" {
			return "", projectAddNoHome
		}
		return filepath.Join(h, strings.TrimPrefix(text, "~")), ""
	}
	if !filepath.IsAbs(text) {
		return "", projectAddRelative
	}
	return filepath.Clean(text), ""
}

// submitProjectAddFolder is the folder step's enter. A blank or relative
// folder keeps the form open with a line saying what is wrong and calls
// nothing, as the plan form does with blank text: a refusal toast after the
// form closed would make the owner open it and type everything again.
func (m Model) submitProjectAddFolder() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.projectAddInput.Value())
	if text == "" {
		m.projectAddNote = projectAddEmpty
		return m, nil
	}
	home := m.deps.HomeDir
	if home == nil {
		home = os.UserHomeDir
	}
	dir, note := expandProjectDir(text, home)
	if note != "" {
		m.projectAddNote = note
		return m, nil
	}
	m.modal = inputProjectAddName
	m.projectAddDir = dir
	m.projectAddNote = ""
	m.projectAddInput = m.newProjectAddInput("blank uses " + filepath.Base(dir))
	return m, m.projectAddInput.Focus()
}

// submitProjectAddName is the name step's enter, the add itself: the seam is
// called once, off the drawing path, with the folder step one took and the
// name as typed ("" for the folder's name). The name is not checked here; the
// command decides whether it clashes and says so in its own words.
func (m Model) submitProjectAddName() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.projectAddInput.Value())
	dir := m.projectAddDir
	m.clearProjectAdd()
	add := m.deps.AddProject
	if add == nil {
		m.notify(notifyWarn, reasonSentence("adding a project is not available here"))
		return m, nil
	}
	m.notify(notifyInfo, "Adding "+dir+"…")
	return m, addProjectCmd(add, dir, name, m.projectGen, m.allProjectsGen)
}

// cancelProjectAdd is esc on either step: nothing was called, and the notice
// says so.
func (m Model) cancelProjectAdd() (tea.Model, tea.Cmd) {
	m.clearProjectAdd()
	m.notify(notifyInfo, projectAddCancelled)
	return m, nil
}

// clearProjectAdd closes the form and forgets what it held.
func (m *Model) clearProjectAdd() {
	m.modal = inputNormal
	m.projectAddDir = ""
	m.projectAddNote = ""
	m.projectAddInput = textinput.Model{}
}

// openProjectRemove is `-` at All projects: the confirm
// for the selected row. It copies the row, so y removes the project the
// dialog names even if a refresh re-sorts the rows under it.
func (m Model) openProjectRemove() (tea.Model, tea.Cmd) {
	if reason := projectEditBlockedReason(actionProjectRemove, m.leaderConditions()); reason != "" {
		m.notify(notifyInfo, reasonSentence(reason))
		return m, nil
	}
	r, _ := m.selectedProjectRow()
	m.modal = inputConfirmProjectRemove
	m.projectRemoveRow = r
	m.clearStatus()
	return m, nil
}

// confirmProjectRemove is the confirm's y, the only way the remove seam is
// reached.
func (m Model) confirmProjectRemove() (tea.Model, tea.Cmd) {
	r := m.projectRemoveRow
	m.clearProjectRemove()
	remove := m.deps.RemoveProject
	if remove == nil {
		m.notify(notifyWarn, reasonSentence("removing a project is not available here"))
		return m, nil
	}
	m.notify(notifyInfo, "Removing "+r.Name+" from the list…")
	return m, removeProjectCmd(remove, r.Root)
}

// cancelProjectRemove is the confirm's n and esc.
func (m Model) cancelProjectRemove() (tea.Model, tea.Cmd) {
	m.clearProjectRemove()
	m.notify(notifyInfo, projectRemoveCancelled)
	return m, nil
}

// clearProjectRemove closes the confirm and forgets the row.
func (m *Model) clearProjectRemove() {
	m.modal = inputNormal
	m.projectRemoveRow = projectrows.Row{}
}

// applyProjectAdded tells an add's result and reads the rows and the
// switcher's list again, so the new row and the new switcher entry appear
// without waiting for their ticks.
//
// The result is told whenever it lands: config.toml changed (or did not)
// whatever the dashboard did meanwhile, and nothing else will say so. Only an
// add that lands in the same project and the same opening of the screen it
// was made from moves the cursor to the new row (allProjectsFollow); after a
// switch or a reopening the owner is looking at something else, and moving
// their cursor would be a surprise. The row is matched on the folder as
// typed, so a folder the command resolved differently (a linked worktree to
// its main checkout, a symlink to its target) is listed and shown, and only
// the cursor stays where it was.
func (m Model) applyProjectAdded(msg projectAddedMsg) (tea.Model, tea.Cmd) {
	kind, text := projectEditNotice(msg.result)
	m.notify(kind, text)
	fresh := m.allProjectsOpen && msg.gen == m.projectGen && msg.openGen == m.allProjectsGen
	if fresh && (msg.result.Outcome == ProjectAdded || msg.result.Outcome == ProjectAlreadyListed) {
		m.allProjectsFollow = msg.dir
	}
	return m, tea.Batch(m.allProjectsCmd(), m.listedCmd())
}

// applyProjectRemoved tells a remove's result and reads the rows and the
// switcher's list again, so the row goes. Nothing else moves: the cursor
// stays where it is, clamped to the shorter list.
func (m Model) applyProjectRemoved(msg projectRemovedMsg) (tea.Model, tea.Cmd) {
	kind, text := projectEditNotice(msg.result)
	m.notify(kind, text)
	return m, tea.Batch(m.allProjectsCmd(), m.listedCmd())
}

// projectEditNotice picks how loudly to say a result: success for a change,
// information for "nothing to change", a warning for a refusal and an error
// for a file that could not be read or written. An empty text is replaced so
// the owner always hears back, and an outcome this package does not know is
// told as information, which says it without claiming success or failure.
func projectEditNotice(r ProjectEditResult) (notificationKind, string) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return notifyWarn, projectEditNoAnswer
	}
	switch r.Outcome {
	case ProjectAdded, ProjectRemoved:
		return notifySuccess, text
	case ProjectAlreadyListed, ProjectNotListed:
		return notifyInfo, text
	case ProjectRefused:
		return notifyWarn, text
	case ProjectFailed:
		return notifyError, text
	}
	return notifyInfo, text
}

// projectAddDialog is the add form, on whichever step is open: the question,
// a line of context, the field, and under it the line saying what is wrong
// after an enter that could not go on.
func (m Model) projectAddDialog() dialog {
	reg := m.modes()
	var lines []string
	verb := "next"
	if m.modal == inputProjectAddName {
		verb = "add"
		lines = append(lines,
			theme.statusLine.Render("What should it be called? Leave it blank to use "+filepath.Base(m.projectAddDir)+"."),
			theme.dim.Render("Folder: "+m.projectAddDir),
		)
	} else {
		lines = append(lines,
			theme.statusLine.Render(projectAddFolderQuestion),
			theme.dim.Render(projectAddFolderNote),
		)
	}
	lines = append(lines, m.projectAddInput.View())
	if m.projectAddNote != "" {
		lines = append(lines, theme.errorText.Render(m.projectAddNote))
	}
	return dialog{
		title: "Add a project",
		lines: lines,
		hint:  confirmModalKeyHint(reg.spec(m.modal).keys.bindings, verb),
	}
}

// projectRemoveDialog is the remove confirm (inputConfirmProjectRemove). It
// names the project by name and folder, from the copy taken when it opened,
// and says what is left alone. The sentences are wrapped rather than cut, as
// the approve confirm's are, because a long folder would otherwise cut off
// the question mark and the part that says nothing is deleted.
func (m Model) projectRemoveDialog() dialog {
	r := m.projectRemoveRow
	width := m.dialogContentWidth()
	var lines []string
	for _, ln := range strings.Split(xansi.Wrap("Remove "+r.Name+" ("+r.Root+") from the daemon's list?", width, ""), "\n") {
		lines = append(lines, theme.statusLine.Render(ln))
	}
	for _, ln := range strings.Split(xansi.Wrap(projectRemoveKeeps, width, ""), "\n") {
		lines = append(lines, theme.dim.Render(ln))
	}
	reg := m.modes()
	return dialog{
		title: "Remove the project?",
		lines: lines,
		hint:  modalKeyHint(reg.spec(inputConfirmProjectRemove).keys.bindings, "remove", "cancel"),
	}
}
