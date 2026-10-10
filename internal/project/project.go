// Package project holds the small type the dashboard and the scanner both
// speak, plus the pure display-naming logic shared by both. It is a leaf
// package: it imports nothing from internal/ui, internal/scan, or
// internal/discover, so those packages can depend on it without a cycle.
package project

import (
	"path/filepath"
	"strings"
)

// Project identifies a discovered repo and how it should be labeled in the
// UI. Path is the resolved absolute repo root and is the sole identity —
// there is deliberately no alias or nickname field. An alias would let two
// different labels point at the same on-disk repo, which would diverge from
// internal/host's SocketPath: that function hashes repoRoot+name to
// disambiguate same-named sessions across repos, and it does that keyed on
// the real path, not a display name. A stored alias would let the two
// diverge silently.
//
// Pinned marks an entry that must survive a rescan even though no search
// root would produce it — the cwd-derived project a user is standing in
// when no configured search root covers it. It defaults to false, so an
// ordinary scanned project is unaffected.
type Project struct {
	Path   string
	Name   string
	Pinned bool
}

// DisplayNames returns one label per input path, in the same order. A path
// whose basename is unique among the inputs gets that bare basename. When
// two or more paths share a basename, every path in that colliding set is
// prefixed with as many trailing parent segments as it takes to make every
// label in the set distinct — paths outside the set are never touched, and
// a set that is still colliding after one parent segment keeps prefixing
// rather than stopping.
//
// It is pure: no filesystem access, so it can run against configured search
// roots that haven't been scanned yet or paths that don't exist locally.
func DisplayNames(paths []string) []string {
	result := make([]string, len(paths))
	if len(paths) == 0 {
		return result
	}

	segments := make([][]string, len(paths))
	depth := make([]int, len(paths))
	for i, p := range paths {
		segments[i] = pathSegments(p)
		depth[i] = 1
	}

	label := func(i int) string {
		s := segments[i]
		d := depth[i]
		if d > len(s) {
			d = len(s)
		}
		return strings.Join(s[len(s)-d:], "/")
	}

	active := make([]int, len(paths))
	for i := range active {
		active[i] = i
	}

	for len(active) > 0 {
		groups := make(map[string][]int)
		for _, i := range active {
			l := label(i)
			groups[l] = append(groups[l], i)
		}

		var next []int
		for l, idxs := range groups {
			if len(idxs) == 1 {
				result[idxs[0]] = l
				continue
			}
			grew := false
			for _, i := range idxs {
				if depth[i] < len(segments[i]) {
					depth[i]++
					grew = true
				}
			}
			if !grew {
				// Every member is already at full path depth and they still
				// collide, meaning the inputs contain duplicate paths. There
				// is nothing further to distinguish them with, so settle on
				// the (identical) full-depth label rather than looping.
				for _, i := range idxs {
					result[i] = label(i)
				}
				continue
			}
			next = append(next, idxs...)
		}
		active = next
	}

	return result
}

// pathSegments splits a path into its non-empty components, so DisplayNames
// can grow a label by trailing segment rather than by raw string slicing.
func pathSegments(path string) []string {
	clean := filepath.Clean(path)
	parts := strings.Split(clean, string(filepath.Separator))
	segments := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			segments = append(segments, p)
		}
	}
	return segments
}

// ProjectsFromPaths builds a []Project by pairing each path with its
// computed display name, preserving input order. Pinned is left at its
// zero value (false); callers that need a pinned entry (W2.T2, for the
// cwd-derived project) set it afterward.
func ProjectsFromPaths(paths []string) []Project {
	names := DisplayNames(paths)
	projects := make([]Project, len(paths))
	for i, p := range paths {
		projects[i] = Project{Path: p, Name: names[i]}
	}
	return projects
}
