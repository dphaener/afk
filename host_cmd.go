package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/dphaener/afk/internal/host"
)

// cmdHost implements the hidden `afk _host` subcommand: the body of the
// detached host process spawned by `afk new` (W3.T1) to keep a session's
// PTY alive across `afk attach`/detach cycles. It is an internal
// implementation detail, never invoked directly by a user, so it is
// deliberately omitted from printUsage's subcommand list (see main.go).
func cmdHost(args []string) {
	fs, flags := newHostFlagSet(flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `afk _host — internal: run the detached session host

This subcommand is spawned by 'afk new'; it is not meant to be invoked
directly.

Usage:
  afk _host --name <name> --worktree <dir> --socket <path> --log <path> --meta <path> [--provider <name>] [--probe-unattached] [--stdio [--rex-session <id> --rex-block <id>]] -- <argv...>

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	cfg, err := flags.serveConfig(fs.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk _host: %v\n", err)
		fs.Usage()
		os.Exit(2)
	}

	if err := host.Serve(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "afk _host: %v\n", err)
		os.Exit(1)
	}
}

// hostFlags holds the parsed `_host` flags.
type hostFlags struct {
	name, worktree, socket, logPath, metaPath, provider string
	// probeUnattached is --probe-unattached: check the child for a wedge even
	// with no client attached. The spawner passes it for sessions the daemon
	// runs, which nobody attaches to; see host.ServeConfig.ProbeUnattached.
	probeUnattached bool
	// stdio is --stdio: serve the host's own terminal, which is a Rex block,
	// instead of a socket client. The rex* ids are the block the spawner
	// started this host in; the host records them and never calls Rex.
	stdio                                      bool
	rexServer, rexSession, rexWindow, rexBlock string
}

// newHostFlagSet declares the `_host` flags. It is split from cmdHost so tests
// can parse a real argument list with ContinueOnError instead of exiting.
func newHostFlagSet(errorHandling flag.ErrorHandling) (*flag.FlagSet, *hostFlags) {
	fs := flag.NewFlagSet("_host", errorHandling)
	f := &hostFlags{}
	fs.StringVar(&f.name, "name", "", "Session name (used for logging only)")
	fs.StringVar(&f.worktree, "worktree", "", "Child process working directory")
	fs.StringVar(&f.socket, "socket", "", "Unix socket path to listen on")
	fs.StringVar(&f.logPath, "log", "", "Host log file path")
	fs.StringVar(&f.metaPath, "meta", "", "host.json path")
	fs.StringVar(&f.provider, "provider", "", "Provider the child runs (empty means claude); selects the resume command on restart")
	fs.BoolVar(&f.probeUnattached, "probe-unattached", false, "Check the child for a wedge even while no client is attached (for sessions nobody watches)")
	fs.BoolVar(&f.stdio, "stdio", false, "Serve this process's own terminal (a Rex block) instead of a socket client")
	fs.StringVar(&f.rexServer, "rex-server", "", "Rex server the block belongs to (recorded in host.json; needs --stdio)")
	fs.StringVar(&f.rexSession, "rex-session", "", "Rex session id (recorded in host.json; needs --stdio and --rex-block)")
	fs.StringVar(&f.rexWindow, "rex-window", "", "Rex window id (recorded in host.json; needs --stdio)")
	fs.StringVar(&f.rexBlock, "rex-block", "", "Rex block id (recorded in host.json; needs --stdio and --rex-session)")
	return fs, f
}

// serveConfig builds the ServeConfig from the parsed flags and the child argv.
// Provider is applied here rather than through buildServeConfig's parameter
// list: it is optional, and an empty value is the claude default.
// ProbeUnattached is applied here for the same reason: it is optional, and
// false is what every session a person runs wants.
func (f *hostFlags) serveConfig(childArgv []string) (host.ServeConfig, error) {
	cfg, err := buildServeConfig(f.name, f.worktree, f.socket, f.logPath, f.metaPath, childArgv)
	if err != nil {
		return host.ServeConfig{}, err
	}
	cfg.Provider = f.provider
	cfg.ProbeUnattached = f.probeUnattached
	return applyStdioFlags(cfg, f)
}

// applyStdioFlags sets the stdio and Rex fields of cfg from the flags, or
// refuses a combination that cannot mean anything. It is a separate pure
// function, not more parameters on buildServeConfig, because those flags are
// optional and the five-string call is already made from several places.
// A Rex id without --stdio is refused because a host with a socket client
// owns its own terminal, so ids for a Rex block would be a false record. The
// session and block ids are refused unless both are given, because host.json
// needs the pair to find the block again.
func applyStdioFlags(cfg host.ServeConfig, f *hostFlags) (host.ServeConfig, error) {
	anyRex := f.rexServer != "" || f.rexSession != "" || f.rexWindow != "" || f.rexBlock != ""
	if anyRex && !f.stdio {
		return host.ServeConfig{}, errors.New("the Rex ids (--rex-server, --rex-session, --rex-window, --rex-block) only apply with --stdio")
	}
	if f.rexSession != "" && f.rexBlock == "" {
		return host.ServeConfig{}, errors.New("--rex-session needs --rex-block: give the id of the Rex block this host runs in")
	}
	if f.rexBlock != "" && f.rexSession == "" {
		return host.ServeConfig{}, errors.New("--rex-block needs --rex-session: give the id of the Rex session this host runs in")
	}
	cfg.Stdio = f.stdio
	if anyRex {
		cfg.Rex = &host.RexBlock{
			Server:    f.rexServer,
			SessionID: f.rexSession,
			WindowID:  f.rexWindow,
			BlockID:   f.rexBlock,
		}
	}
	return cfg, nil
}

// buildServeConfig maps the parsed `_host` flags and the remaining argv
// (everything after the `--` separator) onto a host.ServeConfig. It is pure
// and side-effect free — deeper validation (e.g. that every field is
// non-empty) is host.Serve's own responsibility (host.ServeConfig.validate)
// so this package does not maintain a second copy of that contract; this
// function only guards the one thing Serve cannot: that a child argv was
// supplied at all, which otherwise manifests as a confusing "-- was never
// seen" flag-parsing artifact rather than a clear error.
func buildServeConfig(name, worktree, socket, logPath, metaPath string, childArgv []string) (host.ServeConfig, error) {
	if len(childArgv) == 0 {
		return host.ServeConfig{}, errors.New("child argv is required after --")
	}
	return host.ServeConfig{
		Name:     name,
		Worktree: worktree,
		Socket:   socket,
		MetaPath: metaPath,
		LogPath:  logPath,
		Argv:     childArgv,
	}, nil
}
