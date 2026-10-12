package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/spice"
)

// loadStackFixture reads and parses testdata/stack/log_short.json via
// internal/spice's own parser (ParseLogShort) — reused as-is, per this
// task's "reuse — do not re-parse JSON here" instruction. The fixture
// encodes a two-level stack (main -> feat-a -> feat-b, and main -> feat-c)
// with lines deliberately out of topological AND alphabetical order, since
// `gs log short --json`'s own order is documented as unspecified.
func loadStackFixture(t *testing.T) []spice.Branch {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "stack", "log_short.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	branches, err := spice.ParseLogShort(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return branches
}

// --- stackPanel: gs-gated visibility ---

func TestStackPanel_HiddenWhenNotDetected(t *testing.T) {
	ctx := panelContext{Stack: stackState{Detected: false}}
	if got := (stackPanel{}).Render(ctx, 80); got != nil {
		t.Fatalf("Render() = %v, want nil (hidden) for a non-gs repo", got)
	}
}

func TestStackPanel_DetectedButErrored(t *testing.T) {
	ctx := panelContext{Stack: stackState{Detected: true, Err: "gs log short --json: exit status 1"}}
	got := (stackPanel{}).Render(ctx, 80)
	if len(got) != 1 || !strings.Contains(got[0], "exit status 1") {
		t.Fatalf("Render() = %v, want one line mentioning the error", got)
	}
}

func TestStackPanel_DetectedNoBranches(t *testing.T) {
	ctx := panelContext{Stack: stackState{Detected: true}}
	got := (stackPanel{}).Render(ctx, 80)
	if len(got) != 1 || !strings.Contains(got[0], "No tracked branches") {
		t.Fatalf("Render() = %v, want a 'no tracked branches' line", got)
	}
}

// --- renderStackTree: topology rendering from a fixture ---

func TestRenderStackTree_FromFixture(t *testing.T) {
	branches := loadStackFixture(t)
	sessions := []Session{
		sess("session-a"), // Branch defaults to the session name (see sess()).
	}
	sessions[0].Branch = "feat-a"

	// Stripped for the same reason render() strips: indentation and substring
	// checks are claims about cells, and lipgloss v2 emits styling a test has no
	// profile writer to remove.
	lines := renderStackTree(branches, sessions, 80)
	for i, ln := range lines {
		lines[i] = xansi.Strip(ln)
	}
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4 (one per branch): %v", len(lines), lines)
	}

	joined := strings.Join(lines, "\n")

	// Depth ordering: main first (root), then its children depth-first in
	// name order (feat-a before feat-c), with feat-b nested under feat-a.
	order := []string{"main", "feat-a", "feat-b", "feat-c"}
	lastIdx := -1
	for _, name := range order {
		idx := strings.Index(joined, name)
		if idx == -1 {
			t.Fatalf("output missing branch %q:\n%s", name, joined)
		}
		if idx <= lastIdx {
			t.Fatalf("branch %q rendered out of expected depth-first order:\n%s", name, joined)
		}
		lastIdx = idx
	}

	// feat-b is deeper than feat-a, which is deeper than main. Depth is where
	// the branch name starts on its line: the tree package draws connectors
	// ("├── ", "│   ╰── ") rather than indenting with spaces, so leading
	// whitespace no longer measures it.
	mainLine := lines[indexContaining(t, lines, "main")]
	featALine := lines[indexContaining(t, lines, "feat-a")]
	featBLine := lines[indexContaining(t, lines, "feat-b")]
	featCLine := lines[indexContaining(t, lines, "feat-c")]
	depth := func(line, name string) int { return strings.Index(line, name) }
	if strings.ContainsAny(mainLine, "├╰│") {
		t.Errorf("main is the root and should carry no connector, line = %q", mainLine)
	}
	if depth(featALine, "feat-a") <= depth(mainLine, "main") {
		t.Errorf("feat-a should sit deeper than main: %q vs %q", featALine, mainLine)
	}
	if depth(featBLine, "feat-b") <= depth(featALine, "feat-a") {
		t.Errorf("feat-b should sit deeper than feat-a: %q vs %q", featBLine, featALine)
	}
	if depth(featCLine, "feat-c") != depth(featALine, "feat-a") {
		t.Errorf("feat-c (sibling of feat-a) should sit at the same depth: %q vs %q", featCLine, featALine)
	}
	if !strings.Contains(featALine, "├──") || !strings.Contains(featCLine, "╰──") {
		t.Errorf("siblings should be joined by connectors, last one rounded: %q / %q", featALine, featCLine)
	}

	// Ownership annotation: session-a owns feat-a.
	if !strings.Contains(featALine, "session-a") {
		t.Errorf("feat-a line = %q, want it annotated with owning session-a", featALine)
	}
	if strings.Contains(mainLine, "session-a") || strings.Contains(featBLine, "session-a") {
		t.Errorf("only feat-a should be annotated with session-a")
	}

	// Current-branch marker on feat-b.
	if !strings.Contains(featBLine, "●") {
		t.Errorf("feat-b (current) line = %q, want the current-branch marker", featBLine)
	}
	if strings.Contains(mainLine, "●") {
		t.Errorf("main (not current) should use the non-current marker")
	}
}

func TestRenderStackTree_MultipleOwners(t *testing.T) {
	branches := loadStackFixture(t)
	s1 := sess("s1")
	s1.Branch = "main"
	s2 := sess("s2")
	s2.Branch = "main"
	lines := renderStackTree(branches, []Session{s1, s2}, 80)
	mainLine := lines[indexContaining(t, lines, "main")]
	if !strings.Contains(mainLine, "s1") || !strings.Contains(mainLine, "s2") {
		t.Fatalf("main line = %q, want both owning sessions listed", mainLine)
	}
}

func TestRenderStackTree_NoOrphanedBranchDropped(t *testing.T) {
	// A branch whose Down points at a name absent from the stream (never
	// happens from real gs output, but the renderer must not silently drop
	// data or hang on it) still gets surfaced.
	branches := []spice.Branch{
		{Name: "orphan", Down: &spice.BranchRef{Name: "missing-parent"}},
	}
	lines := renderStackTree(branches, nil, 80)
	if len(lines) != 1 || !strings.Contains(lines[0], "orphan") {
		t.Fatalf("lines = %v, want the orphaned branch surfaced, not dropped", lines)
	}
}

func TestOwningSessions(t *testing.T) {
	tests := []struct {
		name     string
		sessions []Session
		branch   string
		want     string
	}{
		{"no owner", []Session{sess("a")}, "nonexistent", ""},
		{"one owner", []Session{sess("a")}, "a", "a"},
		{"two owners", []Session{sess("a"), func() Session { s := sess("b"); s.Branch = "a"; return s }()}, "a", "a, b"},
		// A fault row carries its branch's name but OWNS nothing — it is the
		// verdict that no session answers for that worktree, so annotating the
		// branch with it would label the branch as owned by the very entry
		// that reports the owner missing.
		{"a fault row is not an owner", []Session{faultSess("a", session.KindWorktreeNoSession, "worktree has no afk session")}, "a", ""},
		{"a fault row does not join a real owner", []Session{sess("a"), faultSess("a", session.KindWorktreeMissing, "worktree directory no longer exists")}, "a", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := owningSessions(tt.sessions, tt.branch); got != tt.want {
				t.Errorf("owningSessions() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- stackCmd: async refresh wiring ---

func TestStackCmd_NotDetected(t *testing.T) {
	m := testModel()
	m.deps.SpiceDetect = func(string) bool { return false }
	m.deps.SpiceLogShort = func(string) ([]spice.Branch, error) {
		t.Fatalf("LogShort must not run when SpiceDetect returns false")
		return nil, nil
	}
	msg := m.stackCmd()().(stackLoadedMsg)
	if msg.stack.Detected {
		t.Fatalf("stack.Detected = true, want false")
	}
}

func TestStackCmd_DetectedSuccess(t *testing.T) {
	branches := loadStackFixture(t)
	m := testModel()
	m.deps.SpiceDetect = func(string) bool { return true }
	m.deps.SpiceLogShort = func(dir string) ([]spice.Branch, error) {
		if dir != "/repo" {
			t.Fatalf("LogShort got dir %q, want /repo", dir)
		}
		return branches, nil
	}
	msg := m.stackCmd()().(stackLoadedMsg)
	if !msg.stack.Detected {
		t.Fatalf("stack.Detected = false, want true")
	}
	if len(msg.stack.Branches) != len(branches) {
		t.Fatalf("Branches = %d, want %d", len(msg.stack.Branches), len(branches))
	}
	if msg.stack.Err != "" {
		t.Fatalf("Err = %q, want empty", msg.stack.Err)
	}
}

func TestStackCmd_DetectedLogShortError(t *testing.T) {
	m := testModel()
	m.deps.SpiceDetect = func(string) bool { return true }
	m.deps.SpiceLogShort = func(string) ([]spice.Branch, error) {
		return nil, errors.New("gs blew up")
	}
	msg := m.stackCmd()().(stackLoadedMsg)
	if !msg.stack.Detected {
		t.Fatalf("stack.Detected = false, want true")
	}
	if !strings.Contains(msg.stack.Err, "gs blew up") {
		t.Fatalf("Err = %q, want to contain the underlying error", msg.stack.Err)
	}
}

// TestStackCmd_DropsTickWhileRefreshInFlight is the regression guard for the
// runaway that made the dashboard unusable on large stacks: `gs log short`
// outlasting the 2s tick, every tick launching another one anyway, and the
// contention between them making each slower until the machine saturated.
//
// The load-bearing assertions are that the second Cmd never calls LogShort at
// all (dropped, not queued behind the first), and that the guard releases
// afterwards so the panel keeps refreshing rather than freezing at one pass.
func TestStackCmd_DropsTickWhileRefreshInFlight(t *testing.T) {
	m := testModel()
	m.deps.SpiceDetect = func(string) bool { return true }

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var calls atomic.Int32
	m.deps.SpiceLogShort = func(string) ([]spice.Branch, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return nil, nil
	}

	first := make(chan tea.Msg, 1)
	go func() { first <- m.stackCmd()() }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first refresh never reached LogShort")
	}

	// The second tick runs on its own goroutine so a regression that lets it
	// through fails on the timeout instead of deadlocking the whole package
	// against the release channel it would then be blocked on.
	second := make(chan tea.Msg, 1)
	go func() { second <- m.stackCmd()() }()
	select {
	case msg := <-second:
		if msg != nil {
			t.Fatalf("second tick returned %#v while a refresh was in flight, want nil", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second tick blocked inside LogShort instead of being dropped")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("LogShort calls = %d, want 1 — the dropped tick must not run it", got)
	}

	close(release)
	select {
	case msg := <-first:
		if _, ok := msg.(stackLoadedMsg); !ok {
			t.Fatalf("first refresh returned %#v, want stackLoadedMsg", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first refresh never returned")
	}

	if msg := m.stackCmd()(); msg == nil {
		t.Fatal("guard never released: a tick after the refresh finished was dropped too")
	}
}

// TestStackCmd_NilGuardStillRefreshes covers a Model built as a bare struct
// literal rather than by New. Nothing in the package does that today, but a
// nil-pointer panic on the refresh path would be a poor way to find out.
func TestStackCmd_NilGuardStillRefreshes(t *testing.T) {
	m := testModel()
	m.stackRefreshing = nil
	m.deps.SpiceDetect = func(string) bool { return true }
	m.deps.SpiceLogShort = func(string) ([]spice.Branch, error) { return nil, nil }

	if msg := m.stackCmd()(); msg == nil {
		t.Fatal("stackCmd() with a nil guard returned no message")
	}
}

// TestPlainBranchesCmd_RunsWhenStackDetected is the regression guard for the
// leaky "gate lifted" half of the worktrunk-invisibility fix (W2.T2): the
// dashboard form now unions plain local branches into the git-spice arm
// (newNewForm), so the fetch that feeds m.plainBranches must run whether or
// not a stack is detected. It fails if the `if m.stack.Detected { return nil
// }` short-circuit is restored.
func TestPlainBranchesCmd_RunsWhenStackDetected(t *testing.T) {
	m := testModel()
	m.stack = stackState{Detected: true, Branches: []spice.Branch{{Name: "main"}}}

	called := false
	m.deps.PlainBranches = func(dir string) ([]string, error) {
		called = true
		if dir != "/repo" {
			t.Fatalf("PlainBranches got dir %q, want /repo", dir)
		}
		return []string{"main", "feat-a"}, nil
	}
	m.deps.PlainTrunk = func(string) (string, error) { return "main", nil }

	cmd := m.plainBranchesCmd()
	if cmd == nil {
		t.Fatal("plainBranchesCmd() = nil while a stack is detected, want a non-nil Cmd")
	}
	msg, ok := cmd().(plainBranchesLoadedMsg)
	if !ok {
		t.Fatalf("plainBranchesCmd() returned %#v, want plainBranchesLoadedMsg", msg)
	}
	if !called {
		t.Fatal("Deps.PlainBranches was never called while a stack is detected")
	}
}

func TestUpdateFoldsStackLoadedMsg(t *testing.T) {
	m := testModel()
	got, cmd := update(t, m, stackLoadedMsg{stack: stackState{Detected: true}})
	if cmd != nil {
		t.Fatalf("folding stackLoadedMsg should not itself return a cmd")
	}
	if !got.stack.Detected {
		t.Fatalf("Model.stack not updated from stackLoadedMsg")
	}
}

// --- View smoke: stack + diff panels rendered together ---

func TestViewIncludesStackAndDiffPanels(t *testing.T) {
	branches := loadStackFixture(t)
	s := sess("feat-a")
	s.Branch = "feat-a"
	s.BaseBranch = "main"
	s.DiffStats = DiffStats{Added: 4, Removed: 1, Files: 1}

	m := testModel(s)
	m.stack = stackState{Detected: true, Branches: branches}

	// The Overview summarises both: the branch's place in the stack as one
	// path, and the diff as one fact line.
	out := render(m)
	for _, want := range []string{"Stack", "main › feat-a", "+4/-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("Overview missing %q:\n%s", want, out)
		}
	}

	// The Stack tab has the whole tree, siblings and owners included.
	m = focusOverview(t, m)
	m.status = "" // the focus toast covers the right pane's top rows
	m, _ = update(t, m, keyRune('3'))
	out = render(m)
	for _, want := range []string{"main", "feat-b", "feat-c", "[feat-a]"} {
		if !strings.Contains(out, want) {
			t.Errorf("Stack tab missing %q:\n%s", want, out)
		}
	}

	// The Diff tab has diffPanel's full line.
	m, _ = update(t, m, keyRune('5'))
	out = render(m)
	if want := "vs main: +4/-1 across 1 file(s)"; !strings.Contains(out, want) {
		t.Errorf("Diff tab missing %q:\n%s", want, out)
	}
}

// TestViewHidesStackPanelWhenNotDetected: without git-spice there is no Stack
// tab in the strip and no stack line on the Overview — never a placeholder.
func TestViewHidesStackPanelWhenNotDetected(t *testing.T) {
	m := testModel(sess("a"))
	m.stack = stackState{Detected: false}
	out := render(m)
	if strings.Contains(out, "Stack") {
		t.Fatalf("View() should show no Stack tab or stack line when git-spice is not detected:\n%s", out)
	}
}

// --- small local helpers ---

func indexContaining(t *testing.T, lines []string, substr string) int {
	t.Helper()
	for i, l := range lines {
		if strings.Contains(l, substr) {
			return i
		}
	}
	t.Fatalf("no line contains %q in %v", substr, lines)
	return -1
}

func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}
