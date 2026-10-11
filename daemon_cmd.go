package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/tokens"
	"github.com/dphaener/afk/internal/usagestate"
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
	newDeps func(p provider.Provider, providerName string, log func(format string, args ...any)) daemon.Deps
	// signalContext returns a context that ends when the process is asked to
	// stop.
	signalContext func() (context.Context, context.CancelFunc)
}

func realDaemonEnv() daemonEnv {
	// A failed UserHomeDir is passed on as "": the unattended preflight then
	// finds no settings file and fails with its manual steps, which is the
	// right answer for a machine whose home is unknown.
	home, _ := os.UserHomeDir()
	now := time.Now
	return daemonEnv{
		config:       daemonconfig.DefaultDeps(),
		state:        usagestate.DefaultDeps(),
		now:          now,
		loc:          time.Local,
		pid:          os.Getpid(),
		gitCommonDir: resolveGitCommonDirIn,
		isDir: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		},
		alive: host.Alive,
		newDeps: func(p provider.Provider, providerName string, log func(string, ...any)) daemon.Deps {
			return realDaemonDeps(p, providerName, home, now, os.Stderr, log)
		},
		signalContext: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		},
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
	deps := env.newDeps(prov, cfg.Provider, logf)
	settings := daemon.Settings{StallAfter: cfg.StallAfter}

	if *once {
		for i, st := range daemon.Once(projects, settings, deps) {
			fmt.Fprintln(stdout, daemonOnceLine(projects[i].Name, st))
		}
		return 0
	}

	ctx, stop := env.signalContext()
	defer stop()
	logf("afk daemon started (pid %d): %d project(s), looking every %s", env.pid, len(projects), cfg.Tick)
	_ = daemon.Run(ctx, projects, settings, cfg.Tick, deps) // Run only ever returns nil
	logf("afk daemon stopped. Every session is still running; the next start picks them up.")
	return 0
}

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
	n.deliver(p, url, msg)
}

// idle sends the one notification with no item behind it. Priority 3 is
// ntfy's default: the build has stopped moving, but nothing is on fire.
func (n daemonNotifier) idle(p daemon.Project, title, body string) {
	url, ok := n.address(p)
	if !ok {
		return
	}
	n.deliver(p, url, notify.NtfyMessage{Title: "afk: " + title, Body: body, Priority: 3})
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

// realDaemonDeps wires the loop to the real machine. Nothing here decides
// anything; each field is one call into the package that owns the work.
func realDaemonDeps(p provider.Provider, providerName, home string, now func() time.Time, logw io.Writer, logf func(string, ...any)) daemon.Deps {
	n := daemonNotifier{
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
	return daemon.Deps{
		Now: now,
		LoadBuild: func(root string) ([]masterplan.Task, string, ledger.Ledger, error) {
			bs, err := loadBuildState(root)
			return bs.Tasks, bs.PlanPath, bs.Ledger, err
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
		Notify:     n.item,
		NotifyIdle: n.idle,
		Log:        logf,
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
	Projects   []daemonProjectStatus
}

// daemonProjectStatus is one project's block.
type daemonProjectStatus struct {
	Name, Root string
	// Missing: there is no folder at Root. Nothing else is read then.
	Missing  bool
	Paused   bool
	HasState bool
	State    daemonstate.State
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
	if dir, err := usagestate.Dir(env.state); err == nil {
		in.PID, in.StartedAt, in.HasMachine = daemonstate.ReadMachine(dir)
		in.Running = in.HasMachine && env.alive(in.PID)
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
	case string(daemon.ActLaunch), string(daemon.ActCreate), string(daemon.ActWait), string(daemon.ActUnpark):
		return true
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
			if text, at, ok := when(st.At); ok {
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
