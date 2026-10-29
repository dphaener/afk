// Package controlroom is the one decision behind the hub's keep-alive: when
// the daemon should start the control-room window (a Rex session named "afk"
// running `afk dash`). The window has to come back after the owner closes it
// or its program ends, but a closed window must not be re-created in a loop,
// so a start is allowed at most once a minute. Under the afk backend the
// control room does not exist, so the decision says Nothing before anything
// else and the caller makes no Rex call at all.
//
// It is pure: no Rex, no files, no clock, no config. The caller reads the
// facts, runs the start and records when it did.
package controlroom

import "time"

// SessionName is the Rex session that holds the control room.
const SessionName = "afk"

// MinInterval is the shortest gap between two starts.
const MinInterval = time.Minute

// Kind says what the caller does.
type Kind int

const (
	// Nothing means make no start (and, for the early rules, no Rex call).
	Nothing Kind = iota
	// Start means create the control-room window now.
	Start
	// Wait means a start is due but one happened less than MinInterval ago.
	Wait
)

// Facts is everything Decide looks at.
type Facts struct {
	// Backend is the value of [runtime] backend ("afk" or "rex").
	Backend string
	// ConfigErr is true when the [runtime] table could not be read.
	ConfigErr bool
	// ServerAnswers is true when the Rex server answered its status call.
	ServerAnswers bool
	// SessionFound is true when the Rex session named SessionName exists.
	SessionFound bool
	// WindowAlive is true when that session has a window with a live program.
	WindowAlive bool
	// Now is the current time.
	Now time.Time
	// LastStart is when the daemon last started the window; zero when never.
	LastStart time.Time
}

// Action is the decision and the plain-language reason for it.
type Action struct {
	Kind   Kind
	Reason string
}

// Decide applies the rules in order. The order matters: the backend check
// comes first so an afk-only machine never needs the server facts, and the
// rate limit comes after "already open" so a healthy window is never counted
// as a restart.
func Decide(in Facts) Action {
	switch {
	case in.Backend != "rex" || in.ConfigErr:
		return Action{Nothing, "the control room runs only under the Rex backend"}
	case !in.ServerAnswers:
		return Action{Nothing, "the Rex server is not answering, so the control room waits"}
	case in.SessionFound && in.WindowAlive:
		return Action{Nothing, "the control room is already open"}
	case !in.LastStart.IsZero() && in.Now.Sub(in.LastStart) < MinInterval:
		return Action{Wait, "the control room restarts at most once a minute"}
	default:
		return Action{Start, "the control room is not open"}
	}
}
