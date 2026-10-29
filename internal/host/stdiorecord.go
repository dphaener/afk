package host

import (
	"time"

	"github.com/dphaener/afk/internal/progstatus"
	"github.com/dphaener/afk/internal/rexlayout"
)

// StdioHeartbeat is how often an unchanged record is sent again. It is a
// safety net, not the mechanism: the record is sent on change and after the
// agent clears its own records, and the heartbeat only repairs a loss those
// two cannot see (a terminal that was redrawn or replaced under the host).
const StdioHeartbeat = 10 * time.Second

// StdioSafeWait is how long a due record may wait for an agent's escape
// sequence to end before the host logs it once. The host never closes or
// splits an agent's sequence to make room, so a sequence that never ends
// holds the record back; the log line is how a person finds out why.
const StdioSafeWait = 30 * time.Second

// PhaseView is the host's reading of status.json plus the owner-park flag.
// Readable is false when the file is missing or unparseable, which is stale
// and never an error.
type PhaseView struct {
	Readable      bool
	Phase         string
	AwaitingInput bool
	Parked        bool
}

// RecordFor is afk's own Program status record for a session in the state
// the files say. Not readable returns false: keep the last record and write
// nothing new, because a stale file says nothing about the session.
//
// The order matters: a failed phase beats awaiting_input (a failure is the
// more urgent thing to show), and done beats it too, since a finished session
// is not waiting for anyone.
func RecordFor(session string, v PhaseView) (progstatus.Record, bool) {
	if !v.Readable {
		return progstatus.Record{}, false
	}
	r := progstatus.Record{ID: "afk", App: "afk"}
	switch {
	case v.Phase == "failed":
		r.State = progstatus.StateError
		r.Msg = session + " failed"
	case v.Phase == "done":
		r.State = progstatus.StateDone
		r.Msg = session + ": done"
	case v.AwaitingInput || v.Parked:
		r.State = progstatus.StateBlocked
		r.Kind = progstatus.KindQuestion
		r.Msg = session + " is waiting for you"
	default:
		r.State = progstatus.StateWorking
		r.Msg = session + ": " + v.Phase
	}
	return r, true
}

// ShouldSend says whether afk's record goes into the terminal now: nothing
// has been sent yet, the record changed, the agent just cleared its records
// (a root clear removes ours with them), or the heartbeat is due.
func ShouldSend(last, next progstatus.Record, sent bool, since time.Duration, agentCleared bool) bool {
	return !sent || next != last || agentCleared || since >= StdioHeartbeat
}

// nextWindowLabel decides whether the Rex window gets a new label after one
// reading of status.json. An unreadable reading sends nothing and keeps the
// last label, for the same reason RecordFor keeps the last record: a stale
// file says nothing about the session. A label equal to the one last sent is
// not sent again, so an unchanged phase makes no Rex call at all.
func nextWindowLabel(session string, planning bool, v PhaseView, last string, sent bool) (string, bool) {
	if !v.Readable {
		return "", false
	}
	next := rexlayout.WindowLabel(session, v.Phase, planning)
	if sent && next == last {
		return "", false
	}
	return next, true
}

// closeWindowOnExit says whether the host closes its window as it exits. It
// does unless the phase is failed: a failed session's window is left open so
// its last screen can be read (docs/PLAN.md, T17.8 assumption A5). The phase
// is now's when status.json can be read at exit, else the last readable
// reading, and a session that never had a readable status.json closes, since
// nothing says it failed.
func closeWindowOnExit(now PhaseView, last *PhaseView) bool {
	phase := ""
	switch {
	case now.Readable:
		phase = now.Phase
	case last != nil:
		phase = last.Phase
	}
	return phase != "failed"
}
