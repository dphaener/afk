package main

import (
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/web"
)

// This file joins the page's four session actions (internal/web's
// Deps.CleanSession, RestartSession, SetSessionLabel and NewSession) to the
// entry points the commands and the dashboard share. It decides nothing about
// a session: each seam calls one entry point and words its outcome for the
// page.

// webCleanSession is the real Deps.CleanSession.
//
// It never forces and never deletes the branch (plan assumptions A1 and A2):
// the dashboard forces after a confirm, but from a phone the destructive path
// is deleting a live session's worktree, so the page refuses what a plain
// `afk clean` refuses and points at the two places that may force. The phase
// the page showed is passed along, so a session that moved on while the
// confirm was open is refused (A5).
func webCleanSession(root, name, phaseSeen string) web.SessionActionResult {
	return webCleanOutcome(cleanSessionForSurface(root, name,
		cleanSurfaceRequest{CheckPhase: true, PhaseSeen: phaseSeen}, defaultCleanSurfaceDeps()), name)
}

// forceHint is the tail of the phase gate's sentence. It names the command's
// own flag, which means nothing on a web page, so webCleanOutcome swaps it for
// the places a forced clean can be made.
const forceHint = " — pass --force to clean it anyway"

// webCleanOutcome maps a clean's result to the page's. Pure.
func webCleanOutcome(res cleanSurfaceResult, name string) web.SessionActionResult {
	switch res.Outcome {
	case cleanCleaned:
		return web.SessionActionResult{Outcome: web.SessionActionDone, Text: webCleanedText(res.Result)}
	case cleanRefusedOutcome, cleanNotFound:
		text := res.Text
		if strings.HasSuffix(text, forceHint) {
			text = strings.TrimSuffix(text, forceHint) +
				". This page does not force a clean. Run `afk clean --force " + name + "` or use `afk dash` if you want it cleaned anyway."
		}
		return web.SessionActionResult{Outcome: web.SessionActionRefused, Text: text}
	default:
		text := res.Text
		if e := strings.TrimSpace(res.EchoStderr); e != "" {
			text += " (" + strings.ReplaceAll(e, "\n", " · ") + ")"
		}
		return web.SessionActionResult{Outcome: web.SessionActionFailed, Text: text}
	}
}

// webCleanedText words a finished clean with the dashboard's success words
// (internal/ui's cleanSucceededStatus): which of the archive, the plain
// removal and the worktree-only removal happened, and that the branch was
// kept. The page never deletes a branch, so there is no recovery clause.
func webCleanedText(r session.CleanResult) string {
	var text string
	switch {
	case r.ArchiveSkipped:
		text = "Cleaned " + r.Name + "; worktree removed, nothing to archive."
	case r.ArchivePath == "":
		text = "Cleaned " + r.Name + "; session directory removed."
	default:
		text = "Cleaned " + r.Name + "; archived as " + filepath.Base(r.ArchivePath) + "."
	}
	if r.SnapshotCommitted {
		text += " Dirty changes were snapshot-committed."
	}
	if r.Branch != "" {
		text += " Branch " + r.Branch + " kept."
	}
	return text
}

// webRestartSession is the real Deps.RestartSession.
func webRestartSession(root, name string) web.SessionActionResult {
	return webActionResult(restartSessionForSurface(root, name, defaultRestartSurfaceDeps()))
}

// webSetSessionLabel is the real Deps.SetSessionLabel.
func webSetSessionLabel(root, name, label string) web.SessionActionResult {
	return webActionResult(setLabelForSurface(root, name, label, defaultLabelSurfaceDeps()))
}

// webNewSession is the real Deps.NewSession. It starts the session as
// `afk new --autonomous` does and no more (A3).
func webNewSession(root, name, description string) web.SessionActionResult {
	return webActionResult(newAutonomousForSurface(root, name, description, defaultNewSurfaceDeps()))
}

// webActionResult copies the root's result to the page's. The outcome strings
// are the same by design (session_surface.go), but an outcome the page does
// not know is turned into a failure here rather than passed on.
func webActionResult(r sessionActionResult) web.SessionActionResult {
	switch r.Outcome {
	case web.SessionActionDone, web.SessionActionRefused, web.SessionActionFailed:
		return web.SessionActionResult{Outcome: r.Outcome, Text: r.Text}
	}
	return web.SessionActionResult{Outcome: web.SessionActionFailed,
		Text: "The action gave an answer afk does not know. Open afk dash to see the session as it is now."}
}
