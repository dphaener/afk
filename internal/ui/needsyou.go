package ui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dphaener/afk/internal/projectrows"
)

// This file is the pure order of everything that needs the owner, across
// every project (T16.6, docs/decisions.md "## T16.6" A1–A3): which things,
// in what order, how `]` and `[` step through them, and where going to each
// one lands. It holds no Model and does no I/O, so the dashboard can call it
// from View and Update alike, and every branch is a table row in
// needsyou_test.go. The Model builds the facts from what is already in memory
// (the cached home-screen rows and the per-project session scans); a key
// never waits on a read.

// needKind is what kind of thing needs the owner. The zero value is "none",
// so a zero needKey in needHere reads as "no item in view".
type needKind int

const (
	// needSession is a session whose agent is waiting on the owner.
	needSession needKind = iota + 1
	// needInbox is the project's oldest open inbox item that blocks a task.
	needInbox
	// needPlan is a plan waiting for the owner's approval.
	needPlan
	// needBroken is a session the owner has to look at that is not asking
	// anything: a worktree fault, a stale status, a wedged or dead host, a
	// failed run.
	needBroken
	// needGone is a project whose folder is gone or cannot be read.
	needGone
)

// needItem is one thing that needs the owner. The landing fields are copied
// from the session the item came from, so landing on it later needs no
// second lookup that could find the session changed or gone.
type needItem struct {
	Kind        needKind
	ProjectPath string
	ProjectName string
	// Session is the session's name, for needSession and needBroken.
	Session string
	// ItemID is the inbox item's id, for needInbox.
	ItemID string
	// WorktreePath, Scratch and HostRunning are the session's, for the
	// session kinds: where an agent landing connects, and whether there is a
	// live agent to connect to at all (sessionEnterTarget decides).
	WorktreePath string
	Scratch      bool
	HostRunning  bool
}

// key is the item's identity for stepping: the parts that tell two items
// apart. The landing fields are left out because they change between scans
// while the item stays the same thing.
func (it needItem) key() needKey {
	return needKey{Kind: it.Kind, ProjectPath: it.ProjectPath, Session: it.Session, ItemID: it.ItemID}
}

// needKey identifies one needItem. Its zero value means no item.
type needKey struct {
	Kind        needKind
	ProjectPath string
	Session     string
	ItemID      string
}

// needsYouProject is one project as the facts carry it. Row is its
// home-screen row when the cached read has it (HasRow); a project with no
// row yet still contributes its sessions. Sessions are the project's
// sessions: the active project's fresh m.sessions, another project's from
// m.projectSessions.
type needsYouProject struct {
	Path, Name string
	Row        projectrows.Row
	HasRow     bool
	Sessions   []Session
}

// needsYouFacts is everything the order is built from, already in memory.
// Projects are in home-screen order (needsYouProjectOrder builds it).
type needsYouFacts struct {
	Projects []needsYouProject
}

// needsYouProjectOrder puts the projects in the order the home screen draws
// them, so `]` walks the screen the owner sees from top to bottom: the rows
// in allProjectsOrder's order (NEEDS YOU first, then the other groups,
// config order inside each), then any listed project the cached read has no
// row for yet, in the listed order. listed carries each project's path,
// name and sessions; a row whose Root matches a listed project is attached
// to it, and a row with no listed project (the unlisted start-up project)
// still comes in its place, with no sessions.
//
// The rows are copied before sorting: allProjectsOrder sorts in place, and
// the slice belongs to the home screen's cache, whose cursor indexes it.
func needsYouProjectOrder(rows []projectrows.Row, listed []needsYouProject) []needsYouProject {
	byPath := make(map[string]int, len(listed))
	for i, p := range listed {
		if _, dup := byPath[p.Path]; !dup {
			byPath[p.Path] = i
		}
	}
	used := make(map[string]bool, len(listed))
	out := make([]needsYouProject, 0, max(len(rows), len(listed)))
	for _, r := range allProjectsOrder(slices.Clone(rows)) {
		if used[r.Root] {
			continue
		}
		used[r.Root] = true
		p := needsYouProject{Path: r.Root, Name: r.Name}
		if i, ok := byPath[r.Root]; ok {
			p = listed[i]
			if p.Name == "" {
				p.Name = r.Name
			}
		}
		p.Row, p.HasRow = r, true
		out = append(out, p)
	}
	for _, p := range listed {
		if used[p.Path] {
			continue
		}
		used[p.Path] = true
		p.Row, p.HasRow = projectrows.Row{}, false
		out = append(out, p)
	}
	return out
}

// needsYouItems is everything that needs the owner, in the order `]` walks.
//
// Projects come in the facts' order (the home screen's), and inside a
// project: every session waiting on the owner, then the one blocking inbox
// item, then the plan to approve, then every broken session, then the gone
// project. That is the home screen and the project view read top to bottom:
// the sessions list puts asking sessions first, and the panel names one
// blocking item. Only one inbox item per project, the oldest (Row.Wait),
// because the answer screen pages through the rest from there; one item per
// open question would make `]` walk the inbox instead of the projects.
//
// A waiting session is never counted again as broken (sessionGroupOf puts
// both in Needs you; showsAwaitingInput is what tells them apart), and a
// terminal session still carrying a stale awaiting flag is not waiting
// (showsAwaitingInput, status-schema.md C4). A creating… placeholder is
// never counted.
//
// It is total and deterministic: a project path given twice counts once
// (the first), and the sessions are put in screen order here even if the
// caller already did (inScreenOrder is a stable sort by group then name,
// so doing it twice changes nothing), so same input, same order.
func needsYouItems(f needsYouFacts) []needItem {
	var out []needItem
	seen := make(map[string]bool, len(f.Projects))
	for _, p := range f.Projects {
		if seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		// A creating… placeholder is inert (every per-session key ignores
		// it), so it is never something to go to.
		sessions := slices.DeleteFunc(inScreenOrder(p.Sessions), func(s Session) bool { return s.Pending })
		sessionItem := func(kind needKind, s Session) needItem {
			return needItem{
				Kind: kind, ProjectPath: p.Path, ProjectName: p.Name,
				Session: s.Name, WorktreePath: s.WorktreePath,
				Scratch: s.Scratch, HostRunning: s.HostRunning,
			}
		}
		for _, s := range sessions {
			if showsAwaitingInput(s) {
				out = append(out, sessionItem(needSession, s))
			}
		}
		if p.HasRow && p.Row.Wait.ItemID != "" {
			out = append(out, needItem{Kind: needInbox, ProjectPath: p.Path, ProjectName: p.Name, ItemID: p.Row.Wait.ItemID})
		}
		if p.HasRow && p.Row.PlanWaiting {
			out = append(out, needItem{Kind: needPlan, ProjectPath: p.Path, ProjectName: p.Name})
		}
		for _, s := range sessions {
			if !showsAwaitingInput(s) && sessionGroupOf(s) == groupNeedsYou {
				out = append(out, sessionItem(needBroken, s))
			}
		}
		if p.HasRow && (p.Row.State == projectrows.StateGone || p.Row.State == projectrows.StateUnreadable) {
			out = append(out, needItem{Kind: needGone, ProjectPath: p.Path, ProjectName: p.Name})
		}
	}
	return out
}

// needHere is where the owner is when they press `]` or `[`: the project in
// view and, when there is one, the session in view (the Agent's session, else
// the selected session at Project; none at All projects).
//
// At is the item the last `]`/`[` landed on, while the owner is still where
// it landed (the zero key when not). Without it a landing that leaves no
// session in view would loop: a plan to approve lands on the project view,
// where the selected session (if any) is not the plan, so the next `]` would
// fall back to "the first item of this project" and land on the plan again;
// a gone project lands back on All projects with its own row selected, and
// would do the same.
type needHere struct {
	Project string
	Session string
	At      needKey
}

// stepNeedsYou is the item `]` (forward) or `[` goes to from here, false
// only when nothing needs the owner.
//
// An exact match (here.At, else a session item of here's project named
// here.Session) steps to the next or previous item and wraps round. Items
// that share the session name (a worktree fault beside a healthy session of
// the same name) all count as here, since here cannot tell them apart, so
// `]` goes past the last of them and `[` before the first; otherwise `]`
// from one would land on the other, which looks to the owner like nothing
// happened.
//
// With no exact match `]` takes the first item of here's project, and `[`
// the item just before it, so from a project row `]` goes into that
// project's first item; when here's project has no items the position is
// the top of the list. Both wrap.
func stepNeedsYou(items []needItem, here needHere, forward bool) (needItem, bool) {
	n := len(items)
	if n == 0 {
		return needItem{}, false
	}
	first, last := -1, -1
	match := func(i int) {
		if first < 0 {
			first = i
		}
		last = i
	}
	if here.At != (needKey{}) {
		for i, it := range items {
			if it.key() == here.At {
				match(i)
			}
		}
	}
	if first < 0 && here.Session != "" {
		for i, it := range items {
			if it.ProjectPath == here.Project && it.Session == here.Session &&
				(it.Kind == needSession || it.Kind == needBroken) {
				match(i)
			}
		}
	}
	if first >= 0 {
		if forward {
			return items[(last+1)%n], true
		}
		return items[(first-1+n)%n], true
	}
	pos := slices.IndexFunc(items, func(it needItem) bool { return it.ProjectPath == here.Project })
	if pos < 0 {
		pos = 0
	}
	if forward {
		return items[pos], true
	}
	return items[(pos-1+n)%n], true
}

// needPlace is where going to an item takes the owner.
type needPlace int

const (
	// landAgent opens the session's agent. The caller decides with
	// sessionEnterTarget, as enter does, so a session with no live agent
	// opens its details instead of an empty Agent box.
	landAgent needPlace = iota
	// landInbox opens the project with its inbox on the item (T16.7 swaps
	// in the answer screen).
	landInbox
	// landProject opens the project view, with Select selected when set.
	landProject
	// landAllProjects stays on All projects with the project's row selected
	// and its gone or unreadable notice, since the project cannot be opened.
	landAllProjects
)

// needLanding is where an item lands and what is selected there.
type needLanding struct {
	Place   needPlace
	Project string
	// Select is the session to select: the agent's for landAgent, the
	// broken session for landProject; "" for a plan.
	Select string
	// ItemID is the inbox item to open on, for landInbox.
	ItemID string
}

// needLandingFor is where going to item lands (A3). A broken session lands
// on the project view rather than its agent because what is wrong with it is
// on the project view (its row's glyph and its details), and many broken
// sessions have no agent to open.
func needLandingFor(item needItem) needLanding {
	l := needLanding{Project: item.ProjectPath}
	switch item.Kind {
	case needSession:
		l.Place, l.Select = landAgent, item.Session
	case needInbox:
		l.Place, l.ItemID = landInbox, item.ItemID
	case needBroken:
		l.Place, l.Select = landProject, item.Session
	case needGone:
		l.Place = landAllProjects
	default:
		l.Place = landProject
	}
	return l
}

// needLabel is how the Agent's band names item after "next needing you: ".
// A session of the active project is just its name, as the band said before
// T16.6; anything elsewhere names its project, because a bare session name
// could be in any project and the owner would not know `]` leaves this one.
// activePath is the active project's path ("" when none is open).
func needLabel(item needItem, activePath string) string {
	switch item.Kind {
	case needSession, needBroken:
		if item.ProjectPath == activePath {
			return item.Session
		}
		return item.Session + " · " + item.ProjectName
	case needInbox:
		return item.ProjectName + ": answer"
	case needPlan:
		return item.ProjectName + ": plan to approve"
	case needGone:
		return item.ProjectName + ": gone"
	}
	return item.ProjectName
}

// needCountsElsewhere is the top bar's attention count: per project other
// than the active one, how many items need the owner, in the items' order
// (so the project the top bar names first is the one `]` reaches first).
// Projects with nothing are left out, so an empty result means nothing
// elsewhere and the top bar never says "0".
func needCountsElsewhere(items []needItem, activePath string) []otherProjectAwaiting {
	var out []otherProjectAwaiting
	at := make(map[string]int)
	for _, it := range items {
		if it.ProjectPath == activePath {
			continue
		}
		if i, ok := at[it.ProjectPath]; ok {
			out[i].count++
			continue
		}
		at[it.ProjectPath] = len(out)
		out = append(out, otherProjectAwaiting{name: it.ProjectName, count: 1})
	}
	return out
}

// String names a kind for test failures and debugging.
func (k needKind) String() string {
	switch k {
	case needSession:
		return "session"
	case needInbox:
		return "inbox"
	case needPlan:
		return "plan"
	case needBroken:
		return "broken"
	case needGone:
		return "gone"
	}
	return fmt.Sprintf("needKind(%d)", int(k))
}

// String is an item as test failures print it: kind, project and the part
// that tells it apart.
func (it needItem) String() string {
	parts := []string{it.Kind.String(), it.ProjectName}
	if it.Session != "" {
		parts = append(parts, it.Session)
	}
	if it.ItemID != "" {
		parts = append(parts, it.ItemID)
	}
	return strings.Join(parts, "/")
}
