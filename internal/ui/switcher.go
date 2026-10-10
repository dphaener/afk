// switcher.go implements the project-switcher overlay (the `p` keybinding,
// W4.T1): a filterable list of discovered projects, following newform.go's
// precedent as a self-contained bubbles-backed component kept out of Model.
// Rows are computed once at construction from the plain data the parent
// supplies ([]project.Project plus a per-project session summary), so this
// package touches neither the filesystem nor internal/discover's Discover
// call directly — it renders whatever the parent already collected. The
// parent is responsible for keeping that data fresh and constructing a new
// switcher when it changes; this struct never refreshes itself and never
// issues a side-effecting tea.Cmd, only ever forwarding whatever
// list.Model.Update hands back.
package ui

import (
	"charm.land/lipgloss/v2"
	"fmt"
	"io"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/project"
)

// switcherRescanKey is the overlay's `R` rescan chord. It is declared here
// (the switcher's own file) rather than inline in Update's switch, and
// mode.go's registry references this SAME value for the overlay's help
// keymap — the shared-by-reference pattern keys.Filter already uses with
// the session list (sessionlist.go) — so the key that actually fires the
// rescan and the key Wave 6's help/palette advertises can never name two
// different chords.
var switcherRescanKey = key.NewBinding(
	key.WithKeys("R"),
	key.WithHelp("R", "rescan"),
)

// switcherResultKind is the outcome of a completed interaction with the
// switcher, handed back to the parent so it — not this package — decides
// what happens next: switch the active project, close the overlay, or
// trigger a rescan. See W3.T1 for the switch-active-project method and
// W4.T2 for the rescan command this component only requests, never runs.
type switcherResultKind int

const (
	// switcherNone is the zero value: still open, nothing to act on yet. A
	// caller that neglects to check Kind therefore does nothing rather than
	// something wrong.
	switcherNone switcherResultKind = iota
	switcherChosen
	switcherDismissed
	switcherRescan
)

// switcherResult reports what Update decided, if anything. Path is only
// meaningful when Kind is switcherChosen.
type switcherResult struct {
	Kind switcherResultKind
	Path string
}

// switcher is the project-switcher overlay's self-contained state: a
// bubbles/list.Model of project rows and nothing else, matching newForm's
// shape (newform.go) for a bubbles-backed sub-component kept out of Model.
// The zero value is NOT safe — list.Model.Update/View nil-deref on it —
// always construct via newSwitcher.
type switcher struct {
	list list.Model
}

// switcherItem is one row: the path it selects, the display name computed
// by internal/project.DisplayNames, and the session summary rendered
// alongside it (count, and whether any of that project's sessions is
// awaiting input).
type switcherItem struct {
	path     string
	name     string
	count    int
	awaiting bool
}

func (i switcherItem) FilterValue() string { return i.name }

// newSwitcher builds the overlay from the parent's current project list,
// per-project session data, and per-project open counts. sessions is keyed
// by Project.Path — the same shape as the per-project discovery cache W3.T1
// adds to Model — so the parent can pass its cache field straight through
// with no translation. counts is likewise keyed by Project.Path; a project
// with no entry (never opened, or counts unavailable) sorts as count zero,
// same as one with an entry actually equal to zero.
//
// Row order is no longer "follows projects as given": newSwitcher sorts a
// local copy into the three tiers this overlay renders in — see
// sortSwitcherProjects below — before building rows from it. The sort lives
// here, in the one place every construction of this component passes
// through, rather than in the caller, so a future third call site cannot
// silently render scan order by neglecting to sort first. m.projects itself
// is untouched by this: it stays in scan order in the parent (model.go),
// because sameProjectPaths compares that slice by position to decide
// whether refreshSwitcher needs to rebuild the overlay at all, and writing a
// usage-derived order back into it would either hide a genuine reorder from
// that gate or make every scan tick look like one.
//
// DisplayNames (internal/project) remains pure and order-preserving exactly
// as it documents itself — the reordering below is this component's own,
// applied to what DisplayNames already returned, not a contradiction of that
// contract.
func newSwitcher(projects []project.Project, sessions map[string][]discover.SessionState, counts map[string]int, width, height int) switcher {
	sorted := sortSwitcherProjects(projects, sessions, counts)

	items := make([]list.Item, len(sorted))
	for i, p := range sorted {
		states := sessions[p.Path]
		awaiting := false
		for _, s := range states {
			if s.AwaitingInput {
				awaiting = true
				break
			}
		}
		items[i] = switcherItem{
			path:     p.Path,
			name:     p.Name,
			count:    len(states),
			awaiting: awaiting,
		}
	}

	l := list.New(items, switcherDelegate{}, width, height)
	// Wraps like the session list; see newSessionList in sessionlist.go.
	l.InfiniteScrolling = true
	l.Title = ""
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	// Off for the reason newSessionList gives: bubbles charges a row for the
	// dots even on a single page, and inside a modal sized to its rows that
	// row came out of the projects — a four-row list paged one project at a
	// time. The box's bottom edge is where a page indicator belongs.
	l.SetShowPagination(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()

	return switcher{list: l}
}

// sortSwitcherProjects orders a COPY of projects into the switcher's three
// display tiers: any project with a session awaiting input first, then
// descending open count, then ascending case-folded display name, then
// ascending full path as a final tie-break. The path tie-break is what makes
// the order total and deterministic even when the first three keys all agree
// (e.g. two never-opened projects that happen to share a display name) —
// sort.Slice is documented as not stable, so without it the pair's relative
// order would depend on the sort's internal partitioning rather than on
// anything about the projects themselves, the same failure mode
// sortSessionStates (internal/discover) exists to avoid for session rows.
//
// Kept pure per this repo's convention for anything that would otherwise
// branch on the environment (CLAUDE.md): sessions and counts arrive as
// parameters instead of being read from disk here, so the ordering is
// table-testable with no filesystem or process state involved. The caller
// (newSwitcher) never mutates projects in place through this function; a
// fresh slice is returned so the original — m.projects in the parent — is
// left exactly as given.
func sortSwitcherProjects(projects []project.Project, sessions map[string][]discover.SessionState, counts map[string]int) []project.Project {
	sorted := make([]project.Project, len(projects))
	copy(sorted, projects)

	awaitingInput := func(p project.Project) bool {
		for _, s := range sessions[p.Path] {
			if s.AwaitingInput {
				return true
			}
		}
		return false
	}

	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]

		if aw, bw := awaitingInput(a), awaitingInput(b); aw != bw {
			return aw
		}
		if ac, bc := counts[a.Path], counts[b.Path]; ac != bc {
			return ac > bc
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		return a.Path < b.Path
	})

	return sorted
}

// SetSize resizes the underlying list. The parent calls this whenever the
// dashboard's frame changes size; the overlay draws within whatever space
// it is given rather than owning any layout math of its own — layoutDims
// and mouseFrameLayout stay untouched and out of scope here.
func (s *switcher) SetSize(width, height int) {
	s.list.SetWidth(width)
	s.list.SetHeight(height)
}

// View renders the overlay.
func (s *switcher) View() string {
	return s.list.View()
}

// Update routes one message through the switcher and reports whether the
// interaction produced an outcome. The only tea.Cmds it ever returns are
// the ones list.Model.Update itself produces — the filter input's cursor
// blink and pagination — forwarded unchanged; dropping them would leave the
// blink cursor dead and pagination unresponsive the same way newform.go's
// form gestures forward m.form.baseList.Update's Cmd rather than
// discarding it. This component issues no Cmd of its own.
//
// Esc has two jobs depending on filter state, mirroring cancelForm's
// documented routing table for the creation form's own list (model.go):
// while a filter is active (Filtering or FilterApplied), Esc clears the
// filter and the overlay stays open; only once there is no filter left does
// Esc dismiss the overlay. FilterState has three values, not two, so the
// clear-filter check below is deliberately keyed on "!= Unfiltered" rather
// than "== Filtering" — gating it just on Filtering would let Esc fall
// through and close the whole overlay on the user's first press after
// narrowing the list with Enter/Tab.
//
// `R` triggers a rescan at every filter state except active editing
// (Filtering), where it is ordinary filter-query text instead — so it can
// be typed into a search but still fires as a shortcut once the user is
// done typing (FilterApplied) or never started (Unfiltered).
func (s *switcher) Update(msg tea.Msg) (tea.Cmd, switcherResult) {
	// tea.KeyPressMsg, not the tea.KeyMsg interface it satisfies: v2 splits key
	// events into press and release, and this program never requests
	// View.KeyboardEnhancements.ReportEventTypes, so a release can only start
	// arriving if some future change turns that on — at which point matching the
	// interface would silently fire every binding twice.
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		s.list, cmd = s.list.Update(msg)
		return cmd, switcherResult{}
	}

	if s.list.FilterState() == list.Filtering {
		switch k.Code {
		case tea.KeyEsc:
			s.list.ResetFilter()
			return nil, switcherResult{}
		case tea.KeyEnter:
			if s.list.FilterInput.Value() == "" {
				// bubbles disables AcceptWhileFiltering while the filter
				// input is empty (list.go's updateKeybindings), so Enter
				// arrives already swallowed and nothing would happen.
				// Reset instead of leaving the user stuck in Filtering with
				// only Esc as a way out.
				s.list.ResetFilter()
				return nil, switcherResult{}
			}
		}
		var cmd tea.Cmd
		s.list, cmd = s.list.Update(k)
		return cmd, switcherResult{}
	}

	if k.Code == tea.KeyEsc && s.list.FilterState() != list.Unfiltered {
		s.list.ResetFilter()
		return nil, switcherResult{}
	}

	switch {
	case k.Code == tea.KeyEsc:
		return nil, switcherResult{Kind: switcherDismissed}
	case k.Code == tea.KeyEnter:
		if it, ok := s.list.SelectedItem().(switcherItem); ok {
			return nil, switcherResult{Kind: switcherChosen, Path: it.path}
		}
		return nil, switcherResult{}
	case key.Matches(k, switcherRescanKey):
		return nil, switcherResult{Kind: switcherRescan}
	}

	var cmd tea.Cmd
	s.list, cmd = s.list.Update(k)
	return cmd, switcherResult{}
}

// switcherDelegate renders one project row: the display name, the session
// count, and an awaiting-input dot when any of the project's sessions has
// one. The dot reuses theme.awaiting — the same style renderRow's "●
// INPUT" badge uses (views.go) — rather than a new color, per the plan's
// instruction to mirror the existing badge pattern instead of introducing
// one.
type switcherDelegate struct{}

func (d switcherDelegate) Height() int  { return 1 }
func (d switcherDelegate) Spacing() int { return 0 }

func (d switcherDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d switcherDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(switcherItem)
	if !ok {
		return
	}

	count := fmt.Sprintf("%d session", it.count)
	if it.count != 1 {
		count += "s"
	}

	width := m.Width()
	if width < 1 {
		width = 1
	}
	// The session count and awaiting dot form a right-aligned column, so
	// counts line up down the list; the name is cut first if the two would
	// collide.
	right := theme.dim.Render(count)
	if it.awaiting {
		right += " " + theme.awaitingFlag.Render("●")
	} else {
		right += "  "
	}
	nameRoom := width - lipgloss.Width(right) - 2
	line := truncate(it.name, max(nameRoom, 1))
	if gap := width - lipgloss.Width(line) - lipgloss.Width(right); gap >= 2 {
		line += strings.Repeat(" ", gap) + right
	} else {
		line = truncate(line+"  "+right, width)
	}

	// Both branches pad to width, not just the selected one: an unselected row
	// left at its natural length renders shorter than the highlight bar, which
	// is what made the wrap read as selection-only — the padded row was the
	// only one ever exactly as wide as the box's wrap point. theme.rowNormal
	// carries no color of its own, so this changes only the trailing
	// whitespace, never how an unselected row looks.
	//
	// spanStyle (styles.go) on the selected branch for the same reason
	// padSessionRow (views.go) needs it: the dim session count closes with an
	// SGR reset, a reset drops the enclosing selection background, and
	// everything after the count is the row's last two cells — the awaiting
	// dot, or the two spaces standing in for it. So the bar ran to the end of
	// "2 sessions" and left the dot that means "this project needs you"
	// sitting outside the highlight, at the one spot the eye goes to.
	if index == m.Index() {
		line = theme.rowSelected.Width(width).Render(spanStyle(line, theme.rowSelected))
	} else {
		line = theme.rowNormal.Width(width).Render(line)
	}

	fmt.Fprint(w, line)
}
