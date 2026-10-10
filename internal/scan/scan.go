// Package scan turns configured search roots (internal/config's SearchRoots)
// into the afk projects beneath them: git repo roots that contain a `.afk`
// directory, returned as internal/project.Project values with their display
// labels already assigned.
//
// Cadence belongs to the caller, so this package caches nothing. internal/ui
// runs Scan on its own scan ticker — 60s by default, deliberately separate
// from the dashboard's 2s discovery ticker — and a cache here would stack a
// second, invisible staleness window on top of the one the caller already
// owns and already documents to the user.
//
// Cost is bounded by exactly three independent limits, all three of which live
// in shouldDescend and are argued for there: a per-root depth cap, pruning at
// every git repo boundary, and an absolute refusal to follow symlinks. They
// matter because search_roots is user-supplied and $HOME is the expected value.
//
// Session state is deliberately absent from everything here. Scan never reads
// or parses status.json; counting a project's sessions is discover.Discover's
// job, which the caller runs once per project against the paths Scan returns.
// That is what keeps this package free of any dependency on internal/discover
// and free of a second status.json parser competing with the one in
// internal/state.
//
// Errors are never fatal and Scan has no error return. An unreadable
// directory, a search root that does not exist, or a checkout too broken for
// `git rev-parse` drops that one subtree from the results and nothing else,
// because the results feed the dashboard's project switcher: showing fewer
// projects degrades gracefully, while failing the whole scan blanks the
// switcher over a single bad directory.
package scan

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dphaener/afk/internal/project"
)

// DefaultMaxDepth is the per-root depth cap applied when Deps carries no
// positive MaxDepth. It matches internal/config's own `max_depth` default, so
// a caller that forwards a zero-value config and a caller that forwards no
// config at all walk identically.
const DefaultMaxDepth = 3

// Deps is the injectable seam for everything Scan needs from outside its own
// logic: the shared git runner used by every other package in this repo, and
// the depth cap. MaxDepth rides along here rather than in Scan's parameter
// list because it is configuration the caller supplies (config.MaxDepth), and
// Deps is already the one place callers assemble that supply.
type Deps struct {
	// Run executes name+args in dir and returns stdout, matching the seam
	// signature in internal/discover and internal/session so a caller can
	// share one implementation across all three.
	Run func(dir, name string, args ...string) (string, error)
	// MaxDepth caps how many levels below a search root the walk descends;
	// see shouldDescend. Any value below 1 is replaced by DefaultMaxDepth.
	//
	// A plain int rather than a pointer, so it doubles as its own "unset"
	// sentinel: a zero-value Deps built by a hermetic test or by a caller
	// that never touched this field means "use the default," identically to
	// an explicit 0. That is safe only because internal/config.Load rejects
	// an explicit max_depth of 0 (or negative) as invalid rather than ever
	// forwarding one — see rawConfig's MaxDepth doc in that package. If this
	// field ever needs to carry a real, deliberate 0 from some other caller,
	// it will need config's *int treatment; don't reuse the sentinel for both.
	MaxDepth int
}

// DefaultDeps returns Deps wired to the real `git` binary and the default
// depth cap.
func DefaultDeps() Deps {
	return Deps{Run: defaultRun, MaxDepth: DefaultMaxDepth}
}

func (d Deps) withDefaults() Deps {
	if d.Run == nil {
		d.Run = defaultRun
	}
	if d.MaxDepth < 1 {
		d.MaxDepth = DefaultMaxDepth
	}
	return d
}

func defaultRun(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Scan walks every root and returns the afk projects found at or beneath
// them, deduplicated by resolved absolute path and sorted by that path.
//
// Each root is itself examined at depth 0 before any descent, which is what
// makes a search root that is *already* a repo register as exactly that one
// repo: the depth-0 check finds it, and the repo-root bound in shouldDescend
// then stops the walk before it enters, so nothing nested inside it can
// appear. A configured root is allowed to be a repo precisely so a user with
// one project does not have to invent a parent directory for it.
//
// The sort is not cosmetic. The project switcher renders this list with a
// cursor in it, so an order that depended on directory-read order would
// reshuffle rows underneath the user on every scan tick.
func Scan(roots []string, d Deps) []project.Project {
	d = d.withDefaults()

	w := walker{deps: d, found: make(map[string]struct{})}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		resolved, ok := resolveRoot(root)
		if !ok {
			continue
		}
		// Overlapping roots stay correct either way (results are keyed by
		// path), but an exactly repeated root would otherwise be walked twice.
		// Roots in an ancestor/descendant relationship are deliberately NOT
		// collapsed: depth is counted per root, so the descendant reaches
		// levels the ancestor's cap puts out of range.
		if _, dup := seen[resolved]; dup {
			continue
		}
		seen[resolved] = struct{}{}
		w.visit(resolved, 0, false)
	}

	paths := make([]string, 0, len(w.found))
	for p := range w.found {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return project.ProjectsFromPaths(paths)
}

// resolveRoot makes one configured search root absolute and symlink-free, and
// reports whether it is usable at all.
//
// Resolving the root is the one sanctioned symlink resolution in this package,
// and it does not weaken the no-follow bound: a root is a path the user
// explicitly asked to be scanned, whereas a symlink met during the walk is a
// path the walk found on its own. It is also load-bearing on darwin, where
// /tmp and /var are themselves symlinks — without it, two roots naming the
// same directory by different spellings would yield the same project twice and
// defeat deduplication.
func resolveRoot(root string) (string, bool) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", false
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return "", false
	}
	return resolved, true
}

// walker carries the walk's immutable inputs and its accumulating result set.
// The set is keyed by path, so the same project reached through two search
// roots collapses to one entry without any ordering assumption.
type walker struct {
	deps  Deps
	found map[string]struct{}
}

// visit examines dir, records it when it qualifies as a afk project, and
// recurses into its children when shouldDescend allows.
//
// Every path put in found is already absolute and symlink-free without a
// further EvalSymlinks call: the root was resolved once by resolveRoot, and
// every component appended below it is a real directory, because a symlinked
// child is refused rather than traversed.
//
// The probe below is skipped entirely for a symlink, and that guard is not
// redundant with shouldDescend's. os.Lstat leaves only the FINAL element of a
// path unresolved, so os.Lstat(link/.git) resolves link — probing a symlinked
// directory's contents *is* following it, which is the one thing this package
// promises never to do.
func (w *walker) visit(dir string, depth int, isSymlink bool) {
	isRepoRoot := false
	if !isSymlink {
		isRepoRoot = hasGitEntry(dir)
		// Both halves of the "repo root AND has .afk" conjunction are
		// required, and their order is a cost decision rather than a semantic
		// one: hasAfkDir is a single os.Lstat while isMainCheckout spawns
		// git, and the overwhelming majority of repos on a developer's disk
		// have no .afk at all. Testing the cheap half first keeps git off
		// the walk's hot path for all of them.
		if isRepoRoot && hasAfkDir(dir) && w.isMainCheckout(dir) {
			w.found[dir] = struct{}{}
		}
	}

	if !shouldDescend(depth, w.deps.MaxDepth, isRepoRoot, isSymlink) {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// Partial results by design: one unreadable directory must not blank
		// the switcher (see the package doc).
		return
	}
	for _, e := range entries {
		// os.ReadDir reports types from lstat, so a symlink appears as
		// ModeSymlink and never as a directory. Symlinked entries are handed
		// to visit anyway, with isSymlink set, so that the refusal lives in
		// shouldDescend alone instead of being silently duplicated by the
		// loop's filter.
		linked := e.Type()&fs.ModeSymlink != 0
		if !e.IsDir() && !linked {
			continue
		}
		w.visit(filepath.Join(dir, e.Name()), depth+1, linked)
	}
}

// shouldDescend decides whether the walk reads dir's children. It is pure:
// every impure probe (the os.Lstat behind isRepoRoot, os.ReadDir's symlink
// bit behind isSymlink) happens in the caller and arrives as a parameter, so
// the whole matrix is table-testable with no filesystem involved — the same
// convention as routeArgs (main.go:93) and shouldHandOffToDash (new.go:142).
//
// The three bounds are independent, and all three are load-bearing. Together
// they are the entire cost guarantee for a walk that runs on a 60s ticker over
// user-supplied roots, where $HOME is the expected value:
//
//   - depth >= maxDepth caps each root at a fixed number of levels, so cost
//     scales with breadth only and not with how deep the deepest tree on the
//     disk happens to run.
//   - isRepoRoot stops the walk at every repo boundary. A repo's interior is
//     where nearly all of a developer's file count lives — node_modules,
//     vendor, build output, .git itself — so never entering one is exactly
//     what makes a name-based skip list for those directories unnecessary. A
//     skip list was considered and rejected: it needs maintaining forever and
//     is already wrong for the next ecosystem's directory name. The same bound
//     keeps the walk out of linked worktrees such as .claude/worktrees/*.
//   - isSymlink refuses to follow links. Without it this is not a tree walk at
//     all: one link pointing back at an ancestor makes it unbounded no matter
//     what the depth cap says, and one link into a large tree smuggles that
//     tree in past the repo pruning.
//
// Weakening any single bound removes the guarantee, because none of them backs
// the other two up.
func shouldDescend(depth, maxDepth int, isRepoRoot, isSymlink bool) bool {
	if depth >= maxDepth {
		return false
	}
	if isRepoRoot {
		return false
	}
	if isSymlink {
		return false
	}
	return true
}

// hasGitEntry is stage one of repo-root detection: does dir hold a `.git`
// entry of its own? One os.Lstat, and it is a genuine root test — a `.git`
// entry exists in a main checkout (as a directory) and in a linked worktree
// (as a gitlink file), and in neither case in a subdirectory of either.
//
// This has to come first, and `git rev-parse` cannot replace it. rev-parse
// succeeds from *any* directory inside a repo, so as the primary probe it
// would misregister a plain subdirectory that happens to contain `.afk` —
// including a configured search root sitting inside a repo — and it would
// spawn a subprocess for every directory walked on every tick.
func hasGitEntry(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// hasAfkDir reports whether dir contains a `.afk` directory, the other
// half of what makes a repo root a afk project. os.Lstat rather than os.Stat
// so that a symlinked `.afk` does not qualify, consistent with the package's
// blanket refusal to follow links.
func hasAfkDir(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, ".afk"))
	return err == nil && fi.IsDir()
}

// isMainCheckout is stage two, run only for directories that already passed
// hasGitEntry: it separates a main checkout from a linked worktree by asking
// git for both the per-worktree git dir and the shared common dir and
// comparing them. They are equal in a main checkout and divergent in a linked
// worktree, which is how `.claude/worktrees/*` gets excluded from the project
// list even though each such worktree is a legitimate repo root with its own
// `.afk`.
//
// --path-format=absolute is belt-and-braces rather than load-bearing today,
// and it is worth keeping deliberately. Without it git answers in whatever
// form is shortest for the caller's position, and the two forms disagree: from
// a repo root both queries return the bare string `.git`, but from a
// subdirectory --git-dir comes back absolute while --git-common-dir comes back
// relative (`../.git`), so a plain string comparison would call two spellings
// of one directory divergent. Stage one currently guarantees this only ever
// runs at a repo root, where the relative forms happen to match — so removing
// the flag would not change today's results, and the day someone calls this
// from anywhere else it would fail silently and misreport every main checkout
// as a linked worktree. The argv assertion in scan_test.go pins the flag for
// that reason.
//
// A single invocation asks for both values because `git rev-parse` accepts
// several queries at once and answers one per line, which halves the number of
// processes spawned per candidate directory.
//
// Failure means "not a main checkout": a `.git` entry git itself cannot make
// sense of is a broken checkout, and a broken checkout is not a project the
// dashboard can do anything with. It is never propagated as an error, and the
// directory is still pruned, since pruning was already decided by stage one.
func (w *walker) isMainCheckout(dir string) bool {
	out, err := w.deps.Run(dir, "git", "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return false
	}
	// Split on newlines rather than whitespace: repository paths routinely
	// contain spaces.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		return false
	}
	gitDir := filepath.Clean(strings.TrimSpace(lines[0]))
	commonDir := filepath.Clean(strings.TrimSpace(lines[1]))
	return gitDir != "" && gitDir == commonDir
}
