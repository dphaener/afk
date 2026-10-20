package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/plancheck"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
)

// cmdPlan implements `afk plan` and its subcommands. With no word (or only
// flags) it is the planning session; routePlanArgs holds that rule.
func cmdPlan(args []string) {
	switch routePlanArgs(args) {
	case planRouteHelp:
		printPlanUsage(os.Stderr)
	case planRouteSession:
		os.Exit(runPlanSession(args, os.Stdout, os.Stderr, realPlanDeps()))
	case planRouteStatus:
		os.Exit(runPlanStatus(args[1:], os.Stdout, os.Stderr, realPlanDeps()))
	case planRouteAdvance:
		os.Exit(runPlanAdvance(args[1:], os.Stdout, os.Stderr, realPlanDeps()))
	case planRouteApprove:
		os.Exit(runPlanApprove(args[1:], os.Stdout, os.Stderr, realApproveDeps()))
	case planRouteMode:
		// The word is the mode: extend, amend, reconcile or fix.
		os.Exit(runPlanMode(args[0], args[1:], os.Stdout, os.Stderr, realPlanDeps()))
	case planRoutePatch:
		os.Exit(runPlanPatch(args[1:], os.Stdout, os.Stderr, realPlanPatchDeps()))
	case planRouteInsert:
		os.Exit(runPlanInsert(args[1:], os.Stdout, os.Stderr, realPlanPatchDeps()))
	case planRouteDepend:
		os.Exit(runPlanDepend(args[1:], os.Stdout, os.Stderr, realPlanPatchDeps()))
	case planRouteNextID:
		os.Exit(runPlanNextID(args[1:], os.Stdout, os.Stderr, realPlanPatchDeps()))
	case planRouteNextFollowup:
		os.Exit(runPlanNextFollowup(args[1:], os.Stdout, os.Stderr, realPlanPatchDeps()))
	case planRouteCheck:
		// A failed Getwd or UserHomeDir is passed on as "": resolveProject
		// only needs them when --project is absent or starts with ~, and it
		// reports the case it cannot resolve.
		cwd, _ := os.Getwd()
		home, _ := os.UserHomeDir()
		os.Exit(runPlanCheck(args[1:], os.Stdout, os.Stderr, cwd, home, time.Now))
	case planRouteLint:
		cwd, _ := os.Getwd()
		home, _ := os.UserHomeDir()
		os.Exit(runPlanLint(args[1:], os.Stdout, os.Stderr, cwd, home, resolveGitCommonDirIn))
	default:
		fmt.Fprintf(os.Stderr, "afk plan: unknown subcommand %q\n", args[0])
		printPlanUsage(os.Stderr)
		os.Exit(2)
	}
}

func printPlanUsage(w io.Writer) {
	fmt.Fprintf(w, `afk plan — plan a project with your agent, change or check a master plan, or
record a task's outcome in a session's plan

Usage:
  %s
  %s
  %s
  %s
  %s
  %s
  %s
  %s
  %s
  %s
  %s
  %s
  %s
  afk plan lint [--project DIR] [--json] [--summary] [FILE]
  afk plan check [--project DIR] [--reason TEXT] <session> <task-id> done|failed

With no word, afk plan starts a planning session in the project folder (the
current folder, or --project DIR) and opens it in this terminal. The session
asks what you want to build, asks its questions in rounds and writes a first
draft of the master plan. It needs a terminal, because it asks you questions.
A folder that is not a git repository yet is turned into one. Leave with
ctrl-\ and run afk plan again to come back: the session keeps running, and
what it has saved is kept in .afk/plan/. On a project that already has a plan
and no planning session open, afk plan asks which of the four changes below
you want; without a terminal it lists the four commands and exits 1.
  --provider NAME  the agent to run (%s); planning sessions
                   work with the default agent only for now
  --dry-run        print what would happen and change nothing
Exit codes: 0 you left the session (or the dry run printed), 1 the session
could not be started here, 2 the command was written wrongly, 10 the session
was opened somewhere else, 11 the session ended, 12 it is not running.

extend, amend, reconcile and fix start a planning session that changes the plan the
project already has. They take --project, --provider and --dry-run and have the
exit codes of afk plan. Each needs a terminal, refuses a project with no plan
(run afk plan first), and refuses while another planning session is open (run
afk plan to go back to it). The files of an earlier, finished session are
moved to .afk/plan/archive/ first, so nothing is overwritten.
  extend     adds a milestone. The session asks what it is for.
  amend      adds, splits or replaces one task. Say what to change in quotes.
             --yes  the session makes the change without asking you to
                    confirm it first
  reconcile  brings the plan in line with a document, such as a design
             system. FILE must be a file inside the project folder.
  fix        finds the cause of a bug and adds one task that fixes it, to the
             plan's Fixes milestone. Say what is broken in quotes.
             --yes  the session adds the task without asking you to confirm it

status prints the planning session's mode, its phase and what it is waiting
for. It only reads. Exit codes: 0 printed, 1 one of the session's files is
damaged, 2 the command was written wrongly.

advance moves the session on to the next phase when the current phase's work
is saved, and says what is missing when it is not. The planning session runs
it itself; you rarely need to. Exit codes: 0 moved, 1 refused, 2 the command
was written wrongly.

approve is your "go" for a plan you have read. It works only when the planning
session of a new plan is at the review phase and afk plan lint reports no
error; otherwise it says why and changes nothing. It then writes afk.toml when
the project has none, commits the plan and the project's documents (the plan,
afk.toml, .gitignore, CONTEXT.md, the design brief and the agent instruction
files; never anything else), creates the task ledger, adds the project to the
projects afk daemon builds, and marks the session approved. The commit is
pushed when the repository has an origin; with via_pr = true under [planning]
in afk.toml it is not pushed and goes out with the first task's pull request.
A running afk daemon starts the first task within a few seconds. Running it
again is safe: a step already done is not repeated, and a run that stopped
half way is finished by running it again. Exit codes: 0 approved (or already
approved), 1 refused or a step failed, 2 the command was written wrongly.

patch, insert and depend change the master plan (the plan named in afk.toml,
docs/PLAN.md by default) by one guarded patch each. The planning session uses
them, and you can too.
  patch    replaces the text in the --old file with the text in the --new
           file. The old text must be in the plan exactly once.
  insert   adds the task in the --entry file directly after task ID
           (--after ID) or at the end of milestone N (--milestone N), or adds
           the whole milestone in the file after the last one
           (--new-milestone). Give exactly one of the three.
  depend   makes TASK wait for ON, by adding ON to its Depends on line.
  --message TEXT  the commit subject, one line (a default is used without it)
Nothing is written when the change would give the plan an error afk plan lint
does not report now, when it would remove or rename a task id, or when it
changes a task that is running or shipping by more than a line starting
"Amended <date>:". The change is then committed, the plan file alone, and
pushed when the repository has an origin. A plan that is not committed yet, or
a folder that is not a git repository, is changed in place and not committed.
With via_pr = true under [planning] in afk.toml the change is made on the
branch afk-plan-changes and a pull request is opened; your plan file changes
when it is merged. Exit codes: 0 changed, 1 refused or failed (the last line
is "Nothing was changed." when that is so), 2 the command was written wrongly.

next-id prints the id a new task should take, alone on one line: the next
number in milestone N (--milestone N), or the id for a task that must run
directly after task ID (--after ID), such as T2.2a. It only reads. Exit codes:
0 printed, 1 there is no such milestone or task, 2 the command was written
wrongly.

next-followup prints where the next design follow-up prompt goes, alone on one
line: docs/design-followup-<n>.md, with the first number no prompt in docs/
has used yet (a number is never used twice, so a gap is not filled). A
follow-up prompt is what you take back to your design tool when the design
system and the plan disagree; afk plan reconcile writes it. It only reads.
Exit codes: 0 printed, 1 the docs folder could not be read, 2 the command was
written wrongly.

lint reads the master plan (FILE, or the plan named in afk.toml) and lists what
is wrong with it. Errors are what the daemon would refuse or misread, such as
an unknown dependency; warnings are what a reader would miss. --summary prints
only the counts and --json prints everything as JSON. Exit codes: 0 no errors,
1 an error or an unreadable plan, 2 the command was written wrongly.

Ticks one task's checkbox in .afk/plans/<session>.md.
  done     marks the task complete:  - [x] (W1.T2) title
  failed   marks the task failed:    - [!] (W1.T2) title FAILED: reason
           and adds the reason to .afk/<session>/issues.md. Needs --reason.

<task-id> is written W1.T2 or (W1.T2). Running the same command twice changes
nothing the second time. A task already marked failed keeps its first reason.

Exit codes: 0 done (or nothing to do), 1 the plan or the task was not found,
2 the command was written wrongly.
`, planSessionUsageLine, planExtendUsageLine, planAmendUsageLine, planReconcileUsageLine, planFixUsageLine,
		planStatusUsageLine, planAdvanceUsageLine, planApproveUsageLine,
		planPatchUsageLine, planInsertUsageLine, planDependUsageLine, planNextIDUsageLine,
		planNextFollowupUsageLine,
		strings.Join(provider.Names(), ", "))
}

// runPlanCheck implements `afk plan check` and returns the exit code: 0 when
// the task has the requested state (changed now or already), 1 when the plan
// or the task does not exist or the file could not be written, 2 for a
// command line that could never work.
//
// cwd, home and now are parameters so the whole command is testable against a
// temp directory without changing the process's working directory or clock.
//
// Flags are accepted before, between and after the three words. Go's flag
// package stops at the first non-flag argument, which would make
// `afk plan check s W1.T1 failed --reason "..."` treat --reason as a fourth
// word. This command is typed by agents from a prompt, and the natural order
// puts the reason last, so the arguments are parsed in a loop instead.
func runPlanCheck(args []string, stdout, stderr io.Writer, cwd, home string, now func() time.Time) int {
	fs := flag.NewFlagSet("plan check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := addProjectFlag(fs)
	reason := fs.String("reason", "", "Why the task failed (required with 'failed')")
	fs.Usage = func() { printPlanUsage(stderr) }

	var words []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}

	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "afk plan check: "+format+"\n", a...)
		fmt.Fprintln(stderr, "Usage: afk plan check [--project DIR] [--reason TEXT] <session> <task-id> done|failed")
		return 2
	}

	if len(words) != 3 {
		return usage("expected a session name, a task id and done or failed, but got %d word(s).", len(words))
	}
	name, rawID, rawTarget := words[0], words[1], words[2]

	if err := session.ValidateName(name); err != nil {
		return usage("%q is not a session name. Session names use lowercase letters, digits and hyphens.", name)
	}
	id, err := plancheck.NormalizeID(rawID)
	if err != nil {
		return usage("%q is not a task id. Write it like W1.T2.", rawID)
	}
	target, ok := plancheck.ParseTarget(rawTarget)
	if !ok {
		return usage("the last word must be done or failed, not %q.", rawTarget)
	}
	if target == plancheck.Failed && plancheck.CollapseReason(*reason) == "" {
		return usage("marking a task failed needs a reason. Add --reason \"what went wrong\".")
	}

	project, err := resolveProject(*proj, cwd, home)
	if err != nil {
		return usage("cannot tell which project to use. Pass --project with the folder that holds .afk/.")
	}

	res, err := plancheck.Apply(plancheck.Request{
		Project: project,
		Session: name,
		TaskID:  id,
		Target:  target,
		Reason:  *reason,
		Now:     now,
	})
	switch {
	case errors.Is(err, plancheck.ErrNoPlan):
		fmt.Fprintf(stderr, "afk plan check: there is no plan at %s.\nCheck the session name, or pass --project with the folder that holds .afk/.\n", res.PlanPath)
		return 1
	case errors.Is(err, plancheck.ErrUnknownTask):
		fmt.Fprintf(stderr, "afk plan check: the plan at %s has no task %s.\nCheck the task id against the task lines under \"## Waves\" in that file.\n", res.PlanPath, id)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "afk plan check: could not update the plan: %v\nNothing was marked. Fix the problem above and run the command again.\n", err)
		return 1
	}

	switch {
	case !res.Changed && target == plancheck.Failed:
		fmt.Fprintf(stdout, "%s is already marked failed in %s. Its first reason is kept and nothing changed.\n", id, res.PlanPath)
	case !res.Changed:
		fmt.Fprintf(stdout, "%s is already marked done in %s. Nothing changed.\n", id, res.PlanPath)
	case target == plancheck.Failed:
		fmt.Fprintf(stdout, "Marked %s failed in %s and added the reason to %s.\n", id, res.PlanPath, res.IssuesPath)
	default:
		fmt.Fprintf(stdout, "Marked %s done in %s.\n", id, res.PlanPath)
	}
	return 0
}
