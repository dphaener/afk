package host

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// listenForInput starts a unix listener whose address is recorded in a host
// record for (worktree, name), so SendInput finds it the way it finds a real
// host. It returns the worktree and the listener.
func listenForInput(t *testing.T, name string) (string, net.Listener) {
	t.Helper()
	worktree := t.TempDir()
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if err := WriteMeta(worktree, name, Meta{PID: 1, Socket: sock}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	return worktree, ln
}

func fastInput(t *testing.T) {
	t.Helper()
	oldPause, oldSettle, oldTimeout := inputSubmitPause, inputSettlePause, inputWriteTimeout
	inputSubmitPause, inputSettlePause = time.Millisecond, time.Millisecond
	t.Cleanup(func() { inputSubmitPause, inputSettlePause, inputWriteTimeout = oldPause, oldSettle, oldTimeout })
}

// TestSendInputReachesARealChild is the one test here that runs a real host
// with a real child on a real terminal, and it is load-bearing for the Enter:
// the child's `read` only returns once the carriage return has reached the
// terminal, so the file appears only if the whole line was submitted.
//
// It keeps the shipped pauses on purpose (no fastInput). The first end-to-end
// ship (docs/decisions.md, T2.2) found that with Detach sent straight after
// the Enter, the host dropped the Enter: its log said "discarding 1 bytes of
// input from a client that no longer holds the session", and the line sat
// typed but never submitted. The recording-listener tests below cannot see
// that, because they stand in for the host. With inputSettlePause set to zero
// this test fails.
func TestSendInputReachesARealChild(t *testing.T) {
	out := filepath.Join(t.TempDir(), "line")
	// The child writes the line it read to a temp name and renames it, so the
	// test never reads half a file, then stays alive as an agent would.
	h := startServe(t, `IFS= read -r line; printf '%s' "$line" > "`+out+`.tmp"; mv "`+out+`.tmp" "`+out+`"; exec cat`)

	const text = "Read ci-failure.md, fix the cause and commit. Do not push."
	if err := SendInput(h.cfg.Worktree, h.cfg.Name, text, time.Second); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if got := waitForFile(t, out); got != text {
		t.Errorf("the child read %q, want %q", got, text)
	}
	if h.returned() {
		t.Errorf("the host stopped after the line was sent: %v", h.err)
	}
	if data, err := os.ReadFile(h.cfg.LogPath); err == nil && strings.Contains(string(data), "discarding") {
		t.Errorf("the host discarded input:\n%s", data)
	}
}

// TestSendInputFrames is load-bearing for the order: text, then "\r", then
// Detach. Enter in its own frame is what keeps a terminal program from reading
// the pair as a paste.
func TestSendInputFrames(t *testing.T) {
	fastInput(t)
	worktree, ln := listenForInput(t, "s1")
	type got struct {
		typ     MessageType
		payload string
	}
	frames := make(chan got, 8)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			typ, p, err := ReadFrame(conn)
			if err != nil {
				close(frames)
				return
			}
			frames <- got{typ, string(p)}
		}
	}()

	if err := SendInput(worktree, "s1", "fix the\ttests", time.Second); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	want := []got{{Data, "fix the\ttests"}, {Data, "\r"}, {Detach, ""}}
	for i, w := range want {
		select {
		case g := <-frames:
			if g != w {
				t.Errorf("frame %d = %v %q, want %v %q", i, g.typ, g.payload, w.typ, w.payload)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("frame %d never arrived", i)
		}
	}
}

// TestSendInputErrors is load-bearing for "nothing hangs": every row must
// return an error, and the never-reads row proves the write deadline works
// because its text is larger than any unix socket buffer.
func TestSendInputErrors(t *testing.T) {
	fastInput(t)

	t.Run("no host record and no socket", func(t *testing.T) {
		err := SendInput(t.TempDir(), "ghost", "hello", 10*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Fatalf("err = %v, want one naming the session", err)
		}
	})

	t.Run("listener that never reads", func(t *testing.T) {
		inputWriteTimeout = 200 * time.Millisecond
		worktree, ln := listenForInput(t, "stuck")
		done := make(chan struct{})
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				<-done
				conn.Close()
			}
		}()
		defer close(done)
		big := strings.Repeat("a", MaxFramePayload)
		start := time.Now()
		err := SendInput(worktree, "stuck", big, time.Second)
		if err == nil || !strings.Contains(err.Error(), "stuck") {
			t.Fatalf("err = %v, want a write error naming the session", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("took %s; the write deadline did not bound it", elapsed)
		}
	})

	rejected := map[string]string{
		"empty":           "",
		"only spaces":     "   ",
		"newline":         "a\nb",
		"carriage return": "a\rb",
		"escape":          "a\x1b[A",
		"nul":             "a\x00b",
		"too long":        strings.Repeat("a", MaxFramePayload+1),
	}
	for name, text := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			// No host exists: a rejection must come before any dial.
			err := SendInput(t.TempDir(), "x", text, time.Hour)
			if err == nil || strings.Contains(err.Error(), "could not reach") {
				t.Fatalf("err = %v, want a rejection before dialling", err)
			}
		})
	}
}
