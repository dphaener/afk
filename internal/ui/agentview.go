// agentview.go holds the Agent screen's own parts (docs/dash-design.md §4.3):
// the band under the strip, the chips row at the foot of the box, and the one
// rule that shares the box's rows between them and the agent's terminal.
//
// The row rule (agentBoxRows) is the single geometry source behind the
// agent's size and its place on screen: agentContentDims takes the terminal's
// rows from it (and so paneRegionSize, attachPaneCmd's size and relayout's
// Resize do), and paneScreenGeometry moves the origin down by its band row
// (and so the mouse and the cursor do). If the drawing and those two ever
// disagreed, the agent would draw at the wrong size and clicks would land a
// row off, which is why nothing else may count the rows itself.
package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// agentRows is how the Agent box's inner rows under the title strip are
// shared: the band row, the agent's terminal and the chips row.
type agentRows struct {
	band, screen, chips int
}

// agentBoxRows shares out the rows of an Agent box outerH tall. With room for
// all three, the band and the chips take one row each and the terminal the
// rest. On a box too short for that (the body's floor of 4 rows makes the
// box as little as 4 tall) the chips go first and then the band, because
// the terminal is the one thing in the box the owner cannot get elsewhere:
// the band repeats the Project's facts, and the chips repeat the key strip.
// The terminal keeps at least one row, as termpane needs a positive size.
func agentBoxRows(outerH int) agentRows {
	avail := outerH - boxBorderCells - paneStripRows
	switch {
	case avail >= 3:
		return agentRows{band: 1, screen: avail - 2, chips: 1}
	case avail == 2:
		return agentRows{band: 1, screen: 1}
	}
	return agentRows{screen: max(avail, 1)}
}

// agentBandSep is what sits between the band's pieces, the project band's
// separator (projectBandText), so the two bands read alike.
const agentBandSep = "  ·  "

// agentNextPrefix leads the band's right part.
const agentNextPrefix = "next needing you: "

// agentBandInput is what the Agent's band sums up, as plain facts: each
// field empty (or zero) is a piece left out.
type agentBandInput struct {
	Phase string // the phase's glyph and word, from stateLook
	Task  string // the linked build task's id
	Diff  string // "+a −r"
	Loop  int    // the conductor's loop count; 0 says nothing
	Host  string // the host's glyph and word, from stateLook
	Next  string // what `]` goes to next, across every project (needLabel)
}

// agentBandFacts turns the session the Agent shows into the band's facts.
// found false (the session left the list under an open Agent) leaves its
// pieces out rather than showing a zero session's "unknown" phase. task is
// the id of the build task linked to the session (linkedTaskFor), "" for
// none, and next the band's right part (nextNeedingYouLabel), "" for none.
//
// The diff is left out where the overview would say something other than a
// count (a one-off has no base, a stale status zeroed its numbers, a missing
// base or a git error is an error, not a diff) and when it is +0 −0: the
// band is one row and an empty diff says nothing the owner can act on.
func agentBandFacts(s Session, found bool, task, next string) agentBandInput {
	in := agentBandInput{Next: next}
	if !found {
		return in
	}
	ph := stateLook(stateSession, sessionPhaseKey(s))
	in.Phase = strings.TrimSpace(ph.glyph + " " + ph.word)
	in.Task = task
	d := s.DiffStats
	if !s.Scratch && !s.StatusStale && s.BaseBranch != "" && !d.BaseMissing && d.Err == "" &&
		(d.Added > 0 || d.Removed > 0) {
		in.Diff = fmt.Sprintf("+%d −%d", d.Added, d.Removed)
	}
	if !s.Scratch && !s.StatusStale {
		in.Loop = s.LoopCount
	}
	if !s.Pending {
		hs := hostStoppedState
		if s.HostRunning {
			hs = hostRunningState
		}
		h := stateLook(stateSession, hs)
		in.Host = h.glyph + " " + h.word
	}
	return in
}

// agentBandText lays the band's pieces out in width cells: the left part
// (phase, task, diff, loop, host, in that order) at the start and
// "next needing you: <item>" at the end. When they do not all fit, pieces
// drop from the right of the left part; next needing you goes last, only
// when it does not fit even alone, and then the left part gets back the room
// it gave up for it. The band never wraps or cuts a piece in half: a cut
// word would read as a different phase or session.
func agentBandText(in agentBandInput, width int) string {
	var left []string
	for _, p := range []string{in.Phase, in.Task, in.Diff, agentLoopPiece(in.Loop), in.Host} {
		if p != "" {
			left = append(left, p)
		}
	}
	right := ""
	if in.Next != "" {
		right = agentNextPrefix + in.Next
	}
	fits := func(l []string, r string) bool {
		w := lipgloss.Width(strings.Join(l, agentBandSep))
		if r != "" {
			if len(l) > 0 {
				w++
			}
			w += lipgloss.Width(r)
		}
		return w <= width
	}
	keep := len(left)
	for keep > 0 && !fits(left[:keep], right) {
		keep--
	}
	if !fits(nil, right) {
		// next needing you does not fit even alone, so it goes, and the
		// left part is fitted again without it: pieces that had made room
		// for it come back rather than leaving an empty band.
		right = ""
		keep = len(left)
		for keep > 0 && !fits(left[:keep], right) {
			keep--
		}
	}
	if width <= 0 {
		return ""
	}
	return spread(strings.Join(left[:keep], agentBandSep), right, width)
}

// agentLoopPiece is the band's loop piece: "loop n", nothing before the
// first loop.
func agentLoopPiece(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("loop %d", n)
}

// agentChip is one chip of the Agent's chips row: the key's action (named
// through the key table) or, for the leader's pass-through, which has no
// row there, a key of its own.
type agentChip struct {
	action modeAction
	key    string
	word   string
}

// agentChipList is §4.3's chips, `ctrl+] ]` second after back. Every chip
// with an action names its key through keyName at the Agent level, so it
// carries the configured leader. The last is the leader typed twice, which
// the leader passes through to the agent as itself (leader.go).
func agentChipList(leader string) []agentChip {
	return []agentChip{
		{action: actionGoBack, word: "back"},
		{action: actionNeedNext, word: "next needing you"},
		{action: actionDetails, word: "details"},
		{action: actionMouseToggle, word: "mouse"},
		{key: leader + " " + leader, word: "send " + leader},
	}
}

// agentChips draws the chips in width cells, projectChips' way: a chip that
// does not fit whole is left out with the ones after it.
func agentChips(leader string, width int) string {
	var out string
	for _, c := range agentChipList(leader) {
		k := c.key
		if c.action != actionNone {
			k = keyName(leader, levelAgent, c.action)
		}
		chip := theme.keyChip(k, c.word)
		next := chip
		if out != "" {
			next = out + allProjectsGap + chip
		}
		if lipgloss.Width(next) > width {
			break
		}
		out = next
	}
	return out
}

// agentBand is the band row for the session called name, contentW cells
// wide (the body's width; the band's fill reaches the border through the
// pane's padding, as the Project's band does).
func (m Model) agentBand(name string, contentW int) string {
	var s Session
	found := false
	for _, cand := range m.sessions {
		if cand.Name == name && name != "" {
			s, found = cand, true
			break
		}
	}
	task := ""
	if i, ok := linkedTaskFor(name, m.build.Rows); ok {
		task = m.build.Rows[i].ID
	}
	next := nextNeedingYouLabel(m.needsYouNow(), m.needsYouActivePath(), name)
	return theme.band(agentBandText(agentBandFacts(s, found, task, next), max(contentW-2, 0)), contentW)
}

// agentBody is the Agent box's body under the strip: the band, the agent's
// terminal (screen) cut or padded to exactly its rows, and the chips, as
// agentBoxRows shares them. The padding is what keeps the chips on the last
// inner row whatever the terminal drew.
func (m Model) agentBody(name, screen string, contentW, outerH int) string {
	r := agentBoxRows(outerH)
	var lines []string
	if r.band > 0 {
		lines = append(lines, m.agentBand(name, contentW))
	}
	sl := strings.Split(screen, "\n")
	if screen == "" {
		sl = nil
	}
	if len(sl) > r.screen {
		sl = sl[:r.screen]
	}
	for len(sl) < r.screen {
		sl = append(sl, "")
	}
	lines = append(lines, sl...)
	if r.chips > 0 {
		lines = append(lines, agentChips(m.leaderKey.name, contentW))
	}
	return strings.Join(lines, "\n")
}
