package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/dphaener/afk/internal/ledger"
)

// cmdLedger implements `afk ledger <subcommand>`.
func cmdLedger(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printLedgerUsage(os.Stderr)
		if len(args) == 0 {
			os.Exit(2)
		}
		return
	}
	switch args[0] {
	case "set":
		// A failed Getwd or UserHomeDir is passed on as "": resolveProject
		// only needs them when --project is absent or starts with ~, and it
		// reports the case it cannot resolve.
		cwd, _ := os.Getwd()
		home, _ := os.UserHomeDir()
		os.Exit(runLedgerSet(args[1:], os.Stdout, os.Stderr, cwd, home, resolveGitCommonDirIn))
	case "init":
		cwd, _ := os.Getwd()
		home, _ := os.UserHomeDir()
		os.Exit(runLedgerInit(args[1:], os.Stdout, os.Stderr, cwd, home, resolveGitCommonDirIn))
	default:
		fmt.Fprintf(os.Stderr, "afk ledger: unknown subcommand %q\n", args[0])
		printLedgerUsage(os.Stderr)
		os.Exit(2)
	}
}

const ledgerSetUsageLine = "afk ledger set [--project DIR] [--replaced-by ID,ID] <task-id>... <status>"

const ledgerInitUsageLine = "afk ledger init [--project DIR] [--done-through ID] [--done ID,ID]"

func printLedgerUsage(w io.Writer) {
	fmt.Fprintf(w, `afk ledger — record where a master-plan task stands

Usage:
  %s
  %s

Sets the status of one or more tasks in .afk/build/ledger.json in the project's
main checkout, for example: afk ledger set T0.1 T0.2 T0.3 done
<status> is one of pending, running, shipping, parked, done, failed, superseded.
A task with no entry is pending. Only the status (and, for superseded, the
replacing tasks) is written; everything else recorded for the task is kept.

  superseded needs --replaced-by with the task ids that take its place,
  for example: afk ledger set T4.2 superseded --replaced-by T4.2a,T4.2b
  (--replaced-by takes a single task id before the status, not several).

init creates the ledger for a project that has none, for work that was built
before afk tracked it:
  --done-through ID  marks every task up to and including ID, in plan order, done
  --done ID,ID       marks the listed tasks done (combines with --done-through)
With neither flag it creates an empty ledger. It never changes a ledger that
already exists; use "afk ledger set" for that.

Exit codes: 0 done (or nothing to do), 1 the plan, the ledger or a task was not
found or the ledger could not be written, 2 the command was written wrongly.
`, ledgerSetUsageLine, ledgerInitUsageLine)
}

// runLedgerSet implements `afk ledger set` and returns the exit code: 0 when
// the task has the requested status (changed now or already), 1 when the
// project's plan or ledger cannot be used or a task id is not in the plan, 2
// for a command line that could never work.
//
// Flags are accepted before, between and after the two words, with the same
// parse loop as runPlanCheck: this command is typed by agents from a prompt,
// and Go's flag package would otherwise read a trailing --replaced-by as a
// third word.
//
// Every id is checked against the plan before the ledger is touched, because
// a typo written to the ledger would sit there as an entry for no task and
// the real task would stay pending.
func runLedgerSet(args []string, stdout, stderr io.Writer, cwd, home string, gitCommonDir func(string) string) int {
	fs := flag.NewFlagSet("afk ledger set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := addProjectFlag(fs)
	replacedBy := fs.String("replaced-by", "", "Comma-separated task ids that replace the task (only with superseded)")
	fs.Usage = func() { printLedgerUsage(stderr) }

	var words []string
	rest := args
	replacedGiven := false
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
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "replaced-by" {
			replacedGiven = true
		}
	})

	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "afk ledger set: "+format+"\n", a...)
		fmt.Fprintln(stderr, "Usage: "+ledgerSetUsageLine)
		return 2
	}

	if len(words) < 2 {
		return usage("expected at least one task id and a status, but got %d word(s).", len(words))
	}
	ids := dedupe(words[:len(words)-1])
	st, err := ledger.ParseStatus(words[len(words)-1])
	if err != nil {
		return usage("%v.", err)
	}

	var repl []string
	if replacedGiven {
		for _, r := range strings.Split(*replacedBy, ",") {
			if r = strings.TrimSpace(r); r != "" {
				repl = append(repl, r)
			}
		}
	}
	if r := ledgerSetRefusal(ids, st, repl, replacedGiven); r != nil {
		printLedgerSetResult(stdout, stderr, *r)
		return r.Code
	}

	root, err := resolveBuildRoot(*proj, cwd, home, gitCommonDir)
	if err != nil {
		return usage("cannot tell which project to use. Pass --project with the folder that holds the master plan.")
	}
	res := setLedgerStatusForSurface(root, ids, st, repl, replacedGiven)
	printLedgerSetResult(stdout, stderr, res)
	return res.Code
}

// runLedgerInit implements `afk ledger init` and returns the exit code: 0 when
// the ledger was created, 1 when the plan cannot be used, an id is not in the
// plan, or a ledger already exists, 2 for a command line that could never work.
//
// It refuses an existing ledger rather than merging: init is for a project
// whose earlier work predates the ledger, and a merge could quietly overwrite
// what the daemon has recorded since. Ids are all checked first so a typo
// leaves no ledger behind (an empty ledger would mean "nothing is built").
func runLedgerInit(args []string, stdout, stderr io.Writer, cwd, home string, gitCommonDir func(string) string) int {
	fs := flag.NewFlagSet("afk ledger init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := addProjectFlag(fs)
	doneThrough := fs.String("done-through", "", "Mark every task up to and including this id done")
	doneList := fs.String("done", "", "Comma-separated task ids to mark done")
	fs.Usage = func() { printLedgerUsage(stderr) }

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
	if len(words) > 0 {
		fmt.Fprintf(stderr, "afk ledger init: unexpected word %q. Task ids go after --done-through or --done.\nUsage: %s\n", words[0], ledgerInitUsageLine)
		return 2
	}

	root, err := resolveBuildRoot(*proj, cwd, home, gitCommonDir)
	if err != nil {
		fmt.Fprintf(stderr, "afk ledger init: cannot tell which project to use. Pass --project with the folder that holds the master plan.\nUsage: %s\n", ledgerInitUsageLine)
		return 2
	}
	state, err := loadBuildState(root)
	if err != nil {
		fmt.Fprintf(stderr, "afk ledger init: %v\n", err)
		return 1
	}

	inPlan := make(map[string]bool, len(state.Tasks))
	for _, t := range state.Tasks {
		inPlan[t.ID] = true
	}
	var done []string
	if id := strings.TrimSpace(*doneThrough); id != "" {
		if !inPlan[id] {
			fmt.Fprintf(stderr, "afk ledger init: %s is not a task in %s.\nRun \"afk tasks\" to see the task ids. Nothing was written.\n", id, state.PlanPath)
			return 1
		}
		for _, t := range state.Tasks {
			done = append(done, t.ID)
			if t.ID == id {
				break
			}
		}
	}
	for _, id := range strings.Split(*doneList, ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if !inPlan[id] {
			fmt.Fprintf(stderr, "afk ledger init: %s is not a task in %s.\nRun \"afk tasks\" to see the task ids. Nothing was written.\n", id, state.PlanPath)
			return 1
		}
		done = append(done, id)
	}
	done = dedupe(done)

	if err := ledger.Init(root, done); err != nil {
		if errors.Is(err, ledger.ErrExists) {
			fmt.Fprintf(stderr, "afk ledger init: the ledger already exists at %s, so it was left alone.\nUse \"afk ledger set\" to change it.\n", ledger.Path(root))
			return 1
		}
		fmt.Fprintf(stderr, "afk ledger init: could not create %s: %v\nNothing was written. Fix the problem above and run the command again.\n", ledger.Path(root), err)
		return 1
	}
	fmt.Fprintf(stdout, "Created %s with %d task(s) marked done.\n", ledger.Path(root), len(done))
	return 0
}

// dedupe drops repeated ids, keeping first occurrences, to match what
// ledger.Set stores.
func dedupe(ids []string) []string {
	var out []string
	for _, s := range ids {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
