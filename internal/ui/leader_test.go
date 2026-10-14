package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// leaderTestRows is a small hand-built row set for the key-decision table, so
// each row of that table names exactly the row it runs. The ctrl+\ pair is
// load-bearing: two rows answer the same key (the list's close-pane and the
// overview's back-to-list), which is the one key conflict the real registry
// has.
func leaderTestRows() []leaderRow {
	return []leaderRow{
		{action: actionNewSession, mode: inputNormal, name: "new session", keyLabel: "n", keys: []string{"n"}, help: "Start a new session"},
		{action: actionClean, mode: inputNormal, name: "clean", keyLabel: "c", keys: []string{"c"}, help: "Clean", blocked: "no session selected"},
		{action: actionFocusPane, mode: inputNormal, name: "focus view", keyLabel: "enter/f", keys: []string{"enter", "f"}, help: "Focus"},
		{action: actionPaneClose, mode: inputNormal, name: "close pane", keyLabel: "ctrl+\\", keys: []string{"ctrl+\\"}, help: "Close", blocked: "no agent pane is open"},
		{action: actionDetailUnfocus, mode: inputDetail, name: "back to list", keyLabel: "esc/ctrl+\\", keys: []string{"esc", "ctrl+\\"}, help: "Back"},
		{action: actionPaneToggle, mode: inputDetail, name: "agent/overview", keyLabel: "ctrl+t", keys: []string{"ctrl+t"}, help: "Switch", blocked: "focus the right side first"},
	}
}

func leaderPress(s string) tea.KeyPressMsg {
	k, ok := paletteKeyPress(s)
	if !ok {
		panic("test key does not round-trip: " + s)
	}
	return k
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
//   - "shared key runs the runnable row": ctrl+\ belongs to two rows, and the
//     dimmed one must not swallow it from the one that can run.
//   - "enter on a dimmed highlight is ignored": enter must not be a back door
//     around the dimming.
//   - "enter is not focus": enter is one of Focus's keys, but in the dialog it
//     runs the highlight, so it must never be matched as a row key.
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
		{name: "enter is not focus", k: leaderPress("enter"), rows: rows, highlight: 4, want: leaderRun, wantRun: actionDetailUnfocus},
		{name: "enter with no rows is ignored", k: leaderPress("enter"), rows: nil, want: leaderIgnore},
		{name: "enter on a dimmed highlight is ignored", k: leaderPress("enter"), rows: rows, highlight: 1, want: leaderIgnore},
		{name: "backspace edits the query", k: leaderPress("backspace"), query: "cl", rows: rows, want: leaderFilter},
		{name: "action key with empty query runs it", k: leaderPress("n"), rows: rows, want: leaderRun, wantRun: actionNewSession},
		{name: "second key of a row runs it", k: leaderPress("f"), rows: rows, want: leaderRun, wantRun: actionFocusPane},
		{name: "action key with a query filters", k: leaderPress("n"), query: "clea", rows: rows, want: leaderFilter, wantText: "n"},
		{name: "letter matching no key filters", k: leaderPress("x"), rows: rows, want: leaderFilter, wantText: "x"},
		{name: "dimmed action key is ignored", k: leaderPress("c"), rows: rows, want: leaderIgnore},
		{name: "dimmed chord is ignored", k: leaderPress("ctrl+t"), rows: rows, want: leaderIgnore},
		{name: "shared key runs the runnable row", k: leaderPress("ctrl+\\"), rows: rows, want: leaderRun, wantRun: actionDetailUnfocus},
		{name: "non-printable unknown key is ignored", k: leaderPress("ctrl+y"), rows: rows, want: leaderIgnore},
		{name: "chord with a query is ignored", k: leaderPress("ctrl+\\"), query: "b", rows: rows, want: leaderIgnore},
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
// same rule cleanRequested enforces, and the reason names ctrl+\ as the way
// out), and the agent/overview rows (from the list the right side is not under
// control, so ctrl+t must not run there even through the dialog).
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
		{"palette always runs", actionPalette, leaderConds{}, ""},
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
		{"projects needs a project", actionProjects, with(func(c *leaderConds) { c.hasActiveProject = false }), "no projects"},
		{"projects with a project", actionProjects, ready, ""},

		{"clean needs a selection", actionClean, with(func(c *leaderConds) { c.selected = false; c.paneOpen = false }), "no session selected"},
		{"clean blocked by an open pane", actionClean, ready, "close the pane first (ctrl+\\)"},
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

// leaderMovementActions is the exclusion set restated independently of
// leader.go, so a change there that lets a movement row in fails here.
var leaderMovementActions = []modeAction{
	actionCursorUp, actionCursorDown, actionPageUp, actionPageDown,
	actionListTop, actionListBottom,
	actionDetailScrollUp, actionDetailScrollDown,
	actionDetailHalfPageUp, actionDetailHalfPageDown,
	actionDetailPageUp, actionDetailPageDown,
	actionDetailGotoTop, actionDetailGotoBottom,
	actionDetailTabNext, actionDetailTabPrev, actionDetailTabJump,
}

// TestLeaderRowsFromTheRealRegistry builds the rows from the shipped
// registry. Every check here is load-bearing:
//   - "every row has a help line" is the guard for actions added later: a new
//     rule in inputNormal or inputDetail appears in the dialog by itself and
//     fails this test until its line is written in leaderHelp.
//   - "every action is present" pins the merge: nothing the list or the
//     overview can do is missing from the dialog.
//   - "no duplicates" pins the merge's other half: Build, Restart, Quit and the
//     rest are rules in both modes and must be one row each.
func TestLeaderRowsFromTheRealRegistry(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	rows := leaderRows(reg, leaderConds{})

	t.Run("every row has a help line", func(t *testing.T) {
		for _, r := range rows {
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
		for _, r := range rows {
			if slices.Contains(leaderMovementActions, r.action) {
				t.Errorf("movement action %v is listed", r.action)
			}
			if r.action == actionOpenLeader {
				t.Errorf("the leader lists itself")
			}
		}
	})

	t.Run("every action is present", func(t *testing.T) {
		have := map[modeAction]bool{}
		for _, r := range rows {
			have[r.action] = true
		}
		for _, mode := range []inputMode{inputNormal, inputDetail} {
			for _, rule := range reg.spec(mode).rules {
				if slices.Contains(leaderMovementActions, rule.action) || rule.action == actionOpenLeader {
					continue
				}
				if !have[rule.action] {
					t.Errorf("%v action %v is missing from the dialog", mode, rule.action)
				}
			}
		}
	})

	t.Run("rows carry name, key and mode", func(t *testing.T) {
		for _, r := range rows {
			if r.name == "" || r.keyLabel == "" || len(r.keys) == 0 {
				t.Errorf("row %v is incomplete: %+v", r.action, r)
			}
			want := inputNormal
			if !slices.ContainsFunc(reg.spec(inputNormal).rules, func(m modeRule) bool { return m.action == r.action }) {
				want = inputDetail
			}
			if r.mode != want {
				t.Errorf("row %v mode = %v, want %v", r.action, r.mode, want)
			}
		}
	})

	t.Run("help order is kept", func(t *testing.T) {
		// New precedes Clean in inputNormal's help order, and the detail-only
		// rows follow every inputNormal row.
		idx := func(a modeAction) int {
			return slices.IndexFunc(rows, func(r leaderRow) bool { return r.action == a })
		}
		if idx(actionNewSession) > idx(actionClean) {
			t.Errorf("new session sorts after clean")
		}
		if idx(actionPaneToggle) < idx(actionQuit) {
			t.Errorf("a detail-only row sorts before an inputNormal row")
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

// TestLeaderDialogFilterAndMove covers the component's two state changes.
// Load-bearing: the highlight is clamped when a filter shrinks the list, or
// enter would run a row that is no longer on screen.
func TestLeaderDialogFilterAndMove(t *testing.T) {
	rows := leaderRows(newModeRegistry(defaultKeyMap()), leaderConds{})
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
	rows := leaderRows(newModeRegistry(defaultKeyMap()), leaderConds{hasActiveProject: true})
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
