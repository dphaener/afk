package host

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// inputWriteTimeout bounds the whole of SendInput's writing. A package
// variable, not a constant, so a test can shorten it.
var inputWriteTimeout = 5 * time.Second

// inputSubmitPause separates the text frame from the Enter frame. A terminal
// program can treat text and a carriage return that arrive in one read as a
// paste and insert a line break instead of submitting. Whether the pause is
// what makes a real agent submit has not been observed: the end-to-end run
// (docs/decisions.md, T2.2) typed into a shell script, which reads a line
// whenever the Enter arrives, pause or no pause. A package variable so tests
// need not wait.
var inputSubmitPause = 200 * time.Millisecond

// inputSettlePause is how long SendInput keeps the session after the Enter
// before it detaches. Without it the Enter is lost, and it looks as if it
// could not be: the Enter is written to the socket before the Detach.
//
// The reason is in host.go. readClient hands a Data frame to a queue
// (queueInput) that a second goroutine, writeChild, empties into the terminal,
// and sends a Detach to the serve loop, which drops the client at once. When
// writeChild takes a chunk off the queue it asks whether the client that typed
// it still holds the session (writeInput, classifyInput) and discards the
// chunk if not. So an Enter queued a moment before the Detach races the drop,
// and usually loses. The first end-to-end ship (docs/decisions.md, T2.2) saw
// exactly that: host.log said "discarding 1 bytes of input from a client that
// no longer holds the session", and the prompt sat typed and never submitted.
//
// The pause lets writeChild write the Enter while this client is still the
// current one. It is a margin, not a handshake (the protocol has no "written"
// reply): writeChild only has to wake and make one write, which takes far less
// than this unless the child has stopped reading its terminal, and then no
// pause would help. A package variable so tests need not wait.
var inputSettlePause = 200 * time.Millisecond

// SendInput types one line of text into the session's terminal and presses
// Enter, then lets go. It is the one-shot counterpart to Attach for a caller
// that only needs to say something to the session (the daemon handing a red CI
// back to the agent).
//
// It reads the socket from the host record, falling back to the derived
// SocketPath the way the dashboard's pane does, dials, writes the text as one
// Data frame, pauses, writes "\r" as a second Data frame, pauses again so the
// host writes the Enter before it sees the Detach (inputSettlePause), then
// sends Detach and closes. Empty text, a newline, or a control character below 0x20 other than
// tab is rejected before anything is dialled, because a newline would submit
// early and other control bytes are keystrokes, not text.
//
// This takes the session's single client slot: a dashboard pane or
// `afk attach` on the same session is told Stolen, exactly as the dashboard's
// restart does. The host also drops input when its queue is full (queueInput),
// so a nil error means "written to the socket", not "read by the agent"; the
// caller confirms from the session's outcome.
//
// It never reads from the connection, which is safe for three reasons read in
// host.go. Output the host sends meanwhile goes into a bounded per-client
// queue (clientPendingLimit); a client that falls behind is dropped, not
// waited for. A single blocked frame write is cut off by clientWriteTimeout.
// And readClient handles our Data and Detach frames independent of whether we
// read anything: Detach reaches the serve loop, which calls dropClient. Closing
// our end then costs the host nothing.
func SendInput(worktree, name, text string, wait time.Duration) error {
	if err := checkInputText(text); err != nil {
		return err
	}
	socket, err := inputSocket(worktree, name)
	if err != nil {
		return err
	}
	conn, err := Dial(socket, wait)
	if err != nil {
		return fmt.Errorf("could not reach session %s: %w", name, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(inputWriteTimeout)
	_ = conn.SetWriteDeadline(deadline)
	if err := WriteFrame(conn, Data, []byte(text)); err != nil {
		return fmt.Errorf("could not send text to session %s: %w", name, err)
	}
	time.Sleep(inputSubmitPause)
	if err := WriteFrame(conn, Data, []byte("\r")); err != nil {
		return fmt.Errorf("could not press Enter in session %s: %w", name, err)
	}
	time.Sleep(inputSettlePause)
	if err := WriteFrame(conn, Detach, nil); err != nil {
		return fmt.Errorf("could not detach from session %s: %w", name, err)
	}
	return nil
}

// inputSocket finds the socket of a session's host: the host record's, falling
// back to the derived SocketPath the way the dashboard's pane does. SendInput
// and SendKeys share it so they can never reach different hosts.
func inputSocket(worktree, name string) (string, error) {
	if meta, err := ReadMeta(worktree, name); err == nil && meta.Socket != "" {
		return meta.Socket, nil
	}
	derived, err := SocketPath(worktree, name)
	if err != nil {
		return "", fmt.Errorf("could not reach session %s: %w", name, err)
	}
	return derived, nil
}

// SendKeys writes keystrokes into the session's terminal, one Data frame per
// chunk, then lets go. It is SendInput for a caller that has already built the
// bytes: a bracketed paste and its Enter, or the keys that answer a dialog.
// Unlike SendInput it does not police them, so escape sequences pass through.
//
// The pauses are SendInput's, for the reasons given at inputSubmitPause and
// inputSettlePause: inputSubmitPause between chunks, so a terminal program
// does not read two chunks as one paste, and inputSettlePause after the last,
// so the host writes it before it sees the Detach. The whole send shares one
// write deadline of inputWriteTimeout, and the connection is never read, as in
// SendInput. It never sends Exit, Restart or Resize.
//
// Like SendInput it takes the session's single client slot, and a nil error
// means "written to the socket", not "read by the agent". An empty list, an
// empty chunk or a chunk over MaxFramePayload is refused before dialling.
func SendKeys(worktree, name string, chunks [][]byte, wait time.Duration) error {
	if len(chunks) == 0 {
		return errors.New("nothing to send: no keystrokes were given")
	}
	for i, c := range chunks {
		if len(c) == 0 {
			return fmt.Errorf("nothing to send: keystroke chunk %d is empty", i+1)
		}
		if len(c) > MaxFramePayload {
			return fmt.Errorf("keystroke chunk %d is %d bytes; at most %d can be sent at once", i+1, len(c), MaxFramePayload)
		}
	}
	socket, err := inputSocket(worktree, name)
	if err != nil {
		return err
	}
	conn, err := Dial(socket, wait)
	if err != nil {
		return fmt.Errorf("could not reach session %s: %w", name, err)
	}
	defer conn.Close()

	_ = conn.SetWriteDeadline(time.Now().Add(inputWriteTimeout))
	for i, c := range chunks {
		if i > 0 {
			time.Sleep(inputSubmitPause)
		}
		if err := WriteFrame(conn, Data, c); err != nil {
			return fmt.Errorf("could not send keys to session %s: %w", name, err)
		}
	}
	time.Sleep(inputSettlePause)
	if err := WriteFrame(conn, Detach, nil); err != nil {
		return fmt.Errorf("could not detach from session %s: %w", name, err)
	}
	return nil
}

// checkInputText applies SendInput's rules for what may be typed.
func checkInputText(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("nothing to send: the text is empty")
	}
	if len(text) > MaxFramePayload {
		return fmt.Errorf("the text is %d bytes; at most %d can be sent at once", len(text), MaxFramePayload)
	}
	for _, r := range text {
		if r < 0x20 && r != '\t' {
			return fmt.Errorf("the text has a newline or control character (0x%02x); send one plain line", r)
		}
	}
	return nil
}
