package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/ledger"
)

// This file is the Build section's content (T16.4; it was the overview's
// Build tab before): the master plan's tasks with their ledger status, under
// a short summary of where the build stands. It draws from the snapshot the
// model already holds (builddata.go) and reads nothing itself.
//
// The section is about the project, not the selected session, so its lines
// are a pure function of the snapshot and two marks: the cursor's row
// (buildcursor.go), drawn selected while the section has the keyboard, and
// the selected session's task, drawn as a linked row ending "◂ name"
// (§3.5). It writes nothing. Starting the build is not done from the
// dashboard at all. Pausing and resuming are, by the `p` key, but not from
// this file: see buildpause.go.

// buildColumnGap is the space between two columns of a task row.
const buildColumnGap = 2

// buildIDMaxWidth caps the id column. Task ids are a few cells ("T3.2"); the
// cap only stops one malformed id from taking the title's room on every row.
const buildIDMaxWidth = 12

// hasBuild reports whether the project has a build to show: a snapshot has
// arrived since the project became active, and it found a master plan or a
// ledger. It is the gate for the pause key and for the Build section's rows.
//
// It is false until the first read lands and again after a project switch
// (forgetBuild), which is what keeps the outgoing project's tasks off the
// screen under the incoming project's name.
func (m Model) hasBuild() bool {
	return m.buildKnown && m.build.HasBuild
}

// buildMarks is what the Build section marks on its rows: the cursor's row
// (cursor, -1 for none), the selected session's task (linked, -1 for none,
// with linkedTo the session's name) and the short token total the cursor's
// row ends with when it has room (total, "" for none).
type buildMarks struct {
	cursor   int
	linked   int
	linkedTo string
	total    string
}

// noBuildMarks draws the rows with nothing marked.
var noBuildMarks = buildMarks{cursor: -1, linked: -1}

// buildTabLines is the Build section's content with nothing marked: the
// summary block, a blank line, then one line per master-plan task. It is the
// layout's pure core, pinned by the table tests without a Model.
func buildTabLines(leader string, snap buildview.Snapshot, plan PlanStatus, width int) []string {
	return buildSectionLines(leader, snap, plan, width, noBuildMarks)
}

// buildRowsTop is how many lines of buildSectionLines come before the first
// task row: the summary block and the blank line under it. It is computed
// from buildSummaryLines, the function that writes those lines, so the
// section's count of where the cursor's row is (buildSectionWindow) cannot
// drift from what is drawn.
func buildRowsTop(leader string, snap buildview.Snapshot, plan PlanStatus, width int) int {
	return len(buildSummaryLines(leader, snap, plan, max(width, 1))) + 1
}

// buildSectionLines is the Build section's content at the given width with
// marks: the summary block, a blank line, then one line per task. Every line
// is exactly width cells or less, one per task, so the section's windowing
// (buildSectionWindow) can count rows as lines.
func buildSectionLines(leader string, snap buildview.Snapshot, plan PlanStatus, width int, marks buildMarks) []string {
	if width < 1 {
		width = 1
	}
	lines := buildSummaryLines(leader, snap, plan, width)
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
	return append(lines, buildSectionRowLines(snap.Rows, width, marks)...)
}

// buildRowGutter is the cells the cursor's gutter takes on the left of every
// row: the marker and a space. Every row has it, marked or not, so the
// columns do not shift sideways as the cursor moves or the section gains and
// loses the keyboard.
const buildRowGutter = 2

// buildCursorMarker is the cursor's glyph. It is the chevron and not the
// session list's rail (cursorRail) so the two cursors read apart when both
// sections are on screen; the selection fill (§3.5) is what says which one
// has the keyboard.
const buildCursorMarker = "›"

// buildSectionRowLines lays the task rows out behind the gutter, with the
// cursor's row drawn selected and the linked row drawn linked (theme.row:
// selected outranks linked, so the linked task under the cursor reads as the
// selection).
//
// The rows are laid out at width minus the gutter by buildRowLayout itself,
// so the rule it keeps (the detail goes first, then the title is cut, the id
// and status always show) holds exactly as it does with nothing marked. A
// linked row ends in "◂ name"; spread gives that the room it needs and cuts
// the row's text first, because the link is what the row is marked for.
//
// total, when not empty, is the cursor's task's short token total (T7.12). It
// follows the row's content after the usual column gap, and only when the row
// shows everything it has (its whole id, title and detail) and the total
// still fits beside them. So the total is the first thing a narrowing section
// gives up, before the detail and the title.
func buildSectionRowLines(rows []buildview.Row, width int, marks buildMarks) []string {
	innerW := max(width-buildRowGutter, 1)
	inner, whole := buildRowLayout(rows, innerW)
	out := make([]string, len(inner))
	for i, ln := range inner {
		selected := i == marks.cursor
		gutter := strings.Repeat(" ", buildRowGutter)
		if selected {
			gutter = buildCursorMarker + " "
			if marks.total != "" && whole[i] {
				if withTotal := ln + strings.Repeat(" ", buildColumnGap) + theme.dim.Render(marks.total); lipgloss.Width(withTotal) <= innerW {
					ln = withTotal
				}
			}
		}
		linkedTo := ""
		if i == marks.linked {
			linkedTo = marks.linkedTo
		}
		out[i] = theme.row(gutter+ln, width, selected, linkedTo)
	}
	return out
}

// buildSummaryLines is the block above the Build section's task rows. Each line is a separate
// fact, and a line is left out when its fact does not hold, so a healthy
// build with an empty inbox is one line. leader is the leader key the inbox
// line names (the Model's, which config.toml sets).
//
// The problem sentence is wrapped, not cut, and it replaces the count: it
// holds an absolute path, the path is what the owner needs to go and fix the
// file, and with a problem the loader has no count to give.
func buildSummaryLines(leader string, snap buildview.Snapshot, plan PlanStatus, width int) []string {
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
	// The plan line follows the first line. It is left out with no planning
	// session, so a project that never planned through afk shows nothing new.
	if plan.Known && (plan.Session || plan.Err != "") {
		lines = append(lines, theme.dim.Render(truncate("plan: "+planStatusLine(plan), width)))
	}
	if snap.Paused {
		lines = append(lines, theme.awaitingFlag.Render(truncate("paused: p or afk resume starts it again", width)))
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
		text := fmt.Sprintf("%d items need you · %s: inbox", blocking, keyName(leader, levelProject, actionInbox))
		if blocking == 1 {
			text = "1 item needs you · " + keyName(leader, levelProject, actionInbox) + ": inbox"
		}
		lines = append(lines, theme.awaitingFlag.Render(truncate(text, width)))
	} else if open > 0 {
		text := fmt.Sprintf("%d open items · %s: inbox", open, keyName(leader, levelProject, actionInbox))
		if open == 1 {
			text = "1 open item · " + keyName(leader, levelProject, actionInbox) + ": inbox"
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
	lines, _ := buildRowLayout(rows, width)
	return lines
}

// buildRowLayout is buildRowLines that also says, row by row, whether the row
// shows everything it has: its id uncut, its title uncut and its detail when
// it has one. The cursor's row adds its token total only to a whole row
// (buildSectionRowLines), which is what makes the total go before the detail
// and the title as the section narrows. The answer comes from the same branch
// that lays the row out, so it cannot disagree with what is drawn.
func buildRowLayout(rows []buildview.Row, width int) ([]string, []bool) {
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
	whole := make([]bool, 0, len(rows))
	for _, r := range rows {
		id := truncate(r.ID, idW)
		complete := false
		line := theme.sessionName.Render(id) + strings.Repeat(" ", idW-lipgloss.Width(id)) + gap +
			buildStatusStyle(r.Status).Render(r.Status) + strings.Repeat(" ", statusW-lipgloss.Width(r.Status))
		room := width - lead
		switch {
		case room < 1:
			// No room for a title at all: the id and the status are the row.
		case aligned && r.Detail != "":
			line += gap + r.Title + strings.Repeat(" ", titleW-lipgloss.Width(r.Title)) +
				gap + theme.dim.Render(r.Detail)
			complete = true
		case r.Detail != "" && lipgloss.Width(r.Title)+buildColumnGap+lipgloss.Width(r.Detail) <= room:
			line += gap + r.Title + gap + theme.dim.Render(r.Detail)
			complete = true
		default:
			line += gap + truncate(r.Title, room)
			complete = r.Detail == "" && lipgloss.Width(r.Title) <= room
		}
		// A last guard for a width smaller than the id and status together.
		out = append(out, truncate(line, width))
		whole = append(whole, complete && lipgloss.Width(r.ID) <= idW && lipgloss.Width(line) <= width)
	}
	return out, whole
}

// buildStatusStyle colours a ledger status with styles the theme already has:
// done is dim (finished work is the least urgent thing in the section), running
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
