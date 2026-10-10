package ui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/dphaener/afk/internal/session"
)

// normalSpec is fetched the way production fetches it, so no assertion below
// can drift onto a hand-built table.
func normalSpec() modeSpec {
	reg := newModeRegistry(defaultKeyMap())
	return reg.spec(inputNormal)
}

func entryDescs(items []paletteItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.desc
	}
	return out
}

func hasDesc(items []paletteItem, desc string) bool {
	for _, it := range items {
		if it.desc == desc {
			return true
		}
	}
	return false
}

// TestPaletteEntriesAreDerivedFromTheModeRules is THE test for this feature:
// the palette's contents are a function of mode.go's dispatch table and of
// nothing else, so the two cannot drift the way the retired hand-written help
// ladder drifted from the keys it described.
//
// Every subtest is load-bearing, and each one fails if the derivation is
// replaced by a hand-maintained list:
//
//   - "every unguarded rule appears" pins the forward direction, including the
//     count, so an entry the rules do not justify is a failure too.
//   - "a rule added to the mode appears with no palette-side edit" is the
//     property the plan asked for stated directly: the added binding exists
//     only in this test's local slice, and the palette finds it anyway.
//   - "a disabled binding vanishes" is the reverse. It is not cosmetic:
//     key.Matches refuses a disabled binding, so such a row would be an action
//     the palette offers and the dispatch declines to perform.
func TestPaletteEntriesAreDerivedFromTheModeRules(t *testing.T) {
	spec := normalSpec()

	t.Run("every unguarded rule appears exactly once, named by its own help text", func(t *testing.T) {
		got := paletteEntries(spec, routeConds{}, nil)

		want := 0
		for _, rule := range spec.rules {
			if rule.needsPane || rule.needsFilter || rule.needsDetail {
				continue
			}
			want++
			h := rule.binding.Help()
			var found *paletteItem
			for i := range got {
				if got[i].desc == h.Desc && got[i].keyLabel == h.Key {
					found = &got[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("rule %v (%q/%q) has no palette entry; entries = %q",
					rule.action, h.Key, h.Desc, entryDescs(got))
			}
			if !key.Matches(found.press, rule.binding) {
				t.Fatalf("entry %q re-presses %q, which its own binding does not match",
					h.Desc, found.press.String())
			}
		}
		if len(got) != want {
			t.Fatalf("palette has %d entries for %d runnable rules: %q", len(got), want, entryDescs(got))
		}
	})

	t.Run("a rule added to the mode appears with no palette-side edit", func(t *testing.T) {
		added := key.NewBinding(key.WithKeys("Z"), key.WithHelp("Z", "teleport somewhere"))
		grown := spec
		grown.rules = append(append([]modeRule(nil), spec.rules...),
			modeRule{binding: added, action: actionNone})

		got := paletteEntries(grown, routeConds{}, nil)
		if !hasDesc(got, "teleport somewhere") {
			t.Fatalf("a new rule did not surface in the palette; entries = %q", entryDescs(got))
		}
		if len(got) != len(paletteEntries(spec, routeConds{}, nil))+1 {
			t.Fatalf("adding one rule changed the entry count by something other than one")
		}
	})

	t.Run("a disabled binding vanishes", func(t *testing.T) {
		shrunk := spec
		shrunk.rules = append([]modeRule(nil), spec.rules...)
		var target key.Binding
		for i := range shrunk.rules {
			if shrunk.rules[i].action == actionNewSession {
				shrunk.rules[i].binding.SetEnabled(false)
				target = shrunk.rules[i].binding
			}
		}
		if target.Keys() == nil {
			t.Fatal("fixture broken: inputNormal has no new-session rule")
		}

		got := paletteEntries(shrunk, routeConds{}, nil)
		if hasDesc(got, target.Help().Desc) {
			t.Fatalf("a disabled binding is still offered; entries = %q", entryDescs(got))
		}
		if len(got) != len(paletteEntries(spec, routeConds{}, nil))-1 {
			t.Fatalf("disabling one binding removed something other than one entry")
		}
	})
}

// TestPaletteEntriesHonorRuleGuards pins that the palette evaluates a rule's
// guard from the same routeConds the dispatch does. Both rows are actions that
// are genuinely absent — not merely inert — when their condition fails, so
// offering them unconditionally would be offering a keypress that route()
// declines to claim.
func TestPaletteEntriesHonorRuleGuards(t *testing.T) {
	spec := normalSpec()

	cases := []struct {
		name  string
		conds routeConds
		desc  string
		want  bool
	}{
		{"close pane needs a pane", routeConds{}, "close pane", false},
		{"close pane with a pane", routeConds{paneOpen: true}, "close pane", true},
		{"clear filter needs a filter", routeConds{}, "clear filter", false},
		{"clear filter with one applied", routeConds{listFiltered: true}, "clear filter", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paletteEntries(spec, tc.conds, nil)
			if hasDesc(got, tc.desc) != tc.want {
				t.Fatalf("%q present = %v, want %v; entries = %q",
					tc.desc, !tc.want, tc.want, entryDescs(got))
			}
		})
	}
}

// TestPaletteEntriesHonorTheLiveStateGate pins the second filter: the caller's
// enabled predicate, which in production is Model.normalBindingEnabled — the
// same function `?`'s full help uses. The palette and full help must never
// advertise different sets, and this is the seam where they could.
func TestPaletteEntriesHonorTheLiveStateGate(t *testing.T) {
	spec := normalSpec()
	keys := defaultKeyMap()

	blockNew := func(b key.Binding) bool {
		if b.Help().Desc == keys.New.Help().Desc {
			return false
		}
		return b.Enabled()
	}
	got := paletteEntries(spec, routeConds{}, blockNew)
	if hasDesc(got, keys.New.Help().Desc) {
		t.Fatalf("the live-state gate did not remove %q; entries = %q",
			keys.New.Help().Desc, entryDescs(got))
	}

	all := paletteEntries(spec, routeConds{}, nil)
	if len(got) != len(all)-1 {
		t.Fatalf("gating one binding changed the entry count by %d, want 1", len(all)-len(got))
	}
}

func TestPaletteOmitsToggleDuringAnySameNameClean(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m.cleaningSessions = map[cleanID]bool{
		{name: "alpha", kind: session.KindStatusWithoutWorktree}: true,
	}
	reg := m.modes()
	entries := paletteEntries(reg.spec(inputNormal), m.routeConditions(), m.normalBindingEnabled)
	if hasDesc(entries, m.keys.Toggle.Help().Desc) {
		t.Fatalf("palette offered Ctrl-T during a same-name clean: %q", entryDescs(entries))
	}
}

// TestPaletteHasOneRunnableCombinedFocusEntry pins the registry-derived
// contract at the palette boundary. Enter and f are aliases on one binding, so
// the palette must render one row with the combined label and re-press its
// canonical physical key into the same current-view focus action.
func TestPaletteHasOneRunnableCombinedFocusEntry(t *testing.T) {
	m := testModel(runningSess("alpha"))
	reg := m.modes()
	entries := paletteEntries(reg.spec(inputNormal), m.routeConditions(), m.normalBindingEnabled)
	focus := m.keys.Focus.Help()

	var found *paletteItem
	for i := range entries {
		if entries[i].desc != focus.Desc {
			continue
		}
		if found != nil {
			t.Fatalf("palette contains focus view more than once: %q", entryDescs(entries))
		}
		found = &entries[i]
	}
	if found == nil {
		t.Fatalf("palette omitted the combined focus entry %q: %q", focus.Desc, entryDescs(entries))
	}
	if found.keyLabel != focus.Key {
		t.Errorf("focus row key = %q, want %q", found.keyLabel, focus.Key)
	}
	if got := reg.route(inputNormal, m.routeConditions(), found.press); got.kind != routeAction || got.action != actionFocusPane {
		t.Errorf("focus row press routes to %+v, want actionFocusPane", got)
	}
}

// TestEveryNormalRuleIsRunnableFromThePalette is the "does exactly what the key
// does" guarantee, proven end to end at the routing layer: re-pressing each
// entry's synthesized key routes back to the SAME action the rule declares.
//
// It is what makes the palette safe to extend. A future binding whose key this
// package cannot synthesize would be silently dropped from the palette by
// paletteEntries (fail-closed by design), and this test is what turns that
// silence into a red build.
func TestEveryNormalRuleIsRunnableFromThePalette(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	spec := reg.spec(inputNormal)
	conds := routeConds{paneOpen: true, listFiltered: true, detailShown: true}

	for _, rule := range spec.rules {
		keys := rule.binding.Keys()
		if len(keys) == 0 {
			t.Fatalf("rule %v has no keys", rule.action)
		}
		press, ok := paletteKeyPress(keys[0])
		if !ok {
			t.Fatalf("rule %v's key %q cannot be synthesized, so the palette will silently drop it",
				rule.action, keys[0])
		}
		dec := reg.route(inputNormal, conds, press)
		if dec.kind != routeAction || dec.action != rule.action {
			t.Fatalf("re-pressing %q routes to %v/%v, want action %v",
				keys[0], dec.kind, dec.action, rule.action)
		}
	}
}

// TestPaletteKeyPress pins the synthesizer's round-trip contract. The failure
// rows matter as much as the successes: a key string this function cannot
// reproduce must report false rather than return some other key, because the
// consequence of a wrong answer is the palette running an action the user did
// not choose.
func TestPaletteKeyPress(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"printable rune", "q", true},
		{"punctuation", "/", true},
		{"the palette's own key", ":", true},
		{"uppercase rune", "R", true},
		{"ctrl chord", "ctrl+c", true},
		{"ctrl chord on punctuation", "ctrl+\\", true},
		{"the resize chord", "ctrl+w", true},
		{"named key", "enter", true},
		{"escape", "esc", true},
		{"paging key", "pgdown", true},
		{"jump key", "home", true},
		{"shift chord on a named key", "shift+tab", true},
		{"empty", "", false},
		// These two parse cleanly and are rejected only by the round-trip: a
		// bare space is printed as "space", and Keystroke always emits
		// modifiers in canonical ctrl/alt/shift order. Without the round-trip
		// both would return a key that names something else.
		{"a bare space", " ", false},
		{"modifiers out of canonical order", "alt+ctrl+a", false},
		{"unknown name", "f13", false},
		{"unhandled modifier", "hyper+a", false},
		{"dangling modifier", "ctrl+", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := paletteKeyPress(tc.in)
			if ok != tc.ok {
				t.Fatalf("paletteKeyPress(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if !ok {
				return
			}
			if got.String() != tc.in {
				t.Fatalf("paletteKeyPress(%q).String() = %q", tc.in, got.String())
			}
			if !key.Matches(got, key.NewBinding(key.WithKeys(tc.in))) {
				t.Fatalf("paletteKeyPress(%q) does not match a binding on that key", tc.in)
			}
		})
	}
}

// TestPaletteModePrecedence pins where inputPalette sits. The modal rows are
// reachable (spawnFinishedMsg raises a modal asynchronously, and it is the only
// place a failed create's output can be read); the rest are declared so the
// frame View draws and the surface that owns the keyboard cannot disagree.
func TestPaletteModePrecedence(t *testing.T) {
	cases := []struct {
		name  string
		state modeState
		want  inputMode
	}{
		{"palette alone", modeState{paletteOpen: true}, inputPalette},
		{"clean confirmation over the palette",
			modeState{paletteOpen: true, modal: inputConfirmClean}, inputConfirmClean},
		{"spawn error over the palette",
			modeState{paletteOpen: true, modal: inputSpawnError}, inputSpawnError},
		{"restart confirmation over the palette",
			modeState{paletteOpen: true, modal: inputConfirmRestart}, inputConfirmRestart},
		{"label prompt over the palette",
			modeState{paletteOpen: true, modal: inputEditLabel}, inputEditLabel},
		{"palette over the switcher",
			modeState{paletteOpen: true, switcherOpen: true}, inputPalette},
		{"palette over the form",
			modeState{paletteOpen: true, formOpen: true}, inputPalette},
		{"palette over the filter prompt",
			modeState{paletteOpen: true, listFiltering: true}, inputPalette},
		{"palette over a focused pane",
			modeState{paletteOpen: true, paneFocused: true}, inputPalette},
		{"palette over resize mode",
			modeState{paletteOpen: true, resizing: true}, inputPalette},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := activeInputMode(tc.state); got != tc.want {
				t.Fatalf("activeInputMode(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}

	var m Model
	m.paletteOpen = true
	if got := m.modeSnapshot(); got != (modeState{paletteOpen: true}) {
		t.Fatalf("modeSnapshot() = %+v, want paletteOpen only", got)
	}
}

// TestColonIsClaimedOnlyByNormalMode pins the collision check. `:` is a
// legitimate character everywhere there is text to type, and a legitimate
// keystroke for a hosted child, so exactly one mode may claim it.
func TestColonIsClaimedOnlyByNormalMode(t *testing.T) {
	reg := newModeRegistry(defaultKeyMap())
	colon, ok := paletteKeyPress(":")
	if !ok {
		t.Fatal("`:` must be synthesizable")
	}

	want := map[inputMode]keyDecision{
		inputNormal:         {kind: routeAction, action: actionPalette},
		inputPalette:        {kind: routeComponent},
		inputSwitcher:       {kind: routeComponent},
		inputForm:           {kind: routeComponent},
		inputFilter:         {kind: routeComponent},
		inputEditLabel:      {kind: routeComponent},
		inputPickProvider:   {kind: routeIgnored},
		inputPane:           {kind: routeChild},
		inputDetail:         {kind: routeIgnored},
		inputResize:         {kind: routeIgnored},
		inputConfirmClean:   {kind: routeIgnored},
		inputConfirmRestart: {kind: routeIgnored},
		inputSpawnError:     {kind: routeIgnored},
	}
	for mode := inputMode(0); mode < inputModeCount; mode++ {
		w, listed := want[mode]
		if !listed {
			t.Fatalf("mode %v has no expectation for `:`", mode)
		}
		if got := reg.route(mode, routeConds{}, colon); got != w {
			t.Fatalf("route(%v, `:`) = %+v, want %+v", mode, got, w)
		}
	}
}

// TestPaletteOpensFiltersAndRuns walks the whole gesture the acceptance
// criteria describe against a real Model: `:` opens, typing filters fuzzily,
// Enter runs the selection, and the result is identical to pressing the key.
//
// The equivalence assertion is the point. It is what would fail if the palette
// ever grew its own action dispatch beside applyKeyAction's.
func TestPaletteOpensFiltersAndRuns(t *testing.T) {
	m := testModel(sess("alpha"))

	m, _ = update(t, m, keyRune(':'))
	if !m.paletteOpen {
		t.Fatal("`:` did not open the palette")
	}
	if activeInputMode(m.modeSnapshot()) != inputPalette {
		t.Fatal("the open palette does not own the keyboard")
	}
	all := len(m.palette.list.VisibleItems())
	if all == 0 {
		t.Fatal("the palette opened empty")
	}

	for _, r := range "new" {
		m, _ = update(t, m, keyRune(r))
	}
	visible := m.palette.list.VisibleItems()
	if len(visible) >= all {
		t.Fatalf("typing narrowed nothing: %d of %d rows remain", len(visible), all)
	}
	if it, ok := visible[0].(paletteItem); !ok || it.desc != m.keys.New.Help().Desc {
		t.Fatalf("top match = %v, want %q", visible[0], m.keys.New.Help().Desc)
	}

	ran, _ := update(t, m, keyEnter)
	if ran.paletteOpen {
		t.Fatal("running an action left the palette open")
	}

	pressed, _ := update(t, testModel(sess("alpha")), keyRune('n'))
	if ran.formOpen != pressed.formOpen || !ran.formOpen {
		t.Fatalf("palette run formOpen=%v, direct keypress formOpen=%v; want both true",
			ran.formOpen, pressed.formOpen)
	}
}

// TestPaletteEscClearsThenCloses pins the switcher's two-stage Esc, adapted to
// a surface that is always accepting text: the first press must not throw away
// the palette along with the query.
func TestPaletteEscClearsThenCloses(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, keyRune(':'))
	all := len(m.palette.list.VisibleItems())

	for _, r := range "clea" {
		m, _ = update(t, m, keyRune(r))
	}
	if len(m.palette.list.VisibleItems()) >= all {
		t.Fatal("the query did not narrow the list")
	}

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.paletteOpen {
		t.Fatal("the first esc closed the palette instead of clearing the query")
	}
	if m.palette.input.Value() != "" {
		t.Fatalf("query = %q after esc, want it cleared", m.palette.input.Value())
	}
	if len(m.palette.list.VisibleItems()) != all {
		t.Fatalf("clearing the query left %d of %d rows", len(m.palette.list.VisibleItems()), all)
	}

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.paletteOpen {
		t.Fatal("the second esc did not close the palette")
	}
}

// TestPaletteArrowsMoveTheSelection covers the gesture bubbles' own list cannot
// serve here: while a list is in its Filtering state it runs no browsing keys
// and treats up/down as filter-accept, so the palette drives the cursor through
// the list's public API instead.
func TestPaletteArrowsMoveTheSelection(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, keyRune(':'))

	// Identity is the key label, not the description: comparing descriptions
	// ("preview↑"/"preview↓", per defaultKeyMap) would still catch a
	// selection that never moved, but the key label is what the palette and
	// this test actually key selection identity on, so assert on that.
	first, ok := m.palette.list.SelectedItem().(paletteItem)
	if !ok {
		t.Fatal("the palette has no selection on open")
	}
	m, _ = update(t, m, keyDown)
	second, ok := m.palette.list.SelectedItem().(paletteItem)
	if !ok || second.keyLabel == first.keyLabel {
		t.Fatalf("down did not move the selection off %q", first.keyLabel)
	}
	m, _ = update(t, m, keyUp)
	if back, ok := m.palette.list.SelectedItem().(paletteItem); !ok || back.keyLabel != first.keyLabel {
		t.Fatalf("up did not return the selection to %q", first.keyLabel)
	}
}

// TestPaletteArrowsWrapAtTheEnds documents the wrap behavior InfiniteScrolling
// is meant to add: bubbles' list clamps by default; these lists set
// InfiniteScrolling so CursorUp/CursorDown wrap (bubbles v2.2.1
// list/list.go:516-565). The palette calls p.list.CursorUp()/CursorDown()
// directly (palette.go, tea.KeyUp/tea.KeyDown cases), so this test covers
// the direct-API path rather than list.Model.Update. The load-bearing
// assertion is the first one — that up from the top entry lands on the LAST
// entry rather than staying put. The second assertion (down from the last
// entry returns to the first) is the return trip and would pass vacuously if
// the first half already failed to move, so it is not by itself sufficient
// evidence of wrap.
//
// Compared against VisibleItems(), not Items(): the palette opens without
// ever calling applyQuery (openPalette in model.go never calls SetFilterText),
// so the list stays Unfiltered and the two happen to coincide here, but
// VisibleItems() is what CursorUp/CursorDown actually walk, so it is the
// correct oracle regardless.
func TestPaletteArrowsWrapAtTheEnds(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, keyRune(':'))

	first, ok := m.palette.list.SelectedItem().(paletteItem)
	if !ok {
		t.Fatal("the palette has no selection on open")
	}

	visible := m.palette.list.VisibleItems()
	last, ok := visible[len(visible)-1].(paletteItem)
	if !ok {
		t.Fatal("the palette's last visible item is not a paletteItem")
	}

	// Load-bearing: up from the first entry must wrap to the last, not clamp.
	m, _ = update(t, m, keyUp)
	if got, ok := m.palette.list.SelectedItem().(paletteItem); !ok || got.keyLabel != last.keyLabel {
		gotLabel := ""
		if ok {
			gotLabel = got.keyLabel
		}
		t.Fatalf("up from the first entry (%q) landed on %q, want it to wrap to the last entry (%q)", first.keyLabel, gotLabel, last.keyLabel)
	}

	m, _ = update(t, m, keyDown)
	if got, ok := m.palette.list.SelectedItem().(paletteItem); !ok || got.keyLabel != first.keyLabel {
		gotLabel := ""
		if ok {
			gotLabel = got.keyLabel
		}
		t.Fatalf("down from the last entry landed on %q, want it to wrap back to the first entry (%q)", gotLabel, first.keyLabel)
	}
}

// TestPaletteIsAdvertisedAndRendered covers the discoverability half: `:` has
// to be findable in `?`'s full help (it is deliberately absent from the width-
// capped short line), and the open palette has to be ON SCREEN, since it owns
// the keyboard.
func TestPaletteIsAdvertisedAndRendered(t *testing.T) {
	m := testModel(sess("alpha"))
	m.helpShowAll = true
	// help.Model's full view is a padded column layout, so the key and its
	// description are separated by run-length padding rather than one space —
	// the line is asserted rather than the concatenation the short line uses.
	full := xansi.Strip(m.renderHelp())
	var advertised bool
	for _, line := range strings.Split(full, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == m.keys.Palette.Help().Key &&
			strings.Contains(line, m.keys.Palette.Help().Desc) {
			advertised = true
		}
	}
	if !advertised {
		t.Fatalf("full help does not advertise %q:\n%s", m.keys.Palette.Help().Key, full)
	}

	m.helpShowAll = false
	m, _ = update(t, m, keyRune(':'))
	frame := render(m)
	if !strings.Contains(frame, "Commands") {
		t.Fatalf("the open palette is not on screen:\n%s", frame)
	}
	if !strings.Contains(frame, paletteRunKey.Help().Desc) {
		t.Fatalf("the palette does not render its own help line:\n%s", frame)
	}
}

// TestPaletteSurvivesAQueryThatMatchesNothing exercises list.filterItems — the
// closure of charmbracelet/bubbletea#826 — with a query that empties the match
// set, which is where the reported nil dereference landed. It cannot fire on a
// list built by list.New (Filter is DefaultFilter, every item is a non-nil
// concrete type), and this is the assertion that keeps that true.
func TestPaletteSurvivesAQueryThatMatchesNothing(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, keyRune(':'))
	for _, r := range "zzzzz" {
		m, _ = update(t, m, keyRune(r))
	}
	if got := len(m.palette.list.VisibleItems()); got != 0 {
		t.Fatalf("a nonsense query left %d rows", got)
	}
	// Enter on nothing must be inert rather than a nil-item panic, and the
	// query must still be editable.
	m, _ = update(t, m, keyEnter)
	if !m.paletteOpen {
		t.Fatal("enter with no match closed the palette")
	}
	if render(m) == "" {
		t.Fatal("an empty match set rendered nothing")
	}
}

// TestPaletteRowsRenderAtUniformWidth pins paletteDelegate's padding the way
// TestSwitcherRowsRenderAtUniformWidth pins the switcher's: the highlighted
// row and every other row render at the same width. Before the W5.T1 fix
// cycle only the selected row was padded (theme.rowSelected.Width), leaving
// unselected rows at their natural length — the same defect W7.T1 fixed on
// the switcher.
//
// It calls paletteDelegate.Render directly per row rather than reading widths
// back out of the list's View(): list.Model.View() joins rows with
// lipgloss.JoinVertical, which pads every shorter line out to the widest one
// regardless of what the delegate did, so a view-level comparison passes even
// with the unselected branch's padding removed (W7.T1's recorded
// vacuous-test trap).
func TestPaletteRowsRenderAtUniformWidth(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, keyRune(':'))
	items := m.palette.list.Items()
	if len(items) < 2 {
		t.Fatalf("palette opened with %d items; need at least 2 to compare", len(items))
	}

	d := paletteDelegate{}
	var selected, unselected strings.Builder
	d.Render(&selected, m.palette.list, 0, items[0])   // index 0 is selected by default
	d.Render(&unselected, m.palette.list, 1, items[1]) // any other index is not

	got, want := lipgloss.Width(unselected.String()), lipgloss.Width(selected.String())
	if got != want {
		t.Errorf("unselected row width = %d, want %d (same as the selected row)", got, want)
	}
	// Rows fill the list width by construction now — the key label is a
	// right-aligned column — so the padding question is answered by the
	// column: every row must end in its key label at the full width.
	if listW := m.palette.list.Width(); got != listW {
		t.Fatalf("row width = %d, want the list width %d", got, listW)
	}
	if row := xansi.Strip(unselected.String()); !strings.HasSuffix(row, items[1].(paletteItem).keyLabel) {
		t.Fatalf("unselected row should end in its right-aligned key label: %q", row)
	}
}

// TestPaletteFullWidthRowDoesNotWrap is renderPalette's container test, the
// direct sibling of TestSwitcherListWidthMatchesRenderedBoxUsableWidth
// (switcher_test.go, W7.T1): a row whose natural width is one column UNDER
// the list's reported width — which the delegate's selected-row padding then
// brings up to exactly that width, the same way any real selected row reaches
// it — must survive renderPalette's real box unbroken at 80 and 120 columns.
//
// Before the W5.T1 fix cycle renderPalette handed sizedBox the bare contentW
// where content-plus-padding was wanted (theme.detailBox is Padding(0, 1)),
// so the box's wrap point sat 2 columns inside the width paletteListSize told
// the list it had, and the padded selected row always wrapped.
func TestPaletteFullWidthRowDoesNotWrap(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := testModel(sess("alpha"))
			m.width = width
			m, _ = update(t, m, keyRune(':'))
			if !m.paletteOpen {
				t.Fatal("`:` did not open the palette")
			}
			listW, _ := m.paletteListSize()

			const suffix = "  q" // desc + two-space gap + keyLabel, per paletteDelegate.Render
			descLen := listW - lipgloss.Width(suffix) - 1
			if descLen < 1 {
				t.Fatalf("listW %d leaves no room for a description", listW)
			}
			desc := strings.Repeat("x", descLen)
			item := paletteItem{desc: desc, keyLabel: "q"}
			m.palette.list.SetItems([]list.Item{item})

			// The expected line is the delegate's own rendering — the same
			// row the box will draw — so the assertion is only about the box.
			var row strings.Builder
			paletteDelegate{}.Render(&row, m.palette.list, 0, item)
			wantLine := xansi.Strip(row.String())
			if got := lipgloss.Width(wantLine); got != listW {
				t.Fatalf("test premise: the row is %d cells, want the list width %d", got, listW)
			}
			out := xansi.Strip(m.renderPaletteModal())
			if !strings.Contains(out, wantLine) {
				t.Fatalf("full-width row wrapped inside the palette box (listW=%d):\n%s", listW, out)
			}
			if got := lipgloss.Width(out); got != listW+4 {
				t.Fatalf("palette box is %d cells wide, want the list width plus border and padding (%d)", got, listW+4)
			}
		})
	}
}

// TestPaletteSelectedRowHighlightSpansTheWholeRow is the palette's copy of the
// session list's and the switcher's version of this assertion. Unlike those
// two it pins behaviour that was already correct rather than repairing a
// defect: the row's one styled fragment is the key label, it sits last, and a
// fragment's opening sequence sets a foreground without clearing the enclosing
// background, so the bar already reached the row's end.
//
// It is here because that correctness is accidental, not structural — it
// survives only while nothing styled lands before the key. Styling the filter's
// matched runs, the obvious next feature for a fuzzy palette, would put a reset
// mid-row and cut the bar there. This is what would catch that, and what keeps
// paletteDelegate's spanStyle call from reading as a no-op somebody can delete.
func TestPaletteSelectedRowHighlightSpansTheWholeRow(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, keyRune(':'))
	items := m.palette.list.Items()
	if len(items) == 0 {
		t.Fatal("palette opened with no items")
	}

	var b strings.Builder
	paletteDelegate{}.Render(&b, m.palette.list, 0, items[0]) // index 0 is selected by default
	assertHighlightSpansRow(t, b.String(), m.palette.list.Width())
}

// TestPaletteUnselectedRowCarriesNoHighlight is the other half: spanStyle is on
// the selected branch only, so every other row must stay unpainted.
func TestPaletteUnselectedRowCarriesNoHighlight(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, keyRune(':'))
	items := m.palette.list.Items()
	if len(items) < 2 {
		t.Fatalf("palette opened with %d items; need at least 2", len(items))
	}

	var b strings.Builder
	paletteDelegate{}.Render(&b, m.palette.list, 1, items[1])
	if strings.Contains(b.String(), "48;2;") {
		t.Errorf("unselected palette row carries a background: %q", b.String())
	}
}
