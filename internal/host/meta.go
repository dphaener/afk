package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// RuntimeAfk is the runtime word for a session run by afk's own host.
const RuntimeAfk = "afk"

// RuntimeRex is the runtime word for a session whose host runs inside a Rex
// terminal block (`afk _host --stdio`).
const RuntimeRex = "rex"

// RexBlock records where a Rex session lives. The ids are recorded verbatim
// from what the spawner passed in; the host never calls Rex, so it cannot
// check them and does not try. An absent "rex" key in host.json means the
// session is not in Rex. Server is empty for the machine's default Rex server.
type RexBlock struct {
	Server    string `json:"server,omitempty"`
	SessionID string `json:"session_id"`
	WindowID  string `json:"window_id,omitempty"`
	BlockID   string `json:"block_id"`
}

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
	// Runtime names the runtime that made this session, so every caller that
	// starts, stops, restarts, types into or opens it chooses the
	// implementation from this file rather than from a machine setting
	// (docs/PLAN.md §2 "Rex is optional"). Empty means "afk": a host.json
	// written before the field existed omits the key and must read as today.
	Runtime string `json:"runtime,omitempty"`
	// Rex holds the Rex block ids for a session with Runtime "rex". It is a
	// pointer so a host.json from before this field, or from an afk-runtime
	// host, reads as nil rather than as an empty block.
	Rex *RexBlock `json:"rex,omitempty"`
	// AgentStatus is the latest Program status (OSC 7501) root record the
	// session's own agent sent in its output; LastExit is how the previous
	// child generation ended (written after a restart in place, since a host
	// whose child exits on its own removes host.json). Both are pointers so
	// that "unknown" is an absent key rather than a zero record: a host.json
	// written before these fields existed, or by a host whose agent has not
	// reported yet, parses with both nil, and older readers ignore the keys.
	AgentStatus *AgentStatus `json:"agent_status,omitempty"`
	LastExit    *LastExit    `json:"last_exit,omitempty"`
}

// AgentStatus is what the agent last said about itself. State, Kind and Msg
// are the report's own fields (state is never "clear": a root clear removes
// the record). At is RFC 3339 UTC and is when the HOST saw the report, not a
// time the agent claimed, because reports carry no timestamp.
type AgentStatus struct {
	State string `json:"state"`
	Kind  string `json:"kind,omitempty"`
	Msg   string `json:"msg,omitempty"`
	At    string `json:"at"`
}

// LastExit is how one child generation ended. Code is the exit status, or -1
// when a signal killed the child, in which case Signal names it (for example
// "SIGTERM"); Signal is empty for an ordinary exit. At is RFC 3339 UTC, when
// the host reaped the child.
type LastExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	At     string `json:"at"`
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
