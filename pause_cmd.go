package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/decisionlog"
)

const (
	pauseUsageLine  = "afk pause [project]"
	resumeUsageLine = "afk resume [project]"
)

func printPauseUsage(w io.Writer) {
	fmt.Fprintf(w, `afk pause — stop the daemon from starting new tasks in a project

Usage:
  %s

<project> is a project name from afk's machine config, or a folder. With
nothing given, the project is the current folder's main checkout.

A task that is already running finishes; no new one starts until "afk resume".

Exit codes: 0 done, 1 the project or the config could not be used or the pause
could not be written, 2 the command was written wrongly.
`, pauseUsageLine)
}

func printResumeUsage(w io.Writer) {
	fmt.Fprintf(w, `afk resume — let the daemon start new tasks in a project again (and clear a stopped line)

Usage:
  %s

<project> is a project name from afk's machine config, or a folder. With
nothing given, the project is the current folder's main checkout.

It also clears a stopped line: if the daemon stopped after a task failed, this
lets it carry on at its next pass.

Exit codes: 0 done (or it was not paused), 1 the project or the config could
not be used or the pause or stop could not be removed, 2 the command was written wrongly.
`, resumeUsageLine)
}

// pauseEnv is everything runPause and runResume need from the machine, so the
// command bodies run in tests with no real home, git, config or clock.
type pauseEnv struct {
	cwd          string
	gitCommonDir func(string) string
	isDir        func(string) bool
	loadConfig   func() (daemonconfig.Config, error)
	now          func() time.Time
}

func realPauseEnv() pauseEnv {
	cwd, _ := os.Getwd()
	return pauseEnv{
		cwd:          cwd,
		gitCommonDir: resolveGitCommonDirIn,
		isDir: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		},
		loadConfig: func() (daemonconfig.Config, error) { return daemonconfig.Load(daemonconfig.DefaultDeps()) },
		now:        time.Now,
	}
}

// cmdPause implements `afk pause`.
func cmdPause(args []string) {
	os.Exit(runPause(args, os.Stdout, os.Stderr, realPauseEnv()))
}

// cmdResume implements `afk resume`.
func cmdResume(args []string) {
	os.Exit(runResume(args, os.Stdout, os.Stderr, realPauseEnv()))
}

// resolvePauseTarget picks the project folder the owner meant. It is pure; the
// caller does the folder and git lookups first.
//
// A configured project name wins over a folder of the same name, because the
// name is what the owner set up on purpose and a stray folder in the current
// directory must not redirect a pause to the wrong project. argAsRoot is the
// main checkout of arg when arg is an existing folder, else "". cwdRoot is the
// current folder's main checkout, used when no argument is given.
func resolvePauseTarget(arg, argAsRoot, cwdRoot string, projects []daemonconfig.Project) (string, error) {
	if arg == "" {
		if cwdRoot == "" {
			return "", errors.New("cannot tell which project to use. Pass a project name or a folder")
		}
		return cwdRoot, nil
	}
	for _, p := range projects {
		if p.Name == arg {
			return p.Path, nil
		}
	}
	if argAsRoot != "" {
		return argAsRoot, nil
	}
	if len(projects) == 0 {
		return "", fmt.Errorf("%q is not a folder, and no projects are configured. Pass a folder, or list projects in afk's machine config (config.toml in afk's config folder, normally ~/.config/afk/config.toml)", arg)
	}
	names := make([]string, 0, len(projects))
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return "", fmt.Errorf("%q is not a configured project or a folder. Known projects: %s", arg, strings.Join(names, ", "))
}

// targetFor does the impure half of resolvePauseTarget for one command line.
// A config that cannot be read only matters when the argument might be a name:
// a folder argument still works without it.
func targetFor(name, usageLine string, args []string, printUsage func(io.Writer), stderr io.Writer, env pauseEnv) (root string, code int, proceed bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUsage(stderr) }
	var words []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return "", 0, false
			}
			return "", 2, false
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}
	if len(words) > 1 {
		fmt.Fprintf(stderr, "%s: expected at most one project, but got %d words.\nUsage: %s\n", name, len(words), usageLine)
		return "", 2, false
	}
	arg := ""
	if len(words) == 1 {
		arg = words[0]
	}

	var projects []daemonconfig.Project
	var cfgErr error
	if arg != "" {
		var cfg daemonconfig.Config
		cfg, cfgErr = env.loadConfig()
		if cfgErr == nil {
			projects = cfg.Projects
		}
	}
	argAsRoot := ""
	if arg != "" && env.isDir(arg) {
		abs := arg
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(env.cwd, abs)
		}
		abs = filepath.Clean(abs)
		argAsRoot = mainCheckout(abs, env.gitCommonDir(abs))
	}
	cwdRoot := ""
	if arg == "" && env.cwd != "" {
		cwdRoot = mainCheckout(env.cwd, env.gitCommonDir(env.cwd))
	}
	root, err := resolvePauseTarget(arg, argAsRoot, cwdRoot, projects)
	if err != nil {
		if cfgErr != nil && argAsRoot == "" {
			fmt.Fprintf(stderr, "%s: %v\n", name, cfgErr)
			return "", 1, false
		}
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return "", 1, false
	}
	return root, 0, true
}

// pauseStage names the step of pauseProject or resumeProject that failed, so
// the command can word its error exactly as it did before the steps were
// split out of it.
type pauseStage int

const (
	stagePauseFlag pauseStage = iota
	stageStopMarker
	stageLogPause
	stageLogResume
	stageLogStopCleared
)

// pauseError is the failure of one step. The flag or marker steps leave the
// state unchanged; the log steps mean the file change was made but the
// decision line was not written.
type pauseError struct {
	stage pauseStage
	err   error
}

func (e *pauseError) Error() string { return e.err.Error() }
func (e *pauseError) Unwrap() error { return e.err }

// resumeOutcome says what resumeProject found and cleared. The stopped task
// and reason are read before the marker is removed, because they are gone once
// it is.
type resumeOutcome struct {
	WasPaused  bool
	WasStopped bool
	StopTask   string
	StopReason string
}

// pauseProject sets the project's pause flag and logs the owner's decision.
// already reports that the flag was there before, in which case the first
// time is kept (daemonstate.Pause). It prints nothing: the web page's pause
// button calls it too, and the page reports the result its own way.
func pauseProject(root string, now time.Time) (already bool, err error) {
	already = daemonstate.Paused(root)
	if err := daemonstate.Pause(root, now); err != nil {
		return already, &pauseError{stage: stagePauseFlag, err: err}
	}
	if err := logPauseDecision(root, now, "pause", "paused"); err != nil {
		return already, &pauseError{stage: stageLogPause, err: err}
	}
	return already, nil
}

// resumeProject clears the pause flag and the stop marker and logs what it
// cleared. Resuming a project that was neither paused nor stopped changes
// nothing and writes no log line, because nothing was decided. It prints
// nothing: the web page's resume button calls it too, and the page reports the
// result its own way.
func resumeProject(root string, now time.Time) (resumeOutcome, error) {
	var out resumeOutcome
	// Read the stop marker before clearing it: the message names the task and
	// the reason, which are gone once the file is.
	out.StopTask, out.StopReason, _ = daemonstate.ReadStop(root)
	was, err := daemonstate.Resume(root)
	if err != nil {
		return out, &pauseError{stage: stagePauseFlag, err: err}
	}
	out.WasPaused = was
	wasStopped, err := daemonstate.ClearStop(root)
	if err != nil {
		return out, &pauseError{stage: stageStopMarker, err: err}
	}
	out.WasStopped = wasStopped
	if was {
		if err := logPauseDecision(root, now, "resume", "resumed"); err != nil {
			return out, &pauseError{stage: stageLogResume, err: err}
		}
	}
	if wasStopped {
		chosen := "cleared the stop"
		if out.StopTask != "" {
			chosen += " after " + out.StopTask
		}
		if err := logPauseDecision(root, now, "stop_cleared", chosen); err != nil {
			return out, &pauseError{stage: stageLogStopCleared, err: err}
		}
	}
	return out, nil
}

// runPause implements `afk pause` and returns the exit code.
func runPause(args []string, stdout, stderr io.Writer, env pauseEnv) int {
	root, code, ok := targetFor("afk pause", pauseUsageLine, args, printPauseUsage, stderr, env)
	if !ok {
		return code
	}
	if _, err := pauseProject(root, env.now()); err != nil {
		var pe *pauseError
		if errors.As(err, &pe) && pe.stage == stageLogPause {
			fmt.Fprintf(stderr, "afk pause: the project is paused, but the decision log could not be written: %v\n", pe.err)
			return 1
		}
		fmt.Fprintf(stderr, "afk pause: %v. Check that you can write to %s.\n", err, filepath.Dir(daemonstate.PausePath(root)))
		return 1
	}
	fmt.Fprintf(stdout, "Paused %s. No new task will start; a task that is running will finish. Run \"afk resume\" to start again.\n", root)
	return 0
}

// runResume implements `afk resume` and returns the exit code. Resuming a
// project that was not paused is a normal answer, not a failure, and writes no
// log line because nothing was decided.
func runResume(args []string, stdout, stderr io.Writer, env pauseEnv) int {
	root, code, ok := targetFor("afk resume", resumeUsageLine, args, printResumeUsage, stderr, env)
	if !ok {
		return code
	}
	out, err := resumeProject(root, env.now())
	if err != nil {
		var pe *pauseError
		if errors.As(err, &pe) {
			switch pe.stage {
			case stageStopMarker:
				fmt.Fprintf(stderr, "afk resume: %v. Check that you can write to %s.\n", pe.err, filepath.Dir(daemonstate.StopPath(root)))
			case stageLogResume:
				fmt.Fprintf(stderr, "afk resume: the project is resumed, but the decision log could not be written: %v\n", pe.err)
			case stageLogStopCleared:
				fmt.Fprintf(stderr, "afk resume: the stopped line is cleared, but the decision log could not be written: %v\n", pe.err)
			default:
				fmt.Fprintf(stderr, "afk resume: %v. Check that you can write to %s.\n", pe.err, filepath.Dir(daemonstate.PausePath(root)))
			}
			return 1
		}
		fmt.Fprintf(stderr, "afk resume: %v\n", err)
		return 1
	}
	if !out.WasPaused && !out.WasStopped {
		fmt.Fprintf(stdout, "%s was not paused; nothing changed.\n", root)
		return 0
	}
	if out.WasPaused {
		fmt.Fprintf(stdout, "Resumed %s. The daemon may start new tasks again.\n", root)
	}
	if out.WasStopped {
		fmt.Fprintf(stdout, "%s\n", stoppedMessage(root, out.StopTask, out.StopReason))
	}
	return 0
}

// stoppedMessage words the "line cleared" sentence. A marker that could not be
// parsed has no task or reason; the sentence then says only what is known.
func stoppedMessage(root, task, reason string) string {
	what := "was stopped"
	if task != "" {
		what += " after " + task
	}
	if reason != "" {
		what += ": " + reason
	}
	what = strings.TrimSuffix(what, ".")
	return fmt.Sprintf("The line for %s %s. It is cleared; the daemon carries on at its next pass.", root, what)
}

// logPauseDecision records an owner's pause or resume in the project's
// decision log. chosen_by is always owner: only a person runs these commands.
func logPauseDecision(root string, now time.Time, kind, chosen string) error {
	return decisionlog.Append(decisionlog.Path(root), decisionlog.Entry{
		At:       now.UTC().Format(time.RFC3339),
		Kind:     kind,
		Chosen:   chosen,
		ChosenBy: decisionlog.ChosenByOwner,
	})
}
