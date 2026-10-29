package rex

import "testing"

// TestGoIn: the afk-runtime row and the not-inside-Rex row are load-bearing.
// The first keeps afk-hosted agents on the Agent level; the second is the
// InsideRex gate, needed because focus_window is session-wide (T17.1
// amendment) and would move the wrong client if fired from outside Rex. The
// other-session row pins the same reason from the owner's side.
func TestGoIn(t *testing.T) {
	tests := []struct {
		name string
		f    GoInFacts
		want string
	}{
		{"afk runtime", GoInFacts{Runtime: "afk", InsideRex: true, OwnerClientOnAgentSession: true, WindowID: "w"}, GoInAfkAgent},
		{"empty runtime is afk", GoInFacts{}, GoInAfkAgent},
		{"rex outside rex", GoInFacts{Runtime: "rex", OwnerClientOnAgentSession: true, WindowID: "w"}, GoInBandText},
		{"rex inside, owner on other session", GoInFacts{Runtime: "rex", InsideRex: true, WindowID: "w"}, GoInBandText},
		{"rex inside, owner attached, no window", GoInFacts{Runtime: "rex", InsideRex: true, OwnerClientOnAgentSession: true}, GoInBandText},
		{"rex inside, owner attached, window", GoInFacts{Runtime: "rex", InsideRex: true, OwnerClientOnAgentSession: true, WindowID: "w"}, GoInFocusWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GoIn(tt.f)
			if got.Kind != tt.want {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.want)
			}
			if got.Reason == "" {
				t.Fatal("Reason is empty")
			}
		})
	}
}

// TestOwnerClient: the macos-over-ios row and the state/kind filters are
// load-bearing, since a stale or non-app client must never be chosen as the
// owner's.
func TestOwnerClient(t *testing.T) {
	c := func(id, kind, plat, at, state string) Client {
		return Client{ID: id, Kind: kind, Platform: plat, ConnectedAt: at, ConnectionState: state}
	}
	tests := []struct {
		name   string
		in     []Client
		wantID string
		wantOK bool
	}{
		{"empty", nil, "", false},
		{"non-app ignored", []Client{c("a", "cli", "macos", "2026-10-28T10:00:00Z", "connected")}, "", false},
		{"disconnected ignored", []Client{c("a", "app", "macos", "2026-10-28T10:00:00Z", "disconnected")}, "", false},
		{"macos over newer ios", []Client{c("ios", "app", "ios", "2026-10-28T12:00:00Z", "connected"), c("mac", "app", "macos", "2026-10-28T10:00:00Z", "connected")}, "mac", true},
		{"newest macos wins", []Client{c("old", "app", "macos", "2026-10-28T10:00:00Z", "connected"), c("new", "app", "macos", "2026-10-28T11:00:00Z", "connected")}, "new", true},
		{"time compare across offsets", []Client{c("a", "app", "macos", "2026-10-28T10:00:00Z", "connected"), c("b", "app", "macos", "2026-10-28T10:30:00+02:00", "connected")}, "a", true},
		{"only ios", []Client{c("ios", "app", "ios", "2026-10-28T10:00:00Z", "connected")}, "ios", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := OwnerClient(tt.in)
			if ok != tt.wantOK || got.ID != tt.wantID {
				t.Fatalf("got (%q, %v), want (%q, %v)", got.ID, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}
