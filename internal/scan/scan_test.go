package scan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/project"
)

// --- shouldDescend ---------------------------------------------------------

// TestShouldDescend is the table over the three independent bounds the whole
// cost guarantee rests on (see the package doc). Every row below is one of
// those bounds or the interaction between two of them, and the ones worth
// calling out are:
//
//   - "at the depth cap" and "one below the depth cap": the cap is `depth >=
//     maxDepth`, not `>`, and off-by-one here is invisible in behavior except
//     as an extra whole level of directories walked on every tick. Pinned
//     together these two rows nail the boundary from both sides.
//   - "repo root at depth 0": D18's case. A configured search root that is
//     itself a repo must register as that one repo and NOT be descended into,
//     which is only true if the repo-root bound outranks a permissive depth.
//   - "symlink that is otherwise descendable": the only row proving the
//     symlink refusal is not incidentally provided by one of the other two
//     bounds. If someone deletes the isSymlink check, this is the row that
//     goes red.
func TestShouldDescend(t *testing.T) {
	tests := []struct {
		name       string
		depth      int
		maxDepth   int
		isRepoRoot bool
		isSymlink  bool
		want       bool
	}{
		{
			name:     "plain directory at the root",
			depth:    0,
			maxDepth: 3,
			want:     true,
		},
		{
			name:     "one below the depth cap",
			depth:    2,
			maxDepth: 3,
			want:     true,
		},
		{
			name:     "at the depth cap",
			depth:    3,
			maxDepth: 3,
			want:     false,
		},
		{
			name:     "past the depth cap",
			depth:    4,
			maxDepth: 3,
			want:     false,
		},
		{
			name:     "zero max depth never descends",
			depth:    0,
			maxDepth: 0,
			want:     false,
		},
		{
			name:       "repo root at depth 0",
			depth:      0,
			maxDepth:   3,
			isRepoRoot: true,
			want:       false,
		},
		{
			name:       "repo root mid walk",
			depth:      1,
			maxDepth:   3,
			isRepoRoot: true,
			want:       false,
		},
		{
			name:      "symlink that is otherwise descendable",
			depth:     0,
			maxDepth:  3,
			isSymlink: true,
			want:      false,
		},
		{
			name:       "symlinked repo root",
			depth:      1,
			maxDepth:   3,
			isRepoRoot: true,
			isSymlink:  true,
			want:       false,
		},
		{
			name:      "symlink past the depth cap",
			depth:     9,
			maxDepth:  3,
			isSymlink: true,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldDescend(tt.depth, tt.maxDepth, tt.isRepoRoot, tt.isSymlink)
			if got != tt.want {
				t.Errorf("shouldDescend(%d, %d, %t, %t) = %t, want %t",
					tt.depth, tt.maxDepth, tt.isRepoRoot, tt.isSymlink, got, tt.want)
			}
		})
	}
}

// --- real-git-repo fixtures ------------------------------------------------
//
// Scan's entire value proposition is faithfully classifying real on-disk git
// state — a main checkout versus a linked worktree versus a plain directory
// that merely sits inside a repo — so (mirroring internal/discover's
// discover_test.go:15-21 reasoning) these tests script real, disposable git
// repos under t.TempDir() rather than faking `git`.

// runGit runs git with args in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// tmpRoot returns a symlink-resolved temp directory. Resolution matters on
// darwin, where t.TempDir() hands back a path under /var (a symlink to
// /private/var): Scan resolves the roots it is given, so an unresolved
// expectation would compare against the wrong string. Tests that specifically
// exercise root resolution pass the unresolved path in on purpose.
func tmpRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	return dir
}

// mkdir creates path (and parents) and returns it.
func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

// initRepo creates a git repo at path with one commit on branch "main" and a
// local identity (sandboxes/CI may have none configured globally), and returns
// path. It works just as well nested inside another repo, which is what the
// pruning tests need.
func initRepo(t *testing.T, path string) string {
	t.Helper()
	mkdir(t, path)
	runGit(t, path, "init", "-q", "-b", "main")
	runGit(t, path, "config", "user.name", "afk Test")
	runGit(t, path, "config", "user.email", "afk-test@example.com")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("seed README in %s: %v", path, err)
	}
	runGit(t, path, "add", "-A")
	runGit(t, path, "commit", "-q", "-m", "chore: Initial commit")
	return path
}

// seedAfk gives path the `.afk/` directory Scan requires of a project.
func seedAfk(t *testing.T, path string) string {
	t.Helper()
	mkdir(t, filepath.Join(path, ".afk", "plans"))
	return path
}

// afkRepo is initRepo + seedAfk: a directory that Scan must register.
func afkRepo(t *testing.T, path string) string {
	t.Helper()
	return seedAfk(t, initRepo(t, path))
}

// symlink creates a symlink at linkPath pointing at target.
func symlink(t *testing.T, target, linkPath string) string {
	t.Helper()
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatalf("symlink %s -> %s: %v", linkPath, target, err)
	}
	return linkPath
}

// paths pulls the Path field off each result, which is what nearly every
// assertion below is about.
func paths(got []project.Project) []string {
	out := make([]string, 0, len(got))
	for _, p := range got {
		out = append(out, p.Path)
	}
	return out
}

// assertPaths compares result paths against want, in order — Scan's ordering
// is part of its contract, so this deliberately does not sort got.
func assertPaths(t *testing.T, got []project.Project, want []string) {
	t.Helper()
	gotPaths := paths(got)
	if len(gotPaths) != len(want) {
		t.Fatalf("Scan returned %d projects %v, want %d %v", len(gotPaths), gotPaths, len(want), want)
	}
	for i := range want {
		if gotPaths[i] != want[i] {
			t.Errorf("Scan()[%d].Path = %q, want %q (full: %v)", i, gotPaths[i], want[i], gotPaths)
		}
	}
}

// --- Scan ------------------------------------------------------------------

// TestScanQualification covers what does and does not count as a afk
// project. The two-stage detection the implementation is required to use
// (cheap `.git` lstat first, `git rev-parse` only for survivors) makes three
// of these rows load-bearing rather than obvious:
//
//   - "repo without .afk" and "plain directory with .afk" are the two
//     halves of the conjunction. Dropping either half of "repo root AND
//     .afk" leaves the other row passing.
//   - "subdirectory of a repo with .afk" is the row that fails if
//     `rev-parse` is used as the primary repo-root probe instead of the `.git`
//     lstat: `rev-parse` succeeds from any subdirectory, and its --git-dir /
//     --git-common-dir outputs agree there, so a subdirectory would
//     misregister as a project.
func TestScanQualification(t *testing.T) {
	t.Run("repo with .afk is found", func(t *testing.T) {
		root := tmpRoot(t)
		proj := afkRepo(t, filepath.Join(root, "proj"))

		assertPaths(t, Scan([]string{root}, Deps{}), []string{proj})
	})

	t.Run("repo without .afk is not found", func(t *testing.T) {
		root := tmpRoot(t)
		initRepo(t, filepath.Join(root, "plain"))

		assertPaths(t, Scan([]string{root}, Deps{}), nil)
	})

	t.Run("plain directory with .afk is not found", func(t *testing.T) {
		root := tmpRoot(t)
		seedAfk(t, mkdir(t, filepath.Join(root, "notarepo")))

		assertPaths(t, Scan([]string{root}, Deps{}), nil)
	})

	t.Run("subdirectory of a repo with .afk is not found", func(t *testing.T) {
		root := tmpRoot(t)
		// The repo itself has no .afk, so nothing prunes the walk on the
		// project's behalf; only the .git-lstat stage keeps sub/ out.
		repo := initRepo(t, filepath.Join(root, "repo"))
		seedAfk(t, mkdir(t, filepath.Join(repo, "sub")))

		assertPaths(t, Scan([]string{root}, Deps{}), nil)
	})

	t.Run("bare repo without .afk is not found", func(t *testing.T) {
		root := tmpRoot(t)
		bare := mkdir(t, filepath.Join(root, "bare.git"))
		runGit(t, bare, "init", "-q", "--bare")

		assertPaths(t, Scan([]string{root}, Deps{}), nil)
	})
}

// TestScanPruning covers the repo-boundary bound: the walk never enters a git
// repo, which is what makes a name-based skip list for node_modules/vendor
// unnecessary and what keeps `.claude/worktrees/*` out of the results.
func TestScanPruning(t *testing.T) {
	t.Run("repo nested inside a repo is pruned", func(t *testing.T) {
		root := tmpRoot(t)
		outer := afkRepo(t, filepath.Join(root, "outer"))
		afkRepo(t, filepath.Join(outer, "inner"))

		assertPaths(t, Scan([]string{root}, Deps{}), []string{outer})
	})

	t.Run("root that is itself a repo returns exactly that repo", func(t *testing.T) {
		// D18: a configured search root may itself be a repo. It registers as
		// that one repo and is NOT descended into, so the nested project stays
		// invisible.
		root := afkRepo(t, tmpRoot(t))
		afkRepo(t, filepath.Join(root, "inner"))

		assertPaths(t, Scan([]string{root}, Deps{}), []string{root})
	})

	t.Run("root that is a repo without .afk returns nothing", func(t *testing.T) {
		root := initRepo(t, tmpRoot(t))
		afkRepo(t, filepath.Join(root, "inner"))

		assertPaths(t, Scan([]string{root}, Deps{}), nil)
	})

	t.Run("linked worktree is not returned as a project", func(t *testing.T) {
		root := tmpRoot(t)
		main := afkRepo(t, filepath.Join(root, "main"))
		wt := filepath.Join(root, "wt-feature")
		runGit(t, main, "worktree", "add", "-q", "-b", "feature", wt, "main")
		// A worktree of a afk project generally has its own .afk/ —
		// that is exactly how .claude/worktrees/* looks, and the
		// --git-dir/--git-common-dir divergence is the only thing excluding it.
		seedAfk(t, wt)

		assertPaths(t, Scan([]string{root}, Deps{}), []string{main})
	})
}

// TestScanDepth pins the depth cap against real directories. The depth-3 and
// depth-4 rows are the boundary from both sides under the default MaxDepth of
// 3, and they live in one tree so a bug that shifted the whole walk by a level
// could not pass both.
func TestScanDepth(t *testing.T) {
	t.Run("depth 3 is found and depth 4 is not", func(t *testing.T) {
		root := tmpRoot(t)
		// root(0)/a(1)/b(2)/shallow(3)
		shallow := afkRepo(t, filepath.Join(root, "a", "b", "shallow"))
		// root(0)/x(1)/y(2)/z(3)/deep(4)
		afkRepo(t, filepath.Join(root, "x", "y", "z", "deep"))

		assertPaths(t, Scan([]string{root}, Deps{}), []string{shallow})
	})

	t.Run("explicit max depth 1", func(t *testing.T) {
		root := tmpRoot(t)
		near := afkRepo(t, filepath.Join(root, "near"))
		afkRepo(t, filepath.Join(root, "mid", "far"))

		assertPaths(t, Scan([]string{root}, Deps{MaxDepth: 1}), []string{near})
	})

	t.Run("non-positive max depth falls back to the default", func(t *testing.T) {
		// A zero-value Deps must walk exactly as DefaultMaxDepth does, since
		// that is what a caller forwarding an unset config hands over.
		root := tmpRoot(t)
		shallow := afkRepo(t, filepath.Join(root, "a", "b", "shallow"))

		for _, maxDepth := range []int{0, -5} {
			assertPaths(t, Scan([]string{root}, Deps{MaxDepth: maxDepth}), []string{shallow})
		}
	})
}

// TestScanSymlinks covers the third bound. The target lives OUTSIDE the search
// root on purpose: a symlink pointing at something the walk would reach anyway
// cannot distinguish "not traversed" from "traversed and deduplicated".
func TestScanSymlinks(t *testing.T) {
	t.Run("symlinked directory is not traversed", func(t *testing.T) {
		base := tmpRoot(t)
		root := mkdir(t, filepath.Join(base, "root"))
		outside := mkdir(t, filepath.Join(base, "outside"))
		afkRepo(t, filepath.Join(outside, "proj"))
		symlink(t, outside, filepath.Join(root, "link"))

		assertPaths(t, Scan([]string{root}, Deps{}), nil)
	})

	t.Run("symlink pointing directly at a project is not registered", func(t *testing.T) {
		base := tmpRoot(t)
		root := mkdir(t, filepath.Join(base, "root"))
		proj := afkRepo(t, filepath.Join(base, "outside", "proj"))
		symlink(t, proj, filepath.Join(root, "link"))

		assertPaths(t, Scan([]string{root}, Deps{}), nil)
	})

	t.Run("dangling symlink is skipped without failing the scan", func(t *testing.T) {
		root := tmpRoot(t)
		symlink(t, filepath.Join(root, "nope"), filepath.Join(root, "dangling"))
		proj := afkRepo(t, filepath.Join(root, "proj"))

		assertPaths(t, Scan([]string{root}, Deps{}), []string{proj})
	})
}

// TestScanRoots covers root handling: resolution, deduplication of overlapping
// roots, and roots that are not usable directories at all.
func TestScanRoots(t *testing.T) {
	t.Run("root is symlink resolved", func(t *testing.T) {
		// t.TempDir() is under /var on darwin, itself a symlink to
		// /private/var. Passing the unresolved path must still yield resolved
		// project paths, or dedupe-by-path silently stops working.
		unresolved := t.TempDir()
		resolved, err := filepath.EvalSymlinks(unresolved)
		if err != nil {
			t.Fatalf("resolving temp dir: %v", err)
		}
		afkRepo(t, filepath.Join(unresolved, "proj"))

		assertPaths(t, Scan([]string{unresolved}, Deps{}), []string{filepath.Join(resolved, "proj")})
	})

	t.Run("overlapping roots yield one entry per project", func(t *testing.T) {
		root := tmpRoot(t)
		mid := mkdir(t, filepath.Join(root, "mid"))
		proj := afkRepo(t, filepath.Join(mid, "proj"))

		assertPaths(t, Scan([]string{root, mid, root}, Deps{}), []string{proj})
	})

	t.Run("relative root is made absolute", func(t *testing.T) {
		root := tmpRoot(t)
		proj := afkRepo(t, filepath.Join(root, "proj"))
		t.Chdir(root)

		assertPaths(t, Scan([]string{"."}, Deps{}), []string{proj})
	})

	t.Run("missing root is skipped, not an error", func(t *testing.T) {
		root := tmpRoot(t)
		proj := afkRepo(t, filepath.Join(root, "proj"))

		got := Scan([]string{filepath.Join(root, "does-not-exist"), root}, Deps{})
		assertPaths(t, got, []string{proj})
	})

	t.Run("root that is a file is skipped", func(t *testing.T) {
		root := tmpRoot(t)
		file := filepath.Join(root, "notadir")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
		proj := afkRepo(t, filepath.Join(root, "proj"))

		assertPaths(t, Scan([]string{file, root}, Deps{}), []string{proj})
	})

	t.Run("empty root list returns no projects", func(t *testing.T) {
		assertPaths(t, Scan(nil, Deps{}), nil)
	})

	t.Run("blank root string is skipped", func(t *testing.T) {
		assertPaths(t, Scan([]string{"", "   "}, Deps{}), nil)
	})
}

// TestScanOrdering asserts the deterministic ordering the switcher depends on:
// without it the project list would reshuffle on every 60s scan tick and move
// rows out from under the user's cursor. The reversed-roots row is the one that
// matters — it proves the order comes from sorting the results, not from the
// order the roots happened to be walked in.
func TestScanOrdering(t *testing.T) {
	root := tmpRoot(t)
	a := afkRepo(t, filepath.Join(root, "aaa"))
	m := afkRepo(t, filepath.Join(root, "mmm"))
	z := afkRepo(t, filepath.Join(root, "zzz"))
	want := []string{a, m, z}

	assertPaths(t, Scan([]string{root}, Deps{}), want)

	// Same tree reached through per-project roots handed over in reverse.
	assertPaths(t, Scan([]string{z, m, a}, Deps{}), want)
}

// TestScanPartialResults is the "one unreadable directory must not blank the
// switcher" guarantee: the scan feeds a UI list, so degrading to fewer
// projects is always better than degrading to none.
func TestScanPartialResults(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 does not deny readdir")
	}
	root := tmpRoot(t)
	good := afkRepo(t, filepath.Join(root, "good"))
	bad := mkdir(t, filepath.Join(root, "unreadable"))
	afkRepo(t, filepath.Join(bad, "hidden"))
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", bad, err)
	}
	// Restore before t.TempDir's cleanup runs, or the cleanup itself fails.
	t.Cleanup(func() { _ = os.Chmod(bad, 0o755) })

	assertPaths(t, Scan([]string{root}, Deps{}), []string{good})
}

// TestScanNames checks the results carry display labels from
// project.ProjectsFromPaths rather than raw paths, including the
// colliding-basename case that is the entire reason that function exists —
// two checkouts of the same repo name under different parents is the ordinary
// state of this very machine.
func TestScanNames(t *testing.T) {
	root := tmpRoot(t)
	afkRepo(t, filepath.Join(root, "solo"))
	afkRepo(t, filepath.Join(root, "one", "api"))
	afkRepo(t, filepath.Join(root, "two", "api"))

	got := Scan([]string{root}, Deps{})
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
		if p.Pinned {
			t.Errorf("Scan result %q has Pinned=true; only the caller sets that", p.Path)
		}
	}
	// Sorted by path: one/api, solo, two/api.
	want := []string{"one/api", "solo", "two/api"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("Scan names = %v, want %v (paths %v)", names, want, paths(got))
	}
}

// TestScanGitSeam pins the git invocation to the shared
// `Run(dir, name string, args ...string)` seam every other package uses, and
// pins the cost model with it. The call-count assertion is the load-bearing
// part: the two-stage design exists so that `git` is spawned once per
// *candidate* directory (one `.git` entry present), not once per directory
// walked, and nothing else in the suite would notice that regression.
func TestScanGitSeam(t *testing.T) {
	root := tmpRoot(t)
	proj := afkRepo(t, filepath.Join(root, "proj"))
	// Directories that must never reach git: plain ones fail the `.git` lstat,
	// and a repo with no `.afk` fails the other half of the conjunction
	// before the subprocess is ever needed.
	mkdir(t, filepath.Join(root, "plain", "deeper"))
	mkdir(t, filepath.Join(root, "empty"))
	initRepo(t, filepath.Join(root, "repo-without-afk"))

	var calls [][]string
	real := DefaultDeps().Run
	d := Deps{Run: func(dir, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{dir, name}, args...))
		return real(dir, name, args...)
	}}

	assertPaths(t, Scan([]string{root}, d), []string{proj})

	if len(calls) != 1 {
		t.Fatalf("git invoked %d times, want 1 (one candidate directory): %v", len(calls), calls)
	}
	got := calls[0]
	want := []string{proj, "git", "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("git call = %v, want %v", got, want)
	}
}

// TestScanGitFailureIsNotFatal covers a directory that passes the cheap `.git`
// check but whose `git rev-parse` fails — a genuinely broken checkout, or a
// `.git` file that is not a gitlink at all. It must not register and must not
// stop the scan.
func TestScanGitFailureIsNotFatal(t *testing.T) {
	root := tmpRoot(t)
	broken := seedAfk(t, mkdir(t, filepath.Join(root, "broken")))
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatalf("write fake .git: %v", err)
	}
	proj := afkRepo(t, filepath.Join(root, "proj"))

	assertPaths(t, Scan([]string{root}, Deps{}), []string{proj})
}

// TestDefaultDeps guards the two defaults callers rely on when they pass a
// zero-value Deps: a real git Run, and the MaxDepth of 3 the config layer also
// defaults to. A nil Run would panic on the first candidate directory.
func TestDefaultDeps(t *testing.T) {
	d := DefaultDeps()
	if d.Run == nil {
		t.Error("DefaultDeps().Run is nil")
	}
	if d.MaxDepth != DefaultMaxDepth {
		t.Errorf("DefaultDeps().MaxDepth = %d, want %d", d.MaxDepth, DefaultMaxDepth)
	}
	if DefaultMaxDepth != 3 {
		t.Errorf("DefaultMaxDepth = %d, want 3 (must match internal/config's default)", DefaultMaxDepth)
	}

	filled := Deps{}.withDefaults()
	if filled.Run == nil {
		t.Error("Deps{}.withDefaults().Run is nil")
	}
	if filled.MaxDepth != DefaultMaxDepth {
		t.Errorf("Deps{}.withDefaults().MaxDepth = %d, want %d", filled.MaxDepth, DefaultMaxDepth)
	}
}

// TestWithDefaults_ExplicitZeroIsTreatedAsUnset pins the chosen semantics for
// the review finding on MaxDepth's nil-vs-zero collapse: this package's
// int-typed MaxDepth cannot itself distinguish "caller left this unset" from
// "caller explicitly passed 0", so it treats both identically and always has
// (see Deps.MaxDepth's doc). That is deliberately still true after the fix —
// the fix is that internal/config.Load now refuses to ever forward a real
// explicit 0, so this sentinel only ever fires for an actually-unset caller.
// This row exists so that invariant cannot be silently narrowed later: if
// Deps.MaxDepth ever needs to carry a genuine 0, it needs config's *int
// treatment, not a change to this behavior.
func TestWithDefaults_ExplicitZeroIsTreatedAsUnset(t *testing.T) {
	got := Deps{MaxDepth: 0}.withDefaults().MaxDepth
	if got != DefaultMaxDepth {
		t.Errorf("Deps{MaxDepth: 0}.withDefaults().MaxDepth = %d, want %d", got, DefaultMaxDepth)
	}
}
