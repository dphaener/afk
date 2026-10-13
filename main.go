package main

import (
	"fmt"
	"os"
)

// subcommands is the single source of truth for what routeArgs accepts, so a
// name can never be routable-but-undispatchable. `_host` is deliberately here
// but absent from printUsage: it is the internal session-host entry point.
var subcommands = map[string]func([]string){
	"new":     cmdNew,
	"dash":    cmdDash,
	"clean":   cmdClean,
	"attach":  cmdAttach,
	"notify":  cmdNotify,
	"plugin":  cmdPlugin,
	"plan":    cmdPlan,
	"tasks":   cmdTasks,
	"next":    cmdNext,
	"ledger":  cmdLedger,
	"profile": cmdProfile,
	"usage":   cmdUsage,
	"inbox":   cmdInbox,
	"tokens":  cmdTokens,
	"daemon":  cmdDaemon,
	"pause":   cmdPause,
	"resume":  cmdResume,
	"_host":   cmdHost,
}

type routeKind int

const (
	// routeInvalid is the zero value deliberately: routeArgs never returns it, so
	// a zero-valued route — or a kind added later that main's switch does not yet
	// handle — hits main's default case and fails closed. If routeDash were the
	// zero value instead, either mistake would silently launch the full-screen
	// dashboard, which is the worst available failure since it takes over the
	// terminal.
	routeInvalid routeKind = iota
	routeDash
	routeUsage
	// routeUsageError exits 1 with no diagnostic line; routeUsage exits 0.
	routeUsageError
	// routeUnknownSubcommand prints "Unknown subcommand: <route.name>" before
	// usage, then exits 1. It is a kind of its own rather than routeUsageError
	// carrying a non-empty name because an empty name is a legitimate value
	// here: `afk ""` names the empty-string subcommand and has always printed
	// the diagnostic with nothing after the colon. A "name is set" sentinel
	// cannot tell that apart from "there is no name".
	routeUnknownSubcommand
	routeSubcommand
)

func (k routeKind) String() string {
	switch k {
	case routeInvalid:
		return "routeInvalid"
	case routeDash:
		return "routeDash"
	case routeUsage:
		return "routeUsage"
	case routeUsageError:
		return "routeUsageError"
	case routeUnknownSubcommand:
		return "routeUnknownSubcommand"
	case routeSubcommand:
		return "routeSubcommand"
	}
	return fmt.Sprintf("routeKind(%d)", int(k))
}

// route is a dispatch decision. name is set for routeSubcommand and
// routeUnknownSubcommand — in both cases it is os.Args[1] verbatim, so it may be
// the empty string. args is what the subcommand receives (os.Args[2:]) and is
// meaningful only for routeSubcommand.
type route struct {
	kind routeKind
	name string
	args []string
}

// routeArgs resolves args (os.Args[1:]) plus the interactivity of stdout into a
// single dispatch decision. Pure and I/O-free — main does the isTerminal call
// and passes the answer in, the same split shouldHandOffToDash uses in new.go —
// so the whole matrix is table-testable with no TTY.
//
// A bare `afk` is gated on STDOUT, not stdin. Bubble Tea only ever inspects
// stdin: in bubbletea v2.0.9 tea.go:1015-1026 (`Run`) it takes os.Stdin and,
// if that is not a terminal, calls OpenTTY() and reads /dev/tty instead.
// stdout is never consulted, so `afk | cat` from an
// interactive shell does NOT fail to start — it starts, writes its alt-screen
// escapes into the pipe, and then sits in raw mode with no visible UI until the
// user interrupts it. (SIGINT does still quit and restore ECHO; the problem is
// an invisible program, not an unkillable one.) Gating on stdout is what
// prevents that: stdout is what decides whether the rendered dashboard can be
// seen at all. With stdout redirected we print usage and exit 1, which is what a
// zero-argument `afk` has always done.
//
// Only a strictly empty argument list is the default action. Anything else,
// including a leading flag, goes through the subcommand table — so
// `afk --interval 5s` is an unknown-subcommand error, not implicit
// flag-forwarding into `afk dash`.
func routeArgs(args []string, stdoutTTY bool) route {
	if len(args) == 0 {
		if !stdoutTTY {
			return route{kind: routeUsageError}
		}
		return route{kind: routeDash}
	}

	name := args[0]
	if _, ok := subcommands[name]; ok {
		return route{kind: routeSubcommand, name: name, args: args[1:]}
	}
	if name == "-h" || name == "--help" || name == "help" {
		return route{kind: routeUsage}
	}
	return route{kind: routeUnknownSubcommand, name: name}
}

func main() {
	// os.Args can be empty when the process is exec'd with an empty argv, so
	// slice defensively rather than indexing: an empty argv is the same
	// zero-argument case, not a panic.
	var args []string
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}

	// isTerminal(os.Stdout), never os.Stdin: stdin is the fd Bubble Tea already
	// inspects and works around by reopening /dev/tty, so gating on it would
	// reject `afk </dev/null` (which works) while still allowing `afk | cat`
	// (which is the invisible-raw-mode hang). See routeArgs' doc comment.
	switch r := routeArgs(args, isTerminal(os.Stdout)); r.kind {
	case routeDash:
		cmdDash(nil)
	case routeUsage:
		printUsage()
	case routeUsageError:
		printUsage()
		os.Exit(1)
	case routeUnknownSubcommand:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", r.name)
		printUsage()
		os.Exit(1)
	case routeSubcommand:
		subcommands[r.name](r.args)
	default:
		// Fail closed. Launching the dashboard here would seize the terminal on
		// what is by definition a routing bug, so an unhandled kind exits like a
		// usage error instead.
		fmt.Fprintf(os.Stderr, "afk: internal error: unhandled route %v\n", r.kind)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `afk CLI — orchestration toolchain

Usage:
  afk                     Launch the dashboard (same as 'afk dash')
  afk <subcommand> [options]

Subcommands:
  new <name>     Create a new afk session. On a TTY this hands off to the dashboard
                 with the session live in an embedded pane; pass --no-dash to skip the
                 dashboard and get a text pointer instead.
  dash           Launch the afk dashboard; keys: ↑/↓ move/preview, enter focus,
                 ctrl-\ unfocus/close pane, n new, c clean, q quit
  clean [name]   Clean afk artifacts (remove worktree and archive session data)
  attach <name>  Reattach to a running session (detach with ctrl-\, session keeps running)
  notify         Post a desktop notification for a session. Delivery is always
                 asynchronous, so this returns immediately; with alerter installed the
                 notification carries a Focus button that switches to the session's pane
  plugin install Install the afk plugin into Claude Code, Codex, and/or OpenCode;
                 asks which of the ones found on PATH unless --target is given
  plan           Plan a project with your agent: it asks what you want to build and
                 writes a first draft of the master plan. Needs a terminal; run it
                 again to come back. 'plan status' says which phase the session is
                 in, and 'plan advance' (run by the session) moves it on:
                 afk plan [--project DIR] [--provider NAME] [--dry-run]
                 On a project that has a plan, 'afk plan extend' adds a milestone,
                 'afk plan amend "<what>"' adds, splits or replaces one task and
                 'afk plan reconcile FILE' brings the plan in line with a document
  plan patch     Change a master plan by a guarded patch: the old text must be in
                 the plan exactly once, and nothing is written if the change would
                 add a checker error or alter a task that is running:
                 afk plan patch --old FILE --new FILE [--message TEXT]
                 afk plan insert (--after ID | --milestone N | --new-milestone) --entry FILE
                 afk plan depend TASK ON
                 afk plan next-id (--milestone N | --after ID)
                 afk plan next-followup [--project DIR]
  plan lint      Check a master plan for what the daemon would refuse or misread:
                 afk plan lint [--project DIR] [--json] [--summary] [FILE]
  plan check     Mark one task of a session's plan done or failed:
                 afk plan check <session> <task-id> done|failed [--reason TEXT]
  tasks          List the master plan's tasks with their status
  next           Show the task that would run next, or why none
  ledger set     Record a task's status by hand:
                 afk ledger set <task-id> <status> [--replaced-by IDS]
  profile        Keep a profile of how you like to build and host things, so plans
                 stop asking: 'profile init' writes a template, 'profile check' lists
                 what is wrong, and 'profile set' adds or changes one entry:
                 afk profile set <topic> <value> [--kind KIND] [--rule|--default] [--reason TEXT]
  usage          Show how much of the agent's usage windows is spent, when they
                 reset, and what the usage governor would do:
                 afk usage [--provider NAME] [--refresh]
  inbox          List the things that need you; 'inbox show <id>' shows one,
                 'inbox answer <id> <text>' answers it, and 'inbox add' (mostly run
                 by the agent) adds one and sends a push notification if set up
  tokens         Show how many tokens a task or session used:
                 afk tokens [--project PATH] [--record] [SESSION|TASK]
  daemon         Build the master plan's tasks unattended, one at a time, in the
                 projects listed in afk's machine config; 'daemon --once' makes
                 one pass and exits, 'daemon status' says what it is doing and why
  pause          Stop the daemon from starting new tasks in a project:
                 afk pause [project]
  resume         Let the daemon start new tasks in a project again:
                 afk resume [project]

Use 'afk <subcommand> --help' for subcommand options.

Dashboard options:
  afk dash --open <name>    Open that session's pane once it is running

Session model:
  Sessions are detached processes that survive dashboard exit. Both the dashboard's
  pane and 'attach' connect to a running session and render it in a box; 'ctrl-\'
  disconnects and leaves the session running. Only one client can be connected at a
  time; a new connection disconnects the previous one.
`)
}
