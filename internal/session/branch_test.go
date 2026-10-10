package session

import (
	"fmt"
	"testing"
)

// TestMerged is the table test for Merged. Load-bearing rows: "unmerged"
// and "unknown branch" both exercise a non-zero exit from `git
// merge-base --is-ancestor` (1 and 128 respectively) and MUST both resolve
// to false through the same code path, because CleanDeps.Run erases the
// exit code -- if this ever special-cased 128 to mean something other than
// "not merged", it would be inventing a distinction the seam cannot
// actually make. "base fallback" is the other load-bearing row: it is the
// only one where base is passed empty, forcing Merged to call TrunkPlain's
// trunk determination before it can even run the ancestry check, and it
// asserts that resolved trunk (not the raw empty string) is what reaches
// `git merge-base`.
func TestMerged(t *testing.T) {
	tests := []struct {
		name        string
		branch      string
		base        string
		run         func(calls *[]string) func(dir, name string, args ...string) (string, error)
		want        bool
		wantGitCall int // exact count of "git" invocations expected; -1 skips the check
	}{
		{
			name:   "merged: is-ancestor exits 0",
			branch: "done-feature",
			base:   "main",
			run: func(calls *[]string) func(string, string, ...string) (string, error) {
				return func(dir, name string, args ...string) (string, error) {
					*calls = append(*calls, fmt.Sprintf("%s %v", name, args))
					return "", nil
				}
			},
			want:        true,
			wantGitCall: 1,
		},
		{
			name:   "unmerged: is-ancestor exits 1",
			branch: "wip-feature",
			base:   "main",
			run: func(calls *[]string) func(string, string, ...string) (string, error) {
				return func(dir, name string, args ...string) (string, error) {
					*calls = append(*calls, fmt.Sprintf("%s %v", name, args))
					return "", fmt.Errorf("exit status 1")
				}
			},
			want:        false,
			wantGitCall: 1,
		},
		{
			name:   "unknown branch: is-ancestor exits 128",
			branch: "does-not-exist",
			base:   "main",
			run: func(calls *[]string) func(string, string, ...string) (string, error) {
				return func(dir, name string, args ...string) (string, error) {
					*calls = append(*calls, fmt.Sprintf("%s %v", name, args))
					return "", fmt.Errorf("exit status 128")
				}
			},
			// Exit 1 (unmerged) and exit 128 (unknown branch) are
			// indistinguishable through CleanDeps.Run -- both must land
			// here, not on a separate "unknown" outcome.
			want:        false,
			wantGitCall: 1,
		},
		{
			name:   "base fallback: empty base resolves via TrunkPlain",
			branch: "feature-x",
			base:   "",
			run: func(calls *[]string) func(string, string, ...string) (string, error) {
				return func(dir, name string, args ...string) (string, error) {
					*calls = append(*calls, fmt.Sprintf("%s %v", name, args))
					if len(args) > 0 && args[0] == "symbolic-ref" {
						return "origin/master\n", nil
					}
					if len(args) > 0 && args[0] == "merge-base" {
						// The resolved trunk ("master", origin/ stripped),
						// not the raw empty base, must reach the
						// ancestry check — refs/heads/-qualified, per the
						// package doc's tag-shadowing hazard.
						if len(args) < 4 || args[3] != "refs/heads/master" {
							return "", fmt.Errorf("merge-base got base %v, want refs/heads/master", args)
						}
						return "", nil
					}
					return "", fmt.Errorf("unexpected call: %s %v", name, args)
				}
			},
			want:        true,
			wantGitCall: -1, // TrunkPlain's own call count is asserted separately below.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			run := tt.run(&calls)
			got := Merged(run, "/repo", tt.branch, tt.base)
			if got != tt.want {
				t.Errorf("Merged(%q, %q, %q) = %v, want %v", "/repo", tt.branch, tt.base, got, tt.want)
			}
			if tt.wantGitCall >= 0 && len(calls) != tt.wantGitCall {
				t.Errorf("git invocation count = %d %v, want %d", len(calls), calls, tt.wantGitCall)
			}
		})
	}
}

// TestMergedBaseFallbackCallSequence asserts the trunk fallback's own calls
// separately from the ancestry check's call count (acceptance criteria:
// "the ancestry check is exactly one git invocation; the trunk fallback's
// own calls are asserted separately"). It pins the exact sequence: trunk
// determination (symbolic-ref) runs before the merge-base ancestry check,
// and the resolved trunk name -- not the empty base -- is what merge-base
// receives.
func TestMergedBaseFallbackCallSequence(t *testing.T) {
	var calls []string
	run := func(dir, name string, args ...string) (string, error) {
		calls = append(calls, fmt.Sprintf("%s %v", name, args))
		switch {
		case len(args) > 0 && args[0] == "symbolic-ref":
			return "origin/main\n", nil
		case len(args) > 0 && args[0] == "merge-base":
			return "", nil
		}
		return "", fmt.Errorf("unexpected call: %s %v", name, args)
	}

	if !Merged(run, "/repo", "feature-x", "") {
		t.Fatal("Merged() = false, want true")
	}

	want := []string{
		`git [symbolic-ref --short refs/remotes/origin/HEAD]`,
		`git [merge-base --is-ancestor refs/heads/feature-x refs/heads/main]`,
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, calls[i], want[i])
		}
	}
}

// TestMergedNeverConsultsSpiceTrunkConfig guards the plan's explicit
// prohibition: `git config spice.trunk` is never populated by git-spice
// (see internal/spice/spice.go's RepoInitialized doc), so a Run stub that
// fails any "config" invocation must never cause a wrong answer -- Merged's
// trunk fallback must not call it at all.
func TestMergedNeverConsultsSpiceTrunkConfig(t *testing.T) {
	run := func(dir, name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "config" {
			t.Fatalf("Merged consulted git config: %v", args)
		}
		switch {
		case len(args) > 0 && args[0] == "symbolic-ref":
			return "", fmt.Errorf("no origin/HEAD")
		case len(args) > 0 && args[0] == "rev-parse" && len(args) > 3 && args[3] == "refs/heads/main":
			return "", nil
		case len(args) > 0 && args[0] == "merge-base":
			return "", nil
		}
		return "", fmt.Errorf("unexpected call: %s %v", name, args)
	}

	if !Merged(run, "/repo", "feature-x", "") {
		t.Fatal("Merged() = false, want true")
	}
}

// TestMergedIgnoresTagsShadowingBranchNames pins the ambiguity CLASS, not one
// instance of it: gitrevisions(7) resolves refs/tags/<name> BEFORE
// refs/heads/<name>, so a bare name handed to `git merge-base --is-ancestor`
// can silently name a TAG. Both arguments are exposed, and each row covers
// one: a tag shadowing the session branch that points inside the base's
// history makes the bare check exit 0 — a false "merged", the one outcome
// W1.T2 forbids, and the gate that lets --delete-branch skip the unmerged
// confirmation — and a tag shadowing the base that points at the branch's own
// tip does the same from the other side. Real git rather than a Run stub,
// deliberately: the defect lives in git's resolution order, which no mock
// reproduces.
func TestMergedIgnoresTagsShadowingBranchNames(t *testing.T) {
	tests := []struct {
		name      string
		tagName   string
		tagTarget string
	}{
		{
			name:      "tag shadowing the branch points inside the base",
			tagName:   "feat",
			tagTarget: "main",
		},
		{
			name:      "tag shadowing the base points at the branch tip",
			tagName:   "main",
			tagTarget: "feat",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepo(t)
			wt := addWorktree(t, repo, "feat")
			// One commit main lacks: feat is genuinely unmerged, so any
			// "merged" answer below can only have come from resolving the
			// tag instead of the branch.
			commitOnBranch(t, wt, "unmerged.txt")
			runGit(t, repo, "tag", tt.tagName, "refs/heads/"+tt.tagTarget)

			if Merged(defaultRun, repo, "feat", "main") {
				t.Errorf("Merged(feat, main) = true for an unmerged branch under a tag named %q: the ancestry check resolved the tag, not the branch", tt.tagName)
			}
		})
	}
}
