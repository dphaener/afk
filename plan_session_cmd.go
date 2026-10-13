package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/host"
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
)

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
}

func realPlanDeps() planDeps {
	// A failed Getwd or UserHomeDir is passed on as "": resolveProject only
	// needs them when --project is absent or starts with ~, and it reports
	// the case it cannot resolve.
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
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
	planRefuseHasPlan
	planDryRun
	planRefuseNoTerminal
	planAttach
	planStart
)

// planStartInput is every fact decidePlanStart reads, as plain values. The
// caller does the looking (git, the filesystem, the terminal, the host's pid).
type planStartInput struct {
	DryRun bool
	// IsRepo: the folder is inside a git repository.
	IsRepo bool
	// HasState: .afk/plan/state.json exists and is sound. StateDamaged: it
	// exists and is not.
	HasState     bool
	StateDamaged bool
	// Recorded is the phase in state.json, "" without one.
	Recorded string
	// PlanExists: the master plan file exists and is not blank.
	PlanExists bool
	HostAlive  bool
	// Terminal: stdin and stdout are both a terminal.
	Terminal bool
	// PlanArgvErr is the provider's refusal to run a planning session, nil
	// when it can.
	PlanArgvErr error
}

// planStartAction is the decision. InitRepo and SeedState say what a start
// does first; in a dry run they say what a start would do, and Reattach says
// it would join a host that is already running instead of starting one.
type planStartAction struct {
	Kind      planStartKind
	InitRepo  bool
	SeedState bool
	Reattach  bool
}

// decidePlanStart is the whole decision of `afk plan`, first match wins. Pure.
//
// The order is the point:
//
//   - The three refusals come before the dry run, because a dry run that
//     promised a start the real command then refuses would be a lie. A project
//     that already has a plan and no session is the case that matters: there
//     the real command must not start a session that would write a second plan
//     over the first.
//   - The dry run comes before the terminal check, because a dry run asks no
//     questions and is what a script or an agent uses to see what would happen.
//   - No terminal comes before attach and start, so that with nobody able to
//     answer, nothing is started at all. A planning host started from a script
//     would sit at its first question inside a detached process, looking alive.
//
// A session in progress is always resumed, even when the master plan exists:
// from the draft phase on the plan is the session's own work.
func decidePlanStart(in planStartInput) planStartAction {
	would := planStartAction{
		InitRepo:  !in.IsRepo && !in.HostAlive,
		SeedState: !in.HasState && !in.HostAlive,
		Reattach:  in.HostAlive,
	}
	switch {
	case in.PlanArgvErr != nil:
		return planStartAction{Kind: planRefuseProvider}
	case in.StateDamaged:
		return planStartAction{Kind: planRefuseDamaged}
	case (in.PlanExists && !in.HasState) || in.Recorded == plansession.PhaseApproved:
		return planStartAction{Kind: planRefuseHasPlan}
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
// The checker's error count is filled in here because plansession.Load leaves
// it alone (that package does not know the plan format). A plan that exists
// but cannot be read a second time is counted as it was loaded: no errors
// known, and the gate that needs the count is the check phase's, which will be
// asked again.
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
		}
	}
	return f, planPath, nil
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

	// The same check `afk new` makes on --provider (resolveProviderArg), minus
	// its rule about conductor sessions, which a planning session is not.
	// Whether the agent can run a planning session at all is the provider's
	// own answer, PlanArgv, and is a refusal (exit 1), not bad usage.
	p, err := provider.Lookup(*providerFlag)
	if err != nil {
		fmt.Fprintf(stderr, "afk plan: %v\nUsage: %s\n", err, planSessionUsageLine)
		return 2
	}
	_, planArgvErr := p.PlanArgv()

	project, code := resolvePlanProject("afk plan", *proj, stderr, deps)
	if code != 0 {
		return code
	}
	files, planPath, err := loadPlanSession(project.Root, deps)
	if err != nil {
		fmt.Fprintf(stderr, "afk plan: %v\n", err)
		return 1
	}
	status := plansession.Decide(files)

	action := decidePlanStart(planStartInput{
		DryRun:       *dryRun,
		IsRepo:       project.IsRepo,
		HasState:     files.HasState,
		StateDamaged: files.StateProblem != "",
		Recorded:     files.State.Phase,
		PlanExists:   files.PlanExists,
		HostAlive:    deps.hostAlive(project.Root),
		Terminal:     deps.stdinTTY() && deps.stdoutTTY(),
		PlanArgvErr:  planArgvErr,
	})
	spec := session.HostSpawn{
		Name:       session.PlanSessionName,
		Worktree:   project.Root,
		MasterPlan: true,
		Provider:   *providerFlag,
	}

	switch action.Kind {
	case planRefuseProvider:
		fmt.Fprintf(stderr, "afk plan: %v.\nNothing was started.\n", planArgvErr)
		return 1
	case planRefuseDamaged:
		fmt.Fprintf(stderr, "afk plan: %s\nNothing was started. Fix %s by hand, then run `afk plan` again. Deleting it starts the session over from the first phase; the brief and your answers in that folder are kept.\n",
			files.StateProblem, plansession.StatePath(project.Root))
		return 1
	case planRefuseHasPlan:
		fmt.Fprintf(stderr, "afk plan: This project already has a plan at %s.\nChanging an existing plan is not built yet, so nothing was started. Run `afk plan lint` to check the plan, or edit it by hand.\n", planPath)
		return 1
	case planDryRun:
		printPlanDryRun(stdout, project, action, status, session.ChildCommandLine(spec), deps.stdinTTY() && deps.stdoutTTY())
		return 0
	case planRefuseNoTerminal:
		fmt.Fprintln(stderr, "afk plan: this needs a terminal, because the planning session asks you questions. Nothing was started.\nRun `afk plan` yourself in a terminal, or `afk plan --dry-run` to see what it would do.")
		return 1
	case planAttach:
		return finishPlanAttach(stdout, stderr, project.Root, false, deps)
	case planStart:
		if code := startPlanSession(stderr, project, action, spec, deps); code != 0 {
			return code
		}
		return finishPlanAttach(stdout, stderr, project.Root, true, deps)
	default:
		fmt.Fprintln(stderr, "afk plan: internal error: no decision was made. Nothing was started.")
		return 1
	}
}

// startPlanSession does what a start needs before the terminal is handed
// over, and returns the exit code to stop with (0 to go on).
//
// The order matters. The marker is written before the host is spawned and on
// every start, not only the first: cleaning the session removes its folder and
// the marker with it, and a host spawned without the marker would be relaunched
// by the dashboard as an ordinary session. The state is seeded before the spawn
// so that the agent's first command, `afk plan status`, finds a session. An
// existing state.json is never rewritten here: after the seed, `afk plan
// advance` is its only writer.
func startPlanSession(stderr io.Writer, project planProject, action planStartAction, spec session.HostSpawn, deps planDeps) int {
	root := project.Root
	if action.InitRepo {
		if err := deps.gitInit(root); err != nil {
			fmt.Fprintf(stderr, "afk plan: could not turn %s into a git repository: %v\nNothing was started. Check that git is installed and that you may write to the folder.\n", root, err)
			return 1
		}
	}
	sessionDir := filepath.Join(root, ".afk", session.PlanSessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "afk plan: could not create %s: %v\nNothing was started.\n", sessionDir, err)
		return 1
	}
	if err := session.WritePlanningMarker(sessionDir); err != nil {
		fmt.Fprintf(stderr, "afk plan: could not write to %s: %v\nNothing was started.\n", sessionDir, err)
		return 1
	}
	if action.SeedState {
		seed := plansession.State{
			Mode:      plansession.ModeNew,
			Phase:     plansession.PhaseBrief,
			UpdatedAt: plansession.FormatTime(deps.now()),
		}
		if err := plansession.WriteState(root, seed); err != nil {
			fmt.Fprintf(stderr, "afk plan: could not write %s: %v\nNothing was started.\n", plansession.StatePath(root), err)
			return 1
		}
	}
	if _, err := deps.spawn(spec); err != nil {
		fmt.Fprintf(stderr, "afk plan: could not start the planning session: %v\nRun `afk plan` again once that is fixed; what is already saved in .afk/plan/ is kept.\n", err)
		return 1
	}
	return 0
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

// printPlanDryRun says what `afk plan` would do in this project. It reads only
// values that were already loaded, so a dry run cannot write anything.
func printPlanDryRun(w io.Writer, project planProject, action planStartAction, status plansession.Status, command string, terminal bool) {
	fmt.Fprintf(w, "Project: %s\n", project.Root)
	switch {
	case action.InitRepo:
		fmt.Fprintln(w, "Would run `git init` there, because the folder is not a git repository yet.")
	case !project.IsRepo:
		fmt.Fprintln(w, "The folder is not a git repository, but its planning session is already running, so `git init` would not be run.")
	default:
		fmt.Fprintln(w, "The folder is already a git repository, so `git init` would not be run.")
	}

	phase := ""
	if status.Session && status.Phase != "" {
		phase = fmt.Sprintf(" It is at the %s phase. %s", status.Phase, status.Waiting)
	}
	switch {
	case action.Reattach:
		fmt.Fprintf(w, "Would reattach to the planning session that is already running.%s\n", phase)
		fmt.Fprintf(w, "The session runs: %s\n", command)
	case action.SeedState:
		fmt.Fprintf(w, "Would start a new planning session at the %s phase and open it in this terminal.\n", plansession.PhaseBrief)
		fmt.Fprintf(w, "Would start: %s\n", command)
	default:
		fmt.Fprintf(w, "Would resume the planning session and open it in this terminal.%s\n", phase)
		fmt.Fprintf(w, "Would start: %s\n", command)
	}
	if !terminal {
		fmt.Fprintln(w, "This is not a terminal. Without --dry-run, `afk plan` would stop here and start nothing, because the session asks you questions.")
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
	return 0
}
