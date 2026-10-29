package rex

import (
	"context"
	"errors"
	"testing"
)

// TestFindSession: the "missing" row is load-bearing (found=false with no
// error, so the caller can create), and the call-error row proves a failure is
// not swallowed into "not found". Duplicates pick the oldest because labels
// are not unique in Rex.
func TestFindSession(t *testing.T) {
	list := `{"sessions":[{"session_id":"session:a","label":"work","extra":1},{"session_id":"session:b","label":"afk"},{"session_id":"session:c","label":"afk"}]}`
	boom := &CallError{Method: "session.list", Code: 3, Stderr: "session_not_found"}
	tests := []struct {
		name      string
		reply     string
		err       error
		label     string
		wantID    string
		wantFound bool
		wantErr   bool
	}{
		{"exists", list, nil, "work", "session:a", true, false},
		{"missing", list, nil, "nope", "", false, false},
		{"duplicate label picks oldest", list, nil, "afk", "session:b", true, false},
		{"empty list", `{"sessions":null}`, nil, "work", "", false, false},
		{"session_not_found propagates", "", boom, "work", "", false, true},
		{"not json", `nope`, nil, "work", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Fake{}).On("session.list", tt.reply, tt.err)
			s, found, err := FindSession(context.Background(), f, tt.label)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Errorf("err = %v, want it to wrap %v", err, tt.err)
			}
			if found != tt.wantFound || s.ID != tt.wantID {
				t.Errorf("got (%+v, %v), want id %q found %v", s, found, tt.wantID, tt.wantFound)
			}
		})
	}
}

// TestCreateSession: the label goes through as sent and a reply without an id
// is an error rather than an empty Session.
func TestCreateSession(t *testing.T) {
	f := (&Fake{}).OnArgs("session.create", map[string]any{"label": "afk"}, `{"session_id":"session:x"}`, nil)
	s, err := CreateSession(context.Background(), f, "afk")
	if err != nil || s.ID != "session:x" || s.Label != "afk" {
		t.Fatalf("got (%+v, %v)", s, err)
	}
	f = (&Fake{}).On("session.create", `{}`, nil)
	if _, err := CreateSession(context.Background(), f, "afk"); err == nil {
		t.Error("reply without session_id: want an error")
	}
}

// TestSetLabel: both ids reach the server, and an empty session id is refused
// before any call.
func TestSetLabel(t *testing.T) {
	f := (&Fake{}).OnArgs("session.set_label", map[string]any{"session_id": "session:x", "label": "new"}, `{"revision":2}`, nil)
	if err := SetLabel(context.Background(), f, "session:x", "new"); err != nil {
		t.Fatal(err)
	}
	g := &Fake{}
	if err := SetLabel(context.Background(), g, "", "new"); err == nil || len(g.Calls()) != 0 {
		t.Errorf("empty id: err %v, calls %d", err, len(g.Calls()))
	}
}
