package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

func pvSession(name string, phase state.Phase, f func(*Session)) Session {
	s := Session{SessionState: discover.SessionState{Name: name, Phase: phase, HostRunning: true, HostPID: 7}}
	if f != nil {
		f(&s)
	}
	return s
}

// TestSessionGroupOf: load-bearing rows are the three reasons for Needs you
// (awaiting input, a wedge, failed), the stale-awaiting row (a done session
// carrying the flag is Done, not Needs you), the standalone session with no
// host.json (Working, not a dead host) and the dead host (Needs you).
func TestSessionGroupOf(t *testing.T) {
	tests := []struct {
		name string
		s    Session
		want sessionGroup
	}{
		{"awaiting input", pvSession("a", state.PhaseExecuting, func(s *Session) { s.AwaitingInput = true }), groupNeedsYou},
		{"awaiting input on a done session is stale", pvSession("a", state.PhaseDone, func(s *Session) { s.AwaitingInput = true; s.HostRunning = false }), groupDone},
		{"wedged", pvSession("a", state.PhaseExecuting, func(s *Session) { s.WedgedSince = "2026-10-20T09:00:00Z" }), groupNeedsYou},
		{"failed", pvSession("a", state.PhaseFailed, func(s *Session) { s.HostRunning = false }), groupNeedsYou},
		{"stale status", pvSession("a", "", func(s *Session) { s.StatusStale = true }), groupNeedsYou},
		{"worktree fault", pvSession("a", "", func(s *Session) { s.Kind = session.KindWorktreeMissing }), groupNeedsYou},
		{"host gone mid-work", pvSession("a", state.PhaseExecuting, func(s *Session) { s.HostRunning = false }), groupNeedsYou},
		{"standalone with no host.json", pvSession("a", state.PhaseExecuting, func(s *Session) { s.HostRunning = false; s.HostPID = 0 }), groupWorking},
		{"paused for usage", pvSession("a", state.PhaseExecuting, func(s *Session) { s.HostRunning = false; s.UsagePaused = true }), groupWorking},
		{"executing", pvSession("a", state.PhaseExecuting, nil), groupWorking},
		{"planning", pvSession("a", state.PhasePlanning, nil), groupWorking},
		{"pending", Session{SessionState: discover.SessionState{Name: "a"}, Pending: true}, groupWorking},
		{"one-off", pvSession("a", "", func(s *Session) { s.Scratch = true }), groupWorking},
		{"done", pvSession("a", state.PhaseDone, func(s *Session) { s.HostRunning = false }), groupDone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionGroupOf(tt.s); got != tt.want {
				t.Errorf("sessionGroupOf = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSessionGroupOrder: the group order (Needs you, Working, Done), name
// order inside a group and an empty group left out are the rows.
func TestSessionGroupOrder(t *testing.T) {
	names := func(runs []sessionGroupRun) []string {
		var out []string
		for _, r := range runs {
			out = append(out, r.Group.String()+":")
			for _, s := range r.Sessions {
				out = append(out, s.Name)
			}
		}
		return out
	}
	tests := []struct {
		name string
		in   []Session
		want []string
	}{
		{"all three, out of order", []Session{
			pvSession("z-done", state.PhaseDone, nil),
			pvSession("m-work", state.PhaseExecuting, nil),
			pvSession("b-ask", state.PhaseExecuting, func(s *Session) { s.AwaitingInput = true }),
		}, []string{"Needs you:", "b-ask", "Working:", "m-work", "Done:", "z-done"}},
		{"name order inside a group", []Session{
			pvSession("c", state.PhaseExecuting, nil), pvSession("a", state.PhaseReviewing, nil), pvSession("b", state.PhaseFixing, nil),
		}, []string{"Working:", "a", "b", "c"}},
		{"empty groups left out", []Session{pvSession("x", state.PhaseDone, nil)}, []string{"Done:", "x"}},
		{"no sessions", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := names(groupSessions(tt.in)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSessionRightWord: awaiting wins over a running host.
func TestSessionRightWord(t *testing.T) {
	tests := []struct {
		name string
		s    Session
		want string
	}{
		{"awaiting input", pvSession("a", state.PhaseExecuting, func(s *Session) { s.AwaitingInput = true }), "asks you"},
		{"host running", pvSession("a", state.PhaseExecuting, nil), "open it"},
		{"neither", pvSession("a", state.PhaseExecuting, func(s *Session) { s.HostRunning = false }), ""},
		{"stale awaiting on a done session", pvSession("a", state.PhaseDone, func(s *Session) { s.AwaitingInput = true; s.HostRunning = false }), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionRightWord(tt.s); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLinkedTaskFor: load-bearing rows are running preferred over an earlier
// done row, and the two no-link cases (a session with no task, a task with
// no session).
func TestLinkedTaskFor(t *testing.T) {
	rows := []buildview.Row{
		{ID: "T1", Status: "done", Session: "s1"},
		{ID: "T2", Status: "running", Session: "s1"},
		{ID: "T3", Status: "pending"},
		{ID: "T4", Status: "done", Session: "s2"},
		{ID: "T5", Status: "done", Session: "s2"},
		{ID: "T6", Status: "parked", Session: "s3"},
		{ID: "T7", Status: "shipping", Session: "s4"},
	}
	tests := []struct {
		name    string
		session string
		rows    []buildview.Row
		wantIdx int
		wantOK  bool
	}{
		{"session with a task", "s3", rows, 5, true},
		{"running preferred over done", "s1", rows, 1, true},
		{"shipping counts as in play", "s4", rows, 6, true},
		{"first done row when only done", "s2", rows, 3, true},
		{"session with none", "nobody", rows, 0, false},
		{"task with no session: empty name maps to nothing", "", rows, 0, false},
		{"no rows", "s1", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, ok := linkedTaskFor(tt.session, tt.rows)
			if idx != tt.wantIdx || ok != tt.wantOK {
				t.Errorf("got (%d, %v), want (%d, %v)", idx, ok, tt.wantIdx, tt.wantOK)
			}
		})
	}
}

// TestSessionSummaryLines: load-bearing rows are the question found by
// session, by task when no session matches, the oldest of two, an answered
// item ignored, and empty values left out.
func TestSessionSummaryLines(t *testing.T) {
	full := pvSession("s1", state.PhaseExecuting, func(s *Session) {
		s.Branch = "feat/x"
		s.BaseBranch = "main"
		s.LoopCount = 2
		s.DiffStats = DiffStats{Added: 3, Removed: 1, Files: 2}
		s.Verdict = &state.ReviewVerdict{Verdict: state.VerdictApprove}
	})
	task := &buildview.Row{ID: "W1.T2", Title: "Add parser"}
	get := func(ls []summaryLine, label string) (summaryLine, bool) {
		for _, l := range ls {
			if l.Label == label {
				return l, true
			}
		}
		return summaryLine{}, false
	}
	t.Run("everything present, no question", func(t *testing.T) {
		ls := sessionSummaryLines(full, task, nil)
		var labels []string
		for _, l := range ls {
			labels = append(labels, l.Label)
		}
		want := []string{"branch", "phase", "task", "diff", "verdict", "host"}
		if !reflect.DeepEqual(labels, want) {
			t.Fatalf("labels = %v, want %v", labels, want)
		}
		if l, _ := get(ls, "phase"); l.Value != "executing · loop 2" {
			t.Errorf("phase = %q", l.Value)
		}
		if l, _ := get(ls, "task"); l.Value != "W1.T2 Add parser" {
			t.Errorf("task = %q", l.Value)
		}
		if l, _ := get(ls, "diff"); l.Value != "+3/-1 · 2 file(s) vs main" {
			t.Errorf("diff = %q", l.Value)
		}
	})
	t.Run("question found by session, oldest first, title then body", func(t *testing.T) {
		items := []inbox.Item{
			{ID: "b", Session: "s1", CreatedAt: "2026-10-21T10:00:00Z", Title: "Later", Body: "later body"},
			{ID: "a", Session: "s1", CreatedAt: "2026-10-21T09:00:00Z", Title: "Which db?", Body: "SQLite or Redis"},
			{ID: "c", Session: "s1", CreatedAt: "2026-10-21T08:00:00Z", Title: "Answered", Answer: "x"},
		}
		l, ok := get(sessionSummaryLines(full, task, items), "need")
		if !ok || l.Value != "Which db? SQLite or Redis" || l.Tone != toneNeed {
			t.Errorf("need = %+v ok=%v", l, ok)
		}
	})
	t.Run("question found by task when none matches the session", func(t *testing.T) {
		items := []inbox.Item{
			{ID: "o", Session: "other", Task: "W9", Title: "Not mine"},
			{ID: "t", Task: "W1.T2", Title: "About my task"},
		}
		l, ok := get(sessionSummaryLines(full, task, items), "need")
		if !ok || l.Value != "About my task" {
			t.Errorf("need = %+v ok=%v", l, ok)
		}
	})
	t.Run("a session match beats a task match", func(t *testing.T) {
		items := []inbox.Item{
			{ID: "t", Task: "W1.T2", CreatedAt: "2026-10-21T01:00:00Z", Title: "By task"},
			{ID: "s", Session: "s1", CreatedAt: "2026-10-21T02:00:00Z", Title: "By session"},
		}
		if l, _ := get(sessionSummaryLines(full, task, items), "need"); l.Value != "By session" {
			t.Errorf("need = %q", l.Value)
		}
	})
	t.Run("no question when nothing matches", func(t *testing.T) {
		items := []inbox.Item{{ID: "o", Session: "other", Title: "x"}}
		if _, ok := get(sessionSummaryLines(full, nil, items), "need"); ok {
			t.Error("need line present")
		}
	})
	t.Run("empty values left out", func(t *testing.T) {
		bare := pvSession("s2", state.PhaseExecuting, func(s *Session) { s.HostRunning = false })
		ls := sessionSummaryLines(bare, nil, nil)
		for _, l := range ls {
			if strings.TrimSpace(l.Value) == "" {
				t.Errorf("empty line kept: %+v", l)
			}
			if l.Label == "branch" || l.Label == "task" || l.Label == "diff" || l.Label == "verdict" {
				t.Errorf("line %q should be left out", l.Label)
			}
		}
		if l, ok := get(ls, "host"); !ok || l.Value != "not running" {
			t.Errorf("host = %+v ok=%v", l, ok)
		}
	})
	t.Run("one-off shows only branch-free facts", func(t *testing.T) {
		ls := sessionSummaryLines(pvSession("o", "", func(s *Session) { s.Scratch = true }), nil, nil)
		if len(ls) != 1 || ls[0].Label != "host" {
			t.Errorf("lines = %+v", ls)
		}
	})
}

// TestNextSection: every key, the wrap at both ends and an unrelated key.
func TestNextSection(t *testing.T) {
	tests := []struct {
		name string
		cur  projectSection
		key  string
		want projectSection
	}{
		{"tab from sessions", sectionSessions, "tab", sectionBuild},
		{"tab from build", sectionBuild, "tab", sectionInbox},
		{"tab wraps from inbox", sectionInbox, "tab", sectionSessions},
		{"shift+tab from inbox", sectionInbox, "shift+tab", sectionBuild},
		{"shift+tab from build", sectionBuild, "shift+tab", sectionSessions},
		{"shift+tab wraps from sessions", sectionSessions, "shift+tab", sectionInbox},
		{"1 direct", sectionInbox, "1", sectionSessions},
		{"2 direct", sectionSessions, "2", sectionBuild},
		{"3 direct", sectionSessions, "3", sectionInbox},
		{"2 on build stays", sectionBuild, "2", sectionBuild},
		{"unrelated key unchanged", sectionBuild, "x", sectionBuild},
		{"4 unchanged", sectionBuild, "4", sectionBuild},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextSection(tt.cur, tt.key); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestProjectEnterTarget: each section with and without a selection, and a
// selection in another section never opens this one's target.
func TestProjectEnterTarget(t *testing.T) {
	tests := []struct {
		name                      string
		sec                       projectSection
		haveSession, haveTask, hi bool
		want                      enterTarget
	}{
		{"sessions with a session", sectionSessions, true, false, false, enterAgent},
		{"sessions empty", sectionSessions, false, true, true, enterNothing},
		{"build with a task", sectionBuild, false, true, false, enterTask},
		{"build empty", sectionBuild, true, false, true, enterNothing},
		{"inbox with an item", sectionInbox, false, false, true, enterItem},
		{"inbox empty", sectionInbox, true, true, false, enterNothing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectEnterTarget(tt.sec, tt.haveSession, tt.haveTask, tt.hi); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestProjectBandText: shipping rows count as pull requests, only open items
// count in the inbox, and stopped beats paused beats running.
func TestProjectBandText(t *testing.T) {
	tests := []struct {
		name string
		in   projectBandInput
		want string
	}{
		{"empty", projectBandInput{}, ""},
		{"running with counts", projectBandInput{State: "building", Build: buildview.Snapshot{
			Done: 3, Total: 8, Current: "W1.T2",
			Rows: []buildview.Row{{Status: "shipping"}, {Status: "shipping"}, {Status: "done"}},
			Items: []inbox.Item{
				{Blocking: true}, {}, {Answer: "x", Blocking: true},
			},
		}}, "building  ·  3 of 8 tasks done  ·  running W1.T2  ·  2 open · 1 blocking in inbox  ·  2 pull request(s) open"},
		{"paused", projectBandInput{Build: buildview.Snapshot{Paused: true, Current: "W1"}}, "build paused"},
		{"stopped beats paused", projectBandInput{Build: buildview.Snapshot{Paused: true, Stopped: true}}, "build stopped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectBandText(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
