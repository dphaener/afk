package planpatch

// The executor behind afk plan patch and its helpers: it takes the lock,
// reads the plan and the ledger, applies one guarded patch, writes the plan
// atomically and commits it, either on the main checkout's branch or, with
// [planning] via_pr, on the branch afk-plan-changes in a temporary worktree
// with a pull request.
//
// Every git behaviour a comment in this file states was checked by running
// the command against git 2.53.0 in a scratch repository (the record is the
// git-facts evidence of T5.4 W3.T1). The gh calls were written against the
// field list `gh pr list --json` prints for gh 2.102.0.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/ship"
)

// Options says which plan to patch, how to build the patch and where the
// change goes.
type Options struct {
	// Root is the main checkout, or the folder holding the plan when it is
	// not a git repository.
	Root string
	// PlanPath is the absolute path of the master plan under Root.
	PlanPath string
	// Edit builds the patch from the plan as it was actually read, under the
	// lock, so a builder never works from a plan another writer has changed
	// since.
	Edit func(plan []byte) (Edit, error)
	// Message is the commit subject; "" means DefaultMessage. One line only.
	Message string
	// ViaPR is [planning] via_pr: commit on a branch and open a pull request
	// instead of committing on the main checkout's branch.
	ViaPR bool
	// Base is [ship] base. Direct mode refuses when the main checkout is on
	// another branch; pull-request mode branches from it. "" means the
	// branch the main checkout is on.
	Base string
}

// Outcome says what Run did. When Run returns an error the Outcome is still
// worth reading: Touched is set once Apply accepted the patch, and Notes
// may say where a worktree was left.
type Outcome struct {
	// Touched holds the ids of the tasks whose entries the patch changed.
	Touched []string
	// Committed is true once git made the commit (on the branch or in the
	// worktree).
	Committed bool
	// Pushed is true once the commit reached origin.
	Pushed bool
	// PR is the pull request's address, pull-request mode only.
	PR string
	// Notes are plain-language lines for the owner: why nothing was
	// committed, how to push by hand, where the change went.
	Notes []string
}

// Deps is what Run needs from outside the package. File reads, the lock and
// the atomic write are real; only the commands and the temporary folder are
// handed in, so a test can script git and gh and never run them.
type Deps struct {
	// Run runs one git or gh command in a folder. It is the shape of
	// ship.RunFunc: a command that exited non-zero returns an error that
	// answers ExitCode(), and stdout comes back even then.
	Run ship.RunFunc
	// TempDir makes a new empty folder for the pull-request worktree and
	// returns its path. git worktree add accepts an empty folder that already
	// exists (checked; a folder with anything in it is refused).
	TempDir func() (string, error)
}

// DefaultMessage is the commit subject when Options.Message is empty.
const DefaultMessage = "docs(plan): Patch the master plan"

// PRBranch is the one branch every pull-request-mode patch is committed on,
// so a second patch builds on the first until the pull request is merged.
const PRBranch = "afk-plan-changes"

// lockName is the lock file under Root/.afk. It is separate from the plan
// because the plan is replaced by rename on every write, and a lock held on
// the old file would stop excluding anyone the moment the new one took its
// name (the same reason the ledger has its own lock file).
const lockName = "plan-patch.lock"

// Run applies one guarded patch to the plan and commits it. On an error
// before the write nothing on disk has changed; the doc of each step below
// says what is left when a step after the write fails.
//
// The steps run in this order: the message is checked (a pure check, so it
// comes before the lock), the lock is taken and held to the end, the ledger
// is read (a damaged ledger stops everything, because rule 8 cannot be
// checked without it), git says whether there is a repository and whether
// the plan is clean, and then one of three paths: in place, direct or
// pull request. The patch itself (read, Edit, Apply, write) happens inside
// the path, after its refusals, so a refused patch never reads the plan
// twice and a refused branch check never builds a patch.
func Run(d Deps, o Options) (Outcome, error) {
	if d.Run == nil {
		return Outcome{}, errors.New("planpatch.Run needs a command runner")
	}
	if o.Edit == nil {
		return Outcome{}, errors.New("planpatch.Run needs an Edit function")
	}
	if strings.ContainsAny(o.Message, "\r\n") {
		return Outcome{}, errors.New("the commit message must be one line, so the patch was not made. Give a short subject with --message and no line breaks")
	}
	if o.Message == "" {
		o.Message = DefaultMessage
	}
	rel, err := planRel(o.Root, o.PlanPath)
	if err != nil {
		return Outcome{}, err
	}

	unlock, err := lock(o.Root)
	if err != nil {
		return Outcome{}, err
	}
	defer unlock()

	held, err := heldTasks(o.Root)
	if err != nil {
		return Outcome{}, err
	}
	r := runner{d: d, o: o, rel: rel, held: held}

	// A folder that is not a repository, or where git cannot run at all, is
	// patched in place: the plan is still the plan, and the owner can commit
	// it however the project is kept. The probe is the one git command that
	// runs in a folder that may not be a repository.
	if _, err := d.Run(o.Root, "git", "rev-parse", "--is-inside-work-tree"); err != nil {
		note := o.Root + " is not a git repository, so the plan was changed in place and nothing was committed"
		if _, exited := exitCode(err); !exited {
			note = "git could not be run (" + err.Error() + "), so the plan was changed in place and nothing was committed"
		}
		return r.inPlace(note)
	}
	// `git status --porcelain -- <plan>` prints one line for the plan when it
	// is untracked ("?? plan") or has uncommitted changes (" M plan"), and
	// nothing when it is tracked and clean. A first draft that is not
	// committed yet must stay patchable, and a plan with the owner's own
	// edits in progress must not have them swept into afk's commit, so both
	// are patched in place and left for the owner to commit.
	out, err := d.Run(o.Root, "git", "status", "--porcelain", "--", rel)
	if err != nil {
		return Outcome{}, fmt.Errorf("git could not say whether the plan is committed: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		return r.inPlace("the plan has changes that are not committed yet, so this one was not committed either")
	}
	if o.ViaPR {
		return r.viaPR()
	}
	return r.direct()
}

// runner carries what every path needs once the lock is held.
type runner struct {
	d    Deps
	o    Options
	rel  string
	held map[string]string
}

// inPlace patches the file in the main checkout and runs no git at all.
func (r runner) inPlace(note string) (Outcome, error) {
	touched, err := r.patch(r.o.PlanPath)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Touched: touched, Notes: []string{note}}, nil
}

// direct patches the plan on the branch the main checkout is on, commits
// only the plan and pushes when there is an origin.
//
// A failed add or commit leaves the patched file in place and returns an
// error that carries git's own words and the command to run by hand: the
// patch was checked and is right, so undoing it would throw away the one
// thing that worked. A failed push is not an error, only a note with the
// command, because the plan on disk is already what the owner asked for and
// the push can be done when the network is back.
func (r runner) direct() (Outcome, error) {
	branch, err := r.currentBranch()
	if err != nil {
		return Outcome{}, err
	}
	if branch == "" {
		return Outcome{}, errors.New("the main checkout is not on a branch (detached HEAD), so the patch was not made. Check out a branch and run the command again")
	}
	if r.o.Base != "" && branch != r.o.Base {
		return Outcome{}, fmt.Errorf("the main checkout is on branch %s, not on %s, so the patch was not made. Check out %s and run the command again, or set [planning] via_pr = true in afk.toml to have the change made on its own branch", branch, r.o.Base, r.o.Base)
	}

	touched, err := r.patch(r.o.PlanPath)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Touched: touched}
	if err := r.commit(r.o.Root); err != nil {
		return out, err
	}
	out.Committed = true

	// `git remote get-url origin` exits 2 with "No such remote 'origin'" when
	// there is none; any failure here reads as no origin, because the push
	// would fail for the same reason and a note is the right answer either way.
	if _, err := r.d.Run(r.o.Root, "git", "remote", "get-url", "origin"); err != nil {
		out.Notes = append(out.Notes, "the commit was not pushed: the repository has no origin remote")
		return out, nil
	}
	// A plain push of one branch and nothing more: there is no flag here
	// that could overwrite the remote, so a rejected push is reported as
	// what it is.
	if _, err := r.d.Run(r.o.Root, "git", "push", "origin", branch); err != nil {
		out.Notes = append(out.Notes, fmt.Sprintf("the commit was made but not pushed (%v). To push it by hand, run: git push origin %s", err, branch))
		return out, nil
	}
	out.Pushed = true
	return out, nil
}

// viaPR makes the change on PRBranch in a temporary worktree and opens or
// reuses a pull request. The main checkout is never switched and its plan is
// never written: the daemon reads the main checkout all the time, and the
// owner may be working there.
//
// The branch is reused while its pull request is open, so a second patch
// builds on the first. Once the pull request is merged the branch is an
// ancestor of the base and is moved back onto the base. A branch with
// commits of its own and no open pull request is neither: its pull request
// was closed without merging, or merged into a base the main checkout has
// not pulled yet, and only the owner can say which.
//
// The results are named so the deferred worktree removal can add its note
// to the Outcome that is actually returned, on the error paths too.
func (r runner) viaPR() (out Outcome, err error) {
	root := r.o.Root
	if _, err := r.d.Run(root, "git", "remote", "get-url", "origin"); err != nil {
		return Outcome{}, errors.New("[planning] via_pr is set but the repository has no origin remote, so there is nowhere to open a pull request. Add one with git remote add origin <address>, or set via_pr = false to commit on the current branch")
	}
	base := r.o.Base
	if base == "" {
		branch, err := r.currentBranch()
		if err != nil {
			return Outcome{}, err
		}
		if branch == "" {
			return Outcome{}, errors.New("the main checkout is not on a branch (detached HEAD), so afk cannot tell which branch the pull request should go to. Check out a branch, or set [ship] base in afk.toml")
		}
		base = branch
	}

	// `git rev-parse --verify --quiet refs/heads/X^{commit}` exits 1 and
	// prints nothing when the branch does not exist; any other failure is a
	// real one.
	exists := true
	if _, err := r.d.Run(root, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+PRBranch+"^{commit}"); err != nil {
		if code, ok := exitCode(err); !ok || code != 1 {
			return Outcome{}, fmt.Errorf("git could not say whether the branch %s exists: %w", PRBranch, err)
		}
		exists = false
	}

	// addArgs is the `git worktree add` argv after "add": the flag that
	// says what to do with the branch, then the folder and the start point.
	var addArgs []string
	var pr string
	switch {
	case !exists:
		addArgs = []string{"-b", PRBranch}
	default:
		// `git merge-base --is-ancestor A B` exits 0 for yes and 1 for no;
		// 128 (a missing ref, a broken repository) is an error, not a no.
		_, err := r.d.Run(root, "git", "merge-base", "--is-ancestor", "refs/heads/"+PRBranch, "refs/heads/"+base)
		ancestor := err == nil
		if err != nil {
			if code, ok := exitCode(err); !ok || code != 1 {
				return Outcome{}, fmt.Errorf("git could not compare the branch %s with %s: %w", PRBranch, base, err)
			}
		}
		if ancestor {
			// -B resets an existing branch to the start point. That is the
			// one forcing flag this package passes, and it is safe here
			// because merge-base just said every commit of the branch is
			// already in the base: resetting it onto the base loses nothing.
			addArgs = []string{"-B", PRBranch}
			break
		}
		found, err := r.findPR()
		if err != nil {
			return Outcome{}, err
		}
		if found == "" {
			return Outcome{}, fmt.Errorf("the branch %s has commits of its own and no open pull request, so the patch was not made. Check that its pull request was merged (and pull %s if it was), then delete the branch %s (git branch -D %s) and run the command again", PRBranch, base, PRBranch, PRBranch)
		}
		pr = found
	}

	wt, err := r.d.TempDir()
	if err != nil {
		return Outcome{}, fmt.Errorf("could not make a folder for the worktree: %w", err)
	}
	// An existing branch is named bare, not as refs/heads/…: `git worktree
	// add <path> refs/heads/X` checks out a detached HEAD at X's commit
	// (checked), and a commit there would belong to no branch. The start
	// point of a new or reset branch is a commit-ish, so the full ref is
	// right there and cannot be misread as a tag.
	args := []string{"worktree", "add"}
	args = append(args, addArgs...)
	args = append(args, wt)
	if addArgs == nil {
		args = append(args, PRBranch)
	} else {
		args = append(args, "refs/heads/"+base)
	}
	if _, err := r.d.Run(root, "git", args...); err != nil {
		return Outcome{}, fmt.Errorf("git could not make a worktree for the branch %s: %w", PRBranch, err)
	}
	defer func() {
		// Without --force: a worktree that still holds a change the commit
		// did not take is kept, and the note says where it is. Removing it
		// would throw the patch away.
		if _, err := r.d.Run(root, "git", "worktree", "remove", wt); err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("the temporary worktree at %s could not be removed (%v). Remove it by hand with: git worktree remove %s", wt, err, wt))
		}
	}()

	touched, err := r.patch(filepath.Join(wt, r.rel))
	if err != nil {
		return out, err
	}
	out.Touched = touched
	if err := r.commit(wt); err != nil {
		return out, err
	}
	out.Committed = true
	// -u sets the upstream the first time; a plain push afterwards. No
	// forcing flag: a branch the remote has moved past is the owner's to
	// look at.
	if _, err := r.d.Run(wt, "git", "push", "-u", "origin", PRBranch); err != nil {
		return out, fmt.Errorf("the commit was made on the branch %s but could not be pushed: %w. Push it by hand with: git push -u origin %s, then open a pull request to %s", PRBranch, err, PRBranch, base)
	}
	out.Pushed = true

	if pr == "" {
		pr, err = r.findPR()
		if err != nil {
			return out, err
		}
	}
	if pr == "" {
		pr, err = r.openPR(base)
		if err != nil {
			return out, err
		}
	}
	out.PR = pr
	out.Notes = append(out.Notes, "your plan file changes when the pull request is merged: "+pr)
	return out, nil
}

// patch reads the plan at path, builds and applies the edit and writes the
// result atomically. It returns the touched ids. On an error nothing was
// written.
func (r runner) patch(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("could not read the plan: %w", err)
	}
	plan, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read the plan: %w", err)
	}
	e, err := r.o.Edit(plan)
	if err != nil {
		return nil, err
	}
	res, err := Apply(Request{Plan: plan, Old: e.Old, New: e.New, Held: r.held})
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(path, res.Plan, info.Mode().Perm()); err != nil {
		return nil, err
	}
	return res.Touched, nil
}

// commit stages and commits the plan and nothing else, in dir. Both commands
// take the plan's path after "--", so no other change in the checkout can be
// swept in (checked: with a second file also modified, the commit holds the
// plan alone and the other file stays modified). Hooks run; --no-verify is
// never passed.
func (r runner) commit(dir string) error {
	if out, err := r.d.Run(dir, "git", "add", "--", r.rel); err != nil {
		return commitError("git add -- "+r.rel, out, err)
	}
	if out, err := r.d.Run(dir, "git", "commit", "-m", r.o.Message, "--", r.rel); err != nil {
		return commitError(fmt.Sprintf("git commit -m %q -- %s", r.o.Message, r.rel), out, err)
	}
	return nil
}

// commitError is a failed add or commit: the plan is patched on disk, so
// the message says so, gives git's words and the command to run by hand.
func commitError(cmd, stdout string, err error) error {
	said := strings.TrimSpace(stdout)
	if said != "" {
		said += "\n"
	}
	said += err.Error()
	return fmt.Errorf("the plan was patched but not committed. git said:\n%s\nTo commit it by hand, run: %s", said, cmd)
}

// currentBranch runs `git branch --show-current`, which prints the branch
// name, or nothing at all on a detached HEAD (checked).
func (r runner) currentBranch() (string, error) {
	out, err := r.d.Run(r.o.Root, "git", "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("git could not say which branch the main checkout is on: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// prRow is one pull request as `gh pr list --json` prints the fields asked
// for here. number, url and isCrossRepository are all in the field list gh
// 2.102.0 prints for `gh pr list --json`.
type prRow struct {
	Number            int    `json:"number"`
	URL               string `json:"url"`
	IsCrossRepository bool   `json:"isCrossRepository"`
}

// findPR returns the address of the open pull request for PRBranch, or "".
//
// It is this package's own lookup rather than ship.GH's FindPR because the
// owner is told the pull request's address, and ship's PR carries only the
// number. A pull request from a fork with a branch of the same name is not
// this one and is passed over, as ship does.
func (r runner) findPR() (string, error) {
	out, err := r.d.Run(r.o.Root, "gh", "pr", "list", "--head", PRBranch, "--state", "open",
		"--json", "number,url,isCrossRepository", "--limit", "20")
	if err != nil {
		return "", fmt.Errorf("gh could not list the pull requests for the branch %s: %w", PRBranch, err)
	}
	var rows []prRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return "", fmt.Errorf("gh pr list printed something that is not a list of pull requests: %w", err)
	}
	for _, row := range rows {
		if row.IsCrossRepository || row.Number <= 0 || row.URL == "" {
			continue
		}
		return row.URL, nil
	}
	return "", nil
}

// openPR opens the pull request and returns its address, which `gh pr
// create` prints on stdout as its last line. The title is the commit
// subject; the body is two plain sentences. Both go to gh as arguments, not
// through a shell, so a subject holding quotes is only ever text.
func (r runner) openPR(base string) (string, error) {
	body := "This pull request changes the master plan, made by afk plan patch with [planning] via_pr set. Merge it to make the change part of the plan."
	out, err := r.d.Run(r.o.Root, "gh", "pr", "create", "--base", base, "--head", PRBranch, "--title", r.o.Message, "--body", body)
	if err != nil {
		return "", fmt.Errorf("the change is pushed on the branch %s but gh could not open a pull request: %w. Open one by hand with: gh pr create --base %s --head %s", PRBranch, err, base, PRBranch)
	}
	lines := strings.Fields(out)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "https://") {
			return lines[i], nil
		}
	}
	return "", fmt.Errorf("gh said it opened a pull request for %s but printed no address; it printed: %s", PRBranch, strings.TrimSpace(out))
}

// heldTasks reads the ledger and returns the ids that rule 8 holds: those
// running or shipping. A missing ledger means nothing is held; a damaged one
// is an error, because a patch made without knowing what is running could
// change the entry of a running task.
func heldTasks(root string) (map[string]string, error) {
	l, err := ledger.Load(root)
	if err != nil {
		return nil, fmt.Errorf("the patch was not made because the ledger could not be read: %w", err)
	}
	held := map[string]string{}
	for _, e := range l.Tasks {
		if e.Status == ledger.Running || e.Status == ledger.Shipping {
			held[e.ID] = string(e.Status)
		}
	}
	return held, nil
}

// planRel returns the plan's path relative to Root, which is what every git
// command gets: git resolves a path after "--" against the folder it runs
// in, and the same relative path names the plan in the main checkout and in
// the pull-request worktree.
func planRel(root, plan string) (string, error) {
	if root == "" || plan == "" {
		return "", errors.New("planpatch.Run needs the project folder and the plan's path")
	}
	rel, err := filepath.Rel(root, plan)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the plan %s is not inside the project folder %s", plan, root)
	}
	return rel, nil
}

// lock takes an exclusive flock on Root/.afk/plan-patch.lock and returns the
// function that releases it. Two patches at once would each read the plan,
// each apply its own edit and the second rename would replace the first
// writer's file, so one patch would be lost with no error anywhere. Holding
// the lock from before the read until after the commit makes read-change-
// write-commit one step per writer. The lock file is left in place
// afterwards, for the reason internal/ledger gives: removing it would let a
// writer already blocked on it go ahead on a deleted inode while the next
// one locks a fresh file.
func lock(root string) (unlock func(), err error) {
	dir := filepath.Join(root, ".afk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	lp := filepath.Join(dir, lockName)
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening the plan lock %s: %w", lp, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", lp, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// writeAtomic replaces the file at path with data through a temp file in the
// same folder, an fsync and a rename, keeping the file's mode. The temp
// file must be in the plan's folder: rename is only atomic within one
// filesystem. The pid in its name is enough to keep writers apart because
// the lock already allows one at a time; it is there so a leftover from a
// killed process is recognisable and gets overwritten.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d", filepath.Base(path), os.Getpid()))
	if err := writeFileSync(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing the patched plan: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("putting the patched plan in place: %w", err)
	}
	return nil
}

// writeFileSync is os.WriteFile plus an fsync before close. Without the sync
// a power loss shortly after the rename can leave the plan's name pointing
// at a file whose contents never reached the disk, which is an empty plan.
func writeFileSync(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// exitCode reports the exit code of a command that ran and exited non-zero.
// ok is false for an error of any other kind (git is missing, the command
// was killed or timed out), which is never an answer to a question.
func exitCode(err error) (code int, ok bool) {
	var e interface{ ExitCode() int }
	if !errors.As(err, &e) {
		return 0, false
	}
	code = e.ExitCode()
	return code, code > 0
}
