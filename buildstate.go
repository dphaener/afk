package main

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/projectconfig"
)

// buildState is everything the build commands (afk tasks, afk next, afk
// ledger set) read before they decide anything: where the project is, the
// parsed and validated master plan, and the ledger of recorded task statuses.
// Loading it in one place keeps the three commands from drifting apart on
// which file is the plan or what a damaged ledger tells the person.
type buildState struct {
	Root     string
	PlanPath string
	Tasks    []masterplan.Task
	Ledger   ledger.Ledger
}

// resolveBuildRoot turns the --project flag value and the working directory
// into the project root the build commands operate on: the flag or cwd via
// resolveProject, then the main checkout via mainCheckout (maincheckout.go).
//
// The second step matters because a session runs in a linked worktree with its
// own gitignored .afk/. The build ledger lives in the main checkout
// (docs/PLAN.md section 4), so a command run from inside a session must be
// pointed back at it or it would read and write a ledger nobody else sees.
//
// gitCommonDir is the impure git lookup, injected so the matrix is testable
// without running git.
func resolveBuildRoot(flagValue, cwd, home string, gitCommonDir func(string) string) (string, error) {
	dir, err := resolveProject(flagValue, cwd, home)
	if err != nil {
		return "", err
	}
	return mainCheckout(dir, gitCommonDir(dir)), nil
}

// loadBuildState loads afk.toml, the master plan it names, and the ledger for
// the project at root. Every error says what happened and what to do next,
// and wraps its cause so callers can still match it with errors.Is.
//
// The plan is validated here (unknown dependencies, cycles) so a command never
// computes a ready list from a plan that cannot be ordered. A missing ledger is
// not an error: it is a project where nothing has been recorded yet.
func loadBuildState(root string) (buildState, error) {
	cfg, err := projectconfig.Load(root)
	if err != nil {
		return buildState{}, err // already names afk.toml and what to do
	}
	planPath := cfg.PlanPath(root)

	tasks, err := masterplan.ParseFile(planPath)
	if errors.Is(err, fs.ErrNotExist) {
		return buildState{}, fmt.Errorf("no master plan at %s; set `plan` in afk.toml or create the file: %w", planPath, err)
	}
	if err != nil {
		return buildState{}, fmt.Errorf("the master plan %s has a problem: %w. Fix the plan and try again", planPath, err)
	}
	if err := masterplan.Validate(tasks); err != nil {
		return buildState{}, fmt.Errorf("the master plan %s has a problem: %w. Fix the plan and try again", planPath, err)
	}

	l, err := ledger.Load(root)
	if errors.Is(err, ledger.ErrCorrupt) {
		return buildState{}, fmt.Errorf("%w. Fix or delete %s (deleting it forgets every recorded task)", err, ledger.Path(root))
	}
	if err != nil {
		return buildState{}, fmt.Errorf("cannot read the task ledger %s: %w. Check the file's permissions", ledger.Path(root), err)
	}

	return buildState{Root: root, PlanPath: planPath, Tasks: tasks, Ledger: l}, nil
}
