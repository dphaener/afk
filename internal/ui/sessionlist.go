// sessionlist.go holds the dashboard's session list — a bubbles/list.Model and
// the ItemDelegate that draws one session row into it. Row CONTENT lives in
// views.go's renderSessionRow; this file only decides which rows are on screen.
package ui

import (
	"fmt"
	"io"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// sessionRowLines and sessionRowGap must keep matching renderSessionRow's
// output. bubbles sizes a page from Height()+Spacing() and pads a short last
// page with that many newlines per missing row, so a delegate that
// under-reports its height overflows the box it was sized for. A row is one
// line with no gap: the list is a table (rowColumns, views.go), and a table
// with a blank line between every row is a list of cards again.
const (
	sessionRowLines = 1
	sessionRowGap   = 0
)

// sessionItem carries the session by value; rebuildListItems refreshes every
// item from Model.sessions on each discovery pass, which is what lets
// Model.selected() answer straight out of the list.
type sessionItem struct {
	session Session
}

// FilterValue includes the on-disk name even when a label is set, because the
// row prints both — a filter that could not find a session by a name shown on
// its own row would be a trap. The provider rides along for the same reason:
// a row can print " codex", so typing "codex" has to find it.
func (i sessionItem) FilterValue() string {
	v := i.session.Name
	if i.session.Label != "" {
		v = i.session.Label + " " + i.session.Name
	}
	if i.session.Provider != "" {
		v += " " + i.session.Provider
	}
	return v
}

// sessionDelegate holds `now` rather than the Model because the only ambient
// fact a row needs is the current time (showsWedged's badge age) and Deps.Now
// is fixed for a Model's life, so it cannot go stale — which a captured Model
// could. Anything mutable a row needs belongs on sessionItem instead.
type sessionDelegate struct {
	now func() time.Time
}

var _ list.ItemDelegate = sessionDelegate{}

func (d sessionDelegate) Height() int  { return sessionRowLines }
func (d sessionDelegate) Spacing() int { return sessionRowGap }

// Update is a no-op: every gesture the session list answers is a rule in
// mode.go's inputNormal or inputFilter spec, so no navigation hides in here.
func (d sessionDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d sessionDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(sessionItem)
	if !ok {
		return
	}
	now := time.Now()
	if d.now != nil {
		now = d.now()
	}
	fmt.Fprint(w, renderSessionRow(it.session, index == m.Index(), m.Width(), now))
}

// newSessionList builds the sidebar's list.Model. The chrome is off because the
// dashboard already renders the session count in its header and its keys in its
// footer; a second copy in the sidebar would contradict the first the moment
// one changed. The pagination indicator is off for a reason of its own,
// recorded in decisions.md: bubbles subtracts its line unconditionally when it
// is enabled (View measures lipgloss.Height("") == 1 for the single-page case),
// costing a sidebar row on every dashboard with nothing to page.
//
// showFilter stays ON with showTitle off, which is what reserves exactly one
// line above the rows for the filter prompt. TitleBar's shipped padding is
// cleared so that line is one row tall in both states; left alone it grows to
// two the moment `/` is pressed, clipping the last row out from under the user.
func newSessionList(keys keyMap, now func() time.Time) list.Model {
	l := list.New(nil, sessionDelegate{now: now}, 0, 0)
	// InfiniteScrolling is what CursorUp/CursorDown check at the boundary
	// (bubbles v2.2.1 list/list.go:516-565): CursorUp calls GoToEnd when the
	// cursor underflows past the first item on the first page, and CursorDown
	// calls GoToStart when it overflows past the last item on the last page —
	// both of those helpers reset the paginator itself (GoToStart/GoToEnd,
	// list.go:567-577), so wrapping works across page boundaries, not just
	// within one page. PrevPage, NextPage, GoToStart, and GoToEnd (the
	// pgup/pgdn/home/end handlers) never read this flag and keep clamping to
	// [0, maxCursorIndex], so those keys are unaffected by turning this on.
	l.InfiniteScrolling = true
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)

	// bubbles v2.2.1 binds list's Quit to "v", not v1's "q" (list/keys.go, whose
	// help text still reads "select"), so this is not the defence against the
	// dashboard's own `q` it would have been under v1 — what keeps `q` ours is
	// that inputNormal never routes an unclaimed key to this component
	// (mode.go's `fall: routeIgnored`). This is what stops "v" quitting outright
	// and skipping requestQuit's teardown.
	l.DisableQuitKeybindings()

	// Removed rather than disabled: list.updateKeybindings re-enables the help
	// toggle on every SetSize/SetItems/filter transition, so SetEnabled(false)
	// would survive only until the first window resize. A binding with no keys
	// can never match, which is the only durable neutralisation.
	l.KeyMap.ShowFullHelp = key.NewBinding()
	l.KeyMap.CloseFullHelp = key.NewBinding()

	// Copied, not aliased: key.Binding is a plain struct (bubbles/v2 key.go),
	// so this assignment copies keys.Filter's fields rather than sharing
	// storage with it. The two sides still cannot name different keys, because
	// both are populated from this same keys.Filter value at construction and
	// neither is reassigned afterward: startSessionFilter forwards the key it
	// matched to this component, which works only while both agree on what
	// that key is.
	l.KeyMap.Filter = keys.Filter

	l.Styles.TitleBar = lipgloss.NewStyle()
	l.FilterInput.Prompt = "/"

	return l
}

// selectListIndex is list.Model.Select with the guard it lacks: Select divides
// by Paginator.PerPage, which is zero on a list.Model nobody constructed — the
// zero-value Models this package's tests build — so an unguarded call panics.
// Such a list has no items either, so declining is the right answer.
func selectListIndex(l *list.Model, i int) {
	if l.Paginator.PerPage < 1 {
		return
	}
	l.Select(i)
}
