package daemon

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
)

// The tests in this file are the pure helpers of the open-question hold, one
// table each. The rule itself is rows of TestDecide, and what the executor
// does with it is the TestTickQuestion tests in daemon_test.go.

func texts(rows []masterplan.Question) []string {
	out := []string{}
	for _, q := range rows {
		out = append(out, q.Text)
	}
	return out
}

func ids(items []inbox.Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// TestQuestionHoldingRows: which rows hold a task.
//
// Load-bearing rows: "an answered row holds nothing" and "a row naming no
// task holds nothing" are the two acceptance rows of the task that say the
// hold must not reach too far; "a row naming two tasks holds each" is what
// lets one question keep two tasks back; "an id that only starts the same
// does not match" guards against T1.1 being held for T1.10's question.
func TestQuestionHoldingRows(t *testing.T) {
	cases := []struct {
		name string
		rows []masterplan.Question
		task string
		want []string
	}{
		{name: "no rows", task: "T1.1", want: []string{}},
		{name: "an open row naming the task holds it",
			rows: []masterplan.Question{openRow("Which region?", "T1.1")}, task: "T1.1", want: []string{"Which region?"}},
		{name: "an answered row holds nothing",
			rows: []masterplan.Question{answeredRow("Which region?", "T1.1")}, task: "T1.1", want: []string{}},
		{name: "a row naming no task holds nothing",
			rows: []masterplan.Question{openRow("Which region?")}, task: "T1.1", want: []string{}},
		{name: "a row naming another task holds nothing",
			rows: []masterplan.Question{openRow("Which region?", "T1.2")}, task: "T1.1", want: []string{}},
		{name: "a row naming two tasks holds each",
			rows: []masterplan.Question{openRow("Which region?", "T1.1", "T1.2")}, task: "T1.2", want: []string{"Which region?"}},
		{name: "two rows hold in plan order",
			rows: []masterplan.Question{openRow("Which bank?", "T1.1"), answeredRow("Old?", "T1.1"), openRow("Which region?", "T1.1")},
			task: "T1.1", want: []string{"Which bank?", "Which region?"}},
		{name: "an id that only starts the same does not match",
			rows: []masterplan.Question{openRow("Which region?", "T1.10")}, task: "T1.1", want: []string{}},
		{name: "a row with an empty question holds nothing",
			rows: []masterplan.Question{openRow("", "T1.1")}, task: "T1.1", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := texts(holdingRows(tc.rows, tc.task)); !slices.Equal(got, tc.want) {
				t.Errorf("holdingRows = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestQuestionItem: which item asks a row.
//
// Load-bearing rows: "the task on the item is not compared" (a row needed by
// two tasks is asked once), "the title alone is not a match" (titles are cut
// to 200 characters, so two long questions can share one, and the body's
// first line is the only exact key), "an open item wins over an answered one"
// and "the newest answered one wins" (which answer is written into the plan),
// and "an item the loop closed itself is not an answer" (its text is the
// loop's own, and must never be written into a row that is open again).
func TestQuestionItem(t *testing.T) {
	row := openRow("Which region?", "T1.1")
	long := strings.Repeat("Which of the many regions on offer ", 8)
	notQuestion := askedItem("x1", "T1.1", "Which region?")
	notQuestion.Kind = "notice"
	otherTitle := askedItem("x2", "T1.1", "Which region?")
	otherTitle.Title = "Which region?"
	selfClosed := answered(askedItem("s1", "T1.1", "Which region?"), time.Minute)
	selfClosed.Answer = answeredInPlan
	crlf := askedItem("c1", "T1.1", "Which region?")
	crlf.Body = "Which region?\r\n\r\nThe rest."
	noTime := answered(askedItem("n1", "T1.1", "Which region?"), time.Minute)
	noTime.AnsweredAt = "not a time"

	cases := []struct {
		name  string
		q     masterplan.Question
		items []inbox.Item
		want  string // the item's id; "" means none
	}{
		{name: "no items", q: row},
		{name: "the item for the row", q: row,
			items: []inbox.Item{askedItem("q1", "T1.1", "Which region?")}, want: "q1"},
		{name: "the task on the item is not compared", q: row,
			items: []inbox.Item{askedItem("q1", "T9.9", "Which region?")}, want: "q1"},
		{name: "an item for another row is not it", q: row,
			items: []inbox.Item{askedItem("q1", "T1.1", "Which bank?")}},
		{name: "the title alone is not a match", q: openRow(long+"first?", "T1.1"),
			items: []inbox.Item{askedItem("q1", "T1.1", long+"second?")}},
		{name: "an item of another kind is not it", q: row, items: []inbox.Item{notQuestion}},
		{name: "an item without the prefix is not it", q: row, items: []inbox.Item{otherTitle}},
		{name: "an open item wins over an answered one", q: row,
			items: []inbox.Item{answered(askedItem("a1", "T1.1", "Which region?"), time.Minute), askedItem("q2", "T1.1", "Which region?")},
			want:  "q2"},
		{name: "the newest answered one wins", q: row,
			items: []inbox.Item{answered(askedItem("a1", "T1.1", "Which region?"), time.Hour),
				answered(askedItem("a2", "T1.1", "Which region?"), time.Minute),
				answered(askedItem("a3", "T1.1", "Which region?"), 2*time.Hour)},
			want: "a2"},
		{name: "an answer with no readable time loses to one with a time", q: row,
			items: []inbox.Item{noTime, answered(askedItem("a2", "T1.1", "Which region?"), time.Hour)}, want: "a2"},
		{name: "an answer with no readable time is still an answer", q: row, items: []inbox.Item{noTime}, want: "n1"},
		{name: "an item the loop closed itself is not an answer", q: row, items: []inbox.Item{selfClosed}},
		{name: "a first line ending in a carriage return still matches", q: row, items: []inbox.Item{crlf}, want: "c1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := questionItem(tc.items, tc.q)
			if ok != (tc.want != "") || got.ID != tc.want {
				t.Errorf("questionItem = %q, %v; want %q", got.ID, ok, tc.want)
			}
		})
	}
}

// TestQuestionIsHold: telling a held task from every other parked task.
//
// Load-bearing rows: "an owner's parked task is not a hold" (it would be set
// back to pending when the owner said it was done, and asked again for ever),
// "an agent's parked task with a session is not a hold" (it would lose its
// session's work by going to pending and starting a new attempt), and "parked
// on a follow-up item is a hold" (answering the follow-up must bring the task
// back to the patch and not to an un-park).
func TestQuestionIsHold(t *testing.T) {
	q := askedItem("q1", "T1.1", "Which region?")
	owner := blocking(item("o1", "T0.0", ""))
	owner.Title = OwnerTitlePrefix + "T0.0 — Stand up the VM"
	withSession := questionHeld("T1.1", "q1")
	withSession.Session = "t1-1"

	cases := []struct {
		name  string
		entry ledger.Entry
		items []inbox.Item
		want  bool
	}{
		{name: "parked on a question item is a hold", entry: questionHeld("T1.1", "q1"), items: []inbox.Item{q}, want: true},
		{name: "parked on an answered question item is a hold", entry: questionHeld("T1.1", "q1"),
			items: []inbox.Item{answered(q, time.Minute)}, want: true},
		{name: "parked on a follow-up item is a hold", entry: questionHeld("T1.1", "f1"),
			items: []inbox.Item{followUpItem("f1", "T1.1")}, want: true},
		{name: "parked on two question items is a hold", entry: questionHeld("T1.1", "q1", "q2"),
			items: []inbox.Item{q, askedItem("q2", "T1.1", "Which bank?")}, want: true},
		{name: "an owner's parked task is not a hold", entry: ownerParked("T0.0", "o1"), items: []inbox.Item{owner}},
		{name: "an agent's parked task with a session is not a hold", entry: withSession, items: []inbox.Item{q}},
		{name: "one item of another sort is enough to say no", entry: questionHeld("T1.1", "q1", "o1"), items: []inbox.Item{q, owner}},
		{name: "an item that is not found is not a hold", entry: questionHeld("T1.1", "gone"), items: []inbox.Item{q}},
		{name: "parked on nothing is not a hold", entry: questionHeld("T1.1"), items: []inbox.Item{q}},
		{name: "a pending task is not a hold", entry: ledger.Entry{ID: "T1.1", Status: ledger.Pending, ParkedOn: []string{"q1"}}, items: []inbox.Item{q}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isQuestionHold(tc.entry, tc.items); got != tc.want {
				t.Errorf("isQuestionHold = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestQuestionStaleItems: which items the loop closes by itself.
//
// Load-bearing rows: "an item whose row is still open stays" (closing it
// would release a held task with the question unanswered), "an item of
// another kind is never closed" (the loop must not answer the owner's other
// items for them), and "an item whose row was answered is stale" (the task
// parked on it would otherwise wait for an answer nobody needs).
func TestQuestionStaleItems(t *testing.T) {
	other := blocking(item("x1", "T1.1", "t1-1"))
	other.Body = "Which region?"
	cases := []struct {
		name  string
		items []inbox.Item
		rows  []masterplan.Question
		want  []string
	}{
		{name: "an item whose row is still open stays",
			items: []inbox.Item{askedItem("q1", "T1.1", "Which region?")},
			rows:  []masterplan.Question{openRow("Which region?", "T1.1")}, want: []string{}},
		{name: "an item whose row was answered is stale",
			items: []inbox.Item{askedItem("q1", "T1.1", "Which region?")},
			rows:  []masterplan.Question{answeredRow("Which region?", "T1.1")}, want: []string{"q1"}},
		{name: "an item whose row is gone is stale",
			items: []inbox.Item{askedItem("q1", "T1.1", "Which region?")},
			rows:  []masterplan.Question{openRow("Which bank?", "T1.1")}, want: []string{"q1"}},
		{name: "an item with no table left is stale",
			items: []inbox.Item{askedItem("q1", "T1.1", "Which region?")}, want: []string{"q1"}},
		{name: "an answered item is left alone",
			items: []inbox.Item{answered(askedItem("q1", "T1.1", "Which region?"), time.Minute)}, want: []string{}},
		{name: "an item of another kind is never closed",
			items: []inbox.Item{other, followUpItem("f1", "T1.1")}, want: []string{}},
		{name: "an item whose row no longer names its task stays while the row is open",
			items: []inbox.Item{askedItem("q1", "T1.1", "Which region?")},
			rows:  []masterplan.Question{openRow("Which region?")}, want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(staleQuestionItems(tc.items, tc.rows)); !slices.Equal(got, tc.want) {
				t.Errorf("staleQuestionItems = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestQuestionAfterPatch: release the task, or tell the owner.
//
// Load-bearing rows: "the row is still open with its item answered" is the
// pull-request case and the failed patch (the task must not start on an
// answer that is not in the plan); "an open row with no item releases" and
// "an open row with an open item releases" are why release is to pending and
// not to running: the hold rule asks those on the next pass.
func TestQuestionAfterPatch(t *testing.T) {
	ans := answered(askedItem("q1", "T1.1", "Which region?"), time.Minute)
	cases := []struct {
		name     string
		rows     []masterplan.Question
		items    []inbox.Item
		wantLeft []string
	}{
		{name: "the row is answered in the plan",
			rows: []masterplan.Question{answeredRow("Which region?", "T1.1")}, items: []inbox.Item{ans}},
		{name: "no table at all", items: []inbox.Item{ans}},
		{name: "the row is still open with its item answered",
			rows: []masterplan.Question{openRow("Which region?", "T1.1")}, items: []inbox.Item{ans}, wantLeft: []string{"Which region?"}},
		{name: "one of two rows reached the plan",
			rows:  []masterplan.Question{answeredRow("Which region?", "T1.1"), openRow("Which bank?", "T1.1")},
			items: []inbox.Item{ans, answered(askedItem("q2", "T1.1", "Which bank?"), time.Minute)}, wantLeft: []string{"Which bank?"}},
		{name: "an open row with no item releases",
			rows: []masterplan.Question{openRow("Which bank?", "T1.1")}, items: []inbox.Item{ans}},
		{name: "an open row with an open item releases",
			rows: []masterplan.Question{openRow("Which bank?", "T1.1")}, items: []inbox.Item{askedItem("q2", "T1.1", "Which bank?")}},
		{name: "an open row for another task releases",
			rows: []masterplan.Question{openRow("Which region?", "T1.2")}, items: []inbox.Item{ans}},
		{name: "the same question on two rows is left twice",
			rows:  []masterplan.Question{openRow("Which region?", "T1.1"), openRow("Which region?", "T1.1")},
			items: []inbox.Item{ans}, wantLeft: []string{"Which region?", "Which region?"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := afterPatch(tc.rows, tc.items, "T1.1")
			left := []string{}
			for _, r := range got.Left {
				left = append(left, r.Q.Text)
				if r.Item.Open() {
					t.Errorf("a left row carries an open item: %+v", r.Item)
				}
			}
			want := tc.wantLeft
			if want == nil {
				want = []string{}
			}
			if !slices.Equal(left, want) || got.Release != (len(want) == 0) {
				t.Errorf("afterPatch = release %v, left %v; want release %v, left %v", got.Release, left, len(want) == 0, want)
			}
		})
	}
}

// TestQuestionBodies pins what the owner is told. The sentences are not
// pinned whole; what must be there is. Load-bearing: the question item's
// first line is the row's text and nothing else (the item is matched to its
// row by it), and every follow-up row without a pull request carries the
// exact text to write, with a pipe escaped the way the plan's table needs.
func TestQuestionBodies(t *testing.T) {
	t.Run("the question item", func(t *testing.T) {
		q := openRow("Which region | zone?", "T1.1", "T1.2")
		body := questionBody(q, "T1.1", "docs/PLAN.md")
		if firstLine(body) != q.Text {
			t.Errorf("first line = %q, want the row's text %q", firstLine(body), q.Text)
		}
		wantContains(t, "the question body", body, "open question", "T1.1, T1.2", "docs/PLAN.md, line 40",
			"has not started anything for T1.1", "afk inbox answer <id> \"<your answer>\"", "writes your answer into the plan")
		it := inbox.NewItem{Task: "T1.1", Kind: "question", Blocking: true, Title: fitTitle(QuestionTitlePrefix + q.Text), Body: body}
		if err := inbox.Validate(it); err != nil {
			t.Errorf("the inbox refuses the item: %v", err)
		}
	})

	cases := []struct {
		name     string
		rows     []followUpRow
		canPatch bool
		has, not []string
	}{
		{name: "a pull request", canPatch: true,
			rows: []followUpRow{{Line: 12, Text: "Which region?", Date: "2026-10-11", Answer: "Frankfurt", PR: "https://example.test/pr/7"}},
			has:  []string{"an open question", "https://example.test/pr/7", "Merge it", "pull the base branch", "/p/demo", "afk inbox answer <id> done"},
			not:  []string{"**Answered"}},
		{name: "a patch that failed", canPatch: true,
			rows: []followUpRow{{Line: 12, Text: "Which region?", Date: "2026-10-11", Answer: "Frankfurt\n| eu", Err: errors.New("the push was refused")}},
			has:  []string{"could not write your answer", "the push was refused", "docs/PLAN.md, line 12", `**Answered 2026-10-11:** Frankfurt \| eu`, "commit"}},
		{name: "a daemon that cannot change the plan",
			rows: []followUpRow{{Line: 12, Text: "Which region?", Date: "2026-10-11", Answer: "Frankfurt"}},
			has:  []string{"This daemon cannot change the plan", "**Answered 2026-10-11:** Frankfurt", "line 12"}},
		{name: "a patch that found no single row", canPatch: true,
			rows: []followUpRow{{Line: 12, Text: "Which region?", Date: "2026-10-11", Answer: "Frankfurt"},
				{Line: 13, Text: "Which region?", Date: "2026-10-11", Answer: "Frankfurt", Notes: []string{"the plan was pushed"}}},
			has: []string{"2 open questions", "could not find a single open row", "line 12", "line 13", "Note: the plan was pushed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := followUpBody("T1.1", "docs/PLAN.md", "/p/demo", tc.rows, tc.canPatch)
			wantContains(t, "the follow-up body", body, tc.has...)
			wantContains(t, "the follow-up body", body, "T1.1", "has not started T1.1", "starts T1.1 once the answer is there")
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("the follow-up body holds %q, which it must not:\n%s", n, body)
				}
			}
			it := inbox.NewItem{Task: "T1.1", Kind: "external_action", Blocking: true,
				Title: fitTitle(QuestionFollowUpTitlePrefix + "T1.1"), Body: body}
			if err := inbox.Validate(it); err != nil {
				t.Errorf("the inbox refuses the item: %v", err)
			}
		})
	}
}

// TestQuestionCleanAnswerMatchesAnswerRow keeps the text a follow-up item
// tells the owner to type the same as the text afk would have written. The
// cleaning is repeated in this package (see cleanAnswer), and this is what
// goes red if the two drift apart.
func TestQuestionCleanAnswerMatchesAnswerRow(t *testing.T) {
	plan := []byte("# Plan\n\n## Open questions\n\n| # | Question | Needed by |\n|---|---|---|\n| 1 | Which region? | T1.1 |\n")
	for _, answer := range []string{"Frankfurt", "  Use Frankfurt | eu-central\n\tplease  ", "a || b"} {
		_, got, ok := masterplan.AnswerRow(plan, "Which region?", "2026-10-11", answer)
		if !ok {
			t.Fatalf("AnswerRow refused %q", answer)
		}
		want := "**Answered 2026-10-11:** " + cleanAnswer(answer) + " |"
		if !strings.Contains(string(got), want) {
			t.Errorf("AnswerRow wrote %q, which does not hold %q", got, want)
		}
	}
}
