package ui

import (
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/buildview"
)

// This file is how the dashboard learns about the project's build: the master
// plan's tasks with their ledger status, the inbox and the last usage reading.
// It only fetches and stores. What is drawn from the snapshot lives with each
// view.
//
// The reading is done by Deps.LoadBuild, which the root package wires in. It
// is not done here for two reasons. The code that knows where a project's
// build files are (the main checkout lookup, the plan and ledger loader) lives
// in the root package, and this package must not import it. And a seam that
// reads the real filesystem is never defaulted (see the end of withDefaults),
// so a Model built with zero Deps has no build data and touches nothing.

// BuildSession is what the build loader needs to know about one session row.
// The model fills it from the rows it already holds, so the loader does not
// run discovery a second time on every tick: discovery lists git worktrees and
// reads several files per session, and the answer would be the same one.
type BuildSession struct {
	Name string
	// Phase is the session's lifecycle phase, "" when it has none to trust.
	Phase string
	// Stale is true when the phase cannot be trusted: status.json is stale,
	// or the session is a one-off that has no status.json at all.
	Stale         bool
	AwaitingInput bool
	HostRunning   bool
}

// buildLoadedMsg carries one finished read. gen is the project generation the
// read was started under (Model.projectGen), so a read that crosses a project
// switch is dropped when it lands and never shows the old project's build
// under the new project's name.
type buildLoadedMsg struct {
	snap buildview.Snapshot
	gen  int
}

// buildSessions copies what the loader needs out of the session rows. A
// placeholder row for a session still being created is left out: only its
// name is filled in, and it has no phase to report.
//
// The stale rule matches the one the web page uses for the same rows, so a
// running task reads the same on both: a one-off session has an empty phase
// without being stale, and is passed on as stale so its phase shows as not
// known and not as a blank.
func (m Model) buildSessions() []BuildSession {
	if len(m.sessions) == 0 {
		return nil
	}
	out := make([]BuildSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.Pending {
			continue
		}
		out = append(out, BuildSession{
			Name:          s.Name,
			Phase:         string(s.Phase),
			Stale:         s.StatusStale || s.Scratch,
			AwaitingInput: s.AwaitingInput,
			HostRunning:   s.HostRunning,
		})
	}
	return out
}

// buildCmd reads the build snapshot for the active project. It returns nil
// when no loader is wired, so a dashboard with no build data issues no command
// at all and behaves exactly as it did before this existed.
//
// The loader runs inside the returned closure, on Bubble Tea's command
// goroutine. Nothing here reads a file on the Update goroutine: the session
// rows are copied from memory before the closure is built, because the closure
// must not read the Model's slice while Update is free to replace it.
//
// The sessions are the ones from the previous load, since this command is
// issued alongside loadCmd and not after it. A phase can therefore be one tick
// (two seconds) old, which is the same age everything else on screen has.
//
// The in-flight guard follows stackCmd's (stack.go). The loader reads a
// handful of small files and runs one git command, so it normally returns in
// milliseconds; the guard is for the day it does not (a stalled disk, a
// network mount), when unguarded ticks would pile reads up behind the stuck
// one. It is released in this goroutine and not when the message is handled,
// so a dropped message cannot leave it held.
func (m Model) buildCmd() tea.Cmd {
	load := m.deps.LoadBuild
	if load == nil {
		return nil
	}
	repo := m.repo
	gen := m.projectGen
	sessions := m.buildSessions()
	refreshing := m.buildRefreshing
	return func() tea.Msg {
		// A nil guard is a Model built as a bare struct literal and not by
		// New, which only a test does, one read at a time.
		if refreshing != nil {
			if !refreshing.CompareAndSwap(false, true) {
				return nil
			}
			defer refreshing.Store(false)
		}
		return buildLoadedMsg{snap: load(repo, sessions), gen: gen}
	}
}

// applyBuildLoaded stores a finished read, unless it belongs to a project the
// dashboard has since left. Everything it does is in memory: the read itself
// happened in the command.
func (m Model) applyBuildLoaded(msg buildLoadedMsg) Model {
	if msg.gen != m.projectGen {
		return m
	}
	m.build = msg.snap
	m.buildKnown = true
	// An open inbox overlay follows the files: it takes the new items here,
	// keeping its cursor and anything typed (refreshInbox).
	m.refreshInbox()
	// The snapshot says which sessions the daemon paused for usage, and the
	// session rows carry that fact themselves (stampUsagePauses). The list is
	// rebuilt only when a row changed, which is rare: a rebuild re-runs the
	// selection and scroll bookkeeping a session load does, and doing that on
	// every two-second tick for nothing would be work with no effect.
	if m.stampUsagePauses() {
		m.rebuildListItems(m.listSelection())
		return m
	}
	// The Build tab's lines come from the snapshot, so the viewport the scroll
	// keys move is re-seeded here, as rebuildListItems does for a session
	// load. Without it the clamp ceiling would be the previous snapshot's.
	m.syncDetailViewport()
	return m
}

// forgetBuild drops the snapshot on a project switch, so the outgoing
// project's tasks, inbox and paused sessions are not shown as the incoming
// project's until its first read lands.
//
// The guard is replaced, not reset. A read still running for the outgoing
// project holds the old guard and releases it when it ends; with a shared
// guard it would hold back the incoming project's first read, which would then
// wait a whole tick for nothing.
func (m *Model) forgetBuild() {
	m.build = buildview.Snapshot{}
	m.buildKnown = false
	if m.buildRefreshing != nil {
		m.buildRefreshing = new(atomic.Bool)
	}
}
