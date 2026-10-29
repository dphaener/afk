package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/rex"
	"github.com/dphaener/afk/internal/screen"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/web"
)

// This file joins the page's four session actions (internal/web's
// Deps.CleanSession, RestartSession, SetSessionLabel and NewSession) to the
// entry points the commands and the dashboard share. It decides nothing about
// a session: each seam calls one entry point and words its outcome for the
// page.

// webCleanSession is the real Deps.CleanSession.
//
// It never forces and never deletes the branch (plan assumptions A1 and A2):
// the dashboard forces after a confirm, but from a phone the destructive path
// is deleting a live session's worktree, so the page refuses what a plain
// `afk clean` refuses and points at the two places that may force. The phase
// the page showed is passed along, so a session that moved on while the
// confirm was open is refused (A5).
func webCleanSession(root, name, phaseSeen string) web.SessionActionResult {
	return webCleanOutcome(cleanSessionForSurface(root, name,
		cleanSurfaceRequest{CheckPhase: true, PhaseSeen: phaseSeen}, defaultCleanSurfaceDeps()), name)
}

// forceHint is the tail of the phase gate's sentence. It names the command's
// own flag, which means nothing on a web page, so webCleanOutcome swaps it for
// the places a forced clean can be made.
const forceHint = " — pass --force to clean it anyway"

// webCleanOutcome maps a clean's result to the page's. Pure.
func webCleanOutcome(res cleanSurfaceResult, name string) web.SessionActionResult {
	switch res.Outcome {
	case cleanCleaned:
		return web.SessionActionResult{Outcome: web.SessionActionDone, Text: webCleanedText(res.Result)}
	case cleanRefusedOutcome, cleanNotFound:
		text := res.Text
		if strings.HasSuffix(text, forceHint) {
			text = strings.TrimSuffix(text, forceHint) +
				". This page does not force a clean. Run `afk clean --force " + name + "` or use `afk dash` if you want it cleaned anyway."
		}
		return web.SessionActionResult{Outcome: web.SessionActionRefused, Text: text}
	default:
		text := res.Text
		if e := strings.TrimSpace(res.EchoStderr); e != "" {
			text += " (" + strings.ReplaceAll(e, "\n", " · ") + ")"
		}
		return web.SessionActionResult{Outcome: web.SessionActionFailed, Text: text}
	}
}

// webCleanedText words a finished clean with the dashboard's success words
// (internal/ui's cleanSucceededStatus): which of the archive, the plain
// removal and the worktree-only removal happened, and that the branch was
// kept. The page never deletes a branch, so there is no recovery clause.
func webCleanedText(r session.CleanResult) string {
	var text string
	switch {
	case r.ArchiveSkipped:
		text = "Cleaned " + r.Name + "; worktree removed, nothing to archive."
	case r.ArchivePath == "":
		text = "Cleaned " + r.Name + "; session directory removed."
	default:
		text = "Cleaned " + r.Name + "; archived as " + filepath.Base(r.ArchivePath) + "."
	}
	if r.SnapshotCommitted {
		text += " Dirty changes were snapshot-committed."
	}
	if r.Branch != "" {
		text += " Branch " + r.Branch + " kept."
	}
	return text
}

// webRestartSession is the real Deps.RestartSession.
func webRestartSession(root, name string) web.SessionActionResult {
	return webActionResult(restartSessionForSurface(root, name, defaultRestartSurfaceDeps()))
}

// webSetSessionLabel is the real Deps.SetSessionLabel.
func webSetSessionLabel(root, name, label string) web.SessionActionResult {
	return webActionResult(setLabelForSurface(root, name, label, defaultLabelSurfaceDeps()))
}

// webNewSession is the real Deps.NewSession. It starts the session as
// `afk new --autonomous` does and no more (A3).
func webNewSession(root, name, description string) web.SessionActionResult {
	return webActionResult(newAutonomousForSurface(root, name, description, defaultNewSurfaceDeps()))
}

// webActionResult copies the root's result to the page's. The outcome strings
// are the same by design (session_surface.go), but an outcome the page does
// not know is turned into a failure here rather than passed on.
func webActionResult(r sessionActionResult) web.SessionActionResult {
	switch r.Outcome {
	case web.SessionActionDone, web.SessionActionRefused, web.SessionActionFailed:
		return web.SessionActionResult{Outcome: r.Outcome, Text: r.Text}
	}
	return web.SessionActionResult{Outcome: web.SessionActionFailed,
		Text: "The action gave an answer afk does not know. Open afk dash to see the session as it is now."}
}

// webWatcher is the real Deps.WatchScreen (T17.14): it finds a session's Rex
// block through host.json and reads its screen as HTML. Each dependency is a
// field so a test drives it with a fake caller and fake files; the real
// caller is built in daemonWebDeps.
type webWatcher struct {
	// worktree finds the session's own worktree, where its host.json is.
	worktree func(root, name string) (string, error)
	// readMeta is host.ReadMeta.
	readMeta func(worktree, name string) (host.Meta, error)
	// runtimeName is the runtime the session was started on, or "" when
	// unknown; it tells a stopped Rex session from an afk-host one once
	// host.json is gone, as the screen saver does (daemon_cmd.go).
	runtimeName func(worktree, name string) string
	caller      rex.Caller
}

// newWebWatcher is the watcher over the real files, given the Rex caller.
func newWebWatcher(c rex.Caller) webWatcher {
	return webWatcher{
		worktree: webSessionWorktree,
		readMeta: host.ReadMeta,
		runtimeName: func(worktree, name string) string {
			rt, err := session.RuntimeFor(worktree, name)
			if err != nil {
				return ""
			}
			return rt.Name()
		},
		caller: c,
	}
}

// webSessionWorktree finds the worktree of the session called name in the
// project at root, by the same discovery the page's session list uses, so
// the watch reads the session the page shows. Discovery reads git and files
// only; it is called off the live reader's goroutine (see watchTick).
func webSessionWorktree(root, name string) (string, error) {
	found, err := discover.Discover(root, discover.DefaultDeps())
	if err != nil {
		return "", fmt.Errorf("the sessions of %s could not be listed (%w)", root, err)
	}
	for _, s := range found {
		if s.Name == name && s.WorktreePath != "" {
			return s.WorktreePath, nil
		}
	}
	return "", fmt.Errorf("there is no session called %s", name)
}

// screen reads the session's screen for the Watch view. It asks Rex for the
// size first (rex.FormatSize with max_bytes one past bound), because a busy
// block is about 0.9 MB of HTML (T17.1) and the page polls every two
// seconds: a screen within the bound came back whole in that one call and
// is used as it is. Only a screen above the bound is read in full, and its
// last bound bytes kept, cut at a line so no tag is split; the hub then
// reads it on the slow cadence.
//
// Any error comes back with Slow set and no HTML, so a page never shows a
// part of a screen, and the hub's next try waits for the slow cadence.
func (w webWatcher) screen(ctx context.Context, root, name string, bound int64) (web.WatchReading, error) {
	failed := func(err error) (web.WatchReading, error) { return web.WatchReading{Slow: true}, err }
	worktree, err := w.worktree(root, name)
	if err != nil {
		return failed(err)
	}
	b, err := w.block(worktree, name)
	if err != nil {
		return failed(err)
	}
	size, err := rex.FormatSize(ctx, w.caller, b.SessionID, b.BlockID, "html", bound+1)
	if err != nil {
		return failed(webWatchRexError(b, err))
	}
	// A reply within the bound is the whole screen, since max_bytes was more
	// than it. The length check guards a Rex that sent less than it said.
	if size.Total <= bound && int64(len(size.Content)) == size.Total {
		return web.WatchReading{Total: size.Total, HTML: size.Content}, nil
	}
	html, err := rex.Format(ctx, w.caller, b.SessionID, b.BlockID, "html")
	if err != nil {
		return failed(webWatchRexError(b, err))
	}
	if int64(len(html)) <= bound {
		return web.WatchReading{Total: int64(len(html)), HTML: html}, nil
	}
	return web.WatchReading{Total: int64(len(html)), HTML: screen.Tail(html, int(bound)), Slow: true}, nil
}

// block finds the session's Rex block from host.json. No block means an
// afk-host session (web.ErrWatchNotRex); a Rex session whose host.json is
// gone has stopped, and so has its block (web.ErrWatchGone).
func (w webWatcher) block(worktree, name string) (host.RexBlock, error) {
	meta, err := w.readMeta(worktree, name)
	switch {
	case err == nil && meta.Rex != nil:
		return *meta.Rex, nil
	case err == nil:
		return host.RexBlock{}, fmt.Errorf("%w: %s runs on afk's own host, which has no screen the page can read", web.ErrWatchNotRex, name)
	case !errors.Is(err, os.ErrNotExist):
		return host.RexBlock{}, fmt.Errorf("the host record of %s could not be read (%w)", name, err)
	case w.runtimeName(worktree, name) == host.RuntimeRex:
		return host.RexBlock{}, fmt.Errorf("%w: %s ran in Rex, but its host has stopped", web.ErrWatchGone, name)
	default:
		return host.RexBlock{}, fmt.Errorf("%w: %s is not running in Rex", web.ErrWatchNotRex, name)
	}
}

// webWatchRexError words a failed Rex read. Exit 3 is Rex's not_found
// (T17.1): the block or its session is gone, which the page reports and the
// hub retries only on the slow cadence.
func webWatchRexError(b host.RexBlock, err error) error {
	var ce *rex.CallError
	if errors.As(err, &ce) && ce.ExitCode() == 3 {
		return fmt.Errorf("%w: Rex block %s (%w)", web.ErrWatchGone, b.BlockID, err)
	}
	return fmt.Errorf("the screen of Rex block %s could not be read (%w)", b.BlockID, err)
}

// withWatchable sets each view's Watchable from its host.json: a session is
// watchable when the record names a Rex block. It reads that one file only
// and never asks Rex, so listing sessions stays as cheap as before; a
// missing or damaged file leaves the session not watchable, which is the
// safe answer (the Watch page itself says why when there is no screen).
func withWatchable(views []web.SessionView, read func(root, name string) (host.Meta, error)) []web.SessionView {
	for i := range views {
		if views[i].WorktreePath == "" {
			continue
		}
		if m, err := read(views[i].WorktreePath, views[i].Name); err == nil && m.Rex != nil {
			views[i].Watchable = true
		}
	}
	return views
}
