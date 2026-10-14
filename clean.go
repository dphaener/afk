package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/prompt"
	"github.com/dphaener/afk/internal/session"
)

// cmdClean implements `afk clean`: lifecycle cleanup for afk sessions.
// With no arguments it lists every discovered session (with phase),
// reconciling `.afk/*/status.json` against `git worktree list --porcelain`
// — including the worktree-side faults and a status-backed `.afk/{name}/`
// stranded in a worktree checked out on some other branch, both ordinary rows
// in this listing (see session.Kind). With a session name it resolves that
// name to one of those rows (see findCleanTarget) and applies whichever
// teardown the row's Kind calls for; for a healthy session that is the
// claude-squad pattern: snapshot-commit if dirty, archive `.afk/{name}/`
// (it is gitignored and would otherwise be lost), remove the worktree, and
// prune. The branch is kept by default — pass --delete-branch to opt into
// deleting it too (see deleteBranchTTYRefused and
// shouldConfirmUnmergedBranchDelete for the unmerged-branch safeguard).
func cmdClean(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	var force bool
	fs.BoolVar(&force, "force", false, "Clean a session even if its phase is not done/failed")
	var deleteBranch bool
	fs.BoolVar(&deleteBranch, "delete-branch", false,
		"Delete the session's branch after cleaning. A merged branch is deleted immediately; "+
			"an unmerged one requires interactive confirmation on a TTY and is refused otherwise. "+
			"Has no effect (and errors) on a scratch session or a session with no worktree of its "+
			"own (status-without-worktree) — neither ever has a branch.")
	project := addProjectFlag(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `afk clean — reconcile and clean up afk sessions

Usage:
  afk clean                 List sessions (with phase)
  afk clean [options] <name>
                               Archive + remove the worktree for session
                               <name>. Any dirty tracked changes are
                               snapshot-committed first. The branch is
                               kept unless --delete-branch is passed.

By default, afk clean operates on sessions in the current repository (cwd).
Pass --project PATH to operate on sessions in a different repository instead.

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "afk clean: expected at most one session <name>")
		fs.Usage()
		os.Exit(2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk clean: cannot determine working directory: %v\n", err)
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk clean: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	// resolveAndValidateProject owns the only-when---project-given validation
	// guard (see its doc comment in new.go); this caller just prints its
	// already-`afk clean:`-prefixed error and exits non-zero.
	repo, err := resolveAndValidateProject("clean", rawProjectFlag(*project), cwd, home)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	deps := session.DefaultCleanDeps()
	sessions, err := session.Discover(repo, deps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "afk clean: %v\n", err)
		os.Exit(1)
	}

	if fs.NArg() == 0 {
		printSessions(sessions)
		return
	}

	// Everything past the listing goes through cleanSessionForSurface, the
	// same entry point the web page uses, so the two cannot check or tear down
	// differently. The terminal questions (the forced-clean confirm and the
	// unmerged-branch one) come in through TTY and Confirm, and only from here.
	res := cleanSessionForSurface(repo, fs.Arg(0), cleanSurfaceRequest{Force: force, DeleteBranch: deleteBranch},
		cleanSurfaceDeps{Clean: deps, TTY: isTerminal(os.Stdin), Confirm: prompt.Confirm})
	os.Exit(printCleanSurfaceResult(os.Stdout, os.Stderr, res))
}

// printCleanSurfaceResult prints a clean's outcome the way `afk clean <name>`
// always has and returns the exit code: what internal/session wrote while it
// worked back on its own stream first (it used to write there directly, ahead
// of everything below), then a refusal or failure on stderr behind
// "afk clean: ", or the summary lines on stdout.
func printCleanSurfaceResult(stdout, stderr io.Writer, res cleanSurfaceResult) int {
	fmt.Fprint(stdout, res.EchoStdout)
	fmt.Fprint(stderr, res.EchoStderr)
	if res.ToStderr {
		fmt.Fprintf(stderr, "afk clean: %s\n", res.Text)
		return res.Code
	}
	for _, line := range res.Lines {
		fmt.Fprintln(stdout, line)
	}
	return res.Code
}

// findCleanTarget returns the session named name, or nil, resolving in three
// tiers and taking the first match WITHIN each tier — well defined only
// because session.Discover sorts its result by a total order, though not one
// that alone decides the tiering below (see session.sortSessions; it orders
// by WorktreePath ahead of Kind, so two entries at different paths do not
// necessarily land tier-in-order).
//
//  1. Any entry whose Kind is not session.Kind.IdentityRelevant (healthy,
//     stale-status, or scratch) always wins. This is the CLI counterpart of
//     the (Name, Scratch, Kind) identity match internal/ui's
//     resolveCleanTarget applies: the CLI has only a bare name to resolve,
//     and first-match-across-kinds would hand `afk clean dup` a fault or
//     stranded teardown for a name that also names an ordinary session. What
//     a bare `afk clean <name>` does for a healthy or scratch session is a
//     documented invariant.
//  2. Failing that, a KindStatusWithoutWorktree entry — the zero-git
//     teardown — wins over a worktree-side fault. A stranded `.afk/X/` and
//     a broken worktree on branch `X` therefore never coin-flip: the
//     stranded one is cleaned first, and the fault stays reachable by naming
//     `X` again once the stranded entry is gone (see sameNameRemaining,
//     which is what tells the caller that).
//  3. Failing that, a worktree-side fault (session.Kind.WorktreeFault).
//
// Pulled out of cmdClean so that choice is testable without an os.Exit-driving
// command run.
func findCleanTarget(sessions []session.Session, name string) *session.Session {
	var stranded, fault *session.Session
	for i := range sessions {
		if sessions[i].Name != name {
			continue
		}
		if !sessions[i].Kind.IdentityRelevant() {
			return &sessions[i]
		}
		if sessions[i].Kind == session.KindStatusWithoutWorktree {
			if stranded == nil {
				stranded = &sessions[i]
			}
			continue
		}
		if fault == nil {
			fault = &sessions[i]
		}
	}
	if stranded != nil {
		return stranded
	}
	return fault
}

// sameNameRemaining returns every entry in sessions that answers to the same
// name as target, other than target itself — the entries a second `afk
// clean <name>` could still reach after target is cleaned. Used by cmdClean
// to tell the user about a same-named entry findCleanTarget did not pick (see
// its doc comment for when that happens), rather than let it silently
// disappear from view once target is gone.
//
// Pure and comparing by value (Session is comparable — see its fields) so it
// is table-testable without going through Discover.
func sameNameRemaining(sessions []session.Session, target session.Session) []session.Session {
	var rest []session.Session
	for _, s := range sessions {
		if s.Name != target.Name || s == target {
			continue
		}
		rest = append(rest, s)
	}
	return rest
}

// deleteBranchNoOpRefused reports whether --delete-branch must be refused
// outright for the given target: a scratch session (session.Session.Scratch's
// doc comment) or a KindStatusWithoutWorktree session (Discover deliberately
// leaves its Branch empty — see the kind's doc comment) never has a branch of
// its own, so there is nothing for the flag to delete, and letting it through
// would silently no-op rather than tell the caller their flag did nothing.
func deleteBranchNoOpRefused(deleteBranch, scratch bool, kind session.Kind) bool {
	return deleteBranch && (scratch || kind == session.KindStatusWithoutWorktree)
}

// deleteBranchTTYRefused reports whether --delete-branch on an unmerged
// branch must be refused outright rather than asked about: unmerged, and
// stdin is not a TTY to ask on. This is what keeps a scripted `afk clean
// --delete-branch` from silently destroying unmerged work — see
// shouldConfirmUnmergedBranchDelete for the interactive counterpart, and
// session.CleanOptions.ConfirmedUnmerged for what a caller passes once past
// either gate.
func deleteBranchTTYRefused(merged, tty bool) bool {
	return !merged && !tty
}

// shouldConfirmUnmergedBranchDelete reports whether --delete-branch must
// pause for an explicit interactive confirmation before deleting an unmerged
// branch: unmerged, and a TTY is available to ask on. A merged branch is
// deleted without asking, matching session.CleanOptions.ConfirmedUnmerged's
// own doc ("a merged branch is deleted regardless of this field").
func shouldConfirmUnmergedBranchDelete(merged, tty bool) bool {
	return !merged && tty
}

// cleanSummary renders the Kept:/Removed: lines cmdClean prints after a
// successful clean. A scratch CleanResult carries only Name — no branch, no
// worktree, no archive, because it never had any of them (see
// session.Session.Scratch's doc comment) — so printing the planned-session
// format for one would render three blank fields, or worse, WorktreePath
// would be the user's own repo root printed next to the word "Removed".
//
// A KindStatusWithoutWorktree CleanResult gets its own format for the same
// reason in reverse: its WorktreePath is the CARRIER worktree
// (cleanStatusWithoutWorktree touches nothing but `.afk/{name}/`), so it
// must never print "Removed: worktree:" — that would claim the user's own
// checkout was removed.
//
// A fault-route CleanResult carries ArchiveSkipped and an empty ArchivePath
// (the session directory died with the worktree or never existed), so the
// archive line says so instead of dangling an empty path — the same fix
// internal/ui's cleanSucceededStatus applies to the same field. A deleted
// git-spice branch can additionally carry RetargetedBranches; that retarget
// is invisible in git itself until the next restack, so this summary is where
// the user learns to restack deliberately.
//
// Extracted to its own function so these branches are table-testable without
// a real os.Exit-driving cmdClean call.
func cleanSummary(scratch bool, result session.CleanResult) string {
	if scratch {
		return fmt.Sprintf(
			"Kept:\n  (nothing — a one-off session has no worktree, branch, or archive)\nRemoved:\n  session: .afk/%s\n",
			result.Name)
	}
	if result.Kind == session.KindStatusWithoutWorktree {
		return fmt.Sprintf(
			"Kept:\n  archive: %s\n  (worktree and branch untouched — this session had no worktree of its own)\nRemoved:\n  session: .afk/%s\n",
			result.ArchivePath, result.Name)
	}
	archiveLine := "  archive: " + result.ArchivePath + "\n"
	if result.ArchiveSkipped {
		archiveLine = "  (no archive — the session directory was gone or never there)\n"
	}
	if result.BranchDeleted {
		out := fmt.Sprintf(
			"Kept:\n%sRemoved:\n  worktree: %s\n  branch:   %s (recoverable at %s)\n",
			archiveLine, result.WorktreePath, result.Branch, result.DeletedBranchSHA)
		if result.RetargetedBranches != "" {
			out += fmt.Sprintf(
				"Note: git-spice retargeted upstack branch(es) %s onto %s's parent without rebasing them — restack deliberately when ready\n",
				result.RetargetedBranches, result.Branch)
		}
		return out
	}
	return fmt.Sprintf("Kept:\n  branch:  %s\n%sRemoved:\n  worktree: %s\n",
		result.Branch, archiveLine, result.WorktreePath)
}

// cleanRefused is cmdClean's sole gate on proceeding without --force: refused
// exactly when force was not passed AND session.CleanableWithoutForce says no.
// Delegating to that shared predicate (rather than re-testing target.Status.Phase
// here) is what makes a scratch session's exemption "no phase to be terminal"
// apply at this call site automatically instead of needing its own copy of the
// phase test.
func cleanRefused(force bool, target session.Session) bool {
	return !force && !session.CleanableWithoutForce(target)
}

// forcedCleanPrompt is the confirmation text shouldConfirmForcedClean gates
// on. The consequence sentence varies with --delete-branch because "the
// branch and archive are kept" is a false promise when that flag was also
// passed — the branch will be deleted right after the forced clean, and a
// user confirming on the unconditional wording would lose a branch they were
// told survives.
//
// A KindStatusWithoutWorktree target gets its own wording regardless of
// deleteBranch: "This removes the worktree" is itself false for that kind —
// its WorktreePath is the carrier, and cleanStatusWithoutWorktree touches
// nothing but `.afk/{name}/` (deleteBranch is refused earlier for this kind
// by deleteBranchNoOpRefused, so it can never reach here true, but the
// consequence sentence does not depend on that to be correct).
//
// Pure so the wording × flag × kind matrix is table-testable without a TTY.
func forcedCleanPrompt(name, phase string, deleteBranch bool, kind session.Kind) string {
	consequence := "This removes the worktree; the branch and archive are kept."
	switch {
	case kind == session.KindStatusWithoutWorktree:
		consequence = "This archives and removes the session directory; the worktree and branch are untouched."
	case deleteBranch:
		consequence = "This removes the worktree; the archive is kept, and --delete-branch removes the branch too."
	}
	return fmt.Sprintf("Session %q is in phase %q (not done/failed).\nForce-clean it? %s",
		name, phase, consequence)
}

// shouldConfirmForcedClean reports whether a forced clean of a session
// should pause for interactive confirmation. This is only true when all of
// the following hold: --force was passed, session.CleanableWithoutForce says
// the session is NOT cleanable without it (i.e. a non-terminal-phase planned
// session — normal clean of a terminal-phase or scratch session never
// prompts), and stdin is an interactive TTY. Non-TTY --force always proceeds
// without prompting, since scripts rely on that exact behavior.
//
// This calls session.CleanableWithoutForce — the same predicate cleanRefused
// calls above — rather than re-testing the phase itself, so the two decisions
// cannot drift apart the way they could when each inlined its own copy.
func shouldConfirmForcedClean(force bool, sess session.Session, tty bool) bool {
	if !force || !tty {
		return false
	}
	return !session.CleanableWithoutForce(sess)
}

// printSessions renders every discovered session (with phase). Discover
// already returns the slice sorted; a worktree-side fault and a
// status-without-worktree session are both ordinary rows in this same
// slice, with no separate section for either (see session.Kind).
//
// Output is styled with lipgloss when stdout is an interactive TTY (phase
// badges matching the dashboard's phase colors, aligned columns). When
// stdout is not a TTY (e.g. piped to `grep`/`cat`), printSessions falls back
// to the original plain-text format byte-for-byte, so scripts that parse
// `afk clean` output keep working.
func printSessions(sessions []session.Session) {
	if !isTerminal(os.Stdout) {
		printSessionsPlain(sessions)
		return
	}
	printSessionsStyled(sessions)
}

// printSessionsPlain is the original, unstyled rendering. Its format changed
// exactly once for this task — the fault/stranded section that used to
// follow the Sessions: list is gone, since every row it used to hold now
// prints as a Sessions: row instead — and must otherwise stay byte-identical:
// it is the machine-readable fallback for non-TTY stdout.
func printSessionsPlain(sessions []session.Session) {
	if len(sessions) == 0 {
		fmt.Println("No afk sessions found.")
		return
	}
	fmt.Println("Sessions:")
	for _, s := range sessions {
		fmt.Printf("  %-30s phase=%-10s branch=%-20s worktree=%s\n",
			s.Name, cleanPhasePlain(s), s.Branch, cleanWorktreeColumn(s))
	}
}

// cleanPhasePlain is the phase column printSessionsPlain prints for s.
//
// A scratch session has no status.json and so no phase, which left the field
// blank — the one session kind whose row said nothing at all about what it
// was, in the listing the user reaches for before deciding what to remove. It
// reports the same word the dashboard's badge uses (internal/ui's
// scratchPhaseKey), so both surfaces name the kind identically.
//
// A status-backed session whose status file is stale keeps its empty phase
// rather than borrowing the styled rendering's "unknown": printSessionsPlain
// is the machine-readable fallback scripts parse, so its columns hold still.
//
// A worktree-side fault prints the dashboard's fault vocabulary in this
// listing's lowercase (see brokenPhaseLabel/orphanPhaseLabel) instead: an
// empty phase would read as an ordinary session with no phase yet rather
// than the broken worktree it is. The raw Kind strings are not printable
// here — at 19 and 21 characters they overflow the `phase=%-10s` field and
// shift the branch= and worktree= columns on exactly those rows.
// KindStaleStatus is deliberately excluded — it predates this move and keeps
// the blank-phase precedent above.
func cleanPhasePlain(s session.Session) string {
	if s.Scratch {
		return scratchPhaseLabel
	}
	if s.Kind.WorktreeFault() {
		return cleanFaultLabel(s.Kind)
	}
	return s.Status.Phase
}

// brokenPhaseLabel and orphanPhaseLabel are the phase-column words for the
// two worktree-side faults, in both the plain and styled listings. They are
// the dashboard's BROKEN/ORPHAN badge vocabulary lowercased to match this
// listing's other phase words (done, failed, scratch), and both fit
// printSessionsPlain's 10-character phase field — see cleanPhasePlain for why
// the raw Kind strings cannot be printed instead.
const (
	brokenPhaseLabel = "broken"
	orphanPhaseLabel = "orphan"
)

// cleanFaultLabel maps a worktree-side fault Kind to its listing label,
// mirroring internal/ui's faultBadgeLabel kind-for-kind so the CLI and the
// dashboard never name the same fault differently.
func cleanFaultLabel(k session.Kind) string {
	if k == session.KindWorktreeMissing {
		return brokenPhaseLabel
	}
	return orphanPhaseLabel
}

// scratchPhaseLabel is what both `afk clean` renderings call a scratch
// session's phase. It mirrors internal/ui's scratchPhaseKey; the two are
// deliberately the same word so the dashboard badge and the CLI listing agree.
// TestScratchPhaseIdentityMatchesInternalUI pins the mirror by parsing
// internal/ui/styles.go, since internal/ui is off-limits to edit here and its
// scratchPhaseKey is unexported (no importable shared symbol is reachable).
const scratchPhaseLabel = "scratch"

// cleanWorktreeColumn is the worktree column both printSessionsPlain and
// printSessionsStyled print for s. A scratch session's WorktreePath holds the
// repo root it was created in (see session.Session.Scratch's doc comment) —
// not a worktree afk clean will ever act on — so printing it verbatim in
// the pre-clean listing reads as "this will be removed", the same misleading
// shape cleanSummary was fixed for (TestCleanSummaryScratchNamesNoEmptyField).
// This applies that same fix to the listing shown before a clean is chosen.
//
// A KindStatusWithoutWorktree session's WorktreePath is the CARRIER for the
// same reason — usually the user's own primary checkout — and gets its own
// label rather than reusing scratchWorktreeLabel: "one-off" describes a
// scratch session, not a status-backed one stranded in someone else's
// worktree.
func cleanWorktreeColumn(s session.Session) string {
	if s.Scratch {
		return scratchWorktreeLabel
	}
	if s.Kind == session.KindStatusWithoutWorktree {
		return statusOnlyWorktreeLabel
	}
	return s.WorktreePath
}

// scratchWorktreeLabel is what afk clean's listing prints in the worktree
// column for a scratch session in place of its WorktreePath.
const scratchWorktreeLabel = "(none — one-off, no worktree)"

// statusOnlyWorktreeLabel is what afk clean's listing prints in the
// worktree column for a KindStatusWithoutWorktree session in place of its
// (carrier) WorktreePath.
const statusOnlyWorktreeLabel = "(none — status only, no worktree of its own)"

// cleanBadgeStyle returns a padded, bold badge style (foreground fg on
// background bg) — the same badge construction the dashboard uses (see
// internal/ui/styles.go's newStyleSet badge() closure) so phase badges look
// identical between `afk clean` and the dashboard.
func cleanBadgeStyle(fg, bg string) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(fg)).
		Background(lipgloss.Color(bg)).
		Bold(true).
		Padding(0, 1)
}

// cleanPhaseBadges maps each lifecycle phase to a badge style, mirroring
// internal/ui/styles.go's phaseBadges map exactly (same palette colors, same
// fg/bg pairing per phase) so `afk clean`'s phase badges match the
// dashboard's.
var cleanPhaseBadges = map[string]lipgloss.Style{
	"planning":  cleanBadgeStyle(palette.White, palette.PhasePlanningBg),
	"executing": cleanBadgeStyle(palette.Black, palette.PhaseExecutingBg),
	"reviewing": cleanBadgeStyle(palette.Black, palette.PhaseReviewingBg),
	"fixing":    cleanBadgeStyle(palette.White, palette.PhaseFixingBg),
	"done":      cleanBadgeStyle(palette.Black, palette.PhaseDoneBg),
	"failed":    cleanBadgeStyle(palette.White, palette.PhaseFailedBg),
}

// cleanPhaseBadgeUnknown is the fallback badge for an empty/unrecognized
// phase (e.g. a stale status file), matching internal/ui/styles.go's
// phaseUnknown fallback.
var cleanPhaseBadgeUnknown = cleanBadgeStyle(palette.White, palette.Border)

// cleanPhaseBadgeScratch is the badge for a scratch session, mirroring the
// dashboard's own scratch badge (internal/ui/styles.go pairs palette.White
// with palette.ScratchBg for scratchPhaseKey) so the two surfaces match.
var cleanPhaseBadgeScratch = cleanBadgeStyle(palette.White, palette.ScratchBg)

// cleanFaultBadges are the worktree-side fault badges, mirroring
// internal/ui/styles.go's faultBadges color-for-color (BROKEN on the failed
// phase's background, ORPHAN on the warning color) so a fault looks the same
// in `afk clean`'s listing as in the dashboard row the user will clean it
// from.
var cleanFaultBadges = map[session.Kind]lipgloss.Style{
	session.KindWorktreeMissing:   cleanBadgeStyle(palette.White, palette.PhaseFailedBg),
	session.KindWorktreeNoSession: cleanBadgeStyle(palette.Black, palette.Warning),
}

// renderPhaseBadge renders a session's phase as a colored badge: its own badge
// for a scratch session, a fault badge for a worktree-side fault, then the
// per-phase badge, then a neutral "unknown" for an empty or unrecognized
// phase.
//
// scratch and the fault kinds are tested first and separately because their
// phase is legitimately empty — a scratch session has no status.json to hold
// one and a fault row's died with its directory or never existed — and both
// would otherwise land on "unknown", which is the opposite of true: their
// kinds are known exactly, and this listing is what a user reads before
// choosing what to remove. "unknown" belongs to the case it was written for,
// a status-backed session whose status file went stale. Labels come from
// cleanPhasePlain so the styled and plain listings can never disagree about
// the same row.
func renderPhaseBadge(s session.Session) string {
	if s.Scratch {
		return cleanPhaseBadgeScratch.Render(scratchPhaseLabel)
	}
	if s.Kind.WorktreeFault() {
		return cleanFaultBadges[s.Kind].Render(cleanPhasePlain(s))
	}
	phase := s.Status.Phase
	label := phase
	if label == "" {
		label = "unknown"
	}
	st, ok := cleanPhaseBadges[phase]
	if !ok {
		st = cleanPhaseBadgeUnknown
	}
	return st.Render(label)
}

// printSessionsStyled renders the same fields and line-per-session structure
// as printSessionsPlain, restyled with lipgloss: the session name in bold,
// the phase as a colored badge (see renderPhaseBadge), the branch in the
// dashboard's branch accent color, and aligned columns via lipgloss Width
// instead of manual %-Ns spacing.
func printSessionsStyled(sessions []session.Session) {
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Muted))
	name := lipgloss.NewStyle().Bold(true).Width(30)
	phaseCol := lipgloss.NewStyle().Width(12)
	branch := lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Branch)).Width(20)
	header := lipgloss.NewStyle().Bold(true)

	if len(sessions) == 0 {
		fmt.Println("No afk sessions found.")
		return
	}
	fmt.Println(header.Render("Sessions:"))
	for _, s := range sessions {
		fmt.Printf("  %s %s%s %s%s %s%s\n",
			name.Render(s.Name),
			label.Render("phase="), phaseCol.Render(renderPhaseBadge(s)),
			label.Render("branch="), branch.Render(s.Branch),
			label.Render("worktree="), cleanWorktreeColumn(s))
	}
}
