package ui

import (
	"github.com/dphaener/afk/internal/project"
	"reflect"
	"slices"
	"testing"

	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

// waitingSess is a live session whose agent asks the owner something.
func waitingSess(name string) Session {
	s := sess(name)
	s.AwaitingInput = true
	s.HostRunning = true
	s.HostPID = 100
	return s
}

// workingSess is a live session that needs nothing.
func workingSess(name string) Session {
	s := sess(name)
	s.HostRunning = true
	s.HostPID = 100
	return s
}

// itemStrings is items as their String forms, for readable comparisons.
func itemStrings(items []needItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.String())
	}
	return out
}

// TestNeedsYouItems pins what counts as needing the owner and in what order.
//
// Load-bearing rows: "kinds in project order" (the in-project order waiting,
// inbox, plan, broken, gone is what makes `]` read the screen top to bottom),
// "project order is the given order" (the order must be the home screen's,
// not name order, or `]` would jump around the screen), "waiting session not
// also broken" (sessionGroupOf puts a waiting session in Needs you too, so
// without the guard it would be stepped to twice), and "terminal session with
// stale awaiting flag" (status-schema.md C4: a done or failed run carrying
// the flag is stale, and counting it would leave a finished session asking
// for ever). The one-row-per-kind cases make sure each broken cause in
// sessionGroupOf reaches the order.
func TestNeedsYouItems(t *testing.T) {
	staleWait := sess("old")
	staleWait.AwaitingInput = true
	staleWait.Phase = state.PhaseDone

	failed := sess("failed")
	failed.Phase = state.PhaseFailed
	// Waiting and also stale: sessionGroupOf puts it in Needs you for both
	// reasons, so it is the row that would be counted twice.
	failedAndWaiting := waitingSess("asks")
	failedAndWaiting.StatusStale = true

	stale := sess("stale")
	stale.StatusStale = true

	dead := sess("dead")
	dead.HostPID = 42
	dead.HostRunning = false

	fault := sess("fault")
	fault.Kind = session.KindWorktreeMissing

	standalone := sess("solo") // HostPID 0: a standalone /afk:go, not broken

	pending := Session{SessionState: sampleState("making")}
	pending.Pending = true
	pending.AwaitingInput = true

	row := func(name string, mut func(*projectrows.Row)) projectrows.Row {
		r := projectrows.Row{Name: name, Root: "/p/" + name, State: projectrows.StateRunning}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	proj := func(name string, r *projectrows.Row, ss ...Session) needsYouProject {
		p := needsYouProject{Path: "/p/" + name, Name: name, Sessions: ss}
		if r != nil {
			p.Row, p.HasRow = *r, true
		}
		return p
	}
	ptr := func(r projectrows.Row) *projectrows.Row { return &r }

	cases := []struct {
		name string
		f    needsYouFacts
		want []string
	}{
		{
			name: "nothing needs you",
			f:    needsYouFacts{Projects: []needsYouProject{proj("a", ptr(row("a", nil)), workingSess("w"), standalone)}},
			want: []string{},
		},
		{
			name: "waiting session",
			f:    needsYouFacts{Projects: []needsYouProject{proj("a", nil, waitingSess("s1"))}},
			want: []string{"session/a/s1"},
		},
		{
			name: "blocking inbox item",
			f: needsYouFacts{Projects: []needsYouProject{proj("a", ptr(row("a", func(r *projectrows.Row) {
				r.Wait = projectrows.Wait{ItemID: "q1"}
			})))}},
			want: []string{"inbox/a/q1"},
		},
		{
			name: "plan to approve",
			f: needsYouFacts{Projects: []needsYouProject{proj("a", ptr(row("a", func(r *projectrows.Row) {
				r.PlanWaiting = true
			})))}},
			want: []string{"plan/a"},
		},
		{
			name: "broken sessions, one per cause",
			f:    needsYouFacts{Projects: []needsYouProject{proj("a", nil, failed, stale, dead, fault, workingSess("ok"))}},
			want: []string{"broken/a/dead", "broken/a/failed", "broken/a/fault", "broken/a/stale"},
		},
		{
			name: "gone project",
			f: needsYouFacts{Projects: []needsYouProject{proj("a", ptr(row("a", func(r *projectrows.Row) {
				r.State = projectrows.StateGone
			})))}},
			want: []string{"gone/a"},
		},
		{
			name: "unreadable project counts as gone",
			f: needsYouFacts{Projects: []needsYouProject{proj("a", ptr(row("a", func(r *projectrows.Row) {
				r.State = projectrows.StateUnreadable
			})))}},
			want: []string{"gone/a"},
		},
		{
			name: "kinds in project order",
			f: needsYouFacts{Projects: []needsYouProject{proj("a", ptr(row("a", func(r *projectrows.Row) {
				r.State = projectrows.StateUnreadable
				r.PlanWaiting = true
				r.Wait = projectrows.Wait{ItemID: "q1"}
			})), failed, waitingSess("zed"), waitingSess("amy"))}},
			want: []string{"session/a/amy", "session/a/zed", "inbox/a/q1", "plan/a", "broken/a/failed", "gone/a"},
		},
		{
			name: "project order is the given order",
			f: needsYouFacts{Projects: []needsYouProject{
				proj("zeta", nil, waitingSess("z1")),
				proj("alpha", ptr(row("alpha", func(r *projectrows.Row) { r.PlanWaiting = true })), waitingSess("a1")),
				proj("mid", nil, failed),
			}},
			want: []string{"session/zeta/z1", "session/alpha/a1", "plan/alpha", "broken/mid/failed"},
		},
		{
			name: "waiting session not also broken",
			f:    needsYouFacts{Projects: []needsYouProject{proj("a", nil, failedAndWaiting)}},
			want: []string{"session/a/asks"},
		},
		{
			name: "terminal session with stale awaiting flag",
			f:    needsYouFacts{Projects: []needsYouProject{proj("a", nil, staleWait)}},
			want: []string{},
		},
		{
			name: "pending placeholder never counts",
			f:    needsYouFacts{Projects: []needsYouProject{proj("a", nil, pending)}},
			want: []string{},
		},
		{
			name: "no row: no inbox, plan or gone item",
			f:    needsYouFacts{Projects: []needsYouProject{proj("a", nil, waitingSess("s"))}},
			want: []string{"session/a/s"},
		},
		{
			name: "a project given twice counts once",
			f: needsYouFacts{Projects: []needsYouProject{
				proj("a", nil, waitingSess("s")),
				proj("b", nil, waitingSess("t")),
				proj("a", nil, waitingSess("s"), waitingSess("u")),
			}},
			want: []string{"session/a/s", "session/b/t"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := itemStrings(needsYouItems(tc.f))
			if !slices.Equal(got, tc.want) {
				t.Errorf("needsYouItems = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("session items carry what the landing needs", func(t *testing.T) {
		s := waitingSess("s1")
		s.WorktreePath = "/wt/elsewhere"
		s.Scratch = true
		items := needsYouItems(needsYouFacts{Projects: []needsYouProject{{Path: "/p/a", Name: "a", Sessions: []Session{s}}}})
		want := needItem{Kind: needSession, ProjectPath: "/p/a", ProjectName: "a", Session: "s1",
			WorktreePath: "/wt/elsewhere", Scratch: true, HostRunning: true}
		if len(items) != 1 || items[0] != want {
			t.Errorf("items = %+v, want [%+v]", items, want)
		}
	})
}

// TestNeedsYouProjectOrder pins the facts' project order: the home screen's.
//
// Load-bearing rows: "needs-you rows first, then config order" (the order
// `]` walks must be the one allProjectsOrder draws), "listed project with no
// row goes after the rows" (a project the cached read has not reached yet
// still has its sessions counted, in the listed order), and "rows are not
// sorted in place" (the rows belong to the home screen's cache, whose cursor
// indexes them).
func TestNeedsYouProjectOrder(t *testing.T) {
	rows := []projectrows.Row{
		{Name: "a", Root: "/p/a", Order: 0, State: projectrows.StateRunning},
		{Name: "b", Root: "/p/b", Order: 1, State: projectrows.StateRunning, NeedsOwner: true},
		{Name: "c", Root: "/p/c", Order: 2, State: projectrows.StateFinished},
		{Name: "solo", Root: "/p/solo", Order: 3, State: projectrows.StateRunning},
	}
	listed := []needsYouProject{
		{Path: "/p/a", Name: "a", Sessions: []Session{waitingSess("as")}},
		{Path: "/p/new", Name: "new"},
		{Path: "/p/b", Name: "b"},
		{Path: "/p/c", Name: "c"},
		{Path: "/p/late", Name: "late"},
	}
	cases := []struct {
		name   string
		rows   []projectrows.Row
		listed []needsYouProject
		want   []string
	}{
		{name: "needs-you rows first, then config order", rows: rows, listed: listed,
			want: []string{"b", "a", "solo", "c", "new", "late"}},
		{name: "listed project with no row goes after the rows", rows: nil, listed: listed,
			want: []string{"a", "new", "b", "c", "late"}},
		{name: "a row with no listed project still comes", rows: rows[3:], listed: nil,
			want: []string{"solo"}},
		{name: "nothing", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needsYouProjectOrder(tc.rows, tc.listed)
			names := make([]string, 0, len(got))
			for _, p := range got {
				names = append(names, p.Name)
			}
			if !slices.Equal(names, tc.want) {
				t.Errorf("order = %q, want %q", names, tc.want)
			}
		})
	}

	t.Run("rows are not sorted in place", func(t *testing.T) {
		in := slices.Clone(rows)
		needsYouProjectOrder(in, listed)
		if !slices.Equal(in, rows) {
			t.Errorf("rows were reordered: %v", in)
		}
	})
	t.Run("a listed project keeps its sessions and gets its row", func(t *testing.T) {
		got := needsYouProjectOrder(rows, listed)
		i := slices.IndexFunc(got, func(p needsYouProject) bool { return p.Path == "/p/a" })
		if i < 0 || !got[i].HasRow || got[i].Row.Root != "/p/a" || len(got[i].Sessions) != 1 {
			t.Errorf("project a = %+v, want its row and its one session", got)
		}
		j := slices.IndexFunc(got, func(p needsYouProject) bool { return p.Path == "/p/new" })
		if j < 0 || got[j].HasRow {
			t.Errorf("project new = %+v, want no row", got)
		}
	})
}

// TestStepNeedsYou pins where `]` and `[` go.
//
// Load-bearing rows: the two "acceptance" rows (two projects each with one
// waiting session: from A's row `]` is A's session, from A's session `]` is
// B's; this is the T16.6 acceptance path), the wrap rows both ways (the list
// is a ring, so `]` past the end and `[` before the start must not stop),
// "at a landed plan" and "at a landed gone project" (without needHere.At the
// next `]` would land on the same item again for ever), "same-named fault
// and session" (both are here, so `]` must go past both), and "empty list"
// (the caller says nothing needs you).
func TestStepNeedsYou(t *testing.T) {
	a := needItem{Kind: needSession, ProjectPath: "/p/a", ProjectName: "a", Session: "s1"}
	b := needItem{Kind: needSession, ProjectPath: "/p/b", ProjectName: "b", Session: "s2"}
	acceptance := []needItem{a, b}

	ap := needItem{Kind: needPlan, ProjectPath: "/p/a", ProjectName: "a"}
	bi := needItem{Kind: needInbox, ProjectPath: "/p/b", ProjectName: "b", ItemID: "q1"}
	bx := needItem{Kind: needBroken, ProjectPath: "/p/b", ProjectName: "b", Session: "x"}
	cs := needItem{Kind: needSession, ProjectPath: "/p/c", ProjectName: "c", Session: "s3"}
	cg := needItem{Kind: needGone, ProjectPath: "/p/c", ProjectName: "c"}
	many := []needItem{a, ap, b, bi, bx, cs, cg}

	dupWait := needItem{Kind: needSession, ProjectPath: "/p/b", ProjectName: "b", Session: "dup"}
	dupFault := needItem{Kind: needBroken, ProjectPath: "/p/b", ProjectName: "b", Session: "dup"}
	dups := []needItem{a, dupWait, dupFault, cs}

	cases := []struct {
		name    string
		items   []needItem
		here    needHere
		forward bool
		want    needItem
		wantOK  bool
	}{
		{name: "acceptance: from A's row ] is A's session", items: acceptance,
			here: needHere{Project: "/p/a"}, forward: true, want: a, wantOK: true},
		{name: "acceptance: from A's session ] is B's", items: acceptance,
			here: needHere{Project: "/p/a", Session: "s1"}, forward: true, want: b, wantOK: true},
		{name: "exact match forward", items: many,
			here: needHere{Project: "/p/b", Session: "s2"}, forward: true, want: bi, wantOK: true},
		{name: "exact match back", items: many,
			here: needHere{Project: "/p/b", Session: "s2"}, forward: false, want: ap, wantOK: true},
		{name: "exact match on a broken session", items: many,
			here: needHere{Project: "/p/b", Session: "x"}, forward: true, want: cs, wantOK: true},
		{name: "wrap forward past the end", items: many,
			here: needHere{Project: "/p/c", At: cg.key()}, forward: true, want: a, wantOK: true},
		{name: "wrap back before the start", items: many,
			here: needHere{Project: "/p/a", Session: "s1"}, forward: false, want: cg, wantOK: true},
		{name: "acceptance wraps from B's session to A's", items: acceptance,
			here: needHere{Project: "/p/b", Session: "s2"}, forward: true, want: a, wantOK: true},
		{name: "no match forward: first item of here's project", items: many,
			here: needHere{Project: "/p/b", Session: "quiet"}, forward: true, want: b, wantOK: true},
		{name: "no match back: the item before here's project", items: many,
			here: needHere{Project: "/p/b"}, forward: false, want: ap, wantOK: true},
		{name: "no match back from the first project wraps", items: many,
			here: needHere{Project: "/p/a"}, forward: false, want: cg, wantOK: true},
		{name: "project with no items forward: the first item", items: many,
			here: needHere{Project: "/p/quiet"}, forward: true, want: a, wantOK: true},
		{name: "project with no items back: the last item", items: many,
			here: needHere{Project: "/p/quiet"}, forward: false, want: cg, wantOK: true},
		{name: "a session name in another project is no match", items: many,
			here: needHere{Project: "/p/c", Session: "s1"}, forward: true, want: cs, wantOK: true},
		{name: "at a landed plan", items: many,
			here: needHere{Project: "/p/a", Session: "quiet", At: ap.key()}, forward: true, want: b, wantOK: true},
		{name: "at a landed gone project", items: many,
			here: needHere{Project: "/p/c", At: cg.key()}, forward: false, want: cs, wantOK: true},
		{name: "a stale At falls back to the session", items: many,
			here: needHere{Project: "/p/b", Session: "s2", At: needKey{Kind: needPlan, ProjectPath: "/p/b"}}, forward: true, want: bi, wantOK: true},
		{name: "same-named fault and session forward", items: dups,
			here: needHere{Project: "/p/b", Session: "dup"}, forward: true, want: cs, wantOK: true},
		{name: "same-named fault and session back", items: dups,
			here: needHere{Project: "/p/b", Session: "dup"}, forward: false, want: a, wantOK: true},
		{name: "one item steps to itself", items: []needItem{a},
			here: needHere{Project: "/p/a", Session: "s1"}, forward: true, want: a, wantOK: true},
		{name: "empty list forward", items: nil, here: needHere{Project: "/p/a"}, forward: true},
		{name: "empty list back", items: []needItem{}, here: needHere{Project: "/p/a", Session: "s1"}, forward: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := stepNeedsYou(tc.items, tc.here, tc.forward)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("stepNeedsYou = %v, %v; want %v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestNeedLandingFor pins where each kind lands (A3).
//
// Load-bearing rows: "waiting session" (an agent landing, which the caller
// routes through sessionEnterTarget), "broken session" (the project view
// with that session selected, not its agent, which often does not exist),
// and "gone project" (All projects, since a gone project cannot be opened).
func TestNeedLandingFor(t *testing.T) {
	cases := []struct {
		name string
		item needItem
		want needLanding
	}{
		{name: "waiting session",
			item: needItem{Kind: needSession, ProjectPath: "/p/a", Session: "s1", HostRunning: true},
			want: needLanding{Place: landAgent, Project: "/p/a", Select: "s1"}},
		{name: "blocking inbox item",
			item: needItem{Kind: needInbox, ProjectPath: "/p/a", ItemID: "q1"},
			want: needLanding{Place: landInbox, Project: "/p/a", ItemID: "q1"}},
		{name: "plan to approve",
			item: needItem{Kind: needPlan, ProjectPath: "/p/a"},
			want: needLanding{Place: landProject, Project: "/p/a"}},
		{name: "broken session",
			item: needItem{Kind: needBroken, ProjectPath: "/p/a", Session: "x"},
			want: needLanding{Place: landProject, Project: "/p/a", Select: "x"}},
		{name: "gone project",
			item: needItem{Kind: needGone, ProjectPath: "/p/a"},
			want: needLanding{Place: landAllProjects, Project: "/p/a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needLandingFor(tc.item); got != tc.want {
				t.Errorf("needLandingFor = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestNeedLabel pins the Agent band's words for each kind.
//
// Load-bearing rows: "session in the active project" (bare name, as the band
// said before T16.6) against "session in another project" (the project is
// named, or the owner cannot tell `]` leaves this project).
func TestNeedLabel(t *testing.T) {
	cases := []struct {
		name string
		item needItem
		want string
	}{
		{name: "session in the active project",
			item: needItem{Kind: needSession, ProjectPath: "/p/a", ProjectName: "alpha", Session: "s1"}, want: "s1"},
		{name: "session in another project",
			item: needItem{Kind: needSession, ProjectPath: "/p/b", ProjectName: "beta", Session: "s2"}, want: "s2 · beta"},
		{name: "broken session in the active project",
			item: needItem{Kind: needBroken, ProjectPath: "/p/a", ProjectName: "alpha", Session: "x"}, want: "x"},
		{name: "broken session in another project",
			item: needItem{Kind: needBroken, ProjectPath: "/p/b", ProjectName: "beta", Session: "x"}, want: "x · beta"},
		{name: "inbox item",
			item: needItem{Kind: needInbox, ProjectPath: "/p/b", ProjectName: "beta", ItemID: "q"}, want: "beta: answer"},
		{name: "plan to approve",
			item: needItem{Kind: needPlan, ProjectPath: "/p/a", ProjectName: "alpha"}, want: "alpha: plan to approve"},
		{name: "gone project",
			item: needItem{Kind: needGone, ProjectPath: "/p/b", ProjectName: "beta"}, want: "beta: gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needLabel(tc.item, "/p/a"); got != tc.want {
				t.Errorf("needLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNeedCountsElsewhere pins the top bar's per-project counts.
//
// Load-bearing rows: "every kind counts" (the top bar counts all five kinds,
// not only waiting sessions as before T16.6), "the active project is left
// out", and "in the items' order" (the project named first is the one `]`
// reaches first, not the one with the most).
func TestNeedCountsElsewhere(t *testing.T) {
	it := func(kind needKind, project string) needItem {
		return needItem{Kind: kind, ProjectPath: "/p/" + project, ProjectName: project, Session: "s"}
	}
	cases := []struct {
		name   string
		items  []needItem
		active string
		want   []otherProjectAwaiting
	}{
		{name: "nothing", items: nil, active: "/p/a", want: nil},
		{name: "the active project is left out",
			items: []needItem{it(needSession, "a"), it(needPlan, "a")}, active: "/p/a", want: nil},
		{name: "every kind counts",
			items:  []needItem{it(needSession, "b"), it(needInbox, "b"), it(needPlan, "b"), it(needBroken, "b"), it(needGone, "b")},
			active: "/p/a", want: []otherProjectAwaiting{{name: "b", count: 5}}},
		{name: "in the items' order",
			items:  []needItem{it(needSession, "gamma"), it(needSession, "a"), it(needSession, "beta"), it(needPlan, "beta")},
			active: "/p/a", want: []otherProjectAwaiting{{name: "gamma", count: 1}, {name: "beta", count: 2}}},
		{name: "no active project counts every project",
			items:  []needItem{it(needSession, "a"), it(needGone, "b")},
			active: "", want: []otherProjectAwaiting{{name: "a", count: 1}, {name: "b", count: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needCountsElsewhere(tc.items, tc.active)
			if !slices.Equal(got, tc.want) {
				t.Errorf("needCountsElsewhere = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestAwaitingElsewhereByLevel pins F10: "elsewhere" leaves the active project
// out only where the owner is in one. At All projects the owner is in none, so
// the project that merely is the active one still counts.
//
// Load-bearing rows: "All projects counts the active project" (the fault) and
// the Project and Agent rows (the exclusion the landing code and the Agent
// band rely on must stay).
func TestAwaitingElsewhereByLevel(t *testing.T) {
	alpha := project.Project{Path: "/repos/alpha", Name: "alpha"}
	beta := project.Project{Path: "/repos/beta", Name: "beta"}
	cases := []struct {
		name string
		mut  func(m *Model)
		want []otherProjectAwaiting
	}{
		{"All projects counts the active project", func(m *Model) { m.allProjectsOpen = true },
			[]otherProjectAwaiting{{name: "alpha", count: 1}}},
		{"Project leaves the active project out", func(m *Model) {}, nil},
		{"Agent leaves the active project out", func(m *Model) {
			m.pane, m.paneName, m.view, m.focus = newFakePane("agent"), "s1", viewAgent, focusRight
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			waiting := sess("s1")
			waiting.AwaitingInput = true
			m := testModel(waiting)
			m.projects = []project.Project{alpha, beta}
			m.activeProject = 0
			tc.mut(&m)
			if tc.name[:5] == "Agent" && m.level() != levelAgent {
				t.Fatalf("fixture broken: level = %v", m.level())
			}
			if got := m.awaitingElsewhere(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("awaitingElsewhere() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
