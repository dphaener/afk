// provider.go implements the provider sidecar, .afk/{name}/provider: a
// one-line file naming the agent CLI a session host runs (for example "codex").
// An absent file means the default provider, Claude, so every session written
// before this sidecar existed keeps its behavior.
//
// The sidecar exists because dashboard relaunches of a stopped host build
// HostSpawn{Name, Worktree} with no flags. The provider choice made at
// `afk new --provider` would otherwise be lost on that path, and the host
// would silently relaunch under Claude. Like the autonomous marker, it is the
// only carrier that survives a stopped-host relaunch.
//
// This file reads and writes the raw name only. It does not validate the name
// against internal/provider; callers resolve it and decide what an unknown
// name means.
package session

import (
	"os"
	"path/filepath"
	"strings"
)

// providerPath returns the sidecar's location: .afk/{name}/provider under
// worktree, alongside status.json, host.json, autonomous, and description.md.
func providerPath(worktree, name string) string {
	return filepath.Join(worktree, ".afk", name, "provider")
}

// WriteProviderMarker writes the provider name, plus a trailing newline, into
// an existing session dir. It overwrites any previous value, so a re-created
// session takes the provider it was created with and no stale one.
func WriteProviderMarker(dir, provider string) error {
	return os.WriteFile(filepath.Join(dir, "provider"), []byte(provider+"\n"), 0o644)
}

// ReadProvider returns the provider name recorded for {name} under worktree,
// with surrounding whitespace trimmed. Any read error (missing file, missing
// session dir, permission denied) returns "", which callers treat as the
// default provider. A read failure therefore falls back to Claude rather than
// failing the relaunch, which matches how IsAutonomous treats a stat error.
func ReadProvider(worktree, name string) string {
	raw, err := os.ReadFile(providerPath(worktree, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
