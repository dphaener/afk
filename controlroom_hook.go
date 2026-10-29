package main

import (
	"context"
	"time"

	"github.com/dphaener/afk/internal/controlroom"
	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/rex"
	"github.com/dphaener/afk/internal/rexlayout"
	"github.com/dphaener/afk/internal/session"
)

// controlRoomCallTimeout bounds each Rex command the hook runs. The hook
// rides the daemon's pass, so a wedged Rex server must cost a few seconds, not
// the pass.
const controlRoomCallTimeout = 10 * time.Second

// controlRoom keeps the hub's control room open: a Rex session named "afk"
// with one window running `afk dash`, so any Rex client (laptop, phone) can
// reach the dashboard. It runs once per daemon pass, beside the plan-wait
// hook, and so it also runs while paused and at the end of `--once`.
//
// It is per pass and machine-wide on purpose. A closed window has to come back
// without the owner asking, and the pass is the loop that already wakes on a
// timer. The control room belongs to the machine, not to a project, so the
// hook has no project root, writes no decision log entry and no daemon.json,
// and raises nothing to the inbox: a failure goes to the daemon's log only.
// Under backend afk (or a bad [runtime]) the decision says Nothing before any
// Rex fact is read, so an afk-only machine makes no Rex call.
//
// lastStart lives in memory: after a daemon restart the first start is not
// rate limited, which is the right call because the window is probably gone.
type controlRoom struct {
	rex         rex.Caller
	loadRuntime func() (daemonconfig.Config, error)
	now         func() time.Time
	executable  func() (string, error)
	logf        func(string, ...any)
	lastStart   time.Time
}

// realControlRoom builds the hook the daemon runs, on the real Rex command.
func realControlRoom(loadRuntime func() (daemonconfig.Config, error), now func() time.Time, logf func(string, ...any)) *controlRoom {
	return &controlRoom{
		rex:         rex.NewCaller(rex.NewRun(controlRoomCallTimeout), "rex"),
		loadRuntime: loadRuntime,
		now:         now,
		executable:  session.DefaultDeps().Executable,
		logf:        logf,
	}
}

// check is the daemon's once-per-pass hook. It never panics and never
// returns an error: every failure is one log line and the next pass tries
// again.
func (c *controlRoom) check(ctx context.Context) {
	facts := controlroom.Facts{Now: c.now(), LastStart: c.lastStart}
	cfg, err := c.loadRuntime()
	facts.Backend = cfg.Runtime.Backend
	facts.ConfigErr = err != nil || cfg.RuntimeErr != nil
	// The backend rule comes before any Rex call, so an afk-only machine (or
	// a bad [runtime]) returns here having made none. Decide says the same
	// thing; the check is repeated because Decide needs the server facts that
	// cost the calls this return avoids.
	if facts.Backend != daemonconfig.BackendRex || facts.ConfigErr {
		return
	}

	if _, err := rex.CheckServer(ctx, c.rex); err != nil {
		// Not answering (or too old): the decision waits; say why in the log.
		c.logf("control room: the Rex server is not answering, so the control room waits: %v", err)
		return
	}
	facts.ServerAnswers = true

	sess, found, err := rex.FindSession(ctx, c.rex, controlroom.SessionName)
	if err != nil {
		c.logf("control room: could not look for the %q Rex session: %v", controlroom.SessionName, err)
		return
	}
	facts.SessionFound = found
	if found {
		blocks, err := rex.ListBlocks(ctx, c.rex, sess.ID)
		if err != nil {
			c.logf("control room: could not list the blocks of the %q Rex session: %v", controlroom.SessionName, err)
			return
		}
		for _, b := range blocks {
			if b.Placement == "placed" && b.WindowID != "" {
				facts.WindowAlive = true
				break
			}
		}
	}

	if controlroom.Decide(facts).Kind != controlroom.Start {
		return
	}
	// Record the attempt before the calls, so a create that fails is still
	// held to once a minute instead of retried every pass.
	c.lastStart = facts.Now

	self, err := c.executable()
	if err != nil || self == "" {
		c.logf("control room: could not find the afk program to open the control room: %v", err)
		return
	}
	sessionID := sess.ID
	if !found {
		created, err := rex.CreateSession(ctx, c.rex, controlroom.SessionName)
		if err != nil {
			c.logf("control room: could not create the %q Rex session: %v", controlroom.SessionName, err)
			return
		}
		sessionID = created.ID
	}
	_, err = rex.CreateWindow(ctx, c.rex, sessionID, controlroom.SessionName, rex.TerminalOptions{
		Command: []string{self, "dash"},
		Size:    rex.Size{Columns: rexlayout.TerminalSize.Columns, Rows: rexlayout.TerminalSize.Rows},
	})
	if err != nil {
		c.logf("control room: could not open the control-room window: %v", err)
		return
	}
	c.logf("control room: opened the control room in the %q Rex session", controlroom.SessionName)
}
