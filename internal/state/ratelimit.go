// ratelimit.go reads the rate-limit marker described in
// plugins/afk/skills/conductor/status-schema.md section 10 ("Rate-limit
// marker"): `.afk/{name}/.rate-limited`, one line {"at":"<RFC 3339 UTC>"},
// written by the StopFailure hook (plugins/afk/hooks/on-stop-failure.sh).
// This package only reads it. The hooks own the file, and status.json is
// never involved.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// rateLimitMarker is the one-line JSON object the StopFailure hook writes.
type rateLimitMarker struct {
	At string `json:"at"`
}

// ReadRateLimited returns the time the session's rate-limit marker says the
// agent was rate-limited, and ok true only when the file exists and its
// "at" parses as RFC 3339. A missing, empty, unparseable, or time-less
// marker is ok false.
//
// This is deliberately stricter than daemonstate.Paused, which never reads
// the pause flag's contents and treats a damaged flag as paused. The caller
// compares the marker's time with the session's last restart, so a marker
// with no usable time cannot be told apart from one left over from before
// that restart. Treating it as present could pause a session that is
// healthy now, so a damaged marker counts as no marker.
func ReadRateLimited(worktree, session string) (at time.Time, ok bool) {
	data, err := os.ReadFile(filepath.Join(worktree, ".afk", session, ".rate-limited"))
	if err != nil {
		return time.Time{}, false
	}
	var m rateLimitMarker
	if err := json.Unmarshal(data, &m); err != nil || m.At == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, m.At)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
