// buildpause.go is the dashboard's `P` and `U` keys: pause and resume the
// active project's build, the way `afk pause` and `afk resume` do.
//
// Nothing here writes a file. The two keys call the Deps.PauseBuild and
// Deps.ResumeBuild seams, which `afk dash` wires to the same functions the
// commands run, so the key and the command cannot drift apart: one pause
// flag, one stop marker, one line in the decision log, whichever way the
// owner asked. This package cannot import the command package, which is why
// the seams exist and why ResumeResult is declared here and filled in there.
//
// The seam is called from inside a tea.Cmd and never from Update or View,
// on the discipline answerInboxCmd documents: a local file write is still
// I/O, and it must not be able to stall the screen.
//
// Pausing does not touch a session. A task that is already running carries
// on to its end; the build loop just starts nothing new while the flag is
// there. The notices say so, because "paused" on its own reads as "stopped
// now".
package ui

import tea "charm.land/bubbletea/v2"

// ResumeResult is what a resume found and undid. It mirrors what `afk resume`
// reports, declared here because the function that produces it lives in a
// package this one cannot import.
//
// A build can be held in two separate ways, and a resume clears both: the
// owner's pause, and a stop the build loop set itself after a task it could
// not get past. Both false means there was nothing to resume.
type ResumeResult struct {
	// WasPaused is true when the owner's pause was on and is now off.
	WasPaused bool
	// WasStopped is true when the build had stopped itself and that stop is
	// now cleared.
	WasStopped bool
	// StopTask is the task the build stopped after, when the stop named one.
	StopTask string
}

// The two notices for a project with nothing to pause or resume. They follow
// noBuildNotice (buildtab.go) word for word apart from the verb, so `b`, `P`
// and `U` say the same thing about the same state and name the same setting.
const (
	noBuildToPauseNotice  = "This project has no master plan, so there is no build to pause. Set plan in afk.toml."
	noBuildToResumeNotice = "This project has no master plan, so there is no build to resume. Set plan in afk.toml."
)

// buildPauseUnavailableNotice is what either key says when its seam is not
// wired. `afk dash` always wires both, so the owner should never read this;
// it exists so that a dashboard built without them answers the key with a
// sentence and not a nil-function panic.
const buildPauseUnavailableNotice = "Pause and resume are not available in this dashboard. Use afk pause and afk resume."

// pauseNotice words a pause that worked. already is true when the build was
// paused before the key was pressed, which is not a failure and changed
// nothing, so it is told as a fact and not as a success.
func pauseNotice(already bool) (notificationKind, string) {
	if already {
		return notifyInfo, "The build was already paused. Press U to resume."
	}
	return notifySuccess, "Build paused. A task that is running finishes; nothing new starts. Press U to resume."
}

// resumeNotice words a resume that worked, one sentence per combination of
// the two things a resume can clear.
//
// The three "cleared" outcomes say the daemon carries on at its next pass
// and not "now": the dashboard starts nothing itself, the build loop reads
// the files on its next pass. Neither set is told as a fact, like an
// already-paused pause.
func resumeNotice(r ResumeResult) (notificationKind, string) {
	stop := "the stop"
	if r.StopTask != "" {
		stop = "the stop after " + r.StopTask
	}
	switch {
	case r.WasPaused && r.WasStopped:
		return notifySuccess, "Build resumed, and " + stop + " is cleared. The daemon carries on at its next pass."
	case r.WasStopped:
		return notifySuccess, "Cleared " + stop + ". The daemon carries on at its next pass."
	case r.WasPaused:
		return notifySuccess, "Build resumed. The daemon may start new tasks again."
	default:
		return notifyInfo, "The build was not paused; nothing changed."
	}
}

// pauseFailedNotice and resumeFailedNotice word a seam error. The reason is
// the seam's own text: only the caller that wired it can see which step
// failed (the flag, or only the decision log), so it words that part.
func pauseFailedNotice(err error) string {
	return "Pausing the build did not work: " + err.Error()
}

func resumeFailedNotice(err error) string {
	return "Resuming the build did not work: " + err.Error()
}

// buildPausedMsg and buildResumedMsg carry one key's outcome back to Update.
// gen is the project generation the key was pressed under, so an outcome
// that crosses a project switch is dropped and never told under another
// project's name (see Model.projectGen).
type buildPausedMsg struct {
	already bool
	err     error
	gen     int
}

type buildResumedMsg struct {
	res ResumeResult
	err error
	gen int
}

// pauseBuild is `P`.
//
// The no-build check comes first and is the same one `b` makes (hasBuild and
// hasActiveProject), so a project with no master plan, and a project whose
// build data has not arrived yet, get a sentence and no write. That check is
// also what guarantees m.build.Root below is a real folder: it is only read
// once a snapshot for this project has landed.
//
// The root is the snapshot's, the project's main checkout, and not m.repo:
// the dashboard may have been started inside a session's worktree, and the
// pause flag lives in the main checkout (answerInboxCmd makes the same
// choice for the same reason).
//
// A second press before the first has reported back calls the seam again.
// That is left alone on purpose: pausing a paused build is harmless, it is
// what running `afk pause` twice does, and a guard would need releasing on
// the dropped-message path too.
func (m Model) pauseBuild() (tea.Model, tea.Cmd) {
	if !m.hasBuild() || !m.hasActiveProject() {
		m.notify(notifyInfo, noBuildToPauseNotice)
		return m, nil
	}
	pause := m.deps.PauseBuild
	if pause == nil {
		m.notify(notifyWarn, buildPauseUnavailableNotice)
		return m, nil
	}
	root := m.build.Root
	gen := m.projectGen
	return m, func() tea.Msg {
		already, err := pause(root)
		return buildPausedMsg{already: already, err: err, gen: gen}
	}
}

// resumeBuild is `U`, on exactly pauseBuild's terms.
func (m Model) resumeBuild() (tea.Model, tea.Cmd) {
	if !m.hasBuild() || !m.hasActiveProject() {
		m.notify(notifyInfo, noBuildToResumeNotice)
		return m, nil
	}
	resume := m.deps.ResumeBuild
	if resume == nil {
		m.notify(notifyWarn, buildPauseUnavailableNotice)
		return m, nil
	}
	root := m.build.Root
	gen := m.projectGen
	return m, func() tea.Msg {
		res, err := resume(root)
		return buildResumedMsg{res: res, err: err, gen: gen}
	}
}

// applyBuildPaused folds a pause's outcome into the Model.
//
// The Build tab is read again in every case that is not dropped, a failure
// included. A failure is not proof that nothing changed: the flag can be
// written and the decision-log line after it refused, and then the build IS
// paused. Reading the files again is the only way the summary line is right
// either way, and it saves the owner waiting for the two-second tick to see
// what their key did.
func (m Model) applyBuildPaused(msg buildPausedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen {
		return m, nil
	}
	if msg.err != nil {
		m.notify(notifyError, pauseFailedNotice(msg.err))
		return m, m.buildCmd()
	}
	kind, text := pauseNotice(msg.already)
	m.notify(kind, text)
	return m, m.buildCmd()
}

// applyBuildResumed folds a resume's outcome into the Model, on
// applyBuildPaused's terms.
func (m Model) applyBuildResumed(msg buildResumedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.projectGen {
		return m, nil
	}
	if msg.err != nil {
		m.notify(notifyError, resumeFailedNotice(msg.err))
		return m, m.buildCmd()
	}
	kind, text := resumeNotice(msg.res)
	m.notify(kind, text)
	return m, m.buildCmd()
}
