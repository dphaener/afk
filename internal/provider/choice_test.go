package provider

import (
	"bytes"
	"errors"
	"testing"
)

func opts(labels ...string) []Option {
	var o []Option
	for _, l := range labels {
		o = append(o, Option{Label: l})
	}
	return o
}

// TestPlanChoiceKeys pins the exact bytes against the recorded trial
// (.afk/t8-4/keystrokes.md). Load-bearing rows: first/middle/last pin the
// digit-to-option mapping (a shifted index answers the wrong question);
// "single-select alone" has no submit key because a stray Enter would reach
// the main prompt; multi-question and multi-select rows end with '1' for the
// review screen and Tab after each multi-select; the refusal rows keep a
// stale or forged reply from sending a digit that does something else (a
// digit above the option count is the "Type something." row). codex and
// opencode must refuse like their PlanConversation.
func TestPlanChoiceKeys(t *testing.T) {
	single := Question{Options: opts("A", "B", "C")}
	rec := Question{Options: []Option{{Label: "A"}, {Label: "B (Recommended)", Recommended: true}}}
	multi := Question{MultiSelect: true, Options: opts("X", "Y", "Z")}
	k := func(s ...string) [][]byte {
		var out [][]byte
		for _, e := range s {
			out = append(out, []byte(e))
		}
		return out
	}
	tests := []struct {
		name     string
		provider Provider
		qs       []Question
		choices  [][]string
		want     [][]byte
		wantErr  error
		anyErr   bool
	}{
		{name: "first option", provider: claude{}, qs: []Question{single}, choices: [][]string{{"A"}}, want: k("1")},
		{name: "middle option", provider: claude{}, qs: []Question{single}, choices: [][]string{{"B"}}, want: k("2")},
		{name: "last option", provider: claude{}, qs: []Question{single}, choices: [][]string{{"C"}}, want: k("3")},
		{name: "recommended label", provider: claude{}, qs: []Question{rec}, choices: [][]string{{"B (Recommended)"}}, want: k("2")},
		{name: "two single-selects", provider: claude{}, qs: []Question{single, single}, choices: [][]string{{"C"}, {"B"}}, want: k("3", "2", "1")},
		{name: "multi-select alone", provider: claude{}, qs: []Question{multi}, choices: [][]string{{"X", "Z"}}, want: k("1", "3", "\t", "1")},
		{name: "multi-select then single", provider: claude{}, qs: []Question{multi, single}, choices: [][]string{{"Y", "Z"}, {"B"}}, want: k("2", "3", "\t", "2", "1")},
		{name: "label not offered", provider: claude{}, qs: []Question{single}, choices: [][]string{{"Q"}}, anyErr: true},
		{name: "count mismatch", provider: claude{}, qs: []Question{single, single}, choices: [][]string{{"A"}}, anyErr: true},
		{name: "two labels for single-select", provider: claude{}, qs: []Question{single}, choices: [][]string{{"A", "B"}}, anyErr: true},
		{name: "same label twice", provider: claude{}, qs: []Question{multi}, choices: [][]string{{"X", "X"}}, anyErr: true},
		{name: "empty choice", provider: claude{}, qs: []Question{single}, choices: [][]string{{}}, anyErr: true},
		{name: "no questions", provider: claude{}, anyErr: true},
		{name: "more options than the trial covered", provider: claude{}, qs: []Question{{Options: opts("A", "B", "C", "D", "E")}}, choices: [][]string{{"A"}}, wantErr: ErrChoiceUnsupported},
		{name: "codex", provider: codex{}, qs: []Question{single}, choices: [][]string{{"A"}}, wantErr: ErrNoPlanSession},
		{name: "opencode", provider: opencode{}, qs: []Question{single}, choices: [][]string{{"A"}}, wantErr: ErrNoPlanSession},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.provider.PlanChoiceKeys(tt.qs, tt.choices)
			if tt.wantErr != nil || tt.anyErr {
				if err == nil || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if got != nil {
					t.Errorf("keys = %q, want none with an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("keys = %q, want %q", got, tt.want)
			}
			for i := range got {
				if !bytes.Equal(got[i], tt.want[i]) {
					t.Fatalf("keys = %q, want %q", got, tt.want)
				}
			}
		})
	}
}
