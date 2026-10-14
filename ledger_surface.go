package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/dphaener/afk/internal/ledger"
)

// ledgerSetOutcome is what one set of task statuses ended as. The dashboard
// and the command both read it, so neither has to parse Text.
type ledgerSetOutcome int

const (
	// ledgerChanged: at least one task now has the status it did not have.
	ledgerChanged ledgerSetOutcome = iota
	// ledgerAlready: every task already had the status; nothing was written
	// that changes the ledger.
	ledgerAlready
	// ledgerRefused: the request cannot be done as asked (a mistake in the
	// words, or an id that is not a task in the plan). The ledger is untouched.
	ledgerRefused
	// ledgerFailed: the plan could not be read or the ledger could not be
	// written. Text names the file.
	ledgerFailed
)

// ledgerChange is one task's part of a set: its status in the file when the
// lock was held (not a caller's guess), and what it was set to.
type ledgerChange struct {
	ID         string
	Old, New   ledger.Status
	Changed    bool
	ReplacedBy []string
}

// ledgerSetResult is the outcome of one set. Text is the plain sentence(s) the
// command prints, without the "afk ledger set: " prefix, so the dashboard can
// show it as is. Code is the command's exit code; ToStderr says which stream
// the command prints Text on.
type ledgerSetResult struct {
	Outcome  ledgerSetOutcome
	Changes  []ledgerChange
	Text     string
	Code     int
	ToStderr bool
}

// ledgerSetRefusal checks the rules that depend only on the words typed, in the
// order the command has always checked them. runLedgerSet runs it before it
// resolves the project, so a command with two mistakes still reports the same
// one it did before this was split out; setLedgerStatusForSurface runs it too,
// so the dashboard cannot skip it. It returns nil when the words are fine.
func ledgerSetRefusal(ids []string, st ledger.Status, repl []string, replacedGiven bool) *ledgerSetResult {
	refuse := func(format string, a ...any) *ledgerSetResult {
		return &ledgerSetResult{Outcome: ledgerRefused, Code: 2, ToStderr: true, Text: fmt.Sprintf(format, a...)}
	}
	if replacedGiven && st != ledger.Superseded {
		return refuse("--replaced-by only goes with the status superseded, not %s. Drop --replaced-by, or use superseded.", st)
	}
	if st == ledger.Superseded && len(repl) == 0 {
		return refuse("a superseded task needs the tasks that replace it. Add --replaced-by with their ids, for example --replaced-by T4.2a,T4.2b.")
	}
	if replacedGiven && len(ids) > 1 {
		return refuse("--replaced-by names the tasks that replace one task, so it cannot be used with %d task ids. Run the command once for each superseded task.", len(ids))
	}
	for _, id := range ids {
		if slices.Contains(repl, id) {
			return refuse("%s cannot be replaced by itself. Name the other tasks in --replaced-by.", id)
		}
	}
	return nil
}

// setLedgerStatusForSurface is `afk ledger set` after its arguments are parsed
// and the project root is resolved. It is shared by the command and the
// dashboard so both make the same checks and the same write, and it prints
// nothing.
//
// Every id is checked against the plan before the ledger is touched, because a
// typo written to the ledger would sit there as an entry for no task and the
// real task would stay pending. Old statuses are read inside ledger.Update's
// lock, so a daemon write between the caller's last look and this call is
// reported as what it was, not as what the caller expected.
func setLedgerStatusForSurface(root string, ids []string, st ledger.Status, replacedBy []string, replacedGiven bool) ledgerSetResult {
	ids = dedupe(ids)
	if r := ledgerSetRefusal(ids, st, replacedBy, replacedGiven); r != nil {
		return *r
	}

	state, err := loadBuildState(root)
	if err != nil {
		return ledgerSetResult{Outcome: ledgerFailed, Code: 1, ToStderr: true, Text: err.Error()}
	}
	inPlan := make(map[string]bool, len(state.Tasks))
	for _, t := range state.Tasks {
		inPlan[t.ID] = true
	}
	for _, want := range append(slices.Clone(ids), replacedBy...) {
		if !inPlan[want] {
			return ledgerSetResult{Outcome: ledgerRefused, Code: 1, ToStderr: true,
				Text: fmt.Sprintf("%s is not a task in %s.\nRun \"afk tasks\" to see the task ids.", want, state.PlanPath)}
		}
	}

	olds := make([]ledger.Status, len(ids))
	changed := make([]bool, len(ids))
	err = ledger.Update(root, func(l *ledger.Ledger) error {
		for i, id := range ids {
			olds[i] = l.Status(id)
			var serr error
			if changed[i], serr = l.Set(id, st, replacedBy); serr != nil {
				return serr
			}
		}
		return nil
	})
	if err != nil {
		return ledgerSetResult{Outcome: ledgerFailed, Code: 1, ToStderr: true,
			Text: fmt.Sprintf("could not update %s: %v\nNothing was changed. Fix the problem above and run the command again.", ledger.Path(root), err)}
	}

	res := ledgerSetResult{Outcome: ledgerAlready}
	var lines []string
	for i, id := range ids {
		c := ledgerChange{ID: id, Old: olds[i], New: st, Changed: changed[i]}
		if !changed[i] {
			res.Changes = append(res.Changes, c)
			lines = append(lines, fmt.Sprintf("%s is already %s", id, st))
			continue
		}
		res.Outcome = ledgerChanged
		line := fmt.Sprintf("%s: %s → %s", id, olds[i], st)
		if st == ledger.Superseded {
			c.ReplacedBy = dedupe(replacedBy)
			line += " (replaced by " + strings.Join(c.ReplacedBy, ", ") + ")"
		}
		res.Changes = append(res.Changes, c)
		lines = append(lines, line)
	}
	res.Text = strings.Join(lines, "\n")
	return res
}

// printLedgerSetResult prints a result the way the command always has: success
// lines on stdout as they are, every refusal on stderr behind "afk ledger set:
// ", and the Usage line after a Code 2 refusal.
func printLedgerSetResult(stdout, stderr io.Writer, res ledgerSetResult) {
	if !res.ToStderr {
		fmt.Fprintln(stdout, res.Text)
		return
	}
	fmt.Fprintf(stderr, "afk ledger set: %s\n", res.Text)
	if res.Code == 2 {
		fmt.Fprintln(stderr, "Usage: "+ledgerSetUsageLine)
	}
}
