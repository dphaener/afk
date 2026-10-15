package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/nexttask"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ship"
	"github.com/dphaener/afk/internal/state"
)

// This file is the impure half of the loop. Tick reads one Observation from
// disk, asks Decide what to do, and carries out the one Action it gets back.
//
// Nothing here outlives a tick. There is no Daemon value, no map and no
// counter: every tick starts from the files under the project's root, so a
// process started a second after another was killed reaches the same
// decision the dead one would have. Anything that looks like it wants a
// remembered flag ("was this already reported?", "did preflight fail last
// time?") is answered from an inbox item or the state file instead.
//
// Everything that reaches outside the project's own plain files is a field
// of Deps, so the tests drive the whole loop with a fake agent that only
// writes status.json and a fake ship that only saves its progress. Git,
// GitHub, the health check and the session's terminal are never touched from
// this package: the ship behind Deps.Ship owns the first three, and
// Deps.SendPrompt, Deps.Restart and Deps.Relaunch the last. The inbox, the ledger, the decision log and the state
// files are used directly: they are plain files under Root with one writer
// package each, and faking them would only test the fake.

// Project is one project the loop runs. Root is its main checkout, never a
// session's worktree: the ledger, the inbox and the logs are shared by every
// session and live in the main checkout only.
type Project struct {
	Name, Root string
}

// Settings are the loop's limits, from machine config.
type Settings struct {
	// StallAfter is how long a session may ask something at the terminal
	// before the owner is told.
	StallAfter time.Duration
	// HostGrace is how long a session may be without a running host before
	// it is reported gone. Zero means DefaultHostGrace.
	HostGrace time.Duration
	// MaxRestarts is how many times one attempt's session may be recovered
	// (its agent restarted, or its host started again) before the task is
	// parked for the owner (PLAN §5.6). It is used as given, with no default
	// here: zero is a real setting that means never recover, and the caller
	// passes the machine config's value, which carries the default.
	MaxRestarts int
	// RestartBackoff is the wait before the second recovery of an attempt;
	// it doubles for each one after that. Used as given, like MaxRestarts.
	RestartBackoff time.Duration
	// ContinueAfter is how long after a recovery the session is told to
	// carry on. Zero means DefaultContinueAfter.
	ContinueAfter time.Duration
	// WedgeConfirm is how long a host's verdict that its agent has stopped
	// answering must have stood before the agent is restarted. Zero means
	// DefaultWedgeConfirm.
	WedgeConfirm time.Duration
	// PauseRetry is how long after a session was asked to stop for usage a
	// host that is still running is made to stop; after three times as long
	// the owner is told (PLAN §6.4). Zero or less means DefaultPauseRetry.
	PauseRetry time.Duration
	// RateLimitFallback is how long a session stopped by its rate-limit
	// marker waits when no usage window gives a reset time. Zero or less
	// means DefaultRateLimitFallback.
	RateLimitFallback time.Duration
	// MaxWakes is how many times one attempt's session may be woken (its
	// resume command typed after a failed turn or a long wait at its prompt)
	// before the owner is told (PLAN T2.5). Like MaxRestarts it is used as
	// given: zero is a real setting that means never wake, and the caller
	// passes the machine config's value, which carries the default.
	MaxWakes int
	// WakeAfter is how long after an agent's turn failed its session is
	// woken. Zero or less means DefaultWakeAfter.
	WakeAfter time.Duration
	// Binary is the identity of afk's program file recorded when the daemon
	// started. The zero value means unknown, and Run then never stops for a
	// new binary.
	Binary Binary
}

const (
	// DefaultHostGrace covers the gap between the ledger saying a task is
	// running and its host process appearing: creating the worktree and
	// starting the host takes seconds, and a session seen in that gap must
	// not be reported dead.
	DefaultHostGrace = 60 * time.Second

	// DefaultContinueAfter is the wait between a session being recovered and
	// its resume command being typed. The resumed agent needs time to start
	// before a typed line reaches a prompt and not a program that is still
	// loading. It is a margin, not a guarantee, in the same way the pauses
	// host.SendInput makes around its Enter are: nothing on disk says
	// "the prompt is ready", so a slow start can still lose the line.
	DefaultContinueAfter = 30 * time.Second

	// DefaultWakeAfter is the wait between an agent's turn failing and its
	// session's resume command being typed. A turn fails on a lost
	// connection or a fault at the far end, and neither is gone the second
	// it is reported; a line typed at once would mostly fail the same way
	// and use up a wake. It is not a setting (PLAN T2.5): the waits that
	// grow, between one wake and the next, come from restart_backoff.
	DefaultWakeAfter = 30 * time.Second

	// DefaultWedgeConfirm is how long a wedge verdict must have stood. It is
	// one of the host's re-probe intervals (wedgeProbeInterval in
	// internal/host/liveness.go, the least gap between two probes of one
	// silent agent, also sixty seconds), so a verdict that any output would
	// have cleared has had the chance to clear before a working agent is
	// stopped.
	DefaultWedgeConfirm = 60 * time.Second

	// DefaultPauseRetry is the wait between asking a session's host to end
	// for usage and making it. The polite stop is one request to the host,
	// which then has to stop its agent before it can end, so a host that is
	// still there a few seconds later is not yet a host that ignored the
	// request; a minute later it is.
	DefaultPauseRetry = time.Minute

	// DefaultRateLimitFallback is the wait for a session that hit a usage
	// limit when nothing says when the limit resets. An hour is a guess that
	// leans towards waiting: resuming early only hits the limit again.
	DefaultRateLimitFallback = time.Hour

	// defaultStallAfter is used when Settings carries no limit. A zero
	// limit would report a stall the moment a session asked anything, so a
	// caller that forgot to set it gets the documented ten minutes and not
	// a page per prompt.
	defaultStallAfter = 10 * time.Minute

	// defaultTick is Run's pace when it is given none.
	defaultTick = 5 * time.Second
)

// The titles of the two inbox items that are about the machine or the
// project and not about one task. They are exported because the loop finds
// its own earlier item by title (the inbox schema has no field for what
// raised an item), so the title is part of how the loop avoids raising the
// same thing twice.
const (
	// PreflightTitle is the blocking item raised when this machine cannot
	// start an agent in the unattended mode without a dialog.
	PreflightTitle = "afk cannot start agents on this machine"
	// StopTitle is the item raised when the plan, the ledger or the project
	// config cannot be used.
	StopTitle = "afk cannot run this project"
	// NoLedgerTitle is the blocking item raised when the project has no
	// ledger file. The loop answers it by itself once the file is there.
	NoLedgerTitle = "This project has no task list yet"
	// OwnerTitlePrefix starts the title of the item raised for a task that
	// is the owner's to do. The task's id and title follow it.
	OwnerTitlePrefix = "Your task: "
	// OverageTitle is the blocking item raised when the account has extra
	// usage switched on (the governor says refuse). The loop answers it by
	// itself once a reading shows extra usage off.
	OverageTitle = "Extra usage is switched on, so afk starts nothing"
	// LoginTitle is the blocking item raised when the agent is logged out on
	// this machine. The loop answers it by itself once a check shows the
	// agent logged in.
	LoginTitle = "The agent is logged out on this machine"
)

// maxItemTitle is the longest title the inbox stores, counted in characters
// (internal/inbox's maxTitleChars, which is not exported). A master-plan
// heading has no such limit, so a title built from one is cut to fit; a
// longer one would be refused by the store and the owner would never hear
// of their task. TestTickOwnerTask's long-title row adds a real item at
// exactly this length, so the two numbers cannot drift apart unnoticed.
const maxItemTitle = 200

// State kinds the executor writes that are not an ActionKind. Every other
// state's kind is the kind of the action taken. KindLineStopped, beside
// Decide in decide.go, is the third.
const (
	// KindStopped: the project's folder is not there. Nothing is written.
	KindStopped = "stopped"
	// KindBlocked: a task is ready but the machine failed the preflight.
	KindBlocked = "blocked"

	// ReasonNoProject is the reason of a KindStopped state.
	ReasonNoProject = "project folder not found"
)

// Kinds lists every state kind the loop can write, each once. It is here so
// that whatever shows a state to the owner can test that it has plain words
// for all of them, and a kind added to the loop cannot reach the owner as a
// bare code word without a test saying so.
func Kinds() []string {
	return []string{
		string(ActStop), string(ActWait), string(ActCreate), string(ActShip),
		string(ActFail), string(ActPark), string(ActHostGone), string(ActStall),
		string(ActUnpark), string(ActLaunch), string(ActIdle),
		string(ActNoLedger), string(ActOwner), string(ActOwnerDone),
		string(ActShipStep),
		string(ActRestart), string(ActRelaunch), string(ActContinue), string(ActGiveUp),
		string(ActUsageWait), string(ActLoginWait), string(ActUsagePause),
		string(ActUsagePaused), string(ActUsageStuck), string(ActUsageResume),
		string(ActQuestionHold), string(ActQuestionAnswered),
		string(ActWake), string(ActWakeGiveUp),
		KindStopped, KindBlocked, KindLineStopped,
	}
}

// The decision log's rule column: which part of docs/PLAN.md took the
// decision.
const (
	ruleCreate    = "PLAN §5.3 step 2"
	rulePreflight = "PLAN §5.3 step 3"
	ruleWatch     = "PLAN §5.3 step 4"
	rulePlan      = "PLAN §5.1"
	ruleTokens    = "PLAN §4.7"
	ruleShip      = "PLAN §5.4"
	ruleRestart   = "PLAN §T2.2a"
	// ruleRecovery is the rule for everything done to a session that stopped
	// or hung. It is not ruleRestart, which was taken first and is about the
	// daemon's own program file.
	ruleRecovery = "PLAN §5.6"
	// ruleGovernor is the rule for everything done because of usage, and
	// ruleLogin for the login check.
	ruleGovernor = "PLAN §6.3"
	ruleLogin    = "PLAN §6.5"
)

// KindDaemonRestart is the decision-log kind for the daemon stopping because
// its program file changed.
const KindDaemonRestart = "daemon_restart"

// ErrNewBinary is what Run returns when afk's program file changed on disk.
// It is not a failure: the caller exits so the service manager starts the new
// program.
var ErrNewBinary = errors.New("afk's program file changed on disk")

// LaunchRequest asks for one session to be created and its agent started.
type LaunchRequest struct {
	Project Project
	// Task is the master-plan task id; Session is the session, branch and
	// worktree name; Description is the text the agent is started with.
	Task, Session, Description string
}

// ShipRequest asks for one task's ship to be taken at most one step forward.
// It is what the loop knows about the ship on this tick; everything the ship
// reads for itself (git, GitHub, the deploy, the project's [ship] table) is
// the business of whatever is behind Deps.Ship.
type ShipRequest struct {
	Project Project
	// Entry is the task's ledger entry as read at the start of the tick. Its
	// Session and Branch name what is being shipped.
	Entry ledger.Entry
	// Worktree is the session's worktree and Base the branch it was cut
	// from, both as discovered on this tick (Base is status.json's
	// base_branch). Both are empty when the session is not on disk, which is
	// an ordinary case and not an error: a session is cleaned up before the
	// ledger says done, and a daemon killed between the two comes back to a
	// shipping task with nothing left to look at. The ship's last states need
	// neither. Base is also empty when status.json cannot be read; the
	// project's own `[ship] base` is then the only base there is.
	Worktree, Base string
	// Progress is the ship's progress as the ledger holds it.
	Progress ship.Progress
	// Save writes the progress to the ledger entry. The ship calls it after
	// each step, so the ledger always holds the state the next action leaves
	// from, and a daemon killed anywhere starts again from a state that is
	// true. It fails when the entry is no longer shipping (the owner moved it
	// by hand), and the ship must then do nothing more.
	Save func(ship.Progress) error
	// Stop writes the project's stop marker. The ship calls it, before it
	// saves the stopped state, when a deploy does not come up healthy.
	Stop func(task, reason string) error
}

// RelaunchRequest asks for a new host for a session whose host is gone.
type RelaunchRequest struct {
	// Worktree is the session's worktree and Session its name, both as
	// discovered on this tick.
	Worktree, Session string
	// ResumeID is the agent's own conversation id, from the session's
	// status.json. When it is not empty the new host resumes that
	// conversation. When it is empty (the session had not recorded one yet,
	// or its status file could not be read) the new host starts the session's
	// ordinary launch command, and the agent picks its work up from its files.
	ResumeID string
}

// Deps is every call the loop makes outside the project's plain files. Now,
// LoadBuild, Sessions, Preflight, Launch and RecordTokens must be set; Notify,
// NotifyIdle, NotifyUrgent, Log, LedgerExists, Stopped, Ship, SendPrompt,
// Restart, Relaunch, Clean, Usage, Login, StopSession, RateLimited,
// TurnFailed, ReadPlan, PatchPlan and PlanWaits may be nil.
//
// ReadPlan is optional in the same way the usage fields below are. With no
// ReadPlan the loop never reads the plan's Open questions table: no task is
// held for a question, no question item is raised or closed, and the loop
// behaves exactly as it did before it had the hold. That is the promise every
// caller and test written before the hold relies on.
//
// The four usage fields are optional as a set and one at a time. With no
// Usage the loop behaves exactly as it did before it had a usage governor:
// no launch is held for usage and no session is paused. That is the promise
// every caller and test written before the governor relies on.
type Deps struct {
	// Now is the clock. The package never reads the real one, so a test
	// moves time by hand.
	Now func() time.Time
	// LoadBuild reads the project config, the master plan it names and the
	// ledger. An error means the project cannot be run until it is fixed.
	LoadBuild func(root string) (tasks []masterplan.Task, planPath string, l ledger.Ledger, err error)
	// Sessions lists the project's sessions from local git and files.
	Sessions func(root string) ([]discover.SessionState, error)
	// Preflight says whether this machine can start an agent in the
	// unattended mode, and what to do when it cannot. It runs the agent
	// CLI's plugin listing, so the loop calls it only when a task is about
	// to launch.
	Preflight func(p Project) (ok bool, steps string)
	// Launch creates the session and starts its agent.
	Launch func(LaunchRequest) error
	// RecordTokens appends the session's token use to the token ledger and
	// returns how many lines the session has there.
	RecordTokens func(p Project, task, session, sessionID string) (lines int, err error)
	// Notify tells the owner about an inbox item the loop just added. It is
	// handed the stored item, id included, so the sender can build the same
	// text `afk inbox add` sends. Best effort: it returns nothing.
	Notify func(p Project, it inbox.Item)
	// NotifyIdle is the one notification with no item behind it: the build
	// has stopped moving and only the owner can restart it.
	NotifyIdle func(p Project, title, body string)
	// Log takes one line for the daemon's own log.
	Log func(format string, args ...any)
	// LedgerExists reports whether the project has a ledger file, damaged or
	// not. Nil means ledger.Exists. It is separate from LoadBuild because
	// LoadBuild cannot say: a missing ledger loads as an empty one, with no
	// error.
	LedgerExists func(root string) bool
	// Stopped reports whether the project's line is stopped, and the reason
	// the stop marker gives (empty when it gives none). Nil means the stop
	// marker in the project's own files, read with internal/daemonstate.
	Stopped func(root string) (reason string, stopped bool)
	// Ship takes a shipping task's ship at most one step forward and says
	// what the loop should do about it. Nil means there is no shipper: a
	// finished task then stays in `shipping` until the owner moves it by
	// hand, which is what the loop did before it could ship.
	Ship func(ShipRequest) ship.Outcome
	// SendPrompt types one line into a session's terminal and presses Enter.
	// Nil means nothing is typed: an un-parked session is then left for the
	// owner to resume, as before, and a recovered session is not told to
	// carry on.
	SendPrompt func(worktree, session, text string) error
	// Restart asks the session's host to restart its agent, resuming the same
	// conversation, and waits for the host's answer. ("", nil) means the agent
	// was restarted. A non-empty refused is the host's own reason for not
	// doing it (there is no conversation to resume, say), and nothing was
	// restarted. An error means the request could not be made or its outcome
	// is not known: the agent may or may not have been restarted. Nil means
	// the loop cannot restart an agent, and a session that has stopped
	// answering is then left alone, as it was before the loop could recover
	// one.
	Restart func(worktree, session string) (refused string, err error)
	// Relaunch starts a new host for a session whose host is gone. Nil means
	// the loop cannot do that, and a gone host is then reported to the owner
	// with an inbox item, as before.
	Relaunch func(RelaunchRequest) error
	// Clean removes a shipped task's session: its worktree, its files and its
	// merged branch. A session that is not there is not an error. Nil means
	// nothing is cleaned and the task is marked done all the same.
	Clean func(p Project, session string) error
	// NotifyUrgent tells the owner about an item at the highest priority. It
	// is used for one thing, a stopped line: a deploy that is down is the one
	// message that should get past a phone's quiet hours. Nil means Notify is
	// used instead. It is a second field, and not an argument added to
	// Notify, so that every existing Notify keeps working unchanged.
	NotifyUrgent func(p Project, it inbox.Item)
	// Binary reads the identity of afk's program file as it is now. Nil
	// means Run never checks. The caller resolves the path; this package
	// never looks for its own executable.
	Binary func() (Binary, error)
	// Usage gives the usage governor's verdict from the latest usage
	// reading, with Consulted set. It may run the agent CLI's usage probe, so
	// the loop calls it only on a tick where the answer can change something
	// (see consults). Nil means usage is never consulted: nothing is held
	// back for usage and no session is paused, the rate-limit marker
	// included, because nothing could ever say a paused session may go on.
	Usage func() UsageView
	// Login gives the last verdict on whether the agent is logged in on this
	// machine. It is called on the same ticks as Usage. Nil means the login
	// is unknown, and unknown holds nothing back.
	Login func() LoginView
	// StopSession asks the session's host to end its agent and itself. With
	// force false it is a request the host may be slow to serve; with force
	// true the caller makes the host stop. An error means the session may
	// still be running; the loop logs it and tries again later. Nil means the
	// loop cannot stop a session, and one that reaches a usage limit is then
	// left running, with the agent's own wait at the limit as the backstop.
	StopSession func(worktree, session string, force bool) error
	// RateLimited reads the session's rate-limit marker: the time the agent's
	// own hook wrote when a turn ended on a usage limit, and false when there
	// is no readable marker. Nil means state.ReadRateLimited.
	RateLimited func(worktree, session string) (time.Time, bool)
	// ReadPlan reads the master plan's text at the path LoadBuild returned,
	// for its Open questions table. It is a second read of a file LoadBuild
	// has already read, and a field of its own, so that LoadBuild keeps its
	// shape. Nil means the table is never read (see above).
	ReadPlan func(path string) ([]byte, error)
	// PatchPlan makes one guarded change to the master plan: the owner's
	// answer written into a row. It lives behind a hook because the change is
	// a commit, and may be a push or a pull request, and this package never
	// runs git. An error that is ErrRowAnswered (by errors.Is) means the row
	// was not open in the text the change was made to, and is not a failure.
	// Nil means this daemon cannot change the plan: a held task whose
	// question is answered then parks on an item that gives the owner the
	// text to write in by hand.
	PatchPlan func(p Project, req PlanPatchRequest) (PlanPatchResult, error)
	// TurnFailed reads the session's failed-turn marker: the time and the
	// reason the agent's own hook wrote when a turn ended on an error worth
	// trying again, and false when there is no readable marker. Nil means
	// state.ReadTurnFailed.
	TurnFailed func(worktree, session string) (state.TurnFailed, bool)
	// Projects gives the list of projects to run, as it is now. Run asks it at
	// the top of every pass, so a project added to the machine config while
	// the daemon runs is picked up with no restart. An error means the list
	// could not be read this time (a config file caught half-saved, or one
	// with a mistake in it): Run then goes on with the last list it had. Nil
	// means the list Run was started with never changes. It is a hook, and
	// this package never reads the config file itself, for the same reason
	// LoadBuild is one: where the list comes from is the caller's business.
	// Once and Tick never ask it.
	Projects func() ([]Project, error)
	// PlanWaits is called once per pass, after every project has been
	// ticked: in Run once per pass, in Once once at the end. It runs
	// whatever the pause flag says, because a planning session is attended
	// work with the owner in the loop, not building, so pausing the build
	// must not silence the news that a question is waiting. Tick never calls
	// it. Nil means the loop does nothing of the kind.
	PlanWaits func(ctx context.Context)
}

// turnFailed is Deps.TurnFailed with its default.
func (d Deps) turnFailed(worktree, sess string) (state.TurnFailed, bool) {
	if d.TurnFailed != nil {
		return d.TurnFailed(worktree, sess)
	}
	return state.ReadTurnFailed(worktree, sess)
}

// rateLimited is Deps.RateLimited with its default.
func (d Deps) rateLimited(worktree, sess string) (time.Time, bool) {
	if d.RateLimited != nil {
		return d.RateLimited(worktree, sess)
	}
	return state.ReadRateLimited(worktree, sess)
}

// ledgerExists is Deps.LedgerExists with its default.
func (d Deps) ledgerExists(root string) bool {
	if d.LedgerExists != nil {
		return d.LedgerExists(root)
	}
	return ledger.Exists(root)
}

// stopped is Deps.Stopped with its default.
//
// The default asks twice on purpose. ReadStop gives the reason but says "no
// marker" for any read error, including a marker that is there and cannot be
// opened. Stopped only looks for the file. A marker that exists must stop the
// line whether or not it can be read (running on past a broken deploy is the
// wrong direction to fail in), so existence decides and the reason is taken
// when there is one.
func (d Deps) stopped(root string) (string, bool) {
	if d.Stopped != nil {
		return d.Stopped(root)
	}
	_, reason, ok := daemonstate.ReadStop(root)
	return reason, ok || daemonstate.Stopped(root)
}

func (d Deps) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

// errLedgerMoved means the ledger on disk no longer holds what this tick
// decided from: the owner ran `afk ledger set`, or another writer got there
// first. Nothing is written, and the next tick decides again from disk.
var errLedgerMoved = errors.New("the ledger changed while afk was deciding")

// pass holds one tick's working values. One is made at the start of a tick
// and dropped at its end; it is a way to pass six arguments, not a place to
// keep anything.
type pass struct {
	p     Project
	d     Deps
	now   time.Time
	store inbox.Store
	obs   Observation
	// planPath is the master plan's path as LoadBuild gave it.
	planPath string
	// planRead means the plan's Open questions table was read on this tick,
	// so Observation.Questions can be trusted to say a row is gone.
	planRead bool
	// row is the discovered session the SessionView was built from, kept
	// because shipping and failing need things the view does not carry: the
	// agent's own session id (for the token ledger), the verdict, and the
	// worktree path and base branch the ship works in.
	row *discover.SessionState
	// parkedRows is the same for Observation.Parked, by task id: an un-park
	// needs the worktree to type the resume command into.
	parkedRows map[string]*discover.SessionState
}

// Tick runs the loop once for one project and returns the project's state as
// it now stands on disk.
//
// A root that is not a git checkout is reported and left completely alone:
// the path comes from a config file typed by hand, and a mistyped one must
// never grow an `.afk` folder somewhere the owner did not mean.
func Tick(p Project, s Settings, d Deps) daemonstate.State {
	now := d.Now()
	if !isCheckout(p.Root) {
		d.logf("%s: %s at %s: no folder there, or no .git in it. Check the path in config.toml.", p.Name, ReasonNoProject, p.Root)
		return daemonstate.State{At: stamp(now), Kind: KindStopped, Reason: ReasonNoProject}
	}
	t := &pass{p: p, d: d, now: now, store: inbox.Store{Dir: inbox.Dir(p.Root), Now: d.Now}}
	st := t.run(s)
	st.At = stamp(now)
	return t.save(st)
}

// Once runs one tick for every project, in order.
func Once(projects []Project, s Settings, d Deps) []daemonstate.State {
	out := make([]daemonstate.State, 0, len(projects))
	for _, p := range projects {
		out = append(out, Tick(p, s, d))
	}
	if d.PlanWaits != nil {
		d.PlanWaits(context.Background())
	}
	return out
}

// Run ticks every project, in order, every tick until ctx ends, then returns
// nil. Ending is not an error: the sessions go on running under their own
// hosts, and the next daemon picks them up from the ledger.
//
// A project that cannot be run does not stop the others: Tick turns every
// failure into a state, an inbox item or a log line and returns.
//
// projects is the list Run starts with. When Deps.Projects is set, each pass
// starts by asking it for the list as it is now, and that list replaces the
// one Run had, so a project added while the daemon runs is ticked on the very
// pass that first sees it. When the hook fails, Run keeps the last list it
// read and logs the failure. That direction is deliberate: the list comes
// from a file the owner edits by hand, and a file caught half-saved, or saved
// with a mistake in it, must not make a running daemon forget every project
// it was building. The same failure is logged once and not once per pass,
// since a pass is seconds long and a broken file can stay broken for hours.
// An empty list is not a failure: the pass does nothing and Run waits for the
// next tick, which is how a daemon started before its first project was
// approved comes to run it.
//
// A project that leaves the list is no longer ticked, and that is all.
// Nothing is done to its sessions: each runs under its own host and outlives
// the daemon by design, and its ledger is still on disk for the day the
// project is added back.
//
// When Deps.Binary is set, each pass then asks whether afk's program file has
// changed since Settings.Binary was recorded. If so, Run logs one
// daemon_restart decision per project and returns ErrNewBinary so the service
// manager starts the new program. The list is read before this check so that
// the restart is recorded for the projects that are running now, not for the
// ones that were running a pass ago. The check is only at the top of a pass,
// never between two projects, so a ship step is never cut in half and every
// project is either ticked by the old program for the whole pass or not at
// all. An unknown start identity or an unreadable file carries on (see
// NewBinary).
//
// Three things are kept between passes: Settings.Binary, the project list,
// and the text of the last failure to read that list (only so it is not
// logged again). None of them is about a build: everything a tick decides
// from is still read from the project's files, so the package's rule that a
// new daemon reaches the decision the dead one would have still holds. Once
// and Tick never check for a new binary and never ask for the list.
func Run(ctx context.Context, projects []Project, s Settings, tick time.Duration, d Deps) error {
	if tick <= 0 {
		tick = defaultTick
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	listErr := ""
	for {
		if d.Projects != nil {
			projects, listErr = reload(projects, listErr, d)
		}
		if d.Binary != nil {
			got, err := d.Binary()
			if NewBinary(s.Binary, got, err) {
				now := d.Now()
				for _, p := range projects {
					t := &pass{p: p, d: d, now: now}
					t.record(KindDaemonRestart, "", "",
						"afk's own program file changed on disk. What now?",
						"stop between passes so the service manager starts the new one",
						ruleRestart)
				}
				return ErrNewBinary
			}
		}
		for _, p := range projects {
			if ctx.Err() != nil {
				return nil
			}
			Tick(p, s, d)
		}
		if d.PlanWaits != nil && ctx.Err() == nil {
			d.PlanWaits(ctx)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// reload asks Deps.Projects for the list and returns the list to run and the
// failure text to remember. have and lastErr are what Run had from the pass
// before.
//
// On a failure the list is returned as it was, and the failure is logged only
// when its text differs from lastErr. A success clears the remembered text,
// so a file that is mended and later broken the same way is reported again:
// that is a second mistake, and the owner has not been told about it. The
// mended file gets a line of its own because a list that reads the same as
// before would otherwise leave no sign that the fix was seen.
//
// The list is copied, because Run holds it between passes and the caller is
// free to reuse the slice it returned.
//
// A project is known by its Root, since that is where its files are. One that
// is only renamed is the same project and is not reported as going or coming.
func reload(have []Project, lastErr string, d Deps) ([]Project, string) {
	got, err := d.Projects()
	if err != nil {
		if msg := err.Error(); msg != lastErr {
			d.logf("afk: the project list could not be read, so the projects already running carry on: %s", msg)
			lastErr = msg
		}
		return have, lastErr
	}
	if lastErr != "" {
		d.logf("afk: the project list can be read again.")
	}
	got = slices.Clone(got)
	for _, p := range got {
		if !slices.ContainsFunc(have, func(h Project) bool { return h.Root == p.Root }) {
			d.logf("afk: now running %s (%s)", p.Name, p.Root)
		}
	}
	for _, h := range have {
		if !slices.ContainsFunc(got, func(p Project) bool { return p.Root == h.Root }) {
			d.logf("afk: no longer running %s", h.Name)
		}
	}
	return got, ""
}

// isCheckout reports whether root is a folder holding a .git entry. Lstat,
// because .git is a file in a linked worktree and a folder in a main
// checkout, and either is enough to say "git knows this place".
func isCheckout(root string) bool {
	if root == "" {
		return false
	}
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return false
	}
	_, err = os.Lstat(filepath.Join(root, ".git"))
	return err == nil
}

// stamp writes a time the way the ledger, the inbox and the logs store one.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// run observes, decides and acts, and returns the state to store (without
// its time).
func (t *pass) run(s Settings) daemonstate.State {
	root := t.p.Root
	o := Observation{
		Now:            t.now,
		StallAfter:     s.StallAfter,
		HostGrace:      s.HostGrace,
		MaxRestarts:    s.MaxRestarts,
		RestartBackoff: s.RestartBackoff,
		ContinueAfter:  s.ContinueAfter,
		WedgeConfirm:   s.WedgeConfirm,
		// Decide is told what this executor can carry out, so it never
		// answers with an action whose function is nil. That is the only
		// guard: restart, relaunch, continueSession and wake call theirs
		// unchecked.
		CanRestart:  t.d.Restart != nil,
		CanRelaunch: t.d.Relaunch != nil,
		CanPrompt:   t.d.SendPrompt != nil,
		CanPause:    t.d.StopSession != nil,
		Paused:      daemonstate.Paused(root),
		NoLedger:    !t.d.ledgerExists(root),

		PauseRetry:        s.PauseRetry,
		RateLimitFallback: s.RateLimitFallback,
		MaxWakes:          s.MaxWakes,
		WakeAfter:         s.WakeAfter,
	}
	o.StopReason, o.Stopped = t.d.stopped(root)
	if o.StallAfter <= 0 {
		o.StallAfter = defaultStallAfter
	}
	if o.HostGrace <= 0 {
		o.HostGrace = DefaultHostGrace
	}
	if o.ContinueAfter <= 0 {
		o.ContinueAfter = DefaultContinueAfter
	}
	if o.WedgeConfirm <= 0 {
		o.WedgeConfirm = DefaultWedgeConfirm
	}
	if o.PauseRetry <= 0 {
		o.PauseRetry = DefaultPauseRetry
	}
	if o.RateLimitFallback <= 0 {
		o.RateLimitFallback = DefaultRateLimitFallback
	}
	if o.WakeAfter <= 0 {
		o.WakeAfter = DefaultWakeAfter
	}

	tasks, planPath, l, err := t.d.LoadBuild(root)
	if err != nil {
		o.BuildErr = err
	} else {
		o.Tasks, o.Ledger, t.planPath = tasks, l, planPath
	}

	// Files that look like items but cannot be read are left out of Items,
	// which is the safe reading everywhere Decide uses them: an id that is
	// not found counts as open, so a task parked on a damaged item stays
	// parked.
	items, _, err := t.store.List()
	if err != nil {
		o.InboxErr = err
	} else {
		o.Items = items
	}

	if o.BuildErr == nil {
		// One listing serves both readers, and it is made only on a tick
		// that uses it: listing sessions costs a git call.
		e, isWatched := watched(o.Ledger)
		parked := parkedSessions(o.Ledger)
		if isWatched || len(parked) > 0 {
			rows, err := t.d.Sessions(root)
			switch {
			case err != nil && isWatched:
				// Never read as "the session is missing": that would
				// create it a second time over a session that is there.
				t.d.logf("%s: could not look at the sessions for %s: %v", t.p.Name, e.ID, err)
				t.obs = o
				return daemonstate.State{Kind: string(ActWait), Task: e.ID, Session: e.Session,
					Reason: fmt.Sprintf("the project's sessions could not be read (%v); trying again on the next pass", err)}
			case err != nil:
				// Nothing is in flight, so the tick goes on: a launch or an
				// idle state does not need the sessions. Only an un-park
				// does, and Decide holds that back (ParkedErr).
				t.d.logf("%s: could not look at the sessions of the parked tasks: %v", t.p.Name, err)
				o.ParkedErr = err
			case isWatched:
				o.Session, t.row = viewOf(rows, e.Session)
				// The marker is not part of what discovery reports, so it is
				// read here, from the same worktree and name. Only for a
				// running entry: a shipping session's agent has finished.
				if o.Session != nil && e.Status == ledger.Running {
					if at, ok := t.d.rateLimited(t.row.WorktreePath, e.Session); ok {
						o.Session.RateLimitedAt = at
					}
					// The failed-turn marker is read the same way and under
					// the same condition: it is about an agent at work.
					if m, ok := t.d.turnFailed(t.row.WorktreePath, e.Session); ok {
						o.Session.TurnFailedAt, o.Session.TurnFailedReason = m.At, m.Reason
					}
				}
			default:
				o.Parked = make(map[string]*SessionView, len(parked))
				t.parkedRows = make(map[string]*discover.SessionState, len(parked))
				for _, pe := range parked {
					if v, row := viewOf(rows, pe.Session); v != nil {
						o.Parked[pe.ID], t.parkedRows[pe.ID] = v, row
					}
				}
			}
		}
	}
	// The plan's open questions, when this daemon can read them. Read on
	// every tick, running task or not: it is one small file read, and it is
	// what lets an item be closed as soon as its row is answered by hand.
	if o.BuildErr == nil && t.d.ReadPlan != nil {
		o.Questions, o.QuestionsErr = t.readQuestions()
		t.planRead = o.QuestionsErr == nil
	}
	// Before Decide, so that a task parked on an item closed here is seen as
	// answered on this same tick. Only on a read that worked: a plan that
	// cannot be read has not lost its rows.
	if t.planRead && o.InboxErr == nil {
		t.obs = o
		if t.clearQuestions() {
			if items, _, err := t.store.List(); err != nil {
				o.InboxErr = err
			} else {
				o.Items = items
			}
		}
	}
	t.obs = o

	// Decide runs once without usage, and a second time with it only when
	// the answer can change something (see consults): asking can start the
	// agent CLI's usage probe, and an idle project must never cause one.
	a := Decide(o)
	if t.consults(o, a) {
		// The login first: the usage source may skip its probe while the
		// agent is known to be logged out, and should see this tick's verdict.
		if t.d.Login != nil {
			o.Login = t.d.Login()
		}
		if t.d.Usage != nil {
			o.Usage = t.d.Usage()
		}
		t.obs = o
		a = Decide(o)
		t.notices(o)
	}

	// The project can be read again: close what was said about it, so a
	// later error is not hidden behind an old one. Only when Decide did not
	// stop, because a stop also comes from a ledger changed by hand while
	// the plan reads fine, and answering that item here would have the next
	// tick raise it again, for ever.
	if a.Kind != ActStop && o.InboxErr == nil {
		t.clearStops()
	}
	// The same for the missing ledger, and on its own condition: the item
	// says "there is no ledger file", and that stops being true the moment
	// there is one, even a damaged one that the tick then stops on.
	if !o.NoLedger && o.InboxErr == nil {
		t.clearNoLedger()
	}

	switch a.Kind {
	case ActStop:
		return t.stop(a)
	case ActNoLedger:
		return t.noLedger(a)
	case ActOwner:
		return t.ownerTask(a)
	case ActOwnerDone:
		return t.ownerDone(a)
	case ActQuestionHold:
		return t.questionHold(a)
	case ActQuestionAnswered:
		return t.questionAnswered(a)
	case ActLaunch:
		return t.launch(a)
	case ActCreate:
		return t.start(a.Kind, a.Task, a.Session, a.Reason)
	case ActShip:
		return t.ship(a)
	case ActFail:
		return t.fail(a)
	case ActPark:
		return t.park(a)
	case ActStall, ActHostGone:
		return t.report(a)
	case ActRestart:
		return t.restart(a)
	case ActRelaunch:
		return t.relaunch(a)
	case ActContinue:
		return t.continueSession(a)
	case ActGiveUp:
		return t.giveUp(a)
	case ActWake:
		return t.wake(a)
	case ActWakeGiveUp:
		return t.wakeGiveUp(a)
	case ActUsagePause:
		return t.usagePause(a)
	case ActUsageStuck:
		return t.usageStuck(a)
	case ActUsageResume:
		return t.usageResume(a)
	case ActUnpark:
		return t.unpark(a)
	case ActShipStep:
		return t.shipStep(a)
	default:
		// ActWait and ActIdle change nothing, and neither do ActUsageWait,
		// ActLoginWait and ActUsagePaused: each is a state and nothing more.
		return stateOf(a)
	}
}

// watched returns the entry whose session this tick must look at: the first
// running or shipping entry in ledger order, when it names a session. It
// follows Decide's rule 2 so that sessions, which cost a git call to list,
// are read only on the ticks that use them.
//
// A shipping entry is watched as well as a running one, though Decide never
// looks at a shipping session's phase. The ship step needs two things only
// discovery has, the session's worktree path and its base branch. Without
// them the ship would run with no worktree: every hand-back to the session
// would fail in a real project while every test with a fake ship passed.
func watched(l ledger.Ledger) (ledger.Entry, bool) {
	for _, e := range l.Tasks {
		if e.Status != ledger.Running && e.Status != ledger.Shipping {
			continue
		}
		return e, e.Session != ""
	}
	return ledger.Entry{}, false
}

// parkedSessions returns the parked entries that name a session, or nothing
// when any entry is running or shipping. Their sessions are looked at so an
// un-park can tell a finished session from a working one (Observation.Parked),
// and an un-park can only happen on a tick with nothing in flight. An owner's
// task is parked with no session and is never in this list.
func parkedSessions(l ledger.Ledger) []ledger.Entry {
	var out []ledger.Entry
	for _, e := range l.Tasks {
		switch {
		case e.Status == ledger.Running || e.Status == ledger.Shipping:
			return nil
		case e.Status == ledger.Parked && e.Session != "":
			out = append(out, e)
		}
	}
	return out
}

// viewOf finds the session called name among the discovered rows and turns
// it into what Decide looks at. It returns nil for a name with no real
// session behind it.
//
// Only a row that came from a status.json counts. Discovery also returns a
// row for a worktree that has a branch but no `.afk/{name}/` folder, and for
// a one-off scratch session; neither has a status file, so neither has a
// phase to watch. Treating the first as a session would leave a half-made
// one (the daemon was killed after the worktree was created and before its
// files were seeded) waiting for a status that will never be written, and
// then reported as a dead host. As "no session" it reaches ActCreate, whose
// Launch fails on the branch that is already there and tells the owner how
// to clear it.
func viewOf(rows []discover.SessionState, name string) (*SessionView, *discover.SessionState) {
	for i := range rows {
		r := &rows[i]
		if r.Name != name || r.Scratch {
			continue
		}
		if r.Kind == session.KindWorktreeNoSession || r.Kind == session.KindWorktreeMissing {
			continue
		}
		v := &SessionView{
			Name:          r.Name,
			Stale:         r.StatusStale,
			Phase:         r.Phase,
			AwaitingInput: r.AwaitingInput,
			HostRunning:   r.HostRunning,
		}
		// The zero time when it cannot be read: Decide then measures from
		// the attempt's start instead.
		v.UpdatedAt, _ = parseTime(r.UpdatedAt)
		// The zero time, which Decide reads as "not wedged", when the host
		// published no verdict or one that cannot be read. An unreadable
		// verdict must not restart anything: there is no telling how long it
		// has stood.
		v.WedgedSince, _ = parseTime(r.WedgedSince)
		return v, r
	}
	return nil, nil
}

// stateOf is the state of a tick that did exactly what Decide said. A wait
// that Decide marked as the stopped line is stored under its own kind, so
// `afk daemon status` can tell it from every other wait.
//
// The three waits the usage governor causes (ActUsageWait, ActLoginWait,
// ActUsagePaused) need no such step: each is an action kind of its own and so
// a state kind of its own. That is what makes the state file change, and save
// log a line, when the governor's verdict changes what the loop does.
func stateOf(a Action) daemonstate.State {
	kind := string(a.Kind)
	if a.Kind == ActWait && a.LineStopped {
		kind = KindLineStopped
	}
	return daemonstate.State{Kind: kind, Task: a.Task, Session: a.Session, Reason: a.Reason}
}

// save stores the tick's state and returns what is on disk afterwards.
//
// The file is written only when something other than the time changed, so
// its time says when the project entered this state, which is what `afk
// daemon status` shows as "since". The same comparison is what makes the
// idle notification fire once: there is no remembered "already told" flag,
// only the fact that the stored state already says so. For that reason the
// notification follows a successful write and is skipped after a failed
// one, since a state that cannot be stored would otherwise notify on every
// tick.
func (t *pass) save(st daemonstate.State) daemonstate.State {
	prev, had := daemonstate.ReadState(t.p.Root)
	if had && prev.Same(st) {
		return prev
	}
	if err := daemonstate.WriteState(t.p.Root, st); err != nil {
		t.d.logf("%s: %v", t.p.Name, err)
		return st
	}
	if st.Kind == string(ActIdle) && t.d.NotifyIdle != nil {
		switch {
		case strings.HasPrefix(st.Reason, string(nexttask.ReasonWaitingOnOwner)):
			t.d.NotifyIdle(t.p, fmt.Sprintf("%s is waiting on you", t.p.Name), st.Reason)
		case strings.HasPrefix(st.Reason, string(nexttask.ReasonBlockedByFailed)):
			t.d.NotifyIdle(t.p, fmt.Sprintf("%s is blocked by a failed task", t.p.Name), st.Reason)
		}
	}
	t.governorChanged(prev, had, st)
	return st
}

// record appends one line to the decision log. It is called only for a
// transition (the ledger changed, an item was added or answered, tokens were
// recorded), never for a state that merely persists, so the log's length is
// the number of things that happened and not the number of ticks.
func (t *pass) record(kind, task, sess, question, chosen, rule string) {
	err := decisionlog.Append(decisionlog.Path(t.p.Root), decisionlog.Entry{
		At:       stamp(t.now),
		Task:     task,
		Session:  sess,
		Kind:     kind,
		Question: question,
		Chosen:   chosen,
		ChosenBy: decisionlog.ChosenByPolicy,
		Rule:     rule,
	})
	if err != nil {
		t.d.logf("%s: the decision log could not be written: %v", t.p.Name, err)
	}
}

// add stores an inbox item and tells the owner about it.
func (t *pass) add(n inbox.NewItem) (inbox.Item, error) {
	it, err := t.store.Add(n)
	if err != nil {
		t.d.logf("%s: the inbox item %q could not be written: %v", t.p.Name, n.Title, err)
		return inbox.Item{}, err
	}
	if t.d.Notify != nil {
		t.d.Notify(t.p, it)
	}
	return it, nil
}

// openTitled finds an open item with exactly this title.
func openTitled(items []inbox.Item, title string) (inbox.Item, bool) {
	for _, it := range items {
		if it.Open() && it.Title == title {
			return it, true
		}
	}
	return inbox.Item{}, false
}

// openTitledFor finds an open item with exactly this title that is about
// this task.
func openTitledFor(items []inbox.Item, title, task string) (inbox.Item, bool) {
	for _, it := range items {
		if it.Open() && it.Title == title && it.Task == task {
			return it, true
		}
	}
	return inbox.Item{}, false
}

// fitTitle cuts a title to what the inbox stores. The cut is by character,
// not by byte, so it cannot land inside one, and it is the same every time
// for the same text. That matters because a title is how the loop finds an
// item it raised earlier.
func fitTitle(title string) string {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) <= maxItemTitle {
		return title
	}
	r := []rune(title)
	return strings.TrimSpace(string(r[:maxItemTitle-1])) + "…"
}

// change makes one transition of one ledger entry, and refuses when the
// entry on disk is not in the status this tick decided from.
//
// The check is what keeps a tick honest about its own observation. The
// ledger was read some time before this write, and `afk ledger set` can run
// in between; ledger.Update re-reads under its lock, so the callback sees
// the entry as it is now. Writing over a status the owner has just set by
// hand would undo their change with nothing to show for it.
func (t *pass) change(id string, from ledger.Status, fn func(e *ledger.Entry)) error {
	return ledger.Update(t.p.Root, func(l *ledger.Ledger) error {
		for i := range l.Tasks {
			if l.Tasks[i].ID != id {
				continue
			}
			if l.Tasks[i].Status != from {
				return errLedgerMoved
			}
			fn(&l.Tasks[i])
			return nil
		}
		// A task with no entry is pending (PLAN §4.2).
		if from != ledger.Pending {
			return errLedgerMoved
		}
		e := ledger.Entry{ID: id, Status: ledger.Pending}
		fn(&e)
		l.Tasks = append(l.Tasks, e)
		return nil
	})
}

// ledgerTrouble is the state of a tick whose ledger write failed. The tick
// ends there; the next one decides again from disk.
func (t *pass) ledgerTrouble(a Action, err error) daemonstate.State {
	t.d.logf("%s: the ledger could not be changed for %s (%s): %v", t.p.Name, a.Task, a.Kind, err)
	return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
		Reason: fmt.Sprintf("the ledger could not be changed for %s (%v); trying again on the next pass", a.Task, err)}
}

// launch starts the next task: preflight, then the ledger, then the session.
func (t *pass) launch(a Action) daemonstate.State {
	blocked := func(id string) daemonstate.State {
		return daemonstate.State{Kind: KindBlocked, Task: a.Task,
			Reason: fmt.Sprintf("%s; see inbox item %s", PreflightTitle, id)}
	}

	// An open item means preflight already failed and the owner has not
	// said the machine is fixed. Preflight is not run again until they do:
	// it starts the agent CLI's plugin listing, which must not run on every
	// tick, and the answer is the owner's way of saying "try again".
	if it, ok := openTitled(t.obs.Items, PreflightTitle); ok {
		return blocked(it.ID)
	}

	if ok, steps := t.d.Preflight(t.p); !ok {
		body := strings.TrimSpace(steps)
		if body == "" {
			body = "The check that this machine can start an agent without a dialog failed, and gave no steps."
		}
		body += "\n\nNothing is started until this item is answered. When the machine is fixed, answer it and afk checks again."
		it, err := t.add(inbox.NewItem{Kind: "failure", Blocking: true, Title: PreflightTitle, Body: body})
		if err != nil {
			return daemonstate.State{Kind: KindBlocked, Task: a.Task,
				Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v)", PreflightTitle, err)}
		}
		t.record("preflight_failed", a.Task, "",
			"Can this machine start an agent in the unattended mode without a dialog?",
			fmt.Sprintf("no; nothing is launched until inbox item %s is answered", it.ID), rulePreflight)
		return blocked(it.ID)
	}

	// The ledger is written before the session is created. Killed between
	// the two, the next tick finds a running entry with no session and
	// creates that same session (ActCreate), as the same attempt. The other
	// order would leave a session nobody is watching and a ledger that
	// starts the task a second time beside it.
	var name string
	err := t.change(a.Task, ledger.Pending, func(e *ledger.Entry) {
		attempt := e.Attempts + 1
		name = SessionName(e.ID, attempt)
		e.Status = ledger.Running
		e.Session, e.Branch = name, name
		e.Attempts = attempt
		e.StartedAt = stamp(t.now)
		e.ParkedOn = nil
		e.FinishedAt = ""
		// A new attempt ships from the beginning. An entry can come here
		// holding an earlier attempt's ship: parked in the middle of one,
		// then set back to pending by hand. Left in place, the new attempt
		// would reach shipping already "holding" the old pull request, look
		// for it under a branch that never had one, and park for ever.
		e.Ship, e.ShipAt = "", ""
		e.CIReruns, e.CIFixCycles = 0, 0
		e.PR, e.MergedSHA = 0, ""
		// And with a fresh set of restarts, for the same reason: the count
		// and the pending prompt belong to the session of the attempt before.
		// So does a pause for usage: a new attempt is a new conversation.
		clearRecovery(e)
		clearUsagePause(e)
	})
	if err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("launch", a.Task, name,
		fmt.Sprintf("Which task runs next in %s?", t.p.Name),
		fmt.Sprintf("%s, in session %s", a.Task, name), ruleCreate)
	return t.start(ActLaunch, a.Task, name, fmt.Sprintf("started %s in session %s", a.Task, name))
}

// start creates the session for a running entry. It is the second half of a
// launch, and all of an ActCreate: the entry's recorded session is created
// again as the same attempt.
//
// An ActCreate makes one ledger change, and only when there is something to
// clear: the entry's recovery fields go back to zero before the session is
// created. They describe the session that is no longer on disk. Left in
// place, a pending continue prompt would be typed into the new session on top
// of the first prompt it was just started with, and the old session's
// restarts would count against a session that has never been restarted. A
// launch has cleared them already in its own ledger write. A paused project
// never gets here (Decide waits instead of creating).
//
// The two usage-pause fields are cleared in that same change, on their own
// condition, since an entry can hold them with no recovery field set. They
// too describe the session that is gone. Left in place, the next tick would
// read the new session as one paused for usage: it would stop the agent that
// was just started, or resume a conversation and type a resume command over
// the new session's first prompt.
func (t *pass) start(kind ActionKind, task, name, reason string) daemonstate.State {
	if e, ok := t.obs.Ledger.Get(task); kind == ActCreate && ok && (hasRecovery(e) || hasUsagePause(e)) {
		if err := t.change(task, ledger.Running, func(e *ledger.Entry) {
			clearRecovery(e)
			clearUsagePause(e)
		}); err != nil {
			return t.ledgerTrouble(Action{Kind: kind, Task: task, Session: name}, err)
		}
	}
	req := LaunchRequest{
		Project:     t.p,
		Task:        task,
		Session:     name,
		Description: fmt.Sprintf("Implement %s from %s", task, t.relPlan()),
	}
	if err := t.d.Launch(req); err != nil {
		return t.launchFailed(task, name, err)
	}
	return daemonstate.State{Kind: string(kind), Task: task, Session: name, Reason: reason}
}

// relPlan is the master plan's path as the agent should be told it: relative
// to the main checkout, because the agent works in a worktree where that
// same relative path names the same file.
func (t *pass) relPlan() string {
	if t.planPath == "" {
		return "the master plan"
	}
	rel, err := filepath.Rel(t.p.Root, t.planPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(t.planPath)
	}
	return filepath.ToSlash(rel)
}

// launchFailed marks a task failed because its session could not be created,
// and tells the owner how to put it right.
//
// The item is added before the ledger is written and looked for first. If
// the tick dies between the two, the next one tries the session again, fails
// again, finds the item it already raised and only finishes the ledger; the
// owner is told once either way.
func (t *pass) launchFailed(task, name string, cause error) daemonstate.State {
	a := Action{Kind: ActFail, Task: task, Session: name}
	title := fmt.Sprintf("%s could not be started (session %s)", task, name)
	it, have := openTitled(t.obs.Items, title)
	if !have {
		body := fmt.Sprintf("afk could not create the session %s for %s:\n\n%v\n\n"+
			"What to do next:\n"+
			"1. Run `afk clean %s` to remove a half-made session, if one was left behind.\n"+
			"2. Run `afk ledger set %s pending` so afk tries the task again.",
			name, task, cause, name, task)
		// A failed add is logged by add; the ledger still has to say failed,
		// or the loop would try the same broken launch on every tick.
		it, _ = t.add(inbox.NewItem{Task: task, Session: name, Kind: "failure", Title: title, Body: body})
	}
	if err := t.change(task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Failed
		e.FinishedAt = stamp(t.now)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("task_failed", task, name,
		fmt.Sprintf("The session for %s could not be created. What now?", task),
		fmt.Sprintf("mark %s failed and tell the owner: %v", task, cause), ruleCreate)
	reason := fmt.Sprintf("%s could not be started: %v", task, cause)
	if it.ID != "" {
		reason += fmt.Sprintf("; see inbox item %s", it.ID)
	}
	return daemonstate.State{Kind: string(ActFail), Task: task, Session: name, Reason: reason}
}

// tokens records the finished session's token use. It never fails the task:
// the work is done whether or not its cost could be counted, so an error is
// logged, written to the decision log, and left behind.
func (t *pass) tokens(a Action) {
	id := ""
	if t.row != nil {
		id = t.row.SessionID
	}
	question := fmt.Sprintf("What did the session for %s use?", a.Task)
	n, err := t.d.RecordTokens(t.p, a.Task, a.Session, id)
	if err != nil {
		t.d.logf("%s: token use for %s (%s) was not recorded: %v", t.p.Name, a.Task, a.Session, err)
		t.record("tokens", a.Task, a.Session, question, fmt.Sprintf("not recorded: %v", err), ruleTokens)
		return
	}
	t.record("tokens", a.Task, a.Session, question, fmt.Sprintf("recorded %d token ledger lines", n), ruleTokens)
}

// ship hands a finished task to shipping. That is all it does: the entry
// becomes `shipping` with no ship state, and every tick after this one is a
// shipStep. With no shipper (Deps.Ship nil) the task waits there until the
// owner moves it by hand.
func (t *pass) ship(a Action) daemonstate.State {
	t.tokens(a)
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Shipping
		clearRecovery(e)
		clearUsagePause(e)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("ship_wait", a.Task, a.Session,
		fmt.Sprintf("The session for %s finished. What now?", a.Task),
		"hand it to shipping", ruleWatch)
	return stateOf(a)
}

// fail marks a task failed after its session ended as failed, with the
// review's verdict in an inbox item. The item comes before the ledger write
// and is looked for first, for the reason given on launchFailed.
func (t *pass) fail(a Action) daemonstate.State {
	t.tokens(a)
	title := fmt.Sprintf("%s failed (session %s)", a.Task, a.Session)
	if _, have := openTitled(t.obs.Items, title); !have {
		_, _ = t.add(inbox.NewItem{Task: a.Task, Session: a.Session, Kind: "failure", Title: title, Body: t.failBody(a)})
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Failed
		e.FinishedAt = stamp(t.now)
		clearRecovery(e)
		clearUsagePause(e)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("task_failed", a.Task, a.Session,
		fmt.Sprintf("The session for %s ended as failed. What now?", a.Task),
		fmt.Sprintf("mark %s failed, tell the owner and move on", a.Task), ruleWatch)
	return stateOf(a)
}

// failBody is the body of a failed task's item: what the review said when
// there is a verdict, and where to look when there is not.
func (t *pass) failBody(a Action) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The session %s for %s ended as failed.\n\n", a.Session, a.Task)
	where := ""
	if t.row != nil && t.row.WorktreePath != "" {
		where = filepath.Join(t.row.WorktreePath, ".afk", a.Session)
	}
	if t.row != nil && t.row.Verdict != nil {
		v := t.row.Verdict
		fmt.Fprintf(&b, "Checks:\n- Compliance: %s\n- Quality: %s\n- QA: %s\n", v.Compliance, v.Quality, v.QA)
		var lines []string
		for _, f := range v.Findings {
			if f.Severity == "blocker" || f.Severity == "major" {
				lines = append(lines, fmt.Sprintf("- [%s] %s: %s", f.Severity, f.TaskID, f.Description))
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\nFindings:\n%s\n", strings.Join(lines, "\n"))
		}
		if where != "" {
			fmt.Fprintf(&b, "\nThe full verdict and the session's notes are in %s.\n", where)
		}
	} else if where != "" {
		fmt.Fprintf(&b, "It left no review verdict afk could read. The session's files are in %s.\n", where)
	} else {
		fmt.Fprintf(&b, "It left no review verdict afk could read. The session's files are in its worktree, under .afk/%s.\n", a.Session)
	}
	fmt.Fprintf(&b, "\nWhat to do next: afk has moved on to any task that does not depend on this one. "+
		"To try %s again in a new session, run `afk ledger set %s pending`.", a.Task, a.Task)
	return b.String()
}

// park records that a task waits on the owner. Its session is left alone.
func (t *pass) park(a Action) daemonstate.State {
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = slices.Clone(a.Items)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("park", a.Task, a.Session,
		fmt.Sprintf("%s has a question only the owner can answer. What now?", a.Task),
		fmt.Sprintf("park it on inbox %s", strings.Join(a.Items, ", ")), ruleWatch)
	return stateOf(a)
}

// unpark puts a parked task back where Decide said: running or shipping. No
// session is started: the one it had is still there, and its original start
// time is kept, because that time is what Decide's once-per-attempt limit
// measures from.
//
// Back to shipping, the ship's clock is set to now and both CI counters to
// zero. The owner's answer buys a fresh set of attempts. Every ship park that
// comes from a limit (no checks in time, no commit after a hand-back, too
// many hand-backs) is decided from that clock or those counters, so with the
// old values the very next step would park again on the same limit, for ever.
//
// The recovery fields are cleared on both paths for the same reason. A task
// parked because its session could not be recovered holds a restart count at
// the limit, and the next tick would give up on it again. A pending continue
// prompt is dropped as well: resume, below, types the same line.
//
// The two usage-pause fields are another matter. Back to running they are
// kept, and nothing is typed: a task that parked while its session was paused
// for usage has no host to type into, and the kept fields are what make the
// next tick start it again in the same conversation (Decide's rule u) and not
// take the missing host for a crash. Back to shipping they are cleared: the
// session has finished, so there is nothing left to pause or to start again.
func (t *pass) unpark(a Action) daemonstate.State {
	if a.Resume == ledger.Shipping {
		if err := t.change(a.Task, ledger.Parked, func(e *ledger.Entry) {
			e.Status = ledger.Shipping
			e.ParkedOn = nil
			e.ShipAt = stamp(t.now)
			e.CIReruns, e.CIFixCycles = 0, 0
			clearRecovery(e)
			clearUsagePause(e)
		}); err != nil {
			return t.ledgerTrouble(a, err)
		}
		t.record("unpark", a.Task, a.Session,
			fmt.Sprintf("Every item %s was parked on is answered. What now?", a.Task),
			"go on shipping from where it stopped, with a fresh set of attempts", ruleShip)
		return stateOf(a)
	}
	if err := t.change(a.Task, ledger.Parked, func(e *ledger.Entry) {
		e.Status = ledger.Running
		e.ParkedOn = nil
		clearRecovery(e)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("unpark", a.Task, a.Session,
		fmt.Sprintf("Every item %s was parked on is answered. What now?", a.Task),
		"mark it running again; its session is still there", ruleWatch)
	// After the ledger, so the line is typed at most once: a daemon killed
	// between the two leaves a running task whose session was not told, and
	// the stall rule is what catches that.
	if e, _ := t.obs.Ledger.Get(a.Task); !hasUsagePause(e) {
		t.resume(a)
	}
	return stateOf(a)
}

// resumeCommand is the line that makes a session pick its work up again from
// its own files. The loop types it into a session it has un-parked (resume)
// or recovered (continueSession), and the stall and stopped-host items give
// the owner the same line to type by hand.
func resumeCommand(sess string) string {
	return "/afk:go --session " + sess
}

// resume types the session's resume command into a session that has just
// been un-parked to running, and nothing else: the command alone on the line.
//
// It is that command and not a sentence such as "your items are answered,
// carry on" because of how a session parks. The conductor's park contract
// (plugins/afk/skills/conductor/prompts/autonomous.md §2 rule 3) has it write
// awaiting_input: true and end its turn, and says (step d) that this command
// is what resumes it: the command's start-up write is the thing that clears
// the flag. A plain sentence would get the agent working with the flag still
// set, and Decide's stall rule would park the session again ten minutes
// later. The answers themselves are not typed: the session reads them from
// the inbox when it resumes.
//
// Nothing is typed when the session is not on disk (the next tick creates it
// again, which starts it) or has ended as failed (the next tick marks the
// task failed). A session whose phase is done never gets here: Decide sends
// it back to shipping.
//
// A send that fails is logged and nothing more. The task is un-parked either
// way; a session that was not woken still shows awaiting_input, and the stall
// rule tells the owner.
func (t *pass) resume(a Action) {
	if t.d.SendPrompt == nil {
		return
	}
	v, row := t.obs.Parked[a.Task], t.parkedRows[a.Task]
	if v == nil || row == nil {
		return
	}
	if !v.Stale && (v.Phase == state.PhaseDone || v.Phase == state.PhaseFailed) {
		return
	}
	if err := t.d.SendPrompt(row.WorktreePath, a.Session, resumeCommand(a.Session)); err != nil {
		t.d.logf("%s: %s was un-parked, but its session %s could not be told to carry on: %v",
			t.p.Name, a.Task, a.Session, err)
	}
}

// progressOf reads a ship's progress out of a ledger entry. A ship_at that
// cannot be read is the zero time, which every timed rule of the ship reads
// as "long ago".
func progressOf(e ledger.Entry) ship.Progress {
	at, _ := parseTime(e.ShipAt)
	return ship.Progress{
		State: ship.State(e.Ship), At: at, PR: e.PR,
		Reruns: e.CIReruns, FixCycles: e.CIFixCycles, MergedSHA: e.MergedSHA,
	}
}

// shipStep takes a shipping task's ship one step forward and acts on what the
// ship says: wait, park, stop the line, or done.
//
// The loop's part is the files it owns. The ledger entry is where the ship's
// progress lives, written through Save; the inbox is where a park or a stop
// reaches the owner; the stop marker is written through Stop. What the step
// actually is (a push, a merge, a health check) happens behind Deps.Ship.
func (t *pass) shipStep(a Action) daemonstate.State {
	if t.d.Ship == nil {
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
			Reason: fmt.Sprintf("%s is waiting to ship", a.Task)}
	}
	e, _ := t.obs.Ledger.Get(a.Task)
	req := ShipRequest{Project: t.p, Entry: e, Progress: progressOf(e)}
	if t.row != nil {
		req.Worktree, req.Base = t.row.WorktreePath, t.row.BaseBranch
	}

	// last is the state the ledger holds, so that the decision log gets one
	// line per change of state and none for a save that only moves a counter
	// or the clock. It is a local of this one call, not something kept.
	last := req.Progress.State
	req.Save = func(p ship.Progress) error {
		err := t.change(a.Task, ledger.Shipping, func(e *ledger.Entry) {
			e.Ship = string(p.State)
			e.ShipAt = ""
			if !p.At.IsZero() {
				e.ShipAt = stamp(p.At)
			}
			e.PR = p.PR
			e.CIReruns, e.CIFixCycles = p.Reruns, p.FixCycles
			e.MergedSHA = p.MergedSHA
		})
		if err != nil {
			return err
		}
		if p.State != last && p.State != ship.NotStarted {
			t.record("ship", a.Task, a.Session,
				fmt.Sprintf("How far has the ship of %s got?", a.Task), string(p.State), ruleShip)
		}
		last = p.State
		return nil
	}
	req.Stop = func(task, reason string) error {
		return daemonstate.Stop(t.p.Root, task, reason, t.now)
	}

	out := t.d.Ship(req)
	switch out.Kind {
	case ship.OutcomePark:
		return t.shipPark(a, out)
	case ship.OutcomeStop:
		return t.shipStop(a, out)
	case ship.OutcomeDone:
		return t.shipDone(a, out)
	}
	return t.shipWait(a, out)
}

// shipWait is the state of a ship that has nothing for the loop to do.
//
// A ship in its stopped state is stored as the stopped line, with the task
// named, because that is what the owner has to act on: the task is not
// waiting on GitHub, it is waiting for `afk resume`.
func (t *pass) shipWait(a Action, out ship.Outcome) daemonstate.State {
	if out.Progress.State == ship.Stopped && t.obs.Stopped {
		t.stopNotTold(a, out.Progress.At)
		return t.stoppedState(a)
	}
	reason := strings.TrimSpace(out.Reason)
	if reason == "" {
		reason = "nothing to do on this pass"
	}
	return daemonstate.State{Kind: string(ActShipStep), Task: a.Task, Session: a.Session,
		Reason: fmt.Sprintf("shipping %s: %s", a.Task, reason)}
}

// stoppedState is the stored state of a task whose ship stopped the line, be
// it the deploy or the after-merge command. The reason it shows is the one
// the stop marker holds, which the ship worded for the cause.
func (t *pass) stoppedState(a Action) daemonstate.State {
	return daemonstate.State{Kind: KindLineStopped, Task: a.Task, Session: a.Session,
		Reason: fmt.Sprintf("%s; %s is marked done once it is cleared", stoppedReason(t.obs), a.Task)}
}

// parkShipping parks a shipping task on one blocking item, keeping its ship
// fields so the answer carries on from the same state.
//
// The item is added before the ledger is written, because the entry needs its
// id, and it is looked for first. If the tick dies between the two, the task
// is still shipping; the next step parks again for the same reason, with the
// same title, finds the open item and only finishes the ledger. The owner is
// told once. That is why a ship's titles hold no counts and no times.
func (t *pass) parkShipping(a Action, n inbox.NewItem, why string) daemonstate.State {
	n.Task, n.Session, n.Blocking = a.Task, a.Session, true
	n.Title = fitTitle(n.Title)
	it, have := openTitledFor(t.obs.Items, n.Title, a.Task)
	if !have {
		var err error
		if it, err = t.add(n); err != nil {
			return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session, Reason: fmt.Sprintf(
				"%s needs the owner (%s), and the inbox item saying so could not be written (%v); trying again on the next pass",
				a.Task, why, err)}
		}
	}
	if err := t.change(a.Task, ledger.Shipping, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = []string{it.ID}
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("park", a.Task, a.Session,
		fmt.Sprintf("The ship of %s cannot go on by itself: %s. What now?", a.Task, why),
		fmt.Sprintf("park it on inbox %s; the answer carries on from where it stopped", it.ID), ruleShip)
	return daemonstate.State{Kind: string(ActPark), Task: a.Task, Session: a.Session,
		Reason: fmt.Sprintf("%s is waiting on the owner: %s; see inbox item %s", a.Task, why, it.ID)}
}

// shipPark parks the task on the item the ship wrote.
func (t *pass) shipPark(a Action, out ship.Outcome) daemonstate.State {
	why := strings.TrimSpace(out.Reason)
	if why == "" {
		why = "the ship stopped and gave no reason"
	}
	n := inbox.NewItem{Kind: ship.ItemFailure, Title: fmt.Sprintf("%s could not be shipped", a.Task),
		Body: upperFirst(why) + ".\n\nAnswer this item and afk tries again."}
	if out.Item != nil {
		n.Kind, n.Title, n.Body = out.Item.Kind, out.Item.Title, out.Item.Body
	}
	return t.parkShipping(a, n, why)
}

// shipStop tells the owner the line is stopped. The marker is already there:
// the ship wrote it, through ShipRequest.Stop, before it saved the stopped
// state. The task stays `shipping`, and its ship finishes it once `afk
// resume` has removed the marker.
func (t *pass) shipStop(a Action, out ship.Outcome) daemonstate.State {
	n := inbox.NewItem{Kind: ship.ItemFailure, Title: stopLostTitle(a.Task), Body: stopLostBody(a.Task, out.Reason)}
	if out.Item != nil {
		n.Kind, n.Title, n.Body = out.Item.Kind, out.Item.Title, out.Item.Body
	}
	t.tellStopped(a, n, out.Reason)
	// The marker was written during this tick, after the observation was
	// made, so the observation is brought up to date for the reason below.
	t.obs.Stopped = true
	if strings.TrimSpace(t.obs.StopReason) == "" {
		t.obs.StopReason = out.Reason
	}
	return t.stoppedState(a)
}

// tellStopped raises the item for a stopped line, unless an open one with the
// same title is already there, and sends it at the highest priority.
//
// The item does not block and the task is not parked on it. What holds the
// line is the marker, and what clears it is `afk resume`; an item the owner
// could answer to "release" the task would be a second way out that leaves
// the marker behind. That is as true of a stop after the merge, when the
// after-merge command failed, as of a deploy that did not come up.
func (t *pass) tellStopped(a Action, n inbox.NewItem, reason string) {
	n.Task, n.Session, n.Blocking = a.Task, a.Session, false
	n.Title = fitTitle(n.Title)
	if _, have := openTitledFor(t.obs.Items, n.Title, a.Task); have {
		return
	}
	it, err := t.store.Add(n)
	if err != nil {
		t.d.logf("%s: the inbox item %q could not be written: %v", t.p.Name, n.Title, err)
		return
	}
	switch {
	case t.d.NotifyUrgent != nil:
		t.d.NotifyUrgent(t.p, it)
	case t.d.Notify != nil:
		t.d.Notify(t.p, it)
	}
	why := strings.TrimSpace(reason)
	if why == "" {
		why = "the ship gave no reason"
	}
	t.record("line_stopped", a.Task, a.Session,
		fmt.Sprintf("The line was stopped after %s: %s. What now?", a.Task, why),
		fmt.Sprintf("stop the line until `afk resume` and tell the owner (inbox %s): %s", it.ID, why), ruleShip)
}

// stopNotTold covers one gap: the ship saved its stopped state and the daemon
// was killed before the item was added. The ship then only waits (the marker
// is there, so it has nothing to report), and without this the owner would
// have a stopped line and no message saying so.
//
// "Not told" is worked out from the inbox, like every once-only rule here:
// no failure item for this task, open or answered, created at or after the
// moment the ship stopped. At, not only after, because times are stored to
// the second. An item whose time cannot be read counts as the message, the
// same lean towards not repeating that reported takes.
func (t *pass) stopNotTold(a Action, stoppedAt time.Time) {
	for _, it := range t.obs.Items {
		if it.Task != a.Task || it.Kind != ship.ItemFailure {
			continue
		}
		created, ok := parseTime(it.CreatedAt)
		if !ok || !created.Before(stoppedAt.Truncate(time.Second)) {
			return
		}
	}
	t.tellStopped(a, inbox.NewItem{Kind: ship.ItemFailure, Title: stopLostTitle(a.Task),
		Body: stopLostBody(a.Task, t.obs.StopReason)}, t.obs.StopReason)
}

// stopLostTitle and stopLostBody are the loop's own words for a stopped line,
// used when the ship's item is not to hand.
func stopLostTitle(task string) string {
	return fmt.Sprintf("The line is stopped after %s", task)
}

func stopLostBody(task, reason string) string {
	why := strings.TrimSpace(reason)
	if why == "" {
		why = "the ship gave no reason"
	}
	return fmt.Sprintf("%s was merged, but afk stopped the line afterwards: %s. No new task starts until you clear it.\n\n"+
		"Look at what went wrong and put it right, then run `afk resume` to clear the stop; "+
		"%s is marked done once it is cleared.", task, why, task)
}

// shipDone finishes a shipped task: clean its session up, then mark it done.
//
// In that order, so that a task never says done while its worktree and branch
// are still lying about. The cost is the gap between the two: a daemon killed
// there comes back to a shipping task with no session on disk. That is why
// shipStep runs with no session at all, and why Clean must treat a session
// that is not there as cleaned.
func (t *pass) shipDone(a Action, out ship.Outcome) daemonstate.State {
	if t.d.Clean != nil {
		if err := t.d.Clean(t.p, a.Session); err != nil {
			return t.parkShipping(a, inbox.NewItem{
				Kind:  ship.ItemFailure,
				Title: fmt.Sprintf("%s is merged, but its session %s could not be cleaned up", a.Task, a.Session),
				Body: fmt.Sprintf("%s is merged and its deploy is fine. The last step is removing the session %s "+
					"(its worktree, its files and its merged branch), and that failed:\n\n    %v\n\n"+
					"What to do next:\n"+
					"1. Run `afk clean %s`. If that fails too, remove the worktree folder and delete the branch %s by hand.\n"+
					"2. Then answer this item. afk tries the clean-up again (a session that is already gone is fine) "+
					"and marks %s done.",
					a.Task, a.Session, err, a.Session, a.Session, a.Task),
			}, fmt.Sprintf("its session %s could not be cleaned up", a.Session))
		}
	}
	if err := t.change(a.Task, ledger.Shipping, func(e *ledger.Entry) {
		e.Status = ledger.Done
		e.FinishedAt = stamp(t.now)
		e.ParkedOn = nil
		// The ledger already holds the merge commit, saved when the merge
		// was seen. The outcome's copy is written only when it has one, so a
		// ship that reports done with an empty progress cannot erase it.
		if out.Progress.MergedSHA != "" {
			e.MergedSHA = out.Progress.MergedSHA
		}
		e.Ship, e.ShipAt = "", ""
		e.CIReruns, e.CIFixCycles = 0, 0
		clearRecovery(e)
		clearUsagePause(e)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("shipped", a.Task, a.Session,
		fmt.Sprintf("%s is merged and its deploy is fine. What now?", a.Task),
		fmt.Sprintf("clean up the session %s and mark %s done", a.Session, a.Task), ruleShip)
	return daemonstate.State{Kind: string(ActShipStep), Task: a.Task, Session: a.Session,
		Reason: fmt.Sprintf("%s is shipped and done", a.Task)}
}

// report raises the item for a stalled session or a dead host and parks the
// task on it.
//
// The item is added first because the ledger entry needs its id. If the tick
// dies before the ledger is written, the next one sees an open blocking item
// for the task and parks on it (Decide's rule e), so no second item is
// raised.
func (t *pass) report(a Action) daemonstate.State {
	n := inbox.NewItem{Task: a.Task, Session: a.Session, Kind: "failure", Blocking: true}
	kind := "stall"
	if a.Kind == ActStall {
		n.Title = StallTitlePrefix + fmt.Sprintf("%s (%s)", a.Task, a.Session)
		// Asked of the same observation Decide chose from, so the item says
		// what was decided: woken already, or why it was not.
		e, _ := t.obs.Ledger.Get(a.Task)
		woken, whyNot := stallWake(t.obs, e, t.obs.Session)
		n.Body = stallBody(a.Task, a.Session, woken, whyNot)
	} else {
		kind = "host_gone"
		n.Title = HostGoneTitlePrefix + fmt.Sprintf("%s (%s)", a.Task, a.Session)
		n.Body = hostGoneBody(a.Task, a.Session)
	}
	it, err := t.add(n)
	if err != nil {
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
			Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v); trying again on the next pass", a.Reason, err)}
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = []string{it.ID}
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record(kind, a.Task, a.Session,
		fmt.Sprintf("%s. What now?", upperFirst(a.Reason)),
		fmt.Sprintf("tell the owner and park %s on inbox %s", a.Task, it.ID), ruleWatch)
	st := stateOf(a)
	st.Reason = fmt.Sprintf("%s; see inbox item %s", a.Reason, it.ID)
	return st
}

// stallBody tells the owner what to do about a session that is asking
// something. `afk attach` is the right tool here because the host is alive:
// attach connects to a running host and never starts one.
//
// Its second sentence says what afk did about the wait before raising the
// item, because that changes what the owner should expect to find. woken
// means the resume command was typed and the session wrote nothing after it,
// so the agent is most likely asking a real question, or sitting in a dialog
// that swallowed the line. Otherwise nothing was typed and whyNot is the
// reason, in stallWake's words; typing the command by hand may then be all
// that is needed. An empty whyNot with woken false does not come from Decide
// (it wakes the session in that case and raises no item) and gets the plain
// sentence, so that a caller in a state this function did not foresee still
// tells the owner something true.
//
// The body cannot name its own item's id, which the store picks when the
// item is written, so it says where the id is shown.
func stallBody(task, sess string, woken bool, whyNot string) string {
	var did string
	switch {
	case woken:
		did = fmt.Sprintf("afk typed its resume command (`%s`) to set it going again, and it was still waiting afterwards, "+
			"so someone has to look.", resumeCommand(sess))
	case whyNot != "":
		did = fmt.Sprintf("afk did not wake it by typing its resume command: %s. So someone has to look.", whyNot)
	default:
		did = "afk did not wake it by typing its resume command, so someone has to look."
	}
	return fmt.Sprintf("The session for %s is asking something at the terminal, and has been for longer than afk waits. "+
		"%s\n\n"+
		"What to do next:\n"+
		"1. Run `afk attach %s` and answer what it is asking. If the agent is idle instead, type `%s` there.\n"+
		"2. Then answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
		"afk goes back to watching %s once it is answered.",
		task, did, sess, resumeCommand(sess), task)
}

// hostGoneBody tells the owner what to do about a session whose host has
// stopped. It deliberately does not send them to the attach command, which
// only connects to a live host and would report that there is none; the
// dashboard is what starts a stopped host again, and it does so in the mode
// the session was created in.
//
// This is the text for a loop with no Deps.Relaunch only, which is the one
// case where "afk cannot yet wake a session by itself" is still true of a
// stopped host. With a Relaunch the loop starts the host again by itself
// (relaunch), and what the owner is sent when that stops working is
// recoveryBody.
func hostGoneBody(task, sess string) string {
	return fmt.Sprintf("The session for %s has stopped running: its host process is gone. "+
		"afk cannot yet wake a session by itself, so someone has to look.\n\n"+
		"What to do next, either:\n"+
		"1. Run `afk dash` and open the session %s. That starts its stopped host again, in the same mode. "+
		"If the agent is idle there, type `%s`.\n"+
		"2. Then answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
		"afk goes back to watching %s once it is answered.\n\n"+
		"Or, if the session is beyond saving: run `afk clean %s`, then `afk ledger set %s pending`, "+
		"and afk starts the task again in a new session.",
		task, sess, resumeCommand(sess), task, sess, task)
}

// hasRecovery reports whether an entry holds anything clearRecovery would
// clear.
func hasRecovery(e ledger.Entry) bool {
	return e.Restarts != 0 || e.RestartAt != "" || e.ContinuePending || e.Wakes != 0 || e.WakeAt != ""
}

// clearRecovery puts an entry's recovery fields back to zero. It is called
// wherever the entry starts a new attempt, gets a new session, is un-parked,
// or leaves `running`: the count, the time and the pending prompt are about
// one session's current run of trouble and mean nothing after any of those.
//
// The two wake fields go with them, though a wake is not a restart and the
// two counts never touch each other. They share the clear points because
// they share the reason: a wake count at its limit left in an un-parked
// entry would hand the task straight back to the owner on the next failed
// turn, and an old wake time would be read as "already woken for this wait".
func clearRecovery(e *ledger.Entry) {
	e.Restarts, e.RestartAt, e.ContinuePending = 0, "", false
	e.Wakes, e.WakeAt = 0, ""
}

// restartedAt saves the moment a recovery finished as the entry's restart
// time, over the one saved before the recovery began.
//
// The clock is read again here, and the tick's own time is not used: that
// was read once, before the recovery, and a restart alone can spend several
// seconds stopping the old agent. Everything timed from restart_at (the wait
// before the continue prompt, the new host's grace) is about the new agent,
// so it is timed from when the new agent started.
//
// A save that fails is logged and nothing more. The earlier time is still
// there, and it only makes the prompt and the grace a few seconds early.
func (t *pass) restartedAt(a Action) {
	at := stamp(t.d.Now())
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) { e.RestartAt = at }); err != nil {
		t.d.logf("%s: the time %s (%s) was started again could not be saved: %v", t.p.Name, a.Task, a.Session, err)
	}
}

// noContinue clears the pending continue prompt after a recovery that did not
// bring an agent back: there is nobody to tell to carry on.
func (t *pass) noContinue(a Action) {
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) { e.ContinuePending = false }); err != nil {
		t.d.logf("%s: the ledger could not be changed for %s (%s): %v", t.p.Name, a.Task, a.Kind, err)
	}
}

// restart asks the host of a session that has stopped answering to restart
// its agent, resuming the same conversation.
//
// The ledger is written before the host is asked, and that order is the
// whole safety of the restart limit. The count lives only in the ledger. A
// daemon killed between the two must find the count already raised when it
// comes back; in the other order a daemon that dies after each restart (or a
// restart that takes the daemon down with it) would find the count unchanged
// every time and restart the same session without limit. The cost is a
// restart that is counted and never made, which only brings the limit one
// step closer.
//
// The continue prompt is marked pending in that same first write, so a
// daemon killed after the agent was restarted still tells it to carry on.
// What follows depends on the host's answer:
//
//   - restarted: the restart time is saved again (see restartedAt).
//   - refused: the host did nothing, so the pending prompt is dropped. The
//     count stays: a session that cannot be restarted must still reach the
//     limit and be handed to the owner.
//   - not known: the prompt stays pending, because the agent may well have
//     been restarted (the answer was lost, or a person attached and took the
//     connection). If it was not, the session is still wedged, Decide never
//     sends a prompt to a wedged session, and the next restart is asked for
//     after the backoff.
func (t *pass) restart(a Action) daemonstate.State {
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Restarts++
		e.RestartAt = stamp(t.now)
		e.ContinuePending = true
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}

	st := stateOf(a)
	var chosen string
	refused, err := t.d.Restart(t.row.WorktreePath, a.Session)
	switch {
	case err != nil:
		t.d.logf("%s: asked the session %s for %s to restart its agent, and what happened is not known: %v",
			t.p.Name, a.Session, a.Task, err)
		chosen = fmt.Sprintf("asked its host to restart the agent; not known whether it did: %v", err)
		st.Reason = fmt.Sprintf("asked the session for %s (%s) to restart; what happened is not known yet", a.Task, a.Session)
	case refused != "":
		t.noContinue(a)
		t.d.logf("%s: the session %s for %s refused to restart its agent: %s", t.p.Name, a.Session, a.Task, refused)
		chosen = fmt.Sprintf("asked its host to restart the agent; the host refused: %s", refused)
		st.Reason = fmt.Sprintf("the session for %s (%s) has stopped answering and could not be restarted: %s", a.Task, a.Session, refused)
	default:
		t.restartedAt(a)
		chosen = "restarted its agent, resuming the same conversation"
		st.Reason = fmt.Sprintf("restarted the session for %s (%s), which had stopped answering", a.Task, a.Session)
	}
	t.record("restart", a.Task, a.Session, fmt.Sprintf("%s. What now?", upperFirst(a.Reason)), chosen, ruleRecovery)
	return st
}

// relaunch starts a new host for a session whose host is gone.
//
// The new host resumes the agent's conversation when the session recorded
// its id, and only then is a continue prompt owed: a resumed agent sits at an
// empty prompt. With no id (the session had not got that far, or its status
// file cannot be read) the new host runs the session's ordinary launch
// command, which carries its own first prompt, and a second line typed on
// top of that would do harm.
//
// The ledger is written before the host is started, for the reason given on
// restart. A relaunch that fails drops the pending prompt (nothing was
// started) and keeps the count; the next one is tried after the host's grace
// and the backoff, both of which Decide times from the restart time saved
// here.
func (t *pass) relaunch(a Action) daemonstate.State {
	req := RelaunchRequest{Worktree: t.row.WorktreePath, Session: a.Session}
	// A stale view's other status fields mean nothing, the session id among
	// them, and resuming a conversation by an id that was not read is worse
	// than starting a fresh one.
	if !t.obs.Session.Stale {
		req.ResumeID = t.row.SessionID
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Restarts++
		e.RestartAt = stamp(t.now)
		e.ContinuePending = req.ResumeID != ""
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}

	how := "as a new conversation that picks the work up from the session's files"
	if req.ResumeID != "" {
		how = "resuming the same conversation"
	}
	st := stateOf(a)
	var chosen string
	if err := t.d.Relaunch(req); err != nil {
		t.noContinue(a)
		t.d.logf("%s: the session %s for %s could not be started again: %v", t.p.Name, a.Session, a.Task, err)
		chosen = fmt.Sprintf("tried to start its host again and could not: %v", err)
		st.Reason = fmt.Sprintf("the session for %s (%s) has stopped running and could not be started again: %v", a.Task, a.Session, err)
	} else {
		t.restartedAt(a)
		chosen = "started its host again, " + how
		st.Reason = fmt.Sprintf("started the session for %s (%s) again, %s", a.Task, a.Session, how)
	}
	t.record("relaunch", a.Task, a.Session, fmt.Sprintf("%s. What now?", upperFirst(a.Reason)), chosen, ruleRecovery)
	return st
}

// typeResume types the session's resume command into it and then clears the
// entry's pending flag. It is the one place the loop types that line into a
// running task's session, shared by the continue step and the wake so that
// the order below cannot differ between them.
//
// The line is typed first and the pending flag cleared after. A send that
// fails leaves the flag set, so the next tick tries again: the host may be
// busy with a person who has just attached. The other order would lose the
// prompt for good on one failed send, and an agent that is never told to
// carry on sits at an empty prompt until a person notices. The cost of this
// order is a daemon killed between the two, which types the line a second
// time; the command is safe to repeat.
//
// sent false means nothing was typed and err is why. sent true with an error
// means the line went out and the ledger could not be changed afterwards.
func (t *pass) typeResume(a Action) (sent bool, err error) {
	if err := t.d.SendPrompt(t.row.WorktreePath, a.Session, resumeCommand(a.Session)); err != nil {
		return false, err
	}
	return true, t.change(a.Task, ledger.Running, func(e *ledger.Entry) { e.ContinuePending = false })
}

// continueSession types the session's resume command into a session that was
// recovered and has had time to start, or that a wake could not reach on its
// first try. See resume for why it is that command and not a sentence: the
// same reasoning holds, and a restarted conductor acts from the phase and
// files it recorded.
//
// A send that fails records nothing: a failed send is not a thing that
// happened, and typeResume has left the flag set for the next tick.
func (t *pass) continueSession(a Action) daemonstate.State {
	sent, err := t.typeResume(a)
	if !sent {
		t.d.logf("%s: the session %s for %s is owed its resume command, but it could not be typed: %v; trying again on the next pass",
			t.p.Name, a.Session, a.Task, err)
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
			Reason: fmt.Sprintf("the session for %s (%s) could not be told to carry on yet (%v); trying again on the next pass",
				a.Task, a.Session, err)}
	}
	if err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("continue", a.Task, a.Session,
		fmt.Sprintf("%s. What now?", upperFirst(a.Reason)),
		fmt.Sprintf("typed `%s` into it", resumeCommand(a.Session)), ruleRecovery)
	return stateOf(a)
}

// wake types the session's resume command into a session whose agent's turn
// failed, or that has sat waiting at its prompt (PLAN T2.5). Nothing is
// restarted: the host and its agent are alive.
//
// The ledger is written before anything is typed, in one write: the count,
// the time and the pending flag. The count first for the reason given on
// restart: it lives only in the ledger, and a daemon that died after typing
// and before saving would come back to the same marker, find the count
// unchanged and wake the same session without limit. The pending flag in the
// same write is what makes the wake survive that death the other way round: a
// daemon killed after the save and before the line was typed comes back to an
// entry that says a line is owed, and the continue step types it once. The
// marker cannot ask for a second wake meanwhile, because it is now older than
// wake_at, and Decide never wakes an entry with a line pending.
//
// A send that fails is the same case reached without a crash. The flag stays
// set, the continue step types the line on a later tick, and the count is not
// raised again: one failure, one wake, however many tries the line takes.
//
// restarts and restart_at are left alone. A wake is not a recovery, and using
// them would let failed turns use up the restarts a hung agent needs, and
// would reset the grace and the backoff recovery times from restart_at.
//
// The decision is logged when it is taken, before the send, so the log holds
// every wake that was counted. When the line then goes out in this same tick
// no second line is logged for it; when it does not, the continue step's own
// line records the tick it did.
func (t *pass) wake(a Action) daemonstate.State {
	var n int
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Wakes++
		e.WakeAt = stamp(t.now)
		e.ContinuePending = true
		n = e.Wakes
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("wake", a.Task, a.Session,
		fmt.Sprintf("%s. What now?", upperFirst(a.Reason)),
		fmt.Sprintf("type `%s` into it (wake %d of %d); nothing is restarted", resumeCommand(a.Session), n, t.obs.MaxWakes),
		ruleRecovery)

	sent, err := t.typeResume(a)
	if !sent {
		t.d.logf("%s: the session %s for %s was to be woken, but its resume command could not be typed: %v; trying again on the next pass",
			t.p.Name, a.Session, a.Task, err)
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
			Reason: fmt.Sprintf("the session for %s (%s) was to be woken, but its resume command could not be typed yet (%v); trying again on the next pass",
				a.Task, a.Session, err)}
	}
	if err != nil {
		return t.ledgerTrouble(a, err)
	}
	return stateOf(a)
}

// wakeGiveUp raises the item for a session whose agent's turn failed with
// every wake used, and parks the task on it. It follows giveUp: the item
// first, because the ledger entry needs its id, and a tick that dies between
// the two is finished by Decide's rule e, which parks on the open item and
// raises no second one.
//
// The wake count is left in the parked entry, where `afk ledger` shows it,
// and is cleared by the un-park, which also types the resume command. The
// marker that led here is older than the owner's answer by then, so Decide
// does not count it against the fresh set.
func (t *pass) wakeGiveUp(a Action) daemonstate.State {
	e, _ := t.obs.Ledger.Get(a.Task)
	it, err := t.add(inbox.NewItem{
		Task: a.Task, Session: a.Session, Kind: "failure", Blocking: true,
		Title: fitTitle(WakeTitlePrefix + fmt.Sprintf("%s (%s)", a.Task, a.Session)),
		Body:  wakeBody(a.Task, a.Session, e.Wakes, t.obs.Session.TurnFailedReason, t.obs.Session.TurnFailedAt),
	})
	if err != nil {
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
			Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v); trying again on the next pass", a.Reason, err)}
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = []string{it.ID}
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("wake_give_up", a.Task, a.Session,
		fmt.Sprintf("%s. What now?", upperFirst(a.Reason)),
		fmt.Sprintf("tell the owner and park %s on inbox %s", a.Task, it.ID), ruleRecovery)
	st := stateOf(a)
	st.Reason = fmt.Sprintf("%s; see inbox item %s", a.Reason, it.ID)
	return st
}

// wakeBody tells the owner what to do about a session afk has stopped waking.
// It sends them to `afk attach`, and not to the dashboard as recoveryBody
// does, because this session's host is running: Decide gives up on waking
// only for a host that is alive and answering.
//
// With no wakes made the first sentence is a different one, for the reason
// given at recoveryBody: that happens only when the machine is set never to
// wake (max_wakes = 0), and "woke it 0 times" would read as a fault in afk.
//
// The reason is shown as the hook recorded it, in quotes, because it is the
// agent CLI's own word for the error and not a sentence of afk's. A marker
// with no reason is still a failed turn, and says so.
func wakeBody(task, sess string, wakes int, reason string, at time.Time) string {
	var first string
	switch {
	case wakes <= 0:
		first = fmt.Sprintf("The agent's turn failed in the session for %s, and afk is set not to wake sessions "+
			"(`max_wakes = 0` in afk's config.toml).", task)
	case wakes == 1:
		first = fmt.Sprintf("The agent's turn kept failing in the session for %s. afk woke it once by typing its resume command "+
			"and the turn failed again, so afk has stopped trying.", task)
	default:
		first = fmt.Sprintf("The agent's turn kept failing in the session for %s. afk woke it %d times by typing its resume command "+
			"and the turn failed again, so afk has stopped trying.", task, wakes)
	}
	why := "no reason was recorded"
	if r := strings.TrimSpace(reason); r != "" {
		why = fmt.Sprintf("the reason recorded was %q", r)
	}
	return first + fmt.Sprintf(" The last failure was at %s, and %s. The session is still open.\n\n"+
		"What to do next:\n"+
		"1. Run `afk attach %s` to look at what the agent last said. A fault at the agent's service usually passes by itself; "+
		"a message about your login or your account is one you have to fix first.\n"+
		"2. Then answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
		"afk types `%s` into the session and carries on with %s, with a fresh set of wakes.\n\n"+
		"Or, if the session is beyond saving: run `afk clean %s`, then `afk ledger set %s pending`, "+
		"and afk starts the task again in a new session.",
		wakeText(at), why, sess, resumeCommand(sess), task, sess, task)
}

// giveUp raises the item for a session that stopped or hung with every
// restart used, and parks the task on it. It follows report: the item first,
// because the ledger entry needs its id, and a tick that dies between the two
// is finished by Decide's rule e, which parks on the open item and raises no
// second one.
//
// The restart count is left in the parked entry, where `afk ledger` shows it,
// and is cleared by the un-park.
func (t *pass) giveUp(a Action) daemonstate.State {
	e, _ := t.obs.Ledger.Get(a.Task)
	it, err := t.add(inbox.NewItem{
		Task: a.Task, Session: a.Session, Kind: "failure", Blocking: true,
		Title: fitTitle(RecoveryTitlePrefix + fmt.Sprintf("%s (%s)", a.Task, a.Session)),
		Body:  recoveryBody(a.Task, a.Session, e.Restarts),
	})
	if err != nil {
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Session: a.Session,
			Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v); trying again on the next pass", a.Reason, err)}
	}
	if err := t.change(a.Task, ledger.Running, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = []string{it.ID}
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("give_up", a.Task, a.Session,
		fmt.Sprintf("%s. What now?", upperFirst(a.Reason)),
		fmt.Sprintf("tell the owner and park %s on inbox %s", a.Task, it.ID), ruleRecovery)
	st := stateOf(a)
	st.Reason = fmt.Sprintf("%s; see inbox item %s", a.Reason, it.ID)
	return st
}

// recoveryBody tells the owner what to do about a session afk has stopped
// trying to recover. It names both ways of looking, because it cannot know
// which fits: the dashboard starts a stopped host again, and attach connects
// to one that is still running.
//
// With no restarts made the first sentence is a different one. That happens
// only when the machine is set never to restart (max_restarts = 0), and
// "restarted it 0 times" would read as a fault in afk and not as the setting
// it is.
func recoveryBody(task, sess string, restarts int) string {
	var first string
	switch {
	case restarts <= 0:
		first = fmt.Sprintf("The session for %s stopped or hung, and afk is set not to restart sessions "+
			"(`max_restarts = 0` in afk's config.toml).", task)
	case restarts == 1:
		first = fmt.Sprintf("afk restarted the session for %s once and it stopped or hung again, so afk has stopped trying.", task)
	default:
		first = fmt.Sprintf("afk restarted the session for %s %d times and it stopped or hung again, so afk has stopped trying.", task, restarts)
	}
	return first + fmt.Sprintf("\n\n"+
		"What to do next, either:\n"+
		"1. Look at it. Run `afk dash` and open the session %s, which also starts its host again if that has stopped, "+
		"or run `afk attach %s` if it is still running. If the agent is idle there, type `%s`.\n"+
		"2. When it is working again, answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
		"afk carries on with %s and a fresh set of restarts.\n\n"+
		"Or, if the session is beyond saving: run `afk clean %s`, then `afk ledger set %s pending`, "+
		"and afk starts the task again in a new session.",
		sess, sess, resumeCommand(sess), task, sess, task)
}

// upperFirst capitalises a reason so it can open a sentence. Reasons from
// Decide are ASCII at their first byte.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// stop tells the owner the project cannot be run, once per distinct error.
//
// An open item with the same title and the same text means this error is
// already in front of the owner, and the tick adds nothing. A different
// error gets its own item, so fixing one problem and hitting another is not
// hidden behind the first report.
func (t *pass) stop(a Action) daemonstate.State {
	st := stateOf(a)
	if t.obs.InboxErr != nil {
		// Without the items there is no telling whether this was already
		// said, and saying it again every tick is worse than the log line.
		t.d.logf("%s: %s: %s (and the inbox could not be read: %v)", t.p.Name, StopTitle, a.Reason, t.obs.InboxErr)
		return st
	}
	body := strings.TrimSpace(a.Reason)
	for _, it := range t.obs.Items {
		if it.Open() && it.Title == StopTitle && strings.TrimSpace(it.Body) == body {
			return st
		}
	}
	it, err := t.add(inbox.NewItem{Kind: "failure", Title: StopTitle, Body: body})
	if err != nil {
		return st
	}
	t.record("plan_error", a.Task, "",
		fmt.Sprintf("Can %s be run?", t.p.Name),
		fmt.Sprintf("no; nothing starts until it is fixed (inbox %s): %s", it.ID, body), rulePlan)
	return st
}

// clearStops answers every open "cannot run this project" item once the
// project can be run again.
func (t *pass) clearStops() {
	var closed []string
	for _, it := range t.obs.Items {
		if !it.Open() || it.Title != StopTitle {
			continue
		}
		if _, err := t.store.Answer(it.ID, "fixed", "cli"); err != nil {
			// Already answered by the owner in the meantime is the same
			// outcome; anything else is tried again on the next tick.
			if !errors.Is(err, inbox.ErrAlreadyAnswered) {
				t.d.logf("%s: inbox item %s could not be answered: %v", t.p.Name, it.ID, err)
			}
			continue
		}
		closed = append(closed, it.ID)
	}
	if len(closed) > 0 {
		t.record("plan_fixed", "", "",
			fmt.Sprintf("Can %s be run?", t.p.Name),
			fmt.Sprintf("yes; answered inbox %s as fixed", strings.Join(closed, ", ")), rulePlan)
	}
}

// noLedger tells the owner, once, that the project has no ledger and how to
// make one. Nothing else happens on such a tick: no task is chosen, so no
// preflight runs and no session starts.
//
// "Once" is an open item with NoLedgerTitle, looked for on every tick. It is
// not a remembered flag, so a daemon started again finds the item its
// predecessor raised and adds nothing. An owner who answers the item without
// making the ledger is told again, which is right: the answer is the only
// way they have to say "look again", and the project still cannot be built.
func (t *pass) noLedger(a Action) daemonstate.State {
	st := stateOf(a)
	it, have := openTitled(t.obs.Items, NoLedgerTitle)
	if !have {
		var err error
		it, err = t.add(inbox.NewItem{Kind: "external_action", Blocking: true, Title: NoLedgerTitle, Body: noLedgerBody(t.p.Root)})
		if err != nil {
			st.Reason = fmt.Sprintf("%s (the inbox item saying so could not be written: %v)", a.Reason, err)
			return st
		}
		t.record("no_ledger", "", "",
			fmt.Sprintf("%s has no ledger file. Which tasks are already built?", t.p.Name),
			fmt.Sprintf("unknown, so nothing is started until the owner runs `afk ledger init` (inbox %s)", it.ID), rulePlan)
	}
	st.Reason = fmt.Sprintf("%s See inbox item %s.", a.Reason, it.ID)
	return st
}

// noLedgerBody says why nothing is running and gives both commands, because
// only the owner knows which of the two projects this is: a new one, where
// every task is still to do, or a part-built one, where starting from the
// first task would build finished work a second time.
func noLedgerBody(root string) string {
	return fmt.Sprintf("afk keeps a list of which tasks in the plan are finished (the ledger). The project at %s has none, "+
		"so afk cannot tell a new project from one that is already part-built. Nothing is started until the list exists.\n\n"+
		"What to do next, one of:\n"+
		"1. A new project, nothing built yet:\n"+
		"   afk ledger init --project %s\n"+
		"2. A project that is part-built. Say which tasks are finished:\n"+
		"   afk ledger init --project %s --done-through T1.4\n"+
		"   marks every task up to and including T1.4 as done (use your own last finished task). "+
		"Add `--done T2.1,T2.3` for finished tasks after that point.\n\n"+
		"You do not need to answer this item. afk closes it by itself once the list exists, and then starts the first task that is ready.",
		root, root, root)
}

// clearNoLedger answers every open "no task list" item once the ledger file
// exists, the way clearStops answers StopTitle.
func (t *pass) clearNoLedger() {
	var closed []string
	for _, it := range t.obs.Items {
		if !it.Open() || it.Title != NoLedgerTitle {
			continue
		}
		if _, err := t.store.Answer(it.ID, "created", "cli"); err != nil {
			if !errors.Is(err, inbox.ErrAlreadyAnswered) {
				t.d.logf("%s: inbox item %s could not be answered: %v", t.p.Name, it.ID, err)
			}
			continue
		}
		closed = append(closed, it.ID)
	}
	if len(closed) > 0 {
		t.record("ledger_found", "", "",
			fmt.Sprintf("Does %s have a ledger file now?", t.p.Name),
			fmt.Sprintf("yes; answered inbox %s as created", strings.Join(closed, ", ")), rulePlan)
	}
}

// ownerTitle is the title of the item for an owner task: the prefix, the id
// and the task's own title, cut to what the inbox stores.
//
// The cut is by character, not by byte, so it cannot land inside one, and it
// is the same every time for the same task. That matters because the title
// is how the loop finds the item it raised (see ownerTask).
func ownerTitle(id, title string) string {
	full := OwnerTitlePrefix + id
	if title = strings.TrimSpace(title); title != "" {
		full += " — " + title
	}
	return fitTitle(full)
}

// ownerTask hands a task to the owner: one blocking item, then the ledger
// entry goes from pending to parked on that item. No session is created and
// the entry never says running, so nothing can mistake it for agent work.
//
// The item is added before the ledger is written, because the entry needs
// its id, and it is looked for first. If the tick dies between the two, the
// next one decides ActOwner again (the task is still pending), finds the open
// item by its title and only finishes the ledger; the owner is told once.
//
// Only an open item counts. An answered one with the same title is from an
// earlier round: the owner set the task back to pending with `afk ledger
// set`, and parking it on an item that is already answered would mark it
// done again on the very next tick without asking.
func (t *pass) ownerTask(a Action) daemonstate.State {
	title := ""
	design := false
	for _, tk := range t.obs.Tasks {
		if tk.ID == a.Task {
			title = tk.Title
			design = tk.DesignSystem()
			break
		}
	}
	itemTitle := ownerTitle(a.Task, title)
	it, have := openTitled(t.obs.Items, itemTitle)
	if !have {
		var err error
		it, err = t.add(inbox.NewItem{Task: a.Task, Kind: "external_action", Blocking: true,
			Title: itemTitle, Body: ownerBody(a.Task, title, t.relPlan(), design)})
		if err != nil {
			return daemonstate.State{Kind: string(ActWait), Task: a.Task,
				Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v); trying again on the next pass", a.Reason, err)}
		}
	}
	if err := t.change(a.Task, ledger.Pending, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = []string{it.ID}
		e.FinishedAt = ""
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("owner_task", a.Task, "",
		fmt.Sprintf("%s is marked as the owner's task. Should an agent be started for it?", a.Task),
		fmt.Sprintf("no; ask the owner with inbox %s and park %s until it is answered", it.ID, a.Task), rulePlan)
	st := stateOf(a)
	st.Reason = fmt.Sprintf("%s; see inbox item %s", a.Reason, it.ID)
	return st
}

// ownerBody tells the owner the task is theirs and that their answer is what
// finishes it. It carries the task's whole title, because the item's own
// title may have been cut to fit.
//
// The design task (design true) gets three steps instead of one: hand the
// brief to the design tool and commit what comes back, run the reconcile, and
// only then answer. The reconcile comes before the answer because the answer
// is what lets the interface tasks start, and they should start against a
// plan already brought in line with the design. The daemon only says so; it
// never reads the design file or runs the reconcile, which is an interactive
// session the owner sees through. The file is committed because an agent
// builds in its own copy of the repository and sees only committed files.
// Every other owner task keeps the one-step body exactly.
func ownerBody(task, title, plan string, design bool) string {
	head := fmt.Sprintf("%s — %s\n\n"+
		"The plan marks this task as yours to do, not an agent's, so afk has not started anything for it. "+
		"Tasks that depend on it wait until it is done.\n\n"+
		"What to do next:\n", task, strings.TrimSpace(title))
	if design {
		return head + fmt.Sprintf(
			"1. Take the design brief (`docs/design-brief.md`) to your design tool, save what it hands back at `%s` in the project's main checkout, and commit it, "+
				"because an agent builds in its own copy of the repository and only sees committed files. The task's steps are under %s in %s.\n"+
				"2. Run `afk plan reconcile %s` in the project folder and see that session through. "+
				"It brings the plan in line with the design, and writes a follow-up prompt for anything the design must change.\n"+
				"3. Then answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
				"Answering is what lets the tasks that build the interface start, so do it after the reconcile.",
			masterplan.DesignSystemFile, task, plan, masterplan.DesignSystemFile)
	}
	return head + fmt.Sprintf(
		"1. Do the task. Its steps are under %s in %s.\n"+
			"2. When it is finished, answer this item with `afk inbox answer <id> done`, using this item's id (shown by `afk inbox`). "+
			"Answering it is what marks %s as done, so answer only when the work really is finished.",
		task, plan, task)
}

// ownerDone marks an owner task done once every item it was parked on is
// answered. The owner's answer is the whole of the evidence: the work was
// done away from this machine, and there is no session, branch or review to
// look at.
func (t *pass) ownerDone(a Action) daemonstate.State {
	if err := t.change(a.Task, ledger.Parked, func(e *ledger.Entry) {
		e.Status = ledger.Done
		e.ParkedOn = nil
		e.FinishedAt = stamp(t.now)
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	t.record("owner_done", a.Task, "",
		fmt.Sprintf("The owner answered the item for their task %s. What now?", a.Task),
		fmt.Sprintf("mark %s done; the answer is the owner saying it is finished", a.Task), rulePlan)
	return stateOf(a)
}

// readQuestions reads the plan's Open questions table through Deps.ReadPlan.
// With no ReadPlan it returns nothing and no error: a daemon that cannot read
// the plan knows of no open row.
func (t *pass) readQuestions() ([]masterplan.Question, error) {
	if t.d.ReadPlan == nil {
		return nil, nil
	}
	data, err := t.d.ReadPlan(t.planPath)
	if err != nil {
		return nil, err
	}
	return masterplan.Questions(data), nil
}

// clearQuestions answers every open question item whose row is no longer open
// in the plan, the way clearStops answers StopTitle: the owner put the answer
// in the plan by hand (or took the row out) and should not have to come back
// to the inbox to say so. It reports whether any item was answered, so the
// caller can read the inbox again before deciding.
func (t *pass) clearQuestions() bool {
	var closed []string
	for _, it := range staleQuestionItems(t.obs.Items, t.obs.Questions) {
		if _, err := t.store.Answer(it.ID, answeredInPlan, inbox.ViaCLI); err != nil {
			// Answered by the owner in the meantime is the same outcome;
			// anything else is tried again on the next tick.
			if !errors.Is(err, inbox.ErrAlreadyAnswered) {
				t.d.logf("%s: inbox item %s could not be answered: %v", t.p.Name, it.ID, err)
			}
			continue
		}
		closed = append(closed, it.ID)
	}
	if len(closed) == 0 {
		return false
	}
	t.record("question_closed", "", "",
		"An open question that was asked in the inbox is no longer open in the plan. What now?",
		fmt.Sprintf("answered inbox %s as %q; nobody needs to answer it there", strings.Join(closed, ", "), answeredInPlan), rulePlan)
	return true
}

// questionHold holds a task for the open questions it needs: one blocking
// item for each row that has none, then the ledger entry goes from pending to
// parked on the rows' items. No session is created and the entry never says
// running, exactly as for an owner's task (see ownerTask), which is the
// pattern this copies.
//
// The items are written before the ledger, because the entry needs their ids,
// and each is looked for first. If the tick dies between the two, the next
// one decides ActQuestionHold again (the task is still pending), finds the
// items by their first line and only finishes the ledger; the owner is asked
// once.
//
// The task is parked on the item of every holding row, answered ones too. An
// answered item whose row is still open is an answer that has not reached the
// plan; parked on it, the task is picked up by questionAnswered on the next
// pass, which writes it.
func (t *pass) questionHold(a Action) daemonstate.State {
	rows := holdingRows(t.obs.Questions, a.Task)
	var ids, added []string
	for _, q := range rows {
		it, have := questionItem(t.obs.Items, q)
		if !have {
			var err error
			it, err = t.add(inbox.NewItem{Task: a.Task, Kind: "question", Blocking: true,
				Title: fitTitle(QuestionTitlePrefix + q.Text), Body: questionBody(q, a.Task, t.relPlan())})
			if err != nil {
				return daemonstate.State{Kind: string(ActWait), Task: a.Task,
					Reason: fmt.Sprintf("%s, and the inbox item saying so could not be written (%v); trying again on the next pass", a.Reason, err)}
			}
			// Seen by the rows after this one: two rows with the same text
			// are one question and get one item.
			t.obs.Items = append(t.obs.Items, it)
			added = append(added, it.ID)
		}
		if !slices.Contains(ids, it.ID) {
			ids = append(ids, it.ID)
		}
	}
	if len(ids) == 0 {
		// Decide held the task for rows this function no longer finds. That
		// cannot happen within one tick; waiting is the answer that starts
		// nothing.
		return daemonstate.State{Kind: string(ActWait), Task: a.Task, Reason: a.Reason}
	}
	if err := t.change(a.Task, ledger.Pending, func(e *ledger.Entry) {
		e.Status = ledger.Parked
		e.ParkedOn = ids
		e.FinishedAt = ""
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	chosen := fmt.Sprintf("no; park %s on inbox %s until the plan has the answers", a.Task, strings.Join(ids, ", "))
	if len(added) > 0 {
		chosen = fmt.Sprintf("no; ask the owner with inbox %s and park %s on inbox %s until the plan has the answers",
			strings.Join(added, ", "), a.Task, strings.Join(ids, ", "))
	}
	t.record("question_hold", a.Task, "",
		fmt.Sprintf("The plan lists %d open question(s) that %s needs. Should it be started?", len(rows), a.Task), chosen, rulePlan)
	st := stateOf(a)
	st.Reason = fmt.Sprintf("%s; see inbox %s", a.Reason, strings.Join(ids, ", "))
	return st
}

// questionAnswered is what happens when every item a held task waited on is
// answered: the answers are written into the plan's rows through PatchPlan,
// and the task goes back to pending. It never goes to running. The launch is
// the next pass's business, and it runs the hold rule first, so a row that is
// still open (a new one, or one nobody has answered) parks the task again
// before anything is started.
//
// The plan is read three times in such a tick, and each read has its job. The
// one before Decide chose this action. The one at the top here is what makes
// the patch happen once: a daemon killed after the patch and before the
// ledger write comes back to a parked task and an answered item, and finds on
// this read that the row is not open any more, so it writes nothing and only
// finishes the ledger. The one after the patches is the only evidence taken
// that an answer is in the plan the agent will read; PatchPlan's own word is
// not, because in pull-request mode it succeeds and the main checkout is
// unchanged.
//
// When an answer is still not there, the task parks on one follow-up item
// that says what to do. Answering that item brings the task back here, which
// tries again; an owner who answers without doing anything is told again,
// which is right, as it is for a missing ledger.
func (t *pass) questionAnswered(a Action) daemonstate.State {
	wait := func(err error) daemonstate.State {
		t.d.logf("%s: the plan could not be read for the open questions of %s: %v", t.p.Name, a.Task, err)
		return daemonstate.State{Kind: string(ActWait), Task: a.Task,
			Reason: fmt.Sprintf("the plan's open questions could not be read (%v); trying again on the next pass", err)}
	}
	questions, err := t.readQuestions()
	if err != nil {
		return wait(err)
	}

	title := fitTitle(QuestionFollowUpTitlePrefix + a.Task)
	tried := map[string]followUpRow{}
	if rows := answeredRows(questions, t.obs.Items, a.Task); len(rows) > 0 {
		// A follow-up that is open and not what the task is parked on was
		// raised by a tick that died before its ledger write. Finish that
		// write; the patch it follows is not tried a second time.
		if it, ok := openTitledFor(t.obs.Items, title, a.Task); ok {
			return t.parkOnFollowUp(a, it, false)
		}
		for _, r := range rows {
			if _, done := tried[r.Q.Text]; done {
				// The same question on two rows. One patch cannot answer
				// both, and a second would only fail the same way.
				continue
			}
			tried[r.Q.Text] = t.patchAnswer(a.Task, r)
		}
		if questions, err = t.readQuestions(); err != nil {
			return wait(err)
		}
	}

	choice := afterPatch(questions, t.obs.Items, a.Task)
	if choice.Release {
		if err := t.change(a.Task, ledger.Parked, func(e *ledger.Entry) {
			e.Status = ledger.Pending
			e.ParkedOn = nil
		}); err != nil {
			return t.ledgerTrouble(a, err)
		}
		t.record("question_answered", a.Task, "",
			fmt.Sprintf("The open questions %s waited on are answered in the plan. What now?", a.Task),
			fmt.Sprintf("set %s back to pending, so it starts when its turn comes", a.Task), rulePlan)
		st := stateOf(a)
		st.Reason = fmt.Sprintf("the open questions %s waited on are answered in the plan; it starts when its turn comes", a.Task)
		return st
	}

	rows := make([]followUpRow, 0, len(choice.Left))
	for _, r := range choice.Left {
		row, ok := tried[r.Q.Text]
		if !ok {
			row = followUpRow{Text: r.Q.Text, Date: t.answerDate(r.Item), Answer: r.Item.Answer}
		}
		// The line is the row's own, as the plan reads now; rows that share
		// a question each get their place named.
		row.Line = r.Q.Line
		rows = append(rows, row)
	}
	it, err := t.add(inbox.NewItem{Task: a.Task, Kind: "external_action", Blocking: true, Title: title,
		Body: followUpBody(a.Task, t.relPlan(), t.p.Root, rows, t.d.PatchPlan != nil)})
	if err != nil {
		return daemonstate.State{Kind: string(ActWait), Task: a.Task,
			Reason: fmt.Sprintf("the answer for %s is not in the plan yet, and the inbox item saying so could not be written (%v); trying again on the next pass", a.Task, err)}
	}
	return t.parkOnFollowUp(a, it, true)
}

// answerDate is the date written beside an answer: the UTC date the owner
// answered on, or today when the item's time cannot be read.
func (t *pass) answerDate(it inbox.Item) string {
	at, ok := parseTime(it.AnsweredAt)
	if !ok {
		at = t.now
	}
	return at.UTC().Format("2006-01-02")
}

// patchAnswer asks PatchPlan to write one answer into its row, and returns
// what the follow-up item would say about the attempt if the row turns out to
// be open afterwards. ErrRowAnswered is not a failure: the row was not open
// in the text the patch was made to, which is the outcome wanted.
func (t *pass) patchAnswer(task string, r heldRow) followUpRow {
	date := t.answerDate(r.Item)
	row := followUpRow{Line: r.Q.Line, Text: r.Q.Text, Date: date, Answer: r.Item.Answer}
	if t.d.PatchPlan == nil {
		return row
	}
	question, answer := r.Q.Text, r.Item.Answer
	res, err := t.d.PatchPlan(t.p, PlanPatchRequest{
		PlanPath: t.planPath,
		Edit: func(plan []byte) ([]byte, []byte, error) {
			old, new, ok := masterplan.AnswerRow(plan, question, date, answer)
			if !ok {
				return nil, nil, ErrRowAnswered
			}
			return old, new, nil
		},
		Message: fmt.Sprintf("docs(plan): Answer an open question for %s", task),
	})
	switch {
	case errors.Is(err, ErrRowAnswered):
	case err != nil:
		t.d.logf("%s: the answer to %q could not be written into the plan for %s: %v", t.p.Name, question, task, err)
		row.Err = err
	default:
		row.PR, row.Notes = res.PR, res.Notes
	}
	return row
}

// parkOnFollowUp points a held task at its follow-up item. The entry stays
// parked; only what it waits on changes. raised says whether this tick added
// the item, which is the only case that is a new decision to log.
func (t *pass) parkOnFollowUp(a Action, it inbox.Item, raised bool) daemonstate.State {
	if err := t.change(a.Task, ledger.Parked, func(e *ledger.Entry) {
		e.ParkedOn = []string{it.ID}
	}); err != nil {
		return t.ledgerTrouble(a, err)
	}
	if raised {
		t.record("question_follow_up", a.Task, "",
			fmt.Sprintf("The owner answered the open questions %s waited on. Is the answer in the plan?", a.Task),
			fmt.Sprintf("no; tell the owner what to do with inbox %s and keep %s parked until it is answered", it.ID, a.Task), rulePlan)
	}
	return daemonstate.State{Kind: string(ActQuestionHold), Task: a.Task,
		Reason: fmt.Sprintf("waiting on the owner: the answer %s needs is not in the plan yet; see inbox item %s", a.Task, it.ID)}
}
