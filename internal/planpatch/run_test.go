package planpatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ship"
)

// call is one command a scripted RunFunc expects, and what it answers. It is
// the shape of internal/ship's test fake, kept here because that one is not
// exported and this package must never run real git or gh.
type call struct {
	dir  string
	argv []string
	out  string
	err  error
	// do runs when the call is made, so a fake `git worktree add` can put the
	// plan in the worktree as the real one would, and a fake `git worktree
	// remove` can take it away.
	do func()
}

// cmd starts an expected call: the directory, then the whole argv.
func cmd(dir string, argv ...string) call { return call{dir: dir, argv: argv} }

// prints sets the stdout of the call.
func (c call) prints(out string) call { c.out = out; return c }

// exits makes the call exit non-zero with the given stderr, as a real command
// that ran and failed does.
func (c call) exits(code int, stderr string) call {
	c.err = &ship.CmdError{Cmd: strings.Join(c.argv, " "), Code: code, Stderr: stderr, Err: fmt.Errorf("exit status %d", code)}
	return c
}

// fails makes the call fail without having run to an exit code.
func (c call) fails(err error) call { c.err = err; return c }

// does adds a side effect to the call.
func (c call) does(fn func()) call { c.do = fn; return c }

// neverPassed are the flags no command this package runs may carry, except
// `-B` on `git worktree add` (see viaPR). The scripted RunFunc checks every
// argv against them, so a row cannot "expect" one by mistake either.
var neverPassed = []string{"--force", "-f", "--force-with-lease", "--no-verify"}

// script returns a RunFunc that accepts exactly the given calls, in order,
// with exactly those directories and arguments, and fails the test on
// anything else, including a call that was expected and never made.
func script(t *testing.T, calls ...call) ship.RunFunc {
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
		if want.do != nil {
			want.do()
		}
		return want.out, want.err
	}
}

// place is where one row runs: a real folder with the plan in it, and the
// folder the fake TempDir hands out for the worktree.
type place struct {
	root, rel, plan, wt string
}

const (
	planFile  = "docs/plan.md"
	prURL     = "https://github.com/o/r/pull/7"
	originURL = "git@github.com:o/r.git\n"
	shaWT     = "98ab9620dbe90a348c80b99bc6040952847a8d60\n"
)

// Calls every row shares, by name.
func (p place) probe() call {
	return cmd(p.root, "git", "rev-parse", "--is-inside-work-tree").prints("true\n")
}
func (p place) status() call { return cmd(p.root, "git", "status", "--porcelain", "--", p.rel) }
func (p place) branch() call { return cmd(p.root, "git", "branch", "--show-current").prints("main\n") }
func (p place) getURL() call {
	return cmd(p.root, "git", "remote", "get-url", "origin").prints(originURL)
}
func (p place) push() call        { return cmd(p.root, "git", "push", "origin", "main") }
func (p place) add(d string) call { return cmd(d, "git", "add", "--", p.rel) }
func (p place) commit(d, msg string) call {
	return cmd(d, "git", "commit", "-m", msg, "--", p.rel).prints("[main 1234567] " + msg + "\n 1 file changed\n")
}
func (p place) verify() call {
	return cmd(p.root, "git", "rev-parse", "--verify", "--quiet", "refs/heads/afk-plan-changes^{commit}")
}
func (p place) isAncestor() call {
	return cmd(p.root, "git", "merge-base", "--is-ancestor", "refs/heads/afk-plan-changes", "refs/heads/main")
}
func (p place) prList() call {
	return cmd(p.root, "gh", "pr", "list", "--head", "afk-plan-changes", "--state", "open",
		"--json", "number,url,isCrossRepository", "--limit", "20")
}
func (p place) prCreate(title string) call {
	return cmd(p.root, "gh", "pr", "create", "--base", "main", "--head", "afk-plan-changes", "--title", title,
		"--body", "This pull request changes the master plan, made by afk plan patch with [planning] via_pr set. Merge it to make the change part of the plan.").
		prints("Creating pull request for afk-plan-changes into main in o/r\n\n" + prURL + "\n")
}
func (p place) pushUp() call   { return cmd(p.wt, "git", "push", "-u", "origin", "afk-plan-changes") }
func (p place) wtRemove() call { return cmd(p.root, "git", "worktree", "remove", p.wt) }

// wtAdd is the fake `git worktree add`: it checks out the plan as git would,
// by copying the main checkout's plan into the worktree.
func (p place) wtAdd(t *testing.T, args ...string) call {
	argv := append([]string{"git", "worktree", "add"}, args...)
	return cmd(p.root, argv...).does(func() {
		data, err := os.ReadFile(p.plan)
		if err != nil {
			t.Fatal(err)
		}
		wtPlan := filepath.Join(p.wt, p.rel)
		if err := os.MkdirAll(filepath.Dir(wtPlan), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wtPlan, data, 0o644); err != nil {
			t.Fatal(err)
		}
	})
}

const (
	oldScope = "- **Scope:** A search box on the home page that matches ingredient lines.\n"
	newScope = "- **Scope:** A search box on the home page that matches ingredient lines, in any case.\n"
)

// scopeEdit is the patch most rows make: one line of T1.3's entry.
func scopeEdit(plan []byte) (Edit, error) {
	return Edit{Old: []byte(oldScope), New: []byte(newScope)}, nil
}

// TestRun pins the executor: which commands run, in which folder and order,
// what is on disk afterwards and what the owner is told.
//
// Load-bearing rows:
//   - every "refused" row that asserts file "same": a refusal must leave the
//     plan byte for byte as it was, because the daemon reads it on disk and
//     a half-made change would be read as the plan.
//   - "apply refuses: two matches" and "a running task's entry": they also
//     assert that no git command after the branch check ran, so a refusal
//     never stages or commits.
//   - "damaged ledger": refused before any git command, because rule 8
//     cannot be checked without the ledger.
//   - the three via_pr rows that assert mainFile "same": the main checkout's
//     plan is never written in pull-request mode, and no command runs in the
//     main checkout that would move its branch (every write runs in wt).
//   - "direct: happy path": add, commit and push in that order, each with the
//     plan's path after "--", so nothing else in the checkout is swept in.
//   - "direct: commit fails": the error, and the file left patched, because
//     the patch was checked and right.
//   - "via_pr: new branch": `-b` and the full ref of the base; "via_pr: open
//     pull request": the existing branch is named bare (a full ref would
//     check out a detached HEAD); "via_pr: branch merged": `-B`, the one
//     forcing flag, only after merge-base said the branch is an ancestor.
func TestRun(t *testing.T) {
	type row struct {
		name   string
		ledger string
		edit   func([]byte) (Edit, error)
		msg    string
		viaPR  bool
		base   string
		calls  func(p place) []call
		// want is the Outcome's Touched, Committed, Pushed and PR; Notes are
		// checked by wantNotes, each a substring of the note at that index.
		want      Outcome
		wantNotes []string
		wantErr   string
		wantKind  string
		// file is "patched" or "same" for the main checkout's plan; wtFile
		// the same for the worktree's plan ("" when no worktree was made).
		file, wtFile string
	}
	rows := []row{
		{
			name: "not a repository: written in place, no git beyond the probe",
			calls: func(p place) []call {
				return []call{cmd(p.root, "git", "rev-parse", "--is-inside-work-tree").exits(128, "fatal: not a git repository")}
			},
			want:      Outcome{Touched: []string{"T1.3"}},
			wantNotes: []string{"is not a git repository, so the plan was changed in place and nothing was committed"},
			file:      "patched",
		},
		{
			name: "git cannot run: written in place, the note says so",
			calls: func(p place) []call {
				return []call{cmd(p.root, "git", "rev-parse", "--is-inside-work-tree").fails(errors.New("exec: \"git\": executable file not found"))}
			},
			want:      Outcome{Touched: []string{"T1.3"}},
			wantNotes: []string{"git could not be run (exec: \"git\": executable file not found), so the plan was changed in place"},
			file:      "patched",
		},
		{
			name:      "plan with uncommitted changes: written, not committed",
			calls:     func(p place) []call { return []call{p.probe(), p.status().prints(" M docs/plan.md\n")} },
			want:      Outcome{Touched: []string{"T1.3"}},
			wantNotes: []string{"the plan has changes that are not committed yet, so this one was not committed either"},
			file:      "patched",
		},
		{
			name:      "untracked plan: written, not committed",
			calls:     func(p place) []call { return []call{p.probe(), p.status().prints("?? docs/plan.md\n")} },
			want:      Outcome{Touched: []string{"T1.3"}},
			wantNotes: []string{"not committed yet"},
			file:      "patched",
		},
		{
			name: "direct: happy path runs add, commit, push in order",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.branch(), p.add(p.root), p.commit(p.root, DefaultMessage), p.getURL(), p.push()}
			},
			want: Outcome{Touched: []string{"T1.3"}, Committed: true, Pushed: true},
			file: "patched",
		},
		{
			name: "direct: the message is the commit subject",
			msg:  "docs(plan): Widen T1.3",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.branch(), p.add(p.root), p.commit(p.root, "docs(plan): Widen T1.3"), p.getURL(), p.push()}
			},
			want: Outcome{Touched: []string{"T1.3"}, Committed: true, Pushed: true},
			file: "patched",
		},
		{
			name: "direct: base matches the branch",
			base: "main",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.branch(), p.add(p.root), p.commit(p.root, DefaultMessage), p.getURL(), p.push()}
			},
			want: Outcome{Touched: []string{"T1.3"}, Committed: true, Pushed: true},
			file: "patched",
		},
		{
			name:    "direct: wrong branch is refused, file unchanged",
			base:    "main",
			calls:   func(p place) []call { return []call{p.probe(), p.status(), p.branch().prints("feature\n")} },
			wantErr: "the main checkout is on branch feature, not on main",
			file:    "same",
		},
		{
			name:    "direct: detached HEAD is refused",
			calls:   func(p place) []call { return []call{p.probe(), p.status(), p.branch().prints("")} },
			wantErr: "not on a branch (detached HEAD)",
			file:    "same",
		},
		{
			name: "direct: no origin: committed, not pushed, a note",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.branch(), p.add(p.root), p.commit(p.root, DefaultMessage),
					p.getURL().exits(2, "error: No such remote 'origin'")}
			},
			want:      Outcome{Touched: []string{"T1.3"}, Committed: true},
			wantNotes: []string{"the commit was not pushed: the repository has no origin remote"},
			file:      "patched",
		},
		{
			name: "direct: push fails: committed, not pushed, the note holds the command",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.branch(), p.add(p.root), p.commit(p.root, DefaultMessage), p.getURL(),
					p.push().exits(128, "fatal: Could not read from remote repository.")}
			},
			want:      Outcome{Touched: []string{"T1.3"}, Committed: true},
			wantNotes: []string{"To push it by hand, run: git push origin main"},
			file:      "patched",
		},
		{
			name: "direct: commit fails: error, file patched",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.branch(), p.add(p.root),
					cmd(p.root, "git", "commit", "-m", DefaultMessage, "--", p.rel).prints("pre-commit hook: lint failed\n").exits(1, "")}
			},
			want:    Outcome{Touched: []string{"T1.3"}},
			wantErr: "the plan was patched but not committed. git said:\npre-commit hook: lint failed\ngit commit -m docs(plan): Patch the master plan -- docs/plan.md: exit status 1\nTo commit it by hand, run: git commit -m \"docs(plan): Patch the master plan\" -- docs/plan.md",
			file:    "patched",
		},
		{
			name: "direct: add fails: error, file patched",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.branch(), p.add(p.root).exits(128, "fatal: Unable to create index.lock")}
			},
			want:    Outcome{Touched: []string{"T1.3"}},
			wantErr: "To commit it by hand, run: git add -- docs/plan.md",
			file:    "patched",
		},
		{
			name: "apply refuses: two matches, file byte-identical, no git write",
			edit: func([]byte) (Edit, error) {
				return Edit{Old: []byte("- **Depends on:** T1.2\n"), New: []byte("- **Depends on:** T1.1\n")}, nil
			},
			calls:    func(p place) []call { return []call{p.probe(), p.status(), p.branch()} },
			wantKind: "many",
			file:     "same",
		},
		{
			name:     "a running task's entry is refused, file unchanged",
			ledger:   `{"tasks":[{"id":"T1.3","status":"running"}]}`,
			calls:    func(p place) []call { return []call{p.probe(), p.status(), p.branch()} },
			wantKind: "held",
			wantErr:  "task T1.3 is running",
			file:     "same",
		},
		{
			name:     "a shipping task's entry is refused too",
			ledger:   `{"tasks":[{"id":"T1.3","status":"shipping"}]}`,
			calls:    func(p place) []call { return []call{p.probe(), p.status(), p.branch()} },
			wantKind: "held",
			wantErr:  "task T1.3 is shipping",
			file:     "same",
		},
		{
			name:   "a done task's entry is not held",
			ledger: `{"tasks":[{"id":"T1.3","status":"done"},{"id":"T2.1","status":"shipping"}]}`,
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.branch(), p.add(p.root), p.commit(p.root, DefaultMessage), p.getURL(), p.push()}
			},
			want: Outcome{Touched: []string{"T1.3"}, Committed: true, Pushed: true},
			file: "patched",
		},
		{
			name:    "damaged ledger: error before any git, file unchanged",
			ledger:  `{"tasks":[{"id":"T1.3","status":"flying"}]}`,
			calls:   func(p place) []call { return nil },
			wantErr: "the patch was not made because the ledger could not be read",
			file:    "same",
		},
		{
			name:    "edit fails: nothing written, no git write",
			edit:    func([]byte) (Edit, error) { return Edit{}, errors.New("no place for the task") },
			calls:   func(p place) []call { return []call{p.probe(), p.status(), p.branch()} },
			wantErr: "no place for the task",
			file:    "same",
		},
		{
			name:    "a message with a line break is refused before anything runs",
			msg:     "subject\n\nbody",
			calls:   func(p place) []call { return nil },
			wantErr: "the commit message must be one line",
			file:    "same",
		},
		{
			name:  "via_pr: new branch: worktree, commit, push, pull request, worktree removed",
			viaPR: true,
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.branch(), p.verify().exits(1, ""),
					p.wtAdd(t, "-b", "afk-plan-changes", p.wt, "refs/heads/main"),
					p.add(p.wt), p.commit(p.wt, DefaultMessage), p.pushUp(),
					p.prList().prints("[]\n"), p.prCreate(DefaultMessage), p.wtRemove()}
			},
			want:      Outcome{Touched: []string{"T1.3"}, Committed: true, Pushed: true, PR: prURL},
			wantNotes: []string{"your plan file changes when the pull request is merged: " + prURL},
			file:      "same",
			wtFile:    "patched",
		},
		{
			name:  "via_pr: open pull request: branch reused, no OpenPR",
			viaPR: true,
			msg:   "docs(plan): Second patch",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.branch(), p.verify().prints(shaWT),
					p.isAncestor().exits(1, ""),
					p.prList().prints(`[{"number":7,"url":"` + prURL + `","isCrossRepository":false}]` + "\n"),
					p.wtAdd(t, p.wt, "afk-plan-changes"),
					p.add(p.wt), p.commit(p.wt, "docs(plan): Second patch"), p.pushUp(), p.wtRemove()}
			},
			want:      Outcome{Touched: []string{"T1.3"}, Committed: true, Pushed: true, PR: prURL},
			wantNotes: []string{"your plan file changes when the pull request is merged: " + prURL},
			file:      "same",
			wtFile:    "patched",
		},
		{
			name:  "via_pr: a fork's pull request with the same branch name is not this one",
			viaPR: true,
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.branch(), p.verify().prints(shaWT),
					p.isAncestor().exits(1, ""),
					p.prList().prints(`[{"number":9,"url":"https://github.com/o/r/pull/9","isCrossRepository":true}]` + "\n")}
			},
			wantErr: "has commits of its own and no open pull request",
			file:    "same",
		},
		{
			name:  "via_pr: branch merged (an ancestor of the base): moved onto the base with -B",
			viaPR: true,
			base:  "main",
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.verify().prints(shaWT),
					p.isAncestor(),
					p.wtAdd(t, "-B", "afk-plan-changes", p.wt, "refs/heads/main"),
					p.add(p.wt), p.commit(p.wt, DefaultMessage), p.pushUp(),
					p.prList().prints("[]\n"), p.prCreate(DefaultMessage), p.wtRemove()}
			},
			want:      Outcome{Touched: []string{"T1.3"}, Committed: true, Pushed: true, PR: prURL},
			wantNotes: []string{"your plan file changes when the pull request is merged"},
			file:      "same",
			wtFile:    "patched",
		},
		{
			name:  "via_pr: own commits and no open pull request: refused, nothing created",
			viaPR: true,
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.branch(), p.verify().prints(shaWT),
					p.isAncestor().exits(1, ""), p.prList().prints("[]\n")}
			},
			wantErr: "the branch afk-plan-changes has commits of its own and no open pull request, so the patch was not made. Check that its pull request was merged (and pull main if it was), then delete the branch afk-plan-changes (git branch -D afk-plan-changes)",
			file:    "same",
		},
		{
			name:  "via_pr: no origin is refused before anything is created",
			viaPR: true,
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL().exits(2, "error: No such remote 'origin'")}
			},
			wantErr: "[planning] via_pr is set but the repository has no origin remote",
			file:    "same",
		},
		{
			name:    "via_pr: detached HEAD and no base is refused",
			viaPR:   true,
			calls:   func(p place) []call { return []call{p.probe(), p.status(), p.getURL(), p.branch().prints("")} },
			wantErr: "cannot tell which branch the pull request should go to",
			file:    "same",
		},
		{
			name:      "via_pr: an uncommitted plan is still patched in place",
			viaPR:     true,
			calls:     func(p place) []call { return []call{p.probe(), p.status().prints(" M docs/plan.md\n")} },
			want:      Outcome{Touched: []string{"T1.3"}},
			wantNotes: []string{"not committed yet"},
			file:      "patched",
		},
		{
			name:  "via_pr: apply refuses in the worktree: nothing committed, worktree removed",
			viaPR: true,
			edit: func([]byte) (Edit, error) {
				return Edit{Old: []byte("- **Depends on:** T1.2\n"), New: []byte("- **Depends on:** T1.1\n")}, nil
			},
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.branch(), p.verify().exits(1, ""),
					p.wtAdd(t, "-b", "afk-plan-changes", p.wt, "refs/heads/main"), p.wtRemove()}
			},
			wantKind: "many",
			file:     "same",
			wtFile:   "same",
		},
		{
			name:  "via_pr: commit fails: error, the worktree is kept and named",
			viaPR: true,
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.branch(), p.verify().exits(1, ""),
					p.wtAdd(t, "-b", "afk-plan-changes", p.wt, "refs/heads/main"),
					p.add(p.wt), cmd(p.wt, "git", "commit", "-m", DefaultMessage, "--", p.rel).exits(1, "hook failed"),
					p.wtRemove().exits(128, "fatal: contains modified or untracked files, use --force to delete it")}
			},
			want:      Outcome{Touched: []string{"T1.3"}},
			wantNotes: []string{"the temporary worktree at "},
			wantErr:   "the plan was patched but not committed",
			file:      "same",
			wtFile:    "patched",
		},
		{
			name:  "via_pr: push fails is an error that names the branch",
			viaPR: true,
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.branch(), p.verify().exits(1, ""),
					p.wtAdd(t, "-b", "afk-plan-changes", p.wt, "refs/heads/main"),
					p.add(p.wt), p.commit(p.wt, DefaultMessage), p.pushUp().exits(1, "rejected"), p.wtRemove()}
			},
			want:    Outcome{Touched: []string{"T1.3"}, Committed: true},
			wantErr: "Push it by hand with: git push -u origin afk-plan-changes, then open a pull request to main",
			file:    "same",
			wtFile:  "patched",
		},
		{
			name:  "via_pr: gh cannot open the pull request: error, the change is pushed",
			viaPR: true,
			calls: func(p place) []call {
				return []call{p.probe(), p.status(), p.getURL(), p.branch(), p.verify().exits(1, ""),
					p.wtAdd(t, "-b", "afk-plan-changes", p.wt, "refs/heads/main"),
					p.add(p.wt), p.commit(p.wt, DefaultMessage), p.pushUp(),
					p.prList().prints("[]\n"), p.prCreate(DefaultMessage).exits(1, "not logged in"), p.wtRemove()}
			},
			want:    Outcome{Touched: []string{"T1.3"}, Committed: true, Pushed: true},
			wantErr: "Open one by hand with: gh pr create --base main --head afk-plan-changes",
			file:    "same",
			wtFile:  "patched",
		},
	}

	original := readFixture(t, "base-plan.md")
	patched := []byte(swap(string(original), oldScope, newScope))

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p := place{root: root, rel: planFile, plan: filepath.Join(root, planFile), wt: filepath.Join(t.TempDir(), "wt")}
			if err := os.MkdirAll(filepath.Dir(p.plan), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p.plan, original, 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.ledger != "" {
				lp := filepath.Join(root, ".afk", "build", "ledger.json")
				if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(lp, []byte(tc.ledger), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			edit := tc.edit
			if edit == nil {
				edit = scopeEdit
			}
			d := Deps{
				Run: script(t, tc.calls(p)...),
				TempDir: func() (string, error) {
					if err := os.MkdirAll(p.wt, 0o755); err != nil {
						return "", err
					}
					return p.wt, nil
				},
			}
			got, err := Run(d, Options{Root: root, PlanPath: p.plan, Edit: edit, Message: tc.msg, ViaPR: tc.viaPR, Base: tc.base})

			if tc.wantKind != "" && kindOf(err) != tc.wantKind {
				t.Errorf("error kind: got %q, want %q", kindOf(err), tc.wantKind)
			}
			if tc.wantErr == "" && tc.wantKind == "" && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("error:\n got %v\nwant one containing %q", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got.Touched, tc.want.Touched) || got.Committed != tc.want.Committed || got.Pushed != tc.want.Pushed || got.PR != tc.want.PR {
				t.Errorf("outcome: got %+v, want Touched %v Committed %v Pushed %v PR %q", got, tc.want.Touched, tc.want.Committed, tc.want.Pushed, tc.want.PR)
			}
			if len(got.Notes) != len(tc.wantNotes) {
				t.Errorf("notes: got %q, want %d note(s) %q", got.Notes, len(tc.wantNotes), tc.wantNotes)
			}
			for i, want := range tc.wantNotes {
				if i < len(got.Notes) && !strings.Contains(got.Notes[i], want) {
					t.Errorf("note %d: got %q, want one containing %q", i, got.Notes[i], want)
				}
			}
			checkFile(t, "main checkout", p.plan, tc.file, original, patched)
			if tc.wtFile != "" {
				checkFile(t, "worktree", filepath.Join(p.wt, p.rel), tc.wtFile, original, patched)
			}
			if _, err := os.Stat(filepath.Join(root, ".afk", "plan-patch.lock")); tc.wantErr != "the commit message must be one line" && err != nil {
				t.Errorf("lock file: %v", err)
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(p.plan), ".*.tmp.*"))
			if len(leftovers) > 0 {
				t.Errorf("temp files left beside the plan: %v", leftovers)
			}
		})
	}
}

// checkFile asserts the plan at path is byte for byte the original ("same")
// or the patched text ("patched").
func checkFile(t *testing.T, where, path, want string, original, patched []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s plan: %v", where, err)
		return
	}
	var exp []byte
	switch want {
	case "same":
		exp = original
	case "patched":
		exp = patched
	default:
		t.Fatalf("row wants file %q, which is neither same nor patched", want)
	}
	if string(got) != string(exp) {
		t.Errorf("%s plan is not %s: %s", where, want, firstDifference(got, exp))
	}
}

// TestRunRefusesBadOptions pins the checks that run before the lock: a
// missing runner or Edit, and a plan outside the project folder, each with
// nothing on disk changed and nothing run. The "plan outside root" row is
// load-bearing: the relative path is what every git command gets, and a
// path that climbs out of the folder would name a file git does not have.
func TestRunRefusesBadOptions(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "plan.md")
	if err := os.WriteFile(plan, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	none := script(t)
	rows := []struct {
		name    string
		d       Deps
		o       Options
		wantErr string
	}{
		{"no runner", Deps{}, Options{Root: root, PlanPath: plan, Edit: scopeEdit}, "needs a command runner"},
		{"no edit", Deps{Run: none}, Options{Root: root, PlanPath: plan}, "needs an Edit function"},
		{"no root", Deps{Run: none}, Options{PlanPath: plan, Edit: scopeEdit}, "needs the project folder and the plan's path"},
		{"plan outside root", Deps{Run: none}, Options{Root: root, PlanPath: filepath.Join(filepath.Dir(root), "elsewhere.md"), Edit: scopeEdit}, "is not inside the project folder"},
		{"plan is root", Deps{Run: none}, Options{Root: root, PlanPath: root, Edit: scopeEdit}, "is not inside the project folder"},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Run(tc.d, tc.o)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error: got %v, want one containing %q", err, tc.wantErr)
			}
			got, _ := os.ReadFile(plan)
			if string(got) != base {
				t.Errorf("plan was changed")
			}
		})
	}
}

// TestRunKeepsMode pins that the atomic write keeps the plan's file mode: a
// plan made read-only for the group stays so after a patch, because the
// temp file is created with the old file's permission bits, not a default.
func TestRunKeepsMode(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, planFile)
	if err := os.MkdirAll(filepath.Dir(plan), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, readFixture(t, "base-plan.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := place{root: root, rel: planFile, plan: plan}
	d := Deps{Run: script(t, cmd(root, "git", "rev-parse", "--is-inside-work-tree").exits(128, ""))}
	if _, err := Run(d, Options{Root: root, PlanPath: plan, Edit: scopeEdit}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.plan)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode after the patch: %o, want 600", info.Mode().Perm())
	}
}
