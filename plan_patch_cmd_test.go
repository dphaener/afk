package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/planpatch"
	"github.com/dphaener/afk/internal/ship"
)

// patchGit stands in for git in these tests, so none of them runs it. The
// zero value answers "not a git repository" to everything, which makes
// planpatch.Run change the plan in place. With repo set it is a repository
// whose plan is tracked and clean, on the branch main.
type patchGit struct {
	repo        bool
	origin      bool
	commitFails bool
	calls       [][]string
}

func (g *patchGit) run(dir, name string, args ...string) (string, error) {
	argv := append([]string{name}, args...)
	g.calls = append(g.calls, argv)
	line := strings.Join(argv, " ")
	exit := func(code int, stderr string) (string, error) {
		return "", &ship.CmdError{Cmd: line, Code: code, Stderr: stderr, Err: fmt.Errorf("exit status %d", code)}
	}
	if !g.repo {
		return exit(128, "fatal: not a git repository")
	}
	switch {
	case strings.HasPrefix(line, "git rev-parse --is-inside-work-tree"):
		return "true\n", nil
	case strings.HasPrefix(line, "git status --porcelain"):
		return "", nil
	case line == "git branch --show-current":
		return "main\n", nil
	case strings.HasPrefix(line, "git add"):
		return "", nil
	case strings.HasPrefix(line, "git commit"):
		if g.commitFails {
			return exit(1, "a hook said no")
		}
		return "", nil
	case line == "git remote get-url origin":
		if !g.origin {
			return exit(2, "error: No such remote 'origin'")
		}
		return "git@example.test:me/recipes.git\n", nil
	case strings.HasPrefix(line, "git push"):
		return "", nil
	}
	return exit(1, "the test's git does not know this command")
}

// writes returns the calls that change the repository: everything but the
// questions.
func (g *patchGit) writes() []string {
	var out []string
	for _, c := range g.calls {
		if len(c) > 1 && (c[1] == "add" || c[1] == "commit" || c[1] == "push") {
			out = append(out, strings.Join(c, " "))
		}
	}
	return out
}

const patchPlanRel = "docs/PLAN.md"

// patchBasePlan is internal/planpatch's fixture: two milestones, five tasks,
// no finding.
func patchBasePlan(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("internal", "planpatch", "testdata", "base-plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// patchRow is one command line against a temp project holding the base plan.
type patchRow struct {
	name string
	// files are the --old, --new and --entry files, written to the folder
	// the command is "typed in", so the rows name them by a relative path.
	files map[string]string
	// held marks tasks in the project's ledger before the command runs.
	held map[string]ledger.Status
	git  patchGit
	// noPlan runs against a project folder with no plan in it.
	noPlan bool
	args   []string

	want int
	// wantOut and wantErr are texts stdout and stderr must hold; exactOut,
	// when set, is the whole of stdout.
	wantOut, wantErr []string
	exactOut         string
	// notErr are texts stderr must not hold.
	notErr []string
	// unchanged asserts the plan is byte for byte the base plan; wantPlan
	// are texts the plan must hold afterwards.
	unchanged bool
	wantPlan  []string
	// wantWrites are the git commands that change the repository, in order.
	wantWrites []string
}

// runPatchRows runs every row through run in a fresh temp project.
func runPatchRows(t *testing.T, rows []patchRow, run func(args []string, stdout, stderr *bytes.Buffer, deps planPatchDeps) int) {
	t.Helper()
	base := patchBasePlan(t)
	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			project, typedIn := t.TempDir(), t.TempDir()
			if !tt.noPlan {
				writePlanFile(t, project, patchPlanRel, base)
			}
			for name, content := range tt.files {
				writePlanFile(t, typedIn, name, content)
			}
			if len(tt.held) > 0 {
				err := ledger.Update(project, func(l *ledger.Ledger) error {
					for id, st := range tt.held {
						if _, err := l.Set(id, st, nil); err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			git := tt.git
			deps := planPatchDeps{
				cwd:          typedIn,
				gitCommonDir: func(string) string { return "" },
				run:          git.run,
				tempDir:      func() (string, error) { return "", fmt.Errorf("no test here makes a worktree") },
			}
			var so, se bytes.Buffer
			code := run(append([]string{"--project", project}, tt.args...), &so, &se, deps)
			if code != tt.want {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.want, so.String(), se.String())
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(so.String(), w) {
					t.Errorf("stdout lacks %q:\n%s", w, so.String())
				}
			}
			if tt.exactOut != "" && so.String() != tt.exactOut {
				t.Errorf("stdout = %q, want %q", so.String(), tt.exactOut)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(se.String(), w) {
					t.Errorf("stderr lacks %q:\n%s", w, se.String())
				}
			}
			for _, w := range tt.notErr {
				if strings.Contains(se.String(), w) {
					t.Errorf("stderr holds %q and must not:\n%s", w, se.String())
				}
			}
			if tt.want != 0 && so.Len() > 0 {
				t.Errorf("a refused command printed on stdout:\n%s", so.String())
			}
			if tt.noPlan {
				// A mistyped --project must not leave afk's folder behind.
				if _, err := os.Stat(filepath.Join(project, ".afk")); err == nil {
					t.Errorf("an .afk folder was made in a folder with no plan")
				}
				return
			}
			got, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(patchPlanRel)))
			if err != nil {
				t.Fatal(err)
			}
			if tt.unchanged && string(got) != base {
				t.Errorf("the plan was changed by a command that must change nothing")
			}
			if tt.want == 0 && len(tt.wantPlan) == 0 && !tt.unchanged && string(got) == base {
				t.Errorf("the command exited 0 and the plan is unchanged")
			}
			for _, w := range tt.wantPlan {
				if !strings.Contains(string(got), w) {
					t.Errorf("the plan lacks %q:\n%s", w, got)
				}
			}
			if !reflect.DeepEqual(git.writes(), tt.wantWrites) {
				t.Errorf("git writes:\n  got  %q\n  want %q", git.writes(), tt.wantWrites)
			}
		})
	}
}

// Texts of the base plan the rows patch.
const (
	t11Scope      = "- **Scope:** The recipes table, the form and the recipe page.\n"
	t11ScopeNew   = "- **Scope:** The recipes table, the form, the recipe page and a delete button.\n"
	t11Acceptance = "- **Acceptance:** A recipe saved through the form is still there after a restart.\n"
	t11Amended    = "- Amended 2026-10-13: the form also takes a photo.\n"
	notInRepoNote = "is not a git repository, so the plan was changed in place and nothing was committed"
)

// TestRunPlanPatch is `afk plan patch` end to end against a temp project,
// with git faked.
//
// Load-bearing rows:
//   - "no match", "two matches", "adds a checker error", "edits a running
//     task": each must exit 1, leave the plan byte for byte as it was and end
//     with "Nothing was changed.". The planning session reads that line to
//     know it may try again; a plan changed by a refused patch is the one
//     thing the guarded patch exists to prevent.
//   - "adds a checker error" also holds the finding to one printing: the
//     error's own text lists the findings, and printing both would say
//     everything twice.
//   - "Amended line on a running task": rule 8's one allowance must get
//     through the command, or a running task could never be amended.
//   - "tracked and clean, with an origin": the only git commands that change
//     anything are add, commit and push, add and commit name the plan alone,
//     and nothing carries a forcing flag.
//   - "commit fails": the plan was written, so the refusal must not say
//     nothing was changed.
//   - "no plan in the project": says where it looked, and makes no .afk
//     folder in a folder that is not a project.
func TestRunPlanPatch(t *testing.T) {
	scope := map[string]string{"old.md": t11Scope, "new.md": t11ScopeNew}
	both := []string{"--old", "old.md", "--new", "new.md"}
	rows := []patchRow{
		{name: "one match", files: scope, args: both, want: 0,
			wantOut:  []string{"Patched docs/PLAN.md. Tasks changed: T1.1.\n", notInRepoNote},
			wantPlan: []string{t11ScopeNew}},
		{name: "outside every task",
			files: map[string]string{"old.md": "| Language | Go, standard library where it is enough |\n", "new.md": "| Language | Go |\n"},
			args:  both, want: 0,
			wantOut:  []string{"Patched docs/PLAN.md. No task's entry was changed.\n"},
			wantPlan: []string{"| Language | Go |\n"}},
		{name: "no match",
			files: map[string]string{"old.md": "this text is not in the plan\n", "new.md": "x\n"},
			args:  both, want: 1, unchanged: true,
			wantErr: []string{"afk plan patch: the old text is not in the plan.", "Nothing was changed.\n"}},
		{name: "two matches",
			files: map[string]string{"old.md": "- **Depends on:** T1.2\n", "new.md": "- **Depends on:** T1.1\n"},
			args:  both, want: 1, unchanged: true,
			wantErr: []string{"the old text is in the plan 2 times", "Nothing was changed.\n"}},
		{name: "old equals new",
			files: map[string]string{"old.md": t11Scope, "new.md": t11Scope},
			args:  both, want: 1, unchanged: true,
			wantErr: []string{"are the same", "Nothing was changed.\n"}},
		{name: "adds a checker error",
			files: map[string]string{"old.md": "- **Depends on:** T1.1\n", "new.md": "- **Depends on:** T1.1, T9.9\n"},
			args:  both, want: 1, unchanged: true,
			wantErr: []string{"would give the plan an error it does not have now", "docs/PLAN.md:31: error: T1.2: ", "[unknown-dependency]", "Nothing was changed.\n"},
			notErr:  []string{"line 31:"}},
		{name: "edits a running task", files: scope, args: both,
			held: map[string]ledger.Status{"T1.1": ledger.Running},
			want: 1, unchanged: true,
			wantErr: []string{"task T1.1 is running", "Amended <date>:", "Nothing was changed.\n"}},
		{name: "Amended line on a running task",
			files: map[string]string{"old.md": t11Acceptance, "new.md": t11Acceptance + t11Amended},
			args:  both,
			held:  map[string]ledger.Status{"T1.1": ledger.Running},
			want:  0, wantOut: []string{"Tasks changed: T1.1."},
			wantPlan: []string{t11Acceptance + t11Amended}},
		{name: "the same edit on a done task", files: scope, args: both,
			held: map[string]ledger.Status{"T1.1": ledger.Done},
			want: 0, wantPlan: []string{t11ScopeNew}},
		{name: "missing --old", files: scope, args: []string{"--new", "new.md"}, want: 2, unchanged: true,
			wantErr: []string{"both --old and --new are needed", "Usage: " + planPatchUsageLine}},
		{name: "missing --new", files: scope, args: []string{"--old", "old.md"}, want: 2, unchanged: true,
			wantErr: []string{"Usage: " + planPatchUsageLine}},
		{name: "a stray word", files: scope, args: append([]string{"T1.1"}, both...), want: 2, unchanged: true,
			wantErr: []string{`unexpected word "T1.1"`, "Usage: " + planPatchUsageLine}},
		{name: "unknown flag", files: scope, args: append([]string{"--force"}, both...), want: 2, unchanged: true,
			wantErr: []string{"Usage: " + planPatchUsageLine}},
		{name: "unreadable --new", files: map[string]string{"old.md": t11Scope},
			args: both, want: 1, unchanged: true,
			wantErr: []string{"afk plan patch: cannot read the --new file new.md", "Nothing was changed.\n"}},
		{name: "no plan in the project", noPlan: true, files: scope, args: both, want: 1,
			wantErr: []string{"there is no master plan at ", filepath.Join("docs", "PLAN.md"), "Nothing was changed.\n"}},
		{name: "tracked and clean, with an origin", files: scope, args: both,
			git:  patchGit{repo: true, origin: true},
			want: 0, wantPlan: []string{t11ScopeNew},
			exactOut: "Patched docs/PLAN.md. Tasks changed: T1.1.\nCommitted and pushed.\n",
			wantWrites: []string{
				"git add -- docs/PLAN.md",
				"git commit -m docs(plan): Patch the master plan -- docs/PLAN.md",
				"git push origin main",
			}},
		{name: "tracked and clean, no origin, own message", files: scope,
			args: append([]string{"--message", "docs(plan): Add a delete button"}, both...),
			git:  patchGit{repo: true},
			want: 0, wantPlan: []string{t11ScopeNew},
			exactOut: "Patched docs/PLAN.md. Tasks changed: T1.1.\nCommitted, but not pushed.\nThe commit was not pushed: the repository has no origin remote\n",
			wantWrites: []string{
				"git add -- docs/PLAN.md",
				"git commit -m docs(plan): Add a delete button -- docs/PLAN.md",
			}},
		{name: "commit fails", files: scope, args: both,
			git:  patchGit{repo: true, origin: true, commitFails: true},
			want: 1, wantPlan: []string{t11ScopeNew},
			wantErr: []string{"the plan was patched but not committed", "a hook said no", "Tasks changed: T1.1.\n"},
			notErr:  []string{"Nothing was changed."},
			wantWrites: []string{
				"git add -- docs/PLAN.md",
				"git commit -m docs(plan): Patch the master plan -- docs/PLAN.md",
			}},
	}
	runPatchRows(t, rows, func(args []string, so, se *bytes.Buffer, deps planPatchDeps) int {
		return runPlanPatch(args, so, se, deps)
	})
}

// TestRunPlanPatchPrintsFindingsAsLintDoes holds planFindingLine to the
// format of `afk plan lint`: the line is built in two places (lint's own
// file is not this command's to share from), so the test is what keeps them
// one format. The checker's error text must also appear once, not twice.
func TestRunPlanPatchPrintsFindingsAsLintDoes(t *testing.T) {
	base := patchBasePlan(t)
	patched := strings.Replace(base, "- **Depends on:** T1.1\n", "- **Depends on:** T1.1, T9.9\n", 1)

	lintProject := t.TempDir()
	writePlanFile(t, lintProject, patchPlanRel, patched)
	var lintOut, lintErr bytes.Buffer
	if code := runPlanLint(nil, &lintOut, &lintErr, lintProject, "", func(string) string { return "" }); code != 1 {
		t.Fatalf("lint exit %d, want 1:\n%s%s", code, lintOut.String(), lintErr.String())
	}

	project, typedIn := t.TempDir(), t.TempDir()
	writePlanFile(t, project, patchPlanRel, base)
	writePlanFile(t, typedIn, "old.md", "- **Depends on:** T1.1\n")
	writePlanFile(t, typedIn, "new.md", "- **Depends on:** T1.1, T9.9\n")
	var git patchGit
	var so, se bytes.Buffer
	code := runPlanPatch([]string{"--project", project, "--old", "old.md", "--new", "new.md"}, &so, &se,
		planPatchDeps{cwd: typedIn, gitCommonDir: func(string) string { return "" }, run: git.run})
	if code != 1 {
		t.Fatalf("patch exit %d, want 1:\n%s", code, se.String())
	}

	found := 0
	for _, f := range planlint.Lint([]byte(patched)).Findings {
		if f.Level != planlint.LevelError {
			continue
		}
		found++
		line := planFindingLine(patchPlanRel, f)
		if !strings.Contains(lintOut.String(), line+"\n") {
			t.Errorf("afk plan lint does not print the finding as planFindingLine does:\n  %s\nlint printed:\n%s", line, lintOut.String())
		}
		if !strings.Contains(se.String(), line+"\n") {
			t.Errorf("the refusal lacks the finding line:\n  %s\nstderr:\n%s", line, se.String())
		}
		if n := strings.Count(se.String(), f.Message); n != 1 {
			t.Errorf("the finding's message is printed %d times, want once:\n%s", n, se.String())
		}
	}
	if found != 1 {
		t.Fatalf("the patched plan has %d errors, want 1", found)
	}
}

// Entries the insert rows add.
const (
	entryT11a = "#### T1.1a — Edit a recipe\n" +
		"- **Depends on:** T1.1\n" +
		"- **Goal:** A saved recipe can be changed.\n" +
		"- **Scope:** An edit form on the recipe page.\n" +
		"- **Acceptance:** A changed recipe shows its new text after a restart.\n"
	entryT23 = "#### T2.3 — Share a recipe by mail\n" +
		"- **Depends on:** T2.2\n" +
		"- **Goal:** The owner mails a recipe from its page.\n" +
		"- **Scope:** A mail link on the recipe page.\n" +
		"- **Acceptance:** The link opens a mail with the recipe's link in it.\n"
	entryM3 = "### M3 — Labels\n\n" +
		"**Outcome:** recipes can be grouped.\n\n" +
		"#### T3.1 — Label a recipe\n" +
		"- **Depends on:** T1.1\n" +
		"- **Goal:** A recipe carries labels.\n" +
		"- **Scope:** A labels field on the form.\n" +
		"- **Acceptance:** A label typed in is shown on the recipe page.\n"
	entryTaken = "#### T1.2 — A second list\n- **Depends on:** T1.1\n"
)

// TestRunPlanInsert is `afk plan insert` against a temp project.
//
// Load-bearing rows:
//   - "two places", "no place" and "--after with --milestone 0": the command
//     must count the place flags itself, because planpatch.Where cannot tell
//     milestone 0 (a real milestone) from no milestone, and would read the
//     third as a plain --after.
//   - "after a running task": inserting next to a held task is allowed and
//     must not be refused by rule 8, or a plan could not grow while it is
//     being built.
//   - "id already in the plan": exit 1 with the plan untouched; a second
//     task with a taken id would make the ledger name two things.
//   - "tracked and clean": the default commit subject names what was added.
func TestRunPlanInsert(t *testing.T) {
	rows := []patchRow{
		{name: "after a task", files: map[string]string{"e.md": entryT11a},
			args: []string{"--after", "T1.1", "--entry", "e.md"}, want: 0,
			wantOut:  []string{"Added T1.1a to docs/PLAN.md.\n", notInRepoNote},
			wantPlan: []string{t11Acceptance + "\n" + entryT11a + "\n#### T1.2 — List the recipes\n"}},
		{name: "after a running task", files: map[string]string{"e.md": entryT11a},
			held: map[string]ledger.Status{"T1.1": ledger.Running},
			args: []string{"--entry", "e.md", "--after", "T1.1"}, want: 0,
			wantOut:  []string{"Added T1.1a to docs/PLAN.md.\n"},
			wantPlan: []string{entryT11a}},
		{name: "end of a milestone", files: map[string]string{"e.md": entryT23},
			args: []string{"--milestone", "2", "--entry", "e.md"}, want: 0,
			wantOut:  []string{"Added T2.3 to docs/PLAN.md.\n"},
			wantPlan: []string{"a wrong token shows a not-found page.\n\n" + entryT23 + "\n## Deferred\n"}},
		{name: "a new milestone", files: map[string]string{"e.md": entryM3},
			args: []string{"--new-milestone", "--entry", "e.md"}, want: 0,
			wantOut:  []string{"Added milestone M3 — Labels to docs/PLAN.md.\n"},
			wantPlan: []string{"a wrong token shows a not-found page.\n\n" + entryM3 + "\n## Deferred\n"}},
		{name: "tracked and clean", files: map[string]string{"e.md": entryT11a},
			git:  patchGit{repo: true, origin: true},
			args: []string{"--after", "T1.1", "--entry", "e.md"}, want: 0,
			exactOut: "Added T1.1a to docs/PLAN.md.\nCommitted and pushed.\n",
			wantPlan: []string{entryT11a},
			wantWrites: []string{
				"git add -- docs/PLAN.md",
				"git commit -m docs(plan): Add T1.1a -- docs/PLAN.md",
				"git push origin main",
			}},
		{name: "a new milestone, tracked and clean", files: map[string]string{"e.md": entryM3},
			git:  patchGit{repo: true},
			args: []string{"--new-milestone", "--entry", "e.md"}, want: 0,
			wantPlan: []string{entryM3},
			wantWrites: []string{
				"git add -- docs/PLAN.md",
				"git commit -m docs(plan): Add M3 — Labels -- docs/PLAN.md",
			}},
		{name: "two places", files: map[string]string{"e.md": entryT11a},
			args: []string{"--after", "T1.1", "--milestone", "1", "--entry", "e.md"}, want: 2, unchanged: true,
			wantErr: []string{"exactly one of --after ID, --milestone N and --new-milestone, but got 2", "Usage: " + planInsertUsageLine}},
		{name: "--after with --milestone 0", files: map[string]string{"e.md": entryT11a},
			args: []string{"--after", "T1.1", "--milestone", "0", "--entry", "e.md"}, want: 2, unchanged: true,
			wantErr: []string{"but got 2"}},
		{name: "a task place with --new-milestone", files: map[string]string{"e.md": entryM3},
			args: []string{"--new-milestone", "--milestone", "2", "--entry", "e.md"}, want: 2, unchanged: true,
			wantErr: []string{"but got 2"}},
		{name: "no place", files: map[string]string{"e.md": entryT11a},
			args: []string{"--entry", "e.md"}, want: 2, unchanged: true,
			wantErr: []string{"but got 0", "Usage: " + planInsertUsageLine}},
		{name: "missing --entry", args: []string{"--after", "T1.1"}, want: 2, unchanged: true,
			wantErr: []string{"--entry is needed"}},
		{name: "milestone that is not a number", files: map[string]string{"e.md": entryT23},
			args: []string{"--milestone", "M2", "--entry", "e.md"}, want: 2, unchanged: true,
			wantErr: []string{"Usage: " + planInsertUsageLine}},
		{name: "unreadable entry", args: []string{"--after", "T1.1", "--entry", "e.md"}, want: 1, unchanged: true,
			wantErr: []string{"cannot read the --entry file e.md", "Nothing was changed.\n"}},
		{name: "id already in the plan", files: map[string]string{"e.md": entryTaken},
			args: []string{"--after", "T1.1", "--entry", "e.md"}, want: 1, unchanged: true,
			wantErr: []string{"afk plan insert: task T1.2 is already in the plan", "afk plan next-id", "Nothing was changed.\n"}},
		{name: "after a task the plan does not have", files: map[string]string{"e.md": entryT11a},
			args: []string{"--after", "T9.9", "--entry", "e.md"}, want: 1, unchanged: true,
			wantErr: []string{"there is no task T9.9 in the plan", "Nothing was changed.\n"}},
		{name: "a milestone the plan does not have", files: map[string]string{"e.md": entryT23},
			args: []string{"--milestone", "7", "--entry", "e.md"}, want: 1, unchanged: true,
			wantErr: []string{"the plan has no milestone M7", "Nothing was changed.\n"}},
		{name: "no plan in the project", noPlan: true, files: map[string]string{"e.md": entryT11a},
			args: []string{"--after", "T1.1", "--entry", "e.md"}, want: 1,
			wantErr: []string{"there is no master plan at ", "Nothing was changed.\n"}},
	}
	runPatchRows(t, rows, func(args []string, so, se *bytes.Buffer, deps planPatchDeps) int {
		return runPlanInsert(args, so, se, deps)
	})
}

// TestRunPlanDepend is `afk plan depend` against a temp project.
//
// Load-bearing rows:
//   - "already waits for it": exit 1 with nothing changed and words about
//     this command, not planpatch's "the old text and the new text are the
//     same", which names files this command does not take.
//   - "a running task": a Depends on line is what the daemon acts on, so
//     rule 8 must hold through this command too.
//   - "flag after the words": the natural order for an agent typing it.
//   - "a word that is not an id": refused as bad usage before it could reach
//     the commit subject.
func TestRunPlanDepend(t *testing.T) {
	rows := []patchRow{
		{name: "adds the id", args: []string{"T1.3", "T1.1"}, want: 0,
			wantOut:  []string{"T1.3 now waits for T1.1 in docs/PLAN.md.\n", notInRepoNote},
			wantPlan: []string{"#### T1.3 — Search by ingredient\n- **Depends on:** T1.2, T1.1\n"}},
		{name: "tracked and clean", args: []string{"T1.3", "T1.1"},
			git:  patchGit{repo: true, origin: true},
			want: 0, exactOut: "T1.3 now waits for T1.1 in docs/PLAN.md.\nCommitted and pushed.\n",
			wantPlan: []string{"- **Depends on:** T1.2, T1.1\n"},
			wantWrites: []string{
				"git add -- docs/PLAN.md",
				"git commit -m docs(plan): Make T1.3 wait for T1.1 -- docs/PLAN.md",
				"git push origin main",
			}},
		{name: "flag after the words", args: []string{"T1.3", "T1.1", "--message", "docs(plan): Search needs the store"},
			git:  patchGit{repo: true},
			want: 0, wantPlan: []string{"- **Depends on:** T1.2, T1.1\n"},
			wantWrites: []string{
				"git add -- docs/PLAN.md",
				"git commit -m docs(plan): Search needs the store -- docs/PLAN.md",
			}},
		{name: "already waits for it", args: []string{"T1.2", "T1.1"}, want: 1, unchanged: true,
			wantErr: []string{"afk plan depend: T1.2 already waits for T1.1", "Nothing was changed.\n"},
			notErr:  []string{"old text"}},
		{name: "a running task", args: []string{"T1.3", "T1.1"},
			held: map[string]ledger.Status{"T1.3": ledger.Running},
			want: 1, unchanged: true,
			wantErr: []string{"task T1.3 is running", "Nothing was changed.\n"}},
		{name: "a task the plan does not have", args: []string{"T1.3", "T9.9"}, want: 1, unchanged: true,
			wantErr: []string{"there is no task T9.9 in the plan", "Nothing was changed.\n"}},
		{name: "a cycle", args: []string{"T1.1", "T1.3"}, want: 1, unchanged: true,
			wantErr: []string{"would give the plan", "Nothing was changed.\n"}},
		{name: "one word", args: []string{"T1.3"}, want: 2, unchanged: true,
			wantErr: []string{"expected two task ids", "Usage: " + planDependUsageLine}},
		{name: "three words", args: []string{"T1.3", "T1.1", "T1.2"}, want: 2, unchanged: true,
			wantErr: []string{"got 3 word(s)"}},
		{name: "a word that is not an id", args: []string{"T1.3", "the store"}, want: 2, unchanged: true,
			wantErr: []string{`"the store" is not a task id`, "Usage: " + planDependUsageLine}},
		{name: "no plan in the project", noPlan: true, args: []string{"T1.3", "T1.1"}, want: 1,
			wantErr: []string{"there is no master plan at ", "Nothing was changed.\n"}},
	}
	runPatchRows(t, rows, func(args []string, so, se *bytes.Buffer, deps planPatchDeps) int {
		return runPlanDepend(args, so, se, deps)
	})
}

// TestRunPlanNextID is `afk plan next-id` against a temp project. Every row
// leaves the plan as it was: the command only reads.
//
// Load-bearing rows:
//   - the three happy rows assert the whole of stdout: the id and a line
//     break, nothing else, because the planning session takes stdout as the
//     id.
//   - "a milestone the plan does not have": the id choice works from ids
//     alone and would answer T7.1; the command must refuse, since afk plan
//     insert --milestone 7 would.
//   - "no plan in the project": a command that changes nothing must not say
//     "Nothing was changed.".
func TestRunPlanNextID(t *testing.T) {
	rows := []patchRow{
		{name: "end of milestone 1", args: []string{"--milestone", "1"}, want: 0, unchanged: true, exactOut: "T1.4\n"},
		{name: "end of milestone 2", args: []string{"--milestone", "2"}, want: 0, unchanged: true, exactOut: "T2.3\n"},
		{name: "after a task", args: []string{"--after", "T1.1"}, want: 0, unchanged: true, exactOut: "T1.1a\n"},
		{name: "after a task the plan does not have", args: []string{"--after", "T9.9"}, want: 1, unchanged: true,
			wantErr: []string{"afk plan next-id: there is no task T9.9 in the plan"}, notErr: []string{"Nothing was changed."}},
		{name: "after a word that is not an id", args: []string{"--after", "first"}, want: 1, unchanged: true,
			wantErr: []string{`"first" is not a task id`}},
		{name: "a milestone the plan does not have", args: []string{"--milestone", "7"}, want: 1, unchanged: true,
			wantErr: []string{"afk plan next-id: the plan has no milestone M7"}},
		{name: "both places", args: []string{"--milestone", "1", "--after", "T1.1"}, want: 2, unchanged: true,
			wantErr: []string{"exactly one of --milestone N and --after ID", "Usage: " + planNextIDUsageLine}},
		{name: "no place", args: nil, want: 2, unchanged: true,
			wantErr: []string{"exactly one of", "Usage: " + planNextIDUsageLine}},
		{name: "a stray word", args: []string{"--milestone", "1", "T1.1"}, want: 2, unchanged: true,
			wantErr: []string{`unexpected word "T1.1"`}},
		{name: "no plan in the project", noPlan: true, args: []string{"--milestone", "1"}, want: 1,
			wantErr: []string{"there is no master plan at "}, notErr: []string{"Nothing was changed."}},
	}
	runPatchRows(t, rows, func(args []string, so, se *bytes.Buffer, deps planPatchDeps) int {
		return runPlanNextID(args, so, se, deps)
	})
}

// TestPlanLeftUntouched is every way a failed change can have left something
// behind. Only the first row may say "Nothing was changed."; each other row
// is one sign that something was done, and a wrong yes there would send the
// planning session to make the same patch twice.
func TestPlanLeftUntouched(t *testing.T) {
	tests := []struct {
		name     string
		out      planpatch.Outcome
		sameFile bool
		want     bool
	}{
		{"refused before the write", planpatch.Outcome{}, true, true},
		{"the plan file differs", planpatch.Outcome{}, false, false},
		{"a task was touched", planpatch.Outcome{Touched: []string{"T1.1"}}, true, false},
		{"committed in a worktree", planpatch.Outcome{Committed: true}, true, false},
		{"pushed", planpatch.Outcome{Pushed: true}, true, false},
		{"a pull request", planpatch.Outcome{PR: "https://example.test/pr/1"}, true, false},
		{"a worktree left behind", planpatch.Outcome{Notes: []string{"the temporary worktree at /x could not be removed"}}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planLeftUntouched(tt.out, tt.sameFile); got != tt.want {
				t.Errorf("planLeftUntouched = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPlanPatchHelpFlag: -h on each command prints its usage line and exits
// 0, without touching a project.
func TestPlanPatchHelpFlag(t *testing.T) {
	tests := []struct {
		name string
		run  func(args []string, so, se *bytes.Buffer, deps planPatchDeps) int
		line string
	}{
		{"patch", func(a []string, so, se *bytes.Buffer, d planPatchDeps) int { return runPlanPatch(a, so, se, d) }, planPatchUsageLine},
		{"insert", func(a []string, so, se *bytes.Buffer, d planPatchDeps) int { return runPlanInsert(a, so, se, d) }, planInsertUsageLine},
		{"depend", func(a []string, so, se *bytes.Buffer, d planPatchDeps) int { return runPlanDepend(a, so, se, d) }, planDependUsageLine},
		{"next-id", func(a []string, so, se *bytes.Buffer, d planPatchDeps) int { return runPlanNextID(a, so, se, d) }, planNextIDUsageLine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var so, se bytes.Buffer
			if code := tt.run([]string{"-h"}, &so, &se, planPatchDeps{}); code != 0 {
				t.Fatalf("exit %d, want 0", code)
			}
			if !strings.Contains(se.String(), "Usage: "+tt.line) {
				t.Errorf("stderr lacks the usage line:\n%s", se.String())
			}
		})
	}
}
