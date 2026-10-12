// Package daemonstate holds the small files the build daemon keeps that
// nothing else owns: the pause flag, the stop marker, the ship-poll file, the
// per-project state file and the machine lock.
//
// They live apart from status.json, the ledger and usage.json on purpose.
// Each of those has its own writers and its own schema; adding the daemon's
// bookkeeping to them would make a second writer for a file that already has
// several. Readers of these files treat a missing or damaged file as "not
// there", never as an error that stops the daemon.
package daemonstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrAlreadyRunning is returned by Lock when another daemon on this machine
// already holds the machine lock.
var ErrAlreadyRunning = errors.New("another afk daemon is already running")

// writeAtomic writes data to path through a temp file in the same folder and
// a rename, so a reader never sees half a file. The rename must stay within
// one folder: across filesystems it is not atomic.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// --- Pause flag ---

// PausePath is the pause flag in a project's main checkout. The flag is
// written by `afk pause` and removed by `afk resume`; the daemon only reads
// it, so a person can stop new work without signalling a process.
func PausePath(project string) string {
	return filepath.Join(project, ".afk", "build", "paused")
}

// Pause sets the pause flag, recording when. Pausing twice is not an error
// and keeps the first time, so the flag says how long the project has been
// paused rather than when the person last pressed the key.
func Pause(project string, now time.Time) error {
	if Paused(project) {
		return nil
	}
	data, err := json.Marshal(struct {
		At string `json:"at"`
	}{At: now.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	if err := writeAtomic(PausePath(project), append(data, '\n')); err != nil {
		return fmt.Errorf("writing pause flag: %w", err)
	}
	return nil
}

// Resume removes the pause flag and reports whether there was one, so the
// command can say "was not paused" instead of claiming it did something.
func Resume(project string) (bool, error) {
	err := os.Remove(PausePath(project))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("removing pause flag: %w", err)
}

// Paused reports whether the flag exists. Its contents are never read: a
// damaged flag must still pause, because the safe reading of "something is
// there" is "stop".
func Paused(project string) bool {
	_, err := os.Stat(PausePath(project))
	return err == nil
}

// --- Stop marker ---

// StopPath is the stop marker in a project's main checkout. The daemon writes
// it when a deploy health check times out, so the line stops until the owner
// runs `afk resume`. It is a file, not a field in memory, because the daemon
// keeps nothing between ticks and must be restartable at any moment: a
// restarted daemon has to find the line still stopped.
func StopPath(project string) string {
	return filepath.Join(project, ".afk", "build", "stopped")
}

// stopRecord is the contents of the stop marker.
type stopRecord struct {
	At     string `json:"at"`
	Task   string `json:"task"`
	Reason string `json:"reason"`
}

// Stop writes the stop marker, naming the task and why. A second stop
// replaces the first: the newest reason is the one the owner needs to see.
func Stop(project, task, reason string, now time.Time) error {
	data, err := json.Marshal(stopRecord{
		At:     now.UTC().Format(time.RFC3339),
		Task:   task,
		Reason: reason,
	})
	if err != nil {
		return err
	}
	if err := writeAtomic(StopPath(project), append(data, '\n')); err != nil {
		return fmt.Errorf("writing stop marker: %w", err)
	}
	return nil
}

// Stopped reports whether the marker exists. Its contents are never read: a
// damaged marker must still stop the line, for the same reason a damaged
// pause flag still pauses. Stopping on a file nobody can parse is the safe
// direction; running on it would push a broken deploy further.
func Stopped(project string) bool {
	_, err := os.Stat(StopPath(project))
	return err == nil
}

// ReadStop returns the task and reason from the marker. ok is false only when
// there is no marker. A marker that exists but cannot be parsed returns ok
// true with empty task and reason, so the caller can still say "stopped" and
// tell the owner to run `afk resume`.
func ReadStop(project string) (task, reason string, ok bool) {
	data, err := os.ReadFile(StopPath(project))
	if err != nil {
		return "", "", false
	}
	var s stopRecord
	if err := json.Unmarshal(data, &s); err != nil {
		return "", "", true
	}
	return s.Task, s.Reason, true
}

// ClearStop removes the stop marker and reports whether there was one, so
// `afk resume` can say "was not stopped" instead of claiming it did something.
// Like Paused, existence is the signal, so a damaged marker is removed too.
func ClearStop(project string) (bool, error) {
	err := os.Remove(StopPath(project))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("removing stop marker: %w", err)
}

// --- After-merge run record ---

// AfterMerge is the record of one run of a project's after-merge command. It
// is how the ship tells "never started", "started and cut off" and "finished"
// apart after a crash (PLAN T2.2a). Every key is always written. An
// unfinished run has FinishedAt "".
type AfterMerge struct {
	Task       string `json:"task"`
	MergedSHA  string `json:"merged_sha"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
}

// AfterMergePath is the run record in a project's main checkout.
func AfterMergePath(root string) string {
	return filepath.Join(root, ".afk", "build", "after-merge.json")
}

// AfterMergeLogPath is where the command's output goes.
func AfterMergeLogPath(root string) string {
	return filepath.Join(root, ".afk", "build", "after-merge.log")
}

// WriteAfterMerge replaces the run record atomically, creating .afk/build
// when it is missing. Times in r should be RFC 3339 UTC strings.
func WriteAfterMerge(root string, r AfterMerge) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := writeAtomic(AfterMergePath(root), append(data, '\n')); err != nil {
		return fmt.Errorf("writing after-merge record: %w", err)
	}
	return nil
}

// ReadAfterMerge reads the run record. A missing file and a damaged one both
// return false: a damaged record must read as "no record of a run". What that
// means for the ship (run the command, or stop) is for the ship state
// machine to decide, not this reader.
func ReadAfterMerge(root string) (AfterMerge, bool) {
	data, err := os.ReadFile(AfterMergePath(root))
	if err != nil {
		return AfterMerge{}, false
	}
	var r AfterMerge
	if err := json.Unmarshal(data, &r); err != nil {
		return AfterMerge{}, false
	}
	return r, true
}

// --- Ship poll ---

// ShipPollPath is the ship-poll file in a project's main checkout. It records
// when GitHub was last asked about the ship, so the daemon's one-second tick
// does not turn into one API call per second. It is a file for the same reason
// the stop marker is: nothing may be kept in memory between ticks.
func ShipPollPath(project string) string {
	return filepath.Join(project, ".afk", "build", "ship-poll")
}

// shipPollRecord is the contents of the ship-poll file. FailingSince is set
// while GitHub reads keep failing and is cleared when one works again, so the
// daemon can tell a short blip from an outage that needs an owner.
type shipPollRecord struct {
	At           string `json:"at"`
	FailingSince string `json:"failing_since"`
}

// readShipPoll returns the poll record. ok is false when the file is missing
// or damaged, and callers then treat both fields as unset.
func readShipPoll(project string) (shipPollRecord, bool) {
	data, err := os.ReadFile(ShipPollPath(project))
	if err != nil {
		return shipPollRecord{}, false
	}
	var r shipPollRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return shipPollRecord{}, false
	}
	return r, true
}

// writeShipPoll replaces the ship-poll file atomically.
func writeShipPoll(project string, r shipPollRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := writeAtomic(ShipPollPath(project), append(data, '\n')); err != nil {
		return fmt.Errorf("writing ship poll: %w", err)
	}
	return nil
}

// parseRFC3339 returns the zero time for an empty or unparsable value. The
// zero time means "unset" to every caller, so a damaged value degrades to
// "poll now" rather than to an error.
func parseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// MarkShipPoll records that GitHub was just asked and got an answer. It keeps
// any failing_since already in the file: a good read does not end an outage
// by itself, and ClearShipPollFailure is the explicit step that does.
func MarkShipPoll(project string, now time.Time) error {
	prev, _ := readShipPoll(project)
	return writeShipPoll(project, shipPollRecord{
		At:           now.UTC().Format(time.RFC3339),
		FailingSince: prev.FailingSince,
	})
}

// LastShipPoll returns when GitHub was last asked. The zero time means the
// file is missing or damaged, which the daemon reads as "poll now".
func LastShipPoll(project string) time.Time {
	r, _ := readShipPoll(project)
	return parseRFC3339(r.At)
}

// MarkShipPollFailure records a failed GitHub read. It always updates At,
// because the read was still an ask and the rate limit must count it, and it
// sets failing_since only when the file does not already have one, so the
// start of the outage is kept rather than moved forward on every retry.
func MarkShipPollFailure(project string, now time.Time) error {
	prev, _ := readShipPoll(project)
	failing := prev.FailingSince
	if parseRFC3339(failing).IsZero() {
		failing = now.UTC().Format(time.RFC3339)
	}
	return writeShipPoll(project, shipPollRecord{
		At:           now.UTC().Format(time.RFC3339),
		FailingSince: failing,
	})
}

// ClearShipPollFailure empties failing_since after a read that worked. When
// nothing is set it writes nothing, so a clean project never grows a poll
// file just to say that there is no failure.
func ClearShipPollFailure(project string) error {
	prev, ok := readShipPoll(project)
	if !ok || prev.FailingSince == "" {
		return nil
	}
	prev.FailingSince = ""
	return writeShipPoll(project, prev)
}

// ShipPollFailingSince returns when the current run of failed GitHub reads
// began. The zero time means no failure is recorded, or the file is damaged.
func ShipPollFailingSince(project string) time.Time {
	r, _ := readShipPoll(project)
	return parseRFC3339(r.FailingSince)
}

// --- Project state ---

// State is what the daemon last did for a project, written only by the
// daemon so `afk` commands and the dashboard can say why it is idle. Every
// key is always present.
type State struct {
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Task    string `json:"task"`
	Session string `json:"session"`
	Reason  string `json:"reason"`
}

// StatePath is the project state file, .afk/build/daemon.json.
func StatePath(project string) string {
	return filepath.Join(project, ".afk", "build", "daemon.json")
}

// WriteState replaces the state file atomically.
func WriteState(project string, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := writeAtomic(StatePath(project), append(data, '\n')); err != nil {
		return fmt.Errorf("writing daemon state: %w", err)
	}
	return nil
}

// ReadState reads the state file. A missing or unparsable file is false and
// never an error: the daemon rewrites it every pass, so a damaged copy is
// just stale.
func ReadState(project string) (State, bool) {
	data, err := os.ReadFile(StatePath(project))
	if err != nil {
		return State{}, false
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, false
	}
	return s, true
}

// Same compares everything but At. The daemon uses it to skip a write (and
// a notification) when nothing but the clock changed.
func (s State) Same(other State) bool {
	s.At, other.At = "", ""
	return s == other
}

// --- Machine lock ---

type machine struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	// BinaryUnknown is true when the daemon could not read its own program
	// file, so it cannot notice a new build. It lives here because
	// `afk daemon status` already reads this file, and it is per daemon run:
	// Lock writes false.
	BinaryUnknown bool `json:"binary_unknown"`
}

// Lock takes the machine-wide daemon lock in dir (afk's state folder, chosen
// by the caller) so two daemons cannot run on one machine, and records who
// holds it in running.json. The name differs from the per-project daemon.json
// so the two are never confused. Only the daemon calls it.
//
// The lock is a non-blocking flock on daemon.lock: the kernel drops it when
// the process dies, so a crash never leaves a stale lock to clean up. The
// lock file is left in place on unlock, for the reason given in
// internal/ledger: removing it lets a second process lock a fresh inode while
// the first still holds the old one.
func Lock(dir string, pid int, now time.Time) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening daemon lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("locking daemon lock: %w", err)
	}
	data, err := json.Marshal(machine{PID: pid, StartedAt: now.UTC().Format(time.RFC3339)})
	if err == nil {
		err = writeAtomic(filepath.Join(dir, "running.json"), append(data, '\n'))
	}
	if err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, fmt.Errorf("writing running.json: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// ReadMachine reads running.json. ok is false when it is missing or damaged.
// The file can outlive the daemon, so it names the last holder, not a live
// one; the lock is the only proof of life.
func ReadMachine(dir string) (pid int, startedAt string, ok bool) {
	data, err := os.ReadFile(filepath.Join(dir, "running.json"))
	if err != nil {
		return 0, "", false
	}
	var m machine
	if err := json.Unmarshal(data, &m); err != nil {
		return 0, "", false
	}
	return m.PID, m.StartedAt, true
}

// MarkBinaryUnknown re-reads running.json, sets binary_unknown and writes it
// back atomically. A missing or damaged file is an error: there is no holder
// to mark.
func MarkBinaryUnknown(dir string) error {
	path := filepath.Join(dir, "running.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading running.json: %w", err)
	}
	var m machine
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("reading running.json: %w", err)
	}
	m.BinaryUnknown = true
	out, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := writeAtomic(path, append(out, '\n')); err != nil {
		return fmt.Errorf("writing running.json: %w", err)
	}
	return nil
}

// BinaryUnknown reports whether the daemon marked its program file unreadable.
// False when the file is missing or damaged.
func BinaryUnknown(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "running.json"))
	if err != nil {
		return false
	}
	var m machine
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	return m.BinaryUnknown
}
