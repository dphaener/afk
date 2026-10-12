package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/nexttask"
)

// TestNextLine pins one owner-facing sentence per decision.
//
// Load-bearing rows: "task" has the id first and two spaces (scripts split on
// it); "waiting with failed" proves the failed sentence is appended rather
// than replacing the wait; "blocked by failed" carries the recovery command;
// "owner task" must NOT have the id-two-spaces-title shape, because that shape
// is what a script launches an agent for, and an owner task must never get one.
func TestNextLine(t *testing.T) {
	tasks := []masterplan.Task{{ID: "T1.1", Title: "First"}, {ID: "T1.2", Title: "Second"},
		{ID: "T0.0", Title: "Stand up the VM (owner; not an agent task)"}}
	l := ledger.Ledger{Tasks: []ledger.Entry{
		{ID: "T1.1", Status: ledger.Running},
		{ID: "T1.2", Status: ledger.Shipping},
	}}
	tests := []struct {
		name string
		d    nexttask.Decision
		want string
	}{
		{"task", nexttask.Decision{Task: "T1.2"}, "T1.2  Second"},
		{"owner task", nexttask.Decision{Task: "T0.0", Owner: true}, "T0.0 is yours to do; the daemon will ask you to confirm it in the inbox. It is: Stand up the VM (owner; not an agent task)"},
		{"owner task not in the plan read", nexttask.Decision{Task: "T9.9", Owner: true}, "T9.9 is yours to do; the daemon will ask you to confirm it in the inbox."},
		{"in progress, one", nexttask.Decision{Reason: nexttask.ReasonInProgress, InProgress: []string{"T1.1"}}, "Nothing to start: T1.1 is already running."},
		{"in progress, shipping", nexttask.Decision{Reason: nexttask.ReasonInProgress, InProgress: []string{"T1.2"}}, "Nothing to start: T1.2 is already shipping."},
		{"in progress, two", nexttask.Decision{Reason: nexttask.ReasonInProgress, InProgress: []string{"T1.1", "T1.2"}}, "Nothing to start: T1.1 is already running; T1.2 is already shipping."},
		{"all done", nexttask.Decision{Reason: nexttask.ReasonAllDone}, "Nothing to start: every task is done."},
		{"waiting without items", nexttask.Decision{Reason: nexttask.ReasonWaitingOnOwner, Parked: []string{"T1.1"}}, "Nothing to start: waiting on you for T1.1."},
		{"waiting with items", nexttask.Decision{Reason: nexttask.ReasonWaitingOnOwner, Parked: []string{"T1.1", "T1.2"}, WaitingOn: []string{"i-1", "i-2"}}, "Nothing to start: waiting on you for T1.1, T1.2 (inbox items i-1, i-2)."},
		{"waiting with failed", nexttask.Decision{Reason: nexttask.ReasonWaitingOnOwner, Parked: []string{"T1.1"}, WaitingOn: []string{"i-1"}, Failed: []string{"T1.2"}}, "Nothing to start: waiting on you for T1.1 (inbox items i-1). Also blocked by failed T1.2."},
		{"blocked by failed", nexttask.Decision{Reason: nexttask.ReasonBlockedByFailed, Failed: []string{"T1.1", "T1.2"}}, `Nothing to start: blocked by failed T1.1, T1.2. Fix the cause, then run "afk ledger set <id> pending" to try again.`},
		{"stuck", nexttask.Decision{Reason: nexttask.ReasonStuck, Stuck: []string{"T1.1"}}, `Nothing to start: T1.1 cannot become ready. Run "afk tasks" to see what each one waits for.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextLine(tt.d, tasks, l); got != tt.want {
				t.Errorf("got %q want %q", got, tt.want)
			}
		})
	}
}

func runNextCmd(t *testing.T, project string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var so, se bytes.Buffer
	code = runNext(append([]string{"--project", project}, args...), &so, &se, project, "", fakeCommonDir(""))
	return code, so.String(), se.String()
}

// TestNextOutcomes runs the command end to end for each kind of answer.
//
// Load-bearing rows: "ready task" is the one line a script reads; every
// "nothing to start" row must still exit 0, because an empty queue is not an
// error; "afk.toml points at another plan" proves the configured plan is used.
func TestNextOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		ledger []string
		toml   bool
		want   string
	}{
		{name: "ready task", ledger: []string{"T1.1=done"}, want: "T1.2  Second\n"},
		{name: "first task when nothing recorded", want: "T1.1  First\n"},
		{name: "in progress", ledger: []string{"T1.1=running"}, want: "Nothing to start: T1.1 is already running.\n"},
		{name: "all done", ledger: []string{"T1.1=done", "T1.2=done", "T1.2a=done", "T1.2b=done"}, want: "Nothing to start: every task is done.\n"},
		{name: "waiting on owner", ledger: []string{"T1.1=parked"}, want: "Nothing to start: waiting on you for T1.1.\n"},
		{name: "blocked by failed", ledger: []string{"T1.1=failed"}, want: "Nothing to start: blocked by failed T1.1. Fix the cause, then run \"afk ledger set <id> pending\" to try again.\n"},
		{name: "stuck", ledger: []string{"T1.1=superseded:T9.9"}, want: "Nothing to start: T1.2, T1.2a, T1.2b cannot become ready. Run \"afk tasks\" to see what each one waits for.\n"},
		{name: "afk.toml points at another plan", toml: true, want: "T7.1  Solo\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := ledgerCmdProject(t, ledgerCmdPlan)
			if tt.toml {
				writeBuildFile(t, project, "afk.toml", "plan = \"other.md\"\n")
				writeBuildFile(t, project, "other.md", "#### T7.1 — Solo\n\n- **Depends on:** none\n")
			}
			seedLedger(t, project, tt.ledger...)
			code, stdout, stderr := runNextCmd(t, project)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			if stdout != tt.want {
				t.Errorf("stdout %q want %q", stdout, tt.want)
			}
		})
	}
}

// TestNextOwnerTask runs the command for a plan whose next task is the
// owner's. Load-bearing: exit 0 with one line on stdout, as for any other
// answer, and once the owner task is done the next line is an ordinary task
// again; "(owner + agent)" is an agent's task and keeps the ordinary shape.
func TestNextOwnerTask(t *testing.T) {
	const plan = "#### T0.0 — Stand up the VM (owner; not an agent task)\n\n- **Depends on:** none\n\n" +
		"#### T0.1 — Wire it up (owner + agent)\n\n- **Depends on:** T0.0\n"
	tests := []struct {
		name   string
		ledger []string
		want   string
	}{
		{name: "owner task is next", want: "T0.0 is yours to do; the daemon will ask you to confirm it in the inbox. It is: Stand up the VM (owner; not an agent task)\n"},
		{name: "owner task done", ledger: []string{"T0.0=done"}, want: "T0.1  Wire it up (owner + agent)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := ledgerCmdProject(t, plan)
			seedLedger(t, project, tt.ledger...)
			code, stdout, stderr := runNextCmd(t, project)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			if stdout != tt.want {
				t.Errorf("stdout %q want %q", stdout, tt.want)
			}
		})
	}
}

// TestNextWithInbox runs afk next against a real inbox folder.
//
// Load-bearing rows: "answered item" releases the parked task (the whole point
// of reading the inbox); "open item" keeps it parked; "item missing" keeps it
// parked, since a deleted item is not an answer.
func TestNextWithInbox(t *testing.T) {
	tests := []struct {
		name   string
		answer bool
		add    bool
		want   string
	}{
		{name: "answered item", add: true, answer: true, want: "T1.1  First\n"},
		{name: "open item", add: true, want: "Nothing to start: waiting on you for T1.1 (inbox items ITEM).\n"},
		{name: "item missing", want: "Nothing to start: waiting on you for T1.1 (inbox items abcd23).\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := ledgerCmdProject(t, ledgerCmdPlan)
			id := "abcd23"
			if tt.add {
				store := inbox.Store{Dir: inbox.Dir(project)}
				it, err := store.Add(inbox.NewItem{Task: "T1.1", Kind: "question", Blocking: true, Title: "Which?"})
				if err != nil {
					t.Fatal(err)
				}
				id = it.ID
				if tt.answer {
					if _, err := store.Answer(id, "yes", "cli"); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := ledger.Update(project, func(l *ledger.Ledger) error {
				if _, err := l.Set("T1.1", ledger.Parked, nil); err != nil {
					return err
				}
				for i := range l.Tasks {
					if l.Tasks[i].ID == "T1.1" {
						l.Tasks[i].ParkedOn = []string{id}
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := runNextCmd(t, project)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			want := strings.ReplaceAll(tt.want, "ITEM", id)
			if stdout != want {
				t.Errorf("stdout %q want %q", stdout, want)
			}
		})
	}
}

// TestNextErrors pins exit codes and messages; stdout stays empty so a script
// never mistakes an error for a task id.
func TestNextErrors(t *testing.T) {
	const cyclePlan = `#### T1.1 — A

- **Depends on:** T1.2

#### T1.2 — B

- **Depends on:** T1.1
`
	tests := []struct {
		name  string
		plan  string
		args  []string
		setup func(t *testing.T, project string)
		code  int
		want  []string
	}{
		{name: "positional word", plan: ledgerCmdPlan, args: []string{"T1.1"}, code: 2, want: []string{"unexpected word \"T1.1\"", "Usage: afk next"}},
		{name: "unknown flag", plan: ledgerCmdPlan, args: []string{"--json"}, code: 2},
		{name: "missing plan", plan: "", code: 1, want: []string{"no master plan at", "docs/PLAN.md"}},
		{name: "cycle", plan: cyclePlan, code: 1, want: []string{"T1.1", "T1.2", "cycle"}},
		{
			name: "damaged ledger", plan: ledgerCmdPlan, code: 1,
			setup: func(t *testing.T, project string) { writeLedgerFile(t, project, "{not json") },
			want:  []string{"damaged", "Fix or delete"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := ledgerCmdProject(t, tt.plan)
			if tt.setup != nil {
				tt.setup(t, project)
			}
			code, stdout, stderr := runNextCmd(t, project, tt.args...)
			if code != tt.code {
				t.Fatalf("code=%d want %d; stderr=%q", code, tt.code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout should be empty, got %q", stdout)
			}
			for _, w := range tt.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr %q lacks %q", stderr, w)
				}
			}
		})
	}
}

func TestNextHelp(t *testing.T) {
	var so, se bytes.Buffer
	if code := runNext([]string{"--help"}, &so, &se, t.TempDir(), "", fakeCommonDir("")); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(se.String(), "afk next") {
		t.Errorf("no usage: %q", se.String())
	}
}
