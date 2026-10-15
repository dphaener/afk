package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/plansession"
)

// planSurfaceOutcome is how a request from the dashboard to start or resume a
// planning session ended.
type planSurfaceOutcome string

const (
	// planSurfaceStarted: a new change session was seeded and its host
	// spawned.
	planSurfaceStarted planSurfaceOutcome = "started"
	// planSurfaceResumed: the session that was already open can be opened. Its
	// host was running, or was started again on the state already there.
	planSurfaceResumed planSurfaceOutcome = "resumed"
	// planSurfaceRefused: nothing was started. Reason says why.
	planSurfaceRefused planSurfaceOutcome = "refused"
)

// planSurfaceResume is the mode of a resume-only request: no mode at all,
// which is what a bare `afk plan` is too.
const planSurfaceResume = ""

// planSurfaceNew is the mode of a request to start a new plan in a folder the
// owner typed. Project is that folder; Text is ignored.
const planSurfaceNew = plansession.ModeNew

// planSurfaceRequest is one request from the dashboard.
type planSurfaceRequest struct {
	// Mode is extend, amend or reconcile, planSurfaceNew to start a new plan,
	// or planSurfaceResume to open the session that is already open and
	// nothing else.
	Mode string
	// Project is the project's folder, absolute. A linked worktree is fine: the
	// session lives in the main checkout, as for the command. For
	// planSurfaceNew it is the folder the owner typed, so "~" and "~/..." are
	// expanded with the home folder first.
	Project string
	// Text is amend's sentence or reconcile's document path, relative to the
	// project. Extend and resume ignore it.
	Text string
	// provider is the agent's name. The dashboard never sets it, so a session
	// started there runs the default agent as `afk plan` without --provider
	// does; it exists so a test can reach the "this agent cannot plan" refusal
	// without naming an agent.
	provider string
}

// planSurfaceResult is what the dashboard is told.
type planSurfaceResult struct {
	Outcome planSurfaceOutcome
	// Reason is the refusal in the command's own words, without the
	// "afk plan <mode>: " in front. "" unless refused.
	Reason string
	// SessionOpen: the refusal is "a planning session is already open". The
	// dashboard offers to open it (a resume request) instead of only showing
	// the reason. OpenMode and OpenPhase are that session's.
	SessionOpen         bool
	OpenMode, OpenPhase string
	// Root is the project's main checkout, the folder the planning session's
	// host runs in. "" when the project could not be found.
	Root string
	// Archived is the folder an earlier session's files were moved to, "" when
	// none were.
	Archived string
}

// startPlanForSurface starts or resumes a planning session for a caller with
// no terminal of its own (the dashboard): everything `afk plan extend`,
// `amend` and `reconcile` do up to the moment they take the terminal, through
// the same looking (lookAtPlanProject), decision (decidePlanStart), refusal
// sentences (planRefusal) and start (startPlanSession), so the dashboard and
// the command cannot decide differently. It prints nothing; the caller opens
// the session in its own pane.
//
// Three things differ from the command, on purpose:
//
//   - Terminal is true. The decision's terminal checks exist so that nothing
//     is started where nobody can answer the session's questions. The
//     dashboard's pane is where they are answered.
//   - The reconcile path is taken as relative to the project (cwd is the
//     project root). The dashboard's own folder is wherever it was started
//     from and means nothing to the owner who typed the path.
//   - The yes marker is never written. It is the owner's agreement to an
//     amend given in advance on the command line; a form in the dashboard
//     does not ask for it, so the session asks before it changes the plan.
//
// A new-plan request is looked at as a bare start (mode ""), not as a mode:
// the decision's "asked" modes all need a plan to change, and a new plan is
// what a bare `afk plan` does in a folder without one. Looking at it as
// anything else would refuse the folder for having no plan. A folder that has
// a plan comes back from the decision as "choose which change", which the
// command answers by asking; here it is refused, in words that point at the
// dashboard's own change actions.
//
// A resume request only ever opens a session that is open (a sound state that
// is not finished). It never starts a new plan or asks which change is wanted:
// on anything else it is refused.
func startPlanForSurface(req planSurfaceRequest, deps planDeps) planSurfaceResult {
	refused := func(reason string) planSurfaceResult {
		return planSurfaceResult{Outcome: planSurfaceRefused, Reason: reason}
	}
	switch req.Mode {
	case planSurfaceResume, planSurfaceNew, plansession.ModeExtend:
	case plansession.ModeAmend:
		if strings.TrimSpace(req.Text) == "" {
			return refused("You did not say what to change. Nothing was started.")
		}
	case plansession.ModeReconcile:
		if strings.TrimSpace(req.Text) == "" {
			return refused("You did not name a document. Nothing was started.")
		}
	default:
		return refused(fmt.Sprintf("%q is not a way to change a plan. Nothing was started.", req.Mode))
	}
	if req.Mode == planSurfaceNew {
		return startNewPlanForSurface(req, deps)
	}
	project, why := surfaceProjectPath(req.Mode, req.Project, deps)
	if why != "" {
		return refused(why)
	}

	look, stop := lookAtPlanProject(req.provider, project, req.Mode, false, func() bool { return true }, deps)
	if stop.code != 0 {
		res := refused(stop.text)
		res.Root = look.project.Root
		return res
	}
	root := look.project.Root
	result := planSurfaceResult{Root: root}
	refuse := func(reason string) planSurfaceResult {
		result.Outcome, result.Reason = planSurfaceRefused, reason
		return result
	}

	in := look.in
	if req.Mode == planSurfaceResume && !(in.HasState && !in.Finished) && !in.StateDamaged {
		return refuse("There is no planning session open in this project to go back to. Nothing was started.")
	}

	action := decidePlanStart(in)
	spec := planHostSpawn(root, req.provider)
	switch {
	case req.Mode == planSurfaceResume && action.Kind == planAttach:
		result.Outcome = planSurfaceResumed
		return result
	case req.Mode == planSurfaceResume && action.Kind == planStart && !action.SeedState:
		// The host is gone and the state is there: start the host on it, as a
		// bare `afk plan` does. Nothing is seeded, so the session goes on from
		// the phase it had reached.
		if _, failed := startPlanSession(look.project, action, planSeed{}, spec, deps); failed != "" {
			return refuse(failed)
		}
		result.Outcome = planSurfaceResumed
		return result
	case req.Mode != planSurfaceResume && action.Kind == planStart:
		brief, err := planBrief(req.Mode, req.Text, req.Text, root, root)
		if err != nil {
			return refuse(fmt.Sprintf("%v\nNothing was started.", err))
		}
		archived, failed := startPlanSession(look.project, action, planSeed{Mode: req.Mode, Brief: brief}, spec, deps)
		result.Archived = archived
		if failed != "" {
			return refuse(failed)
		}
		result.Outcome = planSurfaceStarted
		return result
	case action.Kind == planRefuseInProgress:
		result.SessionOpen = true
		result.OpenMode, result.OpenPhase = look.files.State.Mode, planOpenPhase(look)
	}
	// Every refusal the decision makes, and any decision this caller does not
	// act on, which planRefusal words as an internal error.
	return refuse(planRefusal(action.Kind, look))
}

// surfaceProjectPath turns the folder a caller with no terminal sent into a
// full path, or returns the refusal ("" when it may go on). A new-plan request
// carries what the owner typed, so "~" and "~/..." are expanded with the home
// folder first: the form has no home folder to expand it with, and this is the
// same helper the command's --project uses. Every other mode is sent a
// configured project's folder, which is already full.
//
// A relative folder is refused rather than taken from the current folder,
// which for the dashboard or the daemon is wherever it happened to be started
// and is not something the owner chose.
func surfaceProjectPath(mode, project string, deps planDeps) (string, string) {
	if mode == planSurfaceNew && (project == "~" || strings.HasPrefix(project, "~/")) {
		if expanded, err := resolveProject(project, "", deps.home); err == nil {
			project = expanded
		}
	}
	if !filepath.IsAbs(project) {
		return project, fmt.Sprintf("the project folder %q is not a full path. Nothing was started.", project)
	}
	return project, ""
}

// planFolderKind is what a folder typed for a new plan turns out to be, as
// both the start (startNewPlanForSurface) and the web page's read-only folder
// check (checkNewPlanFolder) read it. One kind for both is the point: the
// confirm card the owner agrees to must describe what Start planning does.
type planFolderKind string

const (
	// planFolderRepo: a git repository with no plan and no session open. A
	// start seeds a new session there.
	planFolderRepo planFolderKind = "repo"
	// planFolderPlain: a folder that is not in a git repository. A start runs
	// git init in it first.
	planFolderPlain planFolderKind = "folder"
	// planFolderOpen: a planning session is open there. A start resumes it
	// (or restarts its host on the saved state) and seeds nothing.
	planFolderOpen planFolderKind = "open"
	// planFolderHasPlan: the folder has a plan and no session open. Refused:
	// a plan that exists is changed, never planned anew.
	planFolderHasPlan planFolderKind = "has_plan"
	// planFolderRefused: anything else the decision refuses (a damaged state,
	// the agent of a finished session still running, an agent that cannot
	// plan), and every folder that could not be looked at.
	planFolderRefused planFolderKind = "refused"
)

// newPlanFolderKind maps the decision for a new-plan request (looked at as a
// bare start with a terminal, see startPlanForSurface) to its kind. Pure.
//
// "Open" is both shapes of an open session: its host running (attach) and
// its host gone with the state kept (a start that seeds nothing). Any kind the
// decision may add later falls to refused, so nothing is offered to start
// that this mapping has not been taught.
func newPlanFolderKind(action planStartAction) planFolderKind {
	switch {
	case action.Kind == planAttach:
		return planFolderOpen
	case action.Kind == planStart && !action.SeedState:
		return planFolderOpen
	case action.Kind == planStart && action.InitRepo:
		return planFolderPlain
	case action.Kind == planStart:
		return planFolderRepo
	case action.Kind == planChooseMode:
		return planFolderHasPlan
	}
	return planFolderRefused
}

// newPlanLook is everything a new-plan request learns before anything is
// started: the folder as a full path, what lookAtPlanProject found there, the
// decision, its kind, and the refusal sentence for has_plan and refused.
type newPlanLook struct {
	// dir is the typed folder as a full path, "" when it could not be made
	// one (a relative path).
	dir    string
	look   planLook
	action planStartAction
	kind   planFolderKind
	reason string
}

// lookAtNewPlanFolder is the looking a new-plan request does, shared by the
// start and the web page's folder check so the two cannot disagree. It only
// reads: the folder, its git repository (deps.gitCommonDir), the planning
// files and afk.toml, and whether a planning host runs (deps.hostAlive, a
// file read and a signal-0). Nothing it calls creates a folder, runs git init
// or writes a file; deps' writers are never reached from here.
//
// A new-plan request is looked at as a bare start (mode ""), not as a mode:
// the decision's "asked" modes all need a plan to change, and a new plan is
// what a bare `afk plan` does in a folder without one. Terminal is true for
// the same reason the dashboard's entry point gives (startPlanForSurface).
func lookAtNewPlanFolder(text, providerName string, deps planDeps) newPlanLook {
	dir, why := surfaceProjectPath(planSurfaceNew, text, deps)
	if why != "" {
		return newPlanLook{kind: planFolderRefused, reason: why}
	}
	look, stop := lookAtPlanProject(providerName, dir, planSurfaceResume, false, func() bool { return true }, deps)
	if stop.code != 0 {
		return newPlanLook{dir: dir, look: look, kind: planFolderRefused, reason: stop.text}
	}
	n := newPlanLook{dir: dir, look: look, action: decidePlanStart(look.in)}
	n.kind = newPlanFolderKind(n.action)
	switch n.kind {
	case planFolderHasPlan:
		n.reason = fmt.Sprintf("this folder already has a plan (%s). To change it, use the dashboard's extend, amend or document actions: press ctrl+], then type the name. Nothing was started.", look.planPath)
	case planFolderRefused:
		// Every refusal the decision makes, and any decision this caller does
		// not act on, which planRefusal words as an internal error.
		n.reason = planRefusal(n.action.Kind, look)
	}
	return n
}

// startNewPlanForSurface is startPlanForSurface for a new-plan request: the
// shared looking, then the start the kind calls for.
func startNewPlanForSurface(req planSurfaceRequest, deps planDeps) planSurfaceResult {
	n := lookAtNewPlanFolder(req.Project, req.provider, deps)
	root := n.look.project.Root
	result := planSurfaceResult{Root: root}
	refuse := func(reason string) planSurfaceResult {
		result.Outcome, result.Reason = planSurfaceRefused, reason
		return result
	}
	spec := planHostSpawn(root, req.provider)
	switch n.kind {
	case planFolderOpen:
		if n.action.Kind == planStart {
			// An open session whose host is gone: go on from the saved state.
			if _, failed := startPlanSession(n.look.project, n.action, planSeed{}, spec, deps); failed != "" {
				return refuse(failed)
			}
		}
		result.Outcome = planSurfaceResumed
		return result
	case planFolderRepo, planFolderPlain:
		// The brief stays empty, as in runPlanRequest: the session asks for it.
		archived, failed := startPlanSession(n.look.project, n.action, planSeed{Mode: plansession.ModeNew}, spec, deps)
		result.Archived = archived
		if failed != "" {
			return refuse(failed)
		}
		result.Outcome = planSurfaceStarted
		return result
	}
	if n.action.Kind == planRefuseInProgress {
		// Not reached by a bare start today (only an asked mode is refused
		// for an open session); kept so the dashboard's offer to open the
		// session survives a change to the decision.
		result.SessionOpen = true
		result.OpenMode, result.OpenPhase = n.look.files.State.Mode, planOpenPhase(n.look)
	}
	return refuse(n.reason)
}

// checkNewPlanFolder is the read-only answer to "what would Start planning do
// with this folder": the full path planning would run in (the main checkout,
// or the typed folder when it is not in a repository or could not be looked
// at), the kind, and the refusal sentence when refused. It is the same
// looking the start does (lookAtNewPlanFolder), so the confirm card and the
// start agree on everything but what changes on disk in between, which the
// start checks again.
func checkNewPlanFolder(text string, deps planDeps) (path string, kind planFolderKind, reason string) {
	n := lookAtNewPlanFolder(text, "", deps)
	path = n.dir
	if n.look.project.Root != "" {
		path = n.look.project.Root
	}
	if n.kind == planFolderRefused {
		reason = n.reason
	}
	return path, n.kind, reason
}
