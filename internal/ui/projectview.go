// projectview.go holds the pure decisions behind the Project level's screen
// (docs/dash-design.md §4.2): which group a session is in, the word at the
// right of its row, which build row belongs to it, the lines of the read-only
// pane, how the focused section moves, what enter opens and the band's text.
//
// Every function here is a pure function of its arguments, with no Model, no
// file read and no clock, so the whole matrix is table-tested without a
// terminal (projectview_test.go). The screen that draws them and the Model
// glue its keys run through are projectviewrender.go's.
package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/state"
)

// sessionGroup is the heading a session is listed under.
type sessionGroup int

const (
	groupNeedsYou sessionGroup = iota
	groupWorking
	groupDone
)

// String is the group heading's words.
func (g sessionGroup) String() string {
	switch g {
	case groupNeedsYou:
		return "Needs you"
	case groupDone:
		return "Done"
	}
	return "Working"
}

// sessionGroupRun is one non-empty group with its sessions in screen order.
type sessionGroupRun struct {
	Group    sessionGroup
	Sessions []Session
}

// sessionGroupOf decides a session's group. Needs you is anything the owner
// has to look at: an agent asking a question (showsAwaitingInput, not the raw
// flag, because a terminal phase carrying it is stale), a fault, or a failed
// run. The faults are the ones the row's glyph already draws as faults (a
// worktree fault Kind, sessionGlyphState), a stale status.json, a wedged
// host, and a host that was started and has since died while the work was
// unfinished.
//
// The dead host is told from "never had one" by HostPID: a standalone
// /afk:go session has no host.json at all (HostPID 0) and is not broken, so
// reading "no host" as a fault would put it under Needs you for ever. A
// session the daemon paused for usage is stopped on purpose, so it stays in
// Working too.
func sessionGroupOf(s Session) sessionGroup {
	if s.Pending {
		return groupWorking
	}
	if showsAwaitingInput(s) {
		return groupNeedsYou
	}
	if isWorktreeFault(s.Kind) {
		return groupNeedsYou
	}
	if s.Scratch {
		return groupWorking
	}
	if s.StatusStale {
		return groupNeedsYou
	}
	if _, wedged := showsWedged(s, time.Time{}); wedged {
		return groupNeedsYou
	}
	switch {
	case s.Phase == state.PhaseFailed:
		return groupNeedsYou
	case s.Phase == state.PhaseDone:
		return groupDone
	}
	if s.HostPID != 0 && !s.HostRunning && !usagePaused(s) {
		return groupNeedsYou
	}
	return groupWorking
}

// groupSessions returns the sessions grouped in screen order: Needs you,
// Working, Done, each in name order, empty groups left out. The result is
// the order the cursor walks, so the list must be laid out from it.
func groupSessions(ss []Session) []sessionGroupRun {
	var by [3][]Session
	for _, s := range ss {
		g := sessionGroupOf(s)
		by[g] = append(by[g], s)
	}
	var out []sessionGroupRun
	for g := range by {
		if len(by[g]) == 0 {
			continue
		}
		slices.SortStableFunc(by[g], func(a, b Session) int { return strings.Compare(a.Name, b.Name) })
		out = append(out, sessionGroupRun{Group: sessionGroup(g), Sessions: by[g]})
	}
	return out
}

// sessionRightWord is the word at the right of a session's row: `asks you`
// when the agent is waiting on the owner, `open it` when its host is running
// so enter will open the agent, and nothing otherwise.
func sessionRightWord(s Session) string {
	switch {
	case showsAwaitingInput(s):
		return "asks you"
	case s.HostRunning:
		return "open it"
	}
	return ""
}

// linkedTaskFor is the build row that belongs to a session: the first row in
// plan order whose ledger session is the name. A row still in play (running,
// shipping or parked) wins over a finished one, because a session that ran
// an earlier task and now runs another is about the second. ok is false for
// an empty name or when no row names it.
func linkedTaskFor(session string, rows []buildview.Row) (index int, ok bool) {
	if session == "" {
		return 0, false
	}
	found := -1
	for i, r := range rows {
		if r.Session != session {
			continue
		}
		switch ledger.Status(r.Status) {
		case ledger.Running, ledger.Shipping, ledger.Parked:
			return i, true
		}
		if found < 0 {
			found = i
		}
	}
	if found < 0 {
		return 0, false
	}
	return found, true
}

// summaryLine is one line of the read-only pane: a label, its value and the
// tone the value is drawn in.
type summaryLine struct {
	Label string
	Value string
	Tone  stateTone
}

// sessionSummaryLines are the read-only pane's lines for the selected
// session: branch, phase and loop, task, the question, diff, verdict and
// host. A line with no value is left out, so a one-off session shows only
// what it has instead of rows of dashes.
//
// The question is not in status.json (it carries only the awaiting_input
// flag), so it comes from the inbox: the oldest open item that names the
// session, else the oldest that names the session's linked task.
func sessionSummaryLines(s Session, linked *buildview.Row, items []inbox.Item) []summaryLine {
	var out []summaryLine
	add := func(label, value string, tone stateTone) {
		if strings.TrimSpace(value) != "" {
			out = append(out, summaryLine{Label: label, Value: value, Tone: tone})
		}
	}
	add("branch", s.Branch, toneQuiet)
	if !s.Scratch && !s.StatusStale && s.Phase != "" {
		phase := string(s.Phase)
		if s.LoopCount > 0 {
			phase += fmt.Sprintf(" · loop %d", s.LoopCount)
		}
		tone := toneWork
		switch sessionGroupOf(s) {
		case groupDone:
			tone = toneDone
		case groupNeedsYou:
			tone = toneNeed
		}
		add("phase", phase, tone)
	}
	if linked != nil {
		task := linked.ID
		if linked.Title != "" {
			task += " " + linked.Title
		}
		add("task", task, toneQuiet)
	}
	if q := sessionQuestion(s, linked, items); q != nil {
		v := q.Title
		if q.Body != "" {
			if v != "" {
				v += " — "
			}
			v += q.Body
		}
		add("need", v, toneNeed)
	}
	if !s.Scratch && !s.StatusStale && s.BaseBranch != "" {
		d := s.DiffStats
		switch {
		case d.BaseMissing:
			add("diff", "base branch "+s.BaseBranch+" no longer exists", toneBad)
		case d.Err != "":
			add("diff", "error: "+d.Err, toneBad)
		default:
			add("diff", fmt.Sprintf("+%d/-%d · %d file(s) vs %s", d.Added, d.Removed, d.Files, s.BaseBranch), toneQuiet)
		}
	}
	switch {
	case s.VerdictError != "":
		add("verdict", "parse error: "+s.VerdictError, toneBad)
	case s.Verdict != nil:
		tone := toneDone
		if s.Verdict.Verdict != state.VerdictApprove {
			tone = toneBad
		}
		add("verdict", string(s.Verdict.Verdict), tone)
	}
	if !s.Pending {
		if s.HostRunning {
			add("host", "running", toneWork)
		} else {
			add("host", "not running", toneQuiet)
		}
	}
	return out
}

// sessionQuestion finds the open inbox item that is this session's question.
// Items arrive in the store's order, which is not age order, so the oldest is
// picked by creation time (RFC 3339 text compares in time order); an equal
// time keeps the earlier item.
func sessionQuestion(s Session, linked *buildview.Row, items []inbox.Item) *inbox.Item {
	oldest := func(match func(inbox.Item) bool) *inbox.Item {
		var best *inbox.Item
		for i := range items {
			it := &items[i]
			if !it.Open() || !match(*it) {
				continue
			}
			if best == nil || it.CreatedAt < best.CreatedAt {
				best = it
			}
		}
		return best
	}
	if s.Name != "" {
		if it := oldest(func(it inbox.Item) bool { return it.Session == s.Name }); it != nil {
			return it
		}
	}
	if linked != nil && linked.ID != "" {
		return oldest(func(it inbox.Item) bool { return it.Task == linked.ID })
	}
	return nil
}

// projectSection is one of the project view's three numbered sections.
type projectSection int

const (
	sectionSessions projectSection = iota + 1
	sectionBuild
	sectionInbox
)

// nextSection is the section after key: tab forward and shift+tab back, both
// wrapping, and 1, 2 and 3 straight to a section. Any other key leaves the
// focus where it is.
func nextSection(cur projectSection, key string) projectSection {
	switch key {
	case "tab":
		return cur%sectionInbox + 1
	case "shift+tab":
		return (cur+sectionInbox-2)%sectionInbox + 1
	case "1":
		return sectionSessions
	case "2":
		return sectionBuild
	case "3":
		return sectionInbox
	}
	return cur
}

// enterTarget is what enter opens at the Project level.
type enterTarget int

const (
	enterNothing enterTarget = iota
	enterAgent
	enterTask
	enterItem
)

// projectEnterTarget maps the focused section and whether it has a selection
// to what enter opens: Sessions the agent, Build the task's details, Inbox
// the item. An empty section opens nothing.
func projectEnterTarget(sec projectSection, haveSession, haveTask, haveItem bool) enterTarget {
	switch {
	case sec == sectionSessions && haveSession:
		return enterAgent
	case sec == sectionBuild && haveTask:
		return enterTask
	case sec == sectionInbox && haveItem:
		return enterItem
	}
	return enterNothing
}

// sessionOpens is what enter on a session opens: its Agent, or its details
// overlay over the Project.
type sessionOpens int

const (
	opensAgent sessionOpens = iota
	opensDetails
)

// sessionEnterFacts are the plain facts sessionEnterTarget decides from. The
// Model reads them off the selected row and its own connection state
// (Model.sessionEnterFacts); nothing here looks at a host.
type sessionEnterFacts struct {
	// hostRunning is the row's (advisory) live-host flag.
	hostRunning bool
	// pending, cleaning and worktreeFault are the rows that have no agent
	// whatever the host flag says: a create still in flight, a clean in
	// flight, a worktree with no session of its own.
	pending, cleaning, worktreeFault bool
	// restarting is a host restart this dashboard dispatched for the row and
	// has not heard back from; dialing an attach it has outstanding for it.
	restarting, dialing bool
	// pane is a pane installed for the row.
	pane bool
}

// sessionEnterTarget is what enter on a session opens (docs/dash-design.md
// §4.3, last paragraph). A session with an agent to show opens its Agent: a
// live host, a restart or a dial of ours under way (the Agent's placeholder
// says "restarting…" or "connecting…" until the pane lands), or a pane
// already installed. Everything else (stopped, pending, cleaning, a worktree
// fault) has no agent, and an Agent for it would be an empty box, so it
// opens the session's details, where the facts about it are. Enter never
// restarts a host: `r` in those details is the restart (restartSelected).
//
// The pane row is checked first because an installed pane is drawn whatever
// discovery says about the host (HostRunning is advisory, and a host that has
// just exited still has its last screen in the pane). The no-agent kinds come
// before the restart and dial rows because restartSelected and connect refuse
// to start either for them, so a restart or dial seen with one of them is a
// stale record and not an agent on its way.
func sessionEnterTarget(f sessionEnterFacts) sessionOpens {
	switch {
	case f.pane:
		return opensAgent
	case f.pending, f.cleaning, f.worktreeFault:
		return opensDetails
	case f.hostRunning, f.restarting, f.dialing:
		return opensAgent
	}
	return opensDetails
}

// projectBandInput is what the band sums up. State is the project's state in
// words as the home screen already shows it.
type projectBandInput struct {
	State string
	Build buildview.Snapshot
}

// projectBandText is the band under the Project strip: the project's state,
// tasks done of total, the build's state, and the inbox counts and open pull
// requests. Open pull requests are the build rows in shipping, so no GitHub
// read is needed. A part with nothing to say is left out.
func projectBandText(in projectBandInput) string {
	b := in.Build
	var parts []string
	if in.State != "" {
		parts = append(parts, in.State)
	}
	if b.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d tasks done", b.Done, b.Total))
	}
	switch {
	case b.Stopped:
		parts = append(parts, "build stopped")
	case b.Paused:
		parts = append(parts, "build paused")
	case b.Current != "":
		parts = append(parts, "running "+b.Current)
	}
	open, blocking := 0, 0
	for _, it := range b.Items {
		if it.Open() {
			open++
			if it.Blocking {
				blocking++
			}
		}
	}
	if open > 0 {
		parts = append(parts, fmt.Sprintf("%d open · %d blocking in inbox", open, blocking))
	}
	prs := 0
	for _, r := range b.Rows {
		if ledger.Status(r.Status) == ledger.Shipping {
			prs++
		}
	}
	if prs > 0 {
		parts = append(parts, fmt.Sprintf("%d pull request(s) open", prs))
	}
	return strings.Join(parts, "  ·  ")
}

// screenOrderIndex is the order the Sessions section draws ss in, as indices
// into ss: groupSessions' order (Needs you, Working, Done; name order inside a
// group), with sessions of equal group and name kept in the order they came
// in. The model stores its sessions in this order (Model.rebuildListItems),
// so the list's cursor walks the rows exactly as the screen shows them and a
// move down from the last Needs you row lands on the first Working one.
//
// It returns indices rather than the sessions so the caller can carry its
// selection across the reorder: the session that was selected stays selected
// when a refresh moves it to another group. The stable sort is what keeps a
// worktree fault ahead of the session that shares its name, the order
// discovery gives them (sortSessionStates).
func screenOrderIndex(ss []Session) []int {
	idx := make([]int, len(ss))
	groups := make([]sessionGroup, len(ss))
	for i := range ss {
		idx[i] = i
		groups[i] = sessionGroupOf(ss[i])
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		if groups[a] != groups[b] {
			return int(groups[a]) - int(groups[b])
		}
		return strings.Compare(ss[a].Name, ss[b].Name)
	})
	return idx
}

// inScreenOrder is ss reordered by screenOrderIndex, in a new slice.
func inScreenOrder(ss []Session) []Session {
	out := make([]Session, 0, len(ss))
	for _, i := range screenOrderIndex(ss) {
		out = append(out, ss[i])
	}
	return out
}

// screenInsertAt is where a new row goes in ss, a list already in screen
// order: before the first row that sorts after it. A creating… placeholder is
// spliced in with it, so it appears where the session will be drawn once
// discovery finds it (in Working, by name) and the selection index can be
// shifted by exactly one.
func screenInsertAt(ss []Session, row Session) int {
	g := sessionGroupOf(row)
	for i := range ss {
		gi := sessionGroupOf(ss[i])
		if gi > g || (gi == g && ss[i].Name > row.Name) {
			return i
		}
	}
	return len(ss)
}

// openInboxItems is what the Inbox section lists: the open items, in the
// order the build snapshot holds them (blocking first, as the reader sorts
// them). An answered item has nothing left to do, so it is the inbox
// overlay's to show, not the section's.
func openInboxItems(items []inbox.Item) []inbox.Item {
	var out []inbox.Item
	for _, it := range items {
		if it.Open() {
			out = append(out, it)
		}
	}
	return out
}

// landInboxCursor is where the Inbox section's cursor goes when a new read
// arrives, by landBuildCursor's rule: on the same item wherever it moved, on
// the row now at the old index (clamped) when the item is gone, and on the
// first row with no previous item. An empty list returns no id and index 0.
func landInboxCursor(prevID string, prevIndex int, items []inbox.Item) (id string, index int) {
	if len(items) == 0 {
		return "", 0
	}
	if prevID == "" {
		return items[0].ID, 0
	}
	for i, it := range items {
		if it.ID == prevID {
			return prevID, i
		}
	}
	i := clampInt(prevIndex, 0, len(items)-1)
	return items[i].ID, i
}

// sectionKeyFor is the key nextSection reads for a section action, so a `:`
// row (which runs the action with no key pressed) moves the focus exactly as
// the key does. Any other action is "", which nextSection leaves alone.
func sectionKeyFor(a modeAction) string {
	switch a {
	case actionSectionNext:
		return "tab"
	case actionSectionPrev:
		return "shift+tab"
	case actionSectionSessions:
		return "1"
	case actionSectionBuild:
		return "2"
	case actionSectionInbox:
		return "3"
	}
	return ""
}
