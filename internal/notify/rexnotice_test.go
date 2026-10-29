package notify

import "testing"

// TestChooseNotice pins the notice matrix. Load-bearing rows: the
// "rex_waiting_mac" row is the only one that removes a toast (Rex's own
// notification already says the same), and every "both_*" row pins rule 2
// over rule 3, the precedence a click depends on: a Rex window must win over
// a tmux pane or the click lands on a pane that does not show the agent.
func TestChooseNotice(t *testing.T) {
	tests := []struct {
		name  string
		facts NoticeFacts
		want  NoticeAction
	}{
		{"tmux_mac_off", NoticeFacts{HasTmuxPane: true}, NoticeAction{Toast: true, FocusTmux: true}},
		{"tmux_mac_on", NoticeFacts{HasTmuxPane: true, MacConnected: true}, NoticeAction{Toast: true, FocusTmux: true}},
		{"tmux_waiting_mac_off", NoticeFacts{HasTmuxPane: true, WaitingOnOwner: true}, NoticeAction{Toast: true, FocusTmux: true}},
		{"tmux_waiting_mac_on", NoticeFacts{HasTmuxPane: true, MacConnected: true, WaitingOnOwner: true}, NoticeAction{Toast: true, FocusTmux: true}},
		{"rex_mac_off", NoticeFacts{HasRexWindow: true}, NoticeAction{Toast: true, FocusRex: true}},
		{"rex_mac_on", NoticeFacts{HasRexWindow: true, MacConnected: true}, NoticeAction{Toast: true, FocusRex: true}},
		{"rex_waiting_mac_off", NoticeFacts{HasRexWindow: true, WaitingOnOwner: true}, NoticeAction{Toast: true, FocusRex: true}},
		{"rex_waiting_mac_on", NoticeFacts{HasRexWindow: true, MacConnected: true, WaitingOnOwner: true}, NoticeAction{FocusRex: true}},
		{"both_mac_off", NoticeFacts{HasTmuxPane: true, HasRexWindow: true}, NoticeAction{Toast: true, FocusRex: true}},
		{"both_mac_on", NoticeFacts{HasTmuxPane: true, HasRexWindow: true, MacConnected: true}, NoticeAction{Toast: true, FocusRex: true}},
		{"both_waiting_mac_off", NoticeFacts{HasTmuxPane: true, HasRexWindow: true, WaitingOnOwner: true}, NoticeAction{Toast: true, FocusRex: true}},
		{"both_waiting_mac_on", NoticeFacts{HasTmuxPane: true, HasRexWindow: true, MacConnected: true, WaitingOnOwner: true}, NoticeAction{FocusRex: true}},
		{"neither_mac_off", NoticeFacts{}, NoticeAction{Toast: true}},
		{"neither_mac_on", NoticeFacts{MacConnected: true}, NoticeAction{Toast: true}},
		{"neither_waiting_mac_off", NoticeFacts{WaitingOnOwner: true}, NoticeAction{Toast: true}},
		{"neither_waiting_mac_on", NoticeFacts{MacConnected: true, WaitingOnOwner: true}, NoticeAction{Toast: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChooseNotice(tt.facts)
			if got.Reason == "" {
				t.Errorf("Reason is empty")
			}
			got.Reason = ""
			if got != tt.want {
				t.Errorf("ChooseNotice(%+v) = %+v, want %+v", tt.facts, got, tt.want)
			}
		})
	}
}
