// tasktokens.go is how the Build tab learns how many tokens the task under its
// cursor used (T7.12): the record type the root package fills in, the read
// issued on the refresh tick and when the cursor lands on another task, and
// the `t` key (the leader's T until T16.2) that opens the record in an overlay
// (tasktokensoverlay.go). It also gives the cursor's row its short total.
//
// Nothing here reads a file. The reading is Deps.LoadTaskTokens, which
// `afk dash` wires to the code behind `afk tokens <task>`, so the overlay
// shows the command's own lines and refuses in its own words. This package
// cannot import the command package or the token ledger's, which is why the
// seam exists and why TaskTokens is declared here and filled in there. It is
// the builddata.go pattern: a generation stamp, an in-flight guard, the read
// run inside a tea.Cmd and never on the Update goroutine or in a View.
package ui

import (
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
)

// TaskTokensState is what the token ledger holds for one task.
//
// TaskTokensNone is the zero value on purpose: a record the seam left empty
// says "nothing recorded", which is never wrong to show, and gives the row no
// total and the overlay no table.
type TaskTokensState int

const (
	// TaskTokensNone: there is no ledger yet, or it holds nothing for the
	// task. Planning sessions record no tokens, so this is the usual answer
	// for a task that has not run.
	TaskTokensNone TaskTokensState = iota
	// TaskTokensRecorded: the ledger has lines for the task, and Lines and
	// Compact hold the command's table.
	TaskTokensRecorded
	// TaskTokensUnreadable: the ledger exists and could not be read; Err
	// says why in the reader's own words.
	TaskTokensUnreadable
)

// TaskTokens is one task's token record as the dashboard shows it. The root
// package fills it from the same entry point `afk tokens <task>` prints
// through, so every line is the command's.
type TaskTokens struct {
	State TaskTokensState
	// Task is the task id as the command prints it.
	Task string
	// Path is the token ledger the record was read from.
	Path string
	// Err is why an unreadable ledger could not be read, worded as the
	// command words it ("reading ledger <path>: …").
	Err string
	// Short is the abbreviated weighted total with its unit word
	// ("2.9M weighted"), for the cursor's row.
	Short string
	// Lines is exactly what the command prints: "Task <id>", then the full
	// table. Compact is "Task <id>" and the same rows with fewer columns, for
	// a box too narrow for Lines.
	Lines, Compact []string
}

// The action's own words. Constants because the tests quote them.
const (
	// taskTokensNoSeam is the row's reason when the seam is not wired.
	taskTokensNoSeam = "this dashboard cannot read the token ledger"
	// taskTokensNoTask is the row's reason on a Build tab with no task under
	// the cursor (an empty plan, or a plan that cannot be read).
	taskTokensNoTask = "no task selected"
	// taskTokensNoneText is what the overlay says under "Task <id>" for a
	// task the ledger holds nothing for.
	taskTokensNoneText = "no tokens recorded for this task yet"
)

// taskTokensLoadedMsg carries one finished read. gen and root are the project
// generation and main checkout it was started under, and task the cursor's
// task then, so a read that crosses a project switch or a cursor move is
// recognised when it lands.
type taskTokensLoadedMsg struct {
	gen  int
	root string
	task string
	rec  TaskTokens
}

// taskTokensBlockedReason is the row's reason, and the action asks it again
// when it runs, so the row and the action cannot disagree. The order is
// taskStatusBlockedReason's, for the same reasons: the seam first, because
// without it opening the Build tab would not help, and the task last, because
// only on the Build tab with the right side focused is there a cursor.
func taskTokensBlockedReason(c leaderConds) string {
	switch {
	case !c.canLoadTaskTokens:
		return taskTokensNoSeam
	case !c.buildTabFocused:
		return taskStatusOpenBuildFirst(c.leader)
	case !c.buildTaskSelected:
		return taskTokensNoTask
	}
	return ""
}

// taskTokensCmd reads the token record of the task under the Build tab's
// cursor. It returns nil with no seam, no build root (no snapshot yet, or
// none since a project switch) or no cursor task, so an unwired dashboard
// issues nothing.
//
// The root, the task and the generation are captured here, on the Update
// goroutine, because the closure must not read the Model while Update is free
// to replace it. The read is not tied to the cursor being drawn: the cursor
// keeps its task while the list has the keyboard, and reading then means the
// total is already there when the owner moves right.
//
// The guard follows buildCmd's: the ledger is one small file and the read
// normally takes milliseconds, but a stalled disk must not have the ticker
// pile reads up behind it. It is released in this goroutine, so a dropped
// message cannot leave it held. A read refused by the guard is not lost: the
// one in flight lands, and applyTaskTokensLoaded reads again if the cursor
// has moved since.
func (m Model) taskTokensCmd() tea.Cmd {
	load := m.deps.LoadTaskTokens
	root, task, gen := m.build.Root, m.buildCursorID, m.projectGen
	if load == nil || root == "" || task == "" {
		return nil
	}
	loading := m.taskTokensLoading
	return func() tea.Msg {
		// A nil guard is a Model built as a bare struct literal, which only a
		// test does.
		if loading != nil {
			if !loading.CompareAndSwap(false, true) {
				return nil
			}
			defer loading.Store(false)
		}
		return taskTokensLoadedMsg{gen: gen, root: root, task: task, rec: load(root, task)}
	}
}

// applyTaskTokensLoaded stores a finished read, unless it is stale.
//
// A read for another generation or another build root belongs to a project
// the dashboard has left and is dropped without a word. A read for a task the
// cursor has since left is dropped too, and the cursor's task is read again
// at once rather than at the next tick: an owner who moved and pressed the
// leader then T is waiting on that read. Only one record is held, the one for
// taskTokensID, because only the cursor's row shows a total.
//
// A pending open (the action ran before the record was held) opens here when
// this is its task and the owner is still where the action was run: the Build
// tab with the right side focused and nothing else over it. Otherwise it is
// dropped, since an overlay appearing over whatever the owner moved on to
// would take the keyboard from them.
func (m Model) applyTaskTokensLoaded(msg taskTokensLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen || msg.root != m.build.Root {
		return m, nil
	}
	if msg.task != m.buildCursorID {
		if m.taskTokensPending != m.buildCursorID {
			m.taskTokensPending = ""
		}
		return m, m.taskTokensCmd()
	}
	m.taskTokens = msg.rec
	m.taskTokensID = msg.task
	pending := m.taskTokensPending
	m.taskTokensPending = ""
	if pending == msg.task && taskTokensBlockedReason(m.leaderConditions()) == "" &&
		activeInputMode(m.modeSnapshot()) == inputDetail {
		m.openTaskTokensOverlay()
	}
	// The cursor's row reads its total from the record, so the viewport the
	// scroll keys move is re-seeded, as applyBuildLoaded does.
	m.syncDetailViewport()
	return m, nil
}

// forgetTaskTokens drops the record on a project switch, so the outgoing
// project's total is not drawn on the incoming project's row and a pending
// open does not open over it. The guard is replaced, not reset, for the
// reason forgetBuild gives.
func (m *Model) forgetTaskTokens() {
	m.taskTokens = TaskTokens{}
	m.taskTokensID = ""
	m.taskTokensPending = ""
	if m.taskTokensLoading != nil {
		m.taskTokensLoading = new(atomic.Bool)
	}
}

// buildCursorTotal is the short total the cursor's row ends with: the held
// record's, when it is for the cursor's task and has tokens recorded. It is
// "" with no cursor drawn, no record for this task yet, nothing recorded, or
// a ledger that could not be read, and the row then shows no total.
func (m Model) buildCursorTotal() string {
	if !m.buildCursorShown() || m.buildCursorID == "" || m.taskTokensID != m.buildCursorID ||
		m.taskTokens.State != TaskTokensRecorded {
		return ""
	}
	return m.taskTokens.Short
}

// openTaskTokens is `t` on the Build tab: the overlay for the task
// under the cursor. With the record already held (the refresh tick read it)
// it opens at once and reads nothing. Otherwise it reads now and opens when
// that task's record lands (applyTaskTokensLoaded); the seam is still only
// called inside the command.
func (m Model) openTaskTokens() (tea.Model, tea.Cmd) {
	if reason := taskTokensBlockedReason(m.leaderConditions()); reason != "" {
		m.notify(notifyInfo, reasonSentence(reason))
		return m, nil
	}
	if m.taskTokensID == m.buildCursorID {
		m.openTaskTokensOverlay()
		return m, nil
	}
	m.taskTokensPending = m.buildCursorID
	return m, m.taskTokensCmd()
}
