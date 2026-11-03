// messages.go defines the dashboard's message types and the tea.Cmd
// functions that produce them. This is where every side effect lives:
// discovery (git + file reads), the refresh ticker, the restart-flow host
// spawn, the background `afk new` spawn, and the interactive shell-outs
// (`afk attach`, `afk clean`). The Model's Update method (model.go) stays
// pure — it only consumes these messages and returns Cmds, never performing
// I/O itself. This
// is the split the task mandates ("Keep the Bubble Tea model pure ... side
// effects in tea.Cmd functions", "Async shell-outs return messages; UI never
// blocks on them").
//
// # Notification copy
//
// Every notification the dashboard writes follows one shape — the literals in
// model.go, the three builders in this file (paneAttachFailedStatus,
// restartFailedStatus, appendWarningSuffix), the clean summaries in model.go,
// the refresh failures in stack.go, and the coalesced summary in
// notification.go:
//
//	outcome · key hint
//
// The outcome leads, in plain words, saying what happened. Then a "·"
// divider, then a hint naming the key that acts on it. One line each. When
// there is no useful key there is no hint and no divider — "no session pane
// to open" is the whole message.
//
// The rules that shape holds to:
//
//   - No "→" arrows, and no parenthesised hints. "opened x · enter to type in it",
//     never "opened x (press enter to type in it)".
//   - "r to restart", not "press r to restart it": the hint names the key
//     and the verb it performs, nothing more.
//   - Key names are spelled the way bubbles/key spells a chord — "ctrl+]",
//     not "ctrl-]". An action key is named as it is pressed where the notice
//     is read (keyName): alone at All projects and Project, after the leader
//     in the Agent. The help row an inch below the toast carries the same
//     spelling (defaultKeyMap's WithHelp in model.go, mouseCaptureHint in
//     views.go), so the two surfaces never disagree about a chord.
//   - At most ONE em-dash clause of qualification inside the outcome, never
//     two: "x isn't running — its host record was stale · r to restart".
//   - The in-flight family ("creating x…", "opening x…") keeps its shape. A
//     trailing ellipsis already reads as "in progress", and there is no
//     outcome yet to lead with.
//
// The divider does double duty, and saying so is what stops the rule reading
// as violated by the messages it most needs to govern. "·" ALSO separates
// clauses inside a composed message — cleanNotes, cleanFailedStatus's
// newline fold, branchOutcome — so "one · per message" is false of every
// clean result. The rule is that the FINAL "·" introduces the key hint when
// the message has one; every earlier "·" separates clauses. A message with no
// key hint is clauses all the way down.
//
// " · " is also what the help line directly beneath the toast uses to
// separate its own items (bubbles' help ShortSeparator, rendered by
// views.go). That is deliberate reuse, not a collision.
//
// # Ordering a long message, because the toast clips it
//
// renderToast wraps to toastMaxWidth and hard-clips at 3 lines (toast.go) —
// roughly 144 cells — and nothing keeps a second copy now that the footer's
// sticky status row is gone. Whatever trails a long message is therefore
// whatever the user never reads. So a composed message is ordered by VALUE,
// not by the order the steps ran in: what cannot be recovered any other way
// leads, and what a reader could look up afterwards trails into the clip.
//
// cleanSucceededStatus (model.go) is the worked example and the reason the
// rule is written down: the outcome, then the deleted branch's recovery
// command (nothing else persists that SHA now), then the archive basename
// (the directory is rediscoverable), then the cosmetic clauses —
// snapshot-committed, upstack retargeting, skipped-step notes. Do not widen
// the toast to fit more of it: the 3-line clip exists so a wall of text
// cannot cover the detail pane the toast floats over.
package ui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/termpane"
)

// tailLines is how many trailing lines of learnings.md / issues.md the
// detail pane shows (task spec: "last 10 lines").
const tailLines = 10

// tickMsg fires on the refresh ticker; each one triggers a fresh discovery
// pass and schedules the next tick.
type tickMsg time.Time

type plainBranchesLoadedMsg struct {
	branches []string
	trunk    string
	err      error
}

// previewDelay is how long the cursor has to sit still on a row before that
// session's pane opens on its own.
//
// It is a debounce, and the reason it has to be a generous one is the host
// protocol rather than taste: a host serves exactly ONE client, and connecting
// STEALS the session from whoever was attached (the displaced client gets a
// host.Stolen frame — see internal/host and hostpane.go's hostChild.Read). An
// auto-preview is therefore not a free read of a row, so it must fire only
// where the user actually stopped, never once per row a scroll passes over.
//
// tea.Tick Cmds cannot be cancelled, so "re-arming on every keypress" really
// means arming another one: scrolling ten rows arms ten timers and all ten
// fire. Model.previewGen is what makes the nine superseded ones inert, which is
// why that counter is load-bearing rather than defensive.
//
// 300ms is a perceptual budget for "the user has deliberately stopped here",
// and that is the whole justification. It is deliberately NOT derived from any
// key-repeat setting: once a key starts repeating the repeats arrive tens of
// milliseconds apart and re-arm the timer themselves, so even a very short
// debounce would already survive a held key. The delay is buying deliberate
// stops, not repeat suppression.
//
// It started at 750ms and came down after the delay was measured end to end:
// the settle was ~80% of the wait before a pane showed anything (the dial itself
// is ~5ms over a unix socket, and the host's redraw nudge is resizeNudgeDelay,
// 25ms — everything left is the child deciding to repaint, which this side does
// not control). Because every keypress re-arms, the settle was only ever
// protecting against a scroll that PAUSES on a row long enough to dial it, which
// is a narrower case than the original number assumed.
const previewDelay = 300 * time.Millisecond

// previewDialWait is the retry-dial window a preview-initiated open gets
// instead of host.DefaultAttachWait (5s — defaultAttachWait in
// internal/host/client.go). SessionState.HostRunning is advisory, so previews
// WILL be pointed at stale host records, and browsing the list must not park on
// one for five seconds per row.
//
// It is a budget, not a bound. host.dialWithRetry checks the remaining budget
// only AFTER a dial returns, and a single attempt is bounded by its own 500ms
// attachDialTimeout (client.go:296-304), so a pathological socket can still
// cost one ~500ms attempt. Nothing may assert 300ms as a ceiling.
const previewDialWait = 300 * time.Millisecond

// previewTimerMsg fires previewDelay after the cursor move that armed it. gen
// is the Model.previewGen it was armed with and name is the session it was
// armed FOR; Model.applyPreviewTimer drops the message unless both still hold,
// because neither the generation nor the selected row can be inferred from the
// other (see that method's comments).
type previewTimerMsg struct {
	gen  int
	name string
}

// previewTimerCmd arms one preview timer, in tickCmd's shape. gen and name are
// captured here, at Cmd-construction time, so the message carries what was true
// when the cursor moved rather than whatever the Model looks like previewDelay
// later.
func previewTimerCmd(gen int, name string) tea.Cmd {
	return tea.Tick(previewDelay, func(time.Time) tea.Msg {
		return previewTimerMsg{gen: gen, name: name}
	})
}

// selectionFlushedMsg reports that one selection flush finished. err is the
// FIRST write error the flush hit, carried for completeness only: Update's case
// for this message drops it (see there for why a failed best-effort write must
// not reach the status line).
type selectionFlushedMsg struct{ err error }

// flushSelectionsCmd persists a set of pending (projectPath, sessionName)
// selections off the UI thread, on the same off-loop-I/O discipline
// listScratchDirsCmd documents.
//
// entries is COPIED here, at Cmd-construction time, and the returned closure
// reads only the copy. A tea.Cmd runs on a bubbletea goroutine while Update
// keeps recording cursor moves into the live Model.dirtySelections map on the
// UI goroutine, so closing over the caller's map would be a concurrent map
// read/write — a hard runtime throw, not a stale value.
//
// The whole set goes through ONE Cmd, writing SEQUENTIALLY, and that is
// load-bearing rather than a micro-optimization. tea.Batch runs each member on
// its own goroutine, and two concurrent uistate.Remember calls each re-read the
// state file, set their own key on what they read, and rename the temp file over
// it: whichever renames last erases the other project's entry, and the caller
// has already cleared its pending set, so nothing ever re-dirties the lost one.
//
// The nil/empty guard returns no Cmd so a caller that has not checked cannot
// dispatch a goroutine that writes nothing and then reports a flush that never
// happened.
func flushSelectionsCmd(fn func(string, string) error, entries map[string]string) tea.Cmd {
	if fn == nil || len(entries) == 0 {
		return nil
	}
	snapshot := make(map[string]string, len(entries))
	for key, name := range entries {
		snapshot[key] = name
	}
	return func() tea.Msg {
		var first error
		for key, name := range snapshot {
			if err := fn(key, name); err != nil && first == nil {
				first = err
			}
		}
		return selectionFlushedMsg{err: first}
	}
}

// sessionsLoadedMsg carries the result of one discovery pass. err is set
// when git worktree enumeration itself failed (a whole-dashboard error);
// individual stale/malformed sessions are surfaced per-session inside
// sessions, never as err.
//
// gen is the project generation this load was DISPATCHED under (loadCmd
// captures m.projectGen at Cmd-creation time and stamps it here), following
// the same precedent as paneOpenRequest.gen / applyPaneAttached's guard: a
// rapid A→B→C switch (or an ordinary 2s tick for the OUTGOING project still
// in flight when a switcher switch lands) can have this message arrive after
// m.projectGen has moved on, carrying a stale project's sessions. Update's
// sessionsLoadedMsg case drops any arrival whose gen does not match the
// CURRENT m.projectGen — before applyLoaded runs and before previewOnNextLoad
// is consumed — so a stale load can neither install the wrong project's
// sessions nor arm (or steal) a preview for it.
type sessionsLoadedMsg struct {
	sessions []Session
	err      error
	at       time.Time
	gen      int
}

// hostRestartedMsg carries the outcome of the restart flow's host spawn (the
// step that precedes the attach for a session whose host is stopped).
//
// focusOnArrival and selectionBound travel with the restart because the pane
// open happens in this message's handler, not at dispatch. `r` from the right
// side sets focusOnArrival (the user is on the right, so the agent lands
// focused); `r` from the session list sets selectionBound instead (the
// keyboard stays on the list, and the pane follows the selection); the
// startup open sets neither.
type hostRestartedMsg struct {
	name           string
	worktree       string
	focusOnArrival bool
	selectionBound bool
	focusRequest   bool
	// gen carries the originating request's project generation through the
	// restart, because Update REBUILDS a paneOpenRequest from this message: a
	// rebuilt request would otherwise be stamped with the CURRENT generation,
	// laundering a pre-switch restart into a post-switch open — exactly the
	// stale-dial install Model.projectGen exists to drop.
	gen int
	err error
}

// spawnFinishedMsg carries the outcome of a BACKGROUND `afk new --no-dash`
// spawn (see spawnNewCmd). Output is the child's whole combined stdout+stderr,
// captured but never shown live.
type spawnFinishedMsg struct {
	name   string
	err    error
	output string
}

// cleanFinishedMsg carries the outcome of an IN-PROCESS clean (see
// cleanSessionCmd). No child process runs in this path, so err is the seam's
// own error (already carrying the failing git command's stderr), and result is
// what to report on success — which route ran decides what that is: a kept
// branch and archive with the worktree removed for a planned session, an
// archive with the worktree and branch untouched for a stranded
// `.afk/{name}/` (KindStatusWithoutWorktree), nothing archived for a
// worktree-side fault. cleanSucceededStatus reads the result's own fields
// rather than the kind, so a new route reports honestly without a new branch
// there.
//
// kind is the reduced clean-target kind the dispatch was made on, echoed back
// so the handler clears the right cleaningSessions entry: the set is keyed by
// (name, kind), and on an error path result.Kind is not populated to recover it
// from.
type cleanFinishedMsg struct {
	name   string
	kind   session.Kind
	result session.CleanResult
	err    error
}

// cleanMergeStateMsg carries one ancestry check's answer back to the clean
// confirmation modal (see cleanMergeStateCmd). target and kind are the identity
// the modal was opened on, echoed so a late answer can be matched against what
// is on screen NOW rather than assumed to describe it — the modal it was asked
// for may have been cancelled and another opened since.
//
// base is the branch the check actually ran against, empty when none could be
// resolved; merged is false in that case and means nothing.
type cleanMergeStateMsg struct {
	target string
	kind   session.Kind
	base   string
	merged bool
}

// paneRedrawMsg fires when the hosted child produced output the pane should
// repaint. It carries the originating pane so a late notification from an
// already-closed pane can be discarded. termpane's Changed channel coalesces
// bursts into a single pending token, so one paneRedrawMsg stands in for many
// child writes.
type paneRedrawMsg struct {
	pane paneHandle
}

// paneExitedMsg fires when the pane's connection ends. That is usually the
// session ending, but not always: a second client connecting steals the
// session and this pane is disconnected with the session still running. err
// is the child's exit error (nil for a clean end; an internal error for a
// contained emulator panic). reason is why the connection ended, when the
// pane can say (hostpane.go's detachReasoner) — host.DetachStolen is the one
// value that changes what Update does with the row, since marking a stolen
// session's live host stopped pins the row at "stopped" until its PID
// changes. Zero means the pane could not say, which Update treats as a real
// exit.
type paneExitedMsg struct {
	pane   paneHandle
	err    error
	reason host.DetachReason
}

// paneOpenRequest groups the parameters for opening a pane, so they travel WITH
// the specific attach rather than living on the Model (where they would leak
// across a failed attach and get consumed by a later unrelated one).
type paneOpenRequest struct {
	name     string
	worktree string

	// scratch marks the target a one-off session (discover.SessionState.Scratch)
	// and is consumed ONLY by restartHostCmd, which uses it to pick the spawn
	// seam: a scratch host's child is a bare `claude`, and respawning it through
	// the conductor seam would launch `/afk:go --session <name>` against a plan
	// that does not exist — the conductor would then write a status.json into
	// `.afk/{name}/`, which stops the session being a one-off to every
	// scanner and changes its teardown under the user: whichever status-backed
	// route it matches after that, the one-off's stop-the-host-and-remove is no
	// longer the one that runs. Every dispatch site
	// that can restart a stopped host must copy it from the row's own Scratch
	// flag; openPaneFor ignores it (an attach dials whatever child the host
	// already runs).
	scratch bool

	restarted      bool
	preview        bool
	focusOnArrival bool

	// selectionBound marks an open made for the SELECTED row without moving
	// the focus — `r` restarting a stopped row from the session list. Such a
	// pane belongs to that selection: if the list-focused cursor has left the
	// row by the time it lands, applyPaneAttached drops it rather than install
	// a pane that would hold the host's one client slot undrawn. The startup
	// and post-create opens deliberately do not set it — they may target a row
	// that was never selected.
	selectionBound bool

	// focusRequest marks an open that a click-to-focus asked for, and exists ONLY
	// to select applyPaneAttached's arrival wording. focusOnArrival cannot carry
	// that because startup and other non-click workflows have their own wording.
	// Nothing branches on this beyond the status line.
	focusRequest bool

	// wait is the retry-dial window; zero means use host.DefaultAttachWait.
	wait time.Duration

	// gen is the project generation this open belongs to (Model.projectGen — see
	// its doc for the race it closes). It is stamped inside openPaneFor and
	// restartHostCmd, the two dispatch funnels, never at a call site: a site that
	// could drop the stamp would ship a dial no project switch can invalidate.
	gen int

	// previewGen stamps a dispatched preview with the preview generation current
	// after openPaneFor registered it. A later explicit surface choice bumps the
	// generation, so applyPaneAttached can close the already-dispatched loser.
	// It is meaningful only when preview is true.
	previewGen int
}

// paneAttachedMsg reports the outcome of the socket-backed pane the dashboard
// opens over a session's detached host. It is distinct from paneOpenedMsg: the
// child here is not a process this dashboard started, it is a host that was
// already running, so a failure means "could not reach the host" rather than
// "could not launch claude".
// restarted records whether this open followed a restart-flow host spawn. That
// is what lets an unreachable host be phrased as "the host we just started
// died, read its log" instead of "your host record was stale, r to
// restart" — advice that has already been tried and failed.
// The request's dial WINDOW is deliberately not echoed back here. It is consumed
// entirely on the way out (req.wait → hostPaneConfig.Wait, below), and nothing in
// applyPaneAttached branches on how long the dial was allowed to take — the
// preview flag is what that branching keys off. A field carried purely because the
// request had one is dead surface that reads as a contract.
// gen IS echoed back, unlike wait, because applyPaneAttached branches on it: an
// arrival whose generation predates the current project is dropped and its
// connection closed (see Model.projectGen).
type paneAttachedMsg struct {
	pane           paneHandle
	name           string
	worktree       string
	restarted      bool
	preview        bool
	focusOnArrival bool
	selectionBound bool
	focusRequest   bool
	gen            int
	previewGen     int
	err            error
}

// tickCmd schedules the next refresh tick after m.interval.
func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// defaultScanInterval is the project-scan cadence used until the first
// LoadConfig result lands, and the fallback for a Model whose scanInterval was
// never set at all. It mirrors internal/daemonconfig's DefaultScanInterval
// rather than reading it, because this package does not import the settings
// reader (see SearchSettings) — the two have to move together.
const defaultScanInterval = 60 * time.Second

// scanTickMsg fires on the PROJECT-scan ticker, which is a second timer
// deliberately independent of the discovery tick above. The 2s tick reads a
// handful of files inside one worktree; a scan walks whole search roots, and
// putting that walk on the UI's refresh cadence would spend a disk crawl every
// couple of seconds on a list that changes when a user creates a repo.
type scanTickMsg time.Time

// projectScanMsg carries the result of one scan pass: the projects the walk
// found, one discovery result per project keyed by project path, and the config
// values that pass read.
//
// One message rather than one per project, because Update applies the list and
// the cache together — a cache that landed a tick before its list would be keyed
// on paths the switcher has no rows for.
//
// err reports a config file that exists but could not be parsed, or a `[dash]`
// value the settings reader refuses (a MISSING file is not an error; the
// reader answers it with defaults). configPath is set even on that path:
// LoadConfig returns the path with its error, and the empty state has to be
// able to name the file that needs fixing.
type projectScanMsg struct {
	projects   []project.Project
	sessions   map[string][]discover.SessionState
	configPath string
	interval   time.Duration
	err        error
	// rendezvous carries the click-to-focus rendezvous this pass resolved for
	// projects that did not already have one, keyed by project path — the git
	// common dir, plus a live watcher over it.
	//
	// Resolving it here rather than in Update is forced twice over: it is a git
	// subprocess and an fsnotify registration, both I/O, and Update performs none
	// by package contract. Passing a live watcher on a message is the price, and
	// applyRendezvous pays it by closing any duplicate it cannot adopt.
	//
	// Only the NEW ones: a project whose rendezvous is already held is skipped
	// before the subprocess runs, so the steady state costs one rev-parse per
	// unresolved project per pass rather than one per project.
	rendezvous map[string]focusRendezvous
}

// listedProjectsMsg carries one read of the machine's project list: the
// entries, the file's path, and the error for a file that exists but could not
// be read or is not valid. path is set on the error path too, so the notice
// can name the file.
//
// It is its own message rather than a field of projectScanMsg because the scan
// answers only after its whole folder walk, and because the two reads fail
// independently: a bad `[dash]` value fails the search's read of config.toml
// and must not hide the listed projects.
type listedProjectsMsg struct {
	entries []ListedProject
	path    string
	err     error
}

// listedCmd reads the project list off the UI thread, and does nothing else.
// It returns nil — no Cmd at all — when the LoadListed seam is absent, the same
// inertness contract as scanCmd: a Model built without the seam never reads the
// developer's real config.toml.
func (m Model) listedCmd() tea.Cmd {
	load := m.deps.LoadListed
	if load == nil {
		return nil
	}
	return func() tea.Msg {
		entries, path, err := load()
		return listedProjectsMsg{entries: entries, path: path, err: err}
	}
}

// scanTickCmd schedules the next project scan at the configured interval, or
// returns nil when there is nothing for the tick to do — an armed ticker is
// itself work, and a Model that can neither scan nor read the project list must
// not carry one. Either is enough to arm it: the tick re-reads the project list
// as well as scanning, and a dashboard with no search settings still has to
// notice a project added to the list.
func (m Model) scanTickCmd() tea.Cmd {
	if !m.scanEnabled() && m.deps.LoadListed == nil {
		return nil
	}
	interval := m.scanInterval
	if interval <= 0 {
		interval = defaultScanInterval
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg { return scanTickMsg(t) })
}

// scanCmd runs one project scan off the UI thread: read the config, walk its
// search roots, then run discovery once per project.
//
// It returns nil — no Cmd whatsoever — when the Scan or LoadConfig seam is
// absent, and that inertness is a contract rather than a convenience.
// withDefaults wires neither seam on purpose (see Deps), so a Model built
// without them never reads the developer's real ~/.config/afk/config.toml and
// never crawls their disk. `afk dash` opts in; nothing else does.
//
// PINNED, LISTED and STARTED projects are discovered alongside the scanned
// ones, captured here at Cmd-creation time in this package's standing style
// (scanExtraPaths). They survive the merge in applyProjectScan by definition —
// no search root produces the pinned start-up row, and a listed project or a
// folder a new plan was started in stays whether or not the search finds it —
// so leaving them out would give the switcher rows with
// no session data, and for a user with no search folders those rows are the
// entire list. A listed folder that is gone has no row and is not covered.
//
// The listed paths are the ones HELD when the Cmd is created; this Cmd does not
// read the project list itself (listedCmd does, and only that). A read that
// changes the listed folders asks for a fresh pass, see applyListedResult.
//
// A project whose discovery fails contributes no cache entry rather than an
// error for the whole pass: this covers every project at once, and one
// unreadable repo must not blank the counts for the rest. The ACTIVE project's
// discovery failures still surface, through loadCmd's sessionsLoadedMsg.
//
// This pass also resolves each project's click-to-focus rendezvous — one
// rev-parse and, for a project this dashboard is not already watching, one
// fsnotify registration. It rides the scan rather than the 2s tick for the same
// reason the walk does: it is per-project subprocess work for a set that changes
// when someone creates a repo. The same failure rule applies, per project rather
// than per pass — an unresolvable or unwritable rendezvous costs that project its
// click-to-focus and nothing else.
func (m Model) scanCmd() tea.Cmd {
	if !m.scanEnabled() {
		return nil
	}
	loadConfig, walk, discoverIn := m.deps.LoadConfig, m.deps.Scan, m.deps.Discover
	runGit, startWatch := m.deps.RunGit, m.deps.StartFocusWatcher
	extra := scanExtraPaths(m.projects, m.listed, m.startedProjects)
	held := m.rendezvousProjects()
	launchDir := m.gitCommonDir
	return func() tea.Msg {
		cfg, configPath, err := loadConfig()
		if err != nil {
			return projectScanMsg{configPath: configPath, err: err}
		}
		found := walk(cfg.Roots, cfg.MaxDepth)
		sessions := make(map[string][]discover.SessionState, len(found)+len(extra))
		rendezvous := make(map[string]focusRendezvous, len(found)+len(extra))
		resolve := func(path string) {
			if held[path] {
				return
			}
			if _, done := rendezvous[path]; done {
				return
			}
			dir, rerr := resolveRendezvousDir(runGit, path)
			if rerr != nil || dir == "" {
				return
			}
			rz := focusRendezvous{dir: dir}
			// No second watcher over the LAUNCH directory: Model.focusWatch is already
			// watching it, and two watchers on one directory wake twice per click.
			if dir != launchDir && startWatch != nil {
				rz.watch = startWatch(dir)
			}
			rendezvous[path] = rz
		}
		for _, p := range found {
			if states, derr := discoverIn(p.Path); derr == nil {
				sessions[p.Path] = states
			}
			resolve(p.Path)
		}
		for _, pin := range extra {
			resolve(pin)
			if _, done := sessions[pin]; done {
				continue
			}
			if states, derr := discoverIn(pin); derr == nil {
				sessions[pin] = states
			}
		}
		return projectScanMsg{
			projects:   found,
			sessions:   sessions,
			configPath: configPath,
			interval:   cfg.Interval,
			rendezvous: rendezvous,
		}
	}
}

// loadCmd runs one discovery pass off the UI thread: it enumerates sessions
// (git worktree list + file reads via internal/discover), reads the last
// tailLines of each session's learnings.md / issues.md, and computes each
// session's diff stats against its base branch (diff.go's dual-cost
// pattern — the currently-focused session gets the full diff; every other
// session gets the cheap --numstat aggregate). All of this is
// file/subprocess I/O, so it lives here in a Cmd, never in Update.
//
// The focused session name is captured at Cmd-creation time (pure, from the
// Model's current cursor) — not re-read inside the async closure — so the
// dual-cost decision reflects what was selected when this tick fired.
func (m Model) loadCmd() tea.Cmd {
	// The backstop for every dispatch site (there are a dozen and a half, most of
	// them message handlers): with no active project there is no repo to enumerate,
	// and running git worktree list against a directory that is not a repo parks a
	// permanent error in the footer. hasActiveProject keys on the repo, never on the
	// project list — see its own comment for why that distinction matters.
	if !m.hasActiveProject() {
		return nil
	}
	repo := m.repo
	deps := m.deps
	// Captured here, at Cmd-creation time, exactly like repo above — and stamped
	// onto the resulting message so a project switch that lands before this async
	// pass returns can be told apart from the load it actually belongs to (see
	// sessionsLoadedMsg.gen).
	gen := m.projectGen
	focusedName := ""
	if s, ok := m.selected(); ok {
		focusedName = s.Name
	}
	// Captured here too, at Cmd-creation time, for the same reason
	// focusedName is: the Diff tab's text is only worth the memory to retain
	// while that tab is actually on screen (see Session.DiffText), and
	// activeDetailTab handles a Stack tab chosen before a rescan found
	// git-spice gone.
	retainText := m.activeDetailTab() == tabDiff
	return func() tea.Msg {
		states, err := deps.Discover(repo)
		if err != nil {
			return sessionsLoadedMsg{err: err, at: deps.Now(), gen: gen}
		}
		sessions := make([]Session, len(states))
		for i, st := range states {
			s := Session{SessionState: st}
			base := filepath.Join(st.WorktreePath, ".afk", st.Name)
			s.LearningsTail = deps.ReadTail(filepath.Join(base, "learnings.md"), tailLines)
			s.IssuesTail = deps.ReadTail(filepath.Join(base, "issues.md"), tailLines)
			s.DiffStats, s.DiffText = loadDiffStats(deps, st, st.Name == focusedName, retainText)
			sessions[i] = s
		}
		return sessionsLoadedMsg{sessions: sessions, at: deps.Now(), gen: gen}
	}
}

// restartHostCmd re-spawns the detached host for a stopped session off the UI
// thread and reports the outcome as a hostRestartedMsg. It CONSUMES
// internal/session's exported spawn helper (via deps.SpawnHost) rather than
// reimplementing the launch.
//
// The attach is chained off the resulting message in Update instead of being
// run inside this same Cmd, so a spawn failure surfaces as guidance rather than
// an attach against a host that was never started. A successful spawn waits
// here for readable host metadata before reporting success; host.Dial starts
// later and therefore cannot absorb that pre-dial publication race.
//
// It takes the whole paneOpenRequest (rather than the three fields it echoes)
// because it is the front half of one open: the hostRestartedMsg handler rebuilds
// a request from it, and every flag that has to survive the restart is already
// grouped here. req.restarted, req.preview and req.wait are the handler's to set —
// a preview never restarts a host.
//
// The POINTER receiver is load-bearing, for the reason schedulePreview's comment
// gives: previewGen is bumped below, and on a value receiver that increment lands
// on a copy Update discards. It compiles, the suite stays green, and the
// cancellation silently does nothing.
func (m *Model) restartHostCmd(req paneOpenRequest) tea.Cmd {
	// Same invalidation openPaneFor performs, and needed for the same reason on a
	// path that does not go through it: a spawn takes far longer than the settle,
	// so a timer armed before this keypress would otherwise fire mid-restart and
	// steal the pane back to whatever row the cursor was on.
	m.previewGen++
	// Stamped here rather than trusted from the caller, mirroring openPaneFor:
	// the two are the only dispatch funnels, so between them no request can leave
	// Update ungenerated.
	req.gen = m.projectGen
	// The seam is chosen by the row's own kind (req.scratch, copied from
	// discover.SessionState.Scratch at every dispatch site): a one-off session's
	// host runs a bare `claude`, and respawning it through the conductor seam
	// would start `/afk:go --session <name>` against a plan that does not
	// exist and write a status.json that stops the session being a one-off (see
	// paneOpenRequest.scratch). Two seams rather than one spec-shaped one
	// because Deps.SpawnHost's signature is load-bearing for its injectors.
	spawn := m.deps.SpawnHost
	if req.scratch {
		spawn = m.deps.SpawnScratchHost
	}
	name, worktree := req.name, req.worktree
	return func() tea.Msg {
		_, err := spawn(name, worktree)
		if err == nil {
			err = m.deps.WaitHostReady(worktree, name)
		}
		return hostRestartedMsg{
			name:           name,
			worktree:       worktree,
			focusOnArrival: req.focusOnArrival,
			selectionBound: req.selectionBound,
			focusRequest:   req.focusRequest,
			gen:            req.gen,
			err:            err,
		}
	}
}

// restartedHostMetaWait is the narrow startup bridge between a successful
// spawn and host.json publication. It matches package main's attach retry:
// Serve normally writes the record within milliseconds, so 500ms is bounded
// recovery rather than another general attach window.
const restartedHostMetaWait = 500 * time.Millisecond

func defaultWaitHostReady(worktree, name string) error {
	return waitForRestartedHostMeta(host.ReadMeta, worktree, name, restartedHostMetaWait, time.Now, time.Sleep)
}

// waitForRestartedHostMeta retries only restart-driven metadata readiness.
// Ordinary opens and previews still reach hostSocketPath immediately. The
// backoff matches host.Dial and package main's readMetaWithRetry: immediate
// first attempt, 10ms doubling to a 100ms cap, and the final read error once
// the deadline is exhausted. now/sleep are injected for deterministic tests.
func waitForRestartedHostMeta(
	readMeta func(worktree, name string) (host.Meta, error),
	worktree, name string,
	wait time.Duration,
	now func() time.Time,
	sleep func(time.Duration),
) error {
	deadline := now().Add(wait)
	backoff := 10 * time.Millisecond
	for {
		if _, err := readMeta(worktree, name); err == nil {
			return nil
		} else if remaining := deadline.Sub(now()); remaining <= 0 {
			return err
		} else if backoff > remaining {
			backoff = remaining
		}
		sleep(backoff)
		if next := backoff * 2; next < 100*time.Millisecond {
			backoff = next
		} else {
			backoff = 100 * time.Millisecond
		}
	}
}

// spawnNewCmd runs `afk new --no-dash <name>` in the BACKGROUND and reports
// the outcome as a spawnFinishedMsg. Nothing is suspended: the dashboard keeps
// ticking and redrawing for the whole (slow) create, and the pending session
// shows up as a placeholder row until discovery finds the real thing.
//
// The child gets no stdin (nil == /dev/null), so `afk new`'s interactive
// git-spice base picker cannot run in this path. That is why base is decided
// HERE, in the prompt modal, and passed through as --base: without it a
// dashboard-spawned session would silently branch off trunk. An empty base
// means "use the non-interactive default", which is exactly trunk.
//
// CAUTION: CombinedOutput gives the child a pipe for stdout and stderr, and
// Wait does not return until both reach EOF. That is safe ONLY because the
// detached session host this create spawns (`afk _host`) points its own
// Stdin/Stdout/Stderr at explicit /dev/null files rather than inheriting ours
// (internal/session.SpawnHost, W3.T1). A grandchild that inherited these pipes
// would hold them open for the session's whole lifetime, so this Cmd — and
// therefore the "creating…" placeholder and the auto-attach chained off it —
// would never complete.
func (m Model) spawnNewCmd(name, base, description string, autonomous bool, providerName string) tea.Cmd {
	c := m.spawnNewExecCmd(name, base, description, autonomous, providerName)
	return func() tea.Msg {
		out, err := c.CombinedOutput()
		return spawnFinishedMsg{name: name, err: err, output: string(out)}
	}
}

// cleanSessionCmd runs the clean IN-PROCESS, off the event loop, and reports
// the outcome as a cleanFinishedMsg. It is the ONLY caller of the
// Deps.CleanSession seam, and therefore the only path by which
// session.CleanSession ever runs inside this package — the work happens in the
// returned closure, which Bubble Tea executes on its own goroutine, never on the
// event-loop goroutine that owns Update.
//
// The seam resolves the worktree and branch itself, so the dashboard never
// has to remember them across the confirmation modal — only what the user
// confirmed travels: the name, the phase, and the two halves of the kind
// (scratch, and the worktree-side fault), the last two because the name alone
// distinguishes neither a scratch session from a planned one sharing it nor a
// healthy session from a broken entry on the same branch (see
// resolveCleanTarget). repo and the seam are captured at Cmd-construction time
// (as loadCmd does), so a Model copy taken later cannot change what this Cmd
// operates on.
func (m Model) cleanSessionCmd(name, confirmedPhase string, scratch bool, kind session.Kind, opts session.CleanOptions) tea.Cmd {
	repo := m.repo
	clean := m.deps.CleanSession
	return func() tea.Msg {
		result, err := clean(repo, name, confirmedPhase, scratch, kind, opts)
		return cleanFinishedMsg{name: name, kind: kind, result: result, err: err}
	}
}

// cleanMergeStateCmd asks whether the confirmed target's branch is already
// merged, OFF the event loop, and reports the answer as a cleanMergeStateMsg.
//
// A tea.Cmd rather than a call inside Update because the check is a git
// invocation: run synchronously it would stall every row, every ticker and
// every keypress for as long as git takes, on a dashboard whose whole design
// (see internal/discover's "it must never block") is built around not doing
// that. The cost of the asynchrony is that `d` can be pressed before the
// answer lands, which updateConfirmClean resolves in the safe direction rather
// than by waiting.
//
// name and kind are captured here and echoed back in the message so
// applyCleanMergeState can drop an answer for a modal that is no longer up;
// repo and the seam are captured at Cmd-construction time as every other Cmd
// in this file does.
func (m Model) cleanMergeStateCmd(name, branch, base string, kind session.Kind) tea.Cmd {
	repo := m.repo
	branchMerged := m.deps.BranchMerged
	return func() tea.Msg {
		merged, resolvedBase := branchMerged(repo, branch, base)
		return cleanMergeStateMsg{target: name, kind: kind, base: resolvedBase, merged: merged}
	}
}

// spawnNewExecCmd builds the exec.Cmd for the background create. It only
// populates fields and never runs anything, which makes it the assertable seam
// for the argv/Dir/stdio contract.
//
// Stdout and Stderr MUST stay nil — CombinedOutput refuses to run a Cmd that
// already has either set. Dir is the repo root so `afk new` resolves the same
// repository the dashboard is watching.
func (m Model) spawnNewExecCmd(name, base, description string, autonomous bool, providerName string) *exec.Cmd {
	c := exec.Command(afkBinary(), spawnNewArgs(name, base, description, autonomous, providerName)...)
	c.Dir = m.repo
	return c
}

// spawnNewArgs builds the argv for the background create:
// ["new", "--no-dash", "--base", base, "--description", description,
// "--autonomous", "--provider", providerName, name], with optional pairs
// omitted when empty/whitespace and --autonomous present only when the form's
// checkbox was ticked (a bare bool flag, never `--autonomous=false`, so an
// unchecked create's argv is exactly what it was before the checkbox existed).
// --provider is likewise emitted only for a non-default provider: "" and
// provider.Default are omitted, so a claude create's argv is byte-identical to
// what it was before the selector existed and `afk new` resolves the default
// itself. Pure, so the order is assertable without running anything.
//
// EVERY flag precedes the positional name, and that ordering is load-bearing,
// not stylistic: Go's flag package stops parsing at the first non-flag token, so
// `afk new <name> --no-dash` would treat --no-dash as a second positional
// argument and fail `afk new`'s "exactly one session <name>" check.
func spawnNewArgs(name, base, description string, autonomous bool, providerName string) []string {
	args := []string{"new", "--no-dash"}
	if base != "" {
		args = append(args, "--base", base)
	}
	if strings.TrimSpace(description) != "" {
		args = append(args, "--description", description)
	}
	if autonomous {
		args = append(args, "--autonomous")
	}
	if providerName != "" && providerName != provider.Default {
		args = append(args, "--provider", providerName)
	}
	return append(args, name)
}

// spawnScratchArgs builds the argv for the background one-off create:
// ["new", "--no-dash", "--scratch", "--provider", providerName]. No base, no
// description, and — unlike spawnNewArgs — no positional name: `afk new`
// allocates a scratch session's name itself (session.NextScratchName, W2.T1)
// whenever it is given none, so there is nothing for the caller to supply.
// --provider follows spawnNewArgs's rule: emitted only for a non-default
// provider, so "" and provider.Default leave a claude one-off's argv
// byte-identical to what it was before the dashboard could pick a provider and
// `afk new` resolves the default itself. Pure, so the argv is assertable
// without running anything.
func spawnScratchArgs(providerName string) []string {
	args := []string{"new", "--no-dash", "--scratch"}
	if providerName != "" && providerName != provider.Default {
		args = append(args, "--provider", providerName)
	}
	return args
}

// spawnScratchExecCmd builds the exec.Cmd for the background one-off create.
// Mirrors spawnNewExecCmd's contract exactly (Dir is the repo root, Stdout
// and Stderr stay nil for CombinedOutput) — it only populates fields and
// never runs anything, which is what makes it the assertable seam. The
// provider is baked into argv here, at construction, so the Cmd a caller later
// runs cannot disagree with the provider that was chosen when it was built.
func (m Model) spawnScratchExecCmd(providerName string) *exec.Cmd {
	c := exec.Command(afkBinary(), spawnScratchArgs(providerName)...)
	c.Dir = m.repo
	return c
}

// spawnScratchCmd runs `afk new --no-dash --scratch [--provider X]` in the
// BACKGROUND and reports the outcome as a spawnFinishedMsg carrying name —
// reusing spawnFinishedMsg's existing handler wholesale (clear the pending
// row, open the error modal on failure, open the pane on success) rather than
// adding a second one for scratch.
//
// name is the dashboard's own guess (openScratch's in-memory
// session.NextScratchName, corrected against the `.afk/` disk listing by
// applyScratchDirs before this Cmd is built), NOT something afk new was
// told to use — spawnScratchArgs carries no name. The two computations agree
// because afk new's child process runs NextScratchName over the same
// listing moments later and nothing else in this repo allocates a scratch
// name in between; see decisions.md (W3.T1) for the residual race this
// leaves and why it is out of scope here.
//
// The child is run through Deps.RunSpawn rather than c.CombinedOutput()
// directly: afkBinary() is the running executable, which under `go test` is
// the test binary itself, so a test that wants to observe the argv a
// dispatched create carries must be able to intercept the run instead of
// executing a child.
func (m Model) spawnScratchCmd(name, providerName string) tea.Cmd {
	c := m.spawnScratchExecCmd(providerName)
	run := m.deps.RunSpawn
	return func() tea.Msg {
		out, err := run(c)
		return spawnFinishedMsg{name: name, err: err, output: string(out)}
	}
}

// afkBinary resolves the path of the running afk binary, falling back to
// argv[0]. The create and attach exec helpers both need this resolution.
func afkBinary() string {
	self, err := os.Executable()
	if err != nil || self == "" {
		return os.Args[0]
	}
	return self
}

// paneAttachFailedStatus is the notification text for a pane that could not
// be opened.
//
// An ErrHostNotReachable failure is the one the user can act on, and its
// phrasing turns on msg.restarted: an unreachable host on a first attempt means
// the host record was stale and `r` (wedgeRecoveryKey, the one restart key)
// will fix it, while an unreachable host right after a restart means the host
// we just spawned died on startup and only its log says why.
// Model.applyPaneAttached pairs this with markHostStopped, so the row reads
// stopped and the "r to restart" hint is true by the time it is read.
func paneAttachFailedStatus(leader string, msg paneAttachedMsg) string {
	if errors.Is(msg.err, host.ErrHostNotReachable) {
		if msg.restarted {
			return fmt.Sprintf("%s didn't start — see %s",
				msg.name, hostLogPath(msg.worktree, msg.name))
		}
		return fmt.Sprintf("%s isn't running — its host record was stale · %s to restart",
			msg.name, keyName(leader, levelProject, actionRestart))
	}
	return fmt.Sprintf("opening %s failed: %v — see %s",
		msg.name, msg.err, hostLogPath(msg.worktree, msg.name))
}

// restartFailedStatus is the notification text for a restart flow whose host
// spawn itself failed, before any attach was attempted.
func restartFailedStatus(name, worktree string, err error) string {
	return fmt.Sprintf("restarting %s failed: %v — see %s",
		name, err, hostLogPath(worktree, name))
}

// hostLogPath is where a session host records why its child died:
// `<worktree>/.afk/<name>/host.log`, the same path internal/session's spawn
// helper hands back. Every attach/restart failure status points at it, because
// it is the only place the actual cause (e.g. claude exiting immediately) is
// written down — the dashboard itself never sees the host's output.
func hostLogPath(worktree, name string) string {
	return filepath.Join(worktree, ".afk", name, "host.log")
}

// spawnFailedHeadline is the first line of the spawn-error modal: which session
// failed to be created, and why.
func spawnFailedHeadline(name string, err error) string {
	return fmt.Sprintf("creating %s failed: %v", name, err)
}

// spawnErrorTailLines is how many trailing output lines the spawn-error modal
// shows. The modal floats over the frame with room of its own, so it can afford
// more than the single row a notification gets — but not so many that the
// dialog covers the body it is reporting about.
const spawnErrorTailLines = 4

// spawnOutputTail reduces a failed background create's captured combined output
// to the last spawnErrorTailLines non-blank lines, in original order. Blank
// lines are dropped rather than kept: `afk new` prints a spaced-out action
// list, and a modal padded with empty rows would hide the actual failure.
func spawnOutputTail(output string) []string {
	var lines []string
	for _, ln := range strings.Split(output, "\n") {
		if trimmed := strings.TrimRight(ln, "\r \t"); strings.TrimSpace(trimmed) != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) > spawnErrorTailLines {
		lines = lines[len(lines)-spawnErrorTailLines:]
	}
	return lines
}

// spawnWarnings extracts the "warning: " lines from a background create's
// captured output (the exact lowercase prefix Plan.Run's bestEffort branch
// writes in internal/session/create.go), in order and deduplicated, with
// the prefix removed. Plan.Run's line has the shape "warning: <step Desc>:
// <err>" — the step Desc was already shown as one of the printed action
// steps on the way to a clean exit, so repeating it on the one-line status
// row would double its length for no new information. A line is only
// treated as having that shape when it contains at least two ": "
// separators (one from "warning: ", one between Desc and err); such a line
// keeps just the text after the last separator. A line with none or one
// (no embedded ": ") is kept in full, minus the prefix.
func spawnWarnings(output string) []string {
	const prefix = "warning: "
	var out []string
	seen := make(map[string]bool)
	for _, ln := range strings.Split(output, "\n") {
		trimmed := strings.TrimRight(ln, "\r \t")
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		w := strings.TrimPrefix(trimmed, prefix)
		if strings.Count(trimmed, ": ") >= 2 {
			if idx := strings.LastIndex(trimmed, ": "); idx >= 0 {
				w = trimmed[idx+len(": "):]
			}
		}
		if seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// appendWarningSuffix adds a successful create's first warning (see
// spawnWarnings) to a notification's outcome, plus a "(+N more)" count when
// there is more than one. A notification is one line, so spawnFinishedMsg's
// success path — unlike the failure modal, which has room of its own for
// spawnOutputTail's lines — cannot afford to dump the full warning list.
//
// The warning joins as a "·" CLAUSE rather than an em-dash qualification,
// because both of its call sites already spend their one permitted em-dash on
// the outcome ("created x — connecting…"). It is appended to the outcome
// alone, before any key hint: the house style at the top of this file wants
// the final "·" to introduce the hint, so the caller with a hint
// ("created x · enter/f to open the agent") wraps the outcome in this and
// adds the hint afterwards.
func appendWarningSuffix(status string, warnings []string) string {
	if len(warnings) == 0 {
		return status
	}
	status += " · " + warnings[0]
	if len(warnings) > 1 {
		status += fmt.Sprintf(" (+%d more)", len(warnings)-1)
	}
	return status
}

// attachPaneCmd opens an embedded pane over a session's already-running
// detached host, off the UI thread. The pane's child is the host's SOCKET, not a
// process this dashboard started (hostpane.go adapts one to termpane.Child), so
// closing the pane leaves the session running.
//
// Geometry is captured before the closure so it matches the region View will
// draw into. The dial carries a retry window, which absorbs the spawn→attach
// race for a host that was only just started.
func (m Model) attachPaneCmd(req paneOpenRequest) tea.Cmd {
	openHost := m.deps.OpenHostPane
	cols, rows := m.paneRegionSize()
	wedgeHint := wedgeHintDashboard(m.leaderKey.name)
	return func() tea.Msg {
		p, err := openHost(hostPaneConfig{
			Name:      req.name,
			Worktree:  req.worktree,
			Cols:      cols,
			Rows:      rows,
			Wait:      req.wait,
			WedgeHint: wedgeHint,
		})
		if err != nil {
			return paneAttachedMsg{
				name:           req.name,
				worktree:       req.worktree,
				restarted:      req.restarted,
				preview:        req.preview,
				focusOnArrival: req.focusOnArrival,
				selectionBound: req.selectionBound,
				focusRequest:   req.focusRequest,
				gen:            req.gen,
				previewGen:     req.previewGen,
				err:            err,
			}
		}
		return paneAttachedMsg{
			pane:           p,
			name:           req.name,
			worktree:       req.worktree,
			restarted:      req.restarted,
			preview:        req.preview,
			focusOnArrival: req.focusOnArrival,
			selectionBound: req.selectionBound,
			focusRequest:   req.focusRequest,
			gen:            req.gen,
			previewGen:     req.previewGen,
		}
	}
}

// paneEventCmd is the pane's redraw/exit subscription: it blocks on the pane's
// Changed and Done channels and returns whichever fires first. Update re-arms
// it on every paneRedrawMsg (the standard Bubble Tea channel-subscription
// pattern) and stops on paneExitedMsg. Selecting on both in one Cmd makes the
// final-screen-then-exit ordering best-effort, not guaranteed: with a pending
// Changed token and a closed Done, select picks either arm at random. When
// Changed wins, the last screen paints and the re-armed Cmd delivers the exit
// next; when Done wins, the exit handler runs against the already-rendered
// pane content (final screen retention is what the tests assert).
func paneEventCmd(p paneHandle) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-p.Changed():
			return paneRedrawMsg{pane: p}
		case <-p.Done():
			msg := paneExitedMsg{pane: p, err: p.Err()}
			if r, ok := p.(detachReasoner); ok {
				msg.reason = r.detachReason()
			}
			return msg
		}
	}
}

// --- focused-pane input routing (W4.T1) ---

// forwardPasteToPane routes a bracketed paste to the hosted child. It hands the
// whole payload to the paste path so the emulator wraps it (when the child
// enabled bracketed-paste mode) and a multi-line paste arrives as one unit
// rather than a burst of Enters.
//
// It is a separate entry point from forwardKeyToPane because Bubble Tea v2
// delivers a paste as its own tea.PasteMsg (paste.go) rather than as a KeyMsg
// carrying a Paste flag, so the two arrive at Update as different message types
// and cannot share one classifier the way they did under v1.
func forwardPasteToPane(p paneHandle, content string) {
	p.Paste(content)
}

// forwardKeyToPane routes a dashboard keypress to the hosted child through
// termpane's mode-aware input API. It deliberately does NOT translate keys to
// bytes: the UI classifies a keypress (literal text vs. a special key) and hands
// identity — a rune plus modifiers — to the emulator, which owns the encoding
// (DECCKM application-cursor arrows, ctrl chords, alt prefixes). Pastes come in
// on their own message type and go through forwardPasteToPane. Anything the
// emulator has no encoding for (e.g. ctrl/shift-modified arrows, F13+) is
// silently dropped by SendKey; that is acceptable for the spike (claude's
// interactions are plain arrows, enter, backspace, Ctrl+C, and text), and the
// reflow-critical and control paths are all covered. These are synchronous
// in-memory calls on the pane handle (like the pane.Close on quit), not
// tea.Cmds, so the caller invokes them directly from Update.
//
// The classification is keyed on Key.Text, which v2 populates only for keys that
// represent printable characters (bubbletea key.go's Key doc; space included —
// ultraviolet's legacy table decodes SP as {Code: KeySpace, Text: " "}). That
// replaces v1's KeyRunes/KeySpace KeyType test, which has no v2 equivalent.
func forwardKeyToPane(p paneHandle, k tea.KeyPressMsg) {
	switch {
	case k.Text != "":
		// Printable input, forwarded verbatim. Text can hold more than one rune
		// (IME composition), same as v1's Runes slice.
		p.SendText(k.Text)

	case k.Mod.Contains(tea.ModAlt) && !k.Mod.Contains(tea.ModCtrl) && unicode.IsPrint(k.Code):
		// An alt-prefixed printable rune. v2 reports it as the bare Code plus
		// ModAlt with Text EMPTY (ultraviolet decoder.go's ESC-prefix paths), so
		// it never reaches the branch above and must go through SendKey for the
		// emulator to add the ESC prefix. unicode.IsPrint is safe as the guard
		// because every special-key code is KeyExtended (unicode.MaxRune+1) or
		// higher, which IsPrint rejects.
		p.SendKey(termpane.KeyEvent{Code: k.Code, Mod: vt.ModAlt})

	default:
		ev, ok := specialKeyEvent(k)
		if !ok {
			return
		}
		if k.Mod.Contains(tea.ModAlt) {
			ev.Mod |= vt.ModAlt
		}
		p.SendKey(ev)
	}
}

// specialKeyEvent maps a Bubble Tea non-printable key press to the emulator's
// key-event identity (code + modifiers). It returns ok=false for key presses the
// emulator has no encoding for, so the caller drops them rather than emitting
// garbage — which matters more under v2 than it did under v1: the emulator's
// fallback for an unrecognised event with no modifiers is `string(key.Code)`
// (x/vt key.go's default arm), so forwarding an unmapped F13 would put a
// private-use-area rune on the child's stdin.
//
// It takes the whole key press rather than a key code because v2 has no
// KeyType: two of the mappings below are distinguished only by modifier bits
// (shift+tab is {Code: KeyTab, Mod: ModShift}, and the entire ctrl-chord family
// is the unmodified code plus ModCtrl), where v1 had a distinct KeyType sentinel
// for each. Terminals still fold several chords onto one code — ctrl+m is CR,
// ctrl+i is HT, ctrl+[ is ESC — but v2 resolves that fold in the DECODER rather
// than here: ultraviolet's legacy key table maps those bytes to the named keys
// (KeyEnter/KeyTab/KeyEscape) unless the LegacyKeyEncoding CtrlM/CtrlI/
// CtrlOpenBracket flags are set, and bubbletea v2 never sets them. So the
// named-key cases still win, for the same reason and with the same result.
func specialKeyEvent(k tea.KeyPressMsg) (termpane.KeyEvent, bool) {
	// Ctrl chords are tested first because v2 reports them as an ORDINARY code
	// plus ModCtrl — ctrl+u is {Code: 'u', Mod: ModCtrl}, not a sentinel — so
	// they would otherwise fall through the named-key switch below and be
	// dropped. The one code this steals from the switch is KeySpace with
	// ModCtrl, which is the NUL chord and belongs to ctrlChordEvent anyway.
	if k.Mod.Contains(tea.ModCtrl) {
		return ctrlChordEvent(k.Code)
	}

	switch k.Code {
	case tea.KeyEnter:
		return termpane.KeyEvent{Code: vt.KeyEnter}, true
	case tea.KeyTab:
		// Shift+tab shares tab's code in v2 and is told apart only by ModShift
		// (ultraviolet decodes CSI Z to {Code: KeyTab, Mod: ModShift}); v1 had a
		// KeyShiftTab sentinel, which v2 deleted.
		if k.Mod.Contains(tea.ModShift) {
			return termpane.KeyEvent{Code: vt.KeyTab, Mod: vt.ModShift}, true
		}
		return termpane.KeyEvent{Code: vt.KeyTab}, true
	case tea.KeyBackspace:
		return termpane.KeyEvent{Code: vt.KeyBackspace}, true
	case tea.KeyEsc:
		return termpane.KeyEvent{Code: vt.KeyEscape}, true

	// Arrows: the emulator applies DECCKM application-cursor encoding based on
	// the mode the child has set, which is exactly why they must route through
	// SendKey rather than a fixed byte sequence.
	case tea.KeyUp:
		return termpane.KeyEvent{Code: vt.KeyUp}, true
	case tea.KeyDown:
		return termpane.KeyEvent{Code: vt.KeyDown}, true
	case tea.KeyRight:
		return termpane.KeyEvent{Code: vt.KeyRight}, true
	case tea.KeyLeft:
		return termpane.KeyEvent{Code: vt.KeyLeft}, true

	case tea.KeyHome:
		return termpane.KeyEvent{Code: vt.KeyHome}, true
	case tea.KeyEnd:
		return termpane.KeyEvent{Code: vt.KeyEnd}, true
	case tea.KeyPgUp:
		return termpane.KeyEvent{Code: vt.KeyPgUp}, true
	case tea.KeyPgDown:
		return termpane.KeyEvent{Code: vt.KeyPgDown}, true
	case tea.KeyDelete:
		return termpane.KeyEvent{Code: vt.KeyDelete}, true
	case tea.KeyInsert:
		return termpane.KeyEvent{Code: vt.KeyInsert}, true

	case tea.KeyF1:
		return termpane.KeyEvent{Code: vt.KeyF1}, true
	case tea.KeyF2:
		return termpane.KeyEvent{Code: vt.KeyF2}, true
	case tea.KeyF3:
		return termpane.KeyEvent{Code: vt.KeyF3}, true
	case tea.KeyF4:
		return termpane.KeyEvent{Code: vt.KeyF4}, true
	case tea.KeyF5:
		return termpane.KeyEvent{Code: vt.KeyF5}, true
	case tea.KeyF6:
		return termpane.KeyEvent{Code: vt.KeyF6}, true
	case tea.KeyF7:
		return termpane.KeyEvent{Code: vt.KeyF7}, true
	case tea.KeyF8:
		return termpane.KeyEvent{Code: vt.KeyF8}, true
	case tea.KeyF9:
		return termpane.KeyEvent{Code: vt.KeyF9}, true
	case tea.KeyF10:
		return termpane.KeyEvent{Code: vt.KeyF10}, true
	case tea.KeyF11:
		return termpane.KeyEvent{Code: vt.KeyF11}, true
	case tea.KeyF12:
		return termpane.KeyEvent{Code: vt.KeyF12}, true

	default:
		return termpane.KeyEvent{}, false
	}
}

// ctrlChordEvent maps the C0 control-key range (Ctrl+<letter> and the
// Ctrl+[\]^_ family, plus Ctrl+@) to a ModCtrl-modified key event. code is the
// key code of an event that already carries ModCtrl, which under v2 is the
// UNMODIFIED character the user pressed — ctrl+a decodes to {Code: 'a', Mod:
// ModCtrl}, so no arithmetic recovers the letter the way v1's C0-byte-numbered
// KeyType required. The emulator re-encodes it (control chords are mode-
// independent, but routing through SendKey keeps byte knowledge out of the UI,
// per the task's "do not hand-roll byte translation" boundary).
//
// Ctrl+@ is absent from the switch on purpose, and is not a dropped mapping:
// ultraviolet decodes NUL to {Code: KeySpace, Mod: ModCtrl} unless the
// LegacyKeyEncoding CtrlAt flag is set, and bubbletea v2 never sets it — so the
// ctrl+space case IS the ctrl+@ case, exactly as v1's KeyCtrlAt was.
//
// Since T9.1 ctrl+space is not the dashboard's leader (ctrl+] is; leader.go),
// so in the agent pane ctrl+space is an ordinary key: no rule takes it, and it
// reaches here and goes to the agent as one NUL, like any other chord. The
// case below must stay for that reason. The leader pressed twice in the pane
// does not come through here: since T9.2 the leader is configurable, and the
// key strip sends the Model's decoded leader straight to the pane's SendKey
// (sendLeaderToPane, model.go), so x/vt encodes ctrl+] as 0x1D there. The
// `]` case stays for a ctrl+] typed in the pane while another leader is set.
//
// Ctrl+[ is likewise absent because the decoder resolves it to KeyEscape before
// it can reach here (see specialKeyEvent's doc), which is where v1 sent it too.
//
// ctrl+\ and ctrl+g reach here from the dashboard's agent pane. Until T7.2 the
// pane's own rules claimed both before forwarding (the keyboard-back chord and
// mouseChordKey); since T16.2 the dashboard's ways to do those are ctrl+] esc
// and ctrl+] m, and the leader is the Agent's only key, so the agent gets
// the chords like any other key, ctrl+t included. afk attach (attachpane.go)
// still reserves ctrl+\ and ctrl+g, so from there their cases below stay
// unreachable.
func ctrlChordEvent(code rune) (termpane.KeyEvent, bool) {
	switch {
	case code == tea.KeySpace: // ctrl+space / ctrl+@ → NUL
		return termpane.KeyEvent{Code: vt.KeySpace, Mod: vt.ModCtrl}, true
	case code >= 'a' && code <= 'z':
		return termpane.KeyEvent{Code: code, Mod: vt.ModCtrl}, true
	case code == '\\':
		return termpane.KeyEvent{Code: '\\', Mod: vt.ModCtrl}, true
	case code == ']':
		return termpane.KeyEvent{Code: ']', Mod: vt.ModCtrl}, true
	case code == '^':
		return termpane.KeyEvent{Code: '^', Mod: vt.ModCtrl}, true
	case code == '_':
		return termpane.KeyEvent{Code: '_', Mod: vt.ModCtrl}, true
	}
	return termpane.KeyEvent{}, false
}

// stderrTail reduces a child's raw stderr into a short, single-purpose
// summary for the status line: the last 3 lines, then hard-capped at 200
// characters (keeping the tail end, since that's what's most likely to
// carry the actual failure), trimmed of surrounding whitespace.
func stderrTail(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	return strings.TrimSpace(out)
}

// --- default side-effect implementations (the real environment) ---

// readTail returns the last n lines of the file at path, or nil when the
// file is missing/unreadable (a session may not have written a given notepad
// yet — that is a normal, first-class state, not an error).
func readTail(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// defaultSpawnHost wires the restart flow to internal/session's exported spawn
// helper with the real environment. A zero-value session.Deps is correct here:
// SpawnHost fills in every real default itself (including os.Executable, which
// locates the afk binary it re-execs as `afk _host`).
func defaultSpawnHost(name, worktree string) (session.HostProcess, error) {
	return session.SpawnHost(session.HostSpawn{Name: name, Worktree: worktree}, session.Deps{})
}

// defaultSpawnScratchHost is defaultSpawnHost's one-off sibling: Scratch: true
// selects the bare-`claude` child argv (session.hostArgs) instead of the
// conductor's `/afk:go --session <name>`. It exists as a SECOND seam rather
// than a bool parameter on Deps.SpawnHost because that field's two-string
// signature is pinned by its existing injectors; restartHostCmd picks between
// the two off paneOpenRequest.scratch.
func defaultSpawnScratchHost(name, worktree string) (session.HostProcess, error) {
	return session.SpawnHost(session.HostSpawn{Name: name, Worktree: worktree, Scratch: true}, session.Deps{})
}

// defaultDiscover wires the UI to the real internal/discover.Discover with
// the real git binary.
func defaultDiscover(repo string) ([]discover.SessionState, error) {
	return discover.Discover(repo, discover.DefaultDeps())
}

// cleanRunner is the injectable seam INSIDE defaultCleanSession: the three
// environment-touching operations the in-process clean performs, each behind a
// field so the wiring between them — the shared capture buffer, the
// Discover-then-refuse order, the error join — is testable without a real git
// repo, worktree, or host process.
//
// defaultCleanRunner is the only place the real implementations are named, which
// is what keeps session.CleanSession reachable from exactly one construction
// site and, at runtime, only from cleanSessionCmd's tea.Cmd closure.
type cleanRunner struct {
	// discover enumerates every entry session.Discover reports, each carrying
	// the Kind that decides its teardown.
	discover func(repo string, d session.CleanDeps) ([]session.Session, error)
	// freshPhase re-reads one discovered session's phase THROUGH THE
	// DASHBOARD'S OWN PARSER (see dashboardPhase — the parser identity is the
	// whole point).
	freshPhase func(sess session.Session) string
	// clean applies the lifecycle cleanup to a resolved session. It is
	// session.CleanSessionWithOptions rather than session.CleanSession
	// because the dashboard now has an answer to carry: the plain wrapper
	// would silently drop the branch-deletion the user confirmed.
	clean func(repo string, sess session.Session, d session.CleanDeps, opts session.CleanOptions) (session.CleanResult, error)
}

func defaultCleanRunner() cleanRunner {
	return cleanRunner{
		discover:   session.Discover,
		freshPhase: dashboardPhase,
		clean:      session.CleanSessionWithOptions,
	}
}

// defaultCleanSession is the real implementation behind Deps.CleanSession: the
// same three steps `afk clean <name>` performs, in the same order, run
// in-process instead of by suspending the TUI for a child.
func defaultCleanSession(repo, name, confirmedPhase string, scratch bool, kind session.Kind, opts session.CleanOptions) (session.CleanResult, error) {
	return defaultCleanRunner().run(repo, name, confirmedPhase, scratch, kind, opts)
}

// defaultBranchMerged is the real implementation behind Deps.BranchMerged: the
// same ancestry check `afk clean --delete-branch` consults, plus the base
// resolution the modal needs a NAME from.
//
// internal/session.Merged already falls back to the repo's trunk when base is
// empty, but it reports only a bool — and the modal's first line has to name
// the base it was checked against, so resolving it here and passing it in
// explicitly is what keeps the label and the check the same base. Doing it the
// other way (letting Merged fall back internally and printing a trunk resolved
// separately) would let the two drift apart in exactly the case that matters,
// a repo whose origin/HEAD is not what the label assumed.
func defaultBranchMerged(repo, branch, base string) (merged bool, resolvedBase string) {
	resolvedBase = base
	if resolvedBase == "" {
		trunk, err := session.TrunkPlain(session.Deps{}, repo)
		if err != nil {
			return false, ""
		}
		resolvedBase = trunk
	}
	return session.Merged(session.DefaultCleanDeps().Run, repo, branch, resolvedBase), resolvedBase
}

// run is defaultCleanSession's body.
//
// The order is the safety property, not a style choice. Read top to bottom:
// buffers wired, name resolved through session.Discover or refused, fresh phase
// re-checked or refused, and only then the clean itself.
func (r cleanRunner) run(repo, name, confirmedPhase string, scratch bool, kind session.Kind, opts session.CleanOptions) (session.CleanResult, error) {
	// Defensive, and deliberately not for the reason it looks like:
	// session.CleanSession never writes to Stdout, and its Stderr writes are
	// bounded and known: a non-ENOENT unlink failure deep inside host teardown,
	// and CleanResult.note's operator-facing lines (archive skipped, a remove
	// that fell back to prune, a git-spice upstack retarget). The
	// outcome summary that must never paint over the alt-screen belongs to the
	// root clean.go, which this path never invokes. The buffer is here so that no
	// FUTURE write inside CleanSession can reach the terminal either. It is
	// therefore usually empty, and joins the error only when it is not — the
	// actionable detail lives in the returned error, which defaultRun already
	// wraps with the failing git command's stderr.
	var out bytes.Buffer
	deps := session.DefaultCleanDeps()
	deps.Stdout = &out
	deps.Stderr = &out

	// Resolve the name through session.Discover rather than trusting the
	// dashboard's row. This is load-bearing and MUST NOT be "simplified" away,
	// and the reason is now the KIND rather than a refusal. The dashboard's
	// list comes from internal/discover, which globs .afk/*/status.json in
	// EVERY worktree including the primary checkout; a standalone /afk:go
	// session's status file living in the main checkout is a supported state
	// and an ordinary dashboard row. session.Discover is what labels that
	// shape KindStatusWithoutWorktree, which is what routes it to the zero-git
	// teardown instead of `git add -A && git commit` in the user's own
	// checkout followed by a `git worktree remove` of the primary worktree.
	// The row cannot supply that label safely on its own: only Discover
	// resolves the WorktreePath and Branch the clean acts on.
	sessions, err := r.discover(repo, deps)
	if err != nil {
		return session.CleanResult{}, cleanErr(err, &out)
	}
	target, err := resolveCleanTarget(sessions, name, confirmedPhase, scratch, kind, r.freshPhase)
	if err != nil {
		return session.CleanResult{}, err
	}

	result, err := r.clean(repo, *target, deps, opts)
	if err != nil {
		return result, cleanErr(err, &out)
	}
	return result, nil
}

// dashboardPhase re-reads a discovered session's phase through internal/state's
// ReadStatus — the SAME parser internal/discover uses to build the dashboard row
// the user confirmed against — and reports a stale read as the empty phase,
// exactly as discover.SessionState.Phase does.
//
// Using this instead of the already-parsed sess.Status.Phase is not redundant, it
// is the fix for a real over-refusal. The two packages parse status.json to
// DIFFERENT strictness. internal/state's ReadStatus rejects unknown keys,
// trailing data, a phase outside the six-value enum, a plan_name that disagrees
// with the containing directory, and an empty session_id outside `planning`, and
// reports every one of those as stale with a zero-value Status — so the dashboard
// shows Phase "". internal/session's own discovery only json-decodes the file, so
// the very same file yields a POPULATED Status.Phase there. Comparing the
// dashboard's "" against session's "executing" refused every stale row
// unconditionally, with a nonsense "changed phase from "" to "executing""
// message, making a stale row impossible to clean from the dashboard at all.
//
// Reading the fresh phase with the dashboard's parser puts both sides of the
// comparison in one vocabulary, so the check answers the question it is actually
// there to answer: did this session change, as the user sees sessions, between
// the confirmation and the clean.
func dashboardPhase(sess session.Session) string {
	res := state.ReadStatus(sess.StatusPath)
	if res.Stale {
		return ""
	}
	return string(res.Status.Phase)
}

// resolveCleanTarget picks name out of what session.Discover reported and
// refuses on either of the two conditions that make a clean unsafe: a name
// Discover does not report at all, and a fresh phase that no longer matches
// the one the user was shown.
//
// It is also where the ROUTING happens, which is the half that is easy to miss:
// the entry it returns carries the Kind CleanSessionWithOptions switches on, so
// resolving a stranded `.afk/{name}/` here is what sends it to the zero-git
// teardown rather than to the snapshot-and-remove one (see cleanRunner.run for
// the primary-worktree hazard that makes the distinction mandatory).
//
// The dashboard's cached phase can be a full refresh interval old, so it cannot
// stand in for the CLI's fresh phase gate. Comparing what the user actually
// confirmed against what is on disk right now restores that safety property: a
// session that moved on while the confirmation modal was up is not the session
// the user agreed to clean.
//
// freshPhase MUST derive the phase the way the dashboard derived confirmedPhase
// (dashboardPhase does; sess.Status.Phase does NOT — see dashboardPhase's own
// comment). With that held, a stale row confirms as "" and re-reads as "", so it
// is cleanable; a row that went stale, or stopped being stale, under the modal
// is a genuine change and is still refused.
//
// scratch joins the match because the name alone is ambiguous: a scratch and a
// planned session can share a name (their `.afk/` dirs live in different
// worktrees), and both have phase "" when the planned one's status is stale —
// so without the kind, the fresh-phase gate cannot tell them apart and a
// confirmed SCRATCH clean would resolve to whichever the slice orders first
// (sortSessions puts the status-backed one there) and worktree-remove work the
// user never confirmed. The confirming UI carries the kind (confirmScratch),
// so the match honors it.
//
// kind joins it for the same reason one step further, now for two shapes. A
// worktree-side fault entry is non-scratch, is named after its branch, and
// reads phase "" — so a healthy session sharing that branch name matches on
// all three of the other terms, and the two need opposite teardowns. A
// stranded `.afk/X/` (KindStatusWithoutWorktree) collides the same way and
// is worse: it is non-scratch, can carry the very same phase as the healthy X
// in its own worktree, and its WorktreePath is the user's own checkout, so
// matching it as healthy is what would run git there. Both sides are reduced
// through cleanTargetKind before they are compared, which is what keeps a
// stale-status row matching exactly as it does today: the two packages label
// different files stale, so that kind is deliberately not part of an entry's
// identity.
//
// Pure given its freshPhase argument, which is what makes both refusals testable
// without a real repo, worktree, or host.
func resolveCleanTarget(
	sessions []session.Session,
	name, confirmedPhase string,
	scratch bool,
	kind session.Kind,
	freshPhase func(sess session.Session) string,
) (*session.Session, error) {
	kind = cleanTargetKind(kind)
	var target *session.Session
	for i := range sessions {
		if sessions[i].Name == name && sessions[i].Scratch == scratch &&
			cleanTargetKind(sessions[i].Kind) == kind {
			target = &sessions[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no session named %q found", name)
	}
	if fresh := freshPhase(*target); fresh != confirmedPhase {
		return nil, fmt.Errorf(
			"session %q changed phase from %q to %q since you confirmed — not cleaning",
			name, confirmedPhase, fresh)
	}
	return target, nil
}

// cleanErr appends whatever the clean deps wrote to their shared buffer to err,
// and only when that buffer is non-empty — it is empty in almost every real
// failure, so it can never become the primary error text.
func cleanErr(err error, out *bytes.Buffer) error {
	if captured := strings.TrimSpace(out.String()); captured != "" {
		return fmt.Errorf("%w: %s", err, captured)
	}
	return err
}

// planApprovedMsg carries one Deps.ApprovePlan call's result back to Update
// (planapprove.go). root and gen are the project folder and generation the
// owner said y under, so a result that crosses a project switch is dropped
// rather than told under the other project's name.
type planApprovedMsg struct {
	root   string
	gen    int
	result PlanApproveResult
}

// projectAddedMsg carries one Deps.AddProject call's result back to Update
// (projectedit.go). gen and openGen are the project generation and the
// all-projects screen's opening the owner submitted under: a result that
// lands after a project switch, or after the screen closed or was opened
// again, is still told (config.toml changed either way, and nothing else
// will say so) but moves no cursor.
type projectAddedMsg struct {
	dir     string
	gen     int
	openGen int
	result  ProjectEditResult
}

// projectRemovedMsg carries one Deps.RemoveProject call's result. It needs
// no generation: a remove moves nothing but the row it took away, which the
// re-read after it takes away whatever the screen shows by then.
type projectRemovedMsg struct {
	root   string
	result ProjectEditResult
}

// addProjectCmd and removeProjectCmd run one add or remove. The seam is
// called here, on the command goroutine, and nowhere else: it reads and
// writes config.toml and runs git, none of which may stall the screen.
func addProjectCmd(add func(dir, name string) ProjectEditResult, dir, name string, gen, openGen int) tea.Cmd {
	return func() tea.Msg {
		return projectAddedMsg{dir: dir, gen: gen, openGen: openGen, result: add(dir, name)}
	}
}

func removeProjectCmd(remove func(root string) ProjectEditResult, root string) tea.Cmd {
	return func() tea.Msg {
		return projectRemovedMsg{root: root, result: remove(root)}
	}
}

// approvePlanCmd runs one approve. approve is called here, on the command
// goroutine, and nowhere else: it writes files, commits and pushes, and a
// slow push must not freeze the screen.
func approvePlanCmd(approve func(root string) PlanApproveResult, root string, gen int) tea.Cmd {
	return func() tea.Msg {
		return planApprovedMsg{root: root, gen: gen, result: approve(root)}
	}
}

// taskStatusSetMsg carries one Deps.SetTaskStatus call's result back to
// Update (taskstatus.go). gen is the project generation the owner said y
// under, so a result that lands after a project switch is dropped rather than
// told under the other project's Build tab.
type taskStatusSetMsg struct {
	gen    int
	result TaskStatusResult
}

// setTaskStatusCmd runs one status change. The seam is called here, on the
// command goroutine, and nowhere else: it takes the ledger's lock and writes
// the file, and a lock held by the build loop must not stall the screen.
func setTaskStatusCmd(set func(root, id, status string, replacedBy []string) TaskStatusResult,
	root, id, status string, replacedBy []string, gen int) tea.Cmd {
	return func() tea.Msg {
		return taskStatusSetMsg{gen: gen, result: set(root, id, status, replacedBy)}
	}
}

// usageRefreshedMsg carries one Deps.RefreshUsage call's result back to
// Update (usagerefresh.go). It carries no project generation, unlike
// taskStatusSetMsg: the reading is the machine's, not a project's, so a
// result that lands after a project switch is still true and is still told.
// The header reload it triggers goes through buildCmd, which is tied to the
// project the dashboard is on when the result lands.
type usageRefreshedMsg struct {
	result UsageRefreshResult
}

// refreshUsageCmd runs one usage reading. The seam is called here, on the
// command goroutine, and nowhere else: it runs the agent's command-line
// tool, which can take up to a minute, and the screen must not wait for it.
func refreshUsageCmd(refresh func() UsageRefreshResult) tea.Cmd {
	return func() tea.Msg {
		return usageRefreshedMsg{result: refresh()}
	}
}
