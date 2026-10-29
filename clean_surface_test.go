package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/session"
)

// cleanSurfaceStatus is a status.json strict enough for internal/state's
// parser (nine keys, plan_name matching the directory), so a CheckPhase row
// compares a real phase and not the stale "" a looser fixture would read as.
func cleanSurfaceStatus(name, phase string) string {
	return `{"session_id":"11111111-1111-1111-1111-111111111111","plan_name":"` + name + `","phase":"` + phase +
		`","branch":"` + name + `","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"2026-10-14T09:00:00Z"}` + "\n"
}

// cleanSurfaceFixture is a throwaway repository on main with `.afk/` ignored,
// and a home folder for the archive. Nothing in it is a real session: there
// is no host, and the host-touching parts of session.CleanDeps are fakes.
type cleanSurfaceFixture struct {
	root, home string
}

func newCleanSurfaceFixture(t *testing.T) cleanSurfaceFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := cleanSurfaceFixture{root: filepath.Join(base, "repo"), home: filepath.Join(base, "home")}
	for _, d := range []string{f.root, f.home} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	daemonCleanGit(t, f.root, "init", "-b", "main")
	daemonCleanGit(t, f.root, "config", "user.name", "afk Test")
	daemonCleanGit(t, f.root, "config", "user.email", "afk-test@example.com")
	writeBuildFile(t, f.root, ".gitignore", ".afk/\n")
	daemonCleanGit(t, f.root, "add", "-A")
	daemonCleanGit(t, f.root, "commit", "-m", "chore: Initial commit")
	return f
}

// worktree returns where a session's worktree lives, beside the repository.
func (f cleanSurfaceFixture) worktree(name string) string {
	return filepath.Join(filepath.Dir(f.root), name)
}

// addSession makes a planned session: a worktree on its own branch holding a
// commit main does not (so the branch is unmerged), and its status.json.
func (f cleanSurfaceFixture) addSession(t *testing.T, name, phase string) {
	t.Helper()
	wt := f.worktree(name)
	daemonCleanGit(t, f.root, "worktree", "add", "-b", name, wt, "main")
	writeBuildFile(t, wt, "work.txt", "work\n")
	daemonCleanGit(t, wt, "add", "-A")
	daemonCleanGit(t, wt, "commit", "-m", "feat: Add work")
	writeBuildFile(t, wt, filepath.Join(".afk", name, "status.json"), cleanSurfaceStatus(name, phase))
}

// deps is the real environment with every host-touching call faked: no host
// is alive, and dialing one fails, so a one-off's teardown only removes files.
func (f cleanSurfaceFixture) deps() cleanSurfaceDeps {
	cd := session.DefaultCleanDeps()
	cd.HomeDir = func() (string, error) { return f.home, nil }
	cd.Now = func() time.Time { return daemonTestNow }
	cd.IsSpiceTracked = func(string, string) bool { return false }
	cd.Alive = func(int) bool { return false }
	cd.SignalKill = func(int, syscall.Signal) error { return nil }
	cd.DialExit = func(string, time.Duration) error { return errors.New("no host here") }
	return cleanSurfaceDeps{Clean: cd}
}

func cleanSurfaceExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestCleanSessionForSurface runs the entry point over real git in temp
// folders, as TestDaemonClean does: a wrong git call cannot be caught by a
// fake. No row starts a host or an agent.
//
// Load-bearing rows:
//   - "phase gate refused" and "phase gate with force": the gate is the whole
//     reason a running session is not cleaned by accident, and Force is the
//     one way past it; both must leave or remove the worktree accordingly.
//   - "stale PhaseSeen refused" and "PhaseSeen matches": the page's confirm
//     names a phase, and a session that moved on since is refused with the
//     worktree intact; the matching row proves the fresh read uses the same
//     strict parser the page shows, or every web clean would be refused.
//   - "stranded entry cleaned": its WorktreePath is the owner's own checkout,
//     so the row checks the repository is still there.
//   - "unmerged branch with no terminal": a surface has no terminal, so
//     --delete-branch on unmerged work must refuse, never delete.
//   - "confirm declined" and "confirm accepted": the command's questions now
//     come in through deps, and a no must change nothing.
//   - "teardown fails": the failure text reaches Text, not the process's
//     stderr.
func TestCleanSessionForSurface(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T, f cleanSurfaceFixture)
		session string
		req     cleanSurfaceRequest
		deps    func(d cleanSurfaceDeps) cleanSurfaceDeps
		want    cleanSurfaceOutcome
		code    int
		text    string // substring of Text; "" means Text must be empty
		check   func(t *testing.T, f cleanSurfaceFixture, res cleanSurfaceResult)
	}{
		{name: "not found", session: "nope",
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "done") },
			want:  cleanNotFound, code: 1,
			text: "no session named \"nope\" found (run `afk clean` to list sessions)"},
		{name: "phase gate refused", session: "t1-1",
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "executing") },
			want:  cleanRefusedOutcome, code: 1,
			text: `session "t1-1" is in phase "executing" (not done/failed) — pass --force to clean it anyway`,
			check: func(t *testing.T, f cleanSurfaceFixture, _ cleanSurfaceResult) {
				if !cleanSurfaceExists(f.worktree("t1-1")) {
					t.Error("a refused clean removed the worktree")
				}
			}},
		{name: "phase gate with force", session: "t1-1", req: cleanSurfaceRequest{Force: true},
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "executing") },
			want:  cleanCleaned,
			check: func(t *testing.T, f cleanSurfaceFixture, _ cleanSurfaceResult) {
				if cleanSurfaceExists(f.worktree("t1-1")) {
					t.Error("a forced clean left the worktree")
				}
			}},
		{name: "stale PhaseSeen refused", session: "t1-1", req: cleanSurfaceRequest{CheckPhase: true, PhaseSeen: "executing"},
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "done") },
			want:  cleanRefusedOutcome, code: 1,
			text: `session "t1-1" changed since it was shown: its phase was "executing" and is now "done". Nothing was cleaned.`,
			check: func(t *testing.T, f cleanSurfaceFixture, _ cleanSurfaceResult) {
				if !cleanSurfaceExists(f.worktree("t1-1")) {
					t.Error("a refused clean removed the worktree")
				}
			}},
		{name: "stale PhaseSeen refused even with force", session: "t1-1",
			req:   cleanSurfaceRequest{Force: true, CheckPhase: true, PhaseSeen: "done"},
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "fixing") },
			want:  cleanRefusedOutcome, code: 1, text: `its phase was "done" and is now "fixing"`},
		{name: "PhaseSeen matches", session: "t1-1", req: cleanSurfaceRequest{CheckPhase: true, PhaseSeen: "done"},
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "done") },
			want:  cleanCleaned},
		{name: "one-off cleaned", session: "scratchy",
			setup: func(t *testing.T, f cleanSurfaceFixture) { writeScratchHost(t, f.root, "scratchy") },
			want:  cleanCleaned,
			check: func(t *testing.T, f cleanSurfaceFixture, res cleanSurfaceResult) {
				if cleanSurfaceExists(filepath.Join(f.root, ".afk", "scratchy")) {
					t.Error("the one-off's session directory is still there")
				}
				want := strings.Split(strings.TrimSuffix(cleanSummary(true, res.Result), "\n"), "\n")
				if strings.Join(res.Lines, "\n") != strings.Join(want, "\n") {
					t.Errorf("Lines = %q, want %q", res.Lines, want)
				}
			}},
		{name: "normal session cleaned", session: "t1-1",
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "done") },
			want:  cleanCleaned,
			check: func(t *testing.T, f cleanSurfaceFixture, res cleanSurfaceResult) {
				if cleanSurfaceExists(f.worktree("t1-1")) {
					t.Error("the worktree is still there")
				}
				if res.Result.ArchivePath == "" || !strings.HasPrefix(res.Result.ArchivePath, f.home) || !cleanSurfaceExists(res.Result.ArchivePath) {
					t.Errorf("ArchivePath = %q, want an existing folder under %s", res.Result.ArchivePath, f.home)
				}
				if daemonCleanGit(t, f.root, "branch", "--list", "t1-1") == "" {
					t.Error("the branch was deleted; a plain clean keeps it")
				}
				want := []string{"Kept:", "  branch:  t1-1", "  archive: " + res.Result.ArchivePath, "Removed:", "  worktree: " + f.worktree("t1-1")}
				if strings.Join(res.Lines, "\n") != strings.Join(want, "\n") {
					t.Errorf("Lines = %q, want %q", res.Lines, want)
				}
			}},
		{name: "dirty worktree is snapshot-committed first", session: "t1-1",
			setup: func(t *testing.T, f cleanSurfaceFixture) {
				f.addSession(t, "t1-1", "done")
				writeBuildFile(t, f.worktree("t1-1"), "work.txt", "changed\n")
			},
			want: cleanCleaned,
			check: func(t *testing.T, f cleanSurfaceFixture, res cleanSurfaceResult) {
				if len(res.Lines) == 0 || res.Lines[0] != `Snapshot-committed dirty worktree for "t1-1"` {
					t.Errorf("Lines = %q, want the snapshot line first", res.Lines)
				}
			}},
		{name: "stranded entry cleaned", session: "lost",
			setup: func(t *testing.T, f cleanSurfaceFixture) {
				writeBuildFile(t, f.root, filepath.Join(".afk", "lost", "status.json"), cleanSurfaceStatus("lost", "done"))
			},
			want: cleanCleaned,
			check: func(t *testing.T, f cleanSurfaceFixture, res cleanSurfaceResult) {
				if res.Result.Kind != session.KindStatusWithoutWorktree {
					t.Errorf("Kind = %v, want status-without-worktree", res.Result.Kind)
				}
				if !cleanSurfaceExists(filepath.Join(f.root, ".git")) {
					t.Error("the owner's own checkout was touched")
				}
				if cleanSurfaceExists(filepath.Join(f.root, ".afk", "lost")) {
					t.Error("the stranded session directory is still there")
				}
			}},
		{name: "worktree fault cleaned", session: "gone",
			setup: func(t *testing.T, f cleanSurfaceFixture) {
				f.addSession(t, "gone", "done")
				if err := os.RemoveAll(f.worktree("gone")); err != nil {
					t.Fatal(err)
				}
			},
			want: cleanCleaned,
			check: func(t *testing.T, f cleanSurfaceFixture, res cleanSurfaceResult) {
				if res.Result.Kind != session.KindWorktreeMissing {
					t.Errorf("Kind = %v, want worktree-missing", res.Result.Kind)
				}
				if strings.Contains(daemonCleanGit(t, f.root, "worktree", "list", "--porcelain"), f.worktree("gone")) {
					t.Error("the broken worktree is still registered")
				}
			}},
		{name: "delete-branch on a one-off refused", session: "scratchy", req: cleanSurfaceRequest{DeleteBranch: true},
			setup: func(t *testing.T, f cleanSurfaceFixture) { writeScratchHost(t, f.root, "scratchy") },
			want:  cleanRefusedOutcome, code: 1,
			text: `--delete-branch has no effect on "scratchy": it has no branch of its own`},
		{name: "unmerged branch with no terminal", session: "t1-1", req: cleanSurfaceRequest{DeleteBranch: true},
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "done") },
			want:  cleanRefusedOutcome, code: 1,
			text: `branch "t1-1" has not been merged — deleting it needs interactive confirmation, and stdin is not a TTY`,
			check: func(t *testing.T, f cleanSurfaceFixture, _ cleanSurfaceResult) {
				if !cleanSurfaceExists(f.worktree("t1-1")) || daemonCleanGit(t, f.root, "branch", "--list", "t1-1") == "" {
					t.Error("a refused clean changed something")
				}
			}},
		{name: "confirm declined", session: "t1-1", req: cleanSurfaceRequest{Force: true},
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "executing") },
			deps: func(d cleanSurfaceDeps) cleanSurfaceDeps {
				d.TTY = true
				d.Confirm = func(string, string, string) (bool, error) { return false, nil }
				return d
			},
			want: cleanRefusedOutcome, code: 1, text: "aborted, no changes made",
			check: func(t *testing.T, f cleanSurfaceFixture, _ cleanSurfaceResult) {
				if !cleanSurfaceExists(f.worktree("t1-1")) {
					t.Error("a declined clean removed the worktree")
				}
			}},
		{name: "confirm accepted deletes unmerged branch", session: "t1-1", req: cleanSurfaceRequest{DeleteBranch: true},
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "done") },
			deps: func(d cleanSurfaceDeps) cleanSurfaceDeps {
				d.TTY = true
				d.Confirm = func(title, yes, _ string) (bool, error) { return yes == "Delete branch", nil }
				return d
			},
			want: cleanCleaned,
			check: func(t *testing.T, f cleanSurfaceFixture, res cleanSurfaceResult) {
				if !res.Result.BranchDeleted || daemonCleanGit(t, f.root, "branch", "--list", "t1-1") != "" {
					t.Error("the confirmed branch delete did not happen")
				}
			}},
		{name: "teardown fails", session: "t1-1",
			setup: func(t *testing.T, f cleanSurfaceFixture) { f.addSession(t, "t1-1", "done") },
			deps: func(d cleanSurfaceDeps) cleanSurfaceDeps {
				d.CleanSession = func(_ string, _ session.Session, cd session.CleanDeps, _ session.CleanOptions) (session.CleanResult, error) {
					_, _ = cd.Stderr.Write([]byte("afk clean: a note on the way\n"))
					return session.CleanResult{}, errors.New("git worktree remove failed")
				}
				return d
			},
			want: cleanFailed, code: 1, text: "git worktree remove failed",
			check: func(t *testing.T, _ cleanSurfaceFixture, res cleanSurfaceResult) {
				if res.EchoStderr != "afk clean: a note on the way\n" {
					t.Errorf("EchoStderr = %q, want what the teardown wrote", res.EchoStderr)
				}
			}},
		{name: "sessions cannot be listed", session: "t1-1",
			setup: func(*testing.T, cleanSurfaceFixture) {},
			deps: func(d cleanSurfaceDeps) cleanSurfaceDeps {
				d.Discover = func(string, session.CleanDeps) ([]session.Session, error) { return nil, errors.New("git is broken") }
				return d
			},
			want: cleanFailed, code: 1, text: "git is broken"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCleanSurfaceFixture(t)
			tt.setup(t, f)
			d := f.deps()
			if tt.deps != nil {
				d = tt.deps(d)
			}
			res := cleanSessionForSurface(f.root, tt.session, tt.req, d)
			if res.Outcome != tt.want || res.Code != tt.code {
				t.Fatalf("Outcome, Code = %q, %d; want %q, %d (Text %q)", res.Outcome, res.Code, tt.want, tt.code, res.Text)
			}
			if res.ToStderr != (tt.want != cleanCleaned) {
				t.Errorf("ToStderr = %v for outcome %q", res.ToStderr, res.Outcome)
			}
			if tt.text == "" && res.Text != "" {
				t.Errorf("Text = %q, want none", res.Text)
			}
			if !strings.Contains(res.Text, tt.text) {
				t.Errorf("Text = %q, want it to contain %q", res.Text, tt.text)
			}
			if strings.HasPrefix(res.Text, "afk clean:") {
				t.Errorf("Text = %q carries the command's prefix", res.Text)
			}
			if tt.want != cleanCleaned && len(res.Lines) != 0 {
				t.Errorf("Lines = %q on a clean that did not happen", res.Lines)
			}
			if tt.check != nil {
				tt.check(t, f, res)
			}
		})
	}
}

// TestPrintCleanSurfaceResult pins how `afk clean <name>` prints a result, so
// moving the work behind cleanSessionForSurface left its output where it was.
// Load-bearing: the refusal row (stderr only, with the prefix, exit 1) and the
// success row (stdout lines byte for byte, exit 0), plus the echo row, since
// internal/session's own warnings used to go straight to stderr.
func TestPrintCleanSurfaceResult(t *testing.T) {
	tests := []struct {
		name           string
		res            cleanSurfaceResult
		stdout, stderr string
		code           int
	}{
		{name: "refused",
			res:    cleanSurfaceResult{Outcome: cleanRefusedOutcome, Code: 1, ToStderr: true, Text: "aborted, no changes made"},
			stderr: "afk clean: aborted, no changes made\n", code: 1},
		{name: "cleaned",
			res:    cleanSurfaceResult{Outcome: cleanCleaned, Lines: []string{"Kept:", "  branch:  t1-1", "Removed:"}},
			stdout: "Kept:\n  branch:  t1-1\nRemoved:\n"},
		{name: "cleaned with session warnings",
			res:    cleanSurfaceResult{Outcome: cleanCleaned, Lines: []string{"Kept:"}, EchoStderr: "afk clean: archive skipped\n"},
			stdout: "Kept:\n", stderr: "afk clean: archive skipped\n"},
		{name: "failed after a warning",
			res:    cleanSurfaceResult{Outcome: cleanFailed, Code: 1, ToStderr: true, Text: "boom", EchoStderr: "afk clean: note\n"},
			stderr: "afk clean: note\nafk clean: boom\n", code: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := printCleanSurfaceResult(&out, &errOut, tt.res)
			if out.String() != tt.stdout || errOut.String() != tt.stderr || code != tt.code {
				t.Errorf("got stdout %q, stderr %q, code %d; want %q, %q, %d",
					out.String(), errOut.String(), code, tt.stdout, tt.stderr, tt.code)
			}
		})
	}
}

// TestCleanSurfaceBeforeClean shows `afk clean` and the web page's clean
// hand the teardown a BeforeClean made for the repository being cleaned, so a
// failed Rex session keeps its last screen (PLAN T17.13).
//
// "save fails, still cleaned" is the load-bearing row: the save is a picture
// of the session and must never stand between the owner and the clean they
// asked for; its failure is a note on the clean's stderr. "default saver" is
// the promise that a surface which sets nothing still gets the real saver
// (here over a session with no host record, so it saves nothing and runs no
// Rex).
func TestCleanSurfaceBeforeClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	tests := []struct {
		name     string
		hookErr  error
		fake     bool
		wantNote string
	}{
		{name: "save works", fake: true},
		{name: "save fails, still cleaned", fake: true, hookErr: errors.New("the agent's screen could not be saved: Rex is not answering"),
			wantNote: "Rex is not answering"},
		{name: "default saver"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCleanSurfaceFixture(t)
			f.addSession(t, "t1-1", "failed")
			d := f.deps()
			var gotRepo string
			var calls int
			if tt.fake {
				d.BeforeClean = func(repo string) func(session.Session) error {
					gotRepo = repo
					return func(s session.Session) error {
						calls++
						if s.Name != "t1-1" || s.Status.Phase != "failed" {
							t.Errorf("BeforeClean got %s in phase %q, want t1-1 failed", s.Name, s.Status.Phase)
						}
						return tt.hookErr
					}
				}
			}
			res := cleanSessionForSurface(f.root, "t1-1", cleanSurfaceRequest{}, d)
			if res.Outcome != cleanCleaned {
				t.Fatalf("outcome = %s (%s), want cleaned", res.Outcome, res.Text)
			}
			if cleanSurfaceExists(f.worktree("t1-1")) {
				t.Fatal("the worktree is still there after the clean")
			}
			if tt.fake && (gotRepo != f.root || calls != 1) {
				t.Fatalf("BeforeClean made for %q and called %d times; want the repo, once", gotRepo, calls)
			}
			if tt.wantNote == "" && res.Result.Notes != "" {
				t.Fatalf("Notes = %q, want none", res.Result.Notes)
			}
			if tt.wantNote != "" && (!strings.Contains(res.Result.Notes, tt.wantNote) || !strings.Contains(res.EchoStderr, tt.wantNote)) {
				t.Fatalf("Notes = %q, stderr = %q; want both to carry %q", res.Result.Notes, res.EchoStderr, tt.wantNote)
			}
		})
	}
}
