package main

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/session"
)

// cleanTestSession builds a minimal session.Session for the gate/confirm
// tables below: a phase for a planned session, or scratch=true for a
// one-off (which carries no phase at all, matching how Discover actually
// produces one — see session.Session.Scratch's doc comment).
func cleanTestSession(phase string, scratch bool) session.Session {
	sess := session.Session{Name: "sess"}
	sess.Status.Phase = phase
	sess.Scratch = scratch
	return sess
}

// TestCleanRefused and TestShouldConfirmForcedClean together are the
// load-bearing regression for W4.T3: both cmdClean's phase gate
// (cleanRefused) and its confirmation gate (shouldConfirmForcedClean) now
// route through session.CleanableWithoutForce instead of each re-encoding
// the done/failed test inline, so a scratch session is cleanable with no
// --force while every non-terminal planned session is still refused.
//
// Rows that are load-bearing beyond the obvious pass/fail split: the scratch
// rows (phase is always "" for a real scratch session, since it has no
// status.json at all) must never be refused and must never need
// confirmation regardless of force/tty, proving the exemption is keyed on
// Scratch and not merely on an empty phase string coinciding with "not
// done/failed". The four non-terminal planned phases (planning, executing,
// reviewing, fixing) must still be refused without --force — this is the
// "must not change refusal behavior for any planned session" constraint.
func TestCleanRefused(t *testing.T) {
	tests := []struct {
		name    string
		force   bool
		phase   string
		scratch bool
		want    bool
	}{
		// Scratch: never refused, with or without --force.
		{"scratch, no force", false, "", true, false},
		{"scratch, force", true, "", true, false},

		// Planned, non-terminal phases: refused without --force.
		{"planning, no force", false, "planning", false, true},
		{"executing, no force", false, "executing", false, true},
		{"reviewing, no force", false, "reviewing", false, true},
		{"fixing, no force", false, "fixing", false, true},
		{"empty phase (stale), no force", false, "", false, true},

		// Planned, non-terminal phases: --force overrides the refusal.
		{"planning, force", true, "planning", false, false},
		{"executing, force", true, "executing", false, false},

		// Terminal phases: never refused, with or without --force.
		{"done, no force", false, "done", false, false},
		{"failed, no force", false, "failed", false, false},
		{"done, force", true, "done", false, false},
		{"failed, force", true, "failed", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := cleanTestSession(tt.phase, tt.scratch)
			got := cleanRefused(tt.force, sess)
			if got != tt.want {
				t.Errorf("cleanRefused(force=%v, phase=%q, scratch=%v) = %v, want %v",
					tt.force, tt.phase, tt.scratch, got, tt.want)
			}
		})
	}
}

// TestShouldConfirmForcedClean covers the full decision matrix for
// shouldConfirmForcedClean: confirmation is required only when force is
// true, the session is NOT cleanable without force per
// session.CleanableWithoutForce (i.e. a planned session in a non-terminal
// phase), and stdin is a TTY. Every other combination must return false,
// including the non-TTY contract that scripts depend on (force +
// non-terminal phase + no TTY must NOT prompt) and the scratch exemption
// (force + scratch + TTY must NOT prompt, since a scratch session is always
// cleanable without force).
func TestShouldConfirmForcedClean(t *testing.T) {
	tests := []struct {
		name    string
		force   bool
		phase   string
		scratch bool
		tty     bool
		want    bool
	}{
		// The one true case: force + non-terminal planned phase + TTY.
		{"force, executing phase, tty", true, "executing", false, true, true},
		{"force, planning phase, tty", true, "planning", false, true, true},
		{"force, reviewing phase, tty", true, "reviewing", false, true, true},
		{"force, fixing phase, tty", true, "fixing", false, true, true},
		{"force, empty phase, tty", true, "", false, true, true},

		// Never for done/failed (terminal phases), regardless of force/tty.
		{"force, done phase, tty", true, "done", false, true, false},
		{"force, failed phase, tty", true, "failed", false, true, false},
		{"force, done phase, no tty", true, "done", false, false, false},
		{"force, failed phase, no tty", true, "failed", false, false, false},
		{"no force, done phase, tty", false, "done", false, true, false},
		{"no force, failed phase, tty", false, "failed", false, true, false},

		// Never for scratch, regardless of force/tty — always cleanable
		// without force, so there is nothing to confirm.
		{"force, scratch, tty", true, "", true, true, false},
		{"force, scratch, no tty", true, "", true, false, false},
		{"no force, scratch, tty", false, "", true, true, false},

		// Never without force.
		{"no force, executing phase, tty", false, "executing", false, true, false},
		{"no force, planning phase, tty", false, "planning", false, true, false},
		{"no force, executing phase, no tty", false, "executing", false, false, false},

		// Never without a TTY, even with force and a non-terminal phase —
		// this is the exact contract scripts rely on (unchanged behavior).
		{"force, executing phase, no tty", true, "executing", false, false, false},
		{"force, planning phase, no tty", true, "planning", false, false, false},
		{"force, empty phase, no tty", true, "", false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := cleanTestSession(tt.phase, tt.scratch)
			got := shouldConfirmForcedClean(tt.force, sess, tt.tty)
			if got != tt.want {
				t.Errorf("shouldConfirmForcedClean(force=%v, phase=%q, scratch=%v, tty=%v) = %v, want %v",
					tt.force, tt.phase, tt.scratch, tt.tty, got, tt.want)
			}
		})
	}
}

// TestCleanSummaryScratchNamesNoEmptyField is the load-bearing row for the
// third part of W4.T3: a scratch CleanResult carries only Name (Branch,
// WorktreePath and ArchivePath are all "" — see session.CleanResult and
// cleanScratchSession), so the planned-session format string would render
// three blank fields verbatim. The scratch branch must print prose that
// names none of them.
func TestCleanSummaryScratchNamesNoEmptyField(t *testing.T) {
	got := cleanSummary(true, session.CleanResult{Name: "scratch-1"})
	for _, bad := range []string{"branch:  \n", "archive: \n", "worktree: \n"} {
		if strings.Contains(got, bad) {
			t.Errorf("cleanSummary(scratch) = %q, contains empty field %q", got, bad)
		}
	}
	if !strings.Contains(got, "scratch-1") {
		t.Errorf("cleanSummary(scratch) = %q, want it to name the session", got)
	}
}

// TestCleanSummaryPlannedStillNamesFields pins the pre-existing planned-
// session format unchanged: branch, archive and worktree are still printed
// verbatim, since a real planned CleanResult's fields are meaningful and
// this task must not alter that path.
func TestCleanSummaryPlannedStillNamesFields(t *testing.T) {
	got := cleanSummary(false, session.CleanResult{
		Name:         "sess",
		Branch:       "sess",
		ArchivePath:  "/archive/sess-2026-08-26",
		WorktreePath: "/repo/wt-sess",
	})
	for _, want := range []string{"branch:  sess", "archive: /archive/sess-2026-08-26", "worktree: /repo/wt-sess"} {
		if !strings.Contains(got, want) {
			t.Errorf("cleanSummary(planned) = %q, missing %q", got, want)
		}
	}
}

// TestCleanSummaryReportsDeletedBranch is the load-bearing row for W3.T1's
// "report the branch outcome and recovery SHA" requirement: a CleanResult
// whose BranchDeleted is true must move the branch line from Kept to Removed
// and print DeletedBranchSHA, the only handle left to recover it (a deleted
// branch is reflog-recoverable by SHA only — see session.CleanResult's doc).
func TestCleanSummaryReportsDeletedBranch(t *testing.T) {
	got := cleanSummary(false, session.CleanResult{
		Name:             "sess",
		Branch:           "sess",
		ArchivePath:      "/archive/sess-2026-08-26",
		WorktreePath:     "/repo/wt-sess",
		BranchDeleted:    true,
		DeletedBranchSHA: "abc1234",
	})
	if strings.Contains(got, "Kept:\n  branch:") {
		t.Errorf("cleanSummary(BranchDeleted) = %q, still lists the branch under Kept", got)
	}
	for _, want := range []string{"archive: /archive/sess-2026-08-26", "worktree: /repo/wt-sess", "sess", "abc1234"} {
		if !strings.Contains(got, want) {
			t.Errorf("cleanSummary(BranchDeleted) = %q, missing %q", got, want)
		}
	}
}

// TestCleanSummaryArchiveSkippedNoDanglingPath is the CLI half of the bug
// class cleanSucceededStatus (internal/ui) was already fixed for in this same
// change: a fault-route CleanResult carries ArchiveSkipped=true and an empty
// ArchivePath — nothing was archived because the session directory died with
// the worktree or never existed — so printing the planned format would render
// `archive: ` with a dangling empty path. The summary must say the archive
// was skipped instead, on both the kept-branch and deleted-branch shapes.
func TestCleanSummaryArchiveSkippedNoDanglingPath(t *testing.T) {
	tests := []struct {
		name   string
		result session.CleanResult
	}{
		{
			"fault route, branch kept",
			session.CleanResult{
				Name: "gone", Branch: "gone", WorktreePath: "/repo/wt-gone",
				Kind: session.KindWorktreeMissing, ArchiveSkipped: true,
			},
		},
		{
			"fault route, branch deleted",
			session.CleanResult{
				Name: "gone", Branch: "gone", WorktreePath: "/repo/wt-gone",
				Kind: session.KindWorktreeMissing, ArchiveSkipped: true,
				BranchDeleted: true, DeletedBranchSHA: "abc1234",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanSummary(false, tt.result)
			if strings.Contains(got, "archive: \n") || strings.HasSuffix(got, "archive: ") {
				t.Errorf("cleanSummary(ArchiveSkipped) = %q, prints an archive line with an empty path", got)
			}
			if !strings.Contains(got, "no archive") {
				t.Errorf("cleanSummary(ArchiveSkipped) = %q, never says the archive was skipped", got)
			}
			if !strings.Contains(got, "worktree: /repo/wt-gone") {
				t.Errorf("cleanSummary(ArchiveSkipped) = %q, missing the removed worktree", got)
			}
		})
	}
}

// TestCleanSummaryReportsRetargetedBranches pins the upstack-retarget notice:
// deleting a git-spice-tracked branch with --restack=none moves its upstack
// branches' parent pointer without rebasing them, CleanResult carries their
// names for exactly this summary, and dropping them here would leave the
// retarget invisible until the user's next restack surprises them.
func TestCleanSummaryReportsRetargetedBranches(t *testing.T) {
	got := cleanSummary(false, session.CleanResult{
		Name:               "sess",
		Branch:             "sess",
		ArchivePath:        "/archive/sess-2026-08-26",
		WorktreePath:       "/repo/wt-sess",
		BranchDeleted:      true,
		DeletedBranchSHA:   "abc1234",
		RetargetedBranches: "up-a, up-b",
	})
	if !strings.Contains(got, "up-a, up-b") {
		t.Errorf("cleanSummary(RetargetedBranches) = %q, never names the retargeted branches", got)
	}

	none := cleanSummary(false, session.CleanResult{
		Name: "sess", Branch: "sess", ArchivePath: "/a", WorktreePath: "/w",
		BranchDeleted: true, DeletedBranchSHA: "abc1234",
	})
	if strings.Contains(none, "retarget") {
		t.Errorf("cleanSummary(no retargets) = %q, mentions retargeting with nothing retargeted", none)
	}
}

// TestCleanSummaryStatusWithoutWorktree pins the kind-specific summary
// cleanStatusWithoutWorktree's result must get: the archive is kept, the
// session directory is what was removed, and — the load-bearing negative —
// it must never print "Removed:\n  worktree:", since that route's
// WorktreePath is the carrier and nothing about it was touched.
func TestCleanSummaryStatusWithoutWorktree(t *testing.T) {
	got := cleanSummary(false, session.CleanResult{
		Name:         "standalone",
		WorktreePath: "/repo",
		ArchivePath:  "/archive/repo-standalone-2026-08-26",
		Kind:         session.KindStatusWithoutWorktree,
	})
	for _, want := range []string{"archive: /archive/repo-standalone-2026-08-26", "session: .afk/standalone", "no worktree of its own"} {
		if !strings.Contains(got, want) {
			t.Errorf("cleanSummary(KindStatusWithoutWorktree) = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "Removed:\n  worktree:") {
		t.Errorf("cleanSummary(KindStatusWithoutWorktree) = %q, must never claim a worktree was removed", got)
	}
}

// TestForcedCleanPrompt pins the confirmation wording against the flags and
// kind that change what the clean will actually do: "the branch and archive
// are kept" is a lie when --delete-branch was also passed, and a user who
// confirms a force-clean on that promise loses a branch they were told would
// survive. For a KindStatusWithoutWorktree target, "This removes the
// worktree" is itself the lie — that route's WorktreePath is the carrier, and
// nothing about the worktree or branch changes — so it gets its own wording
// regardless of --delete-branch (which is refused earlier for this kind
// anyway; see deleteBranchNoOpRefused).
func TestForcedCleanPrompt(t *testing.T) {
	tests := []struct {
		name         string
		deleteBranch bool
		kind         session.Kind
		want         string
		wantAbsent   string
	}{
		{"without --delete-branch", false, session.KindHealthy, "the branch and archive are kept", "delete"},
		{"with --delete-branch", true, session.KindHealthy, "--delete-branch", "the branch and archive are kept"},
		{"status-without-worktree", false, session.KindStatusWithoutWorktree, "worktree and branch are untouched", "removes the worktree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forcedCleanPrompt("sess", "planning", tt.deleteBranch, tt.kind)
			if !strings.Contains(got, tt.want) {
				t.Errorf("forcedCleanPrompt(deleteBranch=%v, kind=%q) = %q, missing %q", tt.deleteBranch, tt.kind, got, tt.want)
			}
			if strings.Contains(got, tt.wantAbsent) {
				t.Errorf("forcedCleanPrompt(deleteBranch=%v, kind=%q) = %q, must not contain %q", tt.deleteBranch, tt.kind, got, tt.wantAbsent)
			}
			for _, base := range []string{"sess", "planning"} {
				if !strings.Contains(got, base) {
					t.Errorf("forcedCleanPrompt(deleteBranch=%v, kind=%q) = %q, missing %q", tt.deleteBranch, tt.kind, got, base)
				}
			}
		})
	}
}

// TestDeleteBranchNoOpRefused pins the kind-aware guard: --delete-branch
// errors on a scratch target or a KindStatusWithoutWorktree target regardless
// of anything else, because neither ever has a branch of its own (see
// session.Session.Scratch's doc comment, and KindStatusWithoutWorktree's,
// whose Branch Discover deliberately leaves empty) — there is nothing the
// flag could delete.
func TestDeleteBranchNoOpRefused(t *testing.T) {
	tests := []struct {
		name         string
		deleteBranch bool
		scratch      bool
		kind         session.Kind
		want         bool
	}{
		{"flag set, scratch", true, true, session.KindHealthy, true},
		{"flag set, status-without-worktree", true, false, session.KindStatusWithoutWorktree, true},
		{"flag set, planned", true, false, session.KindHealthy, false},
		{"flag unset, scratch", false, true, session.KindHealthy, false},
		{"flag unset, status-without-worktree", false, false, session.KindStatusWithoutWorktree, false},
		{"flag unset, planned", false, false, session.KindHealthy, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deleteBranchNoOpRefused(tt.deleteBranch, tt.scratch, tt.kind); got != tt.want {
				t.Errorf("deleteBranchNoOpRefused(deleteBranch=%v, scratch=%v, kind=%q) = %v, want %v",
					tt.deleteBranch, tt.scratch, tt.kind, got, tt.want)
			}
		})
	}
}

// TestCleanDeleteBranchDecisionMatrix is the acceptance criterion's "TTY/non-TTY x
// merged/unmerged matrix" for deleteBranchTTYRefused and
// shouldConfirmUnmergedBranchDelete together: a merged branch is never
// refused and never needs confirmation (it is simply deleted); an unmerged
// one is refused outright without a TTY and needs confirmation with one.
// Exactly one of {refused, needsConfirm, proceeds-unconditionally} holds for
// every cell, which is what makes a script (no TTY) unable to silently
// destroy unmerged work.
func TestCleanDeleteBranchDecisionMatrix(t *testing.T) {
	tests := []struct {
		name        string
		merged      bool
		tty         bool
		wantRefused bool
		wantConfirm bool
	}{
		{"merged, tty", true, true, false, false},
		{"merged, no tty", true, false, false, false},
		{"unmerged, tty", false, true, false, true},
		{"unmerged, no tty", false, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deleteBranchTTYRefused(tt.merged, tt.tty); got != tt.wantRefused {
				t.Errorf("deleteBranchTTYRefused(merged=%v, tty=%v) = %v, want %v",
					tt.merged, tt.tty, got, tt.wantRefused)
			}
			if got := shouldConfirmUnmergedBranchDelete(tt.merged, tt.tty); got != tt.wantConfirm {
				t.Errorf("shouldConfirmUnmergedBranchDelete(merged=%v, tty=%v) = %v, want %v",
					tt.merged, tt.tty, got, tt.wantConfirm)
			}
			if tt.wantRefused && tt.wantConfirm {
				t.Fatalf("test bug: row %q wants both refused and confirm", tt.name)
			}
		})
	}
}

// TestCleanFindTargetResolvesStatusWithoutWorktree asserts W3.T1's hardest
// constraint through the real classifier, not through a hand-built slice: a
// standalone `/afk:go` session (status-backed, carrier checked out on a
// different branch) must resolve to the CARRIER's WorktreePath, since that is
// what cleanStatusWithoutWorktree's zero-git teardown acts on, and must never
// be shadowed by a healthy session of a different name.
//
// The load-bearing part is that the fixture goes through session.Discover:
// findCleanTarget only ever searches the sessions slice it is handed, so the
// property is only real if Discover genuinely emits this shape as a
// KindStatusWithoutWorktree Session rather than filing it elsewhere.
func TestCleanFindTargetResolvesStatusWithoutWorktree(t *testing.T) {
	repo := newTestRepo(t)
	// A standalone `/afk:go` session: status.json for "standalone" living in
	// the PRIMARY checkout, whose branch is "main" — the name/branch mismatch
	// is exactly what makes Discover call it status-without-worktree.
	writeStatus(t, repo, "standalone")
	// A healthy session in the same repo, so the "healthy" assertion below
	// isn't vacuous.
	wt := addWorktree(t, repo, "healthy")
	writeStatus(t, wt, "healthy")
	// A second "dup" name shared by a healthy session and a stranded one, to
	// pin that the healthy entry still wins a shared name.
	dupWt := addWorktree(t, repo, "dup")
	writeStatus(t, dupWt, "dup")
	writeStatus(t, repo, "dup-status")

	sessions, err := session.Discover(repo, session.DefaultCleanDeps())
	if err != nil {
		t.Fatalf("Discover(%q): %v", repo, err)
	}

	standalone := findCleanTarget(sessions, "standalone")
	if standalone == nil {
		t.Fatalf("findCleanTarget(standalone) = nil, discovered %+v", sessions)
	}
	if standalone.Kind != session.KindStatusWithoutWorktree {
		t.Errorf("findCleanTarget(standalone).Kind = %q, want %q", standalone.Kind, session.KindStatusWithoutWorktree)
	}
	if standalone.WorktreePath != repo {
		t.Errorf("findCleanTarget(standalone).WorktreePath = %q, want the carrier %q", standalone.WorktreePath, repo)
	}

	healthy := findCleanTarget(sessions, "healthy")
	if healthy == nil || healthy.Kind != session.KindHealthy {
		t.Fatalf("findCleanTarget(healthy) = %+v, want the healthy session", healthy)
	}

	dup := findCleanTarget(sessions, "dup")
	if dup == nil || dup.Kind != session.KindHealthy || dup.WorktreePath != dupWt {
		t.Fatalf("findCleanTarget(dup) = %+v, want the healthy dup session at %q — a healthy session must beat a stranded one of the same name", dup, dupWt)
	}
}

// TestFindCleanTargetStrandedBeatsWorktreeFault is a hand-built-slice
// regression for the tier the real classifier cannot easily produce for one
// name at once: a stranded (KindStatusWithoutWorktree) entry and a
// worktree-side fault entry sharing a name must never coin-flip. The
// stranded one wins — it is the zero-git teardown — and the fault stays
// reachable only by naming it again once the stranded entry is gone (see
// sameNameRemaining, which is what tells the caller that).
func TestFindCleanTargetStrandedBeatsWorktreeFault(t *testing.T) {
	stranded := session.Session{Name: "dup", WorktreePath: "/repo", Kind: session.KindStatusWithoutWorktree}
	fault := session.Session{Name: "dup", Branch: "dup", WorktreePath: "/repo/wt-dup-fault", Kind: session.KindWorktreeNoSession}

	// Order the fault first in the slice so a bare first-match would pick it,
	// pinning that tiering — not slice order — decides the winner.
	got := findCleanTarget([]session.Session{fault, stranded}, "dup")
	if got == nil || got.Kind != session.KindStatusWithoutWorktree {
		t.Fatalf("findCleanTarget(dup) = %+v, want the stranded entry regardless of worktree-path order", got)
	}

	// And the reverse slice order must agree.
	got2 := findCleanTarget([]session.Session{stranded, fault}, "dup")
	if got2 == nil || got2.Kind != session.KindStatusWithoutWorktree {
		t.Fatalf("findCleanTarget(dup) [reversed order] = %+v, want the stranded entry", got2)
	}
}

// TestSameNameRemaining table-tests the pure helper cmdClean uses to tell the
// user that a second `afk clean <name>` will reach another entry: every
// OTHER session sharing target's name, in whatever order sessions holds them,
// and none that don't share the name (including target's own name compared
// against itself, which must be excluded).
func TestSameNameRemaining(t *testing.T) {
	target := session.Session{Name: "dup", WorktreePath: "/repo", Kind: session.KindStatusWithoutWorktree}
	fault := session.Session{Name: "dup", Branch: "dup", WorktreePath: "/repo/wt-dup-fault", Kind: session.KindWorktreeNoSession}
	other := session.Session{Name: "alpha", Branch: "alpha", WorktreePath: "/repo/wt-alpha"}

	tests := []struct {
		name     string
		sessions []session.Session
		want     []session.Session
	}{
		{"no other entries share the name", []session.Session{target, other}, nil},
		{"one other entry shares the name", []session.Session{target, fault, other}, []session.Session{fault}},
		{"target absent from the slice still finds sharers", []session.Session{fault, other}, []session.Session{fault}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sameNameRemaining(tt.sessions, target)
			if len(got) != len(tt.want) {
				t.Fatalf("sameNameRemaining = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("sameNameRemaining[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestCleanRefusedWorktreeFaultKinds is the acceptance criterion "naming a
// worktree-side fault entry cleans it without --force": both
// KindWorktreeMissing and KindWorktreeNoSession route through
// session.CleanableWithoutForce exactly like a scratch session — they have no
// phase to be terminal because their status.json is gone, not because work
// is in flight (see CleanableWithoutForce's doc).
func TestCleanRefusedWorktreeFaultKinds(t *testing.T) {
	for _, kind := range []session.Kind{session.KindWorktreeMissing, session.KindWorktreeNoSession} {
		t.Run(string(kind), func(t *testing.T) {
			sess := session.Session{Name: "broken", Branch: "broken", WorktreePath: "/repo/wt-broken", Kind: kind}
			if cleanRefused(false, sess) {
				t.Errorf("cleanRefused(force=false, kind=%s) = true, want a worktree-side fault to clean without --force", kind)
			}
		})
	}
}

// TestCleanRefusedStaleStatusLiveHost is the acceptance criterion "a
// corrupt-status entry with a LIVE host still requires --force":
// KindStaleStatus is NOT exempted by CleanableWithoutForce the way the two
// worktree-side kinds are — a corrupt status.json can still sit behind a
// mid-run conductor, so a live host.json for the same (worktree, name) must
// still gate it behind --force. host.WriteMeta with this test process's own
// PID gives host.Alive a real, live PID without a real afk host.
func TestCleanRefusedStaleStatusLiveHost(t *testing.T) {
	dir := t.TempDir()
	if err := host.WriteMeta(dir, "broken", host.Meta{
		PID:       os.Getpid(),
		Socket:    filepath.Join(t.TempDir(), "broken.sock"),
		StartedAt: "2026-07-09T21:14:03Z",
		Argv:      []string{"claude"},
	}); err != nil {
		t.Fatalf("host.WriteMeta: %v", err)
	}
	sess := session.Session{Name: "broken", WorktreePath: dir, Kind: session.KindStaleStatus}
	if !cleanRefused(false, sess) {
		t.Error("cleanRefused(force=false, KindStaleStatus, live host) = false, want a live host to still require --force")
	}
}

// TestCleanPhasePlainWorktreeFaultKind states the phase-column content for a
// fault-kind row: the dashboard's fault vocabulary in this listing's
// lowercase, distinguishing it from an ordinary session now that the row has
// moved out of the Orphans section (see cleanPhasePlain's doc). The raw Kind
// strings are NOT usable here — both overflow printSessionsPlain's
// `phase=%-10s` field and shift every later column on exactly those rows, so
// the length assertion is load-bearing, not decoration. KindStaleStatus keeps
// the pre-existing blank behavior TestCleanPhasePlain already pins.
func TestCleanPhasePlainWorktreeFaultKind(t *testing.T) {
	tests := []struct {
		kind session.Kind
		want string
	}{
		{session.KindWorktreeMissing, "broken"},
		{session.KindWorktreeNoSession, "orphan"},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			sess := session.Session{Name: "broken", Kind: tt.kind}
			got := cleanPhasePlain(sess)
			if got != tt.want {
				t.Errorf("cleanPhasePlain(kind=%s) = %q, want %q", tt.kind, got, tt.want)
			}
			if len(got) > 10 {
				t.Errorf("cleanPhasePlain(kind=%s) = %q (%d chars), overflows the %%-10s phase column",
					tt.kind, got, len(got))
			}
		})
	}
}

// TestCleanPrintSessionsPlainFaultKindOnce is the acceptance criterion "a broken
// entry appearing exactly once in the plain listing": a worktree-side fault
// session (as session.Discover emits it, in the sessions slice) must print
// exactly once, under Sessions, carrying its Kind in the phase column. There
// is no Orphans section left for it to also appear under — printSessions no
// longer takes an orphans slice at all.
func TestCleanPrintSessionsPlainFaultKindOnce(t *testing.T) {
	sess := session.Session{
		Name: "broken", Branch: "broken", WorktreePath: "/repo/wt-broken",
		Kind: session.KindWorktreeNoSession, Reason: "worktree has no status file",
	}

	got := captureStdout(t, func() { printSessionsPlain([]session.Session{sess}) })

	// "broken" alone recurs (name, branch, worktree path columns all echo
	// it), so the row itself — not the substring — is what must appear
	// exactly once: its unique marker is the fault label in the phase column.
	if n := strings.Count(got, "phase=orphan"); n != 1 {
		t.Errorf("printSessionsPlain(fault-kind) printed the row %d times, want exactly once: %q", n, got)
	}
	if n := strings.Count(got, "Sessions:"); n != 1 {
		t.Errorf("printSessionsPlain(fault-kind) printed %q %d times, want exactly once: %q", "Sessions:", n, got)
	}
	if strings.Contains(got, "Orphans:") {
		t.Errorf("printSessionsPlain(fault-kind) = %q, must never print an Orphans section", got)
	}
}

// TestCleanPrintSessionsPlainFaultRowsAlign pins the column geometry the
// plain listing's byte-stability promise implies: the fault labels must fit
// the `phase=%-10s` field, or the branch= and worktree= columns shift on
// exactly the fault rows — the raw Kind strings (19 and 21 chars) did exactly
// that. Asserted per fault kind against a healthy row, on the byte offset of
// "branch=" itself rather than on any label constant, so the test survives a
// relabel but not a re-overflow.
func TestCleanPrintSessionsPlainFaultRowsAlign(t *testing.T) {
	healthy := session.Session{Name: "ok", Branch: "ok", WorktreePath: "/repo/wt-ok"}
	healthy.Status.Phase = "done"
	faults := []session.Session{
		{Name: "gone", Branch: "gone", WorktreePath: "/repo/wt-gone", Kind: session.KindWorktreeMissing},
		{Name: "stray", Branch: "stray", WorktreePath: "/repo/wt-stray", Kind: session.KindWorktreeNoSession},
	}

	got := captureStdout(t, func() {
		printSessionsPlain(append([]session.Session{healthy}, faults...))
	})

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("printSessionsPlain printed %d lines, want 4 (header + 3 rows): %q", len(lines), got)
	}
	wantCol := strings.Index(lines[1], "branch=")
	if wantCol < 0 {
		t.Fatalf("healthy row %q has no branch= column", lines[1])
	}
	for _, row := range lines[2:] {
		if col := strings.Index(row, "branch="); col != wantCol {
			t.Errorf("fault row %q puts branch= at byte %d, healthy row puts it at %d — the phase column overflowed",
				row, col, wantCol)
		}
	}
}

// TestCleanProjectFlagValidation drives resolveAndValidateProject — the
// function cmdClean itself calls between flag parse and session.Discover,
// and the one place the only-when---project guard lives — through a real
// addProjectFlag parse. It deliberately does NOT reconstruct the guard
// test-side: the previous version did, and stayed green under a mutation
// that disabled the production guard outright. The load-bearing rows are
// the two bad-path ones — cmdClean prints the returned error and exits 1,
// so a non-nil, path-naming, `afk clean:`-attributed error here IS the
// exit-non-zero acceptance criterion — and the bare row, whose cwd
// deliberately has NO .git entry: if validation ever runs without an
// explicit --project, that row goes red, pinning the contract that a bare
// `afk clean` from a repo subdirectory keeps working exactly as before
// the flag existed.
func TestCleanProjectFlagValidation(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo/.git: %v", err)
	}
	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("mkdir plain: %v", err)
	}
	missing := filepath.Join(base, "missing")
	cwd := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	tests := []struct {
		name     string
		args     []string
		wantPath string // the resolved repo on success, the path the error must name on failure
		wantErr  string // substring; "" means the pipeline succeeds
	}{
		{"--project at a git repo -> accepted", []string{"--project", repo}, repo, ""},
		{"--project at a non-repo directory -> rejected", []string{"--project", plain}, plain, "not a git repository"},
		{"--project at a missing path -> rejected", []string{"--project", missing}, missing, "does not exist"},
		{"bare invocation -> cwd used unvalidated, even without .git", nil, cwd, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("clean", flag.ContinueOnError)
			project := addProjectFlag(fs)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatalf("Parse(%v): %v", tt.args, err)
			}
			gotRepo, err := resolveAndValidateProject("clean", rawProjectFlag(*project), cwd, base)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("resolveAndValidateProject: %v, want nil", err)
				}
				if gotRepo != tt.wantPath {
					t.Fatalf("repo = %q, want %q", gotRepo, tt.wantPath)
				}
				return
			}
			if err == nil {
				t.Fatalf("resolveAndValidateProject = %q, nil — want error containing %q", gotRepo, tt.wantErr)
			}
			for _, want := range []string{"afk clean:", tt.wantErr, tt.wantPath} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, missing %q", err, want)
				}
			}
		})
	}
}

// TestCleanProjectFlagListsThatRepoSessions covers the acceptance criterion
// that `afk clean --project <path>` lists THAT repo's sessions: the
// --project value is resolved with a cwd that is a different, session-less
// directory, and session.Discover — fed the resolved path exactly as
// cmdClean feeds it — must return the target repo's session anyway. Running
// the same discovery against the cwd instead is the negative control: it
// must find nothing, or this test could pass by accident of discovery
// ignoring its argument.
func TestCleanProjectFlagListsThatRepoSessions(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "sess-one")
	writeStatus(t, wt, "sess-one")
	elsewhere := newTestRepo(t)

	resolved, err := resolveProject(repo, elsewhere, t.TempDir())
	if err != nil {
		t.Fatalf("resolveProject: %v", err)
	}
	if resolved != repo {
		t.Fatalf("resolved = %q, want %q", resolved, repo)
	}

	sessions, err := session.Discover(resolved, session.DefaultCleanDeps())
	if err != nil {
		t.Fatalf("Discover(%q): %v", resolved, err)
	}
	if len(sessions) != 1 || sessions[0].Name != "sess-one" {
		t.Fatalf("Discover(%q) sessions = %+v, want exactly [sess-one]", resolved, sessions)
	}

	control, err := session.Discover(elsewhere, session.DefaultCleanDeps())
	if err != nil {
		t.Fatalf("Discover(%q): %v", elsewhere, err)
	}
	if len(control) != 0 {
		t.Fatalf("Discover(cwd repo) sessions = %+v, want none", control)
	}
}

// TestCleanPhasePlain pins the phase column `afk clean` prints for a
// non-TTY stdout. A scratch session has no status.json and therefore no phase,
// which printed the field blank — the one session kind whose row said nothing
// about what it was. It now says `scratch`, the same word the dashboard's
// badge uses (internal/ui's scratchPhaseKey), so the two surfaces name the
// kind identically.
//
// Load-bearing rows: the scratch row (the fix) and the "stale planned session"
// row, whose phase is ALSO empty and must still print blank — this format is
// the machine-readable fallback scripts parse, so the fix has to key on
// Scratch and not on the empty phase string the two shapes share.
func TestCleanPhasePlain(t *testing.T) {
	tests := []struct {
		name    string
		phase   string
		scratch bool
		want    string
	}{
		{"scratch", "", true, "scratch"},
		{"stale planned session stays blank", "", false, ""},
		{"planning", "planning", false, "planning"},
		{"executing", "executing", false, "executing"},
		{"reviewing", "reviewing", false, "reviewing"},
		{"fixing", "fixing", false, "fixing"},
		{"done", "done", false, "done"},
		{"failed", "failed", false, "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanPhasePlain(cleanTestSession(tt.phase, tt.scratch))
			if got != tt.want {
				t.Errorf("cleanPhasePlain(phase=%q, scratch=%v) = %q, want %q",
					tt.phase, tt.scratch, got, tt.want)
			}
		})
	}
}

// TestRenderPhaseBadgeScratch covers the styled half of the same fix. The
// badge fell through to the neutral `unknown` fallback for a scratch session,
// which is the literal word this feature set out to never show for a session
// kind that is perfectly well known.
//
// Load-bearing rows: the scratch row must read `scratch` and must NOT read
// `unknown`; the two fault rows must carry cleanPhasePlain's own labels and
// NOT `unknown` either (the fault is known exactly — "unknown" is a corrupt
// status file's verdict, a different problem with a different fix — and the
// plain listing already says so, so the two renderings must not disagree
// about the same row); and the empty-phase non-scratch row must still read
// `unknown`, since a stale status file genuinely is an unknown phase. The six
// lifecycle rows are the regression guard that this changed nothing for
// planned sessions.
func TestRenderPhaseBadgeScratch(t *testing.T) {
	tests := []struct {
		name    string
		phase   string
		scratch bool
		kind    session.Kind
		want    string
	}{
		{"scratch", "", true, session.KindHealthy, "scratch"},
		{"stale planned session", "", false, session.KindHealthy, "unknown"},
		{"worktree directory missing", "", false, session.KindWorktreeMissing, "broken"},
		{"worktree without a session", "", false, session.KindWorktreeNoSession, "orphan"},
		{"planning", "planning", false, session.KindHealthy, "planning"},
		{"executing", "executing", false, session.KindHealthy, "executing"},
		{"reviewing", "reviewing", false, session.KindHealthy, "reviewing"},
		{"fixing", "fixing", false, session.KindHealthy, "fixing"},
		{"done", "done", false, session.KindHealthy, "done"},
		{"failed", "failed", false, session.KindHealthy, "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := cleanTestSession(tt.phase, tt.scratch)
			sess.Kind = tt.kind
			got := renderPhaseBadge(sess)
			if !strings.Contains(got, tt.want) {
				t.Errorf("renderPhaseBadge(scratch=%v, kind=%q, phase=%q) = %q, want it to contain %q",
					tt.scratch, tt.kind, tt.phase, got, tt.want)
			}
			if tt.want != "unknown" && strings.Contains(got, "unknown") {
				t.Errorf("renderPhaseBadge(scratch=%v, kind=%q, phase=%q) = %q, must not say \"unknown\"",
					tt.scratch, tt.kind, tt.phase, got)
			}
		})
	}
}

// TestFindCleanTarget pins how `afk clean <name>` picks its session out of
// what session.Discover returned: the first entry with that name, which is
// only a safe rule because Discover's order is a total one (see
// session.sortSessions).
//
// The load-bearing rows are the name collisions. "Scratch and planned" is
// reachable — a `.afk/dup/` scratch directory in the main checkout alongside
// a worktree on branch `dup` — and the two resolve to different removal paths,
// one of which runs `git worktree remove`. The row feeds the slice in
// Discover's own order and asserts the status-backed entry wins, so the CLI
// and the ordering guarantee are pinned against each other rather than each
// asserting its own half.
//
// The two worktree-fault collision rows pin the CLI half of "scratch wins over
// every fault": sortSessions orders a non-scratch fault entry FIRST for a
// shared name, so a first-match-by-name resolution would hand `afk clean
// dup` the fault teardown — terminating the scratch host and removing a
// worktree, unarchived, where a bare clean used to remove only `.afk/dup/`.
// The fault entry must lose to ANY non-fault entry of the same name and stay
// reachable only when the name is unambiguous.
func TestFindCleanTarget(t *testing.T) {
	planned := session.Session{Name: "dup", Branch: "dup", WorktreePath: "/repo/wt-dup"}
	planned.Status.Phase = "done"
	scratchDup := session.Session{Name: "dup", Scratch: true, WorktreePath: "/repo"}
	faultDup := session.Session{
		Name: "dup", Branch: "dup", WorktreePath: "/repo/wt-dup-fault",
		Kind: session.KindWorktreeNoSession,
	}
	other := session.Session{Name: "alpha", Branch: "alpha", WorktreePath: "/repo/wt-alpha"}

	tests := []struct {
		name        string
		sessions    []session.Session
		target      string
		wantFound   bool
		wantScratch bool
		wantWt      string
	}{
		{"single match", []session.Session{other, planned}, "alpha", true, false, "/repo/wt-alpha"},
		{"scratch match", []session.Session{other, scratchDup}, "dup", true, true, "/repo"},
		{
			"name shared by a scratch and a planned session",
			[]session.Session{other, planned, scratchDup},
			"dup", true, false, "/repo/wt-dup",
		},
		// sortSessions orders the non-scratch fault entry first; the scratch
		// must still win. This is the collision that, resolved by bare
		// first-match, deletes a worktree the user never named.
		{
			"name shared by a scratch and a worktree-fault entry",
			[]session.Session{other, faultDup, scratchDup},
			"dup", true, true, "/repo",
		},
		{
			"name shared by a planned session and a worktree-fault entry",
			[]session.Session{other, planned, faultDup},
			"dup", true, false, "/repo/wt-dup",
		},
		{
			"worktree-fault entry with an unambiguous name stays nameable",
			[]session.Session{other, faultDup},
			"dup", true, false, "/repo/wt-dup-fault",
		},
		{"no match", []session.Session{other, planned}, "nope", false, false, ""},
		{"empty", nil, "alpha", false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findCleanTarget(tt.sessions, tt.target)
			if !tt.wantFound {
				if got != nil {
					t.Fatalf("findCleanTarget(%q) = %+v, want nil", tt.target, *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("findCleanTarget(%q) = nil, want a session", tt.target)
			}
			if got.Name != tt.target || got.Scratch != tt.wantScratch || got.WorktreePath != tt.wantWt {
				t.Errorf("findCleanTarget(%q) = {Name:%q Scratch:%v WorktreePath:%q}, want {Name:%q Scratch:%v WorktreePath:%q}",
					tt.target, got.Name, got.Scratch, got.WorktreePath, tt.target, tt.wantScratch, tt.wantWt)
			}
		})
	}
}

// TestFindCleanTargetAgreesWithDiscoverOrder is the end-to-end tie between
// the two: a real repo holding both a scratch `.afk/dup/` and a worktree on
// branch `dup`, discovered through session.Discover exactly as cmdClean does,
// then resolved by name. Whatever order the worktree scan produces, the answer
// is the status-backed session — the CLI never routes `afk clean dup` into
// the wrong removal path.
func TestFindCleanTargetAgreesWithDiscoverOrder(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "dup")
	writeStatus(t, wt, "dup")
	writeScratchHost(t, repo, "dup")
	writeScratchHost(t, repo, "scratch-1")

	sessions, err := session.Discover(repo, session.DefaultCleanDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	dup := findCleanTarget(sessions, "dup")
	if dup == nil {
		t.Fatalf("findCleanTarget(dup) = nil, discovered %+v", sessions)
	}
	if dup.Scratch || dup.WorktreePath != wt {
		t.Errorf("findCleanTarget(dup) = {Scratch:%v WorktreePath:%q}, want the status-backed session at %q",
			dup.Scratch, dup.WorktreePath, wt)
	}

	one := findCleanTarget(sessions, "scratch-1")
	if one == nil {
		t.Fatalf("findCleanTarget(scratch-1) = nil, discovered %+v", sessions)
	}
	if !one.Scratch {
		t.Errorf("findCleanTarget(scratch-1) = %+v, want the scratch session", *one)
	}
	if cleanRefused(false, *one) {
		t.Error("cleanRefused(force=false, scratch) = true, want a scratch session to clean without --force")
	}
}

// TestFindCleanTargetScratchWinsOverWorktreeFault reproduces the
// scratch-vs-fault name collision on disk, end to end through
// session.Discover exactly as cmdClean sees it: a scratch `.afk/dup/` in
// the main checkout, plus a worktree checked out on branch `dup` that carries
// no afk session of its own (a KindWorktreeNoSession fault, since a scratch
// directory deliberately does not count as matchedOwnBranch). sortSessions
// orders the non-scratch fault entry first, so a first-match-by-name
// resolution hands `afk clean dup` the fault teardown — which terminates
// the live scratch host and removes the worktree, unarchived, where a bare
// clean has always removed only `.afk/dup/`. Scratch must win over every
// fault (plan Context), and a bare `afk clean <name>`'s behavior is a
// W3.T1 Must-NOT.
func TestFindCleanTargetScratchWinsOverWorktreeFault(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "dup")
	writeScratchHost(t, repo, "dup")

	sessions, err := session.Discover(repo, session.DefaultCleanDeps())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	var kinds []session.Kind
	for _, s := range sessions {
		kinds = append(kinds, s.Kind)
	}
	// The fixture must actually produce the collision — a fault entry AND a
	// scratch entry both named dup — or the resolution assertion below tests
	// nothing.
	fault := false
	scratch := false
	for _, s := range sessions {
		if s.Name != "dup" {
			continue
		}
		if s.Scratch {
			scratch = true
		}
		if s.Kind == session.KindWorktreeNoSession {
			fault = true
		}
	}
	if !fault || !scratch {
		t.Fatalf("fixture did not produce the collision (fault=%v scratch=%v), discovered kinds %v: %+v",
			fault, scratch, kinds, sessions)
	}

	got := findCleanTarget(sessions, "dup")
	if got == nil {
		t.Fatalf("findCleanTarget(dup) = nil, discovered %+v", sessions)
	}
	if !got.Scratch {
		t.Errorf("findCleanTarget(dup) = {Scratch:%v Kind:%q WorktreePath:%q}, want the scratch session — resolving to the fault entry would remove the worktree at %q",
			got.Scratch, got.Kind, got.WorktreePath, wt)
	}
}

// writeScratchHost seeds a `.afk/{name}/host.json` with no status.json
// beside it — the on-disk shape `afk new --scratch` leaves behind, and the
// only thing that makes a directory a scratch session.
func writeScratchHost(t *testing.T, worktree, name string) {
	t.Helper()
	if err := host.WriteMeta(worktree, name, host.Meta{
		PID:       999999,
		Socket:    filepath.Join(t.TempDir(), name+".sock"),
		StartedAt: "2026-07-09T21:14:03Z",
		Argv:      []string{"claude"},
	}); err != nil {
		t.Fatalf("host.WriteMeta: %v", err)
	}
}

// TestCleanWorktreeColumnScratch is the regression for the W4.T3 finding
// that the pre-clean listing printed worktree={repo root} for a scratch
// session — the exact misleading shape cleanSummary was already fixed for
// (TestCleanSummaryScratchNamesNoEmptyField), left unfixed here. A scratch
// session's WorktreePath holds the repo root it was created in (see
// session.Session.Scratch's doc comment), not a worktree afk clean will
// ever remove, so the listing must say so instead of printing that path.
func TestCleanWorktreeColumnScratch(t *testing.T) {
	scratch := session.Session{Name: "scratch-1", Scratch: true, WorktreePath: "/Users/dev/myrepo"}
	if got := cleanWorktreeColumn(scratch); got == scratch.WorktreePath {
		t.Errorf("cleanWorktreeColumn(scratch) = %q, must not be the raw repo-root WorktreePath", got)
	} else if got != scratchWorktreeLabel {
		t.Errorf("cleanWorktreeColumn(scratch) = %q, want %q", got, scratchWorktreeLabel)
	}

	planned := session.Session{Name: "sess", WorktreePath: "/repo/wt-sess"}
	if got := cleanWorktreeColumn(planned); got != planned.WorktreePath {
		t.Errorf("cleanWorktreeColumn(planned) = %q, want %q (unchanged)", got, planned.WorktreePath)
	}
}

// TestCleanWorktreeColumnStatusWithoutWorktree is the same fix applied to the
// other kind whose WorktreePath is not something afk clean will remove: for
// KindStatusWithoutWorktree, WorktreePath is the CARRIER (often the user's own
// primary checkout), so printing it verbatim reads as "this will be removed"
// exactly as the scratch case did — and must not reuse scratchWorktreeLabel,
// since "one-off" is a scratch-specific word that would misdescribe this kind.
func TestCleanWorktreeColumnStatusWithoutWorktree(t *testing.T) {
	standalone := session.Session{Name: "standalone", WorktreePath: "/repo", Kind: session.KindStatusWithoutWorktree}
	got := cleanWorktreeColumn(standalone)
	if got == standalone.WorktreePath {
		t.Errorf("cleanWorktreeColumn(status-without-worktree) = %q, must not be the raw carrier WorktreePath", got)
	}
	if got != statusOnlyWorktreeLabel {
		t.Errorf("cleanWorktreeColumn(status-without-worktree) = %q, want %q", got, statusOnlyWorktreeLabel)
	}
	if got == scratchWorktreeLabel {
		t.Errorf("cleanWorktreeColumn(status-without-worktree) = %q, must not reuse the scratch label", got)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything fn wrote to it. printSessionsPlain/printSessionsStyled write
// straight to os.Stdout rather than an injected writer, so this is the only
// way to assert on what they actually print without changing either
// signature.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading pipe: %v", err)
	}
	return string(out)
}

// TestPrintSessionsScratchWorktreeColumn covers both printSessions renderings
// end to end: neither may leak a scratch session's repo-root WorktreePath
// into the listing's worktree column. cleanWorktreeColumn (see
// TestCleanWorktreeColumnScratch) is unit-tested on its own, but this is what
// actually catches a call site that bypasses it — the exact way the original
// bug survived cleanSummary's fix once already.
func TestPrintSessionsScratchWorktreeColumn(t *testing.T) {
	sess := session.Session{Name: "scratch-1", Scratch: true, WorktreePath: "/Users/dev/myrepo"}

	plain := captureStdout(t, func() { printSessionsPlain([]session.Session{sess}) })
	if strings.Contains(plain, sess.WorktreePath) {
		t.Errorf("printSessionsPlain(scratch) = %q, leaked the repo-root WorktreePath into the worktree column", plain)
	}
	if !strings.Contains(plain, scratchWorktreeLabel) {
		t.Errorf("printSessionsPlain(scratch) = %q, want it to contain %q", plain, scratchWorktreeLabel)
	}

	styled := captureStdout(t, func() { printSessionsStyled([]session.Session{sess}) })
	if strings.Contains(styled, sess.WorktreePath) {
		t.Errorf("printSessionsStyled(scratch) = %q, leaked the repo-root WorktreePath into the worktree column", styled)
	}
	if !strings.Contains(styled, scratchWorktreeLabel) {
		t.Errorf("printSessionsStyled(scratch) = %q, want it to contain %q", styled, scratchWorktreeLabel)
	}
}

// scratchPhaseIdentity is what this package's scratch rendering must still
// share with internal/ui's copy: the phase key, the word both surfaces show
// for a one-off session.
//
// It used to carry the scratch badge's (fg, bg) colour pair as well. T16.1
// gave the dashboard its own colour themes (internal/palette's Theme, chosen
// by [dash] theme), so internal/ui now builds its scratch badge from theme
// tokens while afk clean, a terminal prompt, keeps palette.Scheme's colours
// unchanged (T16.1 A1/A2: Scheme stays for prompt, clean and install). The two
// surfaces no longer share a colour vocabulary, so a colour comparison could
// only fail or be rigged to pass; the word is what an owner reads across both
// and is what still has to agree.
type scratchPhaseIdentity struct {
	key string
}

// parseUIScratchPhaseIdentity extracts internal/ui's scratchPhaseKey literal
// by parsing internal/ui/styles.go directly rather than importing it.
//
// scratchPhaseKey is unexported by design (see its doc comment there), so no
// shared Go symbol is reachable from this package. Static parsing is what
// W4.T3's fix instruction asked for when package boundaries genuinely prevent
// a single source of truth: a test that fails when the two copies drift apart.
func parseUIScratchPhaseIdentity(t *testing.T) scratchPhaseIdentity {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	srcPath := filepath.Join(filepath.Dir(thisFile), "internal", "ui", "styles.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", srcPath, err)
	}

	var id scratchPhaseIdentity
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if name.Name == "scratchPhaseKey" && i < len(spec.Values) {
				if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						id.key = v
					}
				}
			}
		}
		return true
	})

	if id.key == "" {
		t.Fatalf("%s: scratchPhaseKey constant not found", srcPath)
	}
	return id
}

// readRepoFile reads a file at path relative to the repo root (the directory
// this test file lives in), for assertions against checked-in prose like
// README.md that has no importable symbol to test against directly.
func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// userDocs is every page of the user documentation. README.md was split into
// docs/ (2026-10-17), so a guard on the documented wording must read all of
// them: a stale sentence moved into docs/reference.md would otherwise pass a
// check that reads README.md alone.
var userDocs = []string{
	"README.md",
	"docs/install.md",
	"docs/web.md",
	"docs/dashboard.md",
	"docs/daemon.md",
	"docs/configuration.md",
	"docs/reference.md",
	"docs/development.md",
}

// readUserDocs returns every page of userDocs joined, for assertions about
// what the documentation says wherever it now says it.
func readUserDocs(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, name := range userDocs {
		b.WriteString(readRepoFile(t, name))
		b.WriteString("\n")
	}
	return b.String()
}

// TestReadmeDoesNotClaimSocketLivesInAfkDir is the regression for the
// W4.T2 finding that README.md:208 and :347 claimed a scratch session's
// socket sits inside `.afk/{name}/` alongside host.json and host.log. It
// actually lives under os.TempDir()/afk-{uid}/ — entirely outside
// .afk/{name}/ — per internal/host.SocketPath. This both rejects the exact
// false sentences and ties the corrected claim to what SocketPath actually
// computes, so it fails if either the old wording returns or the documented
// directory ever stops matching the real one.
func TestReadmeDoesNotClaimSocketLivesInAfkDir(t *testing.T) {
	readme := readUserDocs(t)

	for _, bad := range []string{
		"`host.json`, `host.log`, and the socket are present",
		"containing `host.json`, `host.log`, and a socket",
	} {
		if strings.Contains(readme, bad) {
			t.Errorf("the docs still claim the socket lives in .afk/{name}/ alongside host.json/host.log: contains %q", bad)
		}
	}

	socketPath, err := host.SocketPath(t.TempDir(), "scratch-1")
	if err != nil {
		t.Fatalf("host.SocketPath: %v", err)
	}
	socketDir := filepath.Base(filepath.Dir(socketPath))
	if !strings.HasPrefix(socketDir, "afk-") {
		t.Fatalf("host.SocketPath's directory = %q, want a afk-<uid> prefix — README's os.TempDir()/afk-{uid}/ claim would name the wrong thing", socketDir)
	}
	if !strings.Contains(readme, "os.TempDir()/afk-{uid}/") {
		t.Errorf("the docs do not document the socket's actual location (os.TempDir()/afk-{uid}/, matching host.SocketPath)")
	}
}

// TestReadmeDoesNotClaimCKeyTreatsBothKindsIdentically is the regression for
// the W4.T2 finding that README.md's "The dashboard's `c` key applies the
// same logic to both kinds with no distinction" went stale once a later fix
// pass gave scratch sessions their own confirmation copy (internal/ui's
// confirmCleanScratchHint, distinct from confirmCleanHint). W3.T2 (this plan)
// widened that further to a THIRD kind-specific hint
// (confirmCleanKeyHint for KindStatusWithoutWorktree, see decisions.md), so
// the assertion now checks for the general "kind-specific" claim rather than
// the scratch-only wording it started with.
func TestReadmeDoesNotClaimCKeyTreatsBothKindsIdentically(t *testing.T) {
	readme := readUserDocs(t)
	if strings.Contains(readme, "applies the same logic to both kinds with no distinction") {
		t.Error("the docs still claim the dashboard's c key applies identical logic to both session kinds, which is stale now that scratch sessions get their own confirmation copy")
	}
	if !strings.Contains(readme, "kind-specific confirmation") {
		t.Error("the docs do not document that the c key gives each session kind its own confirmation copy")
	}
}

// TestScratchPhaseIdentityMatchesInternalUI is the W4.T3 drift guard: root
// clean.go's scratchPhaseLabel was hand-copied from internal/ui's unexported
// scratchPhaseKey, with nothing else enforcing the two stay in sync. This
// parses internal/ui/styles.go and compares the literal directly, so an edit
// to either side's scratch word fails this test instead of silently drifting.
//
// It no longer compares the badge colours: since T16.1 the dashboard takes
// its colours from its theme and afk clean keeps palette.Scheme's, on purpose
// (scratchPhaseIdentity's doc says why).
func TestScratchPhaseIdentityMatchesInternalUI(t *testing.T) {
	ui := parseUIScratchPhaseIdentity(t)
	if scratchPhaseLabel != ui.key {
		t.Errorf("scratchPhaseLabel = %q, internal/ui's scratchPhaseKey = %q — must name the scratch phase identically",
			scratchPhaseLabel, ui.key)
	}
}
