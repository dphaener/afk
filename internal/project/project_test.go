package project

import (
	"reflect"
	"testing"
)

// TestDisplayNames is table-driven over the collision-resolution rules that
// DisplayNames promises callers. Two rows are load-bearing above the rest:
//
//   - "one two-way collision, others untouched" pins the central invariant
//     that only the colliding set grows a prefix — a naive implementation
//     that uniformly prefixes every entry (e.g. always joins the last two
//     segments) would pass every other row here but fail this one, since it
//     would needlessly lengthen labels that were already unique.
//   - "collision unresolved by one parent segment" pins that prefixing keeps
//     going past a single parent segment when that segment is itself shared
//     (".../x/repo" twice), rather than stopping after one step regardless
//     of whether it actually disambiguated anything.
//
// The three-way rows check that the algorithm generalizes past pairs: one
// case where all three share nothing beyond the basename (resolved in one
// step), and one where two of the three also share an immediate parent (so
// those two need an extra step while the third is already done).
func TestDisplayNames(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{
			name:  "empty input",
			paths: []string{},
			want:  []string{},
		},
		{
			name:  "single input",
			paths: []string{"/home/dev/repos/afk"},
			want:  []string{"afk"},
		},
		{
			name: "no collisions stay bare basenames",
			paths: []string{
				"/home/dev/repos/afk",
				"/home/dev/repos/knowledge",
				"/home/dev/repos/claude-marche",
			},
			want: []string{"afk", "knowledge", "claude-marche"},
		},
		{
			// Load-bearing: only "repo" grows a prefix. "afk" and
			// "knowledge" must stay bare even though they're processed in
			// the same call.
			name: "one two-way collision, others untouched",
			paths: []string{
				"/home/dev/repos/afk",
				"/home/dev/work/repo",
				"/home/dev/personal/repo",
				"/home/dev/repos/knowledge",
			},
			want: []string{"afk", "work/repo", "personal/repo", "knowledge"},
		},
		{
			name: "three-way collision, no shared parent",
			paths: []string{
				"/home/dev/a/repo",
				"/home/dev/b/repo",
				"/home/dev/c/repo",
			},
			want: []string{"a/repo", "b/repo", "c/repo"},
		},
		{
			// Two of the three collisions share an immediate parent segment
			// ("x"), so one parent segment resolves the odd one out
			// ("y/repo") but not the other two, which need to keep going to
			// their own distinguishing parent.
			name: "three-way collision, two share a parent",
			paths: []string{
				"/home/dev/m/x/repo",
				"/home/dev/n/x/repo",
				"/home/dev/y/repo",
			},
			want: []string{"m/x/repo", "n/x/repo", "y/repo"},
		},
		{
			// Load-bearing: one parent segment ("x/repo") is identical for
			// both, so it does not disambiguate. Prefixing must continue to
			// a second segment.
			name: "collision unresolved by one parent segment, keep prefixing",
			paths: []string{
				"/home/a/x/repo",
				"/home/b/x/repo",
			},
			want: []string{"a/x/repo", "b/x/repo"},
		},
		{
			// Basenames differ only in case. Identity and matching are
			// exact-string (case-sensitive), matching D18: no normalization
			// that could make two distinct on-disk paths collapse into one
			// label incorrectly, or fail to distinguish two paths that are
			// distinct only by case.
			name: "paths differing only in case do not collide",
			paths: []string{
				"/home/dev/repos/Afk",
				"/home/dev/other/afk",
			},
			want: []string{"Afk", "afk"},
		},
		{
			name: "order of output matches order of input",
			paths: []string{
				"/home/dev/b/repo",
				"/home/dev/unique",
				"/home/dev/a/repo",
			},
			want: []string{"b/repo", "unique", "a/repo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DisplayNames(tt.paths)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DisplayNames(%v) = %v, want %v", tt.paths, got, tt.want)
			}
		})
	}
}

// TestProjectsFromPaths checks the []Project builder pairs each path with
// its computed name in order and defaults Pinned to false, so an ordinary
// scanned project (the common case) is unaffected by the pinning mechanism
// that W2.T2 layers on top for the cwd-derived project.
func TestProjectsFromPaths(t *testing.T) {
	paths := []string{
		"/home/dev/repos/afk",
		"/home/dev/work/repo",
		"/home/dev/personal/repo",
	}
	want := []Project{
		{Path: "/home/dev/repos/afk", Name: "afk", Pinned: false},
		{Path: "/home/dev/work/repo", Name: "work/repo", Pinned: false},
		{Path: "/home/dev/personal/repo", Name: "personal/repo", Pinned: false},
	}

	got := ProjectsFromPaths(paths)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProjectsFromPaths(%v) = %+v, want %+v", paths, got, want)
	}
}
