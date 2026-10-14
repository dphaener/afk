// Package taskstatus holds the words of the "set a task's status" action: the
// choice list, the sentence saying what follows from a status, the confirm
// text, the "already" and "type the ids" sentences, and the splitting of the
// replacing ids.
//
// It exists because the dashboard (internal/ui) and the daemon's web page
// (internal/web) must say the same thing to the owner before and after a
// change, and internal/web cannot import internal/ui. The package is pure: it
// reads and writes no file and calls no seam; the write itself is always
// `afk ledger set`'s code.
package taskstatus

import (
	"strings"
	"unicode"

	"github.com/dphaener/afk/internal/ledger"
)

// Choices is the list of statuses to pick from: every status the ledger
// accepts, in lifecycle order, as `afk ledger set` takes them.
func Choices() []string {
	sts := ledger.Statuses()
	out := make([]string, len(sts))
	for i, s := range sts {
		out[i] = string(s)
	}
	return out
}

// SplitIDs is the replacing-ids text as ids: split on commas and white space,
// empty pieces dropped. The ids are not checked here; the entry point checks
// them against the plan and says which one is wrong.
func SplitIDs(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

// Consequence is what follows from the new status, in one sentence, so the
// owner reads what the build loop will do before confirming. An unknown status
// gets no sentence rather than a guess.
func Consequence(status string) string {
	switch ledger.Status(status) {
	case ledger.Pending:
		return "The build loop may start it on its next pass."
	case ledger.Done:
		return "Tasks that depend on it may start."
	case ledger.Failed:
		return "The build loop will not start it again until you set it back to pending."
	case ledger.Parked:
		return "The build loop will wait until its inbox items are answered."
	case ledger.Superseded:
		return "Tasks that depend on it will wait for the tasks that replace it."
	case ledger.Running, ledger.Shipping:
		return "The build loop will treat it as having a session; use this only to repair the ledger."
	}
	return ""
}

// Working reports whether the old status means an agent is working on the
// task: the ledger is only a record, so changing it does not stop that agent.
func Working(old string) bool {
	return old == string(ledger.Running) || old == string(ledger.Shipping)
}

// ConfirmText is the confirm's words, one sentence to a line: when the old
// status is running or shipping, first that an agent is working on the task
// and the change does not stop it (the owner might think otherwise); then the
// change itself; then what follows from it.
func ConfirmText(id, old, new string, replacedBy []string) string {
	var lines []string
	if Working(old) {
		lines = append(lines, "An agent is working on "+id+" now; changing its status does not stop it.")
	}
	change := id + ": " + old + " → " + new
	if len(replacedBy) > 0 {
		change += " (replaced by " + strings.Join(replacedBy, ", ") + ")"
	}
	lines = append(lines, change+".")
	if c := Consequence(new); c != "" {
		lines = append(lines, c)
	}
	return strings.Join(lines, "\n")
}

// Already is the answer when the current status is chosen: nothing would
// change, so nothing is called.
func Already(id, status string) string {
	return id + " is already " + status + "."
}

// EmptyIDsPrompt is the answer to a blank replacing-ids field. The step stays
// open, so the owner can type the ids and try again.
func EmptyIDsPrompt(id string) string {
	return "Type the ids of the tasks that replace " + id + ", for example " + id + "a, " + id + "b."
}

// ActionLabel is the confirm button's label, such as "Mark done".
func ActionLabel(status string) string {
	return "Mark " + status
}
