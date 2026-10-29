package rex

import "time"

// The three things going into an agent can do.
const (
	// GoInAfkAgent opens the agent through afk's own host (the Agent level).
	GoInAfkAgent = "afk_agent"
	// GoInFocusWindow brings the agent's Rex window forward.
	GoInFocusWindow = "focus_window"
	// GoInBandText only tells the owner where the agent runs.
	GoInBandText = "band_text"
)

// GoInFacts is everything the going-in choice depends on. The caller reads
// each fact; this file reads nothing, so the whole matrix is table-testable.
type GoInFacts struct {
	// Runtime is the session's runtime from host.json: "rex", "afk" or empty
	// (empty means afk, the only runtime before Rex existed).
	Runtime string
	// InsideRex is true when REX_SESSION and REX_BLOCK are both non-empty in
	// the caller's environment, which the caller reads.
	InsideRex bool
	// OwnerClientOnAgentSession is true when the owner's own Rex app client is
	// attached to the agent's session.
	OwnerClientOnAgentSession bool
	// WindowID is the agent's Rex window id, empty when unknown.
	WindowID string
}

// GoInAction is the chosen action and the cause in a plain sentence. The
// caller builds the full line ("open <project> › <session> in Rex") around it.
type GoInAction struct {
	Kind   string
	Reason string
}

// GoIn picks what going into an agent does. First match wins.
//
// focus_window is gated on InsideRex and on the owner's client because Rex's
// focus_window is session-wide (T17.1 amendment): it moves whichever client is
// attached to that session, not the caller. Fired from a terminal outside Rex,
// or when the owner's client is on another session, it would move the wrong
// screen or nothing, so those cases only say where the agent runs.
func GoIn(f GoInFacts) GoInAction {
	if f.Runtime != "rex" {
		return GoInAction{Kind: GoInAfkAgent, Reason: "This agent runs on afk's own host."}
	}
	switch {
	case !f.InsideRex:
		return GoInAction{Kind: GoInBandText, Reason: "You are not inside Rex, so afk cannot bring the agent's window forward."}
	case !f.OwnerClientOnAgentSession:
		return GoInAction{Kind: GoInBandText, Reason: "Your Rex app is not showing this agent's session, and bringing a window forward moves the whole session."}
	case f.WindowID == "":
		return GoInAction{Kind: GoInBandText, Reason: "afk does not know which Rex window holds this agent."}
	}
	return GoInAction{Kind: GoInFocusWindow, Reason: "Your Rex app is showing this agent's session."}
}

// OwnerClient picks the owner's own app client from a client list: connected
// app clients only, the Mac app before the iOS app, then the newest connection.
// The Mac is preferred because it is where the owner sits when afk runs; an
// unparseable ConnectedAt sorts oldest rather than failing the pick.
func OwnerClient(clients []Client) (Client, bool) {
	var best Client
	var bestAt time.Time
	found := false
	rank := func(c Client) int {
		if c.Platform == "macos" {
			return 1
		}
		return 0
	}
	for _, c := range clients {
		if c.Kind != "app" || c.ConnectionState != "connected" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, c.ConnectedAt)
		if !found || rank(c) > rank(best) || (rank(c) == rank(best) && at.After(bestAt)) {
			best, bestAt, found = c, at, true
		}
	}
	return best, found
}
