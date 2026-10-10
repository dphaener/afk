package plancheck

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/state"
)

// basePlan is a small plan with one task in each checkbox state, a fenced
// example that quotes a task line, and a task line outside `## Waves`.
//
// The fenced example carries its own **Model** line, and W1.T1's Model sits
// above the fence, because state.ParsePlan does not know about code fences:
// it reads the quoted line as a task. Laid out this way the fixture still
// parses there, so every row can be checked through the real reader.
const basePlan = `# Demo plan

## Context

A stray line that looks like a task but is not in the Waves section:

- [ ] (W1.T1) Not a real task

## Waves

### Wave 1: First

- [ ] (W1.T1) Add the thing

  **Model**: heavy

  **What to do**: Write it. The line form is:

  ` + "```" + `
- [ ] (W1.T2) An example inside a fence

  **Model**: light
  ` + "```" + `

- [x] (W1.T3) Already finished

  **Model**: light

- [!] (W1.T4) Broke early FAILED: the first reason

  **Model**: balanced

### Wave 2: Second

- [ ] (W2.T1) Follow up

  **Model**: balanced
`

// parsedTask finds a task in a parsed plan.
func parsedTask(t *testing.T, plan []byte, id string) state.Task {
	t.Helper()
	p, err := state.ParsePlan(plan)
	if err != nil {
		t.Fatalf("output does not parse as a plan: %v\n%s", err, plan)
	}
	for _, w := range p.Waves {
		for _, task := range w.Tasks {
			if task.ID == id {
				return task
			}
		}
	}
	t.Fatalf("parsed plan has no task %s", id)
	return state.Task{}
}

// changedLines returns the indexes of the lines that differ. It fails the
// test when the line counts differ, since the line count must never change.
func changedLines(t *testing.T, before, after []byte) []int {
	t.Helper()
	b := strings.Split(string(before), "\n")
	a := strings.Split(string(after), "\n")
	if len(a) != len(b) {
		t.Fatalf("line count changed: %d -> %d", len(b), len(a))
	}
	var diff []int
	for i := range b {
		if a[i] != b[i] {
			diff = append(diff, i)
		}
	}
	return diff
}

// TestPlanMark pins the line Mark writes for every start state and target.
//
// Load-bearing rows:
//   - "failed over failed with another reason": the first reason must win and
//     changed must be false, because Apply uses changed to decide whether to
//     append to issues.md. A true here logs every retry as a new failure.
//   - "done over failed": the title must come back exactly, with the FAILED
//     suffix gone, or a task that was retried and passed keeps a stale reason
//     glued to its name.
//   - "fenced example is not ticked" and "line outside Waves is not ticked":
//     both use an id that also has a real task line later (or no real one), so
//     a Mark that takes the first textual match ticks the wrong line and the
//     real task stays unticked for ever.
//   - "reason with newlines": a newline in the output adds lines to the plan
//     and breaks every line number the execute workflow holds.
//   - the CRLF and no-trailing-newline rows: the bytes outside the one line
//     must survive, including the `\r` on the changed line itself.
//
// Every changed output is parsed back through state.ParsePlan and compared
// with wantState/wantTitle/wantReason: that parser is the reader of these
// lines, so agreeing with it is the actual contract.
func TestPlanMark(t *testing.T) {
	crlf := strings.ReplaceAll(basePlan, "\n", "\r\n")
	noFinalNewline := strings.TrimSuffix(basePlan, "\n")
	// The only real W1.T2 sits after a fenced example that quotes it.
	fencedThenReal := strings.Replace(basePlan, "### Wave 2: Second", "- [ ] (W1.T2) The real second task\n\n  **Model**: light\n\n### Wave 2: Second", 1)

	cases := []struct {
		name        string
		plan        string
		id          string
		target      Target
		reason      string
		wantErr     error
		wantChanged bool
		wantLine    string // the changed line, without its line ending
		wantState   state.CheckState
		wantTitle   string
		wantReason  string
		// noParseBack skips the state.ParsePlan check for the one fixture
		// that parser cannot read: it is blind to code fences, so a fenced
		// example followed by the real task with the same id is a duplicate
		// id to it. plan_to_args.rb does skip fences, and Mark sides with it.
		noParseBack bool
	}{
		{name: "todo to done", plan: basePlan, id: "W1.T1", target: Done,
			wantChanged: true, wantLine: "- [x] (W1.T1) Add the thing",
			wantState: state.CheckComplete, wantTitle: "Add the thing"},
		{name: "todo to failed keeps the title", plan: basePlan, id: "W1.T1", target: Failed, reason: "tests fail",
			wantChanged: true, wantLine: "- [!] (W1.T1) Add the thing FAILED: tests fail",
			wantState: state.CheckFailed, wantTitle: "Add the thing", wantReason: "tests fail"},
		{name: "done repeated", plan: basePlan, id: "W1.T3", target: Done,
			wantChanged: false, wantState: state.CheckComplete, wantTitle: "Already finished"},
		{name: "failed over failed with another reason", plan: basePlan, id: "W1.T4", target: Failed, reason: "a second reason",
			wantChanged: false, wantState: state.CheckFailed, wantTitle: "Broke early", wantReason: "the first reason"},
		{name: "failed over done", plan: basePlan, id: "W1.T3", target: Failed, reason: "review found a bug",
			wantChanged: true, wantLine: "- [!] (W1.T3) Already finished FAILED: review found a bug",
			wantState: state.CheckFailed, wantTitle: "Already finished", wantReason: "review found a bug"},
		{name: "done over failed", plan: basePlan, id: "W1.T4", target: Done,
			wantChanged: true, wantLine: "- [x] (W1.T4) Broke early",
			wantState: state.CheckComplete, wantTitle: "Broke early"},
		{name: "failed with empty reason", plan: basePlan, id: "W1.T1", target: Failed, reason: "",
			wantErr: ErrEmptyReason},
		{name: "failed with blank reason", plan: basePlan, id: "W1.T1", target: Failed, reason: " \n\t ",
			wantErr: ErrEmptyReason},
		{name: "reason with newlines", plan: basePlan, id: "W1.T1", target: Failed, reason: "exit 1\r\n  panic: boom\n\tat main.go:3\n",
			wantChanged: true, wantLine: "- [!] (W1.T1) Add the thing FAILED: exit 1 panic: boom at main.go:3",
			wantState: state.CheckFailed, wantTitle: "Add the thing", wantReason: "exit 1 panic: boom at main.go:3"},
		{name: "reason that itself says FAILED", plan: basePlan, id: "W1.T1", target: Failed, reason: "suite FAILED: 3 tests",
			wantChanged: true, wantLine: "- [!] (W1.T1) Add the thing FAILED: suite FAILED: 3 tests",
			wantState: state.CheckFailed, wantTitle: "Add the thing", wantReason: "suite FAILED: 3 tests"},
		{name: "id in parentheses", plan: basePlan, id: "(W2.T1)", target: Done,
			wantChanged: true, wantLine: "- [x] (W2.T1) Follow up",
			wantState: state.CheckComplete, wantTitle: "Follow up"},
		{name: "id in lower case", plan: basePlan, id: " w2.t1 ", target: Done,
			wantChanged: true, wantLine: "- [x] (W2.T1) Follow up",
			wantState: state.CheckComplete, wantTitle: "Follow up"},
		{name: "unknown id", plan: basePlan, id: "W9.T9", target: Done, wantErr: ErrUnknownTask},
		{name: "malformed id", plan: basePlan, id: "T1", target: Done, wantErr: ErrBadTaskID},
		{name: "id that is a prefix of another", plan: strings.Replace(basePlan, "(W2.T1)", "(W2.T10)", 1), id: "W2.T1", target: Done,
			wantErr: ErrUnknownTask},
		{name: "fenced example is not ticked", plan: basePlan, id: "W1.T2", target: Done, wantErr: ErrUnknownTask},
		{name: "real task after a fenced example", plan: fencedThenReal, id: "W1.T2", target: Done,
			wantChanged: true, wantLine: "- [x] (W1.T2) The real second task", noParseBack: true},
		{name: "line outside Waves is not ticked", plan: basePlan, id: "W1.T1", target: Done,
			wantChanged: true, wantLine: "- [x] (W1.T1) Add the thing",
			wantState: state.CheckComplete, wantTitle: "Add the thing"},
		{name: "no Waves section", plan: "# P\n\n- [ ] (W1.T1) Loose\n", id: "W1.T1", target: Done, wantErr: ErrUnknownTask},
		{name: "CRLF line endings kept", plan: crlf, id: "W1.T1", target: Failed, reason: "bad",
			wantChanged: true, wantLine: "- [!] (W1.T1) Add the thing FAILED: bad\r",
			wantState: state.CheckFailed, wantTitle: "Add the thing", wantReason: "bad"},
		{name: "no trailing newline kept", plan: noFinalNewline, id: "W2.T1", target: Done,
			wantChanged: true, wantLine: "- [x] (W2.T1) Follow up",
			wantState: state.CheckComplete, wantTitle: "Follow up"},
		{name: "task with no title", plan: "# P\n\n## Waves\n\n### Wave 1: A\n\n- [ ] (W1.T1)\n\n  **Model**: light\n", id: "W1.T1", target: Failed, reason: "bad",
			wantChanged: true, wantLine: "- [!] (W1.T1) FAILED: bad",
			wantState: state.CheckFailed, wantTitle: "", wantReason: "bad"},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			before := []byte(tt.plan)
			keep := append([]byte(nil), before...)
			out, changed, err := Mark(before, tt.id, tt.target, tt.reason)
			if !bytes.Equal(before, keep) {
				t.Fatal("Mark modified its input slice")
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if out != nil || changed {
					t.Errorf("on error got out=%q changed=%v, want nil/false", out, changed)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}
			diff := changedLines(t, before, out)
			if !tt.wantChanged {
				if !bytes.Equal(out, before) {
					t.Fatalf("no-op changed the bytes:\n%s", out)
				}
			} else {
				if len(diff) != 1 {
					t.Fatalf("%d lines changed (%v), want exactly 1", len(diff), diff)
				}
				got := strings.Split(string(out), "\n")[diff[0]]
				if got != tt.wantLine {
					t.Errorf("line = %q, want %q", got, tt.wantLine)
				}
				if strings.HasSuffix(tt.plan, "\n") != strings.HasSuffix(string(out), "\n") {
					t.Error("trailing newline was not preserved")
				}
			}

			if tt.noParseBack {
				return
			}
			id, _ := NormalizeID(tt.id)
			task := parsedTask(t, out, id)
			if task.State != tt.wantState || task.Title != tt.wantTitle || task.FailureReason != tt.wantReason {
				t.Errorf("parsed back as {state:%v title:%q reason:%q}, want {state:%v title:%q reason:%q}",
					task.State, task.Title, task.FailureReason, tt.wantState, tt.wantTitle, tt.wantReason)
			}
		})
	}
}

// TestPlanMarkRoundTrip walks one task through failed and back to done and checks
// the plan ends byte-identical to a plan that was only ever marked done. That
// is the property that makes a retry after a failure invisible afterwards.
func TestPlanMarkRoundTrip(t *testing.T) {
	direct, _, err := Mark([]byte(basePlan), "W1.T1", Done, "")
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := Mark([]byte(basePlan), "W1.T1", Failed, "first try broke")
	if err != nil {
		t.Fatal(err)
	}
	back, changed, err := Mark(failed, "W1.T1", Done, "")
	if err != nil || !changed {
		t.Fatalf("done over failed: changed=%v err=%v", changed, err)
	}
	if !bytes.Equal(back, direct) {
		t.Errorf("failed-then-done differs from done:\n%s\n---\n%s", back, direct)
	}
}

func fixedNow() time.Time { return time.Date(2026, 10, 10, 12, 30, 0, 0, time.FixedZone("x", 3600)) }

// writePlan lays a plan out under a temp project and returns the project dir.
func writePlan(t *testing.T, session, content string) string {
	t.Helper()
	project := t.TempDir()
	path := PlanPath(project, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return project
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPlanApply covers what the file-level function adds to Mark: the issues.md
// entry, the no-op that writes nothing, and the errors that must leave the
// project untouched.
//
// Load-bearing rows: "failed again" must leave issues.md byte-identical (the
// first reason wins, C1), and "missing plan" must create nothing at all, not
// even the lock file, since the usual cause is a mistyped session name in
// somebody's project.
func TestPlanApply(t *testing.T) {
	t.Run("done", func(t *testing.T) {
		project := writePlan(t, "demo", basePlan)
		res, err := Apply(Request{Project: project, Session: "demo", TaskID: "(W1.T1)", Target: Done, Now: fixedNow})
		if err != nil || !res.Changed {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		got := readFile(t, res.PlanPath)
		if diff := changedLines(t, []byte(basePlan), []byte(got)); len(diff) != 1 {
			t.Errorf("changed lines = %v, want one", diff)
		}
		if parsedTask(t, []byte(got), "W1.T1").State != state.CheckComplete {
			t.Error("W1.T1 is not complete")
		}
		if _, err := os.Stat(res.IssuesPath); !os.IsNotExist(err) {
			t.Errorf("done must not create issues.md (stat err = %v)", err)
		}
		if info, err := os.Stat(res.PlanPath); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("plan mode = %v (err %v), want the original 0600", info.Mode().Perm(), err)
		}
		entries, _ := os.ReadDir(filepath.Dir(res.PlanPath))
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp.") {
				t.Errorf("temp file left behind: %s", e.Name())
			}
		}
	})

	t.Run("failed then failed again then done", func(t *testing.T) {
		project := writePlan(t, "demo", basePlan)
		req := Request{Project: project, Session: "demo", TaskID: "W1.T1", Target: Failed, Reason: "exit 1\nboom", Now: fixedNow}
		res, err := Apply(req)
		if err != nil || !res.Changed {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		wantIssues := "## (W1.T1) FAILED\n\n2026-10-10T11:30:00Z: exit 1 boom\n"
		if got := readFile(t, res.IssuesPath); got != wantIssues {
			t.Errorf("issues.md = %q, want %q", got, wantIssues)
		}
		planAfterFirst := readFile(t, res.PlanPath)

		req.Reason = "a different reason"
		res, err = Apply(req)
		if err != nil || res.Changed {
			t.Fatalf("failed again: res=%+v err=%v, want unchanged", res, err)
		}
		if got := readFile(t, res.IssuesPath); got != wantIssues {
			t.Errorf("failed again rewrote issues.md: %q", got)
		}
		if got := readFile(t, res.PlanPath); got != planAfterFirst {
			t.Errorf("failed again rewrote the plan")
		}

		req.Target, req.Reason = Done, ""
		if res, err = Apply(req); err != nil || !res.Changed {
			t.Fatalf("done over failed: res=%+v err=%v", res, err)
		}
		task := parsedTask(t, []byte(readFile(t, res.PlanPath)), "W1.T1")
		if task.State != state.CheckComplete || task.Title != "Add the thing" {
			t.Errorf("after done: %+v", task)
		}
		if got := readFile(t, res.IssuesPath); got != wantIssues {
			t.Errorf("done changed issues.md: %q", got)
		}
	})

	t.Run("failed appends after existing notes", func(t *testing.T) {
		project := writePlan(t, "demo", basePlan)
		issues := IssuesPath(project, "demo")
		if err := os.MkdirAll(filepath.Dir(issues), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(issues, []byte("## (W1.T3) note\n\nno newline at the end"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(Request{Project: project, Session: "demo", TaskID: "W2.T1", Target: Failed, Reason: "bad", Now: fixedNow}); err != nil {
			t.Fatal(err)
		}
		want := "## (W1.T3) note\n\nno newline at the end\n\n## (W2.T1) FAILED\n\n2026-10-10T11:30:00Z: bad\n"
		if got := readFile(t, issues); got != want {
			t.Errorf("issues.md = %q, want %q", got, want)
		}
	})

	errCases := []struct {
		name    string
		session string
		id      string
		target  Target
		reason  string
		wantErr error
	}{
		{name: "missing plan", session: "nope", id: "W1.T1", target: Done, wantErr: ErrNoPlan},
		{name: "unknown task", session: "demo", id: "W9.T9", target: Done, wantErr: ErrUnknownTask},
		{name: "empty reason", session: "demo", id: "W1.T1", target: Failed, wantErr: ErrEmptyReason},
		{name: "bad task id", session: "demo", id: "one", target: Done, wantErr: ErrBadTaskID},
		{name: "session with a path in it", session: "../demo", id: "W1.T1", target: Done, wantErr: ErrBadSession},
		{name: "empty session", session: "", id: "W1.T1", target: Done, wantErr: ErrBadSession},
	}
	for _, tt := range errCases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			project := writePlan(t, "demo", basePlan)
			_, err := Apply(Request{Project: project, Session: tt.session, TaskID: tt.id, Target: tt.target, Reason: tt.reason, Now: fixedNow})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := readFile(t, PlanPath(project, "demo")); got != basePlan {
				t.Error("the plan changed on an error")
			}
			if _, err := os.Stat(IssuesPath(project, "demo")); !os.IsNotExist(err) {
				t.Error("issues.md was created on an error")
			}
			if tt.wantErr == ErrNoPlan {
				if _, err := os.Stat(lockPath(project, tt.session)); !os.IsNotExist(err) {
					t.Error("a lock file was created for a plan that does not exist")
				}
			}
		})
	}

	t.Run("missing project", func(t *testing.T) {
		project := filepath.Join(t.TempDir(), "not-here")
		if _, err := Apply(Request{Project: project, Session: "demo", TaskID: "W1.T1", Target: Done}); !errors.Is(err, ErrNoPlan) {
			t.Fatalf("err = %v, want ErrNoPlan", err)
		}
		if _, err := os.Stat(project); !os.IsNotExist(err) {
			t.Error("Apply created the project directory")
		}
	})
}

// TestPlanApplyConcurrentWritersAllSurvive is the reason this package exists.
// Many writers mark different tasks of one plan at the same moment; every
// mark must be in the file afterwards and every failure in issues.md once.
//
// Without the flock in Apply this fails: each writer reads the plan, changes
// its own line and renames, and a later rename replaces an earlier writer's
// file, dropping that task's mark with no error. (Checked by taking the Flock
// call out: the test then fails on every run. In one process the writers
// also share a temp file name, so the failure usually shows up as a failed
// rename or a task missing from a half-written plan before it shows up as a
// lost mark.) Each goroutine opens its
// own descriptor on the lock file, and flock locks belong to the open file
// description, so goroutines in one process exclude each other exactly as
// separate `afk plan check` processes do.
//
// Run with -count=20 when touching Apply.
func TestPlanApplyConcurrentWritersAllSurvive(t *testing.T) {
	const tasks = 24
	var b strings.Builder
	b.WriteString("# Race\n\n## Waves\n\n### Wave 1: All at once\n\n")
	for i := 1; i <= tasks; i++ {
		fmt.Fprintf(&b, "- [ ] (W1.T%d) Task number %d\n\n  **Model**: light\n\n", i, i)
	}
	original := b.String()
	project := writePlan(t, "race", original)

	start := make(chan struct{})
	errs := make(chan error, tasks)
	var wg sync.WaitGroup
	for i := 1; i <= tasks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := Request{Project: project, Session: "race", TaskID: fmt.Sprintf("W1.T%d", i), Target: Done, Now: fixedNow}
			if i%2 == 0 {
				req.Target, req.Reason = Failed, fmt.Sprintf("reason %d", i)
			}
			<-start
			res, err := Apply(req)
			if err == nil && !res.Changed {
				err = fmt.Errorf("task %d: reported unchanged", i)
			}
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got := []byte(readFile(t, PlanPath(project, "race")))
	if diff := changedLines(t, []byte(original), got); len(diff) != tasks {
		t.Errorf("%d lines changed, want %d", len(diff), tasks)
	}
	issues := readFile(t, IssuesPath(project, "race"))
	for i := 1; i <= tasks; i++ {
		id := fmt.Sprintf("W1.T%d", i)
		task := parsedTask(t, got, id)
		if i%2 == 0 {
			if task.State != state.CheckFailed || task.FailureReason != fmt.Sprintf("reason %d", i) {
				t.Errorf("%s: lost its failed mark: %+v", id, task)
			}
			if n := strings.Count(issues, "## ("+id+") FAILED\n"); n != 1 {
				t.Errorf("%s: %d issues.md entries, want 1", id, n)
			}
		} else if task.State != state.CheckComplete {
			t.Errorf("%s: lost its done mark: %+v", id, task)
		}
		if task.Title != fmt.Sprintf("Task number %d", i) {
			t.Errorf("%s: title = %q", id, task.Title)
		}
	}
	if n := strings.Count(issues, "\n\n\n"); n != 0 {
		t.Errorf("issues.md has %d runs of blank lines: entries interleaved or were padded twice", n)
	}
}

// TestPlanNormalizeID pins the spellings of a task id the command accepts.
func TestPlanNormalizeID(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "W1.T2", want: "W1.T2"},
		{in: "(W1.T2)", want: "W1.T2"},
		{in: "  (w10.t12) ", want: "W10.T12"},
		{in: "W1.T2)", wantErr: true},
		{in: "W1", wantErr: true},
		{in: "", wantErr: true},
		{in: "W1.T2 extra", wantErr: true},
	}
	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			got, err := NormalizeID(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("NormalizeID(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
			}
		})
	}
}
