// Package planchat turns a planning session's conversation into a chat and
// decides whether the session is waiting for the owner (docs/PLAN.md T8.3).
//
// It is the one place that decision lives, because several things consume it:
// the JSON address GET /api/p/{project}/plan/chat (T8.3), the web page's reply
// box, which must know whether to offer a text box or the offered choices
// (T8.4), the page's chat view (T8.12), and the "the planning session is
// waiting for you" notice (T8.13). If each of them decided "waiting" on its
// own they would disagree.
//
// The package is pure. Finding the planning host, checking that it is alive
// and reading the transcript all happen in the caller and arrive through Deps,
// so every branch is a table row and this package never names a provider,
// touches a file or imports the host or session packages.
package planchat

import (
	"errors"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

// State says what the planning session is doing, from the owner's side.
type State string

const (
	// StateNone means no planning session is open for the project (or its
	// agent cannot run one), so there is nothing to show.
	StateNone State = "none"
	// StateWorking means the agent is busy: mid-turn, or its turn ended
	// with background work still running that will wake it again.
	StateWorking State = "working"
	// StateWaitingForText means the agent finished its turn and waits for
	// the owner to type a reply.
	StateWaitingForText State = "waiting_for_text"
	// StateWaitingForChoice means the agent asked a question with options
	// and waits for the owner to pick; Chat.Questions holds them.
	StateWaitingForChoice State = "waiting_for_choice"
	// StateUnknown means a planning session is running but its conversation
	// could not be read; Chat.Problem says why in plain words.
	StateUnknown State = "unknown"
)

// Chat is the planning session as the web page shows it. The JSON tags are
// the API's names. Messages and Questions are never nil, so they encode as []
// rather than null and a page script can iterate them without a check.
type Chat struct {
	Project  string    `json:"project"`
	State    State     `json:"state"`
	Messages []Message `json:"messages"`
	// Questions are the questions waiting for a choice; empty unless State
	// is StateWaitingForChoice.
	Questions []provider.Question `json:"questions"`
	// Problem is a plain sentence for the owner when State is
	// StateUnknown, and empty otherwise.
	Problem string `json:"problem"`
}

// Message is one bubble of the chat.
type Message struct {
	// Who is provider.WhoAgent or provider.WhoOwner.
	Who  string    `json:"who"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	// Questions are set on an agent message that asked the owner to choose,
	// so the chat can show the options it offered (answered or not).
	Questions []provider.Question `json:"questions,omitempty"`
}

// HostInfo is what the caller learned about the project's planning host.
type HostInfo struct {
	// StartedAt is when the host started. The transcript is looked up by
	// it, and a transcript that does not exist yet shortly after it is
	// normal: the agent has not written its first line.
	StartedAt time.Time
	// Provider names the agent the host runs; empty means the default. It
	// is passed through to Deps.Conversation untouched, so this package
	// never interprets a provider name.
	Provider string
	// Alive is whether the host's process is running.
	Alive bool
}

// Deps carries everything Read needs from outside. All fields are required.
type Deps struct {
	// Host reports the planning host for a project's main checkout; false
	// when the project has no planning host at all.
	Host func(root string) (HostInfo, bool)
	// Conversation reads the planning conversation with the named
	// provider. It is only called for a live host, because reading a
	// transcript is file work the page's refresh must not do for a session
	// that is not running (PLAN T8.3 Must not).
	Conversation func(providerName string, q provider.ConversationQuery) ([]provider.ConversationEntry, error)
	// Home is the user's home directory, passed to Conversation.
	Home string
	// Now is the clock, for telling a session that has not written its
	// transcript yet from one whose transcript is lost.
	Now func() time.Time
}

// transcriptGrace is how long after the host starts a missing transcript is
// read as "still starting" rather than "lost". The transcript only appears
// once the agent CLI has started and taken the planning command, which can
// take seconds after the host records its start; two minutes covers a slow
// start without hiding a real problem for long.
const transcriptGrace = 2 * time.Minute

// problemNoTranscript is shown when the host runs but no transcript is found
// after the grace period. The likely causes are a session started by hand
// (not with the planning command the reader looks for) or a cleared one, and
// the dashboard can still show it, so the sentence says where to look.
const problemNoTranscript = "The planning session is running, but afk cannot find its conversation. It may have been started by hand or cleared; open it in afk dash to see it."

// problemReadPrefix starts the sentence shown when reading failed for any
// other reason; the error follows it.
const problemReadPrefix = "afk could not read the planning session's conversation: "

// Read returns the project's planning session as a chat.
//
// The order of checks matters: liveness first, so a dead or missing host
// never costs a transcript read, then the provider's answer. A provider with
// no planning session is StateNone rather than a problem, because it is not
// broken; it simply has nothing to show.
func Read(project, root string, d Deps) Chat {
	chat := Chat{Project: project, State: StateNone, Messages: []Message{}, Questions: []provider.Question{}}
	info, ok := d.Host(root)
	if !ok || !info.Alive {
		return chat
	}
	entries, err := d.Conversation(info.Provider, provider.ConversationQuery{Home: d.Home, Root: root, StartedAt: info.StartedAt})
	switch {
	case err == nil:
	case errors.Is(err, provider.ErrNoPlanSession):
		return chat
	case errors.Is(err, provider.ErrNoTranscript):
		// A negative age (the host's clock ahead of ours) is also "just
		// started": it cannot be stale.
		if d.Now().Sub(info.StartedAt) <= transcriptGrace {
			chat.State = StateWorking
			return chat
		}
		chat.State = StateUnknown
		chat.Problem = problemNoTranscript
		return chat
	default:
		chat.State = StateUnknown
		chat.Problem = problemReadPrefix + err.Error()
		return chat
	}
	chat.Messages, chat.State, chat.Questions = Build(entries)
	return chat
}

// Build turns conversation entries into chat messages and decides the state.
// The returned slices are never nil.
//
// Only text, questions and answers become messages; tool calls, turn ends,
// interruptions and background work are signals for the state, not things
// the owner needs to read in a chat.
//
// The state is decided from the end of the conversation:
//   - The last question with no answer after it means the agent waits for a
//     choice. Only the last question counts: an earlier unanswered one was
//     superseded (the agent moved on), and the answer is matched by ID so
//     an answer to an older question does not settle a newer one.
//   - Otherwise the agent waits for typed text when its last entry, ignoring
//     background starts and ends, ended the turn or was an interruption, and
//     every background job it started has finished. A background job still
//     running reports back to the agent when it finishes and the agent
//     takes a new turn, so the session is not really waiting even though
//     the turn ended.
//   - Everything else is working, including no entries at all: a session
//     whose transcript is empty has not yet answered its opening command.
func Build(entries []provider.ConversationEntry) ([]Message, State, []provider.Question) {
	msgs := []Message{}
	lastQuestion := -1
	answered := map[string]bool{}
	running := map[string]bool{}
	var lastKind provider.EntryKind
	for i, e := range entries {
		switch e.Kind {
		case provider.EntryText:
			msgs = append(msgs, Message{Who: e.Who, Text: e.Text, At: e.At})
		case provider.EntryQuestion:
			msgs = append(msgs, Message{Who: provider.WhoAgent, Text: questionText(e.Questions), At: e.At, Questions: e.Questions})
			lastQuestion = i
			// A question asked again under the same ID (never seen,
			// but cheap to get right) is unanswered until a later
			// answer arrives.
			delete(answered, e.ID)
		case provider.EntryAnswer:
			msgs = append(msgs, Message{Who: provider.WhoOwner, Text: answerText(e.Answers), At: e.At})
			answered[e.ID] = true
		case provider.EntryBackgroundStarted:
			running[e.ID] = true
		case provider.EntryBackgroundDone:
			// Deleting an ID that was never started is a no-op, so a
			// finish seen before its start does not count as "later".
			delete(running, e.ID)
		}
		if e.Kind != provider.EntryBackgroundStarted && e.Kind != provider.EntryBackgroundDone {
			lastKind = e.Kind
		}
	}
	if lastQuestion >= 0 && !answered[entries[lastQuestion].ID] {
		pending := entries[lastQuestion].Questions
		if pending == nil {
			pending = []provider.Question{}
		}
		return msgs, StateWaitingForChoice, pending
	}
	if (lastKind == provider.EntryTurnEnd || lastKind == provider.EntryInterrupted) && len(running) == 0 {
		return msgs, StateWaitingForText, []provider.Question{}
	}
	return msgs, StateWorking, []provider.Question{}
}

// questionText is the text of a question message: each question on its own
// line, so the chat reads like what was asked even where the options are not
// drawn.
func questionText(qs []provider.Question) string {
	lines := make([]string, 0, len(qs))
	for _, q := range qs {
		lines = append(lines, q.Question)
	}
	return strings.Join(lines, "\n")
}

// answerText is the text of an answer message: one "header: label" line per
// answer, or the label alone when the answer has no header (the reader's
// fallback when the transcript kept only the tool result's text).
func answerText(as []provider.Answer) string {
	lines := make([]string, 0, len(as))
	for _, a := range as {
		if a.Header == "" {
			lines = append(lines, a.Label)
			continue
		}
		lines = append(lines, a.Header+": "+a.Label)
	}
	return strings.Join(lines, "\n")
}
