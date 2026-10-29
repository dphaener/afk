// Lifecycle cleanup for `afk clean`. This file implements the
// claude-squad pattern: for a selected finished/abandoned session, snapshot
// any dirty tracked changes, archive the (gitignored) `.afk/{name}/`
// directory somewhere durable, remove the worktree, and prune. The branch is
// kept unless the caller explicitly opts in via CleanOptions.DeleteBranch
// (see CleanSessionWithOptions) — CleanSession, the zero-options wrapper
// every existing caller still uses, never deletes it, which is what
// TestCleanSession_NeverDeletesBranch guards. It also reconciles the
// `.afk/*/status.json` files discovered inside every worktree against `git
// worktree list --porcelain`, and every mismatch that reconciliation finds is
// itself a Session carrying a Kind (see Kind) — because a mismatch the user
// can see and cannot clean is the bug this vocabulary exists to prevent.
//
// Scope decision (see decisions.md (W4.T4)): "worktree without status" is
// only flagged for NON-primary worktrees. The primary worktree (the repo's
// own main working directory, always first in `git worktree list
// --porcelain`) is not itself afk-created — flagging it merely because the
// user hasn't run a afk session there yet would be pure noise. A standalone
// `/afk:go` session (run directly in the primary worktree, per
// status-schema.md's "standalone path") is still discovered, as a
// KindStatusWithoutWorktree Session, because the status-side check is
// unscoped.
//
// A non-primary worktree that fails that check owns something a teardown has
// to remove, unlike a status directory whose carrier the user is standing in.
// Which teardown depends on one fact — whether its directory is still on disk
// — and the two answers need opposite handling: see CleanSession.
//
// Scratch sessions are the shape none of that reconciliation applies to at
// all: no status.json to reconcile against anything, no branch, and no
// worktree of their own to remove. They are discovered from host.json and
// cleaned by stopping the host and removing their directory. See the Scratch
// field on Session and cleanScratchSession. KindStatusWithoutWorktree ends up
// with the same SHAPE of teardown for a different reason — it is status-backed
// and named, but the worktree its status file sits in belongs to someone else
// (see cleanStatusWithoutWorktree).
//
// The disk facts all of that reasons over come from internal/worktree, which
// this package shares with internal/discover: one porcelain parse, one
// `.afk` glob pair per worktree, both status parsers run once. The two
// packages also agree on IDENTITY — a status directory whose carrier worktree
// is checked out on another branch is KindStatusWithoutWorktree in both, so
// the dashboard's (name, scratch, kind) key and this package's by-name
// resolution can never disagree about which session a user meant. What is NOT
// shared is the POLICY each applies to those facts: internal/discover renders
// that row as an ordinary dashboard row (pane, preview and attach all work,
// because a standalone `/afk:go` session is usually live), while this
// package routes it to a teardown that touches nothing but `.afk/{name}/`.
// See cleanStatusWithoutWorktree.
package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/spice"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/worktree"
)

// Kind names how a discovered entry is faulted, in one vocabulary both
// scanners and the dashboard speak.
//
// It is an enum rather than a broken/not-broken bool because the members are
// not interchangeable, in two directions:
//
//   - Three of them are IDENTITY-relevant: the two worktree-side kinds and
//     KindStatusWithoutWorktree. A missing directory, a live foreign worktree
//     and a status directory stranded in someone else's checkout need three
//     different teardowns, and a healthy session can share a name with any of
//     them, so resolving a clean target has to match on the kind as well as
//     the name. IdentityRelevant is that test.
//   - KindStaleStatus is NOT identity-relevant and must keep resolving
//     exactly as it does today. A corrupt status.json is still an ordinary
//     session under an ordinary name; treat it as a distinct identity and
//     every malformed-JSON row starts failing to clean with `no session named
//     %q found`, which is the exact case internal/ui's dashboardPhase exists
//     to keep reachable.
type Kind string

const (
	// KindHealthy is the zero value on purpose: a Session assembled anywhere
	// other than Discover carries no fault, which is what every caller
	// predating this vocabulary already assumed. A scratch session is
	// KindHealthy too — `afk new --scratch` produces a well-formed one-off,
	// not a broken session, and scratch is decided before any fault is
	// considered.
	KindHealthy Kind = ""
	// KindStaleStatus marks a `.afk/{name}/status.json` this package could
	// not decode. Note "this package": internal/state's schema parser rejects
	// four further shapes internal/session accepts (see internal/worktree's
	// package doc), so the two scanners label different files stale, on
	// purpose.
	KindStaleStatus Kind = "stale-status"
	// KindWorktreeMissing marks a worktree git still has registered whose
	// directory was deleted out from under it. Its `.afk` glob returns
	// nothing because the whole path is gone, so it is invisible to every
	// status-side check.
	KindWorktreeMissing Kind = "worktree-no-directory"
	// KindWorktreeNoSession marks a worktree that is present on disk and
	// checked out on a branch, but carries no session of its own. That
	// deliberately includes worktrees afk never created: an unrelated
	// worktree looks exactly like one whose status file was lost, and the
	// teardown has to assume the stranger.
	KindWorktreeNoSession Kind = "worktree-no-session"
	// KindStatusWithoutWorktree marks a status-backed `.afk/{name}/` whose
	// CARRIER worktree is not checked out on branch {name}: a standalone
	// `/afk:go` session in the primary checkout (the common case, per
	// status-schema.md's "standalone path"), or a worktree whose branch was
	// switched or renamed after the session directory was created.
	//
	// Its WorktreePath is that carrier, which is usually the user's own main
	// checkout, and it must NEVER be the target of a git command: `git add
	// -A` and `git commit` aimed there sweep up the user's uncommitted work,
	// and `git worktree remove` aims at their checkout. That is the whole
	// reason this is its own kind rather than a healthy session — see
	// cleanStatusWithoutWorktree, which runs no git at all.
	//
	// It IS identity-relevant. A healthy session `X` in its own worktree and
	// a stranded `.afk/X/` in the main checkout are two DIFFERENT sessions
	// under one name with opposite teardowns, so a by-name resolution must
	// match on the kind too (see IdentityRelevant).
	//
	// It also OVERRIDES KindStaleStatus when both apply: a corrupt
	// status.json in a mismatched carrier is this kind, not a stale one.
	// Routing on the carrier mismatch is the safety property, and a lesser
	// fault winning the label is what would send the teardown back into the
	// user's checkout with git. The stale fact is not lost — it arrives as an
	// empty Status.Phase, which is exactly what CleanableWithoutForce reads
	// to require a dead host before this kind cleans without --force.
	KindStatusWithoutWorktree Kind = "status-without-worktree"
)

// WorktreeFault reports whether k is one of the two WORKTREE-SIDE faults: an
// entry whose problem is the worktree itself, either gone from disk or
// present but carrying no session.
//
// This is deliberately NARROWER than IdentityRelevant and must stay so. It is
// the test for a row that owns no live session — the dashboard renders those
// as inert fault rows with a badge, opens no pane on them, and `afk attach`
// skips them, all correctly, because there is no session behind them to
// reach. KindStatusWithoutWorktree fails this test on purpose: its carrier is
// on disk and its session is very often LIVE (a running standalone
// `/afk:go`), so a pane, a preview and an attach all still make sense for
// it. Broaden this and the dashboard goes dark on exactly the sessions this
// vocabulary was extended to make cleanable.
func (k Kind) WorktreeFault() bool {
	return k == KindWorktreeMissing || k == KindWorktreeNoSession
}

// IdentityRelevant reports whether k is part of a discovered entry's
// IDENTITY: two entries sharing a name but not a kind are two different
// sessions with two different teardowns.
//
// This is the ONE discriminator every by-name resolution must share — the
// dashboard's cleanTargetKind and the CLI's findCleanTarget both resolve
// user-typed names against slices where such an entry can shadow a healthy or
// scratch session of the same name, and each caller re-deriving the test from
// the constants is how the CLI once missed the fix the dashboard got.
//
// WorktreeFault keeps its own, narrower meaning (inert rows, fault badges,
// attach exclusion) unchanged: every worktree fault is identity-relevant, but
// not every identity-relevant kind is a worktree fault.
//
// KindStaleStatus is deliberately NOT identity-relevant: the two scanners
// label different files stale (see internal/worktree's package doc), so it can
// never be part of an entry's identity without making a clean unreachable for
// whichever package disagrees.
func (k Kind) IdentityRelevant() bool {
	return k.WorktreeFault() || k == KindStatusWithoutWorktree
}

// worktreeFaultKind splits the worktree-side fault on the one fact that
// decides its teardown: whether the registered directory is still there.
//
// It is pure and separate from Discover because taking the wrong branch is
// not a cosmetic error. A missing-directory worktree routed as if it existed
// runs `git status` and `git add -A` against a path that does not resolve,
// which lands in the caller's own working directory.
func worktreeFaultKind(dirOnDisk bool) Kind {
	if dirOnDisk {
		return KindWorktreeNoSession
	}
	return KindWorktreeMissing
}

// worktreeFaultSession builds the Session a worktree-side fault is reported
// as. It exists so the two fields a teardown steers by can only ever come
// from the observation that produced the fault.
//
// WorktreePath and Branch are the values git itself has registered, and both
// are non-empty by construction — Discover only reaches here for a worktree
// with a branch, and the porcelain keeps emitting a record's path and branch
// after the directory is deleted (verified against git 2.51.0). A zero
// WorktreePath is not a cosmetic defect here: terminateCleanHost would look
// for host.json relative to the caller's cwd, isDirty would run `git status`
// there, and snapshotCommit would `git add -A && git commit` the user's own
// working directory.
func worktreeFaultSession(obs worktree.Observation) Session {
	kind := worktreeFaultKind(obs.DirOnDisk)
	reason := fmt.Sprintf(
		"worktree %s is checked out on branch %q but has no .afk/%s/status.json",
		obs.Path, obs.Branch, obs.Branch,
	)
	if kind == KindWorktreeMissing {
		reason = fmt.Sprintf(
			"worktree %s is still registered for branch %q but its directory no longer exists",
			obs.Path, obs.Branch,
		)
	}
	return Session{
		Name:         obs.Branch,
		WorktreePath: obs.Path,
		Branch:       obs.Branch,
		Kind:         kind,
		Reason:       reason,
	}
}

// Session is a discovered afk session, in every shape `afk clean` can act
// on: a `.afk/{name}/status.json` whose containing worktree is checked out
// on branch {name}, the same status directory carried by a worktree that is
// NOT (KindStatusWithoutWorktree), a worktree-side fault (see Kind), or a
// scratch session (see the Scratch field). Every discovered entry is one of
// these, so a name the user can see is always a name a clean can resolve.
type Session struct {
	Name         string
	WorktreePath string
	Branch       string
	StatusPath   string
	Status       Status

	// Kind names this session's fault, KindHealthy when it has none. It is
	// independent of Scratch: a scratch session is always KindHealthy, and
	// scratch-ness is settled before any fault is considered.
	Kind Kind

	// Reason explains a non-healthy Kind in the words a user reads, and is
	// empty for a healthy or scratch session. Without it a row would name a
	// session and give no account of its shape: the two worktree-side faults
	// have no status.json and no phase to speak for them, and a
	// KindStatusWithoutWorktree row looks exactly like a healthy one until
	// something says which worktree its status file is actually sitting in.
	Reason string

	// Scratch marks a one-off session created by `afk new --scratch`: a
	// `.afk/{name}/` directory holding a host.json and NO status.json. It
	// has no branch, no worktree of its own and no plan, so Branch,
	// StatusPath and Status are all zero for one, and the name/branch match
	// that identifies a status-backed session cannot apply to it.
	//
	// WorktreePath is still set — to the worktree the directory was found in,
	// which is the repo root for every session `afk new --scratch` creates.
	// It is not something to remove; it is the repoRoot half of the
	// (worktree, name) key that host.ReadMeta and host.SocketPath use to find
	// this session's host.
	Scratch bool
}

// CleanDeps is the injectable seam for afk clean's environment-touching
// operations, mirroring the Deps pattern in create.go.
type CleanDeps struct {
	// Run executes name+args in dir and returns stdout (git commands only).
	Run func(dir, name string, args ...string) (string, error)
	// Now supplies the current time (used to date the archive directory);
	// defaults to time.Now.
	Now func() time.Time
	// HomeDir returns the user's home directory (the archive root lives
	// under it); defaults to os.UserHomeDir.
	HomeDir func() (string, error)
	Stdout  io.Writer
	Stderr  io.Writer

	// Alive reports whether pid is a live process; defaults to host.Alive.
	// Injected (rather than calling host.Alive directly) so table tests can
	// simulate a host that lingers past the socket-Exit/SIGTERM grace
	// periods without touching a real process.
	Alive func(pid int) bool
	// SignalKill sends sig to pid; defaults to syscall.Kill. This is the
	// signal-based fallback used only when the host does not respond to a
	// socket Exit in time.
	SignalKill func(pid int, sig syscall.Signal) error
	// DialExit dials socket and sends a host.Exit frame, bounded by timeout;
	// defaults to dialing a real unix socket. This is always tried first —
	// signals are a fallback for a host that is alive but not answering.
	DialExit func(socket string, timeout time.Duration) error

	// IsSpiceTracked reports whether branch is a git-spice-tracked branch in
	// dir; defaults to spice.IsTracked. Injected (rather than calling
	// spice.IsTracked directly) so table tests can force the spice-tracked
	// branch of CleanOptions.DeleteBranch without a real `gs`/`git-spice`
	// binary or a spice-initialized repo.
	IsSpiceTracked func(dir, branch string) bool
	// DeleteSpiceBranch deletes a git-spice-tracked branch per opts; defaults
	// to spice.DeleteBranch. Injected for the same reason as IsSpiceTracked,
	// and so tests can assert exactly which BranchDeleteOptions
	// CleanSessionWithOptions computed (Force in particular) without
	// depending on a real `gs branch delete` invocation.
	DeleteSpiceBranch func(opts spice.BranchDeleteOptions) (spice.BranchDeleteResult, error)

	// BeforeClean, when set, is called once with the session before
	// CleanSessionWithOptions touches anything: before the host is stopped,
	// because stopping it ends the terminal a caller may want to read (the
	// agent's last screen of a failed session, PLAN T17.13). Nil, the
	// default, calls nothing, which is how every clean behaved before it.
	//
	// What it does, and whether it does anything for this session, is the
	// caller's decision; this package only promises the moment. An error it
	// returns never stops the clean: it becomes one line of Notes, because
	// removing a finished session matters more than keeping a picture of it.
	//
	// It lives here and not on CleanOptions because CleanOptions must stay
	// comparable (internal/ui compares the options a key dispatched with ==),
	// and a func field would make that a compile error.
	BeforeClean func(sess Session) error
}

// DefaultCleanDeps returns CleanDeps wired to the real environment.
func DefaultCleanDeps() CleanDeps {
	return CleanDeps{
		Run:        defaultRun,
		Now:        func() time.Time { return time.Now().UTC() },
		HomeDir:    os.UserHomeDir,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Alive:      host.Alive,
		SignalKill: signalKill,
		DialExit:   dialExit,

		IsSpiceTracked:    spice.IsTracked,
		DeleteSpiceBranch: spice.DeleteBranch,
	}
}

// withDefaults fills in any nil field on d that has a real-environment
// default.
func (d CleanDeps) withDefaults() CleanDeps {
	def := DefaultCleanDeps()
	if d.Run == nil {
		d.Run = def.Run
	}
	if d.Now == nil {
		d.Now = def.Now
	}
	if d.HomeDir == nil {
		d.HomeDir = def.HomeDir
	}
	if d.Stdout == nil {
		d.Stdout = def.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = def.Stderr
	}
	if d.Alive == nil {
		d.Alive = def.Alive
	}
	if d.SignalKill == nil {
		d.SignalKill = def.SignalKill
	}
	if d.DialExit == nil {
		d.DialExit = def.DialExit
	}
	if d.IsSpiceTracked == nil {
		d.IsSpiceTracked = def.IsSpiceTracked
	}
	if d.DeleteSpiceBranch == nil {
		d.DeleteSpiceBranch = def.DeleteSpiceBranch
	}
	return d
}

// signalKill is the real-environment default for CleanDeps.SignalKill.
func signalKill(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// dialExit is the real-environment default for CleanDeps.DialExit: it dials
// socket over a unix connection and sends a single host.Exit control frame,
// which asks a live host to terminate its child and itself in an orderly
// way (see internal/host's Serve loop).
func dialExit(socket string, timeout time.Duration) error {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	return host.WriteFrame(conn, host.Exit, nil)
}

// Discover walks every worktree registered against repo (via `git worktree
// list --porcelain`), finds each `.afk/*/status.json` directly inside it,
// and reconciles the two. Every entry it reports is a Session with a
// teardown, distinguished by Kind:
//
//   - a worktree whose checked-out branch equals a discovered session name is
//     a matched, healthy Session (or KindStaleStatus if its status.json would
//     not parse);
//   - a status directory whose carrier worktree is on some other branch is
//     KindStatusWithoutWorktree, carrying the CARRIER as its WorktreePath and
//     no branch of its own;
//   - a non-primary worktree with no session of its own is a worktree-side
//     fault (see worktreeFaultSession);
//   - a `.afk/{name}/` with host.json and no status.json is a scratch
//     session.
//
// Nothing is reported under two shapes. An entry that was would be printed
// twice and resolved by whichever half a caller searched first, and the two
// halves have different teardowns — which is the coin flip this function's
// single sorted slice exists to foreclose (see sortSessions).
//
// The returned slice is sorted for deterministic output.
func Discover(repo string, d CleanDeps) ([]Session, error) {
	d = d.withDefaults()

	worktrees, err := worktree.List(d.Run, repo)
	if err != nil {
		return nil, err
	}

	var sessions []Session
	for _, wt := range worktrees {
		if wt.Bare {
			continue
		}

		obs, oerr := worktree.Observe(wt, worktree.Deps{})
		if oerr != nil {
			return nil, oerr
		}
		found, ferr := sessionsFromDirs(obs.Dirs)
		if ferr != nil {
			return nil, ferr
		}

		for _, s := range found {
			s.WorktreePath = obs.Path
			if s.Scratch {
				// A scratch session intentionally has no branch, so the
				// name/branch rule below can never match it — applying it
				// would file every scratch session as
				// KindStatusWithoutWorktree and send its teardown looking for
				// an archive it has nothing to put in. Branch is deliberately
				// left empty rather than inheriting the carrier worktree's:
				// CleanSession reports it as a branch it "kept", and a scratch
				// clean keeps no branch because it never had one.
				sessions = append(sessions, s)
				continue
			}
			if obs.Branch != "" && s.Name == obs.Branch {
				// Only a MATCHED session inherits the carrier's branch, and
				// only here. The branch is this session's own — it is the name
				// the user typed — so a clean may report keeping it and
				// --delete-branch may delete it.
				s.Branch = obs.Branch
				sessions = append(sessions, s)
				continue
			}
			// Carrier mismatch. Branch stays EMPTY: obs.Branch belongs to
			// whatever the carrier is checked out on (most often the user's
			// own work), and copying it here is what would offer it to
			// deleteSessionBranch. Kind overrides whatever sessionsFromDirs
			// decided, KindStaleStatus included — routing on the mismatch is
			// what keeps this teardown out of the carrier with git, and the
			// stale fact survives as an empty Status.Phase.
			s.Kind = KindStatusWithoutWorktree
			s.Reason = statusWithoutWorktreeReason(s.Name, obs.Path, obs.Branch)
			sessions = append(sessions, s)
		}

		// All four clauses are load-bearing and none may be dropped.
		// Primary: the repo's own checkout is not afk-created (see the
		// package-level scope decision). Branch: a worktree with no branch has
		// no name for a session directory to equal, so the check has nothing
		// to test. Detached: it also has no branch to report, which matters
		// most for a detached worktree whose directory is gone — there is
		// nothing to name it by. MatchedOwnBranch: without it every healthy
		// non-primary session emits a fault entry of itself alongside its own
		// Session — and it is deliberately status-backed-only, so a scratch
		// `.afk/{branch}/` in this worktree does not suppress a real one
		// (internal/worktree's MatchedOwnBranch doc has the argument).
		if !obs.Primary && obs.Branch != "" && !obs.Detached && !obs.MatchedOwnBranch {
			sessions = append(sessions, worktreeFaultSession(obs))
		}
	}

	sortSessions(sessions)
	return sessions, nil
}

// statusWithoutWorktreeReason is the user-facing account of a carrier
// mismatch, kept pure and separate so its wording can be asserted without a
// repo on disk.
//
// It describes only the mismatch it actually observed. The wording this
// replaced claimed "no worktree is checked out for branch %q", which is false
// whenever a healthy session of the same name exists in its own worktree —
// precisely the collision case where a user needs to be told WHICH of the two
// entries they are looking at.
func statusWithoutWorktreeReason(name, carrier, carrierBranch string) string {
	if carrierBranch == "" {
		return fmt.Sprintf(
			"session %q has its status file under worktree %s, which has no branch checked out, so nothing there is named %q",
			name, carrier, name,
		)
	}
	return fmt.Sprintf(
		"session %q has its status file under worktree %s, which is checked out on branch %q, not %q",
		name, carrier, carrierBranch, name,
	)
}

// sortSessions orders sessions by a key total enough that no two distinct
// entries ever compare equal.
//
// Name alone is not that key. A `.afk/dup/` scratch directory in the main
// checkout and a worktree checked out on branch `dup` carrying its own
// `.afk/dup/status.json` are both legitimate, and Discover reports both under
// the name `dup`. sort.Slice is documented as not stable, so with a name-only
// comparator the order of that pair is whatever the sort happens to leave —
// while every caller resolves a session by taking the FIRST match for a name
// (cmdClean's findCleanTarget, internal/ui's resolveCleanTarget). That makes
// `afk clean dup` a coin flip between two different sessions with two
// different removal paths.
//
// The status-backed entry wins the tie deliberately: its name IS a branch name,
// which is what the user typed it as, and it is the only one of the pair whose
// clean has a worktree to remove. The remaining tie-breaks exist to finish the
// total order, not because any caller depends on their direction.
//
// Note what this order does NOT decide. A name can also be answered by an
// entry whose Kind is identity-relevant — a stranded `.afk/dup/` in the main
// checkout, a worktree fault on branch `dup` — and which of THOSE a bare
// `afk clean dup` resolves to is a tiering decision the callers make (see
// the CLI's findCleanTarget), not a sort order. This comparator only
// guarantees that the answer is the same on every run.
func sortSessions(sessions []Session) {
	sort.Slice(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Scratch != b.Scratch {
			return !a.Scratch
		}
		if a.WorktreePath != b.WorktreePath {
			return a.WorktreePath < b.WorktreePath
		}
		if a.StatusPath != b.StatusPath {
			return a.StatusPath < b.StatusPath
		}
		return a.Kind < b.Kind
	})
}

// sessionsFromDirs applies this package's policy to the `.afk/{name}/`
// directories one worktree observation reported, in observation order.
// WorktreePath and Branch are left zero; Discover fills those from the owning
// worktree.
//
// Scratch-ness is decided FIRST and from HasStatusFile alone — never from a
// fresh stat of the sibling status.json. The two would answer the same
// question about a filesystem holding still, and this one does not hold
// still: the conductor creates status.json and `afk clean` removes it, both
// while discovery runs. A stat is a SECOND observation, so it can disagree
// with the glob that produced the status-backed Session moments earlier and
// emit the same `.afk/{name}/` a second time as a scratch session. Callers
// take the first match by name, so one directory present under two kinds
// means the kind that answers `afk clean {name}` is chosen by sort order,
// and the wrong kind sends a clean down the path that runs `git worktree
// remove`. Deciding from the observation keeps every Session traceable to
// exactly one look at the disk.
//
// The two status outcomes below are also not interchangeable. An UNREADABLE
// status.json aborts the whole scan: the question "what is in this file" went
// unanswered, and "unanswered" must never read as "corrupt", which would
// offer a live session's directory for teardown. A file that was read and
// REJECTED is a KindStaleStatus session with a zero Status — still
// discoverable, because its directory is real and it is exactly what a clean
// needs to reach.
func sessionsFromDirs(dirs []worktree.AfkDir) ([]Session, error) {
	var out []Session
	for _, fd := range dirs {
		if !fd.HasStatusFile {
			out = append(out, Session{Name: fd.Name, Scratch: true})
			continue
		}
		if errors.Is(fd.StatusStrictErr, worktree.ErrStatusUnreadable) {
			return nil, fd.StatusStrictErr
		}
		if fd.StatusStrictErr != nil {
			out = append(out, Session{Name: fd.Name, StatusPath: fd.StatusPath, Kind: KindStaleStatus})
			continue
		}
		out = append(out, Session{
			Name:       fd.Name,
			StatusPath: fd.StatusPath,
			Status:     statusFromState(fd.StatusStrict),
		})
	}
	return out, nil
}

// statusFromState copies internal/state's Status into this package's.
//
// A conversion would be cheaper and is not available: the nine fields and
// their JSON tags are identical, but Phase is state.Phase there and a plain
// string here, and Go requires identical field types for a struct
// conversion. Retyping this package's Phase instead would reach into
// create.go's status-seeding path, which is a larger change than the copy.
// internal/worktree cannot simply return this type — it would have to import
// internal/session, and this package imports it.
func statusFromState(st state.Status) Status {
	return Status{
		SessionID:     st.SessionID,
		PlanName:      st.PlanName,
		Phase:         string(st.Phase),
		Branch:        st.Branch,
		BaseBranch:    st.BaseBranch,
		LoopCount:     st.LoopCount,
		AwaitingInput: st.AwaitingInput,
		TmuxWindow:    st.TmuxWindow,
		UpdatedAt:     st.UpdatedAt,
	}
}

// CleanOptions carries CleanSession's opt-in behavior beyond the default
// lifecycle teardown. The zero value reproduces today's behavior exactly:
// DeleteBranch defaults to false, so CleanSession (CleanSessionWithOptions
// called with CleanOptions{}) never deletes a branch, and every existing
// caller of the unchanged CleanSession signature is unaffected.
//
// Two separate bits, not one, because they answer two different questions:
// whether to attempt a deletion at all (DeleteBranch), and, only once a
// branch is found to be unmerged, whether the caller has already confirmed
// that destroying unmerged work is intended (ConfirmedUnmerged). Collapsing
// them into one bool would force an unmerged branch to be either always
// refused or always deleted with no way to ask first — the `-d`/`-D` and
// `gs branch delete`/`--force` split this mirrors needs both.
type CleanOptions struct {
	// DeleteBranch opts into deleting sess.Branch after the worktree has
	// already been removed. False (the default) matches every behavior
	// CleanSession had before this option existed.
	DeleteBranch bool
	// ConfirmedUnmerged records that the caller has already confirmed
	// deleting a branch Merged reports as NOT merged (e.g. via a second,
	// explicit confirmation prompt upstream of this package). It only has an
	// effect when DeleteBranch is also true, and it never suppresses the
	// merge-state check itself: merge state INFORMS the decision, this bit
	// DECIDES it. A merged branch is deleted regardless of this field.
	ConfirmedUnmerged bool
}

// CleanResult reports the outcome of CleanSession for the caller to print:
// what was kept (branch, archive path) and what was removed (worktree).
type CleanResult struct {
	Name              string
	Branch            string
	WorktreePath      string
	ArchivePath       string
	SnapshotCommitted bool

	// BranchDeleted reports whether CleanOptions.DeleteBranch actually
	// deleted Branch. It is always false when DeleteBranch was not set, and
	// can be false even when it was set (an unconfirmed unmerged branch, a
	// scratch session with no branch, or a delete command that failed — see
	// Notes for which).
	BranchDeleted bool
	// DeletedBranchSHA is Branch's tip commit at the moment it was deleted,
	// empty unless BranchDeleted is true. Deleting a branch only leaves it
	// reflog-recoverable, not visibly present, so the summary line this
	// backs is the recovery handle a user needs to `git branch <name> <sha>`
	// it back.
	DeletedBranchSHA string

	// RetargetedBranches names the upstack branches `gs branch delete`
	// retargeted onto the deleted branch's parent (see
	// spice.BranchDeleteResult.Retargeted), empty unless BranchDeleted is
	// true and the delete went through git-spice. It exists so the summary a
	// caller prints can tell the user to restack those branches deliberately
	// — --restack=none means git-spice moved their parent pointer without
	// rebasing them, and nothing else ever reports that it happened.
	//
	// Comma-joined text rather than a []string for Notes' reason: CleanResult
	// must stay comparable across internal/ui's clean seam.
	RetargetedBranches string

	// Kind echoes the fault the teardown was routed on, so a caller printing
	// the summary can say which route ran without re-deriving it from a
	// Session it may no longer hold.
	Kind Kind

	// ArchiveSkipped records that step 2 was deliberately not attempted, as
	// opposed to attempted and empty. It is a separate bit from an empty
	// ArchivePath because the two are indistinguishable otherwise, and the
	// difference is exactly what the user needs: a skipped archive means the
	// session's notepads were already gone, not that afk chose not to keep
	// them.
	ArchiveSkipped bool

	// Notes carries every step that was reported and skipped rather than run,
	// one per line. This is the bounded best-effort staging create.go's
	// Step.bestEffort models (create.go:485-489), narrowed: a step that can be
	// reported and skipped does not abort the rest of ONE session's teardown.
	// Archiving is never one of them — it is copy-then-verify-then-remove, and
	// a failure there must stay fatal so the source removal below it never
	// runs.
	//
	// It is newline-joined text rather than a []string so CleanResult stays
	// COMPARABLE. It crosses internal/ui's clean seam as a value and is
	// compared there with ==; a slice field makes that a compile error in a
	// package this type has no business constraining.
	Notes string
}

// note records a skipped or degraded step on the result and echoes it to the
// user, mirroring the warning create.go's Plan.Run prints for a failed
// bestEffort step (create.go:982-985).
func (r *CleanResult) note(d CleanDeps, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if r.Notes != "" {
		r.Notes += "\n"
	}
	r.Notes += msg
	fmt.Fprintf(d.Stderr, "afk clean: %s\n", msg)
}

// ErrDirtyForeignWorktree is returned when a KindWorktreeNoSession teardown
// finds uncommitted or untracked content. It is a sentinel so a caller can
// tell a refusal (nothing was touched, the work is intact) from a failure.
var ErrDirtyForeignWorktree = errors.New("worktree has uncommitted or untracked changes and carries no afk session")

// ErrNoWorktreePath is returned when a non-healthy teardown is handed a
// Session with no worktree path. On the worktree-side routes every git command
// takes that path as its working directory, and an empty one means the
// CALLER's working directory (see defaultRun, create.go:187-189) — so this
// refusal is what stands between a malformed Session and `git add -A && git
// commit` in the user's own checkout. cleanStatusWithoutWorktree runs no git
// but returns it for the filesystem analog: with an empty path its removal
// target would be a relative `.afk/{name}`, resolved against wherever the
// process happens to be running. Discover cannot produce one
// (worktreeFaultSession fills it from the porcelain, and Discover fills every
// status-backed entry's from the observation); this guards a Session assembled
// somewhere else, the same way cleanScratchSession guards its own.
var ErrNoWorktreePath = errors.New("session has no worktree path")

// CleanSession applies the claude-squad lifecycle-cleanup pattern to a
// single already-selected session. It is CleanSessionWithOptions called with
// CleanOptions{}, kept as its own entry point (rather than folded away) so
// every caller written before CleanOptions existed — root clean.go and
// internal/ui's defaultCleanRunner — keeps compiling unchanged. See
// CleanSessionWithOptions for what the teardown does.
func CleanSession(repo string, sess Session, d CleanDeps) (CleanResult, error) {
	return CleanSessionWithOptions(repo, sess, d, CleanOptions{})
}

// CleanSessionWithOptions applies the claude-squad lifecycle-cleanup pattern
// to a single already-selected session:
//
//  0. If a host process (`afk _host`) is still running this session, stop
//     it first — `git worktree remove` fails or dangles if a process still
//     holds the worktree as its cwd.
//  1. If the worktree has uncommitted tracked changes, snapshot-commit them
//     with a Conventional-Commits-compliant message (this repo lints commits
//     in CI) so no work is lost.
//  2. Archive `.afk/{name}/` (status, learnings, decisions, issues,
//     verdict — it is gitignored and would otherwise be lost) to
//     ~/.local/share/afk/archive/{repo}-{name}-{date}/, copying then
//     verifying before removing the source.
//  3. Remove the worktree and prune stale administrative data.
//
// The branch is kept unless opts.DeleteBranch is set. That decision is made
// AFTER step 3 (see deleteSessionBranch), never before: a deletion attempted
// while the worktree still existed would have to guess whether removal was
// about to succeed, and every existing teardown step already assumes the
// worktree comes off cleanly or the function has already returned an error.
//
// A scratch session (sess.Scratch) takes none of the steps above except the
// host stop: see cleanScratchSession. It is also exempt from the caller-side
// phase gate that refuses a session outside done/failed — see
// CleanableWithoutForce, which is where that decision lives. It never has a
// branch, so opts.DeleteBranch on one is a reported no-op rather than an
// error: see deleteSessionBranch's empty-Branch case.
//
// A faulted session (sess.Kind) takes one of three narrower routes instead,
// and which one is chosen BEFORE any of the steps above. That order is not
// cosmetic: steps 1 and 2 both run git inside sess.WorktreePath, and for two
// of the three kinds that path is either gone or not this session's to touch.
//
//   - KindWorktreeMissing / KindWorktreeNoSession split on the one fact that
//     separates them, whether the registered directory is still on disk;
//     running step 1 against a path that no longer resolves is how a teardown
//     reaches the caller's own checkout. See cleanMissingWorktree and
//     cleanForeignWorktree. Both carry the branch git itself reports for the
//     worktree (worktreeFaultSession), so opts.DeleteBranch applies to them
//     exactly as it does to a healthy session.
//   - KindStatusWithoutWorktree owns only `.afk/{name}/`; its WorktreePath
//     is the CARRIER worktree, usually the user's own checkout, so its route
//     issues no git command at all and opts.DeleteBranch on it is a reported
//     no-op (its Branch is always empty). See cleanStatusWithoutWorktree.
func CleanSessionWithOptions(repo string, sess Session, d CleanDeps, opts CleanOptions) (CleanResult, error) {
	d = d.withDefaults()
	var (
		res CleanResult
		err error
	)
	// The hook runs first, ahead of every route's own guards, so it sees the
	// session while its host and files are all still there. Its error is
	// held and noted after the route has run: the route builds res afresh,
	// so a note made now would be lost.
	var beforeErr error
	if d.BeforeClean != nil {
		beforeErr = d.BeforeClean(sess)
	}
	switch {
	case sess.Scratch:
		res, err = cleanScratchSession(sess, d)
	case sess.Kind == KindWorktreeMissing:
		res, err = cleanMissingWorktree(repo, sess, d)
	case sess.Kind == KindWorktreeNoSession:
		res, err = cleanForeignWorktree(repo, sess, d)
	case sess.Kind == KindStatusWithoutWorktree:
		res, err = cleanStatusWithoutWorktree(repo, sess, d)
	default:
		res, err = cleanNormalSession(repo, sess, d)
	}
	if beforeErr != nil {
		res.note(d, "before cleaning %s: %v", sess.Name, beforeErr)
	}
	if err == nil && opts.DeleteBranch {
		deleteSessionBranch(&res, repo, sess, d, opts)
	}
	return res, err
}

// cleanNormalSession is CleanSessionWithOptions's route for every session
// that owns its worktree: a healthy session, or one with a
// corrupt-but-present status.json (KindStaleStatus). See
// CleanSessionWithOptions's numbered steps.
//
// "Owns its worktree" is the whole precondition, and Discover is what
// establishes it — a status-backed session only reaches here when its carrier
// is checked out on branch {name}, which makes sess.WorktreePath a worktree
// afk created for this session and nothing else. Every other status-backed
// shape is routed away before this point, because the steps below run `git
// add -A`, `git commit` and `git worktree remove` against that path.
func cleanNormalSession(repo string, sess Session, d CleanDeps) (CleanResult, error) {
	res := CleanResult{Name: sess.Name, Branch: sess.Branch, WorktreePath: sess.WorktreePath, Kind: sess.Kind}

	// Defense in depth, matching the fault routes. Discover always populates
	// both fields, so this is unreachable today — but this is the route that
	// runs `git add -A` and `git commit`, and an empty WorktreePath would run
	// them in the CALLER'S working directory. The guard costs two comparisons;
	// the failure it forecloses is committing the user's own tree.
	if sess.WorktreePath == "" || sess.Name == "" {
		return res, fmt.Errorf("cleaning %s session %q: %w", sess.Kind, sess.Name, ErrNoWorktreePath)
	}

	if err := terminateCleanHost(d, sess); err != nil {
		return res, fmt.Errorf("terminating session host: %w", err)
	}

	dirty, err := isDirty(d, sess.WorktreePath)
	if err != nil {
		return res, fmt.Errorf("checking worktree cleanliness: %w", err)
	}
	if dirty {
		if err := snapshotCommit(d, sess.WorktreePath, sess.Name); err != nil {
			return res, fmt.Errorf("snapshot-committing dirty worktree: %w", err)
		}
		res.SnapshotCommitted = true
	}

	archiveDest, err := archiveSessionDir(d, repo, sess)
	if err != nil {
		return res, fmt.Errorf("archiving session directory: %w", err)
	}
	res.ArchivePath = archiveDest

	// The archive copy is verified at this point (archiveSessionDir already
	// did copy-then-verify), so it is now safe to remove the source. This
	// also keeps `git worktree remove` from ever needing --force: gitignored
	// content does not block removal (verified empirically), but removing it
	// first means the data genuinely lives in exactly one place afterward.
	if err := os.RemoveAll(filepath.Join(sess.WorktreePath, ".afk", sess.Name)); err != nil {
		return res, fmt.Errorf("removing archived .afk/%s: %w", sess.Name, err)
	}

	// Defense in depth: `.afk/` is documented to be gitignored, so the
	// removal above is normally invisible to git. If a target repo violates
	// that convention, deleting a tracked .afk/{name} re-dirties the
	// worktree. The "never remove a dirty worktree without committing
	// first" rule applies to dirtiness we introduce ourselves just as much
	// as to pre-existing dirtiness, so re-check and fold it into a second
	// snapshot commit rather than letting `git worktree remove` fail (or,
	// worse, being tempted to reach for --force).
	dirtyAfterArchive, err := isDirty(d, sess.WorktreePath)
	if err != nil {
		return res, fmt.Errorf("checking worktree cleanliness after archiving: %w", err)
	}
	if dirtyAfterArchive {
		if err := snapshotCommit(d, sess.WorktreePath, sess.Name); err != nil {
			return res, fmt.Errorf("snapshot-committing after archiving: %w", err)
		}
		res.SnapshotCommitted = true
	}

	if _, err := d.Run(repo, "git", "worktree", "remove", sess.WorktreePath); err != nil {
		return res, fmt.Errorf("git worktree remove %s: %w", sess.WorktreePath, err)
	}
	if _, err := d.Run(repo, "git", "worktree", "prune"); err != nil {
		return res, fmt.Errorf("git worktree prune: %w", err)
	}

	return res, nil
}

// cleanMissingWorktree is CleanSession's route for KindWorktreeMissing: git
// still has the worktree registered, but its directory was deleted out from
// under it.
//
// Three of the normal path's steps are not skipped for tidiness, they are
// skipped because their target does not exist:
//
//   - isDirty and snapshotCommit run `git status` and `git add -A` INSIDE the
//     worktree. There is no worktree, and nothing uncommitted can be waiting
//     in a directory that is gone.
//   - archiveSessionDir stats `.afk/{name}` UNDER the worktree, which died
//     with it. The skip is reported rather than silent: a caller told
//     "archived" would be told a falsehood, and the honest message is that
//     the session's notepads were already lost.
//
// terminateCleanHost still runs, and is a harmless no-op here — host.ReadMeta
// simply fails to find host.json under the dead path.
//
// Removal is plain `git worktree remove`, no --force: verified against git
// 2.51.0 that it succeeds on a record the porcelain reports as `prunable`,
// which is exactly this shape. `git worktree prune` is the rescue if it does
// not, so the remove is best-effort and the prune is what must succeed.
//
// The prune is not scoped to this session — git offers no per-worktree form,
// so it clears EVERY prunable record in the repo, including ones nobody named
// in this clean. Accepted rather than worked around: prune deletes only
// $GIT_DIR/worktrees administrative records, never files in any working
// directory, and a prunable record describes a worktree git already considers
// dead — the state this route exists to reap. The widest thing the side
// effect can do is finish another broken entry's removal step early.
func cleanMissingWorktree(repo string, sess Session, d CleanDeps) (CleanResult, error) {
	res := CleanResult{Name: sess.Name, Branch: sess.Branch, WorktreePath: sess.WorktreePath, Kind: sess.Kind}
	if sess.WorktreePath == "" {
		return res, fmt.Errorf("cleaning %s session %q: %w", sess.Kind, sess.Name, ErrNoWorktreePath)
	}

	if err := terminateCleanHost(d, sess); err != nil {
		return res, fmt.Errorf("terminating session host: %w", err)
	}

	res.ArchiveSkipped = true
	res.note(d, "not archiving .afk/%s: the worktree directory %s no longer exists, so neither does the session directory inside it",
		sess.Name, sess.WorktreePath)

	removeErr := error(nil)
	if _, err := d.Run(repo, "git", "worktree", "remove", sess.WorktreePath); err != nil {
		removeErr = err
		res.note(d, "git worktree remove %s failed (%v); falling back to git worktree prune", sess.WorktreePath, err)
	}
	if _, err := d.Run(repo, "git", "worktree", "prune"); err != nil {
		return res, fmt.Errorf("git worktree prune: %w", err)
	}
	// Confirm the entry actually went. `remove` failing and `prune` finding
	// nothing is the signature of the directory reappearing mid-teardown, and
	// without this check the caller prints "Removed: worktree" for a worktree
	// git still registers. Only reached when remove already failed, so the
	// common path pays no extra subprocess.
	if removeErr != nil {
		out, listErr := d.Run(repo, "git", "worktree", "list", "--porcelain")
		if listErr == nil && strings.Contains(out, "worktree "+sess.WorktreePath+"\n") {
			return res, fmt.Errorf("worktree %s is still registered after remove (%v) and prune: "+
				"the directory reappeared mid-teardown; re-run afk clean %s", sess.WorktreePath, removeErr, sess.Name)
		}
	}
	return res, nil
}

// cleanForeignWorktree is CleanSession's route for KindWorktreeNoSession: a
// worktree that is on disk and checked out on a branch, with no session of its
// own. That population deliberately includes worktrees afk never created —
// an unrelated one is indistinguishable from a afk worktree whose status
// file was lost, so this route has to assume the stranger throughout.
//
// Assuming the stranger is what decides both halves:
//
//   - Dirty means REFUSE, never snapshot. isDirty counts untracked files and
//     snapshotCommit runs `git add -A`, so snapshotting here would sweep a
//     stranger's stray files into a commit on a branch afk does not own —
//     and a foreign repo's pre-commit hooks could fail that commit outright,
//     turning a refusal into a confusing error. Refusing leaves the work
//     exactly where its owner left it, which is the point.
//   - Removal is plain `git worktree remove`, never --force. --force is what
//     makes git remove a worktree with uncommitted changes; on a worktree
//     afk does not own that is precisely the outcome this route exists to
//     prevent, and the dirty check above already means it is never needed.
//
// Nothing is archived because there is nothing to archive — a worktree with no
// session has no `.afk/{name}/` — and that is reported for the same reason
// the missing-directory route reports its skip.
func cleanForeignWorktree(repo string, sess Session, d CleanDeps) (CleanResult, error) {
	res := CleanResult{Name: sess.Name, Branch: sess.Branch, WorktreePath: sess.WorktreePath, Kind: sess.Kind}
	if sess.WorktreePath == "" {
		return res, fmt.Errorf("cleaning %s session %q: %w", sess.Kind, sess.Name, ErrNoWorktreePath)
	}

	if err := terminateCleanHost(d, sess); err != nil {
		return res, fmt.Errorf("terminating session host: %w", err)
	}

	dirty, err := isDirty(d, sess.WorktreePath)
	if err != nil {
		return res, fmt.Errorf("checking worktree cleanliness: %w", err)
	}
	if dirty {
		return res, fmt.Errorf("worktree %s (branch %q): %w; commit or discard them first, or remove it yourself",
			sess.WorktreePath, sess.Branch, ErrDirtyForeignWorktree)
	}

	res.ArchiveSkipped = true
	res.note(d, "not archiving .afk/%s: worktree %s carries no session directory", sess.Name, sess.WorktreePath)

	if _, err := d.Run(repo, "git", "worktree", "remove", sess.WorktreePath); err != nil {
		return res, fmt.Errorf("git worktree remove %s: %w", sess.WorktreePath, err)
	}
	if _, err := d.Run(repo, "git", "worktree", "prune"); err != nil {
		return res, fmt.Errorf("git worktree prune: %w", err)
	}
	return res, nil
}

// cleanStatusWithoutWorktree is CleanSession's route for
// KindStatusWithoutWorktree: a status-backed `.afk/{name}/` carried by a
// worktree that is checked out on some other branch — most often a standalone
// `/afk:go` session in the user's primary checkout.
//
// The whole teardown is: stop the host, archive `.afk/{name}/`, remove it.
// Every other step of the normal path is skipped because sess.WorktreePath is
// the CARRIER, not something this session owns:
//
//   - isDirty and snapshotCommit would run `git status`, `git add -A` and
//     `git commit` over the user's own uncommitted work, sweeping in every
//     untracked file (isDirty counts `??` rows, and `git add -A` stages them).
//   - `git worktree remove` would aim at the user's checkout.
//   - `git worktree prune` is skipped too, and not for tidiness: nothing here
//     changes what git has registered, so there is nothing prunable to reap.
//
// It therefore issues no git command at all: d.Run is structurally
// unreachable on this route — the same property cleanScratchSession has, not
// a matter of getting each conditional right. (archiveSessionDir takes `repo`
// only to name the archive directory after it; it shells out for nothing.)
//
// sess.Branch is always empty for this kind — Discover deliberately does not
// copy the carrier's branch onto it — so CleanSessionWithOptions's
// opts.DeleteBranch reaches deleteSessionBranch's empty-Branch case here: a
// reported no-op, never a `git branch -D` against the branch the user is
// standing on.
//
// A `.afk/plans/{name}.md` in the carrier is deliberately left in place. It
// lives in the user's checkout and is not session-owned state (README says
// so), and this route's contract is that it touches exactly one directory.
func cleanStatusWithoutWorktree(repo string, sess Session, d CleanDeps) (CleanResult, error) {
	// Branch is left at its zero value on purpose, and read as "kept nothing"
	// by every caller printing this result: there is no branch to keep.
	res := CleanResult{Name: sess.Name, WorktreePath: sess.WorktreePath, Kind: sess.Kind}

	// The removal below is os.RemoveAll on a path built from sess.Name, and
	// this path's root is the user's own checkout: an empty name would resolve
	// to `.afk` itself — every session directory in the carrier, status-
	// backed ones included — and a traversing one to somewhere outside it.
	// Discover can only produce a filepath.Base'd name; this guards a Session
	// assembled somewhere else, exactly as cleanScratchSession does.
	if sess.Name == "" || sess.Name == "." || sess.Name == ".." || filepath.Base(sess.Name) != sess.Name {
		return res, fmt.Errorf("refusing to clean %s session with unsafe name %q", sess.Kind, sess.Name)
	}
	if sess.WorktreePath == "" {
		return res, fmt.Errorf("cleaning %s session %q: %w", sess.Kind, sess.Name, ErrNoWorktreePath)
	}

	if err := terminateCleanHost(d, sess); err != nil {
		return res, fmt.Errorf("terminating session host: %w", err)
	}

	archiveDest, err := archiveSessionDir(d, repo, sess)
	if err != nil {
		return res, fmt.Errorf("archiving session directory: %w", err)
	}
	res.ArchivePath = archiveDest

	// Only reached once archiveSessionDir has copied AND verified, so the
	// notepads genuinely live somewhere else before anything is destroyed.
	dir := filepath.Join(sess.WorktreePath, ".afk", sess.Name)
	if err := os.RemoveAll(dir); err != nil {
		return res, fmt.Errorf("removing archived .afk/%s: %w", sess.Name, err)
	}
	return res, nil
}

// cleanScratchSession is CleanSession's scratch branch: stop the host with the
// same ladder a normal clean uses, then remove `.afk/{name}/`. That is the
// entire session.
//
// Every other step of the normal path is skipped because a scratch session
// owns nothing for it to act on. There is no dirty worktree to snapshot and no
// worktree to remove — sess.WorktreePath is the repo root the user is working
// in, and `git worktree remove` pointed at it would take their checkout with
// it. Nothing is archived either: the directory holds only host.json and
// host.log, written by the host that just died, with no plan, notepads,
// status or verdict to preserve. (The host's socket is not here at all — see
// host.SocketPath — but terminateCleanHost below still removes it using the
// path recorded inside host.json.)
//
// It therefore issues no git command at all: `git worktree remove` is
// unreachable here as a structural property of the path, not as a matter of
// getting each conditional right. sess.Branch is always empty for a scratch
// session (Discover never sets it for one), so CleanSessionWithOptions's
// opts.DeleteBranch reaches deleteSessionBranch's empty-Branch case here too
// — a reported no-op, not a git command.
func cleanScratchSession(sess Session, d CleanDeps) (CleanResult, error) {
	res := CleanResult{Name: sess.Name}

	// The removal below is os.RemoveAll on a path built from sess.Name, so an
	// empty name would resolve to `.afk` itself and take every session
	// directory in the repo, status-backed ones included. Discover can only
	// produce a filepath.Base'd name; this guards a Session assembled
	// somewhere else.
	if sess.Name == "" || sess.Name == "." || sess.Name == ".." || filepath.Base(sess.Name) != sess.Name {
		return res, fmt.Errorf("refusing to clean scratch session with unsafe name %q", sess.Name)
	}
	if sess.WorktreePath == "" {
		return res, fmt.Errorf("scratch session %q has no worktree path", sess.Name)
	}

	if err := terminateCleanHost(d, sess); err != nil {
		return res, fmt.Errorf("terminating session host: %w", err)
	}

	dir := filepath.Join(sess.WorktreePath, ".afk", sess.Name)
	if err := os.RemoveAll(dir); err != nil {
		return res, fmt.Errorf("removing %s: %w", dir, err)
	}
	return res, nil
}

// deleteSessionBranch implements CleanOptions.DeleteBranch: it runs strictly
// after the worktree teardown that produced res has already succeeded, and
// every failure here is recorded on res via note rather than returned — the
// teardown itself is done by the time this runs, and a branch that could not
// be deleted is not evidence that it failed. repo (not sess.WorktreePath) is
// the working directory for every command below, deliberately: by this point
// the worktree may already be gone (both worktree-side fault routes and the
// normal path remove it before this is called), while repo is the checkout
// that owns the branch ref itself and survives regardless.
//
// Never refuses on merge state alone — see Merged and CleanOptions'
// docs: merge state only decides which message an already-true DeleteBranch
// gets to see; opts.ConfirmedUnmerged is what decides whether an unmerged
// branch is actually removed.
//
// One consequence is worth naming because it contradicts the plain reading of
// "a merged branch deletes immediately": on a session whose worktree was
// DIRTY, the snapshot commit above has already advanced the branch tip past
// the base by the time the merge check runs here. Such a branch was merged
// when the confirmation was shown and is not merged now, so a one-press `d`
// (or a --delete-branch that never prompted) keeps it and says so in the
// notes. That is the safe direction — the snapshot holds work that exists
// nowhere else — but it is a real divergence between what the modal promised
// and what happened, so it is reported rather than silent.
func deleteSessionBranch(res *CleanResult, repo string, sess Session, d CleanDeps, opts CleanOptions) {
	if sess.Branch == "" {
		res.note(d, "not deleting a branch: session %q has no branch", sess.Name)
		return
	}

	if merged := Merged(d.Run, repo, sess.Branch, sess.Status.BaseBranch); !merged && !opts.ConfirmedUnmerged {
		res.note(d, "not deleting branch %q: it has not been merged and deletion was not confirmed", sess.Branch)
		return
	}

	// The tip is captured before the delete runs — there is no "after" to
	// read it from once the branch ref is gone, and a deleted branch is only
	// reflog-recoverable by SHA. Qualified refs/heads/, never the bare name:
	// gitrevisions(7) resolves refs/tags/<name> first, so a tag sharing the
	// branch's name would silently become the recorded "recovery" SHA — a
	// handle naming the wrong work, printed as if it named the deleted branch
	// (see internal/session/branch.go's package doc for the same trap in the
	// ancestry check).
	tipOut, err := d.Run(repo, "git", "rev-parse", "refs/heads/"+sess.Branch)
	if err != nil {
		res.note(d, "not deleting branch %q: could not resolve its tip commit: %v", sess.Branch, err)
		return
	}
	tip := strings.TrimSpace(tipOut)

	if d.IsSpiceTracked(repo, sess.Branch) {
		delRes, err := d.DeleteSpiceBranch(spice.BranchDeleteOptions{Dir: repo, Name: sess.Branch,
			// Force is unconditional once this point is reached (merged per
			// Merged's ancestry check, or unmerged-but-confirmed): without
			// --force, `gs branch delete` refuses an unmerged branch by its
			// own judgment (verified in learnings.md), which this package has
			// already made redundant — Merged's ancestry check against the
			// base it actually consulted is the one this package trusts, and
			// ConfirmedUnmerged is the only other way past it. Passing
			// --force here defers to that decision instead of layering gs's
			// own on top of it.
			Force: true,
		})
		if err != nil {
			res.note(d, "not deleting branch %q via git-spice: %v", sess.Branch, err)
			return
		}
		res.RetargetedBranches = strings.Join(delRes.Retargeted, ", ")
	} else {
		// -D, never -d: -d checks merged-ness against the branch's own
		// upstream or HEAD, not the base Merged actually consulted, so a
		// branch merged into main while HEAD sits elsewhere would be
		// spuriously refused by -d even though Merged has already proven it
		// safe by ancestry. Merged's check being the source of truth is what
		// makes -D here not a loss of safety.
		if _, err := d.Run(repo, "git", "branch", "-D", sess.Branch); err != nil {
			res.note(d, "not deleting branch %q: %v", sess.Branch, err)
			return
		}
	}

	res.BranchDeleted = true
	res.DeletedBranchSHA = tip
}

// CleanableWithoutForce reports whether sess may be cleaned without --force.
//
// `afk clean <name>` refuses a session whose phase is not done or failed,
// because any other phase means work is still in flight. A scratch session has
// no phase to be terminal — no status.json, no plan, no conductor — so that
// gate applied literally would refuse every scratch session forever, leaving
// --force as the only way to remove one. The decision, made here once instead
// of at each caller: a scratch session is always cleanable without --force.
// There is no planned work for it to lose, and its removal destroys a stopped
// host's log directory and nothing else.
//
// This does not weaken any confirmation. The dashboard's `c` still asks before
// cleaning anything, and --force on a scratch session is simply redundant
// rather than refused.
//
// The faulted kinds are the same argument reached by a different route, with
// two exceptions. A worktree-side fault has an empty phase because the status
// file is GONE, not because work is in flight, so --force would be the only
// way to clean the very entries this vocabulary exists to make cleanable. A
// corrupt status.json proves nothing of the sort: the conductor may be
// mid-run behind an unparseable file, so KindStaleStatus passes only while no
// host is running, and a live host still means --force.
//
// KindStatusWithoutWorktree is gated like a healthy session — terminal phase
// or --force — because it HAS a phase: its status.json is intact and its
// conductor may well be mid-run (a standalone `/afk:go` session is the
// common case). Its one wrinkle is the empty phase, which for this kind
// carries two situations at once: a corrupt status.json, since this kind
// overrides KindStaleStatus and nothing else is left to carry that fact, and
// a parsed file whose phase is genuinely empty. Neither is evidence that work
// stopped, so both follow KindStaleStatus's rule and pass only while no host
// is running.
//
// The liveness read is done here rather than by the caller because every
// caller would otherwise have to know to do it, and the one that forgot would
// silently kill live sessions. It is paid for only by the cases whose answer
// depends on it; the decision itself is pure — see cleanableWithoutForce.
func CleanableWithoutForce(sess Session) bool {
	hostRunning := false
	if sess.Kind == KindStaleStatus ||
		(sess.Kind == KindStatusWithoutWorktree && sess.Status.Phase == "") {
		hostRunning = sessionHostRunning(sess)
	}
	return cleanableWithoutForce(sess, hostRunning)
}

// cleanableWithoutForce is CleanableWithoutForce's decision with its one
// environment fact passed in, so the whole matrix is table-testable without a
// live process.
func cleanableWithoutForce(sess Session, hostRunning bool) bool {
	if sess.Scratch {
		return true
	}
	switch sess.Kind {
	case KindWorktreeMissing, KindWorktreeNoSession:
		return true
	case KindStaleStatus:
		return !hostRunning
	case KindStatusWithoutWorktree:
		return sess.Status.Phase == "done" || sess.Status.Phase == "failed" ||
			(sess.Status.Phase == "" && !hostRunning)
	}
	return sess.Status.Phase == "done" || sess.Status.Phase == "failed"
}

// sessionHostRunning reports whether sess still has a live `afk _host`
// process, read from the same `.afk/{name}/host.json` keyed on the worktree
// that `afk attach` and terminateCleanHost use.
//
// The two failure directions are deliberately NOT the same, because this
// feeds the --force gate for a KindStaleStatus session — and for a
// KindStatusWithoutWorktree one whose phase is empty, which is the same
// question about the same file — and the two cases carry opposite evidence:
//
//   - No host.json at all: answer false. There is nothing to be stale about,
//     and a session that never registered a host is not running one.
//   - host.json present but unreadable or corrupt: answer TRUE. A torn
//     host.json is exactly what a crashing or mid-write host leaves behind, so
//     it is evidence that a host existed and no evidence that it stopped.
//     Answering false here is what let a session with BOTH status.json and
//     host.json corrupt clean without --force while its conductor was alive.
//     Unprovable is not the same as disproved; this errs toward --force.
//
// host.Alive is a signal-0 that cannot block. It is also advisory — PIDs get
// recycled — so a true answer can be a false POSITIVE, which again errs toward
// requiring --force.
func sessionHostRunning(sess Session) bool {
	if sess.WorktreePath == "" || sess.Name == "" {
		return false
	}
	meta, err := host.ReadMeta(sess.WorktreePath, sess.Name)
	if err != nil {
		// Distinguish absent from unreadable: only absence is evidence.
		if _, statErr := os.Stat(filepath.Join(sess.WorktreePath, ".afk", sess.Name, "host.json")); statErr != nil {
			return false
		}
		return true
	}
	return host.Alive(meta.PID)
}

// Host-termination timing, package-level vars (not consts) so table tests
// can shrink them and exercise the SIGTERM/SIGKILL fallback paths without
// real multi-second sleeps — same pattern as internal/host's
// resizeNudgeDelay.
var (
	hostDialExitTimeout = 2 * time.Second
	hostExitPollTimeout = 5 * time.Second
	hostSigtermGrace    = 2 * time.Second
	hostSigkillGrace    = 2 * time.Second
	hostPollInterval    = 100 * time.Millisecond
)

// terminateCleanHost is CleanSession's first step: if sess still has a running
// `afk _host` process, stop it before the worktree is touched, and remove
// whatever it leaves behind. `.afk/{name}/host.json` is read via
// host.ReadMeta keyed on sess.WorktreePath (the same worktree-as-repoRoot
// convention `afk attach` uses), so this works regardless of the caller's
// own cwd.
//
//   - No host.json at all: no-op. Nothing recorded a host here, and nothing
//     derived is safe to delete on that basis.
//   - host.json present but unparseable: no PID is recoverable, so NO signal
//     is sent — but the leftovers still go. This splits a case that used to
//     be folded into the one above, where any ReadMeta error returned nil and
//     a single corrupt byte stranded the socket, host.json and host.log on
//     disk permanently, with no command able to reach them. The socket does
//     not come from the file: host.SocketPath derives it from
//     repoRoot+name, so it is recoverable even when nothing in host.json is.
//   - host.json present, dead PID (stale): skip the socket/signal sequence
//     entirely and fall straight to file cleanup.
//   - host.json present, live PID: ask nicely (socket Exit, up to
//     hostExitPollTimeout), then SIGTERM (up to hostSigtermGrace), then
//     SIGKILL (up to hostSigkillGrace); this is a strict escalation, never
//     a bare kill, per the PID-reuse hazard the task calls out.
//
// The socket file, host.json, and host.log are removed unconditionally
// afterward (absence tolerated: the host removes socket+meta itself on a
// clean exit). An error is returned only if a live host could not be
// confirmed dead — proceeding past that point is exactly the failure mode
// this step exists to prevent.
func terminateCleanHost(d CleanDeps, sess Session) error {
	rt, err := RuntimeFor(sess.WorktreePath, sess.Name)
	if err != nil {
		return err
	}
	_, err = rt.Stop(sess.WorktreePath, sess.Name, d)
	return err
}

// HostStop says what StopHost found.
type HostStop int

const (
	// HostGone means nothing was running: no host.json, an unreadable one, or
	// a pid that is no longer alive.
	HostGone HostStop = iota
	// HostStopped means a live host was stopped.
	HostStopped
)

// StopHost ends the `afk _host` process of session name in worktree, with the
// ladder `afk clean` uses (socket Exit, then SIGTERM, then SIGKILL), and
// removes the socket, host.json and host.log it leaves behind. It never
// removes the session folder or any other file.
//
// It exists because approving a plan (T13.2), and later the daemon's end of a
// finished change session (T13.3), must end the agent the way `afk clean`
// does while keeping the session folder: `.afk/master-plan/planning` and
// `.afk/plan/` must survive. terminateCleanHost calls it, so there is one
// ladder.
//
// d.Alive is an advisory signal-0, as for clean: pids get recycled, so a
// true answer can be a false positive. A caller may pass a partly filled
// CleanDeps. An error is returned only when a live host could not be
// confirmed dead; the files are then left in place.
func StopHost(worktree, name string, d CleanDeps) (HostStop, error) {
	d = d.withDefaults()
	worktreeMetaDir := filepath.Join(worktree, ".afk", name)
	metaPath := filepath.Join(worktreeMetaDir, "host.json")
	logPath := filepath.Join(worktreeMetaDir, "host.log")

	meta, err := host.ReadMeta(worktree, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return HostGone, nil
		}
		// Unparseable (or unreadable) host.json. Deriving the socket path
		// rather than reading it back is the whole reason this case can be
		// cleaned up at all; if even that fails, removeIfExists tolerates the
		// empty path and the other two files still go.
		socket, serr := host.SocketPath(worktree, name)
		if serr != nil {
			socket = ""
		}
		removeIfExists(d.Stderr, socket)
		removeIfExists(d.Stderr, metaPath)
		removeIfExists(d.Stderr, logPath)
		return HostGone, nil
	}

	result := HostGone
	if d.Alive(meta.PID) {
		if err := stopLiveHost(d, meta.PID, meta.Socket); err != nil {
			return HostGone, fmt.Errorf("stopping host process (pid %d): %w", meta.PID, err)
		}
		result = HostStopped
	}

	removeIfExists(d.Stderr, meta.Socket)
	removeIfExists(d.Stderr, metaPath)
	removeIfExists(d.Stderr, logPath)
	return result, nil
}

// stopLiveHost escalates from a graceful socket Exit to signals, confirming
// death via d.Alive at each stage before moving to the next. socket may be
// empty (a corrupt host.json) — DialExit is simply skipped in that case.
func stopLiveHost(d CleanDeps, pid int, socket string) error {
	if socket != "" {
		_ = d.DialExit(socket, hostDialExitTimeout)
	}
	if waitForDead(d, pid, hostExitPollTimeout) {
		return nil
	}

	if err := d.SignalKill(pid, syscall.SIGTERM); err != nil && d.Alive(pid) {
		return fmt.Errorf("sending SIGTERM: %w", err)
	}
	if waitForDead(d, pid, hostSigtermGrace) {
		return nil
	}

	if err := d.SignalKill(pid, syscall.SIGKILL); err != nil && d.Alive(pid) {
		return fmt.Errorf("sending SIGKILL: %w", err)
	}
	if waitForDead(d, pid, hostSigkillGrace) {
		return nil
	}

	return fmt.Errorf("process %d is still alive after SIGKILL", pid)
}

// waitForDead polls d.Alive(pid) every hostPollInterval until it reports
// false or timeout elapses (checked via the real wall clock, independent of
// CleanDeps.Now — that seam dates the archive directory, not this poll).
func waitForDead(d CleanDeps, pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !d.Alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(hostPollInterval)
	}
}

// removeIfExists removes path, tolerating its absence, and reporting any
// other removal error to stderr rather than failing the caller — this
// cleanup is best-effort once the host is confirmed gone.
func removeIfExists(stderr io.Writer, path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "afk clean: removing %s: %v\n", path, err)
	}
}

// isDirty reports whether worktree has anything uncommitted at all.
//
// `git status --porcelain` DOES list untracked files, as `??` rows — the
// count here is deliberately not tracked-only. Gitignored content (which is
// how `.afk/` is excluded) is what `--porcelain` omits without `-uall`/
// `--ignored`. The distinction matters to every caller: snapshotCommit pairs
// this with `git add -A`, so an untracked stray file is both counted here and
// swept into the snapshot, which is precisely why cleanForeignWorktree
// REFUSES a dirty stranger instead of snapshotting one.
func isDirty(d CleanDeps, worktree string) (bool, error) {
	out, err := d.Run(worktree, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// snapshotCommit stages and commits everything in worktree with the
// mandated Conventional-Commits-compliant auto-message.
func snapshotCommit(d CleanDeps, worktree, name string) error {
	if _, err := d.Run(worktree, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add -A: %w", err)
	}
	msg := fmt.Sprintf("chore(afk): snapshot session '%s'", name)
	if _, err := d.Run(worktree, "git", "commit", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// archiveSessionDir copies .afk/{name}/ out of the worktree to
// ~/.local/share/afk/archive/{repo}-{name}-{date}/, verifying the copy
// byte-for-byte before the caller removes the source. Never overwrites a
// prior archive of the same session on the same day — it disambiguates with
// a numeric suffix instead.
func archiveSessionDir(d CleanDeps, repo string, sess Session) (string, error) {
	home, err := d.HomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	src := filepath.Join(sess.WorktreePath, ".afk", sess.Name)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("session directory %s: %w", src, err)
	}

	repoName := filepath.Base(repo)
	date := d.Now().Format("2006-01-02")
	archiveRoot := filepath.Join(home, ".local", "share", "afk", "archive")
	dest := uniqueDir(filepath.Join(archiveRoot, fmt.Sprintf("%s-%s-%s", repoName, sess.Name, date)))

	if err := copyTree(src, dest); err != nil {
		return "", fmt.Errorf("copying %s to %s: %w", src, dest, err)
	}
	if err := verifyTreeCopy(src, dest); err != nil {
		return "", fmt.Errorf("verifying archive copy: %w", err)
	}
	return dest, nil
}

// uniqueDir returns dest if it does not yet exist, else the first
// dest-2, dest-3, ... that does not.
func uniqueDir(dest string) string {
	if _, err := os.Stat(dest); err != nil {
		return dest
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", dest, i)
		if _, err := os.Stat(candidate); err != nil {
			return candidate
		}
	}
}

// copyTree recursively copies the contents of src into dst, preserving
// regular-file permissions.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// verifyTreeCopy re-reads every file under src and dst and confirms: every
// source file has a byte-identical archived copy, and the archive contains
// no more/fewer files than the source. This is the "verify" half of
// copy-then-verify — the archive step must never silently lose data.
func verifyTreeCopy(src, dst string) error {
	var srcCount int
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		srcCount++
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		want, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		got, rerr := os.ReadFile(filepath.Join(dst, rel))
		if rerr != nil {
			return fmt.Errorf("missing archived copy of %s: %w", rel, rerr)
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("archived copy of %s does not match source (byte mismatch)", rel)
		}
		return nil
	})
	if err != nil {
		return err
	}

	var dstCount int
	err = filepath.Walk(dst, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			dstCount++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if dstCount != srcCount {
		return fmt.Errorf("archive file count mismatch: source has %d files, archive has %d", srcCount, dstCount)
	}
	return nil
}
