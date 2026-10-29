// Package rexlayout holds the pure decisions behind running a session in a
// Rex window: what a window is called, whether a Rex session must be created
// or an existing one reused, and when a start is refused. It does no I/O and
// calls nothing, so the whole matrix is table-testable without a Rex server;
// the caller gathers the facts and acts on the answer.
package rexlayout

import "errors"

// TerminalSize is the size every afk window is created with, in columns and
// rows. The size is fixed at creation: a block's owner can block a later
// resize, so it is never adjusted afterwards.
var TerminalSize = Size{Columns: 160, Rows: 48}

// Size is a terminal size in character cells.
type Size struct {
	Columns, Rows int
}

// PlanningLabel is the whole label of the planning session's window. It has
// no session prefix because there is only one per project.
const PlanningLabel = "plan"

// labelSeparator joins the session name and the phase word. A middle dot is
// what the spec's example (`t4-3 · review`) uses.
const labelSeparator = " · "

// WindowLabel returns the label for a session's Rex window. A planning
// session is always "plan". Any other session reads "<session> · <word>",
// where the word is a plain name for the known phases and the phase text
// itself for any value this code does not know, so a new phase still shows
// something truthful instead of a blank.
func WindowLabel(session, phase string, planning bool) string {
	if planning {
		return PlanningLabel
	}
	return session + labelSeparator + phaseWord(phase)
}

func phaseWord(phase string) string {
	switch phase {
	case "planning":
		return "plan"
	case "executing":
		return "build"
	case "reviewing":
		return "review"
	case "fixing":
		return "fix"
	case "done":
		return "done"
	case "failed":
		return "failed"
	}
	return phase
}

// Facts is what the caller learned about the project's Rex session. The
// recorded id comes from a sibling session's host.json. The window label is
// deliberately not an input: labels are not unique, so a session is never
// chosen by its label.
type Facts struct {
	// ProjectName names a session this layout has to create.
	ProjectName string
	// SessionID is the Rex session id a sibling's host.json recorded, or
	// empty when no sibling recorded one.
	SessionID string
	// SessionListed says the recorded id appears in session.list. Two listed
	// sessions may share a name; only the id is compared.
	SessionListed bool
	// WindowHasBlock says the recorded window still holds a block. False
	// means the owner closed it by hand.
	WindowHasBlock bool
}

// Plan says what to do in Rex for one new session.
type Plan struct {
	// CreateSession is true when no usable session exists.
	CreateSession bool
	// SessionName is the name to give a created session; empty otherwise.
	SessionName string
	// SessionID is the existing session to open the window in; empty when
	// CreateSession is true.
	SessionID string
	// RecordedWindowGone is true when the recorded window was closed by hand.
	// The session is still reused and a new window is opened in it; the flag
	// lets the caller say so rather than silently ignoring the old id.
	RecordedWindowGone bool
}

// Layout decides between creating the project's Rex session and reusing the
// recorded one. A new window is always created either way. A recorded id that
// session.list does not show (closed by hand, or a server restart) is treated
// as absent and the session is created again; an empty session is never
// destroyed, so the next start finds it.
func Layout(in Facts) Plan {
	if in.SessionID == "" || !in.SessionListed {
		return Plan{CreateSession: true, SessionName: in.ProjectName}
	}
	return Plan{
		SessionID:          in.SessionID,
		RecordedWindowGone: !in.WindowHasBlock,
	}
}

// Backend words, as read from config.toml.
const (
	BackendAfk = "afk"
	BackendRex = "rex"
)

// Refusal returns the reason a new session must not start, or nil. The
// backend "afk" never needs Rex, so the Rex facts are ignored for it. A bad
// backend word arrives as configErr and is returned as is. Under "rex" a
// missing or silent Rex is refused with a message saying how to fix it, and
// never falls back to afk: the owner asked for Rex.
func Refusal(backend string, configErr error, rexInstalled, serverAnswers bool) error {
	if configErr != nil {
		return configErr
	}
	if backend != BackendRex {
		return nil
	}
	if !rexInstalled {
		return errors.New("Rex is not installed, so this session was not started. Install Rex, or set [runtime] backend = \"afk\" in config.toml to run sessions without it.")
	}
	if !serverAnswers {
		return errors.New("Rex is installed but its server does not answer, so this session was not started. Start the Rex server and try again, or set [runtime] backend = \"afk\" in config.toml to run sessions without it.")
	}
	return nil
}
