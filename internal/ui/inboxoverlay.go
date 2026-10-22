// inboxoverlay.go implements the inbox overlay (the `i` keybinding): the
// project's inbox items as a list, one item in full, and a one-line input to
// answer it.
//
// The component follows the switcher's shape (switcher.go): it is
// self-contained, it holds a copy of the data the parent gave it, it never
// reads or writes a file, and it reports what the user decided as a result
// for the parent to act on. The one write this feature makes, recording an
// answer, is issued by the parent as a tea.Cmd through Deps.AnswerInbox
// (answerInboxCmd below), so nothing here runs on the Update goroutine that
// could stall it.
//
// Unlike the switcher it IS refreshed while open (setItems). The inbox has
// other writers: the build loop adds items, and the owner may answer one from
// the web page on a phone while this overlay is on screen. A copy that went
// stale would let the owner type an answer that is then refused.
package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/inbox"
)

// The overlay's keys. They are staged inside inboxOverlay.Update and declared
// HELP-ONLY in the registry (mode.go), on the switcher's and the palette's
// precedent: a parent rule for any of them would implement the gesture twice.
// Update matches against these same values, so the footer cannot name a key
// the overlay does not answer.
//
// There is deliberately no quit key among them. On the item screen every
// letter is text for the answer, and an overlay that quit on one screen and
// typed on the other would lose a half-written answer to a habit.
var (
	inboxUpKey = key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)
	inboxDownKey = key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)
	inboxOpenKey = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "read"),
	)
	inboxCloseKey = key.NewBinding(
		key.WithKeys("esc", "ctrl+g"),
		key.WithHelp("esc", "close"),
	)
	inboxSendKey = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "send answer"),
	)
	// Two stages, as the palette's esc: clear what was typed first, go back
	// only once there is nothing left to clear.
	inboxBackKey = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "clear, then back"),
	)
	inboxBackAnsweredKey = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
	)
	inboxScrollUpKey = key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "scroll up"),
	)
	inboxScrollDownKey = key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdn", "scroll down"),
	)
	inboxCloseAllKey = key.NewBinding(
		key.WithKeys("ctrl+g"),
		key.WithHelp("ctrl+g", "close inbox"),
	)
)

// inboxListKeys and inboxItemKeys are each screen's help line. The registry
// advertises both (inboxModeKeys); the footer shows the one for the screen
// that is up (inboxOverlay.helpKeys).
func inboxListKeys() []key.Binding {
	return []key.Binding{inboxOpenKey, inboxUpKey, inboxDownKey, inboxCloseKey}
}

func inboxItemKeys() []key.Binding {
	return []key.Binding{inboxSendKey, inboxBackKey, inboxScrollUpKey, inboxScrollDownKey, inboxCloseAllKey}
}

func inboxAnsweredItemKeys() []key.Binding {
	return []key.Binding{inboxBackAnsweredKey, inboxScrollUpKey, inboxScrollDownKey, inboxCloseAllKey}
}

// inboxModeKeys is the mode's whole help surface, both screens.
func inboxModeKeys() []key.Binding {
	return append(inboxListKeys(), inboxItemKeys()...)
}

// What the overlay and the `i` key say. They are constants because the tests
// and the README quote them.
const (
	inboxNotReadNotice       = "The inbox has not been read yet. Try again in a moment."
	inboxEmptyText           = "Nothing in the inbox."
	inboxEmptyAnswerNote     = "The answer was empty. Type an answer and send it again."
	inboxAlreadyAnsweredNote = "This item already has an answer, and the first answer is the one that is kept. Nothing was changed."
	inboxSendingNote         = "Saving the answer…"
	inboxAnswerHint          = "type an answer"
	inboxAnswerOptionsHint   = "type an answer, or a number to pick an option"
)

// inboxModalWidth is the overlay's preferred content width: a title and a
// wrapped log line both read comfortably at it. inboxItemRows caps the item
// screen's height before its body scrolls, inboxMinItemRows is the least it
// is given, and inboxMaxListRows caps the list before it scrolls.
// modalContentDims clamps all of them to the terminal.
const (
	inboxModalWidth  = 84
	inboxItemRows    = 24
	inboxMinItemRows = 6
	inboxMaxListRows = 14
)

type inboxScreen int

const (
	inboxScreenList inboxScreen = iota
	inboxScreenItem
)

// inboxResultKind is what one keypress decided, handed back so the parent
// acts on it. The zero value means "still open, nothing to do".
type inboxResultKind int

const (
	inboxNone inboxResultKind = iota
	inboxClosed
	inboxSend
)

// inboxResult carries a send's details. ID, Text and Blocking are meaningful
// only when Kind is inboxSend. Text is what was typed, untrimmed and
// unresolved: the store trims it and turns a number into its option under the
// inbox lock, and doing either here as well would resolve a numeric option
// twice (see dashAnswerInbox in the root package).
type inboxResult struct {
	Kind     inboxResultKind
	ID       string
	Text     string
	Blocking bool
}

type inboxNoteKind int

const (
	inboxNoteInfo inboxNoteKind = iota
	inboxNoteProblem
)

// inboxOverlay is the overlay's state. The zero value is not usable (its
// input and viewport are unbuilt); always construct it with newInboxOverlay.
// The parent keeps the last-built one after closing, as it does the
// switcher's, so a stray render is harmless.
type inboxOverlay struct {
	// items is the parent's snapshot order: open and blocking, open, answered.
	items      []inbox.Item
	unreadable int
	cursor     int
	offset     int

	screen inboxScreen
	// item is the copy shown on the item screen. It is kept beside items and
	// not looked up by id on every draw, because the item's file can vanish
	// from a later snapshot while the owner is still reading it.
	item  inbox.Item
	input textinput.Model
	body  viewport.Model
	// foot is the item screen's lines under the scrolling body: the input and
	// whatever is said about the last send. It is laid out in layout(), not
	// in View, because its height decides the viewport's.
	foot []string

	note     string
	noteKind inboxNoteKind
	// sending is the id whose answer is on its way to disk, "" when none is.
	// It stops a second enter from sending the same answer twice.
	sending string

	width, height int
	// now is the clock reading the ages on screen are worked out from. The
	// parent refreshes it with each snapshot; the component has no clock.
	now time.Time
}

// newInboxOverlay builds the overlay on its list screen.
func newInboxOverlay(items []inbox.Item, unreadable int, now time.Time, width, height int) inboxOverlay {
	o := inboxOverlay{
		items:      append([]inbox.Item(nil), items...),
		unreadable: unreadable,
		input:      newInboxInput(),
		body:       viewport.New(),
		now:        now,
	}
	o.SetSize(width, height)
	return o
}

// newInboxInput is the answer field. It is rebuilt for every item opened, so
// text typed for one item can never be sent as the answer to another.
func newInboxInput() textinput.Model {
	in := textinput.New()
	in.Prompt = "> "
	// Long enough for a pasted token or a paragraph; an answer is one line.
	in.CharLimit = 4000
	// textinput.Update returns at once on an unfocused model, so the field is
	// focused at construction. The blink Cmd is the parent's to schedule.
	_ = in.Focus()
	return in
}

// SetSize gives the overlay its content size. The parent calls it from
// relayout and after anything that changes the size the overlay wants
// (sizeInbox), never from View: View has a value receiver, and sizing done
// there would land on a copy.
func (o *inboxOverlay) SetSize(width, height int) {
	o.width, o.height = max(width, 1), max(height, 1)
	// The field's width excludes its "> " prompt, and one cell is kept for
	// the cursor at the end of a full line.
	o.input.SetWidth(max(o.width-4, 1))
	o.layout()
}

// wantRows is how many content rows the current screen would like at a
// content width. Both screens are sized to what they hold, so two items, or a
// one-line notice, do not float in a tall empty box: the list by its rows,
// an item by its title, its wrapped text and the rows under it, up to the cap
// where the body starts to scroll.
//
// The width is a parameter and not o.width because the parent asks this
// before it sets the size: after a terminal resize the text wraps at the new
// width, and the height has to be worked out for that width, not the old one.
func (o inboxOverlay) wantRows(width int) int {
	if o.screen == inboxScreenItem {
		rows := 1 + len(inboxItemLines(o.item, o.now, width)) + len(o.footLines(width))
		return max(inboxMinItemRows, min(rows, inboxItemRows))
	}
	rows := max(len(o.items), 1)
	if o.unreadable > 0 {
		rows++
	}
	return max(3, min(rows, inboxMaxListRows))
}

// showing reports whether the item screen is up on the item with this id.
func (o inboxOverlay) showing(id string) bool {
	return o.screen == inboxScreenItem && o.item.ID == id
}

// setItems swaps in a newer snapshot of the inbox.
//
// The list keeps its cursor on the same item, by id, so a new item arriving
// above it does not move the selection under the owner's hand. The item
// screen keeps whatever is typed and takes the item's newest copy, which is
// how an item answered from the phone turns into its answered view.
//
// One rule guards against an older snapshot: a copy held here that has an
// answer is never replaced by one that has none. An item's answer is never
// removed (the first answer is the one kept), so "answered, then open" can
// only be a read that started before the answer was written and landed after
// it. Without the rule the item just answered would flick back to open for
// one tick.
func (o *inboxOverlay) setItems(items []inbox.Item, unreadable int, now time.Time) {
	answered := make(map[string]inbox.Item)
	for _, it := range o.items {
		if !it.Open() {
			answered[it.ID] = it
		}
	}
	if !o.item.Open() && o.item.ID != "" {
		answered[o.item.ID] = o.item
	}

	selected := ""
	if o.cursor >= 0 && o.cursor < len(o.items) {
		selected = o.items[o.cursor].ID
	}

	next := make([]inbox.Item, len(items))
	for i, it := range items {
		if kept, ok := answered[it.ID]; ok && it.Open() {
			it = kept
		}
		next[i] = it
	}
	o.items = next
	o.unreadable = unreadable
	o.now = now

	if i := o.indexOf(selected); i >= 0 {
		o.cursor = i
	}
	if o.screen == inboxScreenItem {
		if i := o.indexOf(o.item.ID); i >= 0 {
			o.item = o.items[i]
			if !o.item.Open() && o.sending != o.item.ID && o.note != inboxAlreadyAnsweredNote {
				// It was answered from somewhere else, and the field is gone.
				// A note about the text that was in it (an empty answer) has
				// nothing left to be about. The note for a refused send stays:
				// it is the explanation for the view the owner is now reading.
				o.note = ""
			}
		}
	}
	o.layout()
}

func (o inboxOverlay) indexOf(id string) int {
	if id == "" {
		return -1
	}
	for i, it := range o.items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// replace puts a newer copy of one item into the list, where it is.
func (o *inboxOverlay) replace(it inbox.Item) {
	if i := o.indexOf(it.ID); i >= 0 {
		o.items[i] = it
	}
}

// answered records a successful send: the item's stored copy goes into the
// list and, if that item is still the one on screen, the overlay goes back to
// the list.
func (o *inboxOverlay) answered(it inbox.Item) {
	o.replace(it)
	if o.sending == it.ID {
		o.sending = ""
	}
	if o.showing(it.ID) {
		o.item = it
		o.backToList()
		return
	}
	o.layout()
}

// refused records a send that did not save. stored is the item as the store
// holds it, when the store returned one (an item that already has an answer):
// it replaces the copy on screen, so the answer that stands is what is shown.
func (o *inboxOverlay) refused(id string, stored *inbox.Item, note string) {
	if o.sending == id {
		o.sending = ""
	}
	if stored != nil {
		o.replace(*stored)
	}
	if o.showing(id) {
		if stored != nil {
			o.item = *stored
		}
		o.note, o.noteKind = note, inboxNoteProblem
	}
	o.layout()
}

func (o *inboxOverlay) backToList() {
	if i := o.indexOf(o.item.ID); i >= 0 {
		o.cursor = i
	}
	o.screen = inboxScreenList
	o.input = newInboxInput()
	o.input.SetWidth(max(o.width-4, 1))
	o.note = ""
	o.layout()
}

func (o *inboxOverlay) openItem() tea.Cmd {
	if o.cursor < 0 || o.cursor >= len(o.items) {
		return nil
	}
	o.item = o.items[o.cursor]
	o.screen = inboxScreenItem
	o.input = newInboxInput()
	o.input.SetWidth(max(o.width-4, 1))
	o.note = ""
	o.body.GotoTop()
	o.layout()
	if !o.item.Open() {
		return nil
	}
	return o.input.Focus()
}

// Update routes one message through the overlay and reports what it decided.
//
// A message that is not a keypress is the answer field's own traffic (its
// cursor blink, a paste) and goes to the field when one is showing.
//
// tea.KeyPressMsg and not the tea.KeyMsg interface, for the reason
// switcher.Update gives: a key release must never fire a binding twice.
func (o *inboxOverlay) Update(msg tea.Msg) (tea.Cmd, inboxResult) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if o.screen == inboxScreenItem && o.item.Open() {
			var cmd tea.Cmd
			o.input, cmd = o.input.Update(msg)
			o.layout()
			return cmd, inboxResult{}
		}
		return nil, inboxResult{}
	}
	if o.screen == inboxScreenList {
		return o.updateList(k)
	}
	return o.updateItem(k)
}

func (o *inboxOverlay) updateList(k tea.KeyPressMsg) (tea.Cmd, inboxResult) {
	switch {
	case key.Matches(k, inboxCloseKey):
		return nil, inboxResult{Kind: inboxClosed}
	case key.Matches(k, inboxUpKey):
		o.cursor = max(o.cursor-1, 0)
	case key.Matches(k, inboxDownKey):
		o.cursor = max(min(o.cursor+1, len(o.items)-1), 0)
	case key.Matches(k, inboxOpenKey):
		return o.openItem(), inboxResult{}
	}
	// Every other key is swallowed: a stray letter on the list must not act
	// on the dashboard underneath.
	o.layout()
	return nil, inboxResult{}
}

func (o *inboxOverlay) updateItem(k tea.KeyPressMsg) (tea.Cmd, inboxResult) {
	open := o.item.Open()
	switch {
	case key.Matches(k, inboxCloseAllKey):
		return nil, inboxResult{Kind: inboxClosed}
	case key.Matches(k, inboxBackKey):
		if open && o.input.Value() != "" {
			o.input.Reset()
			o.note = ""
			o.layout()
			return nil, inboxResult{}
		}
		o.backToList()
		return nil, inboxResult{}
	case key.Matches(k, inboxScrollUpKey):
		o.body.PageUp()
		return nil, inboxResult{}
	case key.Matches(k, inboxScrollDownKey):
		o.body.PageDown()
		return nil, inboxResult{}
	case key.Matches(k, inboxSendKey):
		if !open || o.sending == o.item.ID {
			return nil, inboxResult{}
		}
		text := o.input.Value()
		if strings.TrimSpace(text) == "" {
			o.note, o.noteKind = inboxEmptyAnswerNote, inboxNoteProblem
			o.layout()
			return nil, inboxResult{}
		}
		o.sending = o.item.ID
		o.note, o.noteKind = inboxSendingNote, inboxNoteInfo
		o.layout()
		return nil, inboxResult{Kind: inboxSend, ID: o.item.ID, Text: text, Blocking: o.item.Blocking}
	}

	if !open {
		// No field to type into. The line keys scroll, since nothing else
		// wants them here; everything else is swallowed.
		switch {
		case key.Matches(k, inboxUpKey):
			o.body.ScrollUp(1)
		case key.Matches(k, inboxDownKey):
			o.body.ScrollDown(1)
		}
		return nil, inboxResult{}
	}

	// Everything else is text for the answer: letters, digits, the arrows
	// that move the cursor, `q`, ctrl+c. A problem note is about the text as
	// it was, so typing clears it.
	if o.noteKind == inboxNoteProblem {
		o.note = ""
	}
	var cmd tea.Cmd
	o.input, cmd = o.input.Update(k)
	o.layout()
	return cmd, inboxResult{}
}

// helpKeys is the footer's help line for the screen that is up.
func (o inboxOverlay) helpKeys() []key.Binding {
	switch {
	case o.screen == inboxScreenList:
		return inboxListKeys()
	case o.item.Open():
		return inboxItemKeys()
	}
	return inboxAnsweredItemKeys()
}

// title is the text on the box's top edge.
func (o inboxOverlay) title() string {
	if o.screen == inboxScreenItem {
		return "Inbox · " + o.item.ID
	}
	return "Inbox"
}

// hint is the text on the box's bottom edge: where the reader is, when there
// is more than fits. It is empty when everything is on screen.
func (o inboxOverlay) hint() string {
	if o.screen == inboxScreenItem {
		if o.body.TotalLineCount() <= o.body.Height() {
			return ""
		}
		return fmt.Sprintf("%d%%", int(o.body.ScrollPercent()*100+0.5))
	}
	if len(o.items) <= o.listRows() {
		return ""
	}
	return fmt.Sprintf("%d of %d", o.cursor+1, len(o.items))
}

// listRows is how many item rows the list screen can show.
func (o inboxOverlay) listRows() int {
	rows := o.height
	if o.unreadable > 0 {
		rows--
	}
	return max(rows, 1)
}

// layout brings everything size- and state-dependent into agreement: the
// list's scroll offset follows the cursor, and the item screen's foot is
// rebuilt and the body's viewport given the rows that are left.
func (o *inboxOverlay) layout() {
	if o.width < 1 || o.height < 1 {
		return
	}
	if o.cursor >= len(o.items) {
		o.cursor = max(len(o.items)-1, 0)
	}
	rows := o.listRows()
	if o.cursor < o.offset {
		o.offset = o.cursor
	}
	if o.cursor >= o.offset+rows {
		o.offset = o.cursor - rows + 1
	}
	o.offset = max(min(o.offset, len(o.items)-rows), 0)

	if o.screen != inboxScreenItem {
		return
	}
	o.foot = o.footLines(o.width)
	// The title takes a row and the body needs at least one. On a terminal
	// too short for all of it the foot gives way from the top, so the field
	// and the note under it are the last things to go.
	for len(o.foot) > 0 && 2+len(o.foot) > o.height {
		o.foot = o.foot[1:]
	}
	o.body.SetWidth(o.width)
	o.body.SetHeight(max(o.height-1-len(o.foot), 1))
	o.body.SetContent(strings.Join(inboxItemLines(o.item, o.now, o.width), "\n"))
}

// footLines is the item screen under the body: for an open item a blank row,
// what to type, the field and any note; for an answered one only a note.
func (o inboxOverlay) footLines(width int) []string {
	width = max(width, 1)
	var note []string
	if o.note != "" {
		style := theme.dim
		if o.noteKind == inboxNoteProblem {
			style = theme.errorText
		}
		for _, ln := range strings.Split(lipgloss.Wrap(o.note, width, ""), "\n") {
			note = append(note, style.Render(truncate(ln, width)))
		}
	}
	if !o.item.Open() {
		if len(note) == 0 {
			return nil
		}
		return append([]string{""}, note...)
	}
	hint := inboxAnswerHint
	if len(o.item.Options) > 0 {
		hint = inboxAnswerOptionsHint
	}
	foot := []string{"", theme.dim.Render(truncate(hint, width)), truncate(o.input.View(), width)}
	return append(foot, note...)
}

// View draws the screen that is up, every line cut to the content width and
// no more lines than the content height.
func (o inboxOverlay) View() string {
	var lines []string
	if o.screen == inboxScreenItem {
		lines = append(lines, theme.sessionName.Render(truncate(inboxClean(o.item.Title, true), o.width)))
		lines = append(lines, strings.Split(o.body.View(), "\n")...)
		lines = append(lines, o.foot...)
	} else {
		lines = o.listLines()
	}
	if len(lines) > o.height {
		lines = lines[:o.height]
	}
	for i := range lines {
		lines[i] = truncate(lines[i], o.width)
	}
	return strings.Join(lines, "\n")
}

// listLines is the list screen: the rows that fit around the cursor, then a
// line for inbox files that could not be read, so a broken file is not
// silently missing from a list the owner trusts to be whole.
func (o inboxOverlay) listLines() []string {
	var lines []string
	if len(o.items) == 0 {
		lines = append(lines, theme.dim.Render(truncate(inboxEmptyText, o.width)))
	} else {
		cols := inboxColumnsFor(o.items, o.width)
		end := min(o.offset+o.listRows(), len(o.items))
		for i := o.offset; i < end; i++ {
			lines = append(lines, inboxRowLine(o.items[i], cols, o.width, i == o.cursor))
		}
	}
	if o.unreadable > 0 {
		text := fmt.Sprintf("%d inbox files could not be read", o.unreadable)
		if o.unreadable == 1 {
			text = "1 inbox file could not be read"
		}
		lines = append(lines, theme.errorText.Render(truncate(text, o.width)))
	}
	return lines
}

// inboxColumns is the list's column plan for one width: how wide the id, kind
// and task columns are, zero for a column that is left out.
type inboxColumns struct {
	id, kind, task int
}

// inboxMinTitle is the title room below which a column is dropped. Under it
// the title, the one thing that says what the item is, is too short to read.
const inboxMinTitle = 20

// inboxColumnsFor decides which columns the list shows at a width.
//
// The marker, the id and the title always show: the id is what `afk inbox
// answer` takes, and the title is the item. The kind goes first when the
// title would be squeezed, then the task. The task column exists only when
// some item has a task, so a project whose items are all notices does not
// carry an empty column.
func inboxColumnsFor(items []inbox.Item, width int) inboxColumns {
	var c inboxColumns
	for _, it := range items {
		c.id = max(c.id, lipgloss.Width(it.ID))
		c.kind = max(c.kind, lipgloss.Width(it.Kind))
		c.task = max(c.task, lipgloss.Width(it.Task))
	}
	titleRoom := func(c inboxColumns) int {
		room := width - 2 - c.id - len("  answered")
		if c.kind > 0 {
			room -= c.kind + 2
		}
		if c.task > 0 {
			room -= c.task + 2
		}
		return room - 2
	}
	if titleRoom(c) < inboxMinTitle {
		c.kind = 0
	}
	if titleRoom(c) < inboxMinTitle {
		c.task = 0
	}
	return c
}

// inboxRowLine is one list row: a marker for an open item that is holding a
// task up, the id, the kind, the task, the title, and `answered` at the right
// edge for an item that has an answer. The title is cut to what is left; the
// row is never wrapped.
func inboxRowLine(it inbox.Item, cols inboxColumns, width int, selected bool) string {
	marker := " "
	if it.Open() && it.Blocking {
		marker = theme.awaitingFlag.Render("!")
	}
	left := marker + " " + padRight(it.ID, cols.id)
	if cols.kind > 0 {
		left += "  " + theme.dim.Render(padRight(it.Kind, cols.kind))
	}
	if cols.task > 0 {
		left += "  " + padRight(it.Task, cols.task)
	}
	right := ""
	if !it.Open() {
		right = "  " + theme.dim.Render("answered")
	}
	title := inboxClean(it.Title, true)
	if !it.Open() {
		title = theme.dim.Render(title)
	}
	room := width - lipgloss.Width(left) - 2 - lipgloss.Width(right)
	line := left
	if room > 0 {
		line += "  " + truncate(title, room)
	}
	if right != "" {
		if gap := width - lipgloss.Width(line) - lipgloss.Width(right); gap >= 0 {
			line += strings.Repeat(" ", gap) + right
		}
	}
	line = truncate(line, width)
	if selected {
		// spanStyle for the reason the switcher's rows need it: every dim
		// cell ends in a reset that would drop the highlight for the rest of
		// the row.
		return theme.rowSelected.Width(width).Render(spanStyle(line, theme.rowSelected))
	}
	return line
}

func padRight(s string, width int) string {
	if gap := width - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// inboxItemLines is the scrolling part of the item screen, wrapped to width:
// what kind of item it is and where it came from, the body, the options
// numbered from 1, the assumed answer, and the stored answer when there is
// one. It is a pure function of its arguments so the layout is table-testable
// without a Model.
//
// Everything that came from the item's file goes through inboxClean first. A
// failure item's body is the end of a log, and a log holds escape sequences
// and control characters that would repaint parts of the dashboard if they
// reached the terminal as they are.
func inboxItemLines(it inbox.Item, now time.Time, width int) []string {
	if width < 1 {
		width = 1
	}
	wrap := func(s string, style lipgloss.Style, plain bool) []string {
		var out []string
		for _, ln := range strings.Split(lipgloss.Wrap(s, width, ""), "\n") {
			ln = truncate(ln, width)
			if !plain {
				ln = style.Render(ln)
			}
			out = append(out, ln)
		}
		return out
	}

	facts := []string{it.Kind}
	if it.Task != "" {
		facts = append(facts, it.Task)
	}
	if it.Session != "" {
		facts = append(facts, "session "+it.Session)
	}
	if ago := inboxAgo(it.CreatedAt, now); ago != "" {
		facts = append(facts, "raised "+ago)
	}
	switch {
	case !it.Blocking:
		facts = append(facts, "does not hold a task up")
	case it.Open():
		facts = append(facts, "the task waits for your answer")
	default:
		facts = append(facts, "held the task up until it was answered")
	}
	lines := wrap(strings.Join(facts, " · "), theme.dim, false)

	if body := inboxClean(it.Body, false); body != "" {
		lines = append(lines, "")
		lines = append(lines, wrap(body, lipgloss.Style{}, true)...)
	}

	if len(it.Options) > 0 {
		lines = append(lines, "", theme.dim.Render(truncate("Options", width)))
		for i, opt := range it.Options {
			text := inboxClean(opt, true)
			if opt == it.Recommended {
				text += " (recommended)"
			}
			lines = append(lines, inboxHanging(fmt.Sprintf("  %d. ", i+1), text, width)...)
		}
	}

	if assumed := inboxClean(it.Assumed, true); assumed != "" {
		lines = append(lines, "")
		lines = append(lines, inboxHanging("Assumed answer: ", assumed, width)...)
	}

	if !it.Open() {
		lines = append(lines, "")
		lines = append(lines, inboxHanging("Answer: ", inboxClean(it.Answer, true), width)...)
		lines = append(lines, wrap(inboxAnsweredLine(it, now), theme.dim, false)...)
	}
	return lines
}

// inboxAnsweredLine says when and from where the stored answer came. The item
// records the place and the time and nothing about the person, so that is
// what is said.
func inboxAnsweredLine(it inbox.Item, now time.Time) string {
	place := it.AnsweredVia
	switch it.AnsweredVia {
	case inbox.ViaTUI:
		place = "the dashboard"
	case inbox.ViaWeb:
		place = "the web page"
	case inbox.ViaCLI:
		place = "the command line"
	}
	line := "Answered"
	if ago := inboxAgo(it.AnsweredAt, now); ago != "" {
		line += " " + ago
	}
	if place != "" {
		line += " from " + place
	}
	return line + "."
}

// inboxHanging wraps text after a label, with the following lines indented to
// the label's width so a long option still reads as one option.
func inboxHanging(label, text string, width int) []string {
	indent := lipgloss.Width(label)
	if width-indent < 8 {
		// Too narrow to indent: the label goes on the same wrapped run.
		var out []string
		for _, ln := range strings.Split(lipgloss.Wrap(label+text, width, ""), "\n") {
			out = append(out, truncate(ln, width))
		}
		return out
	}
	var out []string
	for i, ln := range strings.Split(lipgloss.Wrap(text, width-indent, ""), "\n") {
		lead := strings.Repeat(" ", indent)
		if i == 0 {
			lead = label
		}
		out = append(out, truncate(lead+ln, width))
	}
	return out
}

// inboxAgo turns an RFC 3339 time from an item's file into "3 hours ago". An
// age and not a clock time, because a clock time needs the reader's time zone
// and this package has none to hand that a test could pin. An empty or
// unreadable time gives "", and the caller leaves the fact out.
func inboxAgo(stamp string, now time.Time) string {
	if stamp == "" {
		return ""
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return ""
	}
	d := now.Sub(at)
	plural := func(n int, unit string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s ago", unit)
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch {
	case d < time.Minute:
		// Also a time slightly in the future, which two clocks that disagree
		// by a few seconds produce.
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/(24*time.Hour)), "day")
}

// inboxClean makes text from an item's file safe to draw: escape sequences
// are removed, tabs become spaces (a tab's width depends on its column, which
// breaks the wrap arithmetic), and every other control character is dropped.
// oneLine also folds line breaks into spaces, for a title or an option.
func inboxClean(s string, oneLine bool) string {
	s = xansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			if oneLine {
				b.WriteByte(' ')
			} else {
				b.WriteByte('\n')
			}
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// --- the Model's side ---

// openInbox is the `i` key: open the overlay on the inbox as the last read
// found it.
//
// It needs a read to have landed. Before the first one there is no telling an
// empty inbox from one not looked at yet, and opening on "nothing in the
// inbox" would be a wrong answer; the notice says to try again, and the next
// read is at most one tick away. A project with no inbox opens and says so.
//
// Nothing else about the dashboard changes: focus, view and the pane are left
// as they were, so closing the overlay returns to exactly what was under it.
func (m Model) openInbox() (tea.Model, tea.Cmd) {
	if !m.buildKnown {
		m.notify(notifyInfo, inboxNotReadNotice)
		return m, nil
	}
	m.inboxOpen = true
	m.inbox = newInboxOverlay(m.build.Items, m.build.Unreadable, m.deps.Now(), 1, 1)
	m.sizeInbox()
	// A toast floats over the top right corner, which is where this box is.
	m.clearStatus()
	return m, nil
}

// openInboxOn opens the overlay on the item with id, as if the owner had moved
// to it and pressed enter. An id the snapshot no longer holds (answered from
// elsewhere between the key and the read) leaves the overlay on its list.
func (m Model) openInboxOn(id string) Model {
	nm, _ := m.openInbox()
	m = nm.(Model)
	if !m.inboxOpen {
		return m
	}
	if i := m.inbox.indexOf(id); i >= 0 {
		m.inbox.cursor = i
		_ = m.inbox.openItem()
		m.sizeInbox()
	}
	return m
}

// inboxSize is the overlay's content size for the current terminal and the
// screen it is on.
//
// The width comes first and by itself: it depends only on the terminal, and
// the height an item wants depends on how its text wraps at that width.
func (m Model) inboxSize() (width, height int) {
	width, _ = modalContentDims(m.width, m.height, inboxModalWidth, inboxMinItemRows)
	return modalContentDims(m.width, m.height, inboxModalWidth, m.inbox.wantRows(width))
}

// sizeInbox applies the current layout to an open overlay, for the reason
// sizeSwitcher exists: View has a value receiver, so sizing applied there
// lands on a copy and is thrown away. It runs from relayout, and after every
// key and every refresh, because the size the overlay wants depends on which
// screen it is on and how many items there are.
func (m *Model) sizeInbox() {
	if !m.inboxOpen || m.width == 0 {
		return
	}
	m.inbox.SetSize(m.inboxSize())
}

// refreshInbox hands an open overlay the snapshot that has just landed.
func (m *Model) refreshInbox() {
	if !m.inboxOpen {
		return
	}
	m.inbox.setItems(m.build.Items, m.build.Unreadable, m.deps.Now())
	m.sizeInbox()
}

// renderInboxModal draws the overlay's box. The keys are on the footer's help
// line (renderHelp), which follows the screen that is up.
func (m Model) renderInboxModal() string {
	width, height := m.inboxSize()
	return renderModalBox(m.inbox.title(), m.inbox.hint(), m.inbox.View(), width, height)
}

// inboxComponentKey is inputInbox's fall-through. Every key reaches the
// overlay, which has no parent rules at all: not the quit chord and not the
// mouse chord. On the item screen each key is either one of the overlay's own
// or text for the answer, and a chord that quit the dashboard from under a
// half-typed answer would lose it. esc and ctrl+g are always a way out, so
// the overlay is not a trap.
func (m Model) inboxComponentKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	cmd, res := m.inbox.Update(k)
	switch res.Kind {
	case inboxClosed:
		m.inboxOpen = false
		return m, cmd
	case inboxSend:
		if m.deps.AnswerInbox == nil {
			// A dashboard built without the seam (a test, an embedding that
			// wired the loader alone) has nowhere to save to. Say so on the
			// item and keep what was typed.
			m.inbox.refused(res.ID, nil, "This dashboard cannot save answers. Run afk inbox answer "+res.ID+" with your answer instead.")
			m.sizeInbox()
			return m, cmd
		}
		// The "saving" note has just appeared under the field.
		m.sizeInbox()
		return m, tea.Batch(cmd, m.answerInboxCmd(res.ID, res.Text, res.Blocking))
	}
	m.sizeInbox()
	return m, cmd
}

// inboxAnsweredMsg carries the outcome of one answer back to Update. gen is
// the project generation the send was started under, so an outcome that
// crosses a project switch is dropped and never reported under another
// project's name.
type inboxAnsweredMsg struct {
	id       string
	blocking bool
	// item is the item as the store holds it: the answered item on success,
	// and the item with the answer that stands when it already had one.
	item inbox.Item
	err  error
	gen  int
}

// answerInboxCmd records an answer off the Update goroutine, on the
// discipline writeLabelCmd documents: a local file write is still I/O, and
// this one also takes the inbox's lock, which another writer may be holding.
//
// The root is the snapshot's, the project's main checkout, and not m.repo:
// the dashboard may have been started inside a session's worktree, and the
// inbox is in the main checkout.
func (m Model) answerInboxCmd(id, text string, blocking bool) tea.Cmd {
	answer := m.deps.AnswerInbox
	root := m.build.Root
	gen := m.projectGen
	return func() tea.Msg {
		it, err := answer(root, id, text)
		return inboxAnsweredMsg{id: id, blocking: blocking, item: it, err: err, gen: gen}
	}
}

// applyInboxAnswered folds one answer's outcome into the Model.
//
// A success goes back to the list and says the task carries on. The dashboard
// does not un-park anything itself: the build loop reads the inbox on its
// next pass and starts the task again, which is why the notice says "within a
// few seconds" and not "now". A fresh read is asked for so the list and the
// Build tab's count are current without waiting for the tick.
//
// A refusal stays on the item and says what happened. When the overlay has
// been closed, or has moved to another item, since the send, the same
// sentence goes out as a notice with the id in it, so an answer that was not
// saved is never silent.
func (m Model) applyInboxAnswered(msg inboxAnsweredMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen {
		return m, nil
	}
	if msg.err == nil {
		if m.inboxOpen {
			m.inbox.answered(msg.item)
			m.sizeInbox()
		}
		text := "Answered " + msg.id + "."
		if msg.blocking {
			text += " The task carries on within a few seconds."
		}
		m.notify(notifyInfo, text)
		return m, m.buildCmd()
	}

	var (
		stored *inbox.Item
		note   string
		toast  string
	)
	switch {
	case errors.Is(msg.err, inbox.ErrAlreadyAnswered):
		stored = &msg.item
		note = inboxAlreadyAnsweredNote
		toast = "Item " + msg.id + " already has an answer, and the first answer is the one that is kept. Nothing was changed."
	case errors.Is(msg.err, inbox.ErrNotFound):
		note = "Item " + msg.id + " is no longer in the inbox, so the answer was not saved."
		toast = note
	case errors.Is(msg.err, inbox.ErrEmptyAnswer):
		note = inboxEmptyAnswerNote
		toast = "The answer to " + msg.id + " was empty, so nothing was saved."
	default:
		note = "The answer to " + msg.id + " could not be saved: " + msg.err.Error() + ". Press enter to try again."
		toast = "The answer to " + msg.id + " could not be saved: " + msg.err.Error() + "."
	}
	onScreen := m.inboxOpen && m.inbox.showing(msg.id)
	if m.inboxOpen {
		m.inbox.refused(msg.id, stored, note)
		m.sizeInbox()
	}
	if !onScreen {
		m.notify(notifyWarn, toast)
	}
	// The inbox on disk is not what the overlay thought it was (the item is
	// answered, or gone), so read it again.
	return m, m.buildCmd()
}
