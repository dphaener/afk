package ui

// needsyoulanding.go wires the pure needs-you order (needsyou.go) to the
// Model (T16.6): the one method that builds the order from what is in memory,
// where the owner is when they press `]` or `[`, and what going to an item
// does. It is kept apart from model.go so the wiring there stays a few lines.
//
// Nothing here reads a file or dials: the other projects' rows come from the
// every-project cache (m.everyProject, read in a Cmd at every level) and their
// sessions from the scan cache (m.projectSessions), so a key never waits on a
// read. An agent landing dials through connect, the same entrance enter uses,
// which itself runs the dial in a Cmd.

import (
	"path/filepath"
	"slices"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/projectrows"
)

// nothingNeedsYouNotice is `]` or `[` with nothing in any project needing
// the owner.
const nothingNeedsYouNotice = "Nothing needs you right now."

// needsYouActivePath is the path the order calls the active project: the
// active switcher entry's, or, on a dashboard handed a repo but no project
// list, the repo itself, whose sessions are m.sessions all the same.
func (m Model) needsYouActivePath() string {
	if p := m.activeProjectPath(); p != "" {
		return p
	}
	if m.hasActiveProject() {
		return m.repo
	}
	return ""
}

// needPathForRoot is the switcher path of the project whose main checkout is
// root, or root itself when the switcher has no entry for it. The rows carry
// the main checkout while the switcher's start-up entry keeps the folder the
// dashboard was started in (projectPathForRoot); keying the order by the
// switcher's path is what lets a landing switch to it and lets the active
// project be told apart by path.
func (m Model) needPathForRoot(root string) string {
	if p, ok := m.projectPathForRoot(root); ok {
		return p
	}
	return root
}

// needsYouNow is everything that needs the owner, in the order `]` walks. It
// is the one builder every caller uses (the keys, the Agent's band and the
// top bar), so the three can never disagree about what comes next. The jump
// search builds its needs-you rows from the same projects
// (needsYouProjectsNow), so `/` puts first what `]` goes to.
func (m Model) needsYouNow() []needItem {
	return needsYouItems(needsYouFacts{Projects: m.needsYouProjectsNow()})
}

// needsYouProjectsNow is every project the dashboard knows, in home-screen
// order, with its sessions and its cached home-screen row: the facts
// needsYouNow and the jump search are built from.
//
// The active project's sessions are its fresh m.sessions (the 2s tick);
// every other project's come from the scan cache, which lags by up to a
// minute, and every project's row from the every-project cache.
func (m Model) needsYouProjectsNow() []needsYouProject {
	active := m.needsYouActivePath()
	listed := make([]needsYouProject, 0, len(m.projects)+1)
	sawActive := false
	for _, p := range m.projects {
		np := needsYouProject{Path: p.Path, Name: p.Name}
		if p.Path == active {
			np.Sessions = m.sessions
			sawActive = true
		} else {
			states := m.projectSessions[p.Path]
			np.Sessions = make([]Session, 0, len(states))
			for _, st := range states {
				np.Sessions = append(np.Sessions, Session{SessionState: st})
			}
		}
		listed = append(listed, np)
	}
	if !sawActive && active != "" {
		listed = append(listed, needsYouProject{Path: active, Name: filepath.Base(active), Sessions: m.sessions})
	}
	rows := make([]projectrows.Row, len(m.everyProject.Rows))
	for i, r := range m.everyProject.Rows {
		r.Root = m.needPathForRoot(r.Root)
		rows[i] = r
	}
	return needsYouProjectOrder(rows, listed)
}

// needHereNow is where the owner is (A2): the agent's session in the Agent,
// the selected session at Project, the selected row's project at All
// projects. The agent's session is the selected one, because the Agent only
// ever shows the pane of the selection (shouldRenderPane).
//
// At is the item the last `]` or `[` landed on, while the owner is still at
// the level and in the project it landed in; a move of the cursor clears it
// (clearNeedAt). Without it a landing that leaves no session in view (a plan,
// an inbox item, a gone project) would land on the same item again.
func (m Model) needHereNow() needHere {
	lv := m.level()
	var h needHere
	switch lv {
	case levelAllProjects:
		if r, ok := m.selectedProjectRow(); ok && m.allProjectsOpen {
			h.Project = m.needPathForRoot(r.Root)
		}
	default:
		h.Project = m.needsYouActivePath()
		if s, ok := m.selected(); ok {
			h.Session = s.Name
		}
	}
	if m.needAt != (needKey{}) && m.needAtLevel == lv && m.needAtProject == h.Project {
		h.At = m.needAt
	}
	return h
}

// clearNeedAt forgets the last landing: the owner moved, so where they are
// is what the next `]` starts from.
func (m *Model) clearNeedAt() {
	m.needAt = needKey{}
	m.needAtProject = ""
}

// nextNeedingYouLabel is the Agent's band's right part: the item `]` would
// go to from the session current of the project active, named by needLabel,
// or "" when nothing needs the owner or the next is the session in view
// itself (the only thing needing the owner is what they are looking at).
func nextNeedingYouLabel(items []needItem, active, current string) string {
	it, ok := stepNeedsYou(items, needHere{Project: active, Session: current}, true)
	if !ok {
		return ""
	}
	if (it.Kind == needSession || it.Kind == needBroken) && it.ProjectPath == active && it.Session == current {
		return ""
	}
	return needLabel(it, active)
}

// stepNeedsYouKey is `]` (forward) or `[`: go to the next or previous thing
// that needs the owner, from where they are. Nothing needing them leaves
// everything where it is and says so.
func (m Model) stepNeedsYouKey(forward bool) (tea.Model, tea.Cmd) {
	it, ok := stepNeedsYou(m.needsYouNow(), m.needHereNow(), forward)
	if !ok {
		m.notify(notifyInfo, nothingNeedsYouNotice)
		return m, nil
	}
	nm, cmd := m.landOnNeed(it)
	nm.needAt = it.key()
	nm.needAtProject = it.ProjectPath
	nm.needAtLevel = nm.level()
	return nm, cmd
}

// landOnNeed goes to item, where needLandingFor says (A3).
func (m Model) landOnNeed(item needItem) (Model, tea.Cmd) {
	l := needLandingFor(item)
	switch l.Place {
	case landAgent:
		return m.landNeedAgent(item)
	case landInbox:
		return m.landNeedInbox(item)
	case landAllProjects:
		return m.landNeedAllProjects(item)
	}
	return m.landNeedProject(item, l.Select)
}

// selectByName puts the session list's cursor on name, as a click does in
// applyFocusRequest: a filter that would hide it is dropped first, since
// the owner asked for this row by going to it.
func (m *Model) selectByName(name string) bool {
	i, ok := m.sessionIndex(name)
	if !ok {
		return false
	}
	if m.list.FilterState() != list.Unfiltered || m.list.SettingFilter() {
		m.list.ResetFilter()
	}
	m.selectSessionIndex(i)
	m.resetDetailScroll()
	m.rememberSelectedRow()
	return true
}

// switchForNeed switches to the item's project, as enter on its home-screen
// row does, and counts the open. It reports false when the switcher has no
// entry for the project yet (the scan is slower than the rows), after saying
// so.
func (m Model) switchForNeed(item needItem) (Model, []tea.Cmd, bool) {
	if _, ok := indexOfProject(m.projects, item.ProjectPath); !ok {
		m.notify(notifyInfo, allProjectsNotListedNotice(item.ProjectName))
		return m, nil, false
	}
	nm, cmds := m.switchProjectCmds(item.ProjectPath)
	nm.recordProjectOpen(item.ProjectPath)
	return nm, cmds, true
}

// landNeedAgent opens a waiting session's agent.
//
// In the active project it is enter on the row (openAgent), so a session
// with no live agent opens its details instead. In another project it takes
// applyFocusRequest's shape: the switch first (every per-project command
// closes over m.repo, and the switch clears the one-shot arms), then the
// name for the first load to select, then the dial from the scan cache's
// worktree, or the details overlay when no host runs. No preview is armed:
// it would race a second dial against this one, and the host answers a
// second client by dropping the first.
func (m Model) landNeedAgent(item needItem) (Model, tea.Cmd) {
	if item.ProjectPath == m.needsYouActivePath() {
		m.allProjectsOpen = false
		// The list has the keyboard while the row is chosen; openAgent puts
		// it on the agent when there is one, and leaves it here when the
		// row opens its details instead.
		m.focus = focusList
		if !m.selectByName(item.Session) {
			// Gone from the list since the order was built: say nothing
			// more than enter on an empty row would.
			return m, nil
		}
		nm, cmd := m.openAgent()
		return nm.(Model), cmd
	}
	m, cmds, ok := m.switchForNeed(item)
	if !ok {
		return m, nil
	}
	m.selectOnNextLoad = item.Session
	if !item.HostRunning {
		// No agent to connect to: its details over the Project, as a click
		// on a stopped session's notification opens them. previewGen is
		// bumped because nothing else here moves the selection through
		// schedulePreview, so an old timer must not fire for another row.
		m.previewGen++
		m.dropPaneForOtherRows()
		m.view = viewAgent
		m.focus = focusList
		m.openDetails(levelProject, item.Session)
		return m, tea.Batch(cmds...)
	}
	return m, tea.Batch(append(cmds, m.connect(paneOpenRequest{
		name:     item.Session,
		worktree: item.WorktreePath,
		scratch:  item.Scratch,
	}))...)
}

// openInboxItemFor is how a blocking inbox item is opened once its project
// is the active one: today's inbox list on that item, or, before the
// project's snapshot is in memory, the follow that opens it when the
// snapshot lands (applyBuildLoaded). It is one function so T16.7 can swap in
// the answer screen in one place.
func (m Model) openInboxItemFor(id string) Model {
	if m.buildKnown {
		return m.openInboxOn(id)
	}
	m.inboxFollow = id
	return m
}

// landNeedInbox opens an item's project with its inbox on the item, as `a`
// at All projects does (answerOldest): the switch clears any earlier follow,
// so the follow is set after it.
func (m Model) landNeedInbox(item needItem) (Model, tea.Cmd) {
	m.focus = focusList
	if item.ProjectPath == m.needsYouActivePath() {
		m.allProjectsOpen = false
		return m.openInboxItemFor(item.ItemID), nil
	}
	m, cmds, ok := m.switchForNeed(item)
	if !ok {
		return m, nil
	}
	m.previewOnNextLoad = true
	m.inboxFollow = item.ItemID
	return m, tea.Batch(cmds...)
}

// landNeedProject opens the item's project at Project: for a plan to
// approve, as enter on its row does (a switch restores the row the owner was
// last on and previews it); for a broken session, with that session
// selected, and no preview armed, since a restore moving the cursor to
// another row first would preview the wrong one.
func (m Model) landNeedProject(item needItem, sel string) (Model, tea.Cmd) {
	m.focus = focusList
	if item.ProjectPath == m.needsYouActivePath() {
		m.allProjectsOpen = false
		// The broken row itself, not a healthy one of the same name: a
		// worktree fault can share its branch's name with a session, and
		// sessionIndex (selectByName) skips faults.
		broken := func(s Session) bool {
			return s.Name == sel && !s.Pending && !showsAwaitingInput(s) && sessionGroupOf(s) == groupNeedsYou
		}
		if i := slices.IndexFunc(m.sessions, broken); sel != "" && i >= 0 {
			if m.list.FilterState() != list.Unfiltered || m.list.SettingFilter() {
				m.list.ResetFilter()
			}
			m.selectSessionIndex(i)
			m.resetDetailScroll()
			m.rememberSelectedRow()
			cmd := m.schedulePreview()
			return m, cmd
		}
		return m, nil
	}
	m, cmds, ok := m.switchForNeed(item)
	if !ok {
		return m, nil
	}
	if sel != "" {
		m.selectOnNextLoad = sel
	} else {
		m.previewOnNextLoad = true
	}
	return m, tea.Batch(cmds...)
}

// landNeedAllProjects is a gone or unreadable project: it cannot be opened,
// so the home screen opens (or stays) with its row selected and the notice
// enter on that row gives. Opening the screen seeds its rows from the
// every-project cache, so the row can be selected now rather than after the
// screen's own read, which then keeps the cursor on it.
func (m Model) landNeedAllProjects(item needItem) (Model, tea.Cmd) {
	m.focus = focusList
	var cmd tea.Cmd
	if !m.allProjectsOpen {
		if m.deps.LoadAllProjects == nil {
			return m, nil
		}
		m, cmd = m.openAllProjects()
		m.allProjectsRows = allProjectsOrder(slices.Clone(m.everyProject.Rows))
		m.allProjectsKnown = len(m.allProjectsRows) > 0
	}
	match := func(r projectrows.Row) bool { return m.needPathForRoot(r.Root) == item.ProjectPath }
	if i := slices.IndexFunc(m.allProjectsRows, match); i >= 0 {
		m.allProjectsCursor = i
		r := m.allProjectsRows[i]
		if r.State == projectrows.StateUnreadable {
			m.notify(notifyWarn, allProjectsUnreadableNotice(r))
		} else {
			m.notify(notifyWarn, allProjectsGoneNotice(r.Name))
		}
	}
	return m, cmd
}
