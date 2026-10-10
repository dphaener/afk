// diff.go computes per-session diff stats against each session's base
// branch, using the dual-cost pattern the task mandates: a FULL `git diff
// {base}` for the currently-focused (selected) session only — expensive,
// since it reads the whole diff text to count changed lines — and the cheap
// `git diff --numstat {base}` aggregate for every other session. Never the
// reverse: running a full diff for every unfocused session would make the
// refresh ticker's cost scale with the number of open sessions instead of
// staying flat.
//
// All of this runs inside loadCmd's Cmd func (messages.go), off the UI
// thread, on the same refresh tick as session discovery — never in Update,
// per this package's pure-Model constraint.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/dphaener/afk/internal/discover"
)

// DiffStats summarizes how a session's branch differs from its base branch.
// Added/Removed are content line counts; Files is the number of files
// touched.
//
// Err and BaseMissing are deliberately separate states, not one string. Err
// means a git diff that was expected to work did not (the worktree was
// removed, the index is locked) — an unexpected failure worth showing
// verbatim. BaseMissing means the recorded base branch no longer resolves,
// which is a diagnosed, ordinary condition: `afk clean --delete-branch`
// removes branches that other sessions still record as their base, and
// base_branch is fixed at session creation and never rewritten. The two
// render differently, so stuffing a sentence into Err would make them
// indistinguishable to the render sites. When BaseMissing is set no diff ran,
// so the counts are meaningless and stay zero.
type DiffStats struct {
	Added       int
	Removed     int
	Files       int
	Err         string
	BaseMissing bool
}

// baseRefAction is what loadDiffStats does about one session's recorded base
// branch: render nothing, report the base as missing, or compute the diff.
type baseRefAction int

const (
	baseRefSkip baseRefAction = iota
	baseRefMissing
	baseRefCompute
)

// baseRefCheckable reports whether a session's base branch is worth asking
// git about at all. It exists as its own predicate — rather than living only
// inside decideBaseRef — because the caller has to answer the same question
// BEFORE it can supply decideBaseRef's baseResolves argument, and a second,
// hand-written copy of the condition at the call site is exactly how the two
// would drift into reporting a skippable row as missing.
func baseRefCheckable(statusStale bool, base string) bool {
	return !statusStale && base != ""
}

// decideBaseRef decides what to do about a session's base branch from facts
// the caller already holds: whether the status is stale, the recorded base
// name, and the caller's pre-fetched answer to whether ANY form of the base
// resolved — deps.ResolveBaseRef trying refs/heads/<base> and then the bare
// name (see baseRefCandidates) before the caller ever gets here. decideBaseRef
// itself does not care which candidate resolved or which ref string will be
// diffed against, only whether the search as a whole succeeded. Pure — the
// rev-parse calls happen in loadDiffStats and arrive here as a single bool —
// so the whole matrix is table-testable without git, per this repo's
// pure-decision-function convention (see shouldHandOffToDash in new.go).
// baseResolves is ignored for a stale or empty base: those skip before the
// ref ever matters, which is why the caller is free not to fetch it.
func decideBaseRef(statusStale bool, base string, baseResolves bool) baseRefAction {
	if !baseRefCheckable(statusStale, base) {
		return baseRefSkip
	}
	if !baseResolves {
		return baseRefMissing
	}
	return baseRefCompute
}

// loadDiffStats computes DiffStats for one discovered session, applying the
// dual-cost pattern via computeDiffStats. It skips the shell-out entirely
// (returns the zero DiffStats, nil text) when the session's status is stale
// or it has no recorded base branch — neither can be trusted, and there is
// nothing to diff against.
//
// retainText asks computeDiffStats to also return the capped diff text
// alongside the counts — see computeDiffStats for why that only ever
// happens on the focused branch, regardless of this argument's value on the
// other branch.
//
// Before diffing it resolves the base ref, because base_branch is written
// once at session creation and is never rewritten: deleting that branch (via
// `afk clean --delete-branch`, among others) leaves every session recording
// it pointed at a dangling name, and `git diff <dangling>` exits 128. The
// resolver returns the exact ref expression it verified, and that string —
// not st.BaseBranch — is what the diff runs against, so the check and the
// diff can never resolve two different objects (see baseRefCandidates for
// why they otherwise could). The check costs one or two extra `rev-parse`
// calls per session per refresh tick, which is acceptable precisely because
// of what it guards: each is strictly cheaper than the `git diff` it may
// skip, it never runs for a row that would not have diffed anyway, and it
// turns a raw exit code into a state the panel can explain.
func loadDiffStats(deps Deps, st discover.SessionState, focused, retainText bool) (DiffStats, []string) {
	ref, resolved := "", false
	if baseRefCheckable(st.StatusStale, st.BaseBranch) {
		ref, resolved = deps.ResolveBaseRef(st.WorktreePath, st.BaseBranch)
	}
	switch decideBaseRef(st.StatusStale, st.BaseBranch, resolved) {
	case baseRefMissing:
		return DiffStats{BaseMissing: true}, nil
	case baseRefCompute:
		return computeDiffStats(deps.DiffFull, deps.DiffNumstat, st.WorktreePath, ref, focused, retainText)
	default:
		return DiffStats{}, nil
	}
}

// computeDiffStats fetches DiffStats for one worktree/ref pair. ref is the
// already-resolved expression Deps.ResolveBaseRef returned, forwarded
// verbatim. focused selects which half of the dual-cost pattern runs: true
// runs fullDiff (`git diff {ref}`, parsed for line counts); false runs
// numstatDiff (`git diff --numstat {ref}`, a cheap per-file aggregate). A
// non-focused session NEVER calls fullDiff — that is the entire point of the
// pattern — and NEVER retains text, regardless of retainText: there is no
// full diff output on that branch to retain.
//
// retainText, when true, is honoured only on the focused branch: it asks for
// the capped line slice retainDiffText builds from the same fullDiff output
// parseFullDiff already counted, so a >2000-line diff's Added/Removed/Files
// still reflect the whole diff while the retained text is capped.
func computeDiffStats(fullDiff, numstatDiff func(dir, ref string) (string, error), dir, ref string, focused, retainText bool) (DiffStats, []string) {
	if focused {
		out, err := fullDiff(dir, ref)
		if err != nil {
			return DiffStats{Err: err.Error()}, nil
		}
		stats := parseFullDiff(out)
		if !retainText {
			return stats, nil
		}
		return stats, retainDiffText(out)
	}
	out, err := numstatDiff(dir, ref)
	if err != nil {
		return DiffStats{Err: err.Error()}, nil
	}
	return parseNumstatDiff(out), nil
}

// retainDiffText builds the capped line slice retained for the Diff tab from
// the focused session's full `git diff` output. Counts come from
// parseFullDiff over the uncapped out, so the header stays correct even when
// this returns a capped, marked slice.
//
// bufio.Scanner is required here, not stylistic. strings.Split would yield
// substrings sharing out's backing array, so retaining up to 2000 of them
// would pin the ENTIRE uncapped diff — tens of MB for a large one — in
// memory, defeating the reason retention is gated at all. Split would also
// turn git's trailing newline into a spurious empty final element, which
// would both trip the cap into a false "truncated" at exactly diffLineCap
// lines and render a blank tail row. sc.Text() returns a fresh string per
// line and has neither problem.
//
// The buffer call mirrors parseFullDiff's exactly (diff.go, parseFullDiff):
// without it, the scanner's default 64KiB token cap makes Scan() return
// false with ErrTooLong on a single line over that (a minified bundle, a
// vendored file), silently truncating DiffText mid-diff with no marker while
// parseFullDiff — scanning the same bytes without a buffer override in its
// own call — still reports full counts. sc.Err() is checked for exactly this
// case, and when set the truncation marker is appended even though
// capDiffLines alone would not have flagged a truncation.
//
// The loop reads at most diffLineCap+1 lines: that one line past the cap is
// what lets capDiffLines tell "exactly diffLineCap" apart from "more than
// diffLineCap" without scanning the rest of a possibly huge diff.
func retainDiffText(out string) []string {
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	lines := make([]string, 0, diffLineCap+1)
	for len(lines) <= diffLineCap && sc.Scan() {
		lines = append(lines, sc.Text())
	}
	kept, truncated := capDiffLines(lines, diffLineCap)
	if sc.Err() != nil {
		truncated = true
	}
	if truncated {
		kept = append(kept, diffTruncationMarker)
	}
	return kept
}

// parseFullDiff counts added/removed content lines and files touched from
// the text of `git diff {base}`. Only lines inside a file's hunks count:
// "+++"/"---" file-header lines are excluded (they share a prefix with real
// added/removed lines but are not diff content), and "diff --git a/... b/..."
// lines mark file boundaries.
func parseFullDiff(out string) DiffStats {
	var stats DiffStats
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			stats.Files++
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			// File-header line, not content — must not count as +/-.
		case strings.HasPrefix(line, "+"):
			stats.Added++
		case strings.HasPrefix(line, "-"):
			stats.Removed++
		}
	}
	return stats
}

// parseNumstatDiff parses `git diff --numstat {base}` output: one line per
// file, "added\tremoved\tpath". Binary files report "-" for both counts (per
// git's documented numstat format) and are counted in Files but not in
// Added/Removed.
func parseNumstatDiff(out string) DiffStats {
	var stats DiffStats
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		stats.Files++
		if a, err := strconv.Atoi(fields[0]); err == nil {
			stats.Added += a
		}
		if r, err := strconv.Atoi(fields[1]); err == nil {
			stats.Removed += r
		}
	}
	return stats
}

// baseRefCandidates lists the ref expressions a session's recorded base is
// tried as, in the order they are tried: refs/heads/<base> first, the bare
// name second. Pure, so a test can pin the order without running git.
//
// Both entries are load-bearing. refs/heads/ goes first because
// gitrevisions(7) resolves refs/tags/<name> before refs/heads/<name>, so a
// bare name lets a tag sharing a branch's name stand in for it — verified
// against git 2.51.0 with a branch and a tag both named probe-base at
// different commits: `rev-parse --verify refs/heads/probe-base` answered the
// branch's commit, bare `probe-base` the tag's, and `git diff probe-base`
// diffed against the tag behind a "refname is ambiguous" warning.
// internal/session/branch.go's package doc records the same hazard from the
// other direction (a tag making an unmerged branch read as merged).
//
// The bare name goes second because status.json's base_branch does NOT
// always name a local branch: internal/session/create.go's resolveBase
// returns `--base` verbatim and seedStatus records it as-is, and its
// baseTrackNone doc names `--base origin/main` and a bare SHA as supported
// inputs. Neither has a refs/heads/ form — on git 2.51.0
// `rev-parse --verify --quiet refs/heads/origin/main` exits 1 while bare
// `origin/main` resolves through refs/remotes/, and a SHA resolves only
// bare — so qualification alone would report a healthy remote-tracking or
// SHA base as deleted. The cost of the fallback is that a tag left behind at
// a DELETED branch's name resolves through the bare form and gets diffed
// against, exactly as the pre-guard bare `git diff <base>` did; that is a
// real diff against a real object, not a false "no longer exists".
func baseRefCandidates(base string) []string {
	return []string{"refs/heads/" + base, base}
}

// The three argv builders below take a candidate from baseRefCandidates and
// pass it through unchanged. The invariant this buys: whichever candidate the
// resolver verified is the exact string both diffs receive, so a test can
// assert the check and the diffs agree without running git.
//
// diffFullArgs pins the output format against the user's git config, because
// a bare `git diff {ref}` is not stable across machines:
//
//   - --no-color: without it, color.diff=always injects ANSI escapes into the
//     output, which breaks classifyDiffLine (an escape sequence, not `+`/`-`,
//     starts the line) and already miscounts in parseFullDiff today. Verified
//     on git 2.51.0 to suppress the injection.
//   - --no-ext-diff: without it, diff.external replaces the format outright.
//     Verified on 2.51.0: `git -c diff.external=/usr/bin/false diff` dies with
//     "fatal: external diff died", so an external driver can also just break
//     the call rather than merely reformat it.
//   - --default-prefix: restores the `a/`/`b/` prefixes regardless of
//     diff.noprefix, diff.mnemonicPrefix, or diff.srcPrefix/dstPrefix.
//     Verified present and effective on git 2.51.0 (the flag requires git
//     ≥2.41); preferred here over `--src-prefix=a/ --dst-prefix=b/` because
//     it is one purpose-built flag rather than two. Downstream diff-line
//     parsing depends on these prefixes being present to tell a file header
//     or a `---`/`+++` file marker apart from an ordinary removed/added
//     line — diff.noprefix=true would otherwise make both unparseable.
//
// diffNumstatArgs needs none of these: verified on git 2.51.0 that `git -c
// diff.external=/usr/bin/false diff --numstat` still produces correct
// numstat output, because --numstat bypasses the external diff driver
// entirely. Adding --no-ext-diff there would be a redundant no-op, not a
// fix, so it is intentionally omitted.
func diffFullArgs(ref string) []string {
	return []string{"diff", "--no-color", "--no-ext-diff", "--default-prefix", ref}
}

func diffNumstatArgs(ref string) []string {
	return []string{"diff", "--numstat", ref}
}

func baseRefVerifyArgs(ref string) []string {
	return []string{"rev-parse", "--verify", "--quiet", ref}
}

// defaultDiffFull runs `git diff {ref}` in dir — the full diff text, ref
// being what defaultResolveBaseRef returned. Used only for the
// currently-focused session (see computeDiffStats).
func defaultDiffFull(dir, ref string) (string, error) {
	return runGit(dir, diffFullArgs(ref)...)
}

// defaultDiffNumstat runs `git diff --numstat {ref}` in dir, ref being what
// defaultResolveBaseRef returned. Used for every session except the focused
// one.
func defaultDiffNumstat(dir, ref string) (string, error) {
	return runGit(dir, diffNumstatArgs(ref)...)
}

// defaultResolveBaseRef tries each of baseRefCandidates(base) in dir with
// `rev-parse --verify --quiet` — git's own idiom for asking, silent on a
// miss, and the same call internal/session/create.go's branchExists makes —
// and returns the first candidate that resolves. (ref, true) means "diff
// against ref"; ("", false) means no form of the base resolves. A miss is an
// expected answer here, not an error to surface.
//
// Only exit 1 moves on to the next candidate. That is the code `--verify
// --quiet` reserves for a ref that did not resolve; every other failure
// (exit 128 "not a git repository", or the process not starting at all
// because dir was removed) means the question could not be ASKED, which is a
// different thing and not something to diagnose as a deleted branch.
// Verified against git 2.51.0: missing ref exits 1, non-repo exits 128.
// Those cases return (candidate, true) so the diff still runs and reports
// the real breakage through DiffStats.Err, preserving the behavior a removed
// worktree has always had. One known soft spot, also verified on 2.51.0: a
// full 40-hex string that names no object passes `rev-parse --verify` (git
// accepts the syntax without checking the object store), so it resolves here
// and the diff then fails with "bad object" — an Err, never a false
// BaseMissing, and the same failure the pre-guard code produced.
func defaultResolveBaseRef(dir, base string) (string, bool) {
	for _, ref := range baseRefCandidates(base) {
		_, err := runGit(dir, baseRefVerifyArgs(ref)...)
		if err == nil {
			return ref, true
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			continue
		}
		return ref, true
	}
	return "", false
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

// diffPanel renders the selected session's diff stats against its base
// branch (computed by loadDiffStats/computeDiffStats in loadCmd), plus — on
// the success path — the retained diff body from ctx.Session.DiffText.
// Hidden (Render returns nil) when there's no base branch recorded yet —
// nothing to diff against, the same "no data yet" first-class state used
// elsewhere in this package (e.g. wavesPanel's "No plan file.").
//
// The four pre-existing states (no base branch, stale status, base missing,
// git error) each still return exactly one line and no body — appending an
// empty diff body under an error would be new, unrequested behaviour, not a
// preservation of the old one.
type diffPanel struct{}

func (diffPanel) Title() string { return "Diff vs base" }

func (diffPanel) Render(ctx panelContext, width int) []string {
	s := ctx.Session
	if s.BaseBranch == "" {
		return nil
	}
	if s.StatusStale {
		return []string{theme.staleText.Render("diff stats unavailable: status is stale")}
	}
	d := s.DiffStats
	if d.BaseMissing {
		line := fmt.Sprintf("base branch '%s' no longer exists", s.BaseBranch)
		return []string{theme.staleText.Render(truncate(line, width))}
	}
	if d.Err != "" {
		return []string{theme.staleText.Render("diff error: " + d.Err)}
	}

	header := theme.dim.Render(truncate(fmt.Sprintf("vs %s: +%d/-%d across %d file(s)", s.BaseBranch, d.Added, d.Removed, d.Files), width))

	// This is also the zero DiffStats of a session whose first load has not
	// landed yet (see the plan's Context section) — it can show transiently
	// where "+0/-0 across 0 file(s)" showed before. It does not, and must
	// not be read to, prove that a diff ran and came back empty.
	if d.Added == 0 && d.Removed == 0 && d.Files == 0 {
		return []string{header, theme.dim.Render(truncate(fmt.Sprintf("no changes vs %s", s.BaseBranch), width))}
	}

	// There are changes but the retained text hasn't landed: reachable on
	// the first frame after a tab switch, and after any cursor move to a
	// different session, since W4.T1 deliberately has no selection-change
	// load dispatch (see that task's note for why).
	if len(s.DiffText) == 0 {
		return []string{header, theme.dim.Render(truncate("loading diff…", width))}
	}

	lines := make([]string, 0, len(s.DiffText)+2)
	lines = append(lines, header, "")
	for _, l := range s.DiffText {
		lines = append(lines, renderDiffLine(l, width))
	}
	return lines
}
