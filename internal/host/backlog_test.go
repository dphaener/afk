package host

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"
)

// TestAppendFrame pins the enqueue-time merge writeLoop's backlog relies on.
// The load-bearing rows are "a non-Data frame is never merged across" (merging
// past a RestartResult would reorder it relative to the output around it) and
// "merges Data behind Data" (the whole fix: a backlog of tiny writes reaches the
// client as one frame, not thousands).
func TestAppendFrame(t *testing.T) {
	data := func(s string) outFrame { return outFrame{typ: Data, payload: []byte(s)} }
	rr := outFrame{typ: RestartResult, payload: []byte("r")}
	big := strings.Repeat("z", maxMergedFrame)

	tests := []struct {
		name string
		in   []outFrame
		want []outFrame
	}{
		{name: "merges Data behind Data", in: []outFrame{data("a"), data("b"), data("c")}, want: []outFrame{data("abc")}},
		{name: "a non-Data frame is never merged across", in: []outFrame{data("a"), rr, data("b")}, want: []outFrame{data("a"), rr, data("b")}},
		{name: "consecutive non-Data frames stay separate", in: []outFrame{rr, rr}, want: []outFrame{rr, rr}},
		{name: "a full frame starts a new one", in: []outFrame{data(big), data("b")}, want: []outFrame{data(big), data("b")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []outFrame
			for _, f := range tt.in {
				// Fresh copies: appendFrame may append to a payload in place.
				got = appendFrame(got, outFrame{typ: f.typ, payload: append([]byte(nil), f.payload...)})
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d frames, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].typ != tt.want[i].typ || !bytes.Equal(got[i].payload, tt.want[i].payload) {
					t.Errorf("frame %d = %v %d bytes, want %v %d bytes", i, got[i].typ, len(got[i].payload), tt.want[i].typ, len(tt.want[i].payload))
				}
			}
		})
	}
}

// TestClientConnExitOnlyWhenTheSessionEnds pins the wire contract the
// dashboard's DetachDropped handling rests on: endAndWait (shutdown) follows
// the queued output with an Exit frame, and a plain close (a detach, or a
// dropped slow client) is a bare EOF. Both halves are load-bearing — an Exit on
// a drop would make the dashboard pin a live host as dead again.
func TestClientConnExitOnlyWhenTheSessionEnds(t *testing.T) {
	tests := []struct {
		name     string
		closeFn  func(c *clientConn)
		wantExit bool
	}{
		{name: "shutdown sends Exit after the output", closeFn: func(c *clientConn) { c.endAndWait(testBudget) }, wantExit: true},
		{name: "a plain close is a bare EOF", closeFn: func(c *clientConn) { c.close() }, wantExit: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, client := net.Pipe()
			t.Cleanup(func() { _ = client.Close() })
			c := newClientConn(server, log.New(io.Discard, "", 0))
			if !c.send(Data, []byte("final output")) {
				t.Fatal("send should have been queued")
			}
			go tt.closeFn(c)

			if err := client.SetReadDeadline(time.Now().Add(testBudget)); err != nil {
				t.Fatalf("SetReadDeadline: %v", err)
			}
			typ, payload, err := ReadFrame(client)
			if err != nil || typ != Data || string(payload) != "final output" {
				t.Fatalf("first frame = (%v, %q, %v), want the queued Data", typ, payload, err)
			}
			typ, _, err = ReadFrame(client)
			switch {
			case tt.wantExit && (err != nil || typ != Exit):
				t.Fatalf("after the output got (%v, %v), want an Exit frame", typ, err)
			case !tt.wantExit && err == nil:
				t.Fatalf("after the output got a %v frame, want EOF", typ)
			case !tt.wantExit && !errors.Is(err, io.EOF):
				t.Fatalf("after the output got %v, want EOF", err)
			}
		})
	}
}

// TestServeKeepsASlowClientThroughATinyWriteFlood is the regression test for
// the codex attach drop: a child that repaints in thousands of one-byte writes
// must not get a client dropped merely for paying a little per frame. The
// client here sleeps per frame read; with a frame-count bound (the old
// 256-slot queue) the backlog overflowed and the host closed the connection
// mid-flood.
func TestServeKeepsASlowClientThroughATinyWriteFlood(t *testing.T) {
	h := startServe(t, `read go
i=0
while [ $i -lt 5000 ]; do printf x; i=$((i+1)); done
printf 'FLOOD-END\n'
exec cat`)
	conn := h.dial(t)
	sendFrame(t, conn, Resize, EncodeResize(24, 80))
	sendFrame(t, conn, Data, []byte("go\n"))

	var got strings.Builder
	deadline := time.Now().Add(testBudget)
	for !strings.Contains(got.String(), "FLOOD-END") {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		typ, payload, err := ReadFrame(conn)
		if err != nil {
			t.Fatalf("connection ended mid-flood after %d bytes: %v (the host dropped a client that was reading)", got.Len(), err)
		}
		if typ != Data {
			t.Fatalf("got %v frame mid-flood", typ)
		}
		got.Write(payload)
		time.Sleep(time.Millisecond)
	}
}
