package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/rex"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
)

// The fault rows are the load-bearing ones: discover.Discover now emits
// worktree-side fault entries (BROKEN/ORPHAN rows) into the same slice, and
// sortSessionStates orders a non-scratch fault entry ahead of a scratch
// session sharing its name — so a name-only first match would resolve
// `afk attach dup` to a row that by definition has no host to dial and
// report "not running" where it used to attach. A fault row can never
// satisfy an attach lookup; a name that ONLY matches fault rows is simply
// not found, exactly as it was before fault rows joined the slice.
func TestFindSessionWorktree(t *testing.T) {
	sessions := []discover.SessionState{
		{Name: "session-a", WorktreePath: "/elsewhere/wt-a"},
		{Name: "session-b", WorktreePath: "/elsewhere/wt-b"},
		// sortSessionStates order: the non-scratch fault entry precedes the
		// scratch session of the same name.
		{Name: "dup", WorktreePath: "/elsewhere/wt-dup-fault", Kind: session.KindWorktreeNoSession},
		{Name: "dup", WorktreePath: "/elsewhere/repo", Scratch: true},
		{Name: "gone", WorktreePath: "/elsewhere/wt-gone", Kind: session.KindWorktreeMissing},
	}

	tests := []struct {
		name   string
		lookup string
		wantWT string
		wantOK bool
	}{
		{"found, worktree distinct from any cwd", "session-a", "/elsewhere/wt-a", true},
		{"found, second session", "session-b", "/elsewhere/wt-b", true},
		{"not found", "no-such-session", "", false},
		{"fault row never shadows the scratch session it collides with", "dup", "/elsewhere/repo", true},
		{"name matching only a fault row is not found", "gone", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotWT, gotOK := findSessionWorktree(sessions, tt.lookup)
			if gotWT != tt.wantWT || gotOK != tt.wantOK {
				t.Errorf("findSessionWorktree(%q) = (%q, %v), want (%q, %v)",
					tt.lookup, gotWT, gotOK, tt.wantWT, tt.wantOK)
			}
		})
	}
}

// runGit runs git with args in dir, failing the test on error. Mirrors
// internal/discover/discover_test.go's own helper of the same name.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// newTestRepo creates a bare-bones git repo at a temp dir with one commit on
// branch "main". Mirrors internal/discover/discover_test.go's helper.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "afk Test")
	runGit(t, dir, "config", "user.email", "afk-test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".afk/\n"), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "chore: Initial commit")
	return dir
}

// addWorktree creates a linked worktree outside repo (a sibling directory,
// matching how `git worktree add` is actually used by internal/session),
// checked out on a fresh branch.
func addWorktree(t *testing.T, repo, branch string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(repo), "wt-"+branch)
	runGit(t, repo, "worktree", "add", "-b", branch, path, "main")
	return path
}

// writeStatus seeds a schema-valid status.json for name inside worktree.
func writeStatus(t *testing.T, worktree, name string) {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	st := state.Status{
		SessionID:  "11111111-1111-1111-1111-111111111111",
		PlanName:   name,
		Phase:      state.PhaseExecuting,
		Branch:     name,
		BaseBranch: "main",
		UpdatedAt:  "2026-07-09T21:14:03Z",
	}
	data, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status.json"), data, 0o644); err != nil {
		t.Fatalf("write status.json: %v", err)
	}
}

func TestResolveSessionWorktree_SessionInWorktreeOutsideCWD(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "some-session")
	writeStatus(t, wt, "some-session")

	// Invoked from repo (the "cwd" `afk attach` runs from, e.g. the
	// dashboard's own repo root) — wt is a sibling directory, not repo and
	// not underneath it.
	got, ok, err := resolveSessionWorktree(repo, "some-session")
	if err != nil {
		t.Fatalf("resolveSessionWorktree: %v", err)
	}
	if !ok {
		t.Fatal("resolveSessionWorktree: ok = false, want true")
	}
	if got != wt {
		t.Errorf("resolveSessionWorktree worktree = %q, want %q", got, wt)
	}
	if got == repo {
		t.Errorf("resolveSessionWorktree returned the cwd (%q); the session's own worktree must be found instead", repo)
	}
}

// TestAttachProjectFlagResolvesTargetRepoSessions drives the exact pipeline
// cmdAttach itself runs between flag parse and runAttach — a real
// addProjectFlag registration, a real fs.Parse, then resolveProject(*proj,
// cwd, home), then runAttach(repo, name, wait) — instead of recomputing which
// repo a --project value ought to resolve to test-side. cmdAttach is an
// os.Exit-driven entry point this repo doesn't unit-test directly (same
// convention as cmdNew/cmdClean), so this is the same shape
// TestNewHandOffProjectCallSite and TestCleanProjectFlagListsThatRepoSessions
// use for the analogous problem in their own commands: real flag parsing,
// then the literal production call sequence, with cmdAttach's own two lines
// left as the only unseamed residue (attach.go is not in this fix's file set).
//
// Before this test, nothing connected the --project CLI surface for `afk
// attach` to resolveSessionWorktree/runAttach's already-tested behavior —
// only resolveProject's own table test (projectflag_test.go) exercised the
// flag value in isolation, hardcoded, with no flag.FlagSet involved at all.
//
// The two rows are mutually exclusive proof that --project, not cwd, decides
// which repo gets searched: "repo" holds the session (worktree + status.json,
// deliberately no host.json), "elsewhere" holds nothing. Routed at "repo" via
// --project from an unrelated cwd, the session IS found (discovery succeeds,
// only the host is missing — "no host.json found"). Routed at cwd with no
// flag, the same session name is NOT found there — a different message ("no
// such session found") that only a real repo mismatch, not a wording change,
// can produce. If --project were silently dropped before reaching
// resolveSessionWorktree, the first row would fail with the second row's
// message instead.
func TestAttachProjectFlagResolvesTargetRepoSessions(t *testing.T) {
	repo := newTestRepo(t)
	wt := addWorktree(t, repo, "sess-one")
	writeStatus(t, wt, "sess-one")
	elsewhere := newTestRepo(t)
	home := t.TempDir()

	tests := []struct {
		name    string
		args    []string
		cwd     string
		wantMsg string
	}{
		{
			name:    "--project routes discovery to the repo holding the session",
			args:    []string{"--project", repo, "sess-one"},
			cwd:     elsewhere,
			wantMsg: "no host.json found",
		},
		{
			name:    "bare invocation from an unrelated cwd finds nothing there",
			args:    []string{"sess-one"},
			cwd:     elsewhere,
			wantMsg: "no such session found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("attach", flag.ContinueOnError)
			project := addProjectFlag(fs)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatalf("Parse(%v): %v", tt.args, err)
			}

			resolvedRepo, err := resolveProject(*project, tt.cwd, home)
			if err != nil {
				t.Fatalf("resolveProject: %v", err)
			}
			msg, code, isError := runAttach(resolvedRepo, fs.Arg(0), 0)

			if !isError {
				t.Fatalf("isError = false, want true (msg=%q)", msg)
			}
			if code != host.ExitNotRunning {
				t.Errorf("code = %d, want host.ExitNotRunning (%d)", code, host.ExitNotRunning)
			}
			if !strings.Contains(msg, tt.wantMsg) {
				t.Errorf("msg = %q, want it to contain %q", msg, tt.wantMsg)
			}
		})
	}
}

func TestRunAttach_NonexistentSessionExitsNotRunning(t *testing.T) {
	repo := newTestRepo(t)

	msg, code, isError := runAttach(repo, "no-such-session", 0)
	if code != host.ExitNotRunning {
		t.Errorf("code = %d, want host.ExitNotRunning (%d)", code, host.ExitNotRunning)
	}
	if !isError {
		t.Error("isError = false, want true")
	}
	if !strings.Contains(msg, "no-such-session") {
		t.Errorf("msg = %q, want it to mention the session name", msg)
	}
}

func TestBuildServeConfig(t *testing.T) {
	tests := []struct {
		name      string
		flagName  string
		worktree  string
		socket    string
		logPath   string
		metaPath  string
		childArgv []string
		wantErr   bool
		want      host.ServeConfig
	}{
		{
			name:      "full flag set, single-word child command",
			flagName:  "my-session",
			worktree:  "/repo/worktree",
			socket:    "/tmp/afk-1000/my-session-abcd1234.sock",
			logPath:   "/repo/worktree/.afk/my-session/host.log",
			metaPath:  "/repo/worktree/.afk/my-session/host.json",
			childArgv: []string{"claude"},
			want: host.ServeConfig{
				Name:     "my-session",
				Worktree: "/repo/worktree",
				Socket:   "/tmp/afk-1000/my-session-abcd1234.sock",
				LogPath:  "/repo/worktree/.afk/my-session/host.log",
				MetaPath: "/repo/worktree/.afk/my-session/host.json",
				Argv:     []string{"claude"},
			},
		},
		{
			name:      "multi-word child argv preserved in order",
			flagName:  "s",
			worktree:  "/wt",
			socket:    "/sock",
			logPath:   "/log",
			metaPath:  "/meta",
			childArgv: []string{"claude", "/afk:go --session s"},
			want: host.ServeConfig{
				Name:     "s",
				Worktree: "/wt",
				Socket:   "/sock",
				LogPath:  "/log",
				MetaPath: "/meta",
				Argv:     []string{"claude", "/afk:go --session s"},
			},
		},
		{
			name:      "empty child argv is an error",
			flagName:  "s",
			worktree:  "/wt",
			socket:    "/sock",
			logPath:   "/log",
			metaPath:  "/meta",
			childArgv: nil,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildServeConfig(tt.flagName, tt.worktree, tt.socket, tt.logPath, tt.metaPath, tt.childArgv)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildServeConfig() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildServeConfig() error = %v, want nil", err)
			}
			if got.Name != tt.want.Name || got.Worktree != tt.want.Worktree ||
				got.Socket != tt.want.Socket || got.LogPath != tt.want.LogPath ||
				got.MetaPath != tt.want.MetaPath || strings.Join(got.Argv, "\x00") != strings.Join(tt.want.Argv, "\x00") {
				t.Errorf("buildServeConfig() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRunAttach_SessionWithNoHostJSONExitsNotRunning(t *testing.T) {
	repo := newTestRepo(t)
	writeStatus(t, repo, "no-host-session")

	// The session is discovered (it has a status.json) but never had a host
	// spawned for it, so host.json is missing.
	msg, code, isError := runAttach(repo, "no-host-session", 0)
	if code != host.ExitNotRunning {
		t.Errorf("code = %d, want host.ExitNotRunning (%d)", code, host.ExitNotRunning)
	}
	if !isError {
		t.Error("isError = false, want true")
	}
	if !strings.Contains(msg, "no-host-session") {
		t.Errorf("msg = %q, want it to mention the session name", msg)
	}
}

// TestReadMetaWithRetry_SucceedsOnceHostJSONAppears is the regression test
// for the reviewer finding that runAttach's single host.ReadMeta call had no
// retry window, unlike the socket dial: a caller racing a freshly-spawned
// host (host.json written moments after Serve starts) would report
// ExitNotRunning even though the host was seconds away from being up.
func TestReadMetaWithRetry_SucceedsOnceHostJSONAppears(t *testing.T) {
	repo := t.TempDir()
	name := "racing-session"
	want := host.Meta{PID: os.Getpid(), Socket: "/tmp/does-not-matter.sock"}

	go func() {
		time.Sleep(30 * time.Millisecond)
		if err := host.WriteMeta(repo, name, want); err != nil {
			t.Errorf("WriteMeta: %v", err)
		}
	}()

	got, err := readMetaWithRetry(repo, name, metaReadWait)
	if err != nil {
		t.Fatalf("readMetaWithRetry: %v", err)
	}
	if got.PID != want.PID || got.Socket != want.Socket {
		t.Errorf("readMetaWithRetry = %+v, want %+v", got, want)
	}
}

// TestReadMetaWithRetry_ZeroWaitTriesOnce confirms wait<=0 still gets
// exactly one attempt (the property runAttach's existing "no host.json at
// all" test, called with wait=0, depends on).
func TestReadMetaWithRetry_ZeroWaitTriesOnce(t *testing.T) {
	repo := t.TempDir()

	start := time.Now()
	_, err := readMetaWithRetry(repo, "never-written", 0)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error: host.json was never written")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("readMetaWithRetry(wait=0) took %s, want a single immediate attempt", elapsed)
	}
}

// TestRunAttach_Rex drives the Rex branch of runAttach through a fake Rex. The
// load-bearing rows: outside Rex says where the agent runs and makes no Rex
// call at all; inside Rex without the owner on that session must not focus
// (focus_window is session-wide); a missing window id must not focus; and an
// afk-runtime session never reaches the Rex branch (the dial path is kept,
// shown by it failing the way it always did with no socket).
func TestRunAttach_Rex(t *testing.T) {
	clients := func(sessions string) string {
		return `{"clients":[{"client_id":"c1","connected_at":"2026-01-01T00:00:00Z","connection_state":"connected",` +
			`"info":{"kind":"app","platform":"macos"},"session_ids":[` + sessions + `]}]}`
	}
	inside := func(k string) string {
		if k == "REX_SESSION" || k == "REX_BLOCK" {
			return "x"
		}
		return ""
	}
	outside := func(string) string { return "" }
	tests := []struct {
		name       string
		runtime    string
		window     string
		getenv     func(string) string
		clients    string
		wantCode   int
		wantErr    bool
		wantMsg    string
		wantMethod []string
	}{
		{name: "outside rex gives band text", runtime: "rex", window: "w1", getenv: outside,
			wantMsg: "This agent runs in Rex: open", wantMethod: nil},
		{name: "inside rex with owner on session focuses", runtime: "rex", window: "w1", getenv: inside,
			clients: clients(`"s1"`), wantMsg: "is shown in Rex", wantMethod: []string{"client.list", "session.focus_window"}},
		{name: "inside rex owner on another session gives band text", runtime: "rex", window: "w1", getenv: inside,
			clients: clients(`"other"`), wantMsg: "This agent runs in Rex: open", wantMethod: []string{"client.list"}},
		{name: "inside rex missing window id gives band text", runtime: "rex", window: "", getenv: inside,
			wantMsg: "This agent runs in Rex: open", wantMethod: nil},
		{name: "afk runtime keeps the dial path", runtime: "", window: "", getenv: inside,
			wantCode: host.ExitNotRunning, wantErr: true, wantMsg: "not running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepo(t)
			writeStatus(t, repo, "sess")
			meta := host.Meta{PID: os.Getpid(), Socket: filepath.Join(t.TempDir(), "none.sock"), Runtime: tt.runtime}
			if tt.runtime == "rex" {
				meta.Rex = &host.RexBlock{SessionID: "s1", WindowID: tt.window, BlockID: "b1"}
			}
			if err := host.WriteMeta(repo, "sess", meta); err != nil {
				t.Fatal(err)
			}
			f := (&rex.Fake{}).On("client.list", tt.clients, nil).On("session.focus_window", `{"revision":1}`, nil)
			msg, code, isErr := runAttachWith(repo, "sess", 0, tt.getenv, func() rex.Caller { return f })
			if code != tt.wantCode || isErr != tt.wantErr {
				t.Errorf("code, isErr = %d, %v; want %d, %v (msg %q)", code, isErr, tt.wantCode, tt.wantErr, msg)
			}
			if !strings.Contains(msg, tt.wantMsg) {
				t.Errorf("msg = %q, want it to contain %q", msg, tt.wantMsg)
			}
			if tt.runtime == "rex" && !strings.Contains(msg, "sess") {
				t.Errorf("msg = %q, want the session name", msg)
			}
			var got []string
			for _, c := range f.Calls() {
				got = append(got, c.Method)
			}
			if strings.Join(got, ",") != strings.Join(tt.wantMethod, ",") {
				t.Errorf("rex calls = %v, want %v", got, tt.wantMethod)
			}
		})
	}
}
