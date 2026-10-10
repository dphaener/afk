// autonomous.go implements the immutable autonomous-mode marker sidecar,
// .afk/{name}/autonomous: an empty file (or directory) that signals whether
// a session should run in autonomous mode. The upstream conductor treats the
// mode as ON when the --autonomous flag is passed OR this marker exists, and
// dashboard restarts re-run /afk:go --session {name} with no flags, so the
// marker is the only carrier that survives across restarts.
//
// Reading and writing both live here in internal/session as distinct entry
// points rather than splitting the reader into internal/discover: the marker
// is exactly as much "session state" as description.md, and consistency
// demands one source of truth for each sidecar's shape and location.
package session

import (
	"os"
	"path/filepath"
)

// autonomousPath returns the sidecar's location: .afk/{name}/autonomous under
// worktree, alongside status.json, host.json, description.md, and label.
func autonomousPath(worktree, name string) string {
	return filepath.Join(worktree, ".afk", name, "autonomous")
}

// WriteAutonomousMarker writes an empty autonomous marker file into an existing
// session dir. The marker signals that the session should run in autonomous mode,
// persisting across dashboard restarts that re-run /afk:go --session {name}
// without the --autonomous flag.
func WriteAutonomousMarker(dir string) error {
	return os.WriteFile(filepath.Join(dir, "autonomous"), []byte{}, 0o644)
}

// IsAutonomous returns true iff the autonomous marker for {name} under worktree
// exists. A file, directory, or any other stat-able path at the marker location
// all report true, matching the upstream conductor's "marker exists" check; any
// stat error (missing, permission denied, etc.) reports false. The marker's
// contents are never read.
func IsAutonomous(worktree, name string) bool {
	_, err := os.Stat(autonomousPath(worktree, name))
	return err == nil
}
