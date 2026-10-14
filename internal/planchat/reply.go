package planchat

import (
	"fmt"
	"strings"

	"github.com/dphaener/afk/internal/provider"
)

// Reply is what the owner sends to the planning session from the web page:
// either typed text, or one list of option labels for each pending question,
// in the order of Chat.Questions. Exactly one of the two must be given.
type Reply struct {
	Text    string     `json:"text"`
	Choices [][]string `json:"choices"`
}

// RefusalReason says why a reply was refused, in a form a page script can
// branch on. The Message beside it is the sentence for the owner.
type RefusalReason string

const (
	// ReasonNoSession means no planning session is open.
	ReasonNoSession RefusalReason = "no_session"
	// ReasonNotWaiting means the session is busy, or its state is unknown.
	ReasonNotWaiting RefusalReason = "not_waiting"
	// ReasonWrongKind means text was sent to a session waiting for a
	// choice, or a choice to one waiting for text.
	ReasonWrongKind RefusalReason = "wrong_kind"
	// ReasonNotOffered means a label is not one the agent offered.
	ReasonNotOffered RefusalReason = "not_offered"
	// ReasonBadReply means the reply is malformed: empty, both kinds at
	// once, the wrong number of answers, or text that cannot be typed.
	ReasonBadReply RefusalReason = "bad_reply"
	// ReasonUnsupported means the session's agent cannot take this kind of
	// reply from the page. Check never returns it; the caller that knows
	// the agent does.
	ReasonUnsupported RefusalReason = "unsupported"
)

// Refusal is a reply that must not be sent. It implements error so a caller
// can return it, and its Message is already a plain sentence.
type Refusal struct {
	Reason  RefusalReason `json:"reason"`
	Message string        `json:"message"`
}

func (r *Refusal) Error() string { return r.Message }

func refuse(reason RefusalReason, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// Check decides whether r may be sent to the session described by chat, and
// returns nil when it may. It is a snapshot decision: the session's state can
// change between Check and the send (the agent may start a new turn), and
// that race is accepted, because the worst outcome is a reply typed into a
// busy agent, which it reads on its next prompt.
func Check(chat Chat, r Reply) *Refusal {
	hasText := r.Text != ""
	hasChoices := r.Choices != nil
	if hasText == hasChoices {
		return refuse(ReasonBadReply, "A reply needs either typed text or chosen options, and not both. Write your answer or pick an option, then send it again.")
	}
	switch chat.State {
	case StateNone:
		return refuse(ReasonNoSession, "No planning session is open for this project, so there is nothing to reply to. Start one with afk dash, then try again.")
	case StateUnknown:
		msg := "afk cannot tell what the planning session is waiting for, so it did not send your reply."
		if chat.Problem != "" {
			msg += " " + chat.Problem
		}
		return refuse(ReasonNotWaiting, "%s Open the session in afk dash to answer it there.", msg)
	case StateWorking:
		return refuse(ReasonNotWaiting, "The planning session is still working and is not waiting for you yet. Wait until it asks, then send your reply again.")
	case StateWaitingForChoice:
		if hasText {
			return refuse(ReasonWrongKind, "The planning session is waiting for you to pick from the options it offered, not for typed text. Choose an option and send again.")
		}
	case StateWaitingForText:
		if hasChoices {
			return refuse(ReasonWrongKind, "The planning session is waiting for typed text, not for a choice. Write your reply and send again.")
		}
		return nil
	default:
		return refuse(ReasonNotWaiting, "The planning session is not waiting for a reply. Wait until it asks, then send again.")
	}
	return checkChoices(chat.Questions, r.Choices)
}

func checkChoices(questions []provider.Question, choices [][]string) *Refusal {
	if len(choices) != len(questions) {
		return refuse(ReasonBadReply, "The planning session asked %d question(s) but your reply answers %d. Answer every question and send again.", len(questions), len(choices))
	}
	for i, q := range questions {
		labels := choices[i]
		switch {
		case !q.MultiSelect && len(labels) != 1:
			return refuse(ReasonBadReply, "Question %d needs exactly one option. Pick one and send again.", i+1)
		case q.MultiSelect && len(labels) == 0:
			return refuse(ReasonBadReply, "Question %d needs at least one option. Pick one or more and send again.", i+1)
		}
		seen := map[string]bool{}
		for _, l := range labels {
			if seen[l] {
				return refuse(ReasonBadReply, "Question %d lists the option %q more than once. Pick each option once and send again.", i+1, l)
			}
			seen[l] = true
			if !offered(q, l) {
				return refuse(ReasonNotOffered, "The option %q is not one the planning session offered for question %d. Reload the page to see the current options and pick again.", l, i+1)
			}
		}
	}
	return nil
}

func offered(q provider.Question, label string) bool {
	for _, o := range q.Options {
		if o.Label == label {
			return true
		}
	}
	return false
}

// TextKeys turns typed text into the writes that deliver it to the agent's
// terminal: the text wrapped in a bracketed paste, then Enter as its own
// write. The paste makes a multi-line reply one message instead of one
// message per line, and the separate Enter lets the agent finish taking the
// paste before it is submitted.
//
// Line endings are normalised to LF (a browser sends CRLF). Nothing else is
// trimmed: the owner's text is delivered as typed. Control bytes other than
// LF and tab are refused, and 0x7f too, because an ESC inside the text could
// end the bracketed paste early and let the rest run as keystrokes.
func TextKeys(text string) ([][]byte, *Refusal) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return nil, refuse(ReasonBadReply, "The reply is empty. Write something and send it again.")
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			return nil, refuse(ReasonBadReply, "The reply contains a control character that cannot be typed into the session. Remove it and send again.")
		}
	}
	return [][]byte{[]byte("\x1b[200~" + text + "\x1b[201~"), []byte("\r")}, nil
}
