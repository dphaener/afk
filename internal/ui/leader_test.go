package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// leaderTestRows is a small hand-built row set for the key-decision table, so
// each row of that table names exactly the row it runs. The `\` pair is
// load-bearing: two rows answer the same key (back to list on the right side
// and close pane on the list), which is the one key the real table shares.
// The quit row carries ctrl+c as a second, unshown key. The last row is a
// by-name row (the T7.3 plan actions): no key label and no keys, so it is
// reached only through the filter and enter.
func leaderTestRows() []leaderRow {
	return []leaderRow{
		{action: actionNewSession, mode: inputNormal, name: "new session", keyLabel: "n", keys: []string{"n"}, help: "Start a new session"},
		{action: actionClean, mode: inputNormal, name: "clean session", keyLabel: "c", keys: []string{"c"}, help: "Clean", blocked: "no session selected"},
		{action: actionFocusPane, mode: inputNormal, name: "focus the right side", keyLabel: "f", keys: []string{"f"}, help: "Focus"},
		{action: actionDetailUnfocus, mode: inputDetail, name: "back to list", keyLabel: "\\", keys: []string{"\\"}, help: "Back", blocked: "the session list already has the keyboard"},
		{action: actionPaneClose, mode: inputNormal, name: "close pane", keyLabel: "\\", keys: []string{"\\"}, help: "Close"},
		{action: actionPaneToggle, mode: inputDetail, name: "agent or overview", keyLabel: "t", keys: []string{"t"}, help: "Switch", blocked: "focus the right side first"},
		{action: actionQuit, mode: inputNormal, name: "quit", keyLabel: "q", keys: []string{"q", "ctrl+c"}, help: "Leave"},
		{action: actionPlanExtend, mode: inputNormal, name: "extend the plan", help: "Add work"},
	}
}

func leaderPress(s string) tea.KeyPressMsg {
	k, ok := leaderPressFor(s)
	if !ok {
		panic("test key does not round-trip: " + s)
	}
	return k
}

// leaderRowFor is the row for action a in rows.
func leaderRowFor(rows []leaderRow, a modeAction) (leaderRow, bool) {
	i := slices.IndexFunc(rows, func(r leaderRow) bool { return r.action == a })
	if i < 0 {
		return leaderRow{}, false
	}
	return rows[i], true
}

var leaderTestCtrlSpace = tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl}

// TestLeaderKeyDecision is every branch of leaderKeyDecision, one row each.
//
// Load-bearing rows:
//   - the two ctrl+space rows: from the pane the second press must reach the
//     agent (passThrough); from anywhere else it only closes, so nothing is
//     typed into a session the user was not talking to.
//   - "action key with a query filters": once the user has typed, `n` is a
//     letter of the query, not New, or filtering for "clean" would open the
//     creation form at its first letter.
//   - "dimmed action key is ignored": the dialog stays open and nothing runs,
//     which is the whole meaning of a dimmed row.
//   - "shared key runs the runnable row": `\` belongs to two rows, and the
//     dimmed one listed first must not swallow it from the one that can run.
//   - "enter on a dimmed highlight is ignored": enter must not be a back door
//     around the dimming.
//   - "ctrl+c runs quit": quit's unshown second key, so a stuck dashboard can
//     be left from inside the dialog too.
//   - "old chord is not a row key": a row shows the key after the leader, so
//     the chord it replaced (ctrl+t) is nobody's key in the dialog.
func TestLeaderKeyDecision(t *testing.T) {
	rows := leaderTestRows()
	cases := []struct {
		name      string
		k         tea.KeyPressMsg
		query     string
		rows      []leaderRow
		highlight int
		fromPane  bool
		want      leaderOutcomeKind
		wantRun   modeAction
		wantText  string
		wantDelta int
	}{
		{name: "esc closes", k: leaderPress("esc"), rows: rows, want: leaderClose},
		{name: "esc closes with a query", k: leaderPress("esc"), query: "cl", rows: rows, want: leaderClose},
		{name: "ctrl+space from the pane passes through", k: leaderTestCtrlSpace, rows: rows, fromPane: true, want: leaderPassThrough},
		{name: "ctrl+space not from the pane closes", k: leaderTestCtrlSpace, rows: rows, want: leaderClose},
		{name: "up moves", k: leaderPress("up"), rows: rows, want: leaderMove, wantDelta: -1},
		{name: "down moves", k: leaderPress("down"), rows: rows, want: leaderMove, wantDelta: 1},
		{name: "enter runs the highlighted row", k: leaderPress("enter"), rows: rows, highlight: 0, want: leaderRun, wantRun: actionNewSession},
		{name: "enter runs the focus row when it is highlighted", k: leaderPress("enter"), rows: rows, highlight: 2, want: leaderRun, wantRun: actionFocusPane},
		{name: "enter with no rows is ignored", k: leaderPress("enter"), rows: nil, want: leaderIgnore},
		{name: "enter on a dimmed highlight is ignored", k: leaderPress("enter"), rows: rows, highlight: 1, want: leaderIgnore},
		{name: "backspace edits the query", k: leaderPress("backspace"), query: "cl", rows: rows, want: leaderFilter},
		{name: "action key with empty query runs it", k: leaderPress("n"), rows: rows, want: leaderRun, wantRun: actionNewSession},
		{name: "focus key runs it", k: leaderPress("f"), rows: rows, want: leaderRun, wantRun: actionFocusPane},
		{name: "ctrl+c runs quit", k: leaderPress("ctrl+c"), rows: rows, want: leaderRun, wantRun: actionQuit},
		{name: "action key with a query filters", k: leaderPress("n"), query: "clea", rows: rows, want: leaderFilter, wantText: "n"},
		{name: "letter matching no key filters", k: leaderPress("x"), rows: rows, want: leaderFilter, wantText: "x"},
		{name: "dimmed action key is ignored", k: leaderPress("c"), rows: rows, want: leaderIgnore},
		{name: "dimmed right-side key is ignored", k: leaderPress("t"), rows: rows, want: leaderIgnore},
		{name: "old chord is not a row key", k: leaderPress("ctrl+t"), rows: rows, want: leaderIgnore},
		{name: "shared key runs the runnable row", k: leaderPress("\\"), rows: rows, want: leaderRun, wantRun: actionPaneClose},
		{name: "non-printable unknown key is ignored", k: leaderPress("ctrl+y"), rows: rows, want: leaderIgnore},
		{name: "chord with a query is ignored", k: leaderPress("ctrl+c"), query: "b", rows: rows, want: leaderIgnore},
		// The by-name row: its name's first letter is nobody's key, so it
		// starts the query, and enter on the highlighted row runs it.
		{name: "a by-name row's letter filters", k: leaderPress("e"), rows: rows, want: leaderFilter, wantText: "e"},
		{name: "enter runs a highlighted by-name row", k: leaderPress("enter"), rows: rows, highlight: 7, want: leaderRun, wantRun: actionPlanExtend},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := leaderKeyDecision(tc.k, tc.query, tc.rows, tc.highlight, tc.fromPane)
			if got.kind != tc.want {
				t.Fatalf("kind = %v, want %v", got.kind, tc.want)
			}
			if tc.want == leaderRun && got.row.action != tc.wantRun {
				t.Errorf("runs %v, want %v", got.row.action, tc.wantRun)
			}
			if tc.want == leaderFilter && got.text != tc.wantText {
				t.Errorf("text = %q, want %q", got.text, tc.wantText)
			}
			if tc.want == leaderMove && got.delta != tc.wantDelta {
				t.Errorf("delta = %d, want %d", got.delta, tc.wantDelta)
			}
		})
	}
}

// TestLeaderBlockedReason has one row per guard and one clear row per action
// that has a guard. Load-bearing: the clean rows (a pane open blocks it, the
// same rule cleanRequested enforces, and the reason names ctrl+space \ as the
// way out), the agent/overview rows (from the list the right side is not
// under control, so it must not run there even through the dialog), and the
// close-pane and clear-filter rows, which carry the two guards the old rules
// had (needsPane, needsFilter) now that the rules are gone. The new-plan rows
// are load-bearing too: "new plan runs with no project" is the case the
// action exists for (a dashboard outside any project), and dimming it there
// as the change actions are dimmed would hide it exactly when it is needed.
func TestLeaderBlockedReason(t *testing.T) {
	ready := leaderConds{
		hasActiveProject: true,
		selected:         true,
		paneOpen:         true,
		rightFocused:     true,
		listFiltered:     true,
		hasBuild:         true,
		buildKnown:       true,
		restartLive:      true,
		canFocus:         true,
		canToggleView:    true,
		canStartPlan:     true,
		canCheckPlan:     true,
		canAllProjects:   true,
	}
	with := func(f func(*leaderConds)) leaderConds {
		c := ready
		f(&c)
		return c
	}
	cases := []struct {
		name   string
		action modeAction
		conds  leaderConds
		want   string // "" is runnable; otherwise a substring of the reason
	}{
		{"quit always runs", actionQuit, leaderConds{}, ""},
		{"mouse always runs", actionMouseToggle, leaderConds{}, ""},
		{"help always runs", actionHelp, leaderConds{}, ""},
		{"filter start always runs", actionFilterStart, leaderConds{}, ""},
		{"resize always runs", actionEnterResize, leaderConds{}, ""},

		{"close pane needs a pane", actionPaneClose, with(func(c *leaderConds) { c.paneOpen = false }), "no agent pane"},
		{"close pane with a pane", actionPaneClose, ready, ""},

		{"focus needs a project", actionFocusPane, with(func(c *leaderConds) { c.hasActiveProject = false; c.rightFocused = false }), "no project"},
		{"focus needs something to focus", actionFocusPane, with(func(c *leaderConds) { c.canFocus = false; c.rightFocused = false }), "nothing to focus"},
		{"focus when the right side has it", actionFocusPane, ready, "already has the keyboard"},
		{"focus from the list", actionFocusPane, with(func(c *leaderConds) { c.rightFocused = false }), ""},

		{"new session needs a project", actionNewSession, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project"},
		{"new session with a project", actionNewSession, ready, ""},
		{"one-off needs a project", actionScratch, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project"},
		{"one-off with a project", actionScratch, ready, ""},
		{"extend needs a project", actionPlanExtend, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project is open"},
		{"amend needs a project", actionPlanAmend, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project is open"},
		{"reconcile needs a project", actionPlanReconcile, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project is open"},
		// Nothing else dims them: no build, no selection, no pane. Whether a
		// change session may start is the start's own answer.
		{"extend runs with only a project", actionPlanExtend, leaderConds{hasActiveProject: true}, ""},
		{"amend runs with only a project", actionPlanAmend, leaderConds{hasActiveProject: true}, ""},
		{"reconcile runs with only a project", actionPlanReconcile, leaderConds{hasActiveProject: true}, ""},
		// "start a new plan" types its folder, so it runs with no project
		// at all; only a missing seam dims it.
		{"new plan runs with no project", actionPlanNew, leaderConds{canStartPlan: true}, ""},
		{"new plan needs the seam", actionPlanNew, with(func(c *leaderConds) { c.canStartPlan = false }), "not available"},
		{"new plan with everything", actionPlanNew, ready, ""},
		// "check the plan" (T7.7) is dimmed only without the seam: no project
		// and a status not read yet are said by the action in a sentence.
		{"check the plan runs with only the seam", actionPlanCheck, leaderConds{canCheckPlan: true}, ""},
		{"check the plan needs the seam", actionPlanCheck, with(func(c *leaderConds) { c.canCheckPlan = false }), "not available"},
		{"check the plan with everything", actionPlanCheck, ready, ""},
		{"projects needs a project", actionProjects, with(func(c *leaderConds) { c.hasActiveProject = false }), "no projects"},
		{"projects with a project", actionProjects, ready, ""},

		{"clean needs a selection", actionClean, with(func(c *leaderConds) { c.selected = false; c.paneOpen = false }), "no session selected"},
		{"clean blocked by an open pane", actionClean, ready, "close the pane first (ctrl+space \\)"},
		{"clean blocked while creating", actionClean, with(func(c *leaderConds) { c.paneOpen = false; c.selectedPending = true }), "still being created"},
		{"clean blocked while cleaning", actionClean, with(func(c *leaderConds) { c.paneOpen = false; c.selectedCleaning = true }), "already being cleaned"},
		{"clean runs", actionClean, with(func(c *leaderConds) { c.paneOpen = false }), ""},

		{"restart needs a selection", actionRestart, with(func(c *leaderConds) { c.selected = false }), "no session selected"},
		{"restart needs a stopped or stuck agent", actionRestart, with(func(c *leaderConds) { c.restartLive = false }), "stopped or stuck"},
		{"restart runs", actionRestart, ready, ""},

		{"label needs a selection", actionEditLabel, with(func(c *leaderConds) { c.selected = false }), "no session selected"},
		{"label blocked while creating", actionEditLabel, with(func(c *leaderConds) { c.selectedPending = true }), "still being created"},
		{"label blocked on a broken row", actionEditLabel, with(func(c *leaderConds) { c.selectedFault = true }), "no worktree"},
		{"label runs", actionEditLabel, ready, ""},

		{"clear filter needs a filter", actionFilterClear, with(func(c *leaderConds) { c.listFiltered = false }), "no filter"},
		{"clear filter runs", actionFilterClear, ready, ""},

		{"build needs a build", actionBuild, with(func(c *leaderConds) { c.hasBuild = false }), "no build"},
		{"build needs a project", actionBuild, with(func(c *leaderConds) { c.hasActiveProject = false }), "no build"},
		{"build runs", actionBuild, ready, ""},
		{"pause needs a build", actionPauseBuild, with(func(c *leaderConds) { c.hasBuild = false }), "no build"},
		{"pause runs", actionPauseBuild, ready, ""},
		{"resume needs a build", actionResumeBuild, with(func(c *leaderConds) { c.hasBuild = false }), "no build"},
		{"resume runs", actionResumeBuild, ready, ""},

		{"inbox needs the build read", actionInbox, with(func(c *leaderConds) { c.buildKnown = false }), "not been read"},
		{"inbox runs", actionInbox, ready, ""},

		{"back to list needs the right side focused", actionDetailUnfocus, with(func(c *leaderConds) { c.rightFocused = false }), "already has the keyboard"},
		{"back to list runs", actionDetailUnfocus, ready, ""},

		{"agent/overview needs the right side focused", actionPaneToggle, with(func(c *leaderConds) { c.rightFocused = false }), "focus the right side first"},
		{"agent/overview needs a live agent", actionPaneToggle, with(func(c *leaderConds) { c.canToggleView = false }), "no live agent"},
		{"agent/overview runs", actionPaneToggle, ready, ""},

		// "all projects" (T7.9) is dimmed only without its reader: it lists
		// config.toml's projects, so a dashboard with no project open can
		// still show them.
		{"all projects runs with only the seam", actionAllProjects, leaderConds{canAllProjects: true}, ""},
		{"all projects needs the seam", actionAllProjects, with(func(c *leaderConds) { c.canAllProjects = false }), "without the project reader"},

		// Over the all-projects screen five rows run and the rest are
		// dimmed with one reason. Pause and resume run even for an active
		// project with no build, because over the screen they act on the
		// selected row, not on the active project; that row is the
		// load-bearing one.
		{"over the screen pause runs without an active build", actionPauseBuild, leaderConds{allProjectsOpen: true}, ""},
		{"over the screen resume runs without an active build", actionResumeBuild, leaderConds{allProjectsOpen: true}, ""},
		{"over the screen all projects runs (closes it)", actionAllProjects, leaderConds{allProjectsOpen: true, canAllProjects: true}, ""},
		{"over the screen mouse runs", actionMouseToggle, leaderConds{allProjectsOpen: true}, ""},
		{"over the screen quit runs", actionQuit, leaderConds{allProjectsOpen: true}, ""},
		{"over the screen new session is dimmed", actionNewSession, with(func(c *leaderConds) { c.allProjectsOpen = true }), allProjectsCloseFirst},
		{"over the screen switch project is dimmed", actionProjects, with(func(c *leaderConds) { c.allProjectsOpen = true }), allProjectsCloseFirst},
		{"over the screen build tasks is dimmed", actionBuild, with(func(c *leaderConds) { c.allProjectsOpen = true }), allProjectsCloseFirst},
		{"over the screen help is dimmed", actionHelp, with(func(c *leaderConds) { c.allProjectsOpen = true }), allProjectsCloseFirst},
		{"over the screen check the plan is dimmed", actionPlanCheck, with(func(c *leaderConds) { c.allProjectsOpen = true }), allProjectsCloseFirst},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := leaderBlockedReason(tc.action, tc.conds)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("blocked %q, want runnable", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestLeaderTableIsTheDecidedTable holds leaderTable to the key table in
// docs/decisions.md "## T7.2", restated here row for row, so a key moved in
// one place and not the other fails. Every row is load-bearing: the table is
// the owner's decision, and the dialog, the notices and (next) every hint are
// read off it.
func TestLeaderTableIsTheDecidedTable(t *testing.T) {
	want := []struct {
		key    string
		action modeAction
	}{
		{"n", actionNewSession},
		{"o", actionScratch},
		{"p", actionProjects},
		// T7.9: upper-case A, so typing "approve" still filters.
		{"A", actionAllProjects},
		// T7.10: keyed, and run only over the all-projects screen.
		{"+", actionProjectAdd},
		{"-", actionProjectRemove},
		{"c", actionClean},
		{"r", actionRestart},
		{"l", actionEditLabel},
		{"/", actionFilterStart},
		{"x", actionFilterClear},
		{"b", actionBuild},
		{"i", actionInbox},
		{"P", actionPauseBuild},
		{"U", actionResumeBuild},
		{"f", actionFocusPane},
		{"t", actionPaneToggle},
		{"\\", actionDetailUnfocus},
		{"\\", actionPaneClose},
		{"g", actionMouseToggle},
		{"w", actionEnterResize},
		{"?", actionHelp},
		{"q", actionQuit},
		// T7.3: by name only, no key after the leader.
		{"", actionPlanExtend},
		{"", actionPlanAmend},
		{"", actionPlanReconcile},
		// T7.4: by name only, no key after the leader.
		{"", actionPlanNew},
		// T7.7: by name only, no key after the leader.
		{"", actionPlanCheck},
		// T7.8: by name only, no key after the leader.
		{"", actionPlanApprove},
	}
	if len(leaderTable) != len(want) {
		t.Fatalf("leaderTable has %d rows, the decided table %d", len(leaderTable), len(want))
	}
	for i, w := range want {
		t.Run(w.action.String(), func(t *testing.T) {
			got := leaderTable[i]
			if got.key != w.key || got.action != w.action {
				t.Errorf("row %d = %q %v, want %q %v", i, got.key, got.action, w.key, w.action)
			}
		})
	}
}

// TestLeaderRowsFromTheTable builds the rows the dialog opens with. Every
// check is load-bearing:
//   - "every row has a name and a help line" is the guard for actions added
//     later: a new table row fails here until both are written.
//   - "no duplicates" and "no movement rows": the table lists each action
//     once and never a key you hold down, nor the leader itself.
//   - "rows show the key after the leader": no row shows esc, enter or a
//     chord any more (T7.1's open question, settled by T7.2).
//   - "every key round-trips": a row's key is turned into a press for the
//     handlers that read it (the filter's `/`), so it must parse.
//   - "right-side rows run as the overview": the mode is what applyKeyAction
//     is called with.
func TestLeaderRowsFromTheTable(t *testing.T) {
	rows := leaderRows(leaderConds{})

	t.Run("one row per table entry", func(t *testing.T) {
		if len(rows) != len(leaderTable) {
			t.Fatalf("%d rows for %d table entries", len(rows), len(leaderTable))
		}
	})

	t.Run("every row has a name and a help line", func(t *testing.T) {
		for _, r := range rows {
			if strings.TrimSpace(r.name) == "" {
				t.Errorf("action %v has no name in leaderNames", r.action)
			}
			if strings.TrimSpace(r.help) == "" {
				t.Errorf("action %v (%q) has no help line in leaderHelp", r.action, r.name)
			}
		}
	})

	t.Run("no duplicates", func(t *testing.T) {
		seen := map[modeAction]bool{}
		for _, r := range rows {
			if seen[r.action] {
				t.Errorf("action %v listed twice", r.action)
			}
			seen[r.action] = true
		}
	})

	t.Run("no movement rows", func(t *testing.T) {
		movement := []modeAction{
			actionCursorUp, actionCursorDown, actionPageUp, actionPageDown,
			actionListTop, actionListBottom,
			actionDetailScrollUp, actionDetailScrollDown,
			actionDetailHalfPageUp, actionDetailHalfPageDown,
			actionDetailPageUp, actionDetailPageDown,
			actionDetailGotoTop, actionDetailGotoBottom,
			actionDetailTabNext, actionDetailTabPrev, actionDetailTabJump,
		}
		for _, r := range rows {
			if slices.Contains(movement, r.action) {
				t.Errorf("movement action %v is listed", r.action)
			}
			if r.action == actionOpenLeader {
				t.Errorf("the leader lists itself")
			}
		}
	})

	t.Run("rows show the key after the leader", func(t *testing.T) {
		for _, r := range rows {
			if r.keyLabel == "" {
				// A by-name row answers to no key at all.
				if len(r.keys) != 0 {
					t.Errorf("by-name row %v answers to keys %q", r.action, r.keys)
				}
				continue
			}
			// A "+" inside a label is a chord (ctrl+x); "+" alone is the add
			// row's own key (T7.10), a single key like any other.
			chord := strings.Contains(r.keyLabel, "+") && r.keyLabel != "+"
			if r.keyLabel == "esc" || r.keyLabel == "enter" || chord || strings.Contains(r.keyLabel, "/f") {
				t.Errorf("row %v shows %q, want a single key after the leader", r.action, r.keyLabel)
			}
			if len(r.keys) == 0 || r.keys[0] != r.keyLabel {
				t.Errorf("row %v keys %q do not start with its label %q", r.action, r.keys, r.keyLabel)
			}
		}
	})

	t.Run("every key round-trips", func(t *testing.T) {
		for _, r := range rows {
			for _, k := range r.keys {
				if _, ok := leaderPressFor(k); !ok {
					t.Errorf("row %v key %q does not round-trip", r.action, k)
				}
			}
		}
	})

	t.Run("right-side rows run as the overview", func(t *testing.T) {
		for _, r := range rows {
			want := inputNormal
			if r.action == actionPaneToggle || r.action == actionDetailUnfocus {
				want = inputDetail
			}
			if r.mode != want {
				t.Errorf("row %v mode = %v, want %v", r.action, r.mode, want)
			}
		}
	})

	t.Run("back to list comes before close pane", func(t *testing.T) {
		back := slices.IndexFunc(rows, func(r leaderRow) bool { return r.action == actionDetailUnfocus })
		closeAt := slices.IndexFunc(rows, func(r leaderRow) bool { return r.action == actionPaneClose })
		if back < 0 || closeAt < 0 || back > closeAt {
			t.Errorf("back to list at %d, close pane at %d; `\\` would close the pane from the right side", back, closeAt)
		}
	})

	t.Run("blocked reasons are filled from the conditions", func(t *testing.T) {
		for _, r := range rows {
			if r.blocked != leaderBlockedReason(r.action, leaderConds{}) {
				t.Errorf("row %v blocked = %q, want leaderBlockedReason's answer", r.action, r.blocked)
			}
		}
	})
}

// TestLeaderMovesToList is every reach against every place the leader opens
// from. Load-bearing rows: a list action from the right side moves the
// keyboard (its old key could only be pressed on the list), an overview
// action from the overview does not (its old key ran there in place), and the
// same overview action from the agent pane does (its old letter was the
// agent's, and an overlay it opens must not close back onto the agent).
func TestLeaderMovesToList(t *testing.T) {
	cases := []struct {
		name  string
		reach leaderReach
		from  inputMode
		want  bool
	}{
		{"list action from the list", reachList, inputNormal, false},
		{"list action from the overview", reachList, inputDetail, true},
		{"list action from the agent pane", reachList, inputPane, true},
		{"overview action from the list", reachOverview, inputNormal, false},
		{"overview action from the overview", reachOverview, inputDetail, false},
		{"overview action from the agent pane", reachOverview, inputPane, true},
		{"anywhere action from the list", reachAnywhere, inputNormal, false},
		{"anywhere action from the overview", reachAnywhere, inputDetail, false},
		{"anywhere action from the agent pane", reachAnywhere, inputPane, false},
		{"right-side action from the overview", reachRight, inputDetail, false},
		{"right-side action from the agent pane", reachRight, inputPane, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := leaderMovesToList(tc.reach, tc.from); got != tc.want {
				t.Fatalf("leaderMovesToList = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRemovedKeyNotice is the lookup behind the notice a removed list key
// raises: every removed key is a row and must name its key after the leader,
// and `:` names the dialog's filter. The no-notice rows are load-bearing:
// k and j still move the cursor (a notice would fire on every move if they
// were listed), x was never a list key, and ctrl+t is a chord, which gets no
// notice because in the agent pane it now belongs to the agent.
func TestRemovedKeyNotice(t *testing.T) {
	cases := []struct {
		key  string
		want string // "" is no notice
	}{
		{"n", "n moved: press ctrl+space then n"},
		{"o", "o moved: press ctrl+space then o"},
		{"p", "p moved: press ctrl+space then p"},
		{"c", "c moved: press ctrl+space then c"},
		{"r", "r moved: press ctrl+space then r"},
		{"l", "l moved: press ctrl+space then l"},
		{"/", "/ moved: press ctrl+space then /"},
		{"b", "b moved: press ctrl+space then b"},
		{"i", "i moved: press ctrl+space then i"},
		{"q", "q moved: press ctrl+space then q"},
		{"f", "f moved: press ctrl+space then f"},
		{"?", "? moved: press ctrl+space then ?"},
		{"P", "P moved: press ctrl+space then P"},
		{"U", "U moved: press ctrl+space then U"},
		{":", ": moved: press ctrl+space, then type to filter the actions"},
		{"k", ""},
		{"j", ""},
		{"x", ""},
		{"ctrl+t", ""},
		{"ctrl+\\", ""},
		{"ctrl+g", ""},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got, ok := removedKeyNotice(tc.key)
			if ok != (tc.want != "") {
				t.Fatalf("removedKeyNotice(%q) ok = %v, want %v", tc.key, ok, tc.want != "")
			}
			if got != tc.want {
				t.Fatalf("notice = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("every removed key is a row above", func(t *testing.T) {
		for _, k := range removedListKeys {
			if !slices.ContainsFunc(cases, func(c struct {
				key  string
				want string
			}) bool {
				return c.key == k && c.want != ""
			}) {
				t.Errorf("removed key %q has no notice row", k)
			}
		}
	})
}

// TestLeaderPressFor round-trips key strings into presses. The rejecting rows
// are the load-bearing ones: a string the constructor cannot reproduce must
// report false rather than return some other key, because the consequence of
// a wrong answer is a handler acting on a key the user did not press.
func TestLeaderPressFor(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"printable rune", "q", true},
		{"punctuation", "/", true},
		{"backslash", "\\", true},
		{"question mark", "?", true},
		{"uppercase rune", "P", true},
		{"ctrl chord", "ctrl+c", true},
		{"ctrl chord on punctuation", "ctrl+\\", true},
		{"named key", "enter", true},
		{"escape", "esc", true},
		{"paging key", "pgdown", true},
		{"shift chord on a named key", "shift+tab", true},
		{"empty", "", false},
		// These two parse cleanly and are rejected only by the round-trip: a
		// bare space is printed as "space", and Keystroke always emits
		// modifiers in canonical ctrl/alt/shift order.
		{"a bare space", " ", false},
		{"modifiers out of canonical order", "alt+ctrl+a", false},
		{"unknown name", "f13", false},
		{"unhandled modifier", "hyper+a", false},
		{"dangling modifier", "ctrl+", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := leaderPressFor(tc.in)
			if ok != tc.ok {
				t.Fatalf("leaderPressFor(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if ok && got.String() != tc.in {
				t.Fatalf("leaderPressFor(%q).String() = %q", tc.in, got.String())
			}
		})
	}
}

// TestLeaderDialogFilterAndMove covers the component's two state changes.
// Load-bearing: the highlight is clamped when a filter shrinks the list, or
// enter would run a row that is no longer on screen.
func TestLeaderDialogFilterAndMove(t *testing.T) {
	rows := leaderRows(leaderConds{})
	cases := []struct {
		name      string
		steps     []leaderOutcome
		wantQuery string
		wantTop   modeAction // the highlighted visible row
		wantCount int        // -1: do not check
	}{
		{"down moves the highlight", []leaderOutcome{{kind: leaderMove, delta: 1}}, "", rows[1].action, len(rows)},
		{"up from the top wraps to the bottom", []leaderOutcome{{kind: leaderMove, delta: -1}}, "", rows[len(rows)-1].action, len(rows)},
		{"typing filters by name", []leaderOutcome{{kind: leaderFilter, text: "c"}, {kind: leaderFilter, text: "l"}, {kind: leaderFilter, text: "e"}, {kind: leaderFilter, text: "a"}, {kind: leaderFilter, text: "n"}}, "clean", actionClean, -1},
		{"filter is case-insensitive", []leaderOutcome{{kind: leaderFilter, text: "CLEAN"}}, "CLEAN", actionClean, -1},
		{"filter matches the help line", []leaderOutcome{{kind: leaderFilter, text: "worktree"}}, "worktree", actionClean, -1},
		{"backspace edits the query", []leaderOutcome{{kind: leaderFilter, text: "clx"}, {kind: leaderFilter, backspace: true}}, "cl", actionClean, -1},
		{"a query matching nothing empties the list", []leaderOutcome{{kind: leaderFilter, text: "zzzz"}}, "zzzz", actionNone, 0},
		{"the highlight is clamped by a filter", []leaderOutcome{{kind: leaderMove, delta: -1}, {kind: leaderFilter, text: "clean"}}, "clean", actionClean, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newLeaderDialog(rows, false)
			for _, o := range tc.steps {
				d.apply(o)
			}
			if d.query != tc.wantQuery {
				t.Errorf("query = %q, want %q", d.query, tc.wantQuery)
			}
			vis := d.visible()
			if tc.wantCount >= 0 && len(vis) != tc.wantCount {
				t.Errorf("visible = %d rows, want %d", len(vis), tc.wantCount)
			}
			if tc.wantTop == actionNone {
				if len(vis) != 0 {
					t.Errorf("want no visible rows, have %d", len(vis))
				}
				return
			}
			if got := vis[d.highlight].action; got != tc.wantTop {
				t.Errorf("highlighted %v, want %v", got, tc.wantTop)
			}
		})
	}
}

// TestLeaderDialogView renders at the two sizes the plan names. Load-bearing:
// no line is wider than the terminal less the modal margins (nothing wraps
// past the box), the box fits the terminal's height, and a dimmed row shows
// its reason.
func TestLeaderDialogView(t *testing.T) {
	rows := leaderRows(leaderConds{hasActiveProject: true})
	for _, sz := range []struct {
		name string
		w, h int
	}{{"80x24", 80, 24}, {"120x40", 120, 40}} {
		t.Run(sz.name, func(t *testing.T) {
			d := newLeaderDialog(rows, false)
			out := d.view(sz.w, sz.h)
			lines := strings.Split(out, "\n")
			if len(lines) > sz.h-2*modalMargin {
				t.Errorf("box is %d lines, terminal leaves %d", len(lines), sz.h-2*modalMargin)
			}
			for i, ln := range lines {
				if w := lipgloss.Width(ln); w > sz.w-2*modalMargin {
					t.Errorf("line %d is %d wide, max %d: %q", i, w, sz.w-2*modalMargin, ln)
				}
			}
			if !strings.Contains(out, "no session selected") {
				t.Errorf("a dimmed row's reason is not shown:\n%s", out)
			}
			if !strings.Contains(out, "new session") {
				t.Errorf("the first row is not shown:\n%s", out)
			}
		})
	}
	t.Run("empty filter says so", func(t *testing.T) {
		d := newLeaderDialog(rows, false)
		d.apply(leaderOutcome{kind: leaderFilter, text: "zzzz"})
		if out := d.view(80, 24); !strings.Contains(out, "no action matches") {
			t.Errorf("empty list does not say so:\n%s", out)
		}
	})
	t.Run("highlight scrolls into view", func(t *testing.T) {
		d := newLeaderDialog(rows, false)
		d.apply(leaderOutcome{kind: leaderMove, delta: -1}) // wraps to the last row
		last := rows[len(rows)-1]
		if out := d.view(80, 24); !strings.Contains(out, last.name) {
			t.Errorf("the highlighted last row %q is not on screen:\n%s", last.name, out)
		}
	})
}
