package ui

import (
	"errors"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

// paneBlurHint keeps the shared pane-title geometry tests binding-derived
// without leaving an unused presentation alias in production code.
var paneBlurHint = focusBindingHint(defaultKeyMap().Focus)

// scratchSess builds a Session matching what internal/discover actually
// produces for a `.afk/{name}/host.json`-only directory (see
// buildScratchSessionState / discover.SessionState.Scratch's doc): every
// status-derived field at its zero value, Scratch true.
func scratchSess(name string) Session {
	return Session{SessionState: discover.SessionState{
		Name:         name,
		WorktreePath: "/wt/" + name,
		Scratch:      true,
	}}
}

// TestPhaseBadgeSixExistingPhasesUnchanged pins ui_test.go:4470-4485's own
// assertions from the other direction: adding the scratch entry to
// phaseBadges/phaseBadgesPlain must not perturb any of the six lifecycle
// phases' label, color, or padding. Every row here is load-bearing — each
// covers one existing phase this task must leave byte-identical.
//
// Color and padding are asserted on the STYLE VALUES, not the rendered
// string: the test binary's stdout is not a TTY, so lipgloss's default
// renderer strips color at Render time and a substring check on the output
// can never see a regressed hue. The fg/bg columns pin each phase to its
// tide token (§3.6: working phases work, done done, failed bad, text
// on-accent; tide is the theme a zero-Deps test draws with); the width
// check pins badge()'s Padding(0, 1) (one cell each side survives even the
// colorless profile); the plain variant pins badgePlain()'s bg-reused-as-fg
// hue with no background and no padding.
func TestPhaseBadgeSixExistingPhasesUnchanged(t *testing.T) {
	cases := []struct {
		phase  string
		want   string
		fg, bg string
	}{
		{"planning", "planning", palette.Tide.OnAccent, palette.Tide.Work},
		{"executing", "executing", palette.Tide.OnAccent, palette.Tide.Work},
		{"reviewing", "reviewing", palette.Tide.OnAccent, palette.Tide.Work},
		{"fixing", "fixing", palette.Tide.OnAccent, palette.Tide.Work},
		{"done", "done", palette.Tide.OnAccent, palette.Tide.Done},
		{"failed", "failed", palette.Tide.OnAccent, palette.Tide.Bad},
	}
	for _, c := range cases {
		t.Run(c.phase, func(t *testing.T) {
			got := theme.phaseBadge(c.phase)
			if !strings.Contains(got, c.want) {
				t.Errorf("phaseBadge(%q) = %q, want to contain %q", c.phase, got, c.want)
			}
			if strings.Contains(got, "unknown") {
				t.Errorf("phaseBadge(%q) unexpectedly rendered the unknown fallback: %q", c.phase, got)
			}
			plain := theme.phaseBadgePlain(c.phase)
			if !strings.Contains(plain, c.want) {
				t.Errorf("phaseBadgePlain(%q) = %q, want to contain %q", c.phase, plain, c.want)
			}

			if w := lipgloss.Width(got); w != len(c.want)+2 {
				t.Errorf("phaseBadge(%q) width = %d, want %d (label plus Padding(0, 1))", c.phase, w, len(c.want)+2)
			}
			st, ok := theme.phaseBadges[c.phase]
			if !ok {
				t.Fatalf("phaseBadges has no entry for %q", c.phase)
			}
			if fgot := st.GetForeground(); fgot != lipgloss.Color(c.fg) {
				t.Errorf("phaseBadges[%q] foreground = %v, want %q", c.phase, fgot, c.fg)
			}
			if bgot := st.GetBackground(); bgot != lipgloss.Color(c.bg) {
				t.Errorf("phaseBadges[%q] background = %v, want %q", c.phase, bgot, c.bg)
			}
			if !st.GetBold() {
				t.Errorf("phaseBadges[%q] lost Bold", c.phase)
			}

			if w := lipgloss.Width(plain); w != len(c.want) {
				t.Errorf("phaseBadgePlain(%q) width = %d, want the bare label width %d", c.phase, w, len(c.want))
			}
			pst, ok := theme.phaseBadgesPlain[c.phase]
			if !ok {
				t.Fatalf("phaseBadgesPlain has no entry for %q", c.phase)
			}
			if fgot := pst.GetForeground(); fgot != lipgloss.Color(c.bg) {
				t.Errorf("phaseBadgesPlain[%q] foreground = %v, want the padded badge's bg %q", c.phase, fgot, c.bg)
			}
			if bgot := pst.GetBackground(); bgot != nil {
				if _, isNoColor := bgot.(lipgloss.NoColor); !isNoColor {
					t.Errorf("phaseBadgesPlain[%q] gained a background: %v", c.phase, bgot)
				}
			}
		})
	}
}

// TestScratchSessionRendersScratchBadgeNeverUnknown is the core load-bearing
// assertion for this task: a scratch SessionState (Scratch: true, every
// status-derived field zero-valued per discover.SessionState.Scratch's doc)
// must render SCRATCH in the row's PHASE column and must never fall through
// to phaseText's UNKNOWN fallback (styles.go), the way an empty Phase would
// for a genuinely stale, non-scratch session.
func TestScratchSessionRendersScratchBadgeNeverUnknown(t *testing.T) {
	m := testModel()
	s := scratchSess("scratch-1")

	row := m.renderRow(s, false, 60)

	want := theme.phaseText(scratchPhaseKey, false)
	if !strings.Contains(row, want) {
		t.Fatalf("expected SCRATCH in the row's phase column; row=%q want=%q", row, want)
	}
	if strings.Contains(strings.ToUpper(row), "UNKN") {
		t.Fatalf("scratch row rendered the unknown fallback: %q", row)
	}
}

// TestScratchSessionPlainBadgeNarrowWidth covers rowColumns' abbreviated rung:
// a scratch session at a width where PHASE is five cells wide must still say
// SCR — never UNKN — and at a width with no PHASE column at all must say
// nothing about its phase rather than fall back to the unknown label.
func TestScratchSessionPlainBadgeNarrowWidth(t *testing.T) {
	m := testModel()
	s := scratchSess("a-fairly-long-scratch-session-name-for-this-narrow-test")

	narrow := m.renderRow(s, false, 34)
	if want := theme.phaseText(scratchPhaseKey, true); !strings.Contains(narrow, want) {
		t.Fatalf("expected abbreviated SCR at width 34; row=%q want=%q", narrow, want)
	}
	tiny := m.renderRow(s, false, 18)
	if strings.Contains(strings.ToUpper(xansi.Strip(tiny)), "UNKN") {
		t.Fatalf("scratch row rendered the unknown fallback with no phase column: %q", tiny)
	}
}

// TestScratchSessionRowShowsNoBranchLine pins the row's shape: one line, and
// since a scratch session's Branch is always "" (SessionState.Scratch's doc)
// nothing on it can be a "→" branch arrow — the branch lives in the detail
// pane for every kind of session now, so this holds for scratch trivially.
func TestScratchSessionRowShowsNoBranchLine(t *testing.T) {
	m := testModel()
	s := scratchSess("scratch-1")

	row := m.renderRow(s, false, 60)
	if strings.Contains(row, "\n") {
		t.Fatalf("expected a one-line row, got: %q", row)
	}
	if strings.Contains(row, "→") {
		t.Errorf("expected no branch arrow on a scratch row, got: %q", row)
	}
}

// TestScratchSessionDetailHeaderHasNoBranchSubLineOrLoopCount covers the
// detail header (renderDetail): a scratch session must show the scratch
// state (glyph and word), no "branch → base" sub-line (both fields are empty, per
// SessionState.Scratch's doc), and no "loop N" (LoopCount is always 0).
func TestScratchSessionDetailHeaderHasNoBranchSubLineOrLoopCount(t *testing.T) {
	m := testModel(scratchSess("scratch-1"))

	detail := m.renderDetailsOverlay(80, 30)

	if strings.Contains(detail, "→") {
		t.Errorf("expected no branch sub-line in scratch detail header, got:\n%s", detail)
	}
	if strings.Contains(detail, "loop ") {
		t.Errorf("expected no loop count in scratch detail header, got:\n%s", detail)
	}
	if strings.Contains(detail, "unknown") {
		t.Errorf("expected no unknown fallback in scratch detail header, got:\n%s", detail)
	}
	// The header draws the state as §3.6 does (T16.1): "○ scratch" in the
	// quiet colour, through the one mapping, stateLook.
	// It is read off detailBody, the text before the box: the box repaints
	// every cell with the panel ground (panelBox), which re-encodes the
	// styled run.
	wantBadge := theme.renderState(stateSession, scratchPhaseKey)
	if !strings.Contains(m.detailBody(detailContentWidth(80)), wantBadge) || !strings.Contains(xansi.Strip(detail), "○ scratch") {
		t.Errorf("expected scratch badge in detail header, got:\n%s", detail)
	}
}

// TestScratchSessionDetailHasNoWavesPanel covers wavesPanel directly: a
// scratch session was never planned by design (internal/session's scratch
// mode never writes a plan file) — it is not a session that failed to plan,
// so the panel must render nothing at all rather than "No plan file." or
// "No plan yet (planning).".
func TestScratchSessionDetailHasNoWavesPanel(t *testing.T) {
	ctx := panelContext{Session: scratchSess("scratch-1")}
	lines := wavesPanel{}.Render(ctx, 60)
	if lines != nil {
		t.Fatalf("expected wavesPanel to render nothing for a scratch session, got: %v", lines)
	}
}

// TestScratchSessionDetailPanelSectionAbsent is the end-to-end complement of
// TestScratchSessionDetailHasNoWavesPanel: the Overview must not print any
// plan/verdict empty-state text for a scratch session, and the Waves tab —
// which the strip still names, since the tab set is fixed per repo — must
// say the session has no plan by design rather than "No plan file.".
func TestScratchSessionDetailPanelSectionAbsent(t *testing.T) {
	m := testModel(scratchSess("scratch-1"))
	detail := m.renderDetailsOverlay(80, 30)

	for _, banned := range []string{"No plan file.", "No plan yet", "Review Verdict", "Progress"} {
		if strings.Contains(detail, banned) {
			t.Errorf("expected no %q in scratch session's Overview, got:\n%s", banned, detail)
		}
	}

	m, _ = update(t, focusOverview(t, m), keyRune('2'))
	waves := xansi.Strip(m.renderDetailsOverlay(80, 30))
	if !strings.Contains(waves, "A one-off session has no plan.") {
		t.Errorf("expected the Waves tab to say a one-off has no plan, got:\n%s", waves)
	}
	if strings.Contains(waves, "No plan file.") {
		t.Errorf("Waves tab fell through to the missing-plan message for a scratch session:\n%s", waves)
	}
}

// TestNormalSessionDetailHeaderUnaffected is a regression guard: a normal
// (non-scratch) session's detail header must keep showing its branch → base
// sub-line, phase badge, and loop count exactly as before this task.
func TestNormalSessionDetailHeaderUnaffected(t *testing.T) {
	s := sess("session-a")
	s.Phase = state.PhaseExecuting
	s.LoopCount = 2
	m := testModel(s)

	detail := m.renderDetailsOverlay(80, 30)

	if !strings.Contains(detail, s.Branch+" → "+s.BaseBranch) {
		t.Errorf("expected branch sub-line preserved for a normal session, got:\n%s", detail)
	}
	if !strings.Contains(detail, "loop 2") {
		t.Errorf("expected loop count preserved for a normal session, got:\n%s", detail)
	}
	// The phase is drawn as §3.6 draws a state since T16.1: "◐ executing" in
	// the working colour, the same word as the old badge.
	wantBadge := theme.renderState(stateSession, string(state.PhaseExecuting))
	if !strings.Contains(m.detailBody(detailContentWidth(80)), wantBadge) || !strings.Contains(xansi.Strip(detail), "◐ executing") {
		t.Errorf("expected normal phase badge preserved, got:\n%s", detail)
	}
}

// TestConfirmCleanBarScratchStatesTheTrueConsequence is the regression test
// for the rejected blocker on the clean confirmation: a scratch target must
// not present its empty phase as "unknown" (its own Must NOT do) and must not
// claim "Removes the worktree; branch and archive kept." — a scratch clean
// stops the host and removes .afk/{name}/ only, and false statements on a
// destructive-action confirmation are the defect. The width rows mirror
// TestConfirmCleanBarFitsAnEightyColumnTerminal so the truthful copy is also
// legible copy.
func TestConfirmCleanBarScratchStatesTheTrueConsequence(t *testing.T) {
	m := testModel(scratchSess("scratch-1"))
	m.width = 80
	m, _ = pressAction(t, m, keyRune('c'))

	if m.modal != inputConfirmClean {
		t.Fatal("`c` on a scratch session should still confirm before cleaning")
	}

	out := m.renderConfirmCleanBar()
	joined := strings.ReplaceAll(out, "\n", " ")

	if !strings.Contains(joined, `Clean one-off session "scratch-1"?`) {
		t.Fatalf("confirm bar should name the one-off target, got:\n%s", out)
	}
	for _, banned := range []string{"unknown", "worktree", "branch and archive"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("confirm bar for a scratch session must not say %q, got:\n%s", banned, out)
		}
	}
	if !strings.Contains(joined, "Stops the host and removes the session directory.") {
		t.Fatalf("confirm bar should state the scratch clean's true consequence, got:\n%s", out)
	}
	if !strings.Contains(joined, "y: clean") || !strings.Contains(joined, "n/esc: cancel") {
		t.Fatalf("confirm bar should keep the key hint, got:\n%s", out)
	}
	for _, ln := range strings.Split(out, "\n") {
		trimmed := strings.TrimRight(ln, " ")
		if w := lipgloss.Width(trimmed); w > 80 {
			t.Fatalf("scratch confirm bar line exceeds width 80 (got %d): %q", w, trimmed)
		}
	}
	if strings.Contains(joined, "…") {
		t.Fatalf("scratch confirm bar must not be truncated at width 80:\n%s", out)
	}
}

// TestConfirmCleanBarStatusWithoutWorktreeStatesTheTrueConsequence is the same
// defect class as the scratch row's, on the route that reaches the user's own
// checkout: this clean archives and removes `.afk/{name}/` and issues no git
// command at all, so the standard line's "Worktree goes" would promise to
// delete the worktree the user is sitting in.
//
// The banned strings are the load-bearing half. "Worktree goes" is the false
// consequence; `d: +delete branch` is a key the modal must not advertise
// because deleteBranchClean makes it inert for this kind (the entry has no
// branch of its own to delete) — and an advertised key that does nothing is
// how a destructive modal loses the user's trust in the keys that do work.
// The phase still shows, unlike a worktree fault's: this row has a real
// lifecycle.
func TestConfirmCleanBarStatusWithoutWorktreeStatesTheTrueConsequence(t *testing.T) {
	m := testModel(strandedSess("standalone"))
	m.width = 80
	m, _ = pressAction(t, m, keyRune('c'))

	if m.confirmKind != session.KindStatusWithoutWorktree {
		t.Fatalf("setup: confirmKind = %q, want the stranded kind", m.confirmKind)
	}

	out := m.renderConfirmCleanBar()
	joined := strings.ReplaceAll(out, "\n", " ")

	if !strings.Contains(joined, `Clean session "standalone" (phase: done)`) {
		t.Fatalf("confirm bar should name the target and its phase, got:\n%s", out)
	}
	if !strings.Contains(joined, "Session dir archived and removed; worktree kept.") {
		t.Fatalf("confirm bar should state this route's true consequence, got:\n%s", out)
	}
	for _, banned := range []string{"Worktree goes", "nothing archived", "+delete branch"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("confirm bar for a stranded session must not say %q, got:\n%s", banned, out)
		}
	}
	if !strings.Contains(joined, "y: clean") || !strings.Contains(joined, "n/esc: cancel") {
		t.Fatalf("confirm bar should keep the key hint, got:\n%s", out)
	}
	for _, ln := range strings.Split(out, "\n") {
		trimmed := strings.TrimRight(ln, " ")
		if w := lipgloss.Width(trimmed); w > 80 {
			t.Fatalf("stranded confirm bar line exceeds width 80 (got %d): %q", w, trimmed)
		}
	}
	if strings.Contains(joined, "…") {
		t.Fatalf("stranded confirm bar must not be truncated at width 80:\n%s", out)
	}
}

// TestConfirmCleanScratchStateClearsOnEveryExit pins that confirmScratch is
// cleared on all three modal exits (esc, n, y), the same discipline
// confirmTarget/confirmPhase get: a leaked true would relabel the NEXT
// session's clean confirmation as a one-off's, hiding the worktree warning
// on a session whose clean does remove a worktree.
func TestConfirmCleanScratchStateClearsOnEveryExit(t *testing.T) {
	for _, exit := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"esc", tea.KeyPressMsg{Code: tea.KeyEsc}},
		{"n", keyRune('n')},
		{"y", keyRune('y')},
	} {
		t.Run(exit.name, func(t *testing.T) {
			m := testModel(scratchSess("scratch-1"))
			m, _ = pressAction(t, m, keyRune('c'))
			if !m.confirmScratch {
				t.Fatal("entering the modal on a scratch session should set confirmScratch")
			}
			m, _ = update(t, m, exit.key)
			if m.confirmScratch {
				t.Fatalf("confirmScratch leaked past the %s exit", exit.name)
			}
		})
	}
}

// TestHeaderAdvertisesOneOffAndLabelKeys is W6.T2's rewrite: the header no
// longer hand-carries `o`/`l` hints on a multi-project rung (or any rung) —
// that discoverability job moved to full help (`?`, normal mode), which
// advertises keys.Scratch/keys.Label unconditionally (TestFullHelpOmitsNoEnabledBinding
// covers the general claim). This asserts the header side of that move:
// neither hint is hand-written here anymore, at any project count.
func TestHeaderAdvertisesOneOffAndLabelKeys(t *testing.T) {
	m := testModel(scratchSess("scratch-1"))
	m.projects = []project.Project{
		{Path: "/a", Name: "alpha"},
		{Path: "/b", Name: "beta"},
	}
	m.activeProject = 0
	m.width = 200

	got := m.renderHeader()
	oneOff := "o one-off"
	label := "l label"
	for _, banned := range []string{oneOff, label} {
		if strings.Contains(got, banned) {
			t.Errorf("renderHeader() = %q, must not hand-carry %q", got, banned)
		}
	}
}

// TestMainRegionBoxesFillTheMainRegion pins the sizedBox arithmetic on every
// detailBox surface at once: sizedBox's argument is the content-PLUS-padding
// size, so every renderer here passes contentW+2 (contentW = mainOuter-4,
// theme.detailBox is Padding(0, 1)) and the rendered block comes out exactly
// mainOuter wide — the width renderPane already fills.
// Width() is exact in lipgloss, so the pre-fix deficit reproduces as a block
// measuring mainOuter-2, not as a silent no-op; this is the test that turns
// red if any surface's +2 is "simplified" away, including renderDetail's
// no-selection, pending and fault branches, which no wrap-level test reaches.
func TestMainRegionBoxesFillTheMainRegion(t *testing.T) {
	pending := sess("pend")
	pending.Pending = true

	for _, width := range []int{80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			cases := []struct {
				label  string
				render func(t *testing.T, mainOuter, bodyHeight int) string
			}{
				{"detail", func(t *testing.T, w, h int) string {
					m := testModel(sess("alpha"))
					m.width = width
					return m.renderDetailsOverlay(w, h)
				}},
				{"detail no selection", func(t *testing.T, w, h int) string {
					m := testModel()
					m.width = width
					return m.renderDetailsOverlay(w, h)
				}},
				{"detail pending", func(t *testing.T, w, h int) string {
					m := testModel(pending)
					m.width = width
					return m.renderDetailsOverlay(w, h)
				}},
				{"detail fault", func(t *testing.T, w, h int) string {
					s := faultSess("busted", session.KindWorktreeMissing, "worktree directory no longer exists")
					m := testModel(s)
					m.width = width
					return m.renderDetailsOverlay(w, h)
				}},
			}

			for _, c := range cases {
				t.Run(c.label, func(t *testing.T) {
					m := testModel()
					m.width = width
					_, mainOuter, bodyHeight := m.layoutDims()
					if got := lipgloss.Width(c.render(t, mainOuter, bodyHeight)); got != mainOuter {
						t.Errorf("%s box is %d cells wide, want mainOuter %d", c.label, got, mainOuter)
					}
				})
			}
		})
	}
}

// --- worktree-side fault rows (BROKEN / ORPHAN) ------------------------------

// faultSess builds the Session internal/discover produces for a worktree-side
// fault (see discover.worktreeFaultState): named after the branch, carrying the
// registered worktree path and a plain-language Reason, with every
// status-derived field at its zero value and no host.
func faultSess(name string, kind session.Kind, reason string) Session {
	return Session{SessionState: discover.SessionState{
		Name:         name,
		WorktreePath: "/wt/" + name,
		Branch:       name,
		Kind:         kind,
		Reason:       reason,
	}}
}

// TestFaultRowRendersBadgeAndReason is this task's core rendering assertion: a
// worktree-side fault row shows its own badge and its reason, and shows
// neither of the two things a row with a lifecycle shows.
//
// The negative assertions are the load-bearing half. A fault row has no phase,
// so falling through to renderRow's normal path would render the "unknown"
// phase badge — the label a session with a BROKEN status.json wears, which is
// a different condition with a different fix. And it has no plan, so a
// progress bar would read as a real session sitting at 0/0 rather than as an
// entry with nothing to make progress on (renderPendingRow's own doc makes the
// same argument for the same reason).
func TestFaultRowRendersBadgeAndReason(t *testing.T) {
	tests := []struct {
		name      string
		kind      session.Kind
		reason    string
		wantBadge string
	}{
		{
			name:      "missing worktree directory renders BROKEN",
			kind:      session.KindWorktreeMissing,
			reason:    "worktree directory no longer exists",
			wantBadge: "BROKEN",
		},
		{
			name:      "worktree with no session renders ORPHAN",
			kind:      session.KindWorktreeNoSession,
			reason:    "worktree has no afk session",
			wantBadge: "ORPHAN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel()
			s := faultSess("busted", tt.kind, tt.reason)

			row := m.renderRow(s, false, 60)
			if strings.Contains(row, "\n") {
				t.Fatalf("expected a one-line row, got: %q", row)
			}
			if !strings.Contains(row, tt.wantBadge) {
				t.Errorf("row missing the %s label: %q", tt.wantBadge, row)
			}
			if !strings.Contains(row, "busted") {
				t.Errorf("row missing the session name: %q", row)
			}
			if strings.Contains(strings.ToUpper(row), "UNKN") {
				t.Errorf("fault row rendered the unknown phase label: %q", row)
			}
			if strings.ContainsAny(row, "█░") {
				t.Errorf("fault row rendered a progress bar: %q", row)
			}
			// The reason moved off the row and into the detail pane, which is
			// the surface with room for it; a fault row that lost it there too
			// would say nothing about what is wrong.
			m.setSessions([]Session{s})
			if detail := xansi.Strip(m.composeDetailBody(80)); !strings.Contains(detail, tt.reason) {
				t.Errorf("detail pane = %q, want the reason %q", detail, tt.reason)
			}
		})
	}
}

// TestFaultRowNarrowWidthKeepsTheBadge covers rowColumns' abbreviated rung
// for a fault: at the 26-cell content width an 80-column terminal gives the
// sidebar, the PHASE column is five cells and the row must still say BRKN,
// because the label is the only thing on the row that says why it is there.
func TestFaultRowNarrowWidthKeepsTheBadge(t *testing.T) {
	m := testModel()
	s := faultSess("a-fairly-long-broken-session-name", session.KindWorktreeMissing, "worktree directory no longer exists")

	row := m.renderRow(s, false, 26)
	if !strings.Contains(row, "BRKN") {
		t.Fatalf("narrow fault row lost its label: %q", row)
	}
}

// TestFaultRowFitsEightyColumns is the width budget. 80 columns is the
// narrowest terminal anyone runs and the row's reason text is the part that
// would overflow; a line wider than the sidebar wraps onto a third line and
// breaks the list's two-lines-per-row height math.
func TestFaultRowFitsEightyColumns(t *testing.T) {
	m := testModel()
	for _, s := range []Session{
		faultSess("busted", session.KindWorktreeMissing, "worktree directory no longer exists"),
		faultSess("stranger", session.KindWorktreeNoSession, "worktree has no afk session"),
	} {
		for _, selected := range []bool{false, true} {
			row := m.renderRow(s, selected, 80)
			// JoinVertical pads narrow lines with trailing spaces, so trim
			// before any width assertion.
			for _, ln := range strings.Split(row, "\n") {
				if w := lipgloss.Width(strings.TrimRight(ln, " ")); w > 80 {
					t.Errorf("%s (selected=%v) line width %d exceeds 80: %q", s.Name, selected, w, ln)
				}
			}
		}
	}
}

// TestFaultRowDetailPaneStatesTheFaultNotEmptyPanels covers the detail pane on
// the same argument renderDetail already makes for a placeholder row: a fault
// entry has no plan, no notepads and no verdict, so running the panels over it
// renders a wall of "No plan file."-style misses for an entry whose actual
// problem is stated nowhere.
func TestFaultRowDetailPaneStatesTheFaultNotEmptyPanels(t *testing.T) {
	s := faultSess("busted", session.KindWorktreeMissing, "worktree directory no longer exists")
	m := testModel(s)

	detail := m.renderDetailsOverlay(80, 30)

	if !strings.Contains(detail, "busted") {
		t.Errorf("detail pane missing the name:\n%s", detail)
	}
	if !strings.Contains(detail, s.Reason) {
		t.Errorf("detail pane missing the reason %q:\n%s", s.Reason, detail)
	}
	if !strings.Contains(detail, s.WorktreePath) {
		t.Errorf("detail pane missing the worktree path %q:\n%s", s.WorktreePath, detail)
	}
	for _, banned := range []string{"No plan file.", "No plan yet", "Review Verdict", "unknown"} {
		if strings.Contains(detail, banned) {
			t.Errorf("detail pane rendered %q for a fault entry:\n%s", banned, detail)
		}
	}
}

// bigPlanSession builds a Session whose plan is far taller than any detail
// box a dashboard would realistically size: 20 waves of 5 tasks each (100
// tasks total), so wavesPanel alone emits well over a hundred lines before
// renderDetail's other panels (header, learnings, issues, verdict) even run.
func bigPlanSession(name string) Session {
	waves := make([]state.Wave, 0, 20)
	for w := 1; w <= 20; w++ {
		tasks := make([]state.Task, 0, 5)
		for tsk := 1; tsk <= 5; tsk++ {
			tasks = append(tasks, state.Task{
				ID:    fmt.Sprintf("W%d.T%d", w, tsk),
				Wave:  w,
				Num:   tsk,
				Title: "some task title here",
				State: state.CheckIncomplete,
			})
		}
		waves = append(waves, state.Wave{Number: w, Name: "wave", Tasks: tasks})
	}
	s := sess(name)
	s.Plan = &state.Plan{Title: "Big Plan", Waves: waves}
	return s
}

// TestDetailPaneClipsToItsHeightBudget is renderDetail's analogue of
// TestSidebarListClipsToItsHeightBudget (sessionlist_test.go:108-128): the
// left pane's hand-rolled loop overflowed by printing too many lines. The
// right pane's defect is worse — the line COUNT already comes out exactly
// right (sizedBox's trailing MaxHeight blind-slices the content to outerH
// lines), so a test that only measures height is green against the bug. What
// MaxHeight slices is an ALREADY-BORDERED block (lipgloss applies MaxHeight
// after applyBorder, and Height() only pads short content — it never trims
// tall content), so the slice lands mid-panel and the closing bottom border
// row is never emitted. Row (b) below — the last line must BE that closing
// border, not chopped panel text — is the load-bearing assertion; row (a)
// passes today and is kept only for parity with the sidebar test and to
// catch a regression in the other direction (real underflow).
func TestDetailPaneClipsToItsHeightBudget(t *testing.T) {
	s := bigPlanSession("alpha")

	for _, tc := range []struct {
		width, height int
	}{
		{100, 24}, // matches the sidebar test's terminal and the plan's own repro note
		{80, 10},  // short terminal: the box budget is far smaller than the plan
		{120, 40}, // generous terminal: plan still dwarfs the budget (100 tasks)
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			m := testModel(s)
			m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
			m.width, m.height = tc.width, tc.height
			m.ready = true
			m.sizeSessionList()

			_, mainOuter, bodyHeight := m.layoutDims()
			detail := m.renderDetailsOverlay(mainOuter, bodyHeight)

			if got := lipgloss.Height(detail); got != bodyHeight {
				t.Errorf("detail height = %d, want exactly the %d-row budget", got, bodyHeight)
			}

			lines := strings.Split(detail, "\n")
			last := lines[len(lines)-1]
			if !strings.Contains(last, "╰") || !strings.Contains(last, "╯") {
				t.Errorf("last rendered line is not a closing border row, got: %q\nfull render:\n%s", last, detail)
			}

			if frame := lipgloss.Height(render(m)); frame > m.height {
				t.Errorf("frame height = %d, want at most %d", frame, m.height)
			}
		})
	}
}

// TestDetailContentWidth pins the one place the detail pane's text width is
// computed. Both sides of the scroll — renderDetail's frame and
// syncDetailViewport's Model viewport — read it, so the rows are the same
// number of cells; the review of this plan's first cut found the arithmetic
// duplicated in two functions with no test on either. The floor row is the
// load-bearing one: below it a narrow terminal would hand the panels a
// negative width.
func TestDetailContentWidth(t *testing.T) {
	for _, tc := range []struct {
		name         string
		outerW, want int
	}{
		{"typical 100-col terminal's main region", 65, 61},
		{"80-col terminal's main region", 52, 48},
		{"exactly at the floor", 14, 10},
		{"below the floor clamps to 10", 8, 10},
		{"zero width clamps to 10", 0, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := detailContentWidth(tc.outerW); got != tc.want {
				t.Fatalf("detailContentWidth(%d) = %d, want %d", tc.outerW, got, tc.want)
			}
		})
	}
}

// TestDetailRowBudget pins the row budget both the frame and the Model
// viewport size from. The overflow rows are load-bearing: the readout row
// comes out of the visible row count, so a Model viewport sized to innerH (or
// to outerH, as the first cut of this plan did) has a scroll ceiling one (or
// three) rows too low and the last content line is unreachable — the blocker
// the review caught. The exact-fit row pins that an equal line count is NOT
// an overflow (no readout, no lost row), and the one-row rows pin that a box
// with a single inner row never gives it up to a readout with nothing to
// read out.
func TestDetailRowBudget(t *testing.T) {
	for _, tc := range []struct {
		name               string
		outerH, totalLines int
		wantRows           int
		wantReadout        bool
	}{
		// The inner rows are outerH less the border and the title strip
		// (paneStripRows): 21 → 18.
		{"fits with room to spare", 21, 5, 18, false},
		{"exact fit is not an overflow", 21, 18, 18, false},
		{"overflow by one reserves the readout row", 21, 19, 17, true},
		{"deep overflow reserves the readout row", 21, 127, 17, true},
		{"80x10's 7-row body overflowing", 7, 127, 3, true},
		{"one inner row, fits", 4, 1, 1, false},
		{"one inner row, overflowing: no readout, the row stays content", 4, 40, 1, false},
		{"border and strip only floors at one row", 3, 0, 1, false},
		{"degenerate zero height floors at one row", 0, 40, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, readout := detailRowBudget(tc.outerH, tc.totalLines)
			if rows != tc.wantRows || readout != tc.wantReadout {
				t.Fatalf("detailRowBudget(%d, %d) = (%d, %v), want (%d, %v)",
					tc.outerH, tc.totalLines, rows, readout, tc.wantRows, tc.wantReadout)
			}
		})
	}
}

// TestScrollReadout pins the readout row's shape: right-aligned in exactly
// width cells, rounded to a whole percent, and routed through truncate like
// every other line in the file so a width narrower than the label cannot
// widen the row. The rounding rows are load-bearing because ScrollPercent is
// a float and the readout the user reads is the integer.
func TestScrollReadout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		percent float64
		width   int
		want    string
	}{
		{"top", 0, 10, "        0%"},
		{"bottom", 1, 10, "      100%"},
		{"rounds half up", 0.425, 10, "       43%"},
		{"rounds down", 0.4249, 10, "       42%"},
		{"width narrower than the label truncates rather than overflows", 0.5, 2, "5…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := xansi.Strip(scrollReadout(tc.percent, tc.width))
			if got != tc.want {
				t.Fatalf("scrollReadout(%v, %d) = %q, want %q", tc.percent, tc.width, got, tc.want)
			}
			if w := lipgloss.Width(got); w > tc.width {
				t.Fatalf("readout is %d cells wide, want at most %d", w, tc.width)
			}
		})
	}
}

// TestDetailReadoutAppearsOnlyOnOverflow pins Objective 3 at the boundary:
// the readout takes the box's last inner row when, and only when, the body
// has more lines than that inner height. The box is sized off the body's OWN
// line count so the exact-fit and overflow-by-one rows straddle the boundary
// by a single row — the place an off-by-one in detailsBodyBudget or
// renderDetailsOverlay would show. The overflow-by-one row also asserts the body's
// last line is reachable, since reserving the readout row shrinks the visible
// page and the Model viewport must shrink with it — and it resizes by sending
// a real tea.WindowSizeMsg through Update rather than poking m.height and
// calling syncDetailViewport by hand, so it exercises the same path the app
// takes and cannot pass on a sizing seam the app never uses.
func TestDetailReadoutAppearsOnlyOnOverflow(t *testing.T) {
	m := testModel(bigPlanSession("alpha"))
	m.detailTab = tabWaves // the Overview shows one wave; only the Waves tab overflows
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	mainOuter, _ := m.detailsSize()
	contentW := detailContentWidth(mainOuter)
	body := strings.Split(xansi.Strip(m.detailBody(contentW)), "\n")
	total := len(body)
	last := strings.TrimSpace(body[total-1])

	lastInnerRow := func(frame string) string {
		lines := strings.Split(xansi.Strip(frame), "\n")
		return lines[len(lines)-2]
	}

	t.Run("exact fit: no readout, last row is content", func(t *testing.T) {
		// The box's inner rows are the title strip (paneStripRows), the tab
		// row (detailsTabRows) and the body under them.
		outerH := total + boxBorderCells + paneStripRows + detailsTabRows
		frame := m.renderDetailsOverlay(mainOuter, outerH)
		if lipgloss.Height(frame) != outerH {
			t.Fatalf("frame is %d rows, want %d", lipgloss.Height(frame), outerH)
		}
		row := lastInnerRow(frame)
		if strings.Contains(row, "%") {
			t.Fatalf("exact-fit frame shows a readout on its last row: %q", row)
		}
		if !strings.Contains(row, last) {
			t.Fatalf("exact-fit frame's last row is %q, want the body's last line %q", row, last)
		}
	})

	t.Run("overflow by one: readout, and the last line still reachable", func(t *testing.T) {
		outerH := total + boxBorderCells + paneStripRows + detailsTabRows - 1
		if row := lastInnerRow(m.renderDetailsOverlay(mainOuter, outerH)); !strings.Contains(row, "0%") {
			t.Fatalf("overflowing frame's last row is %q, want a 0%% readout at the top", row)
		}
		m := focusOverview(t, m)
		m, _ = update(t, m, tea.WindowSizeMsg{
			Width:  100,
			Height: outerH + lipgloss.Height(m.renderHeader()) + lipgloss.Height(m.renderFooter()),
		})
		if _, _, got := m.layoutDims(); got != outerH {
			t.Fatalf("fixture broken: bodyHeight = %d, want %d", got, outerH)
		}
		for i := 0; i < 10; i++ {
			m, _ = update(t, m, keyDown)
		}
		frame := m.renderDetailsOverlay(mainOuter, outerH)
		if !strings.Contains(xansi.Strip(frame), last) {
			t.Fatalf("body's last line %q is unreachable when the readout row is reserved:\n%s", last, xansi.Strip(frame))
		}
		if row := lastInnerRow(frame); !strings.Contains(row, "100%") {
			t.Fatalf("readout at the bottom is %q, want 100%%", row)
		}
	})
}

// TestDetailShortMessagesSoftWrapAtNarrowWidths pins that the no-selection,
// pending and worktree-fault messages fold onto the next row on a narrow
// terminal rather than being cut. On main their sizedBox's Width() wrapped
// them; routing them through a SoftWrap-off viewport ansi.Cut them instead,
// and at 80 columns (contentW 48) that dropped "background", "the session
// exists." and the fault line's period — an unrequested content change the
// review caught with no test on it. Each row's want is the tail of the line
// that the cut used to lose, matched against the frame's text with its box
// borders removed and whitespace collapsed (a wrap may fold a phrase across
// two rows), so the row fails against the cut and passes against either wrap
// or a wider terminal; the height assertion pins that wrapping did not buy
// the text back at the cost of the box's budget.
func TestDetailShortMessagesSoftWrapAtNarrowWidths(t *testing.T) {
	pending := sess("pend")
	pending.Pending = true

	for _, tc := range []struct {
		name  string
		model func() Model
		wants []string
	}{
		{"pending", func() Model { return testModel(pending) },
			[]string{"background", "the session exists."}},
		{"worktree fault", func() Model {
			return testModel(faultSess("busted", session.KindWorktreeMissing, "worktree directory no longer exists"))
		}, []string{"clean it up."}},
		{"no selection", func() Model { return testModel() },
			[]string{"Select a session to see details."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model()
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
			_, mainOuter, bodyHeight := m.layoutDims()
			if got := detailContentWidth(mainOuter); got != 46 {
				t.Fatalf("fixture geometry drifted: contentW = %d at 80 cols, want 46", got)
			}

			frame := m.renderDetailsOverlay(mainOuter, bodyHeight)
			if got := lipgloss.Height(frame); got != bodyHeight {
				t.Errorf("frame is %d rows, want the %d-row budget", got, bodyHeight)
			}
			plain := xansi.Strip(frame)
			flat := strings.Join(strings.Fields(strings.ReplaceAll(plain, "│", " ")), " ")
			for _, want := range tc.wants {
				if !strings.Contains(flat, want) {
					t.Errorf("%q was cut from the %d-column detail pane:\n%s", want, mainOuter, plain)
				}
			}
		})
	}
}

// --- the footer is the help row ---

// TestRenderFooterIsTheHelpRowAlone is the surface contract this wave exists
// for: notifications used to paint a status row here AND the toast, and the
// row is gone.
//
// The assertions are concrete row counts rather than an equality against
// renderHelp(), which a footer that literally returns renderHelp() would
// satisfy no matter what else it did. Every row is load-bearing: `quiet` and
// `help expanded` pin the two legitimate heights — one row normally, more
// under `?`, which is why the height must NOT be hard-coded to 1 anywhere;
// the remaining rows pin that the three inputs that used to ADD a row (a
// status, a discovery error, in-flight work) no longer change the height or
// leak their text into it.
func TestRenderFooterIsTheHelpRowAlone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(*Model)
		wantRows int
		absent   []string
	}{
		{name: "quiet", setup: func(*Model) {}, wantRows: 1},
		{
			// `?` opens the keys overlay rather than growing the footer into
			// a full help (W3.T1 of T16.2), so the footer stays one row.
			name:     "keys overlay open",
			setup:    func(m *Model) { m.keysOpen = true },
			wantRows: 1,
		},
		{
			name:     "a status is set",
			setup:    func(m *Model) { m.notify(notifySuccess, "cleaned a — archived to /archive/a") },
			wantRows: 1,
			absent:   []string{"cleaned a", "/archive/a"},
		},
		{
			name:     "discovery is failing",
			setup:    func(m *Model) { m.loadErr = "git worktree list: exit status 128" },
			wantRows: 1,
			absent:   []string{"discovery error", "exit status 128"},
		},
		{
			name:     "work is in flight",
			setup:    func(m *Model) { m.pendingSpawns = []string{"a"} },
			wantRows: 1,
		},
		{
			name: "work is in flight with a status",
			setup: func(m *Model) {
				m.pendingSpawns = []string{"a"}
				m.notify(notifyInfo, "creating a…")
			},
			wantRows: 1,
			absent:   []string{"creating a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("a"))
			tc.setup(&m)

			footer := xansi.Strip(m.renderFooter())
			rows := lipgloss.Height(footer)
			switch {
			case tc.wantRows == -1 && rows <= 1:
				t.Fatalf("footer is %d rows with helpShowAll on, want more than 1: %q", rows, footer)
			case tc.wantRows > 0 && rows != tc.wantRows:
				t.Fatalf("footer is %d rows, want %d: %q", rows, tc.wantRows, footer)
			}
			for _, s := range tc.absent {
				if strings.Contains(footer, s) {
					t.Errorf("footer carries %q, which belongs to the toast now: %q", s, footer)
				}
			}
			for _, ln := range strings.Split(footer, "\n") {
				if w := lipgloss.Width(strings.TrimRight(ln, " ")); w > m.width {
					t.Errorf("footer line is %d cells, wider than m.width %d: %q", w, m.width, ln)
				}
			}
		})
	}
}

// TestHeaderReportsStalenessWhileDiscoveryFails pins the honesty fix that the
// removal of the sticky footer error made necessary: applyLoaded keeps the
// previous sessions on a discovery failure and stamps lastLoad before the
// error check, so the list on screen freezes with nothing anywhere saying so.
// The failure toasts once, on the transition, and then the dashboard goes
// quiet while staying wrong — this piece is the standing signal that replaces
// both the toast and the refreshed-ago the redesign deleted.
//
// The "refreshed" guards below are the other half of that: the cadence string
// was removed on purpose (it reported the tick interval as news, and it read
// as a freshness claim exactly when the list was frozen), so the healthy
// header must not carry it either. This test would otherwise pass against a
// header that answered a discovery failure by bringing it back.
//
// The clock is advanced between the failing load and the render, because a
// constant Now would report a zero-second staleness and could not tell a real
// elapsed count from a hard-coded one.
func TestHeaderReportsStalenessWhileDiscoveryFails(t *testing.T) {
	m := testModel(sess("a"))
	at := m.deps.Now()

	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("a")}, at: at})
	if got := xansi.Strip(m.renderHeader()); strings.Contains(got, "stale") || strings.Contains(got, "refreshed") {
		t.Fatalf("header = %q, want no freshness claim of any kind after a good load", got)
	}

	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, err: errors.New("git blew up"), at: at.Add(2 * time.Second)})
	m.deps.Now = func() time.Time { return at.Add(32 * time.Second) }
	got := xansi.Strip(m.renderHeader())
	if !strings.Contains(got, "stale") || !strings.Contains(got, "discovery failing") {
		t.Fatalf("header = %q, want it to report staleness while discovery is failing", got)
	}
	if !strings.Contains(got, "30s") {
		t.Fatalf("header = %q, want the 30s since the first failing pass", got)
	}
	if strings.Contains(got, "refreshed") {
		t.Fatalf("header = %q, must not claim a refresh while the list is frozen", got)
	}
	if rows := lipgloss.Height(m.renderHeader()); rows != 1 {
		t.Fatalf("header is %d rows while discovery is failing, want exactly 1: %q", rows, got)
	}

	m, _ = update(t, m, sessionsLoadedMsg{gen: m.projectGen, sessions: []Session{sess("a")}, at: at.Add(34 * time.Second)})
	if got := xansi.Strip(m.renderHeader()); strings.Contains(got, "stale") {
		t.Fatalf("header = %q, want the staleness signal gone once discovery recovers", got)
	}
}

// --- W3.T1: the focused detail pane's visible state -------------------------
//
// Color is not observable on rendered strings in this suite (see
// TestPhaseBadgeSixExistingPhasesUnchanged's doc: the test binary's stdout is
// not a TTY, so lipgloss's default renderer strips color at Render time), so
// every assertion here that cares about WHICH color a box wears reads the
// lipgloss.Style values directly — theme.detailBox/theme.detailBoxFocused and
// m.detailBoxStyle()/m.listBoxStyle() — the same idiom TestFocusBorderTracksInputOwner
// (ui_test.go) already uses for the pane/sidebar pair.

// TestDetailBoxStyleAccentOnAllFourEdgesWhenFocused is plan step 2's "all four
// edges" guard: labelBorders (styles.go) repaints only the top and bottom
// edges of a rendered box, using the SAME style's border colors it is handed.
// A style parameter that reached labelBorders but not sizedBox (or vice
// versa) would leave two edges accented and two gray — this pins that
// theme.detailBoxFocused itself carries the accent on all four sides, so
// wiring the single style value through both call sites (boxDetailBody's
// sizedBox call and renderDetail's labelBorders call) is sufficient by
// construction.
func TestDetailBoxStyleAccentOnAllFourEdgesWhenFocused(t *testing.T) {
	accent := theme.paneBoxFocused.GetBorderTopForeground()
	blurredAccent := theme.detailBox.GetBorderTopForeground()
	if accent == blurredAccent {
		t.Fatalf("precondition: the accent and the blurred border color must differ")
	}

	edges := []struct {
		name             string
		focused, blurred color.Color
	}{
		{"top", theme.detailBoxFocused.GetBorderTopForeground(), theme.detailBox.GetBorderTopForeground()},
		{"bottom", theme.detailBoxFocused.GetBorderBottomForeground(), theme.detailBox.GetBorderBottomForeground()},
		{"left", theme.detailBoxFocused.GetBorderLeftForeground(), theme.detailBox.GetBorderLeftForeground()},
		{"right", theme.detailBoxFocused.GetBorderRightForeground(), theme.detailBox.GetBorderRightForeground()},
	}
	for _, e := range edges {
		if e.focused != accent {
			t.Errorf("%s edge: focused color = %v, want the accent %v", e.name, e.focused, accent)
		}
		if e.blurred == accent {
			t.Errorf("%s edge: blurred style unexpectedly carries the accent", e.name)
		}
	}
}

// TestDetailBoxFocusedBlurredStylesAreGeometryNeutral pins the Must-NOT-do:
// the focused and blurred detail box styles differ ONLY in border color. Same
// border glyph set (so the same width), same padding — anything else would
// move detailContentWidth/detailRowBudget out from under syncDetailViewport,
// the exact regression the plan's Context section warns is a documented
// off-by-N history.
func TestDetailBoxFocusedBlurredStylesAreGeometryNeutral(t *testing.T) {
	if theme.detailBoxFocused.GetBorderStyle() != theme.detailBox.GetBorderStyle() {
		t.Error("focused/blurred detail box border glyphs differ")
	}
	ft, fr, fb, fl := theme.detailBoxFocused.GetPadding()
	bt, br, bb, bl := theme.detailBox.GetPadding()
	if ft != bt || fr != br || fb != bb || fl != bl {
		t.Errorf("focused/blurred detail box padding differs: focused=(%d,%d,%d,%d) blurred=(%d,%d,%d,%d)",
			ft, fr, fb, fl, bt, br, bb, bl)
	}
	_, ftop, fright, fbottom, fleft := theme.detailBoxFocused.GetBorder()
	_, btop, bright, bbottom, bleft := theme.detailBox.GetBorder()
	if ftop != btop || fright != bright || fbottom != bbottom || fleft != bleft {
		t.Error("focused/blurred detail box border sides (which edges are drawn) differ")
	}
}

// TestDetailsOverlayDrawsTheAccentPane is the render-level check of the
// overlay's box: the pane part with the accent border on its left edge (only
// the pane's own border draws it), the accent strip, and the strip's name
// first. The overlay always has the keyboard while it is drawn, so there is
// no blurred form to compare.
func TestDetailsOverlayDrawsTheAccentPane(t *testing.T) {
	m := testModel(sess("alpha"))
	w, h := m.detailsSize()
	got := m.renderDetailsOverlay(w, h)
	if c := cellOf(t, got, 0, 2); hexOf(c.Style.Fg) != palette.Tide.Accent {
		t.Errorf("left edge fg = %s, want %s", hexOf(c.Style.Fg), palette.Tide.Accent)
	}
	if c := cellOf(t, got, 1, 1); hexOf(c.Style.Bg) != palette.Tide.Accent {
		t.Errorf("title strip bg = %s, want %s", hexOf(c.Style.Bg), palette.Tide.Accent)
	}
	if line := strings.Split(xansi.Strip(got), "\n")[1]; !strings.HasPrefix(line, "│ Details · alpha") {
		t.Errorf("strip row = %q, want the title first", line)
	}
}

// TestCombinedFocusPresentationUsesOneBinding is the presentation-side
// complement to the routing matrix: every normal-mode surface that names the
// focus action reads the single combined binding rather than retaining an
// Enter-only edge hint. (The pane's title no longer names it: since W3.T1 of
// T16.2 the pane is drawn only in the Agent, where enter is the agent's.)
func TestCombinedFocusPresentationUsesOneBinding(t *testing.T) {
	m := testModel(runningSess("alpha"))
	focus := m.keys.Focus.Help()
	want := focus.Key + " " + focus.Desc

	short := xansi.Strip(m.renderHelp())
	if got := strings.Count(short, want); got != 1 {
		t.Fatalf("short help has %d copies of %q, want one: %q", got, want, short)
	}
	full := xansi.Strip(renderModeHelp(m.renderNormalHelpKeyMap(), true, 100000))
	if got := strings.Count(full, focus.Key); got != 1 || strings.Count(full, focus.Desc) != 1 {
		t.Fatalf("full help has %d copies of %q, want one: %q", got, want, full)
	}
}

func TestPaneFocusNotificationsUseCombinedBinding(t *testing.T) {
	newModel := func() Model {
		m := testModel(runningSess("alpha"))
		m.keys.Focus = key.NewBinding(
			key.WithKeys("enter", "x"),
			key.WithHelp("enter/x", "focus current"),
		)
		return m
	}

	t.Run("open", func(t *testing.T) {
		m, _ := update(t, newModel(), paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
		if want := "opened alpha · enter/x to type in it, ctrl+] esc to return"; m.status != want {
			t.Fatalf("open status = %q, want %q", m.status, want)
		}
	})
	t.Run("release", func(t *testing.T) {
		m := newModel()
		m.focus = focusRight // connect moves the focus at request time
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha", focusOnArrival: true})
		m = pressKey(t, m, keyEscMsg())
		if want := "input goes to the dashboard — alpha still running · enter/x to open it again"; m.status != want {
			t.Fatalf("release status = %q, want %q", m.status, want)
		}
	})
	// The details round trip from the agent (ctrl+] d, then esc) says
	// nothing: opening clears the notice so no toast covers the overlay's
	// corner, and back in the agent its strip already names the way out, so
	// no toast names a focus key the user no longer needs.
	t.Run("details round trip", func(t *testing.T) {
		m := newModel()
		m, _ = update(t, m, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
		m, _ = update(t, m, keyEnter)
		m = flipViewKey(t, m)
		m = flipViewKey(t, m)
		if m.status != "" || !m.paneOwnsKeyboard() {
			t.Fatalf("round-trip status = %q, pane focused %v; want no notice and the agent focused", m.status, m.paneOwnsKeyboard())
		}
	})
}

// TestNormalHelpNeverOffersCtrlT pins objective 2 on the help surfaces: with
// the list focused ctrl+t controls nothing, so neither the short line nor `?`'s
// full view may offer it, whatever the selected row is — including the rows
// where ctrl+t used to attach, restart, hide or reveal.
func TestNormalHelpNeverOffersCtrlT(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(*Model)
		hiddenOther bool
	}{
		{"running session without a pane", func(*Model) {}, false},
		{"stopped session without a pane", func(m *Model) { m.sessions[0].HostRunning = false }, false},
		{"pending session", func(m *Model) { m.sessions[0].Pending = true }, false},
		{"worktree fault", func(m *Model) { m.sessions[0].Kind = session.KindWorktreeMissing }, false},
		{"cleaning session", func(m *Model) {
			m.cleaningSessions = map[cleanID]bool{cleanIDFor(m.sessions[0]): true}
		}, false},
		{"running selection with another pane hidden", func(*Model) {}, true},
		{"stopped selection with another pane hidden", func(m *Model) { m.sessions[0].HostRunning = false }, true},
		{"no selection with another pane hidden", func(m *Model) { m.setSessions(nil) }, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("alpha"))
			tc.setup(&m)
			m.setSessions(m.sessions)
			if tc.hiddenOther {
				m.pane = newFakePane("OLD")
				m.paneName = "other"
				m.view = viewOverview
			}
			if mode := derivedMode(m); mode != inputNormal {
				t.Fatalf("precondition: mode = %v, want normal", mode)
			}
			if help := xansi.Strip(m.renderHelp()); strings.Contains(help, "ctrl+t") {
				t.Errorf("list-focused help offers ctrl+t: %q", help)
			}
		})
	}
}

// TestNormalHelpAt80KeepsCombinedFocus guards the compact ordering: a no-pane
// selected session must still show the one way to the right side at the
// narrowest supported terminal width, and no ctrl+t beside it.
func TestNormalHelpAt80KeepsCombinedFocus(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m.width = 80
	help := xansi.Strip(m.renderHelp())
	if got := lipgloss.Width(help); got > m.width {
		t.Fatalf("help width = %d, want at most %d: %q", got, m.width, help)
	}
	if want := m.keys.Focus.Help().Key + " " + m.keys.Focus.Help().Desc; !strings.Contains(help, want) {
		t.Errorf("80-column help dropped %q: %q", want, help)
	}
	if strings.Contains(help, "ctrl+t") {
		t.Errorf("80-column list help offers ctrl+t: %q", help)
	}
}

// TestDetailFocusedFooterFitsWidth80KeepsReleaseKeys is plan step 6: at width
// 80 the focused footer must still show the release keys in full. W2.T1's
// truncation-survival ordering (release first) is what this depends on — see
// mode.go's inputDetail keys.bindings doc — so this test verifies the
// outcome rather than re-declaring the order (that belongs to W2.T1, not
// here).
func TestDetailFocusedFooterFitsWidth80KeepsReleaseKeys(t *testing.T) {
	m := testModel(runningSess("alpha"))
	m.width = 80

	// f on a running row in the agent view connects; focusOverview focuses
	// the overview itself, which is the footer under test.
	m = focusOverview(t, m)

	footer := m.renderHelp()
	if got := lipgloss.Width(footer); got > m.width {
		t.Fatalf("focused footer is %d cells wide, want at most %d", got, m.width)
	}
	if !strings.Contains(xansi.Strip(footer), "esc back") {
		t.Errorf("focused footer at width 80 dropped the release key, got: %q", footer)
	}
}

// TestAgentPlaceholderTextNothingSelected pins the no-selection branch of
// agentPlaceholderText, which must agree with restartingRow about what "a
// restart is in flight" means. The "stale gen" row is load-bearing: a restart
// dispatched in another project generation must not paint "restarting" over
// the current project's empty selection. The "current gen" row is the one that
// proves the branch consults the in-flight restart at all.
func TestAgentPlaceholderTextNothingSelected(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(m *Model)
		wantLine string
	}{
		{"restart in flight, current gen", func(m *Model) {
			m.restartInFlight = inFlightRestart{name: "a", gen: m.projectGen}
		}, "restarting a…"},
		{"restart in flight, stale gen", func(m *Model) {
			m.restartInFlight = inFlightRestart{name: "a", gen: m.projectGen + 1}
		}, "no session selected"},
		{"no restart in flight", func(m *Model) {}, "no session selected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel()
			tc.setup(&m)
			if _, ok := m.selected(); ok {
				t.Fatalf("setup: nothing should be selected")
			}
			if _, line, action := m.agentPlaceholderText(); line != tc.wantLine || action != "" {
				t.Errorf("line, action = %q, %q; want %q, empty action", line, action, tc.wantLine)
			}
		})
	}
}

// TestPlaceholderFooterOffersEnterOnlyWhenItConnects pins that the agent
// placeholder's footer and its box agree about enter: both offer it exactly
// when enter would start a new connect. While the row's dial or restart is
// already out, enter only joins it, so advertising it there names a key that
// does nothing new. The "lost, reconnect dialing" row is load-bearing: the
// lost note outlives the reconnect's dispatch, and if it outranks the dial in
// agentPlaceholderText the box and footer offer a reconnect that is already
// happening. ctrl+t and ctrl+\ are live in every state and must always show.
func TestPlaceholderFooterOffersEnterOnlyWhenItConnects(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(m *Model)
		wantEnter bool
	}{
		{"idle running row", func(m *Model) {}, true},
		{"lost pane", func(m *Model) {
			m.paneLost = lostPane{name: "a", message: "a was taken over by another window or the web page; press enter to reopen it"}
		}, true},
		{"dial out", func(m *Model) {
			m.attachInFlight = inFlightOpen{name: "a"}
		}, false},
		{"restart out", func(m *Model) {
			m.restartInFlight = inFlightRestart{name: "a", gen: m.projectGen}
		}, false},
		{"lost, reconnect dialing", func(m *Model) {
			m.paneLost = lostPane{name: "a", message: "a was taken over by another window or the web page; press enter to reopen it"}
			m.attachInFlight = inFlightOpen{name: "a"}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(runningSess("a"))
			m.width = 200
			m.focus = focusRight
			m.view = viewAgent
			tc.setup(&m)
			if !m.agentPlaceholderDrawn() {
				t.Fatalf("setup: the agent placeholder should be drawn")
			}
			footer := m.renderHelp()
			enterKey := m.keys.Focus.Help().Key
			if got := strings.Contains(footer, enterKey); got != tc.wantEnter {
				t.Errorf("footer offers %q = %v, want %v: %q", enterKey, got, tc.wantEnter, footer)
			}
			if _, _, action := m.agentPlaceholderText(); (action != "") != tc.wantEnter {
				t.Errorf("box action = %q, want an action = %v", action, tc.wantEnter)
			}
			// The toggle and back-to-list chords moved into the actions
			// dialog; the footer keeps the one entry that opens it.
			if want := defaultLeaderKey; !strings.Contains(footer, want) {
				t.Errorf("footer dropped %q: %q", want, footer)
			}
		})
	}
}

// TestKeyNameNamesEveryTableKey holds keyName to the key table: for every
// keyed row, at every level it is listed at, the hint is the key alone at All
// projects and Project and the leader then the key in the Agent, so a hint can
// never name a key the dashboard does not answer to there. Load-bearing: the
// Agent rows (a bare key there is typed to the agent), and the by-name rows,
// which have no key and must not be given one.
func TestKeyNameNamesEveryTableKey(t *testing.T) {
	for _, r := range keyTable {
		for _, lv := range []level{levelAllProjects, levelProject, levelAgent} {
			if !r.levels.has(lv) {
				continue
			}
			t.Run(r.name+" at "+lv.String(), func(t *testing.T) {
				got := keyName(defaultLeaderKey, lv, r.action)
				want := r.keyLabel()
				if lv == levelAgent && r.key != "" {
					want = defaultLeaderKey + " " + want
				}
				if r.key == "" {
					// "start a new plan" is `n` at All projects and by name
					// inside a project; the hint names the key it has.
					if r.action == actionPlanNew {
						want = "n"
					} else {
						want = ""
					}
				}
				if got != want {
					t.Errorf("keyName = %q, want %q", got, want)
				}
			})
		}
	}
}

// TestHintsNameTheKeys pins every hint and notice T16.2 rewrote. Load-bearing
// rows:
//   - the footer rows: the list names enter, `:` and `?` alone, the agent
//     pane the leader and its mouse key after it, keeping its "input →
//     session" prefix, and the overview its way back.
//   - full help: one entry per key-table row at Project that can run.
//   - wedge banner hint: shown inside the agent pane, where a bare r is the
//     agent's, so it names the way out of the Agent first.
//   - the old-key rows: ctrl+t, ctrl+g, ctrl+\ and the retired leader chords
//     in a hint name a key that does nothing.
func TestHintsNameTheKeys(t *testing.T) {
	strip := func(m Model) string { return xansi.Strip(m.renderHelp()) }

	list := testModel(runningSess("alpha"))
	list.width = 120

	pane := testModel(runningSess("alpha"))
	pane.width = 120
	pane, _ = update(t, pane, paneAttachedMsg{pane: newFakePane("X"), name: "alpha"})
	pane, _ = update(t, pane, keyEnter)
	if !pane.paneOwnsKeyboard() {
		t.Fatal("fixture broken: the pane does not have the keyboard")
	}

	detail := testModel(runningSess("alpha"))
	detail.width = 120
	detail, _ = update(t, detail, keyRune('d'))
	if !detail.detailOwnsKeyboard() {
		t.Fatal("fixture broken: the detail pane does not have the keyboard")
	}

	footers := []struct {
		name string
		got  string
		want []string
	}{
		{"session list", strip(list), []string{"enter open", "esc all projects", ": actions", "? keys"}},
		{"focused agent pane", strip(pane), []string{"input → session · ", "ctrl+] afk keys", "ctrl+] m mouse:"}},
		{"focused detail pane", strip(detail), []string{"esc back"}},
	}
	for _, tc := range footers {
		t.Run("footer: "+tc.name, func(t *testing.T) {
			for _, want := range tc.want {
				if !strings.Contains(tc.got, want) {
					t.Errorf("footer = %q, want it to contain %q", tc.got, want)
				}
			}
		})
	}

	t.Run("the keys overlay lists the key-table entries", func(t *testing.T) {
		// `?` replaced the full help (W3.T1 of T16.2): every Project key is
		// in the overlay's Project section or, when it works everywhere, in
		// its Everywhere section, with its help line.
		listed := map[string]string{}
		for _, sec := range keysSections(defaultLeaderKey) {
			if sec.title == "Project" || strings.HasPrefix(sec.title, "Everywhere") {
				for _, e := range sec.entries {
					listed[e.key] = e.help
				}
			}
		}
		for _, r := range keyTable {
			if !r.levels.has(levelProject) || r.key == "" {
				continue
			}
			if help, ok := listed[r.keyLabel()]; !ok || help != r.help {
				t.Errorf("%s: the keys overlay lists %q as %q, want %q", r.name, r.keyLabel(), help, r.help)
			}
		}
	})

	t.Run("texts name the key", func(t *testing.T) {
		for _, tc := range []struct{ name, got, want string }{
			{"header switch hint", headerSwitchHint(defaultLeaderKey, levelProject), "/: jump"},
			{"header switch hint in the agent", headerSwitchHint(defaultLeaderKey, levelAgent), "ctrl+] /: jump"},
			{"header inbox hint", headerInboxHint(defaultLeaderKey, levelProject), "i: inbox"},
			{"wedge banner hint", wedgeHintDashboard(defaultLeaderKey), "ctrl+] esc then r"},
			{"pane focus hint", paneFocusHint(defaultLeaderKey), "ctrl+] esc"},
			{"pause notice", func() string { _, s := pauseNotice(defaultLeaderKey, false); return s }(), "Press p to resume"},
			{"mouse hint", list.mouseCaptureHint(), "ctrl+] m"},
		} {
			if !strings.Contains(tc.got, tc.want) {
				t.Errorf("%s = %q, want it to name %q", tc.name, tc.got, tc.want)
			}
		}
	})

	t.Run("no hint names an old key as an action key", func(t *testing.T) {
		for name, text := range map[string]string{
			"switch hint": headerSwitchHint(defaultLeaderKey, levelProject), "inbox hint": headerInboxHint(defaultLeaderKey, levelProject),
			"wedge hint": wedgeHintDashboard(defaultLeaderKey), "pane focus hint": paneFocusHint(defaultLeaderKey),
			"list footer": strip(list), "pane footer": strip(pane), "detail footer": strip(detail),
			"empty state": xansi.Strip(testModel().renderRowsBody(60, 10)),
		} {
			for _, old := range []string{"ctrl+t", "ctrl+g", "ctrl+\\ ", "enter/f", "ctrl+] n", "ctrl+] \\", "ctrl+] g", "ctrl+] t", "ctrl+] p"} {
				if strings.Contains(text, old) {
					t.Errorf("%s = %q, names the old key %q", name, text, old)
				}
			}
		}
	})
}

// The tide tokens the redraw tests below look for in raw SGR, spelled out (as
// render_test.go's selectionBg is) so a theme edit fails them rather than
// making them vacuous.
const (
	tideGroundBg = "48;2;10;18;34"   // bg #0a1222
	tidePanelBg  = "48;2;12;22;41"   // panel #0c1629
	tideStripBg  = "48;2;22;39;74"   // strip #16274a
	tideAccentBg = "48;2;99;179;242" // accent #63b3f2
)

// boxInner is a box row's text between its left and right border glyphs,
// escapes included, for bgActiveAtEveryCell.
func boxInner(line string) string {
	from := strings.Index(line, "│")
	to := strings.LastIndex(line, "│")
	if from < 0 || to <= from {
		return ""
	}
	return line[from+len("│") : to]
}

// TestPaneFillsRowsToTheBorder pins the pane part's body, which today's
// screens are drawn in since T16.1. Load-bearing rows: "selected row reaches
// the border" is §3.5's edge-to-edge selection (the pane's padding wears the
// row's own fill, so the bar does not stop a cell short of the border on each
// side); "plain row is panel" is §1's pane interior; the two flat rows are the
// 16-colour terminal, where panel must not be painted but a selection still
// fills. Every row also checks the pane is the exact width sizedBox gave the
// padded style, which every geometry helper (sidebarRenderedWidth,
// paneContentDims, detailContentWidth) was measured against, and one row
// taller for the title strip.
func TestPaneFillsRowsToTheBorder(t *testing.T) {
	set := newStyleSet(palette.Tide)
	selected := set.row("▌ alpha", 10, true, "")
	cases := []struct {
		name   string
		flat   bool
		row    string
		wantBg string // in force at every cell between the borders; "" = no background at all
	}{
		{"selected row reaches the border", false, selected, selectionBg},
		{"plain row is panel to the border", false, "alpha", tidePanelBg},
		{"flat set keeps a selected row's fill", true, selected, selectionBg},
		{"flat set leaves a plain row unpainted", true, "alpha", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStyleSet(palette.Tide)
			s.flat = tc.flat
			got := strings.Split(s.pane("T", "", tc.row, paneOuterWidth(10), 3+paneStripRows+boxBorderCells, false), "\n")
			want := strings.Split(sizedBox(s.listBox, 12, 3+paneStripRows).Render("x"), "\n")
			if len(got) != len(want) {
				t.Fatalf("pane is %d rows, want sizedBox's %d", len(got), len(want))
			}
			for i := range got {
				if gw, ww := lipgloss.Width(got[i]), lipgloss.Width(want[i]); gw != ww {
					t.Errorf("row %d is %d cells, want sizedBox's %d", i, gw, ww)
				}
			}
			inner := boxInner(got[2])
			if tc.wantBg == "" {
				if strings.Contains(inner, "48;") {
					t.Errorf("a flat pane painted a background: %q", inner)
				}
				return
			}
			if at, ok := bgActiveAtEveryCell(inner, tc.wantBg); !ok {
				t.Errorf("%s is not in force at byte %d of the row: %q", tc.wantBg, at, inner)
			}
			if !tc.flat {
				if at, ok := bgActiveAtEveryCell(boxInner(got[3]), tidePanelBg); !ok {
					t.Errorf("an empty row below the block is not panel at byte %d: %q", at, got[3])
				}
			}
		})
	}
}

// TestPaneTitleStripLayout pins the pane part's title strip, its first inner
// row. Load-bearing rows: "name and context" is the session list's strip
// (project left, paging hint right); "context gives way" and "long name is
// cut" are the narrowing rules (the context goes first, then the name is cut
// with "…", never wrapped), so a narrow sidebar that pages still names its
// project; "focused" is §3.3's accent strip for the pane with the keyboard.
// Every row checks the strip stays exactly as wide as the pane's inside and
// its fill covers it border to border.
func TestPaneTitleStripLayout(t *testing.T) {
	cases := []struct {
		name       string
		title, ctx string
		width      int
		focused    bool
		prefix     string
		suffix     string
		not        string
		fill       string
	}{
		{"name and context", "beta", "page 1/2", 30, false, "│ beta ", " page 1/2 │", "", tideStripBg},
		{"context gives way to the name", "beta", "page 1/2 · pgup/pgdn", 20, false, "│ beta ", " │", "page", tideStripBg},
		{"long name is cut", strings.Repeat("x", 40), "", 20, false, "│ xxx", "… │", "", tideStripBg},
		{"focused strip is accent", "beta", "", 30, true, "│ beta ", " │", "", tideAccentBg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Split(theme.pane(tc.title, tc.ctx, "", tc.width, 4, tc.focused), "\n")[1]
			text := xansi.Strip(got)
			if w := lipgloss.Width(got); w != tc.width {
				t.Errorf("strip row is %d cells, want the pane's %d: %q", w, tc.width, text)
			}
			if !strings.HasPrefix(text, tc.prefix) || !strings.HasSuffix(text, tc.suffix) {
				t.Errorf("strip row = %q, want it to start %q and end %q", text, tc.prefix, tc.suffix)
			}
			if tc.not != "" && strings.Contains(text, tc.not) {
				t.Errorf("strip row = %q, must not contain %q", text, tc.not)
			}
			if at, ok := bgActiveAtEveryCell(boxInner(got), tc.fill); !ok {
				t.Errorf("the strip's fill %s stops at byte %d: %q", tc.fill, at, got)
			}
		})
	}
}

// TestFooterIsAStatusBar pins §3.2 on today's footer: the row is filled with
// strip edge to edge, and its words are renderHelp's, unchanged. Both rows
// are load-bearing: the normal list is the common case, and the keys overlay
// (?) is the footer of an overlay, which must be filled the same way.
func TestFooterIsAStatusBar(t *testing.T) {
	cases := []struct {
		name string
		keys bool
	}{
		{"normal short help", false},
		{"keys overlay open", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel(t, 120, 40)
			if tc.keys {
				m, _ = update(t, m, keyRune('?'))
			}
			footer := strings.Split(m.renderFooter(), "\n")
			help := strings.Split(m.renderHelp(), "\n")
			if len(footer) != len(help) {
				t.Fatalf("footer is %d rows, want renderHelp's %d", len(footer), len(help))
			}
			for i := range footer {
				if w := lipgloss.Width(footer[i]); w != m.width {
					t.Errorf("row %d is %d cells, want the full width %d", i, w, m.width)
				}
				if at, ok := bgActiveAtEveryCell(footer[i], tideStripBg); !ok {
					t.Errorf("row %d: strip stops at byte %d: %q", i, at, footer[i])
				}
				got := strings.TrimRight(xansi.Strip(footer[i]), " ")
				want := strings.TrimRight(xansi.Strip(help[i]), " ")
				if got != want {
					t.Errorf("row %d words changed:\n got %q\nwant %q", i, got, want)
				}
			}
		})
	}
}

// TestViewGroundReachesTheRightEdge pins the ground on every cell of the
// frame. Load-bearing rows: "under a dialog" is the case the row padding in
// View exists for (the Compositor trims a row's bare trailing blanks, which
// left the top bar and the status bar unpainted at their right end); "16
// colours" is §1's exception, where neither the ground nor the panes' panel
// may be painted.
func TestViewGroundReachesTheRightEdge(t *testing.T) {
	cases := []struct {
		name    string
		profile colorprofile.Profile
		dialog  bool
	}{
		{"plain frame", colorprofile.TrueColor, false},
		{"under a dialog", colorprofile.TrueColor, true},
		{"16 colours paint no ground", colorprofile.ANSI, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { useTheme(palette.Tide) })
			d := testDeps()
			d.ColorProfile = tc.profile
			m := New("/repo", 2*time.Second, d)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			m.setSessions(goldenSessions())
			if tc.dialog {
				m, _ = update(t, m, leaderKeyPress)
				var cmd tea.Cmd
				m, cmd = update(t, m, keyRune('c'))
				m = pumpForm(t, m, cmd, 0)
				if m.modal != inputConfirmClean {
					t.Fatal("c did not open the clean confirmation")
				}
			}
			frame := m.View().Content
			if tc.profile == colorprofile.ANSI {
				for _, bg := range []string{tideGroundBg, tidePanelBg} {
					if strings.Contains(frame, bg) {
						t.Errorf("a 16-colour frame carries %s", bg)
					}
				}
				return
			}
			for i, line := range strings.Split(frame, "\n") {
				if w := lipgloss.Width(line); w != m.width {
					t.Errorf("row %d is %d cells, want %d", i, w, m.width)
				}
			}
		})
	}
}

// TestHostIndicatorUsesTheStateMapping pins the session row's glyph to §3.6
// through stateLook (sessionGlyphState picks the state, stateLook draws it).
// Load-bearing rows: "done" and "failed" are a session that has ended, which
// says so whatever its host is doing ("done with its host still up" proves the
// ending outranks the host); "broken" and "orphan" are the worktree faults;
// "running" is the change T16.1 made (◐ in work, where a filled dot used to say
// "needs you" in every other place it appears); "stale with a live host" keeps
// the liveness the column exists to show; "usage paused" keeps the hollow
// glyph of a stopped host.
func TestHostIndicatorUsesTheStateMapping(t *testing.T) {
	paused := sess("alpha")
	paused.UsagePaused = true
	pending := Session{SessionState: discover.SessionState{Name: "alpha"}, Pending: true}
	withPhase := func(s Session, p state.Phase) Session {
		s.Phase = p
		return s
	}
	fault := func(k session.Kind) Session {
		return Session{SessionState: discover.SessionState{Name: "alpha", Kind: k}}
	}
	staleLive := staleSess("alpha")
	staleLive.HostRunning = true
	cases := []struct {
		name  string
		s     Session
		kind  stateKind
		value string
		glyph string
	}{
		{"running", runningSess("alpha"), stateSession, hostRunningState, "◐"},
		{"stopped", sess("alpha"), stateSession, hostStoppedState, "○"},
		{"pending has no host yet", pending, stateSession, hostStoppedState, "○"},
		{"usage paused", paused, stateSession, "paused", "○"},
		{"stale with a live host", staleLive, stateSession, hostRunningState, "◐"},
		{"done", withPhase(sess("alpha"), state.PhaseDone), stateSession, "done", "✓"},
		{"done with its host still up", withPhase(runningSess("alpha"), state.PhaseDone), stateSession, "done", "✓"},
		{"failed", withPhase(sess("alpha"), state.PhaseFailed), stateSession, "failed", "✕"},
		{"broken", fault(session.KindWorktreeMissing), stateFault, brokenBadgeLabel, "✕"},
		{"orphan", fault(session.KindWorktreeNoSession), stateFault, orphanBadgeLabel, "●"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hostIndicator(tc.s)
			if want := theme.renderGlyph(tc.kind, tc.value); got != want {
				t.Errorf("hostIndicator = %q, want stateLook's %q", got, want)
			}
			if xansi.Strip(got) != tc.glyph {
				t.Errorf("glyph = %q, want %q", xansi.Strip(got), tc.glyph)
			}
		})
	}
}

// TestDetailHelpJumpRange pins F11: the details help names the tab digits that
// are drawn. Load-bearing: the plain-repo row, where "1-5" would offer a digit
// that does nothing; the key binding itself is unchanged.
func TestDetailHelpJumpRange(t *testing.T) {
	cases := []struct {
		name string
		st   stackState
		want string
	}{
		{"no stack shows four tabs", stackState{}, "1-4"},
		{"a stack shows five tabs", stackState{Detected: true}, "1-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(sess("alpha"))
			m.stack = tc.st
			var got string
			for _, b := range m.renderDetailHelpKeyMap().short {
				if b.Help().Desc == "jump to tab" {
					got = b.Help().Key
				}
			}
			if got != tc.want {
				t.Fatalf("jump help key = %q, want %q", got, tc.want)
			}
		})
	}
}

// withMsg is s with the agent's own message set, as discovery copies it from
// host.json.
func withMsg(s Session, msg string) Session {
	s.AgentStatus = &discover.AgentStatus{State: "working", Msg: msg}
	return s
}

// TestSessionRowAgentMessage pins where a row shows the agent's message
// (renderSessionRow, the one-line row) and, as important, where it does not.
// Load-bearing rows: "narrow: every rowColumns rung" (below the message
// threshold the row is byte-for-byte the no-message row, so no column moved
// and the feature costs narrow terminals nothing), "no message" (a nil status
// and an empty msg draw as today), "long message" (it is cut with an ellipsis
// and the row is exactly width cells, so a hostile or long message cannot
// widen the row) and "control characters" (an escape inside the text must not
// reach the screen).
func TestSessionRowAgentMessage(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	base := sess("api")
	// A name wider than any narrow rung's name column: it fills the cell, so
	// no room is left over for a message.
	long := sess("a-fairly-long-session-name")
	strip := func(s string) string { return xansi.Strip(s) }
	cases := []struct {
		name  string
		msg   string
		width int
		long  bool   // use the long-named session
		want  string // substring of the stripped row; "" means the row equals the no-message row
	}{
		{"wide: the message shows", "Running tests", 80, false, "Running tests"},
		{"long message is truncated", strings.Repeat("word ", 40), 80, false, "word word"},
		{"control characters are dropped", "Run\x1b[31mning\x07 tests", 80, false, "Run [31mning tests"},
		{"narrow: rung 40", "Running tests", 40, true, ""},
		{"narrow: rung 32", "Running tests", 32, true, ""},
		{"narrow: rung 24", "Running tests", 24, true, ""},
		{"narrow: below 24", "Running tests", 20, true, ""},
		{"no message", "", 80, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := base
			if tc.long {
				base = long
			}
			got := renderSessionRow(withMsg(base, tc.msg), false, tc.width, now)
			if w := lipgloss.Width(got); w != tc.width {
				t.Errorf("row is %d cells, want exactly %d: %q", w, tc.width, strip(got))
			}
			if strings.ContainsAny(got, "\x07") {
				t.Errorf("a control character reached the row: %q", got)
			}
			plain := renderSessionRow(base, false, tc.width, now)
			if tc.want == "" {
				if got != plain {
					t.Errorf("row differs from the no-message row:\n got %q\nwant %q", got, plain)
				}
				return
			}
			if !strings.Contains(strip(got), tc.want) {
				t.Errorf("row %q lacks %q", strip(got), tc.want)
			}
		})
	}
	// The same session with and without a message keeps every column in
	// place: the text before the message is identical.
	with := strip(renderSessionRow(withMsg(base, "Running tests"), false, 80, now))
	without := strip(renderSessionRow(base, false, 80, now))
	if i := strings.Index(with, "Running tests"); i < 0 || !strings.HasPrefix(without, strings.TrimRight(with[:i], " ")) {
		t.Errorf("a message moved a column:\n with %q\n without %q", with, without)
	}
	if len(with) == 0 || lipgloss.Width(with) != lipgloss.Width(without) {
		t.Errorf("message changed the row width: %d vs %d", lipgloss.Width(with), lipgloss.Width(without))
	}
}

// TestSessionSectionRowAgentMessage is the same rule for the Project level's
// Sessions section row, the one the dashboard actually draws there: the
// message shows between the tasks count and the right word when the width has
// room, and a narrow section draws the row it drew before. Load-bearing row:
// "narrow" (the room left is under rowMessageMin, so nothing changes) and
// "section 46" (the 140x40 Sessions section: too tight beside "open it", so
// the message takes that hint's cells, and "asks you" is never given up).
func TestSessionSectionRowAgentMessage(t *testing.T) {
	base := runningSess("api")
	for _, tc := range []struct {
		name  string
		width int
		shows bool
	}{
		{"wide", 90, true},
		{"section 46", 46, true},
		{"narrow", 36, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionSectionRow(withMsg(base, "Running tests"), false, false, 4, tc.width)
			plain := sessionSectionRow(base, false, false, 4, tc.width)
			if w := lipgloss.Width(got); w != tc.width {
				t.Errorf("row is %d cells, want %d", w, tc.width)
			}
			if has := strings.Contains(xansi.Strip(got), "Running tests"); has != tc.shows {
				t.Errorf("message shown = %v, want %v: %q", has, tc.shows, xansi.Strip(got))
			}
			if !tc.shows && got != plain {
				t.Errorf("narrow row differs from the no-message row")
			}
		})
	}
}

// TestSessionSectionRowMessageKeepsAsksYou pins the one thing the message may
// not take: at the 140x40 section width a session that asks the owner keeps
// "asks you" and shows no message, while a running one trades "open it" for
// the message.
func TestSessionSectionRowMessageKeepsAsksYou(t *testing.T) {
	ask := runningSess("api")
	ask.AwaitingInput = true
	for _, tc := range []struct {
		name       string
		s          Session
		wantMsg    bool
		wantAsks   bool
		wantOpenIt bool
	}{
		{"running", runningSess("api"), true, false, false},
		{"asking", ask, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := xansi.Strip(sessionSectionRow(withMsg(tc.s, "Running tests"), false, false, 4, 46))
			if has := strings.Contains(got, "Running tests"); has != tc.wantMsg {
				t.Errorf("message shown = %v, want %v: %q", has, tc.wantMsg, got)
			}
			if has := strings.Contains(got, "asks you"); has != tc.wantAsks {
				t.Errorf("asks you shown = %v, want %v: %q", has, tc.wantAsks, got)
			}
			if has := strings.Contains(got, "open it"); has != tc.wantOpenIt {
				t.Errorf("open it shown = %v, want %v: %q", has, tc.wantOpenIt, got)
			}
		})
	}
}
