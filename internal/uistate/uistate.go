// Package uistate persists afk dashboard UI state that is machine-owned
// scratch data, not user configuration: today, the session last selected per
// project so a project switch (or a dashboard restart) can restore the
// previous pane instead of defaulting to the first row. It is a leaf
// package — no internal/ imports — deliberately kept separate from the
// settings file internal/daemonconfig reads (config.toml, written by its
// owner) so a corrupt or concurrently written state file can never take down
// config loading, and so internal/ui only ever reaches this through the
// function-typed seams it is given.
package uistate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// State is the on-disk shape of afk's UI state file: project path to the
// session name last selected for it, plus project path to a running count of
// deliberate opens (switcher selection, --project, or launching in a
// project's directory) — the frequency signal the switcher sorts by — plus
// the dashboard's sidebar width as an integer percentage of the terminal
// (0, omitted from the file, means "never set": use the default).
type State struct {
	Selected   map[string]string `json:"selected"`
	Opened     map[string]int    `json:"opened"`
	SidebarPct int               `json:"sidebar_pct,omitempty"`
}

// Deps is the injectable seam for uistate's environment-touching operations,
// in the style of internal/daemonconfig's Deps so both packages are hermetic
// and table-testable without a real home directory or filesystem.
type Deps struct {
	// Getenv looks up an environment variable, empty string if unset.
	Getenv func(string) string
	// HomeDir returns the user's home directory.
	HomeDir func() (string, error)
	// ReadFile reads a file's contents.
	ReadFile func(string) ([]byte, error)
	// WriteFile writes data to a file, creating it with the given mode if
	// it doesn't exist.
	WriteFile func(path string, data []byte, mode os.FileMode) error
	// Rename renames (moves) oldpath to newpath.
	Rename func(oldpath, newpath string) error
	// MkdirAll creates a directory and any missing parents.
	MkdirAll func(path string, mode os.FileMode) error
	// Remove deletes a file. Used only to clean up a temp file after a
	// failed Rename; its own error is always discarded, matching every
	// sibling atomic writer in this repo (see internal/dashmeta/dashmeta.go).
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

// withDefaults fills in any nil field on d with its real-environment
// default.
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

// Path resolves the UI state file's location: $XDG_STATE_HOME/afk/state.json
// when XDG_STATE_HOME is set, else ~/.local/state/afk/state.json.
//
// This deliberately does not call an os.UserConfigDir-style platform helper:
// on darwin that resolves to a Library path under the home directory and
// ignores XDG_STATE_HOME entirely (internal/daemonconfig.Path reads
// XDG_CONFIG_HOME by hand too), so it would pick the wrong path on this
// project's primary platform. Checking XDG_STATE_HOME explicitly through the
// Getenv seam is the only way to get XDG-compliant behavior on darwin.
func Path(d Deps) (string, error) {
	d = d.withDefaults()
	if xdg := d.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "afk", "state.json"), nil
	}
	home, err := d.HomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "afk", "state.json"), nil
}

// tmpCounter makes mutate's temp filename unique within this process: it
// increments on every call, regardless of the system clock. A clock-derived
// suffix does not work for this — on darwin, time.Now().UnixNano() has only
// microsecond granularity, so a tight loop of calls routinely produces
// repeated values, which would let one writer's Rename target another's
// still-being-written temp file.
var tmpCounter atomic.Uint64

// rawState mirrors the on-disk JSON schema. It exists separately from State
// only so Load can normalize a nil Selected or Opened map (from a missing
// key, an explicit null, or "{}") before handing State back to callers —
// every success path guarantees non-nil maps, which is load-bearing:
// Remember's `Selected[projectPath] = sessionName` and RecordOpen's
// `Opened[projectPath]++` assignments both panic on a nil map, and both run
// inside the dashboard's flush goroutine. Opened has to be mirrored here for
// the same reason Selected is: Load unmarshals through rawState rather than
// State directly, so a field that exists only on State would decode as its
// zero value (a nil map) on every read, silently discarding whatever
// RecordOpen or Remember wrote. SidebarPct is mirrored for the same reason.
type rawState struct {
	Selected   map[string]string `json:"selected"`
	Opened     map[string]int    `json:"opened"`
	SidebarPct int               `json:"sidebar_pct,omitempty"`
}

// Load reads and parses afk's UI state file. A missing file is the
// ordinary case — the first run, or a fresh XDG_STATE_HOME — so it returns
// an empty State and a nil error rather than an error; only a file that
// exists but can't be read or doesn't parse as JSON is an error. Both error
// paths wrap the underlying fs/json error with %w rather than folding it
// into a message string, because mutate distinguishes them with
// errors.Is/errors.As to decide whether an unreadable file must be left
// alone or a malformed one may be overwritten — a %v wrap would erase that
// distinction and make every malformed file behave like "write nothing".
func Load(d Deps) (State, error) {
	d = d.withDefaults()

	path, err := Path(d)
	if err != nil {
		return State{}, err
	}

	data, err := d.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return State{Selected: map[string]string{}, Opened: map[string]int{}}, nil
		}
		return State{}, fmt.Errorf("reading state %s: %w", path, err)
	}

	var raw rawState
	if err := json.Unmarshal(data, &raw); err != nil {
		return State{}, fmt.Errorf("parsing state %s: %w", path, err)
	}

	if raw.Selected == nil {
		raw.Selected = map[string]string{}
	}
	if raw.Opened == nil {
		raw.Opened = map[string]int{}
	}
	return State{Selected: raw.Selected, Opened: raw.Opened, SidebarPct: raw.SidebarPct}, nil
}

// mutate applies apply to the state file's current contents and writes the
// result back atomically. Remember and RecordOpen are the same
// read-modify-write-rename against different fields of one shared file, so
// the policy they share is stated here once: any divergence in how one of
// them treated a missing, malformed, or unreadable file would make that
// file's failure modes depend on which field was touched last, and a single
// body is what makes that impossible rather than merely intended.
//
// The read-error policy is deliberately narrow. Load's error is classified,
// not just checked for nil, because two very different situations both
// produce "the current file's contents are unusable":
//   - fs.ErrNotExist or a JSON decode error (*json.SyntaxError,
//     *json.UnmarshalTypeError) means the file is either absent or was
//     never valid — this state is machine-owned scratch data, not
//     user-authored, so mutate treats it as empty and overwrites it with
//     a file containing only the new entry.
//   - any other read error (EACCES, EIO, ...) means the file may hold good
//     data that this process simply can't see right now, so mutate
//     returns the error and writes nothing rather than risk clobbering it.
//
// Both maps are non-nil before apply runs, so apply may assign into either
// without a guard of its own (see rawState's doc comment).
func mutate(d Deps, apply func(*State)) error {
	d = d.withDefaults()

	state, err := Load(d)
	if err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &syntaxErr) && !errors.As(err, &typeErr) {
			return err
		}
		state = State{Selected: map[string]string{}, Opened: map[string]int{}}
	}
	if state.Selected == nil {
		state.Selected = map[string]string{}
	}
	if state.Opened == nil {
		state.Opened = map[string]int{}
	}
	apply(&state)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	path, err := Path(d)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := d.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating state directory %s: %w", dir, err)
	}

	// The temp file's name carries the process PID and tmpCounter, not a
	// clock reading, which is what makes it unique within this process by
	// construction (see tmpCounter's doc comment). That is ALL this name
	// buys: uniqueness of the temp file, so the dashboard's tick flush and
	// its inline quit flush never rename over each other's still-being-written
	// temp file. It does NOT make two concurrent mutate calls in this
	// process safe together — both still do their own read-modify-write-rename
	// of the shared state file, and whichever renames last wins, discarding
	// the other's merged entry. That applies across Remember and RecordOpen
	// as much as within either, since both reach the file through here:
	// callers making concurrent calls from the same process MUST serialize
	// them externally (see dash.go's selectionWriteMu); this package holds no
	// lock of its own.
	tmp := filepath.Join(dir, fmt.Sprintf(".state.json.tmp.%d.%d", os.Getpid(), tmpCounter.Add(1)))
	if err := d.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp state file %s: %w", tmp, err)
	}
	if err := d.Rename(tmp, path); err != nil {
		_ = d.Remove(tmp)
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}

// Remember records that sessionName is the last session selected for
// projectPath, merging it into the existing state file rather than
// replacing it wholesale. Empty projectPath or sessionName is a no-op
// (returns nil, writes nothing): callers pass through UI state that may not
// yet have both, and a partial entry would only pollute the file.
//
// This package holds no lock of its own, so a caller invoking Remember
// concurrently with itself or with RecordOpen MUST serialize the calls
// externally (dash.go's selectionWriteMu) — an unserialized pair would let
// whichever renames last silently discard the other's merged field. See
// mutate for that and for the read-error policy.
func Remember(d Deps, projectPath, sessionName string) error {
	if projectPath == "" || sessionName == "" {
		return nil
	}
	return mutate(d, func(s *State) { s.Selected[projectPath] = sessionName })
}

// RecordOpen increments the deliberate-open count for projectPath by one,
// merging it into the existing state file rather than replacing it
// wholesale. Empty projectPath is a no-op (returns nil, writes nothing): an
// empty path cannot come from a deliberate project activation, so recording
// one would only pollute the file.
//
// This package holds no lock of its own, so a caller invoking RecordOpen
// concurrently with itself or with Remember MUST serialize the calls
// externally (dash.go's selectionWriteMu) — an unserialized pair would let
// whichever renames last silently discard the other's merged field. See
// mutate for that and for the read-error policy.
func RecordOpen(d Deps, projectPath string) error {
	if projectPath == "" {
		return nil
	}
	return mutate(d, func(s *State) { s.Opened[projectPath]++ })
}

// RememberSidebarPct records the dashboard's sidebar width as an integer
// percentage of the terminal, merging it into the existing state file rather
// than replacing it wholesale. A pct <= 0 is a no-op (returns nil, writes
// nothing): zero is the "unset" value, so persisting it would only pollute
// the file.
//
// This package holds no lock of its own, so a caller invoking
// RememberSidebarPct concurrently with itself, Remember, or RecordOpen MUST
// serialize the calls externally (dash.go's selectionWriteMu) — an
// unserialized pair would let whichever renames last silently discard the
// other's merged field. See mutate for that and for the read-error policy.
func RememberSidebarPct(d Deps, pct int) error {
	if pct <= 0 {
		return nil
	}
	return mutate(d, func(s *State) { s.SidebarPct = pct })
}
