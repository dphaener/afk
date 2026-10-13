package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dphaener/afk/internal/plancheck"
	"github.com/dphaener/afk/internal/session"
)

// cmdPlan implements `afk plan <subcommand>`.
func cmdPlan(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printPlanUsage(os.Stderr)
		if len(args) == 0 {
			os.Exit(2)
		}
		return
	}
	switch args[0] {
	case "check":
		// A failed Getwd or UserHomeDir is passed on as "": resolveProject
		// only needs them when --project is absent or starts with ~, and it
		// reports the case it cannot resolve.
		cwd, _ := os.Getwd()
		home, _ := os.UserHomeDir()
		os.Exit(runPlanCheck(args[1:], os.Stdout, os.Stderr, cwd, home, time.Now))
	case "lint":
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
	fmt.Fprint(w, `afk plan — check a master plan, or record a task's outcome in a session's plan

Usage:
  afk plan lint [--project DIR] [--json] [--summary] [FILE]
  afk plan check [--project DIR] [--reason TEXT] <session> <task-id> done|failed

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
`)
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
