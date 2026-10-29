package notify

import "github.com/dphaener/afk/internal/dashmeta"

// Target is where a click on a toast should take the owner. PaneID, ClientTTY
// and TerminalApp are the tmux route. RexSession and RexWindow are the Rex route:
// the parent sets them (from the session's host.json rex block, never from
// dashmeta, which records only the dashboard's tmux pane) only when ChooseNotice
// picked Rex, so a non-empty pair is itself the instruction to try Rex first.
// ResolveTarget leaves them empty, so this package needs nothing but dashmeta.
type Target struct {
	PaneID      string
	ClientTTY   string
	TerminalApp string
	RexSession  string
	RexWindow   string
}

// ResolveTarget extracts focus metadata from a dashmeta entry. PaneID and
// ClientTTY come from the entry when found is true, never from TMUX_PANE env
// (which names the launch pane, not the session's dashboard pane). TerminalApp
// comes from AFK_TERMINAL_APP if set, else empty. Empty TerminalApp means
// "switch the pane but skip activation". There is no process-tree walk because
// the tmux server daemonizes (verified ppid 1 in W0.T1), so walking from the
// server pid reaches launchd and matches nothing. Only a walk from a live
// client pid could work, and that adds a blocking tmux query for a cosmetic
// gain, so the env override is the whole mechanism.
func ResolveTarget(entry dashmeta.Entry, found bool, getenv func(string) string) Target {
	var t Target
	if found {
		t.PaneID = entry.PaneID
		t.ClientTTY = entry.ClientTTY
	}
	t.TerminalApp = getenv("AFK_TERMINAL_APP")
	return t
}
