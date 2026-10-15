package planchat

import (
	"fmt"
	"strings"

	"github.com/dphaener/afk/internal/provider"
)

// Reply is what the owner sends to the planning session from the web page:
// typed text, one list of option labels for each pending question (in the
// order of Chat.Questions), or Other, the owner's own answer to a single
// open question instead of one of its options. Exactly one of the three must
// be given.
type Reply struct {
	Text    string     `json:"text"`
	Choices [][]string `json:"choices"`
	Other   string     `json:"other"`
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
	hasOther := r.Other != ""
	given := 0
	for _, b := range []bool{hasText, hasChoices, hasOther} {
		if b {
			given++
		}
	}
	if given != 1 {
		return refuse(ReasonBadReply, "A reply needs exactly one of typed text, chosen options or your own answer. Write your answer, pick an option or type your own answer, then send it again.")
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
		if hasOther {
			return checkOther(chat.Questions, r.Other)
		}
	case StateWaitingForText:
		if hasOther {
			return refuse(ReasonWrongKind, "The planning session is waiting for typed text, not for your own answer to a question. Write your reply and send again.")
		}
		if hasChoices {
			return refuse(ReasonWrongKind, "The planning session is waiting for typed text, not for a choice. Write your reply and send again.")
		}
		return nil
	default:
		return refuse(ReasonNotWaiting, "The planning session is not waiting for a reply. Wait until it asks, then send again.")
	}
	return checkChoices(chat.Questions, r.Choices)
}

// checkOther decides whether an own answer may go to the open question. Only
// a lone single-select question takes one: with several questions, or a
// multi-select one, there is no single place to type it, so the owner is sent
// to the dashboard rather than have the answer land on the wrong question.
func checkOther(questions []provider.Question, text string) *Refusal {
	if len(questions) != 1 || questions[0].MultiSelect {
		return refuse(ReasonUnsupported, "Your own answer can only be sent from the page for a single question where you pick one option. Answer this one in afk dash.")
	}
	return checkTypeable(text)
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
	text = normaliseNewlines(text)
	if refusal := checkTypeable(text); refusal != nil {
		return nil, refusal
	}
	return [][]byte{[]byte("\x1b[200~" + text + "\x1b[201~"), []byte("\r")}, nil
}

func normaliseNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// checkTypeable refuses blank text and text with control bytes, the one rule
// TextKeys and the own-answer check share so the two cannot drift apart.
func checkTypeable(text string) *Refusal {
	text = normaliseNewlines(text)
	if strings.TrimSpace(text) == "" {
		return refuse(ReasonBadReply, "The reply is empty. Write something and send it again.")
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			return refuse(ReasonBadReply, "The reply contains a control character that cannot be typed into the session. Remove it and send again.")
		}
	}
	return nil
}
