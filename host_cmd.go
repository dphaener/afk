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
  afk _host --name <name> --worktree <dir> --socket <path> --log <path> --meta <path> [--provider <name>] -- <argv...>

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
	return fs, f
}

// serveConfig builds the ServeConfig from the parsed flags and the child argv.
// Provider is applied here rather than through buildServeConfig's parameter
// list: it is optional, and an empty value is the claude default.
func (f *hostFlags) serveConfig(childArgv []string) (host.ServeConfig, error) {
	cfg, err := buildServeConfig(f.name, f.worktree, f.socket, f.logPath, f.metaPath, childArgv)
	if err != nil {
		return host.ServeConfig{}, err
	}
	cfg.Provider = f.provider
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
