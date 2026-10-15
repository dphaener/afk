package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/ship"
	"github.com/dphaener/afk/internal/usagestate"
)

// `afk plan approve` is the owner's "go": it turns a plan that a planning
// session has drafted and checked into a build the daemon runs. It is the
// only writer of the phase `approved` (plansession.Advance never hands it
// out), and it writes that phase last, so a run that stops half way leaves
// the session at `review` and running the command again finishes the work.

const planApproveUsageLine = "afk plan approve [--project DIR]"

// planApproveSubject is the subject of the one commit approve makes.
const planApproveSubject = "docs(plan): Approve the master plan"

// planApproveCmd starts every message of the command.
const planApproveCmd = "afk plan approve"

// approveStep is one thing approve does. The values are in the order the
// steps run, and that order is the design:
//
//   - afk.toml before the commit, so the commit holds it.
//   - The ledger before the machine config. The daemon re-reads its project
//     list on every pass, so the moment the project is listed it may be
//     ticked, and it must find a ledger there.
//   - The phase after all four, so `approved` on disk always means a project
//     the daemon is building. Written earlier, a crash would leave a session
//     that says approved with nothing running, and a second run would stop at
//     "already approved" and never finish.
//   - The review-list entry last, because it only tidies the web page.
type approveStep int

const (
	approveWriteConfig approveStep = iota + 1
	approveCommit
	approveLedger
	approveProject
	approvePhase
	approveDropReview
)

// todo is the step as something still to do; did is the same step done. Both
// are used in the report of a run that stopped half way.
func (s approveStep) todo() string {
	switch s {
	case approveWriteConfig:
		return "write afk.toml"
	case approveCommit:
		return "commit the plan and the project's documents"
	case approveLedger:
		return "create the task ledger"
	case approveProject:
		return "add the project to the projects the daemon builds"
	case approvePhase:
		return "mark the planning session approved"
	case approveDropReview:
		return "take the plan off the list of plans waiting for approval"
	}
	return "an unknown step"
}

func (s approveStep) did() string {
	switch s {
	case approveWriteConfig:
		return "wrote afk.toml"
	case approveCommit:
		return "committed the plan and the project's documents"
	case approveLedger:
		return "created the task ledger"
	case approveProject:
		return "added the project to the projects the daemon builds"
	case approvePhase:
		return "marked the planning session approved"
	case approveDropReview:
		return "took the plan off the list of plans waiting for approval"
	}
	return "an unknown step"
}

// approveFacts is what approveSteps reads, as plain values. The caller does
// the looking (the filesystem, git, the machine config, the review list).
type approveFacts struct {
	// HasConfig: the project has an afk.toml.
	HasConfig bool
	// DocsChanged: one of the documents approve commits differs from HEAD or
	// is untracked. It means nothing while HasConfig is false: git is not
	// asked then, because afk.toml is one of the documents and is not there
	// to be asked about yet (see approveSteps).
	DocsChanged bool
	// HasLedger: .afk/build/ledger.json exists.
	HasLedger bool
	// Listed: the project is in the machine's config.toml.
	Listed bool
	// Approved: the recorded phase is `approved`.
	Approved bool
	// Reviewed: the review list has an entry for the project.
	Reviewed bool
}

// approveSteps returns the steps still to do, in the order they run. Pure.
// Every step is decided by its own fact, which is what makes the command safe
// to repeat: a second run after a full one gets no steps, and a run after a
// crash gets only the steps the crash left.
//
// The commit is also planned whenever afk.toml is missing. Writing it makes a
// new file that has to be committed, so asking git before it exists would
// answer "nothing changed" about a file that is about to change. The commit
// step asks git itself in that case, once afk.toml is written.
func approveSteps(f approveFacts) []approveStep {
	var steps []approveStep
	if !f.HasConfig {
		steps = append(steps, approveWriteConfig)
	}
	if !f.HasConfig || f.DocsChanged {
		steps = append(steps, approveCommit)
	}
	if !f.HasLedger {
		steps = append(steps, approveLedger)
	}
	if !f.Listed {
		steps = append(steps, approveProject)
	}
	if !f.Approved {
		steps = append(steps, approvePhase)
	}
	if f.Reviewed {
		steps = append(steps, approveDropReview)
	}
	return steps
}

// approveDeps is everything the command touches outside its arguments. git is
// behind run and each file or folder approve writes is behind its own seam,
// so a test can fail any one step and no test runs git or reads the real
// config or state folder.
type approveDeps struct {
	plan planDeps
	run  ship.RunFunc
	// exists says whether a file is there.
	exists func(path string) bool
	// create writes a new file and fails with fs.ErrExist when the file is
	// already there, so an existing afk.toml is never overwritten.
	create func(path string, data []byte) error
	// project is the machine config, as `afk project` reaches it.
	project projectEnv
	// stateDir is afk's state folder on this machine, where the review list
	// is.
	stateDir     func() (string, error)
	ledgerExists func(root string) bool
	ledgerInit   func(root string, done []string) error
	writeState   func(root string, s plansession.State) error
	// daemonRunning says whether an afk daemon appears to be running, given
	// the state folder. It is advisory (a process id can be reused) and only
	// chooses between two closing sentences.
	daemonRunning func(stateDir string) bool
}

func realApproveDeps() approveDeps {
	return approveDeps{
		plan: realPlanDeps(),
		// The same bound as the plan-patch commands: a person is waiting on
		// one push, and a push cut off half way has to be done by hand.
		run:          ship.NewRun(planPatchPushTimeout),
		exists:       fileExists,
		create:       createExclusive,
		project:      realProjectEnv(),
		stateDir:     func() (string, error) { return usagestate.Dir(usagestate.DefaultDeps()) },
		ledgerExists: ledger.Exists,
		ledgerInit:   ledger.Init,
		writeState:   plansession.WriteState,
		daemonRunning: func(stateDir string) bool {
			pid, _, ok := daemonstate.ReadMachine(stateDir)
			return ok && host.Alive(pid)
		},
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// createExclusive writes a new file. O_EXCL makes the create fail when the
// file is there, so two runs at once, or a file the owner made a moment ago,
// can never be overwritten. A file that could not be written in full is
// removed again: it is ours (we created it), and left behind it would be read
// as the project's config on the next run.
func createExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// approveDocumentNames is the documents approve commits, relative to the
// project root, plan first. These are the files a planning session writes
// outside .afk/: a task session runs in a worktree made from committed files,
// so each of them has to be committed or the agent building a task never
// sees it. Nothing else is ever staged.
//
// The agent instruction files come from internal/provider so that no agent
// is named here.
func approveDocumentNames(planRel string) []string {
	names := []string{planRel, projectconfig.FileName, ".gitignore", "CONTEXT.md", plansession.DesignBriefFile}
	names = append(names, provider.InstructionFiles()...)
	// A plan named in afk.toml could be one of the other documents; git must
	// not be handed one path twice.
	seen := map[string]bool{}
	out := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// approveDocuments is the documents that exist in the project.
func approveDocuments(root, planRel string, exists func(string) bool) []string {
	var docs []string
	for _, name := range approveDocumentNames(planRel) {
		if exists(filepath.Join(root, filepath.FromSlash(name))) {
			docs = append(docs, name)
		}
	}
	return docs
}

// approveDocsChanged asks git whether any of the documents differs from HEAD
// or is untracked. `git status --porcelain` prints one line per such path and
// nothing at all when there is none, so any output means yes.
func approveDocsChanged(run ship.RunFunc, root string, docs []string) (bool, error) {
	out, err := run(root, "git", append([]string{"status", "--porcelain", "--"}, docs...)...)
	if err != nil {
		return false, fmt.Errorf("git could not say whether the plan is committed: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// runPlanApprove implements `afk plan approve`. Exit codes: 0 approved, or
// already approved; 1 refused, or a step failed; 2 the command was written
// wrongly.
//
// Everything after the flags is approvePlanForSurface, which the dashboard
// calls too; the command only prints what it returns, in order, each line to
// the stream it was meant for.
func runPlanApprove(args []string, stdout, stderr io.Writer, deps approveDeps) int {
	flags := flag.NewFlagSet("plan approve", flag.ContinueOnError)
	proj := addProjectFlag(flags)
	if code := parsePlanFlags(flags, args, planApproveCmd, planApproveUsageLine, stderr); code >= 0 {
		return code
	}
	res := approvePlanForSurface(*proj, deps)
	for _, l := range res.Lines {
		w := stdout
		if l.toStderr {
			w = stderr
		}
		_, _ = io.WriteString(w, l.text+"\n")
	}
	return res.Code
}

// approveRun is one run of the steps: what the gate learned, and the result
// the steps add their lines to.
type approveRun struct {
	deps    approveDeps
	root    string
	planRel string
	files   plansession.Files
	viaPR   bool
	facts   approveFacts
	// stateDir is afk's state folder, "" when it could not be found.
	stateDir string
	res      *approveResult
}

// do runs one step and adds one line when it changed something. A step
// that finds its work already done (another run got there first) adds
// nothing and is not a failure.
func (a *approveRun) do(step approveStep) error {
	switch step {
	case approveWriteConfig:
		return a.writeConfig()
	case approveCommit:
		return a.commit()
	case approveLedger:
		err := a.deps.ledgerInit(a.root, nil)
		if errors.Is(err, ledger.ErrExists) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("could not create the task ledger %s: %w", ledger.Path(a.root), err)
		}
		a.res.say("Created the task ledger (%s). No task is recorded as done yet.", ledger.Path(a.root))
		return nil
	case approveProject:
		added, listed, err := addProjectToConfig(a.deps.project, a.root, "")
		if err != nil {
			return fmt.Errorf("could not add the project to afk's config: %w", err)
		}
		if added {
			a.res.say("Added %s to the projects the daemon builds (%s).", listed, daemonconfig.Path(a.deps.project.Deps))
		}
		return nil
	case approvePhase:
		st := a.files.State
		st.Phase = plansession.PhaseApproved
		st.UpdatedAt = plansession.FormatTime(a.deps.plan.now())
		if err := a.deps.writeState(a.root, st); err != nil {
			return fmt.Errorf("could not write %s: %w", plansession.StatePath(a.root), err)
		}
		a.res.say("Marked the planning session approved.")
		return nil
	case approveDropReview:
		// The plan is approved by now. A list that cannot be written only
		// leaves a stale line on the web page, so it is a note.
		if err := daemonstate.RemoveReview(a.stateDir, a.root); err != nil {
			a.res.warn("Note: the plan is approved, but it could not be taken off the list of plans waiting for approval (%s): %v", daemonstate.ReviewsPath(a.stateDir), err)
		} else {
			a.res.say("Took the plan off the list of plans waiting for approval.")
		}
		// A project started from the web page's Plan a new project is also on
		// the being-planned list. Approved, it is a configured project, so the
		// entry only tidies the list; a project that never reached the review
		// list is dropped there anyway once its session reads as approved
		// (beingPlannedGone). Nothing is said when the folder was not listed.
		if err := daemonstate.RemoveBeingPlanned(a.stateDir, a.root); err != nil {
			a.res.warn("Note: the plan is approved, but it could not be taken off the list of projects being planned (%s): %v", daemonstate.BeingPlannedPath(a.stateDir), err)
		}
		return nil
	}
	return fmt.Errorf("afk does not know approve step %d", int(step))
}

// writeConfig creates afk.toml with the one line that says where the plan is.
// The file is then read back through projectconfig.Load: it must name the
// plan the gate read, or every later command would look at another file. A
// file that does not is removed again, which is safe because this run made
// it.
func (a *approveRun) writeConfig() error {
	path := filepath.Join(a.root, projectconfig.FileName)
	err := a.deps.create(path, []byte("plan = "+strconv.Quote(a.planRel)+"\n"))
	if errors.Is(err, fs.ErrExist) {
		return nil // written since the facts were read; never overwritten
	}
	if err != nil {
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	want := filepath.Join(a.root, filepath.FromSlash(a.planRel))
	cfg, err := projectconfig.Load(a.root)
	if err != nil || cfg.PlanPath(a.root) != want {
		_ = os.Remove(path)
		if err == nil {
			err = fmt.Errorf("it names the plan %s, not %s", cfg.PlanPath(a.root), want)
		}
		return fmt.Errorf("wrote %s, but it does not read back as it should, so it was removed again: %w", path, err)
	}
	a.res.say("Wrote %s, which says where the plan is (%s).", projectconfig.FileName, a.planRel)
	return nil
}

// commit stages and commits the documents and nothing else. Both git commands
// take the paths after "--", so no other change in the checkout is swept in:
// never `git add -A`. Hooks run; --no-verify is never passed, because a hook
// the owner installed is a rule of their project.
//
// git is asked whether there is anything to commit only when the facts could
// not ask (afk.toml was missing then). Otherwise the facts already said yes,
// and asking again would only be a second chance to disagree.
func (a *approveRun) commit() error {
	docs := approveDocuments(a.root, a.planRel, a.deps.exists)
	if !a.facts.HasConfig {
		changed, err := approveDocsChanged(a.deps.run, a.root, docs)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
	}
	if out, err := a.deps.run(a.root, "git", append([]string{"add", "--"}, docs...)...); err != nil {
		return approveGitError("git add", out, err)
	}
	if out, err := a.deps.run(a.root, "git", append([]string{"commit", "-m", planApproveSubject, "--"}, docs...)...); err != nil {
		return approveGitError("git commit", out, err)
	}
	a.res.say("Committed the plan and the project's documents: %s.", strings.Join(docs, ", "))
	a.push()
	return nil
}

// approveGitError is a failed add or commit, with what git said. A commit
// most often fails for a missing git identity or a hook, and git's own words
// are what tell the owner which.
func approveGitError(cmd, stdout string, err error) error {
	said := strings.TrimSpace(stdout)
	if said != "" {
		said += "\n"
	}
	return fmt.Errorf("%s did not work. git said:\n%s%w", cmd, said, err)
}

// push sends the commit to origin. It never fails the run: the commit is
// made, the daemon builds from the local checkout, and a push is something
// the owner can do by hand. It is never forced.
//
// With via_pr the project's base branch is not pushed to directly (in such a
// project a push to it can be a deploy), so the commit stays local and goes
// out with the first task's pull request, whose branch is made from it.
func (a *approveRun) push() {
	if a.viaPR {
		a.res.Push = approvePush{Outcome: approvePushViaPR}
		a.res.say("The commit was not pushed, because this project sends plan changes out as pull requests (via_pr under [planning]). It goes out with the first task's pull request.")
		return
	}
	if _, err := a.deps.run(a.root, "git", "remote", "get-url", "origin"); err != nil {
		a.res.Push = approvePush{Outcome: approvePushNoOrigin}
		a.res.say("The repository has no origin, so the commit was not pushed.")
		return
	}
	// `git branch --show-current` prints nothing on a detached HEAD.
	out, err := a.deps.run(a.root, "git", "branch", "--show-current")
	branch := strings.TrimSpace(out)
	if err != nil || branch == "" {
		a.res.Push = approvePush{Outcome: approvePushNoBranch}
		a.res.warn("Note: the commit was not pushed, because the project is not on a branch. To push it by hand, check out a branch in %s and run: git push origin <branch>", a.root)
		return
	}
	if _, err := a.deps.run(a.root, "git", "push", "origin", branch); err != nil {
		a.res.Push = approvePush{Outcome: approvePushFailed, Branch: branch, Err: err}
		a.res.warn("Note: the commit could not be pushed: %v\nTo push it by hand, run: git -C %s push origin %s", err, a.root, branch)
		return
	}
	a.res.Push = approvePush{Outcome: approvePushed, Branch: branch}
	a.res.say("Pushed the commit to origin (%s).", branch)
}
