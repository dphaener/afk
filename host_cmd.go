package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/rex"
	"github.com/dphaener/afk/internal/session"
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

With --stdio and no --rex-* flag, the Rex ids are read from REX_SERVER,
REX_SESSION and REX_BLOCK, which Rex sets for the command in each block.

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	cfg, err := flags.serveConfig(fs.Args(), os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk _host: %v\n", err)
		fs.Usage()
		os.Exit(2)
	}
	cfg.Window = hostWindowControl(cfg, os.Getenv, rex.NewCaller(rex.NewRun(rexWindowCallTimeout), "rex"), exec.LookPath)

	if err := host.Serve(cfg); err != nil {
		// A stdio host passes its child's status on: it is the Rex block's
		// command, and the block shows how that command ended. Nothing is
		// printed, because the child already said what it had to say on the
		// same screen.
		var ce *host.ChildExited
		if errors.As(err, &ce) {
			os.Exit(ce.Code)
		}
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

// serveConfig builds the ServeConfig from the parsed flags, the child argv
// and, for a stdio host, the Rex ids in the environment (getenv, a parameter
// so a test never reads the real one). Provider is applied here rather than
// through buildServeConfig's parameter list: it is optional, and an empty
// value is the claude default. ProbeUnattached is applied here for the same
// reason: it is optional, and false is what every session a person runs wants.
func (f *hostFlags) serveConfig(childArgv []string, getenv func(string) string) (host.ServeConfig, error) {
	cfg, err := buildServeConfig(f.name, f.worktree, f.socket, f.logPath, f.metaPath, childArgv)
	if err != nil {
		return host.ServeConfig{}, err
	}
	cfg.Provider = f.provider
	cfg.ProbeUnattached = f.probeUnattached
	cfg.Planning = f.name == session.PlanSessionName
	cfg, err = applyStdioFlags(cfg, f)
	if err != nil {
		return host.ServeConfig{}, err
	}
	return applyRexEnv(cfg, getenv), nil
}

// applyRexEnv fills a stdio host's Rex ids from the environment Rex gives
// every block's command (REX_SERVER, REX_SESSION, REX_BLOCK), when no --rex-*
// flag gave them. The spawner cannot pass the block id as a flag: a block's id
// exists only once its command has started (docs/PLAN.md, T17.8 assumption
// A4). It is all or nothing, never field by field, so host.json can never
// carry a session from one source and a block from the other. The ids are
// recorded as found, without the flags' pair check: they are a fact of the
// block the host already runs in, and refusing to start would leave a dead
// block instead of a session. Only a stdio host reads them: a detached host
// started from a shell inside Rex inherits the same variables, and they say
// nothing about it.
func applyRexEnv(cfg host.ServeConfig, getenv func(string) string) host.ServeConfig {
	if !cfg.Stdio || cfg.Rex != nil {
		return cfg
	}
	b := host.RexBlock{Server: getenv("REX_SERVER"), SessionID: getenv("REX_SESSION"), BlockID: getenv("REX_BLOCK")}
	if b != (host.RexBlock{}) {
		cfg.Rex = &b
	}
	return cfg
}

// rexWindowCallTimeout is the deadline on one Rex command the host runs for
// its window. The host gives up waiting after its own deadline of about two
// seconds anyway (internal/host's windowCallDeadline); this one makes the
// command itself end, so a Rex that hangs does not leave it running.
const rexWindowCallTimeout = 2 * time.Second

// hostWindowControl backs ServeConfig.Window with Rex for a stdio host that
// runs in a Rex block, which is what REX_BLOCK in its environment says, and
// returns nil (no window to keep up to date) otherwise: every afk-runtime
// host, and a stdio host run outside Rex. Like applyRexEnv it ignores the
// variable for a host that is not stdio, which inherits it from a Rex shell
// without running in that block.
func hostWindowControl(cfg host.ServeConfig, getenv func(string) string, c rex.Caller, lookPath func(string) (string, error)) host.WindowControl {
	if !cfg.Stdio || getenv("REX_BLOCK") == "" {
		return nil
	}
	w := &rexWindow{caller: c, lookPath: lookPath}
	if cfg.Rex != nil {
		w.sessionID, w.blockID, w.windowID = cfg.Rex.SessionID, cfg.Rex.BlockID, cfg.Rex.WindowID
	}
	return w
}

// rexWindow is host.WindowControl over internal/rex. The window id is found
// once, from session.list_blocks, and kept: a block never moves to another
// window on its own, and a Rex call per relabel would double the commands
// run. A --rex-window id, when the spawner gave one, is used as it is.
type rexWindow struct {
	caller             rex.Caller
	lookPath           func(string) (string, error)
	sessionID, blockID string

	mu       sync.Mutex
	windowID string
}

// reachable says, in plain words, when the rex command is not there at all.
// Checked first, so a host whose environment has a half set of ids still
// reports the cause a person can fix rather than an empty id.
func (w *rexWindow) reachable() error {
	if _, err := w.lookPath("rex"); err != nil {
		return errors.New("the rex command is not on PATH")
	}
	return nil
}

func (w *rexWindow) SetLabel(label string) error {
	if err := w.reachable(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), rexWindowCallTimeout)
	defer cancel()
	id, err := w.window(ctx)
	if err != nil {
		return err
	}
	return rex.SetWindowLabel(ctx, w.caller, w.sessionID, id, label)
}

// CloseSelf closes the host's own block, which ends the host's process with
// it; the host calls it as its last act. Closing the block rather than the
// window needs no window id, so it works even when list_blocks never
// answered, and Rex closes a window whose last block is gone.
func (w *rexWindow) CloseSelf() error {
	if err := w.reachable(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), rexWindowCallTimeout)
	defer cancel()
	return rex.CloseBlock(ctx, w.caller, w.sessionID, w.blockID)
}

// window returns the cached window id, finding it on first use. Only a
// success is cached, so a failed lookup is tried again on the next relabel.
func (w *rexWindow) window(ctx context.Context) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.windowID != "" {
		return w.windowID, nil
	}
	id, found, err := rex.WindowOfBlock(ctx, w.caller, w.sessionID, w.blockID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("Rex lists no window for block %s", w.blockID)
	}
	w.windowID = id
	return id, nil
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
