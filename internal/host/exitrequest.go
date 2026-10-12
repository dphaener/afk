package host

import (
	"fmt"
	"time"
)

// RequestExit asks the session's host to end its agent and then itself. It is
// the one-shot counterpart to RequestRestart, for the daemon: it dials, writes
// a single Exit frame and closes. The host's serve loop already answers that
// frame by terminating its child and returning, so this adds no frame type and
// changes nothing in the host.
//
// A nil return means only that the frame was written. The caller learns that
// the host is really gone from host.json (and the host pid) on a later look,
// not from this call: the host acts on the frame after this function has
// returned, and it sends no answer to wait for.
//
// A daemon uses this rather than a signal because it is the same orderly path
// `afk clean` takes (internal/session dialExit). The host ends the child the
// way it ends it for a person, and removes its own socket and host.json
// itself, so nothing is left for the daemon to tidy.
//
// The socket is found the way RequestRestart finds it: host.json's socket when
// it can be read, else the path derived from the worktree and name. wait bounds
// the dial. An error naming the session is returned when there is no socket to
// dial or the write fails.
func RequestExit(worktree, name string, wait time.Duration) error {
	socket := ""
	if meta, merr := ReadMeta(worktree, name); merr == nil {
		socket = meta.Socket
	}
	if socket == "" {
		derived, derr := SocketPath(worktree, name)
		if derr != nil {
			return fmt.Errorf("could not reach session %s: %w", name, derr)
		}
		socket = derived
	}
	conn, err := Dial(socket, wait)
	if err != nil {
		return fmt.Errorf("could not reach session %s: %w", name, err)
	}
	defer conn.Close()

	_ = conn.SetWriteDeadline(time.Now().Add(wait))
	if err := WriteFrame(conn, Exit, nil); err != nil {
		return fmt.Errorf("could not send the exit request to session %s: %w", name, err)
	}
	return nil
}
