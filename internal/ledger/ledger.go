// Package ledger reads and writes a project's task ledger, the file that
// records what has happened to each task of the master plan
// (docs/PLAN.md §4.2). It lives at .afk/build/ledger.json in the project's
// main checkout and holds one entry per task id. A task with no entry is
// pending.
//
// A ledger that exists but cannot be understood is an error, never an empty
// ledger. An empty ledger means "nothing has been done yet", so reading a
// damaged file that way would have the runner start again on tasks that are
// already finished and merged. Only a file that is not there at all is empty.
// For the same reason the reader is strict: an unknown key or status is
// rejected, because quietly dropping a mistyped field from a hand edit loses
// what the owner meant to record.
//
// Update is the only writer. Several processes may write at once (the daemon
// and an owner running `afk ledger set`), so it holds a lock from before the
// read until after the rename; see its comment.
//
// The package knows nothing about the plan. Whether an id names a real task is
// the caller's question.
package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// Status is where a task stands. The values are the words stored in the file.
type Status string

const (
	Pending    Status = "pending"
	Running    Status = "running"
	Shipping   Status = "shipping"
	Parked     Status = "parked"
	Done       Status = "done"
	Failed     Status = "failed"
	Superseded Status = "superseded"
)

var (
	// ErrCorrupt means the ledger file was read but its content is not a
	// valid ledger. Callers tell the owner to fix or delete the file.
	ErrCorrupt = errors.New("ledger file is damaged")
	// ErrInvalid means the callback given to Update left the ledger in a
	// shape Load would reject. Nothing was written.
	ErrInvalid = errors.New("ledger change is not valid")
	// ErrUnknownStatus means a status is not one of Statuses.
	ErrUnknownStatus = errors.New("unknown status")
	// ErrEmptyID means Set was given no task id.
	ErrEmptyID = errors.New("task id is empty")
	// ErrNoReplacement means a task was marked superseded without naming
	// the tasks that replace it.
	ErrNoReplacement = errors.New("a superseded task needs at least one replacing task")
	// ErrSelfReplacement means a task was named as its own replacement.
	ErrSelfReplacement = errors.New("a task cannot be replaced by itself")
	// ErrExists means Init found a ledger already there and left it alone.
	ErrExists = errors.New("a ledger already exists")
)

// Statuses returns every status in lifecycle order. The slice is the
// caller's to keep.
func Statuses() []Status {
	return []Status{Pending, Running, Shipping, Parked, Done, Failed, Superseded}
}

// ParseStatus turns one of the seven words into a Status. The match is exact:
// accepting "Done" or " done" would put a second spelling of a status in the
// file, and a reader comparing against the constant would not see it as done.
func ParseStatus(s string) (Status, error) {
	if slices.Contains(Statuses(), Status(s)) {
		return Status(s), nil
	}
	words := make([]string, 0, 7)
	for _, st := range Statuses() {
		words = append(words, string(st))
	}
	return "", fmt.Errorf("%w %q (want one of %s)", ErrUnknownStatus, s, strings.Join(words, ", "))
}

// Entry is one task's record. Only id and status are always present; the rest
// are filled in as they come to exist and left out of the file while empty.
// StartedAt and FinishedAt are RFC 3339 UTC strings, kept as strings so that
// reading and writing a ledger never rewrites a time it did not set.
type Entry struct {
	ID         string   `json:"id"`
	Status     Status   `json:"status"`
	Session    string   `json:"session,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	PR         int      `json:"pr,omitempty"`
	Attempts   int      `json:"attempts,omitempty"`
	ParkedOn   []string `json:"parked_on,omitempty"`
	StartedAt  string   `json:"started_at,omitempty"`
	FinishedAt string   `json:"finished_at,omitempty"`
	MergedSHA  string   `json:"merged_sha,omitempty"`
	// Ship is how far the ship of a shipping task has got (see ShipStates),
	// ShipAt when it entered that state, and the two counters how often CI
	// was re-run and handed back to the session. They are the shipper's
	// record: Set leaves them alone.
	Ship        string   `json:"ship,omitempty"`
	ShipAt      string   `json:"ship_at,omitempty"`
	CIReruns    int      `json:"ci_reruns,omitempty"`
	CIFixCycles int      `json:"ci_fix_cycles,omitempty"`
	ReplacedBy  []string `json:"replaced_by,omitempty"`
	// Restarts counts the automatic recoveries of the current attempt: a
	// restart request to the host and a relaunch of a gone host each add one.
	// It is saved before the action, so a crash mid-recovery cannot forget a
	// restart that was already sent. It goes back to zero on a new attempt,
	// on an un-park and when the task leaves running (PLAN §5.6). Set leaves
	// it alone.
	Restarts int `json:"restarts,omitempty"`
	// RestartAt is when the last recovery was started, RFC 3339 UTC. The
	// backoff before the next recovery, the host grace after a relaunch and
	// the continue prompt are all timed from it. A value that does not parse
	// is read as the zero time, the same as the other times in an entry.
	RestartAt string `json:"restart_at,omitempty"`
	// Wakes counts the times the daemon typed the resume command to wake the
	// session in the current attempt. It is saved before each wake is typed,
	// so a daemon killed in between does not type a second one on restart. A
	// wake is not a restart: it never touches Restarts or RestartAt, and it
	// goes back to zero wherever Restarts does (PLAN §5.6). Set leaves it alone.
	Wakes int `json:"wakes,omitempty"`
	// WakeAt is when the last wake was saved, RFC 3339 UTC. The backoff before
	// the next wake and the comparison with the failed turn's marker are both
	// timed from it. Like RestartAt it is not checked on load: a value that
	// does not parse is read as the zero time.
	WakeAt string `json:"wake_at,omitempty"`
	// ContinuePending means the agent was resumed after a recovery and has
	// not yet been sent the line that tells it to carry on. It is cleared
	// once that line is typed, so a daemon restart does not type it twice.
	ContinuePending bool `json:"continue_pending,omitempty"`
	// UsagePauseAt is when the daemon stopped this running task's session
	// because of usage, RFC 3339 UTC. It is saved before the session is
	// stopped, so a daemon killed in between does not take the missing host
	// for a crash and recover it. The task stays running; the pause is this
	// field and nothing else. Set leaves it alone.
	UsagePauseAt string `json:"usage_pause_at,omitempty"`
	// UsageWakeAt is the earliest time the paused session may be started
	// again, RFC 3339 UTC. Empty means only a fresh usage reading decides
	// when it resumes. Like RestartAt, it is not checked on load: a value
	// that does not parse is read as the zero time.
	UsageWakeAt string `json:"usage_wake_at,omitempty"`
}

// ShipStates returns every valid value of Entry.Ship in the order a ship
// moves through them, stopped last. The slice is the caller's to keep.
//
// after_merge sits between merged and deploy_healthy: the merge is finished
// and the project's after-merge command is due or done. Whether it started and
// how it ended is in the run record (internal/daemonstate), not here, so the
// ledger entry gains no field (PLAN T2.2a).
func ShipStates() []string {
	return []string{"committed", "pushed", "pr_open", "handed_back", "ci_green", "merged", "after_merge", "deploy_healthy", "stopped"}
}

// Ledger is the whole file.
type Ledger struct {
	Tasks []Entry `json:"tasks"`
}

// Path is where a project's ledger lives.
func Path(project string) string {
	return filepath.Join(project, ".afk", "build", "ledger.json")
}

// lockPath is the file Update locks. It is separate from the ledger because
// the ledger is replaced by rename on every write, and a lock held on the old
// file would stop excluding anyone the moment the new one took its name.
func lockPath(project string) string {
	return filepath.Join(project, ".afk", "build", ".ledger.lock")
}

// Load reads a project's ledger. A missing file is an empty ledger. A file
// whose content is not a valid ledger is an error wrapping ErrCorrupt that
// names the file and the problem. A file that cannot be read at all (a
// permission problem, a directory in its place) is a plain error, not
// ErrCorrupt: nothing is known about its content, so "fix or delete it" would
// be the wrong advice.
func Load(project string) (Ledger, error) {
	l, _, _, err := read(Path(project))
	return l, err
}

// read is Load plus the raw bytes and whether the file exists, which Update
// needs to decide whether a write would change anything.
func read(path string) (l Ledger, data []byte, exists bool, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Ledger{}, nil, false, nil
		}
		return Ledger{}, nil, false, fmt.Errorf("reading ledger %s: %w", path, err)
	}
	l, problem := decode(data)
	if problem != nil {
		return Ledger{}, data, true, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, problem)
	}
	return l, data, true, nil
}

// decode parses and checks a ledger's bytes. The error says what is wrong
// without naming the file; read adds that.
func decode(data []byte) (Ledger, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return Ledger{}, errors.New("the file is empty")
	}
	// Checked by hand because decoding the JSON value null into a struct
	// succeeds and changes nothing, which would read a file holding only
	// "null" as an empty ledger.
	if trimmed[0] != '{' {
		return Ledger{}, errors.New(`the file must hold a JSON object like {"tasks": [...]}`)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l Ledger
	if err := dec.Decode(&l); err != nil {
		return Ledger{}, fmt.Errorf("not valid JSON for a ledger: %v", err)
	}
	// Decode stops at the end of the first value. Two ledgers glued together
	// by a bad merge would otherwise load as the first one alone.
	if _, err := dec.Token(); err != io.EOF {
		return Ledger{}, errors.New("there is more text after the end of the ledger")
	}
	if err := l.validate(); err != nil {
		return Ledger{}, err
	}
	return l, nil
}

// validate checks what the JSON decoder cannot: every entry has an id, no id
// is repeated, and every status is a known one. Load runs it on what it read
// and Update on what it is about to write, so the file never holds a ledger
// that cannot be read back.
func (l Ledger) validate() error {
	seen := make(map[string]bool, len(l.Tasks))
	for i, e := range l.Tasks {
		if e.ID == "" {
			return fmt.Errorf("task entry %d has no id", i+1)
		}
		if seen[e.ID] {
			return fmt.Errorf("task id %q appears more than once", e.ID)
		}
		seen[e.ID] = true
		if e.Status == "" {
			return fmt.Errorf("task %q has no status", e.ID)
		}
		if _, err := ParseStatus(string(e.Status)); err != nil {
			return fmt.Errorf("task %q has %v", e.ID, err)
		}
		if e.Ship != "" && !slices.Contains(ShipStates(), e.Ship) {
			return fmt.Errorf("task %q has unknown ship state %q (want one of %s)", e.ID, e.Ship, strings.Join(ShipStates(), ", "))
		}
		if e.CIReruns < 0 || e.CIFixCycles < 0 {
			return fmt.Errorf("task %q has a negative ci_reruns or ci_fix_cycles", e.ID)
		}
		if e.Restarts < 0 {
			return fmt.Errorf("task %q has a negative restarts", e.ID)
		}
		if e.Wakes < 0 {
			return fmt.Errorf("task %q has a negative wakes", e.ID)
		}
	}
	return nil
}

// Get returns the entry for id, if there is one.
func (l Ledger) Get(id string) (Entry, bool) {
	for _, e := range l.Tasks {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Status returns the task's status. A task with no entry is pending
// (PLAN §4.2), which is what lets a new project start with no ledger at all.
func (l Ledger) Status(id string) Status {
	if e, ok := l.Get(id); ok {
		return e.Status
	}
	return Pending
}

// Set puts the task in the given status, adding an entry when there is none,
// and reports whether anything changed. It does no I/O; call it inside Update
// to save the result.
//
// Only status and replaced_by are touched. Everything else on an existing
// entry (session, branch, attempts, times) is the daemon's record and stays.
//
// replaced_by belongs to superseded alone. A superseded task must name at
// least one replacing task and may not name itself: other tasks that depend
// on it wait for its replacements, so with none, or with only itself, they
// would wait for ever. Repeated ids are kept once. For every other status
// replacedBy is ignored and any stored list is cleared, so a task brought
// back from superseded does not carry a list that is no longer true.
//
// On an error the ledger is unchanged.
func (l *Ledger) Set(id string, st Status, replacedBy []string) (changed bool, err error) {
	if id == "" {
		return false, ErrEmptyID
	}
	if _, err := ParseStatus(string(st)); err != nil {
		return false, err
	}

	// Built fresh so the ledger never shares the caller's slice, and nil for
	// every status but superseded so the field is left out of the file.
	var repl []string
	if st == Superseded {
		for _, r := range replacedBy {
			if r == "" {
				return false, fmt.Errorf("%w: one of the replacing task ids for %s is empty", ErrNoReplacement, id)
			}
			if r == id {
				return false, fmt.Errorf("%w: %s", ErrSelfReplacement, id)
			}
			if !slices.Contains(repl, r) {
				repl = append(repl, r)
			}
		}
		if len(repl) == 0 {
			return false, fmt.Errorf("%w: %s", ErrNoReplacement, id)
		}
	}

	for i := range l.Tasks {
		e := &l.Tasks[i]
		if e.ID != id {
			continue
		}
		if e.Status == st && slices.Equal(e.ReplacedBy, repl) {
			return false, nil
		}
		e.Status, e.ReplacedBy = st, repl
		return true, nil
	}
	l.Tasks = append(l.Tasks, Entry{ID: id, Status: st, ReplacedBy: repl})
	return true, nil
}

// Update changes a project's ledger: it reads the file, hands the ledger to
// fn, and writes back what fn left. It is the only way the file is written.
//
// The lock is the point of this function. Writing a temp file and renaming it
// keeps a reader from ever seeing half a ledger, but it does nothing for two
// writers: both read the ledger, each adds its own entry, and the second
// rename replaces the first writer's file, so one task's record is lost with
// no error anywhere. Holding an exclusive flock from before the read until
// after the rename makes read-change-write one step per writer. The ledger is
// read under the lock for the same reason; anything read before it may be
// stale, which is why fn is given the ledger and callers must not pass in one
// they loaded earlier.
//
// The lock file is left in place afterwards. Removing it would let a writer
// already blocked on it go ahead holding a lock on a deleted inode while the
// next writer creates and locks a fresh file, and the two would run at once.
// It is an empty dot-file beside the ledger.
//
// An error from fn is returned as it is and nothing is written. A damaged
// ledger returns Load's ErrCorrupt error before fn runs and the file is left
// exactly as it was, so whatever can still be recovered from it by hand is
// not replaced. If fn leaves the ledger in a shape Load would reject, Update
// returns ErrInvalid and writes nothing.
//
// The file is not rewritten when the result is byte-for-byte what is already
// there, and a ledger with no tasks is not written where there was no file.
func Update(project string, fn func(*Ledger) error) error {
	path := Path(project)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	unlock, err := lockProject(project)
	if err != nil {
		return err
	}
	defer unlock()

	l, onDisk, exists, err := read(path)
	if err != nil {
		return err
	}
	if err := fn(&l); err != nil {
		return err
	}
	if err := l.validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	sortTasks(l.Tasks)
	if l.Tasks == nil {
		// So an emptied ledger is written as "tasks": [] and not null.
		l.Tasks = []Entry{}
	}
	out, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding ledger: %w", err)
	}
	out = append(out, '\n')

	if exists && bytes.Equal(out, onDisk) {
		return nil
	}
	if !exists && len(l.Tasks) == 0 {
		return nil
	}

	return replaceFile(dir, path, out)
}

// lockProject takes the exclusive lock Update and Init share and returns the
// function that releases it. The directory must already exist.
func lockProject(project string) (unlock func(), err error) {
	lp := lockPath(project)
	lock, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening ledger lock %s: %w", lp, err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, fmt.Errorf("locking %s: %w", lp, err)
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
	}, nil
}

// replaceFile writes out to path through a temp file in dir and a rename.
// The temp file must be in the ledger's directory: rename is only atomic
// within one filesystem. The pid in the name is enough to keep writers
// apart because the lock already allows one at a time; it is there so a
// leftover from a killed process is recognisable and gets overwritten.
func replaceFile(dir, path string, out []byte) error {
	tmp := filepath.Join(dir, fmt.Sprintf(".ledger.json.tmp.%d", os.Getpid()))
	if err := writeFileSync(tmp, out, 0o644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing temp ledger: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming ledger into place: %w", err)
	}
	return nil
}

// Exists reports whether the project has a ledger file, damaged or not. Load
// cannot say: it returns an empty ledger for a missing file, and a project
// with no ledger is not the same as a project whose ledger is empty.
func Exists(project string) bool {
	info, err := os.Stat(Path(project))
	return err == nil && info.Mode().IsRegular()
}

// Init creates a project's ledger with one done entry per id in done (repeated
// ids are kept once), or an empty ledger when there are none. It returns
// ErrExists, changing nothing, when a ledger is already there.
//
// It exists beside Update because an empty ledger is a statement that the
// project is new and nothing has been built, which Update deliberately cannot
// make: it never creates a file for an empty ledger, so that a no-op change
// does not turn "no ledger" into "empty ledger". Init takes the same lock as
// Update, so an Update racing it either sees the new file or runs first and
// makes Init return ErrExists.
func Init(project string, done []string) error {
	path := Path(project)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	unlock, err := lockProject(project)
	if err != nil {
		return err
	}
	defer unlock()

	if Exists(project) {
		return fmt.Errorf("%w: %s", ErrExists, path)
	}
	l := Ledger{Tasks: []Entry{}}
	for _, id := range done {
		if id == "" {
			return ErrEmptyID
		}
		if _, ok := l.Get(id); ok {
			continue
		}
		l.Tasks = append(l.Tasks, Entry{ID: id, Status: Done})
	}
	sortTasks(l.Tasks)
	out, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding ledger: %w", err)
	}
	return replaceFile(dir, path, append(out, '\n'))
}

// writeFileSync is os.WriteFile plus an fsync before close. Without the sync
// a power loss shortly after the rename can leave the ledger's name pointing
// at a file whose contents never reached the disk, and an empty ledger file
// is a damaged one.
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

// taskID is the shape of a master-plan task id: T, milestone, dot, task
// number, and an optional letter for a task that was split (T4.2a).
var taskID = regexp.MustCompile(`^T(\d+)\.(\d+)([a-z]?)$`)

// idKey breaks an id into the parts it sorts by. ok is false for anything
// that is not a task id, including one whose numbers do not fit an int.
func idKey(id string) (milestone, task int, letter string, ok bool) {
	m := taskID.FindStringSubmatch(id)
	if m == nil {
		return 0, 0, "", false
	}
	milestone, err1 := strconv.Atoi(m[1])
	task, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0, "", false
	}
	return milestone, task, m[3], true
}

// sortTasks orders entries the way the plan reads: by milestone, then task
// number, then letter, with the numbers compared as numbers so T4.2 comes
// before T4.2a and both before T4.10. A plain string sort would put T4.10
// before T4.2 and T10.1 before T2.1. The ledger does not check ids against
// the plan, so an id of another shape can be here; those go last, in string
// order, where they are easy to spot and do not disturb the rest.
func sortTasks(tasks []Entry) {
	slices.SortStableFunc(tasks, func(a, b Entry) int {
		am, at, al, aok := idKey(a.ID)
		bm, bt, bl, bok := idKey(b.ID)
		switch {
		case aok && !bok:
			return -1
		case !aok && bok:
			return 1
		case aok && bok:
			if am != bm {
				return am - bm
			}
			if at != bt {
				return at - bt
			}
			if al != bl {
				return strings.Compare(al, bl)
			}
		}
		// Two ids that are not task ids, or two spellings of the same
		// numbers (T4.2 and T04.2).
		return strings.Compare(a.ID, b.ID)
	})
}
