package session

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// scratchNameRE matches only the exact "scratch-N" shape this package emits:
// N must be a positive integer with no leading zero, so "scratch-0",
// "scratch-01", "scratch-x", and "scratch-" (no suffix) all fail to match and
// are treated as unrelated names rather than parsed indices.
var scratchNameRE = regexp.MustCompile(`^scratch-([1-9][0-9]*)$`)

// NextScratchName picks the lowest unused "scratch-N" (N >= 1) given the
// names already in use. It never touches disk — the caller supplies
// `existing` via ListSessionDirs — per this repo's pure-decision-function
// convention (see shouldHandOffToDash in new.go): a decision that branches on
// environment state should be a pure function taking that state as a
// parameter, so it is unit-testable without a filesystem.
//
// Names that don't match "scratch-N" exactly (including malformed ones like
// "scratch-0" or "scratch-01") are ignored rather than rejected: this
// function's job is only to find the lowest free slot, not to validate every
// name in the set.
func NextScratchName(existing []string) string {
	used := make(map[int]bool, len(existing))
	for _, name := range existing {
		m := scratchNameRE.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		used[n] = true
	}
	for n := 1; ; n++ {
		if !used[n] {
			return fmt.Sprintf("scratch-%d", n)
		}
	}
}

// ListSessionDirs returns the names of the entries directly under
// {repo}/.afk/ (each one is a session directory keyed by session name, per
// statusDir). A missing .afk directory is treated as "no sessions yet"
// rather than an error, matching how the rest of this package treats an
// absent or stale on-disk session as unremarkable rather than exceptional.
func ListSessionDirs(repo string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(repo, ".afk"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
