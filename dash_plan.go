package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/dphaener/afk/internal/ghrepo"
	"github.com/dphaener/afk/internal/planfolder"
	"github.com/dphaener/afk/internal/profile"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ship"
	"github.com/dphaener/afk/internal/ui"
)

// dashStartPlan is the dashboard's three plan actions. It runs the same start
// path `afk plan extend|amend|reconcile` run, through startPlanForSurface, so a
// refusal reads as the command's words and a start writes the command's files.
// internal/ui cannot import this package, so it only sees a function of plain
// strings and its own result type.
//
// realPlanDeps is used for its folder, git, host and spawn calls only. Its ask
// and attach are never reached: the entry point has no prompt and the
// dashboard's pane is the terminal.
func dashStartPlan(root, mode, text string) ui.PlanStartResult {
	out := dashStartPlanWith(startPlanForSurface, realPlanDeps(), root, mode, text)
	return waitPlanHostReady(out, func(root string) error {
		_, err := readMetaWithRetry(root, session.PlanSessionName, attachWait)
		return err
	})
}

// waitPlanHostReady holds a started or resumed result until the planning host
// has written host.json. The dashboard opens the pane the moment this returns,
// and the host publishes host.json a few milliseconds after the spawn, so
// without the wait the first open fails with "reading the host record" and
// only a second try works. A host that never writes it turns the result into a
// refusal that says so, rather than opening a pane that cannot connect.
func waitPlanHostReady(out ui.PlanStartResult, wait func(root string) error) ui.PlanStartResult {
	if out.Outcome != ui.PlanStarted && out.Outcome != ui.PlanResumed {
		return out
	}
	if err := wait(out.Root); err != nil {
		return ui.PlanStartResult{
			Outcome: ui.PlanRefused,
			Root:    out.Root,
			Reason:  planHostNotReadyReason(err),
		}
	}
	return out
}

// planHostNotReadyReason is the sentence for a planning host that never wrote
// host.json. The dashboard and the web page both use it, so the two surfaces
// say the same words.
func planHostNotReadyReason(err error) string {
	return "The planning session started but did not write its host record (" + err.Error() + "). Try again; if it keeps happening, look at .afk/master-plan/host.log."
}

// dashStartPlanWith is dashStartPlan with the entry point and its dependencies
// passed in, so a test can pin what the adapter maps without a real project.
func dashStartPlanWith(start func(planSurfaceRequest, planDeps) planSurfaceResult, deps planDeps, root, mode, text string) ui.PlanStartResult {
	res := start(planSurfaceRequest{Mode: mode, Project: root, Text: text}, deps)
	out := ui.PlanStartResult{
		Reason:    res.Reason,
		Open:      res.SessionOpen,
		OpenMode:  res.OpenMode,
		OpenPhase: res.OpenPhase,
		Root:      res.Root,
		Archive:   res.Archived,
	}
	// The switcher compares resolved paths, and the folder the owner typed
	// may go through a link. The planning root is the one place the
	// dashboard learns where the new project really is, so resolve it for the
	// new mode, whose result is what the dashboard switches to.
	if mode == planSurfaceNew && out.Root != "" {
		if resolved, err := filepath.EvalSymlinks(out.Root); err == nil {
			out.Root = resolved
		}
	}
	// An outcome this adapter does not know stays the zero value, refused, so
	// a new outcome added to the entry point opens no pane until it is mapped.
	switch res.Outcome {
	case planSurfaceStarted:
		out.Outcome = ui.PlanStarted
	case planSurfaceResumed:
		out.Outcome = ui.PlanResumed
	default:
		out.Outcome = ui.PlanRefused
	}
	return out
}

// dashPlanFolder tells the new-plan form what its text means: a folder to use,
// a new folder to make under the profile's project location, or a refusal. It
// reads the profile and the disk, so the dashboard only calls it inside a
// command, never while drawing.
func dashPlanFolder(text string) ui.PlanFolderDecision {
	return dashPlanFolderWith(func() planfolder.Location {
		p, _, found, _, err := profile.Load(profile.DefaultDeps())
		home, _ := os.UserHomeDir()
		return planfolder.LocationFrom(p, found, err, home)
	}, os.Stat, text)
}

// dashPlanFolderWith is dashPlanFolder with the location and the stat call
// passed in, so a test pins the mapping without a real profile or disk.
func dashPlanFolderWith(location func() planfolder.Location, stat func(string) (fs.FileInfo, error), text string) ui.PlanFolderDecision {
	d := planfolder.Decide(text, location(), stat)
	out := ui.PlanFolderDecision{Path: d.Path, Reason: d.Reason, Exists: d.Exists}
	// An unknown kind stays the zero value, refused, so nothing is created or
	// started until a new kind is mapped here.
	switch d.Kind {
	case planfolder.KindUse:
		out.Kind = ui.PlanFolderUse
	case planfolder.KindCreate:
		out.Kind = ui.PlanFolderCreate
	}
	return out
}

// dashCreatePlanFolder makes the folder and the git repository in it. mkdir is
// os.Mkdir, never MkdirAll, so the project location itself is never created.
func dashCreatePlanFolder(path string) error {
	return planfolder.Create(path, func(p string) error { return os.Mkdir(p, 0o755) }, gitInitIn)
}

// dashGitHubOffer finds out whether the dashboard should ask about a GitHub
// repository for the folder it has just made (T7.6). It runs gh up to twice
// (auth status, then the account name), so internal/ui only ever calls it
// inside a command. Each gh call is bounded by ship.DefaultTimeout and runs
// with stdin on the null device, so a gh that wants to ask something fails
// rather than waiting behind the dashboard's screen.
func dashGitHubOffer(path string) ui.GitHubOffer {
	return dashGitHubOfferWith(exec.LookPath, ghrepo.NewGH(ship.NewRun(ship.DefaultTimeout)), path)
}

// dashGitHubOfferWith is dashGitHubOffer with the PATH lookup and gh passed
// in, so a test pins the mapping without a real gh. It stops at the first
// check that fails: there is no point asking a logged-out gh for its account,
// and a gh that is not installed cannot be asked anything. ghrepo.Offer then
// sees the missing later answers as nil, which it never reaches because it
// checks in the same order.
func dashGitHubOfferWith(look func(string) (string, error), gh ghrepo.GH, path string) ui.GitHubOffer {
	d := gitHubDecisionWith(look, gh, path)
	return ui.GitHubOffer{Ask: d.Ask, Repo: d.Repo, Notice: d.Notice}
}

// gitHubDecisionWith runs the checks in ghrepo.Offer's order and stops at the
// first that fails, so the dashboard and the web page ask the same questions
// of gh in the same order: nothing asks a logged-out gh for its account, and
// a gh that is not installed is never run.
func gitHubDecisionWith(look func(string) (string, error), gh ghrepo.GH, path string) ghrepo.Decision {
	var authErr, loginErr error
	login := ""
	_, lookErr := look("gh")
	if lookErr == nil {
		authErr = gh.AuthStatus()
		if authErr == nil {
			login, loginErr = gh.Login()
		}
	}
	return ghrepo.Offer(lookErr, authErr, login, loginErr, path)
}

// dashCreateGitHubRepo makes the private repository and sets it as path's
// origin, after the owner's yes, and returns the outcome as one line. gh only
// creates (no --push, no --source), and git adds the remote itself, so
// nothing leaves the machine but the empty repository's name.
func dashCreateGitHubRepo(path, repo string) string {
	run := ship.NewRun(ship.DefaultTimeout)
	return dashCreateGitHubRepoWith(ghrepo.NewGH(run), ghrepo.NewGit(run), path, repo)
}

// dashCreateGitHubRepoWith is dashCreateGitHubRepo with gh and git passed in,
// so a test can count the create and the remote add without a real gh.
func dashCreateGitHubRepoWith(gh ghrepo.GH, git ghrepo.Git, path, repo string) string {
	return ghrepo.OutcomeText(ghrepo.Create(gh, git, path, repo))
}
