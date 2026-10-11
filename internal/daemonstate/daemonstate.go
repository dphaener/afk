// Package daemonstate holds the three small files the build daemon keeps
// that nothing else owns: the pause flag, the per-project state file and the
// machine lock.
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
