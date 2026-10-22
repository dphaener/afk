// allprojects.go is the all-projects screen (T7.9): one row for every project
// the daemon builds, each with its current task, the state of its build and
// what in its inbox needs the owner. Since T16.2 it is the All projects level:
// esc on a project's session list opens it, and enter on a row switches the
// dashboard to that project. It takes the place of the session list and the
// detail side until then. Pause and resume on the selected row's project wait
// for T16.3 to give the level its `p`. Since T16.3 it is drawn as the home
// screen (allprojectsview.go): grouped rows and the selected project's panel.
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
	"time"

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
	// Tasks are each project's master-plan tasks keyed by the row's Root, for
	// the jump search (T16.6) to find a task by name from any level. They come
	// from the plan and ledger the reader already loaded for the row, so they
	// cost no extra read. A project with no plan, or whose plan or inbox could
	// not be read, has no entry.
	Tasks map[string][]JumpTask
	// Err is a failure to read config.toml itself. A project that could not
	// be read is a row (StateUnreadable), never an Err: one broken project
	// must not hide the others. With Err set Rows is ignored and the screen
	// keeps the rows it last showed.
	Err error
}

// JumpTask is one master-plan task as the jump search lists it: its id, its
// title and its ledger status in words ("pending", "running", "done" ...).
type JumpTask struct {
	ID, Title, Status string
}

// allProjectsLoadedMsg carries one finished read. gen is the opening the read
// was started under (Model.allProjectsGen), so a read that lands after the
// screen was closed, or after it was closed and opened again, is dropped by
// the screen. seq and at serve the every-project cache instead (see
// applyEveryProject): seq numbers reads in the order they started and at is
// when this one finished, both stamped in the Cmd so Update never reads a
// clock or a counter that is not its own.
type allProjectsLoadedMsg struct {
	res AllProjects
	gen int
	seq int64
	at  time.Time
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
	if !m.allProjectsOpen {
		return nil
	}
	return m.allProjectsReadCmd()
}

// everyProjectCmd reads every project's row for the cache that lives whatever
// level the dashboard is at (T16.6): `]`, the top bar and the jump search all
// need other projects' rows while the screen is closed. It shares
// allProjectsCmd's single-flight guard, so a read for the screen and one for
// the cache never run side by side, and it is nil only when no reader is wired.
// A read it starts lands with the screen's generation, so the screen drops it
// when closed; only the cache keeps it.
func (m Model) everyProjectCmd() tea.Cmd {
	return m.allProjectsReadCmd()
}

// allProjectsReadCmd is the read both callers share. It is nil with no reader
// wired.
func (m Model) allProjectsReadCmd() tea.Cmd {
	load := m.deps.LoadAllProjects
	if load == nil {
		return nil
	}
	gen := m.allProjectsGen
	refreshing := m.allProjectsRefreshing
	counter := m.allProjectsSeq
	return func() tea.Msg {
		// A nil guard is a Model built as a bare struct literal, which only a
		// test does.
		if refreshing != nil {
			if !refreshing.CompareAndSwap(false, true) {
				return nil
			}
			defer refreshing.Store(false)
		}
		// The number is taken once the guard is held, so reads are numbered in
		// the order they really ran; a nil counter is a bare-struct Model.
		var seq int64
		if counter != nil {
			seq = counter.Add(1)
		}
		res := load()
		return allProjectsLoadedMsg{res: res, gen: gen, seq: seq, at: time.Now()}
	}
}

// applyEveryProject stores a finished read in the every-project cache. Unlike
// the screen it ignores the generation and whether the screen is open: the
// cache is for readers at every level. It takes a read only when it started
// after the one the cache holds (a slower, older read landing late must not
// roll the cache back), and never an Err, which keeps what is there.
func (m Model) applyEveryProject(msg allProjectsLoadedMsg) Model {
	if msg.res.Err != nil || msg.seq < m.everyProjectSeq {
		return m
	}
	m.everyProject = msg.res
	m.everyProjectSeq = msg.seq
	m.everyProjectAt = msg.at
	return m
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
	m = m.applyEveryProject(msg)
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
	rows := allProjectsOrder(slices.Clone(msg.res.Rows))
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

// allProjectsOrder puts rows in the order the home screen draws them (§4.1):
// the groups NEEDS YOU, RUNNING, QUIET, FINISHED, config order inside each.
// The rows are stored in this order, not just drawn in it, so the cursor's
// index, the movement keys and the scroll all walk the list the owner sees;
// stored in projectrows.Sort's order alone, the cursor would jump between
// groups as it moved. Sort first gives config order inside a group, and
// Grouped keeps it.
func allProjectsOrder(rows []projectrows.Row) []projectrows.Row {
	projectrows.Sort(rows)
	out := make([]projectrows.Row, 0, len(rows))
	for _, g := range projectrows.Grouped(rows) {
		out = append(out, g...)
	}
	return out
}

// selectedProjectRow is the row under the screen's cursor.
func (m Model) selectedProjectRow() (projectrows.Row, bool) {
	if m.allProjectsCursor < 0 || m.allProjectsCursor >= len(m.allProjectsRows) {
		return projectrows.Row{}, false
	}
	return m.allProjectsRows[m.allProjectsCursor], true
}

// toggleAllProjects opens the screen, or closes it when it is open. esc at
// Project opens it (levelGoBack), and enter in the empty state, which stands
// in for it with no project open (levelGoIn).
//
// Opening starts from nothing — no rows, the cursor at the top — and reads at
// once. Rows kept from an earlier opening could be minutes old, and a screen
// that shows them as current would be wrong in the way that matters most
// here: a project said to be running that has since stopped. The read guard
// is replaced rather than reused, so a read still out from the earlier
// opening (whose result will be dropped by its generation) cannot keep this
// opening's first read from starting.
//
// The keyboard is already on the list: esc goes back to All projects only
// from the session list. That matters for coming back: enter on the active
// project's row closes the screen and leaves the dashboard as it was, with the
// list focused.
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
	nm, cmd := m.openAllProjects()
	return nm, cmd
}

// openAllProjects opens the screen from nothing and starts its first read. It
// is shared by toggleAllProjects (esc, enter in the empty state) and by the
// start-up decision (settleActiveProject), which opens the screen with no
// keypress. The caller has checked that a reader is wired.
func (m Model) openAllProjects() (Model, tea.Cmd) {
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

// StartLevel is the level the dashboard opens on, decided by package main
// (dashStartLevel) from where it was started and which flags were given. The
// zero value is StartProject, so a Model built without it behaves as it
// always did: the first known project becomes the active one.
type StartLevel int

const (
	// StartProject opens on a project's view: the project the dashboard was
	// started in or named by --project, or, when none was, the first project
	// the scan finds.
	StartProject StartLevel = iota
	// StartAllProjects opens on All projects with no project active, once a
	// project is known. With no project at all the empty state shows instead.
	StartAllProjects
	// StartAgent is StartProject, then the named session's Agent with the
	// keyboard on it (--open).
	StartAgent
)

// moveAllProjects moves the cursor by delta rows, clamped at both ends rather
// than wrapping: the first rows are the ones that need the owner, and a list
// that wrapped would put a held-down up-arrow among the quiet ones.
func (m Model) moveAllProjects(delta int) (tea.Model, tea.Cmd) {
	if len(m.allProjectsRows) == 0 {
		return m, nil
	}
	m.allProjectsCursor = clampInt(m.allProjectsCursor+delta, 0, len(m.allProjectsRows)-1)
	m.clearNeedAt()
	return m, nil
}

// switchFromAllProjects is enter on the screen.
//
// A gone or unreadable row does not switch: there is no folder to show, or
// none the dashboard can read, and the notice says why. The active project
// closes the screen and does nothing else, as picking it in the jump search
// does. Any other row switches exactly as the jump search does (jumpToProject)
// — the same switchProject, the same one-shot preview arm and the same open
// count — so the two ways to change project cannot behave differently.
//
// The project list's entry for a project is found by path. The rows carry the
// main checkout; the list's start-up row keeps the folder the dashboard
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
		// jumpToProject gives.
		nm.previewOnNextLoad = true
		nm.recordProjectOpen(path)
	}
	return nm, cmd
}

// oldestBlockingRow is the row whose project holds the oldest open blocking
// inbox item, for `a`. CreatedAt is the store's RFC3339 stamp, so comparing
// the strings orders them in time (the same choice readDashProjectRow made
// when it picked each row's Wait). A tie goes to the earlier row, which is
// config order within a group; rows with no wait are skipped.
func oldestBlockingRow(rows []projectrows.Row) (projectrows.Row, bool) {
	var best projectrows.Row
	found := false
	for _, r := range rows {
		if r.Wait.ItemID == "" {
			continue
		}
		if !found || r.Wait.CreatedAt < best.Wait.CreatedAt {
			best, found = r, true
		}
	}
	return best, found
}

// answerOldest is `a` at All projects: take the owner to the oldest item
// anywhere that holds a task up. It switches to that project as enter does
// (the active project just closes the screen) and has the inbox open on the
// item once that project's snapshot is in memory; nothing is read here.
func (m Model) answerOldest() (tea.Model, tea.Cmd) {
	r, ok := oldestBlockingRow(m.allProjectsRows)
	if !ok {
		m.notify(notifyInfo, nothingBlockingNotice)
		return m, nil
	}
	if m.isActiveProjectRoot(r.Root) {
		m.allProjectsOpen = false
		if m.buildKnown {
			return m.openInboxOn(r.Wait.ItemID), nil
		}
		m.inboxFollow = r.Wait.ItemID
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
		nm.previewOnNextLoad = true
		nm.recordProjectOpen(path)
	}
	// After the switch, which clears any earlier follow.
	nm.inboxFollow = r.Wait.ItemID
	return nm, cmd
}

// nothingBlockingNotice is `a` with no blocking item in any project.
const nothingBlockingNotice = "Nothing in any inbox holds a task up."

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
		kind, text := pauseNotice(m.leaderKey.name, msg.already)
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
