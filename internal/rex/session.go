package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Session is one Rex session as session.list reports it.
type Session struct {
	ID    string
	Label string
}

// decodeLoose decodes a reply and ignores fields it does not know, because
// Rex adds fields between releases and an added field must not break afk.
// (encoding/json ignores unknown fields unless DisallowUnknownFields is set;
// this helper exists so that stays the one decoding rule for every call.)
func decodeLoose(method string, raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("Rex call %s returned an answer afk could not read: %w", method, err)
	}
	return nil
}

// ListSessions returns every session on the server, oldest first.
func ListSessions(ctx context.Context, c Caller) ([]Session, error) {
	raw, err := c.Call(ctx, "session.list", struct{}{})
	if err != nil {
		return nil, err
	}
	var reply struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Label     string `json:"label"`
		} `json:"sessions"`
	}
	if err := decodeLoose("session.list", raw, &reply); err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(reply.Sessions))
	for _, s := range reply.Sessions {
		out = append(out, Session{ID: s.SessionID, Label: s.Label})
	}
	return out, nil
}

// FindSession returns the first session whose label is exactly label. Labels
// are free text and not unique in Rex (T17.1), so the oldest match wins, which
// keeps a repeat call stable. No match is found=false with no error; any
// error from the call itself (including a CallError) propagates unchanged.
func FindSession(ctx context.Context, c Caller, label string) (Session, bool, error) {
	sessions, err := ListSessions(ctx, c)
	if err != nil {
		return Session{}, false, err
	}
	for _, s := range sessions {
		if s.Label == label {
			return s, true, nil
		}
	}
	return Session{}, false, nil
}

// CreateSession makes an empty session with the given label.
func CreateSession(ctx context.Context, c Caller, label string) (Session, error) {
	raw, err := c.Call(ctx, "session.create", map[string]any{"label": label})
	if err != nil {
		return Session{}, err
	}
	var reply struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeLoose("session.create", raw, &reply); err != nil {
		return Session{}, err
	}
	if reply.SessionID == "" {
		return Session{}, errors.New("Rex call session.create answered without a session id")
	}
	return Session{ID: reply.SessionID, Label: label}, nil
}

// SetLabel renames a session. The method is session.set_label, read from
// `rex api describe session.set_label`: it takes session_id and label, and an
// empty label restores the server's own.
func SetLabel(ctx context.Context, c Caller, sessionID, label string) error {
	if sessionID == "" {
		return errors.New("Rex session id is empty; afk cannot rename a session without it")
	}
	_, err := c.Call(ctx, "session.set_label", map[string]any{"session_id": sessionID, "label": label})
	return err
}
