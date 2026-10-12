package host

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRequestExitEndsARealHost is load-bearing for the whole chain against the
// real serve loop: one Exit frame must make Serve return and take the child
// with it, and the host must remove its own socket, which is why the daemon
// does not signal.
func TestRequestExitEndsARealHost(t *testing.T) {
	requireUnixBind(t)
	cfg := serveConfigFor(t, "")
	pidPath := filepath.Join(cfg.Worktree, "child.pid")
	cfg.Argv = []string{"/bin/sh", "-c", fmt.Sprintf("echo $$ > %s; while true; do sleep 0.05; done", pidPath)}
	h := startServeWithConfig(t, cfg)
	pid := readPIDFile(t, pidPath)

	if err := RequestExit(cfg.Worktree, cfg.Name, time.Second); err != nil {
		t.Fatalf("RequestExit = %v, want nil", err)
	}
	if err := h.waitDone(t); err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
	awaitDead(t, pid)
	assertGone(t, cfg.Socket)
}

// TestRequestExitWithNoHost is load-bearing for the daemon's bounded wait: with
// nothing listening the call must return an error naming the session inside
// the wait, never block and never report success.
func TestRequestExitWithNoHost(t *testing.T) {
	const wait = 500 * time.Millisecond
	start := time.Now()
	err := RequestExit(t.TempDir(), "ghost", wait)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("got %v, want an error naming the session", err)
	}
	if elapsed := time.Since(start); elapsed > wait+time.Second {
		t.Errorf("took %s, want an answer within the %s wait", elapsed, wait)
	}
}

// TestRequestExitSendsOneExitFrameAndCloses is load-bearing for the frame type
// and for not holding the client slot after the request.
func TestRequestExitSendsOneExitFrameAndCloses(t *testing.T) {
	worktree, ln := listenForInput(t, "s1")
	type result struct {
		typ     MessageType
		closeOK bool
	}
	got := make(chan result, 1)
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
		_, _, err = ReadFrame(conn)
		got <- result{typ, err != nil}
	}()
	if err := RequestExit(worktree, "s1", time.Second); err != nil {
		t.Fatalf("RequestExit = %v", err)
	}
	select {
	case r := <-got:
		if r.typ != Exit || !r.closeOK {
			t.Errorf("host saw %v, closed=%v; want Exit then a closed connection", r.typ, r.closeOK)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the host never saw the request")
	}
}
