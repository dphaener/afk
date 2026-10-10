// internal/session's base-tracking logic (baseTrackDecision, the create-step
// degrade path) assumes specific git-spice behaviors that were verified by
// hand against git-spice 0.31.2, not derived from any documented contract:
// tracking onto an untracked base fails mentioning "not tracked" rather than
// guessing a parent, tracking onto a tracked base succeeds and is visible in
// `gs log short`, and untrack reverses it. This test re-verifies those
// behaviors against a real binary so a future git-spice upgrade that changes
// one of them fails here instead of surfacing as a mis-parenting bug in
// afk new. It skips cleanly when git-spice or git isn't on $PATH, which is
// why it lives apart from the pure argv-construction tests in spice_test.go.
package spice

import (
	"os/exec"
	"strings"
	"testing"
)

// TestGitSpiceTrackContract skips unless a git-spice binary resolves (via
// BinaryPath, never the literal "gs" — on this machine gs is a zsh alias for
// "git spice", not an executable on $PATH) and git is available to build a
// throwaway repo. t.TempDir() provides the repo directory and registers its
// own cleanup; no explicit removal is needed here.
func TestGitSpiceTrackContract(t *testing.T) {
	bin, ok := BinaryPath()
	if !ok {
		t.Skip("git-spice binary not found on $PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on $PATH")
	}

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "-c", "user.name=afk-test", "-c", "user.email=afk-test@example.com",
		"commit", "--allow-empty", "-m", "init")

	cmd := exec.Command(bin, "--no-prompt", "repo", "init", "--trunk", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s --no-prompt repo init --trunk main: %v: %s", bin, err, out)
	}

	runGit(t, dir, "branch", "side", "main")
	runGit(t, dir, "branch", "child", "side")

	// (a) tracking onto an untracked base fails, mentioning "not tracked".
	err := TrackBranch(BranchTrackOptions{Dir: dir, Name: "child", Base: "side"})
	if err == nil {
		t.Fatal("TrackBranch(child, base side) = nil error, want an error (side is not tracked yet)")
	}
	if !strings.Contains(err.Error(), "not tracked") {
		t.Errorf("TrackBranch(child, base side) error = %q, want it to contain %q", err, "not tracked")
	}

	// (b) tracking onto a tracked base (trunk) succeeds and shows up in LogShort.
	if err := TrackBranch(BranchTrackOptions{Dir: dir, Name: "side", Base: "main"}); err != nil {
		t.Fatalf("TrackBranch(side, base main) error = %v, want nil", err)
	}
	branches, err := LogShort(dir)
	if err != nil {
		t.Fatalf("LogShort() error = %v", err)
	}
	if _, found := TrackedBranch(branches, "side"); !found {
		t.Errorf("LogShort() = %+v, want it to include %q after tracking", branches, "side")
	}

	// (c) tracking child onto the now-tracked side succeeds.
	if err := TrackBranch(BranchTrackOptions{Dir: dir, Name: "child", Base: "side"}); err != nil {
		t.Fatalf("TrackBranch(child, base side) error = %v after tracking side, want nil", err)
	}

	// (d) untracking removes the branch from LogShort.
	if err := UntrackBranch(dir, "child"); err != nil {
		t.Fatalf("UntrackBranch(child) error = %v, want nil", err)
	}
	branches, err = LogShort(dir)
	if err != nil {
		t.Fatalf("LogShort() error = %v", err)
	}
	if _, found := TrackedBranch(branches, "child"); found {
		t.Errorf("LogShort() = %+v, want it to no longer include %q after untracking", branches, "child")
	}

	// (e) an empty Base is refused without touching the repo at all.
	if err := TrackBranch(BranchTrackOptions{Dir: dir, Name: "child", Base: ""}); err == nil {
		t.Fatal(`TrackBranch(child, base "") = nil error, want the empty-base refusal`)
	} else if !strings.Contains(err.Error(), "refusing to track") {
		t.Errorf("TrackBranch(child, base \"\") error = %q, want the refusal message", err)
	}
}
