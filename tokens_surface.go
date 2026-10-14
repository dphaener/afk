package main

import (
	"bytes"
	"io"
	"strings"

	"github.com/dphaener/afk/internal/tokens"
)

// tokensState is how a task's token record read ended.
type tokensState int

const (
	// tokensRecorded: the ledger holds lines for the task.
	tokensRecorded tokensState = iota
	// tokensNone: the ledger is readable but holds nothing for the task.
	tokensNone
	// tokensUnreadable: the ledger file exists but could not be read.
	tokensUnreadable
)

// taskTokensResult is one task's token record, ready to show. Lines is exactly
// what `afk tokens <task>` prints to stdout, so the dashboard and the command
// cannot drift; Compact is the same rows with fewer columns for a narrow box.
type taskTokensResult struct {
	State tokensState
	Task  string
	// Path is the ledger path, set in every state.
	Path string
	// NoLedger is set with tokensNone when the ledger has no lines at all (no
	// file, or an empty one), as against a ledger that only lacks this task.
	NoLedger bool
	Lines    []string
	Compact  []string
	Weighted float64
	// Short is the abbreviated total with its unit, e.g. "2.9M weighted".
	Short string
	// Err is the reader's own words when State is tokensUnreadable.
	Err string
}

// taskTokensForSurface reads a task's token record from the ledger in the
// project's main checkout. It reads only tokens.jsonl: it never records,
// appends or reads a transcript, so the dashboard can call it on every refresh
// without side effects.
func taskTokensForSurface(root, task string) taskTokensResult {
	path := tokens.LedgerPath(root)
	res := taskTokensResult{Task: task, Path: path}
	all, err := tokens.Read(path)
	if err != nil {
		res.State = tokensUnreadable
		res.Err = err.Error()
		return res
	}
	lines := tokens.ForTask(all, task)
	if len(lines) == 0 {
		res.State = tokensNone
		res.NoLedger = len(all) == 0
		return res
	}
	res.State = tokensRecorded
	for _, l := range lines {
		res.Weighted += l.Weighted
	}
	res.Short = tokens.Short(res.Weighted) + " weighted"
	res.Lines = tableLines(task, lines, tokens.RenderTable)
	res.Compact = tableLines(task, lines, tokens.RenderCompact)
	return res
}

// tableLines is "Task <id>" then a renderer's lines, without a trailing empty
// line. Writing to a bytes.Buffer cannot fail, so the renderer's error is
// dropped.
func tableLines(task string, lines []tokens.Line, render func(io.Writer, []tokens.Line) error) []string {
	var buf bytes.Buffer
	_ = render(&buf, lines)
	return append([]string{"Task " + task}, strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")...)
}
