package main

import (
	"context"
	"io"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/planend"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/session"
)

// composePlanHooks runs the hooks one after another, in the order given. The
// daemon has a single planning hook (daemon.Deps.PlanWaits); this is how the
// wait notifier and the ender share it. Order matters: the notifier goes
// first so the owner is told the session is waiting before its agent ends.
func composePlanHooks(hooks ...func(context.Context)) func(context.Context) {
	return func(ctx context.Context) {
		for _, h := range hooks {
			h(ctx)
		}
	}
}

// planEnder ends the agent of a finished change session that is waiting for
// the owner, from the daemon's once-per-pass planning hook (docs/PLAN.md
// T13.3). The same decision, planend.Decide, guards `afk plan`. Everything it
// touches is a function so a test runs it with no host, transcript or disk.
type planEnder struct {
	targets func() []planWaitTarget
	// files reads the planning files the way `afk plan` does. An error
	// leaves the session alone.
	files       func(root string) (plansession.Files, error)
	chat        func(project, root string) planchat.Chat
	hostAlive   func(root string) bool
	stop        func(root string) (session.HostStop, error)
	logDecision func(root string, e decisionlog.Entry) error
	now         func() time.Time
	logf        func(format string, a ...any)
}

// check is the second half of the daemon's planning hook.
//
// The cheap checks come first so the chat, a transcript read, is only read
// for a candidate: a live host, a state, and a finished session. A failed
// stop or log write is one warning and nothing more; the next pass tries
// again, because the session is still finished and its host still alive.
func (e planEnder) check(ctx context.Context) {
	for _, t := range e.targets() {
		if ctx.Err() != nil {
			return
		}
		if !e.hostAlive(t.Root) {
			continue
		}
		files, err := e.files(t.Root)
		if err != nil {
			e.logf("%s: warning: the planning files could not be read, so a finished planning session was left running: %v", t.Name, err)
			continue
		}
		if !files.HasState || !plansession.Finished(files.State) {
			continue
		}
		d := planend.Decide(planend.Input{
			HasState:  files.HasState,
			State:     files.State,
			Summary:   files.Summary,
			Chat:      e.chat(t.Name, t.Root).State,
			HostAlive: true,
		})
		if !d.End {
			continue
		}
		stopped, err := e.stop(t.Root)
		if err != nil {
			e.logf("%s: warning: the finished planning session's agent could not be ended; it is tried again on the next pass: %v", t.Name, err)
			continue
		}
		if stopped != session.HostStopped {
			continue
		}
		entry := decisionlog.Entry{
			At:       e.now().UTC().Format(time.RFC3339),
			Session:  session.PlanSessionName,
			Kind:     "plan_session_ended",
			Question: "end the finished planning session's agent?",
			Chosen:   "ended",
			ChosenBy: decisionlog.ChosenByPolicy,
			Rule:     planend.Rule,
		}
		if err := e.logDecision(t.Root, entry); err != nil {
			e.logf("%s: warning: the end of the planning session's agent could not be written to the decision log: %v", t.Name, err)
			continue
		}
		e.logf("%s: the finished planning session's agent was ended; its summary is in .afk/plan/summary.md", t.Name)
	}
}

// realPlanEnder wires the ender to the machine. It looks at the same projects
// as the wait notifier (planTargetsFrom). Stderr of the stop is discarded:
// the daemon reports through logf.
func realPlanEnder(projects func() []daemon.Project, stateDir string, now func() time.Time, logf func(string, ...any)) planEnder {
	deps := realPlanDeps()
	return planEnder{
		targets: planTargetsFrom(projects, stateDir),
		files: func(root string) (plansession.Files, error) {
			f, _, err := loadPlanSession(root, deps)
			return f, err
		},
		chat:      webPlanChat(now),
		hostAlive: planHostAlive,
		stop: func(root string) (session.HostStop, error) {
			return session.StopHost(root, session.PlanSessionName, session.CleanDeps{Stderr: io.Discard})
		},
		logDecision: func(root string, e decisionlog.Entry) error {
			return decisionlog.Append(decisionlog.Path(root), e)
		},
		now:  now,
		logf: logf,
	}
}
