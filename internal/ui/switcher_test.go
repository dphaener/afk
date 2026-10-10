package ui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
)

func keySlash() tea.KeyMsg  { return tea.KeyPressMsg{Code: '/', Text: "/"} }
func keyEscMsg() tea.KeyMsg { return tea.KeyPressMsg{Code: tea.KeyEsc} }

func testSwitcherProjects() []project.Project {
	return []project.Project{
		{Path: "/repos/alpha", Name: "alpha"},
		{Path: "/repos/beta", Name: "beta"},
		{Path: "/repos/gamma", Name: "gamma"},
	}
}

// switcherSortProject is a shorthand constructor for the ordering tests
// below, which each need a handful of ad hoc projects rather than the fixed
// alphabetical trio testSwitcherProjects returns.
func switcherSortProject(path, name string) project.Project {
	return project.Project{Path: path, Name: name}
}

// switcherSortPaths extracts the path order out of a sorted project slice so
// each test case below can assert against a plain []string.
func switcherSortPaths(projects []project.Project) []string {
	paths := make([]string, len(projects))
	for i, p := range projects {
		paths[i] = p.Path
	}
	return paths
}

// TestSortSwitcherProjects pins the comparator's four ordering keys, tier by
// tier: awaiting-input, then descending open count, then case-folded name,
// then path. testSwitcherProjects (above) is already alphabetical top to
// bottom, so it cannot catch a broken tie-break on its own — a comparator
// that silently ignored every key but the last would still pass against it.
// Each case below isolates one boundary instead, and states in its name
// which rows are load-bearing and why.
func TestSortSwitcherProjects(t *testing.T) {
	tests := []struct {
		name     string
		projects []project.Project
		sessions map[string][]discover.SessionState
		counts   map[string]int
		want     []string
	}{
		{
			// zulu has the higher count but nothing awaiting; alpha has the
			// lower count but IS awaiting. The awaiting tier must win
			// regardless of count, or alpha would sort last instead of first.
			name: "awaiting-input project lifted above a higher-count project",
			projects: []project.Project{
				switcherSortProject("/repos/zulu", "zulu"),
				switcherSortProject("/repos/alpha", "alpha"),
			},
			sessions: map[string][]discover.SessionState{
				"/repos/alpha": {{AwaitingInput: true}},
			},
			counts: map[string]int{"/repos/zulu": 5, "/repos/alpha": 1},
			want:   []string{"/repos/alpha", "/repos/zulu"},
		},
		{
			// Both projects are awaiting, so the first tier ties. The
			// comparator must keep comparing into the count tier — mike (3)
			// above alpha (1) — rather than stopping once both are awaiting
			// and leaving the pair in input order.
			name: "two awaiting projects ordered by count between themselves",
			projects: []project.Project{
				switcherSortProject("/repos/alpha", "alpha"),
				switcherSortProject("/repos/mike", "mike"),
			},
			sessions: map[string][]discover.SessionState{
				"/repos/alpha": {{AwaitingInput: true}},
				"/repos/mike":  {{AwaitingInput: true}},
			},
			counts: map[string]int{"/repos/alpha": 1, "/repos/mike": 3},
			want:   []string{"/repos/mike", "/repos/alpha"},
		},
		{
			// "Api" and "api" have equal counts and differ only by case; a
			// third project, "Banana", sorts between them under a raw
			// byte-wise comparison ('A' < 'B' < 'a') but must not under
			// case-folded comparison, which is the whole point of folding
			// before comparing rather than after.
			name: "equal counts fall back to case-folded name, keeping differently-cased names adjacent",
			projects: []project.Project{
				switcherSortProject("/repos/api-upper", "Api"),
				switcherSortProject("/repos/banana", "Banana"),
				switcherSortProject("/repos/api-lower", "api"),
			},
			counts: map[string]int{"/repos/api-upper": 1, "/repos/banana": 1, "/repos/api-lower": 1},
			want:   []string{"/repos/api-lower", "/repos/api-upper", "/repos/banana"},
		},
		{
			// Same display name, same count, no awaiting session: every key
			// but the last ties, so the path is what keeps the order
			// deterministic instead of depending on sort.Slice's (documented
			// non-stable) internal partitioning.
			name: "identical display names broken by path",
			projects: []project.Project{
				switcherSortProject("/repos/dup-b", "dup"),
				switcherSortProject("/repos/dup-a", "dup"),
			},
			want: []string{"/repos/dup-a", "/repos/dup-b"},
		},
		{
			// oscar has no entry in counts at all; november has an explicit
			// zero. Both must rank identically — behind papa's count of 1 —
			// or a map lookup miss would be silently treated as something
			// other than zero.
			name: "a project absent from the counts map is treated as zero",
			projects: []project.Project{
				switcherSortProject("/repos/papa", "papa"),
				switcherSortProject("/repos/oscar", "oscar"),
				switcherSortProject("/repos/november", "november"),
			},
			counts: map[string]int{"/repos/papa": 1, "/repos/november": 0},
			want:   []string{"/repos/papa", "/repos/november", "/repos/oscar"},
		},
		{
			// No sessions and no counts at all: every project ties down to
			// name, so the result is pure alphabetical order — the pre-sort
			// behavior this task replaces, still reachable as the degenerate
			// case rather than a special path.
			name:     "an empty counts map yields pure alphabetical order",
			projects: testSwitcherProjects(),
			counts:   map[string]int{},
			want:     []string{"/repos/alpha", "/repos/beta", "/repos/gamma"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := switcherSortPaths(sortSwitcherProjects(tt.projects, tt.sessions, tt.counts))
			if len(got) != len(tt.want) {
				t.Fatalf("order = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("order = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestSortSwitcherProjectsDoesNotMutateInput guards the copy-not-mutate
// contract sortSwitcherProjects documents: the parent (model.go) hands this
// component m.projects directly, and sameProjectPaths compares that slice by
// position on every scan tick to decide whether to rebuild the overlay — an
// in-place sort here would corrupt that comparison from inside a component
// the gate's own code never calls into.
func TestSortSwitcherProjectsDoesNotMutateInput(t *testing.T) {
	original := testSwitcherProjects()
	input := testSwitcherProjects()

	sortSwitcherProjects(input, nil, map[string]int{"/repos/gamma": 5})

	for i := range input {
		if input[i] != original[i] {
			t.Fatalf("input[%d] = %+v, want unmutated %+v", i, input[i], original[i])
		}
	}
}

// TestNewSwitcherRendersInSortedOrder confirms the wiring, not just the pure
// comparator: newSwitcher must actually call sortSwitcherProjects on its way
// to building rows, or a correct comparator sitting unused would still leave
// the overlay showing scan order. gamma is given the awaiting session and
// the highest count while being handed to newSwitcher LAST, so an
// unsorted-passthrough regression (rendering projects in the order given)
// would place it last in the list too.
func TestNewSwitcherRendersInSortedOrder(t *testing.T) {
	projects := []project.Project{
		{Path: "/repos/alpha", Name: "alpha"},
		{Path: "/repos/beta", Name: "beta"},
		{Path: "/repos/gamma", Name: "gamma"},
	}
	sessions := map[string][]discover.SessionState{
		"/repos/gamma": {{AwaitingInput: true}},
	}
	counts := map[string]int{"/repos/gamma": 9}

	sw := newSwitcher(projects, sessions, counts, 60, 10)

	items := sw.list.Items()
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	it, ok := items[0].(switcherItem)
	if !ok || it.path != "/repos/gamma" {
		t.Fatalf("first item = %+v, want gamma (awaiting input, highest count)", items[0])
	}
}

// TestSwitcherRows pins the row contract from the plan's acceptance
// criteria: name, session count, and an awaiting-input dot appear exactly
// for projects that have one. "beta" is the load-bearing row — it has
// sessions but none awaiting, so it must show the count without the dot;
// without a row like it the test could pass by accident (e.g. by always
// drawing the dot whenever count > 0).
func TestSwitcherRows(t *testing.T) {
	sessions := map[string][]discover.SessionState{
		"/repos/alpha": {{AwaitingInput: true}},
		"/repos/beta":  {{}, {}},
		// gamma has no entry at all: zero sessions, no dot.
	}
	sw := newSwitcher(testSwitcherProjects(), sessions, nil, 60, 10)
	out := sw.View()
	lines := strings.Split(out, "\n")

	find := func(name string) string {
		for _, l := range lines {
			if strings.Contains(l, name) {
				return l
			}
		}
		t.Fatalf("no rendered line contains %q; full output:\n%s", name, out)
		return ""
	}

	alpha := find("alpha")
	if !strings.Contains(alpha, "1 session") {
		t.Errorf("alpha row = %q, want count %q", alpha, "1 session")
	}
	if !strings.Contains(alpha, "●") {
		t.Errorf("alpha row = %q, want awaiting-input dot", alpha)
	}

	beta := find("beta")
	if !strings.Contains(beta, "2 sessions") {
		t.Errorf("beta row = %q, want count %q", beta, "2 sessions")
	}
	if strings.Contains(beta, "●") {
		t.Errorf("beta row = %q, want no dot (no session awaiting input)", beta)
	}

	gamma := find("gamma")
	if !strings.Contains(gamma, "0 sessions") {
		t.Errorf("gamma row = %q, want count %q", gamma, "0 sessions")
	}
	if strings.Contains(gamma, "●") {
		t.Errorf("gamma row = %q, want no dot (no sessions at all)", gamma)
	}
}

// TestSwitcherCursorWraps pins the wrapping behavior: the switcher sets
// InfiniteScrolling on bubbles' list.Model, so ↑ from the first project
// wraps to the last and ↓ from the last wraps to the first (matching the
// session list's behavior for consistency). The load-bearing assertion is
// the post-overshoot index, not the intermediate ones. The switcher
// previously clamped (bubbles' default); this test replaced
// TestSwitcherCursorClamps when the switcher switched to wrap.
func TestSwitcherCursorWraps(t *testing.T) {
	tests := []struct {
		name  string
		keys  []tea.KeyMsg
		start int
		want  int
	}{
		{"down four times from 0 wraps to 1", []tea.KeyMsg{keyDown, keyDown, keyDown, keyDown}, 0, 1},
		{"up from 0 wraps to 2", []tea.KeyMsg{keyUp}, 0, 2},
		{"up twice from 0 wraps to 1", []tea.KeyMsg{keyUp, keyUp}, 0, 1},
		{"down from 2 wraps to 0", []tea.KeyMsg{keyDown}, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sw := newSwitcher(testSwitcherProjects(), nil, nil, 60, 10)
			sw.list.Select(tt.start)
			for _, k := range tt.keys {
				sw.Update(k)
			}
			if got := sw.list.Index(); got != tt.want {
				t.Errorf("index = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestSwitcherFilterEscThenDismiss pins the two-level Esc behavior the plan
// requires: the first Esc while a filter is live only clears the filter
// (overlay stays open, result is switcherNone); a second Esc, with no filter
// left, dismisses. Getting the order backwards (dismissing on the first Esc)
// would silently discard a half-typed filter instead of just clearing it.
func TestSwitcherFilterEscThenDismiss(t *testing.T) {
	sw := newSwitcher(testSwitcherProjects(), nil, nil, 60, 10)

	if _, res := sw.Update(keySlash()); res.Kind != switcherNone {
		t.Fatalf("'/' result = %v, want switcherNone", res.Kind)
	}
	if sw.list.FilterState() != list.Filtering {
		t.Fatalf("FilterState = %v, want Filtering", sw.list.FilterState())
	}
	sw.Update(keyRune('a'))

	_, res := sw.Update(keyEscMsg())
	if res.Kind != switcherNone {
		t.Fatalf("first Esc result = %v, want switcherNone (filter cleared, overlay stays open)", res.Kind)
	}
	if sw.list.FilterState() != list.Unfiltered {
		t.Fatalf("FilterState after first Esc = %v, want Unfiltered", sw.list.FilterState())
	}

	_, res = sw.Update(keyEscMsg())
	if res.Kind != switcherDismissed {
		t.Fatalf("second Esc result = %v, want switcherDismissed", res.Kind)
	}
}

// TestSwitcherEnterChoosesPath moves the cursor to a known row and asserts
// Enter reports that row's path, not just "something was chosen" — pins the
// path/name pairing so a delegate bug that renders the right name next to
// the wrong path would be caught.
func TestSwitcherEnterChoosesPath(t *testing.T) {
	sw := newSwitcher(testSwitcherProjects(), nil, nil, 60, 10)
	sw.Update(keyDown)

	_, res := sw.Update(keyEnter)
	if res.Kind != switcherChosen {
		t.Fatalf("Enter result = %v, want switcherChosen", res.Kind)
	}
	if res.Path != "/repos/beta" {
		t.Errorf("Enter path = %q, want %q", res.Path, "/repos/beta")
	}
}

// TestSwitcherRescanKey pins `R`'s two behaviors depending on filter state:
// unfiltered, it's the rescan shortcut; while actively typing a filter
// query, it's ordinary text. Both rows are load-bearing — either one
// missing would let a regression silently swap the two behaviors (e.g.
// eating every `R` keystroke as rescan, which would make the letter
// untypeable in a project name filter).
func TestSwitcherRescanKey(t *testing.T) {
	t.Run("unfiltered R requests a rescan", func(t *testing.T) {
		sw := newSwitcher(testSwitcherProjects(), nil, nil, 60, 10)
		_, res := sw.Update(keyRune('R'))
		if res.Kind != switcherRescan {
			t.Fatalf("result = %v, want switcherRescan", res.Kind)
		}
	})

	t.Run("R while filtering is typed into the query, not a rescan", func(t *testing.T) {
		sw := newSwitcher(testSwitcherProjects(), nil, nil, 60, 10)
		sw.Update(keySlash())
		_, res := sw.Update(keyRune('R'))
		if res.Kind != switcherNone {
			t.Fatalf("result = %v, want switcherNone", res.Kind)
		}
		if got := sw.list.FilterInput.Value(); got != "R" {
			t.Errorf("filter input = %q, want %q", got, "R")
		}
	})
}

// TestSwitcherRenderNarrowWidthDoesNotPanic exercises the delegate's width
// arithmetic at sizes small enough to go negative if unguarded (0 and 1) —
// an unrecovered panic here fails the test process itself, which is the
// point: no explicit recover is needed to prove the "does not panic"
// acceptance criterion.
func TestSwitcherRenderNarrowWidthDoesNotPanic(t *testing.T) {
	sessions := map[string][]discover.SessionState{
		"/repos/alpha": {{AwaitingInput: true}},
	}
	widths := []int{0, 1, 2}
	for _, w := range widths {
		sw := newSwitcher(testSwitcherProjects(), sessions, nil, w, 5)
		_ = sw.View()
	}
}

// TestSwitcherFilterValueUsesDisplayName confirms filtering narrows by the
// rendered name (not the path, which would let an unrelated parent
// directory segment match) — typing a query that is a substring of exactly
// one project's name should leave only that project selectable.
func TestSwitcherFilterValueUsesDisplayName(t *testing.T) {
	sw := newSwitcher(testSwitcherProjects(), nil, nil, 60, 10)
	// list.Model batches filterItems (synchronous, no I/O) together with the
	// filter input's cursor blink (a perpetual tea.Tick that would recurse
	// forever if driven the same way) — so resolve unwraps the batch and
	// feeds only the FilterMatchesMsg back, exactly what a real bubbletea
	// run loop delivers to Update, while leaving the blink Cmd unexecuted
	// exactly as a test harness with no runtime clock naturally would.
	resolve := func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if fm, ok := msg.(list.FilterMatchesMsg); ok {
			sw.Update(fm)
			return
		}
		batch, ok := msg.(tea.BatchMsg)
		if !ok {
			return
		}
		for _, c := range batch {
			if c == nil {
				continue
			}
			if fm, ok := c().(list.FilterMatchesMsg); ok {
				sw.Update(fm)
			}
		}
	}

	cmd, _ := sw.Update(keySlash())
	resolve(cmd)
	for _, r := range "gam" {
		cmd, _ = sw.Update(keyRune(r))
		resolve(cmd)
	}
	sw.Update(keyEnter)

	if sw.list.FilterState() != list.FilterApplied {
		t.Fatalf("FilterState = %v, want FilterApplied", sw.list.FilterState())
	}
	if got := len(sw.list.VisibleItems()); got != 1 {
		t.Fatalf("visible items = %d, want 1", got)
	}
	it, ok := sw.list.VisibleItems()[0].(switcherItem)
	if !ok || it.path != "/repos/gamma" {
		t.Fatalf("visible item = %+v, want gamma", sw.list.VisibleItems()[0])
	}
}

// TestSwitcherListWidthMatchesRenderedBoxUsableWidth is the arithmetic-honesty
// test for switcherListSize, in the style of
// TestSidebarRenderedWidthMatchesRenderedList (mouse_test.go) and
// TestPaneFullWidthContentDoesNotWrap (ui_test.go): it renders the overlay
// with a row padded out to EXACTLY switcherListSize's reported width (the
// same padding switcher.go's Render applies to a real row) and requires the
// row's text to survive as one contiguous, unbroken line at the width
// renderSwitcher's box actually renders at.
//
// The name is sized so "name  0 sessions" stays one column UNDER listW —
// short enough that switcherDelegate's own truncate (a second, unrelated
// mechanism that also shortens an over-length line before the box ever sees
// it) never fires — and Render's Width(listW) padding then brings the row up
// to exactly listW itself, the same way a real selected row reaches full
// width. A row split by the box's own wrap breaks this exact substring, no
// matter which line it lands on; comparing rendered line counts does not
// work here (verified against the pre-fix arithmetic during planning), because
// switcherListSize's height leaves far more blank filler rows than the
// wrapped line needs, so a wrap changes nothing about the total line count.
//
// Before this fix, switcherListSize reported mainOuter-4 while renderSwitcher
// handed sizedBox that same mainOuter-4 as its content-plus-padding argument —
// which put the box's real wrap point 2 columns short of it (mainOuter-6). A
// row padded to the list's reported width therefore always wrapped, and
// because the SELECTED row is the one padded out to that width
// (switcher.go's Render), the wrap tracked selection rather than name length
// or terminal width — exactly the baseline observation in issues.md.
//
// The comparison strips ANSI first (xansi.Strip, as render() does in
// render_test.go): the count suffix is styled separately from the name
// (theme.dim.Render(count), switcher.go), so the raw output has an escape
// sequence sitting between them even when the row does not wrap at all.
func TestSwitcherListWidthMatchesRenderedBoxUsableWidth(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := testModel(sess("a"))
			m.width = width

			listW, listH := m.switcherListSize()
			sessions := map[string][]discover.SessionState{"/repos/x": nil}

			const suffix = "  0 sessions"
			nameLen := listW - len(suffix) - 1
			if nameLen < 1 {
				nameLen = 1
			}
			name := strings.Repeat("x", nameLen)
			full := []project.Project{{Path: "/repos/x", Name: name}}
			m.switcher = newSwitcher(full, sessions, nil, listW, listH)

			// The expected line is the delegate's own rendering — the same
			// row the box will draw — so the assertion is only about the box.
			var row strings.Builder
			switcherDelegate{}.Render(&row, m.switcher.list, 0, m.switcher.list.Items()[0])
			wantLine := xansi.Strip(row.String())
			if got := lipgloss.Width(wantLine); got != listW {
				t.Fatalf("test premise: the row is %d cells, want the list width %d", got, listW)
			}

			out := xansi.Strip(m.renderSwitcherModal())
			if !strings.Contains(out, wantLine) {
				t.Fatalf("full-width row wrapped inside the overlay box (listW=%d):\n%s", listW, out)
			}
			if got := lipgloss.Width(out); got != listW+4 {
				t.Fatalf("switcher box is %d cells wide, want the list width plus border and padding (%d)", got, listW+4)
			}
		})
	}
}

// TestSwitcherRowsRenderAtUniformWidth pins the second half of the fix
// directly at the delegate: the highlighted row and every other row render at
// the same width. Before this fix only the selected row was padded via
// theme.rowSelected.Width(width) — every other row was left at its natural
// length via fmt.Fprint — so the highlight bar was the only row ever as wide
// as the box's wrap point, which is the mechanism behind the "wraps only when
// selected" baseline observation in issues.md.
//
// This calls switcherDelegate.Render directly for each row rather than
// reading widths back out of sw.View(): list.Model.View() joins rows with
// lipgloss.JoinVertical, which pads every line SHORTER than the widest one
// out to match it regardless of what the delegate did — so a view-level
// comparison would pass even with the unselected branch's Width() call
// removed entirely (caught only by mutating it out and rerunning with
// -count=1, matching this plan's other vacuous-test findings).
func TestSwitcherRowsRenderAtUniformWidth(t *testing.T) {
	sw := newSwitcher(testSwitcherProjects(), nil, nil, 40, 10)
	d := switcherDelegate{}

	var selected, unselected strings.Builder
	d.Render(&selected, sw.list, 0, sw.list.Items()[0])   // index 0 is selected by default
	d.Render(&unselected, sw.list, 1, sw.list.Items()[1]) // any other index is not

	got, want := lipgloss.Width(unselected.String()), lipgloss.Width(selected.String())
	if got != want {
		t.Errorf("unselected row width = %d, want %d (same as the selected row)", got, want)
	}
}

// TestSwitcherFilterNarrowsThroughModelUpdate pins the Model-dispatch bug
// found during W5.T1: switcher.Update's own filterItems Cmd produces a
// list.FilterMatchesMsg that has to travel back through Model.Update, and
// until this fix Update's non-key dispatch had no inputSwitcher arm — the
// message was silently dropped while the query text and prompt kept
// rendering (list.go draws those straight from FilterInput/filterState),
// which is what made the missing narrowing read as working.
//
// TestSwitcherFilterValueUsesDisplayName (above) does not catch this: it
// feeds the FilterMatchesMsg straight to sw.Update, bypassing Model.Update's
// dispatch layer entirely.
func TestSwitcherFilterNarrowsThroughModelUpdate(t *testing.T) {
	m := testModel(sess("a"))
	m.projects = testSwitcherProjects()
	m = openSwitcherOverlay(t, m)
	if got := len(m.switcher.list.VisibleItems()); got != len(testSwitcherProjects()) {
		t.Fatalf("precondition: overlay should start with %d visible items, got %d",
			len(testSwitcherProjects()), got)
	}

	// Drive one key through the real dispatch and resolve whatever Cmd it
	// returns back into Update, mirroring what a real bubbletea run loop does
	// (and what TestSwitcherFilterValueUsesDisplayName's own resolve helper
	// does for the component in isolation).
	drive := func(msg tea.Msg) {
		var cmd tea.Cmd
		m, cmd = update(t, m, msg)
		for _, resolved := range collectMsgs(t, cmd) {
			m, _ = update(t, m, resolved)
		}
	}

	drive(keySlash())
	for _, r := range "gam" {
		drive(keyRune(r))
	}

	if got := len(m.switcher.list.VisibleItems()); got != 1 {
		t.Errorf("visible items after filtering to %q = %d, want 1", "gam", got)
	}
	it, ok := m.switcher.list.VisibleItems()[0].(switcherItem)
	if !ok || it.path != "/repos/gamma" {
		t.Errorf("visible item = %+v, want gamma", m.switcher.list.VisibleItems()[0])
	}
}

// TestSwitcherSelectedRowHighlightSpansTheWholeRow is the switcher's copy of
// the session list's TestSelectedRowHighlightSpansTheWholeRow, and it guards a
// defect the switcher had for real: the row's dim session count closes with an
// SGR reset, which dropped the enclosing selection background, so the bar ran
// out at the end of "1 session" and the awaiting dot after it — the row's whole
// point — sat outside the highlight.
//
// "alpha" (awaiting) and "beta" (sessions, no dot) are both exercised because
// the two right-hand tails differ: the dot in one, two plain spaces in the
// other. Only the first proves the dot is covered; only the second proves the
// bar reaches the row's last cell when there is nothing styled to reach it.
func TestSwitcherSelectedRowHighlightSpansTheWholeRow(t *testing.T) {
	const width = 40
	sessions := map[string][]discover.SessionState{
		"/repos/alpha": {{AwaitingInput: true}},
		"/repos/beta":  {{}, {}},
	}
	for i, name := range []string{"alpha", "beta"} {
		t.Run(name, func(t *testing.T) {
			sw := newSwitcher(testSwitcherProjects(), sessions, nil, width, 10)
			selectListIndex(&sw.list, i)

			var b strings.Builder
			switcherDelegate{}.Render(&b, sw.list, i, sw.list.Items()[i])
			assertHighlightSpansRow(t, b.String(), width)
		})
	}
}

// TestSwitcherUnselectedRowCarriesNoHighlight is the other half: spanStyle is
// on the selected branch only, so every other row must stay unpainted. Without
// it, moving the re-assertion up into the row composition would highlight the
// whole list and still satisfy the test above.
func TestSwitcherUnselectedRowCarriesNoHighlight(t *testing.T) {
	sw := newSwitcher(testSwitcherProjects(), map[string][]discover.SessionState{
		"/repos/beta": {{AwaitingInput: true}},
	}, nil, 40, 10)

	var b strings.Builder
	switcherDelegate{}.Render(&b, sw.list, 1, sw.list.Items()[1]) // index 0 is the selected one
	if strings.Contains(b.String(), "48;2;") {
		t.Errorf("unselected switcher row carries a background: %q", b.String())
	}
}
