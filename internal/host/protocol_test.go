package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		msgType MessageType
		payload []byte
	}{
		{name: "data zero-length", msgType: Data, payload: nil},
		{name: "data small", msgType: Data, payload: []byte("hello")},
		{name: "data large (>64KB)", msgType: Data, payload: bytes.Repeat([]byte{0x42}, 70000)},
		{name: "resize zero-length", msgType: Resize, payload: nil},
		{name: "resize payload", msgType: Resize, payload: EncodeResize(24, 80)},
		{name: "detach zero-length", msgType: Detach, payload: nil},
		{name: "exit zero-length", msgType: Exit, payload: nil},
		{name: "stolen zero-length", msgType: Stolen, payload: nil},
		{name: "restart zero-length", msgType: Restart, payload: nil},
		{name: "restart result success (zero-length)", msgType: RestartResult, payload: nil},
		{name: "restart result failure (reason payload)", msgType: RestartResult, payload: []byte("status.json is not usable")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tt.msgType, tt.payload); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			gotType, gotPayload, err := ReadFrame(&buf)
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if gotType != tt.msgType {
				t.Errorf("type = %v, want %v", gotType, tt.msgType)
			}
			if len(tt.payload) == 0 {
				if len(gotPayload) != 0 {
					t.Errorf("payload = %v, want empty", gotPayload)
				}
			} else if !bytes.Equal(gotPayload, tt.payload) {
				t.Errorf("payload length %d, want %d (mismatch)", len(gotPayload), len(tt.payload))
			}
			if buf.Len() != 0 {
				t.Errorf("buffer has %d leftover bytes after ReadFrame", buf.Len())
			}
		})
	}
}

func TestReadFrameTruncated(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty stream", data: nil},
		{name: "partial header", data: []byte{byte(Data), 0x00}},
		{name: "header only, no payload", data: func() []byte {
			var buf bytes.Buffer
			_ = WriteFrame(&buf, Data, []byte("hello world"))
			return buf.Bytes()[:frameHeaderLen]
		}()},
		{name: "truncated payload", data: func() []byte {
			var buf bytes.Buffer
			_ = WriteFrame(&buf, Data, []byte("hello world"))
			full := buf.Bytes()
			return full[:len(full)-3]
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ReadFrame(bytes.NewReader(tt.data))
			if err == nil {
				t.Fatal("ReadFrame: expected error on truncated input, got nil")
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
				t.Errorf("ReadFrame error = %v, want wrapped io.EOF or io.ErrUnexpectedEOF", err)
			}
		})
	}
}

func TestReadFrameRejectsOversizePayload(t *testing.T) {
	header := []byte{byte(Data), 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[1:], MaxFramePayload+1)

	_, _, err := ReadFrame(bytes.NewReader(header))
	if err == nil {
		t.Fatal("ReadFrame: expected an error for a payload over MaxFramePayload, got nil")
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		t.Errorf("ReadFrame error = %v, want a MaxFramePayload rejection, not a truncation error", err)
	}
}

func TestResizeEncodeDecode(t *testing.T) {
	rows, cols, err := DecodeResize(EncodeResize(24, 80))
	if err != nil {
		t.Fatalf("DecodeResize: %v", err)
	}
	if rows != 24 || cols != 80 {
		t.Errorf("got rows=%d cols=%d, want rows=24 cols=80", rows, cols)
	}

	if _, _, err := DecodeResize([]byte{0x01, 0x02}); err == nil {
		t.Error("DecodeResize: expected error on wrong-length payload, got nil")
	}
}

func TestMessageTypeString(t *testing.T) {
	tests := []struct {
		mt   MessageType
		want string
	}{
		{Data, "Data"},
		{Resize, "Resize"},
		{Detach, "Detach"},
		{Exit, "Exit"},
		{Stolen, "Stolen"},
		{Restart, "Restart"},
		{RestartResult, "RestartResult"},
		{MessageType(99), "MessageType(99)"},
	}
	for _, tt := range tests {
		if got := tt.mt.String(); got != tt.want {
			t.Errorf("MessageType(%d).String() = %q, want %q", tt.mt, got, tt.want)
		}
	}
}
