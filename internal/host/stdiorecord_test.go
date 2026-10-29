package host

import (
	"testing"
	"time"

	"github.com/dphaener/afk/internal/progstatus"
)

// TestRecordFor: "failed beats awaiting_input" and "done beats awaiting_input"
// are load-bearing (the more urgent state must win), "parked alone" proves the
// owner-park flag blocks without a status.json flag, and "unreadable" proves a
// stale file writes nothing. Every returned record must encode, since the
// terminal discards a malformed one silently.
func TestRecordFor(t *testing.T) {
	tests := []struct {
		name      string
		v         PhaseView
		wantOK    bool
		wantState progstatus.State
		wantKind  progstatus.Kind
		wantMsg   string
	}{
		{"unreadable", PhaseView{Phase: "failed"}, false, "", "", ""},
		{"planning", PhaseView{Readable: true, Phase: "planning"}, true, progstatus.StateWorking, "", "s1: planning"},
		{"running", PhaseView{Readable: true, Phase: "running"}, true, progstatus.StateWorking, "", "s1: running"},
		{"reviewing", PhaseView{Readable: true, Phase: "reviewing"}, true, progstatus.StateWorking, "", "s1: reviewing"},
		{"empty phase", PhaseView{Readable: true}, true, progstatus.StateWorking, "", "s1: "},
		{"done", PhaseView{Readable: true, Phase: "done"}, true, progstatus.StateDone, "", "s1: done"},
		{"failed", PhaseView{Readable: true, Phase: "failed"}, true, progstatus.StateError, "", "s1 failed"},
		{"awaiting input", PhaseView{Readable: true, Phase: "running", AwaitingInput: true}, true, progstatus.StateBlocked, progstatus.KindQuestion, "s1 is waiting for you"},
		{"parked alone", PhaseView{Readable: true, Phase: "running", Parked: true}, true, progstatus.StateBlocked, progstatus.KindQuestion, "s1 is waiting for you"},
		{"failed beats awaiting_input", PhaseView{Readable: true, Phase: "failed", AwaitingInput: true}, true, progstatus.StateError, "", "s1 failed"},
		{"done beats awaiting_input", PhaseView{Readable: true, Phase: "done", AwaitingInput: true, Parked: true}, true, progstatus.StateDone, "", "s1: done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := RecordFor("s1", tt.v)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.State != tt.wantState || got.Kind != tt.wantKind || got.Msg != tt.wantMsg {
				t.Errorf("got state=%q kind=%q msg=%q, want %q %q %q", got.State, got.Kind, got.Msg, tt.wantState, tt.wantKind, tt.wantMsg)
			}
			if got.ID != "afk" || got.App != "afk" {
				t.Errorf("ID=%q App=%q, want afk afk", got.ID, got.App)
			}
			if _, err := progstatus.Encode(got); err != nil {
				t.Errorf("Encode: %v", err)
			}
		})
	}
}

// TestShouldSend: each condition alone must send, and "unchanged inside the
// heartbeat" is the row that keeps the host from rewriting the terminal on
// every tick.
func TestShouldSend(t *testing.T) {
	a := progstatus.Record{State: progstatus.StateWorking, ID: "afk", App: "afk", Msg: "s: running"}
	changedMsg := a
	changedMsg.Msg = "s: reviewing"
	changedState := a
	changedState.State = progstatus.StateDone
	tests := []struct {
		name    string
		last    progstatus.Record
		next    progstatus.Record
		sent    bool
		since   time.Duration
		cleared bool
		want    bool
	}{
		{"first send", progstatus.Record{}, a, false, 0, false, true},
		{"unchanged inside heartbeat", a, a, true, StdioHeartbeat - time.Nanosecond, false, false},
		{"unchanged at heartbeat", a, a, true, StdioHeartbeat, false, true},
		{"unchanged after heartbeat", a, a, true, StdioHeartbeat + time.Second, false, true},
		{"changed msg", a, changedMsg, true, time.Second, false, true},
		{"changed state", a, changedState, true, time.Second, false, true},
		{"agent clear", a, a, true, time.Second, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldSend(tt.last, tt.next, tt.sent, tt.since, tt.cleared); got != tt.want {
				t.Errorf("ShouldSend = %v, want %v", got, tt.want)
			}
		})
	}
}
