package main

import (
	"io"

	"github.com/dphaener/afk/internal/ui"
)

// dashRefreshUsage is the dashboard's Deps.RefreshUsage: one fresh usage
// reading with the real dependencies, the same as `afk usage --refresh`.
// It probes the agent's command-line tool (a second or two, up to a minute),
// so the dashboard only ever calls it inside a command.
func dashRefreshUsage() ui.UsageRefreshResult {
	return dashRefreshUsageWith(realUsageDeps())
}

// dashRefreshUsageWith is dashRefreshUsage over injected dependencies, so a
// test never reaches a real agent or the real state folder.
//
// It looks up the default agent exactly as `afk usage` does without
// --provider, and goes through refreshUsageForSurface, the entry point the
// command prints from, so the dashboard and the command write the same file.
// The config warning usageGovernorConfig prints is dropped: the dashboard has
// no stderr to show it on, and the defaults it falls back to are the same ones
// the command would use.
//
// A failed save maps to failed, because the owner's next step is the same
// either way: the header still shows the old reading.
func dashRefreshUsageWith(d usageDeps) ui.UsageRefreshResult {
	res := refreshDefaultAgentUsage(d)
	switch res.Outcome {
	case usageRefreshed:
		return ui.UsageRefreshResult{Outcome: ui.UsageRefreshed, Text: res.Text}
	case usageRefreshUnsupported:
		return ui.UsageRefreshResult{Outcome: ui.UsageRefreshUnsupported, Text: res.Text}
	default:
		return ui.UsageRefreshResult{Outcome: ui.UsageRefreshFailed, Text: res.Text}
	}
}

// usageNoAgentText is what a surface says when no default agent can be found.
const usageNoAgentText = "No agent is set up to read usage from. Run afk usage in a terminal to see why."

// refreshDefaultAgentUsage is the step the dashboard and the web page share:
// look up the default agent, read the governor limits, take one reading. They
// share it so the two surfaces cannot drift apart on which agent they read or
// which file they write. A missing agent comes back as a failed result with
// its own sentence, since neither surface has anything else to do with it.
func refreshDefaultAgentUsage(d usageDeps) usageRefreshResult {
	p, err := d.Lookup("")
	if err != nil {
		return usageRefreshResult{Outcome: usageRefreshFailed, Err: err, Text: usageNoAgentText}
	}
	gov := usageGovernorConfig(io.Discard, d)
	return refreshUsageForSurface(p, gov, d)
}
