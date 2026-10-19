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
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/web"
)

// planWaitNotifyTimeout bounds one "the planning session is waiting"
// notification. It runs inside a daemon pass, so a server that never answers
// must cost seconds, not the loop (the same bound as every other daemon
// notification, inboxNotifyTimeout).
const planWaitNotifyTimeout = 10 * time.Second

// planWaitTarget is one project whose planning chat the daemon looks at:
// the name the page shows it under, its main checkout, and where a
// notification about it should lead ("" for no link).
type planWaitTarget struct {
	Name, Root, Link string
}

// planWaitTargets is every project the web page lists, in the page's order
// of precedence, each with the link a notification about it carries. Pure.
//
// The precedence mirrors internal/web's handlers: a configured project comes
// first; a plan waiting for approval is dropped when a configured project has
// its name or folder (server.reviews); a project being planned is dropped when
// either earlier list has its name or folder (server.beingPlanned). Doing the
// same here means a notification never names a project the page would show
// under another name, and its record is kept under the folder the page means.
//
// The link follows what the page serves. The chat address
// /p/{name}/plan/chat is served for a configured project and a project being
// planned (server.findChatProject) but is a 404 for a plan that is only
// waiting for approval, so such a project links to its plan page instead.
// With no public address there is no link, and none while the page has no
// password (webPublicURL gives ""); a link never holds a secret
// (notify.ChatLink and notify.PlanLink add none).
//
// An entry with no folder is skipped: it cannot have a planning session, and
// filepath.Clean would turn it into "." (whatever folder the daemon runs in).
func planWaitTargets(configured, reviews, being []web.Project, publicURL string) []planWaitTarget {
	var out []planWaitTarget
	taken := func(name, root string) bool {
		return slices.ContainsFunc(out, func(t planWaitTarget) bool { return t.Name == name || t.Root == root })
	}
	add := func(ps []web.Project, link func(base, name string) string) {
		for _, p := range ps {
			if p.Root == "" {
				continue
			}
			root := filepath.Clean(p.Root)
			if taken(p.Name, root) {
				continue
			}
			out = append(out, planWaitTarget{Name: p.Name, Root: root, Link: link(publicURL, p.Name)})
		}
	}
	add(configured, notify.ChatLink)
	add(reviews, notify.PlanLink)
	add(being, notify.ChatLink)
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
	// address is the project's ntfy address, "" for none or when its
	// config cannot be read.
	address func(root string) string
	send    func(ctx context.Context, url string, m notify.NtfyMessage) error
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
// again. With no ntfy address nothing is sent and nothing is recorded, so
// adding an address later still announces a wait that is open then.
//
// Every failure is one log line and nothing more. A send error comes from
// notify.Ntfy.Send, which never holds the address's credentials.
func (n planWaitNotifier) check(ctx context.Context) {
	waits := daemonstate.ReadPlanWaits(n.stateDir)
	changed := false
	for _, t := range n.targets() {
		if ctx.Err() != nil {
			break
		}
		i := slices.IndexFunc(waits, func(w daemonstate.PlanWait) bool { return filepath.Clean(w.Root) == t.Root })
		in := planwait.Input{Project: t.Name, Chat: n.chat(t.Name, t.Root), Link: t.Link}
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
		url := n.address(t.Root)
		if url == "" {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, planWaitNotifyTimeout)
		err := n.send(sctx, url, d.Message)
		cancel()
		if err != nil {
			n.logf("%s: warning: the \"planning session is waiting\" notification could not be sent; it is tried again on the next pass: %v", t.Name, err)
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
// every pass, the same reads the web page makes (daemonWebDeps). publicURL
// is webPublicURL's answer, "" while no password is set, and never a secret.
func realPlanWaitNotifier(projects func() []daemon.Project, stateDir, publicURL string, now func() time.Time, logf func(string, ...any)) planWaitNotifier {
	return planWaitNotifier{
		targets: planTargetsFrom(projects, stateDir, publicURL),
		chat:    webPlanChat(now),
		address: func(root string) string {
			cfg, err := projectconfig.Load(root)
			if err != nil {
				logf("%s: warning: the project config could not be read, so no \"planning session is waiting\" notification was sent: %v", filepath.Base(root), err)
				return ""
			}
			return cfg.Notify.NtfyURL
		},
		send: func(ctx context.Context, url string, m notify.NtfyMessage) error {
			return notify.Ntfy{URL: url}.Send(ctx, m)
		},
		stateDir: stateDir,
		now:      now,
		logf:     logf,
	}
}

// planTargetsFrom builds the function that lists the projects whose planning
// session the daemon looks at on each pass. The wait notifier and the planning
// ender share it so both look at exactly the projects the web page lists, in
// the same precedence (planWaitTargets).
func planTargetsFrom(projects func() []daemon.Project, stateDir, publicURL string) func() []planWaitTarget {
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
		return planWaitTargets(configured, webReviews(daemonstate.ReadReviews(stateDir), configured), being, publicURL)
	}
}
