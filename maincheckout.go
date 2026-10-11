package main

import "path/filepath"

// mainCheckout answers "which folder holds the project's .afk/ state".
// Daemon state lives in the main checkout (docs/PLAN.md §4) while agents run
// in linked worktrees, so a command an agent types must not write into the
// worktree's own .afk/. commonDir is the absolute output of
// `git rev-parse --path-format=absolute --git-common-dir` run in dir, or ""
// when dir is not in a git repo. Every worktree of a repo shares one common
// dir, so its parent is the main checkout, and from a subfolder it is still
// the repo's top folder. A bare repository's common dir does not end in
// ".git", so it falls back to dir like the not-a-repo case.
//
// Pure: no git, no filesystem. The caller does the impure lookup.
func mainCheckout(dir, commonDir string) string {
	if commonDir != "" {
		c := filepath.Clean(commonDir)
		if filepath.Base(c) == ".git" {
			return filepath.Dir(c)
		}
	}
	return filepath.Clean(dir)
}

// inferSession takes the paths matched by {worktree top}/.afk/*/status.json
// and returns the session name (the folder holding the file) when there is
// exactly one. A session's worktree holds exactly one status.json, which is
// how an inbox item learns its session without a flag; zero or several
// matches mean we cannot tell, so the answer is "" rather than a guess.
func inferSession(statusPaths []string) string {
	if len(statusPaths) != 1 {
		return ""
	}
	return filepath.Base(filepath.Dir(filepath.Clean(statusPaths[0])))
}
