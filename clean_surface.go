package main

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

// cleanSurfaceOutcome is what one clean ended as. Callers branch on it rather
// than on Text, the same way ledgerSetResult's callers branch on Outcome.
type cleanSurfaceOutcome string

const (
	// cleanCleaned: the session was torn down. Result says what was kept and
	// what was removed.
	cleanCleaned cleanSurfaceOutcome = "cleaned"
	// cleanRefusedOutcome: the clean was not attempted, so nothing changed: the
	// phase gate, a stale PhaseSeen, a --delete-branch with nothing to delete,
	// an unmerged branch with no terminal to ask on, or the owner said no.
	cleanRefusedOutcome cleanSurfaceOutcome = "refused"
	// cleanNotFound: no session answers to the name.
	cleanNotFound cleanSurfaceOutcome = "not_found"
	// cleanFailed: listing the sessions or the teardown itself failed. A
	// teardown can fail part way, so this does not promise nothing changed.
	cleanFailed cleanSurfaceOutcome = "failed"
)

// cleanSurfaceRequest is one clean as a caller asks for it. Force and
// DeleteBranch are `afk clean`'s two flags with the same meaning.
//
// CheckPhase and PhaseSeen are for a surface that showed the owner a phase and
// had them confirm against it (the web page). The phase is read again here and
// a difference is refused, because a session that moved on while the confirm
// was open is not the session the owner agreed to clean. This is the rule the
// dashboard applies in internal/ui's cleanRunner.run. The command has no shown
// phase, so it leaves CheckPhase off.
type cleanSurfaceRequest struct {
	Force        bool
	DeleteBranch bool
	CheckPhase   bool
	PhaseSeen    string
}

// cleanSurfaceResult is the outcome of one clean, printed by nobody.
//
// Lines is exactly what `afk clean <name>` prints on stdout after a success,
// one entry per line. Text is the refusal or failure sentence without the
// "afk clean: " prefix the command puts in front of it. Code is the command's
// exit code and ToStderr says which stream the command prints Text on.
//
// EchoStdout and EchoStderr are what internal/session wrote while it worked
// (its CleanResult.note warnings and unlink failures go to its Stderr). They
// are captured rather than let through so a surface such as the web page
// never writes on the daemon's own streams; the command prints them back on
// the stream they were meant for, which keeps its output as it was.
type cleanSurfaceResult struct {
	Outcome    cleanSurfaceOutcome
	Result     session.CleanResult
	Lines      []string
	Text       string
	Code       int
	ToStderr   bool
	EchoStdout string
	EchoStderr string
}

// cleanSurfaceDeps is everything a clean touches outside its arguments.
//
// Clean is the session package's own seam (git, the clock, the home folder,
// host liveness and signals); its Stdout and Stderr are always replaced by
// buffers. Discover, CleanSession and FreshPhase default to the real ones and
// exist so tests can list a fixed set of sessions, fail the teardown, or move
// the phase under a confirm without a real repository.
//
// TTY and Confirm are the interactive part, and only the command fills them:
// TTY is whether stdin is a terminal, Confirm asks a yes/no question on it. A
// surface leaves both zero, which means "no terminal to ask on" — exactly how
// a piped `afk clean` behaves. The questions live here, not in cmdClean ahead
// of the call, because each one is asked only after a check that needs the
// discovered target (the forced-clean question after the phase gate, the
// unmerged-branch one after the merge check), and asking before those checks
// would ask about a clean that is going to be refused anyway.
type cleanSurfaceDeps struct {
	Clean        session.CleanDeps
	Discover     func(repo string, d session.CleanDeps) ([]session.Session, error)
	CleanSession func(repo string, sess session.Session, d session.CleanDeps, opts session.CleanOptions) (session.CleanResult, error)
	FreshPhase   func(sess session.Session) string
	// BeforeClean makes the clean's CleanDeps.BeforeClean for repo, unless
	// Clean already carries one. It defaults to the real screen saver, so a
	// failed Rex session cleaned from the command or the web page keeps its
	// last screen (PLAN T17.13); a test passes a fake so no Rex is run.
	BeforeClean func(repo string) func(session.Session) error
	TTY         bool
	Confirm     func(title, yes, no string) (bool, error)
}

// defaultCleanSurfaceDeps is cleanSurfaceDeps wired to the real environment
// with no terminal: what a non-interactive surface uses.
func defaultCleanSurfaceDeps() cleanSurfaceDeps {
	return cleanSurfaceDeps{Clean: session.DefaultCleanDeps()}
}

func (d cleanSurfaceDeps) withDefaults() cleanSurfaceDeps {
	if d.Discover == nil {
		d.Discover = session.Discover
	}
	if d.CleanSession == nil {
		d.CleanSession = session.CleanSessionWithOptions
	}
	if d.FreshPhase == nil {
		d.FreshPhase = shownPhase
	}
	if d.BeforeClean == nil {
		d.BeforeClean = realScreenSaver().beforeClean
	}
	return d
}

// shownPhase reads a session's phase the way the surfaces that show one read
// it: internal/state's ReadStatus, the parser behind internal/discover, with a
// stale file read as the empty phase. session.Discover's own Status.Phase is
// not the same answer: it only json-decodes the file, so a file the stricter
// parser calls stale still yields a phase there, and comparing the two would
// refuse every stale row forever (internal/ui's dashboardPhase has the full
// account). A one-off session has no status path, which reads as stale and so
// as "", matching the empty phase the surfaces show for it.
func shownPhase(sess session.Session) string {
	res := state.ReadStatus(sess.StatusPath)
	if res.Stale {
		return ""
	}
	return string(res.Status.Phase)
}

// cleanSessionForSurface is `afk clean <name>` after its flags are parsed and
// the project root is resolved. The command and the web page both call it, so
// both make the same checks in the same order and the same teardown, and it
// prints nothing.
//
// The order of the checks is the command's, and it decides which message a
// request with two problems gets: not found, a --delete-branch with nothing to
// delete, the phase gate, the forced-clean question, then the merge check. The
// PhaseSeen check comes straight after the target is found, ahead of all of
// those, because a session that changed under the confirm makes every later
// answer about a different session than the one the owner looked at.
func cleanSessionForSurface(repo, name string, req cleanSurfaceRequest, deps cleanSurfaceDeps) cleanSurfaceResult {
	deps = deps.withDefaults()
	var stdout, stderr bytes.Buffer
	cd := deps.Clean
	cd.Stdout, cd.Stderr = &stdout, &stderr
	if cd.BeforeClean == nil {
		cd.BeforeClean = deps.BeforeClean(repo)
	}
	done := func(res cleanSurfaceResult) cleanSurfaceResult {
		res.EchoStdout, res.EchoStderr = stdout.String(), stderr.String()
		return res
	}
	refuse := func(format string, a ...any) cleanSurfaceResult {
		return done(cleanSurfaceResult{Outcome: cleanRefusedOutcome, Code: 1, ToStderr: true, Text: fmt.Sprintf(format, a...)})
	}

	sessions, err := deps.Discover(repo, cd)
	if err != nil {
		return done(cleanSurfaceResult{Outcome: cleanFailed, Code: 1, ToStderr: true, Text: err.Error()})
	}
	target := findCleanTarget(sessions, name)
	if target == nil {
		return done(cleanSurfaceResult{Outcome: cleanNotFound, Code: 1, ToStderr: true,
			Text: fmt.Sprintf("no session named %q found (run `afk clean` to list sessions)", name)})
	}
	if req.CheckPhase {
		if fresh := deps.FreshPhase(*target); fresh != req.PhaseSeen {
			return refuse("session %q changed since it was shown: its phase was %s and is now %s. Nothing was cleaned. Look at it again, and clean it again if you still want to.",
				name, phaseWords(req.PhaseSeen), phaseWords(fresh))
		}
	}
	if deleteBranchNoOpRefused(req.DeleteBranch, target.Scratch, target.Kind) {
		return refuse("--delete-branch has no effect on %q: it has no branch of its own", name)
	}
	if cleanRefused(req.Force, *target) {
		return refuse("session %q is in phase %q (not done/failed) — pass --force to clean it anyway",
			name, target.Status.Phase)
	}

	if shouldConfirmForcedClean(req.Force, *target, deps.TTY) {
		if !deps.ask(forcedCleanPrompt(name, target.Status.Phase, req.DeleteBranch, target.Kind), "Force clean", "Cancel") {
			return refuse("aborted, no changes made")
		}
	}

	opts := session.CleanOptions{}
	if req.DeleteBranch {
		merged := session.Merged(cd.Run, repo, target.Branch, target.Status.BaseBranch)
		if deleteBranchTTYRefused(merged, deps.TTY) {
			return refuse("branch %q has not been merged — deleting it needs interactive confirmation, and stdin is not a TTY (omit --delete-branch, or run this from a terminal)",
				target.Branch)
		}
		if shouldConfirmUnmergedBranchDelete(merged, deps.TTY) {
			title := fmt.Sprintf(
				"Branch %q (session %q) has not been merged.\nDelete it anyway? Once deleted it is reflog-recoverable by SHA only.",
				target.Branch, name)
			if !deps.ask(title, "Delete branch", "Keep branch") {
				return refuse("aborted, no changes made")
			}
		}
		opts.DeleteBranch = true
		opts.ConfirmedUnmerged = !merged
	}

	result, err := deps.CleanSession(repo, *target, cd, opts)
	if err != nil {
		return done(cleanSurfaceResult{Outcome: cleanFailed, Result: result, Code: 1, ToStderr: true, Text: err.Error()})
	}

	var lines []string
	if result.SnapshotCommitted {
		lines = append(lines, fmt.Sprintf("Snapshot-committed dirty worktree for %q", result.Name))
	}
	lines = append(lines, strings.Split(strings.TrimSuffix(cleanSummary(target.Scratch, result), "\n"), "\n")...)
	// A stranded entry and a worktree fault (or, in principle, two stranded
	// entries in different carriers) can share a name; findCleanTarget picked
	// exactly one of them, so tell the caller the other is still there rather
	// than let it silently vanish from view.
	for _, other := range sameNameRemaining(sessions, *target) {
		lines = append(lines, fmt.Sprintf("Note: another entry named %q remains (%s at %s); run afk clean %q again to clean it.",
			name, other.Kind, other.WorktreePath, name))
	}
	return done(cleanSurfaceResult{Outcome: cleanCleaned, Result: result, Lines: lines})
}

// ask puts one yes/no question to the owner. With no terminal, or no way to
// ask, the answer is no: a question nobody can answer must never default to
// the destructive side. The command only asks when TTY is true, so the "no
// terminal" half is a guard, not a path it takes.
func (d cleanSurfaceDeps) ask(title, yes, no string) bool {
	if !d.TTY || d.Confirm == nil {
		return false
	}
	ok, err := d.Confirm(title, yes, no)
	return err == nil && ok
}

// phaseWords quotes a phase for the changed-since-shown sentence, and names
// the empty phase (a stale status file, or a one-off session) "unknown"
// rather than printing "" at the owner.
func phaseWords(phase string) string {
	if phase == "" {
		return "unknown"
	}
	return fmt.Sprintf("%q", phase)
}
