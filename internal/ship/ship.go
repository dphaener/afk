// Package ship takes a finished session from "committed" to "deployed": push
// the branch, open a pull request, wait for CI, merge on green, bring the
// local base up to date and check the deploy (docs/PLAN.md §5.4).
//
// It is a state machine whose state is saved outside this package (the ledger,
// through a callback) before every action that leaves it. The daemon keeps
// nothing in memory between ticks and can be killed at any moment, so every
// rule here is written as "read what is true now, then take at most one step":
// an action is always preceded by a read that asks whether it already
// happened. That is what stops a restart from pushing, opening a pull request
// or merging a second time.
//
// This file holds the shared types and the two interfaces every network or
// disk call sits behind. decide.go holds the pure decision. Neither reads a
// clock, a file or the environment.
//
// git.go, gh.go and health.go are the real side of those interfaces: git and
// gh run as plain commands (an argv, never a shell), and one HTTP GET for the
// deploy check. The gh commands were written against the help text of gh
// version 2.102.0 (2026-09-30) and use no flag or JSON field that help does
// not list; gh.go says which pages. The git commands were checked against git
// 2.53.0.
package ship

import (
	"time"

	"github.com/dphaener/afk/internal/projectconfig"
)

// State is where one task's ship has got to. The words are the ones the
// ledger stores in its "ship" field. They are repeated here, not imported,
// so this package does not depend on the ledger: the daemon is the ledger's
// only writer and hands progress in and out as plain values.
type State string

const (
	// NotStarted is a session that has finished but has not been looked at yet.
	NotStarted State = ""
	// Committed means the session's work is committed and nothing is uncommitted.
	Committed State = "committed"
	// Pushed means the remote branch holds the local head.
	Pushed State = "pushed"
	// PROpen means a pull request exists and its checks are being watched.
	PROpen State = "pr_open"
	// HandedBack means a red CI or a conflict was given back to the session
	// and a new commit is awaited.
	HandedBack State = "handed_back"
	// CIGreen means every check passed; the merge is next.
	CIGreen State = "ci_green"
	// Merged means the pull request is merged; the base update, the remote
	// branch delete and the deploy check follow.
	Merged State = "merged"
	// AfterMerge means the merge is finished (the merge commit is recorded,
	// the local base holds it and the remote branch is gone) and the project's
	// after-merge command is due or done. Which of the two is not saved here:
	// it is in the run record the daemon keeps (Deps.AfterMergeRecord), because
	// "started" has to reach the disk before the command does and the ledger
	// is saved only after a step. A project with no after-merge command never
	// enters this state.
	AfterMerge State = "after_merge"
	// DeployHealthy means the deploy answered healthy, or there is no deploy
	// to check.
	DeployHealthy State = "deploy_healthy"
	// Stopped means the line is stopped until the owner clears it: the deploy
	// did not turn healthy in time, or the after-merge command failed, ran out
	// of time or was interrupted.
	Stopped State = "stopped"
)

// States lists every saved state in ship order, without NotStarted (which is
// the absence of a saved state). It exists so a test elsewhere can check that
// these words and the ledger's stay the same list.
func States() []State {
	return []State{Committed, Pushed, PROpen, HandedBack, CIGreen, Merged, AfterMerge, DeployHealthy, Stopped}
}

// Progress is everything about one ship that is saved between ticks.
type Progress struct {
	State State
	// At is when State was entered. Two rules move it without a change of
	// state: a re-run of failed checks, and a push of a newer head while the
	// pull request is open. Both start a fresh wait for GitHub to report, and
	// the timed rules (no checks, the re-run settle time) measure from it.
	// The base update and the remote branch delete inside Merged never move
	// it, so the deploy timeout is measured from the merge. Running the
	// after-merge command never moves it either; once a command has run, the
	// deploy timeout is measured from the end of the command instead
	// (AfterMergeRun.FinishedAt).
	At time.Time
	// PR is the pull request number, 0 until one is found or opened.
	PR int
	// Reruns counts re-runs of failed checks for the current head. It goes
	// back to 0 whenever a new head is pushed or the work is handed back.
	Reruns int
	// FixCycles counts hand-backs to the session for this ship.
	FixCycles int
	// MergedSHA is the commit the merge made on the base.
	MergedSHA string
}

// Pull request states as GitHub names them.
const (
	PRStateOpen   = "OPEN"
	PRStateMerged = "MERGED"
	PRStateClosed = "CLOSED"
)

// PR is what is known about the branch's pull request.
type PR struct {
	Number int
	// State is PRStateOpen, PRStateMerged or PRStateClosed.
	State string
	// Conflicting is true only when GitHub says the branch conflicts with
	// its base. While GitHub is still working mergeability out it must be
	// false: "unknown" is not a conflict, and treating it as one would hand
	// work back for a conflict that does not exist.
	Conflicting bool
	// MergeSHA is the merge commit, set once State is PRStateMerged and
	// GitHub has reported it.
	MergeSHA string
}

// Checks counts the pull request's checks for its current head. Every check
// falls in exactly one bucket, so the four counts add up to Total. A
// cancelled, timed-out or errored check is Failed; a skipped or neutral one
// is Skipped and counts neither for nor against.
type Checks struct {
	Total, Passed, Pending, Failed, Skipped int
	// FailedNames are the names of the Failed checks, for the hand-back file
	// and the park item.
	FailedNames []string
}

// Green reports whether the checks allow a merge: at least one passed and
// everything else was skipped. Zero checks, only skipped checks, anything
// pending and anything failed or cancelled are all not green; a merge with
// no CI behind it is the exposure the plan forbids.
func (c Checks) Green() bool {
	return c.Passed > 0 && c.Passed+c.Skipped == c.Total
}

// Git is every git call a ship makes. The method set is fixed here so the
// executor, its fakes and the real adapter are written against one shape.
// Every method is a single bounded command; none waits or retries.
type Git interface {
	// LocalSHA returns the commit the local branch points at. It must not
	// fetch.
	LocalSHA(branch string) (string, error)
	// RemoteSHA asks the remote for the commit its branch points at. It must
	// ask the remote itself, not a remote-tracking ref, which can be stale
	// after a restart. found is false, with a nil error, when the remote has
	// no such branch.
	RemoteSHA(branch string) (sha string, found bool, err error)
	// Clean reports whether the session worktree has no uncommitted change
	// to a tracked file. Untracked files must not count: every session
	// worktree holds an untracked .afk/ directory, so counting them would
	// make every worktree dirty.
	Clean() (bool, error)
	// AheadOfBase reports whether the branch has at least one commit the
	// base does not. It must not fetch.
	AheadOfBase(base, branch string) (bool, error)
	// Ahead reports whether remoteSHA is the local branch's head or one of
	// its ancestors, which is what makes a push a plain fast-forward. A sha
	// the local repository does not have (someone pushed to the branch on
	// GitHub) is false with a nil error, not an error.
	Ahead(branch, remoteSHA string) (bool, error)
	// HeadTime returns the committer time of the branch's head commit. It is
	// read from the commit so the quiet period before a push survives a
	// restart.
	HeadTime(branch string) (time.Time, error)
	// Title returns the subject and the body of the branch's head commit,
	// which become the pull request's title and body.
	Title(branch string) (subject, body string, err error)
	// AddedLines returns the text of every line the branch adds relative to
	// its merge base with base, without the leading "+".
	AddedLines(base, branch string) ([]string, error)
	// Push pushes the local branch to the remote. It must never force: a
	// rejected push means the remote has commits the session does not, and
	// that is for the owner to see.
	Push(branch string) error
	// DeleteRemote deletes the branch on the remote. A branch that is
	// already gone is not an error.
	DeleteRemote(branch string) error
	// SyncBase fetches the base and fast-forwards the local base branch to
	// it. It must never merge or reset: a base that cannot fast-forward is
	// an error for the owner.
	SyncBase(base string) error
	// BaseSynced reports whether the local base branch contains sha. It must
	// not fetch.
	BaseSynced(base, sha string) (bool, error)
}

// GH is every GitHub call a ship makes. The method set is fixed here for the
// same reason as Git's.
type GH interface {
	// Ready returns nil when the GitHub tool is installed and logged in, and
	// otherwise an error whose text can be shown to the owner.
	Ready() error
	// FindPR looks for the branch's pull request in any state, preferring an
	// open one. found is false, with a nil error, when there is none. It
	// must not create one.
	FindPR(branch string) (pr PR, found bool, err error)
	// OpenPR opens a pull request that is ready for review, not a draft, and
	// returns it. The caller has already checked with FindPR that none
	// exists.
	OpenPR(branch, base, title, body string) (PR, error)
	// Checks returns the checks of the pull request's current head.
	Checks(pr int) (Checks, error)
	// RerunFailed re-runs the failed jobs of the pull request's current
	// head. It must not re-run jobs that passed.
	RerunFailed(pr int) error
	// FailedLog returns the end of the failed jobs' logs, capped to a size
	// that fits in a file an agent will read.
	FailedLog(pr int) (string, error)
	// Merge merges the pull request with the given method (merge, squash or
	// rebase) only if its head is still headSHA, and must fail otherwise. It
	// must not delete the branch; that is a separate, later step.
	Merge(pr int, method, headSHA string) error
}

// AfterMergeRun is what the run record says about the after-merge command for
// one ship. The zero value means "no record": the command has not been started
// for this task and this merge commit.
type AfterMergeRun struct {
	// Found is true when a record exists for this task and this merge commit.
	// A record for another task or another merge is not this ship's and reads
	// as not found; the executor makes that comparison, not whoever keeps the
	// record.
	Found bool
	// Finished is false for a run that was started and whose end was never
	// recorded: the daemon died, or was stopped, while the command ran.
	Finished bool
	// TimedOut is true when the command was stopped for running too long.
	TimedOut bool
	// ExitCode is the command's exit code; -1 when it could not be started.
	// It means nothing while Finished is false.
	ExitCode int
	// FinishedAt is when the command ended.
	FinishedAt time.Time
}

// Facts is everything Decide looks at, as plain values. The executor reads
// only the facts the current state's rules use; the rest stay zero.
//
// Two orderings matter to whoever fills this in. RemoteSHA must be read
// before Checks, so a commit pushed between the two reads shows up as checks
// that are no longer green rather than as a newer sha with an older head's
// green checks. And when a read fails, Decide must be called either not at
// all or with ReadFailingFor set, never with the half-read facts alone.
type Facts struct {
	// Now is the caller's clock reading for this tick.
	Now time.Time
	// Progress is the saved progress.
	Progress Progress
	// Config is the project's [ship] table with Base filled in.
	Config projectconfig.Ship

	// GHReady is whether GH.Ready returned nil; GHError is its text when not.
	GHReady bool
	GHError string

	// LocalSHA and RemoteSHA are the branch's head here and on the remote.
	LocalSHA    string
	RemoteSHA   string
	RemoteFound bool
	// LocalAhead is Git.Ahead: the remote head is the local head or an
	// ancestor of it.
	LocalAhead bool
	// AheadOfBase is whether the branch has any commit the base does not.
	AheadOfBase bool
	// WorktreeClean is Git.Clean (tracked files only).
	WorktreeClean bool
	// HeadAge is how long ago the local head commit was made.
	HeadAge time.Duration

	// PRFound and PR are the result of GH.FindPR.
	PRFound bool
	PR      PR
	// Checks are the pull request's checks.
	Checks Checks
	// HandBackWritten is true when the hand-back file in the session's
	// directory names the current remote head: the session has already been
	// told about this head's failure.
	HandBackWritten bool
	// EnvVars are the variables the branch's diff adds a requirement for and
	// that the owner has not yet answered an item about.
	EnvVars []string

	// BaseSynced is whether the local base holds the merge commit.
	BaseSynced bool
	// HealthChecked is whether the health URL was asked this tick, and
	// Healthy whether it answered healthy.
	HealthChecked bool
	Healthy       bool
	// StopMarker is whether the project's stopped-line file exists.
	StopMarker bool
	// AfterMerge is the run record for this ship's after-merge command.
	AfterMerge AfterMergeRun

	// ReadFailingFor is how long reads of the remote or of GitHub have been
	// failing without a good one in between. Zero means the last read worked.
	ReadFailingFor time.Duration
}

// StepKind is one thing the executor can do in a tick.
type StepKind string

const (
	// StepWait does nothing this tick.
	StepWait StepKind = "wait"
	// StepSave records Step.Progress with no outside action.
	StepSave StepKind = "save"
	// StepPush pushes the branch.
	StepPush StepKind = "push"
	// StepOpenPR opens the pull request. The executor fills Progress.PR from
	// the number GitHub returns.
	StepOpenPR StepKind = "open_pr"
	// StepRerun re-runs the failed checks.
	StepRerun StepKind = "rerun"
	// StepHandBack writes the failure file and prompts the session.
	StepHandBack StepKind = "hand_back"
	// StepPark stops work on this task until the owner answers an item.
	StepPark StepKind = "park"
	// StepMerge merges the pull request at Step.HeadSHA.
	StepMerge StepKind = "merge"
	// StepSyncBase brings the local base branch up to date.
	StepSyncBase StepKind = "sync_base"
	// StepDeleteRemote deletes the merged branch on the remote.
	StepDeleteRemote StepKind = "delete_remote"
	// StepAfterMerge runs the project's after-merge command. It is the one
	// step with no "did it already happen" read on the outside world: a
	// command line has no trace to look for. The executor therefore records
	// the start before the command and the end after it, and Decide never
	// chooses this step when any record of this ship's run exists.
	StepAfterMerge StepKind = "after_merge"
	// StepStop stops the line. It is one kind on purpose: the executor
	// writes the stop marker and then saves Stopped, in that order, so a
	// saved Stopped with no marker can only mean the owner cleared it.
	StepStop StepKind = "stop"
	// StepDone ends the ship.
	StepDone StepKind = "done"
)

// ParkCause says why a task was parked, so the executor can write an item
// that tells the owner what to do about it.
type ParkCause string

const (
	// ParkGHNotReady: the GitHub tool is missing or not logged in.
	ParkGHNotReady ParkCause = "gh_not_ready"
	// ParkDirty: the session left uncommitted changes to tracked files.
	ParkDirty ParkCause = "dirty_worktree"
	// ParkNothingCommitted: the branch has no commit the base lacks.
	ParkNothingCommitted ParkCause = "nothing_committed"
	// ParkPRClosed: the pull request was closed without being merged.
	ParkPRClosed ParkCause = "pr_closed"
	// ParkPRMissing: a pull request was saved but can no longer be found.
	ParkPRMissing ParkCause = "pr_missing"
	// ParkBranchMissing: the branch is gone from the remote while its pull
	// request is still open.
	ParkBranchMissing ParkCause = "branch_missing"
	// ParkDiverged: the remote branch has commits the session does not.
	ParkDiverged ParkCause = "diverged"
	// ParkHandBackCap: CI or a conflict is still failing after the allowed
	// number of hand-backs.
	ParkHandBackCap ParkCause = "hand_back_cap"
	// ParkNoChecks: no check reported on the pull request in time.
	ParkNoChecks ParkCause = "no_checks"
	// ParkHandBackTimeout: the session made no new commit after a hand-back.
	ParkHandBackTimeout ParkCause = "hand_back_timeout"
	// ParkEnvVar: the diff needs environment variables set before the merge.
	ParkEnvVar ParkCause = "env_var"
	// ParkReadFailing: the remote or GitHub could not be read for too long.
	ParkReadFailing ParkCause = "read_failing"
	// ParkUnknownState: the saved state is not one this version knows.
	ParkUnknownState ParkCause = "unknown_state"
	// ParkNoBase: neither the project's [ship] table nor the session names a
	// base branch. Decide never returns it; Advance raises it before any
	// read, because every git and GitHub call that takes a base would
	// otherwise have to guess one.
	ParkNoBase ParkCause = "no_base"
)

// StopCause says why the line is being stopped, so the executor can write an
// item that tells the owner what happened and what to do about it.
type StopCause string

const (
	// StopDeploy: the deploy did not answer healthy in time.
	StopDeploy StopCause = "deploy"
	// StopAfterMergeFailed: the after-merge command ended with a non-zero
	// exit code, could not be started, or ran out of time.
	StopAfterMergeFailed StopCause = "after_merge_failed"
	// StopAfterMergeInterrupted: the command was started and its end was
	// never recorded, so nobody knows whether it finished.
	StopAfterMergeInterrupted StopCause = "after_merge_interrupted"
)

// Step is Decide's answer: one thing to do, and what to save afterwards.
type Step struct {
	Kind StepKind
	// Next is the state the ship is in once the step is carried out. It
	// always equals Progress.State.
	Next State
	// Progress is what to save after the step's action has succeeded. For a
	// wait, a park and a done it is the progress that came in, unchanged.
	// The counters and At are worked out here, not in the executor, so the
	// rules about them (Reruns never above ci_reruns, At moving only when
	// the rules say) are rows in the decision table.
	Progress Progress
	// Reason is one plain sentence saying why, for the log and for items.
	Reason string
	// Cause is set on a park.
	Cause ParkCause
	// Stop is set on a StepStop.
	Stop StopCause
	// EnvVars are the variables to raise items for, on a ParkEnvVar.
	EnvVars []string
	// HeadSHA is the commit a StepMerge must find at the pull request's
	// head, so a commit pushed after the checks were read is never merged
	// unchecked.
	HeadSHA string
	// Conflict is true on a StepHandBack that is for a conflict with the
	// base rather than for failed checks, so the hand-back file says which.
	Conflict bool
}
