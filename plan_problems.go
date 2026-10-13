package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/projectconfig"
)

// maxPlanFindings is how many errors the daemon's bad-plan item lists before
// it says how many more there are.
const maxPlanFindings = 20

// withPlanFindings makes the error the daemon raises for an unreadable master
// plan carry everything `afk plan lint` found, not only the first problem the
// parser stopped at.
//
// It lives at the daemon's seam and not in loadBuildState because the two
// readers differ. `afk tasks` and `afk next` print one problem on a terminal
// and point at the fix; the person re-runs and sees the next one. The
// daemon's inbox item is read later, away from the terminal, so it has to
// carry the whole list or the owner fixes one line per round trip.
//
// The list stops at 20 errors because the body is also sent as a
// notification, and a phone does not need 200 lines. The last line says how
// many were left and which command shows them all.
//
// The text must be identical for the same plan on every call: the daemon
// skips a repeat item only when the title and the exact body match, so a body
// that changed between passes (a time, a map's iteration order) would raise a
// new item on every pass. The checker's findings are sorted and this helper
// adds nothing else.
//
// err comes back unchanged when it is nil, when it is not a master-plan
// problem (a ledger or config error has nothing to add), when the plan cannot
// be read now, or when the checker finds no error (warnings never reach the
// inbox).
func withPlanFindings(err error, planPath string, readFile func(string) ([]byte, error)) error {
	if err == nil {
		return nil
	}
	if !isPlanProblem(err) {
		return err
	}
	data, rerr := readFile(planPath)
	if rerr != nil {
		return err
	}
	var errs []planlint.Finding
	for _, f := range planlint.Lint(data).Findings {
		if f.Level == planlint.LevelError {
			errs = append(errs, f)
		}
	}
	if len(errs) == 0 {
		return err
	}
	var b strings.Builder
	b.WriteString(err.Error())
	b.WriteString("\n\nEvery problem `afk plan lint` found in the plan:")
	for i, f := range errs {
		if i == maxPlanFindings {
			fmt.Fprintf(&b, "\n- and %d more. Run `afk plan lint` to see them all.", len(errs)-maxPlanFindings)
			break
		}
		if f.Line > 0 {
			fmt.Fprintf(&b, "\n- line %d: %s", f.Line, f.Message)
		} else {
			fmt.Fprintf(&b, "\n- %s", f.Message)
		}
	}
	return &planFindingsError{msg: b.String(), err: err}
}

// planFindingsError carries the longer text while still unwrapping to the
// original error, so errors.Is matches the sentinel.
type planFindingsError struct {
	msg string
	err error
}

func (e *planFindingsError) Error() string { return e.msg }
func (e *planFindingsError) Unwrap() error { return e.err }

func isPlanProblem(err error) bool {
	for _, s := range []error{
		masterplan.ErrNoTasks, masterplan.ErrBadHeading, masterplan.ErrDuplicateTask,
		masterplan.ErrNoDependsLine, masterplan.ErrUnknownDependency, masterplan.ErrCycle,
	} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// daemonLoadPlanError is the error half of the daemon's LoadBuild: it works
// out the plan path only now that there is an error (loadBuildState returns an
// empty buildState on failure) and skips the list if afk.toml cannot be read.
func daemonLoadPlanError(root string, err error, readFile func(string) ([]byte, error)) error {
	if err == nil {
		return nil
	}
	cfg, cerr := projectconfig.Load(root)
	if cerr != nil {
		return err
	}
	return withPlanFindings(err, cfg.PlanPath(root), readFile)
}
