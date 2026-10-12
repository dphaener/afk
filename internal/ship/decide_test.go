package ship

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/projectconfig"
)

// now is the tick every row is decided at.
var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type mut func(*Facts)

// in returns the facts for a ship that entered state `ago` before now, with
// every read saying "nothing is wrong": gh ready, a clean worktree with
// commits, local and remote heads equal, pull request #7 open with two passed
// checks, the base up to date. Each row then changes only what its rule reads.
func in(state State, ago time.Duration, muts ...mut) Facts {
	f := Facts{
		Now:      now,
		Progress: Progress{State: state, At: now.Add(-ago)},
		Config: projectconfig.Ship{
			Base:          "main",
			MergeMethod:   "merge",
			CIReruns:      1,
			CIFixCycles:   2,
			Deploys:       true,
			HealthURL:     "https://example.test/health",
			DeployTimeout: 15 * time.Minute,
		},
		GHReady:       true,
		LocalSHA:      "aaa",
		RemoteSHA:     "aaa",
		RemoteFound:   true,
		LocalAhead:    true,
		AheadOfBase:   true,
		WorktreeClean: true,
		HeadAge:       10 * time.Minute,
		PRFound:       true,
		PR:            PR{Number: 7, State: PRStateOpen},
		Checks:        Checks{Total: 2, Passed: 2},
		BaseSynced:    true,
	}
	switch state {
	case PROpen, HandedBack, CIGreen, Merged, AfterMerge, DeployHealthy, Stopped:
		f.Progress.PR = 7
	}
	for _, m := range muts {
		m(&f)
	}
	return f
}

func prog(change func(*Progress)) mut { return func(f *Facts) { change(&f.Progress) } }
func checks(c Checks) mut             { return func(f *Facts) { f.Checks = c } }
func pr(state string) mut             { return func(f *Facts) { f.PR.State = state } }
func noPR(f *Facts)                   { f.PRFound = false; f.PR = PR{} }
func noRemote(f *Facts)               { f.RemoteFound = false; f.RemoteSHA = "" }
func dirty(f *Facts)                  { f.WorktreeClean = false }
func conflicting(f *Facts)            { f.PR.Conflicting = true }
func handBackWritten(f *Facts)        { f.HandBackWritten = true }
func diverged(f *Facts)               { f.LocalSHA = "bbb"; f.LocalAhead = false }

// newHead is a local commit the remote does not have yet, made `age` ago.
func newHead(age time.Duration) mut {
	return func(f *Facts) { f.LocalSHA = "bbb"; f.LocalAhead = true; f.HeadAge = age }
}

// command is a project with an after-merge command.
func command(f *Facts) {
	f.Config.AfterMerge = "make install"
	f.Config.AfterMergeTimeout = 10 * time.Minute
}

// afterMerge is a ship in after_merge as the executor reads it: the merge
// commit saved, the branch gone, a command set, and nothing asked of the
// health URL yet. It is the starting point of every after_merge row.
func afterMerge(f *Facts) {
	mergedAt("m1")(f)
	noRemote(f)
	command(f)
}

// started is a run record with a start and no end.
func started(f *Facts) { f.AfterMerge = AfterMergeRun{Found: true} }

// ended is a run record for a command that ended `ago` before now.
func ended(exitCode int, timedOut bool, ago time.Duration) mut {
	return func(f *Facts) {
		f.AfterMerge = AfterMergeRun{Found: true, Finished: true, ExitCode: exitCode, TimedOut: timedOut, FinishedAt: now.Add(-ago)}
	}
}

func unhealthy(f *Facts) { f.HealthChecked = true; f.Healthy = false }
func healthy(f *Facts)   { f.HealthChecked = true; f.Healthy = true }

func reruns(n int) mut    { return prog(func(p *Progress) { p.Reruns = n }) }
func fixCycles(n int) mut { return prog(func(p *Progress) { p.FixCycles = n }) }

var (
	failed    = checks(Checks{Total: 2, Passed: 1, Failed: 1, FailedNames: []string{"test"}})
	pending   = checks(Checks{Total: 2, Passed: 1, Pending: 1})
	noChecks  = checks(Checks{})
	cancelled = checks(Checks{Total: 2, Passed: 1, Failed: 1, FailedNames: []string{"build"}})
	skipped   = checks(Checks{Total: 2, Skipped: 2})
)

// to builds the expected progress for a step that enters a new state now.
func to(state State, more ...func(*Progress)) func(*Progress) {
	return func(p *Progress) {
		p.State = state
		p.At = now
		for _, m := range more {
			m(p)
		}
	}
}

// TestDecide is the whole ship as a table, one row per rule line of the task
// contract. Each row starts from in(), the "nothing is wrong" facts for a
// state, so a row's mutators are exactly what its rule reads.
//
// Load-bearing rows, and why:
//
//   - "no checks ..." and every other row that is not green: the loop at the
//     bottom fails any row that merges without green checks, and it runs over
//     every row, so a new rule cannot merge unchecked without failing here.
//     "one cancelled check: no merge" and "only skipped checks: not green"
//     are the two bucket mistakes that would merge a cancelled or empty CI.
//   - The cap rows ("failed at the hand-back cap", "conflict at the hand-back
//     cap", "re-runs used up: hand back"): without them a red CI re-runs or
//     prompts the session for ever. "re-run" also pins Reruns at ci_reruns,
//     never above.
//   - "ci_green, pull request already MERGED" is the crash-after-merge row:
//     the merge happened and its save was lost. It must save, never merge
//     again. The same row exists for pr_open.
//   - "stopped, marker present" and "stopped, marker gone": the first keeps a
//     failed deploy from being marked done; the second is how afk resume
//     finishes the task.
//   - The "already" rows ("already on GitHub", "pull request already exists",
//     "already handed this failure", "fix already pushed"): each is the read
//     that stops an action being repeated after a crash.
//   - The merged rows that sync the base and delete the remote branch assert
//     At is unchanged, so the deploy timeout is measured from the merge.
//   - "merged, command set but base not synced yet" and "... remote branch
//     still there": the after-merge command runs in the main checkout, so it
//     must never be reached before the base holds the merge. Move the
//     after-merge rule up in decideMerged and these two go red.
//   - "after_merge, started and never finished: stop the line" is the crash
//     during the command. Without it the row runs the command a second time
//     (or, with the rule after it, carries on as if it had worked).
//   - "after_merge, no record: run the command" asserts the progress is
//     unchanged, and every other after_merge row with a record asserts the
//     step is not after_merge: any record at all means never run it again.
//   - "after_merge, a slow command does not use up the deploy's time": the
//     deploy timeout is measured from the command's end, not from when the
//     state was entered.
//   - "after_merge, reads failing: still runs the command": the state reads
//     neither git nor GitHub, so an outage must not park a merged task.
func TestDecide(t *testing.T) {
	type want struct {
		kind     StepKind
		cause    ParkCause
		change   func(*Progress) // nil: progress is returned unchanged
		headSHA  string
		envVars  []string
		conflict bool
		// stop is the cause a stop must carry; empty means the deploy, which
		// was the only cause before the after-merge command existed.
		stop StopCause
		// reason, when set, must be part of the step's reason.
		reason string
	}
	tests := []struct {
		name  string
		facts Facts
		want  want
	}{
		// --- not started ---
		{"not started, clean with commits: save committed",
			in(NotStarted, 0),
			want{kind: StepSave, change: to(Committed)}},
		{"not started, dirty worktree: park",
			in(NotStarted, 0, dirty),
			want{kind: StepPark, cause: ParkDirty}},
		{"not started, gh not ready: park",
			in(NotStarted, 0, func(f *Facts) { f.GHReady = false; f.GHError = "gh: not logged in" }),
			want{kind: StepPark, cause: ParkGHNotReady}},
		{"not started, nothing committed: park",
			in(NotStarted, 0, func(f *Facts) { f.AheadOfBase = false }),
			want{kind: StepPark, cause: ParkNothingCommitted}},

		// --- committed ---
		{"committed, already on GitHub: save pushed",
			in(Committed, time.Minute),
			want{kind: StepSave, change: to(Pushed)}},
		{"committed, no remote branch: push",
			in(Committed, time.Minute, noRemote),
			want{kind: StepPush, change: to(Pushed)}},
		{"committed, remote behind local: push",
			in(Committed, time.Minute, func(f *Facts) { f.RemoteSHA = "old" }),
			want{kind: StepPush, change: to(Pushed)}},

		// --- pushed ---
		{"pushed, pull request already exists: save pr_open with its number",
			in(Pushed, time.Minute),
			want{kind: StepSave, change: to(PROpen, func(p *Progress) { p.PR = 7 })}},
		{"pushed, no pull request: open one",
			in(Pushed, time.Minute, noPR),
			want{kind: StepOpenPR, change: to(PROpen)}},

		// --- pr_open: the pull request itself ---
		{"pr_open, pull request can no longer be found: park, never open another",
			in(PROpen, time.Minute, noPR),
			want{kind: StepPark, cause: ParkPRMissing}},
		{"pr_open, pull request already MERGED: save merged",
			in(PROpen, time.Minute, pr(PRStateMerged), func(f *Facts) { f.PR.MergeSHA = "m1" }),
			want{kind: StepSave, change: to(Merged, func(p *Progress) { p.MergedSHA = "m1" })}},
		{"pr_open, PR closed: park",
			in(PROpen, time.Minute, pr(PRStateClosed)),
			want{kind: StepPark, cause: ParkPRClosed}},
		{"pr_open, branch gone from GitHub: park",
			in(PROpen, time.Minute, noRemote),
			want{kind: StepPark, cause: ParkBranchMissing}},

		// --- pr_open: a newer local head ---
		{"pr_open, newer clean quiet head: push, fresh clock and re-runs",
			in(PROpen, 10*time.Minute, newHead(2*time.Minute), reruns(1), failed),
			want{kind: StepPush, change: to(PROpen, func(p *Progress) { p.Reruns = 0 })}},
		{"pr_open, newer head under two minutes old: wait",
			in(PROpen, 10*time.Minute, newHead(119*time.Second), failed),
			want{kind: StepWait}},
		{"pr_open, newer head but dirty worktree: the check rules still apply",
			in(PROpen, 10*time.Minute, newHead(10*time.Minute), dirty, pending),
			want{kind: StepWait}},
		{"pr_open, local behind or diverged: park",
			in(PROpen, 10*time.Minute, diverged),
			want{kind: StepPark, cause: ParkDiverged}},

		// --- pr_open: conflict ---
		{"pr_open, conflicting: hand back",
			in(PROpen, time.Minute, conflicting),
			want{kind: StepHandBack, conflict: true,
				change: to(HandedBack, func(p *Progress) { p.FixCycles = 1 })}},
		{"pr_open, conflict at the hand-back cap: park",
			in(PROpen, time.Minute, conflicting, fixCycles(2)),
			want{kind: StepPark, cause: ParkHandBackCap}},
		{"pr_open, mergeability unknown is not a conflict: green goes on",
			in(PROpen, time.Minute, func(f *Facts) { f.PR.Conflicting = false }),
			want{kind: StepSave, change: to(CIGreen)}},

		// --- pr_open: checks ---
		{"pr_open, no checks yet: wait",
			in(PROpen, 4*time.Minute+59*time.Second, noChecks),
			want{kind: StepWait}},
		{"pr_open, no checks after five minutes: park, no merge",
			in(PROpen, 5*time.Minute, noChecks),
			want{kind: StepPark, cause: ParkNoChecks}},
		{"pr_open, only skipped checks: not green",
			in(PROpen, time.Minute, skipped),
			want{kind: StepWait}},
		{"pr_open, only skipped checks after five minutes: park",
			in(PROpen, 6*time.Minute, skipped),
			want{kind: StepPark, cause: ParkNoChecks}},
		{"pr_open, checks pending: wait",
			in(PROpen, time.Minute, pending),
			want{kind: StepWait}},
		{"pr_open, pending and failed together: wait for the run to end",
			in(PROpen, time.Minute, checks(Checks{Total: 2, Pending: 1, Failed: 1})),
			want{kind: StepWait}},
		{"pr_open, failed: re-run, clock restarts, Reruns at the cap not above",
			in(PROpen, 10*time.Minute, failed),
			want{kind: StepRerun, change: func(p *Progress) { p.Reruns = 1; p.At = now }}},
		{"pr_open, one cancelled check: no merge",
			in(PROpen, 10*time.Minute, cancelled, reruns(1), fixCycles(2)),
			want{kind: StepPark, cause: ParkHandBackCap}},
		{"pr_open, failed just after a re-run: wait for GitHub to catch up",
			in(PROpen, 89*time.Second, failed, reruns(1)),
			want{kind: StepWait}},
		{"pr_open, re-runs used up: hand back",
			in(PROpen, 90*time.Second, failed, reruns(1)),
			want{kind: StepHandBack,
				change: to(HandedBack, func(p *Progress) { p.FixCycles = 1; p.Reruns = 0 })}},
		{"pr_open, ci_reruns 0: hand back without a re-run",
			in(PROpen, time.Minute, failed, func(f *Facts) { f.Config.CIReruns = 0 }),
			want{kind: StepHandBack, change: to(HandedBack, func(p *Progress) { p.FixCycles = 1 })}},
		{"pr_open, hand-back due but already handed this failure: save, no prompt",
			in(PROpen, 10*time.Minute, failed, reruns(1), fixCycles(1), handBackWritten),
			want{kind: StepSave,
				change: to(HandedBack, func(p *Progress) { p.FixCycles = 2; p.Reruns = 0 })}},
		{"pr_open, failed at the hand-back cap: park",
			in(PROpen, 10*time.Minute, failed, reruns(1), fixCycles(2)),
			want{kind: StepPark, cause: ParkHandBackCap}},
		{"pr_open, ci_fix_cycles 0: park without a hand-back",
			in(PROpen, 10*time.Minute, failed, reruns(1), func(f *Facts) { f.Config.CIFixCycles = 0 }),
			want{kind: StepPark, cause: ParkHandBackCap}},
		{"pr_open, green first time: save ci_green",
			in(PROpen, time.Minute),
			want{kind: StepSave, change: to(CIGreen)}},
		{"pr_open, passed with some skipped: save ci_green",
			in(PROpen, time.Minute, checks(Checks{Total: 3, Passed: 2, Skipped: 1})),
			want{kind: StepSave, change: to(CIGreen)}},
		{"pr_open, a check in no bucket: wait, not green",
			in(PROpen, time.Minute, checks(Checks{Total: 3, Passed: 2})),
			want{kind: StepWait}},

		// --- handed_back ---
		{"handed_back, waiting for a fix: wait",
			in(HandedBack, 10*time.Minute, handBackWritten, fixCycles(1)),
			want{kind: StepWait}},
		{"handed_back, newer clean quiet head: push, back to pr_open with Reruns 0",
			in(HandedBack, 10*time.Minute, handBackWritten, newHead(3*time.Minute), reruns(1), fixCycles(1)),
			want{kind: StepPush, change: to(PROpen, func(p *Progress) { p.Reruns = 0 })}},
		{"handed_back, newer head under two minutes old: wait even past the hour",
			in(HandedBack, 61*time.Minute, handBackWritten, newHead(time.Minute)),
			want{kind: StepWait}},
		{"handed_back, fix already pushed: save pr_open with Reruns 0",
			in(HandedBack, 61*time.Minute, reruns(1), fixCycles(1)),
			want{kind: StepSave, change: to(PROpen, func(p *Progress) { p.Reruns = 0 })}},
		{"handed_back, no new commit after 60 minutes: park",
			in(HandedBack, 60*time.Minute+time.Second, handBackWritten),
			want{kind: StepPark, cause: ParkHandBackTimeout}},
		{"handed_back, exactly 60 minutes: still waiting",
			in(HandedBack, 60*time.Minute, handBackWritten),
			want{kind: StepWait}},
		{"handed_back, dirty worktree past the hour: park",
			in(HandedBack, 61*time.Minute, handBackWritten, newHead(10*time.Minute), dirty),
			want{kind: StepPark, cause: ParkHandBackTimeout}},
		{"handed_back, local behind or diverged: park",
			in(HandedBack, time.Minute, handBackWritten, diverged),
			want{kind: StepPark, cause: ParkDiverged}},
		{"handed_back, branch gone from GitHub: park",
			in(HandedBack, time.Minute, handBackWritten, noRemote),
			want{kind: StepPark, cause: ParkBranchMissing}},

		// --- ci_green ---
		{"ci_green, still green: merge the sha that was checked",
			in(CIGreen, time.Second),
			want{kind: StepMerge, headSHA: "aaa", change: to(Merged)}},
		{"ci_green, pull request already MERGED: save merged, never merge twice",
			in(CIGreen, time.Second, pr(PRStateMerged), func(f *Facts) { f.PR.MergeSHA = "m1" }, noRemote),
			want{kind: StepSave, change: to(Merged, func(p *Progress) { p.MergedSHA = "m1" })}},
		{"ci_green, pull request can no longer be found: park",
			in(CIGreen, time.Second, noPR),
			want{kind: StepPark, cause: ParkPRMissing}},
		{"ci_green, PR closed: park",
			in(CIGreen, time.Second, pr(PRStateClosed)),
			want{kind: StepPark, cause: ParkPRClosed}},
		{"ci_green, branch gone from GitHub: park",
			in(CIGreen, time.Second, noRemote),
			want{kind: StepPark, cause: ParkBranchMissing}},
		{"ci_green, newer head not green any more: save pr_open",
			in(CIGreen, time.Second, pending, reruns(1), func(f *Facts) { f.RemoteSHA = "ccc" }),
			want{kind: StepSave, change: to(PROpen, func(p *Progress) { p.Reruns = 0 })}},
		{"ci_green, newer head with no checks yet: save pr_open, no merge",
			in(CIGreen, time.Second, noChecks),
			want{kind: StepSave, change: to(PROpen)}},
		{"ci_green, now conflicting: save pr_open",
			in(CIGreen, time.Second, conflicting),
			want{kind: StepSave, change: to(PROpen)}},
		{"ci_green, env vars not answered: park with them",
			in(CIGreen, time.Second, func(f *Facts) { f.EnvVars = []string{"API_KEY", "DB_URL"} }),
			want{kind: StepPark, cause: ParkEnvVar, envVars: []string{"API_KEY", "DB_URL"}}},
		{"ci_green, env vars but not green: back to pr_open before asking the owner",
			in(CIGreen, time.Second, failed, func(f *Facts) { f.EnvVars = []string{"API_KEY"} }),
			want{kind: StepSave, change: to(PROpen)}},

		// --- merged ---
		{"merged, merge sha not saved: fill it from the pull request, clock untouched",
			in(Merged, time.Minute, pr(PRStateMerged), func(f *Facts) { f.PR.MergeSHA = "m1"; f.BaseSynced = false }),
			want{kind: StepSave, change: func(p *Progress) { p.MergedSHA = "m1" }}},
		{"merged, GitHub has not reported the merge sha: wait",
			in(Merged, time.Minute, pr(PRStateMerged)),
			want{kind: StepWait}},
		{"merged, pull request can no longer be found: park",
			in(Merged, time.Minute, noPR),
			want{kind: StepPark, cause: ParkPRMissing}},
		{"merged, base not synced: sync base, clock untouched",
			in(Merged, 20*time.Minute, mergedAt("m1"), func(f *Facts) { f.BaseSynced = false }),
			want{kind: StepSyncBase}},
		{"merged, remote branch still there: delete it, clock untouched",
			in(Merged, 20*time.Minute, mergedAt("m1")),
			want{kind: StepDeleteRemote}},
		{"merged, deploys off: save deploy_healthy",
			in(Merged, time.Minute, mergedAt("m1"), noRemote, func(f *Facts) { f.Config.Deploys = false }),
			want{kind: StepSave, change: to(DeployHealthy)}},
		{"merged, no health URL: save deploy_healthy",
			in(Merged, time.Minute, mergedAt("m1"), noRemote, func(f *Facts) { f.Config.HealthURL = "" }),
			want{kind: StepSave, change: to(DeployHealthy)}},
		{"merged, deploy healthy: save deploy_healthy",
			in(Merged, time.Minute, mergedAt("m1"), noRemote, func(f *Facts) { f.HealthChecked = true; f.Healthy = true }),
			want{kind: StepSave, change: to(DeployHealthy)}},
		{"merged, healthy on the last tick before the timeout would fire: save deploy_healthy",
			in(Merged, 16*time.Minute, mergedAt("m1"), noRemote, func(f *Facts) { f.HealthChecked = true; f.Healthy = true }),
			want{kind: StepSave, change: to(DeployHealthy)}},
		{"merged, deploy not healthy yet: wait",
			in(Merged, 14*time.Minute+59*time.Second, mergedAt("m1"), noRemote, func(f *Facts) { f.HealthChecked = true }),
			want{kind: StepWait}},
		{"merged, deploy past its timeout: stop the line",
			in(Merged, 15*time.Minute, mergedAt("m1"), noRemote, func(f *Facts) { f.HealthChecked = true }),
			want{kind: StepStop, change: to(Stopped)}},
		{"merged, health not checked and past the timeout: stop the line",
			in(Merged, 16*time.Minute, mergedAt("m1"), noRemote),
			want{kind: StepStop, change: to(Stopped)}},

		// --- merged, with an after-merge command ---
		{"merged, command set but merge sha not saved: record it first",
			in(Merged, time.Minute, command, pr(PRStateMerged), func(f *Facts) { f.PR.MergeSHA = "m1" }, noRemote),
			want{kind: StepSave, change: func(p *Progress) { p.MergedSHA = "m1" }}},
		{"merged, command set but base not synced yet: sync first",
			in(Merged, 20*time.Minute, mergedAt("m1"), command, noRemote, func(f *Facts) { f.BaseSynced = false }),
			want{kind: StepSyncBase}},
		{"merged, command set and remote branch still there: delete first",
			in(Merged, 20*time.Minute, mergedAt("m1"), command),
			want{kind: StepDeleteRemote}},
		{"merged, command set, base synced and branch gone: save after_merge",
			in(Merged, time.Minute, mergedAt("m1"), command, noRemote),
			want{kind: StepSave, change: to(AfterMerge), reason: "after-merge command is next"}},
		{"merged, command set and the deploy already past its timeout: after_merge still comes first",
			in(Merged, 20*time.Minute, mergedAt("m1"), command, noRemote, unhealthy),
			want{kind: StepSave, change: to(AfterMerge)}},
		{"merged, command set and the deploy already healthy: after_merge still comes first",
			in(Merged, time.Minute, mergedAt("m1"), command, noRemote, healthy),
			want{kind: StepSave, change: to(AfterMerge)}},

		// --- after_merge ---
		{"after_merge, no record: run the command, progress untouched",
			in(AfterMerge, time.Minute, afterMerge),
			want{kind: StepAfterMerge}},
		{"after_merge, no record and long past the deploy timeout: still run the command",
			in(AfterMerge, time.Hour, afterMerge, unhealthy),
			want{kind: StepAfterMerge}},
		{"after_merge, no merge commit saved: park, never run",
			in(AfterMerge, time.Minute, afterMerge, prog(func(p *Progress) { p.MergedSHA = "" })),
			want{kind: StepPark, cause: ParkUnknownState}},
		{"after_merge, no record and the command was removed, deploys off: save deploy_healthy",
			in(AfterMerge, time.Minute, afterMerge, func(f *Facts) { f.Config.AfterMerge = ""; f.Config.Deploys = false }),
			want{kind: StepSave, change: to(DeployHealthy)}},
		{"after_merge, no record and the command was removed, deploy not healthy yet: wait",
			in(AfterMerge, 14*time.Minute, afterMerge, unhealthy, func(f *Facts) { f.Config.AfterMerge = "" }),
			want{kind: StepWait}},
		{"after_merge, no record and the command was removed, deploy past its timeout: stop for the deploy",
			in(AfterMerge, 15*time.Minute, afterMerge, unhealthy, func(f *Facts) { f.Config.AfterMerge = "" }),
			want{kind: StepStop, change: to(Stopped), stop: StopDeploy}},
		{"after_merge, started and never finished: stop the line",
			in(AfterMerge, time.Minute, afterMerge, started),
			want{kind: StepStop, change: to(Stopped), stop: StopAfterMergeInterrupted, reason: "not known whether the command finished"}},
		{"after_merge, started and never finished, command since removed: still stop the line",
			in(AfterMerge, time.Minute, afterMerge, started, func(f *Facts) { f.Config.AfterMerge = "" }),
			want{kind: StepStop, change: to(Stopped), stop: StopAfterMergeInterrupted}},
		{"after_merge, exit code 3: stop the line",
			in(AfterMerge, time.Minute, afterMerge, ended(3, false, time.Second)),
			want{kind: StepStop, change: to(Stopped), stop: StopAfterMergeFailed, reason: "exit code 3"}},
		{"after_merge, exit code 3 even with a healthy deploy: stop the line",
			in(AfterMerge, time.Minute, afterMerge, ended(3, false, time.Second), healthy),
			want{kind: StepStop, change: to(Stopped), stop: StopAfterMergeFailed}},
		{"after_merge, timed out: stop the line, naming the limit",
			in(AfterMerge, 11*time.Minute, afterMerge, ended(-1, true, time.Second)),
			want{kind: StepStop, change: to(Stopped), stop: StopAfterMergeFailed, reason: "did not finish within 10m0s"}},
		{"after_merge, the command could not be started: stop the line",
			in(AfterMerge, time.Minute, afterMerge, ended(-1, false, time.Second)),
			want{kind: StepStop, change: to(Stopped), stop: StopAfterMergeFailed, reason: "could not be started"}},
		{"after_merge, exit 0 and deploys off: save deploy_healthy",
			in(AfterMerge, time.Minute, afterMerge, ended(0, false, time.Second), func(f *Facts) { f.Config.Deploys = false }),
			want{kind: StepSave, change: to(DeployHealthy)}},
		{"after_merge, exit 0 and no health URL: save deploy_healthy",
			in(AfterMerge, time.Minute, afterMerge, ended(0, false, time.Second), func(f *Facts) { f.Config.HealthURL = "" }),
			want{kind: StepSave, change: to(DeployHealthy)}},
		{"after_merge, exit 0 and the deploy is healthy: save deploy_healthy",
			in(AfterMerge, time.Minute, afterMerge, ended(0, false, time.Second), healthy),
			want{kind: StepSave, change: to(DeployHealthy)}},
		{"after_merge, a slow command does not use up the deploy's time: wait",
			in(AfterMerge, 20*time.Minute, afterMerge, ended(0, false, 14*time.Minute+59*time.Second), unhealthy),
			want{kind: StepWait}},
		{"after_merge, exit 0 and the deploy past its timeout since the command ended: stop for the deploy",
			in(AfterMerge, 20*time.Minute, afterMerge, ended(0, false, 15*time.Minute), unhealthy),
			want{kind: StepStop, change: to(Stopped), stop: StopDeploy, reason: "did not become healthy within 15m0s"}},
		{"after_merge, exit 0 just now but entered long ago: the timeout is not measured from the entry",
			in(AfterMerge, time.Hour, afterMerge, ended(0, false, time.Second)),
			want{kind: StepWait}},
		{"after_merge, reads failing: still runs the command",
			in(AfterMerge, time.Minute, afterMerge, func(f *Facts) { f.ReadFailingFor = time.Hour }),
			want{kind: StepAfterMerge}},
		{"after_merge, reads failing after a good run: still goes on to the deploy",
			in(AfterMerge, time.Minute, afterMerge, ended(0, false, time.Second), healthy, func(f *Facts) { f.ReadFailingFor = time.Hour }),
			want{kind: StepSave, change: to(DeployHealthy)}},

		// --- deploy_healthy and stopped ---
		{"deploy_healthy: done",
			in(DeployHealthy, time.Minute, mergedAt("m1")),
			want{kind: StepDone}},
		{"deploy_healthy with no facts at all (session cleaned up): done",
			Facts{Now: now, Progress: Progress{State: DeployHealthy, At: now, PR: 7}},
			want{kind: StepDone}},
		{"stopped, marker present: wait",
			in(Stopped, time.Hour, func(f *Facts) { f.StopMarker = true }),
			want{kind: StepWait}},
		{"stopped, marker gone: done",
			in(Stopped, time.Hour),
			want{kind: StepDone}},

		// --- reads that keep failing ---
		{"reads failing for 30 minutes: park",
			in(PROpen, time.Hour, func(f *Facts) { f.ReadFailingFor = 30 * time.Minute }),
			want{kind: StepPark, cause: ParkReadFailing}},
		{"reads failing for less than 30 minutes: wait, act on nothing",
			Facts{Now: now, Progress: Progress{State: CIGreen, At: now, PR: 7}, ReadFailingFor: 29 * time.Minute},
			want{kind: StepWait}},
		{"reads failing while not started: park",
			in(NotStarted, 0, func(f *Facts) { f.ReadFailingFor = time.Hour }),
			want{kind: StepPark, cause: ParkReadFailing}},
		{"reads failing does not concern deploy_healthy: done",
			in(DeployHealthy, time.Minute, func(f *Facts) { f.ReadFailingFor = time.Hour }),
			want{kind: StepDone}},
		{"reads failing does not concern stopped: done once the marker is gone",
			in(Stopped, time.Minute, func(f *Facts) { f.ReadFailingFor = time.Hour }),
			want{kind: StepDone}},

		// --- a state this version does not know ---
		{"unknown saved state: park",
			in(State("flying"), time.Minute),
			want{kind: StepPark, cause: ParkUnknownState}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.facts)

			wantProgress := tt.facts.Progress
			if tt.want.change != nil {
				tt.want.change(&wantProgress)
			}
			if got.Kind != tt.want.kind {
				t.Errorf("Kind = %q, want %q (reason: %s)", got.Kind, tt.want.kind, got.Reason)
			}
			if got.Cause != tt.want.cause {
				t.Errorf("Cause = %q, want %q", got.Cause, tt.want.cause)
			}
			if got.Progress != wantProgress {
				t.Errorf("Progress = %+v, want %+v", got.Progress, wantProgress)
			}
			if got.Next != got.Progress.State {
				t.Errorf("Next = %q but Progress.State = %q", got.Next, got.Progress.State)
			}
			if got.HeadSHA != tt.want.headSHA {
				t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, tt.want.headSHA)
			}
			if !reflect.DeepEqual(got.EnvVars, tt.want.envVars) {
				t.Errorf("EnvVars = %v, want %v", got.EnvVars, tt.want.envVars)
			}
			if got.Conflict != tt.want.conflict {
				t.Errorf("Conflict = %v, want %v", got.Conflict, tt.want.conflict)
			}
			if got.Reason == "" {
				t.Error("Reason is empty; every step says why")
			}
			if !strings.Contains(got.Reason, tt.want.reason) {
				t.Errorf("Reason = %q, want it to contain %q", got.Reason, tt.want.reason)
			}
			wantStop := tt.want.stop
			if tt.want.kind == StepStop && wantStop == "" {
				wantStop = StopDeploy
			}
			if got.Stop != wantStop {
				t.Errorf("Stop = %q, want %q", got.Stop, wantStop)
			}
			// The command's text is the owner's and may hold a secret; no
			// reason, which goes to the log and the stop marker, may quote it.
			if cmd := tt.facts.Config.AfterMerge; cmd != "" && strings.Contains(got.Reason, cmd) {
				t.Errorf("Reason quotes the after-merge command: %q", got.Reason)
			}

			// The rules no row may break, checked on every row.
			if got.Kind == StepMerge {
				c := tt.facts.Checks
				if c.Total == 0 || c.Pending > 0 || c.Failed > 0 || !c.Green() {
					t.Errorf("merged with checks %+v", c)
				}
				if got.HeadSHA == "" {
					t.Error("merge names no sha")
				}
			}
			if got.Progress.Reruns > tt.facts.Config.CIReruns && got.Progress.Reruns > tt.facts.Progress.Reruns {
				t.Errorf("Reruns raised to %d, above ci_reruns %d", got.Progress.Reruns, tt.facts.Config.CIReruns)
			}
			switch got.Kind {
			case StepWait, StepPark, StepDone, StepSyncBase, StepDeleteRemote, StepAfterMerge:
				if got.Progress != tt.facts.Progress {
					t.Errorf("%s changed the progress: %+v -> %+v", got.Kind, tt.facts.Progress, got.Progress)
				}
			}
			// The command is run from one place only: after_merge, with a
			// merge commit, a command to run and no record of a run.
			if got.Kind == StepAfterMerge {
				f := tt.facts
				if f.Progress.State != AfterMerge || f.Progress.MergedSHA == "" || f.Config.AfterMerge == "" || f.AfterMerge.Found {
					t.Errorf("the command was chosen in state %q, merge commit %q, command set %v, record %+v",
						f.Progress.State, f.Progress.MergedSHA, f.Config.AfterMerge != "", f.AfterMerge)
				}
			}
		})
	}
}

// mergedAt is a ship whose merge commit is already saved.
func mergedAt(sha string) mut {
	return func(f *Facts) {
		f.Progress.MergedSHA = sha
		f.PR.State = PRStateMerged
		f.PR.MergeSHA = sha
	}
}

// TestDecideNeverMergesUnchecked sweeps every count of passed, pending,
// failed and skipped checks through the two states that lead to a merge. It
// is load-bearing in the same way as the no-checks row, but exhaustive: a
// merge, or a save of ci_green, happens only when the checks are green.
func TestDecideNeverMergesUnchecked(t *testing.T) {
	for passed := 0; passed <= 2; passed++ {
		for pending := 0; pending <= 1; pending++ {
			for failed := 0; failed <= 1; failed++ {
				for skipped := 0; skipped <= 1; skipped++ {
					c := Checks{Total: passed + pending + failed + skipped,
						Passed: passed, Pending: pending, Failed: failed, Skipped: skipped}
					green := passed > 0 && pending == 0 && failed == 0

					got := Decide(in(CIGreen, time.Second, checks(c)))
					if (got.Kind == StepMerge) != green {
						t.Errorf("ci_green with %+v: Kind = %q, want merge == %v", c, got.Kind, green)
					}
					for _, ago := range []time.Duration{time.Minute, time.Hour} {
						got = Decide(in(PROpen, ago, checks(c), reruns(1), fixCycles(2)))
						if got.Kind == StepMerge {
							t.Errorf("pr_open with %+v merged", c)
						}
						if (got.Next == CIGreen) != green {
							t.Errorf("pr_open with %+v after %s: Next = %q, want ci_green == %v", c, ago, got.Next, green)
						}
					}
				}
			}
		}
	}
}

// TestStates pins the saved words. The ledger keeps its own copy of this
// list (this package must not import it), so a change here is a change to a
// file format.
func TestStates(t *testing.T) {
	want := []State{"committed", "pushed", "pr_open", "handed_back", "ci_green", "merged", "after_merge", "deploy_healthy", "stopped"}
	if got := States(); !reflect.DeepEqual(got, want) {
		t.Errorf("States() = %v, want %v", got, want)
	}
}

// TestEnvVarsAdded covers both shapes of pattern. The capture-group row is
// the load-bearing one: the real use is `\$\{([A-Z_]+):\?`, where the whole
// match is punctuation the owner cannot set.
func TestEnvVarsAdded(t *testing.T) {
	tests := []struct {
		name    string
		pattern string // empty: nil pattern
		lines   []string
		want    []string
	}{
		{"with a capture group: the group",
			`\$\{([A-Z_]+):\?`,
			[]string{`  url: ${DB_URL:?}`, `  key: ${API_KEY:?missing}`},
			[]string{"DB_URL", "API_KEY"}},
		{"without a capture group: the whole match",
			`[A-Z]+_TOKEN`,
			[]string{"env: GH_TOKEN", "other: NPM_TOKEN"},
			[]string{"GH_TOKEN", "NPM_TOKEN"}},
		{"unique, in the order first seen",
			`\$\{([A-Z_]+):\?`,
			[]string{`${B:?} ${A:?}`, `${B:?}`, `${C:?} ${A:?}`},
			[]string{"B", "A", "C"}},
		{"two matches on one line",
			`[A-Z]+_TOKEN`,
			[]string{"GH_TOKEN and NPM_TOKEN"},
			[]string{"GH_TOKEN", "NPM_TOKEN"}},
		{"no match", `\$\{([A-Z_]+):\?`, []string{"plain line", "${OPTIONAL:-x}"}, nil},
		{"no lines", `X`, nil, nil},
		{"nil pattern: no check", "", []string{"${DB_URL:?}"}, nil},
		{"an empty capture names no variable", `KEY=([A-Z]*)`, []string{"KEY=", "KEY=ABC"}, []string{"ABC"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var re *regexp.Regexp
			if tt.pattern != "" {
				re = regexp.MustCompile(tt.pattern)
			}
			if got := EnvVarsAdded(re, tt.lines); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EnvVarsAdded = %v, want %v", got, tt.want)
			}
		})
	}
}
