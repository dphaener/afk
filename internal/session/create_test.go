package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/spice"
)

// --- test doubles -------------------------------------------------------

// recordedCmd captures one d.Run invocation for later assertions.
type recordedCmd struct {
	dir  string
	name string
	args []string
}

// fakeRunner records every command and delegates behavior to a handler.
// A nil handler makes every command a silent success returning "".
type fakeRunner struct {
	calls   []recordedCmd
	handler func(dir, name string, args []string) (string, error)
}

func (f *fakeRunner) run(dir, name string, args ...string) (string, error) {
	f.calls = append(f.calls, recordedCmd{dir: dir, name: name, args: append([]string(nil), args...)})
	if f.handler != nil {
		return f.handler(dir, name, args)
	}
	return "", nil
}

// sawCommand reports whether a command whose name matches and whose joined
// args contain sub was recorded.
func (f *fakeRunner) sawCommand(name, sub string) bool {
	for _, c := range f.calls {
		if c.name == name && strings.Contains(strings.Join(c.args, " "), sub) {
			return true
		}
	}
	return false
}

// onlyWorktreeListCalls asserts every command fr recorded is the read-only
// `git worktree list --porcelain` of scratch Build's cross-worktree collision
// check — the ONE command a one-off path is allowed. It replaces the old
// zero-commands assertions: the intent (no probes, no repository mutation)
// is unchanged, but "zero" pinned the repo-root-only collision check that let
// a scratch session shadow a planned session's name.
func onlyWorktreeListCalls(t *testing.T, fr *fakeRunner) {
	t.Helper()
	for _, c := range fr.calls {
		if c.name == "git" && strings.Join(c.args, " ") == "worktree list --porcelain" {
			continue
		}
		t.Errorf("scratch path ran a command beyond the collision enumeration: %s %v", c.name, c.args)
	}
}

// fixedNow returns a deterministic UTC clock for reproducible updated_at.
func fixedNow() func() time.Time {
	t := time.Date(2026, 7, 9, 21, 14, 3, 0, time.UTC)
	return func() time.Time { return t }
}

const fixedNowRFC = "2026-07-09T21:14:03Z"

// plainRepoRunner returns a handler that emulates a plain-git repo where the
// requested branch does not exist and trunk resolves to "main". When a
// worktree is added, the target directory is created on disk so downstream
// seeding can write into it.
func plainRepoRunner(t *testing.T) func(dir, name string, args []string) (string, error) {
	t.Helper()
	return func(dir, name string, args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "git" && strings.HasPrefix(joined, "rev-parse --verify --quiet refs/heads/main"):
			return "abc123\n", nil // main exists (trunk)
		case name == "git" && strings.HasPrefix(joined, "rev-parse --verify --quiet refs/heads/"):
			return "", &exitError{} // any other branch does not exist
		case name == "git" && strings.HasPrefix(joined, "symbolic-ref"):
			return "", &exitError{} // no origin/HEAD
		case name == "git" && args[0] == "worktree" && args[1] == "add":
			// Create the worktree directory so seedStatus can write into it.
			target := worktreePathArg(args)
			if target != "" {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return "", err
				}
			}
			return "", nil
		case name == "git" && strings.HasPrefix(joined, "rev-list --count"):
			return "0\n", nil // no commits beyond base
		default:
			return "", nil
		}
	}
}

// worktreePathArg extracts the worktree directory argument from a
// `git worktree add` argv (the arg containing the .claude/worktrees root).
func worktreePathArg(args []string) string {
	needle := filepath.Join(".claude", "worktrees")
	for _, a := range args {
		if strings.Contains(a, needle) {
			return a
		}
	}
	return ""
}

// exitError is a stand-in non-nil error for commands that "fail" (e.g. a
// branch that does not resolve).
type exitError struct{}

func (*exitError) Error() string { return "exit status 1" }

// harmlessExecutable is a Deps.Executable stand-in resolving to `true`: the
// launch step really forks it, so Setsid and the /dev/null stdio wiring are
// exercised against the real syscalls, but the process ignores the `_host`
// argv it is handed and exits immediately. LookPath's error is surfaced
// through the seam, so a platform without `true` fails loudly rather than
// silently spawning something else.
func harmlessExecutable() func() (string, error) {
	return func() (string, error) { return exec.LookPath("true") }
}

// shortTmpDir points os.TempDir() at a fresh short path under /tmp for the
// duration of the test. host.SocketPath refuses socket paths over 100 bytes,
// and both macOS's default per-user TMPDIR and t.TempDir()'s deep paths can
// push it past that (see internal/host's own shortSocketDir helper).
func shortTmpDir(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fgs")
	if err != nil {
		t.Skipf("cannot create a short temp dir under /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
}

// fakeHostCtl records the rollback path's attempts to stop a host — the
// socket Exit dials and the signals — and can make either fail.
type fakeHostCtl struct {
	dialed  []string
	signals []struct {
		pid int
		sig syscall.Signal
	}
	dialErr error
	sigErr  error
}

func (f *fakeHostCtl) dialExit(socket string, _ time.Duration) error {
	f.dialed = append(f.dialed, socket)
	return f.dialErr
}

func (f *fakeHostCtl) signalKill(pid int, sig syscall.Signal) error {
	f.signals = append(f.signals, struct {
		pid int
		sig syscall.Signal
	}{pid, sig})
	return f.sigErr
}

// baseDeps returns a Deps wired to hermetic fakes: no git-spice, no
// worktrunk, preflight OK, a runner from plainRepoRunner, and a harmless
// stand-in for the afk binary. PickBase is left nil (non-interactive).
// Callers override fields as needed.
func baseDeps(t *testing.T, fr *fakeRunner) Deps {
	t.Helper()
	fr.handler = plainRepoRunner(t)
	return Deps{
		Executable:     harmlessExecutable(),
		DetectGitSpice: func(string) bool { return false },
		Stack:          func(string) ([]spice.Branch, error) { return nil, nil },
		HasWorktrunk:   func() bool { return false },
		AfkPreflight:   func(string) (bool, string) { return true, "" },
		CreateSpiceBranch: func(spice.BranchCreateOptions) error {
			t.Fatalf("CreateSpiceBranch must not be called on the plain-git path")
			return nil
		},
		TrackSpiceBranch: func(spice.BranchTrackOptions) error {
			t.Fatalf("TrackSpiceBranch must not be called: the plain-git path never tracks anything")
			return nil
		},
		UntrackSpiceBranch: func(string, string) error {
			t.Fatalf("UntrackSpiceBranch must not be called: the plain-git path never tracks anything")
			return nil
		},
		Run:    fr.run,
		Now:    fixedNow(),
		Stdout: &strings.Builder{},
		Stderr: &strings.Builder{},
	}
}

// --- ValidateName -------------------------------------------------------

func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "billing", false},
		{"kebab", "add-billing-webhook", false},
		{"ticket-prefix", "turt-1234-add-billing", false},
		{"digits-only", "12345", false},
		{"leading-digit", "1-feature", false},
		{"single-char", "a", false},
		{"empty", "", true},
		{"leading-hyphen", "-billing", true},
		{"trailing-hyphen-ok", "billing-", false}, // trailing hyphen is allowed by the contract
		{"uppercase", "Billing", true},
		{"underscore", "add_billing", true},
		{"space", "add billing", true},
		{"slash", "feat/billing", true},
		{"dot", "billing.v2", true},
		{"semicolon-injection", "billing;rm -rf", true},
		{"dollar-injection", "billing$(whoami)", true},
		{"backtick-injection", "billing`id`", true},
		{"unicode", "billing-café", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName(tc.input)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateName(%q) = nil, want error", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateName(%q) = %v, want nil", tc.input, err)
			}
		})
	}
}

func TestBuildRejectsInvalidName(t *testing.T) {
	fr := &fakeRunner{}
	_, err := Build(Config{Name: "Bad Name", RepoDir: t.TempDir()}, baseDeps(t, fr))
	if err == nil {
		t.Fatal("Build accepted an invalid name")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("Build ran %d commands before rejecting an invalid name; want 0", len(fr.calls))
	}
}

// --- seedStatus byte contract ------------------------------------------

func TestSeedStatusMatchesSchema(t *testing.T) {
	worktree := t.TempDir()
	cfg := Config{Name: "add-billing-webhook", Description: ""}
	if err := seedStatus(worktree, cfg, "main", "", fixedNow()); err != nil {
		t.Fatalf("seedStatus: %v", err)
	}

	path := filepath.Join(worktree, ".afk", "add-billing-webhook", "status.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading seeded status: %v", err)
	}

	// Assert exactly the nine schema keys, no more, no fewer.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("status.json is not valid JSON: %v", err)
	}
	wantKeys := []string{
		"session_id", "plan_name", "phase", "branch", "base_branch",
		"loop_count", "awaiting_input", "tmux_window", "updated_at",
	}
	if len(generic) != len(wantKeys) {
		t.Fatalf("status.json has %d keys, want %d: %v", len(generic), len(wantKeys), generic)
	}
	for _, k := range wantKeys {
		if _, ok := generic[k]; !ok {
			t.Errorf("status.json missing required key %q", k)
		}
	}

	// Assert the exact seed values.
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("decoding Status: %v", err)
	}
	want := Status{
		SessionID:     "",
		PlanName:      "add-billing-webhook",
		Phase:         "planning",
		Branch:        "add-billing-webhook",
		BaseBranch:    "main",
		LoopCount:     0,
		AwaitingInput: false,
		TmuxWindow:    "",
		UpdatedAt:     fixedNowRFC,
	}
	if s != want {
		t.Errorf("seeded status = %+v, want %+v", s, want)
	}

	// updated_at must be RFC3339 UTC (Z suffix).
	if !strings.HasSuffix(s.UpdatedAt, "Z") {
		t.Errorf("updated_at %q is not UTC (no Z suffix)", s.UpdatedAt)
	}
	if _, err := time.Parse(time.RFC3339, s.UpdatedAt); err != nil {
		t.Errorf("updated_at %q is not RFC3339: %v", s.UpdatedAt, err)
	}
}

func TestSeedStatusWritesDescriptionSidecar(t *testing.T) {
	worktree := t.TempDir()
	cfg := Config{Name: "billing", Description: "Add Stripe billing; drop legacy $PATH stuff"}
	if err := seedStatus(worktree, cfg, "main", "", fixedNow()); err != nil {
		t.Fatalf("seedStatus: %v", err)
	}
	descPath := filepath.Join(worktree, ".afk", "billing", "description.md")
	got, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatalf("reading description sidecar: %v", err)
	}
	if string(got) != cfg.Description+"\n" {
		t.Errorf("description sidecar = %q, want %q", string(got), cfg.Description+"\n")
	}
	// The status.json must not contain the free-text description anywhere.
	data, _ := os.ReadFile(filepath.Join(worktree, ".afk", "billing", "status.json"))
	if strings.Contains(string(data), "Stripe") {
		t.Errorf("free-text description leaked into status.json: %s", data)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".afk", "billing", "title.txt")); !os.IsNotExist(err) {
		t.Errorf("title.txt exists but should not (err=%v)", err)
	}
}

// TestNewOutputStylesStripsANSIForNonTTYWriter guards the lipgloss v1->v2
// renderer migration (decisions.md finding (f)): lipgloss v2 deleted
// Renderer/NewRenderer, so Style.Render() always emits full ANSI regardless
// of destination. newOutputStyles must still make color profiling follow the
// io.Writer it's given rather than the process's real os.Stdout — the only
// thing standing between a real TTY getting color and an in-memory buffer
// getting corrupted with escape codes is writing through the io.Writer
// newOutputStyles returns instead of the raw one passed in.
func TestNewOutputStylesStripsANSIForNonTTYWriter(t *testing.T) {
	var buf bytes.Buffer
	styles, out := newOutputStyles(&buf)

	fmt.Fprintf(out, "%s %s\n", styles.arrow.Render("→"), "some step")
	fmt.Fprintf(out, "  %s %s\n", styles.number.Render("1."), "some action")

	got := buf.String()
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("output written through newOutputStyles's writer contains an ANSI escape sequence for a non-TTY *bytes.Buffer:\n%q", got)
	}
	for _, want := range []string{"→", "some step", "1.", "some action"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}

	// Confirms the raw, unwrapped Style.Render output DOES contain ANSI —
	// otherwise the strip assertion above would pass vacuously if the styles
	// never colored anything to begin with.
	if raw := styles.arrow.Render("→"); !strings.ContainsRune(raw, '\x1b') {
		t.Fatalf("styles.arrow.Render produced no ANSI at all; the strip assertion above would be meaningless: %q", raw)
	}
}

// --- dry-run is provably side-effect-free ------------------------------

func TestDryRunNoSideEffects(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	var out strings.Builder
	d.Stdout = &out
	// A picker that fails the test if the dry-run ever invokes it.
	d.PickBase = func(string, []string, string) (string, error) {
		t.Fatal("PickBase must not be called during dry-run")
		return "", nil
	}
	// Resolving the afk binary is the first thing the launch step does, so
	// this is the tripwire for a dry-run that reaches it.
	d.Executable = func() (string, error) {
		t.Fatal("Executable must not be consulted during dry-run")
		return "", nil
	}

	cfg := Config{Name: "add-billing-webhook", RepoDir: repo, DryRun: true}
	if err := Create(cfg, d); err != nil {
		t.Fatalf("dry-run Create: %v", err)
	}

	// No mutating command may have been issued.
	if fr.sawCommand("git", "worktree add") {
		t.Error("dry-run issued `git worktree add`")
	}
	if fr.sawCommand("git", "branch") {
		t.Error("dry-run issued a `git branch` mutation")
	}
	if fr.sawCommand("wt", "switch") {
		t.Error("dry-run issued `wt switch`")
	}

	// Nothing may have been written to disk.
	if _, err := os.Stat(filepath.Join(repo, ".claude")); !os.IsNotExist(err) {
		t.Errorf(".claude was created during dry-run (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".afk")); !os.IsNotExist(err) {
		t.Errorf(".afk was created during dry-run (err=%v)", err)
	}

	// The full action list must be printed.
	printed := out.String()
	for _, want := range []string{
		"dry run",
		"Actions:",
		"Base branch:",
		"Preflight",
		"worktree",
		"status.json",
		"/afk:go --session add-billing-webhook",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("dry-run output missing %q\n---\n%s", want, printed)
		}
	}
}

func TestDryRunGitSpiceShowsPicker(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	d.DetectGitSpice = func(string) bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) {
		return []spice.Branch{
			{Name: "main"},
			{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
			{Name: "feat-b", Down: &spice.BranchRef{Name: "feat-a"}},
		}, nil
	}
	d.PickBase = func(string, []string, string) (string, error) {
		t.Fatal("PickBase must not be called during dry-run")
		return "", nil
	}
	var out strings.Builder
	d.Stdout = &out

	cfg := Config{Name: "stacked-feature", RepoDir: repo, DryRun: true}
	if err := Create(cfg, d); err != nil {
		t.Fatalf("dry-run Create (git-spice): %v", err)
	}
	printed := out.String()
	if !strings.Contains(printed, "git-spice:   true") {
		t.Errorf("dry-run did not report git-spice mode:\n%s", printed)
	}
	// The picker must be advertised with trunk + stack branches as options.
	for _, want := range []string{"picker offers", "main", "feat-a", "feat-b"} {
		if !strings.Contains(printed, want) {
			t.Errorf("git-spice dry-run missing picker option %q\n---\n%s", want, printed)
		}
	}
}

func TestDryRunNoGitSpiceOmitsPicker(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	// gs would be detected, but --no-git-spice forces plain-git.
	d.DetectGitSpice = func(string) bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) {
		t.Fatal("Stack must not be consulted when --no-git-spice is set")
		return nil, nil
	}
	var out strings.Builder
	d.Stdout = &out

	cfg := Config{Name: "plain", RepoDir: repo, DryRun: true, NoGitSpice: true}
	if err := Create(cfg, d); err != nil {
		t.Fatalf("dry-run Create (--no-git-spice): %v", err)
	}
	printed := out.String()
	if !strings.Contains(printed, "git-spice:   false") {
		t.Errorf("--no-git-spice did not disable git-spice mode:\n%s", printed)
	}
	if strings.Contains(printed, "picker offers") {
		t.Errorf("--no-git-spice must not advertise a base picker:\n%s", printed)
	}
}

// --- base resolution ----------------------------------------------------

func TestResolveBaseExplicitFlagWins(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	d.DetectGitSpice = func(string) bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) {
		return []spice.Branch{{Name: "main"}}, nil
	}
	d.PickBase = func(string, []string, string) (string, error) {
		t.Fatal("PickBase must not be called when --base is explicit")
		return "", nil
	}
	p, err := Build(Config{Name: "x", RepoDir: repo, Base: "develop"}, d)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Base != "develop" {
		t.Errorf("Base = %q, want develop", p.Base)
	}
}

func TestResolveBasePickerInvokedWhenInteractive(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	d.DetectGitSpice = func(string) bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) {
		return []spice.Branch{
			{Name: "main"},
			{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
		}, nil
	}
	var gotOptions []string
	d.PickBase = func(prompt string, options []string, dflt string) (string, error) {
		gotOptions = options
		return "feat-a", nil
	}
	// Not a dry-run: the picker is consulted.
	p, err := Build(Config{Name: "x", RepoDir: repo}, d)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Base != "feat-a" {
		t.Errorf("Base = %q, want feat-a (picker choice)", p.Base)
	}
	if len(gotOptions) == 0 || gotOptions[0] != "main" {
		t.Errorf("picker options = %v, want trunk 'main' first", gotOptions)
	}
}

// forEachRefRunner wraps a base handler, answering
// `git for-each-ref --format=%(refname:short) refs/heads/` with out/err and
// delegating every other command to base. Lets a test override just the
// local-branch listing without losing plainRepoRunner's rev-parse/worktree
// behavior.
func forEachRefRunner(base func(dir, name string, args []string) (string, error), out string, err error) func(dir, name string, args []string) (string, error) {
	return func(dir, name string, args []string) (string, error) {
		if name == "git" && strings.Join(args, " ") == "for-each-ref --format=%(refname:short) refs/heads/" {
			return out, err
		}
		return base(dir, name, args)
	}
}

// TestResolveBaseGitSpiceOffersUntrackedLocalBranches pins the union fix:
// the git-spice arm's picker (and its dry-run description) must offer trunk,
// then the tracked stack, then every other local branch — not the tracked
// stack alone, which hides worktrunk-created (untracked) branches. Row (b)
// is load-bearing: a broken `for-each-ref` must degrade to the tracked stack
// rather than fail Build. Row (c) is load-bearing: an explicit --base must
// never spend a git call on the local-branch listing.
func TestResolveBaseGitSpiceOffersUntrackedLocalBranches(t *testing.T) {
	stack := []spice.Branch{
		{Name: "main"},
		{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
	}

	t.Run("interactive: union of stack and local branches", func(t *testing.T) {
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		fr.handler = forEachRefRunner(plainRepoRunner(t), "bug-x\nfeat-a\nmain\nzeta\n", nil)
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		var gotOptions []string
		var gotDefault string
		d.PickBase = func(prompt string, options []string, dflt string) (string, error) {
			gotOptions = options
			gotDefault = dflt
			return "main", nil
		}
		if _, err := Build(Config{Name: "x", RepoDir: repo}, d); err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := []string{"main", "feat-a", "bug-x", "zeta"}
		if !reflect.DeepEqual(gotOptions, want) {
			t.Errorf("picker options = %v, want %v", gotOptions, want)
		}
		if gotDefault != "main" {
			t.Errorf("picker default = %q, want main", gotDefault)
		}
	})

	t.Run("for-each-ref error degrades to the tracked stack", func(t *testing.T) {
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		fr.handler = forEachRefRunner(plainRepoRunner(t), "", errors.New("fatal: not a git repository"))
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		var gotOptions []string
		d.PickBase = func(prompt string, options []string, dflt string) (string, error) {
			gotOptions = options
			return "main", nil
		}
		if _, err := Build(Config{Name: "x", RepoDir: repo}, d); err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := []string{"main", "feat-a"}
		if !reflect.DeepEqual(gotOptions, want) {
			t.Errorf("picker options = %v, want %v (degraded to tracked stack)", gotOptions, want)
		}
	})

	t.Run("explicit --base skips the picker and the for-each-ref call", func(t *testing.T) {
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		fr.handler = forEachRefRunner(plainRepoRunner(t), "bug-x\nfeat-a\nmain\nzeta\n", nil)
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		d.PickBase = func(string, []string, string) (string, error) {
			t.Fatal("PickBase must not be called when --base is explicit")
			return "", nil
		}
		p, err := Build(Config{Name: "x", RepoDir: repo, Base: "develop"}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if p.Base != "develop" {
			t.Errorf("Base = %q, want develop", p.Base)
		}
		if fr.sawCommand("git", "for-each-ref") {
			t.Errorf("explicit --base must not spend a git call on the local-branch listing: %v", fr.calls)
		}
	})

	t.Run("dry-run description lists the full union", func(t *testing.T) {
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		fr.handler = forEachRefRunner(plainRepoRunner(t), "bug-x\nfeat-a\nmain\nzeta\n", nil)
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		var out strings.Builder
		d.Stdout = &out

		if err := Create(Config{Name: "x", RepoDir: repo, DryRun: true}, d); err != nil {
			t.Fatalf("dry-run Create: %v", err)
		}
		printed := out.String()
		for _, want := range []string{"main", "feat-a", "bug-x", "zeta"} {
			if !strings.Contains(printed, want) {
				t.Errorf("dry-run description missing %q:\n%s", want, printed)
			}
		}
	})
}

// --- base-tracking decision ---------------------------------------------

// TestBaseTrackDecision tables the full input matrix of baseTrackDecision:
// every row names the action it expects, and warn rows also pin a reason
// substring (empty means "reason must be empty").
func TestBaseTrackDecision(t *testing.T) {
	cases := []struct {
		name              string
		gitSpice          bool
		baseTracked       bool
		baseIsLocalBranch bool
		parent            string
		parentTracked     bool
		wantAction        baseTrackAction
		wantReasonSubstr  string
	}{
		{
			name:       "not git-spice mode",
			gitSpice:   false,
			wantAction: baseTrackNone,
		},
		{
			name:        "base already tracked",
			gitSpice:    true,
			baseTracked: true,
			wantAction:  baseTrackNone,
		},
		{
			name:              "base is not a local branch (--base origin/main or a SHA)",
			gitSpice:          true,
			baseTracked:       false,
			baseIsLocalBranch: false,
			wantAction:        baseTrackNone,
		},
		{
			name:              "untracked local base, no known parent",
			gitSpice:          true,
			baseTracked:       false,
			baseIsLocalBranch: true,
			parent:            "",
			parentTracked:     false,
			wantAction:        baseTrackWarn,
			wantReasonSubstr:  "status.json",
		},
		{
			name:              "untracked local base, parent known but untracked",
			gitSpice:          true,
			baseTracked:       false,
			baseIsLocalBranch: true,
			parent:            "feature-x",
			parentTracked:     false,
			wantAction:        baseTrackWarn,
			wantReasonSubstr:  "feature-x",
		},
		{
			name:              "untracked local base, parent known and tracked",
			gitSpice:          true,
			baseTracked:       false,
			baseIsLocalBranch: true,
			parent:            "main",
			parentTracked:     true,
			wantAction:        baseTrackExplicit,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, reason := baseTrackDecision(tc.gitSpice, tc.baseTracked, tc.baseIsLocalBranch, tc.parent, tc.parentTracked)
			if action != tc.wantAction {
				t.Errorf("baseTrackDecision(...) action = %v, want %v", action, tc.wantAction)
			}
			if tc.wantReasonSubstr == "" {
				if reason != "" {
					t.Errorf("baseTrackDecision(...) reason = %q, want empty", reason)
				}
			} else if !strings.Contains(reason, tc.wantReasonSubstr) {
				t.Errorf("baseTrackDecision(...) reason = %q, want substring %q", reason, tc.wantReasonSubstr)
			}
		})
	}
}

// untrackedBaseRunner layers two answers for an untracked local base branch
// over plainRepoRunner(t): base resolves as an existing local branch (so
// branchExists is true), and `git worktree list --porcelain` reports a
// worktree for it at worktreeDir — the fixture directory holding base's own
// status.json. worktreeDir == "" means no worktree stanza is reported (the
// "no worktree found for base" row). Shared by
// TestBuildResolvesBaseParentFromStatusFile's rows; reusable by later tasks
// that exercise the same scenario.
func untrackedBaseRunner(t *testing.T, base, worktreeDir string) func(dir, name string, args []string) (string, error) {
	t.Helper()
	fallback := plainRepoRunner(t)
	return func(dir, name string, args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "git" && joined == "rev-parse --verify --quiet refs/heads/"+base:
			return "def456\n", nil
		case name == "git" && joined == "worktree list --porcelain":
			if worktreeDir == "" {
				return "", nil
			}
			return fmt.Sprintf("worktree %s\nHEAD def456\nbranch refs/heads/%s\n", worktreeDir, base), nil
		default:
			return fallback(dir, name, args)
		}
	}
}

// writeBaseStatusFixture writes base's own .afk/<base>/status.json into
// worktreeDir — the file Build reads to learn which branch base was created
// off. planName is a separate parameter because state.ReadStatus treats
// plan_name != <containing directory> as stale, which is exactly how the
// "stale status.json" rows are built.
func writeBaseStatusFixture(t *testing.T, worktreeDir, base, planName, baseBranch string) {
	t.Helper()
	s := Status{
		PlanName:   planName,
		Phase:      "planning",
		Branch:     base,
		BaseBranch: baseBranch,
		UpdatedAt:  fixedNowRFC,
	}
	if err := writeStatusAtomic(statusDir(worktreeDir, base), s); err != nil {
		t.Fatalf("writing base status fixture: %v", err)
	}
}

// TestBuildResolvesBaseParentFromStatusFile pins Build's use of
// baseTrackDecision against base's own status.json. Row (b) is load-bearing:
// removing the status lookup (parent always "") makes it fail (warn instead
// of explicit). Row (c) is load-bearing: no worktree for base must warn, not
// error. Row (d) is load-bearing: a stale status.json (plan_name mismatch)
// must warn rather than trust a name-mismatched base_branch. Row (e) is
// load-bearing: a known but untracked parent must warn, never track blind.
func TestBuildResolvesBaseParentFromStatusFile(t *testing.T) {
	stack := []spice.Branch{{Name: "main"}}
	base := "old-feature"

	writeBaseStatus := func(t *testing.T, worktreeDir, planName, baseBranch string) {
		t.Helper()
		writeBaseStatusFixture(t, worktreeDir, base, planName, baseBranch)
	}

	t.Run("a: base already tracked -> BaseTracked true, action none", func(t *testing.T) {
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		d.DetectGitSpice = func(string) bool { return true }
		trackedStack := []spice.Branch{{Name: "main"}, {Name: base, Down: &spice.BranchRef{Name: "main"}}}
		var stackCalls int
		d.Stack = func(string) ([]spice.Branch, error) {
			stackCalls++
			return trackedStack, nil
		}
		p, err := Build(Config{Name: "new-session", RepoDir: repo, Base: base}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if !p.BaseTracked {
			t.Errorf("BaseTracked = false, want true")
		}
		if p.BaseTrack != baseTrackNone {
			t.Errorf("BaseTrack = %v, want baseTrackNone", p.BaseTrack)
		}
		if stackCalls != 1 {
			t.Errorf("d.Stack called %d times, want exactly 1 (Build must reuse resolveBase's stack)", stackCalls)
		}
	})

	t.Run("b: base absent from stack, status.json names a tracked parent -> action explicit", func(t *testing.T) {
		repo := t.TempDir()
		worktreeDir := t.TempDir()
		writeBaseStatus(t, worktreeDir, base, "main")
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		fr.handler = untrackedBaseRunner(t, base, worktreeDir)
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		p, err := Build(Config{Name: "new-session", RepoDir: repo, Base: base}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if p.BaseTracked {
			t.Errorf("BaseTracked = true, want false")
		}
		if p.BaseTrack != baseTrackExplicit {
			t.Errorf("BaseTrack = %v, want baseTrackExplicit", p.BaseTrack)
		}
		if p.BaseParent != "main" {
			t.Errorf("BaseParent = %q, want main", p.BaseParent)
		}
		if !fr.sawCommand("git", "worktree list --porcelain") {
			t.Errorf("resolveWorktreePath's `git worktree list --porcelain` was never run; calls: %+v", fr.calls)
		}
	})

	t.Run("c: no worktree found for base -> warn, reason mentions status.json", func(t *testing.T) {
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		fr.handler = untrackedBaseRunner(t, base, "")
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		p, err := Build(Config{Name: "new-session", RepoDir: repo, Base: base}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if p.BaseTrack != baseTrackWarn {
			t.Errorf("BaseTrack = %v, want baseTrackWarn", p.BaseTrack)
		}
		if !strings.Contains(p.BaseTrackReason, "status.json") {
			t.Errorf("BaseTrackReason = %q, want it to mention status.json", p.BaseTrackReason)
		}
		// Pins that Build actually attempted the worktree lookup rather than
		// short-circuiting straight to "no parent" — a stubbed-out lookup
		// would produce the identical warn+reason and slip past the checks
		// above.
		if !fr.sawCommand("git", "worktree list --porcelain") {
			t.Errorf("resolveWorktreePath's `git worktree list --porcelain` was never run; calls: %+v", fr.calls)
		}
	})

	t.Run("d: status.json present but stale -> warn", func(t *testing.T) {
		repo := t.TempDir()
		worktreeDir := t.TempDir()
		writeBaseStatus(t, worktreeDir, "mismatched-plan-name", "main")
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		fr.handler = untrackedBaseRunner(t, base, worktreeDir)
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		p, err := Build(Config{Name: "new-session", RepoDir: repo, Base: base}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if p.BaseTrack != baseTrackWarn {
			t.Errorf("BaseTrack = %v, want baseTrackWarn", p.BaseTrack)
		}
		if want := "status.json for base is stale (plan-name-mismatch)"; p.BaseTrackReason != want {
			t.Errorf("BaseTrackReason = %q, want %q (a stale file must not be reported as missing)", p.BaseTrackReason, want)
		}
		// Same rationale as row (c): a fixture landed on disk that a skipped
		// lookup would never read.
		if !fr.sawCommand("git", "worktree list --porcelain") {
			t.Errorf("resolveWorktreePath's `git worktree list --porcelain` was never run; calls: %+v", fr.calls)
		}
	})

	t.Run("e: parent known but untracked -> warn, reason names the parent", func(t *testing.T) {
		repo := t.TempDir()
		worktreeDir := t.TempDir()
		writeBaseStatus(t, worktreeDir, base, "untracked-parent")
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		fr.handler = untrackedBaseRunner(t, base, worktreeDir)
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		p, err := Build(Config{Name: "new-session", RepoDir: repo, Base: base}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if p.BaseTrack != baseTrackWarn {
			t.Errorf("BaseTrack = %v, want baseTrackWarn", p.BaseTrack)
		}
		if !strings.Contains(p.BaseTrackReason, "untracked-parent") {
			t.Errorf("BaseTrackReason = %q, want it to name the parent %q", p.BaseTrackReason, "untracked-parent")
		}
	})

	t.Run("f: --base is a ref branchExists rejects -> action none, no warning fields", func(t *testing.T) {
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		d.DetectGitSpice = func(string) bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return stack, nil }
		p, err := Build(Config{Name: "new-session", RepoDir: repo, Base: "origin/main"}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if p.BaseTrack != baseTrackNone {
			t.Errorf("BaseTrack = %v, want baseTrackNone", p.BaseTrack)
		}
		if p.BaseParent != "" || p.BaseTrackReason != "" {
			t.Errorf("BaseParent/BaseTrackReason = %q/%q, want both empty", p.BaseParent, p.BaseTrackReason)
		}
	})
}

// --- full plain-git flow (non-dry-run) ---------------------------------

func TestCreatePlainGitFullFlow(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	var out strings.Builder
	d.Stdout = &out

	cfg := Config{Name: "add-billing-webhook", RepoDir: repo}
	if err := Create(cfg, d); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A plain-git branch+worktree must have been created off trunk.
	if !fr.sawCommand("git", "worktree add -b add-billing-webhook") {
		t.Errorf("plain-git worktree+branch not created; calls: %+v", fr.calls)
	}
	// The launch step must be announced with the child command line.
	if !strings.Contains(out.String(), "/afk:go --session add-billing-webhook") {
		t.Errorf("launch step not announced:\n%s", out.String())
	}

	// status.json must be seeded correctly and left with an empty tmux_window
	// (nothing updates it anymore).
	wtStatus := filepath.Join(repo, ".claude", "worktrees", "add-billing-webhook", ".afk", "add-billing-webhook", "status.json")
	data, err := os.ReadFile(wtStatus)
	if err != nil {
		t.Fatalf("reading seeded status: %v", err)
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("decoding status: %v", err)
	}
	if s.Phase != "planning" || s.PlanName != "add-billing-webhook" || s.Branch != "add-billing-webhook" || s.BaseBranch != "main" {
		t.Errorf("seeded status wrong: %+v", s)
	}
	if s.TmuxWindow != "" {
		t.Errorf("tmux_window = %q, want \"\" (no tmux window is ever created)", s.TmuxWindow)
	}
	if s.SessionID != "" || s.AwaitingInput || s.LoopCount != 0 {
		t.Errorf("non-seed fields mutated: %+v", s)
	}
}

// TestCreateLaunchesHostUnconditionally locks in the replacement for the old
// "prints manual launch commands" behavior: the host is always spawned, and
// the manual-launch fallback text is never printed.
func TestCreateLaunchesHostUnconditionally(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	var out strings.Builder
	d.Stdout = &out

	if err := Create(Config{Name: "launch-check", RepoDir: repo}, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	printed := out.String()
	if strings.Contains(printed, "Start the session manually") {
		t.Errorf("the manual-launch fallback is still present:\n%s", printed)
	}
	if !strings.Contains(printed, "Launch detached session host") {
		t.Errorf("host launch step missing:\n%s", printed)
	}
}

// --- git-spice full flow uses internal/spice ---------------------------

func TestCreateGitSpiceUsesSpiceBranch(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	d.DetectGitSpice = func(string) bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) {
		return []spice.Branch{{Name: "main"}}, nil
	}
	var gotOpts spice.BranchCreateOptions
	spiceCalled := false
	d.CreateSpiceBranch = func(opts spice.BranchCreateOptions) error {
		spiceCalled = true
		gotOpts = opts
		return nil
	}
	if err := Create(Config{Name: "stacked", RepoDir: repo}, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !spiceCalled {
		t.Fatal("git-spice path did not call CreateSpiceBranch")
	}
	if gotOpts.Name != "stacked" || gotOpts.Base != "main" {
		t.Errorf("CreateSpiceBranch opts = %+v, want Name=stacked Base=main", gotOpts)
	}
	// The worktree must have been created detached (spice creates the branch,
	// not `git worktree add -b`).
	if !fr.sawCommand("git", "worktree add --detach") {
		t.Errorf("git-spice path did not create a detached worktree; calls: %+v", fr.calls)
	}
	if fr.sawCommand("git", "worktree add -b") {
		t.Error("git-spice path used plain `git worktree add -b` instead of spice")
	}
}

// --- git-spice tracking: fixtures ---------------------------------------

// trackRecorder is the TrackSpiceBranch fake for the tracking flows. Every
// call is recorded in order, and an empty Base fails the test outright:
// spice.TrackBranch refuses one, and gs would otherwise guess a parent —
// the mis-parenting trap the whole feature exists to avoid. fail maps a
// branch name to the error the fake returns for it, so a row can make just
// the base-track attempt (or just the session-track attempt) blow up.
type trackRecorder struct {
	t     *testing.T
	calls []spice.BranchTrackOptions
	fail  map[string]error
}

func (r *trackRecorder) track(opts spice.BranchTrackOptions) error {
	if opts.Base == "" {
		r.t.Errorf("TrackSpiceBranch called with an empty Base for %q: gs would guess a parent", opts.Name)
	}
	r.calls = append(r.calls, opts)
	return r.fail[opts.Name]
}

// gsTrackRunner is the command fixture for the git-spice tracking flows. On
// top of plainRepoRunner it adds three answers:
//
//   - `rev-parse --verify --quiet refs/heads/<base>` succeeds, so
//     branchExists(base) is true. This is mandatory for every row with an
//     untracked local base: plainRepoRunner answers success only for "main",
//     and without this the decision lands in baseTrackNone and the row
//     silently exercises the wrong matrix cell.
//   - `worktree list --porcelain` reports one stanza per entry in worktrees.
//     Both halves are needed at once: Build resolves base's worktree to read
//     its status.json, and the worktrunk creation step resolves the new
//     session's worktree through the same command.
//   - `wt -y switch --create` materializes every mapped directory, so
//     seedStatus can write into whichever one resolveWorktreePath returns.
func gsTrackRunner(t *testing.T, base string, worktrees map[string]string) func(dir, name string, args []string) (string, error) {
	t.Helper()
	fallback := plainRepoRunner(t)
	return func(dir, name string, args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "git" && base != "" && joined == "rev-parse --verify --quiet refs/heads/"+base:
			return "def456\n", nil
		case name == "git" && joined == "worktree list --porcelain":
			branches := make([]string, 0, len(worktrees))
			for br := range worktrees {
				branches = append(branches, br)
			}
			sort.Strings(branches)
			var b strings.Builder
			for _, br := range branches {
				fmt.Fprintf(&b, "worktree %s\nHEAD def456\nbranch refs/heads/%s\n\n", worktrees[br], br)
			}
			return b.String(), nil
		case name == "wt" && strings.Contains(joined, "switch --create"):
			for _, p := range worktrees {
				if err := os.MkdirAll(p, 0o755); err != nil {
					return "", err
				}
			}
			return "", nil
		default:
			return fallback(dir, name, args)
		}
	}
}

// warningLines returns every line of s that starts with prefix. Used to pin
// the exact warning count a create emits: the dashboard's spawnWarnings
// matches `warning:`-prefixed lines, so a duplicate or a missing one is a
// user-visible defect, not a cosmetic one.
func warningLines(s, prefix string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			out = append(out, line)
		}
	}
	return out
}

// stepDescs returns every step description in p, for asserting on the steps
// Build inserted (and, in dry-run, prints verbatim).
func stepDescs(p *Plan) []string {
	out := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		out = append(out, s.Desc)
	}
	return out
}

// --- git-spice tracking: worktrunk path ---------------------------------

// TestCreateWorktrunkTracksSpiceBranch is the headline fix: a session
// created through worktrunk in a git-spice repo must end up tracked, which
// is what stops every future session's branch from being invisible to the
// base picker. It is load-bearing against deleting the post-worktrunk track
// step.
func TestCreateWorktrunkTracksSpiceBranch(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	wtPath := filepath.Join(repo, ".wt", "wt-session")
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	fr.handler = gsTrackRunner(t, "", map[string]string{"wt-session": wtPath})
	d.DetectGitSpice = func(string) bool { return true }
	d.HasWorktrunk = func() bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
	d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
		t.Fatalf("CreateSpiceBranch must not be called: worktrunk creates the branch")
		return nil
	}
	rec := &trackRecorder{t: t}
	d.TrackSpiceBranch = rec.track
	var errOut strings.Builder
	d.Stderr = &errOut

	if err := Create(Config{Name: "wt-session", RepoDir: repo}, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := []spice.BranchTrackOptions{{Dir: repo, Name: "wt-session", Base: "main"}}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Errorf("TrackSpiceBranch calls = %+v, want %+v", rec.calls, want)
	}
	if got := warningLines(errOut.String(), "warning:"); len(got) != 0 {
		t.Errorf("clean worktrunk+git-spice create emitted warnings: %v", got)
	}
}

// TestCreateWorktrunkUntrackedBaseTracksBaseWithExplicitParent pins the
// healing step and its ORDER: the base is tracked with the explicit parent
// from its own status.json first, then the new branch is tracked onto it.
// Reversing the two would hit `gs branch track --base <untracked>`, which
// git-spice rejects.
func TestCreateWorktrunkUntrackedBaseTracksBaseWithExplicitParent(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	base := "old-feature"
	baseWT := t.TempDir()
	writeBaseStatusFixture(t, baseWT, base, base, "main")
	wtPath := filepath.Join(repo, ".wt", "new-session")
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	fr.handler = gsTrackRunner(t, base, map[string]string{base: baseWT, "new-session": wtPath})
	d.DetectGitSpice = func(string) bool { return true }
	d.HasWorktrunk = func() bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
	d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
		t.Fatalf("CreateSpiceBranch must not be called: worktrunk creates the branch")
		return nil
	}
	rec := &trackRecorder{t: t}
	d.TrackSpiceBranch = rec.track
	var errOut strings.Builder
	d.Stderr = &errOut

	if err := Create(Config{Name: "new-session", RepoDir: repo, Base: base}, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := []spice.BranchTrackOptions{
		{Dir: repo, Name: base, Base: "main"},
		{Dir: repo, Name: "new-session", Base: base},
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Errorf("TrackSpiceBranch calls = %+v, want %+v (base first, with its explicit parent)", rec.calls, want)
	}
	if got := warningLines(errOut.String(), "warning:"); len(got) != 0 {
		t.Errorf("successful healing emitted warnings: %v", got)
	}
}

// TestCreateUntrackedBaseNeverGuesses is the crucible row: whenever the
// base's parent cannot be established from its own status.json, afk must
// NOT track it (a guessed parent makes gs flag the branch needsRestack, and
// the user's next `gs stack restack` replays an unrelated session's commits
// onto it). Every row must instead emit exactly one `warning: base ...`
// line — the line the dashboard's spawnWarnings matches — and still succeed.
func TestCreateUntrackedBaseNeverGuesses(t *testing.T) {
	const base = "old-feature"
	const name = "new-session"
	rows := []struct {
		label      string
		planName   string // "" means: write no status.json at all
		baseBranch string
		wantReason string
	}{
		{
			label:      "no status.json for base",
			wantReason: "no status.json for base",
		},
		{
			label:      "stale status.json (plan_name mismatch)",
			planName:   "mismatched-plan-name",
			baseBranch: "main",
			wantReason: "status.json for base is stale (plan-name-mismatch)",
		},
		{
			label:      "parent named but untracked",
			planName:   base,
			baseBranch: "untracked-parent",
			wantReason: `parent "untracked-parent" is not tracked`,
		},
	}
	for _, row := range rows {
		for _, useWT := range []bool{true, false} {
			label := row.label + "/plain-git"
			how := "with plain git"
			if useWT {
				label = row.label + "/worktrunk"
				how = "by worktrunk"
			}
			t.Run(label, func(t *testing.T) {
				shortTmpDir(t)
				repo := t.TempDir()
				baseWT := t.TempDir()
				if row.planName != "" {
					writeBaseStatusFixture(t, baseWT, base, row.planName, row.baseBranch)
				}
				worktrees := map[string]string{base: baseWT}
				if useWT {
					worktrees[name] = filepath.Join(repo, ".wt", name)
				}
				fr := &fakeRunner{}
				d := baseDeps(t, fr)
				fr.handler = gsTrackRunner(t, base, worktrees)
				d.DetectGitSpice = func(string) bool { return true }
				d.HasWorktrunk = func() bool { return useWT }
				d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
				d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
					t.Fatalf("CreateSpiceBranch must not be called with an untracked base")
					return nil
				}
				// d.TrackSpiceBranch stays baseDeps' t.Fatalf tripwire: no
				// row here may track anything.
				var errOut strings.Builder
				d.Stderr = &errOut

				p, err := Build(Config{Name: name, RepoDir: repo, Base: base}, d)
				if err != nil {
					t.Fatalf("Build: %v", err)
				}
				wantDesc := fmt.Sprintf("WARNING: base %q is not git-spice-tracked (%s) — %q will be created %s and left untracked", base, row.wantReason, name, how)
				var warnSteps []string
				for _, desc := range stepDescs(p) {
					if strings.HasPrefix(desc, "WARNING: ") {
						warnSteps = append(warnSteps, desc)
					}
				}
				if len(warnSteps) != 1 {
					t.Fatalf("WARNING steps = %v, want exactly one (%q)", warnSteps, wantDesc)
				}
				if warnSteps[0] != wantDesc {
					t.Errorf("WARNING step =\n  %q\nwant\n  %q", warnSteps[0], wantDesc)
				}
				if err := p.Run(); err != nil {
					t.Fatalf("Run must not fail because the base is untracked: %v", err)
				}
				got := warningLines(errOut.String(), "warning: base")
				if len(got) != 1 {
					t.Fatalf("`warning: base` lines = %v, want exactly one\n--- stderr ---\n%s", got, errOut.String())
				}
				if want := "warning: " + strings.TrimPrefix(wantDesc, "WARNING: "); got[0] != want {
					t.Errorf("stderr warning =\n  %q\nwant\n  %q", got[0], want)
				}
			})
		}
	}
}

// TestCreateWorktrunkSkipsTrackWhenBaseTrackFails covers the run-time
// failure of an otherwise-valid heal: `gs branch track` on the base errors,
// so the session branch cannot be tracked onto it either. Both facts must be
// reported, exactly once each, and the create must still succeed.
func TestCreateWorktrunkSkipsTrackWhenBaseTrackFails(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	base := "old-feature"
	baseWT := t.TempDir()
	writeBaseStatusFixture(t, baseWT, base, base, "main")
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	fr.handler = gsTrackRunner(t, base, map[string]string{base: baseWT, "new-session": filepath.Join(repo, ".wt", "new-session")})
	d.DetectGitSpice = func(string) bool { return true }
	d.HasWorktrunk = func() bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
	d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
		t.Fatalf("CreateSpiceBranch must not be called: worktrunk creates the branch")
		return nil
	}
	rec := &trackRecorder{t: t, fail: map[string]error{base: errors.New("gs: branch not tracked")}}
	d.TrackSpiceBranch = rec.track
	var errOut strings.Builder
	d.Stderr = &errOut

	if err := Create(Config{Name: "new-session", RepoDir: repo, Base: base}, d); err != nil {
		t.Fatalf("Create must not fail when git-spice tracking fails: %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0].Name != base {
		t.Fatalf("TrackSpiceBranch calls = %+v, want exactly one (the base attempt)", rec.calls)
	}
	warns := warningLines(errOut.String(), "warning:")
	if len(warns) != 2 {
		t.Fatalf("warning lines = %v, want two (base track failed, session track skipped)\n--- stderr ---\n%s", warns, errOut.String())
	}
	if !strings.Contains(warns[0], "Track base branch") || !strings.Contains(warns[0], "branch not tracked") {
		t.Errorf("first warning = %q, want the failed base-track step", warns[0])
	}
	if !strings.Contains(warns[1], `Track branch "new-session"`) || !strings.Contains(warns[1], "skipping") {
		t.Errorf("second warning = %q, want the skipped session track", warns[1])
	}
}

// --- git-spice tracking: plain-git degrade (no worktrunk) ---------------

// TestCreateGitSpiceDegradesToPlainWhenBaseUntracked pins the no-worktrunk
// half. `gs branch create --target <untracked>` fails with "branch not
// tracked", so an untracked base must route to plain `git worktree add -b`
// instead of failing the create — which is what it does today.
func TestCreateGitSpiceDegradesToPlainWhenBaseUntracked(t *testing.T) {
	const base = "old-feature"
	const name = "new-session"
	rows := []struct {
		label       string
		parent      string // base_branch written into base's status.json
		trackFails  bool
		wantWarnSub string
	}{
		{
			label:       "base-track attempt fails at run time",
			parent:      "main",
			trackFails:  true,
			wantWarnSub: fmt.Sprintf("warning: base %q is not git-spice-tracked; created %q as a plain git branch", base, name),
		},
		{
			label:       "parent unknown, nothing to track",
			parent:      "",
			wantWarnSub: fmt.Sprintf("warning: base %q is not git-spice-tracked (no status.json for base)", base),
		},
	}
	for _, row := range rows {
		t.Run(row.label, func(t *testing.T) {
			shortTmpDir(t)
			repo := t.TempDir()
			baseWT := t.TempDir()
			if row.parent != "" {
				writeBaseStatusFixture(t, baseWT, base, base, row.parent)
			}
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			fr.handler = gsTrackRunner(t, base, map[string]string{base: baseWT})
			d.DetectGitSpice = func(string) bool { return true }
			d.HasWorktrunk = func() bool { return false }
			d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
			d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
				t.Fatalf("CreateSpiceBranch must not be called with an untracked base: gs rejects --target <untracked>")
				return nil
			}
			if row.trackFails {
				rec := &trackRecorder{t: t, fail: map[string]error{base: errors.New("gs: branch not tracked")}}
				d.TrackSpiceBranch = rec.track
			}
			var errOut strings.Builder
			d.Stderr = &errOut

			if err := Create(Config{Name: name, RepoDir: repo, Base: base}, d); err != nil {
				t.Fatalf("Create must degrade, not fail: %v", err)
			}
			if !fr.sawCommand("git", fmt.Sprintf("worktree add -b %s", name)) {
				t.Errorf("degrade did not run `git worktree add -b %s`; calls: %+v", name, fr.calls)
			}
			if !fr.sawCommand("git", base) {
				t.Errorf("degrade did not branch off %q; calls: %+v", base, fr.calls)
			}
			if fr.sawCommand("git", "worktree add --detach") {
				t.Error("degrade still created a detached worktree for the git-spice path")
			}
			if !strings.Contains(errOut.String(), row.wantWarnSub) {
				t.Errorf("stderr missing %q\n--- stderr ---\n%s", row.wantWarnSub, errOut.String())
			}
		})
	}
}

// TestCreateExplicitRefBaseIsWarningFree guards the cell that must stay
// silent: `--base origin/main` (or a SHA) is a deliberate, valid choice that
// git-spice simply cannot track. Row (a) is load-bearing against restoring
// an unconditional post-worktrunk track step; row (b) pins that the
// no-worktrunk path degrades to plain git — today it fails hard with
// `gs branch create --target origin/main: branch not tracked`.
func TestCreateExplicitRefBaseIsWarningFree(t *testing.T) {
	const base = "origin/main"
	const name = "ref-session"

	t.Run("a: worktrunk inserts no track step", func(t *testing.T) {
		shortTmpDir(t)
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		// base is deliberately NOT made to resolve as a local branch.
		fr.handler = gsTrackRunner(t, "", map[string]string{name: filepath.Join(repo, ".wt", name)})
		d.DetectGitSpice = func(string) bool { return true }
		d.HasWorktrunk = func() bool { return true }
		d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
		d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
			t.Fatalf("CreateSpiceBranch must not be called: worktrunk creates the branch")
			return nil
		}
		var errOut strings.Builder
		d.Stderr = &errOut

		p, err := Build(Config{Name: name, RepoDir: repo, Base: base}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		for _, desc := range stepDescs(p) {
			if strings.HasPrefix(desc, "Track ") || strings.HasPrefix(desc, "WARNING: ") {
				t.Errorf("unexpected step for a non-local base: %q", desc)
			}
		}
		if err := p.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := warningLines(errOut.String(), "warning:"); len(got) != 0 {
			t.Errorf("`--base %s` emitted warnings: %v", base, got)
		}
	})

	t.Run("b: no worktrunk creates a plain-git branch, no warning", func(t *testing.T) {
		shortTmpDir(t)
		repo := t.TempDir()
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		d.DetectGitSpice = func(string) bool { return true }
		d.HasWorktrunk = func() bool { return false }
		d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
		d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
			t.Fatalf("CreateSpiceBranch must not be called: gs cannot track %s", base)
			return nil
		}
		var errOut strings.Builder
		d.Stderr = &errOut

		p, err := Build(Config{Name: name, RepoDir: repo, Base: base}, d)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		wantDesc := fmt.Sprintf("Create worktree at %s + plain-git branch %q off %s (base is not a local branch; git-spice cannot track it)", p.WorktreeTarget, name, base)
		if !slicesContains(stepDescs(p), wantDesc) {
			t.Errorf("step descriptions = %q\nwant one equal to\n  %q", stepDescs(p), wantDesc)
		}
		if err := p.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !fr.sawCommand("git", fmt.Sprintf("worktree add -b %s", name)) {
			t.Errorf("plain-git creation command missing; calls: %+v", fr.calls)
		}
		if got := warningLines(errOut.String(), "warning:"); len(got) != 0 {
			t.Errorf("`--base %s` emitted warnings: %v", base, got)
		}
	})
}

// TestCreateTrackFailureNeverFailsCreate: git-spice bookkeeping is never
// allowed to fail a create, so the session-branch track step is bestEffort.
func TestCreateTrackFailureNeverFailsCreate(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	fr.handler = gsTrackRunner(t, "", map[string]string{"wt-session": filepath.Join(repo, ".wt", "wt-session")})
	d.DetectGitSpice = func(string) bool { return true }
	d.HasWorktrunk = func() bool { return true }
	d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
	d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
		t.Fatalf("CreateSpiceBranch must not be called: worktrunk creates the branch")
		return nil
	}
	rec := &trackRecorder{t: t, fail: map[string]error{"wt-session": errors.New("gs: boom")}}
	d.TrackSpiceBranch = rec.track
	var errOut strings.Builder
	d.Stderr = &errOut

	if err := Create(Config{Name: "wt-session", RepoDir: repo}, d); err != nil {
		t.Fatalf("Create must not fail when `gs branch track` fails: %v", err)
	}
	warns := warningLines(errOut.String(), "warning:")
	if len(warns) != 1 || !strings.Contains(warns[0], "gs: boom") {
		t.Errorf("warning lines = %v, want one naming the track failure", warns)
	}
	// A failed track must not be recorded as this invocation's, or rollback
	// would untrack a branch it never tracked.
	if fr.sawCommand("git", "worktree remove") {
		t.Error("a failed bestEffort track rolled the create back")
	}
}

// slicesContains is a local helper (no generics dependency on the Go
// version's slices package in this file) for exact-match Desc assertions.
func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestDryRunListsTrackSteps walks the creation matrix and pins the step
// descriptions --dry-run prints verbatim. The Desc is fixed at Build time,
// so each cell must read truthfully about what will actually happen.
func TestDryRunListsTrackSteps(t *testing.T) {
	const base = "old-feature"
	const name = "dry-session"
	rows := []struct {
		label    string
		useWT    bool
		base     string // --base value
		parent   string // base_branch in base's status.json; "" writes no file
		localRef bool   // whether base resolves as a local branch
		want     []string
		absent   []string
	}{
		{
			label:    "worktrunk, base tracked",
			useWT:    true,
			base:     "main",
			localRef: true,
			want:     []string{fmt.Sprintf("Track branch %q with git-spice (base %q)", name, "main")},
			absent:   []string{"Track base branch", "WARNING: base"},
		},
		{
			label:    "worktrunk, untracked base with a tracked parent",
			useWT:    true,
			base:     base,
			parent:   "main",
			localRef: true,
			want: []string{
				fmt.Sprintf("Track base branch %q with git-spice (parent %q from its session's status.json)", base, "main"),
				fmt.Sprintf("Track branch %q with git-spice (base %q)", name, base),
			},
			absent: []string{"WARNING: base"},
		},
		{
			label:    "worktrunk, untracked base with no parent",
			useWT:    true,
			base:     base,
			localRef: true,
			want:     []string{fmt.Sprintf("WARNING: base %q is not git-spice-tracked (no status.json for base) — %q will be created by worktrunk and left untracked", base, name)},
			absent:   []string{"Track base branch", "Track branch"},
		},
		{
			label:    "no worktrunk, untracked base with a tracked parent",
			base:     base,
			parent:   "main",
			localRef: true,
			want: []string{
				fmt.Sprintf("Track base branch %q with git-spice (parent %q from its session's status.json)", base, "main"),
				fmt.Sprintf("tracked git-spice branch %q if tracking the base succeeds, else a plain git branch", name),
			},
			absent: []string{"WARNING: base", fmt.Sprintf("Track branch %q", name)},
		},
		{
			label:    "no worktrunk, untracked base with no parent",
			base:     base,
			localRef: true,
			want: []string{
				fmt.Sprintf("WARNING: base %q is not git-spice-tracked (no status.json for base) — %q will be created with plain git and left untracked", base, name),
				fmt.Sprintf("plain-git branch %q off %s (base untracked)", name, base),
			},
			absent: []string{"Track base branch", "Track branch"},
		},
		{
			label:  "no worktrunk, base is not a local branch",
			base:   "origin/main",
			want:   []string{fmt.Sprintf("plain-git branch %q off %s (base is not a local branch; git-spice cannot track it)", name, "origin/main")},
			absent: []string{"Track base branch", "Track branch", "WARNING: base"},
		},
	}
	for _, row := range rows {
		t.Run(row.label, func(t *testing.T) {
			repo := t.TempDir()
			baseWT := t.TempDir()
			if row.parent != "" {
				writeBaseStatusFixture(t, baseWT, row.base, row.base, row.parent)
			}
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			localBase := ""
			if row.localRef {
				localBase = row.base
			}
			fr.handler = gsTrackRunner(t, localBase, map[string]string{row.base: baseWT})
			d.DetectGitSpice = func(string) bool { return true }
			d.HasWorktrunk = func() bool { return row.useWT }
			d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
			var out strings.Builder
			d.Stdout = &out

			if err := Create(Config{Name: name, RepoDir: repo, Base: row.base, DryRun: true}, d); err != nil {
				t.Fatalf("dry-run Create: %v", err)
			}
			printed := out.String()
			for _, want := range row.want {
				if !strings.Contains(printed, want) {
					t.Errorf("dry-run output missing %q\n--- output ---\n%s", want, printed)
				}
			}
			for _, absent := range row.absent {
				if strings.Contains(printed, absent) {
					t.Errorf("dry-run output must not contain %q\n--- output ---\n%s", absent, printed)
				}
			}
		})
	}
}

// --- rollback -----------------------------------------------------------

func TestRollbackRemovesWorktreeAndDeletesEmptyBranch(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	// Make the host launch fail so rollback fires after worktree+seed
	// succeeded. Failing the Executable lookup is the cheapest way in: it
	// happens before anything is forked.
	d.Executable = func() (string, error) { return "", &exitError{} }
	var errOut strings.Builder
	d.Stderr = &errOut

	err := Create(Config{Name: "rollme", RepoDir: repo}, d)
	if err == nil {
		t.Fatal("Create should have failed when the host launch failed")
	}
	// Rollback must remove the just-created worktree and delete the branch
	// (0 commits beyond base).
	if !fr.sawCommand("git", "worktree remove --force") {
		t.Errorf("rollback did not remove the worktree; calls: %+v", fr.calls)
	}
	if !fr.sawCommand("git", "branch -D rollme") {
		t.Errorf("rollback did not delete the empty branch; calls: %+v", fr.calls)
	}
}

func TestRollbackKeepsBranchWithCommits(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	d.Executable = func() (string, error) { return "", &exitError{} }
	base := plainRepoRunner(t)
	fr.handler = func(dir, name string, args []string) (string, error) {
		joined := strings.Join(args, " ")
		if name == "git" && strings.HasPrefix(joined, "rev-list --count") {
			return "3\n", nil // branch has work — must NOT be deleted
		}
		return base(dir, name, args)
	}
	var errOut strings.Builder
	d.Stderr = &errOut

	if err := Create(Config{Name: "keepme", RepoDir: repo}, d); err == nil {
		t.Fatal("Create should have failed when the host launch failed")
	}
	if fr.sawCommand("git", "branch -D keepme") {
		t.Error("rollback deleted a branch that had commits beyond base")
	}
	if !strings.Contains(errOut.String(), "leaving branch") {
		t.Errorf("rollback did not warn about preserving the branch:\n%s", errOut.String())
	}
}

// TestRollbackUntracksSpiceBranch pins the two halves of tracking rollback:
// the session branch THIS invocation tracked is untracked again, while a
// base branch it tracked is deliberately left tracked (untracking someone
// else's branch is worse than leaving a correct parent behind) with a note
// saying so. Row (a) is load-bearing against emitting that note
// unconditionally.
func TestRollbackUntracksSpiceBranch(t *testing.T) {
	const name = "roll-session"
	untrackedBase := "old-feature"

	rows := []struct {
		label     string
		base      string
		writeStat bool
		wantTrack []spice.BranchTrackOptions
		wantNote  bool
	}{
		{
			label:     "a: base was already tracked -> no note",
			base:      "main",
			wantTrack: []spice.BranchTrackOptions{{Dir: "", Name: name, Base: "main"}},
			wantNote:  false,
		},
		{
			label:     "b: base was tracked here -> note that it is left tracked",
			base:      untrackedBase,
			writeStat: true,
			wantTrack: []spice.BranchTrackOptions{
				{Dir: "", Name: untrackedBase, Base: "main"},
				{Dir: "", Name: name, Base: untrackedBase},
			},
			wantNote: true,
		},
	}
	for _, row := range rows {
		t.Run(row.label, func(t *testing.T) {
			repo := t.TempDir()
			baseWT := t.TempDir()
			if row.writeStat {
				writeBaseStatusFixture(t, baseWT, row.base, row.base, "main")
			}
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			fr.handler = gsTrackRunner(t, row.base, map[string]string{
				row.base: baseWT,
				name:     filepath.Join(repo, ".wt", name),
			})
			d.DetectGitSpice = func(string) bool { return true }
			d.HasWorktrunk = func() bool { return true }
			d.Stack = func(string) ([]spice.Branch, error) { return []spice.Branch{{Name: "main"}}, nil }
			d.CreateSpiceBranch = func(spice.BranchCreateOptions) error {
				t.Fatalf("CreateSpiceBranch must not be called: worktrunk creates the branch")
				return nil
			}
			rec := &trackRecorder{t: t}
			d.TrackSpiceBranch = rec.track
			var untracked []string
			d.UntrackSpiceBranch = func(dir, n string) error {
				if dir != repo {
					t.Errorf("UntrackSpiceBranch dir = %q, want the repo root %q", dir, repo)
				}
				untracked = append(untracked, n)
				return nil
			}
			// Fail the launch step so rollback runs after the track steps.
			d.Executable = func() (string, error) { return "", &exitError{} }
			var errOut strings.Builder
			d.Stderr = &errOut

			if err := Create(Config{Name: name, RepoDir: repo, Base: row.base}, d); err == nil {
				t.Fatal("Create should have failed when the host launch failed")
			}
			for i := range row.wantTrack {
				row.wantTrack[i].Dir = repo
			}
			if !reflect.DeepEqual(rec.calls, row.wantTrack) {
				t.Fatalf("TrackSpiceBranch calls = %+v, want %+v", rec.calls, row.wantTrack)
			}
			if !reflect.DeepEqual(untracked, []string{name}) {
				t.Errorf("UntrackSpiceBranch names = %v, want exactly [%s] (never the base)", untracked, name)
			}
			note := fmt.Sprintf("note: base branch %q was tracked with git-spice (parent %q) and is left tracked", row.base, "main")
			if got := strings.Contains(errOut.String(), note); got != row.wantNote {
				t.Errorf("rollback note present = %v, want %v (looking for %q)\n--- stderr ---\n%s", got, row.wantNote, note, errOut.String())
			}
		})
	}
}

// --- preconditions ------------------------------------------------------

func TestBuildRefusesExistingBranch(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	// Override runner so the target branch resolves (already exists).
	d.Run = func(dir, name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if name == "git" && strings.HasPrefix(joined, "rev-parse --verify --quiet refs/heads/dup") {
			return "abc\n", nil
		}
		return fr.run(dir, name, args...)
	}
	_, err := Build(Config{Name: "dup", RepoDir: repo}, d)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Build error = %v, want 'already exists'", err)
	}
}

func TestBuildRefusesExistingWorktreePath(t *testing.T) {
	repo := t.TempDir()
	// Pre-create the worktree target so the precondition trips.
	target := filepath.Join(repo, ".claude", "worktrees", "dup")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	_, err := Build(Config{Name: "dup", RepoDir: repo}, d)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Build error = %v, want 'already exists'", err)
	}
}

func TestBuildRequiresRepoDir(t *testing.T) {
	fr := &fakeRunner{}
	_, err := Build(Config{Name: "x"}, baseDeps(t, fr))
	if err == nil || !strings.Contains(err.Error(), "RepoDir") {
		t.Fatalf("Build error = %v, want RepoDir required", err)
	}
}

// --- worktrunk path -----------------------------------------------------

func TestCreateWorktrunkPath(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	// worktrunk owns its own path scheme; emulate one under the repo.
	wtPath := filepath.Join(repo, ".wt", "wt-session")
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	d.HasWorktrunk = func() bool { return true }
	var out strings.Builder
	d.Stdout = &out

	base := plainRepoRunner(t)
	fr.handler = func(dir, name string, args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "wt" && strings.Contains(joined, "switch --create"):
			if err := os.MkdirAll(wtPath, 0o755); err != nil {
				return "", err
			}
			return "", nil
		case name == "git" && joined == "worktree list --porcelain":
			// Report the worktrunk-managed worktree for the branch so the
			// package can resolve its path tool-agnostically.
			return "worktree " + wtPath + "\nHEAD abc123\nbranch refs/heads/wt-session\n", nil
		default:
			return base(dir, name, args)
		}
	}

	if err := Create(Config{Name: "wt-session", RepoDir: repo}, d); err != nil {
		t.Fatalf("Create (worktrunk): %v", err)
	}
	// Must drive worktrunk with -y (non-interactive), never plain git.
	if !fr.sawCommand("wt", "-y switch --create") {
		t.Errorf("worktrunk path did not invoke `wt -y switch --create`; calls: %+v", fr.calls)
	}
	// The resolved base branch must be handed to wt so the base picker /
	// trunk determination (behavior 2) is honored on the worktrunk path.
	if !fr.sawCommand("wt", "--base main") {
		t.Errorf("worktrunk path did not pass the resolved base via --base; calls: %+v", fr.calls)
	}
	if fr.sawCommand("git", "worktree add") {
		t.Error("worktrunk path fell back to `git worktree add`")
	}
	// status.json must land inside the worktrunk-managed worktree.
	statusPath := filepath.Join(wtPath, ".afk", "wt-session", "status.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("status.json not seeded in worktrunk worktree: %v", err)
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("decoding status: %v", err)
	}
	if s.Branch != "wt-session" || s.BaseBranch != "main" || s.TmuxWindow != "" {
		t.Errorf("worktrunk-path status wrong: %+v", s)
	}
}

// --- free-text title is never spliced into any command -----------------

func TestDescriptionNeverSplicedIntoCommands(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	var out strings.Builder
	d.Stdout = &out

	const secret = "Add billing; rm -rf / $(whoami) `id`"
	if err := Create(Config{Name: "titled", RepoDir: repo, Description: secret}, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	needles := []string{secret, "rm -rf", "whoami", "`id`"}
	// The free-text description must never appear (whole or in part) in any argv.
	for _, c := range fr.calls {
		joined := strings.Join(c.args, " ")
		for _, needle := range needles {
			if strings.Contains(joined, needle) {
				t.Errorf("free-text description leaked into command %s %v", c.name, c.args)
			}
		}
	}
	// Nor in the host argv, which carries the child command line.
	hostJoined := strings.Join(hostArgs(HostSpawn{Name: "titled", Worktree: "/wt"}, "/sock"), " ")
	for _, needle := range needles {
		if strings.Contains(hostJoined, needle) {
			t.Errorf("free-text description leaked into the host argv: %s", hostJoined)
		}
	}
	// It must be persisted to the description.md sidecar with exactly one trailing newline.
	descPath := filepath.Join(repo, ".claude", "worktrees", "titled", ".afk", "titled", "description.md")
	got, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatalf("description sidecar not written: %v", err)
	}
	if string(got) != secret+"\n" {
		t.Errorf("description sidecar = %q, want %q", string(got), secret+"\n")
	}
	titlePath := filepath.Join(repo, ".claude", "worktrees", "titled", ".afk", "titled", "title.txt")
	if _, err := os.Stat(titlePath); !os.IsNotExist(err) {
		t.Errorf("title.txt exists but should not (err=%v)", err)
	}
}

// --- detached host launch ----------------------------------------------

func TestHostArgs(t *testing.T) {
	tests := []struct {
		name   string
		spec   HostSpawn
		socket string
		want   []string
	}{
		{
			name:   "plain session",
			spec:   HostSpawn{Name: "billing", Worktree: "/repo/.claude/worktrees/billing"},
			socket: "/tmp/afk-501/billing-deadbeef.sock",
			want: []string{
				"_host",
				"--name", "billing",
				"--worktree", "/repo/.claude/worktrees/billing",
				"--socket", "/tmp/afk-501/billing-deadbeef.sock",
				"--log", "/repo/.claude/worktrees/billing/.afk/billing/host.log",
				"--meta", "/repo/.claude/worktrees/billing/.afk/billing/host.json",
				"--",
				"claude", "/afk:go --session billing",
			},
		},
		{
			name:   "hyphenated session in a worktrunk path",
			spec:   HostSpawn{Name: "add-billing-webhook", Worktree: "/wt/add-billing-webhook"},
			socket: "/tmp/afk-0/add-billing-webhook-01234567.sock",
			want: []string{
				"_host",
				"--name", "add-billing-webhook",
				"--worktree", "/wt/add-billing-webhook",
				"--socket", "/tmp/afk-0/add-billing-webhook-01234567.sock",
				"--log", "/wt/add-billing-webhook/.afk/add-billing-webhook/host.log",
				"--meta", "/wt/add-billing-webhook/.afk/add-billing-webhook/host.json",
				"--",
				"claude", "/afk:go --session add-billing-webhook",
			},
		},
		{
			name:   "codex session carries --provider before the separator",
			spec:   HostSpawn{Name: "billing", Worktree: "/wt/billing", Autonomous: true, Provider: "codex"},
			socket: "/tmp/afk-501/billing-deadbeef.sock",
			want: []string{
				"_host",
				"--name", "billing",
				"--worktree", "/wt/billing",
				"--socket", "/tmp/afk-501/billing-deadbeef.sock",
				"--log", "/wt/billing/.afk/billing/host.log",
				"--meta", "/wt/billing/.afk/billing/host.json",
				"--provider", "codex",
				"--",
				"codex", "$afk:conductor --session billing --autonomous",
			},
		},
		{
			name:   "opencode session carries --provider before the separator",
			spec:   HostSpawn{Name: "billing", Worktree: "/wt/billing", Autonomous: true, Provider: "opencode"},
			socket: "/tmp/afk-501/billing-deadbeef.sock",
			want: []string{
				"_host",
				"--name", "billing",
				"--worktree", "/wt/billing",
				"--socket", "/tmp/afk-501/billing-deadbeef.sock",
				"--log", "/wt/billing/.afk/billing/host.log",
				"--meta", "/wt/billing/.afk/billing/host.json",
				"--provider", "opencode",
				"--",
				"opencode", "--standalone", "--prompt", "/afk-go --session billing --autonomous",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hostArgs(tt.spec, tt.socket)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("hostArgs = %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

// TestHostArgsChildArgvIsSeparated pins the two properties the `_host`
// subcommand's flag parsing depends on: everything after `--` is the child
// argv, and the child gets `claude` plus exactly ONE argument (the slash
// command as a single word), not a pre-split command line.
func TestHostArgsChildArgvIsSeparated(t *testing.T) {
	args := hostArgs(HostSpawn{Name: "sess", Worktree: "/wt"}, "/sock")
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatalf("hostArgs has no `--` separator: %v", args)
	}
	child := args[sep+1:]
	want := []string{"claude", "/afk:go --session sess"}
	if !reflect.DeepEqual(child, want) {
		t.Fatalf("child argv = %#v, want %#v", child, want)
	}
	cfg, err := buildServeConfigLike(args[:sep], child)
	if err != nil {
		t.Fatalf("host flags do not round-trip: %v", err)
	}
	if cfg["--name"] != "sess" || cfg["--worktree"] != "/wt" || cfg["--socket"] != "/sock" {
		t.Errorf("host flags = %v", cfg)
	}
	if cfg["--log"] != filepath.Join("/wt", ".afk", "sess", "host.log") {
		t.Errorf("--log = %q", cfg["--log"])
	}
	if cfg["--meta"] != filepath.Join("/wt", ".afk", "sess", "host.json") {
		t.Errorf("--meta = %q", cfg["--meta"])
	}
}

// buildServeConfigLike reduces the flag half of a host argv to a map, so the
// argv test can assert flag/value pairing without importing package main's
// flag set.
func buildServeConfigLike(flags, child []string) (map[string]string, error) {
	if len(child) == 0 {
		return nil, errors.New("empty child argv")
	}
	if len(flags) == 0 || flags[0] != "_host" {
		return nil, fmt.Errorf("argv does not start with the _host subcommand: %v", flags)
	}
	rest := flags[1:]
	if len(rest)%2 != 0 {
		return nil, fmt.Errorf("odd number of flag tokens: %v", rest)
	}
	out := map[string]string{}
	for i := 0; i < len(rest); i += 2 {
		if !strings.HasPrefix(rest[i], "--") {
			return nil, fmt.Errorf("expected a flag at %d, got %q", i, rest[i])
		}
		out[rest[i]] = rest[i+1]
	}
	return out, nil
}

// --- unattended mode ------------------------------------------------------

// unattendedLaunch is what the default provider says an unattended launch
// looks like. The tests below compare against it instead of spelling the
// option out, because this package must not know any provider's option: the
// whole translation is p.Unattended.
func unattendedLaunch(t *testing.T, name string, scratch, autonomous bool) []string {
	t.Helper()
	p, err := provider.Lookup("")
	if err != nil {
		t.Fatalf("provider.Lookup(\"\"): %v", err)
	}
	plain := p.LaunchArgv(name, scratch, autonomous)
	argv, err := p.Unattended(plain)
	if err != nil {
		t.Fatalf("the default provider has no unattended mode: %v", err)
	}
	if reflect.DeepEqual(argv, plain) {
		t.Fatalf("the default provider's unattended argv equals its plain one (%q); these tests would prove nothing", argv)
	}
	return argv
}

// providerWithoutUnattended finds a provider that reports
// ErrUnattendedUnsupported by asking each one, so no provider is named here.
func providerWithoutUnattended(t *testing.T) string {
	t.Helper()
	for _, name := range provider.Names() {
		p, err := provider.Lookup(name)
		if err != nil {
			t.Fatalf("provider.Lookup(%q): %v", name, err)
		}
		if _, err := p.Unattended(p.LaunchArgv("probe", false, false)); errors.Is(err, provider.ErrUnattendedUnsupported) {
			return name
		}
	}
	t.Skip("every provider has an unattended mode; nothing to refuse")
	return ""
}

// childOf returns the child argv of a host argv: everything after `--`.
func childOf(t *testing.T, args []string) []string {
	t.Helper()
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	t.Fatalf("host argv has no `--` separator: %v", args)
	return nil
}

// TestHostArgsUnattended pins the child argv of an unattended spawn against
// what the provider itself returns. Load-bearing rows: the three "unattended"
// rows show the mode reaches every launch shape (planned, autonomous,
// one-off), and "off" is the row that keeps today's sessions as they are: the
// same spec with the field clear must give exactly the plain provider argv.
// TestHostArgs above pins those plain bytes literally and is left untouched.
func TestHostArgsUnattended(t *testing.T) {
	plain, err := provider.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		spec HostSpawn
		want []string
	}{
		{
			name: "unattended planned session",
			spec: HostSpawn{Name: "billing", Worktree: "/wt/billing", Unattended: true},
			want: unattendedLaunch(t, "billing", false, false),
		},
		{
			name: "unattended autonomous session",
			spec: HostSpawn{Name: "billing", Worktree: "/wt/billing", Autonomous: true, Unattended: true},
			want: unattendedLaunch(t, "billing", false, true),
		},
		{
			name: "unattended one-off",
			spec: HostSpawn{Name: "billing", Worktree: "/wt/billing", Scratch: true, Unattended: true},
			want: unattendedLaunch(t, "billing", true, false),
		},
		{
			name: "off",
			spec: HostSpawn{Name: "billing", Worktree: "/wt/billing", Autonomous: true},
			want: plain.LaunchArgv("billing", false, true),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := hostArgs(tt.spec, "/sock")
			got := childOf(t, args)
			t.Logf("child argv: %q", got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("child argv = %#v\nwant %#v", got, tt.want)
			}
			// The mode changes the child argv only: the host's own flags are
			// the same with it on or off.
			off := tt.spec
			off.Unattended = false
			offArgs := hostArgs(off, "/sock")
			hostFlags := args[:len(args)-len(got)]
			offFlags := offArgs[:len(offArgs)-len(childOf(t, offArgs))]
			if !reflect.DeepEqual(hostFlags, offFlags) {
				t.Errorf("host flags changed with the mode:\n on  %#v\n off %#v", hostFlags, offFlags)
			}
			if line := childCommandLine(tt.spec); !strings.HasPrefix(line, got[0]) || strings.Count(line, `"`) != 2*(len(got)-1) {
				t.Errorf("childCommandLine = %q does not render the %d-word child argv", line, len(got))
			}
		})
	}
}

// TestResolveSpawnUnattended pins how SpawnHost settles a spawn's mode.
// Load-bearing rows: "marker, field clear" is the dashboard's relaunch of a
// stopped host, which builds a HostSpawn with only Name and Worktree: without
// that row passing, a daemon-run session relaunched from the dashboard would
// wait at a permission dialog nobody sees. "no marker, field clear" is every
// session that exists today and must come out untouched. The two "no mode"
// rows must be errors, never a plain launch, whichever way the mode was asked
// for.
func TestResolveSpawnUnattended(t *testing.T) {
	without := providerWithoutUnattended(t)
	const name = "relaunch"
	cases := []struct {
		name      string
		marker    bool
		field     bool
		provider  string
		want      bool
		wantError bool
	}{
		{name: "no marker, field clear", want: false},
		{name: "marker, field clear", marker: true, want: true},
		{name: "no marker, field set", field: true, want: true},
		{name: "marker and field", marker: true, field: true, want: true},
		{name: "provider with no mode, field clear and no marker", provider: without, want: false},
		{name: "provider with no mode, field set", provider: without, field: true, wantError: true},
		{name: "provider with no mode, marker", provider: without, marker: true, wantError: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			wt := t.TempDir()
			dir := filepath.Join(wt, ".afk", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.marker {
				if err := WriteUnattendedMarker(dir); err != nil {
					t.Fatal(err)
				}
			}
			spec := HostSpawn{Name: name, Worktree: wt, Provider: tt.provider, Unattended: tt.field}
			got, err := resolveSpawnUnattended(spec)
			if tt.wantError {
				if !errors.Is(err, provider.ErrUnattendedUnsupported) {
					t.Fatalf("resolveSpawnUnattended = %+v, err %v; want ErrUnattendedUnsupported", got, err)
				}
				t.Logf("refused with: %v", err)
				return
			}
			if err != nil {
				t.Fatalf("resolveSpawnUnattended: %v", err)
			}
			if got.Unattended != tt.want {
				t.Fatalf("Unattended = %v, want %v", got.Unattended, tt.want)
			}
			child := childOf(t, hostArgs(got, "/sock"))
			t.Logf("child argv: %q", child)
			p, err := provider.Lookup(tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			want := p.LaunchArgv(name, false, false)
			if tt.want {
				want = unattendedLaunch(t, name, false, false)
			}
			if !reflect.DeepEqual(child, want) {
				t.Errorf("child argv = %#v\nwant %#v", child, want)
			}
		})
	}
}

// TestSpawnHostCarriesTheUnattendedMarker runs the dashboard's relaunch for
// real: SpawnHost is given only Name and Worktree, the stand-in for the afk
// binary records the argv it was started with, and the child argv in it must
// be the provider's unattended launch when the marker exists and the plain
// one when it does not. TestResolveSpawnUnattended proves the resolution;
// this proves SpawnHost actually runs it before building the host command.
func TestSpawnHostCarriesTheUnattendedMarker(t *testing.T) {
	plain, err := provider.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	const name = "relaunch"
	for _, marker := range []bool{true, false} {
		t.Run(fmt.Sprintf("marker=%v", marker), func(t *testing.T) {
			shortTmpDir(t)
			wt := t.TempDir()
			dir := filepath.Join(wt, ".afk", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if marker {
				if err := WriteUnattendedMarker(dir); err != nil {
					t.Fatal(err)
				}
			}
			// Written to a temp name and renamed, so the file is whole the
			// moment it appears.
			record := filepath.Join(t.TempDir(), "argv")
			script := filepath.Join(t.TempDir(), "fake-afk")
			body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + record + ".tmp'\nmv '" + record + ".tmp' '" + record + "'\n"
			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			d := Deps{Executable: func() (string, error) { return script, nil }}
			if _, err := SpawnHost(HostSpawn{Name: name, Worktree: wt}, d); err != nil {
				t.Fatalf("SpawnHost: %v", err)
			}
			var raw []byte
			deadline := time.Now().Add(10 * time.Second)
			for {
				raw, err = os.ReadFile(record)
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the stand-in host never recorded its argv: %v", err)
				}
				time.Sleep(5 * time.Millisecond)
			}
			child := childOf(t, strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n"))
			t.Logf("child argv handed to the host: %q", child)
			want := plain.LaunchArgv(name, false, false)
			if marker {
				want = unattendedLaunch(t, name, false, false)
			}
			if !reflect.DeepEqual(child, want) {
				t.Fatalf("child argv = %#v\nwant %#v", child, want)
			}
		})
	}
}

// TestSpawnHostRefusesUnattendedWithoutAMode: a direct spawn of an unattended
// session under a provider with no unattended mode fails with a plain message
// before the afk binary is even located, so no host process is started.
// Both rows are load-bearing: "field" is a caller asking outright, "marker"
// is a relaunch of a session whose files say it runs unattended.
func TestSpawnHostRefusesUnattendedWithoutAMode(t *testing.T) {
	without := providerWithoutUnattended(t)
	const name = "relaunch"
	for _, how := range []string{"field", "marker"} {
		t.Run(how, func(t *testing.T) {
			wt := t.TempDir()
			dir := filepath.Join(wt, ".afk", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			// The provider comes from its sidecar, as it does on a relaunch.
			if err := WriteProviderMarker(dir, without); err != nil {
				t.Fatal(err)
			}
			spec := HostSpawn{Name: name, Worktree: wt}
			if how == "field" {
				spec.Unattended = true
			} else if err := WriteUnattendedMarker(dir); err != nil {
				t.Fatal(err)
			}
			called := 0
			d := Deps{Executable: func() (string, error) { called++; return exec.LookPath("true") }}
			h, err := SpawnHost(spec, d)
			if !errors.Is(err, provider.ErrUnattendedUnsupported) {
				t.Fatalf("SpawnHost = %+v, err %v; want ErrUnattendedUnsupported", h, err)
			}
			t.Logf("SpawnHost refused with: %v", err)
			for _, want := range []string{name, without, "unattended"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if called != 0 {
				t.Errorf("Executable called %d times; the refusal must come before anything is forked", called)
			}
			if h.PID != 0 {
				t.Errorf("a host process was started (pid %d)", h.PID)
			}
		})
	}
}

// TestBuildRejectsUnattendedWithoutAMode: Build's promise is that an error
// aborts before any side effect. For a provider with no unattended mode that
// means no git command at all is recorded (no branch, no worktree) and
// nothing is written, in every mode including dry-run. The "supported" row is
// the control: the same config under the default provider must build.
func TestBuildRejectsUnattendedWithoutAMode(t *testing.T) {
	without := providerWithoutUnattended(t)
	cases := []struct {
		name string
		cfg  Config
	}{
		{"planned", Config{}},
		{"autonomous", Config{Autonomous: true}},
		{"one-off", Config{Scratch: true}},
		{"dry-run", Config{DryRun: true}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			cfg := tt.cfg
			cfg.Name, cfg.RepoDir, cfg.Provider, cfg.Unattended = "no-mode", repo, without, true
			p, err := Build(cfg, d)
			if !errors.Is(err, provider.ErrUnattendedUnsupported) {
				t.Fatalf("Build = %v, err %v; want ErrUnattendedUnsupported", p, err)
			}
			t.Logf("Build refused with: %v", err)
			if len(fr.calls) != 0 {
				t.Errorf("Build ran commands before refusing: %+v", fr.calls)
			}
			if entries, _ := os.ReadDir(repo); len(entries) != 0 {
				t.Errorf("Build left files behind: %v", entries)
			}
			// Create is Build then Run, so it must refuse the same way.
			if err := Create(cfg, d); !errors.Is(err, provider.ErrUnattendedUnsupported) {
				t.Errorf("Create err = %v, want ErrUnattendedUnsupported", err)
			}
		})
	}
	t.Run("supported", func(t *testing.T) {
		fr := &fakeRunner{}
		d := baseDeps(t, fr)
		if _, err := Build(Config{Name: "has-mode", RepoDir: t.TempDir(), Unattended: true}, d); err != nil {
			t.Fatalf("Build under the default provider: %v", err)
		}
	})
}

// TestUnattendedStepOrder asserts, on the assembled steps, that the marker is
// its own step after the session directory exists and before the host
// launch, in both creation modes. Load-bearing: the order (every spawn after
// the first has only the marker to go by, so it must be on disk before a host
// exists) and the absence rows, since a plan without the mode must gain no
// step.
func TestUnattendedStepOrder(t *testing.T) {
	const name = "unatt-order"
	const markerDesc = "Write unattended marker .afk/" + name + "/unattended"
	for _, scratch := range []bool{false, true} {
		for _, unattended := range []bool{true, false} {
			t.Run(fmt.Sprintf("scratch=%v/unattended=%v", scratch, unattended), func(t *testing.T) {
				fr := &fakeRunner{}
				d := baseDeps(t, fr)
				p, err := Build(Config{Name: name, RepoDir: t.TempDir(), Scratch: scratch, Unattended: unattended}, d)
				if err != nil {
					t.Fatalf("Build: %v", err)
				}
				after := "Seed .afk/" + name + "/status.json"
				if scratch {
					after = "for the session host's files"
				}
				dir := stepIndex(p.Steps, after)
				marker := stepIndex(p.Steps, markerDesc)
				host := stepIndex(p.Steps, "Launch detached session host")
				if dir < 0 || host < 0 {
					t.Fatalf("dir=%d host=%d; steps missing", dir, host)
				}
				if !unattended {
					if marker != -1 {
						t.Fatalf("marker step present without Unattended at %d", marker)
					}
					return
				}
				if !(dir < marker && marker < host) {
					t.Fatalf("order dir=%d marker=%d host=%d, want dir < marker < host", dir, marker, host)
				}
				if p.Steps[marker].undo != nil {
					t.Errorf("marker step has an undo; rollback removes the session dir and the marker with it")
				}
			})
		}
	}
}

// TestCreateUnattended runs the plan, live and as a dry run. Load-bearing:
// live, the marker exists exactly when Unattended is set (and the autonomous
// marker is independent of it); dry, the plan lists the marker write and the
// launch line is the provider's unattended argv, and nothing is created. The
// "off" rows print exactly the plain launch line.
func TestCreateUnattended(t *testing.T) {
	const name = "unatt-live"
	quoted := func(argv []string) string {
		parts := []string{argv[0]}
		for _, a := range argv[1:] {
			parts = append(parts, fmt.Sprintf("%q", a))
		}
		return strings.Join(parts, " ")
	}
	plain, err := provider.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		unattended bool
		autonomous bool
		dry        bool
	}{
		{name: "live unattended", unattended: true},
		{name: "live unattended autonomous", unattended: true, autonomous: true},
		{name: "live off"},
		{name: "live autonomous only", autonomous: true},
		{name: "dry unattended", unattended: true, dry: true},
		{name: "dry off", dry: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			shortTmpDir(t)
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			var out strings.Builder
			d.Stdout = &out
			cfg := Config{Name: name, RepoDir: repo, Unattended: tt.unattended, Autonomous: tt.autonomous, DryRun: tt.dry}
			if err := Create(cfg, d); err != nil {
				t.Fatalf("Create: %v", err)
			}
			printed := out.String()
			want := plain.LaunchArgv(name, false, tt.autonomous)
			if tt.unattended {
				want = unattendedLaunch(t, name, false, tt.autonomous)
			}
			launch := "Launch detached session host running: " + quoted(want) + "\n"
			if !strings.Contains(printed, launch) {
				t.Errorf("launch line missing %q:\n%s", launch, printed)
			}
			markerLine := "Write unattended marker .afk/" + name + "/unattended"
			if got := strings.Contains(printed, markerLine); got != tt.unattended {
				t.Errorf("marker line present = %v, want %v:\n%s", got, tt.unattended, printed)
			}
			if tt.dry {
				if _, err := os.Stat(filepath.Join(repo, ".afk")); !os.IsNotExist(err) {
					t.Errorf(".afk created during dry-run (err=%v)", err)
				}
				if _, err := os.Stat(filepath.Join(repo, ".claude")); !os.IsNotExist(err) {
					t.Errorf("a worktree folder was created during dry-run (err=%v)", err)
				}
				return
			}
			wt := filepath.Join(repo, ".claude", "worktrees", name)
			if got := IsUnattended(wt, name); got != tt.unattended {
				t.Errorf("IsUnattended = %v, want %v", got, tt.unattended)
			}
			if got := IsAutonomous(wt, name); got != tt.autonomous {
				t.Errorf("IsAutonomous = %v, want %v", got, tt.autonomous)
			}
		})
	}
}

// TestBuildHostCmdStdioAndSetsid is the compile-time half of the TUI-freeze
// tripwire (W5.T2 owns the runtime half): all three streams must be the same
// explicit *os.File, never nil (which would inherit) and never a pipe-backed
// io.Writer (which would make the spawner's Wait block on EOF).
func TestBuildHostCmdStdioAndSetsid(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer null.Close()

	spec := HostSpawn{Name: "sess", Worktree: "/wt"}
	cmd := buildHostCmd("/usr/local/bin/afk", spec, "/sock", null)

	if cmd.Path != "/usr/local/bin/afk" {
		t.Errorf("Path = %q, want the resolved afk binary", cmd.Path)
	}
	wantArgs := append([]string{"/usr/local/bin/afk"}, hostArgs(spec, "/sock")...)
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Errorf("Args = %#v\nwant %#v", cmd.Args, wantArgs)
	}
	if cmd.Dir != "/wt" {
		t.Errorf("Dir = %q, want the session worktree", cmd.Dir)
	}
	for _, s := range []struct {
		field string
		got   any
	}{{"Stdin", cmd.Stdin}, {"Stdout", cmd.Stdout}, {"Stderr", cmd.Stderr}} {
		if s.got == nil {
			t.Errorf("%s is nil: the host would inherit the spawner's descriptor", s.field)
			continue
		}
		f, ok := s.got.(*os.File)
		if !ok {
			t.Errorf("%s is %T, want *os.File (anything else makes exec.Cmd build a pipe)", s.field, s.got)
			continue
		}
		if f != null {
			t.Errorf("%s = %v, want the /dev/null file", s.field, f.Name())
		}
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Errorf("SysProcAttr = %+v, want Setsid true", cmd.SysProcAttr)
	}
	if cmd.Process != nil {
		t.Error("buildHostCmd started the process; it must only populate fields")
	}
}

func TestSpawnHostValidatesSpec(t *testing.T) {
	tests := []struct {
		name string
		spec HostSpawn
		want string
	}{
		{"missing name", HostSpawn{Worktree: "/wt"}, "session name is required"},
		{"missing worktree", HostSpawn{Name: "sess"}, "worktree is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Deps{Executable: func() (string, error) {
				t.Fatal("Executable must not be consulted for an invalid spec")
				return "", nil
			}}
			if _, err := SpawnHost(tt.spec, d); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SpawnHost error = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestSpawnHostUsesTheExecutableSeam proves the seam W5.T2's fd-inheritance
// test depends on: SpawnHost resolves the binary through Deps.Executable and
// nothing else, and it surfaces that lookup's failure verbatim.
func TestSpawnHostUsesTheExecutableSeam(t *testing.T) {
	shortTmpDir(t)
	wt := t.TempDir()

	called := 0
	d := Deps{Executable: func() (string, error) {
		called++
		return "", errors.New("no afk binary here")
	}}
	_, err := SpawnHost(HostSpawn{Name: "sess", Worktree: wt}, d)
	if err == nil || !strings.Contains(err.Error(), "no afk binary here") {
		t.Fatalf("SpawnHost error = %v, want the Executable failure", err)
	}
	if called != 1 {
		t.Errorf("Executable called %d times, want 1", called)
	}

	d.Executable = func() (string, error) { return "", nil }
	if _, err := SpawnHost(HostSpawn{Name: "sess", Worktree: wt}, d); err == nil || !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("SpawnHost error = %v, want the empty-path rejection", err)
	}
}

func TestSpawnHostStartsADetachedProcess(t *testing.T) {
	shortTmpDir(t)
	wt := t.TempDir()

	d := Deps{Executable: harmlessExecutable()}
	h, err := SpawnHost(HostSpawn{Name: "sess", Worktree: wt}, d)
	if err != nil {
		t.Fatalf("SpawnHost: %v", err)
	}
	if h.PID <= 0 {
		t.Errorf("PID = %d, want the spawned process's pid", h.PID)
	}
	if h.PID == os.Getpid() {
		t.Error("PID is our own pid: nothing was forked")
	}
	wantSocket, err := host.SocketPath(wt, "sess")
	if err != nil {
		t.Fatalf("host.SocketPath: %v", err)
	}
	if h.Socket != wantSocket {
		t.Errorf("Socket = %q, want host.SocketPath(worktree, name) = %q", h.Socket, wantSocket)
	}
	if h.LogPath != filepath.Join(wt, ".afk", "sess", "host.log") {
		t.Errorf("LogPath = %q", h.LogPath)
	}
	if h.MetaPath != filepath.Join(wt, ".afk", "sess", "host.json") {
		t.Errorf("MetaPath = %q", h.MetaPath)
	}
}

// TestSpawnHostReapsAnExitedHost pins the background-Wait discipline: the
// stand-in host (`true`) exits immediately, and SpawnHost's reaper must
// collect it. With a bare Release() instead of a Wait, the exited host stays
// a zombie of this (long-lived) process, signal-0 liveness never goes false,
// and `afk clean` aborts with "still alive after SIGKILL" — the exact
// regression this test exists to catch.
func TestSpawnHostReapsAnExitedHost(t *testing.T) {
	shortTmpDir(t)
	wt := t.TempDir()

	h, err := SpawnHost(HostSpawn{Name: "sess", Worktree: wt}, Deps{Executable: harmlessExecutable()})
	if err != nil {
		t.Fatalf("SpawnHost: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for host.Alive(h.PID) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still signal-0 alive after 5s: the exited host was never reaped (zombie)", h.PID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStopSpawnedHost(t *testing.T) {
	tests := []struct {
		name        string
		h           HostProcess
		dialErr     error
		sigErr      error
		wantDialed  bool
		wantSignals int
		wantErr     string
	}{
		{
			name:       "socket exit succeeds, no signal",
			h:          HostProcess{PID: 4321, Socket: "/sock"},
			wantDialed: true,
		},
		{
			name:        "socket exit fails, SIGTERM fallback",
			h:           HostProcess{PID: 4321, Socket: "/sock"},
			dialErr:     errors.New("connection refused"),
			wantDialed:  true,
			wantSignals: 1,
		},
		{
			name:        "no socket recorded, straight to SIGTERM",
			h:           HostProcess{PID: 4321},
			wantSignals: 1,
		},
		{
			name:       "no socket and no pid is an error",
			h:          HostProcess{},
			wantErr:    "no socket path",
			wantDialed: false,
		},
		{
			name:        "both paths failing reports both",
			h:           HostProcess{PID: 4321, Socket: "/sock"},
			dialErr:     errors.New("connection refused"),
			sigErr:      errors.New("no such process"),
			wantDialed:  true,
			wantSignals: 1,
			wantErr:     "no such process",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctl := &fakeHostCtl{dialErr: tt.dialErr, sigErr: tt.sigErr}
			d := Deps{DialExit: ctl.dialExit, SignalKill: ctl.signalKill}.withDefaults()
			err := stopSpawnedHost(d, tt.h)

			if tt.wantErr == "" && err != nil {
				t.Fatalf("stopSpawnedHost = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("stopSpawnedHost = %v, want an error containing %q", err, tt.wantErr)
			}
			if got := len(ctl.dialed) > 0; got != tt.wantDialed {
				t.Errorf("dialed = %v, want %v (dials: %v)", got, tt.wantDialed, ctl.dialed)
			}
			if len(ctl.signals) != tt.wantSignals {
				t.Fatalf("signals = %+v, want %d", ctl.signals, tt.wantSignals)
			}
			for _, s := range ctl.signals {
				if s.pid != tt.h.PID {
					t.Errorf("signalled pid %d, want %d", s.pid, tt.h.PID)
				}
				if s.sig != syscall.SIGTERM {
					t.Errorf("sent %v, want SIGTERM (never SIGKILL: the host is an unreaped child)", s.sig)
				}
			}
		})
	}
}

// TestLaunchStepUndoStopsTheHost covers the rollback the launch step carries
// for ordering safety: launch is last today, so the undo is reached only by
// appending a failing step after it — exactly what a future step would do.
func TestLaunchStepUndoStopsTheHost(t *testing.T) {
	shortTmpDir(t)
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	ctl := &fakeHostCtl{}
	d.DialExit = ctl.dialExit
	d.SignalKill = ctl.signalKill
	var errOut strings.Builder
	d.Stderr = &errOut

	p, err := Build(Config{Name: "undome", RepoDir: repo}, d)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p.Steps = append(p.Steps, Step{
		Desc: "a later step that fails",
		run:  func() error { return &exitError{} },
	})
	if err := p.Run(); err == nil {
		t.Fatal("Run should have failed on the appended step")
	}

	if len(ctl.dialed) != 1 {
		t.Fatalf("rollback dialed %v, want exactly one socket Exit", ctl.dialed)
	}
	wantSocket, err := host.SocketPath(p.WorktreeTarget, "undome")
	if err != nil {
		t.Fatalf("host.SocketPath: %v", err)
	}
	if ctl.dialed[0] != wantSocket {
		t.Errorf("rollback dialed %q, want the spawned host's socket %q", ctl.dialed[0], wantSocket)
	}
	if len(ctl.signals) != 0 {
		t.Errorf("rollback signalled %+v despite the socket Exit succeeding", ctl.signals)
	}
	// The worktree/branch rollback must still run after the host is stopped.
	if !fr.sawCommand("git", "worktree remove --force") {
		t.Errorf("rollback did not remove the worktree; calls: %+v", fr.calls)
	}
}

// --- Exported seams for internal/ui -------------------------------------

// TestTrunkOf covers the exported trunk-identification seam. Load-bearing:
// the nil-parent row ensures internal/ui can trust the same trunk logic the CLI
// uses without reimplementing it.
func TestTrunkOf(t *testing.T) {
	cases := []struct {
		name  string
		stack []spice.Branch
		want  string
	}{
		{
			name: "trunk has no parent",
			stack: []spice.Branch{
				{Name: "main"},
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
			},
			want: "main",
		},
		{
			name: "trunk deep in stack",
			stack: []spice.Branch{
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
				{Name: "feat-b", Down: &spice.BranchRef{Name: "feat-a"}},
				{Name: "main"},
			},
			want: "main",
		},
		{
			name: "fallback to main by name",
			stack: []spice.Branch{
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
				{Name: "main", Down: &spice.BranchRef{Name: "legacy"}},
			},
			want: "main",
		},
		{
			name: "fallback to master by name",
			stack: []spice.Branch{
				{Name: "feat-a", Down: &spice.BranchRef{Name: "master"}},
				{Name: "master", Down: &spice.BranchRef{Name: "legacy"}},
			},
			want: "master",
		},
		{
			name: "fallback to first when no trunk found",
			stack: []spice.Branch{
				{Name: "develop", Down: &spice.BranchRef{Name: "legacy"}},
				{Name: "feat-a", Down: &spice.BranchRef{Name: "develop"}},
			},
			want: "develop",
		},
		{
			name:  "empty stack returns main",
			stack: []spice.Branch{},
			want:  "main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TrunkOf(tc.stack)
			if got != tc.want {
				t.Errorf("TrunkOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBranchOptions covers the exported picker-options seam. Load-bearing:
// trunk-first ordering must hold for internal/ui so its form offers branches
// identically to the CLI.
func TestBranchOptions(t *testing.T) {
	cases := []struct {
		name  string
		stack []spice.Branch
		trunk string
		want  []string
	}{
		{
			name: "trunk first, then stack order",
			stack: []spice.Branch{
				{Name: "main"},
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
				{Name: "feat-b", Down: &spice.BranchRef{Name: "feat-a"}},
			},
			trunk: "main",
			want:  []string{"main", "feat-a", "feat-b"},
		},
		{
			name: "trunk not first in stack input",
			stack: []spice.Branch{
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
				{Name: "main"},
				{Name: "feat-b", Down: &spice.BranchRef{Name: "feat-a"}},
			},
			trunk: "main",
			want:  []string{"main", "feat-a", "feat-b"},
		},
		{
			name: "trunk only",
			stack: []spice.Branch{
				{Name: "main"},
			},
			trunk: "main",
			want:  []string{"main"},
		},
		{
			name:  "empty stack",
			stack: []spice.Branch{},
			trunk: "main",
			want:  []string{"main"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BranchOptions(tc.stack, tc.trunk)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("BranchOptions = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOrderTrunkFirst covers the plain-git trunk-first reordering seam.
// Load-bearing: the trunk-already-first row must confirm it is not duplicated
// when already at the front, and the trunk-absent row must add it unconditionally.
func TestOrderTrunkFirst(t *testing.T) {
	cases := []struct {
		name  string
		names []string
		trunk string
		want  []string
	}{
		{
			name:  "trunk not first",
			names: []string{"feat-a", "feat-b", "main"},
			trunk: "main",
			want:  []string{"main", "feat-a", "feat-b"},
		},
		{
			name:  "trunk already first",
			names: []string{"main", "feat-a", "feat-b"},
			trunk: "main",
			want:  []string{"main", "feat-a", "feat-b"},
		},
		{
			name:  "trunk absent from list",
			names: []string{"feat-a", "feat-b"},
			trunk: "main",
			want:  []string{"main", "feat-a", "feat-b"},
		},
		{
			name:  "empty input",
			names: []string{},
			trunk: "main",
			want:  []string{"main"},
		},
		{
			name:  "trunk duplicated in input",
			names: []string{"main", "feat-a", "main", "feat-b"},
			trunk: "main",
			want:  []string{"main", "feat-a", "feat-b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OrderTrunkFirst(tc.names, tc.trunk)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("OrderTrunkFirst = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBranchesPlain covers the exported plain-git branch lister. Load-bearing:
// the zero-Deps row ensures the withDefaults guard makes BranchesPlain safe to
// call from internal/ui with a zero Deps, which is exactly the shape the
// dashboard will invoke.
func TestBranchesPlain(t *testing.T) {
	cases := []struct {
		name       string
		deps       Deps
		handler    func(dir, name string, args ...string) (string, error)
		wantBranch string
		wantErr    bool
	}{
		{
			name: "normal multi-branch output",
			deps: Deps{Run: nil},
			handler: func(dir, name string, args ...string) (string, error) {
				if name == "git" && strings.Join(args, " ") == "for-each-ref --format=%(refname:short) refs/heads/" {
					return "main\nfeat-a\nfeat-b\n", nil
				}
				return "", &exitError{}
			},
			wantBranch: "feat-a",
		},
		{
			name: "output with trailing and blank lines",
			deps: Deps{Run: nil},
			handler: func(dir, name string, args ...string) (string, error) {
				if name == "git" && strings.Join(args, " ") == "for-each-ref --format=%(refname:short) refs/heads/" {
					return "main\n\nfeat-a\n\n\n", nil
				}
				return "", &exitError{}
			},
			wantBranch: "feat-a",
		},
		{
			name: "empty output",
			deps: Deps{Run: nil},
			handler: func(dir, name string, args ...string) (string, error) {
				if name == "git" && strings.Join(args, " ") == "for-each-ref --format=%(refname:short) refs/heads/" {
					return "", nil
				}
				return "", &exitError{}
			},
			wantBranch: "",
		},
		{
			name: "run error",
			deps: Deps{Run: nil},
			handler: func(dir, name string, args ...string) (string, error) {
				return "", errors.New("fatal: not a git repository")
			},
			wantErr: true,
		},
		{
			name:    "zero-value Deps",
			deps:    Deps{},
			handler: nil, // never called
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.deps
			if tc.handler != nil {
				d.Run = tc.handler
			}
			repo := t.TempDir()
			branches, err := BranchesPlain(d, repo)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("BranchesPlain = %v, nil; want error", branches)
				}
				return
			}
			if err != nil {
				t.Fatalf("BranchesPlain error = %v, want nil", err)
			}
			if tc.wantBranch != "" {
				found := false
				for _, b := range branches {
					if b == tc.wantBranch {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("BranchesPlain = %v, want to contain %q", branches, tc.wantBranch)
				}
			} else {
				if len(branches) != 0 {
					t.Errorf("BranchesPlain = %v, want empty", branches)
				}
			}
		})
	}
}

// TestResolveBasePlainGitPaths covers the plain-git half of resolveBase
// (git-spice rows already in TestResolveBasePickerInvokedWhenInteractive).
// Load-bearing: picker-received options must be trunk-first, picker-received
// dflt must equal trunk, DryRun never prompts, BranchesPlain error degrades to
// trunk without failing session creation.
func TestResolveBasePlainGitPaths(t *testing.T) {
	cases := []struct {
		name              string
		cfg               Config
		deps              Deps
		handler           func(dir, name string, args ...string) (string, error)
		wantBase          string
		wantPickerOptions []string
		wantPickerDefault string
		wantErr           bool
	}{
		{
			name: "explicit --base wins",
			cfg:  Config{Base: "develop", DryRun: false},
			deps: Deps{
				PickBase: func(string, []string, string) (string, error) {
					t.Fatal("PickBase must not be called when --base is explicit")
					return "", nil
				},
			},
			wantBase: "develop",
		},
		{
			name: "PickBase nil yields trunk",
			cfg:  Config{DryRun: false},
			deps: Deps{
				PickBase: nil,
			},
			handler: func(dir, name string, args ...string) (string, error) {
				joined := strings.Join(args, " ")
				if name == "git" && strings.HasPrefix(joined, "rev-parse --verify --quiet refs/heads/main") {
					return "abc123\n", nil
				}
				return "", &exitError{}
			},
			wantBase: "main",
		},
		{
			name: "PickBase set and non-dry-run calls picker",
			cfg:  Config{DryRun: false},
			deps: Deps{
				PickBase: func(prompt string, options []string, dflt string) (string, error) {
					return "feat-b", nil
				},
			},
			handler: func(dir, name string, args ...string) (string, error) {
				joined := strings.Join(args, " ")
				switch {
				case name == "git" && joined == "for-each-ref --format=%(refname:short) refs/heads/":
					return "feat-a\nmain\nfeat-b\n", nil
				case name == "git" && strings.HasPrefix(joined, "rev-parse --verify --quiet refs/heads/main"):
					return "abc123\n", nil
				default:
					return "", &exitError{}
				}
			},
			wantBase:          "feat-b",
			wantPickerOptions: []string{"main", "feat-a", "feat-b"},
			wantPickerDefault: "main",
		},
		{
			name: "picker returning empty falls back to trunk",
			cfg:  Config{DryRun: false},
			deps: Deps{
				PickBase: func(prompt string, options []string, dflt string) (string, error) {
					return "", nil
				},
			},
			handler: func(dir, name string, args ...string) (string, error) {
				joined := strings.Join(args, " ")
				switch {
				case name == "git" && joined == "for-each-ref --format=%(refname:short) refs/heads/":
					return "main\nfeat-a\n", nil
				case name == "git" && strings.HasPrefix(joined, "rev-parse --verify --quiet refs/heads/main"):
					return "abc123\n", nil
				default:
					return "", &exitError{}
				}
			},
			wantBase:          "main",
			wantPickerOptions: []string{"main", "feat-a"},
			wantPickerDefault: "main",
		},
		{
			name: "DryRun never prompts",
			cfg:  Config{DryRun: true},
			deps: Deps{
				PickBase: func(string, []string, string) (string, error) {
					t.Fatal("PickBase must not be called during dry-run")
					return "", nil
				},
			},
			handler: func(dir, name string, args ...string) (string, error) {
				if name == "git" && strings.HasPrefix(strings.Join(args, " "), "rev-parse --verify --quiet refs/heads/main") {
					return "abc123\n", nil
				}
				return "", &exitError{}
			},
			wantBase: "main",
		},
		{
			name: "BranchesPlain error degrades to trunk",
			cfg:  Config{DryRun: false},
			deps: Deps{
				PickBase: func(string, []string, string) (string, error) {
					t.Fatal("PickBase must not be called when BranchesPlain fails")
					return "", nil
				},
			},
			handler: func(dir, name string, args ...string) (string, error) {
				joined := strings.Join(args, " ")
				switch {
				case name == "git" && joined == "for-each-ref --format=%(refname:short) refs/heads/":
					return "", errors.New("fatal: not a git repository")
				case name == "git" && strings.HasPrefix(joined, "rev-parse --verify --quiet refs/heads/main"):
					return "abc123\n", nil
				default:
					return "", &exitError{}
				}
			},
			wantBase: "main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			d := tc.deps
			if tc.handler != nil {
				d.Run = tc.handler
			}
			if d.Run == nil {
				d.Run = func(dir, name string, args ...string) (string, error) {
					joined := strings.Join(args, " ")
					if name == "git" && strings.HasPrefix(joined, "rev-parse --verify --quiet refs/heads/main") {
						return "abc123\n", nil
					}
					return "", &exitError{}
				}
			}
			// Capture picker calls
			var gotOptions []string
			var gotDefault string
			if d.PickBase != nil {
				orig := d.PickBase
				d.PickBase = func(prompt string, options []string, dflt string) (string, error) {
					gotOptions = options
					gotDefault = dflt
					return orig(prompt, options, dflt)
				}
			}
			d = d.withDefaults()

			base, desc, _, err := resolveBase(tc.cfg, d, repo, false)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveBase = %q, nil; want error", base)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBase error = %v, want nil", err)
			}
			if base != tc.wantBase {
				t.Errorf("resolveBase base = %q, want %q", base, tc.wantBase)
			}
			if !strings.Contains(desc, base) {
				t.Errorf("resolveBase desc = %q, does not contain base %q", desc, base)
			}
			if tc.wantPickerOptions != nil {
				if !reflect.DeepEqual(gotOptions, tc.wantPickerOptions) {
					t.Errorf("picker received options = %v, want %v", gotOptions, tc.wantPickerOptions)
				}
			}
			if tc.wantPickerDefault != "" {
				if gotDefault != tc.wantPickerDefault {
					t.Errorf("picker received default = %q, want %q", gotDefault, tc.wantPickerDefault)
				}
			}
		})
	}
}

// TestTrunkPlainExported checks the exported wrapper delegates and survives a
// zero Deps. internal/ui reaches it with session.Deps{}, so the withDefaults
// call inside is what keeps that call site from nil-dereferencing on d.Run.
func TestTrunkPlainExported(t *testing.T) {
	t.Run("zero Deps does not panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("TrunkPlain panicked with a zero Deps: %v", r)
			}
		}()
		if _, err := TrunkPlain(Deps{}, t.TempDir()); err == nil {
			t.Log("no error in a non-repo dir is acceptable; the point is that it did not panic")
		}
	})

	t.Run("delegates to trunkPlain", func(t *testing.T) {
		d := Deps{Run: func(dir, name string, args ...string) (string, error) {
			if len(args) > 1 && args[0] == "symbolic-ref" {
				return "origin/master\n", nil
			}
			return "", fmt.Errorf("unexpected: %v", args)
		}}
		got, err := TrunkPlain(d, "/repo")
		if err != nil {
			t.Fatalf("TrunkPlain: %v", err)
		}
		if got != "master" {
			t.Errorf("TrunkPlain = %q, want %q (origin/ prefix stripped)", got, "master")
		}
	})
}

// --- one-off (scratch) sessions -----------------------------------------

// TestHostArgsScratch pins the child argv each session kind gets out of the
// one pure argv seam. Load-bearing rows: "scratch" is the entire point of the
// mode — the child must be exactly `claude`, with no second element naming a
// plan that does not exist — and "normal" is the regression guard that adding
// the mode left the conductor's argv byte-identical, since every existing
// session's launch depends on it.
func TestHostArgsScratch(t *testing.T) {
	cases := []struct {
		name      string
		spec      HostSpawn
		wantChild []string
	}{
		{
			name:      "scratch",
			spec:      HostSpawn{Name: "scratch-1", Worktree: "/repo", Scratch: true},
			wantChild: []string{"claude"},
		},
		{
			name:      "normal",
			spec:      HostSpawn{Name: "scratch-1", Worktree: "/repo"},
			wantChild: []string{"claude", "/afk:go --session scratch-1"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			args := hostArgs(tt.spec, "/sock")
			sep := -1
			for i, a := range args {
				if a == "--" {
					sep = i
					break
				}
			}
			if sep < 0 {
				t.Fatalf("hostArgs has no `--` separator: %v", args)
			}
			if got := args[sep+1:]; !reflect.DeepEqual(got, tt.wantChild) {
				t.Errorf("child argv = %#v, want %#v", got, tt.wantChild)
			}
			// The flag half is mode-independent: only the child argv differs,
			// so a scratch host is addressed, logged and metadata-tracked
			// exactly like any other.
			wantFlags := []string{
				"_host",
				"--name", "scratch-1",
				"--worktree", "/repo",
				"--socket", "/sock",
				"--log", filepath.Join("/repo", ".afk", "scratch-1", "host.log"),
				"--meta", filepath.Join("/repo", ".afk", "scratch-1", "host.json"),
			}
			if got := args[:sep]; !reflect.DeepEqual(got, wantFlags) {
				t.Errorf("flag argv = %#v, want %#v", got, wantFlags)
			}
		})
	}
}

// scratchForbiddenInDesc are the substrings no step of a one-off plan may
// mention. They are asserted against the rendered step descriptions, not just
// against the executed commands, because `--dry-run` prints exactly these
// lines: a scratch action list that says "worktree" or "status.json" is a
// lie about what the plan will do, whether or not a command follows.
var scratchForbiddenInDesc = []string{"worktree", "branch", "status.json"}

// TestScratchPlanSteps checks the step list Build assembles. Load-bearing
// rows: "scratch" must contain no branch step, no worktree step and no
// status-seed step — the three things a one-off is defined by not having —
// and "normal" is the guard that the ordinary plan still contains all three.
func TestScratchPlanSteps(t *testing.T) {
	cases := []struct {
		name    string
		scratch bool
		// wantAbsent are substrings that must not appear in any step's Desc.
		wantAbsent []string
		// wantPresent are substrings that must each appear in some step's Desc.
		wantPresent []string
	}{
		{
			name:        "scratch",
			scratch:     true,
			wantAbsent:  scratchForbiddenInDesc,
			wantPresent: []string{"Launch detached session host", "one-off"},
		},
		{
			name:        "normal",
			scratch:     false,
			wantPresent: []string{"worktree", "branch", "status.json", "Launch detached session host"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			p, err := Build(Config{Name: "scratch-1", RepoDir: repo, Scratch: tt.scratch}, d)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			var joined string
			for _, s := range p.Steps {
				joined += s.Desc + "\n"
			}
			lower := strings.ToLower(joined)
			for _, bad := range tt.wantAbsent {
				if strings.Contains(lower, strings.ToLower(bad)) {
					t.Errorf("step list mentions %q:\n%s", bad, joined)
				}
			}
			for _, want := range tt.wantPresent {
				if !strings.Contains(lower, strings.ToLower(want)) {
					t.Errorf("step list is missing %q:\n%s", want, joined)
				}
			}
		})
	}
}

// TestScratchBuildResolvesNothing pins the read-only half of a one-off Build:
// with no branch, no worktree and no base to resolve, none of the probes run
// and no base picker can ever be reached from this path. The ONE command it is
// allowed is the read-only `git worktree list --porcelain` of the collision
// check — asserted exactly, so anything more (a probe, a mutation) fails here.
func TestScratchBuildResolvesNothing(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	d.DetectGitSpice = func(string) bool {
		t.Fatal("scratch Build probed git-spice")
		return false
	}
	d.HasWorktrunk = func() bool {
		t.Fatal("scratch Build probed worktrunk")
		return false
	}
	d.AfkPreflight = func(string) (bool, string) {
		t.Fatal("scratch Build ran the /afk:go preflight")
		return false, ""
	}
	d.PickBase = func(string, []string, string) (string, error) {
		t.Fatal("scratch Build offered a base picker")
		return "", nil
	}
	p, err := Build(Config{Name: "scratch-1", RepoDir: repo, Scratch: true}, d)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(fr.calls) != 1 || fr.calls[0].name != "git" || strings.Join(fr.calls[0].args, " ") != "worktree list --porcelain" {
		t.Errorf("scratch Build commands = %+v, want exactly one read-only `git worktree list --porcelain`", fr.calls)
	}
	if p.Base != "" {
		t.Errorf("Base = %q, want empty (a one-off has no base)", p.Base)
	}
	if p.WorktreeTarget != "" {
		t.Errorf("WorktreeTarget = %q, want empty", p.WorktreeTarget)
	}
}

// TestScratchCreate covers the executed one-off flow end to end. Load-bearing:
// the no-status.json assertion is the whole design premise (a one-off is
// discovered through host.json instead, so seeding a phase it does not have
// would put a lie in an externally-owned schema), and the description row
// proves free text still lands on disk without ever reaching an argv.
func TestScratchCreate(t *testing.T) {
	const secret = "poke at this; rm -rf / $(whoami) `id`"
	cases := []struct {
		name        string
		description string
	}{
		{name: "no description"},
		{name: "with description", description: secret},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			shortTmpDir(t)
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			var out strings.Builder
			d.Stdout = &out

			cfg := Config{Name: "scratch-1", RepoDir: repo, Scratch: true, Description: tt.description}
			if err := Create(cfg, d); err != nil {
				t.Fatalf("Create: %v", err)
			}

			// No wt and no mutating git: a one-off touches no repository
			// state at all — the only command across the whole Create is
			// Build's read-only collision enumeration.
			onlyWorktreeListCalls(t, fr)
			if _, err := os.Stat(filepath.Join(repo, ".claude", "worktrees")); !os.IsNotExist(err) {
				t.Errorf("scratch create made a worktree root (err=%v)", err)
			}

			// The session directory exists (the host writes host.json and
			// host.log into it) but carries no status.json.
			dir := filepath.Join(repo, ".afk", "scratch-1")
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				t.Fatalf("session dir %s: stat=%v err=%v", dir, info, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "status.json")); !os.IsNotExist(err) {
				t.Errorf("scratch create seeded a status.json (err=%v)", err)
			}

			descPath := filepath.Join(dir, "description.md")
			if tt.description == "" {
				if _, err := os.Stat(descPath); !os.IsNotExist(err) {
					t.Errorf("description.md written with no description (err=%v)", err)
				}
			} else {
				got, err := os.ReadFile(descPath)
				if err != nil {
					t.Fatalf("description sidecar not written: %v", err)
				}
				if string(got) != tt.description+"\n" {
					t.Errorf("description sidecar = %q, want %q", string(got), tt.description+"\n")
				}
				// Nothing in the printed plan may echo the free text.
				if strings.Contains(out.String(), "rm -rf") {
					t.Errorf("free-text description leaked into the plan output:\n%s", out.String())
				}
			}

			// The announced child is a bare claude, never the conductor.
			if strings.Contains(out.String(), "/afk:go") {
				t.Errorf("scratch launch announced the conductor:\n%s", out.String())
			}
		})
	}
}

// TestScratchBuildRefusesExistingSessionDir keeps a one-off from colliding
// with a live session's own directory: .afk/<name>/ in the repo root is
// where the host writes host.json, so reusing it would hand two hosts the
// same metadata path (and the same socket).
func TestScratchBuildRefusesExistingSessionDir(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".afk", "scratch-1"), 0o755); err != nil {
		t.Fatalf("seeding the collision: %v", err)
	}
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	if _, err := Build(Config{Name: "scratch-1", RepoDir: repo, Scratch: true}, d); err == nil {
		t.Fatal("Build accepted a scratch name whose directory already exists")
	}
}

// TestScratchBuildCollisionCheckPropagatesStatErrors covers the other branch
// of the same collision check: an os.Stat failure that is NOT "the
// directory doesn't exist" (here, permission denied on an unsearchable
// parent) must abort Build with that error, not be silently read as "no
// collision, proceed." Build's own doc comment promises it aborts before
// any side effect on a detection failure; treating every non-nil Stat error
// as "doesn't exist" breaks that promise by deferring the failure to
// Run's later MkdirAll instead.
func TestScratchBuildCollisionCheckPropagatesStatErrors(t *testing.T) {
	repo := t.TempDir()
	afkDir := filepath.Join(repo, ".afk")
	if err := os.MkdirAll(afkDir, 0o755); err != nil {
		t.Fatalf("seeding .afk: %v", err)
	}
	// Strip search permission on .afk so os.Stat on the child session
	// directory fails with "permission denied" rather than ENOENT.
	if err := os.Chmod(afkDir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(afkDir, 0o755); err != nil {
			t.Fatalf("restoring .afk permissions: %v", err)
		}
	})

	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	_, err := Build(Config{Name: "scratch-1", RepoDir: repo, Scratch: true}, d)
	if err == nil {
		t.Fatal("Build succeeded despite an unresolvable Stat error, want it to abort")
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Build error = %v, want a Stat-failure error, not a collision message", err)
	}
}

// TestScratchRollback is the safety test for the mode. Load-bearing rows: both
// failure points must leave the repository untouched — no `git worktree
// remove` and no `git branch -D` may ever be attempted for a session that
// created neither — while still removing .afk/<name>/ and stopping a host
// that did get spawned.
func TestScratchRollback(t *testing.T) {
	cases := []struct {
		name string
		// failLaunch fails the host spawn itself (nothing to stop).
		failLaunch bool
		// wantDialed is whether rollback should have dialed the host's Exit.
		wantDialed bool
	}{
		{name: "host launch fails", failLaunch: true},
		{name: "a later step fails", wantDialed: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			shortTmpDir(t)
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			ctl := &fakeHostCtl{}
			d.DialExit = ctl.dialExit
			d.SignalKill = ctl.signalKill
			var errOut strings.Builder
			d.Stderr = &errOut
			if tt.failLaunch {
				d.Executable = func() (string, error) { return "", &exitError{} }
			}

			p, err := Build(Config{Name: "scratch-1", RepoDir: repo, Scratch: true}, d)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !tt.failLaunch {
				p.Steps = append(p.Steps, Step{
					Desc: "a later step that fails",
					run:  func() error { return &exitError{} },
				})
			}
			if err := p.Run(); err == nil {
				t.Fatal("Run should have failed")
			}

			// The single most dangerous thing this mode could do.
			for _, c := range fr.calls {
				joined := strings.Join(c.args, " ")
				if strings.Contains(joined, "worktree remove") || strings.Contains(joined, "branch -D") {
					t.Errorf("scratch rollback attempted %s %v", c.name, c.args)
				}
			}
			onlyWorktreeListCalls(t, fr)

			// The session directory must be gone.
			dir := filepath.Join(repo, ".afk", "scratch-1")
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("scratch rollback left %s behind (err=%v)", dir, err)
			}

			if tt.wantDialed {
				wantSocket, serr := host.SocketPath(repo, "scratch-1")
				if serr != nil {
					t.Fatalf("host.SocketPath: %v", serr)
				}
				if len(ctl.dialed) != 1 || ctl.dialed[0] != wantSocket {
					t.Errorf("rollback dialed %v, want exactly [%s]", ctl.dialed, wantSocket)
				}
			} else if len(ctl.dialed) != 0 {
				t.Errorf("rollback dialed %v despite no host having spawned", ctl.dialed)
			}
		})
	}
}

// TestScratchDryRunNoSideEffects mirrors TestDryRunNoSideEffects for the new
// mode: --dry-run must stay provably side-effect-free, and a one-off's only
// footprint (.afk/<name>/) is easy to create a step too early.
func TestScratchDryRunNoSideEffects(t *testing.T) {
	repo := t.TempDir()
	fr := &fakeRunner{}
	d := baseDeps(t, fr)
	var out strings.Builder
	d.Stdout = &out

	if err := Create(Config{Name: "scratch-1", RepoDir: repo, Scratch: true, DryRun: true}, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".afk")); !os.IsNotExist(err) {
		t.Errorf("dry run created .afk (err=%v)", err)
	}
	onlyWorktreeListCalls(t, fr)
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("dry-run banner missing:\n%s", out.String())
	}
}

// TestScratchBuildRefusesCrossWorktreeCollision widens the collision check
// beyond the repo root. A planned session keeps its `.afk/<name>/` inside
// its OWN worktree, so a repo-root-only stat lets `afk new --scratch dup`
// succeed while a planned session `dup` exists — and every clean-side caller
// resolves a name to the FIRST matching session, so a later confirmed scratch
// clean of `dup` can run the planned session's removal path (snapshot,
// archive, `git worktree remove`) against the wrong session. Load-bearing
// rows: "planned session in another worktree" is exactly that blocker
// scenario; "enumeration fails" pins that an unanswerable collision question
// aborts Build (the same stance the stat-error branch takes) instead of being
// read as "no collision"; "no collision" proves genuinely free names still
// build.
func TestScratchBuildRefusesCrossWorktreeCollision(t *testing.T) {
	cases := []struct {
		name string
		// seedFile, when non-empty, seeds `.afk/dup/<seedFile>` inside the
		// second worktree — status.json is the planned-session shape,
		// host.json the one-off shape. Either claims the name.
		seedFile string
		// listErr makes `git worktree list --porcelain` itself fail.
		listErr bool
		// wantErr is a substring of the expected Build error; "" means Build
		// must succeed.
		wantErr string
	}{
		{name: "planned session in another worktree", seedFile: "status.json", wantErr: "already exists"},
		{name: "one-off in another worktree", seedFile: "host.json", wantErr: "already exists"},
		{name: "no collision"},
		{name: "enumeration fails", listErr: true, wantErr: "worktree"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			other := t.TempDir()
			if tt.seedFile != "" {
				dir := filepath.Join(other, ".afk", "dup")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("seeding %s: %v", dir, err)
				}
				if err := os.WriteFile(filepath.Join(dir, tt.seedFile), []byte("{}\n"), 0o644); err != nil {
					t.Fatalf("seeding %s: %v", tt.seedFile, err)
				}
			}
			porcelain := "worktree " + repo + "\nHEAD abc\nbranch refs/heads/main\n\n" +
				"worktree " + other + "\nHEAD def\nbranch refs/heads/dup\n"
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			fr.handler = func(dir, name string, args []string) (string, error) {
				if name == "git" && len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
					if tt.listErr {
						return "", &exitError{}
					}
					return porcelain, nil
				}
				t.Errorf("scratch Build ran an unexpected command: %s %v", name, args)
				return "", nil
			}
			_, err := Build(Config{Name: "dup", RepoDir: repo, Scratch: true}, d)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Build refused a free name: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Build accepted scratch name \"dup\" despite the collision")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Build error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestScratchRunLosingRaceLeavesWinnerDirIntact covers the concurrent
// same-name create: Build's collision check passes while `.afk/<name>/` is
// absent, another create wins the directory before this one's steps run, and
// the loser must (a) fail rather than adopt the winner's directory and
// (b) roll back WITHOUT removing it — that directory's host.json is a
// one-off's only on-disk trace, so a RemoveAll here orphans the winner's live
// host. Load-bearing rows: "loses the race" is the direct exclusive-create
// path; "rollback after a later failure" exists to catch the os.MkdirAll
// mutant, under which the create step "succeeds" on the pre-existing
// directory, marks it as this invocation's own, and the appended failure
// makes rollback destroy it.
func TestScratchRunLosingRaceLeavesWinnerDirIntact(t *testing.T) {
	cases := []struct {
		name string
		// appendFailingStep forces a rollback from a step AFTER the directory
		// step, the shape under which the MkdirAll mutant turns destructive.
		appendFailingStep bool
	}{
		{name: "loses the race"},
		{name: "rollback after a later failure", appendFailingStep: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			shortTmpDir(t)
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)

			p, err := Build(Config{Name: "scratch-1", RepoDir: repo, Scratch: true}, d)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			// The winner lands between Build's collision check and Run.
			dir := filepath.Join(repo, ".afk", "scratch-1")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("seeding the winner's directory: %v", err)
			}
			sentinel := filepath.Join(dir, "host.json")
			if err := os.WriteFile(sentinel, []byte(`{"pid":1}`), 0o644); err != nil {
				t.Fatalf("seeding the winner's host.json: %v", err)
			}

			if tt.appendFailingStep {
				p.Steps = append(p.Steps, Step{
					Desc: "a later step that fails",
					run:  func() error { return &exitError{} },
				})
			}

			if err := p.Run(); err == nil {
				t.Fatal("Run succeeded despite losing the directory race")
			}
			if _, err := os.Stat(sentinel); err != nil {
				t.Errorf("rollback destroyed the winner's %s: %v", sentinel, err)
			}
		})
	}
}

// TestMergeBaseOptions covers the union of git-spice-tracked and local branches
// for the base picker. Load-bearing: stack-trunk ordering must hold (the reason
// BranchOptions was exported), empty-local must match BranchOptions, empty-stack
// must match OrderTrunkFirst, and empty-trunk must never produce a blank option
// (the empty-trunk precedence rule). Duplicate elimination across stack and local
// is the key invariant that makes the picker consistent: duplicates appear once.
func TestMergeBaseOptions(t *testing.T) {
	cases := []struct {
		name  string
		stack []spice.Branch
		trunk string
		local []string
		want  []string
	}{
		{
			name: "union of tracked stack and local branches",
			stack: []spice.Branch{
				{Name: "main"},
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
			},
			trunk: "main",
			local: []string{"bug-x", "feat-a", "main", "zeta"},
			want:  []string{"main", "feat-a", "bug-x", "zeta"},
		},
		{
			name: "empty local returns BranchOptions",
			stack: []spice.Branch{
				{Name: "main"},
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
			},
			trunk: "main",
			local: []string{},
			want:  []string{"main", "feat-a"},
		},
		{
			name:  "empty stack and local returns OrderTrunkFirst",
			stack: []spice.Branch{},
			trunk: "main",
			local: []string{"bug-x", "feat-a"},
			want:  []string{"main", "bug-x", "feat-a"},
		},
		{
			name: "trunk not present in local still appears first",
			stack: []spice.Branch{
				{Name: "main"},
			},
			trunk: "main",
			local: []string{"bug-x", "feat-a"},
			want:  []string{"main", "bug-x", "feat-a"},
		},
		{
			name: "empty trunk produces no blank option",
			stack: []spice.Branch{
				{Name: "main"},
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
			},
			trunk: "",
			local: []string{"bug-x", "feat-a", "main"},
			want:  []string{"main", "feat-a", "bug-x"},
		},
		{
			name: "duplicate names across stack and local appear once",
			stack: []spice.Branch{
				{Name: "main"},
				{Name: "feat-a", Down: &spice.BranchRef{Name: "main"}},
				{Name: "feat-b", Down: &spice.BranchRef{Name: "feat-a"}},
			},
			trunk: "main",
			local: []string{"feat-a", "feat-b", "main", "new-branch"},
			want:  []string{"main", "feat-a", "feat-b", "new-branch"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeBaseOptions(tc.stack, tc.trunk, tc.local)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MergeBaseOptions = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- autonomous mode -----------------------------------------------------

// TestChildArgv pins the child argv for every mode. Load-bearing rows:
// "autonomous" is the only place the fixed --autonomous token enters the
// argv; "scratch ignores autonomous" guards that a one-off never names a
// conductor flag; "plain" is the byte-identical regression guard for every
// existing session's launch. The codex rows prove the provider, not a
// hardcoded "claude", picks the binary; "unknown provider" pins the
// documented fall back to Claude's argv.
func TestChildArgv(t *testing.T) {
	cases := []struct {
		name string
		spec HostSpawn
		want []string
	}{
		{"plain", HostSpawn{Name: "billing", Worktree: "/wt"}, []string{"claude", "/afk:go --session billing"}},
		{"autonomous", HostSpawn{Name: "billing", Worktree: "/wt", Autonomous: true}, []string{"claude", "/afk:go --session billing --autonomous"}},
		{"scratch", HostSpawn{Name: "s1", Worktree: "/repo", Scratch: true}, []string{"claude"}},
		{"scratch ignores autonomous", HostSpawn{Name: "s1", Worktree: "/repo", Scratch: true, Autonomous: true}, []string{"claude"}},
		{"explicit claude", HostSpawn{Name: "billing", Worktree: "/wt", Provider: "claude"}, []string{"claude", "/afk:go --session billing"}},
		{"codex scratch", HostSpawn{Name: "s1", Worktree: "/repo", Scratch: true, Provider: "codex"}, []string{"codex"}},
		{"codex autonomous conductor", HostSpawn{Name: "billing", Worktree: "/wt", Autonomous: true, Provider: "codex"}, []string{"codex", "$afk:conductor --session billing --autonomous"}},
		{"opencode scratch", HostSpawn{Name: "s1", Worktree: "/repo", Scratch: true, Provider: "opencode"}, []string{"opencode", "--standalone"}},
		{"opencode interactive", HostSpawn{Name: "billing", Worktree: "/wt", Provider: "opencode"}, []string{"opencode", "--standalone", "--prompt", "/afk-go --session billing"}},
		{"opencode autonomous conductor", HostSpawn{Name: "billing", Worktree: "/wt", Autonomous: true, Provider: "opencode"}, []string{"opencode", "--standalone", "--prompt", "/afk-go --session billing --autonomous"}},
		{"unknown provider", HostSpawn{Name: "billing", Worktree: "/wt", Provider: "bogus"}, []string{"claude", "/afk:go --session billing"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := childArgv(tt.spec); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("childArgv = %#v, want %#v", got, tt.want)
			}
			// hostArgs must carry exactly childArgv after the separator.
			args := hostArgs(tt.spec, "/sock")
			sep := -1
			for i, a := range args {
				if a == "--" {
					sep = i
					break
				}
			}
			if sep < 0 {
				t.Fatalf("hostArgs has no `--` separator: %v", args)
			}
			if got := args[sep+1:]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("hostArgs child = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// stepIndex returns the index of the first step whose Desc contains sub, or -1.
func stepIndex(steps []Step, sub string) int {
	for i, s := range steps {
		if strings.Contains(s.Desc, sub) {
			return i
		}
	}
	return -1
}

// TestAutonomousStepOrder asserts, on the assembled steps, that the marker is
// its own step between the status seed and the host launch. Load-bearing
// rows: order seed < marker < host (the conductor reads the marker at start,
// so it must exist before the host does) and the absence row for a
// non-autonomous plan, which must gain no step at all.
func TestAutonomousStepOrder(t *testing.T) {
	const name = "auto-order"
	const markerDesc = "Write autonomous marker .afk/" + name + "/autonomous"
	for _, autonomous := range []bool{true, false} {
		t.Run(fmt.Sprintf("autonomous=%v", autonomous), func(t *testing.T) {
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			p, err := Build(Config{Name: name, RepoDir: t.TempDir(), Autonomous: autonomous}, d)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			seed := stepIndex(p.Steps, "Seed .afk/"+name+"/status.json")
			marker := stepIndex(p.Steps, markerDesc)
			host := stepIndex(p.Steps, "Launch detached session host")
			if seed < 0 || host < 0 {
				t.Fatalf("seed=%d host=%d; steps missing", seed, host)
			}
			if !autonomous {
				if marker != -1 {
					t.Fatalf("marker step present without Autonomous at %d", marker)
				}
				return
			}
			if !(seed < marker && marker < host) {
				t.Fatalf("order seed=%d marker=%d host=%d, want seed < marker < host", seed, marker, host)
			}
			if p.Steps[marker].undo != nil {
				t.Errorf("marker step has an undo; rollback removes the worktree and the marker with it")
			}
			if !strings.Contains(p.Steps[seed].Desc, "(phase planning, base ") || strings.Contains(p.Steps[seed].Desc, "autonomous") {
				t.Errorf("seed step Desc changed: %q", p.Steps[seed].Desc)
			}
		})
	}
}

// TestCreateAutonomousMarker runs the plan live: the marker exists after Create
// only when Autonomous is set, and a non-autonomous run leaves no sidecar.
func TestCreateAutonomousMarker(t *testing.T) {
	for _, autonomous := range []bool{true, false} {
		t.Run(fmt.Sprintf("autonomous=%v", autonomous), func(t *testing.T) {
			shortTmpDir(t)
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			var out strings.Builder
			d.Stdout = &out
			const name = "auto-live"
			if err := Create(Config{Name: name, RepoDir: repo, Autonomous: autonomous}, d); err != nil {
				t.Fatalf("Create: %v", err)
			}
			wt := filepath.Join(repo, ".claude", "worktrees", name)
			if got := IsAutonomous(wt, name); got != autonomous {
				t.Errorf("IsAutonomous = %v, want %v", got, autonomous)
			}
			if autonomous != strings.Contains(out.String(), "--autonomous") {
				t.Errorf("--autonomous in output = %v, want %v:\n%s", !autonomous, autonomous, out.String())
			}
		})
	}
}

// TestDryRunAutonomousMatchesArgv: the dry-run launch line and the real child
// argv come from one childArgv, so the printed command is the command run.
// Load-bearing rows: both modes must print the exact quoted argv, and only the
// autonomous one lists the marker step.
func TestDryRunAutonomousMatchesArgv(t *testing.T) {
	cases := []struct {
		name       string
		autonomous bool
		wantLaunch string
		wantMarker bool
	}{
		{"plain", false, `claude "/afk:go --session dry-auto"`, false},
		{"autonomous", true, `claude "/afk:go --session dry-auto --autonomous"`, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			var out strings.Builder
			d.Stdout = &out
			cfg := Config{Name: "dry-auto", RepoDir: repo, DryRun: true, Autonomous: tt.autonomous}
			if err := Create(cfg, d); err != nil {
				t.Fatalf("dry-run Create: %v", err)
			}
			printed := out.String()
			if !strings.Contains(printed, "Launch detached session host running: "+tt.wantLaunch) {
				t.Errorf("launch line missing %q:\n%s", tt.wantLaunch, printed)
			}
			if got := strings.Contains(printed, "Write autonomous marker .afk/dry-auto/autonomous"); got != tt.wantMarker {
				t.Errorf("marker line present = %v, want %v:\n%s", got, tt.wantMarker, printed)
			}
			if _, err := os.Stat(filepath.Join(repo, ".afk")); !os.IsNotExist(err) {
				t.Errorf(".afk created during dry-run (err=%v)", err)
			}
		})
	}
}

// --- provider -------------------------------------------------------------

// TestResolveSpawnProvider pins how SpawnHost settles a spawn's provider.
// Load-bearing rows: "empty spec reads the sidecar" is the dashboard
// relaunch path (it passes only Name and Worktree), and "unknown sidecar
// errors" guards against silently relaunching a Codex session under Claude.
// "sidecar claude canonicalizes" keeps the host argv of a Claude session
// byte-identical even if a sidecar names the default explicitly.
func TestResolveSpawnProvider(t *testing.T) {
	cases := []struct {
		name      string
		sidecar   string // "" = no sidecar file
		spec      string
		want      string
		wantError bool
	}{
		{name: "no sidecar, no spec", want: ""},
		{name: "empty spec reads the sidecar", sidecar: "codex", want: "codex"},
		{name: "explicit spec wins over the sidecar", sidecar: "claude", spec: "codex", want: "codex"},
		{name: "sidecar claude canonicalizes", sidecar: "claude", want: ""},
		{name: "unknown sidecar errors", sidecar: "bogus", wantError: true},
		{name: "unknown spec errors", spec: "bogus", wantError: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			wt := t.TempDir()
			const name = "relaunch"
			if tt.sidecar != "" {
				dir := filepath.Join(wt, ".afk", name)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := WriteProviderMarker(dir, tt.sidecar); err != nil {
					t.Fatal(err)
				}
			}
			got, err := resolveSpawnProvider(HostSpawn{Name: name, Worktree: wt, Provider: tt.spec})
			if tt.wantError {
				if err == nil {
					t.Fatalf("resolveSpawnProvider = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSpawnProvider: %v", err)
			}
			if got.Provider != tt.want {
				t.Errorf("Provider = %q, want %q", got.Provider, tt.want)
			}
			hasFlag := strings.Contains(strings.Join(hostArgs(got, "/sock"), " "), "--provider")
			if hasFlag != (tt.want != "") {
				t.Errorf("hostArgs --provider present = %v, want %v", hasFlag, tt.want != "")
			}
		})
	}
}

// TestSpawnHostRejectsUnknownSidecarProvider proves SpawnHost itself runs the
// provider resolution: a corrupt sidecar fails before the afk binary is
// even located, so no host is started under the wrong CLI.
func TestSpawnHostRejectsUnknownSidecarProvider(t *testing.T) {
	wt := t.TempDir()
	dir := filepath.Join(wt, ".afk", "relaunch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteProviderMarker(dir, "bogus"); err != nil {
		t.Fatal(err)
	}
	called := 0
	d := Deps{Executable: func() (string, error) { called++; return exec.LookPath("true") }}
	if _, err := SpawnHost(HostSpawn{Name: "relaunch", Worktree: wt}, d); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("SpawnHost err = %v, want an unknown-provider error naming %q", err, "bogus")
	}
	if called != 0 {
		t.Errorf("Executable called %d times; resolution must fail first", called)
	}
}

// TestDryRunProviderPlan checks the printed plan per provider. Load-bearing
// rows: the codex rows must list the provider-marker step and a host line
// running codex (the printed command is the executed one); "explicit claude"
// must print exactly what a flagless create prints, with no marker step.
func TestDryRunProviderPlan(t *testing.T) {
	cases := []struct {
		name       string
		cfg        Config
		wantLaunch string
		wantMarker bool
	}{
		{"codex autonomous", Config{Provider: "codex", Autonomous: true}, `codex "$afk:conductor --session dry-prov --autonomous"`, true},
		{"codex scratch", Config{Provider: "codex", Scratch: true}, "codex (one-off, in ", true},
		{"opencode autonomous", Config{Provider: "opencode", Autonomous: true}, `opencode "--standalone" "--prompt" "/afk-go --session dry-prov --autonomous"`, true},
		{"opencode scratch", Config{Provider: "opencode", Scratch: true}, `opencode "--standalone" (one-off, in `, true},
		{"explicit claude", Config{Provider: "claude"}, `claude "/afk:go --session dry-prov"`, false},
		{"default", Config{}, `claude "/afk:go --session dry-prov"`, false},
		{"default scratch", Config{Scratch: true}, "claude (one-off, in ", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			var out strings.Builder
			d.Stdout = &out
			cfg := tt.cfg
			cfg.Name, cfg.RepoDir, cfg.DryRun = "dry-prov", repo, true
			if err := Create(cfg, d); err != nil {
				t.Fatalf("dry-run Create: %v", err)
			}
			printed := out.String()
			marker := "Write provider marker .afk/dry-prov/provider (" + tt.cfg.Provider + ")"
			if !strings.Contains(printed, "Launch detached session host running: "+tt.wantLaunch) {
				t.Errorf("launch line missing %q:\n%s", tt.wantLaunch, printed)
			}
			if got := strings.Contains(printed, marker); got != tt.wantMarker {
				t.Errorf("marker line present = %v, want %v:\n%s", got, tt.wantMarker, printed)
			}
			if tt.wantMarker && strings.Index(printed, marker) > strings.Index(printed, "Launch detached session host") {
				t.Errorf("provider marker listed after the host launch:\n%s", printed)
			}
			if _, err := os.Stat(filepath.Join(repo, ".afk")); !os.IsNotExist(err) {
				t.Errorf(".afk created during dry-run (err=%v)", err)
			}
		})
	}
}

// TestBuildRejectsUnknownProvider: an unknown provider aborts Build in both
// modes, before any command runs.
func TestBuildRejectsUnknownProvider(t *testing.T) {
	for _, scratch := range []bool{false, true} {
		t.Run(fmt.Sprintf("scratch=%v", scratch), func(t *testing.T) {
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			_, err := Build(Config{Name: "bad-prov", RepoDir: t.TempDir(), Scratch: scratch, Provider: "bogus"}, d)
			if err == nil || !strings.Contains(err.Error(), "bogus") {
				t.Fatalf("Build err = %v, want an unknown-provider error", err)
			}
			if len(fr.calls) != 0 {
				t.Errorf("Build ran commands before rejecting the provider: %+v", fr.calls)
			}
		})
	}
}

// TestCreateWritesProviderSidecar runs the plan live. Load-bearing: a codex
// session leaves .afk/<name>/provider behind (the dashboard relaunch reads
// it), and a Claude one, explicit or not, leaves no sidecar at all.
func TestCreateWritesProviderSidecar(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		session func(repo string) string // where .afk/<name>/ lives
		want    string
	}{
		{"codex autonomous", Config{Provider: "codex", Autonomous: true}, func(repo string) string { return filepath.Join(repo, ".claude", "worktrees", "prov-live") }, "codex"},
		{"codex scratch", Config{Provider: "codex", Scratch: true}, func(repo string) string { return repo }, "codex"},
		{"opencode autonomous", Config{Provider: "opencode", Autonomous: true}, func(repo string) string { return filepath.Join(repo, ".claude", "worktrees", "prov-live") }, "opencode"},
		{"opencode scratch", Config{Provider: "opencode", Scratch: true}, func(repo string) string { return repo }, "opencode"},
		{"explicit claude", Config{Provider: "claude"}, func(repo string) string { return filepath.Join(repo, ".claude", "worktrees", "prov-live") }, ""},
		{"default scratch", Config{Scratch: true}, func(repo string) string { return repo }, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			shortTmpDir(t)
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			cfg := tt.cfg
			cfg.Name, cfg.RepoDir = "prov-live", repo
			if err := Create(cfg, d); err != nil {
				t.Fatalf("Create: %v", err)
			}
			wt := tt.session(repo)
			if got := ReadProvider(wt, "prov-live"); got != tt.want {
				t.Errorf("ReadProvider = %q, want %q", got, tt.want)
			}
			_, err := os.Stat(providerPath(wt, "prov-live"))
			if exists := err == nil; exists != (tt.want != "") {
				t.Errorf("sidecar exists = %v, want %v", exists, tt.want != "")
			}
		})
	}
}

// TestBuildPreflightDispatchesOnProvider covers the nil-AfkPreflight
// default: Build asks the session's own provider, through Deps.Run. An
// injected AfkPreflight still overrides it (every other test relies on
// that), so this test clears it.
func TestBuildPreflightDispatchesOnProvider(t *testing.T) {
	cases := []struct {
		provider string
		bin      string
		listing  string
		wantDesc string
	}{
		{"", "claude", "afk@claude-marche\n", "Preflight: afk plugin / /afk:go available"},
		{"codex", "codex", "afk@claude-marche  installed\n", "Preflight: afk plugin available in codex"},
		{"opencode", "opencode", "ID        VERSION  SOURCE\nafk     2.0.24   ~/.config/opencode/plugins/afk\n", "Preflight: afk plugin available in opencode"},
	}
	for _, tt := range cases {
		t.Run(tt.bin, func(t *testing.T) {
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			d.AfkPreflight = nil
			git := fr.handler
			fr.handler = func(dir, name string, args []string) (string, error) {
				if name == tt.bin && strings.Join(args, " ") == "plugin list" {
					return tt.listing, nil
				}
				return git(dir, name, args)
			}
			p, err := Build(Config{Name: "pre-flight", RepoDir: t.TempDir(), Autonomous: true, Provider: tt.provider}, d)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !fr.sawCommand(tt.bin, "plugin list") {
				t.Errorf("preflight never ran `%s plugin list`: %+v", tt.bin, fr.calls)
			}
			if !p.PreflightOK || stepIndex(p.Steps, tt.wantDesc) < 0 {
				t.Errorf("PreflightOK = %v, msg %q; want a %q step", p.PreflightOK, p.PreflightMsg, tt.wantDesc)
			}
		})
	}
}

// TestHostProviderNote pins the claude row to empty: the dry-run launch line
// for the default provider must stay byte-identical to a pre-provider one.
func TestHostProviderNote(t *testing.T) {
	cases := []struct{ provider, want string }{
		{"", ""},
		{"codex", " (host --provider codex)"},
		{"opencode", " (host --provider opencode)"},
	}
	for _, c := range cases {
		if got := hostProviderNote(HostSpawn{Provider: c.provider}); got != c.want {
			t.Errorf("hostProviderNote(%q) = %q, want %q", c.provider, got, c.want)
		}
	}
}
