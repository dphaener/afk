package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestNewSessionGuidance pins the post-create hand-off line. `afk new` no
// longer leaves the session in the caller's terminal, so this is the only
// thing telling the user where it went.
func TestNewSessionGuidance(t *testing.T) {
	got := newSessionGuidance("add-billing-webhook")
	for _, want := range []string{"add-billing-webhook", "created and running", "afk dash"} {
		if !strings.Contains(got, want) {
			t.Errorf("newSessionGuidance() = %q, missing %q", got, want)
		}
	}
	// This line is only ever printed when the terminal was NOT handed to the
	// dashboard (--no-dash, or piped stdio — see shouldHandOffToDash), so it must
	// stay a plain `afk dash` pointer: telling a script's log to run
	// `afk dash --open` would be advice for the one case this never covers.
	if strings.Contains(got, "--open") {
		t.Errorf("newSessionGuidance() should not reference --open: %q", got)
	}
	// The needle is split so the plan's residue check (`grep -rni` for the
	// literal multiplexer name) matches only real usages, never this
	// negative assertion about it.
	banned := "tm" + "ux"
	if strings.Contains(got, banned) {
		t.Errorf("newSessionGuidance() still mentions %s: %q", banned, got)
	}
}

// TestShouldHandOffToDash is the full matrix for `afk new`'s ending. The two
// non-negotiable rows are the non-terminal ones: `afk dash` is a Bubble Tea
// full-screen program and launching it on a pipe fails outright, so a scripted or
// CI `afk new` must get the plain-text guidance instead.
//
// --no-dash is the explicit opt-out, and it is also what the dashboard's own
// background spawn passes — without it, creating a session from the dashboard
// would launch a second dashboard inside the first.
func TestShouldHandOffToDash(t *testing.T) {
	tests := []struct {
		name      string
		stdinTTY  bool
		stdoutTTY bool
		noDash    bool
		want      bool
	}{
		{"full tty, no flag -> hand off", true, true, false, true},
		{"full tty, --no-dash -> guidance", true, true, true, false},
		{"stdout piped -> guidance", true, false, false, false},
		{"stdin piped -> guidance", false, true, false, false},
		{"both piped -> guidance", false, false, false, false},
		{"both piped, --no-dash -> guidance", false, false, true, false},
		{"stdout piped, --no-dash -> guidance", true, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldHandOffToDash(tt.stdinTTY, tt.stdoutTTY, tt.noDash)
			if got != tt.want {
				t.Errorf("shouldHandOffToDash(%v, %v, %v) = %v, want %v",
					tt.stdinTTY, tt.stdoutTTY, tt.noDash, got, tt.want)
			}
		})
	}
}

// TestDashOpenArgs pins the hand-off argv. The flag-before-value pairing is
// exact: a transposed ["dash", name, "--open"] would make the name a positional
// the dash flag set rejects. When a project is given, --project follows --open
// and its argument. When project is empty, --project is omitted entirely.
func TestDashOpenArgs(t *testing.T) {
	tests := []struct {
		name    string
		session string
		project rawProjectFlag
		want    []string
	}{
		{"no project flag", "add-billing-webhook", "", []string{"dash", "--open", "add-billing-webhook"}},
		{"with project flag", "add-billing-webhook", "/path/to/repo", []string{"dash", "--open", "add-billing-webhook", "--project", "/path/to/repo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dashOpenArgs(tt.session, tt.project)
			if len(got) != len(tt.want) {
				t.Fatalf("dashOpenArgs(%q, %q) = %v, want %v", tt.session, tt.project, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("dashOpenArgs(%q, %q) = %v, want %v", tt.session, tt.project, got, tt.want)
				}
			}
		})
	}
}

// TestResolveNewArgs covers the full decision matrix for `afk new`'s
// argument handling: this is the automated guard for scripted callers'
// exit-code semantics (usageErr == true must map to os.Exit(2) in cmdNew).
//
// Load-bearing autonomous rows: "autonomous + scratch" (usage error carrying
// autonomousScratchConflictMsg, including with zero args and with nargs>1 —
// a scratch session runs a bare claude that never sees /afk:go, so the flag
// would silently do nothing) and "autonomous + scratch + base" (the older
// scratchBaseConflictMsg keeps precedence, so adding this flag did not change
// an existing error). The "autonomous + base" and "autonomous alone" rows
// prove the flag constrains nothing else.
//
// Load-bearing scratch rows: the two mutual-exclusion rows ("scratch + base"
// and "scratch + no-git-spice", both usage errors carrying
// scratchBaseConflictMsg regardless of nargs/tty/dryRun — a scratch session
// never reaches resolveBase, so those flags would otherwise silently do
// nothing) and the no-positional-name row ("zero args, scratch -> proceed,
// no prompt" — valid on every tty/dryRun combination, unlike the non-scratch
// zero-args rows above it, because the caller allocates the name itself via
// session.NextScratchName instead of prompting).
//
// wantAllocate is load-bearing for the ordering fix a review caught: it is
// EXACTLY the signal cmdNew must defer until after --project is resolved
// (session.ListSessionDirs needs repo), and every other row proves the
// opposite — allocateScratchName is false for every non-scratch row and for
// "scratch, one arg", so cmdNew's ordinary name-resolution path (prompt or
// fs.Arg(0)) never depends on repo and keeps running ahead of --project
// resolution exactly as it always did. If resolveNewArgs's matrix ever
// changed such that some other row also needed repo, cmdNew (which switches
// on this return value rather than re-deriving "scratch && nargs == 0"
// inline) would pick that up automatically; a hand-recomputed inline
// condition would not.
func TestResolveNewArgs(t *testing.T) {
	tests := []struct {
		name         string
		nargs        int
		tty          bool
		dryRun       bool
		scratch      bool
		baseSet      bool
		noGitSpice   bool
		wantPrompt   bool
		wantAllocate bool
		wantUsageErr bool
		wantErrMsg   string
		autonomous   bool
	}{
		{"zero args, tty, no dry-run -> prompt", 0, true, false, false, false, false, true, false, false, "", false},
		{"zero args, tty, dry-run -> usage error", 0, true, true, false, false, false, false, false, true, "", false},
		{"zero args, non-tty, no dry-run -> usage error", 0, false, false, false, false, false, false, false, true, "", false},
		{"zero args, non-tty, dry-run -> usage error", 0, false, true, false, false, false, false, false, true, "", false},
		{"one arg, tty, no dry-run -> proceed", 1, true, false, false, false, false, false, false, false, "", false},
		{"one arg, tty, dry-run -> proceed", 1, true, true, false, false, false, false, false, false, "", false},
		{"one arg, non-tty, no dry-run -> proceed", 1, false, false, false, false, false, false, false, false, "", false},
		{"one arg, non-tty, dry-run -> proceed", 1, false, true, false, false, false, false, false, false, "", false},
		{"two args, tty, no dry-run -> usage error", 2, true, false, false, false, false, false, false, true, "", false},
		{"two args, tty, dry-run -> usage error", 2, true, true, false, false, false, false, false, true, "", false},
		{"two args, non-tty, no dry-run -> usage error", 2, false, false, false, false, false, false, false, true, "", false},
		{"two args, non-tty, dry-run -> usage error", 2, false, true, false, false, false, false, false, true, "", false},
		{"three args -> usage error", 3, false, false, false, false, false, false, false, true, "", false},

		// --- scratch rows ---
		{"scratch, zero args, tty, no dry-run -> proceed, no prompt (name allocated)", 0, true, false, true, false, false, false, true, false, "", false},
		{"scratch, zero args, non-tty, no dry-run -> proceed, no prompt", 0, false, false, true, false, false, false, true, false, "", false},
		{"scratch, zero args, tty, dry-run -> proceed, no prompt", 0, true, true, true, false, false, false, true, false, "", false},
		{"scratch, zero args, non-tty, dry-run -> proceed, no prompt", 0, false, true, true, false, false, false, true, false, "", false},
		{"scratch, one arg -> proceed with given name", 1, true, false, true, false, false, false, false, false, "", false},
		{"scratch, two args -> usage error", 2, false, false, true, false, false, false, false, true, "", false},
		{"scratch + base -> usage error, scratchBaseConflictMsg", 0, true, false, true, true, false, false, false, true, scratchBaseConflictMsg, false},
		{"scratch + no-git-spice -> usage error, scratchBaseConflictMsg", 0, true, false, true, false, true, false, false, true, scratchBaseConflictMsg, false},
		{"scratch + base + no-git-spice -> usage error, scratchBaseConflictMsg", 1, true, false, true, true, true, false, false, true, scratchBaseConflictMsg, false},
		{"scratch + base wins over nargs>1 (message still the conflict, not generic)", 2, true, false, true, true, false, false, false, true, scratchBaseConflictMsg, false},

		// --- autonomous rows ---
		{"autonomous alone, one arg -> proceed", 1, true, false, false, false, false, false, false, false, "", true},
		{"autonomous, zero args, tty -> prompt for name like any non-scratch run", 0, true, false, false, false, false, true, false, false, "", true},
		{"autonomous + base -> proceed (base is valid with autonomous)", 1, true, false, false, true, false, false, false, false, "", true},
		{"autonomous + scratch -> usage error, autonomousScratchConflictMsg", 1, true, false, true, false, false, false, false, true, autonomousScratchConflictMsg, true},
		{"autonomous + scratch, zero args -> usage error, no name allocated", 0, true, false, true, false, false, false, false, true, autonomousScratchConflictMsg, true},
		{"autonomous + scratch wins over nargs>1 (message is the conflict, not generic)", 2, false, false, true, false, false, false, false, true, autonomousScratchConflictMsg, true},
		{"autonomous + scratch + base -> scratchBaseConflictMsg first (existing conflict keeps precedence)", 1, true, false, true, true, false, false, false, true, scratchBaseConflictMsg, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPrompt, gotAllocate, gotUsageErr, gotErrMsg := resolveNewArgs(tt.nargs, tt.tty, tt.dryRun, tt.scratch, tt.baseSet, tt.noGitSpice, tt.autonomous)
			if gotPrompt != tt.wantPrompt {
				t.Errorf("resolveNewArgs(%d, %v, %v, %v, %v, %v, %v) promptForName = %v, want %v",
					tt.nargs, tt.tty, tt.dryRun, tt.scratch, tt.baseSet, tt.noGitSpice, tt.autonomous, gotPrompt, tt.wantPrompt)
			}
			if gotAllocate != tt.wantAllocate {
				t.Errorf("resolveNewArgs(%d, %v, %v, %v, %v, %v, %v) allocateScratchName = %v, want %v",
					tt.nargs, tt.tty, tt.dryRun, tt.scratch, tt.baseSet, tt.noGitSpice, tt.autonomous, gotAllocate, tt.wantAllocate)
			}
			if gotUsageErr != tt.wantUsageErr {
				t.Errorf("resolveNewArgs(%d, %v, %v, %v, %v, %v, %v) usageErr = %v, want %v",
					tt.nargs, tt.tty, tt.dryRun, tt.scratch, tt.baseSet, tt.noGitSpice, tt.autonomous, gotUsageErr, tt.wantUsageErr)
			}
			if gotErrMsg != tt.wantErrMsg {
				t.Errorf("resolveNewArgs(%d, %v, %v, %v, %v, %v, %v) errMsg = %q, want %q",
					tt.nargs, tt.tty, tt.dryRun, tt.scratch, tt.baseSet, tt.noGitSpice, tt.autonomous, gotErrMsg, tt.wantErrMsg)
			}
			// promptForName and allocateScratchName are mutually exclusive name
			// sources (a positional arg or usage-error row leaves both false) —
			// cmdNew's three-way switch relies on that to avoid a fourth,
			// unreachable branch.
			if gotPrompt && gotAllocate {
				t.Errorf("resolveNewArgs(%d, %v, %v, %v, %v, %v, %v) returned both promptForName and allocateScratchName true",
					tt.nargs, tt.tty, tt.dryRun, tt.scratch, tt.baseSet, tt.noGitSpice, tt.autonomous)
			}
		})
	}
}

// TestCmdNewDoesNotRederiveScratchNameDecision guards against the exact shape
// of finding W2.T1 (b): a prior cmdNew re-derived "does this invocation need
// an allocated name?" inline as `scratch && fs.NArg() == 0`, duplicating a
// decision resolveNewArgs already makes and returns as allocateScratchName.
// cmdNew is not otherwise unit-testable (flag.ExitOnError and real TTY/prompt
// I/O throughout), so this reads the production source directly — the same
// technique TestNewSessionGuidance already uses for its banned-substring
// check — and fails if the literal re-derivation reappears anywhere in the
// file (resolveNewArgs itself never spells out fs.NArg() at all; it takes
// nargs as a plain parameter, so the substring is unambiguous).
func TestCmdNewDoesNotRederiveScratchNameDecision(t *testing.T) {
	src, err := os.ReadFile("new.go")
	if err != nil {
		t.Fatalf("read new.go: %v", err)
	}
	banned := "scratch " + "&&" + " fs.NArg()"
	if strings.Contains(string(src), banned) {
		t.Errorf("new.go re-derives the scratch/no-name decision inline (%q) instead of trusting resolveNewArgs's allocateScratchName", banned)
	}
	if !strings.Contains(string(src), "allocateScratchName") {
		t.Fatalf("new.go no longer threads allocateScratchName through at all — this test can't be checking what it thinks it's checking")
	}
}

// TestCmdNewPreservesNonScratchNameResolutionOrder pins the ordering finding:
// a prior cmdNew moved ALL name and description resolution to after
// --project resolution, an observable and untested change to every existing
// (non-scratch) flag combination. The fix restores the original order for
// everything except the one output that genuinely needs repo
// (allocateScratchName, since the allocated name comes from scanning
// {repo}/.afk/) and defers only that. cmdNew has no other seam to assert
// this through (see TestCmdNewDoesNotRederiveScratchNameDecision's doc
// comment on why source inspection is used here), so this checks the actual
// byte order of the four call sites that make up the sequence: the
// resolveNewArgs call, the fs.Arg(0) fallback (representing the ordinary,
// repo-independent name path), the shouldPromptForDescription call, then
// --project resolution, then finally the deferred, repo-dependent
// session.NextScratchName call — which is the ONLY one of the four allowed
// to come after --project resolution.
func TestCmdNewPreservesNonScratchNameResolutionOrder(t *testing.T) {
	src, err := os.ReadFile("new.go")
	if err != nil {
		t.Fatalf("read new.go: %v", err)
	}
	body := string(src)

	markers := []string{
		`resolveNewArgs(fs.NArg(), tty, dryRun, scratch, base != "", noGitSpice, autonomous)`,
		`name = fs.Arg(0)`,
		`shouldPromptForDescription(description != "", tty, dryRun, scratch)`,
		`resolveAndValidateProject("new", raw, cwd, home)`,
		`session.NextScratchName(existing)`,
	}
	positions := make([]int, len(markers))
	for i, m := range markers {
		pos := strings.Index(body, m)
		if pos < 0 {
			t.Fatalf("marker %d (%q) not found in new.go — update this test's markers alongside cmdNew", i, m)
		}
		positions[i] = pos
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] >= positions[i] {
			t.Errorf("cmdNew ordering regressed: marker %q (offset %d) must come before %q (offset %d)",
				markers[i-1], positions[i-1], markers[i], positions[i])
		}
	}
}

// TestDescriptionPromptCallSite pins the ARGUMENT cmdNew passes, not just the
// decision function. The two are separately wrong-able: the function can be
// correct while the call site hands it an inverted expression, which is exactly
// the bug this test was added for — `description == ""` reads naturally next to
// a parameter called descriptionSet and inverts the whole feature, prompting
// when --description was supplied and staying silent when it was not. A table
// over shouldPromptForDescription alone cannot see that, because it never
// evaluates the caller's expression.
func TestDescriptionPromptCallSite(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        bool
	}{
		{"flag omitted -> prompt", "", true},
		{"flag supplied -> no prompt", "some context", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The literal expression from cmdNew's call site. If that line and
			// this one drift apart, this test stops protecting anything — keep
			// them identical.
			got := shouldPromptForDescription(tt.description != "", true, false, false)
			if got != tt.want {
				t.Errorf("with description=%q on a TTY: prompt=%v, want %v", tt.description, got, tt.want)
			}
		})
	}
}

// TestShouldPromptForDescription covers the full decision matrix for the
// description prompt decision. The load-bearing rows are the dry-run and
// non-TTY ones: those are what keep scripted `afk new` and the dashboard's
// background `afk new --no-dash` spawn from hanging forever on a prompt
// that no one can answer. The scratch rows are load-bearing too — a scratch
// session must never prompt for a description, on ANY tty/dryRun combination,
// since it has no conductor and no planning phase for one to seed.
func TestShouldPromptForDescription(t *testing.T) {
	tests := []struct {
		name           string
		descriptionSet bool
		tty            bool
		dryRun         bool
		scratch        bool
		want           bool
	}{
		{"empty, tty, no dry-run -> prompt", false, true, false, false, true},
		{"empty, tty, dry-run -> no prompt", false, true, true, false, false},
		{"empty, non-tty, no dry-run -> no prompt", false, false, false, false, false},
		{"empty, non-tty, dry-run -> no prompt", false, false, true, false, false},
		{"set, tty, no dry-run -> no prompt", true, true, false, false, false},
		{"set, tty, dry-run -> no prompt", true, true, true, false, false},
		{"set, non-tty, no dry-run -> no prompt", true, false, false, false, false},
		{"set, non-tty, dry-run -> no prompt", true, false, true, false, false},

		// --- scratch rows ---
		{"scratch, empty, tty, no dry-run -> no prompt", false, true, false, true, false},
		{"scratch, empty, non-tty, no dry-run -> no prompt", false, false, false, true, false},
		{"scratch, empty, tty, dry-run -> no prompt", false, true, true, true, false},
		{"scratch, set, tty, no dry-run -> no prompt", true, true, false, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldPromptForDescription(tt.descriptionSet, tt.tty, tt.dryRun, tt.scratch)
			if got != tt.want {
				t.Errorf("shouldPromptForDescription(%v, %v, %v, %v) = %v, want %v",
					tt.descriptionSet, tt.tty, tt.dryRun, tt.scratch, got, tt.want)
			}
		})
	}
}

// TestNewHandOffProjectCallSite pins the hand-off argv a real flag parse
// produces. The historical blocker here — cmdNew passing the RESOLVED repo,
// non-empty on every invocation because resolveProject falls back to cwd, so
// every bare `afk new` grew `--project <cwd>` — is no longer this test's
// to catch: runDashOpen and dashOpenArgs take rawProjectFlag, and a resolved
// repo is a plain string, so `runDashOpen(name, repo)` fails to COMPILE
// (mutation-verified). What this test still owns is the behavior end: a
// bare parse must yield the byte-identical pre---project argv (the
// load-bearing row), and an explicit flag value must ride through verbatim.
func TestNewHandOffProjectCallSite(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "bare invocation -> argv byte-identical to the pre-feature hand-off",
			args: []string{"my-session"},
			want: []string{"dash", "--open", "my-session"},
		},
		{
			name: "--project given -> raw flag value appended",
			args: []string{"--project", "/path/to/repo", "my-session"},
			want: []string{"dash", "--open", "my-session", "--project", "/path/to/repo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("new", flag.ContinueOnError)
			project := addProjectFlag(fs)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatalf("Parse(%v): %v", tt.args, err)
			}
			// Mirrors cmdNew's flag boundary: raw := rawProjectFlag(*project),
			// then runDashOpen(name, raw) -> dashOpenArgs(name, raw). The type
			// system, not this mirroring, is what prevents the resolved-repo
			// regression — see rawProjectFlag's doc comment.
			got := dashOpenArgs(fs.Arg(0), rawProjectFlag(*project))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("hand-off argv = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNewResolveAndValidateProject drives the flag-to-repo pipeline cmdNew
// itself calls — resolveAndValidateProject, where the only-when---project
// guard now lives — instead of reconstructing that guard test-side (a
// reconstruction passed twice against production code whose guard was
// disabled). The load-bearing rows: the two bad-path ones are `afk new
// --project`'s exit-non-zero acceptance criterion (cmdNew prints the
// returned error and exits 1, so a non-nil, path-naming, `afk
// new:`-attributed error here IS that criterion), and the bare row's cwd
// deliberately has NO .git entry, so any mutation that validates without an
// explicit --project turns it red — the plan's Must-NOT (bare `afk new`
// from a repo subdirectory keeps working) pinned against production code.
func TestNewResolveAndValidateProject(t *testing.T) {
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
	cwd := filepath.Join(base, "cwd-without-git")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	tests := []struct {
		name     string
		raw      rawProjectFlag
		cwd      string
		wantRepo string
		wantErr  string // substring; "" means the pipeline succeeds
		wantPath string // path the error must name; "" skips the check
	}{
		{"--project at a git repo -> accepted", rawProjectFlag(repo), cwd, repo, "", ""},
		{"--project at a non-repo directory -> rejected", rawProjectFlag(plain), cwd, "", "not a git repository", plain},
		{"--project at a missing path -> rejected", rawProjectFlag(missing), cwd, "", "does not exist", missing},
		{"bare -> cwd used unvalidated, even without .git", "", cwd, cwd, "", ""},
		{"no flag, no cwd -> attributed resolve error", "", "", "", "both empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAndValidateProject("new", tt.raw, tt.cwd, base)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("resolveAndValidateProject: %v, want nil", err)
				}
				if got != tt.wantRepo {
					t.Fatalf("repo = %q, want %q", got, tt.wantRepo)
				}
				return
			}
			if err == nil {
				t.Fatalf("resolveAndValidateProject = %q, nil — want error containing %q", got, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "afk new:") {
				t.Errorf("error = %q, not attributed to afk new", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, missing %q", err, tt.wantErr)
			}
			if tt.wantPath != "" && !strings.Contains(err.Error(), tt.wantPath) {
				t.Errorf("error = %q, does not name the path %q", err, tt.wantPath)
			}
		})
	}
}

// TestValidateProjectPath unit-covers the pure rejection decision beneath
// resolveAndValidateProject (which the pipeline tests reach through the
// production guard). The load-bearing rows are "nonexistent path" and
// "directory without .git" — the two rejections the plan names explicitly —
// plus the linked-worktree row, where .git is a FILE and an IsDir-style check
// would wrongly reject a valid checkout. probeProjectPath runs against real
// t.TempDir fixtures so the impure gatherer is exercised too, not just the
// pure decision.
func TestValidateProjectPath(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo/.git: %v", err)
	}
	worktree := filepath.Join(base, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatalf("write worktree .git file: %v", err)
	}
	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("mkdir plain: %v", err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	missing := filepath.Join(base, "missing")

	tests := []struct {
		name    string
		cmd     string
		path    string
		wantErr string // substring of the error; "" means nil
	}{
		{"main checkout accepted", "new", repo, ""},
		{"linked worktree (.git file) accepted", "new", worktree, ""},
		{"nonexistent path rejected", "new", missing, "does not exist"},
		{"plain file rejected", "new", file, "not a directory"},
		{"directory without .git rejected", "new", plain, "not a git repository"},
		{"clean attributes its own errors", "clean", missing, "afk clean:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProjectPath(tt.cmd, tt.path, probeProjectPath(tt.path))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateProjectPath(%q, %q) = %v, want nil", tt.cmd, tt.path, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateProjectPath(%q, %q) = nil, want error containing %q", tt.cmd, tt.path, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, missing %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.path) {
				t.Errorf("error = %q, does not name the path %q", err, tt.path)
			}
		})
	}
}

// TestResolveProviderArg covers --provider validation. Load-bearing rows:
// "codex alone" (the interactive conductor cannot run under Codex, so the exact
// codexInteractiveConflictMsg must come back), the two codex-ok rows (autonomous
// and scratch are the only supported Codex modes), and "unknown" (must name the
// valid providers rather than silently launching Claude). The empty and
// explicit-claude rows prove default behavior is unconstrained.
func TestResolveProviderArg(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		scratch    bool
		autonomous bool
		wantMsg    string // exact; "" means valid
		wantSubstr []string
	}{
		{"default empty, plain", "", false, false, "", nil},
		{"claude explicit, plain", "claude", false, false, "", nil},
		{"claude explicit, scratch", "claude", true, false, "", nil},
		{"codex + autonomous ok", "codex", false, true, "", nil},
		{"codex + scratch ok", "codex", true, false, "", nil},
		{"codex alone -> conflict const", "codex", false, false, codexInteractiveConflictMsg, nil},
		{"opencode interactive ok", "opencode", false, false, "", nil},
		{"opencode + autonomous ok", "opencode", false, true, "", nil},
		{"opencode + scratch ok", "opencode", true, false, "", nil},
		{"unknown -> names valid providers", "gemini", false, true, "", []string{"gemini", "claude", "codex"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveProviderArg(tt.provider, tt.scratch, tt.autonomous)
			if len(tt.wantSubstr) > 0 {
				for _, s := range tt.wantSubstr {
					if !strings.Contains(got, s) {
						t.Errorf("resolveProviderArg(%q) = %q, want it to contain %q", tt.provider, got, s)
					}
				}
				return
			}
			if got != tt.wantMsg {
				t.Errorf("resolveProviderArg(%q, %v, %v) = %q, want %q", tt.provider, tt.scratch, tt.autonomous, got, tt.wantMsg)
			}
		})
	}
}
