package planchat

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

var t0 = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

// scopeQuestion is a two-option question whose second option is the
// recommended one, so a row can check the flag survives into pending.
var scopeQuestion = provider.Question{
	Question: "Where should the plan be shown?",
	Header:   "Surface",
	Options: []provider.Option{
		{Label: "In the terminal", Description: "afk dash only"},
		{Label: "A web page (recommended)", Description: "the daemon's page", Recommended: true},
	},
}

var sizeQuestion = provider.Question{
	Question:    "Which sizes?",
	Header:      "Size",
	MultiSelect: true,
	Options:     []provider.Option{{Label: "Small"}, {Label: "Large"}},
}

func text(who, s string, sec int) provider.ConversationEntry {
	return provider.ConversationEntry{Kind: provider.EntryText, Who: who, Text: s, At: at(sec)}
}

func kind(k provider.EntryKind, id string, sec int) provider.ConversationEntry {
	return provider.ConversationEntry{Kind: k, ID: id, At: at(sec)}
}

func question(id string, sec int, qs ...provider.Question) provider.ConversationEntry {
	return provider.ConversationEntry{Kind: provider.EntryQuestion, Who: provider.WhoAgent, ID: id, At: at(sec), Questions: qs}
}

func answer(id string, sec int, as ...provider.Answer) provider.ConversationEntry {
	return provider.ConversationEntry{Kind: provider.EntryAnswer, Who: provider.WhoOwner, ID: id, At: at(sec), Answers: as}
}

// TestBuild is the matrix for the chat and the waiting decision. The
// load-bearing rows:
//   - "pending question with options": the only way the page learns it must
//     offer choices, and that the recommended option is marked.
//   - "answered question": an answer must clear the choice by ID, or the page
//     would keep offering a question that was already answered.
//   - "background pending after a turn end": the turn ended but the agent
//     will be woken again, so typing must not be offered yet; without this
//     row a plain "last entry is a turn end" rule passes every other case.
//   - "background finished then turn end": the matching done must release
//     the wait, or a session that ever used a background agent would read as
//     working forever.
//   - "still working": a tool call as the last entry is mid-turn.
//   - "no entries": an empty transcript is a session that has not answered
//     yet, not one waiting for the owner.
func TestBuild(t *testing.T) {
	tests := []struct {
		name        string
		entries     []provider.ConversationEntry
		wantMsgs    []Message
		wantState   State
		wantPending []provider.Question
	}{
		{
			name: "plain exchange",
			entries: []provider.ConversationEntry{
				text(provider.WhoOwner, "Plan a web page", 0),
				kind(provider.EntryTool, "", 1),
				text(provider.WhoAgent, "Here is the plan.", 2),
				kind(provider.EntryTurnEnd, "", 3),
			},
			wantMsgs: []Message{
				{Who: provider.WhoOwner, Text: "Plan a web page", At: at(0)},
				{Who: provider.WhoAgent, Text: "Here is the plan.", At: at(2)},
			},
			wantState:   StateWaitingForText,
			wantPending: []provider.Question{},
		},
		{
			name: "pending question with options",
			entries: []provider.ConversationEntry{
				text(provider.WhoOwner, "Plan a web page", 0),
				question("q1", 1, scopeQuestion, sizeQuestion),
			},
			wantMsgs: []Message{
				{Who: provider.WhoOwner, Text: "Plan a web page", At: at(0)},
				{Who: provider.WhoAgent, Text: "Where should the plan be shown?\nWhich sizes?", At: at(1), Questions: []provider.Question{scopeQuestion, sizeQuestion}},
			},
			wantState:   StateWaitingForChoice,
			wantPending: []provider.Question{scopeQuestion, sizeQuestion},
		},
		{
			name: "answered question",
			entries: []provider.ConversationEntry{
				question("q1", 1, scopeQuestion, sizeQuestion),
				answer("q1", 2,
					provider.Answer{Question: scopeQuestion.Question, Header: "Surface", Label: "A web page (recommended)"},
					provider.Answer{Question: "typed by hand", Label: "Small, Large"}),
				kind(provider.EntryTool, "", 3),
			},
			wantMsgs: []Message{
				{Who: provider.WhoAgent, Text: "Where should the plan be shown?\nWhich sizes?", At: at(1), Questions: []provider.Question{scopeQuestion, sizeQuestion}},
				{Who: provider.WhoOwner, Text: "Surface: A web page (recommended)\nSmall, Large", At: at(2)},
			},
			wantState:   StateWorking,
			wantPending: []provider.Question{},
		},
		{
			name: "answer to an older question does not settle a newer one",
			entries: []provider.ConversationEntry{
				question("q1", 1, scopeQuestion),
				answer("q1", 2, provider.Answer{Header: "Surface", Label: "In the terminal"}),
				question("q2", 3, sizeQuestion),
			},
			wantMsgs: []Message{
				{Who: provider.WhoAgent, Text: "Where should the plan be shown?", At: at(1), Questions: []provider.Question{scopeQuestion}},
				{Who: provider.WhoOwner, Text: "Surface: In the terminal", At: at(2)},
				{Who: provider.WhoAgent, Text: "Which sizes?", At: at(3), Questions: []provider.Question{sizeQuestion}},
			},
			wantState:   StateWaitingForChoice,
			wantPending: []provider.Question{sizeQuestion},
		},
		{
			name: "waiting for typed text",
			entries: []provider.ConversationEntry{
				text(provider.WhoOwner, "Plan a web page", 0),
				text(provider.WhoAgent, "What should it be called?", 1),
				kind(provider.EntryTurnEnd, "", 2),
			},
			wantMsgs: []Message{
				{Who: provider.WhoOwner, Text: "Plan a web page", At: at(0)},
				{Who: provider.WhoAgent, Text: "What should it be called?", At: at(1)},
			},
			wantState:   StateWaitingForText,
			wantPending: []provider.Question{},
		},
		{
			name: "still working",
			entries: []provider.ConversationEntry{
				text(provider.WhoOwner, "Plan a web page", 0),
				text(provider.WhoAgent, "Reading the code.", 1),
				{Kind: provider.EntryTool, Text: "Read", At: at(2)},
			},
			wantMsgs: []Message{
				{Who: provider.WhoOwner, Text: "Plan a web page", At: at(0)},
				{Who: provider.WhoAgent, Text: "Reading the code.", At: at(1)},
			},
			wantState:   StateWorking,
			wantPending: []provider.Question{},
		},
		{
			name: "background pending after a turn end",
			entries: []provider.ConversationEntry{
				text(provider.WhoAgent, "Started a researcher.", 1),
				kind(provider.EntryBackgroundStarted, "agent-1", 2),
				kind(provider.EntryBackgroundStarted, "bash-1", 3),
				kind(provider.EntryTurnEnd, "", 4),
				kind(provider.EntryBackgroundDone, "bash-1", 5),
			},
			wantMsgs: []Message{
				{Who: provider.WhoAgent, Text: "Started a researcher.", At: at(1)},
			},
			wantState:   StateWorking,
			wantPending: []provider.Question{},
		},
		{
			name: "background finished then turn end",
			entries: []provider.ConversationEntry{
				kind(provider.EntryBackgroundStarted, "agent-1", 1),
				kind(provider.EntryTurnEnd, "", 2),
				kind(provider.EntryBackgroundDone, "agent-1", 3),
				text(provider.WhoAgent, "The researcher is back.", 4),
				kind(provider.EntryTurnEnd, "", 5),
			},
			wantMsgs: []Message{
				{Who: provider.WhoAgent, Text: "The researcher is back.", At: at(4)},
			},
			wantState:   StateWaitingForText,
			wantPending: []provider.Question{},
		},
		{
			name: "background done seen before its start is not a later done",
			entries: []provider.ConversationEntry{
				kind(provider.EntryBackgroundDone, "agent-1", 1),
				kind(provider.EntryBackgroundStarted, "agent-1", 2),
				kind(provider.EntryTurnEnd, "", 3),
			},
			wantMsgs:    []Message{},
			wantState:   StateWorking,
			wantPending: []provider.Question{},
		},
		{
			name: "interrupted",
			entries: []provider.ConversationEntry{
				text(provider.WhoOwner, "Plan a web page", 0),
				{Kind: provider.EntryTool, Text: "Bash", At: at(1)},
				{Kind: provider.EntryInterrupted, Who: provider.WhoOwner, At: at(2)},
			},
			wantMsgs: []Message{
				{Who: provider.WhoOwner, Text: "Plan a web page", At: at(0)},
			},
			wantState:   StateWaitingForText,
			wantPending: []provider.Question{},
		},
		{
			name:        "no entries",
			entries:     nil,
			wantMsgs:    []Message{},
			wantState:   StateWorking,
			wantPending: []provider.Question{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs, state, pending := Build(tt.entries)
			if state != tt.wantState {
				t.Errorf("state = %q, want %q", state, tt.wantState)
			}
			if !reflect.DeepEqual(msgs, tt.wantMsgs) {
				t.Errorf("messages =\n%#v\nwant\n%#v", msgs, tt.wantMsgs)
			}
			if !reflect.DeepEqual(pending, tt.wantPending) {
				t.Errorf("pending =\n%#v\nwant\n%#v", pending, tt.wantPending)
			}
			if msgs == nil || pending == nil {
				t.Errorf("Build returned a nil slice (messages %v, pending %v); the API must encode [] not null", msgs == nil, pending == nil)
			}
			// DeepEqual already compares the flag; this names the
			// failure plainly when a row expects a recommended option.
			if hasRecommended(tt.wantPending) {
				var rec bool
				for _, q := range pending {
					for _, o := range q.Options {
						rec = rec || o.Recommended
					}
				}
				if !rec {
					t.Errorf("pending lost the recommended flag: %#v", pending)
				}
			}
		})
	}
}

func hasRecommended(qs []provider.Question) bool {
	for _, q := range qs {
		for _, o := range q.Options {
			if o.Recommended {
				return true
			}
		}
	}
	return false
}

// TestRead is the matrix for Read's outside checks. The load-bearing rows:
//   - "host not alive" and "no host": the conversation must not be read at
//     all (PLAN T8.3: no transcript read unless the planning host is alive),
//     so the fake fails the test if it is called.
//   - "no transcript fresh" against "no transcript stale": the only thing
//     telling a session that has not written its transcript yet from one
//     whose transcript afk cannot find; swap them and a new session shows a
//     scary problem, or a lost one shows "working" forever.
//   - "no planning session for the provider": an agent without planning
//     sessions is not broken, so it is "none", not a problem.
func TestRead(t *testing.T) {
	started := t0
	readErr := errors.New("permission denied")
	okEntries := []provider.ConversationEntry{
		text(provider.WhoOwner, "Plan a web page", 0),
		question("q1", 1, scopeQuestion),
	}
	tests := []struct {
		name        string
		host        HostInfo
		hasHost     bool
		entries     []provider.ConversationEntry
		err         error
		now         time.Time
		wantCalled  bool
		wantState   State
		wantProblem string
		wantMsgs    int
		wantQs      int
	}{
		{name: "no host", hasHost: false, wantState: StateNone},
		{name: "host not alive", hasHost: true, host: HostInfo{StartedAt: started, Alive: false}, wantState: StateNone},
		{name: "no planning session for the provider", hasHost: true, host: HostInfo{StartedAt: started, Provider: "other", Alive: true}, err: provider.ErrNoPlanSession, wantCalled: true, wantState: StateNone},
		{name: "no transcript fresh", hasHost: true, host: HostInfo{StartedAt: started, Alive: true}, err: fmt.Errorf("%w: nothing yet", provider.ErrNoTranscript), now: started.Add(2 * time.Minute), wantCalled: true, wantState: StateWorking},
		{name: "no transcript, host clock ahead", hasHost: true, host: HostInfo{StartedAt: started, Alive: true}, err: provider.ErrNoTranscript, now: started.Add(-time.Minute), wantCalled: true, wantState: StateWorking},
		{name: "no transcript stale", hasHost: true, host: HostInfo{StartedAt: started, Alive: true}, err: fmt.Errorf("%w: nothing", provider.ErrNoTranscript), now: started.Add(2*time.Minute + time.Second), wantCalled: true, wantState: StateUnknown, wantProblem: "The planning session is running, but its conversation has not started. It may be waiting at a question in its terminal, such as whether to trust a new folder; open it in afk dash or with afk attach and answer it. If it was started by hand or cleared, afk cannot show it here."},
		{name: "read error", hasHost: true, host: HostInfo{StartedAt: started, Alive: true}, err: readErr, now: started.Add(time.Hour), wantCalled: true, wantState: StateUnknown, wantProblem: "afk could not read the planning session's conversation: permission denied"},
		{name: "ok", hasHost: true, host: HostInfo{StartedAt: started, Provider: "p", Alive: true}, entries: okEntries, now: started.Add(time.Hour), wantCalled: true, wantState: StateWaitingForChoice, wantMsgs: 2, wantQs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			d := Deps{
				Host: func(root string) (HostInfo, bool) {
					if root != "/work/project" {
						t.Errorf("Host root = %q", root)
					}
					return tt.host, tt.hasHost
				},
				Conversation: func(name string, q provider.ConversationQuery) ([]provider.ConversationEntry, error) {
					called = true
					if !tt.wantCalled {
						t.Errorf("Conversation was called for a host that is missing or not alive")
					}
					want := provider.ConversationQuery{Home: "/home/owner", Root: "/work/project", StartedAt: tt.host.StartedAt}
					if q != want || name != tt.host.Provider {
						t.Errorf("Conversation(%q, %+v), want (%q, %+v)", name, q, tt.host.Provider, want)
					}
					return tt.entries, tt.err
				},
				Home: "/home/owner",
				Now:  func() time.Time { return tt.now },
			}
			chat := Read("demo", "/work/project", d)
			if called != tt.wantCalled {
				t.Errorf("Conversation called = %v, want %v", called, tt.wantCalled)
			}
			if chat.Project != "demo" {
				t.Errorf("project = %q, want demo", chat.Project)
			}
			if chat.State != tt.wantState {
				t.Errorf("state = %q, want %q", chat.State, tt.wantState)
			}
			if chat.Problem != tt.wantProblem {
				t.Errorf("problem = %q, want %q", chat.Problem, tt.wantProblem)
			}
			if tt.wantState == StateUnknown && strings.TrimSpace(chat.Problem) == "" {
				t.Errorf("unknown state with no plain problem")
			}
			if len(chat.Messages) != tt.wantMsgs || len(chat.Questions) != tt.wantQs {
				t.Errorf("got %d messages and %d questions, want %d and %d", len(chat.Messages), len(chat.Questions), tt.wantMsgs, tt.wantQs)
			}
			// Every state must encode its lists as [] so a page script
			// can iterate them without a null check.
			b, err := json.Marshal(chat)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "null") {
				t.Errorf("chat encodes a null: %s", b)
			}
		})
	}
}

// TestChatJSONKeys pins the API names T8.4, T8.12 and T8.13 read.
func TestChatJSONKeys(t *testing.T) {
	_, state, pending := Build([]provider.ConversationEntry{question("q1", 1, scopeQuestion)})
	chat := Chat{Project: "demo", State: state, Messages: []Message{{Who: provider.WhoAgent, Text: "hi", At: t0}}, Questions: pending}
	b, err := json.Marshal(chat)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"project":"demo"`, `"state":"waiting_for_choice"`, `"messages":[{"who":"agent","text":"hi","at":"2026-10-14T09:00:00Z"}]`, `"questions":[{"question":`, `"multi_select":false`, `"recommended":true`, `"problem":""`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON %s\nmissing %s", got, want)
		}
	}
}
