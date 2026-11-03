package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Meta is the host process's persisted metadata: enough for another process
// (the CLI, the dashboard) to find and validate a running host without
// dialing it.
//
// LastOutputAt and WedgedSince are additive and optional on read: a host.json
// written before these fields existed has neither key, and that must parse as
// "unknown" (zero value), never as an error, so older hosts stay compatible
// with newer readers.
type Meta struct {
	PID          int      `json:"pid"`
	Socket       string   `json:"socket"`
	StartedAt    string   `json:"started_at"`
	Argv         []string `json:"argv"`
	LastOutputAt string   `json:"last_output_at,omitempty"`
	WedgedSince  string   `json:"wedged_since,omitempty"`
	// Provider names the binary this host runs (for example "codex" or
	// "opencode"). Empty means claude: hosts written before this field
	// existed omit the key.
	Provider string `json:"provider,omitempty"`
}

func metaDir(repoRoot, name string) string {
	return filepath.Join(repoRoot, ".afk", name)
}

func metaPath(repoRoot, name string) string {
	return filepath.Join(metaDir(repoRoot, name), "host.json")
}

// WriteMeta persists m to .afk/<name>/host.json under repoRoot via a
// same-directory temp-file+rename (mirrors writeStatusAtomic,
// internal/session/create.go:211-233) so readers never observe a torn file.
func WriteMeta(repoRoot, name string, m Meta) error {
	dir := metaDir(repoRoot, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling host meta: %w", err)
	}
	data = append(data, '\n')

	tmp := filepath.Join(dir, fmt.Sprintf(".host.json.tmp.%d", os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp host meta: %w", err)
	}
	final := filepath.Join(dir, "host.json")
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming host meta into place: %w", err)
	}
	return nil
}

// ReadMeta reads the host metadata written by WriteMeta.
func ReadMeta(repoRoot, name string) (Meta, error) {
	path := metaPath(repoRoot, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return m, nil
}

// Alive reports whether pid is a live process, by sending it signal 0
// (syscall.Kill(pid, 0)) — this delivers no signal, only checks existence
// and permission.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
