// Package worktree is the one place afk looks at a git worktree and the
// `.afk/` directories inside it. It answers questions of fact and refuses
// questions of judgement.
//
// Two packages scan the same disk for different reasons. internal/discover
// builds dashboard rows; internal/session decides what `afk clean` may
// tear down. They had grown near-identical copies of the porcelain parser
// and the `.afk` glob pair, and the copies were drifting. This package
// holds the single copy.
//
// # Facts only
//
// Nothing here returns a kind enum, and TestObserveReturnsNoVerdict fails
// the build if a field name starts to read like one. That is not tidiness.
// The two consumers agree on identity (both label a status directory whose
// carrying worktree is checked out on a different branch as
// session.KindStatusWithoutWorktree) but deliberately DISAGREE on policy:
// internal/discover renders it as an ordinary dashboard row (pane-capable),
// while internal/session tears it down via a zero-git route that archives
// and removes only `.afk/{name}/`, leaving the carrier untouched. Facts
// are shared; policy stays in the consumer.
//
// # One observation
//
// Observe takes the `.afk` glob pair exactly once and reports the parse
// RESULTS, not just whether files exist. Callers must never re-glob or
// re-stat what Observe already saw. internal/session/clean.go's
// classifyAfkDirs doc explains why at length; the short version is that a
// second look at the same directory can disagree with the first while a
// conductor or a `afk clean` is mid-rename, and the same `.afk/{name}/`
// then gets emitted under two different kinds. Every caller resolves a name
// by taking the first match, so one directory under two kinds means sort
// order picks the teardown route.
//
// Existence is therefore decided by the glob results alone —
// HasStatusFile and HasHostFile come from the two match lists, never from a
// follow-up os.Stat. What the glob pair does NOT decide is content, and the
// two status parse results below are two reads of one file. That is forced
// by the contract (statusSchema must be state.ReadStatus, which does its own
// read and its own retry) and it is harmless, because both results attach to
// a directory whose kind the glob already fixed. The race worth defending
// against is a re-derived EXISTENCE, not a re-read of bytes.
//
// # Two status parsers, deliberately
//
// StatusStrict and StatusSchema are separate results and must stay separate.
// internal/session decodes status.json with DisallowUnknownFields and
// nothing else; internal/state's ReadStatus adds a trailing-data check and
// status-schema.md's three invariants (plan_name matches the directory,
// phase is one of six, session_id non-empty outside planning). A file can
// therefore be a live session to one and stale to the other — trailing data,
// a plan_name mismatch, and an unknown phase all do it. internal/ui's
// dashboardPhase exists because of that disagreement and its freshPhase gate
// depends on it, so collapsing the two results here would silently move the
// clean-refusal gate.
//
// # Never blocks
//
// W2.T1 puts Observe on internal/discover's 2s refresh ticker, where
// anything that stalls freezes the whole dashboard. Everything here is a
// plain file read, a glob, or a stat. There is no dial, no IPC, no network,
// no write, no lock, no sleep, and no subprocess: `git worktree list
// --porcelain` runs through the caller's injected RunFunc, which keeps the
// one unavoidable subprocess on the caller's side of the seam.
// TestObserveNeverBlocks enforces that as an import allowlist rather than
// leaving it to review, mirroring internal/discover's TestDiscoverNeverBlocks.
package worktree

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/state"
)

// ErrStatusAbsent reports that the glob pair saw no status.json in this
// `.afk/{name}/` directory. It is a distinct outcome from a decode
// failure: this one is the normal shape of a `afk new --scratch` one-off,
// while a decode failure is a session whose file is corrupt. Both arrive as
// a non-nil StatusStrictErr, so a caller that only nil-checks would conflate
// them.
var ErrStatusAbsent = errors.New("no status.json in this .afk directory")

// ErrStatusUnreadable reports that status.json matched the glob but could
// not be read (permissions, a mid-flight removal, an I/O error) as opposed
// to being read and rejected.
//
// The distinction is load-bearing for W2.T1: internal/session's discovery
// aborts the entire scan on a read failure and keeps going with a
// zero-Status Session on a decode failure. Preserving that split requires
// telling them apart, and errors.Is on this sentinel is how. The underlying
// cause is wrapped too, so os.ErrPermission and friends still match.
var ErrStatusUnreadable = errors.New("status.json unreadable")

// ErrHostAbsent reports that the glob pair saw no host.json in this
// directory. HasHostFile says the same thing; this exists so that
// HostMetaErr is never nil alongside a zero-value HostMeta, which would read
// as "the host has PID 0" to a caller that skipped the boolean.
var ErrHostAbsent = errors.New("no host.json in this .afk directory")

// RunFunc executes name+args in dir and returns stdout. It matches
// internal/session's CleanDeps.Run and internal/discover's Deps.Run exactly,
// so either package can pass its existing seam straight through.
type RunFunc func(dir, name string, args ...string) (string, error)

// Worktree is one record from `git worktree list --porcelain`.
type Worktree struct {
	// Path is the registered worktree path. It is what git recorded, which
	// is not the same as what exists: a worktree whose directory was deleted
	// keeps its record and its path. Observe's DirOnDisk is the fact that
	// tells the two apart.
	Path string
	// Branch is the short branch name, empty when detached or bare.
	Branch string
	// Detached marks a worktree checked out at a commit rather than a
	// branch. Both scanners exempt these from every branch-named check,
	// because there is no branch to name them by.
	Detached bool
	// Bare marks a bare repository record, which has no working tree to
	// scan.
	Bare bool
	// Primary marks the main checkout. `git worktree list` documents the
	// main worktree as the first record, and List derives this from that
	// position alone — including when the main worktree is bare, matching
	// what both scanners compute today.
	Primary bool
}

// AfkDir is one `.afk/{name}/` directory as a single glob pair saw it,
// with every parse result that directory's files produced.
//
// It carries no notion of what the combination MEANS. "Scratch", for
// instance, is HasHostFile && !HasStatusFile to internal/session — but the
// absence of status.json is also how a session whose file was just removed
// looks, and the two consumers do not have to agree on which one it is.
type AfkDir struct {
	// Name is the directory name, which status-schema.md §1 makes the
	// session identity.
	Name string
	// Path is the `.afk/{Name}` directory itself.
	Path string
	// StatusPath is the status.json the glob matched, empty when it matched
	// nothing.
	StatusPath string
	// HostPath is the host.json the glob matched, empty when it matched
	// nothing.
	HostPath string
	// HasStatusFile and HasHostFile come from the glob results, never from a
	// follow-up stat — see the package doc's "One observation".
	HasStatusFile bool
	HasHostFile   bool

	// StatusStrict is the DisallowUnknownFields decode internal/session
	// performs, with no schema validation on top: no trailing-data check and
	// none of status-schema.md's three invariants. Zero value when
	// StatusStrictErr is non-nil.
	//
	// It is state.Status rather than internal/session's identically-shaped
	// Status because this package cannot import internal/session (W2.T1
	// makes the dependency run the other way). The two structs carry the
	// same nine fields with the same JSON tags and differ only in Phase's
	// named type, so the decode is byte-for-byte the same decode.
	StatusStrict state.Status
	// StatusStrictErr is ErrStatusAbsent when there was no file,
	// ErrStatusUnreadable (wrapping the cause) when the read failed, and the
	// json decode error otherwise.
	StatusStrictErr error

	// StatusSchema is state.ReadStatus's result: the schema-validating
	// parser, whose ReadResult carries its own staleness channel instead of
	// an error. When there is no status.json this is synthesized as
	// stale/missing rather than left at its zero value, which would read as
	// a valid, empty status.
	StatusSchema state.ReadResult

	// HostMeta is host.json parsed by internal/host.ReadMeta — the same
	// reader the host itself writes through, so the two cannot drift. Zero
	// value when HostMetaErr is non-nil.
	HostMeta host.Meta
	// HostMetaErr is ErrHostAbsent when there was no file, and ReadMeta's
	// own read/parse error otherwise. A corrupt host.json yields a non-nil
	// error with HasHostFile still true: no PID is recoverable, but the file
	// is there and still has to be cleaned up.
	HostMetaErr error
}

// Observation is everything one pass over a single worktree saw: the
// porcelain record's facts, whether the directory is actually there, and
// every `.afk/{name}/` inside it.
type Observation struct {
	// Path, Branch, Bare, Primary and Detached are carried through from the
	// Worktree record unchanged, so a consumer never needs both values.
	Path     string
	Branch   string
	Bare     bool
	Primary  bool
	Detached bool

	// DirOnDisk reports whether Path is currently a directory. It is false
	// for a worktree registered with git whose directory was deleted out
	// from under it, which git still lists (with a `prunable` reason).
	//
	// This is the fact to branch on BEFORE running any git command against
	// Path. A teardown that skips it runs `git status` and `git add -A`
	// inside a directory that does not exist, which resolves to the caller's
	// current working directory.
	DirOnDisk bool

	// MatchedOwnBranch reports whether one of Dirs is STATUS-BACKED and
	// named exactly this worktree's branch — that is, whether this worktree
	// carries a session of its own.
	//
	// The definition is narrower than "a directory by that name exists", in
	// both directions, and both narrowings matter:
	//
	//   - A corrupt status.json still counts. The directory is status-backed
	//     whether or not the JSON parses, and a session with a broken file
	//     plainly still exists.
	//   - A scratch directory of the same name does NOT count. A
	//     `.afk/{branch}/` holding only host.json is a `afk new
	//     --scratch` one-off that says nothing about whether this worktree
	//     has a session (internal/session/clean.go:227-230). Counting it
	//     would silently suppress a real worktree-without-status orphan.
	//
	// It is false whenever Branch is empty, because a detached or bare
	// worktree has no branch for a directory name to equal.
	MatchedOwnBranch bool

	// Dirs holds the status-glob matches first, in glob order, then the
	// host-glob matches that the status glob did not already account for.
	// Both consumers sort their own output with a total comparator, so
	// nothing depends on this order; it is fixed so that repointing them at
	// this package is provably order-neutral.
	Dirs []AfkDir
}

// Deps is the injectable seam for every environment-touching call Observe
// makes. The zero value is the real environment: withDefaults fills each nil
// field, so a caller that wants production behavior passes Deps{}.
//
// ReadFile and ReadStatus both read status.json and are deliberately not one
// field — ReadStatus is state.ReadStatus, which owns its own read and retry,
// and the contract requires that exact reader. See the package doc.
type Deps struct {
	// Glob enumerates paths; defaults to filepath.Glob. Injecting it is how
	// a test proves the pair is taken exactly once.
	Glob func(pattern string) ([]string, error)
	// Stat reports on the worktree directory; defaults to os.Stat.
	Stat func(path string) (os.FileInfo, error)
	// ReadFile supplies the bytes for the strict decode; defaults to
	// os.ReadFile. This mirrors the readStatus seam classifyAfkDirs
	// already injects for the same reason: it lets a table test reproduce a
	// mid-write interleaving without racing a real writer.
	ReadFile func(path string) ([]byte, error)
	// ReadStatus is the schema-validating parser; defaults to
	// state.ReadStatus.
	ReadStatus func(path string) state.ReadResult
	// ReadMeta parses host.json; defaults to host.ReadMeta.
	ReadMeta func(worktreePath, name string) (host.Meta, error)
}

// DefaultDeps returns Deps wired to the real filesystem.
func DefaultDeps() Deps {
	return Deps{
		Glob:       filepath.Glob,
		Stat:       os.Stat,
		ReadFile:   os.ReadFile,
		ReadStatus: state.ReadStatus,
		ReadMeta:   host.ReadMeta,
	}
}

func (d Deps) withDefaults() Deps {
	def := DefaultDeps()
	if d.Glob == nil {
		d.Glob = def.Glob
	}
	if d.Stat == nil {
		d.Stat = def.Stat
	}
	if d.ReadFile == nil {
		d.ReadFile = def.ReadFile
	}
	if d.ReadStatus == nil {
		d.ReadStatus = def.ReadStatus
	}
	if d.ReadMeta == nil {
		d.ReadMeta = def.ReadMeta
	}
	return d
}

// List runs `git worktree list --porcelain` in repo through run and parses
// the result.
//
// It runs in repo rather than in each worktree because worktree registration
// lives in the repo's shared git metadata: the complete list comes back
// regardless of which worktree repo happens to be.
//
// A git failure is returned, never smoothed into an empty list. An empty
// list is indistinguishable from "this repo has no worktrees", which would
// make every session in it look orphaned at once.
func List(run RunFunc, repo string) ([]Worktree, error) {
	out, err := run(repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list --porcelain: %w", err)
	}
	return parsePorcelain(out), nil
}

// parsePorcelain parses the stable porcelain format documented by `git
// worktree list --porcelain`: records separated by blank lines, each opening
// with a `worktree <path>` line, and the main worktree first.
//
// Unrecognized lines are ignored, which is what keeps a `prunable <reason>`
// line — git's marker for a worktree whose directory or gitdir file has gone
// missing, verified against git 2.51.0 — from disturbing the Path and Branch
// on the same record. Those two fields are exactly what a teardown needs for
// such a record, so dropping them would be worse than not parsing it at all.
//
// `prunable` is deliberately NOT surfaced as a field. It is git's opinion,
// covering several causes beyond a missing directory, and Observe's
// DirOnDisk answers the narrower question consumers actually branch on.
// Offering both would invite a caller to branch on the wrong one.
func parsePorcelain(out string) []Worktree {
	var worktrees []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			cur.Primary = len(worktrees) == 0
			worktrees = append(worktrees, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "bare":
			cur.Bare = true
		case line == "detached":
			cur.Detached = true
		}
	}
	flush()
	return worktrees
}

// Observe takes one look at wt: whether its directory is there, and every
// `.afk/{name}/` inside it, parsed by both status parsers and by
// internal/host's host.json reader.
//
// It observes exactly the worktree it is handed and skips nothing. Whether a
// bare worktree is worth observing, whether the primary one is exempt from
// some check, and what any of these facts imply are all the caller's to
// decide — see the package doc.
//
// The only error it returns is a glob failure, which means the worktree path
// itself is not a valid glob pattern (an unterminated `[`). Every other
// failure is reported as a per-directory result, because a session whose
// status.json is corrupt is still a session that exists and still has to be
// reachable.
func Observe(wt Worktree, d Deps) (Observation, error) {
	d = d.withDefaults()

	obs := Observation{
		Path:     wt.Path,
		Branch:   wt.Branch,
		Bare:     wt.Bare,
		Primary:  wt.Primary,
		Detached: wt.Detached,
	}
	if info, err := d.Stat(wt.Path); err == nil && info.IsDir() {
		obs.DirOnDisk = true
	}

	statusMatches, err := d.Glob(filepath.Join(wt.Path, ".afk", "*", "status.json"))
	if err != nil {
		return Observation{}, fmt.Errorf("scanning %s for .afk sessions: %w", wt.Path, err)
	}
	hostMatches, err := d.Glob(filepath.Join(wt.Path, ".afk", "*", "host.json"))
	if err != nil {
		return Observation{}, fmt.Errorf("scanning %s for .afk host files: %w", wt.Path, err)
	}

	hostPathByName := make(map[string]string, len(hostMatches))
	for _, m := range hostMatches {
		hostPathByName[filepath.Base(filepath.Dir(m))] = m
	}

	statusBacked := make(map[string]bool, len(statusMatches))
	for _, m := range statusMatches {
		name := filepath.Base(filepath.Dir(m))
		statusBacked[name] = true

		fd := AfkDir{
			Name:          name,
			Path:          filepath.Dir(m),
			StatusPath:    m,
			HasStatusFile: true,
		}
		fd.StatusStrict, fd.StatusStrictErr = decodeStrict(d.ReadFile, m)
		fd.StatusSchema = d.ReadStatus(m)
		applyHost(&fd, d, wt.Path, hostPathByName[name])
		obs.Dirs = append(obs.Dirs, fd)
	}

	for _, m := range hostMatches {
		name := filepath.Base(filepath.Dir(m))
		if statusBacked[name] {
			continue
		}
		fd := AfkDir{
			Name:            name,
			Path:            filepath.Dir(m),
			StatusStrictErr: fmt.Errorf("%s: %w", filepath.Dir(m), ErrStatusAbsent),
			StatusSchema:    absentStatusResult(filepath.Dir(m)),
		}
		applyHost(&fd, d, wt.Path, m)
		obs.Dirs = append(obs.Dirs, fd)
	}

	obs.MatchedOwnBranch = matchedOwnBranch(wt.Branch, obs.Dirs)
	return obs, nil
}

// matchedOwnBranch is the pure half of Observation.MatchedOwnBranch, split
// out so the rule (and only the rule) is what a table test exercises. See
// that field's doc for why status-backed is the test and why a scratch
// directory of the same name deliberately fails it.
func matchedOwnBranch(branch string, dirs []AfkDir) bool {
	if branch == "" {
		return false
	}
	for _, d := range dirs {
		if d.HasStatusFile && d.Name == branch {
			return true
		}
	}
	return false
}

// applyHost fills fd's host.json half. hostPath is the glob match ("" when
// the glob matched nothing), and it is the ONLY thing consulted to decide
// whether the file is there.
//
// ReadMeta is skipped entirely when the glob matched nothing. That is not
// just a saved syscall on a 2s ticker: an unconditional read would be a
// second observation answering a question the glob already answered, and it
// could answer it differently.
func applyHost(fd *AfkDir, d Deps, worktreePath, hostPath string) {
	if hostPath == "" {
		fd.HostMetaErr = fmt.Errorf("%s: %w", fd.Path, ErrHostAbsent)
		return
	}
	fd.HasHostFile = true
	fd.HostPath = hostPath
	fd.HostMeta, fd.HostMetaErr = d.ReadMeta(worktreePath, fd.Name)
	if fd.HostMetaErr != nil {
		fd.HostMeta = host.Meta{}
	}
}

// decodeStrict reproduces internal/session's status decode exactly:
// DisallowUnknownFields (status-schema.md §1: "Parsers MUST reject unknown
// keys as corruption rather than ignore them silently") and nothing else.
//
// The omissions are the point, not an oversight. There is no dec.More()
// trailing-data check and no schema invariant here, because internal/state's
// ReadStatus applies both and the disagreement between the two parsers is
// what StatusStrict and StatusSchema exist to expose separately.
func decodeStrict(readFile func(string) ([]byte, error), path string) (state.Status, error) {
	data, err := readFile(path)
	if err != nil {
		return state.Status{}, fmt.Errorf("%w: reading %s: %w", ErrStatusUnreadable, path, err)
	}
	var st state.Status
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&st); derr != nil {
		return state.Status{}, fmt.Errorf("decoding %s: %w", path, derr)
	}
	return st, nil
}

// absentStatusResult synthesizes the ReadResult for a directory the status
// glob did not match.
//
// state.ReadStatus is not called for one: it would read a file the glob just
// reported absent, sleep out its retry, and read it again, once per scratch
// directory per tick. But its zero value cannot be used either — a
// ReadResult with Stale false reads as a valid, empty status to a caller
// that forgot to check HasStatusFile, which is the strictly worse failure.
func absentStatusResult(dir string) state.ReadResult {
	return state.ReadResult{
		Stale:  true,
		Reason: state.StaleMissing,
		Detail: fmt.Sprintf("no status.json in %s", dir),
	}
}
