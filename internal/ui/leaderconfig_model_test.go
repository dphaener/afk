package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// ctrlOPress is ctrl+o as the program receives it: the byte 0x0F decodes to
// {Code: 'o', Mod: ModCtrl}, with no Text, like every ctrl+letter.
var ctrlOPress = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}

// leaderConfiguredModel is testModel with config.toml's [dash] leader set to
// leader, built through New so the leader takes the path `afk dash` gives it
// (Deps.Leader, then checkLeader) rather than being poked into the Model.
func leaderConfiguredModel(leader string, sessions ...Session) Model {
	d := testDeps()
	d.Leader = leader
	m := New("/repo", time.Second, d)
	m.ready = true
	m.width = 120
	m.height = 40
	m.sizeSessionList()
	m.setSessions(sessions)
	return m
}

// The three places a leader is pressed from, for a model with leader set:
// the session list with a pane open, the focused overview, and the focused
// agent pane. Each hands back the fake pane so a row can read what the agent
// was sent.
func leaderFromList(t *testing.T, leader string) (Model, *fakePane) {
	t.Helper()
	fp := newFakePane("X")
	m := leaderConfiguredModel(leader, sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	return m, fp
}

func leaderFromDetail(t *testing.T, leader string) (Model, *fakePane) {
	t.Helper()
	fp := newFakePane("X")
	m := leaderConfiguredModel(leader, runningSess("alpha"), runningSess("beta"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "alpha"})
	return focusOverview(t, m), fp
}

func leaderFromPane(t *testing.T, leader string) (Model, *fakePane) {
	t.Helper()
	fp := newFakePane("X")
	m := leaderConfiguredModel(leader, sess("a"))
	m, _ = update(t, m, paneAttachedMsg{pane: fp, name: "a"})
	m, _ = update(t, m, keyEnter)
	if !m.paneOwnsKeyboard() {
		t.Fatal("setup: enter did not focus the pane")
	}
	return m, fp
}

// TestConfiguredLeaderOpensTheStrip presses keys through Update with
// config.toml's [dash] leader set (T9.2) and checks whether the key strip is
// open and the exact bytes the agent would read (agentBytes, a real x/vt
// encoder). Load-bearing rows:
//   - "ctrl+o opens the strip from the agent pane": a configured leader has
//     to work where it is a key, which proves decideKey is handed the
//     Model's leader, not a package constant.
//   - the list and detail rows: keys work alone at Project since T16.2, so
//     the configured leader opens nothing there either.
//   - "ctrl+o twice in the pane types one 0x0F": the pass-through sends the
//     configured key (m.leaderKey.key), encoded by x/vt, not a hard-coded
//     ctrl+]. With the old literal this row reads 0x1D.
//   - "ctrl+] with ctrl+o goes to the agent": once another leader is set,
//     ctrl+] is no longer bound, so in the pane it goes to the agent like any
//     other chord.
//   - "refused q falls back": a refused leader must leave the dashboard
//     usable, so ctrl+] still opens the strip and q is not swallowed.
//   - "unset is ctrl+]": the default is unchanged for an owner who never set
//     a leader.
func TestConfiguredLeaderOpensTheStrip(t *testing.T) {
	cases := []struct {
		name      string
		leader    string
		model     func(t *testing.T, leader string) (Model, *fakePane)
		keys      []tea.KeyPressMsg
		wantStrip bool
		wantBytes string
	}{
		{name: "ctrl+o opens nothing from the list", leader: "ctrl+o", model: leaderFromList, keys: []tea.KeyPressMsg{ctrlOPress}},
		{name: "ctrl+o opens nothing from the detail side", leader: "ctrl+o", model: leaderFromDetail, keys: []tea.KeyPressMsg{ctrlOPress}},
		{name: "ctrl+o opens the strip from the agent pane", leader: "ctrl+o", model: leaderFromPane, keys: []tea.KeyPressMsg{ctrlOPress}, wantStrip: true},
		{name: "ctrl+o twice in the pane types one 0x0F", leader: "ctrl+o", model: leaderFromPane, keys: []tea.KeyPressMsg{ctrlOPress, ctrlOPress}, wantBytes: "\x0f"},
		{name: "ctrl+] with ctrl+o opens nothing from the list", leader: "ctrl+o", model: leaderFromList, keys: []tea.KeyPressMsg{leaderKeyPress}},
		{name: "ctrl+] with ctrl+o goes to the agent from the pane", leader: "ctrl+o", model: leaderFromPane, keys: []tea.KeyPressMsg{leaderKeyPress}, wantBytes: "\x1d"},
		{name: "refused q falls back to ctrl+]", leader: "q", model: leaderFromPane, keys: []tea.KeyPressMsg{leaderKeyPress}, wantStrip: true},
		{name: "refused q is not the leader in the pane", leader: "q", model: leaderFromPane, keys: []tea.KeyPressMsg{keyRune('q').(tea.KeyPressMsg)}},
		{name: "unset is ctrl+]", leader: "", model: leaderFromPane, keys: []tea.KeyPressMsg{leaderKeyPress}, wantStrip: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fp := tc.model(t, tc.leader)
			for _, k := range tc.keys {
				m, _ = update(t, m, k)
			}
			if m.keyStripShown() != tc.wantStrip {
				t.Fatalf("strip shown = %v, want %v", m.keyStripShown(), tc.wantStrip)
			}
			if onScreen := strings.Contains(render(m), keyStripWaiting); onScreen != tc.wantStrip {
				t.Fatalf("strip on screen = %v, want %v", onScreen, tc.wantStrip)
			}
			if len(fp.texts) == 0 {
				if got := agentBytes(t, fp); got != tc.wantBytes {
					t.Fatalf("the agent read %q, want %q", got, tc.wantBytes)
				}
			}
		})
	}
}

// TestConfiguredLeaderNamesItsKey renders the screens that name the leader
// with ctrl+o configured and checks they say ctrl+o and never ctrl+]. Every
// row is load-bearing: each surface used to read a package-level ctrl+]
// (a var frozen at init, or the constant), and a row failing means that
// surface would tell the owner to press a key that does nothing.
func TestConfiguredLeaderNamesItsKey(t *testing.T) {
	cases := []struct {
		name string
		// screen renders the surface for a model with ctrl+o set.
		screen func(t *testing.T) string
		want   string
	}{
		{"pane footer", func(t *testing.T) string {
			m, _ := leaderFromPane(t, "ctrl+o")
			return render(m)
		}, "ctrl+o afk keys"},
		{"pane mouse hint", func(t *testing.T) string {
			m, _ := leaderFromPane(t, "ctrl+o")
			return render(m)
		}, "ctrl+o m mouse:"},
		{"key strip", func(t *testing.T) string {
			m, _ := leaderFromPane(t, "ctrl+o")
			m, _ = update(t, m, ctrlOPress)
			return render(m)
		}, "send ctrl+o"},
		{"actions search from the agent", func(t *testing.T) string {
			m, _ := leaderFromPane(t, "ctrl+o")
			m, _ = update(t, m, ctrlOPress)
			m, _ = update(t, m, keyRune(':'))
			return render(m)
		}, "ctrl+o i"},
		{"focused pane title", func(t *testing.T) string {
			m, _ := leaderFromPane(t, "ctrl+o")
			return m.paneTitleHint()
		}, "keys go to the agent · ctrl+o esc back"},
		{"focus notice", func(t *testing.T) string {
			m, _ := leaderFromPane(t, "ctrl+o")
			return m.status
		}, "ctrl+o esc to return"},
		{"wedge hint", func(t *testing.T) string {
			return wedgeHintDashboard(leaderConfiguredModel("ctrl+o").leaderKey.name)
		}, "press ctrl+o esc then r to restart"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.screen(t)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("screen does not say %q:\n%s", tc.want, got)
			}
			if strings.Contains(got, defaultLeaderKey) {
				t.Fatalf("screen names %s with ctrl+o configured:\n%s", defaultLeaderKey, got)
			}
		})
	}
}

// TestConfiguredLeaderHeader checks the header's leader line for each kind
// of configured value, rendered at several widths. Load-bearing rows:
//   - "unset" and "accepted ctrl+o": no line, so a dashboard whose leader is
//     fine keeps the header it had.
//   - the "refused q" rows: the line names the value, config.toml and the
//     key in use, at every width down to a narrow one, and the header stays
//     exactly one row (TestRenderHeaderIsExactlyOneRow's rule) because a
//     second row would push the whole frame down.
//   - "refused enter, wide": the reserved-key refusal says the dashboard uses
//     it, from checkLeader's reason, so the owner learns why.
func TestConfiguredLeaderHeader(t *testing.T) {
	cases := []struct {
		name   string
		leader string
		width  int
		want   []string // all must appear; nil means no leader line
	}{
		{"unset", "", 120, nil},
		{"accepted ctrl+o", "ctrl+o", 120, nil},
		{"refused q, very wide", "q", 240, []string{`"q" has no ctrl or alt`, "config.toml", "using ctrl+]", headerBadgeText}},
		{"refused q, wide", "q", 120, []string{`leader "q" not used (needs ctrl or alt)`, "config.toml", "using ctrl+]"}},
		{"refused q, narrow", "q", 70, []string{`leader "q" not used`, "config.toml", "using ctrl+]"}},
		{"refused enter, wide", "enter", 120, []string{`"enter"`, "the dashboard uses it", "config.toml", "using ctrl+]"}},
		{"refused q, tiny", "q", 20, []string{`leader "q"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := leaderConfiguredModel(tc.leader, sess("a"))
			m.width = tc.width
			raw := m.renderHeader()
			header := xansi.Strip(raw)
			t.Logf("header at width %d: %q", tc.width, header)
			if strings.Contains(raw, "\n") {
				t.Fatalf("header is more than one row: %q", header)
			}
			if w := lipgloss.Width(raw); w > tc.width {
				t.Fatalf("header is %d cells wide, want at most %d: %q", w, tc.width, header)
			}
			if tc.want == nil {
				if strings.Contains(header, "leader") || strings.Contains(header, "config.toml") {
					t.Fatalf("header has a leader line with leader %q: %q", tc.leader, header)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(header, w) {
					t.Errorf("header %q does not contain %q", header, w)
				}
			}
			// The line sits in the full frame too, on its first row.
			if first, _, _ := strings.Cut(render(m), "\n"); !strings.Contains(first, "leader") {
				t.Errorf("frame's first row %q has no leader line", first)
			}
		})
	}
}
