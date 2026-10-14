package main

import (
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// webRefreshUsage is the web page's Deps.RefreshUsage: one fresh usage
// reading with the real dependencies, the same as `afk usage --refresh` and
// the dashboard's refresh. It probes the agent's command-line tool (a second
// or two, up to a minute); the web package runs it in the background and lets
// only one run at a time, because the entry point has no guard of its own.
//
// st is the state folder the daemon was started with. The reading is written
// there, not to the default folder, so the page, the daemon loop and the live
// reader all see the same usage.json.
func webRefreshUsage(st usagestate.Deps) func() web.UsageRefreshResult {
	d := realUsageDeps()
	d.State = st
	d.Run = realUsageRunner(st)
	return func() web.UsageRefreshResult { return webRefreshUsageWith(d) }
}

// webRefreshUsageWith is the reading over injected dependencies, so a test
// never reaches a real agent or the real state folder.
//
// It goes through the step the dashboard uses, so the same agent is read and
// the same file is written. A failed save maps to failed, as on the dashboard:
// the owner's next step is the same either way, and the old reading stays.
func webRefreshUsageWith(d usageDeps) web.UsageRefreshResult {
	res := refreshDefaultAgentUsage(d)
	switch res.Outcome {
	case usageRefreshed:
		return web.UsageRefreshResult{Outcome: web.UsageRefreshed, Text: res.Text}
	case usageRefreshUnsupported:
		return web.UsageRefreshResult{Outcome: web.UsageRefreshUnsupported, Text: res.Text}
	default:
		return web.UsageRefreshResult{Outcome: web.UsageRefreshFailed, Text: res.Text}
	}
}
