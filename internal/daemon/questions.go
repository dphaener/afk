package daemon

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
)

// This file is the pure half of the open-question hold (docs/PLAN.md T5.6):
// a task named in the "Needed by" cell of an open row of the plan's Open
// questions table is not started until the row is answered in the plan.
//
// Like everything else in the loop, the hold keeps nothing in memory and adds
// no file of its own. What it knows is read back on every pass from three
// places that already exist: the plan (is the row still open?), the inbox
// (was the owner asked, and what did they say?) and the ledger (is the task
// parked, and on which items?). The helpers here are how each of those
// questions is answered, so that each can be a row in a table test.

// The title prefixes of the two items the hold raises. They are exported for
// the same reason the other prefixes in this package are: the inbox schema
// has no field for what raised an item, so the title is how the loop finds
// its own items again, and none may begin with another.
const (
	// QuestionTitlePrefix starts the title of the item that asks the owner
	// one open question of the plan. The question's own text follows it.
	QuestionTitlePrefix = "Open question: "
	// QuestionFollowUpTitlePrefix starts the title of the item raised when
	// the owner's answer could not be put into the plan in the project's main
	// checkout. The task's id follows it.
	QuestionFollowUpTitlePrefix = "Put the answer in the plan: "
)

// answeredInPlan is the answer the loop itself gives a question item whose
// row is no longer open in the plan (the owner wrote the answer there by
// hand, or took the row out). It is a constant because questionItem has to
// recognise it: it is not the owner's answer to anything, and it must never
// be written into a row.
const answeredInPlan = "answered in the plan"

// ErrRowAnswered is what a PlanPatchRequest's Edit returns when the row it
// was to answer is not an open row in the plan text it was given: it was
// answered already, was removed, or appears more than once so there is no
// telling which is meant. A PatchPlan hook must hand it back so that
// errors.Is finds it; the loop reads it as "nothing to write", not as a
// failure.
var ErrRowAnswered = errors.New("the plan has no single open row for that question")

// PlanPatchRequest asks for one small change to the master plan, made the
// guarded way a plan change is made (checked, committed, and pushed or put in
// a pull request, as the project is set up).
type PlanPatchRequest struct {
	// PlanPath is the plan's path as LoadBuild gave it.
	PlanPath string
	// Edit is handed the plan text the change is being made to and returns
	// the one line to replace and what replaces it. It is a function, and not
	// a ready pair of lines, because the hook may apply the change to a copy
	// of the plan that is newer than the one this pass read; the row is found
	// again in that text, and ErrRowAnswered says it is not open there.
	Edit func(plan []byte) (old, new []byte, err error)
	// Message is the commit message.
	Message string
}

// PlanPatchResult is what a PatchPlan that returned no error did.
type PlanPatchResult struct {
	// PR is the address of the pull request holding the change, when the
	// project changes its plan through one. The plan in the main checkout is
	// then unchanged until the owner merges and pulls.
	PR string
	// Notes are plain sentences about the change that the owner should see.
	Notes []string
}

// heldRow is one open row together with the inbox item that asks it.
type heldRow struct {
	Q    masterplan.Question
	Item inbox.Item
}

// holdingRows returns the open rows that name task in their "Needed by" cell,
// in plan order. An answered row holds nothing, and neither does a row whose
// question cell is empty: there is nothing to ask the owner, and an item with
// no first line could never be matched to its row again.
func holdingRows(questions []masterplan.Question, task string) []masterplan.Question {
	var out []masterplan.Question
	for _, q := range questions {
		if !q.Open || q.Text == "" {
			continue
		}
		for _, id := range q.NeededBy {
			if id == task {
				out = append(out, q)
				break
			}
		}
	}
	return out
}

// firstLine is the first line of an item's body. A question item's first
// line is its row's question text, exactly, and that is the whole link
// between the two.
func firstLine(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	return strings.TrimRight(line, "\r")
}

// isQuestionItem reports whether it is an item the hold raised for a row.
func isQuestionItem(it inbox.Item) bool {
	return it.Kind == "question" && strings.HasPrefix(it.Title, QuestionTitlePrefix)
}

// questionItem finds the item that asks q: one of kind `question` whose title
// starts with QuestionTitlePrefix and whose body's first line is the row's
// question text. An open one wins; otherwise the newest answered one.
//
// The match is on the body's first line and not on the title, because a title
// is cut to what the inbox stores and two long questions with the same start
// would share one. The item's task is not compared, so a row needed by two
// tasks is asked once and both wait on the same item.
//
// An item the loop closed itself (answeredInPlan) is never returned. Its
// answer is not the owner's, so for a row that is open again there is nothing
// to write, and the owner has to be asked afresh.
func questionItem(items []inbox.Item, q masterplan.Question) (inbox.Item, bool) {
	var best inbox.Item
	found := false
	for _, it := range items {
		if !isQuestionItem(it) || firstLine(it.Body) != q.Text {
			continue
		}
		if it.Open() {
			return it, true
		}
		if it.Answer == answeredInPlan {
			continue
		}
		if !found || answeredLater(it, best) {
			best, found = it, true
		}
	}
	return best, found
}

// answeredLater reports whether a was answered after b. A time that cannot be
// read is the oldest there is.
func answeredLater(a, b inbox.Item) bool {
	at, aok := parseTime(a.AnsweredAt)
	bt, bok := parseTime(b.AnsweredAt)
	switch {
	case !aok:
		return false
	case !bok:
		return true
	}
	return at.After(bt)
}

// isQuestionHold reports whether a ledger entry is a task the hold parked: it
// is parked, has no session, and every item it is parked on is found and
// carries one of the hold's two title prefixes.
//
// This is how Decide tells such a task from the two other things that park.
// An agent's task parked on a question has a session. An owner's task has
// none either, but its item carries OwnerTitlePrefix. The difference matters
// because each is released differently: a held task has never been started,
// so it must go back to pending and never to running (which would have the
// next pass create a session for a task whose row may still be open), and
// never to done.
//
// An item that cannot be found makes the answer no. nexttask never chooses a
// parked task with a missing item anyway, so this only keeps the function
// from calling something a hold on no evidence.
func isQuestionHold(e ledger.Entry, items []inbox.Item) bool {
	if e.Status != ledger.Parked || e.Session != "" || len(e.ParkedOn) == 0 {
		return false
	}
	byID := make(map[string]inbox.Item, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	for _, id := range e.ParkedOn {
		it, ok := byID[id]
		if !ok {
			return false
		}
		if !strings.HasPrefix(it.Title, QuestionTitlePrefix) && !strings.HasPrefix(it.Title, QuestionFollowUpTitlePrefix) {
			return false
		}
	}
	return true
}

// staleQuestionItems returns the open question items whose row is no longer
// an open row of the plan: the owner answered it in the plan by hand, or took
// it out. Nobody needs those answered any more, and a task parked on one
// would wait for ever.
func staleQuestionItems(items []inbox.Item, questions []masterplan.Question) []inbox.Item {
	open := make(map[string]bool, len(questions))
	for _, q := range questions {
		if q.Open {
			open[q.Text] = true
		}
	}
	var out []inbox.Item
	for _, it := range items {
		if it.Open() && isQuestionItem(it) && !open[firstLine(it.Body)] {
			out = append(out, it)
		}
	}
	return out
}

// answeredRows returns the open rows naming task whose item the owner has
// answered: the rows whose answer is known and is not in the plan yet.
func answeredRows(questions []masterplan.Question, items []inbox.Item, task string) []heldRow {
	var out []heldRow
	for _, q := range holdingRows(questions, task) {
		if it, ok := questionItem(items, q); ok && !it.Open() {
			out = append(out, heldRow{Q: q, Item: it})
		}
	}
	return out
}

// patchChoice is what the executor does with a held task once its answers
// have been written, or could not be.
type patchChoice struct {
	// Release means the task goes back to pending.
	Release bool
	// Left holds the rows whose answer is still not in the plan. It is empty
	// when Release is true.
	Left []heldRow
}

// afterPatch is the choice the executor makes for a held task, from the plan
// as it reads after the patches were tried.
//
// The task is released when no open row naming it has an answered item left.
// That is deliberately not "no open row names it": a row that is open with no
// item, or with an item nobody has answered, is a new question, and the hold
// rule asks it on the next pass once the task is pending again. Otherwise an
// answer did not reach the plan in the main checkout (the change is in a pull
// request, or it failed), and the owner is told what to do.
//
// It reads the plan and not the patch's own result, because the plan is what
// the agent will read: a patch that reported success and changed nothing must
// not start the task.
func afterPatch(questions []masterplan.Question, items []inbox.Item, task string) patchChoice {
	left := answeredRows(questions, items, task)
	return patchChoice{Release: len(left) == 0, Left: left}
}

// cleanAnswer is an answer as masterplan.AnswerRow writes it into a cell:
// every run of white space is one space and each pipe is escaped. It is
// repeated here, and not exported from there, only for the follow-up item's
// text, which shows the owner what to type when afk could not write it.
func cleanAnswer(answer string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(answer), " "), "|", `\|`)
}

// followUpRow is one row of a follow-up item: the row, the answer, and what
// became of the attempt to write it.
type followUpRow struct {
	Line   int
	Text   string
	Date   string
	Answer string
	// PR is the pull request the answer was put in; Err is why it could not
	// be written. Both empty means the patch was tried, or counted as done,
	// and the row is open all the same.
	PR    string
	Err   error
	Notes []string
}

// followUpBody is the body of the item raised when an answer is not in the
// plan in the main checkout. It says what happened and what to do, row by
// row: with a pull request, merge it and pull; in every other case, the exact
// text to add to the row's Question cell, after the reason afk did not add it.
// canPatch false means this daemon has no way to change the plan at all.
func followUpBody(task, plan, root string, rows []followUpRow, canPatch bool) string {
	var b strings.Builder
	what := "an open question"
	if len(rows) != 1 {
		what = fmt.Sprintf("%d open questions", len(rows))
	}
	fmt.Fprintf(&b, "You answered %s that %s needs, but the answer is not in the plan (%s) in the project folder yet. "+
		"An agent reads the answer from the plan, so afk has not started %s.\n\nWhat to do next:\n", what, task, plan, task)
	for i, r := range rows {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, r.Text)
		if r.PR != "" {
			fmt.Fprintf(&b, "   afk put your answer in a pull request: %s\n"+
				"   Merge it, then pull the base branch in the project folder (%s).\n", r.PR, root)
		} else {
			switch {
			case !canPatch:
				b.WriteString("   This daemon cannot change the plan, so the answer has to be written in by hand.\n")
			case r.Err != nil:
				fmt.Fprintf(&b, "   afk could not write your answer into the plan: %v\n", r.Err)
			default:
				b.WriteString("   afk could not find a single open row for this question to write your answer into " +
					"(the row may be in the plan twice, or the answer may hold a character that breaks the table).\n")
			}
			fmt.Fprintf(&b, "   In %s, line %d, add this at the end of the row's Question cell, then commit the change:\n"+
				"   **Answered %s:** %s\n", plan, r.Line, r.Date, cleanAnswer(r.Answer))
		}
		for _, n := range r.Notes {
			if n = strings.TrimSpace(n); n != "" {
				fmt.Fprintf(&b, "   Note: %s\n", n)
			}
		}
	}
	fmt.Fprintf(&b, "\nWhen that is done, answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
		"afk reads the plan again and starts %s once the answer is there.", task)
	return b.String()
}

// questionBody is the body of the item that asks one open question. Its first
// line is the row's question text exactly, which is how the item is matched
// to its row on every later pass (see questionItem); nothing may be put
// before it.
func questionBody(q masterplan.Question, task, plan string) string {
	needs := task
	if len(q.NeededBy) > 1 {
		needs = strings.Join(q.NeededBy, ", ")
	}
	return fmt.Sprintf("%s\n\n"+
		"The plan lists this as an open question that %s needs answered first (%s, line %d), so afk has not started anything for %s.\n\n"+
		"What to do next:\n"+
		"Answer this item with `afk inbox answer <id> \"<your answer>\"`, using this item's id (shown by `afk inbox`). "+
		"afk writes your answer into the plan and then starts %s.",
		q.Text, needs, plan, q.Line, task, task)
}

// holdReason is the sentence for a task held by n open rows.
func holdReason(task string, n int) string {
	if n == 1 {
		return fmt.Sprintf("waiting on the owner: %s needs 1 open question answered first", task)
	}
	return fmt.Sprintf("waiting on the owner: %s needs %d open questions answered first", task, n)
}
