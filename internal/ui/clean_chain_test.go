package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/session"
)

// This file is the one test in the suite that runs the ENTIRE dashboard clean
// chain — discover.Discover, cleanTargetKind, session.Discover,
// resolveCleanTarget, and (for one case) session.CleanSessionWithOptions —
// against a real repo, rather than against the fakes every other resolveClean
// table test in ui_test.go drives. Those tables are the right tool for pinning
// resolveCleanTarget's refusal logic in isolation; none of them proves the two
// packages' Discover functions actually agree on which row is which for the
// same directory, which is the whole point of the shared
// session.Kind.IdentityRelevant() discriminator (see model.go's
// cleanTargetKind doc and clean.go's package doc). A regression that made the
// two packages disagree about a KindStatusWithoutWorktree row would pass every
// existing fake-driven test and still reproduce the `no session named %q
// found` bug this plan exists to fix.
//
// Each subtest builds its own throwaway repo with e2eRepo/e2eGit
// (endtoend_test.go) rather than sharing one, because the two cases need
// different worktree layouts and a shared repo would let one subtest's
// fixture leak into the other's row count.

// writeChainStatus seeds a schema-valid nine-key status.json for name inside
// worktree, with branch as the JSON `branch` field (independent of
// worktree's actual checked-out branch — that mismatch is what the
// KindStatusWithoutWorktree row is about). This package has no existing
// helper that also lets the caller pick the phase and the branch field
// separately (restartsend_test.go's seedStatus hardcodes phase "executing"
// and branch "main"; internal/session/clean_test.go's writeStatus lives in a
// different package), so it is written fresh here.
func writeChainStatus(t *testing.T, worktree, name, phase, branch string) {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := fmt.Sprintf(`{
  "session_id": "11111111-1111-1111-1111-111111111111",
  "plan_name": %q,
  "phase": %q,
  "branch": %q,
  "base_branch": "main",
  "loop_count": 0,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": "2026-07-09T21:14:03Z"
}
`, name, phase, branch)
	if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write status.json: %v", err)
	}
}

// chainCleanDeps wires session.CleanDeps to the real environment (defaultRun,
// real git) but with HomeDir pointed at a temp dir, so archiveSessionDir
// writes under a throwaway path rather than the real
// ~/.local/share/afk/archive, and Stdout/Stderr collected rather than
// printed, matching internal/session/clean_test.go's testCleanDeps.
func chainCleanDeps(t *testing.T, home string) session.CleanDeps {
	t.Helper()
	d := session.DefaultCleanDeps()
	d.HomeDir = func() (string, error) { return home, nil }
	d.Stdout = &strings.Builder{}
	d.Stderr = &strings.Builder{}
	return d
}

// failingRunChainCleanDeps is chainCleanDeps with a Run that fails the test on
// any invocation — the only honest way to assert "this route runs no git"
// against a real repo, mirroring internal/session/clean_test.go's
// failingRunCleanDeps: a filesystem check can only observe the damage after
// the fact, and only in a fixture where the git command happened to succeed.
func failingRunChainCleanDeps(t *testing.T, home string) session.CleanDeps {
	t.Helper()
	d := chainCleanDeps(t, home)
	d.Run = func(dir, name string, args ...string) (string, error) {
		t.Fatalf("Run must not be called on the status-without-worktree route, got %s %v (dir=%s)", name, args, dir)
		return "", nil
	}
	return d
}

// findRow returns the discover.SessionState in rows whose Kind matches kind,
// failing the test if there is not exactly one.
func findRow(t *testing.T, rows []discover.SessionState, name string, kind session.Kind) discover.SessionState {
	t.Helper()
	var found []discover.SessionState
	for _, row := range rows {
		if row.Name == name && row.Kind == kind {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("discover.Discover reported %d rows named %q with kind %q, want 1: %+v", len(found), name, kind, rows)
	}
	return found[0]
}

func TestCleanChainResolvesStrandedSession(t *testing.T) {
	// Subtest 1: a standalone `/afk:go` session's status.json living in the
	// primary checkout (checked out on "main", not "standalone") next to a
	// healthy session in its own worktree. This is the exact shape the plan's
	// Context names as the common case of the bug: the dashboard's `c` on the
	// standalone row must resolve to the carrier, not to "no session named
	// standalone found".
	t.Run("standalone session resolves to its carrier and cleans without git", func(t *testing.T) {
		repo := e2eRepo(t)
		writeChainStatus(t, repo, "standalone", "done", "standalone")

		healthyWT := filepath.Join(filepath.Dir(repo), "wt-healthy")
		e2eGit(t, repo, "worktree", "add", "-b", "healthy", healthyWT, "main")
		writeChainStatus(t, healthyWT, "healthy", "done", "healthy")

		// A bystander untracked file in the carrier: the assertion that proves
		// this route ran no git is that this file is STILL untracked
		// afterward — a snapshot commit (git add -A && git commit) would have
		// swept it in.
		untracked := filepath.Join(repo, "scratch.txt")
		if err := os.WriteFile(untracked, []byte("keep me\n"), 0o644); err != nil {
			t.Fatalf("seeding untracked file: %v", err)
		}

		rows, err := discover.Discover(repo, discover.Deps{})
		if err != nil {
			t.Fatalf("discover.Discover: %v", err)
		}
		row := findRow(t, rows, "standalone", session.KindStatusWithoutWorktree)

		home := t.TempDir()
		sessions, err := session.Discover(repo, chainCleanDeps(t, home))
		if err != nil {
			t.Fatalf("session.Discover: %v", err)
		}

		kind := cleanTargetKind(row.Kind)
		target, err := resolveCleanTarget(sessions, "standalone", string(row.Phase), row.Scratch, kind, dashboardPhase)
		if err != nil {
			t.Fatalf("resolveCleanTarget: %v", err)
		}
		if target.Kind != session.KindStatusWithoutWorktree {
			t.Errorf("target.Kind = %q, want %q", target.Kind, session.KindStatusWithoutWorktree)
		}
		if target.WorktreePath != repo {
			t.Errorf("target.WorktreePath = %q, want the primary checkout %q", target.WorktreePath, repo)
		}
		if target.Branch != "" {
			t.Errorf("target.Branch = %q, want empty", target.Branch)
		}

		if _, err := session.CleanSessionWithOptions(repo, *target, failingRunChainCleanDeps(t, home), session.CleanOptions{}); err != nil {
			t.Fatalf("CleanSessionWithOptions: %v", err)
		}

		if _, err := os.Stat(untracked); err != nil {
			t.Errorf("untracked file missing after clean: %v", err)
		}
		if _, err := os.Stat(filepath.Join(repo, ".afk", "standalone")); !os.IsNotExist(err) {
			t.Errorf(".afk/standalone still present after clean (stat err=%v)", err)
		}
	})

	// Subtest 2: a name collision, healthy `dup` in its own worktree and a
	// stranded `dup` in the primary checkout. This is the case the mutation
	// check below actually exercises: reverting cleanTargetKind to
	// isWorktreeFault(k) collapses BOTH rows' reduced kind to KindHealthy
	// (KindStatusWithoutWorktree is not a worktree fault), so resolveCleanTarget
	// can no longer tell the two `dup` entries apart and the first match in
	// session.Discover's sorted order answers for both directions — at least
	// one of the two assertions below then gets the wrong entry's
	// WorktreePath/Kind. Subtest 1 alone would not catch that mutation: it has
	// only one entry named "standalone", so both sides of the reduction stay
	// consistent with themselves.
	t.Run("a name collision steers to the matching entry in each direction", func(t *testing.T) {
		repo := e2eRepo(t)

		dupWT := filepath.Join(filepath.Dir(repo), "wt-dup")
		e2eGit(t, repo, "worktree", "add", "-b", "dup", dupWT, "main")
		writeChainStatus(t, dupWT, "dup", "done", "dup")

		writeChainStatus(t, repo, "dup", "done", "dup")

		rows, err := discover.Discover(repo, discover.Deps{})
		if err != nil {
			t.Fatalf("discover.Discover: %v", err)
		}
		healthyRow := findRow(t, rows, "dup", session.KindHealthy)
		strandedRow := findRow(t, rows, "dup", session.KindStatusWithoutWorktree)

		sessions, err := session.Discover(repo, chainCleanDeps(t, t.TempDir()))
		if err != nil {
			t.Fatalf("session.Discover: %v", err)
		}

		healthyTarget, err := resolveCleanTarget(sessions, "dup", string(healthyRow.Phase), healthyRow.Scratch,
			cleanTargetKind(healthyRow.Kind), dashboardPhase)
		if err != nil {
			t.Fatalf("resolveCleanTarget(healthy dup): %v", err)
		}
		if healthyTarget.Kind != session.KindHealthy {
			t.Errorf("healthy dup: target.Kind = %q, want %q", healthyTarget.Kind, session.KindHealthy)
		}
		if healthyTarget.WorktreePath != dupWT {
			t.Errorf("healthy dup: target.WorktreePath = %q, want %q", healthyTarget.WorktreePath, dupWT)
		}

		strandedTarget, err := resolveCleanTarget(sessions, "dup", string(strandedRow.Phase), strandedRow.Scratch,
			cleanTargetKind(strandedRow.Kind), dashboardPhase)
		if err != nil {
			t.Fatalf("resolveCleanTarget(stranded dup): %v", err)
		}
		if strandedTarget.Kind != session.KindStatusWithoutWorktree {
			t.Errorf("stranded dup: target.Kind = %q, want %q", strandedTarget.Kind, session.KindStatusWithoutWorktree)
		}
		if strandedTarget.WorktreePath != repo {
			t.Errorf("stranded dup: target.WorktreePath = %q, want the primary checkout %q", strandedTarget.WorktreePath, repo)
		}
	})
}
