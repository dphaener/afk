package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// leaderTestRows is a small hand-built row set for the key-decision table, so
// each row of that table names exactly the row it runs. The clean row is
// dimmed, so enter on it is the "a dimmed row does not run" row. The last row
// is a by-name row (the T7.3 plan actions): no key label, reached only
// through the query and enter.
func leaderTestRows() []leaderRow {
	return []leaderRow{
		{action: actionNewSession, name: "new session", keyLabel: "n", help: "Start a new session"},
		{action: actionClean, name: "clean session", keyLabel: "c", help: "Clean", blocked: "no session selected"},
		{action: actionQuit, name: "quit", keyLabel: "q", help: "Leave"},
		{action: actionPlanExtend, name: "extend the plan", help: "Add work"},
	}
}

// testNamedKeys maps the bubbles/key name of every non-printable key a test
// presses to its tea key code, for leaderPress.
var testNamedKeys = map[string]rune{
	"enter":     tea.KeyEnter,
	"esc":       tea.KeyEsc,
	"tab":       tea.KeyTab,
	"space":     tea.KeySpace,
	"backspace": tea.KeyBackspace,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
}

// leaderPress builds the tea.KeyPressMsg that a key.Binding key string names,
// as the terminal's decoding delivers it: Text set for an unmodified
// printable rune (ultraviolet's Key.String returns Text when it is set), the
// modifier-qualified form otherwise. It panics when the built press does not
// print back as the string it was built from, so a test can never press some
// other key than the one it names.
func leaderPress(s string) tea.KeyPressMsg {
	rest := s
	var mod tea.KeyMod
	for parsing := true; parsing; {
		switch {
		case strings.HasPrefix(rest, "ctrl+"):
			mod |= tea.ModCtrl
			rest = strings.TrimPrefix(rest, "ctrl+")
		case strings.HasPrefix(rest, "alt+"):
			mod |= tea.ModAlt
			rest = strings.TrimPrefix(rest, "alt+")
		case strings.HasPrefix(rest, "shift+"):
			mod |= tea.ModShift
			rest = strings.TrimPrefix(rest, "shift+")
		default:
			parsing = false
		}
	}
	var k tea.KeyPressMsg
	if code, ok := testNamedKeys[rest]; ok {
		k = tea.KeyPressMsg{Code: code, Mod: mod}
	} else {
		runes := []rune(rest)
		if len(runes) != 1 {
			panic("test key does not round-trip: " + s)
		}
		k = tea.KeyPressMsg{Code: runes[0], Mod: mod}
		if mod == 0 {
			k.Text = rest
		}
	}
	if k.String() != s {
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

// leaderTestCtrlBracket is the leader, ctrl+], as ultraviolet decodes 0x1D.
// leaderTestCtrlSpace is the leader before T9.1, an ordinary key since.
var (
	leaderTestCtrlBracket = tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}
	leaderTestCtrlSpace   = tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl}
)

// TestLeaderKeyDecision is every branch of the `:` search's key decision, one
// row each.
//
// Load-bearing rows:
//   - "a row's key types with an empty query", "q types" and "a dimmed row's
//     key types": letters ALWAYS type into the query (§4.7). Until T16.2 a
//     row's key ran it from an empty query, so typing "new" ran new session
//     at its n and q quit the dashboard; this is the rule that ended it.
//   - "the leader closes": it is how the search was reached from the Agent,
//     and it must never type a byte into the query or the agent.
//   - "enter on a dimmed highlight is ignored": enter must not be a back door
//     around the dimming.
//   - the two configured-leader rows: with config.toml's leader in use (T9.2)
//     the configured key closes, and ctrl+] is then an unbound chord, ignored.
func TestLeaderKeyDecision(t *testing.T) {
	rows := leaderTestRows()
	cases := []struct {
		name      string
		k         tea.KeyPressMsg
		leader    string // "" means defaultLeaderKey
		rows      []leaderRow
		highlight int
		want      leaderOutcomeKind
		wantRun   modeAction
		wantText  string
		wantDelta int
	}{
		{name: "esc closes", k: leaderPress("esc"), rows: rows, want: leaderClose},
		{name: "the leader closes", k: leaderTestCtrlBracket, rows: rows, want: leaderClose},
		{name: "ctrl+space is ignored", k: leaderTestCtrlSpace, rows: rows, want: leaderIgnore},
		{name: "up moves", k: leaderPress("up"), rows: rows, want: leaderMove, wantDelta: -1},
		{name: "down moves", k: leaderPress("down"), rows: rows, want: leaderMove, wantDelta: 1},
		{name: "enter runs the highlighted row", k: leaderPress("enter"), rows: rows, highlight: 0, want: leaderRun, wantRun: actionNewSession},
		{name: "enter with no rows is ignored", k: leaderPress("enter"), rows: nil, want: leaderIgnore},
		{name: "enter on a dimmed highlight is ignored", k: leaderPress("enter"), rows: rows, highlight: 1, want: leaderIgnore},
		{name: "enter runs a highlighted by-name row", k: leaderPress("enter"), rows: rows, highlight: 3, want: leaderRun, wantRun: actionPlanExtend},
		{name: "backspace edits the query", k: leaderPress("backspace"), rows: rows, want: leaderFilter},
		{name: "a row's key types with an empty query", k: leaderPress("n"), rows: rows, want: leaderFilter, wantText: "n"},
		{name: "q types", k: leaderPress("q"), rows: rows, want: leaderFilter, wantText: "q"},
		{name: "a dimmed row's key types", k: leaderPress("c"), rows: rows, want: leaderFilter, wantText: "c"},
		{name: "a letter matching no key types", k: leaderPress("x"), rows: rows, want: leaderFilter, wantText: "x"},
		{name: "punctuation types", k: leaderPress("/"), rows: rows, want: leaderFilter, wantText: "/"},
		{name: "a chord is ignored", k: leaderPress("ctrl+y"), rows: rows, want: leaderIgnore},
		{name: "configured ctrl+o closes", k: leaderPress("ctrl+o"), leader: "ctrl+o", rows: rows, want: leaderClose},
		{name: "configured ctrl+o leaves ctrl+] unbound", k: leaderTestCtrlBracket, leader: "ctrl+o", rows: rows, want: leaderIgnore},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leader := tc.leader
			if leader == "" {
				leader = defaultLeaderKey
			}
			got := leaderKeyDecision(tc.k, leader, tc.rows, tc.highlight)
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
// that has a guard. Load-bearing: "clean runs with a pane open" (since T16.2
// no key closes a pane on its own, so the pane is detached when the clean is
// confirmed instead, and a reason here would make clean unreachable while a
// pane is open), and the new-plan rows: "new plan runs with no project" is the
// case the action exists for (a dashboard outside any project), and dimming
// it there as the change actions are dimmed would hide it exactly when it is
// needed.
func TestLeaderBlockedReason(t *testing.T) {
	ready := leaderConds{
		leader:           defaultLeaderKey,
		hasActiveProject: true,
		selected:         true,
		paneOpen:         true,
		hasBuild:         true,
		buildKnown:       true,
		restartLive:      true,
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
		{"keys always runs", actionKeys, leaderConds{}, ""},
		{"go back always runs", actionGoBack, leaderConds{}, ""},

		{"new session needs a project", actionNewSession, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project"},
		{"new session with a project", actionNewSession, ready, ""},
		{"one-off needs a project", actionScratch, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project"},
		{"one-off with a project", actionScratch, ready, ""},
		{"extend needs a project", actionPlanExtend, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project is open"},
		{"amend needs a project", actionPlanAmend, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project is open"},
		{"reconcile needs a project", actionPlanReconcile, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project is open"},
		{"fix needs a project", actionPlanFix, with(func(c *leaderConds) { c.hasActiveProject = false }), "no project is open"},
		// Nothing else dims them: no build, no selection, no pane. Whether a
		// change session may start is the start's own answer.
		{"extend runs with only a project", actionPlanExtend, leaderConds{hasActiveProject: true}, ""},
		{"amend runs with only a project", actionPlanAmend, leaderConds{hasActiveProject: true}, ""},
		{"reconcile runs with only a project", actionPlanReconcile, leaderConds{hasActiveProject: true}, ""},
		{"fix runs with only a project", actionPlanFix, leaderConds{hasActiveProject: true}, ""},
		// "start a new plan" types its folder, so it runs with no project
		// at all; only a missing seam dims it.
		{"new plan runs with no project", actionPlanNew, leaderConds{canStartPlan: true}, ""},
		{"new plan needs the seam", actionPlanNew, with(func(c *leaderConds) { c.canStartPlan = false }), "not available"},
		{"new plan with everything", actionPlanNew, ready, ""},
		// "check the plan" (T7.7) is dimmed only without the seam: no project
		// and a status not read yet are said by the action in a sentence.
		{"check the plan runs with only the seam", actionPlanCheck, leaderConds{canCheckPlan: true}, ""},
		{"check the plan needs the seam", actionPlanCheck, with(func(c *leaderConds) { c.canCheckPlan = false }), "not available"},
		{"projects needs a project", actionProjects, with(func(c *leaderConds) { c.hasActiveProject = false }), "no projects"},
		{"projects with a project", actionProjects, ready, ""},

		{"clean needs a selection", actionClean, with(func(c *leaderConds) { c.selected = false }), "no session selected"},
		{"clean runs with a pane open (load-bearing)", actionClean, ready, ""},
		{"clean blocked while creating", actionClean, with(func(c *leaderConds) { c.selectedPending = true }), "still being created"},
		{"clean blocked while cleaning", actionClean, with(func(c *leaderConds) { c.selectedCleaning = true }), "already being cleaned"},

		{"restart needs a selection", actionRestart, with(func(c *leaderConds) { c.selected = false }), "no session selected"},
		{"restart needs a stopped or stuck agent", actionRestart, with(func(c *leaderConds) { c.restartLive = false }), "stopped or stuck"},
		{"restart runs", actionRestart, ready, ""},

		{"label needs a selection", actionEditLabel, with(func(c *leaderConds) { c.selected = false }), "no session selected"},
		{"label blocked while creating", actionEditLabel, with(func(c *leaderConds) { c.selectedPending = true }), "still being created"},
		{"label blocked on a broken row", actionEditLabel, with(func(c *leaderConds) { c.selectedFault = true }), "no worktree"},
		{"label runs", actionEditLabel, ready, ""},

		{"pause needs a build", actionPauseToggle, with(func(c *leaderConds) { c.hasBuild = false }), "no build"},
		{"pause needs a project", actionPauseToggle, with(func(c *leaderConds) { c.hasActiveProject = false }), "no build"},
		{"pause runs", actionPauseToggle, ready, ""},

		// "read usage" (T7.13) is dimmed only without its seam: the
		// reading is the machine's, so no project or build is needed.
		{"read usage needs the seam", actionRefreshUsage, ready, "cannot read usage"},
		{"read usage runs with only the seam", actionRefreshUsage, leaderConds{canRefreshUsage: true}, ""},

		{"inbox needs the build read", actionInbox, with(func(c *leaderConds) { c.buildKnown = false }), "not been read"},
		{"inbox runs", actionInbox, ready, ""},

		// Add and remove a project ask projectEditBlockedReason: away from
		// All projects they say how to get there.
		{"add away from all projects", actionProjectAdd, leaderConds{canAddProject: true}, "Go back to All projects first (esc)"},
		{"add at all projects", actionProjectAdd, leaderConds{canAddProject: true, allProjectsOpen: true}, ""},
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

// TestKeyTableIsTheDecidedTable holds keyTable to docs/dash-design.md §5,
// restated here row for row with the levels each key works at, so a key moved
// in one place and not the other fails. It replaces T7.2's
// TestLeaderTableIsTheDecidedTable. Every row is load-bearing: the table is
// the design, and the keys, the `:` search, the key strip, the footers and
// every hint are read off it.
func TestKeyTableIsTheDecidedTable(t *testing.T) {
	want := []struct {
		key    string
		action modeAction
		levels levelSet
	}{
		{"j", actionMove, atAllProjects | atProject},
		{"enter", actionGoIn, atAllProjects | atProject},
		{"esc", actionGoBack, atEveryLevel},
		// `/` opens today's project switcher until T16.6 (A3).
		{"/", actionProjects, atEveryLevel},
		{":", actionActionsSearch, atEveryLevel},
		{"i", actionInbox, atEveryLevel},
		// d needs a selected session, which All projects has not (A2).
		{"d", actionDetails, atProject | atAgent},
		{"R", actionRefreshUsage, atEveryLevel},
		{"?", actionKeys, atEveryLevel},
		{"q", actionQuit, atEveryLevel},
		{"n", actionPlanNew, atAllProjects},
		{"+", actionProjectAdd, atAllProjects},
		{"-", actionProjectRemove, atAllProjects},
		{"a", actionAnswerOldest, atAllProjects},
		{"p", actionPauseToggle, atAllProjects | atProject},
		{"n", actionNewSession, atProject},
		{"o", actionScratch, atProject},
		{"c", actionClean, atProject},
		{"r", actionRestart, atProject},
		{"l", actionEditLabel, atProject},
		// The project view's sections (T16.4).
		{"tab", actionSectionNext, atProject},
		{"shift+tab", actionSectionPrev, atProject},
		{"1", actionSectionSessions, atProject},
		{"2", actionSectionBuild, atProject},
		{"3", actionSectionInbox, atProject},
		{"s", actionTaskStatus, atProject},
		{"t", actionTaskTokens, atProject},
		{"m", actionMouseToggle, atAgent},
		// The plan actions: by name only, from inside a project.
		{"", actionPlanExtend, atProject | atAgent},
		{"", actionPlanAmend, atProject | atAgent},
		{"", actionPlanFix, atProject | atAgent},
		{"", actionPlanReconcile, atProject | atAgent},
		{"", actionPlanCheck, atProject | atAgent},
		{"", actionPlanApprove, atProject | atAgent},
		{"", actionPlanNew, atProject | atAgent},
	}
	if len(keyTable) != len(want) {
		t.Fatalf("keyTable has %d rows, the decided table %d", len(keyTable), len(want))
	}
	for i, w := range want {
		t.Run(w.action.String()+" "+w.key, func(t *testing.T) {
			got := keyTable[i]
			if got.key != w.key || got.action != w.action || got.levels != w.levels {
				t.Errorf("row %d = %q %v %b, want %q %v %b", i, got.key, got.action, got.levels, w.key, w.action, w.levels)
			}
		})
	}
}

// TestActionRows builds the `:` search's rows at every level. Every check is
// load-bearing:
//   - "every action is listed once": §4.7 searches every action, the ones
//     with no key included, so a key-table action missing here is one the
//     search has lost; "start a new plan" has two rows in the table and must
//     still be one row here.
//   - "every row has a name and a help line": a row with neither cannot be
//     found or read.
//   - "an action away from its level is dimmed with where it works": the
//     search lists it so a person learns where to go, and must not run it.
//   - "keys are named as they are pressed": the leader then the key in the
//     Agent, the key alone elsewhere.
//   - "blocked reasons are the row's": the search dims a row exactly when its
//     key would only say why it did nothing.
func TestActionRows(t *testing.T) {
	for _, lv := range []level{levelAllProjects, levelProject, levelAgent} {
		rows := actionRows(lv, defaultLeaderKey, leaderConds{})
		t.Run(lv.String()+" every action is listed once", func(t *testing.T) {
			seen := map[modeAction]int{}
			for _, r := range rows {
				seen[r.action]++
			}
			for _, kr := range keyTable {
				if searchSkips(kr.action) {
					if seen[kr.action] != 0 {
						t.Errorf("%v is listed but cannot be run from a list", kr.action)
					}
					continue
				}
				if seen[kr.action] != 1 {
					t.Errorf("%v (%q) listed %d times, want once", kr.action, kr.name, seen[kr.action])
				}
			}
		})
		t.Run(lv.String()+" every row has a name and a help line", func(t *testing.T) {
			for _, r := range rows {
				if strings.TrimSpace(r.name) == "" || strings.TrimSpace(r.help) == "" {
					t.Errorf("row %v has name %q, help %q", r.action, r.name, r.help)
				}
			}
		})
		t.Run(lv.String()+" an action away from its level is dimmed with where it works", func(t *testing.T) {
			for _, r := range rows {
				if _, ok := keyRowForAction(lv, r.action); ok {
					continue
				}
				if !strings.HasPrefix(r.blocked, "works ") {
					t.Errorf("row %v away from %v blocked = %q, want where it works", r.action, lv, r.blocked)
				}
			}
		})
		t.Run(lv.String()+" keys are named as they are pressed", func(t *testing.T) {
			for _, r := range rows {
				kr, ok := keyRowForAction(lv, r.action)
				if !ok || kr.key == "" {
					continue
				}
				want := kr.keyLabel()
				if lv == levelAgent {
					want = defaultLeaderKey + " " + want
				}
				if r.keyLabel != want {
					t.Errorf("row %v key = %q, want %q", r.action, r.keyLabel, want)
				}
			}
		})
		t.Run(lv.String()+" blocked reasons are the row's", func(t *testing.T) {
			for _, r := range rows {
				kr, ok := keyRowForAction(lv, r.action)
				if !ok {
					continue
				}
				if r.blocked != kr.blockedReason(leaderConds{}) {
					t.Errorf("row %v blocked = %q, want %q", r.action, r.blocked, kr.blockedReason(leaderConds{}))
				}
			}
		})
	}
	t.Run("start a new plan is n at all projects and by name in a project", func(t *testing.T) {
		at, _ := leaderRowFor(actionRows(levelAllProjects, defaultLeaderKey, leaderConds{}), actionPlanNew)
		in, _ := leaderRowFor(actionRows(levelProject, defaultLeaderKey, leaderConds{}), actionPlanNew)
		if at.keyLabel != "n" || in.keyLabel != "" {
			t.Errorf("start a new plan keys = %q at all projects, %q in a project; want n and none", at.keyLabel, in.keyLabel)
		}
	})
}

// TestKeyName pins how a hint names a key from each level. Load-bearing: the
// Agent rows (the leader comes first there, or the hint names a key the agent
// would get), and "restart from the agent", which has no Agent key and must
// still name the key it does have rather than nothing.
func TestKeyName(t *testing.T) {
	cases := []struct {
		name string
		lv   level
		a    modeAction
		want string
	}{
		{"new session at project", levelProject, actionNewSession, "n"},
		{"inbox at all projects", levelAllProjects, actionInbox, "i"},
		{"inbox from the agent (load-bearing)", levelAgent, actionInbox, "ctrl+] i"},
		{"back from the agent (load-bearing)", levelAgent, actionGoBack, "ctrl+] esc"},
		{"mouse at project names the agent's chord", levelProject, actionMouseToggle, "ctrl+] m"},
		{"restart from the agent (load-bearing)", levelAgent, actionRestart, "r"},
		{"a by-name action has no key", levelProject, actionPlanExtend, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyName(defaultLeaderKey, tc.lv, tc.a); got != tc.want {
				t.Fatalf("keyName(%v, %v) = %q, want %q", tc.lv, tc.a, got, tc.want)
			}
		})
	}
	t.Run("configured leader", func(t *testing.T) {
		if got := keyName("ctrl+o", levelAgent, actionDetails); got != "ctrl+o d" {
			t.Fatalf("keyName = %q, want ctrl+o d", got)
		}
	})
}

// TestLeaderDialogFilterAndMove covers the search's two state changes.
// Load-bearing: the highlight is clamped when a filter shrinks the list, or
// enter would run a row that is no longer on screen.
func TestLeaderDialogFilterAndMove(t *testing.T) {
	rows := actionRows(levelProject, defaultLeaderKey, leaderConds{})
	// typed is the outcomes of typing s one key at a time, as letters always
	// type into the query: s starts with s, the task-status key at Project.
	typed := func(s string) []leaderOutcome {
		var out []leaderOutcome
		for _, r := range s {
			out = append(out, leaderOutcome{kind: leaderFilter, text: string(r)})
		}
		return out
	}
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
		{"backspace edits the query", []leaderOutcome{{kind: leaderFilter, text: "cleax"}, {kind: leaderFilter, backspace: true}}, "clea", actionClean, -1},
		{"a query matching nothing empties the list", []leaderOutcome{{kind: leaderFilter, text: "zzzz"}}, "zzzz", actionNone, 0},
		{"the highlight is clamped by a filter", []leaderOutcome{{kind: leaderMove, delta: -1}, {kind: leaderFilter, text: "clean"}}, "clean", actionClean, -1},
		// Letters always type, so a name is found from its first letter even
		// when that letter is another action's key (n is new session's).
		{"a word starting with a key's letter filters", typed("start a new p"), "start a new p", actionPlanNew, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newLeaderDialog(rows, defaultLeaderKey)
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
	rows := actionRows(levelProject, defaultLeaderKey, leaderConds{hasActiveProject: true})
	for _, sz := range []struct {
		name string
		w, h int
	}{{"80x24", 80, 24}, {"120x40", 120, 40}} {
		t.Run(sz.name, func(t *testing.T) {
			d := newLeaderDialog(rows, defaultLeaderKey)
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
			if !strings.Contains(out, "go in") {
				t.Errorf("the first row is not shown:\n%s", out)
			}
		})
	}
	t.Run("empty filter says so", func(t *testing.T) {
		d := newLeaderDialog(rows, defaultLeaderKey)
		d.apply(leaderOutcome{kind: leaderFilter, text: "zzzz"})
		if out := d.view(80, 24); !strings.Contains(out, "no action matches") {
			t.Errorf("empty list does not say so:\n%s", out)
		}
	})
	t.Run("highlight scrolls into view", func(t *testing.T) {
		d := newLeaderDialog(rows, defaultLeaderKey)
		d.apply(leaderOutcome{kind: leaderMove, delta: -1}) // wraps to the last row
		last := rows[len(rows)-1]
		if out := d.view(80, 24); !strings.Contains(out, last.name) {
			t.Errorf("the highlighted last row %q is not on screen:\n%s", last.name, out)
		}
	})
}
