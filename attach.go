package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/rex"
	"github.com/dphaener/afk/internal/ui"
)

// attachWait is how long `afk attach` retries dialing the host's socket
// before giving up. This is also the window that lets the dial recover from a
// stale-but-not-yet-cleaned-up host.json whose PID has been recycled — see
// host.ErrHostNotReachable's doc comment.
const attachWait = 5 * time.Second

// metaReadWait bounds a short, self-healing retry against host.ReadMeta
// returning "no host.json yet" for a host that has only just been spawned
// and has not finished its startup write. Only the socket dial got a retry
// window before this — a caller racing a freshly-spawned host (e.g. the
// dashboard's restart flow) could fail here even though the host is about
// to be fully up. Deliberately far smaller than attachWait: Serve writes
// host.json within milliseconds of starting, long before the dial even
// needs to answer, so this is a narrow race, not a slow one.
const metaReadWait = 500 * time.Millisecond

// cmdAttach implements `afk attach`: reattach to a session and render it in
// an embedded pane (internal/ui.AttachPane) rather than handing the raw
// terminal to the host. That is what makes a session look the same here as in
// the dashboard. It resolves the session's worktree (host.json lives at
// `<worktree>/.afk/<name>/host.json`, and worktrees are NOT necessarily under
// the current working directory) and delegates everything else.
func cmdAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	proj := addProjectFlag(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `afk attach — reattach to a running session

Usage:
  afk attach [options] <name>

Detach with ctrl-\ (the session keeps running in the background). If another
client attaches to the same session, this client is disconnected.

  ctrl-g     hand the mouse to the terminal (native selection), again to return it

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "afk attach: exactly one session <name> is required")
		fs.Usage()
		os.Exit(2)
	}
	name := fs.Arg(0)

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk attach: cannot determine working directory: %v\n", err)
		os.Exit(1)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk attach: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	repo, err := resolveProject(*proj, cwd, home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk attach: %v\n", err)
		os.Exit(1)
	}

	msg, code, isError := runAttach(repo, name, attachWait)
	if isError {
		fmt.Fprintln(os.Stderr, msg)
	} else {
		// Printed only after ui.AttachPane has returned — i.e. after the
		// terminal has already been restored out of raw/alt-screen mode.
		// A message printed before that point would be clobbered by the
		// restore, the same caveat the codebase documents for
		// cleanSelected's own terminal handling.
		fmt.Println(msg)
	}
	os.Exit(code)
}

// runAttach performs the session lookup and the attach itself for name from
// repo, returning the human-facing message, the process exit code, and
// whether the message is an error (destined for stderr) rather than a
// normal detach status line (destined for stdout). It is split out of
// cmdAttach — which does nothing but flag parsing and the final
// print+os.Exit — purely so the exit-code contract is unit-testable without
// special-casing os.Exit in a test process.
func runAttach(repo, name string, wait time.Duration) (msg string, code int, isError bool) {
	return runAttachWith(repo, name, wait, os.Getenv, func() rex.Caller {
		return rex.NewCaller(rex.NewRun(rex.DefaultTimeout), "rex")
	})
}

// runAttachWith is runAttach with its two outside facts injected: the
// environment (is the caller inside Rex) and the Rex caller. The caller is a
// constructor so that an afk-runtime attach never builds one, and a machine
// without Rex is never asked about it.
func runAttachWith(repo, name string, wait time.Duration, getenv func(string) string, newCaller func() rex.Caller) (msg string, code int, isError bool) {
	worktree, ok, err := resolveSessionWorktree(repo, name)
	if err != nil {
		return fmt.Sprintf("afk attach: %v", err), 1, true
	}
	if !ok {
		return fmt.Sprintf("afk attach: session %q is not running (no such session found; run `afk clean` to list sessions)", name),
			host.ExitNotRunning, true
	}

	meta, err := readMetaWithRetry(worktree, name, metaReadWait)
	if err != nil {
		return fmt.Sprintf("afk attach: session %q is not running (no host.json found)", name),
			host.ExitNotRunning, true
	}

	// A Rex-backed session's host runs inside a Rex block and refuses the
	// socket dial below, so it takes the same going-in decision as the
	// dashboard's enter and never dials.
	if meta.Runtime == "rex" {
		return attachRex(repo, name, meta, getenv, newCaller()), 0, false
	}

	reason, err := ui.AttachPane(name, worktree, wait)
	if err != nil {
		// The "host not reachable" error (the retry-dial window exhausted) is
		// deliberately mapped to ExitNotRunning, not a generic failure — this is
		// what lets a caller (e.g. the dashboard) recover from a stale host.json
		// whose PID has been reused by an unrelated process. Every other error is
		// a genuine, unexpected failure.
		if errors.Is(err, host.ErrHostNotReachable) {
			return fmt.Sprintf("afk attach: session %q is not running: %v", name, err),
				host.ExitNotRunning, true
		}
		return fmt.Sprintf("afk attach: %v", err), 1, true
	}

	return fmt.Sprintf("afk attach: %s", reason.String()), reason.ExitCode(), false
}

// attachRex makes the going-in decision (rex.GoIn) for a Rex-backed session and
// returns the one line to print. Rex is only asked when the caller is inside Rex
// and the window is known: those are the only facts that need the owner's
// client, and every other path answers with the band text without a call. A
// failed client list counts as "owner not on that session", so the owner still
// gets the band text rather than an error for a convenience step.
func attachRex(repo, name string, meta host.Meta, getenv func(string) string, caller rex.Caller) string {
	var sessionID, windowID string
	if meta.Rex != nil {
		sessionID, windowID = meta.Rex.SessionID, meta.Rex.WindowID
	}
	facts := rex.GoInFacts{
		Runtime:   meta.Runtime,
		InsideRex: getenv("REX_SESSION") != "" && getenv("REX_BLOCK") != "",
		WindowID:  windowID,
	}
	ctx := context.Background()
	if facts.InsideRex && windowID != "" && sessionID != "" {
		if clients, err := rex.ListClients(ctx, caller, "app"); err == nil {
			if c, ok := rex.OwnerClient(clients); ok {
				facts.OwnerClientOnAgentSession = slices.Contains(c.SessionIDs, sessionID)
			}
		}
	}
	band := fmt.Sprintf("This agent runs in Rex: open %s › %s in Rex", filepath.Base(repo), name)
	if rex.GoIn(facts).Kind != rex.GoInFocusWindow {
		return band
	}
	if err := rex.FocusWindow(ctx, caller, sessionID, windowID); err != nil {
		return fmt.Sprintf("%s (afk could not bring the window forward: %v)", band, err)
	}
	return fmt.Sprintf("afk attach: %s is shown in Rex.", name)
}

// resolveSessionWorktree finds the worktree hosting the session named name
// by running discover.Discover from repo. repo is the directory `afk
// attach` was invoked from (typically the repo root, since that is where
// the dashboard invokes it) — the session's own worktree is discovered
// independently and need not equal repo. ok is false when no session named
// name was found anywhere discover.Discover looked.
func resolveSessionWorktree(repo, name string) (worktree string, ok bool, err error) {
	sessions, err := discover.Discover(repo, discover.DefaultDeps())
	if err != nil {
		return "", false, err
	}
	wt, found := findSessionWorktree(sessions, name)
	return wt, found, nil
}

// readMetaWithRetry polls host.ReadMeta for up to wait, closing the narrow
// startup race documented at metaReadWait. Same backoff shape as
// host.Dial's own dialWithRetry: tight at first, capped at 100ms, and the
// deadline is only checked after a failed attempt so wait<=0 still gets
// exactly one try (this is what keeps runAttach(..., 0)'s existing "no
// host.json at all" test behavior unchanged).
func readMetaWithRetry(worktree, name string, wait time.Duration) (host.Meta, error) {
	deadline := time.Now().Add(wait)
	backoff := 10 * time.Millisecond
	for {
		meta, err := host.ReadMeta(worktree, name)
		if err == nil {
			return meta, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return host.Meta{}, err
		}
		if backoff > remaining {
			backoff = remaining
		}
		time.Sleep(backoff)
		if next := backoff * 2; next < 100*time.Millisecond {
			backoff = next
		} else {
			backoff = 100 * time.Millisecond
		}
	}
}

// findSessionWorktree is the pure lookup within already-discovered
// sessions, split out from resolveSessionWorktree so the "find by name"
// decision is testable without touching git or the filesystem.
//
// A worktree-side fault row (session.Kind.WorktreeFault) never satisfies the
// lookup: by definition it carries no `.afk/{name}/host.json`, so there is
// no host to attach to. Attach deliberately keeps excluding only worktree
// faults, because a stranded session (one whose status directory is in a
// worktree checked out on a different branch) has a host reachable via its
// carrier worktree. A name-only lookup that excluded the stranded entry would
// report "not running" for a session whose host is running via its carrier.
// With a healthy session X in `/repo/wt-x` and a stranded X carried by `/repo`,
// sortSessionStates orders `/repo` first, so `afk attach X` reaches the
// carrier's host.json and the stranded entry shadows the healthy one; this
// pre-existing collision is accepted (the shadowing behavior did not change
// when the stranded entry was promoted from Orphan to KindStatusWithoutWorktree).
// A name that only matches fault rows is not found, exactly as it was before
// fault rows joined discover's slice.
func findSessionWorktree(sessions []discover.SessionState, name string) (worktree string, ok bool) {
	for _, s := range sessions {
		if s.Name == name && !s.Kind.WorktreeFault() {
			return s.WorktreePath, true
		}
	}
	return "", false
}
