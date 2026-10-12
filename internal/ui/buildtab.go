package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/ledger"
)

// This file is the Build tab: the master plan's tasks with their ledger
// status, under a short summary of where the build stands. It draws from the
// snapshot the model already holds (builddata.go) and reads nothing itself.
//
// The tab is about the project, not the selected session, so nothing here
// takes a Session: it reads the same on every row and with no row at all.
// It only shows. Starting, pausing or resuming the build, and writing the
// ledger, are not done from here.

// noBuildNotice is what `b` says in a project that has no build to show. The
// key stays listed in `?` and `:` in that state because it explains itself:
// the notice names the one setting that makes the tab appear.
const noBuildNotice = "This project has no master plan, so there is no build to show. Set plan in afk.toml."

// buildColumnGap is the space between two columns of a task row.
const buildColumnGap = 2

// buildIDMaxWidth caps the id column. Task ids are a few cells ("T3.2"); the
// cap only stops one malformed id from taking the title's room on every row.
const buildIDMaxWidth = 12

// hasBuild reports whether the project has a build to show: a snapshot has
// arrived since the project became active, and it found a master plan or a
// ledger. It is the one gate for the Build tab's place in the strip and for
// the `b` key, so the two cannot disagree.
//
// It is false until the first read lands and again after a project switch
// (forgetBuild), which is what keeps the outgoing project's tasks off the
// screen under the incoming project's name.
func (m Model) hasBuild() bool {
	return m.buildKnown && m.build.HasBuild
}

// buildPanel is the Build tab's one panel. It ignores ctx.Session on purpose.
type buildPanel struct{}

func (buildPanel) Title() string { return "Build" }

func (buildPanel) Render(ctx panelContext, width int) []string {
	return buildTabLines(ctx.Build, width)
}

// buildTabLines is the Build tab's content at the given width: the summary
// block, a blank line, then one line per master-plan task. Every line is at
// most width cells, so the detail pane's soft wrap never folds one and the
// row count stays one per task.
//
// It is a pure function of the snapshot so the whole layout is table-testable
// without a Model.
func buildTabLines(snap buildview.Snapshot, width int) []string {
	if width < 1 {
		width = 1
	}
	lines := buildSummaryLines(snap, width)
	if snap.Problem != "" {
		// The loader leaves the rows empty when the plan or the ledger cannot
		// be read. Saying "no tasks" under the reason would be a second,
		// wrong, explanation.
		return lines
	}
	if len(snap.Rows) == 0 {
		return append(lines, "", theme.dim.Render(truncate("The master plan has no tasks.", width)))
	}
	lines = append(lines, "")
	return append(lines, buildRowLines(snap.Rows, width)...)
}

// buildSummaryLines is the block above the task rows. Each line is a separate
// fact, and a line is left out when its fact does not hold, so a healthy
// build with an empty inbox is one line.
//
// The problem sentence is wrapped, not cut, and it replaces the count: it
// holds an absolute path, the path is what the owner needs to go and fix the
// file, and with a problem the loader has no count to give.
func buildSummaryLines(snap buildview.Snapshot, width int) []string {
	var lines []string
	if snap.Problem != "" {
		wrapped := lipgloss.Wrap(snap.Problem, width, "")
		for _, ln := range strings.Split(wrapped, "\n") {
			lines = append(lines, theme.errorText.Render(truncate(ln, width)))
		}
	} else {
		head := fmt.Sprintf("%d/%d done", snap.Done, snap.Total)
		if snap.Current != "" {
			head += " · " + snap.Current
			if snap.CurrentPhase != "" {
				head += ": " + snap.CurrentPhase
			}
		}
		lines = append(lines, truncate(head, width))
	}
	if snap.Paused {
		lines = append(lines, theme.awaitingFlag.Render(truncate("paused: afk resume starts it again", width)))
	}
	if snap.Stopped {
		stop := "stopped"
		switch {
		case snap.StopTask != "" && snap.StopReason != "":
			stop += ": " + snap.StopTask + " — " + snap.StopReason
		case snap.StopTask != "":
			stop += ": " + snap.StopTask
		case snap.StopReason != "":
			stop += ": " + snap.StopReason
		}
		lines = append(lines, theme.errorText.Render(truncate(stop, width)))
	}
	if open, blocking := buildview.OpenCounts(snap.Items); blocking > 0 {
		text := fmt.Sprintf("%d items need you · i: inbox", blocking)
		if blocking == 1 {
			text = "1 item needs you · i: inbox"
		}
		lines = append(lines, theme.awaitingFlag.Render(truncate(text, width)))
	} else if open > 0 {
		text := fmt.Sprintf("%d open items · i: inbox", open)
		if open == 1 {
			text = "1 open item · i: inbox"
		}
		lines = append(lines, truncate(text, width))
	}
	if snap.Unreadable > 0 {
		text := fmt.Sprintf("%d inbox files could not be read", snap.Unreadable)
		if snap.Unreadable == 1 {
			text = "1 inbox file could not be read"
		}
		lines = append(lines, theme.errorText.Render(truncate(text, width)))
	}
	return lines
}

// buildRowLines lays the task rows out as columns: id, status, title, detail.
//
// The id and the status always show, padded so they line up down the list.
// When every row fits at its full width the title is padded too and the
// details form a fourth column. When they do not all fit, each row decides
// for itself, in the order the plan asks for: the detail goes first, and only
// then is the title cut. A row therefore never shows a cut title beside a
// detail. Deciding per row and not for the whole table is deliberate: one
// long title would otherwise remove the detail from every row, and the detail
// is where "waiting on you" and the pull request number are.
func buildRowLines(rows []buildview.Row, width int) []string {
	idW, statusW, titleW, detailW := 0, 0, 0, 0
	for _, r := range rows {
		idW = max(idW, lipgloss.Width(r.ID))
		statusW = max(statusW, lipgloss.Width(r.Status))
		titleW = max(titleW, lipgloss.Width(r.Title))
		detailW = max(detailW, lipgloss.Width(r.Detail))
	}
	idW = min(idW, buildIDMaxWidth)
	lead := idW + buildColumnGap + statusW + buildColumnGap
	gap := strings.Repeat(" ", buildColumnGap)
	aligned := detailW == 0 || lead+titleW+buildColumnGap+detailW <= width

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		id := truncate(r.ID, idW)
		line := theme.sessionName.Render(id) + strings.Repeat(" ", idW-lipgloss.Width(id)) + gap +
			buildStatusStyle(r.Status).Render(r.Status) + strings.Repeat(" ", statusW-lipgloss.Width(r.Status))
		room := width - lead
		switch {
		case room < 1:
			// No room for a title at all: the id and the status are the row.
		case aligned && r.Detail != "":
			line += gap + r.Title + strings.Repeat(" ", titleW-lipgloss.Width(r.Title)) +
				gap + theme.dim.Render(r.Detail)
		case r.Detail != "" && lipgloss.Width(r.Title)+buildColumnGap+lipgloss.Width(r.Detail) <= room:
			line += gap + r.Title + gap + theme.dim.Render(r.Detail)
		default:
			line += gap + truncate(r.Title, room)
		}
		// A last guard for a width smaller than the id and status together.
		out = append(out, truncate(line, width))
	}
	return out
}

// buildStatusStyle colours a ledger status with styles the theme already has:
// done is dim (finished work is the least urgent thing on the tab), running
// and shipping take the session list's live-host colour, parked takes the
// awaiting flag because it waits on the owner, and failed is error text.
// Pending and any word this function does not know are left plain, so a new
// ledger status still shows, in the ledger's own word.
func buildStatusStyle(status string) lipgloss.Style {
	switch ledger.Status(status) {
	case ledger.Done, ledger.Superseded:
		return theme.dim
	case ledger.Running, ledger.Shipping:
		return theme.hostRunning
	case ledger.Parked:
		return theme.awaitingFlag
	case ledger.Failed:
		return theme.errorText
	}
	return lipgloss.NewStyle()
}

// usagePaused reports whether the row should read as paused for usage and not
// as stopped: the build's ledger records a usage pause for this session
// (Session.UsagePaused, stamped from the build snapshot) and its host is not
// running.
//
// The host check matters. `r` still restarts such a row, and a session the
// owner started again is running, whatever the ledger says until the build
// loop's next pass.
func usagePaused(s Session) bool {
	return s.UsagePaused && !s.HostRunning && !s.Pending && !isWorktreeFault(s.Kind)
}

// usagePausedText is the sentence form, for the overview's status line and
// for notices: with the time the daemon means to start the session again when
// the ledger has one.
func usagePausedText(s Session) string {
	if s.UsageWake != "" {
		return "paused for usage until " + s.UsageWake
	}
	return "paused for usage"
}

// usagePausedLabel is the session list's PHASE cell for a paused row. The
// column is nine cells at its widest and five when abbreviated (rowColumns),
// so the sentence does not fit; the overview one line away carries it in
// full.
func usagePausedLabel(abbrev bool) string {
	if abbrev {
		return "PAUSE"
	}
	return "PAUSED"
}

// stoppedText is how a notice describes a row with no live host: "is stopped",
// or "is paused for usage" when that is why.
func stoppedText(s Session) string {
	if usagePaused(s) {
		return "is paused for usage"
	}
	return "is stopped"
}

// stampUsagePauses copies the build snapshot's paused sessions onto the
// session rows and reports whether any row changed.
//
// The fact is copied onto Session, and not looked up at draw time, because
// the session list's rows are drawn by a delegate that holds no Model
// (sessionDelegate's doc, sessionlist.go): whatever a row needs has to ride
// on its item. Both sides call this: a session load (rebuildListItems), since
// fresh rows arrive unstamped, and a build load (applyBuildLoaded), since the
// set of paused sessions changes without the rows changing.
func (m *Model) stampUsagePauses() bool {
	changed := false
	for i := range m.sessions {
		wake, paused := "", false
		if m.buildKnown {
			wake, paused = m.build.PausedSessions[m.sessions[i].Name]
		}
		if m.sessions[i].UsagePaused != paused || m.sessions[i].UsageWake != wake {
			m.sessions[i].UsagePaused = paused
			m.sessions[i].UsageWake = wake
			changed = true
		}
	}
	return changed
}
