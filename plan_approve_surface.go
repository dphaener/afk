package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/session"
)

// approveOutcome is how one approve run ended.
type approveOutcome string

const (
	// approveApproved: every step that was left ran, and the phase is
	// approved.
	approveApproved approveOutcome = "approved"
	// approveAlreadyApproved: the session was approved before this run, which
	// changed nothing.
	approveAlreadyApproved approveOutcome = "already approved"
	// approveRefused: the gate said no before the first step. Nothing was
	// changed.
	approveRefused approveOutcome = "refused"
	// approveNoProject: the project folder could not be found or used, so
	// nothing was looked at.
	approveNoProject approveOutcome = "no project"
	// approveFailed: a step failed. The steps before it are done and stay
	// done; a second run does only what is left.
	approveFailed approveOutcome = "failed"
)

// approvePushOutcome is what happened to the push of the approve commit.
type approvePushOutcome string

const (
	// approvePushNotAttempted: no commit was made in this run (it failed, or
	// the documents were already committed), so there was nothing to push.
	// The zero value, so a result that never reached the push says so.
	approvePushNotAttempted approvePushOutcome = ""
	approvePushed           approvePushOutcome = "pushed"
	// approvePushViaPR: the project sends plan changes out as pull requests,
	// so the commit stays local on purpose.
	approvePushViaPR approvePushOutcome = "via_pr"
	// approvePushNoOrigin: the repository has no origin to push to.
	approvePushNoOrigin approvePushOutcome = "no origin"
	// approvePushNoBranch: the checkout is not on a branch (a detached HEAD).
	approvePushNoBranch approvePushOutcome = "no branch"
	// approvePushFailed: git push was run and failed. Err and Branch say how
	// and where, so the owner can push by hand.
	approvePushFailed approvePushOutcome = "failed"
)

// approvePush is the push part of a run.
type approvePush struct {
	Outcome approvePushOutcome
	// Branch is the branch pushed, or that failed to push. "" otherwise.
	Branch string
	// Err is git's error when Outcome is approvePushFailed.
	Err error
}

// approveCounts is the checker's summary of the approved plan, read as the
// closing line reads it. Read is false when the plan could not be read again
// at the end (the line then has no counts) and on every outcome but approved,
// where nothing was counted; Summary is then all zeros and means nothing.
type approveCounts struct {
	Read    bool
	Summary planlint.Summary
}

// approveLine is one thing `afk plan approve` prints: text, without its
// closing newline, and which stream it goes to.
type approveLine struct {
	toStderr bool
	text     string
}

// approveResult is everything one approve run did, as data. The command
// prints Lines and exits with Code; the dashboard words the other fields
// itself, because the command's lines are written for a terminal (several to
// a run, some for stderr) and a notice is one short message.
type approveResult struct {
	Outcome approveOutcome
	// Root is the project's main checkout, "" when it could not be found.
	Root string
	// PlanRel is the master plan's path relative to Root, with forward
	// slashes. "" when the run stopped before it was worked out.
	PlanRel string
	// Reason is the refusal (approveRefused) or why there is no project
	// (approveNoProject), in the command's words without "afk plan approve: "
	// in front. "" for the other outcomes.
	Reason string
	// Code is the exit code the command returns: 0 approved or already
	// approved, 1 refused or failed, and for no project findPlanProject's own
	// 1 or 2.
	Code int
	// Failed and Err are the step that failed and why (approveFailed only).
	Failed approveStep
	Err    error
	// Done is the steps this run did (for approveFailed, the ones before the
	// failure); Left is the steps still to do, the failed one first.
	Done, Left []approveStep
	Counts     approveCounts
	// DaemonRunning: an afk daemon appeared to be running when the run
	// finished (approveApproved only). Advisory, as daemonRunning is.
	DaemonRunning bool
	Push          approvePush
	// Lines is the command's output, in the order it is printed.
	Lines []approveLine
}

// say adds a line for stdout; warn adds one for stderr.
func (r *approveResult) say(format string, a ...any) {
	r.Lines = append(r.Lines, approveLine{text: fmt.Sprintf(format, a...)})
}

func (r *approveResult) warn(format string, a ...any) {
	r.Lines = append(r.Lines, approveLine{toStderr: true, text: fmt.Sprintf(format, a...)})
}

// approvePlanForSurface is `afk plan approve` after its flags are parsed: it
// finds the project, runs the gate, gathers the facts, runs the steps
// approveSteps returns in order and stops at the first that fails. It prints
// nothing. The command calls it and prints Lines, so the dashboard (which
// needs the run as data, not as text on a terminal it does not have) and the
// command go down the one path and cannot approve differently.
//
// projectFlag is what --project would hold: "" for the current folder.
//
// The gate refuses anything that is not a reviewed, clean draft, and changes
// nothing. The facts are gathered once, before any step.
func approvePlanForSurface(projectFlag string, deps approveDeps) approveResult {
	var res approveResult
	project, why, code := findPlanProject(projectFlag, deps.plan)
	if code != 0 {
		res.Outcome, res.Reason, res.Code = approveNoProject, why, code
		res.warn("%s: %s", planApproveCmd, why)
		return res
	}
	root := project.Root
	res.Root = root

	// refuse is for everything before the first step, where it is still true
	// that nothing was changed.
	refuse := func(format string, a ...any) approveResult {
		res.Outcome, res.Code = approveRefused, 1
		res.Reason = fmt.Sprintf(format, a...)
		res.warn("%s: the plan was not approved.\n%s", planApproveCmd, res.Reason)
		res.warn("%s", planNothingChanged)
		return res
	}

	files, planPath, err := loadPlanSession(root, deps.plan)
	if err != nil {
		return refuse("%v", err)
	}
	verdict, err := plansession.Approve(files)
	if err != nil {
		return refuse("%v", err)
	}
	if verdict == plansession.ApproveAlready {
		res.Outcome = approveAlreadyApproved
		res.say("The plan is already approved. %s", planNothingChanged)
		return res
	}

	// The planning session passed, but the daemon reads the plan its own way
	// (masterplan.Parse and Validate, and the ledger). A plan it would refuse
	// must never be approved: the project would be listed and then sit there
	// with an error on every pass.
	if _, err := loadBuildState(root); err != nil {
		return refuse("The daemon could not build from this project as it is: %v.", err)
	}
	if !project.IsRepo {
		return refuse("%s is not a git repository, so the plan cannot be committed and the daemon could not build in it. Run `git init` there, then run `%s` again.", root, planApproveCmd)
	}
	// projectconfig refuses a plan path that is absolute or climbs out with
	// "..", so this cannot happen through afk.toml today. It is kept because
	// a plan outside the project would be committed nowhere, and the check
	// costs nothing if projectconfig ever loosens.
	planRel, err := filepath.Rel(root, planPath)
	if err != nil || planRel == ".." || strings.HasPrefix(planRel, ".."+string(filepath.Separator)) {
		return refuse("The master plan %s is outside the project folder %s, so it cannot be committed with the project. Move it into the project and set `plan` in afk.toml to where it is.", planPath, root)
	}
	planRel = filepath.ToSlash(planRel)
	res.PlanRel = planRel

	// via_pr is read before afk.toml is written. A project with no afk.toml
	// can still have it set, in afk.local.toml.
	projCfg, err := projectconfig.Load(root)
	if err != nil {
		return refuse("%v", err)
	}
	machine, err := daemonconfig.Load(deps.project.Deps)
	if err != nil {
		// Step 4 would fail on the same file, after the commit was made.
		return refuse("afk's config cannot be read, so the project could not be added to it: %v.", err)
	}

	run := approveRun{deps: deps, root: root, planRel: planRel, files: files, viaPR: projCfg.Planning.ViaPR, res: &res}
	for _, p := range machine.Projects {
		if filepath.Clean(p.Path) == root {
			run.facts.Listed = true
		}
	}
	run.facts.HasConfig = deps.exists(filepath.Join(root, projectconfig.FileName))
	if run.facts.HasConfig {
		changed, err := approveDocsChanged(deps.run, root, approveDocuments(root, planRel, deps.exists))
		if err != nil {
			return refuse("%v.", err)
		}
		run.facts.DocsChanged = changed
	}
	run.facts.HasLedger = deps.ledgerExists(root)
	run.facts.Approved = files.State.Phase == plansession.PhaseApproved
	// The state folder is only needed for the review list and the closing
	// sentence. When it cannot be found there is no list to tidy.
	if dir, err := deps.stateDir(); err == nil {
		run.stateDir = dir
		for _, r := range daemonstate.ReadReviews(dir) {
			if filepath.Clean(r.Root) == root {
				run.facts.Reviewed = true
			}
		}
	}

	steps := approveSteps(run.facts)
	for i, step := range steps {
		if err := run.do(step); err != nil {
			res.Outcome, res.Code = approveFailed, 1
			res.Failed, res.Err = step, err
			res.Done, res.Left = steps[:i], steps[i:]
			run.reportFailure(step, err, steps[:i], steps[i:])
			return res
		}
	}

	res.Outcome = approveApproved
	res.Done = steps
	run.reportApproved(planPath)
	return res
}

// reportFailure says what stopped the run, which steps this run did and
// which are left, and that running the command again finishes them.
func (a *approveRun) reportFailure(failed approveStep, err error, done, left []approveStep) {
	a.res.warn("%s: could not %s.\n%v", planApproveCmd, failed.todo(), err)
	if len(done) == 0 {
		a.res.warn("Done in this run: nothing.")
	} else {
		parts := make([]string, len(done))
		for i, s := range done {
			parts[i] = s.did()
		}
		a.res.warn("Done in this run: %s.", strings.Join(parts, "; "))
	}
	parts := make([]string, len(left))
	for i, s := range left {
		parts[i] = s.todo()
	}
	a.res.warn("Not done: %s.", strings.Join(parts, "; "))
	a.res.warn("The plan is not approved yet. Fix that and run %s again. It does only the steps that are left.", planApproveCmd)
}

// reportApproved is the closing lines of a run that finished. The counts
// come from the checker's summary, the one place a plan is counted, so they
// are the numbers `afk plan lint --summary` prints.
func (a *approveRun) reportApproved(planPath string) {
	if data, err := os.ReadFile(planPath); err == nil {
		s := planlint.Lint(data).Summary
		a.res.Counts = approveCounts{Read: true, Summary: s}
		a.res.say("Approved the plan at %s: %s in %s, %s for you to do, %s.", a.planRel,
			plural(s.Tasks, "task"), plural(s.Milestones, "milestone"), plural(s.OwnerTasks, "task"), plural(s.OpenQuestions, "open question"))
	} else {
		a.res.say("Approved the plan at %s.", a.planRel)
	}
	a.res.DaemonRunning = a.stateDir != "" && a.deps.daemonRunning(a.stateDir)
	if a.res.DaemonRunning {
		a.res.say("A running afk daemon starts the first ready task within a few seconds.")
	} else {
		a.res.say("No afk daemon seems to be running. Start it with afk daemon, and it starts the first ready task.")
	}
	a.res.say("The planning session is no longer needed. You can close it with afk clean %s.", session.PlanSessionName)
}
