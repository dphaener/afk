package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/dphaener/afk/internal/daemonconfig"
)

// projectOutcome is what an add or a remove of a project ended as. The
// dashboard and the command both read it, so neither has to parse Text.
type projectOutcome int

const (
	projectAdded projectOutcome = iota
	projectAlreadyListed
	projectRemoved
	projectNotListed
	// projectRefused: the request cannot be done as asked (not a folder, not a
	// repository, a name clash, a mistake in the file).
	projectRefused
	// projectFailed: the config file could not be found, read, created or
	// written. Text names the file.
	projectFailed
)

// projectEditResult is the outcome of one add or remove. Text is the plain
// sentence(s) the command prints, without the "afk project add: " prefix, so
// the dashboard can show it as is. Code is the command's exit code.
type projectEditResult struct {
	Outcome  projectOutcome
	Name     string
	Root     string
	Text     string
	Code     int
	ToStderr bool
}

// addProjectForSurface is `afk project add` after the folder is resolved: dir
// is absolute. It is shared by the command and the dashboard so both make the
// same checks and the same edit.
func addProjectForSurface(env projectEnv, dir, name string) projectEditResult {
	if !env.isDir(dir) {
		return projectEditResult{Outcome: projectRefused, Code: 1, ToStderr: true,
			Text: fmt.Sprintf("%s is not a folder. Check the path.", dir)}
	}
	common := env.gitCommonDir(dir)
	if common == "" {
		return projectEditResult{Outcome: projectRefused, Code: 1, ToStderr: true,
			Text: fmt.Sprintf("%s is not a git repository, so the daemon could not build in it. Run `git init` there first, or give another folder.", dir)}
	}
	root := mainCheckout(dir, common)

	added, listed, err := addProjectToConfig(env, root, name)
	if err != nil {
		outcome := projectRefused
		var ioErr configIOError
		if errors.As(err, &ioErr) {
			outcome = projectFailed
		}
		return projectEditResult{Outcome: outcome, Root: root, Code: 1, ToStderr: true, Text: err.Error()}
	}
	if !added {
		return projectEditResult{Outcome: projectAlreadyListed, Name: listed, Root: root,
			Text: fmt.Sprintf("%s is already in the list. Nothing was changed.", listed)}
	}
	return projectEditResult{Outcome: projectAdded, Name: listed, Root: root,
		Text: fmt.Sprintf("Added %s (%s). A running afk daemon picks it up within a few seconds.", listed, root)}
}

// removeProjectForSurface is `afk project remove` after its arguments are
// parsed. key is a listed name or a folder.
func removeProjectForSurface(env projectEnv, key string) projectEditResult {
	path := daemonconfig.Path(env.Deps)
	if path == "" {
		return projectEditResult{Outcome: projectFailed, Code: 1, ToStderr: true,
			Text: "cannot find afk's config folder: set XDG_CONFIG_HOME to an absolute path or make sure your home directory is known."}
	}
	text, err := readConfigText(env, path)
	if err != nil {
		return projectEditResult{Outcome: projectFailed, Code: 1, ToStderr: true, Text: err.Error()}
	}

	// A name or the path as written first; then, for a folder given another
	// way (relative, ~, or a linked worktree), the main checkout it resolves to.
	keys := []string{key}
	if cwd, cerr := env.cwd(); cerr == nil {
		home, _ := env.HomeDir()
		if dir, rerr := resolveProject(key, cwd, home); rerr == nil && env.isDir(dir) {
			if root := mainCheckout(dir, env.gitCommonDir(dir)); root != key {
				keys = append(keys, root)
			}
		}
	}
	var newText string
	removed := false
	for _, k := range keys {
		newText, removed, err = daemonconfig.RemoveProject(text, k)
		if err != nil {
			return projectEditResult{Outcome: projectRefused, Name: key, Code: 1, ToStderr: true, Text: err.Error()}
		}
		if removed {
			break
		}
	}
	if !removed {
		return projectEditResult{Outcome: projectNotListed, Name: key, Code: 1, ToStderr: true,
			Text: fmt.Sprintf("No project named %s. Run `%s` to see what is listed.", key, projectListUsage)}
	}
	if err := env.write(path, []byte(newText)); err != nil {
		return projectEditResult{Outcome: projectFailed, Name: key, Code: 1, ToStderr: true,
			Text: fmt.Sprintf("could not write %s: %v\nThe file was not changed.", path, err)}
	}
	return projectEditResult{Outcome: projectRemoved, Name: key,
		Text: fmt.Sprintf("Removed %s from the list. Its files and any session that is running were left alone.", key)}
}

// printProjectResult prints a result as the command always has: Text to
// stdout or stderr, with the command's prefix on every outcome except "not
// listed", which has never had one.
func printProjectResult(stdout, stderr io.Writer, prefix string, res projectEditResult) {
	w := stdout
	if res.ToStderr {
		w = stderr
	}
	if res.Outcome != projectNotListed && res.ToStderr {
		fmt.Fprint(w, prefix)
	}
	fmt.Fprintln(w, res.Text)
}
