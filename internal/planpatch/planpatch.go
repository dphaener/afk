// Package planpatch changes a master plan by a guarded patch: an old text
// that must be in the plan exactly once, and the new text that replaces it.
// It implements the afk plan patch command of docs/PLAN.md T5.4 and rule 8
// of its M5 introduction (an id is never renumbered or reused, and a task
// that is running or shipping only ever gains an Amended line).
//
// Apply is pure. It reads no file, runs no git and knows nothing about the
// ledger: the caller reads the plan and turns the ledger into Held, and the
// executor in run.go writes what Apply returns. Every check (the match
// count, rule 8, the checker) runs on the patched text in memory, before any
// write, because the daemon reads the plan on disk whenever it decides what
// to run. A patch that was written and then undone would leave the plan
// wrong for a moment, and a moment is enough for the daemon to start the
// wrong task. So when Apply returns an error, nothing was changed, and the
// plan on disk was never anything but what the owner last approved.
package planpatch

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
)

// Sentinel errors. Each message says what to do next, because the command
// prints it as is to an owner who typed the patch by hand.
var (
	ErrEmptyOld = errors.New("the old text is empty, so there is nothing to find in the plan. Put the exact text to replace in the old file")
	ErrNoMatch  = errors.New("the old text is not in the plan. Copy the text to replace from the plan exactly as it is there, with the same spaces and line breaks")
	ErrNoChange = errors.New("the old text and the new text are the same, so the patch would change nothing. Put the changed text in the new file")
)

// ManyMatchesError says the old text is in the plan more than once, so the
// patch cannot tell which place to change.
type ManyMatchesError struct {
	Count int
}

func (e *ManyMatchesError) Error() string {
	return fmt.Sprintf("the old text is in the plan %d times, so afk cannot tell which one to change. Add a line before or after it to the old text, so it matches one place only", e.Count)
}

// HeldError says the patch changes a task that is running or shipping, which
// rule 8 allows only by adding an Amended line.
type HeldError struct {
	Task   string
	Status string
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("task %s is %s, so its entry can only have an Amended line added to it. Leave its text as it is and add a line starting \"Amended <date>:\" that says what changed", e.Task, e.Status)
}

// RemovedIDError says the patch would make a task id disappear from the
// plan, which rule 8 never allows, whatever the ledger says.
type RemovedIDError struct {
	Task string
}

func (e *RemovedIDError) Error() string {
	return fmt.Sprintf("the patch removes task %s from the plan. A task id is never removed or renamed: keep its entry, add an Amended line saying it was replaced, and mark it superseded in the ledger", e.Task)
}

// LintError says the patched plan would have a checker error the plan does
// not have now.
type LintError struct {
	// Added holds the new error-level findings, with the line they would be
	// at in the patched plan.
	Added []planlint.Finding
}

func (e *LintError) Error() string {
	var b strings.Builder
	what := "an error"
	if len(e.Added) != 1 {
		what = fmt.Sprintf("%d errors", len(e.Added))
	}
	fmt.Fprintf(&b, "the patch would give the plan %s it does not have now, so nothing was changed. Fix the new text:", what)
	for _, f := range e.Added {
		if f.Line > 0 {
			fmt.Fprintf(&b, "\n  line %d: %s", f.Line, f.Message)
		} else {
			fmt.Fprintf(&b, "\n  %s", f.Message)
		}
	}
	return b.String()
}

// Edit is one guarded patch as text: Old must be in the plan exactly once
// and New replaces it. The builders in edit.go return one, and the executor
// in run.go takes one from the caller's Edit function and hands it to Apply.
type Edit struct {
	Old, New []byte
}

// Request is one guarded patch, as plain values.
type Request struct {
	// Plan is the plan as read.
	Plan []byte
	// Old must occur exactly once in Plan.
	Old []byte
	// New replaces Old.
	New []byte
	// Held maps a task id to its ledger status. Only "running" and
	// "shipping" hold a task (rule 8); any other status is ignored, so a
	// caller may pass the whole ledger and a done task stays editable.
	Held map[string]string
}

// Result is what Apply returns when the patch is accepted.
type Result struct {
	// Plan is the patched plan.
	Plan []byte
	// Touched holds the ids of the tasks whose entries the patch changed,
	// in plan order.
	Touched []string
}

// Statuses that hold a task under rule 8. They are spelled here rather than
// imported from internal/ledger so this package stays free of the ledger,
// whose reading is the caller's job.
const (
	statusRunning  = "running"
	statusShipping = "shipping"
)

var (
	// amendedRE matches an Amended line as plans write them: "Amended
	// 2026-10-13 …", "- Amended 2026-10-13 …" or "- **Amended 2026-10-13
	// (T1.1 done):** …", with any indentation.
	amendedRE = regexp.MustCompile(`^[ \t]*(- )?(\*\*)?Amended \d{4}-\d{2}-\d{2}`)
	// listItemRE matches an indented bullet or numbered item. An indented
	// continuation of an Amended line may not be one: masterplan reads an
	// indented "- **Depends on:**" line as a task's Depends on line, so a
	// "continuation" that is a bullet could change what a running task
	// waits for.
	listItemRE = regexp.MustCompile(`^[ \t]+([-*+]|\d+[.)])[ \t]`)
)

// Apply checks and applies one guarded patch. On any error nothing was
// changed and Result is empty.
//
// The checks run in the order a person would want to hear about them: a
// patch that cannot even be placed is refused before the plan is read for
// tasks, a removed id is refused before the ledger is consulted (it is wrong
// whatever the ledger says), and the checker runs last because its findings
// are about the text that was going to be written.
func Apply(r Request) (Result, error) {
	if len(r.Old) == 0 {
		return Result{}, ErrEmptyOld
	}
	if bytes.Equal(r.Old, r.New) {
		return Result{}, ErrNoChange
	}
	switch n := bytes.Count(r.Plan, r.Old); {
	case n == 0:
		return Result{}, ErrNoMatch
	case n > 1:
		return Result{}, &ManyMatchesError{Count: n}
	}
	patched := bytes.Replace(r.Plan, r.Old, r.New, 1)

	before := masterplan.Scan(r.Plan)
	after := masterplan.Scan(patched)
	ch := diffLines(r.Plan, patched)
	touched := touchedEntries(before, ch)

	if id := removedID(before, after); id != "" {
		return Result{}, &RemovedIDError{Task: id}
	}
	for _, e := range touched {
		status, held := r.Held[e.id]
		if !held || (status != statusRunning && status != statusShipping) {
			continue
		}
		if !amendsOnly(e, ch) {
			return Result{}, &HeldError{Task: e.id, Status: status}
		}
	}
	if added := addedErrors(planlint.Lint(r.Plan), planlint.Lint(patched)); len(added) > 0 {
		return Result{}, &LintError{Added: added}
	}

	var ids []string
	for _, e := range touched {
		ids = append(ids, e.id)
	}
	return Result{Plan: patched, Touched: ids}, nil
}

// change is the patch as lines of the old plan: the removed lines (a
// contiguous range of 1-based line numbers, empty when the patch only
// inserts) and the inserted lines, which go after old line at.
type change struct {
	// at is the 1-based number of the last old line before the change, or
	// 0 when the change starts at the first line.
	at int
	// removedFrom and removedTo bound the removed old lines, inclusive.
	// removedFrom > removedTo means no line was removed.
	removedFrom, removedTo int
	inserted               []string
	// oldLines is every line of the old plan, kept so the entry rules can
	// read the plan's text by line number without a second split.
	oldLines []string
}

func (c change) insertOnly() bool { return c.removedFrom > c.removedTo }

// diffLines finds the changed lines by dropping the lines the old and the
// new whole-file text share at the start and at the end. Lines are split on
// "\n" alone, exactly as masterplan.Scan splits them, so a line number here
// is a line number there and a "\r" stays part of its line.
//
// The shared suffix is capped so it cannot reach into the shared prefix.
// The two overlap whenever the text around the change repeats itself: a
// deleted entry whose neighbours share their Acceptance line, or an inserted
// entry whose last lines repeat the lines just above it. Uncapped, the
// suffix would claim lines the prefix already has, the removed range would
// run backwards and the inserted slice would end before it starts.
func diffLines(oldText, newText []byte) change {
	oldLines := strings.Split(string(oldText), "\n")
	newLines := strings.Split(string(newText), "\n")
	p := 0
	for p < len(oldLines) && p < len(newLines) && oldLines[p] == newLines[p] {
		p++
	}
	s := 0
	for s < len(oldLines)-p && s < len(newLines)-p && oldLines[len(oldLines)-1-s] == newLines[len(newLines)-1-s] {
		s++
	}
	return change{
		at:          p,
		removedFrom: p + 1,
		removedTo:   len(oldLines) - s,
		inserted:    newLines[p : len(newLines)-s],
		oldLines:    oldLines,
	}
}

// entry is one task's part of the plan: its heading line through the line
// before the next heading of level 1 to 4 outside a fence, or the end of
// the file. Lines are 1-based and inclusive.
type entry struct {
	id         string
	start, end int
	// lines is every line of the old plan, so a rule can look at the
	// entry's text without a second split.
	lines []string
}

// entries lists every task's entry from the scan, in plan order.
func entries(o masterplan.Outline, lines []string) []entry {
	var es []entry
	for _, t := range o.Tasks {
		end := len(lines)
		for _, h := range o.Headings {
			if h.Line > t.Line {
				end = h.Line - 1
				break
			}
		}
		es = append(es, entry{id: t.ID, start: t.Line, end: end, lines: lines})
	}
	return es
}

// touchedEntries returns the entries the patch changed, in plan order. A
// task is touched when a removed line lies in its entry, or when the patch
// only inserts lines and the line before the insertion point lies in its
// entry.
//
// The one exemption is new text that opens with a heading: it belongs to
// that heading, not to the task above it, which is how a new task is put
// directly after a running one. The exemption holds only when no non-blank
// line of the entry follows the insertion point. A heading inserted in the
// middle of an entry moves every line after it under the new heading, so
// the task above loses its Scope or Acceptance without a line of its own
// being removed, and rule 8 must still see that as a change to it.
func touchedEntries(o masterplan.Outline, ch change) []entry {
	var touched []entry
	for _, e := range entries(o, ch.oldLines) {
		if !ch.insertOnly() {
			if ch.removedFrom <= e.end && ch.removedTo >= e.start {
				touched = append(touched, e)
			}
			continue
		}
		if ch.at < e.start || ch.at > e.end {
			continue
		}
		if ch.opensWithHeading() && !e.hasTextAfter(ch.at) {
			continue
		}
		touched = append(touched, e)
	}
	return touched
}

// opensWithHeading reports whether the first non-blank inserted line is a
// heading of level 1 to 4.
func (c change) opensWithHeading() bool {
	for _, line := range c.inserted {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return isHeading(line)
	}
	return false
}

// hasTextAfter reports whether the entry has a non-blank line after old line
// n (1-based).
func (e entry) hasTextAfter(n int) bool {
	for i := n; i < e.end; i++ {
		if strings.TrimSpace(e.lines[i]) != "" {
			return true
		}
	}
	return false
}

// removedID returns the first task id, in plan order, that the old plan has
// and the patched plan does not, or "".
func removedID(before, after masterplan.Outline) string {
	kept := make(map[string]bool, len(after.Tasks))
	for _, t := range after.Tasks {
		kept[t.ID] = true
	}
	for _, t := range before.Tasks {
		if !kept[t.ID] {
			return t.ID
		}
	}
	return ""
}

// amendsOnly reports whether the change leaves a held task's entry as rule 8
// allows: no line of the entry removed, and every non-blank inserted line an
// Amended line or an indented continuation of one. Blank lines are passed
// over because an Amended line is usually added with one before it.
func amendsOnly(e entry, ch change) bool {
	if !ch.insertOnly() && ch.removedFrom <= e.end && ch.removedTo >= e.start {
		return false
	}
	inAmended := false
	for _, line := range ch.inserted {
		switch {
		case strings.TrimSpace(line) == "":
		case amendedRE.MatchString(line):
			inAmended = true
		case inAmended && (line[0] == ' ' || line[0] == '\t') && !listItemRE.MatchString(line):
		default:
			return false
		}
	}
	return true
}

// isHeading reports whether line is a heading of level 1 to 4 as the daemon
// reads one. It asks masterplan's own scan rather than keeping a copy of its
// heading pattern, so the two can never disagree about what a heading is.
func isHeading(line string) bool {
	return len(masterplan.Scan([]byte(line)).Headings) == 1
}

// errorKey is the identity of an error-level finding for the before-and-after
// comparison: rule, task and message, never the line, because a patch above
// a finding moves it without changing it.
//
// A duplicate-task finding is compared on rule and task alone. Its message
// names the line of the first definition ("T1.1 is already a task on line
// 40"), so a patch that inserts a line above that definition would change
// the message of an error the plan already had, and the comparison would
// wrongly call it new. It is the only error rule whose message holds a line
// number; the others quote the offending text or name ids.
func errorKey(f planlint.Finding) string {
	if f.Rule == planlint.RuleDuplicateTask {
		return f.Rule + "\x00" + f.Task
	}
	return f.Rule + "\x00" + f.Task + "\x00" + f.Message
}

// addedErrors returns the error-level findings of after that before does not
// have, as a multiset: two unknown dependencies of one task on the same id
// are two findings, and the patch must not add a third.
func addedErrors(before, after planlint.Report) []planlint.Finding {
	have := map[string]int{}
	for _, f := range before.Findings {
		if f.Level == planlint.LevelError {
			have[errorKey(f)]++
		}
	}
	var added []planlint.Finding
	for _, f := range after.Findings {
		if f.Level != planlint.LevelError {
			continue
		}
		k := errorKey(f)
		if have[k] > 0 {
			have[k]--
			continue
		}
		added = append(added, f)
	}
	return added
}
