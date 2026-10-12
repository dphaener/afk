package ship

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// call is one command a scripted RunFunc expects, and what it answers.
type call struct {
	dir  string
	argv []string
	out  string
	err  error
}

// cmd starts an expected call: the directory, then the whole argv.
func cmd(dir string, argv ...string) call { return call{dir: dir, argv: argv} }

// prints sets the stdout of the call.
func (c call) prints(out string) call { c.out = out; return c }

// exits makes the call exit non-zero with the given stderr, as a real command
// that ran and failed does.
func (c call) exits(code int, stderr string) call {
	c.err = &CmdError{Cmd: strings.Join(c.argv, " "), Code: code, Stderr: stderr, Err: fmt.Errorf("exit status %d", code)}
	return c
}

// fails makes the call fail without having run to an exit code.
func (c call) fails(err error) call { c.err = err; return c }

// neverPassed are the flags no command this package runs may carry, whatever
// the method and whatever the arguments. The scripted RunFunc checks every
// argv against them, so a row cannot "expect" one by mistake either.
var neverPassed = []string{"--force", "-f", "--force-with-lease", "--no-verify", "--admin", "--auto", "--delete-branch", "-d"}

// script returns a RunFunc that accepts exactly the given calls, in order,
// with exactly those directories and arguments, and fails the test on
// anything else, including a call that was expected and never made.
func script(t *testing.T, calls ...call) RunFunc {
	t.Helper()
	next := 0
	t.Cleanup(func() {
		if next < len(calls) {
			t.Errorf("command never run: %q", calls[next].argv)
		}
	})
	return func(dir, name string, args ...string) (string, error) {
		argv := append([]string{name}, args...)
		for _, arg := range argv {
			for _, bad := range neverPassed {
				if arg == bad || strings.HasPrefix(arg, bad+"=") {
					t.Errorf("command carries %s: %q", bad, argv)
				}
			}
		}
		if name != "git" && name != "gh" {
			t.Errorf("command is neither git nor gh (a shell?): %q", argv)
		}
		if next >= len(calls) {
			t.Errorf("unexpected command: %q in %s", argv, dir)
			return "", errors.New("unexpected command")
		}
		want := calls[next]
		next++
		if dir != want.dir || !reflect.DeepEqual(argv, want.argv) {
			t.Errorf("command %d:\n got %q in %s\nwant %q in %s", next, argv, dir, want.argv, want.dir)
		}
		return want.out, want.err
	}
}

const (
	mainDir = "/repo"
	wtDir   = "/repo-wt/t2-3"
	shaA    = "7e11c07370c013210a5a92dfd36f1f8419d85758"
	shaB    = "5fb11bff790cd7d877e6be8509283bcacd4fcc4d"
)

// g starts an expected git call in the main checkout.
func g(args ...string) call { return cmd(mainDir, append([]string{"git"}, args...)...) }

// TestGitCommands pins the argv of every Git method and how each answer is
// read. The fake RunFunc fails on any argv that differs by one element.
//
// Load-bearing rows:
//   - "clean: only untracked files": the command itself leaves untracked files
//     out (--untracked-files=no), so empty output is clean. Every session
//     worktree holds an untracked .afk/ directory; counting it would park
//     every ship as dirty.
//   - "clean runs in the worktree": every other command runs in the main
//     checkout, and Clean in the wrong directory would report the owner's
//     checkout, not the session's.
//   - "remote sha: other branch with the same ending": ls-remote matches its
//     argument against the end of ref names, so the answer for "fix" can list
//     "refs/heads/other/fix" alone. That must read as not found, or another
//     branch's commit would be taken for this one's.
//   - "ahead: remote commit not here" and "base synced: merge commit not here":
//     a commit the repository does not have is a plain no. It is the ordinary
//     state right after a merge on GitHub, and reading it as an error would
//     hold the base update behind a failed read.
//   - "ahead: git fails for another reason": exit 128 from merge-base is not
//     "no"; a broken repository must not read as a diverged branch.
//   - "delete remote: already gone": GitHub may delete the branch itself, and
//     a crash after the delete asks again. Both must be success.
//   - "push": exactly `git push origin {branch}`. The fake also refuses every
//     forcing flag on every row.
func TestGitCommands(t *testing.T) {
	tests := []struct {
		name    string
		calls   []call
		do      func(Git) (any, error)
		want    any
		wantErr string
	}{
		{
			name:  "local sha",
			calls: []call{g("rev-parse", "--verify", "--quiet", "refs/heads/t2-3^{commit}").prints(shaA + "\n")},
			do:    func(git Git) (any, error) { return git.LocalSHA("t2-3") },
			want:  shaA,
		},
		{
			name:    "local sha: no such branch",
			calls:   []call{g("rev-parse", "--verify", "--quiet", "refs/heads/t2-3^{commit}").exits(1, "")},
			do:      func(git Git) (any, error) { return git.LocalSHA("t2-3") },
			wantErr: "the local branch t2-3 does not exist",
		},
		{
			name:  "remote sha",
			calls: []call{g("ls-remote", "--heads", "origin", "t2-3").prints(shaA + "\trefs/heads/t2-3\n")},
			do: func(git Git) (any, error) {
				sha, found, err := git.RemoteSHA("t2-3")
				return []any{sha, found}, err
			},
			want: []any{shaA, true},
		},
		{
			name:  "remote sha: branch not on the remote",
			calls: []call{g("ls-remote", "--heads", "origin", "t2-3").prints("")},
			do: func(git Git) (any, error) {
				sha, found, err := git.RemoteSHA("t2-3")
				return []any{sha, found}, err
			},
			want: []any{"", false},
		},
		{
			name:  "remote sha: other branch with the same ending",
			calls: []call{g("ls-remote", "--heads", "origin", "fix").prints(shaB + "\trefs/heads/other/fix\n")},
			do: func(git Git) (any, error) {
				sha, found, err := git.RemoteSHA("fix")
				return []any{sha, found}, err
			},
			want: []any{"", false},
		},
		{
			name: "remote sha: picks the exact branch among several",
			calls: []call{g("ls-remote", "--heads", "origin", "fix").prints(
				shaB + "\trefs/heads/other/fix\n" + shaA + "\trefs/heads/fix\n")},
			do: func(git Git) (any, error) {
				sha, found, err := git.RemoteSHA("fix")
				return []any{sha, found}, err
			},
			want: []any{shaA, true},
		},
		{
			name:    "remote sha: the remote cannot be reached",
			calls:   []call{g("ls-remote", "--heads", "origin", "t2-3").exits(128, "fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com")},
			do:      func(git Git) (any, error) { _, _, err := git.RemoteSHA("t2-3"); return nil, err },
			wantErr: "Could not resolve host",
		},
		{
			name:  "clean runs in the worktree",
			calls: []call{cmd(wtDir, "git", "status", "--porcelain", "--untracked-files=no").prints("")},
			do:    func(git Git) (any, error) { return git.Clean() },
			want:  true,
		},
		{
			// What git prints for a worktree whose only changes are untracked
			// files, with this flag: nothing (seen with git 2.53.0).
			name:  "clean: only untracked files",
			calls: []call{cmd(wtDir, "git", "status", "--porcelain", "--untracked-files=no").prints("\n")},
			do:    func(git Git) (any, error) { return git.Clean() },
			want:  true,
		},
		{
			name:  "clean: a tracked file is changed",
			calls: []call{cmd(wtDir, "git", "status", "--porcelain", "--untracked-files=no").prints(" M main.go\n")},
			do:    func(git Git) (any, error) { return git.Clean() },
			want:  false,
		},
		{
			name:  "ahead of base",
			calls: []call{g("rev-list", "--count", "refs/heads/main..refs/heads/t2-3").prints("3\n")},
			do:    func(git Git) (any, error) { return git.AheadOfBase("main", "t2-3") },
			want:  true,
		},
		{
			name:  "ahead of base: nothing committed",
			calls: []call{g("rev-list", "--count", "refs/heads/main..refs/heads/t2-3").prints("0\n")},
			do:    func(git Git) (any, error) { return git.AheadOfBase("main", "t2-3") },
			want:  false,
		},
		{
			name:    "ahead of base: not a number",
			calls:   []call{g("rev-list", "--count", "refs/heads/main..refs/heads/t2-3").prints("many\n")},
			do:      func(git Git) (any, error) { return git.AheadOfBase("main", "t2-3") },
			wantErr: "not a number",
		},
		{
			name: "ahead: remote head is an ancestor",
			calls: []call{
				g("cat-file", "-e", shaA+"^{commit}"),
				g("merge-base", "--is-ancestor", shaA, "refs/heads/t2-3"),
			},
			do:   func(git Git) (any, error) { return git.Ahead("t2-3", shaA) },
			want: true,
		},
		{
			name: "ahead: remote head is not an ancestor",
			calls: []call{
				g("cat-file", "-e", shaA+"^{commit}"),
				g("merge-base", "--is-ancestor", shaA, "refs/heads/t2-3").exits(1, ""),
			},
			do:   func(git Git) (any, error) { return git.Ahead("t2-3", shaA) },
			want: false,
		},
		{
			name: "ahead: remote commit not here",
			calls: []call{
				g("cat-file", "-e", shaA+"^{commit}").exits(128, "fatal: Not a valid object name "+shaA+"^{commit}"),
			},
			do:   func(git Git) (any, error) { return git.Ahead("t2-3", shaA) },
			want: false,
		},
		{
			name: "ahead: git fails for another reason",
			calls: []call{
				g("cat-file", "-e", shaA+"^{commit}"),
				g("merge-base", "--is-ancestor", shaA, "refs/heads/t2-3").exits(128, "fatal: Not a valid commit name refs/heads/t2-3"),
			},
			do:      func(git Git) (any, error) { return git.Ahead("t2-3", shaA) },
			wantErr: "Not a valid commit name",
		},
		{
			name: "ahead: git cannot be run",
			calls: []call{
				g("cat-file", "-e", shaA+"^{commit}").fails(exec.ErrNotFound),
			},
			do:      func(git Git) (any, error) { return git.Ahead("t2-3", shaA) },
			wantErr: "executable file not found",
		},
		{
			name: "base synced",
			calls: []call{
				g("cat-file", "-e", shaB+"^{commit}"),
				g("merge-base", "--is-ancestor", shaB, "refs/heads/main"),
			},
			do:   func(git Git) (any, error) { return git.BaseSynced("main", shaB) },
			want: true,
		},
		{
			name: "base synced: merge commit not here",
			calls: []call{
				g("cat-file", "-e", shaB+"^{commit}").exits(128, "fatal: Not a valid object name "+shaB+"^{commit}"),
			},
			do:   func(git Git) (any, error) { return git.BaseSynced("main", shaB) },
			want: false,
		},
		{
			name: "base synced: fetched but the base is behind",
			calls: []call{
				g("cat-file", "-e", shaB+"^{commit}"),
				g("merge-base", "--is-ancestor", shaB, "refs/heads/main").exits(1, ""),
			},
			do:   func(git Git) (any, error) { return git.BaseSynced("main", shaB) },
			want: false,
		},
		{
			name:  "head time",
			calls: []call{g("log", "-1", "--format=%ct", "refs/heads/t2-3").prints("1791741600\n")},
			do:    func(git Git) (any, error) { return git.HeadTime("t2-3") },
			want:  time.Unix(1791741600, 0).UTC(),
		},
		{
			name:    "head time: not a number",
			calls:   []call{g("log", "-1", "--format=%ct", "refs/heads/t2-3").prints("\n")},
			do:      func(git Git) (any, error) { return git.HeadTime("t2-3") },
			wantErr: "not a number",
		},
		{
			name: "title and body",
			calls: []call{
				g("log", "-1", "--format=%s", "refs/heads/t2-3").prints("feat(ship): Add the adapters\n"),
				g("log", "-1", "--format=%b", "refs/heads/t2-3").prints("Why it was needed.\n\nSecond paragraph.\n\n"),
			},
			do: func(git Git) (any, error) {
				s, b, err := git.Title("t2-3")
				return []any{s, b}, err
			},
			want: []any{"feat(ship): Add the adapters", "Why it was needed.\n\nSecond paragraph."},
		},
		{
			name: "added lines",
			calls: []call{g("diff", "--no-color", "--no-ext-diff", "--unified=0", "refs/heads/main...refs/heads/t2-3").prints(
				"diff --git a/app.go b/app.go\nindex 1..2 100644\n--- a/app.go\n+++ b/app.go\n@@ -3,0 +4 @@ func main() {\n+\tkey := os.Getenv(\"STRIPE_KEY\")\n")},
			do:   func(git Git) (any, error) { return git.AddedLines("main", "t2-3") },
			want: []string{"\tkey := os.Getenv(\"STRIPE_KEY\")"},
		},
		{
			name:  "push",
			calls: []call{g("push", "origin", "t2-3")},
			do:    func(git Git) (any, error) { return nil, git.Push("t2-3") },
		},
		{
			name: "push: rejected because the remote has other commits",
			calls: []call{g("push", "origin", "t2-3").exits(1,
				"To github.com:x/y.git\n ! [rejected]        t2-3 -> t2-3 (fetch first)\nerror: failed to push some refs to 'github.com:x/y.git'")},
			do:      func(git Git) (any, error) { return nil, git.Push("t2-3") },
			wantErr: "[rejected]",
		},
		{
			name:  "delete remote",
			calls: []call{g("push", "origin", "--delete", "t2-3")},
			do:    func(git Git) (any, error) { return nil, git.DeleteRemote("t2-3") },
		},
		{
			// The text is what git 2.53.0 printed for a branch the remote did
			// not have.
			name: "delete remote: already gone",
			calls: []call{g("push", "origin", "--delete", "t2-3").exits(1,
				"error: unable to delete 't2-3': remote ref does not exist\nerror: failed to push some refs to 'github.com:x/y.git'")},
			do: func(git Git) (any, error) { return nil, git.DeleteRemote("t2-3") },
		},
		{
			name: "delete remote: refused for another reason",
			calls: []call{g("push", "origin", "--delete", "t2-3").exits(1,
				"remote: error: GH006: Protected branch update failed for refs/heads/t2-3.")},
			do:      func(git Git) (any, error) { return nil, git.DeleteRemote("t2-3") },
			wantErr: "Protected branch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.do(NewGit(script(t, tt.calls...), mainDir, wtDir))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestGitSyncBase covers the base update (assumption A9).
//
// Load-bearing rows:
//   - "ff-only refused": the main checkout's base has commits GitHub does not.
//     It must be an error, and no second command may follow it (the script
//     fails on any extra call): a fallback to a real merge or a reset is
//     exactly what this method must never do.
//   - "base checked out in another worktree": git refuses the fetch into the
//     branch. The error must carry git's own sentence, because the inbox item
//     shows it to the owner as the reason.
//   - "main checkout on another branch": the fetch refspec has no leading "+",
//     which is what makes git refuse anything but a fast-forward.
func TestGitSyncBase(t *testing.T) {
	const refused = "fatal: refusing to fetch into branch 'refs/heads/main' checked out at '/repo-wt/other'"
	tests := []struct {
		name    string
		calls   []call
		wantErr string
	}{
		{
			name: "main checkout on the base: fast-forward merge",
			calls: []call{
				g("fetch", "origin", "main"),
				g("branch", "--show-current").prints("main\n"),
				g("merge", "--ff-only", "origin/main"),
			},
		},
		{
			name: "main checkout on another branch: fetch into the base",
			calls: []call{
				g("fetch", "origin", "main"),
				g("branch", "--show-current").prints("t2-9\n"),
				g("fetch", "origin", "main:main"),
			},
		},
		{
			name: "main checkout on no branch: fetch into the base",
			calls: []call{
				g("fetch", "origin", "main"),
				g("branch", "--show-current").prints("\n"),
				g("fetch", "origin", "main:main"),
			},
		},
		{
			name: "ff-only refused",
			calls: []call{
				g("fetch", "origin", "main"),
				g("branch", "--show-current").prints("main\n"),
				g("merge", "--ff-only", "origin/main").exits(128, "fatal: Not possible to fast-forward, aborting."),
			},
			wantErr: "Not possible to fast-forward",
		},
		{
			name: "base checked out in another worktree",
			calls: []call{
				g("fetch", "origin", "main"),
				g("branch", "--show-current").prints("t2-9\n"),
				g("fetch", "origin", "main:main").exits(128, refused),
			},
			wantErr: refused,
		},
		{
			name: "the remote cannot be reached",
			calls: []call{
				g("fetch", "origin", "main").exits(128, "fatal: unable to access 'https://github.com/x/y.git/'"),
			},
			wantErr: "unable to access",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewGit(script(t, tt.calls...), mainDir, wtDir).SyncBase("main")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestGitRefusesUnusableNames checks that nothing is run for a name git would
// read as an option, or with no worktree to look at. The script has no calls,
// so any command fails the test.
//
// The load-bearing rows are the leading "-" ones: there is no shell here, but
// git parses its own arguments, and `git push origin --delete` shows how much
// a single flag-shaped argument can change.
func TestGitRefusesUnusableNames(t *testing.T) {
	tests := []struct {
		name string
		do   func(Git) error
	}{
		{"push: flag-shaped branch", func(git Git) error { return git.Push("--force") }},
		{"push: empty branch", func(git Git) error { return git.Push("") }},
		{"push: branch with a space", func(git Git) error { return git.Push("a b") }},
		{"delete remote: flag-shaped branch", func(git Git) error { return git.DeleteRemote("--all") }},
		{"sync base: flag-shaped base", func(git Git) error { return git.SyncBase("--upload-pack=x") }},
		{"sync base: empty base", func(git Git) error { return git.SyncBase("") }},
		{"remote sha: flag-shaped branch", func(git Git) error { _, _, err := git.RemoteSHA("-x"); return err }},
		{"local sha: empty branch", func(git Git) error { _, err := git.LocalSHA(""); return err }},
		{"ahead: flag-shaped sha", func(git Git) error { _, err := git.Ahead("t2-3", "--all"); return err }},
		{"ahead: empty sha", func(git Git) error { _, err := git.Ahead("t2-3", ""); return err }},
		{"base synced: empty base", func(git Git) error { _, err := git.BaseSynced("", shaA); return err }},
		{"added lines: empty base", func(git Git) error { _, err := git.AddedLines("", "t2-3"); return err }},
		{"ahead of base: flag-shaped branch", func(git Git) error { _, err := git.AheadOfBase("main", "-n"); return err }},
		{"title: empty branch", func(git Git) error { _, _, err := git.Title(""); return err }},
		{"head time: empty branch", func(git Git) error { _, err := git.HeadTime(""); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.do(NewGit(script(t), mainDir, wtDir)); err == nil {
				t.Fatal("no error")
			}
		})
	}
	t.Run("clean: no worktree on disk", func(t *testing.T) {
		clean, err := NewGit(script(t), mainDir, "").Clean()
		if err == nil || clean {
			t.Fatalf("Clean() = %v, %v; want false and an error", clean, err)
		}
	})
}

// TestGitAddedLinesParsing is the diff reader on its own.
//
// Load-bearing rows:
//   - "added line that starts with ++": in a diff it reads "+++…", the same as
//     a file header. It is an added line and must be kept, or a required
//     environment variable on it would be missed.
//   - "file headers are not lines": "+++ b/file" must never come back as an
//     added line, or a file named like a variable would park a merge.
//   - "removed and context lines": only "+" lines count.
func TestGitAddedLinesParsing(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []string
	}{
		{name: "empty diff", diff: "", want: nil},
		{
			name: "file headers are not lines",
			diff: "diff --git a/STRIPE_KEY b/STRIPE_KEY\nnew file mode 100644\nindex 0000000..e69de29\n--- /dev/null\n+++ b/STRIPE_KEY\n",
			want: nil,
		},
		{
			name: "added line that starts with ++",
			diff: "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,0 +2,2 @@\n+++ counter\n+plain\n",
			want: []string{"++ counter", "plain"},
		},
		{
			name: "removed and context lines",
			diff: "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,2 +1,2 @@\n-old\n+new\n context\n\\ No newline at end of file\n",
			want: []string{"new"},
		},
		{
			name: "two files, two hunks each",
			diff: "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n+one\n@@ -9 +9 @@\n+two\n" +
				"diff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@\n+three\n",
			want: []string{"one", "two", "three"},
		},
		{
			name: "windows line ends and an empty added line",
			diff: "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1,2 @@\n+KEY=1\r\n+\n",
			want: []string{"KEY=1", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addedLines(tt.diff); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("addedLines() = %q, want %q", got, tt.want)
			}
		})
	}
}

// helperEnv tells the test binary to act as a stand-in command instead of
// running tests. The real RunFunc is tested against the test binary itself so
// the test needs no other program on the machine.
const helperEnv = "AFK_SHIP_TEST_HELPER"

// TestGitRunHelper is not a test. NewRun's tests start this binary again with
// helperEnv set, and it behaves as the command they need.
func TestGitRunHelper(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch mode {
	case "echo":
		wd, _ := os.Getwd()
		fmt.Printf("%s\n%d\n", wd, len(args))
		for _, a := range args {
			fmt.Println(a)
		}
		os.Exit(0)
	case "fail":
		fmt.Print("printed before failing")
		fmt.Fprint(os.Stderr, "\n  the reason it failed  \n\n")
		os.Exit(3)
	case "stdin":
		data, err := io.ReadAll(os.Stdin)
		fmt.Printf("read %d bytes, err %v", len(data), err)
		os.Exit(0)
	case "env":
		fmt.Printf("%s|%s|%s", os.Getenv("LC_ALL"), os.Getenv("GIT_TERMINAL_PROMPT"), os.Getenv("GH_PROMPT_DISABLED"))
		os.Exit(0)
	case "hang":
		// Long enough that only being killed ends it inside the test.
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(2)
}

// helper runs the test binary as a stand-in command through run.
func helper(t *testing.T, run RunFunc, mode, dir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv(helperEnv, mode)
	return run(dir, os.Args[0], append([]string{"-test.run=^TestGitRunHelper$", "--"}, args...)...)
}

// TestGitRunFunc checks the real RunFunc against a stand-in command.
//
// Load-bearing rows:
//   - "arguments arrive as they are": a subject with quotes, `$(…)`, a
//     backtick and a newline reaches the program as one argument each. That
//     is the proof no shell is in between.
//   - "non-zero exit": stdout is still returned (Checks depends on it), the
//     exit code is readable through exited (Ahead and BaseSynced depend on
//     it), and the error text is the command plus trimmed stderr.
//   - "stdin is closed": a command that asks a question reads end-of-file at
//     once instead of waiting for a keyboard.
//   - "timeout": a command that hangs is stopped, and the error says so
//     rather than reading as an exit code.
func TestGitRunFunc(t *testing.T) {
	t.Run("arguments arrive as they are", func(t *testing.T) {
		dir := t.TempDir()
		tricky := []string{`Fix "quotes"`, "$(touch " + filepath.Join(dir, "pwned") + ")", "`id`", "a b  c", "--", ""}
		out, err := helper(t, NewRun(0), "echo", dir, tricky...)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		wd, _ := filepath.EvalSymlinks(lines[0])
		wantDir, _ := filepath.EvalSymlinks(dir)
		if wd != wantDir {
			t.Errorf("ran in %s, want %s", wd, wantDir)
		}
		want := append([]string{fmt.Sprint(len(tricky))}, tricky...)
		if got := lines[1:]; !reflect.DeepEqual(got, want) {
			t.Errorf("arguments = %q, want %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
			t.Error("the $(…) argument was run by a shell")
		}
	})

	t.Run("non-zero exit", func(t *testing.T) {
		out, err := helper(t, NewRun(0), "fail", t.TempDir(), "one", "two")
		if out != "printed before failing" {
			t.Errorf("stdout = %q, want what was printed before the failure", out)
		}
		code, ok := exited(err)
		if !ok || code != 3 {
			t.Fatalf("exited(err) = %d, %v; want 3, true (err: %v)", code, ok, err)
		}
		var ce *CmdError
		if !errors.As(err, &ce) {
			t.Fatalf("error is %T, want *CmdError", err)
		}
		if ce.Stderr != "the reason it failed" {
			t.Errorf("stderr = %q, want it trimmed", ce.Stderr)
		}
		text := err.Error()
		if !strings.HasSuffix(text, "-- one two: the reason it failed") || !strings.HasPrefix(text, os.Args[0]) {
			t.Errorf("error text = %q, want the command then the reason", text)
		}
	})

	t.Run("stdin is closed", func(t *testing.T) {
		out, err := helper(t, NewRun(0), "stdin", t.TempDir())
		if err != nil || out != "read 0 bytes, err <nil>" {
			t.Errorf("got %q, %v; want an immediate end-of-file", out, err)
		}
	})

	t.Run("environment", func(t *testing.T) {
		t.Setenv("LC_ALL", "de_DE.UTF-8")
		out, err := helper(t, NewRun(0), "env", t.TempDir())
		if err != nil || out != "C|0|1" {
			t.Errorf("got %q, %v; want untranslated messages and no prompts", out, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		_, err := helper(t, NewRun(300*time.Millisecond), "hang", t.TempDir())
		if err == nil {
			t.Fatal("a command that hangs returned no error")
		}
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("took %s to give up on a 300ms timeout", took)
		}
		if _, ok := exited(err); ok {
			t.Errorf("a timeout reads as an exit code: %v", err)
		}
		if !strings.Contains(err.Error(), "gave no answer within 300ms") {
			t.Errorf("error text = %q, want it to say the command gave no answer in time", err)
		}
	})

	t.Run("missing program", func(t *testing.T) {
		_, err := NewRun(0)(t.TempDir(), "afk-no-such-program-for-this-test", "x")
		if !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("error = %v, want one that is exec.ErrNotFound", err)
		}
		if _, ok := exited(err); ok {
			t.Error("a program that never started reads as an exit code")
		}
	})

	t.Run("default timeout is thirty seconds", func(t *testing.T) {
		if DefaultTimeout != 30*time.Second {
			t.Errorf("DefaultTimeout = %s; a pass runs every project in turn, so this must stay 30s", DefaultTimeout)
		}
	})
}

// TestGitAgainstRealRepo runs the real adapter over real git, against a
// scratch repository whose "origin" is a bare repository in the same
// temporary directory. Nothing leaves the machine and no real session or
// project is touched.
//
// It exists because the scripted rows above can only say "if git answers like
// this"; this is where git's actual exit codes and messages are checked: a
// commit that is not in the repository, a delete of a branch that is already
// gone, a push the remote rejects, a fast-forward that is refused, and a fetch
// into a branch that another worktree has checked out.
//
// Load-bearing steps:
//   - the rejected push leaves the remote branch where it was: Push never
//     overwrites.
//   - the refused fast-forward leaves the local base where it was: SyncBase
//     never merges or resets.
func TestGitAgainstRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	// Keep the owner's git configuration (signing, hooks, templates, a
	// default branch name) out of the scratch repositories.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("HOME", root)
	t.Setenv("GIT_AUTHOR_NAME", "afk test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "afk test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")

	run := NewRun(0)
	origin := filepath.Join(root, "origin.git")
	main := filepath.Join(root, "main")
	wt := filepath.Join(root, "wt")
	other := filepath.Join(root, "other") // a second clone: "somebody else", and GitHub's merge

	do := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(dir, "git", args...)
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		return strings.TrimSpace(out)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(dir, file, text, message string) string {
		t.Helper()
		write(filepath.Join(dir, file), text)
		do(dir, "add", file)
		do(dir, "commit", "-q", "-m", message)
		return do(dir, "rev-parse", "HEAD")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	do(root, "init", "-q", "--bare", "-b", "main", origin)
	do(root, "init", "-q", "-b", "main", main)
	do(main, "remote", "add", "origin", origin)
	commit(main, "app.txt", "one\n", "first")
	do(main, "push", "-q", "origin", "main")
	do(main, "worktree", "add", "-q", "-b", "feat", wt)
	do(root, "clone", "-q", origin, other)

	git := NewGit(run, main, wt)

	// Nothing committed yet, and only an untracked .afk/ directory.
	write(filepath.Join(wt, ".afk", "feat", "status.json"), "{}\n")
	if ahead, err := git.AheadOfBase("main", "feat"); err != nil || ahead {
		t.Errorf("AheadOfBase before any commit = %v, %v; want false", ahead, err)
	}
	if clean, err := git.Clean(); err != nil || !clean {
		t.Errorf("Clean with only untracked files = %v, %v; want true", clean, err)
	}
	write(filepath.Join(wt, "app.txt"), "one\nedited\n")
	if clean, err := git.Clean(); err != nil || clean {
		t.Errorf("Clean with a changed tracked file = %v, %v; want false", clean, err)
	}

	head := commit(wt, "app.txt", "one\n++ counter\nSTRIPE_KEY=x\n", "feat: add \"the\" thing $(id)\n\nThe body.\n\nMore body.")
	if clean, err := git.Clean(); err != nil || !clean {
		t.Errorf("Clean after the commit = %v, %v; want true", clean, err)
	}
	if ahead, err := git.AheadOfBase("main", "feat"); err != nil || !ahead {
		t.Errorf("AheadOfBase after a commit = %v, %v; want true", ahead, err)
	}
	if sha, err := git.LocalSHA("feat"); err != nil || sha != head {
		t.Errorf("LocalSHA = %q, %v; want %q", sha, err, head)
	}
	if _, err := git.LocalSHA("no-such-branch"); err == nil {
		t.Error("LocalSHA of a branch that does not exist returned no error")
	}
	subject, body, err := git.Title("feat")
	must(err)
	if subject != `feat: add "the" thing $(id)` || body != "The body.\n\nMore body." {
		t.Errorf("Title = %q, %q", subject, body)
	}
	at, err := git.HeadTime("feat")
	must(err)
	if age := time.Since(at); age < -time.Minute || age > time.Hour {
		t.Errorf("HeadTime = %s, which is not just now", at)
	}
	lines, err := git.AddedLines("main", "feat")
	must(err)
	if want := []string{"++ counter", "STRIPE_KEY=x"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("AddedLines = %q, want %q", lines, want)
	}

	// Push, and read it back from the remote.
	if _, found, err := git.RemoteSHA("feat"); err != nil || found {
		t.Errorf("RemoteSHA before the push: found = %v, err = %v; want not found", found, err)
	}
	must(git.Push("feat"))
	if sha, found, err := git.RemoteSHA("feat"); err != nil || !found || sha != head {
		t.Errorf("RemoteSHA after the push = %q, %v, %v; want %q", sha, found, err, head)
	}
	if ahead, err := git.Ahead("feat", head); err != nil || !ahead {
		t.Errorf("Ahead of its own head = %v, %v; want true", ahead, err)
	}

	// Somebody else pushes to the branch. Their commit is not in this
	// repository: not ahead, no error, and a push is rejected, not forced.
	do(other, "fetch", "-q", "origin", "feat")
	do(other, "checkout", "-q", "-b", "feat", "origin/feat")
	theirs := commit(other, "theirs.txt", "x\n", "theirs")
	do(other, "push", "-q", "origin", "feat")
	if ahead, err := git.Ahead("feat", theirs); err != nil || ahead {
		t.Errorf("Ahead of a commit this repository does not have = %v, %v; want false, nil", ahead, err)
	}
	commit(wt, "mine.txt", "y\n", "mine")
	if err := git.Push("feat"); err == nil {
		t.Error("a push over somebody else's commit was not rejected")
	}
	if sha, _, _ := git.RemoteSHA("feat"); sha != theirs {
		t.Errorf("after the rejected push the remote branch is at %q, want it left at %q", sha, theirs)
	}
	// Once their commit is here, the local branch is simply not ahead of it.
	do(main, "fetch", "-q", "origin", "feat")
	if ahead, err := git.Ahead("feat", theirs); err != nil || ahead {
		t.Errorf("Ahead of a commit beside the local head = %v, %v; want false, nil", ahead, err)
	}

	// "GitHub merges": a new commit on the remote base.
	mergeOnRemote := func(n int) string {
		t.Helper()
		do(other, "checkout", "-q", "main")
		do(other, "pull", "-q", "--ff-only", "origin", "main")
		sha := commit(other, fmt.Sprintf("merged-%d.txt", n), "m\n", fmt.Sprintf("merge %d", n))
		do(other, "push", "-q", "origin", "main")
		return sha
	}

	// The main checkout is on the base: a fast-forward merge there.
	merged := mergeOnRemote(1)
	if synced, err := git.BaseSynced("main", merged); err != nil || synced {
		t.Errorf("BaseSynced before the update = %v, %v; want false, nil", synced, err)
	}
	must(git.SyncBase("main"))
	if synced, err := git.BaseSynced("main", merged); err != nil || !synced {
		t.Errorf("BaseSynced after the update = %v, %v; want true", synced, err)
	}
	if _, err := os.Stat(filepath.Join(main, "merged-1.txt")); err != nil {
		t.Errorf("the main checkout's files did not move with its branch: %v", err)
	}

	// The main checkout is on another branch: a fetch into the base.
	do(main, "checkout", "-q", "-b", "elsewhere")
	merged = mergeOnRemote(2)
	must(git.SyncBase("main"))
	if synced, err := git.BaseSynced("main", merged); err != nil || !synced {
		t.Errorf("BaseSynced after a fetch into the base = %v, %v; want true", synced, err)
	}

	// The base is checked out in a linked worktree: git refuses, and says so.
	linked := filepath.Join(root, "linked")
	do(main, "worktree", "add", "-q", linked, "main")
	merged = mergeOnRemote(3)
	err = git.SyncBase("main")
	if err == nil || !strings.Contains(err.Error(), "refusing to fetch into branch") {
		t.Errorf("SyncBase with the base checked out elsewhere: error = %v, want git's refusal", err)
	}
	if synced, err := git.BaseSynced("main", merged); err != nil || synced {
		t.Errorf("BaseSynced after the refused fetch = %v, %v; want false, nil", synced, err)
	}
	do(main, "worktree", "remove", linked)

	// The main checkout's base has a commit GitHub does not: no fast-forward.
	do(main, "checkout", "-q", "main")
	localOnly := commit(main, "local-only.txt", "z\n", "local only")
	err = git.SyncBase("main")
	if err == nil {
		t.Fatal("SyncBase fast-forwarded a base that has its own commits")
	}
	if now, _ := git.LocalSHA("main"); now != localOnly {
		t.Errorf("after the refused fast-forward the local base is at %q, want it left at %q", now, localOnly)
	}

	// Delete the remote branch, twice.
	must(git.DeleteRemote("feat"))
	if _, found, err := git.RemoteSHA("feat"); err != nil || found {
		t.Errorf("RemoteSHA after the delete: found = %v, err = %v; want not found", found, err)
	}
	if err := git.DeleteRemote("feat"); err != nil {
		t.Errorf("deleting a branch that is already gone: %v; want no error", err)
	}
}
