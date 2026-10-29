package rex

import (
	"context"
	"encoding/json"
	"testing"
)

// TestListClients: the unknown-field row is load-bearing because Rex adds
// fields; the no-kinds row pins that the kinds key is left out, not null.
func TestListClients(t *testing.T) {
	reply := `{"clients":[{"client_id":"c1","connected_at":"2026-10-28T10:00:00Z","connection_state":"connected",
	  "info":{"kind":"app","platform":"macos","label":"studio","name":"Rex.app","version":"1.6.0","future":true},
	  "principal":{"kind":"peer","transport":"unix","display":"uid 501"},"session_ids":["session:a"],"brand_new":{"x":1}}]}`
	t.Run("unknown fields decode", func(t *testing.T) {
		f := (&Fake{}).OnArgs("client.list", map[string]any{"kinds": []string{"app"}}, reply, nil)
		got, err := ListClients(context.Background(), f, "app")
		if err != nil || len(got) != 1 {
			t.Fatalf("got (%v, %v)", got, err)
		}
		c := got[0]
		if c.ID != "c1" || c.Kind != "app" || c.Platform != "macos" || c.Label != "studio" ||
			c.ConnectedAt != "2026-10-28T10:00:00Z" || c.ConnectionState != "connected" || len(c.SessionIDs) != 1 {
			t.Errorf("client = %+v", c)
		}
	})
	t.Run("no kinds omits the key", func(t *testing.T) {
		f := (&Fake{}).On("client.list", `{"clients":null}`, nil)
		got, err := ListClients(context.Background(), f)
		if err != nil || len(got) != 0 {
			t.Fatalf("got (%v, %v)", got, err)
		}
		if a := string(f.Calls()[0].Args); a != `{}` {
			t.Errorf("args = %s, want {}", a)
		}
	})
	t.Run("not json", func(t *testing.T) {
		f := (&Fake{}).On("client.list", `x`, nil)
		if _, err := ListClients(context.Background(), f); err == nil {
			t.Error("want an error")
		}
	})
}

// TestFocusWindow: the empty rows are load-bearing: an empty window id would
// ask Rex to clear the active window, so no call may be made at all.
func TestFocusWindow(t *testing.T) {
	t.Run("sends both ids and ignores revision reply", func(t *testing.T) {
		f := (&Fake{}).On("session.focus_window", `{"revision":42}`, nil)
		if err := FocusWindow(context.Background(), f, "session:a", "window:b"); err != nil {
			t.Fatal(err)
		}
		calls := f.Calls()
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		var got map[string]string
		if err := json.Unmarshal(calls[0].Args, &got); err != nil {
			t.Fatal(err)
		}
		if got["session_id"] != "session:a" || got["window_id"] != "window:b" || len(got) != 2 {
			t.Errorf("args = %v", got)
		}
	})
	for _, tt := range []struct{ name, s, w string }{
		{"empty window", "session:a", ""},
		{"empty session", "", "window:b"},
		{"empty both", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &Fake{}
			if err := FocusWindow(context.Background(), f, tt.s, tt.w); err == nil {
				t.Error("want a refusal")
			}
			if n := len(f.Calls()); n != 0 {
				t.Errorf("calls = %d, want 0", n)
			}
		})
	}
}
