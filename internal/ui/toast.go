// toast.go floats a fresh notification in the top-right corner for a few
// seconds. It is now the dashboard's ONLY notification surface: the footer
// used to keep the same text sticky underneath so a toast easy to miss had a
// second, lasting chance to be read, but that safety net is gone (views.go's
// renderFooter is help text alone). Outcome detail — a clean's archive path,
// a branch-recovery command — is ephemeral by design now: it is visible for
// toastTTL and then gone, rediscoverable afterwards only the slow way (list
// the archive directory, read `git reflog`), which the plan judged an
// acceptable trade against a second copy of every message.
//
// The kind recorded on the notification (notification.go) drives which of
// styleSet's toastKinds renders it, so an error is distinguishable from a
// success at a glance without reading the words.
//
// Freshness is a clock comparison in View against statusChangedAt, which
// notify records whenever the status text changes. No timer Cmd is armed: the
// dashboard re-renders on its discovery tick anyway, so a toast lingers at
// most one interval past toastTTL — and a Cmd here would have turned every
// status write into a Cmd-returning call, which dozens of tests pin as nil.
package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// toastTTL is how long a status change stays lifted into the corner.
const toastTTL = 4 * time.Second

// toastMaxWidth caps the toast's content so a long outcome wraps into a small
// box rather than a banner across the whole frame. 48 keeps the box under
// two-thirds of an 80-column terminal, so the pane's left edge stays readable
// beside it.
const toastMaxWidth = 48

// toastVisible reports whether the current status is fresh enough to float.
// Empty statuses never toast: the line went quiet, and nothing needs saying.
func (m Model) toastVisible() bool {
	if m.status == "" || m.statusChangedAt.IsZero() {
		return false
	}
	return m.deps.Now().Sub(m.statusChangedAt) < toastTTL
}

// renderToast draws the status text in a small bordered box, styled per kind
// (styleSet.toastKinds), wrapped to the toast width and clipped to a few
// lines so a wall of text cannot cover the detail pane it is floating over.
func renderToast(kind notificationKind, text string, termW int) string {
	width := min(toastMaxWidth, termW-2*modalMargin-4)
	if width < 10 {
		width = 10
	}
	st := theme.toastStyle(kind)
	// The marker leads the wrapped text itself, not just the rendered
	// output, so it counts against the wrap width and the clip like any
	// other character rather than floating outside the box's measured size.
	msg := text
	if st.marker != "" {
		msg = st.marker + " " + text
	}
	body := lipgloss.Wrap(msg, width, "")
	lines := strings.Split(body, "\n")
	if len(lines) > 3 {
		lines = append(lines[:2], truncate(lines[2], width))
	}
	content := st.text.Render(strings.Join(lines, "\n"))
	// A toast is sized to its text, not to the maximum, so a short message
	// makes a short box.
	w := 0
	for _, ln := range lines {
		w = max(w, lipgloss.Width(ln))
	}
	return sizedBox(st.box, w+2, len(lines)).Render(content)
}

// composeToast places the toast in the frame's top-right corner, one row
// under the header and one cell in from the edge, above everything else.
func composeToast(frame, toast string, width int) string {
	x := max(0, width-lipgloss.Width(toast)-1)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(frame),
		lipgloss.NewLayer(toast).X(x).Y(1).Z(2),
	).Render()
}

// windowTitle is the terminal title the frame declares: the tool, the active
// project when several are known, and the awaiting count when it is not zero
// — the one number a person switching tabs wants without switching.
func (m Model) windowTitle() string {
	title := "afk"
	if idx := m.activeProject; len(m.projects) > 1 && idx >= 0 && idx < len(m.projects) {
		title += " · " + m.projects[idx].Name
	}
	if n := m.awaitingCount(); n > 0 {
		title += " · " + itoa(n) + " awaiting input"
	}
	return title
}

// terminalProgress is the frame's OSC 9;4 progress bar: the fleet's task
// completion across every planned session that is still working, or nil (no
// bar) once nothing is. Terminals that draw it (Ghostty, iTerm2, Windows
// Terminal) show it on the tab; the rest ignore the sequence. It is nil rather
// than 100% when everything is done, so a finished dashboard does not leave a
// full bar nagging on the tab.
func (m Model) terminalProgress() *tea.ProgressBar {
	var complete, total int
	active := false
	for _, s := range m.sessions {
		if s.Scratch || s.Pending || s.StatusStale || isWorktreeFault(s.Kind) {
			continue
		}
		complete += s.Progress.Complete
		total += s.Progress.Total
		if !s.Phase.Terminal() {
			active = true
		}
	}
	if !active || total == 0 {
		return nil
	}
	return tea.NewProgressBar(tea.ProgressBarDefault, complete*100/total)
}

// itoa avoids pulling strconv in for one small integer.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
