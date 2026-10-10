// Package plancheck is the one writer of a plan file's task checkboxes. It
// backs `afk plan check`, which task agents and the execute workflow call to
// record that a task finished or failed.
//
// It lives outside internal/state on purpose: that package is the read-only
// parser for the plan file and promises never to write. This package writes
// the lines state.ParsePlan reads back, so the two must agree on the line
// form, and the tests here round-trip every line they produce through that
// parser.
//
// Why a command and not an agent editing the file: several task agents finish
// at about the same time, and each used to rewrite the whole plan to tick its
// own box. A whole-file rewrite from a stale read silently undoes whichever
// tick landed in between. Here every writer takes one exclusive lock, re-reads
// the plan under it and changes exactly one line.
package plancheck

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Target is the state a task's checkbox is being set to.
type Target int

const (
	// Done writes `- [x] (ID) {title}`.
	Done Target = iota
	// Failed writes `- [!] (ID) {title} FAILED: {reason}`.
	Failed
)

// String is the word the command line uses for the target.
func (t Target) String() string {
	if t == Failed {
		return "failed"
	}
	return "done"
}

// ParseTarget reads the command line's `done` or `failed`.
func ParseTarget(s string) (Target, bool) {
	switch s {
	case "done":
		return Done, true
	case "failed":
		return Failed, true
	}
	return Done, false
}

var (
	// ErrUnknownTask means the plan has no task line with the given id.
	ErrUnknownTask = errors.New("no such task in the plan")
	// ErrEmptyReason means Failed was asked for without a reason. The plan
	// parser rejects a `[!]` line with no reason, so writing one would make
	// the whole plan unreadable to the dashboard.
	ErrEmptyReason = errors.New("a failed task needs a reason")
	// ErrBadTaskID means the id is not of the form W1.T2.
	ErrBadTaskID = errors.New("task id must look like W1.T2")
	// ErrBadSession means the session name cannot be a file name.
	ErrBadSession = errors.New("session name is not a plain name")
	// ErrNoPlan means the plan file does not exist.
	ErrNoPlan = errors.New("plan file not found")
)

var (
	taskIDRE = regexp.MustCompile(`^W\d+\.T\d+$`)
	// taskLineRE matches a task line at column 0, the only place both plan
	// parsers (internal/state/plan.go and plan_to_args.rb) look for one.
	// Group 1 is the checkbox character, 2 the id, 3 everything after the
	// closing parenthesis.
	taskLineRE = regexp.MustCompile(`^- \[([ x!])\] \((W\d+\.T\d+)\)(.*)$`)
	// failedSuffixRE splits a failed line's remainder into title and reason
	// at the FIRST "FAILED:", the same non-greedy split as failedReasonRE in
	// internal/state/plan.go. They must stay the same split, or `done` over a
	// failed line would restore a different title from the one the parser
	// showed while the task was failed.
	failedSuffixRE = regexp.MustCompile(`^(.*?)\s*FAILED:`)
	sectionRE      = regexp.MustCompile(`^## (.+)$`)
)

// NormalizeID accepts `W1.T2` or `(W1.T2)`, in either letter case, and returns
// `W1.T2`. Both spellings are accepted because the id appears in the plan in
// parentheses and callers copy it from there.
func NormalizeID(s string) (string, error) {
	id := strings.TrimSpace(s)
	if strings.HasPrefix(id, "(") && strings.HasSuffix(id, ")") {
		id = id[1 : len(id)-1]
	}
	id = strings.ToUpper(strings.TrimSpace(id))
	if !taskIDRE.MatchString(id) {
		return "", fmt.Errorf("%w (got %q)", ErrBadTaskID, s)
	}
	return id, nil
}

// CollapseReason puts a reason on one line. A reason is often a pasted error
// with newlines in it, and a newline written into the task line would add
// lines to the plan and push the second half of the reason into the task's
// body. Tabs and runs of spaces are collapsed along the way.
func CollapseReason(reason string) string {
	return strings.Join(strings.Fields(reason), " ")
}

// Mark returns plan with the checkbox of task id set to target. It is pure:
// bytes in, bytes out.
//
// Only the task's own line changes, and the number of lines never changes
// (the execute workflow addresses task blocks by line number). Line endings
// and trailing whitespace on the line are kept as they were.
//
// changed is false when the line already has the target state, and the bytes
// come back untouched. For Failed that means the first recorded reason wins:
// a second failure report for the same task (a retry, or the workflow marking
// what the agent already marked) must not rewrite the reason or log a second
// entry.
//
// A task line is looked for only where the plan parsers look: at column 0,
// inside the `## Waves` section, outside fenced code blocks. A plan that
// quotes a task line in an example must not have the example ticked instead
// of the task.
func Mark(plan []byte, id string, target Target, reason string) (out []byte, changed bool, err error) {
	id, err = NormalizeID(id)
	if err != nil {
		return nil, false, err
	}
	reason = CollapseReason(reason)
	if target == Failed && reason == "" {
		return nil, false, ErrEmptyReason
	}

	lines := bytes.Split(plan, []byte("\n"))
	inWaves := false
	inFence := false
	for i, raw := range lines {
		body := strings.TrimRight(string(raw), " \t\r")
		tail := string(raw[len(body):])

		if lead := strings.TrimLeft(body, " \t"); strings.HasPrefix(lead, "```") || strings.HasPrefix(lead, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := sectionRE.FindStringSubmatch(body); m != nil {
			inWaves = strings.TrimSpace(m[1]) == "Waves"
			continue
		}
		if !inWaves {
			continue
		}
		m := taskLineRE.FindStringSubmatch(body)
		if m == nil || m[2] != id {
			continue
		}

		box, rest := m[1], strings.TrimSpace(m[3])
		if (target == Done && box == "x") || (target == Failed && box == "!") {
			return plan, false, nil
		}
		title := rest
		if box == "!" {
			if fm := failedSuffixRE.FindStringSubmatch(rest); fm != nil {
				title = strings.TrimSpace(fm[1])
			}
		}

		var b strings.Builder
		if target == Done {
			b.WriteString("- [x] (" + id + ")")
		} else {
			b.WriteString("- [!] (" + id + ")")
		}
		if title != "" {
			b.WriteString(" " + title)
		}
		if target == Failed {
			b.WriteString(" FAILED: " + reason)
		}
		b.WriteString(tail)

		newLines := make([][]byte, len(lines))
		copy(newLines, lines)
		newLines[i] = []byte(b.String())
		return bytes.Join(newLines, []byte("\n")), true, nil
	}
	return nil, false, fmt.Errorf("%w: %s", ErrUnknownTask, id)
}

// Request is one `afk plan check` call.
type Request struct {
	// Project is the directory that holds `.afk/`.
	Project string
	// Session is the session name, which is also the plan's file name.
	Session string
	// TaskID is `W1.T2`, with or without parentheses.
	TaskID string
	Target Target
	// Reason is required for Failed and ignored for Done.
	Reason string
	// Now stamps the issues.md entry. Injected so tests do not depend on the
	// clock; nil means time.Now.
	Now func() time.Time
}

// Result says what Apply did, for the command's message.
type Result struct {
	// Changed is false when the task already had the target state.
	Changed bool
	// PlanPath and IssuesPath are the files the request addresses. IssuesPath
	// is written only for a Failed request that changed the plan.
	PlanPath   string
	IssuesPath string
}

// PlanPath is where a session's plan lives.
func PlanPath(project, session string) string {
	return filepath.Join(project, ".afk", "plans", session+".md")
}

// IssuesPath is the session's issues notepad.
func IssuesPath(project, session string) string {
	return filepath.Join(project, ".afk", session, "issues.md")
}

// lockPath is the lock every writer of one plan takes. It is a separate file
// and not the plan itself because the plan is replaced by rename: a lock on
// the plan's inode would be a lock on a file that is no longer the plan the
// moment the first writer finishes.
func lockPath(project, session string) string {
	return filepath.Join(project, ".afk", "plans", "."+session+".lock")
}

// Apply marks one task in a session's plan file and, for Failed, logs the
// reason in the session's issues.md.
//
// The lock is the point of this function. Writing a temp file and renaming it
// keeps a reader from ever seeing half a plan, but it does nothing for two
// writers: both read the plan, each changes its own line, and the second
// rename replaces the first writer's file, so one task's tick is lost with no
// error anywhere. Holding an exclusive flock from before the read until after
// the rename makes read-change-write one step per writer. The plan is re-read
// under the lock for the same reason; anything read before it may be stale.
//
// The lock file is left in place afterwards. Removing it would let a writer
// already blocked on it go ahead holding a lock on a deleted inode while the
// next writer creates and locks a fresh file, and the two would run at once.
// It is an empty dot-file beside the plan.
//
// For Failed, issues.md is appended to before the plan is renamed, still
// under the lock. If the process dies between the two, a retry appends a
// second entry and then marks the plan: a duplicate entry is harmless. The
// other order could mark the plan and lose the entry for good, because the
// retry would find the line already failed and change nothing. issues.md is
// appended to in place (O_APPEND) and not rewritten by rename, because task
// agents append their own notes to it without this lock and a rewrite from a
// stale read would drop theirs.
func Apply(req Request) (Result, error) {
	res := Result{}
	// The name is joined into paths, so anything that is not a single path
	// element could address a file outside .afk/.
	if req.Session == "" || req.Session != filepath.Base(req.Session) || strings.HasPrefix(req.Session, ".") {
		return res, fmt.Errorf("%w: %q", ErrBadSession, req.Session)
	}
	id, err := NormalizeID(req.TaskID)
	if err != nil {
		return res, err
	}
	reason := CollapseReason(req.Reason)
	if req.Target == Failed && reason == "" {
		return res, ErrEmptyReason
	}
	now := req.Now
	if now == nil {
		now = time.Now
	}

	res.PlanPath = PlanPath(req.Project, req.Session)
	res.IssuesPath = IssuesPath(req.Project, req.Session)

	// Checked before the lock is opened, so a mistyped session name or
	// project does not leave a lock file (or need a plans directory) behind.
	info, err := os.Stat(res.PlanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return res, fmt.Errorf("%w: %s", ErrNoPlan, res.PlanPath)
		}
		return res, fmt.Errorf("checking plan %s: %w", res.PlanPath, err)
	}

	lp := lockPath(req.Project, req.Session)
	lock, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return res, fmt.Errorf("opening plan lock %s: %w", lp, err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return res, fmt.Errorf("locking %s: %w", lp, err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	data, err := os.ReadFile(res.PlanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return res, fmt.Errorf("%w: %s", ErrNoPlan, res.PlanPath)
		}
		return res, fmt.Errorf("reading plan %s: %w", res.PlanPath, err)
	}

	out, changed, err := Mark(data, id, req.Target, reason)
	if err != nil {
		return res, err
	}
	if !changed {
		return res, nil
	}

	if req.Target == Failed {
		if err := appendIssue(res.IssuesPath, id, reason, now()); err != nil {
			return res, err
		}
	}

	// The temp file must be in the plan's directory: rename is only atomic
	// within one filesystem. The pid in the name is enough to keep writers
	// apart because the lock already allows one at a time; it is there so a
	// leftover from a killed process is recognisable and gets overwritten.
	dir := filepath.Dir(res.PlanPath)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.md.tmp.%d", req.Session, os.Getpid()))
	if err := writeFileSync(tmp, out, info.Mode().Perm()); err != nil {
		_ = os.Remove(tmp)
		return res, fmt.Errorf("writing temp plan: %w", err)
	}
	if err := os.Rename(tmp, res.PlanPath); err != nil {
		_ = os.Remove(tmp)
		return res, fmt.Errorf("renaming plan into place: %w", err)
	}
	res.Changed = true
	return res, nil
}

// writeFileSync is os.WriteFile plus an fsync before close. Without the sync
// a power loss shortly after the rename can leave the plan's name pointing at
// a file whose contents never reached the disk, which is an empty plan.
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

// appendIssue adds a `## (ID) FAILED` entry to issues.md, creating the file
// and its directory when they are missing (a session whose plan exists may
// not have written a notepad yet). The whole entry goes out in one Write on
// an O_APPEND descriptor so it cannot interleave with an agent appending its
// own note at the same moment.
func appendIssue(path, id, reason string, at time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var b strings.Builder
	if len(existing) > 0 {
		// Start on a fresh line with one blank line before the heading,
		// whatever the previous writer left at the end of the file.
		if !bytes.HasSuffix(existing, []byte("\n")) {
			b.WriteString("\n")
		}
		if !bytes.HasSuffix(existing, []byte("\n\n")) {
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "## (%s) FAILED\n\n%s: %s\n", id, at.UTC().Format(time.RFC3339), reason)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return fmt.Errorf("appending to %s: %w", path, err)
	}
	return f.Close()
}
