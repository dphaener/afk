package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
)

// taskStatusCall is one call of the SetTaskStatus seam.
type taskStatusCall struct {
	root, id, status string
	replacedBy       []string
}

// taskStatusSeam records every call of the seam and gives the reply the test
// wants. It never writes anything.
type taskStatusSeam struct {
	calls []taskStatusCall
	res   TaskStatusResult
}

func (s *taskStatusSeam) set(root, id, status string, replacedBy []string) TaskStatusResult {
	s.calls = append(s.calls, taskStatusCall{root, id, status, replacedBy})
	return s.res
}

// taskStatusModel is the Build tab of buildTabSnap with the keyboard on the
// right, the cursor on its current task (T1.2) and the seam wired to a
// recorder.
func taskStatusModel(t *testing.T) (Model, *taskStatusSeam) {
	t.Helper()
	seam := &taskStatusSeam{res: TaskStatusResult{Outcome: TaskStatusChanged, Text: "T3.2: failed → pending"}}
	m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
	m.deps.SetTaskStatus = seam.set
	m, _ = pressAction(t, m, keyB)
	if !m.buildCursorShown() {
		t.Fatalf("setup: the Build tab's cursor is not shown (tab=%v focus=%v)", m.activeDetailTab(), m.focus)
	}
	m.clearStatus()
	return m, seam
}

// taskStatusCursorOn moves the Build tab's cursor to the task id with the
// arrow keys, the way the owner does.
func taskStatusCursorOn(t *testing.T, m Model, id string) Model {
	t.Helper()
	want := buildRowIndex(m.build.Rows, id)
	if want < 0 {
		t.Fatalf("setup: no row %s", id)
	}
	for m.buildCursorIndex != want {
		k := keyDown
		if m.buildCursorIndex > want {
			k = keyUp
		}
		before := m.buildCursorIndex
		m, _ = update(t, m, k)
		if m.buildCursorIndex == before {
			t.Fatalf("setup: the cursor is stuck on row %d", before)
		}
	}
	return m
}

// taskStatusPicker opens the picker on task id with ctrl+space S.
func taskStatusPicker(t *testing.T, m Model, id string) Model {
	t.Helper()
	m = taskStatusCursorOn(t, m, id)
	m, _ = pressAction(t, m, keyRune('S'))
	if m.modal != inputPickTaskStatus || m.taskStatus.id != id {
		t.Fatalf("setup: ctrl+space S gave modal %v for %q, want the picker for %s", m.modal, m.taskStatus.id, id)
	}
	return m
}

// taskStatusChoose moves the picker's highlight to status with the arrow keys
// and presses enter.
func taskStatusChoose(t *testing.T, m Model, status string) (Model, tea.Cmd) {
	t.Helper()
	want := slices.Index(taskStatusChoices(), status)
	if want < 0 {
		t.Fatalf("setup: %q is not a status", status)
	}
	for m.taskStatusPick != want {
		k := keyDown
		if m.taskStatusPick > want {
			k = keyUp
		}
		m, _ = update(t, m, k)
	}
	return update(t, m, keyEnter)
}

// TestTaskStatusBlockedReason is the row's reason, which the action asks
// again when it runs. Load-bearing rows: "no seam wins over everything" (the
// reason must not send the owner to a tab where it still cannot run), "from
// the list" (the cursor is drawn only with the right side focused, so the
// action cannot know the task) and "no task" (an empty or unreadable plan).
// "over the all-projects screen" pins that the screen's own rule still
// decides there.
func TestTaskStatusBlockedReason(t *testing.T) {
	ok := leaderConds{canSetTaskStatus: true, buildTabFocused: true, buildTaskSelected: true}
	with := func(f func(*leaderConds)) leaderConds { c := ok; f(&c); return c }
	tests := []struct {
		name  string
		conds leaderConds
		want  string
	}{
		{"runs on a selected task", ok, ""},
		{"no seam wins over everything", leaderConds{}, taskStatusNoSeam},
		{"from the list", with(func(c *leaderConds) { c.buildTabFocused = false; c.buildTaskSelected = false }),
			"open the Build tab first (ctrl+space b)"},
		{"no task", with(func(c *leaderConds) { c.buildTaskSelected = false }), taskStatusNoTask},
		{"over the all-projects screen", with(func(c *leaderConds) { c.allProjectsOpen = true }), allProjectsCloseFirst},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := leaderBlockedReason(actionTaskStatus, tt.conds); got != tt.want {
				t.Fatalf("reason %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTaskStatusRowDimmed drives the dialog from a real model. Every row is
// load-bearing: each is a place the owner can press ctrl+space S where the
// action must not open the picker, and the seam must never be called. "the
// seam is nil" is the zero-Deps guarantee: a dashboard not wired to the
// ledger cannot write it.
func TestTaskStatusRowDimmed(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (Model, *taskStatusSeam)
		want  string
	}{
		{"the seam is nil", func(t *testing.T) (Model, *taskStatusSeam) {
			m, seam := taskStatusModel(t)
			m.deps.SetTaskStatus = nil
			return m, seam
		}, taskStatusNoSeam},
		{"the session list has the keyboard", func(t *testing.T) (Model, *taskStatusSeam) {
			seam := &taskStatusSeam{}
			m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
			m.deps.SetTaskStatus = seam.set
			return m, seam
		}, "open the Build tab first (ctrl+space b)"},
		{"the Build tab has no tasks", func(t *testing.T) (Model, *taskStatusSeam) {
			seam := &taskStatusSeam{}
			m := withBuild(t, testModel(sess("alpha")), buildview.Snapshot{Root: "/repo", HasBuild: true})
			m.deps.SetTaskStatus = seam.set
			m, _ = pressAction(t, m, keyB)
			return m, seam
		}, taskStatusNoTask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, seam := tt.setup(t)
			m, _ = update(t, m, leaderKeyPress)
			r, ok := leaderRowFor(m.leader.rows, actionTaskStatus)
			if !ok || r.blocked != tt.want {
				t.Fatalf("row blocked %q, want %q", r.blocked, tt.want)
			}
			m, cmd := update(t, m, keyRune('S'))
			for _, msg := range collectMsgs(t, cmd) {
				m, _ = update(t, m, msg)
			}
			if m.modal != inputNormal || len(seam.calls) != 0 {
				t.Fatalf("a dimmed row opened %v or called the seam %d times", m.modal, len(seam.calls))
			}
		})
	}
	t.Run("the action refuses on its own", func(t *testing.T) {
		m := withBuild(t, testModel(sess("alpha")), buildTabSnap())
		m.deps.SetTaskStatus = (&taskStatusSeam{}).set
		m, cmd := runAction(m, actionTaskStatus)
		if cmd != nil || m.modal != inputNormal {
			t.Fatalf("modal %v, cmd %v; want nothing but a notice", m.modal, cmd != nil)
		}
		if m.status != "Open the Build tab first (ctrl+space b)." {
			t.Errorf("status %q", m.status)
		}
	})
}

// TestTaskStatusPicker checks the picker's list. Load-bearing: all seven
// statuses in the ledger's order, "(now)" on the current one only, and the
// highlight starting on it, for a task in the middle of the list and for one
// at each end of the lifecycle.
func TestTaskStatusPicker(t *testing.T) {
	tests := []struct {
		id, status string
	}{
		{"T3.2", "failed"},
		{"T1.2", "running"},
		{"T4.1", "pending"},
		{"T5.1", "superseded"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			m, _ := taskStatusModel(t)
			m = taskStatusPicker(t, m, tt.id)
			var rows []string
			for _, ln := range m.taskStatusPickDialog().lines {
				ln = xansi.Strip(ln)
				if strings.HasPrefix(ln, "› ") || strings.HasPrefix(ln, "  ") {
					rows = append(rows, ln)
				}
			}
			want := []string{"pending", "running", "shipping", "parked", "done", "failed", "superseded"}
			if len(rows) != len(want) {
				t.Fatalf("picker rows %q, want the seven statuses", rows)
			}
			for i, w := range want {
				label := w
				if w == tt.status {
					label += " (now)"
				}
				mark := "  "
				if w == tt.status {
					mark = "› "
				}
				if rows[i] != mark+label {
					t.Errorf("row %d = %q, want %q", i, rows[i], mark+label)
				}
			}
			if h := m.taskStatusPickDialog().hint; h != "↑/k: up · ↓/j: down · enter: choose · esc: cancel" {
				t.Errorf("hint %q", h)
			}
		})
	}
}

// TestTaskStatusConfirmText is the confirm's words. Every row is load-bearing:
// each new status has its own consequence, superseded names its replacements,
// and a running or shipping task is warned about first, because changing the
// record does not stop the agent.
func TestTaskStatusConfirmText(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		ids      []string
		want     string
	}{
		{"pending", "failed", "pending", nil,
			"T2.3: failed → pending.\nThe build loop may start it on its next pass."},
		{"done", "failed", "done", nil,
			"T2.3: failed → done.\nTasks that depend on it may start."},
		{"failed", "pending", "failed", nil,
			"T2.3: pending → failed.\nThe build loop will not start it again until you set it back to pending."},
		{"parked", "pending", "parked", nil,
			"T2.3: pending → parked.\nThe build loop will wait until its inbox items are answered."},
		{"superseded with ids", "failed", "superseded", []string{"T2.3a", "T2.3b"},
			"T2.3: failed → superseded (replaced by T2.3a, T2.3b).\nTasks that depend on it will wait for the tasks that replace it."},
		{"to running", "pending", "running", nil,
			"T2.3: pending → running.\nThe build loop will treat it as having a session; use this only to repair the ledger."},
		{"to shipping", "pending", "shipping", nil,
			"T2.3: pending → shipping.\nThe build loop will treat it as having a session; use this only to repair the ledger."},
		{"from running", "running", "pending", nil,
			"An agent is working on T2.3 now; changing its status does not stop it.\nT2.3: running → pending.\nThe build loop may start it on its next pass."},
		{"from shipping", "shipping", "done", nil,
			"An agent is working on T2.3 now; changing its status does not stop it.\nT2.3: shipping → done.\nTasks that depend on it may start."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskStatusConfirmText("T2.3", tt.old, tt.new, tt.ids); got != tt.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestTaskStatusSplitIDs is the replacing-ids step's split. Load-bearing:
// commas, spaces and both together all separate, and nothing typed is no ids,
// which is what keeps the step open.
func TestTaskStatusSplitIDs(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string
	}{
		{"commas", "T1,T2", []string{"T1", "T2"}},
		{"spaces", "T1 T2", []string{"T1", "T2"}},
		{"both", " T1, T2  T3 ,", []string{"T1", "T2", "T3"}},
		{"nothing", "  , ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitTaskIDs(tt.text); !slices.Equal(got, tt.want) {
				t.Fatalf("splitTaskIDs(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// TestTaskStatusConfirm drives the confirm with every answer. Load-bearing
// rows: "n calls nothing", "esc calls nothing" and "enter calls nothing"
// (only y writes), and "y calls the seam once", which also checks the call
// happens in the command and never inside Update, with the copy of the task
// taken when the picker opened.
func TestTaskStatusConfirm(t *testing.T) {
	want := []taskStatusCall{{root: "/repo", id: "T3.2", status: "pending"}}
	tests := []struct {
		name     string
		answer   tea.Msg
		wantCall []taskStatusCall
		wantMode inputMode
		wantStat string
	}{
		{"y calls the seam once", keyRune('y'), want, inputNormal, "T3.2: failed → pending"},
		{"Y works as y", keyRune('Y'), want, inputNormal, "T3.2: failed → pending"},
		{"n calls nothing", keyRune('n'), nil, inputNormal, taskStatusCancelled},
		{"esc calls nothing", keyEsc, nil, inputNormal, taskStatusCancelled},
		{"enter calls nothing and keeps the question", keyEnter, nil, inputConfirmTaskStatus, ""},
		{"a stray key calls nothing and keeps the question", keyRune('x'), nil, inputConfirmTaskStatus, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, seam := taskStatusModel(t)
			m = taskStatusPicker(t, m, "T3.2")
			m, _ = taskStatusChoose(t, m, "pending")
			if m.modal != inputConfirmTaskStatus {
				t.Fatalf("enter on pending gave modal %v, want the confirm", m.modal)
			}
			m, cmd := update(t, m, tt.answer)
			if len(seam.calls) != 0 {
				t.Fatal("the seam ran inside Update")
			}
			for _, msg := range collectMsgs(t, cmd) {
				m, _ = update(t, m, msg)
			}
			if len(seam.calls) != len(tt.wantCall) {
				t.Fatalf("seam calls %+v, want %+v", seam.calls, tt.wantCall)
			}
			for i := range tt.wantCall {
				g, w := seam.calls[i], tt.wantCall[i]
				if g.root != w.root || g.id != w.id || g.status != w.status || !slices.Equal(g.replacedBy, w.replacedBy) {
					t.Errorf("call %+v, want %+v", g, w)
				}
			}
			if m.modal != tt.wantMode {
				t.Errorf("modal %v, want %v", m.modal, tt.wantMode)
			}
			if tt.wantStat != "" && m.status != tt.wantStat {
				t.Errorf("status %q, want %q", m.status, tt.wantStat)
			}
		})
	}
	t.Run("the dialog says the change and the keys", func(t *testing.T) {
		m, _ := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T1.2")
		m, _ = taskStatusChoose(t, m, "pending")
		d := m.taskStatusConfirmDialog()
		text := xansi.Strip(strings.Join(d.lines, " "))
		for _, w := range []string{"An agent is working on T1.2 now", "T1.2: running → pending.", "on its next pass."} {
			if !strings.Contains(text, w) {
				t.Errorf("dialog %q does not say %q", text, w)
			}
		}
		if d.hint != "y: set · n/esc: cancel" {
			t.Errorf("hint %q", d.hint)
		}
	})
}

// TestTaskStatusPickerChoices drives the picker's own answers. Load-bearing
// rows: "the current status calls nothing" (nothing would change, so nothing
// is written), "esc calls nothing", and "superseded when already superseded"
// (its replacing ids may be what the owner wants to change, so it is not
// "already").
func TestTaskStatusPickerChoices(t *testing.T) {
	t.Run("the current status calls nothing", func(t *testing.T) {
		m, seam := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T3.2")
		m, cmd := taskStatusChoose(t, m, "failed")
		if cmd != nil || m.modal != inputNormal || len(seam.calls) != 0 {
			t.Fatalf("modal %v, cmd %v, calls %d; want the picker closed and nothing called", m.modal, cmd != nil, len(seam.calls))
		}
		if m.status != "T3.2 is already failed." || m.statusKind != notifyInfo {
			t.Errorf("notice %v %q", m.statusKind, m.status)
		}
	})
	t.Run("esc calls nothing", func(t *testing.T) {
		m, seam := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T3.2")
		m, cmd := update(t, m, keyEsc)
		if cmd != nil || m.modal != inputNormal || len(seam.calls) != 0 || m.status != taskStatusCancelled {
			t.Fatalf("modal %v, cmd %v, calls %d, status %q", m.modal, cmd != nil, len(seam.calls), m.status)
		}
	})
	t.Run("a stray key is swallowed", func(t *testing.T) {
		m, _ := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T3.2")
		pick := m.taskStatusPick
		m, _ = update(t, m, keyRune('y'))
		if m.modal != inputPickTaskStatus || m.taskStatusPick != pick {
			t.Fatalf("y in the picker gave modal %v pick %d", m.modal, m.taskStatusPick)
		}
	})
	t.Run("the highlight stops at both ends", func(t *testing.T) {
		m, _ := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T4.1")
		m, _ = update(t, m, keyUp)
		if m.taskStatusPick != 0 {
			t.Fatalf("up from the first row gave %d", m.taskStatusPick)
		}
		for range 10 {
			m, _ = update(t, m, keyDown)
		}
		if m.taskStatusPick != len(taskStatusChoices())-1 {
			t.Fatalf("down past the last row gave %d", m.taskStatusPick)
		}
	})
	t.Run("superseded when already superseded asks for the ids", func(t *testing.T) {
		m, _ := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T5.1")
		m, _ = taskStatusChoose(t, m, "superseded")
		if m.modal != inputTaskStatusReplacedBy {
			t.Fatalf("modal %v, want the replacing-ids step", m.modal)
		}
	})
}

// TestTaskStatusReplacedBy drives the replacing-ids step. Load-bearing rows:
// "an empty enter stays open" (a refusal after the dialog closed would mean
// starting again), and "the ids are split and passed on", which also pins
// that they are not checked here: the entry point checks them against the
// plan.
func TestTaskStatusReplacedBy(t *testing.T) {
	open := func(t *testing.T) (Model, *taskStatusSeam) {
		m, seam := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T3.2")
		m, _ = taskStatusChoose(t, m, "superseded")
		if m.modal != inputTaskStatusReplacedBy {
			t.Fatalf("setup: modal %v, want the replacing-ids step", m.modal)
		}
		return m, seam
	}
	t.Run("an empty enter stays open", func(t *testing.T) {
		m, seam := open(t)
		m = typeText(t, m, "  ")
		m, _ = update(t, m, keyEnter)
		if m.modal != inputTaskStatusReplacedBy || len(seam.calls) != 0 {
			t.Fatalf("modal %v, calls %d", m.modal, len(seam.calls))
		}
		want := "Type the ids of the tasks that replace T3.2, for example T3.2a, T3.2b."
		if m.taskStatusNote != want {
			t.Errorf("note %q, want %q", m.taskStatusNote, want)
		}
		if !strings.Contains(xansi.Strip(strings.Join(m.taskStatusReplacedByDialog().lines, "\n")), want) {
			t.Error("the dialog does not show the note")
		}
	})
	t.Run("esc calls nothing", func(t *testing.T) {
		m, seam := open(t)
		m = typeText(t, m, "T9.9")
		m, _ = update(t, m, keyEsc)
		if m.modal != inputNormal || len(seam.calls) != 0 || m.status != taskStatusCancelled {
			t.Fatalf("modal %v, calls %d, status %q", m.modal, len(seam.calls), m.status)
		}
	})
	t.Run("the ids are split and passed on", func(t *testing.T) {
		m, seam := open(t)
		m = typeText(t, m, "T6.1, T6.2 T6.3")
		m, _ = update(t, m, keyEnter)
		if m.modal != inputConfirmTaskStatus {
			t.Fatalf("modal %v, want the confirm", m.modal)
		}
		if !strings.Contains(xansi.Strip(strings.Join(m.taskStatusConfirmDialog().lines, " ")), "(replaced by T6.1, T6.2, T6.3)") {
			t.Error("the confirm does not name the replacing ids")
		}
		m, cmd := update(t, m, keyRune('y'))
		for _, msg := range collectMsgs(t, cmd) {
			m, _ = update(t, m, msg)
		}
		if len(seam.calls) != 1 {
			t.Fatalf("seam calls %d, want 1", len(seam.calls))
		}
		c := seam.calls[0]
		if c.id != "T3.2" || c.status != "superseded" || !slices.Equal(c.replacedBy, []string{"T6.1", "T6.2", "T6.3"}) {
			t.Errorf("call %+v", c)
		}
	})
}

// TestTaskStatusNotices is each outcome's notice. Every row is load-bearing:
// a change, a no-op, a refusal and a broken ledger must not read alike,
// "empty text" must still say something, and "unknown outcome" must not
// claim success.
func TestTaskStatusNotices(t *testing.T) {
	tests := []struct {
		name     string
		res      TaskStatusResult
		wantKind notificationKind
		wantText string
	}{
		{"changed", TaskStatusResult{TaskStatusChanged, "T3.2: failed → pending"}, notifySuccess, "T3.2: failed → pending"},
		{"already", TaskStatusResult{TaskStatusAlready, "T3.2 is already pending"}, notifyInfo, "T3.2 is already pending"},
		{"refused", TaskStatusResult{TaskStatusRefused, "unknown task id T9.9 (not in plan.md)"}, notifyWarn, "unknown task id T9.9 (not in plan.md)"},
		{"failed", TaskStatusResult{TaskStatusFailed, "could not update /repo/.afk/build/ledger.json"}, notifyError, "could not update /repo/.afk/build/ledger.json"},
		{"empty text", TaskStatusResult{TaskStatusChanged, "  "}, notifyWarn, taskStatusNoAnswer},
		{"unknown outcome", TaskStatusResult{"odd", "Something."}, notifyInfo, "Something."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := taskStatusModel(t)
			m, _ = update(t, m, taskStatusSetMsg{gen: m.projectGen, result: tt.res})
			if m.statusKind != tt.wantKind || m.status != tt.wantText {
				t.Errorf("notice %v %q, want %v %q", m.statusKind, m.status, tt.wantKind, tt.wantText)
			}
		})
	}
}

// TestTaskStatusRereadsAndGuards checks what happens around the write.
// Load-bearing rows: "a result re-reads the build" (the row must show the new
// status without waiting for the tick), "a stale result is dropped" (a
// result from a project the dashboard has left must not be told), "one write
// at a time" and "a project switch before y calls nothing".
func TestTaskStatusRereadsAndGuards(t *testing.T) {
	t.Run("a result re-reads the build", func(t *testing.T) {
		m, _ := taskStatusModel(t)
		reads := 0
		m.deps.LoadBuild = func(string, []BuildSession) buildview.Snapshot {
			reads++
			return buildTabSnap()
		}
		m.taskStatusSetting = true
		m, cmd := update(t, m, taskStatusSetMsg{gen: m.projectGen, result: TaskStatusResult{TaskStatusChanged, "done"}})
		if m.taskStatusSetting {
			t.Error("the in-flight flag is still set")
		}
		msgs := collectMsgs(t, cmd)
		if reads != 1 || len(msgs) != 1 {
			t.Fatalf("reads %d, msgs %d; want one build read", reads, len(msgs))
		}
		if _, ok := msgs[0].(buildLoadedMsg); !ok {
			t.Fatalf("the re-read gave %T", msgs[0])
		}
	})
	t.Run("a stale result is dropped", func(t *testing.T) {
		m, _ := taskStatusModel(t)
		m.taskStatusSetting = true
		m, cmd := update(t, m, taskStatusSetMsg{gen: m.projectGen - 1, result: TaskStatusResult{TaskStatusChanged, "T3.2: failed → pending"}})
		if cmd != nil || m.status != "" || m.taskStatusSetting {
			t.Fatalf("cmd %v, status %q, in flight %v; want it dropped without a word", cmd != nil, m.status, m.taskStatusSetting)
		}
	})
	t.Run("one write at a time", func(t *testing.T) {
		m, seam := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T3.2")
		m, _ = taskStatusChoose(t, m, "pending")
		m, cmd := update(t, m, keyRune('y'))
		if cmd == nil || !m.taskStatusSetting || !m.hasInFlightWork() {
			t.Fatalf("y gave cmd %v, in flight %v", cmd != nil, m.taskStatusSetting)
		}
		m, _ = pressAction(t, m, keyRune('S'))
		if m.modal != inputNormal || m.status != taskStatusBusy {
			t.Fatalf("a second S gave modal %v, status %q", m.modal, m.status)
		}
		if len(seam.calls) != 0 {
			t.Fatal("the seam ran inside Update")
		}
	})
	t.Run("a project switch before y calls nothing", func(t *testing.T) {
		m, seam := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T3.2")
		m, _ = taskStatusChoose(t, m, "pending")
		m.projectGen++
		m, cmd := update(t, m, keyRune('y'))
		for _, msg := range collectMsgs(t, cmd) {
			m, _ = update(t, m, msg)
		}
		if len(seam.calls) != 0 || m.status != taskStatusProjectChanged {
			t.Fatalf("calls %d, status %q", len(seam.calls), m.status)
		}
	})
	t.Run("the task is the one the picker opened on", func(t *testing.T) {
		m, seam := taskStatusModel(t)
		m = taskStatusPicker(t, m, "T3.2")
		m, _ = taskStatusChoose(t, m, "pending")
		// A build read lands with the rows reordered under the open confirm.
		snap := buildTabSnap()
		slices.Reverse(snap.Rows)
		m = withBuild(t, m, snap)
		m, cmd := update(t, m, keyRune('y'))
		for _, msg := range collectMsgs(t, cmd) {
			m, _ = update(t, m, msg)
		}
		if len(seam.calls) != 1 || seam.calls[0].id != "T3.2" {
			t.Fatalf("calls %+v, want one for T3.2", seam.calls)
		}
	})
}
