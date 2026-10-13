package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/planpatch"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ship"
	"github.com/dphaener/afk/internal/tokens"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// This file is the daemon's command line and its wiring, and nothing else.
// Every rule about what the loop does lives in internal/daemon; what is here
// reads the machine config, takes the machine lock, hands the loop the real
// versions of the things it reaches outside for, and prints a report.

const daemonUsageLine = "afk daemon [--once] | afk daemon status"

func printDaemonUsage(w io.Writer) {
	fmt.Fprintf(w, `afk daemon — build the master plan's tasks without anyone at the keyboard

Usage:
  afk daemon           Run until stopped. For each project in afk's machine
                       config it starts the next ready task in its own session,
                       one task at a time, and watches it.
  afk daemon --once    Look at every project once, do what is due, and exit.
  afk daemon status    Say what the daemon is doing in each project, and why.

Projects are listed in config.toml in afk's config folder (normally
~/.config/afk/config.toml), one [[project]] with a path each.

Stopping the daemon (ctrl-c, or a stop from the service manager) leaves every
session running; the next start picks them up again. "afk pause" stops new
tasks from starting in one project without stopping the daemon.

When a new version of afk is installed over the one that is running, the
daemon stops by itself between two looks (exit 0), so a service manager set to
restart it always starts the new version. Run by hand, start it again.

Exit codes: 0 done or stopped (status: the report was printed), 1 the config
could not be used, no project is configured, or a daemon is already running,
2 the command was written wrongly.
`)
}

// daemonEnv is everything `afk daemon` takes from the machine, so the command
// body runs in tests against temp folders with a fake loop underneath: no real
// home, no git, no agent CLI, no network and no signal.
type daemonEnv struct {
	// config and state resolve afk's config and state folders. They are the
	// packages' own Deps so a test points XDG_CONFIG_HOME and XDG_STATE_HOME
	// at temp folders through Getenv and never touches the process's real
	// environment.
	config daemonconfig.Deps
	state  usagestate.Deps
	now    func() time.Time
	// loc is the zone times are shown in.
	loc *time.Location
	pid int
	// gitCommonDir is `git rev-parse --path-format=absolute --git-common-dir`
	// run in dir, or "" outside a git repo.
	gitCommonDir func(dir string) string
	isDir        func(string) bool
	// alive reports whether a process id is running.
	alive func(pid int) bool
	// newDeps builds what the loop reaches outside for. The real one starts
	// agents; tests return fakes, which is the one reason it is a field.
	//
	// gov is the [governor] table of the machine config: the usage limits the
	// real loop hands to its usage source and its login check.
	//
	// publicURL is the address the owner reaches the web page at, or "" when
	// the page is off or has none. It is all of the [web] table the loop is
	// given, and it is there for one thing: the link in a notification. The
	// token is never handed over.
	newDeps func(p provider.Provider, providerName string, gov daemonconfig.Governor, publicURL string, log func(format string, args ...any)) daemon.Deps
	// signalContext returns a context that ends when the process is asked to
	// stop.
	signalContext func() (context.Context, context.CancelFunc)
	// binary reads the identity of afk's own program file as it is on disk
	// now: once when the daemon starts and once at the top of every pass, so
	// a new build installed over it is noticed (PLAN T2.2a). Nil means the
	// daemon never looks. An error means the file cannot be seen.
	binary func() (daemon.Binary, error)
	// listen opens the web page's port. It is a field so a test hands back a
	// listener of its own and no test ever binds a fixed port.
	listen func(addr string) (net.Listener, error)
}

func realDaemonEnv() daemonEnv {
	// A failed UserHomeDir is passed on as "": the unattended preflight then
	// finds no settings file and fails with its manual steps, which is the
	// right answer for a machine whose home is unknown.
	home, _ := os.UserHomeDir()
	now := time.Now
	// The path is asked for once, here, and the same text is read on every
	// call. Asking again later would give the wrong answer exactly when it
	// matters: on Linux os.Executable reads the /proc/self/exe link each time
	// (os/executable_procfs.go), and that link follows the file this process
	// was started from, not the name it had. Once a new build has replaced
	// the file, the link no longer leads to the new one, so a fresh lookup
	// could never see it. The path as it was at the start is where the new
	// build lands.
	exe, exeErr := os.Executable()
	st := usagestate.DefaultDeps()
	return daemonEnv{
		config:       daemonconfig.DefaultDeps(),
		state:        st,
		now:          now,
		loc:          time.Local,
		pid:          os.Getpid(),
		gitCommonDir: resolveGitCommonDirIn,
		isDir: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		},
		alive: host.Alive,
		newDeps: func(p provider.Provider, providerName string, gov daemonconfig.Governor, publicURL string, log func(string, ...any)) daemon.Deps {
			return realDaemonDeps(p, providerName, home, st, gov, publicURL, now, os.Stderr, log)
		},
		signalContext: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		},
		binary: func() (daemon.Binary, error) {
			if exeErr != nil {
				return daemon.Binary{}, exeErr
			}
			return daemon.ReadBinary(exe)
		},
		listen: func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) },
	}
}

// cmdDaemon implements `afk daemon`.
func cmdDaemon(args []string) {
	os.Exit(runDaemon(args, os.Stdout, os.Stderr, realDaemonEnv()))
}

// runDaemon implements `afk daemon` and returns the exit code: 0 done, 1 a
// runtime problem, 2 a command line that could never work.
//
// Flags are accepted before and after the word, parsed in a loop as
// runLedgerSet does, because the flag package stops at the first non-flag.
func runDaemon(args []string, stdout, stderr io.Writer, env daemonEnv) int {
	fs := flag.NewFlagSet("afk daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	once := fs.Bool("once", false, "Look at every project once and exit")
	fs.Usage = func() { printDaemonUsage(stderr) }

	var words []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		words = append(words, rest[0])
		rest = rest[1:]
	}

	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "afk daemon: "+format+"\n", a...)
		fmt.Fprintln(stderr, "Usage: "+daemonUsageLine)
		return 2
	}
	switch {
	case len(words) == 0:
	case words[0] != "status":
		return usage("unknown word %q. The only word it takes is status.", words[0])
	case len(words) > 1:
		return usage("status takes nothing after it, but got %q.", words[1])
	case *once:
		return usage("--once does not go with status. Status only reads; it never starts anything.")
	default:
		return runDaemonStatus(stdout, stderr, env)
	}

	cfg, err := daemonconfig.Load(env.config)
	if err != nil {
		fmt.Fprintf(stderr, "afk daemon: %v\n", err)
		return 1
	}
	cfgPath := daemonconfig.Path(env.config)
	if len(cfg.Projects) == 0 {
		fmt.Fprintf(stderr, "afk daemon: no projects are configured, so there is nothing to build.\n%s", addProjectHelp(cfgPath))
		return 1
	}
	prov, err := provider.Lookup(cfg.Provider)
	if err != nil {
		fmt.Fprintf(stderr, "afk daemon: %s sets provider = %q, which afk does not know. Use one of: %s.\n",
			cfgPath, cfg.Provider, strings.Join(provider.Names(), ", "))
		return 1
	}

	// The lock is taken for --once too. One pass can start a session, and a
	// pass run beside a live daemon could start a second session for a task
	// in the moment between the other's read of the ledger and its write.
	stateDir, err := usagestate.Dir(env.state)
	if err != nil {
		fmt.Fprintf(stderr, "afk daemon: cannot find afk's state folder: %v. Set XDG_STATE_HOME to an absolute path or make sure your home directory is known.\n", err)
		return 1
	}
	unlock, err := daemonstate.Lock(stateDir, env.pid, env.now())
	if errors.Is(err, daemonstate.ErrAlreadyRunning) {
		if pid, _, ok := daemonstate.ReadMachine(stateDir); ok {
			fmt.Fprintf(stderr, "afk daemon is already running (pid %d). Stop that one first, or use \"afk daemon status\" to see what it is doing.\n", pid)
		} else {
			fmt.Fprintln(stderr, "afk daemon is already running. Stop that one first, or use \"afk daemon status\" to see what it is doing.")
		}
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "afk daemon: %v. Check that you can write to %s.\n", err, stateDir)
		return 1
	}
	defer unlock()

	logf := daemonLogger(stderr, env.now)
	projects, dropped := daemonProjects(cfg.Projects, env.gitCommonDir)
	for _, d := range dropped {
		logf("%s", d)
	}
	deps := env.newDeps(prov, cfg.Provider, cfg.Governor, webPublicURL(cfg.Web), logf)
	settings := daemonSettings(cfg)

	if *once {
		for i, st := range daemon.Once(projects, settings, deps) {
			fmt.Fprintln(stdout, daemonOnceLine(projects[i].Name, st))
		}
		return 0
	}

	// The program file's identity is recorded here, after the lock (so only
	// the daemon that really runs marks running.json) and never for --once,
	// which leaves after one pass whatever is on disk.
	//
	// A file that cannot be read is not a reason to refuse to run: the daemon
	// builds as before and only loses the restart on a new version. That loss
	// is silent unless it is written down, so it is said once in the log and
	// kept in running.json, where `afk daemon status` reads it. The seam is
	// still handed to the loop; with no identity from the start the loop's
	// rule (daemon.NewBinary) is to carry on whatever a later read says.
	if env.binary != nil {
		id, err := env.binary()
		if err != nil {
			logf("%s (%v)", binaryUnknownText, err)
			if err := daemonstate.MarkBinaryUnknown(stateDir); err != nil {
				logf("warning: that could not be recorded for \"afk daemon status\": %v", err)
			}
		} else {
			settings.Binary = id
		}
		deps.Binary = env.binary
	}

	ctx, stop := env.signalContext()
	defer stop()

	// The web page. It is opened here and nowhere earlier: after the machine
	// lock, so a second daemon that is about to be refused never takes the
	// port from the one that is running, and after the --once return, because
	// a single pass is over before anyone could open a page. It is opened
	// before the loop so that a port that cannot be had is an exit with a
	// message the owner sees at start-up, not a line lost in a running log.
	//
	// The stop is deferred, not tied to ctx: the new-version way out below
	// returns without ctx ever ending, and a listener left open there would
	// keep the port from the daemon the service manager starts next.
	//
	// Neither log line may hold the token, and the address is the configured
	// bind, which has none.
	if cfg.Web.Enabled() {
		h, err := web.New(web.Config{Token: cfg.Web.Token, PublicURL: cfg.Web.PublicURL}, daemonWebDeps(projects, env.state, env.now))
		if err != nil {
			fmt.Fprintf(stderr, "afk daemon: could not set up the web page: %v. Check the [web] table in %s.\n", err, cfgPath)
			return 1
		}
		ln, err := env.listen(cfg.Web.Bind)
		if err != nil {
			fmt.Fprintf(stderr, "afk daemon: could not open the web page on %s: %v. Another program may be using that port; change [web] bind in %s.\n", cfg.Web.Bind, err, cfgPath)
			return 1
		}
		defer serveDaemonWeb(ln, h, logf)()
		logf("web page: listening on http://%s", cfg.Web.Bind)
	} else {
		logf("web page: off (set [web] token in %s to turn it on)", cfgPath)
	}

	logf("afk daemon started (pid %d): %d project(s), looking every %s", env.pid, len(projects), cfg.Tick)
	// Both ways out are exit 0. Being asked to stop is not a failure, and
	// neither is stopping for a new version: the daemon chose to, between two
	// passes. The service manager starts it again because the unit says
	// "always", not because of the exit code, and a non-zero code here would
	// show every install as a crash in its log.
	if err := daemon.Run(ctx, projects, settings, cfg.Tick, deps); errors.Is(err, daemon.ErrNewBinary) {
		logf("afk daemon found a new version of itself on disk and is stopping so it can be started again. Every session is still running.")
		return 0
	}
	logf("afk daemon stopped. Every session is still running; the next start picks them up.")
	return 0
}

// webPublicURL is the address a notification may link to, or "" for no link.
// Pure.
//
// A public address with no token gives no link. With no token the page is
// off (daemonconfig.Web), so the link would open nothing, and a notification
// that leads to an error page is worse than one with only the command in it.
func webPublicURL(w daemonconfig.Web) string {
	if !w.Enabled() {
		return ""
	}
	return w.PublicURL
}

// binaryUnknownText is what the daemon's log and `afk daemon status` both say
// when the daemon cannot read its own program file.
const binaryUnknownText = "afk cannot see its own program file, so it will not restart by itself when a new version is installed. Restart it by hand after an install."

// addProjectHelp says where the machine config goes and what to put in it.
func addProjectHelp(cfgPath string) string {
	return fmt.Sprintf("Add a [[project]] with a path to %s:\n\n  [[project]]\n  path = \"/full/path/to/your/project\"\n", cfgPath)
}

// daemonLogger returns the daemon's log: one timestamped line per call on w.
// The daemon has no log file of its own; under a service manager stderr is
// the journal, and at a terminal it is the terminal.
func daemonLogger(w io.Writer, now func() time.Time) func(format string, args ...any) {
	return func(format string, args ...any) {
		fmt.Fprintf(w, "%s %s\n", now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
}

// daemonProjects turns the configured projects into the loop's, each pointed
// at its main checkout. It is pure apart from the injected git lookup.
//
// A configured path may be a session's worktree (it is a folder like any
// other to the person typing the config). The ledger, the inbox and the logs
// live in the main checkout only, so every path goes through mainCheckout
// first. Two entries that turn out to be the same checkout would make two
// loops over one ledger, each believing it runs one task at a time; the
// later one is dropped and reported.
func daemonProjects(cfg []daemonconfig.Project, gitCommonDir func(string) string) (projects []daemon.Project, dropped []string) {
	seen := map[string]string{}
	for _, p := range cfg {
		root := mainCheckout(p.Path, gitCommonDir(p.Path))
		if first, dup := seen[root]; dup {
			dropped = append(dropped, fmt.Sprintf("%s is left out: its folder is the same checkout as %s (%s). List each project once in config.toml.", p.Name, first, root))
			continue
		}
		seen[root] = p.Name
		projects = append(projects, daemon.Project{Name: p.Name, Root: root})
	}
	return projects, dropped
}

// daemonOnceLine is what `afk daemon --once` prints for one project.
func daemonOnceLine(name string, st daemonstate.State) string {
	line := name + ": " + daemonKindText(st.Kind)
	if st.Task != "" {
		line += " (" + st.Task
		if st.Session != "" {
			line += ", session " + st.Session
		}
		line += ")"
	}
	if st.Reason != "" {
		line += ": " + st.Reason
	}
	return line
}

// daemonLaunchConfig is the session the daemon asks for when it starts a
// task. It is a function of its own so a test can pin the three switches.
//
// Autonomous and Unattended are separate and both needed: the first tells the
// conductor not to ask the owner questions, the second tells the agent CLI
// not to ask permission. With only one of them the session stops at a prompt
// nobody is there to answer.
//
// NoGitSpice is always set and Base is always empty. An unattended launch
// must come out the same whether or not git-spice happens to be set up in the
// main checkout, and with no base named the plain-git trunk is used.
func daemonLaunchConfig(req daemon.LaunchRequest, providerName string) session.Config {
	return session.Config{
		Name:        req.Session,
		Description: req.Description,
		RepoDir:     req.Project.Root,
		Autonomous:  true,
		Unattended:  true,
		NoGitSpice:  true,
		Provider:    providerName,
	}
}

// daemonPreflight is the check run before a task is launched: can this
// machine start the agent with nobody at the terminal?
//
// Both halves always run and their steps are joined, so one inbox item lists
// everything to fix; stopping at the first would cost the owner a second
// round trip for the second problem. The plugin check is the provider's
// existing one. Session creation treats that as a warning, because a person
// is watching and can install the plugin; here nobody is, and an agent
// started without the plugin would sit at an unknown command for ever.
func daemonPreflight(p provider.Provider, ud provider.UnattendedDeps, run provider.RunFunc) func(daemon.Project) (bool, string) {
	return func(proj daemon.Project) (bool, string) {
		okMode, modeSteps := p.UnattendedPreflight(ud)
		okPlugin, pluginSteps := p.Preflight(proj.Root, run)
		var steps []string
		if !okMode && strings.TrimSpace(modeSteps) != "" {
			steps = append(steps, strings.TrimSpace(modeSteps))
		}
		if !okPlugin && strings.TrimSpace(pluginSteps) != "" {
			steps = append(steps, strings.TrimSpace(pluginSteps))
		}
		return okMode && okPlugin, strings.Join(steps, "\n\n")
	}
}

// daemonNotifier sends the daemon's push notifications. Every failure is a
// line in the log and nothing more: a notification is a courtesy on top of
// the inbox item, which is already on disk, and must never fail a pass.
type daemonNotifier struct {
	loadConfig func(root string) (projectconfig.Config, error)
	send       func(ctx context.Context, url string, m notify.NtfyMessage) error
	logf       func(format string, args ...any)
	// publicURL is where the owner reaches the web page, or "" for no page.
	// When set, each notification carries a link that opens what it is about
	// (ntfy's Click header, see notify.NtfyMessage). The link never holds the
	// token: the browser proves itself with its cookie.
	publicURL string
}

// item tells the owner about an inbox item the loop just added, with the
// text `afk inbox add` sends for the same item.
func (n daemonNotifier) item(p daemon.Project, it inbox.Item) {
	url, ok := n.address(p)
	if !ok {
		return
	}
	msg, ok := notify.InboxMessage(it.ID, it.Task, it.Kind, it.Title, it.Body, it.Blocking, url)
	if !ok {
		return
	}
	msg.Click = notify.ItemLink(n.publicURL, p.Name, it.ID)
	n.deliver(p, url, msg)
}

// idle sends the one notification with no item behind it. Priority 3 is
// ntfy's default: the build has stopped moving, but nothing is on fire. With
// no item to open, its link goes to the project's page.
func (n daemonNotifier) idle(p daemon.Project, title, body string) {
	url, ok := n.address(p)
	if !ok {
		return
	}
	n.deliver(p, url, notify.NtfyMessage{Title: "afk: " + title, Body: body, Priority: 3,
		Click: notify.ProjectLink(n.publicURL, p.Name)})
}

// urgent tells the owner about an item at ntfy's highest priority (5), the
// one that gets past a phone's quiet hours. The loop uses it for one thing: a
// stopped line, whether a deploy did not come up or a project's after-merge
// command failed.
//
// The text is the same as item's. InboxMessage is asked with "failure" and
// blocking rather than the item's own kind and flag for one reason: those two
// only choose whether to send and how loudly, the stop item is deliberately
// not blocking (`afk resume` clears the line, not an answer), and here both
// questions are already settled.
func (n daemonNotifier) urgent(p daemon.Project, it inbox.Item) {
	url, ok := n.address(p)
	if !ok {
		return
	}
	msg, ok := notify.InboxMessage(it.ID, it.Task, "failure", it.Title, it.Body, true, url)
	if !ok {
		return
	}
	msg.Priority = 5
	msg.Click = notify.ItemLink(n.publicURL, p.Name, it.ID)
	n.deliver(p, url, msg)
}

// address reads the project's ntfy address. No address is silence: most
// projects have none.
func (n daemonNotifier) address(p daemon.Project) (string, bool) {
	cfg, err := n.loadConfig(p.Root)
	if err != nil {
		n.logf("%s: warning: the project config could not be read, so no notification was sent: %v", p.Name, err)
		return "", false
	}
	return cfg.Notify.NtfyURL, cfg.Notify.NtfyURL != ""
}

func (n daemonNotifier) deliver(p daemon.Project, url string, m notify.NtfyMessage) {
	// Bounded, because this runs inside a pass: a server that never answers
	// must cost seconds, not the loop.
	ctx, cancel := context.WithTimeout(context.Background(), inboxNotifyTimeout)
	defer cancel()
	if err := n.send(ctx, url, m); err != nil {
		n.logf("%s: warning: the notification could not be sent: %v", p.Name, err)
	}
}

// daemonPatchPlan is the loop's PatchPlan: one guarded change to the master
// plan, made through planpatch.Run exactly as `afk plan patch` makes it.
//
// It lives here and not in internal/daemon because the daemon package stays
// free of git, gh and the project's config: it only says what to write (the
// Edit it hands over) and this file, which already owns every other real
// call, decides how the change reaches the plan (a commit on the main
// checkout's branch, or a pull request when [planning] via_pr is set).
//
// The config is read on every call, not once at start-up, so a via_pr the
// owner turns on while the daemon runs applies to the next answer. A config
// that cannot be read is an error and nothing is changed: guessing the
// default would commit to the base branch of a project that asked for pull
// requests.
//
// An error from req.Edit comes back from planpatch.Run as it was returned
// (Run's patch step returns it before it writes anything), so
// errors.Is(err, daemon.ErrRowAnswered) holds for the loop. The %w below
// keeps that true even if Run ever wraps it.
func daemonPatchPlan(load func(root string) (projectconfig.Config, error), run ship.RunFunc, tempDir func() (string, error)) func(daemon.Project, daemon.PlanPatchRequest) (daemon.PlanPatchResult, error) {
	return func(p daemon.Project, req daemon.PlanPatchRequest) (daemon.PlanPatchResult, error) {
		cfg, err := load(p.Root)
		if err != nil {
			return daemon.PlanPatchResult{}, fmt.Errorf("the project's afk.toml could not be read, so the plan was not changed: %w", err)
		}
		out, err := planpatch.Run(
			planpatch.Deps{Run: run, TempDir: tempDir},
			planpatch.Options{
				Root:     p.Root,
				PlanPath: req.PlanPath,
				Edit: func(plan []byte) (planpatch.Edit, error) {
					old, new, err := req.Edit(plan)
					if err != nil {
						return planpatch.Edit{}, fmt.Errorf("%w", err)
					}
					return planpatch.Edit{Old: old, New: new}, nil
				},
				Message: req.Message,
				ViaPR:   cfg.Planning.ViaPR,
				Base:    cfg.Ship.Base,
			})
		if err != nil {
			return daemon.PlanPatchResult{}, err
		}
		return daemon.PlanPatchResult{PR: out.PR, Notes: out.Notes}, nil
	}
}

// realDaemonDeps wires the loop to the real machine. Nothing here decides
// anything; each field is one call into the package that owns the work.
//
// st is where afk's state folder is found and gov is the machine config's
// [governor] table. Both go to the usage source and the login check only
// (daemon_governor.go); the loop itself never sees a threshold.
//
// publicURL is the web page's public address, or "": it goes to the notifier
// only, for the link in a notification.
func realDaemonDeps(p provider.Provider, providerName, home string, st usagestate.Deps, gov daemonconfig.Governor, publicURL string, now func() time.Time, logw io.Writer, logf func(string, ...any)) daemon.Deps {
	n := daemonNotifier{
		publicURL:  publicURL,
		loadConfig: projectconfig.Load,
		send: func(ctx context.Context, url string, m notify.NtfyMessage) error {
			return notify.Ntfy{URL: url}.Send(ctx, m)
		},
		logf: logf,
	}
	sd := session.DefaultDeps()
	// Session creation prints its steps as it goes. They belong in the
	// daemon's log, not on a stdout nobody reads. PickBase stays nil: there
	// is nobody to pick, and nil means "use the default base".
	sd.Stdout, sd.Stderr = logw, logw
	// The same for a clean-up's notes.
	cd := session.DefaultCleanDeps()
	cd.Stdout, cd.Stderr = logw, logw
	sendInput := func(worktree, sess, text string) error {
		return host.SendInput(worktree, sess, text, daemonSendWait)
	}
	sw := shipWiring{
		// One runner for git and gh: ship's adapters need a runner that
		// returns what was printed even on a failure and an error that knows
		// its exit code, and ship.NewRun is the one that promises both
		// (session's runner promises neither).
		run:       ship.NewRun(0),
		health:    ship.NewHealth(nil),
		sendInput: sendInput,
		listInbox: func(root string) ([]inbox.Item, error) {
			items, _, err := inbox.Store{Dir: inbox.Dir(root)}.List()
			return items, err
		},
		afterMerge: ship.RunAfterMerge,
		now:        now,
	}
	// One of each for the whole daemon, not one per project: usage and the
	// login belong to the account on this machine, and every project's pass
	// asks the same two.
	usage := realUsageSource(p, st, gov, now, logf)
	login := realLoginSource(p, st, gov, now, logf)
	return daemon.Deps{
		Now: now,
		LoadBuild: func(root string) ([]masterplan.Task, string, ledger.Ledger, error) {
			bs, err := loadBuildState(root)
			return bs.Tasks, bs.PlanPath, bs.Ledger, daemonLoadPlanError(root, err, os.ReadFile)
		},
		Sessions: func(root string) ([]discover.SessionState, error) {
			return discover.Discover(root, discover.DefaultDeps())
		},
		Preflight: daemonPreflight(p, provider.UnattendedDeps{Home: home, Euid: os.Geteuid, ReadFile: os.ReadFile}, sd.Run),
		Launch: func(req daemon.LaunchRequest) error {
			return session.Create(daemonLaunchConfig(req, providerName), sd)
		},
		RecordTokens: func(proj daemon.Project, task, sess, sessionID string) (int, error) {
			res, err := tokens.Record(tokens.RecordInput{
				Provider:     p,
				Home:         home,
				MainCheckout: proj.Root,
				Task:         task,
				Session:      sess,
				SessionID:    sessionID,
				Now:          now(),
			})
			return len(res.Lines), err
		},
		Notify:       n.item,
		NotifyIdle:   n.idle,
		NotifyUrgent: n.urgent,
		Log:          logf,
		Ship:         daemonShip(projectconfig.Load, sw),
		SendPrompt:   sendInput,
		Clean:        daemonClean(cd, ledger.Load),
		Restart: func(worktree, sess string) (string, error) {
			return host.RequestRestart(worktree, sess, daemonSendWait, daemonRestartReply)
		},
		// The provider and the unattended mode come from the session's own
		// files inside SpawnHost, so none is passed here.
		Relaunch: func(req daemon.RelaunchRequest) error {
			_, err := session.SpawnHost(session.HostSpawn{Name: req.Session, Worktree: req.Worktree, ResumeID: req.ResumeID}, sd)
			return err
		},
		ReadPlan:    os.ReadFile,
		PatchPlan:   daemonPatchPlan(projectconfig.Load, sw.run, func() (string, error) { return os.MkdirTemp("", "afk-plan-") }),
		Usage:       usage.view,
		Login:       login.view,
		StopSession: daemonStopSession(realStopDeps()),
	}
}

// daemonSettings turns the machine config into the loop's settings. It is a
// function of its own so a test can see that the recovery keys reach the loop:
// a loop left without them has max_restarts = 0 and parks on the first trouble.
// The same holds for max_wakes: left out, the loop would never wake a session
// and would park on the first failed turn, with an item that blames a setting
// the owner never made.
func daemonSettings(cfg daemonconfig.Config) daemon.Settings {
	return daemon.Settings{
		StallAfter:     cfg.StallAfter,
		MaxRestarts:    cfg.MaxRestarts,
		RestartBackoff: cfg.RestartBackoff,
		MaxWakes:       cfg.MaxWakes,
	}
}

// daemonRestartReply is how long a pass waits for a host to say how its
// restart went. The host's signal ladder can take about nine seconds and the
// dashboard waits the same; the pass waits for the answer so that the count
// and the time it saves describe a restart that finished (PLAN §5.6).
const daemonRestartReply = 15 * time.Second

// daemonSendWait is how long a typed line waits for a session's host to
// answer its socket. It is short on purpose, and much shorter than the wait
// `afk attach` uses: attach may be racing a host that is still starting, but
// the daemon only types into sessions that have been running for a while, so
// the host is up or it is not, and a pass must not sit on a dead socket.
const daemonSendWait = 2 * time.Second

// shipWiring is what the ship reaches the machine through, gathered so a test
// can hand shipDeps fakes and see where each call goes.
type shipWiring struct {
	// run runs git and gh. It must return what was printed even when the
	// command failed, and an error that knows its exit code (ship.RunFunc).
	run ship.RunFunc
	// health asks a deploy's health URL once.
	health func(url string) (bool, error)
	// sendInput types one line into a session's terminal.
	sendInput func(worktree, session, text string) error
	// listInbox lists the project's inbox items.
	listInbox func(root string) ([]inbox.Item, error)
	// afterMerge runs a project's after-merge command in dir, with its output
	// going to logPath, and waits for it (ship.RunAfterMerge). It is a field
	// so a test sees which folder it was given and runs no shell.
	afterMerge func(dir, command, logPath string, timeout time.Duration) (exitCode int, timedOut bool, err error)
	now        func() time.Time
}

// daemonShip is the loop's Deps.Ship: read the project's [ship] table, then
// let ship.Advance take one step. Every rule about that step is in
// internal/ship.
//
// A project config that cannot be read is a wait, with the error as the
// reason, and nothing else: no step is taken on settings nobody can see, and
// it is not a park either, because fixing the file is all it takes and the
// next pass reads it again.
func daemonShip(loadConfig func(root string) (projectconfig.Config, error), w shipWiring) func(daemon.ShipRequest) ship.Outcome {
	return func(req daemon.ShipRequest) ship.Outcome {
		cfg, err := loadConfig(req.Project.Root)
		if err != nil {
			return ship.Outcome{
				Kind:     ship.OutcomeWait,
				Reason:   fmt.Sprintf("the project config could not be read, so nothing was done: %v. Fix the file and the ship carries on", err),
				Progress: req.Progress,
			}
		}
		return ship.Advance(shipRequest(req, cfg.Ship), shipDeps(req, w))
	}
}

// shipRequest turns what the loop knows about a ship into ship's own request.
//
// The base is the project's `[ship] base` when it is set, and otherwise the
// branch the session was cut from. With no session on disk and no base set
// there is none, and both are passed on empty: ship has a rule for that (it
// parks and asks for a base), and guessing one here would open a pull request
// against the wrong branch.
func shipRequest(req daemon.ShipRequest, cfg projectconfig.Ship) ship.Request {
	if cfg.Base == "" {
		cfg.Base = req.Base
	}
	r := ship.Request{
		Task:     req.Entry.ID,
		Session:  req.Entry.Session,
		Branch:   req.Entry.Branch,
		Base:     req.Base,
		Progress: req.Progress,
		Config:   cfg,
	}
	if req.Worktree != "" {
		r.SessionDir = filepath.Join(req.Worktree, ".afk", req.Entry.Session)
	}
	return r
}

// shipDeps builds everything one step of a ship touches. Each field is one
// call into the package that owns the work; a test checks none is left nil,
// because ship.Advance calls them without asking.
//
// The session's worktree may be gone: it is cleaned up before the ledger says
// done, and a daemon killed between the two comes back to a shipping task
// with nothing on disk. Git is then built over the main checkout alone (every
// command but the "is the worktree clean" read runs there anyway), and gh
// runs in the main checkout, because it finds the GitHub repository from the
// folder it is run in and any checkout of the repository will do.
//
// The after-merge command is the opposite case: it runs in the main checkout
// and nowhere else, whether or not the worktree is still there. It is the
// project's own "install what was just merged" step (PLAN T2.2a), the merge
// is on the base branch, and the main checkout is where the base branch is
// checked out and was just brought up to date. The session's worktree holds
// the task's branch, which by then is merged and about to be removed. Its run
// record and its log are kept in the main checkout's .afk/build for the same
// reason the stop marker is: they have to outlive the session.
func shipDeps(req daemon.ShipRequest, w shipWiring) ship.Deps {
	root := req.Project.Root
	ghDir := req.Worktree
	if ghDir == "" {
		ghDir = root
	}
	stop := req.Stop
	if stop == nil {
		stop = func(task, reason string) error { return daemonstate.Stop(root, task, reason, w.now()) }
	}
	return ship.Deps{
		Git:    ship.NewGit(w.run, root, req.Worktree),
		GH:     ship.NewGH(w.run, ghDir),
		Health: w.health,
		Prompt: func(text string) error {
			if req.Worktree == "" {
				return fmt.Errorf("session %s is not on disk, so there is no terminal to type into", req.Entry.Session)
			}
			return w.sendInput(req.Worktree, req.Entry.Session, text)
		},
		WriteFile: writeFileAtomic,
		ReadFile:  os.ReadFile,
		EnvAnswered: func(variable string) bool {
			// An inbox that cannot be listed is "not answered": the merge
			// waits rather than going through on a guess.
			items, err := w.listInbox(root)
			return err == nil && envAnswered(items, req.Entry.ID, variable)
		},
		Stop:             stop,
		StopMarker:       func() bool { return daemonstate.Stopped(root) },
		LastPoll:         func() time.Time { return daemonstate.LastShipPoll(root) },
		MarkPoll:         func(t time.Time) error { return daemonstate.MarkShipPoll(root, t) },
		ReadFailingSince: func() time.Time { return daemonstate.ShipPollFailingSince(root) },
		MarkReadFailure:  func(t time.Time) error { return daemonstate.MarkShipPollFailure(root, t) },
		ClearReadFailure: func() error { return daemonstate.ClearShipPollFailure(root) },
		Now:              w.now,
		Save:             req.Save,
		AfterMergeRecord: func() (string, string, ship.AfterMergeRun, bool) {
			rec, ok := daemonstate.ReadAfterMerge(root)
			if !ok {
				return "", "", ship.AfterMergeRun{}, false
			}
			return rec.Task, rec.MergedSHA, afterMergeRun(rec), true
		},
		MarkAfterMerge: func(task, mergedSHA string, run ship.AfterMergeRun, startedAt time.Time) error {
			return daemonstate.WriteAfterMerge(root, afterMergeRecord(task, mergedSHA, run, startedAt))
		},
		RunAfterMerge: func(command string, timeout time.Duration) (int, bool, error) {
			return w.afterMerge(root, command, daemonstate.AfterMergeLogPath(root), timeout)
		},
		AfterMergeTail: func() string {
			return ship.TailFile(daemonstate.AfterMergeLogPath(root), afterMergeTailLines, afterMergeTailBytes)
		},
	}
}

// How much of the after-merge command's output goes into the inbox item when
// the command fails. The item is read on a phone, and the whole output is in
// the log file the item names; the last lines are where a failing build says
// what went wrong.
const (
	afterMergeTailLines = 20
	afterMergeTailBytes = 2000
)

// afterMergeRun turns the run record on disk into what ship reads. Pure, so
// each shape of record is a table row.
//
// A run counts as finished only when its finish time can be read. An empty
// finished_at is the start mark, written before the command starts. One that
// is there but does not parse is treated the same way, and the exit code
// beside it is not believed: a record damaged that far cannot be trusted to
// say the command succeeded, and "started, not known to have finished" is the
// reading that stops the line and brings the owner, where "finished with exit
// 0" would carry on to the deploy check on the word of a broken file.
func afterMergeRun(rec daemonstate.AfterMerge) ship.AfterMergeRun {
	if rec.FinishedAt == "" {
		return ship.AfterMergeRun{}
	}
	at, err := time.Parse(time.RFC3339, rec.FinishedAt)
	if err != nil {
		return ship.AfterMergeRun{}
	}
	return ship.AfterMergeRun{Finished: true, TimedOut: rec.TimedOut, ExitCode: rec.ExitCode, FinishedAt: at}
}

// afterMergeRecord is the other direction: what ship knows about a run, as
// the record that is written. Pure.
//
// A run that has not finished is written with no finish time, exit code 0 and
// timed_out false whatever the caller passed, so a start mark always looks
// the same and finished_at alone says which of the two a record is.
func afterMergeRecord(task, mergedSHA string, run ship.AfterMergeRun, startedAt time.Time) daemonstate.AfterMerge {
	rec := daemonstate.AfterMerge{
		Task:      task,
		MergedSHA: mergedSHA,
		StartedAt: startedAt.UTC().Format(time.RFC3339),
	}
	if run.Finished {
		rec.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339)
		rec.ExitCode = run.ExitCode
		rec.TimedOut = run.TimedOut
	}
	return rec
}

// envAnswered reports whether the owner has answered the item that asked them
// to set this environment variable before this task merges. Pure, so each way
// of not matching is a table row.
//
// The variable has to be a whole word of the title. The title is ship's
// ("Set {variable} before {task} merges"), and a plain substring test would
// let the answer about MY_API_KEY also release a merge that waits on API_KEY.
func envAnswered(items []inbox.Item, task, variable string) bool {
	if variable == "" {
		return false
	}
	for _, it := range items {
		if it.Kind != ship.ItemEnvVar || it.Task != task || it.Open() {
			continue
		}
		for _, word := range strings.Fields(it.Title) {
			if word == variable {
				return true
			}
		}
	}
	return false
}

// writeFileAtomic writes a file so that a reader sees the old contents or the
// new, never half of the new: a temp file beside it, then a rename. The
// hand-back file it is used for is read by an agent that may look the moment
// the prompt arrives, and by the next pass to learn whether the hand-back
// already happened, and a daemon can be killed between any two writes.
//
// The folder is not created. It is the session's own, and one that is missing
// means the session is gone; making it again would leave a stray folder
// behind for a file nobody will read.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		// CreateTemp makes the file 0600; the session's other files are 0644.
		werr = os.Chmod(name, 0o644)
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		_ = os.Remove(name)
	}
	return werr
}

// daemonCleanOptions is how a shipped task's session is cleaned: the branch
// is always deleted, and deleted on GitHub's word when the ledger holds a
// merge commit.
//
// ConfirmedUnmerged looks wrong for a branch that has just been merged. The
// reason is what session.Merged checks: whether the branch's tip is an
// ancestor of the local base. After a squash or a rebase merge it never is
// (the base holds new commits with the same changes, not the branch's own),
// so that check alone would keep every such branch for ever. A merged_sha in
// the ledger is GitHub having said the pull request merged, which is the
// better evidence. Without one the local check decides, as it does for
// `afk clean --delete-branch` with nobody to ask.
func daemonCleanOptions(e ledger.Entry) session.CleanOptions {
	return session.CleanOptions{DeleteBranch: true, ConfirmedUnmerged: e.MergedSHA != ""}
}

// daemonClean is the loop's Deps.Clean: find the session the way `afk clean`
// does and clean it up. No session of that name is success, not an error: the
// loop cleans before it marks the task done, so a daemon killed between the
// two asks again for a session that is already gone.
//
// The ledger is read here because the loop hands over only the session's
// name. One that cannot be read just means the cautious options.
func daemonClean(cd session.CleanDeps, loadLedger func(root string) (ledger.Ledger, error)) func(daemon.Project, string) error {
	return func(p daemon.Project, name string) error {
		sessions, err := session.Discover(p.Root, cd)
		if err != nil {
			return err
		}
		target := findCleanTarget(sessions, name)
		if target == nil {
			return nil
		}
		var entry ledger.Entry
		if l, err := loadLedger(p.Root); err == nil {
			for _, e := range l.Tasks {
				if e.Session == name {
					entry = e
					break
				}
			}
		}
		_, err = session.CleanSessionWithOptions(p.Root, *target, cd, daemonCleanOptions(entry))
		return err
	}
}

// --- afk daemon status ---

// daemonStatus is everything `afk daemon status` prints, gathered first so
// the rendering is a pure function of it.
type daemonStatus struct {
	ConfigPath string
	Now        time.Time
	Loc        *time.Location
	// HasMachine is whether a daemon has ever recorded itself on this
	// machine; PID and StartedAt are that record. Running is whether that
	// process id is alive now.
	HasMachine bool
	PID        int
	StartedAt  string
	Running    bool
	// BinaryUnknown is that record saying the daemon could not read its own
	// program file when it started. It is only worth a line while that daemon
	// is running: the next start writes the record afresh.
	BinaryUnknown bool
	// Usage is the governor's decision as usage.json holds it, or nil when it
	// holds none. It is one for the machine, not one per project.
	Usage    *usagestate.Governor
	Projects []daemonProjectStatus
}

// daemonProjectStatus is one project's block.
type daemonProjectStatus struct {
	Name, Root string
	// Missing: there is no folder at Root. Nothing else is read then.
	Missing bool
	Paused  bool
	// Stopped: the line's stop marker is there. StopTask and StopReason are
	// what it says, both empty when the marker cannot be read.
	Stopped              bool
	StopTask, StopReason string
	HasState             bool
	State                daemonstate.State
	// Counts is how many recorded tasks are in each status.
	Counts map[ledger.Status]int
	// LedgerProblem is set, with what to do, when the ledger cannot be used.
	LedgerProblem string
}

// runDaemonStatus implements `afk daemon status`. It only reads, and exits 0
// whenever it could print the report: a project in trouble is a line in the
// report, not a failed command.
func runDaemonStatus(stdout, stderr io.Writer, env daemonEnv) int {
	cfg, err := daemonconfig.Load(env.config)
	if err != nil {
		fmt.Fprintf(stderr, "afk daemon status: %v\n", err)
		return 1
	}
	in := daemonStatus{ConfigPath: daemonconfig.Path(env.config), Now: env.now(), Loc: env.loc}
	// The stored decision is shown as it is; an unreadable usage.json is
	// simply no line.
	if f, ok := usagestate.Read(env.state); ok {
		in.Usage = f.Governor
	}
	if dir, err := usagestate.Dir(env.state); err == nil {
		in.PID, in.StartedAt, in.HasMachine = daemonstate.ReadMachine(dir)
		in.Running = in.HasMachine && env.alive(in.PID)
		in.BinaryUnknown = daemonstate.BinaryUnknown(dir)
	}
	for _, p := range cfg.Projects {
		// The same resolution the daemon does, so status reads the files
		// the daemon writes even when the path names a session's worktree.
		ps := daemonProjectStatus{Name: p.Name, Root: mainCheckout(p.Path, env.gitCommonDir(p.Path))}
		// Checked here because the daemon writes nothing under a folder that
		// is not there, so there is no state file to say so.
		if !env.isDir(ps.Root) {
			ps.Missing = true
			in.Projects = append(in.Projects, ps)
			continue
		}
		ps.Paused = daemonstate.Paused(ps.Root)
		ps.StopTask, ps.StopReason, ps.Stopped = daemonstate.ReadStop(ps.Root)
		ps.State, ps.HasState = daemonstate.ReadState(ps.Root)
		l, err := ledger.Load(ps.Root)
		switch {
		case errors.Is(err, ledger.ErrCorrupt):
			ps.LedgerProblem = fmt.Sprintf("%v. Fix or delete %s (deleting it forgets every recorded task)", err, ledger.Path(ps.Root))
		case err != nil:
			ps.LedgerProblem = fmt.Sprintf("cannot read the task ledger %s: %v. Check the file's permissions", ledger.Path(ps.Root), err)
		default:
			ps.Counts = map[ledger.Status]int{}
			for _, e := range l.Tasks {
				ps.Counts[e.Status]++
			}
		}
		in.Projects = append(in.Projects, ps)
	}
	fmt.Fprint(stdout, renderDaemonStatus(in))
	return 0
}

// daemonKindText says in plain words what a state kind means. The kinds are
// the loop's own vocabulary (internal/daemon); an unknown one is shown as it
// is, so a newer daemon's state still reads under an older binary.
func daemonKindText(kind string) string {
	switch kind {
	case string(daemon.ActLaunch):
		return "started a task"
	case string(daemon.ActCreate):
		return "started the session for a running task again"
	case string(daemon.ActWait):
		return "waiting"
	case string(daemon.ActIdle):
		return "nothing to start"
	case string(daemon.ActShip):
		return "a task is finished and waiting to be shipped"
	case string(daemon.ActFail):
		return "a task failed"
	case string(daemon.ActPark):
		return "a task is parked until you answer its inbox items"
	case string(daemon.ActUnpark):
		return "a parked task is running again"
	case string(daemon.ActStall):
		return "a session is asking something at its terminal"
	case string(daemon.ActHostGone):
		return "a session has stopped running"
	case string(daemon.ActStop):
		return "stopped, because the project cannot be run"
	case daemon.KindStopped:
		return "stopped, because the project folder was not found"
	case daemon.KindBlocked:
		return "a task is ready, but this machine cannot start agents"
	case string(daemon.ActShipStep):
		return "shipping a finished task"
	case string(daemon.ActOwner):
		return "the next task is yours to do, and is waiting in your inbox"
	case string(daemon.ActOwnerDone):
		return "you confirmed your task, so it is marked done"
	case string(daemon.ActQuestionHold):
		return "a task is waiting for you to answer an open question from the plan"
	case string(daemon.ActQuestionAnswered):
		return "an open question was answered, so the task it held can start"
	case string(daemon.ActNoLedger):
		return "nothing is started, because this project has no task ledger yet"
	case daemon.KindLineStopped:
		return "the line is stopped, so no new task will start"
	case string(daemon.ActRestart):
		return "restarting a session that stopped answering"
	case string(daemon.ActRelaunch):
		return "starting a stopped session again"
	case string(daemon.ActContinue):
		return "telling a restarted session to carry on"
	case string(daemon.ActGiveUp):
		return "a session could not be recovered; waiting for you"
	case string(daemon.ActUsageWait):
		return "nothing new is started, because of the usage limits"
	case string(daemon.ActLoginWait):
		return "nothing new is started, because the agent is logged out"
	case string(daemon.ActUsagePause):
		return "stopping a session because of the usage limits"
	case string(daemon.ActUsagePaused):
		return "a session is paused because of the usage limits"
	case string(daemon.ActUsageStuck):
		return "a session that had to pause for usage would not stop; waiting for you"
	case string(daemon.ActUsageResume):
		return "starting a session that was paused for usage again"
	case string(daemon.ActWake):
		return "waking a session whose agent stopped working"
	case string(daemon.ActWakeGiveUp):
		return "a session kept failing after being woken; waiting for you"
	}
	return kind
}

// daemonTaskRunning reports whether a state describes a task whose session is
// at work, which is when "how long has it been like this" is worth a line.
func daemonTaskRunning(st daemonstate.State) bool {
	if st.Session == "" {
		return false
	}
	switch st.Kind {
	case string(daemon.ActLaunch), string(daemon.ActCreate), string(daemon.ActWait), string(daemon.ActUnpark),
		string(daemon.ActRestart), string(daemon.ActRelaunch), string(daemon.ActContinue):
		return true
	case string(daemon.ActWake):
		// The session was just sent its resume command, like ActContinue.
		return true
	case string(daemon.ActGiveUp), string(daemon.ActWakeGiveUp):
		// Parked, like ActPark: the task waits for the owner, not the session.
		return false
	case string(daemon.ActShipStep), daemon.KindLineStopped:
		// A shipping task and one that stopped the line (a deploy that did
		// not come up, or a failed after-merge command) both name a session,
		// but its work is finished: the wait is on GitHub, the deploy or the
		// owner, not on the session.
		return false
	case string(daemon.ActOwner), string(daemon.ActOwnerDone), string(daemon.ActNoLedger),
		string(daemon.ActQuestionHold), string(daemon.ActQuestionAnswered):
		// These never have a session; listed so every kind the loop gained
		// with shipping is answered here on purpose.
		return false
	case string(daemon.ActUsageResume):
		// The paused session is being started again, like ActRelaunch.
		return true
	case string(daemon.ActUsageWait), string(daemon.ActLoginWait),
		string(daemon.ActUsagePause), string(daemon.ActUsagePaused), string(daemon.ActUsageStuck):
		// Held back or paused for usage: a session may be named, but its
		// agent is stopped or was never started, so a running time would read
		// as progress that is not happening.
		return false
	}
	return false
}

// daemonStateChurns reports whether a state's time is rewritten while nothing
// has really changed. A ship's reason alternates between "waiting a moment
// before asking GitHub again" and what it is waiting for, and each change
// rewrites the state file, so its time is the last poll and not when the
// shipping began. Showing that as "Since" would say a ship that has waited an
// hour on CI began seconds ago.
//
// The six usage and login kinds do not churn, and are listed so that is a
// decision and not an oversight: their reasons are fixed sentences with no
// percentage or age in them (the loop keeps the governor's own sentence out
// of its state for exactly this), so the file is rewritten only when the kind
// or the wake time really changes and its time is when the wait began.
func daemonStateChurns(st daemonstate.State) bool {
	switch st.Kind {
	case string(daemon.ActShipStep):
		return true
	case string(daemon.ActUsageWait), string(daemon.ActLoginWait), string(daemon.ActUsagePause),
		string(daemon.ActUsagePaused), string(daemon.ActUsageStuck), string(daemon.ActUsageResume):
		return false
	}
	return false
}

// agoText says how long ago something was, for a report read by a person.
func agoText(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	return untilText(d)
}

// stoppedLineText is the line of the report for a project whose line is
// stopped: after which task, why, and the command that clears it. It reads
// the stop marker's own words and not the daemon's state file, so it is right
// even when the daemon has not looked since the stop, or is not running.
//
// A marker that cannot be read still stops the line (existence is the
// signal), so it gets the same line without the two things it could not say.
func stoppedLineText(p daemonProjectStatus) string {
	s := "Stopped: the line is stopped"
	if p.StopTask != "" {
		s += " after " + p.StopTask
	}
	if why := strings.TrimRight(strings.TrimSpace(p.StopReason), "."); why != "" {
		s += ": " + why
	}
	return s + fmt.Sprintf(". No new task will start until you run \"afk resume %s\", which clears the stop.", p.Name)
}

// renderDaemonStatus builds the whole report. Pure: every time, path and
// file it mentions was read by the caller.
//
// The state file's time is when the project entered its current state, not
// when it was last looked at (the daemon rewrites the file only on a
// change), which is why it can be shown as "since".
func renderDaemonStatus(in daemonStatus) string {
	loc := in.Loc
	if loc == nil {
		loc = time.UTC
	}
	when := func(s string) (text string, t time.Time, ok bool) {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return "", time.Time{}, false
		}
		return t.In(loc).Format("Mon 2 Jan 15:04"), t, true
	}

	var b strings.Builder
	started, _, startedOK := when(in.StartedAt)
	switch {
	case in.Running && startedOK:
		fmt.Fprintf(&b, "The afk daemon is running (pid %d, started %s).\n", in.PID, started)
	case in.Running:
		fmt.Fprintf(&b, "The afk daemon is running (pid %d).\n", in.PID)
	case in.HasMachine && startedOK:
		fmt.Fprintf(&b, "The afk daemon is not running. It was last started %s. What follows is how it left things.\n", started)
	case in.HasMachine:
		b.WriteString("The afk daemon is not running. What follows is how it left things.\n")
	default:
		b.WriteString("The afk daemon is not running, and has not been started on this machine yet.\n")
	}
	// Only for a daemon that is running. The note is about that process, and
	// a stopped one's record would tell the owner to restart something that
	// is not there; the next start looks again and writes its own answer.
	if in.Running && in.BinaryUnknown {
		b.WriteString(binaryUnknownText + "\n")
	}
	// Machine-wide, so it comes before the projects: every one of them spends
	// from the same account.
	if line := usageStatusLine(in.Usage, loc); line != "" {
		b.WriteString(line + "\n")
	}

	if len(in.Projects) == 0 {
		b.WriteString("\nNo projects are configured, so there is nothing to build.\n")
		b.WriteString(addProjectHelp(in.ConfigPath))
		return b.String()
	}

	for _, p := range in.Projects {
		fmt.Fprintf(&b, "\n%s (%s)\n", p.Name, p.Root)
		if p.Missing {
			fmt.Fprintf(&b, "  The project folder does not exist. Check its path in %s.\n", in.ConfigPath)
			continue
		}
		if p.Paused {
			fmt.Fprintf(&b, "  Paused: no new task will start until you run \"afk resume %s\". A task that is running carries on.\n", p.Name)
		}
		if p.Stopped {
			b.WriteString("  " + stoppedLineText(p) + "\n")
		}
		if !p.HasState {
			b.WriteString("  The daemon has not looked at this project yet.\n")
		} else {
			st := p.State
			// A state file outlives the daemon that wrote it. With no
			// daemon running nothing is being done, so the same line is
			// labelled as the last thing that was, and it may no longer
			// match the pause flag or the ledger read just now.
			label := "Doing:  "
			if !in.Running {
				label = "Last:   "
			}
			fmt.Fprintf(&b, "  %s %s\n", label, daemonKindText(st.Kind))
			switch {
			case st.Task != "" && st.Session != "":
				fmt.Fprintf(&b, "  Task:    %s, in session %s\n", st.Task, st.Session)
			case st.Task != "":
				fmt.Fprintf(&b, "  Task:    %s\n", st.Task)
			case st.Session != "":
				fmt.Fprintf(&b, "  Session: %s\n", st.Session)
			}
			if st.Reason != "" {
				fmt.Fprintf(&b, "  Why:     %s\n", st.Reason)
			}
			if text, at, ok := when(st.At); ok && daemonStateChurns(st) {
				fmt.Fprintf(&b, "  Updated: %s (%s ago)\n", text, agoText(in.Now.Sub(at)))
			} else if ok {
				ago := agoText(in.Now.Sub(at))
				fmt.Fprintf(&b, "  Since:   %s (%s ago)\n", text, ago)
				if in.Running && daemonTaskRunning(st) {
					fmt.Fprintf(&b, "  Running: session %s has been in this state for %s\n", st.Session, ago)
				}
			} else {
				b.WriteString("  Since:   unknown (the state file holds no readable time)\n")
			}
		}
		switch {
		case p.LedgerProblem != "":
			fmt.Fprintf(&b, "  Problem: %s\n", p.LedgerProblem)
		default:
			var parts []string
			for _, s := range ledger.Statuses() {
				if n := p.Counts[s]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", n, s))
				}
			}
			if len(parts) == 0 {
				b.WriteString("  Tasks:   none recorded yet\n")
			} else {
				fmt.Fprintf(&b, "  Tasks:   %s\n", strings.Join(parts, ", "))
			}
		}
	}
	return b.String()
}
