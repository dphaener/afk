package ship

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/projectconfig"
)

// PollEvery is the shortest time between two rounds of GitHub reads for one
// project. The daemon ticks every second, and a ship that asked GitHub on
// every tick would use up the hourly API allowance within minutes. The time of
// the last round is kept outside this package (Deps.LastPoll), because nothing
// may be held in memory between ticks.
const PollEvery = 20 * time.Second

// HandBackFile is the file, in the session's directory, that a hand-back
// writes for the session to read. It is also the only trace a typed prompt
// leaves, so it doubles as the "was this failure already handed back" read.
const HandBackFile = "ci-failure.md"

// handBackHeadPrefix starts the line of the hand-back file that names the
// commit the file was written for. handBackHead reads it back.
const handBackHeadPrefix = "Head commit: "

// AfterMergeLog is where the after-merge command's output is kept, relative to
// the project's main checkout. It is here only so a stop item can tell the
// owner where to look: the file is written by whoever fills Deps.RunAfterMerge,
// and that caller's path must stay this one.
const AfterMergeLog = ".afk/build/after-merge.log"

// The kinds of item a ship raises. They are the inbox's words, repeated here
// so this package does not import the inbox: the daemon turns an Item into an
// inbox item.
const (
	ItemFailure = "failure"
	ItemEnvVar  = "env_var"
)

// Request is one task's ship as the daemon knows it at this tick.
type Request struct {
	// Task is the master-plan task id and Session the session's name; both
	// appear in the texts shown to the owner.
	Task, Session string
	// Branch is the session's branch. Base is the branch the session was cut
	// from, used when the project's [ship] table names no base.
	Branch, Base string
	// SessionDir is the worktree's .afk/{session} directory, where the
	// hand-back file is written.
	SessionDir string
	// Progress is the progress last saved.
	Progress Progress
	// Config is the project's [ship] table.
	Config projectconfig.Ship
}

// Deps is everything Advance touches outside its arguments. Every field is
// required, with one exception: Git and GH may be nil when the saved state is
// AfterMerge, DeployHealthy or Stopped, because Advance calls neither there
// (the session's worktree may already be gone).
type Deps struct {
	Git Git
	GH  GH
	// Health asks the health URL once. An error is treated as "not healthy
	// yet": a deploy that is restarting refuses connections, and that is the
	// ordinary case, not a failure of the ship.
	Health func(url string) (healthy bool, err error)
	// Prompt types one line into the session.
	Prompt func(text string) error
	// WriteFile and ReadFile write and read the hand-back file. ReadFile must
	// return an error that matches fs.ErrNotExist when there is no file.
	WriteFile func(path string, data []byte) error
	ReadFile  func(path string) ([]byte, error)
	// EnvAnswered reports whether the owner has answered the item about this
	// environment variable for this task.
	EnvAnswered func(variable string) bool
	// Stop writes the project's stop marker; StopMarker reports whether it
	// exists.
	Stop       func(task, reason string) error
	StopMarker func() bool
	// LastPoll and MarkPoll hold the time of the last round of GitHub reads.
	LastPoll func() time.Time
	MarkPoll func(time.Time) error
	// ReadFailingSince is when the current run of failed reads began, zero
	// when the last read worked. MarkReadFailure records a failed read and
	// ClearReadFailure a good one.
	ReadFailingSince func() time.Time
	MarkReadFailure  func(time.Time) error
	ClearReadFailure func() error
	Now              func() time.Time
	// Save records the progress. It is the only persistence Advance has for
	// the ship's state; the after-merge run record below is the one other
	// thing it writes.
	Save func(Progress) error

	// The four fields below are the after-merge command. They are separate on
	// purpose: the start mark, the command and the finish mark are three
	// moments a kill can fall between, and each has its own answer.

	// AfterMergeRecord returns the run record on disk, whoever it belongs to:
	// the task and merge commit it was written for, and how the run stands.
	// ok is false when there is no record, and a damaged record is no record.
	// run.Found is not read; Advance sets it by comparing the task and the
	// merge commit with its own.
	AfterMergeRecord func() (task, mergedSHA string, run AfterMergeRun, ok bool)
	// MarkAfterMerge writes the run record, replacing whatever was there. A
	// run with Finished false is the start mark, written before the command
	// starts; the same call with Finished true records how it ended.
	// startedAt is passed both times because the record is written whole.
	MarkAfterMerge func(task, mergedSHA string, run AfterMergeRun, startedAt time.Time) error
	// RunAfterMerge runs the command in the project's main checkout and waits
	// for it, for at most timeout. It has the shape of this package's own
	// RunAfterMerge without the folder and the log path, which the caller
	// knows and Advance does not. An error means the command never ran.
	RunAfterMerge func(command string, timeout time.Duration) (exitCode int, timedOut bool, err error)
	// AfterMergeTail returns the end of the command's output for the stop
	// item, "" when there is none.
	AfterMergeTail func() string
}

// OutcomeKind says what the daemon should do after one call of Advance.
type OutcomeKind string

const (
	// OutcomeWait: nothing for the daemon to do; call again next tick. A step
	// that was carried out and saved is a wait too.
	OutcomeWait OutcomeKind = "wait"
	// OutcomePark: the owner has to act; Item says what.
	OutcomePark OutcomeKind = "park"
	// OutcomeStop: the line is stopped; Item says why and how to clear it.
	OutcomeStop OutcomeKind = "stop"
	// OutcomeDone: the ship is over and the session can be cleaned up.
	OutcomeDone OutcomeKind = "done"
)

// Item is what the owner is told when a ship parks or stops the line.
type Item struct {
	// Kind is ItemFailure or ItemEnvVar.
	Kind string
	// Title is one line. It is the same every time the same thing goes wrong
	// for the same task, so the daemon can tell an item it already raised.
	Title string
	// Body says what happened and what to do next.
	Body string
}

// Outcome is the result of one call of Advance.
type Outcome struct {
	Kind OutcomeKind
	// Reason is one plain sentence for the log.
	Reason string
	// Item is set on a park and on a stop.
	Item *Item
	// Progress is the progress as saved when Advance returned.
	Progress Progress
}

// Advance takes a ship at most one step forward and returns. It never loops
// and never sleeps; the daemon's tick is the loop.
//
// The order inside one call is fixed, and it is what makes a kill at any
// moment safe:
//
//  1. Read what is true now. Only the facts the saved state's rules look at
//     are read, each through Git, GH or Health.
//  2. Decide.
//  3. For an action: the state the ship is in is already saved; carry the
//     action out; then save the next state. If the process dies, or the save
//     fails, between the action and the save, the next call starts from the
//     old state, and its read finds the action already done (the remote head
//     equals the local one, a pull request exists, the pull request is
//     merged), so it saves and does not act a second time.
//
// The after-merge command is the exception to 3, because a command line
// leaves nothing on the outside to read back. Its trace is a run record
// written by Advance itself, the start before the command and the end after
// it (see afterMerge), and "a record exists" is the read that stops a second
// run.
//
// A read that fails changes nothing and is tried again at the next poll. An
// action that fails parks the task with an item carrying the error.
func Advance(r Request, d Deps) Outcome {
	a := &advance{r: r, d: d, now: d.Now(), cfg: r.Config}
	if a.cfg.Base == "" {
		a.cfg.Base = r.Base
	}
	state := r.Progress.State

	// A base is never guessed. "main" is right for most projects and wrong
	// for the rest, and a pull request opened against the wrong branch, or a
	// merge of it, cannot be taken back by a later tick.
	if readsGit(state) && a.cfg.Base == "" {
		step := Step{Kind: StepPark, Next: state, Progress: r.Progress, Cause: ParkNoBase,
			Reason: "there is no base branch to ship to"}
		return a.park(step, Facts{}, nil)
	}

	if polls(state) {
		// A last poll in the future (the clock was set back) must not hold
		// the ship until the clock catches up, hence the lower bound.
		since := a.now.Sub(d.LastPoll())
		if since >= 0 && since < PollEvery {
			return a.wait("waiting a moment before asking GitHub again")
		}
		// Marked before the reads, not after, so a round of reads that fails
		// or is killed half way still counts against the rate.
		if err := d.MarkPoll(a.now); err != nil {
			return a.wait(fmt.Sprintf("could not record the time of this GitHub poll: %v", err))
		}
	}

	f, err := a.gather()
	if err != nil {
		return a.readFailed(err)
	}
	if readsGit(state) && !d.ReadFailingSince().IsZero() {
		// An error here leaves the old mark in place, which at worst parks
		// the task early; the park item then carries the next read error.
		_ = d.ClearReadFailure()
	}
	return a.carryOut(Decide(f), f)
}

// advance holds one call's arguments so the helpers below do not each take
// five parameters. It lives for one call of Advance and no longer.
type advance struct {
	r   Request
	d   Deps
	now time.Time
	// cfg is the request's [ship] table with Base filled in from the
	// session's base when the table names none.
	cfg projectconfig.Ship
}

// readsGit reports whether a state's rules read git or GitHub. AfterMerge,
// DeployHealthy and Stopped do not: by then the session may be cleaned up,
// and Git and GH may be nil. An unknown state reads nothing either; Decide
// parks it.
func readsGit(s State) bool {
	switch s {
	case NotStarted, Committed, Pushed, PROpen, HandedBack, CIGreen, Merged:
		return true
	}
	return false
}

// polls reports whether a state is one that is waited in while GitHub is
// asked again and again, and so is held to PollEvery. NotStarted and
// Committed read once and move on, so they are not. AfterMerge asks GitHub
// nothing, but it is waited in while the health URL is asked, and it is held
// to the same rate so a deploy is polled no more often there than in Merged.
func polls(s State) bool {
	switch s {
	case Pushed, PROpen, HandedBack, CIGreen, Merged, AfterMerge:
		return true
	}
	return false
}

// gather reads the facts the saved state's rules look at and nothing more.
// It stops reading as soon as an earlier rule of the state is certain to
// decide (a pull request that is gone, a branch that is not on the remote),
// so the facts it leaves at zero are ones Decide will not reach. An error
// means a read failed and the facts must not be used.
func (a *advance) gather() (Facts, error) {
	f := Facts{Now: a.now, Progress: a.r.Progress, Config: a.cfg}
	git, gh := a.d.Git, a.d.GH
	branch := a.r.Branch

	switch a.r.Progress.State {
	case NotStarted:
		// A tool that is missing or logged out is a fact for the owner, not
		// a read to retry: Decide parks on it before anything else.
		if err := gh.Ready(); err != nil {
			f.GHError = err.Error()
			return f, nil
		}
		f.GHReady = true
		clean, err := git.Clean()
		if err != nil {
			return f, err
		}
		f.WorktreeClean = clean
		ahead, err := git.AheadOfBase(a.cfg.Base, branch)
		if err != nil {
			return f, err
		}
		f.AheadOfBase = ahead

	case Committed:
		local, err := git.LocalSHA(branch)
		if err != nil {
			return f, err
		}
		f.LocalSHA = local
		remote, found, err := git.RemoteSHA(branch)
		if err != nil {
			return f, err
		}
		f.RemoteSHA, f.RemoteFound = remote, found

	case Pushed:
		if err := a.findPR(&f); err != nil {
			return f, err
		}

	case PROpen:
		if err := a.findPR(&f); err != nil {
			return f, err
		}
		if !f.PRFound || f.PR.State != PRStateOpen {
			return f, nil
		}
		// The heads are read before the checks (see Facts).
		if err := a.heads(&f); err != nil {
			return f, err
		}
		if !f.RemoteFound {
			return f, nil
		}
		c, err := gh.Checks(f.PR.Number)
		if err != nil {
			return f, err
		}
		f.Checks = c
		if f.PR.Conflicting || c.Failed > 0 {
			written, err := a.handBackWritten(f.RemoteSHA)
			if err != nil {
				return f, err
			}
			f.HandBackWritten = written
		}

	case HandedBack:
		if err := a.heads(&f); err != nil {
			return f, err
		}
		if !f.RemoteFound {
			return f, nil
		}
		written, err := a.handBackWritten(f.RemoteSHA)
		if err != nil {
			return f, err
		}
		f.HandBackWritten = written

	case CIGreen:
		if err := a.findPR(&f); err != nil {
			return f, err
		}
		if !f.PRFound || f.PR.State != PRStateOpen {
			return f, nil
		}
		remote, found, err := git.RemoteSHA(branch)
		if err != nil {
			return f, err
		}
		f.RemoteSHA, f.RemoteFound = remote, found
		if !found {
			return f, nil
		}
		c, err := gh.Checks(f.PR.Number)
		if err != nil {
			return f, err
		}
		f.Checks = c
		if !c.Green() || f.PR.Conflicting {
			return f, nil
		}
		if pattern := a.cfg.EnvPattern(); pattern != nil {
			lines, err := git.AddedLines(a.cfg.Base, branch)
			if err != nil {
				return f, err
			}
			for _, v := range EnvVarsAdded(pattern, lines) {
				if !a.d.EnvAnswered(v) {
					f.EnvVars = append(f.EnvVars, v)
				}
			}
		}

	case Merged:
		if err := a.findPR(&f); err != nil {
			return f, err
		}
		if !f.PRFound || a.r.Progress.MergedSHA == "" {
			return f, nil
		}
		synced, err := git.BaseSynced(a.cfg.Base, a.r.Progress.MergedSHA)
		if err != nil {
			return f, err
		}
		f.BaseSynced = synced
		if !synced {
			return f, nil
		}
		remote, found, err := git.RemoteSHA(branch)
		if err != nil {
			return f, err
		}
		f.RemoteSHA, f.RemoteFound = remote, found
		// With an after-merge command the deploy is not looked at from this
		// state at all: Decide moves to AfterMerge first, and the health URL
		// is first asked after the command, which is usually what brings the
		// new version up.
		if found || a.cfg.AfterMerge != "" || !a.cfg.Deploys || a.cfg.HealthURL == "" {
			return f, nil
		}
		// The error is dropped on purpose (see Deps.Health), and its text is
		// not put in any reason: it can quote the URL, and a health URL may
		// carry a password.
		healthy, err := a.d.Health(a.cfg.HealthURL)
		f.HealthChecked = true
		f.Healthy = healthy && err == nil

	case AfterMerge:
		f.AfterMerge = a.afterMergeRun()
		// The health URL is asked only where Decide's deploy rules will run:
		// after a command that ended well, or when there is no command to
		// run any more. Asking while the command is still due would be a
		// wasted call, and after a failure the line stops whatever it says.
		run := f.AfterMerge
		ranWell := run.Found && run.Finished && !run.TimedOut && run.ExitCode == 0
		nothingToRun := !run.Found && a.cfg.AfterMerge == ""
		if (ranWell || nothingToRun) && a.cfg.Deploys && a.cfg.HealthURL != "" {
			// The error is dropped for the same reasons as in Merged.
			healthy, err := a.d.Health(a.cfg.HealthURL)
			f.HealthChecked = true
			f.Healthy = healthy && err == nil
		}

	case Stopped:
		f.StopMarker = a.d.StopMarker()
	}
	return f, nil
}

// afterMergeRun reads the run record and answers for this ship only. A record
// written for another task, or for an earlier merge of this one, is the last
// ship's leftover (the file is one per project and is never deleted) and must
// read as "the command has not been started": taking it for this ship's would
// skip the command, or stop the line for a failure that was already dealt
// with. An empty merge commit never matches, so two records that both lack
// one are not mistaken for each other.
func (a *advance) afterMergeRun() AfterMergeRun {
	task, sha, run, ok := a.d.AfterMergeRecord()
	if !ok || sha == "" || task != a.r.Task || sha != a.r.Progress.MergedSHA {
		return AfterMergeRun{}
	}
	run.Found = true
	return run
}

func (a *advance) findPR(f *Facts) error {
	pr, found, err := a.d.GH.FindPR(a.r.Branch)
	if err != nil {
		return err
	}
	f.PR, f.PRFound = pr, found
	return nil
}

// heads reads the remote and local heads and, only when they differ, the
// three facts the newer-head rules look at. When the heads are equal those
// rules do not run, so LocalAhead and WorktreeClean are left true ("nothing is
// wrong") without a call.
func (a *advance) heads(f *Facts) error {
	git, branch := a.d.Git, a.r.Branch
	remote, found, err := git.RemoteSHA(branch)
	if err != nil {
		return err
	}
	f.RemoteSHA, f.RemoteFound = remote, found
	if !found {
		return nil
	}
	local, err := git.LocalSHA(branch)
	if err != nil {
		return err
	}
	f.LocalSHA = local
	f.LocalAhead, f.WorktreeClean = true, true
	if local == remote {
		return nil
	}
	ahead, err := git.Ahead(branch, remote)
	if err != nil {
		return err
	}
	f.LocalAhead = ahead
	if !ahead {
		return nil
	}
	clean, err := git.Clean()
	if err != nil {
		return err
	}
	f.WorktreeClean = clean
	if !clean {
		return nil
	}
	at, err := git.HeadTime(branch)
	if err != nil {
		return err
	}
	f.HeadAge = a.now.Sub(at)
	return nil
}

// handBackWritten reports whether the hand-back file names remoteSHA: the
// session has already been told about this head's failure. No file is "not
// told". Any other read error is a failed read, not "not told": answering
// "not told" on a file that merely could not be read would prompt the session
// a second time.
func (a *advance) handBackWritten(remoteSHA string) (bool, error) {
	if a.r.SessionDir == "" || remoteSHA == "" {
		return false, nil
	}
	data, err := a.d.ReadFile(a.handBackPath())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return handBackHead(data) == remoteSHA, nil
}

func (a *advance) handBackPath() string {
	return filepath.Join(a.r.SessionDir, HandBackFile)
}

// handBackHead returns the commit a hand-back file was written for, or ""
// when the text names none. The first matching line wins: it sits above the
// log tail, which is text from CI and could hold a line that looks the same.
func handBackHead(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, handBackHeadPrefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// readFailed is the answer to any failed read: wait, and after
// ReadFailureLimit of nothing but failures, park. Decide is asked only for
// the park, with nothing but the time in the facts, because the facts that
// were being read are half there.
func (a *advance) readFailed(err error) Outcome {
	since := a.d.ReadFailingSince()
	if !since.IsZero() {
		f := Facts{Now: a.now, Progress: a.r.Progress, Config: a.cfg, ReadFailingFor: a.now.Sub(since)}
		if step := Decide(f); step.Kind == StepPark {
			// The mark is cleared so that the owner's answer buys a fresh
			// thirty minutes. Left in place, the first failed read after the
			// answer would park again at once.
			_ = a.d.ClearReadFailure()
			return a.park(step, f, err)
		}
	}
	reason := fmt.Sprintf("a read failed and will be tried again: %v", err)
	if markErr := a.d.MarkReadFailure(a.now); markErr != nil {
		reason += fmt.Sprintf(" (and the failure could not be recorded: %v)", markErr)
	}
	return a.wait(reason)
}

// carryOut does the one step Decide chose.
func (a *advance) carryOut(step Step, f Facts) Outcome {
	git, gh := a.d.Git, a.d.GH
	task, branch := a.r.Task, a.r.Branch

	switch step.Kind {
	case StepWait:
		return a.wait(step.Reason)

	case StepDone:
		return Outcome{Kind: OutcomeDone, Reason: step.Reason, Progress: a.r.Progress}

	case StepSave:
		return a.save(step.Progress, step.Reason)

	case StepPark:
		return a.park(step, f, nil)

	case StepPush:
		if err := git.Push(branch); err != nil {
			return a.failed(
				fmt.Sprintf("%s could not be pushed to GitHub", task),
				fmt.Sprintf("afk tried to push the branch %s and GitHub refused it.", branch),
				err,
				pushHint(err))
		}
		return a.save(step.Progress, step.Reason)

	case StepOpenPR:
		subject, body, err := git.Title(branch)
		if err != nil {
			return a.readFailed(err)
		}
		pr, err := gh.OpenPR(branch, a.cfg.Base, subject, a.prBody(body))
		if err != nil {
			return a.failed(
				fmt.Sprintf("A pull request for %s could not be opened", task),
				fmt.Sprintf("afk pushed the branch %s but could not open a pull request for it against %s.", branch, a.cfg.Base),
				err,
				"Answer this item once the cause is fixed and afk tries again. "+
					"If you open the pull request yourself, afk finds it and carries on.")
		}
		next := step.Progress
		next.PR = pr.Number
		return a.save(next, step.Reason)

	case StepRerun:
		if err := gh.RerunFailed(f.PR.Number); err != nil {
			return a.failed(
				fmt.Sprintf("The failed checks for %s could not be re-run", task),
				fmt.Sprintf("Checks failed on pull request #%d and afk could not re-run them.", f.PR.Number),
				err,
				"Answer this item and afk tries the re-run again.")
		}
		return a.save(step.Progress, step.Reason)

	case StepHandBack:
		return a.handBack(step, f)

	case StepMerge:
		if err := gh.Merge(f.PR.Number, a.cfg.MergeMethod, step.HeadSHA); err != nil {
			return a.failed(
				fmt.Sprintf("%s could not be merged", task),
				fmt.Sprintf("Every check passed on pull request #%d, but the merge was refused.", f.PR.Number),
				err,
				"Answer this item once the cause is fixed and afk tries the merge again. "+
					"If you merge the pull request yourself, afk notices and carries on.")
		}
		return a.save(step.Progress, step.Reason)

	case StepSyncBase:
		if err := git.SyncBase(a.cfg.Base); err != nil {
			return a.failed(
				fmt.Sprintf("Your local %s could not be updated after %s merged", a.cfg.Base, task),
				fmt.Sprintf("%s is merged on GitHub, but afk could not bring your local %s branch up to date with it. "+
					"The likely cause is that %s is checked out in another worktree, "+
					"or that the project checkout has local commits on %s that GitHub does not have.",
					task, a.cfg.Base, a.cfg.Base, a.cfg.Base),
				err,
				fmt.Sprintf("Update %s by hand (for example `git pull --ff-only` where it is checked out), "+
					"then answer this item. The next task is built on your local %s, so afk waits until it holds this merge.",
					a.cfg.Base, a.cfg.Base))
		}
		// Nothing to save: the progress does not change (At must stay at the
		// time of the merge), and the next call's BaseSynced read is the
		// record that this happened.
		return a.wait(step.Reason)

	case StepDeleteRemote:
		if err := git.DeleteRemote(branch); err != nil {
			return a.failed(
				fmt.Sprintf("The merged branch for %s could not be deleted on GitHub", task),
				fmt.Sprintf("%s is merged, but afk could not delete the branch %s on GitHub.", task, branch),
				err,
				"Delete the branch on GitHub yourself, or answer this item and afk tries again.")
		}
		// Nothing to save, for the same reason as the base update.
		return a.wait(step.Reason)

	case StepAfterMerge:
		return a.afterMerge()

	case StepStop:
		return a.stop(step, f)
	}
	return a.wait(fmt.Sprintf("the ship decided on %q, which this version cannot carry out", string(step.Kind)))
}

// handBack gives a failure to the session: write the file, type the prompt,
// save. The order is what stops a second prompt.
//
// The file names the remote head it was written for and is written first.
// After a crash anywhere later, the next call reads the file, sees that it
// names the current head, and Decide saves HandedBack without prompting. So a
// crash after the prompt never prompts twice. A crash between the file and
// the prompt loses that one prompt: the session is never told, makes no
// commit, and the HandBackTimeout park an hour later is what tells the owner.
// That is the cheaper of the two mistakes; a prompt typed twice lands in the
// middle of the fix the first one started.
func (a *advance) handBack(step Step, f Facts) Outcome {
	task := a.r.Task
	var log string
	if !step.Conflict {
		var err error
		// The log is the useful half of the file, so a failure to fetch it
		// is waited out like any other read instead of handing back a file
		// without it.
		if log, err = a.d.GH.FailedLog(f.PR.Number); err != nil {
			return a.readFailed(err)
		}
	}
	path := a.handBackPath()
	if err := a.d.WriteFile(path, []byte(a.handBackText(step, f, log))); err != nil {
		return a.failed(
			fmt.Sprintf("The CI failure for %s could not be handed to its session", task),
			fmt.Sprintf("afk could not write %s, the file that tells the session what failed.", path),
			err,
			"Answer this item once the cause is fixed and afk tries again.")
	}
	prompt := fmt.Sprintf("The pull request for this branch needs a fix. Read %s, fix the cause and commit on this branch. "+
		"Do not push and do not open a pull request.", path)
	if err := a.d.Prompt(prompt); err != nil {
		return a.failed(
			fmt.Sprintf("The session for %s could not be reached to fix its pull request", task),
			fmt.Sprintf("Pull request #%d needs a fix, and the session %s could not be reached to be told. "+
				"What failed is written in %s.", f.PR.Number, a.r.Session, path),
			err,
			fmt.Sprintf("Open the session with `afk attach %s` and point it at %s: it should fix the cause and commit, "+
				"without pushing. Then answer this item; afk does not type the prompt again, it waits for the commit and pushes it.",
				a.r.Session, HandBackFile))
	}
	return a.save(step.Progress, step.Reason)
}

// handBackText is the hand-back file. It is written for an agent to read, so
// it says what failed and exactly what is and is not wanted back.
func (a *advance) handBackText(step Step, f Facts, log string) string {
	var b strings.Builder
	if step.Conflict {
		fmt.Fprintf(&b, "# Pull request #%d conflicts with %s\n\n", f.PR.Number, a.cfg.Base)
	} else {
		fmt.Fprintf(&b, "# CI failed on pull request #%d\n\n", f.PR.Number)
	}
	fmt.Fprintf(&b, "Pull request: #%d\n", f.PR.Number)
	fmt.Fprintf(&b, "%s%s\n\n", handBackHeadPrefix, f.RemoteSHA)
	if step.Conflict {
		fmt.Fprintf(&b, "The branch `%s` conflicts with `%s`, so the pull request cannot be merged.\n", a.r.Branch, a.cfg.Base)
		fmt.Fprintf(&b, "It must have `%s` merged in: merge `%s` into this branch, resolve the conflicts and commit the result.\n\n", a.cfg.Base, a.cfg.Base)
	} else {
		b.WriteString("These checks failed, and re-running them did not help:\n\n")
		if len(f.Checks.FailedNames) == 0 {
			b.WriteString("- (GitHub did not name the failed jobs)\n")
		}
		for _, name := range f.Checks.FailedNames {
			fmt.Fprintf(&b, "- %s\n", name)
		}
		b.WriteString("\n## The end of the failed jobs' log\n\n")
		// Indented, not fenced: the log is CI output and may hold a fence.
		for _, line := range strings.Split(strings.TrimRight(log, "\n"), "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
		b.WriteString("\n")
	}
	b.WriteString("## What to do\n\n")
	b.WriteString("Fix the cause and commit on this branch. Do not push and do not open a pull request: ")
	b.WriteString("afk pushes your commit and watches the checks again.\n")
	return b.String()
}

// afterMerge runs the project's after-merge command: start mark, command,
// finish mark. Nothing is saved to the ledger; the ship stays in AfterMerge
// and the next call reads the record.
//
// The command must never run twice for one merge, and it leaves no trace of
// its own to read back, so the record is written on both sides of it. A kill
// at each point then has one reading:
//
//   - before the start mark: no record, so the next call runs the command,
//     for the first time.
//   - after the start mark, before or while the command runs: a started,
//     unfinished record. Nobody can know whether the command ran or how far
//     it got, so Decide stops the line and the owner looks.
//   - after the finish mark: the record says how it ended, and the next call
//     carries on from there.
//
// That is why a start mark that cannot be written runs nothing (a command
// with no start mark could run again after a crash), and why a finish mark
// that cannot be written is left alone: the record on disk still says
// "started", which stops the line at the next call. That is the safe reading
// of a result that was lost.
func (a *advance) afterMerge() Outcome {
	task, sha := a.r.Task, a.r.Progress.MergedSHA
	if err := a.d.MarkAfterMerge(task, sha, AfterMergeRun{}, a.now); err != nil {
		return a.wait(fmt.Sprintf(
			"the start of the after-merge command could not be recorded, so it was not run and will be tried again: %v", err))
	}
	code, timedOut, err := a.d.RunAfterMerge(a.cfg.AfterMerge, a.cfg.AfterMergeTimeout)
	if err != nil {
		// The shell could not be started, or the log could not be opened.
		// It is recorded as a finished run that failed, so the next call
		// stops the line; -1 is the runner's own word for "never ran". Not
		// recording it would leave a start mark, and the owner would be told
		// afk was interrupted, which is not what happened.
		code, timedOut = -1, false
	}
	// The clock is read again, not a.now: the command may have taken minutes,
	// and the deploy timeout is measured from this time.
	run := AfterMergeRun{Finished: true, TimedOut: timedOut, ExitCode: code, FinishedAt: a.d.Now()}
	if markErr := a.d.MarkAfterMerge(task, sha, run, a.now); markErr != nil {
		return a.wait(fmt.Sprintf("the after-merge command ended, but how it ended could not be recorded: %v", markErr))
	}
	switch {
	case err != nil:
		return a.wait(fmt.Sprintf("the after-merge command could not be started: %v", err))
	case timedOut:
		return a.wait(fmt.Sprintf("the after-merge command did not finish within %s and was stopped",
			roundDuration(a.cfg.AfterMergeTimeout)))
	case code != 0:
		return a.wait(fmt.Sprintf("the after-merge command ended with exit code %d", code))
	}
	return a.wait("the after-merge command finished")
}

// stop stops the line: marker first, then the saved state, then the outcome.
//
// The marker comes first because Decide reads a saved Stopped with no marker
// as "the owner cleared it" and finishes the task. Saving first and dying
// before the marker would therefore mark a failed deploy, or a failed
// after-merge command, done with nobody told. In this order a crash between
// the two finds the ship still where it was (in Merged or AfterMerge and past
// its timeout, or in AfterMerge with the same run record), and stops again,
// which only rewrites the marker; a crash after the save finds the marker and
// waits.
func (a *advance) stop(step Step, f Facts) Outcome {
	if err := a.d.Stop(a.r.Task, step.Reason); err != nil {
		return a.wait(fmt.Sprintf("the line could not be stopped and will be tried again: %v", err))
	}
	if err := a.d.Save(step.Progress); err != nil {
		return a.wait(fmt.Sprintf("the ship's progress could not be saved: %v", err))
	}
	return Outcome{Kind: OutcomeStop, Reason: step.Reason, Item: a.stopItem(step, f), Progress: step.Progress}
}

// stopItem is what the owner reads when the line stops. The words depend on
// the cause, because what to do differs: a deploy is checked and rolled back,
// a command is fixed and perhaps run by hand.
//
// The command's own text is never put in the title. A title leaves the
// machine in a push notification and is what the daemon matches items on, and
// a command line can hold a token. The body does not quote it either, though
// the end of the log may: the runner's first line of the log names it.
func (a *advance) stopItem(step Step, f Facts) *Item {
	task := a.r.Task
	switch step.Stop {
	case StopAfterMergeFailed:
		return &Item{
			Kind:  ItemFailure,
			Title: fmt.Sprintf("The after-merge command failed after %s", task),
			Body: fmt.Sprintf("%s was merged, but %s. "+
				"This is the command set as `after_merge` under `[ship]` in afk.toml; afk ran it in the project folder. "+
				"afk has stopped the line: no new task starts until you clear it.\n\n",
				task, afterMergeFailure(f)) + a.afterMergeHelp(),
		}
	case StopAfterMergeInterrupted:
		return &Item{
			Kind:  ItemFailure,
			Title: fmt.Sprintf("The after-merge command was interrupted after %s", task),
			Body: fmt.Sprintf("%s was merged and afk started the after-merge command "+
				"(`after_merge` under `[ship]` in afk.toml, run in the project folder). "+
				"afk itself was stopped while the command ran, so it does not know whether the command finished. "+
				"afk has stopped the line: no new task starts until you clear it.\n\n",
				task) + a.afterMergeHelp(),
		}
	}
	// StopDeploy, and a stop with no cause from a version that set none.
	where := healthURLForOwner(a.cfg.HealthURL)
	return &Item{
		Kind:  ItemFailure,
		Title: fmt.Sprintf("The deploy after %s did not become healthy", task),
		Body: fmt.Sprintf("%s was merged, but %s did not answer healthy within %s. "+
			"afk has stopped the line: no new task starts until you clear it.\n\n"+
			"Check the deploy, and fix it or roll it back. Then run `afk resume` to clear the stop; "+
			"%s is marked done once it is cleared.",
			task, where, roundDuration(a.cfg.DeployTimeout), task),
	}
}

// afterMergeHelp is the half of an after-merge stop item that both causes
// share: the end of the output, where the rest is, and what to do.
func (a *advance) afterMergeHelp() string {
	var b strings.Builder
	if tail := strings.TrimRight(a.d.AfterMergeTail(), "\n"); strings.TrimSpace(tail) != "" {
		b.WriteString("The end of the command's output:\n\n")
		// Indented, not fenced: the output is the command's and may hold a fence.
		for _, line := range strings.Split(tail, "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("afk has no output from the command.\n\n")
	}
	fmt.Fprintf(&b, "The full output is in `%s` in the project folder.\n\n", AfterMergeLog)
	fmt.Fprintf(&b, "Fix the cause. If the command still needs running, run it by hand in the project folder. "+
		"Then run `afk resume` to clear the stop; %s is marked done once it is cleared, "+
		"and afk does not run the command again.", a.r.Task)
	return b.String()
}

// healthURLForOwner is the health URL as it may be shown in an item, which
// can leave the machine in a push notification. A health URL may carry a
// password or a token, so the user part and the query are dropped; what is
// left still tells the owner which deploy is meant.
func healthURLForOwner(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "the health URL in afk.toml"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (a *advance) wait(reason string) Outcome {
	return Outcome{Kind: OutcomeWait, Reason: reason, Progress: a.r.Progress}
}

// save records the progress after a step. A save that fails is a wait, not a
// park: the progress returned is the old one, and the next call's reads find
// whatever the step already did.
func (a *advance) save(next Progress, reason string) Outcome {
	if err := a.d.Save(next); err != nil {
		return a.wait(fmt.Sprintf("the ship's progress could not be saved: %v", err))
	}
	return Outcome{Kind: OutcomeWait, Reason: reason, Progress: next}
}

// failed parks the task for an action that returned an error. The body is
// what happened, the command's own error, and what to do, in that order.
func (a *advance) failed(title, happened string, err error, todo string) Outcome {
	return Outcome{
		Kind:     OutcomePark,
		Reason:   fmt.Sprintf("%s: %v", title, err),
		Item:     &Item{Kind: ItemFailure, Title: title, Body: happened + "\n\nThe error was:\n\n    " + indentError(err) + "\n\n" + todo},
		Progress: a.r.Progress,
	}
}

// park turns a park step into the item the owner reads. lastErr is the read
// error behind a ParkReadFailing, nil otherwise.
func (a *advance) park(step Step, f Facts, lastErr error) Outcome {
	task, session, branch := a.r.Task, a.r.Session, a.r.Branch
	failedSet := fmt.Sprintf("`afk ledger set %s failed`", task)
	attach := fmt.Sprintf("`afk attach %s`", session)
	item := &Item{Kind: ItemFailure}

	switch step.Cause {
	case ParkNoBase:
		item.Title = fmt.Sprintf("%s cannot ship: no base branch is set", task)
		item.Body = "afk does not know which branch this work should be merged into, and it will not guess.\n\n" +
			"Set it in `afk.toml`:\n\n    [ship]\n    base = \"main\"\n\n" +
			"with the name of your own base branch, then answer this item and afk tries again."

	case ParkGHNotReady:
		item.Title = fmt.Sprintf("%s cannot ship: GitHub's command-line tool is not ready", task)
		item.Body = sentence(step.Reason) + "\n\n" +
			"Install `gh` or run `gh auth login`, then answer this item and afk tries again."

	case ParkDirty:
		item.Title = fmt.Sprintf("%s finished with changes that are not committed", task)
		item.Body = fmt.Sprintf("The session %s finished, but its worktree has changes to tracked files that were never committed. "+
			"afk ships only what is committed.\n\n"+
			"Open the session with %s and commit or discard the changes, then answer this item and afk tries again.",
			session, attach)

	case ParkNothingCommitted:
		item.Title = fmt.Sprintf("%s finished without committing anything", task)
		item.Body = fmt.Sprintf("The session %s finished, but its branch %s has no commit that %s does not already have, so there is nothing to ship.\n\n"+
			"If the task needed no change, run `afk ledger set %s done`. "+
			"Otherwise open the session with %s, get the work committed, and answer this item.",
			session, branch, a.cfg.Base, task, attach)

	case ParkPRClosed, ParkPRMissing:
		item.Title = fmt.Sprintf("The pull request for %s is closed or missing", task)
		item.Body = sentence(step.Reason) + " afk never opens a second pull request for the same work.\n\n" +
			"Reopen it on GitHub and answer this item, or run " + failedSet + " to give the task up."

	case ParkBranchMissing:
		item.Title = fmt.Sprintf("The branch for %s is gone from GitHub", task)
		item.Body = fmt.Sprintf("The branch %s was deleted on GitHub while its pull request was still waiting.\n\n"+
			"Restore the branch on GitHub (or push it again from the session's worktree) and answer this item, or run %s to give the task up.",
			branch, failedSet)

	case ParkDiverged:
		item.Title = fmt.Sprintf("The branch for %s has commits on GitHub that the session does not have", task)
		item.Body = fmt.Sprintf("The branch %s on GitHub has commits the session does not have, so afk cannot push the session's work without overwriting them, and it never force-pushes.\n\n"+
			"Pull them into the session's branch (open it with %s), then answer this item and afk pushes again.",
			branch, attach)

	case ParkHandBackCap:
		what := "CI is still failing"
		if f.PR.Conflicting {
			what = "the branch still conflicts with " + a.cfg.Base
		}
		item.Title = fmt.Sprintf("%s: %s after the session tried to fix it", task, what)
		body := fmt.Sprintf("On pull request #%d, %s after %s to the session %s.",
			f.PR.Number, what, plural(a.r.Progress.FixCycles, "hand-back", "hand-backs"), session)
		if !f.PR.Conflicting && len(f.Checks.FailedNames) > 0 {
			body += " The failing jobs: " + strings.Join(f.Checks.FailedNames, ", ") + "."
		}
		item.Body = body + "\n\n" + fmt.Sprintf(
			"Fix it yourself on the branch %s (open the session with %s), or run %s to give the task up. "+
				"Answering this item hands the failure to the session again.", branch, attach, failedSet)

	case ParkNoChecks:
		item.Title = fmt.Sprintf("No checks ran on the pull request for %s", task)
		item.Body = fmt.Sprintf("Pull request #%d has been open for five minutes and GitHub has reported no check on it. "+
			"afk never merges a pull request that no CI has passed.\n\n"+
			"Add a CI workflow that runs on pull requests, or merge the pull request by hand and run `afk ledger set %s done`. "+
			"Answering this item only makes afk wait another five minutes for checks.",
			f.PR.Number, task)

	case ParkHandBackTimeout:
		item.Title = fmt.Sprintf("The session for %s did not commit a fix", task)
		item.Body = fmt.Sprintf("afk handed a failure on the pull request to the session %s an hour ago, and it has made no new commit since. "+
			"What it was asked to fix is in %s.\n\n"+
			"Open the session with %s to see where it is. Answering this item makes afk wait another hour for its commit; "+
			"or run %s to give the task up.",
			session, a.handBackPath(), attach, failedSet)

	case ParkEnvVar:
		v := step.EnvVars[0]
		item.Kind = ItemEnvVar
		item.Title = fmt.Sprintf("Set %s before %s merges", v, task)
		body := fmt.Sprintf("The change for %s reads the environment variable %s, and merging deploys it. "+
			"Every check has passed; the merge is waiting only for this.\n\n"+
			"Set %s wherever the deployed app gets its environment, then answer this item and afk merges.",
			task, v, v)
		if rest := step.EnvVars[1:]; len(rest) > 0 {
			body += "\n\nThe change also reads " + strings.Join(rest, ", ") + "; an item for each follows this one."
		}
		item.Body = body

	case ParkReadFailing:
		item.Title = fmt.Sprintf("GitHub could not be reached while shipping %s", task)
		body := fmt.Sprintf("afk has not been able to read the state of %s from GitHub for %s.",
			task, roundDuration(f.ReadFailingFor))
		if lastErr != nil {
			body += "\n\nThe last error was:\n\n    " + indentError(lastErr)
		}
		item.Body = body + "\n\nCheck `gh auth status` and your network, then answer this item and afk tries again."

	default:
		// ParkUnknownState, and any cause a later version adds before this
		// switch learns it: the reason is still a plain sentence.
		item.Title = fmt.Sprintf("%s cannot ship: afk does not understand its saved progress", task)
		item.Body = sentence(step.Reason) + " It was probably written by a newer version of afk.\n\n" +
			"Update afk and answer this item, or run " + failedSet + " to give the task up."
	}
	return Outcome{Kind: OutcomePark, Reason: step.Reason, Item: item, Progress: a.r.Progress}
}

// prBody is the pull request's body: the commit's own body, then the task and
// session it came from, so the pull request can be traced back from GitHub.
func (a *advance) prBody(commitBody string) string {
	trailer := fmt.Sprintf("Task: %s\nSession: %s", a.r.Task, a.r.Session)
	if body := strings.TrimSpace(commitBody); body != "" {
		return body + "\n\n" + trailer
	}
	return trailer
}

// sentence turns one of Decide's reasons ("the session left ...") into a
// sentence for an item body.
func sentence(reason string) string {
	if reason == "" {
		return ""
	}
	s := strings.ToUpper(reason[:1]) + reason[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// indentError keeps a many-line error inside the indented block it is shown in.
func indentError(err error) string {
	return strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", "\n    ")
}
