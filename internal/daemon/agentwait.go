package daemon

import (
	"time"

	"github.com/dphaener/afk/internal/agentreport"
)

// agentWaitSince is the one place the stall rule (PLAN §5.3, rule h in
// decideRunning) learns when a session started waiting on the owner, and
// whether it is waiting at all. There are two signals and either is enough:
//
//   - The hook: status.json says awaiting_input. Its wait is measured from
//     hookRef, the last sign of life (lastActivity), exactly as it was before
//     reports existed, so a session with no report decides as it always did.
//   - The report: the host.json `agent_status` the host republished is
//     `blocked` and fresh (agentreport.Blocked and agentreport.Fresh against
//     the host's start and its last exit). Its wait is measured from the
//     report's own time.
//
// The report's time is the start, and not the status write, because the
// report is what the host saw first: Claude Code sends `blocked` the moment
// it shows its question or permission prompt, before any hook has written
// awaiting_input (docs/PLAN.md §8 T17.1). When both signals hold, the earlier
// start wins, which is the "whichever came first" of §8 T17.12.
//
// floor is the last thing the loop or the owner did to the session: the
// attempt's start, its last restart, its last wake and the owner's last
// answer (lastActivity without the status write). The report's start never
// goes below it. A report keeps its time for as long as the agent stays
// blocked, so without the floor a session woken from a report-only stall
// would be reported on the very next tick, not given a whole StallAfter to
// act on the line it was typed, and an answer the owner gave would not
// restart the clock. Those are the two reasons lastActivity counts a wake and
// an answer for the hook, and they hold for the report the same way. The
// status write is left out of the floor on purpose: the hook's own write of
// awaiting_input is a status write, and counting it would make the report
// never the earlier of the two.
//
// It reads nothing and writes nothing. In particular a report never writes
// status.json; the hooks stay the authority for awaiting_input.
func agentWaitSince(s *SessionView, hookRef, floor time.Time) (since time.Time, ok bool) {
	if s.AwaitingInput {
		since, ok = hookRef, true
	}
	if agentreport.Blocked(s.Report) && agentreport.Fresh(s.Report, s.HostStartedAt, s.LastExitAt) {
		at := latest(s.Report.At, floor)
		if !ok || at.Before(since) {
			since, ok = at, true
		}
	}
	return since, ok
}
