package host

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// TestRequestRestartAgainstAFakeHost is load-bearing for the mapping of each
// frame to a result: an empty RestartResult is the only success, a non-empty
// one is a refusal with a nil error (so the caller can tell "refused" from
// "unknown"), and Stolen, an early close and a timeout must all be
// ErrRestartUnknown, never a success. The Data row proves child output before
// the answer is skipped, not mistaken for it.
func TestRequestRestartAgainstAFakeHost(t *testing.T) {
	type frame struct {
		typ     MessageType
		payload string
	}
	cases := []struct {
		name        string
		frames      []frame
		holdOpen    bool // keep the connection open after the frames
		wantRefused string
		wantUnknown bool
	}{
		{name: "success", frames: []frame{{RestartResult, ""}}},
		{name: "refusal with a reason", frames: []frame{{RestartResult, "status.json not usable"}}, wantRefused: "status.json not usable"},
		{name: "stolen", frames: []frame{{Stolen, ""}}, holdOpen: true, wantUnknown: true},
		{name: "close before a result", frames: nil, wantUnknown: true},
		{name: "reply timeout", frames: nil, holdOpen: true, wantUnknown: true},
		{name: "data frames before the result", frames: []frame{{Data, "banner"}, {Data, "[afk] restarting"}, {RestartResult, ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worktree, ln := listenForInput(t, "s1")
			got := make(chan MessageType, 1)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				typ, _, err := ReadFrame(conn)
				if err != nil {
					return
				}
				got <- typ
				for _, f := range tc.frames {
					if err := WriteFrame(conn, f.typ, []byte(f.payload)); err != nil {
						return
					}
				}
				if tc.holdOpen {
					<-release
				}
			}()

			refused, err := RequestRestart(worktree, "s1", time.Second, 300*time.Millisecond)
			if tc.wantUnknown {
				if !errors.Is(err, ErrRestartUnknown) || refused != "" {
					t.Fatalf("got (%q, %v), want ErrRestartUnknown", refused, err)
				}
			} else if err != nil || refused != tc.wantRefused {
				t.Fatalf("got (%q, %v), want (%q, nil)", refused, err, tc.wantRefused)
			}
			select {
			case typ := <-got:
				if typ != Restart {
					t.Errorf("the host received a %v frame, want Restart", typ)
				}
			case <-time.After(time.Second):
				t.Error("the host never received a frame")
			}
		})
	}
}

// TestRequestRestartClosesItsConnection is load-bearing for the daemon not
// holding the client slot: after the answer the host must see the connection
// end.
func TestRequestRestartClosesItsConnection(t *testing.T) {
	worktree, ln := listenForInput(t, "s1")
	closed := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = ReadFrame(conn)
		_ = WriteFrame(conn, RestartResult, nil)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := ReadFrame(conn); err != nil {
			var ne net.Error
			if !(errors.As(err, &ne) && ne.Timeout()) {
				close(closed)
			}
		}
	}()
	if refused, err := RequestRestart(worktree, "s1", time.Second, time.Second); err != nil || refused != "" {
		t.Fatalf("got (%q, %v)", refused, err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the connection stayed open after the result")
	}
}

// TestRequestRestartWithNoListener is load-bearing for the error naming the
// session, which is all an owner sees in the daemon's log.
func TestRequestRestartWithNoListener(t *testing.T) {
	refused, err := RequestRestart(t.TempDir(), "ghost", 10*time.Millisecond, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "ghost") || errors.Is(err, ErrRestartUnknown) || refused != "" {
		t.Fatalf("got (%q, %v), want a dial error naming the session", refused, err)
	}
}

// TestRequestRestartRestartsARealHost is load-bearing for the whole chain
// against the real host: ("", nil) and a changed child, which a fake listener
// cannot show.
func TestRequestRestartRestartsARealHost(t *testing.T) {
	requireUnixBind(t)
	f := newRestartFixture(t)
	f.writeStatus(t, "executing", restartSessionID)
	compressRestartLadder(t)

	h := startServeWithConfig(t, f.cfg)
	oldPID := readPIDFile(t, f.oldPIDPath)

	refused, err := RequestRestart(f.cfg.Worktree, f.cfg.Name, time.Second, 20*time.Second)
	if err != nil || refused != "" {
		t.Fatalf("RequestRestart = (%q, %v), want (\"\", nil)", refused, err)
	}
	awaitDead(t, oldPID)
	if got, want := waitForFile(t, f.resumedArgsPath), "--resume "+restartSessionID; got != want {
		t.Errorf("the resumed child was run with %q, want %q", got, want)
	}
	_ = h
}
