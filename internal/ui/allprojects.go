// allprojects.go is the all-projects screen (T7.9): one row for every project
// the daemon builds, each with its current task, the state of its build and
// what in its inbox needs the owner, opened from the actions dialog ("all
// projects", ctrl+] A). It takes the place of the session list and the
// detail side until esc, enter switches the dashboard to the selected
// project, and pause and resume act on the selected row's project rather
// than the active one.
//
// The rows are built by internal/projectrows, the pure package the web page
// will share (T8.5), so the two surfaces cannot word a project's state
// differently. This file only fetches and stores them, on the pattern of
// planstatusdata.go: the reading is Deps.LoadAllProjects, wired by the root
// package, which knows where every project's files are. Nothing here or in
// allprojectsview.go reads a file.
//
// The rows are read only while the screen is open: once when it opens and
// then on every discovery tick. Every project's ledger, inbox and state file
// is a handful of reads each, so paying it every two seconds for a screen
// nobody is looking at would be waste.
package ui

import (
	"slices"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/projectrows"
)

// AllProjects is one read of every project's row. It is declared here and
// filled in by the root package (dash_allprojects.go), which can read the
// files this package must not.
type AllProjects struct {
	// Rows are the built rows, one per listed project plus the dashboard's
	// active project when it is not listed. Their order does not matter:
	// the screen sorts them with projectrows.Sort.
	Rows []projectrows.Row
	// ConfigPath is the config.toml the list was read from, named in the
	// empty-list line and the unreadable line.
	ConfigPath string
	// Err is a failure to read config.toml itself. A project that could not
	// be read is a row (StateUnreadable), never an Err: one broken project
	// must not hide the others. With Err set Rows is ignored and the screen
	// keeps the rows it last showed.
	Err error
}

// allProjectsLoadedMsg carries one finished read. gen is the opening the read
// was started under (Model.allProjectsGen), so a read that lands after the
// screen was closed, or after it was closed and opened again, is dropped.
type allProjectsLoadedMsg struct {
	res AllProjects
	gen int
}

// allProjectsPausedMsg and allProjectsResumedMsg carry the outcome of a pause
// or resume run on a row of the screen. They are separate from
// buildPausedMsg and buildResumedMsg on purpose: those are dropped when the
// project generation moved, because they are told as the active project's;
// these name their project, so they are true whatever the dashboard shows
// by the time they land.
type allProjectsPausedMsg struct {
	name    string
	already bool
	err     error
}

type allProjectsResumedMsg struct {
	name string
	res  ResumeResult
	err  error
}

// The screen's notices. Constants because the tests and the README quote
// them.
const (
	// allProjectsUnwiredNotice is the action with no reader wired. `afk dash`
	// always wires one; a dashboard built without it answers in a sentence
	// and points at the shell report that says the same things.
	allProjectsUnwiredNotice = "This dashboard was started without the project reader, so it cannot show all projects. Run afk daemon status in a shell instead."
	// allProjectsCloseFirst is the reason every dialog row but pause,
	// resume, all projects, add and remove a project (T7.10), mouse and quit
	// is dimmed over the screen. Those
	// rows act on the session list, the detail side or the active project,
	// none of which is on screen while it is open.
	allProjectsCloseFirst = "Close the all-projects screen first (esc)."
	// allProjectsReading is what the screen shows before its first read.
	allProjectsReading = "Reading the projects…"
)

// allProjectsGoneNotice is enter, pause or resume on a row whose folder is
// gone: nothing can be done to a project that is not there, and the two
// commands that fix it are named.
func allProjectsGoneNotice(name string) string {
	return "The folder of " + name + " is gone. Fix its path in config.toml or remove it with afk project remove."
}

// allProjectsUnreadableNotice is enter on a row whose files could not be
// read. The dashboard would open on a project it cannot show the build of,
// so it says why and stays.
func allProjectsUnreadableNotice(r projectrows.Row) string {
	return "The files of " + r.Name + " could not be read, so the dashboard did not switch to it: " + r.Detail
}

// allProjectsNotListedNotice is enter on a row the switcher does not have yet
// (the project list is read on a slower tick than these rows). Switching
// needs the switcher's entry, so the owner is told to wait for it.
func allProjectsNotListedNotice(name string) string {
	return name + " is not in the dashboard's project list yet. Try again in a moment."
}

// allProjectsCmd reads every project's row. It returns nil when no reader is
// wired or the screen is closed, which is the whole of "the seam is not
// called while the screen is closed": the tick asks for it unconditionally.
//
// The reader runs inside the returned closure, on the command goroutine,
// never on the Update goroutine or in a View. The guard follows buildCmd's:
// one slow read (a stalled disk under one project) must not be piled up
// behind by the refresh ticker, and it is released in this goroutine so a
// dropped message cannot leave it held.
func (m Model) allProjectsCmd() tea.Cmd {
	load := m.deps.LoadAllProjects
	if load == nil || !m.allProjectsOpen {
		return nil
	}
	gen := m.allProjectsGen
	refreshing := m.allProjectsRefreshing
	return func() tea.Msg {
		// A nil guard is a Model built as a bare struct literal, which only a
		// test does.
		if refreshing != nil {
			if !refreshing.CompareAndSwap(false, true) {
				return nil
			}
			defer refreshing.Store(false)
		}
		return allProjectsLoadedMsg{res: load(), gen: gen}
	}
}

// applyAllProjectsLoaded stores a finished read.
//
// A read for a screen that is closed, or for an earlier opening of it, is
// dropped: it would refill rows nobody asked for, or put an old opening's
// rows under a new one's cursor.
//
// An Err keeps the rows already shown and only records why: config.toml
// half-written by an editor would otherwise blank the whole screen for a
// tick. A good read replaces the rows and keeps the cursor on the project it
// was on, wherever the sort moved it, so a row whose state changed under the
// owner's hands does not move the highlight to another project.
func (m Model) applyAllProjectsLoaded(msg allProjectsLoadedMsg) Model {
	if !m.allProjectsOpen || msg.gen != m.allProjectsGen {
		return m
	}
	if msg.res.ConfigPath != "" {
		m.allProjectsConfigPath = msg.res.ConfigPath
	}
	if msg.res.Err != nil {
		m.allProjectsErr = msg.res.Err.Error()
		return m
	}
	m.allProjectsErr = ""
	var keep string
	if r, ok := m.selectedProjectRow(); ok {
		keep = r.Root
	}
	rows := slices.Clone(msg.res.Rows)
	projectrows.Sort(rows)
	m.allProjectsRows = rows
	m.allProjectsKnown = true
	if i := slices.IndexFunc(rows, func(r projectrows.Row) bool { return r.Root == keep }); keep != "" && i >= 0 {
		m.allProjectsCursor = i
	}
	// A project just added from the screen takes the cursor once its row is
	// read (projectedit.go). It waits for a read that has the row rather than
	// the first read after the add: a tick's read already out when the add
	// wrote the file can land first, without it.
	if follow := m.allProjectsFollow; follow != "" {
		if i := slices.IndexFunc(rows, func(r projectrows.Row) bool { return r.Root == follow }); i >= 0 {
			m.allProjectsCursor = i
			m.allProjectsFollow = ""
		}
	}
	m.allProjectsCursor = clampInt(m.allProjectsCursor, 0, max(len(rows)-1, 0))
	return m
}

// selectedProjectRow is the row under the screen's cursor.
func (m Model) selectedProjectRow() (projectrows.Row, bool) {
	if m.allProjectsCursor < 0 || m.allProjectsCursor >= len(m.allProjectsRows) {
		return projectrows.Row{}, false
	}
	return m.allProjectsRows[m.allProjectsCursor], true
}

// toggleAllProjects is the "all projects" action: it opens the screen, or
// closes it when it is open (the dialog over the screen keeps the row for
// exactly that).
//
// Opening starts from nothing — no rows, the cursor at the top — and reads at
// once. Rows kept from an earlier opening could be minutes old, and a screen
// that shows them as current would be wrong in the way that matters most
// here: a project said to be running that has since stopped. The read guard
// is replaced rather than reused, so a read still out from the earlier
// opening (whose result will be dropped by its generation) cannot keep this
// opening's first read from starting.
//
// The keyboard is already on the list: the leader row is a list action
// (leaderMovesToList). That matters for closing: esc leaves the dashboard
// as it was, with the list focused.
func (m Model) toggleAllProjects() (tea.Model, tea.Cmd) {
	if m.allProjectsOpen {
		m.allProjectsOpen = false
		return m, nil
	}
	if m.deps.LoadAllProjects == nil {
		// The dialog dims the row in this case; this is for a run that
		// reaches the action some other way.
		m.notify(notifyInfo, allProjectsUnwiredNotice)
		return m, nil
	}
	m.allProjectsOpen = true
	m.allProjectsGen++
	m.allProjectsRows = nil
	m.allProjectsKnown = false
	m.allProjectsErr = ""
	m.allProjectsCursor = 0
	m.allProjectsFollow = ""
	m.allProjectsRefreshing = new(atomic.Bool)
	return m, m.allProjectsCmd()
}

// moveAllProjects moves the cursor by delta rows, clamped at both ends rather
// than wrapping: the first rows are the ones that need the owner, and a list
// that wrapped would put a held-down up-arrow among the quiet ones.
func (m Model) moveAllProjects(delta int) (tea.Model, tea.Cmd) {
	if len(m.allProjectsRows) == 0 {
		return m, nil
	}
	m.allProjectsCursor = clampInt(m.allProjectsCursor+delta, 0, len(m.allProjectsRows)-1)
	return m, nil
}

// switchFromAllProjects is enter on the screen.
//
// A gone or unreadable row does not switch: there is no folder to show, or
// none the dashboard can read, and the notice says why. The active project
// closes the screen and does nothing else, as re-picking it in the switcher
// does. Any other row switches exactly as the switcher does — the same
// switchProject, the same one-shot preview arm and the same open count — so
// the two ways to change project cannot behave differently.
//
// The switcher's entry for a project is found by path. The rows carry the
// main checkout; the switcher's start-up row keeps the folder the dashboard
// was started in, which for a linked worktree is not the main checkout, so
// that row is also matched through Deps.LaunchMainCheckout (as
// isActiveProjectRoot does).
func (m Model) switchFromAllProjects() (tea.Model, tea.Cmd) {
	r, ok := m.selectedProjectRow()
	if !ok {
		return m, nil
	}
	switch r.State {
	case projectrows.StateGone:
		m.notify(notifyWarn, allProjectsGoneNotice(r.Name))
		return m, nil
	case projectrows.StateUnreadable:
		m.notify(notifyWarn, allProjectsUnreadableNotice(r))
		return m, nil
	}
	if m.isActiveProjectRoot(r.Root) {
		m.allProjectsOpen = false
		return m, nil
	}
	path, ok := m.projectPathForRoot(r.Root)
	if !ok {
		m.notify(notifyInfo, allProjectsNotListedNotice(r.Name))
		return m, nil
	}
	m.allProjectsOpen = false
	nm, cmd := m.switchProject(path)
	if nm.repo != m.repo {
		// Armed and counted only on a real switch, for the reasons
		// switcherComponentKey gives.
		nm.previewOnNextLoad = true
		nm.recordProjectOpen(path)
	}
	return nm, cmd
}

// projectPathForRoot is the switcher entry's path for a project whose main
// checkout is root: the entry with that path, or the pinned start-up row when
// root is the main checkout of the linked worktree the dashboard was started
// in.
func (m Model) projectPathForRoot(root string) (string, bool) {
	if i, ok := indexOfProject(m.projects, root); ok {
		return m.projects[i].Path, true
	}
	if mc := m.deps.LaunchMainCheckout; mc != "" && root == mc {
		for _, p := range m.projects {
			if p.Pinned {
				return p.Path, true
			}
		}
	}
	return "", false
}

// rowPauseRefusal is why pause or resume will not run on row r, or "" when
// it may. A gone folder has nothing to write into, and a project with no
// master plan has no build, worded as `b` and the pause keys word it. A row
// that could not be read is let through: the seam reads the project itself
// and says what is wrong in its own words, which is more than this row
// knows.
func rowPauseRefusal(r projectrows.Row, pause bool) string {
	switch r.State {
	case projectrows.StateGone:
		return allProjectsGoneNotice(r.Name)
	case projectrows.StateNoPlan:
		if pause {
			return r.Name + ": " + noBuildToPauseNotice
		}
		return r.Name + ": " + noBuildToResumeNotice
	}
	return ""
}

// pauseSelectedProject is pause with the screen open: the selected row's
// project, through the same seam as the pause key, off the Update goroutine.
func (m Model) pauseSelectedProject() (tea.Model, tea.Cmd) {
	r, ok := m.selectedProjectRow()
	if !ok {
		return m, nil
	}
	if text := rowPauseRefusal(r, true); text != "" {
		m.notify(notifyInfo, text)
		return m, nil
	}
	pause := m.deps.PauseBuild
	if pause == nil {
		m.notify(notifyWarn, buildPauseUnavailableNotice)
		return m, nil
	}
	root, name := r.Root, r.Name
	return m, func() tea.Msg {
		already, err := pause(root)
		return allProjectsPausedMsg{name: name, already: already, err: err}
	}
}

// resumeSelectedProject is resume with the screen open, on
// pauseSelectedProject's terms.
func (m Model) resumeSelectedProject() (tea.Model, tea.Cmd) {
	r, ok := m.selectedProjectRow()
	if !ok {
		return m, nil
	}
	if text := rowPauseRefusal(r, false); text != "" {
		m.notify(notifyInfo, text)
		return m, nil
	}
	resume := m.deps.ResumeBuild
	if resume == nil {
		m.notify(notifyWarn, buildPauseUnavailableNotice)
		return m, nil
	}
	root, name := r.Root, r.Name
	return m, func() tea.Msg {
		res, err := resume(root)
		return allProjectsResumedMsg{name: name, res: res, err: err}
	}
}

// applyAllProjectsPaused words a row's pause with the pause key's sentences,
// led by the project's name, since it may not be the project on screen. The
// rows and the active project's Build tab are read again in every case, a
// failure included, for the reason applyBuildPaused gives: a failure is not
// proof that nothing changed. The row read is nil once the screen is closed.
func (m Model) applyAllProjectsPaused(msg allProjectsPausedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notify(notifyError, msg.name+": "+pauseFailedNotice(msg.err))
	} else {
		kind, text := pauseNotice(msg.already)
		m.notify(kind, msg.name+": "+text)
	}
	return m, tea.Batch(m.allProjectsCmd(), m.buildCmd())
}

// applyAllProjectsResumed is applyAllProjectsPaused for a resume.
func (m Model) applyAllProjectsResumed(msg allProjectsResumedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notify(notifyError, msg.name+": "+resumeFailedNotice(msg.err))
	} else {
		kind, text := resumeNotice(msg.res)
		m.notify(kind, msg.name+": "+text)
	}
	return m, tea.Batch(m.allProjectsCmd(), m.buildCmd())
}
