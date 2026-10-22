// keysoverlay.go draws the keys overlay `?` opens (docs/dash-design.md §4.9,
// T16.2): the path through the three levels, the agent rule in an alert band,
// then every key, level by level, with `/` narrowing the rows. It replaced the
// footer's full help, which listed one level's keys at a time and could not
// say how the levels join up.
//
// Every row is built from the key table (keyTable, keytable.go), so the
// overlay cannot list a key the dashboard does not answer to, nor leave an
// action out: TestKeysOverlayListsEveryAction holds it to the table.
//
// Like the other overlays it holds nothing typed that quitting could lose,
// reads no file and runs nothing. Its key decision is a pure function of plain
// values (keysKeyDecision), so every branch is a table row.
package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The overlay's keys, help-only in the registry (mode.go) on the plan check's
// precedent: keysKeyDecision stages them, and a parent rule for any of them
// would implement the gesture twice. They are what the footer shows while
// the overlay is up.
var (
	keysScrollKey = key.NewBinding(
		key.WithKeys("j", "k", "up", "down"),
		key.WithHelp("j/k", "scroll"),
	)
	keysSearchKey = key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "search"),
	)
	keysCloseKey = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close"),
	)
)

// keysOverlayKeys is the overlay's help line, in the order the footer shows
// it.
func keysOverlayKeys() []key.Binding {
	return []key.Binding{keysSearchKey, keysScrollKey, keysCloseKey}
}

// keysOverlayWidth is the overlay's preferred content width: "a large
// centred overlay" (§4.9), as wide as a 120-column terminal allows inside the
// modal margin. modalContentDims narrows it on a smaller one.
const keysOverlayWidth = 112

// keysTwoColumnsFrom is the content width from which the levels are laid out
// in two columns rather than one: below it a column would leave a help line
// too few cells to read.
const keysTwoColumnsFrom = 96

// keysKeyColumn is the widest the key column grows: wide enough for the
// Agent's "ctrl+] ctrl+]", the longest key a person presses.
const keysKeyColumn = 14

// keysPath is the path line at the top of the overlay (§4.9): the three
// levels and the keys between them.
const keysPath = "All projects ─enter▸ Project ─enter▸ Agent ◂esc"

// keysAgentRule is the alert band's text: the one rule a person must know
// before anything else works, because without it every key they press in the
// agent goes to the agent. It names the configured leader.
func keysAgentRule(leader string) string {
	return "In the agent every key goes to the agent: press " + leader + " first"
}

// keysEntry is one key in the overlay: the key as it is pressed, and its line
// of help. A by-name action has the name where the key would be.
type keysEntry struct {
	key, help string
}

// keysSection is one level's keys under a heading.
type keysSection struct {
	title   string
	entries []keysEntry
}

// keysSections is every key-table row, grouped as §5 groups them: the keys
// that work at every level once, then each level's own, then the actions with
// no key, which the `:` search reaches by name. A row that works at two
// levels but not everywhere (`d`, the move keys) is listed under each, since
// a person reads the section for the level they are on.
//
// The Agent's keys are named with the leader in front, as they are pressed
// there, and the section ends with the leader twice, which is how the agent
// gets the leader itself (keyStripEntries lists it the same way).
//
// An action with two rows ("start a new plan" is `n` at All projects and by
// name inside a project) is listed once per section it has a row for, so it
// appears under its key and under the by-name actions alike.
func keysSections(leader string) []keysSection {
	every := keysSection{title: "Everywhere (in the agent, press " + leader + " first)"}
	all := keysSection{title: "All projects"}
	project := keysSection{title: "Project"}
	agent := keysSection{title: "Agent, after " + leader}
	byName := keysSection{title: "By name, from :"}
	for _, r := range keyTable {
		switch {
		case r.key == "":
			byName.entries = append(byName.entries, keysEntry{key: r.name, help: r.help})
		case r.levels == atEveryLevel:
			every.entries = append(every.entries, keysEntry{key: r.keyLabel(), help: r.help})
		default:
			e := keysEntry{key: r.keyLabel(), help: r.help}
			if r.levels.has(levelAllProjects) {
				all.entries = append(all.entries, e)
			}
			if r.levels.has(levelProject) {
				project.entries = append(project.entries, e)
			}
			if r.levels.has(levelAgent) {
				agent.entries = append(agent.entries, keysEntry{key: rowKeyAt(r, levelAgent, leader), help: r.help})
			}
		}
	}
	agent.entries = append(agent.entries, keysEntry{
		key: leader + " " + leader, help: "Send " + leader + " itself to the agent",
	})
	return []keysSection{every, all, project, agent, byName}
}

// filterKeysSections keeps the entries whose key or help contains query,
// case-insensitively, and drops a section left empty. A substring match, as
// the `:` search uses, because a person typing a word expects the rows that
// contain it.
func filterKeysSections(sections []keysSection, query string) []keysSection {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return sections
	}
	var out []keysSection
	for _, s := range sections {
		var kept []keysEntry
		for _, e := range s.entries {
			if strings.Contains(strings.ToLower(e.key+" "+e.help), q) {
				kept = append(kept, e)
			}
		}
		if len(kept) > 0 {
			out = append(out, keysSection{title: s.title, entries: kept})
		}
	}
	return out
}

// keysSectionLines draws one section for a column width wide: the heading,
// then each entry as its key (in the help key's colour, padded to keyW) and
// its help, the help wrapped under itself rather than cut, because the end of
// a help line ("as afk tokens does") is often the part that says what the
// action is.
func keysSectionLines(s keysSection, width int) []string {
	keyW := 0
	for _, e := range s.entries {
		keyW = max(keyW, lipgloss.Width(e.key))
	}
	// A by-name action's "key" is its name, and the move row's is eight keys,
	// either of which would push every help line in the section over to the
	// right. The key column stops at keysKeyColumn (or half the column, if
	// that is less), and a key wider than that stands on a line of its own
	// with its help under it, so no key is cut and the column stays narrow.
	keyW = min(keyW, keysKeyColumn, max(width/2, 1))
	helpW := max(width-keyW-2, 1)
	indent := strings.Repeat(" ", keyW+2)
	lines := []string{theme.sessionName.Render(truncate(s.title, width))}
	for _, e := range s.entries {
		lead := ""
		if lipgloss.Width(e.key) > keyW {
			lines = append(lines, theme.helpStyles.ShortKey.Render(truncate(e.key, width)))
			lead = indent
		} else {
			lead = theme.helpStyles.ShortKey.Render(e.key) + strings.Repeat(" ", keyW-lipgloss.Width(e.key)+2)
		}
		for i, w := range strings.Split(lipgloss.Wrap(e.help, helpW, ""), "\n") {
			if i == 0 {
				lines = append(lines, lead+truncate(w, helpW))
				continue
			}
			lines = append(lines, indent+truncate(w, helpW))
		}
	}
	return lines
}

// keysBodyLines is the overlay's scrolling part for a content width: the
// sections in one column, or in two from keysTwoColumnsFrom, with a blank line
// between sections. The two columns are filled by sections, not by lines, so
// no section is split across them: the first column takes sections until it
// holds about half the lines.
//
// With nothing matching the query it is one line that says so (and what was
// typed), so an empty box never reads as a broken one.
func keysBodyLines(sections []keysSection, query string, width int) []string {
	if len(sections) == 0 {
		return []string{theme.dim.Render(truncate(fmt.Sprintf("No key matches %q. Press esc to clear the search.", query), width))}
	}
	if width < keysTwoColumnsFrom {
		return stackKeysSections(sections, width)
	}
	colW := (width - 3) / 2
	drawn := make([][]string, len(sections))
	total := 0
	for i, s := range sections {
		drawn[i] = keysSectionLines(s, colW)
		total += len(drawn[i]) + 1
	}
	var left, right []string
	for _, d := range drawn {
		target := &right
		if len(left) < total/2 || len(left) == 0 {
			target = &left
		}
		if len(*target) > 0 {
			*target = append(*target, "")
		}
		*target = append(*target, d...)
	}
	out := make([]string, max(len(left), len(right)))
	for i := range out {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		r := ""
		if i < len(right) {
			r = right[i]
		}
		out[i] = padRight(l, colW) + "   " + r
	}
	return out
}

// stackKeysSections is keysBodyLines' one-column layout.
func stackKeysSections(sections []keysSection, width int) []string {
	var out []string
	for i, s := range sections {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, keysSectionLines(s, width)...)
	}
	return out
}

// keysOverlay is the overlay's state: the query, whether keys are typing into
// it, and how far the body is scrolled. The rows themselves are rebuilt from
// the key table each frame; they never change while the dashboard runs, so
// there is nothing to snapshot.
type keysOverlay struct {
	leader string
	query  string
	// typing is the filter line taking keys: `/` starts it, enter or esc
	// stops it.
	typing bool
	offset int
	// width and height are the content size, set by SetSize from relayout,
	// never from View.
	width, height int
}

// newKeysOverlay opens the overlay for the Model's leader.
func newKeysOverlay(leader string, width, height int) keysOverlay {
	o := keysOverlay{leader: leader}
	o.SetSize(width, height)
	return o
}

// keysHeadRows is how many content rows sit above the body: the path, a
// blank, the alert band and a blank. The filter line, while there is one,
// adds keysFilterRows more.
const (
	keysHeadRows   = 4
	keysFilterRows = 2
)

// filterShown reports whether the filter line is drawn: while typing, and
// while a query narrows the rows after typing stopped, so the reader can see
// why rows are missing.
func (o keysOverlay) filterShown() bool {
	return o.typing || o.query != ""
}

// body is the scrolling part at the current width.
func (o keysOverlay) body() []string {
	return keysBodyLines(filterKeysSections(keysSections(o.leader), o.query), o.query, o.width)
}

// bodyRows is how many body lines fit under the head.
func (o keysOverlay) bodyRows() int {
	head := keysHeadRows
	if o.filterShown() {
		head += keysFilterRows
	}
	return max(o.height-head, 1)
}

// wantRows is how many content rows the overlay would like at a content
// width, so the parent can size the box before the body is laid out.
func (o keysOverlay) wantRows(width int) int {
	o.width = width
	return keysHeadRows + keysFilterRows + len(o.body())
}

// SetSize gives the overlay its content size and re-clamps the scroll.
func (o *keysOverlay) SetSize(width, height int) {
	o.width, o.height = max(width, 1), max(height, 1)
	o.clamp()
}

// clamp keeps the offset inside the body, which a resize or a query can
// shorten under it.
func (o *keysOverlay) clamp() {
	o.offset = min(max(o.offset, 0), max(len(o.body())-o.bodyRows(), 0))
}

// keysOutcomeKind is what one key in the overlay does.
type keysOutcomeKind int

const (
	// keysIgnore is the zero value: nothing happens and the overlay stays.
	keysIgnore keysOutcomeKind = iota
	// keysClose closes the overlay.
	keysClose
	// keysStartTyping starts the filter line.
	keysStartTyping
	// keysStopTyping stops typing and keeps the query.
	keysStopTyping
	// keysClear empties the query and stops typing.
	keysClear
	// keysType appends text to the query.
	keysType
	// keysBackspace drops the query's last character.
	keysBackspace
	// keysScroll moves the body by delta lines.
	keysScroll
	// keysTop and keysBottom scroll to either end.
	keysTop
	keysBottom
)

// String exists for test-failure output; nothing renders it.
func (k keysOutcomeKind) String() string {
	return [...]string{"ignore", "close", "start-typing", "stop-typing", "clear", "type", "backspace", "scroll", "top", "bottom"}[k]
}

// keysOutcome is keysKeyDecision's answer; text and delta are meaningful only
// for keysType and keysScroll.
type keysOutcome struct {
	kind  keysOutcomeKind
	text  string
	delta int
}

// keysKeyDecision decides what key k (its String form, and the text it
// types) does in the overlay. page is how many lines pgup and pgdown move.
//
// The order is the design:
//
//  1. esc steps back one thing at a time (A7): while typing or with a query,
//     it clears the query; with neither, it closes. A person who searched
//     and wants out presses it twice, and never loses the overlay to a key
//     meant for the search.
//  2. The leader closes, as it closes the `:` search: it is how the overlay
//     was reached from the Agent, and pressing it again reads as "never
//     mind". checkLeader refuses every key below as a leader, so testing it
//     first shadows none of them.
//  3. While typing, enter stops typing and keeps the query, backspace edits
//     it, the arrows still scroll, and any other printable key types, `j`,
//     `k`, `/` and `?` included, so any word can be searched for.
//  4. Otherwise `/` starts typing, `?` closes (it is the key that opened the
//     overlay), and j/k, the arrows, pgup/pgdown and home/end scroll.
func keysKeyDecision(k, text, leader string, typing, hasQuery bool, page int) keysOutcome {
	if k == "esc" {
		if typing || hasQuery {
			return keysOutcome{kind: keysClear}
		}
		return keysOutcome{kind: keysClose}
	}
	if k == leader {
		return keysOutcome{kind: keysClose}
	}
	switch k {
	case "up":
		return keysOutcome{kind: keysScroll, delta: -1}
	case "down":
		return keysOutcome{kind: keysScroll, delta: 1}
	case "pgup":
		return keysOutcome{kind: keysScroll, delta: -page}
	case "pgdown":
		return keysOutcome{kind: keysScroll, delta: page}
	}
	if typing {
		switch {
		case k == "enter":
			return keysOutcome{kind: keysStopTyping}
		case k == "backspace":
			return keysOutcome{kind: keysBackspace}
		case text != "":
			return keysOutcome{kind: keysType, text: text}
		}
		return keysOutcome{}
	}
	switch k {
	case "/":
		return keysOutcome{kind: keysStartTyping}
	case "?":
		return keysOutcome{kind: keysClose}
	case "k":
		return keysOutcome{kind: keysScroll, delta: -1}
	case "j":
		return keysOutcome{kind: keysScroll, delta: 1}
	case "home":
		return keysOutcome{kind: keysTop}
	case "end":
		return keysOutcome{kind: keysBottom}
	}
	return keysOutcome{}
}

// Update handles one key and reports whether it closed the overlay.
func (o *keysOverlay) Update(k tea.KeyPressMsg) (closed bool) {
	out := keysKeyDecision(k.String(), k.Text, o.leader, o.typing, o.query != "", o.bodyRows())
	switch out.kind {
	case keysClose:
		return true
	case keysStartTyping:
		o.typing = true
	case keysStopTyping:
		o.typing = false
	case keysClear:
		o.query, o.typing = "", false
		o.offset = 0
	case keysType:
		o.query += out.text
		o.offset = 0
	case keysBackspace:
		if o.query != "" {
			_, size := utf8.DecodeLastRuneInString(o.query)
			o.query = o.query[:len(o.query)-size]
		}
		o.offset = 0
	case keysScroll:
		o.offset += out.delta
	case keysTop:
		o.offset = 0
	case keysBottom:
		o.offset = len(o.body())
	}
	o.clamp()
	return false
}

// hint is the box's bottom edge: what the keys do now, and how far down the
// reader is when the body is taller than the box.
func (o keysOverlay) hint() string {
	h := "/ search · j/k scroll · esc close"
	if o.typing {
		h = "type to search · enter keeps it · esc clears"
	} else if o.query != "" {
		h = "esc clears the search · / edits it"
	}
	if body := len(o.body()); body > o.bodyRows() {
		h += fmt.Sprintf(" · %d–%d of %d", o.offset+1, min(o.offset+o.bodyRows(), body), body)
	}
	return h
}

// View draws the content: the path, the alert band, the filter line while
// there is one, then the body from the scroll offset, no more lines than fit.
func (o keysOverlay) View() string {
	lines := []string{
		truncate(keysPath, o.width),
		"",
		theme.alertBar(keysAgentRule(o.leader), o.width),
		"",
	}
	if o.filterShown() {
		q := "/ " + o.query
		if o.typing {
			q += "▏"
		}
		lines = append(lines, truncate(q, o.width), "")
	}
	body := o.body()
	end := min(o.offset+o.bodyRows(), len(body))
	for _, ln := range body[o.offset:end] {
		lines = append(lines, truncate(ln, o.width))
	}
	if len(lines) > o.height {
		lines = lines[:o.height]
	}
	return strings.Join(lines, "\n")
}

// openKeys is `?` (the leader then `?` in the Agent): the keys overlay. The
// keyboard stays where it was (agentActionStays), so closing it from the
// Agent goes back to typing into the agent.
func (m Model) openKeys() (tea.Model, tea.Cmd) {
	m.keysOpen = true
	m.keysView = newKeysOverlay(m.leaderKey.name, 1, 1)
	m.sizeKeysOverlay()
	// A toast floats over the top right corner, which the box covers.
	m.clearStatus()
	return m, nil
}

// keysOverlaySize is the overlay's content size for the current terminal:
// the width first, since the rows the body wants depend on how it wraps.
func (m Model) keysOverlaySize() (width, height int) {
	width, _ = modalContentDims(m.width, m.height, keysOverlayWidth, 3)
	return modalContentDims(m.width, m.height, keysOverlayWidth, m.keysView.wantRows(width))
}

// sizeKeysOverlay applies the current layout to an open overlay, from
// relayout and after opening, for the reason sizeInbox exists.
func (m *Model) sizeKeysOverlay() {
	if !m.keysOpen || m.width == 0 {
		return
	}
	m.keysView.SetSize(m.keysOverlaySize())
}

// renderKeysModal draws the overlay's box.
func (m Model) renderKeysModal() string {
	width, height := m.keysOverlaySize()
	return renderModalBox("keys", m.keysView.hint(), m.keysView.View(), width, height)
}

// keysComponentKey is inputKeys' fall-through: every key but ctrl+c (a
// parent rule, mode.go) reaches the overlay.
func (m Model) keysComponentKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.keysView.Update(k) {
		m.keysOpen = false
	}
	return m, nil
}
