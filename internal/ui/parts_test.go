package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/state"
)

// TestStateLook pins §3.6's one mapping from a state to its glyph, colour and
// word. Load-bearing rows: one per session phase (state.Phase constants, so a
// renamed phase breaks here), the awaiting-input row (the only ● on a
// session), every fault the session list shows, and one per projectrows
// state constant — the all-projects screen draws through this table, and a
// label missing from it would fall back to the quiet look silently. The words
// must be today's words (this task changes no text on screen). "unknown
// value" and "unknown project" are the failure path: a value no table holds
// draws as the quiet ○ with its own word, and never panics.
func TestStateLook(t *testing.T) {
	tests := []struct {
		name      string
		kind      stateKind
		value     string
		wantGlyph string
		wantTone  stateTone
		wantWord  string
	}{
		{"phase planning", stateSession, string(state.PhasePlanning), "◐", toneWork, "planning"},
		{"phase executing", stateSession, string(state.PhaseExecuting), "◐", toneWork, "executing"},
		{"phase reviewing", stateSession, string(state.PhaseReviewing), "◐", toneWork, "reviewing"},
		{"phase fixing", stateSession, string(state.PhaseFixing), "◐", toneWork, "fixing"},
		{"phase done", stateSession, string(state.PhaseDone), "✓", toneDone, "done"},
		{"phase failed", stateSession, string(state.PhaseFailed), "✕", toneBad, "failed"},
		{"awaiting input", stateSession, awaitingInputState, "●", toneNeed, "awaiting input"},
		{"one-off session", stateSession, scratchPhaseKey, "○", toneQuiet, "scratch"},
		{"usage paused session", stateSession, "paused", "○", toneQuiet, "paused"},
		{"pending session", stateSession, pendingBadge, "○", toneQuiet, pendingBadge},
		{"empty phase", stateSession, "", "✕", toneBad, "unknown"},
		{"fault stale", stateFault, "STALE", "✕", toneBad, "STALE"},
		{"fault broken", stateFault, brokenBadgeLabel, "✕", toneBad, "BROKEN"},
		{"fault unknown", stateFault, "UNKNOWN", "✕", toneBad, "UNKNOWN"},
		{"fault orphan", stateFault, orphanBadgeLabel, "●", toneNeed, "ORPHAN"},
		{"fault lower case", stateFault, "stale", "✕", toneBad, "STALE"},
		{"project gone", stateProject, string(projectrows.StateGone), "✕", toneBad, "gone"},
		{"project can't read", stateProject, string(projectrows.StateUnreadable), "✕", toneBad, "can't read"},
		{"project no plan", stateProject, string(projectrows.StateNoPlan), "○", toneQuiet, "no plan"},
		{"project stopped", stateProject, string(projectrows.StateStopped), "✕", toneBad, "stopped"},
		{"project paused", stateProject, string(projectrows.StatePaused), "○", toneQuiet, "paused"},
		{"project waiting", stateProject, string(projectrows.StateWaiting), "●", toneNeed, "waiting on you"},
		{"project held", stateProject, string(projectrows.StateHeld), "○", toneQuiet, "held for usage"},
		{"project running", stateProject, string(projectrows.StateRunning), "◐", toneWork, "running"},
		{"project finished", stateProject, string(projectrows.StateFinished), "✓", toneDone, "finished"},
		{"project nothing ready", stateProject, string(projectrows.StateNothingReady), "○", toneQuiet, "nothing ready"},
		{"project executing", stateProject, "executing", "◐", toneWork, "executing"},
		{"project shipping", stateProject, "shipping", "◐", toneWork, "shipping"},
		{"project plan to approve", stateProject, "plan to approve", "●", toneNeed, "plan to approve"},
		{"project parked", stateProject, "parked", "●", toneNeed, "parked"},
		{"project failed", stateProject, "failed", "✕", toneBad, "failed"},
		{"project done", stateProject, "done", "✓", toneDone, "done"},
		{"project pending", stateProject, "pending", "○", toneQuiet, "pending"},
		{"project one-off", stateProject, "one-off", "○", toneQuiet, "one-off"},
		{"unknown value", stateSession, "nope", "○", toneQuiet, "nope"},
		{"unknown project", stateProject, " nope ", "○", toneQuiet, "nope"},
		{"unknown kind", stateKind(99), "running", "○", toneQuiet, "running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stateLook(tt.kind, tt.value)
			if got.glyph != tt.wantGlyph || got.tone != tt.wantTone || got.word != tt.wantWord {
				t.Errorf("stateLook(%d, %q) = {%q %d %q}, want {%q %d %q}",
					tt.kind, tt.value, got.glyph, got.tone, got.word, tt.wantGlyph, tt.wantTone, tt.wantWord)
			}
		})
	}
}

// TestStateToneColours pins each tone's colour to its §1 token, and that
// renderState draws glyph and word in it.
func TestStateToneColours(t *testing.T) {
	s := newStyleSet(palette.Tide)
	tests := []struct {
		tone stateTone
		want string
	}{
		{toneQuiet, palette.Tide.Muted},
		{toneNeed, palette.Tide.Need},
		{toneBad, palette.Tide.Bad},
		{toneWork, palette.Tide.Work},
		{toneDone, palette.Tide.Done},
	}
	for _, tt := range tests {
		t.Run(toneGlyph[tt.tone], func(t *testing.T) {
			if got := hexOf(s.toneColor(tt.tone)); got != tt.want {
				t.Errorf("toneColor = %s, want %s", got, tt.want)
			}
		})
	}
	got := s.renderState(stateSession, "executing")
	if xansi.Strip(got) != "◐ executing" {
		t.Errorf("renderState text = %q", xansi.Strip(got))
	}
	if c := cellOf(t, got, 0, 0); hexOf(c.Style.Fg) != palette.Tide.Work {
		t.Errorf("renderState glyph fg = %s, want work %s", hexOf(c.Style.Fg), palette.Tide.Work)
	}
}

// sgrFg is the SGR foreground parameter run for a "#rrggbb" token.
func sgrFg(hex string) string {
	return "38;2;" + strings.TrimPrefix(sgrBg(hex), "48;2;")
}

// TestPartRow pins §3.5's row. Load-bearing rows: "selected" — filled with
// sel at every cell, edge to edge, its words sel-fg and a coloured word on it
// stripped of its own colour (assertHighlightSpansRow is the same check the
// session list's highlight bar is held to); "plain" — no padding of its own,
// since the pane's padding is the row's; "truncated" — a long name is cut
// with … and the row stays its width; "linked" ends with ◂ name.
func TestPartRow(t *testing.T) {
	s := newStyleSet(palette.Tide)
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("red")

	t.Run("selected", func(t *testing.T) {
		got := s.row("a "+red+" word", 30, true, "")
		assertHighlightSpansRow(t, got, 30)
		if !strings.Contains(got, sgrFg(palette.Tide.SelFg)) {
			t.Errorf("selected row words are not sel-fg: %q", got)
		}
		if strings.Contains(got, "38;2;255;0;0") {
			t.Errorf("a coloured word kept its colour on the selection: %q", got)
		}
		if text := xansi.Strip(got); text != "a red word"+strings.Repeat(" ", 20) {
			t.Errorf("text = %q", text)
		}
	})
	t.Run("plain", func(t *testing.T) {
		got := s.row("name", 10, false, "")
		if got != "name      " {
			t.Errorf("plain row = %q", got)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		got := s.row("a very long session name", 12, false, "")
		if text := xansi.Strip(got); text != "a very long…" {
			t.Errorf("text = %q, want %q", text, "a very long…")
		}
	})
	t.Run("linked", func(t *testing.T) {
		got := s.row("T3 build", 24, false, "feat")
		if text := xansi.Strip(got); text != "T3 build          ◂ feat" {
			t.Errorf("text = %q", text)
		}
		if at, ok := bgActiveAtEveryCell(got, sgrBg(palette.Tide.Strip)); !ok {
			t.Errorf("linked row strip fill stops at byte %d: %q", at, got)
		}
		if !strings.Contains(got, sgrFg(palette.Tide.Accent)) {
			t.Errorf("link mark is not accent: %q", got)
		}
	})
	t.Run("selected outranks linked", func(t *testing.T) {
		got := s.row("T3", 10, true, "feat")
		if strings.Contains(xansi.Strip(got), "◂") {
			t.Errorf("selected row shows the link mark: %q", xansi.Strip(got))
		}
	})
}

// TestPartTitleStrip pins §3.3's focus rule: unfocused strip-fg on strip,
// focused on-accent on accent, either way filled across its whole width.
func TestPartTitleStrip(t *testing.T) {
	s := newStyleSet(palette.Tide)
	tests := []struct {
		name    string
		focused bool
		bg, fg  string
	}{
		{"unfocused", false, palette.Tide.Strip, palette.Tide.StripFg},
		{"focused", true, palette.Tide.Accent, palette.Tide.OnAccent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.titleStrip("Sessions", "1/3", 24, tt.focused)
			if text := xansi.Strip(got); text != " Sessions           1/3 " {
				t.Errorf("text = %q", text)
			}
			if at, ok := bgActiveAtEveryCell(got, sgrBg(tt.bg)); !ok {
				t.Errorf("strip fill stops at byte %d: %q", at, got)
			}
			if !strings.Contains(got, sgrFg(tt.fg)) {
				t.Errorf("strip text colour missing %s: %q", tt.fg, got)
			}
		})
	}
}

// TestPartPane pins the pane's geometry and colours: outer size exactly w×h,
// the border line (accent when focused), the strip on the first inner row, a
// numbered section, and every interior cell painted panel. "too small" must
// not panic or draw a broken box.
func TestPartPane(t *testing.T) {
	s := newStyleSet(palette.Tide)
	for _, focused := range []bool{false, true} {
		name := "unfocused"
		border := palette.Tide.Line
		if focused {
			name, border = "focused", palette.Tide.Accent
		}
		t.Run(name, func(t *testing.T) {
			got := s.pane("Agent", "feat", "line one\nline two", 20, 6, focused)
			lines := strings.Split(got, "\n")
			if len(lines) != 6 {
				t.Fatalf("pane has %d rows, want 6:\n%s", len(lines), xansi.Strip(got))
			}
			for i, ln := range lines {
				if w := lipgloss.Width(ln); w != 20 {
					t.Errorf("row %d is %d cells, want 20", i, w)
				}
			}
			if c := cellOf(t, got, 0, 0); hexOf(c.Style.Fg) != border {
				t.Errorf("border fg = %s, want %s", hexOf(c.Style.Fg), border)
			}
			if text := xansi.Strip(lines[1]); text != "│ Agent       feat │" {
				t.Errorf("strip row = %q", text)
			}
			// Body cells, including the blanks after the words, are panel.
			for _, x := range []int{1, 9, 18} {
				if c := cellOf(t, got, x, 2); hexOf(c.Style.Bg) != palette.Tide.Panel {
					t.Errorf("interior (%d,2) bg = %s, want panel %s", x, hexOf(c.Style.Bg), palette.Tide.Panel)
				}
			}
			if c := cellOf(t, got, 1, 4); hexOf(c.Style.Bg) != palette.Tide.Panel {
				t.Errorf("empty body row bg = %s, want panel", hexOf(c.Style.Bg))
			}
		})
	}
	t.Run("section numbered", func(t *testing.T) {
		got := s.section(2, "Build", "", "", 20, 4, false)
		if !strings.Contains(xansi.Strip(got), " 2 Build ") {
			t.Errorf("section strip = %q", xansi.Strip(got))
		}
	})
	t.Run("narrow section names all three", func(t *testing.T) {
		names := []string{"Sessions", "Build", "Inbox"}
		for _, focused := range []bool{false, true} {
			got := s.sectionTabs(names, 1, "1/7", "row", 50, 4, focused)
			lines := strings.Split(xansi.Strip(got), "\n")
			if len(lines) != 4 || lines[1] != "│ 1 Sessions · 2 Build · 3 Inbox             1/7 │" {
				t.Errorf("focused=%v: narrow section =\n%s", focused, strings.Join(lines, "\n"))
			}
			// The shown section is the bold one; the others are not.
			if c := cellOf(t, got, 15, 1); c.Style.Attrs&uv.AttrBold == 0 {
				t.Errorf("focused=%v: the shown name is not bold", focused)
			}
			if c := cellOf(t, got, 2, 1); c.Style.Attrs&uv.AttrBold != 0 {
				t.Errorf("focused=%v: a name not shown is bold", focused)
			}
			if !focused {
				if c := cellOf(t, got, 2, 1); hexOf(c.Style.Fg) != palette.Tide.Muted {
					t.Errorf("a name not shown is %s, want muted", hexOf(c.Style.Fg))
				}
			}
		}
		// Too narrow for the names and the context: the context goes first.
		got := xansi.Strip(s.sectionTabs(names, 0, "1/7", "", 34, 3, false))
		if line := strings.Split(got, "\n")[1]; line != "│ 1 Sessions · 2 Build · 3 Inbox │" {
			t.Errorf("cut strip = %q", line)
		}
	})
	t.Run("read-only", func(t *testing.T) {
		got := s.readOnlyPane("Summary", "", "body", 20, 6)
		lines := strings.Split(xansi.Strip(got), "\n")
		if len(lines) != 6 || lines[1] != "│ Summary          │" || lines[2] != "│"+strings.Repeat("─", 18)+"│" {
			t.Errorf("read-only pane =\n%s", strings.Join(lines, "\n"))
		}
		if c := cellOf(t, got, 2, 1); c.Style.Bg != nil || hexOf(c.Style.Fg) != palette.Tide.Muted {
			t.Errorf("read-only strip: bg %s fg %s, want no fill and muted", hexOf(c.Style.Bg), hexOf(c.Style.Fg))
		}
	})
	t.Run("body cut", func(t *testing.T) {
		got := s.pane("A", "", "1\n2\n3\n4\n5", 10, 4, false)
		if lines := strings.Split(xansi.Strip(got), "\n"); len(lines) != 4 || !strings.HasPrefix(lines[2], "│ 1") {
			t.Errorf("cut pane =\n%s", xansi.Strip(got))
		}
	})
	t.Run("too small", func(t *testing.T) {
		if got := s.pane("A", "", "x", 2, 2, false); got != "" {
			t.Errorf("2x2 pane = %q, want empty", got)
		}
	})
}

// TestPartBars pins the one-row parts' text and width: progress bars (the
// rounding of the filled part and the label kept whole), key chip, band,
// alert bar, group heading, status bar and top bar. Load-bearing: "progress
// 3/12" and "usage 62%" are §3.7's two labels; "status bar cut" proves the
// keys survive and the message is cut with … when the row is short.
func TestPartBars(t *testing.T) {
	s := newStyleSet(palette.Tide)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"progress 3/12", s.progressCount(3, 12, 20), "████░░░░░░░░░░░ 3/12"},
		{"progress none", s.progressCount(0, 0, 8), "░░░░ 0/0"},
		{"progress too narrow", s.progressCount(3, 12, 4), "3/12"},
		{"usage 62%", s.progressPercent(62, 14), "██████░░░░ 62%"},
		{"key chip", s.keyChip("enter", "open agent"), " enter  open agent"},
		{"band", s.band("● 4 need you", 16), " ● 4 need you   "},
		{"alert bar", s.alertBar("feat asks you", 16), " feat asks you  "},
		{"group heading", s.groupHeading("Needs you", 3, toneNeed, false, 20), " ▾ Needs you      3 "},
		{"group folded", s.groupHeading("Done", 12, toneDone, true, 12), " ▸ Done  12 "},
		{"status bar", s.statusBar("● feat asks you", s.statusKeys([]keyHint{{"?", "keys"}}), 30), "● feat asks you         ? keys"},
		{"status bar cut", s.statusBar("● a very long message here", s.statusKeys([]keyHint{{"q", "quit"}, {"?", "keys"}}), 26), "● a very l… q quit  ? keys"},
		{"status bar help row", s.statusBar("q quit • ? help", "", 20), "q quit • ? help     "},
		{"top bar", s.topBar(s.topBarPath("afk", []string{"All projects", "afk"}), "1 more", 34), " afk  All projects ▸ afk    1 more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if text := xansi.Strip(tt.got); text != tt.want {
				t.Errorf("text = %q, want %q", text, tt.want)
			}
		})
	}
	t.Run("progress colours", func(t *testing.T) {
		got := s.progressCount(1, 2, 7)
		if c := cellOf(t, got, 0, 0); hexOf(c.Style.Fg) != palette.Tide.Done {
			t.Errorf("filled cell fg = %s, want done", hexOf(c.Style.Fg))
		}
		if c := cellOf(t, got, 2, 0); hexOf(c.Style.Fg) != palette.Tide.Faint {
			t.Errorf("empty cell fg = %s, want faint", hexOf(c.Style.Fg))
		}
	})
	t.Run("key chip colours", func(t *testing.T) {
		got := s.keyChip("d", "details")
		c := cellOf(t, got, 1, 0)
		if hexOf(c.Style.Bg) != palette.Tide.Accent || hexOf(c.Style.Fg) != palette.Tide.OnAccent {
			t.Errorf("key cell fg %s bg %s, want on-accent on accent", hexOf(c.Style.Fg), hexOf(c.Style.Bg))
		}
		if c := cellOf(t, got, 5, 0); hexOf(c.Style.Fg) != palette.Tide.Muted {
			t.Errorf("action fg = %s, want muted", hexOf(c.Style.Fg))
		}
	})
	t.Run("status bar fill", func(t *testing.T) {
		got := s.statusBar("x", s.statusKeys([]keyHint{{"q", "quit"}}), 20)
		if at, ok := bgActiveAtEveryCell(got, sgrBg(palette.Tide.Strip)); !ok {
			t.Errorf("status bar fill stops at byte %d: %q", at, got)
		}
		if lipgloss.Width(got) != 20 {
			t.Errorf("status bar width = %d", lipgloss.Width(got))
		}
	})
	t.Run("alert bar fill", func(t *testing.T) {
		got := s.alertBar("x", 10)
		if at, ok := bgActiveAtEveryCell(got, sgrBg(palette.Tide.Need)); !ok {
			t.Errorf("alert bar fill stops at byte %d: %q", at, got)
		}
	})
	t.Run("top bar badge", func(t *testing.T) {
		got := s.topBar(s.topBarPath("afk", []string{"afk"}), "", 20)
		c := cellOf(t, got, 1, 0)
		if hexOf(c.Style.Bg) != palette.Tide.Accent || hexOf(c.Style.Fg) != palette.Tide.OnAccent {
			t.Errorf("badge fg %s bg %s, want on-accent on accent", hexOf(c.Style.Fg), hexOf(c.Style.Bg))
		}
		if lipgloss.Width(got) != 20 {
			t.Errorf("top bar width = %d", lipgloss.Width(got))
		}
	})
}
