package planchat

import (
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/provider"
)

func qs() []provider.Question {
	return []provider.Question{
		{Question: "Which?", MultiSelect: false, Options: []provider.Option{{Label: "A"}, {Label: "B (Recommended)", Recommended: true}}},
		{Question: "Which many?", MultiSelect: true, Options: []provider.Option{{Label: "X"}, {Label: "Y"}}},
	}
}

// TestCheck: every refusal branch and the accepting rows. Load-bearing: the
// not_offered row (a label outside the offered ones must never reach the
// terminal), the recommended-label row (labels match exactly, so the
// "(Recommended)" suffix as the agent wrote it is accepted and "B" is not),
// and the both/neither rows, which come before any state check.
func TestCheck(t *testing.T) {
	choice := Chat{State: StateWaitingForChoice, Questions: qs()}
	text := Chat{State: StateWaitingForText, Questions: []provider.Question{}}
	cases := []struct {
		name string
		chat Chat
		r    Reply
		want RefusalReason // "" means accepted
	}{
		{"neither", text, Reply{}, ReasonBadReply},
		{"both", text, Reply{Text: "hi", Choices: [][]string{{"A"}}}, ReasonBadReply},
		{"none text", Chat{State: StateNone}, Reply{Text: "hi"}, ReasonNoSession},
		{"none choice", Chat{State: StateNone}, Reply{Choices: [][]string{{"A"}}}, ReasonNoSession},
		{"unknown text", Chat{State: StateUnknown, Problem: "broken."}, Reply{Text: "hi"}, ReasonNotWaiting},
		{"unknown choice", Chat{State: StateUnknown}, Reply{Choices: [][]string{{"A"}}}, ReasonNotWaiting},
		{"working text", Chat{State: StateWorking}, Reply{Text: "hi"}, ReasonNotWaiting},
		{"working choice", Chat{State: StateWorking}, Reply{Choices: [][]string{{"A"}}}, ReasonNotWaiting},
		{"unlisted state", Chat{State: State("odd")}, Reply{Text: "hi"}, ReasonNotWaiting},
		{"text to choice", choice, Reply{Text: "hi"}, ReasonWrongKind},
		{"choice to text", text, Reply{Choices: [][]string{{"A"}}}, ReasonWrongKind},
		{"text ok", text, Reply{Text: "hi"}, ""},
		{"choice ok", choice, Reply{Choices: [][]string{{"B (Recommended)"}, {"X", "Y"}}}, ""},
		{"count too few", choice, Reply{Choices: [][]string{{"A"}}}, ReasonBadReply},
		{"count too many", choice, Reply{Choices: [][]string{{"A"}, {"X"}, {"X"}}}, ReasonBadReply},
		{"single zero labels", choice, Reply{Choices: [][]string{{}, {"X"}}}, ReasonBadReply},
		{"single two labels", choice, Reply{Choices: [][]string{{"A", "B (Recommended)"}, {"X"}}}, ReasonBadReply},
		{"multi empty", choice, Reply{Choices: [][]string{{"A"}, {}}}, ReasonBadReply},
		{"duplicate", choice, Reply{Choices: [][]string{{"A"}, {"X", "X"}}}, ReasonBadReply},
		{"not offered single", choice, Reply{Choices: [][]string{{"Z"}, {"X"}}}, ReasonNotOffered},
		{"not offered multi", choice, Reply{Choices: [][]string{{"A"}, {"X", "Z"}}}, ReasonNotOffered},
		{"recommended suffix dropped", choice, Reply{Choices: [][]string{{"B"}, {"X"}}}, ReasonNotOffered},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Check(c.chat, c.r)
			if c.want == "" {
				if got != nil {
					t.Fatalf("refused: %v", got)
				}
				return
			}
			if got == nil || got.Reason != c.want {
				t.Fatalf("got %v, want reason %s", got, c.want)
			}
			if got.Message == "" || got.Error() != got.Message {
				t.Fatalf("bad message %q", got.Message)
			}
		})
	}
}

// TestCheckUnknownCarriesProblem: the unknown-state message must include the
// problem sentence and point at afk dash, since that is the owner's way out.
func TestCheckUnknownCarriesProblem(t *testing.T) {
	got := Check(Chat{State: StateUnknown, Problem: "Transcript lost."}, Reply{Text: "x"})
	if got == nil || !strings.Contains(got.Message, "Transcript lost.") || !strings.Contains(got.Message, "afk dash") {
		t.Fatalf("got %v", got)
	}
}

// TestTextKeys: load-bearing rows are ESC and 0x7f (an ESC would end the
// bracketed paste early), the line-ending rows (one message, LF only), tab
// kept, and "spaces only" refused while surrounding spaces are not trimmed.
func TestTextKeys(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // pasted body; "" means refused
	}{
		{"one line", "hello", "hello"},
		{"multi line", "a\nb", "a\nb"},
		{"crlf", "a\r\nb", "a\nb"},
		{"lone cr", "a\rb", "a\nb"},
		{"tab kept", "a\tb", "a\tb"},
		{"spaces kept", "  a  ", "  a  "},
		{"empty", "", ""},
		{"spaces only", "  \n\t ", ""},
		{"esc", "a\x1bb", ""},
		{"del", "a\x7fb", ""},
		{"nul", "a\x00b", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keys, ref := TextKeys(c.in)
			if c.want == "" {
				if ref == nil || ref.Reason != ReasonBadReply || keys != nil {
					t.Fatalf("got %q %v, want refusal", keys, ref)
				}
				return
			}
			if ref != nil {
				t.Fatalf("refused: %v", ref)
			}
			if len(keys) != 2 || string(keys[0]) != "\x1b[200~"+c.want+"\x1b[201~" || string(keys[1]) != "\r" {
				t.Fatalf("got %q", keys)
			}
		})
	}
}
