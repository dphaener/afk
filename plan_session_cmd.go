package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ui"
)

// Usage lines are constants so the error messages repeat exactly what the
// help says.
const (
	planSessionUsageLine = "afk plan [--project DIR] [--provider NAME] [--dry-run]"
	planStatusUsageLine  = "afk plan status [--project DIR]"
	planAdvanceUsageLine = "afk plan advance [--project DIR]"

	planExtendUsageLine    = "afk plan extend [--project DIR] [--dry-run] [--provider NAME]"
	planAmendUsageLine     = "afk plan amend [--project DIR] [--dry-run] [--provider NAME] [--yes] \"<what>\""
	planReconcileUsageLine = "afk plan reconcile [--project DIR] [--dry-run] [--provider NAME] FILE"
)

// planModeCommands is the three ways to change a plan that exists, one to a
// line. Every message that sends the owner to them prints this one text, so
// the commands cannot be listed differently in two places.
const planModeCommands = "  afk plan extend             add a milestone\n" +
	"  afk plan amend \"<what>\"     add, split or replace one task\n" +
	"  afk plan reconcile FILE     bring the plan in line with a document\n"

// planBriefFile is the brief's name inside the planning folder. The brief is
// the session's own file in a new or extend session; `afk plan amend` and
// `afk plan reconcile` write it themselves, because there the owner's request
// is on the command line and asking for it again would be asking twice. It
// must stay the name internal/plansession reads for the brief gate.
const planBriefFile = "brief.md"

// planArchiveFolder is the folder inside the planning folder that
// plansession.Archive leaves alone. It is named here only so a dry run can say
// whether there is anything a start would move; a wrong name would make that
// one sentence wrong and nothing else.
const planArchiveFolder = "archive"

// planDeps is everything `afk plan`, `afk plan status` and `afk plan advance`
// touch outside their own arguments and the project's files. The commands
// start a git repository, a detached host and a full-screen terminal program;
// with all of that behind one struct the tests fake it, so no test runs git,
// starts a host or needs a terminal.
type planDeps struct {
	cwd, home string
	// gitCommonDir is `git rev-parse --git-common-dir` for a folder, "" when
	// the folder is not in a repository.
	gitCommonDir func(dir string) string
	gitInit      func(dir string) error
	stdinTTY     func() bool
	stdoutTTY    func() bool
	// hostAlive says whether the project's planning host is running.
	hostAlive func(project string) bool
	spawn     func(spec session.HostSpawn) (session.HostProcess, error)
	// attach takes the terminal until the owner leaves the session. started
	// is true when the host was spawned a moment ago.
	attach func(project string, started bool) (msg string, code int, isError bool)
	// lintErrors is the number of errors the plan checker finds in a plan.
	lintErrors func(plan []byte) int
	now        func() time.Time
	// ask prints a question and returns the line the owner typed, without its
	// line break. It is only called when both stdin and stdout are a terminal.
	ask func(prompt string) (string, error)
	// review is what the "plan ready for review" notification uses; the zero
	// value sends nothing.
	review planReviewDeps
}

func realPlanDeps() planDeps {
	// A failed Getwd or UserHomeDir is passed on as "": resolveProject only
	// needs them when --project is absent or starts with ~, and it reports
	// the case it cannot resolve.
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	// One reader for every question: a reader made per question could take a
	// later answer into its buffer and drop it when it is thrown away.
	stdin := bufio.NewReader(os.Stdin)
	return planDeps{
		cwd:          cwd,
		home:         home,
		gitCommonDir: resolveGitCommonDirIn,
		gitInit:      gitInitIn,
		stdinTTY:     func() bool { return isTerminal(os.Stdin) },
		stdoutTTY:    func() bool { return isTerminal(os.Stdout) },
		hostAlive:    planHostAlive,
		spawn: func(spec session.HostSpawn) (session.HostProcess, error) {
			return session.SpawnHost(spec, session.DefaultDeps())
		},
		attach:     attachPlanSession,
		lintErrors: func(plan []byte) int { return planlint.Lint(plan).Summary.Errors },
		now:        time.Now,
		review:     realPlanReviewDeps(),
		ask: func(prompt string) (string, error) {
			fmt.Fprint(os.Stdout, prompt)
			line, err := stdin.ReadString('\n')
			if err != nil && line == "" {
				return "", err
			}
			return strings.TrimRight(line, "\r\n"), nil
		},
	}
}

// gitInitIn runs `git init` in dir. Its output is kept for the error message
// and otherwise dropped: git's hints about the default branch name are noise
// to somebody who only asked to plan a project.
func gitInitIn(dir string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// planHostAlive reports whether the project's planning host appears to be
// running: host.json is there and its pid answers signal 0. Like discovery's
// HostRunning it is advisory, because a pid can be reused by another process.
// A wrong "alive" costs little here: the attach that follows finds nobody
// listening on the socket and says the session is not running.
func planHostAlive(project string) bool {
	meta, err := host.ReadMeta(project, session.PlanSessionName)
	return err == nil && host.Alive(meta.PID)
}

// attachPlanSession renders the planning session in this terminal until the
// owner leaves it, as `afk attach` does (runAttach in attach.go), with one
// difference: it does not look the session up through discovery. `afk plan`
// already knows the session's folder, because it is the project itself, and a
// look-up a moment after the spawn would find no host.json yet and report a
// session that is starting as "not running".
//
// For the same reason a host that was only just started gets the whole attach
// window to write host.json, not the short one runAttach allows a host that is
// expected to be up already.
func attachPlanSession(project string, started bool) (msg string, code int, isError bool) {
	name := session.PlanSessionName
	metaWait := metaReadWait
	if started {
		metaWait = attachWait
	}
	notRunning := func(detail string) (string, int, bool) {
		return fmt.Sprintf("afk plan: the planning session is not running (%s).\nRun `afk plan` again to start it. If it stops again, %s says why.",
			detail, filepath.Join(project, ".afk", name, "host.log")), host.ExitNotRunning, true
	}
	if _, err := readMetaWithRetry(project, name, metaWait); err != nil {
		return notRunning("it did not write its host.json")
	}
	reason, err := ui.AttachPane(name, project, attachWait)
	if err != nil {
		if errors.Is(err, host.ErrHostNotReachable) {
			return notRunning(err.Error())
		}
		return fmt.Sprintf("afk plan: %v", err), 1, true
	}
	switch reason {
	case host.DetachUser:
		msg = "afk plan: you left the planning session. It keeps running; run `afk plan` again to come back to it."
	case host.DetachStolen:
		msg = "afk plan: the planning session was opened somewhere else (another terminal or the dashboard), so this one let go of it. It keeps running."
	case host.DetachExited:
		msg = "afk plan: the planning session ended. What it saved is kept in .afk/plan/; run `afk plan` again to pick it up where it stopped."
	default:
		msg = "afk plan: the connection to the planning session was lost. Run `afk plan` again to come back to it."
	}
	return msg, reason.ExitCode(), false
}

// planRoute is which part of `afk plan` an argument list asks for.
type planRoute int

const (
	planRouteSession planRoute = iota
	planRouteHelp
	planRouteCheck
	planRouteLint
	planRouteStatus
	planRouteAdvance
	// planRouteApprove is `afk plan approve`: the owner's "go" for a reviewed
	// plan. It is in plan_approve_cmd.go.
	planRouteApprove
	// planRouteMode is `afk plan extend`, `amend` or `reconcile`: a planning
	// session that changes a plan that exists. The word is the mode.
	planRouteMode
	// The four below change a plan (or, for next-id, read it) without a
	// planning session: one guarded patch each, made by plan_patch_cmd.go.
	planRoutePatch
	planRouteInsert
	planRouteDepend
	planRouteNextID
	// planRouteNextFollowup reads docs/ and prints where the next design
	// follow-up prompt goes. It is also in plan_patch_cmd.go.
	planRouteNextFollowup
	planRouteUnknown
)

// routePlanArgs decides which part of `afk plan` runs. Pure, so the matrix is
// a table test.
//
// No arguments, or a first argument that is a flag, is the planning session
// itself: `afk plan`, `afk plan --dry-run`, `afk plan --project X`. That is
// the same rule `afk profile` uses for its list. Help is checked first, or
// `afk plan --help` would be read as a flag for the session.
func routePlanArgs(args []string) planRoute {
	if len(args) == 0 {
		return planRouteSession
	}
	switch args[0] {
	case "-h", "--help", "help":
		return planRouteHelp
	case "check":
		return planRouteCheck
	case "lint":
		return planRouteLint
	case "status":
		return planRouteStatus
	case "advance":
		return planRouteAdvance
	case "approve":
		return planRouteApprove
	case plansession.ModeExtend, plansession.ModeAmend, plansession.ModeReconcile:
		return planRouteMode
	case "patch":
		return planRoutePatch
	case "insert":
		return planRouteInsert
	case "depend":
		return planRouteDepend
	case "next-id":
		return planRouteNextID
	case "next-followup":
		return planRouteNextFollowup
	}
	if strings.HasPrefix(args[0], "-") {
		return planRouteSession
	}
	return planRouteUnknown
}

// planStartKind is what `afk plan` does once it has looked at the project.
type planStartKind int

const (
	// planStartInvalid is the zero value on purpose, so an action nobody
	// filled in is treated as a failure and never as "start".
	planStartInvalid planStartKind = iota
	planRefuseProvider
	planRefuseDamaged
	// planRefuseNoPlan: a mode was asked for and the project has no plan.
	planRefuseNoPlan
	// planRefuseInProgress: a mode was asked for while a session is open.
	planRefuseInProgress
	// planRefusePlanGone: a bare `afk plan`, the session is finished and the
	// plan it wrote is not there.
	planRefusePlanGone
	// planRefuseHostBusy: the earlier session is finished (or left no state)
	// and its agent is still running.
	planRefuseHostBusy
	// planRefuseNeedsMode: a bare `afk plan` on a project with a plan, with
	// nobody at a terminal to say which change is wanted.
	planRefuseNeedsMode
	// planChooseMode: the same on a terminal: ask which change is wanted.
	planChooseMode
	planDryRun
	planRefuseNoTerminal
	planAttach
	planStart
)

// planStartInput is every fact decidePlanStart reads, as plain values. The
// caller does the looking (git, the filesystem, the terminal, the host's pid).
type planStartInput struct {
	DryRun bool
	// Mode is the mode asked for on the command line or in answer to the
	// question: extend, amend or reconcile. "" is a bare `afk plan`.
	Mode string
	// IsRepo: the folder is inside a git repository.
	IsRepo bool
	// HasState: .afk/plan/state.json exists and is sound. StateDamaged: it
	// exists and is not.
	HasState     bool
	StateDamaged bool
	// Recorded is the phase in state.json, "" without one. The decision does
	// not read it (Finished is what it needs to know about the phase); it is
	// kept so a table row says which session it describes.
	Recorded string
	// Finished: the recorded session has nothing left to do
	// (plansession.Finished). Only ever true together with HasState.
	Finished bool
	// HasOldFiles: .afk/plan/ holds something a new change session would
	// move into the archive.
	HasOldFiles bool
	// PlanExists: the master plan file exists and is not blank.
	PlanExists bool
	HostAlive  bool
	// Terminal: stdin and stdout are both a terminal.
	Terminal bool
	// PlanArgvErr is the provider's refusal to run a planning session, nil
	// when it can.
	PlanArgvErr error
}

// planStartAction is the decision. InitRepo, Archive and SeedState say what a
// start does first; in a dry run they say what a start would do, Reattach says
// it would join a host that is already running instead of starting one, and
// AskMode says it would ask which change is wanted.
type planStartAction struct {
	Kind      planStartKind
	InitRepo  bool
	Archive   bool
	SeedState bool
	Reattach  bool
	AskMode   bool
}

// decidePlanStart is the whole decision of `afk plan` and of the three
// commands that start a change session, first match wins. Pure.
//
// Three words are used below. A session is open when it has a state and is
// not finished. A mode is asked for when the command was extend, amend or
// reconcile (or the owner picked one of them). The plan can be changed when
// it exists and no session is open.
//
// The order is the point:
//
//   - An open session is never replaced. A mode asked for while one is open is
//     refused, because seeding the new mode would move the open session's
//     brief and answers into the archive in the middle of its work. A bare
//     `afk plan` still resumes it, in any mode: from the draft phase on the
//     plan is the session's own work, so "the plan exists" must not get in
//     the way.
//   - Every refusal comes before the dry run, because a dry run that promised
//     a start the real command then refuses would be a lie. That includes the
//     agent of a finished session that is still running: a second host would
//     be started beside it under the same name.
//   - The dry run comes before the terminal checks, because a dry run asks no
//     questions and is what a script or an agent uses to see what would
//     happen. On a project with a plan and no mode it says that the real
//     command would ask.
//   - No terminal comes before asking, attaching and starting, so that with
//     nobody able to answer, nothing is started at all. A planning host
//     started from a script would sit at its first question inside a detached
//     process, looking alive.
//
// A finished session whose plan is gone is refused rather than started over:
// the plan may only have been moved, or afk.toml may name the wrong file, and
// starting a new session there would write a second plan.
func decidePlanStart(in planStartInput) planStartAction {
	asked := in.Mode != ""
	open := in.HasState && !in.Finished
	canChange := in.PlanExists && !open
	would := planStartAction{
		InitRepo: !in.IsRepo && !in.HostAlive,
		// A mode start always seeds: it only gets this far with no session
		// open, and whatever state is there belongs to a finished one.
		SeedState: (!in.HasState || asked) && !in.HostAlive,
		Reattach:  in.HostAlive,
		Archive:   asked && in.HasOldFiles,
	}
	switch {
	case in.PlanArgvErr != nil:
		return planStartAction{Kind: planRefuseProvider}
	case in.StateDamaged:
		return planStartAction{Kind: planRefuseDamaged}
	case asked && !in.PlanExists:
		return planStartAction{Kind: planRefuseNoPlan}
	case asked && open:
		return planStartAction{Kind: planRefuseInProgress}
	case !asked && in.Finished && !in.PlanExists:
		return planStartAction{Kind: planRefusePlanGone}
	case canChange && in.HostAlive:
		return planStartAction{Kind: planRefuseHostBusy}
	case canChange && !asked && in.DryRun:
		return planStartAction{Kind: planDryRun, AskMode: true}
	case canChange && !asked && !in.Terminal:
		return planStartAction{Kind: planRefuseNeedsMode}
	case canChange && !asked:
		return planStartAction{Kind: planChooseMode}
	case in.DryRun:
		would.Kind = planDryRun
		return would
	case !in.Terminal:
		return planStartAction{Kind: planRefuseNoTerminal}
	case in.HostAlive:
		return planStartAction{Kind: planAttach, Reattach: true}
	default:
		would.Kind = planStart
		return would
	}
}

// planProject is where a planning command works.
type planProject struct {
	// Dir is the folder the owner named or stood in.
	Dir string
	// Root is the folder that holds .afk/plan/: the repository's main
	// checkout, or Dir itself when Dir is not in a repository.
	Root   string
	IsRepo bool
}

// resolvePlanProject finds the project for a planning command and returns the
// exit code to stop with (0 to go on).
//
// It does not use validateProjectPath, which demands a .git entry: an empty
// folder is exactly where a new project starts, and `afk plan` turns it into a
// repository itself. Inside a repository the project is the main checkout, as
// for the build commands (resolveBuildRoot), so a command typed from a linked
// worktree reads and writes the one planning state everybody else sees.
func resolvePlanProject(cmd, flagValue string, stderr io.Writer, deps planDeps) (planProject, int) {
	dir, err := resolveProject(flagValue, deps.cwd, deps.home)
	if err != nil {
		fmt.Fprintf(stderr, "%s: cannot tell which folder to use. Pass --project with the project's folder.\n", cmd)
		return planProject{}, 2
	}
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintf(stderr, "%s: the folder %s was not found.\nCreate it first (mkdir -p %s), or pass --project with a folder that exists.\n", cmd, dir, dir)
		return planProject{}, 1
	case err != nil:
		fmt.Fprintf(stderr, "%s: cannot look at %s: %v\n", cmd, dir, err)
		return planProject{}, 1
	case !fi.IsDir():
		fmt.Fprintf(stderr, "%s: %s is a file, not a folder. Pass --project with the project's folder.\n", cmd, dir)
		return planProject{}, 1
	}
	common := deps.gitCommonDir(dir)
	return planProject{Dir: dir, Root: mainCheckout(dir, common), IsRepo: common != ""}, 0
}

// loadPlanSession reads the planning session of the project at root and
// returns it with the master plan's path. The path comes from afk.toml exactly
// as `afk tasks` finds it (loadBuildState), default docs/PLAN.md.
//
// The checker's error count and the four facts about the design task are
// filled in here because plansession.Load leaves them alone (that package does
// not know the plan format). All five come from one read of the plan, so the
// design gate never judges the task in one version of the file and the errors
// in another. A plan that exists but cannot be read a second time is counted
// as it was loaded: no errors known and no design task seen. The gates that
// need them are asked again on the next command, and the worst a failed read
// does is refuse one advance.
func loadPlanSession(root string, deps planDeps) (plansession.Files, string, error) {
	cfg, err := projectconfig.Load(root)
	if err != nil {
		return plansession.Files{}, "", err // already names afk.toml and what to do
	}
	planPath := cfg.PlanPath(root)
	f := plansession.Load(root, planPath)
	if f.PlanExists {
		if data, err := os.ReadFile(planPath); err == nil {
			f.PlanErrors = deps.lintErrors(data)
			f.DesignRule, f.DesignTask, f.DesignTaskFirst, f.DesignDependents = designFacts(data)
		}
	}
	return f, planPath, nil
}

// designRulePhrases are the three texts the plan's header must hold for it to
// state which document wins on what. They are the words the design gate's
// refusal names (internal/plansession, missingDesign) and the words the
// planning session is told to write, so the three places must keep the same
// spelling.
var designRulePhrases = []string{masterplan.DesignSystemFile, "wins on behaviour", "wins on looks"}

// designFacts reads from a plan's text the four things the design gate of a
// planning session asks about. Pure.
//
//   - rule: the text before the first "## " heading (the title and the "How to
//     use this document" paragraph) holds all of designRulePhrases. Case is
//     ignored and any run of white space counts as one space, because the
//     paragraph is prose and a line break inside "wins on behaviour" does not
//     change what it says. Only the header counts: the same words in a task's
//     entry would be a note about one task, not the rule for the whole plan.
//   - task: the id of the first task masterplan's Task.DesignSystem
//     recognises, "" when there is none. That method is also how the daemon
//     picks the task whose inbox item gets the reconcile step, so the gate
//     cannot pass on a task the daemon would treat as an ordinary one.
//   - first: that task is in the same milestone as the first task in the
//     file. A plan's first milestone is not always M1 (this repository's own
//     plan starts at M0), so the number is taken from the plan, not assumed.
//   - dependents: how many tasks name that id in their Depends on line.
//
// A plan masterplan.Parse would refuse gives four zero values, the header
// rule included. The checker reports such a plan as an error and the gate
// asks about errors first, so the zeros are never what the owner is told; and
// facts read from half a plan could name a design task that the daemon,
// which refuses the plan whole, would never see.
func designFacts(plan []byte) (rule bool, task string, first bool, dependents int) {
	outline := masterplan.Scan(plan)
	// The two conditions under which masterplan.Parse returns an error.
	if len(outline.Problems) > 0 || len(outline.Tasks) == 0 {
		return false, "", false, 0
	}

	// Headings come from the scan, so a "## " line inside a code fence does
	// not end the header.
	lines := strings.Split(string(plan), "\n")
	for _, h := range outline.Headings {
		if h.Level == 2 {
			lines = lines[:h.Line-1]
			break
		}
	}
	header := strings.ToLower(strings.Join(strings.Fields(strings.Join(lines, "\n")), " "))
	rule = true
	for _, phrase := range designRulePhrases {
		if !strings.Contains(header, strings.ToLower(phrase)) {
			rule = false
		}
	}

	for _, t := range outline.Tasks {
		if t.DesignSystem() {
			task = t.ID
			break
		}
	}
	if task == "" {
		return rule, "", false, 0
	}
	firstMilestone, okFirst := taskMilestone(outline.Tasks[0].ID)
	milestone, ok := taskMilestone(task)
	first = ok && okFirst && milestone == firstMilestone
	for _, t := range outline.Tasks {
		for _, dep := range t.DependsOn {
			if dep == task {
				dependents++
				break
			}
		}
	}
	return rule, task, first, dependents
}

// taskMilestone is the milestone number in a task id: 1 for T1.4, 12 for
// T12.3a. A task has no milestone field (masterplan.Task holds the id, the
// title and the dependencies), so the id is the only place to read it from.
// ok is false for a text that is not a task id, or whose number is too long
// to read.
func taskMilestone(id string) (n int, ok bool) {
	if !masterplan.ValidID(id) {
		return 0, false
	}
	number, _, _ := strings.Cut(strings.TrimPrefix(id, "T"), ".")
	n, err := strconv.Atoi(number)
	return n, err == nil
}

// parsePlanFlags parses a planning command's flags and refuses any word left
// over. It returns the exit code to stop with, or -1 to go on.
func parsePlanFlags(flags *flag.FlagSet, args []string, cmd, usageLine string, stderr io.Writer) int {
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s\nRun `afk plan --help` for what each command does.\n", usageLine)
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected word %q.\nUsage: %s\n", cmd, flags.Arg(0), usageLine)
		return 2
	}
	return -1
}

// planRequest is one command line that starts or resumes a planning session,
// once its flags and words are read.
type planRequest struct {
	// cmd is the command as the owner typed it ("afk plan amend"), for the
	// start of every message.
	cmd, usageLine string
	// mode is extend, amend or reconcile; "" is a bare `afk plan`.
	mode              string
	project, provider string
	dryRun, yes       bool
	// text is what `amend` was asked to change; file is the document
	// `reconcile` was given, as typed.
	text, file string
}

// planSeed is what a start writes into .afk/plan/ besides the phase.
type planSeed struct {
	Mode string
	// Brief is the whole of brief.md, "" when the session asks for the brief
	// itself.
	Brief string
	// Yes: write the marker that says the owner already agreed to the change.
	Yes bool
}

// runPlanSession implements `afk plan`: start the project's planning session,
// or come back to it. It returns the exit code: 0 when the owner left the
// session normally or a dry run printed its answer, 1 when the session could
// not be started here, 2 for a command line that could never work, and the
// `afk attach` codes (10 taken over elsewhere, 11 the session ended, 12 it is
// not running) once it has attached.
func runPlanSession(args []string, stdout, stderr io.Writer, deps planDeps) int {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	proj := addProjectFlag(flags)
	providerFlag := flags.String("provider", "", "Agent the session runs: "+strings.Join(provider.Names(), "|"))
	dryRun := flags.Bool("dry-run", false, "Print what would happen and change nothing")
	if code := parsePlanFlags(flags, args, "afk plan", planSessionUsageLine, stderr); code >= 0 {
		return code
	}
	return runPlanRequest(planRequest{
		cmd:       "afk plan",
		usageLine: planSessionUsageLine,
		project:   *proj,
		provider:  *providerFlag,
		dryRun:    *dryRun,
	}, stdout, stderr, deps)
}

// planModeUsageLine is the usage line of the command that starts a mode, ""
// for a word that is not one of the three.
func planModeUsageLine(mode string) string {
	switch mode {
	case plansession.ModeExtend:
		return planExtendUsageLine
	case plansession.ModeAmend:
		return planAmendUsageLine
	case plansession.ModeReconcile:
		return planReconcileUsageLine
	}
	return ""
}

// runPlanMode implements `afk plan extend`, `afk plan amend "<what>"` and
// `afk plan reconcile FILE`: start a planning session that changes the plan
// the project already has. Exit codes are those of `afk plan`.
//
// Flags are accepted before, between and after the words, for the reason
// runPlanCheck gives: Go's flag package stops at the first word, and
// `afk plan amend "add an export" --yes` is the order people type.
//
// --yes exists for amend only, so on the other two it is an unknown flag and
// not a flag that is silently ignored.
func runPlanMode(mode string, args []string, stdout, stderr io.Writer, deps planDeps) int {
	usageLine := planModeUsageLine(mode)
	if usageLine == "" {
		fmt.Fprintf(stderr, "afk plan: %q is not a way to change a plan. Run one of:\n%s", mode, planModeCommands)
		return 2
	}
	cmd := "afk plan " + mode
	flags := flag.NewFlagSet("plan "+mode, flag.ContinueOnError)
	flags.SetOutput(stderr)
	proj := addProjectFlag(flags)
	providerFlag := flags.String("provider", "", "Agent the session runs: "+strings.Join(provider.Names(), "|"))
	dryRun := flags.Bool("dry-run", false, "Print what would happen and change nothing")
	yes := new(bool)
	if mode == plansession.ModeAmend {
		flags.BoolVar(yes, "yes", false, "Do not ask for confirmation before the change is made")
	}
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s\nRun `afk plan --help` for what each command does.\n", usageLine)
	}

	var words []string
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		rest = flags.Args()
		if len(rest) == 0 {
			break
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, cmd+": "+format+"\n", a...)
		fmt.Fprintln(stderr, "Usage: "+usageLine)
		return 2
	}

	req := planRequest{
		cmd:       cmd,
		usageLine: usageLine,
		mode:      mode,
		project:   *proj,
		provider:  *providerFlag,
		dryRun:    *dryRun,
		yes:       *yes,
	}
	switch mode {
	case plansession.ModeExtend:
		if len(words) != 0 {
			return usage("unexpected word %q. This command takes no word: the session asks you what the new milestone is for.", words[0])
		}
	case plansession.ModeAmend:
		req.text = strings.Join(words, " ")
		if strings.TrimSpace(req.text) == "" {
			return usage("say what to change, in quotes. For example: afk plan amend \"add a task that exports CSV\".")
		}
	case plansession.ModeReconcile:
		if len(words) != 1 || strings.TrimSpace(words[0]) == "" {
			return usage("expected one file, the document to bring the plan in line with, but got %d word(s).", len(words))
		}
		req.file = words[0]
	}
	return runPlanRequest(req, stdout, stderr, deps)
}

// runPlanRequest is the body of `afk plan` and of the three mode commands:
// look at the project, decide (decidePlanStart), and do what was decided.
//
// A bare `afk plan` on a project with a plan asks which change is wanted and
// then goes on as the mode command would have, through the same decision, so
// an answer typed here and a command typed in full cannot behave differently.
func runPlanRequest(req planRequest, stdout, stderr io.Writer, deps planDeps) int {
	// The same check `afk new` makes on --provider (resolveProviderArg), minus
	// its rule about conductor sessions, which a planning session is not.
	// Whether the agent can run a planning session at all is the provider's
	// own answer, PlanArgv, and is a refusal (exit 1), not bad usage.
	p, err := provider.Lookup(req.provider)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\nUsage: %s\n", req.cmd, err, req.usageLine)
		return 2
	}
	_, planArgvErr := p.PlanArgv()

	project, code := resolvePlanProject(req.cmd, req.project, stderr, deps)
	if code != 0 {
		return code
	}
	files, planPath, err := loadPlanSession(project.Root, deps)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", req.cmd, err)
		return 1
	}
	status := plansession.Decide(files)
	terminal := deps.stdinTTY() && deps.stdoutTTY()

	in := planStartInput{
		DryRun:       req.dryRun,
		Mode:         req.mode,
		IsRepo:       project.IsRepo,
		HasState:     files.HasState,
		StateDamaged: files.StateProblem != "",
		Recorded:     files.State.Phase,
		Finished:     files.HasState && plansession.Finished(files.State),
		HasOldFiles:  planFolderHasFiles(project.Root),
		PlanExists:   files.PlanExists,
		HostAlive:    deps.hostAlive(project.Root),
		Terminal:     terminal,
		PlanArgvErr:  planArgvErr,
	}
	action := decidePlanStart(in)
	if action.Kind == planChooseMode {
		if code := choosePlanMode(&req, planPath, stderr, deps); code != 0 {
			return code
		}
		in.Mode = req.mode
		action = decidePlanStart(in)
	}

	// The document is looked for only once nothing else stands in the way, so
	// that "this project has no plan" is not hidden behind a mistyped path. It
	// is looked for before a dry run answers, or the dry run would promise a
	// start the real command refuses.
	seed := planSeed{Mode: plansession.ModeNew}
	if req.mode != "" {
		seed = planSeed{Mode: req.mode, Yes: req.yes}
	}
	switch action.Kind {
	case planDryRun, planRefuseNoTerminal, planStart:
		switch req.mode {
		case plansession.ModeAmend:
			seed.Brief = req.text
			if !strings.HasSuffix(seed.Brief, "\n") {
				seed.Brief += "\n"
			}
		case plansession.ModeReconcile:
			rel, err := resolveReconcileFile(req.file, deps.cwd, project.Root)
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\nNothing was started.\n", req.cmd, err)
				return 1
			}
			seed.Brief = fmt.Sprintf("Reconcile the plan with %s.\n", rel)
		}
	}

	spec := session.HostSpawn{
		Name:       session.PlanSessionName,
		Worktree:   project.Root,
		MasterPlan: true,
		Provider:   req.provider,
	}

	switch action.Kind {
	case planRefuseProvider:
		fmt.Fprintf(stderr, "%s: %v.\nNothing was started.\n", req.cmd, planArgvErr)
		return 1
	case planRefuseDamaged:
		fmt.Fprintf(stderr, "%s: %s\nNothing was started. Fix %s by hand, then run `afk plan` again. Deleting it starts the session over from the first phase; the brief and your answers in that folder are kept.\n",
			req.cmd, files.StateProblem, plansession.StatePath(project.Root))
		return 1
	case planRefuseNoPlan:
		fmt.Fprintf(stderr, "%s: this project has no plan yet (afk looked for it at %s), so there is nothing to change.\nRun `afk plan` first to write one. Nothing was started.\n", req.cmd, planPath)
		return 1
	case planRefuseInProgress:
		phase := status.Phase
		if phase == "" {
			phase = files.State.Phase
		}
		fmt.Fprintf(stderr, "%s: a planning session is already open in this project. It is in the %s mode, at the %s phase.\nRun `afk plan` to go back to it. A new change can be started once that session is finished. Nothing was started.\n",
			req.cmd, files.State.Mode, phase)
		return 1
	case planRefusePlanGone:
		fmt.Fprintf(stderr, "%s: the planning session here is finished, but its plan is not at %s.\nPut the plan back there, or name the right file with the `plan` key in afk.toml, then run `afk plan` again. To plan this project from the start instead, move the folder %s away first. Nothing was started.\n",
			req.cmd, planPath, plansession.Dir(project.Root))
		return 1
	case planRefuseHostBusy:
		fmt.Fprintf(stderr, "%s: the earlier planning session is finished, but its agent is still running.\nLeave that agent and run `afk clean %s`, then run this command again. To look at it first, run `afk attach %s`. Nothing was started.\n",
			req.cmd, session.PlanSessionName, session.PlanSessionName)
		return 1
	case planRefuseNeedsMode:
		fmt.Fprintf(stderr, "%s: This project already has a plan at %s.\nSay how you want to change it by running one of these in a terminal:\n%sNothing was started.\n", req.cmd, planPath, planModeCommands)
		return 1
	case planDryRun:
		printPlanDryRun(stdout, planDryRunView{
			cmd:      req.cmd,
			project:  project,
			action:   action,
			status:   status,
			command:  session.ChildCommandLine(spec),
			terminal: terminal,
			seed:     seed,
			planPath: planPath,
		})
		return 0
	case planRefuseNoTerminal:
		// A mode command is not repeated here with --dry-run added: amend and
		// reconcile need their word too, and the owner still has it on screen.
		advice := "Run `afk plan` yourself in a terminal, or `afk plan --dry-run` to see what it would do."
		if req.mode != "" {
			advice = "Run the same command yourself in a terminal, or add --dry-run to see what it would do."
		}
		fmt.Fprintf(stderr, "%s: this needs a terminal, because the planning session asks you questions. Nothing was started.\n%s\n", req.cmd, advice)
		return 1
	case planAttach:
		return finishPlanAttach(stdout, stderr, project.Root, false, deps)
	case planStart:
		archived, code := startPlanSession(stderr, req.cmd, project, action, seed, spec, deps)
		if code != 0 {
			return code
		}
		code = finishPlanAttach(stdout, stderr, project.Root, true, deps)
		// Said after the attach for the reason finishPlanAttach gives: printed
		// before, the full-screen session would wipe it.
		if archived != "" {
			fmt.Fprintf(stdout, "The earlier planning session's files were moved to %s.\n", archived)
		}
		return code
	default:
		fmt.Fprintf(stderr, "%s: internal error: no decision was made. Nothing was started.\n", req.cmd)
		return 1
	}
}

// planFolderHasFiles reports whether .afk/plan/ holds anything a new change
// session would move into the archive: any entry but the archive folder. A
// folder that is missing or cannot be read counts as empty; the real move
// (plansession.Archive) reports a folder it cannot read.
func planFolderHasFiles(root string) bool {
	entries, err := os.ReadDir(plansession.Dir(root))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() == planArchiveFolder && entry.IsDir() {
			continue
		}
		return true
	}
	return false
}

// choosePlanMode asks the owner which change they want and fills the request
// in as the matching command would have. It returns the exit code to stop
// with (0 to go on).
//
// An answer that is empty or not one of the three starts nothing. Guessing
// would be worse than stopping: the three modes ask for different things, and
// a session started in the wrong one has to be finished or cleaned away
// before the right one can start.
func choosePlanMode(req *planRequest, planPath string, stderr io.Writer, deps planDeps) int {
	nothing := func(why string) int {
		fmt.Fprintf(stderr, "afk plan: %s Nothing was started.\nTo change the plan, run `afk plan` again, or one of:\n%s", why, planModeCommands)
		return 1
	}
	// A failed read (the terminal was closed) is the same as no answer.
	ask := func(prompt string) string {
		answer, err := deps.ask(prompt)
		if err != nil {
			return ""
		}
		return answer
	}

	answer := strings.TrimSpace(ask(fmt.Sprintf("This project already has a plan at %s. What do you want to do?\n"+
		"  1) add a milestone (extend)\n"+
		"  2) add, split or replace one task (amend)\n"+
		"  3) bring the plan in line with a document (reconcile)\n"+
		"Type 1, 2 or 3 and press Enter: ", planPath)))
	switch strings.ToLower(answer) {
	case "1", plansession.ModeExtend:
		req.mode = plansession.ModeExtend
	case "2", plansession.ModeAmend:
		req.text = ask("What do you want to change? Say it in a sentence: ")
		if strings.TrimSpace(req.text) == "" {
			return nothing("You did not say what to change.")
		}
		req.mode = plansession.ModeAmend
	case "3", plansession.ModeReconcile:
		req.file = strings.TrimSpace(ask("Which document? Give its path inside the project: "))
		if req.file == "" {
			return nothing("You did not name a document.")
		}
		req.mode = plansession.ModeReconcile
	case "":
		return nothing("You did not pick one.")
	default:
		return nothing(fmt.Sprintf("%q is not one of the three choices.", answer))
	}
	req.cmd = "afk plan " + req.mode
	req.usageLine = planModeUsageLine(req.mode)
	return 0
}

// resolveReconcileFile finds the document `afk plan reconcile` was given and
// returns its path relative to the project folder, with forward slashes.
//
// The path must be relative to the project because that is where the planning
// session's agent runs, and the brief names the file for it. It must be
// inside the project for the same reason: the agent is not told to read
// anything outside.
//
// A relative path is looked for in the folder the command was typed in, as
// every command does, and then in the project folder. The second place is for
// `afk plan reconcile --project ~/shop docs/design-system.md` typed from
// somewhere else, where the owner plainly means the project's own file.
func resolveReconcileFile(arg, cwd, root string) (string, error) {
	var candidates []string
	switch {
	case filepath.IsAbs(arg):
		candidates = []string{filepath.Clean(arg)}
	default:
		if cwd != "" {
			candidates = append(candidates, filepath.Join(cwd, arg))
		}
		if inRoot := filepath.Join(root, arg); len(candidates) == 0 || candidates[0] != inRoot {
			candidates = append(candidates, inRoot)
		}
	}

	outside, folder := "", ""
	for _, path := range candidates {
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if outside == "" {
				outside = path
			}
			continue
		}
		if fi.IsDir() {
			if folder == "" {
				folder = path
			}
			continue
		}
		return filepath.ToSlash(rel), nil
	}
	switch {
	case folder != "":
		return "", fmt.Errorf("%s is a folder, not a file. Name the one document to bring the plan in line with", folder)
	case outside != "":
		return "", fmt.Errorf("%s is outside the project folder %s. The planning session only reads files inside the project, so copy the document into the project and name the copy", outside, root)
	}
	return "", fmt.Errorf("the file %s was not found (afk looked for %s). Check the path; it must be a file inside the project folder %s", arg, strings.Join(candidates, " and "), root)
}

// startPlanSession does what a start needs before the terminal is handed
// over. It returns the folder an earlier session's files were moved to ("" when
// none were) and the exit code to stop with (0 to go on).
//
// The order matters. The marker is written before the host is spawned and on
// every start, not only the first: cleaning the session removes its folder and
// the marker with it, and a host spawned without the marker would be relaunched
// by the dashboard as an ordinary session. The state is seeded before the spawn
// so that the agent's first command, `afk plan status`, finds a session. An
// existing state.json is never rewritten here, except that a change session
// replaces a finished one's after moving it to the archive: after the seed,
// `afk plan advance` is its only writer.
//
// Within the seed, the old files are moved away first (Archive moves
// everything, so anything written before it would go with them), and
// state.json is written last. A start that fails before state.json exists
// leaves no session, so the same command can simply be run again; had the
// state been written first, the second try would be refused as "a session is
// already open" and would find it without its brief.
func startPlanSession(stderr io.Writer, cmd string, project planProject, action planStartAction, seed planSeed, spec session.HostSpawn, deps planDeps) (string, int) {
	root := project.Root
	if action.InitRepo {
		if err := deps.gitInit(root); err != nil {
			fmt.Fprintf(stderr, "%s: could not turn %s into a git repository: %v\nNothing was started. Check that git is installed and that you may write to the folder.\n", cmd, root, err)
			return "", 1
		}
	}
	sessionDir := filepath.Join(root, ".afk", session.PlanSessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "%s: could not create %s: %v\nNothing was started.\n", cmd, sessionDir, err)
		return "", 1
	}
	if err := session.WritePlanningMarker(sessionDir); err != nil {
		fmt.Fprintf(stderr, "%s: could not write to %s: %v\nNothing was started.\n", cmd, sessionDir, err)
		return "", 1
	}
	archived := ""
	if action.SeedState {
		if action.Archive {
			var err error
			archived, err = plansession.Archive(root, deps.now())
			if err != nil {
				fmt.Fprintf(stderr, "%s: could not put the earlier planning session's files away: %v.\nNothing was started.\n", cmd, err)
				return archived, 1
			}
		}
		// Where the old files went is part of every failure from here on, or
		// the owner would look for them in the folder they just left.
		moved := ""
		if archived != "" {
			moved = fmt.Sprintf(" The earlier planning session's files are in %s.", archived)
		}
		planDir := plansession.Dir(root)
		if err := os.MkdirAll(planDir, 0o755); err != nil {
			fmt.Fprintf(stderr, "%s: could not create %s: %v\nNothing was started.%s\n", cmd, planDir, err, moved)
			return archived, 1
		}
		if seed.Brief != "" {
			path := filepath.Join(planDir, planBriefFile)
			if err := os.WriteFile(path, []byte(seed.Brief), 0o644); err != nil {
				fmt.Fprintf(stderr, "%s: could not write %s: %v\nNothing was started.%s\n", cmd, path, err, moved)
				return archived, 1
			}
		}
		if seed.Yes {
			path := filepath.Join(planDir, plansession.YesFile)
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				fmt.Fprintf(stderr, "%s: could not write %s: %v\nNothing was started.%s\n", cmd, path, err, moved)
				return archived, 1
			}
		}
		state := plansession.State{
			Mode:      seed.Mode,
			Phase:     plansession.PhaseBrief,
			UpdatedAt: plansession.FormatTime(deps.now()),
		}
		if err := plansession.WriteState(root, state); err != nil {
			fmt.Fprintf(stderr, "%s: could not write %s: %v\nNothing was started.%s\n", cmd, plansession.StatePath(root), err, moved)
			return archived, 1
		}
	}
	if _, err := deps.spawn(spec); err != nil {
		fmt.Fprintf(stderr, "%s: could not start the planning session: %v\nRun `afk plan` again once that is fixed; what is already saved in .afk/plan/ is kept.\n", cmd, err)
		return archived, 1
	}
	return archived, 0
}

// finishPlanAttach hands the terminal to the session and prints how it ended.
// The message is printed only after attach returns, when the terminal is out
// of the full-screen mode; printed earlier it would be wiped by the restore.
func finishPlanAttach(stdout, stderr io.Writer, root string, started bool, deps planDeps) int {
	msg, code, isError := deps.attach(root, started)
	if isError {
		fmt.Fprintln(stderr, msg)
	} else {
		fmt.Fprintln(stdout, msg)
	}
	return code
}

// planDryRunView is everything a dry run prints, already loaded.
type planDryRunView struct {
	// cmd is the command as typed, without its words ("afk plan amend").
	cmd      string
	project  planProject
	action   planStartAction
	status   plansession.Status
	command  string
	terminal bool
	// seed is what a start would write: its mode, and the brief when the
	// command line supplies it.
	seed     planSeed
	planPath string
}

// printPlanDryRun says what the command would do in this project. It reads
// only values that were already loaded, so a dry run cannot write anything.
func printPlanDryRun(w io.Writer, v planDryRunView) {
	action := v.action
	fmt.Fprintf(w, "Project: %s\n", v.project.Root)
	if action.AskMode {
		fmt.Fprintf(w, "This project already has a plan at %s, so `afk plan` would ask how you want to change it, and start nothing until you answer.\n", v.planPath)
		fmt.Fprintf(w, "To say it on the command line, run one of:\n%s", planModeCommands)
		if !v.terminal {
			fmt.Fprintln(w, "This is not a terminal. Without --dry-run, `afk plan` would stop here and start nothing, because there is nobody to ask.")
		}
		fmt.Fprintln(w, "This was a dry run. Nothing was changed.")
		return
	}
	switch {
	case action.InitRepo:
		fmt.Fprintln(w, "Would run `git init` there, because the folder is not a git repository yet.")
	case !v.project.IsRepo:
		fmt.Fprintln(w, "The folder is not a git repository, but its planning session is already running, so `git init` would not be run.")
	default:
		fmt.Fprintln(w, "The folder is already a git repository, so `git init` would not be run.")
	}

	phase := ""
	if v.status.Session && v.status.Phase != "" {
		phase = fmt.Sprintf(" It is at the %s phase. %s", v.status.Phase, v.status.Waiting)
	}
	// The mode of a session that exists is the recorded one; a start names the
	// mode it would seed.
	running := ""
	if v.status.Mode != "" {
		running = fmt.Sprintf(" (mode: %s)", v.status.Mode)
	}
	switch {
	case action.Reattach:
		fmt.Fprintf(w, "Would reattach to the planning session that is already running%s.%s\n", running, phase)
		fmt.Fprintf(w, "The session runs: %s\n", v.command)
	case action.SeedState:
		if action.Archive {
			fmt.Fprintf(w, "Would move the earlier planning session's files from %s into its archive folder, so nothing is overwritten.\n", plansession.Dir(v.project.Root))
		}
		fmt.Fprintf(w, "Would start a new planning session (mode: %s) at the %s phase and open it in this terminal.\n", v.seed.Mode, plansession.PhaseBrief)
		if v.seed.Brief != "" {
			fmt.Fprintf(w, "Would save this as the session's brief: %s", v.seed.Brief)
		}
		if v.seed.Yes {
			fmt.Fprintln(w, "Would tell the session that you already agreed to the change (--yes), so it does not ask before making it.")
		}
		fmt.Fprintf(w, "Would start: %s\n", v.command)
	default:
		fmt.Fprintf(w, "Would resume the planning session%s and open it in this terminal.%s\n", running, phase)
		fmt.Fprintf(w, "Would start: %s\n", v.command)
	}
	if !v.terminal {
		fmt.Fprintf(w, "This is not a terminal. Without --dry-run, `%s` would stop here and start nothing, because the session asks you questions.\n", v.cmd)
	}
	fmt.Fprintln(w, "This was a dry run. Nothing was changed.")
}

// runPlanStatus implements `afk plan status`: print the planning session's
// mode, phase and what it waits for. Exit codes: 0 printed (a project with no
// session included), 1 a session file is damaged or the project could not be
// read, 2 the command was written wrongly.
//
// It only reads. In particular it never turns a folder into a repository:
// asking where a session stands must be safe to do anywhere.
func runPlanStatus(args []string, stdout, stderr io.Writer, deps planDeps) int {
	flags := flag.NewFlagSet("plan status", flag.ContinueOnError)
	proj := addProjectFlag(flags)
	if code := parsePlanFlags(flags, args, "afk plan status", planStatusUsageLine, stderr); code >= 0 {
		return code
	}
	project, code := resolvePlanProject("afk plan status", *proj, stderr, deps)
	if code != 0 {
		return code
	}
	files, _, err := loadPlanSession(project.Root, deps)
	if err != nil {
		fmt.Fprintf(stderr, "afk plan status: %v\n", err)
		return 1
	}
	status := plansession.Decide(files)

	fmt.Fprintf(stdout, "Project: %s\n", project.Root)
	if !status.Session {
		fmt.Fprintln(stdout, "There is no planning session here. Run `afk plan` in this folder to start one.")
	} else {
		printPlanStatus(stdout, status)
	}
	for _, problem := range status.Problems {
		fmt.Fprintf(stdout, "Problem: %s\n", problem)
	}
	if len(status.Problems) > 0 {
		return 1
	}
	return 0
}

// printPlanStatus prints the three lines both status and advance show. When a
// file of an earlier phase has gone missing the phase is earlier than the one
// recorded, and the line says both, or the owner would see a session that
// seems to have gone backwards with no reason given.
func printPlanStatus(w io.Writer, status plansession.Status) {
	mode, phase := status.Mode, status.Phase
	if mode == "" {
		mode = "unknown"
	}
	switch {
	case phase == "":
		phase = "unknown"
	case phase != status.Recorded:
		phase = fmt.Sprintf("%s (the session had reached %s, but this earlier phase is not finished)", phase, status.Recorded)
	}
	fmt.Fprintf(w, "Mode: %s\nPhase: %s\nWaiting for: %s\n", mode, phase, status.Waiting)
}

// runPlanAdvance implements `afk plan advance`: move the recorded phase on by
// one when the current phase's work is on disk. Exit codes: 0 moved, 1 refused
// (the reason is printed) or the state could not be written, 2 the command was
// written wrongly.
//
// This is the command the planning session's agent runs, and after `afk plan`
// seeds state.json it is that file's only writer. The decision is
// plansession.Advance; this function loads, asks and writes. The agent cannot
// talk its way past a gate, because the gate looks at the files.
func runPlanAdvance(args []string, stdout, stderr io.Writer, deps planDeps) int {
	flags := flag.NewFlagSet("plan advance", flag.ContinueOnError)
	proj := addProjectFlag(flags)
	if code := parsePlanFlags(flags, args, "afk plan advance", planAdvanceUsageLine, stderr); code >= 0 {
		return code
	}
	project, code := resolvePlanProject("afk plan advance", *proj, stderr, deps)
	if code != 0 {
		return code
	}
	files, _, err := loadPlanSession(project.Root, deps)
	if err != nil {
		fmt.Fprintf(stderr, "afk plan advance: %v\n", err)
		return 1
	}
	next, err := plansession.Advance(files)
	if err != nil {
		// Advance's reasons are whole sentences, so they go on their own line.
		fmt.Fprintf(stderr, "afk plan advance: the phase was not changed.\n%v\n", err)
		return 1
	}

	old := files.State.Phase
	files.State.Phase = next
	files.State.UpdatedAt = plansession.FormatTime(deps.now())
	if err := plansession.WriteState(project.Root, files.State); err != nil {
		fmt.Fprintf(stderr, "afk plan advance: could not write %s: %v\nThe phase was not changed.\n", plansession.StatePath(project.Root), err)
		return 1
	}
	fmt.Fprintf(stdout, "Phase: %s → %s\n", old, next)
	fmt.Fprintf(stdout, "Waiting for: %s\n", plansession.Decide(files).Waiting)
	// Only a new plan reaching review asks for the owner's read. The three
	// modes that change a plan have their own commands and no notification.
	if files.State.Mode == plansession.ModeNew && next == plansession.PhaseReview {
		notifyPlanReady(project.Root, deps.review, deps.now(), stderr)
	}
	return 0
}
