// Package dashmeta manages per-dashboard metadata for `afk dash` processes:
// which tmux pane each dashboard occupies, to support click-to-focus from
// hooks. This is distinct from session host metadata (internal/host): a host
// runs in the background, while a dashboard is the only place a session is
// actually viewable, so only the dashboard can record where it is.
//
// Location is <git-common-dir>/afk-dash/<pid>.json. The git common dir
// (from `git rev-parse --path-format=absolute --git-common-dir`) is stable
// across all worktrees in a repo, which matters because a hook firing with
// cwd set to a session's worktree must be able to find the dashboard file
// even when the dashboard runs in a different worktree. One file per pid:
// multiple dashboards run concurrently on the same machine, and a single
// shared file would mean the second overwrites the first and the first's
// cleanup deletes the second's entry.
package dashmeta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Entry is one dashboard's persisted metadata: enough to target the pane it
// occupies from another process.
type Entry struct {
	PID       int    `json:"pid"`
	Tmux      string `json:"tmux"`       // TMUX env value
	PaneID    string `json:"pane_id"`    // TMUX_PANE value
	ClientTTY string `json:"client_tty"` // from tmux display-message -p '#{client_tty}'
	StartedAt string `json:"started_at"`
}

// EntryPath is the calling process's own entry file,
// <git-common-dir>/afk-dash/<pid>.json, or "" when gitCommonDir is not
// absolute.
//
// Exported so the writer, the remover, and `afk dash`'s AFK_DEBUG
// diagnostic share ONE definition. Separate `fmt.Sprintf("%d.json", ...)` calls
// drift silently: a write and a remove that disagree leak an entry that outlives
// its dashboard and keeps aiming clicks at a dead pane, and a diagnostic that
// disagrees reports "nothing written" about a file sitting right there.
func EntryPath(gitCommonDir string) string {
	dir := Dir(gitCommonDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid()))
}

// WriteEntry persists e to afk-dash/<pid>.json under the given git common
// dir via a same-directory temp-file+rename (mirrors internal/host.WriteMeta)
// so readers never observe a torn file. When tmux or paneID is empty, this is
// a no-op: the dashboard is not running inside tmux, so there is no pane to
// record.
func WriteEntry(gitCommonDir, tmux, paneID, clientTTY, startedAt string) error {
	if tmux == "" || paneID == "" {
		return nil
	}
	if !filepath.IsAbs(gitCommonDir) {
		// Defensive: --path-format=absolute should always give an absolute
		// path, but if it doesn't, treat it as "no location available" and
		// write nothing rather than creating a directory somewhere unexpected.
		return nil
	}

	dir, err := EnsureDir(gitCommonDir)
	if err != nil {
		return err
	}

	e := Entry{
		PID:       os.Getpid(),
		Tmux:      tmux,
		PaneID:    paneID,
		ClientTTY: clientTTY,
		StartedAt: startedAt,
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling dash entry: %w", err)
	}
	data = append(data, '\n')

	tmp := filepath.Join(dir, fmt.Sprintf(".%d.json.tmp.%d", e.PID, e.PID))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp dash entry: %w", err)
	}
	final := EntryPath(gitCommonDir)
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming dash entry into place: %w", err)
	}
	return nil
}

// ReadEntry scans afk-dash/ under the given git common dir and returns the
// first entry whose pid is live (non-blocking syscall.Kill(pid, 0), advisory
// exactly as internal/discover's HostRunning is). Missing, unparseable, and
// dead-pid entries are treated alike as not-found (never an error). Entries
// it finds dead are removed, so a SIGKILLed dash self-heals.
//
// Only pid-named files are considered (isEntryFileName). The scan USED to accept
// every *.json in the directory, which quietly destroyed click-to-focus:
// afk-dash/ also holds request.json, a Request unmarshals into Entry without
// error because encoding/json ignores unknown fields, and the resulting
// Entry{PID: 0} then failed alive() and was deleted by the self-heal above — so
// every `afk notify` (which calls this) cancelled any click the user had just
// made. The match is POSITIVE rather than a request.json exception on purpose:
// this directory is a rendezvous point that has already grown a second file kind
// once, and a third would walk into the same deletion.
func ReadEntry(gitCommonDir string) (Entry, bool) {
	return scanEntries(gitCommonDir, true)
}

// PeekEntry is ReadEntry without the dead-pid removal: same answer, no writes.
//
// It exists for `afk notify --dry-run`, which must be able to report what a
// real run would target without changing anything — a diagnostic that mutates the
// state it is diagnosing is one you cannot use twice and get the same reading
// from. Every delivering path still calls ReadEntry, so the self-heal happens
// exactly where a stale entry would otherwise do harm.
func PeekEntry(gitCommonDir string) (Entry, bool) {
	return scanEntries(gitCommonDir, false)
}

// scanEntries is the shared scan behind ReadEntry and PeekEntry. selfHeal is the
// ONLY difference between them, so the two can never disagree about which entry
// is chosen — a second scan implementation would be free to pick differently and
// make --dry-run report a target the real run does not use.
func scanEntries(gitCommonDir string, selfHeal bool) (Entry, bool) {
	if !filepath.IsAbs(gitCommonDir) {
		return Entry{}, false
	}

	dir := filepath.Join(gitCommonDir, "afk-dash")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Entry{}, false
	}

	var result Entry
	var found bool

	for _, ent := range entries {
		if ent.IsDir() || !isEntryFileName(ent.Name()) {
			continue
		}

		path := filepath.Join(dir, ent.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}

		if alive(e.PID) {
			if !found {
				result = e
				found = true
			}
		} else if selfHeal {
			_ = os.Remove(path)
		}
	}

	return result, found
}

// RemoveEntry removes the entry file for the calling process, best-effort.
// Used on clean dashboard shutdown.
func RemoveEntry(gitCommonDir string) error {
	path := EntryPath(gitCommonDir)
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// isEntryFileName reports whether name is one of the entry files this package
// owns: a decimal pid followed by ".json", and nothing else. Pure, so the whole
// name matrix is table-testable without a directory.
//
// A leading '0' is rejected because no pid alive() could confirm is written that
// way, and requiring a digit first is also what keeps the atomic rename's
// ".<pid>.json.tmp.<pid>" out of the scan.
func isEntryFileName(name string) bool {
	digits := strings.TrimSuffix(name, ".json")
	if digits == name || digits == "" || digits[0] == '0' {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}

// alive reports whether pid is a live process, by sending it signal 0
// (syscall.Kill(pid, 0)) — this delivers no signal, only checks existence
// and permission. This is ADVISORY: PIDs get recycled by the OS after the
// original process exits, so a recycled PID can false-positive. The
// authoritative check is always attempting to interact with the actual pane.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
