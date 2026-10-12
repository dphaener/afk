package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// This file joins the web page (internal/web) to `afk daemon`: the real
// versions of what the page reads and the two buttons it presses, and the
// HTTP server around it. Nothing here decides anything about a build.
//
// The server shares nothing in memory with the build loop. The page reads the
// same files the loop writes (the ledger, the inbox, daemon.json, usage.json)
// and its buttons write the same files `afk pause`, `afk resume` and
// `afk inbox answer` do. The loop then sees the change on its next pass, as
// it does for those commands. That is deliberate: the loop runs on one
// goroutine and its rules assume nothing changes under it during a pass, so a
// request handler that reached into its state would need a lock around every
// rule, and a handler that crashed or hung could take the loop with it. Files
// are already the one way every afk process talks to another.

// daemonWebDeps builds what the page reaches outside for. projects are the
// loop's own (each already pointed at its main checkout, duplicates dropped),
// so the page and the loop always agree on which folder a name means.
func daemonWebDeps(projects []daemon.Project, st usagestate.Deps, now func() time.Time) web.Deps {
	ps := make([]web.Project, 0, len(projects))
	for _, p := range projects {
		ps = append(ps, web.Project{Name: p.Name, Root: p.Root})
	}
	return web.Deps{
		Projects: ps,
		LoadBuild: func(root string) ([]masterplan.Task, ledger.Ledger, error) {
			bs, err := loadBuildState(root)
			return bs.Tasks, bs.Ledger, err
		},
		// The same read the dashboard does on its two-second ticker. It lists
		// git worktrees and reads files; it never dials a session's host, so
		// a wedged session cannot hold a page request. Sessions that cannot
		// be listed are no sessions: the page then shows the phase as
		// unknown, which is true.
		Sessions: func(root string) []web.SessionView {
			found, err := discover.Discover(root, discover.DefaultDeps())
			if err != nil {
				return nil
			}
			return webSessionViews(found)
		},
		Usage: func() (usagestate.File, bool) { return usagestate.Read(st) },
		Pause: func(root string) error {
			_, err := pauseProject(root, now())
			return webPauseError(root, err)
		},
		Resume: func(root string) error {
			_, err := resumeProject(root, now())
			return webPauseError(root, err)
		},
		Now: now,
	}
}

// webSessionViews copies what the page needs out of discovery's rows. Pure.
//
// A one-off session (`afk new --scratch`) has no status.json, so its phase is
// empty without being stale. It is passed on as stale: the page shows a stale
// session's phase as "unknown", where an empty phase would show as a blank.
func webSessionViews(found []discover.SessionState) []web.SessionView {
	views := make([]web.SessionView, 0, len(found))
	for _, s := range found {
		views = append(views, web.SessionView{
			Name:          s.Name,
			Phase:         string(s.Phase),
			Stale:         s.StatusStale || s.Scratch,
			AwaitingInput: s.AwaitingInput,
			HostRunning:   s.HostRunning,
		})
	}
	return views
}

// webPauseError words a failed pause or resume for the page, which shows the
// text as it is. The sentences are the ones `afk pause` and `afk resume`
// print for the same failure, so the owner reads one explanation whichever
// way they pressed the button. Pure.
//
// A failed log step still returns an error although the pause or resume
// itself went through: the commands exit 1 for it, and the sentence says
// which half happened.
func webPauseError(root string, err error) error {
	if err == nil {
		return nil
	}
	var pe *pauseError
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.stage {
	case stageLogPause:
		return fmt.Errorf("the project is paused, but the decision log could not be written: %w", pe.err)
	case stageLogResume:
		return fmt.Errorf("the project is resumed, but the decision log could not be written: %w", pe.err)
	case stageLogStopCleared:
		return fmt.Errorf("the stopped line is cleared, but the decision log could not be written: %w", pe.err)
	case stageStopMarker:
		return fmt.Errorf("%w. Check that afk can write to %s", pe.err, filepath.Dir(daemonstate.StopPath(root)))
	default:
		return fmt.Errorf("%w. Check that afk can write to %s", pe.err, filepath.Dir(daemonstate.PausePath(root)))
	}
}

// The limits on one connection to the page. They exist because the page is
// opened from a phone: a connection that goes quiet half way (a tunnel, a
// lift, a locked screen) would otherwise hold its goroutine and its socket
// until the daemon is restarted, and the daemon runs for weeks. Each is the
// net/http.Server field of the same name. The page's answers are small files
// rendered at once, so none of these is ever close to a working request.
const (
	webReadHeaderTimeout = 5 * time.Second
	webReadTimeout       = 15 * time.Second
	webWriteTimeout      = 30 * time.Second
	webIdleTimeout       = 2 * time.Minute
	// webShutdownLimit is how long a stopping daemon waits for requests in
	// flight before it closes their connections. The daemon is stopped by a
	// service manager with its own, usually short, patience.
	webShutdownLimit = 5 * time.Second
)

// serveDaemonWeb serves h on ln, on a goroutine of its own, and returns the
// function that shuts it down. stop closes the listener, gives requests in
// flight webShutdownLimit to finish, closes whatever is left, and returns
// once the serving goroutine has ended, so nothing of the page outlives the
// daemon that started it.
//
// The page is a convenience on top of the build. If the server stops by
// itself after start-up, that is one line in the log and nothing else: the
// loop is not told and carries on. logf must be safe to call from a second
// goroutine.
func serveDaemonWeb(ln net.Listener, h http.Handler, logf func(format string, args ...any)) (stop func()) {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: webReadHeaderTimeout,
		ReadTimeout:       webReadTimeout,
		WriteTimeout:      webWriteTimeout,
		IdleTimeout:       webIdleTimeout,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			logf("web page: it stopped answering (%v). The build carries on without it; restart afk daemon to bring the page back.", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), webShutdownLimit)
		defer cancel()
		_ = srv.Shutdown(ctx)
		// Shutdown returns with connections still open when its time runs
		// out; Close ends those. The listener is closed here by name as
		// well, so that "the port is free once stop returns" does not rest
		// on which of the two calls got to it; a second close of a closed
		// listener is an error nobody needs.
		_ = srv.Close()
		_ = ln.Close()
		// Serve returns once its listener is closed (net/http's Serve
		// answers ErrServerClosed after a Shutdown or Close), so this wait
		// is short.
		<-done
	}
}
