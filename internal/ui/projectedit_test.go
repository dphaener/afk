package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/projectrows"
)

// projectEditSeam is a fake AddProject and RemoveProject: it records every
// call and answers with res.
type projectEditSeam struct {
	adds    [][2]string
	removes []string
	res     ProjectEditResult
}

func (s *projectEditSeam) add(dir, name string) ProjectEditResult {
	s.adds = append(s.adds, [2]string{dir, name})
	return s.res
}

func (s *projectEditSeam) remove(root string) ProjectEditResult {
	s.removes = append(s.removes, root)
	return s.res
}

// projectEditModel is allProjectsModel with the add and remove seams wired to
// a recorder and the home folder pinned to /home/owner, the screen open on
// the fixture's rows.
func projectEditModel(t *testing.T) (Model, *projectEditSeam, *allProjectsSeam) {
	t.Helper()
	m, rows, _ := allProjectsModel(t)
	seam := &projectEditSeam{res: ProjectEditResult{Outcome: ProjectAdded, Text: "Added it."}}
	m.deps.AddProject = seam.add
	m.deps.RemoveProject = seam.remove
	m.deps.HomeDir = func() (string, error) { return "/home/owner", nil }
	return openAllProjects(t, m), seam, rows
}

// openAddForm is ctrl+] + with the folder field emptied, so a test types
// the whole folder itself.
func openAddForm(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = pressAction(t, m, keyRune('+'))
	if m.modal != inputProjectAddFolder {
		t.Fatalf("setup: ctrl+] + left modal %v, want the folder step", m.modal)
	}
	m.projectAddInput.SetValue("")
	return m
}

// TestProjectEditBlockedReason is the add and remove rows' reasons. Load-
// bearing rows: "away from the screen" for both (the rows act on the screen's
// rows, and must not run where those are not shown), "remove on an unlisted
// row is dimmed" (the start-up project has no entry, and its reason names it
// with its own case), "a seam beats the screen" (opening the screen would not
// help), and "other rows over the screen are unchanged", which pins that
// adding these two did not open the screen to anything else.
func TestProjectEditBlockedReason(t *testing.T) {
	both := leaderConds{leader: defaultLeaderKey, canAddProject: true, canRemoveProject: true, allProjectsOpen: true,
		projectRowSelected: true, projectRowListed: true, projectRowName: "alpha"}
	with := func(f func(*leaderConds)) leaderConds { c := both; f(&c); return c }
	tests := []struct {
		name  string
		a     modeAction
		conds leaderConds
		want  string
	}{
		{"add runs over the screen", actionProjectAdd, both, ""},
		{"add runs with no row selected", actionProjectAdd, with(func(c *leaderConds) { c.projectRowSelected = false }), ""},
		{"add runs on an unlisted row", actionProjectAdd, with(func(c *leaderConds) { c.projectRowListed = false }), ""},
		{"add dimmed away from the screen", actionProjectAdd, with(func(c *leaderConds) { c.allProjectsOpen = false }), projectEditOpenScreenFirst(defaultLeaderKey)},
		{"add dimmed without its seam", actionProjectAdd, with(func(c *leaderConds) { c.canAddProject = false }), "adding a project is not available here"},
		{"a seam beats the screen", actionProjectAdd, leaderConds{}, "adding a project is not available here"},
		{"remove runs on a listed row", actionProjectRemove, both, ""},
		{"remove dimmed away from the screen", actionProjectRemove, with(func(c *leaderConds) { c.allProjectsOpen = false }), projectEditOpenScreenFirst(defaultLeaderKey)},
		{"remove dimmed without its seam", actionProjectRemove, with(func(c *leaderConds) { c.canRemoveProject = false }), "removing a project is not available here"},
		{"remove dimmed with no row selected", actionProjectRemove, with(func(c *leaderConds) { c.projectRowSelected = false }), "no project is selected"},
		{"remove on an unlisted row is dimmed", actionProjectRemove,
			with(func(c *leaderConds) { c.projectRowListed = false; c.projectRowName = "repo" }),
			"repo is not in config.toml, so there is nothing to remove."},
		{"other rows over the screen are unchanged", actionNewSession, both, allProjectsCloseFirst},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := leaderBlockedReason(tt.a, tt.conds); got != tt.want {
				t.Errorf("reason = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProjectEditRowsDimmedAwayFromTheScreen opens the dialog on the session
// list with both seams wired: both rows are dimmed with the way to the
// screen, and their keys run nothing.
func TestProjectEditRowsDimmedAwayFromTheScreen(t *testing.T) {
	m, seam, _ := projectEditModel(t)
	m, _ = update(t, m, keyEsc)
	if m.allProjectsOpen {
		t.Fatal("setup: esc left the screen open")
	}
	m, _ = update(t, m, leaderKeyPress)
	for _, a := range []modeAction{actionProjectAdd, actionProjectRemove} {
		r, ok := leaderRowFor(m.leader.rows, a)
		if !ok || r.blocked != projectEditOpenScreenFirst(defaultLeaderKey) {
			t.Errorf("%v row blocked %q, want %q", a, r.blocked, projectEditOpenScreenFirst(defaultLeaderKey))
		}
	}
	for _, k := range []rune{'+', '-'} {
		got, _ := update(t, m, keyRune(k))
		if got.modal != inputNormal || !got.leaderOpen {
			t.Errorf("%c away from the screen: modal %v, dialog open %v; want nothing run", k, got.modal, got.leaderOpen)
		}
	}
	if len(seam.adds)+len(seam.removes) != 0 {
		t.Fatalf("a seam was called: %v %v", seam.adds, seam.removes)
	}
}

// TestProjectEditAddFormDefault is the folder step's starting text. Load-
// bearing rows: "unlisted active project" (the folder the owner most likely
// means) and "listed active project" (a listed default would only come back
// "already in the list"), and "linked worktree" because the list holds main
// checkouts: the worktree's own path would never be recognised as listed.
func TestProjectEditAddFormDefault(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Model)
		want string
	}{
		{"unlisted active project", func(*Model) {}, "/repo"},
		{"listed active project", func(m *Model) {
			m.listed = []ListedProject{{Path: "/repo", Name: "repo"}}
		}, ""},
		{"listed by the screen's row", func(m *Model) {
			for i := range m.allProjectsRows {
				if m.allProjectsRows[i].Root == "/repo" {
					m.allProjectsRows[i].Listed = true
				}
			}
		}, ""},
		{"linked worktree resolves to its main checkout", func(m *Model) {
			m.projects[0].Path = "/repo-wt"
			m.deps.LaunchMainCheckout = "/repo"
		}, "/repo"},
		{"linked worktree of a listed project", func(m *Model) {
			m.projects[0].Path = "/repo-wt"
			m.deps.LaunchMainCheckout = "/repo"
			m.listed = []ListedProject{{Path: "/repo", Name: "repo"}}
		}, ""},
		{"active listed project that is not the start-up row", func(m *Model) {
			m.activeProject = 1
			m.listed = []ListedProject{{Path: "/p/alpha", Name: "alpha"}}
		}, ""},
		{"no project list uses the repo", func(m *Model) {
			m.projects = nil
			m.allProjectsRows = nil
		}, "/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, _ := projectEditModel(t)
			tt.mut(&m)
			m, _ = pressAction(t, m, keyRune('+'))
			if m.modal != inputProjectAddFolder {
				t.Fatalf("modal %v, want the folder step", m.modal)
			}
			if got := m.projectAddInput.Value(); got != tt.want {
				t.Errorf("folder field starts with %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProjectEditAdd drives the add form. Load-bearing rows: "folder and name"
// (the seam is called once, with both, and only when the command runs: never
// from Update), "~/x expands", "relative path refused without a call" (a
// relative path would be read against a folder the owner cannot see),
// "blank folder keeps the form open", and the two esc rows, which must call
// nothing and say so.
func TestProjectEditAdd(t *testing.T) {
	tests := []struct {
		name     string
		folder   string
		name2    string // typed on the name step; "-" means the step is not reached
		home     func() (string, error)
		escAt    inputMode
		wantCall [2]string
		wantMode inputMode // the modal after the last key
		wantNote string
		wantStat string
	}{
		{name: "folder and name", folder: "/p/new", name2: "newname", wantCall: [2]string{"/p/new", "newname"}},
		{name: "blank name uses the folder name", folder: "/p/new", name2: "", wantCall: [2]string{"/p/new", ""}},
		{name: "~/x expands", folder: "~/code/x", name2: "", wantCall: [2]string{"/home/owner/code/x", ""}},
		{name: "bare ~ is the home folder", folder: "~", name2: "", wantCall: [2]string{"/home/owner", ""}},
		{name: "the folder is cleaned", folder: "/p/new/../other/", name2: "", wantCall: [2]string{"/p/other", ""}},
		{name: "relative path refused without a call", folder: "code/x", name2: "-",
			wantMode: inputProjectAddFolder, wantNote: projectAddRelative},
		{name: "relative ~name path refused without a call", folder: "~bob/x", name2: "-",
			wantMode: inputProjectAddFolder, wantNote: projectAddRelative},
		{name: "blank folder keeps the form open", folder: "  ", name2: "-",
			wantMode: inputProjectAddFolder, wantNote: projectAddEmpty},
		{name: "unknown home refused without a call", folder: "~/x", name2: "-",
			home:     func() (string, error) { return "", errors.New("no HOME") },
			wantMode: inputProjectAddFolder, wantNote: projectAddNoHome},
		{name: "esc on the folder step calls nothing", folder: "/p/new", name2: "-", escAt: inputProjectAddFolder,
			wantMode: inputNormal, wantStat: projectAddCancelled},
		{name: "esc on the name step calls nothing", folder: "/p/new", name2: "x", escAt: inputProjectAddName,
			wantMode: inputNormal, wantStat: projectAddCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, seam, _ := projectEditModel(t)
			if tt.home != nil {
				m.deps.HomeDir = tt.home
			}
			m = openAddForm(t, m)
			m = typeText(t, m, tt.folder)
			var cmd tea.Cmd
			if tt.escAt == inputProjectAddFolder {
				m, cmd = update(t, m, keyEsc)
			} else {
				m, cmd = update(t, m, keyEnter)
			}
			if tt.name2 != "-" {
				if m.modal != inputProjectAddName {
					t.Fatalf("after the folder's enter modal %v (note %q), want the name step", m.modal, m.projectAddNote)
				}
				m = typeText(t, m, tt.name2)
				if tt.escAt == inputProjectAddName {
					m, cmd = update(t, m, keyEsc)
				} else {
					m, cmd = update(t, m, keyEnter)
				}
			}
			if len(seam.adds) != 0 {
				t.Fatalf("the seam ran inside Update: %v", seam.adds)
			}
			for _, msg := range collectMsgs(t, cmd) {
				m, _ = update(t, m, msg)
			}
			if tt.wantCall == ([2]string{}) {
				if len(seam.adds) != 0 {
					t.Fatalf("the seam was called: %v", seam.adds)
				}
			} else if len(seam.adds) != 1 || seam.adds[0] != tt.wantCall {
				t.Fatalf("seam calls %v, want exactly [%v]", seam.adds, tt.wantCall)
			}
			if m.modal != tt.wantMode {
				t.Errorf("modal %v, want %v", m.modal, tt.wantMode)
			}
			if m.projectAddNote != tt.wantNote {
				t.Errorf("note %q, want %q", m.projectAddNote, tt.wantNote)
			}
			if tt.wantStat != "" && m.status != tt.wantStat {
				t.Errorf("status %q, want %q", m.status, tt.wantStat)
			}
		})
	}
}

// TestProjectEditRemove drives the remove confirm. Load-bearing rows: "remove
// calls the seam only on y" (with the row's root, and never from Update),
// "n_calls_nothing", "esc calls nothing" and "enter calls nothing" (the
// default is no), and "remove dimmed on the unlisted row", whose key must not
// raise the confirm at all.
func TestProjectEditRemove(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		answer   tea.Msg
		wantCall []string
		wantMode inputMode
		wantStat string
	}{
		{"remove calls the seam only on y", "/p/beta", keyRune('y'), []string{"/p/beta"}, inputNormal, ""},
		{"Y works as y", "/p/alpha", keyRune('Y'), []string{"/p/alpha"}, inputNormal, ""},
		{"n_calls_nothing", "/p/beta", keyRune('n'), nil, inputNormal, projectRemoveCancelled},
		{"esc calls nothing", "/p/beta", keyEsc, nil, inputNormal, projectRemoveCancelled},
		{"enter calls nothing and keeps the question", "/p/beta", keyEnter, nil, inputConfirmProjectRemove, ""},
		{"a stray key calls nothing and keeps the question", "/p/beta", keyRune('x'), nil, inputConfirmProjectRemove, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, seam, _ := projectEditModel(t)
			seam.res = ProjectEditResult{Outcome: ProjectRemoved, Text: "Removed it."}
			m = cursorOn(t, m, tt.root)
			m, _ = pressAction(t, m, keyRune('-'))
			if m.modal != inputConfirmProjectRemove || m.projectRemoveRow.Root != tt.root {
				t.Fatalf("ctrl+] - gave modal %v for %q, want the confirm for %s", m.modal, m.projectRemoveRow.Root, tt.root)
			}
			if len(seam.removes) != 0 {
				t.Fatal("the seam ran before the answer")
			}
			m, cmd := update(t, m, tt.answer)
			if len(seam.removes) != 0 {
				t.Fatal("the seam ran inside Update")
			}
			for _, msg := range collectMsgs(t, cmd) {
				m, _ = update(t, m, msg)
			}
			if len(seam.removes) != len(tt.wantCall) || (len(tt.wantCall) == 1 && seam.removes[0] != tt.wantCall[0]) {
				t.Fatalf("seam calls %v, want %v", seam.removes, tt.wantCall)
			}
			if m.modal != tt.wantMode {
				t.Errorf("modal %v, want %v", m.modal, tt.wantMode)
			}
			if tt.wantStat != "" && m.status != tt.wantStat {
				t.Errorf("status %q, want %q", m.status, tt.wantStat)
			}
		})
	}

	t.Run("remove dimmed on the unlisted row", func(t *testing.T) {
		m, seam, _ := projectEditModel(t)
		m = cursorOn(t, m, "/repo")
		m, _ = update(t, m, leaderKeyPress)
		r, ok := leaderRowFor(m.leader.rows, actionProjectRemove)
		if !ok || r.blocked != projectRemoveNotListed("repo") {
			t.Fatalf("remove row blocked %q, want %q", r.blocked, projectRemoveNotListed("repo"))
		}
		m, _ = update(t, m, keyRune('-'))
		if m.modal == inputConfirmProjectRemove || len(seam.removes) != 0 {
			t.Fatalf("a dimmed remove raised the confirm (%v) or called the seam (%v)", m.modal, seam.removes)
		}
	})
	t.Run("the action refuses the unlisted row on its own", func(t *testing.T) {
		m, seam, _ := projectEditModel(t)
		m = cursorOn(t, m, "/repo")
		m, cmd := runAction(m, actionProjectRemove)
		if cmd != nil || m.modal != inputNormal || len(seam.removes) != 0 {
			t.Fatalf("modal %v, cmd %v, calls %v; want nothing but a notice", m.modal, cmd != nil, seam.removes)
		}
		if m.status != projectRemoveNotListed("repo") {
			t.Errorf("status %q, want %q", m.status, projectRemoveNotListed("repo"))
		}
	})
	t.Run("the dialog names the project and what is left alone", func(t *testing.T) {
		m, _, _ := projectEditModel(t)
		m = cursorOn(t, m, "/p/beta")
		m, _ = pressAction(t, m, keyRune('-'))
		d := m.projectRemoveDialog()
		text := xansi.Strip(strings.Join(d.lines, " "))
		for _, want := range []string{"Remove beta (/p/beta) from the daemon's list?", "are not touched"} {
			if !strings.Contains(text, want) {
				t.Errorf("dialog %q does not say %q", text, want)
			}
		}
		if d.hint != "y: remove · n/esc: cancel" {
			t.Errorf("hint %q", d.hint)
		}
	})
}

// TestProjectEditNotices is each outcome's notice. Every row is load-bearing:
// the kind is how loudly the result is said, and a change, a no-op, a refusal
// and a broken file must not read alike. "empty text" must still say
// something, and "unknown outcome" must not claim success.
func TestProjectEditNotices(t *testing.T) {
	tests := []struct {
		name     string
		res      ProjectEditResult
		wantKind notificationKind
		wantText string
	}{
		{"added", ProjectEditResult{ProjectAdded, "Added x (/p/x)."}, notifySuccess, "Added x (/p/x)."},
		{"removed", ProjectEditResult{ProjectRemoved, "Removed x from the list."}, notifySuccess, "Removed x from the list."},
		{"already", ProjectEditResult{ProjectAlreadyListed, "x is already in the list."}, notifyInfo, "x is already in the list."},
		{"not_listed", ProjectEditResult{ProjectNotListed, "No project named x."}, notifyInfo, "No project named x."},
		{"refused", ProjectEditResult{ProjectRefused, "/tmp is not a git repository."}, notifyWarn, "/tmp is not a git repository."},
		{"failed", ProjectEditResult{ProjectFailed, "Could not write config.toml."}, notifyError, "Could not write config.toml."},
		{"empty text", ProjectEditResult{ProjectAdded, "  "}, notifyWarn, projectEditNoAnswer},
		{"unknown outcome", ProjectEditResult{"odd", "Something."}, notifyInfo, "Something."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, msg := range []tea.Msg{
				projectAddedMsg{dir: "/p/x", result: tt.res},
				projectRemovedMsg{root: "/p/x", result: tt.res},
			} {
				m, _, _ := projectEditModel(t)
				m, _ = update(t, m, msg)
				if m.statusKind != tt.wantKind || m.status != tt.wantText {
					t.Errorf("%T: notice %v %q, want %v %q", msg, m.statusKind, m.status, tt.wantKind, tt.wantText)
				}
			}
		})
	}
}

// TestProjectEditRereads checks that a result reads the rows and the
// switcher's list again, and when the cursor follows an added row. Load-
// bearing rows: "added row takes the cursor" (the owner sees what they made),
// "an add after a project switch moves nothing" and "an add after the screen
// reopened moves nothing" (the generation), and "remove re-reads" (the row
// must go without waiting for the tick).
func TestProjectEditRereads(t *testing.T) {
	newRow := projectrows.Build(projectrows.Input{Name: "new", Root: "/p/new", Listed: true, Order: 8, HasPlan: true, Total: 2})
	tests := []struct {
		name       string
		msg        func(m Model) tea.Msg
		mut        func(m Model) Model
		wantCursor string
	}{
		{"added row takes the cursor", func(m Model) tea.Msg {
			return projectAddedMsg{dir: "/p/new", gen: m.projectGen, openGen: m.allProjectsGen,
				result: ProjectEditResult{ProjectAdded, "Added new."}}
		}, nil, "/p/new"},
		{"an add after a project switch moves nothing", func(m Model) tea.Msg {
			return projectAddedMsg{dir: "/p/new", gen: m.projectGen - 1, openGen: m.allProjectsGen,
				result: ProjectEditResult{ProjectAdded, "Added new."}}
		}, nil, "/p/beta"},
		{"an add after the screen reopened moves nothing", func(m Model) tea.Msg {
			return projectAddedMsg{dir: "/p/new", gen: m.projectGen, openGen: m.allProjectsGen - 1,
				result: ProjectEditResult{ProjectAdded, "Added new."}}
		}, nil, "/p/beta"},
		{"a refused add moves nothing", func(m Model) tea.Msg {
			return projectAddedMsg{dir: "/p/new", gen: m.projectGen, openGen: m.allProjectsGen,
				result: ProjectEditResult{ProjectRefused, "No."}}
		}, nil, "/p/beta"},
		{"remove re-reads", func(m Model) tea.Msg {
			return projectRemovedMsg{root: "/p/beta", result: ProjectEditResult{ProjectRemoved, "Removed beta."}}
		}, nil, "/p/beta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, rows := projectEditModel(t)
			listedReads := 0
			m.deps.LoadListed = func() ([]ListedProject, string, error) {
				listedReads++
				return nil, "/cfg/config.toml", nil
			}
			m = cursorOn(t, m, "/p/beta")
			rows.res.Rows = append(allProjectsFixture(), newRow)
			before := rows.calls
			m, cmd := update(t, m, tt.msg(m))
			for _, msg := range collectMsgs(t, cmd) {
				m, _ = update(t, m, msg)
			}
			if rows.calls != before+1 {
				t.Errorf("rows read %d times after the result, want 1", rows.calls-before)
			}
			if listedReads != 1 {
				t.Errorf("project list read %d times after the result, want 1", listedReads)
			}
			if r, _ := m.selectedProjectRow(); r.Root != tt.wantCursor {
				t.Errorf("cursor on %q, want %q", r.Root, tt.wantCursor)
			}
		})
	}
	t.Run("a result after the screen closed is still told", func(t *testing.T) {
		m, _, rows := projectEditModel(t)
		m, _ = update(t, m, keyEsc)
		before := rows.calls
		m, cmd := update(t, m, projectAddedMsg{dir: "/p/new", gen: m.projectGen, openGen: m.allProjectsGen,
			result: ProjectEditResult{ProjectAdded, "Added new."}})
		for _, msg := range collectMsgs(t, cmd) {
			m, _ = update(t, m, msg)
		}
		if m.status != "Added new." {
			t.Errorf("status %q, want the result", m.status)
		}
		if rows.calls != before || m.allProjectsFollow != "" {
			t.Errorf("closed screen: %d reads, follow %q; want none", rows.calls-before, m.allProjectsFollow)
		}
	})
}
