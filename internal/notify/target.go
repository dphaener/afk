package notify

import "github.com/dphaener/afk/internal/dashmeta"

type Target struct {
	PaneID      string
	ClientTTY   string
	TerminalApp string
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
