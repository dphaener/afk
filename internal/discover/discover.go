// Package discover implements read-only session discovery for `afk dash`
// (W5.T3+): it enumerates every git worktree registered against a repo,
// scans each for `.afk/*/status.json`, and joins the status file with its
// plan file and review verdict (via internal/state) into a SessionState.
//
// There is a second source, for scratch sessions: a `.afk/*/host.json`
// with no status.json beside it. `afk new --scratch` creates no plan,
// branch, or worktree and seeds no status file, so host.json is the only
// trace such a session leaves on disk. Those become SessionStates with
// Scratch set and every status-derived field at its zero value — notably
// StatusStale FALSE, since the absence of status.json is that session
// type's normal shape and not a broken file. A directory holding both files
// is a normal session and is never counted twice.
//
// This package is deliberately read-only and uncached: every call to
// Discover re-reads the filesystem and git from scratch. The dashboard
// re-runs Discover on its refresh ticker (status-schema.md's writers mutate
// these files continuously via re-read-merge + atomic rename), so a cached
// or stale view would silently show the wrong phase/progress/awaiting-input
// state. Session identity, status.json semantics, and the plan/verdict file
// locations all come from the binding contracts this package implements
// against without inventing anything:
//
//   - plugins/afk/skills/conductor/status-schema.md (in this repo) —
//     status.json shape, phase lifecycle, awaiting_input.
//   - status-schema.md §1's identity model — plan file lives at
//     `.afk/plans/{name}.md` *relative to the worktree the session runs
//     in*, alongside `.afk/{name}/status.json` and
//     `.afk/{name}/review-verdict.md` in the same worktree.
//
// Git-worktree discovery is source-of-truth via `git worktree list
// --porcelain` — this package never depends on worktrunk (`wt`) being
// installed; worktrees it creates appear in the same porcelain output as
// worktrees created any other way.
//
// The never-block guarantee, stated precisely, because `afk dash` calls
// Discover on a 2s refresh ticker and anything that stalls here freezes the
// whole dashboard for the length of the stall. Everything this package does
// is exactly one of three things, and there is no fourth:
//
//  1. Plain file reads and directory globs — os.ReadFile/os.Open/filepath.Glob,
//     via worktree.Observe (which runs state.ReadStatus and host.ReadMeta),
//     state.ParsePlanFile, state.ParseVerdictFile, host.ReadMeta and
//     session.ReadLabel.
//  2. One subprocess per call: `git worktree list --porcelain` (defaultRun,
//     os/exec), which this package does wait on. It is the only operation
//     here that is not a syscall on a local file, and therefore the only one
//     whose duration this package does not control; it is unavoidable
//     because worktree registration lives in the repo's git metadata and git
//     is its only supported reader. No argument this package passes git names
//     a remote.
//  3. host.Alive's signal-0 (syscall.Kill(pid, 0)), detailed below.
//
// What is NOT here: no dial, no connect, no unix-socket or any other IPC, no
// network operation of any kind, no write, no lock, no sleep. Socket liveness
// is exclusively `afk attach`'s concern (internal/host.Dial's own
// dial-retry).
//
// That absence is enforced mechanically, not by convention, and it has to be:
// `net` IS in this package's transitive dependency graph — internal/host and
// internal/session both import it for their own dialing (host.Dial,
// clean.go's dialExit), neither of which this package calls — so "the binary
// can't dial" is not a property the import graph can give us. What
// TestDiscoverNeverBlocks pins instead is discover.go's own surface: an
// allowlist of its direct imports, plus the exact set of identifiers it may
// reference through internal/host (ReadMeta, Alive) and internal/session
// (ReadLabel, ReadProvider, IsAutonomous). Reaching for host.Dial, or for any of internal/session's
// git-mutating teardown, fails the suite rather than passing review.
//
// One sanctioned non-file check: HostRunning. Discover's contract is
// otherwise "file reads only" — every field above comes from parsing
// status.json, a plan file, or a verdict file. HostRunning is populated by
// reading `.afk/<name>/host.json` (via internal/host.ReadMeta, itself a
// plain file read) and, only if that succeeds, calling host.Alive(pid) — a
// non-blocking signal-0 syscall (syscall.Kill(pid, 0)) that checks process
// existence without sending a signal and cannot block. That non-blocking
// guarantee is exactly why it's allowed under the ticker contract above
// while a real liveness check — dialing the host's unix socket — is not.
//
// HostRunning is ADVISORY, not authoritative: signal-0 only proves *some*
// process holds that PID right now, and PIDs get recycled by the OS after
// the original process exits. A recycled PID can false-positive
// HostRunning: true for a host that is actually gone. The authoritative
// check is always the attach dial; W4.T1 handles the false-positive path by
// treating a "running" session whose attach dial fails as stopped and
// offering to restart it. A missing host.json, or one naming a PID that is
// no longer alive, means HostRunning: false — never an error, since a stale
// host.json for an exited host is an entirely ordinary, expected state.
//
// A second, non-file-reading dependency worth flagging explicitly here
// rather than only at its one call site (applyLabel): this package imports
// internal/session — the package that also owns `git worktree remove` and
// every other mutating step of session creation/teardown — for exactly one
// function, ReadLabel, plus the Kind vocabulary, which is a type and
// constants and so has no runtime surface to carry. ReadLabel is itself a
// plain os.Open plus a bounded read of `.afk/{name}/label` (see its own doc
// comment), so it carries none of internal/session's git-mutating surface or
// blocking potential across the import; nothing in this package ever calls
// anything else internal/session exports, which TestDiscoverNeverBlocks pins
// as a selector allowlist rather than leaving to review. The alternative
// — duplicating the label sidecar's path/shape here instead of importing the
// one definition — was
// rejected because it would let the two packages' readers silently drift
// apart, exactly the failure mode host.json's shared internal/host.ReadMeta
// (above) already avoids by the same one-definition argument.
package discover

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/worktree"
)

// Progress tallies a session's plan tasks by checkbox state. The zero value
// (all fields 0) is the correct, first-class representation of a session
// that has no plan file yet — most commonly a session still in phase
// `planning`, since `afk new` seeds status.json before the interview runs
// and produces the plan file only once it completes (status-schema.md §8).
type Progress struct {
	Total      int
	Complete   int
	Failed     int
	Incomplete int
}

// SessionState is one discovered afk session: a `.afk/{name}/` directory
// found inside some worktree, with its status.json, plan file, and review
// verdict (when present) joined together.
type SessionState struct {
	// Name is the session name / plan slug (status-schema.md §1: the
	// containing directory name, which status.json's own plan_name field
	// must equal for a valid, non-stale status file).
	Name string
	// Label is the mutable display-label sidecar's text (`.afk/{name}/label`,
	// internal/session's ReadLabel/WriteLabel, W4.T1), or "" when unset. It is
	// display-only, exactly like host.json's LastOutputAt/WedgedSince below:
	// missing, empty, or unreadable are all indistinguishable "no label"
	// (never an error, never a staleness signal), and it never substitutes
	// for Name anywhere Name is used as an on-disk identity (branch, worktree
	// path, socket key) — only a renderer choosing what to SHOW consults it.
	Label string
	// Autonomous reports that `.afk/{name}/autonomous` exists
	// (internal/session's IsAutonomous): the conductor runs without
	// interactive gates, and the marker is what carries that mode across a
	// dashboard restart that re-runs `/afk:go --session {name}` without the
	// flag. It is a single stat and the marker's contents are never read.
	// False for scratch sessions (no conductor) and when the marker is
	// absent. Independent of status.json, so a stale session still carries it.
	Autonomous bool
	// Provider is the canonical agent-CLI name from the
	// `.afk/{name}/provider` sidecar (internal/session's ReadProvider), or
	// "" for the default provider (claude): an absent, empty, unreadable, or
	// explicitly "claude" sidecar all collapse to "" so a consumer only ever
	// sees a non-empty value for a session that needs calling out. Read from
	// the sidecar rather than host.json because the sidecar survives a host
	// stop, so a stopped Codex session still reports its provider. Display
	// only — never an error and never a staleness signal.
	Provider string
	// WorktreePath is the git worktree hosting this session — the main
	// checkout itself is a valid host, not just linked worktrees.
	WorktreePath string
	// Scratch marks a session discovered from `.afk/{name}/host.json`
	// with no status.json beside it: a one-off `afk new --scratch`
	// session, which runs a bare `claude` in the repo root with no plan, no
	// branch, and no worktree of its own (internal/session's scratch mode).
	//
	// It is a KIND marker, never a health signal. A scratch session is
	// entirely well-formed; it simply has no lifecycle to report. Every
	// status-derived field on this struct is therefore the zero value for
	// one — SessionID, Branch, BaseBranch, Phase, LoopCount, AwaitingInput,
	// UpdatedAt, Progress, Plan, Verdict — and StatusStale is FALSE,
	// because a missing status.json is this session type's normal on-disk
	// shape rather than a broken status file. Consumers must branch on
	// Scratch before reading Phase: an empty Phase means "has no lifecycle"
	// here, where everywhere else it means "unknown, status is stale".
	Scratch bool
	// SessionID is status.json's `session_id` field: the agent session
	// identifier a conductor conversation resumes via the session's
	// provider (`claude --resume <session_id>`, or `codex resume
	// <session_id>`; a bare `claude --resume` with no ID instead opens
	// claude's interactive session picker). Carried through as-is; it is the
	// zero value ("") when StatusStale is true, and is schema-valid-empty
	// only while Phase is `planning` (state.ReadStatus enforces this
	// invariant before Discover ever sees the parsed status).
	SessionID string
	// Branch is status.json's own `branch` field: "the working branch for
	// this session" (status-schema.md §2), carried through as-is.
	Branch string
	// BaseBranch is status.json's `base_branch` field.
	BaseBranch string
	// Phase is the session's lifecycle phase. It is the zero value ("")
	// when StatusStale is true — callers must check StatusStale before
	// trusting Phase (and every other status-derived field below).
	Phase state.Phase
	// LoopCount is status.json's `loop_count` field.
	LoopCount int
	// AwaitingInput is status.json's `awaiting_input` field.
	AwaitingInput bool
	// UpdatedAt is status.json's `updated_at` field (RFC 3339 UTC), useful
	// for a dashboard to judge the age of a possibly-stale AwaitingInput
	// per status-schema.md §5's "Known, accepted staleness window".
	UpdatedAt string

	// Kind names this row's fault in the vocabulary internal/session defines
	// and `afk clean` acts on, KindHealthy when it has none.
	//
	// KindStaleStatus here means StatusStale UNDER THIS PACKAGE'S PARSER, and
	// that is not the same file set internal/session calls stale: trailing
	// data, a plan_name mismatch, an unknown phase and an empty session_id
	// outside `planning` are all stale to state.ReadStatus and decode
	// cleanly for internal/session. Sharing the vocabulary is what lets the
	// dashboard hand a row to `afk clean` by (name, kind); it does not
	// make the two packages' verdicts identical, and internal/ui's
	// dashboardPhase exists precisely because they differ.
	//
	// KindStatusWithoutWorktree is the one kind where the two packages DO
	// agree on identity, and it overrides KindStaleStatus when the file also
	// fails this package's own parse: a status.json whose carrier worktree is
	// checked out on another branch (or is detached) routes to
	// internal/session's zero-git teardown regardless of whether the file
	// parses, so the kind here has to say so too. The row still renders as an
	// ordinary one (see the package doc); only clean identity is shared.
	//
	// StaleReason, not this field, remains the place the STALE reason lives.
	Kind session.Kind
	// Reason explains a worktree-side fault Kind, or a KindStatusWithoutWorktree
	// row's carrier mismatch, in the words a user reads; empty for every other
	// row. It exists because a worktree-side fault has no phase, no progress
	// and no status file — its second line has nothing else to show without
	// it — and because a KindStatusWithoutWorktree row otherwise carries no
	// hint that its clean identity differs from an ordinary healthy row's.
	//
	// It deliberately names no path. The dashboard's row is a sidebar line
	// that truncates at its own width, and a path is the first thing that
	// would eat it; WorktreePath is a field of its own for the surfaces (the
	// detail pane, `afk clean`) that have room for it.
	Reason string
	// StatusStale is true when status.json could not be read as a valid,
	// schema-conformant file (missing, unparseable, or violating an
	// invariant — see internal/state's StaleReason). Every status-derived
	// field above is the zero value in that case; the session is still
	// surfaced (its directory exists) rather than silently dropped.
	StatusStale bool
	// StaleReason explains StatusStale; zero value ("") when not stale.
	StaleReason state.StaleReason

	// Progress is the task-checkbox tally from the plan file. It is the
	// zero value both when there is no plan file yet (first-class planning
	// state, not an error) and when status is stale.
	Progress Progress
	// Plan is the fully-parsed plan file (waves + tasks with their checkbox
	// states), or nil when there is no plan file yet (a session still in
	// `planning`) or when the plan failed to parse (see PlanError). Progress
	// is derived from it; consumers that need the per-task breakdown (e.g.
	// the `afk dash` detail pane) read Plan directly rather than re-parsing.
	Plan *state.Plan
	// PlanError is set when a plan file exists but failed to parse (e.g. a
	// writer is mid-write, or the file is genuinely malformed). Empty when
	// there is simply no plan file yet, and empty on success.
	PlanError string

	// Verdict is the parsed review-verdict.md, or nil when the session has
	// no verdict file yet (true for any session that hasn't reached
	// `reviewing` at least once).
	Verdict *state.ReviewVerdict
	// VerdictError is set when a review-verdict.md file exists but failed
	// to parse. Empty when there is simply no verdict file yet, and empty
	// on success.
	VerdictError string

	// HostRunning reports whether a host process appears to be alive for
	// this session, per host.json + signal-0 (see the package doc's
	// "sanctioned non-file check" section). ADVISORY ONLY: a recycled PID
	// can false-positive this to true. False whenever host.json is absent
	// or names a PID that is no longer alive — never an error either way.
	HostRunning bool
	// HostPID is the PID recorded in host.json, or 0 when host.json is
	// absent or unreadable. Set even when HostRunning is false (e.g. a
	// stale host.json naming a dead PID), so callers can log/debug it.
	HostPID int
	// LastOutputAt is host.json's `last_output_at` field (internal/host's
	// Meta, W1.T2): an RFC 3339 UTC timestamp of the most recent byte the
	// host's PTY pump observed from the child. Empty ("") means unknown —
	// host.json is absent, unparseable, or (same as a pre-W1.T2 host) never
	// wrote the field — never an error and never distinguished from one
	// another, exactly like HostPID/HostRunning above.
	LastOutputAt string
	// WedgedSince is host.json's `wedged_since` field: set by the host's
	// liveness monitor once an attached session's child has gone silent
	// past its probe deadline, and cleared the instant output resumes.
	// Empty means "not currently flagged wedged", which reads the same as
	// "unknown" (absent/unparseable host.json, or a pre-W1.T2 host) since
	// there is no way to tell those apart from the file alone.
	WedgedSince string
}

// Deps is the injectable seam for discover's one environment-touching
// operation (listing worktrees via git). Real wiring comes from
// DefaultDeps; tests may substitute a fake to exercise parsing in isolation,
// though this package's own tests mostly favor real scripted git repos —
// discovery's entire value proposition is faithfully reflecting real git
// worktree state and real on-disk files.
type Deps struct {
	// Run executes name+args in dir and returns stdout.
	Run func(dir, name string, args ...string) (string, error)
}

// DefaultDeps returns Deps wired to the real `git` binary.
func DefaultDeps() Deps {
	return Deps{Run: defaultRun}
}

func (d Deps) withDefaults() Deps {
	if d.Run == nil {
		d.Run = defaultRun
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

// Discover enumerates every worktree registered against repo (via `git
// worktree list --porcelain`, run from repo — since worktree registration
// lives in the repo's shared git metadata, this returns the complete list
// regardless of which worktree repo itself happens to be), scans each
// non-bare worktree (including the main checkout — it is a valid session
// host, not just linked worktrees) through worktree.Observe, and joins each
// status-backed `.afk/{name}/` with its plan file and review verdict into a
// SessionState. Observe takes the `.afk/*/status.json` and
// `.afk/*/host.json` glob pair once and reports both results per directory,
// so a directory holding a host.json and no status.json becomes a scratch
// SessionState (see SessionState.Scratch) and a directory holding both yields
// exactly one, non-scratch, entry. The combined result is sorted by
// sortSessionStates — primarily by Name, so scratch and status-backed
// sessions interleave rather than grouping by source, and then by a
// tie-break total enough that two sessions sharing a name always come back in
// the same order (see that function; the order matches internal/session's).
//
// A status-backed entry whose carrier worktree is checked out on a branch
// other than the directory's own name (including a detached carrier) gets
// Kind: session.KindStatusWithoutWorktree — the same kind internal/session's
// own Discover uses for the identical directory, which is what lets a
// dashboard row resolve to the right `afk clean` teardown by (name, kind).
// It still renders here as an ordinary row (see buildSessionState); the two
// packages agree on identity and differ only in how the row is presented.
//
// Discover makes no writes and caches nothing: every call re-reads git and
// the filesystem from scratch, by design (see the package doc).
func Discover(repo string, d Deps) ([]SessionState, error) {
	d = d.withDefaults()

	worktrees, err := worktree.List(d.Run, repo)
	if err != nil {
		return nil, err
	}

	var out []SessionState
	for _, wt := range worktrees {
		if wt.Bare {
			continue
		}

		obs, err := worktree.Observe(wt, worktree.Deps{})
		if err != nil {
			return nil, err
		}
		// A directory holding a host.json and no status.json is a scratch
		// session — `afk new --scratch` seeds neither a status file nor a
		// plan, so host.json is the only trace it leaves on disk.
		//
		// HasStatusFile is the whole test, and it comes from the one glob
		// pair Observe already took, never from an independent stat of the
		// sibling status.json. Both would answer the same question, but only
		// the observation answers it from the SAME look that classified the
		// directory: a writer creating or removing status.json between the
		// two calls could otherwise make a stat disagree with the glob and
		// emit one `.afk/{name}/` twice, once per kind.
		for _, fd := range obs.Dirs {
			if fd.HasStatusFile {
				out = append(out, buildSessionState(obs, fd))
				continue
			}
			out = append(out, buildScratchSessionState(obs, fd))
		}

		// All four clauses are load-bearing, and they are internal/session's
		// four, unchanged — the two packages share these FACTS even where
		// their policies differ. Primary: the repo's own checkout is the
		// user's, never a afk-created worktree. Branch: a worktree with no
		// branch has no name for a session directory to equal. Detached: same,
		// and it is the shape a deleted directory leaves with nothing to name
		// it by. MatchedOwnBranch: without it every healthy session grows a
		// duplicate fault row of itself, and it is deliberately
		// status-backed-only, so a scratch `.afk/{branch}/` in this worktree
		// does not suppress a real fault.
		if !obs.Primary && obs.Branch != "" && !obs.Detached && !obs.MatchedOwnBranch {
			out = append(out, worktreeFaultState(obs))
		}
	}

	sortSessionStates(out)
	return out, nil
}

// sortSessionStates orders discovered sessions by a key total enough that no
// two distinct entries ever compare equal.
//
// Name alone is not that key, and this package learned that second. A scratch
// `.afk/dup/` in the main checkout and a linked worktree checked out on
// branch `dup` carrying its own `.afk/dup/status.json` are both legitimate,
// and Discover reports both under the name `dup`. sort.Slice is documented as
// not stable, so a name-only comparator leaves that pair in whatever order the
// sort happened to land on — while every consumer resolves a name by taking
// the FIRST match (attach.go's findSessionWorktree, internal/ui's
// resolveFocusTarget and sessionByName). That makes `afk attach dup` and a
// click-to-focus on `dup` aim at a different session run to run.
//
// The key is deliberately the same one internal/session's sortSessions uses
// (clean.go), including the direction of the Scratch tie-break: the
// status-backed entry wins, because its name IS a branch name and it is the
// only one of the pair whose clean has a worktree to remove. The two packages
// order the same real-world entities for callers that all take the first
// match, so a disagreement between them means one name resolves to two
// different sessions depending only on which package asked. They cannot share
// one comparator — the inputs are session.Session and discover.SessionState,
// and internal/session cannot import this package (the dependency runs the
// other way) — so the agreement is pinned by test instead, in
// TestDiscoverOrdersNameCollisionsLikeSessionDiscover.
//
// There is no StatusPath tie-break here, where internal/session has one,
// because this package does not carry that path on a row. Kind is the final
// tie-break instead, and it is what keeps the totality claim above true by
// construction rather than by case analysis: rows now come from THREE sources
// (the status glob, the host.json pass, and worktreeFaultState), and arguing
// that no two of them can agree on name, scratch-ness and worktree path is
// exactly the kind of argument a later change quietly invalidates.
func sortSessionStates(out []SessionState) {
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Scratch != b.Scratch {
			return !a.Scratch
		}
		if a.WorktreePath != b.WorktreePath {
			return a.WorktreePath < b.WorktreePath
		}
		return a.Kind < b.Kind
	})
}

// statusWithoutWorktreeReason is SessionState.Reason for a row whose carrier
// worktree is checked out on a branch other than the session's own name (or
// is detached). It names no path, matching Reason's own doc — the worktree
// fault rows below are the only other producer of Reason, and this row still
// renders as an ordinary one, so nothing reads this text yet; it exists so a
// future surface (a detail pane, `afk clean`'s listing) has it without
// re-deriving the fact from Kind alone.
const statusWithoutWorktreeReason = "status file lives in a worktree checked out on another branch"

// buildSessionState joins the observed status parse for one `.afk/{name}/`
// directory with its plan file and review verdict.
//
// The status result arrives already parsed, in fd.StatusSchema: Observe ran
// state.ReadStatus once for this directory and re-running it here would be a
// second read of a file a writer mutates continuously, able to disagree with
// the first.
func buildSessionState(obs worktree.Observation, fd worktree.AfkDir) SessionState {
	ss := SessionState{Name: fd.Name, WorktreePath: obs.Path}
	applyLabel(&ss)
	applyProvider(&ss)
	// Before the stale early-return below, like the label: the marker is its
	// own file and says nothing about whether status.json parses. Not called
	// from buildScratchSessionState, which has no conductor to be autonomous.
	ss.Autonomous = session.IsAutonomous(ss.WorktreePath, ss.Name)

	// HostRunning/HostPID come from host.json, entirely independent of
	// status.json's own validity — a stale/unparseable status.json says
	// nothing about whether the host process is alive (that's the whole
	// point of the STALE-but-HostRunning-true "attach anyway" case W4.T1
	// handles). Populate this BEFORE the stale early-return below so a
	// session with a stale status.json but a live host still reports
	// HostRunning: true.
	applyHostMeta(&ss)

	// A carrier mismatch — this worktree's checked-out branch is not fd.Name,
	// including a detached carrier (obs.Branch == "") — is the one fact
	// internal/session's Discover uses to route this same directory to its
	// zero-git teardown instead of cleanNormalSession. Checking it before the
	// stale early-return, and applying it to BOTH paths below, is what keeps
	// this package's clean identity agreeing with internal/session's for the
	// same directory regardless of whether the file happens to parse: a
	// corrupt status.json in a mismatched carrier is still
	// KindStatusWithoutWorktree, overriding KindStaleStatus, because the
	// mismatch is what the clean route steers by, not the parse result.
	stranded := obs.Branch == "" || fd.Name != obs.Branch

	res := fd.StatusSchema
	if res.Stale {
		ss.StatusStale = true
		ss.StaleReason = res.Reason
		ss.Kind = session.KindStaleStatus
		if stranded {
			ss.Kind = session.KindStatusWithoutWorktree
			ss.Reason = statusWithoutWorktreeReason
		}
		return ss
	}
	st := res.Status
	ss.SessionID = st.SessionID
	ss.Phase = st.Phase
	ss.Branch = st.Branch
	ss.BaseBranch = st.BaseBranch
	ss.LoopCount = st.LoopCount
	ss.AwaitingInput = st.AwaitingInput
	ss.UpdatedAt = st.UpdatedAt
	if stranded {
		ss.Kind = session.KindStatusWithoutWorktree
		ss.Reason = statusWithoutWorktreeReason
	}

	// Plan file: status-schema.md §1, `.afk/plans/{name}.md`, relative to
	// this same worktree. A phase-`planning` session typically has none yet
	// (afk new seeds status.json before the interview runs) — that is a
	// first-class state, not an error, so a missing plan file leaves
	// Progress at its zero value with no PlanError.
	planPath := filepath.Join(obs.Path, ".afk", "plans", fd.Name+".md")
	plan, perr := state.ParsePlanFile(planPath)
	switch {
	case perr == nil:
		ss.Plan = plan
		ss.Progress = progressFromPlan(plan)
	case errors.Is(perr, os.ErrNotExist):
		// No plan file yet — leave Progress at its zero value.
	default:
		ss.PlanError = perr.Error()
	}

	// Review verdict: status-schema.md §1, `.afk/{name}/review-verdict.md`.
	// Absent for any session that has not yet reached `reviewing`.
	verdictPath := filepath.Join(obs.Path, ".afk", fd.Name, "review-verdict.md")
	verdict, verr := state.ParseVerdictFile(verdictPath)
	switch {
	case verr == nil:
		ss.Verdict = verdict
	case errors.Is(verr, os.ErrNotExist):
		// No verdict file yet.
	default:
		ss.VerdictError = verr.Error()
	}

	// Re-read host.json here, after status/plan/verdict parsing, in addition
	// to the early read above: the two calls are not redundant. The early
	// one exists solely to cover the STALE early-return path a few lines up;
	// this one is what the non-stale return actually reports, so a
	// status-backed session's HostRunning/LastOutputAt/WedgedSince reflect
	// host.json as of just before Discover hands the SessionState back, not
	// as of before the (unbounded-ish, file-count-scaling) plan/verdict
	// parsing above it. Collapsing this to a single early call would shift
	// every status-backed session's liveness snapshot to an earlier point in
	// the tick — an observable behavior change the task that introduced
	// scratch discovery (W2.T2) explicitly ruled out ("do not change how
	// existing status.json-backed sessions are discovered or reported").
	applyHostMeta(&ss)

	return ss
}

// worktreeFaultKind splits the worktree-side fault on the one fact that
// decides its teardown: whether the registered directory is still there.
//
// Pure, and separate from Discover, for the reason internal/session's
// identically-shaped function gives — the two kinds need opposite teardowns,
// and the dashboard hands this value to `afk clean` as half of the row's
// identity. It returns internal/session's constants rather than a local enum
// so a row and the Session it resolves to are comparable without a string
// conversion across the seam.
func worktreeFaultKind(dirOnDisk bool) session.Kind {
	if dirOnDisk {
		return session.KindWorktreeNoSession
	}
	return session.KindWorktreeMissing
}

// worktreeFaultState builds the row for a worktree that carries no session of
// its own. It is the ONE place this package's fault rows are constructed, so
// the kind, the reason, and the two fields a teardown steers by can only come
// from the observation that produced the fault.
//
// Deliberately asymmetric with the other two builders: no label and no host
// metadata are read. Both live under `.afk/{name}/`, and the whole reason
// this entry exists is that no such directory belongs to it — a `.afk/{name}/`
// sitting in the same worktree is some other session's (a scratch one-off,
// which is why MatchedOwnBranch ignores those). The fault row's HostRunning
// therefore stays false, which is also what keeps the dashboard's auto-preview
// from ever dialing one.
func worktreeFaultState(obs worktree.Observation) SessionState {
	kind := worktreeFaultKind(obs.DirOnDisk)
	reason := "worktree has no afk session"
	if kind == session.KindWorktreeMissing {
		reason = "worktree directory no longer exists"
	}
	return SessionState{
		Name:         obs.Branch,
		WorktreePath: obs.Path,
		Branch:       obs.Branch,
		Kind:         kind,
		Reason:       reason,
	}
}

// buildScratchSessionState builds the SessionState for a `.afk/{name}/`
// directory that holds a host.json and no status.json — a scratch session
// (see SessionState.Scratch).
//
// Deliberately asymmetric with buildSessionState: it reads host.json and
// nothing else. No status.json exists to read; `.afk/plans/{name}.md` is
// not consulted because a scratch name comes from a counter that never
// looks at the plans directory (internal/session.NextScratchName), so a
// same-named plan file belongs to an unrelated session and joining it in
// would report another plan's progress here; and review-verdict.md cannot
// exist for a session that never reaches `reviewing`. Every one of those
// fields stays at its zero value, and StatusStale stays FALSE — a scratch
// session is not a session with a broken status file.
func buildScratchSessionState(obs worktree.Observation, fd worktree.AfkDir) SessionState {
	ss := SessionState{Name: fd.Name, WorktreePath: obs.Path, Scratch: true}
	applyLabel(&ss)
	applyProvider(&ss)
	applyHostMeta(&ss)
	return ss
}

// applyLabel fills ss.Label from `.afk/{ss.Name}/label` under
// ss.WorktreePath via internal/session.ReadLabel, leaving it at its zero
// value ("") when the file is absent, empty, or unreadable — never an error,
// exactly like applyHostMeta below for host.json. A plain file read (via
// os.Open — see ReadLabel), so this cannot block the dashboard's ticker.
func applyLabel(ss *SessionState) {
	if label, ok := session.ReadLabel(ss.WorktreePath, ss.Name); ok {
		ss.Label = label
	}
}

// applyProvider fills ss.Provider from `.afk/{ss.Name}/provider` under
// ss.WorktreePath via internal/session.ReadProvider, canonicalizing the
// default provider to "" and leaving it "" on any read failure (ReadProvider
// already folds errors into ""). A plain os.ReadFile, so it cannot block the
// dashboard's ticker. Called from both builders, before any stale early-return:
// the sidecar is its own file and says nothing about status.json.
func applyProvider(ss *SessionState) {
	if name := session.ReadProvider(ss.WorktreePath, ss.Name); name != provider.Default {
		ss.Provider = name
	}
}

// applyHostMeta fills ss's host-derived fields from
// `.afk/{ss.Name}/host.json` under ss.WorktreePath, leaving them at their
// zero values when that file is absent or unparseable — never an error
// either way, since a stale or missing host.json for an exited host is an
// ordinary state (see the package doc). host.Alive is the sanctioned
// non-blocking signal-0 check; nothing here dials or blocks.
//
// It is called from three sites, and each has its own reason rather than a
// shared one. buildScratchSessionState calls it once because host.json is the
// only file a scratch session has. buildSessionState calls it twice: the first
// call precedes state.ReadStatus so the STALE early-return still carries
// liveness (host liveness is independent of status.json's validity — that is
// what makes the STALE-status-but-live-host "attach anyway" case reachable),
// and the second deliberately runs AFTER status/plan/verdict parsing so the
// non-stale return reports host.json as of the end of the tick rather than as
// of before that parsing. The two are re-reads of the same file, not a
// populate-then-refine pair, so the second overwrites the first outright.
func applyHostMeta(ss *SessionState) {
	meta, err := host.ReadMeta(ss.WorktreePath, ss.Name)
	if err != nil {
		return
	}
	ss.HostPID = meta.PID
	ss.HostRunning = host.Alive(meta.PID)
	ss.LastOutputAt = meta.LastOutputAt
	ss.WedgedSince = meta.WedgedSince
}

// progressFromPlan tallies every task across every wave of plan by checkbox
// state.
func progressFromPlan(plan *state.Plan) Progress {
	var p Progress
	for _, w := range plan.Waves {
		for _, t := range w.Tasks {
			p.Total++
			switch t.State {
			case state.CheckComplete:
				p.Complete++
			case state.CheckFailed:
				p.Failed++
			default:
				p.Incomplete++
			}
		}
	}
	return p
}
