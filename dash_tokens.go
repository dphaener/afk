package main

import "github.com/dphaener/afk/internal/ui"

// dashLoadTaskTokens is the dashboard's Deps.LoadTaskTokens: one task's token
// record, read through taskTokensForSurface, the entry point `afk tokens
// <task>` prints from, so the overlay and the command cannot disagree.
//
// It reads only the token ledger. It never reads a transcript and never
// records, because the dashboard calls it on every refresh and a read that
// wrote anything would turn each tick into a side effect. The dashboard's root
// is already the project's main checkout, where the ledger lives, so no
// further lookup is needed.
//
// The three states map one to one; "no ledger yet" and "nothing for this task"
// are both ui.TaskTokensNone, since the overlay says the same thing for both.
func dashLoadTaskTokens(root, task string) ui.TaskTokens {
	res := taskTokensForSurface(root, task)
	out := ui.TaskTokens{Task: res.Task, Path: res.Path}
	switch res.State {
	case tokensRecorded:
		out.State = ui.TaskTokensRecorded
		out.Short = res.Short
		out.Lines = res.Lines
		out.Compact = res.Compact
	case tokensUnreadable:
		out.State = ui.TaskTokensUnreadable
		out.Err = res.Err
	default:
		out.State = ui.TaskTokensNone
	}
	return out
}
