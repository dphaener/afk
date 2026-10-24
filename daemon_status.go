package main

import (
	"context"
	"io"
	"reflect"

	"github.com/dphaener/afk/internal/afkstatus"
	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/progstatus"
	"github.com/dphaener/afk/internal/projectrows"
)

// daemonStatusReporter tells the terminal a foreground `afk daemon` runs in
// what afk needs from the owner, as Program status records (OSC 7501). Rex
// shows the records on the tab of the terminal that sent them, so the owner
// sees a blocked build without opening the page. Under systemd there is no
// terminal, so the caller builds no reporter and nothing is sent.
//
// It sends only what changed since the last pass, so a quiet daemon writes
// nothing, and clear removes everything it sent when the daemon stops.
type daemonStatusReporter struct {
	rows func() []projectrows.Row
	out  io.Writer
	last []progstatus.Record
	sent bool
}

// check is the once-per-pass hook (daemon.Deps.PlanWaits). The context is
// unused: building rows reads files and never waits on anything.
func (s *daemonStatusReporter) check(context.Context) {
	next := afkstatus.Records(s.rows())
	if reflect.DeepEqual(next, s.last) {
		return
	}
	if b := afkstatus.Diff(s.last, next); b != nil {
		_, _ = s.out.Write(b)
		s.sent = true
	}
	s.last = next
}

// clear removes every record this reporter sent. With nothing ever sent it
// writes nothing, so a daemon that never reported leaves the terminal alone.
func (s *daemonStatusReporter) clear() {
	if !s.sent {
		return
	}
	_, _ = s.out.Write(afkstatus.ClearAll())
	s.sent = false
	s.last = nil
}

// attachDaemonStatus hooks a reporter into deps when the daemon has a
// terminal to report to, and returns the function that clears it on the way
// out (a no-op otherwise). Kept apart from runDaemon so the "terminal or
// not" decision is testable without running the loop.
func attachDaemonStatus(env daemonEnv, deps *daemon.Deps, projects func() []daemon.Project, stateDir string) func() {
	if env.statusOut == nil {
		return func() {}
	}
	out := env.statusOut()
	if out == nil {
		return func() {}
	}
	r := &daemonStatusReporter{rows: webProjectRows(projects, stateDir, env.now), out: out}
	if deps.PlanWaits != nil {
		deps.PlanWaits = composePlanHooks(deps.PlanWaits, r.check)
	} else {
		deps.PlanWaits = r.check
	}
	return r.clear
}
