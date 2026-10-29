package rex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"sync"
)

// Event is one Rex event this package understands. Type is "session_event" or
// "block_event"; SessionID and BlockID are the ids on the line (BlockID is
// empty for a session event). Payload is the whole line as Rex sent it, so a
// caller can read the fields Rex adds later without a change here.
type Event struct {
	Type      string
	SessionID string
	BlockID   string
	Payload   json.RawMessage
}

// Stream is a running `rex events` command: its stdout, a Wait that returns
// the command's exit error once it ends, and a Kill that stops it.
type Stream struct {
	Stdout io.ReadCloser
	Wait   func() error
	Kill   func()
}

// StreamFunc starts `bin events --all --json` (name and args are given) and
// returns the running stream. Tests pass a fake; NewStreamFunc is the real one.
type StreamFunc func(ctx context.Context, name string, args ...string) (*Stream, error)

// NewStreamFunc returns a StreamFunc that runs the command directly (no
// shell), with stdin on the null device. It has no deadline of its own: an
// event stream is meant to last, and the caller ends it by cancelling ctx.
func NewStreamFunc() StreamFunc {
	return func(ctx context.Context, name string, args ...string) (*Stream, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.WaitDelay = killGrace
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &Stream{
			Stdout: out,
			Wait:   cmd.Wait,
			Kill: func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			},
		}, nil
	}
}

// EventStream delivers Rex events on a channel that always closes.
type EventStream struct {
	ch  chan Event
	mu  sync.Mutex
	err error
}

// Events returns the channel of events. It is closed when the stream ends:
// at end of output, when ctx is cancelled, or on a read error.
func (s *EventStream) Events() <-chan Event { return s.ch }

// Err returns why the stream ended. It is only meaningful after the channel
// has closed: the command's wait error, or nil after a clean cancel.
func (s *EventStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Events starts `bin events --all --json` through run and decodes one JSON
// object per line. A line that is not JSON, or has a type other than
// session_event or block_event, is skipped, because Rex may add event kinds.
//
// The stream ends on cancel, not only at end of output, because a stuck
// `rex events` must never hold a caller: the dashboard draws on a ticker and
// must never wait on this. On cancel the command is killed and its stdout
// closed, which unblocks the reader, and Err is nil (a cancel is not a fault).
// Events itself never blocks; one goroutine is the only reader.
func Events(ctx context.Context, run StreamFunc, bin string) *EventStream {
	s := &EventStream{ch: make(chan Event)}
	go s.read(ctx, run, bin)
	return s
}

func (s *EventStream) finish(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	close(s.ch)
}

func (s *EventStream) read(ctx context.Context, run StreamFunc, bin string) {
	st, err := run(ctx, bin, "events", "--all", "--json")
	if err != nil {
		if ctx.Err() != nil {
			err = nil
		}
		s.finish(err)
		return
	}
	done := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			st.Kill()
			_ = st.Stdout.Close()
		case <-done:
		}
	}()

	br := bufio.NewReader(st.Stdout)
	for {
		line, rerr := br.ReadBytes('\n')
		if ev, ok := parseEvent(line); ok {
			select {
			case s.ch <- ev:
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			break
		}
		if rerr != nil {
			break
		}
	}
	close(done)
	<-watched
	werr := st.Wait()
	if ctx.Err() != nil {
		s.finish(nil)
		return
	}
	s.finish(werr)
}

// parseEvent decodes one line. Blank, non-JSON and unknown-type lines are not
// events.
func parseEvent(line []byte) (Event, bool) {
	var head struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		BlockID   string `json:"block_id"`
	}
	if json.Unmarshal(line, &head) != nil {
		return Event{}, false
	}
	switch head.Type {
	case "session_event", "block_event":
	default:
		return Event{}, false
	}
	payload := make([]byte, 0, len(line))
	payload = append(payload, line...)
	for len(payload) > 0 && (payload[len(payload)-1] == '\n' || payload[len(payload)-1] == '\r') {
		payload = payload[:len(payload)-1]
	}
	return Event{Type: head.Type, SessionID: head.SessionID, BlockID: head.BlockID, Payload: payload}, true
}
