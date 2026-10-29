package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/prompt"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
)

// cmdNew implements `afk new`: create an isolated afk session (branch +
// worktree + seeded status.json + launched conductor).
func cmdNew(args []string) {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	var (
		base        string
		description string
		dryRun      bool
		noGitSpice  bool
		noDash      bool
		scratch     bool
		autonomous  bool
		providerFlg string
	)
	fs.StringVar(&base, "base", "", "Base branch to stack/branch on (skips the picker)")
	fs.StringVar(&description, "description", "", "Free-text initial task context for the conductor (stored in .afk/<name>/description.md)")
	fs.BoolVar(&dryRun, "dry-run", false, "Print the full action list without making any changes")
	fs.BoolVar(&noGitSpice, "no-git-spice", false, "Force plain-git behavior even if git-spice is detected")
	// No backquotes in this usage string: flag.UnquoteUsage treats the first
	// backquoted span as the flag's operand name, which on a bool flag renders as
	// nonsense ("-no-dash afk dash --open <name>").
	fs.BoolVar(&noDash, "no-dash", false,
		"Do not open the dashboard afterwards; print how to reach the session instead")
	fs.BoolVar(&scratch, "scratch", false,
		"Create a one-off session: no branch, no worktree, no plan — a bare provider CLI (claude by default) in the current repo. "+
			"The name may be omitted (one is allocated); --base and --no-git-spice are rejected with it.")
	fs.BoolVar(&autonomous, "autonomous", false,
		"run the session in afk autonomous mode (no user stops; requires a afk plugin with autonomy support)")
	fs.StringVar(&providerFlg, "provider", "",
		"CLI the session host runs: "+strings.Join(provider.Names(), "|")+" (default claude); codex requires --autonomous or --scratch")
	project := addProjectFlag(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `afk new — create a new afk session

Usage:
  afk new [options] <name>

The <name> is strictly validated kebab-case (^[a-z0-9][a-z0-9-]*$) and becomes
the branch, worktree directory, and plan slug. If omitted on an interactive
terminal (and --dry-run is not set), you will be prompted for it.

On a terminal with no --description, you will also be prompted for optional
initial task context. The description seeds the conductor's planning phase.
Interactive afk new on a terminal now asks up to three questions: name,
description, and base branch.

On a terminal this ends by opening the dashboard with the new session live in an
embedded pane, so you land in it directly. Pass --no-dash (or redirect stdio, as a
script would) to get a one-line pointer instead.

By default, the new session is created in the current repository (cwd). Pass
--project PATH to create it in a different repository instead.

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	tty := isTerminal(os.Stdin)
	stdoutTTY := isTerminal(os.Stdout)
	promptForName, allocateScratchName, usageErr, errMsg := resolveNewArgs(fs.NArg(), tty, dryRun, scratch, base != "", noGitSpice, autonomous)
	if usageErr {
		if errMsg == "" {
			errMsg = "afk new: exactly one session <name> is required"
		}
		fmt.Fprintln(os.Stderr, errMsg)
		fs.Usage()
		os.Exit(2)
	}

	if provErr := resolveProviderArg(providerFlg, scratch, autonomous); provErr != "" {
		fmt.Fprintln(os.Stderr, provErr)
		fs.Usage()
		os.Exit(2)
	}

	// Name and description resolution keep their pre-scratch position, ahead of
	// --project resolution: this is the ordering every existing (non-scratch)
	// flag combination has always run in, and neither step needs repo. The one
	// exception is allocateScratchName, which genuinely cannot run yet — the
	// allocated name is chosen by scanning {repo}/.afk/, so repo has to be
	// resolved first. That single case is deferred below; nothing else moves.
	var name string
	switch {
	case promptForName:
		n, err := prompt.SessionName(session.ValidateName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "afk new: %v\n", err)
			os.Exit(1)
		}
		name = n
	case allocateScratchName:
		// Left unresolved until repo is known — see below.
	default:
		name = fs.Arg(0)
	}

	if shouldPromptForDescription(description != "", tty, dryRun, scratch) {
		d, err := prompt.Description("Optional task description (seeds planning)")
		if err != nil {
			fmt.Fprintf(os.Stderr, "afk new: %v\n", err)
			os.Exit(1)
		}
		description = d
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk new: cannot determine working directory: %v\n", err)
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk new: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	// The one string -> rawProjectFlag conversion in this command, at the flag
	// boundary. Everything downstream carries the distinction in the type.
	raw := rawProjectFlag(*project)
	repo, err := resolveAndValidateProject("new", raw, cwd, home)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if allocateScratchName {
		existing, err := session.ListSessionDirs(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "afk new: %v\n", err)
			os.Exit(1)
		}
		name = session.NextScratchName(existing)
	}

	cfg := session.Config{
		Name:        name,
		Description: description,
		RepoDir:     repo,
		Base:        base,
		DryRun:      dryRun,
		NoGitSpice:  noGitSpice,
		Scratch:     scratch,
		Autonomous:  autonomous,
		Provider:    providerFlg,
	}

	deps := session.DefaultDeps()
	// Wire an interactive base picker only when attached to a terminal. Absent
	// a TTY (or when --base is supplied) the session package falls back to the
	// non-interactive default (trunk / --base) without prompting.
	if tty {
		deps.PickBase = prompt.PickBase
	}
	// The machine's backend decides where a NEW session runs. Read here, once
	// per start, so a change to config.toml takes effect on the next session.
	deps = withRuntimeSetting(deps, func() (daemonconfig.Config, error) { return daemonconfig.Load(daemonconfig.DefaultDeps()) })

	if err := session.Create(cfg, deps); err != nil {
		fmt.Fprintf(os.Stderr, "afk new: %v\n", err)
		reportStartRefusal(repo, name, err, os.Stderr)
		os.Exit(1)
	}
	if dryRun {
		return
	}
	if !shouldHandOffToDash(tty, stdoutTTY, noDash) {
		fmt.Println(newSessionGuidance(name))
		return
	}
	// The hand-off gets the RAW --project flag value, never the resolved repo:
	// resolveProject falls back to cwd, so its result is non-empty on EVERY
	// invocation, and passing it here appended `--project <cwd>` to a bare
	// `afk new`'s argv — which must stay byte-identical to the pre-flag
	// hand-off (see TestNewHandOffProjectCallSite). That regression is now a
	// COMPILE error, not merely a test failure: runDashOpen takes
	// rawProjectFlag and `repo` is a plain (resolved) string, so
	// `runDashOpen(name, repo)` does not type-check. The child inherits this
	// process's cwd, so an explicit flag value (even a relative or ~-prefixed
	// one) re-resolves to the same repo there.
	if err := runDashOpen(name, raw); err != nil {
		// The session itself was created successfully, so tell the user how to
		// reach it by hand before exiting non-zero for the failed hand-off.
		fmt.Fprintf(os.Stderr, "afk new: could not open the dashboard: %v\n", err)
		fmt.Println(newSessionGuidance(name))
		os.Exit(1)
	}
}

// shouldHandOffToDash decides whether `afk new` ends by giving its terminal to
// `afk dash --open <name>` or by printing plain-text guidance. Pure, so the
// whole matrix is unit-testable.
//
// BOTH stdin and stdout must be real terminals: the dashboard is a Bubble Tea
// full-screen program, and launching one on a pipe fails outright — so a scripted
// or CI `afk new` has to get the text, not the TUI. --no-dash is the explicit
// opt-out, and it is what the dashboard's own background spawn passes so that
// creating a session from the dashboard cannot recursively launch a second one.
func shouldHandOffToDash(stdinTTY, stdoutTTY, noDash bool) bool {
	return !noDash && stdinTTY && stdoutTTY
}

// rawProjectFlag is the --project flag value exactly as the user typed it:
// empty on a bare invocation, unresolved otherwise. It is a distinct type
// rather than a plain string because the resolved repo path is ALSO a string
// and — resolveProject falling back to cwd — is non-empty on every
// invocation; to the compiler the two were interchangeable, and that
// interchangeability produced a real blocker (cmdNew handed the resolved
// repo to the dash hand-off, so every bare `afk new` grew a
// `--project <cwd>` argument). Passing a resolved path where the raw flag
// belongs now fails to type-check. Convert only at a flag boundary, right
// after FlagSet.Parse.
type rawProjectFlag string

// resolveAndValidateProject is the flag-to-repo decision shared by `afk
// new` and `afk clean`: resolve raw against cwd/home, then — only when
// --project was explicitly given — require the resolved path to be an
// existing git repository. It returns the repo to operate on, or the exact
// `afk <cmd>:`-prefixed error the caller prints before exiting non-zero
// (os.Exit stays in the callers, per this repo's pure-decision convention).
// The raw-flag guard lives HERE, in the one function both commands call, so
// the tests that pin it (TestNewResolveAndValidateProject,
// TestCleanProjectFlagValidation) exercise the production guard itself — a
// test-local reconstruction of this guard passed against disabled
// production code twice. A bare invocation's cwd must never be subjected to
// the check: bare `afk new` from a repo subdirectory works today and must
// keep working (the plan's W2.T1 Must-NOT).
func resolveAndValidateProject(cmd string, raw rawProjectFlag, cwd, home string) (string, error) {
	repo, err := resolveProject(string(raw), cwd, home)
	if err != nil {
		return "", fmt.Errorf("afk %s: %v", cmd, err)
	}
	if raw != "" {
		if verr := validateProjectPath(cmd, repo, probeProjectPath(repo)); verr != nil {
			return "", verr
		}
	}
	return repo, nil
}

// dashOpenArgs builds the hand-off argv: ["dash", "--open", name] when project
// is empty, or ["dash", "--open", name, "--project", project] when a project is
// given. Pure, so the flag-before-value order is assertable without launching anything.
func dashOpenArgs(name string, project rawProjectFlag) []string {
	args := []string{"dash", "--open", name}
	if project != "" {
		args = append(args, "--project", string(project))
	}
	return args
}

// runDashOpen replaces this command's remaining work with a FOREGROUND
// `afk dash --open <name>` child that inherits the real terminal on all three
// stdio fds — the dashboard needs the TTY to run at all, and inheriting it (as
// opposed to piping) is what lets the pane render the session here.
//
// Run blocks until the user quits the dashboard, which is the intent: `afk new`
// on a terminal should end with the user inside their new session, not back at a
// shell prompt. Quitting only detaches — the session's host outlives it.
func runDashOpen(name string, project rawProjectFlag) error {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = os.Args[0]
	}
	c := exec.Command(self, dashOpenArgs(name, project)...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// newSessionGuidance is the line printed once the session's detached host is
// running, in the cases where the terminal is NOT handed to the dashboard
// (--no-dash, or a non-terminal stdio): the session is no longer in this
// terminal, so the user has to be told how to reach it. Pure, so the wording is
// assertable.
func newSessionGuidance(name string) string {
	return fmt.Sprintf("session %s created and running — open `afk dash` to attach", name)
}

// scratchBaseConflictMsg is the exact usage error for `--scratch` combined
// with `--base` or `--no-git-spice`. A scratch session never reaches
// resolveBase (session.Build returns before it), so an explicit --base or
// --no-git-spice would silently do nothing — this is caught here, at the
// flag boundary, instead of failing quietly downstream.
const scratchBaseConflictMsg = "afk new: --scratch cannot be combined with --base or --no-git-spice: a scratch session has no branch, so a base is meaningless"

// autonomousScratchConflictMsg is the exact usage error for `--autonomous`
// combined with `--scratch`. A scratch session launches a bare claude with no
// /afk:go conductor, so there is nothing for autonomous mode to attach to;
// accepting the flag would silently do nothing.
const autonomousScratchConflictMsg = "afk new: --autonomous cannot be combined with --scratch: a scratch session has no conductor, so autonomous mode has nothing to drive"

// codexInteractiveConflictMsg aliases the provider package's text so the
// rule and its message have one owner; the dashboard form shares it.
const codexInteractiveConflictMsg = provider.CodexInteractiveConflictMsg

// resolveProviderArg is the pure validation for --provider, kept beside
// resolveNewArgs rather than inside it so that function's signature (pinned by
// call-site tests) is untouched. It returns the exact usage error to print, or
// "" when the combination is valid. "" and "claude" are always valid and
// behave identically (Build canonicalizes "claude" to the default).
func resolveProviderArg(name string, scratch, autonomous bool) string {
	if _, err := provider.Lookup(name); err != nil {
		return fmt.Sprintf("afk new: %v", err)
	}
	if err := provider.CheckMode(name, scratch, autonomous); err != nil {
		return err.Error()
	}
	return ""
}

// resolveNewArgs is the pure, testable decision function for `afk new`'s
// argument handling. It never touches I/O so the full matrix can be unit
// tested headlessly.
//
// allocateScratchName is the one output that is itself repo-dependent
// downstream (the caller allocates via session.ListSessionDirs +
// session.NextScratchName, which reads {repo}/.afk/), so cmdNew must defer
// acting on it until AFTER --project is resolved. promptForName and the
// fs.Arg(0) fallback are not: both existed before --scratch and neither one
// needs repo, so cmdNew must keep resolving them where it always did — ahead
// of --project — for every scratch and non-scratch combination alike. This
// function draws that line: it is the ONLY place that decides which of the
// three name sources applies, so cmdNew consumes promptForName /
// allocateScratchName directly instead of re-deriving "scratch &&
// nargs == 0" inline (a prior version did exactly that, which is redundant
// with the case below by construction — nargs == 1 already matched above it
// in this switch — and duplicates the decision in a form that can silently
// drift from it after a future change to this matrix).
//
// Decision matrix:
//   - scratch && (baseSet || noGitSpice) -> usage error, scratchBaseConflictMsg
//     (checked first: independent of nargs/tty/dryRun, since it is a flag
//     misuse rather than a name-resolution problem)
//   - scratch && autonomous -> usage error, autonomousScratchConflictMsg
//     (after the base check so the older message keeps precedence; still
//     ahead of the nargs checks for the same flag-misuse reason)
//   - nargs > 1                       -> usage error, generic message (unchanged)
//   - nargs == 1                      -> proceed with the given name (unchanged;
//     applies to scratch too — a positional name with --scratch is used as-is,
//     still ValidateName-checked by session.Build)
//   - scratch && nargs == 0           -> proceed, no prompt, allocateScratchName
//     true (caller allocates the name via session.NextScratchName once repo is
//     resolved; unlike the non-scratch case below, this is valid on ANY
//     tty/dryRun combination — a one-off session never needs an interactive name)
//   - nargs == 0 && dryRun            -> usage error, regardless of tty
//     (dry-run is promptless by design: the base picker is already suppressed
//     under dry-run at create.go's resolveBase, and the name prompt must be
//     consistent with that)
//   - nargs == 0 && tty && !dryRun    -> prompt for the name
//   - nargs == 0 && !tty              -> usage error (unchanged)
func resolveNewArgs(nargs int, tty bool, dryRun bool, scratch bool, baseSet bool, noGitSpice bool, autonomous bool) (promptForName bool, allocateScratchName bool, usageErr bool, errMsg string) {
	switch {
	case scratch && (baseSet || noGitSpice):
		return false, false, true, scratchBaseConflictMsg
	case scratch && autonomous:
		return false, false, true, autonomousScratchConflictMsg
	case nargs > 1:
		return false, false, true, ""
	case nargs == 1:
		return false, false, false, ""
	case scratch:
		return false, true, false, ""
	case dryRun:
		return false, false, true, ""
	case tty:
		return true, false, false, ""
	default:
		return false, false, true, ""
	}
}

// shouldPromptForDescription decides whether to interactively prompt for a
// description. Pure, so the decision matrix is unit-testable without a TTY.
//
// Decision matrix:
//   - descriptionSet (flag was supplied)  -> no prompt
//   - scratch                             -> no prompt (a one-off session has no
//     conductor and no planning phase for a description to seed)
//   - dryRun                              -> no prompt (dry-run is promptless)
//   - !tty                                -> no prompt (not interactive)
//   - descriptionSet==false && !scratch && tty && !dryRun -> prompt
func shouldPromptForDescription(descriptionSet bool, tty bool, dryRun bool, scratch bool) bool {
	return !descriptionSet && !scratch && tty && !dryRun
}

// projectPathProbe carries the filesystem facts validateProjectPath decides
// on. The stat calls live in probeProjectPath so the decision itself stays
// pure per this repo's convention — while validation embedded os.Exit, the
// exit-non-zero-on-bad-path contract for --project had no test coverage at
// all (see TestValidateProjectPath).
type projectPathProbe struct {
	statErr error
	isDir   bool
	gitErr  error
}

// probeProjectPath is the impure half of --project validation: it gathers
// whether path stats as a directory and whether it has a .git entry. The
// .git Lstat (present in both main checkouts and linked worktrees)
// deliberately replaces a `git rev-parse` spawn — it is the minimal check
// for "a git repo root" and costs one syscall instead of a subprocess.
func probeProjectPath(path string) projectPathProbe {
	var p projectPathProbe
	fi, err := os.Stat(path)
	p.statErr = err
	if err == nil {
		p.isDir = fi.IsDir()
	}
	_, p.gitErr = os.Lstat(filepath.Join(path, ".git"))
	return p
}

// validateProjectPath is the pure decision for an explicit --project path,
// shared by `afk new` and `afk clean` (cmd supplies the prefix so each
// command's failure is attributed to it). It returns the exact error the
// command prints before exiting non-zero, or nil when the path is an
// existing directory with a .git entry. os.Exit stays in the callers, which
// call this only when --project was explicitly given — a bare invocation's
// cwd must never be subjected to this check (see the plan's W2.T1 Must-NOT:
// bare `afk new` from a repo subdirectory works today and must keep working).
func validateProjectPath(cmd, path string, p projectPathProbe) error {
	switch {
	case p.statErr != nil && os.IsNotExist(p.statErr):
		return fmt.Errorf("afk %s: --project %q does not exist", cmd, path)
	case p.statErr != nil:
		return fmt.Errorf("afk %s: cannot stat --project %q: %v", cmd, path, p.statErr)
	case !p.isDir:
		return fmt.Errorf("afk %s: --project %q is not a directory", cmd, path)
	case p.gitErr != nil:
		return fmt.Errorf("afk %s: --project %q is not a git repository", cmd, path)
	default:
		return nil
	}
}

// withRuntimeSetting puts the machine's `[runtime]` setting on d, for a start
// that creates a session. A config.toml that cannot be read at all is carried
// as RuntimeErr and refused like a bad backend word: with the file unreadable
// afk cannot know the owner did not ask for Rex, and starting on its own host
// anyway is the silent fallback the setting forbids. The load happens on the
// caller's goroutine, so a dashboard caller must call this inside its tea.Cmd.
func withRuntimeSetting(d session.Deps, load func() (daemonconfig.Config, error)) session.Deps {
	cfg, err := load()
	if err != nil {
		d.RuntimeErr = fmt.Errorf("afk could not read config.toml, so it cannot tell where this session should run: %w", err)
		return d
	}
	d.Backend = cfg.Runtime.Backend
	d.RuntimeErr = cfg.RuntimeErr
	return d
}

// startRefusedTitle is the title of the blocking item a refused start raises.
const startRefusedTitle = "A new session was not started"

// startRefusalItem is the inbox item for a refused start. The text is the
// refusal's own plain message, so the terminal, the toast and the inbox all
// say the same thing.
func startRefusalItem(name string, err error) inbox.NewItem {
	return inbox.NewItem{
		Session:  name,
		Kind:     "failure",
		Blocking: true,
		Title:    startRefusedTitle,
		Body:     err.Error() + "\n\nNothing was started. Fix this, then start the session again.",
	}
}

// recordStartRefusal adds the refusal's item to the project's inbox, unless
// an open one with the same words is already there: a build loop or a
// repeated `afk new` must not pile up copies of one problem. It returns the
// item and whether this call added it.
func recordStartRefusal(store inbox.Store, name string, err error) (inbox.Item, bool, error) {
	n := startRefusalItem(name, err)
	items, _, lerr := store.List()
	if lerr == nil {
		for _, it := range items {
			if it.Open() && it.Kind == n.Kind && it.Title == n.Title && it.Body == n.Body {
				return it, false, nil
			}
		}
	}
	it, aerr := store.Add(n)
	return it, aerr == nil, aerr
}

// reportStartRefusal raises the inbox item when err is a refused start and
// says so on w. Any other error is left alone. A failure to write the item is
// told, never hidden, but never replaces the refusal already printed.
func reportStartRefusal(root, name string, err error, w io.Writer) {
	var refused *session.StartRefusedError
	if !errors.As(err, &refused) {
		return
	}
	it, _, aerr := recordStartRefusal(inbox.Store{Dir: inbox.Dir(root)}, name, err)
	if aerr != nil {
		fmt.Fprintf(w, "afk new: the inbox item for this could not be written: %v\n", aerr)
		return
	}
	fmt.Fprintf(w, "afk new: this is also in your inbox as item %s.\n", it.ID)
}
