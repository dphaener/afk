// Package session implements `afk new`: creating an isolated afk session
// (branch + worktree + seeded status.json + launched conductor).
//
// The package is split into a pure planning phase (Build) and an execution
// phase (Plan.Run). Build performs only read-only detection and precondition
// checks; every side effect lives in a Step closure that runs solely in the
// non-dry-run path. This keeps `afk new --dry-run` provably side-effect-free
// and keeps the whole flow testable via injected dependencies (Deps).
//
// Git-spice concerns are delegated exclusively to internal/spice and
// dependency probing to internal/probe — this package embeds neither.
package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/probe"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/spice"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/worktree"
)

// nameRE is the STRICT kebab-case identity contract from the status schema
// (§1): a session name becomes the branch, the worktree directory, and the
// plan slug, so it must be safe as all three and safe to interpolate into a
// shell/exec argv. Must start with a lowercase alphanumeric, then any run of
// lowercase alphanumerics and hyphens.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateName enforces the strict kebab-case identity rule. Anything else is
// rejected — there is no normalization or coercion.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("session name is required")
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid session name %q: must be kebab-case matching %s (lowercase letters, digits, hyphens; must start with a letter or digit)", name, nameRE.String())
	}
	return nil
}

// Config holds the validated, user-facing inputs for `afk new`.
type Config struct {
	// Name is the session identity (branch = worktree dir = plan slug). It is
	// the ONLY value ever interpolated into a shell/exec command line, and it
	// is strictly validated as kebab-case before any use.
	Name string
	// Description is optional free-text initial task context for the conductor.
	// It is NEVER spliced into a shell line; it is written to a sidecar file
	// inside the worktree.
	Description string
	// RepoDir is the git repository `afk new` operates in.
	RepoDir string
	// Base optionally forces the base branch, bypassing the picker.
	Base string
	// DryRun prints the full action list and makes zero changes.
	DryRun bool
	// NoGitSpice forces plain-git behavior even when git-spice is detected.
	NoGitSpice bool
	// Scratch creates a one-off session: no branch, no worktree, no plan and
	// no status.json. Its host runs the bare provider CLI in RepoDir, so the
	// conversation still has full repo context. Such a session is found on
	// disk through .afk/<name>/host.json alone — deliberately, because it
	// has no conductor and therefore no phase to record in the externally
	// owned status schema. Base and NoGitSpice are meaningless with it set.
	Scratch bool
	// Autonomous launches the conductor in autonomous mode: a fixed
	// --autonomous argv token plus a .afk/{name}/autonomous marker written
	// at seed time so the mode survives dashboard restarts, which re-run
	// /afk:go without the flag. Meaningless with Scratch (no conductor);
	// the CLI rejects the combination and assembleScratchSteps ignores it.
	Autonomous bool
	// Provider names the agent CLI the host runs (see internal/provider).
	// Empty means the default, Claude. Build rejects an unknown name and
	// canonicalizes "claude" to empty, so a Claude session's host argv and
	// on-disk footprint are byte-identical to sessions created before
	// providers existed.
	Provider string
	// Unattended launches the agent in the provider's unattended permission
	// mode, in which it never stops to ask whether it may act. The daemon
	// sets it, because nobody is at the terminal. It adds the provider's
	// option to the child argv and writes a .afk/{name}/unattended marker at
	// seed time, so the mode survives a host relaunch and a child restart
	// (see unattended.go). Build rejects it for a provider that has no such
	// mode. Independent of Autonomous: that one tells the conductor not to
	// ask the owner questions, this one tells the CLI not to ask permission.
	Unattended bool
}

// Deps is the injectable seam for every environment-touching operation. Real
// wiring comes from DefaultDeps; tests substitute fakes to exercise the flow
// hermetically. A nil field is filled in by Create/DefaultDeps where it has a
// sensible default; PickBase intentionally defaults to nil (non-interactive).
type Deps struct {
	// DetectGitSpice reports whether dir is operating in git-spice mode.
	DetectGitSpice func(dir string) bool
	// Stack returns the git-spice stack (trunk plus tracked branches).
	Stack func(dir string) ([]spice.Branch, error)
	// HasWorktrunk reports whether the `wt` binary is available.
	HasWorktrunk func() bool
	// AfkPreflight reports whether the afk plugin (and /afk:go) is
	// available in dir, plus manual remediation steps when it is not. nil,
	// the default, runs the session provider's own Preflight through Run:
	// which CLI to ask depends on Config.Provider, which a Deps built before
	// the Config (as new.go does) cannot know. A non-nil value overrides the
	// check for every provider, which is what tests inject.
	AfkPreflight func(dir string) (ok bool, manualSteps string)
	// PickBase presents the base picker. nil means non-interactive: the
	// resolved default (trunk, or --base) is used without prompting.
	PickBase func(prompt string, options []string, dflt string) (string, error)
	// CreateSpiceBranch creates a tracked git-spice branch.
	CreateSpiceBranch func(opts spice.BranchCreateOptions) error
	// TrackSpiceBranch tracks an existing local branch into the git-spice
	// stack under an explicit parent. assembleSteps calls it for at most two
	// branches per create: the picked base (when its own status.json names a
	// tracked parent) and the new session branch after worktrunk created it.
	// Never call it with an empty opts.Base: spice.TrackBranch refuses and
	// gs would otherwise guess a parent, which is the mis-parenting trap
	// described in the plan.
	TrackSpiceBranch func(opts spice.BranchTrackOptions) error
	// UntrackSpiceBranch removes name from the git-spice stack without
	// touching the branch itself. Used only by the rollback of the step that
	// tracked the new session branch — a base branch this create tracked is
	// deliberately left tracked.
	UntrackSpiceBranch func(dir, name string) error
	// Run executes name+args in dir and returns stdout. Used for git.
	Run func(dir, name string, args ...string) (string, error)
	// Executable resolves the path of the running afk binary — the program
	// re-executed as `afk _host` to launch a session's detached host.
	// Defaults to os.Executable. This is an explicit seam because under
	// `go test` os.Executable() resolves to the TEST binary, which has no
	// `_host` subcommand; tests inject a stand-in here instead.
	Executable func() (string, error)
	// SignalKill sends sig to pid; defaults to syscall.Kill. Used only by
	// rollback, to stop a host this invocation just spawned.
	SignalKill func(pid int, sig syscall.Signal) error
	// DialExit dials socket and sends a host.Exit frame, bounded by timeout;
	// defaults to a real unix dial. Rollback tries this before SignalKill —
	// the same ordering, and the same defaults, as CleanDeps uses.
	DialExit func(socket string, timeout time.Duration) error
	// Now supplies the current time (for updated_at); defaults to time.Now.
	Now func() time.Time
	// Stdout/Stderr are the output sinks; default to os.Stdout/os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
}

// DefaultDeps returns Deps wired to the real environment.
func DefaultDeps() Deps {
	return Deps{
		DetectGitSpice:     spice.Detect,
		Stack:              spice.LogShort,
		HasWorktrunk:       func() bool { return probe.ProbeWorktrunk().Present },
		AfkPreflight:       nil, // dispatched on Config.Provider by Build
		CreateSpiceBranch:  spice.CreateBranch,
		TrackSpiceBranch:   spice.TrackBranch,
		UntrackSpiceBranch: spice.UntrackBranch,
		Run:                defaultRun,
		Executable:         os.Executable,
		SignalKill:         signalKill,
		DialExit:           dialExit,
		Now:                func() time.Time { return time.Now().UTC() },
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
	}
}

// withDefaults fills in any nil field on d that has a real-environment default.
func (d Deps) withDefaults() Deps {
	def := DefaultDeps()
	if d.DetectGitSpice == nil {
		d.DetectGitSpice = def.DetectGitSpice
	}
	if d.Stack == nil {
		d.Stack = def.Stack
	}
	if d.HasWorktrunk == nil {
		d.HasWorktrunk = def.HasWorktrunk
	}
	if d.CreateSpiceBranch == nil {
		d.CreateSpiceBranch = def.CreateSpiceBranch
	}
	if d.TrackSpiceBranch == nil {
		d.TrackSpiceBranch = def.TrackSpiceBranch
	}
	if d.UntrackSpiceBranch == nil {
		d.UntrackSpiceBranch = def.UntrackSpiceBranch
	}
	if d.Run == nil {
		d.Run = def.Run
	}
	if d.Executable == nil {
		d.Executable = def.Executable
	}
	if d.SignalKill == nil {
		d.SignalKill = def.SignalKill
	}
	if d.DialExit == nil {
		d.DialExit = def.DialExit
	}
	if d.Now == nil {
		d.Now = def.Now
	}
	if d.Stdout == nil {
		d.Stdout = def.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = def.Stderr
	}
	// PickBase is intentionally left nil (non-interactive default).
	return d
}

// defaultRun runs a command in dir and returns stdout; on failure the error
// carries the command and its stderr for diagnosis.
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

// Status is the seeded status.json contract (status-schema.md §2). All nine
// keys are always present; JSON tag order mirrors the schema table.
type Status struct {
	SessionID     string `json:"session_id"`
	PlanName      string `json:"plan_name"`
	Phase         string `json:"phase"`
	Branch        string `json:"branch"`
	BaseBranch    string `json:"base_branch"`
	LoopCount     int    `json:"loop_count"`
	AwaitingInput bool   `json:"awaiting_input"`
	TmuxWindow    string `json:"tmux_window"`
	UpdatedAt     string `json:"updated_at"`
}

// rfc3339UTC renders t as an RFC 3339 UTC timestamp per the schema.
func rfc3339UTC(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// statusDir is the per-session directory inside a worktree.
func statusDir(worktree, name string) string {
	return filepath.Join(worktree, ".afk", name)
}

// writeStatusAtomic writes s to <dir>/status.json via a unique same-directory
// temp file + rename (schema §6). Same-directory rename is required for
// atomicity on POSIX.
func writeStatusAtomic(dir string, s Status) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling status: %w", err)
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, fmt.Sprintf(".status.json.tmp.%d", os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp status: %w", err)
	}
	final := filepath.Join(dir, "status.json")
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming status into place: %w", err)
	}
	return nil
}

// seedStatus creates the initial status.json (schema §3/§4 seed values) and,
// if a description is set, a sidecar description file. tmuxWindow is always
// passed "": no code path in this package ever populates tmux_window (the
// field stays on Status for schema compatibility).
func seedStatus(worktree string, cfg Config, base, tmuxWindow string, now func() time.Time) error {
	dir := statusDir(worktree, cfg.Name)
	s := Status{
		SessionID:     "",
		PlanName:      cfg.Name,
		Phase:         "planning",
		Branch:        cfg.Name,
		BaseBranch:    base,
		LoopCount:     0,
		AwaitingInput: false,
		TmuxWindow:    tmuxWindow,
		UpdatedAt:     rfc3339UTC(now()),
	}
	if err := writeStatusAtomic(dir, s); err != nil {
		return err
	}
	return writeDescriptionSidecar(dir, cfg.Description)
}

// writeDescriptionSidecar persists free-text description into dir, never
// interpolating it into any command line. It is split out of seedStatus
// because a scratch session writes this file and nothing else: it has no
// status.json to carry the text alongside.
func writeDescriptionSidecar(dir, description string) error {
	if strings.TrimSpace(description) == "" {
		return nil
	}
	text := strings.TrimRight(description, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "description.md"), []byte(text), 0o644); err != nil {
		return fmt.Errorf("writing description file: %w", err)
	}
	return nil
}

// --- detached session host ----------------------------------------------

// HostSpawn identifies the session whose detached host process is to be
// started. Worktree is the session's own worktree (NOT the repo root): it is
// the host's and the child's working directory, the parent of
// .afk/<name>/{host.log,host.json}, and — for socket-path derivation — the
// per-session key handed to host.SocketPath, matching how `afk attach`
// already calls host.ReadMeta(worktree, name).
type HostSpawn struct {
	Name     string
	Worktree string
	// Scratch selects the one-off child argv — the bare provider CLI instead
	// of the conductor's prompt. The mode lives here rather
	// than being read off Config so hostArgs stays pure and the argv
	// difference is assertable without building a Plan.
	Scratch bool
	// Autonomous appends the fixed --autonomous token to the conductor's
	// argv. A boolean, never text, so nothing user-supplied can ride along.
	// Ignored when Scratch is set.
	Autonomous bool
	// Provider names the agent CLI the host runs; empty means Claude.
	// SpawnHost fills an empty value from the .afk/<name>/provider
	// sidecar, because the dashboard's relaunch of a stopped host builds a
	// HostSpawn with only Name and Worktree.
	Provider string
	// Unattended runs the child in the provider's unattended permission
	// mode. SpawnHost also turns it on when the .afk/<name>/unattended
	// marker exists, for the same reason it fills Provider: the dashboard's
	// relaunch names only the session, and a daemon-run session relaunched
	// without the mode would wait at a permission dialog nobody sees.
	Unattended bool
	// ResumeID, when set, makes the child the provider's resume command for
	// that conversation id instead of the launch command. The daemon uses it
	// to bring back a session whose host is gone (docs/PLAN.md §5.6). It is a
	// value, never free text from a user: it comes from status.json's
	// session_id. It is used as given and is never empty on that path.
	ResumeID string
	// MasterPlan makes the child the provider's planning command: the attended
	// session `afk plan` starts, in which the owner answers questions. Scratch
	// and Autonomous are ignored with it, since that command takes neither.
	// SpawnHost also turns it on when the .afk/<name>/planning marker exists,
	// for the same reason it fills Provider and Unattended: the dashboard's
	// relaunch names only the session, and a planning session relaunched
	// without it would start a conductor for a session that has no plan.
	// Together with Unattended or ResumeID it is an error, never a launch: a
	// planning session with nobody at the terminal waits for ever on its first
	// question.
	MasterPlan bool
}

// HostProcess describes a host started by SpawnHost. PID is captured before
// the process handle is released, so it stays valid for signalling; LogPath
// is where the host records why a child died, which callers surface in
// their failure guidance.
type HostProcess struct {
	PID      int
	Socket   string
	LogPath  string
	MetaPath string
}

// hostLogPath and hostMetaPath are the per-session host file paths inside a
// worktree, alongside status.json.
func hostLogPath(worktree, name string) string {
	return filepath.Join(statusDir(worktree, name), "host.log")
}

func hostMetaPath(worktree, name string) string {
	return filepath.Join(statusDir(worktree, name), "host.json")
}

// hostArgs builds the argument list (argv[1:]) for the detached host
// process: the `_host` subcommand's flags, then `--`, then the child argv
// the host runs on its PTY. Only fixed tokens and the strictly-validated
// session name ever reach the argv: no description, base or other
// user-supplied text, and a scratch spawn interpolates nothing at all. Pure —
// this is the seam the argv tests assert against.
//
// --provider is appended only for a non-empty Provider, so a Claude session's
// host argv stays byte-identical to the one afk built before providers
// existed. The host records it in host.json so a wedged-child restart resumes
// with the same CLI.
func hostArgs(spec HostSpawn, socket string) []string {
	args := []string{
		"_host",
		"--name", spec.Name,
		"--worktree", spec.Worktree,
		"--socket", socket,
		"--log", hostLogPath(spec.Worktree, spec.Name),
		"--meta", hostMetaPath(spec.Worktree, spec.Name),
	}
	if spec.Provider != "" {
		args = append(args, "--provider", spec.Provider)
	}
	if spec.Unattended {
		// The daemon supervises an unattended session through files and
		// attaches no client, so a host that probes only while a client is
		// attached would never see the agent wedge. The flag must come before
		// the separator, or it would be read as part of the child's command.
		args = append(args, "--probe-unattached")
	}
	args = append(args, "--")
	return append(args, childArgv(spec)...)
}

// childArgv is the command the host runs on its PTY. It is split out of
// hostArgs because the dry-run step text needs the same argv but has no
// socket path to give hostArgs yet; both callers share this one definition so
// the printed command can never drift from the executed one. Pure.
//
// It cannot fail, and launchArgv can, so this is launchArgv with the error
// dropped. That is safe only because every path that reaches here has already
// called launchArgv and stopped on its error: Build for a new session (and so
// for --dry-run), resolveSpawnPlanning and resolveSpawnUnattended for
// SpawnHost. An error return would ripple into the pure display helper
// childCommandLine and into hostArgs for no benefit. Keep that true when
// adding a caller: for an unattended spawn the dropped error would mean a
// child started in the mode that asks questions, and for a planning spawn it
// would mean an empty argv, so a host with no command to run.
func childArgv(spec HostSpawn) []string {
	argv, _ := launchArgv(spec)
	return argv
}

// launchArgv is the child command for spec: the provider's launch argv (or,
// with ResumeID set, its resume argv for that conversation), passed
// through the provider's unattended mode when the spawn asks for it. Pure.
//
// An unknown Provider falls back to the default provider's argv rather than
// returning an error, as it always has: Build and resolveSpawnProvider have
// validated the name before anything gets here.
//
// A provider with no unattended mode is an error, and the argv returned with
// it is the plain one only so display code has something to print. Callers
// that start a process must not use it. This package never names the option
// or the provider that has it: the whole translation is p.Unattended.
//
// A planning spawn (MasterPlan) is p.PlanArgv and nothing else. Its two errors
// both come with a nil argv, because there is no plain command to fall back
// on: the provider has no planning session, or the spec also asks for an
// unattended or resumed child. The second is checked first, since it is wrong
// under every provider.
func launchArgv(spec HostSpawn) ([]string, error) {
	p, err := provider.Lookup(spec.Provider)
	if err != nil {
		p, _ = provider.Lookup(provider.Default)
	}
	if spec.MasterPlan {
		if spec.Unattended || spec.ResumeID != "" {
			return nil, fmt.Errorf("session %q: %w", spec.Name, ErrPlanningUnattended)
		}
		argv, err := p.PlanArgv()
		if err != nil {
			return nil, fmt.Errorf("session %q cannot be a planning session with provider %q: %w", spec.Name, p.Name(), err)
		}
		return argv, nil
	}
	var argv []string
	if spec.ResumeID != "" {
		argv = p.ResumeArgv(spec.ResumeID)
	} else {
		argv = p.LaunchArgv(spec.Name, spec.Scratch, spec.Autonomous)
	}
	if !spec.Unattended {
		return argv, nil
	}
	unattended, err := p.Unattended(argv)
	if err != nil {
		return argv, fmt.Errorf("session %q cannot run unattended with provider %q, so afk will not start it with nobody at the terminal: %w", spec.Name, p.Name(), err)
	}
	return unattended, nil
}

// ErrPlanningUnattended is the refusal for a planning spawn that also asks for
// the unattended mode or for a resumed conversation, which is how the daemon
// brings a session back with nobody watching. A planning session exists to
// ask its owner questions, so started that way it would sit at its first
// question inside a detached host, looking alive.
var ErrPlanningUnattended = errors.New("a planning session is never unattended: it asks its owner questions, so afk only starts it from `afk plan` at a terminal")

// hostProviderNote names the --provider flag hostArgs passes to the host, so
// the dry-run launch line shows the provider the host will record and restart
// with, not just the child command. Empty for the default provider, whose host
// argv carries no --provider flag, so Claude's line stays byte-identical. Pure.
func hostProviderNote(spec HostSpawn) string {
	if spec.Provider == "" {
		return ""
	}
	return " (host --provider " + spec.Provider + ")"
}

// resolveSpawnProvider settles which provider a host spawn runs. An empty
// spec.Provider is filled from the session's provider sidecar — the only
// carrier of the choice across a dashboard relaunch, which passes no
// provider. The result is validated: an unknown name (a hand-edited or
// corrupt sidecar) is an error, not a silent fall back to Claude, because
// relaunching a Codex session under the wrong CLI loses its conversation
// without saying why. A name that resolves to the default is canonicalized
// to empty so the host argv matches a pre-provider Claude spawn.
func resolveSpawnProvider(spec HostSpawn) (HostSpawn, error) {
	if spec.Provider == "" {
		spec.Provider = ReadProvider(spec.Worktree, spec.Name)
	}
	p, err := provider.Lookup(spec.Provider)
	if err != nil {
		return spec, fmt.Errorf("session %q: %w", spec.Name, err)
	}
	if p.Name() == provider.Default {
		spec.Provider = ""
	}
	return spec, nil
}

// resolveSpawnPlanning settles whether a host spawn is a planning session: on
// when the caller set MasterPlan or when the session's planning marker exists.
// It sits beside resolveSpawnProvider and resolveSpawnUnattended because it is
// the same problem: the dashboard's relaunch of a stopped host passes only
// Name and Worktree, so the marker is the only carrier. It must run after
// resolveSpawnProvider, which settles the provider being asked, and before
// resolveSpawnUnattended, whose own launchArgv call then refuses a session
// carrying both markers.
//
// The provider's refusal is returned here, before anything is forked. Left to
// hostArgs it would be dropped (see childArgv) and the host would be started
// with no child command at all.
func resolveSpawnPlanning(spec HostSpawn) (HostSpawn, error) {
	if !spec.MasterPlan {
		spec.MasterPlan = IsPlanning(spec.Worktree, spec.Name)
	}
	if !spec.MasterPlan {
		return spec, nil
	}
	if _, err := launchArgv(spec); err != nil {
		return spec, err
	}
	return spec, nil
}

// resolveSpawnUnattended settles whether a host spawn runs the child in the
// provider's unattended mode: on when the caller set it or when the session's
// unattended marker exists. It sits beside resolveSpawnProvider because it is
// the same problem: the dashboard's relaunch of a stopped host passes only
// Name and Worktree, so the marker is the only carrier of the mode. It must
// run after resolveSpawnProvider, which settles the provider being asked.
//
// Go reads this marker itself, unlike the autonomous one, which the plugin's
// conductor reads: the mode is an option on the CLI's command line and cannot
// be recovered once the CLI has started.
//
// A provider with no unattended mode is an error here, before anything is
// forked, and never a plain launch. A session that was meant to run with
// nobody watching and is started in the mode that asks questions waits at a
// permission dialog inside a detached host, where it looks alive for ever.
func resolveSpawnUnattended(spec HostSpawn) (HostSpawn, error) {
	if !spec.Unattended {
		spec.Unattended = IsUnattended(spec.Worktree, spec.Name)
	}
	if _, err := launchArgv(spec); err != nil {
		return spec, err
	}
	return spec, nil
}

// ChildCommandLine is the command a host spawned from spec would run, as one
// printable line. `afk plan --dry-run` prints it, so what the dry run shows is
// built by the same code as what a real start runs and cannot drift from it.
// It does not read the session's markers (SpawnHost does that), so the caller
// passes the spec it would hand to SpawnHost. It is "" when the spec has no
// command, which for a planning spec means the provider refused: ask the
// provider's PlanArgv first and show its error instead.
func ChildCommandLine(spec HostSpawn) string {
	return childCommandLine(spec)
}

// childCommandLine renders childArgv for display, quoting every argument
// after the program name so the slash command reads as the single word it is.
// An empty argv renders as "": only a refused planning spec has one, and
// indexing it would turn a refusal into a panic.
func childCommandLine(spec HostSpawn) string {
	argv := childArgv(spec)
	if len(argv) == 0 {
		return ""
	}
	parts := []string{argv[0]}
	for _, a := range argv[1:] {
		parts = append(parts, strconv.Quote(a))
	}
	return strings.Join(parts, " ")
}

// buildHostCmd assembles the exec.Cmd for the detached host. It only
// populates fields — nothing runs until Start — so tests can assert the
// argv, the stdio wiring and SysProcAttr without spawning anything.
//
// The stdio discipline is load-bearing, not defensive: all three streams
// are pointed at an explicit *os.File (os.DevNull, opened by SpawnHost) so
// the host inherits NONE of the spawning process's descriptors. A host that
// held its spawner's fd 2 would wedge the dashboard, whose exec wrapper
// sets a non-*os.File stderr and therefore Waits on a pipe that only
// reaches EOF when every holder closes it (internal/ui/messages.go:137-152).
// Passing *os.File values also keeps exec.Cmd from creating pipes and
// copier goroutines, which is what makes releasing the process safe.
// Setsid detaches the host into its own session so it survives the
// spawner's terminal and leads its own process group.
func buildHostCmd(self string, spec HostSpawn, socket string, null *os.File) *exec.Cmd {
	cmd := exec.Command(self, hostArgs(spec, socket)...)
	cmd.Dir = spec.Worktree
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// SpawnHost starts the detached `afk _host` process that owns spec's PTY
// and returns once it has been started — it never waits for it. The host
// outlives this process: `afk new` exits immediately afterwards, `afk
// dash`/`afk attach` connect to the returned socket.
//
// It is exported because the dashboard's restart flow re-spawns a stopped
// session's host through this exact function: the argv, the socket
// derivation and the stdio discipline all have to agree with what `afk
// attach` later expects, so both callers share one spawn path.
func SpawnHost(spec HostSpawn, in Deps) (HostProcess, error) {
	d := in.withDefaults()
	if spec.Name == "" {
		return HostProcess{}, fmt.Errorf("spawning session host: session name is required")
	}
	if spec.Worktree == "" {
		return HostProcess{}, fmt.Errorf("spawning session host %q: worktree is required", spec.Name)
	}
	spec, err := resolveSpawnProvider(spec)
	if err != nil {
		return HostProcess{}, fmt.Errorf("spawning session host: %w", err)
	}
	spec, err = resolveSpawnPlanning(spec)
	if err != nil {
		return HostProcess{}, fmt.Errorf("spawning session host: %w", err)
	}
	spec, err = resolveSpawnUnattended(spec)
	if err != nil {
		return HostProcess{}, fmt.Errorf("spawning session host: %w", err)
	}
	self, err := d.Executable()
	if err != nil {
		return HostProcess{}, fmt.Errorf("locating the afk binary: %w", err)
	}
	if self == "" {
		return HostProcess{}, fmt.Errorf("locating the afk binary: got an empty path")
	}
	socket, err := host.SocketPath(spec.Worktree, spec.Name)
	if err != nil {
		return HostProcess{}, err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return HostProcess{}, fmt.Errorf("opening %s for the session host: %w", os.DevNull, err)
	}
	defer null.Close()

	cmd := buildHostCmd(self, spec, socket, null)
	if err := cmd.Start(); err != nil {
		return HostProcess{}, fmt.Errorf("spawning session host for %q: %w", spec.Name, err)
	}
	h := HostProcess{
		PID:      cmd.Process.Pid,
		Socket:   socket,
		LogPath:  hostLogPath(spec.Worktree, spec.Name),
		MetaPath: hostMetaPath(spec.Worktree, spec.Name),
	}
	// Reap in the background, never block: the host is meant to outlive us,
	// so SpawnHost returns immediately — but the spawner must still Wait
	// eventually. A bare Release() would leave an exited host as a zombie
	// whenever the spawner stays alive (the dashboard's restart flow, or
	// `afk new` blocked in the dash hand-off): signal-0 liveness stays true
	// for a zombie, so discovery would keep reporting the session as running
	// and `afk clean` would abort with "still alive after SIGKILL" before
	// ever removing the worktree. When the spawner exits first the goroutine
	// dies with it and init reaps the host instead — same outcome either way.
	// Wait is safe to run concurrently with the spawner's own lifetime
	// because all three stdio fields are *os.File: exec.Cmd created no pipes
	// and no copier goroutines for Wait to block on.
	go func() { _ = cmd.Wait() }()
	return h, nil
}

// hostRollbackExitTimeout bounds the socket Exit attempt when rolling back a
// just-spawned host. It is deliberately short: rollback is already an error
// path, and SIGTERM is a strictly more reliable second attempt than waiting
// longer on a host that is not answering.
const hostRollbackExitTimeout = 500 * time.Millisecond

// stopSpawnedHost stops a host started by SpawnHost during rollback. It asks
// over the socket first (Exit lets the host terminate its child and remove
// its own socket and host.json), then falls back to SIGTERM on the PID
// captured at spawn, which the host's signal handler also treats as an
// orderly shutdown.
//
// Unlike clean.go's stopLiveHost this never polls host.Alive and never
// escalates to SIGKILL, because here the host is still THIS process's own
// child: it is reaped asynchronously by SpawnHost's background Wait, so
// between its exit and that reap signal-0 liveness still reports true — a
// wait-for-death loop would race the reaper and could report a false
// "still alive after SIGKILL".
func stopSpawnedHost(d Deps, h HostProcess) error {
	exitErr := fmt.Errorf("no socket path recorded for the session host")
	if h.Socket != "" {
		exitErr = d.DialExit(h.Socket, hostRollbackExitTimeout)
	}
	if exitErr == nil {
		return nil
	}
	if h.PID <= 0 {
		return fmt.Errorf("stopping session host: %w (and no pid to signal)", exitErr)
	}
	if err := d.SignalKill(h.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stopping session host: Exit over %s failed (%v) and SIGTERM to pid %d failed: %w", h.Socket, exitErr, h.PID, err)
	}
	return nil
}

// Step is one line of the action plan. Informational steps have a nil run.
// bestEffort steps warn (not roll back) on failure.
type Step struct {
	Desc       string
	run        func() error
	undo       func() error
	bestEffort bool
}

// execState is the shared mutable state threaded through the step closures at
// execution time. It is never touched during dry-run.
type execState struct {
	worktree string
	host     HostProcess
	// runtime is the runtime that spawned host, so the rollback undoes it
	// with the same implementation.
	runtime       Runtime
	createdWT     bool
	createdBranch bool
	createdDir    bool
	// baseTracked is the RUN-TIME git-spice tracking state of the base
	// branch: seeded from Plan.BaseTracked and promoted to true only by the
	// base-track step. The creation step reads it instead of Plan.BaseTracked
	// so that a base-track attempt which fails at run time degrades to plain
	// git rather than calling `gs branch create --target <untracked>`, which
	// git-spice rejects with "branch not tracked".
	baseTracked bool
	// trackedBaseHere records that THIS invocation tracked the base branch.
	// Rollback's note is conditioned on it: a base that was already tracked
	// must not be reported as something this create left behind.
	trackedBaseHere bool
	// trackedNew records that this invocation tracked the new session
	// branch, which is the only case in which rollback may untrack it.
	trackedNew bool
}

// outputStyles are the lipgloss styles used to decorate Plan output (step
// progress markers, the dry-run action list). lipgloss v2 has no renderer
// concept — Style.Render() always emits full-fidelity ANSI regardless of
// where the output is going, so the writer-bound behavior this package
// depends on (a real TTY gets color, a pipe or in-memory test buffer gets
// plain, ANSI-free text) is enforced by newOutputStyles's returned io.Writer
// instead of by a renderer keyed to the style. Callers MUST write through
// that returned writer, not the raw w passed in — writing to w directly
// bypasses profiling and silently reintroduces the bug this type exists to
// avoid (see decisions.md finding (f)).
type outputStyles struct {
	arrow  lipgloss.Style // step progress marker ("→")
	number lipgloss.Style // dry-run action list numbering
}

// newOutputStyles builds outputStyles plus a color-profile-aware writer
// wrapping w. The profile is detected from w itself (colorprofile.Detect),
// never from the process's real os.Stdout, so a real TTY gets color and a
// pipe or in-memory test buffer gets plain, ANSI-free text regardless of
// where output is actually going.
func newOutputStyles(w io.Writer) (outputStyles, io.Writer) {
	styles := outputStyles{
		arrow:  lipgloss.NewStyle().Foreground(lipgloss.Color(palette.HeaderAccent)).Bold(true),
		number: lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Muted)),
	}
	return styles, colorprofile.NewWriter(w, os.Environ())
}

// Plan is the fully-resolved action plan produced by Build.
type Plan struct {
	Config       Config
	GitSpice     bool
	UseWorktrunk bool
	Base         string
	// BaseTracked reports whether Base is already a git-spice-tracked
	// branch. Only meaningful when GitSpice is true; false in plain-git mode
	// and false for an explicit --base naming an untracked branch.
	BaseTracked bool
	// BaseTrack is the pure tracking decision for an untracked local Base
	// (see baseTrackDecision). Zero value (baseTrackNone) when the decision
	// was never computed — GitSpice is false, BaseTracked is true, or Base
	// is not a local branch afk can track.
	BaseTrack baseTrackAction
	// BaseParent is the parent branch found in Base's own status.json, set
	// whenever BaseTrack was computed (whether or not it resolved to
	// baseTrackExplicit) — empty when no parent could be determined.
	BaseParent string
	// BaseTrackReason is the human-readable warning text for
	// BaseTrack == baseTrackWarn; empty otherwise.
	BaseTrackReason string
	WorktreeTarget  string // known target path, or "" when worktrunk-managed
	PreflightOK     bool
	PreflightMsg    string
	Steps           []Step

	deps   Deps
	st     *execState
	stdout io.Writer
	stderr io.Writer
	styles outputStyles
}

// Create builds and runs the plan for cfg. This is the package entry point.
func Create(cfg Config, d Deps) error {
	p, err := Build(cfg, d)
	if err != nil {
		return err
	}
	return p.Run()
}

// Build performs validation, read-only detection, and precondition checks,
// then assembles the ordered Step list. It makes no changes. Errors here
// (invalid name, pre-existing branch/worktree, detection failure) abort
// before any side effect and apply equally to dry-run.
func Build(cfg Config, in Deps) (*Plan, error) {
	if err := ValidateName(cfg.Name); err != nil {
		return nil, err
	}
	// Refused here, before any detection, for ordinary and one-off sessions
	// alike. It is not part of ValidateName because `afk plan check` uses that
	// for plan names, where these two are fine. See ReservedName for what each
	// name would break.
	if ReservedName(cfg.Name) {
		return nil, fmt.Errorf("the name %q is kept for `afk plan`, which keeps the project's planning session and its files under that name. Pick another name for this session", cfg.Name)
	}
	d := in.withDefaults()
	repo := cfg.RepoDir
	if repo == "" {
		return nil, fmt.Errorf("RepoDir is required")
	}
	// Resolve the provider before any branch of Build so an unknown name
	// aborts every mode, dry-run included. Canonicalizing the default to ""
	// is what keeps an explicit `--provider claude` byte-identical to no
	// flag at all: no --provider host flag, no sidecar.
	prov, err := provider.Lookup(cfg.Provider)
	if err != nil {
		return nil, err
	}
	if prov.Name() == provider.Default {
		cfg.Provider = ""
	}
	// Checked here, where the provider is already resolved, so a provider
	// with no unattended mode aborts before any side effect: no branch, no
	// worktree, and the same in dry-run. Left to the launch step it would
	// fail last, after everything else had been created and had to be rolled
	// back.
	if cfg.Unattended {
		spawn := HostSpawn{Name: cfg.Name, Scratch: cfg.Scratch, Autonomous: cfg.Autonomous && !cfg.Scratch, Provider: cfg.Provider, Unattended: true}
		if _, err := launchArgv(spawn); err != nil {
			return nil, err
		}
	}

	st := &execState{}
	styles, stdout := newOutputStyles(d.Stdout)
	p := &Plan{
		Config: cfg,
		deps:   d,
		st:     st,
		stdout: stdout,
		stderr: d.Stderr,
		styles: styles,
	}

	// A scratch session creates no branch and no worktree, so every check
	// below it is about resources it will never touch: probing git-spice or
	// worktrunk decides nothing, the branch/worktree-path preconditions guard
	// nothing, and resolving a base would offer a picker for a branch that is
	// never created. Skipping them keeps the dashboard's one-keypress create
	// near-instant — the ONLY command a scratch Build runs is the read-only
	// `git worktree list --porcelain` of the collision check below. The one
	// thing a scratch session does own is .afk/<name>/ in the repo root,
	// where its host writes host.json: two sessions sharing that directory
	// would share a metadata path and a socket, so a collision is refused
	// (no --force in v1).
	if cfg.Scratch {
		dir := statusDir(repo, cfg.Name)
		switch _, err := os.Stat(dir); {
		case err == nil:
			return nil, fmt.Errorf("session directory %q already exists — refusing to proceed (no --force in v1)", dir)
		case !os.IsNotExist(err):
			// Anything other than "doesn't exist" (permission denied, a
			// bad path component, ...) is a real failure to answer the
			// collision question, not evidence there's no collision. Left
			// unchecked here it self-corrects only in the sense that the
			// later mkdir in Run's steps hits the same error — but by
			// then Build has already returned success and assembled a plan,
			// which breaks the "Build makes no changes / errors abort
			// before any side effect" contract this function's own doc
			// comment promises.
			return nil, fmt.Errorf("checking session directory %q: %w", dir, err)
		}
		// The repo root is not the only place a session claims a name: a
		// planned session keeps its .afk/<name>/ inside its OWN worktree,
		// so the stat above cannot see it. Letting the create through anyway
		// puts two same-name sessions on disk, and every clean-side caller
		// resolves a name to the FIRST matching session — so a later
		// confirmed clean of the scratch one can run the planned session's
		// removal path (snapshot, archive, `git worktree remove`) against
		// the wrong session. Enumerate every worktree the same way `afk
		// clean`'s Discover does and refuse the name if ANY of them already
		// carries a .afk/<name>/. A failed enumeration aborts for the same
		// reason the stat-error branch above does: it leaves the collision
		// question unanswered, and "unanswered" must never read as "free".
		worktrees, err := worktree.List(d.Run, repo)
		if err != nil {
			return nil, fmt.Errorf("enumerating worktrees for the %q collision check: %w", cfg.Name, err)
		}
		for _, wt := range worktrees {
			if wt.Bare || filepath.Clean(wt.Path) == filepath.Clean(repo) {
				continue
			}
			wdir := statusDir(wt.Path, cfg.Name)
			switch _, serr := os.Stat(wdir); {
			case serr == nil:
				return nil, fmt.Errorf("session %q already exists (%s) — refusing to proceed (no --force in v1)", cfg.Name, wdir)
			case !os.IsNotExist(serr):
				return nil, fmt.Errorf("checking session directory %q: %w", wdir, serr)
			}
		}
		p.assembleSteps(fmt.Sprintf("One-off session %q in %s (no plan, host runs a bare %s)", cfg.Name, repo, prov.Name()))
		return p, nil
	}

	p.GitSpice = !cfg.NoGitSpice && d.DetectGitSpice(repo)
	p.UseWorktrunk = d.HasWorktrunk()

	// Preconditions — refuse to proceed if the branch already exists. No
	// force flags in v1.
	if branchExists(d, repo, cfg.Name) {
		return nil, fmt.Errorf("branch %q already exists — refusing to proceed (no --force in v1)", cfg.Name)
	}

	// Determine the worktree target. worktrunk manages its own path scheme, so
	// the concrete path is only known after creation; the plain-git path is a
	// fixed root already covered by .gitignore.
	if !p.UseWorktrunk {
		p.WorktreeTarget = filepath.Join(repo, ".claude", "worktrees", cfg.Name)
		if _, err := os.Stat(p.WorktreeTarget); err == nil {
			return nil, fmt.Errorf("worktree path %q already exists — refusing to proceed (no --force in v1)", p.WorktreeTarget)
		}
	}

	// Resolve the base branch (git-spice picker or plain-git trunk). Reuse
	// the returned stack below rather than calling d.Stack a second time.
	base, baseDesc, stack, err := resolveBase(cfg, d, repo, p.GitSpice)
	if err != nil {
		return nil, err
	}
	p.Base = base
	if p.GitSpice {
		_, p.BaseTracked = spice.TrackedBranch(stack, base)
	}

	// An untracked local base, in git-spice mode, is a candidate for
	// tracking (closing the drift described in the plan's Context: every
	// worktrunk-created session branch is otherwise permanently invisible to
	// the picker). branchExists gates this on base actually being a local
	// branch afk can track — an explicit --base naming a remote ref or a
	// SHA (accepted today by `git worktree add`/`wt --base`) must stay
	// warning-free, which is exactly baseTrackDecision's baseTrackNone case.
	// Look up base's own status.json (written by whichever afk session
	// created it) for the parent it was branched from: afk never lets gs
	// guess a parent (the mis-parenting trap in the plan's Context), so an
	// unknown or untracked parent always warns instead of tracking blind.
	if p.GitSpice && !p.BaseTracked && branchExists(d, repo, base) {
		var parent string
		// ReadStatus reports a missing file as stale too; here that means
		// "not a afk session", which the warning must not call stale.
		var staleReason state.StaleReason
		if wt, werr := resolveWorktreePath(d, repo, base); werr == nil {
			res := state.ReadStatus(filepath.Join(statusDir(wt, base), "status.json"))
			switch {
			case !res.Stale:
				parent = res.Status.BaseBranch
			case res.Reason != state.StaleMissing:
				staleReason = res.Reason
			}
		}
		var parentTracked bool
		if parent != "" {
			_, parentTracked = spice.TrackedBranch(stack, parent)
		}
		p.BaseTrack, p.BaseTrackReason = baseTrackDecision(p.GitSpice, p.BaseTracked, true, parent, parentTracked)
		p.BaseParent = parent
		// The pure decision only sees "no parent"; a stale file is a
		// different repair (fix the file) than an absent one, so name it.
		if p.BaseTrack == baseTrackWarn && parent == "" && staleReason != "" {
			p.BaseTrackReason = fmt.Sprintf("status.json for base is stale (%s)", staleReason)
		}
	}

	// Preflight (read-only, non-fatal). A nil AfkPreflight means "ask the
	// session's own provider", which only Build can do: it is the first
	// point where Deps and Config.Provider meet.
	if d.AfkPreflight != nil {
		p.PreflightOK, p.PreflightMsg = d.AfkPreflight(repo)
	} else {
		p.PreflightOK, p.PreflightMsg = prov.Preflight(repo, d.Run)
	}

	p.assembleSteps(baseDesc)
	return p, nil
}

// resolveBase decides the base branch. In git-spice mode it consults the
// stack (offering the picker when interactive); otherwise it uses the
// plain-git trunk with no picker. An explicit --base always wins. The
// returned stack is whatever d.Stack produced in the git-spice arm (nil in
// plain-git mode) — Build reuses it for the base-tracking decision instead
// of calling d.Stack a second time.
//
// The git-spice arm's options list is the tracked stack alone unless the
// local branch listing is also folded in: the tracked stack hides branches
// that were never `gs branch track`-ed, which is exactly what a
// worktrunk-created session branch is. Folding in BranchesPlain's output via
// MergeBaseOptions is what makes those branches selectable again. The local
// listing is only fetched when cfg.Base is empty (an explicit --base never
// reads the option list, so it should not cost a git call), and a failed
// listing degrades to the tracked stack rather than failing Build — a broken
// `for-each-ref` must not block session creation.
func resolveBase(cfg Config, d Deps, repo string, gs bool) (base, desc string, stack []spice.Branch, err error) {
	if gs {
		s, serr := d.Stack(repo)
		if serr != nil {
			return "", "", nil, fmt.Errorf("reading git-spice stack: %w", serr)
		}
		stack = s
		trunk := TrunkOf(stack)
		options := BranchOptions(stack, trunk)
		if cfg.Base == "" {
			if local, lerr := BranchesPlain(d, repo); lerr == nil {
				options = MergeBaseOptions(stack, trunk, local)
			}
		}
		switch {
		case cfg.Base != "":
			return cfg.Base, fmt.Sprintf("Base branch: %q (from --base)", cfg.Base), stack, nil
		case !cfg.DryRun && d.PickBase != nil:
			picked, perr := d.PickBase("Select the base branch for this session:", options, trunk)
			if perr != nil {
				return "", "", nil, fmt.Errorf("base picker: %w", perr)
			}
			if picked == "" {
				picked = trunk
			}
			return picked, fmt.Sprintf("Base branch: %q (git-spice picker)", picked), stack, nil
		default:
			return trunk, fmt.Sprintf("Base branch: %q (git-spice; picker offers %s)", trunk, strings.Join(options, ", ")), stack, nil
		}
	}
	switch {
	case cfg.Base != "":
		return cfg.Base, fmt.Sprintf("Base branch: %q (from --base)", cfg.Base), nil, nil
	case !cfg.DryRun && d.PickBase != nil:
		trunk, terr := trunkPlain(d, repo)
		if terr != nil {
			return "", "", nil, terr
		}
		branches, berr := BranchesPlain(d, repo)
		if berr != nil {
			return trunk, fmt.Sprintf("Base branch: %q (plain-git trunk; branch list unavailable)", trunk), nil, nil
		}
		options := OrderTrunkFirst(branches, trunk)
		picked, perr := d.PickBase("Select the base branch for this session:", options, trunk)
		if perr != nil {
			return "", "", nil, fmt.Errorf("base picker: %w", perr)
		}
		if picked == "" {
			picked = trunk
		}
		return picked, fmt.Sprintf("Base branch: %q (plain-git picker)", picked), nil, nil
	default:
		trunk, terr := trunkPlain(d, repo)
		if terr != nil {
			return "", "", nil, terr
		}
		return trunk, fmt.Sprintf("Base branch: %q (plain-git trunk; no picker)", trunk), nil, nil
	}
}

// baseTrackAction is the three-valued outcome of baseTrackDecision.
type baseTrackAction int

const (
	// baseTrackNone: no tracking needed — not git-spice mode, base is
	// already tracked, or base is not a local branch afk can track (e.g.
	// `--base origin/main` or a bare SHA, which `git worktree add`/`wt
	// --base` accept today and which must stay warning-free).
	baseTrackNone baseTrackAction = iota
	// baseTrackExplicit: untracked local base with a known, tracked parent
	// — track it with an explicit `--base <parent>`.
	baseTrackExplicit
	// baseTrackWarn: untracked local base whose parent is unknown (no
	// status.json, or a stale one) or untracked — never tracked, one
	// warning instead.
	baseTrackWarn
)

// baseTrackDecision is the pure half of "should afk track an untracked
// local base branch, and with what parent". It never guesses: per the
// plan's Context, `gs branch track` with no `--base` guesses a parent and
// exits 0, which is exactly the mis-parenting trap that made a prior
// afk session's history replay onto an unrelated one. An unknown or
// untracked parent always warns rather than tracking blind.
func baseTrackDecision(gitSpice, baseTracked, baseIsLocalBranch bool, parent string, parentTracked bool) (action baseTrackAction, reason string) {
	if !gitSpice || baseTracked || !baseIsLocalBranch {
		return baseTrackNone, ""
	}
	if parent == "" {
		return baseTrackWarn, "no status.json for base"
	}
	if !parentTracked {
		return baseTrackWarn, fmt.Sprintf("parent %q is not tracked", parent)
	}
	return baseTrackExplicit, ""
}

// assembleSteps builds the ordered Step list. Mutating steps carry undo
// closures for rollback. leadDesc is the plan's first informational line: the
// resolved base branch for an ordinary session, the no-plan note for a
// scratch one.
func (p *Plan) assembleSteps(leadDesc string) {
	if p.Config.Scratch {
		p.assembleScratchSteps(leadDesc)
		return
	}

	cfg := p.Config
	d := p.deps
	repo := cfg.RepoDir
	name := cfg.Name
	base := p.Base
	st := p.st

	steps := []Step{{Desc: leadDesc}}

	switch {
	case p.PreflightOK && cfg.Provider == "":
		steps = append(steps, Step{Desc: "Preflight: afk plugin / /afk:go available"})
	case p.PreflightOK:
		steps = append(steps, Step{Desc: fmt.Sprintf("Preflight: afk plugin available in %s", cfg.Provider)})
	default:
		steps = append(steps, Step{Desc: "Preflight WARNING: " + p.PreflightMsg})
	}

	// An untracked local base has to be tracked BEFORE anything is created:
	// both `gs branch create --target <untracked>` and `gs branch track
	// --base <untracked>` fail with "branch not tracked" (git-spice 0.31.2,
	// pinned in the plan), so the base's tracking state decides which
	// creation path can work at all. The parent comes from the base's own
	// status.json (Plan.BaseParent) — never from gs's guess.
	//
	// bestEffort, because git-spice bookkeeping must never fail a create:
	// Plan.Run warns and continues, and the creation step below then sees
	// st.baseTracked still false and degrades.
	if p.GitSpice && p.BaseTrack == baseTrackExplicit {
		steps = append(steps, Step{
			Desc:       fmt.Sprintf("Track base branch %q with git-spice (parent %q from its session's status.json)", base, p.BaseParent),
			bestEffort: true,
			run: func() error {
				// Dir is the repo root, not base's own worktree: `gs
				// --no-prompt branch track --base X <name>` succeeds from the
				// root even when <name> is checked out elsewhere (pinned in
				// the plan), and using one directory for both track calls is
				// one fewer thing to reason about during rollback.
				if err := d.TrackSpiceBranch(spice.BranchTrackOptions{Dir: repo, Name: base, Base: p.BaseParent}); err != nil {
					return err
				}
				st.baseTracked = true
				st.trackedBaseHere = true
				return nil
			},
			// Deliberately NOT reverted. The base branch belongs to another
			// session that outlives this one, and its tracking is now
			// correct (an explicit, verified parent); untracking someone
			// else's branch metadata because an unrelated later step failed
			// is the worse outcome. Say so instead, so a metadata change
			// that survives a rolled-back create is never a surprise.
			undo: func() error {
				if st.trackedBaseHere {
					fmt.Fprintf(p.stderr, "note: base branch %q was tracked with git-spice (parent %q) and is left tracked\n", base, p.BaseParent)
				}
				return nil
			},
		})
	}

	// baseTrackWarn is the PRIMARY case on a machine with both wt and gs
	// installed: every previously created session branch is untracked and
	// most have no reachable status.json, so there is no parent to track
	// them under and afk refuses to guess one.
	//
	// Unlike the informational "Preflight WARNING" step above, this step has
	// a run, which writes the same sentence to stderr with the lowercase
	// `warning: ` prefix Plan.Run uses for bestEffort failures. Step
	// descriptions go to STDOUT, and the dashboard surfaces only
	// `warning:`-prefixed STDERR lines from a create that succeeded — with a
	// nil run this case would present there as a clean success.
	if p.GitSpice && p.BaseTrack == baseTrackWarn {
		createdBy := "with plain git"
		if p.UseWorktrunk {
			createdBy = "by worktrunk"
		}
		warning := fmt.Sprintf("base %q is not git-spice-tracked (%s) — %q will be created %s and left untracked", base, p.BaseTrackReason, name, createdBy)
		steps = append(steps, Step{
			Desc: "WARNING: " + warning,
			run: func() error {
				fmt.Fprintf(p.stderr, "warning: %s\n", warning)
				return nil
			},
		})
	}

	// Worktree + branch creation (mode-dependent), with rollback. The
	// description is fixed at Build time (--dry-run prints it verbatim), so
	// the git-spice arm words all four base-tracking cells truthfully rather
	// than promising a tracked branch it may have to degrade away from.
	var createDesc string
	switch {
	case p.UseWorktrunk:
		createDesc = fmt.Sprintf("Create worktree + branch %q off %s via worktrunk (wt -y switch --create --base %s)", name, base, base)
	case p.GitSpice && p.BaseTracked:
		createDesc = fmt.Sprintf("Create worktree at %s (detached at %s) + tracked git-spice branch %q", p.WorktreeTarget, base, name)
	case p.GitSpice && p.BaseTrack == baseTrackExplicit:
		createDesc = fmt.Sprintf("Create worktree at %s + tracked git-spice branch %q if tracking the base succeeds, else a plain git branch", p.WorktreeTarget, name)
	case p.GitSpice && p.BaseTrack == baseTrackWarn:
		createDesc = fmt.Sprintf("Create worktree at %s + plain-git branch %q off %s (base untracked)", p.WorktreeTarget, name, base)
	case p.GitSpice:
		// baseTrackNone with an untracked base: --base named something that
		// is not a local branch (origin/main, a tag, a SHA). git-spice
		// cannot track such a ref at all, so this is not a degradation to
		// warn about — today the same input fails hard with `gs branch
		// create --target origin/main: branch not tracked`.
		createDesc = fmt.Sprintf("Create worktree at %s + plain-git branch %q off %s (base is not a local branch; git-spice cannot track it)", p.WorktreeTarget, name, base)
	default:
		createDesc = fmt.Sprintf("Create worktree at %s + plain-git branch %q off %s", p.WorktreeTarget, name, base)
	}
	steps = append(steps, Step{
		Desc: createDesc,
		run: func() error {
			switch {
			case p.UseWorktrunk:
				// -y skips worktrunk's approval prompts: `afk new` invokes wt
				// as a non-interactive subprocess, where a blocking prompt would
				// hang session creation. --base carries the resolved base branch
				// (from --base, the git-spice picker, or trunk) so behavior (2)
				// is honored on the worktrunk path too; without it wt silently
				// branches off its own default, contradicting the seeded
				// base_branch. The validated name is the sole positional operand.
				wtArgs := []string{"-y", "switch", "--create"}
				if base != "" {
					wtArgs = append(wtArgs, "--base", base)
				}
				wtArgs = append(wtArgs, name)
				if _, err := d.Run(repo, "wt", wtArgs...); err != nil {
					return err
				}
				st.createdWT = true
				st.createdBranch = true
			case p.GitSpice && st.baseTracked:
				if _, err := d.Run(repo, "git", "worktree", "add", "--detach", p.WorktreeTarget, base); err != nil {
					return err
				}
				st.createdWT = true
				st.worktree = p.WorktreeTarget
				if err := d.CreateSpiceBranch(spice.BranchCreateOptions{Dir: p.WorktreeTarget, Name: name, Base: base}); err != nil {
					return err
				}
				st.createdBranch = true
			case p.GitSpice:
				// Degrade to the plain-git creation below: gs refuses
				// `branch create --target <untracked>`, so an untracked base
				// (never tracked, or a track attempt that just failed) must
				// not reach CreateSpiceBranch. Identical commands and
				// execState bookkeeping to the default arm, so rollback is
				// unchanged.
				if _, err := d.Run(repo, "git", "worktree", "add", "-b", name, p.WorktreeTarget, base); err != nil {
					return err
				}
				st.createdWT = true
				st.createdBranch = true
				st.worktree = p.WorktreeTarget
				// Only baseTrackExplicit is a surprise worth a warning: the
				// base-track step above was expected to succeed and did not.
				// baseTrackWarn already printed its own warning, and
				// baseTrackNone (a non-local --base) is a normal outcome.
				if p.BaseTrack == baseTrackExplicit {
					fmt.Fprintf(p.stderr, "warning: base %q is not git-spice-tracked; created %q as a plain git branch\n", base, name)
				}
			default:
				if _, err := d.Run(repo, "git", "worktree", "add", "-b", name, p.WorktreeTarget, base); err != nil {
					return err
				}
				st.createdWT = true
				st.createdBranch = true
				st.worktree = p.WorktreeTarget
			}
			if st.worktree == "" {
				wp, err := resolveWorktreePath(d, repo, name)
				if err != nil {
					return fmt.Errorf("resolving worktree path: %w", err)
				}
				st.worktree = wp
			}
			return nil
		},
		undo: func() error {
			wt := st.worktree
			if wt == "" {
				wt = p.WorktreeTarget
			}
			if st.createdWT && wt != "" {
				if _, err := d.Run(repo, "git", "worktree", "remove", "--force", wt); err != nil {
					fmt.Fprintf(p.stderr, "rollback: could not remove worktree %s: %v\n", wt, err)
				}
			}
			if st.createdBranch {
				// Delete the just-created branch only if it has no commits
				// beyond its base — never destroy work.
				if n, err := commitsBeyondBase(d, repo, base, name); err == nil && n == 0 {
					if _, derr := d.Run(repo, "git", "branch", "-D", name); derr != nil {
						fmt.Fprintf(p.stderr, "rollback: could not delete branch %q: %v\n", name, derr)
					}
				} else {
					fmt.Fprintf(p.stderr, "rollback: leaving branch %q in place (has commits beyond %s, or the check failed)\n", name, base)
				}
			}
			return nil
		},
	})

	// Close the drift at its source: worktrunk creates a plain, never-tracked
	// git branch, so in a git-spice repo afk tracks it right after. Without
	// this, every session branch is permanently invisible to the base picker
	// (the bug this plan fixes).
	//
	// Inserted immediately AFTER the creation step and BEFORE the seed step,
	// not appended at the end: Plan.Run rolls back only steps that already
	// completed, so a step added last would never have its undo run.
	//
	// Only when the base is tracked or is about to be: for baseTrackWarn the
	// WARNING step already said the branch is left untracked, and for
	// baseTrackNone (`--base origin/main`) there is nothing wrong to report.
	if p.UseWorktrunk && p.GitSpice && (p.BaseTracked || p.BaseTrack == baseTrackExplicit) {
		steps = append(steps, Step{
			Desc:       fmt.Sprintf("Track branch %q with git-spice (base %q)", name, base),
			bestEffort: true,
			run: func() error {
				// Reachable only when the base-track step failed at run
				// time. `gs branch track --base <untracked>` would fail with
				// "branch not tracked" anyway; returning the error routes it
				// through bestEffort's warning instead of attempting it.
				if !st.baseTracked {
					return fmt.Errorf("base %q is not tracked; skipping", base)
				}
				if err := d.TrackSpiceBranch(spice.BranchTrackOptions{Dir: repo, Name: name, Base: base}); err != nil {
					return err
				}
				st.trackedNew = true
				return nil
			},
			undo: func() error {
				if !st.trackedNew {
					return nil
				}
				if err := d.UntrackSpiceBranch(repo, name); err != nil {
					fmt.Fprintf(p.stderr, "rollback: could not untrack branch %q: %v\n", name, err)
				}
				return nil
			},
		})
	}

	// Seed status.json BEFORE launching the conductor (schema §3: the file
	// exists before the Claude session does).
	steps = append(steps, Step{
		Desc: fmt.Sprintf("Seed .afk/%s/status.json (phase planning, base %s)", name, base),
		run: func() error {
			return seedStatus(st.worktree, cfg, base, "", d.Now)
		},
	})

	// The marker is its own step, not folded into seedStatus, so the seed
	// step's Desc and status.json fields stay as the schema contract has them
	// and --dry-run lists the write on its own line. It runs after the seed
	// (the session dir exists) and before the host (the conductor reads the
	// marker at start). No undo: rollback removes the worktree and the marker
	// with it, same as the seed step.
	if cfg.Autonomous {
		steps = append(steps, Step{
			Desc: fmt.Sprintf("Write autonomous marker .afk/%s/autonomous", name),
			run: func() error {
				return WriteAutonomousMarker(statusDir(st.worktree, name))
			},
		})
	}

	if cfg.Provider != "" {
		steps = append(steps, providerMarkerStep(name, cfg.Provider, func() string { return statusDir(st.worktree, name) }))
	}

	if cfg.Unattended {
		steps = append(steps, unattendedMarkerStep(name, func() string { return statusDir(st.worktree, name) }))
	}

	// Launch the detached host that owns the session's PTY. Unconditional:
	// the host is the terminal multiplexer now, so nothing here depends on
	// this process staying alive. `afk dash` and `afk attach` connect to
	// it afterwards.
	spawn := HostSpawn{Name: name, Autonomous: cfg.Autonomous, Provider: cfg.Provider, Unattended: cfg.Unattended}
	steps = append(steps, Step{
		Desc: "Launch detached session host running: " + childCommandLine(spawn) + hostProviderNote(spawn),
		run: func() error {
			spawn.Worktree = st.worktree
			rt, err := RuntimeFor(spawn.Worktree, spawn.Name)
			if err != nil {
				return err
			}
			h, err := rt.Spawn(spawn, d)
			if err != nil {
				return err
			}
			st.host = h
			st.runtime = rt
			return nil
		},
		// Rollback: a session whose worktree and branch are about to be
		// destroyed must not leave a host (and its agent CLI) running behind it.
		// Launch is currently the last step, so this fires only if a later
		// step is ever appended — it exists so that ordering change is safe.
		undo: p.hostUndo(),
	})

	p.Steps = steps
}

// providerMarkerStep writes the .afk/<name>/provider sidecar. It is its own
// step, like the autonomous marker, so --dry-run lists the write. It runs
// after the session dir exists and before the host launch: a dashboard
// relaunch reads the sidecar, and the first spawn must already agree with
// it. Only a non-default provider gets one, so Claude sessions leave no new
// file behind. No undo: rollback removes the session dir and the sidecar
// with it. dir is a func because the worktree path is only known at run
// time.
func providerMarkerStep(name, providerName string, dir func() string) Step {
	return Step{
		Desc: fmt.Sprintf("Write provider marker .afk/%s/provider (%s)", name, providerName),
		run: func() error {
			return WriteProviderMarker(dir(), providerName)
		},
	}
}

// unattendedMarkerStep writes the .afk/<name>/unattended marker. Its own
// step, like the autonomous and provider markers, so --dry-run lists the
// write. It runs after the session dir exists and before the host launch: the
// first spawn sets the mode itself, but every later one (a dashboard
// relaunch, a child restart) has only this file to go by, so it must be on
// disk before a host exists that could be restarted. No undo: rollback
// removes the session dir and the marker with it.
func unattendedMarkerStep(name string, dir func() string) Step {
	return Step{
		Desc: fmt.Sprintf("Write unattended marker .afk/%s/unattended", name),
		run: func() error {
			return WriteUnattendedMarker(dir())
		},
	}
}

// hostUndo is the launch step's rollback, shared by both creation modes: a
// session whose on-disk footprint is about to be removed must not leave a
// host (and its agent CLI) running behind it.
func (p *Plan) hostUndo() func() error {
	return func() error {
		if p.st.host.PID == 0 {
			return nil
		}
		rt := p.st.runtime
		if rt == nil {
			rt = afkRuntime{}
		}
		if err := rt.Undo(p.st.host, p.deps); err != nil {
			fmt.Fprintf(p.stderr, "rollback: %v (see %s)\n", err, p.st.host.LogPath)
		}
		return nil
	}
}

// assembleScratchSteps builds the one-off plan: create .afk/<name>/ in the
// repo root, then launch a host whose child is the bare provider CLI. There is no
// branch step, no worktree step and no status.json step — a one-off has no
// conductor, so it has no phase, and seeding one would put a fabricated value
// into a schema this repo does not own. It is found on disk through the
// host.json the host writes into that directory instead.
//
// Descriptions here are worded to avoid naming resources the mode never
// creates, because `--dry-run` prints them verbatim.
func (p *Plan) assembleScratchSteps(leadDesc string) {
	cfg := p.Config
	d := p.deps
	repo := cfg.RepoDir
	name := cfg.Name
	st := p.st
	dir := statusDir(repo, name)

	createDesc := fmt.Sprintf("Create %s for the session host's files", dir)
	if strings.TrimSpace(cfg.Description) != "" {
		createDesc += " + description.md"
	}

	steps := []Step{
		{Desc: leadDesc},
		{
			Desc: createDesc,
			run: func() error {
				// .afk/ itself is shared ground every session's directory
				// sits in, so creating it non-exclusively is fine. The leaf
				// must be exclusive (os.Mkdir, not MkdirAll) because
				// createdDir is the undo's ownership test: under a concurrent
				// same-name create, MkdirAll would let the loser "succeed" on
				// the winner's directory, mark it as its own, and roll back
				// over the winner's host.json — a one-off's only on-disk
				// trace — orphaning that host.
				if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
					return fmt.Errorf("creating %s: %w", filepath.Dir(dir), err)
				}
				if err := os.Mkdir(dir, 0o755); err != nil {
					return fmt.Errorf("creating %s: %w", dir, err)
				}
				st.createdDir = true
				st.worktree = repo
				return writeDescriptionSidecar(dir, cfg.Description)
			},
			undo: func() error {
				// Only ever the directory this invocation created (guaranteed
				// by the exclusive os.Mkdir above), and never anything above
				// it: rollback for a one-off must be incapable of reaching a
				// checkout.
				if !st.createdDir {
					return nil
				}
				if err := os.RemoveAll(dir); err != nil {
					fmt.Fprintf(p.stderr, "rollback: could not remove %s: %v\n", dir, err)
				}
				return nil
			},
		},
	}

	if cfg.Provider != "" {
		steps = append(steps, providerMarkerStep(name, cfg.Provider, func() string { return dir }))
	}

	// Unlike Autonomous, which a one-off ignores because it has no conductor,
	// the unattended mode is the CLI's own and applies to a bare one-off too.
	if cfg.Unattended {
		steps = append(steps, unattendedMarkerStep(name, func() string { return dir }))
	}

	spawn := HostSpawn{Name: name, Worktree: repo, Scratch: true, Provider: cfg.Provider, Unattended: cfg.Unattended}
	steps = append(steps, Step{
		Desc: fmt.Sprintf("Launch detached session host running: %s (one-off, in %s)%s", childCommandLine(spawn), repo, hostProviderNote(spawn)),
		run: func() error {
			rt, err := RuntimeFor(spawn.Worktree, spawn.Name)
			if err != nil {
				return err
			}
			h, err := rt.Spawn(spawn, d)
			if err != nil {
				return err
			}
			st.host = h
			st.runtime = rt
			return nil
		},
		undo: p.hostUndo(),
	})

	p.Steps = steps
}

// Run executes the plan. In dry-run it prints the full action list and makes
// no changes. Otherwise it runs each mutating step, rolling back this
// invocation's resources (in reverse) on the first non-best-effort failure.
func (p *Plan) Run() error {
	if p.Config.DryRun {
		fmt.Fprintf(p.stdout, "afk new %q — dry run (no changes will be made)\n", p.Config.Name)
		fmt.Fprintf(p.stdout, "  repo:        %s\n", p.Config.RepoDir)
		fmt.Fprintf(p.stdout, "  git-spice:   %v\n", p.GitSpice)
		fmt.Fprintf(p.stdout, "  worktrunk:   %v\n", p.UseWorktrunk)
		fmt.Fprintf(p.stdout, "Actions:\n")
		for i, s := range p.Steps {
			fmt.Fprintf(p.stdout, "  %s %s\n", p.styles.number.Render(fmt.Sprintf("%d.", i+1)), s.Desc)
		}
		return nil
	}

	// Seed the run-time tracking state from the Build-time decision before
	// the first step runs: the base-track step only ever promotes it to
	// true, and the creation step branches on it.
	p.st.baseTracked = p.BaseTracked

	var done []Step
	for _, s := range p.Steps {
		fmt.Fprintf(p.stdout, "%s %s\n", p.styles.arrow.Render("→"), s.Desc)
		if s.run == nil {
			continue
		}
		if err := s.run(); err != nil {
			if s.bestEffort {
				fmt.Fprintf(p.stderr, "warning: %s: %v\n", s.Desc, err)
				continue
			}
			fmt.Fprintf(p.stderr, "error: %s: %v\n", s.Desc, err)
			p.rollback(done)
			return err
		}
		done = append(done, s)
	}
	fmt.Fprintf(p.stdout, "\nSession %q ready.\n", p.Config.Name)
	return nil
}

// rollback undoes completed steps in reverse order. Only resources created by
// this invocation are touched (tracked in execState).
func (p *Plan) rollback(done []Step) {
	if len(done) == 0 {
		return
	}
	fmt.Fprintf(p.stderr, "rolling back partial session creation...\n")
	for i := len(done) - 1; i >= 0; i-- {
		if done[i].undo != nil {
			_ = done[i].undo()
		}
	}
}

// --- git helpers --------------------------------------------------------

// branchExists reports whether refs/heads/<name> resolves in repo.
func branchExists(d Deps, repo, name string) bool {
	_, err := d.Run(repo, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// commitsBeyondBase returns the number of commits on name that are not on
// base (git rev-list --count base..name).
func commitsBeyondBase(d Deps, repo, base, name string) (int, error) {
	out, err := d.Run(repo, "git", "rev-list", "--count", base+".."+name)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parsing rev-list count %q: %w", strings.TrimSpace(out), err)
	}
	return n, nil
}

// resolveWorktreePath finds the worktree whose checked-out branch is name,
// parsing `git worktree list --porcelain`. This is tool-agnostic: it works
// whether the worktree was created by wt or by git.
func resolveWorktreePath(d Deps, repo, name string) (string, error) {
	out, err := d.Run(repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	want := "refs/heads/" + name
	var current string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch "):
			if strings.TrimSpace(strings.TrimPrefix(line, "branch ")) == want && current != "" {
				return current, nil
			}
		}
	}
	return "", fmt.Errorf("no worktree found for branch %q", name)
}

// trunkPlain determines the trunk branch for a plain-git repo: the
// origin/HEAD symbolic ref, else main, else master, else the current branch.
func trunkPlain(d Deps, repo string) (string, error) {
	if out, err := d.Run(repo, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			return strings.TrimPrefix(s, "origin/"), nil
		}
	}
	for _, cand := range []string{"main", "master"} {
		if _, err := d.Run(repo, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+cand); err == nil {
			return cand, nil
		}
	}
	if out, err := d.Run(repo, "git", "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if s := strings.TrimSpace(out); s != "" && s != "HEAD" {
			return s, nil
		}
	}
	return "", fmt.Errorf("could not determine a trunk branch in %s", repo)
}

// TrunkOf picks the trunk from a git-spice stack: the branch with no parent
// (Down == nil). Falls back to a main/master name match, then the first
// branch, then "main".
//
// Exported so internal/ui can compute the same trunk-first base ordering the
// CLI uses rather than duplicating the logic and drifting.
func TrunkOf(stack []spice.Branch) string {
	for _, b := range stack {
		if b.Down == nil {
			return b.Name
		}
	}
	for _, b := range stack {
		if b.Name == "main" || b.Name == "master" {
			return b.Name
		}
	}
	if len(stack) > 0 {
		return stack[0].Name
	}
	return "main"
}

// BranchOptions lists the picker options: trunk first, then the remaining
// branch names in stack order.
//
// Exported so internal/ui can offer the identical base-picker ordering the CLI
// uses rather than duplicating the logic and drifting.
func BranchOptions(stack []spice.Branch, trunk string) []string {
	opts := []string{trunk}
	for _, b := range stack {
		if b.Name != trunk {
			opts = append(opts, b.Name)
		}
	}
	return opts
}

// OrderTrunkFirst reorders names to place trunk first, preserving input order
// for the rest and omitting trunk duplicates. Exported because both resolveBase
// and internal/ui need it: the dashboard form must offer the same ordering the
// CLI computes, and inlining this in each caller is exactly how the two entry
// points originally diverged.
func OrderTrunkFirst(names []string, trunk string) []string {
	var out []string
	// An empty trunk is "not resolved yet", not a branch: prepending it would
	// hand callers a blank first option to render and preselect.
	if trunk != "" {
		out = append(out, trunk)
	}
	for _, n := range names {
		if n != trunk {
			out = append(out, n)
		}
	}
	return out
}

// MergeBaseOptions returns the union of git-spice-tracked branches and local
// branches for the base picker, in the order: trunk first (when non-empty),
// then tracked branches in stack order (skipping trunk), then local branches
// not already present, in local's order (git's for-each-ref order). With a
// non-empty trunk and empty local, it returns what BranchOptions returns; with
// an empty stack and non-empty local, it returns what OrderTrunkFirst returns.
// An empty trunk never produces a blank option even if local contains names,
// preserving the empty-trunk precedence rule from OrderTrunkFirst — the function
// exists so the CLI and dashboard collapse to a single implementation and never
// drift.
func MergeBaseOptions(stack []spice.Branch, trunk string, local []string) []string {
	var out []string
	// Trunk first (when non-empty).
	if trunk != "" {
		out = append(out, trunk)
	}

	// Track which names have been seen to eliminate duplicates.
	seen := make(map[string]bool)
	if trunk != "" {
		seen[trunk] = true
	}

	// Then tracked branches from stack in order, skipping trunk.
	for _, b := range stack {
		if b.Name != trunk && !seen[b.Name] {
			out = append(out, b.Name)
			seen[b.Name] = true
		}
	}

	// Then local branches not already present, in local's order.
	for _, n := range local {
		if !seen[n] {
			out = append(out, n)
			seen[n] = true
		}
	}

	return out
}

// TrunkPlain resolves the trunk branch of a plain-git repo, delegating to the
// same helper resolveBase uses. Exported for the same reason as BranchOptions:
// internal/ui must not guess a trunk. Hardcoding "main" there produces a
// phantom option in a master-trunk repo and preselects a branch that does not
// exist, which is precisely the entry-point drift these seams exist to close.
func TrunkPlain(d Deps, repo string) (string, error) {
	return trunkPlain(d.withDefaults(), repo)
}

// BranchesPlain lists local branch names from a plain-git repo, in git's order.
// Exported so internal/ui can populate a base picker with the same branches
// resolveBase would offer.
func BranchesPlain(d Deps, repo string) ([]string, error) {
	d = d.withDefaults()
	out, err := d.Run(repo, "git", "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			branches = append(branches, trimmed)
		}
	}
	return branches, nil
}
