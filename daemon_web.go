package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
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

// daemonWebDeps builds what the page reaches outside for.
//
// projects answers with the loop's own list as it is now (each project already
// pointed at its main checkout, duplicates dropped), so the page and the loop
// always agree on which folder a name means, and a project added while the
// daemon runs shows on the page from the pass that first reads it. It must
// hand back a slice the page may keep (runDaemon passes projectList.get, which
// copies) and must not wait on the loop.
//
// stateDir is afk's state folder on this machine, where the list of plans
// waiting for approval is kept.
func daemonWebDeps(projects func() []daemon.Project, stateDir string, st usagestate.Deps, now func() time.Time) web.Deps {
	configured := func() []web.Project {
		got := projects()
		ps := make([]web.Project, 0, len(got))
		for _, p := range got {
			ps = append(ps, web.Project{Name: p.Name, Root: p.Root})
		}
		return ps
	}
	return web.Deps{
		Projects: configured,
		Reviews: func() []web.Project {
			return webReviews(daemonstate.ReadReviews(stateDir), configured())
		},
		ReadPlan: webReadPlan,
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
		TaskTokens:    webTaskTokens,
		SetTaskStatus: webSetTaskStatus,
		ProjectRows:   webProjectRows(projects, stateDir, now),
		Repository:    webRepository(runGitOutput),
		PlanChat:      webPlanChat(now),
		PlanReply:     webPlanReply(now),
		Now:           now,
	}
}

// webProjectRows is the real version of web.Deps.ProjectRows: the
// all-projects rows for the daemon's last good project list, sorted the way
// the dashboard sorts them (needs-you first, then list order).
//
// It never reads config.toml: the daemon owns that list and hands it over
// through projects. Each entry is already a main checkout with duplicates
// dropped, so no git is run to find it again (gitCommonDir answers "", which
// mainCheckout takes as "use the folder as given"); a listed folder that has
// gone since the list was read still shows as gone, because isDir is checked
// on every call. There is no unlisted active project here: the daemon has no
// project it was started in. Nothing dials; the one discovery call is made
// only for a project with a running task, as the dashboard does.
func webProjectRows(projects func() []daemon.Project, stateDir string, now func() time.Time) func() []projectrows.Row {
	parts := dashAllProjectsParts{
		isDir: func(path string) bool {
			fi, err := os.Stat(path)
			return err == nil && fi.IsDir()
		},
		gitCommonDir: func(string) string { return "" },
		evalSymlinks: filepath.EvalSymlinks,
		sessions: func(root string) []web.SessionView {
			found, err := discover.Discover(root, discover.DefaultDeps())
			if err != nil {
				return nil
			}
			return webSessionViews(found)
		},
		now: now,
	}
	return func() []projectrows.Row {
		got := projects()
		listed := make([]rowProject, 0, len(got))
		for _, p := range got {
			listed = append(listed, rowProject{Name: p.Name, Path: p.Root})
		}
		rows, _ := buildListedRows(parts, listed, reviewRoots(daemonstate.ReadReviews(stateDir)))
		projectrows.Sort(rows)
		return rows
	}
}

// repositoryWait bounds the one git call the project page makes per request.
// git config only reads a file, so it answers in milliseconds; the bound is
// there for a repository on a hung network mount, which must not hold the
// page.
const repositoryWait = 2 * time.Second

// runGitOutput runs git with args and returns what it printed. It is the
// only part of webRepository that touches the machine.
func runGitOutput(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "git", args...).Output()
}

// webRepository is the real version of web.Deps.Repository: the project's
// origin address, from `git config --get remote.origin.url`, with any user
// name and password taken out (webRepositoryURL). Any failure (no origin, not
// a repository, git missing, the timeout) is "", which the page shows as
// "None set". run is the git call, injected so the test needs no repository.
func webRepository(run func(ctx context.Context, args ...string) ([]byte, error)) func(root string) string {
	return func(root string) string {
		ctx, cancel := context.WithTimeout(context.Background(), repositoryWait)
		defer cancel()
		out, err := run(ctx, "-C", root, "config", "--get", "remote.origin.url")
		if err != nil {
			return ""
		}
		return webRepositoryURL(strings.TrimSpace(string(out)))
	}
}

// webRepositoryURL takes the secrets out of a remote's address. Pure.
//
// An address with a scheme ("https://user:token@host/x") loses its whole
// user part, not just a password: on https a lone user name is very often a
// token (GitHub's "https://<token>@github.com/..." form), and the page has no
// use for it. An scp-like address ("git@host:owner/repo") keeps a plain user,
// which is the ssh login and never a secret, but loses one that holds a
// colon. An address that will not parse is cut at its last "@" rather than
// shown as it is, so a damaged URL cannot leak its password.
func webRepositoryURL(raw string) string {
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		if u, err := url.Parse(raw); err == nil {
			u.User = nil
			return u.String()
		}
		// A password may hold a "/" (which is what made the parse fail), so
		// the cut is at the last "@" anywhere after the scheme: a path with
		// an "@" of its own loses its start, which is the safe way to be
		// wrong.
		rest := raw[i+3:]
		if at := strings.LastIndexByte(rest, '@'); at >= 0 {
			rest = rest[at+1:]
		}
		return raw[:i+3] + rest
	}
	// scp-like: [user@]host:path. The user part ends at the last "@" before
	// the first "/" (a path may hold an "@" of its own).
	head := raw
	if j := strings.IndexByte(raw, '/'); j >= 0 {
		head = raw[:j]
	}
	if at := strings.LastIndexByte(head, '@'); at >= 0 && strings.Contains(head[:at], ":") {
		return raw[at+1:]
	}
	return raw
}

// webPlanChat is the real read behind GET /api/p/{project}/plan/chat.
//
// The planning host's own files say whether a planning session is open and
// which agent CLI runs it; the transcript is read only while that host's
// process is alive (planchat.Read never calls Conversation otherwise), so a
// finished or never-started session costs two file reads and nothing more.
// The home folder is resolved once: it cannot change while the daemon runs,
// and a failure leaves it empty, which planchat.Read reports as a
// conversation that cannot be found.
func webPlanChat(now func() time.Time) func(project, root string) planchat.Chat {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	deps := planchat.Deps{
		Host: func(root string) (planchat.HostInfo, bool) {
			meta, err := host.ReadMeta(root, session.PlanSessionName)
			if err != nil {
				return planchat.HostInfo{}, false
			}
			// A start time that cannot be read cannot be matched to a
			// transcript, so the host is treated as not running.
			started, err := time.Parse(time.RFC3339, meta.StartedAt)
			if err != nil {
				return planchat.HostInfo{Provider: meta.Provider}, true
			}
			return planchat.HostInfo{StartedAt: started, Provider: meta.Provider, Alive: host.Alive(meta.PID)}, true
		},
		// An empty name is the default provider; Lookup resolves it.
		Conversation: func(name string, q provider.ConversationQuery) ([]provider.ConversationEntry, error) {
			p, err := provider.Lookup(name)
			if err != nil {
				return nil, err
			}
			return p.PlanConversation(q)
		},
		Home: home,
		Now:  now,
	}
	return func(project, root string) planchat.Chat {
		return planchat.Read(project, root, deps)
	}
}

// planReplyWait bounds how long one reply may spend reaching the planning
// host. A slow or wedged host must not stall the web request behind it; the
// owner is told the reply was not delivered and can send it again.
const planReplyWait = 2 * time.Second

// planReplyDeps are the three things sendPlanReply needs from outside, so its
// decisions can be tested without a host, a transcript or an agent CLI.
type planReplyDeps struct {
	// Chat reads the planning session's current state, the same read the
	// page's GET uses, so a refusal here is the decision the page showed.
	Chat func(project, root string) planchat.Chat
	// Provider resolves the agent that runs the planning host.
	Provider func(root string) (provider.Provider, error)
	// Send writes the chunks to the planning host's terminal.
	Send func(root string, chunks [][]byte) error
}

// webPlanReply is the real send behind POST /api/p/{project}/plan/chat.
func webPlanReply(now func() time.Time) func(project, root string, r planchat.Reply) error {
	d := planReplyDeps{
		Chat: webPlanChat(now),
		Provider: func(root string) (provider.Provider, error) {
			meta, err := host.ReadMeta(root, session.PlanSessionName)
			if err != nil {
				return nil, err
			}
			// An empty name is the default provider; Lookup resolves it.
			return provider.Lookup(meta.Provider)
		},
		Send: func(root string, chunks [][]byte) error {
			return host.SendKeys(root, session.PlanSessionName, chunks, planReplyWait)
		},
	}
	return func(project, root string, r planchat.Reply) error {
		return sendPlanReply(project, root, r, d)
	}
}

// sendPlanReply checks a reply against the planning session's state and, only
// if it may be sent, types it into the session. A refusal comes back as a
// *planchat.Refusal and sends nothing; any other error means the send itself
// failed.
//
// The state can change between the read and the send (the agent may start a
// new turn). That race is accepted: the agent ignores the keys or reads them
// on its next prompt, and the next read of the page shows what happened.
func sendPlanReply(project, root string, r planchat.Reply, d planReplyDeps) error {
	chat := d.Chat(project, root)
	if refusal := planchat.Check(chat, r); refusal != nil {
		return refusal
	}
	var chunks [][]byte
	if r.Text != "" {
		keys, refusal := planchat.TextKeys(r.Text)
		if refusal != nil {
			return refusal
		}
		chunks = keys
	} else {
		p, err := d.Provider(root)
		if err != nil {
			return fmt.Errorf("could not tell which agent runs the planning session: %w", err)
		}
		keys, err := p.PlanChoiceKeys(chat.Questions, r.Choices)
		switch {
		case errors.Is(err, provider.ErrChoiceUnsupported), errors.Is(err, provider.ErrNoPlanSession):
			return &planchat.Refusal{
				Reason:  planchat.ReasonUnsupported,
				Message: "afk cannot answer this kind of question from the web page for this agent. Open the planning session in afk dash and answer it there.",
			}
		case err != nil:
			return fmt.Errorf("could not build the keys for your choice: %w", err)
		}
		chunks = keys
	}
	if err := d.Send(root, chunks); err != nil {
		return fmt.Errorf("could not send your reply to the planning session: %w", err)
	}
	return nil
}

// webReviews turns the plans waiting for approval into the page's projects,
// leaving out every one whose folder is a configured project. Pure.
//
// Such an entry is not waiting for anything: `afk plan approve` adds the
// project to the machine config before it takes the entry off the list, so
// between those two steps (or for good, when the second one failed) the same
// project is in both. Listed twice, the owner would be asked to approve a plan
// that is already being built.
//
// The page drops these too (internal/web's reviews). It is done here as well
// so that what this function hands over is right by itself, whatever a later
// change to the page does with it.
func webReviews(reviews []daemonstate.Review, configured []web.Project) []web.Project {
	out := make([]web.Project, 0, len(reviews))
	for _, rv := range reviews {
		root := filepath.Clean(rv.Root)
		if slices.ContainsFunc(configured, func(p web.Project) bool { return filepath.Clean(p.Root) == root }) {
			continue
		}
		out = append(out, web.Project{Name: rv.Name, Root: root})
	}
	return out
}

// webReadPlan reads the master plan of the project at root, from wherever the
// project's afk.toml says it is. The path is returned even when the read
// fails, so the page can say which file it looked for.
//
// A project with no afk.toml yet is the usual case for a plan waiting for
// approval (approve is what writes the file), and projectconfig.Load answers
// for it with the default path, docs/PLAN.md, which is where a planning
// session writes its draft.
func webReadPlan(root string) (string, []byte, error) {
	cfg, err := projectconfig.Load(root)
	if err != nil {
		return "", nil, err
	}
	path := cfg.PlanPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		return path, nil, err
	}
	return path, data, nil
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
//
// Every request's context descends from one base context that is cancelled
// the moment shutdown starts. That is for the page's live-change stream
// (GET /events): it is a request that never finishes by itself, and Shutdown
// only closes connections once their requests have ended. Without the cancel
// each open page would hold the stop for the whole webShutdownLimit and then
// be cut by Close; with it the stream handler sees its context end and
// returns at once, and the daemon stops in milliseconds. Ordinary requests
// are unaffected: they finish in far less time than it takes to notice.
func serveDaemonWeb(ln net.Listener, h http.Handler, logf func(format string, args ...any)) (stop func()) {
	base, cancelBase := context.WithCancel(context.Background())
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: webReadHeaderTimeout,
		ReadTimeout:       webReadTimeout,
		WriteTimeout:      webWriteTimeout,
		IdleTimeout:       webIdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	// net/http runs the functions registered here when Shutdown starts, each
	// on its own goroutine, before it waits for anything.
	srv.RegisterOnShutdown(cancelBase)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			logf("web page: it stopped answering (%v). The build carries on without it; restart afk daemon to bring the page back.", err)
		}
	}()
	return func() {
		// Shutdown has cancelled the base already; this releases it in
		// every case, and a second cancel does nothing.
		defer cancelBase()
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
