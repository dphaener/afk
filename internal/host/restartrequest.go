package host

import (
	"errors"
	"fmt"
	"time"
)

// ErrRestartUnknown means a restart request was delivered (or may have been)
// but the host's answer never arrived: another client took the session first,
// the connection closed, or the wait ran out. The restart may or may not have
// happened, so the caller must not treat this as either success or refusal.
var ErrRestartUnknown = errors.New("the outcome of the restart request is not known")

// RequestRestart asks the session's host to restart its agent and waits for
// the host's answer. It is the one-shot counterpart to the dashboard's restart
// sender, for the daemon.
//
// The connection is short-lived on purpose: the caller is the daemon, which
// must not hold the session's only client slot, because any person running
// `afk attach` takes that slot and the daemon's supervision would end without
// anyone noticing. The connection is closed as soon as an answer (or the lack
// of one) is known. For its duration the request does take the slot from an
// attached client, who is told Stolen and attaches again; the session was
// frozen, which is why it is being restarted.
//
// It returns ("", nil) when the host restarted the agent, (reason, nil) when
// the host refused or failed and said why, and ("", ErrRestartUnknown) when a
// Stolen frame arrives, the connection closes first, or reply runs out. A dial
// or write failure is returned as an error naming the session. Data frames
// (child output and the host's "[afk]" lines) are skipped while waiting, as
// the dashboard does, because they are a rendering for panes, not the protocol.
func RequestRestart(worktree, name string, wait, reply time.Duration) (refused string, err error) {
	socket := ""
	if meta, merr := ReadMeta(worktree, name); merr == nil {
		socket = meta.Socket
	}
	if socket == "" {
		derived, derr := SocketPath(worktree, name)
		if derr != nil {
			return "", fmt.Errorf("could not reach session %s: %w", name, derr)
		}
		socket = derived
	}
	conn, err := Dial(socket, wait)
	if err != nil {
		return "", fmt.Errorf("could not reach session %s: %w", name, err)
	}
	defer conn.Close()

	_ = conn.SetWriteDeadline(time.Now().Add(inputWriteTimeout))
	if err := WriteFrame(conn, Restart, nil); err != nil {
		return "", fmt.Errorf("could not send the restart request to session %s: %w", name, err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(reply))
	for {
		typ, payload, err := ReadFrame(conn)
		if err != nil {
			return "", ErrRestartUnknown
		}
		switch typ {
		case RestartResult:
			return string(payload), nil
		case Stolen:
			return "", ErrRestartUnknown
		}
	}
}
