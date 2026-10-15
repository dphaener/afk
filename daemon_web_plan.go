package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/planfolder"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/profile"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/web"
)

// This file joins the web page's plan status and approve seams to the
// dashboard's own: the page words the plan line with ui.PlanStatus.Line and
// the result with approveResultText, so the two surfaces say the same things.

// webPlanStatus is web.Deps.PlanStatus. It is the dashboard's file read with
// one change: the main checkout lookup returns root as given. The page only
// asks about a configured or listed project, whose root already is its main
// checkout, so a page request runs no git and no network.
func webPlanStatus(root string) web.PlanStatus {
	return webPlanStatusWith(dashPlanStatusParts{gitCommonDir: func(string) string { return "" }}, root)
}

// webPlanStatusWith is webPlanStatus with the parts passed in, so a test can
// check the mapping without reading a project.
func webPlanStatusWith(parts dashPlanStatusParts, root string) web.PlanStatus {
	s := loadDashPlanStatus(parts, root)
	if s.Err != "" {
		// An unreadable status says nothing else; the page words it as
		// "could not be read".
		return web.PlanStatus{Problems: []string{}}
	}
	problems := s.Problems
	if problems == nil {
		problems = []string{}
	}
	return web.PlanStatus{
		Known:      true,
		Session:    s.Session,
		Damaged:    s.Damaged,
		Mode:       s.Mode,
		Phase:      s.Phase,
		Line:       s.Line(),
		Approvable: s.Session && !s.Damaged && s.Mode == "new" && s.Phase == "review",
		Problems:   problems,
	}
}

// webApprovePlan is web.Deps.ApprovePlan: the steps of afk plan approve for
// the project at root. It commits and pushes, so the page calls it only after
// the owner's confirm.
func webApprovePlan(root string) web.PlanApproveResult {
	return webApprovePlanWith(approvePlanForSurface, realApproveDeps(), root)
}

// webApprovePlanWith is webApprovePlan with the entry point and its
// dependencies passed in, so a test can check the mapping without git. It maps
// outcomes as dashApprovePlanWith does; any outcome it does not know is a
// failure, the safe reading.
func webApprovePlanWith(approve func(string, approveDeps) approveResult, deps approveDeps, root string) web.PlanApproveResult {
	res := approve(root, deps)
	out := web.PlanApproveFailed
	switch res.Outcome {
	case approveApproved:
		out = web.PlanApproved
	case approveAlreadyApproved:
		out = web.PlanApproveAlready
	case approveRefused, approveNoProject:
		out = web.PlanApproveRefused
	}
	return web.PlanApproveResult{Outcome: out, Text: approveResultText(res)}
}

// webStartPlan is web.Deps.StartPlan: afk plan extend, amend or reconcile for
// the project at root, minus the terminal. It never sets a provider (the
// default agent runs) and never writes amend's yes marker, so the page cannot
// skip the owner's read of the sentence.
func webStartPlan(root, mode, text string) web.PlanStartResult {
	return webStartPlanWith(startPlanForSurface, realPlanDeps(), func(root string) error {
		_, err := readMetaWithRetry(root, session.PlanSessionName, attachWait)
		return err
	}, root, mode, text)
}

// webStartPlanWith is webStartPlan with the entry point, its dependencies and
// the host wait passed in. A started or resumed result is held until the host
// has written host.json, because the page sends the owner to the chat view at
// once and the chat reads that record. An outcome it does not know is a
// refusal, so nothing reads as started that the entry point did not say.
func webStartPlanWith(start func(planSurfaceRequest, planDeps) planSurfaceResult, deps planDeps, wait func(root string) error, root, mode, text string) web.PlanStartResult {
	res := start(planSurfaceRequest{Mode: mode, Project: root, Text: text}, deps)
	out := web.PlanStartResult{
		Outcome:   web.PlanStartRefused,
		Reason:    res.Reason,
		Open:      res.SessionOpen,
		OpenMode:  res.OpenMode,
		OpenPhase: res.OpenPhase,
		Root:      res.Root,
	}
	// The page names a new project after the folder planning really runs in
	// and keeps it on the being-planned list by that folder, while the owner
	// may have typed a path through a link. Resolved for the new mode only,
	// as dashStartPlanWith does: the other modes are sent a configured
	// project's folder, which is the name the rest of the page knows it by.
	if mode == planSurfaceNew && out.Root != "" {
		if resolved, err := filepath.EvalSymlinks(out.Root); err == nil {
			out.Root = resolved
		}
	}
	switch res.Outcome {
	case planSurfaceStarted:
		out.Outcome = web.PlanStartStarted
	case planSurfaceResumed:
		out.Outcome = web.PlanStartResumed
	}
	if out.Outcome == web.PlanStartRefused {
		return out
	}
	// host.json is written in the folder the host runs in, the main
	// checkout, which for a new plan typed as a subfolder is not the folder
	// the page sent.
	waitIn := root
	if out.Root != "" {
		waitIn = out.Root
	}
	if err := wait(waitIn); err != nil {
		return web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: planHostNotReadyReason(err)}
	}
	return out
}

// webCheckPlanFolder is web.Deps.CheckPlanFolder: what Start planning would do
// with the text typed into the page's Where field. A bare name is decided
// against the owner profile's project location (planfolder.Decide), as the
// dashboard's form does; a path gets the same looking the start does
// (checkNewPlanFolder), so the confirm card and the start agree. It only
// reads: the profile, the disk, and git through planCheckDeps, whose writers
// refuse instead of writing.
func webCheckPlanFolder(text string) web.PlanFolderCheck {
	return webCheckPlanFolderNamesWith(webProfileLocation, os.Stat, planCheckDeps(realPlanDeps()), text)
}

// webProfileLocation reads the owner profile's project location. It is the
// same reading dashPlanFolder does, so a name means the same folder on the
// page and on the dashboard.
func webProfileLocation() planfolder.Location {
	p, _, found, _, err := profile.Load(profile.DefaultDeps())
	home, _ := os.UserHomeDir()
	return planfolder.LocationFrom(p, found, err, home)
}

// webCheckPlanFolderNamesWith is webCheckPlanFolder with the location, the
// stat call and the plan dependencies passed in, so a test can check every
// name and path row against a fake location and temp folders.
//
// Name is set for every bare-name text, whatever the kind: the page's create
// card posts it back (never the full path), and an existing folder reached
// from a name gets a note that it was found, not made.
//
// A name whose folder already exists is not refused, as planfolder words it:
// it is looked at as that folder's path, so the card says repo, folder, open
// or has_plan for it exactly as when the owner types the path. planfolder's
// sentence for it ("The dashboard can plan in it.") is never shown here.
func webCheckPlanFolderNamesWith(location func() planfolder.Location, stat func(string) (fs.FileInfo, error), deps planDeps, text string) web.PlanFolderCheck {
	name := strings.TrimSpace(text)
	if !planfolder.IsName(name) {
		return webCheckPlanFolderWith(deps, text)
	}
	d := planfolder.Decide(name, location(), stat)
	var out web.PlanFolderCheck
	switch {
	case d.Kind == planfolder.KindCreate:
		out = web.PlanFolderCheck{Kind: web.PlanFolderCreate, Path: d.Path}
	case d.Kind == planfolder.KindRefuse && d.Exists:
		out = webCheckPlanFolderWith(deps, d.Path)
	default:
		// KindUse cannot come back for a bare name; were planfolder to change
		// that, refusing is the safe reading, since nothing here was taught
		// what it means for a name.
		out = web.PlanFolderCheck{Kind: web.PlanFolderRefused, Reason: d.Reason}
		if out.Reason == "" {
			out.Reason = "afk could not tell what this name means. Nothing was started."
		}
	}
	out.Name = name
	return out
}

// webCheckPlanFolderWith is the page's check for a typed path, with the
// dependencies passed in, so a test can check every kind against a temp
// folder and a fake git. A kind it does not know is refused, so the page
// never offers to start what this mapping has not been taught.
//
// A missing folder and a file get the page's own sentences. The shared
// looking words them for the command line ("pass --project ..."), which a
// page with a Where field has no use for, and the page can now offer a name
// instead of making the owner create the folder first.
func webCheckPlanFolderWith(deps planDeps, text string) web.PlanFolderCheck {
	path, kind, reason := checkNewPlanFolder(text, deps)
	out := web.PlanFolderCheck{Path: path, Kind: web.PlanFolderRefused, Reason: reason}
	switch kind {
	case planFolderRepo:
		out.Kind = web.PlanFolderRepo
	case planFolderPlain:
		out.Kind = web.PlanFolderFolder
	case planFolderOpen:
		out.Kind = web.PlanFolderOpen
	case planFolderHasPlan:
		out.Kind = web.PlanFolderHasPlan
	}
	if out.Kind == web.PlanFolderRefused && filepath.IsAbs(path) {
		// The same os.Stat findPlanProject refused on. Any other refusal is
		// about a folder that is there, so these two only replace its words.
		fi, err := os.Stat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out.Reason = fmt.Sprintf("There is no folder at %s. Type the path of a folder that exists, or a plain name to make a new folder in your project location. Nothing was started.", path)
		case err == nil && !fi.IsDir():
			out.Reason = fmt.Sprintf("%s is a file, not a folder. Type the path of a folder. Nothing was started.", path)
		}
	}
	if out.Kind == web.PlanFolderRefused && out.Reason == "" {
		out.Reason = "afk could not tell what this folder is. Nothing was started."
	}
	return out
}

// webCreatePlanFolder is web.Deps.CreatePlanFolder: after the owner confirmed
// a new project by name, make its folder under the profile's project
// location and turn it into a git repository.
func webCreatePlanFolder(text string) (string, error) {
	return webCreatePlanFolderWith(webProfileLocation, os.Stat, func(p string) error { return os.Mkdir(p, 0o755) }, gitInitIn, text)
}

// webCreatePlanFolderWith is webCreatePlanFolder with the location, the stat
// call and the two side effects passed in, so a test can check the guard and
// the failure sentences without a real profile or git.
//
// It decides again from the typed text and creates only on KindCreate. That
// is the guard that matters: the text comes from a POST, and anyone holding
// the token can post anything, so a path, a name with a slash, or a name
// whose folder appeared since the card was drawn must make nothing. Where the
// folder goes is decided here from the profile, never taken from the request.
//
// mkdir is os.Mkdir in production, never MkdirAll, so the project location
// itself is never created. A failure removes nothing (planfolder.Create); the
// path returned is the folder left on disk, or "" when mkdir itself failed
// and nothing was made, so the page never claims a folder that is not there.
func webCreatePlanFolderWith(location func() planfolder.Location, stat func(string) (fs.FileInfo, error), mkdir, gitInit func(string) error, text string) (string, error) {
	name := strings.TrimSpace(text)
	if !planfolder.IsName(name) {
		return "", fmt.Errorf("%s is a path, and only a plain project name makes a new folder. Nothing was created.", name)
	}
	d := planfolder.Decide(name, location(), stat)
	switch {
	case d.Kind == planfolder.KindCreate:
	case d.Exists:
		return "", fmt.Errorf("%s already exists, so nothing was created. Check the folder again to plan in it.", d.Path)
	case d.Reason != "":
		return "", errors.New(d.Reason)
	default:
		return "", errors.New("afk could not tell what this name means. Nothing was created.")
	}
	made := false
	err := planfolder.Create(d.Path, func(p string) error {
		if err := mkdir(p); err != nil {
			return err
		}
		made = true
		return nil
	}, gitInit)
	if !made {
		return "", err
	}
	return d.Path, err
}

// webPlanLocation is web.Deps.PlanLocation: the profile's project location
// for the Where field's hint, or "" when none is set or it is unusable, so the
// hint never names a folder a name could not be made in.
func webPlanLocation() string {
	return webPlanLocationFrom(webProfileLocation())
}

// webPlanLocationFrom is webPlanLocation's rule on a given location. Pure.
func webPlanLocationFrom(loc planfolder.Location) string {
	if !loc.Set || loc.Problem != "" {
		return ""
	}
	return loc.Path
}

// errPlanCheckWrites is what the folder check's writers answer. The check
// never reaches them (lookAtNewPlanFolder only reads); they are replaced so
// that a later change that did reach one fails instead of creating a
// repository or starting a host from a page that only drew a confirm card.
var errPlanCheckWrites = errors.New("the folder check only reads; it must not change anything")

// planCheckDeps is deps with every writer replaced by one that refuses: no
// git init, no host spawn, no terminal and no question. T8.15 must not create
// a folder or a repository before the owner confirms.
func planCheckDeps(deps planDeps) planDeps {
	deps.gitInit = func(string) error { return errPlanCheckWrites }
	deps.spawn = func(session.HostSpawn) (session.HostProcess, error) { return session.HostProcess{}, errPlanCheckWrites }
	deps.attach = func(string, bool) (string, int, bool) { return errPlanCheckWrites.Error(), 1, true }
	deps.ask = func(string) (string, error) { return "", errPlanCheckWrites }
	deps.stdinTTY = func() bool { return false }
	deps.stdoutTTY = func() bool { return false }
	return deps
}

// beingPlannedGone says whether the planning session behind a being-planned
// entry is gone, from what plansession.ReadState returned for its folder.
// Pure.
//
// Gone is no state.json at all (the folder was cleaned up or moved) or a
// finished session (plansession.Finished: approved, the usual way out). A
// state.json that cannot be read is not gone: it is a session the owner has
// to fix, and dropping it would hide the one project that needs them.
// Whether the host runs is not asked: a session whose host has stopped is
// still resumable from the page while its state is open.
func beingPlannedGone(ok bool, err error, st plansession.State) bool {
	if err != nil {
		return false
	}
	if !ok {
		return true
	}
	return plansession.Finished(st)
}

// planSessionGone reads the planning state at root for beingPlannedGone. It
// is a file read, so the overview can afford it on every request.
func planSessionGone(root string) bool {
	st, ok, err := plansession.ReadState(root)
	return beingPlannedGone(ok, err, st)
}

// webBeingPlanned turns the being-planned list into the page's entries,
// leaving out the ones whose planning session is gone. The page drops those
// a configured project or a review entry also has (internal/web's
// beingPlanned); deciding "gone" needs the planning files, which that package
// does not read, so it is done here. Never nil, so the API's field is a list.
func webBeingPlanned(list []daemonstate.BeingPlanned, gone func(root string) bool) []web.BeingPlannedView {
	out := make([]web.BeingPlannedView, 0, len(list))
	for _, e := range list {
		if gone(e.Root) {
			continue
		}
		out = append(out, web.BeingPlannedView{Name: e.Name, Root: e.Root})
	}
	return out
}

// webAddBeingPlanned is web.Deps.AddBeingPlanned: after a new-mode start, it
// puts the folder on the being-planned list in stateDir and returns the name
// the page's chat is under.
//
// It first drops the entries whose planning session is gone. The read hides
// them anyway, so the prune only keeps the file from growing; a prune that
// fails is not reported, since the add's own write then fails the same way
// and says so.
//
// The name must lead to one folder (/p/<name>/plan/chat), so the names of the
// configured projects and of the review list are taken. A folder that is on
// the review list already keeps the review's name: it is the same project,
// and the owner has seen it under that name.
func webAddBeingPlanned(stateDir string, configured func() []web.Project, now func() time.Time, gone func(root string) bool) func(root string) (string, error) {
	return func(root string) (string, error) {
		_ = daemonstate.PruneBeingPlanned(stateDir, gone)
		root = filepath.Clean(root)
		var taken []string
		for _, p := range configured() {
			taken = append(taken, p.Name)
		}
		name := ""
		for _, rv := range daemonstate.ReadReviews(stateDir) {
			if filepath.Clean(rv.Root) == root {
				name = rv.Name
				continue
			}
			taken = append(taken, rv.Name)
		}
		e, err := daemonstate.AddBeingPlanned(stateDir, daemonstate.BeingPlanned{
			Name: name,
			Root: root,
			At:   now().UTC().Format(time.RFC3339),
		}, taken)
		if err != nil {
			return "", err
		}
		return e.Name, nil
	}
}
