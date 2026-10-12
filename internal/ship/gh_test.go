package ship

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture reads a file from testdata. testdata/SOURCES.txt says which files
// are a real gh's output and which were written by hand in the same shape.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// h starts an expected gh call. gh runs in the directory NewGH was given.
func h(args ...string) call { return cmd(wtDir, append([]string{"gh"}, args...)...) }

func prList(branch string) call {
	return h("pr", "list", "--head", branch, "--state", "all",
		"--json", "number,state,mergeable,mergeCommit,isCrossRepository", "--limit", "20")
}

// TestGHFindPR pins the one command FindPR runs and how its answer is read.
//
// Load-bearing rows:
//   - "no pull request": an empty list with exit 0 is found=false and no
//     error. It is the ordinary answer before the pull request is opened, and
//     an error here would park every ship at its first step.
//   - "open wins over merged and closed": a branch name is reused when a task
//     is tried again; picking the old closed pull request would park the new
//     attempt as "closed".
//   - "merged wins over closed": MergeSHA must come from the merged one.
//   - "a fork's pull request is not ours": the filter is by branch name only.
//   - "mergeable unknown is not a conflict": GitHub says UNKNOWN while it is
//     still working it out; only CONFLICTING is a conflict.
func TestGHFindPR(t *testing.T) {
	tests := []struct {
		name      string
		fixture   string
		want      PR
		wantFound bool
		wantErr   string
	}{
		{name: "no pull request", fixture: "pr-list-none.json"},
		{
			name:      "merged, as a real gh prints it",
			fixture:   "pr-list-merged.json",
			want:      PR{Number: 11, State: PRStateMerged, MergeSHA: shaB},
			wantFound: true,
		},
		{
			name:      "open wins over merged and closed",
			fixture:   "pr-list-several.json",
			want:      PR{Number: 12, State: PRStateOpen},
			wantFound: true,
		},
		{
			name:      "merged wins over closed",
			fixture:   "pr-list-merged-and-closed.json",
			want:      PR{Number: 11, State: PRStateMerged, MergeSHA: shaB},
			wantFound: true,
		},
		{
			name:      "only closed ones: the newest",
			fixture:   "pr-list-closed-only.json",
			want:      PR{Number: 5, State: PRStateClosed},
			wantFound: true,
		},
		{
			name:      "conflicting",
			fixture:   "pr-list-conflicting.json",
			want:      PR{Number: 12, State: PRStateOpen, Conflicting: true},
			wantFound: true,
		},
		{name: "a fork's pull request is not ours", fixture: "pr-list-fork-only.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := NewGH(script(t, prList("t2-3").prints(fixture(t, tt.fixture))), wtDir)
			pr, found, err := gh.FindPR("t2-3")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found != tt.wantFound || pr != tt.want {
				t.Errorf("FindPR() = %+v, %v; want %+v, %v", pr, found, tt.want, tt.wantFound)
			}
		})
	}

	t.Run("mergeable unknown is not a conflict", func(t *testing.T) {
		out := `[{"isCrossRepository":false,"mergeCommit":null,"mergeable":"UNKNOWN","number":12,"state":"OPEN"}]`
		pr, found, err := NewGH(script(t, prList("t2-3").prints(out)), wtDir).FindPR("t2-3")
		if err != nil || !found || pr.Conflicting {
			t.Errorf("FindPR() = %+v, %v, %v; want found and not conflicting", pr, found, err)
		}
	})
	t.Run("gh fails", func(t *testing.T) {
		gh := NewGH(script(t, prList("t2-3").exits(1, "HTTP 502: Bad Gateway")), wtDir)
		if _, found, err := gh.FindPR("t2-3"); err == nil || found || !strings.Contains(err.Error(), "502") {
			t.Errorf("found = %v, err = %v; want the gh error", found, err)
		}
	})
	t.Run("not json", func(t *testing.T) {
		gh := NewGH(script(t, prList("t2-3").prints("<html>")), wtDir)
		if _, found, err := gh.FindPR("t2-3"); err == nil || found {
			t.Errorf("found = %v, err = %v; want an error", found, err)
		}
	})
	t.Run("flag-shaped branch runs nothing", func(t *testing.T) {
		if _, _, err := NewGH(script(t), wtDir).FindPR("--web"); err == nil {
			t.Error("no error")
		}
	})
}

// TestGHChecks pins `gh pr checks {n} --json name,bucket` and the reading of
// its buckets and exit codes.
//
// Load-bearing rows:
//   - "pending, exit 8" and "failed, exit 1": gh exits non-zero after printing
//     the list. The list is the answer; treating the exit code as a failed
//     read would mean a red CI is never seen, only retried.
//   - "no checks reported": zero checks and no error. The ship waits five
//     minutes on that and then tells the owner; an error would instead park
//     it as "GitHub cannot be read".
//   - "cancel counts as failed": a cancelled CI run must never merge (A30).
//   - "unknown bucket counts as failed": every check must land in a count, or
//     the totals never add up and nothing ever turns green or red.
//   - "gh fails with nothing printed": a real failure stays an error.
func TestGHChecks(t *testing.T) {
	checks := func() call { return h("pr", "checks", "12", "--json", "name,bucket") }
	tests := []struct {
		name    string
		call    call
		want    Checks
		wantErr string
	}{
		{
			name: "all passed, as a real gh prints it",
			call: checks().prints(fixture(t, "checks-pass.json")),
			want: Checks{Total: 1, Passed: 1},
		},
		{
			name: "pending, exit 8",
			call: checks().prints(fixture(t, "checks-pending.json")).exits(8, ""),
			want: Checks{Total: 2, Passed: 1, Pending: 1},
		},
		{
			name: "pending, exit 0",
			call: checks().prints(fixture(t, "checks-pending.json")),
			want: Checks{Total: 2, Passed: 1, Pending: 1},
		},
		{
			name: "failed, exit 1",
			call: checks().prints(fixture(t, "checks-failed.json")).exits(1, ""),
			want: Checks{Total: 3, Passed: 1, Failed: 1, Skipped: 1, FailedNames: []string{"check"}},
		},
		{
			name: "cancel counts as failed",
			call: checks().prints(fixture(t, "checks-cancel.json")).exits(1, ""),
			want: Checks{Total: 2, Passed: 1, Failed: 1, FailedNames: []string{"check"}},
		},
		{
			name: "unknown bucket counts as failed",
			call: checks().prints(fixture(t, "checks-unknown-bucket.json")),
			want: Checks{Total: 2, Passed: 1, Failed: 1, FailedNames: []string{"new-kind"}},
		},
		{
			name: "no checks reported",
			call: checks().exits(1, "no checks reported on the 't2-3' branch"),
			want: Checks{},
		},
		{
			name: "no checks, as an empty list",
			call: checks().prints("[]\n"),
			want: Checks{},
		},
		{
			name:    "gh fails with nothing printed",
			call:    checks().exits(1, "HTTP 502: Bad Gateway (https://api.github.com/graphql)"),
			wantErr: "502",
		},
		{
			name:    "gh cannot be run",
			call:    checks().fails(exec.ErrNotFound),
			wantErr: "executable file not found",
		},
		{
			name:    "a message that only mentions no checks, from a command that never ran",
			call:    checks().fails(errors.New("no checks reported")),
			wantErr: "no checks reported",
		},
		{
			name:    "not json, exit 0",
			call:    checks().prints("check\tpass\t2m29s\thttps://example.invalid\n"),
			wantErr: "not a list of checks",
		},
		{
			name:    "nothing printed, exit 0",
			call:    checks(),
			wantErr: "printed nothing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewGH(script(t, tt.call), wtDir).Checks(12)
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
				t.Errorf("Checks() = %+v, want %+v", got, tt.want)
			}
			if sum := got.Passed + got.Pending + got.Failed + got.Skipped; sum != got.Total {
				t.Errorf("the four counts add up to %d, not Total %d", sum, got.Total)
			}
		})
	}
	t.Run("no number runs nothing", func(t *testing.T) {
		if _, err := NewGH(script(t), wtDir).Checks(0); err == nil {
			t.Error("no error")
		}
	})
}

// TestGHOpenPR checks `gh pr create`. The body's temporary file has a name
// the test cannot know in advance, so this test has its own RunFunc.
//
// Load-bearing rows:
//   - "awkward title": quotes, `$(…)`, a backtick and a leading dash reach gh
//     as the one argument after --title, byte for byte.
//   - "number not in the output": gh exited 0, so the pull request exists.
//     The number is then asked for, not reported as a failure, or the owner
//     would be told a pull request could not be opened when it was.
//   - every row: no --draft, the body in a file that is gone afterwards.
func TestGHOpenPR(t *testing.T) {
	const awkward = `Fix "quotes", $(rm -rf ~) and ` + "`id`" + ` -- 'done'`
	tests := []struct {
		name     string
		title    string
		body     string
		out      string
		err      error
		findOut  string // when set, a `gh pr list` call is expected next
		want     PR
		wantErr  string
		wantNone bool // gh must not be run at all
	}{
		{
			name:  "plain",
			title: "feat(ship): Add the adapters",
			body:  "Why.\n\nTask: T2.2\nSession: t2-2\n",
			out:   "https://github.com/dphaener/afk/pull/12\n",
			want:  PR{Number: 12, State: PRStateOpen},
		},
		{
			name:  "awkward title",
			title: awkward,
			body:  "body with \"quotes\" and $(echo no)\n",
			out:   "https://github.com/dphaener/afk/pull/7\n",
			want:  PR{Number: 7, State: PRStateOpen},
		},
		{
			name:  "notices before the address",
			title: "t",
			body:  "b",
			out:   "Warning: 1 uncommitted change\nhttps://github.com/dphaener/afk/pull/31\n",
			want:  PR{Number: 31, State: PRStateOpen},
		},
		{
			name:    "number not in the output",
			title:   "t",
			body:    "b",
			out:     "\n",
			findOut: `[{"isCrossRepository":false,"mergeCommit":null,"mergeable":"UNKNOWN","number":44,"state":"OPEN"}]`,
			want:    PR{Number: 44, State: PRStateOpen},
		},
		{
			name:    "number not in the output and none found",
			title:   "t",
			body:    "b",
			out:     "done\n",
			findOut: "[]",
			wantErr: "none can be found",
		},
		{
			name:    "gh refuses",
			title:   "t",
			body:    "b",
			err:     &CmdError{Cmd: "gh pr create", Code: 1, Stderr: "pull request create failed: GraphQL: No commits between main and t2-3 (createPullRequest)"},
			wantErr: "No commits between",
		},
		{name: "no title", title: "  ", body: "b", wantErr: "no subject line", wantNone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyFile string
			calls := 0
			run := func(dir, name string, args ...string) (string, error) {
				calls++
				argv := append([]string{name}, args...)
				if calls == 2 && tt.findOut != "" {
					if want := prList("t2-3").argv; !reflect.DeepEqual(argv, want) {
						t.Errorf("second command = %q, want %q", argv, want)
					}
					return tt.findOut, nil
				}
				if calls != 1 {
					t.Errorf("unexpected command: %q", argv)
					return "", errors.New("unexpected command")
				}
				if dir != wtDir {
					t.Errorf("ran in %s, want %s", dir, wtDir)
				}
				if len(argv) != 11 {
					t.Fatalf("argv = %q, want 11 elements", argv)
				}
				bodyFile = argv[10]
				want := []string{"gh", "pr", "create", "--base", "main", "--head", "t2-3", "--title", tt.title, "--body-file", bodyFile}
				if !reflect.DeepEqual(argv, want) {
					t.Errorf("argv = %q\nwant   %q", argv, want)
				}
				data, err := os.ReadFile(bodyFile)
				if err != nil {
					t.Errorf("the body file cannot be read while gh runs: %v", err)
				}
				if string(data) != tt.body {
					t.Errorf("body file holds %q, want %q", data, tt.body)
				}
				return tt.out, tt.err
			}

			pr, err := NewGH(run, wtDir).OpenPR("t2-3", "main", tt.title, tt.body)
			if tt.wantNone && calls != 0 {
				t.Errorf("gh was run %d time(s), want none", calls)
			}
			if !tt.wantNone && calls == 0 {
				t.Error("gh was never run")
			}
			if bodyFile != "" {
				if _, statErr := os.Stat(bodyFile); !errors.Is(statErr, os.ErrNotExist) {
					t.Errorf("the body file %s was left behind", bodyFile)
				}
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pr != tt.want {
				t.Errorf("OpenPR() = %+v, want %+v", pr, tt.want)
			}
		})
	}
}

// TestGHMerge pins `gh pr merge`.
//
// Load-bearing rows:
//   - the three methods: each passes exactly one strategy flag and
//     --match-head-commit with the sha whose checks were read, and nothing
//     else. The scripted RunFunc fails on any extra argument, which is what
//     keeps a branch delete or an override of failing requirements out.
//   - "no head sha": refused without running gh. A merge with no sha to match
//     is a merge of whatever is there now, checked or not.
//   - "unknown method": refused without running gh; with no strategy flag gh
//     asks which, and there is nobody to answer.
func TestGHMerge(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		sha     string
		calls   []call
		wantErr string
	}{
		{name: "merge", method: "merge", sha: shaA, calls: []call{h("pr", "merge", "12", "--merge", "--match-head-commit", shaA)}},
		{name: "squash", method: "squash", sha: shaA, calls: []call{h("pr", "merge", "12", "--squash", "--match-head-commit", shaA)}},
		{name: "rebase", method: "rebase", sha: shaA, calls: []call{h("pr", "merge", "12", "--rebase", "--match-head-commit", shaA)}},
		{
			name: "head moved since the checks were read", method: "merge", sha: shaA,
			calls: []call{h("pr", "merge", "12", "--merge", "--match-head-commit", shaA).
				exits(1, "GraphQL: Head branch was modified. Review and try the merge again. (mergePullRequest)")},
			wantErr: "Head branch was modified",
		},
		{name: "no head sha", method: "merge", sha: "", wantErr: "not known"},
		{name: "flag-shaped head sha", method: "merge", sha: "--admin", wantErr: "not known"},
		{name: "unknown method", method: "fast", sha: shaA, wantErr: "not a merge method"},
		{name: "empty method", method: "", sha: shaA, wantErr: "not a merge method"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewGH(script(t, tt.calls...), wtDir).Merge(12, tt.method, tt.sha)
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
	t.Run("no number runs nothing", func(t *testing.T) {
		if err := NewGH(script(t), wtDir).Merge(0, "merge", shaA); err == nil {
			t.Error("no error")
		}
	})
}

// TestGHReady pins `gh auth status` and the words the owner reads when it
// fails. Both failure rows are load-bearing: the text becomes the body of an
// inbox item, so it must say what is wrong and name the command that fixes it.
func TestGHReady(t *testing.T) {
	status := func() call { return h("auth", "status") }
	tests := []struct {
		name string
		call call
		want []string // substrings of the error; nil means ready
	}{
		{name: "logged in", call: status().prints("github.com\n  ✓ Logged in to github.com account someone\n")},
		{
			name: "not logged in",
			call: status().exits(1, "You are not logged into any GitHub hosts. To log in, run: gh auth login"),
			want: []string{"not logged in", "gh auth login", "You are not logged into any GitHub hosts"},
		},
		{
			name: "gh is not installed",
			call: status().fails(&CmdError{Cmd: "gh auth status", Code: -1, Err: &exec.Error{Name: "gh", Err: exec.ErrNotFound}}),
			want: []string{"not installed", "https://cli.github.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewGH(script(t, tt.call), wtDir).Ready()
			if tt.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("no error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %q", err, want)
				}
			}
		})
	}
}

func prHead() call {
	return h("pr", "view", "12", "--json", "headRefName,headRefOid")
}

func runList() call {
	return h("run", "list", "--branch", "t2-1", "--commit", shaA,
		"--json", "databaseId,conclusion,headSha", "--limit", "100")
}

// TestGHRerunFailed pins the three commands of a re-run.
//
// Load-bearing rows:
//   - "failed and cancelled runs of this head": the fixture also holds a run
//     that passed, one still going and a failed run of an older commit. None
//     of those may be re-run, and --failed is on every re-run so jobs that
//     passed are left alone.
//   - "nothing failed as a run": no re-run and no error (the failing check is
//     not an Actions run, or the re-run already started before a crash).
//   - "a re-run is refused": the error comes back and the next run is not
//     tried, so the owner sees the first refusal.
func TestGHRerunFailed(t *testing.T) {
	tests := []struct {
		name    string
		calls   []call
		wantErr string
	}{
		{
			name: "failed and cancelled runs of this head",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().prints(fixture(t, "run-list-mixed.json")),
				h("run", "rerun", "38181614285", "--failed"),
				h("run", "rerun", "38176094691", "--failed"),
			},
		},
		{
			name: "nothing failed as a run",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().prints(fixture(t, "run-list-success.json")),
			},
		},
		{
			name: "no runs at all",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().prints("[]"),
			},
		},
		{
			name: "a re-run is refused",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().prints(fixture(t, "run-list-mixed.json")),
				h("run", "rerun", "38181614285", "--failed").exits(1, "run 38181614285 cannot be rerun; its workflow file may be broken"),
			},
			wantErr: "cannot be rerun",
		},
		{
			name:    "the pull request cannot be read",
			calls:   []call{prHead().exits(1, "HTTP 502: Bad Gateway")},
			wantErr: "502",
		},
		{
			name:    "the pull request has no head commit",
			calls:   []call{prHead().prints(`{"headRefName":"t2-1","headRefOid":""}`)},
			wantErr: "head commit",
		},
		{
			name: "the runs cannot be listed",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().exits(1, "HTTP 502: Bad Gateway"),
			},
			wantErr: "502",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewGH(script(t, tt.calls...), wtDir).RerunFailed(12)
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

// TestGHFailedLog pins the log commands and the cut.
//
// Load-bearing rows:
//   - "long log": only the last 80 lines come back. The hand-back file is read
//     by an agent, and the failure is at the end of a log.
//   - "no failed run": an empty log, not an error, so the hand-back still
//     happens with the names of the failing checks.
//   - "the log cannot be read": an error, so the hand-back waits for the log
//     instead of going out without it.
func TestGHFailedLog(t *testing.T) {
	var long strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&long, "check\tRun tests\tline %d\n", i)
	}
	tests := []struct {
		name    string
		calls   []call
		check   func(t *testing.T, log string)
		wantErr string
	}{
		{
			name: "both failed runs, in order",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().prints(fixture(t, "run-list-mixed.json")),
				h("run", "view", "38181614285", "--log-failed").prints("deploy\tcancelled\n"),
				h("run", "view", "38176094691", "--log-failed").prints("check\tFAIL: TestX\n\n"),
			},
			check: func(t *testing.T, log string) {
				if log != "deploy\tcancelled\ncheck\tFAIL: TestX" {
					t.Errorf("log = %q", log)
				}
			},
		},
		{
			name: "long log",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().prints(fixture(t, "run-list-mixed.json")),
				h("run", "view", "38181614285", "--log-failed").prints(""),
				h("run", "view", "38176094691", "--log-failed").prints(long.String()),
			},
			check: func(t *testing.T, log string) {
				lines := strings.Split(log, "\n")
				if len(lines) != 80 || !strings.HasSuffix(lines[0], "line 121") || !strings.HasSuffix(lines[79], "line 200") {
					t.Errorf("kept %d lines, from %q to %q; want the last 80", len(lines), lines[0], lines[len(lines)-1])
				}
			},
		},
		{
			name: "no failed run",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().prints(fixture(t, "run-list-success.json")),
			},
			check: func(t *testing.T, log string) {
				if log != "" {
					t.Errorf("log = %q, want none", log)
				}
			},
		},
		{
			name: "the log cannot be read",
			calls: []call{
				prHead().prints(fixture(t, "pr-view-head.json")),
				runList().prints(fixture(t, "run-list-mixed.json")),
				h("run", "view", "38181614285", "--log-failed").exits(1, "log not found"),
			},
			wantErr: "log not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, err := NewGH(script(t, tt.calls...), wtDir).FailedLog(12)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, log)
		})
	}
}

// TestGHLogTail is the cut on its own: at most 80 lines, then at most 6000
// bytes, from the end.
//
// Load-bearing rows:
//   - "byte cut drops the half line": a cut in the middle of a line would
//     start the file with a fragment that reads as a different message.
//   - "one line longer than the limit": there is no whole line to keep, so
//     the end of it is kept, and never half a character.
func TestGHLogTail(t *testing.T) {
	wide := strings.Repeat("x", 199) // 200 bytes with its newline
	var fifty strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&fifty, "%02d%s\n", i, wide[2:])
	}
	tests := []struct {
		name  string
		text  string
		check func(t *testing.T, got string)
	}{
		{name: "empty", text: "", check: func(t *testing.T, got string) {
			if got != "" {
				t.Errorf("got %q", got)
			}
		}},
		{name: "short text is kept whole", text: "a\nb\n\n", check: func(t *testing.T, got string) {
			if got != "a\nb" {
				t.Errorf("got %q", got)
			}
		}},
		{name: "byte cut drops the half line", text: fifty.String(), check: func(t *testing.T, got string) {
			// 50 lines of 200 bytes is 9999 bytes without the last newline.
			// The last 6000 bytes start on the newline that ends line 19, so
			// what is left after it is lines 20 to 49, whole.
			lines := strings.Split(got, "\n")
			if len(got) > 6000 || len(lines) != 30 || !strings.HasPrefix(lines[0], "20") || !strings.HasPrefix(lines[29], "49") {
				t.Errorf("kept %d bytes in %d lines, first %.4q", len(got), len(lines), lines[0])
			}
			for _, line := range lines {
				if len(line) != 199 {
					t.Fatalf("a kept line is %d bytes, want whole lines of 199", len(line))
				}
			}
		}},
		{name: "one line longer than the limit", text: strings.Repeat("é", 4000) + "a", check: func(t *testing.T, got string) {
			// 8001 bytes; the last 6000 start in the middle of an "é".
			if len(got) != 5999 || !strings.HasSuffix(got, "a") || strings.Trim(strings.TrimSuffix(got, "a"), "é") != "" {
				t.Errorf("kept %d bytes starting %.6q; want 5999 bytes of whole characters", len(got), got)
			}
		}},
		{name: "invalid bytes do not survive", text: "ok \xff\xfe end", check: func(t *testing.T, got string) {
			if strings.ContainsAny(got, "\xff\xfe") || !strings.HasSuffix(got, " end") {
				t.Errorf("got %q", got)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tail(tt.text, failedLogLines, failedLogBytes))
		})
	}
}
