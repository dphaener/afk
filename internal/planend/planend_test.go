package planend

import (
	"testing"

	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/plansession"
)

// TestDecide covers every input of the decision. The load-bearing rows are
// the unfinished session (ending it would cut off a conversation in
// progress), the missing or blank summary (the summary is what survives the
// end), and the agent still replying (the chat "working": ending it would
// lose a turn). Each of the six conditions has a row that fails alone.
func TestDecide(t *testing.T) {
	st := func(mode, phase string) plansession.State {
		return plansession.State{Mode: mode, Phase: phase}
	}
	good := func() Input {
		return Input{
			HasState:  true,
			State:     st(plansession.ModeExtend, plansession.PhaseReview),
			Summary:   "Added two tasks.",
			Chat:      planchat.StateWaitingForText,
			HostAlive: true,
		}
	}
	tests := []struct {
		name   string
		mut    func(*Input)
		end    bool
		reason string
	}{
		{"extend waiting for text", func(*Input) {}, true, "the change session is finished, its summary is saved and its agent is waiting for you"},
		{"amend waiting for text", func(i *Input) { i.State = st(plansession.ModeAmend, plansession.PhaseReview) }, true, ""},
		{"reconcile waiting for text", func(i *Input) { i.State = st(plansession.ModeReconcile, plansession.PhaseReview) }, true, ""},
		{"extend waiting for choice", func(i *Input) { i.Chat = planchat.StateWaitingForChoice }, true, ""},
		{"no state", func(i *Input) { i.HasState = false; i.State = plansession.State{} }, false, "there is no planning session state"},
		{"new at review", func(i *Input) { i.State = st(plansession.ModeNew, plansession.PhaseReview) }, false, "a new session is ended by afk plan approve"},
		{"new at approved", func(i *Input) { i.State = st(plansession.ModeNew, plansession.PhaseApproved) }, false, "a new session is ended by afk plan approve"},
		{"unknown mode", func(i *Input) { i.State = st("bogus", plansession.PhaseReview) }, false, "the session is not a change session"},
		{"extend at check", func(i *Input) { i.State = st(plansession.ModeExtend, plansession.PhaseCheck) }, false, "the session is not finished"},
		{"extend at draft", func(i *Input) { i.State = st(plansession.ModeExtend, plansession.PhaseDraft) }, false, "the session is not finished"},
		{"summary empty", func(i *Input) { i.Summary = "" }, false, "its summary is not saved"},
		{"summary blank", func(i *Input) { i.Summary = " \n\t " }, false, "its summary is not saved"},
		{"chat none", func(i *Input) { i.Chat = planchat.StateNone }, false, "its agent is still replying"},
		{"chat working", func(i *Input) { i.Chat = planchat.StateWorking }, false, "its agent is still replying"},
		{"chat unknown", func(i *Input) { i.Chat = planchat.StateUnknown }, false, "its agent is still replying"},
		{"host not alive", func(i *Input) { i.HostAlive = false }, false, "its agent is not running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := good()
			tt.mut(&in)
			got := Decide(in)
			if got.End != tt.end {
				t.Fatalf("End = %v, want %v (reason %q)", got.End, tt.end, got.Reason)
			}
			if got.Reason == "" {
				t.Fatalf("Reason is empty")
			}
			if tt.reason != "" && got.Reason != tt.reason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tt.reason)
			}
		})
	}
}

func TestRuleNames(t *testing.T) {
	if Rule == "" {
		t.Fatal("Rule is empty")
	}
}
