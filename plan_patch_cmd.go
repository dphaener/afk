package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/planpatch"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/ship"
)

// The commands that change a master plan that exists: afk plan patch (a
// guarded patch from two files), afk plan insert (a task or a milestone put
// in place by code), afk plan depend (one more task to wait for) and afk plan
// next-id (which only reads, and prints the id a new task should take).
//
// This file holds no rule about what a patch may do. It reads the command
// line and the files it names, hands planpatch.Run a function that builds the
// patch, and prints what came back. The match count, rule 8 and the checker
// are in internal/planpatch, so the planning session, the owner at a shell
// and later the daemon are all held to one set of rules.

const (
	planPatchUsageLine  = "afk plan patch [--project DIR] --old FILE --new FILE [--message TEXT]"
	planInsertUsageLine = "afk plan insert [--project DIR] (--after ID | --milestone N | --new-milestone) --entry FILE [--message TEXT]"
	planDependUsageLine = "afk plan depend [--project DIR] [--message TEXT] TASK ON"
	planNextIDUsageLine = "afk plan next-id [--project DIR] (--milestone N | --after ID)"
)

// planNothingChanged is the last line of a refusal that left everything as
// it was. The planning session's prompt quotes it, so it must stay this text.
const planNothingChanged = "Nothing was changed."

// planPatchPushTimeout bounds one git or gh command of these commands. It is
// longer than ship's 30 seconds because a person is waiting on one push here,
// not a daemon working through every project in a pass, and a push over a
// slow line that is cut off half way leaves a commit the owner must push by
// hand.
const planPatchPushTimeout = 2 * time.Minute

// planPatchDeps is everything these commands touch outside their arguments
// and the project's files. git and gh are behind run, so no test runs either.
type planPatchDeps struct {
	cwd, home string
	// gitCommonDir is `git rev-parse --git-common-dir` for a folder, "" when
	// the folder is not in a repository.
	gitCommonDir func(dir string) string
	run          ship.RunFunc
	// tempDir makes the empty folder a pull-request worktree is put in.
	tempDir func() (string, error)
}

func realPlanPatchDeps() planPatchDeps {
	// A failed Getwd or UserHomeDir is passed on as "": resolveProject only
	// needs them when --project is absent or starts with ~, and it reports
	// the case it cannot resolve.
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return planPatchDeps{
		cwd:          cwd,
		home:         home,
		gitCommonDir: resolveGitCommonDirIn,
		run:          ship.NewRun(planPatchPushTimeout),
		tempDir:      func() (string, error) { return os.MkdirTemp("", "afk-plan-") },
	}
}

// parsePlanPatchArgs parses flags that may come before, between or after the
// words, and returns the words. code is the exit code to stop with, or -1 to
// go on.
//
// Go's flag package stops at the first word, which would turn
// `afk plan depend T1.2 T1.1 --message "…"` into four words. These commands
// are typed by an agent from a prompt, in whatever order reads naturally, so
// the arguments are parsed in a loop, as afk ledger set does.
func parsePlanPatchArgs(flags *flag.FlagSet, args []string, usageLine string, stderr io.Writer) (words []string, code int) {
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s\nRun `afk plan --help` for what each command does.\n", usageLine)
	}
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0
			}
			return nil, 2
		}
		rest = flags.Args()
		if len(rest) == 0 {
			return words, -1
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}
}

// planPatchUsage prints a command-line mistake and the usage line, and
// returns exit code 2.
func planPatchUsage(stderr io.Writer, cmd, usageLine, format string, a ...any) int {
	fmt.Fprintf(stderr, cmd+": "+format+"\n", a...)
	fmt.Fprintf(stderr, "Usage: %s\n", usageLine)
	return 2
}

// flagGiven reports whether a flag was on the command line. It is how
// `--milestone 0` (a real milestone) is told from no --milestone at all.
func flagGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// planTarget is the plan a command works on.
type planTarget struct {
	root     string
	planPath string
	// shown is the plan's path as messages print it: relative to the project
	// folder (docs/PLAN.md), which is how the owner and the commit name it.
	shown string
	cfg   projectconfig.Config
}

// resolvePlanTarget finds the project, reads afk.toml and checks the plan is
// there. code is the exit code to stop with, or -1 to go on.
//
// The plan is looked for here, before planpatch.Run, for two reasons. The
// message can say where it was looked for and how to start one, which Run's
// own "could not read the plan" cannot. And Run makes the project's .afk
// folder for its lock before it reads the plan, so a mistyped --project would
// otherwise leave an .afk folder behind in a folder that is not a project.
//
// changes is false for a command that only reads; it is then not told that
// nothing was changed.
func resolvePlanTarget(cmd, usageLine, proj string, changes bool, stderr io.Writer, deps planPatchDeps) (planTarget, int) {
	root, err := resolveBuildRoot(proj, deps.cwd, deps.home, deps.gitCommonDir)
	if err != nil {
		return planTarget{}, planPatchUsage(stderr, cmd, usageLine, "cannot tell which project to use. Pass --project with the folder that holds the master plan.")
	}
	refuse := func(format string, a ...any) (planTarget, int) {
		fmt.Fprintf(stderr, cmd+": "+format+"\n", a...)
		if changes {
			fmt.Fprintln(stderr, planNothingChanged)
		}
		return planTarget{}, 1
	}
	cfg, err := projectconfig.Load(root)
	if err != nil {
		return refuse("%v", err)
	}
	t := planTarget{root: root, planPath: cfg.PlanPath(root), cfg: cfg}
	t.shown = t.planPath
	if rel, err := filepath.Rel(root, t.planPath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.shown = filepath.ToSlash(rel)
	}
	if _, err := os.Stat(t.planPath); errors.Is(err, fs.ErrNotExist) {
		return refuse("there is no master plan at %s. Start one with afk plan, or set plan in afk.toml to where the plan is, or pass --project with the project's folder.", t.planPath)
	}
	return t, -1
}

// readPlanInput reads a file named on the command line, relative to the
// folder the command was typed in.
func readPlanInput(deps planPatchDeps, flagName, path string) ([]byte, error) {
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(deps.cwd, full)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("cannot read the %s file %s: %w. Check the path", flagName, path, err)
	}
	return data, nil
}

// planChange is one change to the plan, ready to run.
type planChange struct {
	cmd     string
	target  planTarget
	edit    func(plan []byte) (planpatch.Edit, error)
	message string
	// done is the sentence that says what changed. where is the plan's shown
	// path, with the branch added when the change went to a pull request.
	done func(where string, out planpatch.Outcome) string
}

// planLeftUntouched reports whether a failed change left everything as it
// was, which is when the refusal may end with "Nothing was changed.".
//
// planpatch.Run gives no flag for it, so it is read from what can be seen.
// sameFile is whether the plan in the main checkout holds the bytes it held
// before the command. That alone is not enough with [planning] via_pr, where
// the main checkout's plan never changes: there a commit that was made, a
// task that was touched or a note (a worktree left behind with the patched
// plan in it) each mean something was done. A false "Nothing was changed." is
// the costly mistake, since the planning session then makes the same patch a
// second time, so every doubt answers no.
func planLeftUntouched(out planpatch.Outcome, sameFile bool) bool {
	return sameFile && !out.Committed && !out.Pushed && out.PR == "" && len(out.Touched) == 0 && len(out.Notes) == 0
}

// planFindingLine is one finding as afk plan lint prints it, so a refused
// patch reads like the checker's own report. TestRunPlanPatch compares it
// with what runPlanLint prints for the same plan.
func planFindingLine(shown string, f planlint.Finding) string {
	loc := shown
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", shown, f.Line)
	}
	task := ""
	if f.Task != "" {
		task = f.Task + ": "
	}
	return fmt.Sprintf("%s: %s: %s%s [%s]", loc, f.Level, task, f.Message, f.Rule)
}

// planNote is a note from planpatch as a line of its own: the notes are
// written to follow other words, so they start in lower case.
func planNote(note string) string {
	r, size := utf8.DecodeRuneInString(note)
	if size == 0 {
		return note
	}
	return string(unicode.ToUpper(r)) + note[size:]
}

// applyPlanChange runs one change through planpatch.Run and prints the
// outcome. It returns the exit code.
func applyPlanChange(c planChange, stdout, stderr io.Writer, deps planPatchDeps) int {
	before, beforeErr := os.ReadFile(c.target.planPath)
	out, err := planpatch.Run(
		planpatch.Deps{Run: deps.run, TempDir: deps.tempDir},
		planpatch.Options{
			Root:     c.target.root,
			PlanPath: c.target.planPath,
			Edit:     c.edit,
			Message:  c.message,
			ViaPR:    c.target.cfg.Planning.ViaPR,
			Base:     c.target.cfg.Ship.Base,
		})
	if err != nil {
		var lint *planpatch.LintError
		if errors.As(err, &lint) {
			// The error's own text lists the findings too. They are printed
			// here in the checker's format instead, and only once.
			what := "an error"
			if len(lint.Added) != 1 {
				what = fmt.Sprintf("%d errors", len(lint.Added))
			}
			fmt.Fprintf(stderr, "%s: the change would give the plan %s it does not have now. The line numbers are those the plan would have had:\n", c.cmd, what)
			for _, f := range lint.Added {
				fmt.Fprintln(stderr, planFindingLine(c.target.shown, f))
			}
			fmt.Fprintln(stderr, "Fix the new text and run the command again.")
		} else {
			fmt.Fprintf(stderr, "%s: %v\n", c.cmd, err)
		}
		// Run can fail after the write (a commit git refused, a push that
		// did not go through), and then what it did do is still worth
		// knowing.
		if len(out.Touched) > 0 {
			fmt.Fprintf(stderr, "Tasks changed: %s.\n", strings.Join(out.Touched, ", "))
		}
		for _, n := range out.Notes {
			fmt.Fprintln(stderr, planNote(n))
		}
		after, afterErr := os.ReadFile(c.target.planPath)
		if planLeftUntouched(out, beforeErr == nil && afterErr == nil && bytes.Equal(before, after)) {
			fmt.Fprintln(stderr, planNothingChanged)
		}
		return 1
	}

	where := c.target.shown
	if out.PR != "" {
		where += " on the branch " + planpatch.PRBranch
	}
	fmt.Fprintln(stdout, c.done(where, out))
	switch {
	case out.PR != "":
		fmt.Fprintf(stdout, "Committed and pushed.\nPull request: %s\n", out.PR)
	case out.Committed && out.Pushed:
		fmt.Fprintln(stdout, "Committed and pushed.")
	case out.Committed:
		// Why it was not pushed is the note that follows.
		fmt.Fprintln(stdout, "Committed, but not pushed.")
	}
	for _, n := range out.Notes {
		fmt.Fprintln(stdout, planNote(n))
	}
	return 0
}

// tasksChanged is the second sentence of a success line: which task entries
// the change went into.
func tasksChanged(out planpatch.Outcome) string {
	if len(out.Touched) == 0 {
		return ""
	}
	return " Tasks changed: " + strings.Join(out.Touched, ", ") + "."
}

// runPlanPatch implements `afk plan patch` and returns the exit code: 0 the
// plan was patched, 1 refused or failed, 2 the command was written wrongly.
func runPlanPatch(args []string, stdout, stderr io.Writer, deps planPatchDeps) int {
	const cmd = "afk plan patch"
	flags := flag.NewFlagSet("plan patch", flag.ContinueOnError)
	proj := addProjectFlag(flags)
	oldPath := flags.String("old", "", "File holding the text to replace")
	newPath := flags.String("new", "", "File holding the text to put in its place")
	message := flags.String("message", "", "Commit subject")
	words, code := parsePlanPatchArgs(flags, args, planPatchUsageLine, stderr)
	if code >= 0 {
		return code
	}
	if len(words) > 0 {
		return planPatchUsage(stderr, cmd, planPatchUsageLine, "unexpected word %q. The old and the new text are given as files, with --old and --new.", words[0])
	}
	if *oldPath == "" || *newPath == "" {
		return planPatchUsage(stderr, cmd, planPatchUsageLine, "both --old and --new are needed: --old names a file holding the text to replace, --new a file holding what to put in its place.")
	}

	target, code := resolvePlanTarget(cmd, planPatchUsageLine, *proj, true, stderr, deps)
	if code >= 0 {
		return code
	}
	var edit planpatch.Edit
	for _, in := range []struct {
		name, path string
		into       *[]byte
	}{{"--old", *oldPath, &edit.Old}, {"--new", *newPath, &edit.New}} {
		data, err := readPlanInput(deps, in.name, in.path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v.\n%s\n", cmd, err, planNothingChanged)
			return 1
		}
		*in.into = data
	}

	return applyPlanChange(planChange{
		cmd:     cmd,
		target:  target,
		edit:    func([]byte) (planpatch.Edit, error) { return edit, nil },
		message: *message,
		done: func(where string, out planpatch.Outcome) string {
			if len(out.Touched) == 0 {
				return fmt.Sprintf("Patched %s. No task's entry was changed.", where)
			}
			return fmt.Sprintf("Patched %s.%s", where, tasksChanged(out))
		},
	}, stdout, stderr, deps)
}

// runPlanInsert implements `afk plan insert` and returns the exit code: 0
// the task or milestone was added, 1 refused or failed, 2 the command was
// written wrongly.
func runPlanInsert(args []string, stdout, stderr io.Writer, deps planPatchDeps) int {
	const cmd = "afk plan insert"
	flags := flag.NewFlagSet("plan insert", flag.ContinueOnError)
	proj := addProjectFlag(flags)
	after := flags.String("after", "", "Task id the new task goes directly after")
	milestone := flags.Int("milestone", 0, "Number of the milestone the new task is added to the end of")
	newMilestone := flags.Bool("new-milestone", false, "The entry is a whole new milestone")
	entryPath := flags.String("entry", "", "File holding the task or the milestone")
	message := flags.String("message", "", "Commit subject")
	words, code := parsePlanPatchArgs(flags, args, planInsertUsageLine, stderr)
	if code >= 0 {
		return code
	}
	usage := func(format string, a ...any) int {
		return planPatchUsage(stderr, cmd, planInsertUsageLine, format, a...)
	}
	if len(words) > 0 {
		return usage("unexpected word %q. The task is given as a file, with --entry.", words[0])
	}
	// The place is checked here and not left to planpatch.Where: milestone 0
	// is a real milestone, so a Where cannot say "no milestone was given",
	// and `--after T1.1 --milestone 0` would pass as a plain --after.
	afterGiven, milestoneGiven := flagGiven(flags, "after"), flagGiven(flags, "milestone")
	places := 0
	for _, given := range []bool{afterGiven, milestoneGiven, *newMilestone} {
		if given {
			places++
		}
	}
	if places != 1 {
		return usage("say where the entry goes with exactly one of --after ID, --milestone N and --new-milestone, but got %d of them.", places)
	}
	if afterGiven && *after == "" {
		return usage("--after needs the id of the task the new one goes after, such as --after T2.2.")
	}
	if *entryPath == "" {
		return usage("--entry is needed: it names a file holding the task (or the milestone) as it should stand in the plan.")
	}

	target, code := resolvePlanTarget(cmd, planInsertUsageLine, *proj, true, stderr, deps)
	if code >= 0 {
		return code
	}
	entry, err := readPlanInput(deps, "--entry", *entryPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v.\n%s\n", cmd, err, planNothingChanged)
		return 1
	}

	// The entry's own heading names what is added, for the commit subject
	// and the success line. An entry with no such heading is refused by the
	// builder; the names below are then never printed.
	outline := masterplan.Scan(entry)
	name, subject := "the entry", ""
	var edit func(plan []byte) (planpatch.Edit, error)
	if *newMilestone {
		if len(outline.Headings) > 0 {
			name = "milestone " + outline.Headings[0].Text
			subject = "docs(plan): Add " + outline.Headings[0].Text
		}
		edit = func(plan []byte) (planpatch.Edit, error) { return planpatch.AppendMilestone(plan, entry) }
	} else {
		if len(outline.Tasks) > 0 {
			name = outline.Tasks[0].ID
			subject = "docs(plan): Add " + name
		}
		where := planpatch.Where{After: *after}
		if milestoneGiven {
			where = planpatch.Where{Milestone: *milestone}
		}
		edit = func(plan []byte) (planpatch.Edit, error) { return planpatch.InsertTask(plan, entry, where) }
	}
	if *message != "" {
		subject = *message
	}

	return applyPlanChange(planChange{
		cmd:     cmd,
		target:  target,
		edit:    edit,
		message: subject,
		done: func(where string, out planpatch.Outcome) string {
			return fmt.Sprintf("Added %s to %s.%s", name, where, tasksChanged(out))
		},
	}, stdout, stderr, deps)
}

// runPlanDepend implements `afk plan depend TASK ON` and returns the exit
// code: 0 TASK now waits for ON, 1 refused or failed (it already does, among
// others), 2 the command was written wrongly.
func runPlanDepend(args []string, stdout, stderr io.Writer, deps planPatchDeps) int {
	const cmd = "afk plan depend"
	flags := flag.NewFlagSet("plan depend", flag.ContinueOnError)
	proj := addProjectFlag(flags)
	message := flags.String("message", "", "Commit subject")
	words, code := parsePlanPatchArgs(flags, args, planDependUsageLine, stderr)
	if code >= 0 {
		return code
	}
	usage := func(format string, a ...any) int {
		return planPatchUsage(stderr, cmd, planDependUsageLine, format, a...)
	}
	if len(words) != 2 {
		return usage("expected two task ids, the task and the task it must wait for, but got %d word(s).", len(words))
	}
	task, on := words[0], words[1]
	// Both ids go into the commit subject, so a word that is not an id is
	// refused here, before it can be written anywhere.
	for _, id := range words {
		if !masterplan.ValidID(id) {
			return usage("%q is not a task id. A task id looks like T2.3 or T2.3a.", id)
		}
	}

	target, code := resolvePlanTarget(cmd, planDependUsageLine, *proj, true, stderr, deps)
	if code >= 0 {
		return code
	}
	subject := fmt.Sprintf("docs(plan): Make %s wait for %s", task, on)
	if *message != "" {
		subject = *message
	}
	return applyPlanChange(planChange{
		cmd:    cmd,
		target: target,
		edit: func(plan []byte) (planpatch.Edit, error) {
			e, err := planpatch.AddDependency(plan, task, on)
			// ErrNoChange's own words are about an old and a new file, which
			// this command does not have.
			if errors.Is(err, planpatch.ErrNoChange) {
				return e, fmt.Errorf("%s already waits for %s, so there is nothing to add", task, on)
			}
			return e, err
		},
		message: subject,
		done: func(where string, _ planpatch.Outcome) string {
			return fmt.Sprintf("%s now waits for %s in %s.", task, on, where)
		},
	}, stdout, stderr, deps)
}

// runPlanNextID implements `afk plan next-id` and returns the exit code: 0
// the id was printed, 1 there is no such milestone or task (or no plan), 2
// the command was written wrongly. It only reads, and prints the id alone on
// one line so a script can take the whole of stdout as the id.
func runPlanNextID(args []string, stdout, stderr io.Writer, deps planPatchDeps) int {
	const cmd = "afk plan next-id"
	flags := flag.NewFlagSet("plan next-id", flag.ContinueOnError)
	proj := addProjectFlag(flags)
	after := flags.String("after", "", "Task id the new task goes directly after")
	milestone := flags.Int("milestone", 0, "Number of the milestone the new task is added to the end of")
	words, code := parsePlanPatchArgs(flags, args, planNextIDUsageLine, stderr)
	if code >= 0 {
		return code
	}
	usage := func(format string, a ...any) int {
		return planPatchUsage(stderr, cmd, planNextIDUsageLine, format, a...)
	}
	if len(words) > 0 {
		return usage("unexpected word %q.", words[0])
	}
	afterGiven, milestoneGiven := flagGiven(flags, "after"), flagGiven(flags, "milestone")
	if afterGiven == milestoneGiven {
		return usage("say where the new task goes with exactly one of --milestone N and --after ID.")
	}
	if afterGiven && *after == "" {
		return usage("--after needs the id of the task the new one goes after, such as --after T2.2.")
	}

	target, code := resolvePlanTarget(cmd, planNextIDUsageLine, *proj, false, stderr, deps)
	if code >= 0 {
		return code
	}
	plan, err := os.ReadFile(target.planPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: cannot read the master plan %s: %v\n", cmd, target.planPath, err)
		return 1
	}
	var ids []string
	for _, t := range masterplan.Scan(plan).Tasks {
		ids = append(ids, t.ID)
	}

	var id string
	if afterGiven {
		id, err = planpatch.Between(ids, *after)
	} else {
		id, err = planpatch.NextInMilestone(ids, *milestone)
		if err == nil {
			// NextInMilestone works from the ids alone and would answer T7.1
			// for a milestone the plan does not have. Whether the milestone
			// is there is the builder's reading, so the builder is asked: an
			// id is only printed when afk plan insert --milestone N would
			// take a task with it. The entry is a stand-in and goes nowhere.
			probe := fmt.Sprintf("#### %s — New task\n- **Depends on:** —\n", id)
			_, err = planpatch.InsertTask(plan, []byte(probe), planpatch.Where{Milestone: *milestone})
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return 1
	}
	fmt.Fprintln(stdout, id)
	return 0
}
