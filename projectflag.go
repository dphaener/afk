package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"
)

// resolveProject determines which project path to operate on, combining the
// --project flag value and the current working directory with explicit priority.
//
// This function is pure — it performs no I/O or environment reads. The caller
// does os.Getwd() and os.UserHomeDir() and passes the results in, following the
// pattern used in routeArgs (main.go:93) and shouldHandOffToDash (new.go:146).
// Keeping the function pure makes it fully testable without a TTY, temp files,
// or environment mutation, and makes the error handling contractual: the caller
// reports the error with the right exit code.
//
// Precedence:
// - If flagValue is non-empty, use it (explicit flag beats cwd)
// - If flagValue is empty but cwd is non-empty, use cwd
// - If both are empty, return an error
//
// The result is always absolute, with ~ expanded to home and normalized
// (cleaned: trailing slashes, .. segments, and duplicate separators removed).
func resolveProject(flagValue, cwd, home string) (string, error) {
	var resolved string

	if flagValue != "" {
		resolved = flagValue
	} else if cwd != "" {
		resolved = cwd
	} else {
		return "", fmt.Errorf("--project flag and current working directory both empty")
	}

	// Expand ~ prefix using the home parameter.
	if strings.HasPrefix(resolved, "~/") {
		resolved = filepath.Join(home, resolved[2:])
	} else if resolved == "~" {
		resolved = home
	}

	// Make relative paths absolute relative to cwd when flag is used.
	// If flagValue was used, it may be relative, so use cwd as the base.
	if flagValue != "" && !filepath.IsAbs(resolved) {
		resolved = filepath.Join(cwd, resolved)
	}

	// Clean the path: collapses .. segments, duplicate separators, and trailing slashes.
	resolved = filepath.Clean(resolved)

	return resolved, nil
}

// addProjectFlag registers the shared --project flag with consistent naming and
// help text. It returns the pointer to the string var so the caller can read the
// parsed value after FlagSet.Parse.
//
// All subcommands use the same flag name and usage text through this helper,
// ensuring consistency across the suite and reducing duplication. The caller
// passes in its own FlagSet and gets back a pointer to read after parsing:
//
//	proj := addProjectFlag(fs)
//	fs.Parse(args)
//	path, err := resolveProject(*proj, cwd, home)
func addProjectFlag(fs *flag.FlagSet) *string {
	return fs.String("project", "", "Path to the project repo to operate on")
}
