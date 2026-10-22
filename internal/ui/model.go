// Package ui implements the `afk dash` Bubble Tea dashboard: a session
// list with a progress bar, phase badge, and awaiting-input indicator, plus
// a detail pane for the selected session (waves/tasks with states, the last
// lines of learnings.md / issues.md, and the review verdict when present).
//
// Design contract (from the task brief):
//   - The Model is pure: Update mutates only in-memory state and returns
//     tea.Cmds. Every side effect — discovery, the refresh ticker, the
//     restart-flow host spawn, the background create, opening a pane over a
//     session's host — lives in a tea.Cmd function in messages.go. This keeps
//     Update fully testable without a TTY (see ui_test.go), and guarantees the
//     UI never blocks on git.
//   - Refresh is re-running discovery on a ticker (--interval, default 2s),
//     file reads only. No capture-pane, no pane polling. No writes to any
//     .afk file.
//   - Session identity, status.json semantics, and the plan/verdict/notepad
//     file locations all come from
//     plugins/afk/skills/conductor/status-schema.md (the binding contract)
//     via internal/discover and internal/state — this package invents none of
//     them.
//   - A session is a DETACHED host process (internal/host) that outlives the
//     dashboard. The dashboard connects to one and renders it in an embedded
//     pane; it never takes over the real terminal, and closing a pane detaches
//     rather than ending the session.
package ui

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/dashmeta"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/spice"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/termpane"
	"github.com/dphaener/afk/internal/usagestate"
)

// paneHandle is the subset of *termpane.Pane the dashboard drives. Referring
// to it through an interface (rather than the concrete type) is what lets the
// Deps.OpenHostPane seam return a fake in ui_test.go, so pane state
// transitions, rendering, and teardown are all exercised without a real socket
// or host process. It mirrors termpane.Pane's exported surface in full, so
// growing pane behavior later never has to reshape this interface or the Deps
// signature.
type paneHandle interface {
	// Render returns the emulator's current screen (ANSI-styled) for the UI to
	// place inside the pane box.
	Render() string
	// SendKey/SendText/Paste route input to the session.
	SendKey(k termpane.KeyEvent)
	SendText(s string)
	Paste(s string)
	// SendMouse routes a mouse event to the session, in the emulator's own
	// coordinate space (see mouse.go's paneMouseEvent for the translation). The
	// emulator no-ops it unless the child asked for mouse reports, so forwarding
	// one to a child that did not is harmless.
	SendMouse(m termpane.MouseEvent)
	// Resize propagates a new pane geometry to both the emulator and the
	// session's PTY (which is what makes the child repaint).
	Resize(cols, rows int) error
	// Size reports the current pane geometry in cells.
	Size() (cols, rows int)
	// Changed is the coalescing redraw-notification channel; Done closes when
	// the connection ends and Err then reports the outcome.
	Changed() <-chan struct{}
	Done() <-chan struct{}
	Err() error
	// Close DETACHES from the session and reclaims the pane's goroutines. The
	// session keeps running — see hostpane.go's hostChild.Close.
	Close() error
	// Cursor reports the child's cursor in the emulator's own coordinate
	// space — the same untranslated frame Render() draws in. View() is what
	// translates it onto the screen (origin + cell; see cursor.go's
	// paneCursor), so this method must not apply any offset itself.
	Cursor() termpane.CursorState
}

// inFlightOpen is the pane-open whose dial has not reported back yet (see
// Model.attachInFlight). Its zero value means "nothing outstanding", which is why
// name is what emptiness is read off.
//
// It is a record rather than a bare name because three questions have to be
// answered separately, and a name alone conflates them:
//
//   - "is a dial for this session outstanding" — what stops a second dial to one
//     host, in either direction (an explicit connect during a preview's dial,
//     or a preview during an explicit open).
//   - "was that dial an explicit open" — what stops a PREVIEW's arrival from
//     releasing a claim an explicit open is holding.
//   - "did a connect claim this exact preview" — what lets the preview's
//     already-dispatched result become the explicit surface without opening a
//     second client, while every other stale preview remains rejectable.
//
// claimedPreview stays keyed to this one dial: connect marks it when enter
// (or ctrl+t into the agent view) targets the same session, and
// applyPaneAttached consumes it only when that preview result arrives. It is
// deliberately not a free-standing Model flag, which could leak onto a later
// unrelated arrival.
type inFlightOpen struct {
	name           string
	preview        bool
	claimedPreview bool
	focusOnArrival bool
}

// inFlightRestart is an outstanding restartHostCmd. gen is the project
// generation it was dispatched under, so a pre-switch report cannot clear a
// same-named restart the incoming project owns.
type inFlightRestart struct {
	name           string
	gen            int
	focusOnArrival bool
}

// lostPane names a session whose pane connection ended without the session
// ending; message is the placeholder's first line.
type lostPane struct {
	name    string
	message string
}

// Session is the dashboard's per-session view model: the discovered
// SessionState (status + plan + verdict, joined by internal/discover) plus
// the notepad tails and diff stats the detail pane renders. Tails and diff
// stats are computed fresh each discovery pass (file reads / async git
// shell-outs only) alongside the rest of the session state — see loadCmd in
// messages.go and diff.go's dual-cost computeDiffStats.
type Session struct {
	discover.SessionState
	LearningsTail []string
	IssuesTail    []string
	DiffStats     DiffStats

	// DiffText is the capped full-diff text the Diff tab renders, one string
	// per line. It lives beside DiffStats, not inside it, because DiffStats
	// must stay comparable with == (go vet rejects a []string field there,
	// and nine `==`/`!=` sites in diff_test.go depend on it). It is usually
	// empty: loadCmd only retains it for the focused session, and only while
	// the Diff tab itself is on screen (computeDiffStats's retainText) — an
	// unbounded diff has no reason to sit resident on the Model while some
	// other tab is showing.
	DiffText []string

	// Pending marks a placeholder row for a session whose background create has
	// not finished yet — discovery cannot see it, because its worktree does not
	// exist yet. Only Name is populated on such a row, and it is INERT: every
	// per-session key (enter, c) ignores it. Placeholders are merged into
	// Model.sessions itself rather than overlaid at render time, because the
	// list's items, selected() and every keybinding index that one slice.
	Pending bool

	// UsagePaused is true when the build's ledger records that the daemon
	// paused this session for usage, and UsageWake is when it means to start
	// it again, as text ("" when no time is known). Discovery does not set
	// them: they are stamped from the build snapshot (stampUsagePauses,
	// buildtab.go) so a row can say "paused for usage" where it would
	// otherwise read as stopped.
	UsagePaused bool
	UsageWake   string
}

// cleanTargetKind reduces a row's Kind to the part that identifies a clean
// target, which is deliberately not all of it.
//
// It asks session.Kind.IdentityRelevant rather than naming the kinds, so this
// package and the CLI's findCleanTarget can never disagree about which ones
// are part of a name's identity. Three qualify today. The two WORKTREE-SIDE
// kinds: a missing directory and a live foreign worktree need opposite
// teardowns, and either can share a name with a healthy session, so the name
// alone resolves to whichever entry sortSessions happened to order first. And
// KindStatusWithoutWorktree, which is NOT a worktree fault — its row keeps a
// pane, a preview and an attach — but whose WorktreePath is the user's own
// checkout, so a stranded `.afk/X/` resolved as the healthy X would run
// `git add -A && git commit` there.
//
// KindStaleStatus is NOT, and collapsing it to KindHealthy here is the whole
// reason this is a function rather than an equality check at the call sites.
// internal/state calls four status.json shapes stale that internal/session
// decodes cleanly (trailing data, a plan_name mismatch, an unknown phase, an
// empty session_id outside `planning`), so the dashboard's row and the Session
// it must resolve to legitimately disagree about that kind for the same file.
// Matching on it would fail every malformed-status row with `no session named
// %q found` — the same over-refusal dashboardPhase exists to have fixed once
// already, in the same seam.
func cleanTargetKind(k session.Kind) session.Kind {
	if k.IdentityRelevant() {
		return k
	}
	return session.KindHealthy
}

// cleanID is the identity of an in-flight clean: the same (name, reduced kind)
// pair the confirm modal captures and resolveCleanTarget matches on. It is a
// comparable struct so it can key cleaningSessions directly.
type cleanID struct {
	name string
	kind session.Kind
}

// cleanIDFor builds the key for a row.
func cleanIDFor(s Session) cleanID {
	return cleanID{name: s.Name, kind: cleanTargetKind(s.Kind)}
}

// cleaningByName reports whether ANY in-flight clean carries this name,
// whatever its kind. It is what the name-only paths must ask — the pane open,
// the pane ARRIVAL, the focus request, and the restart chain, none of which has
// a row to reduce.
//
// Those paths used to build a (name, KindHealthy) key instead, on the argument
// that a pane can only exist on a row that is not a worktree-side fault. That
// argument does not survive KindStatusWithoutWorktree: a stranded `.afk/X/`
// renders as an ordinary row with a live host, so its clean is registered under
// the stranded kind and the healthy lookup missed it — landing a pane on a host
// the clean was about to SIGKILL.
//
// The looser question over-refuses on purpose. A healthy X and a stranded X
// block each other on these four paths for the clean's duration (~11s worst
// case), because a bare name cannot say which one was meant and the wrong
// answer opens a pane onto a session being torn down. `c` itself still matches
// the exact pair, so the user can always clean the other one.
func (m Model) cleaningByName(name string) bool {
	for id := range m.cleaningSessions {
		if id.name == name {
			return true
		}
	}
	return false
}

// cleanMergeState is what the clean confirmation modal knows about its
// target branch's merge state. The three fields travel together because two
// of them are meaningless alone: merged=false reads identically whether the
// ancestry check said "no" or has not answered yet, and base names the branch
// that check was actually run against — interpolated into the modal's first
// line so it can never claim a merge into a trunk nobody consulted.
//
// The zero value is the honest starting point: nothing known. base=="" means
// the base could not even be named (an unresolvable trunk), which is
// deliberately indistinguishable from "not answered yet" everywhere it is
// read — both route `d` to the second confirmation, the safe direction.
type cleanMergeState struct {
	known  bool
	merged bool
	base   string
}

// isWorktreeFault reports whether k is one of the two faults the dashboard
// renders as a BROKEN or ORPHAN row: an entry with no session behind it, no
// host to dial, and nothing to preview. It delegates to the shared
// discriminator rather than naming the constants itself so this package can
// never disagree with the CLI's findCleanTarget about which kinds are
// identity-relevant — that drift is exactly how the CLI once shipped without
// the collision fix this seam already had.
func isWorktreeFault(k session.Kind) bool {
	return k.WorktreeFault()
}

// SearchSettings is the dashboard's folder-search settings (the `[dash]` table
// of config.toml) as the dashboard needs them. Like ListedProject it is this
// package's own type rather than the settings reader's (internal/daemonconfig),
// because the dashboard must not read that file itself: the read is the
// injected Deps.LoadConfig seam, and `afk dash` converts the reader's result
// into this. The reader has already applied the defaults and limits, so a
// value that arrives here is one the search can use as it stands.
type SearchSettings struct {
	// Roots are the folders to search, already absolute.
	Roots []string
	// Interval is how often the search runs again.
	Interval time.Duration
	// MaxDepth is how many folder levels below each root the search goes, at
	// least 1 (the reader refuses anything lower; see Deps.Scan and
	// internal/scan on why a 0 must never reach the walk).
	MaxDepth int
}

// Deps is the injectable seam for every environment-touching operation the
// dashboard performs. DefaultDeps (via withDefaults) wires the real
// implementations; tests substitute fakes so Update can be exercised with
// synthetic messages and no real git/filesystem/subprocesses.
type Deps struct {
	// Discover loads all sessions for repo (git worktree enumeration + file
	// reads). Defaults to internal/discover.Discover with the real git.
	Discover func(repo string) ([]discover.SessionState, error)
	// ReadTail returns the last n lines of the file at path, or nil when the
	// file is absent. Defaults to readTail.
	ReadTail func(path string, n int) []string
	// SpawnHost re-launches the detached host for a stopped session in its
	// worktree, returning the started process. Defaults to defaultSpawnHost,
	// a thin wrapper over internal/session.SpawnHost — the dashboard consumes
	// that helper and never reimplements the launch.
	SpawnHost func(name, worktree string) (session.HostProcess, error)
	// SpawnScratchHost is SpawnHost's one-off sibling: same launch, but the
	// child argv is a bare `claude` (session.HostSpawn.Scratch). A separate
	// field rather than a flag on SpawnHost because that signature is pinned
	// by its injectors; restartHostCmd picks the seam off the row's own
	// Scratch flag (paneOpenRequest.scratch), so a stopped one-off session is
	// never relaunched as a conductor. Defaults to defaultSpawnScratchHost.
	SpawnScratchHost func(name, worktree string) (session.HostProcess, error)
	// WaitHostReady bridges the narrow interval after a successful host spawn
	// but before that host has published a readable host.json. restartHostCmd
	// calls it before reporting success, so only restart-driven opens wait;
	// ordinary opens and previews remain immediate. Defaults to
	// defaultWaitHostReady.
	WaitHostReady func(worktree, name string) error
	// Now returns the current time (for the "last refreshed" age and
	// deterministic tests). Defaults to time.Now.
	Now func() time.Time
	// LoadBuild reads what the dashboard shows about the project's build: the
	// master plan's tasks with their ledger status, the inbox and the last
	// usage reading. repo is the active project's folder, which may be a
	// linked worktree; the loader finds the main checkout itself, because that
	// is where the ledger and the inbox live. sessions are the rows the model
	// already holds, so the loader does not run discovery again. It is called
	// only from inside a tea.Cmd (builddata.go), never from Update or View.
	//
	// It has NO default, like Scan and LoadConfig: it reads the real
	// filesystem and the machine's usage file, so only `afk dash` wires it. A
	// nil LoadBuild means no build data, ever, and no command is issued.
	LoadBuild func(repo string, sessions []BuildSession) buildview.Snapshot
	// LoadPlanStatus reads the planning session's state and the plan checker's
	// result for the project whose folder is repo (a linked worktree is
	// resolved to its main checkout by the loader, because .afk/plan and the
	// master plan live there). It exists so the Build section and the master-plan
	// overview can say what `afk plan status` and `afk plan lint` would, and so
	// the approve action can tell a new plan at review from anything else. It
	// is called only from inside a tea.Cmd, never from Update or View.
	//
	// It has NO default, like LoadBuild: it reads the real filesystem. A nil
	// LoadPlanStatus means no plan status, ever, and no command is issued.
	LoadPlanStatus func(repo string) PlanStatus
	// LoadAllProjects reads one row for every project the daemon builds (the
	// `[[project]]` list in config.toml, plus the dashboard's active project
	// when it is not listed) for the all-projects screen (allprojects.go): its
	// current task, the state of its build, its inbox counts and whether its
	// plan waits for approval. A project that cannot be read is a row saying
	// so, never an error; Err is config.toml itself failing. It is read off the
	// draw path, inside a tea.Cmd, and only while the screen is open.
	//
	// It has NO default, like LoadBuild: it reads the real filesystem, and
	// only the root package (`afk dash`) wires it. A nil LoadAllProjects means
	// the screen cannot open, and esc at Project says so.
	LoadAllProjects func() AllProjects
	// LoadActivity reads the home screen's LAST 24H cells and THIS WEEK sums
	// (T16.8) from the decision log and the token ledger of each project at
	// roots, counted against now. It runs on the project scan (once a
	// minute, and when the set of home rows changes), never on the 2s
	// refresh: the cells are two hours wide, so a minute's lag cannot be
	// seen, and the logs grow without bound. It is called only inside a
	// tea.Cmd (activityReadCmd), never from Update or View.
	//
	// It has NO default, like LoadAllProjects: it reads the real files, and
	// only the root package wires it. A nil LoadActivity means the column and
	// the chart are not drawn at all.
	LoadActivity func(roots []string, now time.Time) HomeActivity
	// ApprovePlan turns the new plan at review in the project at root into a
	// build, the way `afk plan approve --project root` does (T7.8): it writes
	// afk.toml, commits and pushes the plan, starts the ledger, lists the
	// project and records the phase approved, stopping at the first step that
	// fails. It returns what happened as one outcome and one plain line, and
	// prints nothing. It commits and pushes, so it is called only after the
	// owner's y in inputConfirmPlanApprove, and only inside a tea.Cmd
	// (planapprove.go), never from Update or View.
	//
	// It has NO default, for AnswerInbox's reason: it writes into a real
	// project and pushes to its remote, so a zero Deps must not be able to.
	// A nil ApprovePlan dims the "approve the plan" row with "approving is
	// not available here", and nothing is called.
	ApprovePlan func(root string) PlanApproveResult
	// AddProject puts the folder dir on the machine's project list, under
	// name or, when name is "", the folder's name, the way `afk project add`
	// does (T7.10). dir is already absolute: the add form expands `~` and
	// refuses any other relative path before calling it. RemoveProject takes
	// the project whose main checkout is root off that list, the way
	// `afk project remove` does; its folder, plan and sessions are not
	// touched. Each returns what happened as one outcome and the command's
	// own words, and prints nothing.
	//
	// They are the one exception to "the dashboard never writes config.toml"
	// (master plan M7, "What the dashboard writes"): the root package wires
	// them to the code behind the two commands, which edits the file as text
	// so its comments and the [dash] table survive, and this package never
	// touches the file itself. They are called only inside a tea.Cmd
	// (projectedit.go), remove only after the owner's y.
	//
	// They have NO default, for ApprovePlan's reason: they write the real
	// config.toml, so a zero Deps must not be able to. A nil one dims its row
	// in the `:` search, its key says why, and nothing is called.
	AddProject    func(dir, name string) ProjectEditResult
	RemoveProject func(root string) ProjectEditResult
	// SetTaskStatus sets the status of the task id in the build ledger of
	// the project whose main checkout is root (the Root of the snapshot
	// LoadBuild returned), the way `afk ledger set id status` does (T7.11):
	// the id and any replacing ids are checked against the plan, and the
	// ledger is written under its lock. replacedBy is the ids that replace
	// the task, given only with status superseded. It returns what happened
	// as one outcome and the command's own words, and prints nothing.
	//
	// It is the dashboard's one write to the ledger (master plan M7, "What
	// the dashboard writes"): the root package wires it to the code behind
	// the command, and this package never touches the file itself. It is
	// called only after the owner's y in inputConfirmTaskStatus, and only
	// inside a tea.Cmd (taskstatus.go), never from Update or View.
	//
	// It has NO default, for ApprovePlan's reason: it writes a real project's
	// ledger, so a zero Deps must not be able to. A nil SetTaskStatus dims
	// the "set this task's status" row, and nothing is called.
	SetTaskStatus func(root, id, status string, replacedBy []string) TaskStatusResult
	// LoadTaskTokens reads the token record of the task id in the project
	// whose main checkout is root (the Root of the snapshot LoadBuild
	// returned), the way `afk tokens id` reads it (T7.12): the command's own
	// lines, a compact form of its table and a short total, or why there is
	// nothing to show. It prints nothing.
	//
	// It reads only the token ledger. It never reads a transcript and never
	// records tokens, so the dashboard cannot add to the ledger it shows;
	// recording stays `afk tokens --record`. It is called on the refresh tick
	// and when the Build section's cursor lands on another task, and only inside
	// a tea.Cmd (tasktokens.go), never from Update or View.
	//
	// It has NO default, for LoadBuild's reason: it reads the real
	// filesystem, so a zero Deps must not. A nil LoadTaskTokens dims the
	// "show this task's tokens" row, gives no row a total, and reads nothing.
	LoadTaskTokens func(root, task string) TaskTokens
	// RefreshUsage takes a fresh usage reading of the machine's agent and
	// saves it, with the governor's decision for it, to usage.json, the way
	// `afk usage --refresh` does (T7.13). It returns what happened as one
	// outcome and a plain sentence, and prints nothing. The header then
	// shows the new reading through the next LoadBuild, which reads
	// usage.json; on a failure the file is left as it was, so the header
	// keeps the old reading.
	//
	// It runs the agent's command-line tool, which takes a second or two
	// and can take up to a minute, so it is called only inside a tea.Cmd
	// (usagerefresh.go), never from Update or View, and only on the owner's
	// action: never on the refresh tick, at start-up or on a timer.
	//
	// It has NO default, for LoadBuild's reason: it runs a real CLI and
	// writes the real state folder, so a zero Deps must not. A nil
	// RefreshUsage dims the "refresh usage" row, and nothing is called.
	RefreshUsage func() UsageRefreshResult
	// HomeDir is the home folder the add form expands `~` with. nil means
	// os.UserHomeDir, which reads $HOME and touches no file; it is a seam so
	// a test can pin the expansion without changing the process environment.
	HomeDir func() (string, error)
	// AnswerInbox records the owner's answer to the inbox item id in the
	// project whose main checkout is root (the Root of the snapshot LoadBuild
	// returned), and returns the item as stored. It is the dashboard's only
	// write to a project's build files. It has NO default, for the same
	// reason LoadBuild has none: a zero Deps must not be able to write into a
	// real project's inbox.
	AnswerInbox func(root, id, text string) (inbox.Item, error)
	// PauseBuild pauses the build of the project whose main checkout is root
	// (the Root of the snapshot LoadBuild returned): a task that is running
	// finishes and nothing new starts. already reports that the build was
	// paused before the call. It is the dashboard's `P` key (buildpause.go),
	// and `afk dash` wires it to what `afk pause` runs, so the key and the
	// command write the same files and the same decision-log line.
	//
	// It has NO default, for AnswerInbox's reason: it writes into a real
	// project, so a zero Deps must not be able to. A nil PauseBuild makes the
	// key say pausing is not available, and nothing is called.
	//
	// An error does not mean nothing changed. The flag can be written and the
	// decision-log line after it refused; the error then says so, and the
	// dashboard reads the build files again either way.
	PauseBuild func(root string) (already bool, err error)
	// ResumeBuild is the `U` key's seam, on PauseBuild's terms: it clears the
	// owner's pause and a stop the build set itself, and reports which of the
	// two it found. It has NO default either, and a nil one is answered the
	// same way.
	ResumeBuild func(root string) (ResumeResult, error)
	// StartPlan is the seam behind the dashboard's five plan actions
	// (planstart.go): it starts a change session on the project at root the
	// way `afk plan <mode>` does, short of taking the terminal, and returns
	// what happened. mode is PlanModeExtend, PlanModeAmend, PlanModeReconcile,
	// PlanModeFix, PlanModeNew (root is then the folder as the owner typed it, and the
	// start behaves as bare `afk plan` there) or PlanModeResume (open a
	// planning session that is already open, never start one); text is
	// amend's sentence, reconcile's path or fix's description of what is
	// broken, as typed.
	// Every refusal comes back as a PlanRefused result in the command's own
	// words, so it has no error return.
	//
	// It has NO default, for AnswerInbox's reason: it writes into a real
	// project and spawns a host, so a zero Deps must not be able to. A nil
	// StartPlan makes the actions say starting a plan change is not
	// available, and nothing is called.
	StartPlan func(root, mode, text string) PlanStartResult
	// PlanFolder decides what the new-plan form's text means (T7.5): a full
	// or ~/ path to use as typed, a bare project name to create under the
	// owner profile's project location, or a refusal with its reason. It
	// reads the profile and the disk, so it is only ever called inside a
	// tea.Cmd, never from Update or View. The decision itself lives outside
	// this package, which cannot import it; `afk dash` maps it into
	// PlanFolderDecision.
	//
	// A nil PlanFolder keeps the form as T7.4 built it: the text goes to
	// StartPlan unchanged, so a dashboard without the seam still plans in a
	// typed folder and only loses the create-by-name step.
	PlanFolder func(text string) PlanFolderDecision
	// CreatePlanFolder makes the folder a confirmed name stands for and makes
	// it a git repository. It is called only after the owner's y in
	// inputConfirmPlanFolder, and only inside a tea.Cmd: it writes to disk
	// and runs git. Its error is a plain sentence saying what was left on
	// disk, shown as it is. It has NO default, for AnswerInbox's reason: a
	// zero Deps must not be able to create folders. A nil one on y is
	// answered with a sentence and nothing is created.
	CreatePlanFolder func(path string) error
	// GitHubOffer finds out whether to ask the owner about a GitHub
	// repository for a folder CreatePlanFolder has just made (T7.6): gh
	// installed, logged in, and the account to name. It runs gh, so it is
	// only ever called inside a tea.Cmd, and only for a folder made by name,
	// never for a typed path or a folder that already existed.
	//
	// It has NO default: a zero Deps must not run gh. A nil GitHubOffer
	// keeps T7.5 exactly as it was: planning starts straight after the
	// create and no question is asked.
	GitHubOffer func(path string) GitHubOffer
	// CreateGitHubRepo makes the private repository repo on GitHub and adds
	// it as path's origin, pushing nothing, and returns the outcome as one
	// plain line (made, name taken, failed, or made without origin). It is
	// called only after the owner's y in inputConfirmGitHubRepo, inside a
	// tea.Cmd. It has NO default, for AnswerInbox's reason: a zero Deps must
	// not be able to make a repository. A nil one on y makes nothing, says
	// so, and planning starts all the same.
	CreateGitHubRepo func(path, repo string) string
	// SpiceDetect reports whether the repo at dir is operating in git-spice
	// mode. Defaults to internal/spice.Detect. The stack panel is hidden
	// entirely when this returns false — never a placeholder.
	SpiceDetect func(dir string) bool
	// SpiceLogShort runs `gs log short --json` in dir and returns the
	// parsed, sorted stack. Defaults to internal/spice.LogShort. Only
	// called when SpiceDetect(dir) is true.
	SpiceLogShort func(dir string) ([]spice.Branch, error)
	// PlainBranches lists plain-git branches for repo. Defaults to a thin
	// wrapper over session.BranchesPlain. Fetched asynchronously alongside
	// stackCmd so newNewForm stays pure.
	PlainBranches func(repo string) ([]string, error)
	// PlainTrunk resolves repo's trunk the way `afk new` does. Defaults to a
	// thin wrapper over session.TrunkPlain. This package must never guess a
	// trunk: "main" is wrong in a master-trunk repo, and a guessed default is
	// how the form's base and the CLI's base drifted apart to begin with.
	PlainTrunk func(repo string) (string, error)
	// DiffFull returns the full `git diff {ref}` text for the worktree at
	// dir, ref being exactly what ResolveBaseRef returned. Used ONLY for the
	// currently-focused session — see diff.go's dual-cost pattern. Its text
	// is retained on Session.DiffText (not just counted) when the Diff tab
	// is on screen — see computeDiffStats's retainText parameter. Defaults
	// to defaultDiffFull.
	DiffFull func(dir, ref string) (string, error)
	// DiffNumstat returns `git diff --numstat {ref}` output for the worktree
	// at dir, ref being exactly what ResolveBaseRef returned. Used for every
	// session except the focused one. Defaults to defaultDiffNumstat.
	DiffNumstat func(dir, ref string) (string, error)
	// ResolveBaseRef turns the session's recorded base branch into the ref
	// expression to diff against in the worktree at dir — refs/heads/{base}
	// when that resolves, the bare name when only it does (a remote-tracking
	// base or a SHA) — and reports false when no form resolves. It gates both
	// diff calls above AND supplies their ref argument: base_branch is fixed
	// at session creation and never rewritten, so a deleted base otherwise
	// reaches `git diff` and exits 128, and diffing against anything other
	// than the string this verified would let the check and the diff resolve
	// different objects. It returns a bool, not an error, because "the
	// branch is gone" is an answer this dashboard expects, not a fault.
	// Defaults to defaultResolveBaseRef.
	ResolveBaseRef func(dir, base string) (ref string, ok bool)
	// OpenHostPane connects to a session's already-running detached host and
	// returns a pane rendering it. Defaults to openHostPane (hostpane.go: a
	// real socket adapted to termpane.Child); tests substitute a fake so pane
	// open/redraw/exit flows run without a real host or PTY.
	//
	// This is the ONLY way the dashboard shows a session, which is what keeps
	// "the session renders inside the dashboard" true by construction: there is
	// no seam here that could hand over the real terminal.
	OpenHostPane func(cfg hostPaneConfig) (paneHandle, error)
	// CleanSession resolves name to a real session and applies lifecycle
	// cleanup: stop its host, snapshot-commit dirty tracked changes,
	// archive `.afk/{name}/`, remove the worktree, prune. The branch is kept
	// unless opts says otherwise. confirmedPhase is the phase the user was
	// shown when they confirmed; the resolution step re-reads status.json and
	// refuses if it has changed underneath them. scratch and kind are the
	// identity they confirmed: the name alone distinguishes neither a one-off
	// from a planned session sharing it nor a healthy session from a
	// worktree-side fault entry on the same branch (see resolveCleanTarget).
	// opts carries the branch-deletion answer the modal collected: the zero
	// value is byte-for-byte the behavior this seam had before `d` existed,
	// and the branch is kept. Defaults to defaultCleanSession.
	CleanSession func(repo, name, confirmedPhase string, scratch bool, kind session.Kind, opts session.CleanOptions) (session.CleanResult, error)
	// BranchMerged reports whether branch is already merged into base, and
	// names the base it actually consulted — base is status.json's
	// base_branch, which is empty for a stale row and for both worktree-side
	// fault kinds, so the answer has to say which trunk it fell back to or the
	// modal cannot honestly interpolate one. A false merged with an empty base
	// means the check could not run at all.
	//
	// It shells out to git, so it is only ever called from inside a tea.Cmd
	// (cleanMergeStateCmd). Defaults to defaultBranchMerged.
	BranchMerged func(repo, branch, base string) (merged bool, resolvedBase string)
	// WriteLabel persists the display-label sidecar (`.afk/{name}/label`)
	// for a session in worktree. Defaults to internal/session.WriteLabel, on
	// the same delegation pattern as CleanSession and SpawnHost above: the
	// dashboard never re-implements the atomic-write discipline a sidecar
	// file needs, it only calls the package that already owns it.
	WriteLabel func(worktree, name, label string) error
	// ListSessionDirs lists `.afk/*` directory names in repo, for
	// openScratch's placeholder-name correction. A seam (defaulting to
	// internal/session.ListSessionDirs) because it is disk I/O and therefore
	// must only ever run inside a tea.Cmd, where tests need to observe and
	// substitute it — see listScratchDirsCmd.
	ListSessionDirs func(repo string) ([]string, error)
	// RunSpawn runs the background one-off create's exec.Cmd and returns its
	// combined output. A seam (defaulting to (*exec.Cmd).CombinedOutput)
	// because afkBinary() is the running executable — the test binary under
	// `go test` — so a test asserting which argv a dispatched create carries
	// must intercept the run rather than execute a child. See
	// spawnScratchCmd.
	RunSpawn func(c *exec.Cmd) ([]byte, error)
	// SendRestart requests that name's already-running host restart its
	// child in place, by sending W1.T3's Restart control frame, and then
	// READS the host's answer: restarted=true when the host confirmed the
	// respawn (an empty RestartResult frame), err non-nil when it refused or
	// failed (the frame's payload is the reason), and (false, nil) when the
	// frame was delivered but no answer came — an older host, or one that
	// went away first. Defaults to defaultSendRestart; the signal ladder and
	// respawn are the host's own job (W4.T1/restart.go). Tests substitute a
	// fake so the confirm-and-send flow runs with no real socket.
	SendRestart func(worktree, name string) (restarted bool, err error)
	// InitialSessionName, when non-empty, names a session whose pane should be
	// opened automatically once the dashboard is ready to host one: `afk
	// new`'s TTY handoff (a freshly-created session) and `afk dash --open
	// <name>` (an existing session) both set this rather than exposing a
	// separate startup path. It is a plain value, not a func seam like the rest
	// of Deps, but it lives here — not a new ui.New parameter — so the
	// package's existing three-argument New(repo, interval, deps) signature
	// never changes.
	InitialSessionName string
	// StartLevel is the level the dashboard opens on (StartLevel's doc). The
	// zero value keeps the older behaviour, so only the root package's
	// decision (dashStartLevel) changes what a Model does at start.
	StartLevel StartLevel
	// GitCommonDir is where the click-to-focus rendezvous lives: a clicked
	// notification writes afk-dash/request.json under it and the dashboard
	// watches for that file (focusrequest.go). Empty disables the whole
	// mechanism silently — a dashboard that cannot locate the directory simply
	// never sees a request, exactly as one running with no notifier installed
	// never gets a click. Like InitialSessionName this is a plain value rather
	// than a func seam, for the same reason: New's signature stays fixed.
	GitCommonDir string
	// Tmux, PaneID, ClientTTY and StartedAt are the tmux location this dashboard
	// advertises in its dashmeta entry — the four values `afk notify` needs to
	// switch a clicked notification to this pane. They are resolved by dash.go's
	// cmdDash before New is called, and like GitCommonDir they are plain values
	// rather than func seams, for the same reason: New's three-argument signature
	// stays fixed.
	//
	// The Model writes that entry (Init's writeDashEntryCmd for the launch repo,
	// applyRendezvous for every project a scan resolves) rather than the startup
	// path, because the Model is the only thing that knows the project set and
	// there is one entry per project. Whether the location is recordable at all is
	// still decided in package main — see dashLocation.recordable and
	// writeDashEntryCmd's doc comment on why the predicate is deliberately not
	// duplicated here.
	Tmux      string
	PaneID    string
	ClientTTY string
	StartedAt string
	// WriteDashEntry publishes the dashmeta entry for gitCommonDir. Defaults to
	// dashmeta.WriteEntry.
	//
	// Unlike Scan and LoadConfig below, wiring the REAL implementation as the
	// default costs this package's tests nothing: dashmeta.WriteEntry no-ops
	// unless Tmux and PaneID are both non-empty and GitCommonDir is absolute, and
	// no Model in the suite supplies all three. A zero-value Deps therefore
	// touches no disk, exactly as StartFocusWatcher does with an empty
	// GitCommonDir.
	WriteDashEntry func(gitCommonDir, tmux, paneID, clientTTY, startedAt string) error
	// RemoveDashEntry deletes this dashboard's dashmeta entry under gitCommonDir,
	// called on quit. Defaults to dashmeta.RemoveEntry, which treats an
	// already-absent file as success — and that idempotence is depended on rather
	// than incidental: runDashWith keeps its own deferred removal for the
	// abnormal-exit case, so in an ordinary quit this one runs first and the
	// other finds nothing.
	RemoveDashEntry func(gitCommonDir string) error
	// Projects is the initial project switcher list, resolved by the caller
	// (dash.go's cmdDash) before New is even called: the cwd-derived project,
	// pinned, and nothing else — W3.T1's scan ticker is what grows this into
	// the full configured-search-roots list a moment after startup. Like
	// InitialSessionName and GitCommonDir this is a plain value rather than a
	// func seam, for the same reason: New's three-argument signature never
	// changes, so a caller-computed list is the only way to hand this in.
	Projects []project.Project
	// ActiveProjectIndex is which entry in Projects starts selected, or -1
	// when Projects is empty (launched outside any repo, before the first
	// scan has had a chance to populate anything). See dash.go's
	// pickActiveProject for how the caller derives this.
	ActiveProjectIndex int
	// Scan walks the configured search roots and returns every afk project
	// under them. maxDepth is a PARAMETER rather than something the seam closes
	// over because it lives on SearchSettings, while dash.go's wrapper is built
	// before any config load has happened — a wrapper that had to supply the
	// depth itself would either hardcode it, silently ignoring the user's
	// max_depth, or read the config a second time.
	//
	// It has NO default: withDefaults leaves it nil, and nil disables the whole
	// scan pipeline — no ticker, no walk, no config read (see scanCmd). That is
	// what keeps this package's own tests, and any Model that has not opted in,
	// off the developer's real disk. The production wiring lives in dash.go.
	Scan func(roots []string, maxDepth int) []project.Project
	// LoadConfig reads the folder-search settings (the `[dash]` table of
	// config.toml) and returns them alongside the PATH of that file. The path is part of the result because the empty state
	// has to name that file and views.go performs no I/O of its own (its package
	// contract: every value it renders was placed on the Model by Update), so
	// this is the only way it can get there.
	//
	// A missing config file is not an error — the production reader
	// (internal/daemonconfig, wired by `afk dash`) answers it with defaults —
	// so a non-nil error means a file that exists and could not be read, is not
	// valid, or has a `[dash]` value the reader refuses. The path comes back on
	// an error too. Like Scan it has no default, and nil means the pipeline is
	// inert.
	LoadConfig func() (SearchSettings, string, error)
	// LoadListed reads the machine's project list (the `[[project]]` tables in
	// config.toml, what `afk project list` prints) and returns one entry per
	// listed project, the PATH of that file, and the error for a file that
	// exists but could not be read or is not valid. The path comes back on an
	// error too, so the notice can name the file to fix. A missing file is not
	// an error: it is an empty list.
	//
	// It is a separate seam from LoadConfig, with its own command and message,
	// though both now read config.toml, because the two reads fail
	// independently and arrive at different speeds: this read is one small
	// file, while a scan answers only after its whole folder walk. Listed
	// projects must not wait for that walk, and a bad `[dash]` value (which
	// fails LoadConfig but not this read) must not hide them.
	//
	// Like Scan and LoadConfig it has NO default: withDefaults leaves it nil,
	// and nil means no listed read is ever issued (listedCmd), which keeps a
	// zero-Deps Model off the developer's real settings file. This package
	// never reads that file itself; the production reader is wired by
	// `afk dash`.
	LoadListed func() (entries []ListedProject, path string, err error)
	// StartupNotice is one line the caller wants said once, as a toast, when
	// the dashboard starts. Empty means there is nothing to say.
	//
	// It is a plain string and not a reader like LoadConfig or LoadListed
	// because it is decided once per start, by the caller, before the program
	// runs: there is nothing to read again, and a function here would invite a
	// call on every tick, which is the one thing a once-per-start notice must
	// not do. This package does not know what the text is about; `afk dash`
	// writes it.
	StartupNotice string
	// Leader is the leader key as written in config.toml's [dash] table,
	// read by the caller (`afk dash`) once at start; "" means the default,
	// ctrl+]. New checks it with checkLeader and keeps what it decides, so a
	// key the dashboard cannot use falls back to ctrl+] and the header says
	// why. A plain string and not a reader, for StartupNotice's reason: the
	// leader is decided once per start (no live reload on scan ticks), and
	// this package still reads no file of its own.
	Leader string
	// Theme is the colour theme as written in config.toml's [dash] table,
	// read by the caller once at start; "" means unset (tide on a dark
	// terminal, paper on a light one). New checks it with checkTheme, so an
	// unknown name draws tide and says why in an error toast. A plain string
	// for Leader's reason: decided once per start, and this package reads no
	// file of its own.
	Theme string
	// ColorProfile is how many colours the terminal shows, detected by the
	// caller (`afk dash`, colorprofile.Detect) so this package never probes a
	// terminal. It decides only whether the ground is painted (paintsGround):
	// bubbletea already rounds every colour to the profile at its writer. The
	// zero value, colorprofile.Unknown, counts as true colour, so a test's
	// zero Deps paints exactly as a modern terminal would.
	ColorProfile colorprofile.Profile
	// LaunchMainCheckout is the main checkout of the start-up project (the
	// pinned entry in Projects). It differs from that entry's path only when
	// the dashboard was started inside a linked worktree; empty means "same as
	// the path". A plain value resolved by the caller, because finding a main
	// checkout is a git call and this package's merge does none. mergeProjects
	// uses it to see that a worktree's start-up row and a listed main checkout
	// are one project.
	LaunchMainCheckout string
	// LoadSelections returns every remembered project-to-session selection —
	// keyed by resolved project path, see rememberedSelection's doc comment —
	// for New to seed rememberedSelection from. Called exactly once, at
	// startup; nothing later in this package calls it again.
	//
	// Like Scan and LoadConfig above, it has NO default: withDefaults leaves it
	// nil, and nil leaves rememberedSelection nil too, which is exactly how an
	// un-wired Model behaves — no startup preview arm, no read of any on-disk
	// state. That is what keeps every zero-Deps Model in this package's test
	// suite off the developer's real state file.
	LoadSelections func() map[string]string
	// RememberSelection persists one (projectPath, sessionName) entry to disk.
	// Errors are the caller's to ignore, which is a durability posture and not
	// laziness: this is machine-owned convenience state, so the whole cost of a
	// lost write is that the next launch of that project opens on row 0 instead
	// of the row the user left — nothing observable as corruption, and nothing
	// worth interrupting a flush for. The production writer takes the same
	// position, a single write-then-rename with no fsync and no retry. Like
	// LoadSelections it has no default.
	RememberSelection func(projectPath, sessionName string) error
	// LoadSidebarPct returns the remembered sidebar split as an integer
	// percentage of the terminal width, for New to seed Model.sidebarPct from.
	// Called exactly once, at startup. Zero means "nothing remembered" and any
	// out-of-range value is fine: sidebarOuterWidthFor clamps it.
	//
	// Like LoadSelections it has NO default: withDefaults leaves it nil, and
	// nil leaves the split at its built-in default, which keeps every zero-Deps
	// Model in this package's tests off the developer's real state file.
	LoadSidebarPct func() int
	// RememberSidebarPct persisted the split resize mode committed. Nothing
	// calls it since W3.T1 of T16.2 removed resize mode (the Agent is drawn
	// over the Project, so nothing is split); the field stays only until the
	// start-up task takes dash.go's wiring of it out (.afk/t16-2/issues.md).
	RememberSidebarPct func(pct int) error
	// LoadProjectOpens returns how many times each project has been
	// deliberately opened — keyed by Project.Path, the same key the project
	// list and the per-project session cache use — for New to seed
	// projectOpens from. It ordered the project switcher's rows until the
	// jump search replaced it (T16.6); the count is still kept and written,
	// so the file stays right for whatever reads it next. Called exactly once, at startup;
	// nothing later in this package calls it again, so the in-memory map plus
	// this run's own increments are what every ordering decision reads.
	//
	// Like LoadSelections it has NO default, and nil leaves projectOpens
	// holding only the opens THIS run recorded — which is why an un-wired
	// Model (every zero-Deps Model in this package's tests) reads no state
	// file and still orders its rows deterministically.
	LoadProjectOpens func() map[string]int
	// RecordProjectOpen persists ONE deliberate open of projectPath, adding to
	// whatever count is already on disk rather than replacing it — so the
	// caller sends one call per open, not a total. That is what lets this
	// package hold the authoritative tally in memory for the run and still
	// survive a state file another process wrote in between.
	//
	// Errors are the caller's to ignore, on exactly RememberSelection's
	// durability posture: this is machine-owned convenience state, and the
	// whole cost of a lost write is one project ranking a place lower in the
	// switcher until the next time it is opened. Like it, no default.
	RecordProjectOpen func(projectPath string) error
	// StartFocusWatcher creates the fsnotify watcher over a git common dir's
	// afk-dash/ directory, returning nil when no watch is available (see
	// startFocusWatcher, the default). A nil return is NOT an error: the
	// discovery tick's fallback poll honors requests without it, just a tick
	// later.
	//
	// Called once per PROJECT — at New for the launch repo, and from the scan for
	// every project a pass resolves a rendezvous for — so a watch that cannot be
	// created costs exactly the project it failed for.
	StartFocusWatcher func(gitCommonDir string) focusWatcher
	// RunGit runs git in dir and returns its stdout. Defaults to runGit (diff.go),
	// the same helper DiffFull and DiffNumstat are built on.
	//
	// It is the raw git seam rather than a purpose-named one because its single
	// caller needs the ARGV pinned, not the answer mocked: the scan resolves each
	// project's rendezvous with `rev-parse --path-format=absolute
	// --git-common-dir` (see gitCommonDirArgs, which documents what dropping the
	// flag silently costs), and a test can only assert that invocation through a
	// seam that carries it.
	RunGit func(dir string, args ...string) (string, error)
	// Pid identifies this dashboard for click-to-focus: requests are addressed to
	// afk-dash/request.<pid>.json so exactly one dashboard honors a click. It is
	// a seam only so a test can run two Models with different pids against one
	// directory; the default is os.Getpid, which is also what dashmeta uses to name
	// this dashboard's Entry, so the pid a click is addressed to is always the pid
	// whose pane and client tty the click switches to.
	Pid func() int
}

func (d Deps) withDefaults() Deps {
	if d.Discover == nil {
		d.Discover = defaultDiscover
	}
	if d.ReadTail == nil {
		d.ReadTail = readTail
	}
	if d.SpawnHost == nil {
		d.SpawnHost = defaultSpawnHost
	}
	if d.SpawnScratchHost == nil {
		d.SpawnScratchHost = defaultSpawnScratchHost
	}
	if d.WaitHostReady == nil {
		d.WaitHostReady = defaultWaitHostReady
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.SpiceDetect == nil {
		d.SpiceDetect = spice.Detect
	}
	if d.SpiceLogShort == nil {
		d.SpiceLogShort = spice.LogShort
	}
	if d.PlainBranches == nil {
		d.PlainBranches = func(repo string) ([]string, error) {
			return session.BranchesPlain(session.Deps{}, repo)
		}
	}
	if d.PlainTrunk == nil {
		d.PlainTrunk = func(repo string) (string, error) {
			return session.TrunkPlain(session.Deps{}, repo)
		}
	}
	if d.DiffFull == nil {
		d.DiffFull = defaultDiffFull
	}
	if d.DiffNumstat == nil {
		d.DiffNumstat = defaultDiffNumstat
	}
	if d.ResolveBaseRef == nil {
		d.ResolveBaseRef = defaultResolveBaseRef
	}
	if d.OpenHostPane == nil {
		d.OpenHostPane = openHostPane
	}
	if d.SendRestart == nil {
		d.SendRestart = defaultSendRestart
	}
	if d.CleanSession == nil {
		d.CleanSession = defaultCleanSession
	}
	if d.BranchMerged == nil {
		d.BranchMerged = defaultBranchMerged
	}
	if d.WriteLabel == nil {
		d.WriteLabel = session.WriteLabel
	}
	if d.ListSessionDirs == nil {
		d.ListSessionDirs = session.ListSessionDirs
	}
	if d.RunSpawn == nil {
		d.RunSpawn = func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() }
	}
	if d.StartFocusWatcher == nil {
		d.StartFocusWatcher = startFocusWatcher
	}
	if d.WriteDashEntry == nil {
		d.WriteDashEntry = dashmeta.WriteEntry
	}
	if d.RemoveDashEntry == nil {
		d.RemoveDashEntry = dashmeta.RemoveEntry
	}
	if d.RunGit == nil {
		d.RunGit = runGit
	}
	if d.Pid == nil {
		d.Pid = os.Getpid
	}
	// LoadBuild and AnswerInbox are absent for the same reason as the two below:
	// the first reads a real project's build files and the machine's usage
	// file, and the second writes a real inbox. A nil LoadBuild issues no
	// command (buildCmd), so a zero-Deps Model has no build data.
	//
	// PauseBuild and ResumeBuild are absent too: they write a real project's
	// pause flag, stop marker and decision log. A nil one makes its key say
	// pausing is not available here (buildpause.go) and calls nothing.
	//
	// Scan and LoadConfig are absent from this list deliberately, and it is the
	// one omission here that is load-bearing. Wiring them would make every
	// zero-Deps Model — the whole of this package's test suite included — read
	// the developer's real ~/.config/afk/config.toml and walk whatever search
	// roots it names. The scan pipeline is inert without them (scanCmd,
	// scanTickCmd), so opting in is `afk dash`'s decision alone.
	//
	// LoadListed is absent for the same reason: wired here, every zero-Deps
	// Model would read the developer's real config.toml. A nil LoadListed
	// issues no command (listedCmd).
	return d
}

// mouseChordKey is the chord that hands the mouse back and forth between the
// session and the host terminal. It lives here next to the keymap because that
// is where the dashboard binds it (keyMap.Mouse, matched with key.Matches).
//
// It is NOT the single point of truth for the chord, and this comment used to
// claim otherwise. `afk attach` has no keymap struct, so attachpane.go compares
// the chord directly (isCtrlChord(msg, 'g')) and never reads this constant.
// Retargeting the chord therefore means changing BOTH: this string and that
// comparison. The two are kept honest by the tests rather than by the type
// system — each front end has a toggle test that presses a real ctrl+g.
//
// It is the SECOND chord the hosted child never sees (after ctrl+\, the
// focus-release / detach chord), which is why ctrl+g: it is BEL, readline's
// `abort`, and emacs' keyboard-quit — a key whose meaning in an interactive
// child is already "cancel what I was typing", so intercepting it costs the
// session nothing a user would miss.
const mouseChordKey = "ctrl+g"

// keyMap holds the dashboard's few shared keybindings. Using bubbles/key
// keeps them declarative (matched via key.Matches) and lets the help line
// render straight from them.
//
// Since T16.2 the dashboard's actions are named in the key table
// (keytable.go), which says each action's key and the levels it works at;
// the bindings that only named a key for the help or a hint went with the
// leader table, so a key cannot be declared in two places that drift. What
// stays is what is matched as a binding: the list's cursor keys, enter (the
// placeholder's connect, and the footer's "open"), r (the placeholder's
// restart, and the wedge banner's key), the mouse chord in the overlays, and
// the leader.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Restart key.Binding
	Focus   key.Binding
	Mouse   key.Binding
	// Leader is the leader key, which in the Agent shows the key strip
	// (keystrip.go): ctrl+] by default, or the key config.toml's [dash]
	// leader sets, which New puts here. See its comment in defaultKeyMap
	// for how ctrl+] arrives.
	Leader key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		// Both directions preview the settled row's pane on their own — that
		// part is genuinely shared, not copy-paste — but the footer shows
		// both bindings side by side, where two identical "preview" labels
		// read as a glitch rather than a deliberate description. The arrow
		// glyph appended to each label is the distinguisher.
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "preview↑"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "preview↓"),
		),
		// wedgeRecoveryKey (hostpane.go) names this same key in the wedge
		// banner drawn inside a wedged pane — bound here, not re-declared,
		// so the banner's guidance and this binding cannot name two
		// different keys (see hostpane.go's decision comment on the const).
		// It is the one restart key (restartSelected): a stopped row restarts
		// at once, a wedged running row after a confirmation. The key table's
		// restart row is the same key.
		Restart: key.NewBinding(
			key.WithKeys(wedgeRecoveryKey),
			key.WithHelp(wedgeRecoveryKey, "restart"),
		),
		// enter goes in a level (§5): on the session list it opens the
		// selected session's agent (openAgent), and in the agent placeholder
		// it connects. The key table's "go in" row is the same key.
		Focus: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "open"),
		),
		Mouse: key.NewBinding(
			key.WithKeys(mouseChordKey),
			key.WithHelp("ctrl+g", "release mouse"),
		),
		// ctrl+] is the leader (T7.1; ctrl+] since T9.1). A terminal sends it
		// as the byte 0x1D, and ultraviolet's decoder turns 0x1D into
		// {Code: ']', Mod: ModCtrl} (decoder.go parseControl, which maps
		// 0x1C–0x1F to code+0x40); a kitty-protocol CSI 93;5u decodes to the
		// same key (parseKittyKeyboard). Either way String() is "ctrl+]", the
		// string this binding matches (docs/decisions.md "## T9.1"). Unlike
		// the old Ctrl+Space, which shares the NUL byte with ctrl+@ and
		// ctrl+2, no other chord sends 0x1D.
		//
		// It is a key only in the Agent (decideKey). There it takes a key
		// from the agent, which is why the strip gives it back: ctrl+]
		// pressed twice sends ctrl+] on, and the pane encodes it to 0x1D
		// again (sendLeaderToPane).
		//
		// This is the default. New replaces it with the leader config.toml
		// sets (Deps.Leader, checked by checkLeader), so the footers and
		// every hint name the key that is actually bound.
		Leader: leaderBinding(defaultLeaderKey),
	}
}

// paneFocus is which side of the dashboard's split owns the keyboard: the
// session list on the left, or the right pane. It is one of exactly two axes
// of interaction state (the other is rightView), and there are two rather
// than one combined enum or a set of flags because that is the model the user
// holds: "switch focus between the left and right panes, and when in the
// right pane, switch between the agent view and the overview, that's it. When
// on the left pane, you can't control anything in the right pane." Every
// state the dashboard can be in is a point on these two axes, so there is no
// focus a mask has to hide and no stale flag a reconcile pass has to clear.
//
// The zero value is focusList, so a zero Model starts with the keyboard on
// the dashboard.
type paneFocus int

const (
	// focusList: the session list owns the keyboard; the right pane is
	// display only.
	focusList paneFocus = iota
	// focusRight: the right pane owns the keyboard. Which surface receives it
	// — the agent's PTY or the overview's viewport — is rightView's question,
	// answered together with whether a pane is actually drawn (modeSnapshot).
	focusRight
)

// String exists for test-failure output; nothing renders it.
func (f paneFocus) String() string {
	switch f {
	case focusList:
		return "list"
	case focusRight:
		return "right"
	}
	return "unknown"
}

// rightView is which surface the right pane shows: the live agent session or
// the overview (summary/diff/stack tabs). It is dashboard-wide, not per
// session — one preference the user flips with ctrl+t, carried across rows —
// and it is orthogonal to paneFocus: the overview can be showing while the
// list has the keyboard, and the agent can be showing while the list has the
// keyboard. Neither axis implies the other, which is the whole reason they
// are two fields rather than one.
//
// The zero value is viewAgent: a fresh dashboard previews the agent.
type rightView int

const (
	// viewAgent: the right pane shows the session pane when one is installed
	// for the selected row, and the overview otherwise — there is nothing
	// else to draw for a row without a pane.
	viewAgent rightView = iota
	// viewOverview: the right pane shows the overview even when a pane is
	// installed. The pane stays alive and resizing behind it, so switching
	// back is instant.
	viewOverview
)

// String exists for test-failure output; nothing renders it.
func (v rightView) String() string {
	switch v {
	case viewAgent:
		return "agent"
	case viewOverview:
		return "overview"
	}
	return "unknown"
}

// Model is the dashboard state. It is a pure value: Update returns a new
// Model plus Cmds and performs no I/O.
type Model struct {
	repo     string
	interval time.Duration
	deps     Deps
	keys     keyMap

	// sessions is the last discovery result, sorted by name (internal/
	// discover sorts it).
	//
	// list is the sidebar's bubbles/list.Model, and it holds the SELECTION —
	// there is no cursor field beside it. Its items mirror this slice one for
	// one and are rebuilt from it by rebuildListItems, the single funnel every
	// mutation of `sessions` goes through, so the two can never disagree about
	// what is on screen or which row is selected. Because list.Model's own
	// index addresses its VISIBLE items, listSelection/selectSessionIndex are
	// what translate between that index and a position in this slice while a
	// filter is applied; nothing else should read list.Index() directly.
	sessions []Session
	list     list.Model

	// deadHostPIDs maps a session name to the host PID a pane-open attempt
	// PROVED was not serving it (see markHostStopped / applyDeadHostOverrides).
	// It exists because discovery's HostRunning is advisory and recomputed from
	// scratch every tick: without it, a stale host.json naming a recycled PID
	// would keep re-asserting "running" and undo the correction seconds after it
	// was made.
	deadHostPIDs map[string]int

	// pendingSpawns names the sessions whose background create is still
	// running, in submission order. mergePendingSpawns folds one placeholder row
	// per name into m.sessions on EVERY discovery result (applyLoaded replaces
	// that slice wholesale, so a one-time merge at keypress time would be wiped
	// by the next 2s tick). A name leaves this list when discovery reports the
	// real session or when its spawn finishes, whichever happens first.
	pendingSpawns []string

	// cleaningSessions is the set of session names whose in-process clean is
	// still running: populated when the confirm modal dispatches, cleared by the
	// cleanFinishedMsg handler.
	//
	// It exists because the old suspended shell-out was an accidental mutex: the
	// whole TUI was suspended, so a second `c` was physically impossible until the
	// clean returned. In-process it is not: the row stays in m.sessions until the
	// chained loadCmd's result lands (which itself waits on the clean, up to ~11s),
	// and Bubble Tea runs every Cmd on its own goroutine. Two concurrent cleans of
	// one session mean two `git add -A`/`commit` against a single index, an
	// archive step stat-ing a source the other already deleted, and two
	// `git worktree remove`. enter, ctrl+t and `r` consult this set too — a
	// clean SIGKILLs the session's host, which must never happen under a live
	// pane.
	//
	// Keyed by (name, cleanTargetKind), not by name: an identity-relevant entry
	// can share a name with a healthy or one-off session, and a name-only key
	// made one entry's in-flight clean block `c` on the OTHER entry for the
	// clean's whole duration. Same identity pair the confirm modal captures and
	// resolveCleanTarget matches on, for the same reason — one vocabulary, not
	// three.
	//
	// The name-only check is nonetheless deliberately BACK for the four paths
	// that have no row to reduce (see cleaningByName), and it is back because
	// KindStatusWithoutWorktree broke the argument that retired it: a stranded
	// `.afk/X/` is pane-capable while its clean identity is non-healthy, so
	// those paths looked up a key that could not exist and opened panes onto
	// sessions being torn down. The cost is the accepted over-refusal — a
	// healthy X and a stranded X block each other's ctrl+t/preview/focus/restart
	// for ~11s — and TestNameOnlyPathsRefuseAnySameNameClean pins it as
	// intended, not as a bug to fix.
	cleaningSessions map[cleanID]bool

	// stack is the repo-wide git-spice stack topology, refreshed by
	// stackCmd (stack.go) alongside sessions on every tick. Its zero value
	// (Detected: false) is exactly "git-spice not detected yet / absent",
	// which is why the stack panel never shows a placeholder before the
	// first refresh completes either.
	stack stackState

	// notifiedStackErr is the stack.Err text a notification has already been
	// raised for, mirroring notifiedLoadErr below: stackCmd re-runs on every
	// 2s tick (projectCmds), so an unsuppressed repeat would toast forever.
	// Cleared on recovery and by switchProject, same as notifiedLoadErr.
	notifiedStackErr string

	// stackRefreshing serializes stackCmd against the refresh ticker. It is a
	// shared pointer rather than a bool field because the guard has to survive
	// Model being copied by value through Init and Update, and has to be
	// readable from the Cmd goroutine that owns the invocation.
	stackRefreshing *atomic.Bool

	// build is the last build snapshot Deps.LoadBuild returned for the active
	// project, and buildKnown whether one has arrived since the project became
	// active. Both are cleared on a project switch (forgetBuild). buildKnown
	// stays false for the whole run when no loader is wired, which is what
	// keeps the screen exactly as it was before build data existed.
	build      buildview.Snapshot
	buildKnown bool

	// buildCursorID and buildCursorIndex are the Build section's cursor: the
	// task it is on and that task's row. The id is what the cursor follows
	// across reads; the index is where it lands when the id has left the list
	// (landBuildCursor). Both are re-landed on every build read and cleared
	// with the snapshot on a project switch (forgetBuild).
	buildCursorID    string
	buildCursorIndex int

	// section is the project view's focused section (T16.4, projectview.go):
	// Sessions, Build or Inbox. The zero value reads as Sessions
	// (focusedSection), so a Model built as a bare literal starts where New
	// starts it. It is a field beside focus rather than a new focus value: the
	// level (levels.go) and the input mode are derived from focus and view,
	// and a third focus value would have to be taught to every one of their
	// call sites for a choice that only the Project screen reads. It goes
	// back to Sessions on a project switch (forgetBuild).
	section projectSection

	// inboxCursorID and inboxCursorIndex are the Inbox section's cursor over
	// the open items, kept by id for the reason the Build cursor is: the
	// inbox is read again every two seconds and an item can be answered from
	// the web page between two reads (landInboxCursor).
	inboxCursorID    string
	inboxCursorIndex int

	// buildRefreshing keeps one slow build read from being stacked on itself
	// by the refresh ticker, the way stackRefreshing does for the stack. It is
	// a pointer for the same reason: the guard must survive the Model being
	// copied by value, and the Cmd goroutine reads it.
	buildRefreshing *atomic.Bool

	// planStatus is the last plan status Deps.LoadPlanStatus returned for the
	// active project; its Known field says whether one has arrived since the
	// project became active. planStatusRefreshing is the in-flight guard, a
	// pointer for the same reason buildRefreshing is.
	planStatus           PlanStatus
	planStatusRefreshing *atomic.Bool

	// taskTokens is the token record Deps.LoadTaskTokens last returned for
	// the Build section cursor's task, and taskTokensID the task it is for ("" when
	// none is held). Only one is held: only the cursor's row shows a total.
	// taskTokensPending is the task "show this task's tokens" is waiting on
	// when it ran before the record was held, and taskTokensLoading the
	// in-flight guard, a pointer for the reason buildRefreshing is. All four
	// are cleared on a project switch (forgetTaskTokens).
	taskTokens        TaskTokens
	taskTokensID      string
	taskTokensPending string
	taskTokensLoading *atomic.Bool

	// The all-projects screen (allprojects.go). allProjectsOpen is its "is it
	// open" flag, beside the overlays' for their reason; while it is set the
	// screen replaces the list and the detail side. allProjectsRows are the
	// rows as last read and sorted, allProjectsKnown whether a read has landed
	// since it opened, allProjectsCursor the selected row, allProjectsErr why
	// the last read of config.toml failed and allProjectsConfigPath the file
	// it names. allProjectsGen counts openings, so a read started under an
	// earlier one is dropped; allProjectsRefreshing is the in-flight guard, a
	// pointer for the reason buildRefreshing is.
	allProjectsOpen       bool
	allProjectsRows       []projectrows.Row
	allProjectsKnown      bool
	allProjectsCursor     int
	allProjectsErr        string
	allProjectsConfigPath string
	allProjectsGen        int
	allProjectsRefreshing *atomic.Bool
	// everyProject is the newest read of every project's row and task list,
	// kept at every level (T16.6) apart from the screen's own rows above.
	// everyProjectSeq is the start number of the read it came from,
	// everyProjectAt when that read finished, and allProjectsSeq the shared
	// counter that numbers reads as they start.
	everyProject    AllProjects
	everyProjectSeq int64
	everyProjectAt  time.Time
	// homeUsage is the usage reading that read carried, and homeUsageKnown
	// whether it had one; the home screen falls back on it when no project
	// has filled m.build.
	homeUsage      usagestate.File
	homeUsageKnown bool
	allProjectsSeq *atomic.Int64
	// homeActivity is the last LoadActivity read (activityview.go) and
	// homeActivityKnown whether one has landed. activityRoots are the sorted
	// roots that read was for, so a rows read with the same set of projects
	// starts no new one; activityRefreshing is its in-flight guard, a pointer
	// for the reason buildRefreshing is.
	homeActivity       HomeActivity
	homeActivityKnown  bool
	activityRoots      []string
	activityRefreshing *atomic.Bool
	// allProjectsFollow is the folder an add from the screen just listed: the
	// next read that has a row for it puts the cursor there, so the owner
	// sees the row they made. It is cleared when that happens and when the
	// screen opens again (projectedit.go).
	allProjectsFollow string
	// inboxFollow is the inbox item `a` at All projects is taking the owner
	// to. The inbox opens on it when the snapshot of the project switched to
	// lands (applyBuildLoaded); a project switch clears it.
	inboxFollow string
	// needAt is the item the last `]` or `[` landed on, with the project
	// and level it landed at (T16.6). needHereNow passes it on as where the
	// owner is only while they are still there, and a cursor move clears
	// it, so the next `]` steps past a plan or a gone project rather than
	// landing on it again.
	needAt        needKey
	needAtProject string
	needAtLevel   level

	// The add form and the remove confirm of the all-projects screen
	// (T7.10, projectedit.go). projectAddInput is the form's one field, used
	// by both steps; projectAddDir is the folder step one took, already
	// expanded, and projectAddNote the line under the field after an enter
	// that could not go on. projectRemoveRow is a copy of the row the confirm
	// was raised for, so y removes exactly the project the dialog named
	// whatever the two-second refresh does to the rows meanwhile.
	projectAddInput  textinput.Model
	projectAddDir    string
	projectAddNote   string
	projectRemoveRow projectrows.Row

	// The Build section's "set this task's status" (T7.11, taskstatus.go).
	// taskStatus is a copy of the cursor's task taken when the picker
	// opened, so y changes exactly the task the dialog names whatever the
	// two-second build read does to the rows meanwhile. taskStatusPick is the
	// picker's highlighted row, an index into taskStatusChoices;
	// taskStatusNew and taskStatusReplacedBy are the choice the confirm
	// shows. taskStatusInput and taskStatusNote are the replacing-ids step's
	// field and the line under it after a blank enter. taskStatusSetting is
	// set from y until the result is back, so a second change cannot start
	// meanwhile.
	taskStatus           taskStatusTarget
	taskStatusPick       int
	taskStatusNew        string
	taskStatusReplacedBy []string
	taskStatusInput      textinput.Model
	taskStatusNote       string
	taskStatusSetting    bool

	// usageRefreshing is set from the "refresh usage" action until its
	// result is back, so a second press cannot start a second reading of
	// the agent's usage meanwhile (usagerefresh.go). It is not reset on a
	// project switch: the reading is the machine's, not the project's.
	usageRefreshing bool

	plainBranches []string
	plainTrunk    string
	// notifiedBranchesErr is the plainBranchesLoadedMsg listing-failure text a
	// notification has already been raised for — the genuine-listing-failure
	// case only; a trunk-only failure never sets it (see
	// notifyBranchesError). Cleared on recovery and by switchProject, same as
	// notifiedLoadErr.
	notifiedBranchesErr string

	// notifiedScanErr is the projectScanMsg config-parse-failure text a
	// notification has already been raised for, mirroring notifiedLoadErr:
	// scanTickMsg re-drives applyProjectScan every scan interval, so an
	// unsuppressed repeat would toast on every pass for a config file that
	// stays malformed. Cleared on recovery, so a config that breaks, gets
	// fixed, then breaks again notifies both times. Not reset by
	// switchProject — the scanned config is global, not per-project, unlike
	// notifiedStackErr/notifiedBranchesErr above.
	notifiedScanErr string

	// notifiedListedErr is the unreadable-project-list notice already raised,
	// for the same once-per-distinct-text rule as notifiedScanErr (the list is
	// read again on every scan tick). Cleared by the next good read. Not reset
	// by switchProject: the project list belongs to the machine, not to one
	// project.
	notifiedListedErr string

	width  int
	height int
	ready  bool

	// loadErr is a whole-dashboard discovery failure (e.g. git worktree
	// enumeration failed); empty on success. Per-session staleness is held
	// on each Session, not here.
	//
	// Nothing renders it any more — the footer's discovery-error row is gone
	// and the failure surfaces as a notification instead. It is kept as the
	// live "is discovery failing, and with what text" fact that
	// notifyDiscoveryError compares against to fire once per distinct failure.
	loadErr string
	// notifiedLoadErr is the loadErr text a notification has already been
	// raised for, so a failure that persists across the 2s ticker is reported
	// once rather than every pass. Cleared on recovery, so the same error
	// returning after a good pass is reported again.
	notifiedLoadErr string
	lastLoad        time.Time
	// loadFailingSince is when the current run of discovery failures began,
	// zero while discovery is healthy. It exists because lastLoad cannot carry
	// this: lastLoad means "a load has landed for the current project", and
	// making it mean "a load SUCCEEDED" would quietly change what reads it
	// under a failing discovery. The header reports staleness from this
	// instead.
	loadFailingSince time.Time

	// status is a transient one-line message (pane/restart outcomes, exec
	// outcomes, recovery guidance) shown above the help line. It is assigned
	// only by notify and clearStatus (notification.go), which is what lets
	// statusKind be trusted as describing the text currently in it.
	status     string
	statusKind notificationKind
	// pendingBackground is the background-origin events awaiting the status
	// slot: those collected during the current Update, plus any deferred by
	// an earlier one because a toast was live. Update's wrapper flushes it
	// after every message (notification.go's flushBackground). Nil is the
	// common case; a non-nil slice here after Update returns means something
	// is waiting for a toast to expire.
	pendingBackground []backgroundEvent

	// modal is the footer modal currently up, or inputNormal when none is —
	// one of the inputMode values isModal reports true for, never any
	// other. It is one of the four facts modeSnapshot reads (mode.go); the
	// modal OUTRANKS every other surface, so this field alone decides who owns
	// the keyboard whenever it is set.
	modal inputMode

	// spawnErrName/spawnErrHeadline/spawnErrTail are the spawn-error modal's
	// content, captured when spawnFinishedMsg reported a failure and cleared on
	// dismissal. The tail is the only record of the failure the user can see —
	// the background child's output never reached the terminal.
	spawnErrName     string
	spawnErrHeadline string
	spawnErrTail     []string

	// confirmTarget and confirmPhase are the pending clean target's name and
	// remembered phase, captured at the moment inputConfirmClean is
	// entered (the Cmd-creation-time capture invariant: the confirm modal's
	// `y` handler uses these, never a fresh m.selected() lookup, because
	// background ticks reshuffle m.sessions while the modal is up). An empty
	// confirmPhase means the session was stale when confirm mode was entered
	// (a non-stale session always has a non-empty Phase) — the view renders
	// "status stale" for that case instead of an empty phase string.
	confirmTarget string
	confirmPhase  state.Phase
	// confirmScratch remembers whether the pending clean target is a one-off
	// session, captured at modal entry on the same invariant as the two fields
	// above (never re-read from the reshuffling selection). It selects the
	// modal's copy: a scratch clean stops the host and removes .afk/{name}/
	// only, so the standard "Worktree goes; archive kept." line would be false
	// on a destructive-action confirmation, and a scratch
	// session's empty phase must not present as "unknown" (it has no lifecycle
	// to be unknown about).
	confirmScratch bool
	// confirmKind is the second half of the captured identity: the
	// worktree-side fault the confirmed row carried, reduced through
	// cleanTargetKind so a stale-status row confirms as KindHealthy and keeps
	// resolving the way it always has. Captured with the fields above and
	// cleared on every exit for confirmScratch's reason — a kind left set
	// would be applied to the NEXT session confirmed, and a healthy session
	// cleaned under KindWorktreeMissing resolves to nothing at all.
	confirmKind session.Kind
	// confirmMerge is the answer to "has this branch already been merged?" for
	// the confirmed target, filled in asynchronously by cleanMergeStateMsg
	// because the check is a git invocation and Update must never run one (see
	// cleanMergeStateCmd). It stays zero — known false — until that answer
	// lands, and `d` treats an unknown merge state as UNMERGED, so a keypress
	// arriving first is never blocked and never assumed safe.
	//
	// Captured-at-entry like the four fields above, in the sense that matters:
	// the request names the row selected when `c` was pressed, and the answer
	// is dropped unless it still matches confirmTarget/confirmKind. A
	// reshuffling tick can move or delete that row while the modal is up.
	confirmMerge cleanMergeState
	// confirmDeleteUnmerged is the modal's second state: `d` was pressed on a
	// branch that is not known to be merged, and the footer has swapped to the
	// "unmerged commits will be lost" confirmation. Only a SECOND `d` deletes
	// from here. It is cleared on every exit for confirmScratch's reason and
	// then some — leaked, it would turn the next modal's first `d` into an
	// immediate unmerged delete.
	confirmDeleteUnmerged bool

	// confirmRestartTarget/confirmRestartWorktree are restartWedgedSelected's
	// captured target, the inputConfirmRestart analogue of confirmTarget/
	// confirmPhase: captured once on entry, read (never re-selected) by
	// confirmRestart for the same reshuffling-tick
	// reason confirmTarget's doc gives.
	confirmRestartTarget   string
	confirmRestartWorktree string

	// labelInput is inputEditLabel's single field (bubbles/textinput, the
	// same component newform.go's nameInput uses). Its zero value is not
	// safe (same caveat as newForm's components, newform.go:24-28) — only
	// ever read or updated while m.modal == inputEditLabel.
	//
	// labelTarget/labelWorktree are the session's name and worktree,
	// captured at openLabelForm time on the same invariant confirmTarget and
	// confirmRestartTarget already document: the 2s discovery ticker keeps
	// running under the prompt, so submitLabelForm must write against the
	// session that was selected when 'l' was pressed, never a fresh
	// m.selected() lookup that a reshuffling tick could have moved.
	labelInput    textinput.Model
	labelTarget   string
	labelWorktree string

	// planTextInput is inputPlanText's field, on labelInput's terms: only
	// read or updated while m.modal == inputPlanText. planTextMode is the
	// form's purpose (PlanModeAmend, PlanModeReconcile, PlanModeFix or
	// PlanModeNew);
	// planTextRoot and planTextGen are the project it was opened for,
	// captured at open on labelTarget's invariant, so a submit after a project
	// switch is refused rather than sent to the wrong project. The new-plan
	// form leaves planTextRoot empty: its folder is what the owner types. planTextNote is the line shown
	// under the field after a blank enter.
	planTextInput textinput.Model
	planTextMode  string
	planTextRoot  string
	planTextGen   int
	planTextNote  string

	// planOffer is the refusal inputConfirmPlanOpen was raised for (a
	// planning session is already open), and planOfferGen the project
	// generation it arrived under. y sends the resume request to its Root.
	planOffer    PlanStartResult
	planOfferGen int

	// planFolder is the decision inputConfirmPlanFolder was raised for (a
	// folder to create, or one that already exists and may be planned in),
	// and planFolderGen the project generation it arrived under, on
	// planOffer's terms: y acts on exactly the path the dialog showed.
	planFolder    PlanFolderDecision
	planFolderGen int

	// githubPath and githubRepo are the folder and the repository
	// inputConfirmGitHubRepo was raised for, and githubGen the project
	// generation it arrived under, on planFolder's terms: y makes exactly
	// the repository the dialog named, for exactly that folder.
	githubPath string
	githubRepo string
	githubGen  int

	// planApprove is a copy of the plan status inputConfirmPlanApprove was
	// raised with, planApproveRoot the project folder and planApproveGen the
	// project generation it was raised under, on githubPath's terms: y
	// approves exactly the project the dialog named, and the counts the
	// dialog shows cannot change under the owner's eyes when the two-second
	// refresh stores a newer status. planApproving is set from y until the
	// outcome is back, so a second approve cannot start a second commit and
	// push of the same plan meanwhile (planapprove.go).
	planApprove     PlanStatus
	planApproveRoot string
	planApproveGen  int
	planApproving   bool

	// pendingPaneLead is the GitHub step's outcome line while the
	// master-plan pane a new plan's start opened is still dialing
	// (armPaneLead). The pane's arrival notice would otherwise replace it the
	// moment the pane connects, so the arrival puts it first (withPaneLead)
	// and disarms it. A project switch drops it with planSelectPending.
	pendingPaneLead string

	// planSelectPending is the master-plan row's name while a plan action's
	// pane open waits for discovery to list the row (openPlanPane). It is
	// applied on each successful load and dropped once the row is selected,
	// once the keyboard leaves the right side, or on a project switch.
	planSelectPending string

	// providerPick is inputPickProvider's highlighted row, an index into
	// provider.Names() — the same representation newForm.provider uses, so
	// the two selectors cannot disagree about order. openProviderPicker
	// resets it to defaultProviderIndex() on EVERY open: a one-off is
	// usually a Claude one-off, and a highlight remembered from a previous
	// pick would turn a reflexive `o`, enter into a session on whatever
	// provider was chosen last time. Only read while m.modal ==
	// inputPickProvider.
	providerPick int

	// statusChangedAt is when the status last changed to a non-empty value,
	// recorded by notify (notification.go) so toast.go can float a fresh
	// message. The zero value means "never", which toastVisible treats as
	// not fresh.
	statusChangedAt time.Time

	// detailTab is which of the detail pane's tabs is showing (views.go's
	// detailTab). It is one setting for the whole dashboard, not one per
	// session: a person comparing two sessions' waves wants the same tab after
	// moving the cursor, and detailViewportFor already resets the scroll per
	// session. The zero value is the Overview, which is also what a tab the
	// current repo cannot show (Stack without git-spice) falls back to — see
	// activeDetailTab.
	detailTab detailTab

	// detailViewport holds the detail pane's scroll position. It lives on
	// Model, and is mutated ONLY from Update (syncDetailViewport on
	// tea.WindowSizeMsg, on every list rebuild and before every scroll key;
	// resetDetailScroll's snap to the top when the selected session changes;
	// the four actionDetail* cases in applyKeyAction that W4.T1 wired to
	// shift+↑/shift+↓/ctrl+u/ctrl+d), never from View or any render*
	// function, for the reason renderNewForm's and switcher's doc comments
	// already flag for their own state: View takes a value receiver
	// (`func (m Model) View() string`, views.go), so a mutation applied
	// during render lands on a copy the caller discards and is gone before
	// the next frame. A viewport that clamped or resized itself while
	// rendering would appear to work once and then silently stop clamping.
	// renderDetailsOverlay (detailsoverlay.go), called from View every
	// frame the details overlay is up, reads this field's offset to bound
	// content before composing the box (the fix for the
	// MaxHeight-after-applyBorder bug W1.T1's failing test documents);
	// W4.T1's actionDetail* cases scroll it
	// directly, after syncDetailViewport has seeded real content and the
	// frame's own row count — see its doc for why both are required.
	detailViewport viewport.Model

	// detailViewportFor is the name of the session detailViewport's current
	// scroll offset belongs to. resetDetailScroll compares it against the
	// newly-selected session from both funnels a selection change can take
	// — schedulePreview for every key-driven one (cursor move, filter
	// start/clear/edit) and rebuildListItems for every tick-driven one (a
	// discovery load or pending-spawn merge that changes which session sits
	// at an unmoved index) — and, on a mismatch, snaps detailViewport back to
	// the top before updating this field. Without it, moving the cursor from
	// a session scrolled halfway down its detail pane onto a different
	// session would show that session's content starting mid-scroll instead
	// of at the top.
	detailViewportFor string

	// pane is the embedded terminal pane currently on screen, or nil when none is
	// open. The dashboard renders one at a time and a connect switches it
	// between sessions (see applyPaneAttached) — a switch detaches the
	// outgoing session rather than ending it, so this is a view onto one of
	// many running sessions, not a limit on how many can exist. paneName is
	// the session it is connected to. Whether keyboard input routes to it is
	// NOT a field on the pane: it is derived from focus and view below
	// (paneOwnsKeyboard).
	pane     paneHandle
	paneName string

	// focus and view are the dashboard's entire interaction state: which side
	// of the split owns the keyboard, and which surface the right side shows.
	// The four flags they replace (a pane-focused flag, a detail-focused flag,
	// a pane-hidden flag and a saved focus to restore on reveal) could encode
	// states the user has no model for — a focused pane hidden behind the
	// summary, a focused detail with the pane drawn over it — and each one
	// needed a mask or a reconcile pass to paper over. See the two type docs.
	focus paneFocus
	view  rightView

	// previewGen is what makes a superseded preview timer INERT. tea.Tick Cmds
	// cannot be cancelled, so every armed timer fires; each one carries the
	// generation it was armed with, and applyPreviewTimer drops any message whose
	// generation is no longer the current one. Incrementing it is therefore the
	// only way to invalidate a pending preview — schedulePreview bumps it on every
	// path (including the ones that arm nothing, so moving onto a row that cannot
	// be previewed still cancels the previous row's timer), and openPaneFor bumps
	// it so an explicit open cannot be stolen back by a timer armed before it.
	previewGen int

	// projectGen is what makes a pane dial outstanding across a PROJECT SWITCH
	// inert, on exactly previewGen's mechanics (above): a dispatched dial cannot
	// be cancelled, so every paneOpenRequest is stamped with the generation it was
	// created under (by openPaneFor and restartHostCmd — the dispatch funnels,
	// never a call site), the paneAttachedMsg echoes it back, and applyPaneAttached
	// drops a stale arrival, closing the connection it was handed. Without the
	// guard, a dial outstanding when the user switches projects installs a pane
	// for a session that is not in the current list — and when the NEW project has
	// a same-named session, the surplus-connection branch keeps the OLD project's
	// pane and closes the new one, routing `f` keystrokes into a different
	// project's Claude session.
	//
	// switchProjectCmds bumps it on every actual switch, beside its previewGen
	// bump; both share the discarded-copy hazard schedulePreview's comment
	// documents (the bump must land on the Model the caller keeps).
	projectGen int

	// attachInFlight records the pane-open whose dial has not yet reported back,
	// zero when none is outstanding. It closes the reverse of previewGen's race:
	// bumping the generation kills timers armed BEFORE an explicit open, but a dial
	// can take seconds, and a `j` pressed during it would arm a preview that fires
	// while the explicit attach is still in flight — opening a second connection and
	// needlessly stealing that session too.
	//
	// It records EVERY open, previews included, because the harm is symmetric:
	// enter pressed while a preview is dialing the same session would otherwise
	// dial it a second time, and the host answers a second client by sending Stolen
	// to the first. Knowing a dial is already outstanding is what lets
	// connectSelected claim it instead (see inFlightOpen).
	attachInFlight inFlightOpen

	// restartInFlight is attachInFlight for host respawns. A second connect for
	// the same name joins it instead of spawning a second host, whose bind probe
	// would steal the first one's pane.
	restartInFlight inFlightRestart

	// paneLost is a pane that ended while its session kept running (steal, drop,
	// read error), so the agent view can offer a reconnect instead of drawing
	// the row as if nothing happened.
	paneLost lostPane

	// spinner animates on the help row while any operation hasInFlightWork
	// reads (a create, a clean, a restart request) is outstanding. It is driven
	// by spinner.TickMsg, self-perpetuating once started (see
	// startSpinnerIfNeeded and the TickMsg case in Update) — spinnerActive and
	// otherOpsInFlight below are the two pieces of bookkeeping that mechanism
	// needs and that no existing tracker already provides.
	spinner spinner.Model

	// spinnerActive is true while the spinner's tick loop is running. It is
	// the guard against starting a SECOND, interleaved loop when a second
	// operation goes in flight while one is already ticking — without it,
	// two concurrent tea.Tick chains would double the spinner's frame rate.
	// It is set on the transition into hasInFlightWork() (by
	// startSpinnerIfNeeded) and cleared on the transition out of it (in the
	// TickMsg case, which is also what stops the tick loop from perpetuating
	// itself once nothing is left in flight).
	spinnerActive bool

	// otherOpsInFlight counts in-flight operations with no tracker of their
	// own: the wedged-host restart (restartChildCmd), the stopped-host
	// respawn (restartHostCmd), and the label write (writeLabelCmd). Session
	// creation, cleaning, and pane-open dials already keep their own records
	// for reasons unrelated to the spinner (pendingSpawns, cleaningSessions,
	// attachInFlight) — see hasInFlightWork, which reads all four rather than
	// inventing a fifth copy of facts three of them already track.
	otherOpsInFlight int

	// mouseCapture records whether the PROGRAM currently holds the terminal's
	// mouse reporting — not whether the dashboard feels like routing events. The
	// two must not be allowed to differ: the ctrl+g toggle returns
	// tea.DisableMouse / tea.EnableMouseCellMotion so the real DEC modes follow
	// this flag, because a routing-only flag would leave the host terminal in
	// mouse-reporting mode and native click-drag selection — the entire point of
	// releasing the mouse — would stay broken.
	//
	// It starts true because dash.go constructs the program with
	// tea.WithMouseCellMotion(), so capture is on before the first Update runs.
	mouseCapture bool

	// initialSession is the session Deps.InitialSessionName named at startup
	// (afk new's TTY handoff, or `afk dash --open`), captured once in New.
	// Empty means no startup pane was requested. firstLoadDone/ready gate
	// maybeOpenInitialPane: the pane needs concrete cols/rows (from the first
	// tea.WindowSizeMsg — ready) AND a populated session list (from the first
	// discovery pass — firstLoadDone) before it can be opened, and either can
	// arrive first. initialPaneAttempted ensures the attempt (found or not)
	// happens at most once, so a session that never shows up does not retry
	// on every subsequent refresh tick.
	initialSession       string
	firstLoadDone        bool
	initialPaneAttempted bool

	// previewOnNextLoad arms a one-shot preview on the first
	// sessionsLoadedMsg after a SWITCHER-driven project switch or at startup.
	// It exists because schedulePreview needs a selected row to arm against,
	// and at switch time switchProjectCmds has just set m.sessions = nil —
	// there is nothing to preview yet, only a load in flight that will land
	// later, asynchronously, as its own Update case. WHICH row gets previewed
	// is not this flag's business: the sessionsLoadedMsg case restores the
	// remembered session for the incoming project first when one is recorded
	// and still in the list, and otherwise leaves applyLoaded's row 0 — see
	// that case, which is also the only consumer.
	//
	// Exactly two paths set it, and both are user intent to land somewhere.
	// The first is a project switch the owner chose (jumpToProject, enter on
	// an All projects row, and the needs-you and `a` landings): the other two
	// switchProjectCmds callers deliberately do not. applyFocusRequest already
	// dispatches its own explicit open after the switch, and arming a preview
	// on top of it would race a second dial against the one the click itself
	// asked for. applyScanResult's fallback to the first-discovered project is
	// not a user selection at all — it is automatic reconciliation for a
	// vanished active project — so there is no user-facing "switch" for a
	// preview to be a reward for.
	//
	// The second is New: a startup with a remembered selection for the launch
	// project (Deps.LoadSelections) sets this flag too, so the very first
	// sessionsLoadedMsg previews the row the user was last on instead of
	// waiting for them to move the cursor. It stays false when
	// Deps.InitialSessionName is set — an explicit `--open` request already
	// names the pane to show, and arming a preview on top of it would race a
	// second dial against the one the request itself asked for, the same
	// reason applyFocusRequest's explicit open suppresses it above.
	//
	// The startup arm opens one race the switcher arm never could, because it
	// is the only one live before the first load: a focus-request click on a
	// STOPPED session in the launch project goes out as restartHostCmd
	// (applyFocusRequest's !target.hostRunning branch), not openPaneFor, and
	// openPaneFor is where a non-preview explicit open clears this flag. The
	// clear still happens — hostRestartedMsg chains into openPaneFor — but
	// only once the respawn returns, which can be after the first load has
	// already consumed the flag and dialed the remembered session's preview
	// alongside the restart's own open. Left as is rather than also clearing
	// at the restart dispatch site: the flag is consumed once either way, and
	// of the two dials only the click's carries focusOnArrival (the preview's
	// request in applyPreviewTimer sets preview, never focus), so the extra
	// dial can cost a pane swap but never the keyboard.
	previewOnNextLoad bool

	// selectOnNextLoad is the session NAME to put the cursor on at the first
	// SUCCESSFUL sessionsLoadedMsg after a click-to-focus request switched
	// projects. It exists for the half of applyFocusRequest's job that cannot
	// be done at request time: the switch has just set m.sessions = nil, so
	// there is no row to select yet, only a load in flight that will land later
	// as its own Update case (the same reason previewOnNextLoad exists).
	//
	// It is deliberately NOT previewOnNextLoad, and must not be folded into it.
	// That flag ARMS A PREVIEW, and a preview on this path would race a second
	// dial against the explicit open the click itself just dispatched — the race
	// previewOnNextLoad's own doc comment describes above, and the reason
	// switchProjectCmds clears that flag for every switch while only the
	// switcher re-arms it. This field carries a name and moves a cursor; it
	// dials nothing. The two also differ in WHICH row they land on:
	// previewOnNextLoad restores whatever rememberedSelection holds for the
	// incoming project, where this one names the clicked session and overrides
	// that memory (and then re-records it, via rememberSelectedRow).
	//
	// Empty means nothing armed. switchProjectCmds clears it alongside
	// previewOnNextLoad so a LATER switch — by the switcher, or by a second
	// click — never inherits an arm left by an earlier one whose load never
	// landed.
	selectOnNextLoad string

	// projects and activeProject are the switcher's list and selection,
	// captured from Deps.Projects / Deps.ActiveProjectIndex in New. This task
	// (W2.T2) only threads them onto the Model; W3.T1's scan ticker is what
	// grows projects beyond the single cwd-derived entry and repoints
	// activeProject as the list changes.
	projects      []project.Project
	activeProject int

	// projectSessions caches one discovery result per project path, refreshed by
	// each scan pass — it is what lets the switcher show session counts and
	// awaiting-input state for projects that are not the active one. Its nil zero
	// value is exactly "nothing scanned yet", and reads from a nil map are legal,
	// so a single-project Model needs no initialization and behaves as before.
	//
	// The ACTIVE project's rows do not come from here: they come from m.sessions,
	// which the 2s discovery tick keeps fresher than any scan interval could.
	projectSessions map[string][]discover.SessionState

	// rememberedSelection is the in-memory mirror of the on-disk
	// selection-memory state, keyed by selectionKey() — the RESOLVED project
	// path, never the raw launch cwd (see selectionKeyFor's doc comment for
	// why the two can differ at startup). New seeds it from
	// Deps.LoadSelections; W3.T1's recording path is what adds to it and
	// persists via Deps.RememberSelection. A nil map is legal and reads as
	// "nothing remembered for any project", exactly how an un-wired Model (nil
	// LoadSelections) behaves, so no initialization is required. Entries are
	// never evicted — a project that drops out of the scan keeps its
	// remembered session in case it reappears (plan Context).
	rememberedSelection map[string]string

	// dirtySelections is the subset of rememberedSelection changed since the
	// last flush, keyed identically (resolved project path → session name).
	// The 2s discovery tick drains it, which is what debounces the disk write
	// down to at most one per pending key per tick however fast the cursor
	// moves.
	//
	// It is a per-key SET and not a single "something changed" flag: a cursor
	// move in project A followed by a switch to B inside the same tick must
	// still write A's entry, and a flag can only be resolved against
	// selectionKey() at flush time — which by then answers B. A's change would
	// be lost for the rest of the run, because rememberedSelection already
	// holds it, so no later move re-dirties it.
	dirtySelections map[string]string

	// projectOpens is the in-memory tally of how often each project has been
	// deliberately opened, keyed by Project.Path (NOT by selectionKey(): the
	// switcher's rows, its counts map and the per-project session cache are
	// all keyed by the resolved entry path, and selectionKey falls back to the
	// launch repo, which for a subdirectory or symlinked launch is a spelling
	// no project entry carries). New seeds it from Deps.LoadProjectOpens and
	// recordProjectOpen is the only thing that adds to it.
	//
	// It is the authoritative count for the run: the switcher re-reads it on
	// every construction, so an increment reorders the rows on the next open
	// with no rescan and no disk round-trip. A nil map is legal and reads as
	// "nothing ever opened", which is exactly how an un-wired Model behaves.
	projectOpens map[string]int

	// dirtyOpens is how many opens have been recorded per project since the
	// last flush — a tally of PENDING INCREMENTS, not a mirror of
	// projectOpens. Deps.RecordProjectOpen adds one to the on-disk count per
	// call, so two opens of one project inside a single tick need two calls;
	// a per-key set (dirtySelections' shape, where the value IS the state to
	// write) would flush one of them and leave the file permanently one
	// behind the in-memory count.
	//
	// It is per-key for the same reason dirtySelections is: opens of two
	// different projects inside one tick must both be written, and a single
	// "something changed" flag can only be resolved against the CURRENT
	// project at flush time.
	dirtyOpens map[string]int

	// scanInterval is the project-scan ticker's cadence, seeded with
	// defaultScanInterval in New and repointed by every scan result — the config
	// is re-read on each pass, so editing scan_interval takes effect within one
	// interval rather than at the next launch. It is deliberately not m.interval;
	// see scanTickMsg for why the two cadences cannot be shared.
	scanInterval time.Duration

	// listed is the machine's project list as last read WELL, and found is what
	// the last GOOD folder search found. Both are remembered, rather than only
	// the merged m.projects, because the two reads land at different times and
	// each has to be merged against the other's last value: a listed read that
	// arrives between two scans must not drop the found rows, and a scan must
	// not drop the listed ones. A read that failed leaves its half untouched
	// (foldListedRead, applyScanResult). found starts as the unpinned entries
	// the caller handed in; see unpinnedProjects.
	listed []ListedProject
	found  []project.Project

	// startedProjects is every folder a "start a new plan" moved the dashboard
	// to during this run, in the order they were started, each once. It is
	// held for the run only and never written anywhere: starting a plan does
	// not list the folder in config.toml (`afk plan approve` does, and the
	// dashboard writes that file only through the all-projects screen's add
	// and remove, T7.10), so after a restart the folder is back only through
	// `afk dash --project`, a search folder or that add. It is merged as its own source (mergeInput.started) because the
	// found and listed halves are replaced by every read and would drop it.
	startedProjects []string

	// configPath is where LoadConfig resolved the config file on the last scan,
	// held for the empty state to render. It stays empty while the scan pipeline
	// is inert, which any reader has to tolerate.
	configPath string

	// gitCommonDir and focusWatch are the click-to-focus request channel: a
	// clicked notification writes afk-dash/request.json under gitCommonDir and
	// focusWatch is the fsnotify watcher that wakes on it (focusrequest.go). A nil
	// focusWatch means no fast path was available, and the 2s discovery tick's
	// fallback poll carries the mechanism on its own — both are silent about it,
	// because a click that cannot be honored must do nothing rather than warn.
	gitCommonDir string
	focusWatch   focusWatcher
	// rendezvous is the same channel for EVERY project the switcher knows, keyed
	// by project path: each scan pass resolves a project's git common dir and
	// starts a watcher over it, so a click on a notification from a project this
	// dashboard is not currently showing still lands (applyFocusRequest switches
	// first, then opens).
	//
	// The launch project's directory (gitCommonDir above) appears here too once a
	// scan attributes it, but its watcher does not: focusWatch already covers that
	// directory, and rendezvousDirs deduplicates it so the tick polls it once, the
	// scan writes its entry once, and quit removes it once.
	//
	// A project absent from this map has no click-to-focus and nothing else wrong
	// with it — an unresolvable git common dir or an unwritable afk-dash/ is a
	// per-project degradation, never a dashboard failure, exactly as the absence
	// of a notifier is not one.
	rendezvous map[string]focusRendezvous
	// dashEntry is the tmux location this dashboard publishes into that same
	// directory for `afk notify` to aim at, captured from Deps in New. It is the
	// outbound half of the rendezvous the two fields above are the inbound half
	// of, which is why it lives here rather than beside the pane state.
	dashEntry dashEntryMeta
	// pid identifies this dashboard, and requests are addressed to it: the file
	// this dashboard reads, watches for, and deletes is request.<pid>.json. Held
	// as a field rather than called at each use so tests can drive two distinct
	// dashboards against one directory — which is the case the shared-file design
	// got wrong.
	pid int

	// form is the in-TUI session-creation form's state; formOpen is the "is it
	// open" flag, mirroring pane's own nil-handle sentinel pattern. It is one of
	// the four facts modeSnapshot reads: while it is set the derived mode is
	// inputForm (unless a modal or the overlay outranks it), and inputForm's
	// spec sends every unclaimed key to the focused component instead of the
	// dashboard's bindings — see formKey's routing table for how Tab, Enter, Esc
	// and the base filter divide up the shared keys.
	form     newForm
	formOpen bool

	// jump is the jump search's state (jump.go, §4.6); jumpOpen is its "is
	// it open" flag, alongside formOpen rather than folded into modal —
	// modals are mutually exclusive, and this genuinely coexists with them.
	//
	// Its place in modePrecedence is the project switcher's, which it
	// replaced (T16.6): above the form and the pane and below every modal.
	// Three states can therefore coexist with it and each resolves the same
	// way:
	//
	//   - A pane, visible or hidden. The box draws over it; the pane keeps
	//     running and stays connected (opening the search is not choosing
	//     anything — the landing is what switches or detaches).
	//   - A FOCUSED pane, reachable only by a focusOnArrival attach landing
	//     while the box is already up, since a focused pane takes `/` itself.
	//     Keys go to the box, because the box is what is on screen.
	//   - A modal, which OUTRANKS the box: a spawn failure arriving
	//     underneath takes the keyboard until it is dismissed, and dismissing
	//     it hands the keyboard back here.
	jump     jumpDialog
	jumpOpen bool

	// inbox is the inbox overlay's state (inboxoverlay.go); inboxOpen is the
	// "is it open" flag, beside jumpOpen for the reason it sits beside
	// formOpen: an overlay coexists with a modal, so it cannot
	// be folded into the single-valued modal field. The
	// struct is not zeroed on close: its zero value has an unbuilt input and
	// viewport, and keeping the last-built one makes a stray render harmless.
	inbox     inboxOverlay
	inboxOpen bool

	// planCheck is the check-the-plan overlay (plancheckoverlay.go) and
	// planCheckOpen its "is it open" flag, beside inboxOpen for the same
	// reason. The struct holds the status copied at open and is not zeroed
	// on close, so a stray render draws the last box rather than panicking
	// on an unbuilt viewport.
	planCheck     planCheckOverlay
	planCheckOpen bool

	// taskTokensView is the tokens overlay (tasktokensoverlay.go) and
	// taskTokensOpen its "is it open" flag, beside planCheckOpen for the same
	// reason; the struct is not zeroed on close, for the same reason either.
	taskTokensView taskTokensOverlay
	taskTokensOpen bool

	// detailsOpen is the details overlay's flag (detailsoverlay.go, §4.4),
	// beside the other overlays' for their reason. detailsFrom is the level
	// it was opened from and detailsFor the session it was opened for. They
	// are recorded at open because the overlay moves no focus: the level
	// under it is the one it was opened on, and closeDetailsIfGone needs the
	// name to tell when the session left the list.
	detailsOpen bool
	detailsFrom level
	detailsFor  string
	// detailsRestartFor is the session `r` in the overlay restarted
	// (detailsRestart), so the pane that restart brings can close the
	// overlay and open the Agent (applyPaneAttached). closeDetails clears
	// it: an owner who closed the overlay has moved away from it.
	detailsRestartFor string

	// leader is the `:` actions search (leader.go); leaderOpen is its "is
	// it open" flag, beside the other overlays' for their reason. leaderFrom
	// is the mode it was opened in and leaderLevel the level, and
	// leaderFromPlaceholder says the right side was showing the agent
	// placeholder. All three are recorded at open because the search's own
	// mode outranks the surfaces it was opened from, so they cannot be
	// derived again when a row is run (runLeaderRow).
	leader                leaderDialog
	leaderOpen            bool
	leaderFrom            inputMode
	leaderLevel           level
	leaderFromPlaceholder bool

	// keyStripOpen is the key strip's flag (keystrip.go, §4.8): the leader
	// pressed in the Agent, waiting for the afk key that follows. It is an
	// overlay's flag like leaderOpen, not a stored level: the level stays
	// derived (currentLevel), and inputKeyStrip's predicate reads the Agent's
	// facts beside the flag, so a flag left set by a path that moved the
	// focus cannot take the keyboard anywhere else.
	keyStripOpen bool

	// leaderKey is the leader key this dashboard uses, decided once in New
	// by checkLeader from Deps.Leader: its name is what every hint and help
	// line name and decideKey matches, its key is what the pass-through sends,
	// and a non-empty refused puts the leader line in the header. It never
	// changes while the dashboard runs.
	leaderKey leaderSetting

	// themeSetting is the theme this dashboard draws with, decided once in
	// New by checkTheme from Deps.Theme. The package's theme variable is
	// always built from themeSetting.theme; the background answer replaces it
	// only when it is not pinned (onBackground).
	themeSetting themeSetting

	// keysView is the keys overlay (keysoverlay.go) and keysOpen its "is it
	// open" flag, beside planCheckOpen's kind: `?` opens it at every level.
	keysView keysOverlay
	keysOpen bool

	// sidebarPct is the sidebar's share of the terminal width, as an integer
	// percentage. Zero means the default (sidebarWidthPct); layoutDims and
	// mouseFrameLayout both read it through sidebarOuterWidthFor, which clamps
	// it, so no other code does split arithmetic. New seeds it from
	// Deps.LoadSidebarPct and nothing writes it afterwards.
	sidebarPct int

	quitting bool
}

// New builds a dashboard Model for repo, refreshing every interval, with the
// given Deps (zero-value Deps gets the real implementations).
func New(repo string, interval time.Duration, deps Deps) Model {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	d := deps.withDefaults()
	leader := checkLeader(d.Leader)
	// The theme is applied before anything below captures a style (the
	// spinner takes theme.spinner by value), so a chosen theme is on screen
	// from the first frame.
	// useThemeFor also tells the parts whether the terminal can show the
	// panes' panel ground (16 colours cannot, §1).
	themeSet := checkTheme(d.Theme)
	useThemeFor(themeSet.theme, d.ColorProfile)
	keys := defaultKeyMap()
	keys.Leader = leaderBinding(leader.name)
	var remembered map[string]string
	if d.LoadSelections != nil {
		remembered = d.LoadSelections()
	}
	var opens map[string]int
	if d.LoadProjectOpens != nil {
		opens = d.LoadProjectOpens()
	}
	var sidebarPct int
	if d.LoadSidebarPct != nil {
		sidebarPct = d.LoadSidebarPct()
	}
	// Launching INTO a project is a deliberate activation — the caller resolved
	// this entry from `--project PATH` or from the cwd repo, both of which are
	// the user naming a project — so the launch target is recorded here, at the
	// one place both spellings arrive as a single resolved entry. It is
	// deliberately NOT the same act as applyScanResult's fallback promotion,
	// which activates m.projects[0] because the active project vanished and
	// records nothing (see recordProjectOpen).
	//
	// The write is the ordinary pending one: the first discovery tick or the
	// quit flush carries it to disk, so startup performs no state write of its
	// own and a dashboard opened for two seconds costs nothing.
	var dirtyOpens map[string]int
	if path := launchProjectPath(deps.Projects, deps.ActiveProjectIndex); path != "" {
		if opens == nil {
			opens = map[string]int{}
		}
		opens[path]++
		dirtyOpens = map[string]int{path: 1}
	}
	previewOnNextLoad := false
	if deps.InitialSessionName == "" {
		key := selectionKeyFor(deps.Projects, deps.ActiveProjectIndex, repo)
		if remembered[key] != "" {
			previewOnNextLoad = true
		}
	}
	m := Model{
		repo:                  repo,
		interval:              interval,
		deps:                  d,
		keys:                  keys,
		leaderKey:             leader,
		themeSetting:          themeSet,
		stackRefreshing:       new(atomic.Bool),
		buildRefreshing:       new(atomic.Bool),
		planStatusRefreshing:  new(atomic.Bool),
		taskTokensLoading:     new(atomic.Bool),
		allProjectsRefreshing: new(atomic.Bool),
		allProjectsSeq:        new(atomic.Int64),
		activityRefreshing:    new(atomic.Bool),
		list:                  newSessionList(d.Now),
		detailViewport:        viewport.New(),
		initialSession:        deps.InitialSessionName,
		gitCommonDir:          deps.GitCommonDir,
		projects:              deps.Projects,
		found:                 unpinnedProjects(deps.Projects),
		activeProject:         deps.ActiveProjectIndex,
		rememberedSelection:   remembered,
		projectOpens:          opens,
		dirtyOpens:            dirtyOpens,
		previewOnNextLoad:     previewOnNextLoad,
		scanInterval:          defaultScanInterval,
		pid:                   d.Pid(),
		focusWatch:            d.StartFocusWatcher(deps.GitCommonDir),
		dashEntry: dashEntryMeta{
			tmux:      deps.Tmux,
			paneID:    deps.PaneID,
			clientTTY: deps.ClientTTY,
			startedAt: deps.StartedAt,
		},
		mouseCapture: true,
		sidebarPct:   sidebarPct,
		spinner:      spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(theme.spinner)),
	}
	m.raiseStartupNotice(deps.StartupNotice)
	m.raiseThemeNotice(themeSet.notice)
	return m
}

// Init kicks off the first discovery pass immediately, starts the refresh
// ticker, kicks off the first git-spice stack refresh, arms the click-to-focus
// request subscription, publishes this dashboard's dashmeta entry, and starts the
// project scan (one immediate pass plus its own slower ticker).
//
// focusRequestWatchCmd is nil when no fsnotify watch could be created, and
// tea.Batch drops nil Cmds, so the dashboard starts identically either way — the
// difference is only whether requests arrive in milliseconds or on the next tick.
// writeDashEntryCmd is nil under the same rule when there is nowhere to publish
// to, and both halves of the rendezvous being independently absent is normal: a
// dashboard outside tmux writes no entry and still watches for requests.
//
// Every Cmd here belongs to the TOP-LEVEL batch, not to a nested one. tea.Batch
// flattens exactly one level for a receiver that inspects tea.BatchMsg, so a Cmd
// tucked into a sub-batch is opaque to the tests that assert startup dispatched
// it.
func (m Model) Init() tea.Cmd {
	cmds := append(m.projectCmds(), m.tickCmd(),
		// Ask the terminal for its background so an unset theme can pick
		// paper on a light terminal (themeSetting.onBackground); a terminal
		// that never answers leaves tide in place.
		tea.RequestBackgroundColor,
		focusRequestWatchCmd(m.focusWatch, m.gitCommonDir, m.pid),
		writeDashEntryCmd(m.deps.WriteDashEntry, m.gitCommonDir, m.dashEntry),
		// The first scan rides the batch instead of waiting out a whole scan
		// interval: the startup path hands over only the cwd-derived project (a
		// slow search root must never delay the first frame), so without this the
		// switcher would sit at one entry for a minute.
		//
		// The listed read is its own Cmd beside the scan, not a step inside it:
		// it is one small file, and the scan answers only after its whole walk.
		m.listedCmd(), m.scanCmd(), m.scanTickCmd())
	// The every-project cache is read once at start too (T16.6), as a Cmd.
	if read := m.everyProjectCmd(); read != nil {
		cmds = append(cmds, read)
	}
	return tea.Batch(cmds...)
}

// Update is the pure message handler. It never performs I/O — it mutates
// in-memory state and returns Cmds (defined in messages.go) for any side
// effect.
//
// Nothing about the status is done here. statusChangedAt is stamped by
// Model.notify (notification.go) at the moment of the write, because a
// handler issuing two notifications in one message has to see the first
// one's timestamp to decide about the second — a stamp applied out here,
// after handle has returned, is invisible to exactly the code that reads it.
// The risk of a per-site helper is that a site neglects to call it; the
// answer is that every assignment to the status field lives in
// notification.go and nowhere else, so a forgotten call is one grep away
// rather than a silent misbehaviour.
//
// Update stays split from handle because it is the one seam where work that
// has to run after every message, whatever the message was, can go — and
// background arbitration is exactly that work. flushBackground runs here so
// that everything one message produced (every transition one discovery
// snapshot revealed) is one batch, and so that a batch deferred behind a
// live toast is promoted by whichever message arrives once the toast has
// expired. The discovery tick makes sure one does; no timer is armed for it,
// so promotion can trail the TTL by up to one tick — see flushBackground.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.handle(msg)
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	nm.flushBackground()
	return nm, cmd
}

// There is deliberately no post-handle focus reconciliation here. The old
// detail-focus flag needed one (a pane could arrive under it and leave the
// keyboard on a surface no longer drawn); focus and view cannot express that
// state, because which right surface owns the keyboard is derived from them
// and from what is drawn at the moment of each keypress (modeSnapshot).

// handle is Update's body: the message switch itself.
func (m Model) handle(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		// The terminal answered RequestBackgroundColor. An unset theme
		// follows it (tide or paper); a theme the owner chose stays. The
		// shared style set is rebuilt either way, from the one setting, and
		// the spinner captured its style at New, so it is re-pointed here.
		m.themeSetting = m.themeSetting.onBackground(msg.IsDark())
		useThemeFor(m.themeSetting.theme, m.deps.ColorProfile)
		m.spinner.Style = theme.spinner
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.relayout()
		// The first WindowSizeMsg is one of the two arrivals maybeOpenInitialPane
		// waits on (the other is the first discovery load) — see its doc comment.
		return m.maybeOpenInitialPane()

	case tea.PasteMsg:
		// A bracketed paste is its own message type in v2 (v1 delivered it as a
		// KeyMsg with Paste set), so it never reaches the key dispatch. Only a
		// focused pane has anywhere to put it: everything else on this screen is
		// a single-keystroke surface, and the form's own fields are driven by
		// bubbles components that never saw v1's paste flag either.
		//
		// The test is the DERIVED mode, not the raw pane flags, and that is the
		// whole point of asking it here: a focused pane that has been outranked
		// (a form or an overlay drawn over it, a modal up) is not on screen, and
		// pasting into a child the user cannot see is the same defect for a
		// paste as for a keystroke. Same invariant, different message path.
		switch activeInputMode(m.modeSnapshot()) {
		case inputPane:
			forwardPasteToPane(m.pane, msg.Content)
		case inputInbox:
			// The inbox overlay's answer field is the one other place a paste
			// belongs: an item can ask for a token or a path, and those are
			// pasted, not typed. The overlay hands a non-key message to the
			// field only when an open item's field is on screen.
			cmd, _ := m.inbox.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.dispatchKey(msg)

	// tea.MouseMsg is v2's mouse INTERFACE, not v1's struct: this one case binds
	// all four of MouseClickMsg / MouseReleaseMsg / MouseWheelMsg /
	// MouseMotionMsg (bubbletea/v2 mouse.go:46-51, which documents it as the
	// catch-all). Splitting it into concrete cases would be a silent
	// downgrade — whichever kind was left out would fall through to the default
	// and never reach the session — so the kind is discriminated once, in
	// paneMouseEvent, where the emulator's matching type is chosen.
	case tea.MouseMsg:
		return m.handleMouse(msg)

	case spinner.TickMsg:
		// spinner.Model.Update always returns a fresh Tick Cmd on a TickMsg it
		// accepts, which is what makes the loop self-perpetuating once
		// started (startSpinnerIfNeeded) — so THIS is also the only place the
		// loop can stop. Once nothing is left in flight, the message is
		// simply not forwarded: no new Tick Cmd is produced, the chain dies,
		// and spinnerActive is cleared so the next transition into
		// hasInFlightWork() starts a fresh one rather than finding the flag
		// already set with no loop actually running.
		if !m.hasInFlightWork() {
			m.spinnerActive = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tickMsg:
		// Re-run discovery, the stack refresh, the plain-branch refresh, the
		// click-to-focus fallback check, and schedule the next tick. All run in
		// Cmds (off the UI thread), so the ticker never blocks the UI.
		//
		// The focus check rides THIS ticker rather than a faster one of its own:
		// fsnotify is the fast path, and a dedicated timer for the degraded case
		// would spend a syscall every few hundred milliseconds to shave latency
		// off a path that only runs when the fast one is unavailable.
		//
		// No project scan rides this ticker, and that is not an omission: the walk
		// has its own timer (scanTickMsg) two orders of magnitude slower, because a
		// search-root crawl every 2s would cost real disk for a list that changes
		// when someone creates a repo.
		//
		// Every project's rendezvous is swept, not just the active one: a click on
		// a notification from another project has to be honored too, and this is
		// the whole mechanism when no watch could be created.
		cmds := append(m.projectCmds(), m.tickCmd())
		cmds = append(cmds, m.focusRequestPollCmds()...)
		// The all-projects screen is read again on this tick while it is open,
		// and only then: allProjectsCmd is nil while it is closed.
		if all := m.allProjectsCmd(); all != nil {
			cmds = append(cmds, all)
		}
		// The spinner's tick loop is armed HERE, not at any of the dispatch
		// sites that add an in-flight operation — see startSpinnerIfNeeded's
		// doc comment for why this is the one safe place. hasInFlightWork is
		// what keeps this ticker (which runs every m.interval regardless)
		// from arming a spinner loop nothing needs: the extra Cmd is added
		// only while something actually is in flight, and startSpinnerIfNeeded
		// itself is the guard against re-arming a loop already running.
		if m.hasInFlightWork() {
			if spin := m.startSpinnerIfNeeded(); spin != nil {
				cmds = append(cmds, spin)
			}
		}
		// This ticker is also the selection-memory debounce: schedulePreview
		// records into m.dirtySelections on every cursor move and writes nothing,
		// so a user holding `j` costs no disk at all and the whole pending set
		// lands in one write here. ONE Cmd for the set, never one per entry —
		// flushSelectionsCmd's doc explains why two concurrent writers lose an
		// entry between them.
		//
		// The set is cleared whether or not the seam exists. With no
		// RememberSelection wired (this package's own tests, and any caller that
		// builds Deps without it) nothing will ever flush it, so keeping the
		// entries would only grow a map for the life of the process.
		if len(m.dirtySelections) > 0 {
			if m.deps.RememberSelection != nil {
				cmds = append(cmds, flushSelectionsCmd(m.deps.RememberSelection, m.dirtySelections))
			}
			m.dirtySelections = nil
		}
		// The open counts ride the same debounce, for the same reason and with
		// the same clear-even-with-no-seam rule. They go out as their OWN Cmd
		// rather than being folded into the selection flush: the two seams have
		// different signatures and the count write has to happen even for a
		// project whose selection never changed. The two Cmds do land on the
		// same state file from two goroutines, which is why the production
		// writers (dash.go) take one shared mutex over it — a detail this
		// package cannot enforce and deliberately does not try to.
		if len(m.dirtyOpens) > 0 {
			if m.deps.RecordProjectOpen != nil {
				cmds = append(cmds, flushProjectOpensCmd(m.deps.RecordProjectOpen, m.dirtyOpens))
			}
			m.dirtyOpens = nil
		}
		return m, tea.Batch(cmds...)

	case scanTickMsg:
		// The listed read, the scan and the re-arm, and nothing else: this ticker
		// exists to be independent of the discovery tick above. Reading the
		// project list here is what makes a project added with `afk project add`
		// appear without a restart.
		cmds := []tea.Cmd{m.listedCmd(), m.scanCmd(), m.scanTickCmd()}
		// The every-project cache is refreshed on this slow tick while the
		// screen is closed; while it is open the 2s tick already reads.
		if !m.allProjectsOpen {
			if read := m.everyProjectCmd(); read != nil {
				cmds = append(cmds, read)
			}
		}
		// The home screen's activity is read again on this tick and on no
		// other (Deps.LoadActivity), from the rows already held: this tick's
		// own rows read lands later and starts a read only if the set of
		// projects changed.
		if roots := m.activityRootsNow(); len(roots) > 0 {
			if read := m.activityReadCmd(roots); read != nil {
				cmds = append(cmds, read)
			}
		}
		return m, tea.Batch(cmds...)

	case projectScanMsg:
		return m.applyProjectScan(msg)

	case listedProjectsMsg:
		return m.applyListedProjects(msg)

	case previewTimerMsg:
		return m.applyPreviewTimer(msg)

	case selectionFlushedMsg:
		// Dropped, error and all. Persisting the selection is convenience state
		// (the dashboard reopening on the row the user left), and the write
		// happens every 2s — surfacing a failure would turn an unwritable state
		// directory into a status line that can never be cleared, on a ticker,
		// for a feature the user never asked about. The in-memory
		// rememberedSelection still serves the restore for the rest of the run,
		// so nothing observable is lost until the process exits.
		return m, nil

	case projectOpensFlushedMsg:
		// Dropped, error and all, on the case above's reasoning. The in-memory
		// tally still orders the switcher for the rest of the run, so a failed
		// write costs a ranking the next launch has to rebuild — nothing the
		// user can act on mid-session.
		return m, nil

	case sessionsLoadedMsg:
		// Dropped whole, before applyLoaded runs and before previewOnNextLoad is
		// even read, on the same precedent as hostRestartedMsg's and
		// applyPaneAttached's own projectGen guards: a switch bumps m.projectGen
		// synchronously, so a load dispatched under the OUTGOING generation (an
		// ordinary 2s tick still in flight, or B's load in a rapid A→B→C switch)
		// can land after the switch with a stale project's sessions attached. Applying
		// it would show the wrong project's rows under the new one; consuming
		// previewOnNextLoad off it would either arm a preview for a session not in
		// the current list or leave the INCOMING project's own load finding the flag
		// already spent, silently bringing the bug this wave fixes back. See
		// sessionsLoadedMsg.gen for where the stamp is set.
		if msg.gen != m.projectGen {
			return m, nil
		}
		m = m.applyLoaded(msg)
		m.notifyDiscoveryError()
		// The first discovery load is the other arrival maybeOpenInitialPane
		// waits on (the first tea.WindowSizeMsg is the other) — see its doc
		// comment. Set firstLoadDone unconditionally (even on a load error):
		// a whole-dashboard discovery failure still counts as "the first load
		// arrived" for gating purposes, or a startup pane request would wait
		// forever behind a persistently-failing discovery.
		m.firstLoadDone = true
		nm, cmd := m.maybeOpenInitialPane()
		m = nm.(Model)
		var previewCmd tea.Cmd
		// schedulePreview runs AFTER maybeOpenInitialPane, not just alongside it:
		// if a startup pane request was just dispatched above, its attachInFlight
		// record needs to already be on m before schedulePreview's own "only an
		// EXPLICIT open blocks arming" refusal (see its doc comment) can see it
		// and refuse to arm a competing dial for the same session.
		if m.previewOnNextLoad && msg.err == nil && m.ready {
			// Consume the flag on the first SUCCESSFUL load only: a failed load
			// (msg.err != nil) leaves it set so the retry that eventually succeeds
			// is the one that arms, rather than the switch being "used up" by a
			// load that produced nothing to preview.
			//
			// m.ready is the same kind of deferral, for New's startup arm: a load
			// can land before the first tea.WindowSizeMsg, and attachPaneCmd
			// captures paneRegionSize() at DISPATCH time (messages.go), so a
			// preview dialed at width 0 hands the emulator garbage geometry —
			// which is why maybeOpenInitialPane above refuses until m.ready too.
			// Leaving the flag set (exactly as the error case does) lets the next
			// tick's load, by then past the first resize, consume it. The guard
			// is on the flag rather than on which path armed it because nothing
			// makes an early arm impossible: the jump search opens without
			// waiting for m.ready. A jump switch before the
			// first resize is merely unlikely, and deferring it one load is the
			// same right answer there.
			m.previewOnNextLoad = false
			// Restore the row the user was last on in THIS project, by name —
			// applyLoaded has just re-clamped the selection to row 0 for a switch
			// (it preserves an index, and the switch cleared m.sessions).
			//
			// It lives inside this block and nowhere else because the block is
			// already the exact set of loads a restore is wanted for, with no
			// second flag to keep in sync: previewOnNextLoad is set by the two
			// sanctioned paths only (the switcher's own switcherChosen case and
			// New's startup arm), switchProjectCmds clears it for every fresh
			// switch, openPaneFor clears it for a non-preview explicit open (the
			// focus-request path), and applyScanResult's fallback promotion never
			// sets it at all. Restoring here therefore inherits every exclusion
			// the feature asked for. The ordinary 2s tick path is deliberately
			// untouched: applyLoaded's own index preservation is what should hold
			// there, and a restore on every load would drag the cursor back off
			// whatever row the user has since moved to.
			//
			// A name that is absent — a session cleaned since the last run, or the
			// placeholder name openScratch guessed before applyScratchDirs
			// corrected it — falls back to applyLoaded's row 0 rather than
			// reporting anything: nothing about a stale convenience entry is worth
			// a notification. sessionIndex skips worktree-fault rows, so a fault
			// row bearing the remembered name falls back the same way (it could
			// never satisfy the pane open the preview is about to attempt).
			if name := m.rememberedSelection[m.selectionKey()]; name != "" {
				if i, ok := m.sessionIndex(name); ok {
					m.selectSessionIndex(i)
				}
			}
			// Previews whatever is now selected — the restored row, or row 0 — and
			// re-records that name through schedulePreview's own funnel, which
			// makes the restore idempotent across runs.
			previewCmd = m.schedulePreview()
		}
		// The other one-shot arm: a click-to-focus request that switched projects
		// left the NAME it wants selected here, because at request time the switch
		// had just emptied m.sessions (see selectOnNextLoad and applyFocusRequest).
		// Consumed on the first SUCCESSFUL load for the same reason the preview arm
		// is — a failed load produced no rows to select from, so leaving it set
		// hands the arm to the retry that eventually succeeds rather than spending
		// it on nothing. It needs no m.ready guard of its own: unlike a preview
		// this moves a cursor and dials nothing, so there is no geometry to be
		// captured too early. The stale-generation drop at the top of the case
		// protects it too — an outgoing project's load must not consume an arm
		// belonging to the incoming one.
		//
		// This arms NO preview. The click already dispatched its own explicit open
		// for this very session, and a preview would race a second dial against
		// it — the whole reason this is a separate field from previewOnNextLoad.
		if m.selectOnNextLoad != "" && msg.err == nil {
			name := m.selectOnNextLoad
			m.selectOnNextLoad = ""
			// An absent name falls back to applyLoaded's row 0 and records
			// NOTHING, which is the difference that matters: recording row 0 here
			// would overwrite this project's remembered selection with a row the
			// user never chose, as the price of a click whose target discovery has
			// not caught up with (or whose session was cleaned meanwhile).
			if i, ok := m.sessionIndex(name); ok {
				m.selectSessionIndex(i)
				m.resetDetailScroll()
				m.rememberSelectedRow()
			}
		}
		// A plan action's open waiting for the master-plan row (see
		// planSelectPending). Successful loads only, on selectOnNextLoad's
		// reasoning: a failed load has no rows to select from.
		if msg.err == nil {
			m.applyPlanSelectPending()
		}
		return m, tea.Batch(cmd, previewCmd)

	case stackLoadedMsg:
		m.stack = msg.stack
		m.notifyStackError()
		return m, nil

	case buildLoadedMsg:
		// A read can land the cursor on another task (the first read, or its
		// task gone from the plan), and that task's tokens are read at once
		// rather than at the next tick.
		prevTask := m.buildCursorID
		m = m.applyBuildLoaded(msg)
		if m.buildCursorID != prevTask {
			return m, m.taskTokensCmd()
		}
		return m, nil

	case taskTokensLoadedMsg:
		return m.applyTaskTokensLoaded(msg)

	case planStatusLoadedMsg:
		return m.applyPlanStatusLoaded(msg), nil

	case inboxAnsweredMsg:
		return m.applyInboxAnswered(msg)

	case buildPausedMsg:
		return m.applyBuildPaused(msg)

	case buildResumedMsg:
		return m.applyBuildResumed(msg)

	case allProjectsLoadedMsg:
		m = m.applyAllProjectsLoaded(msg)
		return m, m.activityOnNewRoots()

	case activityLoadedMsg:
		return m.applyActivityLoaded(msg), nil

	case allProjectsPausedMsg:
		return m.applyAllProjectsPaused(msg)

	case allProjectsResumedMsg:
		return m.applyAllProjectsResumed(msg)

	case planStartedMsg:
		return m.applyPlanStarted(msg)

	case planFolderDecidedMsg:
		return m.applyPlanFolderDecided(msg)

	case planFolderCreatedMsg:
		return m.applyPlanFolderCreated(msg)

	case githubOfferMsg:
		return m.applyGitHubOffer(msg)

	case githubRepoCreatedMsg:
		return m.applyGitHubRepoCreated(msg)

	case planApprovedMsg:
		return m.applyPlanApproved(msg)

	case projectAddedMsg:
		return m.applyProjectAdded(msg)

	case projectRemovedMsg:
		return m.applyProjectRemoved(msg)

	case taskStatusSetMsg:
		return m.applyTaskStatusSet(msg)

	case usageRefreshedMsg:
		return m.applyUsageRefreshed(msg)

	case plainBranchesLoadedMsg:
		// Keep the branches even when trunk resolution failed: both come back in
		// one message, and a repo whose trunk is unresolvable (no origin/HEAD, no
		// main or master, detached HEAD) still has branches worth offering.
		// Gating both on err emptied the form in exactly that case.
		if msg.branches != nil {
			m.plainBranches = msg.branches
		}
		if msg.err == nil {
			m.plainTrunk = msg.trunk
		}
		m.notifyBranchesError(msg)
		return m, nil

	case hostRestartedMsg:
		// Balances the noteOtherOpStarted call at every restartHostCmd dispatch site,
		// unconditionally and before any branch below: every outcome this case
		// can take — including the stale-generation drop two lines down —
		// reports back exactly once through this message.
		m.endOtherOp()
		joinedFocus := false
		if m.restartInFlight.name == msg.name && m.restartInFlight.gen == msg.gen {
			joinedFocus = m.restartInFlight.focusOnArrival
			m.restartInFlight = inFlightRestart{}
		}
		focusOnArrival := msg.focusOnArrival || joinedFocus
		// A restart that crossed a project switch. The respawn cannot be
		// un-dispatched — the OUTGOING project's host is up again, which is
		// harmless — but everything below is stale: the failure status and the
		// success status both describe a session the current list does not show,
		// and chaining into openPaneFor would restamp the rebuilt request with
		// the CURRENT generation, laundering this pre-switch dial past
		// applyPaneAttached's guard (see projectGen). Dropped whole instead.
		if msg.gen != m.projectGen {
			return m, nil
		}
		// A restart the user focused (connect moved the focus right at request
		// time) that ends here without a pane hands the keyboard back, on the
		// rule applyPaneAttached's failure branch follows.
		if msg.err != nil {
			if focusOnArrival {
				m.handBackUnansweredFocus()
			}
			m.notify(notifyError, restartFailedStatus(msg.name, msg.worktree, msg.err))
			return m, m.loadCmd()
		}
		// A clean confirmed while the respawn was in flight. The host is already
		// back up by the time this lands — the spawn cannot be un-dispatched — but
		// the clean stops it again on its way through, so the only thing to get
		// right here is not claiming otherwise and not opening a pane onto it.
		if m.cleaningByName(msg.name) {
			if focusOnArrival {
				m.handBackUnansweredFocus()
			}
			m.notify(notifyWarn, "can't open "+msg.name+" — it's being cleaned")
			return m, m.loadCmd()
		}
		// restartHostCmd reports success only after the new host has published a
		// readable host record. Chain straight into the pane open; host.Dial's
		// retry window handles the remaining socket-listen interval.
		m.notify(notifySuccess, "restarted "+msg.name+" — connecting…")
		if cmd := m.openPaneFor(paneOpenRequest{
			name:           msg.name,
			worktree:       msg.worktree,
			restarted:      true,
			focusOnArrival: focusOnArrival,
			selectionBound: msg.selectionBound,
			focusRequest:   msg.focusRequest,
		}); cmd != nil {
			return m, cmd
		}
		if focusOnArrival {
			m.handBackUnansweredFocus()
		}
		return m, m.loadCmd()

	case scratchDirsListedMsg:
		return m.applyScratchDirs(msg)

	case spawnFinishedMsg:
		// The placeholder goes either way: on success discovery now has the
		// real session, on failure there is nothing to be pending about.
		m.clearPendingSpawn(msg.name)
		if msg.err != nil {
			m.modal = inputSpawnError
			m.spawnErrName = msg.name
			m.spawnErrHeadline = spawnFailedHeadline(msg.name, msg.err)
			m.spawnErrTail = spawnOutputTail(msg.output)
			// The modal IS the report, and it carries the child's output as
			// well as the headline. A notification would toast the same
			// failure a second time, in the corner, over the dialog saying it.
			m.clearStatus()
			return m, m.loadCmd()
		}
		// Every degradation the session-creation base-branch tracking can
		// take (an untracked base left as plain git, a `gs branch track`
		// that failed) is a best-effort "warning:" line on an otherwise
		// exit-0 run (Plan.Run's bestEffort branch) — CombinedOutput's error is nil, so
		// without this the dashboard user who picked an untracked base sees
		// a clean success with the branch silently left untracked.
		warnings := spawnWarnings(msg.output)
		if m.pane != nil {
			// Another session is already in the pane. Creating a session no
			// longer implies watching it — the host runs regardless — so say
			// where it went rather than displacing what the user is looking at.
			m.notify(notifySuccess, appendWarningSuffix("created "+msg.name+" — running in the background", warnings)+" · "+m.keys.Focus.Help().Key+" to open the agent")
			return m, m.loadCmd()
		}
		m.notify(notifySuccess, appendWarningSuffix("created "+msg.name+" — connecting…", warnings))
		// Reload AND connect: the reload picks the real session up in the list,
		// the pane renders it. No wait between them — the dial's retry window
		// absorbs the host's startup.
		open := m.openPaneFor(paneOpenRequest{
			name:     msg.name,
			worktree: m.worktreeFor(msg.name),
		})
		if open == nil {
			return m, m.loadCmd()
		}
		return m, tea.Batch(m.loadCmd(), open)

	case labelSavedMsg:
		// Balances the noteOtherOpStarted call at writeLabelCmd's dispatch site.
		m.endOtherOp()
		// A discovery refresh, on success and on failure alike: on success it
		// is what makes the new label appear (or the file's removal revert the
		// row to the real name); on failure the row is untouched, but a stale
		// list is still worth re-confirming rather than trusting the pre-write
		// snapshot.
		if msg.err != nil {
			m.notify(notifyError, "label save failed for "+msg.name+": "+msg.err.Error())
			return m, m.loadCmd()
		}
		m.notify(notifySuccess, "label saved for "+msg.name)
		return m, m.loadCmd()

	case cleanMergeStateMsg:
		return m.applyCleanMergeState(msg)

	case cleanFinishedMsg:
		// The in-flight entry goes either way: on success the session is gone, on
		// failure it is untouched and retryable. Leaving it set on the error path
		// would lock the row out of `c`, enter and `r` for the rest of the run.
		m.clearCleaning(cleanID{name: msg.name, kind: msg.kind})
		if msg.err != nil {
			m.notify(notifyError, cleanFailedStatus(msg.name, msg.result, msg.err))
		} else {
			m.notify(notifySuccess, cleanSucceededStatus(msg.result))
		}
		// Refresh immediately: this is what makes the cleaned row disappear.
		return m, m.loadCmd()

	case restartChildRequestedMsg:
		// Balances the noteOtherOpStarted call at restartChildCmd's dispatch site
		// (confirmRestart), before the outcome switch below so every branch
		// of it balances the count the same way.
		m.endOtherOp()
		switch {
		case msg.err != nil:
			m.notify(notifyError, "restart failed for "+msg.name+": "+msg.err.Error())
		case msg.restarted:
			// The restarted child is whichever provider the session runs; an
			// empty Provider (or a row that vanished) is the default, claude.
			agent := provider.Default
			if s, ok := m.sessionByName(msg.name); ok && s.Provider != "" {
				agent = s.Provider
			}
			m.notify(notifySuccess, "restarted "+msg.name+" — "+agent+" is resuming its conversation · "+m.agentKeyHint()+" to reopen the agent")
		default:
			m.notify(notifyWarn, "restart requested for "+msg.name+" — the host reported no outcome · "+m.agentKeyHint()+" to reconnect the agent")
		}
		// Refresh immediately on every outcome: on success the restart itself
		// cleared wedged_since in host.json (restart.go's noteRestart), and on
		// failure the row must honestly keep its WEDGED badge next to the
		// reason now sitting in the toast.
		return m, m.loadCmd()

	case focusRequestMsg:
		return m.applyFocusRequest(msg)

	case paneAttachedMsg:
		return m.applyPaneAttached(msg)

	case paneRedrawMsg:
		if msg.pane != m.pane {
			// A late notification from a pane we already closed: ignore it and
			// do NOT re-arm (that pane is gone).
			return m, nil
		}
		// Returning from Update re-renders View (painting the new screen); we
		// only need to re-arm the subscription so the next notification wakes
		// us again. termpane's Changed channel coalesces, so one redraw message
		// stands in for a whole burst of session output — see learnings.md.
		return m, paneEventCmd(m.pane)

	case paneExitedMsg:
		if msg.pane != m.pane {
			return m, nil
		}
		// The connection ended. That is either the session finishing or another
		// client stealing it; both mean this pane is done. Close is idempotent
		// (Done already fired) and detaches rather than terminating, so a steal
		// cannot take the session down with it.
		ended := m.paneName
		wasFocused := m.paneOwnsKeyboard()
		m.forgetPane()
		// A connection lost under a live session keeps the keyboard on the
		// right, in the agent view's reconnect placeholder: handing it to the
		// list mid-typing would deliver the next `q` to the quit binding.
		if lost, ok := lostPaneMessage(ended, msg); ok {
			m.paneLost = lostPane{name: ended, message: lost}
			// Only a pane that owned the keyboard forces the agent view: one
			// lost behind a focused overview leaves that overview drawn and
			// focused, and ctrl+t then shows the reconnect placeholder.
			if wasFocused {
				m.focus = focusRight
				m.view = viewAgent
			}
		}
		switch {
		case msg.err != nil:
			m.notify(notifyError, "disconnected from "+ended+": "+msg.err.Error())
		case msg.reason == host.DetachStolen:
			// A steal is not a death. The session is still running under
			// whichever client took it (a `afk attach` elsewhere, a sibling
			// dashboard's preview), so the row must keep reading as running:
			// markHostStopped here would pin a LIVE host's PID into
			// deadHostPIDs and hold the row at "stopped" until that PID
			// changed — which for a live host is indefinitely.
			m.notify(notifyInfo, takenOverMessage(ended))
		case msg.reason == host.DetachDropped:
			// Not a death either, and NOT markHostStopped, for the same
			// reason as a steal: the host closed this connection without an
			// Exit frame, which it does when it drops a client that fell
			// behind its output and keeps serving. Pinning that live PID in
			// deadHostPIDs is what turned every later connect into a restart
			// whose second `afk _host` stole the freshly opened pane via
			// its bind probe. Whether the host is in fact gone (the other
			// way a bare close happens) is left to discovery's signal-0.
			m.notify(notifyWarn, "connection to "+ended+" dropped — it may still be running · "+m.agentKeyHint()+" to reopen the agent")
		default:
			m.notify(notifyInfo, "session ended: "+ended)
			// The host exits with its child, so the next discovery pass
			// reads this row's HostRunning true→false with no terminal phase
			// to explain it (a scratch session never has one) and the
			// snapshot diff would add "x host died" to the "session ended"
			// just issued — one event told twice. markHostStopped flips the
			// row in place, so there is no transition left for the diff to
			// find (reportsHostDeath). A zero reason lands here too: a pane
			// that cannot say why it ended is treated as a real exit, the
			// direction that keeps a dead host from reading as alive.
			m.markHostStopped(ended)
		}
		return m, m.loadCmd()

	default:
		// Everything with no case of its own — the components' own blink and
		// spinner traffic — goes to whichever surface OWNS INPUT, derived
		// exactly as a keypress is. Keying this off the raw formOpen flag (as it
		// once did) split mode ownership in half: a form outranked by a modal
		// would keep feeding its cursor while the modal held the keyboard.
		//
		// Only a few modes have a component that takes an arbitrary tea.Msg
		// since T16.2 removed the session list's filter prompt. The jump
		// search is not one of them: its query is plain state with no
		// component behind it (jump.go), so there is nothing to route here.
		mode := activeInputMode(m.modeSnapshot())
		// A list.FilterMatchesMsg names no sender, and two lists in this
		// package can ask for one: the sidebar's and the creation form's base
		// picker. The form's list owns it only while the form owns input;
		// every other mode's belongs to the sidebar,
		// whose own result can land after Enter has already returned the mode to
		// normal or after a modal opened over it. Applying the wrong one puts
		// another list's items into this one — the sidebar goes blank and
		// selected() answers false — so ownership is decided here rather than by
		// a bare type case.
		//
		// A new match set changes which session is selected with no key having
		// moved, so it invalidates a pending preview like any other selection
		// change.
		if matches, ok := msg.(list.FilterMatchesMsg); ok {
			switch mode {
			case inputForm:
			default:
				var cmd tea.Cmd
				m.list, cmd = m.list.Update(matches)
				return m, tea.Batch(cmd, m.schedulePreview())
			}
		}
		switch mode {
		case inputForm:
			var cmd tea.Cmd
			// The checkbox and the provider selector have no component, so
			// they have no case: there is nothing for blink or filter
			// traffic to reach.
			switch m.form.focusIndex {
			case formFocusName:
				m.form.nameInput, cmd = m.form.nameInput.Update(msg)
			case formFocusDescription:
				m.form.description, cmd = m.form.description.Update(msg)
			case formFocusBase:
				m.form.baseList, cmd = m.form.baseList.Update(msg)
			}
			return m, cmd
		case inputEditLabel:
			var cmd tea.Cmd
			m.labelInput, cmd = m.labelInput.Update(msg)
			return m, cmd
		case inputPlanText:
			var cmd tea.Cmd
			m.planTextInput, cmd = m.planTextInput.Update(msg)
			return m, cmd
		case inputInbox:
			// The answer field's cursor blink. The overlay reports no result
			// for a message that is not a keypress.
			cmd, _ := m.inbox.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

// applyPaneAttached folds the outcome of a pane-open attempt into the Model.
//
// A failure that wraps host.ErrHostNotReachable is the false-positive
// correction the dashboard needs: SessionState.HostRunning is advisory
// (host.json plus a signal-0 liveness check, which a recycled PID or a stale
// host.json can both fool), so a session the list showed as RUNNING can still
// have nothing answering on its socket. That combination is proof the flag
// lied, and the answer is to show the session as stopped and offer the restart,
// never to surface a dead-end error.
//
// None of that correction applies to a PREVIEW, which is why its failure branch
// comes first and does nothing at all — see the comment on it.
func (m Model) applyPaneAttached(msg paneAttachedMsg) (tea.Model, tea.Cmd) {
	// The project-generation guard comes before EVERYTHING, including the
	// attachInFlight clear below: a dial dispatched before a project switch
	// reports back here for a session in the OUTGOING project (see projectGen).
	// Installing it would put a pane on screen for a session not in m.sessions;
	// letting it reach the same-name surplus branch below (when the NEW project
	// has a same-named session already installed) would close the new connection
	// and keep the old one; letting a stale FAILURE through would write status —
	// and worse, markHostStopped — against the new project's same-named row. And
	// it must not clear attachInFlight either: switchProjectCmds already cleared
	// the outgoing record when it bumped the generation, so any record present
	// now belongs to a dial the INCOMING project owns — possibly for the very
	// same name — and a stale arrival clearing it (or stealing its joined focus)
	// reopens the double-dial window the record exists to close.
	//
	// The connection is closed, not leaked: it holds the old host's single
	// client slot, and this message carries the only handle to it.
	if msg.gen != m.projectGen {
		if msg.pane != nil {
			_ = msg.pane.Close()
		}
		return m, nil
	}
	// FIRST statement past the generation guard, before any other branching,
	// because this method has five exits
	// (error, duplicate handle, cleaning, same-name surplus, install) and Update's
	// paneAttachedMsg case is the single funnel into all of them. A clear scattered
	// across the exits leaves one path able to miss it, and a missed clear leaves
	// attachInFlight set for the life of the process — every future preview
	// silently dead.
	//
	// Matched on the name because two rapid explicit opens (a connect to A,
	// then one to B before A lands) leave the record naming "B"; an
	// unconditional clear would let A's arrival clear it while B is still
	// dialing, reopening exactly the window
	// this guard exists to close. One residual is accepted rather than engineered
	// around: if the program quits before a Cmd's result is delivered the record stays
	// set, which is harmless — the Model dies with it.
	//
	// Matched on preview TOO, and that is the half that is easy to get wrong: a
	// stale preview must never release a different explicit dial's claim.
	matchesInFlight := msg.name == m.attachInFlight.name && msg.preview == m.attachInFlight.preview
	claimedPreview := matchesInFlight && msg.preview && m.attachInFlight.claimedPreview
	// A dispatched preview can become stale after its Cmd has started: an
	// explicit open and detail focus both bump previewGen, and a passive
	// preview has no business replacing an overview the user is reading.
	// Drop either loser here, closing its host client, before it can replace
	// the user's chosen surface.
	//
	// A CLAIMED preview is exempt from both, unconditionally. The claim is
	// enter (or ctrl+t into the agent view) adopting this dial as the user's
	// own connect, made with the right side focused — so detail ownership is
	// the claim's normal state while it waits (right focus in agent view with
	// no pane yet IS detail ownership), not a reason to drop it, and the
	// generation was bumped by the claim itself. Gating the claim on
	// detailOwnsKeyboard would drop exactly the dial enter had claimed.
	stalePreview := msg.preview && !claimedPreview && (m.detailOwnsKeyboard() ||
		(msg.previewGen != 0 && msg.previewGen != m.previewGen))
	if stalePreview {
		if matchesInFlight {
			m.attachInFlight = inFlightOpen{}
		}
		if msg.pane != nil {
			_ = msg.pane.Close()
		}
		return m, nil
	}
	joinedFocus := false
	if matchesInFlight {
		joinedFocus = m.attachInFlight.focusOnArrival
		m.attachInFlight = inFlightOpen{}
	}
	focusOnArrival := msg.focusOnArrival || joinedFocus
	// honourFocus is whether this arrival lands the user IN the session. A
	// focused connect (connect moved the focus right at request time, for a
	// keypress and a click alike) gives way to any later focus choice the
	// user made while it dialed: switching to the overview (ctrl+t), or going
	// back to the list (ctrl+\, esc, a list click). Either is an explicit "not
	// the agent right now", and an arrival that moved the focus anyway would
	// hand the next typed keys to a session the user walked away from. The
	// pane installs unfocused instead, one enter or ctrl+t away.
	honourFocus := focusOnArrival && m.view != viewOverview && m.focus == focusRight
	passivePreview := msg.preview && !claimedPreview
	if msg.err != nil {
		// A failed PREVIEW never proves the host is dead — no host correction or
		// reload. A passive preview is completely silent; when a connect claimed
		// this short-window dial, report that it did not connect and let the
		// next enter perform a full explicit attempt.
		// All three would be wrong here, and each for its own reason:
		//
		//   - A status write puts error text on screen produced by nothing but a
		//     cursor move.
		//   - markHostStopped's justification (above) rests on the FIVE-SECOND dial
		//     window being proof the flag lied. A preview's ~300ms budget is not that
		//     proof, and the correction is not cheap to get wrong: it pins
		//     HostRunning=false in deadHostPIDs keyed on a PID a LIVE host keeps
		//     re-asserting, and the override only lapses when that PID changes (see
		//     applyDeadHostOverrides). A host merely slow to answer would read as
		//     stopped permanently, and `r` would then spawn a SECOND host for a
		//     session that already has one.
		//   - loadCmd here means browsing N stale rows fires N extra full discovery
		//     passes (git worktree list + per-session numstat) on top of the ticker.
		if msg.preview {
			if claimedPreview {
				m.handBackUnansweredFocus()
				m.notify(notifyWarn, m.withPaneLead(msg.name, "opening "+msg.name+" did not connect · "+m.keys.Focus.Help().Key+" to retry"))
			}
			return m, nil
		}
		if focusOnArrival {
			m.handBackUnansweredFocus()
		}
		m.notify(notifyError, m.withPaneLead(msg.name, paneAttachFailedStatus(m.leaderKey.name, msg)))
		if errors.Is(msg.err, host.ErrHostNotReachable) {
			m.markHostStopped(msg.name)
		}
		return m, m.loadCmd()
	}
	// A duplicate delivery of the pane already installed: nothing to do, and
	// emphatically nothing to close — closing here would detach from the session
	// currently on screen.
	if m.pane != nil && msg.pane == m.pane {
		return m, nil
	}
	// A focused connect whose row the user has since left (ctrl+\, then a
	// cursor move) is abandoned: installing it would put a pane on a row that
	// is not selected, holding the host's one client slot undrawn. Only a
	// list-focused keyboard can have left: under right focus the selection is
	// pinned, and a click may name a session not yet in the list. A
	// selection-bound open (`r` from the list) is abandoned the same way.
	if (focusOnArrival || msg.selectionBound) && m.focus == focusList {
		if s, ok := m.selected(); ok && s.Name != msg.name {
			_ = msg.pane.Close()
			return m, nil
		}
	}
	// A clean confirmed while this connection was in flight. openPaneFor gates
	// DISPATCH, which cannot see a `c`,`y` that lands afterwards, so the arriving
	// connection is dropped here instead: installing it would put a live pane on a
	// host the clean is about to SIGKILL, in a worktree it is deleting.
	if m.cleaningByName(msg.name) {
		_ = msg.pane.Close()
		if focusOnArrival {
			m.handBackUnansweredFocus()
		}
		// Silent for a PREVIEW, for the reason the error branch above is: the status
		// would be error text produced by nothing but a cursor move (the clean was
		// confirmed while this preview's dial was outstanding, so the user asked for
		// the clean and never asked for this connection). An explicit open still says
		// why it was refused — there the user IS waiting on an answer.
		if !passivePreview {
			m.notify(notifyWarn, "can't open "+msg.name+" — it's being cleaned")
		}
		return m, nil
	}
	// A second connection to the session already on screen (two opens for one
	// name raced). Keep the one installed and detach the extra, or the surplus
	// connection leaks a client slot on the host with no handle left to close it.
	//
	// The FOCUS still has to be honoured before returning. A preview's pane for X
	// can install first and a click-to-focus connection for X then arrive as the
	// surplus — and the connection
	// being surplus does not make the keypress surplus. Dropping the focus here is
	// indistinguishable to the user from the click not working.
	if m.pane != nil && msg.name == m.paneName {
		_ = msg.pane.Close()
		if honourFocus {
			// focusPane words its own notice; an armed lead goes before it
			// as before the install path's (withPaneLead).
			lead := m.takePaneLead(msg.name)
			next, cmd := m.focusPane()
			if fm, ok := next.(Model); ok && lead != "" {
				fm.notify(fm.statusKind, leadSentence(lead, fm.status))
				return fm, cmd
			}
			return next, cmd
		}
		return m, nil
	}
	previous := m.pane
	previousName := m.paneName
	// Read before the install: detailsAwaitsPane compares against the
	// overlay, which the switch below closes.
	fromDetails := !honourFocus && m.detailsAwaitsPane(msg.name)
	m.pane = msg.pane
	m.paneName = msg.name
	m.paneLost = lostPane{}
	// From the MESSAGE, never a standalone Model flag, and only on this
	// install path — which is what lets the four exits above need no cleanup.
	// This is also the earliest point a focus can be honoured: one line up,
	// m.pane was still the session being switched away from.
	switch {
	case honourFocus:
		// The user lands IN the session: a connect (key or click) whose
		// request still stands (see honourFocus).
		m.showAndFocusPane()
	case focusOnArrival:
		// A connect the user has since switched to the overview or the list
		// for: the pane fills in, and focus and view stay exactly as the user
		// left them.
	case fromDetails:
		// The restart `r` asked for in the session's details overlay, with
		// the overlay still up on that session: the owner is still waiting
		// on it, so the overlay closes and the Agent opens with the keyboard
		// on it (T16.5). Had the owner closed the overlay or moved off the
		// row meanwhile, this is the default case's quiet install instead.
		m.closeDetails()
		m.showAndFocusPane()
	default:
		// An arriving pane nobody focused — preview, post-create auto-open,
		// startup pane — lands with the keyboard on the list and leaves the
		// view alone. `view` is the user's ctrl+t choice and nothing passive
		// may rewrite it: a pane arriving under the overview installs behind
		// it, and ctrl+t reveals it. Writing view=agent here is how a cursor
		// move once yanked a chosen overview back to the agent.
		m.focus = focusList
	}
	if previous != nil {
		// The swap: detach the outgoing session (it keeps running — see
		// hostpane.go's hostChild.Close) now that its replacement is connected
		// and installed. Its redraw/exit subscription is still armed and will
		// deliver one paneExitedMsg for a pane that is no longer m.pane; Update's
		// handle check discards it.
		//
		// This runs BEFORE the focus branch below, and nothing between here and the
		// single return may skip it. A honored focus request used to return early
		// from inside that branch, jumping the close: a click that switched from an
		// already-open pane left the outgoing connection — and its client slot on
		// the old host, of which there is exactly one — held by nobody, with no
		// handle anywhere to close it.
		_ = previous.Close()
	}
	// A PREVIEW says nothing. It was not requested, so it has no standing to write
	// over whatever the user was reading — and nothing is lost by the silence: the
	// pane's own title line names the session and carries the "[enter/f to type in it]"
	// hint (renderPane), which is where that guidance is looked for anyway.
	//
	// A honored CLICK is the one open whose wording is neither of the two below: it
	// arrives already focused, so "enter/f to type in it" would advise a chord the user
	// has no need of, and "switched to" would bury the fact that the keyboard has
	// moved. It gets focusPane's own wording instead. This rides on the message
	// rather than on focusOnArrival, which cannot stand in for it: startup and
	// click paths intentionally carry different wording policies.
	//
	// The click wording is gated on honourFocus: a click whose connect the
	// user walked away from (back to the list, or to the overview) installs
	// the pane unfocused, and "input goes to X" would then be false. That
	// click falls through to "switched to" or "opened", which are true.
	//
	// Whichever wording it gets, a lead armed for this pane goes first
	// (withPaneLead): the new plan's GitHub outcome was on screen only while
	// the pane dialed, and this notice is what would replace it.
	if !passivePreview {
		var text string
		switch {
		case msg.focusRequest && honourFocus:
			text = focusRequestHonoredStatus(m.leaderKey.name, msg.name)
		case previous != nil:
			text = "switched to " + msg.name + " — " + previousName +
				" still running in the background"
		case honourFocus, fromDetails:
			text = focusRequestHonoredStatus(m.leaderKey.name, msg.name)
		default:
			text = "opened " + msg.name + " · " + m.keys.Focus.Help().Key + " to type in it, " + keyName(m.leaderKey.name, levelAgent, actionGoBack) + " to return"
		}
		m.notify(notifyInfo, m.withPaneLead(msg.name, text))
	}
	// Begin listening for redraw/exit notifications from the pane.
	return m, paneEventCmd(msg.pane)
}

// handleMouse applies the focus rules to a terminal mouse event, then either
// forwards it (routeMouse) or turns it into a focus change. The mouse follows
// the keyboard, level by level:
//
//   - The Agent: the agent box covers the Project, so every event goes to
//     routeMouse, which forwards the ones inside the agent's screen and drops
//     the rest. A click outside the box does NOT leave the Agent: the way out
//     is the leader then esc, and a stray click beside the box taking the
//     keyboard away from the agent would be worse than one that does nothing.
//   - Project with the project view drawn (the keyboard on its sections): a
//     left click on a section focuses it and selects the row clicked
//     (projectClick); a wheel notch moves the focused section's selection
//     (projectWheel). Nothing reaches a pane, and a click opens nothing:
//     enter does. Before T16.4 a click on the right side was `d`; the right
//     side is now the Build and Inbox sections, so that is gone (A12).
//   - The details overlay, over either: a wheel notch over its box scrolls
//     it (detailsWheel) and everything else is dropped, so nothing reaches
//     the agent or a section under it.
//
// The drop-early shape is deliberate: only a click needs the frame layout
// (mouseFrameLayout renders the header and footer), so motion — one event
// per cell the pointer crosses — never pays for it here.
//
// The same input-mode guard as routeMouse applies first, widened by
// inputDetail for the overlay's wheel and inputPlaceholder for the Agent's
// box. Mouse capture released (ctrl+g) silences every case, focus changes
// included: the user handed the mouse back to the terminal.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.quitting || !m.ready || m.width == 0 || m.height == 0 || !m.mouseCapture {
		return m, nil
	}
	switch activeInputMode(m.modeSnapshot()) {
	case inputDetail:
		return m.detailsWheel(msg)
	case inputNormal, inputPane, inputPlaceholder:
	default:
		return m, nil
	}

	if m.level() == levelAgent {
		m.routeMouse(msg)
		return m, nil
	}

	// The Project: the project view is what is drawn, whichever side the
	// focus was last left on (a right focus with nothing in the agent box is
	// the Project too, currentLevel's agentShown).
	//
	// The empty state stands where the project view would be when there is
	// no project; it has nothing to click.
	if !m.hasActiveProject() {
		return m, nil
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Mouse().Button == tea.MouseLeft {
		return m.projectClick(click.Mouse().X, click.Mouse().Y)
	}
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		return m.projectWheel(wheel.Mouse().Button)
	}
	return m, nil
}

// routeMouse forwards a terminal mouse event to the hosted session when it
// landed inside the pane's content area, and drops it otherwise. It mutates no
// Model state and returns no Cmd: like forwardKeyToPane, delivery is a
// synchronous in-memory call on the pane handle, so handleMouse invokes it
// directly.
//
// handleMouse only calls it with focus=right: the mouse follows the keyboard,
// so a list-focused dashboard never delivers anything to the session. (The
// forwarding itself does not consult m.focus; the gate lives in one place,
// handleMouse.)
//
// An event outside the content area is DROPPED, never clamped and never
// repurposed. Clamping would deliver a real click at a cell the user never
// touched.
func (m Model) routeMouse(msg tea.MouseMsg) {
	// The handle can already be nil while an event is in flight: paneExitedMsg
	// clears it, and a queued mouse event decoded before that message is
	// dispatched still arrives afterwards.
	if m.pane == nil {
		return
	}
	// Before the first tea.WindowSizeMsg — and while quitting — View paints no
	// pane at all (views.go:23-27), yet the geometry helpers still answer: they
	// floor every dimension at a positive number, so a zero-size Model yields a
	// plausible origin of (14, 3) for a box that is nowhere on screen and an event
	// there reaches the session. Mouse events do arrive that early, because the
	// renderer writes ?1002/?1006 as soon as the first View declares
	// MouseModeCellMotion (bubbletea/v2 cursed_renderer.go:145-152) and the first
	// WindowSizeMsg is just another message in the same queue. The condition
	// mirrors View's own; the test pins the two against the rendered frame rather
	// than against each other.
	if m.quitting || !m.ready || m.width == 0 || m.height == 0 {
		return
	}
	// The mouse is input like any other, so it asks the same question a keypress
	// does — who owns input right now (mode.go) — instead of restating the three
	// predicates that used to be checked here one by one. Only the two modes that
	// leave the pane on screen and let it keep the mouse continue past this:
	//
	//   - The creation form REMOVES the pane from the frame entirely (View
	//     renders the form in the main region instead), so every coordinate the
	//     pane geometry would produce refers to something not on screen.
	//   - The project switcher takes that same region, so the same reasoning
	//     applies — plus it owns input while it is up: a wheel notch must not
	//     scroll a hidden session, and a click must not land on the list behind
	//     the overlay.
	//   - A modal still SHOWS the pane but OWNS input. Its taller footer also
	//     shrinks the body region without a Resize (Resize fires only on
	//     tea.WindowSizeMsg), so the region and the emulator disagree while it is
	//     up.
	switch activeInputMode(m.modeSnapshot()) {
	case inputNormal, inputPane:
	default:
		return
	}
	// A pane can EXIST without being drawn. The preview feature made
	// shouldRenderPane's false branch reachable — go back to the Project with
	// the pane connected and View paints the project view instead, with
	// m.pane still non-nil — so the nil check above is no longer
	// the same question as "is the pane on screen". Without this the geometry
	// helpers answer for a box the user cannot see and the event reaches the
	// session anyway.
	//
	// It calls shouldRenderPane rather than restating its condition so the two
	// cannot drift; that is also why the test asserts against the rendered frame
	// instead of against this guard.
	if !m.shouldRenderPane() {
		return
	}
	// Not dead code, despite the released mouse mode stopping the terminal from
	// reporting. Under v2 that release is a FRAME, not a Cmd: ctrl+g flips this
	// flag, and the ?1002/?1006 reset is not written until the next render sees a
	// different View.MouseMode (bubbletea/v2 cursed_renderer.go:383-403). Every
	// event decoded before that write is already queued and would otherwise reach
	// the session after the user asked for their mouse back — a wider window than
	// v1's, not a narrower one, since renders are coalesced at frame rate. This is
	// what the field's doc comment means by "the two must not be allowed to
	// differ".
	if !m.mouseCapture {
		return
	}

	// The origin and the region come from paneScreenGeometry (views.go), which
	// View also calls to place the child's CURSOR. Sharing it is the whole point:
	// the two paths translate in opposite directions across the same boundary, so
	// any divergence would draw the cursor at a cell where a click does not land.
	// It still measures the layout exactly ONCE per event (mouseFrameLayout, one
	// header and one footer render — see mouse.go's package doc for that budget).
	originX, originY, regionCols, regionRows := m.paneScreenGeometry()

	// The bounds are the per-axis minimum of that region and the size the
	// emulator actually is; neither is trustworthy alone (see mouseBounds). This
	// stays here rather than moving into the shared helper because it is a
	// mouse-only rule: an event at a cell the emulator does not have must be
	// dropped, while the cursor path has no event to drop and is already bounded
	// by paneCursor's own checks.
	paneCols, paneRows := m.pane.Size()
	cols, rows := mouseBounds(regionCols, regionRows, paneCols, paneRows)

	ev, ok := paneMouseEvent(msg, originX, originY, cols, rows)
	if !ok {
		return
	}
	forwardMouseToPane(m.pane, ev)
}

// toggleMouseCapture implements the ctrl+g chord: it hands the mouse back to the
// host terminal, or takes it again.
//
// Flipping mouseCapture IS the whole effect, and it is not merely a routing
// flag. Under Bubble Tea v1 this had to return tea.EnableMouseCellMotion or
// tea.DisableMouse — Cmds that emit the DEC sequences — because the terminal's
// mouse mode was set once, by a tea.ProgramOption at startup. v2 deleted both
// Cmds and made the mode a field on the tea.View a model returns, so View
// (views.go) re-derives it from this flag on the next frame and the program
// emits ?1002h/?1002l itself. Verified against v2.0.9 by running a program that
// toggles View.MouseMode: each change puts the enable/disable pair on the wire.
// That is what actually lets the host terminal see click-drag again so its
// native selection works.
//
// The Cmd being nil is therefore load-bearing INFORMATION, not an omission: the
// two callers that used to need one no longer have anything to issue.
//
// It is reached from every mode whose spec declares the chord — normal, pane,
// the form and all four modals — through one actionMouseToggle case in
// applyKeyAction. The modal and form modes are why this must not touch anything
// except mouseCapture and status:
// each is entered while a modal or the form holds state the chord has no business
// disturbing, and a second copy of the flip is how one of them would eventually
// grow a side effect the others do not have.
//
// Both messages name the chord, because either state is one a user can get
// stuck in and the chord is the way out of both. They are short for the
// toast's sake, not the footer's: the footer no longer prints a status line at
// all, and renderToast wraps to a 48-cell box and clips at 3 lines (toast.go),
// so a wording that ran long would lose its own tail — which is the hint, the
// only part that matters. The help line carries `ctrl+g mouse: term`
// independently, in the same spelling (mouseCaptureHint, views.go).
//
// Both messages are now visible under the clean confirmation and the
// spawn-error modal, which they were not before. The old footer row was
// invisible there because renderFooter hands the whole footer to a dialog;
// View composes the toast LAST, above every overlay and dialog (views.go), so
// the one state where the chord used to say nothing is gone.
func (m Model) toggleMouseCapture() (tea.Model, tea.Cmd) {
	m.mouseCapture = !m.mouseCapture
	if m.mouseCapture {
		m.notify(notifyInfo, "mouse goes to the session · "+m.keyHintFor(actionMouseToggle)+" releases it")
		return m, nil
	}
	m.notify(notifyInfo, "mouse released to the terminal · "+m.keyHintFor(actionMouseToggle)+" takes it back")
	return m, nil
}

// isCtrlChord reports whether k is ctrl+code. It exists because Bubble Tea v2
// deleted every named ctrl-chord constant this repo used (KeyCtrlBackslash,
// KeyCtrlC, KeyCtrlG, KeyCtrlJ, KeyCtrlK): a chord is now the UNMODIFIED code
// plus the ModCtrl bit, so a v1 `k.Type == <ctrl sentinel>` comparison has no
// one-symbol v2 spelling, and each site would otherwise repeat a two-clause
// test that is easy to get half right.
//
// code is the lowercase letter or literal punctuation, matching what
// ultraviolet's legacy key table decodes the C0 byte to — ctrl+c is
// {Code: 'c', Mod: ModCtrl}, never {Code: 'C'}.
func isCtrlChord(k tea.KeyPressMsg, code rune) bool {
	return k.Code == code && k.Mod.Contains(tea.ModCtrl)
}

// routeConditions evaluates the ambient facts mode.go's rule guards read. It is
// the ONE place they are computed: dispatchKey routes with it and
// leaderConditions builds on it, so the search dims a row on the same facts
// the dispatch would act on.
func (m Model) routeConditions() routeConds {
	return routeConds{
		paneOpen: m.pane != nil,
	}
}

// switchDetailTab is the three tab actions: next and previous wrap around the
// visible tabs, jump lands on the tab at the pressed digit's position among
// them (so with git-spice absent, `3` is Notes, not a hidden Stack). A digit
// past the last tab is ignored. Only detail mode routes these keys, which is
// the details overlay's, so no guard is needed here: a tab switched with the
// overlay closed is simply the tab it opens on.
//
// A switch snaps the viewport to the top before syncing: the offset belonged
// to the tab that was showing, and a Waves offset applied to a three-line
// Overview would clamp to its bottom and hide the head.
func (m Model) switchDetailTab(a modeAction, k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	tabs := visibleDetailTabs(m.stack)
	cur := slices.Index(tabs, m.activeDetailTab())
	switch a {
	case actionDetailTabNext:
		cur = (cur + 1) % len(tabs)
	case actionDetailTabPrev:
		cur = (cur + len(tabs) - 1) % len(tabs)
	case actionDetailTabJump:
		digit := []rune(k.String())
		if len(digit) != 1 || digit[0] < '1' || digit[0] > '9' {
			return m, nil
		}
		cur = int(digit[0] - '1')
		if cur >= len(tabs) {
			return m, nil
		}
	}
	if tabs[cur] == m.detailTab {
		return m, nil
	}
	m.detailTab = tabs[cur]
	m.detailViewport.GotoTop()
	m.syncDetailViewport()
	if m.detailTab == tabDiff {
		// Without this, landing on the Diff tab shows "loading diff…" until
		// the next refresh tick (--interval, default 2s). This does not
		// multiply the refresh cadence: tickCmd is re-armed in the tickMsg
		// case, not in sessionsLoadedMsg, so this extra dispatch is a single
		// one-off load, not a new recurring tick.
		return m, m.loadCmd()
	}
	return m, nil
}

// dispatchKey routes one keypress through the single mode mechanism (mode.go):
// derive who owns the keyboard from a snapshot of the overlay-activity fields,
// and hand the key to it. It is pure — navigation mutates the cursor; actions
// return a Cmd (the actual spawn/connect I/O happens in the Cmd).
//
// There is deliberately no if-ladder here, and none in the handlers below.
// Every ordering the old handleKey maintained by hand is either a row of
// modePrecedence or a rule present in one mode's spec and absent from
// another's.
//
// Since T16.2 the modes that ARE a level — the list, All projects, the agent
// pane, its placeholder and the key strip — go to levelKey, which asks the
// key table (decideKey, levels.go) what the key does at that level. The
// overlays, forms and modals keep their own rules: an open overlay gets every
// key first (§5), which is what activeInputMode ranking them above the level
// modes already means. The details overlay is one of them since T16.5.
func (m Model) dispatchKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	mode := activeInputMode(m.modeSnapshot())
	switch mode {
	case inputNormal, inputAllProjects, inputPane, inputPlaceholder, inputKeyStrip:
		return m.levelKey(mode, k)
	}
	if mode == inputDetail && key.Matches(k, m.keys.Restart) {
		// `r` in the details overlay (T16.5). It is answered here rather
		// than as one of the overlay's mode rules because whether it acts is
		// the row's (restartKeyLive), not the mode's: the rules are a static
		// table, and a rule that claimed `r` everywhere would also have the
		// overlay's help line name it on a pending or cleaning session. The
		// help line and the chip ask restartKeyLive too.
		return m.detailsRestart()
	}
	reg := m.modes()
	switch dec := reg.route(mode, m.routeConditions(), k); dec.kind {
	case routeAction:
		return m.applyKeyAction(mode, dec.action, k)
	case routeComponent:
		return m.routeKeyToComponent(mode, k)
	case routeChild:
		forwardKeyToPane(m.pane, k)
		return m, nil
	}
	// routeIgnored: the confirmations swallow unclaimed keys so a stray
	// keystroke cannot answer one.
	return m, nil
}

// levelFacts snapshots the facts currentLevel derives the level from. The
// agent pane and its placeholder are what the Agent level is (agentShown); the
// details overlay is drawn over a level and is not one.
func (m Model) levelFacts() levelFacts {
	return levelFacts{
		allProjectsOpen:  m.allProjectsOpen,
		hasActiveProject: m.hasActiveProject(),
		focus:            m.focus,
		view:             m.view,
		agentShown:       m.shouldRenderPane() || m.agentPlaceholderDrawn(),
	}
}

// level is the level the dashboard is at now (currentLevel over levelFacts).
func (m Model) level() level { return currentLevel(m.levelFacts()) }

// keyHintFor is keyName seen from the level the dashboard is at now: the key
// alone at All projects and Project, the leader then the key in the Agent.
func (m Model) keyHintFor(a modeAction) string {
	return keyName(m.leaderKey.name, m.level(), a)
}

// levelKey is one key at a level (§5): decideKey says what it does, and this
// carries it out.
//
//   - A table key runs its action alone at All projects and Project
//     (runLevelAction); enter and esc go in and out a level; ctrl+c and q
//     quit.
//   - In the Agent, with the key strip closed, every key but the leader goes
//     to the agent. The leader opens the strip; with it open the next key
//     runs an Agent key, the leader again sends one leader on, esc goes back
//     to Project, and any other key closes the strip and sends nothing. The
//     strip closes on every key, whatever it did.
//   - A move key and a key the table does not list go to the mode's own
//     rules: the list's cursor and the all-projects screen's rows. A key
//     none of them claims does nothing.
//
// The agent placeholder (no live pane yet, or a lost one) is the Agent level
// too (A6), but it has no agent to send keys to, so with the strip closed its
// own keys act alone (placeholderKey): enter connects, r restarts.
//
// A flag left set after the Agent was left by a path that is not a key (the
// agent's pane went away) is cleared here: inputKeyStrip's predicate already
// ignores it, and clearing it keeps a later return to the Agent from finding
// the strip open.
func (m Model) levelKey(mode inputMode, k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	lv := m.level()
	armed := mode == inputKeyStrip
	m.keyStripOpen = false
	placeholder := m.focus == focusRight && m.agentPlaceholderDrawn()
	if lv == levelAgent && !armed && placeholder {
		return m.placeholderKey(k)
	}
	o := decideKey(lv, overlayNone, armed, m.leaderKey.name, k.String())
	switch o.kind {
	case outcomeOpenStrip:
		m.keyStripOpen = true
		m.clearStatus()
		return m, nil
	case outcomeCloseStrip:
		return m, nil
	case outcomePassLeader:
		m.sendLeaderToPane()
		return m, nil
	case outcomeSendToAgent:
		forwardKeyToPane(m.pane, k)
		return m, nil
	case outcomeQuit:
		return m.requestQuit()
	case outcomeGoIn:
		return m.levelGoIn(lv)
	case outcomeGoBack:
		return m.levelGoBack(lv, placeholder)
	case outcomeRun:
		return m.runLevelAction(lv, mode, o.action, placeholder)
	}
	reg := m.modes()
	if dec := reg.route(mode, m.routeConditions(), k); dec.kind == routeAction {
		return m.applyKeyAction(mode, dec.action, k)
	}
	return m, nil
}

// sendLeaderToPane is the leader pressed twice in the Agent: the agent gets
// the key the dashboard took. m.leaderKey.key is the decoded form checkLeader
// built for the leader's name, so tea.KeyPressMsg(key).String() is the name
// the binding matched. It goes straight to SendKey rather than through
// forwardKeyToPane, whose specialKeyEvent table lists only the keys a person
// types into an agent and would drop a configured leader such as
// ctrl+shift+a or an F-key before the emulator saw it. x/vt's
// Emulator.SendKey (key.go) owns the encoding: ctrl+] is 0x1D, ctrl+o 0x0F,
// an alt chord gets its ESC prefix. A modified key it has no encoding for
// writes nothing, which is the same "drop rather than send garbage"
// forwardKeyToPane keeps; it cannot write a stray rune either, because x/vt's
// fallback writes the bare code only for an unmodified key and checkLeader
// refuses every leader without ctrl or alt.
func (m Model) sendLeaderToPane() {
	if m.pane != nil {
		k := m.leaderKey.key
		m.pane.SendKey(termpane.KeyEvent{Code: k.Code, Mod: k.Mod})
	}
}

// levelGoIn is enter at lv: in one level, or open what is selected (§5).
//
// At All projects it opens the selected project, as enter on the screen
// always has (switchFromAllProjects); with no project open and the screen not
// drawn (the empty state stands in for All projects there) it opens the
// screen. At Project it opens what the focused section's selection suggests
// (projectGoIn): a session's agent, a task's details or an inbox item. The
// Agent has no level below it, so enter is not a key there.
func (m Model) levelGoIn(lv level) (tea.Model, tea.Cmd) {
	switch lv {
	case levelAllProjects:
		if m.allProjectsOpen {
			return m.switchFromAllProjects()
		}
		if m.deps.LoadAllProjects == nil {
			// The empty state says what to do in its own words; a notice
			// about a missing project reader would only add noise there.
			return m, nil
		}
		return m.toggleAllProjects()
	case levelProject:
		return m.projectGoIn()
	}
	return m, nil
}

// levelGoBack is esc at lv: out one level, or close what is open (§5).
//
// At Project esc goes to All projects. In the Agent
// (the leader then esc) the keyboard comes back to Project and the pane stays
// connected while the session is selected (A5): the pane's own release, or
// the placeholder's, which each say the right thing. All projects is the top
// level, so esc there does nothing. The keys overlay `?` opens and the
// details overlay `d` opens are overlays with their own esc (keysoverlay.go,
// detailsoverlay.go), so they never reach here.
func (m Model) levelGoBack(lv level, placeholder bool) (tea.Model, tea.Cmd) {
	switch lv {
	case levelProject:
		return m.toggleAllProjects()
	case levelAgent:
		if placeholder {
			return m.releasePlaceholderFocus()
		}
		return m.releasePaneFocus()
	}
	return m, nil
}

// agentActionStays reports whether action a, run from the Agent, runs with
// the keyboard where it is. The ones that do not care where the keyboard is
// (the mouse, quit, a usage read) stay; `:` and `d` choose their own place
// (openActions records it, and the details overlay is laid over the Agent
// with the pane still focused behind it, so esc there returns to the
// agent). Everything
// else moves the keyboard to the list first, as its key is a Project key: an
// overlay it opens (the switcher, the inbox, a plan form) is then over the
// Project, and closing it leaves the list with the keyboard rather than
// typing the next key into the agent.
func agentActionStays(a modeAction) bool {
	switch a {
	case actionMouseToggle, actionQuit, actionRefreshUsage, actionActionsSearch, actionDetails, actionGoBack, actionKeys:
		return true
	case actionNeedNext, actionNeedPrev:
		// `]` and `[` decide the keyboard's place themselves, by where they
		// land: another agent keeps it on the agent with no stop at the
		// list in between, and a project landing hands it to the list.
		return true
	}
	return false
}

// keyRowForAction is the key table's row for action a at level lv, the one
// whose blocked reason applies there.
func keyRowForAction(lv level, a modeAction) (keyRow, bool) {
	for _, r := range keyTable {
		if r.action == a && r.levels.has(lv) {
			return r, true
		}
	}
	return keyRow{}, false
}

// runLevelAction runs action a at level lv, from mode (the mode the key or
// the search was opened in) and with placeholder saying the Agent level was
// the agent placeholder. It is the one place a level key and a `:` row run,
// so the two cannot do different things.
//
// A row whose blocked reason holds says why in a notice and runs nothing:
// with keys working alone there is no dialog to dim the row in, and a key
// that silently did nothing would read as broken.
func (m Model) runLevelAction(lv level, mode inputMode, a modeAction, placeholder bool) (tea.Model, tea.Cmd) {
	if row, ok := keyRowForAction(lv, a); ok {
		if reason := row.blockedReason(m.leaderConditions()); reason != "" {
			m.notify(notifyInfo, reasonSentence(reason))
			return m, nil
		}
	}
	if m.detailsOpen && a == actionRestart {
		// The `:` search's restart row run over the details overlay is `r`
		// inside it, so the restart's pane can bring the Agent up in the
		// overlay's place (detailsRestart).
		return m.detailsRestart()
	}
	if m.detailsOpen && !detailsActionStays(a) {
		// A row run from the `:` search the details overlay opened: the
		// action opens or changes something the overlay would cover, so the
		// overlay closes first and the action runs from the level under it,
		// in the mode that level has (the overlay's mode is gone with it).
		m.closeDetails()
		mode = activeInputMode(m.modeSnapshot())
	}
	switch a {
	case actionGoIn:
		return m.levelGoIn(lv)
	case actionGoBack:
		return m.levelGoBack(lv, placeholder)
	case actionActionsSearch:
		return m.openActions(mode, lv, placeholder)
	}
	if lv == levelAgent && !agentActionStays(a) {
		m.focus = focusList
		mode = inputNormal
	}
	return m.applyKeyAction(mode, a, tea.KeyPressMsg{})
}

// applyKeyAction performs the action a mode's rules claimed the key for.
//
// The cross-cutting actions are answered ONCE here for every mode that declares
// them, which is the duplication the old per-handler intercepts carried: the
// mouse chord was re-implemented at the top of all four modal handlers, the
// form handler, the focused-pane branch and the normal switch, and drift
// between any two of those copies was invisible until a user hit it.
//
// Only the modal answers are ambiguous across modes — y/n/esc mean the same
// gesture in the clean and restart confirmations but clear different captured
// state — so those, and only those, dispatch on the mode that claimed them.
func (m Model) applyKeyAction(mode inputMode, a modeAction, k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch a {
	case actionQuit:
		return m.requestQuit()
	case actionMouseToggle:
		return m.toggleMouseCapture()
	case actionKeys:
		return m.openKeys()
	case actionDetails:
		return m.showDetails()
	case actionDetailsClose:
		m.closeDetails()
		return m, nil
	case actionActionsSearch:
		// `:` from the details overlay (its own rule); the level keys reach
		// the search through runLevelAction instead.
		return m.openActions(mode, m.level(), m.focus == focusRight && m.agentPlaceholderDrawn())
	case actionPauseToggle:
		return m.pauseToggle()
	case actionAnswerOldest:
		return m.answerOldest()
	case actionNeedNext:
		return m.stepNeedsYouKey(true)
	case actionNeedPrev:
		return m.stepNeedsYouKey(false)
	case actionSectionNext, actionSectionPrev, actionSectionSessions, actionSectionBuild, actionSectionInbox:
		return m.changeSection(a)
	}

	// A move is the owner leaving where `]` last landed them: from here the
	// next `]` starts from the new selection.
	if isListMove(a) {
		m.clearNeedAt()
	}

	// The list's move keys move inside the project view's focused section:
	// the session list in Sessions (below), the task cursor in Build and the
	// item cursor in Inbox (moveInSection).
	if mode == inputNormal && isListMove(a) && m.focusedSection() != sectionSessions && m.hasActiveProject() {
		return m.moveInSection(a)
	}

	switch a {
	// schedulePreview runs AFTER the list has moved, in every direction: arming
	// first would target the row the selection just left. Every one of these
	// mutates m.list through the addressable local m — a value receiver here
	// would write the move to a copy Update discards.
	case actionCursorUp:
		m.list.CursorUp()
		return m, m.schedulePreview()
	case actionCursorDown:
		m.list.CursorDown()
		return m, m.schedulePreview()
	case actionPageUp:
		m.list.PrevPage()
		return m, m.schedulePreview()
	case actionPageDown:
		m.list.NextPage()
		return m, m.schedulePreview()
	case actionListTop:
		m.list.GoToStart()
		return m, m.schedulePreview()
	case actionListBottom:
		m.list.GoToEnd()
		return m, m.schedulePreview()

	// The detail viewport's own scroll, unlike the four cases above: these
	// move m.detailViewport, not m.list, and never call schedulePreview since
	// the selection itself hasn't changed.
	case actionDetailScrollUp, actionDetailScrollDown, actionDetailHalfPageUp, actionDetailHalfPageDown,
		actionDetailPageUp, actionDetailPageDown, actionDetailGotoTop, actionDetailGotoBottom:
		return m.scrollDetail(a)
	case actionDetailTabNext, actionDetailTabPrev, actionDetailTabJump:
		return m.switchDetailTab(a, k)

	case actionInbox:
		return m.openInbox()
	case actionPlanExtend:
		return m.startPlanExtend()
	case actionPlanAmend:
		return m.openPlanTextForm(PlanModeAmend)
	case actionPlanReconcile:
		return m.openPlanTextForm(PlanModeReconcile)
	case actionPlanFix:
		return m.openPlanTextForm(PlanModeFix)
	case actionPlanNew:
		return m.openPlanNewForm()
	case actionPlanCheck:
		return m.openPlanCheck()
	case actionPlanApprove:
		return m.openPlanApprove()

	case actionAllProjectsUp:
		return m.moveAllProjects(-1)
	case actionAllProjectsDown:
		return m.moveAllProjects(1)
	case actionAllProjectsPageUp:
		return m.moveAllProjects(-m.allProjectsPage())
	case actionAllProjectsPageDown:
		return m.moveAllProjects(m.allProjectsPage())
	case actionAllProjectsTop:
		return m.moveAllProjects(-len(m.allProjectsRows))
	case actionAllProjectsBottom:
		return m.moveAllProjects(len(m.allProjectsRows))
	case actionProjectAdd:
		return m.openProjectAdd()
	case actionProjectRemove:
		return m.openProjectRemove()
	case actionTaskStatus:
		return m.openTaskStatus()
	case actionTaskTokens:
		return m.openTaskTokens()
	case actionRefreshUsage:
		return m.refreshUsage()

	case actionNewSession:
		return m.openNewForm()
	case actionScratch:
		return m.openProviderPicker()
	case actionPickerUp, actionPickerDown:
		// The two pickers share their move keys; the mode says whose
		// highlight moves.
		delta := -1
		if a == actionPickerDown {
			delta = 1
		}
		if mode == inputPickTaskStatus {
			return m.moveTaskStatusPick(delta)
		}
		return m.moveProviderPick(delta)
	case actionProjects:
		return m.openJump()
	case actionClean:
		return m.cleanRequested()
	case actionRestart:
		return m.restartSelected()
	case actionEditLabel:
		return m.openLabelForm()

	case actionDismiss:
		return m.dismissSpawnError()
	case actionSubmit:
		// enter in the text prompts; the mode says which one.
		switch mode {
		case inputPlanText:
			return m.submitPlanText()
		case inputProjectAddFolder:
			return m.submitProjectAddFolder()
		case inputProjectAddName:
			return m.submitProjectAddName()
		case inputTaskStatusReplacedBy:
			return m.submitTaskStatusReplacedBy()
		}
		return m.submitLabelEdit()

	case actionFormCancel:
		return m.cancelForm()
	case actionFormSubmit:
		return m.submitFormKey(k)
	case actionFormNextField:
		return m.moveFormFocus(1, k)
	case actionFormPrevField:
		return m.moveFormFocus(-1, k)
	}

	switch mode {
	case inputConfirmClean:
		switch a {
		case actionConfirm:
			return m.confirmClean()
		case actionDeleteBranch:
			return m.deleteBranchClean()
		case actionDeny, actionCancel:
			return m.cancelClean()
		}
	case inputConfirmRestart:
		switch a {
		case actionConfirm:
			return m.confirmRestart()
		case actionDeny, actionCancel:
			return m.cancelRestart()
		}
	case inputEditLabel:
		if a == actionCancel {
			return m.cancelLabelEdit()
		}
	case inputPlanText:
		if a == actionCancel {
			return m.cancelPlanText()
		}
	case inputProjectAddFolder, inputProjectAddName:
		if a == actionCancel {
			return m.cancelProjectAdd()
		}
	case inputConfirmProjectRemove:
		switch a {
		case actionConfirm:
			return m.confirmProjectRemove()
		case actionDeny, actionCancel:
			return m.cancelProjectRemove()
		}
	case inputPickTaskStatus:
		switch a {
		case actionConfirm:
			return m.chooseTaskStatus()
		case actionCancel:
			return m.cancelTaskStatus()
		}
	case inputTaskStatusReplacedBy:
		if a == actionCancel {
			return m.cancelTaskStatus()
		}
	case inputConfirmTaskStatus:
		switch a {
		case actionConfirm:
			return m.confirmTaskStatus()
		case actionDeny, actionCancel:
			return m.cancelTaskStatus()
		}
	case inputConfirmPlanOpen:
		switch a {
		case actionConfirm:
			return m.confirmPlanOpen()
		case actionDeny, actionCancel:
			return m.cancelPlanOpen()
		}
	case inputConfirmPlanFolder:
		switch a {
		case actionConfirm:
			return m.confirmPlanFolder()
		case actionDeny, actionCancel:
			return m.cancelPlanFolder()
		}
	case inputConfirmGitHubRepo:
		switch a {
		case actionConfirm:
			return m.confirmGitHubRepo()
		case actionDeny, actionCancel:
			return m.cancelGitHubRepo()
		}
	case inputConfirmPlanApprove:
		switch a {
		case actionConfirm:
			return m.confirmPlanApprove()
		case actionDeny, actionCancel:
			return m.cancelPlanApprove()
		}
	case inputPickProvider:
		switch a {
		case actionConfirm:
			return m.confirmProviderPick()
		case actionCancel:
			return m.cancelProviderPick()
		}
	}
	return m, nil
}

// routeKeyToComponent hands a key no rule claimed to the mode's own bubbles
// component. Only the modes whose spec falls through to routeComponent reach
// here; the rest either swallow unclaimed keys or forward them to a child.
func (m Model) routeKeyToComponent(mode inputMode, k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch mode {
	case inputForm:
		return m.formComponentKey(k)
	case inputJump:
		return m.jumpComponentKey(k)
	case inputLeader:
		return m.leaderComponentKey(k)
	case inputInbox:
		return m.inboxComponentKey(k)
	case inputPlanCheck:
		return m.planCheckComponentKey(k)
	case inputTaskTokens:
		return m.taskTokensComponentKey(k)
	case inputKeys:
		return m.keysComponentKey(k)
	case inputEditLabel:
		var cmd tea.Cmd
		m.labelInput, cmd = m.labelInput.Update(k)
		return m, cmd
	case inputPlanText:
		var cmd tea.Cmd
		m.planTextInput, cmd = m.planTextInput.Update(k)
		return m, cmd
	case inputProjectAddFolder, inputProjectAddName:
		var cmd tea.Cmd
		m.projectAddInput, cmd = m.projectAddInput.Update(k)
		return m, cmd
	case inputTaskStatusReplacedBy:
		var cmd tea.Cmd
		m.taskStatusInput, cmd = m.taskStatusInput.Update(k)
		return m, cmd
	}
	return m, nil
}

// relayout brings every size-dependent component into agreement with the
// current width, height and sidebar split. It is the one re-layout sequence,
// run on every tea.WindowSizeMsg.
//
// A live pane must track the geometry: its region is recomputed from the
// current size and the emulator + the session's PTY are resized together
// (termpane.Resize does both), so the session reflows. Resize is a bounded,
// synchronous call (a frame write plus an in-memory grid resize), so it runs
// inline rather than in a Cmd — same treatment as the synchronous pane.Close
// on quit.
//
// This runs while the Project is showing with a live pane behind it: the
// emulator must keep tracking geometry or going back into the Agent exposes a
// grid with stale wrap points.
func (m *Model) relayout() {
	m.sizeFormComponents()
	m.sizeInbox()
	m.sizePlanCheck()
	m.sizeTaskTokens()
	m.sizeKeysOverlay()
	m.sizeSessionList()
	m.syncDetailViewport()
	if m.pane != nil {
		cols, rows := m.paneRegionSize()
		if err := m.pane.Resize(cols, rows); err != nil {
			m.notify(notifyError, "pane resize failed: "+err.Error())
		}
	}
}

// sizeSessionList keeps the stored list's page size in step with the frame, so
// the paging keys move by the number of rows actually on screen. renderRowsBody
// re-sizes a copy per frame for the rendering itself; this is for the keys.
func (m *Model) sizeSessionList() {
	if !m.ready || m.width == 0 || m.height == 0 {
		return
	}
	listOuter, _, bodyHeight := m.layoutDims()
	m.list.SetSize(sidebarContentWidth(listOuter), listBodyRows(bodyHeight)-sidebarCaptionRows)
}

// syncDetailViewport brings the Model's detail viewport — the one the scroll
// keys move — into agreement with the frame renderDetailsOverlay will draw:
// the same box (detailsBoxRect), the same body (detailBody), the same content
// width (detailContentWidth) and the same row count (detailsBodyBudget), then
// a re-clamp of the offset against that
// height. It runs from Update only (tea.WindowSizeMsg, rebuildListItems and
// scrollDetail), never from View, for the reason documented on the
// detailViewport field.
//
// All three inputs matter, not just the height. The content is what gives the
// viewport a TotalLineCount at all: SetContent is otherwise called only on
// renderDetailsOverlay's per-frame value copy, so without it the Model's viewport
// holds zero lines and every scroll method clamps its offset straight back to
// 0 regardless of how far it was asked to move. The height must be the rows
// the frame draws — detailsBodyBudget's, tab row and readout row already
// subtracted — and
// not bodyHeight or any other outer measure, because maxYOffset is
// TotalLineCount()-Height() (bubbles/v2@v2.2.1 viewport.go): a Model viewport
// taller than the drawn one has a ceiling that stops short of the last rows of
// content, and they can never be scrolled into view. (This is NOT the
// arrangement sizeSessionList has with the list: that one subtracts
// boxBorderCells itself and hands the list its inner row count directly, and
// the detail pane must do the same.)
//
// The trailing SetYOffset is the re-clamp: neither SetHeight nor SetWidth
// clamps yOffset (only SetYOffset does, against the CURRENT height), so a
// resize that grows the pane, or a tick that shortens the body, would
// otherwise leave the offset past the new end until something else moved it.
func (m *Model) syncDetailViewport() {
	if !m.ready || m.width == 0 || m.height == 0 {
		return
	}
	boxW, boxH := m.detailsSize()
	contentW := detailContentWidth(boxW)
	m.detailViewport.SetWidth(contentW)
	m.detailViewport.SetContent(m.detailBody(contentW))
	rows, _ := detailsBodyBudget(boxH, m.detailViewport.TotalLineCount())
	m.detailViewport.SetHeight(rows)
	m.detailViewport.SetYOffset(m.detailViewport.YOffset())
}

// resetDetailScroll snaps the detail viewport to the top when the selected
// session is not the one its offset belongs to (see detailViewportFor). It is
// called from both funnels a selection change can take — schedulePreview for
// key-driven moves and rebuildListItems for tick-driven ones — so the check
// lives once here rather than at either's call sites. Comparing by name
// rather than by list index is what catches a filter narrowing, or a
// discovery tick, putting a different session at the same on-screen row the
// cursor never moved from.
func (m *Model) resetDetailScroll() {
	name := ""
	if s, ok := m.selected(); ok {
		name = s.Name
	}
	if name != m.detailViewportFor {
		m.detailViewport.GotoTop()
		m.detailViewportFor = name
	}
}

// scrollDetail is the eight detail-scroll actions: up/down, ctrl+u/ctrl+d,
// pgup/pgdn and home/end, and the wheel's notches. It does nothing while the
// details overlay is closed: nothing is drawing the viewport then, so moving
// its offset would be state changing on an invisible surface.
// syncDetailViewport runs first every time — see its doc for why the offset
// otherwise clamps to 0, or stops short of the last rows.
func (m Model) scrollDetail(a modeAction) (tea.Model, tea.Cmd) {
	if !m.detailsOpen {
		return m, nil
	}
	m.syncDetailViewport()
	switch a {
	case actionDetailScrollUp:
		m.detailViewport.ScrollUp(1)
	case actionDetailScrollDown:
		m.detailViewport.ScrollDown(1)
	case actionDetailHalfPageUp:
		m.detailViewport.HalfPageUp()
	case actionDetailHalfPageDown:
		m.detailViewport.HalfPageDown()
	case actionDetailPageUp:
		m.detailViewport.PageUp()
	case actionDetailPageDown:
		m.detailViewport.PageDown()
	case actionDetailGotoTop:
		m.detailViewport.GotoTop()
	case actionDetailGotoBottom:
		m.detailViewport.GotoBottom()
	}
	return m, nil
}

// releasePaneFocus is the Agent's way back to Project (ctrl+] esc, T16.2;
// ctrl+] \ before it, ctrl+\ before T7.2): the keyboard comes back to the
// dashboard and the session is left completely alone — still attached, still
// rendering in the pane (A5).
func (m Model) releasePaneFocus() (tea.Model, tea.Cmd) {
	m.focus = focusList
	m.notify(notifyInfo, "input goes to the dashboard — "+m.paneName+" still running · "+m.keys.Focus.Help().Key+" to open it again")
	return m, nil
}

// showDetails is `d` (the leader then `d` in the Agent): the selected
// session's details overlay (detailsoverlay.go, §4.4), recorded as opened
// from the level the dashboard is at. Nothing under it moves: from the Agent
// the pane stays installed, focused and drawn (dimmed) behind the overlay,
// its preview generation untouched, so esc puts the keyboard straight back
// in the agent. With no session selected nothing opens and the notice says
// why (detailsBlockedReason, which runLevelAction checks for a key; this
// check is for a caller that reaches here another way).
func (m Model) showDetails() (tea.Model, tea.Cmd) {
	s, ok := m.selected()
	if !ok || !m.hasActiveProject() {
		m.notify(notifyInfo, reasonSentence(detailsBlockedReason(leaderConds{})))
		return m, nil
	}
	m.openDetails(m.level(), s.Name)
	return m, nil
}

// openAgent is enter on a session at Project: what sessionEnterTarget says
// the row opens. A row with an agent opens its Agent with the keyboard on it
// (§5): the right side switches to the agent view, an installed pane for the
// row is focused, and a row with none is connected with the focus already on
// the right, so the pane lands focused (connectSelected), or joins the
// restart or dial already out for it. A row with no live agent (stopped,
// pending, a fault, cleaning) opens its details overlay over the Project
// instead, as §4.3 says the Agent shows such a session's details; `r` there
// restarts a stopped one (detailsRestart). Enter NEVER restarts a host,
// because "look at this session" must not spawn a process as a side effect.
// The view is the agent's either way, so a row that comes back to life shows
// its agent.
func (m Model) openAgent() (tea.Model, tea.Cmd) {
	if !m.hasActiveProject() {
		return m, nil
	}
	m.view = viewAgent
	s, ok := m.selected()
	if !ok {
		if m.paneForSelection() {
			return m.focusPane()
		}
		return m, nil
	}
	if sessionEnterTarget(m.sessionEnterFacts(s)) == opensDetails {
		m.openDetails(levelProject, s.Name)
		return m, nil
	}
	if m.paneForSelection() {
		return m.focusPane()
	}
	return m.connectSelected()
}

// detailsRestart is `r` inside the details overlay: restartSelected exactly
// (a stopped row restarts at once, a WEDGED running one asks first), but only
// where restartKeyLive says `r` acts, which is also where the overlay draws
// its `r restart` chip. Anywhere else it does nothing, silently: the key is
// not offered there, and restartSelected's "being cleaned" notice would toast
// over the overlay for a key the overlay never named.
//
// A stopped row's restart is recorded in detailsRestartFor so its pane's
// arrival can close the overlay and open the Agent (applyPaneAttached's
// fromDetails). The request itself goes out exactly as `r` from the list
// sends it (selectionBound), because the overlay was opened at Project with
// the keyboard there: if the owner closes the overlay before the pane lands,
// the pane installs quietly behind the Project as it always has.
func (m Model) detailsRestart() (tea.Model, tea.Cmd) {
	if !m.restartKeyLive() {
		return m, nil
	}
	if s, ok := m.selected(); ok && m.restartableStopped(s) {
		m.detailsRestartFor = s.Name
	}
	return m.restartSelected()
}

// detailsAwaitsPane is whether a pane arriving for name is the one `r` in the
// details overlay asked for, with the overlay still open on that session.
func (m Model) detailsAwaitsPane(name string) bool {
	return name != "" && m.detailsOpen && m.detailsFor == name && m.detailsRestartFor == name
}

// cleanRequested is the clean action (`c` at Project). It goes straight to
// the confirmation, a pane open on the session included: since T16.2 no key
// closes a pane on its own, so the pane is detached when the clean is
// confirmed (dispatchConfirmedClean) rather than demanded closed first.
func (m Model) cleanRequested() (tea.Model, tea.Cmd) {
	return m.cleanSelected()
}

// requestQuit implements the quit keybinding. It closes any open pane and
// exits: closing a pane DETACHES from the session's host (hostpane.go's
// hostChild.Close), and that host is a separate process, so quitting the
// dashboard cannot end a session. Reattach with `afk dash` or
// `afk attach <name>`.
//
// Quitting is immediate and unconditional, including while a clean is still in
// flight. That abandons the clean's Cmd goroutine, and a clean is not indifferent
// to that the way an attached host is: by the time `git worktree remove` runs,
// `.afk/{name}/` has already been archived AND deleted from the worktree, so a
// process death in that window leaves a half-applied clean — archived, source
// gone, worktree still there — with the archive path reported nowhere.
//
// Guarding that window properly needs a context.Context threaded through
// Deps.CleanSession into session.CleanSession, whose signature is fixed; without
// one, the clean can be neither cancelled nor waited for. A two-press warning was
// tried here and withdrawn: it could not survive its own state (one flag, never
// reset, warns once per process rather than once per clean), and a guard that
// silently stops guarding is worse than an honest immediate quit. See issues.md.
func (m Model) requestQuit() (tea.Model, tea.Cmd) {
	m.forgetPane()
	// Closing the fsnotify watcher releases its kqueue/inotify descriptor and
	// ends the blocked subscription Cmd by closing its Events channel. Bounded and
	// synchronous, so it runs inline here alongside the pane close rather than in a
	// Cmd — a tea.Quit returned beside it may never let a Cmd run.
	//
	// EVERY watcher, not just the launch repo's: the set is per project (see
	// Model.rendezvous), and one left open is one kqueue descriptor and one
	// goroutine that outlive the dashboard.
	dirs := m.rendezvousDirs()
	if m.focusWatch != nil {
		_ = m.focusWatch.Close()
		m.focusWatch = nil
	}
	for _, rz := range m.rendezvous {
		if rz.watch != nil {
			_ = rz.watch.Close()
		}
	}
	m.rendezvous = nil
	// The dashmeta entries go with them, inline for that same reason: a click
	// landing after a clean quit would otherwise switch the user to a pane with no
	// dashboard in it — for whichever project's notification they clicked, which is
	// why every directory is swept and not only the launch one. Best-effort and
	// idempotent — dashmeta.RemoveEntry counts an already-absent file as success —
	// because runDashWith removes the launch one again in a defer, which is the only
	// removal an abnormal exit gets. The nil guard is for a Model that never went
	// through New (the zero value, which this package's own tests construct) and
	// therefore never had withDefaults fill the seam in.
	if m.deps.RemoveDashEntry != nil {
		for _, dir := range dirs {
			_ = m.deps.RemoveDashEntry(dir)
		}
	}
	// Whatever the last tick left pending is written here, INLINE, for the same
	// reason the watcher closes and the dashmeta removals above are inline: a Cmd
	// returned beside tea.Quit may never run, and quitting is exactly when the
	// selection worth remembering is the one the user is looking at. Each entry
	// is one small same-directory write, bounded like those, and the errors are
	// dropped on the same best-effort policy as the selectionFlushedMsg case.
	//
	// A tick flush may still be in flight on its own goroutine right now, so
	// these writes can interleave with it. Serializing them belongs to the
	// production seam (W5.T1 puts a mutex there) — this package must not import
	// sync for a concern only the real writer has, and a lost entry here costs
	// one project reopening on row 0.
	//
	// A tighter case than interleaving: a flushSelectionsCmd dispatched by the
	// tick just BEFORE q already cleared m.dirtySelections when it was
	// dispatched, so those entries are not in the loop below at all — they are
	// running on the goroutine bubbletea's handleCommands started for that Cmd
	// and never waits on before Program.Run returns (charm.land/bubbletea/v2
	// tea.go:727, "we'll have to leak the goroutine"). Process exit can cut
	// that write off mid-flight, at worst leaving an orphaned temp file next to
	// the state file. Consistent with this feature's best-effort posture: the
	// next Remember call is unaffected (a fresh temp name, not this one), and
	// nothing here claims that write completed.
	if m.deps.RememberSelection != nil {
		for key, name := range m.dirtySelections {
			_ = m.deps.RememberSelection(key, name)
		}
	}
	// The pending opens are written inline here on exactly the same reasoning,
	// and they are the LAST thing the quit path can still save: quitting
	// immediately after switching to a project is the common shape (open the
	// dashboard, jump to the project you wanted, work, quit), so a count left
	// to a Cmd beside tea.Quit is the count of the switch the user most
	// recently made. One call per pending increment, because the seam adds one
	// per call — see dirtyOpens.
	if m.deps.RecordProjectOpen != nil {
		for path, n := range m.dirtyOpens {
			for range n {
				_ = m.deps.RecordProjectOpen(path)
			}
		}
	}
	m.quitting = true
	return m, tea.Quit
}

// openNewForm opens the in-TUI session-creation form (the `n` keybinding).
// Unlike clean, this is allowed while a pane is open: the create runs in the
// background and the new session gets its own host, so it does not disturb
// whatever is currently rendered (see the spawnFinishedMsg handler).
func (m Model) openNewForm() (tea.Model, tea.Cmd) {
	// Inert in the empty state: a session
	// needs a repo to be created in, and View draws the empty-state block instead
	// of the form — opening it here would trap the keyboard in an invisible form.
	if !m.hasActiveProject() {
		m.notify(notifyWarn, "no active project to create a session in")
		return m, nil
	}
	m.formOpen = true
	m.form = newNewForm(m.stack, m.plainBranches, m.plainTrunk)
	m.clearStatus()
	// Size the components here, not in renderNewForm: View has a value receiver,
	// so any sizing it applies is written to a copy and thrown away — a list left
	// at 0x0 renders nothing and filters nothing. Returning the focus Cmd is what
	// starts the name field's cursor blinking; dropping it leaves the form
	// looking inert until the first Tab.
	m.sizeFormComponents()
	return m, m.form.nameInput.Focus()
}

// sizeFormComponents applies the current layout to the form's bubbles
// components. Called on open and on resize — both places where the mutation
// lands on the real Model rather than a render-time copy.
func (m *Model) sizeFormComponents() {
	if !m.formOpen || m.width == 0 {
		return
	}
	contentW, contentH := m.formModalDims()
	listH := contentH - formChromeRows
	if listH < 3 {
		listH = 3
	}
	m.form.nameInput.SetWidth(contentW - 6)
	m.form.description.SetWidth(contentW - 6)
	m.form.baseList.SetWidth(contentW)
	m.form.baseList.SetHeight(listH)
}

// formChromeRows is what renderNewFormModal draws besides the base list: the
// name label and input, a blank, the description label and its 3-row
// textarea, a blank, the base label, a blank, the Autonomous checkbox, and the
// Provider selector — 12 rows. The three-field form counted 9 rows but
// declared 10; the tenth was the empty row left by the newline written after
// the list, which the renderer no longer writes, so the sum and the constant
// now agree (checked against dash-120x40-form). The optional error line is not
// counted — the box's MaxHeight absorbs it — and the keys are on the footer's
// help line.
const formChromeRows = 12

// formModalWidth and formModalRows are the creation form's preferred content
// size. Wide enough for a long branch name in the base list, tall enough for
// the chrome plus an eight-row base list; modalContentDims clamps both to the
// terminal.
const (
	formModalWidth = 64
	formModalRows  = formChromeRows + 8
)

// formModalDims is the form modal's content size for the current terminal.
func (m Model) formModalDims() (contentW, contentH int) {
	return modalContentDims(m.width, m.height, formModalWidth, formModalRows)
}

// formBaseFiltering reports whether the base list is mid-filter-EDIT, the one
// state in which the form's three claimed gestures mean something other than
// what they mean anywhere else. The registry says inputForm owns esc, tab,
// shift+tab and enter; this predicate is what those handlers ask to find out
// what the gesture means right now — the same division of labor the switcher
// has, where the parent claims the key and the component stages the meaning.
//
// The form's full routing table:
//   - Base focused AND list.Filtering: the list owns enter/tab/shift+tab/
//     ctrl+k/up/ctrl+j/down (AcceptWhileFiltering). Tab and Enter apply the
//     filter. Esc cancels the filter and the form stays open. Empty-filter
//     trap: with no filter input bubbles has already disabled and swallowed
//     tab/enter, so those two reset the filter and then do what they mean
//     outside one.
//   - Enter, otherwise: submits from name, base, the Autonomous checkbox and
//     the Provider selector.
//     With the description focused it falls through to the component, which
//     inserts a newline.
//   - Esc, otherwise: cancels the form (its only exit).
//   - Tab/Shift+Tab, otherwise: cycle focus through name/description/base/
//     autonomous/provider, wrapping; blur outgoing, focus incoming, return the
//     Cmd.
//   - Everything else: the mode falls through to formComponentKey, where space
//     on the checkbox toggles it, space/→ and ← on the Provider selector cycle
//     it forward and back (and space is a literal space in name/description).
//
// q is a valid session-name character and ctrl+c is not a rule in this mode
// (it falls through to the component), so Esc is the form's only exit
// (TestFormSwallowsQuitKeysEscIsOnlyExit). The mouse chord IS a rule and is not
// an exit: it reconfigures the terminal and returns with formOpen and the name
// buffer untouched. Being a rule is what makes it reachable in every form
// state, live filter included; bubbles' filter does not claim ctrl+g, so that
// costs the filter submachine nothing.
func (m Model) formBaseFiltering() bool {
	return m.form.focusIndex == formFocusBase && m.form.baseList.FilterState() == list.Filtering
}

// cancelForm is the form's esc.
func (m Model) cancelForm() (tea.Model, tea.Cmd) {
	// Esc on the base field clears an ACTIVE OR APPLIED filter before it will
	// cancel the form. FilterState has three values, not two: Tab/Enter move a
	// live filter from Filtering to FilterApplied, so gating on Filtering alone
	// would close the whole form on the user's first press after they narrowed
	// the list — the opposite of the documented two-level behavior, and the same
	// trap the old project switcher's two-stage Esc gated on "!= Unfiltered"
	// to avoid.
	if m.form.focusIndex == formFocusBase && m.form.baseList.FilterState() != list.Unfiltered {
		m.form.resetFilterKeepingSelection()
		return m, nil
	}
	m.formOpen = false
	m.form = newNewForm(m.stack, m.plainBranches, m.plainTrunk)
	m.notify(notifyInfo, "session creation cancelled")
	return m, nil
}

// submitFormKey is the form's enter: apply a live filter, insert a newline in
// the description, or submit, depending on component state.
func (m Model) submitFormKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.formBaseFiltering() {
		if m.form.baseList.FilterInput.Value() != "" {
			var cmd tea.Cmd
			m.form.baseList, cmd = m.form.baseList.Update(k)
			return m, cmd
		}
		// bubbles disables AcceptWhileFiltering while the filter input is empty
		// (list.go's updateKeybindings), so the key arrives already swallowed.
		// Reset, then do what it means outside a filter — otherwise the user is
		// stuck in Filtering with only Esc out.
		m.form.resetFilterKeepingSelection()
	}
	if m.form.focusIndex == formFocusDescription {
		return m.formComponentKey(k)
	}
	// Name, base, the checkbox and the provider selector all submit. Enter on
	// the checkbox does not toggle it (nor cycle the selector): space does, and
	// an enter that toggled would make the field the only one from which the
	// form cannot be submitted.
	return m.submitNewForm()
}

// moveFormFocus is the form's tab (delta 1) and shift+tab (delta -1).
func (m Model) moveFormFocus(delta int, k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.formBaseFiltering() {
		if m.form.baseList.FilterInput.Value() != "" {
			var cmd tea.Cmd
			m.form.baseList, cmd = m.form.baseList.Update(k)
			return m, cmd
		}
		m.form.resetFilterKeepingSelection()
	}
	return m, m.form.moveFocus(delta)
}

// formComponentKey is inputForm's fall-through: the focused field gets the key.
func (m Model) formComponentKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.form.focusIndex {
	case formFocusName:
		m.form.nameInput, cmd = m.form.nameInput.Update(k)
		// Editing the name drops a stale validation error, as the hand-rolled
		// editor this replaced did on every insert and delete. Without it the
		// user keeps reading an error naming a name they already fixed.
		m.form.err = ""
	case formFocusDescription:
		m.form.description, cmd = m.form.description.Update(k)
	case formFocusBase:
		m.form.baseList, cmd = m.form.baseList.Update(k)
	case formFocusAutonomous:
		// Space is the toggle only here; every other key on the checkbox is
		// inert. Mod is checked so Ctrl+Space (NUL) does not toggle.
		if k.Code == tea.KeySpace && k.Mod == 0 {
			m.form.toggleAutonomous()
		}
	case formFocusProvider:
		// Space and → cycle forward, ← back; everything else is inert. Same
		// Mod guard as the checkbox, so Ctrl+Space and modified arrows do
		// nothing. ← and → are free here: only the name and description use
		// them as cursor keys, and those arms above are untouched.
		if k.Mod != 0 {
			break
		}
		switch k.Code {
		case tea.KeySpace, tea.KeyRight:
			m.form.cycleProvider(1)
		case tea.KeyLeft:
			m.form.cycleProvider(-1)
		}
	}
	return m, cmd
}

// submitNewForm validates the form's current name (via session.ValidateName,
// wrapped in newForm.validate — this package never re-implements that regex)
// and its provider/mode pair (via provider.CheckMode, likewise never
// re-implemented here) and, on success, dispatches the background create. On a
// validation failure the form stays open with the inline error newForm.validate
// just recorded — no Cmd is returned, so nothing async happens for an invalid
// name or a codex create without Autonomous.
//
// Submitting does two things beyond dispatching the Cmd: it records the name in
// pendingSpawns and merges the placeholder row in immediately, so the list
// shows the pending session on this very frame instead of waiting up to a full
// tick. The merge inside applyLoaded is what keeps it there afterwards.
func (m Model) submitNewForm() (tea.Model, tea.Cmd) {
	name, base, description, autonomous, providerName, err := m.form.validate()
	if err != nil {
		return m, nil
	}
	m.formOpen = false
	m.form = newNewForm(m.stack, m.plainBranches, m.plainTrunk)
	m.addPendingSpawn(name)
	m.mergePendingSpawnsNow()
	m.notify(notifyInfo, "creating "+name+"…")
	return m, m.spawnNewCmd(name, base, description, autonomous, providerName)
}

// openProviderPicker implements the `o` keybinding: it raises the one-off
// provider picker (inputPickProvider) with Claude highlighted, and does
// nothing else. No placeholder row and no Cmd until enter: the picker is the
// point at which the user can still change their mind, so esc must leave the
// dashboard exactly as it was, and a row or a dispatched listing created here
// would have to be unwound on cancel.
//
// Inert in the empty state with today's warning, on the same predicate
// openScratch and openNewForm use: a dialog offering to create a
// session where none can be created would only fail one keypress later.
func (m Model) openProviderPicker() (tea.Model, tea.Cmd) {
	if !m.hasActiveProject() {
		m.notify(notifyWarn, "no active project to create a session in")
		return m, nil
	}
	m.modal = inputPickProvider
	m.providerPick = defaultProviderIndex()
	m.clearStatus()
	return m, nil
}

// moveProviderPick moves the picker's highlight by delta, wrapping at both
// ends the way newForm.cycleProvider does, so up from Claude lands on the
// last provider rather than sticking.
func (m Model) moveProviderPick(delta int) (tea.Model, tea.Cmd) {
	n := len(provider.Names())
	m.providerPick = ((m.providerPick+delta)%n + n) % n
	return m, nil
}

// confirmProviderPick is the picker's enter: close it, then start the one-off
// on the highlighted provider through openScratch, which owns the placeholder
// row and the listing → create chain. The modal is cleared BEFORE openScratch
// runs so a refusal there (the project vanished under the dialog) leaves the
// keyboard in normal mode rather than stranded in a picker with nothing to do.
func (m Model) confirmProviderPick() (tea.Model, tea.Cmd) {
	names := provider.Names()
	idx := m.providerPick
	if idx < 0 || idx >= len(names) {
		idx = defaultProviderIndex()
	}
	m.modal = inputNormal
	return m.openScratch(names[idx])
}

// cancelProviderPick is the picker's esc: close it with nothing created. No
// placeholder exists to clear and no Cmd is returned, because
// openProviderPicker deferred both to confirm.
func (m Model) cancelProviderPick() (tea.Model, tea.Cmd) {
	m.modal = inputNormal
	m.notify(notifyInfo, "one-off cancelled")
	return m, nil
}

// openScratch starts a one-off "scratch" session on providerName: the `o`
// picker's confirm calls it (confirmProviderPick), and nothing about the
// create itself is interactive past that choice. It follows
// submitNewForm's dispatch shape (addPendingSpawn, mergePendingSpawns, a
// "creating…" status, then the background create) with one substitution: the
// name comes from session.NextScratchName here instead of a validated form
// field, because there is no form.
//
// See decisions.md (W3.T1) for why that name is only ever used for this
// pending row and never passed to spawnScratchCmd as an argument — afk new
// allocates its own name for a scratch session with no positional argument
// given.
//
// The name is guessed here from MEMORY ALONE (m.sessions plus pendingSpawns)
// so the placeholder row lands on this very frame with no I/O on the Update
// path — the same off-loop discipline writeLabelCmd documents. The `.afk/`
// disk listing that catches a scratch directory discovery has not seen yet
// (one created in another terminal between ticks) still happens, but inside
// the returned Cmd; applyScratchDirs corrects the placeholder if the disk
// disagrees, BEFORE the create is dispatched.
//
// providerName rides along (listScratchDirsCmd → scratchDirsListedMsg →
// spawnScratchCmd); only a non-default one reaches `afk new` as --provider.
//
// m.sessions, not m.pendingSpawns, is what two `o` presses in the same tick
// must be guessed against: mergePendingSpawns drops a name from pendingSpawns
// as soon as its placeholder row is inserted (it only tracks names still
// WAITING to be merged), but that same row stays in m.sessions until the real
// create replaces or fails it — insertPlaceholder puts it there before this
// method returns. Guessing against pendingSpawns alone would let a name
// reappear on the very next press.
func (m Model) openScratch(providerName string) (tea.Model, tea.Cmd) {
	// Inert in the empty state, on the same predicate openNewForm
	// uses: a session needs a repo to be created in.
	if !m.hasActiveProject() {
		m.notify(notifyWarn, "no active project to create a session in")
		return m, nil
	}
	known := make([]string, 0, len(m.sessions)+len(m.pendingSpawns))
	for _, s := range m.sessions {
		known = append(known, s.Name)
	}
	known = append(known, m.pendingSpawns...)
	name := session.NextScratchName(known)
	m.addPendingSpawn(name)
	m.mergePendingSpawnsNow()
	m.notify(notifyInfo, "creating "+name+"…")
	return m, m.listScratchDirsCmd(name, providerName)
}

// scratchDirsListedMsg carries one `.afk/` directory listing back to Update
// alongside the placeholder name openScratch guessed before the listing ran.
type scratchDirsListedMsg struct {
	guess string
	dirs  []string
	err   error
	// provider is the agent provider chosen when `o` was pressed, carried
	// through the async listing so applyScratchDirs dispatches the create with
	// the choice made then, not whatever the dashboard holds when the listing
	// lands. "" and provider.Default both mean the default.
	provider string
}

// listScratchDirsCmd runs Deps.ListSessionDirs off the UI thread, on the same
// off-loop-I/O discipline writeLabelCmd documents: even a local directory read
// is I/O, and I/O on the synchronous Update path can stall the whole dashboard
// on a slow or contended filesystem. repo and the seam are captured at
// Cmd-construction time, as loadCmd does.
func (m Model) listScratchDirsCmd(guess, providerName string) tea.Cmd {
	repo := m.repo
	list := m.deps.ListSessionDirs
	return func() tea.Msg {
		dirs, err := list(repo)
		return scratchDirsListedMsg{guess: guess, dirs: dirs, err: err, provider: providerName}
	}
}

// applyScratchDirs finishes what openScratch started, once the disk listing
// is in hand: dispatch the create, correcting the placeholder first when the
// disk already holds the guessed name. The correction matters because the
// guess is what the spawnFinishedMsg handler later clears the placeholder by
// and opens the pane for — left uncorrected, a stale guess would point both
// at a directory afk new never created. On a failed listing the placeholder
// is taken down instead of dispatching: a permanent "creating…" row for a
// create that never ran is the leak clearPendingSpawn exists to prevent.
func (m Model) applyScratchDirs(msg scratchDirsListedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.clearPendingSpawn(msg.guess)
		m.notify(notifyError, "one-off session failed: "+msg.err.Error())
		return m, nil
	}
	name := msg.guess
	for _, dir := range msg.dirs {
		if dir != msg.guess {
			continue
		}
		// The guess is already taken on disk. Re-guess against everything now
		// known — the listing plus the current rows (which include this
		// guess's own placeholder and any later presses' placeholders, all of
		// which must stay avoided).
		known := make([]string, 0, len(msg.dirs)+len(m.sessions)+len(m.pendingSpawns))
		known = append(known, msg.dirs...)
		for _, s := range m.sessions {
			known = append(known, s.Name)
		}
		known = append(known, m.pendingSpawns...)
		name = session.NextScratchName(known)
		m.clearPendingSpawn(msg.guess)
		m.addPendingSpawn(name)
		m.mergePendingSpawnsNow()
		m.notify(notifyInfo, "creating "+name+"…")
		break
	}
	return m, m.spawnScratchCmd(name, msg.provider)
}

// openJump opens the jump search (`/` at every level, the leader then `/`
// in the Agent, §4.6). The rows are a snapshot of what is in memory now
// (jumpFactsNow); opening reads nothing and dials nothing, and the box never
// refreshes itself while it is up (jump.go).
//
// It is allowed while a pane is open and deliberately leaves it ALONE: the
// pane keeps running and stays connected, exactly as it does behind the
// Project. Only a landing that leaves the session detaches it, so the cost
// of a glance at the list is never a dropped connection.
//
// It opens at All projects with no project open too (A7), since the home
// screen's rows are in memory there. The key's blocked reason
// (jumpBlockedReason) keeps it shut only when there is nothing at all to
// list, the empty state, where a box with no rows would be a key trap in
// front of the one screen that says what to do.
func (m Model) openJump() (tea.Model, tea.Cmd) {
	m.jump = newJumpDialog(jumpRows(m.jumpFactsNow()), m.leaderKey.name)
	m.jumpOpen = true
	m.clearStatus()
	return m, nil
}

// jumpFactsNow is the jump rows' facts from memory: the projects in
// home-screen order with their sessions and cached rows, the same builder
// `]` uses (needsYouProjectsNow), and each project's tasks from the
// every-project cache, re-keyed from the row's main checkout to the project
// list's path as the projects are.
func (m Model) jumpFactsNow() jumpFacts {
	tasks := make(map[string][]JumpTask, len(m.everyProject.Tasks))
	for root, ts := range m.everyProject.Tasks {
		tasks[m.needPathForRoot(root)] = ts
	}
	return jumpFacts{Projects: m.needsYouProjectsNow(), Tasks: tasks}
}

// canJump reports whether the jump search has anything to list: a project
// open, a project in the list, or a row in the every-project cache.
func (m Model) canJump() bool {
	return m.hasActiveProject() || len(m.projects) > 0 || len(m.everyProject.Rows) > 0
}

// jumpComponentKey is inputJump's fall-through: every key but ctrl+c reaches
// the box, and this carries out what jumpKeyDecision decided. Type, clear
// and move change only the box. Close and go close it first, so the landing
// sees the dashboard with the box gone.
//
// Its spec's single rule is the ctrl+c chord, which quits, as over the `:`
// search: the box holds a throwaway query, and a box that could trap the
// keyboard would be worse than a lost search. `q` is a letter here.
func (m Model) jumpComponentKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	o := m.jump.decide(k, m.width, m.height)
	switch o.kind {
	case jumpClose:
		m.jumpOpen = false
	case jumpGo:
		m.jumpOpen = false
		return m.jumpLand(o.row)
	case jumpType, jumpClear, jumpMove:
		m.jump.apply(o)
	}
	return m, nil
}

// jumpLand goes to row (§4.6): a project opens its view, a session its agent
// and a task its project with the task selected in Build. Wherever it lands
// the owner has moved, so the last `]` landing is forgotten and the next `]`
// starts from here.
func (m Model) jumpLand(r jumpRow) (tea.Model, tea.Cmd) {
	m.clearNeedAt()
	m.focus = focusList
	switch r.kind {
	case jumpSession:
		item := needItem{
			Kind: needSession, ProjectPath: r.projectPath, ProjectName: r.projectName,
			Session: r.session, WorktreePath: r.worktree, Scratch: r.scratch, HostRunning: r.hostRunning,
		}
		if r.fault {
			// A worktree fault has no agent: it is opened where what is wrong
			// with it shows, the project view with it selected, as `]` does
			// for a broken session.
			item.Kind = needBroken
			nm, cmd := m.landNeedProject(item, r.session)
			return nm, cmd
		}
		// W3.T1's agent landing, enter's rule: no live agent opens the
		// session's details instead.
		nm, cmd := m.landNeedAgent(item)
		return nm, cmd
	case jumpTask:
		nm, cmd, ok := m.jumpToProject(r)
		if !ok {
			return nm, cmd
		}
		nm.selectBuildTask(r.taskID)
		return nm, cmd
	}
	nm, cmd, _ := m.jumpToProject(r)
	return nm, cmd
}

// jumpToProject opens r's project at Project, as enter on its home-screen
// row does (switchFromAllProjects): a gone or unreadable project says why
// and stays, the active project just closes All projects, and any other
// switches with the one-shot preview armed and the open counted. It reports
// false when it did not land in the project.
//
// switchProject owns every consequence of the switch — repointing m.repo,
// detaching the pane, clearing session-scoped state — and its Model must be
// kept (previewGen is bumped inside it). The preview and the count are only
// for a real switch: switchProject leaves m.repo alone for the path already
// active, and arming there would preview row 0 on the next ordinary tick
// with no switch to justify it, while counting would let a held-down enter
// inflate the open count of the project the owner is already in.
func (m Model) jumpToProject(r jumpRow) (Model, tea.Cmd, bool) {
	if r.hasRow {
		switch r.row.State {
		case projectrows.StateGone:
			m.notify(notifyWarn, allProjectsGoneNotice(r.row.Name))
			return m, nil, false
		case projectrows.StateUnreadable:
			m.notify(notifyWarn, allProjectsUnreadableNotice(r.row))
			return m, nil, false
		}
	}
	if r.projectPath == m.needsYouActivePath() {
		m.allProjectsOpen = false
		return m, nil, true
	}
	if _, ok := indexOfProject(m.projects, r.projectPath); !ok {
		m.notify(notifyInfo, allProjectsNotListedNotice(r.projectName))
		return m, nil, false
	}
	m.allProjectsOpen = false
	nm, cmd := m.switchProject(r.projectPath)
	if nm.repo != m.repo {
		nm.previewOnNextLoad = true
		nm.recordProjectOpen(r.projectPath)
	}
	return nm, cmd, true
}

// selectBuildTask focuses the project view's Build section with its cursor
// on task id. In a project whose build is in memory the cursor moves now;
// after a switch the snapshot is not read yet, and the id is enough:
// applyBuildLoaded's landBuildCursor keeps a cursor id the new rows hold, so
// the first read lands the cursor on the task. The switch has already reset
// the section and the cursor (forgetBuild), which is why this runs after it.
func (m *Model) selectBuildTask(id string) {
	m.section = sectionBuild
	m.buildCursorID = id
	if i := buildRowIndex(m.build.Rows, id); m.buildKnown && i >= 0 {
		m.buildCursorIndex = i
	}
}

// openActions is `:` (the leader then `:` in the Agent): the actions search
// over every action, with a snapshot of the rows for level lv and whether
// each can run right now. It records where it was opened — the mode, the
// level and whether that was the agent placeholder — which runLeaderRow
// needs and cannot work out again once the search's own mode outranks the
// surface it was opened from. Opening is pure state: no Cmd, nothing
// blocking.
func (m Model) openActions(from inputMode, lv level, placeholder bool) (tea.Model, tea.Cmd) {
	m.leaderFrom = from
	m.leaderLevel = lv
	m.leaderFromPlaceholder = placeholder
	m.leader = newLeaderDialog(actionRows(lv, m.leaderKey.name, m.leaderConditions()), m.leaderKey.name)
	m.leaderOpen = true
	m.clearStatus()
	return m, nil
}

// leaderConditions fills leaderConds from the live Model. Each field is the
// same expression the guard it mirrors uses (see leaderConds), so a row is
// dimmed exactly when its key would do nothing or only say why.
func (m Model) leaderConditions() leaderConds {
	conds := m.routeConditions()
	c := leaderConds{
		leader:            m.leaderKey.name,
		hasActiveProject:  m.hasActiveProject(),
		canJump:           m.canJump(),
		paneOpen:          conds.paneOpen,
		hasBuild:          m.hasBuild(),
		buildKnown:        m.buildKnown,
		canStartPlan:      m.deps.StartPlan != nil,
		canCheckPlan:      m.deps.LoadPlanStatus != nil,
		canApprovePlan:    m.deps.ApprovePlan != nil,
		canAllProjects:    m.deps.LoadAllProjects != nil,
		allProjectsOpen:   m.level() == levelAllProjects,
		canAddProject:     m.deps.AddProject != nil,
		canRemoveProject:  m.deps.RemoveProject != nil,
		canSetTaskStatus:  m.deps.SetTaskStatus != nil,
		canLoadTaskTokens: m.deps.LoadTaskTokens != nil,
		canRefreshUsage:   m.deps.RefreshUsage != nil,
		buildTabFocused:   m.buildSectionFocused(),
		planKnown:         m.planStatus.Known,
		planErr:           m.planStatus.Err != "",
		planSession:       m.planStatus.Session,
		planDamaged:       m.planStatus.Damaged,
		planMode:          m.planStatus.Mode,
		planPhase:         m.planStatus.Phase,
		restartLive:       m.restartKeyLive(),
	}
	if s, ok := m.selected(); ok {
		c.selected = true
		c.selectedPending = s.Pending
		c.selectedFault = isWorktreeFault(s.Kind)
		c.selectedCleaning = m.cleaningSessions[cleanIDFor(s)]
	}
	// The cursor's index is checked against its id as well as the rows'
	// length: the picker reads the row at the index, and a cursor whose two
	// halves disagreed would open it on another task.
	if m.buildCursorOnTask() && m.buildCursorID != "" && m.buildCursorIndex < len(m.build.Rows) &&
		m.build.Rows[m.buildCursorIndex].ID == m.buildCursorID {
		c.buildTaskSelected = true
	}
	if r, ok := m.selectedProjectRow(); ok {
		c.projectRowSelected = true
		c.projectRowListed = r.Listed
		c.projectRowName = r.Name
	}
	return c
}

// leaderComponentKey is inputLeader's fall-through: every key but ctrl+c
// reaches the search, and this carries out what leaderKeyDecision decided.
// Filter and move change only the search. Close and run close it first, so
// the action a row runs sees the dashboard as it is with the search gone — a
// run that opens an overlay or a modal gets the keyboard, not the search.
func (m Model) leaderComponentKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	o := m.leader.decide(k)
	switch o.kind {
	case leaderClose:
		m.leaderOpen = false
	case leaderRun:
		m.leaderOpen = false
		return m.runLeaderRow(o.row)
	case leaderFilter, leaderMove:
		m.leader.apply(o)
	}
	return m, nil
}

// runLeaderRow runs a `:` row's action through runLevelAction, the function
// every level key reaches, at the level and from the surface the search was
// opened at, so the search never has a second implementation of anything
// and nothing is ever typed to the agent.
func (m Model) runLeaderRow(r leaderRow) (tea.Model, tea.Cmd) {
	return m.runLevelAction(m.leaderLevel, m.leaderFrom, r.action, m.leaderFromPlaceholder)
}

// dismissSpawnError closes the spawn-error modal. Both esc and enter map to it
// (the modal is pure information — there is nothing to confirm, so demanding a
// specific key would only be a trap), and its spec swallows every other key, so
// the output tail cannot be lost to a stray keystroke.
//
// The mouse chord IS a rule in this mode, for the reason the modal exists: its
// whole content is the failed create's captured output, which is the text a user
// is most likely to want to select and paste into a bug report — and they cannot
// while the session owns the mouse. Releasing it leaves the modal up and the
// output intact (m.modal and the spawnErr* fields are untouched), so the text is
// still on screen to select once the terminal can select it.
func (m Model) dismissSpawnError() (tea.Model, tea.Cmd) {
	m.modal = inputNormal
	m.spawnErrName = ""
	m.spawnErrHeadline = ""
	m.spawnErrTail = nil
	return m, nil
}

// connectSelected opens the selected session's agent and focuses the right
// side for it: enter on a running session (openAgent) and enter in the agent
// placeholder (placeholderKey). Its dials go through openPaneFor only, so the clean gate, the preview
// invalidation and the project stamp all apply, and there is no second dial
// path.
//
// It NEVER restarts a host. A row with no live agent (rowHasAgent false) is
// refused here, at the entrance, rather than trusted to every caller's guard:
// "look at this session" must not spawn a process, and `r` (restartSelected)
// is the one restart key. A restart of ours already in flight counts as an
// agent, so connect joins it instead of refusing.
//
// Status validity deliberately does NOT gate this. A host is reached via
// host.json, which is written and read independently of status.json, so a
// STALE session with a live host is still perfectly viewable — and looking at
// it is often exactly how a user finds out why its status went stale.
//
// Focus moves to the right side at the keypress, not on arrival, and only
// when a connect is actually under way. The keypress is the user's choice of
// surface; waiting for the arrival to move the focus is what let a stray key
// typed during the dial land on a list binding. Every dial still carries
// focusOnArrival so applyPaneAttached knows the request was the user's: the
// pane lands focused, unless the user switched to the overview while it dialed
// (then it installs behind the overview), and a failed connect hands the
// keyboard back to the list, where enter retries.
//
// The refusals — no selection, a pending placeholder, a worktree fault, a
// clean in flight — leave the focus where it was: there is nothing on the
// right that could receive the keyboard, so moving it would strand it.
//
// The opposite error is corrected after the fact rather than pre-checked here:
// HostRunning is advisory (a recycled PID can fake it), so a session that
// claims to be running but is not gets fixed from the failed dial — see
// applyPaneAttached.
func (m Model) connectSelected() (tea.Model, tea.Cmd) {
	s, ok := m.selected()
	if !ok {
		return m, nil
	}
	if s.Pending {
		// A placeholder has no worktree and no host yet; the pane opens on its
		// own the moment the create finishes (see the spawnFinishedMsg handler).
		return m, nil
	}
	if isWorktreeFault(s.Kind) {
		// A worktree-side fault names a worktree, not a session: one of them
		// has no directory left and the other never had a `.afk/{name}/` of
		// its own, so there is no host.json to dial through and nothing a pane
		// could show. The refusal is on the KIND rather than on HostRunning
		// because that field is advisory — a recycled PID fakes it — and the
		// answer here does not depend on liveness at all. `c` is the only key
		// this row answers.
		return m, nil
	}
	if m.cleaningSessions[cleanIDFor(s)] {
		// A clean in flight stops this session's host and deletes its worktree.
		// Opening a pane over it would put a live view on a host the clean is
		// about to SIGKILL, so the answer is to say what is happening instead.
		m.notify(notifyWarn, "can't open "+s.Name+" — it's being cleaned")
		return m, nil
	}
	// Both callers handle this before calling us. Keep the guard for any
	// future caller: an installed pane never needs a second connection.
	if m.pane != nil && s.Name == m.paneName {
		m.showAndFocusPane()
		return m, nil
	}
	if !m.rowHasAgent(s) {
		m.notify(notifyInfo, s.Name+" "+stoppedText(s)+" · "+m.keyHintFor(actionRestart)+" to restart")
		return m, nil
	}
	return m, m.connect(paneOpenRequest{
		name:     s.Name,
		worktree: s.WorktreePath,
		scratch:  s.Scratch,
	})
}

// connect is the one way the right side is connected to a session: enter/f
// from the list, ctrl+t into the agent view, and a click-to-focus request all
// land here, so all three move the focus right at request time and agree on
// what an outstanding dial or restart means. It selects the agent view, and
// dials only through openPaneFor. It never starts a restart: callers refuse a
// row with no live agent first, and a restart already in flight is joined.
func (m *Model) connect(req paneOpenRequest) tea.Cmd {
	req.focusOnArrival = true
	m.view = viewAgent
	// A dial for this session is ALREADY outstanding — almost always a preview
	// whose settle timer fired just before the keypress. Dialing again would
	// connect a second client, and the host answers that by sending Stolen to
	// the first, so the pane this keypress is meant to type into would be torn down
	// as it arrived. Claim that dial instead and let the arrival already on its
	// way install the pane focused (see inFlightOpen and applyPaneAttached).
	// Bumping previewGen makes every other pending/dispatched preview stale;
	// claimedPreview is the narrow exception for this exact result, and it
	// survives any later bump — that is what a claim means.
	if m.attachInFlight.name == req.name {
		if m.attachInFlight.preview {
			m.previewGen++
			m.attachInFlight.claimedPreview = true
			m.previewOnNextLoad = false
		}
		m.attachInFlight.focusOnArrival = true
		m.focus = focusRight
		m.notify(notifyInfo, "opening "+req.name+"…")
		return nil
	}
	if m.restartInFlight.name == req.name && m.restartInFlight.gen == m.projectGen {
		m.restartInFlight.focusOnArrival = true
		m.focus = focusRight
		m.notify(notifyInfo, "restarting "+req.name+"…")
		return nil
	}
	// A pane for a DIFFERENT session is installed. It is deliberately left
	// running until the new connection succeeds (see applyPaneAttached, which
	// performs the swap): closing it first would mean a failed dial costs the
	// user the session they were already watching.
	if m.pane != nil {
		m.notify(notifyInfo, "switching to "+req.name+"…")
	} else {
		m.notify(notifyInfo, "opening "+req.name+"…")
	}
	cmd := m.openPaneFor(req)
	if cmd != nil {
		m.focus = focusRight
	}
	return cmd
}

// startRestart dispatches restartHostCmd and records it in restartInFlight.
// Every restart dispatch goes through here so the record cannot miss one.
func (m *Model) startRestart(req paneOpenRequest) tea.Cmd {
	m.noteOtherOpStarted()
	m.restartInFlight = inFlightRestart{name: req.name, gen: m.projectGen, focusOnArrival: req.focusOnArrival}
	return m.restartHostCmd(req)
}

// placeholderKey answers a key while the agent placeholder owns the
// keyboard with the key strip closed: enter connects (or reconnects) the
// selected row through connectSelected, r restarts it (A6: the placeholder
// keeps its enter and its restart), and the leader shows the key strip as it
// does over a live agent, whose esc goes back to Project. Every other key is
// swallowed: there is no agent to send it to, and nothing typed at a lost or
// still-connecting agent may reach a Project key.
//
// enter and r act alone here, unlike the Agent's other keys, because the
// rule that the leader comes first exists so the agent gets every key, and
// the placeholder is the one Agent with no agent to get them.
func (m Model) placeholderKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.keys.Focus):
		return m.connectSelected()
	case key.Matches(k, m.keys.Restart):
		return m.restartSelected()
	case key.Matches(k, m.keys.Leader):
		m.keyStripOpen = true
		m.clearStatus()
		return m, nil
	}
	return m, nil
}

// releasePlaceholderFocus is "back to Project" from the agent placeholder
// (ctrl+] esc there): the keyboard goes to the list, and the placeholder
// stays drawn for the connect or restart still under way.
func (m Model) releasePlaceholderFocus() (tea.Model, tea.Cmd) {
	m.focus = focusList
	m.notify(notifyInfo, "input goes to the session list · "+m.keys.Focus.Help().Key+" to come back")
	return m, nil
}

// takenOverMessage is the one sentence for a pane another client took over,
// shared by the status notice and the placeholder so the two cannot drift. The
// host's Stolen frame does not say who took the session, so the text names both
// possibilities rather than claiming it was the web page.
func takenOverMessage(name string) string {
	return name + " was taken over by another window or the web page; press enter to reopen it"
}

// lostPaneMessage is the placeholder line for a pane that ended while its
// session may still be running, and false for a session that ended: that row
// reads as stopped and needs no reconnect offer.
func lostPaneMessage(name string, msg paneExitedMsg) (string, bool) {
	switch {
	case msg.err != nil, msg.reason == host.DetachDropped:
		return "disconnected from " + name, true
	case msg.reason == host.DetachStolen:
		return takenOverMessage(name), true
	}
	return "", false
}

// handBackUnansweredFocus returns the keyboard to the list when a connect the
// user focused (connectSelected, or `r` from the right side) ends without a
// pane for the selected row: the dial failed, the restart failed, or a clean
// got there first. It acts while the focused right side shows the agent
// placeholder — a placeholder with nothing to connect has nothing that can
// act on a key, and the retry gesture is a Project key — and while it shows
// nothing at all (a stopped row whose restart failed has no agent to show),
// so the focus is never left on a surface that is not drawn. With the pane
// drawn, the user already went back to it, and nothing changes.
func (m *Model) handBackUnansweredFocus() {
	if m.focus == focusRight && !m.shouldRenderPane() {
		m.focus = focusList
	}
}

// forgetPane is the one teardown for an installed pane: close (detach) it,
// drop the handle, and hand the keyboard back to the list if the pane was
// what held it. The focus rule is the reason this is a helper rather than
// three lines at each site: a pane that owned the keyboard going away must
// not leave focus on the right with nothing drawn to receive it, while a
// focused OVERVIEW (right focus with the pane behind it, or for another row)
// keeps the keyboard exactly as before — the surface the user was reading is
// still there. The view is left alone; it is a dashboard-wide preference,
// not state about this pane.
//
// It is a no-op with no pane installed, so callers need no guard.
func (m *Model) forgetPane() {
	if m.pane == nil {
		return
	}
	if m.paneOwnsKeyboard() {
		m.focus = focusList
	}
	_ = m.pane.Close()
	m.pane = nil
	m.paneName = ""
}

// agentKeyHint is the key a toast names for "get to this row's agent" from
// wherever the keyboard is now: enter, which opens the selected session's
// agent from the list (openAgent), and from the agent placeholder reconnects
// it (placeholderKey). With the details overlay up enter is not its key, and
// esc first puts the keyboard back on the one of those two it was opened
// from, so the toast says both.
func (m Model) agentKeyHint() string {
	if m.detailsOpen {
		return "esc, then " + m.keys.Focus.Help().Key
	}
	return m.keys.Focus.Help().Key
}

// focusPane routes keyboard input to the session in the pane. It is the
// shared Enter/f current-surface action. Surface choice stays in
// applyKeyAction; this helper is pane-only.
func (m Model) focusPane() (tea.Model, tea.Cmd) {
	if m.pane == nil {
		m.notify(notifyWarn, "no session pane to open")
		return m, nil
	}
	m.showAndFocusPane()
	m.notify(notifyInfo, "input goes to "+m.paneName+" · "+keyName(m.leaderKey.name, levelAgent, actionGoBack)+" to return")
	return m, nil
}

// showAndFocusPane is the single way input is ever handed to the pane: it
// selects the agent view and focuses the right side together, so a focused
// pane is always the drawn one. Focusing without the view flip would land the
// keyboard on the overview when the pane sits behind it — a click-to-focus
// that arrives while the user is reading the summary must put them IN the
// session, not scrolling its diff.
//
// It deliberately does NOT set a status: each caller's wording differs (the
// Enter/f key and a honored click say different things),
// and folding them together here would flatten that.
func (m *Model) showAndFocusPane() {
	m.view = viewAgent
	m.focus = focusRight
}

// applyFocusRequest handles a pending click-to-focus request: open the named
// session's pane and focus it, so the user's keystrokes reach the Claude session
// instead of landing on the dashboard's list.
//
// It connects through connect, the entrance enter/f uses, so a click and a
// keypress agree on focus, view, and outstanding dials. The two things it adds are
// deciding WHICH session, from a name on disk, and then SELECTING that session
// in the list — moving the cursor onto it and recording it as this project's
// remembered row — so the dashboard behind the pane is showing the session the
// click was about, this run and the next.
//
// Every refusal is silent and consumes the request. A stale one has missed its
// moment; an unknown name has nothing to open; a session mid-clean must not get a
// pane. None of them is an error the user needs told about — the click simply does
// nothing, exactly as a click on a stale pane does.
func (m Model) applyFocusRequest(msg focusRequestMsg) (tea.Model, tea.Cmd) {
	// Which rendezvous this came from decides everything below: the watcher to
	// re-arm, the file to consume, and the PROJECT the named session lives in.
	watch, dir := m.watchFor(msg.dir)
	// The watch subscription must be re-armed before anything can return early,
	// or one ignored event retires the fast path for the rest of the run.
	rearm := tea.Cmd(nil)
	if msg.fromWatch {
		rearm = focusRequestWatchCmd(watch, dir, m.pid)
	}

	target, known := m.resolveFocusTarget(m.projectForRendezvous(msg.dir), msg.request.Name)
	switch decideFocusRequest(msg, known, m.deps.Now(), dashmeta.RequestMaxAge) {
	case focusRequestIgnore:
		return m, rearm
	case focusRequestDiscard:
		return m, tea.Batch(rearm, removeFocusRequestCmd(dir, m.pid))
	}

	consume := removeFocusRequestCmd(dir, m.pid)
	if m.cleaningByName(target.name) {
		return m, tea.Batch(rearm, consume)
	}
	// The switch comes FIRST, and it has to: every per-project command closes over
	// m.repo, and the pane that is about to open belongs to the incoming project.
	// It is a no-op for a request in the project already active (switchProject
	// returns early on the already-active path), which is what keeps a click on the
	// current project from tearing down the pane it is aiming at.
	cmds := []tea.Cmd{rearm, consume}
	if target.project != "" {
		var switchCmds []tea.Cmd
		m, switchCmds = m.switchProjectCmds(target.project)
		cmds = append(cmds, switchCmds...)
	}

	// Move the cursor onto the clicked session, ahead of all three honor
	// branches below (pane already installed, live host, stopped host): each one
	// opens the same session, so which branch handles the pane says nothing
	// about where the list should be pointing.
	//
	// Deliberately NOT schedulePreview, even though that is the funnel every
	// key-driven move uses. A preview here would arm a dial for the very session
	// the branches below are already dialing explicitly, and the loser of that
	// race gets Stolen — see previewOnNextLoad's doc comment and openPaneFor's
	// clear of it. selectSessionIndex + resetDetailScroll + rememberSelectedRow
	// is exactly the part of that funnel a click wants: the selection, the
	// viewport reset the selection implies, and the memory.
	if i, ok := m.sessionIndex(target.name); ok {
		// A click is explicit user intent naming one session, so it outranks an
		// in-progress filter: selectSessionIndex leaves the selection alone when
		// the filter HIDES the target (by design, for cursor moves), which would
		// silently drop the one thing this request is for. Resetting first also
		// leaves the list showing the row the pane belongs to, rather than a
		// query written against a different question.
		if m.list.FilterState() != list.Unfiltered || m.list.SettingFilter() {
			m.list.ResetFilter()
		}
		m.selectSessionIndex(i)
		m.resetDetailScroll()
		m.rememberSelectedRow()
	} else if len(m.sessions) == 0 {
		// The switch above cleared m.sessions, so there is nothing to select
		// yet — hand the name to the load that switchProjectCmds just dispatched
		// (see selectOnNextLoad, and Update's sessionsLoadedMsg case).
		//
		// Only the sessions-cleared shape arms it. A populated list that does not
		// contain the name is a session discovery has not caught up with in the
		// ACTIVE project, and the open below handles it from the on-disk target
		// alone; arming for that would move the cursor a whole tick later, after
		// the pane is up, for a row the user is no longer looking at.
		m.selectOnNextLoad = target.name
	}

	// Already the pane installed: there is nothing to open, so this collapses to
	// the reveal-and-focus alone. Handled here rather than through the open path
	// because a second connection to the same session would be opened and then
	// discarded (see applyPaneAttached), leaving the focus to the pane already
	// installed. It goes through showAndFocusPane because the installed pane may
	// be HIDDEN behind the summary, and a click must land the user in a session
	// they can see.
	if m.pane != nil && target.name == m.paneName {
		m.showAndFocusPane()
		m.notify(notifyInfo, focusRequestHonoredStatus(m.leaderKey.name, target.name))
		return m, tea.Batch(cmds...)
	}

	// A stopped host is not restarted by a click, on enter's rule (see
	// openAgent): a row with no live agent opens its details overlay over the
	// Project, and `r` restarts it.
	//
	// previewGen is bumped because a click is the one selection change that
	// skips schedulePreview (see above): a timer armed for the row the user
	// last sat on is still live, and firing now it would install a pane for a
	// row that is no longer selected.
	//
	// No connection is coming to replace a pane installed for another row, so
	// it is detached here: left alone it stays connected but undrawn, holding
	// its host's one client slot and blocking `c` on the row the click chose.
	if !target.hostRunning && !m.restartingRow(target.name) {
		m.previewGen++
		m.dropPaneForOtherRows()
		m.view = viewAgent
		m.focus = focusList
		m.openDetails(levelProject, target.name)
		// The row is looked up for its wording only: a session the daemon
		// paused for usage must not be called stopped here either.
		word := "is stopped"
		for _, row := range m.sessions {
			if row.Name == target.name {
				word = stoppedText(row)
				break
			}
		}
		m.notify(notifyInfo, target.name+" "+word+" · "+m.keyHintFor(actionRestart)+" to restart")
		return m, tea.Batch(cmds...)
	}
	// The same entrance enter uses (connect): the focus moves right and the
	// view to the agent now, at request time, so keys typed while the dial is
	// out land on the placeholder rather than on list bindings. focusRequest
	// only picks the arrival's wording.
	return m, tea.Batch(append(cmds, m.connect(paneOpenRequest{
		name:         target.name,
		worktree:     target.worktree,
		scratch:      target.scratch,
		focusRequest: true,
	}))...)
}

// focusRequestHonoredStatus is the notification for a honored click. It is
// deliberately focusPane's own wording — the same string focusPane and
// togglePane build inline — because the user has arrived in the session with
// the keyboard live however they got there, and the one thing they need is the
// chord that gives it back.
func focusRequestHonoredStatus(leader, name string) string {
	return "input goes to " + name + " · " + keyName(leader, levelAgent, actionGoBack) + " to return"
}

// maybeOpenInitialPane opens the startup pane requested via
// Deps.InitialSessionName (afk new's TTY handoff, or `afk dash --open
// <name>`) once BOTH the first discovery load AND the first tea.WindowSizeMsg
// have arrived: the pane needs concrete cols/rows (only known after the first
// resize) and the session's worktree (only known after the first load resolves
// it), and either can arrive first. It runs the attempt at most once —
// initialPaneAttempted is set as soon as both conditions hold, whether or not
// the session was actually found — so a session that never shows up (e.g. a
// race with a still-writing host.json) surfaces one error rather than retrying
// on every subsequent refresh tick.
//
// A session whose host is not running yet is restarted first: the handoff
// from `afk new` lands here microseconds after the host was spawned, so
// "not running yet" is the common case, not an error. This is a surface-open
// request, not a user's enter, so the "enter never restarts" rule does not
// apply.
func (m Model) maybeOpenInitialPane() (tea.Model, tea.Cmd) {
	if m.initialSession == "" || m.initialPaneAttempted {
		return m, nil
	}
	if !m.firstLoadDone || !m.ready {
		return m, nil
	}
	// The creation form can be opened (n) before the first load + first size
	// both arrive; a startup pane must never open behind it (the form and the
	// pane are mutually exclusive in the main region, and a pane appearing
	// under a modal form would be invisible until the form closes). The
	// attempt is consumed, not deferred: nothing re-triggers this method
	// after the form closes.
	if m.formOpen {
		m.initialPaneAttempted = true
		m.notify(notifyWarn, "startup pane for "+m.initialSession+" skipped — creation form is open")
		return m, nil
	}
	m.initialPaneAttempted = true
	s, ok := m.sessionByName(m.initialSession)
	if !ok {
		m.notify(notifyError, "could not open "+m.initialSession+": no such session found")
		return m, nil
	}
	if m.cleaningSessions[cleanIDFor(s)] {
		m.notify(notifyWarn, "can't open "+s.Name+" — it's being cleaned")
		return m, nil
	}
	// Move the cursor onto the named session before dialing it. The pane is
	// drawn only for the selected row (paneForSelection), and the first load
	// leaves the cursor on row 0, so without this a startup pane for any other
	// row connects and is never shown: the right side draws row 0's overview,
	// "opened X · enter/f to type in it" focuses row 0 instead of X, and the
	// connection holds X's one client slot behind it. Found by walking through
	// `afk dash --open` against a built binary (docs/decisions.md, T0.6).
	//
	// It is the same three calls a click-to-focus makes, for the same reason
	// (see applyFocusRequest): the selection, the viewport reset a selection
	// change implies, and the memory, but NOT schedulePreview, which would arm
	// a second dial for the session the open below is already dialing. No
	// previewGen bump is needed here as it is there: New arms no startup
	// preview when a startup pane is requested, so no timer is live yet.
	if i, ok := m.sessionIndex(s.Name); ok {
		m.selectSessionIndex(i)
		m.resetDetailScroll()
		m.rememberSelectedRow()
	}
	// --open lands in the Agent with the keyboard on it (docs/dash-design.md
	// §5): the request is the owner's, so it is a focus gesture and the
	// arrival carries focusOnArrival. A start that is not --open never gets
	// here with StartAgent, and a Model built without a StartLevel keeps the
	// older unfocused open.
	toAgent := m.deps.StartLevel == StartAgent
	if toAgent && (s.Pending || isWorktreeFault(s.Kind)) {
		// No host to restart and no agent to land in: a create still in
		// flight, or a worktree with no session of its own. --open shows
		// the session's details instead, as enter does (sessionEnterTarget).
		// A stopped session is not here: --open restarts it first, below.
		m.view = viewAgent
		m.openDetails(levelProject, s.Name)
		return m, nil
	}
	if !s.HostRunning {
		m.notify(notifyInfo, "restarting "+s.Name+"…")
		if toAgent {
			// Focus moves at request time, as connect does: a failed restart
			// hands it back through handBackUnansweredFocus.
			m.view = viewAgent
			m.focus = focusRight
		}
		return m, m.startRestart(paneOpenRequest{
			name:           s.Name,
			worktree:       s.WorktreePath,
			scratch:        s.Scratch,
			focusOnArrival: toAgent,
		})
	}
	if toAgent {
		return m, m.connect(paneOpenRequest{
			name:     s.Name,
			worktree: s.WorktreePath,
		})
	}
	return m, m.openPaneFor(paneOpenRequest{
		name:     s.Name,
		worktree: s.WorktreePath,
	})
}

// schedulePreview invalidates any pending preview and, when the newly-selected
// row can actually be previewed, arms a fresh one for it.
//
// The POINTER receiver is not a style choice and must not be "simplified" to a
// value receiver. applyKeyAction has a value receiver, but its m is an
// addressable local, so Go takes &m implicitly and the mutation persists into
// the Model it returns — exactly how the list moves in applyKeyAction. With a value
// receiver the previewGen bump would be written to a copy and thrown away, every
// arriving timer would carry a generation matching the (never-advanced) Model,
// and the feature would misfire in a way no other check catches.
//
// It bumps previewGen on EVERY path, refusals included. A refusal that skipped
// the bump would leave the previous row's timer live, so moving from a running
// session onto a stopped one would still open the session the cursor just left.
func (m *Model) schedulePreview() tea.Cmd {
	m.previewGen++
	s, ok := m.selected()

	// The detail viewport's scroll offset is keyed on the session it belongs
	// to (see detailViewportFor's doc): every KEY-driven path that can change
	// the selection lands here, so the reset happens once, in this funnel,
	// rather than at each of schedulePreview's call sites. rebuildListItems
	// makes the same call for the tick-driven paths.
	//
	// Deliberately NOT paired with the syncDetailViewport that rebuildListItems
	// runs alongside its reset. A reset leaves the offset at 0, which is in
	// range for a viewport of any content length, so the previous session's
	// line count lingering on the Model viewport cannot produce a wrong clamp
	// here. Both parties that would care re-sync before they act anyway —
	// renderDetailsOverlay sizes its own copy every frame, and scrollDetail syncs
	// ahead of every key — so composing a body in this funnel would only cost
	// a render nobody reads, on the hot path of every cursor move.
	m.resetDetailScroll()
	m.followSelection()

	if !ok {
		return nil
	}

	// Remember the row the cursor settled on. This funnel is the right place
	// because every KEY-driven selection change lands here — all twelve call
	// sites: Update's sessionsLoadedMsg case (the post-switch preview) and its
	// list.FilterMatchesMsg case, applyKeyAction's six cursor-move actions (up,
	// down, page up, page down, top, bottom), and the four filter paths
	// (startSessionFilter, clearSessionFilter, and sessionFilterKey's two
	// returns). There is no mouse-driven row selection to miss: mouse.go only
	// forwards events into the pane and never touches m.list. The one selection
	// change that does NOT come through here is a click-to-focus request, which
	// moves the cursor onto the session it is opening and calls
	// rememberSelectedRow itself precisely because it must NOT arm a preview —
	// see applyFocusRequest. Conversely the TICK-driven path deliberately does
	// not land here — applyLoaded/rebuildListItems preserve the INDEX, so a
	// session vanishing under the cursor changes the selected row with no key
	// press, and recording there would overwrite the user's chosen name with
	// whatever the list shuffled under it.
	//
	// The call runs AHEAD of every refusal below and reads m.list.SettingFilter()
	// here rather than moving or reusing the SettingFilter refusal further
	// down — that one stays exactly where it is, because it guards DIALING and
	// must keep landing after the previewGen bump. This is a second, independent
	// read of the same predicate: `/` jumps the selection to the first match and
	// every key typed into the prompt moves it again, so a query would otherwise
	// be remembered as a choice the user never made. The guard stays at this call
	// site rather than inside the helper because it is about how THIS funnel is
	// reached: applyFocusRequest has no filter prompt open by the time it records
	// (it resets an applied filter first), and a helper that refused mid-filter
	// would be refusing for a reason that does not exist there.
	if !m.list.SettingFilter() {
		m.rememberSelectedRow()
	}

	// The overview is drawn, so there is no agent pane to fill in: a dial now
	// would hold the host's one client slot for a pane nobody can see. Like
	// every refusal here it lands after the bump, so the previous row's timer
	// dies too. applyPreviewTimer re-checks it at fire time.
	if m.view == viewOverview {
		return nil
	}

	// A placeholder has no worktree and no host yet — the same refusal
	// connectSelected makes.
	if s.Pending {
		return nil
	}
	// A SEARCH must not connect to anything. `/` jumps the selection to the
	// first row and every key typed into the prompt changes the match set, so
	// arming here would dial whichever session currently matches — stealing it
	// from a afk attach in another terminal — as a side effect of typing a
	// query. Accepting or cancelling the filter arms normally, because by then
	// the prompt is closed and this is false. Like every refusal here, it lands
	// AFTER the bump: the previous row's timer still has to die.
	if m.list.SettingFilter() {
		return nil
	}
	// A worktree-side fault has no host to dial either — again the same
	// refusal connectSelected makes, and not covered by the HostRunning check
	// below: that field is advisory, so a recycled PID would arm a timer for
	// a row whose worktree may not even exist.
	if isWorktreeFault(s.Kind) {
		return nil
	}
	// Already on screen: re-dialing it would open a second connection to its host
	// and get the pane the user is looking at Stolen out from under itself.
	if m.pane != nil && s.Name == m.paneName {
		return nil
	}
	// Never restart a host from a preview: a cursor move must not start processes.
	if !s.HostRunning {
		return nil
	}
	// A clean stops this session's host and deletes its worktree.
	if m.cleaningSessions[cleanIDFor(s)] {
		return nil
	}
	// Only an EXPLICIT open blocks arming, though applyPreviewTimer refuses to DIAL
	// while ANY open is outstanding. The asymmetry is deliberate: refusing to arm
	// during a preview's own dial would leave a dead spot — a move onto C while B's
	// preview is dialing would arm nothing, B's dial would then complete, and C would
	// never be previewed until the user moved again. Arming is free and steals
	// nothing; only the dial can, and applyPreviewTimer is where that is refused.
	// It has to be: previewDelay and previewDialWait are both 300ms, so a timer
	// armed now fires at roughly the moment an outstanding preview dial exhausts
	// its budget — the two cannot be ordered, and this used to claim they could
	// (previewDelay was 750ms when that reasoning was written). The re-check at
	// fire time is what actually holds.
	if m.attachInFlight.name != "" && !m.attachInFlight.preview {
		return nil
	}
	return previewTimerCmd(m.previewGen, s.Name)
}

// followSelection makes the pane follow a list-focused selection change: a
// pane that no longer belongs to the selected row is detached at once, so it
// never holds its host's single client slot while undrawn, and a lost-pane
// note for a row the cursor left is dropped. With the right side focused the
// selection is pinned (rebuildListItems), so there is nothing to follow.
func (m *Model) followSelection() {
	if m.focus != focusList {
		return
	}
	m.dropPaneForOtherRows()
}

// dropPaneForOtherRows is followSelection without its focus gate, for a
// selection change that moves the focus itself (a click on a stopped row):
// the pane is detached because nothing will ever draw it, whoever held the
// keyboard before the move.
func (m *Model) dropPaneForOtherRows() {
	if m.pane != nil && !m.paneForSelection() {
		m.forgetPane()
	}
	if s, ok := m.selected(); ok && s.Name != m.paneLost.name {
		m.paneLost = lostPane{}
	}
}

// rememberSelectedRow records the row the cursor is on as this project's
// remembered selection, keyed by selectionKey. It writes memory only:
// dirtySelections is the queue the selection tick and quit flush through
// Deps.RememberSelection, so nothing here touches the disk or needs to fail.
//
// The `!=` guard is what keeps that queue honest — re-recording the name
// already remembered would mark the project dirty and buy a disk write for a
// change that is not one, on every path that lands back on the same row.
//
// Everything selectable IS recorded, including the rows that arm no preview —
// stopped, pending, worktree-fault, and the row already in the pane. The user
// pointed at it, and restoring the cursor to it is not the same act as dialing
// it. The one soft edge is a pending placeholder: its name is openScratch's
// guess, which applyScratchDirs may correct once the directory listing lands,
// so remembering it can leave a phantom that a later restore fails to find and
// falls back to row 0 for.
//
// The POINTER receiver is required for the same reason schedulePreview's is:
// both callers hold an addressable local Model, so Go takes &m implicitly and
// the map writes persist into the Model they return. A value receiver would
// discard them into a copy with nothing else failing.
//
// It re-reads m.selected() rather than taking a Session, so the two callers
// cannot disagree with the list about which row is selected — applyFocusRequest
// has just moved the cursor with selectSessionIndex, and schedulePreview has
// already read the same row. An empty list, or a filter narrowed to nothing,
// records nothing.
func (m *Model) rememberSelectedRow() {
	s, ok := m.selected()
	if !ok {
		return
	}
	key := m.selectionKey()
	if m.rememberedSelection == nil {
		m.rememberedSelection = map[string]string{}
	}
	if m.rememberedSelection[key] != s.Name {
		m.rememberedSelection[key] = s.Name
		if m.dirtySelections == nil {
			m.dirtySelections = map[string]string{}
		}
		m.dirtySelections[key] = s.Name
	}
}

// applyPreviewTimer opens the previewed pane, or drops the timer.
//
// Every condition here is re-checked rather than trusted from schedule time,
// because previewDelay is long enough for all of them to change. The cleaning check is
// the sharpest: confirming a clean returns mode to inputNormal, so without it a
// timer armed before the clean would reach openPaneFor and write "cleaning X —
// cannot open it" into the status line — error text produced by nothing but a
// cursor move.
//
// The NAME check is load-bearing and is NOT redundant with the generation check.
// applyLoaded replaces m.sessions wholesale on every 2s tick and
// rebuildListItems holds the same INDEX, so a session appearing or disappearing changes which
// session the cursor points at with no cursor move, hence no generation bump.
// Without this check that tick alone would open a pane onto a row the user never
// selected.
//
// Right focus gates it whichever surface holds the keyboard: a timer armed
// less than previewDelay before `f` focused the detail pane is still live,
// and without this gate it would fire mid-read, open a pane and replace the
// diff the user just brought the keyboard to. schedulePreview only bumps
// previewGen on a cursor move, and focusing the detail pane is not one.
func (m Model) applyPreviewTimer(msg previewTimerMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.previewGen {
		return m, nil
	}
	if m.modal != inputNormal || m.formOpen || m.focus == focusRight || m.detailsOpen {
		return m, nil
	}
	// The overview is drawn: a preview has nothing to fill in. Re-checked
	// here, not only at arming, because a timer armed in the agent view can
	// still be pending when the view changes, and this is the last gate
	// before the dial.
	if m.view == viewOverview {
		return m, nil
	}
	// ANY outstanding dial, preview or explicit (unlike schedulePreview, which only
	// refuses to arm for an explicit one — see there). Dialing a host that already
	// has a connection in flight makes it kick the first client, so this is the last
	// gate a PREVIEW passes before dialing.
	//
	// It does not establish an at-most-one-attach-in-flight invariant, and no comment
	// here should claim one: attachInFlight is a single slot, so two explicit opens
	// naming different sessions both dispatch and the second simply overwrites the
	// record. That is docs/performance/pane-preview-races.md's B1, still open.
	if m.attachInFlight.name != "" {
		return m, nil
	}
	s, ok := m.selected()
	if !ok || s.Name != msg.name {
		return m, nil
	}
	if m.cleaningSessions[cleanIDFor(s)] {
		return m, nil
	}
	if m.pane != nil && s.Name == m.paneName {
		return m, nil
	}
	// Re-verified from the fresh value, never from anything captured at schedule
	// time: a tick can have flipped any of them since. The kind is in here for
	// the reason this whole block exists — applyLoaded replaces m.sessions
	// wholesale and clampCursor holds the same INDEX, so a session that
	// disappeared between arming and firing can leave the cursor on a fault
	// row the timer was never armed for.
	if s.Pending || isWorktreeFault(s.Kind) || !s.HostRunning {
		return m, nil
	}
	return m, m.openPaneFor(paneOpenRequest{
		name:     s.Name,
		worktree: s.WorktreePath,
		preview:  true,
		wait:     previewDialWait,
	})
}

// markHostStopped records that a pane-open attempt proved no host is serving
// the named session: it flips the cached HostRunning immediately, and remembers
// the PID that was disproved so the next discovery pass cannot re-assert it
// (see applyDeadHostOverrides).
//
// A HostPID of 0 means there is no host.json to be stale about, so discovery
// will report the session as stopped on its own and nothing needs remembering.
//
// Fault rows are skipped for the same reason every other by-name lookup skips
// them: under the fault/scratch name collision this loop would otherwise flip
// HostRunning on whichever row sorted first, leaving a stale restart hint on a
// row that never had a host to disprove.
func (m *Model) markHostStopped(name string) {
	for i := range m.sessions {
		if m.sessions[i].Name != name || isWorktreeFault(m.sessions[i].Kind) {
			continue
		}
		m.sessions[i].HostRunning = false
		if pid := m.sessions[i].HostPID; pid != 0 {
			if m.deadHostPIDs == nil {
				m.deadHostPIDs = make(map[string]int, 1)
			}
			m.deadHostPIDs[name] = pid
		}
		// An IN-PLACE field edit still has to be published to the list, whose
		// items carry their own copy of each Session: without this the row keeps
		// drawing the live-host dot and — because selected() reads the item —
		// the very next enter retries the connect this correction just proved
		// impossible. Every mutation of m.sessions ends in a rebuild, this one
		// included.
		m.rebuildListItems(m.listSelection())
		return
	}
}

// applyDeadHostOverrides re-applies markHostStopped's corrections over a fresh
// discovery result and drops the ones that have lapsed.
//
// This is what makes a correction stick long enough to be acted on. Discovery
// re-derives HostRunning from host.json plus a signal-0 check on every tick, so
// a stale host.json naming a recycled PID reports "running" again within
// milliseconds. Without this, the "r to restart" hint would be
// contradicted by the next tick and enter would retry the same impossible
// connection forever instead of restarting.
//
// The correction is keyed on the exact PID that was disproved, so it lapses on
// its own as soon as host.json names a different one. A genuinely new host —
// restarted from here, or started by a `afk new` in another terminal — is
// therefore trusted immediately, with no explicit invalidation to get wrong.
func (m *Model) applyDeadHostOverrides() {
	if len(m.deadHostPIDs) == 0 {
		return
	}
	stillDead := make(map[string]int, len(m.deadHostPIDs))
	for i := range m.sessions {
		s := &m.sessions[i]
		pid, ok := m.deadHostPIDs[s.Name]
		if !ok || s.HostPID != pid {
			continue
		}
		s.HostRunning = false
		stillDead[s.Name] = pid
	}
	m.deadHostPIDs = stillDead
}

// hasInFlightWork reports whether any background operation this dashboard
// dispatched is still outstanding — the one question the spinner's tick loop
// exists to answer. It reads the trackers each async family already keeps
// for its OWN correctness (pendingSpawns, cleaningSessions, attachInFlight)
// rather than inventing a duplicate of facts three of them already track;
// otherOpsInFlight covers the remaining three async dispatches
// (restartHostCmd, restartChildCmd, writeLabelCmd) that keep no record of
// their own. planApproving is the dashboard's plan approve (planapprove.go),
// whose commit and push can outlast the working notice's toast, and
// usageRefreshing is the dashboard's usage reading (usagerefresh.go), which
// runs the agent's CLI and can take up to a minute.
//
// attachInFlight is read with its preview half excluded on purpose. A
// preview dial (applyPreviewTimer) sets no status text, so a spinner tied to
// it would render next to whatever unrelated status happened to already be
// on screen.
func (m Model) hasInFlightWork() bool {
	return len(m.pendingSpawns) > 0 ||
		len(m.cleaningSessions) > 0 ||
		(m.attachInFlight.name != "" && (!m.attachInFlight.preview || m.attachInFlight.claimedPreview)) ||
		m.otherOpsInFlight > 0 ||
		m.planApproving ||
		m.taskStatusSetting ||
		m.usageRefreshing
}

// startSpinnerIfNeeded arms the spinner's tick loop and is a no-op if the
// loop is already running (spinner.TickMsg is self-perpetuating once
// started; see its case in Update, which is also the only place the loop
// stops). It is called from exactly one place, tickMsg's case: every
// dispatch site that ADDS an in-flight operation (submitNewForm,
// confirmClean, startRestart (the `r` key), and the rest
// hasInFlightWork reads) is also under a test that runs its returned Cmd
// directly and type-asserts a specific message out of it — batching a second
// Cmd in at THAT point would turn the assertion's target into a
// tea.BatchMsg. The discovery ticker already returns a multi-Cmd batch
// nobody asserts a single type against, which is what makes it the one safe
// place to attach a genuinely independent Cmd; the cost is that the spinner
// does not begin ANIMATING until the next tick (at most m.interval, 2s by
// default) even though it is visible — at its static first frame — the
// instant the help row starts showing it (see renderHelp).
func (m *Model) startSpinnerIfNeeded() tea.Cmd {
	if m.spinnerActive {
		return nil
	}
	m.spinnerActive = true
	return m.spinner.Tick
}

// noteOtherOpStarted records that a restartHostCmd, restartChildCmd, or
// writeLabelCmd dispatch is now in flight (see otherOpsInFlight). It is a
// plain mutation, not a Cmd: the spinner's tick is armed separately, from
// tickMsg's case, so that adding this bookkeeping never changes the shape of
// the Cmd these dispatch sites return (see startSpinnerIfNeeded).
func (m *Model) noteOtherOpStarted() {
	m.otherOpsInFlight++
}

// endOtherOp reverses noteOtherOpStarted. Called unconditionally at the top
// of the three message cases those dispatches report back through
// (hostRestartedMsg, restartChildRequestedMsg, labelSavedMsg), before any of
// their own branching, so every outcome — success, failure, or a
// project-generation mismatch — balances the count exactly once.
func (m *Model) endOtherOp() {
	if m.otherOpsInFlight > 0 {
		m.otherOpsInFlight--
	}
}

// addPendingSpawn records that a background create for name is in flight.
// Re-submitting a name already pending is a no-op rather than a second row.
func (m *Model) addPendingSpawn(name string) {
	for _, p := range m.pendingSpawns {
		if p == name {
			return
		}
	}
	m.pendingSpawns = append(m.pendingSpawns, name)
}

// clearPendingSpawn drops name's placeholder, both from the pending list and
// from the sessions slice it was merged into. Removing it from m.sessions here
// (rather than waiting for the next discovery pass to rebuild the slice) is what
// keeps a failed create from leaving a permanent "creating…" row behind.
func (m *Model) clearPendingSpawn(name string) {
	kept := m.pendingSpawns[:0]
	for _, p := range m.pendingSpawns {
		if p != name {
			kept = append(kept, p)
		}
	}
	m.pendingSpawns = kept

	sessions := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.Pending && s.Name == name {
			continue
		}
		sessions = append(sessions, s)
	}
	m.setSessions(sessions)
}

// markCleaning records that an in-process clean of id is in flight. The set is
// allocated lazily, mirroring deadHostPIDs.
func (m *Model) markCleaning(id cleanID) {
	if m.cleaningSessions == nil {
		m.cleaningSessions = make(map[cleanID]bool, 1)
	}
	m.cleaningSessions[id] = true
}

func (m *Model) clearCleaning(id cleanID) {
	delete(m.cleaningSessions, id)
}

// openPaneFor is the single gate in front of attachPaneCmd, so every entrance to
// it consults the in-flight clean set: connect (enter, ctrl+t, a click), the
// restart chain (hostRestartedMsg), the post-create auto-open
// (spawnFinishedMsg), and the startup pane (maybeOpenInitialPane). Guarding
// only the keypress left three paths open — most sharply `r` on a stopped
// session followed by `c`,`y` while the restart Cmd is still in flight, which
// lands a live pane on the host the clean is about to SIGKILL.
//
// Pane ARRIVAL is guarded separately, in applyPaneAttached: a pane dispatched
// before the clean was confirmed is still in flight when this gate has already
// been passed, so the arriving connection is dropped there rather than installed.
//
// It returns a nil Cmd on refusal, which every caller can return directly.
//
// Being that single gate is also why the preview invalidation lives HERE rather
// than at each call site: bumping previewGen once covers the restart chain
// (hostRestartedMsg), the post-create auto-open (spawnFinishedMsg), connect,
// the startup pane, and any entrance added later. Without it a preview armed one
// settle ago lands on top of an attach the user just asked for, opens a SECOND
// connection to the same host, and the host sends Stolen to the first — closing
// the very pane that was requested.
func (m *Model) openPaneFor(req paneOpenRequest) tea.Cmd {
	m.previewGen++
	if req.preview {
		req.previewGen = m.previewGen
	}
	// The single gate is also why the project-generation STAMP lives here (and in
	// restartHostCmd) rather than at each request-literal site: Update is
	// synchronous, so at dispatch time m.projectGen is by definition the project
	// this request was made in, and no future entrance can lose it. The one
	// path where the stamp would lie — the restart chain, whose request is
	// rebuilt from a message that crossed the async boundary — is dropped before
	// it gets here (see the hostRestartedMsg case).
	req.gen = m.projectGen
	if m.cleaningByName(req.name) {
		m.notify(notifyWarn, "can't open "+req.name+" — it's being cleaned")
		return nil
	}
	// Recorded only on the path that actually returns a Cmd, and therefore only where
	// a paneAttachedMsg will be delivered to clear it again. Recording it above the
	// refusal would leak: the refusal returns no Cmd, so applyPaneAttached never
	// runs, and the record would stay set for the life of the process — silently
	// disabling every future preview.
	m.attachInFlight = inFlightOpen{
		name:           req.name,
		preview:        req.preview,
		focusOnArrival: req.focusOnArrival,
	}
	// A pending post-switch arm is superseded HERE for a non-preview request,
	// rather than left to schedulePreview's own "an explicit open blocks arming"
	// refusal (model.go, schedulePreview) to cover: that refusal only holds while
	// THIS dial is still outstanding, so a same-project focus-request click
	// racing behind an earlier switcher switch's still-in-flight load — which
	// switchProjectCmds never sees, because same-project is its own no-op path —
	// would otherwise survive past this open's own paneAttachedMsg arrival and
	// get consumed by that switch's load once it lands, arming a preview that
	// steals the pane back from the session this explicit request just asked
	// for. req.preview is excluded because the preview's own dial
	// (applyPreviewTimer's openPaneFor call) also funnels through here, and by
	// then previewOnNextLoad has already been consumed — Update's
	// sessionsLoadedMsg case clears it before ever calling schedulePreview.
	if !req.preview {
		m.previewOnNextLoad = false
	}
	return m.attachPaneCmd(req)
}

// cleanFailedStatus summarizes a clean that failed partway through, naming the
// ARCHIVE before the error text whenever the archive step got far enough to
// produce one.
//
// The ordering rationale is cleanSucceededStatus's, and it applies with more
// force here, not less. CleanSession archives `.afk/{name}/` and then REMOVES
// the source before it touches the worktree (internal/session/clean.go), so a
// failure at `git worktree remove` leaves the only copy of that session's
// notepads in the archive — and unlike the success path, the row survives to keep
// suggesting nothing happened. The toast clips at 3 lines, so the archive leads
// and a long git error is what the clip takes. Any multi-line error folds into
// one line the way the retired shell-out folded its stderr tail.
//
// The archive is named by basename, matching cleanSucceededStatus: the two
// messages describe the same directory and would otherwise disagree about how
// to write it, and the archive root is rediscoverable by listing it.
func cleanFailedStatus(name string, result session.CleanResult, err error) string {
	status := "cleaning " + name + " failed"
	if result.ArchivePath != "" {
		status += " after archiving as " + filepath.Base(result.ArchivePath)
	}
	return status + ": " + strings.ReplaceAll(err.Error(), "\n", " · ")
}

// cleanSucceededStatus summarizes a finished clean, ordered by VALUE rather
// than by the order the steps ran in (the house style, messages.go): outcome,
// then the deleted branch's recovery command, then the archive basename, then
// the cosmetic clauses.
//
// The order is what a reader actually gets. renderToast wraps to
// toastMaxWidth and hard-clips at 3 lines, a full lifecycle clean runs well
// past that, and no other surface keeps a copy — so whatever trails is what
// nobody reads. The recovery command leads because a deleted branch is
// reflog-only and this is the one place its SHA is ever shown; the archive
// follows because only its basename is irrecoverable, the root being one
// listing away; the snapshot commit, the upstack retargeting and the notes are
// all re-derivable from git and are deliberately expendable to the clip.
func cleanSucceededStatus(result session.CleanResult) string {
	// A scratch clean archives nothing — session.cleanScratchSession stops the
	// host and removes .afk/{name}/, and its CleanResult carries only Name.
	// An empty ArchivePath is the honest discriminator (the lifecycle clean
	// archives unconditionally before it can succeed), and without this branch
	// the message would claim an archive with a dangling empty name —
	// cleanFailedStatus above already guards the same field the same way.
	// A worktree-side fault clean also archives nothing, and for the opposite
	// reason: the `.afk/{name}/` it would have archived is either gone with
	// the deleted directory or was never there. ArchiveSkipped is exactly that
	// distinction (internal/session keeps it separate from an empty
	// ArchivePath for this reason), so the two empty-archive cases do not have
	// to share one sentence — "session directory removed" is false here.
	recovery, cosmetic := branchOutcome(result)
	if result.ArchiveSkipped {
		return cleanNotes(joinClauses("cleaned "+result.Name+" — worktree removed; nothing to archive",
			recovery, cosmetic), result)
	}
	if result.ArchivePath == "" {
		return cleanNotes("cleaned "+result.Name+" — session directory removed", result)
	}
	snapshot := ""
	if result.SnapshotCommitted {
		snapshot = "snapshot-committed dirty changes"
	}
	return cleanNotes(joinClauses("cleaned "+result.Name, recovery,
		"archived as "+filepath.Base(result.ArchivePath), snapshot, cosmetic), result)
}

// branchOutcome splits what happened to the branch into the half that has to
// survive the toast's clip and the half that does not, so cleanSucceededStatus
// can place them on opposite sides of the archive.
//
// recovery is the deleted branch and the command that undoes it. A deleted
// branch is only reflog-recoverable, `git branch <name> <sha>` is the whole
// recovery, and this notification is the only place the SHA is ever shown —
// the row it describes is about to vanish from the list and nothing else
// records it. The SHA is abbreviated to 12 hex characters, which resolve
// unambiguously in any repository this side of a million commits, where 40
// would spend a third of a toast line on a handle.
//
// cosmetic is the rest: a KEPT branch (git still has it, so the note is a
// convenience) and the upstack retargeting. A git-spice delete with
// --restack=none moves upstack branches' parent pointer without rebasing them
// and nothing in git shows that it happened, which is why the note exists at
// all — but `gs log short` shows the new topology, so it is re-derivable in a
// way the SHA is not, and it trails.
func branchOutcome(result session.CleanResult) (recovery, cosmetic string) {
	if result.Branch == "" {
		return "", ""
	}
	if !result.BranchDeleted {
		return "", "branch " + result.Branch + " kept"
	}
	recovery = "branch " + result.Branch + " deleted"
	if sha := result.DeletedBranchSHA; sha != "" {
		if len(sha) > 12 {
			sha = sha[:12]
		}
		// Branch is named once, inside the command a reader would actually run —
		// not also in the leading clause. The clause's footprint used to scale
		// with 2x len(Branch); that redundancy is what exhausted the toast's clip
		// on a realistic branch name and lost the SHA (see issues.md
		// post-approval-fixes-2).
		recovery = "branch deleted — recover: git branch " + result.Branch + " " + sha
	}
	if result.RetargetedBranches != "" {
		cosmetic = "upstack retargeted, not restacked: " + result.RetargetedBranches
	}
	return recovery, cosmetic
}

// joinClauses strings the clauses of a composed message together with the
// house divider, dropping the empty ones. Every clause of a clean summary is
// conditional on a CleanResult field, so the alternative is a "· " guarded at
// each append — which is what made the old chain's order impossible to change
// without moving the separators too.
func joinClauses(clauses ...string) string {
	kept := clauses[:0]
	for _, c := range clauses {
		if c != "" {
			kept = append(kept, c)
		}
	}
	return strings.Join(kept, " · ")
}

// cleanNotes folds internal/session's skipped-step notes onto the end of the
// summary. They are the only account of a step that was reported rather than
// run — a refused removal in a foreign worktree, a branch left alone because
// it was unmerged and unconfirmed — and before this they reached no surface at
// all: the in-process clean points both of its deps' streams at a capture
// buffer, so nothing CleanSession writes can reach the terminal.
//
// Last, and newline-flattened, for cleanFailedStatus's reason: the toast clips
// at 3 lines, so whatever trails is what the clip takes, and the notes are the
// most recoverable thing here.
func cleanNotes(status string, result session.CleanResult) string {
	notes := strings.TrimSpace(result.Notes)
	if notes == "" {
		return status
	}
	return status + " · " + strings.ReplaceAll(notes, "\n", " · ")
}

// mergePendingSpawns folds one placeholder row per still-pending spawn into
// m.sessions, and drops any pending name discovery has caught up with.
//
// It must be called from applyLoaded, not just from the keypress that started
// the spawn: applyLoaded replaces m.sessions wholesale on every 2s tick, so a
// one-time merge would survive at most one tick. Placeholders are inserted in
// name order because internal/discover returns sessions sorted by name, and a
// row appended out of order would make the list jump when the real session
// replaces it.
// mergePendingSpawnsNow folds the placeholders in and rebuilds the list in one
// step, for the keypress paths that want the row on THIS frame rather than on
// the next discovery tick.
func (m *Model) mergePendingSpawnsNow() {
	m.rebuildListItems(m.mergePendingSpawns(m.listSelection()))
}

// sel is the selection's position in m.sessions, threaded through and returned
// so applyLoaded rebuilds the list ONCE after every placeholder is spliced in
// rather than per insertion.
func (m *Model) mergePendingSpawns(sel int) int {
	if len(m.pendingSpawns) == 0 {
		return sel
	}
	stillPending := make([]string, 0, len(m.pendingSpawns))
	for _, name := range m.pendingSpawns {
		if _, ok := m.sessionIndex(name); ok {
			continue
		}
		stillPending = append(stillPending, name)
		sel = m.insertPlaceholder(name, sel)
	}
	m.pendingSpawns = stillPending
	return sel
}

// insertPlaceholder splices an inert "creating…" row for name into m.sessions at
// its place in screen order (screenInsertAt: in Working, by name) and returns
// the selection's new position, so it stays on whatever session it was
// pointing at (an insertion at or before it shifts that session down by one).
func (m *Model) insertPlaceholder(name string, sel int) int {
	row := Session{SessionState: discover.SessionState{Name: name}, Pending: true}
	at := screenInsertAt(m.sessions, row)
	m.sessions = append(m.sessions, Session{})
	copy(m.sessions[at+1:], m.sessions[at:])
	m.sessions[at] = row
	if at <= sel {
		sel++
	}
	return sel
}

// sessionIndex and sessionByName skip worktree-side fault rows for attach.go's
// findSessionWorktree reason: a fault row carries no host.json by definition,
// so it can never satisfy a lookup whose callers go on to open, restart, or
// place a pane. A fault row is named after its BRANCH, so it can collide with
// a scratch session's name — and sortSessionStates orders the non-scratch
// fault first, so without the skip the fault row wins the very lookups it can
// never answer. The fault rows themselves are reached by cursor and by
// resolveCleanTarget's (name, kind) match, never by name alone.
func (m Model) sessionIndex(name string) (int, bool) {
	for i := range m.sessions {
		if m.sessions[i].Name == name && !isWorktreeFault(m.sessions[i].Kind) {
			return i, true
		}
	}
	return 0, false
}

// worktreeFor returns the discovered worktree path for name, or "" when
// discovery has not seen it yet — which is the normal case immediately after a
// create finishes. An empty path still reaches a running host: the pane open
// re-reads host.json from it, and its failure guidance degrades to a hint
// without a path rather than failing outright.
func (m Model) worktreeFor(name string) string {
	if i, ok := m.sessionIndex(name); ok {
		return m.sessions[i].WorktreePath
	}
	return ""
}

// sessionByName returns the session named name from the last discovery
// snapshot, or ok=false when it is not present (e.g. the list changed between
// an async pane-open attempt and its result). Fault rows never match — see
// sessionIndex.
func (m Model) sessionByName(name string) (Session, bool) {
	for _, s := range m.sessions {
		if s.Name == name && !isWorktreeFault(s.Kind) {
			return s, true
		}
	}
	return Session{}, false
}

// cleanSelected implements the `c` keybinding: EVERY selected session confirms
// in inputConfirmClean, terminal phases included. cleanRequested blocks `c`
// entirely while a pane is open, so this never runs alongside a session the
// user is watching.
//
// There is no phase-based branch left, and no bare-list branch either. The
// old no-selection path shelled out to `afk clean` with no name, whose stdout
// was discarded the instant the alt-screen was restored — nothing was ever
// readable from it, and the dashboard's own table IS that list, live. So an
// empty selection now just says why the keypress did nothing.
//
// A terminal (done/failed) phase no longer skips the modal. That fast path
// dispatched with no phase captured, and the in-process seam re-checks the
// confirmed phase against status.json at clean time — routing every session
// through here is what guarantees confirmPhase is always the phase the user was
// actually shown, which is the only thing that comparison can be meaningful
// against. Stale rows need no separate case: their Phase is "" and the seam
// treats that as matching only an equally-stale fresh read.
//
// The name/phase are remembered at modal-entry time under the invariant
// documented on confirmTarget — background ticks reshuffle m.sessions while the
// modal is up, so the `y` handler must never re-read the selection.
func (m Model) cleanSelected() (tea.Model, tea.Cmd) {
	s, ok := m.selected()
	if !ok {
		m.notify(notifyWarn, "no session selected — nothing to clean")
		return m, nil
	}
	if s.Pending {
		// Inert, and emphatically not a fall-through to the bare `afk clean`
		// list: cleaning a session whose create is still running mid-flight is
		// the worst possible interpretation of this keypress.
		return m, nil
	}
	if m.cleaningSessions[cleanIDFor(s)] {
		m.notify(notifyWarn, "already cleaning "+s.Name+" — wait for it to finish")
		return m, nil
	}
	m.modal = inputConfirmClean
	// The status is deliberately NOT cleared on entry: the toast composes
	// above the dialog (View), so a clean result that lands while this is up
	// is read immediately — and its archive basename and recovery SHA are
	// reported nowhere else.
	m.confirmTarget = s.Name
	m.confirmPhase = s.Phase
	m.confirmScratch = s.Scratch
	m.confirmKind = cleanTargetKind(s.Kind)
	m.confirmMerge = cleanMergeState{}
	m.confirmDeleteUnmerged = false
	// The merge state the `d` offer is labelled with is requested HERE, once,
	// off the event loop — see cleanMergeStateCmd. Three rows have nothing to
	// ask about: a one-off has no branch by construction; a row whose
	// status.json did not parse has no branch NAME to hand git (internal/state
	// zeroes every status-derived field on a stale read), so asking would run
	// an ancestry check against the empty string; and a stranded
	// `.afk/{name}/` has a branch name but no branch of its OWN — the entry
	// session.Discover reports for it carries Branch "", its teardown deletes
	// nothing, and `d` is inert on it, so an answer here would only label an
	// offer the modal never makes. All three leave the merge state unknown,
	// which is where `d`'s own refusals read it from.
	if !s.Scratch && s.Branch != "" && s.Kind != session.KindStatusWithoutWorktree {
		return m, m.cleanMergeStateCmd(s.Name, s.Branch, s.BaseBranch, m.confirmKind)
	}
	return m, nil
}

// clearCleanConfirm drops every field captured when inputConfirmClean was
// entered. It exists as one function rather than four copies because it is
// called from four exits (esc, n, y, and a dispatching d) and the cost of one
// exit neglecting one field is a leak onto the NEXT session the user confirms
// — a stale merge verdict labels the wrong branch, and a stale
// confirmDeleteUnmerged turns that session's first `d` into an immediate
// unmerged delete with no second confirmation at all.
func (m *Model) clearCleanConfirm() {
	m.confirmTarget = ""
	m.confirmPhase = ""
	m.confirmScratch = false
	m.confirmKind = session.KindHealthy
	m.confirmMerge = cleanMergeState{}
	m.confirmDeleteUnmerged = false
}

// applyCleanMergeState folds an ancestry check's answer into the open modal,
// or drops it.
//
// The drop is the point. The check runs off the event loop, so its answer can
// arrive after the user cancelled that modal and opened another — and the
// merge state is exactly what the modal's first line asserts and what `d`
// branches on, so a misapplied answer would invite a one-press delete of a
// branch nobody checked. The guard is the confirmed identity, all three
// parts the modal captured: an empty confirmTarget means no modal is up at
// all, the kind separates a fault entry from the healthy session sharing its
// name, and scratch-ness separates a scratch modal from that same healthy
// twin — a scratch modal never requests merge state (see cleanSelected), so
// any answer arriving while one is up is some other modal's by construction.
//
// An answer that could not name a base is dropped too, for the reason
// cleanMergeState's doc gives: there is nothing to interpolate, and "unknown"
// is already the safe reading.
func (m Model) applyCleanMergeState(msg cleanMergeStateMsg) (tea.Model, tea.Cmd) {
	if m.confirmTarget == "" || m.confirmScratch || msg.target != m.confirmTarget || msg.kind != m.confirmKind {
		return m, nil
	}
	if msg.base == "" {
		return m, nil
	}
	m.confirmMerge = cleanMergeState{known: true, merged: msg.merged, base: msg.base}
	return m, nil
}

// confirmClean is the clean confirmation's y/Y. There is no argv to build and no
// --force flag to order any more: the clean runs IN-PROCESS through
// cleanSessionCmd, which carries the remembered name and phase to the
// Deps.CleanSession seam and reports back as a cleanFinishedMsg. Both remembered
// fields are read before they are cleared, and the name joins cleaningSessions
// so a second `c` on it cannot dispatch a concurrent clean.
//
// Reaching an answer at all depends on inputConfirmClean outranking normal mode,
// which is what keeps this modal's `n` from colliding with the global New
// binding and opening the creation form mid-confirmation.
//
// Ignored while the unmerged confirmation is up: that footer advertises `d`
// and cancel only, and answering a "these commits will be lost" question with
// a key it does not offer is not an answer to it. The first confirmation's `y`
// is still one esc away, and unchanged — it dispatches ZERO CleanOptions even
// when the modal knows the branch is merged, because the delete offer is
// deleteBranchClean's alone.
func (m Model) confirmClean() (tea.Model, tea.Cmd) {
	if m.confirmDeleteUnmerged {
		return m, nil
	}
	return m.dispatchConfirmedClean(session.CleanOptions{})
}

// deleteBranchClean is the confirmation's d/D (actionDeleteBranch): the same
// clean plus the branch, and the only answer with a second state — on a branch
// this modal knows is merged it dispatches immediately, and on any other it
// swaps the footer to the unmerged confirmation and waits for a second d. It
// is never REFUSED. Deleting an unmerged branch is a legitimate thing to want,
// the commits stay reflog-recoverable, and a refusal would only send the user
// to `git branch -D` with less information than this modal has.
func (m Model) deleteBranchClean() (tea.Model, tea.Cmd) {
	// Two kinds have no branch to delete, so the offer is never made and the
	// key is inert — neither confirmation's hint advertises it, and this is
	// what makes that true. A one-off never had a branch. A stranded
	// `.afk/{name}/` is carried by a worktree checked out on another branch,
	// so the entry session.Discover reports for it carries Branch "" and its
	// teardown runs no git at all; routing `d` on it to the unmerged
	// confirmation would ask the user to accept losing commits that this clean
	// cannot touch.
	if m.confirmScratch || m.confirmKind == session.KindStatusWithoutWorktree {
		return m, nil
	}
	// Merged is the only state that deletes on one press. NOT KNOWN is
	// deliberately treated like NOT MERGED: the ancestry check may still be in
	// flight, and holding the keypress until it lands would make the modal
	// feel wedged while assuming "merged" would delete unreviewed commits on a
	// guess. Routing to the second confirmation costs one keypress and can
	// never be the wrong answer.
	if m.confirmMerge.known && m.confirmMerge.merged {
		return m.dispatchConfirmedClean(session.CleanOptions{DeleteBranch: true})
	}
	if !m.confirmDeleteUnmerged {
		m.confirmDeleteUnmerged = true
		return m, nil
	}
	return m.dispatchConfirmedClean(session.CleanOptions{DeleteBranch: true, ConfirmedUnmerged: true})
}

// cancelClean is the clean confirmation's n/N and esc: back to normal mode with
// no Cmd and nothing dispatched. It answers the unmerged second confirmation
// too — both of that footer's advertised cancels land here, and
// clearCleanConfirm is what guarantees the staged confirmDeleteUnmerged cannot
// leak onto the next modal.
//
// The mouse chord, by contrast, is a rule in this mode rather than an answer: it
// reconfigures the TERMINAL and leaves confirmTarget/confirmPhase/modal
// untouched, so the confirmation is still open and still waiting for y/n
// afterwards. See toggleMouseCapture for why it must be reachable from here.
func (m Model) cancelClean() (tea.Model, tea.Cmd) {
	m.modal = inputNormal
	m.clearCleanConfirm()
	return m, nil
}

// dispatchConfirmedClean is the one exit that acts: it reads the captured
// identity BEFORE clearing it, marks the name in-flight so a second `c` cannot
// start a concurrent clean of it, and hands the whole answer — including the
// branch-deletion bits — to the seam.
//
// Every caller passes a literal CleanOptions, which is what keeps `y`'s
// promise mechanically true: the zero value is the pre-`d` behavior, and `y`
// is the only key that passes it.
func (m Model) dispatchConfirmedClean(opts session.CleanOptions) (tea.Model, tea.Cmd) {
	name, phase := m.confirmTarget, m.confirmPhase
	scratch, kind := m.confirmScratch, m.confirmKind
	m.modal = inputNormal
	m.clearCleanConfirm()
	// The pane on the session being cleaned is detached first: its worktree
	// is about to go, and a pane left attached would show a session the
	// clean is taking apart. Detaching ends nothing (the host is its own
	// process), and a pane on another session is left alone.
	if m.pane != nil && m.paneName == name {
		m.forgetPane()
	}
	m.markCleaning(cleanID{name: name, kind: kind})
	m.notify(notifyInfo, "cleaning "+name+"…")
	return m, m.cleanSessionCmd(name, string(phase), scratch, kind, opts)
}

// restartSelected is `r`, the one restart key. It is live on the session list,
// in the details overlay (detailsRestart), and on the agent placeholder — everywhere except a
// live agent pane, where `r` is typing and forwards to the child (inputPane
// has no rule for it). What it does depends on the row:
//
//   - a stopped row (no host, not pending/fault/cleaning): restart the host
//     straight away through startRestart → restartHostCmd → hostRestartedMsg
//     → openPaneFor, the chain enter used to run. No confirmation: respawning
//     a host that is not running destroys nothing.
//   - a running row the dashboard shows as WEDGED: the existing confirm flow
//     (restartWedgedSelected), because that restart kills a live process.
//   - anything else: inert.
//
// From the right side the restart carries focusOnArrival, so the agent lands
// focused (or behind the overview, when that is the view). From the list it
// is selectionBound instead: the keyboard stays on the list, and the arriving
// pane is dropped if the cursor has moved off the row by then. From the
// details overlay (opened at Project, so the keyboard is on the list) it is
// selectionBound too, and the arrival closes the overlay and opens the Agent
// while the overlay is still up on the row (detailsRestart).
//
// restartableStopped refuses a row whose restart is already in flight, whose
// dial is outstanding, or whose pane is installed, so a second `r` — or an `r`
// on a row discovery has not yet re-read as running — cannot spawn a second
// host that would steal the first one's client.
func (m Model) restartSelected() (tea.Model, tea.Cmd) {
	s, ok := m.selected()
	if !ok {
		return m, nil
	}
	if !s.HostRunning && m.cleaningSessions[cleanIDFor(s)] {
		// The clean stops this host for good; respawning it would put a live
		// child in a worktree being deleted. Said rather than silent, because
		// on a stopped row `r` is otherwise exactly the key that acts.
		m.notify(notifyWarn, "can't restart "+s.Name+" — it's being cleaned")
		return m, nil
	}
	if m.restartableStopped(s) {
		right := m.focus == focusRight
		m.notify(notifyInfo, "restarting "+s.Name+"…")
		return m, m.startRestart(paneOpenRequest{
			name:           s.Name,
			worktree:       s.WorktreePath,
			scratch:        s.Scratch,
			focusOnArrival: right,
			selectionBound: !right,
		})
	}
	if !s.HostRunning {
		return m, nil
	}
	return m.restartWedgedSelected()
}

// restartableStopped is "`r` would respawn this row's host": no host running,
// a row that is a session (not pending, not a worktree fault, not being
// cleaned), and nothing of ours already connecting to it — no restart in
// flight, no dial outstanding, no pane installed.
func (m Model) restartableStopped(s Session) bool {
	if s.HostRunning || s.Pending || isWorktreeFault(s.Kind) || m.cleaningSessions[cleanIDFor(s)] {
		return false
	}
	if m.restartingRow(s.Name) || m.attachInFlight.name == s.Name {
		return false
	}
	return m.pane == nil || m.paneName != s.Name
}

// restartWedgedSelected implements the wedge-recovery keybinding (W6.T3):
// gate entry on the row's OWN wedge verdict — showsWedged is the exact
// predicate views.go's renderRow uses for the WEDGED badge, so this key can
// never fire on a session the dashboard is not already showing as wedged.
//
// A healthy session, an empty selection, and a placeholder row (Pending) are
// all inert with no status write: the key was never advertised for any of
// them (only the wedge banner and the WEDGED badge invite it), so unlike
// cleanSelected's empty-list case there is nothing to explain.
func (m Model) restartWedgedSelected() (tea.Model, tea.Cmd) {
	s, ok := m.selected()
	if !ok || s.Pending {
		return m, nil
	}
	if m.cleaningSessions[cleanIDFor(s)] {
		return m, nil
	}
	if _, wedged := showsWedged(s, m.deps.Now()); !wedged {
		return m, nil
	}
	// A one-off session never reaches the confirmation. The host's restart
	// resumes the conversation via the session id recorded in status.json
	// (internal/host/restart.go), and a scratch session has no status.json by
	// design — so confirming would close the pane and steal the client slot
	// only for the restart to fail. Unlike the healthy/empty/Pending refusals
	// above, this one explains itself: the WEDGED badge advertises `r` on this
	// very row, so silence would read as a broken key.
	if s.Scratch {
		m.notify(notifyWarn, s.Name+" is a one-off session — restart can't resume it · "+m.agentKeyHint()+" to show the agent, "+keyName(m.leaderKey.name, levelProject, actionClean)+" to clean")
		return m, nil
	}
	m.modal = inputConfirmRestart
	m.confirmRestartTarget = s.Name
	m.confirmRestartWorktree = s.WorktreePath
	// The question and its steal warning are the dialog's (confirmRestartDialog,
	// views.go); the toast is left for outcomes.
	return m, nil
}

// confirmRestart is the restart confirmation's y/Y, mirroring confirmClean:
// the two modals share a spec shape, so esc/n/N cancel with no Cmd and every
// unclaimed key is swallowed rather than dismissing the prompt.
//
// It closes any pane already open on the target FIRST, synchronously and
// via a plain Close (a Detach, not the frame this confirms) — never left for
// the restart itself to discover as a stolen connection. The alternative,
// dialing a second connection while the first stays attached, would have the
// host steal that first connection out from under this dashboard's own pane
// as a side effect of a restart THIS process asked for, and surface as an
// unexplained "session ended" a moment before the real "restart requested"
// status lands. Closing it here means the only thing the user sees is the
// restart status this function itself sets.
func (m Model) confirmRestart() (tea.Model, tea.Cmd) {
	name, worktree := m.confirmRestartTarget, m.confirmRestartWorktree
	m.modal = inputNormal
	m.confirmRestartTarget = ""
	m.confirmRestartWorktree = ""
	// Re-validated at confirm time, not just at entry: the 2s discovery ticker
	// keeps running under this prompt, so the session can recover (its child
	// speaks, noteOutput clears wedged_since) or vanish (a clean) while the user
	// reads the question. The gate is the same showsWedged predicate that
	// admitted the keypress, against the CURRENT snapshot — a healthy child must
	// never be signal-laddered off a stale prompt.
	if !m.sessionStillWedged(name) {
		m.notify(notifyWarn, name+" is no longer wedged — restart cancelled")
		return m, nil
	}
	if m.paneName == name {
		m.forgetPane()
	}
	m.notify(notifyInfo, "requesting restart for "+name+"…")
	m.noteOtherOpStarted()
	return m, m.restartChildCmd(name, worktree)
}

// cancelRestart is the restart confirmation's n/N and esc: back to normal mode
// with the captured target dropped and no signal sent.
func (m Model) cancelRestart() (tea.Model, tea.Cmd) {
	m.modal = inputNormal
	m.confirmRestartTarget = ""
	m.confirmRestartWorktree = ""
	return m, nil
}

// openLabelForm implements the `l` keybinding: opens a single-field inline
// prompt (bubbles/textinput — the same component newform.go's nameInput
// uses) to set or clear the selected session's display label. Pre-filled
// with the session's current label (empty when unset), so pressing enter
// with no edits round-trips the existing value rather than clearing it.
//
// Inert for an empty selection and a Pending placeholder row, on the same
// predicate openNewForm/openScratch/restartWedgedSelected all use: a
// placeholder has no real `.afk/{name}/` directory yet (Session's own doc
// comment — only Name is populated on one), so there is nothing to write a
// label sidecar into.
//
// Inert for a worktree-side fault row for a stronger reason than "nothing to
// write": there is nowhere it would be RIGHT to write. A missing-directory
// row's path does not resolve, and a worktree-with-no-session row's does — into
// a checkout afk never created, where WriteLabel's MkdirAll would leave a
// `.afk/` directory behind. Not creating state in a worktree afk does not
// own is the same rule internal/session's foreign-worktree teardown follows
// when it refuses to snapshot-commit one.
func (m Model) openLabelForm() (tea.Model, tea.Cmd) {
	s, ok := m.selected()
	if !ok || s.Pending || isWorktreeFault(s.Kind) {
		return m, nil
	}
	m.modal = inputEditLabel
	m.labelTarget = s.Name
	m.labelWorktree = s.WorktreePath
	in := textinput.New()
	in.Placeholder = "display label (blank clears it)"
	in.CharLimit = 128
	in.SetValue(s.Label)
	in.CursorEnd()
	if m.width > 4 {
		// The dialog's content width less the input's own "> " prompt and a
		// cell of slack, so the field never spills into the box's padding.
		in.SetWidth(m.dialogContentWidth() - 4)
	}
	m.labelInput = in
	m.clearStatus()
	return m, m.labelInput.Focus()
}

// cancelLabelEdit is the label prompt's esc: it closes with no write.
//
// Unlike the two y/n confirmations, this mode falls through to its component
// rather than swallowing unclaimed keys — there is text to type, and every key
// the spec does not claim reaches the textinput.
func (m Model) cancelLabelEdit() (tea.Model, tea.Cmd) {
	m.modal = inputNormal
	m.labelTarget = ""
	m.labelWorktree = ""
	m.notify(notifyInfo, "label edit cancelled")
	return m, nil
}

// submitLabelEdit is the label prompt's enter. It reads labelTarget and
// labelWorktree as captured at open time, never a fresh m.selected() — the same
// reshuffling-tick reason confirmTarget's doc gives.
func (m Model) submitLabelEdit() (tea.Model, tea.Cmd) {
	name, worktree, label := m.labelTarget, m.labelWorktree, m.labelInput.Value()
	m.modal = inputNormal
	m.labelTarget = ""
	m.labelWorktree = ""
	m.notify(notifyInfo, "saving label for "+name+"…")
	m.noteOtherOpStarted()
	return m, m.writeLabelCmd(worktree, name, label)
}

// labelSavedMsg carries the outcome of one label write (writeLabelCmd) back
// to Update.
type labelSavedMsg struct {
	name string
	err  error
}

// writeLabelCmd runs Deps.WriteLabel off the UI thread, on the same
// off-loop-I/O discipline restartChildCmd documents just above: even a local
// file write is I/O, and I/O on the synchronous Update path can stall the
// whole dashboard on a slow or contended filesystem.
func (m Model) writeLabelCmd(worktree, name, label string) tea.Cmd {
	write := m.deps.WriteLabel
	return func() tea.Msg {
		err := write(worktree, name, label)
		return labelSavedMsg{name: name, err: err}
	}
}

// restartChildRequestedMsg carries the outcome of one restart request to a
// wedged session's host (see restartChildCmd). Unlike hostRestartedMsg
// (which re-spawns a STOPPED host process this dashboard never had a
// connection to), the host here was already running. The three outcomes the
// task contract names map onto the two fields: err non-nil is FAILED (a dial
// or write failure, or the host's own refusal read back off the wire),
// restarted is RESTARTED (the host confirmed the respawn with an empty
// RestartResult frame), and neither is REQUESTED — the frame was delivered
// but no answer came within restartReplyTimeout, which is all an older host
// can ever report.
type restartChildRequestedMsg struct {
	name      string
	restarted bool
	err       error
}

// restartChildCmd sends the request off the UI thread and reports the
// outcome as a restartChildRequestedMsg — the whole reason this exists
// rather than a plain call in confirmRestart is the one
// this task forbids outright: dialing a socket is I/O, and I/O on the
// synchronous Update path can block the whole dashboard on a host that never
// answers, which is exactly the failure mode this wave exists to recover
// from, not add a second copy of.
func (m Model) restartChildCmd(name, worktree string) tea.Cmd {
	send := m.deps.SendRestart
	return func() tea.Msg {
		restarted, err := send(worktree, name)
		return restartChildRequestedMsg{name: name, restarted: restarted, err: err}
	}
}

// restartReplyTimeout bounds how long defaultSendRestart waits for the
// host's RestartResult after delivering the Restart frame. It must outlast
// the host's whole shipped signal ladder (~9s against a child that ignores
// SIGINT and SIGTERM; internal/host's restartLadder) plus the PTY drain and
// respawn, or a slow-but-successful restart would be misreported as
// outcome-unknown; 15s leaves that margin. Expiry is NOT a failure — an
// older host never sends the frame at all — so it degrades to the
// "requested" outcome. A package-level var, not a const, for the same
// test-compression seam as hostFrameWriteTimeout.
var restartReplyTimeout = 15 * time.Second

// defaultSendRestart is Deps.SendRestart's real implementation: dial the
// session's host at the same socket path hostpane.go's hostSocketPath
// resolves for the pane itself, then delegate the frame exchange to
// sendRestartAndAwaitResult.
//
// This is a NEW connection, not the pane's existing one — paneHandle
// (this file) has no method that reaches a live pane's socket, by design
// (see its own doc comment: it mirrors termpane.Pane's surface exactly, and
// growing it is out of this task's Files list). A host serves exactly one
// client at a time and a new dial steals whichever one was attached — a
// `afk attach` in another terminal included, which is why the restart
// confirmation prompt warns about it — so confirmRestart detaches this
// dashboard's own pane on the target FIRST, precisely so this dial never
// steals from the process that issued it.
func defaultSendRestart(worktree, name string) (bool, error) {
	socket, err := hostSocketPath(worktree, name)
	if err != nil {
		return false, err
	}
	conn, err := host.Dial(socket, host.DefaultAttachWait)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	return sendRestartAndAwaitResult(conn)
}

// sendRestartAndAwaitResult writes one Restart frame and reads frames until
// the host answers with a RestartResult, the connection ends, or
// restartReplyTimeout expires. Factored over a bare net.Conn so the whole
// matrix is testable against net.Pipe without a socket or a host.
//
// The write carries hostFrameWriteTimeout, closing the third client→host
// write path (Objective 5's intent is EVERY such path): host.Dial sets no
// deadline of its own, so without this a host that stopped reading its
// socket would park the restart Cmd's goroutine forever. Data frames are
// skipped rather than surfaced — on this freshly attached connection they
// are child output and the host's human-readable "[afk] ..." notify lines,
// a rendering for panes, not the protocol. A Stolen frame or an unanswered
// wait degrades to (false, nil): the frame was delivered, the outcome went
// to someone else or was never reported, and claiming either success or
// failure would be inventing information.
func sendRestartAndAwaitResult(conn net.Conn) (bool, error) {
	_ = conn.SetWriteDeadline(time.Now().Add(hostFrameWriteTimeout))
	if err := host.WriteFrame(conn, host.Restart, nil); err != nil {
		return false, fmt.Errorf("sending the restart request: %w", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(restartReplyTimeout))
	for {
		typ, payload, err := host.ReadFrame(conn)
		if err != nil {
			return false, nil
		}
		switch typ {
		case host.RestartResult:
			if len(payload) == 0 {
				return true, nil
			}
			return false, errors.New(string(payload))
		case host.Stolen:
			return false, nil
		}
	}
}

// applyLoaded folds a discovery result into the Model, clamping the cursor
// to the new session count. On error it keeps the previously-displayed
// sessions (a transient git hiccup should not blank the dashboard) and records
// the error for the header and for notifyDiscoveryError, which the caller runs
// after this returns.
func (m Model) applyLoaded(msg sessionsLoadedMsg) Model {
	m.lastLoad = msg.at
	if msg.err != nil {
		m.loadErr = msg.err.Error()
		// The FIRST failure of a run is what the header counts from: a
		// re-stamp on every failing pass would report the age of the latest
		// attempt, which is always ~one interval, rather than how long the
		// list on screen has been frozen.
		if m.loadFailingSince.IsZero() {
			m.loadFailingSince = msg.at
		}
		return m
	}
	m.loadErr = ""
	m.loadFailingSince = time.Time{}
	// The selection's position is read BEFORE the slice is replaced and applied
	// after, which is what carries it across a wholesale replacement — the
	// clamp-the-old-index behaviour the cursor field had.
	sel := m.listSelection()
	previous := m.sessions
	// Screen order before sel is applied, for setSessions' reason.
	m.sessions = inScreenOrder(msg.sessions)
	// Both of these re-apply model-side corrections over a result that replaced
	// m.sessions wholesale, and both must run on EVERY pass or they last one tick.
	sel = m.mergePendingSpawns(sel)
	m.applyDeadHostOverrides()
	// AFTER applyDeadHostOverrides, never before: that override corrects
	// HostRunning model-side, and diffing a raw discovery `true` against the
	// previous snapshot's corrected `false` reads as a host flapping dead and
	// alive on every single tick.
	m.notifySessionTransitions(previous)
	m.rebuildListItems(sel)
	return m
}

// hasReading reports whether a row carries a status reading the snapshot diff
// can compare against another tick's.
//
// A stale read leaves Phase "" and never assigns AwaitingInput (discover
// returns before both — see showsAwaitingInput's doc), so those zero values
// are the ABSENCE of a reading, not a state. Treating them as a state is how
// one transient stale tick makes a finished session appear to re-cross into
// Done the moment it recovers. Pending placeholders carry only a Name, and
// worktree faults have no session behind them; same answer.
//
// The exclusion covers the whole row, HostRunning included, even though that
// field survives an unreadable status.json: a host death hidden behind a
// stale tick surfaces on the next clean pass, which is late rather than wrong.
func hasReading(s Session) bool {
	return !s.Pending && !s.StatusStale && !isWorktreeFault(s.Kind)
}

// notifySessionTransitions diffs the outgoing session snapshot against the one
// just installed and raises a background notification per state change.
//
// TRANSITIONS ONLY. A session that IS finished, waiting or hostless is not
// news — it would be news every 2s. A session seen for the FIRST time is not a
// transition either, whatever state it is in: otherwise opening the dashboard
// on a directory of finished sessions fires a burst of completions for work
// that ended days ago.
//
// Rows are matched by cleanIDFor's (name, identity-relevant kind) pair rather
// than by name, because a healthy session and a stranded status directory can
// share a name and one row's phase would read as the other's transition. A
// duplicate key within one snapshot keeps the first row, as every other
// by-name lookup here does.
//
// Awaiting-input is suppressed for the session SHOWING in the pane: every
// conductor turn boundary flips awaiting_input, so the session the user is
// looking straight at would toast continuously. A pane behind the overview
// is not being looked at, so its session notifies like any
// other.
//
// Host death is warn and never worse, because HostRunning comes from an
// advisory kill(pid, 0) and PIDs get recycled, so it can simply be wrong.
// Its own three suppressions are in reportsHostDeath.
//
// Accepted imperfection: after a discovery outage recovers the surviving
// snapshot is several ticks old, so a session that finished mid-outage
// notifies late. Late is correct here; silent would not be.
func (m *Model) notifySessionTransitions(previous []Session) {
	if len(previous) == 0 {
		return
	}
	before := make(map[cleanID]Session, len(previous))
	for _, s := range previous {
		if !hasReading(s) {
			continue
		}
		id := cleanIDFor(s)
		if _, dup := before[id]; dup {
			continue
		}
		before[id] = s
	}
	for _, s := range m.sessions {
		if !hasReading(s) {
			continue
		}
		old, ok := before[cleanIDFor(s)]
		if !ok {
			continue
		}
		// A phase COMPARISON rather than !old.Phase.Terminal(): done→failed is
		// a real crossing worth reporting, and comparing the phases reports it
		// as the failure it is while still saying nothing about a session that
		// sat in the same terminal phase across both snapshots.
		if s.Phase.Terminal() && s.Phase != old.Phase {
			if s.Phase == state.PhaseFailed {
				m.notifyBackground(notifyError, bgFailed, s.Name+" failed")
			} else {
				m.notifyBackground(notifySuccess, bgFinished, s.Name+" finished")
			}
		}
		if showsAwaitingInput(s) && !showsAwaitingInput(old) && !m.paneShowing(s.Name) {
			m.notifyBackground(notifyWarn, bgAwaitingInput, s.Name+" is awaiting input")
		}
		if m.reportsHostDeath(old, s) {
			m.notifyBackground(notifyWarn, bgHostDied, s.Name+" host died")
		}
	}
}

// paneShowing reports whether the named session is the one on screen in the
// pane — open AND not behind the overview. paneName is "" with no pane open
// and no session is named "", so the pane handle itself need not be
// consulted.
func (m *Model) paneShowing(name string) bool {
	return name == m.paneName && m.view == viewAgent
}

// reportsHostDeath reports whether the host loss between these two readings of
// one session is news. Three ordinary host losses are not:
//
//   - A terminal new phase. A session reaching Done normally loses its host,
//     and "a finished" followed by "a host died" describes one event twice.
//   - A clean in progress. internal/session/clean.go stops the host before
//     `git worktree remove`, and the archive copy, commit and removal
//     routinely straddle a tick, so the row is still in the snapshot with
//     HostRunning going true→false and no terminal phase to catch it (a
//     scratch session never has one). The user's own "cleaning x…" toast is
//     live, so this would defer and resurface after "cleaned x", naming a
//     session that no longer exists.
//   - A death already reported. Two paths tell the user first and then call
//     markHostStopped: a failed pane open (paneAttachFailedStatus) and a pane
//     whose session ended under it (paneExitedMsg's "session ended"). Its
//     in-place edit to m.sessions usually leaves no transition for the diff
//     to find at all — but that is another function's detail, and the kind a
//     refactor drops without noticing. deadHostPIDs holds only PIDs one of
//     those paths actually disproved and applyDeadHostOverrides has already
//     dropped the lapsed ones, so an exact match is that same death, stated
//     here rather than inferred.
func (m *Model) reportsHostDeath(old, s Session) bool {
	if !old.HostRunning || s.HostRunning {
		return false
	}
	if s.Phase.Terminal() {
		return false
	}
	if m.cleaningSessions[cleanIDFor(s)] {
		return false
	}
	if pid, ok := m.deadHostPIDs[s.Name]; ok && pid == s.HostPID {
		return false
	}
	return true
}

// notifyDiscoveryError raises a whole-dashboard discovery failure as an
// error-kind notification, on the TRANSITION into the failure and again only
// when the error text changes. Discovery re-runs every m.interval (2s by
// default), so notifying per failing pass would be a toast that never clears.
//
// It compares against notifiedLoadErr rather than leaning on notify's own
// identical-text skip, because that skip only protects the timestamp: any
// notification in between — a keypress the user made, a clearStatus from a
// modal — would leave the next failing pass looking like a new message and
// re-float a toast for a failure already reported.
//
// It is called from the sessionsLoadedMsg case just after applyLoaded rather
// than from inside applyLoaded. The transition is visible from either place,
// and keeping it at the call site means how this notification is routed can
// change without touching the function that computes the snapshot.
//
// It is background-origin: the failure arrives on the discovery tick, not on
// anything the user did, so it waits behind a live toast and coalesces with
// whatever else the tick surfaced (notification.go's flushBackground) rather
// than overwriting a message the user just triggered. The suppression above
// is unaffected — notifiedLoadErr records that the failure was HANDED to the
// arbiter, and deferral never drops a batch, so handed is as good as shown.
func (m *Model) notifyDiscoveryError() {
	if m.loadErr == "" {
		m.notifiedLoadErr = ""
		return
	}
	if m.loadErr == m.notifiedLoadErr {
		return
	}
	m.notifiedLoadErr = m.loadErr
	m.notifyBackground(notifyError, bgDiscoveryError, "discovery error: "+m.loadErr)
}

// notifyStackError raises a git-spice stack-topology refresh failure
// (stackLoadedMsg.Err) as a background, error-kind notification, on the
// transition into failure and again only when the error text changes — the
// same one-per-distinct-failure rule notifyDiscoveryError enforces above,
// because stackCmd re-runs on the same 2s cadence via projectCmds and an
// unsuppressed repeat would be a toast that never stops.
//
// This adds a notification; it does not move one. m.stack.Err is unchanged
// and the Stack tab (stackPanel.Render, stack.go) keeps rendering it exactly
// as before.
//
// Background-origin for the same reason notifyDiscoveryError is: the failure
// arrives on the refresh tick, not on anything the user did.
func (m *Model) notifyStackError() {
	if m.stack.Err == "" {
		m.notifiedStackErr = ""
		return
	}
	if m.stack.Err == m.notifiedStackErr {
		return
	}
	m.notifiedStackErr = m.stack.Err
	m.notifyBackground(notifyError, bgStackError, stackFailedStatus(m.stack.Err))
}

// notifyBranchesError raises a GENUINE plain-git branch-listing failure as a
// background, error-kind notification, on the transition into failure and
// again only when the error text changes — plainBranchesCmd fires on the
// same 2s cadence as stackCmd, so this needs the identical suppression.
//
// Deliberately does not fire for a trunk-resolution failure with branches
// intact. plainBranchesCmd's two error paths (stack.go) differ in exactly one
// observable way by the time the message reaches here: a genuine listing
// failure never populates branches at all (msg.branches stays the nil zero
// value), while a repo that resolved its branch list but failed to resolve a
// trunk (no origin/HEAD, no main/master) always carries whatever the listing
// actually returned. The latter is not a real failure — see
// TestPlainBranchesKeptWhenTrunkResolutionFails for the existing, unaffected
// handling of that data — so it stays silent rather than erroring on every
// startup in a repo shaped that way.
func (m *Model) notifyBranchesError(msg plainBranchesLoadedMsg) {
	if msg.err == nil || msg.branches != nil {
		m.notifiedBranchesErr = ""
		return
	}
	if msg.err.Error() == m.notifiedBranchesErr {
		return
	}
	m.notifiedBranchesErr = msg.err.Error()
	m.notifyBackground(notifyError, bgBranchesError, branchesFailedStatus(msg.err))
}

// notifyScanError raises a project-scan config-parse failure (projectScanMsg.err)
// as a background, error-kind notification, on the transition into failure and
// again only when the error text changes — mirroring notifyDiscoveryError and
// notifyStackError above. scanTickMsg re-drives applyProjectScan on every scan
// interval (see scanTickMsg's doc in messages.go), so an unsuppressed repeat
// would toast on every pass for a config file that stays malformed; before the
// footer's status row was removed that repeat was harmless because the sticky
// text never changed, but notify now re-stamps identical text once its own
// toast has expired, so an unguarded call here would toast forever.
//
// errText is passed rather than the message itself: the caller (applyScanResult)
// already holds msg.err and calls this once on the failure path and once with ""
// on the success path immediately below it, which needs no msg to construct.
func (m *Model) notifyScanError(errText string) {
	if errText == "" {
		m.notifiedScanErr = ""
		return
	}
	if errText == m.notifiedScanErr {
		return
	}
	m.notifiedScanErr = errText
	m.notifyBackground(notifyError, bgScanError, "project scan failed: "+strings.ReplaceAll(errText, "\n", " · "))
}

// projectCmds is the per-project refresh trio — discovery, the git-spice stack
// topology, the plain-branch list — plus the build read when a loader is wired
// (builddata.go), or nothing at all when there is no active project.
//
// The suppression keys on the active repo and never on len(projects). That
// distinction is the whole reason this helper exists: a Model built straight from
// New with no project list still has a real repo (that is every test in this
// package, and it is what `afk dash` was before projects existed at all), and
// it has to keep refreshing. What must NOT refresh is a dashboard launched
// outside any repo, where the active index is -1 — there discovery runs git
// against a directory that is not a repo and parks a permanent error in the
// footer, underneath the empty state.
func (m Model) projectCmds() []tea.Cmd {
	if !m.hasActiveProject() {
		return nil
	}
	cmds := []tea.Cmd{m.loadCmd(), m.stackCmd(), m.plainBranchesCmd()}
	// The build read joins the trio only when a loader is wired. A nil entry
	// would be harmless to tea.Batch, but the callers that take these
	// unbatched (switchProjectCmds) hand the slice on as it is.
	if build := m.buildCmd(); build != nil {
		cmds = append(cmds, build)
	}
	if ps := m.planStatusCmd(); ps != nil {
		cmds = append(cmds, ps)
	}
	// The cursor's task's tokens, so its row total follows the ledger as
	// the build loop records, and the overlay opens without waiting.
	if tt := m.taskTokensCmd(); tt != nil {
		cmds = append(cmds, tt)
	}
	return cmds
}

// hasActiveProject reports whether there is a project for the per-project
// commands to run against. An empty repo means nothing was ever resolved; an
// active index of -1 means the dashboard launched outside any repo and no scan
// has landed yet (D7's first-project promotion happens in applyProjectScan).
//
// It deliberately says nothing about len(m.projects): see projectCmds.
func (m Model) hasActiveProject() bool {
	return m.repo != "" && m.activeProject >= 0
}

// activeProjectPath is the active project's path, or "" when the active index
// points outside the list. Both the launched-outside-a-repo case (index -1) and
// a Model handed a repo but no project list read as "" here, which is why the
// per-project suppression above keys on hasActiveProject instead: the second of
// those two must keep refreshing.
func (m Model) activeProjectPath() string {
	if m.activeProject < 0 || m.activeProject >= len(m.projects) {
		return ""
	}
	return m.projects[m.activeProject].Path
}

// selectionKeyFor is the pure core of Model.selectionKey, split out so New can
// compute the startup key from deps.Projects/deps.ActiveProjectIndex and repo
// before a Model exists to call a method on — the two call this same function
// so they cannot drift apart.
//
// It keys on the RESOLVED project path (projects[active].Path) rather than
// repo because repo is the launch cwd cleaned by resolveProject
// (projectflag.go) with nothing more: no toplevel resolution, no symlink
// resolution. A launch from a repo subdirectory, or a symlinked one (macOS's
// /tmp resolving to /private/tmp), leaves repo disagreeing with Project.Path
// until applyScanResult heals it — which typically lands after the first
// discovery load (resolveCwdRepoRoot in dash.go is what performs that
// resolution before New is ever called for the pinned entry). Keying on the
// resolved path instead makes startup, recording, and restore agree from the
// first frame.
//
// repo is the fallback when no resolved path exists yet: active out of
// range — including the empty-Projects, launched-outside-a-repo case where
// active is -1 — falls back to it so there is still some key to record
// under.
func selectionKeyFor(projects []project.Project, active int, repo string) string {
	if active >= 0 && active < len(projects) {
		return projects[active].Path
	}
	return repo
}

// launchProjectPath is the resolved path of the project the dashboard launched
// INTO — the `--project PATH` target or the cwd repo, both of which reach New
// as projects[active] — or "" when there is no such entry (launched outside any
// repo, index -1, before the first scan lands).
//
// It is NOT selectionKeyFor, and the difference is the whole reason it exists:
// that function falls back to the launch repo so there is always SOME key to
// remember a session under, where an open count keyed on a repo spelling no
// project entry carries (a subdirectory or symlinked launch) would tally a
// project the switcher can never rank, and rank the real entry as never opened.
// Empty here means "record nothing", which costs one uncounted open of a
// project that has no entry to be ordered among anyway.
//
// Pure, per this repo's convention for anything that decides off the
// environment: the impure part (resolving the cwd, reading --project) happened
// in dash.go long before New, so the whole matrix is table-testable.
func launchProjectPath(projects []project.Project, active int) string {
	if active >= 0 && active < len(projects) {
		return projects[active].Path
	}
	return ""
}

// recordProjectOpen tallies one DELIBERATE open of the project at path: the
// in-memory count the switcher orders by, and a pending increment for the next
// flush. It writes memory only — dirtyOpens is the queue the discovery tick and
// the quit flush drain through Deps.RecordProjectOpen — so nothing here touches
// the disk or needs to fail.
//
// Both maps are bumped even when no seam is wired, matching
// rememberSelectedRow: ordering is an in-memory decision, and a Model with no
// persistence still has to rank the project the user just opened above the ones
// they did not.
//
// Exactly two acts reach here, and both are the user naming a project: the
// launch target (New) and a switcher choice that actually switched. A
// re-select of the active project no-ops before reaching here,
// and applyScanResult's promotion of m.projects[0] never calls it at all — that
// is automatic reconciliation for a project that vanished, so counting it would
// let a project the user never chose climb the ordering every time their active
// one disappeared.
//
// The POINTER receiver is required for the same reason rememberSelectedRow's
// is: the map writes have to land on the Model the caller returns, not on a
// copy Go discards.
func (m *Model) recordProjectOpen(path string) {
	if path == "" {
		return
	}
	if m.projectOpens == nil {
		m.projectOpens = map[string]int{}
	}
	m.projectOpens[path]++
	if m.dirtyOpens == nil {
		m.dirtyOpens = map[string]int{}
	}
	m.dirtyOpens[path]++
}

// projectOpensFlushedMsg reports that one open-count flush finished. err is the
// first write error it hit, carried for completeness only: Update's case for
// this message drops it, on selectionFlushedMsg's reasoning — an unwritable
// state directory must not produce a toast on a 2s ticker for a feature
// the user never asked about.
type projectOpensFlushedMsg struct{ err error }

// flushProjectOpensCmd persists a set of pending per-project open increments
// off the UI thread, in flushSelectionsCmd's shape and under the same two
// constraints.
//
// entries is COPIED at Cmd-construction time because the returned closure runs
// on a bubbletea goroutine while Update keeps recording opens into the live
// Model.dirtyOpens on the UI goroutine — closing over the caller's map would be
// a concurrent map read/write, a hard runtime throw rather than a stale value.
//
// And the whole set goes through ONE Cmd, writing SEQUENTIALLY, because
// tea.Batch runs each member on its own goroutine while every write here is a
// read-modify-rename of a single shared file: two concurrent writers each add
// one to the count they read, and whichever renames last erases the other's
// increment. The caller has already cleared its pending set by then, so nothing
// re-dirties the lost one.
//
// n calls for a count of n, not one call carrying n, because the seam's
// contract is "record one open" (Deps.RecordProjectOpen) — the on-disk total is
// the one thing this package never claims to know, since another afk process
// may have written it since startup.
func flushProjectOpensCmd(fn func(string) error, entries map[string]int) tea.Cmd {
	if fn == nil || len(entries) == 0 {
		return nil
	}
	snapshot := make(map[string]int, len(entries))
	for path, n := range entries {
		snapshot[path] = n
	}
	return func() tea.Msg {
		var first error
		for path, n := range snapshot {
			for range n {
				if err := fn(path); err != nil && first == nil {
					first = err
				}
			}
		}
		return projectOpensFlushedMsg{err: first}
	}
}

// selectionKey binds selectionKeyFor to this Model's own fields. Every read
// and write of rememberedSelection goes through this (or selectionKeyFor
// directly, at the one site — New — where no Model exists yet to call it on).
func (m Model) selectionKey() string {
	return selectionKeyFor(m.projects, m.activeProject, m.repo)
}

// rendezvousDirs lists every click-to-focus directory this dashboard is
// responsible for: the launch repo's first, then one per project the scan
// resolved, deduplicated.
//
// The dedupe is the load-bearing part, and it has three consumers that would
// each go wrong on their own: the tick would poll the launch directory twice
// (two messages for one click, the second re-deciding a request the first
// consumed), the scan would write its entry twice, and quit would remove it
// twice. Project-list order rather than map order, so all three are
// deterministic.
func (m Model) rendezvousDirs() []string {
	var dirs []string
	seen := make(map[string]bool, len(m.rendezvous)+1)
	if m.gitCommonDir != "" {
		dirs = append(dirs, m.gitCommonDir)
		seen[m.gitCommonDir] = true
	}
	for _, p := range m.projects {
		rz, ok := m.rendezvous[p.Path]
		if !ok || rz.dir == "" || seen[rz.dir] {
			continue
		}
		seen[rz.dir] = true
		dirs = append(dirs, rz.dir)
	}
	return dirs
}

// rendezvousProjects is the set of projects that already have a rendezvous —
// what the scan needs in order to skip the rev-parse and the watch creation for
// them. Captured at Cmd-creation time in this package's standing style, so the
// closure reads no Model state.
//
// A project whose resolution FAILED is absent from it, so the next pass retries:
// a directory becomes a repo the moment someone runs `git init` in it, and a
// rendezvous that could not be created is not a permanent verdict.
//
// The same reasoning covers a rendezvous whose DIRECTORY resolved but whose
// fsnotify registration failed (a nil watch): it is reported as unheld so the
// next pass retries the registration, because poll-only is a degradation to
// recover from, not a verdict either — descriptor exhaustion passes, permissions
// get fixed. The launch directory's entry is exempt: its watch is nil BY DESIGN
// (Model.focusWatch already covers that directory), so retrying it would
// re-resolve and re-register the launch repo on every pass forever.
func (m Model) rendezvousProjects() map[string]bool {
	if len(m.rendezvous) == 0 {
		return nil
	}
	held := make(map[string]bool, len(m.rendezvous))
	for path, rz := range m.rendezvous {
		if rz.watch == nil && rz.dir != m.gitCommonDir {
			continue
		}
		held[path] = true
	}
	return held
}

// focusRequestPollCmds is the fallback sweep, one Cmd per rendezvous directory.
// They belong in the TICK's top-level batch (see Init's comment on why nesting
// hides a Cmd from the tests that assert it was dispatched).
func (m Model) focusRequestPollCmds() []tea.Cmd {
	dirs := m.rendezvousDirs()
	cmds := make([]tea.Cmd, 0, len(dirs))
	for _, dir := range dirs {
		cmds = append(cmds, focusRequestPollCmd(dir, m.pid))
	}
	return cmds
}

// projectForRendezvous names the project whose rendezvous is dir — the answer to
// "which project did this request arrive in", which the request itself cannot
// give (see focusRequestMsg.dir).
//
// An empty result means "no project attribution", which is the single-project
// dashboard's ordinary state as well as a message built before any scan landed:
// applyFocusRequest resolves it against the ACTIVE project, which is exactly the
// behavior there was when a dashboard had one rendezvous.
func (m Model) projectForRendezvous(dir string) string {
	if dir == "" {
		return ""
	}
	// The ACTIVE project wins a SHARED directory. Two project entries can resolve
	// to one git common dir (a pinned worktree beside its scanned main checkout —
	// the case issues.md's W5.T3 entry confirmed on this disk), and the request
	// file cannot say which was meant, so list order used to attribute the click
	// to the scanned sibling and switch the user away from the project they were
	// standing in. Either owner opens the same session (both enumerate one repo),
	// so the tie-break that never switches is the one that cannot be wrong.
	if active := m.activeProjectPath(); active != "" {
		if rz, ok := m.rendezvous[active]; ok && rz.dir == dir {
			return active
		}
	}
	for _, p := range m.projects {
		if rz, ok := m.rendezvous[p.Path]; ok && rz.dir == dir {
			return p.Path
		}
	}
	return ""
}

// watchFor resolves the rendezvous a message from dir belongs to: the watcher to
// re-arm and the directory to read and consume the request from.
//
// The launch directory is answered from focusWatch rather than from the map, for
// the reason the map's own doc comment gives — its watcher lives in that field
// and deliberately not in the map. A directory with no rendezvous left (its
// project left the search roots while the message was in flight) yields a nil
// watcher, retiring that subscription instead of resurrecting it.
func (m Model) watchFor(dir string) (focusWatcher, string) {
	if dir == "" || dir == m.gitCommonDir {
		return m.focusWatch, m.gitCommonDir
	}
	for _, rz := range m.rendezvous {
		if rz.dir == dir {
			return rz.watch, rz.dir
		}
	}
	return nil, dir
}

// applyRendezvous folds one scan pass's resolved rendezvous set into the Model:
// keep what is still in the project list, adopt what the pass resolved for
// projects that appeared, and tear down what belongs to projects that are gone.
//
// Teardown is both halves or neither. A watcher left open leaks a kqueue
// descriptor; an entry left behind is worse than a leak, because `afk notify`
// reads it and switches the user to a tmux pane whose dashboard is no longer
// watching that project at all.
//
// Watchers arrive on the message because creating one is I/O (a directory
// creation plus an fsnotify registration) and Update performs none by package
// contract — the scan Cmd is where the work belongs. The consequence is that two
// overlapping passes can both resolve the same new project, so an incoming
// duplicate for a project that already has one is CLOSED here rather than
// dropped on the floor.
func (m Model) applyRendezvous(msg projectScanMsg) (Model, []tea.Cmd) {
	if msg.err != nil {
		return m, nil
	}
	next := make(map[string]focusRendezvous, len(m.projects))
	var cmds []tea.Cmd
	for _, p := range m.projects {
		if held, ok := m.rendezvous[p.Path]; ok {
			// A registration RETRY that landed: rendezvousProjects reports a held
			// nil-watch rendezvous as unheld so the scan re-resolves it, and a pass
			// that finally produced a live watcher upgrades the entry here — armed
			// and entry-written exactly as a first adoption is (the entry write is
			// an idempotent rewrite of the same payload, and this branch runs at
			// most once per project: an upgraded entry is held from then on). Kept
			// as-is otherwise, so a retry that failed again changes nothing and a
			// duplicate live watcher for an already-live entry is closed, not
			// leaked.
			if inc, dup := msg.rendezvous[p.Path]; dup && inc.watch != nil {
				if held.watch == nil {
					next[p.Path] = inc
					cmds = append(cmds,
						writeDashEntryCmd(m.deps.WriteDashEntry, inc.dir, m.dashEntry),
						focusRequestWatchCmd(inc.watch, inc.dir, m.pid))
					continue
				}
				_ = inc.watch.Close()
			}
			next[p.Path] = held
			continue
		}
		if inc, ok := msg.rendezvous[p.Path]; ok {
			next[p.Path] = inc
			cmds = append(cmds, writeDashEntryCmd(m.deps.WriteDashEntry, inc.dir, m.dashEntry))
			// Adoption must ARM the watcher, not merely hold it. A focusWatcher is
			// inert until a focusRequestWatchCmd goroutine blocks on its Events
			// channel (the same dispatch Init performs for the launch watcher and
			// applyFocusRequest performs to re-arm), so a watcher adopted without one
			// is a kqueue descriptor plus two goroutines that never deliver a click —
			// the fast path silently dead for every project but the launch one, with
			// the 2s poll masking it. nil for a watch-less rendezvous (the launch
			// directory's entry, or a project whose fsnotify registration failed),
			// which tea.Batch drops; the fallback poll carries those.
			if arm := focusRequestWatchCmd(inc.watch, inc.dir, m.pid); arm != nil {
				cmds = append(cmds, arm)
			}
		}
	}
	for path, held := range m.rendezvous {
		if _, kept := next[path]; kept {
			continue
		}
		if held.watch != nil {
			_ = held.watch.Close()
		}
		// The launch directory is exempt: this dashboard is still running in it, and
		// Init's entry there advertises the pane the user is looking at.
		if held.dir != m.gitCommonDir {
			cmds = append(cmds, removeDashEntryCmd(m.deps.RemoveDashEntry, held.dir))
		}
	}
	// A resolution for a project the union did not keep (pinned-ness changed, or the
	// pass raced a list that shrank). Nothing references its watcher, so closing it
	// here is the difference between a bounded set and a descriptor leak per scan.
	for path, inc := range msg.rendezvous {
		if _, kept := next[path]; kept || inc.watch == nil {
			continue
		}
		_ = inc.watch.Close()
	}
	if len(next) == 0 {
		next = nil
	}
	m.rendezvous = next
	return m, cmds
}

// focusTarget is the session a focus request names, resolved in the project whose
// watcher fired. It is deliberately not a Session: for a project that is not the
// active one there is no Session to be had — m.sessions holds the active project
// alone — and the three fields below are everything the open needs.
type focusTarget struct {
	// project is empty when the request belongs to the ACTIVE project (or to no
	// identifiable project), which is what tells applyFocusRequest there is
	// nothing to switch.
	project     string
	name        string
	worktree    string
	hostRunning bool
	// scratch travels to paneOpenRequest.scratch so a click on a STOPPED
	// one-off session restarts it through the scratch spawn seam (see
	// restartHostCmd) rather than as a conductor.
	scratch bool
}

// resolveFocusTarget finds the named session in project, reporting whether it is
// openable at all.
//
// The active project is answered from m.sessions, which the 2s tick keeps fresh.
// Any OTHER project is answered from the scan cache, which is the only place a
// session outside the active project is known — and it has to be, because
// switchProject clears m.sessions and the switch happens before the open. The
// cache is up to one scan interval stale, which costs a click on a session
// created in the last minute (discarded as unknown, silently, exactly as a
// cleaned session's click is) and nothing worse: a session that has since
// STOPPED still opens, through the same restart path a stale HostRunning already
// took.
//
// A PENDING placeholder reads as unopenable, matching the pre-existing
// decision — it has no worktree and no host yet, so it is as unopenable as a
// name nothing matches.
func (m Model) resolveFocusTarget(project, name string) (focusTarget, bool) {
	if project == "" || project == m.activeProjectPath() {
		s, ok := m.sessionByName(name)
		if !ok || s.Pending {
			return focusTarget{}, false
		}
		return focusTarget{name: s.Name, worktree: s.WorktreePath, hostRunning: s.HostRunning, scratch: s.Scratch}, true
	}
	for _, st := range m.projectSessions[project] {
		// Fault rows are skipped for sessionIndex's reason: no host, nothing
		// to open. The cache is the one by-name resolver sessionByName's own
		// skip cannot cover, because it never passes through m.sessions.
		if st.Name != name || isWorktreeFault(st.Kind) {
			continue
		}
		return focusTarget{
			project:     project,
			name:        st.Name,
			worktree:    st.WorktreePath,
			hostRunning: st.HostRunning,
			scratch:     st.Scratch,
		}, true
	}
	return focusTarget{}, false
}

// scanEnabled reports whether both scan seams were injected. Everything in the
// scan pipeline gates on it, so an un-opted-in Model performs no config read and
// no disk walk — see Deps.Scan and withDefaults.
func (m Model) scanEnabled() bool {
	return m.deps.Scan != nil && m.deps.LoadConfig != nil
}

// applyProjectScan folds one scan pass into the Model: the config values it
// read, the unioned project list, and the per-project session cache.
//
// The active project is re-found by PATH rather than kept at its index, because
// a rescan can reorder the list and an index reused across two different lists
// silently repoints the dashboard at a different project mid-use.
//
// An open jump search is left as it is: it holds a snapshot taken when it
// opened (jump.go), and rows moving under the highlight would send the owner
// somewhere they did not aim. The new project shows the next time it opens.
func (m Model) applyProjectScan(msg projectScanMsg) (tea.Model, tea.Cmd) {
	nm, cmds := m.applyScanResult(msg)
	// The rendezvous refresh runs AFTER the list is settled: it adds and removes
	// per the projects that survived the union, not per what the walk reported.
	nm, rzCmds := nm.applyRendezvous(msg)
	// One batch LEVEL, deliberately: tea.Batch flattens exactly one for a receiver
	// inspecting tea.BatchMsg, so nesting the switch's own batch inside another
	// would hide load/stack/branches from the tests that assert a promotion
	// dispatched them (which is why applyScanResult returns a slice).
	return nm, tea.Batch(append(cmds, rzCmds...)...)
}

// applyScanResult is applyProjectScan's state transition, split out so the
// overlay refresh above runs on exactly one path instead of being repeated at
// each of the four exits below.
func (m Model) applyScanResult(msg projectScanMsg) (Model, []tea.Cmd) {
	// Heal m.repo to the active entry's normalized spelling. A dashboard launched
	// from a repo SUBDIRECTORY carries that subdirectory as m.repo (New copies
	// the cwd/--project value verbatim) while the pinned project entry holds the
	// git toplevel, and nothing else ever reconciles them: switchProjectCmds
	// assigns the entry path, but a kept-active pass never switches. The
	// divergence is benign for the git subprocesses (they resolve the same repo
	// from either directory) but it breaks the one-spelling assumption every path
	// comparison in this file makes. It runs BEFORE the error exit below, off the
	// list already held rather than anything this message carries, because the
	// heal does not depend on the scan having succeeded — a subdirectory launch
	// whose config never loads still needs settling on the entry's form.
	if p := m.activeProjectPath(); p != "" {
		m.repo = p
	}
	if msg.configPath != "" {
		m.configPath = msg.configPath
	}
	if msg.err != nil {
		// A config file that went malformed under a running dashboard keeps the list
		// and the cache already held: those projects still exist, only the file
		// naming their roots is unreadable, so emptying the switcher would hide
		// working projects over a typo.
		m.notifyScanError(msg.err.Error())
		return m, nil
	}
	// Reached only when msg.err is nil: clears any suppression armed by a
	// prior failing pass, so the same config error returning after a
	// successful scan is reported again rather than silently swallowed.
	m.notifyScanError("")

	if msg.interval > 0 {
		m.scanInterval = msg.interval
	}
	was := m.activeProjectPath()
	// Remembered, not only merged: the next listed read re-merges against it
	// (see Model.found). Only a good search reaches this line, so a search that
	// failed keeps the rows the last good one found.
	m.found = msg.projects
	m.projects = m.mergedProjects()
	m.projectSessions = msg.sessions
	return m.settleActiveProject(was)
}

// settleActiveProject re-finds the active project after m.projects was rebuilt,
// by PATH: was is the active path before the rebuild. It is shared by the two
// things that rebuild the list, a scan result and a read of the project list,
// so both treat a kept, a vanished and a never-existed active project the same
// way.
func (m Model) settleActiveProject(was string) (Model, []tea.Cmd) {
	if i, ok := indexOfProject(m.projects, was); ok {
		m.activeProject = i
		// m.repo needs nothing here. On the scan path applyScanResult's heal
		// already settled it on this same path — `was` IS the pre-merge active
		// entry's spelling — and a listed read leaves it as it was.
		return m, nil
	}
	if len(m.projects) == 0 {
		// Nothing found, listed or pinned. -1 rather than a fabricated entry, and
		// the session list is left alone: the projects it came from are gone from
		// the search roots and the project list, not necessarily gone from disk.
		m.activeProject = -1
		return m, nil
	}
	// Started anywhere but inside a project (was is empty: no project was ever
	// active), the dashboard opens on All projects and stays there until the
	// owner picks one, instead of making the first project active (docs/
	// dash-design.md §5). Needs the reader: without it the screen cannot be
	// drawn, and the older promotion below is the only way to show a project.
	// Checked on every settle, not once, because the project list is rebuilt
	// every minute and the screen is already open on the later ones.
	if was == "" && m.deps.StartLevel != StartProject && m.deps.LoadAllProjects != nil {
		m.activeProject = -1
		if m.allProjectsOpen {
			return m, nil
		}
		nm, cmd := m.openAllProjects()
		return nm, []tea.Cmd{cmd}
	}
	// The old index is meaningless against the new list, and it has to be dropped
	// BEFORE the switch rather than after: switchProject no-ops on the
	// already-active path, and a stale index pointing anywhere inside the fresh
	// list would make the incoming project look like the current one — leaving the
	// repo pointed at a project that is gone.
	m.activeProject = -1
	// Either the active project vanished from the list, or there never was one
	// (launched outside any repo, index -1, populated by this very message — a
	// scan's found projects or a read's listed ones, whichever lands first).
	// Never-existed and vanished are different states but they share this
	// resolution — D7's "the first project is active" — and both have to run the
	// full switch, because the repo every per-project command closes over changes.
	return m.switchProjectCmds(m.projects[0].Path)
}

// switchProject repoints the dashboard at the project at path and returns the
// per-project commands for it. The caller MUST keep the returned Model:
// previewGen and projectGen are bumped below, and an increment onto a copy that
// gets discarded compiles, leaves the suite green, and cancels nothing.
//
// An unknown path and the already-active path are both no-ops. The second is not
// mere tidiness — applyFocusRequest drives this same method for a click, and a
// click inside the CURRENT project would otherwise run the teardown below and
// close the pane holding the session the user just clicked.
//
// Switching DETACHES any open pane, with the same teardown requestQuit performs.
// The outgoing session's host is a separate process and keeps running, so nothing
// is lost, but shouldRenderPane, the `c`-clean guard and the paneName-versus-
// m.sessions matching all assume the paned session is in the current list; a pane
// carried across a switch leaves all three inconsistent. The rendezvous is the
// one thing requestQuit tears down that this does not, and now for a stronger
// reason than when there was only one of them: the set spans every project (see
// Model.rendezvous), so tearing any of it down on a switch would stop the
// dashboard hearing about the projects it just left — which is the case
// click-to-focus across projects exists for. The entries name a tmux pane a
// project switch does not move, so they stay as they are too.
func (m Model) switchProject(path string) (Model, tea.Cmd) {
	nm, cmds := m.switchProjectCmds(path)
	return nm, tea.Batch(cmds...)
}

// switchProjectCmds is switchProject returning its Cmds UNBATCHED, for the two
// callers that have Cmds of their own to send alongside them (the scan's
// promotion, and a focus request that switches before it opens). Batching there
// and again here would put the per-project trio one level deeper than
// tea.Batch's single flatten, which is the difference between a test seeing
// sessionsLoadedMsg and seeing an opaque tea.BatchMsg.
func (m Model) switchProjectCmds(path string) (Model, []tea.Cmd) {
	if path == "" || path == m.activeProjectPath() {
		return m, nil
	}
	i, ok := indexOfProject(m.projects, path)
	if !ok {
		return m, nil
	}
	m.activeProject = i
	// One repo field, repointed — rather than every command being rewritten to
	// look up the active project. loadCmd, stackCmd and plainBranchesCmd all close
	// over m.repo, and this single assignment is what keeps their contract ("the
	// repo this dashboard is showing") true across a switch.
	m.repo = path
	m.forgetPane()
	// Every value below is session- or repo-scoped and belongs to the OUTGOING
	// project; left in place, each would read as the incoming project's until its
	// first load lands seconds later.
	// The filter is dropped with the rows it was written against: a query that
	// matched the outgoing project's sessions would silently hide most of the
	// incoming project's, with the only clue a one-line prompt the switch just
	// scrolled past.
	m.list.ResetFilter()
	m.sessions = nil
	m.rebuildListItems(0)
	m.loadErr = ""
	// The incoming project gets its own first report of a failure: a
	// suppression left from the outgoing project's discovery error would
	// swallow the identical error here, and its failing-since would date the
	// new project's staleness from before the switch.
	m.notifiedLoadErr = ""
	m.loadFailingSince = time.Time{}
	// A background event still waiting for the slot describes the outgoing
	// project's sessions; landing it under the incoming project's rows would
	// name a session that is not on screen.
	m.pendingBackground = nil
	m.lastLoad = time.Time{}
	m.stack = stackState{}
	// A stale suppression from the outgoing project's stack/branches failure
	// must not swallow the incoming project's first report of the same text,
	// for the identical reason notifiedLoadErr is reset two lines above.
	m.notifiedStackErr = ""
	m.notifiedBranchesErr = ""
	m.plainBranches = nil
	m.plainTrunk = ""
	// The build snapshot is the outgoing project's tasks and inbox. It must go
	// before projectCmds below issues the incoming project's first read.
	m.forgetBuild()
	m.forgetPlanStatus()
	m.forgetTaskTokens()
	// The inbox overlay shows the outgoing project's items, and an answer
	// typed into it would be sent against a snapshot that has just been
	// dropped. It closes with the switch; an answer already on its way to
	// disk still lands, and its outcome is dropped by the generation stamp.
	m.inboxOpen = false
	m.inboxFollow = ""
	// The plan-check overlay shows the outgoing project's plan. It holds
	// nothing typed, so closing it loses nothing.
	m.planCheckOpen = false
	// The tokens overlay shows a task of the outgoing project's plan, and
	// holds nothing typed.
	m.taskTokensOpen = false
	// The all-projects screen closes with a switch, whichever way the switch
	// came (its own enter, a click on a notification, a plan started in
	// another folder): the owner moved to a project, and the project is what
	// they should see.
	m.allProjectsOpen = false
	m.deadHostPIDs = nil
	m.pendingSpawns = nil
	// A preview timer armed on the outgoing project's rows is inert from here (see
	// previewGen), so is any dial still outstanding — its arrival now carries a
	// stale generation and applyPaneAttached drops it (see projectGen) — and an
	// in-flight dial's record names a session no longer in the list, so it would
	// block nothing worth blocking.
	m.previewGen++
	m.projectGen++
	m.attachInFlight = inFlightOpen{}
	m.restartInFlight = inFlightRestart{}
	m.paneLost = lostPane{}
	// A focus-request switch racing in behind an earlier jump switch's still-
	// in-flight load must not inherit that switch's arm — applyFocusRequest owns
	// its own explicit open for ITS switch, so previewOnNextLoad must start false
	// for every fresh switch and only an owner's switch (jumpToProject and its
	// kin) re-sets it afterward.
	m.previewOnNextLoad = false
	// The selection arm is cleared for the mirror-image reason, and it applies to
	// BOTH directions: a jump switch behind a still-in-flight focus-request
	// load must not restore that click's target instead of the incoming project's
	// own remembered row, and a second click must not land on the first one's
	// session. applyFocusRequest re-sets it after this returns, for the switch it
	// is performing — so the clear is safe on its own path too.
	m.selectOnNextLoad = ""
	// A plan action's armed selection belongs to the outgoing project, whose
	// planning session is not in the incoming one's list. So does the lead
	// armed for its pane: applyPlanNewOpened arms it after its own switch.
	m.planSelectPending = ""
	m.pendingPaneLead = ""
	// m.cleaningSessions is deliberately KEPT. A clean already dispatched is still
	// running in the outgoing project, its cleanFinishedMsg will arrive and clear
	// the entry, and until then the worst a stale name costs is a refused `c` on a
	// same-named session elsewhere. Clearing it would instead let two concurrent
	// cleans run against one worktree, which is the destructive direction.
	return m, m.projectCmds()
}

// indexOfProject finds path in projects. An empty path never matches, so a Model
// with no active project cannot be repointed at an entry by accident.
func indexOfProject(projects []project.Project, path string) (int, bool) {
	if path == "" {
		return -1, false
	}
	for i := range projects {
		if projects[i].Path == path {
			return i, true
		}
	}
	return -1, false
}

// setSessions replaces the session list. It is the only assignment to
// m.sessions outside the two placeholder splicers, which mutate the slice and
// then call rebuildListItems themselves.
//
// The new list is put in screen order before sel is applied to it, as
// applyLoaded does, so sel keeps meaning a place on the screen: holding the
// index (rebuildListItems) of a list in some other order would select
// whatever session that order happened to put there.
func (m *Model) setSessions(sessions []Session) {
	sel := m.listSelection()
	m.sessions = inScreenOrder(sessions)
	m.rebuildListItems(sel)
}

// rebuildListItems refreshes the list component's items from m.sessions and
// points it back at position sel in that slice, clamped.
//
// Holding the INDEX across the rebuild, rather than re-finding the previously
// selected session by name, is deliberate: applyPreviewTimer's name check exists precisely because a tick
// that adds or drops a row can change which session an unmoved index points at,
// and re-deriving selection by name here would leave that check guarding
// nothing while quietly changing what the arrow keys land on. The exception is
// a right-focused keyboard, whose row is pinned by name — see holdSelection.
//
// SetItems' tea.Cmd is dropped and the filter re-run inline instead. Upstream
// nils filteredItems and defers the recompute to that Cmd, so dropping it alone
// would blank a filtered sidebar until the next keystroke — and plumbing a Cmd
// out of here would push one through applyLoaded, mergePendingSpawns and
// clearPendingSpawn, none of which return one today. filterItems is a pure
// closure over a list.Model copy (no I/O, no timers), so running it here keeps
// Update's no-I/O contract intact and makes the item/filter/selection update
// atomic.
func (m *Model) rebuildListItems(sel int) {
	before, hadBefore := m.selected()
	// Fresh rows from discovery carry no usage pause; the build snapshot does.
	m.stampUsagePauses()
	// The rows are stored in the order the Sessions section draws them, so
	// the cursor walks what the screen shows (screenOrderIndex). A row whose
	// group changed since the slice was ordered (a pause stamped just above,
	// a dead host marked model-side) moves here, and sel moves with the
	// session it pointed at rather than staying on a position another
	// session now holds.
	sel = m.orderSessionsForScreen(sel)
	items := make([]list.Item, len(m.sessions))
	for i := range m.sessions {
		items[i] = sessionItem{session: m.sessions[i]}
	}
	if cmd := m.list.SetItems(items); cmd != nil {
		if matches, ok := cmd().(list.FilterMatchesMsg); ok {
			m.list, _ = m.list.Update(matches)
		}
	}
	m.selectSessionIndex(sel)
	if hadBefore {
		m.holdSelection(before)
	}
	// A rebuild can change which session the unmoved index points at (the
	// reason applyPreviewTimer checks names), and it always refreshes the
	// content the detail pane draws — so the scroll offset is re-keyed to the
	// selection and the Model's viewport is re-seeded here, the tick-driven
	// counterpart of schedulePreview's reset and scrollDetail's sync. Without
	// this a session dropping out ahead of the cursor would leave the next
	// session's detail scrolled to the old offset, and a body that shrank
	// would leave the clamp ceiling stale until the next scroll key.
	m.resetDetailScroll()
	m.syncDetailViewport()
	m.closeDetailsIfGone()
}

// holdSelection applies the pane-lifecycle rules to a rebuild that moved the
// selection off before, the row selected going in.
//
// With the right side focused, or the details overlay open, the selection is
// PINNED by name (and kind, since a fault row and a session can share a
// name): a tick that reshuffles the list must not change which session the
// focused surface or the overlay belongs to. This is
// the one exception to rebuildListItems' hold-the-index rule, and it costs
// applyPreviewTimer's name check nothing — previews never fire under right
// focus. A pinned row that left discovery cannot be held; its pane, if it had
// one, is detached, and the keyboard stays right on whatever the right side
// now shows — handing it to the list would deliver typed keys to list
// bindings.
//
// With the list focused the index holds as before, and a pane whose row the
// rebuild moved off is detached exactly as a cursor move would detach it.
func (m *Model) holdSelection(before Session) {
	if after, ok := m.selected(); ok && after.Name == before.Name && after.Kind == before.Kind {
		return
	}
	if m.focus == focusRight || m.detailsOpen {
		for i := range m.sessions {
			if m.sessions[i].Name == before.Name && m.sessions[i].Kind == before.Kind {
				m.selectSessionIndex(i)
				return
			}
		}
		if _, ok := m.selected(); ok && before.Name == m.paneName {
			m.forgetPane()
		}
		return
	}
	if _, ok := m.selected(); ok && m.pane != nil && before.Name == m.paneName {
		m.forgetPane()
	}
}

// listSelection is the selected session's index in m.sessions — the successor
// of the old cursor field. list.GlobalIndex() answers it directly: it maps a
// visible index back through the filter's match set when one is applied, and
// returns the plain index when none is.
func (m Model) listSelection() int { return m.list.GlobalIndex() }

// selectSessionIndex points the list at the session at index i in m.sessions,
// clamping i into range.
//
// While a filter is applied the list's index addresses only the visible subset,
// so the target has to be translated — by name, the one identity that survives
// filtering. A target that the filter hides leaves the selection where it is,
// merely re-clamped into the subset: moving it would jump the user to an
// unrelated row for a change they did not make.
func (m *Model) selectSessionIndex(i int) {
	if len(m.sessions) == 0 {
		selectListIndex(&m.list, 0)
		return
	}
	if i >= len(m.sessions) {
		i = len(m.sessions) - 1
	}
	if i < 0 {
		i = 0
	}
	if m.list.FilterState() == list.Unfiltered {
		selectListIndex(&m.list, i)
		return
	}
	visible := m.list.VisibleItems()
	want := m.sessions[i].Name
	for j := range visible {
		if it, ok := visible[j].(sessionItem); ok && it.session.Name == want {
			selectListIndex(&m.list, j)
			return
		}
	}
	if m.list.Index() >= len(visible) {
		selectListIndex(&m.list, max(0, len(visible)-1))
	}
}

// selected returns the currently-selected session, or ok=false when the list
// is empty — or when a filter has narrowed it to nothing.
func (m Model) selected() (Session, bool) {
	it, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		return Session{}, false
	}
	return it.session, true
}

// sessionStillWedged re-checks the restart gate against the latest discovery
// snapshot, by name rather than by cursor: the list can reshuffle under an
// open confirmation, and the question being answered is about the session
// the user confirmed, not whatever now sits at their cursor. A session that
// disappeared reports false — there is nothing left to restart.
func (m Model) sessionStillWedged(name string) bool {
	for _, s := range m.sessions {
		if s.Name != name {
			continue
		}
		_, wedged := showsWedged(s, m.deps.Now())
		return wedged
	}
	return false
}
