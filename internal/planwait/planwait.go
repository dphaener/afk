// Package planwait is the one decision behind T8.13's notification: whether
// the daemon should tell the owner that a planning session is waiting for
// them. It is pure (no files, no network, no clock, no host access); the
// caller reads the chat, sends the message and writes the record.
package planwait

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/planchat"
)

// Action says what the caller does with the stored record of the last wait
// it notified about.
type Action int

const (
	// Keep leaves the record as it is.
	Keep Action = iota
	// Set writes Decision.Key, and only after the send succeeded.
	Set
	// Clear removes the record.
	Clear
)

// agentWho is the Who value of an agent message (provider.WhoAgent). It is
// spelled here so this package stays free of the agent-CLI package.
const agentWho = "agent"

const (
	maxFirstLine = 200
	emptyBody    = "The planning session is waiting for your answer."
	bodyTail     = "\n\nAnswer it in the chat on afk's web page, or in afk dash."
)

// Input is everything Decide looks at.
type Input struct {
	Project   string
	Chat      planchat.Chat
	Recorded  string // the key last notified about
	HasRecord bool
}

// Decision is what the caller does.
type Decision struct {
	Send    bool
	Message notify.Message
	Record  Action
	Key     string
}

func waiting(c planchat.Chat) bool {
	return c.State == planchat.StateWaitingForText || c.State == planchat.StateWaitingForChoice
}

// Key names one wait, or is "" when the session is not waiting. The
// transcript only grows, so an answer followed by a new wait always changes
// the key (more messages), and re-reading the same wait gives the same key.
func Key(c planchat.Chat) string {
	if !waiting(c) {
		return ""
	}
	var at time.Time
	var text string
	if n := len(c.Messages); n > 0 {
		at = c.Messages[n-1].At
		text = c.Messages[n-1].Text
	}
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%s|%d|%s|%s", c.State, len(c.Messages), at.Format(time.RFC3339Nano), hex.EncodeToString(sum[:])[:12])
}

// FirstLine is the line of the wait worth showing in a notification: the
// first non-empty line of the first question for a choice wait, or of the
// last agent message for a text wait, cut to 200 runes.
func FirstLine(c planchat.Chat) string {
	var text string
	switch c.State {
	case planchat.StateWaitingForChoice:
		if len(c.Questions) > 0 {
			text = c.Questions[0].Question
		}
	case planchat.StateWaitingForText:
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].Who == agentWho {
				text = c.Messages[i].Text
				break
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > maxFirstLine {
			line = string(r[:maxFirstLine]) + "…"
		}
		return line
	}
	return ""
}

// Decide chooses whether to notify. A transient read failure (working or
// unknown) keeps the record so the same wait is not announced twice. It never
// records before sending: the caller writes Set only after a successful send.
func Decide(in Input) Decision {
	switch in.Chat.State {
	case planchat.StateNone:
		// The host is gone: the next session's first wait is new.
		if in.HasRecord {
			return Decision{Record: Clear}
		}
		return Decision{Record: Keep}
	case planchat.StateWaitingForText, planchat.StateWaitingForChoice:
	default:
		return Decision{Record: Keep}
	}
	key := Key(in.Chat)
	if in.HasRecord && key == in.Recorded {
		return Decision{Record: Keep}
	}
	body := FirstLine(in.Chat)
	if body == "" {
		body = emptyBody
	}
	return Decision{
		Send: true,
		Message: notify.Message{
			Title: "afk: " + in.Project + " is waiting for you",
			Body:  body + bodyTail,
		},
		Record: Set,
		Key:    key,
	}
}
