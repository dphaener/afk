// Package host implements the afk session multiplexer, unix-only: the wire
// protocol between a detachable host process and its attached client
// (protocol.go), socket path derivation (socket.go), host metadata
// persistence (meta.go), the shared `afk attach` exit-code contract
// (exit.go), Serve — the detached `afk _host` process owning the child's
// PTY (host.go) — and the client-side dialing (client.go's Dial, plus
// Attach, the raw-terminal client no afk command runs today: both
// `afk attach` and the dashboard render through internal/ui, which dials
// with Dial and drives the frame protocol itself via hostChild in
// internal/ui/hostpane.go).
package host

import (
	"encoding/binary"
	"fmt"
	"io"
)

// MessageType identifies the kind of frame on the wire. Data carries PTY
// bytes in both directions; Resize, Detach, Exit, and Restart are
// client→host control messages; Stolen is host→client, sent when another
// client attaches so the current client knows to restore its terminal and
// return.
type MessageType byte

const (
	// Data carries raw PTY bytes, in either direction.
	Data MessageType = iota + 1
	// Resize carries a new terminal size (client→host). Payload is the
	// 4-byte encoding produced by EncodeResize.
	Resize
	// Detach requests that the client cleanly detach (client→host).
	Detach
	// Exit requests that the host terminate the child process and itself
	// (client→host).
	Exit
	// Stolen notifies the client that another client has attached and it
	// must restore its terminal and return (host→client).
	Stolen
	// Restart requests that the host restart the child process
	// (client→host).
	Restart
	// RestartResult reports how a requested restart concluded (host→client):
	// an empty payload means the child was respawned, a non-empty payload is
	// the human-readable reason it was refused or failed. It exists because
	// the requester is not always a pane watching the session's output — the
	// dashboard's restart trigger dials, sends one Restart, and needs a typed
	// answer rather than having to infer one from Data-frame silence.
	RestartResult
)

// String renders t for logs/debugging.
func (t MessageType) String() string {
	switch t {
	case Data:
		return "Data"
	case Resize:
		return "Resize"
	case Detach:
		return "Detach"
	case Exit:
		return "Exit"
	case Stolen:
		return "Stolen"
	case Restart:
		return "Restart"
	case RestartResult:
		return "RestartResult"
	default:
		return fmt.Sprintf("MessageType(%d)", byte(t))
	}
}

// frameHeaderLen is the fixed header size: 1-byte type + 4-byte big-endian
// payload length.
const frameHeaderLen = 5

// MaxFramePayload bounds the length a ReadFrame caller will allocate for one
// frame's payload. Every real payload on this wire is small (PTY chunks,
// resize pairs); without this bound, a corrupt or hostile 4-byte length
// prefix could make ReadFrame allocate up to 4GiB before the truncation
// check ever fires. 1MiB comfortably covers ptyReadBufSize (32KB) and this
// package's own >64KB round-trip test with room to spare.
const MaxFramePayload = 1 << 20 // 1 MiB

// WriteFrame writes one framed message to w: a 1-byte type, a 4-byte
// big-endian payload length, then the payload bytes. payload may be nil or
// empty (a 0-length frame). It operates on a plain io.Writer so callers can
// use it over a net.Conn, a pipe, or an in-memory buffer in tests.
func WriteFrame(w io.Writer, t MessageType, payload []byte) error {
	header := make([]byte, frameHeaderLen)
	header[0] = byte(t)
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("writing frame header: %w", err)
	}
	if len(payload) == 0 {
		return nil
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("writing frame payload: %w", err)
	}
	return nil
}

// ReadFrame reads one framed message from r. It returns an error (wrapping
// io.EOF/io.ErrUnexpectedEOF) if r is closed or truncated mid-frame — a
// partial header or a payload shorter than the declared length are both
// truncation errors, never a partial MessageType/payload. A declared length
// over MaxFramePayload is rejected before the allocation, so a corrupt or
// hostile length prefix cannot make ReadFrame allocate an arbitrary amount
// of memory.
func ReadFrame(r io.Reader) (MessageType, []byte, error) {
	header := make([]byte, frameHeaderLen)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, fmt.Errorf("reading frame header: %w", err)
	}
	t := MessageType(header[0])
	n := binary.BigEndian.Uint32(header[1:])
	if n == 0 {
		return t, nil, nil
	}
	if n > MaxFramePayload {
		return 0, nil, fmt.Errorf("frame payload of %d bytes exceeds MaxFramePayload (%d)", n, MaxFramePayload)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, fmt.Errorf("reading frame payload (%d bytes): %w", n, err)
	}
	return t, payload, nil
}

// resizePayloadLen is the fixed Resize payload size: two big-endian uint16s
// (rows, cols).
const resizePayloadLen = 4

// EncodeResize encodes a terminal size as a Resize frame payload.
func EncodeResize(rows, cols uint16) []byte {
	b := make([]byte, resizePayloadLen)
	binary.BigEndian.PutUint16(b[0:2], rows)
	binary.BigEndian.PutUint16(b[2:4], cols)
	return b
}

// DecodeResize decodes a Resize frame payload produced by EncodeResize.
func DecodeResize(payload []byte) (rows, cols uint16, err error) {
	if len(payload) != resizePayloadLen {
		return 0, 0, fmt.Errorf("resize payload must be %d bytes, got %d", resizePayloadLen, len(payload))
	}
	rows = binary.BigEndian.Uint16(payload[0:2])
	cols = binary.BigEndian.Uint16(payload[2:4])
	return rows, cols, nil
}
