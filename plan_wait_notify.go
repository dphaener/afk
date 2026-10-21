package main

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/planwait"
	"github.com/dphaener/afk/internal/web"
	"github.com/dphaener/afk/internal/webpush"
)

// planWaitTarget is one project whose planning chat the daemon looks at:
// the name the page shows it under, its main checkout, and the path on the
// page a push about it opens. A push opens a path against the page's own
// origin, so it never carries an address.
type planWaitTarget struct {
	Name, Root, Path string
}

// planWaitTargets is every project the web page lists, in the page's order
// of precedence, each with the path a push about it opens. Pure.
//
// The precedence mirrors internal/web's handlers: a configured project comes
// first; a plan waiting for approval is dropped when a configured project has
// its name or folder (server.reviews); a project being planned is dropped when
// either earlier list has its name or folder (server.beingPlanned). Doing the
// same here means a notification never names a project the page would show
// under another name, and its record is kept under the folder the page means.
//
// The path follows what the page serves. The chat path /p/{name}/plan/chat
// is served for a configured project and a project being planned
// (server.findChatProject) but is a 404 for a plan that is only waiting for
// approval, so such a project opens its plan page instead.
//
// An entry with no folder is skipped: it cannot have a planning session, and
// filepath.Clean would turn it into "." (whatever folder the daemon runs in).
func planWaitTargets(configured, reviews, being []web.Project) []planWaitTarget {
	var out []planWaitTarget
	taken := func(name, root string) bool {
		return slices.ContainsFunc(out, func(t planWaitTarget) bool { return t.Name == name || t.Root == root })
	}
	add := func(ps []web.Project, path func(name string) string) {
		for _, p := range ps {
			if p.Root == "" {
				continue
			}
			root := filepath.Clean(p.Root)
			if taken(p.Name, root) {
				continue
			}
			out = append(out, planWaitTarget{Name: p.Name, Root: root, Path: path(p.Name)})
		}
	}
	add(configured, notify.ChatPath)
	add(reviews, notify.PlanPath)
	add(being, notify.ChatPath)
	return out
}

// planWaitNotifier tells the owner, once per wait, that a planning session is
// waiting for them (docs/PLAN.md T8.13). Everything it touches outside its
// own state file is a function, so a test runs it with no host, no
// transcript and no network.
type planWaitNotifier struct {
	targets func() []planWaitTarget
	// chat is the planning chat of one project, read only through
	// planchat.Read, which never opens a transcript for a dead host.
	chat func(project, root string) planchat.Chat
	// push is the owner's signed-up devices; the zero value pushes nothing.
	push pushTarget
	// stateDir is afk's state folder, where plan-waits.json lives.
	stateDir string
	now      func() time.Time
	logf     func(format string, a ...any)
}

// check is the daemon's once-per-pass hook (daemon.Deps.PlanWaits).
//
// plan-waits.json is read again on every call and never kept in memory, so a
// daemon restarted after a crash knows what the dead one sent and does not
// repeat it. A wait is recorded only after its notification went out: a
// failed send is retried on the next pass, and a delivered one is never sent
// again. "Delivered" means at least one device took the push. With no device,
// or with every push failing, nothing is recorded, so signing up a device
// later still announces a wait that is open then.
//
// Every failure is one log line and nothing more (pushTarget.deliver writes
// it, naming the device by its label and never its endpoint).
func (n planWaitNotifier) check(ctx context.Context) {
	waits := daemonstate.ReadPlanWaits(n.stateDir)
	changed := false
	for _, t := range n.targets() {
		if ctx.Err() != nil {
			break
		}
		i := slices.IndexFunc(waits, func(w daemonstate.PlanWait) bool { return filepath.Clean(w.Root) == t.Root })
		in := planwait.Input{Project: t.Name, Chat: n.chat(t.Name, t.Root)}
		if i >= 0 {
			in.Recorded, in.HasRecord = waits[i].Key, true
		}
		d := planwait.Decide(in)
		if d.Record == planwait.Clear && i >= 0 {
			waits = slices.Delete(waits, i, i+1)
			changed = true
			continue
		}
		if !d.Send {
			continue
		}
		if n.push.deliver(webpush.Payload{Title: d.Message.Title, Body: d.Message.Body, URL: t.Path, Tag: "afk-plan-" + t.Name}, webpush.Options{}, n.logf) == 0 {
			continue
		}
		rec := daemonstate.PlanWait{Root: t.Root, Key: d.Key, At: n.now().UTC().Format(time.RFC3339)}
		if i >= 0 {
			waits[i] = rec
		} else {
			waits = append(waits, rec)
		}
		changed = true
	}
	if !changed {
		return
	}
	if err := daemonstate.WritePlanWaits(n.stateDir, waits); err != nil {
		n.logf("warning: afk could not record which planning waits it has told you about, so one may be sent again: %v", err)
	}
}

// realPlanWaitNotifier wires the notifier to the machine. projects is the
// loop's current project list (runDaemon's projectList.get), so a project
// added while the daemon runs is looked at from the pass that first reads
// it; the review list and the being-planned list are read from stateDir on
// every pass, the same reads the web page makes (daemonWebDeps).
func realPlanWaitNotifier(projects func() []daemon.Project, stateDir string, now func() time.Time, logf func(string, ...any)) planWaitNotifier {
	return planWaitNotifier{
		targets:  planTargetsFrom(projects, stateDir),
		chat:     webPlanChat(now),
		push:     pushTarget{dir: stateDir, send: webPushSend(stateDir, now)},
		stateDir: stateDir,
		now:      now,
		logf:     logf,
	}
}

// planTargetsFrom builds the function that lists the projects whose planning
// session the daemon looks at on each pass. The wait notifier and the planning
// ender share it so both look at exactly the projects the web page lists, in
// the same precedence (planWaitTargets).
func planTargetsFrom(projects func() []daemon.Project, stateDir string) func() []planWaitTarget {
	return func() []planWaitTarget {
		got := projects()
		configured := make([]web.Project, 0, len(got))
		for _, p := range got {
			configured = append(configured, web.Project{Name: p.Name, Root: p.Root})
		}
		views := webBeingPlanned(daemonstate.ReadBeingPlanned(stateDir), planSessionGone)
		being := make([]web.Project, 0, len(views))
		for _, v := range views {
			being = append(being, web.Project{Name: v.Name, Root: v.Root})
		}
		return planWaitTargets(configured, webReviews(daemonstate.ReadReviews(stateDir), configured), being)
	}
}
