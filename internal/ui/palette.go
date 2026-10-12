// palette.go implements the `:` command palette (W6.T3): a fuzzy-searchable
// list of every action the dashboard's normal mode can currently perform.
//
// It is DERIVED, not registered. Its rows come from mode.go's dispatch table —
// one row per modeRule whose binding is enabled and whose guard currently holds
// — so an action appears here because the dispatch can perform it, never
// because it was listed twice. There is deliberately no action enum, no handler
// switch and no name table in this file: selecting a row re-presses the row's
// own key through Model.dispatchKey, which is why "the palette does exactly
// what the key does" is a property of the construction rather than a claim two
// copies of a table have to keep agreeing on.
//
// The two bubbles components do all the work the name suggests: list.Model
// renders the rows and does the fuzzy matching (list.DefaultFilter, applied
// through SetFilterText), textinput.Model holds the query. Nothing here
// implements a list, an input, or a matcher.
package ui

import (
	"charm.land/lipgloss/v2"
	"fmt"
	"io"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// The palette's own gestures, declared HELP-ONLY on the same precedent as the
// switcher's (mode.go): each is staged inside palette.Update, and declaring
// them as parent rules in the registry would implement them twice — the exact
// drift this whole mechanism exists to eliminate.
var (
	paletteRunKey = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "run"),
	)
	paletteMoveKey = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "move"),
	)
	// Two stages, matching the switcher's Esc exactly: clear the query first,
	// dismiss only once there is nothing left to clear.
	paletteCloseKey = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "clear, then close"),
	)
)

// paletteResultKind is the outcome of a completed interaction, handed back so
// the parent — not this component — decides what happens next. Same division of
// labour as switcherResult: this component never runs an action.
type paletteResultKind int

const (
	// paletteNone is the zero value: still open, nothing to act on yet, so a
	// caller that neglects to check Kind does nothing rather than something
	// wrong.
	paletteNone paletteResultKind = iota
	paletteRun
	paletteDismissed
)

// paletteResult reports what Update decided, if anything. Press is meaningful
// only when Kind is paletteRun, and is the keypress the chosen row stands for —
// the parent re-dispatches it rather than being told an action name.
type paletteResult struct {
	Kind  paletteResultKind
	Press tea.KeyPressMsg
}

// commandPalette is the command palette's self-contained state. The zero value is NOT
// safe (list.Model.View nil-derefs on it, and its Filter func is nil, which is
// the one way to reach the historical filterItems nil-dereference — see
// newPalette) — always construct via newPalette.
type commandPalette struct {
	list  list.Model
	input textinput.Model
}

// paletteItem is one row. press is the keypress the row stands for: the parent
// re-dispatches it, so no action name is carried and no handler is duplicated.
type paletteItem struct {
	desc     string
	keyLabel string
	press    tea.KeyPressMsg
}

// FilterValue includes the key label as well as the description so `:` then
// "ctrl" finds the chord actions and `:` then "new" finds the named ones. The
// session list makes the same choice for the same reason (sessionlist.go): the
// row prints both, and a query that could not find a row by something printed
// on it is a trap.
func (i paletteItem) FilterValue() string { return i.desc + " " + i.keyLabel }

// paletteEntries is the derivation, and it is the whole point of this file:
// one row per dispatch rule the mode could act on right now.
//
// A rule contributes a row when all four hold, and each exclusion is a
// correctness requirement rather than a curation choice:
//
//   - Its binding is enabled. key.Matches refuses a disabled binding
//     (bubbles/v2@v2.2.1 key.go:Matches), so a disabled rule could not fire
//     from a keypress either — offering it would be offering a no-op.
//   - Its guards hold, evaluated from the SAME routeConds the dispatch uses
//     (mode.go's route), so "close pane" is offered only with a pane open and
//     "clear filter" only with a filter applied — again, the exact set of keys
//     that would do something if pressed.
//   - Its first key round-trips through paletteKeyPress. A key this package
//     cannot synthesize could not be re-pressed, and a row that silently did
//     nothing would be worse than an absent one. TestEveryNormalRuleIsRunnable
//     fails loudly if a future binding lands in that state.
//   - enabled(binding) — the caller's live-state gate, so the palette hides
//     exactly what `?`'s full help hides (Model.normalBindingEnabled, views.go).
//
// Order is the mode's HELP order (spec.keys.bindings), not its rule order:
// rules are ordered for match fidelity against the handlers they replaced,
// which starts at ctrl+t and reads like nothing a person would scan. Rules with
// no keymap entry sort last rather than being dropped, so adding a rule alone
// still surfaces it — the derivation must not depend on remembering to touch a
// second slice.
func paletteEntries(spec modeSpec, conds routeConds, enabled func(key.Binding) bool) []paletteItem {
	if enabled == nil {
		enabled = key.Binding.Enabled
	}

	position := func(b key.Binding) int {
		for i, kb := range spec.keys.bindings {
			if slices.Equal(kb.Keys(), b.Keys()) {
				return i
			}
		}
		return len(spec.keys.bindings)
	}

	type ranked struct {
		item paletteItem
		at   int
	}
	var ranks []ranked
	for _, rule := range spec.rules {
		if rule.needsPane && !conds.paneOpen {
			continue
		}
		if rule.needsFilter && !conds.listFiltered {
			continue
		}
		if !rule.binding.Enabled() || !enabled(rule.binding) {
			continue
		}
		keys := rule.binding.Keys()
		if len(keys) == 0 {
			continue
		}
		press, ok := paletteKeyPress(keys[0])
		if !ok {
			continue
		}
		h := rule.binding.Help()
		ranks = append(ranks, ranked{
			item: paletteItem{desc: h.Desc, keyLabel: h.Key, press: press},
			at:   position(rule.binding),
		})
	}
	slices.SortStableFunc(ranks, func(a, b ranked) int { return a.at - b.at })

	items := make([]paletteItem, len(ranks))
	for i, r := range ranks {
		items[i] = r.item
	}
	return items
}

// paletteNamedKeys inverts the named half of ultraviolet's keyTypeString
// (key.go:459), which is unexported, so a binding key like "pgup" can be turned
// back into the Key it was printed from. Only names a key.Binding in this
// package could plausibly carry are listed; anything else fails the round-trip
// below rather than being guessed at.
var paletteNamedKeys = map[string]rune{
	"enter":     tea.KeyEnter,
	"esc":       tea.KeyEsc,
	"tab":       tea.KeyTab,
	"space":     tea.KeySpace,
	"backspace": tea.KeyBackspace,
	"delete":    tea.KeyDelete,
	"insert":    tea.KeyInsert,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
}

// paletteKeyPress builds the tea.KeyPressMsg that a key.Binding key string
// names, so choosing a palette row can re-press the row's own key.
//
// The reported ok is a ROUND-TRIP assertion, not a parse-succeeded flag: the
// constructed message is accepted only when its String() reproduces the exact
// string it was built from — the same String() key.Matches compares against
// (bubbles/v2@v2.2.1 key.go:Matches). That is what makes a wrong parse fail
// closed. Without it, a mis-decoded chord would dispatch some OTHER binding,
// which is the one failure mode a command palette must not have.
//
// Text is set for an unmodified printable rune and left empty otherwise,
// mirroring how the terminal's own decoding arrives: Key.String returns Text
// when it is non-empty (ultraviolet key.go:391), and Keystroke's
// modifier-qualified form otherwise.
func paletteKeyPress(name string) (tea.KeyPressMsg, bool) {
	rest := name
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
	if code, ok := paletteNamedKeys[rest]; ok {
		k = tea.KeyPressMsg{Code: code, Mod: mod}
	} else {
		runes := []rune(rest)
		if len(runes) != 1 {
			return tea.KeyPressMsg{}, false
		}
		k = tea.KeyPressMsg{Code: runes[0], Mod: mod}
		if mod == 0 {
			k.Text = rest
		}
	}
	if k.String() != name {
		return tea.KeyPressMsg{}, false
	}
	return k, true
}

// newPalette builds the overlay from a mode's spec and the ambient conditions
// its dispatch would evaluate. Like the switcher it holds a SNAPSHOT and never
// refreshes itself: rebuilding under the user's hands would reset both the
// cursor and the query they are typing, and the discovery tick fires every two
// seconds. The set it shows is therefore the set that was runnable when `:` was
// pressed, which is also the set the user was looking at.
//
// list.New is what makes the historical filterItems nil-dereference
// (charmbracelet/bubbletea#826, in list.filterItems.func1) unreachable here:
// the closure's only nil-derefable operands in bubbles v2.2.1 are m.Filter and
// a nil Item, and list.New assigns Filter = DefaultFilter (list.go:236) while
// every item below is a non-nil concrete paletteItem. A zero-value list.Model
// is the one construction that keeps Filter nil, and this package never makes
// one — the same reason the session list and the switcher are safe.
func newPalette(spec modeSpec, conds routeConds, enabled func(key.Binding) bool, width, height int) commandPalette {
	entries := paletteEntries(spec, conds, enabled)
	items := make([]list.Item, len(entries))
	for i, e := range entries {
		items[i] = e
	}

	l := list.New(items, paletteDelegate{}, width, height)
	// Wraps like the session list; see newSessionList in sessionlist.go.
	l.InfiniteScrolling = true
	l.Title = ""
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	// The query lives in this component's own textinput, rendered above the
	// list, so the list's built-in filter BAR is off — its filter FUNCTION is
	// exactly what SetFilterText drives. Filtering stays enabled because
	// SetFilterText is the entry point to it.
	l.SetShowFilter(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()

	in := textinput.New()
	in.Prompt = ": "
	in.Placeholder = "type to filter actions"
	in.CharLimit = 64
	if width > 4 {
		in.SetWidth(width - 4)
	}
	// textinput.Update returns immediately on an unfocused model, so focusing at
	// construction is what makes the component usable by itself. The Cmd is the
	// cursor blink and is the parent's to schedule (openPalette re-calls Focus
	// for it) — a component built in a test needs the focus, not the timer.
	_ = in.Focus()

	return commandPalette{list: l, input: in}
}

// SetSize resizes the components to whatever space the parent gives them; like
// the switcher, this overlay owns no layout math of its own.
func (p *commandPalette) SetSize(width, height int) {
	p.list.SetWidth(width)
	p.list.SetHeight(height)
	if width > 4 {
		p.input.SetWidth(width - 4)
	}
}

// applyQuery pushes the current query into the list's own filter.
//
// SetFilterText runs list's filter SYNCHRONOUSLY (list.go:285-292 executes
// filterItems' Cmd inline and installs the result), which is why this component
// needs no FilterMatchesMsg plumbing at all. That matters beyond convenience:
// FilterMatchesMsg names no sender, three other lists in this package can emit
// one, and a parent that drops or misroutes it leaves a filter that silently
// narrows nothing — the defect the project switcher has today (issues.md,
// W7.T1). A surface that never emits the message cannot have that bug.
func (p *commandPalette) applyQuery() {
	p.list.SetFilterText(p.input.Value())
}

// Update routes one message through the palette and reports whether the
// interaction produced an outcome.
//
// Every gesture is staged here rather than delegated to list.Model.Update,
// and that is deliberate rather than a re-implementation. While a list is in
// its own Filtering state it hands the whole message to the filter input and
// runs NO browsing keys (list.go:843-846), so up/down would not move the
// cursor; worse, bubbles' default AcceptWhileFiltering set includes up, down,
// tab, shift+tab, ctrl+j and ctrl+k, so those would end filtering instead. The
// list's public cursor API is driven directly for movement, and its filter
// function through SetFilterText — nothing here reimplements either.
//
// Esc is two-stage, matching switcher.Update: with a query it clears the query
// and the palette stays open; with none it dismisses. A palette is always
// accepting text, so "clear" is keyed on the query being non-empty rather than
// on a FilterState the component never leaves.
func (p *commandPalette) Update(msg tea.Msg) (tea.Cmd, paletteResult) {
	// tea.KeyPressMsg, not the tea.KeyMsg interface it satisfies — same reason
	// switcher.Update gives: v2 splits press and release, and matching the
	// interface would silently fire every gesture twice if this program ever
	// requested release events.
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return cmd, paletteResult{}
	}

	// Matches on Code alone, ignoring Mod: e.g. ctrl+enter or alt+up would run
	// the same action as enter/up. Accepted, not an oversight — none of these
	// four keys has a modified variant meaning something else in the palette,
	// so widening the match costs nothing.
	switch k.Code {
	case tea.KeyEsc:
		if p.input.Value() != "" {
			p.input.SetValue("")
			p.applyQuery()
			return nil, paletteResult{}
		}
		return nil, paletteResult{Kind: paletteDismissed}
	case tea.KeyEnter:
		if it, ok := p.list.SelectedItem().(paletteItem); ok {
			return nil, paletteResult{Kind: paletteRun, Press: it.press}
		}
		// No match under the cursor (the query narrowed to nothing). Staying
		// open is the only useful answer: the query is still there to edit.
		return nil, paletteResult{}
	case tea.KeyUp:
		p.list.CursorUp()
		return nil, paletteResult{}
	case tea.KeyDown:
		p.list.CursorDown()
		return nil, paletteResult{}
	}

	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	// Re-filtering only on an actual change keeps SetFilterText's GoToStart from
	// yanking the cursor back to the top on a keypress that typed nothing.
	if p.input.Value() != before {
		p.applyQuery()
	}
	return cmd, paletteResult{}
}

func (p *commandPalette) View() string {
	return p.input.View() + "\n\n" + p.list.View()
}

type paletteDelegate struct{}

func (d paletteDelegate) Height() int  { return 1 }
func (d paletteDelegate) Spacing() int { return 0 }

func (d paletteDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d paletteDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(paletteItem)
	if !ok {
		return
	}

	width := m.Width()
	if width < 1 {
		width = 1
	}

	// The key label sits in a right-aligned column so the keys line up down
	// the list and the eye can scan them; the description is cut first if
	// the two would collide.
	key := theme.helpStyles.ShortKey.Render(it.keyLabel)
	descRoom := width - lipgloss.Width(key) - 2
	line := truncate(it.desc, max(descRoom, 1))
	if gap := width - lipgloss.Width(line) - lipgloss.Width(key); gap >= 2 {
		line += strings.Repeat(" ", gap) + key
	} else {
		line = truncate(line+"  "+key, width)
	}

	// Both branches pad to width, not just the selected one — the same fix
	// switcherDelegate.Render carries (W7.T1's defect (2)): an unselected row
	// left at its natural length renders shorter than the highlight bar, so any
	// width defect in the containing box tracks selection instead of content.
	// theme.rowNormal is a zero-value style, so this adds only trailing spaces.
	//
	// spanStyle (styles.go) carries the selection background across the SGR
	// resets lipgloss leaves inside line — see padSessionRow (views.go) for
	// what goes wrong without it.
	//
	// Unlike the session row and the switcher row, THIS row has no reachable
	// defect today: its one styled fragment is the key label, it sits last,
	// and a fragment's opening sequence sets a foreground without clearing the
	// enclosing background, so the bar already reached the end of the line.
	// The call is here because the three delegates are otherwise the same
	// shape, and the first styled fragment that lands anywhere but last —
	// highlighting the filter's matched runs is the obvious one — would
	// silently cut the bar at that point. Keeping the odd one out uniform is
	// cheaper than diagnosing that later.
	if index == m.Index() {
		line = theme.rowSelected.Width(width).Render(spanStyle(line, theme.rowSelected))
	} else {
		line = theme.rowNormal.Width(width).Render(line)
	}

	fmt.Fprint(w, line)
}
