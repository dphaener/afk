// usagerefresh.go is the dashboard's "refresh usage" (T7.13): the leader row
// (ctrl+] R) that takes a fresh usage reading with the same effect as
// `afk usage --refresh`, the "Reading usage…" notice while it runs, and the
// notice its result becomes.
//
// Nothing here runs the agent's CLI or reads usage.json. The reading is
// Deps.RefreshUsage, which `afk dash` wires to the code behind
// `afk usage --refresh`, so the dashboard and the command probe, decide and
// save in one way and refuse in the same words. This package cannot import
// the command package, which is why the seam exists and why
// UsageRefreshResult is declared here and filled in there.
//
// The header is not told the new reading by the result: the result carries
// only an outcome and a sentence. It re-reads usage.json through LoadBuild
// (buildCmd), the one place the header's reading comes from, so the header
// cannot show a reading that was not saved, and a failed reading, which
// leaves the file as it was, leaves the old reading on screen.
package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// UsageRefreshOutcome is what one usage reading did, as the dashboard picks
// how loudly to say it.
type UsageRefreshOutcome string

// The outcomes the seam reports. Strings, on TaskStatusResult's pattern, so
// the root package's adapter and a failing test both read plainly.
const (
	// UsageRefreshed: a reading was taken and saved to usage.json.
	UsageRefreshed UsageRefreshOutcome = "refreshed"
	// UsageRefreshUnsupported: the agent cannot report its usage at all, so
	// nothing was read or written. A warning, not an error: nothing broke,
	// and pressing again will not change it.
	UsageRefreshUnsupported UsageRefreshOutcome = "unsupported"
	// UsageRefreshFailed: the CLI was missing, the reading failed, or the
	// reading could not be saved. usage.json is as it was.
	UsageRefreshFailed UsageRefreshOutcome = "failed"
)

// UsageRefreshResult is what one usage reading did, as the dashboard shows
// it. The root package words Text as a plain sentence, without the
// "afk usage: " prefix the command puts before its errors and without the
// agent's name; the dashboard only picks how loudly to say it from Outcome.
type UsageRefreshResult struct {
	Outcome UsageRefreshOutcome
	// Text is the plain-language notice, shown as it is.
	Text string
}

// The action's own words. Constants because the tests quote them.
const (
	// usageRefreshNoSeam is the row's reason when the seam is not wired.
	usageRefreshNoSeam = "this dashboard cannot read usage"
	// usageRefreshBusy is a second press while a reading is still running:
	// one reading at a time, so two runs of the agent's CLI cannot race to
	// write usage.json, and a held key does not start a queue of them.
	usageRefreshBusy = "A usage reading is already running."
	// usageReadingNotice is said as the reading starts. It can take up to a
	// minute, and the header does not change until it lands, so without it
	// the press would look like it did nothing.
	usageReadingNotice = "Reading usage…"
	// usageRefreshNoAnswer stands in for an empty result text, so the owner
	// is never left without word of what the reading did.
	usageRefreshNoAnswer = "Could not tell whether usage was read. Run afk usage to see the last reading."
)

// usageRefreshBlockedReason is the row's reason, and the action asks it again
// when it runs, so the row and the action cannot disagree.
//
// Only the seam dims it. It needs no project, no build and no selection: the
// reading is the machine's, and the header shows it wherever a build is
// read. Over the all-projects screen leaderBlockedReason's own rule decides
// before this is asked, and dims it with the screen's reason.
func usageRefreshBlockedReason(c leaderConds) string {
	if !c.canRefreshUsage {
		return usageRefreshNoSeam
	}
	return ""
}

// refreshUsage is ctrl+] R. It calls nothing itself: the seam runs in the
// command it returns, so Update never waits on the agent's CLI.
//
// The blocked reason comes first, so a dashboard without the seam says that
// rather than that a reading is running. The in-flight check comes next:
// a reading already out is answered with a notice and no command.
func (m Model) refreshUsage() (tea.Model, tea.Cmd) {
	if reason := usageRefreshBlockedReason(m.leaderConditions()); reason != "" {
		m.notify(notifyInfo, reasonSentence(reason))
		return m, nil
	}
	if m.usageRefreshing {
		m.notify(notifyInfo, usageRefreshBusy)
		return m, nil
	}
	m.usageRefreshing = true
	m.notify(notifyInfo, usageReadingNotice)
	return m, refreshUsageCmd(m.deps.RefreshUsage)
}

// applyUsageRefreshed tells one reading's result and reads the build again,
// so the header shows what usage.json now holds without waiting for the
// tick. The in-flight flag is cleared whatever happens, the reading being
// over either way.
//
// Unlike a task status result, it is never dropped for a project switch:
// the reading belongs to the machine, so its notice is as true under the new
// project as under the old. The re-read is buildCmd, which reads the project
// the dashboard is on now, and is tied to it by its own generation check.
func (m Model) applyUsageRefreshed(msg usageRefreshedMsg) (tea.Model, tea.Cmd) {
	m.usageRefreshing = false
	kind, text := usageRefreshNotice(msg.result)
	m.notify(kind, text)
	return m, m.buildCmd()
}

// usageRefreshNotice picks how loudly to say a result: success for a saved
// reading, a warning for an agent that cannot report usage, and an error for
// a reading that failed or could not be saved. An empty text is replaced so
// the owner always hears back, and an unknown outcome is told as
// information, which says it without claiming success or failure.
func usageRefreshNotice(r UsageRefreshResult) (notificationKind, string) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return notifyWarn, usageRefreshNoAnswer
	}
	switch r.Outcome {
	case UsageRefreshed:
		return notifySuccess, text
	case UsageRefreshUnsupported:
		return notifyWarn, text
	case UsageRefreshFailed:
		return notifyError, text
	}
	return notifyInfo, text
}
