package rex

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStream struct {
	kills atomic.Int32
	args  []string
}

func (f *fakeStream) fn(r io.ReadCloser, waitErr error) StreamFunc {
	return func(ctx context.Context, name string, args ...string) (*Stream, error) {
		f.args = append([]string{name}, args...)
		return &Stream{
			Stdout: r,
			Wait:   func() error { return waitErr },
			Kill:   func() { f.kills.Add(1) },
		}, nil
	}
}

func drain(t *testing.T, s *EventStream) []Event {
	t.Helper()
	var got []Event
	timeout := time.After(time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatal("channel did not close within a second")
		}
	}
}

// TestEvents: the cancel row is load-bearing: a stuck `rex events` must end
// the stream promptly, call kill and report no error. The wait-error row pins
// that a failed command is not silently reported as a clean end.
func TestEvents(t *testing.T) {
	waitErr := errors.New("exit status 1")
	cases := []struct {
		name    string
		input   string
		waitErr error
		want    []Event
		wantErr error
	}{
		{"two events then EOF",
			`{"type":"session_event","session_id":"s1"}` + "\n" + `{"type":"block_event","session_id":"s1","block_id":"b1"}` + "\n",
			nil,
			[]Event{{Type: "session_event", SessionID: "s1"}, {Type: "block_event", SessionID: "s1", BlockID: "b1"}}, nil},
		{"unknown type skipped",
			`{"type":"future_event","session_id":"s1"}` + "\n" + `{"type":"session_event","session_id":"s2"}` + "\n",
			nil, []Event{{Type: "session_event", SessionID: "s2"}}, nil},
		{"non-JSON line skipped",
			"not json\n\n" + `{"type":"block_event","session_id":"s1","block_id":"b9"}` + "\n",
			nil, []Event{{Type: "block_event", SessionID: "s1", BlockID: "b9"}}, nil},
		{"last line without newline",
			`{"type":"session_event","session_id":"s3"}`,
			nil, []Event{{Type: "session_event", SessionID: "s3"}}, nil},
		{"wait error after EOF",
			`{"type":"session_event","session_id":"s1"}` + "\n",
			waitErr, []Event{{Type: "session_event", SessionID: "s1"}}, waitErr},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			f := &fakeStream{}
			s := Events(context.Background(), f.fn(io.NopCloser(strings.NewReader(tc.input)), tc.waitErr), "rex")
			got := drain(t, s)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d events, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Type != w.Type || g.SessionID != w.SessionID || g.BlockID != w.BlockID {
					t.Errorf("event %d = %+v, want %+v", i, g, w)
				}
				if !strings.HasPrefix(string(g.Payload), "{") || strings.HasSuffix(string(g.Payload), "\n") {
					t.Errorf("event %d payload = %q", i, g.Payload)
				}
			}
			if !errors.Is(s.Err(), tc.wantErr) {
				t.Errorf("Err = %v, want %v", s.Err(), tc.wantErr)
			}
			if f.kills.Load() != 0 {
				t.Errorf("kill called %d times on a clean end", f.kills.Load())
			}
			if strings.Join(f.args, " ") != "rex events --all --json" {
				t.Errorf("args = %v", f.args)
			}
		})
	}

	t.Run("cancel", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pw.Close()
		f := &fakeStream{}
		ctx, cancel := context.WithCancel(context.Background())
		s := Events(ctx, f.fn(pr, errors.New("signal: killed")), "rex")
		go pw.Write([]byte(`{"type":"session_event","session_id":"s1"}` + "\n"))
		select {
		case <-s.Events():
		case <-time.After(time.Second):
			t.Fatal("no first event")
		}
		cancel()
		drain(t, s)
		if s.Err() != nil {
			t.Errorf("Err after cancel = %v, want nil", s.Err())
		}
		if f.kills.Load() != 1 {
			t.Errorf("kill called %d times, want 1", f.kills.Load())
		}
	})

	t.Run("start error", func(t *testing.T) {
		boom := errors.New("no rex")
		s := Events(context.Background(), func(context.Context, string, ...string) (*Stream, error) { return nil, boom }, "rex")
		drain(t, s)
		if !errors.Is(s.Err(), boom) {
			t.Errorf("Err = %v", s.Err())
		}
	})
}
