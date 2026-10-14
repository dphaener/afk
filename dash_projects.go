package main

import (
	"os"
	"path/filepath"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/ui"
)

// This file is the real version of ui.Deps.LoadListed: the projects in the
// machine's config.toml, the same list `afk project list` prints and the
// daemon builds. It lives here and not in internal/ui because the main
// checkout lookup is in this package, and because internal/ui reads no file
// itself (the dashboard calls this from inside a command, off its Update
// goroutine).
//
// It only reads config.toml (through the daemon's own loader, so the dashboard
// and the daemon agree on whether the file is good) and runs at most one
// bounded `git rev-parse` per listed folder that exists. It never writes a
// file, dials a host or touches the network.

// dashProjectParts are the lookups loadDashListed does outside config.toml,
// injected so tests run against temp folders with no git.
type dashProjectParts struct {
	cfg daemonconfig.Deps
	// isDir reports whether path is a folder that exists.
	isDir func(path string) bool
	// gitCommonDir is the git common dir as seen from dir, "" when dir is not
	// in a repository. The real one is bounded by gitCommonDirTimeout.
	gitCommonDir func(dir string) string
	evalSymlinks func(path string) (string, error)
}

func realDashProjectParts() dashProjectParts {
	return dashProjectParts{
		cfg: daemonconfig.DefaultDeps(),
		isDir: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		},
		gitCommonDir: resolveGitCommonDirIn,
		evalSymlinks: filepath.EvalSymlinks,
	}
}

// dashLoadListed is ui.Deps.LoadListed for the real dashboard.
func dashLoadListed() ([]ui.ListedProject, string, error) {
	return loadDashListed(realDashProjectParts())
}

// loadDashListed reads config.toml and turns each [[project]] into the row
// the switcher shows.
//
// The path is returned on an error too, so the notice can name the file. A
// missing file is no error: the daemon's loader yields no projects for it.
// On an error no entries come back; the dashboard keeps the rows it had.
//
// A listed path may be a linked worktree (the daemon accepts one), so it goes
// through mainCheckout, and then EvalSymlinks so it is spelled the way
// internal/scan and resolveCwdRepoRoot spell theirs: the merge in internal/ui
// compares paths as plain strings, and a different spelling would show one
// project as two rows. A folder that is gone gets no git call (git in a
// missing folder only fails, after a process start) and no resolution, so its
// path stays as listed.
func loadDashListed(p dashProjectParts) ([]ui.ListedProject, string, error) {
	path := daemonconfig.Path(p.cfg)
	cfg, err := daemonconfig.Load(p.cfg)
	if err != nil {
		return nil, path, err
	}
	var out []ui.ListedProject
	seen := map[string]bool{}
	for _, lp := range cfg.Projects {
		var entry ui.ListedProject
		if !p.isDir(lp.Path) {
			entry = ui.ListedProject{Path: filepath.Clean(lp.Path), Name: lp.Name, Gone: true}
		} else {
			root := mainCheckout(lp.Path, p.gitCommonDir(lp.Path))
			if resolved, err := p.evalSymlinks(root); err == nil {
				root = resolved
			}
			entry = ui.ListedProject{Path: root, Name: lp.Name}
		}
		// Two entries on one checkout are one project; the first wins, as the
		// daemon keeps the first (daemonProjects).
		if seen[entry.Path] {
			continue
		}
		seen[entry.Path] = true
		out = append(out, entry)
	}
	return out, path, nil
}

// launchMainCheckout is the main checkout of the start-up project (the
// pinned row), "" when there is none. It differs from the row's path only
// when the dashboard was started inside a linked worktree, which is the case
// the switcher needs it for. The git call is the same bounded one the
// start-up lookup already makes.
func launchMainCheckout(projects []project.Project, gitCommonDir func(string) string, evalSymlinks func(string) (string, error)) string {
	for _, pr := range projects {
		if !pr.Pinned {
			continue
		}
		root := mainCheckout(pr.Path, gitCommonDir(pr.Path))
		if resolved, err := evalSymlinks(root); err == nil {
			root = resolved
		}
		return root
	}
	return ""
}
