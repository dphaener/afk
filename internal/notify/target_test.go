package notify

import (
	"testing"

	"github.com/dphaener/afk/internal/dashmeta"
)

// TestResolveTarget covers the decision matrix for focus target resolution.
// Load-bearing rows: entry found (PaneID and ClientTTY populated from it);
// entry not found (PaneID and ClientTTY empty); AFK_TERMINAL_APP set vs unset
// (the unset row with PaneID populated proves pane-switching without activation).
func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name    string
		entry   dashmeta.Entry
		found   bool
		envVars map[string]string
		want    Target
	}{
		{
			name: "entry found, no terminal app",
			entry: dashmeta.Entry{
				PID:       12345,
				Tmux:      "/tmp/tmux-501/default,67890,0",
				PaneID:    "%42",
				ClientTTY: "/dev/ttys003",
				StartedAt: "2026-08-05T10:00:00Z",
			},
			found:   true,
			envVars: map[string]string{},
			want: Target{
				PaneID:      "%42",
				ClientTTY:   "/dev/ttys003",
				TerminalApp: "",
			},
		},
		{
			name: "entry found, terminal app set",
			entry: dashmeta.Entry{
				PID:       12345,
				Tmux:      "/tmp/tmux-501/default,67890,0",
				PaneID:    "%42",
				ClientTTY: "/dev/ttys003",
				StartedAt: "2026-08-05T10:00:00Z",
			},
			found: true,
			envVars: map[string]string{
				"AFK_TERMINAL_APP": "com.mitchellh.ghostty",
			},
			want: Target{
				PaneID:      "%42",
				ClientTTY:   "/dev/ttys003",
				TerminalApp: "com.mitchellh.ghostty",
			},
		},
		{
			name:    "entry not found",
			entry:   dashmeta.Entry{},
			found:   false,
			envVars: map[string]string{},
			want: Target{
				PaneID:      "",
				ClientTTY:   "",
				TerminalApp: "",
			},
		},
		{
			name:  "entry not found, terminal app set",
			entry: dashmeta.Entry{},
			found: false,
			envVars: map[string]string{
				"AFK_TERMINAL_APP": "com.mitchellh.ghostty",
			},
			want: Target{
				PaneID:      "",
				ClientTTY:   "",
				TerminalApp: "com.mitchellh.ghostty",
			},
		},
		{
			name: "pane without activation is valid",
			entry: dashmeta.Entry{
				PID:       12345,
				Tmux:      "/tmp/tmux-501/default,67890,0",
				PaneID:    "%50",
				ClientTTY: "/dev/ttys000",
				StartedAt: "2026-08-05T11:00:00Z",
			},
			found:   true,
			envVars: map[string]string{},
			want: Target{
				PaneID:      "%50",
				ClientTTY:   "/dev/ttys000",
				TerminalApp: "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string {
				return tt.envVars[key]
			}
			got := ResolveTarget(tt.entry, tt.found, getenv)
			if got != tt.want {
				t.Errorf("ResolveTarget() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
