package main

import (
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/ui"
)

// dashSetTaskStatus is the dashboard's Deps.SetTaskStatus: `afk ledger set`
// for one task, minus the command's argument parsing and printing. It goes
// through setLedgerStatusForSurface, the same entry point the command prints
// from, so the dashboard makes the same plan check and the same locked write
// and never touches the ledger file itself (docs/PLAN.md §2, T7.11 row).
//
// Text passes through as the command words it, without the "afk ledger set: "
// prefix or the usage line, because the dashboard shows it as a notice.
func dashSetTaskStatus(root, id, status string, replacedBy []string) ui.TaskStatusResult {
	st, err := ledger.ParseStatus(status)
	if err != nil {
		return ui.TaskStatusResult{Outcome: ui.TaskStatusRefused, Text: err.Error()}
	}
	res := setLedgerStatusForSurface(root, []string{id}, st, replacedBy, len(replacedBy) > 0)
	out := ui.TaskStatusResult{Text: res.Text}
	switch res.Outcome {
	case ledgerChanged:
		out.Outcome = ui.TaskStatusChanged
	case ledgerAlready:
		out.Outcome = ui.TaskStatusAlready
	case ledgerRefused:
		out.Outcome = ui.TaskStatusRefused
	default:
		out.Outcome = ui.TaskStatusFailed
	}
	return out
}
