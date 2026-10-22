package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/state"
)

// TestAgentBandText pins the Agent's band (§4.3) from a session's facts to
// the one row it draws. Load-bearing rows: "with a task and a diff" (every
// piece, in order, with next needing you at the right end), "no task and no
// diff" (empty pieces are left out, not shown as dashes or +0 −0), and the
// narrow rows, which prove pieces drop whole from the right of the left part
// while next needing you stays, then next needing you goes last (only when it
// cannot fit alone, and the left pieces come back), and the band never grows
// past its width (it is one row: anything wider would be cut
// mid-word or wrap).
func TestAgentBandText(t *testing.T) {
	withDiff := runningSess("feat-auth")
	withDiff.LoopCount = 3
	withDiff.DiffStats = DiffStats{Added: 412, Removed: 88, Files: 9}

	bare := runningSess("bare")

	stale := runningSess("stale")
	stale.StatusStale = true
	stale.DiffStats = DiffStats{Added: 5, Removed: 1}
	stale.LoopCount = 2

	missingBase := runningSess("gone-base")
	missingBase.DiffStats = DiffStats{BaseMissing: true}

	stopped := sess("stopped")

	cases := []struct {
		name  string
		s     Session
		found bool
		task  string
		next  string
		width int
		want  string
	}{
		{
			name: "with a task and a diff", s: withDiff, found: true, task: "W2.T1", next: "fix-login", width: 110,
			want: "◐ executing  ·  W2.T1  ·  +412 −88  ·  loop 3  ·  ◐ host running" +
				strings.Repeat(" ", 110-len([]rune("◐ executing  ·  W2.T1  ·  +412 −88  ·  loop 3  ·  ◐ host running"))-len("next needing you: fix-login")) +
				"next needing you: fix-login",
		},
		{
			name: "no task and no diff", s: bare, found: true, width: 40,
			want: "◐ executing  ·  ◐ host running" + strings.Repeat(" ", 40-len([]rune("◐ executing  ·  ◐ host running"))),
		},
		{
			name: "a stale status shows no diff and no loop", s: stale, found: true, width: 40,
			want: "✕ unknown  ·  ◐ host running" + strings.Repeat(" ", 40-len([]rune("✕ unknown  ·  ◐ host running"))),
		},
		{
			name: "a missing base is not a diff", s: missingBase, found: true, width: 40,
			want: "◐ executing  ·  ◐ host running" + strings.Repeat(" ", 40-len([]rune("◐ executing  ·  ◐ host running"))),
		},
		{
			name: "a stopped host says so", s: stopped, found: true, width: 30,
			want: "◐ executing  ·  ○ host stopped",
		},
		{
			name: "narrow: the host then the loop drop, next needing you stays", s: withDiff, found: true, task: "W2.T1",
			next: "fix-login", width: 63,
			want: "◐ executing  ·  W2.T1  ·  +412 −88  next needing you: fix-login",
		},
		{
			name: "narrower: every left piece but the phase drops", s: withDiff, found: true, task: "W2.T1",
			next: "fix-login", width: 40,
			want: "◐ executing  next needing you: fix-login",
		},
		{
			name: "too narrow for next needing you: it goes last", s: withDiff, found: true, task: "W2.T1",
			next: "fix-login", width: 26,
			want: "◐ executing  ·  W2.T1     ",
		},
		{
			name: "too narrow for any piece: an empty band", s: withDiff, found: true, next: "fix-login", width: 5,
			want: "     ",
		},
		{
			name: "the session left the list: only next needing you", s: Session{}, found: false, next: "fix-login",
			width: 30,
			want:  strings.Repeat(" ", 3) + "next needing you: fix-login",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := agentBandFacts(c.s, c.found, c.task, nil)
			in.Next = c.next
			got := agentBandText(in, c.width)
			if got != c.want {
				t.Errorf("band = %q, want %q", got, c.want)
			}
			if w := lipgloss.Width(got); w != c.width {
				t.Errorf("band is %d cells, want exactly %d", w, c.width)
			}
			if strings.Contains(got, "\n") {
				t.Errorf("band wraps: %q", got)
			}
		})
	}
}

// TestNextNeedingYou pins the band's right part (A2). Load-bearing rows:
// "the current session itself is excluded" (the Agent you are in is not the
// next one) and "Sessions order" (Needs you first, by name, which is the
// order the Sessions section shows, not the order the slice came in).
func TestNextNeedingYou(t *testing.T) {
	asking := func(name string) Session {
		s := runningSess(name)
		s.AwaitingInput = true
		return s
	}
	doneAsking := asking("done-one")
	doneAsking.Phase = state.PhaseDone

	cases := []struct {
		name     string
		current  string
		sessions []Session
		want     string
	}{
		{"none awaits input", "a", []Session{runningSess("a"), runningSess("b")}, ""},
		{"one other awaits input", "a", []Session{runningSess("a"), asking("b")}, "b"},
		{"the current session itself is excluded", "a", []Session{asking("a"), runningSess("b")}, ""},
		{"Sessions order: by name inside Needs you", "a", []Session{runningSess("a"), asking("zeta"), asking("beta")}, "beta"},
		{"a finished session's stale flag does not count", "a", []Session{runningSess("a"), doneAsking}, ""},
		{"no sessions", "a", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextNeedingYou(c.current, c.sessions); got != c.want {
				t.Errorf("nextNeedingYou = %q, want %q", got, c.want)
			}
		})
	}
}

// TestAgentBoxRows pins how the Agent box's rows are shared (§4.3, A6).
// Load-bearing rows: "room for all three" (the band and the chips take one
// row each and the terminal the rest: what paneRegionSize sizes the agent
// to) and the short boxes, where the chips then the band give way so the
// terminal keeps a row (termpane needs a positive size) and the origin and
// the drawing still agree.
func TestAgentBoxRows(t *testing.T) {
	cases := []struct {
		name   string
		outerH int
		want   agentRows
	}{
		{"room for all three", 38, agentRows{band: 1, screen: 33, chips: 1}},
		{"the smallest box with all three", 6, agentRows{band: 1, screen: 1, chips: 1}},
		{"two inner rows: the chips give way", 5, agentRows{band: 1, screen: 1}},
		{"one inner row: the terminal keeps it", 4, agentRows{screen: 1}},
		{"no inner row: still a positive size", 2, agentRows{screen: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentBoxRows(c.outerH); got != c.want {
				t.Errorf("agentBoxRows(%d) = %+v, want %+v", c.outerH, got, c.want)
			}
		})
	}
}

// TestAgentChips pins the Agent's chips row (§4.3, A7). Load-bearing rows:
// "wide" (the four keys that work now, named with the leader, and no
// `ctrl+] ]` until T16.6 builds it), "narrow" (a chip that does not fit
// whole is dropped with the ones after it) and "a configured leader" (the
// chips read the leader from the key table, not a constant).
func TestAgentChips(t *testing.T) {
	cases := []struct {
		name    string
		leader  string
		width   int
		want    []string
		without []string
	}{
		{
			name: "wide", leader: defaultLeaderKey, width: 114,
			want:    []string{"ctrl+] esc  back", "ctrl+] d  details", "ctrl+] m  mouse", "ctrl+] ctrl+]  send ctrl+]"},
			without: []string{"ctrl+] ]"},
		},
		{
			name: "narrow", leader: defaultLeaderKey, width: 40,
			want:    []string{"ctrl+] esc  back", "ctrl+] d  details"},
			without: []string{"mouse", "send"},
		},
		{
			name: "a configured leader", leader: "ctrl+a", width: 114,
			want:    []string{"ctrl+a esc  back", "ctrl+a ctrl+a  send ctrl+a"},
			without: []string{"ctrl+]"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := agentChips(c.leader, c.width)
			got := xansi.Strip(raw)
			if w := lipgloss.Width(raw); w > c.width {
				t.Errorf("chips are %d cells, wider than %d", w, c.width)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("chips %q lack %q", got, w)
				}
			}
			for _, w := range c.without {
				if strings.Contains(got, w) {
					t.Errorf("chips %q should not hold %q", got, w)
				}
			}
		})
	}
}

// TestAgentBandReadsTheModel ties the band to the Model: the linked task
// comes from the build rows (linkedTaskFor), the next needing you from the
// project's sessions, and the band is drawn in the Agent's frame under the
// strip.
func TestAgentBandReadsTheModel(t *testing.T) {
	asking := runningSess("other")
	asking.AwaitingInput = true
	m := testModel(runningSess("live"), asking)
	m.build = buildview.Snapshot{Rows: []buildview.Row{
		{ID: "W1.T1", Session: "older", Status: string(ledger.Done)},
		{ID: "W3.T1", Session: "live", Status: string(ledger.Running)},
	}}
	got := xansi.Strip(m.agentBand("live", 114))
	for _, want := range []string{"◐ executing", "W3.T1", "◐ host running", "next needing you: other"} {
		if !strings.Contains(got, want) {
			t.Errorf("band %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "W1.T1") {
		t.Errorf("band %q names another session's task", got)
	}
}
