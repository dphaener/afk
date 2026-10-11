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
	default:
		fmt.Fprintf(os.Stderr, "afk ledger: unknown subcommand %q\n", args[0])
		printLedgerUsage(os.Stderr)
		os.Exit(2)
	}
}

const ledgerSetUsageLine = "afk ledger set [--project DIR] [--replaced-by ID,ID] <task-id> <status>"

func printLedgerUsage(w io.Writer) {
	fmt.Fprintf(w, `afk ledger — record where a master-plan task stands

Usage:
  %s

Sets one task's status in .afk/build/ledger.json in the project's main checkout.
<status> is one of pending, running, shipping, parked, done, failed, superseded.
A task with no entry is pending. Only the status (and, for superseded, the
replacing tasks) is written; everything else recorded for the task is kept.

  superseded needs --replaced-by with the task ids that take its place,
  for example: afk ledger set T4.2 superseded --replaced-by T4.2a,T4.2b

Exit codes: 0 done (or nothing to do), 1 the plan, the ledger or a task was not
found or the ledger could not be written, 2 the command was written wrongly.
`, ledgerSetUsageLine)
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

	if len(words) != 2 {
		return usage("expected a task id and a status, but got %d word(s).", len(words))
	}
	id := words[0]
	st, err := ledger.ParseStatus(words[1])
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
	if replacedGiven && st != ledger.Superseded {
		return usage("--replaced-by only goes with the status superseded, not %s. Drop --replaced-by, or use superseded.", st)
	}
	if st == ledger.Superseded && len(repl) == 0 {
		return usage("a superseded task needs the tasks that replace it. Add --replaced-by with their ids, for example --replaced-by T4.2a,T4.2b.")
	}
	if slices.Contains(repl, id) {
		return usage("%s cannot be replaced by itself. Name the other tasks in --replaced-by.", id)
	}

	root, err := resolveBuildRoot(*proj, cwd, home, gitCommonDir)
	if err != nil {
		return usage("cannot tell which project to use. Pass --project with the folder that holds the master plan.")
	}
	state, err := loadBuildState(root)
	if err != nil {
		fmt.Fprintf(stderr, "afk ledger set: %v\n", err)
		return 1
	}

	inPlan := make(map[string]bool, len(state.Tasks))
	for _, t := range state.Tasks {
		inPlan[t.ID] = true
	}
	for _, want := range append([]string{id}, repl...) {
		if !inPlan[want] {
			fmt.Fprintf(stderr, "afk ledger set: %s is not a task in %s.\nRun \"afk tasks\" to see the task ids.\n", want, state.PlanPath)
			return 1
		}
	}

	var old ledger.Status
	var changed bool
	err = ledger.Update(root, func(l *ledger.Ledger) error {
		old = l.Status(id)
		var serr error
		changed, serr = l.Set(id, st, repl)
		return serr
	})
	if err != nil {
		fmt.Fprintf(stderr, "afk ledger set: could not update %s: %v\nNothing was changed. Fix the problem above and run the command again.\n", ledger.Path(root), err)
		return 1
	}

	if !changed {
		fmt.Fprintf(stdout, "%s is already %s\n", id, st)
		return 0
	}
	line := fmt.Sprintf("%s: %s → %s", id, old, st)
	if st == ledger.Superseded {
		line += " (replaced by " + strings.Join(dedupe(repl), ", ") + ")"
	}
	fmt.Fprintln(stdout, line)
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
