package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/usagestate"
)

// planReviewDeps is what telling the owner "the plan is ready" touches outside
// the project's files: the machine's state folder and config, the project's
// afk.toml, git, and the network. All of it is behind functions so a test
// opens no connection. The zero value means "do not tell anyone"; the tests
// of the other plan commands leave it unset and so cannot send by accident.
type planReviewDeps struct {
	stateDir     func() (string, error)
	loadProject  func(root string) (projectconfig.Config, error)
	loadMachine  func() (daemonconfig.Config, error)
	gitCommonDir func(dir string) string
	send         func(ctx context.Context, url string, m notify.NtfyMessage) error
}

func (d planReviewDeps) ready() bool {
	return d.stateDir != nil && d.loadProject != nil && d.loadMachine != nil && d.gitCommonDir != nil && d.send != nil
}

func realPlanReviewDeps() planReviewDeps {
	return planReviewDeps{
		stateDir:     func() (string, error) { return usagestate.Dir(usagestate.DefaultDeps()) },
		loadProject:  projectconfig.Load,
		loadMachine:  func() (daemonconfig.Config, error) { return daemonconfig.Load(daemonconfig.DefaultDeps()) },
		gitCommonDir: resolveGitCommonDirIn,
		send: func(ctx context.Context, url string, m notify.NtfyMessage) error {
			return notify.Ntfy{URL: url}.Send(ctx, m)
		},
	}
}

// planReviewNotifyTimeout bounds the one POST, as the inbox's does: the phase
// is already written, so a slow server must not make the command hang.
const planReviewNotifyTimeout = 10 * time.Second

// planReviewLink is the address of the plan's page, or "" when there is none
// to give: the machine config could not be read, the page is off (no token),
// or it sets no public address. Pure; sameDir answers "is this configured
// path the project folder?", so the git lookup stays with the caller.
//
// The name in the address is the one the page serves the plan under. A
// configured project is served under its configured name, which need not be
// its folder's; a project that is only on the review list is served under the
// name the list gave it (reviewName). The link never holds the token.
func planReviewLink(cfg daemonconfig.Config, cfgErr error, root, reviewName string, sameDir func(configured, root string) bool) string {
	if cfgErr != nil || webPublicURL(cfg.Web) == "" {
		return ""
	}
	for _, p := range cfg.Projects {
		if sameDir(p.Path, root) {
			return notify.PlanLink(cfg.Web.PublicURL, p.Name)
		}
	}
	return notify.PlanLink(cfg.Web.PublicURL, reviewName)
}

// planReviewBody is the notification text. The counts come from the plan
// checker's summary, the one place a plan is counted.
func planReviewBody(s *planlint.Summary) string {
	const next = "Read it, then run afk plan approve in the project folder."
	if s == nil {
		return next
	}
	return fmt.Sprintf("%s in %s, %s for you to do, %s, %s, %s. %s",
		plural(s.Tasks, "task"), plural(s.Milestones, "milestone"), plural(s.OwnerTasks, "task"),
		plural(s.OpenQuestions, "open question"), plural(s.Errors, "error"), plural(s.Warnings, "warning"), next)
}

// notifyPlanReady puts the project on the review list and sends the one "plan
// ready for review" notification. It is best effort end to end: the phase is
// already written, so every failure is one line on stderr and nothing else.
// With no ntfy_url nothing is sent and nothing is said about it.
func notifyPlanReady(root string, deps planReviewDeps, now time.Time, stderr io.Writer) {
	if !deps.ready() {
		return
	}
	fail := func(err error) {
		fmt.Fprintf(stderr, "afk plan advance: could not send the \"plan ready\" notification: %v\n", err)
	}
	root = filepath.Clean(root)

	reviewName := filepath.Base(root)
	if dir, err := deps.stateDir(); err != nil {
		fail(err)
	} else if err := daemonstate.AddReview(dir, daemonstate.Review{Root: root, At: now.UTC().Format(time.RFC3339)}); err != nil {
		fail(err)
	} else {
		for _, r := range daemonstate.ReadReviews(dir) {
			if filepath.Clean(r.Root) == root {
				reviewName = r.Name
			}
		}
	}

	proj, err := deps.loadProject(root)
	if err != nil {
		fail(err)
		return
	}
	if proj.Notify.NtfyURL == "" {
		return
	}
	var summary *planlint.Summary
	if data, err := os.ReadFile(proj.PlanPath(root)); err == nil {
		s := planlint.Lint(data).Summary
		summary = &s
	}
	machine, machineErr := deps.loadMachine()
	msg := notify.NtfyMessage{
		Title: "Plan ready for review: " + filepath.Base(root),
		Body:  planReviewBody(summary),
		Click: planReviewLink(machine, machineErr, root, reviewName, func(configured, r string) bool {
			return mainCheckout(configured, deps.gitCommonDir(configured)) == filepath.Clean(r)
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), planReviewNotifyTimeout)
	defer cancel()
	if err := deps.send(ctx, proj.Notify.NtfyURL, msg); err != nil {
		fail(err)
	}
}
