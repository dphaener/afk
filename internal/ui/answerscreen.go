// answerscreen.go holds the pure decisions behind the answer screen of
// docs/dash-design.md §4.5: which choices an inbox item offers, what is sent
// for one, which item `a` opens and the line about what happens next.
//
// Nothing here reads the Model, so every branch is table-testable.
package ui

import (
	"strconv"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/inbox"
)

// ownChoiceLabel is the last choice of every item: the way out for an answer
// none of the options says.
const ownChoiceLabel = "Write my own answer"

// answerChoice is one line of the CHOOSE ONE list.
type answerChoice struct {
	Label       string
	Recommended bool
	// Own marks the last choice, the one that opens the text field.
	Own bool
}

// answerChoices lists the item's options in order, then the Own choice. An
// item with no options gives only the Own choice, which is why the screen
// opens straight in the field for it.
func answerChoices(it inbox.Item) []answerChoice {
	out := make([]answerChoice, 0, len(it.Options)+1)
	for _, o := range it.Options {
		out = append(out, answerChoice{Label: o, Recommended: o == it.Recommended})
	}
	return append(out, answerChoice{Label: ownChoiceLabel, Own: true})
}

// answerStartChoice is where the selection begins: the recommended option, so
// that enter alone takes afk's suggestion, else the first choice.
func answerStartChoice(it inbox.Item) int {
	if it.Recommended == "" {
		return 0
	}
	for i, o := range it.Options {
		if o == it.Recommended {
			return i
		}
	}
	return 0
}

// answerTextFor is the text to send for the selected choice, and false when
// there is nothing to send.
//
// An option is sent as its number, the digit string, because the store
// resolves an all-digit in-range answer to that option exactly as
// `afk inbox answer <id> 2` does. Sending the option's own text would be
// re-resolved wrongly when that text is itself a number (an option "3" in
// first place would become option 3). An own answer is sent as typed; a
// blank one is not an answer.
func answerTextFor(choices []answerChoice, idx int, own string) (string, bool) {
	if idx < 0 || idx >= len(choices) {
		return "", false
	}
	if !choices[idx].Own {
		return strconv.Itoa(idx + 1), true
	}
	if strings.TrimSpace(own) == "" {
		return "", false
	}
	return own, true
}

// oldestBlockingItem is the open item that holds a task up and was created
// first, ties broken by id. It scans the whole slice rather than taking the
// first match because the inbox order is the display order, not a promise
// about age.
func oldestBlockingItem(items []inbox.Item) (inbox.Item, bool) {
	var best inbox.Item
	found := false
	for _, it := range items {
		if !it.Open() || !it.Blocking {
			continue
		}
		if !found || answerOlder(it, best) {
			best, found = it, true
		}
	}
	return best, found
}

// answerOlder orders two items by creation time then id. Times are compared
// as times when both parse (so differing offsets still order correctly) and
// as text otherwise.
func answerOlder(a, b inbox.Item) bool {
	ta, errA := time.Parse(time.RFC3339, a.CreatedAt)
	tb, errB := time.Parse(time.RFC3339, b.CreatedAt)
	switch {
	case errA == nil && errB == nil && !ta.Equal(tb):
		return ta.Before(tb)
	case (errA != nil || errB != nil) && a.CreatedAt != b.CreatedAt:
		return a.CreatedAt < b.CreatedAt
	}
	return a.ID < b.ID
}

// answerNextLine says what the owner's answer will set going. An answered
// item shows its answer instead, so it has no line.
func answerNextLine(it inbox.Item) string {
	switch {
	case !it.Open():
		return ""
	case it.Blocking && it.Task != "":
		return it.Task + " starts again within a few seconds of your answer"
	case it.Blocking:
		return "afk carries on within a few seconds of your answer"
	}
	return "Nothing waits on this; afk keeps your answer for the record"
}

// answerItemPosition is the 1-based place of id among the open items, in the
// order given, and how many open items there are. Answered items are not
// walked by the up and down keys, so they do not count. It is 0, 0 when id is
// not among them.
func answerItemPosition(items []inbox.Item, id string) (pos, total int) {
	for _, it := range items {
		if !it.Open() {
			continue
		}
		total++
		if it.ID == id {
			pos = total
		}
	}
	if pos == 0 {
		return 0, 0
	}
	return pos, total
}
