// Merge-state reporting for `afk clean`'s branch-delete offer. Ancestry
// only -- no patch-equivalence (`git cherry`), no squash detection, no PR
// lookup, no network. This is a user decision (decisions.md (W1.T2)): a
// false "merged" must stay impossible, and a squash-merged branch reading
// as unmerged is the accepted cost. This file only reports state; it never
// gates or decides whether a branch should be deleted.
//
// "Impossible" is only as true as the refs are unambiguous. Both names are
// therefore qualified refs/heads/<name> before they reach git: gitrevisions(7)
// resolves refs/tags/<name> BEFORE refs/heads/<name>, so a bare name lets a
// tag sharing the branch's name stand in for it -- and a tag pointing inside
// the base's history reads as merged (exit 0, plus only a "refname is
// ambiguous" warning), which is exactly the false "merged" this file exists
// to rule out. Verified empirically against git 2.51.0.
package session

// Merged reports whether branch has already been merged into base, using
// ancestry only: `git merge-base --is-ancestor refs/heads/<branch>
// refs/heads/<base>`, exit 0 meaning merged. The refs/heads/ qualification is
// load-bearing, not pedantry -- see the package doc for the tag-shadowing
// false "merged" it rules out; both TrunkPlain and status.json's base_branch
// name local branches, so the qualification can never mis-address a base
// that today resolves correctly. run matches CleanDeps.Run's signature and, like
// CleanDeps.Run, returns only (stdout, error) -- the real process exit code
// never crosses that seam, so exit 1 (unmerged) and exit 128 (branch or
// base does not exist) are indistinguishable here. Merged treats every
// non-nil error as "not merged": the safe direction, since collapsing the
// two exit codes the other way would make a false "merged" possible.
//
// base is status.json's base_branch. When it is empty -- status.json is
// missing or failed to parse, a call the two status parsers in this repo
// deliberately make differently, so it stays the caller's decision, not
// this function's -- Merged falls back to the repo's existing trunk
// determination via TrunkPlain (create.go's trunkPlain: origin/HEAD, then
// main, then master). It never consults `git config spice.trunk`: that key
// is never populated by git-spice (verified empirically; see
// internal/spice/spice.go's RepoInitialized doc), so treating its presence
// as a signal is a known-dead heuristic this repo has already ruled out.
func Merged(run func(dir, name string, args ...string) (string, error), repo, branch, base string) bool {
	resolvedBase := base
	if resolvedBase == "" {
		trunk, err := TrunkPlain(Deps{Run: run}, repo)
		if err != nil {
			return false
		}
		resolvedBase = trunk
	}
	_, err := run(repo, "git", "merge-base", "--is-ancestor", "refs/heads/"+branch, "refs/heads/"+resolvedBase)
	return err == nil
}
