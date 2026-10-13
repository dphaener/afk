// turnfailed.go reads the turn-failed marker, `.afk/{name}/.turn-failed`,
// described in plugins/afk/skills/conductor/status-schema.md section 10. It
// is one line {"at":"<RFC 3339 UTC>","reason":"…"}, written by the StopFailure
// hook for an API error worth trying again (a dropped connection, an
// overloaded or failing server). A rate limit writes the rate-limit marker
// instead, and the daemon waits for the usage reset for that one. This
// package only reads the marker. The hooks are its only writers, and
// status.json is never involved.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// TurnFailed is the content of a turn-failed marker. Reason is the
// StopFailure error type the hook recorded, and may be empty: the time alone
// is enough for the daemon to wake the session.
type TurnFailed struct {
	At     time.Time
	Reason string
}

// turnFailedMarker is the one-line JSON object the StopFailure hook writes.
type turnFailedMarker struct {
	At     string `json:"at"`
	Reason string `json:"reason"`
}

// ReadTurnFailed returns the marker for the session and ok true only when the
// file exists, parses as JSON, and its "at" parses as RFC 3339. A missing,
// empty, non-JSON, time-less or bad-time marker is ok false. A missing or
// empty "reason" is allowed and reads as "".
//
// The strictness matches ReadRateLimited and for the same reason. The daemon
// compares the marker's time with the session's wake and stall clocks, so a
// marker with no usable time cannot be told apart from one left over from an
// earlier failure. Treating it as present could type the resume command into
// a session that is healthy now, so a damaged marker counts as no marker.
func ReadTurnFailed(worktree, session string) (TurnFailed, bool) {
	data, err := os.ReadFile(filepath.Join(worktree, ".afk", session, ".turn-failed"))
	if err != nil {
		return TurnFailed{}, false
	}
	var m turnFailedMarker
	if err := json.Unmarshal(data, &m); err != nil || m.At == "" {
		return TurnFailed{}, false
	}
	t, err := time.Parse(time.RFC3339, m.At)
	if err != nil {
		return TurnFailed{}, false
	}
	return TurnFailed{At: t, Reason: m.Reason}, true
}
