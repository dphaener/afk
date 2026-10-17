package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/dashmeta"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/scan"
	"github.com/dphaener/afk/internal/ui"
	"github.com/dphaener/afk/internal/uistate"
)

// defaultDashInterval is the dashboard's default discovery-refresh cadence,
// shared by `afk dash` and afk new's in-process TTY handoff (cmdNew in
// new.go), which does not expose its own --interval flag.
const defaultDashInterval = 2 * time.Second

// selectionWriteMu guards every read-modify-write this process makes to the
// persistent state file — rememberSelection AND recordProjectOpen, which write
// different fields of the same file and so must share one lock rather than
// hold two. The dashboard's tick flush (Cmd goroutine) and its quit flush
// (inline on the UI goroutine) both call RememberSelection and
// RecordProjectOpen, and each performs read-modify-write on that same file.
// Without synchronization, concurrent calls can lose one another's writes as
// follows:
// tick flush reads state{a:1, b:2}, adds c:3, writes {a:1, b:2, c:3}.
// quit flush (started before tick's write) reads state{a:1, b:2}, adds d:4,
// writes {a:1, b:2, d:4} — the tick's c:3 is lost.
//
// The lock covers only calls this process makes while it is still running.
// A tick flush dispatched just before quit runs on a goroutine bubbletea's
// Program never waits on before returning (charm.land/bubbletea/v2 tea.go:727,
// "we'll have to leak the goroutine"), so process exit can cut that write off
// mid-flight regardless of this mutex, at worst leaving an orphaned
// .state.json.tmp.* file. Best-effort, and self-heals: the next Remember call
// picks a fresh temp name and the orphan is never read back.
var selectionWriteMu sync.Mutex

// cmdDash implements `afk dash`: a Bubble Tea dashboard over the afk
// sessions discovered in the current repo. It re-runs session discovery on a
// --interval ticker (default 2s, file reads only) and never writes to any
// .afk file. The keys are listed in dashUsage; the focus model they describe
// is documented on paneFocus and rightView in internal/ui/model.go.
//
// --open <name> opens one named session's pane as soon as discovery reports it.
// That is how `afk new` hands its terminal over: the created session's host
// needs a moment to publish host.json, and the dial's own retry window absorbs
// the rest of that startup race.
func cmdDash(args []string) {
	fs := flag.NewFlagSet("dash", flag.ExitOnError)
	var (
		interval time.Duration
		open     string
	)
	proj := addProjectFlag(fs)
	fs.DurationVar(&interval, "interval", defaultDashInterval,
		"Refresh interval for re-running session discovery (e.g. 2s, 500ms)")
	fs.StringVar(&open, "open", "",
		"Open the named session's pane on start (restarts its host first if it is stopped)")
	fs.Usage = func() { dashUsage(os.Stderr, fs) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if interval <= 0 {
		interval = defaultDashInterval
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk dash: cannot determine working directory: %v\n", err)
		os.Exit(1)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk dash: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	repo, err := resolveProject(*proj, cwd, home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk dash: %v\n", err)
		os.Exit(1)
	}

	// The config file is deliberately NOT loaded or validated here. Exiting 1
	// on a malformed file at launch made the empty state's whole reason to
	// exist unreachable: renderEmptyState is where the user is told WHICH file
	// to fix (m.configPath rides projectScanMsg even when the settings load
	// errors — see loadDashConfig below), and a dashboard that refuses to start can
	// never show it. The first scan tick reads the config through
	// ui.Deps.LoadConfig moments after startup, so a launch-time parse error
	// degrades to exactly the state a config that goes bad under a RUNNING
	// dashboard produces. A --project path that is not a repo is a different
	// case and stays a hard exit in `new`/`clean`, which are specified to
	// reject it; `dash` instead starts on the empty state (see docs/dashboard.md,
	// "Launching outside a repo").
	tmux := os.Getenv("TMUX")
	loc := dashLocation{
		gitCommonDir: resolveGitCommonDirIn(repo),
		tmux:         tmux,
		paneID:       os.Getenv("TMUX_PANE"),
		clientTTY:    resolveTmuxClientTTY(tmux),
		startedAt:    time.Now().Format(time.RFC3339),
	}

	if os.Getenv(dashDebugEnv) != "" {
		fmt.Fprintln(os.Stderr, dashLocationDiagnostic(loc))
	}

	if err := runDash(repo, interval, open, loc); err != nil {
		fmt.Fprintf(os.Stderr, "afk dash: %v\n", err)
		os.Exit(1)
	}
}

// dashUsage presents the dashboard's normal-mode input contract. Keeping it
// separate from cmdDash makes the user-facing key semantics directly testable
// without starting the interactive program.
func dashUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(w, `afk dash — launch the session dashboard

Usage:
  afk dash [options]

Keys:
  ↑/k, ↓/j   move selection, preview running session in agent pane
  pgup/pgdn  page through the list; home/end jump to the first or last session
  enter      focus the right side (connects a running session in agent view)
  tab        next tab on the right side (shift+tab goes back)
  esc        back to the list from the right side; clears the filter on the list
  ctrl+c     quit from the list and the overview (in the agent pane it goes to the agent)

Actions: press ctrl+] (the default leader key), then the letter. The actions dialog
lists them too. Set leader = "ctrl+o" (or another key) in config.toml's [dash]
table to change the leader; it is read when the dashboard starts.
  ctrl+] n    create a session (in-TUI form)
  ctrl+] o    create a one-off session
  ctrl+] p    open the project switcher (filterable list of discovered projects)
  ctrl+] c    confirm clean in the dashboard (runs in-process)
  ctrl+] r    restart a stopped session; confirm to restart a wedged one
  ctrl+] l    edit the session's label
  ctrl+] /    filter sessions by name
  ctrl+] x    clear the filter
  ctrl+] b    show the Build tab: the master plan's tasks and their status (needs a master plan)
  ctrl+] i    open the inbox: read an item, answer it by typing or by number (enter sends)
  ctrl+] P    pause the build
  ctrl+] U    resume the build
  ctrl+] f    focus the right side
  ctrl+] t    switch the right side between agent and overview
  ctrl+] \    close the pane from the list; back to the list from the right side
  ctrl+] g    hand the mouse to the terminal (native selection), again to return it
  ctrl+] w    resize the panes
  ctrl+] ?    show the full key help
  ctrl+] q    quit

Options:
`)
	fs.SetOutput(w)
	fs.PrintDefaults()
}

// dashDebugEnv gates the startup diagnostic. It is PERMANENT, not scaffolding.
// The click-to-focus regression this exists for was a stale `~/bin/afk` whose
// build predated the feature: every unit test passed, the dashboard rendered
// normally, and the only symptom was an absent file — a bug with no observable
// surface anywhere. `AFK_DEBUG=1 afk dash` prints, in one line, both the four
// values `WriteEntry` gates on and the exact path they resolve to, so the next
// reader of an empty afk-dash/ can distinguish "the code is wrong", "tmux is
// not there", and "this binary does not have the feature" without a rebuild.
const dashDebugEnv = "AFK_DEBUG"

// dashLocation is everything `afk dash` must learn from its environment to
// record where it can be reached. It is a struct rather than five parameters so
// gatherers, the diagnostic, and the write cannot fall out of positional
// agreement, and so a test can construct one directly.
type dashLocation struct {
	gitCommonDir string
	tmux         string
	paneID       string
	clientTTY    string
	startedAt    string
}

// recordable reports whether loc can produce a dashmeta entry, mirroring
// WriteEntry's three gates exactly.
//
// It duplicates those gates ON PURPOSE, and the duplication is the point:
// WriteEntry returns nil for "wrote nothing deliberately" and for "wrote it", so
// its error is useless as a did-it-happen signal. Every caller that wants to
// report or assert the outcome needs the predicate separately. Pure, so the
// matrix is table-testable.
func (loc dashLocation) recordable() bool {
	return loc.tmux != "" && loc.paneID != "" && filepath.IsAbs(loc.gitCommonDir)
}

// dashLocationDiagnostic renders loc as one AFK_DEBUG line: every gated value,
// whether the gates pass, and the absolute path the entry lands at.
//
// The PATH is included rather than just the inputs because "wrote nothing" and
// "wrote somewhere the reader does not scan" look identical from an empty
// directory listing, and they were the two live hypotheses during the
// regression. Pure, so the wording is assertable.
func dashLocationDiagnostic(loc dashLocation) string {
	path := dashmeta.EntryPath(loc.gitCommonDir)
	if path == "" {
		path = "(none)"
	}
	return fmt.Sprintf(
		"afk dash: git-common-dir=%q tmux=%q pane=%q client-tty=%q recordable=%t entry=%s",
		loc.gitCommonDir, loc.tmux, loc.paneID, loc.clientTTY, loc.recordable(), path)
}

// runDash starts the dashboard Bubble Tea program in-process for repo,
// re-running discovery every interval. When openSession is non-empty, the
// dashboard opens that session's pane automatically once its first discovery
// load and first resize have both arrived (see Model.maybeOpenInitialPane).
// This is the shared dash-startup path: `afk dash` calls it directly, and
// afk new's TTY handoff (cmdNew in new.go) calls it in-process after a
// successful create — same binary, no exec, no recursion.
//
// gitCommonDir does double duty: the dashboard records its pane there for
// `afk notify` to target, and it watches the same directory for the focus
// requests a clicked notification writes back (internal/ui's focusrequest.go).
// One rendezvous point, both directions.
//
// It is also where the old-settings-file notice is decided, because this is
// the one function every start of the dashboard passes through, and the
// notice is owed once per start.
func runDash(repo string, interval time.Duration, openSession string, loc dashLocation) error {
	return runDashAnnouncing(repo, interval, openSession, loc, os.Stderr, realOldSettingsSource(), runDashProgram)
}

// runDashAnnouncing is runDash with its three impure parts injected: where the
// notice is printed, what the disk says about the old settings file, and the
// blocking program.
//
// The notice is printed BEFORE the program starts, on purpose. The dashboard
// takes over the whole screen, so anything written while it runs is lost, and
// anything written after it quits is seen only then. Written first, the text
// sits in the terminal's own scrollback, where the lines to copy are still
// there after quitting. The short form goes to the program so the dashboard
// can say, while it is up, that those lines exist.
//
// The check runs exactly once here and its answer is handed down as a plain
// string, so nothing further in can show the notice a second time.
func runDashAnnouncing(repo string, interval time.Duration, openSession string, loc dashLocation,
	stderr io.Writer, src oldSettingsSource,
	program func(repo string, interval time.Duration, openSession string, loc dashLocation, notice string) error) error {
	notice := announceOldSettingsFile(stderr, src)
	return runDashWith(repo, interval, openSession, loc,
		func(repo string, interval time.Duration, openSession string, loc dashLocation) error {
			return program(repo, interval, openSession, loc, notice)
		})
}

// runDashProgram is the real dashboard: a Bubble Tea program over the whole
// terminal, blocking until the user quits.
//
// It takes the whole dashLocation rather than just the git common dir because
// the dashmeta entry is written by the Model now (see dashDeps): the tmux values
// that go INTO that entry have to reach ui.Deps, and threading them one at a
// time through this signature is how they would fall out of agreement.
//
// notice is the short form of the old-settings-file notice, empty when there
// is none; runDashAnnouncing decided it.
func runDashProgram(repo string, interval time.Duration, openSession string, loc dashLocation, notice string) error {
	projects, activeProject := dashInitialProjects(repo)
	model := ui.New(repo, interval, dashDepsWithNotice(openSession, loc, projects, activeProject, notice))
	// The alt screen and mouse reporting are the Model's to declare now: under
	// Bubble Tea v2 they are fields on the tea.View that ui.Model.View returns
	// (internal/ui/views.go), not tea.ProgramOptions applied here.
	// MouseModeCellMotion asks the terminal for ?1002 + ?1006: wheel notches,
	// button presses, releases, and drag motion — but no report for every cell a
	// button-less pointer crosses, which is the difference between it and
	// MouseModeAllMotion. The dashboard translates those events into the hosted
	// session's coordinate space and forwards them (see the tea.MouseMsg case in
	// internal/ui/model.go), which is what makes scrolling inside the pane reach
	// the session instead of the host terminal's scrollback.
	//
	// The cost is that the host terminal no longer sees click-drag while the
	// dashboard runs, so its own text selection is unavailable — that is what
	// ctrl-g exists for: it flips the Model's mouseCapture flag, the next frame
	// declares MouseModeNone, and the mouse goes back to the terminal without
	// ending the session.
	_, err := newDashProgram(model).Run()
	return err
}

// newDashProgram builds the dashboard's tea.Program. It carries no option list
// any more: the two terminal modes the dashboard needs used to be
// tea.ProgramOptions applied here and are now declared by ui.Model.View, so what
// this constructor still pins is that runDashProgram and the tests build their
// program identically.
//
// The guarantee the deleted option list carried — that the terminal is actually
// ASKED to report mouse events, which no test injecting tea.MouseMsg into Update
// can see — moved with it, into
// TestDashProgramOptionsAskTheTerminalForMouseReports, which now runs a real
// program over the real Model's declared tea.View.
//
// extra is how a test gets a runnable program: headless input, a buffer for
// output, no signal handler. The real dash passes none.
//
// The one line this leaves unpinned is runDashProgram's call above, which cannot
// be tested at all — it blocks on the user's terminal until they quit.
func newDashProgram(model tea.Model, extra ...tea.ProgramOption) *tea.Program {
	return tea.NewProgram(model, extra...)
}

// runDashWith is runDash with the blocking program injected, and it exists for
// exactly one reason: to make the dashmeta entry's lifecycle TESTABLE.
//
// The click-to-focus regression was an absent afk-dash/<pid>.json while every
// unit test passed, because WriteEntry was tested in isolation and nothing tested
// that the dash startup path CALLS it with usable values. That gap cannot be
// closed against the real runDash — it runs a full-screen Bubble Tea program that
// blocks until a human quits — so the one line that must be asserted was
// unreachable. With the program as a parameter, a test passes a stub that returns
// immediately and inspects afk-dash/ WHILE the dashboard is notionally up.
//
// The WRITE no longer happens here: the Model owns it (internal/ui's
// writeDashEntryCmd), because the Model is the only thing that knows which
// projects it is watching and W5.T3 writes one entry per project. What stays is
// the removal, and it stays deliberately — belt and suspenders, not a leftover.
// This defer fires even when the program returns an error, whereas the Model's
// removal rides requestQuit and therefore only runs on a CLEAN quit. Without it,
// any abnormal exit leaves an entry that outlives its dashboard and keeps aiming
// clicks at a pane with nothing in it. Both paths run in the ordinary case, so
// dashmeta.RemoveEntry's already-absent-is-success behavior is load-bearing.
func runDashWith(repo string, interval time.Duration, openSession string, loc dashLocation,
	run func(repo string, interval time.Duration, openSession string, loc dashLocation) error) error {
	// The outcome is swallowed: a dashboard that cannot tidy up its own entry is
	// fully functional, and the stale entry it leaves is self-healed by
	// dashmeta.ReadEntry's dead-pid removal on the next `afk notify`.
	defer func() { _ = dashmeta.RemoveEntry(loc.gitCommonDir) }()

	return run(repo, interval, openSession, loc)
}

// gitCommonDirTimeout bounds the `git rev-parse` call below. It is the only
// git invocation on `afk notify`'s critical path, which runs on every Claude
// Code hook, and the measured whole-command budget there is ~20ms — so a git
// that never returns (an NFS-mounted repo, a filesystem stall) would hang a hook
// rather than a background job. 2s is far beyond any healthy answer while still
// being a bound; the failure mode on expiry is an empty string, identical to
// running outside a repo, which every caller already handles.
const gitCommonDirTimeout = 2 * time.Second

// resolveGitCommonDir returns the repo's git common dir for the current working
// directory, or "" when it cannot be determined.
func resolveGitCommonDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return resolveGitCommonDirIn(dir)
}

// resolveGitCommonDirIn returns the git common dir as seen from dir.
//
// dir is passed EXPLICITLY as cmd.Dir rather than relying on the inherited
// working directory. The answer is entirely a function of where git runs, and an
// ambient-cwd dependency is invisible at the call site — it makes the dashboard's
// rendezvous point depend on something no caller states, which is unreviewable
// even when it happens to be right (`afk new`'s handoff re-execs
// `afk dash --open` as a child, so the value crosses a process boundary here).
//
// --path-format=absolute is load-bearing: the bare --git-common-dir form can
// return a RELATIVE path, and every dashmeta writer silently no-ops on one, so
// dropping the flag would turn click-to-focus off with no error anywhere.
func resolveGitCommonDirIn(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommonDirTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveCwdRepoRoot returns dir's repo root, symlink-resolved, or "" when dir
// is not inside a git repo (or the lookup fails for any other reason).
//
// Both steps are load-bearing. `git rev-parse --show-toplevel` is what turns a
// launch from a repo SUBDIRECTORY into the repo root — an everyday case that
// must not produce a phantom second project — and EvalSymlinks matches
// internal/scan's own resolution of every path it returns (Project.Path is
// contractually a resolved absolute repo root), which on macOS matters even
// for something as ordinary as /tmp resolving to /private/tmp. Skipping either
// step means the string-equality dedupe in pickActiveProject can miss a repo
// the scan already found, producing a duplicate switcher row for it.
func resolveCwdRepoRoot(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommonDirTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	top := strings.TrimSpace(string(out))
	resolved, err := filepath.EvalSymlinks(top)
	if err != nil {
		return ""
	}
	return resolved
}

// pickActiveProject decides which entry of projects should start selected,
// given cwdRepo — the caller's already-normalized cwd repo root (see
// resolveCwdRepoRoot), or "" when cwd is not inside a git repo. It is pure:
// the git/filesystem work that produces cwdRepo happens in the caller, so the
// whole decision matrix is table-testable without a git repo of its own.
//
//   - cwdRepo matches an existing entry (by exact path — both sides are already
//     resolved, so string equality is enough): that entry's index, list unchanged.
//   - cwdRepo is non-empty and matches nothing: appended to the end with
//     Pinned: true and selected — the ad-hoc entry for a repo no search root
//     covers (D6). DisplayNames is recomputed over every path, not just the new
//     one, so the addition can't leave an existing entry's label stale if it
//     creates a basename collision.
//   - cwdRepo is empty and projects is non-empty: index 0 (D7 — with no cwd
//     repo, the first project is active).
//   - cwdRepo is empty and projects is empty: -1. The list is returned exactly
//     as given rather than fabricating an entry, so the caller can pass it
//     through to the dashboard's empty state unchanged.
func pickActiveProject(projects []project.Project, cwdRepo string) ([]project.Project, int) {
	if cwdRepo == "" {
		if len(projects) == 0 {
			return projects, -1
		}
		return projects, 0
	}

	for i, p := range projects {
		if p.Path == cwdRepo {
			return projects, i
		}
	}

	paths := make([]string, 0, len(projects)+1)
	for _, p := range projects {
		paths = append(paths, p.Path)
	}
	paths = append(paths, cwdRepo)
	names := project.DisplayNames(paths)

	updated := make([]project.Project, len(projects)+1)
	for i, p := range projects {
		p.Name = names[i]
		updated[i] = p
	}
	updated[len(projects)] = project.Project{Path: cwdRepo, Name: names[len(names)-1], Pinned: true}
	return updated, len(projects)
}

// dashInitialProjects computes the project list and active index a dashboard
// starting in repo hands to ui.New. The startup path never scans configured
// search roots (a slow or unreachable one must not delay the first frame — see
// cmdDash's comment on not loading the config file), so the only candidate here is the
// cwd-derived project itself; W3.T1's scan ticker is what grows this into the
// full list a moment after startup.
func dashInitialProjects(repo string) ([]project.Project, int) {
	return pickActiveProject(nil, resolveCwdRepoRoot(repo))
}

// loadSelectionsWith is the production implementation of ui.Deps.LoadSelections.
// It wraps uistate.Load and returns the Selected map from the loaded state.
// Errors are swallowed and an empty non-nil map is returned: a corrupt or
// unreadable state file must never keep the dashboard from starting, and the
// startup path has no diagnostic surface for state-file errors (AFK_DEBUG
// prints only the dash-location diagnostic). The map is guaranteed non-nil
// because uistate.Load ensures it on the success path, and this function
// substitutes an empty non-nil map when Load errors.
func loadSelectionsWith(d uistate.Deps) map[string]string {
	st, err := uistate.Load(d)
	if err != nil {
		return map[string]string{}
	}
	// Load already ensures Selected is non-nil on success (see uistate.Load),
	// so this path always returns a non-nil map.
	return st.Selected
}

// loadSelections is a zero-argument wrapper around loadSelectionsWith that
// passes uistate.DefaultDeps(). The split exists so a test can drive the
// error policy above through fake uistate.Deps without a real state file,
// while ui.Deps.LoadSelections still gets the parameterless func it is typed
// as — there are no package-level var seams in this file to swap instead.
func loadSelections() map[string]string {
	return loadSelectionsWith(uistate.DefaultDeps())
}

// rememberSelection is the production implementation of
// ui.Deps.RememberSelection. The whole uistate.Remember call — read, merge,
// write, rename — happens under selectionWriteMu, whose comment has the
// lost-update sequence this serializes away. Nothing narrower works: the loss
// happens between the read and the rename, so a lock held over only one of
// them would leave the window open.
func rememberSelection(projectPath, sessionName string) error {
	selectionWriteMu.Lock()
	defer selectionWriteMu.Unlock()
	return uistate.Remember(uistate.DefaultDeps(), projectPath, sessionName)
}

// loadProjectOpensWith is the production implementation of
// ui.Deps.LoadProjectOpens. It wraps uistate.Load and returns the Opened map
// from the loaded state, on exactly loadSelectionsWith's error policy: a
// corrupt or unreadable state file must never keep the dashboard from
// starting, so errors are swallowed and an empty non-nil map is returned —
// which degrades the switcher's count tier to alphabetical order rather than
// failing startup. The map is guaranteed non-nil because uistate.Load ensures
// it on the success path, and this function substitutes an empty non-nil map
// when Load errors.
func loadProjectOpensWith(d uistate.Deps) map[string]int {
	st, err := uistate.Load(d)
	if err != nil {
		return map[string]int{}
	}
	// Load already ensures Opened is non-nil on success (see uistate.Load),
	// so this path always returns a non-nil map.
	return st.Opened
}

// loadProjectOpens is a zero-argument wrapper around loadProjectOpensWith
// that passes uistate.DefaultDeps(), on exactly loadSelections's split: a
// test drives the error policy above through fake uistate.Deps without a
// real state file, while ui.Deps.LoadProjectOpens still gets the
// parameterless func it is typed as.
func loadProjectOpens() map[string]int {
	return loadProjectOpensWith(uistate.DefaultDeps())
}

// recordProjectOpen is the production implementation of
// ui.Deps.RecordProjectOpen. The whole uistate.RecordOpen call happens under
// selectionWriteMu — the SAME mutex rememberSelection takes above, not a
// second one — because RecordOpen and Remember read-modify-write-rename the
// same shared state file (see uistate.RecordOpen's and Remember's doc
// comments): an unserialized pair would let whichever renames last silently
// discard the other's merged field, exactly the collision selectionWriteMu's
// comment already documents for two concurrent selection writes.
func recordProjectOpen(projectPath string) error {
	selectionWriteMu.Lock()
	defer selectionWriteMu.Unlock()
	return uistate.RecordOpen(uistate.DefaultDeps(), projectPath)
}

// loadSidebarPctWith is the production implementation of
// ui.Deps.LoadSidebarPct. It wraps uistate.Load and returns the SidebarPct from
// the loaded state. Errors are swallowed and 0 is returned: a corrupt or
// unreadable state file must never keep the dashboard from starting, and 0
// means "use the default split" to the UI, which clamps whatever it is given.
func loadSidebarPctWith(d uistate.Deps) int {
	st, err := uistate.Load(d)
	if err != nil {
		return 0
	}
	return st.SidebarPct
}

// loadSidebarPct is a zero-argument wrapper around loadSidebarPctWith that
// passes uistate.DefaultDeps(), on exactly loadSelections's split, so
// ui.Deps.LoadSidebarPct still gets the parameterless func it is typed as.
func loadSidebarPct() int {
	return loadSidebarPctWith(uistate.DefaultDeps())
}

// rememberSidebarPct is the production implementation of
// ui.Deps.RememberSidebarPct. The whole uistate.RememberSidebarPct call
// happens under selectionWriteMu — the SAME mutex rememberSelection and
// recordProjectOpen take — because it read-modify-write-renames the same
// shared state file, and an unserialized writer would let whichever renames
// last discard the other's merged field.
func rememberSidebarPct(pct int) error {
	selectionWriteMu.Lock()
	defer selectionWriteMu.Unlock()
	return uistate.RememberSidebarPct(uistate.DefaultDeps(), pct)
}

// dashDeps builds the ui.Deps the real dashboard runs with, split out of
// runDashProgram so the exact values `afk dash` wires — Scan and
// LoadConfig above all — are reachable from a test without going through a
// blocking tea.Program.Run. It is a pure assembly step: everything it needs
// is already a parameter or one of scanProjects/loadDashConfig below.
//
// loc arrives whole and leaves as four plain values plus GitCommonDir, because
// the dashmeta entry is written by the Model rather than by the startup path.
// The GATE on that write is not threaded with them: it stays in this package as
// recordable(), which the AFK_DEBUG diagnostic reports, and dashmeta.WriteEntry
// applies the same three checks itself at the far end. So a location that is not
// recordable still travels, and still writes nothing — one decision, stated
// twice on purpose (see recordable's own comment), never carried by a caller.
func dashDeps(openSession string, loc dashLocation, projects []project.Project, activeProject int) ui.Deps {
	return ui.Deps{
		InitialSessionName: openSession,
		GitCommonDir:       loc.gitCommonDir,
		Tmux:               loc.tmux,
		PaneID:             loc.paneID,
		ClientTTY:          loc.clientTTY,
		StartedAt:          loc.startedAt,
		Projects:           projects,
		ActiveProjectIndex: activeProject,
		Scan:               scanProjects,
		LoadConfig:         loadDashConfig,
		Leader:             startupLeader(),
		LoadSelections:     loadSelections,
		RememberSelection:  rememberSelection,
		LoadProjectOpens:   loadProjectOpens,
		RecordProjectOpen:  recordProjectOpen,
		LoadSidebarPct:     loadSidebarPct,
		RememberSidebarPct: rememberSidebarPct,
		// The build seams have no default in internal/ui (they read and
		// write a real project), so this is the only place they are wired.
		// Left out, the dashboard runs with no build data and nothing says so,
		// and P and U say pause and resume are not available. PauseBuild and
		// ResumeBuild call the same functions as `afk pause` and `afk resume`.
		LoadBuild: dashLoadBuild,
		// LoadPlanStatus has no default either: left out, the Build tab shows
		// no plan line and the check-the-plan action says it cannot read the plan.
		LoadPlanStatus: dashLoadPlanStatus,
		// ApprovePlan commits and pushes the plan, so the dashboard calls it
		// only after the owner's yes in the confirm, inside a command.
		ApprovePlan: dashApprovePlan,
		AnswerInbox: dashAnswerInbox,
		// AddProject and RemoveProject edit config.toml through the same entry
		// points as `afk project`; without them the + and - rows say the
		// dashboard was started without them.
		AddProject:    dashAddProject,
		RemoveProject: dashRemoveProject,
		PauseBuild:    dashPauseBuild,
		ResumeBuild:   dashResumeBuild,
		// SetTaskStatus has no default in internal/ui (it writes a real
		// project's ledger). It calls the entry point `afk ledger set` prints
		// from, so the dashboard and the command cannot disagree
		// (docs/PLAN.md §2, T7.11 row); without it S says it cannot change the ledger.
		SetTaskStatus: dashSetTaskStatus,
		// LoadTaskTokens has no default in internal/ui. It calls the entry
		// point `afk tokens <task>` prints from and reads only the token
		// ledger (never a transcript, never a write), so the Build tab's
		// total and the T overlay match the command (T7.12); without it T
		// says it cannot read the token ledger.
		LoadTaskTokens: dashLoadTaskTokens,
		// RefreshUsage has no default in internal/ui (it probes the agent's
		// command-line tool and writes usage.json). It calls the entry point
		// `afk usage --refresh` works through, so the leader then R and the
		// command cannot disagree (T7.13); without it R says it cannot read usage.
		RefreshUsage: dashRefreshUsage,
		// LoadListed has no default in internal/ui either (it reads the
		// machine's config.toml), so without this line the switcher would
		// silently never show the projects `afk project add` listed. The read
		// itself happens in the model's command, never before the first frame.
		LoadListed: dashLoadListed,
		// LoadAllProjects has no default in internal/ui either; without this
		// line "all projects" in the leader dialog says the dashboard was
		// started without the reader. The start-up project's main checkout is
		// the row the screen adds when that project is not listed.
		LoadAllProjects: dashLoadAllProjects(launchMainCheckout(projects, resolveGitCommonDirIn, filepath.EvalSymlinks)),
		// StartPlan has no default in internal/ui (it starts a real session), so
		// this is the only place the three plan actions are wired. Nor do the
		// GitHub seams (they run gh and can make a repository): without
		// GitHubOffer and CreateGitHubRepo a folder made by name would go
		// straight to planning and no question would ever be asked.
		StartPlan:          dashStartPlan,
		PlanFolder:         dashPlanFolder,
		CreatePlanFolder:   dashCreatePlanFolder,
		GitHubOffer:        dashGitHubOffer,
		CreateGitHubRepo:   dashCreateGitHubRepo,
		LaunchMainCheckout: launchMainCheckout(projects, resolveGitCommonDirIn, filepath.EvalSymlinks),
	}
}

// dashDepsWithNotice is dashDeps plus the start-up notice. It is its own
// function, not a line inside runDashProgram, because runDashProgram cannot be
// tested (it blocks on the terminal), and a notice that was decided but never
// reached the Model would fail silently: no toast, no error.
func dashDepsWithNotice(openSession string, loc dashLocation, projects []project.Project, activeProject int, notice string) ui.Deps {
	deps := dashDeps(openSession, loc, projects, activeProject)
	deps.StartupNotice = notice
	return deps
}

// scanProjects is the production implementation of ui.Deps.Scan. maxDepth is
// a parameter rather than something this closure captures because dashDeps
// runs before the settings are ever loaded — see the doc comment on ui.Deps.Scan for
// why a single-argument seam here would be a dead end.
func scanProjects(roots []string, maxDepth int) []project.Project {
	d := scan.DefaultDeps()
	d.MaxDepth = maxDepth
	return scan.Scan(roots, d)
}

// loadDashConfig is the production implementation of ui.Deps.LoadConfig: the
// dashboard's folder-search settings, read from the `[dash]` table of the
// machine's config.toml.
//
// daemonconfig.Path is resolved as its own call, independent of
// daemonconfig.Load's outcome, because the empty state still needs to name
// the file even when that file failed to parse (see ui.Deps.LoadConfig's doc
// comment) — a wrapper that took the path only from a successful load would
// lose it on exactly the error path that most needs it.
//
// There are two ways this fails, and they are kept apart on purpose. A file
// that cannot be read or is not valid TOML fails Load itself. A bad `[dash]`
// value does not: Load carries it as Config.DashErr so the daemon and every
// other command sharing that load never fail over a dashboard setting. This
// wrapper is the one reader that turns DashErr back into an error, because
// the folder search is the one thing those settings drive. Its message
// already starts with the file and the key, so the dashboard's scan-error
// toast shows it as it is.
func loadDashConfig() (ui.SearchSettings, string, error) {
	d := daemonconfig.DefaultDeps()
	path := daemonconfig.Path(d)
	cfg, err := daemonconfig.Load(d)
	if err != nil {
		return ui.SearchSettings{}, path, err
	}
	if cfg.DashErr != nil {
		return ui.SearchSettings{}, path, cfg.DashErr
	}
	return ui.SearchSettings{
		Roots:    cfg.Dash.SearchRoots,
		Interval: cfg.Dash.ScanInterval,
		MaxDepth: cfg.Dash.MaxDepth,
	}, path, nil
}

// startupLeader is the leader key read once when `afk dash` starts. It is a
// variable so a test can stand in for the config file.
var startupLeader = func() string { return configuredLeader(daemonconfig.DefaultDeps()) }

// configuredLeader reads config.toml's [dash] leader, best effort: it never
// fails and never prints. A malformed file must not stop the dashboard (see
// the rule at the top of cmdDash), so a load error gives "" (the default
// leader). When the [dash] table is bad Load resets it, and the first scan
// toast already names the file and the key, so that gives "" as well.
func configuredLeader(d daemonconfig.Deps) string {
	cfg, err := daemonconfig.Load(d)
	if err != nil || cfg.DashErr != nil {
		return ""
	}
	return cfg.Dash.Leader
}

// resolveTmuxClientTTY asks tmux which tty the attached client is on, or returns
// "" when there is no tmux to ask.
//
// Bounded by the same timeout as the git call above, for the same reason applied
// to a different failure: this runs on `afk dash` STARTUP, before the Bubble Tea
// program exists, so a tmux server that has stopped answering its socket would
// hang the dashboard with a blank terminal and nothing to look at. Expiry yields
// "", identical to running outside tmux — an entry with no client_tty, which
// internal/notify's target resolution already handles.
func resolveTmuxClientTTY(tmux string) string {
	if tmux == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCommonDirTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_tty}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
