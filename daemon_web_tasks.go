package main

import (
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/web"
)

// This file holds the two seams the web page's Tasks screens reach the root
// package through. Both are thin: the reading and the writing are
// taskTokensForSurface and setLedgerStatusForSurface, the same entry points
// `afk tokens` and `afk ledger set` print from, so the page cannot disagree
// with either command.

// webTaskTokens is the real version of web.Deps.TaskTokens. It only reads.
//
// A project with no token ledger and a project whose ledger lacks this task
// both read "none": to the page there is nothing to show either way, and the
// difference (NoLedger) only changes the wording the command prints.
func webTaskTokens(root, task string) web.TaskTokens {
	res := taskTokensForSurface(root, task)
	switch res.State {
	case tokensUnreadable:
		return web.TaskTokens{State: web.TokensUnreadable, Err: res.Err}
	case tokensNone:
		return web.TaskTokens{State: web.TokensNone}
	}
	out := web.TaskTokens{
		State: web.TokensRecorded,
		Total: res.Weighted,
		Short: res.Short,
	}
	for _, p := range res.Summary.Phases {
		out.Phases = append(out.Phases, web.TokenPhase{Phase: p.Phase, Weighted: p.Weighted, Share: p.Share})
	}
	for _, r := range res.Summary.Roles {
		out.Roles = append(out.Roles, web.TokenRole{Role: r.Role, Calls: r.Calls, Input: r.Input, Output: r.Output, Weighted: r.Weighted})
	}
	return out
}

// webSetTaskStatus is the real version of web.Deps.SetTaskStatus: `afk ledger
// set` for one task, through the one writer the command and the dashboard
// share (dashSetTaskStatus does the same for the dashboard). Old and New are
// the change's real statuses, read inside the ledger's lock, so the page
// reports what happened and not what it expected.
//
// replacedBy is passed as "given" whenever it is non-empty, which is how the
// command's own rule "--replaced-by only goes with superseded" is applied to a
// request that sends ids with another status.
func webSetTaskStatus(root, id, status string, replacedBy []string) web.TaskStatusResult {
	st, err := ledger.ParseStatus(status)
	if err != nil {
		return web.TaskStatusResult{Outcome: web.StatusRefused, Text: err.Error()}
	}
	res := setLedgerStatusForSurface(root, []string{id}, st, replacedBy, len(replacedBy) > 0)
	out := web.TaskStatusResult{Text: res.Text}
	if len(res.Changes) > 0 {
		out.Old = string(res.Changes[0].Old)
		out.New = string(res.Changes[0].New)
	}
	switch res.Outcome {
	case ledgerChanged:
		out.Outcome = web.StatusChanged
	case ledgerAlready:
		out.Outcome = web.StatusAlready
	case ledgerRefused:
		out.Outcome = web.StatusRefused
	default:
		out.Outcome = web.StatusFailed
	}
	return out
}
