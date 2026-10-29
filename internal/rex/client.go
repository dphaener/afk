package rex

import (
	"context"
	"errors"
)

// Client is one program connected to the Rex server. Kind, Platform and Label
// come from the client's own info block; the probe and the owner-client choice
// (T17.10) read them. ConnectionState is "connected" or "lost"; Rex says to
// treat a missing value as connected, and that is left to the caller.
type Client struct {
	ID              string
	Kind            string
	Platform        string
	Label           string
	ConnectedAt     string
	ConnectionState string
	SessionIDs      []string
}

// ListClients lists connected clients, in the order they connected, limited to
// the given kinds when any are named. With no kinds the field is left out
// rather than sent as null.
func ListClients(ctx context.Context, c Caller, kinds ...string) ([]Client, error) {
	args := map[string]any{}
	if len(kinds) > 0 {
		args["kinds"] = kinds
	}
	raw, err := c.Call(ctx, "client.list", args)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Clients []struct {
			ClientID        string `json:"client_id"`
			ConnectedAt     string `json:"connected_at"`
			ConnectionState string `json:"connection_state"`
			Info            struct {
				Kind     string `json:"kind"`
				Platform string `json:"platform"`
				Label    string `json:"label"`
			} `json:"info"`
			SessionIDs []string `json:"session_ids"`
		} `json:"clients"`
	}
	if err := decodeLoose("client.list", raw, &reply); err != nil {
		return nil, err
	}
	out := make([]Client, 0, len(reply.Clients))
	for _, cl := range reply.Clients {
		out = append(out, Client{
			ID: cl.ClientID, Kind: cl.Info.Kind, Platform: cl.Info.Platform, Label: cl.Info.Label,
			ConnectedAt: cl.ConnectedAt, ConnectionState: cl.ConnectionState, SessionIDs: cl.SessionIDs,
		})
	}
	return out, nil
}

// FocusWindow makes a window the active one. It is session-wide: every client
// attached to that session switches (T17.1), so choosing whose client to
// move is the caller's job. Rex has no session.select. An empty id is refused
// before any call, because Rex would take an empty window_id as a request to
// clear the active window. The {revision} reply is ignored.
func FocusWindow(ctx context.Context, c Caller, sessionID, windowID string) error {
	if sessionID == "" || windowID == "" {
		return errors.New("Rex session or window id is empty; afk cannot bring a window forward without both")
	}
	_, err := c.Call(ctx, "session.focus_window", map[string]string{"session_id": sessionID, "window_id": windowID})
	return err
}
