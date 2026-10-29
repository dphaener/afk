package notify

// NoticeFacts are the plain values ChooseNotice decides from. The caller does
// the impure reading (host.json, Rex, tmux) so the whole matrix is
// table-testable without any of them.
type NoticeFacts struct {
	HasTmuxPane    bool
	HasRexWindow   bool
	MacConnected   bool
	WaitingOnOwner bool
}

// NoticeAction says whether to show afk's own toast and where a click goes.
type NoticeAction struct {
	Toast     bool
	FocusRex  bool
	FocusTmux bool
	Reason    string
}

// ChooseNotice picks the toast and the click target. First match wins.
//
// Rule 1 is the only one that removes a toast: when the agent is waiting on
// the owner and Rex for Mac is connected, Rex raises its own program
// notification for the same block, so a second toast would be a duplicate.
// Rule 2 sits above tmux because a Rex-backed session has its window in Rex,
// so a tmux focus would land on a pane that does not show the agent.
func ChooseNotice(f NoticeFacts) NoticeAction {
	switch {
	case f.WaitingOnOwner && f.HasRexWindow && f.MacConnected:
		return NoticeAction{
			FocusRex: true,
			Reason:   "Rex for Mac is connected and already shows that this agent is waiting for you, so afk skips its own notice.",
		}
	case f.HasRexWindow:
		return NoticeAction{
			Toast:    true,
			FocusRex: true,
			Reason:   "The agent runs in Rex, so clicking the notice shows its Rex window.",
		}
	case f.HasTmuxPane:
		return NoticeAction{
			Toast:     true,
			FocusTmux: true,
			Reason:    "The agent has a tmux pane, so clicking the notice switches to it.",
		}
	default:
		return NoticeAction{
			Toast:  true,
			Reason: "There is no window to show, so the notice has no click target.",
		}
	}
}
