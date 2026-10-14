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
	if req.Mode == planSurfaceNew && (req.Project == "~" || strings.HasPrefix(req.Project, "~/")) {
		// The form takes what the owner types, and the interface has no home
		// folder to expand it with. Same helper the command's --project uses.
		if expanded, err := resolveProject(req.Project, "", deps.home); err == nil {
			req.Project = expanded
		}
	}
	// A relative folder would be taken from wherever the dashboard happened to
	// be started, which is not something the owner chose.
	if !filepath.IsAbs(req.Project) {
		return refused(fmt.Sprintf("the project folder %q is not a full path. Nothing was started.", req.Project))
	}

	lookMode := req.Mode
	if lookMode == planSurfaceNew {
		lookMode = planSurfaceResume // a bare start; see the comment above
	}
	look, stop := lookAtPlanProject(req.provider, req.Project, lookMode, false, func() bool { return true }, deps)
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
	case req.Mode == planSurfaceNew && action.Kind == planAttach:
		result.Outcome = planSurfaceResumed
		return result
	case req.Mode == planSurfaceNew && action.Kind == planStart && !action.SeedState:
		// An open session whose host is gone: go on from the saved state.
		if _, failed := startPlanSession(look.project, action, planSeed{}, spec, deps); failed != "" {
			return refuse(failed)
		}
		result.Outcome = planSurfaceResumed
		return result
	case req.Mode == planSurfaceNew && action.Kind == planStart:
		// The brief stays empty, as in runPlanRequest: the session asks for it.
		archived, failed := startPlanSession(look.project, action, planSeed{Mode: plansession.ModeNew}, spec, deps)
		result.Archived = archived
		if failed != "" {
			return refuse(failed)
		}
		result.Outcome = planSurfaceStarted
		return result
	case req.Mode == planSurfaceNew && action.Kind == planChooseMode:
		return refuse(fmt.Sprintf("this folder already has a plan (%s). To change it, use the dashboard's extend, amend or document actions: press ctrl+space, then type the name. Nothing was started.", look.planPath))
	case req.Mode != planSurfaceResume && req.Mode != planSurfaceNew && action.Kind == planStart:
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
