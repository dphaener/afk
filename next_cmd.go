package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/nexttask"
)

const nextUsageLine = "afk next [--project DIR]"

func printNextUsage(w io.Writer) {
	fmt.Fprintf(w, `afk next — name the master-plan task that should start next

Usage:
  %s

Prints one line: the task id and title, or the reason nothing can start.
Nothing is started or recorded.

Exit codes: 0 a line was printed (a task or a reason), 1 the plan, the ledger
or afk.toml could not be used, 2 the command was written wrongly.
`, nextUsageLine)
}

// cmdNext implements `afk next`.
func cmdNext(args []string) {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	os.Exit(runNext(args, os.Stdout, os.Stderr, cwd, home, resolveGitCommonDirIn))
}

// runNext implements `afk next` and returns the exit code. Having nothing to
// start is a normal answer, not a failure, so every decision exits 0; only an
// unusable plan, ledger or config exits 1.
//
// The open-inbox filter comes from the project's real inbox (openItemsFunc),
// so a parked task whose items are all answered is the next task. An inbox
// that cannot be listed gives nil, which nexttask.Next reads as "every item
// is open": the task stays parked rather than starting on a guess.
func runNext(args []string, stdout, stderr io.Writer, cwd, home string, gitCommonDir func(string) string) int {
	state, code, proceed := loadBuildForCommand("afk next", nextUsageLine, printNextUsage, args, stderr, cwd, home, gitCommonDir)
	if !proceed {
		return code
	}
	d := nexttask.Next(state.Tasks, state.Ledger, openItemsFunc(inbox.Store{Dir: inbox.Dir(state.Root)}))
	fmt.Fprintln(stdout, nextLine(d, state.Tasks, state.Ledger))
	return 0
}

// openItemsFunc builds nexttask's open-items question from an inbox. It returns
// nil when the inbox cannot be listed. An id the inbox does not hold counts as
// open: an item that was deleted or cannot be read has not been answered, and
// releasing a parked task on missing evidence would start work the owner never
// approved. Unreadable files are not listed, so they land in that same case.
func openItemsFunc(store inbox.Store) func(string) bool {
	items, _, err := store.List()
	if err != nil {
		return nil
	}
	answered := make(map[string]bool, len(items))
	for _, it := range items {
		if !it.Open() {
			answered[it.ID] = true
		}
	}
	return func(id string) bool { return !answered[id] }
}

// nextLine turns a decision into the single line afk next prints. It is pure
// so every reason is a table row.
func nextLine(d nexttask.Decision, tasks []masterplan.Task, l ledger.Ledger) string {
	if d.Task != "" {
		title := ""
		for _, t := range tasks {
			if t.ID == d.Task {
				title = t.Title
				break
			}
		}
		return d.Task + "  " + title
	}
	switch d.Reason {
	case nexttask.ReasonInProgress:
		parts := make([]string, 0, len(d.InProgress))
		for _, id := range d.InProgress {
			parts = append(parts, fmt.Sprintf("%s is already %s", id, l.Status(id)))
		}
		return "Nothing to start: " + strings.Join(parts, "; ") + "."
	case nexttask.ReasonAllDone:
		return "Nothing to start: every task is done."
	case nexttask.ReasonWaitingOnOwner:
		s := "Nothing to start: waiting on you for " + strings.Join(d.Parked, ", ")
		if len(d.WaitingOn) > 0 {
			s += " (inbox items " + strings.Join(d.WaitingOn, ", ") + ")"
		}
		s += "."
		if len(d.Failed) > 0 {
			s += " Also blocked by failed " + strings.Join(d.Failed, ", ") + "."
		}
		return s
	case nexttask.ReasonBlockedByFailed:
		return "Nothing to start: blocked by failed " + strings.Join(d.Failed, ", ") + `. Fix the cause, then run "afk ledger set <id> pending" to try again.`
	case nexttask.ReasonStuck:
		return "Nothing to start: " + strings.Join(d.Stuck, ", ") + ` cannot become ready. Run "afk tasks" to see what each one waits for.`
	}
	return "Nothing to start."
}
