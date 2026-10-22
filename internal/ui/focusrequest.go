// focusrequest.go implements the dashboard's end of click-to-focus: a clicked
// notification writes <git-common-dir>/afk-dash/request.<pid>.json naming a
// session (internal/dashmeta), and the dashboard whose pid that is opens that
// session's pane AND focuses it, so the user's keystrokes land in the Claude
// session rather than in a list.
//
// The file is addressed to ONE dashboard's pid, matching how W1.T2 scopes Entry
// files, and that is what keeps concurrent dashboards from fighting: a shared
// request file was read by all of them, so all of them opened the same session,
// and internal/host serving exactly one client meant the second connection stole
// it out of the first's pane.
//
// Why a request FILE rather than keystrokes: a live dashboard has no IPC and no
// name-search keybinding, so the only send-keys route to a NAMED session is to
// count j/k presses against the current row order — which shifts as sessions
// change phase, so the count races the thing it is targeting. A name in a file
// cannot select the wrong session.
//
// Why fsnotify AND a poll: fsnotify is the fast path (the pane opens within
// milliseconds of the click, so the dashboard has already acted by the time the
// user's tmux client arrives), but watch creation can fail and some filesystems
// do not support it. A silent no-watch must degrade to SLOW, never to dead, so
// the existing 2s discovery tick also checks for a pending request. No new timer
// is added for this.
//
// The rendezvous is per PROJECT, not per dashboard: one entry and one watcher in
// every project the switcher knows (Model.rendezvous), because a notification
// from a project that is not on screen has to be clickable too. The directory
// stays per repo — moving it anywhere machine-global would change what `afk
// notify` resolves — and the project a request belongs to is therefore decided by
// which watcher fired, never by the request's contents: dashmeta.Request carries
// only a session name, and two projects may hold the same one. Everything above
// holds per project as well, degradation included: a project whose rendezvous
// cannot be resolved or written loses click-to-focus alone.
package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/dphaener/afk/internal/dashmeta"
)

// focusRequestMsg carries a pending focus request into Update. It is produced by
// both paths — the fsnotify subscription and the discovery tick's fallback
// check — so there is exactly one handler and the two paths cannot drift.
//
// found=false is a first-class, expected value: the fallback check fires on every
// tick and almost always finds nothing.
type focusRequestMsg struct {
	request dashmeta.Request
	found   bool
	// fromWatch marks a message produced by the fsnotify subscription, which is
	// what tells Update to re-arm it. The fallback path must NOT re-arm (the tick
	// already reschedules itself), or the two would multiply into a busy loop.
	fromWatch bool
	// dir is the rendezvous directory this message came from, and it is how the
	// PROJECT is determined: dashmeta.Request carries a session name and nothing
	// else, deliberately — a project field there would change what `afk notify`
	// has to resolve. So the only thing that can tell two projects' identically
	// named sessions apart is which directory the file landed in, which is to say
	// which watcher fired. The Model maps it back to a project
	// (projectForRendezvous).
	//
	// Empty means the launch rendezvous, which is also what a hand-built message
	// in a single-project test carries: both resolve to this dashboard's own
	// gitCommonDir and to the active project, exactly as before there were
	// several.
	dir string
}

// dashEntryMeta is the tmux location a dashmeta entry records: everything
// `afk notify` needs in order to switch to this dashboard's pane. The values
// are resolved by `afk dash` before the UI exists and arrive through Deps.
//
// One struct rather than four parameters threaded separately, for the same
// reason dash.go's dashLocation is a struct: the write, the Model field and
// W5.T3's per-project write cannot fall out of positional agreement if there is
// only one ordering of them.
type dashEntryMeta struct {
	tmux      string
	paneID    string
	clientTTY string
	startedAt string
}

// focusRendezvous is ONE project's end of the click-to-focus rendezvous: the
// git common dir whose afk-dash/ directory holds that project's entry and
// requests, plus the fsnotify watcher over it.
//
// watch is nil for two different reasons that need no distinguishing anywhere:
// no watch could be created for this project (an unwritable directory, a
// filesystem without support, no descriptors left), or the directory is this
// dashboard's own launch rendezvous and Model.focusWatch already covers it. Both
// mean "nothing to re-arm here", and the fallback poll carries the project
// either way — the "no-watch degrades to slow, never to dead" rule in this
// file's package doc applies per project, not just per dashboard.
type focusRendezvous struct {
	dir   string
	watch focusWatcher
}

// gitCommonDirArgs is the rev-parse invocation that locates a repo's rendezvous
// directory, the same one dash.go's resolveGitCommonDirIn issues. That function
// cannot be reused: it lives in package main, which this package must not import
// (main imports it), so the argv is restated here and issued through Deps.RunGit.
//
// --path-format=absolute is not optional. Without it --git-common-dir answers a
// path relative to the cwd from anywhere but the repo root, and
// dashmeta.WriteEntry declines a relative directory rather than creating one
// somewhere unexpected — so dropping the flag turns click-to-focus off for every
// project whose worktree layout puts git's answer in relative form, silently.
func gitCommonDirArgs() []string {
	return []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}
}

// resolveRendezvousDir asks git for the git common dir of the repo at dir.
//
// Resolution happens during the SCAN, off the UI thread, and never on the 2s
// discovery tick: it is one subprocess per project, which is affordable once a
// minute for a set that changes when someone creates a repo, and not affordable
// on the cadence that keeps the session list fresh.
//
// A directory that is not a repo is an ordinary answer here, not a failure worth
// reporting: a search root may hold anything, and a project with no resolvable
// rendezvous simply has no click-to-focus.
func resolveRendezvousDir(run func(dir string, args ...string) (string, error), dir string) (string, error) {
	out, err := run(dir, gitCommonDirArgs()...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// writeDashEntryCmd publishes this dashboard's dashmeta entry under
// gitCommonDir, off the UI thread.
//
// Ownership sits here rather than on `afk dash`'s startup path because the
// entry is one end of the same rendezvous the watcher below is the other end of,
// and the Model is the only thing that knows which projects it is watching: one
// entry goes into every project's rendezvous, and which projects those are is a
// scan result. It is a Cmd rather than an inline write because Update performs no
// I/O by package contract, and the per-project refresh is driven by those scan
// results arriving as messages — a Cmd is the only sanctioned route from Update
// to the filesystem.
//
// The error is discarded, matching the swallow the startup path performed before
// it: a dashboard that cannot advertise its pane is fully functional, just
// unreachable by a clicked notification, and refusing to start over that would
// trade the whole tool for a convenience. nil — no Cmd whatsoever — is returned
// when there is no seam or no directory to write into.
//
// Whether the location is worth recording at all is NOT decided here. dashmeta's
// own gates (empty tmux, empty pane id, a relative git common dir) refuse the
// write, and `afk dash`'s dashLocation.recordable mirrors them for the
// AFK_DEBUG diagnostic. This package cannot see that predicate, so it passes
// the values along and lets the writer decide, which is what keeps the answer in
// one place instead of two that can drift.
func writeDashEntryCmd(write func(gitCommonDir, tmux, paneID, clientTTY, startedAt string) error,
	gitCommonDir string, meta dashEntryMeta) tea.Cmd {
	if write == nil || gitCommonDir == "" {
		return nil
	}
	return func() tea.Msg {
		_ = write(gitCommonDir, meta.tmux, meta.paneID, meta.clientTTY, meta.startedAt)
		return nil
	}
}

// removeDashEntryCmd withdraws this dashboard's entry from gitCommonDir, off the
// UI thread. It is writeDashEntryCmd's counterpart for a project that LEFT the
// project list while the dashboard was running.
//
// requestQuit removes entries inline instead, and the asymmetry is forced rather
// than untidy: a Cmd returned beside tea.Quit may never run, while a scan result
// is an ordinary message with more Updates after it. Both routes are best-effort
// and idempotent (dashmeta.RemoveEntry counts an already-absent file as success),
// because a leftover entry aims a click at a pane whose dashboard is no longer
// watching that project — a wrong answer, where no entry is merely a missing
// convenience.
func removeDashEntryCmd(remove func(gitCommonDir string) error, gitCommonDir string) tea.Cmd {
	if remove == nil || gitCommonDir == "" {
		return nil
	}
	return func() tea.Msg {
		_ = remove(gitCommonDir)
		return nil
	}
}

// focusWatcher is the fsnotify seam. The dashboard drives it through an
// interface so tests exercise the whole request pipeline — event to pane open to
// focus, plus the re-arm — with a fake that has no inotify/kqueue behind it.
type focusWatcher interface {
	// Events delivers filesystem events for the watched directory.
	Events() <-chan fsnotify.Event
	// Close stops the watcher and releases its resources.
	Close() error
}

// fsnotifyWatcher wraps *fsnotify.Watcher as the focusWatcher interface.
//
// Errors are not surfaced to the dashboard — there is nothing useful it could do
// about a watch overflow that beats degrading to the fallback poll — but they MUST
// still be drained, and this is not a defensive nicety. Verified in
// fsnotify@v1.10.1's source:
//
//   - fsnotify.go:280 builds the watcher as
//     `make(chan Event, defaultBufferSize), make(chan error)`, and
//     backend_kqueue.go:186 sets defaultBufferSize = 0 on darwin — so BOTH
//     channels are unbuffered.
//   - shared.go's sendError BLOCKS: it selects on `w.done` and `w.Errors <- err`
//     with no default, so it parks until something receives or the watcher closes.
//   - backend_kqueue.go's readEvents calls sendError (lines 454, 534, 542) and
//     sendEvent (507) from the SAME goroutine, so an unreceived error stops that
//     goroutine from ever delivering another event.
//
// One undrained error therefore kills the fast path for the rest of the run and
// silently drops click-to-focus onto the 2s poll — precisely the "a wedged
// consumer must never stall the producer" rule CLAUDE.md sets for the host pumps.
// The drain discards what it reads: draining is the whole job.
type fsnotifyWatcher struct{ w *fsnotify.Watcher }

func (f fsnotifyWatcher) Events() <-chan fsnotify.Event { return f.w.Events }
func (f fsnotifyWatcher) Close() error                  { return f.w.Close() }

// drainErrors receives and discards from errs until it is closed.
//
// Close() is what ends this goroutine, and it does so without a second signal:
// every backend closes both channels when its event loop returns (kqueue does it
// in readEvents' defer, backend_kqueue.go:442-443), and Close() is what stops that
// loop. A receive on a closed channel returns immediately, so the range ends —
// no leak, and nothing to coordinate.
func drainErrors(errs <-chan error) {
	for range errs {
	}
}

// startFocusWatcher watches afk-dash/ for focus requests.
//
// It watches the DIRECTORY, not request.json itself, and that is load-bearing:
// the file is published by an atomic rename (dashmeta.WriteRequest), so a watch
// on the path would follow the inode that gets replaced and go deaf after the
// first request — kqueue in particular watches an open fd, not a name. A
// directory watch reports the rename as a Create/Rename event on the new name
// every time.
//
// Every failure returns a nil watcher and no error the caller has to handle: no
// git common dir, an undirectoryable path, an unsupported filesystem, or the
// process being out of watch descriptors all mean the same thing — no fast path,
// fall back to the tick.
func startFocusWatcher(gitCommonDir string) focusWatcher {
	if gitCommonDir == "" {
		return nil
	}
	dir, err := dashmeta.EnsureDir(gitCommonDir)
	if err != nil {
		return nil
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil
	}
	// Started only once the watch is established, so a failed Add does not leave a
	// goroutine ranging over a channel nothing will ever close.
	go drainErrors(w.Errors)
	return fsnotifyWatcher{w: w}
}

// focusRequestWatchCmd is the fsnotify subscription, in the same shape as
// paneEventCmd: it blocks on the watcher's channel in a tea.Cmd goroutine and
// returns one tea.Msg. Update re-arms it on every focusRequestMsg carrying
// fromWatch, and the re-arm is LOAD-BEARING — without it the dashboard would
// honor exactly one click per run and then go silent, with nothing to point at.
//
// Blocking here cannot stall the UI: Bubble Tea runs each Cmd on its own
// goroutine, and Update never touches the watcher. A wedged or closed watcher
// ends this Cmd rather than the dashboard — a closed Events channel yields a
// not-ok receive, which returns a found=false, fromWatch=false message so the
// subscription retires quietly and the tick's fallback carries on alone.
//
// The read is deliberately narrow: only events naming THIS dashboard's own
// request file are turned into a message, so neither the Entry writes and removals
// that also happen in this directory nor a request addressed to another dashboard
// can wake the pipeline.
func focusRequestWatchCmd(w focusWatcher, gitCommonDir string, pid int) tea.Cmd {
	if w == nil {
		return nil
	}
	return func() tea.Msg {
		for {
			ev, ok := <-w.Events()
			if !ok {
				return focusRequestMsg{dir: gitCommonDir}
			}
			if !isRequestEvent(ev, pid) {
				continue
			}
			r, found := dashmeta.ReadRequest(gitCommonDir, pid)
			return focusRequestMsg{request: r, found: found, fromWatch: true, dir: gitCommonDir}
		}
	}
}

// isRequestEvent reports whether ev concerns the focus-request file addressed to
// pid. Pure, so the event matrix is table-testable without a filesystem.
//
// Chmod is excluded: it is the one op that cannot mean "a new request arrived",
// and on macOS a kqueue backend reports an ATTRIB for ordinary metadata churn.
// Remove and Rename are INCLUDED even though they mean the file is going away:
// the atomic rename that publishes a request is observed as a Rename on the
// destination name by some backends, so excluding it would drop real requests.
// A remove that is only a removal costs one ReadRequest that finds nothing.
func isRequestEvent(ev fsnotify.Event, pid int) bool {
	if !isRequestPath(ev.Name, pid) {
		return false
	}
	return ev.Op != fsnotify.Chmod
}

// isRequestPath reports whether path names the request file belonging to pid,
// matched on basename. fsnotify reports the path it was given joined with the
// entry name, so comparing whole paths would be sensitive to symlink and
// cleanliness differences between what the watcher was handed and what the writer
// used.
//
// Matching on the pid-scoped name is what makes a concurrent dashboard's request
// invisible here: two dashboards watch the SAME directory and therefore see each
// other's events, so the name is the only thing that separates them.
func isRequestPath(path string, pid int) bool {
	return baseName(path) == dashmeta.RequestFileName(pid)
}

// baseName is filepath.Base without importing filepath for one call, and
// without its empty-path special case: an empty path has no basename here.
func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// focusRequestPollCmd is the fallback check, dispatched from the SAME 2s
// discovery tick that reloads sessions. It exists so a dashboard whose fsnotify
// watch could not be created still honors clicks, just a tick later.
//
// It is a Cmd, not an inline read in Update, because it touches the filesystem:
// the tick handler stays pure and the read happens on Bubble Tea's Cmd goroutine.
// Returning found=false on the overwhelmingly common "no request pending" tick is
// intentional — the handler's no-op for that case is one comparison.
func focusRequestPollCmd(gitCommonDir string, pid int) tea.Cmd {
	if gitCommonDir == "" {
		return nil
	}
	return func() tea.Msg {
		// The sweep rides this tick rather than startup: a request addressed to a
		// dashboard that died before reading it is removed by nobody else, since a
		// live dashboard only ever deletes its own file.
		dashmeta.SweepStaleRequests(gitCommonDir)
		r, found := dashmeta.ReadRequest(gitCommonDir, pid)
		return focusRequestMsg{request: r, found: found, dir: gitCommonDir}
	}
}

// focusRequestDecision is what to do about a pending request.
type focusRequestDecision int

const (
	// focusRequestIgnore leaves the request file alone: there is nothing to
	// consume (no request was pending).
	focusRequestIgnore focusRequestDecision = iota
	// focusRequestDiscard consumes the request without acting on it — it is
	// stale, or it names a session this dashboard cannot see.
	focusRequestDiscard
	// focusRequestHonor consumes the request and opens+focuses the session.
	focusRequestHonor
)

// decideFocusRequest is the whole request decision, pure so the matrix is
// table-testable without a filesystem, a clock, or a session.
//
// A stale request is DISCARDED rather than honored: the click has already missed
// its moment, and yanking the user's focus for something they did minutes ago is
// worse than doing nothing. A request naming a session discovery does not know is
// discarded for the same reason it is not an error — the session was cleaned, or
// this dashboard is watching a different repo, and either way there is nothing to
// open. Both are consumed, not left pending, so they cannot be re-decided on
// every subsequent tick.
func decideFocusRequest(msg focusRequestMsg, known bool, now time.Time, maxAge time.Duration) focusRequestDecision {
	if !msg.found || msg.request.Name == "" {
		return focusRequestIgnore
	}
	if !dashmeta.RequestFresh(msg.request, now, maxAge) {
		return focusRequestDiscard
	}
	if !known {
		return focusRequestDiscard
	}
	return focusRequestHonor
}

// removeFocusRequestCmd deletes the consumed request off the UI thread. Its
// result is discarded — a removal that fails leaves a request the next decision
// discards as stale, which is already the correct outcome.
func removeFocusRequestCmd(gitCommonDir string, pid int) tea.Cmd {
	if gitCommonDir == "" {
		return nil
	}
	return func() tea.Msg {
		_ = dashmeta.RemoveRequest(gitCommonDir, pid)
		return nil
	}
}
