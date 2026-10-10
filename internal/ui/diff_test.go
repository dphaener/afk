package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/discover"
)

// resolveQualified is the fake ResolveBaseRef for tests that do not care
// about the fallback: every base is a live local branch.
func resolveQualified(_, base string) (string, bool) { return "refs/heads/" + base, true }

// TestComputeDiffStats_FocusedUsesFullDiffOnly verifies the dual-cost pattern:
// a focused session (the currently-selected one) uses the full git diff to
// count changed lines.
func TestComputeDiffStats_FocusedUsesFullDiffOnly(t *testing.T) {
	fullCalled := false
	full := func(dir, base string) (string, error) {
		fullCalled = true
		if dir != "/wt" || base != "main" {
			t.Fatalf("fullDiff(%q, %q)", dir, base)
		}
		return "diff --git a/x b/x\n+added\n-removed\n", nil
	}
	numstat := func(string, string) (string, error) {
		t.Fatalf("numstatDiff must not be called for the focused session")
		return "", nil
	}
	got, _ := computeDiffStats(full, numstat, "/wt", "main", true, false)
	if !fullCalled {
		t.Fatalf("fullDiff was not called")
	}
	want := DiffStats{Added: 1, Removed: 1, Files: 1}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestComputeDiffStats_UnfocusedUsesNumstatOnly verifies the dual-cost
// pattern: an unfocused session uses the cheap --numstat aggregate and NEVER
// calls fullDiff, regardless of retainText — there is no full diff output on
// that branch for retainText to retain.
func TestComputeDiffStats_UnfocusedUsesNumstatOnly(t *testing.T) {
	for _, retainText := range []bool{false, true} {
		t.Run(fmt.Sprintf("retainText=%v", retainText), func(t *testing.T) {
			full := func(string, string) (string, error) {
				t.Fatalf("fullDiff must NEVER be called for an unfocused session")
				return "", nil
			}
			numstatCalled := false
			numstat := func(dir, base string) (string, error) {
				numstatCalled = true
				return "3\t1\tfile.go\n", nil
			}
			got, text := computeDiffStats(full, numstat, "/wt", "main", false, retainText)
			if !numstatCalled {
				t.Fatalf("numstatDiff was not called")
			}
			want := DiffStats{Added: 3, Removed: 1, Files: 1}
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			if len(text) != 0 {
				t.Fatalf("unfocused session must never return retained text, got %v", text)
			}
		})
	}
}

// TestComputeDiffStats_FullDiffError verifies that computeDiffStats surfaces
// errors from fullDiff.
func TestComputeDiffStats_FullDiffError(t *testing.T) {
	full := func(string, string) (string, error) { return "", errors.New("git diff failed") }
	numstat := func(string, string) (string, error) { return "", nil }
	got, _ := computeDiffStats(full, numstat, "/wt", "main", true, false)
	if got.Err == "" {
		t.Fatalf("expected Err to be set")
	}
}

// TestComputeDiffStats_RetainTextGating verifies the focused branch: text is
// retained only when retainText is set, a diff at exactly the cap is
// returned whole with no marker, and a diff past the cap is truncated to the
// cap plus the marker line — while Added/Removed/Files, computed from the
// UNCAPPED output, always reflect the whole diff.
func TestComputeDiffStats_RetainTextGating(t *testing.T) {
	numstat := func(string, string) (string, error) { return "", nil }
	diffOfNLines := func(n int) string {
		lines := make([]string, 0, n+1)
		lines = append(lines, "diff --git a/x b/x")
		for i := 1; i < n; i++ {
			lines = append(lines, fmt.Sprintf("+line%d", i))
		}
		return strings.Join(lines, "\n") + "\n"
	}

	t.Run("retainText false returns no text even when focused", func(t *testing.T) {
		full := func(string, string) (string, error) { return diffOfNLines(5), nil }
		got, text := computeDiffStats(full, numstat, "/wt", "main", true, false)
		if len(text) != 0 {
			t.Fatalf("retainText=false: got text %v, want none", text)
		}
		if got.Files != 1 {
			t.Fatalf("got %+v, want Files=1", got)
		}
	})

	t.Run("exactly the cap: no marker", func(t *testing.T) {
		full := func(string, string) (string, error) { return diffOfNLines(diffLineCap), nil }
		_, text := computeDiffStats(full, numstat, "/wt", "main", true, true)
		if len(text) != diffLineCap {
			t.Fatalf("got %d lines, want exactly %d", len(text), diffLineCap)
		}
		if text[len(text)-1] == diffTruncationMarker {
			t.Fatalf("an exactly-cap diff must not be marked truncated")
		}
	})

	t.Run("past the cap: capped plus marker", func(t *testing.T) {
		full := func(string, string) (string, error) { return diffOfNLines(diffLineCap + 500), nil }
		stats, text := computeDiffStats(full, numstat, "/wt", "main", true, true)
		if len(text) != diffLineCap+1 {
			t.Fatalf("got %d lines, want %d (cap + marker)", len(text), diffLineCap+1)
		}
		if text[len(text)-1] != diffTruncationMarker {
			t.Fatalf("last line = %q, want the truncation marker", text[len(text)-1])
		}
		if stats.Added != diffLineCap+499 {
			t.Fatalf("Added = %d, want the UNCAPPED count", stats.Added)
		}
	})
}

// TestParseFullDiff verifies that parseFullDiff correctly counts added/removed
// lines and files from a full git diff output.
func TestParseFullDiff(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want DiffStats
	}{
		{
			name: "single file, adds and removes",
			in: strings.Join([]string{
				"diff --git a/foo.go b/foo.go",
				"index abc..def 100644",
				"--- a/foo.go",
				"+++ b/foo.go",
				"@@ -1,2 +1,2 @@",
				"-old line",
				"+new line",
				"+another new line",
				"",
			}, "\n"),
			want: DiffStats{Added: 2, Removed: 1, Files: 1},
		},
		{
			name: "two files",
			in: strings.Join([]string{
				"diff --git a/a.go b/a.go",
				"--- a/a.go",
				"+++ b/a.go",
				"+one",
				"diff --git a/b.go b/b.go",
				"--- a/b.go",
				"+++ b/b.go",
				"-two",
				"",
			}, "\n"),
			want: DiffStats{Added: 1, Removed: 1, Files: 2},
		},
		{
			name: "empty diff",
			in:   "",
			want: DiffStats{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseFullDiff(tt.in); got != tt.want {
				t.Errorf("parseFullDiff() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestParseNumstatDiff verifies that parseNumstatDiff correctly aggregates
// per-file added/removed counts from git diff --numstat output.
func TestParseNumstatDiff(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want DiffStats
	}{
		{"single file", "5\t2\tfoo.go\n", DiffStats{Added: 5, Removed: 2, Files: 1}},
		{"multiple files", "5\t2\tfoo.go\n1\t0\tbar.go\n", DiffStats{Added: 6, Removed: 2, Files: 2}},
		{"binary file counted but not summed", "-\t-\timg.png\n3\t1\tfoo.go\n", DiffStats{Added: 3, Removed: 1, Files: 2}},
		{"empty", "", DiffStats{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseNumstatDiff(tt.in); got != tt.want {
				t.Errorf("parseNumstatDiff() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadDiffStats_SkipsWhenStaleOrNoBase verifies that loadDiffStats skips
// the shell-out entirely (never calls the diff functions) when the session's
// status is stale or it has no recorded base branch.
func TestLoadDiffStats_SkipsWhenStaleOrNoBase(t *testing.T) {
	deps := Deps{
		DiffFull: func(string, string) (string, error) {
			t.Fatalf("must not shell out when status is stale or base is empty")
			return "", nil
		},
		DiffNumstat: func(string, string) (string, error) {
			t.Fatalf("must not shell out when status is stale or base is empty")
			return "", nil
		},
	}.withDefaults()

	stale := discover.SessionState{StatusStale: true, BaseBranch: "main"}
	if got, text := loadDiffStats(deps, stale, false, false); got != (DiffStats{}) || len(text) != 0 {
		t.Errorf("stale session: got %+v, %v, want zero value", got, text)
	}

	noBase := discover.SessionState{BaseBranch: ""}
	if got, text := loadDiffStats(deps, noBase, true, true); got != (DiffStats{}) || len(text) != 0 {
		t.Errorf("no-base session: got %+v, %v, want zero value", got, text)
	}
}

// TestLoadDiffStats_RunsWhenEligible verifies that loadDiffStats shells out
// and computes stats when the session is eligible: status is not stale and
// it has a recorded base branch.
func TestLoadDiffStats_RunsWhenEligible(t *testing.T) {
	deps := Deps{
		ResolveBaseRef: resolveQualified,
		DiffFull: func(dir, base string) (string, error) {
			return "diff --git a/x b/x\n+one\n", nil
		},
		DiffNumstat: func(dir, base string) (string, error) {
			return "1\t0\tx\n", nil
		},
	}.withDefaults()

	st := discover.SessionState{WorktreePath: "/wt", BaseBranch: "main"}
	got, _ := loadDiffStats(deps, st, true, false)
	if got.Added != 1 || got.Files != 1 {
		t.Errorf("focused: got %+v", got)
	}
	got, _ = loadDiffStats(deps, st, false, false)
	if got.Added != 1 || got.Files != 1 {
		t.Errorf("unfocused: got %+v", got)
	}
}

// TestDiffPanel_HiddenWithNoBaseBranch verifies that diffPanel.Render returns
// nil (hidden) when the session has no recorded base branch.
func TestDiffPanel_HiddenWithNoBaseBranch(t *testing.T) {
	s := sess("a")
	s.BaseBranch = ""
	got := (diffPanel{}).Render(panelContext{Session: s}, 80)
	if got != nil {
		t.Fatalf("Render() = %v, want nil (hidden) with no base branch", got)
	}
}

// TestDiffPanel_StaleStatus verifies that diffPanel.Render returns a stale
// status message when the session's status is stale.
func TestDiffPanel_StaleStatus(t *testing.T) {
	s := sess("a")
	s.StatusStale = true
	got := (diffPanel{}).Render(panelContext{Session: s}, 80)
	if len(got) != 1 || !strings.Contains(got[0], "stale") {
		t.Fatalf("Render() = %v, want a stale-status line", got)
	}
}

// TestDiffPanel_Error verifies that diffPanel.Render surfaces an error when
// DiffStats.Err is set.
func TestDiffPanel_Error(t *testing.T) {
	s := sess("a")
	s.DiffStats = DiffStats{Err: "boom"}
	got := (diffPanel{}).Render(panelContext{Session: s}, 80)
	if len(got) != 1 || !strings.Contains(got[0], "boom") {
		t.Fatalf("Render() = %v, want the error surfaced", got)
	}
}

// TestDiffPanel_BaseMissing verifies that diffPanel.Render renders EXACTLY
// the named missing-base line — nothing prepended, nothing appended, not the
// counts line, not the raw Err text — when DiffStats.BaseMissing is set, and
// that it names the session's recorded base branch verbatim. Equality on the
// ANSI-stripped text, as detailtabs_test.go asserts, is what rules out an
// exit code or git plumbing riding along on the same line.
func TestDiffPanel_BaseMissing(t *testing.T) {
	s := sess("a")
	s.BaseBranch = "reject-edits-completed-questionnaire"
	s.DiffStats = DiffStats{BaseMissing: true, Err: "exit status 128: should not be read"}
	got := (diffPanel{}).Render(panelContext{Session: s}, 80)
	if len(got) != 1 {
		t.Fatalf("Render() = %v, want exactly one line", got)
	}
	want := "base branch 'reject-edits-completed-questionnaire' no longer exists"
	if plain := xansi.Strip(got[0]); plain != want {
		t.Fatalf("Render() = %q, want exactly %q", plain, want)
	}
}

// TestDiffPanel_RendersCounts verifies that diffPanel.Render's line 0 is
// still the single-line summary with the base branch name and diff stats
// (added, removed, and file count) — a body is now permitted after it, so
// this no longer asserts len(got) == 1 (that was the one-line-only behaviour
// W5.T1 replaced).
func TestDiffPanel_RendersCounts(t *testing.T) {
	s := sess("a")
	s.BaseBranch = "main"
	s.DiffStats = DiffStats{Added: 12, Removed: 3, Files: 2}
	got := (diffPanel{}).Render(panelContext{Session: s}, 80)
	if len(got) == 0 {
		t.Fatalf("Render() = %v, want at least the summary line", got)
	}
	for _, want := range []string{"main", "+12", "-3", "2 file"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("line 0 = %q, want it to contain %q", got[0], want)
		}
	}
}

// TestOverviewDiffFactReportsMissingBase verifies the Overview tab's Diff
// fact renders the same diagnosed line as the Diff tab panel — not the raw
// "error: <git text>" branch — when DiffStats.BaseMissing is set. The two
// render sites consume the same DiffStats and must stay in sync. Equality on
// the ANSI-stripped fact line, as TestDiffPanel_BaseMissing asserts for the
// panel, is what rules out the seeded Err text riding along on the same line
// rather than merely being absent from the whole panel's output.
func TestOverviewDiffFactReportsMissingBase(t *testing.T) {
	s := sess("feat-a")
	s.BaseBranch = "reject-edits-completed-questionnaire"
	s.DiffStats = DiffStats{BaseMissing: true, Err: "exit status 128: should not be read"}
	out := overviewPanel{}.Render(panelContext{Session: s}, 80)

	var diffLine string
	for _, line := range out {
		if plain := xansi.Strip(line); strings.HasPrefix(plain, "Diff") {
			diffLine = plain
		}
	}
	want := "Diff      base branch 'reject-edits-completed-questionnaire' no longer exists"
	if diffLine != want {
		t.Fatalf("Overview Diff fact = %q, want exactly %q", diffLine, want)
	}
}

// --- real-git fixtures ----------------------------------------------------

// diffGit runs git with args in dir, failing the test on error. The package's
// production runGit already owns that name in this package, hence the t-first
// spelling.
func diffGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// newDiffTestRepo builds a throwaway repository with a base branch holding one
// commit and a feature branch checked out on top of it holding one more. It
// returns the repo path and the base branch's name.
//
// The base branch is deliberately NOT named "main": the branch under test has
// to be one this test created, so that deleting it later cannot collide with
// whatever the developer's init.defaultBranch happens to be. Global and system
// git config are neutralized for the whole test (t.Setenv covers the child
// processes the production seam spawns too, not just the ones this helper
// runs), and identity is set per-repo, so the result does not depend on the
// machine running it.
func newDiffTestRepo(t *testing.T) (repo, base string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	base = "diff-test-base"
	diffGit(t, dir, "-c", "init.defaultBranch="+base, "init")
	diffGit(t, dir, "config", "user.name", "afk Test")
	diffGit(t, dir, "config", "user.email", "afk-test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	diffGit(t, dir, "add", "-A")
	diffGit(t, dir, "commit", "-m", "chore: Initial commit")
	diffGit(t, dir, "checkout", "-b", "diff-test-feature")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("root\nsecond line\n"), 0o644); err != nil {
		t.Fatalf("modify README: %v", err)
	}
	diffGit(t, dir, "add", "-A")
	diffGit(t, dir, "commit", "-m", "feat: Add a line")
	return dir, base
}

// TestLoadDiffStats_DeletedBaseBranch is the regression test for the reported
// bug: a session whose recorded base branch has since been deleted rendered
// "diff error: git diff <base>: exit status 128: fatal: ambiguous argument".
// It runs the whole default seam — no fakes — because the defect is in what
// git does with a dangling ref, which no stub reproduces, and this scenario
// had zero coverage. The healthy half runs first so a failure there tells you
// the fixture is wrong rather than the guard.
func TestLoadDiffStats_DeletedBaseBranch(t *testing.T) {
	repo, base := newDiffTestRepo(t)
	deps := Deps{}.withDefaults()
	st := discover.SessionState{WorktreePath: repo, BaseBranch: base}

	healthy, _ := loadDiffStats(deps, st, false, false)
	if healthy.Err != "" || healthy.BaseMissing {
		t.Fatalf("live base: got %+v, want real stats", healthy)
	}
	if healthy.Added != 1 || healthy.Files != 1 {
		t.Fatalf("live base: got %+v, want +1 across 1 file", healthy)
	}

	diffGit(t, repo, "branch", "-D", base)

	got, _ := loadDiffStats(deps, st, false, false)
	if got.Err != "" {
		t.Fatalf("deleted base: surfaced a raw git error %q, want the diagnosed missing-base state", got.Err)
	}
	if !got.BaseMissing {
		t.Fatalf("deleted base: got %+v, want BaseMissing", got)
	}
}

// TestDefaultResolveBaseRef covers the resolver seam against real git, and
// pins WHICH form it hands the diff, not just whether one resolved. Rows:
//
//   - live local branch: the qualified form, so a tag sharing the name can
//     never shadow it (gitrevisions(7) resolves refs/tags/ before refs/heads/
//     for a bare name; internal/session/branch.go documents the same hazard);
//   - branch AND tag of the same name at different commits: still the
//     qualified form — the branch wins;
//   - a name that is only a tag: the bare form, via the fallback. This is the
//     fallback's accepted cost (a real object, not a false "no longer
//     exists"), and asserting it keeps the behaviour deliberate;
//   - deleted local branch with no tag: no form resolves — the bug under
//     repair;
//   - "could not ask" (a non-repo, exit 128; a missing dir, the process never
//     starts): optimistic, so the diff runs and reports the real breakage
//     as Err instead of misdiagnosing a removed worktree as a deleted base.
func TestDefaultResolveBaseRef(t *testing.T) {
	repo, base := newDiffTestRepo(t)
	qualified := "refs/heads/" + base

	if ref, ok := defaultResolveBaseRef(repo, base); !ok || ref != qualified {
		t.Fatalf("live branch: got (%q, %v), want (%q, true)", ref, ok, qualified)
	}

	diffGit(t, repo, "tag", base, "HEAD")
	if ref, ok := defaultResolveBaseRef(repo, base); !ok || ref != qualified {
		t.Errorf("branch shadowed by a same-named tag: got (%q, %v), want (%q, true) — the branch must win", ref, ok, qualified)
	}
	diffGit(t, repo, "tag", "-d", base)

	diffGit(t, repo, "tag", base+"-tag-only", "HEAD")
	if ref, ok := defaultResolveBaseRef(repo, base+"-tag-only"); !ok || ref != base+"-tag-only" {
		t.Errorf("tag-only name: got (%q, %v), want the bare form via the fallback", ref, ok)
	}

	diffGit(t, repo, "branch", "-D", base)
	if ref, ok := defaultResolveBaseRef(repo, base); ok || ref != "" {
		t.Errorf("deleted branch: got (%q, %v), want (\"\", false)", ref, ok)
	}

	notARepo := t.TempDir()
	if ref, ok := defaultResolveBaseRef(notARepo, base); !ok || ref != qualified {
		t.Errorf("non-repo: got (%q, %v), want (%q, true) — the question could not be asked", ref, ok, qualified)
	}
	if ref, ok := defaultResolveBaseRef(filepath.Join(notARepo, "does-not-exist"), base); !ok || ref != qualified {
		t.Errorf("missing dir: got (%q, %v), want (%q, true) — the question could not be asked", ref, ok, qualified)
	}
}

// TestLoadDiffStats_RemoteTrackingBase is the regression test for the
// fallback: `afk new --base origin/main` is a supported input
// (internal/session/create.go, baseTrackNone) and status.json records it
// verbatim, but refs/heads/origin/<x> never exists, so strict qualification
// alone reported every such session as "base branch no longer exists". It
// runs the whole default seam against a remote-tracking ref created with
// update-ref (no network), in both directions: present → real stats through
// the bare form; deleted → BaseMissing, not Err.
func TestLoadDiffStats_RemoteTrackingBase(t *testing.T) {
	repo, base := newDiffTestRepo(t)
	deps := Deps{}.withDefaults()
	remote := "origin/" + base

	diffGit(t, repo, "update-ref", "refs/remotes/"+remote, strings.TrimSpace(diffGit(t, repo, "rev-parse", base)))
	diffGit(t, repo, "branch", "-D", base)
	st := discover.SessionState{WorktreePath: repo, BaseBranch: remote}

	for _, focused := range []bool{false, true} {
		got, _ := loadDiffStats(deps, st, focused, false)
		if got.BaseMissing {
			t.Fatalf("focused=%v: remote-tracking base %q reported missing — the bare-name fallback is gone", focused, remote)
		}
		if got.Err != "" {
			t.Fatalf("focused=%v: remote-tracking base: Err = %q, want real stats", focused, got.Err)
		}
		if got.Added != 1 || got.Files != 1 {
			t.Fatalf("focused=%v: remote-tracking base: got %+v, want +1 across 1 file", focused, got)
		}
	}

	diffGit(t, repo, "update-ref", "-d", "refs/remotes/"+remote)
	got, _ := loadDiffStats(deps, st, false, false)
	if got.Err != "" {
		t.Fatalf("deleted remote-tracking base: surfaced a raw git error %q, want BaseMissing", got.Err)
	}
	if !got.BaseMissing {
		t.Fatalf("deleted remote-tracking base: got %+v, want BaseMissing", got)
	}
}

// TestLoadDiffStats_ShaBase covers the other non-branch input create.go
// accepts, a bare commit SHA, which likewise has no refs/heads/ form. Both
// directions: a real SHA diffs normally; an abbreviated hex that names no
// object is BaseMissing, not Err. (A FULL 40-hex that names no object is
// deliberately not asserted here: git 2.51.0 accepts the syntax in
// `rev-parse --verify` without consulting the object store, so it reaches
// the diff and fails there as an Err — see defaultResolveBaseRef's doc.)
func TestLoadDiffStats_ShaBase(t *testing.T) {
	repo, base := newDiffTestRepo(t)
	deps := Deps{}.withDefaults()
	sha := strings.TrimSpace(diffGit(t, repo, "rev-parse", base))
	diffGit(t, repo, "branch", "-D", base)

	got, _ := loadDiffStats(deps, discover.SessionState{WorktreePath: repo, BaseBranch: sha}, false, false)
	if got.BaseMissing || got.Err != "" {
		t.Fatalf("SHA base: got %+v, want real stats", got)
	}
	if got.Added != 1 || got.Files != 1 {
		t.Fatalf("SHA base: got %+v, want +1 across 1 file", got)
	}

	got, _ = loadDiffStats(deps, discover.SessionState{WorktreePath: repo, BaseBranch: "0123456"}, false, false)
	if got.Err != "" {
		t.Fatalf("unknown abbreviated SHA: surfaced a raw git error %q, want BaseMissing", got.Err)
	}
	if !got.BaseMissing {
		t.Fatalf("unknown abbreviated SHA: got %+v, want BaseMissing", got)
	}
}

// TestLoadDiffStats_TagCannotShadowBranch runs the whole default seam with a
// tag and a branch sharing the base's name at DIFFERENT commits (the tag sits
// on the feature tip, so a diff against it is empty). The branch must win:
// real stats, not zeroes. That is the reason refs/heads/ is tried first and
// the reason the diff must use the form the resolver returned. The second
// half pins the fallback's accepted cost: once the branch is gone, the same
// name resolves to the tag through the bare form and the diff runs against
// it — a real diff against a real object, not BaseMissing and not Err.
func TestLoadDiffStats_TagCannotShadowBranch(t *testing.T) {
	repo, base := newDiffTestRepo(t)
	deps := Deps{}.withDefaults()
	st := discover.SessionState{WorktreePath: repo, BaseBranch: base}

	diffGit(t, repo, "tag", base, "HEAD")
	for _, focused := range []bool{false, true} {
		got, _ := loadDiffStats(deps, st, focused, false)
		if got.BaseMissing || got.Err != "" {
			t.Fatalf("focused=%v: branch+tag: got %+v, want real stats", focused, got)
		}
		if got.Added != 1 || got.Files != 1 {
			t.Fatalf("focused=%v: branch+tag: got %+v, want +1 across 1 file — the tag shadowed the branch", focused, got)
		}
	}

	diffGit(t, repo, "branch", "-D", base)
	got, _ := loadDiffStats(deps, st, false, false)
	if got.BaseMissing || got.Err != "" {
		t.Fatalf("tag only: got %+v, want a diff against the tag via the bare fallback", got)
	}
	if got != (DiffStats{}) {
		t.Fatalf("tag only: got %+v, want zero counts (the tag is at the feature tip)", got)
	}
}

// TestDecideBaseRef is the pure decision matrix. Load-bearing rows: the two
// stale/empty rows pin that the skip predates the ref answer entirely (the
// caller must not pay a rev-parse for a row it will not diff, and a stale row
// whose base happens to resolve must still skip — so both ref answers are
// covered for stale); "base does not resolve" is the bug this task fixes and
// must be a distinct outcome from skip, because skip renders nothing while
// missing has to be visible.
func TestDecideBaseRef(t *testing.T) {
	tests := []struct {
		name         string
		statusStale  bool
		base         string
		baseResolves bool
		want         baseRefAction
	}{
		{"stale status, ref resolves", true, "main", true, baseRefSkip},
		{"stale status, ref does not resolve", true, "main", false, baseRefSkip},
		{"empty base", false, "", false, baseRefSkip},
		{"stale status and empty base", true, "", false, baseRefSkip},
		{"base resolves", false, "main", true, baseRefCompute},
		{"base does not resolve", false, "gone-branch", false, baseRefMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideBaseRef(tt.statusStale, tt.base, tt.baseResolves)
			if got != tt.want {
				t.Errorf("decideBaseRef(%v, %q, %v) = %v, want %v",
					tt.statusStale, tt.base, tt.baseResolves, got, tt.want)
			}
		})
	}
}

// TestBaseRefCandidates pins the pure candidate list: exactly two forms,
// refs/heads/<base> strictly before the bare name. The order is the whole
// tag-shadowing defence (a bare name resolves refs/tags/ first), and the
// bare entry is the whole remote-tracking/SHA fix, so a change to either is
// a behaviour change, not a refactor.
func TestBaseRefCandidates(t *testing.T) {
	tests := []struct {
		name string
		base string
		want []string
	}{
		{"local branch name", "feature-base", []string{"refs/heads/feature-base", "feature-base"}},
		{"remote-tracking name", "origin/main", []string{"refs/heads/origin/main", "origin/main"}},
		{"sha", "7d494e9", []string{"refs/heads/7d494e9", "7d494e9"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := baseRefCandidates(tt.base)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("baseRefCandidates(%q) = %v, want %v", tt.base, got, tt.want)
			}
		})
	}
}

// TestBaseRefArgvAgrees pins the invariant the resolver design exists for:
// for every candidate form, the ref the validation check verifies is
// byte-identical to the ref both diffs receive. If the check qualified while
// a diff re-derived its own form from the bare base (or vice versa), a tag
// could make the guard pass and the diff still resolve a different object.
// Asserting the built argv rather than running git keeps the row cheap and
// makes a regression a diff of literals.
func TestBaseRefArgvAgrees(t *testing.T) {
	for _, ref := range baseRefCandidates("feature-base") {
		t.Run(ref, func(t *testing.T) {
			check := baseRefVerifyArgs(ref)
			full := diffFullArgs(ref)
			numstat := diffNumstatArgs(ref)
			if want := []string{"rev-parse", "--verify", "--quiet", ref}; strings.Join(check, " ") != strings.Join(want, " ") {
				t.Errorf("ref check argv = %v, want %v", check, want)
			}
			if want := []string{"diff", "--no-color", "--no-ext-diff", "--default-prefix", ref}; strings.Join(full, " ") != strings.Join(want, " ") {
				t.Errorf("full diff argv = %v, want %v", full, want)
			}
			if want := []string{"diff", "--numstat", ref}; strings.Join(numstat, " ") != strings.Join(want, " ") {
				t.Errorf("numstat diff argv = %v, want %v", numstat, want)
			}
		})
	}
}

// TestLoadDiffStats_DiffsAgainstResolvedRef pins, with the seam faked, that
// loadDiffStats forwards the resolver's answer to the diff — not the recorded
// base, not a re-qualified form. The fake resolves to the bare remote name
// precisely because that is the case where the two differ.
func TestLoadDiffStats_DiffsAgainstResolvedRef(t *testing.T) {
	var fullRef, numstatRef string
	deps := Deps{
		ResolveBaseRef: func(dir, base string) (string, bool) {
			if dir != "/wt" || base != "origin/main" {
				t.Errorf("ResolveBaseRef(%q, %q), want (/wt, origin/main)", dir, base)
			}
			return "origin/main", true
		},
		DiffFull: func(_, ref string) (string, error) {
			fullRef = ref
			return "", nil
		},
		DiffNumstat: func(_, ref string) (string, error) {
			numstatRef = ref
			return "", nil
		},
	}.withDefaults()

	st := discover.SessionState{WorktreePath: "/wt", BaseBranch: "origin/main"}
	loadDiffStats(deps, st, true, false)
	loadDiffStats(deps, st, false, false)
	if fullRef != "origin/main" {
		t.Errorf("DiffFull received %q, want the resolver's %q", fullRef, "origin/main")
	}
	if numstatRef != "origin/main" {
		t.Errorf("DiffNumstat received %q, want the resolver's %q", numstatRef, "origin/main")
	}
}

// TestLoadDiffStats_SkipsRefCheckWhenStaleOrNoBase pins the cost guard: the
// rev-parse is per-session per-refresh-tick, so a stale or base-less row must
// not pay for it any more than it pays for the diff it guards.
func TestLoadDiffStats_SkipsRefCheckWhenStaleOrNoBase(t *testing.T) {
	deps := Deps{
		ResolveBaseRef: func(_, base string) (string, bool) {
			t.Errorf("must not check the base ref when status is stale or base is empty")
			return "refs/heads/" + base, true
		},
		DiffFull: func(string, string) (string, error) {
			t.Errorf("must not shell out when status is stale or base is empty")
			return "", nil
		},
		DiffNumstat: func(string, string) (string, error) {
			t.Errorf("must not shell out when status is stale or base is empty")
			return "", nil
		},
	}.withDefaults()

	stale := discover.SessionState{StatusStale: true, BaseBranch: "main"}
	if got, _ := loadDiffStats(deps, stale, false, false); got != (DiffStats{}) {
		t.Errorf("stale session: got %+v, want zero value", got)
	}
	noBase := discover.SessionState{BaseBranch: ""}
	if got, _ := loadDiffStats(deps, noBase, true, false); got != (DiffStats{}) {
		t.Errorf("no-base session: got %+v, want zero value", got)
	}
}

// TestLoadDiffStats_MissingBaseSkipsDiff verifies the guard does its job with
// the seam faked: an unresolvable base never reaches the diff call at all, and
// the state it reports is BaseMissing, not Err.
func TestLoadDiffStats_MissingBaseSkipsDiff(t *testing.T) {
	deps := Deps{
		ResolveBaseRef: func(dir, base string) (string, bool) {
			if dir != "/wt" || base != "gone" {
				t.Errorf("ResolveBaseRef(%q, %q), want (/wt, gone)", dir, base)
			}
			return "", false
		},
		DiffFull: func(string, string) (string, error) {
			t.Errorf("must not diff against a base that does not resolve")
			return "", nil
		},
		DiffNumstat: func(string, string) (string, error) {
			t.Errorf("must not diff against a base that does not resolve")
			return "", nil
		},
	}.withDefaults()

	st := discover.SessionState{WorktreePath: "/wt", BaseBranch: "gone"}
	for _, focused := range []bool{true, false} {
		got, _ := loadDiffStats(deps, st, focused, false)
		if !got.BaseMissing {
			t.Errorf("focused=%v: got %+v, want BaseMissing", focused, got)
		}
		if got.Err != "" {
			t.Errorf("focused=%v: Err = %q, want empty — a diagnosed missing base is not a git failure", focused, got.Err)
		}
	}
}

// TestSwitchDetailTab_ToDiff_DispatchesLoad verifies that switching onto the
// Diff tab returns a non-nil Cmd — the tab-switch dispatch W4.T1 adds so a
// switch does not sit on a placeholder until the next refresh tick. Landing
// via shift+tab from the Overview mirrors TestDetailTabSwitching's own
// "wraps to the last tab" row: the Diff tab is always last in
// visibleDetailTabs, with or without git-spice, so this needs no stack
// fixture.
func TestSwitchDetailTab_ToDiff_DispatchesLoad(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	got, cmd := m.switchDetailTab(actionDetailTabPrev, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	nm, ok := got.(Model)
	if !ok {
		t.Fatalf("switchDetailTab returned %T, want ui.Model", got)
	}
	if nm.activeDetailTab() != tabDiff {
		t.Fatalf("fixture broken: expected to land on the Diff tab, got %v", nm.activeDetailTab())
	}
	if cmd == nil {
		t.Fatalf("switchDetailTab to tabDiff must dispatch a load Cmd, got nil")
	}
}

// TestSwitchDetailTab_ToOtherTab_NoDispatch verifies the dispatch above is
// specific to landing on the Diff tab: switching to some other tab, with no
// prior tab-switch dispatch already in flight, returns a nil Cmd exactly as
// it did before this task.
func TestSwitchDetailTab_ToOtherTab_NoDispatch(t *testing.T) {
	m := testModel(sess("alpha"))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	got, cmd := m.switchDetailTab(actionDetailTabNext, tea.KeyPressMsg{Code: tea.KeyTab})
	nm, ok := got.(Model)
	if !ok {
		t.Fatalf("switchDetailTab returned %T, want ui.Model", got)
	}
	if nm.activeDetailTab() != tabWaves {
		t.Fatalf("fixture broken: expected to land on the Waves tab, got %v", nm.activeDetailTab())
	}
	if cmd != nil {
		t.Fatalf("switchDetailTab to a non-Diff tab must not dispatch a load Cmd")
	}
}

// TestLoadCmd_RetainsDiffTextOnlyWhenDiffTabActive verifies loadCmd's
// retainText capture (messages.go): the focused session's diff text lands on
// Session.DiffText when the Diff tab is active at Cmd-creation time, and is
// withheld — same DiffFull call, same output — when some other tab is
// active, per Session.DiffText's doc comment that it stays resident only
// while the tab that displays it is on screen.
func TestLoadCmd_RetainsDiffTextOnlyWhenDiffTabActive(t *testing.T) {
	newModel := func() Model {
		m := New("/repo", time.Second, Deps{
			Discover: func(string) ([]discover.SessionState, error) {
				return []discover.SessionState{sampleState("a")}, nil
			},
			ReadTail:       func(string, int) []string { return nil },
			Now:            time.Now,
			ResolveBaseRef: resolveQualified,
			DiffFull: func(dir, ref string) (string, error) {
				return "diff --git a/x b/x\n+one\n", nil
			},
			DiffNumstat: func(dir, ref string) (string, error) { return "1\t0\tx\n", nil },
		})
		m.setSessions([]Session{sess("a")})
		m.selectSessionIndex(0)
		return m
	}

	overview := newModel()
	msg, ok := overview.loadCmd()().(sessionsLoadedMsg)
	if !ok || len(msg.sessions) != 1 {
		t.Fatalf("Overview tab active: loadCmd() = %+v, want one session", msg)
	}
	if len(msg.sessions[0].DiffText) != 0 {
		t.Fatalf("Overview tab active: DiffText = %v, want none retained", msg.sessions[0].DiffText)
	}

	onDiff := newModel()
	onDiff.detailTab = tabDiff
	msg, ok = onDiff.loadCmd()().(sessionsLoadedMsg)
	if !ok || len(msg.sessions) != 1 {
		t.Fatalf("Diff tab active: loadCmd() = %+v, want one session", msg)
	}
	want := []string{"diff --git a/x b/x", "+one"}
	if got := msg.sessions[0].DiffText; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Diff tab active: DiffText = %v, want %v", got, want)
	}
}
