package main

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/dphaener/afk/internal/web"
)

// This file is the real version of web.Deps.AddProject and
// web.Deps.RemoveProject. Like dash_project.go, each is a thin adapter: turn
// what the page sent into the argument the shared entry point wants
// (project_surface.go), call it, and map the outcome onto the web constants.
// The page says exactly what `afk project add` and `afk project remove` say
// because Text, Name and Root pass through untouched. Neither adapter writes
// config.toml itself, so the owner's comments and the [dash] table survive.

// webProjectMu is held for the whole of one add or remove. The daemon's web
// server handles requests concurrently, and both entry points read
// config.toml, change the text and write it back, so two page posts that
// overlapped could each read the old file and the later write would silently
// undo the earlier one.
var webProjectMu sync.Mutex

// webAddProject adds the folder the owner typed on the page.
func webAddProject(folder, name string) web.ProjectEditResult {
	return webAddProjectWith(realProjectEnv(), folder, name)
}

func webAddProjectWith(env projectEnv, folder, name string) web.ProjectEditResult {
	webProjectMu.Lock()
	defer webProjectMu.Unlock()

	folder = strings.TrimSpace(folder)
	if folder == "" {
		return web.ProjectEditResult{Outcome: web.ProjectRefused,
			Text: "Give the folder to add."}
	}
	dir := folder
	if folder == "~" || strings.HasPrefix(folder, "~/") {
		home, err := env.HomeDir()
		if err != nil || home == "" {
			return web.ProjectEditResult{Outcome: web.ProjectRefused,
				Text: "afk could not find your home folder, so it cannot read a path that starts with ~. Give the full path instead."}
		}
		dir = filepath.Join(home, strings.TrimPrefix(folder, "~"))
	}
	// The daemon's own working folder means nothing to the person at the
	// page, so a relative path is refused rather than guessed at.
	if !filepath.IsAbs(dir) {
		return web.ProjectEditResult{Outcome: web.ProjectRefused,
			Text: "Give the full path to the folder, or start it with ~ for your home folder."}
	}
	return webProjectResult(addProjectForSurface(env, filepath.Clean(dir), strings.TrimSpace(name)))
}

// webRemoveProject takes the listed project called name off the list.
func webRemoveProject(name string) web.ProjectEditResult {
	return webRemoveProjectWith(realProjectEnv(), name)
}

// webRemoveProjectWith passes the listed name straight to the entry point:
// daemon.Project.Name is the [[project]] name from config.toml
// (daemonProjects copies it), so no match by folder is needed. Root is
// whatever the entry point reports, which for a remove is nothing; the page
// does not need it.
func webRemoveProjectWith(env projectEnv, name string) web.ProjectEditResult {
	webProjectMu.Lock()
	defer webProjectMu.Unlock()
	return webProjectResult(removeProjectForSurface(env, name))
}

// webProjectResult maps the shared outcome onto the page's strings. Anything
// unknown becomes failed, never a success.
func webProjectResult(r projectEditResult) web.ProjectEditResult {
	out := web.ProjectFailed
	switch r.Outcome {
	case projectAdded:
		out = web.ProjectAdded
	case projectAlreadyListed:
		out = web.ProjectAlreadyListed
	case projectRemoved:
		out = web.ProjectRemoved
	case projectNotListed:
		out = web.ProjectNotListed
	case projectRefused:
		out = web.ProjectRefused
	}
	return web.ProjectEditResult{Outcome: out, Name: r.Name, Root: r.Root, Text: r.Text}
}
