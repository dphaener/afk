package ui

import (
	"fmt"
	"image/color"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	xansi "github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// --- renderHeader: badge, cross-project attention, switch hint ---

// wantBadgeOnlyHeader recomputes the quiet header directly — the badge, then
// spaces out to the frame's width — WITHOUT going through headerLine, so the
// "nothing to report" tests below have an oracle that cannot agree with the
// implementation by sharing its bug. headerLine's own ladder is pinned
// separately by TestHeaderLineDegradesInOrder.
func wantBadgeOnlyHeader(m Model) string {
	badge := theme.header.Render(headerBadgeText)
	return badge + strings.Repeat(" ", m.width-lipgloss.Width(badge))
}

// attentionModel is the shape every cross-project test below needs: four
// known projects with alpha active, gamma and delta holding sessions that
// are awaiting input, and beta quiet. The open counts are what put gamma
// ahead of delta — alphabetically delta sorts FIRST — so any test asserting
// "gamma … delta" is also asserting that the header takes its order from
// sortSwitcherProjects rather than from map iteration or from the names.
func attentionModel(sessions ...Session) Model {
	m := testModel(sessions...)
	m.projects = []project.Project{
		{Path: "/repos/alpha", Name: "alpha"},
		{Path: "/repos/beta", Name: "beta"},
		{Path: "/repos/gamma", Name: "gamma"},
		{Path: "/repos/delta", Name: "delta"},
	}
	m.activeProject = 0
	m.projectSessions = map[string][]discover.SessionState{
		"/repos/beta":  {{AwaitingInput: false}},
		"/repos/gamma": {{AwaitingInput: true}, {AwaitingInput: true}},
		"/repos/delta": {{AwaitingInput: true}},
	}
	m.projectOpens = map[string]int{"/repos/gamma": 5, "/repos/delta": 3}
	return m
}

// TestRenderHeaderCarriesNoProjectChromeForSingleOrZeroProjects pins the
// quiet header byte-identical to the bare badge: no switch hint (there is
// nowhere to switch to), no attention line (there is no other project to
// report on), and none of the chrome this redesign deleted — the
// `N session(s)` count, the `(N of M projects)` counter, the
// `refreshed … every …` cadence, or the `o`/`l` key hints an earlier change
// hand-wrote here before full help (`?`) took that job over.
//
// The zero-project row is the load-bearing one: it is what every
// pre-existing test in this package already runs as, because testModel never
// sets Deps.Projects. A header that grew an "0 awaiting" line, or a hint to
// a switcher with one entry, would change all of them.
func TestRenderHeaderCarriesNoProjectChromeForSingleOrZeroProjects(t *testing.T) {
	tests := []struct {
		name     string
		projects []project.Project
		active   int
	}{
		{"zero projects (legacy default Model)", nil, 0},
		{"exactly one project", []project.Project{{Path: "/only", Name: "only"}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("a"), sess("b"))
			m.projects = tc.projects
			m.activeProject = tc.active

			got := m.renderHeader()
			for _, banned := range []string{
				"session(s)", "projects)", "refreshed", "every 1s", "awaiting",
				headerSwitchHint,
			} {
				if strings.Contains(xansi.Strip(got), banned) {
					t.Errorf("renderHeader() = %q, must not carry %q", got, banned)
				}
			}
			if want := wantBadgeOnlyHeader(m); got != want {
				t.Fatalf("renderHeader() = %q, want the bare badge %q", got, want)
			}
		})
	}
}

// TestRenderHeaderDoesNotHandCarryOneOffAndLabel is the surviving half of a
// W6.T2 contract: the header used to hand-write `o`/`l` hints because the
// global help line was capped at its measured width and could not carry
// them. Full help (bound to `?` in normal mode) now advertises every key in
// inputNormal's keymap unconditionally — TestFullHelpOmitsNoEnabledBinding
// covers that generally — so the header must not grow its own copy back.
//
// The redesign gives the header exactly ONE hint, and this pins that it is
// the only one, at every project count. The multi-project row is the
// load-bearing one: it is the only shape where the header renders a hint at
// all, so it is the only shape where a second one could hide.
func TestRenderHeaderDoesNotHandCarryOneOffAndLabel(t *testing.T) {
	tests := []struct {
		name     string
		projects []project.Project
		wantHint bool
	}{
		{"zero projects (the unconfigured default)", nil, false},
		{"exactly one project", []project.Project{{Path: "/only", Name: "only"}}, false},
		{
			"several projects",
			[]project.Project{{Path: "/a", Name: "alpha"}, {Path: "/b", Name: "beta"}},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("a"), sess("b"))
			m.projects = tc.projects
			m.width = 80

			got := xansi.Strip(m.renderHeader())
			for _, banned := range []string{
				m.keys.Scratch.Help().Key + " " + m.keys.Scratch.Help().Desc,
				m.keys.Label.Help().Key + " " + m.keys.Label.Help().Desc,
			} {
				if strings.Contains(got, banned) {
					t.Errorf("renderHeader() at width 80 = %q, must not hand-carry %q", got, banned)
				}
			}
			if has := strings.Contains(got, headerSwitchHint); has != tc.wantHint {
				t.Fatalf("renderHeader() at width 80 = %q, switch hint present = %v, want %v",
					got, has, tc.wantHint)
			}
		})
	}
}

// TestRenderHeaderShowsCrossProjectAttention is the multi-project half of
// the acceptance criteria, and it replaces the old
// TestRenderHeaderShowsProjectAndCount: the ACTIVE project's name and its
// `(N of M projects)` position are gone from the header — the session list
// box's border label carries the name now — and what the row reports instead
// is the projects the user cannot see from here.
//
// The order assertion is load-bearing twice over: it proves the clauses come
// from sortSwitcherProjects (delta sorts before gamma by name, and only the
// open counts put gamma first) and it proves only the FIRST clause spells
// out the word "awaiting".
func TestRenderHeaderShowsCrossProjectAttention(t *testing.T) {
	m := attentionModel()
	m.width = 200

	got := xansi.Strip(m.renderHeader())
	if want := awaitingGlyph + " 2 awaiting in gamma · 1 in delta"; !strings.Contains(got, want) {
		t.Fatalf("renderHeader() = %q, want it to contain %q", got, want)
	}
	if !strings.Contains(got, headerSwitchHint) {
		t.Errorf("renderHeader() = %q, want the switch hint with four known projects", got)
	}
	for _, banned := range []string{"alpha", "beta", "of 4 projects", "session(s)", "refreshed"} {
		if strings.Contains(got, banned) {
			t.Errorf("renderHeader() = %q, must not carry %q", got, banned)
		}
	}
}

// TestRenderHeaderCarriesStalenessBesideTheAttentionLine is the wide-width
// end of the staleness signal, where nothing is shed and both alerts are on
// screen at once — the state TestHeaderLineKeepsStalenessLast's rungs never
// show, since every one of those rows is a width that drops something.
//
// Three things are load-bearing here. The healthy assertions run FIRST, on
// the same model, so the piece is proved absent before it is proved present:
// a test that only ever checks the failing state passes just as well against
// a header that says "stale" unconditionally. The reading order (staleness
// before the attention counts) is asserted because the staleness qualifies
// those counts — they come from the same discovery pass — and a reader who
// meets them first has already believed them. And the row count is checked
// with both alerts up, since this is the widest the header's left half ever
// gets and so the likeliest state to wrap.
func TestRenderHeaderCarriesStalenessBesideTheAttentionLine(t *testing.T) {
	m := attentionModel()
	m.width = 200

	if p := m.headerPieces(); p.stale != "" {
		t.Fatalf("headerPieces().stale = %q with a zero loadFailingSince, want empty", p.stale)
	}
	if got := xansi.Strip(m.renderHeader()); strings.Contains(got, "stale") {
		t.Fatalf("renderHeader() = %q, want no staleness piece while discovery is healthy", got)
	}

	m.loadFailingSince = m.deps.Now().Add(-45 * time.Second)
	got := xansi.Strip(m.renderHeader())
	for _, want := range []string{
		headerBadgeText,
		"stale · discovery failing 45s",
		awaitingGlyph + " 2 awaiting in gamma · 1 in delta",
		headerSwitchHint,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderHeader() = %q, want it to contain %q", got, want)
		}
	}
	if strings.Index(got, "stale ·") > strings.Index(got, "awaiting in gamma") {
		t.Errorf("renderHeader() = %q, want the staleness signal before the counts it invalidates", got)
	}
	if rows := lipgloss.Height(m.renderHeader()); rows != 1 {
		t.Fatalf("renderHeader() is %d rows with both alerts up, want exactly 1: %q", rows, got)
	}
}

// TestRenderHeaderIgnoresAnOutOfRangeActiveIndex covers the transient window
// applyProjectScan's own doc comment calls out: a union that grows the list
// past one entry promotes the active index in the SAME pass, so an
// out-of-range index alongside a multi-project list should never be observed
// in production — but the header must degrade rather than index out of the
// slice.
//
// "Degrade" here means treating NO project as active, so every known project
// counts as another one, which is the same tolerance awaitingElsewhere
// documents. A header that instead dropped the attention line entirely would
// also "not panic" and would be wrong.
func TestRenderHeaderIgnoresAnOutOfRangeActiveIndex(t *testing.T) {
	m := attentionModel()
	m.activeProject = -1
	m.projectSessions["/repos/alpha"] = []discover.SessionState{{AwaitingInput: true}}
	m.width = 200

	got := xansi.Strip(m.renderHeader())
	for _, want := range []string{"2 awaiting in gamma", "1 in delta", "1 in alpha"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderHeader() = %q, want it to contain %q for an out-of-range index", got, want)
		}
	}
	if lipgloss.Height(m.renderHeader()) != 1 {
		t.Fatalf("renderHeader() = %q, want exactly one row", got)
	}
}

// TestHeaderLineDegradesInOrder pins headerLine's drop order with plain,
// unstyled pieces so every threshold below is arithmetic on the strings in
// the fixture: the named attention collapses to the aggregate first, then
// the hint goes, then the BADGE goes — before the attention line, which is
// the rung a reader is most likely to "fix" — and only then is anything
// handed to truncate. Each row sits one cell below the previous rung's
// natural width, which is the place an off-by-one in the ladder shows.
//
// The badge-only fixture at the end is the other load-bearing case: with no
// attention line to keep, rung 4 must not exist at all. An unconditional
// rung 4 is an empty left half beside an empty right half, it "fits" at
// every width, and it would render the header as a row of spaces instead of
// letting rung 5 truncate the badge.
func TestHeaderLineDegradesInOrder(t *testing.T) {
	p := headerPieces{
		badge:          "afk Dashboard",
		attention:      "● 2 awaiting in gamma · 1 in delta",
		attentionShort: "● 3 awaiting in 2 other projects",
		hint:           "p: switch projects",
	}
	withHint := func(l string) int {
		return lipgloss.Width(l) + headerMinGap + lipgloss.Width(p.hint)
	}
	named := withHint("afk Dashboard ● 2 awaiting in gamma · 1 in delta")
	aggregate := withHint("afk Dashboard ● 3 awaiting in 2 other projects")
	noHint := lipgloss.Width("afk Dashboard ● 3 awaiting in 2 other projects")
	noBadge := lipgloss.Width("● 3 awaiting in 2 other projects")

	cases := []struct {
		name         string
		width        int
		has, lacks   []string
		rightAligned bool
	}{
		{"everything fits", named,
			[]string{"afk Dashboard", "gamma", "delta", "p: switch projects"}, nil, true},
		{"one short: the names collapse to the aggregate", named - 1,
			[]string{"afk Dashboard", "2 other projects", "p: switch projects"}, []string{"gamma"}, true},
		{"then the hint", aggregate - 1,
			[]string{"afk Dashboard", "2 other projects"}, []string{"p: switch projects"}, true},
		{"then the badge, and the attention survives it", noHint - 1,
			[]string{"2 other projects"}, []string{"afk Dashboard"}, true},
		{"below the attention line alone it truncates", noBadge - 1,
			[]string{"3 awaiting"}, []string{"afk Dashboard"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := headerLine(p, tc.width)
			if w := lipgloss.Width(got); w > tc.width {
				t.Fatalf("width %d exceeds the budget %d: %q", w, tc.width, got)
			}
			if tc.rightAligned && lipgloss.Width(got) != tc.width {
				t.Errorf("a fitting header must span the full width (%d), got %d: %q",
					tc.width, lipgloss.Width(got), got)
			}
			if lipgloss.Height(got) != 1 {
				t.Errorf("width %d: header is %d rows, want exactly 1: %q",
					tc.width, lipgloss.Height(got), got)
			}
			for _, want := range tc.has {
				if !strings.Contains(got, want) {
					t.Errorf("width %d: %q lacks %q", tc.width, got, want)
				}
			}
			for _, banned := range tc.lacks {
				if strings.Contains(got, banned) {
					t.Errorf("width %d: %q still has %q", tc.width, got, banned)
				}
			}
		})
	}

	if got := headerLine(p, 0); got != "" {
		t.Fatalf("headerLine at width 0 = %q, want empty (truncate's <=0 contract)", got)
	}

	quiet := headerPieces{badge: "afk Dashboard"}
	if got := headerLine(quiet, 40); got != "afk Dashboard"+strings.Repeat(" ", 27) {
		t.Errorf("a badge-only header at width 40 = %q, want the badge padded out", got)
	}
	if got := headerLine(quiet, 10); got != "afk Dashb…" {
		t.Fatalf("a badge-only header at width 10 = %q, want the badge truncated, not blanked", got)
	}
}

// TestHeaderLineDegradesWithUsage pins the ladder when a usage reading is
// standing: the hint goes first, then the names collapse, then the long usage
// text gives way to the short, then the badge, and the short usage outlives
// the badge but not the attention line. Each row sits one cell under the
// previous rung's natural width. Load-bearing: "the badge goes, the short
// usage stays" (the reason the short form exists) and "usage-only survives
// with no attention at all", the case where the left half is empty.
func TestHeaderLineDegradesWithUsage(t *testing.T) {
	p := headerPieces{
		badge:          "afk Dashboard",
		attention:      "● 2 awaiting in gamma · 1 in delta",
		attentionShort: "● 3 awaiting in 2 other projects",
		usage:          "usage: paused until Mon 2 Jan 15:04 · Week (all models) 91%",
		usageShort:     "paused · 91%",
		hint:           "p: switch projects",
	}
	w := lipgloss.Width
	full := w("afk Dashboard ● 2 awaiting in gamma · 1 in delta") + headerMinGap + w(p.usage) + 2 + w(p.hint)
	noHint := w("afk Dashboard ● 2 awaiting in gamma · 1 in delta") + headerMinGap + w(p.usage)
	aggregate := w("afk Dashboard ● 3 awaiting in 2 other projects") + headerMinGap + w(p.usage)
	short := w("afk Dashboard ● 3 awaiting in 2 other projects") + headerMinGap + w(p.usageShort)
	noBadge := w("● 3 awaiting in 2 other projects") + headerMinGap + w(p.usageShort)
	attOnly := w("● 3 awaiting in 2 other projects")

	cases := []struct {
		name       string
		width      int
		has, lacks []string
	}{
		{"everything fits", full,
			[]string{"afk Dashboard", "gamma", "usage: paused", "p: switch projects"}, nil},
		{"one short: the hint goes first", full - 1,
			[]string{"afk Dashboard", "gamma", "usage: paused"}, []string{"p: switch"}},
		{"then the names collapse", noHint - 1,
			[]string{"afk Dashboard", "2 other projects", "usage: paused"}, []string{"gamma"}},
		{"then the long usage becomes the short", aggregate - 1,
			[]string{"afk Dashboard", "2 other projects", "paused · 91%"}, []string{"usage:"}},
		{"then the badge, and the short usage survives it", short - 1,
			[]string{"2 other projects", "paused · 91%"}, []string{"afk Dashboard"}},
		{"then the short usage, and the attention survives it", noBadge - 1,
			[]string{"2 other projects"}, []string{"paused · 91%", "afk Dashboard"}},
		{"below the attention line it truncates", attOnly - 1,
			[]string{"3 awaiting"}, []string{"paused", "afk Dashboard"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := headerLine(p, tc.width)
			if gw := lipgloss.Width(got); gw > tc.width {
				t.Fatalf("width %d exceeds the budget %d: %q", gw, tc.width, got)
			}
			if lipgloss.Height(got) != 1 {
				t.Errorf("width %d: header is not one row: %q", tc.width, got)
			}
			for _, want := range tc.has {
				if !strings.Contains(got, want) {
					t.Errorf("width %d: %q lacks %q", tc.width, got, want)
				}
			}
			for _, banned := range tc.lacks {
				if strings.Contains(got, banned) {
					t.Errorf("width %d: %q still has %q", tc.width, got, banned)
				}
			}
		})
	}

	t.Run("staleness outlives the short usage", func(t *testing.T) {
		q := p
		q.stale = "stale · discovery failing 12s"
		got := headerLine(q, w(q.stale)+2)
		if !strings.Contains(got, "stale") || strings.Contains(got, "paused") || strings.Contains(got, "awaiting") {
			t.Errorf("got %q, want the staleness signal alone", got)
		}
	})

	t.Run("usage alone, no attention, no hint", func(t *testing.T) {
		q := headerPieces{badge: "afk Dashboard", usage: p.usage, usageShort: p.usageShort}
		got := headerLine(q, w("afk Dashboard")+headerMinGap+w(q.usageShort))
		if !strings.Contains(got, "afk Dashboard") || !strings.Contains(got, "paused · 91%") {
			t.Errorf("got %q, want badge and short usage", got)
		}
		got = headerLine(q, w(q.usageShort)+2)
		if strings.Contains(got, "afk Dashboard") || !strings.Contains(got, "paused · 91%") {
			t.Errorf("got %q, want the short usage to outlive the badge", got)
		}
		if lipgloss.Width(got) != w(q.usageShort)+2 {
			t.Errorf("a fitting header spans the full width, got %d", lipgloss.Width(got))
		}
	})
}

// usageModel is a model holding a usage reading taken "now", for the header
// tests. action "" stores no governor decision.
func usageModel(action string) Model {
	m := testModel(sess("a"))
	m.projects = nil
	wake := m.deps.Now().Add(2 * time.Hour)
	reset := m.deps.Now().Add(3 * time.Hour)
	f := usagestate.File{
		At: m.deps.Now(),
		Windows: []usagestate.Window{
			{Kind: provider.WindowSession, UsedPct: 40, ResetsAt: &reset},
			{Kind: provider.WindowWeekly, UsedPct: 91, ResetsAt: &reset},
		},
	}
	if action != "" {
		f.Governor = &usagestate.Governor{Action: action, WakeAt: &wake, At: m.deps.Now()}
	}
	m.build = buildview.Snapshot{Usage: f, UsageKnown: true}
	m.buildKnown = true
	return m
}

// TestHeaderUsageSegment pins what the header says about usage through the
// real Model path: the long form at a wide width, the short form at a narrow
// one, and nothing at all without a reading (also when the snapshot says the
// reading is unknown, and when no build data has arrived).
func TestHeaderUsageSegment(t *testing.T) {
	long := usageModel("pause")
	long.width = 160
	got := xansi.Strip(long.renderHeader())
	if !strings.Contains(got, "usage: paused until ") || !strings.Contains(got, "· Week (all models) 91%") {
		t.Errorf("160 columns = %q, want the long segment", got)
	}
	if lipgloss.Height(long.renderHeader()) != 1 {
		t.Errorf("160 columns is not one row")
	}

	narrow := usageModel("pause")
	narrow.width = 30
	got = xansi.Strip(narrow.renderHeader())
	if !strings.Contains(got, "paused · 91%") || strings.Contains(got, "usage:") {
		t.Errorf("30 columns = %q, want the short segment", got)
	}

	for name, mk := range map[string]func() Model{
		"no build data": func() Model { m := usageModel("pause"); m.buildKnown = false; return m },
		"reading not known": func() Model {
			m := usageModel("pause")
			m.build.UsageKnown = false
			return m
		},
		"never loaded": func() Model { m := testModel(sess("a")); m.projects = nil; return m },
	} {
		t.Run(name, func(t *testing.T) {
			m := mk()
			m.width = 160
			got := xansi.Strip(m.renderHeader())
			if strings.Contains(got, "usage") || strings.Contains(got, "paused") {
				t.Errorf("%s: header = %q, want no usage text", name, got)
			}
		})
	}

	t.Run("a run decision is dim and a pause is a warning", func(t *testing.T) {
		ok, held := usageModel("run"), usageModel("pause")
		ok.width, held.width = 160, 160
		pOK, pHeld := ok.headerPieces(), held.headerPieces()
		if pOK.usage != theme.dim.Render(xansi.Strip(pOK.usage)) {
			t.Errorf("run usage = %q, want theme.dim", pOK.usage)
		}
		if pHeld.usage != theme.awaitingFlag.Render(xansi.Strip(pHeld.usage)) {
			t.Errorf("pause usage = %q, want the warning style", pHeld.usage)
		}
	})
}

// TestHeaderLineKeepsStalenessLast extends the ladder above with the piece
// that only exists while discovery is failing, and pins the one ordering
// decision that is not obvious from the strings: staleness outranks the
// attention line, so it is the LAST thing standing before truncation.
//
// The load-bearing rows are the two below the badge's exit. "the attention
// line goes, the staleness survives" is the new rung, and it is the one a
// reader is most likely to invert on the reasonable-sounding grounds that an
// awaiting count is actionable and a staleness notice is not — the point
// being that a frozen list makes the count itself untrustworthy, since both
// come from discovery. "truncates the staleness signal, not the attention
// line" pins the same ranking one rung further down, in the branch after the
// candidate loop, which the rung rows cannot reach.
//
// Widths are computed from the fixture, as above, each one cell below the
// previous rung's natural width.
func TestHeaderLineKeepsStalenessLast(t *testing.T) {
	p := headerPieces{
		badge:          "afk Dashboard",
		stale:          "stale · discovery failing 30s",
		attention:      "● 2 awaiting in gamma · 1 in delta",
		attentionShort: "● 3 awaiting in 2 other projects",
		hint:           "p: switch projects",
	}
	withHint := func(l string) int {
		return lipgloss.Width(l) + headerMinGap + lipgloss.Width(p.hint)
	}
	named := withHint("afk Dashboard stale · discovery failing 30s  ● 2 awaiting in gamma · 1 in delta")
	aggregate := withHint("afk Dashboard stale · discovery failing 30s  ● 3 awaiting in 2 other projects")
	noHint := lipgloss.Width("afk Dashboard stale · discovery failing 30s  ● 3 awaiting in 2 other projects")
	noBadge := lipgloss.Width("stale · discovery failing 30s  ● 3 awaiting in 2 other projects")
	staleOnly := lipgloss.Width("stale · discovery failing 30s")

	cases := []struct {
		name         string
		width        int
		has, lacks   []string
		rightAligned bool
	}{
		{"everything fits", named,
			[]string{"afk Dashboard", "discovery failing 30s", "gamma", "delta", "p: switch projects"},
			nil, true},
		{"one short: the names collapse to the aggregate", named - 1,
			[]string{"afk Dashboard", "discovery failing 30s", "2 other projects", "p: switch projects"},
			[]string{"gamma"}, true},
		{"then the hint", aggregate - 1,
			[]string{"afk Dashboard", "discovery failing 30s", "2 other projects"},
			[]string{"p: switch projects"}, true},
		{"then the badge, and both alerts survive it", noHint - 1,
			[]string{"discovery failing 30s", "2 other projects"},
			[]string{"afk Dashboard"}, true},
		{"then the attention line, and the staleness survives it", noBadge - 1,
			[]string{"discovery failing 30s"},
			[]string{"2 other projects", "3 awaiting", "afk Dashboard"}, true},
		{"below the staleness signal alone it truncates that, not the attention line", staleOnly - 1,
			[]string{"stale · discovery"},
			[]string{"awaiting", "afk Dashboard"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := headerLine(p, tc.width)
			if w := lipgloss.Width(got); w > tc.width {
				t.Fatalf("width %d exceeds the budget %d: %q", w, tc.width, got)
			}
			if tc.rightAligned && lipgloss.Width(got) != tc.width {
				t.Errorf("a fitting header must span the full width (%d), got %d: %q",
					tc.width, lipgloss.Width(got), got)
			}
			if lipgloss.Height(got) != 1 {
				t.Errorf("width %d: header is %d rows, want exactly 1: %q",
					tc.width, lipgloss.Height(got), got)
			}
			for _, want := range tc.has {
				if !strings.Contains(got, want) {
					t.Errorf("width %d: %q lacks %q", tc.width, got, want)
				}
			}
			for _, banned := range tc.lacks {
				if strings.Contains(got, banned) {
					t.Errorf("width %d: %q still has %q", tc.width, got, banned)
				}
			}
		})
	}

	// A staleness signal with nothing awaiting anywhere must still shed the
	// badge and still be the thing that survives — the same shape the
	// badge-only fixture pins for the quiet header, one rung over.
	failing := headerPieces{badge: "afk Dashboard", stale: "stale · discovery failing 30s"}
	if got := headerLine(failing, staleOnly); got != failing.stale {
		t.Errorf("a badge+staleness header at width %d = %q, want the badge shed and the staleness kept",
			staleOnly, got)
	}
	if got := headerLine(failing, staleOnly-1); got != "stale · discovery failing 3…" {
		t.Fatalf("a badge+staleness header at width %d = %q, want the staleness truncated, not the badge kept",
			staleOnly-1, got)
	}
}

// TestRenderHeaderReportsOnlyOtherProjectsAwaiting replaces
// TestRenderHeaderCountsAwaitingSessions. The number the header carries is
// still the one that asks for action, but it is now explicitly NOT the
// active project's: those rows are a few lines below with a dot in their
// flag column, and repeating the total up here was the redundancy this
// redesign spent the row on.
//
// Both rows are load-bearing. "only the active project is awaiting" is the
// regression this inverts — the old header printed "1 awaiting input" for
// exactly this model — and "nothing awaiting anywhere" proves the empty
// result renders as nothing at all rather than "0 awaiting".
func TestRenderHeaderReportsOnlyOtherProjectsAwaiting(t *testing.T) {
	waiting := sess("a")
	waiting.AwaitingInput = true

	t.Run("only the active project is awaiting", func(t *testing.T) {
		m := attentionModel(waiting, sess("b"))
		m.projectSessions = map[string][]discover.SessionState{
			"/repos/alpha": {{AwaitingInput: true}},
		}
		if got := xansi.Strip(m.renderHeader()); strings.Contains(got, "awaiting") {
			t.Fatalf("renderHeader() = %q, must not report the active project's own awaiting rows", got)
		}
	})

	t.Run("nothing awaiting anywhere", func(t *testing.T) {
		m := attentionModel(sess("a"))
		m.projectSessions = nil
		got := xansi.Strip(m.renderHeader())
		for _, banned := range []string{"awaiting", "0 "} {
			if strings.Contains(got, banned) {
				t.Fatalf("renderHeader() = %q, must render no attention line at all, not %q", got, banned)
			}
		}
	})
}

// TestRenderHeaderSwitchHintVisibleAtEightyColumns keeps the original
// contract this test was written for, which the redesign makes literal
// again: at a REAL 80-column terminal, with the attention line populated,
// the `p: switch projects` hint must still be on screen. 80 columns is the
// width the ladder's first two rungs are tuned against, and the rung that
// drops the hint must not be reached there.
func TestRenderHeaderSwitchHintVisibleAtEightyColumns(t *testing.T) {
	m := attentionModel(sess("a"), sess("b"))
	m.width = 80

	got := m.renderHeader()
	stripped := xansi.Strip(got)
	if !strings.Contains(stripped, headerSwitchHint) {
		t.Fatalf("renderHeader() at width 80 = %q, want it to contain %q", stripped, headerSwitchHint)
	}
	if !strings.Contains(stripped, "awaiting in gamma") {
		t.Errorf("renderHeader() at width 80 = %q, want the named attention line to survive", stripped)
	}
	if lipgloss.Width(got) > 80 {
		t.Fatalf("header width %d exceeds the 80-column budget: %q", lipgloss.Width(got), got)
	}
}

// TestRenderHeaderIsExactlyOneRow is the invariant layoutDims,
// mouseFrameLayout and paneScreenGeometry all depend on: each measures
// lipgloss.Height(m.renderHeader()) independently, so a header that grew a
// second row in ANY state would desynchronise mouse routing from layout and
// send every click a row off (TestMouseFrameLayoutAgreesWithLayoutDims is
// the other end of that contract).
//
// The widths are chosen to walk the ladder rather than to be round: 200
// takes the named rung, 69 is one cell below the named rung's natural width
// with these exact pieces and so takes the aggregate, and 20 falls through
// to truncate. The zero-width row is the one a reader is most likely to
// break — the frame is built before the first WindowSizeMsg lands.
//
// The two failing-discovery rows are the states the rest of the package
// almost never renders, and the ones where the left half is at its widest:
// the staleness signal is a fourth piece competing for the same single row,
// and it is on screen exactly when the user is least able to afford a
// mis-routed click.
func TestRenderHeaderIsExactlyOneRow(t *testing.T) {
	quiet := func() Model {
		m := testModel(sess("a"))
		m.projects = nil
		return m
	}
	hinted := func() Model {
		m := testModel(sess("a"))
		m.projects = []project.Project{{Path: "/a", Name: "alpha"}, {Path: "/b", Name: "beta"}}
		return m
	}
	failing := func() Model {
		m := attentionModel()
		m.loadFailingSince = m.deps.Now().Add(-45 * time.Second)
		return m
	}

	cases := []struct {
		name  string
		model Model
		width int
	}{
		{"attention empty, hint absent", quiet(), 120},
		{"attention empty, hint present", hinted(), 120},
		{"attention named, hint present", attentionModel(), 200},
		{"attention aggregate, hint present", attentionModel(), 69},
		{"too narrow for anything but a truncated attention line", attentionModel(), 20},
		{"zero width, before the first WindowSizeMsg", attentionModel(), 0},
		{"discovery failing, everything on screen at once", failing(), 200},
		{"discovery failing, too narrow for anything but a truncated staleness signal", failing(), 20},
	}
	// A long usage segment at every width the layout is tested at, with and
	// without attention and failing discovery: the segment must be cut, never
	// wrapped, and 20 columns is where only truncation is left.
	withUsage := func(m Model) Model {
		u := usageModel("pause")
		m.build, m.buildKnown = u.build, true
		return m
	}
	for _, w := range []int{200, 120, 80, 60, 40, 20} {
		for _, st := range []struct {
			name string
			mk   func() Model
		}{
			{"quiet", quiet}, {"hinted", hinted}, {"attention", func() Model { return attentionModel() }}, {"failing", failing},
		} {
			cases = append(cases, struct {
				name  string
				model Model
				width int
			}{fmt.Sprintf("usage reading, %s, %d columns", st.name, w), withUsage(st.mk()), w})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model
			m.width = tc.width
			got := m.renderHeader()
			if h := lipgloss.Height(got); h != 1 {
				t.Fatalf("renderHeader() at width %d is %d rows, want exactly 1: %q",
					tc.width, h, got)
			}
			if w := lipgloss.Width(got); w > tc.width {
				t.Fatalf("renderHeader() at width %d is %d cells wide: %q", tc.width, w, got)
			}
		})
	}

	// The two middle rungs must actually be the rungs their names claim,
	// otherwise this test would pass while measuring the same state six
	// times.
	wide, narrow := attentionModel(), attentionModel()
	wide.width, narrow.width = 200, 69
	if got := xansi.Strip(wide.renderHeader()); !strings.Contains(got, "in gamma") {
		t.Errorf("width 200 = %q, want the NAMED attention rung", got)
	}
	if got := xansi.Strip(narrow.renderHeader()); !strings.Contains(got, "2 other projects") {
		t.Errorf("width 69 = %q, want the AGGREGATE attention rung", got)
	}
	pinched := failing()
	pinched.width = 20
	if got := xansi.Strip(pinched.renderHeader()); !strings.Contains(got, "stale") || strings.Contains(got, "awaiting") {
		t.Errorf("width 20 while discovery fails = %q, want the TRUNCATED STALENESS rung", got)
	}
}

// --- attentionNamed / attentionAggregate: the two attention wordings ---

// TestAttentionNamed pins the named wording clause by clause, against the
// builder directly rather than through renderHeader, because the header
// tests all wire the same two other projects and a width ladder on top:
// none of them can reach the one-project or many-project wordings, and a
// plural bug in either would ship.
//
// Load-bearing rows: "one other project" is the only case with no joiner at
// all, and is the wording a single awaiting session elsewhere produces —
// the commonest real case and the one the header tests never build.
// "counts other than one" guards the word "awaiting" belonging to the FIRST
// clause only; a loop that appended it every time reads as a stutter and
// blows the ladder's width budget.
func TestAttentionNamed(t *testing.T) {
	cases := []struct {
		name   string
		others []otherProjectAwaiting
		want   string
	}{
		{"nothing awaiting elsewhere", nil, ""},
		{"empty slice is the same signal", []otherProjectAwaiting{}, ""},
		{
			"one other project",
			[]otherProjectAwaiting{{name: "gamma", count: 1}},
			awaitingGlyph + " 1 awaiting in gamma",
		},
		{
			"counts other than one",
			[]otherProjectAwaiting{{name: "gamma", count: 2}, {name: "delta", count: 1}},
			awaitingGlyph + " 2 awaiting in gamma · 1 in delta",
		},
		{
			"three keep joining with the same separator",
			[]otherProjectAwaiting{
				{name: "gamma", count: 3}, {name: "delta", count: 2}, {name: "eps", count: 1},
			},
			awaitingGlyph + " 3 awaiting in gamma · 2 in delta · 1 in eps",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := xansi.Strip(attentionNamed(tc.others)); got != tc.want {
				t.Fatalf("attentionNamed() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAttentionAggregate pins the collapsed wording, and specifically its
// plurals. The singular — "3 awaiting in 1 other project" — is the row the
// plan called out by name and the one nothing else in the package reaches:
// every header test wires exactly two other projects, and headerPieces
// discards the aggregate whenever it is not narrower than the named form,
// which for one short project name it usually is not. The builder is still
// the thing that has to be right, because a long project name does reach
// this wording, so it is asserted here where the width ladder cannot hide
// it.
//
// The other load-bearing row is the total: the aggregate sums the counts
// across projects and reports the PROJECT count separately, so "6 awaiting
// in 4 other projects" is two different numbers and a builder that reused
// one for the other would still look plausible.
func TestAttentionAggregate(t *testing.T) {
	cases := []struct {
		name   string
		others []otherProjectAwaiting
		want   string
	}{
		{"nothing awaiting elsewhere", nil, ""},
		{"empty slice is the same signal", []otherProjectAwaiting{}, ""},
		{
			"one other project is singular",
			[]otherProjectAwaiting{{name: "gamma", count: 3}},
			awaitingGlyph + " 3 awaiting in 1 other project",
		},
		{
			"one other project with one session is still singular",
			[]otherProjectAwaiting{{name: "gamma", count: 1}},
			awaitingGlyph + " 1 awaiting in 1 other project",
		},
		{
			"several projects are plural and the total is summed",
			[]otherProjectAwaiting{
				{name: "gamma", count: 3}, {name: "delta", count: 1},
				{name: "eps", count: 1}, {name: "zeta", count: 1},
			},
			awaitingGlyph + " 6 awaiting in 4 other projects",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := xansi.Strip(attentionAggregate(tc.others)); got != tc.want {
				t.Fatalf("attentionAggregate() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHeaderPiecesKeepsASingularAggregateWhenItIsNarrower is the end of the
// singular wording's path: headerPieces collapses attentionShort back onto
// the named form whenever the aggregate is not actually shorter, so with one
// other project the singular phrase survives only when that project's name
// is longer than "1 other project". That is the case this pins, because it
// is the only way a user ever sees the singular on screen.
func TestHeaderPiecesKeepsASingularAggregateWhenItIsNarrower(t *testing.T) {
	long := "a-very-long-project-name-that-outruns-the-phrase"
	m := testModel(sess("a"))
	m.projects = []project.Project{
		{Name: "alpha", Path: "/repos/alpha"},
		{Name: long, Path: "/repos/long"},
	}
	m.activeProject = 0
	m.projectSessions = map[string][]discover.SessionState{
		"/repos/long": {{Name: "x", AwaitingInput: true}, {Name: "y", AwaitingInput: true}},
	}

	p := m.headerPieces()
	if got, want := xansi.Strip(p.attention), awaitingGlyph+" 2 awaiting in "+long; got != want {
		t.Fatalf("named attention = %q, want %q", got, want)
	}
	if got, want := xansi.Strip(p.attentionShort), awaitingGlyph+" 2 awaiting in 1 other project"; got != want {
		t.Fatalf("aggregate attention = %q, want the singular %q", got, want)
	}
}

// --- awaitingElsewhere: cross-project attention data (W1.T1) ---

// TestAwaitingElsewhere covers the cross-project attention helper W2.T1 will
// consume to build the header's attention line. Two rows are load-bearing:
// "several other projects" proves the result follows sortSwitcherProjects's
// order rather than Go's randomized map iteration — the projects are given
// open counts that put them in the REVERSE of alphabetical order, so a
// helper that iterated m.projectSessions directly, or sorted by name instead
// of reusing sortSwitcherProjects, would produce the alphabetical order and
// fail this case. "the active project itself awaiting" proves the exclusion
// is a real path match, not an accidental side effect of the zero-count
// filter that would happen to hide it anyway.
func TestAwaitingElsewhere(t *testing.T) {
	alpha := project.Project{Path: "/repos/alpha", Name: "alpha"}
	beta := project.Project{Path: "/repos/beta", Name: "beta"}
	gamma := project.Project{Path: "/repos/gamma", Name: "gamma"}
	delta := project.Project{Path: "/repos/delta", Name: "delta"}

	tests := []struct {
		name            string
		projects        []project.Project
		activeProject   int
		projectSessions map[string][]discover.SessionState
		projectOpens    map[string]int
		want            []otherProjectAwaiting
	}{
		{
			name:          "no other project awaiting -> empty result",
			projects:      []project.Project{alpha, beta},
			activeProject: 0,
			projectSessions: map[string][]discover.SessionState{
				"/repos/beta": {{AwaitingInput: false}},
			},
			want: nil,
		},
		{
			name:          "one other project awaiting",
			projects:      []project.Project{alpha, beta},
			activeProject: 0,
			projectSessions: map[string][]discover.SessionState{
				"/repos/beta": {{AwaitingInput: true}, {AwaitingInput: true}},
			},
			want: []otherProjectAwaiting{{name: "beta", count: 2}},
		},
		{
			name:          "several other projects awaiting, ordered by sortSwitcherProjects not map iteration",
			projects:      []project.Project{alpha, beta, gamma, delta},
			activeProject: 0,
			projectSessions: map[string][]discover.SessionState{
				"/repos/beta":  {{AwaitingInput: true}},
				"/repos/gamma": {{AwaitingInput: true}, {AwaitingInput: true}, {AwaitingInput: true}},
				"/repos/delta": {{AwaitingInput: true}, {AwaitingInput: true}},
			},
			projectOpens: map[string]int{"/repos/gamma": 5, "/repos/delta": 3, "/repos/beta": 1},
			want: []otherProjectAwaiting{
				{name: "gamma", count: 3},
				{name: "delta", count: 2},
				{name: "beta", count: 1},
			},
		},
		{
			name:          "the active project itself awaiting is excluded",
			projects:      []project.Project{alpha, beta},
			activeProject: 0,
			projectSessions: map[string][]discover.SessionState{
				"/repos/alpha": {{AwaitingInput: true}},
			},
			want: nil,
		},
		{
			name:          "an out-of-range activeProject treats every project as other",
			projects:      []project.Project{alpha, beta},
			activeProject: 5,
			projectSessions: map[string][]discover.SessionState{
				"/repos/alpha": {{AwaitingInput: true}},
			},
			want: []otherProjectAwaiting{{name: "alpha", count: 1}},
		},
		{
			name:            "a nil m.projectSessions is legal and yields nothing",
			projects:        []project.Project{alpha, beta},
			activeProject:   0,
			projectSessions: nil,
			want:            nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel()
			m.projects = tc.projects
			m.activeProject = tc.activeProject
			m.projectSessions = tc.projectSessions
			m.projectOpens = tc.projectOpens

			got := m.awaitingElsewhere()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("awaitingElsewhere() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// --- renderList: the session box's border label (W3.T1) ---

// sessionListLabelModel builds a Model with one project per name and idx as
// the active one, mirroring attentionModel's project set but without any
// projectSessions/projectOpens data — this suite is about which NAME lands
// on the box, not about the header's cross-project attention line.
func sessionListLabelModel(names []string, idx int) Model {
	m := testModel(sess("alpha"))
	var projects []project.Project
	for _, n := range names {
		projects = append(projects, project.Project{Path: "/repos/" + n, Name: n})
	}
	m.projects = projects
	m.activeProject = idx
	return m
}

// TestSessionListLabel pins sessionListLabel's two branches directly, without
// going through renderList's box drawing. The zero-project and
// out-of-range/negative-index rows are the load-bearing ones: every
// zero-Deps Model in this package's suite never sets Deps.Projects, so those
// rows are what most of the suite already exercises, not a rare corner — a
// helper that returned "" for them would blank the box label package-wide.
func TestSessionListLabel(t *testing.T) {
	tests := []struct {
		name  string
		model Model
		want  string
	}{
		{"several projects, second active", sessionListLabelModel([]string{"alpha", "beta", "gamma"}, 1), "beta"},
		{"exactly one project, active", sessionListLabelModel([]string{"solo"}, 0), "solo"},
		{"zero projects falls back", sessionListLabelModel(nil, 0), sessionListFallbackLabel},
		{"out-of-range active index falls back", sessionListLabelModel([]string{"alpha", "beta"}, 5), sessionListFallbackLabel},
		{"negative active index falls back", sessionListLabelModel([]string{"alpha"}, -1), sessionListFallbackLabel},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.model.sessionListLabel(); got != tc.want {
				t.Errorf("sessionListLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderListTopEdgeCarriesTheProjectLabel proves the label actually
// reaches the rendered box, not just the pure helper above: renderList must
// hand sessionListLabel() to labelBordersStyled rather than keeping the old
// "Sessions" literal.
func TestRenderListTopEdgeCarriesTheProjectLabel(t *testing.T) {
	m := sessionListLabelModel([]string{"alpha", "beta"}, 1)
	m.width, m.height = 120, 40
	m.ready = true
	m.sizeSessionList()
	listOuter, _, bodyHeight := m.layoutDims()

	top := strings.Split(xansi.Strip(m.renderList(listOuter, bodyHeight)), "\n")[0]
	if !strings.Contains(top, "beta") {
		t.Fatalf("top edge does not carry the active project's name:\n%s", top)
	}
	if strings.Contains(top, sessionListFallbackLabel) {
		t.Fatalf("top edge still carries the %q fallback with a known active project:\n%s",
			sessionListFallbackLabel, top)
	}
}

// TestRenderListTopEdgeFallsBackToSessionsWithNoProject is
// TestRenderListTopEdgeCarriesTheProjectLabel's counterpart for testModel's
// own default shape (no Deps.Projects at all), which is the case every other
// pre-existing sidebar test in this package already renders under.
func TestRenderListTopEdgeFallsBackToSessionsWithNoProject(t *testing.T) {
	m := testModel(sess("alpha"))
	listOuter, _, bodyHeight := m.layoutDims()

	top := strings.Split(xansi.Strip(m.renderList(listOuter, bodyHeight)), "\n")[0]
	if !strings.Contains(top, sessionListFallbackLabel) {
		t.Fatalf("top edge does not fall back to %q with no known project:\n%s", sessionListFallbackLabel, top)
	}
}

// TestRenderListLabelTruncatesALongProjectNameWithoutOverflow pins the plan's
// claim that paintEdge already handles a project name wider than the sidebar:
// no panic, and the box's width to the cell is unaffected by the label it
// carries — renderList's own width arithmetic (sidebarContentWidth, the
// sizedBox call) is untouched by this task, so a widened box would mean the
// label leaked outside paintEdge's truncation.
func TestRenderListLabelTruncatesALongProjectNameWithoutOverflow(t *testing.T) {
	longName := strings.Repeat("x", 200)
	m := sessionListLabelModel([]string{longName}, 0)
	m.width, m.height = 80, 24
	m.ready = true
	m.sizeSessionList()
	listOuter, _, bodyHeight := m.layoutDims()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("renderList panicked on an over-long project name: %v", r)
		}
	}()

	out := m.renderList(listOuter, bodyHeight)
	lines := strings.Split(out, "\n")
	want := sidebarRenderedWidth(listOuter)
	for i, line := range lines {
		if w := lipgloss.Width(line); w != want {
			t.Errorf("line %d width = %d, want %d (sidebarRenderedWidth, unaffected by the label)", i, w, want)
		}
	}
}

// TestRenderDetailTitleStyleUnchanged pins that renderDetail's border label
// still flows through labelBorders/theme.boxTitle unchanged: the
// labelBordersStyled sibling function must not leak its stronger title style
// (boxTitleStrong) into any caller besides renderList's session box.
func TestRenderDetailTitleStyleUnchanged(t *testing.T) {
	m := testModel(sess("alpha"))
	m.selectSessionIndex(0)
	_, mainOuter, bodyHeight := m.layoutDims()

	title, _ := m.detailBoxLabels()
	if title == "" {
		t.Fatalf("test setup: detail box has no title to compare against")
	}

	top := strings.Split(m.renderDetail(mainOuter, bodyHeight), "\n")[0]
	if !strings.Contains(top, theme.boxTitle.Render(title)) {
		t.Fatalf("detail box title is not rendered with theme.boxTitle:\n%s", top)
	}
	if strings.Contains(top, theme.boxTitleStrong.Render(title)) {
		t.Fatalf("detail box title leaked renderList's boxTitleStrong style:\n%s", top)
	}
}

// TestLabelBordersDelegatesToStyledWithBoxTitle pins the required delegation:
// labelBorders must BE labelBordersStyled with titleStyle fixed to
// theme.boxTitle, not a second implementation that could drift from it and
// silently change every existing caller (the detail pane, the switcher,
// palette and form modals).
func TestLabelBordersDelegatesToStyledWithBoxTitle(t *testing.T) {
	box := sizedBox(theme.detailBox, 20, 5).Render("alpha\nbeta\ngamma")

	got := labelBorders(box, theme.detailBox, "Sessions", "hint")
	want := labelBordersStyled(box, theme.detailBox, "Sessions", "hint", theme.boxTitle)
	if got != want {
		t.Fatalf("labelBorders diverged from labelBordersStyled(..., theme.boxTitle):\ngot:  %q\nwant: %q", got, want)
	}
}

// TestLabelBordersStyledChangesOnlyTheLabelStyle proves the new titleStyle
// parameter changes the label's own rendering and nothing about the box's
// geometry — the same geometry contract TestLabelBordersKeepsGeometry pins
// for the unstyled path in labelborders_test.go.
func TestLabelBordersStyledChangesOnlyTheLabelStyle(t *testing.T) {
	box := sizedBox(theme.detailBox, 20, 5).Render("alpha\nbeta\ngamma")

	plain := labelBordersStyled(box, theme.detailBox, "Sessions", "", theme.boxTitle)
	strong := labelBordersStyled(box, theme.detailBox, "Sessions", "", theme.boxTitleStrong)
	if plain == strong {
		t.Fatalf("boxTitleStrong rendered identically to boxTitle; fixture cannot distinguish the two styles")
	}

	pLines, sLines := strings.Split(plain, "\n"), strings.Split(strong, "\n")
	if len(pLines) != len(sLines) {
		t.Fatalf("styled variants have different heights: %d vs %d", len(pLines), len(sLines))
	}
	for i := range pLines {
		if w1, w2 := lipgloss.Width(pLines[i]), lipgloss.Width(sLines[i]); w1 != w2 {
			t.Errorf("line %d width differs between title styles: %d vs %d", i, w1, w2)
		}
		if i > 0 && pLines[i] != sLines[i] {
			t.Errorf("interior line %d changed between title styles", i)
		}
	}
}

// TestTitleStylesStayReadableOnBothSchemes is the regression test for a
// defect that shipped: boxTitleStrong was Foreground(p.White) with no
// Background, and palette.Light.White is "#ffffff", so the session box's
// project label — the frame's primary identifier — rendered white-on-white
// and was unreadable on a light terminal.
//
// Nothing in the package could catch it. The golden fixtures are plain text
// with the ANSI stripped, so a colour is invisible to them, and the only
// other assertion about this style (TestLabelBordersStyledChangesOnlyThe
// LabelStyle) checks that it DIFFERS from boxTitle — which white-on-white
// satisfies perfectly.
//
// Load-bearing rows: light/boxTitleStrong is the exact defect. The dark rows
// guard the obvious over-correction, swapping to a near-black that then
// disappears on a dark terminal. boxTitle is here because it reaches the
// screen through the same labelBordersStyled path and is the style
// boxTitleStrong has to stay distinct from — a "fix" that collapsed the two
// would be readable and still wrong.
//
// Both schemes are live at runtime: applyTheme rebuilds the whole theme from
// palette.For(isDark) when tea.BackgroundColorMsg arrives.
func TestTitleStylesStayReadableOnBothSchemes(t *testing.T) {
	// Each scheme's terminal background, per the palette package doc: Dark is
	// "the scheme for a dark terminal background", Light is for a light one.
	// A style that sets a foreground and no background is drawn onto exactly
	// this.
	schemes := []struct {
		name       string
		scheme     palette.Scheme
		background string
	}{
		{"dark", palette.Dark, palette.Dark.Black},
		{"light", palette.Light, palette.Light.White},
	}

	for _, sc := range schemes {
		set := newStyleSet(sc.scheme)
		bg := lipgloss.Color(sc.background)

		titles := []struct {
			name  string
			style lipgloss.Style
		}{
			{"boxTitle", set.boxTitle},
			{"boxTitleStrong", set.boxTitleStrong},
		}
		for _, ti := range titles {
			t.Run(sc.name+"/"+ti.name, func(t *testing.T) {
				if _, bare := ti.style.GetBackground().(lipgloss.NoColor); !bare {
					t.Fatalf("%s now sets its own Background; this test's premise (the "+
						"label lands on the terminal background) no longer holds", ti.name)
				}
				fg := ti.style.GetForeground()
				if sameColor(fg, bg) {
					t.Fatalf("%s foreground is the %s terminal background (%s) — invisible",
						ti.name, sc.name, sc.background)
				}
				if got := contrastRatio(fg, bg); got < minTitleContrast {
					t.Errorf("%s contrast against the %s background (%s) = %.2f, want >= %.2f",
						ti.name, sc.name, sc.background, got, minTitleContrast)
				}
			})
		}

		t.Run(sc.name+"/strong-differs-from-plain", func(t *testing.T) {
			if sameColor(set.boxTitle.GetForeground(), set.boxTitleStrong.GetForeground()) {
				t.Fatalf("boxTitleStrong and boxTitle share a foreground on the %s scheme; "+
					"the session box's label no longer outweighs a detail-pane or modal title", sc.name)
			}
		})
	}
}

// minTitleContrast is WCAG 2.1's AA ratio for normal-size text. Chosen over
// the 3.0 large-text ratio because a border label is small, and over AAA's
// 7.0 because boxTitle's accent hues are deliberately hues rather than
// maximum-contrast greys and AAA would force them to plain black and white.
const minTitleContrast = 4.5

// contrastRatio is WCAG 2.1's relative-luminance ratio between two colors,
// 1.0 (identical) to 21.0 (black on white). A plain "is the foreground equal
// to the background" check would have caught the exact defect this file's
// readability test guards, but only that one: #fefefe on #ffffff is a
// different color and just as unreadable.
func contrastRatio(a, b color.Color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func relativeLuminance(c color.Color) float64 {
	r, g, b := rgb8(c)
	lin := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab := rgb8(a)
	br, bg, bb := rgb8(b)
	return ar == br && ag == bg && ab == bb
}

// rgb8 unpacks a color.Color to 8-bit channels. color.Color.RGBA reports
// 16-bit alpha-premultiplied values; every palette entry is an opaque hex
// triple, so the high byte is the original channel exactly.
func rgb8(c color.Color) (r, g, b float64) {
	cr, cg, cb, _ := c.RGBA()
	return float64(cr >> 8), float64(cg >> 8), float64(cb >> 8)
}

// --- the empty state ---

// TestRenderEmptyStateShowsConfigPathAndGuidance covers the acceptance
// criterion that the empty state names the resolved config path (read off the
// Model field, never resolved afresh by the render layer) plus the example and
// the alternative of launching from inside a repo.
func TestRenderEmptyStateShowsConfigPathAndGuidance(t *testing.T) {
	m := testModel()
	m.projects = nil
	m.activeProject = -1
	m.configPath = "/home/dev/.config/afk/config.toml"

	out := render(m)
	// Every line of the example is looked for, the `[dash]` header included:
	// search_roots shown without its table would be a line that does nothing
	// when pasted.
	wants := append([]string{"/home/dev/.config/afk/config.toml"}, emptyStateConfigExample...)
	wants = append(wants, "repo", "q: quit")
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("empty-state View missing %q in:\n%s", want, out)
		}
	}
}

// TestRenderEmptyStateFallsBackToGenericGuidanceWithoutAPath covers the case
// where no scan pass has landed yet (configPath is still its zero value): the
// block must still say something useful rather than naming a GUESSED path —
// the settings reader resolves it through XDG_CONFIG_HOME, which views.go
// cannot consult (its package doc forbids the I/O), so a hardcoded guess could
// name a file the running binary isn't actually reading.
func TestRenderEmptyStateFallsBackToGenericGuidanceWithoutAPath(t *testing.T) {
	m := testModel()
	m.projects = nil
	m.activeProject = -1

	out := render(m)
	if !strings.Contains(out, genericConfigIntro) {
		t.Errorf("empty-state View = %q, want generic guidance %q", out, genericConfigIntro)
	}
	if strings.Contains(out, ".config/afk") {
		t.Errorf("empty-state View = %q, must not guess a config path when none is known", out)
	}
}

// TestViewRendersNormallyForTheLegacySingleProjectModel guards the boundary
// against the empty state's own trigger: a Model with zero entries in
// m.projects (every pre-existing test in this package) has a REAL active
// project via m.activeProject/m.repo and must render the ordinary two-region
// body, not the empty state.
func TestViewRendersNormallyForTheLegacySingleProjectModel(t *testing.T) {
	out := render(testModel(sess("alpha")))
	if strings.Contains(out, "No projects found") {
		t.Fatalf("legacy single-project Model rendered the empty state:\n%s", out)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("legacy single-project Model did not render its session list:\n%s", out)
	}
}

// TestQuitsFromEmptyState pins the acceptance criterion that q still quits
// with zero known projects — handleKey's quit case sits above any
// project-related branching, so this only needs to prove that stays true.
func TestQuitsFromEmptyState(t *testing.T) {
	m := testModel()
	m.projects = nil
	m.activeProject = -1

	nm, _ := update(t, m, keyRune('q'))
	if !nm.quitting {
		t.Fatalf("q did not quit from the empty state")
	}
}

// TestEmptyStateViewDoesNotPanic is TestViewDoesNotPanic's pathological-size
// battery re-run against the empty state specifically: the pre-existing test
// never exercises this branch (every Model it builds has a real active
// project), so it proves nothing about renderEmptyState's own width/height
// floors on its own.
func TestEmptyStateViewDoesNotPanic(t *testing.T) {
	for _, dims := range []struct{ w, h int }{
		{0, 0}, {1, 1}, {1, 40}, {40, 1}, {5, 3}, {24, 8}, {120, 40},
	} {
		t.Run(fmt.Sprintf("w=%d/h=%d", dims.w, dims.h), func(t *testing.T) {
			m := testModel()
			m.projects = nil
			m.activeProject = -1
			m.width, m.height = dims.w, dims.h

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("View() panicked at width=%d height=%d: %v", dims.w, dims.h, r)
				}
			}()
			_ = render(m)
		})
	}
}

// TestOverlayKeysAreInertInTheEmptyState pins the fix for the empty-state
// keyboard trap: `p` used to open the switcher (and `n` the form) into a state
// View never renders — an invisible component capturing every key, with `q`
// silently no longer quitting. Both keys must now be inert with the empty state
// on screen, and `q` pressed right after must still quit.
//
// Load-bearing rows: both, one per key that used to open an invisible
// component — removing either guard turns exactly its row red.
func TestOverlayKeysAreInertInTheEmptyState(t *testing.T) {
	tests := []struct {
		name string
		key  rune
	}{
		{"p must not open an invisible switcher", 'p'},
		{"n must not open an invisible form", 'n'},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel()
			m.projects = nil
			m.activeProject = -1

			m, _ = update(t, m, keyRune(tt.key))
			if m.switcherOpen || m.formOpen {
				t.Fatalf("switcherOpen=%v formOpen=%v after %q — an overlay opened in the empty state", m.switcherOpen, m.formOpen, tt.key)
			}
			if out := render(m); !strings.Contains(out, "No projects found") {
				t.Fatalf("the empty state left the screen after %q:\n%s", tt.key, out)
			}

			m, _ = update(t, m, keyRune('q'))
			if !m.quitting {
				t.Fatalf("q did not quit after %q — the empty-state keyboard trap is back", tt.key)
			}
		})
	}
}

// TestEmptyStateKeepsAnOpenOverlayVisibleAndQuittable covers the one way an
// overlay can still coexist with the empty state: it was opened over a real
// project list and a scan then emptied that list underneath it (scanned-only
// projects leaving the search roots — nothing pinned survives). The overlay must
// stay ON SCREEN while it still owns the keyboard — as a modal it floats over
// the empty-state block, which stays visible around it — esc must recover, and
// q must quit from the recovered empty state.
func TestEmptyStateKeepsAnOpenOverlayVisibleAndQuittable(t *testing.T) {
	m := testModel()
	m.projects = scannedProjects("/p/a", "/p/b")
	m.activeProject = 0
	m, _ = update(t, m, keyRune('p'))
	if !m.switcherOpen {
		t.Fatal("precondition: the switcher must open over a real project list")
	}

	m, _ = update(t, m, projectScanMsg{})
	if m.hasActiveProject() {
		t.Fatal("precondition: the scan must have emptied the project list")
	}
	if !m.switcherOpen {
		t.Fatal("precondition: the overlay is still open under the emptied list")
	}
	out := render(m)
	if !strings.Contains(out, "Projects") {
		t.Fatalf("the open switcher is not on screen:\n%s", out)
	}

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.switcherOpen {
		t.Fatal("esc did not dismiss the overlay")
	}
	if out := render(m); !strings.Contains(out, "No projects found") {
		t.Fatalf("dismissing the overlay did not reveal the empty state:\n%s", out)
	}
	m, _ = update(t, m, keyRune('q'))
	if !m.quitting {
		t.Fatal("q did not quit from the empty state after the overlay closed")
	}
}
