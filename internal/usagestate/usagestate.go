// Package usagestate owns usage.json, the snapshot of the agent
// subscription's usage windows (docs/PLAN.md §4.5).
//
// The file is per machine, not per project: a subscription belongs to the
// person at this machine, so every project and every session on it spends
// from the same windows. Keeping one file under the XDG state directory
// means a probe made for one project is visible to all the others instead of
// each project paying for its own probe.
//
// Readers treat a missing, empty or unparseable file as "no snapshot", never
// as an error (PLAN §3). The file is machine-written scratch data that is
// replaced continuously; a reader that failed on a half-understood file would
// take the dashboard down for something the next probe repairs. Read
// therefore returns a bool, not an error, and ignores keys it does not know
// so a later task can add fields without breaking an older binary.
//
// Write goes through a temp file in the same directory as the target,
// because rename is only atomic within one filesystem: a temp file in
// /tmp could turn the final rename into a copy and let a reader see half a
// file.
package usagestate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

// SourceProbe marks a snapshot that came from a live probe of the provider.
const SourceProbe = "probe"

// fileName is the snapshot's name inside Dir.
const fileName = "usage.json"

// Deps is the injectable seam for the environment-touching operations,
// mirroring internal/uistate's Deps so tests never touch the real home.
type Deps struct {
	// Getenv looks up an environment variable, empty string if unset.
	Getenv func(string) string
	// HomeDir returns the user's home directory.
	HomeDir func() (string, error)
	// ReadFile reads a file's contents.
	ReadFile func(string) ([]byte, error)
	// WriteFile writes data to a file with the given mode.
	WriteFile func(path string, data []byte, mode os.FileMode) error
	// Rename renames (moves) oldpath to newpath.
	Rename func(oldpath, newpath string) error
	// MkdirAll creates a directory and any missing parents.
	MkdirAll func(path string, mode os.FileMode) error
	// Remove deletes a file. Used only to clean up a temp file after a
	// failed Rename; its own error is discarded.
	Remove func(path string) error
}

// DefaultDeps returns Deps wired to the real environment.
func DefaultDeps() Deps {
	return Deps{
		Getenv:    os.Getenv,
		HomeDir:   os.UserHomeDir,
		ReadFile:  os.ReadFile,
		WriteFile: os.WriteFile,
		Rename:    os.Rename,
		MkdirAll:  os.MkdirAll,
		Remove:    os.Remove,
	}
}

// withDefaults fills any nil field with its real-environment default.
func (d Deps) withDefaults() Deps {
	def := DefaultDeps()
	if d.Getenv == nil {
		d.Getenv = def.Getenv
	}
	if d.HomeDir == nil {
		d.HomeDir = def.HomeDir
	}
	if d.ReadFile == nil {
		d.ReadFile = def.ReadFile
	}
	if d.WriteFile == nil {
		d.WriteFile = def.WriteFile
	}
	if d.Rename == nil {
		d.Rename = def.Rename
	}
	if d.MkdirAll == nil {
		d.MkdirAll = def.MkdirAll
	}
	if d.Remove == nil {
		d.Remove = def.Remove
	}
	return d
}

// Dir is afk's per-machine state directory: $XDG_STATE_HOME/afk when that
// variable is set to an absolute path, else ~/.local/state/afk. A relative
// XDG_STATE_HOME is ignored, as the XDG spec requires, because it would put
// the file under whatever directory the process happened to start in.
//
// It checks the variable through Getenv rather than an os.UserConfigDir-style
// helper because those resolve to a Library path on darwin and ignore XDG
// entirely (the same reason internal/uistate.Path does it by hand).
func Dir(d Deps) (string, error) {
	d = d.withDefaults()
	if xdg := d.Getenv("XDG_STATE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "afk"), nil
	}
	home, err := d.HomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "afk"), nil
}

// Path is the snapshot file's location: Dir plus usage.json.
func Path(d Deps) (string, error) {
	dir, err := Dir(d)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Window is one usage window as stored on disk.
type Window struct {
	Kind    string  `json:"kind"`
	UsedPct float64 `json:"used_pct"`
	// ResetsAt is nil, and omitted from the file, when the reset time is
	// unknown; a zero time would serialize as year 1 and read as a real date.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Scope    string     `json:"scope,omitempty"`
}

// Governor is the decision the governor made from a snapshot, as the writer
// of the snapshot computed it. It is stored as words and times so this
// package never has to import internal/governor: it records what was decided,
// it does not decide. Readers show it as recorded; they do not recompute it,
// because the thresholds may have changed since the snapshot was written.
type Governor struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	// WakeAt is nil, and omitted from the file, when the decision has no wake
	// time (a zero time would serialize as year 1 and read as a real date).
	WakeAt *time.Time `json:"wake_at,omitempty"`
	At     time.Time  `json:"at"`
}

// File is the on-disk shape of usage.json. It holds the measurements and, when
// the writer made one, the governor's decision computed from them. A file
// written before the governor was stored has no governor key and reads with a
// nil Governor.
type File struct {
	At             time.Time `json:"at"`
	Provider       string    `json:"provider"`
	Windows        []Window  `json:"windows"`
	OverageEnabled bool      `json:"overage_enabled"`
	Source         string    `json:"source"`
	// Governor is nil, and omitted from the file, when no decision was stored.
	Governor *Governor `json:"governor,omitempty"`
}

// FromSnapshot converts a probe result into the file shape. Times are
// normalised to UTC so the file reads the same on every machine, and Windows
// is always a non-nil slice so the file says [] and not null.
func FromSnapshot(providerName string, s provider.UsageSnapshot) File {
	ws := make([]Window, 0, len(s.Windows))
	for _, w := range s.Windows {
		fw := Window{Kind: w.Kind, UsedPct: w.UsedPct, Scope: w.Scope}
		if !w.ResetsAt.IsZero() {
			t := w.ResetsAt.UTC()
			fw.ResetsAt = &t
		}
		ws = append(ws, fw)
	}
	return File{
		At:             s.At.UTC(),
		Provider:       providerName,
		Windows:        ws,
		OverageEnabled: s.OverageEnabled,
		Source:         SourceProbe,
	}
}

// Snapshot is the reverse of FromSnapshot: a nil reset time becomes the zero
// time again.
func (f File) Snapshot() provider.UsageSnapshot {
	ws := make([]provider.Window, 0, len(f.Windows))
	for _, w := range f.Windows {
		pw := provider.Window{Kind: w.Kind, UsedPct: w.UsedPct, Scope: w.Scope}
		if w.ResetsAt != nil {
			pw.ResetsAt = *w.ResetsAt
		}
		ws = append(ws, pw)
	}
	return provider.UsageSnapshot{At: f.At, Windows: ws, OverageEnabled: f.OverageEnabled}
}

// tmpCounter makes the temp file name unique within this process without
// reading the clock (darwin's clock is too coarse to rely on in a tight
// loop, see internal/uistate).
var tmpCounter atomic.Uint64

// Write stores f atomically: indented JSON in a same-directory temp file,
// then a rename over usage.json. A reader therefore sees the old file or the
// new one, never a partial write. Concurrent writers last-write-wins, which
// is right for a snapshot that the next probe supersedes.
func Write(d Deps, f File) error {
	d = d.withDefaults()
	if f.Windows == nil {
		f.Windows = []Window{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling usage snapshot: %w", err)
	}
	data = append(data, '\n')

	path, err := Path(d)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := d.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating state directory %s: %w", dir, err)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".usage.json.tmp.%d.%d", os.Getpid(), tmpCounter.Add(1)))
	if err := d.WriteFile(tmp, data, 0o644); err != nil {
		_ = d.Remove(tmp)
		return fmt.Errorf("writing temp usage file %s: %w", tmp, err)
	}
	if err := d.Rename(tmp, path); err != nil {
		_ = d.Remove(tmp)
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}

// Read loads the snapshot. The bool is false for a missing, unreadable,
// empty or unparseable file and for one whose "at" is zero (a file without a
// timestamp cannot be judged fresh or stale); in every such case the caller
// should act as if no snapshot exists. Unknown keys are ignored.
func Read(d Deps) (File, bool) {
	d = d.withDefaults()
	path, err := Path(d)
	if err != nil {
		return File{}, false
	}
	data, err := d.ReadFile(path)
	if err != nil {
		return File{}, false
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, false
	}
	if f.At.IsZero() {
		return File{}, false
	}
	return f, true
}
