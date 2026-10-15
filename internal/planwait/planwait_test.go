package planwait

import (
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/provider"
)

var t0 = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

func textChat(n int) planchat.Chat {
	c := planchat.Chat{State: planchat.StateWaitingForText}
	for i := 0; i < n; i++ {
		who := "agent"
		if i%2 == 1 {
			who = "owner"
		}
		c.Messages = append(c.Messages, planchat.Message{Who: who, Text: "m" + string(rune('a'+i)), At: t0.Add(time.Duration(i) * time.Minute)})
	}
	return c
}

func choiceChat() planchat.Chat {
	return planchat.Chat{
		State:     planchat.StateWaitingForChoice,
		Messages:  []planchat.Message{{Who: "agent", Text: "q", At: t0}},
		Questions: []provider.Question{{Question: "Which database?\nmore"}},
	}
}

// TestDecide: the load-bearing rows are same_wait, answered_then_waiting_again,
// host_gone, unknown and working: they pin that a re-read wait never re-sends,
// that a new wait always does, and that a transient read failure keeps the record.
func TestDecide(t *testing.T) {
	text := textChat(1)
	answered := textChat(3)
	tests := []struct {
		name      string
		in        Input
		send      bool
		record    Action
		wantKey   bool
		click     string
		bodyStart string
	}{
		{"new_wait", Input{Project: "p", Chat: text, Link: "http://x/chat"}, true, Set, true, "http://x/chat", "ma"},
		{"new_wait_choice", Input{Project: "p", Chat: choiceChat(), Link: "http://x/chat"}, true, Set, true, "http://x/chat", "Which database?"},
		{"same_wait", Input{Project: "p", Chat: text, Recorded: Key(text), HasRecord: true}, false, Keep, false, "", ""},
		{"answered_then_waiting_again", Input{Project: "p", Chat: answered, Recorded: Key(text), HasRecord: true, Link: "l"}, true, Set, true, "l", "mc"},
		{"host_gone", Input{Project: "p", Chat: planchat.Chat{State: planchat.StateNone}, Recorded: "k", HasRecord: true}, false, Clear, false, "", ""},
		{"none_without_record", Input{Project: "p", Chat: planchat.Chat{State: planchat.StateNone}}, false, Keep, false, "", ""},
		{"no_link_settings", Input{Project: "p", Chat: text}, true, Set, true, "", "ma"},
		{"working_keeps_record", Input{Project: "p", Chat: planchat.Chat{State: planchat.StateWorking}, Recorded: "k", HasRecord: true}, false, Keep, false, "", ""},
		{"unknown_keeps_record", Input{Project: "p", Chat: planchat.Chat{State: planchat.StateUnknown}, Recorded: "k", HasRecord: true}, false, Keep, false, "", ""},
		{"stale_record_cleared_wait_resends", Input{Project: "p", Chat: text, Recorded: Key(text), HasRecord: false}, true, Set, true, "", "ma"},
		{"empty_text_uses_default_body", Input{Project: "p", Chat: planchat.Chat{State: planchat.StateWaitingForText}}, true, Set, true, "", "The planning session is waiting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.in)
			if d.Send != tt.send || d.Record != tt.record {
				t.Fatalf("Send=%v Record=%v, want %v %v", d.Send, d.Record, tt.send, tt.record)
			}
			if (d.Key != "") != tt.wantKey {
				t.Fatalf("Key = %q, wantKey %v", d.Key, tt.wantKey)
			}
			if !tt.send {
				if d.Message != (notify.NtfyMessage{}) {
					t.Fatalf("no-send decision carries a message: %+v", d.Message)
				}
				return
			}
			if d.Message.Title != "afk: p is waiting for you" || d.Message.Priority != 4 || d.Message.Click != tt.click {
				t.Fatalf("message = %+v", d.Message)
			}
			if !strings.HasPrefix(d.Message.Body, tt.bodyStart) || !strings.HasSuffix(d.Message.Body, "\n\nAnswer it in the chat on afk's web page, or in afk dash.") {
				t.Fatalf("body = %q", d.Message.Body)
			}
		})
	}
}

func TestKey(t *testing.T) {
	a, b := textChat(1), textChat(1)
	other := textChat(1)
	other.Messages[0].Text = "different"
	tests := []struct {
		name string
		x, y planchat.Chat
		same bool
	}{
		{"same_chat_twice", a, b, true},
		{"one_more_message", textChat(1), textChat(2), false},
		{"different_last_text", a, other, false},
		{"text_vs_choice_state", textChat(1), func() planchat.Chat { c := textChat(1); c.State = planchat.StateWaitingForChoice; return c }(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kx, ky := Key(tt.x), Key(tt.y)
			if kx == "" || (kx == ky) != tt.same {
				t.Fatalf("keys %q %q, same want %v", kx, ky, tt.same)
			}
		})
	}
	for _, s := range []planchat.State{planchat.StateNone, planchat.StateWorking, planchat.StateUnknown} {
		if k := Key(planchat.Chat{State: s}); k != "" {
			t.Errorf("Key(%s) = %q, want empty", s, k)
		}
	}
	if Key(planchat.Chat{State: planchat.StateWaitingForText}) == "" {
		t.Error("empty waiting chat must still have a key")
	}
}

func TestFirstLine(t *testing.T) {
	long := strings.Repeat("é", 250)
	tests := []struct {
		name string
		c    planchat.Chat
		want string
	}{
		{"leading_blank_lines", planchat.Chat{State: planchat.StateWaitingForText, Messages: []planchat.Message{{Who: "agent", Text: "\n  \n  hello \nsecond"}}}, "hello"},
		{"long_line_cut", planchat.Chat{State: planchat.StateWaitingForText, Messages: []planchat.Message{{Who: "agent", Text: long}}}, strings.Repeat("é", 200) + "…"},
		{"choice_uses_first_question", choiceChat(), "Which database?"},
		{"text_skips_owner_message", planchat.Chat{State: planchat.StateWaitingForText, Messages: []planchat.Message{{Who: "agent", Text: "agent says"}, {Who: "owner", Text: "owner says"}}}, "agent says"},
		{"choice_without_questions", planchat.Chat{State: planchat.StateWaitingForChoice}, ""},
		{"not_waiting", planchat.Chat{State: planchat.StateWorking}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstLine(tt.c); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
