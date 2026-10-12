package ship

// The GitHub adapter. Every argv in this file was written against the help
// text of gh version 2.102.0 (2026-09-30), read on the machine this was
// written on: `gh auth status`, `gh pr list`, `gh pr view`, `gh pr create`,
// `gh pr checks`, `gh pr merge`, `gh run list`, `gh run rerun` and
// `gh run view`. No flag or JSON field is used that those pages do not list.
//
// Three flags those pages do list must never appear here: `gh pr merge`'s
// --admin (merges past failing requirements), --auto (merges later, when
// nobody is looking at the checks) and --delete-branch (the branch delete is
// its own later step, after the base is up to date).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type ghCLI struct {
	run RunFunc
	dir string
}

// NewGH returns GH over the real gh command, run in dir. gh works out which
// GitHub repository it is talking to from the git remotes of that directory.
func NewGH(run RunFunc, dir string) GH {
	return &ghCLI{run: run, dir: dir}
}

func (g *ghCLI) gh(args ...string) (string, error) {
	return g.run(g.dir, "gh", args...)
}

// Ready runs `gh auth status`, which exits 1 when an account cannot log in.
// Its text goes into an inbox item, so both ways it can fail are put in plain
// words with the command that fixes them.
func (g *ghCLI) Ready() error {
	_, err := g.gh("auth", "status")
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("the GitHub command-line tool (gh) is not installed or not on the PATH. Install it from https://cli.github.com and run `gh auth login`")
	}
	return fmt.Errorf("the GitHub command-line tool (gh) is not logged in. Run `gh auth login`. gh said: %w", err)
}

// prRow is one pull request as `gh pr list --json` prints it. mergeCommit is
// null until the pull request is merged.
type prRow struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	Mergeable         string `json:"mergeable"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	MergeCommit       *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

// mergeableConflicting is the one value of "mergeable" that means a conflict.
// The others seen are MERGEABLE and UNKNOWN (GitHub has not worked it out
// yet, and it is also what a merged pull request reports); neither is a
// conflict.
const mergeableConflicting = "CONFLICTING"

// prListLimit is how many pull requests for one branch name are looked at. A
// branch name is reused when a task is attempted again, so there can be more
// than one; there are never many.
const prListLimit = "20"

// FindPR lists the pull requests whose head is this branch, in any state.
//
// `gh pr list` is used, not `gh pr view {branch}`: with no pull request the
// list prints an empty array and exits 0, where the view exits 1 with a
// message that would have to be told apart from a real failure by its text.
//
// More than one can come back. A branch name is reused when a task is tried
// again, and the earlier try's pull request, closed by hand, is still there.
// An open one is the one being shipped, so it wins; then a merged one (the
// ship that already happened); a closed one only when it is all there is.
// Within one state the newest (highest number) wins.
//
// A pull request from a fork is skipped. The filter is by branch name only,
// so on a public repository anybody's fork with a branch of the same name
// would otherwise be taken for the session's pull request.
func (g *ghCLI) FindPR(branch string) (PR, bool, error) {
	if err := refName("session", branch); err != nil {
		return PR{}, false, err
	}
	out, err := g.gh("pr", "list", "--head", branch, "--state", "all",
		"--json", "number,state,mergeable,mergeCommit,isCrossRepository", "--limit", prListLimit)
	if err != nil {
		return PR{}, false, err
	}
	var rows []prRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return PR{}, false, fmt.Errorf("gh pr list printed something that is not a list of pull requests: %w", err)
	}
	rank := map[string]int{PRStateOpen: 3, PRStateMerged: 2, PRStateClosed: 1}
	var best *prRow
	for i := range rows {
		row := &rows[i]
		if row.IsCrossRepository || row.Number <= 0 || rank[row.State] == 0 {
			continue
		}
		if best == nil || rank[row.State] > rank[best.State] ||
			(rank[row.State] == rank[best.State] && row.Number > best.Number) {
			best = row
		}
	}
	if best == nil {
		return PR{}, false, nil
	}
	pr := PR{Number: best.Number, State: best.State, Conflicting: best.Mergeable == mergeableConflicting}
	if best.MergeCommit != nil && best.State == PRStateMerged {
		pr.MergeSHA = best.MergeCommit.OID
	}
	return pr, true, nil
}

// pullURL finds the pull request number in the URL `gh pr create` prints.
var pullURL = regexp.MustCompile(`/pull/(\d+)`)

// OpenPR opens the pull request with `gh pr create`.
//
// --head is always given: the help says it is what makes gh skip its own
// "where should I push this" step, and the branch is already pushed. --draft
// is never given, so the pull request is ready for review. The title is one
// argument, passed as it is. The body goes through a file, not an argument,
// because it is a whole commit message and can be long.
func (g *ghCLI) OpenPR(branch, base, title, body string) (PR, error) {
	if err := refNames(base, branch); err != nil {
		return PR{}, err
	}
	if strings.TrimSpace(title) == "" {
		return PR{}, errors.New("the branch's last commit has no subject line to use as the pull request's title")
	}
	f, err := os.CreateTemp("", "afk-pr-body-*.md")
	if err != nil {
		return PR{}, fmt.Errorf("could not make a temporary file for the pull request's text: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	_, werr := f.WriteString(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return PR{}, fmt.Errorf("could not write the pull request's text to a temporary file: %w", werr)
	}

	out, err := g.gh("pr", "create", "--base", base, "--head", branch, "--title", title, "--body-file", path)
	if err != nil {
		return PR{}, err
	}
	if m := pullURL.FindAllStringSubmatch(out, -1); len(m) > 0 {
		if n, err := strconv.Atoi(m[len(m)-1][1]); err == nil && n > 0 {
			return PR{Number: n, State: PRStateOpen}, nil
		}
	}
	// gh exited 0, so the pull request exists; only its number could not be
	// read from what was printed. Asking GitHub is the truthful answer, and
	// reporting a failure here would tell the owner a pull request could not
	// be opened when it was.
	pr, found, err := g.FindPR(branch)
	if err != nil {
		return PR{}, err
	}
	if !found {
		return PR{}, fmt.Errorf("gh said it opened a pull request for %s but none can be found; it printed: %s", branch, strings.TrimSpace(out))
	}
	return pr, nil
}

// checkRow is one check as `gh pr checks --json name,bucket` prints it.
type checkRow struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
}

// noChecksText is in what gh says when a pull request has no checks at all.
const noChecksText = "no checks reported"

// Checks reads `gh pr checks {n} --json name,bucket`.
//
// The exit code is not the answer. The help gives exit 8 for "checks
// pending", and gh also exits non-zero when a check has failed, in both cases
// after printing the list. So what was printed is read first, and when it is
// a list it is the answer whatever the exit code. Only when nothing usable
// was printed does the error count, and then "no checks reported" is still an
// answer (zero checks), not a failure.
//
// The buckets are the five the help lists: pass, fail, pending, skipping and
// cancel. A cancelled check counts as failed, since a cancelled CI run proves
// nothing. A bucket this version does not know also counts as failed: every
// check has to land somewhere or the totals never add up and the ship waits
// for ever, and failed is the answer that can never lead to a merge and that
// puts the check's name in front of somebody.
func (g *ghCLI) Checks(pr int) (Checks, error) {
	if pr <= 0 {
		return Checks{}, errors.New("no pull request number was given to read checks for")
	}
	out, err := g.gh("pr", "checks", strconv.Itoa(pr), "--json", "name,bucket")
	var rows []checkRow
	if strings.TrimSpace(out) != "" {
		if perr := json.Unmarshal([]byte(out), &rows); perr == nil {
			return countChecks(rows), nil
		} else if err == nil {
			return Checks{}, fmt.Errorf("gh pr checks printed something that is not a list of checks: %w", perr)
		}
	}
	if err != nil {
		if _, ok := exited(err); ok && strings.Contains(err.Error(), noChecksText) {
			return Checks{}, nil
		}
		return Checks{}, err
	}
	return Checks{}, errors.New("gh pr checks printed nothing")
}

func countChecks(rows []checkRow) Checks {
	var c Checks
	for _, row := range rows {
		c.Total++
		switch row.Bucket {
		case "pass":
			c.Passed++
		case "pending":
			c.Pending++
		case "skipping":
			c.Skipped++
		default: // "fail", "cancel", and anything not known (see Checks)
			c.Failed++
			c.FailedNames = append(c.FailedNames, row.Name)
		}
	}
	return c
}

// runRow is one workflow run as `gh run list --json` prints it. conclusion is
// empty while the run is still going.
type runRow struct {
	ID         int64  `json:"databaseId"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"headSha"`
}

// runListLimit is how many runs for one commit are asked for. `gh run list`
// stops at 20 unless told otherwise, and a repository can have more workflows
// than that.
const runListLimit = "100"

// failedRuns returns the ids of the workflow runs that ended badly for the
// pull request's current head.
//
// `gh pr checks` does not give run ids, so this takes two reads: the pull
// request's branch and head commit, then the runs for that branch and commit.
// The commit is filtered again here so an older commit's failure is never
// re-run or quoted, whatever the listing returns.
//
// A check that is not a GitHub Actions run (a status posted by another
// service) has no run, so the result can be empty while a check is failing.
func (g *ghCLI) failedRuns(pr int) ([]int64, error) {
	if pr <= 0 {
		return nil, errors.New("no pull request number was given")
	}
	out, err := g.gh("pr", "view", strconv.Itoa(pr), "--json", "headRefName,headRefOid")
	if err != nil {
		return nil, err
	}
	var head struct {
		Branch string `json:"headRefName"`
		SHA    string `json:"headRefOid"`
	}
	if err := json.Unmarshal([]byte(out), &head); err != nil {
		return nil, fmt.Errorf("gh pr view printed something that is not a pull request: %w", err)
	}
	if head.SHA == "" || strings.HasPrefix(head.SHA, "-") {
		return nil, fmt.Errorf("gh did not give the head commit of pull request #%d", pr)
	}
	if err := refName("pull request", head.Branch); err != nil {
		return nil, err
	}
	out, err = g.gh("run", "list", "--branch", head.Branch, "--commit", head.SHA,
		"--json", "databaseId,conclusion,headSha", "--limit", runListLimit)
	if err != nil {
		return nil, err
	}
	var rows []runRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("gh run list printed something that is not a list of runs: %w", err)
	}
	var ids []int64
	for _, row := range rows {
		if row.HeadSHA != head.SHA || row.ID <= 0 {
			continue
		}
		switch row.Conclusion {
		case "failure", "cancelled", "timed_out", "startup_failure":
			ids = append(ids, row.ID)
		}
	}
	return ids, nil
}

// RerunFailed re-runs each failed run with --failed, which the help describes
// as "only failed jobs, including dependencies".
//
// No failed run to re-run is not an error. It happens when the failing check
// is not an Actions run, and after a crash that follows a re-run (the run is
// going again and has no conclusion). Reporting a failure would park the task
// on an item that answering can never clear; saying "done" lets the ship move
// on to handing the failure back, which is the next thing to try anyway.
func (g *ghCLI) RerunFailed(pr int) error {
	ids, err := g.failedRuns(pr)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := g.gh("run", "rerun", strconv.FormatInt(id, 10), "--failed"); err != nil {
			return err
		}
	}
	return nil
}

// The failed log is cut to what an agent can usefully read: the end of it,
// where the failure is.
const (
	failedLogLines = 80
	failedLogBytes = 6000
)

// FailedLog joins `gh run view {id} --log-failed` for each failed run and
// keeps the end. No failed run gives an empty log and no error, for the
// reason given on RerunFailed; the hand-back file still names the checks.
func (g *ghCLI) FailedLog(pr int) (string, error) {
	ids, err := g.failedRuns(pr)
	if err != nil {
		return "", err
	}
	var all strings.Builder
	for _, id := range ids {
		out, err := g.gh("run", "view", strconv.FormatInt(id, 10), "--log-failed")
		if err != nil {
			return "", err
		}
		all.WriteString(strings.TrimRight(out, "\n"))
		all.WriteString("\n")
	}
	return tail(all.String(), failedLogLines, failedLogBytes), nil
}

// tail keeps the last maxLines lines of text and then at most maxBytes bytes
// of those, cutting from the front. A cut never leaves half a line at the top
// when a whole line fits, and never half a character.
func tail(text string, maxLines, maxBytes int) string {
	text = strings.ToValidUTF8(strings.TrimRight(text, "\n"), "?")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	text = strings.Join(lines, "\n")
	if len(text) > maxBytes {
		text = text[len(text)-maxBytes:]
		if i := strings.IndexByte(text, '\n'); i >= 0 && i < len(text)-1 {
			text = text[i+1:]
		}
		for len(text) > 0 && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
	}
	return text
}

// mergeFlags maps the project's merge_method to gh's flag. One of the three
// is always passed: without one gh asks which, and there is nobody to answer.
var mergeFlags = map[string]string{
	"merge":  "--merge",
	"squash": "--squash",
	"rebase": "--rebase",
}

// Merge merges with --match-head-commit, which the help describes as the
// "commit SHA that the pull request head must match to allow merge". That is
// what stops a commit pushed after the checks were read from being merged
// unchecked, so a merge with no sha to match is refused here rather than sent
// without the flag.
func (g *ghCLI) Merge(pr int, method, headSHA string) error {
	if pr <= 0 {
		return errors.New("no pull request number was given to merge")
	}
	flag, ok := mergeFlags[method]
	if !ok {
		return fmt.Errorf("%q is not a merge method; use merge, squash or rebase", method)
	}
	if headSHA == "" || strings.HasPrefix(headSHA, "-") {
		return errors.New("the commit whose checks passed is not known, so the merge was not attempted")
	}
	_, err := g.gh("pr", "merge", strconv.Itoa(pr), flag, "--match-head-commit", headSHA)
	return err
}
