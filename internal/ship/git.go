package ship

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// RunFunc runs one command in dir and returns what it printed on stdout. It
// has the shape of session.Deps.Run so the daemon can hand one function to
// both packages, and it is the only way this package's adapters reach git and
// gh, so a test can stand in for both with one fake.
//
// Two things about it are relied on by the adapters and must hold for any
// implementation. A command that started and exited non-zero returns an error
// that answers ExitCode() (as *CmdError and *exec.ExitError do), because some
// exit codes are answers, not failures ("is not an ancestor" is exit 1). And
// stdout is returned even when err is not nil, because `gh pr checks` prints
// its result and then exits non-zero when a check is failing or pending.
type RunFunc func(dir, name string, args ...string) (stdout string, err error)

// DefaultTimeout bounds one command. It is 30 seconds, not longer, because the
// daemon ships one project after another in a single pass: one command that
// hangs holds every other project for as long as it is allowed to.
const DefaultTimeout = 30 * time.Second

// killGrace is how long a command gets, after it has been killed for running
// out of time, to let go of its output pipes. git starts helpers (a credential
// helper, ssh) that inherit those pipes and can outlive it; without a bound,
// collecting the output would wait on them and the timeout would not be one.
const killGrace = 2 * time.Second

// CmdError is a command that could not be run or did not exit 0. Its text is
// shown to the owner in inbox items, so it names the command and gives what
// the command itself said, and nothing else.
type CmdError struct {
	// Cmd is the command line, for the owner to read (it is never run).
	Cmd string
	// Code is the exit code, or -1 when the command did not start, was killed
	// or ran out of time.
	Code int
	// Stderr is what the command wrote to stderr, trimmed.
	Stderr string
	// Err is the underlying error.
	Err error
}

func (e *CmdError) Error() string {
	if e.Stderr != "" {
		return e.Cmd + ": " + e.Stderr
	}
	if e.Err != nil {
		return e.Cmd + ": " + e.Err.Error()
	}
	return fmt.Sprintf("%s: exit status %d", e.Cmd, e.Code)
}

func (e *CmdError) Unwrap() error { return e.Err }

// ExitCode is the method the adapters look for; see exited.
func (e *CmdError) ExitCode() int { return e.Code }

// exited reports the exit code of a command that ran and exited non-zero. ok
// is false for an error of any other kind (the binary is missing, the command
// was killed, it timed out), which is never an answer to a question.
func exited(err error) (code int, ok bool) {
	var e interface{ ExitCode() int }
	if !errors.As(err, &e) {
		return 0, false
	}
	code = e.ExitCode()
	return code, code > 0
}

// NewRun returns the real RunFunc. A timeout of zero or less means
// DefaultTimeout.
//
// It never goes through a shell: the name and each argument reach the program
// as they are, so a commit subject holding quotes or `$(` is only ever text.
// Stdin is the null device, so a command that would ask a question reads
// end-of-file and fails instead of waiting for a keyboard that is not there.
func NewRun(timeout time.Duration) RunFunc {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return func(dir, name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		// Stdin is left nil on purpose: os/exec then opens the null device
		// for the child.
		cmd.Stdin = nil
		cmd.WaitDelay = killGrace
		cmd.Env = append(os.Environ(),
			// DeleteRemote reads one of git's messages ("remote ref does not
			// exist"), so git must not translate it.
			"LC_ALL=C",
			// git must fail, not ask for a user name on the terminal the
			// daemon was started from.
			"GIT_TERMINAL_PROMPT=0",
			// The same for gh, and no update notice or colour codes mixed
			// into text that ends up in an inbox item (`gh help environment`).
			"GH_PROMPT_DISABLED=1",
			"GH_NO_UPDATE_NOTIFIER=1",
			"NO_COLOR=1",
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		err := cmd.Run()
		if err == nil {
			return stdout.String(), nil
		}
		ce := &CmdError{
			Cmd:    strings.Join(append([]string{name}, args...), " "),
			Code:   -1,
			Stderr: strings.TrimSpace(stderr.String()),
			Err:    err,
		}
		var ee *exec.ExitError
		switch {
		case ctx.Err() != nil:
			// Whatever the killed command managed to print is not the reason;
			// the reason is the time.
			ce.Stderr = ""
			ce.Err = fmt.Errorf("gave no answer within %s and was stopped", timeout)
		case errors.As(err, &ee):
			ce.Code = ee.ExitCode()
		}
		return stdout.String(), ce
	}
}

// gitCLI is Git over the git command.
//
// Every command runs in the main checkout except Clean. Branches, objects and
// the remote are shared by every worktree of a repository, so the main
// checkout answers the same, and it is the one directory that is still there
// after the session's worktree has been cleaned up (the base update and the
// remote branch delete can both happen then).
type gitCLI struct {
	run      RunFunc
	main     string
	worktree string
}

// NewGit returns Git over real git commands. mainCheckout is the project's
// main checkout; worktree is the session's worktree and may be empty when the
// session is no longer on disk, in which case Clean is an error and every
// other method still works.
func NewGit(run RunFunc, mainCheckout, worktree string) Git {
	return &gitCLI{run: run, main: mainCheckout, worktree: worktree}
}

// originRemote is the only remote a ship talks to. It is a fixed name, not
// read from the branch's upstream setting, because a session branch that has
// never been pushed has no upstream to read.
const originRemote = "origin"

// refName refuses a branch name that git would read as an option. Nothing
// here goes through a shell, but git itself parses a leading "-" as a flag,
// and a branch name comes from a file on disk.
func refName(what, name string) error {
	if name == "" {
		return fmt.Errorf("no %s branch name was given", what)
	}
	if strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("%q is not a usable %s branch name", name, what)
	}
	return nil
}

// localRef names a local branch in full. A bare name can also match a tag or a
// remote-tracking ref of the same name, and git then picks one with a warning;
// the full name cannot be misread.
func localRef(branch string) string { return "refs/heads/" + branch }

func (g *gitCLI) git(args ...string) (string, error) {
	return g.run(g.main, "git", args...)
}

func (g *gitCLI) LocalSHA(branch string) (string, error) {
	if err := refName("session", branch); err != nil {
		return "", err
	}
	out, err := g.git("rev-parse", "--verify", "--quiet", localRef(branch)+"^{commit}")
	if err != nil {
		// --quiet prints nothing for a missing ref, so say what it means.
		if code, ok := exited(err); ok && code == 1 {
			return "", fmt.Errorf("the local branch %s does not exist", branch)
		}
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("git gave no commit for the local branch %s", branch)
	}
	return sha, nil
}

// RemoteSHA asks the remote with ls-remote, which goes over the network every
// time and reads no remote-tracking ref.
//
// ls-remote treats its argument as a pattern matched against the end of each
// ref name, so asking for "fix" also lists "refs/heads/other/fix". Only the
// line whose ref is exactly this branch counts.
func (g *gitCLI) RemoteSHA(branch string) (string, bool, error) {
	if err := refName("session", branch); err != nil {
		return "", false, err
	}
	out, err := g.git("ls-remote", "--heads", originRemote, branch)
	if err != nil {
		return "", false, err
	}
	want := localRef(branch)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == want {
			return fields[0], true, nil
		}
	}
	return "", false, nil
}

// Clean leaves untracked files out with --untracked-files=no: every session
// worktree holds an untracked .afk/ directory.
func (g *gitCLI) Clean() (bool, error) {
	if g.worktree == "" {
		return false, errors.New("the session's worktree is not on disk, so its uncommitted changes cannot be checked")
	}
	out, err := g.run(g.worktree, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

func (g *gitCLI) AheadOfBase(base, branch string) (bool, error) {
	if err := refNames(base, branch); err != nil {
		return false, err
	}
	out, err := g.git("rev-list", "--count", localRef(base)+".."+localRef(branch))
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false, fmt.Errorf("git rev-list --count printed %q, not a number", strings.TrimSpace(out))
	}
	return n > 0, nil
}

func refNames(base, branch string) error {
	if err := refName("base", base); err != nil {
		return err
	}
	return refName("session", branch)
}

func (g *gitCLI) Ahead(branch, remoteSHA string) (bool, error) {
	if err := refName("session", branch); err != nil {
		return false, err
	}
	return g.contains(localRef(branch), remoteSHA)
}

func (g *gitCLI) BaseSynced(base, sha string) (bool, error) {
	if err := refName("base", base); err != nil {
		return false, err
	}
	return g.contains(localRef(base), sha)
}

// contains reports whether sha is ref's head or one of its ancestors.
//
// It asks first whether the repository has the commit at all. A commit that is
// not here is the ordinary case for both callers (the merge commit GitHub just
// made, before the base is fetched; a commit someone pushed to the branch on
// GitHub), and `git merge-base --is-ancestor` answers it with the same exit
// 128 it uses for a broken repository. Asked on its own, "not here" is a plain
// no.
func (g *gitCLI) contains(ref, sha string) (bool, error) {
	if sha == "" || strings.HasPrefix(sha, "-") {
		return false, fmt.Errorf("%q is not a commit id", sha)
	}
	if _, err := g.git("cat-file", "-e", sha+"^{commit}"); err != nil {
		if _, ok := exited(err); ok {
			return false, nil
		}
		return false, err
	}
	if _, err := g.git("merge-base", "--is-ancestor", sha, ref); err != nil {
		// Exit 1 is git's "no, it is not an ancestor". Anything else is a
		// real failure and must not read as "no".
		if code, ok := exited(err); ok && code == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (g *gitCLI) HeadTime(branch string) (time.Time, error) {
	if err := refName("session", branch); err != nil {
		return time.Time{}, err
	}
	out, err := g.git("log", "-1", "--format=%ct", localRef(branch))
	if err != nil {
		return time.Time{}, err
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("git printed %q for the commit time of %s, not a number", strings.TrimSpace(out), branch)
	}
	return time.Unix(secs, 0).UTC(), nil
}

func (g *gitCLI) Title(branch string) (string, string, error) {
	if err := refName("session", branch); err != nil {
		return "", "", err
	}
	subject, err := g.git("log", "-1", "--format=%s", localRef(branch))
	if err != nil {
		return "", "", err
	}
	body, err := g.git("log", "-1", "--format=%b", localRef(branch))
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(subject), strings.TrimSpace(body), nil
}

// AddedLines reads a diff with no context lines against the merge base (the
// three dots), so a line the base gained since the branch was cut is not
// counted as added by the branch.
//
// --no-color and --no-ext-diff keep the owner's git configuration (colour
// always on, an external diff tool) from changing what is parsed here.
func (g *gitCLI) AddedLines(base, branch string) ([]string, error) {
	if err := refNames(base, branch); err != nil {
		return nil, err
	}
	out, err := g.git("diff", "--no-color", "--no-ext-diff", "--unified=0", localRef(base)+"..."+localRef(branch))
	if err != nil {
		return nil, err
	}
	return addedLines(out), nil
}

// addedLines keeps the "+" lines inside hunks. The "+++ b/file" header is told
// apart by where it is (before the file's first "@@"), not by its text: an
// added line whose own text starts with "++" also reads "+++…" in a diff, and
// dropping it by its text would hide a line from the required-env check.
func addedLines(diff string) []string {
	var lines []string
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			lines = append(lines, strings.TrimSuffix(line[1:], "\r"))
		}
	}
	return lines
}

// Push is a plain push of one branch and nothing more. There is no flag here
// that could overwrite the remote, so a rejected push comes back as the error
// it is.
func (g *gitCLI) Push(branch string) error {
	if err := refName("session", branch); err != nil {
		return err
	}
	_, err := g.git("push", originRemote, branch)
	return err
}

// remoteRefGone is what git says when asked to delete a branch the remote
// does not have. It was read from git 2.53.0 run with LC_ALL=C, which NewRun
// sets so the text is not translated.
const remoteRefGone = "remote ref does not exist"

func (g *gitCLI) DeleteRemote(branch string) error {
	if err := refName("session", branch); err != nil {
		return err
	}
	_, err := g.git("push", originRemote, "--delete", branch)
	if err != nil && strings.Contains(err.Error(), remoteRefGone) {
		// GitHub deletes the branch itself when the repository is set to, and
		// a crash after the delete asks again. Both find it gone.
		return nil
	}
	return err
}

// SyncBase fast-forwards the local base to the remote's, and only that.
//
// Which command can do it depends on where the base is checked out. When the
// main checkout is on the base, its files have to move with the branch, so it
// is a merge there, with --ff-only so it can never make a merge commit. When
// it is not, the branch is moved by a fetch into it, which git also refuses
// unless it is a fast-forward (there is no "+" on the refspec). git refuses
// that fetch outright when the base is checked out in some other worktree;
// that is passed on as the error, for the owner to update it there.
func (g *gitCLI) SyncBase(base string) error {
	if err := refName("base", base); err != nil {
		return err
	}
	if _, err := g.git("fetch", originRemote, base); err != nil {
		return err
	}
	current, err := g.git("branch", "--show-current")
	if err != nil {
		return err
	}
	if strings.TrimSpace(current) == base {
		_, err = g.git("merge", "--ff-only", originRemote+"/"+base)
		return err
	}
	_, err = g.git("fetch", originRemote, base+":"+base)
	return err
}
