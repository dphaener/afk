// Package spice provides a thin, read-mostly wrapper around git-spice (gs)
// for detecting whether a repo is operating in git-spice mode, creating
// tracked branches, and reading stack state via the JSON log stream.
//
// This package intentionally does NOT implement PR submission. That stays
// with the dev-workflow /pr command. Specifically, this package:
//
//   - NEVER constructs or invokes `gs commit fixup` — v0.24.2 has a known
//     data-loss bug in that command path (see plugins/dev-workflow/skills/
//     git-spice/SKILL.md, "## Pitfalls", in the claude-marche checkout).
//   - NEVER parses `gs branch submit` stdout/stderr. Its logger-tagged
//     output (INF/WRN/FTL prefixed lines) is not a stable contract.
//   - NEVER invokes submit/publish operations at all (`gs branch submit`,
//     `gs stack submit`, or anything that pushes/opens a PR). This package
//     only reads stack state (LogShort) and creates local tracked branches
//     (CreateBranch).
package spice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"syscall"
	"time"
)

// binaryNames lists the git-spice executable names to probe, canonical name
// first. `git-spice` became the canonical binary name in v0.25.0; `gs` is
// the historical short alias still provided by most package managers as a
// fallback and still common on developer machines. Never hardcode either
// name alone — probe both.
var binaryNames = []string{"git-spice", "gs"}

// binaryPath resolves the git-spice binary using the supplied lookup
// function (normally exec.LookPath). It is factored out so tests can inject
// a fake lookup and exercise the binary-present/absent axis of the
// detection matrix without depending on the real $PATH.
func binaryPath(lookPath func(string) (string, error)) (string, bool) {
	for _, name := range binaryNames {
		if p, err := lookPath(name); err == nil {
			return p, true
		}
	}
	return "", false
}

// BinaryPath resolves the git-spice binary on $PATH, preferring the
// canonical `git-spice` name and falling back to the historical `gs` alias.
// The second return value is false if neither resolves.
func BinaryPath() (string, bool) {
	return binaryPath(exec.LookPath)
}

// RepoInitialized reports whether the git repo at dir has been initialized
// for git-spice.
//
// Detection deliberately does NOT use `git config --get spice.trunk`. That
// config key is never populated by git-spice; trunk/remote state lives in
// the repo's own ref storage at refs/spice/data (a git commit whose tree
// contains a `repo` blob shaped {"trunk":"...","remote":"..."}), not in
// .git/config. Reading `git config --get spice.trunk` returns empty even on
// a correctly-initialized, actively-used git-spice repo and would silently
// disable trunk-guard / detection logic.
//
// This was verified empirically against a live, actively-used git-spice
// repo (claude-marche, git-spice 0.28.0): `git config --get spice.trunk`
// exits 1 with empty output, while `git rev-parse --verify refs/spice/data`
// exits 0 and `gs ls` shows a real tracked three-branch stack. See
// .afk/dev-workflow-git-spice-integration/{issues,decisions}.md and
// .afk/afk-go-orchestration/issues.md (claude-marche checkout) for the
// paper trail; the corrected dev-workflow git-spice reference skill (branch
// git-spice-dev-workflow there) documents the same refs/spice/data
// detection mechanism.
func RepoInitialized(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/spice/data")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// Detector allows injecting the binary-resolution and repo-initialization
// checks independently, so tests can exercise the full detection matrix
// (binary present/absent x repo-initialized true/false) without depending
// on the environment's real $PATH or a real repo for every case. Detect
// uses the zero-value Detector, which wires up the real implementations.
type Detector struct {
	// LookPath resolves a binary name to a path, or returns an error if not
	// found. Defaults to exec.LookPath.
	LookPath func(name string) (string, error)
	// RepoInit reports whether dir is git-spice-initialized. Defaults to
	// RepoInitialized.
	RepoInit func(dir string) bool
}

// Detect reports whether dir is operating in git-spice mode: a resolvable
// git-spice/gs binary AND a git-spice-initialized repo. Both conditions are
// required — a bare binary-presence check is not sufficient, and a
// git-spice-initialized repo is useless without a binary to drive it.
func (d Detector) Detect(dir string) bool {
	lookPath := d.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	repoInit := d.RepoInit
	if repoInit == nil {
		repoInit = RepoInitialized
	}

	_, present := binaryPath(lookPath)
	return present && repoInit(dir)
}

// Detect reports whether dir is operating in git-spice mode, using the real
// binary resolution and repo-initialization checks. See Detector.Detect.
func Detect(dir string) bool {
	return Detector{}.Detect(dir)
}

// BranchRef is a named reference to another branch in the stack (used for
// the "down" parent and "ups" children fields of the log stream).
type BranchRef struct {
	Name string `json:"name"`
}

// Change describes the pull/change request associated with a branch, if any.
type Change struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Push describes how the local branch compares to its remote tracking ref.
type Push struct {
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
}

// Branch is one entry of the `gs log short --json` stream: a single tracked
// branch and its position in the stack.
type Branch struct {
	Name    string      `json:"name"`
	Current bool        `json:"current,omitempty"`
	Down    *BranchRef  `json:"down,omitempty"`
	Ups     []BranchRef `json:"ups,omitempty"`
	Change  *Change     `json:"change,omitempty"`
	Push    *Push       `json:"push,omitempty"`
}

// ParseLogShort parses the output of `gs log short --json`: a stream of
// JSON objects, one per tracked branch, in an unspecified order (per the
// git-spice docs: "Write to stdout as a stream of JSON objects in an
// unspecified order"). It decodes the stream with json.Decoder (which
// handles both newline-delimited and directly-concatenated objects) and
// sorts the result by branch name so callers get a deterministic stack
// regardless of the order git-spice emitted.
func ParseLogShort(r io.Reader) ([]Branch, error) {
	dec := json.NewDecoder(r)
	var branches []Branch
	for {
		var b Branch
		err := dec.Decode(&b)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("spice: parsing gs log short --json stream: %w", err)
		}
		branches = append(branches, b)
	}

	sort.Slice(branches, func(i, j int) bool {
		return branches[i].Name < branches[j].Name
	})

	return branches, nil
}

// LogShortTimeout bounds a single `gs log short --json` invocation. It is a
// backstop against a call that never returns, NOT a throttle — repeat callers
// must still serialize their own refreshes. Ten seconds sits far above the
// honest cold cost of a large stack (~2s at 49 branches); a timeout tight
// enough to throttle would turn a slow repo into a permanently errored panel.
const LogShortTimeout = 10 * time.Second

// BuildLogShortArgs constructs the argument list for `gs log short --json`.
//
// `--no-prompt` is as non-negotiable here as in BuildBranchDeleteArgs, and for
// a subtler reason: a caller can redirect stdin to /dev/null and still not be
// non-interactive. git-spice's prompts reach for the controlling terminal, and
// a child inherits its parent's, so /dev/tty opens successfully and the prompt
// waits on a terminal nobody is watching.
func BuildLogShortArgs() []string {
	return []string{"log", "short", "--json", "--no-prompt"}
}

// LogShort runs `gs log short --json` in dir and returns the parsed,
// sorted stack. It requires a resolvable git-spice binary; callers should
// gate on Detect(dir) first.
//
// It is bounded by LogShortTimeout and killed by process GROUP, not by pid:
// git-spice fans one `log short` out into a dozen or more concurrent `git`
// children, so signalling the parent alone abandons every one of them still
// running.
func LogShort(dir string) ([]Branch, error) {
	bin, ok := BinaryPath()
	if !ok {
		return nil, fmt.Errorf("spice: git-spice binary not found on $PATH")
	}
	return logShort(bin, dir, LogShortTimeout)
}

// logShort takes the binary and timeout as parameters so the expiry and
// process-group teardown paths are testable with a stub binary, and without
// the suite waiting out LogShortTimeout.
func logShort(bin, dir string, timeout time.Duration) ([]Branch, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, BuildLogShortArgs()...)
	cmd.Dir = dir
	// Setpgid puts the child in its own process group so Cancel can signal
	// the whole tree with a negative pid. Without it the negative-pid kill
	// would target OUR group and take the dashboard down with the refresh.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// The git grandchildren inherit the stdout pipe, so Wait can still block
	// on EOF after the group is signalled. WaitDelay caps that wait instead
	// of trusting every descendant to have died already.
	cmd.WaitDelay = time.Second

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("spice: %s log short --json: timed out after %s", bin, timeout)
		}
		return nil, fmt.Errorf("spice: %s log short --json: %w", bin, err)
	}

	return ParseLogShort(bytes.NewReader(out))
}

// BranchCreateOptions configures a `gs branch create` invocation.
type BranchCreateOptions struct {
	// Dir is the working directory the command runs in (the git repo).
	Dir string
	// Name is the new branch's name. If empty, git-spice generates one from
	// the commit message (only meaningful when Name is left blank AND a
	// commit is being made in the same invocation; this package does not
	// stage or commit anything, so callers should normally supply Name).
	Name string
	// Base is the branch the new branch is created on top of, passed as
	// `--target`. If empty, git-spice uses the current branch as the base.
	Base string
}

// BuildBranchCreateArgs constructs the argument list for `gs branch create`
// from opts, without invoking anything. `gs branch create` always tracks
// the resulting branch as part of the stack — that bookkeeping is the
// entire reason to use it instead of `git branch`/`git checkout -b`.
func BuildBranchCreateArgs(opts BranchCreateOptions) []string {
	args := []string{"branch", "create"}
	if opts.Name != "" {
		args = append(args, opts.Name)
	}
	if opts.Base != "" {
		args = append(args, "--target", opts.Base)
	}
	// --no-commit: this package never stages or commits changes on behalf
	// of the caller. Without it, `gs branch create` would create an empty
	// commit when no changes are staged, which is not this package's
	// concern to decide.
	args = append(args, "--no-commit")
	return args
}

// BuildBranchCreateCmd constructs (but does not run) the *exec.Cmd for
// creating a tracked branch, using the resolved binary path bin.
func BuildBranchCreateCmd(bin string, opts BranchCreateOptions) *exec.Cmd {
	cmd := exec.Command(bin, BuildBranchCreateArgs(opts)...)
	cmd.Dir = opts.Dir
	return cmd
}

// CreateBranch creates a new tracked branch per opts, using the resolved
// git-spice binary. It requires a resolvable git-spice binary; callers
// should gate on Detect(opts.Dir) first.
func CreateBranch(opts BranchCreateOptions) error {
	bin, ok := BinaryPath()
	if !ok {
		return fmt.Errorf("spice: git-spice binary not found on $PATH")
	}

	cmd := BuildBranchCreateCmd(bin, opts)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("spice: %s branch create: %w: %s", bin, err, string(out))
	}
	return nil
}

// TrackedBranch reports whether branches (as returned by LogShort) contains
// name, returning the matched entry when it does. It is the pure half of
// the create/delete split this package already uses: a plain slice lookup,
// so callers deciding spice-vs-plain-git can be table-tested without a
// resolvable git-spice binary or a real invocation of `gs log short`.
func TrackedBranch(branches []Branch, name string) (Branch, bool) {
	for _, b := range branches {
		if b.Name == name {
			return b, true
		}
	}
	return Branch{}, false
}

// IsTracked reports whether name is a git-spice-tracked branch in dir. Every
// failure mode — binary absent, repo not spice-initialized, or `gs log
// short --json` erroring — degrades to false rather than propagating an
// error, per this package's contract that git-spice absence must never be
// treated as a fault: callers fall back to plain git without a special case.
func IsTracked(dir, name string) bool {
	if !Detect(dir) {
		return false
	}
	branches, err := LogShort(dir)
	if err != nil {
		return false
	}
	_, ok := TrackedBranch(branches, name)
	return ok
}

// BranchDeleteOptions configures a `gs branch delete` invocation.
type BranchDeleteOptions struct {
	// Dir is the working directory the command runs in (the git repo).
	Dir string
	// Name is the branch to delete.
	Name string
	// Force allows deleting a branch with unmerged commits. Without it,
	// `gs branch delete` refuses a branch that isn't merged.
	Force bool
}

// BuildBranchDeleteArgs constructs the argument list for `gs branch delete`
// from branch and force, without invoking anything.
//
// Two flags are non-negotiable, verified against the installed git-spice
// (0.31.2) rather than assumed:
//
//   - `--restack=none`, always passed explicitly. `--restack` on this
//     version is not boolean — it takes none|aboves|upstack — and the
//     `spice.branchDelete.restack` user config can enable restacking with
//     no flag passed at all. Omitting this flag would let a stranger's git
//     config decide whether afk rebases branches nobody named.
//   - `--no-prompt`, the global flag disabling git-spice's interactive
//     prompts. Without it, an invocation with no branches given (or any
//     other missing-information case) can block forever waiting on a
//     terminal that a non-interactive caller never has.
func BuildBranchDeleteArgs(branch string, force bool) []string {
	args := []string{"branch", "delete", branch}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--restack=none", "--no-prompt")
	return args
}

// BuildBranchDeleteCmd constructs (but does not run) the *exec.Cmd for
// deleting a tracked branch, using the resolved binary path bin.
func BuildBranchDeleteCmd(bin string, opts BranchDeleteOptions) *exec.Cmd {
	cmd := exec.Command(bin, BuildBranchDeleteArgs(opts.Name, opts.Force)...)
	cmd.Dir = opts.Dir
	return cmd
}

// BranchDeleteResult reports the outcome of a DeleteBranch call.
type BranchDeleteResult struct {
	// Retargeted lists the names of branches that were upstack of the
	// deleted branch immediately before deletion. `gs branch delete`
	// retargets these onto the deleted branch's downstack parent (or trunk
	// if none), even with --restack=none — that flag only controls whether
	// git-spice rebases them onto their new parent immediately, not whether
	// the parent pointer moves. Populated from the LogShort topology
	// captured before the delete runs, never by scraping command output
	// (this package's contract forbids treating gs's logger-tagged stdout
	// as a stable format). Callers report this so a user can restack those
	// branches deliberately rather than being surprised by a moved base.
	Retargeted []string
}

// DeleteBranch deletes a git-spice-tracked branch per opts, using the
// resolved git-spice binary. It requires a resolvable git-spice binary and
// a tracked branch; callers should gate on IsTracked(opts.Dir, opts.Name)
// first.
func DeleteBranch(opts BranchDeleteOptions) (BranchDeleteResult, error) {
	bin, ok := BinaryPath()
	if !ok {
		return BranchDeleteResult{}, fmt.Errorf("spice: git-spice binary not found on $PATH")
	}

	var result BranchDeleteResult
	if branches, err := LogShort(opts.Dir); err == nil {
		if b, found := TrackedBranch(branches, opts.Name); found {
			for _, up := range b.Ups {
				result.Retargeted = append(result.Retargeted, up.Name)
			}
		}
	}

	cmd := BuildBranchDeleteCmd(bin, opts)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("spice: %s branch delete: %w: %s", bin, err, string(out))
	}
	return result, nil
}

// BranchTrackOptions configures a `gs branch track` invocation.
type BranchTrackOptions struct {
	// Dir is the working directory the command runs in (the git repo).
	Dir string
	// Name is the branch to track.
	Name string
	// Base is the parent branch to track Name onto. Must not be empty:
	// an empty Base would cause gs to guess a parent, which this package
	// must never allow (the mis-parenting trap described in the plan).
	Base string
}

// BuildBranchTrackArgs constructs the argument list for `gs branch track`
// from opts, without invoking anything.
//
// `--no-prompt` is non-negotiable: afk invokes gs as a non-interactive
// subprocess, and a base prompt would hang session creation waiting on
// a terminal that the parent will never have.
func BuildBranchTrackArgs(opts BranchTrackOptions) []string {
	args := []string{"--no-prompt", "branch", "track", opts.Name}
	if opts.Base != "" {
		args = append(args, "--base", opts.Base)
	}
	return args
}

// BuildBranchTrackCmd constructs (but does not run) the *exec.Cmd for
// tracking a branch, using the resolved binary path bin.
func BuildBranchTrackCmd(bin string, opts BranchTrackOptions) *exec.Cmd {
	cmd := exec.Command(bin, BuildBranchTrackArgs(opts)...)
	cmd.Dir = opts.Dir
	return cmd
}

// TrackBranch tracks a branch with git-spice per opts, using the resolved
// git-spice binary. It returns an error immediately without invoking anything
// when opts.Base is empty — this prevents the mis-parenting trap where
// `gs --no-prompt branch track <name>` with no --base guesses a parent and
// exits 0. It requires a resolvable git-spice binary; callers should gate on
// Detect(opts.Dir) first.
func TrackBranch(opts BranchTrackOptions) error {
	if opts.Base == "" {
		return fmt.Errorf("spice: branch track: refusing to track %q without an explicit base — gs would guess a parent", opts.Name)
	}

	bin, ok := BinaryPath()
	if !ok {
		return fmt.Errorf("spice: git-spice binary not found on $PATH")
	}

	cmd := BuildBranchTrackCmd(bin, opts)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("spice: %s branch track: %w: %s", bin, err, string(out))
	}
	return nil
}

// BuildBranchUntrackArgs constructs the argument list for `gs branch untrack`
// from name, without invoking anything.
//
// `--no-prompt` is non-negotiable: afk invokes gs as a non-interactive
// subprocess.
func BuildBranchUntrackArgs(name string) []string {
	return []string{"--no-prompt", "branch", "untrack", name}
}

// BuildBranchUntrackCmd constructs (but does not run) the *exec.Cmd for
// untracking a branch, using the resolved binary path bin.
func BuildBranchUntrackCmd(bin string, dir string, name string) *exec.Cmd {
	cmd := exec.Command(bin, BuildBranchUntrackArgs(name)...)
	cmd.Dir = dir
	return cmd
}

// UntrackBranch untracks a branch with git-spice, using the resolved
// git-spice binary. It requires a resolvable git-spice binary; callers should
// gate on Detect(dir) first.
func UntrackBranch(dir string, name string) error {
	bin, ok := BinaryPath()
	if !ok {
		return fmt.Errorf("spice: git-spice binary not found on $PATH")
	}

	cmd := BuildBranchUntrackCmd(bin, dir, name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("spice: %s branch untrack: %w: %s", bin, err, string(out))
	}
	return nil
}
