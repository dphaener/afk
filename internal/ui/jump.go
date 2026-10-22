package ui

// jump.go is the jump search (docs/dash-design.md §4.6, T16.6): `/` at every
// level, the leader then `/` in the Agent. One box over every project,
// session and master-plan task the dashboard has in memory, across every
// project, with what needs the owner first. It replaced the project
// switcher, a list of project names only.
//
// It is shaped like the `:` actions search (leader.go), for that search's
// reasons:
//
//   - The rows are a snapshot taken when the box opens and never refreshed
//     while it is up. The discovery tick fires every two seconds, and a row
//     moving under the highlight would send the owner somewhere they did not
//     aim.
//   - Letters always type into the query, j and k included. A search box
//     whose letters sometimes moved the highlight would make a name starting
//     with j unreachable by typing it.
//   - The key decision is a pure function of the key and the box's state, so
//     every branch is a table row (jump_test.go). The Model only carries out
//     the outcome.
//
// Nothing here reads a file or dials. The rows come from what the Model
// already holds (the project list, the per-project session scans, the
// every-project cache with each project's tasks), built by jumpRows from
// plain facts; the landing on enter is the Model's (jumpLand, model.go).

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/projectrows"
)

// jumpKind is what a row goes to. The order of the constants is the order
// the kinds are listed in after the needs-you rows (§4.6: projects, then
// sessions, then tasks).
type jumpKind int

const (
	jumpProject jumpKind = iota + 1
	jumpSession
	jumpTask
)

// String is the kind's word in the row's kind column.
func (k jumpKind) String() string {
	switch k {
	case jumpProject:
		return "project"
	case jumpSession:
		return "session"
	case jumpTask:
		return "task"
	}
	return "unknown"
}

// jumpField is one text a row can be matched on. at is where the text starts
// in the row's name, in runes, so the matched letters can be underlined
// there; -1 is a text the row matches on but does not show (a session's
// on-disk name behind its label).
type jumpField struct {
	text string
	at   int
}

// jumpRow is one row of the jump search, with everything enter needs to land
// on it copied in, so landing needs no second lookup that could find the
// thing changed or gone.
type jumpRow struct {
	kind jumpKind
	// name is what the row is called: a project's name, a session's label or
	// name, a task's id and title.
	name string
	// fields are the texts the query is matched against, in the order they
	// are tried. A task matches on its id or its title (§4.6), never on a
	// run of letters spread across both.
	fields []jumpField
	// projectPath and projectName are the project the row belongs to (the
	// project itself for a project row). projectPath is the project list's
	// path, the one a switch is made with.
	projectPath, projectName string
	// row is the project's home-screen row, for a project row whose cached
	// read has it (hasRow): enter on a gone or unreadable project says why
	// instead of switching.
	row    projectrows.Row
	hasRow bool
	// session, worktree, scratch, hostRunning and fault are the session's,
	// for a session row: where an agent landing connects, whether there is a
	// live agent to connect to, and whether the row is a worktree fault,
	// which has no agent and is opened on the project view instead.
	session     string
	worktree    string
	scratch     bool
	hostRunning bool
	fault       bool
	// taskID is the task's id, for a task row.
	taskID string
	// stateKind and state are the §3.6 state the row's glyph and word show,
	// through stateLook.
	stateKind stateKind
	state     string
	// needRank is the row's place in the needs-you order (needsYouItems), or
	// -1 when nothing about it needs the owner. Rows with a rank come first,
	// in rank order.
	needRank int
}

// jumpFacts is what the jump rows are built from, all of it already in
// memory. Projects are in home-screen order with their sessions and cached
// row (Model.needsYouProjectsNow, the same facts `]` walks); Tasks are each
// project's master-plan tasks, keyed by the project's path.
type jumpFacts struct {
	Projects []needsYouProject
	Tasks    map[string][]JumpTask
}

// jumpRows builds the search's rows from f and puts them in order: what
// needs the owner first, in the order `]` walks it (needsYouItems), then the
// projects, the sessions and the tasks, each by name with the project path
// as the tie-break, so the order is total and the same every time the box
// opens.
//
// What needs the owner is the same five kinds `]` steps through, each put on
// the row it is about: a waiting or broken session on that session's row, a
// plan to approve or a gone project on the project's row, and a blocking
// inbox item on the row of the task it holds up (the item names the task;
// that task is what the owner is held up on). An inbox item whose task is
// not listed falls back to its project's row, so it is never lost.
//
// A creating… placeholder is left out: it has no session to open yet, and
// every other per-session key ignores it too. A project given twice counts
// once.
func jumpRows(f jumpFacts) []jumpRow {
	var rows []jumpRow
	seen := make(map[string]bool, len(f.Projects))
	for _, p := range f.Projects {
		if seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		pr := jumpRow{
			kind: jumpProject, name: p.Name, fields: []jumpField{{text: p.Name}},
			projectPath: p.Path, projectName: p.Name, row: p.Row, hasRow: p.HasRow,
			stateKind: stateProject, needRank: -1,
		}
		if p.HasRow {
			pr.state = p.Row.Label
		}
		rows = append(rows, pr)
		for _, s := range p.Sessions {
			if s.Pending {
				continue
			}
			shown := displayName(s)
			sr := jumpRow{
				kind: jumpSession, name: shown, fields: []jumpField{{text: shown}},
				projectPath: p.Path, projectName: p.Name,
				session: s.Name, worktree: s.WorktreePath, scratch: s.Scratch,
				hostRunning: s.HostRunning, fault: isWorktreeFault(s.Kind),
				needRank: -1,
			}
			if shown != s.Name {
				sr.fields = append(sr.fields, jumpField{text: s.Name, at: -1})
			}
			sr.stateKind, sr.state = jumpSessionState(s)
			rows = append(rows, sr)
		}
		for _, t := range f.Tasks[p.Path] {
			name := t.ID
			fields := []jumpField{{text: t.ID}}
			if t.Title != "" {
				name += " " + t.Title
				fields = append(fields, jumpField{text: t.Title, at: utf8.RuneCountInString(t.ID) + 1})
			}
			rows = append(rows, jumpRow{
				kind: jumpTask, name: name, fields: fields,
				projectPath: p.Path, projectName: p.Name, taskID: t.ID,
				stateKind: stateProject, state: t.Status, needRank: -1,
			})
		}
	}

	waitTask := make(map[string]string, len(f.Projects))
	for _, p := range f.Projects {
		if p.HasRow {
			if _, ok := waitTask[p.Path]; !ok {
				waitTask[p.Path] = p.Row.Wait.Task
			}
		}
	}
	// mark ranks every row match accepts at rank i, keeping the lowest rank
	// a row is given (a project can be both a plan to approve and gone).
	mark := func(i int, match func(jumpRow) bool) bool {
		found := false
		for j := range rows {
			if !match(rows[j]) {
				continue
			}
			if rows[j].needRank < 0 || i < rows[j].needRank {
				rows[j].needRank = i
			}
			found = true
		}
		return found
	}
	for i, it := range needsYouItems(needsYouFacts{Projects: f.Projects}) {
		projectRow := func(r jumpRow) bool { return r.kind == jumpProject && r.projectPath == it.ProjectPath }
		switch it.Kind {
		case needSession, needBroken:
			mark(i, func(r jumpRow) bool {
				return r.kind == jumpSession && r.projectPath == it.ProjectPath && r.session == it.Session
			})
		case needInbox:
			task := waitTask[it.ProjectPath]
			taskRow := func(r jumpRow) bool {
				return r.kind == jumpTask && r.projectPath == it.ProjectPath && r.taskID == task
			}
			if task == "" || !mark(i, taskRow) {
				mark(i, projectRow)
			}
		default:
			mark(i, projectRow)
		}
	}
	slices.SortStableFunc(rows, jumpCompare)
	return rows
}

// jumpCompare is jumpRows' order: ranked rows first by rank, then kind, then
// name ignoring case, then project path, then the session or task that is
// left to tell two rows apart.
func jumpCompare(a, b jumpRow) int {
	an, bn := a.needRank >= 0, b.needRank >= 0
	if an != bn {
		if an {
			return -1
		}
		return 1
	}
	if an {
		if c := cmp.Compare(a.needRank, b.needRank); c != 0 {
			return c
		}
	}
	return cmp.Or(
		cmp.Compare(a.kind, b.kind),
		cmp.Compare(strings.ToLower(a.name), strings.ToLower(b.name)),
		cmp.Compare(a.projectPath, b.projectPath),
		cmp.Compare(a.session, b.session),
		cmp.Compare(a.taskID, b.taskID),
	)
}

// jumpSessionState is the §3.6 state a session row shows: asking the owner
// when it waits on them, the fault's badge for a worktree fault, and
// otherwise its phase as the Agent's band words it (sessionPhaseKey: a stale
// status is "unknown", a one-off is quiet).
func jumpSessionState(s Session) (stateKind, string) {
	switch {
	case showsAwaitingInput(s):
		return stateSession, awaitingInputState
	case isWorktreeFault(s.Kind):
		return stateFault, faultBadgeLabel(s.Kind)
	}
	return stateSession, sessionPhaseKey(s)
}

// jumpQuery is the query as it is matched: spaces dropped, since a space
// typed between words would otherwise have to match a space in the name, and
// "w1 t1" is as likely a way to look for W1.T1 as "w1t1".
func jumpQuery(q string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, q)
}

// jumpMatch reports whether every rune of query appears in text in order,
// ignoring case (a subsequence match), and at which rune positions of text,
// taking each one as early as it can. An empty query matches with no
// positions. A subsequence rather than the `:` search's substring, because
// names here are typed fast and in part ("afkdsh" for afk-dashboard), and
// the underlined letters show why a row matched.
func jumpMatch(query, text string) ([]int, bool) {
	q := []rune(query)
	if len(q) == 0 {
		return nil, true
	}
	pos := make([]int, 0, len(q))
	i := 0
	for at, r := range []rune(text) {
		if unicode.ToLower(r) == unicode.ToLower(q[i]) {
			pos = append(pos, at)
			i++
			if i == len(q) {
				return pos, true
			}
		}
	}
	return nil, false
}

// jumpHit is a row the query kept and the rune positions of its name to
// underline.
type jumpHit struct {
	row   jumpRow
	marks []int
}

// jumpFilter keeps the rows query matches, in their order, with the letters
// that matched. A row is tried field by field and the first field that
// matches is the one underlined.
func jumpFilter(rows []jumpRow, query string) []jumpHit {
	q := jumpQuery(query)
	out := make([]jumpHit, 0, len(rows))
	for _, r := range rows {
		if q == "" {
			out = append(out, jumpHit{row: r})
			continue
		}
		for _, f := range r.fields {
			pos, ok := jumpMatch(q, f.text)
			if !ok {
				continue
			}
			var marks []int
			if f.at >= 0 {
				marks = make([]int, len(pos))
				for i, p := range pos {
					marks[i] = f.at + p
				}
			}
			out = append(out, jumpHit{row: r, marks: marks})
			break
		}
	}
	return out
}

// jumpOutcomeKind is what one key in the jump search does.
type jumpOutcomeKind int

const (
	// jumpIgnore is the zero value: nothing happens and the box stays open.
	jumpIgnore jumpOutcomeKind = iota
	// jumpGo closes the box and lands on jumpOutcome.row.
	jumpGo
	// jumpType edits the query: appends text, or drops the last character
	// when backspace is set.
	jumpType
	// jumpClear empties the query and keeps the box open.
	jumpClear
	// jumpClose closes the box and does nothing else.
	jumpClose
	// jumpMove moves the highlight by delta, wrapping round when wrap is
	// set and stopping at the ends otherwise.
	jumpMove
)

// String exists for test-failure output; nothing renders it.
func (k jumpOutcomeKind) String() string {
	switch k {
	case jumpIgnore:
		return "ignore"
	case jumpGo:
		return "go"
	case jumpType:
		return "type"
	case jumpClear:
		return "clear"
	case jumpClose:
		return "close"
	case jumpMove:
		return "move"
	}
	return "unknown"
}

// jumpOutcome is jumpKeyDecision's answer. Only the fields its kind names
// are meaningful.
type jumpOutcome struct {
	kind      jumpOutcomeKind
	row       jumpRow
	text      string
	backspace bool
	delta     int
	wrap      bool
}

// jumpKeyDecision decides what key k does in the jump search. query is
// what is typed, hits the rows on screen (already filtered by it),
// highlight the index of the highlighted one and page how many rows the box
// shows, which pgup and pgdn move by.
//
// The rules run in a fixed order:
//
//  1. The leader closes: it is how the box was reached from the Agent, and
//     pressing it again reads as "never mind", as in the `:` search.
//  2. esc clears a typed query first and closes an empty one, so a wrong
//     search is one key from a fresh one and two keys from closed.
//  3. up and down move the highlight and wrap round, so the last row is one
//     up-arrow away; pgup and pgdn move a page and stop at the ends.
//  4. enter goes to the highlighted row. With no row (nothing matches) it
//     does nothing and the box stays open, so the owner can correct the
//     query instead of losing it.
//  5. backspace edits the query.
//  6. Any other key with text types it into the query. Letters always type,
//     j and k included (§4.6, as §4.7 says for the `:` search).
//
// Anything else is ignored. checkLeader refuses any leader with no ctrl or
// alt (leaderconfig.go), so esc, the arrows, the page keys, enter and
// backspace can never be the leader, and testing it first cannot shadow one
// of the box's own keys.
func jumpKeyDecision(k tea.KeyPressMsg, leader, query string, hits []jumpHit, highlight, page int) jumpOutcome {
	s := k.String()
	if s == leader {
		return jumpOutcome{kind: jumpClose}
	}
	page = max(page, 1)
	switch s {
	case "esc":
		if query != "" {
			return jumpOutcome{kind: jumpClear}
		}
		return jumpOutcome{kind: jumpClose}
	case "up":
		return jumpOutcome{kind: jumpMove, delta: -1, wrap: true}
	case "down":
		return jumpOutcome{kind: jumpMove, delta: 1, wrap: true}
	case "pgup":
		return jumpOutcome{kind: jumpMove, delta: -page}
	case "pgdown":
		return jumpOutcome{kind: jumpMove, delta: page}
	case "enter":
		if highlight < 0 || highlight >= len(hits) {
			return jumpOutcome{}
		}
		return jumpOutcome{kind: jumpGo, row: hits[highlight].row}
	case "backspace":
		return jumpOutcome{kind: jumpType, backspace: true}
	}
	if k.Text != "" {
		return jumpOutcome{kind: jumpType, text: k.Text}
	}
	return jumpOutcome{}
}

// jumpDialog is the jump search's state: the rows snapshotted at open, the
// query and the highlight. It never refreshes itself (see the file comment).
type jumpDialog struct {
	rows      []jumpRow
	query     string
	highlight int
	// leader is the Model's leader key, which closes the box when pressed
	// in it.
	leader string
}

func newJumpDialog(rows []jumpRow, leader string) jumpDialog {
	return jumpDialog{rows: rows, leader: leader}
}

// visible is the rows the query keeps, with their underlines.
func (d jumpDialog) visible() []jumpHit {
	return jumpFilter(d.rows, d.query)
}

// decide is jumpKeyDecision against the box's own state, for a box drawn in
// a terminal of width×height (which sets the page).
func (d jumpDialog) decide(k tea.KeyPressMsg, width, height int) jumpOutcome {
	_, _, area := d.dims(width, height)
	return jumpKeyDecision(k, d.leader, d.query, d.visible(), d.highlight, area)
}

// apply carries out the outcomes that change the box itself: type, clear
// and move. The others are the Model's. A query edit puts the highlight back
// on the first row, so it can never index past a list the query just
// shortened, and the first row is the best one (needs-you first).
func (d *jumpDialog) apply(o jumpOutcome) {
	switch o.kind {
	case jumpType:
		if o.backspace {
			if d.query != "" {
				_, size := utf8.DecodeLastRuneInString(d.query)
				d.query = d.query[:len(d.query)-size]
			}
		} else {
			d.query += o.text
		}
		d.highlight = 0
	case jumpClear:
		d.query = ""
		d.highlight = 0
	case jumpMove:
		n := len(d.visible())
		if n == 0 {
			d.highlight = 0
			return
		}
		if o.wrap {
			d.highlight = ((d.highlight+o.delta)%n + n) % n
			return
		}
		d.highlight = clampInt(d.highlight+o.delta, 0, n-1)
	}
}

// jumpDialogWidth is the box's preferred content width: §3.8 gives inbox,
// jump and actions 76–84 columns. On an 80-column terminal modalContentDims
// narrows it and the name column gives way first.
const jumpDialogWidth = 80

// jumpMaxRows is the most rows the box shows before it scrolls. The box is
// sized to the snapshot, not to what the query keeps, so it does not shrink
// and grow under the owner's typing.
const jumpMaxRows = 14

// jumpHint is the box's bottom edge.
const jumpHint = "type to search · enter goes there · esc closes"

// jumpPlaceholder is the query line before anything is typed.
const jumpPlaceholder = "type a project, session or task"

// jumpNoMatch is the line shown when the query keeps no row.
const jumpNoMatch = "nothing matches"

// dims is the box's content size for a terminal of width×height and how many
// rows it shows (the query line and a blank line take two).
func (d jumpDialog) dims(width, height int) (contentW, contentH, area int) {
	contentW, contentH = modalContentDims(width, height, jumpDialogWidth, max(min(len(d.rows), jumpMaxRows), 1)+2)
	return contentW, contentH, max(contentH-2, 1)
}

// view renders the box for a terminal of width×height, ready for
// composeModal. Every line is cut to the content width, so nothing wraps
// past the border, and the rows scroll to keep the highlight on screen.
func (d jumpDialog) view(width, height int) string {
	hits := d.visible()
	contentW, contentH, area := d.dims(width, height)

	query := theme.dim.Render(jumpPlaceholder)
	if d.query != "" {
		query = "> " + d.query
	}
	lines := []string{truncate(query, contentW), ""}

	if len(hits) == 0 {
		lines = append(lines, theme.dim.Render(truncate(jumpNoMatch, contentW)))
	} else {
		cols := jumpColumnsFor(d.rows, contentW)
		start := 0
		if d.highlight >= area {
			start = d.highlight - area + 1
		}
		end := min(start+area, len(hits))
		for i := start; i < end; i++ {
			lines = append(lines, jumpRowLine(hits[i], cols, i == d.highlight))
		}
	}
	return renderModalBox("jump", jumpHint, strings.Join(lines, "\n"), contentW, contentH)
}

// jumpColumns is the width of each column of a row line. Measured over the
// whole snapshot rather than the rows on screen, so the columns do not jump
// about as the query narrows the list.
type jumpColumns struct {
	name, project, kind, state, width int
}

// jumpKindW is the kind column's width: "session", the longest kind word.
const jumpKindW = 7

// jumpColumnsFor sizes the columns for rows in width cells: the project and
// state columns as wide as their widest entry up to a cap, the kind column
// fixed, and the name column the rest. On a narrow box the project and then
// the state column give way so the name keeps at least ten cells.
func jumpColumnsFor(rows []jumpRow, width int) jumpColumns {
	c := jumpColumns{kind: jumpKindW, width: width}
	for _, r := range rows {
		if r.kind != jumpProject {
			c.project = max(c.project, lipgloss.Width(r.projectName))
		}
		c.state = max(c.state, lipgloss.Width(stateLook(r.stateKind, r.state).word))
	}
	c.project = min(c.project, 20)
	c.state = min(c.state, 16)
	// glyph, space, then three two-cell gaps between the four other columns.
	fixed := 2 + 3*2 + c.kind
	c.name = width - fixed - c.project - c.state
	for c.name < 10 && c.project > 6 {
		c.project--
		c.name++
	}
	for c.name < 10 && c.state > 6 {
		c.state--
		c.name++
	}
	c.name = max(c.name, 1)
	return c
}

// jumpMatchStyle marks a matched letter. An underline rather than a colour,
// so it reads on the selected row's fill and in every theme alike.
var jumpMatchStyle = lipgloss.NewStyle().Underline(true)

// jumpNameCell is name cut to width cells with "…" and padded to width, with
// the runes at marks underlined. A mark past the cut is dropped: the letter
// it would underline is not on screen.
func jumpNameCell(name string, marks []int, width int) string {
	runes := []rune(name)
	keep := len(runes)
	cut := false
	if lipgloss.Width(name) > width {
		cut = true
		w := 0
		for i, r := range runes {
			rw := xansi.StringWidth(string(r))
			if w+rw > width-1 {
				keep = i
				break
			}
			w += rw
		}
	}
	marked := make(map[int]bool, len(marks))
	for _, p := range marks {
		marked[p] = true
	}
	var b strings.Builder
	for i := 0; i < keep; i++ {
		if marked[i] {
			b.WriteString(jumpMatchStyle.Render(string(runes[i])))
		} else {
			b.WriteRune(runes[i])
		}
	}
	if cut {
		b.WriteString("…")
	}
	return padRight(b.String(), width)
}

// jumpRowLine is one row: the state's glyph, the name with its matched
// letters underlined, the project (blank for a project row, whose name is
// the project), the kind and the state's word. The selected row is filled
// edge to edge (§3.5); the others carry the state's colour on the glyph and
// the word and dim the project and kind.
func jumpRowLine(h jumpHit, c jumpColumns, selected bool) string {
	r := h.row
	look := stateLook(r.stateKind, r.state)
	project := ""
	if r.kind != jumpProject {
		project = r.projectName
	}
	name := jumpNameCell(r.name, h.marks, c.name)
	proj := padRight(truncate(project, c.project), c.project)
	kind := padRight(r.kind.String(), c.kind)
	word := truncate(look.word, c.state)
	if selected {
		line := look.glyph + " " + name + "  " + proj + "  " + kind + "  " + word
		line = truncate(line, c.width)
		return theme.rowSelected.Width(c.width).Render(spanStyle(line, theme.rowSelected))
	}
	tone := theme.stateStyle(r.stateKind, r.state)
	line := tone.Render(look.glyph) + " " + name + "  " + theme.dim.Render(proj) + "  " +
		theme.dim.Render(kind) + "  " + tone.Render(word)
	return truncate(line, c.width)
}
