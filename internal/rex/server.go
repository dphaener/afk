package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// MinRevision is the oldest Rex api_revision afk supports. It is the revision
// T17.1 tested against; no version number is compared because the docs name
// none.
const MinRevision = 7

// ErrTooOld marks a Rex server older than MinRevision. The error returned by
// CheckServer wraps it with the revision needed and the server's own.
var ErrTooOld = errors.New("Rex server is too old")

// Server is what server.status says about the Rex server a Caller reaches.
//
// Revision 0 means "unknown": the login-server proxy reports 0 (T17.1,
// measured), so 0 is not evidence of an old server.
type Server struct {
	Revision   int
	InstanceID string
	Sessions   int
}

// CheckServer asks the server behind c for its api_revision.
//
// A revision of 0 is returned with no error, because the login-server proxy
// reports 0 and an unknown revision must not read as too old. A revision from
// 1 to MinRevision-1 returns ErrTooOld. A reply that is not JSON, or that has
// no api_revision, is a parse error and never a zero revision, so a broken
// answer cannot pass for the proxy.
//
// The result is not cached: the caller decides how often to ask, and a server
// can be restarted at a different revision between asks.
func CheckServer(ctx context.Context, c Caller) (Server, error) {
	raw, err := c.Call(ctx, "server.status", struct{}{})
	if err != nil {
		return Server{}, fmt.Errorf("checking the Rex server: %w", err)
	}
	var reply struct {
		APIRevision  *int   `json:"api_revision"`
		InstanceID   string `json:"instance_id"`
		SessionCount *int   `json:"session_count"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Server{}, fmt.Errorf("Rex server.status reply is not usable JSON: %w", err)
	}
	if reply.APIRevision == nil {
		return Server{}, errors.New("Rex server.status reply has no api_revision")
	}
	s := Server{Revision: *reply.APIRevision, InstanceID: reply.InstanceID}
	if reply.SessionCount != nil {
		s.Sessions = *reply.SessionCount
	}
	if s.Revision >= 1 && s.Revision < MinRevision {
		return s, fmt.Errorf("this Rex server reports api revision %d; afk needs revision %d or later — update Rex: %w",
			s.Revision, MinRevision, ErrTooOld)
	}
	return s, nil
}
