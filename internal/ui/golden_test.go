package ui

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/spice"
	"github.com/dphaener/afk/internal/state"
)

// updateGolden rewrites every golden frame from the current rendering instead
// of comparing against it: `go test ./internal/ui/ -run Golden -update`. The
// resulting `git diff internal/ui/testdata/golden` IS the review artifact for a
// layout change, which is the reason these frames exist — every other frame
// assertion in this package is a strings.Contains or a width count, and neither
// shows a reviewer what the dashboard looks like.
var updateGolden = flag.Bool("update", false, "rewrite golden frames under testdata/golden")

// assertGolden compares one rendered frame against testdata/golden/<name>.txt.
//
// The frame is ANSI-stripped (render, render_test.go) so the file holds the
// characters a user sees and nothing else; colour is asserted by the style
// tests, not here. Line endings are normalised and a trailing newline is added
// so editors and `git diff` treat the files as ordinary text.
func assertGolden(t *testing.T, name, frame string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	got := strings.ReplaceAll(frame, "\r\n", "\n") + "\n"
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v (run with -update to create it)", path, err)
	}
	if string(want) == got {
		return
	}
	t.Errorf("frame differs from %s (run with -update to accept):\n--- want\n%s\n--- got\n%s", path, want, got)
}

// goldenNow is the fixed clock every golden frame is rendered at. testDeps
// pins Now to time.Unix(1_000_000, 0); the fixture's lastLoad and updated_at
// values are chosen relative to it so every rendered age — the wedged
// badge's, the Overview's updated time — never drifts.
//
// The header used to be the main reason this clock exists: it carried a
// "refreshed Xs ago · every Ys" cadence string keyed off lastLoad, and the
// offset below was picked to make it read "4s ago". That string is gone, so
// no frame renders lastLoad as text any more. It is still set, and still
// matters: switcherSessions (model.go) gates its live-session swap on
// lastLoad being non-zero, so the switcher fixture's per-project counts
// depend on it.
var goldenNow = time.Unix(1_000_000, 0)

// goldenSessions is the six-row fixture the mockups were drawn from, one row
// per rendering branch the list has: an executing session awaiting input, a
// labelled reviewing session with a verdict, a planning session with no plan
// yet, a done session whose host has stopped, a scratch session, and a BROKEN
// worktree. Names are chosen so the longest ("fix-login-redirect") is wider
// than the 80-column name budget and the ellipsis path is exercised.
func goldenSessions() []Session {
	auth := sess("feat-auth")
	auth.HostRunning = true
	auth.HostPID = 4812
	auth.AwaitingInput = true
	auth.LoopCount = 3
	auth.Progress = discover.Progress{Total: 7, Complete: 3, Incomplete: 4}
	auth.DiffStats = DiffStats{Added: 412, Removed: 88, Files: 9}
	// A small, realistic slice of raw `git diff` output — two files, a hunk
	// header each, added/removed/context lines, and at least one
	// tab-indented context line — so the regenerated dash-120x40-diff golden
	// pins real diff rendering (headers, colouring shapes, tab expansion)
	// instead of the "loading diff…" placeholder an empty DiffText would
	// produce (see W6.T2).
	auth.DiffText = []string{
		"diff --git a/internal/auth/user.go b/internal/auth/user.go",
		"index abc1234..def5678 100644",
		"--- a/internal/auth/user.go",
		"+++ b/internal/auth/user.go",
		"@@ -10,7 +10,9 @@ func Authenticate(token string) (*User, error) {",
		" \tif token == \"\" {",
		" \t\treturn nil, ErrMissingToken",
		" \t}",
		"-\tuser := lookup(token)",
		"+\tuser, err := lookup(token)",
		"+\tif err != nil {",
		"+\t\treturn nil, err",
		"+\t}",
		" \treturn user, nil",
		" }",
		"diff --git a/internal/auth/session.go b/internal/auth/session.go",
		"index 1111111..2222222 100644",
		"--- a/internal/auth/session.go",
		"+++ b/internal/auth/session.go",
		"@@ -1,3 +1,3 @@",
		" package auth",
		"-const sessionTTL = 15 * time.Minute",
		"+const sessionTTL = 30 * time.Minute",
	}
	auth.IssuesTail = []string{
		"legacy_sessions has no FK to users — confirm before dropping",
		"rate limiter test flaky under -race",
	}
	auth.LearningsTail = []string{"huh v2 needs explicit WithWidth"}
	auth.Plan = &state.Plan{Title: "Auth", Waves: []state.Wave{
		{Number: 1, Name: "Foundations", Tasks: []state.Task{
			{ID: "W1.T1", Wave: 1, Num: 1, Title: "Add session model", State: state.CheckComplete},
			{ID: "W1.T2", Wave: 1, Num: 2, Title: "Wire auth middleware", State: state.CheckComplete},
			{ID: "W1.T3", Wave: 1, Num: 3, Title: "Seed admin user", State: state.CheckComplete},
		}},
		{Number: 2, Name: "Migration", Tasks: []state.Task{
			{ID: "W2.T1", Wave: 2, Num: 1, Title: "Migrate legacy sessions", State: state.CheckIncomplete},
			{ID: "W2.T2", Wave: 2, Num: 2, Title: "Add logout endpoint", State: state.CheckIncomplete},
			{ID: "W2.T3", Wave: 2, Num: 3, Title: "Rotate refresh tokens", State: state.CheckIncomplete},
			{ID: "W2.T4", Wave: 2, Num: 4, Title: "Remove legacy table", State: state.CheckIncomplete},
		}},
	}}

	billing := sess("feat-billing")
	billing.Label = "Billing v2"
	billing.BaseBranch = "feat-auth"
	billing.Phase = state.PhaseReviewing
	billing.HostRunning = true
	billing.HostPID = 4813
	billing.Progress = discover.Progress{Total: 7, Complete: 7}
	billing.DiffStats = DiffStats{Added: 1204, Removed: 31, Files: 22}
	billing.Verdict = &state.ReviewVerdict{
		Verdict:    state.VerdictReject,
		Compliance: state.CheckPass,
		Quality:    state.CheckFail,
		QA:         state.CheckPass,
		Findings: []state.Finding{
			{Severity: state.SeverityMajor, TaskID: "W2.T1", Description: "invoice totals ignore tax rounding"},
		},
	}

	login := sess("fix-login-redirect")
	login.Phase = state.PhasePlanning
	login.HostRunning = true
	login.HostPID = 4814
	login.AwaitingInput = true
	login.Progress = discover.Progress{}

	search := sess("feat-search")
	search.Phase = state.PhaseDone
	search.Progress = discover.Progress{Total: 5, Complete: 5}
	search.DiffStats = DiffStats{Added: 88, Removed: 12, Files: 4}

	scratch := Session{SessionState: discover.SessionState{
		Name:         "scratch-1",
		WorktreePath: "/repo",
		Scratch:      true,
		HostRunning:  true,
		HostPID:      4815,
	}}

	broken := Session{SessionState: discover.SessionState{
		Name:         "old-migration",
		WorktreePath: "/wt/old-migration",
		Branch:       "old-migration",
		Kind:         session.KindWorktreeMissing,
		Reason:       "worktree directory missing",
	}}

	return []Session{auth, billing, login, search, scratch, broken}
}

// goldenStack is the git-spice topology the fixture sessions live in:
// main → feat-auth → feat-billing, and main → fix-login-redirect, with
// feat-auth checked out. It is built by hand rather than parsed from
// testdata/stack/log_short.json because that fixture's branch names (feat-a,
// feat-b) match none of goldenSessions', and the Overview's stack line and the
// Stack tab's owner annotations only appear for branches a session owns.
func goldenStack() []spice.Branch {
	ref := func(name string) *spice.BranchRef { return &spice.BranchRef{Name: name} }
	return []spice.Branch{
		{Name: "main", Ups: []spice.BranchRef{{Name: "feat-auth"}, {Name: "fix-login-redirect"}}},
		{Name: "feat-auth", Current: true, Down: ref("main"), Ups: []spice.BranchRef{{Name: "feat-billing"}}},
		{Name: "feat-billing", Down: ref("feat-auth")},
		{Name: "fix-login-redirect", Down: ref("main")},
	}
}

// goldenModel is a dashboard sized to w×h through the same tea.WindowSizeMsg
// the program receives, so every sizer (list, form, switcher, palette, detail
// viewport) is in the state a real resize leaves it in — not the shortcut
// testModel takes.
//
// It deliberately wires NO projects: m.projects, m.activeProject and
// m.projectSessions are all left zero. Do not "fix" that by adding projects
// here. The header redesign has two branches and this is how both get pinned —
// a project-less base gives every frame built on it the session box's
// "Sessions" fallback label, an empty cross-project attention line and no
// "p: switch projects" hint, while TestGoldenMultiProjectDashboard sets those
// three fields on its own copy to pin the populated branch. Adding projects
// here would silently delete the fallback branch's only golden coverage.
func goldenModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := New("/repo", 2*time.Second, testDeps())
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m.setSessions(goldenSessions())
	m.lastLoad = goldenNow.Add(-4 * time.Second)
	m.stack = stackState{Detected: true, Branches: goldenStack()}
	return m
}

// TestGoldenDashboardFrames pins the whole dashboard at the two sizes the
// redesign was mocked at. 80×24 is the smallest terminal the README promises
// works; 120×40 is a comfortable default. Any layout change lands here first,
// as a frame diff a reviewer can read.
func TestGoldenDashboardFrames(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"dash-80x24", 80, 24},
		{"dash-120x40", 120, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel(t, tc.w, tc.h)
			assertGolden(t, tc.name, render(m))
		})
	}
}

// TestGoldenModals pins the three floating overlays at 120×40 over the fixture
// dashboard: the project switcher, the command palette and the creation form.
// Each is opened through its own key so the frame is the one a user sees.
func TestGoldenModals(t *testing.T) {
	open := func(t *testing.T, m Model, k tea.KeyPressMsg) Model {
		m, cmd := update(t, m, k)
		return pumpForm(t, m, cmd, 0)
	}
	t.Run("dash-120x40-switcher", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		m.projects = []project.Project{{Path: "/repo", Name: "api"}, {Path: "/repos/afk", Name: "afk"}}
		m.activeProject = 0
		m = open(t, m, keyRune('p').(tea.KeyPressMsg))
		if !m.switcherOpen {
			t.Fatal("p did not open the switcher")
		}
		assertGolden(t, "dash-120x40-switcher", render(m))
	})
	t.Run("dash-120x40-palette", func(t *testing.T) {
		m := open(t, goldenModel(t, 120, 40), keyRune(':').(tea.KeyPressMsg))
		if !m.paletteOpen {
			t.Fatal(": did not open the palette")
		}
		assertGolden(t, "dash-120x40-palette", render(m))
	})
	t.Run("dash-120x40-form", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		m.plainBranches = []string{"main", "feat-auth", "feat-billing"}
		m.plainTrunk = "main"
		m = open(t, m, keyRune('n').(tea.KeyPressMsg))
		if !m.formOpen {
			t.Fatal("n did not open the creation form")
		}
		assertGolden(t, "dash-120x40-form", render(m))
	})
}

// TestGoldenDialogs pins the five confirmation/prompt dialogs at 120×40 over
// the fixture dashboard: the clean confirmation (on the selected feat-auth),
// the restart confirmation (on a wedged fixture), the label editor, the
// failed-create report, and the one-off provider picker (fresh from `o`, so on
// its default row).
func TestGoldenDialogs(t *testing.T) {
	t.Run("dash-120x40-clean", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		m, cmd := update(t, m, keyRune('c'))
		m = pumpForm(t, m, cmd, 0)
		if m.modal != inputConfirmClean {
			t.Fatal("c did not open the clean confirmation")
		}
		assertGolden(t, "dash-120x40-clean", render(m))
	})
	t.Run("dash-120x40-restart", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		sessions := goldenSessions()
		sessions[0].WedgedSince = goldenNow.Add(-12 * time.Minute).UTC().Format(time.RFC3339)
		m.setSessions(sessions)
		m, _ = update(t, m, keyRune('r'))
		if m.modal != inputConfirmRestart {
			t.Fatal("r did not open the restart confirmation")
		}
		assertGolden(t, "dash-120x40-restart", render(m))
	})
	t.Run("dash-120x40-label", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		m, _ = update(t, m, keyRune('l'))
		if m.modal != inputEditLabel {
			t.Fatal("l did not open the label editor")
		}
		assertGolden(t, "dash-120x40-label", render(m))
	})
	t.Run("dash-120x40-spawn-error", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		m.modal = inputSpawnError
		m.spawnErrName = "feat-search-v2"
		m.spawnErrHeadline = `afk new "feat-search-v2" failed: exit status 1`
		m.spawnErrTail = []string{
			"fatal: a branch named 'feat-search-v2' already exists",
			"hint: pick another name or delete the branch first",
		}
		assertGolden(t, "dash-120x40-spawn-error", render(m))
	})
	t.Run("dash-120x40-provider-picker", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		m, _ = update(t, m, keyRune('o'))
		if m.modal != inputPickProvider {
			t.Fatal("o did not open the provider picker")
		}
		assertGolden(t, "dash-120x40-provider-picker", render(m))
	})
}

// TestGoldenToast pins a fresh status floating in the corner over the fixture
// dashboard. The footer below it carries the help line only — the toast is the
// whole of the notification, and nothing survives its expiry.
//
// Two kinds are framed. The info toast is markerless by design, so it is the
// only frame that shows the bare message; the error toast carries the ✗
// marker, which is the one per-kind signal an ANSI-stripped fixture can show
// (toast_test.go's style test asserts that each kind renders distinctly from
// the others, ANSI included; it does not assert on any specific border
// colour, and neither does this fixture). Both seeds are the strings the code
// produces for those sites — model.go's restart outcome and messages.go's
// restartFailedStatus — so the frames depict copy a user can actually see.
func TestGoldenToast(t *testing.T) {
	t.Run("dash-120x40-toast", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		m.notify(notifyInfo, "restarted feat-billing — claude is resuming its conversation · enter/f to reopen the agent")
		assertGolden(t, "dash-120x40-toast", render(m))
	})
	t.Run("dash-120x40-toast-error", func(t *testing.T) {
		m := goldenModel(t, 120, 40)
		m.notify(notifyError, restartFailedStatus("feat-billing", "/repo/.afk-worktrees/feat-billing", errors.New("exit status 1")))
		assertGolden(t, "dash-120x40-toast-error", render(m))
	})
}

// TestGoldenDetailTabs pins each detail tab at 120×40 with the first fixture
// session selected. The Overview is already in dash-120x40; these are the
// other four, so a change to any one tab's composition is reviewable on its
// own frame.
func TestGoldenDetailTabs(t *testing.T) {
	cases := []struct {
		name string
		tab  detailTab
	}{
		{"dash-120x40-waves", tabWaves},
		{"dash-120x40-stack", tabStack},
		{"dash-120x40-notes", tabNotes},
		{"dash-120x40-diff", tabDiff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel(t, 120, 40)
			m.detailTab = tc.tab
			m.syncDetailViewport()
			assertGolden(t, tc.name, render(m))
		})
	}
}

// TestGoldenMultiProjectDashboard pins the header redesign's populated path,
// which none of the other 14 frames exercise: goldenModel's own base state is
// deliberately project-less (see its doc comment), so every other golden
// frame renders the "Sessions" fallback label and an empty attention line —
// that IS the fallback path's golden coverage, and it must stay that way
// rather than gaining projects here too. This is the one fixture that turns
// on m.projects, m.activeProject and m.projectSessions together, so the
// review diff can show the sidebar's border naming the active project, the
// header's named attention line, and the "p: switch projects" hint all at
// once.
//
// Three projects, like dash-120x40-switcher's fixture, but with
// m.projectSessions populated so the sort in awaitingElsewhere (and in the
// switcher, which shares the comparator) has awaiting counts to order by:
// "alpha" is active (path "/repo", matching goldenModel's own session
// fixture, so the sidebar's existing session rows still make sense under an
// "alpha" title) and carries no entry in projectSessions, so it is excluded
// from the attention line by path even though it is also the first project
// alphabetically. "beta" and "gamma" are both "other" and both awaiting,
// which is the row that proves attentionNamed's ordering (by
// sortSwitcherProjects, not by map iteration) and its "N awaiting in X · M in
// Y" joining both hold at once — beta sorts first only because "beta" <
// "gamma", not because of insertion order in the map literal below.
func TestGoldenMultiProjectDashboard(t *testing.T) {
	m := goldenModel(t, 120, 40)
	m.projects = []project.Project{
		{Path: "/repo", Name: "alpha"},
		{Path: "/repos/beta", Name: "beta"},
		{Path: "/repos/gamma", Name: "gamma"},
	}
	m.activeProject = 0
	m.projectSessions = map[string][]discover.SessionState{
		"/repos/gamma": {
			{Name: "feat-search", AwaitingInput: true},
		},
		"/repos/beta": {
			{Name: "feat-payments", AwaitingInput: true},
			{Name: "fix-webhook", AwaitingInput: true},
		},
	}
	assertGolden(t, "dash-120x40-multiproject", render(m))
}

// goldenBuild is the build the Build tab's frame is drawn from: one task in
// each state an owner meets in a normal week (done, running, shipping, parked
// on a question, failed, waiting on another task, and one that is theirs to
// do), a blocking inbox item, and the build paused.
func goldenBuild() buildview.Snapshot {
	return buildview.Snapshot{
		Root:     "/repo",
		HasBuild: true,
		Rows: []buildview.Row{
			{ID: "T1.1", Title: "Session model", Status: "done", Detail: "PR #12 · 4c30e4a"},
			{ID: "T1.2", Title: "Auth middleware", Status: "running", Detail: "feat-auth · executing"},
			{ID: "T2.1", Title: "Billing totals", Status: "shipping", Detail: "ci running · PR #14"},
			{ID: "T2.2", Title: "Legacy sessions", Status: "parked", Detail: "waiting on you: q-3"},
			{ID: "T2.3", Title: "Login redirect", Status: "failed", Detail: "attempt 2"},
			{ID: "T3.1", Title: "Search index", Status: "pending", Detail: "waiting for T2.1"},
			{ID: "T3.2", Title: "Pick a mail provider", Status: "pending", Detail: "ready · yours to do", Owner: true},
		},
		Done:         1,
		Total:        7,
		Current:      "T1.2",
		CurrentPhase: "executing",
		Paused:       true,
		Items: []inbox.Item{
			{ID: "q-3", Kind: "question", Blocking: true, Title: "Drop the legacy_sessions table?"},
		},
	}
}

// goldenInbox is the build read the inbox frames are drawn from: goldenBuild
// with the inbox an owner finds after a day away. One question is holding a
// task up and has options, one failure is holding another up, one notice
// blocks nothing, and one question was answered from the web page. The times
// are set against goldenNow so every age on screen is fixed.
func goldenInbox() buildview.Snapshot {
	at := func(d time.Duration) string { return goldenNow.Add(-d).UTC().Format(time.RFC3339) }
	snap := goldenBuild()
	snap.Items = []inbox.Item{
		{
			ID: "k7m2pq", CreatedAt: at(3 * time.Hour), Task: "T2.2", Session: "feat-auth",
			Kind: "question", Blocking: true,
			Title: "Drop the legacy_sessions table?",
			Body: "The migration in T2.2 removes legacy_sessions. Two reports in admin/ still read it.\n\n" +
				"Dropping it now breaks those reports until T3.1 moves them to the new table.",
			Options:     []string{"Drop it now and fix the reports in T3.1", "Keep it for one release", "Leave it and close T2.2"},
			Recommended: "Keep it for one release",
		},
		{
			ID: "r4x9wd", CreatedAt: at(25 * time.Minute), Task: "T2.3", Session: "fix-login-redirect",
			Kind: "failure", Blocking: true,
			Title: "Login redirect failed its checks twice",
			Body:  "--- FAIL: TestRedirectAfterLogin (0.02s)\n    redirect_test.go:41: got /login, want /dashboard",
		},
		{
			ID: "b3n8ht", CreatedAt: at(50 * time.Hour), Task: "T3.1",
			Kind: "notice", Title: "Used the staging key for the search index",
			Assumed: "staging is fine until launch",
		},
		{
			ID: "p5c6yz", CreatedAt: at(26 * time.Hour), Task: "T3.2",
			Kind: "question", Blocking: true, Title: "Which mail provider?",
			Options: []string{"Postal", "Mailer"},
			Answer:  "Postal", AnsweredAt: at(22 * time.Hour), AnsweredVia: inbox.ViaWeb,
		},
	}
	return snap
}

// TestGoldenInbox pins the inbox overlay's two screens at 120×40 over the
// fixture dashboard, reached the way a user reaches them: the build read
// arrives, `i` opens the list, enter opens the first item. The item frame is
// the one a layout change is most likely to move: the body, the numbered
// options with the recommended one marked, and the answer field are all on
// it, with a number typed so the field's contents are part of the frame.
func TestGoldenInbox(t *testing.T) {
	m := goldenModel(t, 120, 40)
	m, _ = update(t, m, buildLoadedMsg{snap: goldenInbox(), gen: m.projectGen})
	m, _ = update(t, m, keyRune('i'))
	if !m.inboxOpen {
		t.Fatal("i did not open the inbox")
	}
	t.Run("dash-inbox-120x40", func(t *testing.T) {
		assertGolden(t, "dash-inbox-120x40", render(m))
	})
	t.Run("dash-inbox-item-120x40", func(t *testing.T) {
		im, _ := update(t, m, keyEnter)
		im, _ = update(t, im, keyRune('2'))
		if im.inbox.screen != inboxScreenItem || im.inbox.input.Value() != "2" {
			t.Fatalf("the item screen is not up with 2 typed (screen=%v text=%q)", im.inbox.screen, im.inbox.input.Value())
		}
		assertGolden(t, "dash-inbox-item-120x40", render(im))
	})
}

// TestGoldenBuildTab pins the Build tab at 120×40 over the fixture dashboard,
// reached the way a user reaches it: the build's data arrives, then `b`. The
// notice that key raises is cleared before the frame is taken, because a toast
// floats over the pane's top corner and would hide the tab strip, which is
// the part of this frame a layout change is most likely to move.
func TestGoldenBuildTab(t *testing.T) {
	m := goldenModel(t, 120, 40)
	m, _ = update(t, m, buildLoadedMsg{snap: goldenBuild(), gen: m.projectGen})
	m, _ = update(t, m, keyRune('b'))
	if m.activeDetailTab() != tabBuild || m.focus != focusRight {
		t.Fatalf("b did not open the Build tab (tab=%v focus=%v)", m.activeDetailTab(), m.focus)
	}
	m.clearStatus()
	assertGolden(t, "dash-build-120x40", render(m))
}
