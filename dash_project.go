package main

import (
	"fmt"
	"path/filepath"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/ui"
)

// This file is the real version of ui.Deps.AddProject and ui.Deps.RemoveProject.
// They live here and not in internal/ui because the command's checks and the
// config.toml edit are in this package (project_surface.go), and the dashboard
// must say exactly what `afk project add` and `afk project remove` say. Each
// one is a thin adapter: map the dashboard's argument onto the shared entry
// point, and map the outcome back onto the ui.Project* constants. Neither
// writes config.toml itself, so the comments and the [dash] table survive.

// dashAddProject adds the folder dir (absolute, already cleaned by the
// dashboard) to config.toml. name "" means the folder's own name.
func dashAddProject(dir, name string) ui.ProjectEditResult {
	return dashAddProjectWith(realProjectEnv(), dir, name)
}

func dashAddProjectWith(env projectEnv, dir, name string) ui.ProjectEditResult {
	return dashProjectResult(addProjectForSurface(env, dir, name))
}

// dashRemoveProject takes the project whose row root is root off the list.
func dashRemoveProject(root string) ui.ProjectEditResult {
	return dashRemoveProjectWith(realProjectEnv(), filepath.EvalSymlinks, root)
}

// dashRemoveProjectWith finds the [[project]] entry the row stands for by the
// same reckoning loadDashAllProjects uses to build rows (main checkout, then
// symlinks resolved, and a folder that is gone matched by its cleaned path),
// because the row's root is that result and not the text in the file. It then
// removes by the entry's name, never by path, so a row can only remove the
// entry it was built from.
func dashRemoveProjectWith(env projectEnv, evalSymlinks func(string) (string, error), root string) ui.ProjectEditResult {
	path := daemonconfig.Path(env.Deps)
	cfg, err := daemonconfig.Load(env.Deps)
	if err != nil {
		return ui.ProjectEditResult{Outcome: ui.ProjectFailed,
			Text: fmt.Sprintf("Could not read %s: %v", path, err)}
	}
	want := filepath.Clean(root)
	for _, lp := range cfg.Projects {
		got := filepath.Clean(lp.Path)
		if env.isDir(lp.Path) {
			got = mainCheckout(lp.Path, env.gitCommonDir(lp.Path))
			if resolved, rerr := evalSymlinks(got); rerr == nil {
				got = resolved
			}
		}
		if got == want {
			return dashProjectResult(removeProjectForSurface(env, lp.Name))
		}
	}
	return ui.ProjectEditResult{Outcome: ui.ProjectNotListed,
		Text: fmt.Sprintf("%s is not in config.toml, so there is nothing to remove.", root)}
}

// dashProjectResult maps the shared outcome onto the dashboard's strings. The
// Text is passed through untouched: it is the command's own sentence.
func dashProjectResult(r projectEditResult) ui.ProjectEditResult {
	out := ui.ProjectFailed
	switch r.Outcome {
	case projectAdded:
		out = ui.ProjectAdded
	case projectAlreadyListed:
		out = ui.ProjectAlreadyListed
	case projectRemoved:
		out = ui.ProjectRemoved
	case projectNotListed:
		out = ui.ProjectNotListed
	case projectRefused:
		out = ui.ProjectRefused
	}
	return ui.ProjectEditResult{Outcome: out, Text: r.Text}
}
