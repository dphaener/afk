// keystrip.go draws the key strip (docs/dash-design.md §4.8, T16.2): the
// small pane the leader shows in the Agent, listing the afk keys that work
// there, two to a row. The next key runs one; the leader again sends one
// leader to the agent; esc goes back to Project; any other key closes the
// strip with nothing sent (levelKey, model.go).
//
// The strip is drawn from the key table (keyTable's Agent-level rows), so it
// cannot list a key the Agent does not answer to, nor leave one out.
package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// keyStripWidth is the strip's outer width (§3.8: 54 columns in the
// bottom-right corner). The box is the modal box, content plus one cell of
// padding and one of border on each side, so the content is four less.
const keyStripWidth = 54

// keyStripEntry is one key on the strip: the key as pressed after the leader
// and the word for what it does.
type keyStripEntry struct {
	key, word string
}

// keyStripEntries is the strip's keys in the key table's order: every row
// that has a key at the Agent level, then the leader itself, which sends one
// leader to the agent. The leader is not a table row (decideKey answers it
// before the table), so it is added here, last, where a person looking for
// "how do I type ctrl+] into the agent" will find it.
func keyStripEntries(leader string) []keyStripEntry {
	var out []keyStripEntry
	for _, r := range keyTable {
		if r.key == "" || !r.levels.has(levelAgent) {
			continue
		}
		out = append(out, keyStripEntry{key: r.keyLabel(), word: r.name})
	}
	return append(out, keyStripEntry{key: leader, word: "send " + leader})
}

// renderKeyStrip is the strip's box for a terminal width wide: the entries
// two to a row, each cell the key in the help key's colour and its word.
// Every cell is cut to its half of the content width, so nothing wraps past
// the border on a narrow terminal.
func renderKeyStrip(leader string, width int) string {
	entries := keyStripEntries(leader)
	contentW := min(keyStripWidth, width) - 4
	contentW = max(contentW, 10)
	half := contentW / 2
	keyW := 0
	for _, e := range entries {
		keyW = max(keyW, lipgloss.Width(e.key))
	}
	cell := func(e keyStripEntry) string {
		plain := padRight(e.key, keyW) + " " + e.word
		cut := truncate(plain, half-1)
		if strings.HasPrefix(cut, e.key) {
			return theme.helpStyles.ShortKey.Render(e.key) + strings.TrimPrefix(cut, e.key)
		}
		return cut
	}
	var lines []string
	for i := 0; i < len(entries); i += 2 {
		left := cell(entries[i])
		line := padRight(left, half)
		if i+1 < len(entries) {
			line += cell(entries[i+1])
		}
		lines = append(lines, line)
	}
	return renderModalBox("afk keys", "esc back · other keys close", strings.Join(lines, "\n"), contentW, len(lines))
}

// composeKeyStrip lays the strip over frame in the bottom-right corner, just
// above the status bar. It dims nothing itself: the strip is shown only in
// the Agent, and composeAgent has already dimmed the Project under the agent
// and the agent box by keyStripDim (§3.9: 50%, lighter than an overlay's,
// because the strip is open for a moment and the agent's output under it
// should stay readable). The status bar was left at full strength there: it
// is what says the dashboard is waiting for an afk key.
func composeKeyStrip(frame, box string, width, height int) string {
	x := max(0, width-lipgloss.Width(box))
	y := max(0, height-1-lipgloss.Height(box))
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(frame),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	).Render()
}

// keyStripShown reports whether the key strip has the keyboard, and so is
// drawn: the mode is derived as a key is, so the strip on screen and the
// strip taking keys cannot disagree.
func (m Model) keyStripShown() bool {
	return activeInputMode(m.modeSnapshot()) == inputKeyStrip
}
