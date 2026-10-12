package ship

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The timed rules. They are constants, not configuration, because each is a
// guess about how long something outside our control takes, not a preference
// of the project.
const (
	// NoChecksGrace is how long an open pull request may show no check
	// before the task parks. CI that has not started within this long is
	// taken to be absent, and a pull request with no CI is never merged.
	NoChecksGrace = 5 * time.Minute
	// HandBackTimeout is how long a session has to commit a fix after a
	// hand-back before the task parks.
	HandBackTimeout = 60 * time.Minute
	// QuietHead is how old the local head commit must be before a newer
	// head is pushed. An agent fixing CI often commits more than once, and
	// pushing the first of those commits would start a CI run on half a fix.
	// It is measured from the commit's own timestamp so it survives a
	// restart.
	QuietHead = 2 * time.Minute
	// RerunSettle is how long failed checks are ignored after a re-run.
	// GitHub takes a moment to flip a re-run check back to pending; without
	// this the old failure is read again at once and spends a hand-back.
	RerunSettle = 90 * time.Second
	// ReadFailureLimit is how long reads may keep failing before the task
	// parks. Shorter failures are waited out.
	ReadFailureLimit = 30 * time.Minute
)

// Decide chooses the one step to take from the facts read this tick. It is
// pure: no I/O, no clock, no environment. Within a state the first rule that
// matches wins, and the order is part of the contract (decide_test.go has a
// row per rule).
//
// Every action rule sits behind a rule that notices the action has already
// happened, so calling Decide again after a crash between an action and its
// save chooses a save, not the action a second time.
func Decide(f Facts) Step {
	state := f.Progress.State

	// AfterMerge, DeployHealthy and Stopped read neither the remote nor GitHub
	// (the session may already be cleaned up), so a failing read cannot
	// concern them and must not park a ship that is otherwise over. For
	// AfterMerge it matters more than for the other two: a park there would
	// leave a merged task waiting on GitHub for a command that needs none.
	if state != AfterMerge && state != DeployHealthy && state != Stopped {
		if f.ReadFailingFor >= ReadFailureLimit {
			return park(f, ParkReadFailing, fmt.Sprintf(
				"GitHub could not be read for %s", roundDuration(f.ReadFailingFor)))
		}
		if f.ReadFailingFor > 0 {
			// The other facts were not read this tick; acting on them
			// would be acting on zero values.
			return wait(f, "the last read of GitHub failed; trying again")
		}
	}

	switch state {
	case NotStarted:
		return decideNotStarted(f)
	case Committed:
		return decideCommitted(f)
	case Pushed:
		return decidePushed(f)
	case PROpen:
		return decidePROpen(f)
	case HandedBack:
		return decideHandedBack(f)
	case CIGreen:
		return decideCIGreen(f)
	case Merged:
		return decideMerged(f)
	case AfterMerge:
		return decideAfterMerge(f)
	case DeployHealthy:
		return Step{Kind: StepDone, Next: state, Progress: f.Progress, Reason: "the deploy is healthy"}
	case Stopped:
		if f.StopMarker {
			return wait(f, "the line is stopped until the owner clears it")
		}
		// The executor writes the marker before it saves Stopped, so a
		// missing marker here is the owner saying the stop (a failed deploy
		// or a failed after-merge command) is dealt with.
		return Step{Kind: StepDone, Next: state, Progress: f.Progress, Reason: "the owner cleared the stopped line"}
	default:
		return park(f, ParkUnknownState, fmt.Sprintf("the saved ship state %q is not one this version knows", string(state)))
	}
}

func decideNotStarted(f Facts) Step {
	// Checked first because every later state needs GitHub, and "install or
	// log in" is the one thing here the owner can fix without the session.
	if !f.GHReady {
		reason := "the GitHub command-line tool is not ready"
		if f.GHError != "" {
			reason += ": " + f.GHError
		}
		return park(f, ParkGHNotReady, reason)
	}
	if !f.WorktreeClean {
		return park(f, ParkDirty, "the session left changes that are not committed")
	}
	if !f.AheadOfBase {
		return park(f, ParkNothingCommitted, "the session committed nothing on its branch")
	}
	return save(moved(f, Committed), "the session's work is committed")
}

func decideCommitted(f Facts) Step {
	if f.RemoteFound && f.RemoteSHA == f.LocalSHA {
		return save(moved(f, Pushed), "the branch is already on GitHub")
	}
	return act(StepPush, moved(f, Pushed), "pushing the branch")
}

func decidePushed(f Facts) Step {
	next := moved(f, PROpen)
	if f.PRFound {
		next.PR = f.PR.Number
		return save(next, fmt.Sprintf("pull request #%d already exists for the branch", f.PR.Number))
	}
	// PR stays 0 here; the executor fills it from what GitHub returns.
	return act(StepOpenPR, next, "opening a pull request")
}

func decidePROpen(f Facts) Step {
	if step, ok := prSettled(f); ok {
		return step
	}
	if !f.RemoteFound {
		return park(f, ParkBranchMissing, "the branch is no longer on GitHub")
	}
	if step, ok := newerHead(f); ok {
		return step
	}
	if f.PR.Conflicting {
		return handBack(f, true)
	}

	c := f.Checks
	if c.Passed+c.Pending+c.Failed == 0 {
		// Nothing to judge: no checks at all, or only skipped ones, which
		// can never turn green.
		if f.Now.Sub(f.Progress.At) < NoChecksGrace {
			return wait(f, "waiting for checks to start")
		}
		return park(f, ParkNoChecks, "no checks ran on the pull request within five minutes")
	}
	if c.Pending > 0 {
		return wait(f, "checks are still running")
	}
	if c.Failed > 0 {
		if f.Progress.Reruns > 0 && f.Now.Sub(f.Progress.At) < RerunSettle {
			return wait(f, "waiting for the re-run to start")
		}
		if f.Progress.Reruns < f.Config.CIReruns {
			// At moves to the time of the re-run so RerunSettle has
			// something to measure from. Reruns+1 is at most ci_reruns.
			next := f.Progress
			next.Reruns++
			next.At = f.Now
			return act(StepRerun, next, "checks failed; re-running the failed ones")
		}
		return handBack(f, false)
	}
	if c.Green() {
		return save(moved(f, CIGreen), "every check passed")
	}
	// The counts do not add up to Total: some check is in none of the
	// buckets. Not green, so never a merge; nothing to re-run either.
	return wait(f, "some checks have not reported a result yet")
}

func decideHandedBack(f Facts) Step {
	if !f.RemoteFound {
		return park(f, ParkBranchMissing, "the branch is no longer on GitHub")
	}
	if step, ok := newerHead(f); ok {
		return step
	}
	if !f.HandBackWritten {
		// The hand-back file names a commit that is no longer the remote
		// head, so the fix was pushed and the save after it was lost.
		// Without this rule that crash would sit here for an hour and park.
		next := moved(f, PROpen)
		next.Reruns = 0
		return save(next, "the fix is already on GitHub")
	}
	if f.Now.Sub(f.Progress.At) > HandBackTimeout {
		return park(f, ParkHandBackTimeout, "the session made no new commit within an hour of being handed the failure")
	}
	return wait(f, "waiting for the session to commit a fix")
}

func decideCIGreen(f Facts) Step {
	if step, ok := prSettled(f); ok {
		return step
	}
	if !f.RemoteFound {
		return park(f, ParkBranchMissing, "the branch is no longer on GitHub")
	}
	// Green was true when this state was saved, not necessarily now: a
	// commit may have been pushed since. Going back to PROpen lets its rules
	// judge the new head.
	if !f.Checks.Green() || f.PR.Conflicting {
		next := moved(f, PROpen)
		next.Reruns = 0
		return save(next, "the pull request is no longer ready to merge; checking it again")
	}
	if len(f.EnvVars) > 0 {
		step := park(f, ParkEnvVar, "the change needs "+strings.Join(f.EnvVars, ", ")+" set before it merges")
		step.EnvVars = append([]string(nil), f.EnvVars...)
		return step
	}
	// MergedSHA stays empty: the merge command prints no sha, so Merged's
	// first rule fills it from the pull request.
	step := act(StepMerge, moved(f, Merged), "every check passed; merging")
	step.HeadSHA = f.RemoteSHA
	return step
}

func decideMerged(f Facts) Step {
	if !f.PRFound {
		return park(f, ParkPRMissing, missingPR(f))
	}
	if f.Progress.MergedSHA == "" {
		if f.PR.MergeSHA == "" {
			return wait(f, "waiting for GitHub to report the merge commit")
		}
		next := f.Progress
		next.MergedSHA = f.PR.MergeSHA
		return save(next, "recording the merge commit")
	}
	// The two steps below keep Progress as it is, At included, so the
	// deploy timeout is measured from the merge.
	if !f.BaseSynced {
		return act(StepSyncBase, f.Progress, "bringing the local base branch up to date")
	}
	if f.RemoteFound {
		return act(StepDeleteRemote, f.Progress, "deleting the merged branch on GitHub")
	}
	// The command comes after the two steps above and before the deploy
	// rules, and the order is the point: it runs in the main checkout, so the
	// local base must already hold the merge, and what it does (an install, a
	// restart) is usually what makes the deploy healthy. With no command the
	// rules are the ones this state always had.
	if f.Config.AfterMerge != "" {
		return save(moved(f, AfterMerge), "the merge is finished; the after-merge command is next")
	}
	return decideDeploy(f, f.Progress.At)
}

// decideAfterMerge holds the rules for a merge whose after-merge command is
// due or done. The command is run at most once for a task and a merge commit,
// whatever the order of crashes, and that follows from one thing: the step
// that runs it is chosen only when there is no record of this ship's run at
// all. Any record, however it ended, leads somewhere else.
func decideAfterMerge(f Facts) Step {
	run := f.AfterMerge
	// Merged never moves here without the merge commit, so this is a ledger
	// edited by hand or written by a version that worked differently. The
	// record is matched on the merge commit; running the command against an
	// empty one would match any other record with an empty one.
	if f.Progress.MergedSHA == "" {
		return park(f, ParkUnknownState, "the saved ship state \"after_merge\" has no merge commit with it")
	}
	if !run.Found {
		if f.Config.AfterMerge == "" {
			// The owner took the command out of afk.toml after this state
			// was saved and before the command ran. There is nothing to run,
			// and the ship must not sit here for ever.
			return decideDeploy(f, f.Progress.At)
		}
		// Progress is unchanged, At included: nothing is saved by this step.
		// The run record is its only trace.
		return act(StepAfterMerge, f.Progress, "running the after-merge command")
	}
	if !run.Finished {
		// Running it again is the worse mistake: a second install on top of
		// one that may be half done. Carrying on as if it had worked is the
		// other mistake. So the owner is asked.
		step := act(StepStop, moved(f, Stopped),
			"afk was stopped while the after-merge command ran, so it is not known whether the command finished")
		step.Stop = StopAfterMergeInterrupted
		return step
	}
	if run.TimedOut || run.ExitCode != 0 {
		step := act(StepStop, moved(f, Stopped), afterMergeFailure(f))
		step.Stop = StopAfterMergeFailed
		return step
	}
	// A slow command must not use up the deploy's time, and the deploy the
	// command started cannot be healthy before the command ended.
	return decideDeploy(f, run.FinishedAt)
}

// afterMergeFailure says how a finished after-merge command went wrong. It is
// the reason saved in the stop marker and shown to the owner, so it never
// holds the command's text.
func afterMergeFailure(f Facts) string {
	switch run := f.AfterMerge; {
	case run.TimedOut:
		return fmt.Sprintf("the after-merge command did not finish within %s", roundDuration(f.Config.AfterMergeTimeout))
	case run.ExitCode < 0:
		// The runner keeps -1 for a command that never ran (aftermerge.go);
		// "exit code -1" would tell the owner nothing.
		return "the after-merge command could not be started"
	default:
		return fmt.Sprintf("the after-merge command ended with exit code %d", run.ExitCode)
	}
}

// decideDeploy holds the deploy rules, shared by Merged (a project with no
// after-merge command) and AfterMerge. since is the moment the deploy timeout
// is measured from: the merge when no command ran, the end of the command
// when one did.
func decideDeploy(f Facts, since time.Time) Step {
	if !f.Config.Deploys || f.Config.HealthURL == "" {
		return save(moved(f, DeployHealthy), "there is no deploy to check")
	}
	if f.HealthChecked && f.Healthy {
		return save(moved(f, DeployHealthy), "the deploy is healthy")
	}
	if f.Now.Sub(since) >= f.Config.DeployTimeout {
		step := act(StepStop, moved(f, Stopped), fmt.Sprintf(
			"the deploy did not become healthy within %s", roundDuration(f.Config.DeployTimeout)))
		step.Stop = StopDeploy
		return step
	}
	return wait(f, "waiting for the deploy to become healthy")
}

// prSettled holds the rules PROpen and CIGreen share about the pull request
// itself. They come before every other rule in both states: a pull request
// that is already merged must be recorded and never merged again (this is the
// read that makes a crash right after the merge safe), and one that is closed
// or gone must never be replaced by a second one.
func prSettled(f Facts) (Step, bool) {
	if !f.PRFound {
		return park(f, ParkPRMissing, missingPR(f)), true
	}
	switch f.PR.State {
	case PRStateMerged:
		next := moved(f, Merged)
		next.MergedSHA = f.PR.MergeSHA
		return save(next, "the pull request is already merged"), true
	case PRStateClosed:
		return park(f, ParkPRClosed, fmt.Sprintf("pull request #%d was closed without being merged", f.Progress.PR)), true
	}
	return Step{}, false
}

// newerHead holds the rules PROpen and HandedBack share about a local head
// that differs from the remote one. ok is false when the heads are equal, or
// when the worktree is dirty (the agent is mid-edit; the state's later rules
// still apply).
//
// A head that is ahead and clean but younger than QuietHead waits instead of
// falling through. The push is at most two minutes away, and falling through
// would judge the old head's checks: in PROpen that can spend a re-run or a
// hand-back on a failure the agent is already fixing, and in HandedBack it
// can hit the timeout seconds before the fix goes out.
func newerHead(f Facts) (Step, bool) {
	if f.LocalSHA == f.RemoteSHA {
		return Step{}, false
	}
	if !f.LocalAhead {
		return park(f, ParkDiverged, "the branch on GitHub has commits the session does not have"), true
	}
	if !f.WorktreeClean {
		return Step{}, false
	}
	if f.HeadAge < QuietHead {
		return wait(f, "the session committed a moment ago; waiting to see if it commits again"), true
	}
	// A new head gets a fresh re-run allowance and a fresh clock: GitHub
	// needs a moment to report checks for it, and the no-checks rule would
	// otherwise measure from the first push and park at once.
	next := f.Progress
	next.State = PROpen
	next.At = f.Now
	next.Reruns = 0
	return act(StepPush, next, "pushing the session's new commit"), true
}

// handBack is the rule for a failure that re-running cannot fix: give it to
// the session, unless the cap is reached or the session was already told.
func handBack(f Facts, conflict bool) Step {
	what := "checks are still failing"
	if conflict {
		what = "the branch conflicts with " + baseName(f)
	}
	if f.Progress.FixCycles >= f.Config.CIFixCycles {
		return park(f, ParkHandBackCap, fmt.Sprintf("%s after %s to the session",
			what, plural(f.Progress.FixCycles, "hand-back", "hand-backs")))
	}
	next := moved(f, HandedBack)
	next.FixCycles++
	next.Reruns = 0
	if f.HandBackWritten {
		// The file already names this head, so the prompt went out and the
		// save after it was lost. Saving without prompting is what stops a
		// second prompt after a crash.
		return save(next, "the session was already handed this failure")
	}
	step := act(StepHandBack, next, what+"; handing the failure to the session")
	step.Conflict = conflict
	return step
}

// moved returns the progress with a new state entered now.
func moved(f Facts, to State) Progress {
	next := f.Progress
	next.State = to
	next.At = f.Now
	return next
}

func wait(f Facts, reason string) Step {
	return Step{Kind: StepWait, Next: f.Progress.State, Progress: f.Progress, Reason: reason}
}

func park(f Facts, cause ParkCause, reason string) Step {
	return Step{Kind: StepPark, Next: f.Progress.State, Progress: f.Progress, Reason: reason, Cause: cause}
}

func save(next Progress, reason string) Step {
	return Step{Kind: StepSave, Next: next.State, Progress: next, Reason: reason}
}

func act(kind StepKind, next Progress, reason string) Step {
	return Step{Kind: kind, Next: next.State, Progress: next, Reason: reason}
}

func missingPR(f Facts) string {
	if f.Progress.PR > 0 {
		return fmt.Sprintf("pull request #%d can no longer be found", f.Progress.PR)
	}
	return "the pull request can no longer be found"
}

func baseName(f Facts) string {
	if f.Config.Base == "" {
		return "its base branch"
	}
	return f.Config.Base
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// roundDuration drops the fraction of a second so a reason reads "31m0s",
// not "31m0.004s".
func roundDuration(d time.Duration) string {
	return d.Round(time.Second).String()
}

// EnvVarsAdded returns the environment variables the added lines name,
// according to the project's required_env_pattern: the first capture group of
// each match when the pattern has one, otherwise the whole match. Each name
// appears once, in the order first seen. A nil pattern means the project has
// no such check.
func EnvVarsAdded(pattern *regexp.Regexp, addedLines []string) []string {
	if pattern == nil {
		return nil
	}
	var vars []string
	seen := map[string]bool{}
	for _, line := range addedLines {
		for _, m := range pattern.FindAllStringSubmatch(line, -1) {
			name := m[0]
			if len(m) > 1 {
				name = m[1]
			}
			// An empty name (an optional group that did not take part, or a
			// pattern that matches nothing) names no variable to set.
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			vars = append(vars, name)
		}
	}
	return vars
}
