// unattended.go implements the unattended-mode marker sidecar,
// .afk/{name}/unattended: an empty file whose existence means the session's
// agent runs in the provider's unattended permission mode, the one in which
// it never stops to ask whether it may run a command. The daemon creates
// sessions this way, because nobody is at the terminal to answer.
//
// It mirrors autonomous.go, with one difference that decides who reads it.
// The autonomous marker is read by the plugin's conductor, inside the agent.
// The unattended mode is an option on the agent CLI's command line, and by
// the time the plugin runs the CLI has already started, so the plugin cannot
// recover it. Go must read this marker itself, every time it builds a child
// command line: SpawnHost here (see resolveSpawnUnattended), and the host
// when it restarts a child (internal/host/restart.go).
//
// The file name is host.UnattendedMarkerName rather than a constant of this
// package, because internal/host needs it too and cannot import this package.
package session

import (
	"os"
	"path/filepath"

	"github.com/dphaener/afk/internal/host"
)

// unattendedPath returns the sidecar's location: .afk/{name}/unattended under
// worktree, alongside status.json, host.json and the autonomous marker.
func unattendedPath(worktree, name string) string {
	return filepath.Join(statusDir(worktree, name), host.UnattendedMarkerName)
}

// WriteUnattendedMarker writes an empty unattended marker file into an
// existing session dir. From then on every host spawned for the session, and
// every restart of its child, names the provider's unattended mode on the
// command line.
func WriteUnattendedMarker(dir string) error {
	return os.WriteFile(filepath.Join(dir, host.UnattendedMarkerName), []byte{}, 0o644)
}

// IsUnattended returns true iff the unattended marker for {name} under
// worktree exists. As with IsAutonomous, anything stat-able at the path
// counts and the contents are never read; any stat error reports false.
func IsUnattended(worktree, name string) bool {
	_, err := os.Stat(unattendedPath(worktree, name))
	return err == nil
}
