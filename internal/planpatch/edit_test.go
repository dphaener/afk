package planpatch

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.md from what the scripted operations produce")

const (
	entry14  = "#### T1.4 — Fourth\n- **Depends on:** T1.3\n- **Goal:** g\n- **Scope:** s4\n- **Acceptance:** a\n"
	entry11a = "#### T1.1a — Between\n- **Depends on:** T1.1\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n"
	entry21  = "#### T2.1 — Next\n- **Depends on:** T1.3\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n"
	blockM2  = "### M2 — Two\n\n**Outcome:** more.\n\n" + entry21 + "\n#### T2.2 — Last\n- **Depends on:** T2.1\n- **Goal:** g\n- **Scope:** s\n- **Acceptance:** a\n"

	// t13Tail is the end of T1.3's entry in base and the blank line after
	// it, which is where a task added to the end of M1 goes.
	t13Tail = "- **Scope:** s3\n- **Acceptance:** a\n\n"
)

// emptyM2 is base with a second milestone that has no task yet.
var emptyM2 = swap(base, "## Deferred\n", "### M2 — Two\n\n**Outcome:** more.\n\n## Deferred\n")

// onlyMilestones is base cut off after its last task, so the last entry
// runs to the end of the file.
var onlyMilestones = base[:strings.Index(base, "\n## Deferred")]

// allRunning holds every task of plan, so a row can show that a builder's
// insertion is no change to any task around it.
func allRunning(plan string) map[string]string {
	held := map[string]string{}
	for _, t := range masterplan.Scan([]byte(plan)).Tasks {
		held[t.ID] = "running"
	}
	return held
}

// checkEdit holds what every builder promises: Old is in the plan exactly
// once, it starts at a heading line, and replacing it gives want.
func checkEdit(t *testing.T, plan string, e Edit, want, wantOldPrefix string) {
	t.Helper()
	if n := bytes.Count([]byte(plan), e.Old); n != 1 {
		t.Fatalf("Old occurs %d times in the plan, want 1:\n%s", n, e.Old)
	}
	if !strings.HasPrefix(string(e.Old), wantOldPrefix) {
		t.Errorf("Old starts with %q, want it to start with %q", firstLine(e.Old), wantOldPrefix)
	}
	if i := bytes.Index([]byte(plan), e.Old); i > 0 && plan[i-1] != '\n' {
		t.Errorf("Old does not start at the start of a line")
	}
	if got := strings.Replace(plan, string(e.Old), string(e.New), 1); got != want {
		t.Errorf("patched plan =\n%s\nwant\n%s", got, want)
	}
}

// TestInsertTask is the placing of one task entry. The load-bearing rows
// are the happy ones as a group, because each is also run through Apply
// with every task of the plan held as running and must be accepted with
// nothing touched: a new task is put after a running one all the time, and
// a builder that rewrote a line of the entry above (a blank line included)
// would be refused by rule 8 exactly then. "the task heading is also shown
// in a code fence" holds the other promise, that Old is there once even
// when the nearest heading is not unique.
func TestInsertTask(t *testing.T) {
	fenced := swap(base, "The owner asked: \"build it\".\n", "The owner asked: \"build it\".\n\n```\n"+t13Entry+"```\n")
	fencedBelow := swap(base, "- Nothing.\n", "- Nothing.\n\n```\n"+t13Entry+"```\n")
	noGap := swap(base, t13Tail+"## Deferred", "- **Scope:** s3\n- **Acceptance:** a\n## Deferred")
	wideGap := swap(base, t13Tail+"## Deferred", t13Tail+"\n## Deferred")
	unclosed := swap(base, "#### T1.3 — Third\n", "#### T1.3 — Third\n```\nexample\n")

	tests := []struct {
		name string
		// plan is the plan to add to; "" means base.
		plan  string
		entry string
		where Where
		// want is the patched plan.
		want string
		// wantOld is how Old must start; "" means "#### T".
		wantOld string
		// wantErr is a piece of the error message wanted.
		wantErr string
	}{
		{
			name:  "after a task in the middle of a milestone",
			entry: entry11a, where: Where{After: "T1.1"},
			want:    swap(base, "- **Scope:** s1\n- **Acceptance:** a\n\n", "- **Scope:** s1\n- **Acceptance:** a\n\n"+entry11a+"\n"),
			wantOld: "#### T1.1 — First\n",
		},
		{
			name:  "after the last task of a milestone",
			entry: entry14, where: Where{After: "T1.3"},
			want:    swap(base, t13Tail, t13Tail+entry14+"\n"),
			wantOld: "#### T1.3 — Third\n",
		},
		{
			name:  "at the end of a milestone",
			entry: entry14, where: Where{Milestone: 1},
			want:    swap(base, t13Tail, t13Tail+entry14+"\n"),
			wantOld: "#### T1.3 — Third\n",
		},
		{
			name: "at the end of the first of two milestones",
			plan: emptyM2, entry: entry14, where: Where{Milestone: 1},
			want: swap(emptyM2, t13Tail, t13Tail+entry14+"\n"),
		},
		{
			name: "into a milestone with no task",
			plan: emptyM2, entry: entry21, where: Where{Milestone: 2},
			want:    swap(emptyM2, "**Outcome:** more.\n\n", "**Outcome:** more.\n\n"+entry21+"\n"),
			wantOld: "### M2 — Two\n",
		},
		{
			name: "after the last entry of the file",
			plan: onlyMilestones, entry: entry14, where: Where{After: "T1.3"},
			want: onlyMilestones + "\n" + entry14,
		},
		{
			name: "after the last line of a file with no line break at its end",
			plan: strings.TrimSuffix(onlyMilestones, "\n"), entry: entry14, where: Where{Milestone: 1},
			want: onlyMilestones + "\n" + entry14,
		},
		{
			name: "no blank line before the next heading",
			plan: noGap, entry: entry14, where: Where{After: "T1.3"},
			want: swap(noGap, "- **Scope:** s3\n- **Acceptance:** a\n## Deferred", t13Tail+entry14+"\n## Deferred"),
		},
		{
			name: "blank lines the plan already has are left alone",
			plan: wideGap, entry: entry14, where: Where{After: "T1.3"},
			want: swap(wideGap, t13Tail+"\n## Deferred", t13Tail+entry14+"\n\n## Deferred"),
		},
		{
			name:  "blank lines around the entry are dropped",
			entry: "\n\n" + entry14 + "\n\n", where: Where{Milestone: 1},
			want: swap(base, t13Tail, t13Tail+entry14+"\n"),
		},
		{
			name:  "an entry with no line break at its end",
			entry: strings.TrimSuffix(entry14, "\n"), where: Where{Milestone: 1},
			want: swap(base, t13Tail, t13Tail+entry14+"\n"),
		},
		{
			name: "the task heading is also shown in a code fence",
			plan: fenced, entry: entry14, where: Where{After: "T1.3"},
			want:    swap(fenced, t13Tail+"## Deferred", t13Tail+entry14+"\n## Deferred"),
			wantOld: "#### T1.2 — Second\n",
		},
		{
			name: "the task heading is shown again further down",
			plan: fencedBelow, entry: entry14, where: Where{After: "T1.3"},
			want:    swap(fencedBelow, t13Tail+"## Deferred", t13Tail+entry14+"\n## Deferred"),
			wantOld: "#### T1.2 — Second\n",
		},
		{
			name:  "an unknown task to go after",
			entry: entry14, where: Where{After: "T9.9"},
			wantErr: "no task T9.9",
		},
		{
			name:  "an unknown milestone",
			entry: entry14, where: Where{Milestone: 7},
			wantErr: "no milestone M7",
		},
		{
			name:  "no place named means milestone 0, which this plan lacks",
			entry: entry14, where: Where{},
			wantErr: "no milestone M0",
		},
		{
			name:  "both a task and a milestone",
			entry: entry14, where: Where{After: "T1.3", Milestone: 1},
			wantErr: "Name one of the two",
		},
		{
			name:  "an id the plan already has",
			entry: strings.Replace(entry14, "T1.4", "T1.2", 1), where: Where{Milestone: 1},
			wantErr: "task T1.2 is already in the plan",
		},
		{
			name:  "an entry that does not start with a task heading",
			entry: "Some notes first.\n\n" + entry14, where: Where{Milestone: 1},
			wantErr: "must start with a task heading",
		},
		{
			name:  "an entry whose heading has no dash",
			entry: strings.Replace(entry14, "T1.4 — Fourth", "T1.4 Fourth", 1), where: Where{Milestone: 1},
			wantErr: "must start with a task heading",
		},
		{
			name:  "an entry with two tasks",
			entry: entry14 + "\n" + entry11a, where: Where{Milestone: 1},
			wantErr: "more than one heading",
		},
		{
			name:  "an empty entry",
			entry: "\n \n", where: Where{Milestone: 1},
			wantErr: "the entry is empty",
		},
		{
			name: "a code fence above the place is never closed",
			plan: unclosed, entry: entry14, where: Where{After: "T1.3"},
			wantErr: "would not be read as a task",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := tt.plan
			if plan == "" {
				plan = base
			}
			e, err := InsertTask([]byte(plan), []byte(tt.entry), tt.where)
			if tt.wantErr != "" {
				wantBuildErr(t, e, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			wantOld := tt.wantOld
			if wantOld == "" {
				wantOld = "#### T"
			}
			checkEdit(t, plan, e, tt.want, wantOld)

			got, err := Apply(Request{Plan: []byte(plan), Old: e.Old, New: e.New, Held: allRunning(plan)})
			if err != nil {
				t.Fatalf("Apply refused the builder's patch with every task running: %v", err)
			}
			if len(got.Touched) != 0 {
				t.Errorf("Touched = %v, want none: an added task is no change to the tasks around it", got.Touched)
			}
		})
	}
}

// TestAppendMilestone is the placing of a whole milestone. As in
// TestInsertTask the happy rows are run through Apply with every task held
// and must touch none. The row "after the last milestone, not the first"
// is the one that tells this builder from InsertTask: a new milestone goes
// after the end of the last milestone's section, wherever its own number
// would sort.
func TestAppendMilestone(t *testing.T) {
	blockM3 := strings.NewReplacer("M2", "M3", "T2.", "T3.").Replace(blockM2)
	noMilestone := swap(base, "### M1 — One\n", "Tasks, in order.\n")

	tests := []struct {
		name    string
		plan    string
		block   string
		want    string
		wantOld string
		wantErr string
	}{
		{
			name:    "before the next section",
			block:   blockM2,
			want:    swap(base, t13Tail, t13Tail+blockM2+"\n"),
			wantOld: "#### T1.3 — Third\n",
		},
		{
			name: "at the end of the file",
			plan: onlyMilestones, block: blockM2,
			want: onlyMilestones + "\n" + blockM2,
		},
		{
			name: "after the last milestone, not the first",
			plan: emptyM2, block: blockM3,
			want:    swap(emptyM2, "**Outcome:** more.\n\n", "**Outcome:** more.\n\n"+blockM3+"\n"),
			wantOld: "### M2 — Two\n",
		},
		{
			name:  "a milestone with no task yet",
			block: "\n### M2 — Two\n\n**Outcome:** more.\n\n",
			want:  swap(base, t13Tail, t13Tail+"### M2 — Two\n\n**Outcome:** more.\n\n"),
		},
		{
			name:    "a milestone number the plan already has",
			block:   strings.Replace(blockM2, "### M2", "### M1", 1),
			wantErr: "already has a milestone M1",
		},
		{
			name:    "a block that does not start with a milestone heading",
			block:   entry21,
			wantErr: "must start with a heading like",
		},
		{
			name:    "a milestone heading with no number",
			block:   strings.Replace(blockM2, "### M2 — Two", "### Milestone two", 1),
			wantErr: "must start with a heading like",
		},
		{
			name:    "a block that holds a section heading",
			block:   blockM2 + "\n## Deferred later\n\n- x\n",
			wantErr: "second section heading",
		},
		{
			name:    "a block that holds two milestones",
			block:   blockM2 + "\n### M3 — Three\n",
			wantErr: "second section heading",
		},
		{
			name:    "a task id the plan already has",
			block:   strings.Replace(blockM2, "#### T2.2 — Last", "#### T1.2 — Last", 1),
			wantErr: "task T1.2 is already in the plan",
		},
		{
			name: "a plan with no milestone",
			plan: noMilestone, block: blockM2,
			wantErr: "has no milestone yet",
		},
		{
			name:    "an empty block",
			block:   "",
			wantErr: "the milestone is empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := tt.plan
			if plan == "" {
				plan = base
			}
			e, err := AppendMilestone([]byte(plan), []byte(tt.block))
			if tt.wantErr != "" {
				wantBuildErr(t, e, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			wantOld := tt.wantOld
			if wantOld == "" {
				wantOld = "#### T"
			}
			checkEdit(t, plan, e, tt.want, wantOld)

			got, err := Apply(Request{Plan: []byte(plan), Old: e.Old, New: e.New, Held: allRunning(plan)})
			if err != nil {
				t.Fatalf("Apply refused the builder's patch with every task running: %v", err)
			}
			if len(got.Touched) != 0 {
				t.Errorf("Touched = %v, want none", got.Touched)
			}
		})
	}
}

// TestAddDependency is the rewriting of one Depends on line. Every row
// makes T1.3 wait for T1.1 on base with T1.3's line set to the row's value.
// The load-bearing rows: "a note in brackets after the last id" and "text
// after the ids" (the text a person wrote on the line is kept and stays
// with the id it was about), "a note that runs on to the next line" and
// "prose with no id" (where the builder cannot be sure, it refuses and does
// not guess what a task waits for), and "a Depends on line shown in a code
// fence" (the line changed is the one the daemon reads). Each accepted row
// is also run through Apply twice: free, where it must touch T1.3 alone,
// and with T1.3 running, where rule 8 must refuse it.
func TestAddDependency(t *testing.T) {
	const line = "- **Depends on:** T1.2\n"
	// with is base with T1.3's Depends on line replaced by text.
	with := func(text string) string { return swap(base, line, text) }
	fence := swap(base, "#### T1.3 — Third\n", "#### T1.3 — Third\n```\n- **Depends on:** T9.9\n```\n")

	tests := []struct {
		name string
		// plan is the plan to change; "" means base.
		plan string
		// from and to are T1.3's Depends on line before and after, when
		// plan is "".
		from, to string
		task, on string
		want     string
		wantErr  string
		// wantNoChange is set when ErrNoChange is wanted.
		wantNoChange bool
	}{
		{name: "one id", from: line, to: "- **Depends on:** T1.2, T1.1\n"},
		{name: "a dash for none", from: "- **Depends on:** —\n", to: "- **Depends on:** T1.1\n"},
		{name: "a hyphen for none", from: "- **Depends on:** -\n", to: "- **Depends on:** T1.1\n"},
		{name: "None", from: "- **Depends on:** None\n", to: "- **Depends on:** T1.1\n"},
		{name: "none", from: "- **Depends on:** none\n", to: "- **Depends on:** T1.1\n"},
		{name: "no value at all", from: "- **Depends on:**\n", to: "- **Depends on:** T1.1\n"},
		{name: "text after the ids", from: "- **Depends on:** T1.2 — once it has merged\n", to: "- **Depends on:** T1.2, T1.1 — once it has merged\n"},
		{name: "a note in brackets after the last id", from: "- **Depends on:** T1.2 (reuses its page)\n", to: "- **Depends on:** T1.2 (reuses its page), T1.1\n"},
		{name: "a note with brackets inside it", from: "- **Depends on:** T1.2 (its page (the list)) and more\n", to: "- **Depends on:** T1.2 (its page (the list)), T1.1 and more\n"},
		{name: "an indented line", from: "  - **Depends on:** T1.2\n", to: "  - **Depends on:** T1.2, T1.1\n"},
		{name: "a Windows line ending is kept", from: "- **Depends on:** T1.2\r\n", to: "- **Depends on:** T1.2, T1.1\r\n"},
		{
			name: "a Depends on line shown in a code fence is passed over",
			plan: fence,
			want: swap(fence, line, "- **Depends on:** T1.2, T1.1\n"),
		},
		{name: "already listed", from: "- **Depends on:** T1.2, T1.1\n", wantNoChange: true},
		{name: "already listed by all prior", from: "- **Depends on:** all prior\n", wantNoChange: true},
		{name: "a note that runs on to the next line", from: "- **Depends on:** T1.2 (reuses its page,\n  and its list)\n", wantErr: "not closed on that line"},
		{name: "prose with no id", from: "- **Depends on:** whenever there is time\n", wantErr: "names no task id"},
		{name: "no Depends on line", plan: swap(base, line, ""), wantErr: "has no Depends on line"},
		{name: "an unknown task", task: "T9.9", on: "T1.1", wantErr: "no task T9.9"},
		{name: "an unknown task to wait for", task: "T1.3", on: "T9.8", wantErr: "no task T9.8"},
		{name: "a task waiting for itself", task: "T1.3", on: "T1.3", wantErr: "cannot wait for itself"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, want := tt.plan, tt.want
			if plan == "" {
				plan = base
				if tt.from != "" {
					plan = with(tt.from)
					if tt.to != "" {
						want = with(tt.to)
					}
				}
			}
			task, on := tt.task, tt.on
			if task == "" {
				task, on = "T1.3", "T1.1"
			}
			e, err := AddDependency([]byte(plan), task, on)
			if tt.wantNoChange {
				if !errors.Is(err, ErrNoChange) {
					t.Fatalf("error = %v, want ErrNoChange", err)
				}
				return
			}
			if tt.wantErr != "" {
				wantBuildErr(t, e, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			checkEdit(t, plan, e, want, "#### T1.3 — Third\n")

			got, err := Apply(Request{Plan: []byte(plan), Old: e.Old, New: e.New})
			if err != nil {
				t.Fatalf("Apply refused the builder's patch: %v", err)
			}
			if !reflect.DeepEqual(got.Touched, []string{"T1.3"}) {
				t.Errorf("Touched = %v, want [T1.3]", got.Touched)
			}
			var held *HeldError
			if _, err := Apply(Request{Plan: []byte(plan), Old: e.Old, New: e.New, Held: map[string]string{"T1.3": "running"}}); !errors.As(err, &held) {
				t.Errorf("with T1.3 running, Apply error = %v, want *HeldError", err)
			}
		})
	}
}

func wantBuildErr(t *testing.T, e Edit, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one holding %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to hold %q", err, want)
	}
	if e.Old != nil || e.New != nil {
		t.Errorf("an Edit came back with the error: %+v", e)
	}
}

// op is one step of a scripted planning session: what the agent would run
// after the owner answered. "{id}" in a text field stands for the id the
// last next-id or between step chose, so a script never works an id out by
// hand, which is the rule the skill is held to.
type op struct {
	// do is next-id, between, insert, milestone, depend or patch.
	do string
	// milestone is the milestone of a next-id step, and of an insert step
	// with no after.
	milestone int
	// after is the task a between step, or an insert step, goes after.
	after string
	// text is the entry of an insert step or the block of a milestone step.
	text string
	// task and on are a depend step: task must wait for on.
	task, on string
	// old and new are a patch step.
	old, new string
	// wantID is the id a next-id or between step must choose.
	wantID string
	// touched is what Apply must report for a step that changes the plan.
	touched []string
}

// runScript runs ops in order on plan, every change through a builder and
// then Apply, as the commands do. It stops at the first step that fails and
// returns the plan as it then stands, the index of that step and its error;
// the index is len(ops) when every step ran.
func runScript(t *testing.T, plan []byte, ops []op, held map[string]string) ([]byte, int, error) {
	t.Helper()
	id := ""
	fill := func(s string) string { return strings.ReplaceAll(s, "{id}", id) }
	for i, o := range ops {
		var ids []string
		for _, task := range masterplan.Scan(plan).Tasks {
			ids = append(ids, task.ID)
		}
		var e Edit
		var err error
		switch o.do {
		case "next-id":
			id, err = NextInMilestone(ids, o.milestone)
		case "between":
			id, err = Between(ids, fill(o.after))
		case "insert":
			e, err = InsertTask(plan, []byte(fill(o.text)), Where{After: fill(o.after), Milestone: o.milestone})
		case "milestone":
			e, err = AppendMilestone(plan, []byte(fill(o.text)))
		case "depend":
			e, err = AddDependency(plan, fill(o.task), fill(o.on))
		case "patch":
			e = Edit{Old: []byte(fill(o.old)), New: []byte(fill(o.new))}
		default:
			t.Fatalf("step %d: unknown step %q", i, o.do)
		}
		if err != nil {
			return plan, i, err
		}
		if o.do == "next-id" || o.do == "between" {
			if id != o.wantID {
				t.Errorf("step %d (%s): chose %s, want %s", i, o.do, id, o.wantID)
			}
			continue
		}
		res, err := Apply(Request{Plan: plan, Old: e.Old, New: e.New, Held: held})
		if err != nil {
			return plan, i, err
		}
		if !reflect.DeepEqual(res.Touched, o.touched) {
			t.Errorf("step %d (%s): Touched = %v, want %v", i, o.do, res.Touched, o.touched)
		}
		plan = res.Plan
	}
	return plan, len(ops), nil
}

const (
	m3Block = `### M3 — Plan the week

**Outcome:** the owner picks the week's meals and gets one shopping list.

#### {id} — Pick recipes for a week
- **Depends on:** T1.2
- **Goal:** The owner puts a recipe on each day of the coming week.
- **Scope:** A week page with seven days and a way to put a recipe on a day.
- **Acceptance:** A recipe put on Tuesday is still on Tuesday after a restart.

#### T3.2 — Make a shopping list
- **Depends on:** {id}
- **Goal:** One list of everything the week's recipes need.
- **Scope:** A list page that adds up the ingredient lines of the week's recipes.
- **Acceptance:** Two recipes that both need leeks give one leek line on the list.
`
	privateEntry = `#### {id} — Mark a recipe as private
- **Depends on:** T1.1
- **Goal:** The owner keeps a recipe out of anything that is shared.
- **Scope:** A private tick box on the form, saved with the recipe.
- **Acceptance:** A recipe saved as private shows a lock beside its name.
`
	checkEntry = `#### {id} — Check a recipe before it is saved
- **Depends on:** T1.1
- **Goal:** A recipe with no name or no ingredient is not saved.
- **Scope:** Checks on the form, with a plain message beside the field that is wrong.
- **Acceptance:** Saving a form with no name shows the message and saves nothing.
`
	linkEntry = `#### {id} — Make the share link
- **Depends on:** T2.1
- **Goal:** The owner gets a link for one recipe.
- **Scope:** A share button and a link with a random token.
- **Acceptance:** Pressing the button twice gives the same link.
`
	sharedPageEntry = `#### {id} — Show a shared recipe
- **Depends on:** T2.2a
- **Goal:** Someone with the link reads the recipe without an account.
- **Scope:** A read-only page behind the link.
- **Acceptance:** The link opens the recipe in a private window, and a wrong token shows a not-found page.
`
	t22Acceptance = "- **Acceptance:** The link opens the recipe in a private window, and a wrong token shows a not-found page.\n\n#### T2.2a"
)

// modes is the scripted session for each way of changing a plan, and the
// golden plan it must end at. TestModes runs them and TestFixturesLintClean
// checks the golden files, so the two share this list.
var modes = []struct {
	name   string
	golden string
	// wantTasks is the number of tasks the golden plan must have.
	wantTasks int
	held      map[string]string
	ops       []op
}{
	{
		// A new milestone of two tasks, numbered by code.
		name: "extend", golden: "extend.golden.md", wantTasks: 7,
		ops: []op{
			{do: "next-id", milestone: 3, wantID: "T3.1"},
			{do: "milestone", text: m3Block},
			{do: "next-id", milestone: 3, wantID: "T3.3"},
		},
	},
	{
		// A task added at the end of a milestone, and a later task made to
		// wait for it. T2.2's line has a note in brackets, which stays with
		// the id it was written about.
		name: "amend-add", golden: "amend-add.golden.md", wantTasks: 6,
		ops: []op{
			{do: "next-id", milestone: 1, wantID: "T1.4"},
			{do: "insert", milestone: 1, text: privateEntry},
			{do: "depend", task: "T2.2", on: "{id}", touched: []string{"T2.2"}},
		},
	},
	{
		// A task that must run between two others, while the task before it
		// is running: a letter id, placed after that task without touching
		// it, and the following task made to wait for it.
		name: "amend-between", golden: "amend-between.golden.md", wantTasks: 6,
		held: map[string]string{"T1.1": "running"},
		ops: []op{
			{do: "between", after: "T1.1", wantID: "T1.1a"},
			{do: "insert", after: "T1.1", text: checkEntry},
			{do: "depend", task: "T1.2", on: "{id}", touched: []string{"T1.2"}},
		},
	},
	{
		// A task split in two while it is held: the parts go after it under
		// letter ids, and the old task only gains an Amended line, which is
		// all rule 8 lets a held task gain.
		name: "amend-split", golden: "amend-split.golden.md", wantTasks: 7,
		held: map[string]string{"T2.2": "shipping"},
		ops: []op{
			{do: "between", after: "T2.2", wantID: "T2.2a"},
			{do: "insert", after: "T2.2", text: linkEntry},
			{do: "between", after: "{id}", wantID: "T2.2b"},
			{do: "insert", after: "T2.2a", text: sharedPageEntry},
			{
				do:      "patch",
				old:     t22Acceptance,
				new:     strings.Replace(t22Acceptance, "\n\n", "\n- **Amended 2026-10-13:** split into T2.2a and T2.2b, which replace this task.\n\n", 1),
				touched: []string{"T2.2"},
			},
		},
	},
	{
		// Three plain patches after reading a design document against the
		// plan: a decision, a task's Scope and an open question.
		name: "reconcile", golden: "reconcile.golden.md", wantTasks: 5,
		ops: []op{
			{
				do:  "patch",
				old: "| Pages | Plain HTML rendered on the server, no script |",
				new: "| Pages | Plain HTML rendered on the server, no script, with the colours and type of `docs/design-system.md` |",
			},
			{
				do:      "patch",
				old:     "- **Scope:** A print style sheet for the recipe page.",
				new:     "- **Scope:** A print style sheet for the recipe page, with the print sizes of `docs/design-system.md`.",
				touched: []string{"T2.1"},
			},
			{
				do:  "patch",
				old: "| Which paper size should the print page fit? | T2.1 |",
				new: "| Which paper size should the print page fit? **Answered 2026-10-13:** A4, as `docs/design-system.md` says. | T2.1 |",
			},
		},
	},
}

// TestModes runs one scripted session per mode on testdata/base-plan.md and
// compares the plan it ends at with a golden file. Run with -update after
// the package path to rewrite the golden files.
//
// These rows are what stands in for a real planning session, which no test
// can run: they show that the ids an agent is given and the places its
// entries land give a plan the daemon can read, for each of the three ways
// of changing one. The load-bearing rows are "amend-between" and
// "amend-split", which run with the neighbouring task held and so fail if a
// builder ever rewrites a line of a running task, and "a running task
// stops the script", which shows the refusal is at the step that breaks
// rule 8 and leaves the plan as the step before it left it.
func TestModes(t *testing.T) {
	basePlan := readFixture(t, "base-plan.md")

	for _, tt := range modes {
		t.Run(tt.name, func(t *testing.T) {
			got, ran, err := runScript(t, basePlan, tt.ops, tt.held)
			if err != nil {
				t.Fatalf("step %d (%s) failed: %v", ran, tt.ops[ran].do, err)
			}
			if r := planlint.Lint(got); r.Summary.Errors != 0 {
				t.Errorf("the plan after the script has %d checker errors: %+v", r.Summary.Errors, r.Findings)
			}
			path := filepath.Join("testdata", tt.golden)
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/planpatch/ -update to write it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("the plan after the %s script is not %s: %s\nIf the change is meant, run go test ./internal/planpatch/ -update", tt.name, path, firstDifference(got, want))
			}
		})
	}

	t.Run("a running task stops the script", func(t *testing.T) {
		ops := []op{
			{do: "between", after: "T1.1", wantID: "T1.1a"},
			{do: "insert", after: "T1.1", text: checkEntry},
			{do: "depend", task: "T1.2", on: "{id}", touched: []string{"T1.2"}},
			{do: "patch", old: "- Photos of the dish.\n", new: "- Photos of the dish.\n- Videos.\n"},
		}
		held := map[string]string{"T1.2": "running"}
		got, ran, err := runScript(t, basePlan, ops, held)
		var he *HeldError
		if !errors.As(err, &he) || he.Task != "T1.2" || he.Status != "running" {
			t.Fatalf("error = %v, want a *HeldError for T1.2 running", err)
		}
		if ran != 2 {
			t.Errorf("the script stopped at step %d, want 2 (the depend step)", ran)
		}
		// The plan is what the steps before the refused one made, and the
		// refused step and the one after it left no mark on it.
		want, wantRan, err := runScript(t, basePlan, ops[:2], held)
		if err != nil || wantRan != 2 {
			t.Fatalf("the first two steps failed on their own: step %d, %v", wantRan, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("the refused step changed the plan: %s", firstDifference(got, want))
		}
		if bytes.Contains(got, []byte("Videos")) {
			t.Errorf("a step after the refused one ran")
		}
		if bytes.Equal(got, basePlan) {
			t.Errorf("the plan is the base plan, so the steps before the refused one did not run")
		}
	})
}

// TestFixturesLintClean guards the fixtures themselves. base-plan.md must
// have no finding at all, so anything the checker says about a patched plan
// comes from the patch; and each golden plan must have no error, because a
// golden file is a claim that the mode leaves a plan the daemon can run.
func TestFixturesLintClean(t *testing.T) {
	t.Run("base-plan.md", func(t *testing.T) {
		r := planlint.Lint(readFixture(t, "base-plan.md"))
		if len(r.Findings) != 0 {
			t.Errorf("findings: %+v", r.Findings)
		}
		if r.Summary.Milestones != 2 || r.Summary.Tasks != 5 || r.Summary.OpenQuestions != 2 {
			t.Errorf("summary = %+v, want 2 milestones, 5 tasks, 2 open questions", r.Summary)
		}
	})
	for _, tt := range modes {
		t.Run(tt.golden, func(t *testing.T) {
			r := planlint.Lint(readFixture(t, tt.golden))
			if r.Summary.Errors != 0 {
				t.Errorf("%d checker errors: %+v", r.Summary.Errors, r.Findings)
			}
			if r.Summary.Tasks != tt.wantTasks {
				t.Errorf("%d tasks, want %d", r.Summary.Tasks, tt.wantTasks)
			}
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// firstDifference says where two plans first differ, so a golden failure
// points at a line and does not print two whole plans.
func firstDifference(got, want []byte) string {
	g, w := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl || i >= len(g) || i >= len(w) {
			return fmt.Sprintf("first difference at line %d\n  got:    %q\n  golden: %q", i+1, gl, wl)
		}
	}
	return "no difference"
}
