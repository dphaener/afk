package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestCheckLeader walks every way a [dash] leader value can be right or
// wrong. Load-bearing rows: "ctrl+m", "alt+space" and "ctrl+shift+a" prove the
// accepted key round-trips through tea.KeyPressMsg.String (what key.Matches
// sees); "shift+ctrl+a" and "shift+a" are keys that parse but never arrive
// under that name; the bare letters and f5 are the no-modifier rule; enter,
// esc, k and the rest are the keys the dashboard keeps for itself. Every
// refusal must fall back to ctrl+] and quote the value, because the header line
// is built from the reason.
func TestCheckLeader(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		want       string // accepted key name; "" means refused
		wantReason string // substring of the reason, refusals only
	}{
		{"unset", "", defaultLeaderKey, ""},
		{"white space only", "   ", defaultLeaderKey, ""},
		{"default written out", "ctrl+]", "ctrl+]", ""},
		{"ctrl+m", "ctrl+m", "ctrl+m", ""},
		{"alt+space", "alt+space", "alt+space", ""},
		{"ctrl+o", "ctrl+o", "ctrl+o", ""},
		{"trimmed", "  ctrl+o\t", "ctrl+o", ""},
		{"upper case is lower-cased", "CTRL+O", "ctrl+o", ""},
		{"ctrl+shift+a", "ctrl+shift+a", "ctrl+shift+a", ""},
		{"alt+f5", "alt+f5", "alt+f5", ""},
		{"ctrl+f63", "ctrl+f63", "ctrl+f63", ""},
		{"alt+backspace", "alt+backspace", "alt+backspace", ""},
		{"shift+f5", "shift+f5", "shift+f5", ""},
		{"wrong modifier order", "shift+ctrl+a", "", "ever see"},
		{"repeated modifier", "ctrl+ctrl+a", "", "ever see"},
		{"unknown modifier", "hyperdrive+x", "", "ever see"},
		{"unknown key name", "ctrl+foo", "", "ever see"},
		{"trailing plus", "ctrl+", "", "ever see"},
		{"media key", "ctrl+mute", "", "ever see"},
		{"plain letter", "q", "", "ctrl or alt"},
		{"plain digit", "5", "", "ctrl or alt"},
		{"plain symbol", "?", "", "ctrl or alt"},
		{"function key, no modifier", "f5", "", "ctrl or alt"},
		{"shift with a letter", "shift+a", "", "ever see"},
		{"enter", "enter", "", "already uses"},
		{"esc", "esc", "", "already uses"},
		{"tab", "tab", "", "already uses"},
		{"shift+tab", "shift+tab", "", "already uses"},
		{"ctrl+c", "ctrl+c", "", "already uses"},
		{"up", "up", "", "already uses"},
		{"pgdown", "pgdown", "", "already uses"},
		{"home", "home", "", "already uses"},
		{"ctrl+u", "ctrl+u", "", "already uses"},
		{"ctrl+d", "ctrl+d", "", "already uses"},
		{"k", "k", "", "already uses"},
		{"j", "j", "", "already uses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkLeader(tt.in)
			if tt.want == "" {
				if got.name != defaultLeaderKey || got.key != (tea.Key{Code: ']', Mod: tea.ModCtrl}) {
					t.Fatalf("refusal must fall back to %s, got %q %+v", defaultLeaderKey, got.name, got.key)
				}
				if got.refused == "" {
					t.Fatal("refusal carries no reason")
				}
				if !strings.Contains(got.refused, strings.TrimSpace(tt.in)) {
					t.Errorf("reason %q does not mention the value %q", got.refused, tt.in)
				}
				if !strings.Contains(got.refused, tt.wantReason) {
					t.Errorf("reason %q lacks %q", got.refused, tt.wantReason)
				}
				return
			}
			if got.refused != "" {
				t.Fatalf("refused %q: %s", tt.in, got.refused)
			}
			if got.name != tt.want {
				t.Fatalf("name = %q, want %q", got.name, tt.want)
			}
			if s := tea.KeyPressMsg(got.key).String(); s != got.name {
				t.Errorf("round trip: KeyPressMsg(%+v).String() = %q, want %q", got.key, s, got.name)
			}
			if got.configured != strings.TrimSpace(tt.in) {
				t.Errorf("configured = %q, want the trimmed input", got.configured)
			}
		})
	}
}
