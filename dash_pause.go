package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/ui"
)

// dashPauseBuild is the dashboard's P key. It calls pauseProject, the same
// function `afk pause` and the web page's button call, so the key sets the
// same flag and writes the same decision-log line (an already-paused project
// still gets its line, as with the command). internal/ui cannot import this
// package, so the dashboard only sees a function value and a bool.
//
// The error text is webPauseError's: one set of words for the page and the
// dashboard, with the same per-stage meaning (flag not written: the reason and
// the folder to check; log stage: the change WAS made). already is returned
// with a log-stage error too, because the pause happened.
func dashPauseBuild(root string) (bool, error) {
	return dashPauseAt(root, time.Now())
}

// dashPauseAt is dashPauseBuild with the clock passed in, so a test can pin
// the time written to the flag.
func dashPauseAt(root string, now time.Time) (bool, error) {
	already, err := pauseProject(root, now)
	return already, dashPauseError(root, err)
}

// dashResumeBuild is the dashboard's U key. It calls resumeProject, the same
// function `afk resume` and the web page's button call, so it clears the same
// flag and marker and logs the same lines. The outcome is mapped to ui's own
// type because internal/ui cannot import this package. The outcome is
// returned even with a log-stage error: the flag or marker is already gone.
func dashResumeBuild(root string) (ui.ResumeResult, error) {
	return dashResumeAt(root, time.Now())
}

// dashResumeAt is dashResumeBuild with the clock passed in.
func dashResumeAt(root string, now time.Time) (ui.ResumeResult, error) {
	out, err := resumeProject(root, now)
	return ui.ResumeResult{
		WasPaused:  out.WasPaused,
		WasStopped: out.WasStopped,
		StopTask:   out.StopTask,
	}, dashPauseError(root, err)
}

// dashPauseError is webPauseError with the reason put first for a write that
// failed. The dashboard shows the notice in a narrow box that cuts the text
// off, and a path error leads with the temp file's long path, so the reason
// ("permission denied") never showed. For the flag and marker stages the
// operating-system reason is lifted out of the path error and leads, followed
// by the folder to check. Other stages keep webPauseError's words, which
// already start with the part that matters.
func dashPauseError(root string, err error) error {
	var pe *pauseError
	if !errors.As(err, &pe) {
		return webPauseError(root, err)
	}
	var dir string
	switch pe.stage {
	case stageStopMarker:
		dir = filepath.Dir(daemonstate.StopPath(root))
	case stagePauseFlag:
		dir = filepath.Dir(daemonstate.PausePath(root))
	default:
		return webPauseError(root, err)
	}
	reason := pe.err
	var pathErr *fs.PathError
	if errors.As(pe.err, &pathErr) {
		reason = pathErr.Err
	}
	return fmt.Errorf("%w. Check that afk can write to %s", reason, dir)
}
