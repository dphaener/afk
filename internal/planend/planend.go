// Package planend is the one decision behind T13.3: whether a finished
// change session's planning agent should be ended. It is pure (no files, no
// host access, no clock, no network); the caller reads the session state,
// the summary and the chat, stops the host and writes the decision log.
package planend

import (
	"strings"

	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/plansession"
)

// Rule is the one-line rule text the caller records in the decision log.
const Rule = "a finished change session's agent is ended once its summary is saved and it waits for the owner (T13.3)"

// Input is everything Decide looks at.
type Input struct {
	HasState  bool
	State     plansession.State
	Summary   string // the text of .afk/plan/summary.md
	Chat      planchat.State
	HostAlive bool
}

// Decision is what the caller does: end the agent, or leave it.
type Decision struct {
	End    bool
	Reason string
}

// Decide ends the agent only when every condition holds. The order of the
// checks picks the most useful reason, not a different outcome: the host is
// checked first because with no agent there is nothing to say about the rest.
//
// The mode is compared against the three change modes rather than "not new"
// so a mode added later is left alone until someone decides it may be ended.
func Decide(in Input) Decision {
	if !in.HostAlive {
		return Decision{Reason: "its agent is not running"}
	}
	if !in.HasState {
		return Decision{Reason: "there is no planning session state"}
	}
	switch in.State.Mode {
	case plansession.ModeExtend, plansession.ModeAmend, plansession.ModeReconcile:
	case plansession.ModeNew:
		return Decision{Reason: "a new session is ended by afk plan approve"}
	default:
		return Decision{Reason: "the session is not a change session"}
	}
	if !plansession.Finished(in.State) {
		return Decision{Reason: "the session is not finished"}
	}
	if strings.TrimSpace(in.Summary) == "" {
		return Decision{Reason: "its summary is not saved"}
	}
	if in.Chat != planchat.StateWaitingForText && in.Chat != planchat.StateWaitingForChoice {
		return Decision{Reason: "its agent is still replying"}
	}
	return Decision{End: true, Reason: "the change session is finished, its summary is saved and its agent is waiting for you"}
}
