package rex

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestCheckServer: revision_0 is load-bearing (the login-server proxy reports
// 0, so it must read as unknown, not too old); revision_6 pins the floor;
// missing_key and not_json pin that a broken reply never becomes revision 0.
func TestCheckServer(t *testing.T) {
	cases := []struct {
		name         string
		reply        string
		callErr      error
		wantRev      int
		wantSessions int
		wantTooOld   bool
		wantErr      string
		wantCallErr  bool
	}{
		{name: "revision 7", reply: `{"api_revision":7,"instance_id":"i1","session_count":2}`, wantRev: 7, wantSessions: 2},
		{name: "revision 8", reply: `{"api_revision":8,"instance_id":"i1","session_count":0}`, wantRev: 8},
		{name: "revision 6", reply: `{"api_revision":6}`, wantRev: 6, wantTooOld: true, wantErr: "needs revision 7"},
		{name: "revision 0", reply: `{"api_revision":0}`},
		{name: "missing key", reply: `{"instance_id":"i1"}`, wantErr: "no api_revision"},
		{name: "not json", reply: `hello`, wantErr: "not usable JSON"},
		{name: "call error", callErr: &CallError{Method: "server.status", Code: 1, Stderr: "boom"}, wantCallErr: true, wantErr: "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := (&Fake{}).On("server.status", tc.reply, tc.callErr)
			s, err := CheckServer(context.Background(), f)
			if errors.Is(err, ErrTooOld) != tc.wantTooOld {
				t.Fatalf("ErrTooOld = %v, err %v", errors.Is(err, ErrTooOld), err)
			}
			var ce *CallError
			if errors.As(err, &ce) != tc.wantCallErr {
				t.Fatalf("CallError = %v, err %v", errors.As(err, &ce), err)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want text %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if err == nil || tc.wantTooOld {
				if s.Revision != tc.wantRev || s.Sessions != tc.wantSessions {
					t.Fatalf("got %+v", s)
				}
			}
			if calls := f.Calls(); len(calls) != 1 || calls[0].Method != "server.status" || string(calls[0].Args) != "{}" {
				t.Fatalf("calls = %+v", calls)
			}
		})
	}
}
