package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/nexttask"
)

const tasksUsageLine = "afk tasks [--project DIR]"

func printTasksUsage(w io.Writer) {
	fmt.Fprintf(w, `afk tasks — list the master plan's tasks and where each one stands

Usage:
  %s

Prints one row per task in plan order: its id, title, status from the task
ledger (pending when nothing is recorded) and the tasks it still waits for.

Exit codes: 0 listed, 1 the plan, the ledger or afk.toml could not be used,
2 the command was written wrongly.
`, tasksUsageLine)
}

// cmdTasks implements `afk tasks`.
func cmdTasks(args []string) {
	// A failed Getwd or UserHomeDir is passed on as "": resolveProject only
	// needs them when --project is absent or starts with ~, and it reports
	// the case it cannot resolve.
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	os.Exit(runTasks(args, os.Stdout, os.Stderr, cwd, home, resolveGitCommonDirIn))
}

// loadBuildForCommand is the argument handling and loading that afk tasks and
// afk next share: parse only --project, refuse any word, resolve the main
// checkout and load the build state. proceed is false when the command is
// over, and code is then its exit code (0 for --help, 1 for a plan, ledger or
// config error, 2 for bad usage).
func loadBuildForCommand(name, usageLine string, printUsage func(io.Writer), args []string, stderr io.Writer, cwd, home string, gitCommonDir func(string) string) (state buildState, code int, proceed bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := addProjectFlag(fs)
	fs.Usage = func() { printUsage(stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return buildState{}, 0, false
		}
		return buildState{}, 2, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected word %q. This command takes no arguments, only --project.\nUsage: %s\n", name, fs.Arg(0), usageLine)
		return buildState{}, 2, false
	}
	root, err := resolveBuildRoot(*proj, cwd, home, gitCommonDir)
	if err != nil {
		fmt.Fprintf(stderr, "%s: cannot tell which project to use. Pass --project with the folder that holds the master plan.\nUsage: %s\n", name, usageLine)
		return buildState{}, 2, false
	}
	state, err = loadBuildState(root)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return buildState{}, 1, false
	}
	return state, 0, true
}

// runTasks implements `afk tasks` and returns the exit code: 0 after the table
// is printed, 1 when the plan, ledger or afk.toml cannot be used, 2 for a
// command line that could never work.
//
// The unmet column comes from nexttask.Unmet, the same rule afk next applies,
// so the table explains why a task is not the one afk next names. A done or
// superseded task shows "-" because what it once waited for no longer matters.
func runTasks(args []string, stdout, stderr io.Writer, cwd, home string, gitCommonDir func(string) string) int {
	state, code, proceed := loadBuildForCommand("afk tasks", tasksUsageLine, printTasksUsage, args, stderr, cwd, home, gitCommonDir)
	if !proceed {
		return code
	}

	tw := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTITLE\tSTATUS\tUNMET DEPENDENCIES")
	for _, t := range state.Tasks {
		st := state.Ledger.Status(t.ID)
		cell := string(st)
		unmet := "-"
		switch st {
		case ledger.Superseded:
			if e, ok := state.Ledger.Get(t.ID); ok && len(e.ReplacedBy) > 0 {
				cell = fmt.Sprintf("superseded (by %s)", strings.Join(e.ReplacedBy, ", "))
			}
		case ledger.Done:
		default:
			if u := nexttask.Unmet(t, state.Ledger); len(u) > 0 {
				unmet = strings.Join(u, ", ")
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.ID, t.Title, cell, unmet)
	}
	tw.Flush()

	inPlan := make(map[string]bool, len(state.Tasks))
	for _, t := range state.Tasks {
		inPlan[t.ID] = true
		if t.Note != "" {
			fmt.Fprintf(stdout, "Note: %s depends on %q, which names no task ids, so afk treats it as having no dependencies.\n", t.ID, t.Note)
		}
	}
	for _, e := range state.Ledger.Tasks {
		if !inPlan[e.ID] {
			fmt.Fprintf(stdout, "Note: the ledger has an entry for %s, which is not in the plan.\n", e.ID)
		}
	}
	return 0
}
