package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/dphaener/afk/internal/dashmeta"
	"github.com/dphaener/afk/internal/notify"
)

// notifyInstallCmd is how a user gets the click-to-focus backend. Printed by
// --check whenever the resolved backend is osascript or nothing, since those are
// exactly the cases where clicking a toast cannot bring you to the session.
const notifyInstallCmd = "brew install vjeantet/tap/alerter"

// notifySettingsPath names the System Settings entry to change, and the style to
// change it to, when a toast auto-dismisses before it can be clicked. It is
// TERMINAL, not alerter: W0.T1 established that alerter delivers under the
// com.apple.Terminal identity by default, and that overriding it with --sender
// suppresses delivery entirely, so Terminal is the app whose notification style
// the user has to change.
//
// Every string built from this SUGGESTS the step and states what it is for; none
// promises that it works. That hedge is deliberate and must survive edits: whether
// the Alerts style actually makes these toasts persist was never observed. System
// Settings cannot be driven programmatically, so it is verifiable by the user
// alone. Two things are known and pull in opposite directions — alerter's
// InstallFakeBundleIdentifierHook("com.apple.Terminal") makes Terminal the only
// entry that could apply, while `com.apple.Terminal` is absent from this machine's
// com.apple.ncprefs.plist entirely (verified read-only) even though delivery
// works. So the row may not exist until macOS has registered it, which is why the
// wording below hedges on finding it at all rather than instructing a toggle as
// though it were sitting there.
const notifySettingsPath = "System Settings > Notifications > Terminal, style Alerts"

// cmdNotify implements `afk notify`: post a desktop notification for the
// current session and return immediately.
//
// NOTHING is delivered in this process. Both backends spawn a detached child:
// osascript's `display notification` alone measures 90-170ms (verified in
// W0.T1), which would blow the whole latency budget for a path that runs on
// every Claude Code hook. The child is re-exec'd `afk notify --watch ...`, one
// spawn path for both backends, so the flags that carry the toast across the
// process boundary have a single definition and a single test
// (TestNotifySpawnArgsMatchInternalFlags).
func cmdNotify(args []string) {
	var f notifyFlags
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	defineNotifyFlags(fs, &f)
	fs.Usage = func() { notifyUsage(os.Stderr) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "afk notify: unexpected argument; all input is passed as flags")
		fs.Usage()
		os.Exit(2)
	}

	if f.watch {
		// Every watch path exits 0, including every failure inside it: this
		// process is the detached child of a hook, and a non-zero exit here
		// would surface to the user as a failed hook over something as
		// inconsequential as a pane that no longer exists.
		toast, target, request := notifyWatchInputs(f, resolveGitCommonDir())
		runNotifyWatch(notify.Backend(f.backend), toast, target, request)
		return
	}

	selected, _, found := notify.Select(exec.LookPath)

	if f.check {
		for _, line := range notifyCheckLines(selected, found) {
			fmt.Println(line)
		}
		// Deliberately inverts internal/probe's convention of never reporting
		// absence through an exit code: notify.sh needs a capability probe it
		// can branch on with `if`, and a shell script cannot read a Result
		// struct. 0 means "a notifier exists", non-zero means "none".
		if !found {
			os.Exit(1)
		}
		return
	}

	gitCommonDir := resolveGitCommonDir()
	repo := f.repo
	if repo == "" {
		repo = notifyRepoName(gitCommonDir)
	}
	toast := notify.Format(notify.Input{PlanName: f.plan, Repo: repo, Message: f.message})

	// --dry-run reads the dash entry through PeekEntry, never ReadEntry. ReadEntry
	// DELETES the entries it finds dead as it scans (its self-heal), so calling it
	// here would let a diagnostic command mutate the rendezvous directory — and a
	// `--dry-run` that changes state cannot be used to inspect a suspect one,
	// which is the only reason to reach for it. PeekEntry answers the same question
	// and writes nothing.
	entry, entryFound := dashmeta.PeekEntry(gitCommonDir)
	target := notify.ResolveTarget(entry, entryFound, os.Getenv)

	if f.dryRun {
		for _, line := range notifyDryRunLines(toast, selected, found, target) {
			fmt.Println(line)
		}
		return
	}

	// The self-heal happens only on the delivering path, where a stale entry would
	// otherwise keep aiming clicks at a dead dashboard's pane forever.
	entry, entryFound = dashmeta.ReadEntry(gitCommonDir)
	target = notify.ResolveTarget(entry, entryFound, os.Getenv)

	if !found {
		// The default path with no notifier installed is a silent success. A
		// missing optional convenience is not a failure of the caller's work.
		return
	}

	// Spawn errors are swallowed: this path must never fail a caller, so the
	// worst outcome available is no toast.
	_ = spawnDetached(notifySelf(), notifySpawnArgs(toast, selected, target, f.plan, entry.PID))
}

// notifyFlags holds every value `afk notify` parses. It exists so
// defineNotifyFlags can be the ONE place the flag names are registered: the
// flag-contract test builds a real FlagSet through that same function and
// compares its registrations against what notifySpawnArgs emits, which it could
// not do against flags declared inline in cmdNotify.
type notifyFlags struct {
	plan    string
	repo    string
	message string
	dryRun  bool
	check   bool

	watch       bool
	backend     string
	title       string
	subtitle    string
	body        string
	group       string
	pane        string
	clientTTY   string
	terminalApp string
	session     string
	dashPID     int
}

// defineNotifyFlags registers every flag name on fs. The names here are the
// canonical contract: the public five are called by name from notify.sh, and the
// internal ones are the wire format between this process and the detached child
// notifySpawnArgs builds. A rename on either side is invisible to the compiler,
// which is why TestNotifySpawnArgsMatchInternalFlags compares the two sets.
//
// --dash-pid names the dashboard a honored click should be addressed to. The
// PARENT resolves it, from the same dash entry it takes --pane and --client-tty
// from, so the request the child publishes and the client the child switches can
// never belong to different dashboards. It is not re-resolved in the child: by
// click time the entry may have changed, and a request addressed to a dashboard
// other than the one being switched to is the two-dashboard bug in miniature.
//
// --session carries the session name to the click path so a honored click can
// name the session it wants opened. It is separate from --plan (the public flag)
// because the two are not the same contract: --plan is what notify.sh passes in,
// and --session is what crosses to the child. They currently carry the same value,
// and collapsing them would put a public flag name in the child's wire format
// where notify.sh's callers could not see it change.
//
// There is NO --sound flag, and that is deliberate rather than an oversight.
// notify.Format never populates Toast.Sound because the sound name is
// per-backend — AlerterArgs hardcodes the literal "default", OsascriptArgs
// hardcodes "Ping" because no default.aiff exists — so the parent had nothing to
// put in it and the child had nothing to do with it. A flag that is always empty
// and silently ignored still READS as live: the next reader wires the child's
// Toast.Sound into an argv builder, the parent keeps sending "", and the toasts
// go silent with nothing to point at. Removing it is the only state in which the
// code says what is true.
//
// The internal flags carry EMPTY usage strings deliberately: notifyUsage prints
// the public options by hand rather than calling PrintDefaults, so these never
// appear in `afk notify --help` — the same treatment `_host` gets in
// printUsage. Nothing stops a user passing --watch, but documenting it would
// invite it, and a hand-run watcher blocks for up to 12h.
// notifyWatchInputs maps parsed flags onto the watch path's three inputs. It is
// a separate pure function because inline struct literals are unassertable: a
// dropped field compiles, vets, and passes the flag-contract test (which only
// compares flag names). That shipped once — --dash-pid never reached
// FocusRequest.TargetPID, so every focus request was refused and click-to-focus
// was silently dead with the suite green. gitCommonDir is a parameter to keep
// the mapping pure.
func notifyWatchInputs(f notifyFlags, gitCommonDir string) (notify.Toast, notify.Target, notify.FocusRequest) {
	// Toast.Sound is deliberately left at its zero value: there is no --sound
	// flag on the wire, because the sound name is per-backend and
	// AlerterArgs/OsascriptArgs each supply their own (see defineNotifyFlags).
	toast := notify.Toast{
		Title:    f.title,
		Subtitle: f.subtitle,
		Body:     f.body,
		Group:    f.group,
	}
	target := notify.Target{
		PaneID:      f.pane,
		ClientTTY:   f.clientTTY,
		TerminalApp: f.terminalApp,
	}
	request := notify.FocusRequest{
		Session:      f.session,
		TargetPID:    f.dashPID,
		GitCommonDir: gitCommonDir,
	}
	return toast, target, request
}

func defineNotifyFlags(fs *flag.FlagSet, f *notifyFlags) {
	fs.StringVar(&f.plan, "plan", "", "Session/plan name the notification is about")
	fs.StringVar(&f.repo, "repo", "", "Repository name (defaults to the git repo this runs in)")
	fs.StringVar(&f.message, "message", "", "Notification text")
	fs.BoolVar(&f.dryRun, "dry-run", false, "Print what would be delivered and exit without delivering it")
	fs.BoolVar(&f.check, "check", false, "Report whether a notifier is available; exits non-zero when none is")

	fs.BoolVar(&f.watch, "watch", false, "")
	fs.StringVar(&f.backend, "backend", "", "")
	fs.StringVar(&f.title, "title", "", "")
	fs.StringVar(&f.subtitle, "subtitle", "", "")
	fs.StringVar(&f.body, "body", "", "")
	fs.StringVar(&f.group, "group", "", "")
	fs.StringVar(&f.pane, "pane", "", "")
	fs.StringVar(&f.clientTTY, "client-tty", "", "")
	fs.StringVar(&f.terminalApp, "terminal-app", "", "")
	fs.StringVar(&f.session, "session", "", "")
	fs.IntVar(&f.dashPID, "dash-pid", 0, "")
}

// notifyUsage writes the help text. It lists the public options by hand instead
// of calling fs.PrintDefaults, which would print the internal spawn flags too.
func notifyUsage(w io.Writer) {
	fmt.Fprintf(w, `afk notify — post a desktop notification for a session

Usage:
  afk notify [options]

Delivery is always asynchronous: a detached child posts the notification and
this command returns immediately, so it is safe to call from a hook on the
critical path. When no notifier is installed it does nothing and exits 0.

With alerter installed, the notification carries a Focus button that switches
your terminal to the dashboard pane the session is running in.

Options:
  --plan <name>      Session/plan name the notification is about
  --repo <name>      Repository name (defaults to the git repo this runs in)
  --message <text>   Notification text
  --dry-run          Print what would be delivered, deliver nothing
  --check            Report whether a notifier is available; exits non-zero
                     when none is, so scripts can use it as a capability probe
`)
}

// runNotifyWatch is the body of the detached child. alerter is watched because
// it reports the click on stdout and can therefore focus the session; osascript
// is fired once and abandoned, because `display notification` returns
// immediately and can never report a click. Both live behind the same --watch
// re-exec so there is only one spawn path to keep in sync.
func runNotifyWatch(backend notify.Backend, toast notify.Toast, target notify.Target, fr notify.FocusRequest) {
	path, err := exec.LookPath(string(backend))
	if err != nil {
		return
	}
	switch backend {
	case notify.BackendAlerter:
		// The icon is resolved HERE, in the child, rather than passed as a
		// flag: extraction touches the filesystem, and the parent is on a
		// hook's critical path. IconPath() returns "" on any failure and
		// AlerterArgs then omits --app-icon entirely.
		notify.Watch(path, notify.AlerterArgs(toast, notify.Focusable(target), notify.IconPath()), target, fr)
	case notify.BackendOsascript:
		cmd := exec.Command(path, notify.OsascriptArgs(toast)...)
		if null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
			defer null.Close()
			cmd.Stdin = null
			cmd.Stdout = null
			cmd.Stderr = null
		}
		_ = cmd.Run()
	}
}

// notifyRepoName derives the repo identity from the git COMMON dir's parent,
// never from the basename of the working directory. In a afk worktree the cwd
// is `<repo>.<session>` (e.g. afk.improve-desktop-notifications), which
// would collapse the repo and the session into one token and silently void the
// group-collision guarantee that `afk-{repo}-{plan}` exists to provide — two
// sessions of the same name in different repos would start replacing each
// other's toasts. The common dir is shared by every worktree of a repo, so it
// answers the same everywhere.
//
// `~/ontra/afk/.git` -> `afk`. A `--separate-git-dir` or bare setup
// gives a common dir like `/elsewhere/afk.git`, whose parent is unrelated
// to the repo, so a non-".git" basename is used directly with the suffix
// trimmed. Pure, so both shapes are table-testable without a git repo.
func notifyRepoName(gitCommonDir string) string {
	if gitCommonDir == "" {
		return ""
	}
	dir := filepath.Clean(gitCommonDir)
	base := filepath.Base(dir)
	if base == ".git" {
		parent := filepath.Base(filepath.Dir(dir))
		if parent == "." || parent == string(filepath.Separator) {
			return ""
		}
		return parent
	}
	name := strings.TrimSuffix(base, ".git")
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// notifySpawnArgs builds the argv for the detached child. Pure, so the flag
// names can be asserted against the FlagSet's own registrations
// (TestNotifySpawnArgsMatchInternalFlags) without spawning anything.
//
// A flag whose value is empty is OMITTED ENTIRELY rather than emitted with "".
// The child's FlagSet already defaults every one of these to "", so an absent
// flag parses identically to an empty one — the difference is only in what argv
// can contain, and two things there are worth refusing:
//
//   - A valueless flag at the END of argv silently annexes whatever gets
//     appended after it later. `--terminal-app` was last, and Go's flag package
//     takes the next argument as a non-bool flag's value UNCONDITIONALLY, dash
//     prefix and all (see flag.(*FlagSet).parseOne) — so a flag added below
//     `--terminal-app` would not fail to parse, it would silently become
//     `--terminal-app`'s value and vanish.
//   - An `--x ""` pair is indistinguishable in `ps` from a flag that swallowed
//     the token after it, which is exactly how this was misread during
//     verification. Argv is the only diagnostic surface a process with stdio on
//     /dev/null has; keeping it free of empty elements keeps it readable.
//
// Which values can really be empty: `--pane` and `--client-tty` whenever no
// dashboard recorded an entry, `--terminal-app` whenever AFK_TERMINAL_APP is
// unset (the normal case), and `--session` whenever the caller passed no --plan.
// `--title`, `--body` and `--group` cannot be empty out of notify.Format, and
// `--backend` cannot be empty at the one call site (the spawn is unreachable
// unless Select found one) — they go through the same omit-if-empty path anyway,
// because the invariant worth having is "no empty element in argv, ever", not
// "no empty element among the three fields that happen to be able to produce one
// today".
//
// The flag-contract test still sees the complete set from one call: it passes a
// non-empty value for every field, and a separate test covers the omissions.
func notifySpawnArgs(t notify.Toast, backend notify.Backend, target notify.Target, session string, dashPID int) []string {
	args := []string{"notify", "--watch"}
	// dashPID rides the wire as a string like every other flag, and 0 (no dashboard
	// entry found) omits it exactly as an empty string does — a request addressed to
	// pid 0 could never be read by anyone.
	dash := ""
	if dashPID > 0 {
		dash = strconv.Itoa(dashPID)
	}
	for _, f := range []struct{ name, value string }{
		{"backend", string(backend)},
		{"title", t.Title},
		{"subtitle", t.Subtitle},
		{"body", t.Body},
		{"group", t.Group},
		{"pane", target.PaneID},
		{"client-tty", target.ClientTTY},
		{"terminal-app", target.TerminalApp},
		{"session", session},
		{"dash-pid", dash},
	} {
		if f.value == "" {
			continue
		}
		args = append(args, "--"+f.name, f.value)
	}
	return args
}

// notifyDryRunLines is the --dry-run report: every field that decides what the
// user would see, plus the three environment-dependent target fields, so a
// misconfigured pane or terminal is visible without firing anything. Pure, so
// each line is assertable individually — asserting the whole block would make
// the test depend on whether a dashboard happens to be running.
func notifyDryRunLines(t notify.Toast, backend notify.Backend, found bool, target notify.Target) []string {
	backendName := string(backend)
	if !found {
		backendName = "(none)"
	}
	return []string{
		"title:        " + t.Title,
		"subtitle:     " + t.Subtitle,
		"body:         " + t.Body,
		"group:        " + t.Group,
		"sound:        " + notifyDryRunSound(backend, found),
		"backend:      " + backendName,
		"pane:         " + notifyDryRunValue(target.PaneID),
		"client-tty:   " + notifyDryRunValue(target.ClientTTY),
		"terminal-app: " + notifyDryRunValue(target.TerminalApp),
	}
}

// notifyDryRunSound reports the sound the chosen backend would actually use.
// Toast.Sound is empty by design (see notify.Format): the name is per-backend —
// alerter takes the literal "default", osascript needs "Ping" because there is
// no default.aiff — and each argv builder supplies its own. Reporting the empty
// field would tell the user "no sound", which is wrong.
func notifyDryRunSound(backend notify.Backend, found bool) string {
	if !found {
		return "(none)"
	}
	switch backend {
	case notify.BackendAlerter:
		return "default"
	case notify.BackendOsascript:
		return "Ping"
	}
	return "(none)"
}

func notifyDryRunValue(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// notifyCheckLines is --check's report. When the backend is osascript or absent,
// it includes the install command and the Settings path, because those are the
// two cases where a notification cannot be clicked to reach the session:
// osascript's `display notification` never reports interaction at all, and a
// banner-styled toast auto-dismisses before anyone can click it. Pure, so the
// wording is assertable.
//
// The Settings line is phrased as "if … try", not as an instruction with a
// promised result, and the phrasing is the point: whether the Alerts style makes
// these toasts persist was never observed (see notifySettingsPath), and a --check
// that states it as fact would be this command asserting something nobody ran.
func notifyCheckLines(backend notify.Backend, found bool) []string {
	settings := []string{
		"If a toast vanishes before you can click it, try " + notifySettingsPath + ".",
		"Look for Terminal, not alerter: alerter delivers under Terminal's identity.",
		"(Whether that makes these toasts persist is unverified — the entry may also",
		" be absent until macOS registers it.)",
	}
	if found && backend == notify.BackendAlerter {
		return append([]string{"notifier: alerter (click-to-focus available)"}, settings...)
	}
	var lines []string
	if found {
		lines = append(lines, "notifier: "+string(backend)+" (no click-to-focus)")
	} else {
		lines = append(lines, "notifier: none found")
	}
	lines = append(lines,
		"Install alerter for click-to-focus notifications:",
		"  "+notifyInstallCmd)
	return append(lines, settings...)
}

// notifySelf locates this binary for the re-exec, the same idiom runDashOpen
// uses (new.go:146-150): os.Executable can fail or return "" on some platforms,
// and os.Args[0] is the only fallback available.
func notifySelf() string {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = os.Args[0]
	}
	return self
}

// spawnDetached starts name with args as a fully detached process and returns
// without waiting for it.
//
// SysProcAttr sets Setsid and ONLY Setsid. On darwin,
// SysProcAttr{Setsid: true, Setpgid: true} fails EVERY spawn with
// "fork/exec: operation not permitted" (verified in W0.T1); either field alone
// works. Setsid already makes the child a session leader and the leader of a new
// process group whose pgid equals its pid, so `Kill(-pid, ...)` is a group kill
// with no second field — and since this path swallows its errors so it can never
// fail a caller, adding Setpgid would turn every notification into a silent
// no-op with no diagnostic anywhere.
//
// All three stdio fds go to /dev/null rather than being inherited. The child
// outlives this process, so holding the caller's fd 2 would keep a pipe open
// that its reader waits on for EOF — the wedge documented at
// internal/session/create.go's buildHostCmd.
func spawnDetached(name string, args []string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()

	cmd := exec.Command(name, args...)
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Release, never Wait: this process is about to exit, and the child is
	// reparented to init rather than being left a zombie.
	return cmd.Process.Release()
}
